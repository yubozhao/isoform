package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"yansu-agent/agent"
)

type taskLifecycleFakeRunner struct {
	response  string
	streamErr string
	runErr    error
	input     *agent.TaskInput
}

func (r *taskLifecycleFakeRunner) Init(context.Context) error { return nil }
func (r *taskLifecycleFakeRunner) IsReady() bool              { return true }
func (r *taskLifecycleFakeRunner) ExecuteTask(
	_ context.Context,
	input *agent.TaskInput,
	callback agent.StreamCallback,
) (string, error) {
	r.input = input
	if r.streamErr != "" {
		if err := callback(&agent.StreamEvent{
			Type:  "error",
			Error: r.streamErr,
		}); err != nil {
			return "", err
		}
	}
	if r.response != "" {
		if err := callback(&agent.StreamEvent{
			Type:    "assistant",
			Content: r.response,
		}); err != nil {
			return "", err
		}
	}
	return "task-lifecycle-run", r.runErr
}
func (r *taskLifecycleFakeRunner) CancelRun(string) error { return nil }
func (r *taskLifecycleFakeRunner) RespondToPermission(string, string) error {
	return nil
}
func (r *taskLifecycleFakeRunner) CancelPermission(string) {}
func (r *taskLifecycleFakeRunner) Shutdown() error         { return nil }

func taskLifecycleFixture() TaskLifecycleInput {
	return TaskLifecycleInput{
		SourceKind:     "activity_window",
		SourceID:       "window-1",
		PrefixCutoffAt: 2_000,
		CutoffAt:       5_000,
		PrefixEvidence: []TaskLifecycleEvidence{
			{
				Ref:        "prefix:incident",
				Kind:       "onset",
				SourceKind: "activity_event",
				SourceID:   "event-incident-onset",
				OccurredAt: 1_000,
				Summary:    "The user began investigating the incident.",
			},
			{
				Ref:        "prefix:update",
				Kind:       "onset",
				SourceKind: "activity_event",
				SourceID:   "event-update-onset",
				OccurredAt: 1_500,
				Summary:    "The user began drafting a customer update.",
			},
		},
		SuffixEvidence: []TaskLifecycleEvidence{
			{
				Ref:        "suffix:one",
				Kind:       "action",
				SourceKind: "activity_event",
				SourceID:   "event-one",
				OccurredAt: 3_000,
				Summary:    "The user edited the incident notes.",
			},
			{
				Ref:        "suffix:two",
				Kind:       "action",
				SourceKind: "activity_event",
				SourceID:   "event-two",
				OccurredAt: 4_000,
				Summary:    "The user returned to the customer update.",
			},
		},
		Episodes: []TaskLifecycleEpisodeSnapshot{
			{
				EpisodeID:          "episode-incident",
				CurrentStatus:      TaskEpisodeStatusActive,
				InferredStartedAt:  1_000,
				Task:               "Investigate the incident",
				DesiredResult:      "Identify and verify the cause",
				TaskConfidence:     0.9,
				BoundaryConfidence: 0.8,
				EvidenceRefs:       []string{"prefix:incident"},
			},
			{
				EpisodeID:          "episode-update",
				CurrentStatus:      TaskEpisodeStatusPaused,
				InferredStartedAt:  1_500,
				Task:               "Prepare a customer update",
				DesiredResult:      "A factual update ready to send",
				TaskConfidence:     0.8,
				BoundaryConfidence: 0.7,
				EvidenceRefs:       []string{"prefix:update"},
			},
		},
	}
}

func TestTaskLifecycleFalseCompletionBecomesUnknown(t *testing.T) {
	input := taskLifecycleFixture()
	input.SuffixEvidence = []TaskLifecycleEvidence{{
		Ref:        "suffix:app-switch",
		Kind:       "app_switch",
		SourceKind: "activity_event",
		SourceID:   "event-app-switch",
		OccurredAt: 3_000,
		Summary:    "The user switched from the editor to chat.",
	}}

	got := normalizeTaskLifecycleResponse(input, TaskLifecycleResponse{
		Observations: []TaskLifecycleObservation{{
			LocalKey:           "incident-done",
			Kind:               TaskLifecycleKindCompleted,
			EpisodeIDs:         []string{"episode-incident"},
			OccurredAt:         4_999,
			Confidence:         0.92,
			BoundaryConfidence: 0.8,
			ResultSummary:      "The incident was resolved.",
			ResultConfidence:   0.9,
			EvidenceRefs:       []string{"suffix:app-switch"},
			Reason:             "The user left the editor.",
		}},
	})

	if len(got.Observations) != 1 {
		t.Fatalf("observations=%+v, want one explicit unknown", got.Observations)
	}
	observation := got.Observations[0]
	if observation.Kind != TaskLifecycleKindUnknown ||
		observation.Confidence != 0 ||
		observation.ResultSummary != "" {
		t.Fatalf("false completion was not contained: %+v", observation)
	}
	if observation.OccurredAt != 3_000 {
		t.Fatalf("occurredAt=%d, want source-backed timestamp 3000", observation.OccurredAt)
	}
	if len(observation.Alternatives) == 0 ||
		observation.Alternatives[0].Kind != TaskLifecycleKindCompleted {
		t.Fatalf("original completion was not retained as an alternative: %+v", observation)
	}
}

