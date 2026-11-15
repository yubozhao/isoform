package main

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestDB opens an in-memory SQLite database with the same settings as
// production (single connection, WAL, foreign keys).
func newTestDB(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA foreign_keys=ON")
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestStore creates an ActivityStore backed by a temporary file DB with all
// migrations applied.
func newTestStore(t testing.TB) *ActivityStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewActivityStore(dir)
	if err != nil {
		t.Fatalf("NewActivityStore: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
		// Close prevents new borrowers, but a borrowed connection can still
		// finish releasing SQLite's WAL files after Close returns.
		deadline := time.Now().Add(5 * time.Second)
		for _, db := range []*sql.DB{s.readDB, s.db} {
			for db != nil && db.Stats().OpenConnections != 0 {
				if time.Now().After(deadline) {
					t.Errorf("activity store connections still open during fixture cleanup: %+v", db.Stats())
					return
				}
				time.Sleep(time.Millisecond)
			}
		}
	})
	return s
}

// ──────────────────────────────────────────────
// Bootstrap reconcile tests
// ──────────────────────────────────────────────

func TestBootstrapReconcile_FreshDB(t *testing.T) {
	// Fresh database — migration_log should be empty after bootstrap (no false positives),
	// and all migrations should be applied after runPendingMigrations.
	s := newTestStore(t)

	// All migrations should have been applied.
	for _, m := range allActivityMigrations {
		if !s.appliedMigrations[m.Name] {
			t.Errorf("migration %q not applied on fresh DB", m.Name)
		}
	}

	// migration_log should have all entries.
	var count int
	s.db.QueryRow("SELECT count(*) FROM migration_log").Scan(&count)
	if count != len(allActivityMigrations) {
		t.Errorf("migration_log count = %d, want %d", count, len(allActivityMigrations))
	}
}

