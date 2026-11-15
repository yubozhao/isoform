package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// These are derived learning records, never the authority or observation
// ledgers. Children precede parents so SQLite's RESTRICT foreign keys remain
// enabled throughout the explicit, user-confirmed erasure.
type memoryForgetDerivativeTable struct {
	table, kind, recordType, primaryKey, columns string
}

var memoryForgetDerivativeTables = []memoryForgetDerivativeTable{
	{"work_pattern_profile_ablation_evaluations_v1", "work_profile", "evaluation", "id", "id, ablation_run_id"},
	{"work_pattern_profile_ablation_runs_v1", "work_profile", "analysis", "id", "id, profile_snapshot_id"},
	{"work_pattern_profile_corrections_v1", "work_profile", "revision", "id", "id, profile_snapshot_id, correction AS statement"},
	{"work_pattern_profile_evidence_v1", "work_profile", "evidence", "id", "id, profile_snapshot_id, source_kind, source_id"},
	{"work_pattern_profile_snapshots_v1", "work_profile", "snapshot", "id", "id, canonical_json"},
	{"self_dream_compilations_v1", "self_dream", "compilation", "id", "id, operation_id, decision_id, source_entry_ids_json, output_entry_ids_json"},
	{"self_dream_decisions_v1", "self_dream", "decision", "id", "id, operation_id"},
	{"self_dream_operations_v1", "self_dream", "entry", "id", "id, run_id, attempt_id, statement, target_entry_ids_json, evidence_refs_json"},
	{"self_dream_attempts_v1", "self_dream", "attempt", "id", "id, run_id"},
	{"self_dream_evaluations_v1", "self_dream", "evaluation", "id", "id, run_id"},
	{"self_dream_events_v1", "self_dream", "event", "id", "id, run_id"},
	{"self_dream_replays_v1", "self_dream", "analysis", "id", "id, run_id"},
	{"self_dream_runs_v1", "self_dream", "analysis", "id", "id, (SELECT json_group_array(json_extract(value, '$.id')) FROM json_each(base_self_json)) AS base_self_ids_json, manifest_json"},
	{"purpose_open_loop_relation_events_v1", "purpose_map", "event", "id", "id, relation_id, evidence_event_id"},
	{"purpose_open_loop_relations_v1", "purpose_map", "relation", "id", "id, subject_id, object_id"},
	{"purpose_open_loop_evidence_v1", "purpose_map", "evidence", "id", "id, event_id, source_kind, source_id"},
	{"purpose_open_loop_events_v1", "purpose_map", "event", "id", "id, open_loop_id, title"},
	{"purpose_open_loops_v1", "purpose_map", "entry", "id", "id"},
	{"purpose_reconciliation_receipts_v1", "purpose_map", "receipt", "id", "id, run_id, hypothesis_id, open_loop_id, event_id, relation_id"},
	{"purpose_extraction_hypotheses_v1", "purpose_map", "hypothesis", "id", "id, run_id, title, evidence_refs_json"},
	{"purpose_extraction_runs_v1", "purpose_map", "analysis", "id", "id, manifest_json, json_extract(input_json, '$.currentPurpose') AS current_purpose_json"},
	{"assistant_text_edits_v1", "self_dream", "revision", "id", "id, value AS statement, (SELECT json_group_array(id) FROM self_model_entries WHERE assistant_text_edits_v1.field_id IN ('self:' || entry_key || ':statement', 'self:' || entry_key || ':scope')) AS source_entry_ids_json"},
	{"self_model_entries", "self_dream", "entry", "id", "id, statement, evidence_json, contradictions_json"},
	{"shadow_task_learning_materializations", "shadow_learning", "receipt", "id", "id, revision_id, target_kind, target_id, (SELECT json_extract(candidate_json, '$.statement') FROM shadow_task_learning_revisions AS revision WHERE revision.revision_id = shadow_task_learning_materializations.revision_id) AS statement"},
	{"shadow_task_learning_revisions", "shadow_learning", "revision", "revision_id", "revision_id AS id, run_id, projection_key, target_kind, json_extract(candidate_json, '$.statement') AS statement"},
	{"shadow_task_learning_runs", "shadow_learning", "analysis", "id", "id"},
	{"background_understanding_materializations_v1", "background_learning", "receipt", "id", "id, artifact_kind, artifact_id, summary AS statement"},
}