func TestTaskLifecycleReasoningAcceptsOnlyLiteralUserAuthoredEvidence(
	t *testing.T,
) {
	input := taskLifecycleFixture()
	input.SuffixEvidence = []TaskLifecycleEvidence{
		{
			Ref:         "suffix:user-reason",
			Kind:        "keyboard",
			SourceKind:  "activity_event",
			SourceID:    "user-reason",
			OccurredAt:  3_000,
			Summary:     "The user entered a rationale.",
			ContentKind: TaskLifecycleContentCommittedText,
			Content: "I am checking the details because the customer needs " +
				"an accurate answer.",
			Actor: TaskLifecycleActorUser,
		},
		{
			Ref:        "suffix:other-party",
			Kind:       "speech",
			SourceKind: "activity_event",
			SourceID:   "other-party",
			OccurredAt: 3_500,
			Summary:    `Other-party speech: "We need this because it is urgent."`,
			Actor:      TaskLifecycleActorOtherParty,
		},
		{
			Ref:         "suffix:user-correction",
			Kind:        "keyboard",
			SourceKind:  "activity_event",
			SourceID:    "user-correction",
			OccurredAt:  4_000,
			Summary:     "The user corrected the approach.",
			ContentKind: TaskLifecycleContentCommittedText,
			Content:     "Actually, I changed direction and will verify first.",
			Actor:       TaskLifecycleActorUser,
		},
	}
	got := normalizeTaskLifecycleResponse(input, TaskLifecycleResponse{
		ReasoningObservations: []TaskLifecycleReasoningObservation{
			{
				LocalKey:    "reason",
				EpisodeID:   "episode-incident",
				Kind:        TaskLifecycleReasoningStatedRationale,
				Statement:   "The model's paraphrase must not be retained.",
				SourceQuote: "because the customer needs an accurate answer",
				Confidence:  0.94,
				EvidenceRefs: []string{
					"suffix:user-reason",
				},
			},
			{
				LocalKey:    "other-party",
				EpisodeID:   "episode-incident",
				Kind:        TaskLifecycleReasoningStatedRationale,
				SourceQuote: "because it is urgent",
				Confidence:  0.9,
				EvidenceRefs: []string{
					"suffix:other-party",
				},
			},
			{
				LocalKey:    "invented",
				EpisodeID:   "episode-incident",
				Kind:        TaskLifecycleReasoningStatedRationale,
				SourceQuote: "because speed matters most",
				Confidence:  0.8,
				EvidenceRefs: []string{
					"suffix:user-reason",
				},
			},
			{
				LocalKey:    "correction",
				EpisodeID:   "episode-incident",
				Kind:        TaskLifecycleReasoningCorrection,
				Statement:   "A changed approach.",
				SourceQuote: "Actually, I changed direction",
				Confidence:  0.88,
				EvidenceRefs: []string{
					"suffix:user-correction",
				},
			},
		},
	})
	if len(got.Observations) != 0 || len(got.ReasoningObservations) != 2 {
		t.Fatalf("normalized reasoning=%+v", got)
	}
	if got.ReasoningObservations[0].Statement !=
		got.ReasoningObservations[0].SourceQuote ||
		got.ReasoningObservations[0].OccurredAt != 3_000 ||
		got.ReasoningObservations[1].Kind !=
			TaskLifecycleReasoningCorrection {
		t.Fatalf("reasoning provenance=%+v", got.ReasoningObservations)
	}
}

func TestTaskLifecycleAcceptsSourcedCompletionAndAbandonment(t *testing.T) {
	input := taskLifecycleFixture()
	input.SuffixEvidence = []TaskLifecycleEvidence{
		{
			Ref:        "suffix:submission",
			Kind:       "submission",
			SourceKind: "activity_event",
			SourceID:   "event-submission",
			OccurredAt: 3_000,
			Summary:    "The incident analysis was submitted with a verified cause.",
		},
		{
			Ref:        "suffix:cancelled",
			Kind:       "explicit_abandonment",
			SourceKind: "activity_event",
			SourceID:   "event-cancelled",
			OccurredAt: 4_000,
			Summary:    "The user explicitly cancelled the customer update.",
		},
	}
	normalized, err := normalizeTaskLifecycleInput(input)
	if err != nil {
		t.Fatal(err)
	}
	got := normalizeTaskLifecycleResponse(normalized, TaskLifecycleResponse{
		Observations: []TaskLifecycleObservation{
			{
				LocalKey:           "cancel-update",
				Kind:               TaskLifecycleKindAbandoned,
				EpisodeIDs:         []string{"episode-update"},
				Confidence:         0.95,
				BoundaryConfidence: 0.9,
				EvidenceRefs:       []string{"suffix:cancelled"},
			},
			{
				LocalKey:           "complete-incident",
				Kind:               TaskLifecycleKindCompleted,
				EpisodeIDs:         []string{"episode-incident"},
				Confidence:         0.92,
				BoundaryConfidence: 0.88,
				ResultSummary:      "The verified incident cause was submitted.",
				ResultConfidence:   0.9,
				EvidenceRefs:       []string{"suffix:submission"},
			},
		},
	})

	if len(got.Observations) != 2 {
		t.Fatalf("observations=%+v, want sourced completion and abandonment", got.Observations)
	}
	if got.Observations[0].Kind != TaskLifecycleKindCompleted ||
		got.Observations[0].ResultSummary == "" ||
		got.Observations[1].Kind != TaskLifecycleKindAbandoned {
		t.Fatalf("supported lifecycle changes were degraded: %+v", got.Observations)
	}
}

func TestTaskLifecycleCandidateCanContinueAndComplete(t *testing.T) {
	t.Run("continuation activates candidate", func(t *testing.T) {
		input := taskLifecycleFixture()
		input.Episodes = []TaskLifecycleEpisodeSnapshot{{
			EpisodeID:          "episode-incident",
			CurrentStatus:      TaskEpisodeStatusCandidate,
			InferredStartedAt:  1_000,
			Task:               "Investigate the incident",
			DesiredResult:      "Identify and verify the cause",
			TaskConfidence:     0.9,
			BoundaryConfidence: 0.8,
			EvidenceRefs:       []string{"prefix:incident"},
		}}
		input.PrefixEvidence = input.PrefixEvidence[:1]
		input.SuffixEvidence = []TaskLifecycleEvidence{{
			Ref:        "suffix:typing",
			Kind:       "keyboard",
			SourceKind: "activity_event",
			SourceID:   "event-typing",
			OccurredAt: 3_000,
			Summary:    "The user continued writing the incident analysis.",
		}}

		got := normalizeTaskLifecycleResponse(input, TaskLifecycleResponse{
			Observations: []TaskLifecycleObservation{{
				LocalKey:           "continue-incident",
				Kind:               TaskLifecycleKindResumed,
				EpisodeIDs:         []string{"episode-incident"},
				Confidence:         0.88,
				BoundaryConfidence: 0.82,
				EvidenceRefs:       []string{"suffix:typing"},
				Reason:             "The observed work continued.",
			}},
		})

		if len(got.Observations) != 1 ||
			got.Observations[0].Kind != TaskLifecycleKindContinued {
			t.Fatalf("candidate continuation=%+v, want continued", got.Observations)
		}
	})

	t.Run("strong result closes candidate", func(t *testing.T) {
		input := taskLifecycleFixture()
		input.Episodes[0].CurrentStatus = TaskEpisodeStatusCandidate
		input.Episodes = input.Episodes[:1]
		input.PrefixEvidence = input.PrefixEvidence[:1]
		input.SuffixEvidence = []TaskLifecycleEvidence{{
			Ref:        "suffix:submission",
			Kind:       "submission",
			SourceKind: "activity_event",
			SourceID:   "event-submission",
			OccurredAt: 3_000,
			Summary:    "The incident analysis was submitted.",
		}}

		got := normalizeTaskLifecycleResponse(input, TaskLifecycleResponse{
			Observations: []TaskLifecycleObservation{{
				LocalKey:           "complete-incident",
				Kind:               TaskLifecycleKindCompleted,
				EpisodeIDs:         []string{"episode-incident"},
				Confidence:         0.92,
				BoundaryConfidence: 0.86,
				ResultSummary:      "The incident analysis was submitted.",
				ResultConfidence:   0.9,
				EvidenceRefs:       []string{"suffix:submission"},
			}},
		})

		if len(got.Observations) != 1 ||
			got.Observations[0].Kind != TaskLifecycleKindCompleted {
			t.Fatalf("candidate completion=%+v, want completed", got.Observations)
		}
	})
}

