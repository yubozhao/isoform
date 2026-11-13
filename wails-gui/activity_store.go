package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sync/singleflight"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"wails-gui/internal/platform/activitymon"

	_ "modernc.org/sqlite"
)

// ── Protected-session read policy ────────────────────────────────────────
//
// Sessions that touched a protected app are persisted with
// triage_status = "protected" (see activity_monitor.go extractSession).
// The store enforces a single read-side invariant:
//
//	DEFAULT STORE READS NEVER RETURN PROTECTED SESSIONS' DATA.
//
// Bulk/content getters (GetSessions, GetFilteredSessions, GetSessionsByIDs,
// GetSessionsByTriggerCreatedAfter, GetSessionEvents, GetSessionSegments,
// GetSessionSnapshots, GetRecentSnapshotsForApp, GetSemanticPendingSessions,
// embedding/search queries) exclude protected sessions unless the name
// carries the IncludingProtected suffix. The suffixed variants exist ONLY
// for user-facing UI that shows the user their own timeline (Activity page
// list/detail) and for retention/cleanup; they must never feed an LLM
// prompt, an embedding, a search index, or an external service. A new
// consumer that uses the default API is safe by construction and needs no
// privacy knowledge.
//
// Deliberate exemptions (each annotated at its definition):
//   - GetSessionsByAge / GetSessionsOlderThan / GetSessionsByOCRStatus:
//     maintenance sweeps that must see protected sessions to purge or
//     skip-mark them.
//   - GetSessionByID: single-row metadata lookup backing the detail UI and
//     transcript plumbing (transcripts never exist for protected sessions).
//   - GetSessionEventRows: only reachable through session lists that are
//     already default-filtered (LoadTimelineRunEvents).
//   - GetPendingSessions / semantic-event readers / OCR frame readers:
//     safe by construction — protected sessions are never "pending", never
//     semantically processed, never OCR'd.
//
// sqlExcludeProtected is the shared predicate; it must be applied against
// the activity_sessions table (aliased s where joins are involved).
const sqlExcludeProtected = ` AND COALESCE(s.triage_status, '') != 'protected'`

// ActivityStore provides SQLite-backed persistence for activity monitoring data.
// Uses a separate DB (~/.yansu-agent/activity.db) to avoid bloating the main sessions.db.
type ActivityStore struct {
	// repeatedOnsetFlight shares one all-time repeated-onset scan among callers.
	repeatedOnsetFlight singleflight.Group
	// repeatedOnsets holds the all-time repeated-onset set for one read revision.
	repeatedOnsets taskEpisodeRepeatedCache
	db             *sql.DB
	readDB         *sql.DB
	readWriter     *sql.DB
	segmentsDir    string
	snapshotsDir   string
	eventsDir      string
	audioDir       string
	// Revision is even while sources are stable and odd during deletion.
	readPrivacyMu               sync.Mutex
	readPrivacyRevision         atomic.Uint64
	readPrivacySessionRevisions map[string]uint64
	readPrivacySources          activityPrivacySourceCache
	mu                          sync.RWMutex
	embedder                    EmbeddingProvider
	embeddingMu                 sync.Mutex
	embeddingBackfillMu         sync.Mutex
	embeddingBackfillAfter      activityEmbeddingCursor
	appliedMigrations           map[string]bool // populated during migrate(); read-only after init
	// timelineRunsMu serializes run derivation, evidence/summary writes and
	// retention privacy preparation. Retention releases the DB between pages;
	// unrelated activity readers and writers do not acquire this mutex.
	timelineRunsMu      sync.Mutex
	threadOutcomeMu     sync.Mutex
	taskShadowReleaseMu sync.Mutex
}

// ActivitySessionRow represents a persisted activity session.
type ActivitySessionRow struct {
	ID               string `json:"id"`
	StartTime        int64  `json:"startTime"`
	EndTime          int64  `json:"endTime"`
	TriggerKind      string `json:"triggerKind"`
	AppNames         string `json:"appNames"` // JSON array
	EventCount       int    `json:"eventCount"`
	SegmentCount     int    `json:"segmentCount"`
	SnapshotCount    int    `json:"snapshotCount"`
	TotalSizeBytes   int64  `json:"totalSizeBytes"`
	TriageStatus     string `json:"triageStatus"`
	OCRStatus        string `json:"ocrStatus"`
	AutomationStatus string `json:"automationStatus"`
	HandoffStatus    string `json:"handoffStatus"`
	SOPStatus        string `json:"sopStatus"`
	CreatedAt        int64  `json:"createdAt"`

	// Meeting detection fields (added by meeting_columns migration).
	IsMeeting      bool   `json:"isMeeting"`
	MeetingApp     string `json:"meetingApp,omitempty"`
	TranscriptPath string `json:"transcriptPath,omitempty"`

	// Meeting audio recording (added by audio_columns migration).
	AudioPath      string `json:"audioPath,omitempty"`
	AudioSizeBytes int64  `json:"audioSizeBytes,omitempty"`
}

// OCRFrameRow represents a single OCR-processed frame.
//
// WARNING: `SegmentID` is overloaded — it can refer to two different tables
// depending on which producer wrote the row (see activity_ocr.go):
//
//   - Snapshot-based path (most rows on modern sessions):
//     SegmentID stores activity_snapshots.id. The OCR ran against the JPEG
//     snapshot directly; FrameTime is (snapshot.Timestamp - session.StartTime)
//     in seconds.
//   - Segment-based path (legacy, video-segment OCR sampler):
//     SegmentID stores activity_segments.id. FrameTime is a seconds offset
//     into the video segment.
//
// The column is named segment_id only because the schema was authored for the
// segment-based path first; the snapshot-based path reuses the column to avoid
// a migration. Consumers that need to map an OCR frame back to a snapshot
// MUST try the snapshot.id interpretation before falling back to the segment
// lookup — see selectedFrames assembly in activity_triage.go.
type OCRFrameRow struct {
	ID         int64   `json:"id"`
	SessionID  string  `json:"sessionId"`
	SegmentID  int64   `json:"segmentId"`
	FrameTime  float64 `json:"frameTime"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
	// BoxesJSON is a JSON array of OCRHighlightBox in FULL-FRAME normalized
	// [0,1] coordinates (ocr_boxes_json_column migration). '' = not computed,
	// '[]' = computed with no text. Snapshot-based producers map observations
	// out of the focus crop before persisting; consumers can therefore overlay
	// them on the raw snapshot without knowing the crop that OCR actually saw.
	BoxesJSON string `json:"boxesJson,omitempty"`
}

// ActivitySnapshotRow represents a persisted JPEG screenshot captured on user events.
type ActivitySnapshotRow struct {
	ID          int64  `json:"id"`
	SessionID   string `json:"sessionId"`
	Timestamp   int64  `json:"timestamp"`
	FilePath    string `json:"filePath"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	SizeBytes   int64  `json:"sizeBytes"`
	Trigger     string `json:"trigger"`
	StorageTier string `json:"storageTier"`
	AXText      string `json:"axText"`
	BrowserURL  string `json:"browserUrl"`

	// Structured parser output (snapshot_parsed_columns migration).
	// Empty when no app-specific parser matched the capture.
	ContentType string `json:"contentType,omitempty"` // "conversation", "document", "task_list", "events", "terminal", "web_page"
	App         string `json:"app,omitempty"`         // short app key from parser metadata, e.g. "slack", "notion", "gmail"
	ParsedJSON  string `json:"parsedJson,omitempty"`  // full ParsedContent serialized as JSON

	// Focused-window bounds captured at the same instant as the JPEG
	// (focus_rect_columns migration). Stored as virtual-desktop-normalized
	// coordinates in [0,1] so they survive resolution changes between capture
	// and consumer. All four 0 means "unknown" — the OCR cropper and any
	// future region-aware consumer must fall back to the full frame in that
	// case (legacy snapshots, multi-display fallback, non-darwin platforms).
	FocusRectX float64 `json:"focusRectX,omitempty"`
	FocusRectY float64 `json:"focusRectY,omitempty"`
	FocusRectW float64 `json:"focusRectW,omitempty"`
	FocusRectH float64 `json:"focusRectH,omitempty"`

	// True once the OCR worker has actually cropped recognition to the focus
	// rect for this snapshot. Stays false when OCR ran on the full frame —
	// either because the rect was unknown at capture, or because the crop
	// helper rejected it (degenerate size, decode error). Lets the audit UI
	// only draw the rect overlay when the persisted rect WAS the OCR scope,
	// not just the capture-time focus indicator (ocr_used_focus_rect_column
	// migration).
	OCRUsedFocusRect bool `json:"ocrUsedFocusRect,omitempty"`
}

type ActivityEventRow struct {
	ID        int64                     `json:"id"`
	SessionID string                    `json:"sessionId"`
	SortOrder int                       `json:"sortOrder"`
	Event     activitymon.ActivityEvent `json:"event"`
}

