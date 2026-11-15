package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"wails-gui/storage"
	"yansu-agent/agent"
)

func taskShadowContinuationItemForRun(
	t *testing.T,
	snapshot TaskShadowContinuationSnapshot,
	runID string,
) TaskShadowContinuationItem {
	t.Helper()
	for _, item := range snapshot.Items {
		if item.ShadowRunID == runID {
			return item
		}
	}
	t.Fatalf("continuation item for run %q not found in %+v", runID, snapshot)
	return TaskShadowContinuationItem{}
}

func createPendingTaskShadowContinuationRun(
	t *testing.T,
	store *ActivityStore,
	model string,
) (TaskEpisode, ShadowRun) {
	t.Helper()
	episode := createComposeShadowTaskEpisodeFixture(t, store)
	run, added, err := store.CreateShadowTaskRunFromEpisode(
		episode,
		model,
		1_200,
	)
	if err != nil || !added {
		t.Fatalf("create continuation run: added=%v err=%v", added, err)
	}
	return episode, run
}

func completeTaskShadowContinuationEpisode(
	t *testing.T,
	store *ActivityStore,
	episode TaskEpisode,
	content string,
) TaskEpisode {
	t.Helper()
	destination := taskLifecycleTestResultDestination()
	input := TaskLifecycleInput{
		SourceKind:     "activity",
		SourceID:       "continuation-window",
		PrefixCutoffAt: 1_100,
		CutoffAt:       2_000,
		Episodes: []TaskLifecycleEpisodeSnapshot{{
			EpisodeID:          episode.ID,
			CurrentStatus:      episode.CurrentStatus,
			InferredStartedAt:  episode.InferredStartAt,
			Task:               episode.LatestHypothesis.TaskHypothesis,
			DesiredResult:      episode.LatestHypothesis.DesiredResultHypothesis,
			TaskConfidence:     episode.LatestHypothesis.TaskConfidence,
			BoundaryConfidence: episode.LatestHypothesis.BoundaryConfidence,
			EvidenceRefs:       []string{"prefix:onset"},
		}},
		PrefixEvidence: []TaskLifecycleEvidence{{
			Ref:        "prefix:onset",
			Kind:       "onset",
			SourceKind: "activity_event",
			SourceID:   "continuation-onset",
			OccurredAt: 1_000,
			Summary:    "The user began composing the customer update.",
		}},
		SuffixEvidence: []TaskLifecycleEvidence{
			{
				Ref:         "suffix:text",
				Kind:        TaskLifecycleEvidenceTextCommit,
				SourceKind:  "activity_event",
				SourceID:    "continuation-text",
				OccurredAt:  1_500,
				Summary:     "The user committed the customer update.",
				ContentKind: TaskLifecycleContentCommittedText,
				Content:     content,
				Actor:       TaskLifecycleActorUser,
				Destination: destination,
			},
			{
				Ref:        "suffix:send",
				Kind:       "sent",
				SourceKind: "activity_event",
				SourceID:   "continuation-send",
				OccurredAt: 1_600,
				Summary:    "The user sent the customer update.",
				Actor:      TaskLifecycleActorUser,
			},
		},
	}
	response := TaskLifecycleResponse{Observations: []TaskLifecycleObservation{{
		LocalKey:         "continuation-complete",
		Kind:             TaskLifecycleKindCompleted,
		EpisodeIDs:       []string{episode.ID},
		OccurredAt:       1_600,
		Confidence:       0.94,
		ResultSummary:    "The customer update was sent.",
		ResultConfidence: 0.91,
		EvidenceRefs:     []string{"suffix:text", "suffix:send"},
	}}}
	if _, err := PersistTaskLifecycleObservations(
		store,
		input,
		response,
		2_100,
	); err != nil {
		t.Fatalf("persist continuation lifecycle: %v", err)
	}
	completed, err := store.GetTaskEpisode(episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	return completed
}

func persistTaskShadowContinuationEvaluation(
	t *testing.T,
	store *ActivityStore,
	run ShadowRun,
) ShadowRunEvaluation {
	t.Helper()
	input, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	refs := sortedShadowTaskEvidenceIDs(input)
	if len(refs) == 0 {
		t.Fatal("continuation evaluation input has no evidence")
	}
	resultQuality := matchedTaskDimension(refs[0])
	resultQuality.Assessment = ShadowTaskQualityEquivalent
	output := ShadowTaskEvaluationOutput{
		Version:          ShadowTaskEvaluationOutputVersion,
		Summary:          "The observed and isolated results were compared.",
		Task:             matchedTaskDimension(refs[0]),
		Boundary:         matchedTaskDimension(refs[0]),
		Process:          matchedTaskDimension(refs[0]),
		ResultSimilarity: matchedTaskDimension(refs[0]),
		ResultQuality:    resultQuality,
		Timing:           matchedTaskDimension(refs[0]),
		Reasoning:        unknownTaskDimension(),
		Safety: ShadowTaskEvaluationDimension{
			Assessment:   ShadowTaskSafetySafe,
			Score:        shadowScore(1),
			Confidence:   1,
			Reason:       "The attempt remained isolated.",
			EvidenceRefs: []string{refs[0]},
		},
	}
	prompt, _, err := buildShadowTaskEvaluationPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	evaluation, added, err := PersistShadowTaskEvaluation(
		store,
		input,
		ShadowTaskEvaluationResult{
			EvaluatorVersion:  ShadowTaskEvaluatorVersion,
			ComparatorVersion: ShadowTaskComparatorVersion,
			InputVersion:      ShadowTaskEvaluationInputVersion,
			ComparatorPrompt:  prompt,
			ComparatorHash:    shadowTaskComparatorHash(prompt),
			EvaluatorModel:    "test-model",
			EvaluatorRaw:      `{"test":"continuation"}`,
			Evaluation:        &output,
		},
		2_200,
	)
	if err != nil || !added {
		t.Fatalf("persist continuation evaluation: added=%v err=%v", added, err)
	}
	return evaluation
}

func persistUnmaterializedTaskShadowLearning(
	t *testing.T,
	store *ActivityStore,
	capabilityKey string,
	createdAt int64,
) ShadowTaskLearningRun {
	t.Helper()
	records, err := store.ListShadowTaskLearningEvaluationRecords(capabilityKey)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := BuildShadowTaskLearningProjection(records)
	if err != nil {
		t.Fatal(err)
	}
	run, added, err := PersistShadowTaskLearningProjection(
		store,
		projection,
		createdAt,
	)
	if err != nil || !added {
		t.Fatalf("persist unmaterialized learning: added=%v err=%v", added, err)
	}
	return run
}

func TestTaskShadowContinuationSnapshotDerivesRecoveryStates(t *testing.T) {
	t.Run("execution pending", func(t *testing.T) {
		store := newTestStore(t)
		_, run := createPendingTaskShadowContinuationRun(t, store, "frozen-model")
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.State != taskShadowContinuationExecutionPending ||
			item.Action != taskShadowContinuationActionExecute ||
			snapshot.ExecutionPendingCount != 1 {
			t.Fatalf("execution continuation=%+v snapshot=%+v", item, snapshot)
		}
	})

	t.Run("awaiting terminal", func(t *testing.T) {
		store := newTestStore(t)
		_, run := createPendingTaskShadowContinuationRun(t, store, "frozen-model")
		app := &App{
			runner:   &shadowExecutionFakeRunner{},
			activity: &activityMonitorState{store: store},
		}
		if _, _, err := app.executeShadowCandidatePlan(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.State != taskShadowContinuationAwaitingTerminal ||
			item.Action != taskShadowContinuationActionNone ||
			snapshot.AwaitingTerminalCount != 1 {
			t.Fatalf("terminal continuation=%+v snapshot=%+v", item, snapshot)
		}
	})

	t.Run("evaluation pending", func(t *testing.T) {
		store := newTestStore(t)
		_, run, _ := createCompletedComposeTextPairFixture(t, store)
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.State != taskShadowContinuationEvaluationPending ||
			item.Action != taskShadowContinuationActionEvaluate ||
			snapshot.EvaluationPendingCount != 1 {
			t.Fatalf("evaluation continuation=%+v snapshot=%+v", item, snapshot)
		}
	})

	t.Run("learning pending then complete", func(t *testing.T) {
		store := newTestStore(t)
		_, run, _ := createCompletedComposeTextPairFixture(t, store)
		evaluation := persistTaskShadowContinuationEvaluation(t, store, run)

		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.State != taskShadowContinuationLearningPending ||
			item.Action != taskShadowContinuationActionEvaluate ||
			!reflect.DeepEqual(item.CurrentEvaluationIDs, []string{evaluation.ID}) {
			t.Fatalf("learning continuation=%+v snapshot=%+v", item, snapshot)
		}

		app := &App{activity: &activityMonitorState{store: store}}
		learning, added, err := app.BuildAndPersistShadowTaskLearning(
			run.CapabilityKey,
			3_100,
		)
		if err != nil || !added {
			t.Fatalf("persist continuation learning: added=%v err=%v", added, err)
		}
		snapshot, err = BuildTaskShadowContinuationSnapshot(store, 100, 3_200)
		if err != nil {
			t.Fatal(err)
		}
		item = taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.State != taskShadowContinuationComplete ||
			item.Action != taskShadowContinuationActionNone ||
			item.LearningRunID != learning.ID ||
			snapshot.CompleteCount != 1 {
			t.Fatalf("complete continuation=%+v snapshot=%+v", item, snapshot)
		}
	})

	t.Run("learning projection waits for terminal materialization", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		store := newTestStore(t)
		_, run, _ := createCompletedComposeTextPairFixture(t, store)
		evaluation := persistTaskShadowContinuationEvaluation(t, store, run)
		learning := persistUnmaterializedTaskShadowLearning(
			t,
			store,
			run.CapabilityKey,
			3_100,
		)

		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_200)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.State != taskShadowContinuationMaterializationPending ||
			item.Action != taskShadowContinuationActionMaterialize ||
			item.LearningRunID != learning.ID ||
			!reflect.DeepEqual(item.CurrentEvaluationIDs, []string{evaluation.ID}) ||
			item.LearningMaterialization == nil ||
			item.LearningMaterialization.Complete ||
			item.LearningMaterialization.MissingCount == 0 ||
			snapshot.MaterializationPendingCount != 1 {
			t.Fatalf("materialization-pending continuation=%+v snapshot=%+v", item, snapshot)
		}

		runner := &taskShadowContinuationRecoveryRunner{}
		app := &App{
			runner:   runner,
			activity: &activityMonitorState{store: store},
		}
		triager := NewActivityTriager(app, nil)
		recovery := triager.recoverTaskShadowContinuations(
			context.Background(),
			"materialization_recovery_test",
		)
		triager.wg.Wait()
		if !reflect.DeepEqual(
			recovery.MaterializationRecoveredRunIDs,
			[]string{learning.ID},
		) || len(recovery.ExecutionDispatchedIDs) != 0 ||
			len(recovery.EvaluationDispatchedIDs) != 0 ||
			len(recovery.Errors) != 0 {
			t.Fatalf("materialization recovery=%+v", recovery)
		}
		item = taskShadowContinuationItemForRun(
			t,
			mustTaskShadowContinuationSnapshot(t, store),
			run.ID,
		)
		if item.State != taskShadowContinuationComplete ||
			item.LearningMaterialization == nil ||
			!item.LearningMaterialization.Complete {
			t.Fatalf("materialized continuation=%+v", item)
		}
		phases, _ := runner.snapshot()
		if len(phases) != 0 {
			t.Fatalf("materialization recovery called the model: %v", phases)
		}

		replayed := triager.recoverTaskShadowContinuations(
			context.Background(),
			"materialization_replay_test",
		)
		if len(replayed.MaterializationRecoveredRunIDs) != 0 ||
			len(replayed.Errors) != 0 {
			t.Fatalf("idempotent materialization recovery=%+v", replayed)
		}
	})

	t.Run("explicit no-candidate learning exclusion is resolved", func(t *testing.T) {
		store := newTestStore(t)
		_, run, _ := createCompletedComposeTextPairFixture(t, store)
		evaluation := persistTaskShadowContinuationEvaluation(t, store, run)
		learning, added, err := PersistShadowTaskLearningProjection(
			store,
			ShadowTaskLearningProjection{
				PolicyVersion:   ShadowTaskLearningPolicyVersion,
				AuthorityEffect: ShadowTaskLearningAuthorityNone,
				EvaluationCount: 1,
				ExcludedEvaluations: []ShadowTaskLearningExclusion{{
					EvaluationID: evaluation.ID,
					Reason:       "insufficient source-backed evidence",
				}},
			},
			3_100,
		)
		if err != nil || !added {
			t.Fatalf("persist no-candidate learning: added=%v err=%v", added, err)
		}
		item := taskShadowContinuationItemForRun(
			t,
			mustTaskShadowContinuationSnapshot(t, store),
			run.ID,
		)
		if item.State != taskShadowContinuationComplete ||
			item.LearningRunID != learning.ID ||
			item.LearningMaterialization == nil ||
			!item.LearningMaterialization.Complete ||
			item.LearningMaterialization.CandidateCount != 0 {
			t.Fatalf("no-candidate continuation=%+v", item)
		}
	})

	t.Run("durable failure does not retry", func(t *testing.T) {
		store := newTestStore(t)
		_, run := createPendingTaskShadowContinuationRun(t, store, "frozen-model")
		plan := *run.Prediction.CandidateExecutionPlan
		attempt, added, err := store.AddShadowExecutionAttempt(ShadowExecutionAttempt{
			ShadowRunID:    run.ID,
			IdempotencyKey: run.ID + ":" + shadowExecutionRuntimeVersion,
			RuntimeVersion: shadowExecutionRuntimeVersion,
			Status:         "error",
			Model:          run.Model,
			Prompt:         plan.Prompt,
			Plan:           plan,
			Error:          "durable test failure",
			StartedAt:      2_000,
			CompletedAt:    2_100,
			CreatedAt:      2_100,
		})
		if err != nil || !added {
			t.Fatalf("persist failed attempt: added=%v err=%v", added, err)
		}
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.State != taskShadowContinuationShadowFailed ||
			item.Action != taskShadowContinuationActionNone ||
			item.AttemptID != attempt.ID ||
			snapshot.BlockedCount != 1 {
			t.Fatalf("failed continuation=%+v snapshot=%+v", item, snapshot)
		}
	})
}

