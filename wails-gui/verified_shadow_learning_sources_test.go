package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func verifiedShadowLearningTestCandidate(
	t *testing.T,
	records []ShadowTaskLearningEvaluationRecord,
	targetKind, learningKind string,
) (ShadowTaskLearningProjection, ShadowTaskLearningCandidate) {
	t.Helper()
	projection, err := BuildShadowTaskLearningProjection(records)
	if err != nil {
		t.Fatalf("build production Shadow learning projection: %v", err)
	}
	candidate := shadowTaskLearningFindCandidate(
		t,
		projection,
		targetKind,
		learningKind,
	)
	if candidate.Qualification.Decision != ShadowTaskLearningDecisionQualified {
		t.Fatalf("test candidate is not qualified: %+v", candidate.Qualification)
	}
	return ShadowTaskLearningProjection{
		PolicyVersion:   projection.PolicyVersion,
		AuthorityEffect: projection.AuthorityEffect,
		EvaluationCount: projection.EvaluationCount,
		Candidates:      []ShadowTaskLearningCandidate{candidate},
	}, candidate
}

func persistVerifiedShadowLearningTestCandidate(
	t *testing.T,
	store *ActivityStore,
	projection ShadowTaskLearningProjection,
	candidate ShadowTaskLearningCandidate,
	revisionCreatedAt, receiptCreatedAt int64,
) (ShadowTaskLearningRun, ShadowTaskLearningMaterialization) {
	t.Helper()
	run, added, err := PersistShadowTaskLearningProjection(
		store,
		projection,
		revisionCreatedAt,
	)
	if err != nil || !added {
		t.Fatalf("persist verified Shadow learning: added=%v err=%v", added, err)
	}
	app := &App{activity: &activityMonitorState{store: store}}
	receipt, err := app.materializeShadowTaskLearningCandidateAt(
		candidate,
		revisionCreatedAt,
		receiptCreatedAt,
	)
	if err != nil || receipt.Status != ShadowTaskLearningMaterializationMaterialized {
		t.Fatalf("materialize verified Shadow learning: receipt=%+v err=%v", receipt, err)
	}
	return run, receipt
}

func verifiedShadowLearningSourceByRevision(
	result VerifiedShadowLearningSourceResult,
	revisionID string,
) *VerifiedShadowLearningSource {
	for index := range result.Sources {
		if result.Sources[index].Learning.RevisionID == revisionID {
			return &result.Sources[index]
		}
	}
	return nil
}

func verifiedShadowLearningHasExclusion(
	result VerifiedShadowLearningSourceResult,
	revisionID, reason string,
) bool {
	for _, exclusion := range result.Exclusions {
		if exclusion.SourceID == revisionID && exclusion.Reason == reason {
			return true
		}
	}
	return false
}

func TestVerifiedShadowLearningSourceKindCannotBypassProvenance(t *testing.T) {
	_, err := AssembleTaskContext(TaskContextAssemblyInput{
		EpisodeID: "forged-verified-source",
		CutoffAt:  2_000,
		CreatedAt: 3_000,
		Candidates: []TaskContextCandidate{{
			Ref:             "forged-verified-source",
			Section:         TaskContextSectionSelf,
			SourceKind:      taskContextSourceVerifiedShadowLearningKind,
			SourceID:        "forged-receipt",
			ObservedAt:      1_000,
			Content:         "Unsupported evaluated claim.",
			EvidenceClass:   TaskContextEvidenceEvaluatedProjection,
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance:       1,
		}},
	})
	if err == nil {
		t.Fatal("verified source kind without immutable provenance was accepted")
	}
}