func TestTaskLifecycleMaterializesComposeTextResult(t *testing.T) {
	input := taskLifecycleFixture()
	input.Episodes = []TaskLifecycleEpisodeSnapshot{{
		EpisodeID:          "episode-update",
		CurrentStatus:      TaskEpisodeStatusCandidate,
		InferredStartedAt:  1_500,
		Task:               "Compose a customer update",
		DesiredResult:      "A factual update ready to send",
		TaskConfidence:     0.9,
		BoundaryConfidence: 0.8,
		EvidenceRefs:       []string{"prefix:update"},
	}}
	input.PrefixEvidence = input.PrefixEvidence[1:]
	input.SuffixEvidence = []TaskLifecycleEvidence{
		{
			Ref:              "suffix:text",
			Kind:             TaskLifecycleEvidenceTextCommit,
			SourceKind:       "activity_event",
			SourceID:         "event-text",
			OccurredAt:       3_000,
			Summary:          "The user entered the customer update.",
			ContentKind:      TaskLifecycleContentCommittedText,
			Content:          "The rollout is complete and verification passed.",
			ContentTruncated: false,
			Actor:            TaskLifecycleActorUser,
			Destination:      taskLifecycleTestResultDestination(),
		},
		{
			Ref:        "suffix:send",
			Kind:       "sent",
			SourceKind: "activity_event",
			SourceID:   "event-send",
			OccurredAt: 3_100,
			Summary:    "The user sent the customer update.",
		},
	}

	got := normalizeTaskLifecycleResponse(input, TaskLifecycleResponse{
		Observations: []TaskLifecycleObservation{{
			LocalKey:           "complete-update",
			Kind:               TaskLifecycleKindCompleted,
			EpisodeIDs:         []string{"episode-update"},
			Confidence:         0.92,
			BoundaryConfidence: 0.86,
			ResultSummary:      "A customer update was sent.",
			ResultConfidence:   0.9,
			EvidenceRefs:       []string{"suffix:text", "suffix:send"},
		}},
	})

	if len(got.Observations) != 1 {
		t.Fatalf("observations=%+v", got.Observations)
	}
	result := got.Observations[0].MaterializedResult
	if result == nil ||
		result.Workflow != TaskWorkflowComposeText ||
		result.Content != "The rollout is complete and verification passed." ||
		result.SHA256 != sha256Hex([]byte(result.Content)) ||
		result.Destination == nil ||
		result.Destination.SemanticRole !=
			TaskLifecycleDestinationUserResultCandidate ||
		result.DestinationSHA256 == "" ||
		result.Truncated {
		t.Fatalf("materialized result=%+v", result)
	}
	if !strings.Contains(got.Observations[0].ResultSummary, result.Content) {
		t.Fatalf("result summary=%q, want committed text", got.Observations[0].ResultSummary)
	}
}