func TestActivityStoreDeleteSemanticEvent(t *testing.T) {
	s := newTestStore(t)
	ev := SemanticEventRow{
		ID:               "event-delete-me",
		Title:            "Delete me",
		Description:      "Temporary semantic event",
		Category:         "connect",
		Entities:         "[]",
		Apps:             `["Slack"]`,
		StartMs:          1000,
		EndMs:            2000,
		SourceRefs:       "{}",
		ThumbnailRefs:    "[]",
		CategoryDotColor: semanticCategoryDotColor("connect"),
		Status:           "settled",
		CreatedAt:        1000,
		UpdatedAt:        1000,
	}
	if err := s.InsertSemanticEvent(ev); err != nil {
		t.Fatalf("InsertSemanticEvent: %v", err)
	}
	if err := s.DeleteSemanticEvent(ev.ID); err != nil {
		t.Fatalf("DeleteSemanticEvent: %v", err)
	}
	rows, err := s.GetSemanticEventsInRange(0, 3000)
	if err != nil {
		t.Fatalf("GetSemanticEventsInRange: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("deleted event still returned: got %d rows", len(rows))
	}
	if err := s.DeleteSemanticEvent(ev.ID); err == nil {
		t.Fatal("DeleteSemanticEvent missing row: got nil error")
	}
}

func TestBootstrapReconcile_FullyMigrated(t *testing.T) {
	// Simulate a DB that was fully migrated with the old PRAGMA system.
	// Bootstrap should correctly identify all migrations via schema probes.
	db := newTestDB(t)
	s := &ActivityStore{db: db, appliedMigrations: make(map[string]bool)}

	// Manually run all migrations to create the schema (without migration_log).
	// drop_removed_agency_tables is left out: a database from the PRAGMA
	// system predates it, and once it has run, bootstrap cannot recognize the
	// Agency migrations whose tables it dropped (see
	// TestDropRemovedAgencyTablesReplaysWhenTheLogIsLost).
	for _, m := range allActivityMigrations {
		if m.Name == "drop_removed_agency_tables" {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Fn(tx); err != nil {
			tx.Rollback()
			t.Fatalf("migration %q: %v", m.Name, err)
		}
		tx.Commit()
	}

	// Now create migration_log and run bootstrap.
	if err := s.ensureMigrationLog(); err != nil {
		t.Fatal(err)
	}
	if err := s.bootstrapMigrationLog(); err != nil {
		t.Fatal(err)
	}

	// Bootstrap should record every schema-derivable migration.
	// activity_summaries_moments_partial_reset and
	// timeline_summary_image_failure_requeue are intentionally excluded:
	// they are pure data backfills/repairs (UPDATE-only, no schema change),
	// so the bootstrap rule deliberately returns false and lets
	// runPendingMigrations apply them idempotently on next launch.
	// drop_removed_agency_tables and drop_removed_shadow_tables leave nothing
	// to probe, so their rules return false the same way. The identifier policy cleanup also has no schema
	// marker: losing its log must not leave legacy prose tokens untouched.
	// noticed_plain_invoice_followup rewrites saved wording and dismissal keys
	// only, so it has no schema marker either; runPendingMigrations applies it.
	dataOnlyMigrations := []string{
		"activity_summaries_moments_partial_reset",
		"timeline_summary_image_failure_requeue",
		"drop_removed_agency_tables",
		"drop_removed_shadow_tables",
		"activity_evidence_identifier_policy",
		"activity_evidence_receipt_policy",
		"noticed_plain_invoice_followup",
	}
	wantBootstrapped := len(allActivityMigrations) - len(dataOnlyMigrations)
	var count int
	db.QueryRow("SELECT count(*) FROM migration_log").Scan(&count)
	if count != wantBootstrapped {
		t.Errorf("bootstrap recorded %d migrations, want %d (all schema-derivable migrations)", count, wantBootstrapped)
	}

	// And the data-only migrations must not be marked applied yet.
	for _, name := range dataOnlyMigrations {
		var resetCount int
		db.QueryRow(
			"SELECT count(*) FROM migration_log WHERE name = ?", name,
		).Scan(&resetCount)
		if resetCount != 0 {
			t.Errorf("%s should not be bootstrapped (data-only)", name)
		}
	}
}

func TestBootstrapReconcile_CalendarHoldsPartialSchema(t *testing.T) {
	for _, tc := range []struct {
		name, schema, missing string
	}{
		{"missing active index", `CREATE TABLE holds (subject_key TEXT, source_active INTEGER, state TEXT, failure_reason TEXT)`, "calendar_hold_active_source"},
		{"missing recovery column", `CREATE TABLE holds (subject_key TEXT, source_active INTEGER, state TEXT); CREATE INDEX holds_active_source ON holds(subject_key)`, "calendar_hold_recovery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			if _, err := db.Exec(tc.schema); err != nil {
				t.Fatal(err)
			}
			s := &ActivityStore{db: db, appliedMigrations: make(map[string]bool)}
			if err := s.ensureMigrationLog(); err != nil {
				t.Fatal(err)
			}
			if err := s.bootstrapMigrationLog(); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM migration_log WHERE name = ?", tc.missing).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial migration %s recorded %d times: %v", tc.missing, count, err)
			}
		})
	}
}