func TestVerifiedShadowLearningIsCorrectionAwareAtTheTaskCutoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	base := time.Now().UnixMilli() + 20_000
	record := shadowTaskLearningWithReasoning(
		shadowTaskLearningTestRecord(2),
		ShadowTaskEvidenceExplicitReasoning,
		"Confirm the owner before drafting next steps.",
	)
	projection, candidate := verifiedShadowLearningTestCandidate(
		t,
		[]ShadowTaskLearningEvaluationRecord{record},
		ShadowTaskLearningTargetMemory,
		ShadowTaskLearningKindReasoning,
	)
	_, _ = persistVerifiedShadowLearningTestCandidate(
		t, store, projection, candidate, base, base+100,
	)
	addLearningCorrection(t, store, AgencyLearningCorrectionRecord{
		IdempotencyKey:     "verified-learning-cutoff-correction",
		CapabilityKey:      candidate.CapabilityKey,
		LearningRevisionID: candidate.RevisionID,
		OriginalStatement:  candidate.Statement,
		Correction:         "Confirm both the owner and severity before drafting.",
		CreatedAt:          base + 300,
	})

	historical, err := VerifiedShadowLearningSourcesAt(store, base+200, "owner")
	if err != nil || verifiedShadowLearningSourceByRevision(historical, candidate.RevisionID) == nil {
		t.Fatalf("future correction changed historical context result=%+v err=%v", historical, err)
	}
	future, err := VerifiedShadowLearningSourcesAt(store, base+400, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if verifiedShadowLearningSourceByRevision(future, candidate.RevisionID) != nil ||
		!verifiedShadowLearningHasExclusion(
			future,
			candidate.RevisionID,
			TaskContextSourceReasonLearningCorrected,
		) {
		t.Fatalf("corrected learning remained eligible: %+v", future)
	}
}

func TestVerifiedShadowLearningDoesNotFallBackToSupersededRevision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	base := time.Now().UnixMilli() + 25_000
	statement := "Confirm the owner before drafting next steps."
	firstRecord := shadowTaskLearningWithReasoning(
		shadowTaskLearningTestRecord(31),
		ShadowTaskEvidenceExplicitReasoning,
		statement,
	)
	firstProjection, firstCandidate := verifiedShadowLearningTestCandidate(
		t,
		[]ShadowTaskLearningEvaluationRecord{firstRecord},
		ShadowTaskLearningTargetMemory,
		ShadowTaskLearningKindReasoning,
	)
	_, _ = persistVerifiedShadowLearningTestCandidate(
		t,
		store,
		firstProjection,
		firstCandidate,
		base,
		base,
	)
	secondRecord := shadowTaskLearningWithReasoning(
		shadowTaskLearningTestRecord(32),
		ShadowTaskEvidenceExplicitReasoning,
		statement,
	)
	secondProjection, secondCandidate := verifiedShadowLearningTestCandidate(
		t,
		[]ShadowTaskLearningEvaluationRecord{firstRecord, secondRecord},
		ShadowTaskLearningTargetMemory,
		ShadowTaskLearningKindReasoning,
	)
	if firstCandidate.ProjectionKey != secondCandidate.ProjectionKey ||
		firstCandidate.RevisionID == secondCandidate.RevisionID {
		t.Fatalf("test did not produce append-only revisions first=%+v second=%+v", firstCandidate, secondCandidate)
	}
	if _, added, err := PersistShadowTaskLearningProjection(
		store,
		secondProjection,
		base+200,
	); err != nil || !added {
		t.Fatalf("persist newer unmaterialized revision: added=%v err=%v", added, err)
	}

	result, err := VerifiedShadowLearningSourcesAt(store, base+300, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if verifiedShadowLearningSourceByRevision(result, firstCandidate.RevisionID) != nil ||
		!verifiedShadowLearningHasExclusion(
			result,
			firstCandidate.RevisionID,
			TaskContextSourceReasonLearningSuperseded,
		) ||
		!verifiedShadowLearningHasExclusion(
			result,
			secondCandidate.RevisionID,
			TaskContextSourceReasonLearningMissingReceipt,
		) {
		t.Fatalf("stale qualified learning survived newer revision: %+v", result)
	}
}

