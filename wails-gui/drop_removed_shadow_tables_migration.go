package main

import (
	"database/sql"
	"fmt"
)

// The shadow agent's task observer and release gate were removed on
// 2026-11-14 (docs/rework-2026-11.md). Their migrations are now no-ops, so a
// new database never creates these objects; this drops them from databases
// that already have them. Children come before their parents: with foreign
// keys on, DROP TABLE first deletes all rows, and that fails while a child row
// still references them. Dropping a table also drops its indexes and triggers.
//
// The learning ledger (shadow_task_learning_*) stays: forgetting a memory still
// deletes the learning derived from it and memory recall still hides the
// entries the user corrected. The other shadow tables stay while the typed Self
// model, the authority gateway and task agent attempts still read them.
var removedShadowTables = []string{
	"task_shadow_observer_trace_events_v1",
	"task_shadow_observer_traces_v1",
	"task_shadow_capability_release_events_v1",
}

// removedShadowPipelineIndexes cover the observer columns below; SQLite will
// not drop an indexed column.
var removedShadowPipelineIndexes = []string{
	"idx_activity_pipeline_runs_shadow_observer_trace",
	"idx_activity_pipeline_runs_shadow_observer_expected",
}

// removedShadowPipelineColumns recorded, on every triage run, whether the
// shadow observer was dispatched.
var removedShadowPipelineColumns = []string{
	"shadow_observer_expected",
	"shadow_observer_admission",
	"shadow_observer_dispatch_status",
	"shadow_observer_dispatch_reason",
	"shadow_observer_trace_id",
}

// migrateRemovedShadowNoop stands in for the removed shadow observer and
// release migrations so their names stay in the history.
func migrateRemovedShadowNoop(*sql.Tx) error { return nil }

func migrateDropRemovedShadowTables(tx *sql.Tx) error {
	if tx == nil {
		return fmt.Errorf("migrate drop removed shadow tables: transaction is nil")
	}
	for _, index := range removedShadowPipelineIndexes {
		if _, err := tx.Exec("DROP INDEX IF EXISTS " + index); err != nil {
			return fmt.Errorf("drop removed shadow index %s: %w", index, err)
		}
	}
	for _, table := range removedShadowTables {
		if _, err := tx.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			return fmt.Errorf("drop removed shadow table %s: %w", table, err)
		}
	}
	for _, column := range removedShadowPipelineColumns {
		var found int
		if err := tx.QueryRow(
			`SELECT count(*) FROM pragma_table_info('activity_pipeline_runs') WHERE name = ?`,
			column,
		).Scan(&found); err != nil {
			return fmt.Errorf("inspect activity_pipeline_runs.%s: %w", column, err)
		}
		if found == 0 {
			continue
		}
		if _, err := tx.Exec("ALTER TABLE activity_pipeline_runs DROP COLUMN " + column); err != nil {
			return fmt.Errorf("drop activity_pipeline_runs.%s: %w", column, err)
		}
	}
	return nil
}
