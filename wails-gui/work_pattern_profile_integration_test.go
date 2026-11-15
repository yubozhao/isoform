package main

import (
	"fmt"
	"strings"
	"testing"
)

func qualifiedWorkPatternFixture(
	t *testing.T,
	store *ActivityStore,
) WorkPatternProfileSnapshot {
	t.Helper()
	cutoff := int64(0)
	for session := 0; session < 3; session++ {
		cutoff = saveWorkPatternSession(
			t,
			store,
			"profile-fixture-"+string(rune('a'+session)),
			"done",
			10_000+int64(session)*200_000,
			[]string{
				"Safari",
				"ChatGPT",
				"Terminal",
				"Safari",
				"ChatGPT",
				"Terminal",
			},
		)
	}
	snapshot, _, err := store.EnsureWorkPatternProfileSnapshot(
		cutoff,
		cutoff+1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != WorkPatternProfileStatusQualified {
		t.Fatalf("fixture status=%s", snapshot.Status)
	}
	return snapshot
}

func TestWorkPatternProfileRejectsFutureOnsetPrior(t *testing.T) {
	input := TaskOnsetInput{
		SourceKind:  "activity",
		SourceID:    "future-profile",
		WindowStart: 10_000,
		CutoffAt:    20_000,
		Evidence: []TaskOnsetEvidence{{
			Ref:        "event:1",
			SourceKind: "activity_event",
			SourceID:   "1",
			OccurredAt: 10_000,
			Summary:    "Focused work",
		}},
		WorkProfile: &WorkPatternProfileContext{
			SnapshotID:     "profile",
			SnapshotSHA256: strings.Repeat("a", 64),
			PolicyVersion:  WorkPatternProfilePolicyVersion,
			SourceCutoffAt: 10_000,
			InfluenceMode:  WorkPatternInfluenceProfile,
		},
	}
	if _, err := normalizeTaskOnsetInput(input); err == nil ||
		!strings.Contains(err.Error(), "precede") {
		t.Fatalf("future profile error=%v", err)
	}
}

func TestWorkPatternProfileSemanticContextUsesBudgetNotItemCount(
	t *testing.T,
) {
	candidates := make([]TaskContextCandidate, 0, 40)
	for index := 0; index < 40; index++ {
		candidates = append(candidates, TaskContextCandidate{
			Ref:        "memory:" + string(rune('a'+index)),
			Section:    TaskContextSectionKnowledge,
			SourceKind: "memory_markdown",
			SourceID:   "source",
			ObservedAt: int64(1_000 + index),
			Content: fmt.Sprintf(
				"A source-backed working preference for context %d.",
				index,
			),
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyInclude,
			Relevance:       0.8,
		})
	}
	signals := selectWorkPatternSemanticSignals(
		TaskContextSourceResult{Candidates: candidates},
		100_000,
	)
	if len(signals) != len(candidates) {
		t.Fatalf(
			"semantic signals=%d, want all %d within budget",
			len(signals),
			len(candidates),
		)
	}
	totalTokens := 0
	for _, signal := range signals {
		totalTokens += workPatternEstimatedTokens(signal.Content)
	}
	if totalTokens > workPatternSemanticContextTokenBudget {
		t.Fatalf(
			"semantic context tokens=%d, budget=%d",
			totalTokens,
			workPatternSemanticContextTokenBudget,
		)
	}
}

func TestWorkPatternProfileSemanticContextDeduplicatesExactContent(
	t *testing.T,
) {
	candidates := []TaskContextCandidate{
		{
			Ref:             "memory:newer",
			Section:         TaskContextSectionKnowledge,
			SourceKind:      "memory_markdown",
			SourceID:        "newer",
			ObservedAt:      2_000,
			Content:         "Prefer source-backed decisions.",
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyInclude,
			Relevance:       0.9,
		},
		{
			Ref:             "knowledge:duplicate",
			Section:         TaskContextSectionKnowledge,
			SourceKind:      "knowledge_markdown",
			SourceID:        "duplicate",
			ObservedAt:      1_000,
			Content:         "  prefer   source-backed decisions. ",
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyInclude,
			Relevance:       0.8,
		},
	}
	signals := selectWorkPatternSemanticSignals(
		TaskContextSourceResult{Candidates: candidates},
		3_000,
	)
	if len(signals) != 1 || signals[0].Ref != "memory:newer" {
		t.Fatalf("deduplicated semantic signals=%+v", signals)
	}
	manifest := workPatternProfileSelectionManifest(
		WorkPatternProfileSnapshot{SourceCutoffAt: 3_000},
		TaskContextSourceResult{Candidates: candidates},
		nil,
		signals,
		nil,
		nil,
	)
	foundDuplicate := false
	for _, item := range manifest {
		if item.Ref == "semantic:knowledge:duplicate" &&
			item.Reason == "duplicate_semantic_content" &&
			!item.Included {
			foundDuplicate = true
		}
	}
	if !foundDuplicate {
		t.Fatalf("duplicate omission missing from manifest: %+v", manifest)
	}
}

func TestWorkPatternProfileSemanticContextExcludesVisibleTaskSource(
	t *testing.T,
) {
	candidates := []TaskContextCandidate{
		{
			Ref:             "memory:user-preference",
			Section:         TaskContextSectionKnowledge,
			SourceKind:      "memory_markdown",
			SourceID:        "preference",
			ObservedAt:      1_000,
			Content:         "The user prefers concise proposals.",
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyInclude,
			Relevance:       0.9,
		},
		{
			Ref:             "activity-snapshot:source-email",
			Section:         TaskContextSectionActivity,
			SourceKind:      taskContextSourceActivitySnapshotKind,
			SourceID:        "session:42",
			ObservedAt:      1_100,
			Content:         "A sender asked for a detailed ten-page proposal.",
			EvidenceClass:   TaskContextEvidenceObservedContext,
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance:       1,
		},
	}
	signals := selectWorkPatternSemanticSignals(
		TaskContextSourceResult{Candidates: candidates},
		2_000,
	)
	if len(signals) != 1 || signals[0].Ref != "memory:user-preference" {
		t.Fatalf("visible source contaminated work profile signals: %+v", signals)
	}
}

func TestWorkPatternProfileContextSelectsOneModePerObservedAppPair(
	t *testing.T,
) {
	projection := func(
		key string,
		apps []string,
		confidence float64,
	) WorkPatternProjection {
		return WorkPatternProjection{
			ScopeKind:    WorkPatternScopeWorkMode,
			ScopeKey:     key,
			EvidenceKind: WorkPatternEvidenceObserved,
			Status:       WorkPatternProfileStatusQualified,
			Confidence:   confidence,
			SampleCount:  20,
			SessionCount: 5,
			Apps:         apps,
		}
	}
	selected, _ := selectWorkPatternProjections(
		WorkPatternProfileSnapshot{
			Projections: []WorkPatternProjection{
				projection(
					"editor+safari",
					[]string{"editor", "safari"},
					0.8,
				),
				projection(
					"chatgpt+editor+safari",
					[]string{"chatgpt", "editor", "safari"},
					0.9,
				),
				projection(
					"editor+safari+yansu",
					[]string{"editor", "safari", "yansu"},
					0.85,
				),
			},
		},
		[]string{"editor", "safari"},
		nil,
		nil,
	)
	modeCount := 0
	for _, item := range selected {
		if item.ScopeKind == WorkPatternScopeWorkMode {
			modeCount++
			if item.ScopeKey != "editor+safari" {
				t.Fatalf("selected redundant superset: %+v", selected)
			}
		}
	}
	if modeCount != 1 {
		t.Fatalf("selected modes=%d, want 1: %+v", modeCount, selected)
	}
}

func TestWorkPatternProfileSelectionManifestOmitsHiddenAndFutureEvidence(
	t *testing.T,
) {
	store := newTestStore(t)
	snapshot := qualifiedWorkPatternFixture(t, store)
	sources := TaskContextSourceResult{Candidates: []TaskContextCandidate{
		{
			Ref:             "shadow:private",
			Section:         TaskContextSectionKnowledge,
			SourceKind:      "shadow_run",
			SourceID:        "private",
			ObservedAt:      snapshot.SourceCutoffAt,
			Content:         "hidden shadow claim",
			EvidenceClass:   TaskContextEvidenceHiddenShadow,
			PrivacyDecision: TaskContextPrivacyInclude,
			Relevance:       1,
		},
		{
			Ref:             "memory:future",
			Section:         TaskContextSectionKnowledge,
			SourceKind:      "memory_markdown",
			SourceID:        "future",
			ObservedAt:      snapshot.SourceCutoffAt + 1,
			Content:         "future user evidence",
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyInclude,
			Relevance:       1,
		},
	}}
	profile, err := buildWorkPatternProfileContext(
		store,
		snapshot,
		sources,
		"review work",
		nil,
		nil,
		WorkPatternInfluenceProfile,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"shadow:private",
		"hidden shadow claim",
		"memory:future",
		"future user evidence",
	} {
		if strings.Contains(profile.CanonicalJSON, forbidden) {
			t.Fatalf(
				"excluded evidence %q entered frozen profile context",
				forbidden,
			)
		}
	}
}

func TestWorkPatternProfileSelectionManifestRecordsTokenTruncation(
	t *testing.T,
) {
	source := TaskContextCandidate{
		Ref:        "memory:large",
		Section:    TaskContextSectionKnowledge,
		SourceKind: "memory_markdown",
		SourceID:   "large",
		ObservedAt: 10,
		Content: strings.Repeat(
			"context ",
			workPatternSemanticContextTokenBudget,
		),
		EvidenceClass:   TaskContextEvidenceExplicitUser,
		PrivacyDecision: TaskContextPrivacyInclude,
		Relevance:       1,
	}
	sources := TaskContextSourceResult{
		Candidates: []TaskContextCandidate{source},
	}
	signals := selectWorkPatternSemanticSignals(sources, 10)
	if len(signals) != 1 || !signals[0].Truncated {
		t.Fatalf("truncated semantic signals=%+v", signals)
	}
	manifest := workPatternProfileSelectionManifest(
		WorkPatternProfileSnapshot{SourceCutoffAt: 10},
		sources,
		nil,
		signals,
		nil,
		nil,
	)
	if len(manifest) != 1 ||
		!manifest[0].Included ||
		!manifest[0].Truncated ||
		manifest[0].Reason != "selected_and_truncated_to_token_budget" {
		t.Fatalf("truncation manifest=%+v", manifest)
	}
}

func TestWorkPatternProfileCorrectionSuppressesOnlyFutureSelection(
	t *testing.T,
) {
	store := newTestStore(t)
	first := qualifiedWorkPatternFixture(t, store)
	global := projectionByScope(
		t,
		first,
		WorkPatternScopeGlobal,
		"all",
	)
	statement := workPatternProjectionStatement(global)
	correction, added, err := store.RecordWorkPatternProfileCorrection(
		RecordWorkPatternProfileCorrectionInput{
			IdempotencyKey:    "correct-global-pattern",
			ProfileSnapshotID: first.ID,
			ScopeKind:         global.ScopeKind,
			ScopeKey:          global.ScopeKey,
			OriginalStatement: statement,
			Correction:        "This pattern applies only while debugging.",
			CreatedAt:         first.SourceCutoffAt + 100,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !added || correction.Revision != 1 {
		t.Fatalf("correction=%+v added=%v", correction, added)
	}
	olderContext, err := buildWorkPatternProfileContext(
		store,
		first,
		TaskContextSourceResult{},
		"",
		nil,
		nil,
		WorkPatternInfluenceProfile,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(olderContext.SelectedProjections) == 0 {
		t.Fatal("correction leaked backward into an older profile cutoff")
	}

	next, _, err := store.EnsureWorkPatternProfileSnapshot(
		first.SourceCutoffAt+1_000,
		first.SourceCutoffAt+1_001,
	)
	if err != nil {
		t.Fatal(err)
	}
	futureContext, err := buildWorkPatternProfileContext(
		store,
		next,
		TaskContextSourceResult{},
		"",
		nil,
		nil,
		WorkPatternInfluenceProfile,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, projection := range futureContext.SelectedProjections {
		if projection.ScopeKind == global.ScopeKind &&
			projection.ScopeKey == global.ScopeKey {
			t.Fatal("corrected projection remained selectable")
		}
	}
	if len(futureContext.Contradictions) != 1 ||
		futureContext.Contradictions[0].ID != correction.ID {
		t.Fatalf(
			"future contradictions=%+v, want correction %s",
			futureContext.Contradictions,
			correction.ID,
		)
	}
	if _, err := store.db.Exec(`
		UPDATE work_pattern_profile_corrections_v1
		SET correction = 'rewritten'
		WHERE id = ?`,
		correction.ID,
	); err == nil {
		t.Fatal("correction update unexpectedly succeeded")
	}
}

func TestWorkPatternProfileCorrectionRejectsStaleStatement(t *testing.T) {
	store := newTestStore(t)
	snapshot := qualifiedWorkPatternFixture(t, store)
	global := projectionByScope(
		t,
		snapshot,
		WorkPatternScopeGlobal,
		"all",
	)
	_, _, err := store.RecordWorkPatternProfileCorrection(
		RecordWorkPatternProfileCorrectionInput{
			IdempotencyKey:    "stale-work-pattern",
			ProfileSnapshotID: snapshot.ID,
			ScopeKind:         global.ScopeKind,
			ScopeKey:          global.ScopeKey,
			OriginalStatement: "An outdated statement.",
			Correction:        "This applies only to a specific workflow.",
			CreatedAt:         snapshot.SourceCutoffAt + 1,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "stale statement") {
		t.Fatalf("stale correction error=%v", err)
	}
	corrections, listErr := store.ListWorkPatternProfileCorrections(
		snapshot.SourceCutoffAt + 10,
	)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(corrections) != 0 {
		t.Fatalf("stale correction was persisted: %+v", corrections)
	}
}

func TestRecordWorkPatternCorrectionAppendsANewProfileRevision(
	t *testing.T,
) {
	store := newTestStore(t)
	first := qualifiedWorkPatternFixture(t, store)
	global := projectionByScope(
		t,
		first,
		WorkPatternScopeGlobal,
		"all",
	)
	app := &App{activity: &activityMonitorState{store: store}}
	correction, err := app.RecordWorkPatternProfileCorrection(
		RecordWorkPatternProfileCorrectionInput{
			IdempotencyKey:    "append-profile-revision",
			ProfileSnapshotID: first.ID,
			ScopeKind:         global.ScopeKind,
			ScopeKey:          global.ScopeKey,
			OriginalStatement: workPatternProjectionStatement(global),
			Correction:        "This pattern applies only during release review.",
			CreatedAt:         first.SourceCutoffAt + 100,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if correction.ID == "" || correction.Revision != 1 {
		t.Fatalf("correction=%+v", correction)
	}
	snapshots, err := store.ListWorkPatternProfileSnapshots(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 2 {
		t.Fatalf("profile revisions=%d, want at least 2", len(snapshots))
	}
	latest := snapshots[0]
	if latest.ID == first.ID ||
		latest.SourceCutoffAt < correction.CreatedAt {
		t.Fatalf("latest profile did not include correction: %+v", latest)
	}
	context, err := buildWorkPatternProfileContext(
		store,
		latest,
		TaskContextSourceResult{},
		"",
		nil,
		nil,
		WorkPatternInfluenceProfile,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, projection := range context.SelectedProjections {
		if projection.ScopeKind == global.ScopeKind &&
			projection.ScopeKey == global.ScopeKey {
			t.Fatal("corrected projection survived in the appended revision")
		}
	}
	if len(context.Contradictions) == 0 ||
		context.Contradictions[0].ID != correction.ID {
		t.Fatalf("contradictions=%+v", context.Contradictions)
	}
	selfEntries, err := store.ListCurrentSelfModelEntries()
	if err != nil {
		t.Fatal(err)
	}
	foundCorrection := false
	for _, entry := range selfEntries {
		if entry.Statement == correction.Correction &&
			entry.ReasoningStatus == "explicit_user_correction" &&
			entry.Scope == "how_you_work" {
			foundCorrection = true
		}
	}
	if !foundCorrection {
		t.Fatalf(
			"work pattern correction did not become Self: %+v",
			selfEntries,
		)
	}
	var authorityCount int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM authority_decisions`,
	).Scan(&authorityCount); err != nil {
		t.Fatal(err)
	}
	if authorityCount != 0 {
		t.Fatalf("correction created %d authority decisions", authorityCount)
	}
}