func TestTaskShadowContinuationLearningIndexIncludesNoCandidateReceipt(
	t *testing.T,
) {
	index := taskShadowContinuationLearningIndex([]ShadowTaskLearningRun{{
		ID:              "learning-with-no-candidate",
		PolicyVersion:   ShadowTaskLearningPolicyVersion,
		AuthorityEffect: ShadowTaskLearningAuthorityNone,
		Projection: ShadowTaskLearningProjection{
			PolicyVersion:   ShadowTaskLearningPolicyVersion,
			AuthorityEffect: ShadowTaskLearningAuthorityNone,
			ExcludedEvaluations: []ShadowTaskLearningExclusion{{
				EvaluationID: "evaluation-with-no-learning",
				Reason:       "no durable user pattern was supported",
			}},
		},
	}})
	if index["evaluation-with-no-learning"] != "learning-with-no-candidate" {
		t.Fatalf("learning index=%v", index)
	}
}

func TestTaskShadowContinuationFailsClosedBeforeRecoveredExecution(t *testing.T) {
	t.Run("missing frozen model", func(t *testing.T) {
		store := newTestStore(t)
		_, run := createPendingTaskShadowContinuationRun(t, store, "")
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.State != taskShadowContinuationBlocked ||
			item.Action != taskShadowContinuationActionNone ||
			!strings.Contains(item.Reason, "no frozen model") {
			t.Fatalf("missing-model continuation=%+v", item)
		}
	})

	t.Run("unsafe frozen context", func(t *testing.T) {
		store := newTestStore(t)
		input := taskEpisodeFixture(
			"unsafe-continuation-context",
			1_000,
			1_100,
			TaskEpisodeStatusCandidate,
		)
		input.InitialHypothesis.TaskHypothesis =
			"Convert the supplied email into a one-page document"
		input.InitialHypothesis.DesiredResultHypothesis =
			"A one-page document based on the supplied email"
		episode, added, err := store.CreateTaskEpisode(input)
		if err != nil || !added {
			t.Fatalf("create unsafe episode: added=%v err=%v", added, err)
		}
		run, added, err := store.CreateShadowTaskRunFromEpisode(
			episode,
			"frozen-model",
			1_200,
		)
		if err != nil || !added {
			t.Fatalf("create unsafe run: added=%v err=%v", added, err)
		}
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.State != taskShadowContinuationBlocked ||
			item.Action != taskShadowContinuationActionNone ||
			!strings.Contains(item.Reason, "frozen task context is unsafe") {
			t.Fatalf("unsafe-context continuation=%+v", item)
		}
	})

	t.Run("missing frozen plan", func(t *testing.T) {
		store := newTestStore(t)
		_, current := createPendingTaskShadowContinuationRun(
			t,
			store,
			"frozen-model",
		)
		withoutPlan := current
		withoutPlan.ID = ""
		withoutPlan.DecisionKey += ":without-plan"
		withoutPlan.SourceID += ":without-plan"
		withoutPlan.Prediction.CandidateExecutionPlan = nil
		withoutPlan.PredictionHash = ""
		withoutPlan.CreatedAt++
		withoutPlan, added, err := store.CreateShadowRun(withoutPlan)
		if err != nil || !added {
			t.Fatalf("create planless run: added=%v err=%v", added, err)
		}
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, withoutPlan.ID)
		if item.State != taskShadowContinuationBlocked ||
			item.Action != taskShadowContinuationActionNone ||
			!strings.Contains(item.Reason, "no executable frozen plan") {
			t.Fatalf("missing-plan continuation=%+v", item)
		}
	})

	t.Run("workflow outside registry", func(t *testing.T) {
		store := newTestStore(t)
		_, current := createPendingTaskShadowContinuationRun(
			t,
			store,
			"frozen-model",
		)
		outside := current
		outside.ID = ""
		outside.DecisionKey += ":outside"
		outside.SourceID += ":outside"
		outside.CapabilityKey = "browser.form.submit"
		outside.CreatedAt++
		outside, added, err := store.CreateShadowRun(outside)
		if err != nil || !added {
			t.Fatalf("create outside run: added=%v err=%v", added, err)
		}
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, outside.ID)
		if item.State != taskShadowContinuationBlocked ||
			item.Action != taskShadowContinuationActionNone ||
			!strings.Contains(item.Reason, "outside the closed production registry") {
			t.Fatalf("outside-workflow continuation=%+v", item)
		}
	})
}