// ActivitySegmentRow represents a persisted video segment index entry.
type ActivitySegmentRow struct {
	ID          int64  `json:"id"`
	SessionID   string `json:"sessionId"`
	StartTime   int64  `json:"startTime"`
	EndTime     int64  `json:"endTime"`
	FilePath    string `json:"filePath"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	FPS         int    `json:"fps"`
	Codec       string `json:"codec"`
	SizeBytes   int64  `json:"sizeBytes"`
	StorageTier string `json:"storageTier"`
}

func activityDatabaseURI(dbPath string) url.URL {
	uriPath := filepath.ToSlash(dbPath)
	// URL paths need a leading slash before a Windows drive letter.
	if filepath.VolumeName(dbPath) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return url.URL{Scheme: "file", Path: uriPath}
}

// NewActivityStore opens (or creates) the activity database and storage directories.
func NewActivityStore(baseDir string) (*ActivityStore, error) {
	return openActivityStore(baseDir, true)
}

func openActivityStore(baseDir string, refreshProfiles bool) (*ActivityStore, error) {
	dbPath, err := filepath.Abs(filepath.Join(baseDir, "activity.db"))
	if err != nil {
		return nil, fmt.Errorf("resolve activity database: %w", err)
	}
	segmentsDir := filepath.Join(baseDir, "activity", "segments")
	snapshotsDir := filepath.Join(baseDir, "activity", "snapshots")
	eventsDir := filepath.Join(baseDir, "activity", "events")
	audioDir := filepath.Join(baseDir, "activity", "audio")

	for _, dir := range []string{segmentsDir, snapshotsDir, eventsDir, audioDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("create activity dir %s: %w", dir, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	writerURI := activityDatabaseURI(dbPath)
	db, err := sql.Open("sqlite", writerURI.String())
	if err != nil {
		return nil, fmt.Errorf("open activity database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}

	// Wait up to 5 s for a contended write before surfacing SQLITE_BUSY. The
	// activity-rotator's batch updates were spamming the log with thousands of
	// "database is locked" errors when the rotator and the snapshot writer
	// raced; busy_timeout makes the driver block briefly instead.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	s := &ActivityStore{db: db, segmentsDir: segmentsDir, snapshotsDir: snapshotsDir, eventsDir: eventsDir, audioDir: audioDir}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate activity db: %w", err)
	}
	if refreshProfiles {
		if _, err := s.RefreshShadowCapabilityProfiles(time.Now().UnixMilli()); err != nil {
			log.Printf("[shadow-profile] startup refresh failed: %v", err)
		}
	}

	// Keep capture's single writer; WAL readers never wait for that pool slot.
	uri := writerURI
	uri.RawQuery = "mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	readDB, err := sql.Open("sqlite", uri.String())
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open activity readers: %w", err)
	}
	readDB.SetMaxOpenConns(4)
	readDB.SetMaxIdleConns(4)
	if err := readDB.Ping(); err != nil {
		readDB.Close()
		db.Close()
		return nil, fmt.Errorf("connect activity readers: %w", err)
	}
	s.readDB = readDB
	s.readWriter = db
	return s, nil
}

func (s *ActivityStore) migrate() error {
	// Infrastructure: migration_log table and bootstrap are foundational.
	// If these fail, the DB is not usable — fail hard.
	if err := s.ensureMigrationLog(); err != nil {
		return fmt.Errorf("ensure migration_log: %w", err)
	}
	if err := s.bootstrapMigrationLog(); err != nil {
		return fmt.Errorf("bootstrap migration_log: %w", err)
	}

	// Execute pending migrations. Individual failures are logged but do not
	// block store initialization (fail-and-continue).
	s.runPendingMigrations()

	return nil
}

// reader shares the store identity and privacy revision state with writers.
func (s *ActivityStore) reader() *sql.DB {
	if s.readDB != nil && s.readWriter == s.db {
		return s.readDB
	}
	return s.db // In-memory and migration-only stores keep their own connection.
}

// Close releases both the capture writer and the WAL reader pool.
func (s *ActivityStore) Close() error {
	var readErr error
	if s.readDB != nil {
		readErr = s.readDB.Close()
	}

	if s.db != nil {
		return firstErr(readErr, s.db.Close())
	}
	return nil
}

// SaveSession persists an activity session with its events and segment references.
func (s *ActivityStore) SaveSession(session ActivitySessionRow, events []activitymon.ActivityEvent, segments []ActivitySegmentRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	isMeeting := 0
	if session.IsMeeting {
		isMeeting = 1
	}
	_, err = tx.Exec(`
		INSERT INTO activity_sessions (id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			is_meeting, meeting_app, transcript_path, audio_path, audio_size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.StartTime, session.EndTime, session.TriggerKind,
		session.AppNames, session.EventCount, session.SegmentCount, session.SnapshotCount,
		session.TotalSizeBytes, session.TriageStatus,
		isMeeting, session.MeetingApp, session.TranscriptPath,
		session.AudioPath, session.AudioSizeBytes,
		session.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert activity_session: %w", err)
	}

	// Batch insert events
	if len(events) > 0 {
		stmt, err := tx.Prepare(`
			INSERT INTO activity_events (session_id, timestamp, kind, data, sort_order)
			VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare event insert: %w", err)
		}
		defer stmt.Close()

		for i, evt := range events {
			data, _ := json.Marshal(evt)
			if _, err := stmt.Exec(session.ID, evt.Timestamp, string(evt.Kind), string(data), i); err != nil {
				return fmt.Errorf("insert event %d: %w", i, err)
			}
		}
	}

	// Batch insert segments
	if len(segments) > 0 {
		stmt, err := tx.Prepare(`
			INSERT INTO activity_segments (session_id, start_time, end_time, file_path,
				width, height, fps, codec, size_bytes, storage_tier)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare segment insert: %w", err)
		}
		defer stmt.Close()

		for i, seg := range segments {
			if _, err := stmt.Exec(session.ID, seg.StartTime, seg.EndTime, seg.FilePath,
				seg.Width, seg.Height, seg.FPS, seg.Codec, seg.SizeBytes, seg.StorageTier); err != nil {
				return fmt.Errorf("insert segment %d: %w", i, err)
			}
		}
	}

	return tx.Commit()
}

