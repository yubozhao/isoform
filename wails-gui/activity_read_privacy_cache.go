package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Cache only immutable source facts, never a privacy decision. The existing
// source-revision triggers cover event/snapshot inserts, updates, moves and
// deletes. Current session metadata is checked on every lookup as well.
const activityPrivacySourceCacheLimit = 32768

type activityPrivacySourceState struct {
	db                           *sql.DB
	start, end, revision, schema int64
	apps, status, meeting        string
}

type activityPrivacyPeopleSessionFact struct {
	session ActivitySessionRow
	state   activityPrivacySourceState
}

type activityPrivacySnapshotInput struct {
	app, browserURL, parsed string
}

type activityPrivacyProvenanceInputs struct {
	eventData []string
	snapshots []activityPrivacySnapshotInput
}

type activityPrivacyCachedSource struct {
	state  activityPrivacySourceState
	source activitySourceProvenance
	used   uint64
}

type activityPrivacySourceCache struct {
	mu            sync.Mutex
	entries       map[string]activityPrivacyCachedSource
	clock         uint64
	schemaDB      *sql.DB
	schema        int64
	metadataReads atomic.Uint64
	sourceScans   atomic.Uint64
	eventRows     atomic.Uint64
	snapshotRows  atomic.Uint64
}

type activityPrivacyQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *ActivityStore) activityPrivacySourceState(q activityPrivacyQueryer, id string) (activityPrivacySourceState, error) {
	return s.activityPrivacySourceStateContext(context.Background(), q, id)
}

func (s *ActivityStore) activityPrivacySourceStateContext(ctx context.Context, q activityPrivacyQueryer, id string) (activityPrivacySourceState, error) {
	state := activityPrivacySourceState{db: s.db}
	s.readPrivacySources.metadataReads.Add(1)
	err := q.QueryRowContext(ctx, `SELECT s.start_time,s.end_time,COALESCE(s.app_names,''),COALESCE(s.triage_status,''),COALESCE(s.meeting_app,''),COALESCE(r.revision,0),
        (SELECT schema_version FROM pragma_schema_version)
        FROM activity_sessions s LEFT JOIN activity_evidence_source_revisions r ON r.session_id=s.id WHERE s.id=?`, id).
		Scan(&state.start, &state.end, &state.apps, &state.status, &state.meeting, &state.revision, &state.schema)
	return state, err
}