func TestTaskShadowContinuationInvalidLaterResultOverridesOldEvaluation(
	t *testing.T,
) {
	store := newTestStore(t)
	episode, run, _ := createCompletedComposeTextPairFixture(t, store)
	evaluation := persistTaskShadowContinuationEvaluation(t, store, run)
	otherDestination := taskLifecycleTestResultDestination()
	otherDestination.WindowTitle = "Different customer thread"
	invalid := appendCurrentTaskResultEvidence(
		t,
		store,
		episode.ID,
		TaskWorkflowComposeText,
		"later-cross-destination-result",
		"A later result sent to a different destination.",
		otherDestination,
		2_500,
	)

	snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
	if err != nil {
		t.Fatal(err)
	}
	item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
	if item.State != taskShadowContinuationBlocked ||
		item.Action != taskShadowContinuationActionNone ||
		item.PairState != ShadowTaskPairStateUserResultUnavailable ||
		!reflect.DeepEqual(item.CurrentEvaluationIDs, []string{evaluation.ID}) ||
		!containsString(item.CurrentEvaluationIDs, evaluation.ID) ||
		!containsString(
			taskShadowPairReadinessForTest(t, store, run.ID).
				InvalidUserResultEvidenceIDs,
			invalid.ID,
		) {
		t.Fatalf("invalid provenance continuation=%+v snapshot=%+v", item, snapshot)
	}
}

