package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"yansu-agent/agent/authority"
)

const (
	taskShadowDebugContextSecret    = "debug-private-frozen-context"
	taskShadowDebugEvidenceSecret   = "debug-private-evidence-payload"
	taskShadowDebugExecutionSecret  = "debug-private-execution-prompt"
	taskShadowDebugArtifactSecret   = "debug-private-artifact-content"
	taskShadowDebugComparatorSecret = "debug-private-comparator-prompt"
	taskShadowDebugEvaluatorSecret  = "debug-private-evaluator-raw"
	taskShadowDebugAttemptPrompt    = "exact-task-agent-debug-prompt"
	taskShadowDebugAttemptRaw       = "exact-task-agent-debug-raw"
	taskShadowDebugAttemptError     = "exact-task-agent-debug-error"
)

func newTaskShadowDebugTestStore(t *testing.T) *ActivityStore {
	t.Helper()
	store, err := NewActivityStore(t.TempDir())
	if err != nil {
		t.Fatalf("create task shadow debug store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task shadow debug store: %v", err)
		}
	})
	return store
}

func taskShadowDebugEpisodeInput(
	id string,
	startedAt int64,
	withPrivateData bool,
) CreateTaskEpisodeInput {
	onsetEvidenceID := id + "-onset-evidence"
	contextJSON := `{"project":"Yansu","surface":"Agency"}`
	payloadJSON := `{"kind":"window"}`
	if withPrivateData {
		contextJSON = `{"project":"Yansu","private":"` +
			taskShadowDebugContextSecret + `"}`
		payloadJSON = `{"private":"` + taskShadowDebugEvidenceSecret + `"}`
	}
	return CreateTaskEpisodeInput{
		ID:              id,
		InferredStartAt: startedAt,
		DetectedAt:      startedAt + 10,
		InitialStatus:   TaskEpisodeStatusCandidate,
		FrozenContext: TaskEpisodeFrozenContext{
			CutoffAt:      startedAt,
			CanonicalJSON: contextJSON,
			Manifest: []TaskContextManifestEntry{
				{
					Ref:             id + ":included",
					Section:         TaskContextSectionActivity,
					SourceKind:      "activity",
					SourceID:        id + "-activity",
					ObservedAt:      startedAt,
					EvidenceClass:   TaskContextEvidenceObservedUser,
					PrivacyDecision: TaskContextPrivacyRedact,
					Included:        true,
					ContentHash:     "included-hash",
					IncludedChars:   24,
				},
				{
					Ref:             id + ":hidden-shadow",
					Section:         TaskContextSectionKnowledge,
					SourceKind:      "shadow_learning",
					SourceID:        id + "-hidden",
					ObservedAt:      startedAt,
					EvidenceClass:   TaskContextEvidenceHiddenShadow,
					PrivacyDecision: TaskContextPrivacyExclude,
					Included:        false,
					Reason:          "hidden shadow evidence cannot enter task context",
				},
				{
					Ref:             id + ":protected",
					Section:         TaskContextSectionActivity,
					SourceKind:      "protected_app",
					SourceID:        id + "-protected",
					ObservedAt:      startedAt,
					EvidenceClass:   TaskContextEvidenceObservedUser,
					PrivacyDecision: TaskContextPrivacyExclude,
					Included:        false,
					Reason:          "protected source excluded",
				},
			},
		},
		InitialEvidence: []TaskEpisodeEvidence{
			{
				ID:          onsetEvidenceID,
				Role:        TaskEpisodeEvidenceOnset,
				SourceKind:  "activity_session",
				SourceID:    id + "-onset",
				Summary:     "The user began implementing the debugger.",
				PayloadJSON: payloadJSON,
				ObservedAt:  startedAt,
			},
		},
		InitialHypothesis: TaskEpisodeHypothesisRevision{
			TaskHypothesis:          "Implement the internal task shadow debugger",
			DesiredResultHypothesis: "A bounded read-only diagnostic view",
			TaskConfidence:          0.88,
			BoundaryConfidence:      0.74,
			DesiredResultConfidence: 0.83,
			Alternatives: []TaskEpisodeHypothesisAlternative{
				{
					Kind:       "task",
					Statement:  "Review the debugger without changing it",
					Confidence: 0.12,
				},
			},
			ReasoningHypotheses: []TaskEpisodeReasoningHypothesis{
				{
					Statement:   "The user may prioritize diagnostic traceability.",
					Confidence:  0.49,
					Basis:       TaskReasoningBasisContextualInference,
					EvidenceIDs: []string{onsetEvidenceID},
				},
			},
		},
		CreatedAt: startedAt + 10,
	}
}

func taskShadowDebugExactOnsetInput(
	t *testing.T,
	id, evidenceRef string,
	startedAt int64,
) CreateTaskEpisodeInput {
	t.Helper()
	input := taskShadowDebugEpisodeInput(id, startedAt, false)
	onset := TaskOnsetEvidence{
		Ref:        evidenceRef,
		Kind:       "keyboard",
		SourceKind: "event",
		SourceID:   "shared-source-event",
		OccurredAt: startedAt,
		Summary:    "Entered the same user request",
	}
	payload, err := json.Marshal(onset)
	if err != nil {
		t.Fatal(err)
	}
	input.InitialEvidence[0].SourceKind = onset.SourceKind
	input.InitialEvidence[0].SourceID = onset.SourceID
	input.InitialEvidence[0].Summary = onset.Summary
	input.InitialEvidence[0].PayloadJSON = string(payload)
	input.InitialEvidence[0].ObservedAt = onset.OccurredAt
	return input
}

type taskShadowDebugTestGraph struct {
	Store             *ActivityStore
	App               *App
	Episode           TaskEpisode
	Run               ShadowRun
	Execution         ShadowExecutionAttempt
	Observation       ShadowRunObservation
	Evaluation        ShadowRunEvaluation
	ReasoningEvidence TaskEpisodeEvidence
	LearningRun       ShadowTaskLearningRun
	RevisionID        string
	Materialization   ShadowTaskLearningMaterialization
	AgentAttempts     []TaskAgentAttempt
}

func recordTaskShadowDebugAgentAttempt(
	t *testing.T,
	store *ActivityStore,
	episodeID, runID, evaluationID, phase, key string,
	startedAt int64,
	failed bool,
) TaskAgentAttempt {
	t.Helper()
	promptVersion := "task-shadow-debug-v1"
	switch phase {
	case TaskAgentAttemptPhaseOnset:
		promptVersion = taskOnsetPromptVersion
	case TaskAgentAttemptPhaseLifecycle:
		promptVersion = taskLifecyclePromptVersion
	case TaskAgentAttemptPhaseEvaluation:
		promptVersion = ShadowTaskComparatorVersion
	case TaskAgentAttemptPhaseLearning:
		promptVersion = ShadowTaskLearningPolicyVersion
	}
	input := TaskAgentAttemptInput{
		IdempotencyKey:      key,
		Phase:               phase,
		TaskEpisodeID:       episodeID,
		ShadowRunID:         runID,
		ShadowEvaluationID:  evaluationID,
		ModelName:           "gpt-5.6-sol",
		ModelVersion:        "2026-07-28",
		PromptVersion:       promptVersion,
		PromptText:          taskShadowDebugAttemptPrompt + ":" + phase,
		NormalizedInputJSON: `{"phase":"` + phase + `"}`,
		Status:              TaskAgentAttemptStatusSucceeded,
		RawOutput:           taskShadowDebugAttemptRaw + ":" + phase,
		ParsedOutputJSON:    `{"ok":true}`,
		StartedAt:           startedAt,
		CompletedAt:         startedAt + 10,
		CreatedAt:           startedAt,
	}
	if failed {
		input.Status = TaskAgentAttemptStatusFailed
		input.ParsedOutputJSON = ""
		input.Error = taskShadowDebugAttemptError + ":" + phase
	}
	item, added, err := store.RecordTaskAgentAttempt(input)
	if err != nil || !added {
		t.Fatalf("record %s task agent attempt: added=%v err=%v", phase, added, err)
	}
	return item
}

