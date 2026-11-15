package main

import (
	"strings"
	"testing"
)

func shadowScore(value float64) *float64 {
	return &value
}

func newShadowRunFixture(id, decisionKey string) ShadowRun {
	const createdAt = int64(1_000_000)
	return ShadowRun{
		ID:            id,
		DecisionKey:   decisionKey,
		CapabilityKey: "customer.follow_up.draft",
		SourceKind:    "activity",
		SourceID:      "activity-decision-1",
		Context: ShadowContextSnapshot{
			CapturedAt:           createdAt,
			ProductionEventAt:    createdAt - 100,
			PromptContext:        "The customer has not replied for five business days. The user reopened the thread.",
			SelectionBudgetChars: 48_000,
			References: []ShadowEvidenceReference{
				{
					Role:       "activity",
					SourceKind: "activity",
					SourceID:   "activity-decision-1",
					Content:    "The user reopened the customer thread.",
					Summary:    "Customer thread reopened",
					ObservedAt: createdAt - 100,
				},
				{
					Role:       "commitment",
					SourceKind: "memory",
					SourceID:   "commitment-1",
					Content:    "Follow up if the customer does not reply after five business days.",
					Summary:    "Follow-up timing commitment",
					ObservedAt: createdAt - 10_000,
				},
			},
		},
		Prediction: ShadowPrediction{
			Goal:            "Move the customer commitment forward",
			PredictedAction: "Draft a concise follow-up to the customer.",
			ExpectedResult:  "A follow-up draft is ready for review.",
			ExpectedTiming:  "Within the current work session",
			ReasoningHypotheses: []ShadowReasoningHypothesis{
				{
					Statement:  "The user follows up when a promised response is five business days late.",
					Confidence: 0.72,
					Basis:      "repeated_choice",
					EvidenceRefs: []string{
						"commitment-1",
					},
				},
				{
					Statement:         "The user may only be checking for new information before deciding.",
					Confidence:        0.31,
					Basis:             "model_inference",
					ContradictionRefs: []string{"activity-decision-1"},
				},
			},
			RequiredCapabilities: []string{"email.draft.create"},
			PlannedEffects: []ShadowPlannedEffect{{
				Capability: "email.draft.create",
				Effect:     "local_write",
				Resource:   "customer-thread",
			}},
			GoalConfidence:   0.78,
			ActionConfidence: 0.81,
			ResultConfidence: 0.74,
			TimingConfidence: 0.62,
		},
		Model:                   "gpt-5.6-sol",
		PromptVersion:           "shadow-run-v1",
		RunnerVersion:           "shadow-ledger-v1",
		ObservationHorizonStart: createdAt,
		ObservationHorizonEnd:   createdAt + 86_400_000,
		CreatedAt:               createdAt,
	}
}