func taskShadowPairReadinessForTest(
	t *testing.T,
	store *ActivityStore,
	runID string,
) ShadowTaskPairReadiness {
	t.Helper()
	readiness, err := BuildShadowTaskPairReadiness(store, runID)
	if err != nil {
		t.Fatal(err)
	}
	return readiness
}

func TestTaskShadowContinuationContainsHistoricalAndFirstRunRuns(t *testing.T) {
	store := newTestStore(t)
	_, current := createPendingTaskShadowContinuationRun(t, store, "frozen-model")
	historical := current
	historical.ID = ""
	historical.DecisionKey += ":historical"
	historical.SourceID += ":historical"
	historical.PromptVersion = "shadow-task-v6"
	historical.CreatedAt++
	if _, added, err := store.CreateShadowRun(historical); err != nil || !added {
		t.Fatalf("create historical continuation run: added=%v err=%v", added, err)
	}
	firstRun := seedFirstRunContamination(t, store)
	excludeFirstRunSource(t, store, firstRun, "shadow_run", current.ID)

	snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
	if err != nil {
		t.Fatal(err)
	}
	item := taskShadowContinuationItemForRun(t, snapshot, current.ID)
	if item.State != taskShadowContinuationFirstRunExcluded ||
		item.Action != taskShadowContinuationActionNone ||
		snapshot.FirstRunExcludedCount != 1 ||
		snapshot.HistoricalCount != 1 ||
		len(snapshot.Items) != 1 {
		t.Fatalf("contained continuation=%+v snapshot=%+v", item, snapshot)
	}
}

