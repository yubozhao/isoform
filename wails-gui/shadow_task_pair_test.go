package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestShadowTaskPairReadinessTracksProductionPairStates(t *testing.T) {
	store := newTestStore(t)
	input := taskEpisodeFixture(
		"pair-readiness",
		1_000,
		1_100,
		TaskEpisodeStatusCandidate,
	)
	input.InitialHypothesis.TaskHypothesis = "Compose a customer update"
	input.InitialHypothesis.DesiredResultHypothesis =
		"A factual update ready to send"
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create episode: added=%v err=%v", added, err)
	}
	run, added, err := store.CreateShadowTaskRunFromEpisode(
		episode,
		"test-model",
		1_200,
	)
	if err != nil || !added {
		t.Fatalf("create run: added=%v err=%v", added, err)
	}
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != ShadowTaskPairStateShadowPending {
		t.Fatalf("state=%q, want shadow pending", readiness.State)
	}

	app := &App{
		runner:   &shadowExecutionFakeRunner{},
		activity: &activityMonitorState{store: store},
	}
	if _, _, err := app.executeShadowCandidatePlan(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	readiness, err = BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != ShadowTaskPairStateAwaitingUserResult ||
		!readiness.ShadowResultMaterialized {
		t.Fatalf("readiness=%+v, want observed outcome wait", readiness)
	}
}

func TestShadowTaskPairReadinessRecognizesMaterializedPair(t *testing.T) {
	store := newTestStore(t)
	_, run, userText := createCompletedComposeTextPairFixture(t, store)
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != ShadowTaskPairStateReady ||
		!readiness.Ready ||
		readiness.UserResultSHA256 != sha256Hex([]byte(userText)) ||
		!readiness.ShadowResultMaterialized {
		t.Fatalf("readiness=%+v, want pair ready", readiness)
	}
}

func TestShadowTaskPairReadinessRecognizesAnalyzeTextPair(t *testing.T) {
	store := newTestStore(t)
	_, run, userText := createCompletedAnalyzeTextPairFixture(t, store)
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Workflow != TaskWorkflowAnalyzeText ||
		readiness.State != ShadowTaskPairStateReady ||
		!readiness.Ready ||
		!readiness.UserResultMaterialized ||
		readiness.UserResultSHA256 != sha256Hex([]byte(userText)) {
		t.Fatalf("analysis readiness=%+v", readiness)
	}
}

func TestShadowTaskPairReadinessDoesNotLetHistoricalEvaluationBlockCurrentPair(
	t *testing.T,
) {
	store := newTestStore(t)
	_, run, _ := createCompletedComposeTextPairFixture(t, store)
	observation, added, err := store.AddShadowRunObservation(ShadowRunObservation{
		ShadowRunID:    run.ID,
		SourceKind:     "historical_test",
		SourceID:       "historical-observation",
		ObservedAction: "The user composed the update.",
		ObservedResult: "The update was completed.",
		ObservedAt:     2_000,
		CreatedAt:      2_100,
	})
	if err != nil || !added {
		t.Fatalf("add historical observation: added=%v err=%v", added, err)
	}
	unknown := ShadowEvaluationDimension{
		Assessment: ShadowTaskAssessmentUnknown,
		Confidence: 0.2,
	}
	historical, added, err := store.AddShadowRunEvaluation(ShadowRunEvaluation{
		ShadowRunID:       run.ID,
		ObservationID:     observation.ID,
		IdempotencyKey:    run.ID + ":historical-comparator",
		ComparisonVersion: "shadow-task-comparator-v2",
		EvaluatorVersion:  "shadow-task-evaluator-v2",
		EvaluatorModel:    "historical-model",
		Action:            unknown,
		Outcome:           unknown,
		Timing:            unknown,
		Reasoning:         unknown,
		Safety:            unknown,
		CreatedAt:         2_200,
	})
	if err != nil || !added {
		t.Fatalf("add historical evaluation: added=%v err=%v", added, err)
	}

	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != ShadowTaskPairStateReady || !readiness.Ready ||
		len(readiness.CurrentEvaluationIDs) != 0 ||
		len(readiness.HistoricalEvaluationIDs) != 1 ||
		readiness.HistoricalEvaluationIDs[0] != historical.ID ||
		len(readiness.EvaluationIDs) != 1 ||
		readiness.EvaluationIDs[0] != historical.ID {
		t.Fatalf("historical evaluation blocked current pair: %+v", readiness)
	}
	foundReason := false
	for _, reason := range readiness.Reasons {
		foundReason = foundReason || reason == "historical_evaluation_generation_only"
	}
	if !foundReason {
		t.Fatalf("historical generation reason missing: %+v", readiness.Reasons)
	}
}