type memoryForgetDerivedRow struct {
	table, kind, recordType, primaryKey, id, title string
	fields                                         map[string]string
	refs                                           []string
}

func memoryForgetSQLName(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// References are parsed as JSON values, not substring matches in statements.
// In particular memory:one must never match memory:one-other.
func memoryForgetJSONRefs(value any, reference bool, refs *[]string) {
	switch node := value.(type) {
	case map[string]any:
		sourceKind, _ := node["sourceKind"].(string)
		if sourceKind == "" {
			sourceKind, _ = node["source_kind"].(string)
		}
		for key, child := range node {
			if key == "targetId" {
				if kind, ok := node["targetKind"].(string); ok && kind != "" {
					if id, ok := child.(string); ok && id != "" {
						*refs = append(*refs, kind+":"+id)
					}
					continue
				}
			}
			if (key == "sourceId" || key == "source_id") && sourceKind != "" {
				if id, ok := child.(string); ok && id != "" {
					*refs = append(*refs, sourceKind+":"+id)
				}
				continue
			}
			switch key {
			case "id", "sourceId", "source_id", "ref", "entryId", "entry_id",
				"sourceRefs", "evidenceRefs", "targetEntryIds", "sourceEntryIds",
				"outputEntryIds", "profileSnapshotId", "openLoopId", "runId",
				"revisionId", "materializationReceiptId", "currentEventId":
				memoryForgetJSONRefs(child, true, refs)
			default:
				memoryForgetJSONRefs(child, false, refs)
			}
		}
	case []any:
		for _, child := range node {
			memoryForgetJSONRefs(child, reference, refs)
		}
	case string:
		if reference && node != "" {
			*refs = append(*refs, node)
		}
	}
}

func loadMemoryForgetDerivedRows(tx *sql.Tx) ([]memoryForgetDerivedRow, error) {
	// Read only the provenance graph and display text. Prompts, model output,
	// duplicated payloads, and frozen source bodies are not needed for matching.
	// One schema query replaces a table-existence query for every ledger.
	schemaRows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, err
	}
	tables := map[string]bool{}
	for schemaRows.Next() {
		var name string
		if err := schemaRows.Scan(&name); err != nil {
			schemaRows.Close()
			return nil, err
		}
		tables[name] = true
	}
	err = schemaRows.Err()
	schemaRows.Close()
	if err != nil {
		return nil, err
	}
	var result []memoryForgetDerivedRow
	for _, spec := range memoryForgetDerivativeTables {
		if !tables[spec.table] {
			continue
		}
		rows, err := tx.Query(`SELECT ` + spec.columns + ` FROM ` + memoryForgetSQLName(spec.table) + ` ORDER BY id`)
		if err != nil {
			return nil, fmt.Errorf("load memory derivatives from %s: %w", spec.table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return nil, err
			}
			item := memoryForgetDerivedRow{table: spec.table, kind: spec.kind, recordType: spec.recordType, primaryKey: spec.primaryKey, fields: map[string]string{}}
			for i, column := range columns {
				var value string
				switch raw := values[i].(type) {
				case string:
					value = raw
				case []byte:
					value = string(raw)
				default:
					continue
				}
				item.fields[column] = value
				if strings.HasSuffix(column, "_json") && value != "" {
					var decoded any
					if err := json.Unmarshal([]byte(value), &decoded); err != nil {
						rows.Close()
						return nil, fmt.Errorf("read memory derivative %s.%s: %w", spec.table, column, err)
					}
					memoryForgetJSONRefs(decoded, true, &item.refs)
				} else if strings.HasSuffix(column, "_id") && column != "source_id" && column != "target_id" && column != "artifact_id" && value != "" {
					item.refs = append(item.refs, value)
				}
			}
			if sourceID := item.fields["source_id"]; sourceID != "" {
				ref := sourceID
				if kind := item.fields["source_kind"]; kind != "" {
					ref = kind + ":" + sourceID
				}
				item.refs = append(item.refs, ref)
			}
			for _, target := range []struct{ kind, id string }{
				{item.fields["target_kind"], item.fields["target_id"]},
				{item.fields["artifact_kind"], item.fields["artifact_id"]},
			} {
				if target.id != "" {
					item.refs = append(item.refs, target.kind+":"+target.id)
				}
			}
			if spec.table == "shadow_task_learning_revisions" && item.fields["target_kind"] == correctedLearningTargetMemory {
				// The target is deterministic, including a crash between saving
				// the Markdown and recording its materialization receipt.
				item.refs = append(item.refs, "memory:"+legacyLearningMemoryTargetID(item.fields["projection_key"]))
			}
			item.id = item.fields["id"]
			item.title = item.fields["title"]
			if item.title == "" {
				item.title = item.fields["statement"]
			}
			result = append(result, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func memoryForgetRefMatches(ref string, ids map[string]bool) bool {
	if ids[ref] {
		return true
	}
	// The first component is a source kind; the next component is the stable
	// record ID. Further components identify a version, timestamp, or hash.
	kind, record, ok := strings.Cut(ref, ":")
	if !ok {
		return false
	}
	switch kind {
	case "memory", "memory_markdown", "self", "canonical_self", "typed_self", "self_model_entry",
		"work-profile", "work-profile-correction", "work_pattern_profile", "purpose", "purpose_open_loop",
		"self_dream_user_decision", "self_dream_rollout_decision", "self_dream_supersession",
		"verified_shadow_learning":
		for id := range ids {
			if record == id || strings.HasPrefix(record, id+":") {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// Call while holding memoryMutationMu.RLock, after a model returns and before
// persisting its frozen evidence. A source erased during inference must not be
// recreated through a late Self Dream or Purpose result.
func validateMemoryForgetSource(db *sql.DB, kind, id, content string, frozenRefs ...string) error {
	var table string
	switch kind {
	case taskContextSourceMemoryKind, "memory":
		dir, err := memoryDir()
		if err != nil {
			return err
		}
		if len(frozenRefs) > 0 {
			forgotten, err := isMemoryHistoricalReferenceForgotten(dir, id, append([]string{id}, frozenRefs...), time.Now())
			if err != nil {
				return err
			}
			if forgotten {
				return errMemoryForgotten
			}
		}
		if content == "" {
			if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
				return fmt.Errorf("invalid memory entry ID")
			}
			// Receipt and frozen-source callers have only the stable ID. Use
			// the prepared semantic receipt for the actual saved bytes, never
			// ask the matcher to verify an artificial empty observation.
			data, readErr := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(id, ".md")+".md"))
			if readErr != nil && !os.IsNotExist(readErr) {
				return readErr
			}
			content = string(data)
		}
		forgotten, err := isMemoryForgotten(dir, id, content, time.Now())
		if err != nil {
			return err
		}
		if forgotten {
			return errMemoryForgotten
		}
		return nil
	case taskContextSourceSelfKind:
		table = "self_model_entries"
	case "work_pattern_profile":
		table = "work_pattern_profile_snapshots_v1"
	case "work_pattern_profile_correction":
		table = "work_pattern_profile_corrections_v1"
	case "purpose_open_loop":
		table = "purpose_open_loops_v1"
	default:
		return nil
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+memoryForgetSQLName(table)+` WHERE id = ?)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("frozen learning source %s/%s is no longer available", kind, id)
	}
	return nil
}

func planMemoryForgetDerivatives(tx *sql.Tx, memoryID string, additionalIDs ...string) ([]memoryForgetDerivedRow, error) {
	if tx == nil {
		return nil, fmt.Errorf("memory derivative transaction is unavailable")
	}
	rows, err := loadMemoryForgetDerivedRows(tx)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, id := range append([]string{memoryID}, additionalIDs...) {
		ids[id], ids[id+".md"] = true, true
	}
	selected := make(map[int]bool)
	for changed := true; changed; {
		changed = false
		for i, row := range rows {
			if selected[i] {
				continue
			}
			matches := ids[row.id]
			for _, ref := range row.refs {
				matches = matches || memoryForgetRefMatches(ref, ids) || memoryForgetHistoricalRefMatches(ref, row.refs, ids)
			}
			if !matches {
				continue
			}
			selected[i], ids[row.id], changed = true, true, true
			// Frozen runs and their owned evidence cannot survive with holes.
			// Relations only propagate toward their own event tree, never to the
			// unrelated open loop on the other end of a relation.
			switch row.table {
			case "self_dream_events_v1", "self_dream_attempts_v1", "self_dream_operations_v1", "self_dream_evaluations_v1", "self_dream_replays_v1",
				"purpose_extraction_hypotheses_v1", "shadow_task_learning_revisions":
				ids[row.fields["run_id"]] = true
			case "shadow_task_learning_materializations":
				ids[row.fields["revision_id"]] = true
			case "self_dream_decisions_v1", "self_dream_compilations_v1":
				ids[row.fields["operation_id"]] = true
			case "purpose_open_loop_evidence_v1":
				ids[row.fields["event_id"]] = true
			case "purpose_open_loop_events_v1", "purpose_reconciliation_receipts_v1":
				ids[row.fields["open_loop_id"]] = true
			case "purpose_open_loop_relation_events_v1":
				ids[row.fields["relation_id"]] = true
			case "work_pattern_profile_evidence_v1":
				ids[row.fields["profile_snapshot_id"]] = true
			}
			if row.table == "self_dream_compilations_v1" {
				var outputIDs []string
				if err := json.Unmarshal([]byte(row.fields["output_entry_ids_json"]), &outputIDs); err != nil {
					return nil, err
				}
				for _, id := range outputIDs {
					ids[id] = true
				}
			}
			delete(ids, "")
		}
	}
	result := make([]memoryForgetDerivedRow, 0, len(selected))
	for i, row := range rows {
		if selected[i] {
			result = append(result, row)
		}
	}
	return result, nil
}

func collectMemoryForgetDerivatives(tx *sql.Tx, memoryID string, additionalIDs ...string) ([]MemoryForgetItem, error) {
	rows, err := planMemoryForgetDerivatives(tx, memoryID, additionalIDs...)
	if err != nil {
		return nil, err
	}
	items := make([]MemoryForgetItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, MemoryForgetItem{Kind: row.kind, ID: row.id, Title: row.title, RecordType: row.recordType})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func deleteMemoryForgetDerivatives(tx *sql.Tx, memoryID string, additionalIDs ...string) error {
	rows, err := planMemoryForgetDerivatives(tx, memoryID, additionalIDs...)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	// Removing/reinstating the exact immutable-delete triggers within this
	// transaction is a narrow erasure exception. Other connections never see
	// the exception, update guards remain active, and rollback restores it all.
	triggers := map[string]string{}
	for _, row := range rows {
		if _, loaded := triggers[row.table]; loaded {
			continue
		}
		triggerRows, err := tx.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ? AND name IN (?, ?, ?)`,
			row.table, row.table+"_no_delete", strings.TrimSuffix(row.table, "_v1")+"_no_delete_v1", "work_pattern_profile_ablation_eval_no_delete_v1")
		if err != nil {
			return err
		}
		type trigger struct{ name, ddl string }
		var found []trigger
		for triggerRows.Next() {
			var item trigger
			if err := triggerRows.Scan(&item.name, &item.ddl); err != nil {
				triggerRows.Close()
				return err
			}
			found = append(found, item)
		}
		err = triggerRows.Err()
		triggerRows.Close()
		if err != nil {
			return err
		}
		var definitions strings.Builder
		for _, item := range found {
			if _, err := tx.Exec(`DROP TRIGGER ` + memoryForgetSQLName(item.name)); err != nil {
				return err
			}
			definitions.WriteString(item.ddl + ";\n")
		}
		triggers[row.table] = definitions.String()
	}
	for _, row := range rows {
		if _, err := tx.Exec(`DELETE FROM `+memoryForgetSQLName(row.table)+` WHERE `+memoryForgetSQLName(row.primaryKey)+` = ?`, row.id); err != nil {
			return fmt.Errorf("forget derivative %s/%s: %w", row.table, row.id, err)
		}
	}
	for _, spec := range memoryForgetDerivativeTables {
		if ddl := triggers[spec.table]; ddl != "" {
			if _, err := tx.Exec(ddl); err != nil {
				return fmt.Errorf("restore immutable memory derivative guards: %w", err)
			}
		}
	}
	return nil
}

// legacyLearningMemoryTargetID is the memory ID shadow learning wrote for a
// projection key, so forgetting that memory still finds its learning rows.
func legacyLearningMemoryTargetID(projectionKey string) string {
	return "shadow-learning-" + sha256Hex([]byte(projectionKey))[:16]
}