func TestShadowRunRoundTripPreservesImmutableContextPredictionAndEvidence(t *testing.T) {
	store := newTestStore(t)
	input := newShadowRunFixture("shadow-run-1", "decision-1")

	created, added, err := store.CreateShadowRun(input)
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("first shadow run insert must be new")
	}
	if len(created.ContextHash) != 64 || len(created.PredictionHash) != 64 {
		t.Fatalf("missing canonical payload hashes: context=%q prediction=%q", created.ContextHash, created.PredictionHash)
	}

	got, err := store.GetShadowRun(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Context.PromptContext != input.Context.PromptContext {
		t.Fatalf("prompt context changed: %q", got.Context.PromptContext)
	}
	if len(got.Prediction.ReasoningHypotheses) != 2 {
		t.Fatalf("reasoning hypotheses=%+v", got.Prediction.ReasoningHypotheses)
	}
	evidence, err := store.ListShadowRunEvidence(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 || evidence[0].ContentHash == "" || evidence[1].ContentHash == "" {
		t.Fatalf("shadow evidence=%+v", evidence)
	}

	if _, err := store.db.Exec(
		`UPDATE shadow_runs SET prediction_json = '{}' WHERE id = ?`,
		created.ID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("direct shadow run mutation error=%v, want immutable rejection", err)
	}
}

func TestCreateShadowRunFromTriageCapturesProductionContextWithoutInitiative(t *testing.T) {
	store := newTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	const (
		sourceID          = "activity-shadow-source"
		prompt            = "exact production prompt with memory and knowledge context"
		summary           = "The user reopened a customer thread after five business days."
		productionEventAt = int64(2_000_000)
		createdAt         = int64(2_001_000)
	)
	response := &activityTriageResponse{
		WorthInitiative:          true,
		InitiativeTitle:          "Draft customer follow-up",
		InitiativeGoal:           "Move the customer commitment forward",
		PredictedUserAction:      "Draft a concise follow-up.",
		InitiativeRationale:      "The thread was reopened after the stated waiting period.",
		InitiativeConfidence:     0.81,
		ShadowCapabilityKey:      "Customer / Follow Up / Draft",
		ShadowExpectedResult:     "A follow-up draft exists.",
		ShadowExpectedTiming:     "Within the current work session",
		ShadowGoalConfidence:     0.78,
		ShadowResultConfidence:   0.74,
		ShadowTimingConfidence:   0.62,
		WorthAutomation:          true,
		AutomationName:           "Draft the customer follow-up",
		AutomationDescription:    "Prepare the same draft Yansu predicts the user will write.",
		AutomationPrompt:         "Draft a concise follow-up using the customer thread.",
		AutomationScheduleKind:   "manual",
		AutomationScheduleConfig: []byte(`{}`),
		AutomationRationale:      "This is a bounded draft with no external send.",
		AutomationConfidence:     0.70,
		// Reasoning is intentionally unknown. The helper must not turn the
		// evidence rationale into a claim about why the user acted.
		ShadowReasoningHypotheses: nil,
	}

	run, added, err := app.createShadowRunFromTriage(
		response,
		"activity",
		sourceID,
		prompt,
		summary,
		"gpt-5.6-terra",
		productionEventAt,
		createdAt,
	)
	if err != nil || !added {
		t.Fatalf("create shadow run: added=%v err=%v", added, err)
	}
	if run.Context.PromptContext != prompt || run.Context.ProductionEventAt != productionEventAt {
		t.Fatalf("production context changed: %+v", run.Context)
	}
	if run.CapabilityKey != "customer.follow.up.draft" {
		t.Fatalf("capability key=%q", run.CapabilityKey)
	}
	if run.Prediction.EvidenceRationale != response.InitiativeRationale {
		t.Fatalf("evidence rationale=%q", run.Prediction.EvidenceRationale)
	}
	if len(run.Prediction.ReasoningHypotheses) != 0 {
		t.Fatalf("unknown reasoning was invented: %+v", run.Prediction.ReasoningHypotheses)
	}
	if run.Prediction.CandidateExecutionPlan == nil ||
		run.Prediction.CandidateExecutionPlan.Prompt != response.AutomationPrompt {
		t.Fatalf("candidate plan was not frozen into shadow output: %+v", run.Prediction.CandidateExecutionPlan)
	}
	if run.ObservationHorizonEnd-run.ObservationHorizonStart != shadowObservationWindow.Milliseconds() {
		t.Fatalf("observation horizon=%d", run.ObservationHorizonEnd-run.ObservationHorizonStart)
	}

	var initiativeCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM initiatives`).Scan(&initiativeCount); err != nil {
		t.Fatal(err)
	}
	if initiativeCount != 0 {
		t.Fatalf("shadow triage created %d initiative rows", initiativeCount)
	}

	replayed, added, err := app.createShadowRunFromTriage(
		response,
		"activity",
		sourceID,
		prompt,
		summary,
		"gpt-5.6-terra",
		productionEventAt,
		createdAt,
	)
	if err != nil || added || replayed.ID != run.ID {
		t.Fatalf("idempotent replay: run=%+v added=%v err=%v", replayed, added, err)
	}
}

func TestShadowRunCreateIsIdempotentAndRejectsConflictingReplay(t *testing.T) {
	store := newTestStore(t)
	input := newShadowRunFixture("shadow-run-idempotent", "decision-idempotent")
	first, added, err := store.CreateShadowRun(input)
	if err != nil || !added {
		t.Fatalf("first create: added=%v err=%v", added, err)
	}
	replayed, added, err := store.CreateShadowRun(input)
	if err != nil || added {
		t.Fatalf("replay: added=%v err=%v", added, err)
	}
	if replayed.ID != first.ID || replayed.PredictionHash != first.PredictionHash {
		t.Fatalf("replay changed immutable run: first=%+v replay=%+v", first, replayed)
	}

	conflict := input
	conflict.ID = "shadow-run-conflict"
	conflict.Prediction.PredictedAction = "Wait another week before following up."
	if _, _, err := store.CreateShadowRun(conflict); err == nil || !strings.Contains(err.Error(), "different immutable payload") {
		t.Fatalf("conflicting replay error=%v", err)
	}
	unchanged, err := store.GetShadowRun(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Prediction.PredictedAction != input.Prediction.PredictedAction {
		t.Fatalf("conflict changed prediction: %+v", unchanged.Prediction)
	}
}

func TestShadowRunObservationIsAppendOnlyAndReasoningEvidenceIsExplicit(t *testing.T) {
	store := newTestStore(t)
	run, _, err := store.CreateShadowRun(newShadowRunFixture("shadow-run-observed", "decision-observed"))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListPendingShadowRuns(run.ObservationHorizonStart, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != run.ID {
		t.Fatalf("pending runs=%+v", pending)
	}

	input := ShadowRunObservation{
		ID:             "shadow-observation-1",
		ShadowRunID:    run.ID,
		SourceKind:     "activity",
		SourceID:       "activity-outcome-1",
		ObservedAction: "Drafted a customer follow-up.",
		ObservedResult: "The draft was saved and later sent.",
		ReasoningEvidence: []ShadowReasoningEvidence{{
			Kind:       "stated_rationale",
			Statement:  "I said I would follow up after five business days.",
			Confidence: 0.96,
			SourceRefs: []string{"activity-outcome-1"},
		}},
		Evidence: []ShadowEvidenceReference{{
			Role:       "outcome",
			SourceKind: "activity",
			SourceID:   "activity-outcome-1",
			Content:    "Drafted and sent the customer follow-up.",
		}},
		ObservedAt: run.CreatedAt + 5_000,
		CreatedAt:  run.CreatedAt + 5_100,
	}
	created, added, err := store.AddShadowRunObservation(input)
	if err != nil || !added {
		t.Fatalf("add observation: added=%v err=%v", added, err)
	}
	replayed, added, err := store.AddShadowRunObservation(input)
	if err != nil || added || replayed.ID != created.ID {
		t.Fatalf("observation replay: item=%+v added=%v err=%v", replayed, added, err)
	}

	conflict := input
	conflict.ID = "shadow-observation-conflict"
	conflict.ObservedAction = "Waited without drafting a follow-up."
	if _, _, err := store.AddShadowRunObservation(conflict); err == nil || !strings.Contains(err.Error(), "different immutable payload") {
		t.Fatalf("conflicting observation error=%v", err)
	}
	pending, err = store.ListPendingShadowRuns(run.ObservationHorizonStart+10_000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("observed run remained pending: %+v", pending)
	}
	detail, err := store.GetShadowRunDetail(run.ID, run.ObservationHorizonStart+10_000)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != "observed" || len(detail.Observations) != 1 {
		t.Fatalf("observed detail=%+v", detail)
	}
}

func TestShadowRunEvaluationKeepsUnknownReasoningOutOfFailureScore(t *testing.T) {
	store := newTestStore(t)
	run, _, err := store.CreateShadowRun(newShadowRunFixture("shadow-run-evaluated", "decision-evaluated"))
	if err != nil {
		t.Fatal(err)
	}
	observation, _, err := store.AddShadowRunObservation(ShadowRunObservation{
		ID:             "shadow-observation-evaluated",
		ShadowRunID:    run.ID,
		SourceKind:     "activity",
		SourceID:       "activity-outcome-evaluated",
		ObservedAction: "Drafted a customer follow-up.",
		ObservedResult: "The customer replied.",
		ObservedAt:     run.CreatedAt + 5_000,
		CreatedAt:      run.CreatedAt + 5_100,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := ShadowRunEvaluation{
		ID:                "shadow-evaluation-1",
		ShadowRunID:       run.ID,
		ObservationID:     observation.ID,
		IdempotencyKey:    "shadow-evaluation-key-1", // gitleaks:allow
		ComparisonVersion: "shadow-comparator-v1",
		EvaluatorModel:    "gpt-5.6-terra",
		ComparatorPrompt:  "exact frozen comparator prompt",
		Action: ShadowEvaluationDimension{
			Assessment: "matched",
			Score:      shadowScore(0.96),
			Confidence: 0.94,
		},
		Outcome: ShadowEvaluationDimension{
			Assessment: "partial",
			Score:      shadowScore(0.72),
			Confidence: 0.81,
		},
		Timing: ShadowEvaluationDimension{
			Assessment: "acceptable_alternative",
			Score:      shadowScore(0.84),
			Confidence: 0.76,
		},
		Reasoning: ShadowEvaluationDimension{
			Assessment: "unknown",
			Confidence: 0.20,
			Reason:     "The behavior did not reveal why the user followed up.",
		},
		Safety: ShadowEvaluationDimension{
			Assessment: "safe",
			Score:      shadowScore(1),
			Confidence: 0.99,
		},
		Summary:   "Action matched; reasoning remains unknown.",
		CreatedAt: run.CreatedAt + 6_000,
	}
	created, added, err := store.AddShadowRunEvaluation(input)
	if err != nil || !added {
		t.Fatalf("add evaluation: added=%v err=%v", added, err)
	}
	if created.Reasoning.Score != nil {
		t.Fatalf("unknown reasoning received a failure score: %+v", created.Reasoning)
	}
	if created.ComparatorHash != sha256Hex([]byte(input.ComparatorPrompt)) {
		t.Fatalf("comparator prompt hash=%q", created.ComparatorHash)
	}
	replayed, added, err := store.AddShadowRunEvaluation(input)
	if err != nil || added || replayed.ID != created.ID {
		t.Fatalf("evaluation replay: item=%+v added=%v err=%v", replayed, added, err)
	}

	conflict := input
	conflict.ID = "shadow-evaluation-conflict"
	conflict.Summary = "Conflicting evaluator output."
	if _, _, err := store.AddShadowRunEvaluation(conflict); err == nil || !strings.Contains(err.Error(), "different immutable payload") {
		t.Fatalf("conflicting evaluation error=%v", err)
	}
	detail, err := store.GetShadowRunDetail(run.ID, run.CreatedAt+7_000)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != "evaluated" || len(detail.Evaluations) != 1 {
		t.Fatalf("evaluated detail=%+v", detail)
	}

	invalid := input
	invalid.ID = "shadow-evaluation-invalid"
	invalid.IdempotencyKey = "shadow-evaluation-invalid-key"
	invalid.Reasoning.Score = shadowScore(0)
	if _, _, err := store.AddShadowRunEvaluation(invalid); err == nil || !strings.Contains(err.Error(), "cannot have a score") {
		t.Fatalf("unknown reasoning score error=%v", err)
	}

	hashMismatch := input
	hashMismatch.ID = "shadow-evaluation-hash-mismatch"
	hashMismatch.IdempotencyKey = "shadow-evaluation-hash-mismatch-key"
	hashMismatch.ComparatorHash = "incorrect"
	if _, _, err := store.AddShadowRunEvaluation(hashMismatch); err == nil || !strings.Contains(err.Error(), "hash does not match") {
		t.Fatalf("comparator hash mismatch error=%v", err)
	}
}

func TestShadowRunDetailMarksUnobservedExpiredRunWithoutInventingOutcome(t *testing.T) {
	store := newTestStore(t)
	run, _, err := store.CreateShadowRun(newShadowRunFixture("shadow-run-expired", "decision-expired"))
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.GetShadowRunDetail(run.ID, run.ObservationHorizonEnd+1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != "expired" || len(detail.Observations) != 0 || len(detail.Evaluations) != 0 {
		t.Fatalf("expired detail invented evidence: %+v", detail)
	}
}