func TestTaskLifecycleTextCommitCandidateRequiresTextWorkflowAndSameEventContent(
	t *testing.T,
) {
	fixture := func(task, desiredResult, content string) TaskLifecycleInput {
		input := taskLifecycleFixture()
		input.Episodes = []TaskLifecycleEpisodeSnapshot{{
			EpisodeID:          "episode-update",
			CurrentStatus:      TaskEpisodeStatusCandidate,
			InferredStartedAt:  1_500,
			Task:               task,
			DesiredResult:      desiredResult,
			TaskConfidence:     0.9,
			BoundaryConfidence: 0.8,
			EvidenceRefs:       []string{"prefix:update"},
		}}
		input.PrefixEvidence = input.PrefixEvidence[1:]
		input.SuffixEvidence = []TaskLifecycleEvidence{{
			Ref:         "suffix:return",
			Kind:        TaskLifecycleEvidenceTextCommit,
			SourceKind:  "activity_event",
			SourceID:    "event-return",
			OccurredAt:  3_000,
			Summary:     "The keyboard burst ended with Return.",
			ContentKind: TaskLifecycleContentCommittedText,
			Content:     content,
			Actor:       TaskLifecycleActorUser,
			Destination: taskLifecycleTestResultDestination(),
		}}
		return input
	}
	response := TaskLifecycleResponse{Observations: []TaskLifecycleObservation{{
		LocalKey:           "complete-update",
		Kind:               TaskLifecycleKindCompleted,
		EpisodeIDs:         []string{"episode-update"},
		Confidence:         0.9,
		BoundaryConfidence: 0.8,
		ResultSummary:      "Submitted text.",
		ResultConfidence:   0.8,
		EvidenceRefs:       []string{"suffix:return"},
	}}}

	t.Run("compose text with retained content", func(t *testing.T) {
		input := fixture(
			"Compose a customer update",
			"A factual update ready to send",
			"The rollout is complete.",
		)
		input.SuffixEvidence[0].Destination =
			taskLifecycleTestSendOnReturnDestination()
		got := normalizeTaskLifecycleResponse(
			input,
			response,
		)
		if len(got.Observations) != 1 ||
			got.Observations[0].Kind != TaskLifecycleKindCompleted ||
			got.Observations[0].MaterializedResult == nil ||
			got.Observations[0].MaterializedResult.Content !=
				"The rollout is complete." {
			t.Fatalf("supported commit=%+v", got.Observations)
		}
	})

	t.Run("analyze text with retained content", func(t *testing.T) {
		input := fixture(
			"Review the supplied proposal",
			"Findings that identify the material gaps and risks",
			"The pricing assumption is unsupported by the supplied evidence.",
		)
		input.SuffixEvidence[0].Destination =
			taskLifecycleTestSendOnReturnDestination()
		got := normalizeTaskLifecycleResponse(
			input,
			response,
		)
		if len(got.Observations) != 1 ||
			got.Observations[0].Kind != TaskLifecycleKindCompleted ||
			got.Observations[0].MaterializedResult == nil ||
			got.Observations[0].MaterializedResult.Workflow !=
				TaskWorkflowAnalyzeText ||
			got.Observations[0].MaterializedResult.Content !=
				"The pricing assumption is unsupported by the supplied evidence." {
			t.Fatalf("supported analysis commit=%+v", got.Observations)
		}
	})

	t.Run("editor Return alone is not terminal", func(t *testing.T) {
		got := normalizeTaskLifecycleResponse(
			fixture(
				"Compose a customer update",
				"A factual update ready to send",
				"The first paragraph is drafted.",
			),
			response,
		)
		if len(got.Observations) != 1 ||
			got.Observations[0].Kind != TaskLifecycleKindUnknown ||
			got.Observations[0].MaterializedResult != nil {
			t.Fatalf("editor Return escaped=%+v", got.Observations)
		}
	})

	t.Run("other party text cannot become user result", func(t *testing.T) {
		input := fixture(
			"Review the supplied proposal",
			"Findings that identify the material gaps and risks",
			"The proposal is approved.",
		)
		input.SuffixEvidence[0].Actor = TaskLifecycleActorOtherParty
		got := normalizeTaskLifecycleResponse(input, response)
		if len(got.Observations) != 1 ||
			got.Observations[0].Kind != TaskLifecycleKindUnknown ||
			got.Observations[0].MaterializedResult != nil {
			t.Fatalf("other-party result escaped=%+v", got.Observations)
		}
	})

	for _, appName := range []string{"ChatGPT", "Grok", "Yansu"} {
		t.Run("delegated ai prompt in "+appName+" cannot become user result", func(t *testing.T) {
			input := fixture(
				"Review the supplied proposal",
				"Findings that identify the material gaps and risks",
				"Identify the proposal's material gaps and risks.",
			)
			input.SuffixEvidence[0].Summary =
				`Entered text in Mail: "Identify the gaps"`
			input.SuffixEvidence[0].Destination = taskLifecycleTestDestination(
				"",
				appName,
				"Proposal review",
			)
			got := normalizeTaskLifecycleResponse(input, response)
			if len(got.Observations) != 1 ||
				got.Observations[0].Kind != TaskLifecycleKindUnknown ||
				got.Observations[0].MaterializedResult != nil {
				t.Fatalf("delegated AI prompt escaped=%+v", got.Observations)
			}
		})
	}

	t.Run("summary wording cannot reject a structured user result", func(t *testing.T) {
		input := fixture(
			"Review the supplied proposal",
			"Findings that identify the material gaps and risks",
			"The pricing assumption is unsupported.",
		)
		input.SuffixEvidence[0].Summary =
			`Entered text in ChatGPT: "Review the proposal"`
		input.SuffixEvidence[0].Destination =
			taskLifecycleTestSendOnReturnDestination()
		got := normalizeTaskLifecycleResponse(input, response)
		if len(got.Observations) != 1 ||
			got.Observations[0].MaterializedResult == nil {
			t.Fatalf("summary overrode structured destination=%+v", got.Observations)
		}
	})

	for _, test := range []struct {
		name        string
		destination func() *TaskLifecycleResultDestination
	}{
		{
			name: "legacy destination",
			destination: func() *TaskLifecycleResultDestination {
				return nil
			},
		},
		{
			name: "generic browser destination",
			destination: func() *TaskLifecycleResultDestination {
				return taskLifecycleTestDestination(
					"com.apple.Safari",
					"Safari",
					"Customer proposal",
				)
			},
		},
		{
			name: "wrong capture method",
			destination: func() *TaskLifecycleResultDestination {
				destination := taskLifecycleTestResultDestination()
				destination.CaptureMethod = "key_tokens"
				return destination
			},
		},
	} {
		t.Run(test.name+" cannot become user result", func(t *testing.T) {
			input := fixture(
				"Review the supplied proposal",
				"Findings that identify the material gaps and risks",
				"The pricing assumption is unsupported.",
			)
			input.SuffixEvidence[0].Destination = test.destination()
			got := normalizeTaskLifecycleResponse(input, response)
			if len(got.Observations) != 1 ||
				got.Observations[0].Kind != TaskLifecycleKindUnknown ||
				got.Observations[0].MaterializedResult != nil {
				t.Fatalf("inadmissible destination escaped=%+v", got.Observations)
			}
		})
	}

	t.Run("nearby delegated prompt is not swept into a valid result", func(t *testing.T) {
		input := fixture(
			"Review the supplied proposal",
			"Findings that identify the material gaps and risks",
			"The pricing assumption is unsupported.",
		)
		input.SuffixEvidence[0].Destination =
			taskLifecycleTestSendOnReturnDestination()
		delegated := input.SuffixEvidence[0]
		delegated.Ref = "suffix:yansu-prompt"
		delegated.SourceID = "event-yansu-prompt"
		delegated.OccurredAt++
		delegated.Summary = `Entered text in Yansu / New chat: "Summarize this"`
		delegated.Content = "Summarize this analysis for me."
		delegated.Destination = taskLifecycleTestDestination(
			"com.wails.Yansu",
			"Yansu",
			"New chat",
		)
		input.SuffixEvidence = append(input.SuffixEvidence, delegated)
		mixedResponse := response
		mixedResponse.Observations = append(
			[]TaskLifecycleObservation(nil),
			response.Observations...,
		)
		mixedResponse.Observations[0].EvidenceRefs = []string{
			"suffix:return",
			"suffix:yansu-prompt",
		}

		got := normalizeTaskLifecycleResponse(input, mixedResponse)
		if len(got.Observations) != 1 ||
			got.Observations[0].MaterializedResult == nil ||
			got.Observations[0].MaterializedResult.Content !=
				"The pricing assumption is unsupported." ||
			len(got.Observations[0].MaterializedResult.EvidenceRefs) != 1 {
			t.Fatalf("mixed result evidence escaped=%+v", got.Observations)
		}
	})

	t.Run("multiple user destinations cannot form one result", func(t *testing.T) {
		input := fixture(
			"Review the supplied proposal",
			"Findings that identify the material gaps and risks",
			"The pricing assumption is unsupported.",
		)
		second := input.SuffixEvidence[0]
		second.Ref = "suffix:slack-return"
		second.SourceID = "event-slack-return"
		second.OccurredAt++
		second.Content = "A separate Slack conclusion."
		second.Destination = taskLifecycleTestDestination(
			"com.tinyspeck.slackmacgap",
			"Slack",
			"Customer channel",
		)
		input.SuffixEvidence = append(input.SuffixEvidence, second)
		mixedResponse := response
		mixedResponse.Observations = append(
			[]TaskLifecycleObservation(nil),
			response.Observations...,
		)
		mixedResponse.Observations[0].EvidenceRefs = []string{
			"suffix:return",
			"suffix:slack-return",
		}

		got := normalizeTaskLifecycleResponse(input, mixedResponse)
		if len(got.Observations) != 1 ||
			got.Observations[0].Kind != TaskLifecycleKindUnknown ||
			got.Observations[0].MaterializedResult != nil {
			t.Fatalf("cross-destination result escaped=%+v", got.Observations)
		}
	})

	t.Run("explicit completion can close but not combine destinations", func(t *testing.T) {
		input := fixture(
			"Review the supplied proposal",
			"Findings that identify the material gaps and risks",
			"The pricing assumption is unsupported.",
		)
		second := input.SuffixEvidence[0]
		second.Ref = "suffix:slack-return"
		second.SourceID = "event-slack-return"
		second.OccurredAt++
		second.Content = "A separate Slack conclusion."
		second.Destination = taskLifecycleTestDestination(
			"com.tinyspeck.slackmacgap",
			"Slack",
			"Customer channel",
		)
		terminal := input.SuffixEvidence[0]
		terminal.Ref = "suffix:sent"
		terminal.SourceID = "event-sent"
		terminal.OccurredAt += 2
		terminal.Kind = "sent"
		terminal.ContentKind = ""
		terminal.Content = ""
		terminal.Actor = ""
		terminal.Destination = nil
		input.SuffixEvidence = append(input.SuffixEvidence, second, terminal)
		mixedResponse := response
		mixedResponse.Observations = append(
			[]TaskLifecycleObservation(nil),
			response.Observations...,
		)
		mixedResponse.Observations[0].EvidenceRefs = []string{
			"suffix:return",
			"suffix:slack-return",
			"suffix:sent",
		}

		got := normalizeTaskLifecycleResponse(input, mixedResponse)
		if len(got.Observations) != 1 ||
			got.Observations[0].Kind != TaskLifecycleKindCompleted ||
			got.Observations[0].ResultSummary != "" ||
			got.Observations[0].MaterializedResult != nil {
			t.Fatalf("cross-destination completion escaped=%+v", got.Observations)
		}
	})

	for _, test := range []struct {
		name    string
		task    string
		result  string
		content string
	}{
		{
			name:    "non text result task",
			task:    "Review images in a social post carousel",
			result:  "Select the strongest image",
			content: "Looks complete.",
		},
		{
			name:    "bare Return",
			task:    "Compose a customer update",
			result:  "A factual update ready to send",
			content: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeTaskLifecycleResponse(
				fixture(test.task, test.result, test.content),
				response,
			)
			if len(got.Observations) != 1 ||
				got.Observations[0].Kind != TaskLifecycleKindUnknown ||
				got.Observations[0].MaterializedResult != nil {
				t.Fatalf("unsupported commit escaped=%+v", got.Observations)
			}
		})
	}
}