func TestVerifiedShadowLearningRejectsChangedMaterializationTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := newTestStore(t)
	base := time.Now().UnixMilli() + 30_000
	record := shadowTaskLearningWithReasoning(
		shadowTaskLearningTestRecord(3),
		ShadowTaskEvidenceExplicitReasoning,
		"Confirm incident scope before drafting.",
	)
	projection, candidate := verifiedShadowLearningTestCandidate(
		t,
		[]ShadowTaskLearningEvaluationRecord{record},
		ShadowTaskLearningTargetMemory,
		ShadowTaskLearningKindReasoning,
	)
	_, receipt := persistVerifiedShadowLearningTestCandidate(
		t, store, projection, candidate, base, base+100,
	)
	path := filepath.Join(home, configDirName, "memory", receipt.TargetID+".md")
	if err := os.WriteFile(path, []byte("changed after materialization\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := VerifiedShadowLearningSourcesAt(store, base+200, "incident")
	if err != nil {
		t.Fatal(err)
	}
	if verifiedShadowLearningSourceByRevision(result, candidate.RevisionID) != nil ||
		!verifiedShadowLearningHasExclusion(
			result,
			candidate.RevisionID,
			TaskContextSourceReasonLearningTargetMismatch,
		) {
		t.Fatalf("changed materialization target was admitted: %+v", result)
	}
}

func TestVerifiedShadowLearningUsesTypedSelfReceiptTime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	base := time.Now().UnixMilli() + 40_000
	statement := "Confirm incident ownership before describing next steps."
	records := []ShadowTaskLearningEvaluationRecord{
		shadowTaskLearningWithReasoning(
			shadowTaskLearningTestRecord(11),
			ShadowTaskEvidenceExplicitReasoning,
			statement,
		),
		shadowTaskLearningWithReasoning(
			shadowTaskLearningTestRecord(12),
			ShadowTaskEvidenceExplicitReasoning,
			statement,
		),
		shadowTaskLearningWithReasoning(
			shadowTaskLearningTestRecord(13),
			ShadowTaskEvidenceExplicitReasoning,
			statement,
		),
	}
	projection, candidate := verifiedShadowLearningTestCandidate(
		t,
		records,
		ShadowTaskLearningTargetTypedSelf,
		ShadowTaskLearningKindReasoning,
	)
	if candidate.SuggestedSelfStatus != selfModelStatusActive {
		t.Fatalf("test typed Self candidate status=%q", candidate.SuggestedSelfStatus)
	}
	_, receipt := persistVerifiedShadowLearningTestCandidate(
		t, store, projection, candidate, base, base+500,
	)
	entry, err := store.GetSelfModelEntryByID(receipt.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.CreatedAt != base {
		t.Fatalf("typed Self target is not backdated for recovery test: %+v", entry)
	}

	before, err := VerifiedShadowLearningSourcesAt(store, base+250, "incident")
	if err != nil {
		t.Fatal(err)
	}
	if verifiedShadowLearningSourceByRevision(before, candidate.RevisionID) != nil ||
		!verifiedShadowLearningHasExclusion(
			before,
			candidate.RevisionID,
			TaskContextSourceReasonLearningAfterCutoff,
		) {
		t.Fatalf("backdated typed Self bypassed receipt cutoff: %+v", before)
	}
	after, err := VerifiedShadowLearningSourcesAt(store, base+600, "incident")
	if err != nil || verifiedShadowLearningSourceByRevision(after, candidate.RevisionID) == nil {
		t.Fatalf("typed Self absent after receipt result=%+v err=%v", after, err)
	}
}

func TestVerifiedShadowLearningRechecksFirstRunContainment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	base := time.Now().UnixMilli() + 50_000
	record := shadowTaskLearningWithReasoning(
		shadowTaskLearningTestRecord(21),
		ShadowTaskEvidenceExplicitReasoning,
		"Confirm scope before drafting.",
	)
	projection, candidate := verifiedShadowLearningTestCandidate(
		t,
		[]ShadowTaskLearningEvaluationRecord{record},
		ShadowTaskLearningTargetMemory,
		ShadowTaskLearningKindReasoning,
	)
	_, _ = persistVerifiedShadowLearningTestCandidate(
		t, store, projection, candidate, base, base+100,
	)
	firstRun := seedFirstRunContamination(t, store)
	excludeFirstRunSource(
		t,
		store,
		firstRun,
		"shadow_run",
		candidate.Evidence[0].ShadowRunID,
	)
	result, err := VerifiedShadowLearningSourcesAt(store, base+200, "drafting")
	if err != nil {
		t.Fatal(err)
	}
	if verifiedShadowLearningSourceByRevision(result, candidate.RevisionID) != nil ||
		!verifiedShadowLearningHasExclusion(
			result,
			candidate.RevisionID,
			TaskContextSourceReasonLearningFirstRun,
		) {
		t.Fatalf("guided learning was admitted: %+v", result)
	}
}