func TestBootstrapReconcile_BrokenV8(t *testing.T) {
	// Simulate the incident: user_version=8 but prediction_proposals missing.
	// Bootstrap should NOT mark prediction_tables as applied.
	// After runPendingMigrations, prediction tables should exist.
	db := newTestDB(t)
	s := &ActivityStore{db: db, appliedMigrations: make(map[string]bool)}

	// Run all migrations EXCEPT prediction_tables and migrations that add
	// columns to prediction_proposals. Those dependent migrations cannot be
	// applied until runPendingMigrations recreates the missing table.
	for _, m := range allActivityMigrations {
		if m.Name == "prediction_tables" || m.Name == "prediction_cron_columns" || m.Name == "handoff_prompt_columns" || m.Name == "rename_crystal_to_triage" {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Fn(tx); err != nil {
			tx.Rollback()
			t.Fatalf("migration %q: %v", m.Name, err)
		}
		tx.Commit()
	}

	// Run the full migrate() flow.
	if err := s.ensureMigrationLog(); err != nil {
		t.Fatal(err)
	}
	if err := s.bootstrapMigrationLog(); err != nil {
		t.Fatal(err)
	}

	// prediction_tables should NOT be in migration_log (table doesn't exist).
	var predCount int
	db.QueryRow("SELECT count(*) FROM migration_log WHERE name = 'prediction_tables'").Scan(&predCount)
	if predCount != 0 {
		t.Error("prediction_tables should not be in migration_log (table missing)")
	}

	// prediction_cron_columns should also NOT be there (depends on prediction_tables).
	db.QueryRow("SELECT count(*) FROM migration_log WHERE name = 'prediction_cron_columns'").Scan(&predCount)
	if predCount != 0 {
		t.Error("prediction_cron_columns should not be in migration_log")
	}

	// Run pending migrations — this should create the missing tables.
	s.runPendingMigrations()

	// Now prediction_proposals should exist.
	exists, err := s.hasTable("prediction_proposals")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("prediction_proposals still missing after runPendingMigrations")
	}

	// kind column should exist.
	hasKind, err := s.hasColumn("prediction_proposals", "kind")
	if err != nil {
		t.Fatal(err)
	}
	if !hasKind {
		t.Error("prediction_proposals.kind column missing after runPendingMigrations")
	}

	// appliedMigrations should be complete.
	if !s.appliedMigrations["prediction_tables"] {
		t.Error("prediction_tables not in appliedMigrations")
	}
	if !s.appliedMigrations["prediction_cron_columns"] {
		t.Error("prediction_cron_columns not in appliedMigrations")
	}
}