func TestTaskShadowContinuationCurrentRunStillRequiresValidImmutableGraph(
	t *testing.T,
) {
	store := newTestStore(t)
	_, run := createPendingTaskShadowContinuationRun(t, store, "frozen-model")
	if _, err := store.db.Exec(`DROP TRIGGER shadow_runs_no_update`); err != nil {
		t.Fatalf("drop immutable trigger for corruption fixture: %v", err)
	}
	if _, err := store.db.Exec(
		`UPDATE shadow_runs SET prediction_json = '{' WHERE id = ?`,
		run.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000); err == nil ||
		!strings.Contains(err.Error(), "load current shadow run") {
		t.Fatalf("invalid current run continuation error=%v", err)
	}
}

func TestTaskShadowContinuationExpiredOutcomeContainment(t *testing.T) {
	t.Run("valid result remains evaluable", func(t *testing.T) {
		store := newTestStore(t)
		episode, run := createPendingTaskShadowContinuationRun(t, store, "frozen-model")
		app := &App{
			runner:   &shadowExecutionFakeRunner{},
			activity: &activityMonitorState{store: store},
		}
		if _, _, err := app.executeShadowCandidatePlan(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		appendCurrentTaskResultEvidence(
			t,
			store,
			episode.ID,
			TaskWorkflowComposeText,
			"expired-valid-result",
			"The retained customer update result.",
			taskLifecycleTestResultDestination(),
			1_500,
		)
		cutoffAt := int64(1_500) + taskLifecycleCandidateHorizon.Milliseconds()
		expiry := expireStaleTaskEpisodes(store, cutoffAt, cutoffAt+1)
		if !reflect.DeepEqual(expiry.ExpiredIDs, []string{episode.ID}) ||
			len(expiry.Errors) != 0 {
			t.Fatalf("expiry=%+v", expiry)
		}
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, cutoffAt+2)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.EpisodeStatus != TaskEpisodeStatusExpired ||
			item.State != taskShadowContinuationEvaluationPending ||
			item.Action != taskShadowContinuationActionEvaluate {
			t.Fatalf("result-bearing expiry continuation=%+v", item)
		}
	})

	t.Run("missing result stays blocked", func(t *testing.T) {
		store := newTestStore(t)
		episode, run := createPendingTaskShadowContinuationRun(t, store, "frozen-model")
		app := &App{
			runner:   &shadowExecutionFakeRunner{},
			activity: &activityMonitorState{store: store},
		}
		if _, _, err := app.executeShadowCandidatePlan(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		lastSignalAt := max(episode.InferredStartAt, episode.DetectedAt)
		cutoffAt := lastSignalAt + taskLifecycleCandidateHorizon.Milliseconds()
		expiry := expireStaleTaskEpisodes(store, cutoffAt, cutoffAt+1)
		if !reflect.DeepEqual(expiry.ExpiredIDs, []string{episode.ID}) ||
			len(expiry.Errors) != 0 {
			t.Fatalf("expiry=%+v", expiry)
		}
		snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, cutoffAt+2)
		if err != nil {
			t.Fatal(err)
		}
		item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
		if item.EpisodeStatus != TaskEpisodeStatusExpired ||
			item.PairState != ShadowTaskPairStateObservationExpired ||
			item.State != taskShadowContinuationBlocked ||
			item.Action != taskShadowContinuationActionNone {
			t.Fatalf("missing-result expiry continuation=%+v", item)
		}
	})
}

