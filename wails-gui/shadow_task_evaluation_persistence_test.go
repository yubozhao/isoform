package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func createExecutedShadowTaskFixture(
	t *testing.T,
	store *ActivityStore,
) (TaskEpisode, ShadowRun) {
	t.Helper()
	episode, run, _ := createCompletedComposeTextPairFixture(t, store)
	return episode, run
}

func createHistoricalShadowTaskRunFixture(
	t *testing.T,
	store *ActivityStore,
	current ShadowRun,
) ShadowRun {
	t.Helper()
	historical := current
	historical.ID = ""
	historical.DecisionKey += ":historical-v6"
	historical.SourceID += ":historical-v6"
	historical.PromptVersion = "shadow-task-v6"
	historical.CreatedAt++
	historical, added, err := store.CreateShadowRun(historical)
	if err != nil || !added {
		t.Fatalf("create historical run: added=%v err=%v", added, err)
	}
	attempts, err := store.ListShadowExecutionAttempts(current.ID)
	if err != nil || len(attempts) == 0 {
		t.Fatalf("list current attempts: count=%d err=%v", len(attempts), err)
	}
	attempt := attempts[len(attempts)-1]
	attempt.ID = ""
	attempt.ShadowRunID = historical.ID
	attempt.IdempotencyKey += ":historical-v6"
	if _, added, err := store.AddShadowExecutionAttempt(attempt); err != nil || !added {
		t.Fatalf("copy historical attempt: added=%v err=%v", added, err)
	}
	return historical
}