func TestTaskLifecycleInterleavedEpisodesRemainIndependent(t *testing.T) {
	input, err := normalizeTaskLifecycleInput(taskLifecycleFixture())
	if err != nil {
		t.Fatal(err)
	}
	got := normalizeTaskLifecycleResponse(input, TaskLifecycleResponse{
		Observations: []TaskLifecycleObservation{
			{
				LocalKey:           "resume-update",
				Kind:               TaskLifecycleKindResumed,
				EpisodeIDs:         []string{"episode-update"},
				Confidence:         0.83,
				BoundaryConfidence: 0.72,
				EvidenceRefs:       []string{"suffix:two"},
				Reason:             "Editing resumed in the update.",
			},
			{
				LocalKey:           "pause-incident",
				Kind:               TaskLifecycleKindPaused,
				EpisodeIDs:         []string{"episode-incident"},
				Confidence:         0.78,
				BoundaryConfidence: 0.67,
				EvidenceRefs:       []string{"suffix:one"},
				Reason:             "Attention moved away after editing.",
			},
		},
	})

	if len(got.Observations) != 2 {
		t.Fatalf("observations=%+v, want two interleaved episode observations", got.Observations)
	}
	if got.Observations[0].Kind != TaskLifecycleKindPaused ||
		got.Observations[0].EpisodeIDs[0] != "episode-incident" ||
		got.Observations[1].Kind != TaskLifecycleKindResumed ||
		got.Observations[1].EpisodeIDs[0] != "episode-update" {
		t.Fatalf("interleaved observations were conflated: %+v", got.Observations)
	}
	if len(got.UnconsumedEvidenceRefs) != 0 {
		t.Fatalf("unconsumed=%v, want all suffix evidence assigned once", got.UnconsumedEvidenceRefs)
	}
}

