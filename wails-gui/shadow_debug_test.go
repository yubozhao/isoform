package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yansu-agent/agent/authority"
)

func TestShadowCorrelationAttemptRoundTripAndImmutability(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UnixMilli()
	attempt, err := store.AddShadowCorrelationAttempt(ShadowCorrelationAttempt{
		ID:                "shadow-attempt-1",
		SourceKind:        "activity",
		SourceID:          "activity-source-1",
		ObservedAt:        now - 1_000,
		ObservedSummary:   "The user opened the customer thread.",
		Status:            "completed",
		Reason:            "comparison_completed",
		ReportedMatches:   1,
		PersistedRunIDs:   []string{"shadow-run-1"},
		ComparatorPrompt:  "exact comparator prompt",
		ComparatorVersion: shadowCorrelationPromptVersion,
		EvaluatorModel:    "gpt-5.6-terra",
		EvaluatorRaw:      `{"matches":[{"shadowRunId":"shadow-run-1"}]}`,
		Candidates: []ShadowCorrelationCandidateAudit{
			{
				ShadowRunID:   "shadow-run-1",
				CapabilityKey: "customer.follow_up.draft",
				Relevance:     4,
				PayloadBytes:  1_200,
				Selected:      true,
				Reason:        "selected",
			},
			{
				ShadowRunID:   "shadow-run-2",
				CapabilityKey: "calendar.meeting.schedule",
				Relevance:     0,
				PayloadBytes:  49_500,
				Reason:        "context_budget",
			},
		},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempt.CandidateCount != 2 || attempt.SelectedCount != 1 {
		t.Fatalf("derived candidate counts=%+v", attempt)
	}
	if attempt.ComparatorHash != sha256Hex([]byte(attempt.ComparatorPrompt)) {
		t.Fatalf("comparator hash=%q", attempt.ComparatorHash)
	}

	items, err := store.ListShadowCorrelationAttempts(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != attempt.ID ||
		len(items[0].Candidates) != 2 ||
		items[0].PersistedRunIDs[0] != "shadow-run-1" {
		t.Fatalf("attempt round trip=%+v", items)
	}
	if _, err := store.db.Exec(
		`UPDATE shadow_correlation_attempts SET status = 'error' WHERE id = ?`,
		attempt.ID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("attempt update error=%v", err)
	}
}

func TestShadowAgentDebugSnapshotAndRunDetail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	run, _, err := store.CreateShadowRun(newShadowRunFixture("shadow-debug-run", "shadow-debug-decision"))
	if err != nil {
		t.Fatal(err)
	}
	observation, _, err := store.AddShadowRunObservation(ShadowRunObservation{
		ShadowRunID:    run.ID,
		SourceKind:     "activity",
		SourceID:       "shadow-debug-observation",
		ObservedAction: "Drafted the customer follow-up.",
		ObservedResult: "The draft was saved.",
		ObservedAt:     run.CreatedAt + 1_000,
		CreatedAt:      run.CreatedAt + 1_100,
	})
	if err != nil {
		t.Fatal(err)
	}
	score := 1.0
	_, _, err = store.AddShadowRunEvaluation(ShadowRunEvaluation{
		ShadowRunID:       run.ID,
		ObservationID:     observation.ID,
		IdempotencyKey:    "shadow-debug-evaluation",
		ComparisonVersion: shadowCorrelationPromptVersion,
		ComparatorPrompt:  "debug comparator prompt",
		Action:            ShadowEvaluationDimension{Assessment: "matched", Score: &score, Confidence: 0.9},
		Outcome:           ShadowEvaluationDimension{Assessment: "matched", Score: &score, Confidence: 0.8},
		Timing:            ShadowEvaluationDimension{Assessment: "unknown"},
		Reasoning:         ShadowEvaluationDimension{Assessment: "unknown"},
		Safety:            ShadowEvaluationDimension{Assessment: "unknown"},
		CreatedAt:         run.CreatedAt + 1_200,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AddShadowCorrelationAttempt(ShadowCorrelationAttempt{
		SourceKind:        "activity",
		SourceID:          observation.SourceID,
		ObservedAt:        observation.ObservedAt,
		Status:            "completed",
		PersistedRunIDs:   []string{run.ID},
		ComparatorVersion: shadowCorrelationPromptVersion,
		CreatedAt:         time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, added, err := store.RebuildShadowCapabilityProfile(
		run.CapabilityKey,
		run.CreatedAt+1_300,
	)
	if err != nil || !added {
		t.Fatalf("debug profile: added=%v err=%v", added, err)
	}
	_, _, err = store.AddShadowExecutionAttempt(ShadowExecutionAttempt{
		ShadowRunID:    run.ID,
		IdempotencyKey: run.ID + ":debug-runtime",
		RuntimeVersion: shadowExecutionRuntimeVersion,
		Status:         "completed",
		PolicyVersion:  authority.ShadowPolicyVersion,
		Prompt:         "Draft the customer follow-up.",
		Plan:           *shadowRunWithCandidatePlan("unused", "unused").Prediction.CandidateExecutionPlan,
		Decisions:      []authority.Evaluation{},
		StartedAt:      run.CreatedAt + 1_250,
		CompletedAt:    run.CreatedAt + 1_260,
		CreatedAt:      run.CreatedAt + 1_260,
	})
	if err != nil {
		t.Fatal(err)
	}

	memDir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(memDir, "shadow-run-learning-"+run.ID+".md"),
		[]byte("- Observed action: Drafted the customer follow-up.\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.GetShadowAgentDebugSnapshot(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Runs) != 1 || snapshot.Runs[0].Status != "evaluated" ||
		snapshot.Runs[0].ObservationCount != 1 ||
		snapshot.Runs[0].EvaluationCount != 1 {
		t.Fatalf("debug run summaries=%+v", snapshot.Runs)
	}
	if len(snapshot.Attempts) != 1 || snapshot.Attempts[0].PersistedRunIDs[0] != run.ID {
		t.Fatalf("debug attempts=%+v", snapshot.Attempts)
	}
	if len(snapshot.Profiles) != 1 ||
		snapshot.Profiles[0].ID != profile.ID ||
		snapshot.Profiles[0].Readiness.Status != "collecting" {
		t.Fatalf("debug profiles=%+v", snapshot.Profiles)
	}

	detail, err := app.GetShadowAgentDebugRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Detail.Run.ID != run.ID ||
		len(detail.Detail.Observations) != 1 ||
		len(detail.Detail.Evaluations) != 1 ||
		len(detail.ExecutionAttempts) != 1 {
		t.Fatalf("debug detail=%+v", detail)
	}
	if !detail.MemoryProjection.Exists ||
		!strings.Contains(detail.MemoryProjection.Content, "Drafted the customer follow-up.") {
		t.Fatalf("memory projection=%+v", detail.MemoryProjection)
	}
	if _, err := os.Stat(detail.MemoryProjection.Path); err != nil {
		t.Fatal(err)
	}
}

func TestShadowRunDebugSummariesPreserveLifecycleStatus(t *testing.T) {
	store := newTestStore(t)
	const now = int64(2_000_000)

	runs := []struct {
		id         string
		horizonEnd int64
	}{
		{id: "shadow-summary-awaiting", horizonEnd: now + 1_000},
		{id: "shadow-summary-expired", horizonEnd: now - 1},
		{id: "shadow-summary-observed", horizonEnd: now + 1_000},
		{id: "shadow-summary-evaluated", horizonEnd: now + 1_000},
	}
	for _, item := range runs {
		run := newShadowRunFixture(item.id, item.id+"-decision")
		run.ObservationHorizonEnd = item.horizonEnd
		if _, added, err := store.CreateShadowRun(run); err != nil || !added {
			t.Fatalf("create %s: added=%v err=%v", item.id, added, err)
		}
	}

	for _, runID := range []string{
		"shadow-summary-observed",
		"shadow-summary-evaluated",
	} {
		observation, added, err := store.AddShadowRunObservation(
			ShadowRunObservation{
				ShadowRunID:    runID,
				SourceKind:     "activity",
				SourceID:       runID + "-observation",
				ObservedAction: "Drafted the customer follow-up.",
				ObservedResult: "The draft was saved.",
				ObservedAt:     now - 100,
				CreatedAt:      now - 90,
			},
		)
		if err != nil || !added {
			t.Fatalf("observe %s: added=%v err=%v", runID, added, err)
		}
		if runID != "shadow-summary-evaluated" {
			continue
		}
		score := 1.0
		if _, added, err := store.AddShadowRunEvaluation(
			ShadowRunEvaluation{
				ShadowRunID:       runID,
				ObservationID:     observation.ID,
				IdempotencyKey:    runID + "-evaluation",
				ComparisonVersion: shadowCorrelationPromptVersion,
				ComparatorPrompt:  "summary comparator prompt",
				Action: ShadowEvaluationDimension{
					Assessment: "matched",
					Score:      &score,
					Confidence: 0.9,
				},
				Outcome: ShadowEvaluationDimension{
					Assessment: "matched",
					Score:      &score,
					Confidence: 0.9,
				},
				Timing:    ShadowEvaluationDimension{Assessment: "unknown"},
				Reasoning: ShadowEvaluationDimension{Assessment: "unknown"},
				Safety:    ShadowEvaluationDimension{Assessment: "unknown"},
				CreatedAt: now - 80,
			},
		); err != nil || !added {
			t.Fatalf("evaluate %s: added=%v err=%v", runID, added, err)
		}
	}

	summaries, err := store.ListShadowRunDebugSummaries(20, now)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]ShadowRunDebugSummary, len(summaries))
	for _, summary := range summaries {
		byID[summary.ID] = summary
	}
	wantStatus := map[string]string{
		"shadow-summary-awaiting":  "awaiting_observation",
		"shadow-summary-expired":   "expired",
		"shadow-summary-observed":  "observed",
		"shadow-summary-evaluated": "evaluated",
	}
	for runID, status := range wantStatus {
		summary, ok := byID[runID]
		if !ok || summary.Status != status {
			t.Fatalf("summary %s=%+v, want status %s", runID, summary, status)
		}
		if summary.PredictedAction == "" || summary.ExpectedResult == "" {
			t.Fatalf("summary %s lost prediction fields: %+v", runID, summary)
		}
	}
	if byID["shadow-summary-observed"].ObservationCount != 1 ||
		byID["shadow-summary-observed"].EvaluationCount != 0 ||
		byID["shadow-summary-evaluated"].ObservationCount != 1 ||
		byID["shadow-summary-evaluated"].EvaluationCount != 1 {
		t.Fatalf("summary counts=%+v", byID)
	}
}

func TestShadowAgentDebugBrowserRPCMethodsAreAllowlisted(t *testing.T) {
	for _, method := range []string{
		"GetShadowAgentDebugSnapshot",
		"GetShadowAgentDebugRun",
	} {
		if !browserRPCAllowlist[method] {
			t.Errorf("%s must be browser RPC allowlisted", method)
		}
	}
}