func TestShadowTaskPairReadinessQuarantinesHistoricalWorkerGeneration(
	t *testing.T,
) {
	store := newTestStore(t)
	_, run, _ := createCompletedComposeTextPairFixture(t, store)
	historical := createHistoricalShadowTaskRunFixture(t, store, run)

	readiness, err := BuildShadowTaskPairReadiness(store, historical.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != ShadowTaskPairStateHistoricalGeneration ||
		readiness.Ready || readiness.CurrentGeneration ||
		readiness.ShadowPromptVersion != "shadow-task-v6" ||
		!readiness.UserResultMaterialized ||
		!readiness.ShadowResultMaterialized ||
		len(readiness.CurrentEvaluationIDs) != 0 {
		t.Fatalf("historical worker readiness=%+v", readiness)
	}
	foundReason := false
	for _, reason := range readiness.Reasons {
		foundReason = foundReason ||
			reason == "historical_shadow_prompt_generation"
	}
	if !foundReason {
		t.Fatalf("historical worker reason missing: %+v", readiness.Reasons)
	}
}

func TestShadowTaskPairReadinessContainsExpiredObservationWithoutResult(
	t *testing.T,
) {
	store := newTestStore(t)
	input := taskEpisodeFixture(
		"pair-expired-without-result",
		1_000,
		1_100,
		TaskEpisodeStatusCandidate,
	)
	input.InitialHypothesis.TaskHypothesis = "Compose a customer update"
	input.InitialHypothesis.DesiredResultHypothesis =
		"A factual update ready to send"
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create episode: added=%v err=%v", added, err)
	}
	app := &App{
		runner:   &shadowExecutionFakeRunner{},
		activity: &activityMonitorState{store: store},
	}
	run, _, _, err := app.executeTaskEpisodeShadow(context.Background(), episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	cutoffAt := episode.DetectedAt + taskLifecycleCandidateHorizon.Milliseconds()
	expiry := expireStaleTaskEpisodes(store, cutoffAt, cutoffAt+1)
	if len(expiry.Errors) != 0 || len(expiry.ExpiredIDs) != 1 {
		t.Fatalf("expiry=%+v", expiry)
	}
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != ShadowTaskPairStateObservationExpired || readiness.Ready {
		t.Fatalf("readiness=%+v", readiness)
	}
}

func TestShadowTaskPairReadinessAllowsExpiredEpisodeWithObservedResult(
	t *testing.T,
) {
	store := newTestStore(t)
	input := taskEpisodeFixture(
		"pair-expired-with-result",
		1_000,
		1_100,
		TaskEpisodeStatusCandidate,
	)
	input.InitialHypothesis.TaskHypothesis = "Compose a customer update"
	input.InitialHypothesis.DesiredResultHypothesis =
		"A factual update ready to send"
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create episode: added=%v err=%v", added, err)
	}
	app := &App{
		runner:   &shadowExecutionFakeRunner{},
		activity: &activityMonitorState{store: store},
	}
	run, _, _, err := app.executeTaskEpisodeShadow(context.Background(), episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	userText := "The independently observed update."
	lifecycleInput := TaskLifecycleInput{
		SourceKind:     "activity",
		SourceID:       "expired-result-window",
		PrefixCutoffAt: 1_100,
		CutoffAt:       2_000,
		Episodes: []TaskLifecycleEpisodeSnapshot{{
			EpisodeID:          episode.ID,
			CurrentStatus:      TaskEpisodeStatusCandidate,
			InferredStartedAt:  episode.InferredStartAt,
			Task:               episode.LatestHypothesis.TaskHypothesis,
			DesiredResult:      episode.LatestHypothesis.DesiredResultHypothesis,
			TaskConfidence:     episode.LatestHypothesis.TaskConfidence,
			BoundaryConfidence: episode.LatestHypothesis.BoundaryConfidence,
			EvidenceRefs:       []string{"prefix"},
		}},
		PrefixEvidence: []TaskLifecycleEvidence{{
			Ref:        "prefix",
			Kind:       "onset",
			SourceKind: "activity_event",
			SourceID:   "onset",
			OccurredAt: 1_000,
			Summary:    "The user began composing an update.",
		}},
		SuffixEvidence: []TaskLifecycleEvidence{
			{
				Ref:         "commit",
				Kind:        TaskLifecycleEvidenceTextCommit,
				SourceKind:  "activity_event",
				SourceID:    "commit",
				OccurredAt:  1_500,
				Summary:     "The keyboard burst ended with Return.",
				ContentKind: TaskLifecycleContentCommittedText,
				Content:     userText,
				Actor:       TaskLifecycleActorUser,
				Destination: taskLifecycleTestResultDestination(),
			},
			{
				Ref:        "submit",
				Kind:       "submission",
				SourceKind: "activity_event",
				SourceID:   "submit",
				OccurredAt: 1_600,
				Summary:    "The user submitted the update.",
			},
		},
	}
	response := normalizeTaskLifecycleResponse(
		lifecycleInput,
		TaskLifecycleResponse{Observations: []TaskLifecycleObservation{{
			LocalKey:           "observed-result",
			Kind:               TaskLifecycleKindResult,
			EpisodeIDs:         []string{episode.ID},
			Confidence:         0.8,
			BoundaryConfidence: 0.4,
			ResultSummary:      "Observed submitted text.",
			ResultConfidence:   0.8,
			EvidenceRefs:       []string{"commit", "submit"},
		}}},
	)
	if _, err := PersistTaskLifecycleObservations(
		store,
		lifecycleInput,
		response,
		2_001,
	); err != nil {
		t.Fatal(err)
	}
	cutoffAt := int64(1_600) + taskLifecycleCandidateHorizon.Milliseconds()
	expiry := expireStaleTaskEpisodes(store, cutoffAt, cutoffAt+1)
	if len(expiry.Errors) != 0 || len(expiry.ExpiredIDs) != 1 {
		t.Fatalf("expiry=%+v", expiry)
	}
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != ShadowTaskPairStateReady ||
		!readiness.Ready ||
		readiness.UserResultSHA256 != sha256Hex([]byte(userText)) ||
		readiness.UserResultDestinationSHA256 == "" {
		t.Fatalf("readiness=%+v", readiness)
	}
}

func TestShadowTaskPairReadinessFailsClosedOnLaterInvalidResultProvenance(
	t *testing.T,
) {
	store := newTestStore(t)
	episode, run, _ := createCompletedComposeTextPairFixture(t, store)
	content := "A later malformed result."
	payload, err := json.Marshal(TaskLifecycleObservation{
		MaterializedResult: &TaskLifecycleMaterializedResult{
			Workflow:          TaskWorkflowComposeText,
			Kind:              TaskLifecycleContentCommittedText,
			Content:           content,
			SHA256:            sha256Hex([]byte(content)),
			EvidenceRefs:      []string{"later:commit"},
			Destination:       taskLifecycleTestResultDestination(),
			DestinationSHA256: "tampered",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	invalid, added, err := store.AppendTaskEpisodeEvidence(TaskEpisodeEvidence{
		TaskEpisodeID: episode.ID,
		Role:          TaskEpisodeEvidenceResult,
		SourceKind:    "activity_event",
		SourceID:      "later-invalid-destination",
		Summary:       "A later result carried malformed provenance.",
		PayloadJSON:   string(payload),
		ObservedAt:    2_500,
		CreatedAt:     2_501,
	})
	if err != nil || !added {
		t.Fatalf("append invalid result: added=%v err=%v", added, err)
	}
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Ready ||
		readiness.State != ShadowTaskPairStateUserResultUnavailable ||
		len(readiness.InvalidUserResultEvidenceIDs) != 1 ||
		readiness.InvalidUserResultEvidenceIDs[0] != invalid.ID ||
		!containsString(readiness.Reasons, "invalid_user_result_provenance") {
		t.Fatalf("invalid provenance readiness=%+v", readiness)
	}
}

func TestComposeTextPairPersistsEvaluationAndContaminationSafeLearning(
	t *testing.T,
) {
	store := newTestStore(t)
	_, run, _ := createCompletedComposeTextPairFixture(t, store)
	input, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	find := func(origin, kind string) string {
		t.Helper()
		for _, evidence := range input.Evidence {
			if evidence.Origin == origin && evidence.Kind == kind {
				return evidence.ID
			}
		}
		t.Fatalf("missing %s/%s evidence in %+v", origin, kind, input.Evidence)
		return ""
	}
	observedTask := find(
		ShadowTaskEvidenceOriginObserved,
		ShadowTaskEvidenceTask,
	)
	observedResult := find(
		ShadowTaskEvidenceOriginObserved,
		ShadowTaskEvidenceResult,
	)
	observedBoundary := find(
		ShadowTaskEvidenceOriginObserved,
		ShadowTaskEvidenceBoundary,
	)
	shadowTask := find(
		ShadowTaskEvidenceOriginShadow,
		ShadowTaskEvidenceTask,
	)
	shadowResult := find(
		ShadowTaskEvidenceOriginShadow,
		ShadowTaskEvidenceResult,
	)
	qualityCriterion := find(
		ShadowTaskEvidenceOriginCriterion,
		ShadowTaskEvidenceQualityCriterion,
	)
	dimension := func(
		assessment string,
		score float64,
		refs ...string,
	) ShadowTaskEvaluationDimension {
		return ShadowTaskEvaluationDimension{
			Assessment:   assessment,
			Score:        &score,
			Confidence:   0.9,
			Reason:       "Source-backed comparison.",
			EvidenceRefs: refs,
		}
	}
	output := ShadowTaskEvaluationOutput{
		Version: ShadowTaskEvaluationOutputVersion,
		Summary: "The independently produced drafts were compared without " +
			"granting authority.",
		Task: dimension(
			ShadowTaskAssessmentMatched,
			0.9,
			observedTask,
			shadowTask,
		),
		Boundary: dimension(
			ShadowTaskAssessmentMatched,
			0.85,
			observedBoundary,
		),
		Process: unknownTaskDimension(),
		ResultSimilarity: dimension(
			ShadowTaskAssessmentPartial,
			0.8,
			observedResult,
			shadowResult,
		),
		ResultQuality: dimension(
			ShadowTaskQualityEquivalent,
			0.82,
			observedResult,
			shadowResult,
			qualityCriterion,
		),
		Timing:    unknownTaskDimension(),
		Reasoning: unknownTaskDimension(),
		Safety:    unknownTaskDimension(),
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
			EvaluatorRaw:      `{"test":"validated before persistence"}`,
			Evaluation:        &output,
		},
		2_200,
	)
	if err != nil || !added {
		t.Fatalf("persist evaluation: added=%v err=%v", added, err)
	}

	app := &App{activity: &activityMonitorState{store: store}}
	learning, learningAdded, err := app.BuildAndPersistShadowTaskLearning(
		run.CapabilityKey,
		2_300,
	)
	if err != nil || !learningAdded {
		t.Fatalf("persist learning: added=%v err=%v", learningAdded, err)
	}
	if learning.Projection.EvaluationCount != 1 ||
		len(learning.Projection.Candidates) == 0 {
		t.Fatalf("learning projection=%+v", learning.Projection)
	}
	for _, candidate := range learning.Projection.Candidates {
		if candidate.CapabilityKey != TaskWorkflowComposeText ||
			candidate.AuthorityEffect != ShadowTaskLearningAuthorityNone {
			t.Fatalf("candidate escaped workflow or authority boundary: %+v", candidate)
		}
		for _, evidence := range candidate.Evidence {
			if evidence.Origin == ShadowTaskEvidenceOriginShadow ||
				evidence.Summary == evaluation.Summary {
				t.Fatalf("shadow/evaluator content contaminated learning: %+v", evidence)
			}
		}
	}
	replayedLearning, replayedAdded, err := app.BuildAndPersistShadowTaskLearning(
		run.CapabilityKey,
		9_999,
	)
	if err != nil || replayedAdded || replayedLearning.ID != learning.ID ||
		replayedLearning.CreatedAt != learning.CreatedAt {
		t.Fatalf(
			"idempotent learning replay=%+v added=%v err=%v",
			replayedLearning,
			replayedAdded,
			err,
		)
	}
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != ShadowTaskPairStateEvaluated ||
		len(readiness.EvaluationIDs) != 1 ||
		readiness.EvaluationIDs[0] != evaluation.ID {
		t.Fatalf("evaluated readiness=%+v", readiness)
	}
}

func TestShadowTaskPairReadinessRejectsTextWorkflowToolActivity(t *testing.T) {
	for _, test := range []struct {
		name      string
		actions   []ShadowExecutionAction
		artifacts []ShadowExecutionArtifact
		reason    string
		evaluated bool
	}{
		{
			name: "tool action",
			actions: []ShadowExecutionAction{{
				Type:     "tool_use",
				ToolName: "Write",
			}},
			reason: "isolated_text_workflow_used_tools",
		},
		{
			name: "artifact",
			artifacts: []ShadowExecutionArtifact{{
				Path:      "unexpected.txt",
				SizeBytes: 10,
				SHA256:    sha256Hex([]byte("unexpected")),
			}},
			reason:    "isolated_text_workflow_created_artifacts",
			evaluated: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			_, run, _ := createCompletedComposeTextPairFixture(t, store)
			if test.evaluated {
				input, err := BuildShadowTaskEvaluationInput(store, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				prompt, _, err := buildShadowTaskEvaluationPrompt(input)
				if err != nil {
					t.Fatal(err)
				}
				unknown := unknownTaskDimension()
				output := ShadowTaskEvaluationOutput{
					Version:          ShadowTaskEvaluationOutputVersion,
					Summary:          "The valid direct-text pair was compared.",
					Task:             unknown,
					Boundary:         unknown,
					Process:          unknown,
					ResultSimilarity: unknown,
					ResultQuality:    unknown,
					Timing:           unknown,
					Reasoning:        unknown,
					Safety:           unknown,
				}
				if _, added, err := PersistShadowTaskEvaluation(
					store,
					input,
					ShadowTaskEvaluationResult{
						EvaluatorVersion:  ShadowTaskEvaluatorVersion,
						ComparatorVersion: ShadowTaskComparatorVersion,
						InputVersion:      ShadowTaskEvaluationInputVersion,
						ComparatorPrompt:  prompt,
						ComparatorHash:    shadowTaskComparatorHash(prompt),
						EvaluatorModel:    "test-model",
						Evaluation:        &output,
					},
					2_200,
				); err != nil || !added {
					t.Fatalf("persist current evaluation: added=%v err=%v", added, err)
				}
			}
			attempts, err := store.ListShadowExecutionAttempts(run.ID)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("attempts=%d err=%v", len(attempts), err)
			}
			attempt := attempts[0]
			attempt.ID = ""
			attempt.IdempotencyKey += ":containment-violation"
			attempt.Actions = test.actions
			attempt.Artifacts = test.artifacts
			attempt.AssistantOutput = ""
			attempt.CompletedAt++
			attempt.CreatedAt++
			if _, added, err := store.AddShadowExecutionAttempt(attempt); err != nil || !added {
				t.Fatalf("add violating attempt: added=%v err=%v", added, err)
			}

			readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if readiness.State != ShadowTaskPairStateShadowFailed || readiness.Ready {
				t.Fatalf("readiness=%+v", readiness)
			}
			if readiness.ShadowResultMaterialized {
				t.Fatalf("artifact/tool activity materialized a result: %+v", readiness)
			}
			found := false
			for _, reason := range readiness.Reasons {
				found = found || reason == test.reason
			}
			if !found {
				t.Fatalf("reasons=%v, want %q", readiness.Reasons, test.reason)
			}
			if _, err := BuildShadowTaskEvaluationInput(store, run.ID); !errors.Is(err, ErrShadowTaskShadowResultUnavailable) {
				t.Fatalf("evaluation input err=%v", err)
			}
		})
	}
}

func TestAnalyzeTextLearningStaysCapabilityScopedAndOutsideComposeRelease(
	t *testing.T,
) {
	store := newTestStore(t)
	_, run, _ := createCompletedAnalyzeTextPairFixture(t, store)
	input, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var output ShadowTaskEvaluationOutput
	if err := json.Unmarshal(
		[]byte(taskShadowQualityEvaluationOutput(t, store, run.ID)),
		&output,
	); err != nil {
		t.Fatal(err)
	}
	prompt, _, err := buildShadowTaskEvaluationPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, added, err := PersistShadowTaskEvaluation(
		store,
		input,
		ShadowTaskEvaluationResult{
			EvaluatorVersion:  ShadowTaskEvaluatorVersion,
			ComparatorVersion: ShadowTaskComparatorVersion,
			InputVersion:      ShadowTaskEvaluationInputVersion,
			ComparatorPrompt:  prompt,
			ComparatorHash:    shadowTaskComparatorHash(prompt),
			EvaluatorModel:    "test-model",
			EvaluatorRaw:      `{"test":"analysis evaluation"}`,
			Evaluation:        &output,
		},
		2_200,
	); err != nil || !added {
		t.Fatalf("persist analysis evaluation: added=%v err=%v", added, err)
	}
	app := &App{activity: &activityMonitorState{store: store}}
	learning, added, err := app.BuildAndPersistShadowTaskLearning(
		TaskWorkflowAnalyzeText,
		2_300,
	)
	if err != nil || !added {
		t.Fatalf("persist analysis learning: added=%v err=%v", added, err)
	}
	if len(learning.Projection.Candidates) == 0 {
		t.Fatalf("analysis learning=%+v", learning.Projection)
	}
	for _, candidate := range learning.Projection.Candidates {
		if candidate.CapabilityKey != TaskWorkflowAnalyzeText ||
			candidate.AuthorityEffect != ShadowTaskLearningAuthorityNone {
			t.Fatalf("analysis candidate escaped boundary: %+v", candidate)
		}
	}
	releases, err := store.ListTaskShadowCapabilityReleaseEvents(
		TaskWorkflowAnalyzeText,
		20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 0 {
		t.Fatalf("analysis learning entered compose release ledger: %+v", releases)
	}
}