func TestTaskLifecycleSwitchesBetweenOverlappingEpisodes(t *testing.T) {
	input := taskLifecycleFixture()
	input.SuffixEvidence = []TaskLifecycleEvidence{{
		Ref:        "suffix:switch",
		Kind:       "task_switch",
		SourceKind: "activity_event",
		SourceID:   "event-switch",
		OccurredAt: 3_500,
		Summary:    "The user moved from incident analysis back to the customer update.",
	}}
	normalized, err := normalizeTaskLifecycleInput(input)
	if err != nil {
		t.Fatal(err)
	}

	got := normalizeTaskLifecycleResponse(normalized, TaskLifecycleResponse{
		Observations: []TaskLifecycleObservation{{
			LocalKey:           "incident-to-update",
			Kind:               TaskLifecycleKindSwitched,
			EpisodeIDs:         []string{"episode-incident"},
			TargetEpisodeIDs:   []string{"episode-update"},
			Confidence:         0.85,
			BoundaryConfidence: 0.75,
			EvidenceRefs:       []string{"suffix:switch"},
			Reason:             "The observed attention boundary connects two known episodes.",
		}},
	})

	if len(got.Observations) != 1 {
		t.Fatalf("observations=%+v, want one task switch", got.Observations)
	}
	observation := got.Observations[0]
	if observation.Kind != TaskLifecycleKindSwitched ||
		observation.EpisodeIDs[0] != "episode-incident" ||
		observation.TargetEpisodeIDs[0] != "episode-update" {
		t.Fatalf("task switch relation=%+v", observation)
	}
}

func TestTaskLifecycleLateResultAppendsWithoutRepeatingCompletion(t *testing.T) {
	input := taskLifecycleFixture()
	input.Episodes = []TaskLifecycleEpisodeSnapshot{{
		EpisodeID:               "episode-incident",
		CurrentStatus:           TaskEpisodeStatusCompleted,
		InferredStartedAt:       1_000,
		Task:                    "Investigate the incident",
		DesiredResult:           "Identify and verify the cause",
		TaskConfidence:          0.9,
		BoundaryConfidence:      0.8,
		EvidenceRefs:            []string{"prefix:incident"},
		KnownResultEvidenceRefs: []string{"prefix:incident"},
	}}
	input.SuffixEvidence = []TaskLifecycleEvidence{{
		Ref:        "suffix:late-result",
		Kind:       "test_result",
		SourceKind: "terminal_event",
		SourceID:   "test-run-2",
		OccurredAt: 4_500,
		Summary:    "The later verification run passed.",
	}}

	normalized, err := normalizeTaskLifecycleInput(input)
	if err != nil {
		t.Fatal(err)
	}
	got := normalizeTaskLifecycleResponse(normalized, TaskLifecycleResponse{
		Observations: []TaskLifecycleObservation{{
			LocalKey:           "late-verification",
			Kind:               TaskLifecycleKindResult,
			EpisodeIDs:         []string{"episode-incident"},
			Confidence:         0.94,
			BoundaryConfidence: 0.88,
			ResultSummary:      "The follow-up verification passed.",
			ResultConfidence:   0.95,
			EvidenceRefs:       []string{"suffix:late-result"},
			Reason:             "A source-backed test result arrived later.",
		}},
	})

	if len(got.Observations) != 1 ||
		got.Observations[0].Kind != TaskLifecycleKindResult ||
		got.Observations[0].ResultSummary == "" {
		t.Fatalf("late result=%+v, want appendable result observation", got)
	}
	if normalized.Episodes[0].CurrentStatus != TaskEpisodeStatusCompleted ||
		len(normalized.Episodes[0].KnownResultEvidenceRefs) != 1 ||
		normalized.Episodes[0].KnownResultEvidenceRefs[0] != "prefix:incident" {
		t.Fatalf("existing completion/result state was overwritten: %+v", normalized.Episodes[0])
	}
	if got.Observations[0].EvidenceRefs[0] != "suffix:late-result" {
		t.Fatalf("late result reused prior evidence: %+v", got.Observations[0])
	}
}

func TestTaskLifecycleDuplicateEvidenceIsConsumedOnceDeterministically(t *testing.T) {
	input, err := normalizeTaskLifecycleInput(taskLifecycleFixture())
	if err != nil {
		t.Fatal(err)
	}
	got := normalizeTaskLifecycleResponse(input, TaskLifecycleResponse{
		Observations: []TaskLifecycleObservation{
			{
				LocalKey:           "resume-update",
				Kind:               TaskLifecycleKindResumed,
				EpisodeIDs:         []string{"episode-update"},
				Confidence:         0.7,
				BoundaryConfidence: 0.7,
				EvidenceRefs:       []string{"suffix:two"},
			},
			{
				LocalKey:           "pause-incident",
				Kind:               TaskLifecycleKindPaused,
				EpisodeIDs:         []string{"episode-incident"},
				Confidence:         0.8,
				BoundaryConfidence: 0.8,
				EvidenceRefs:       []string{"suffix:two"},
			},
		},
	})

	if len(got.Observations) != 1 {
		t.Fatalf("observations=%+v, duplicate source was consumed more than once", got.Observations)
	}
	if got.Observations[0].LocalKey != "pause-incident" {
		t.Fatalf("deterministic ordering selected %q, want pause-incident", got.Observations[0].LocalKey)
	}
	consumed := 0
	for _, observation := range got.Observations {
		for _, ref := range observation.EvidenceRefs {
			if ref == "suffix:two" {
				consumed++
			}
		}
	}
	if consumed != 1 {
		t.Fatalf("suffix:two consumption count=%d, want 1", consumed)
	}
}

func TestTaskLifecycleRejectsCutoffLeakage(t *testing.T) {
	input := taskLifecycleFixture()
	input.SuffixEvidence = append(input.SuffixEvidence, TaskLifecycleEvidence{
		Ref:        "suffix:future",
		Kind:       "result",
		SourceKind: "activity_event",
		SourceID:   "future-result",
		OccurredAt: input.CutoffAt + 1,
		Summary:    "A future result that must not enter this observation.",
	})
	if _, _, err := buildTaskLifecyclePrompt(input); err == nil ||
		!strings.Contains(err.Error(), "outside") {
		t.Fatalf("future suffix error=%v, want cutoff rejection", err)
	}

	input = taskLifecycleFixture()
	input.PrefixEvidence = append(input.PrefixEvidence, TaskLifecycleEvidence{
		Ref:        "prefix:future",
		Kind:       "result",
		SourceKind: "activity_event",
		SourceID:   "future-prefix-result",
		OccurredAt: input.PrefixCutoffAt + 1,
		Summary:    "A suffix event mislabeled as prefix.",
	})
	if _, _, err := buildTaskLifecyclePrompt(input); err == nil ||
		!strings.Contains(err.Error(), "after prefix cutoff") {
		t.Fatalf("future prefix error=%v, want phase-boundary rejection", err)
	}

	input = taskLifecycleFixture()
	input.Episodes[0].InferredStartedAt = input.PrefixCutoffAt + 1
	if _, _, err := buildTaskLifecyclePrompt(input); err == nil ||
		!strings.Contains(err.Error(), "outside the prefix snapshot") {
		t.Fatalf("suffix-started episode error=%v, want prefix-snapshot rejection", err)
	}
}

