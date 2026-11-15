package main

import (
	"database/sql"
	"fmt"
)

// The learning ledger the shadow agent wrote. The agent is gone, but the
// tables stay: forgetting a memory still deletes the learning derived from
// it, and memory recall still hides the entries the user corrected
// (learning_corrections.go).

func migrateShadowTaskLearning(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS shadow_task_learning_runs (
			id              TEXT PRIMARY KEY,
			input_hash      TEXT NOT NULL,
			policy_version  TEXT NOT NULL,
			authority_effect TEXT NOT NULL CHECK (authority_effect = 'none'),
			projection_json TEXT NOT NULL,
			created_at      INTEGER NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_shadow_task_learning_runs_input
			ON shadow_task_learning_runs(input_hash, policy_version);
		CREATE INDEX IF NOT EXISTS idx_shadow_task_learning_runs_created
			ON shadow_task_learning_runs(created_at DESC);

		CREATE TABLE IF NOT EXISTS shadow_task_learning_revisions (
			revision_id      TEXT PRIMARY KEY,
			run_id           TEXT NOT NULL REFERENCES shadow_task_learning_runs(id),
			projection_key   TEXT NOT NULL,
			policy_version   TEXT NOT NULL,
			target_kind      TEXT NOT NULL,
			learning_kind    TEXT NOT NULL,
			capability_key   TEXT NOT NULL,
			decision         TEXT NOT NULL,
			evidence_hash    TEXT NOT NULL,
			authority_effect TEXT NOT NULL CHECK (authority_effect = 'none'),
			candidate_json   TEXT NOT NULL,
			created_at       INTEGER NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_shadow_task_learning_revision_key
			ON shadow_task_learning_revisions(projection_key, evidence_hash);
		CREATE INDEX IF NOT EXISTS idx_shadow_task_learning_target
			ON shadow_task_learning_revisions(target_kind, decision, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_shadow_task_learning_capability
			ON shadow_task_learning_revisions(capability_key, created_at DESC);

		CREATE TABLE IF NOT EXISTS shadow_task_learning_materializations (
			id              TEXT PRIMARY KEY,
			revision_id     TEXT NOT NULL REFERENCES shadow_task_learning_revisions(revision_id),
			target_kind     TEXT NOT NULL,
			target_id       TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL,
			error           TEXT NOT NULL DEFAULT '',
			created_at      INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_task_learning_materialization
			ON shadow_task_learning_materializations(revision_id, target_kind);
		CREATE INDEX IF NOT EXISTS idx_shadow_task_learning_materialization_status
			ON shadow_task_learning_materializations(status, created_at DESC);

		CREATE TRIGGER IF NOT EXISTS shadow_task_learning_runs_no_update
		BEFORE UPDATE ON shadow_task_learning_runs
		BEGIN SELECT RAISE(ABORT, 'shadow task learning runs are immutable'); END;
		CREATE TRIGGER IF NOT EXISTS shadow_task_learning_runs_no_delete
		BEFORE DELETE ON shadow_task_learning_runs
		BEGIN SELECT RAISE(ABORT, 'shadow task learning runs are immutable'); END;
		CREATE TRIGGER IF NOT EXISTS shadow_task_learning_revisions_no_update
		BEFORE UPDATE ON shadow_task_learning_revisions
		BEGIN SELECT RAISE(ABORT, 'shadow task learning revisions are immutable'); END;
		CREATE TRIGGER IF NOT EXISTS shadow_task_learning_revisions_no_delete
		BEFORE DELETE ON shadow_task_learning_revisions
		BEGIN SELECT RAISE(ABORT, 'shadow task learning revisions are immutable'); END;
		CREATE TRIGGER IF NOT EXISTS shadow_task_learning_materializations_no_update
		BEFORE UPDATE ON shadow_task_learning_materializations
		BEGIN SELECT RAISE(ABORT, 'shadow task learning materializations are immutable'); END;
		CREATE TRIGGER IF NOT EXISTS shadow_task_learning_materializations_no_delete
		BEFORE DELETE ON shadow_task_learning_materializations
		BEGIN SELECT RAISE(ABORT, 'shadow task learning materializations are immutable'); END;
	`)
	if err != nil {
		return fmt.Errorf("create shadow task learning ledger: %w", err)
	}
	return nil
}
