package main

import (
	"testing"
	"time"
)

// A database from before the shadow agent was removed: the observer and
// release tables with rows, a trace event that references its trace, and the
// observer columns and indexes on activity_pipeline_runs.
func seedRemovedShadowObjects(t *testing.T, store *ActivityStore) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE task_shadow_observer_traces_v1 (id TEXT PRIMARY KEY, created_at INTEGER NOT NULL)`,
		`CREATE TABLE task_shadow_observer_trace_events_v1 (
			id TEXT PRIMARY KEY,
			trace_id TEXT NOT NULL REFERENCES task_shadow_observer_traces_v1(id) ON DELETE RESTRICT)`,
		`CREATE TABLE task_shadow_capability_release_events_v1 (id TEXT PRIMARY KEY)`,
		`CREATE TRIGGER task_shadow_observer_traces_v1_no_delete BEFORE DELETE ON task_shadow_observer_traces_v1
			BEGIN SELECT RAISE(ABORT, 'immutable'); END`,
		`INSERT INTO task_shadow_observer_traces_v1 (id, created_at) VALUES ('trace-1', 1)`,
		`INSERT INTO task_shadow_observer_trace_events_v1 (id, trace_id) VALUES ('event-1', 'trace-1')`,
		`INSERT INTO task_shadow_capability_release_events_v1 (id) VALUES ('release-1')`,
		`ALTER TABLE activity_pipeline_runs ADD COLUMN shadow_observer_expected INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE activity_pipeline_runs ADD COLUMN shadow_observer_admission TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE activity_pipeline_runs ADD COLUMN shadow_observer_dispatch_status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE activity_pipeline_runs ADD COLUMN shadow_observer_dispatch_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE activity_pipeline_runs ADD COLUMN shadow_observer_trace_id TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX idx_activity_pipeline_runs_shadow_observer_trace
			ON activity_pipeline_runs(shadow_observer_trace_id) WHERE shadow_observer_trace_id <> ''`,
		`CREATE INDEX idx_activity_pipeline_runs_shadow_observer_expected
			ON activity_pipeline_runs(shadow_observer_expected, started_at)`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
}

func runDropRemovedShadowTables(t *testing.T, store *ActivityStore) {
	t.Helper()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateDropRemovedShadowTables(tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestDropRemovedShadowTablesKeepsPipelineRunsAndTheLearningLedger(t *testing.T) {
	store := newTestStore(t)
	seedRemovedShadowObjects(t, store)
	id, err := store.SaveActivityPipelineRun(ActivityPipelineRunRow{Kind: "group", StartedAt: time.Now().UnixMilli(), GroupID: "group-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE activity_pipeline_runs SET shadow_observer_trace_id = 'trace-1' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	runDropRemovedShadowTables(t, store)
	runDropRemovedShadowTables(t, store) // a second run finds nothing to do

	for _, table := range removedShadowTables {
		if exists, err := store.hasTable(table); err != nil || exists {
			t.Fatalf("%s still exists (err %v)", table, err)
		}
	}
	for _, column := range removedShadowPipelineColumns {
		if exists, err := store.hasColumn("activity_pipeline_runs", column); err != nil || exists {
			t.Fatalf("activity_pipeline_runs.%s still exists (err %v)", column, err)
		}
	}
	run, err := store.GetActivityPipelineRunByID(id)
	if err != nil || run.GroupID != "group-1" {
		t.Fatalf("pipeline run lost: %+v, %v", run, err)
	}
	for _, table := range []string{
		"shadow_task_learning_runs", "shadow_task_learning_revisions",
		"shadow_task_learning_materializations", "agency_learning_corrections_v1",
	} {
		if exists, err := store.hasTable(table); err != nil || !exists {
			t.Fatalf("%s was dropped (err %v)", table, err)
		}
	}
}

// A new database never creates the observer objects, and pipeline runs save
// and read back without them.
func TestNewDatabaseHasNoShadowObserverObjects(t *testing.T) {
	store := newTestStore(t)
	for _, table := range removedShadowTables {
		if exists, err := store.hasTable(table); err != nil || exists {
			t.Fatalf("%s exists on a new database (err %v)", table, err)
		}
	}
	id, err := store.SaveActivityPipelineRun(ActivityPipelineRunRow{Kind: "group", StartedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateActivityPipelineRunCompletion(id, time.Now().UnixMilli(), "", "dismissed", "", "", "", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetActivityPipelineRunByID(id); err != nil {
		t.Fatal(err)
	}
}