func TestTaskLifecycleRejectsUnboundedSuffixEvidence(t *testing.T) {
	input := taskLifecycleFixture()
	input.SuffixEvidence = make(
		[]TaskLifecycleEvidence,
		0,
		taskLifecycleMaxSuffixEvidence+1,
	)
	for i := 0; i <= taskLifecycleMaxSuffixEvidence; i++ {
		input.SuffixEvidence = append(input.SuffixEvidence, TaskLifecycleEvidence{
			Ref:        "suffix:bounded-" + string(rune(0x1000+i)),
			Kind:       "action",
			SourceKind: "activity_event",
			SourceID:   "bounded-" + string(rune(0x1000+i)),
			OccurredAt: input.PrefixCutoffAt + 1 + int64(i),
			Summary:    "Bounded lifecycle evidence.",
		})
	}
	if _, _, err := buildTaskLifecyclePrompt(input); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized evidence error=%v, want bounded-input rejection", err)
	}
}

func TestTaskLifecycleAcceptsProductionSizedContextWithinBudget(
	t *testing.T,
) {
	input := taskLifecycleFixture()
	input.Episodes = input.Episodes[:1]
	input.PrefixEvidence = input.PrefixEvidence[:1]
	input.SuffixEvidence = make([]TaskLifecycleEvidence, 0, 100)
	for index := 0; index < 100; index++ {
		input.SuffixEvidence = append(
			input.SuffixEvidence,
			TaskLifecycleEvidence{
				Ref:         fmt.Sprintf("suffix:large:%03d", index),
				Kind:        "keyboard",
				SourceKind:  "activity_event",
				SourceID:    fmt.Sprintf("large-%03d", index),
				OccurredAt:  input.PrefixCutoffAt + 1 + int64(index),
				Summary:     "The user continued composing source-backed text.",
				ContentKind: TaskLifecycleContentCommittedText,
				Content:     strings.Repeat("production context ", 65),
			},
		)
	}
	prompt, normalized, err := buildTaskLifecyclePrompt(input)
	if err != nil {
		t.Fatalf("production-sized lifecycle context: %v", err)
	}
	if len(normalized.SuffixEvidence) != 100 ||
		len(prompt) <= 64_000 ||
		len(prompt) > taskLifecycleContextBudget+10_000 {
		t.Fatalf(
			"production-sized prompt=%d suffix=%d",
			len(prompt),
			len(normalized.SuffixEvidence),
		)
	}
}

func TestTaskLifecycleStillRejectsContextBeyondExpandedBudget(
	t *testing.T,
) {
	input := taskLifecycleFixture()
	input.Episodes = input.Episodes[:1]
	input.PrefixEvidence = input.PrefixEvidence[:1]
	input.SuffixEvidence = make(
		[]TaskLifecycleEvidence,
		0,
		taskLifecycleMaxSuffixEvidence,
	)
	for index := 0; index < taskLifecycleMaxSuffixEvidence; index++ {
		input.SuffixEvidence = append(
			input.SuffixEvidence,
			TaskLifecycleEvidence{
				Ref:         fmt.Sprintf("suffix:oversized:%03d", index),
				Kind:        "keyboard",
				SourceKind:  "activity_event",
				SourceID:    fmt.Sprintf("oversized-%03d", index),
				OccurredAt:  input.PrefixCutoffAt + 1 + int64(index),
				Summary:     "The user continued composing source-backed text.",
				ContentKind: TaskLifecycleContentCommittedText,
				Content:     strings.Repeat("bounded context ", 150),
			},
		)
	}
	if _, _, err := buildTaskLifecyclePrompt(input); err == nil ||
		!strings.Contains(err.Error(), "exceeds 256000-byte budget") {
		t.Fatalf("oversized lifecycle error=%v", err)
	}
}

