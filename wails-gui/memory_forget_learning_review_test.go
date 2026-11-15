package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seedMemoryForgetLearningReview(t *testing.T) (*App, []legacyLearningCandidate, string) {
	t.Helper()
	app, _ := newForgetTestApp(t)
	activity := newTestStore(t)
	app.activity = &activityMonitorState{store: activity}
	candidates := []legacyLearningCandidate{{
		RevisionID: "revision:one", ProjectionKey: "projection:one",
		TargetKind: correctedLearningTargetMemory, Statement: "Private learning that must be erased.",
	}}
	for _, kind := range []string{correctedLearningTargetMemory, correctedLearningTargetKnowledge} {
		candidates = append(candidates, legacyLearningCandidate{
			RevisionID: "independent-revision:" + kind, ProjectionKey: "independent:" + kind,
			TargetKind: kind, Statement: "Independent " + kind + " learning must remain visible.",
		})
	}
	runID := "learning-run"
	seedLegacyLearningRun(t, activity, runID, candidates, 1000)
	for _, sibling := range candidates[1:] {
		artifactID := legacyLearningMemoryTargetID(sibling.ProjectionKey)
		dir, err := memoryDir()
		if sibling.TargetKind == correctedLearningTargetKnowledge {
			dir, err = knowledgeDir()
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, artifactID+".md"), []byte("# Learning\n\n"+sibling.Statement+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		seedLegacyLearningMaterialization(t, activity, sibling.RevisionID, sibling.TargetKind, artifactID, 1100)
	}
	target := legacyLearningMemoryTargetID(candidates[0].ProjectionKey)
	if _, err := app.SaveMemoryEntry(target, "# Private learning\n\n"+candidates[0].Statement); err != nil {
		t.Fatal(err)
	}
	receiptID := seedLegacyLearningMaterialization(t, activity, candidates[0].RevisionID, correctedLearningTargetMemory, target, 1100)
	if _, err := activity.db.Exec(`INSERT INTO work_pattern_profile_snapshots_v1
		(id, policy_version, source_window_start, source_cutoff_at, source_hash, status, sample_count, session_count, confidence, canonical_json, sha256, created_at)
		VALUES ('forgotten-learning-profile', 'review-test', 1, 2, 'test', 'qualified', 1, 1, 1, ?, 'test', 3)`,
		`{"semanticSignals":[{"sourceKind":"verified_shadow_learning","sourceId":"`+receiptID+`"}]}`); err != nil {
		t.Fatal(err)
	}
	// Background understanding no longer runs, but receipts it wrote before
	// it was removed stay in the ledger and must still be forgotten.
	if _, err := activity.db.Exec(`INSERT INTO background_understanding_runs_v1
		(id, idempotency_key, trigger_kind, trigger_reason, mission_kind, source, policy_version, source_scope_json, started_at, created_at)
		VALUES ('forget-learning-background', 'forget-learning-background', 'manual', 'Retained learning', 'retained_activity', 'local_activity', 'test', '{}', 1000, 1000)`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"memory", "knowledge"} {
		if _, err := activity.db.Exec(`INSERT INTO background_understanding_materializations_v1
			(id, run_id, artifact_kind, artifact_id, action, summary, created_at)
			VALUES (?, 'forget-learning-background', ?, ?, 'created', ?, 1200)`,
			"forget-learning-background:"+kind, kind, target, "Recorded "+kind); err != nil {
			t.Fatal(err)
		}
	}
	return app, candidates, runID
}

func TestMemoryForgetReviewRemovesShadowAndBackgroundLearning(t *testing.T) {
	app, candidates, runID := seedMemoryForgetLearningReview(t)
	target := legacyLearningMemoryTargetID(candidates[0].ProjectionKey)
	before := memoryForgetTriggerDefinitions(t, app.activity.store.db)
	preview, err := app.GetMemoryForgetPreview(target)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range preview.Items {
		seen[item.ID] = true
	}
	if !seen[runID] || !seen[candidates[0].RevisionID] {
		t.Errorf("preview omitted source learning records: %+v", preview.Items)
	}
	for _, sibling := range candidates[1:] {
		if !seen[sibling.RevisionID] {
			t.Errorf("preview omitted affected shared-analysis revision %s", sibling.RevisionID)
		}
		var siblingReceipt string
		if err := app.activity.store.db.QueryRow(`SELECT id FROM shadow_task_learning_materializations WHERE revision_id = ?`, sibling.RevisionID).Scan(&siblingReceipt); err != nil {
			t.Fatal(err)
		}
		if !seen[siblingReceipt] {
			t.Errorf("preview omitted affected shared-analysis receipt %s", siblingReceipt)
		}
		for _, item := range preview.Items {
			if item.ID == siblingReceipt && item.Title != sibling.Statement {
				t.Errorf("shared-analysis receipt has no readable artifact statement: %+v", item)
			}
		}
	}
	if err := app.ForgetMemoryEntry(target, preview.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"shadow_task_learning_runs", "shadow_task_learning_revisions", "shadow_task_learning_materializations", "work_pattern_profile_snapshots_v1"} {
		if count := purposeCountRows(t, app.activity.store, table); count != 0 {
			t.Errorf("forgotten learning survives in %s: %d rows", table, count)
		}
	}
	var memoryReceipts, knowledgeReceipts int
	if err := app.activity.store.db.QueryRow(`SELECT count(*) FROM background_understanding_materializations_v1 WHERE artifact_kind='memory'`).Scan(&memoryReceipts); err != nil {
		t.Fatal(err)
	}
	if err := app.activity.store.db.QueryRow(`SELECT count(*) FROM background_understanding_materializations_v1 WHERE artifact_kind='knowledge'`).Scan(&knowledgeReceipts); err != nil {
		t.Fatal(err)
	}
	if memoryReceipts != 0 || knowledgeReceipts != 1 {
		t.Errorf("background memory receipts=%d, independent same-ID knowledge receipts=%d", memoryReceipts, knowledgeReceipts)
	}
	if !reflect.DeepEqual(before, memoryForgetTriggerDefinitions(t, app.activity.store.db)) {
		t.Fatal("forget changed immutable learning triggers")
	}
	for _, sibling := range candidates[1:] {
		dir, err := memoryDir()
		if sibling.TargetKind == correctedLearningTargetKnowledge {
			dir, err = knowledgeDir()
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(dir, legacyLearningMemoryTargetID(sibling.ProjectionKey)+".md"))
		if err != nil || !strings.Contains(string(content), sibling.Statement) {
			t.Errorf("forget removed unlisted sibling %s artifact: err=%v", sibling.TargetKind, err)
		}
	}
}

func TestMemoryForgetReviewLoaderSkipsLargeUnrelatedPayloads(t *testing.T) {
	rig := newSelfDreamTestRig(t)
	large := strings.Repeat("unrelated prompt text ", 100_000)
	for i := 0; i < 12; i++ {
		if _, _, err := rig.store.AddSelfDreamRun(SelfDreamRunRecord{
			IdempotencyKey: string(rune('a' + i)), TriggerKind: "manual", PolicyVersion: "test",
			AssemblerVersion: "test", SourceCutoffAt: 1, BaseSelfHash: "test", ContextHash: "test",
			InputHash: "test", PromptVersion: "test", Prompt: large, PromptHash: "test", CreatedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := rig.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	start := time.Now()
	rows, err := loadMemoryForgetDerivedRows(tx)
	if err != nil {
		t.Fatal(err)
	}
	bytesRead := 0
	for _, row := range rows {
		if row.title == row.id || row.recordType == "" {
			t.Errorf("analysis preview lacks a readable label: table=%s id=%s type=%s", row.table, row.id, row.recordType)
		}
		for _, value := range row.fields {
			bytesRead += len(value)
		}
		for _, forbidden := range []string{"prompt", "raw_output", "context_json", "payload_json"} {
			if _, loaded := row.fields[forbidden]; loaded {
				t.Errorf("preview loaded unrelated %s.%s", row.table, forbidden)
			}
		}
	}
	t.Logf("loader: %d queries, %d rows, %d field bytes, %s elapsed; fixture prompt bytes=%d", 1+len(memoryForgetDerivativeTables), len(rows), bytesRead, time.Since(start), len(large)*12)
	if bytesRead > 100_000 {
		t.Errorf("preview transferred %d bytes of unrelated payload", bytesRead)
	}
	items, err := collectMemoryForgetDerivatives(tx, "absent-memory")
	if err != nil || len(items) != 0 {
		t.Fatalf("unrelated analysis preview=%+v err=%v", items, err)
	}
}

func TestMemoryForgetReviewHistoricalSourceKeepsCurrentDerivatives(t *testing.T) {
	store := newTestStore(t)
	const sourceID = "independently-corrected-memory"
	const oldContent = "# Earlier synthesis\n\nAn observation derived from the forgotten source."
	const currentContent = "# User correction\n\nA separate current preference."
	entries := map[string]string{}
	for _, fixture := range []struct{ name, ref string }{
		{"old", taskContextSourceRef(taskContextSourceMemoryKind, sourceID, 1000, oldContent)},
		{"current", taskContextSourceRef(taskContextSourceMemoryKind, sourceID, 2000, currentContent)},
		{"legacy", "memory:" + sourceID},
	} {
		entry, _, err := store.AddSelfModelEntry(SelfModelEntry{
			Kind: selfModelKindPreference, Scope: "project:yansu", Statement: "Independent " + fixture.name + " derivative.",
			Status: selfModelStatusActive, Confidence: 0.9, ReasoningStatus: "explicit_user", CreatedAt: 3000,
			Evidence: []SelfModelEvidence{{SourceKind: taskContextSourceMemoryKind, SourceID: sourceID,
				SourceRefs: []string{fixture.ref}, Confidence: 1, OccurredAt: 2000}},
		})
		if err != nil {
			t.Fatal(err)
		}
		entries[fixture.name] = entry.ID
	}
	selector := "memory-version:" + sourceID + ":" + taskContextHash(strings.TrimSpace(oldContent))[:16]
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	preview, err := collectMemoryForgetDerivatives(tx, "forgotten-source", selector)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range preview {
		seen[item.ID] = true
	}
	if !seen[entries["old"]] || !seen[entries["legacy"]] || seen[entries["current"]] {
		t.Errorf("historical removal scope omitted old/ambiguous evidence or included current evidence: %+v", preview)
	}
	if err := deleteMemoryForgetDerivatives(tx, "forgotten-source", selector); err != nil {
		t.Fatal(err)
	}
	for name, id := range entries {
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM self_model_entries WHERE id = ?`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if name == "current" {
			want = 1
		}
		if count != want {
			t.Errorf("%s derivative count = %d, want %d", name, count, want)
		}
	}
}

func TestMemoryForgetReviewRejectsLateHistoricalSelfDream(t *testing.T) {
	app, _ := newForgetTestApp(t)
	store := newTestStore(t)
	app.activity = &activityMonitorState{store: store}
	if _, err := app.saveMemoryEntry("source", "# Source\n\nA private observation.", false); err != nil {
		t.Fatal(err)
	}
	old, err := app.saveMemoryEntry("corrected", "# Former synthesis\n\n- **MergedFrom**: source\n\nThe former private derived observation.", false)
	if err != nil {
		t.Fatal(err)
	}
	current, err := app.SaveMemoryEntry(old.ID, "# Independent correction\n\nA different current preference.")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := app.GetMemoryForgetPreview("source")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ForgetMemoryEntry("source", preview.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name, ref string
		forgotten bool
	}{
		{"old", taskContextSourceRef(taskContextSourceMemoryKind, old.ID, old.ModTime, old.Content), true},
		{"current", taskContextSourceRef(taskContextSourceMemoryKind, current.ID, current.ModTime, current.Content), false},
		{"legacy", "memory:" + old.ID, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			_, _, err := store.AddSelfDreamRun(SelfDreamRunRecord{
				IdempotencyKey: "late-history-" + fixture.name, TriggerKind: "manual", CreatedAt: 3000,
				PolicyVersion: "test", AssemblerVersion: "test", SourceCutoffAt: 2000,
				BaseSelfHash: "test", ContextHash: "test", InputHash: "test", PromptVersion: "test", PromptHash: "test",
				Manifest: []TaskContextManifestEntry{{Ref: fixture.ref, SourceKind: taskContextSourceMemoryKind, SourceID: current.ID, Included: true}},
			})
			if fixture.forgotten && !errors.Is(err, errMemoryForgotten) {
				t.Fatalf("late historical Self Dream accepted erased frozen version: %v", err)
			}
			if !fixture.forgotten && err != nil {
				t.Fatalf("current corrected Self Dream was rejected: %v", err)
			}
		})
	}
}