// SaveSessionWithSnapshots inserts a session row plus its snapshots in one
// transaction. Used by the external-conversation ingest path (Slack/Discord/
// Telegram messages injected as a synthetic chat session) which has snapshots
// but no captured events/segments — so it cannot reuse SaveSession's
// events/segments shape directly.
func (s *ActivityStore) SaveSessionWithSnapshots(session ActivitySessionRow, snapshots []ActivitySnapshotRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	isMeeting := 0
	if session.IsMeeting {
		isMeeting = 1
	}
	if _, err := tx.Exec(`
		INSERT INTO activity_sessions (id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			is_meeting, meeting_app, transcript_path, audio_path, audio_size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.StartTime, session.EndTime, session.TriggerKind,
		session.AppNames, session.EventCount, session.SegmentCount, session.SnapshotCount,
		session.TotalSizeBytes, session.TriageStatus,
		isMeeting, session.MeetingApp, session.TranscriptPath,
		session.AudioPath, session.AudioSizeBytes,
		session.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert activity_session: %w", err)
	}

	if err := s.SaveSnapshots(tx, snapshots); err != nil {
		return fmt.Errorf("insert snapshots: %w", err)
	}

	return tx.Commit()
}

// GetSessions returns activity sessions ordered by start_time descending.
// Excludes protected sessions (default read policy).
func (s *ActivityStore) GetSessions(limit, offset int) ([]ActivitySessionRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			ocr_status, COALESCE(automation_status, ''), COALESCE(handoff_status, ''),
			COALESCE(sop_status, ''),
			COALESCE(is_meeting, 0), COALESCE(meeting_app, ''), COALESCE(transcript_path, ''),
			COALESCE(audio_path, ''), COALESCE(audio_size_bytes, 0),
			created_at
		FROM activity_sessions s
		WHERE 1=1`+sqlExcludeProtected+`
		ORDER BY start_time DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []ActivitySessionRow
	for rows.Next() {
		var s ActivitySessionRow
		var isMeeting int
		if err := rows.Scan(&s.ID, &s.StartTime, &s.EndTime, &s.TriggerKind,
			&s.AppNames, &s.EventCount, &s.SegmentCount, &s.SnapshotCount, &s.TotalSizeBytes,
			&s.TriageStatus, &s.OCRStatus, &s.AutomationStatus, &s.HandoffStatus,
			&s.SOPStatus,
			&isMeeting, &s.MeetingApp, &s.TranscriptPath,
			&s.AudioPath, &s.AudioSizeBytes,
			&s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		s.IsMeeting = isMeeting != 0
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// GetFilteredSessions returns sessions filtered by time range and keyword.
// Pass startMs=0 or endMs=0 to skip that bound. Empty keyword means no keyword filter.
// GetFilteredSessions excludes protected sessions (default read policy).
func (s *ActivityStore) GetFilteredSessions(limit, offset int, startMs, endMs int64, keyword string) ([]ActivitySessionRow, error) {
	return s.getFilteredSessions(limit, offset, startMs, endMs, keyword, false)
}

// GetFilteredSessionsIncludingProtected returns protected sessions too.
// Reserved for the Activity-page UI (the user viewing their own timeline);
// never feed the result to an LLM, embedding, or search index.
func (s *ActivityStore) GetFilteredSessionsIncludingProtected(limit, offset int, startMs, endMs int64, keyword string) ([]ActivitySessionRow, error) {
	return s.getFilteredSessions(limit, offset, startMs, endMs, keyword, true)
}

func (s *ActivityStore) getFilteredSessions(limit, offset int, startMs, endMs int64, keyword string, includeProtected bool) ([]ActivitySessionRow, error) {
	kw := "%" + keyword + "%"

	// trigram tokenizer requires >= 3 characters; fall back to LIKE for short keywords.
	useFTS := utf8.RuneCountInString(keyword) >= 3
	var ocrClause string
	var ocrParam string
	if useFTS {
		ocrClause = `EXISTS (
			SELECT 1 FROM activity_ocr_frames o
			JOIN activity_ocr_fts f ON f.rowid = o.id
			WHERE o.session_id = s.id AND f.text MATCH ?
		)`
		ocrParam = `"` + keyword + `"`
	} else {
		ocrClause = `EXISTS (
			SELECT 1 FROM activity_ocr_frames o
			WHERE o.session_id = s.id AND o.text LIKE ?
		)`
		ocrParam = kw
	}

	protectedClause := sqlExcludeProtected
	if includeProtected {
		protectedClause = ""
	}
	query := `
		SELECT id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			ocr_status, COALESCE(automation_status, ''), COALESCE(handoff_status, ''),
			COALESCE(sop_status, ''),
			COALESCE(is_meeting, 0), COALESCE(meeting_app, ''), COALESCE(transcript_path, ''),
			COALESCE(audio_path, ''), COALESCE(audio_size_bytes, 0),
			created_at
		FROM activity_sessions s
		WHERE (? = 0 OR s.start_time >= ?)
		  AND (? = 0 OR s.end_time <= ?)
		  AND (? = '' OR s.app_names LIKE ? OR EXISTS (
			SELECT 1 FROM activity_events e
			WHERE e.session_id = s.id AND e.data LIKE ?
		  ) OR ` + ocrClause + `)` + protectedClause + `
		ORDER BY s.start_time DESC
		LIMIT ? OFFSET ?`
	rows, err := s.reader().Query(query,
		startMs, startMs,
		endMs, endMs,
		keyword, kw, kw, ocrParam,
		limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("query filtered sessions: %w", err)
	}
	defer rows.Close()

	var sessions []ActivitySessionRow
	for rows.Next() {
		var r ActivitySessionRow
		var isMeeting int
		if err := rows.Scan(&r.ID, &r.StartTime, &r.EndTime, &r.TriggerKind,
			&r.AppNames, &r.EventCount, &r.SegmentCount, &r.SnapshotCount, &r.TotalSizeBytes,
			&r.TriageStatus, &r.OCRStatus, &r.AutomationStatus, &r.HandoffStatus,
			&r.SOPStatus,
			&isMeeting, &r.MeetingApp, &r.TranscriptPath,
			&r.AudioPath, &r.AudioSizeBytes,
			&r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		r.IsMeeting = isMeeting != 0
		sessions = append(sessions, r)
	}
	return sessions, rows.Err()
}

// GetSessionsByIDs returns a map from session_id → row for every id in
// the input slice that exists. Missing ids are silently absent from
// the result map (callers handle the gap — typically the row was
// pruned by retention, or the session is protected: default read policy
// excludes protected sessions, and the map-gap contract means callers
// skip them naturally). Single SQL query regardless of input size,
// which is the whole point: ListPrincipalActivitySessions was paying
// 50 round-trips per principal before this.
func (s *ActivityStore) GetSessionsByIDs(ids []string) (map[string]*ActivitySessionRow, error) {
	return s.getSessionsByIDs(ids, false)
}

func (s *ActivityStore) GetSessionsByIDsContext(ctx context.Context, ids []string) (map[string]*ActivitySessionRow, error) {
	return s.getSessionsByIDsContext(ctx, ids, false)
}

// GetSessionsByIDsIncludingProtected returns protected sessions too.
// Reserved for user-facing UI; never feed an LLM or index.
func (s *ActivityStore) GetSessionsByIDsIncludingProtected(ids []string) (map[string]*ActivitySessionRow, error) {
	return s.getSessionsByIDs(ids, true)
}

func (s *ActivityStore) getSessionsByIDs(ids []string, includeProtected bool) (map[string]*ActivitySessionRow, error) {
	return s.getSessionsByIDsContext(context.Background(), ids, includeProtected)
}

func (s *ActivityStore) getSessionsByIDsContext(ctx context.Context, ids []string, includeProtected bool) (map[string]*ActivitySessionRow, error) {
	out := make(map[string]*ActivitySessionRow, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	// Build (?,?,?...) placeholder list inline. SQLite caps at 999
	// variables per statement; cap our batch at 500 with iterative
	// chunking to stay well under that.
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1] // drop trailing comma
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		protectedClause := sqlExcludeProtected
		if includeProtected {
			protectedClause = ""
		}
		query := `
			SELECT id, start_time, end_time, trigger_kind, app_names,
				event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
				ocr_status, COALESCE(automation_status, ''), COALESCE(handoff_status, ''),
				COALESCE(sop_status, ''),
				COALESCE(is_meeting, 0), COALESCE(meeting_app, ''), COALESCE(transcript_path, ''),
				COALESCE(audio_path, ''), COALESCE(audio_size_bytes, 0),
				created_at
			FROM activity_sessions s
			WHERE id IN (` + placeholders + `)` + protectedClause
		rows, err := s.reader().QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query sessions by ids: %w", err)
		}
		for rows.Next() {
			var row ActivitySessionRow
			var isMeeting int
			if err := rows.Scan(
				&row.ID, &row.StartTime, &row.EndTime, &row.TriggerKind,
				&row.AppNames, &row.EventCount, &row.SegmentCount, &row.SnapshotCount, &row.TotalSizeBytes,
				&row.TriageStatus, &row.OCRStatus, &row.AutomationStatus, &row.HandoffStatus,
				&row.SOPStatus,
				&isMeeting, &row.MeetingApp, &row.TranscriptPath,
				&row.AudioPath, &row.AudioSizeBytes,
				&row.CreatedAt,
			); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan session: %w", err)
			}
			row.IsMeeting = isMeeting != 0
			out[row.ID] = &row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iter sessions: %w", err)
		}
		rows.Close()
	}
	return out, nil
}

// GetSessionsByTriggerCreatedAfter returns sessions created after createdAfterMs
// for a specific trigger. Used by onboarding previews to evaluate only the
// short capture window the user just started. Excludes protected sessions
// (default read policy) — the onboarding preview both mutates triage_status
// and sends session content to an LLM, neither of which may touch them.
func (s *ActivityStore) GetSessionsByTriggerCreatedAfter(triggerKind string, createdAfterMs int64) ([]ActivitySessionRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			ocr_status, COALESCE(automation_status, ''), COALESCE(handoff_status, ''),
			COALESCE(sop_status, ''),
			COALESCE(is_meeting, 0), COALESCE(meeting_app, ''), COALESCE(transcript_path, ''),
			COALESCE(audio_path, ''), COALESCE(audio_size_bytes, 0),
			created_at
		FROM activity_sessions s
		WHERE trigger_kind = ? AND created_at >= ?`+sqlExcludeProtected+`
		ORDER BY start_time ASC`, triggerKind, createdAfterMs)
	if err != nil {
		return nil, fmt.Errorf("query sessions by trigger and created_at: %w", err)
	}
	defer rows.Close()

	var sessions []ActivitySessionRow
	for rows.Next() {
		var r ActivitySessionRow
		var isMeeting int
		if err := rows.Scan(&r.ID, &r.StartTime, &r.EndTime, &r.TriggerKind,
			&r.AppNames, &r.EventCount, &r.SegmentCount, &r.SnapshotCount, &r.TotalSizeBytes,
			&r.TriageStatus, &r.OCRStatus, &r.AutomationStatus, &r.HandoffStatus,
			&r.SOPStatus,
			&isMeeting, &r.MeetingApp, &r.TranscriptPath,
			&r.AudioPath, &r.AudioSizeBytes,
			&r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		r.IsMeeting = isMeeting != 0
		sessions = append(sessions, r)
	}
	return sessions, rows.Err()
}

// GetSessionByID returns a single session by its ID.
func (s *ActivityStore) GetSessionByID(id string) (*ActivitySessionRow, error) {
	var row ActivitySessionRow
	var isMeeting int
	err := s.reader().QueryRow(`
		SELECT id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			ocr_status, COALESCE(automation_status, ''), COALESCE(handoff_status, ''),
			COALESCE(sop_status, ''),
			COALESCE(is_meeting, 0), COALESCE(meeting_app, ''), COALESCE(transcript_path, ''),
			COALESCE(audio_path, ''), COALESCE(audio_size_bytes, 0),
			created_at
		FROM activity_sessions
		WHERE id = ?`, id).Scan(
		&row.ID, &row.StartTime, &row.EndTime, &row.TriggerKind,
		&row.AppNames, &row.EventCount, &row.SegmentCount, &row.SnapshotCount, &row.TotalSizeBytes,
		&row.TriageStatus, &row.OCRStatus, &row.AutomationStatus, &row.HandoffStatus,
		&row.SOPStatus,
		&isMeeting, &row.MeetingApp, &row.TranscriptPath,
		&row.AudioPath, &row.AudioSizeBytes,
		&row.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("query session by id: %w", err)
	}
	row.IsMeeting = isMeeting != 0
	return &row, nil
}

// isSessionProtected reports whether the session carries the "protected"
// privacy terminal state. Shared by the default per-session getters.
// FAILS CLOSED: an unexpected database error is treated as protected, so a
// transient failure can never leak a protected session's rows. A missing
// session (ErrNoRows) is not protected — its child rows are empty anyway.
func (s *ActivityStore) isSessionProtected(sessionID string) bool {
	var status string
	err := s.reader().QueryRow(`SELECT COALESCE(triage_status, '') FROM activity_sessions WHERE id = ?`, sessionID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		log.Printf("[activity] isSessionProtected(%s): %v — failing closed", sessionID, err)
		return true
	}
	return status == "protected"
}

// GetSessionEvents returns all events for a given session, ordered by sort_order.
// Returns nil for protected sessions (default read policy); maintenance and
// user-facing-UI callers needing those rows use the IncludingProtected variant.
func (s *ActivityStore) GetSessionEvents(sessionID string) ([]activitymon.ActivityEvent, error) {
	if s.isSessionProtected(sessionID) {
		return nil, nil
	}
	return s.GetSessionEventsIncludingProtected(sessionID)
}

// GetSessionEventsIncludingProtected returns events regardless of the
// session's protected state. Reserved for retention/cleanup, voice
// enrollment, and the Activity detail UI; never feed an LLM or index.
func (s *ActivityStore) GetSessionEventsIncludingProtected(sessionID string) ([]activitymon.ActivityEvent, error) {
	rows, err := s.reader().Query(`
		SELECT data FROM activity_events
		WHERE session_id = ?
		ORDER BY sort_order`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []activitymon.ActivityEvent
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		var evt activitymon.ActivityEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			log.Printf("[activity] failed to unmarshal event: %v", err)
			continue
		}
		events = append(events, evt)
	}
	return events, rows.Err()
}

func (s *ActivityStore) GetSessionEventRows(sessionID string) ([]ActivityEventRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, session_id, sort_order, data FROM activity_events
		WHERE session_id = ?
		ORDER BY sort_order`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query event rows: %w", err)
	}
	defer rows.Close()

	var events []ActivityEventRow
	for rows.Next() {
		var row ActivityEventRow
		var data string
		if err := rows.Scan(&row.ID, &row.SessionID, &row.SortOrder, &data); err != nil {
			return nil, fmt.Errorf("scan event row: %w", err)
		}
		if err := json.Unmarshal([]byte(data), &row.Event); err != nil {
			log.Printf("[activity] failed to unmarshal event row: %v", err)
			continue
		}
		events = append(events, row)
	}
	return events, rows.Err()
}

// GetTimelineGroupSummaries returns cached semantic summaries keyed by group id.
func (s *ActivityStore) GetTimelineGroupSummaries(groupIDs []string) (map[string]ActivityTimelineGroupSummary, error) {
	result := make(map[string]ActivityTimelineGroupSummary)
	if len(groupIDs) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(groupIDs)), ",")
	args := make([]any, len(groupIDs))
	for i, id := range groupIDs {
		args[i] = id
	}
	rows, err := s.reader().Query(`
		SELECT group_id, title, description, activity_type, entities, timeline_json
		FROM activity_timeline_group_summaries
		WHERE group_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query timeline group summaries: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var summary ActivityTimelineGroupSummary
		var entitiesJSON, timelineJSON string
		if err := rows.Scan(&summary.GroupID, &summary.Title, &summary.Description,
			&summary.ActivityType, &entitiesJSON, &timelineJSON); err != nil {
			return nil, fmt.Errorf("scan timeline group summary: %w", err)
		}
		summary.Title = timelineTitleSaysComputer(timelineTitlePastTense(summary.Title))
		if err := json.Unmarshal([]byte(entitiesJSON), &summary.Entities); err != nil {
			log.Printf("[activity] failed to unmarshal timeline group summary entities: %v", err)
		}
		if err := json.Unmarshal([]byte(timelineJSON), &summary.Timeline); err != nil {
			log.Printf("[activity] failed to unmarshal timeline group summary timeline: %v", err)
		}
		result[summary.GroupID] = summary
	}
	return result, rows.Err()
}