func TestTaskShadowContinuationRecoveryHonorsDisabledShadow(t *testing.T) {
	activityStore := newTestStore(t)
	_, run := createPendingTaskShadowContinuationRun(
		t,
		activityStore,
		"frozen-model",
	)
	stateStore, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stateStore.Close() })
	runner := &taskShadowContinuationRecoveryRunner{}
	app := &App{
		store:    stateStore,
		runner:   runner,
		activity: &activityMonitorState{store: activityStore},
	}
	if err := app.SaveUIState(triageEnableShadowKey, "false"); err != nil {
		t.Fatal(err)
	}
	triager := NewActivityTriager(app, nil)
	recovery := triager.recoverTaskShadowContinuations(
		context.Background(),
		"shadow_disabled_test",
	)
	triager.wg.Wait()
	if recovery.ScannedRunCount != 0 ||
		len(recovery.ExecutionDispatchedIDs) != 0 ||
		len(recovery.EvaluationDispatchedIDs) != 0 ||
		len(recovery.Errors) != 0 {
		t.Fatalf("disabled recovery=%+v", recovery)
	}
	phases, _ := runner.snapshot()
	if len(phases) != 0 {
		t.Fatalf("disabled Shadow invoked runner: %v", phases)
	}
	attempts, err := activityStore.ListShadowExecutionAttempts(run.ID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("disabled attempts=%+v err=%v", attempts, err)
	}
}

func TestTaskShadowQueuedWorkRechecksDisabledShadow(t *testing.T) {
	t.Run("execution", func(t *testing.T) {
		activityStore := newTestStore(t)
		_, run := createPendingTaskShadowContinuationRun(
			t,
			activityStore,
			"frozen-model",
		)
		stateStore, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stateStore.Close() })
		runner := &taskShadowContinuationRecoveryRunner{}
		app := &App{
			store:    stateStore,
			runner:   runner,
			activity: &activityMonitorState{store: activityStore},
		}
		triager := NewActivityTriager(app, nil)
		triager.shadowExecutionSem <- struct{}{}
		triager.dispatchShadowCandidateExecution(context.Background(), run)
		if err := app.SaveUIState(triageEnableShadowKey, "false"); err != nil {
			t.Fatal(err)
		}
		<-triager.shadowExecutionSem
		triager.wg.Wait()

		phases, _ := runner.snapshot()
		if len(phases) != 0 {
			t.Fatalf("disabled queued execution invoked runner: %v", phases)
		}
		attempts, err := activityStore.ListShadowExecutionAttempts(run.ID)
		if err != nil || len(attempts) != 0 {
			t.Fatalf("disabled queued attempts=%+v err=%v", attempts, err)
		}
	})

	t.Run("evaluation", func(t *testing.T) {
		activityStore := newTestStore(t)
		_, run, _ := createCompletedComposeTextPairFixture(t, activityStore)
		stateStore, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stateStore.Close() })
		runner := &taskShadowContinuationRecoveryRunner{}
		app := &App{
			store:    stateStore,
			runner:   runner,
			activity: &activityMonitorState{store: activityStore},
		}
		triager := NewActivityTriager(app, nil)
		triager.shadowExecutionSem <- struct{}{}
		triager.dispatchTaskShadowEvaluation(context.Background(), run.ID)
		if err := app.SaveUIState(triageEnableShadowKey, "false"); err != nil {
			t.Fatal(err)
		}
		<-triager.shadowExecutionSem
		triager.wg.Wait()

		phases, _ := runner.snapshot()
		if len(phases) != 0 {
			t.Fatalf("disabled queued evaluation invoked runner: %v", phases)
		}
		evaluations, err := activityStore.ListShadowRunEvaluations(run.ID)
		if err != nil || len(evaluations) != 0 {
			t.Fatalf(
				"disabled queued evaluations=%+v err=%v",
				evaluations,
				err,
			)
		}
	})
}

type taskShadowContinuationRecoveryRunner struct {
	mu     sync.Mutex
	phases []string
	models map[string]string
}

func (r *taskShadowContinuationRecoveryRunner) Init(context.Context) error {
	return nil
}

func (r *taskShadowContinuationRecoveryRunner) IsReady() bool { return true }