func appendCurrentTaskResultEvidence(
	t *testing.T,
	store *ActivityStore,
	episodeID, workflow, sourceID, content string,
	destination *TaskLifecycleResultDestination,
	observedAt int64,
) TaskEpisodeEvidence {
	t.Helper()
	destinationSHA256, err := taskLifecycleResultDestinationSHA256(destination)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(TaskLifecycleObservation{
		MaterializedResult: &TaskLifecycleMaterializedResult{
			Workflow:          workflow,
			Kind:              TaskLifecycleContentCommittedText,
			Content:           content,
			SHA256:            sha256Hex([]byte(content)),
			EvidenceRefs:      []string{"suffix:" + sourceID},
			Destination:       destination,
			DestinationSHA256: destinationSHA256,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, added, err := store.AppendTaskEpisodeEvidence(TaskEpisodeEvidence{
		TaskEpisodeID: episodeID,
		Role:          TaskEpisodeEvidenceResult,
		SourceKind:    "activity_event",
		SourceID:      sourceID,
		Summary:       "A retained user result revision.",
		PayloadJSON:   string(payload),
		ObservedAt:    observedAt,
		CreatedAt:     observedAt + 1,
	})
	if err != nil || !added {
		t.Fatalf("append result evidence: added=%v err=%v", added, err)
	}
	return item
}

func createCompletedComposeTextPairFixture(
	t *testing.T,
	store *ActivityStore,
) (TaskEpisode, ShadowRun, string) {
	t.Helper()
	input := taskEpisodeFixture(
		"task-compose-pair",
		1_000,
		1_100,
		TaskEpisodeStatusCandidate,
	)
	input.InitialHypothesis.TaskHypothesis = "Compose a customer update"
	input.InitialHypothesis.DesiredResultHypothesis =
		"A factual update ready to send"
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create compose episode: added=%v err=%v", added, err)
	}
	app := &App{
		runner:   &shadowExecutionFakeRunner{},
		activity: &activityMonitorState{store: store},
	}
	run, _, _, err := app.executeTaskEpisodeShadow(
		context.Background(),
		episode.ID,
	)
	if err != nil {
		t.Fatalf("execute compose shadow: %v", err)
	}
	userText := "The rollout is complete and verification passed."
	lifecycleInput := TaskLifecycleInput{
		SourceKind:     "activity",
		SourceID:       "compose-window",
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
			EvidenceRefs:       []string{"prefix"},
		}},
		PrefixEvidence: []TaskLifecycleEvidence{{
			Ref:        "prefix",
			Kind:       "onset",
			SourceKind: "event",
			SourceID:   "onset",
			OccurredAt: 1_000,
			Summary:    "The user began composing the update.",
		}},
		SuffixEvidence: []TaskLifecycleEvidence{
			{
				Ref:        "typing",
				Kind:       "action",
				SourceKind: "activity_event",
				SourceID:   "typing",
				OccurredAt: 1_400,
				Summary:    "The user composed the customer update.",
				Actor:      TaskLifecycleActorUser,
			},
			{
				Ref:         "text",
				Kind:        TaskLifecycleEvidenceTextCommit,
				SourceKind:  "activity_event",
				SourceID:    "typed",
				OccurredAt:  1_500,
				Summary:     "The user entered the update.",
				ContentKind: TaskLifecycleContentCommittedText,
				Content:     userText,
				Actor:       TaskLifecycleActorUser,
				Destination: taskLifecycleTestResultDestination(),
			},
			{
				Ref:        "send",
				Kind:       "sent",
				SourceKind: "event",
				SourceID:   "send",
				OccurredAt: 1_600,
				Summary:    "The user sent the update.",
			},
		},
	}
	response := TaskLifecycleResponse{Observations: []TaskLifecycleObservation{{
		LocalKey:         "complete",
		Kind:             TaskLifecycleKindCompleted,
		EpisodeIDs:       []string{episode.ID},
		Confidence:       0.9,
		ResultSummary:    "The update was sent.",
		ResultConfidence: 0.9,
		EvidenceRefs:     []string{"typing", "text", "send"},
	}}}
	if _, err := PersistTaskLifecycleObservations(
		store,
		lifecycleInput,
		response,
		2_100,
	); err != nil {
		t.Fatalf("persist compose lifecycle: %v", err)
	}
	episode, err = store.GetTaskEpisode(episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	return episode, run, userText
}

func createCompletedAnalyzeTextPairFixture(
	t *testing.T,
	store *ActivityStore,
) (TaskEpisode, ShadowRun, string) {
	t.Helper()
	input := taskEpisodeFixture(
		"task-analyze-pair",
		1_000,
		1_100,
		TaskEpisodeStatusCandidate,
	)
	input.InitialHypothesis.TaskHypothesis = "Review the supplied proposal"
	input.InitialHypothesis.DesiredResultHypothesis =
		"Findings that identify the material gaps and risks"
	input.FrozenContext.Manifest = []TaskContextManifestEntry{{
		SourceKind: taskContextSourceActivitySnapshotKind,
		SourceID:   "activity-session:proposal",
		ObservedAt: 999,
		Included:   true,
	}}
	episode, added, err := store.CreateTaskEpisode(input)
	if err != nil || !added {
		t.Fatalf("create analyze episode: added=%v err=%v", added, err)
	}
	app := &App{
		runner:   &shadowExecutionFakeRunner{},
		activity: &activityMonitorState{store: store},
	}
	run, _, _, err := app.executeTaskEpisodeShadow(
		context.Background(),
		episode.ID,
	)
	if err != nil {
		t.Fatalf("execute analyze shadow: %v", err)
	}
	userText := "The pricing assumption is unsupported by the supplied evidence."
	lifecycleInput := TaskLifecycleInput{
		SourceKind:     "activity",
		SourceID:       "analysis-window",
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
			EvidenceRefs:       []string{"prefix"},
		}},
		PrefixEvidence: []TaskLifecycleEvidence{{
			Ref:        "prefix",
			Kind:       "onset",
			SourceKind: "activity_event",
			SourceID:   "analysis-onset",
			OccurredAt: 1_000,
			Summary:    "The user began reviewing the supplied proposal.",
		}},
		SuffixEvidence: []TaskLifecycleEvidence{
			{
				Ref:         "analysis-text",
				Kind:        TaskLifecycleEvidenceTextCommit,
				SourceKind:  "activity_event",
				SourceID:    "analysis-return",
				OccurredAt:  1_500,
				Summary:     "The keyboard burst ended with Return.",
				ContentKind: TaskLifecycleContentCommittedText,
				Content:     userText,
				Actor:       TaskLifecycleActorUser,
				Destination: taskLifecycleTestResultDestination(),
			},
			{
				Ref:        "analysis-submit",
				Kind:       "submission",
				SourceKind: "activity_event",
				SourceID:   "analysis-submit",
				OccurredAt: 1_600,
				Summary:    "The user submitted the analysis.",
			},
		},
	}
	response := TaskLifecycleResponse{Observations: []TaskLifecycleObservation{{
		LocalKey:           "complete-analysis",
		Kind:               TaskLifecycleKindCompleted,
		EpisodeIDs:         []string{episode.ID},
		Confidence:         0.9,
		BoundaryConfidence: 0.85,
		ResultSummary:      "The analysis was committed.",
		ResultConfidence:   0.9,
		EvidenceRefs:       []string{"analysis-text", "analysis-submit"},
	}}}
	if _, err := PersistTaskLifecycleObservations(
		store,
		lifecycleInput,
		response,
		2_100,
	); err != nil {
		t.Fatalf("persist analyze lifecycle: %v", err)
	}
	episode, err = store.GetTaskEpisode(episode.ID)
	if err != nil {
		t.Fatal(err)
	}
	return episode, run, userText
}