// UpsertTimelineGroupSummary stores the model-generated semantic layer for an
// app-run group. The prompt/response columns are retained for local debugging.
// A model result completing after retention must not recreate purged content.
func (s *ActivityStore) UpsertTimelineGroupSummary(
	group ActivityTimelineGroup,
	summary ActivityTimelineGroupSummary,
	model string,
	prompt string,
	responseRaw string,
) error {
	s.timelineRunsMu.Lock()
	defer s.timelineRunsMu.Unlock()
	entitiesJSON, err := json.Marshal(summary.Entities)
	if err != nil {
		return fmt.Errorf("marshal timeline group summary entities: %w", err)
	}
	timelineJSON, err := json.Marshal(summary.Timeline)
	if err != nil {
		return fmt.Errorf("marshal timeline group summary timeline: %w", err)
	}
	now := time.Now().UnixMilli()
	_, err = s.db.Exec(`
		INSERT INTO activity_timeline_group_summaries (
			group_id, app_key, app_name, start_ms, end_ms,
			title, description, activity_type, entities, timeline_json,
			model, prompt, response_raw, created_at, updated_at
		) SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM activity_retention_sources_v1
			WHERE source_kind='activity_run' AND source_id=?)
		ON CONFLICT(group_id) DO UPDATE SET
			app_key = excluded.app_key,
			app_name = excluded.app_name,
			start_ms = excluded.start_ms,
			end_ms = excluded.end_ms,
			title = excluded.title,
			description = excluded.description,
			activity_type = excluded.activity_type,
			entities = excluded.entities,
			timeline_json = excluded.timeline_json,
			model = excluded.model,
			prompt = excluded.prompt,
			response_raw = excluded.response_raw,
			updated_at = excluded.updated_at
	`, group.ID, group.AppKey, group.AppName, group.StartTime, group.EndTime,
		summary.Title, summary.Description, summary.ActivityType, string(entitiesJSON), string(timelineJSON),
		model, prompt, responseRaw, now, now, group.ID)
	if err != nil {
		return fmt.Errorf("upsert timeline group summary: %w", err)
	}
	return nil
}

// GetSessionSegments returns all video segments for a given session.
// Returns nil for protected sessions (default read policy).
func (s *ActivityStore) GetSessionSegments(sessionID string) ([]ActivitySegmentRow, error) {
	if s.isSessionProtected(sessionID) {
		return nil, nil
	}
	return s.GetSessionSegmentsIncludingProtected(sessionID)
}

