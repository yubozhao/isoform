package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func migrateActivitySummarySourceProvenance(tx *sql.Tx) error {
	var found int
	if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('activity_summaries') WHERE name='source_provenance'`).Scan(&found); err != nil {
		return err
	}
	if found > 0 {
		return nil
	}
	_, err := tx.Exec(`ALTER TABLE activity_summaries ADD COLUMN source_provenance TEXT NOT NULL DEFAULT ''`)
	return err
}

// Source metadata is retained with the digest, not the raw event text. This
// lets later sensitive-app and pause changes hide a digest after raw retention.
type activitySummarySourceSet struct {
	Sources          []activitySourceProvenance
	Revision         uint64
	MeetingContext   string
	MemoryContext    string
	KnowledgeContext string
	DailySummaries   []string
}

func activitySummaryBounds(date, kind string) (int64, int64, error) {
	start, err := time.ParseInLocation("2006-01-02", strings.TrimSuffix(date, "/week"), time.Local)
	if err != nil {
		return 0, 0, err
	}
	days := 1
	if kind == "weekly" {
		days = 7
	}
	return start.UnixMilli(), start.AddDate(0, 0, days).UnixMilli(), nil
}

func (s *ActivityStore) summarySources(startMs, endMs int64) ([]activitySourceProvenance, error) {
	sessions, err := s.modelSessionsOverlapping(startMs, endMs)
	if err != nil {
		return nil, err
	}
	sources := make([]activitySourceProvenance, 0, len(sessions))
	for _, session := range sessions {
		source, err := s.activitySessionProvenance(session)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func (a *App) captureSummarySources(date, kind string) (activitySummarySourceSet, error) {
	startMs, endMs, err := activitySummaryBounds(date, kind)
	if err != nil {
		return activitySummarySourceSet{}, err
	}
	store := a.activity.store
	store.readPrivacyMu.Lock()
	defer store.readPrivacyMu.Unlock()
	sources, err := store.summarySources(startMs, endMs)
	if err != nil {
		return activitySummarySourceSet{}, err
	}
	result := activitySummarySourceSet{Revision: store.readPrivacyRevision.Load()}
	for _, source := range sources {
		allowed, err := a.activitySourceProvenanceAllowsRead(source)
		if err != nil {
			return result, err
		}
		if allowed {
			if source.SessionID != "" {
				row, err := store.GetSessionByID(source.SessionID)
				if err != nil {
					return result, err
				}
				if row != nil && row.IsMeeting && row.TranscriptPath != "" {
					text, dependencies, err := a.readMeetingTranscript(*row)
					if err != nil {
						continue
					}
					result.Sources = append(result.Sources, dependencies...)
					result.MeetingContext += formatDailyMeetingContext(*row, text)
				}
			}
			result.Sources = append(result.Sources, source)
		}
	}
	if result.MeetingContext != "" {
		result.MeetingContext = "\n## Meetings\n" + result.MeetingContext
	}
	// Rich daily prompts also contain memories/knowledge whose source sessions
	// can be older than the digest day. Retain those dependencies as well.
	if kind == "daily" {
		memories, err := a.GetMemoryEntries()
		if err != nil {
			return result, err
		}
		for _, entry := range memories {
			if entry.ModTime >= startMs && entry.ModTime < endMs && a.memorySourceAllowsRead(entry.Content) {
				extra, err := a.summaryMemorySources(entry.Content, startMs, endMs)
				if err != nil {
					return result, err
				}
				result.Sources = append(result.Sources, extra...)
				result.MemoryContext += fmt.Sprintf("\n### %s\n%s\n", entry.Title, entry.Content)
			}
		}
		knowledge, err := a.GetKnowledgeEntries()
		if err != nil {
			return result, err
		}
		for _, entry := range knowledge {
			if strings.HasPrefix(entry.Date, date) && a.memorySourceAllowsRead(entry.Content) {
				extra, err := a.summaryMemorySources(entry.Content, startMs, endMs)
				if err != nil {
					return result, err
				}
				result.Sources = append(result.Sources, extra...)
				result.KnowledgeContext += fmt.Sprintf("\n### %s\n%s\n", entry.Title, entry.Content)
			}
		}
	}
	if result.MemoryContext != "" {
		result.MemoryContext = redactActivityModelText("\n## Memories captured today\n" + result.MemoryContext)
	}
	if result.KnowledgeContext != "" {
		result.KnowledgeContext = redactActivityModelText("\n## Knowledge learned today\n" + result.KnowledgeContext)
	}
	// Weekly prompts can consume retained daily digests after raw retention.
	if kind == "weekly" {
		for day := time.UnixMilli(startMs); day.UnixMilli() < endMs; day = day.AddDate(0, 0, 1) {
			date := day.Format("2006-01-02")
			snapshot, found := store.cachedActivitySummarySnapshot(date, "daily", a.currentUILanguage())
			if !found || !a.activitySummarySnapshotAllowsRead(date, "daily", snapshot) {
				continue
			}
			summary, raw := snapshot.Summary, snapshot.Provenance
			if raw != "" {
				var sources []activitySourceProvenance
				if err := json.Unmarshal([]byte(raw), &sources); err != nil {
					return result, err
				}
				result.Sources = append(result.Sources, sources...)
			}
			result.DailySummaries = append(result.DailySummaries, fmt.Sprintf("%s: %s", date, summary))
		}
	}

	return result, nil
}

func (a *App) summarySourcesAllowRead(sources []activitySourceProvenance) bool {
	if a == nil || a.IsClosedEyesModeEnabled() || a.activity == nil || a.activity.store == nil {
		return false
	}
	policy, err := a.activityReadPrivacyPolicy()
	if err != nil {
		return false
	}
	for _, source := range sources {
		// Recheck current metadata and revision without decoding unchanged
		// event payloads. Only retained proof may authorize a missing row.
		if source.SessionID != "" {
			current, err := a.activity.store.activitySessionProvenance(ActivitySessionRow{ID: source.SessionID})
			if err != nil && (!errors.Is(err, sql.ErrNoRows) || !source.Retained) {
				return false
			}
			if errors.Is(err, sql.ErrNoRows) && source.Retained {
				// A later explicit deletion overrides retained digest proof.
				current, err = a.activity.store.retainedActivitySourceProvenance(source.SessionID)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return false
				}
			}
			if err == nil && !policy.allows(current) {
				return false
			}
			if row, readErr := a.activity.store.GetSessionByID(source.SessionID); readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
				return false
			} else if row != nil && row.TranscriptPath != "" {
				if _, err := a.transcriptSourcesWithPolicy(a.activity.store, *row, "", policy); err != nil {
					return false
				}
			}
		}
		if !policy.allows(source) {
			return false
		}
	}
	return true
}

func (a *App) activityCachedSummaryAllowsModelRead(date, kind string, startMs, endMs int64) bool {
	if a == nil || a.activity == nil || a.activity.store == nil || a.IsClosedEyesModeEnabled() {
		return false
	}
	rows, err := a.activity.store.reader().Query(`SELECT source_provenance FROM activity_summaries WHERE date=? AND type=?`, date, kind)
	if err != nil {
		return false
	}
	var all []string
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			rows.Close()
			return false
		}
		all = append(all, raw)
	}
	readErr, closeErr := rows.Err(), rows.Close()
	if readErr != nil || closeErr != nil {
		return false
	}
	if len(all) == 0 {
		return a.activityRangeAllowsModelRead(startMs, endMs)
	}
	for _, raw := range all {
		if raw == "" {
			if !a.activityRangeAllowsModelRead(startMs, endMs) {
				return false
			}
			continue
		}
		var sources []activitySourceProvenance
		if json.Unmarshal([]byte(raw), &sources) != nil || len(sources) == 0 || !a.summarySourcesAllowRead(sources) {
			return false
		}
	}
	return true
}

// Only deletion of a captured source can invalidate a pending publication.
// Unrelated retention ticks must not discard a user's summary or moments.
func (s *ActivityStore) withSummarySources(sources activitySummarySourceSet, write func() error) error {
	s.readPrivacyMu.Lock()
	defer s.readPrivacyMu.Unlock()
	for _, source := range sources.Sources {
		if source.SessionID != "" && s.readPrivacySessionRevisions[source.SessionID] > sources.Revision {
			return errors.New("activity source changed during model generation")
		}
	}
	return write()
}

// Before deleting raw sources, fill legacy rows' provenance while the original
// app/time metadata still exists. Legacy caches already missing their sources
// remain unreadable; unknown provenance is never guessed from the narrative.
func (s *ActivityStore) preserveLegacySummaryProvenance(startMs, endMs int64) error {
	dayStart := time.UnixMilli(startMs).In(time.Local)
	dayEnd := time.UnixMilli(max(startMs, endMs)).In(time.Local)
	week := func(day time.Time) string {
		return day.AddDate(0, 0, -(int(day.Weekday())+6)%7).Format("2006-01-02") + "/week"
	}
	rows, err := s.reader().Query(`SELECT date,type,language FROM activity_summaries WHERE source_provenance='' AND ((type='daily' AND date>=? AND date<=?) OR (type='weekly' AND date>=? AND date<=?))`, dayStart.Format("2006-01-02"), dayEnd.Format("2006-01-02"), week(dayStart), week(dayEnd))
	if err != nil {
		return err
	}
	type cacheKey struct{ date, kind, language string }
	var keys []cacheKey
	for rows.Next() {
		var key cacheKey
		if err := rows.Scan(&key.date, &key.kind, &key.language); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range keys {
		start, end, err := activitySummaryBounds(key.date, key.kind)
		if err != nil {
			continue
		}
		sources, err := s.summarySources(start, end)
		if err != nil {
			return err
		}
		if len(sources) == 0 {
			continue
		}
		raw, err := json.Marshal(sources)
		if err != nil {
			return err
		}
		if _, err = s.db.Exec(`UPDATE activity_summaries SET source_provenance=? WHERE date=? AND type=? AND language=? AND source_provenance=''`, string(raw), key.date, key.kind, key.language); err != nil {
			return fmt.Errorf("preserve digest source provenance: %w", err)
		}
	}
	return nil
}

func (a *App) summaryMemorySources(content string, startMs, endMs int64) ([]activitySourceProvenance, error) {
	provenance := memoryReadProvenanceFromContent(content)
	fm := parseMemoryFrontmatter(content)
	sources := []activitySourceProvenance{{StartTime: startMs, EndTime: endMs, Apps: append(fm.Apps, provenance.Apps...)}}
	for _, id := range compactStrings(append(fm.ActivitySessionIDs, provenance.Sessions...)) {
		source, err := a.activity.store.activityDependencyProvenance(id, sources[0].Apps)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// Capture content and its source proof in one read. A concurrent regeneration
// must never authorize an old narrative using the replacement row's proof.
type activitySummaryReadSnapshot struct {
	Summary    string
	Moments    string
	IsPartial  bool
	CreatedAt  int64
	Language   string
	Provenance string
}

func (s *ActivityStore) cachedActivitySummarySnapshot(date, kind, language string) (activitySummaryReadSnapshot, bool) {
	snapshot, err := s.readActivitySummarySnapshot(date, kind, language)
	return snapshot, err == nil
}

func (s *ActivityStore) readActivitySummarySnapshot(date, kind, language string) (activitySummaryReadSnapshot, error) {
	var snapshot activitySummaryReadSnapshot
	var partial int
	err := s.reader().QueryRow(`SELECT summary,moments_json,is_partial,created_at,language,source_provenance
        FROM activity_summaries WHERE date=? AND type=?
        ORDER BY language=? DESC,language='' DESC,created_at DESC LIMIT 1`, date, kind, normalizeSummaryLanguage(language)).Scan(&snapshot.Summary, &snapshot.Moments, &partial, &snapshot.CreatedAt, &snapshot.Language, &snapshot.Provenance)
	snapshot.IsPartial = partial == 1
	return snapshot, err
}

func (a *App) activitySummarySnapshotAllowsRead(date, kind string, snapshot activitySummaryReadSnapshot) bool {
	if snapshot.Provenance == "" {
		start, end, err := activitySummaryBounds(date, kind)
		return err == nil && a.activityRangeAllowsModelRead(start, end)
	}
	var sources []activitySourceProvenance
	return json.Unmarshal([]byte(snapshot.Provenance), &sources) == nil && len(sources) > 0 && a.summarySourcesAllowRead(sources)
}

func (a *App) readableActivitySummarySnapshot(date, kind, language string) (activitySummaryReadSnapshot, bool) {
	if a == nil || a.activity == nil || a.activity.store == nil || a.activity.store.db == nil || a.IsClosedEyesModeEnabled() {
		return activitySummaryReadSnapshot{}, false
	}
	snapshot, exists := a.activity.store.cachedActivitySummarySnapshot(date, kind, language)
	if !exists || !a.activitySummarySnapshotAllowsRead(date, kind, snapshot) {
		return activitySummaryReadSnapshot{}, false
	}
	return snapshot, true
}

// The local Activity UI displays the original narrative. CLI and model
// consumers must redact at their respective outbound boundaries.
func (a *App) cachedActivitySummaryForRead(date, kind, language string) (summary string, isPartial bool, createdAt int64, cachedLanguage string, found bool) {
	snapshot, exists := a.readableActivitySummarySnapshot(date, kind, language)
	if !exists {
		return
	}
	return snapshot.Summary, snapshot.IsPartial, snapshot.CreatedAt, snapshot.Language, true
}

// Retention keeps every privacy restriction observed before raw-source removal.
// Marking this proof explicitly is also what authorizes missing raw sources;
// a stale snapshot read before an explicit deletion cannot use that exemption.
func (s *ActivityStore) retainSummarySourcePrivacy(tx *sql.Tx, sessionID string) error {
	state, err := s.activityPrivacySourceState(tx, sessionID)
	if err != nil {
		return err
	}
	// Retention is a one-time cold read, not a cache hit: the facts and final
	// delete share the transaction even when another writer updates a source.
	current, err := s.decodeActivitySessionProvenance(tx, ActivitySessionRow{ID: sessionID, StartTime: state.start, EndTime: state.end, AppNames: state.apps, TriageStatus: state.status, MeetingApp: state.meeting})
	if err != nil {
		return err
	}
	current.Retained = true
	if err := saveActivitySourcePrivacyReceipt(tx, current); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT date,type,language,source_provenance FROM activity_summaries
        WHERE EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(source_provenance) THEN source_provenance ELSE '[]' END) WHERE json_extract(value,'$.sessionId')=?)`, sessionID)
	if err != nil {
		return err
	}
	type retainedRow struct{ date, kind, language, raw string }
	var records []retainedRow
	for rows.Next() {
		var record retainedRow
		if err := rows.Scan(&record.date, &record.kind, &record.language, &record.raw); err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, record := range records {
		var sources []activitySourceProvenance
		if err := json.Unmarshal([]byte(record.raw), &sources); err != nil {
			return err
		}
		for i := range sources {
			source := &sources[i]
			if source.SessionID != sessionID {
				continue
			}
			source.Private = source.Private || current.Private
			source.Apps = compactStrings(append(source.Apps, current.Apps...))
			source.StartTime = min(source.StartTime, current.StartTime)
			source.EndTime = max(source.EndTime, current.EndTime)
			source.Retained = true
		}
		raw, err := json.Marshal(sources)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE activity_summaries SET source_provenance=? WHERE date=? AND type=? AND language=? AND source_provenance=?`, string(raw), record.date, record.kind, record.language, record.raw); err != nil {
			return err
		}
	}
	return nil
}
