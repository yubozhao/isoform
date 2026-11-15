package main

import (
	"context"

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