func (r *taskShadowContinuationRecoveryRunner) ExecuteTask(
	_ context.Context,
	input *agent.TaskInput,
	callback agent.StreamCallback,
) (string, error) {
	phase := "execute"
	content := "A complete source-backed text result."
	if strings.Contains(input.Prompt, "You are Yansu's strict Shadow Task comparator.") {
		phase = "evaluate"
		var observed ShadowTaskEvaluationInput
		if err := decodeTaskShadowRolloutPrompt(
			input.Prompt,
			"\nInput:\n",
			&observed,
		); err != nil {
			return "", err
		}
		refs := sortedShadowTaskEvidenceIDs(observed)
		if len(refs) == 0 {
			return "", errors.New("continuation comparator has no evidence")
		}
		resultQuality := matchedTaskDimension(refs[0])
		resultQuality.Assessment = ShadowTaskQualityEquivalent
		payload, err := json.Marshal(ShadowTaskEvaluationOutput{
			Version:          ShadowTaskEvaluationOutputVersion,
			Summary:          "The observed and isolated results were compared.",
			Task:             matchedTaskDimension(refs[0]),
			Boundary:         matchedTaskDimension(refs[0]),
			Process:          matchedTaskDimension(refs[0]),
			ResultSimilarity: matchedTaskDimension(refs[0]),
			ResultQuality:    resultQuality,
			Timing:           matchedTaskDimension(refs[0]),
			Reasoning:        unknownTaskDimension(),
			Safety: ShadowTaskEvaluationDimension{
				Assessment:   ShadowTaskSafetySafe,
				Score:        shadowScore(1),
				Confidence:   1,
				Reason:       "The attempt remained isolated.",
				EvidenceRefs: []string{refs[0]},
			},
		})
		if err != nil {
			return "", err
		}
		content = string(payload)
	}
	r.mu.Lock()
	if r.models == nil {
		r.models = make(map[string]string)
	}
	r.phases = append(r.phases, phase)
	r.models[phase] = input.Model
	r.mu.Unlock()
	if err := callback(&agent.StreamEvent{
		Type:    "assistant",
		Content: content,
		RunID:   "continuation-" + phase,
	}); err != nil {
		return "", err
	}
	return "continuation-" + phase, nil
}

func (r *taskShadowContinuationRecoveryRunner) snapshot() ([]string, map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	phases := append([]string{}, r.phases...)
	models := make(map[string]string, len(r.models))
	for key, value := range r.models {
		models[key] = value
	}
	return phases, models
}

func (r *taskShadowContinuationRecoveryRunner) CancelRun(string) error {
	return nil
}

func (r *taskShadowContinuationRecoveryRunner) RespondToPermission(
	string,
	string,
) error {
	return nil
}

func (r *taskShadowContinuationRecoveryRunner) CancelPermission(string) {}
func (r *taskShadowContinuationRecoveryRunner) Shutdown() error         { return nil }

func TestTaskShadowContinuationRecoveryCompletesInterruptedNaturalPair(
	t *testing.T,
) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	episode, run := createPendingTaskShadowContinuationRun(
		t,
		store,
		"frozen-shadow-model",
	)
	completeTaskShadowContinuationEpisode(
		t,
		store,
		episode,
		"The customer rollout is complete and all checks passed.",
	)
	runner := &taskShadowContinuationRecoveryRunner{}
	app := &App{
		runner:   runner,
		activity: &activityMonitorState{store: store},
	}
	triager := NewActivityTriager(app, nil)

	recovery := triager.recoverTaskShadowContinuations(
		context.Background(),
		"test_recovery",
	)
	triager.wg.Wait()
	if !reflect.DeepEqual(recovery.ExecutionDispatchedIDs, []string{run.ID}) ||
		len(recovery.EvaluationDispatchedIDs) != 0 ||
		len(recovery.Errors) != 0 {
		t.Fatalf("recovery=%+v", recovery)
	}
	snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
	if err != nil {
		t.Fatal(err)
	}
	item := taskShadowContinuationItemForRun(t, snapshot, run.ID)
	if item.State != taskShadowContinuationComplete ||
		item.LearningRunID == "" ||
		len(item.CurrentEvaluationIDs) != 1 {
		t.Fatalf("completed recovery item=%+v snapshot=%+v", item, snapshot)
	}
	phases, models := runner.snapshot()
	if !reflect.DeepEqual(phases, []string{"execute", "evaluate"}) ||
		models["execute"] != "frozen-shadow-model" {
		t.Fatalf("runner phases=%v models=%v", phases, models)
	}

	replayed := triager.recoverTaskShadowContinuations(
		context.Background(),
		"test_replay",
	)
	triager.wg.Wait()
	if len(replayed.ExecutionDispatchedIDs) != 0 ||
		len(replayed.EvaluationDispatchedIDs) != 0 ||
		len(replayed.Errors) != 0 {
		t.Fatalf("replayed recovery=%+v", replayed)
	}
	phases, _ = runner.snapshot()
	if !reflect.DeepEqual(phases, []string{"execute", "evaluate"}) {
		t.Fatalf("idempotent replay called runner again: %v", phases)
	}
	attempts, err := store.ListShadowExecutionAttempts(run.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	evaluations, err := store.ListShadowRunEvaluations(run.ID)
	if err != nil || len(evaluations) != 1 {
		t.Fatalf("evaluations=%+v err=%v", evaluations, err)
	}
}

func TestTaskShadowContinuationRecoveryResumesEvaluationOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	_, run, _ := createCompletedComposeTextPairFixture(t, store)
	runner := &taskShadowContinuationRecoveryRunner{}
	app := &App{
		runner:   runner,
		activity: &activityMonitorState{store: store},
	}
	triager := NewActivityTriager(app, nil)

	recovery := triager.recoverTaskShadowContinuations(
		context.Background(),
		"evaluation_only_test",
	)
	triager.wg.Wait()
	if len(recovery.ExecutionDispatchedIDs) != 0 ||
		!reflect.DeepEqual(recovery.EvaluationDispatchedIDs, []string{run.ID}) ||
		len(recovery.Errors) != 0 {
		t.Fatalf("evaluation-only recovery=%+v", recovery)
	}
	item := taskShadowContinuationItemForRun(
		t,
		mustTaskShadowContinuationSnapshot(t, store),
		run.ID,
	)
	if item.State != taskShadowContinuationComplete ||
		item.LearningRunID == "" ||
		len(item.CurrentEvaluationIDs) != 1 {
		t.Fatalf("evaluation-only continuation=%+v", item)
	}
	phases, _ := runner.snapshot()
	if !reflect.DeepEqual(phases, []string{"evaluate"}) {
		t.Fatalf("evaluation-only runner phases=%v", phases)
	}
	attempts, err := store.ListShadowExecutionAttempts(run.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("evaluation-only attempts=%+v err=%v", attempts, err)
	}
}