// batchActivityPrivacySourceStatesContext reads the privacy state of ids in
// one query; an id without a session row is absent from the result.
func (s *ActivityStore) batchActivityPrivacySourceStatesContext(ctx context.Context, q activityPrivacyQueryer, ids []string) (map[string]activityPrivacySourceState, error) {
	out := make(map[string]activityPrivacySourceState, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	s.readPrivacySources.metadataReads.Add(uint64(len(ids)))
	rows, err := q.QueryContext(ctx, `SELECT s.id,s.start_time,s.end_time,COALESCE(s.app_names,''),COALESCE(s.triage_status,''),COALESCE(s.meeting_app,''),COALESCE(r.revision,0),
        (SELECT schema_version FROM pragma_schema_version)
        FROM activity_sessions s LEFT JOIN activity_evidence_source_revisions r ON r.session_id=s.id
        WHERE s.id IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		state := activityPrivacySourceState{db: s.db}
		if err := rows.Scan(&id, &state.start, &state.end, &state.apps, &state.status, &state.meeting, &state.revision, &state.schema); err != nil {
			return nil, err
		}
		out[id] = state
	}
	return out, rows.Err()
}

func (s *ActivityStore) batchActivityPrivacyPeopleSessionFactsContext(ctx context.Context, q activityPrivacyQueryer, ids []string) (map[string]activityPrivacyPeopleSessionFact, error) {
	out := make(map[string]activityPrivacyPeopleSessionFact, len(ids))
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		s.readPrivacySources.metadataReads.Add(uint64(len(chunk)))
		rows, err := q.QueryContext(ctx, `SELECT s.id,s.start_time,s.end_time,COALESCE(s.app_names,''),COALESCE(s.triage_status,''),COALESCE(s.meeting_app,''),COALESCE(s.transcript_path,''),COALESCE(r.revision,0),
            (SELECT schema_version FROM pragma_schema_version)
            FROM activity_sessions s LEFT JOIN activity_evidence_source_revisions r ON r.session_id=s.id
            WHERE s.id IN (`+placeholders+`) ORDER BY s.id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var fact activityPrivacyPeopleSessionFact
			fact.state.db = s.db
			if err := rows.Scan(&fact.session.ID, &fact.session.StartTime, &fact.session.EndTime, &fact.session.AppNames,
				&fact.session.TriageStatus, &fact.session.MeetingApp, &fact.session.TranscriptPath,
				&fact.state.revision, &fact.state.schema); err != nil {
				rows.Close()
				return nil, err
			}
			fact.state.start = fact.session.StartTime
			fact.state.end = fact.session.EndTime
			fact.state.apps = fact.session.AppNames
			fact.state.status = fact.session.TriageStatus
			fact.state.meeting = fact.session.MeetingApp
			out[fact.session.ID] = fact
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// cachedActivitySessionProvenanceContext never falls back to raw events or
// snapshots. A missing or stale verified fact withholds the derivative.
func (s *ActivityStore) cachedActivitySessionProvenanceContext(ctx context.Context, id string) (activitySourceProvenance, bool, error) {
	state := activityPrivacySourceState{db: s.db}
	s.readPrivacySources.metadataReads.Add(1)
	err := s.reader().QueryRowContext(ctx, `SELECT s.start_time,s.end_time,COALESCE(s.app_names,''),COALESCE(s.triage_status,''),COALESCE(s.meeting_app,''),COALESCE(r.revision,0),
        (SELECT schema_version FROM pragma_schema_version)
        FROM activity_sessions s LEFT JOIN activity_evidence_source_revisions r ON r.session_id=s.id WHERE s.id=?`, id).
		Scan(&state.start, &state.end, &state.apps, &state.status, &state.meeting, &state.revision, &state.schema)
	if err != nil {
		return activitySourceProvenance{}, false, err
	}
	cache := &s.readPrivacySources
	cache.mu.Lock()
	valid := cache.schemaDB == s.db && cache.schema == state.schema
	cache.mu.Unlock()
	if !valid {
		return activitySourceProvenance{}, false, nil
	}
	source, found := s.cachedActivitySessionProvenance(id, state)
	return source, found, ctx.Err()
}

func (s *ActivityStore) validateActivityPrivacySourceSchemaContext(ctx context.Context, q activityPrivacyQueryer, state activityPrivacySourceState) error {
	cache := &s.readPrivacySources
	cache.mu.Lock()
	valid := cache.schemaDB == s.db && cache.schema == state.schema
	cache.mu.Unlock()
	if valid {
		return nil
	}
	var triggers int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND tbl_name IN ('activity_events','activity_snapshots') AND name IN (
        'evidence_revision_activity_events_INSERT','evidence_revision_activity_events_UPDATE','evidence_revision_activity_events_DELETE',
        'evidence_revision_activity_snapshots_INSERT','evidence_revision_activity_snapshots_UPDATE','evidence_revision_activity_snapshots_DELETE')`).Scan(&triggers); err != nil {
		return err
	}
	if triggers != 6 {
		return errors.New("activity privacy source revision triggers unavailable")
	}
	// Resolve the actual source columns without scanning any source rows.
	rows, err := q.QueryContext(ctx, `SELECT data FROM activity_events WHERE 0 UNION ALL SELECT app || parsed_json FROM activity_snapshots WHERE 0`)
	if err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	cache.mu.Lock()
	cache.schemaDB, cache.schema = s.db, state.schema
	cache.mu.Unlock()
	return nil
}

func (s *ActivityStore) batchActivityPrivacyProvenanceInputsWithQueryer(ctx context.Context, q activityPrivacyQueryer, ids []string) (map[string]activityPrivacyProvenanceInputs, error) {
	out := make(map[string]activityPrivacyProvenanceInputs, len(ids))
	for _, id := range ids {
		out[id] = activityPrivacyProvenanceInputs{}
	}
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, `SELECT session_id, data FROM activity_events WHERE session_id IN (`+placeholders+`) ORDER BY session_id, id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, data string
			if err := rows.Scan(&id, &data); err != nil {
				rows.Close()
				return nil, err
			}
			inputs := out[id]
			inputs.eventData = append(inputs.eventData, data)
			out[id] = inputs
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		rows, err = q.QueryContext(ctx, `SELECT session_id, COALESCE(app, ''), COALESCE(browser_url, ''), COALESCE(parsed_json, '') FROM activity_snapshots WHERE session_id IN (`+placeholders+`) ORDER BY session_id, id`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var input activityPrivacySnapshotInput
			if err := rows.Scan(&id, &input.app, &input.browserURL, &input.parsed); err != nil {
				rows.Close()
				return nil, err
			}
			inputs := out[id]
			inputs.snapshots = append(inputs.snapshots, input)
			out[id] = inputs
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *ActivityStore) cachedActivitySessionProvenance(id string, state activityPrivacySourceState) (activitySourceProvenance, bool) {
	cache := &s.readPrivacySources
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, found := cache.entries[id]
	if !found || entry.state != state {
		return activitySourceProvenance{}, false
	}
	cache.clock++
	entry.used = cache.clock
	cache.entries[id] = entry
	entry.source.Apps = append([]string(nil), entry.source.Apps...)
	return entry.source, true
}

func (s *ActivityStore) rememberActivitySessionProvenance(id string, state activityPrivacySourceState, source activitySourceProvenance) {
	cache := &s.readPrivacySources
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]activityPrivacyCachedSource)
	}
	if _, exists := cache.entries[id]; !exists && len(cache.entries) >= activityPrivacySourceCacheLimit {
		var oldestID string
		var oldest uint64
		for key, entry := range cache.entries {
			if oldestID == "" || entry.used < oldest {
				oldestID, oldest = key, entry.used
			}
		}
		delete(cache.entries, oldestID)
	}
	cache.clock++
	source.Apps = append([]string(nil), source.Apps...)
	cache.entries[id] = activityPrivacyCachedSource{state: state, source: source, used: cache.clock}
}

func (s *ActivityStore) activitySessionProvenance(session ActivitySessionRow) (activitySourceProvenance, error) {
	return s.activitySessionProvenanceContext(context.Background(), session)
}

func (s *ActivityStore) activitySessionProvenanceWithInputs(session ActivitySessionRow, inputs *activityPrivacyProvenanceInputs) (activitySourceProvenance, error) {
	if inputs == nil {
		return s.activitySessionProvenance(session)
	}
	return s.decodeActivitySessionProvenanceInputs(session, inputs.eventData, inputs.snapshots)
}

func (s *ActivityStore) activitySessionProvenanceContext(ctx context.Context, session ActivitySessionRow) (activitySourceProvenance, error) {
	if s == nil || s.db == nil {
		return activitySourceProvenance{}, errors.New("activity store unavailable")
	}
	state, err := s.activityPrivacySourceStateContext(ctx, s.reader(), session.ID)
	if err != nil {
		return activitySourceProvenance{}, err
	}
	if err := s.validateActivityPrivacySourceSchemaContext(ctx, s.reader(), state); err != nil {
		return activitySourceProvenance{}, err
	}
	if source, found := s.cachedActivitySessionProvenance(session.ID, state); found {
		return source, nil
	}

	// Use one SQLite read snapshot for the source revision, session metadata
	// and raw provenance. Never publish a cache entry assembled across writes.
	tx, err := s.reader().BeginTx(ctx, nil)
	if err != nil {
		return activitySourceProvenance{}, err
	}
	defer tx.Rollback()
	state, err = s.activityPrivacySourceStateContext(ctx, tx, session.ID)
	if err != nil {
		return activitySourceProvenance{}, err
	}
	if err := s.validateActivityPrivacySourceSchemaContext(ctx, tx, state); err != nil {
		return activitySourceProvenance{}, err
	}
	current := ActivitySessionRow{ID: session.ID, StartTime: state.start, EndTime: state.end, AppNames: state.apps, TriageStatus: state.status, MeetingApp: state.meeting}
	source, err := s.decodeActivitySessionProvenanceContext(ctx, tx, current)
	if err != nil {
		return activitySourceProvenance{}, fmt.Errorf("read activity source provenance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return activitySourceProvenance{}, err
	}
	s.rememberActivitySessionProvenance(session.ID, state, source)
	return source, nil
}
