package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// The removed Agency's planning runs (agency_origination_runs_v1) were kept
// when its other tables were dropped, because leftover one-time work in
// agency_origination_permission_requests_v1 still names them by id. Nothing
// reads a run's prompt, inputs or outputs again (the plan card reads the
// permission request alone), yet they take about 2.4 MB a run: 3.1 GB, the
// largest table, on one profile. They are cleared in small batches in the
// background; the rows, ids, statuses and times stay, so every reference
// still resolves. The Agency kept this ledger immutable with a no-update
// trigger; nothing writes it any more, so the trigger is dropped first. The
// freed pages go back to the disk with the next VACUUM.

// agencyRunsSlimmedMigration is recorded in migration_log once every run is
// cleared, so later passes skip the table.
const agencyRunsSlimmedMigration = "slim_agency_origination_runs"

// agencyRunsSlimBatch is how many runs one transaction clears (about 24 MB).
const agencyRunsSlimBatch = 10

// agencyRunsToSlim returns the ids of the planning runs to clear, nil when
// the table is gone or already cleared.
func (s *ActivityStore) agencyRunsToSlim() ([]string, error) {
	var done int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM migration_log WHERE name = ?`, agencyRunsSlimmedMigration).Scan(&done); err != nil {
		return nil, fmt.Errorf("read migration log: %w", err)
	}
	if done > 0 || !s.tableExists("agency_origination_runs_v1") {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id FROM agency_origination_runs_v1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list agency runs: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan agency run: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// tableExists reports whether the database has the table.
func (s *ActivityStore) tableExists(name string) bool {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return err == nil && n > 0
}

// agencyRunBytes is what the planning runs' pages take (0 when unknown or
// already cleared). It reads every page of the table, so only the cleanup
// report asks.
func (s *ActivityStore) agencyRunBytes() int64 {
	ids, err := s.agencyRunsToSlim()
	if err != nil || len(ids) == 0 {
		return 0
	}
	var bytes sql.NullInt64
	if err := s.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE name = 'agency_origination_runs_v1'`).Scan(&bytes); err != nil {
		return 0
	}
	return bytes.Int64
}

// slimAgencyRuns clears the prompts, inputs and outputs of the given runs.
func (s *ActivityStore) slimAgencyRuns(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS agency_origination_runs_v1_no_update`); err != nil {
		return fmt.Errorf("drop agency run trigger: %w", err)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := tx.Exec(`
		UPDATE agency_origination_runs_v1
		SET source_input_json = '{}', source_manifest_json = '{}', planning_input_json = '{}',
			prompt = '', raw_output = '', output_json = '{}', payload_json = '{}',
			rejected_evidence_refs_json = '[]', rejected_candidate_keys_json = '[]',
			resource_policy_json = '{}', resource_usage_json = '{}'
		WHERE id IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("clear agency runs: %w", err)
	}
	return tx.Commit()
}

// markAgencyRunsSlimmed records that every run is cleared.
func (s *ActivityStore) markAgencyRunsSlimmed() error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO migration_log (name, applied_at) VALUES (?, ?)`,
		agencyRunsSlimmedMigration, time.Now().UnixMilli())
	return err
}

// slimRetiredAgencyRuns clears planning runs in batches until all are done,
// deadline passes or stop (when set) says so. Returns how many it cleared.
func (r *StorageRotator) slimRetiredAgencyRuns(deadline time.Time, pause time.Duration, stop func() bool) int {
	ids, err := r.store.agencyRunsToSlim()
	if err != nil {
		log.Printf("[activity-rotator] list agency runs to clear: %v", err)
		return 0
	}
	cleared := 0
	for start := 0; start < len(ids); start += agencyRunsSlimBatch {
		if !time.Now().Before(deadline) || (stop != nil && stop()) {
			return cleared
		}
		batch := ids[start:min(start+agencyRunsSlimBatch, len(ids))]
		if err := r.store.slimAgencyRuns(batch); err != nil {
			log.Printf("[activity-rotator] clear agency runs: %v", err)
			return cleared
		}
		cleared += len(batch)
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	if len(ids) > 0 {
		if err := r.store.markAgencyRunsSlimmed(); err != nil {
			log.Printf("[activity-rotator] record cleared agency runs: %v", err)
		}
		log.Printf("[activity-rotator] cleared %d retired Agency planning run(s); the next VACUUM gives the space back", cleared)
		r.RequestVacuum()
	}
	return cleared
}

// tidyRetiredDatabase drops the tables of removed features that are still in
// the database (the drop migration failed or did not run).
func (r *StorageRotator) tidyRetiredDatabase() {
	tables, _, err := r.store.retiredTablesPresent()
	if err != nil || len(tables) == 0 {
		return
	}
	if err := r.store.dropRetiredTables(); err != nil {
		log.Printf("[activity-rotator] drop retired tables: %v", err)
		return
	}
	log.Printf("[activity-rotator] dropped %d retired table(s)", len(tables))
	r.RequestVacuum()
}