func TestVerifiedShadowCapabilitySelectionHasNoBlindCountCap(t *testing.T) {
	store := newTestStore(t)
	base := time.Now().UnixMilli() + 60_000
	const count = 205
	for index := 0; index < count; index++ {
		records := make([]ShadowTaskLearningEvaluationRecord, 0, 5)
		for recordIndex := 0; recordIndex < 5; recordIndex++ {
			record := shadowTaskLearningTestRecord(recordIndex + 1)
			record.CapabilityKey = fmt.Sprintf("verified.capability.%03d", index)
			record.TaskEpisodeID += fmt.Sprintf(":%03d", index)
			record.Input.TaskEpisodeID = record.TaskEpisodeID
			record.ShadowRunID += fmt.Sprintf(":%03d", index)
			record.Input.ShadowRunID = record.ShadowRunID
			record.FrozenContextHash = workPatternHash([]byte(
				fmt.Sprintf("%s:%02d", record.CapabilityKey, recordIndex),
			))
			record.Input.FrozenContextHash = record.FrozenContextHash
			records = append(records, record)
		}
		projection, candidate := verifiedShadowLearningTestCandidate(
			t,
			records,
			ShadowTaskLearningTargetCapabilityProfile,
			ShadowTaskLearningKindCapabilityEvidence,
		)
		createdAt := base + int64(index)
		_, _ = persistVerifiedShadowLearningTestCandidate(
			t,
			store,
			projection,
			candidate,
			createdAt,
			createdAt,
		)
	}

	signals, err := listWorkPatternCapabilitySignalsAt(store, base+count+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != count {
		t.Fatalf("verified capability count=%d, want %d", len(signals), count)
	}
	selected, manifest, err := selectWorkPatternCapabilitySignalsAt(
		store,
		base+count+1,
		"verified capability",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) != count || len(selected) == 0 || len(selected) >= count {
		t.Fatalf("selected=%d manifest=%d, want bounded prompt and complete manifest", len(selected), len(manifest))
	}
	for _, item := range manifest {
		if item.Learning == nil || item.Learning.MaterializationReceiptID == "" ||
			(item.Reason != "selected_for_current_context" &&
				item.Reason != "omitted_by_relevance" &&
				item.Reason != "omitted_by_token_budget") {
			t.Fatalf("incomplete capability selection lineage: %+v", item)
		}
	}
}

func TestVerifiedShadowLearningBuildsTheNextImmutableProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	base := time.Now().UnixMilli() + 70_000
	records := []ShadowTaskLearningEvaluationRecord{
		shadowTaskLearningTestRecord(41),
		shadowTaskLearningTestRecord(42),
		shadowTaskLearningTestRecord(43),
		shadowTaskLearningTestRecord(44),
		shadowTaskLearningTestRecord(45),
	}
	projection, candidate := verifiedShadowLearningTestCandidate(
		t,
		records,
		ShadowTaskLearningTargetCapabilityProfile,
		ShadowTaskLearningKindCapabilityEvidence,
	)
	_, receipt := persistVerifiedShadowLearningTestCandidate(
		t,
		store,
		projection,
		candidate,
		base,
		base+100,
	)
	snapshot, added, err := store.EnsureWorkPatternProfileSnapshot(
		base+200,
		base+300,
	)
	if err != nil || !added {
		t.Fatalf("build next profile: added=%v err=%v", added, err)
	}
	foundProjection := false
	for _, item := range snapshot.Projections {
		if item.ScopeKind == WorkPatternScopeCapability &&
			item.ScopeKey == candidate.CapabilityKey &&
			item.Status == WorkPatternProfileStatusQualified {
			foundProjection = true
		}
	}
	if !foundProjection {
		t.Fatalf("verified capability projection missing: %+v", snapshot.Projections)
	}
	evidence, err := store.ListWorkPatternProfileEvidence(snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundReceipt := false
	for _, item := range evidence {
		if item.SourceKind == taskContextSourceVerifiedShadowLearningKind &&
			item.SourceID == receipt.ID && item.Included &&
			item.Reason == "verified_evaluated_capability_source" {
			foundReceipt = true
		}
	}
	if !foundReceipt {
		t.Fatalf("profile receipt provenance missing: %+v", evidence)
	}

	sources, err := GatherTaskContextSources(TaskContextSourceRequest{
		Store:    store,
		CutoffAt: snapshot.SourceCutoffAt,
		Query:    candidate.CapabilityKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	context, err := buildWorkPatternProfileContext(
		store,
		snapshot,
		sources,
		candidate.CapabilityKey,
		nil,
		nil,
		WorkPatternInfluenceProfile,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(context.CapabilitySignals) != 1 ||
		context.CapabilitySignals[0].Learning == nil ||
		context.CapabilitySignals[0].Learning.RevisionID != candidate.RevisionID ||
		context.CapabilitySignals[0].Learning.MaterializationReceiptID != receipt.ID {
		t.Fatalf("profile context learning signal=%+v", context.CapabilitySignals)
	}
	foundSelection := false
	for _, item := range context.SelectionManifest {
		if item.Included && item.Learning != nil &&
			item.Learning.RevisionID == candidate.RevisionID &&
			item.Learning.MaterializationReceiptID == receipt.ID {
			foundSelection = true
		}
	}
	if !foundSelection {
		t.Fatalf("profile selection lineage missing: %+v", context.SelectionManifest)
	}
	compact, err := compileWorkPatternProfilePrompt(
		context,
		workPatternProfilePromptMaximumBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact.CapabilitySignals) != 1 ||
		compact.CapabilitySignals[0].Learning != nil {
		t.Fatalf("internal learning lineage entered model prompt: %+v", compact.CapabilitySignals)
	}
	for _, item := range compact.SelectionManifest {
		if item.Learning != nil {
			t.Fatalf("internal selection lineage entered model prompt: %+v", item)
		}
	}
	promptProjection, err := json.Marshal(compact)
	if err != nil {
		t.Fatal(err)
	}
	selectionEvidence, err := buildWorkPatternProfileSelectionEvidence(
		context,
		compact,
		context,
		string(promptProjection),
		"later-task-episode",
		base+400,
	)
	if err != nil {
		t.Fatal(err)
	}
	provenance, ok := decodeWorkPatternProfileSelectionEvidence(selectionEvidence)
	if !ok {
		t.Fatalf("decode frozen selection evidence: %+v", selectionEvidence)
	}
	foundOnsetLineage := false
	foundShadowLineage := false
	for _, item := range provenance.OnsetSelectionManifest {
		if item.Learning != nil &&
			item.Learning.RevisionID == candidate.RevisionID &&
			item.Learning.MaterializationReceiptID == receipt.ID &&
			item.OnsetPromptIncluded != nil &&
			*item.OnsetPromptIncluded {
			foundOnsetLineage = true
		}
	}
	for _, item := range provenance.SelectionManifest {
		if item.Learning != nil &&
			item.Learning.RevisionID == candidate.RevisionID &&
			item.Learning.MaterializationReceiptID == receipt.ID &&
			item.ShadowContextIncluded != nil &&
			*item.ShadowContextIncluded {
			foundShadowLineage = true
		}
	}
	if !foundOnsetLineage || !foundShadowLineage {
		t.Fatalf(
			"frozen selection receipt lost exact admission: onset=%v shadow=%v provenance=%+v",
			foundOnsetLineage,
			foundShadowLineage,
			provenance,
		)
	}
	for _, table := range []string{"authority_decisions", "initiatives"} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("verified learning created %d rows in %s", count, table)
		}
	}
}