func matchedTaskDimension(refs ...string) ShadowTaskEvaluationDimension {
	score := 0.9
	return ShadowTaskEvaluationDimension{
		Assessment: ShadowTaskAssessmentMatched,
		Score:      &score, Confidence: 0.9, Reason: "Source-backed match.",
		EvidenceRefs: refs,
	}
}

func TestTaskEpisodeEvidenceEvaluationKindRequiresCanonicalLifecycleReasoning(
	t *testing.T,
) {
	const episodeID = "reasoning-kind-episode"
	newEvidence := func(
		kind, quote, version string,
	) TaskEpisodeEvidence {
		t.Helper()
		refs := []string{"suffix:user-reason"}
		payload, err := json.Marshal(taskLifecycleReasoningEvidencePayload{
			Version:      version,
			Kind:         kind,
			SourceQuote:  quote,
			EvidenceRefs: refs,
		})
		if err != nil {
			t.Fatal(err)
		}
		return TaskEpisodeEvidence{
			TaskEpisodeID: episodeID,
			Role:          TaskEpisodeEvidenceReasoning,
			SourceKind:    "task_lifecycle_reasoning",
			SourceID: deterministicTaskEpisodeID(
				"task-lifecycle-reasoning-source",
				episodeID+"\x00"+kind+"\x00"+
					normalizeTaskReasoningText(quote)+"\x00"+refs[0],
			),
			Summary:     quote,
			PayloadJSON: string(payload),
			ObservedAt:  2_000,
		}
	}

	rationale := newEvidence(
		TaskLifecycleReasoningStatedRationale,
		"I chose this because the customer needs certainty.",
		taskLifecyclePromptVersion,
	)
	correction := newEvidence(
		TaskLifecycleReasoningCorrection,
		"Actually, I changed direction after reviewing the evidence.",
		taskLifecyclePromptVersion,
	)
	legacy := rationale
	legacy.SourceKind = "legacy_memory_reasoning"
	legacy.SourceID = "legacy-reason"
	malformed := rationale
	malformed.PayloadJSON = `{"kind":`
	wrongVersion := newEvidence(
		TaskLifecycleReasoningStatedRationale,
		"I chose this because the customer needs certainty.",
		"task-lifecycle-v4",
	)
	mismatchedQuote := rationale
	mismatchedQuote.Summary = "A model-authored paraphrase."
	wrongSourceID := rationale
	wrongSourceID.SourceID = "forged-source"

	for _, test := range []struct {
		name string
		item TaskEpisodeEvidence
		want string
	}{
		{name: "rationale", item: rationale, want: ShadowTaskEvidenceExplicitReasoning},
		{name: "correction", item: correction, want: ShadowTaskEvidenceExplicitCorrection},
		{name: "legacy untyped", item: legacy, want: ShadowTaskEvidenceAction},
		{name: "malformed", item: malformed, want: ShadowTaskEvidenceAction},
		{name: "historical version", item: wrongVersion, want: ShadowTaskEvidenceAction},
		{name: "mismatched quote", item: mismatchedQuote, want: ShadowTaskEvidenceAction},
		{name: "forged source identity", item: wrongSourceID, want: ShadowTaskEvidenceAction},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := taskEpisodeEvidenceEvaluationKind(test.item); got != test.want {
				t.Fatalf("kind=%q, want %q", got, test.want)
			}
		})
	}
}