func TestBootstrapReconcile_PartialMigration(t *testing.T) {
	// Simulate a partially applied ocr_frames_fts migration: table and FTS
	// exist but triggers are missing (old non-transactional system crashed
	// mid-migration). Bootstrap should NOT record ocr_frames_fts.
	db := newTestDB(t)
	s := &ActivityStore{db: db, appliedMigrations: make(map[string]bool)}

	// Run core_tables + start_time_index fully.
	for _, name := range []string{"core_tables", "start_time_index"} {
		for _, m := range allActivityMigrations {
			if m.Name == name {
				tx, _ := db.Begin()
				if err := m.Fn(tx); err != nil {
					tx.Rollback()
					t.Fatalf("migration %q: %v", name, err)
				}
				tx.Commit()
			}
		}
	}

	// Manually create partial ocr_frames_fts state: table + FTS + column + index, but NO triggers.
	db.Exec(`CREATE TABLE IF NOT EXISTS activity_ocr_frames (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		segment_id INTEGER NOT NULL,
		frame_time REAL NOT NULL,
		text TEXT NOT NULL,
		confidence REAL NOT NULL DEFAULT 0.0
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_ocr_frames_session ON activity_ocr_frames(session_id)`)
	db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS activity_ocr_fts USING fts5(
		text, content='activity_ocr_frames', content_rowid='id', tokenize='trigram'
	)`)
	db.Exec(`ALTER TABLE activity_sessions ADD COLUMN ocr_status TEXT NOT NULL DEFAULT 'pending'`)
	// Intentionally omit triggers.

	// Run bootstrap.
	if err := s.ensureMigrationLog(); err != nil {
		t.Fatal(err)
	}
	if err := s.bootstrapMigrationLog(); err != nil {
		t.Fatal(err)
	}

	// ocr_frames_fts should NOT be recorded (triggers missing).
	var count int
	db.QueryRow("SELECT count(*) FROM migration_log WHERE name = 'ocr_frames_fts'").Scan(&count)
	if count != 0 {
		t.Error("ocr_frames_fts should not be in migration_log when triggers are missing")
	}

	// core_tables and start_time_index should be recorded.
	db.QueryRow("SELECT count(*) FROM migration_log WHERE name = 'core_tables'").Scan(&count)
	if count != 1 {
		t.Error("core_tables should be in migration_log")
	}

	// After runPendingMigrations, the triggers should be created.
	s.runPendingMigrations()

	has, err := s.hasTrigger("ocr_fts_insert")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("ocr_fts_insert trigger should exist after runPendingMigrations")
	}

	if !s.appliedMigrations["ocr_frames_fts"] {
		t.Error("ocr_frames_fts should be in appliedMigrations after runPendingMigrations")
	}
}

func TestBootstrapReconcile_MissingIndex(t *testing.T) {
	// Simulate prediction_tables partially applied: tables exist but index missing.
	// Bootstrap should NOT record prediction_tables.
	db := newTestDB(t)
	s := &ActivityStore{db: db, appliedMigrations: make(map[string]bool)}

	// Run all migrations except prediction_tables and migrations that depend
	// on prediction_proposals. The fixture creates the partial table below.
	for _, m := range allActivityMigrations {
		if m.Name == "prediction_tables" || m.Name == "prediction_cron_columns" || m.Name == "embedding_columns" || m.Name == "handoff_prompt_columns" || m.Name == "rename_crystal_to_triage" {
			continue
		}
		tx, _ := db.Begin()
		if err := m.Fn(tx); err != nil {
			tx.Rollback()
			t.Fatalf("migration %q: %v", m.Name, err)
		}
		tx.Commit()
	}

	// Manually create prediction tables WITHOUT the status index.
	db.Exec(`CREATE TABLE IF NOT EXISTS prediction_proposals (
		id TEXT PRIMARY KEY, source_kind TEXT, source_id TEXT, status TEXT,
		user_intent TEXT, predicted_next TEXT, tool_proposal TEXT, app_name TEXT,
		description TEXT, features TEXT, confidence REAL,
		chat_session_id TEXT, dismiss_reason TEXT, created_at INTEGER, resolved_at INTEGER
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS prediction_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT, proposal_id TEXT, outcome TEXT,
		source_kind TEXT, time_to_decision INTEGER, created_at INTEGER
	)`)
	// Intentionally omit idx_prediction_proposals_status.

	if err := s.ensureMigrationLog(); err != nil {
		t.Fatal(err)
	}
	if err := s.bootstrapMigrationLog(); err != nil {
		t.Fatal(err)
	}

	// prediction_tables should NOT be recorded (index missing).
	var count int
	db.QueryRow("SELECT count(*) FROM migration_log WHERE name = 'prediction_tables'").Scan(&count)
	if count != 0 {
		t.Error("prediction_tables should not be in migration_log when index is missing")
	}

	// After runPendingMigrations, the index should be created (idempotent DDL).
	s.runPendingMigrations()

	has, err := s.hasIndex("idx_prediction_proposals_status")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("idx_prediction_proposals_status should exist after runPendingMigrations")
	}

	if !s.appliedMigrations["prediction_tables"] {
		t.Error("prediction_tables should be in appliedMigrations after runPendingMigrations")
	}
}

// ──────────────────────────────────────────────
// Transaction and fail-and-continue tests
// ──────────────────────────────────────────────

func TestMigrationTransactional(t *testing.T) {
	// A migration that fails mid-execution should not leave partial state.
	db := newTestDB(t)
	s := &ActivityStore{db: db, appliedMigrations: make(map[string]bool)}

	if err := s.ensureMigrationLog(); err != nil {
		t.Fatal(err)
	}

	// Create a single-migration registry with a function that fails after DDL.
	failMigration := activityMigration{
		Name: "test_fail",
		Fn: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`CREATE TABLE test_table (id INTEGER PRIMARY KEY)`); err != nil {
				return err
			}
			return fmt.Errorf("intentional failure after DDL")
		},
	}

	// Manually run the migration through the same path as runPendingMigrations.
	tx, _ := db.Begin()
	err := failMigration.Fn(tx)
	if err != nil {
		tx.Rollback()
	}

	// test_table should NOT exist (transaction rolled back).
	exists, _ := s.hasTable("test_table")
	if exists {
		t.Error("test_table should not exist after rollback")
	}

	// migration_log should NOT contain "test_fail".
	var count int
	db.QueryRow("SELECT count(*) FROM migration_log WHERE name = 'test_fail'").Scan(&count)
	if count != 0 {
		t.Error("test_fail should not be in migration_log after rollback")
	}
}

func TestFailAndContinue(t *testing.T) {
	// If one migration fails, subsequent migrations should still run.
	db := newTestDB(t)
	s := &ActivityStore{db: db, appliedMigrations: make(map[string]bool)}

	if err := s.ensureMigrationLog(); err != nil {
		t.Fatal(err)
	}

	// Temporarily replace allActivityMigrations with a custom set.
	origMigrations := allActivityMigrations
	defer func() { allActivityMigrations = origMigrations }()

	allActivityMigrations = []activityMigration{
		{Name: "step_1", Fn: func(tx *sql.Tx) error {
			_, err := tx.Exec(`CREATE TABLE step1 (id INTEGER PRIMARY KEY)`)
			return err
		}},
		{Name: "step_2_fail", Fn: func(tx *sql.Tx) error {
			return fmt.Errorf("intentional failure")
		}},
		{Name: "step_3", Fn: func(tx *sql.Tx) error {
			_, err := tx.Exec(`CREATE TABLE step3 (id INTEGER PRIMARY KEY)`)
			return err
		}},
	}

	s.runPendingMigrations()

	// step_1 should succeed.
	if !s.appliedMigrations["step_1"] {
		t.Error("step_1 should be applied")
	}
	exists1, _ := s.hasTable("step1")
	if !exists1 {
		t.Error("step1 table should exist")
	}

	// step_2_fail should NOT be applied.
	if s.appliedMigrations["step_2_fail"] {
		t.Error("step_2_fail should not be applied")
	}

	// step_3 should still succeed (fail-and-continue).
	if !s.appliedMigrations["step_3"] {
		t.Error("step_3 should be applied despite step_2 failure")
	}
	exists3, _ := s.hasTable("step3")
	if !exists3 {
		t.Error("step3 table should exist")
	}
}

// ──────────────────────────────────────────────
// Idempotent rerun test
// ──────────────────────────────────────────────

func TestIdempotentRerun(t *testing.T) {
	// Running all migrations twice should be a no-op the second time.
	s := newTestStore(t)

	// Count migrations after first run.
	var count1 int
	s.db.QueryRow("SELECT count(*) FROM migration_log").Scan(&count1)

	// Run again.
	s.runPendingMigrations()

	// Count should be the same.
	var count2 int
	s.db.QueryRow("SELECT count(*) FROM migration_log").Scan(&count2)
	if count1 != count2 {
		t.Errorf("migration_log count changed: %d -> %d", count1, count2)
	}
}

// ──────────────────────────────────────────────
// Schema inspection helper tests
// ──────────────────────────────────────────────

func TestHasTable(t *testing.T) {
	s := newTestStore(t)

	exists, err := s.hasTable("activity_sessions")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("activity_sessions should exist")
	}

	exists, err = s.hasTable("nonexistent_table")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("nonexistent_table should not exist")
	}
}

func TestHasColumn(t *testing.T) {
	s := newTestStore(t)

	has, err := s.hasColumn("activity_sessions", "start_time")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("activity_sessions.start_time should exist")
	}

	has, err = s.hasColumn("activity_sessions", "nonexistent_col")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("nonexistent_col should not exist")
	}

	// Non-existent table should return false, no error.
	has, err = s.hasColumn("no_such_table", "col")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("column on non-existent table should return false")
	}
}

func TestHasIndex(t *testing.T) {
	s := newTestStore(t)

	has, err := s.hasIndex("idx_activity_sessions_start_time")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("idx_activity_sessions_start_time should exist")
	}

	has, err = s.hasIndex("nonexistent_idx")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("nonexistent_idx should not exist")
	}
}

func TestTableSchemaContains(t *testing.T) {
	s := newTestStore(t)

	// After ocr_constraint_fix, activity_ocr_frames should NOT contain
	// "REFERENCES activity_segments".
	contains, err := s.tableSchemaContains("activity_ocr_frames", "REFERENCES activity_segments")
	if err != nil {
		t.Fatal(err)
	}
	if contains {
		t.Error("activity_ocr_frames should not reference activity_segments after constraint fix")
	}

	// Non-existent table should return false.
	contains, err = s.tableSchemaContains("no_such_table", "anything")
	if err != nil {
		t.Fatal(err)
	}
	if contains {
		t.Error("non-existent table should return false")
	}
}

func TestMigrationLogRecordsExist(t *testing.T) {
	// After full store init, migration_log should have all migrations
	// and prediction_test.go's old PRAGMA assertion is replaced by this.
	s := newTestStore(t)

	var count int
	s.db.QueryRow(`SELECT count(*) FROM migration_log WHERE name IN ('prediction_tables', 'prediction_cron_columns')`).Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 prediction migration records, got %d", count)
	}
}

// TestGetOCRTextBySnapshot_ExactMatch confirms the happy path: when the
// requested snapshot has its own OCR row, that row's text is returned.
func TestGetOCRTextBySnapshot_ExactMatch(t *testing.T) {
	s := newTestStore(t)
	const sid = "sess-exact"

	session := ActivitySessionRow{
		ID: sid, StartTime: 1000, EndTime: 2000,
		TriggerKind: "idle_transition", AppNames: `["TIM"]`, EventCount: 1,
	}
	if err := s.SaveSession(session, nil, nil); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := s.SaveOCRFrame(OCRFrameRow{
		SessionID: sid, SegmentID: 100, FrameTime: 1.0,
		Text: "exact-match text", Confidence: 0.9,
	}); err != nil {
		t.Fatalf("SaveOCRFrame: %v", err)
	}

	got, err := s.GetOCRTextBySnapshot(sid, 100)
	if err != nil {
		t.Fatalf("GetOCRTextBySnapshot: %v", err)
	}
	if got != "exact-match text" {
		t.Errorf("expected exact match, got %q", got)
	}
}

// TestGetOCRTextBySnapshot_NearestSiblingFallback covers the user-reported
// TIM case: every other snapshot has no OCR (worker samples ~50% of frames
// per session), so clicking an un-sampled frame must surface the nearest
// sibling's text rather than returning empty.
func TestGetOCRTextBySnapshot_NearestSiblingFallback(t *testing.T) {
	s := newTestStore(t)
	const sid = "sess-fallback"

	session := ActivitySessionRow{
		ID: sid, StartTime: 1000, EndTime: 5000,
		TriggerKind: "idle_transition", AppNames: `["TIM"]`, EventCount: 1,
	}
	if err := s.SaveSession(session, nil, nil); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	// OCR worker sampled only the odd-numbered snapshots.
	for _, sample := range []struct {
		snapID int64
		text   string
	}{
		{100, "early frame text"},
		{300, "later frame text"},
	} {
		if err := s.SaveOCRFrame(OCRFrameRow{
			SessionID: sid, SegmentID: sample.snapID, FrameTime: 1.0,
			Text: sample.text, Confidence: 0.9,
		}); err != nil {
			t.Fatalf("SaveOCRFrame: %v", err)
		}
	}

	cases := []struct {
		name   string
		snapID int64
		want   string
	}{
		// 150 is closer to 100 than to 300.
		{"closer-to-early", 150, "early frame text"},
		// 250 is closer to 300 than to 100.
		{"closer-to-late", 250, "later frame text"},
		// Exact tie (200 = midpoint). ORDER BY ABS(...) ASC, id DESC
		// breaks the tie toward the newer row.
		{"exact-tie", 200, "later frame text"},
		// Way before any sampled frame — still falls back to the nearest.
		{"far-before", 10, "early frame text"},
		// Way after — falls back to the nearest after.
		{"far-after", 9999, "later frame text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.GetOCRTextBySnapshot(sid, tc.snapID)
			if err != nil {
				t.Fatalf("GetOCRTextBySnapshot: %v", err)
			}
			if got != tc.want {
				t.Errorf("snap %d: got %q want %q", tc.snapID, got, tc.want)
			}
		})
	}
}

// TestGetOCRTextBySnapshot_NoFallbackAcrossSessions ensures the sibling
// fallback never crosses a session boundary — chat in session A must not
// leak into a popup viewing session B.
func TestGetOCRTextBySnapshot_NoFallbackAcrossSessions(t *testing.T) {
	s := newTestStore(t)

	for _, sid := range []string{"sess-A", "sess-B"} {
		if err := s.SaveSession(ActivitySessionRow{
			ID: sid, StartTime: 1000, EndTime: 2000,
			TriggerKind: "idle_transition", AppNames: `["TIM"]`, EventCount: 1,
		}, nil, nil); err != nil {
			t.Fatalf("SaveSession %s: %v", sid, err)
		}
	}
	// Only session A has any OCR data.
	if err := s.SaveOCRFrame(OCRFrameRow{
		SessionID: "sess-A", SegmentID: 100, FrameTime: 1.0,
		Text: "session-A content", Confidence: 0.9,
	}); err != nil {
		t.Fatalf("SaveOCRFrame: %v", err)
	}

	// Asking for a session-B snapshot must return empty, NOT session A's
	// text, even though A has a "near" segment id.
	got, err := s.GetOCRTextBySnapshot("sess-B", 100)
	if err != nil {
		t.Fatalf("GetOCRTextBySnapshot: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty (no cross-session leak), got %q", got)
	}
}

// TestGetOCRTextBySnapshot_EmptyWhenSessionHasNothing covers the (rare)
// case where the OCR worker hasn't run at all yet for a session: every
// snapshot should report empty, no error.
func TestGetOCRTextBySnapshot_EmptyWhenSessionHasNothing(t *testing.T) {
	s := newTestStore(t)
	const sid = "sess-empty"
	if err := s.SaveSession(ActivitySessionRow{
		ID: sid, StartTime: 1000, EndTime: 2000,
		TriggerKind: "idle_transition", AppNames: `["TIM"]`, EventCount: 1,
	}, nil, nil); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := s.GetOCRTextBySnapshot(sid, 42)
	if err != nil {
		t.Fatalf("GetOCRTextBySnapshot: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for session with no OCR, got %q", got)
	}
}

func TestGetExactOCRTextBySnapshotNeverFallsBack(t *testing.T) {
	s := newTestStore(t)
	const sid = "sess-exact-only"
	if err := s.SaveSession(ActivitySessionRow{
		ID: sid, StartTime: 1000, EndTime: 3000,
		TriggerKind: "idle_transition", AppNames: `["Mail"]`, EventCount: 1,
	}, nil, nil); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := s.SaveOCRFrame(OCRFrameRow{
		SessionID: sid, SegmentID: 100, FrameTime: 1,
		Text: "source from the earlier frame", Confidence: 0.9,
	}); err != nil {
		t.Fatalf("SaveOCRFrame: %v", err)
	}

	got, err := s.GetExactOCRTextBySnapshot(sid, 101)
	if err != nil {
		t.Fatalf("GetExactOCRTextBySnapshot: %v", err)
	}
	if got != "" {
		t.Fatalf("exact lookup borrowed sibling OCR: %q", got)
	}

	got, err = s.GetExactOCRTextBySnapshot(sid, 100)
	if err != nil {
		t.Fatalf("GetExactOCRTextBySnapshot exact: %v", err)
	}
	if got != "source from the earlier frame" {
		t.Fatalf("exact lookup = %q", got)
	}
}