func TestTaskLifecycleMergeAndSplitRelations(t *testing.T) {
	input := taskLifecycleFixture()
	input.PrefixEvidence = append(input.PrefixEvidence,
		TaskLifecycleEvidence{
			Ref:        "prefix:duplicate",
			Kind:       "onset",
			SourceKind: "activity_event",
			SourceID:   "event-duplicate-onset",
			OccurredAt: 1_600,
			Summary:    "A duplicate incident task was detected.",
		},
		TaskLifecycleEvidence{
			Ref:        "prefix:research",
			Kind:       "onset",
			SourceKind: "activity_event",
			SourceID:   "event-research-onset",
			OccurredAt: 1_700,
			Summary:    "The user began a broad research task.",
		},
		TaskLifecycleEvidence{
			Ref:        "prefix:market",
			Kind:       "onset",
			SourceKind: "activity_event",
			SourceID:   "event-market-onset",
			OccurredAt: 1_800,
			Summary:    "The market-analysis child task was detected.",
		},
		TaskLifecycleEvidence{
			Ref:        "prefix:technical",
			Kind:       "onset",
			SourceKind: "activity_event",
			SourceID:   "event-technical-onset",
			OccurredAt: 1_900,
			Summary:    "The technical-analysis child task was detected.",
		},
	)
	input.Episodes = append(input.Episodes,
		TaskLifecycleEpisodeSnapshot{
			EpisodeID:          "episode-duplicate",
			CurrentStatus:      TaskEpisodeStatusActive,
			InferredStartedAt:  1_600,
			Task:               "Investigate the same incident",
			DesiredResult:      "Identify the incident cause",
			TaskConfidence:     0.6,
			BoundaryConfidence: 0.5,
			EvidenceRefs:       []string{"prefix:duplicate"},
		},
		TaskLifecycleEpisodeSnapshot{
			EpisodeID:          "episode-research",
			CurrentStatus:      TaskEpisodeStatusActive,
			InferredStartedAt:  1_700,
			Task:               "Research the opportunity",
			DesiredResult:      "A supported opportunity assessment",
			TaskConfidence:     0.8,
			BoundaryConfidence: 0.6,
			EvidenceRefs:       []string{"prefix:research"},
		},
		TaskLifecycleEpisodeSnapshot{
			EpisodeID:          "episode-market",
			CurrentStatus:      TaskEpisodeStatusCandidate,
			InferredStartedAt:  1_800,
			Task:               "Analyze the market",
			DesiredResult:      "A market assessment",
			TaskConfidence:     0.7,
			BoundaryConfidence: 0.6,
			EvidenceRefs:       []string{"prefix:market"},
		},
		TaskLifecycleEpisodeSnapshot{
			EpisodeID:          "episode-technical",
			CurrentStatus:      TaskEpisodeStatusCandidate,
			InferredStartedAt:  1_900,
			Task:               "Analyze technical feasibility",
			DesiredResult:      "A feasibility assessment",
			TaskConfidence:     0.7,
			BoundaryConfidence: 0.6,
			EvidenceRefs:       []string{"prefix:technical"},
		},
	)
	input.SuffixEvidence = []TaskLifecycleEvidence{
		{
			Ref:        "suffix:merge",
			Kind:       "task_merge",
			SourceKind: "activity_event",
			SourceID:   "event-merge",
			OccurredAt: 3_000,
			Summary:    "Evidence shows the duplicate and incident are one task.",
		},
		{
			Ref:        "suffix:split",
			Kind:       "task_split",
			SourceKind: "activity_event",
			SourceID:   "event-split",
			OccurredAt: 4_000,
			Summary:    "The research work separated into market and technical tasks.",
		},
	}

	normalized, err := normalizeTaskLifecycleInput(input)
	if err != nil {
		t.Fatal(err)
	}
	got := normalizeTaskLifecycleResponse(normalized, TaskLifecycleResponse{
		Observations: []TaskLifecycleObservation{
			{
				LocalKey:           "split-research",
				Kind:               TaskLifecycleKindSplit,
				EpisodeIDs:         []string{"episode-research"},
				TargetEpisodeIDs:   []string{"episode-technical", "episode-market"},
				Confidence:         0.82,
				BoundaryConfidence: 0.75,
				EvidenceRefs:       []string{"suffix:split"},
			},
			{
				LocalKey:           "merge-duplicate",
				Kind:               TaskLifecycleKindMerged,
				EpisodeIDs:         []string{"episode-duplicate"},
				TargetEpisodeIDs:   []string{"episode-incident"},
				Confidence:         0.9,
				BoundaryConfidence: 0.8,
				EvidenceRefs:       []string{"suffix:merge"},
			},
		},
	})

	if len(got.Observations) != 2 {
		t.Fatalf("relations=%+v, want merge and split", got.Observations)
	}
	if got.Observations[0].Kind != TaskLifecycleKindMerged ||
		got.Observations[0].TargetEpisodeIDs[0] != "episode-incident" {
		t.Fatalf("merge relation=%+v", got.Observations[0])
	}
	if got.Observations[1].Kind != TaskLifecycleKindSplit ||
		strings.Join(got.Observations[1].TargetEpisodeIDs, ",") !=
			"episode-market,episode-technical" {
		t.Fatalf("split relation=%+v", got.Observations[1])
	}
}

func TestTaskLifecycleRunnerUsesStrictObserverMode(t *testing.T) {
	runner := &taskLifecycleFakeRunner{response: `{
		"observations": [{
			"localKey": "pause-incident",
			"kind": "paused",
			"episodeIds": ["episode-incident"],
			"targetEpisodeIds": [],
			"occurredAt": 3000,
			"confidence": 0.8,
			"boundaryConfidence": 0.7,
			"resultSummary": "",
			"resultConfidence": 0,
			"evidenceRefs": ["suffix:one"],
			"reason": "The user paused incident work.",
			"alternatives": []
		}]
	}`}
	got, raw, prompt, err := runTaskLifecycleObservation(
		context.Background(),
		runner,
		"gpt-5.6-sol",
		taskLifecycleFixture(),
	)
	if err != nil {
		t.Fatalf("runTaskLifecycleObservation: %v raw=%q prompt=%q", err, raw, prompt)
	}
	if runner.input == nil ||
		!runner.input.NoTools ||
		runner.input.AutoApproveTools ||
		!runner.input.Ephemeral ||
		!runner.input.NoSystem ||
		!runner.input.PlanMode ||
		!runner.input.SuppressTelemetry {
		t.Fatalf("runner input=%+v, want isolated no-tool observer", runner.input)
	}
	if runner.input.Model != "gpt-5.6-sol" ||
		!strings.Contains(prompt, taskLifecyclePromptVersion) {
		t.Fatalf("runner model/prompt mismatch: input=%+v prompt=%q", runner.input, prompt)
	}
	if len(got.Observations) != 1 ||
		got.Observations[0].Kind != TaskLifecycleKindPaused {
		t.Fatalf("normalized response=%+v", got)
	}
}

func TestTaskLifecycleRunnerReportsModelErrors(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		runner := &taskLifecycleFakeRunner{response: `not-json`}
		_, raw, _, err := runTaskLifecycleObservation(
			context.Background(),
			runner,
			"gpt-5.6-sol",
			taskLifecycleFixture(),
		)
		if err == nil ||
			!strings.Contains(err.Error(), "parse task lifecycle response") ||
			raw != "not-json" {
			t.Fatalf("err=%v raw=%q, want parse error with raw output", err, raw)
		}
	})

	t.Run("stream error", func(t *testing.T) {
		runner := &taskLifecycleFakeRunner{streamErr: "model unavailable"}
		_, _, _, err := runTaskLifecycleObservation(
			context.Background(),
			runner,
			"gpt-5.6-sol",
			taskLifecycleFixture(),
		)
		if err == nil ||
			!strings.Contains(err.Error(), "model unavailable") {
			t.Fatalf("err=%v, want propagated stream error", err)
		}
	})
}