func TestLegacyUntypedReasoningCannotSupportCurrentEvaluationOrLearning(
	t *testing.T,
) {
	store := newTestStore(t)
	episode, run, _ := createCompletedComposeTextPairFixture(t, store)
	legacy, added, err := store.AppendTaskEpisodeEvidence(TaskEpisodeEvidence{
		TaskEpisodeID: episode.ID,
		Role:          TaskEpisodeEvidenceReasoning,
		SourceKind:    "legacy_memory_reasoning",
		SourceID:      "legacy-reasoning-row",
		Summary:       "The user probably preferred a concise update.",
		PayloadJSON:   `{"modelInference":"concise is better"}`,
		ObservedAt:    1_700,
		CreatedAt:     2_200,
	})
	if err != nil || !added {
		t.Fatalf("append legacy reasoning: added=%v err=%v", added, err)
	}

	input, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacyRef := "observed:" + legacy.ID
	found := false
	for _, evidence := range input.Evidence {
		if evidence.ID != legacyRef {
			continue
		}
		found = true
		if evidence.Kind != ShadowTaskEvidenceAction {
			t.Fatalf("legacy evidence kind=%q", evidence.Kind)
		}
	}
	if !found {
		t.Fatalf("legacy evidence %q missing from comparison input", legacyRef)
	}

	output, _, err := normalizeShadowTaskEvaluationOutput(
		input,
		ShadowTaskEvaluationOutput{
			Version:          ShadowTaskEvaluationOutputVersion,
			Summary:          "The evaluator attempted to trust legacy reasoning.",
			Task:             unknownTaskDimension(),
			Boundary:         unknownTaskDimension(),
			Process:          unknownTaskDimension(),
			ResultSimilarity: unknownTaskDimension(),
			ResultQuality:    unknownTaskDimension(),
			Timing:           unknownTaskDimension(),
			Reasoning:        matchedTaskDimension(legacyRef),
			Safety:           unknownTaskDimension(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if output.Reasoning.Assessment != ShadowTaskAssessmentUnknown ||
		output.Reasoning.Score != nil ||
		output.Reasoning.Confidence > shadowTaskUnsupportedReasoningConfidenceCap {
		t.Fatalf("legacy reasoning was trusted: %+v", output.Reasoning)
	}

	projection, err := BuildShadowTaskLearningProjection(
		[]ShadowTaskLearningEvaluationRecord{{
			EvaluationID:        "legacy-reasoning-evaluation",
			PersistedAt:         3_000,
			TaskEpisodeStatus:   TaskEpisodeStatusCompleted,
			ShadowPromptVersion: shadowTaskPromptVersion,
			ComparisonVersion:   ShadowTaskComparatorVersion,
			EvaluatorVersion:    ShadowTaskEvaluatorVersion,
			CapabilityKey:       run.CapabilityKey,
			TaskEpisodeID:       episode.ID,
			ShadowRunID:         run.ID,
			FrozenContextHash:   run.TaskContextHash,
			Input:               input,
			Evaluation:          &output,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range projection.Candidates {
		if candidate.LearningKind == ShadowTaskLearningKindReasoning ||
			candidate.LearningKind == ShadowTaskLearningKindCorrection {
			t.Fatalf("legacy reasoning reached learning: %+v", candidate)
		}
	}
}

func unknownTaskDimension() ShadowTaskEvaluationDimension {
	return ShadowTaskEvaluationDimension{
		Assessment: ShadowTaskAssessmentUnknown,
		Score:      nil, Confidence: 0.2, Reason: "Evidence is unavailable.",
		EvidenceRefs: []string{},
	}
}

func TestBuildAndPersistShadowTaskEvaluationPreservesEightDimensions(t *testing.T) {
	store := newTestStore(t)
	_, run := createExecutedShadowTaskFixture(t, store)
	input, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if input.TaskEpisodeID != run.TaskEpisodeID ||
		input.FrozenContextHash != run.TaskContextHash ||
		len(input.Shadow.Actions) != 0 ||
		len(input.Shadow.Artifacts) != 0 ||
		strings.TrimSpace(input.Shadow.Result) == "" ||
		input.Observed.Result == "" {
		t.Fatalf("evaluation input=%+v", input)
	}
	refs := sortedShadowTaskEvidenceIDs(input)
	if len(refs) == 0 {
		t.Fatal("evaluation input has no evidence")
	}
	output := ShadowTaskEvaluationOutput{
		Version:          ShadowTaskEvaluationOutputVersion,
		Summary:          "The tasks and local draft results were similar.",
		Task:             matchedTaskDimension(refs[0]),
		Boundary:         matchedTaskDimension(refs[0]),
		Process:          matchedTaskDimension(refs[0]),
		ResultSimilarity: matchedTaskDimension(refs[0]),
		ResultQuality:    unknownTaskDimension(),
		Timing:           matchedTaskDimension(refs[0]),
		Reasoning:        unknownTaskDimension(),
		Safety: ShadowTaskEvaluationDimension{
			Assessment: ShadowTaskSafetySafe, Score: shadowScore(1),
			Confidence: 1, Reason: "All writes stayed isolated.",
			EvidenceRefs: []string{},
		},
	}
	prompt, _, err := buildShadowTaskEvaluationPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	result := ShadowTaskEvaluationResult{
		EvaluatorVersion:  ShadowTaskEvaluatorVersion,
		ComparatorVersion: ShadowTaskComparatorVersion,
		InputVersion:      ShadowTaskEvaluationInputVersion,
		ComparatorPrompt:  prompt,
		ComparatorHash:    shadowTaskComparatorHash(prompt),
		EvaluatorModel:    "test-model",
		EvaluatorRaw:      `{"valid":"raw"}`,
		Evaluation:        &output,
	}
	evaluation, added, err := PersistShadowTaskEvaluation(store, input, result, 3_000)
	if err != nil {
		t.Fatal(err)
	}
	if !added || evaluation.TaskEvaluation == nil ||
		evaluation.TaskEvaluation.Task.Assessment != ShadowTaskAssessmentMatched ||
		evaluation.TaskEvaluation.ResultQuality.Score != nil {
		t.Fatalf("evaluation=%+v added=%v", evaluation, added)
	}
	replayed, replayAdded, err := PersistShadowTaskEvaluation(store, input, result, 3_000)
	if err != nil || replayAdded || replayed.ID != evaluation.ID {
		t.Fatalf("replay=%+v added=%v err=%v", replayed, replayAdded, err)
	}
}

func TestShadowTaskEvaluationRejectsHistoricalWorkerGeneration(t *testing.T) {
	store := newTestStore(t)
	_, run := createExecutedShadowTaskFixture(t, store)
	currentInput, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	historical := createHistoricalShadowTaskRunFixture(t, store, run)
	if _, err := BuildShadowTaskEvaluationInput(store, historical.ID); !errors.Is(err, ErrShadowTaskHistoricalGeneration) {
		t.Fatalf("build historical evaluation input err=%v", err)
	}
	input := currentInput
	input.ShadowRunID = historical.ID
	prompt, _, err := buildShadowTaskEvaluationPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	unknown := unknownTaskDimension()
	output := ShadowTaskEvaluationOutput{
		Version:          ShadowTaskEvaluationOutputVersion,
		Summary:          "Historical worker output must remain inspectable only.",
		Task:             unknown,
		Boundary:         unknown,
		Process:          unknown,
		ResultSimilarity: unknown,
		ResultQuality:    unknown,
		Timing:           unknown,
		Reasoning:        unknown,
		Safety:           unknown,
	}
	_, added, err := PersistShadowTaskEvaluation(
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
		3_000,
	)
	if added || !errors.Is(err, ErrShadowTaskHistoricalGeneration) {
		t.Fatalf("persist historical evaluation: added=%v err=%v", added, err)
	}
	evaluations, err := store.ListShadowRunEvaluations(historical.ID)
	if err != nil {
		t.Fatal(err)
	}
	observations, err := store.ListShadowRunObservations(historical.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != 0 || len(observations) != 0 {
		t.Fatalf(
			"historical persistence created state: evaluations=%d observations=%d",
			len(evaluations),
			len(observations),
		)
	}
}

func TestBuildShadowTaskEvaluationUsesMaterializedComposeTextPair(t *testing.T) {
	store := newTestStore(t)
	episode, run, userText := createCompletedComposeTextPairFixture(t, store)
	if episode.CurrentStatus != TaskEpisodeStatusCompleted {
		t.Fatalf("episode status=%q, want completed", episode.CurrentStatus)
	}
	if run.CapabilityKey != TaskWorkflowComposeText {
		t.Fatalf("capability=%q, want %q", run.CapabilityKey, TaskWorkflowComposeText)
	}

	evidence, err := store.ListTaskEpisodeEvidence(episode.ID, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var actionCount, resultCount int
	for _, item := range evidence {
		switch item.Role {
		case TaskEpisodeEvidenceAction:
			actionCount++
		case TaskEpisodeEvidenceResult:
			resultCount++
		}
	}
	if actionCount == 0 || resultCount == 0 {
		t.Fatalf(
			"action=%d result=%d, want separated process and result evidence",
			actionCount,
			resultCount,
		)
	}

	input, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Observed.Result != userText ||
		len(input.Observed.Artifacts) != 1 ||
		input.Observed.Artifacts[0].Digest != sha256Hex([]byte(userText)) {
		t.Fatalf("observed materialized result=%+v", input.Observed)
	}
	if len(input.QualityCriteria) < 6 {
		t.Fatalf("quality criteria=%v, want text composition criteria", input.QualityCriteria)
	}
}

func TestBuildShadowTaskEvaluationUsesMaterializedAnalyzeTextPair(t *testing.T) {
	store := newTestStore(t)
	episode, run, userText := createCompletedAnalyzeTextPairFixture(t, store)
	if episode.CurrentStatus != TaskEpisodeStatusCompleted {
		t.Fatalf("episode status=%q, want completed", episode.CurrentStatus)
	}
	if run.CapabilityKey != TaskWorkflowAnalyzeText {
		t.Fatalf("capability=%q, want %q", run.CapabilityKey, TaskWorkflowAnalyzeText)
	}

	input, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Observed.Result != userText ||
		len(input.Observed.Artifacts) != 1 ||
		input.Observed.Artifacts[0].Digest != sha256Hex([]byte(userText)) {
		t.Fatalf("observed materialized analysis=%+v", input.Observed)
	}
	criteria := strings.Join(input.QualityCriteria, "\n")
	for _, required := range []string{
		"Distinguish evidence-backed findings from uncertainty.",
		"Cover material risks, gaps, or tradeoffs supported by the frozen context.",
		"Avoid invented facts and unsupported causal claims.",
	} {
		if !strings.Contains(criteria, required) {
			t.Fatalf("analysis criteria lack %q: %v", required, input.QualityCriteria)
		}
	}
}

func TestBuildShadowTaskEvaluationSelectsLatestSameDestinationResult(t *testing.T) {
	store := newTestStore(t)
	episode, run, supersededText := createCompletedComposeTextPairFixture(t, store)
	latestText := "The rollout is complete, verified, and ready to announce."
	latest := appendCurrentTaskResultEvidence(
		t,
		store,
		episode.ID,
		TaskWorkflowComposeText,
		"latest-mail-result",
		latestText,
		taskLifecycleTestResultDestination(),
		2_500,
	)

	input, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Observed.Result != latestText ||
		len(input.Observed.Artifacts) != 1 ||
		input.Observed.Artifacts[0].Digest != sha256Hex([]byte(latestText)) {
		t.Fatalf("latest observed result=%+v", input.Observed)
	}
	for _, item := range input.Evidence {
		if strings.Contains(item.Summary, supersededText) {
			t.Fatalf("superseded result text entered comparator evidence: %+v", item)
		}
	}
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.UserResultSHA256 != sha256Hex([]byte(latestText)) ||
		readiness.UserResultDestinationSHA256 == "" ||
		len(readiness.UserResultEvidenceIDs) != 1 ||
		readiness.UserResultEvidenceIDs[0] != latest.ID {
		t.Fatalf("latest pair readiness=%+v", readiness)
	}
}

func TestBuildShadowTaskEvaluationRejectsCrossDestinationResults(t *testing.T) {
	store := newTestStore(t)
	episode, run, _ := createCompletedComposeTextPairFixture(t, store)
	conflicting := appendCurrentTaskResultEvidence(
		t,
		store,
		episode.ID,
		TaskWorkflowComposeText,
		"slack-result",
		"A separate conclusion entered in Slack.",
		taskLifecycleTestDestination(
			"com.tinyspeck.slackmacgap",
			"Slack",
			"Customer channel",
		),
		2_500,
	)

	if _, err := BuildShadowTaskEvaluationInput(store, run.ID); !errors.Is(
		err,
		ErrShadowTaskObservedResultUnavailable,
	) || !strings.Contains(err.Error(), "conflicting_user_result_destinations") {
		t.Fatalf("cross-destination evaluation error=%v", err)
	}
	readiness, err := BuildShadowTaskPairReadiness(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Ready ||
		readiness.State != ShadowTaskPairStateUserResultUnavailable ||
		len(readiness.InvalidUserResultEvidenceIDs) < 2 ||
		!containsString(readiness.InvalidUserResultEvidenceIDs, conflicting.ID) ||
		!containsString(readiness.Reasons, "conflicting_user_result_destinations") {
		t.Fatalf("cross-destination readiness=%+v", readiness)
	}
}

func TestTaskEpisodeMaterializedResultRejectsCrossWorkflowReuse(t *testing.T) {
	content := "A source-backed result."
	payload, err := json.Marshal(TaskLifecycleObservation{
		MaterializedResult: &TaskLifecycleMaterializedResult{
			Workflow:     TaskWorkflowComposeText,
			Kind:         TaskLifecycleContentCommittedText,
			Content:      content,
			SHA256:       sha256Hex([]byte(content)),
			EvidenceRefs: []string{"suffix:return"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = taskEpisodeEvidenceMaterializedResult(
		TaskEpisodeEvidence{PayloadJSON: string(payload)},
		TaskWorkflowAnalyzeText,
	)
	if err == nil || !strings.Contains(err.Error(), "does not match expected workflow") {
		t.Fatalf("cross-workflow error=%v", err)
	}
}

func TestTaskEpisodeMaterializedResultKeepsLegacyDestinationOutOfComparison(
	t *testing.T,
) {
	content := "A historical source-backed result."
	payload, err := json.Marshal(TaskLifecycleObservation{
		MaterializedResult: &TaskLifecycleMaterializedResult{
			Workflow:     TaskWorkflowComposeText,
			Kind:         TaskLifecycleContentCommittedText,
			Content:      content,
			SHA256:       sha256Hex([]byte(content)),
			EvidenceRefs: []string{"suffix:return"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := taskEpisodeEvidenceMaterializedResult(
		TaskEpisodeEvidence{PayloadJSON: string(payload)},
		TaskWorkflowComposeText,
	)
	if err != nil || result != nil {
		t.Fatalf("legacy result=%+v err=%v, want historical-only", result, err)
	}
}

func TestTaskEpisodeMaterializedResultRejectsDestinationDigestTampering(
	t *testing.T,
) {
	content := "A current source-backed result."
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
			EvidenceRefs:      []string{"suffix:return"},
			Destination:       destination,
			DestinationSHA256: destinationSHA256,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	materialized := envelope["materializedResult"].(map[string]any)
	materializedDestination := materialized["destination"].(map[string]any)
	materializedDestination["windowTitle"] = "Tampered destination"
	tampered, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	_, err = taskEpisodeEvidenceMaterializedResult(
		TaskEpisodeEvidence{PayloadJSON: string(tampered)},
		TaskWorkflowComposeText,
	)
	if err == nil || !strings.Contains(err.Error(), "destination digest does not match") {
		t.Fatalf("destination tamper error=%v", err)
	}
}

func TestTaskReasoningFlowsFromPredictionAndCorrectionIntoEvaluation(
	t *testing.T,
) {
	store := newTestStore(t)
	episode := createReasoningShadowTaskEpisodeFixture(t, store)
	app := &App{
		runner:   &shadowExecutionFakeRunner{},
		activity: &activityMonitorState{store: store},
	}
	run, _, _, err := app.executeTaskEpisodeShadow(
		context.Background(),
		episode.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	userText := "Actually, I changed direction because the customer needs certainty."
	lifecycleInput := TaskLifecycleInput{
		SourceKind:     "activity",
		SourceID:       "reasoning-completion-window",
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
			EvidenceRefs:       []string{"prefix"},
		}},
		PrefixEvidence: []TaskLifecycleEvidence{{
			Ref:        "prefix",
			Kind:       "onset",
			SourceKind: "event",
			SourceID:   "onset",
			OccurredAt: 1_000,
			Summary:    "The user began composing.",
		}},
		SuffixEvidence: []TaskLifecycleEvidence{
			{
				Ref:         "text",
				Kind:        TaskLifecycleEvidenceTextCommit,
				SourceKind:  "activity_event",
				SourceID:    "typed",
				OccurredAt:  1_500,
				Summary:     "The user entered and committed the proposal.",
				ContentKind: TaskLifecycleContentCommittedText,
				Content:     userText,
				Actor:       TaskLifecycleActorUser,
				Destination: taskLifecycleTestResultDestination(),
			},
			{
				Ref:        "send",
				Kind:       "sent",
				SourceKind: "activity_event",
				SourceID:   "send",
				OccurredAt: 1_600,
				Summary:    "The user sent the proposal.",
				Actor:      TaskLifecycleActorSystem,
			},
		},
	}
	lifecycleResponse := TaskLifecycleResponse{
		Observations: []TaskLifecycleObservation{{
			LocalKey:         "complete",
			Kind:             TaskLifecycleKindCompleted,
			EpisodeIDs:       []string{episode.ID},
			Confidence:       0.9,
			ResultSummary:    "The proposal was sent.",
			ResultConfidence: 0.9,
			EvidenceRefs:     []string{"text", "send"},
		}},
		ReasoningObservations: []TaskLifecycleReasoningObservation{{
			LocalKey:    "correction",
			EpisodeID:   episode.ID,
			Kind:        TaskLifecycleReasoningCorrection,
			Statement:   "A model paraphrase that must be discarded.",
			SourceQuote: "Actually, I changed direction",
			Confidence:  0.9,
			EvidenceRefs: []string{
				"text",
			},
		}},
	}
	if _, err := PersistTaskLifecycleObservations(
		store,
		lifecycleInput,
		lifecycleResponse,
		2_100,
	); err != nil {
		t.Fatal(err)
	}

	input, err := BuildShadowTaskEvaluationInput(store, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.PredictedReasoning) != 1 ||
		input.PredictedReasoning[0].Statement !=
			run.Prediction.ReasoningHypotheses[0].Statement {
		t.Fatalf("predicted reasoning=%+v", input.PredictedReasoning)
	}
	correctionRef := ""
	for _, item := range input.Evidence {
		if item.Kind == ShadowTaskEvidenceExplicitCorrection {
			correctionRef = item.ID
			if item.Summary != "Actually, I changed direction" {
				t.Fatalf("correction summary=%q", item.Summary)
			}
		}
	}
	if correctionRef == "" {
		t.Fatalf("correction evidence missing: %+v", input.Evidence)
	}

	output := ShadowTaskEvaluationOutput{
		Version:          ShadowTaskEvaluationOutputVersion,
		Summary:          "The explicit correction changed the predicted reason.",
		Task:             unknownTaskDimension(),
		Boundary:         unknownTaskDimension(),
		Process:          unknownTaskDimension(),
		ResultSimilarity: unknownTaskDimension(),
		ResultQuality:    unknownTaskDimension(),
		Timing:           unknownTaskDimension(),
		Reasoning:        matchedTaskDimension(correctionRef),
		Safety:           unknownTaskDimension(),
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
			EvaluatorRaw:      `{"reasoning":"source-backed"}`,
			Evaluation:        &output,
		},
		3_000,
	)
	if err != nil || !added {
		t.Fatalf("persist reasoning evaluation: added=%v err=%v", added, err)
	}
	observations, err := store.ListShadowRunObservations(run.ID)
	if err != nil || len(observations) != 1 ||
		len(observations[0].ReasoningEvidence) != 1 ||
		observations[0].ReasoningEvidence[0].Kind != "correction" {
		t.Fatalf("reasoning observations=%+v err=%v evaluation=%+v", observations, err, evaluation)
	}
}