func TestTaskShadowContinuationRecoveryResumesLearningWithoutModelCall(
	t *testing.T,
) {
	t.Setenv("HOME", t.TempDir())
	store := newTestStore(t)
	_, run, _ := createCompletedComposeTextPairFixture(t, store)
	evaluation := persistTaskShadowContinuationEvaluation(t, store, run)
	runner := &taskShadowContinuationRecoveryRunner{}
	app := &App{
		runner:   runner,
		activity: &activityMonitorState{store: store},
	}
	triager := NewActivityTriager(app, nil)

	recovery := triager.recoverTaskShadowContinuations(
		context.Background(),
		"learning_only_test",
	)
	triager.wg.Wait()
	if len(recovery.ExecutionDispatchedIDs) != 0 ||
		!reflect.DeepEqual(recovery.EvaluationDispatchedIDs, []string{run.ID}) ||
		len(recovery.Errors) != 0 {
		t.Fatalf("learning-only recovery=%+v", recovery)
	}
	item := taskShadowContinuationItemForRun(
		t,
		mustTaskShadowContinuationSnapshot(t, store),
		run.ID,
	)
	if item.State != taskShadowContinuationComplete ||
		item.LearningRunID == "" ||
		!reflect.DeepEqual(item.CurrentEvaluationIDs, []string{evaluation.ID}) {
		t.Fatalf("learning-only continuation=%+v", item)
	}
	phases, _ := runner.snapshot()
	if len(phases) != 0 {
		t.Fatalf("learning-only recovery called the model: %v", phases)
	}
}

func mustTaskShadowContinuationSnapshot(
	t *testing.T,
	store *ActivityStore,
) TaskShadowContinuationSnapshot {
	t.Helper()
	snapshot, err := BuildTaskShadowContinuationSnapshot(store, 100, 3_000)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type taskShadowLegacyFailureRunner struct {
	mu     sync.Mutex
	phases []string
}

func (r *taskShadowLegacyFailureRunner) Init(context.Context) error { return nil }
func (r *taskShadowLegacyFailureRunner) IsReady() bool              { return true }

func (r *taskShadowLegacyFailureRunner) record(phase string) {
	r.mu.Lock()
	r.phases = append(r.phases, phase)
	r.mu.Unlock()
}

func (r *taskShadowLegacyFailureRunner) ExecuteTask(
	_ context.Context,
	input *agent.TaskInput,
	callback agent.StreamCallback,
) (string, error) {
	switch {
	case strings.Contains(input.Prompt, "You are Yansu's task-onset detector."):
		r.record("onset")
		var observed TaskOnsetInput
		if err := decodeTaskShadowRolloutPrompt(
			input.Prompt,
			"\nObservation:\n",
			&observed,
		); err != nil {
			return "", err
		}
		if len(observed.Evidence) == 0 {
			return "", errors.New("onset prompt has no evidence")
		}
		payload, err := json.Marshal(TaskOnsetResponse{
			Episodes: []TaskOnsetPrediction{{
				LocalKey:           "customer-update",
				Task:               "Compose a customer update",
				DesiredResult:      "A factual update ready to send",
				InferredStartedAt:  observed.CutoffAt,
				TaskConfidence:     0.93,
				BoundaryConfidence: 0.9,
				ResultConfidence:   0.88,
				EvidenceRefs:       []string{observed.Evidence[len(observed.Evidence)-1].Ref},
			}},
		})
		if err != nil {
			return "", err
		}
		if err := callback(&agent.StreamEvent{
			Type:    "assistant",
			Content: string(payload),
		}); err != nil {
			return "", err
		}
		return "shadow-onset", nil
	case input.ExecutionMode == "shadow":
		r.record("execute")
		if err := callback(&agent.StreamEvent{
			Type:    "assistant",
			Content: "A complete source-backed customer update.",
		}); err != nil {
			return "", err
		}
		return "shadow-execution", nil
	default:
		r.record("legacy_triage")
		return "", errors.New("legacy activity triage unavailable")
	}
}

func (r *taskShadowLegacyFailureRunner) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.phases...)
}

func (r *taskShadowLegacyFailureRunner) CancelRun(string) error { return nil }
func (r *taskShadowLegacyFailureRunner) RespondToPermission(string, string) error {
	return nil
}
func (r *taskShadowLegacyFailureRunner) CancelPermission(string) {}
func (r *taskShadowLegacyFailureRunner) Shutdown() error         { return nil }

func countString(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}
