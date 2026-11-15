package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The shadow agent is gone, but databases keep the learning it wrote and the
// corrections the user made to it. These helpers write those rows directly,
// the way the removed code left them, so privacy tests can keep checking
// that forget, recall and replay still honor them.

type legacyLearningCandidate struct {
	RevisionID    string
	ProjectionKey string
	TargetKind    string
	Statement     string
}

// seedLegacyLearningRun stores one learning run and its revisions.
func seedLegacyLearningRun(t *testing.T, store *ActivityStore, runID string, candidates []legacyLearningCandidate, at int64) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO shadow_task_learning_runs
		(id, input_hash, policy_version, authority_effect, projection_json, created_at)
		VALUES (?, ?, 'legacy', 'none', '{}', ?)`, runID, "input:"+runID, at); err != nil {
		t.Fatalf("seed learning run: %v", err)
	}
	for _, candidate := range candidates {
		raw, err := json.Marshal(map[string]string{
			"revisionId": candidate.RevisionID, "projectionKey": candidate.ProjectionKey,
			"targetKind": candidate.TargetKind, "statement": candidate.Statement,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO shadow_task_learning_revisions
			(revision_id, run_id, projection_key, policy_version, target_kind, learning_kind,
			 capability_key, decision, evidence_hash, authority_effect, candidate_json, created_at)
			VALUES (?, ?, ?, 'legacy', ?, 'pattern', 'coding.review', 'qualified', ?, 'none', ?, ?)`,
			candidate.RevisionID, runID, candidate.ProjectionKey, candidate.TargetKind,
			"evidence:"+candidate.RevisionID, string(raw), at); err != nil {
			t.Fatalf("seed learning revision: %v", err)
		}
	}
}

// seedLegacyLearningMaterialization records that a revision wrote targetID.
func seedLegacyLearningMaterialization(t *testing.T, store *ActivityStore, revisionID, targetKind, targetID string, at int64) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := store.db.Exec(`INSERT INTO shadow_task_learning_materializations
		(id, revision_id, target_kind, target_id, status, created_at)
		VALUES (?, ?, ?, ?, 'materialized', ?)`, id, revisionID, targetKind, targetID, at); err != nil {
		t.Fatalf("seed learning materialization: %v", err)
	}
	return id
}

// AgencyLearningCorrectionRecord is a correction of a learned pattern the
// removed Desk let the user make.
type AgencyLearningCorrectionRecord struct {
	ID                 string
	IdempotencyKey     string
	JourneyID          string
	CapabilityKey      string
	LearningRevisionID string
	OriginalStatement  string
	Correction         string
	CreatedAt          int64
}

// addLearningCorrection stores a correction the way the removed Desk did.
func addLearningCorrection(t *testing.T, store *ActivityStore, item AgencyLearningCorrectionRecord) {
	t.Helper()
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if _, err := store.db.Exec(`
		INSERT INTO agency_learning_corrections_v1 (
			id, idempotency_key, journey_id, capability_key, learning_revision_id,
			original_statement, correction, payload_json, payload_hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, '{}', 'hash', ?)`,
		item.ID, item.IdempotencyKey, item.JourneyID,
		strings.TrimSpace(item.CapabilityKey),
		item.LearningRevisionID, item.OriginalStatement, item.Correction,
		item.CreatedAt,
	); err != nil {
		t.Fatalf("store learning correction: %v", err)
	}
}

// insertLegacyRow inserts a row into a table the removed shadow agent wrote,
// filling every required column the test leaves out with an empty value.
func insertLegacyRow(t *testing.T, store *ActivityStore, table string, values map[string]any) {
	t.Helper()
	rows, err := store.db.Query(`SELECT name, type, "notnull", dflt_value FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	columns := []string{}
	args := []any{}
	for rows.Next() {
		var name, kind string
		var notNull int
		var dflt any
		if err := rows.Scan(&name, &kind, &notNull, &dflt); err != nil {
			t.Fatal(err)
		}
		value, ok := values[name]
		if !ok {
			if notNull == 0 || dflt != nil {
				continue
			}
			value = ""
			if strings.Contains(strings.ToUpper(kind), "INT") || strings.Contains(strings.ToUpper(kind), "REAL") {
				value = 0
			}
		}
		columns = append(columns, name)
		args = append(args, value)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", ")
	if _, err := store.db.Exec(`INSERT INTO `+table+` (`+strings.Join(columns, ", ")+`) VALUES (`+marks+`)`, args...); err != nil {
		t.Fatalf("insert legacy %s row: %v", table, err)
	}
}