// GetSessionSegmentsIncludingProtected returns segments regardless of the
// session's protected state. Reserved for retention/cleanup and the
// Activity detail UI; never feed an LLM or index.
func (s *ActivityStore) GetSessionSegmentsIncludingProtected(sessionID string) ([]ActivitySegmentRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, session_id, start_time, end_time, file_path, width, height,
			fps, codec, size_bytes, storage_tier
		FROM activity_segments
		WHERE session_id = ?
		ORDER BY start_time`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query segments: %w", err)
	}
	defer rows.Close()

	var segments []ActivitySegmentRow
	for rows.Next() {
		var seg ActivitySegmentRow
		if err := rows.Scan(&seg.ID, &seg.SessionID, &seg.StartTime, &seg.EndTime,
			&seg.FilePath, &seg.Width, &seg.Height, &seg.FPS, &seg.Codec,
			&seg.SizeBytes, &seg.StorageTier); err != nil {
			return nil, fmt.Errorf("scan segment: %w", err)
		}
		segments = append(segments, seg)
	}
	return segments, rows.Err()
}

// UpdateTriageStatus updates the activity-pipeline triage status of a session.
// A queued triager must not undo privacy protection applied after it read the row.
func (s *ActivityStore) UpdateTriageStatus(sessionID, status string) error {
	result, err := s.db.Exec(`
		UPDATE activity_sessions
		SET triage_status = ?
		WHERE id = ? AND COALESCE(triage_status, '') != 'protected'`, status, sessionID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed == 1 && status == "protected" {
		publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: sessionID})
	}
	return err
}

// ResetTriagingToPending recovers sessions stuck in the transient "triaging"
// state back to "pending" so the triager picks them up on the next tick.
// Rows enter "triaging" when triageGroup starts AI triage and
// should normally transition to dismissed/pending before the
// function returns. They can leak if the process is killed mid-triage or if
// the per-session UpdateTriageStatus call silently failed (e.g. DB locked).
// Called at triager startup to drain any leaks from prior runs.
func (s *ActivityStore) ResetTriagingToPending() (int64, error) {
	res, err := s.db.Exec(`
		UPDATE activity_sessions
		SET triage_status = 'pending'
		WHERE triage_status = 'triaging'`)
	if err != nil {
		return 0, fmt.Errorf("reset triaging sessions: %w", err)
	}
	return res.RowsAffected()
}

// ResetTriagingSessionsByIDs is a scoped variant of ResetTriagingToPending that
// only touches sessions whose IDs are in the supplied set AND are still in the
// "triaging" state. Used by triageGroup's deferred cleanup to
// recover any sessions left stranded by failed UpdateTriageStatus writes or
// panics, without disturbing sessions that already reached a terminal state.
func (s *ActivityStore) ResetTriagingSessionsByIDs(ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := `UPDATE activity_sessions SET triage_status = 'pending' WHERE triage_status = 'triaging' AND id IN (` + placeholders + `)`
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("reset triaging sessions by ids: %w", err)
	}
	return res.RowsAffected()
}

// UpdateMeetingStatus marks a session as a meeting and stores the meeting app
// name and transcript file path.
// ClearMeetingRecording forgets a session's call recording and transcript
// file (the caller removes the files).
func (s *ActivityStore) ClearMeetingRecording(sessionID string) error {
	_, err := s.db.Exec(`
		UPDATE activity_sessions
		SET transcript_path = '', audio_path = '', audio_size_bytes = 0
		WHERE id = ?`, sessionID)
	return err
}

func (s *ActivityStore) UpdateMeetingStatus(sessionID, meetingApp, transcriptPath string) error {
	_, err := s.db.Exec(`
		UPDATE activity_sessions
		SET is_meeting = 1, meeting_app = ?, transcript_path = ?
		WHERE id = ?`, meetingApp, transcriptPath, sessionID)
	return err
}

// UpdateAutomationStatus updates the automation extraction status of a session.
func (s *ActivityStore) UpdateAutomationStatus(sessionID, status string) error {
	_, err := s.db.Exec(`
		UPDATE activity_sessions
		SET automation_status = ?
		WHERE id = ?`, status, sessionID)
	return err
}

// GetPendingSessions returns sessions that haven't been evaluated by triage.
func (s *ActivityStore) GetPendingSessions() ([]ActivitySessionRow, error) {
	return s.getSessionsByStatus("pending")
}

func (s *ActivityStore) getSessionsByStatus(status string) ([]ActivitySessionRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			ocr_status, COALESCE(automation_status, ''), COALESCE(handoff_status, ''),
			COALESCE(sop_status, ''), created_at
		FROM activity_sessions
		WHERE triage_status = ?
		ORDER BY start_time ASC`, status)
	if err != nil {
		return nil, fmt.Errorf("query sessions by status: %w", err)
	}
	defer rows.Close()

	var sessions []ActivitySessionRow
	for rows.Next() {
		var s ActivitySessionRow
		if err := rows.Scan(&s.ID, &s.StartTime, &s.EndTime, &s.TriggerKind,
			&s.AppNames, &s.EventCount, &s.SegmentCount, &s.SnapshotCount, &s.TotalSizeBytes,
			&s.TriageStatus, &s.OCRStatus, &s.AutomationStatus, &s.HandoffStatus,
			&s.SOPStatus, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// GetSessionsByAge returns sessions ordered by start_time ascending (oldest first).
func (s *ActivityStore) GetSessionsByAge() ([]ActivitySessionRow, error) {
	return s.querySessionsByAge("")
}

// GetUntranscribedMeetings returns only durable call recordings still waiting
// for their transcript. Recovery has no age cutoff: a failed ASR restart must
// never age a recording out of the retry queue.
func (s *ActivityStore) GetUntranscribedMeetings() ([]ActivitySessionRow, error) {
	return s.querySessionsByAge("WHERE is_meeting = 1 AND audio_path <> '' AND COALESCE(transcript_path, '') = '' AND triage_status <> 'protected'")
}

// GetSessionsOlderThan returns sessions whose start_time predates thresholdMs,
// ordered ASC (oldest first). Used by the rotator's age-based purge so the
// filter happens in SQLite instead of pulling the entire activity_sessions
// table into memory just to discard the bulk of it.
func (s *ActivityStore) GetSessionsOlderThan(thresholdMs int64) ([]ActivitySessionRow, error) {
	return s.querySessionsByAge("WHERE start_time < ?", thresholdMs)
}

// querySessionsByAge backs the age and pending-recording reads. Callers supply
// an internal WHERE clause and its bound arguments; no user SQL enters here.
func (s *ActivityStore) querySessionsByAge(where string, args ...any) ([]ActivitySessionRow, error) {
	query := `
		SELECT id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			ocr_status, COALESCE(automation_status, ''), COALESCE(handoff_status, ''),
			COALESCE(sop_status, ''),
			COALESCE(is_meeting, 0), COALESCE(meeting_app, ''), COALESCE(transcript_path, ''),
			COALESCE(audio_path, ''), COALESCE(audio_size_bytes, 0),
			created_at
		FROM activity_sessions ` + where + `
		ORDER BY start_time ASC`

	rows, err := s.reader().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query sessions by age: %w", err)
	}
	defer rows.Close()

	var sessions []ActivitySessionRow
	for rows.Next() {
		var s ActivitySessionRow
		var isMeeting int
		if err := rows.Scan(&s.ID, &s.StartTime, &s.EndTime, &s.TriggerKind,
			&s.AppNames, &s.EventCount, &s.SegmentCount, &s.SnapshotCount, &s.TotalSizeBytes,
			&s.TriageStatus, &s.OCRStatus, &s.AutomationStatus, &s.HandoffStatus,
			&s.SOPStatus,
			&isMeeting, &s.MeetingApp, &s.TranscriptPath,
			&s.AudioPath, &s.AudioSizeBytes,
			&s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		s.IsMeeting = isMeeting != 0
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// DeleteSession removes source data and any day/week summaries that could
// contain it. In-memory derivatives observe the revision without SQL on chat.
func (s *ActivityStore) DeleteSession(sessionID string) error {
	return s.deleteSession(sessionID, false)
}

// Retention removes raw captures while retaining generated journals and their
// minimal app/time provenance. Explicit user deletion keeps its stronger purge.
func (s *ActivityStore) deleteRetainedSession(sessionID string) error {
	return s.deleteSession(sessionID, true)
}

func (s *ActivityStore) deleteSession(sessionID string, retainSummaries bool) error {
	deleted := false
	defer func() {
		if !deleted {
			return
		}
		scope := privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: sessionID}
		if retainSummaries {
			// Retention is not a privacy action (seal v2 rule 2): readers drop
			// their raw copies, but no sealed brief is amended.
			publishRetentionRemoval(scope)
			return
		}
		publishPrivacyWithdrawal(scope)
	}()
	s.readPrivacyMu.Lock()
	defer s.readPrivacyMu.Unlock()
	s.readPrivacyRevision.Add(1)
	defer s.readPrivacyRevision.Add(1)
	if retainSummaries {
		session, err := s.GetSessionByID(sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.preserveLegacySummaryProvenance(session.StartTime, session.EndTime); err != nil {
			return err
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !retainSummaries {
		// Explicit deletion must not later look like legacy retention. Keep a
		// content-free denial even when a prior retention removed the raw row.
		if err := saveActivitySourcePrivacyReceipt(tx, activitySourceProvenance{SessionID: sessionID, Private: true}); err != nil {
			return err
		}
		// A deleted call's notes go with it, even after retention removed
		// its raw row; retention itself keeps them.
		if err := deleteMeetingNoteSlicesOfSession(tx, sessionID); err != nil {
			return fmt.Errorf("delete meeting notes of deleted session: %w", err)
		}
	}
	var startMs, endMs int64
	if err := tx.QueryRow(`SELECT start_time, end_time FROM activity_sessions WHERE id = ?`, sessionID).Scan(&startMs, &endMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		return err
	}
	if retainSummaries {
		// Read the final source facts and retain them in the same transaction
		// that removes the raw rows. A concurrent capture cannot slip between
		// the privacy proof and deletion.
		if err := s.retainSummarySourcePrivacy(tx, sessionID); err != nil {
			return err
		}
		if err := recordSessionRetentionReceipt(tx, sessionID); err != nil {
			return err
		}
	}
	start := time.UnixMilli(startMs).In(time.Local)
	end := time.UnixMilli(max(startMs, endMs)).In(time.Local)
	weekKey := func(day time.Time) string {
		daysSinceMonday := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -daysSinceMonday).Format("2006-01-02") + "/week"
	}
	// Delete every language variant, including moments stored on the daily
	// row. Cross-midnight and cross-week sessions invalidate both intervals.
	if !retainSummaries {
		if _, err := tx.Exec(`DELETE FROM activity_summaries
		WHERE (source_provenance='' AND ((type = 'daily' AND date >= ? AND date <= ?)
		   OR (type = 'weekly' AND date >= ? AND date <= ?)))
           OR EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(source_provenance) THEN source_provenance ELSE '[]' END) WHERE json_extract(value,'$.sessionId')=?)`,
			start.Format("2006-01-02"), end.Format("2006-01-02"), weekKey(start), weekKey(end), sessionID); err != nil {
			return fmt.Errorf("invalidate summaries for deleted session: %w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM activity_sessions WHERE id = ?`, sessionID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.readPrivacySessionRevisions == nil {
		s.readPrivacySessionRevisions = make(map[string]uint64)
	}
	s.readPrivacySessionRevisions[sessionID] = s.readPrivacyRevision.Load() + 1
	deleted = true
	return nil
}

// GetTotalStorageBytes returns the sum of all on-disk activity bytes:
// segment files, snapshot files, and per-session audio files. Audio lives on
// activity_sessions (audio_size_bytes) rather than its own table, so it must
// be summed explicitly — otherwise quota enforcement undercounts meeting /
// audio-only recordings and the configured cap is silently bypassed.
func (s *ActivityStore) GetTotalStorageBytes() (int64, error) {
	var segTotal, snapTotal, audioTotal sql.NullInt64
	if err := s.reader().QueryRow("SELECT SUM(size_bytes) FROM activity_segments").Scan(&segTotal); err != nil {
		return 0, fmt.Errorf("sum segment bytes: %w", err)
	}
	if err := s.reader().QueryRow("SELECT SUM(size_bytes) FROM activity_snapshots").Scan(&snapTotal); err != nil {
		return 0, fmt.Errorf("sum snapshot bytes: %w", err)
	}
	if err := s.reader().QueryRow("SELECT SUM(audio_size_bytes) FROM activity_sessions").Scan(&audioTotal); err != nil {
		return 0, fmt.Errorf("sum audio bytes: %w", err)
	}
	var total int64
	if segTotal.Valid {
		total += segTotal.Int64
	}
	if snapTotal.Valid {
		total += snapTotal.Int64
	}
	if audioTotal.Valid {
		total += audioTotal.Int64
	}
	return total, nil
}

// ActivityStorageStats summarizes how much activity data is on disk and is
// subject to user-configured retention. Every session is subject to retention.
type ActivityStorageStats struct {
	RetainedBytes        int64 `json:"retainedBytes"`
	RetainedSessionCount int   `json:"retainedSessionCount"`
	// EarliestRetainedMs is the start_time (Unix ms) of the oldest
	// session, or 0 when no session exists.
	EarliestRetainedMs int64 `json:"earliestRetainedMs"`
}

// GetActivityStorageStats returns aggregated storage usage for the
// Settings → Activity panel: total bytes / session count subject to retention,
// plus the earliest session start_time so the UI can show "your oldest
// recording is from X". audio_size_bytes lives on activity_sessions while
// segment / snapshot bytes live on their own tables, so we issue three grouped
// queries and sum the results.
func (s *ActivityStore) GetActivityStorageStats() (ActivityStorageStats, error) {
	var stats ActivityStorageStats

	// (1) Sessions: count + earliest start_time + audio bytes.
	if err := s.scanSessionAggregates(&stats); err != nil {
		return stats, err
	}

	// (2 & 3) Segment + snapshot bytes.
	for _, query := range []string{
		`SELECT COALESCE(SUM(size_bytes), 0) FROM activity_segments`,
		`SELECT COALESCE(SUM(size_bytes), 0) FROM activity_snapshots`,
	} {
		var bytes int64
		if err := s.reader().QueryRow(query).Scan(&bytes); err != nil {
			return stats, fmt.Errorf("query blob bytes: %w", err)
		}
		stats.RetainedBytes += bytes
	}

	return stats, nil
}

// scanSessionAggregates fills the session-count / earliest-start / audio-bytes
// portion of stats. Split out so we can use defer rows.Close() and surface
// rows.Err() without nesting cleanup inside GetActivityStorageStats.
func (s *ActivityStore) scanSessionAggregates(stats *ActivityStorageStats) error {
	err := s.reader().QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(MIN(start_time), 0),
			COALESCE(SUM(audio_size_bytes), 0)
		FROM activity_sessions
	`).Scan(&stats.RetainedSessionCount, &stats.EarliestRetainedMs, &stats.RetainedBytes)
	if err != nil {
		return fmt.Errorf("query session aggregates: %w", err)
	}
	return nil
}

// UpdateSegmentTier updates the storage tier for a segment.
func (s *ActivityStore) UpdateSegmentTier(segmentID int64, tier string) error {
	_, err := s.db.Exec("UPDATE activity_segments SET storage_tier = ? WHERE id = ?", tier, segmentID)
	return err
}

// UpdateSegmentSize updates the size after recompression.
func (s *ActivityStore) UpdateSegmentSize(segmentID int64, newSize int64) error {
	_, err := s.db.Exec("UPDATE activity_segments SET size_bytes = ? WHERE id = ?", newSize, segmentID)
	return err
}

// Vacuum runs VACUUM on the database to reclaim space.
func (s *ActivityStore) Vacuum() error {
	_, err := s.db.Exec("VACUUM")
	return err
}

// CheckpointTruncate runs `PRAGMA wal_checkpoint(TRUNCATE)`, which writes all
// committed WAL frames back to the main DB and shrinks the WAL file to zero.
// Returns (busy, logFrames, checkpointedFrames, err) per SQLite's
// pragma contract: busy != 0 means an active reader prevented the full
// truncate (the checkpoint still ran as far as it could).
func (s *ActivityStore) CheckpointTruncate() (int, int, int, error) {
	var busy, logFrames, ckptFrames int
	err := s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &ckptFrames)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("wal_checkpoint(TRUNCATE): %w", err)
	}
	return busy, logFrames, ckptFrames, nil
}

// GetSegmentsByTier returns segments in the given storage tier.
func (s *ActivityStore) GetSegmentsByTier(tier string) ([]ActivitySegmentRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, session_id, start_time, end_time, file_path, width, height,
			fps, codec, size_bytes, storage_tier
		FROM activity_segments
		WHERE storage_tier = ?
		ORDER BY start_time`, tier)
	if err != nil {
		return nil, fmt.Errorf("query segments by tier: %w", err)
	}
	defer rows.Close()

	var segments []ActivitySegmentRow
	for rows.Next() {
		var seg ActivitySegmentRow
		if err := rows.Scan(&seg.ID, &seg.SessionID, &seg.StartTime, &seg.EndTime,
			&seg.FilePath, &seg.Width, &seg.Height, &seg.FPS, &seg.Codec,
			&seg.SizeBytes, &seg.StorageTier); err != nil {
			return nil, fmt.Errorf("scan segment: %w", err)
		}
		segments = append(segments, seg)
	}
	return segments, rows.Err()
}

// GetSegmentsOlderThan returns segments with start_time before the given threshold.
func (s *ActivityStore) GetSegmentsOlderThan(threshold time.Time) ([]ActivitySegmentRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, session_id, start_time, end_time, file_path, width, height,
			fps, codec, size_bytes, storage_tier
		FROM activity_segments
		WHERE start_time < ?
		ORDER BY start_time`, threshold.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("query old segments: %w", err)
	}
	defer rows.Close()

	var segments []ActivitySegmentRow
	for rows.Next() {
		var seg ActivitySegmentRow
		if err := rows.Scan(&seg.ID, &seg.SessionID, &seg.StartTime, &seg.EndTime,
			&seg.FilePath, &seg.Width, &seg.Height, &seg.FPS, &seg.Codec,
			&seg.SizeBytes, &seg.StorageTier); err != nil {
			return nil, fmt.Errorf("scan segment: %w", err)
		}
		segments = append(segments, seg)
	}
	return segments, rows.Err()
}

// SegmentsDir returns the path to the segments storage directory.
func (s *ActivityStore) SegmentsDir() string {
	return s.segmentsDir
}

// EventsDir returns the path to the events storage directory.
func (s *ActivityStore) EventsDir() string {
	return s.eventsDir
}

// SnapshotsDir returns the path to the snapshots storage directory.
func (s *ActivityStore) SnapshotsDir() string {
	return s.snapshotsDir
}

// AudioDir returns the path to the meeting audio storage directory.
func (s *ActivityStore) AudioDir() string {
	return s.audioDir
}

// AttachMeetingRecording records a recovered recording on the session it
// belongs to, unless that session has one.
func (s *ActivityStore) AttachMeetingRecording(sessionID, audioPath string, sizeBytes int64) error {
	_, err := s.db.Exec(`UPDATE activity_sessions SET audio_path = ?, audio_size_bytes = ?
		WHERE id = ? AND COALESCE(audio_path, '') = ''`, audioPath, sizeBytes, sessionID)
	return err
}

func (s *ActivityStore) getSessionTranscriptPath(sessionID string) string {
	var path string
	_ = s.reader().QueryRow(`SELECT COALESCE(transcript_path, '') FROM activity_sessions WHERE id = ?`, sessionID).Scan(&path)
	return path
}

func (s *ActivityStore) getSessionAudioPath(sessionID string) string {
	var path string
	_ = s.reader().QueryRow(`SELECT COALESCE(audio_path, '') FROM activity_sessions WHERE id = ?`, sessionID).Scan(&path)
	return path
}

// SaveSnapshots batch-inserts snapshot rows within a transaction.
func (s *ActivityStore) SaveSnapshots(tx *sql.Tx, snapshots []ActivitySnapshotRow) error {
	if len(snapshots) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`
		INSERT INTO activity_snapshots (session_id, timestamp, file_path, width, height,
			size_bytes, trigger_kind, storage_tier, ax_text, browser_url,
			content_type, app, parsed_json,
			focus_rect_x, focus_rect_y, focus_rect_w, focus_rect_h)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare snapshot insert: %w", err)
	}
	defer stmt.Close()

	for i, snap := range snapshots {
		if _, err := stmt.Exec(snap.SessionID, snap.Timestamp, snap.FilePath,
			snap.Width, snap.Height, snap.SizeBytes, snap.Trigger, snap.StorageTier,
			snap.AXText, snap.BrowserURL,
			snap.ContentType, snap.App, snap.ParsedJSON,
			snap.FocusRectX, snap.FocusRectY, snap.FocusRectW, snap.FocusRectH); err != nil {
			return fmt.Errorf("insert snapshot %d: %w", i, err)
		}
	}
	return nil
}

// GetSessionSnapshots returns all snapshots for a session, ordered by timestamp.
// Returns nil for protected sessions (default read policy).
func (s *ActivityStore) GetSessionSnapshots(sessionID string) ([]ActivitySnapshotRow, error) {
	if s.isSessionProtected(sessionID) {
		return nil, nil
	}
	return s.GetSessionSnapshotsIncludingProtected(sessionID)
}

// GetSessionSnapshotsIncludingProtected returns snapshots regardless of the
// session's protected state. Reserved for retention/cleanup and the
// Activity detail UI; never feed an LLM or index.
func (s *ActivityStore) GetSessionSnapshotsIncludingProtected(sessionID string) ([]ActivitySnapshotRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, session_id, timestamp, file_path, width, height,
			size_bytes, COALESCE(trigger_kind, ''), storage_tier,
			COALESCE(ax_text, ''), COALESCE(browser_url, ''),
			COALESCE(content_type, ''), COALESCE(app, ''), COALESCE(parsed_json, ''),
			COALESCE(focus_rect_x, 0), COALESCE(focus_rect_y, 0),
			COALESCE(focus_rect_w, 0), COALESCE(focus_rect_h, 0),
			COALESCE(ocr_used_focus_rect, 0)
		FROM activity_snapshots
		WHERE session_id = ?
		ORDER BY timestamp`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []ActivitySnapshotRow
	for rows.Next() {
		var snap ActivitySnapshotRow
		var ocrUsedFocusRect int
		if err := rows.Scan(&snap.ID, &snap.SessionID, &snap.Timestamp, &snap.FilePath,
			&snap.Width, &snap.Height, &snap.SizeBytes, &snap.Trigger, &snap.StorageTier,
			&snap.AXText, &snap.BrowserURL,
			&snap.ContentType, &snap.App, &snap.ParsedJSON,
			&snap.FocusRectX, &snap.FocusRectY, &snap.FocusRectW, &snap.FocusRectH,
			&ocrUsedFocusRect); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		snap.OCRUsedFocusRect = ocrUsedFocusRect != 0
		snapshots = append(snapshots, snap)
	}
	return snapshots, rows.Err()
}

// GetSnapshotsByTier returns snapshots in the given storage tier.
func (s *ActivityStore) GetSnapshotsByTier(tier string) ([]ActivitySnapshotRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, session_id, timestamp, file_path, width, height,
			size_bytes, COALESCE(trigger_kind, ''), storage_tier,
			COALESCE(ax_text, ''), COALESCE(browser_url, ''),
			COALESCE(content_type, ''), COALESCE(app, ''), COALESCE(parsed_json, ''),
			COALESCE(focus_rect_x, 0), COALESCE(focus_rect_y, 0),
			COALESCE(focus_rect_w, 0), COALESCE(focus_rect_h, 0),
			COALESCE(ocr_used_focus_rect, 0)
		FROM activity_snapshots
		WHERE storage_tier = ?
		ORDER BY timestamp`, tier)
	if err != nil {
		return nil, fmt.Errorf("query snapshots by tier: %w", err)
	}
	defer rows.Close()

	var snapshots []ActivitySnapshotRow
	for rows.Next() {
		var snap ActivitySnapshotRow
		var ocrUsedFocusRect int
		if err := rows.Scan(&snap.ID, &snap.SessionID, &snap.Timestamp, &snap.FilePath,
			&snap.Width, &snap.Height, &snap.SizeBytes, &snap.Trigger, &snap.StorageTier,
			&snap.AXText, &snap.BrowserURL,
			&snap.ContentType, &snap.App, &snap.ParsedJSON,
			&snap.FocusRectX, &snap.FocusRectY, &snap.FocusRectW, &snap.FocusRectH,
			&ocrUsedFocusRect); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		snap.OCRUsedFocusRect = ocrUsedFocusRect != 0
		snapshots = append(snapshots, snap)
	}
	return snapshots, rows.Err()
}

// GetLatestSnapshot returns the most recent snapshot across all sessions, or
// nil if none exist. Used by the quick-chat focused-app capture path to read
// back a freshly-triggered snapshot regardless of which session it lands in.
func (s *ActivityStore) GetLatestSnapshot() (*ActivitySnapshotRow, error) {
	row := s.reader().QueryRow(`
		SELECT id, session_id, timestamp, file_path, width, height,
			size_bytes, COALESCE(trigger_kind, ''), storage_tier,
			COALESCE(ax_text, ''), COALESCE(browser_url, ''),
			COALESCE(content_type, ''), COALESCE(app, ''), COALESCE(parsed_json, ''),
			COALESCE(focus_rect_x, 0), COALESCE(focus_rect_y, 0),
			COALESCE(focus_rect_w, 0), COALESCE(focus_rect_h, 0),
			COALESCE(ocr_used_focus_rect, 0)
		FROM activity_snapshots
		ORDER BY timestamp DESC
		LIMIT 1`)
	var snap ActivitySnapshotRow
	var ocrUsedFocusRect int
	if err := row.Scan(&snap.ID, &snap.SessionID, &snap.Timestamp, &snap.FilePath,
		&snap.Width, &snap.Height, &snap.SizeBytes, &snap.Trigger, &snap.StorageTier,
		&snap.AXText, &snap.BrowserURL,
		&snap.ContentType, &snap.App, &snap.ParsedJSON,
		&snap.FocusRectX, &snap.FocusRectY, &snap.FocusRectW, &snap.FocusRectH,
		&ocrUsedFocusRect); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan latest snapshot: %w", err)
	}
	snap.OCRUsedFocusRect = ocrUsedFocusRect != 0
	return &snap, nil
}

// GetRecentSnapshotsForApp returns up to `limit` recent snapshots from
// sessions whose AppNames JSON includes the given display name (e.g. "Discord",
// "Slack"). Used by the quick-chat per-app suggestion path for "previous
// activity in this app" context (Tier 2). Filters by JSON-quoted match so
// "Mail" doesn't match "MailMate".
func (s *ActivityStore) GetRecentSnapshotsForApp(appDisplayName string, sinceMs int64, limit int) ([]ActivitySnapshotRow, error) {
	if appDisplayName == "" || limit <= 0 {
		return nil, nil
	}
	pattern := `%"` + strings.ReplaceAll(strings.ReplaceAll(appDisplayName, `\`, `\\`), `"`, `\"`) + `"%`
	rows, err := s.reader().Query(`
		SELECT s.id, s.session_id, s.timestamp, s.file_path, s.width, s.height,
			s.size_bytes, COALESCE(s.trigger_kind, ''), s.storage_tier,
			COALESCE(s.ax_text, ''), COALESCE(s.browser_url, ''),
			COALESCE(s.content_type, ''), COALESCE(s.app, ''), COALESCE(s.parsed_json, ''),
			COALESCE(s.focus_rect_x, 0), COALESCE(s.focus_rect_y, 0),
			COALESCE(s.focus_rect_w, 0), COALESCE(s.focus_rect_h, 0),
			COALESCE(s.ocr_used_focus_rect, 0)
		FROM activity_snapshots s
		JOIN activity_sessions sess ON sess.id = s.session_id
		WHERE sess.app_names LIKE ? AND s.timestamp >= ?
		AND COALESCE(sess.triage_status, '') != 'protected'
		ORDER BY s.timestamp DESC
		LIMIT ?`, pattern, sinceMs, limit)
	if err != nil {
		return nil, fmt.Errorf("query snapshots for app: %w", err)
	}
	defer rows.Close()

	var snapshots []ActivitySnapshotRow
	for rows.Next() {
		var snap ActivitySnapshotRow
		var ocrUsedFocusRect int
		if err := rows.Scan(&snap.ID, &snap.SessionID, &snap.Timestamp, &snap.FilePath,
			&snap.Width, &snap.Height, &snap.SizeBytes, &snap.Trigger, &snap.StorageTier,
			&snap.AXText, &snap.BrowserURL,
			&snap.ContentType, &snap.App, &snap.ParsedJSON,
			&snap.FocusRectX, &snap.FocusRectY, &snap.FocusRectW, &snap.FocusRectH,
			&ocrUsedFocusRect); err != nil {
			return nil, fmt.Errorf("scan snapshot for app: %w", err)
		}
		snap.OCRUsedFocusRect = ocrUsedFocusRect != 0
		snapshots = append(snapshots, snap)
	}
	return snapshots, rows.Err()
}

// UpdateSnapshotTier updates the storage tier for a snapshot.
// A row marked image-missing keeps that mark; only the startup reconcile
// clears it, when the file is back.
func (s *ActivityStore) UpdateSnapshotTier(snapshotID int64, tier string) error {
	_, err := s.db.Exec("UPDATE activity_snapshots SET storage_tier = ? WHERE id = ? AND storage_tier != ?",
		tier, snapshotID, snapshotTierMissing)
	return err
}

// MarkSnapshotMissingByFilePath marks the snapshot row for relPath as
// image-missing (tier "missing", size 0) and recounts its session over rows
// with a file. The row keeps its text. Used when the screenshot's masked
// file was never written. Reports whether a row was marked.
func (s *ActivityStore) MarkSnapshotMissingByFilePath(relPath string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var sessionID sql.NullString
	var sizeBytes int64
	err = tx.QueryRow(`
		SELECT session_id, COALESCE(size_bytes, 0) FROM activity_snapshots
		WHERE file_path = ? AND storage_tier != ? LIMIT 1`, relPath, snapshotTierMissing).
		Scan(&sessionID, &sizeBytes)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find snapshot: %w", err)
	}
	if _, err := tx.Exec(`UPDATE activity_snapshots SET storage_tier = ?, size_bytes = 0 WHERE file_path = ?`,
		snapshotTierMissing, relPath); err != nil {
		return false, fmt.Errorf("mark snapshot: %w", err)
	}
	if sessionID.Valid {
		if _, err := tx.Exec(`
			UPDATE activity_sessions
			SET snapshot_count = (SELECT COUNT(*) FROM activity_snapshots WHERE session_id = ? AND storage_tier != ?),
				total_size_bytes = MAX(total_size_bytes - ?, 0)
			WHERE id = ?`, sessionID.String, snapshotTierMissing, sizeBytes, sessionID.String); err != nil {
			return false, fmt.Errorf("update session counts: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// UpdateSnapshotSizeByFilePath sets the size of the snapshot row for relPath
// and moves its session's total by the difference. Used when the PII-masked
// JPEG lands after its row was committed with the captured size.
func (s *ActivityStore) UpdateSnapshotSizeByFilePath(relPath string, sizeBytes int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var sessionID sql.NullString
	var oldSize int64
	err = tx.QueryRow(`SELECT session_id, COALESCE(size_bytes, 0) FROM activity_snapshots WHERE file_path = ? LIMIT 1`, relPath).
		Scan(&sessionID, &oldSize)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find snapshot: %w", err)
	}
	if _, err := tx.Exec(`UPDATE activity_snapshots SET size_bytes = ? WHERE file_path = ?`, sizeBytes, relPath); err != nil {
		return fmt.Errorf("update snapshot size: %w", err)
	}
	if sessionID.Valid {
		if _, err := tx.Exec(`
			UPDATE activity_sessions SET total_size_bytes = MAX(total_size_bytes + ?, 0)
			WHERE id = ?`, sizeBytes-oldSize, sessionID.String); err != nil {
			return fmt.Errorf("update session size: %w", err)
		}
	}
	return tx.Commit()
}

// HasImage reports whether the row's screenshot file can be shown or read.
// A row marked image-missing keeps its text but has no image.
func (r ActivitySnapshotRow) HasImage() bool {
	return strings.TrimSpace(r.FilePath) != "" && r.StorageTier != snapshotTierMissing
}

// snapshotTierMissing is the storage_tier of a snapshot row whose JPEG is not
// on disk. The row keeps its AX text and parsed content; only the image is
// gone, so the Activity screen shows a placeholder and the OCR worker skips
// it. The rotator only moves hot and warm rows, so it leaves these alone.
const snapshotTierMissing = "missing"

// SnapshotFileReconcile counts what ReconcileSnapshotFiles changed.
type SnapshotFileReconcile struct {
	Marked       int // rows whose JPEG is gone, now marked image-missing
	Restored     int // image-missing rows whose JPEG is back
	FilesRemoved int // JPEGs and temp files with no row
}

// ReconcileSnapshotFiles brings snapshot rows from before beforeMs in line
// with the JPEGs on disk:
//   - a row whose JPEG is gone is marked image-missing (tier "missing", size 0)
//     and keeps its AX text and parsed content;
//   - an image-missing row whose JPEG is back gets its tier and size again;
//   - a JPEG or temporary file with no row is removed. Such files are frames
//     the app wrote but never committed (a quit before the session was
//     saved) or half-written temp files; they are not referenced anywhere.
//
// Sessions with changed rows are recounted over rows with a file. Rows and
// files from beforeMs on belong to the running app and are left alone.
func (s *ActivityStore) ReconcileSnapshotFiles(beforeMs int64) (SnapshotFileReconcile, error) {
	return s.ReconcileSnapshotFilesExcept(beforeMs, nil)
}

// ReconcileSnapshotFilesExcept is ReconcileSnapshotFiles for a running app:
// a file for which inUse reports true (a frame the capture still holds,
// whose row is written when its session ends) is never removed, and each
// file is checked against the rows again right before it goes.
func (s *ActivityStore) ReconcileSnapshotFilesExcept(beforeMs int64, inUse func(relPath string) bool) (SnapshotFileReconcile, error) {
	var result SnapshotFileReconcile

	type snapshotFileRow struct {
		id        int64
		sessionID string
		relPath   string
		size      int64
		tier      string
		timestamp int64
	}
	// Read every row first and close the cursor: this database runs on one
	// connection, and holding it across the directory listings below would
	// block the UI, the OCR worker and session commits.
	rows, err := s.reader().Query(`
		SELECT id, COALESCE(session_id, ''), file_path, COALESCE(size_bytes, 0), storage_tier, timestamp
		FROM activity_snapshots
		WHERE file_path != ''`)
	if err != nil {
		return result, fmt.Errorf("query snapshots: %w", err)
	}
	var all []snapshotFileRow
	for rows.Next() {
		var r snapshotFileRow
		if err := rows.Scan(&r.id, &r.sessionID, &r.relPath, &r.size, &r.tier, &r.timestamp); err != nil {
			rows.Close()
			return result, fmt.Errorf("scan snapshot: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("iterate snapshots: %w", err)
	}
	rows.Close()

	referenced := make(map[string]bool, len(all))
	for _, r := range all {
		referenced[filepath.Clean(r.relPath)] = true
	}

	// One directory listing per date folder instead of a stat per row. A
	// folder that cannot be read is left out entirely, so its rows and files
	// stay as they are.
	type dirListing struct {
		readable bool
		files    map[string]os.FileInfo
	}
	listings := make(map[string]dirListing)
	listDir := func(dir string) dirListing {
		if l, ok := listings[dir]; ok {
			return l
		}
		l := dirListing{files: map[string]os.FileInfo{}}
		entries, err := os.ReadDir(filepath.Join(s.snapshotsDir, dir))
		if err == nil || os.IsNotExist(err) {
			l.readable = true
			for _, e := range entries {
				if info, infoErr := e.Info(); infoErr == nil && info.Mode().IsRegular() {
					l.files[e.Name()] = info
				}
			}
		}
		listings[dir] = l
		return l
	}

	type tierChange struct {
		id   int64
		tier string
		size int64
	}
	var changes []tierChange
	// Size change per session, applied to its total_size_bytes.
	sizeDelta := make(map[string]int64)
	for _, r := range all {
		if r.timestamp >= beforeMs {
			continue
		}
		dir, name := filepath.Split(filepath.Clean(r.relPath))
		l := listDir(dir)
		if !l.readable {
			continue
		}
		info, present := l.files[name]
		var c tierChange
		switch {
		case !present && r.tier != snapshotTierMissing:
			c = tierChange{id: r.id, tier: snapshotTierMissing, size: 0}
			result.Marked++
		case present && r.tier == snapshotTierMissing:
			c = tierChange{id: r.id, tier: "hot", size: info.Size()}
			result.Restored++
		default:
			continue
		}
		changes = append(changes, c)
		if r.sessionID != "" {
			sizeDelta[r.sessionID] += c.size - r.size
		}
	}

	if len(changes) > 0 {
		tx, err := s.db.Begin()
		if err != nil {
			return result, fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		for _, c := range changes {
			if _, err := tx.Exec(`UPDATE activity_snapshots SET storage_tier = ?, size_bytes = ? WHERE id = ?`,
				c.tier, c.size, c.id); err != nil {
				return result, fmt.Errorf("update snapshot %d: %w", c.id, err)
			}
		}
		for sessionID, delta := range sizeDelta {
			if _, err := tx.Exec(`
				UPDATE activity_sessions
				SET snapshot_count = (SELECT COUNT(*) FROM activity_snapshots WHERE session_id = ? AND storage_tier != ?),
					total_size_bytes = MAX(total_size_bytes + ?, 0)
				WHERE id = ?`, sessionID, snapshotTierMissing, delta, sessionID); err != nil {
				return result, fmt.Errorf("update session %s counts: %w", sessionID, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return result, fmt.Errorf("commit: %w", err)
		}
	}

	// Files with no row: only snapshot JPEGs and temp files in the date
	// folders, and only ones written before this run started.
	dateDirs, err := os.ReadDir(s.snapshotsDir)
	if err != nil {
		return result, nil
	}
	for _, d := range dateDirs {
		if !d.IsDir() || !isSnapshotDateDir(d.Name()) {
			continue
		}
		l := listDir(d.Name() + string(filepath.Separator))
		if !l.readable {
			continue
		}
		for name, info := range l.files {
			if info.ModTime().UnixMilli() >= beforeMs {
				continue
			}
			isTemp := strings.HasSuffix(name, ".jpg.tmp")
			rel := filepath.Join(d.Name(), name)
			if !isTemp && (!strings.HasSuffix(name, ".jpg") || referenced[rel]) {
				continue
			}
			if inUse != nil && inUse(strings.TrimSuffix(rel, ".tmp")) {
				continue
			}
			if !isTemp && s.snapshotRowExists(rel) {
				continue // its session was saved after the listing
			}
			// The listing can be stale by now: look again right before
			// removing, and leave the file alone if it is gone, no longer a
			// regular file, or changed (time or size) since it was listed.
			path := filepath.Join(s.snapshotsDir, d.Name(), name)
			fresh, err := os.Lstat(path)
			if err != nil || !fresh.Mode().IsRegular() ||
				!fresh.ModTime().Equal(info.ModTime()) || fresh.Size() != info.Size() {
				continue
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				log.Printf("[activity] failed to remove unreferenced snapshot file %s/%s: %v", d.Name(), name, err)
				continue
			}
			result.FilesRemoved++
		}
	}
	return result, nil
}

// snapshotRowExists reports whether a snapshot row refers to relPath (true
// when that cannot be read, so nothing is removed on doubt).
func (s *ActivityStore) snapshotRowExists(relPath string) bool {
	var n int
	if err := s.reader().QueryRow(`SELECT COUNT(*) FROM activity_snapshots WHERE file_path = ?`, relPath).Scan(&n); err != nil {
		return true
	}
	return n > 0
}

// isSnapshotDateDir reports whether name is a YYYY-MM-DD snapshot folder.
func isSnapshotDateDir(name string) bool {
	_, err := time.Parse("2006-01-02", name)
	return err == nil
}

// GetTotalSnapshotBytes returns the sum of all snapshot sizes.
func (s *ActivityStore) GetTotalSnapshotBytes() (int64, error) {
	var total sql.NullInt64
	err := s.reader().QueryRow("SELECT SUM(size_bytes) FROM activity_snapshots").Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// FindClosestSnapshot finds the snapshot closest to the target timestamp for a session.
func (s *ActivityStore) FindClosestSnapshot(sessionID string, targetTS int64) (*ActivitySnapshotRow, error) {
	var snap ActivitySnapshotRow
	var ocrUsedFocusRect int
	err := s.reader().QueryRow(`
		SELECT id, session_id, timestamp, file_path, width, height,
			size_bytes, COALESCE(trigger_kind, ''), storage_tier,
			COALESCE(ax_text, ''), COALESCE(browser_url, ''),
			COALESCE(content_type, ''), COALESCE(app, ''), COALESCE(parsed_json, ''),
			COALESCE(focus_rect_x, 0), COALESCE(focus_rect_y, 0),
			COALESCE(focus_rect_w, 0), COALESCE(focus_rect_h, 0),
			COALESCE(ocr_used_focus_rect, 0)
		FROM activity_snapshots
		WHERE session_id = ? AND storage_tier != ?
		ORDER BY ABS(timestamp - ?)
		LIMIT 1`, sessionID, snapshotTierMissing, targetTS).Scan(
		&snap.ID, &snap.SessionID, &snap.Timestamp, &snap.FilePath,
		&snap.Width, &snap.Height, &snap.SizeBytes, &snap.Trigger, &snap.StorageTier,
		&snap.AXText, &snap.BrowserURL,
		&snap.ContentType, &snap.App, &snap.ParsedJSON,
		&snap.FocusRectX, &snap.FocusRectY, &snap.FocusRectW, &snap.FocusRectH,
		&ocrUsedFocusRect,
	)
	if err != nil {
		return nil, err
	}
	snap.OCRUsedFocusRect = ocrUsedFocusRect != 0
	return &snap, nil
}

// MarkSnapshotOCRUsedFocusRect records that the OCR worker successfully scoped
// recognition to the focused-window rect for this snapshot (i.e. the cropper
// did NOT fall back to the full frame). Lets the audit UI surface a
// distinction between "rect known at capture" (focus_rect_*) and "rect was
// the actual OCR scope" — which is what users need to verify when debugging
// suspected context bleed.
func (s *ActivityStore) MarkSnapshotOCRUsedFocusRect(snapshotID int64) error {
	_, err := s.db.Exec(
		"UPDATE activity_snapshots SET ocr_used_focus_rect = 1 WHERE id = ?",
		snapshotID,
	)
	return err
}

// ── OCR methods ───────────────────────────────────────────────────────

// SaveOCRFrame persists a single OCR frame result. FTS5 is synced via triggers.
func (s *ActivityStore) SaveOCRFrame(frame OCRFrameRow) error {
	_, err := s.db.Exec(`
		INSERT INTO activity_ocr_frames (session_id, segment_id, frame_time, text, confidence, boxes_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		frame.SessionID, frame.SegmentID, frame.FrameTime, frame.Text, frame.Confidence, frame.BoxesJSON)
	return err
}

// GetOCRTextBySnapshot returns the OCR text recorded for a specific snapshot.
//
// The OCR worker samples up to ocrMaxSnapshotFramesPerRun frames per session
// (not every snapshot) so many snapshots have no OCR row of their own. When
// the exact snapshot has no text we fall back to the nearest sibling
// snapshot in the same session — chat / document surfaces are visually
// stable across consecutive snapshots, so the sibling's OCR is almost
// always a better signal than the empty "no text recognized" placeholder.
// The fallback only fires within the same session so unrelated activity
// can never leak across.
//
// Returns "" only when the session has no OCR data at all. Used by the
// activity-detail event popup.
func (s *ActivityStore) GetOCRTextBySnapshot(sessionID string, snapshotID int64) (string, error) {
	var text string
	err := s.reader().QueryRow(`
		SELECT text FROM activity_ocr_frames
		WHERE session_id = ? AND segment_id = ?
		ORDER BY id DESC
		LIMIT 1`, sessionID, snapshotID).Scan(&text)
	if err == nil && text != "" {
		return text, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("query ocr by snapshot: %w", err)
	}

	// Fall back: pick the closest sibling snapshot in this session that
	// HAS OCR text. ABS(segment_id - target) keeps the nearest neighbor
	// (snapshot ids are monotonic per process so this is a good proxy
	// for "captured at closest time"). LIMIT 1 ties broken by id DESC
	// (newer first) so an exact-tie picks the most recent. text != ''
	// keeps text-less box-cache rows (SaveOCRBoxesForSnapshot inserts at
	// distance 0) from shadowing a sibling that has real text.
	err = s.reader().QueryRow(`
		SELECT text FROM activity_ocr_frames
		WHERE session_id = ? AND text != ''
		ORDER BY ABS(segment_id - ?) ASC, id DESC
		LIMIT 1`, sessionID, snapshotID).Scan(&text)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("query nearest-sibling ocr: %w", err)
	}
	return text, nil
}

// GetExactOCRTextBySnapshot returns only OCR produced for the requested
// snapshot. Unlike GetOCRTextBySnapshot, it never borrows a sibling frame.
// This is the safe lookup for immutable task-onset context because a nearby
// frame may have been captured after the task began.
func (s *ActivityStore) GetExactOCRTextBySnapshot(
	sessionID string,
	snapshotID int64,
) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("activity store is unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || snapshotID <= 0 {
		return "", errors.New("exact OCR lookup requires a session and snapshot")
	}
	if s.isSessionProtected(sessionID) {
		return "", nil
	}
	var text string
	err := s.reader().QueryRow(`
		SELECT text
		FROM activity_ocr_frames
		WHERE session_id = ? AND segment_id = ? AND TRIM(text) != ''
		ORDER BY id DESC
		LIMIT 1`,
		sessionID,
		snapshotID,
	).Scan(&text)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("query exact ocr by snapshot: %w", err)
	}
	return strings.TrimSpace(text), nil
}

// UpdateOCRStatus updates the OCR processing status of a session.
func (s *ActivityStore) UpdateOCRStatus(sessionID, status string) error {
	_, err := s.db.Exec(`UPDATE activity_sessions SET ocr_status = ? WHERE id = ?`, status, sessionID)
	return err
}

// ResetAllOCR clears all existing OCR data and resets every session to pending
// so the OCR worker will re-process them. This is useful after changing the OCR
// engine (e.g. adding multi-language support).
func (s *ActivityStore) ResetAllOCR() (int64, error) {
	if _, err := s.db.Exec(`DELETE FROM activity_ocr_frames`); err != nil {
		return 0, fmt.Errorf("delete ocr frames: %w", err)
	}
	res, err := s.db.Exec(`UPDATE activity_sessions SET ocr_status = 'pending' WHERE ocr_status = 'done'`)
	if err != nil {
		return 0, fmt.Errorf("reset ocr status: %w", err)
	}
	return res.RowsAffected()
}

// GetSessionsByOCRStatus returns sessions with the given OCR status.
func (s *ActivityStore) GetSessionsByOCRStatus(status string) ([]ActivitySessionRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			ocr_status, COALESCE(automation_status, ''), created_at
		FROM activity_sessions
		WHERE ocr_status = ?
		ORDER BY start_time ASC`, status)
	if err != nil {
		return nil, fmt.Errorf("query sessions by ocr status: %w", err)
	}
	defer rows.Close()

	var sessions []ActivitySessionRow
	for rows.Next() {
		var r ActivitySessionRow
		if err := rows.Scan(&r.ID, &r.StartTime, &r.EndTime, &r.TriggerKind,
			&r.AppNames, &r.EventCount, &r.SegmentCount, &r.SnapshotCount, &r.TotalSizeBytes,
			&r.TriageStatus, &r.OCRStatus, &r.AutomationStatus, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, r)
	}
	return sessions, rows.Err()
}

// GetOCRFramesBySession returns OCR frames for a session, ordered by frame time (max 50).
func (s *ActivityStore) GetOCRFramesBySession(sessionID string) ([]OCRFrameRow, error) {
	rows, err := s.reader().Query(`
		SELECT id, session_id, segment_id, frame_time, text, confidence
		FROM activity_ocr_frames
		WHERE session_id = ?
		ORDER BY frame_time
		LIMIT 50`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query ocr frames by session: %w", err)
	}
	defer rows.Close()

	var frames []OCRFrameRow
	for rows.Next() {
		var f OCRFrameRow
		if err := rows.Scan(&f.ID, &f.SessionID, &f.SegmentID, &f.FrameTime, &f.Text, &f.Confidence); err != nil {
			return nil, fmt.Errorf("scan ocr frame: %w", err)
		}
		frames = append(frames, f)
	}
	return frames, rows.Err()
}

// GetOCRMatchFrames returns OCR frames for a session that match a keyword via FTS5.
// Falls back to LIKE for short keywords (< 3 chars) since the trigram tokenizer requires at least 3.
func (s *ActivityStore) GetOCRMatchFrames(sessionID, keyword string) ([]OCRFrameRow, error) {
	var query string
	var param string
	if utf8.RuneCountInString(keyword) >= 3 {
		query = `
			SELECT o.id, o.session_id, o.segment_id, o.frame_time, o.text, o.confidence
			FROM activity_ocr_frames o
			JOIN activity_ocr_fts f ON f.rowid = o.id
			WHERE o.session_id = ? AND f.text MATCH ?
			ORDER BY o.frame_time`
		param = `"` + keyword + `"`
	} else {
		query = `
			SELECT o.id, o.session_id, o.segment_id, o.frame_time, o.text, o.confidence
			FROM activity_ocr_frames o
			WHERE o.session_id = ? AND o.text LIKE ?
			ORDER BY o.frame_time`
		param = "%" + keyword + "%"
	}
	rows, err := s.reader().Query(query, sessionID, param)
	if err != nil {
		return nil, fmt.Errorf("query ocr match frames: %w", err)
	}
	defer rows.Close()

	var frames []OCRFrameRow
	for rows.Next() {
		var f OCRFrameRow
		if err := rows.Scan(&f.ID, &f.SessionID, &f.SegmentID, &f.FrameTime, &f.Text, &f.Confidence); err != nil {
			return nil, fmt.Errorf("scan ocr frame: %w", err)
		}
		frames = append(frames, f)
	}
	return frames, rows.Err()
}

// UpdateHandoffStatus updates the handoff_status column on an activity session.
// Mirrors UpdateAutomationStatus and serves the same retry-guard purpose.
func (s *ActivityStore) UpdateHandoffStatus(sessionID, status string) error {
	_, err := s.db.Exec(`
		UPDATE activity_sessions
		SET handoff_status = ?
		WHERE id = ?`, status, sessionID)
	return err
}

// UpdateSOPStatus updates the sop_status column on an activity session.
// Mirrors UpdateHandoffStatus: the triager's retry guard against
// re-dispatching SOP authoring for the same group.
func (s *ActivityStore) UpdateSOPStatus(sessionID, status string) error {
	_, err := s.db.Exec(`
		UPDATE activity_sessions
		SET sop_status = ?
		WHERE id = ?`, status, sessionID)
	return err
}