func seedTaskShadowDebugGraph(t *testing.T) taskShadowDebugTestGraph {
	t.Helper()
	store := newTaskShadowDebugTestStore(t)
	input := taskShadowDebugEpisodeInput("task-shadow-debug-episode", 1_000, true)
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create task episode: added=%v err=%v", added, err)
	}
	for index, evidence := range []TaskEpisodeEvidence{
		{
			Role:       TaskEpisodeEvidenceAction,
			SourceKind: "activity",
			SourceID:   "debug-action-1",
			Summary:    "The user inspected the task ledger.",
			ObservedAt: 1_100,
		},
		{
			Role:       TaskEpisodeEvidenceAction,
			SourceKind: "activity",
			SourceID:   "debug-action-2",
			Summary:    "The user added the read-only aggregate.",
			ObservedAt: 1_200,
		},
		{
			Role:       TaskEpisodeEvidenceResult,
			SourceKind: "activity",
			SourceID:   "debug-result",
			Summary:    "The debugger tests passed.",
			ObservedAt: 1_300,
		},
	} {
		evidence.TaskEpisodeID = episode.ID
		evidence.CreatedAt = evidence.ObservedAt + int64(index)
		if _, _, err := store.AppendTaskEpisodeEvidence(evidence); err != nil {
			t.Fatalf("append task episode evidence: %v", err)
		}
	}
	reasoningPayload, err := json.Marshal(taskLifecycleReasoningEvidencePayload{
		Version:      taskLifecyclePromptVersion,
		Kind:         TaskLifecycleReasoningCorrection,
		SourceQuote:  "Actually, I changed direction because traceability matters.",
		EvidenceRefs: []string{"suffix:user-correction"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reasoningSourceID := deterministicTaskEpisodeID(
		"task-lifecycle-reasoning-source",
		strings.Join([]string{
			episode.ID,
			TaskLifecycleReasoningCorrection,
			normalizeTaskReasoningText(
				"Actually, I changed direction because traceability matters.",
			),
			"suffix:user-correction",
		}, "\x00"),
	)
	reasoningEvidence, added, err := store.AppendTaskEpisodeEvidence(
		TaskEpisodeEvidence{
			TaskEpisodeID: episode.ID,
			Role:          TaskEpisodeEvidenceReasoning,
			SourceKind:    "task_lifecycle_reasoning",
			SourceID:      reasoningSourceID,
			Summary:       "Actually, I changed direction because traceability matters.",
			PayloadJSON:   string(reasoningPayload),
			ObservedAt:    1_250,
			CreatedAt:     1_251,
		},
	)
	if err != nil || !added {
		t.Fatalf("append reasoning evidence: added=%v err=%v", added, err)
	}

	run, added, err := store.CreateShadowTaskRunFromEpisode(
		episode,
		"gpt-5.6-sol",
		1_050,
	)
	if err != nil || !added {
		t.Fatalf("create task mirror run: added=%v err=%v", added, err)
	}
	plan := *run.Prediction.CandidateExecutionPlan
	plan.Prompt = taskShadowDebugExecutionSecret
	execution, added, err := store.AddShadowExecutionAttempt(ShadowExecutionAttempt{
		ShadowRunID:    run.ID,
		IdempotencyKey: run.ID + ":debug-execution",
		RuntimeVersion: shadowExecutionRuntimeVersion,
		Status:         "completed",
		PolicyVersion:  authority.ShadowPolicyVersion,
		AgentRunID:     "isolated-agent-run",
		Model:          "gpt-5.6-sol",
		Prompt:         taskShadowDebugExecutionSecret,
		Plan:           plan,
		Decisions:      []authority.Evaluation{},
		Actions: []ShadowExecutionAction{
			{
				Type:      "tool",
				ToolName:  "write_file",
				ToolUseID: "tool-use-1",
				InputJSON: `{"path":"debug.txt"}`,
				Output:    "wrote isolated artifact",
			},
		},
		Artifacts: []ShadowExecutionArtifact{
			{
				Path:      "debug.txt",
				SizeBytes: int64(len(taskShadowDebugArtifactSecret)),
				SHA256:    sha256Hex([]byte(taskShadowDebugArtifactSecret)),
				Content:   taskShadowDebugArtifactSecret,
			},
		},
		AssistantOutput: "private duplicate execution result",
		StartedAt:       1_060,
		CompletedAt:     1_090,
		CreatedAt:       1_090,
	})
	if err != nil || !added {
		t.Fatalf("record execution attempt: added=%v err=%v", added, err)
	}

	observation, added, err := store.AddShadowRunObservation(ShadowRunObservation{
		ShadowRunID:    run.ID,
		SourceKind:     "task_episode_evaluation",
		SourceID:       "task-shadow-debug-observation",
		ObservedAction: "The user implemented the debugger backend.",
		ObservedResult: "A bounded read-only debugger was produced.",
		ReasoningEvidence: []ShadowReasoningEvidence{
			{
				Kind:       "correction",
				Statement:  reasoningEvidence.Summary,
				Confidence: 0.9,
				SourceRefs: []string{"observed:" + reasoningEvidence.ID},
			},
		},
		Evidence: []ShadowEvidenceReference{
			{
				Role:       "result",
				SourceKind: "task_episode_evidence",
				SourceID:   "debug-result",
				Content:    "duplicate private observation content",
				Summary:    "The debugger tests passed.",
				ObservedAt: 1_300,
			},
		},
		ObservedAt: 1_300,
		CreatedAt:  1_310,
	})
	if err != nil || !added {
		t.Fatalf("record observation: added=%v err=%v", added, err)
	}
	score := 0.91
	reasoningEvidenceRef := "observed:" + reasoningEvidence.ID
	evaluationInput := ShadowTaskEvaluationInput{
		Version:            ShadowTaskEvaluationInputVersion,
		TaskEpisodeID:      episode.ID,
		ShadowRunID:        run.ID,
		FrozenContextHash:  run.TaskContextHash,
		PredictedReasoning: append([]ShadowReasoningHypothesis(nil), run.Prediction.ReasoningHypotheses...),
		Shadow: ShadowTaskEvaluationWork{
			Task:          run.Prediction.Goal,
			DesiredResult: run.Prediction.ExpectedResult,
			Result:        "The isolated debugger implementation completed.",
		},
		Observed: ShadowTaskEvaluationWork{
			Task:          episode.LatestHypothesis.TaskHypothesis,
			DesiredResult: episode.LatestHypothesis.DesiredResultHypothesis,
			Result:        "The user completed the debugger implementation.",
		},
		Evidence: []ShadowTaskEvaluationEvidence{
			{
				ID:      run.Prediction.ReasoningHypotheses[0].EvidenceRefs[0],
				Origin:  ShadowTaskEvidenceOriginObserved,
				Kind:    ShadowTaskEvidenceTask,
				Summary: "The user began implementing the debugger.",
			},
			{
				ID:      reasoningEvidenceRef,
				Origin:  ShadowTaskEvidenceOriginObserved,
				Kind:    ShadowTaskEvidenceExplicitCorrection,
				Summary: reasoningEvidence.Summary,
			},
		},
	}
	taskEvaluation := ShadowTaskEvaluationOutput{
		Version:          ShadowTaskEvaluationOutputVersion,
		Summary:          "The source-backed correction changed the predicted explanation.",
		Task:             unknownTaskDimension(),
		Boundary:         unknownTaskDimension(),
		Process:          unknownTaskDimension(),
		ResultSimilarity: unknownTaskDimension(),
		ResultQuality:    unknownTaskDimension(),
		Timing:           unknownTaskDimension(),
		Reasoning:        matchedTaskDimension(reasoningEvidenceRef),
		Safety:           unknownTaskDimension(),
	}
	evaluation, added, err := store.AddShadowRunEvaluation(ShadowRunEvaluation{
		ShadowRunID:       run.ID,
		ObservationID:     observation.ID,
		IdempotencyKey:    run.ID + ":debug-evaluation",
		ComparisonVersion: ShadowTaskComparatorVersion,
		EvaluatorVersion:  ShadowTaskEvaluatorVersion,
		EvaluatorModel:    "gpt-5.6-terra",
		ComparatorPrompt:  taskShadowDebugComparatorSecret,
		EvaluatorRaw:      taskShadowDebugEvaluatorSecret,
		Action: ShadowEvaluationDimension{
			Assessment: "matched",
			Score:      &score,
			Confidence: 0.9,
		},
		Outcome: ShadowEvaluationDimension{
			Assessment: "partial",
			Score:      &score,
			Confidence: 0.86,
		},
		Timing: ShadowEvaluationDimension{Assessment: "unknown"},
		Reasoning: ShadowEvaluationDimension{
			Assessment:   "matched",
			Score:        &score,
			Confidence:   0.9,
			Reason:       "The explicit correction is independently observed.",
			EvidenceRefs: []string{reasoningEvidenceRef},
		},
		Safety: ShadowEvaluationDimension{
			Assessment: "safe",
			Score:      &score,
			Confidence: 1,
		},
		TaskEvaluationInput: &evaluationInput,
		TaskEvaluation:      &taskEvaluation,
		Summary:             "The shadow matched the observed task closely.",
		CreatedAt:           1_320,
	})
	if err != nil || !added {
		t.Fatalf("record evaluation: added=%v err=%v", added, err)
	}

	agentAttempts := []TaskAgentAttempt{
		recordTaskShadowDebugAgentAttempt(
			t, store, episode.ID, "", "", TaskAgentAttemptPhaseOnset,
			"debug-onset", 1_400, false,
		),
		recordTaskShadowDebugAgentAttempt(
			t, store, episode.ID, "", "", TaskAgentAttemptPhaseLifecycle,
			"debug-lifecycle", 1_500, false,
		),
		recordTaskShadowDebugAgentAttempt(
			t, store, episode.ID, run.ID, evaluation.ID,
			TaskAgentAttemptPhaseEvaluation, "debug-evaluation", 1_600, false,
		),
		recordTaskShadowDebugAgentAttempt(
			t, store, episode.ID, run.ID, evaluation.ID,
			TaskAgentAttemptPhaseLearning, "debug-learning", 1_700, true,
		),
	}

	revisionID := "task-shadow-debug-learning-revision"
	projection := ShadowTaskLearningProjection{
		PolicyVersion:   ShadowTaskLearningPolicyVersion,
		AuthorityEffect: ShadowTaskLearningAuthorityNone,
		EvaluationCount: 1,
		ExcludedEvaluations: []ShadowTaskLearningExclusion{
			{
				RecordKey:    "debug-record",
				EvaluationID: evaluation.ID,
				Reason:       "insufficient repeated evidence",
			},
		},
		Candidates: []ShadowTaskLearningCandidate{
			{
				ProjectionKey:   "debug:process-pattern",
				RevisionID:      revisionID,
				RevisionMode:    ShadowTaskLearningRevisionAppendOnly,
				PolicyVersion:   ShadowTaskLearningPolicyVersion,
				TargetKind:      ShadowTaskLearningTargetMemory,
				LearningKind:    ShadowTaskLearningKindProcessPattern,
				CapabilityKey:   run.CapabilityKey,
				Statement:       "The user validates debugger linkage before UI work.",
				EvidenceHash:    sha256Hex([]byte("debug-learning-evidence")),
				AuthorityEffect: ShadowTaskLearningAuthorityNone,
				Evidence: []ShadowTaskLearningProvenance{
					{
						EvaluationID:  evaluation.ID,
						TaskEpisodeID: episode.ID,
						ShadowRunID:   run.ID,
						ContextHash:   episode.FrozenContext.SHA256,
						EvidenceID:    "observed:debug-result",
						Origin:        ShadowTaskEvidenceOriginObserved,
						Kind:          ShadowTaskEvidenceResult,
						Summary:       "The debugger tests passed.",
						OccurredAt:    1_300,
						PersistedAt:   1_320,
					},
				},
				Dimensions: []ShadowTaskLearningDimensionEvidence{
					{
						EvaluationID: evaluation.ID,
						Name:         "result_quality",
						Assessment:   "partial",
						Score:        &score,
						Confidence:   0.86,
						EvidenceRefs: []string{"observed:debug-result"},
					},
				},
				Qualification: ShadowTaskLearningQualification{
					Decision:                 ShadowTaskLearningDecisionCollecting,
					Reasons:                  []string{"needs repeated production evidence"},
					ObservedEvaluationCount:  1,
					ObservedTaskEpisodeCount: 1,
					ObservedContextCount:     1,
					RequiredEvaluationCount:  3,
					RequiredTaskEpisodeCount: 3,
					RequiredContextCount:     2,
				},
			},
		},
	}
	learningRun, added, err := PersistShadowTaskLearningProjection(
		store,
		projection,
		1_800,
	)
	if err != nil || !added {
		t.Fatalf("persist learning projection: added=%v err=%v", added, err)
	}
	materialization, added, err := store.AddShadowTaskLearningMaterialization(
		ShadowTaskLearningMaterialization{
			RevisionID: revisionID,
			TargetKind: ShadowTaskLearningTargetMemory,
			Status:     "failed",
			Error:      "memory writer unavailable",
			CreatedAt:  1_900,
		},
	)
	if err != nil || !added {
		t.Fatalf("persist materialization: added=%v err=%v", added, err)
	}

	return taskShadowDebugTestGraph{
		Store:             store,
		App:               &App{activity: &activityMonitorState{store: store}},
		Episode:           episode,
		Run:               run,
		Execution:         execution,
		Observation:       observation,
		Evaluation:        evaluation,
		ReasoningEvidence: reasoningEvidence,
		LearningRun:       learningRun,
		RevisionID:        revisionID,
		Materialization:   materialization,
		AgentAttempts:     agentAttempts,
	}
}

func TestTaskShadowDebugEmptyStateAndGuard(t *testing.T) {
	var nilApp *App
	if _, err := nilApp.GetTaskShadowDebugSnapshot(10); err == nil {
		t.Fatal("nil app unexpectedly accessed task shadow debugger")
	}
	if _, err := (&App{}).GetTaskShadowDebugDetail("", "", 10); err == nil {
		t.Fatal("uninitialized app unexpectedly accessed task shadow debugger")
	}

	store := newTaskShadowDebugTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	snapshot, err := app.GetTaskShadowDebugSnapshot(10)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Episodes == nil || len(snapshot.Episodes) != 0 {
		t.Fatalf("empty snapshot episodes=%+v", snapshot.Episodes)
	}
	if len(snapshot.EnabledWorkflows) != 2 ||
		snapshot.EnabledWorkflows[0] != TaskWorkflowComposeText ||
		snapshot.EnabledWorkflows[1] != TaskWorkflowAnalyzeText {
		t.Fatalf("empty snapshot workflows=%v", snapshot.EnabledWorkflows)
	}
	if snapshot.ReasoningVersions != taskShadowDebugReasoningCurrentVersions() {
		t.Fatalf("reasoning versions=%+v", snapshot.ReasoningVersions)
	}
	if snapshot.LifecycleHealth.EpisodeCount != 0 ||
		snapshot.LifecycleHealth.OpenCount != 0 ||
		snapshot.LifecycleHealth.PolicyVersion !=
			taskLifecycleExpiryPolicyVersion ||
		snapshot.LifecycleHealth.PromptVersion != taskLifecyclePromptVersion ||
		snapshot.LifecycleHealth.ShadowPromptVersion != shadowTaskPromptVersion {
		t.Fatalf("empty lifecycle health=%+v", snapshot.LifecycleHealth)
	}
	if snapshot.Continuation.Version != taskShadowContinuationVersion ||
		snapshot.Continuation.ScannedRunCount != 0 ||
		snapshot.Continuation.Items == nil ||
		len(snapshot.Continuation.Items) != 0 {
		t.Fatalf("empty continuation=%+v", snapshot.Continuation)
	}
	if _, err := app.GetTaskShadowDebugDetail("", "", 10); err == nil ||
		!strings.Contains(err.Error(), "task episode ID") {
		t.Fatalf("empty detail selector error=%v", err)
	}
	for _, method := range []string{
		"GetTaskShadowDebugSnapshot",
		"GetTaskShadowDebugDetail",
	} {
		if !browserRPCAllowlist[method] {
			t.Errorf("%s must use the browser RPC devtools allowlist", method)
		}
	}
	for _, forbidden := range []string{
		"ApproveTaskShadow",
		"PromoteTaskShadow",
		"ReviewTaskShadow",
		"ExecuteTaskShadow",
		"ReconcileShadowTaskLearningMaterializations",
	} {
		if browserRPCAllowlist[forbidden] {
			t.Errorf("mutating task shadow debugger method %s must not exist", forbidden)
		}
	}
}

func TestTaskShadowDebugReportsHistoricalExactDuplicateOnsetsReadOnly(
	t *testing.T,
) {
	store := newTaskShadowDebugTestStore(t)
	for _, fixture := range []struct {
		id  string
		ref string
	}{
		{id: "duplicate-onset-a", ref: "event:shared-start"},
		{id: "duplicate-onset-b", ref: "event:shared-start"},
		{id: "same-source-different-ref", ref: "event:other-start"},
	} {
		input := taskShadowDebugExactOnsetInput(
			t,
			fixture.id,
			fixture.ref,
			1_000,
		)
		if _, added, err := store.CreateTaskEpisode(input); err != nil || !added {
			t.Fatalf("create %s: added=%v err=%v", fixture.id, added, err)
		}
	}
	var beforeEpisodes, beforeEvidence int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_episodes`).Scan(
		&beforeEpisodes,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_episode_evidence`).Scan(
		&beforeEvidence,
	); err != nil {
		t.Fatal(err)
	}

	app := &App{activity: &activityMonitorState{store: store}}
	snapshot, err := app.GetTaskShadowDebugSnapshot(20)
	if err != nil {
		t.Fatal(err)
	}
	health := snapshot.LifecycleHealth
	if health.DuplicateOnsetClusterCount != 1 ||
		health.DuplicateOnsetEpisodeCount != 2 ||
		health.DuplicateOnsetClustersTruncated ||
		len(health.DuplicateOnsetClusters) != 1 {
		t.Fatalf("duplicate onset health=%+v", health)
	}
	cluster := health.DuplicateOnsetClusters[0]
	if cluster.SourceKind != "event" ||
		cluster.SourceID != "shared-source-event" ||
		cluster.EvidenceRef != "event:shared-start" ||
		cluster.ObservedAt != 1_000 || cluster.InferredStartAt != 1_000 ||
		!reflect.DeepEqual(
			cluster.EpisodeIDs,
			[]string{"duplicate-onset-a", "duplicate-onset-b"},
		) {
		t.Fatalf("duplicate onset cluster=%+v", cluster)
	}
	var afterEpisodes, afterEvidence int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_episodes`).Scan(
		&afterEpisodes,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_episode_evidence`).Scan(
		&afterEvidence,
	); err != nil {
		t.Fatal(err)
	}
	if afterEpisodes != beforeEpisodes || afterEvidence != beforeEvidence {
		t.Fatalf(
			"debug read mutated history: episodes %d->%d evidence %d->%d",
			beforeEpisodes,
			afterEpisodes,
			beforeEvidence,
			afterEvidence,
		)
	}
}

func TestTaskShadowDebugCombinesRedactedResultDestinationReceipts(
	t *testing.T,
) {
	content := "private committed customer update"
	destination := taskLifecycleTestResultDestination()
	destinationSHA256, err := taskLifecycleResultDestinationSHA256(destination)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(TaskLifecycleObservation{
		MaterializedResult: &TaskLifecycleMaterializedResult{
			Workflow:          TaskWorkflowComposeText,
			Kind:              TaskLifecycleContentCommittedText,
			Content:           content,
			SHA256:            sha256Hex([]byte(content)),
			EvidenceRefs:      []string{"suffix:text"},
			Destination:       destination,
			DestinationSHA256: destinationSHA256,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipts := taskShadowDebugResultDestinations(
		[]TaskEpisodeEvidence{
			{
				ID:          "result-a",
				Role:        TaskEpisodeEvidenceResult,
				PayloadJSON: string(payload),
				ObservedAt:  1_500,
			},
			{
				ID:          "result-b",
				Role:        TaskEpisodeEvidenceResult,
				PayloadJSON: string(payload),
				ObservedAt:  1_600,
			},
		},
		TaskWorkflowComposeText,
		20,
	)
	if len(receipts) != 1 || !receipts[0].Current ||
		!receipts[0].Valid || receipts[0].Status != "current_valid" ||
		len(receipts[0].EvidenceIDs) != 2 ||
		receipts[0].DestinationSHA256 != destinationSHA256 ||
		receipts[0].LatestObservedAt != 1_600 {
		t.Fatalf("destination receipts=%+v", receipts)
	}
	encoded, err := json.Marshal(receipts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), content) {
		t.Fatal("debug destination receipt exposed committed text")
	}
}

func TestTaskShadowDebugNoRunDistinguishesRolloutEligibility(t *testing.T) {
	store := newTaskShadowDebugTestStore(t)
	outsideInput := taskShadowDebugEpisodeInput(
		"debug-outside-rollout",
		1_000,
		false,
	)
	outside, added, err := store.CreateTaskEpisode(outsideInput)
	if err != nil || !added {
		t.Fatalf("create outside episode: added=%v err=%v", added, err)
	}
	outsideSummary, err := taskShadowDebugEpisodeSummary(store, outside)
	if err != nil {
		t.Fatal(err)
	}
	if outsideSummary.LatestPairState != "outside_production_rollout" ||
		taskWorkflowProductionShadowEligible(outsideSummary.LatestPairWorkflow) ||
		outsideSummary.LinkedShadowRunCount != 0 ||
		len(outsideSummary.LatestPairReasons) != 1 {
		t.Fatalf("outside rollout summary=%+v", outsideSummary)
	}
	app := &App{activity: &activityMonitorState{store: store}}
	outsideDetail, err := app.GetTaskShadowDebugDetail(outside.ID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if outsideDetail.ProductionRollout.Eligible ||
		outsideDetail.ProductionRollout.State != "outside_production_rollout" ||
		outsideDetail.ProductionRollout.Workflow != outsideSummary.LatestPairWorkflow ||
		outsideDetail.ProductionRollout.Reason != outsideSummary.LatestPairReasons[0] {
		t.Fatalf("outside rollout detail=%+v", outsideDetail.ProductionRollout)
	}

	composeInput := taskShadowDebugEpisodeInput(
		"debug-compose-pending",
		2_000,
		false,
	)
	composeInput.InitialHypothesis.TaskHypothesis =
		"Compose a customer update"
	composeInput.InitialHypothesis.DesiredResultHypothesis =
		"A concise update ready to send"
	compose, added, err := store.CreateTaskEpisode(composeInput)
	if err != nil || !added {
		t.Fatalf("create compose episode: added=%v err=%v", added, err)
	}
	composeSummary, err := taskShadowDebugEpisodeSummary(store, compose)
	if err != nil {
		t.Fatal(err)
	}
	if composeSummary.LatestPairWorkflow != TaskWorkflowComposeText ||
		composeSummary.LatestPairState != "shadow_pending" ||
		composeSummary.LinkedShadowRunCount != 0 ||
		len(composeSummary.LatestPairReasons) != 1 {
		t.Fatalf("compose pending summary=%+v", composeSummary)
	}
	composeDetail, err := app.GetTaskShadowDebugDetail(compose.ID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if !composeDetail.ProductionRollout.Eligible ||
		composeDetail.ProductionRollout.State != "eligible" ||
		composeDetail.ProductionRollout.Workflow != TaskWorkflowComposeText ||
		len(composeDetail.ProductionRollout.EnabledWorkflows) != 2 ||
		composeDetail.ProductionRollout.EnabledWorkflows[0] !=
			TaskWorkflowComposeText ||
		composeDetail.ProductionRollout.EnabledWorkflows[1] !=
			TaskWorkflowAnalyzeText {
		t.Fatalf("compose rollout detail=%+v", composeDetail.ProductionRollout)
	}

	analyzeInput := taskShadowDebugEpisodeInput(
		"debug-analyze-insufficient",
		2_500,
		false,
	)
	analyzeInput.InitialHypothesis.TaskHypothesis =
		"Review the supplied proposal"
	analyzeInput.InitialHypothesis.DesiredResultHypothesis =
		"Findings that identify the material gaps and risks"
	analyze, added, err := store.CreateTaskEpisode(analyzeInput)
	if err != nil || !added {
		t.Fatalf("create analyze episode: added=%v err=%v", added, err)
	}
	analyzeDetail, err := app.GetTaskShadowDebugDetail(analyze.ID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if analyzeDetail.ProductionRollout.Workflow != TaskWorkflowAnalyzeText ||
		analyzeDetail.ProductionRollout.Eligible ||
		analyzeDetail.ProductionRollout.State !=
			"insufficient_frozen_context" ||
		!strings.Contains(
			analyzeDetail.ProductionRollout.Reason,
			"exact pre-onset activity snapshot",
		) {
		t.Fatalf("analyze rollout detail=%+v", analyzeDetail.ProductionRollout)
	}

	transformInput := taskShadowDebugEpisodeInput(
		"debug-transform-insufficient",
		3_000,
		false,
	)
	transformInput.InitialHypothesis.TaskHypothesis =
		"Convert email content into a one-page document using ChatGPT"
	transformInput.InitialHypothesis.DesiredResultHypothesis =
		"A one-page version of the email content"
	transform, added, err := store.CreateTaskEpisode(transformInput)
	if err != nil || !added {
		t.Fatalf("create transform episode: added=%v err=%v", added, err)
	}
	transformSummary, err := taskShadowDebugEpisodeSummary(store, transform)
	if err != nil {
		t.Fatal(err)
	}
	if transformSummary.LatestPairWorkflow != TaskWorkflowComposeText ||
		transformSummary.LatestPairState != "insufficient_frozen_context" ||
		transformSummary.LinkedShadowRunCount != 0 {
		t.Fatalf("transform summary=%+v", transformSummary)
	}
	transformDetail, err := app.GetTaskShadowDebugDetail(
		transform.ID,
		"",
		20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if transformDetail.ProductionRollout.Eligible ||
		transformDetail.ProductionRollout.State !=
			"insufficient_frozen_context" {
		t.Fatalf("transform rollout=%+v", transformDetail.ProductionRollout)
	}
	gate := taskShadowReleaseGateFromDetail(
		&transformDetail,
		true,
		activitymon.AppInfo{AppName: "Mail", BundleID: "com.apple.mail"},
	)
	if gate.Stage != "frozen_context_insufficient" ||
		!gate.CanObserveNaturalTask || gate.Complete ||
		!strings.Contains(gate.RequiredAction, "background learning") {
		t.Fatalf("transform gate=%+v", gate)
	}

	compact, err := listTaskShadowDebugEpisodeSummaries(
		store,
		taskShadowDebugMaxLimit,
	)
	if err != nil {
		t.Fatal(err)
	}
	compactByID := make(map[string]TaskShadowDebugEpisodeSummary, len(compact))
	for _, item := range compact {
		compactByID[item.ID] = item
	}
	for _, episode := range []TaskEpisode{outside, compose, analyze, transform} {
		detailed, detailErr := taskShadowDebugEpisodeSummary(store, episode)
		if detailErr != nil {
			t.Fatal(detailErr)
		}
		indexed, ok := compactByID[episode.ID]
		if !ok {
			t.Fatalf("compact summary missing episode %q", episode.ID)
		}
		if !reflect.DeepEqual(indexed, detailed) {
			t.Fatalf(
				"no-run compact summary drifted for %q\ncompact=%+v\ndetailed=%+v",
				episode.ID,
				indexed,
				detailed,
			)
		}
	}
}

func TestTaskShadowReleaseGateDerivesOnlyPersistedProof(t *testing.T) {
	locked := taskShadowReleaseGateFromDetail(
		nil,
		true,
		activitymon.AppInfo{
			BundleID: "com.apple.loginwindow",
			AppName:  "loginwindow",
		},
	)
	if locked.Stage != "desktop_locked" ||
		locked.Complete ||
		locked.CanObserveNaturalTask {
		t.Fatalf("locked release gate=%+v", locked)
	}
	for _, step := range locked.Steps {
		if step.Complete || step.EvidenceID != "" {
			t.Fatalf("lock state invented release evidence: %+v", step)
		}
	}

	waiting := taskShadowReleaseGateFromDetail(
		nil,
		true,
		activitymon.AppInfo{
			BundleID: "com.apple.mail",
			AppName:  "Mail",
		},
	)
	if waiting.Stage != "waiting_for_natural_task" ||
		waiting.Complete ||
		!waiting.CanObserveNaturalTask {
		t.Fatalf("waiting release gate=%+v", waiting)
	}

	detail := &TaskShadowDebugDetail{
		Episode: TaskShadowDebugEpisodeDetail{
			Episode: TaskShadowDebugEpisode{
				ID:            "natural-compose-episode",
				CurrentStatus: TaskEpisodeStatusCompleted,
			},
		},
		Runs: []TaskShadowDebugRun{
			{
				Run: ShadowRun{
					ID:            "natural-compose-run",
					CapabilityKey: TaskWorkflowComposeText,
					PromptVersion: shadowTaskPromptVersion,
				},
				PairReadiness: ShadowTaskPairReadiness{
					Workflow:               TaskWorkflowComposeText,
					State:                  ShadowTaskPairStateEvaluated,
					Ready:                  true,
					ShadowPromptVersion:    shadowTaskPromptVersion,
					CurrentGeneration:      true,
					TaskEpisodeStatus:      TaskEpisodeStatusCompleted,
					ShadowAttemptStatus:    "completed",
					UserResultMaterialized: true,
					UserResultSHA256:       strings.Repeat("a", 64),
					UserResultDestinationSHA256: strings.Repeat(
						"c",
						64,
					),
					ShadowResultMaterialized: true,
					ShadowResultSHA256:       strings.Repeat("b", 64),
					EvaluationIDs:            []string{"natural-evaluation"},
					CurrentEvaluationIDs:     []string{"natural-evaluation"},
					Reasons:                  []string{},
				},
				ExecutionAttempts: []TaskShadowDebugExecutionAttempt{
					{
						Attempt: ShadowExecutionAttempt{
							ID:     "natural-shadow-attempt",
							Status: "completed",
						},
					},
				},
				Evaluations: []TaskShadowDebugEvaluation{
					{
						Evaluation: ShadowRunEvaluation{
							ID: "natural-evaluation",
						},
					},
				},
			},
		},
		Learning: TaskShadowDebugLearning{
			Runs: []ShadowTaskLearningRun{
				{
					ID:            "natural-learning-run",
					PolicyVersion: ShadowTaskLearningPolicyVersion,
					Projection: ShadowTaskLearningProjection{
						PolicyVersion: ShadowTaskLearningPolicyVersion,
						Candidates: []ShadowTaskLearningCandidate{{
							Dimensions: []ShadowTaskLearningDimensionEvidence{{
								EvaluationID: "natural-evaluation",
							}},
						}},
					},
				},
			},
		},
	}
	detail.releaseObserverProvenance = TaskShadowReleaseObserverProvenance{
		Version:                  TaskShadowReleaseObserverProvenanceVersion,
		State:                    TaskShadowReleaseObserverOriginVerified,
		Reason:                   "exact test provenance",
		SourceComplete:           true,
		OriginComplete:           true,
		PipelineRunID:            42,
		TraceID:                  "natural-observer-trace",
		TraceContractVersion:     TaskShadowObserverTraceContractVersion,
		CoverageState:            TaskShadowObserverCoverageCovered,
		Admission:                TaskShadowObserverAdmissionEligible,
		Flow:                     TaskShadowObserverFlowFullOnset,
		TerminalStatus:           TaskShadowObserverEventCompleted,
		ProductionOnsetAttemptID: "natural-onset-attempt",
		TaskEpisodeID:            "natural-compose-episode",
		Workflow:                 TaskWorkflowComposeText,
		WorkflowDecisionState:    TaskShadowWorkflowStateRunCreated,
		ShadowRunID:              "natural-compose-run",
		CandidateTraceCount:      1,
		CandidateDecisionCount:   1,
		IntegrityValid:           true,
	}
	detail.releaseLearningRunID = "natural-learning-run"
	detail.releaseLearningResolution = &ShadowTaskLearningMaterializationResolution{
		Version:             ShadowTaskLearningMaterializationVersion,
		RunID:               "natural-learning-run",
		Complete:            true,
		CandidateCount:      1,
		TerminalCount:       1,
		MaterializedCount:   1,
		PendingRevisionIDs:  []string{},
		ErrorRevisionIDs:    []string{},
		TerminalRevisionIDs: []string{"natural-learning-revision"},
	}
	complete := taskShadowReleaseGateFromDetail(
		detail,
		false,
		activitymon.AppInfo{},
	)
	if !complete.Complete ||
		complete.Stage != "complete" ||
		complete.Version != taskShadowReleaseGateVersion ||
		!complete.CurrentGeneration ||
		complete.SelectedWorkflow != TaskWorkflowComposeText ||
		complete.RequiredShadowPrompt != shadowTaskPromptVersion ||
		complete.SelectedShadowPrompt != shadowTaskPromptVersion ||
		complete.LatestTaskEpisodeID != "natural-compose-episode" ||
		complete.LatestShadowRunID != "natural-compose-run" {
		t.Fatalf("complete release gate=%+v", complete)
	}
	for _, step := range complete.Steps {
		if !step.Complete || step.EvidenceID == "" {
			t.Fatalf("complete gate is missing persisted proof: %+v", step)
		}
	}
	if len(complete.Steps) != 9 {
		t.Fatalf("source-bound gate steps=%+v", complete.Steps)
	}

	projectionOnly := *detail
	pendingResolution := *detail.releaseLearningResolution
	pendingResolution.Complete = false
	pendingResolution.TerminalCount = 0
	pendingResolution.MaterializedCount = 0
	pendingResolution.MissingCount = 1
	pendingResolution.PendingRevisionIDs = []string{"natural-learning-revision"}
	pendingResolution.TerminalRevisionIDs = []string{}
	projectionOnly.releaseLearningResolution = &pendingResolution
	projectionOnlyGate := taskShadowReleaseGateFromDetail(
		&projectionOnly,
		true,
		activitymon.AppInfo{BundleID: "com.apple.mail", AppName: "Mail"},
	)
	if projectionOnlyGate.Complete ||
		projectionOnlyGate.Stage != "learning_materialization_pending" ||
		projectionOnlyGate.LearningResolution == nil ||
		projectionOnlyGate.LearningResolution.MissingCount != 1 {
		t.Fatalf("projection-only release gate=%+v", projectionOnlyGate)
	}

	withoutObserver := *detail
	withoutObserver.releaseObserverProvenance =
		TaskShadowReleaseObserverProvenance{}
	withoutObserverGate := taskShadowReleaseGateFromDetail(
		&withoutObserver,
		true,
		activitymon.AppInfo{BundleID: "com.apple.mail", AppName: "Mail"},
	)
	if withoutObserverGate.Complete ||
		withoutObserverGate.Stage != "observer_source_pending" ||
		withoutObserverGate.Steps[0].Complete ||
		withoutObserverGate.Steps[1].Complete {
		t.Fatalf(
			"downstream-only proof bypassed observer source: %+v",
			withoutObserverGate,
		)
	}

	missingDestinationDetail := *detail
	missingDestinationDetail.Runs = append(
		[]TaskShadowDebugRun(nil),
		detail.Runs...,
	)
	missingDestinationDetail.Runs[0].PairReadiness.UserResultDestinationSHA256 = ""
	missingDestinationGate := taskShadowReleaseGateFromDetail(
		&missingDestinationDetail,
		true,
		activitymon.AppInfo{BundleID: "com.apple.mail", AppName: "Mail"},
	)
	if missingDestinationGate.Complete ||
		missingDestinationGate.Stage != "result_destination_pending" {
		t.Fatalf("missing destination completed gate: %+v", missingDestinationGate)
	}

	analyzeDetail := *detail
	analyzeDetail.Runs = append([]TaskShadowDebugRun(nil), detail.Runs...)
	analyzeDetail.Runs[0].Run.CapabilityKey = TaskWorkflowAnalyzeText
	analyzeDetail.Runs[0].PairReadiness.Workflow = TaskWorkflowAnalyzeText
	analyzeDetail.releaseObserverProvenance.Workflow = TaskWorkflowAnalyzeText
	analyzeGate := taskShadowReleaseGateFromDetail(
		&analyzeDetail,
		false,
		activitymon.AppInfo{},
	)
	if !analyzeGate.Complete ||
		analyzeGate.SelectedWorkflow != TaskWorkflowAnalyzeText {
		t.Fatalf("analyze parity proof=%+v", analyzeGate)
	}
	if taskShadowReleaseGateWorkflowEnabled("research.web") {
		t.Fatal("unsupported workflow entered natural parity proof")
	}
	newestRunDetail := *detail
	newestRunDetail.Runs = []TaskShadowDebugRun{
		detail.Runs[0],
		detail.Runs[0],
	}
	newestRunDetail.Runs[0].Run.ID = "newest-current-run"
	newestRunDetail.Runs[1].Run.ID = "older-current-run"
	if selectedRun := taskShadowReleaseGateCurrentRun(&newestRunDetail); selectedRun == nil || selectedRun.Run.ID != "newest-current-run" {
		t.Fatalf("current proof selected old run: %+v", selectedRun)
	}

	historicalWorkerDetail := *detail
	historicalWorkerDetail.Runs = append(
		[]TaskShadowDebugRun(nil),
		detail.Runs...,
	)
	historicalWorkerDetail.Runs[0].Run.PromptVersion = "shadow-task-v6"
	historicalWorkerDetail.Runs[0].PairReadiness.CurrentGeneration = false
	historicalWorkerDetail.releaseObserverProvenance.State =
		TaskShadowReleaseObserverAwaitingCurrentRun
	historicalWorkerDetail.releaseObserverProvenance.OriginComplete = false
	historicalWorkerGate := taskShadowReleaseGateFromDetail(
		&historicalWorkerDetail,
		true,
		activitymon.AppInfo{BundleID: "com.apple.mail", AppName: "Mail"},
	)
	if historicalWorkerGate.Complete ||
		historicalWorkerGate.CurrentGeneration ||
		historicalWorkerGate.Stage != "waiting_for_current_shadow_generation" ||
		len(historicalWorkerGate.HistoricalShadowRuns) != 1 ||
		historicalWorkerGate.HistoricalShadowRuns[0] != "natural-compose-run" ||
		historicalWorkerGate.LatestShadowRunID != "" {
		t.Fatalf("historical worker generation entered release gate: %+v", historicalWorkerGate)
	}
	for _, step := range historicalWorkerGate.Steps {
		if step.Key == "observer_source" {
			if !step.Complete || step.EvidenceID == "" {
				t.Fatalf("historical worker lost covered source: %+v", step)
			}
			continue
		}
		if step.Complete || step.EvidenceID != "" {
			t.Fatalf("historical worker completed %s: %+v", step.Key, step)
		}
	}

	historicalDetail := *detail
	historicalDetail.Runs = append([]TaskShadowDebugRun(nil), detail.Runs...)
	historicalDetail.Runs[0].PairReadiness.CurrentEvaluationIDs = nil
	historicalDetail.Runs[0].PairReadiness.HistoricalEvaluationIDs =
		[]string{"natural-evaluation"}
	historicalDetail.Runs[0].PairReadiness.State = ShadowTaskPairStateReady
	historicalDetail.Learning = TaskShadowDebugLearning{Runs: []ShadowTaskLearningRun{{
		ID:            "historical-learning-run",
		PolicyVersion: "shadow-task-learning-v1",
		Projection: ShadowTaskLearningProjection{
			PolicyVersion: "shadow-task-learning-v1",
			Candidates: []ShadowTaskLearningCandidate{{
				Dimensions: []ShadowTaskLearningDimensionEvidence{{
					EvaluationID: "natural-evaluation",
				}},
			}},
		},
	}}}
	historicalGate := taskShadowReleaseGateFromDetail(
		&historicalDetail,
		true,
		activitymon.AppInfo{BundleID: "com.apple.mail", AppName: "Mail"},
	)
	if historicalGate.Complete || historicalGate.Stage != "evaluation_pending" {
		t.Fatalf("historical generations completed release gate: %+v", historicalGate)
	}
	for _, step := range historicalGate.Steps {
		if (step.Key == "evaluation" || step.Key == "learning") && step.Complete {
			t.Fatalf("historical generation completed %s: %+v", step.Key, step)
		}
	}

	pendingDetail := *detail
	pendingDetail.Episode.Episode.ID = "newer-pending-compose"
	pendingDetail.Episode.Episode.CurrentStatus = TaskEpisodeStatusCandidate
	pendingDetail.Runs = nil
	pendingDetail.Learning = TaskShadowDebugLearning{}
	pendingDetail.releaseObserverProvenance.State =
		TaskShadowReleaseObserverAwaitingCurrentRun
	pendingDetail.releaseObserverProvenance.OriginComplete = false
	pendingGate := taskShadowReleaseGateFromDetail(
		&pendingDetail,
		true,
		activitymon.AppInfo{
			BundleID: "com.apple.mail",
			AppName:  "Mail",
		},
	)
	if pendingGate.Stage != "task_episode_pending" ||
		strings.Contains(pendingGate.RequiredAction, "save") ||
		!strings.Contains(pendingGate.RequiredAction, "send") {
		t.Fatalf("pending gate has misleading completion copy: %+v", pendingGate)
	}
	detail.Learning.Runs = nil
	detail.releaseLearningRunID = ""
	detail.releaseLearningResolution = nil
	learningPending := taskShadowReleaseGateFromDetail(
		detail,
		true,
		activitymon.AppInfo{
			BundleID: "com.apple.mail",
			AppName:  "Mail",
		},
	)
	if learningPending.Stage != "learning_pending" ||
		learningPending.Complete ||
		!learningPending.CanObserveNaturalTask {
		t.Fatalf("learning-pending release gate=%+v", learningPending)
	}
}

func TestTaskShadowReleaseGateIndexSelectionPreservesOlderProofCandidates(
	t *testing.T,
) {
	episodes := []taskShadowDebugEpisodeIndexRow{
		{Summary: TaskShadowDebugEpisodeSummary{
			ID:             "newer-pending-compose",
			Status:         TaskEpisodeStatusCandidate,
			TaskHypothesis: "Compose a customer update",
		}},
		{Summary: TaskShadowDebugEpisodeSummary{
			ID:             "newer-current-compose",
			Status:         TaskEpisodeStatusActive,
			TaskHypothesis: "Compose a customer update",
		}},
		{Summary: TaskShadowDebugEpisodeSummary{
			ID:             "older-complete-analyze",
			Status:         TaskEpisodeStatusCompleted,
			TaskHypothesis: "Analyze this proposal",
		}},
		{Summary: TaskShadowDebugEpisodeSummary{
			ID:             "historical-only-compose",
			Status:         TaskEpisodeStatusCompleted,
			TaskHypothesis: "Compose a customer update",
		}},
		{Summary: TaskShadowDebugEpisodeSummary{
			ID:             "outside-rollout",
			Status:         TaskEpisodeStatusCompleted,
			TaskHypothesis: "Research the market",
		}},
	}
	runs := map[string][]taskShadowDebugRunIndexRow{
		"newer-current-compose": {{
			ID:            "newer-current-run",
			CapabilityKey: TaskWorkflowComposeText,
			PromptVersion: shadowTaskPromptVersion,
		}},
		"older-complete-analyze": {{
			ID:            "older-current-run",
			CapabilityKey: TaskWorkflowAnalyzeText,
			PromptVersion: shadowTaskPromptVersion,
		}},
		"historical-only-compose": {{
			ID:            "historical-run",
			CapabilityKey: TaskWorkflowComposeText,
			PromptVersion: "shadow-task-v6",
		}},
	}

	selection := taskShadowReleaseGateIndexSelectionExcluding(
		episodes,
		runs,
		map[string]int{
			"older-complete-analyze":  1,
			"historical-only-compose": 1,
		},
		nil,
	)
	if selection.FirstEligibleEpisodeID != "newer-pending-compose" ||
		selection.FirstCurrentEpisodeID != "newer-current-compose" ||
		!reflect.DeepEqual(
			selection.PossibleCompleteEpisodeIDs,
			[]string{"older-complete-analyze"},
		) {
		t.Fatalf("release index selection=%+v", selection)
	}
}

func TestTaskShadowReleaseGateLearningRunIncludesExplicitExclusion(t *testing.T) {
	learning := TaskShadowDebugLearning{Runs: []ShadowTaskLearningRun{{
		ID:              "no-candidate-learning-run",
		PolicyVersion:   ShadowTaskLearningPolicyVersion,
		AuthorityEffect: ShadowTaskLearningAuthorityNone,
		Projection: ShadowTaskLearningProjection{
			PolicyVersion:   ShadowTaskLearningPolicyVersion,
			AuthorityEffect: ShadowTaskLearningAuthorityNone,
			ExcludedEvaluations: []ShadowTaskLearningExclusion{{
				EvaluationID: "excluded-evaluation",
				Reason:       "insufficient source-backed evidence",
			}},
		},
	}}}
	if runID := taskShadowReleaseGateLearningRunID(
		learning,
		[]string{"excluded-evaluation"},
	); runID != "no-candidate-learning-run" {
		t.Fatalf("explicit exclusion learning run=%q", runID)
	}
}

func TestTaskShadowDebugSnapshotIsBounded(t *testing.T) {
	store := newTaskShadowDebugTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	for index, id := range []string{"debug-episode-old", "debug-episode-mid", "debug-episode-new"} {
		input := taskShadowDebugEpisodeInput(
			id,
			int64(1_000+index*1_000),
			false,
		)
		if id == "debug-episode-old" {
			input.InitialHypothesis.TaskHypothesis =
				"Compose a customer update"
			input.InitialHypothesis.DesiredResultHypothesis =
				"A concise update ready to send"
		}
		if _, added, err := store.CreateTaskEpisode(input); err != nil || !added {
			t.Fatalf("create %s: added=%v err=%v", id, added, err)
		}
	}
	recentEvidenceAt := time.Now().UnixMilli()
	if _, _, err := store.AppendTaskEpisodeEvidence(TaskEpisodeEvidence{
		TaskEpisodeID: "debug-episode-old",
		Role:          TaskEpisodeEvidenceAction,
		SourceKind:    "activity_event",
		SourceID:      "debug-recent-generic-action",
		Summary:       "A recent generic action was associated with the old task.",
		ObservedAt:    recentEvidenceAt,
		CreatedAt:     recentEvidenceAt,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.GetTaskShadowDebugSnapshot(2)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Limit != 2 || len(snapshot.Episodes) != 2 {
		t.Fatalf("bounded snapshot=%+v", snapshot)
	}
	if snapshot.LifecycleHealth.EpisodeCount != 3 ||
		snapshot.LifecycleHealth.OpenCount != 3 ||
		snapshot.LifecycleHealth.OverdueOpenCount != 3 ||
		snapshot.LifecycleHealth.StatusCounts[TaskEpisodeStatusCandidate] != 3 ||
		snapshot.LifecycleHealth.PolicyVersion != taskLifecycleExpiryPolicyVersion ||
		snapshot.LifecycleHealth.EvidenceRenews ||
		snapshot.LifecycleHealth.ActivitySourcePolicy !=
			taskLifecycleActivitySourcePolicy {
		t.Fatalf("complete lifecycle health=%+v", snapshot.LifecycleHealth)
	}
	if snapshot.Episodes[0].ID != "debug-episode-new" ||
		snapshot.Episodes[1].ID != "debug-episode-mid" {
		t.Fatalf("snapshot order=%+v", snapshot.Episodes)
	}
	if snapshot.QualityCampaign.Counts.DetectedEpisodes != 1 {
		t.Fatalf(
			"bounded episode list truncated quality evidence: %+v",
			snapshot.QualityCampaign.Counts,
		)
	}
	capped, err := app.GetTaskShadowDebugSnapshot(taskShadowDebugMaxLimit + 1_000)
	if err != nil {
		t.Fatal(err)
	}
	if capped.Limit != taskShadowDebugMaxLimit || len(capped.Episodes) != 3 {
		t.Fatalf("maximum bound snapshot=%+v", capped)
	}
	for _, episode := range capped.Episodes {
		if episode.Status != TaskEpisodeStatusCandidate {
			t.Fatalf("debugger read mutated lifecycle: %+v", episode)
		}
	}
}

func TestTaskShadowDebugContextStatsPreserveFrozenManifestSemantics(t *testing.T) {
	store := newTaskShadowDebugTestStore(t)
	input := taskShadowDebugEpisodeInput("debug-context-stats", 1_000, false)
	input.FrozenContext.Manifest = append(
		input.FrozenContext.Manifest,
		TaskContextManifestEntry{
			Ref:             "debug-context-stats:activity-snapshot",
			Section:         TaskContextSectionActivity,
			SourceKind:      taskContextSourceActivitySnapshotKind,
			SourceID:        "activity-snapshot-1",
			ObservedAt:      input.InferredStartAt,
			EvidenceClass:   TaskContextEvidenceObservedUser,
			PrivacyDecision: TaskContextPrivacyRedact,
			Included:        true,
			ContentHash:     "activity-snapshot-hash",
			IncludedChars:   32,
		},
	)
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create context-stats episode: added=%v err=%v", added, err)
	}
	stats, err := taskShadowDebugContextStats(store, []string{episode.ID})
	if err != nil {
		t.Fatal(err)
	}
	included, excluded := taskShadowDebugManifestCounts(
		episode.FrozenContext.Manifest,
	)
	actual := stats[episode.ID]
	if actual.IncludedCount != included ||
		actual.ExcludedCount != excluded ||
		!actual.HasFrozenSourceContext {
		t.Fatalf(
			"relational context stats=%+v, want included=%d excluded=%d source=true",
			actual,
			included,
			excluded,
		)
	}
}

func TestTaskShadowDebugCompactIndexMatchesDetailedSummary(t *testing.T) {
	graph := seedTaskShadowDebugGraph(t)
	expected, err := taskShadowDebugEpisodeSummary(graph.Store, graph.Episode)
	if err != nil {
		t.Fatal(err)
	}
	items, err := listTaskShadowDebugEpisodeSummaries(
		graph.Store,
		taskShadowDebugMaxLimit,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("compact summaries=%d, want 1", len(items))
	}
	if !reflect.DeepEqual(items[0], expected) {
		t.Fatalf(
			"compact summary drifted from detailed contract\ncompact=%+v\ndetailed=%+v",
			items[0],
			expected,
		)
	}
	health, err := taskShadowLifecycleHealth(graph.Store, 4_000, items)
	if err != nil {
		t.Fatal(err)
	}
	if health.PairStateCounts[expected.LatestPairState] != 1 {
		t.Fatalf("compact lifecycle pair states=%+v", health.PairStateCounts)
	}
}

func TestTaskShadowDebugHistoricalSummaryPreservesGenerationState(t *testing.T) {
	store := newTaskShadowDebugTestStore(t)
	input := taskShadowDebugEpisodeInput("debug-historical-summary", 1_000, false)
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create historical-summary episode: added=%v err=%v", added, err)
	}
	run := newShadowRunFixture(
		"debug-historical-summary-run",
		"debug-historical-summary-decision",
	)
	run.Mode = "task_mirror"
	run.TaskEpisodeID = episode.ID
	run.TaskContextHash = episode.FrozenContext.SHA256
	run.CapabilityKey = TaskWorkflowComposeText
	run.PromptVersion = "shadow-task-v6"
	if _, added, err := store.CreateShadowRun(run); err != nil || !added {
		t.Fatalf("create historical-summary run: added=%v err=%v", added, err)
	}

	items, err := listTaskShadowDebugEpisodeSummaries(
		store,
		taskShadowDebugMaxLimit,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 ||
		items[0].LatestPairWorkflow != TaskWorkflowComposeText ||
		items[0].LatestPairState != ShadowTaskPairStateHistoricalGeneration ||
		!reflect.DeepEqual(
			items[0].LatestPairReasons,
			[]string{"historical_shadow_prompt_generation"},
		) {
		t.Fatalf("historical compact summary=%+v", items)
	}
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != items[0].LatestPairState ||
		readiness.Workflow != items[0].LatestPairWorkflow ||
		!containsString(
			readiness.Reasons,
			items[0].LatestPairReasons[0],
		) {
		t.Fatalf(
			"historical compact summary drifted from detail: summary=%+v readiness=%+v",
			items[0],
			readiness,
		)
	}
}

func TestTaskShadowDebugLatestRunIsNotBoundedToOldestRuns(t *testing.T) {
	store := newTaskShadowDebugTestStore(t)
	input := taskShadowDebugEpisodeInput("debug-latest-run", 1_000, false)
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create episode: added=%v err=%v", added, err)
	}

	const runCount = taskShadowDebugMaxRuns + 1
	for index := 0; index < runCount; index++ {
		run := newShadowRunFixture(
			fmt.Sprintf("debug-latest-run-%03d", index),
			fmt.Sprintf("debug-latest-decision-%03d", index),
		)
		run.Mode = "task_mirror"
		run.TaskEpisodeID = episode.ID
		run.TaskContextHash = episode.FrozenContext.SHA256
		run.CreatedAt = int64(10_000 + index)
		if _, added, err := store.CreateShadowRun(run); err != nil || !added {
			t.Fatalf("create run %d: added=%v err=%v", index, added, err)
		}
	}

	items, err := listTaskShadowDebugEpisodeSummaries(
		store,
		taskShadowDebugMaxLimit,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].LinkedShadowRunCount != runCount ||
		len(items[0].LinkedShadowRunIDs) != 20 ||
		items[0].LinkedShadowRunIDs[0] != "debug-latest-run-081" ||
		items[0].LinkedShadowRunIDs[19] != "debug-latest-run-100" {
		t.Fatalf("bounded compact run index=%+v", items)
	}
	detailed, err := taskShadowDebugEpisodeSummary(store, episode)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(items[0], detailed) {
		t.Fatalf(
			"bounded compact summary drifted\ncompact=%+v\ndetailed=%+v",
			items[0],
			detailed,
		)
	}
}

func TestTaskShadowDebugDetailLinksLedgersAndRedactsPrivatePayloads(t *testing.T) {
	graph := seedTaskShadowDebugGraph(t)
	detail, err := graph.App.GetTaskShadowDebugDetail(
		graph.Episode.ID,
		graph.Run.ID,
		20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Episode.Episode.ID != graph.Episode.ID ||
		detail.Episode.Episode.CurrentStatus != TaskEpisodeStatusCandidate ||
		detail.SelectedShadowRunID != graph.Run.ID {
		t.Fatalf("episode selection=%+v", detail)
	}
	lease := detail.Episode.Episode.ObservationLease
	if lease.PolicyVersion != taskLifecycleExpiryPolicyVersion ||
		lease.Status != TaskEpisodeStatusCandidate ||
		!lease.Open || !lease.Due || lease.EvidenceRenews ||
		lease.AnchorKind != "state_transition" ||
		lease.AnchorAt != graph.Episode.DetectedAt ||
		lease.AnchorTransitionID == "" ||
		lease.DeadlineAt != graph.Episode.DetectedAt+taskLifecycleCandidateHorizon.Milliseconds() {
		t.Fatalf("task observation lease=%+v", lease)
	}
	if len(detail.Runs) != 1 ||
		detail.Runs[0].Run.ID != graph.Run.ID ||
		detail.Runs[0].Status != "evaluated" ||
		len(detail.Runs[0].ExecutionAttempts) != 1 ||
		len(detail.Runs[0].Evaluations) != 1 {
		t.Fatalf("run linkage=%+v", detail.Runs)
	}
	if detail.Runs[0].ExecutionAttempts[0].Attempt.ID != graph.Execution.ID ||
		detail.Runs[0].Evaluations[0].Evaluation.ID != graph.Evaluation.ID {
		t.Fatalf("execution/evaluation linkage=%+v", detail.Runs[0])
	}
	if detail.Reasoning.Version != taskShadowReasoningDebugVersion ||
		detail.Reasoning.Counts.OnsetHypotheses != 1 ||
		detail.Reasoning.Counts.ShadowHypotheses != 1 ||
		detail.Reasoning.Counts.ObservedReasoning != 1 ||
		detail.Reasoning.Counts.KnownComparisons != 1 ||
		detail.Reasoning.Counts.UnknownComparisons != 0 ||
		detail.Reasoning.Coverage != "supported" {
		t.Fatalf("reasoning counts=%+v coverage=%q", detail.Reasoning.Counts, detail.Reasoning.Coverage)
	}
	if len(detail.Reasoning.OnsetHypotheses) != 1 ||
		!detail.Reasoning.OnsetHypotheses[0].ProvenanceResolved ||
		detail.Reasoning.OnsetHypotheses[0].Basis !=
			TaskReasoningBasisContextualInference {
		t.Fatalf("onset reasoning=%+v", detail.Reasoning.OnsetHypotheses)
	}
	onsetReasoning := detail.Reasoning.OnsetHypotheses[0]
	if onsetReasoning.EvidenceRefs == nil || onsetReasoning.ProfileRefs == nil ||
		onsetReasoning.ContradictionRefs == nil {
		t.Fatalf("onset reasoning collections must serialize as arrays: %+v", onsetReasoning)
	}
	if len(detail.Reasoning.Observed) != 1 ||
		!detail.Reasoning.Observed[0].Valid ||
		detail.Reasoning.Observed[0].Kind != TaskLifecycleReasoningCorrection ||
		detail.Reasoning.Observed[0].EvidenceID != graph.ReasoningEvidence.ID {
		t.Fatalf("observed reasoning=%+v", detail.Reasoning.Observed)
	}
	if detail.Reasoning.Observed[0].EvidenceRefs == nil {
		t.Fatalf("observed reasoning references must serialize as an array: %+v", detail.Reasoning.Observed[0])
	}
	if len(detail.Reasoning.Comparisons) != 1 ||
		!detail.Reasoning.Comparisons[0].Current ||
		!detail.Reasoning.Comparisons[0].PredictionBound ||
		!detail.Reasoning.Comparisons[0].CitationsValid ||
		detail.Reasoning.Comparisons[0].Unknown ||
		detail.Reasoning.Comparisons[0].Score == nil {
		t.Fatalf("reasoning comparison=%+v", detail.Reasoning.Comparisons)
	}
	comparison := detail.Reasoning.Comparisons[0]
	if comparison.PredictedHypotheses == nil || comparison.SupportedEvidenceRefs == nil ||
		comparison.EvidenceRefs == nil {
		t.Fatalf("reasoning comparison collections must serialize as arrays: %+v", comparison)
	}
	if len(detail.Reasoning.Predictions) != 1 ||
		detail.Reasoning.Predictions[0].Hypotheses == nil {
		t.Fatalf("reasoning prediction hypotheses must serialize as an array: %+v", detail.Reasoning.Predictions)
	}
	if versions := detail.Reasoning.Versions; len(versions.OnsetPrompts) != 1 ||
		!versions.OnsetPrompts[0].Current || len(versions.LifecyclePrompts) != 1 ||
		!versions.LifecyclePrompts[0].Current || len(versions.ShadowPrompts) != 1 ||
		!versions.ShadowPrompts[0].Current || len(versions.Comparators) != 1 ||
		!versions.Comparators[0].Current || len(versions.Evaluators) != 1 ||
		!versions.Evaluators[0].Current {
		t.Fatalf("reasoning version lineage=%+v", versions)
	}
	if len(detail.TaskAgentAttempts) != 4 {
		t.Fatalf("task agent attempts=%+v", detail.TaskAgentAttempts)
	}
	phases := make(map[string]bool)
	for _, attempt := range detail.TaskAgentAttempts {
		phases[attempt.Phase] = true
	}
	for _, phase := range []string{
		TaskAgentAttemptPhaseOnset,
		TaskAgentAttemptPhaseLifecycle,
		TaskAgentAttemptPhaseEvaluation,
		TaskAgentAttemptPhaseLearning,
	} {
		if !phases[phase] {
			t.Errorf("missing exact %s model attempt", phase)
		}
	}
	if len(detail.Learning.Runs) != 1 ||
		detail.Learning.Runs[0].ID != graph.LearningRun.ID ||
		len(detail.Learning.Revisions) != 1 ||
		detail.Learning.Revisions[0].Candidate.RevisionID != graph.RevisionID ||
		len(detail.Learning.Materializations) != 1 ||
		detail.Learning.Materializations[0].ID != graph.Materialization.ID {
		t.Fatalf("learning linkage=%+v", detail.Learning)
	}
	if len(detail.Correlations.ShadowRunIDs) != 1 ||
		detail.Correlations.ShadowRunIDs[0] != graph.Run.ID ||
		len(detail.Correlations.ExecutionAttemptIDs) != 1 ||
		detail.Correlations.ExecutionAttemptIDs[0] != graph.Execution.ID ||
		len(detail.Correlations.EvaluationIDs) != 1 ||
		detail.Correlations.EvaluationIDs[0] != graph.Evaluation.ID ||
		len(detail.Correlations.LearningRevisionIDs) != 1 ||
		detail.Correlations.LearningRevisionIDs[0] != graph.RevisionID {
		t.Fatalf("correlations=%+v", detail.Correlations)
	}
	var contextExclusion, learningExclusion bool
	for _, exclusion := range detail.Exclusions {
		switch {
		case exclusion.Stage == "context_selection" &&
			strings.Contains(exclusion.Reason, "hidden shadow"):
			contextExclusion = true
		case exclusion.Stage == "learning" &&
			exclusion.Reason == "insufficient repeated evidence":
			learningExclusion = true
		}
	}
	if !contextExclusion || !learningExclusion {
		t.Fatalf("exclusion reasons=%+v", detail.Exclusions)
	}

	payload, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(payload)
	for _, secret := range []string{
		taskShadowDebugContextSecret,
		taskShadowDebugEvidenceSecret,
		taskShadowDebugExecutionSecret,
		taskShadowDebugArtifactSecret,
		taskShadowDebugComparatorSecret,
		taskShadowDebugEvaluatorSecret,
		"duplicate private observation content",
		"private duplicate execution result",
	} {
		if strings.Contains(serialized, secret) {
			t.Errorf("private payload %q leaked through task shadow debugger", secret)
		}
	}
	for _, exactDiagnostic := range []string{
		taskShadowDebugAttemptPrompt,
		taskShadowDebugAttemptRaw,
		taskShadowDebugAttemptError,
	} {
		if !strings.Contains(serialized, exactDiagnostic) {
			t.Errorf("exact model-attempt diagnostic %q was removed", exactDiagnostic)
		}
	}

	resolvedByRun, err := graph.App.GetTaskShadowDebugDetail("", graph.Run.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedByRun.Episode.Episode.ID != graph.Episode.ID {
		t.Fatalf("run-only resolution=%+v", resolvedByRun.Episode.Episode)
	}
	otherInput := taskShadowDebugEpisodeInput("different-debug-episode", 5_000, false)
	other, added, err := graph.Store.CreateTaskEpisode(otherInput)
	if err != nil || !added {
		t.Fatalf("create other episode: added=%v err=%v", added, err)
	}
	if _, err := graph.App.GetTaskShadowDebugDetail(other.ID, graph.Run.ID, 20); err == nil ||
		!strings.Contains(err.Error(), "different task episode") {
		t.Fatalf("mismatched linkage error=%v", err)
	}
}

func TestTaskShadowDebugReasoningDoesNotResolvePostOnsetEvidence(t *testing.T) {
	const cutoffAt int64 = 1_000
	detail := TaskEpisodeDetail{
		Episode: TaskEpisode{
			FrozenContext: TaskEpisodeFrozenContext{CutoffAt: cutoffAt},
			LatestHypothesis: TaskEpisodeHypothesisRevision{
				ReasoningHypotheses: []TaskEpisodeReasoningHypothesis{
					{
						Statement:   "The visible draft likely needs revision.",
						Confidence:  0.3,
						Basis:       TaskReasoningBasisContextualInference,
						EvidenceIDs: []string{"evidence-before-cutoff"},
					},
					{
						Statement:   "A later result explains the task.",
						Confidence:  0.3,
						Basis:       TaskReasoningBasisContextualInference,
						EvidenceIDs: []string{"evidence-after-cutoff"},
					},
				},
			},
		},
		Evidence: []TaskEpisodeEvidence{
			{ID: "evidence-before-cutoff", ObservedAt: cutoffAt},
			{ID: "evidence-after-cutoff", ObservedAt: cutoffAt + 1},
		},
	}
	runs := []TaskShadowDebugRun{{Run: ShadowRun{
		ID:            "shadow-post-onset-provenance",
		PromptVersion: shadowTaskPromptVersion,
		Prediction: ShadowPrediction{ReasoningHypotheses: []ShadowReasoningHypothesis{
			{
				Statement:    "The visible draft likely needs revision.",
				Confidence:   0.3,
				Basis:        TaskReasoningBasisContextualInference,
				EvidenceRefs: []string{"observed:evidence-before-cutoff"},
			},
			{
				Statement:    "A later result explains the task.",
				Confidence:   0.3,
				Basis:        TaskReasoningBasisContextualInference,
				EvidenceRefs: []string{"observed:evidence-after-cutoff"},
			},
		}},
	}}}

	reasoning := taskShadowDebugReasoning(detail, runs, nil, 20)
	if len(reasoning.OnsetHypotheses) != 2 ||
		!reasoning.OnsetHypotheses[0].ProvenanceResolved ||
		reasoning.OnsetHypotheses[1].ProvenanceResolved {
		t.Fatalf("onset hypotheses=%+v", reasoning.OnsetHypotheses)
	}
	if len(reasoning.Predictions) != 1 ||
		len(reasoning.Predictions[0].Hypotheses) != 2 ||
		!reasoning.Predictions[0].Hypotheses[0].ProvenanceResolved ||
		reasoning.Predictions[0].Hypotheses[1].ProvenanceResolved {
		t.Fatalf("shadow predictions=%+v", reasoning.Predictions)
	}
}

func taskShadowDebugReadFingerprint(t *testing.T, store *ActivityStore) string {
	t.Helper()
	tables := []string{
		"task_episodes",
		"task_episode_context_snapshots",
		"task_episode_hypotheses",
		"task_episode_evidence",
		"task_episode_transitions",
		"task_episode_links",
		"shadow_runs",
		"shadow_run_evidence",
		"shadow_run_observations",
		"shadow_run_evaluations",
		"shadow_execution_attempts",
		"task_agent_attempts",
		"task_agent_attempt_events",
		"shadow_task_learning_runs",
		"shadow_task_learning_revisions",
		"shadow_task_learning_materializations",
		"work_pattern_profile_snapshots_v1",
		"work_pattern_profile_evidence_v1",
		"self_model_entries",
		"authority_grants",
		"initiatives",
		"agency_origination_candidates_v1",
		"agency_origination_permission_requests_v1",
	}
	type fingerprint struct {
		Table    string `json:"table"`
		Count    int    `json:"count"`
		MaxRowID int64  `json:"maxRowId"`
	}
	items := make([]fingerprint, 0, len(tables))
	for _, table := range tables {
		item := fingerprint{Table: table}
		query := "SELECT COUNT(*), COALESCE(MAX(rowid), 0) FROM " + table
		if err := store.db.QueryRow(query).Scan(&item.Count, &item.MaxRowID); err != nil {
			t.Fatalf("fingerprint %s: %v", table, err)
		}
		items = append(items, item)
	}
	payload, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestTaskShadowDebugReasoningReadsAreSideEffectFree(t *testing.T) {
	graph := seedTaskShadowDebugGraph(t)
	before := taskShadowDebugReadFingerprint(t, graph.Store)
	for range 3 {
		if _, err := graph.App.GetTaskShadowDebugSnapshot(200); err != nil {
			t.Fatal(err)
		}
		if _, err := graph.App.GetTaskShadowDebugDetail(
			graph.Episode.ID,
			graph.Run.ID,
			200,
		); err != nil {
			t.Fatal(err)
		}
	}
	after := taskShadowDebugReadFingerprint(t, graph.Store)
	if after != before {
		t.Fatalf("debugger read changed durable state\nbefore=%s\nafter=%s", before, after)
	}
}

func TestTaskShadowDebugDetailListsAreBounded(t *testing.T) {
	graph := seedTaskShadowDebugGraph(t)
	detail, err := graph.App.GetTaskShadowDebugDetail(graph.Episode.ID, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Limit != 2 ||
		len(detail.Episode.Hypotheses) > 2 ||
		len(detail.Episode.Evidence) > 2 ||
		len(detail.Episode.Transitions) > 2 ||
		len(detail.Episode.Links) > 2 ||
		len(detail.Episode.Episode.FrozenContext.Manifest) > 2 ||
		len(detail.Runs) > 2 ||
		len(detail.TaskAgentAttempts) > 2 ||
		len(detail.Learning.Runs) > 2 ||
		len(detail.Learning.Revisions) > 2 ||
		len(detail.Learning.Materializations) > 2 ||
		len(detail.Exclusions) > 2 {
		t.Fatalf("detail exceeded requested bounds: %+v", detail)
	}
	for _, run := range detail.Runs {
		if len(run.Evidence) > 2 ||
			len(run.Observations) > 2 ||
			len(run.ExecutionAttempts) > 2 ||
			len(run.Evaluations) > 2 {
			t.Fatalf("nested run exceeded requested bounds: %+v", run)
		}
	}
}

func TestTaskShadowDebugDetailPrioritizesIncludedManifestEvidence(t *testing.T) {
	store := newTaskShadowDebugTestStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	input := taskShadowDebugEpisodeInput("debug-prioritized-manifest", 5_000, false)
	input.FrozenContext.Manifest = []TaskContextManifestEntry{
		{Ref: "excluded-1", Included: false, Reason: "after_task_cutoff"},
		{Ref: "excluded-2", Included: false, Reason: "budget_exhausted"},
		{Ref: "included-1", Included: true},
		{Ref: "included-2", Included: true},
	}
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create episode: added=%v err=%v", added, err)
	}
	detail, err := app.GetTaskShadowDebugDetail(episode.ID, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	context := detail.Episode.Episode.FrozenContext
	if context.ManifestEntryCount != 4 ||
		context.ManifestIncludedCount != 2 ||
		context.ManifestExcludedCount != 2 ||
		!context.ManifestTruncated {
		t.Fatalf("manifest totals=%+v", context)
	}
	if len(context.Manifest) != 2 ||
		context.Manifest[0].Ref != "included-1" ||
		context.Manifest[1].Ref != "included-2" {
		t.Fatalf("prioritized manifest=%+v", context.Manifest)
	}
}

func listTaskShadowDebugEpisodeSummaries(
	store *ActivityStore,
	limit int,
) ([]TaskShadowDebugEpisodeSummary, error) {
	indexRows, err := taskShadowDebugEpisodeIndexRows(store, limit)
	if err != nil {
		return nil, err
	}
	episodeIDs := taskShadowDebugEpisodeIndexIDs(indexRows)
	runs, runCounts, err := taskShadowDebugIndexRuns(store, episodeIDs)
	if err != nil {
		return nil, err
	}
	return taskShadowDebugEpisodeSummariesFromIndex(
		store,
		indexRows,
		runs,
		runCounts,
	)
}

func taskShadowDebugEpisodeSummary(
	store *ActivityStore,
	episode TaskEpisode,
) (TaskShadowDebugEpisodeSummary, error) {
	runs, err := listTaskShadowDebugRuns(store, episode.ID, 20)
	if err != nil {
		return TaskShadowDebugEpisodeSummary{}, err
	}
	runIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
	}
	runCount, attemptCount, evaluationCount, err := taskShadowDebugEpisodeCounts(
		store,
		episode.ID,
	)
	if err != nil {
		return TaskShadowDebugEpisodeSummary{}, err
	}
	included, excluded := taskShadowDebugManifestCounts(episode.FrozenContext.Manifest)
	latestPair := ShadowTaskPairReadiness{
		Reasons:                      []string{},
		UserResultEvidenceIDs:        []string{},
		InvalidUserResultEvidenceIDs: []string{},
		ShadowArtifactDigests:        []string{},
		EvaluationIDs:                []string{},
	}
	if len(runs) > 0 {
		latestPair, err = BuildShadowTaskPairReadiness(
			store,
			runs[len(runs)-1].ID,
		)
		if err != nil {
			latestPair = ShadowTaskPairReadiness{
				Workflow: runs[len(runs)-1].CapabilityKey,
				State:    "readiness_error",
				Reasons:  []string{err.Error()},
			}
		}
	} else {
		rollout := taskShadowDebugProductionRollout(episode)
		latestPair.Workflow = rollout.Workflow
		if rollout.Eligible {
			latestPair.State = "shadow_pending"
			latestPair.Reasons = []string{
				"eligible production task has no shadow run",
			}
		} else {
			latestPair.State = rollout.State
			latestPair.Reasons = []string{rollout.Reason}
		}
	}
	return TaskShadowDebugEpisodeSummary{
		ID:                       episode.ID,
		Status:                   episode.CurrentStatus,
		TaskHypothesis:           episode.LatestHypothesis.TaskHypothesis,
		DesiredResultHypothesis:  episode.LatestHypothesis.DesiredResultHypothesis,
		TaskConfidence:           episode.LatestHypothesis.TaskConfidence,
		BoundaryConfidence:       episode.LatestHypothesis.BoundaryConfidence,
		DesiredResultConfidence:  episode.LatestHypothesis.DesiredResultConfidence,
		InferredStartAt:          episode.InferredStartAt,
		DetectedAt:               episode.DetectedAt,
		ContextSnapshotID:        episode.FrozenContext.ID,
		ContextHash:              episode.FrozenContext.SHA256,
		ContextIncludedCount:     included,
		ContextExcludedCount:     excluded,
		LinkedShadowRunIDs:       runIDs,
		LinkedShadowRunCount:     runCount,
		TaskAgentAttemptCount:    attemptCount,
		PersistedEvaluationCount: evaluationCount,
		LatestPairWorkflow:       latestPair.Workflow,
		LatestPairState:          latestPair.State,
		LatestPairReasons:        latestPair.Reasons,
		CreatedAt:                episode.CreatedAt,
	}, nil
}

func taskShadowDebugEpisodeCounts(
	store *ActivityStore,
	taskEpisodeID string,
) (int, int, int, error) {
	var runCount, attemptCount, evaluationCount int
	if err := store.db.QueryRow(`
		SELECT COUNT(*)
		FROM shadow_runs
		WHERE mode = 'task_mirror' AND task_episode_id = ?`,
		taskEpisodeID,
	).Scan(&runCount); err != nil {
		return 0, 0, 0, err
	}
	if err := store.db.QueryRow(`
		SELECT COUNT(*)
		FROM task_agent_attempts
		WHERE task_episode_id = ?
			OR shadow_run_id IN (
				SELECT id FROM shadow_runs
				WHERE mode = 'task_mirror' AND task_episode_id = ?
			)
			OR shadow_evaluation_id IN (
				SELECT evaluation.id
				FROM shadow_run_evaluations evaluation
				JOIN shadow_runs run ON run.id = evaluation.shadow_run_id
				WHERE run.mode = 'task_mirror' AND run.task_episode_id = ?
			)`,
		taskEpisodeID,
		taskEpisodeID,
		taskEpisodeID,
	).Scan(&attemptCount); err != nil {
		return 0, 0, 0, err
	}
	if err := store.db.QueryRow(`
		SELECT COUNT(*)
		FROM shadow_run_evaluations evaluation
		JOIN shadow_runs run ON run.id = evaluation.shadow_run_id
		WHERE run.mode = 'task_mirror' AND run.task_episode_id = ?`,
		taskEpisodeID,
	).Scan(&evaluationCount); err != nil {
		return 0, 0, 0, err
	}
	return runCount, attemptCount, evaluationCount, nil
}

func taskShadowDebugManifestCounts(manifest []TaskContextManifestEntry) (int, int) {
	var included, excluded int
	for _, item := range manifest {
		if item.Included {
			included++
		} else {
			excluded++
		}
	}
	return included, excluded
}
