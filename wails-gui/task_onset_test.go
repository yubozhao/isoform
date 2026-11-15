package main

import (
	"context"

	"yansu-agent/agent"
)

type taskOnsetFakeRunner struct {
	response string
	input    *agent.TaskInput
}

func (r *taskOnsetFakeRunner) Init(context.Context) error { return nil }
func (r *taskOnsetFakeRunner) IsReady() bool              { return true }
func (r *taskOnsetFakeRunner) ExecuteTask(
	_ context.Context,
	input *agent.TaskInput,
	callback agent.StreamCallback,
) (string, error) {
	r.input = input
	if err := callback(&agent.StreamEvent{Type: "assistant", Content: r.response}); err != nil {
		return "", err
	}
	return "task-onset-run", nil
}
func (r *taskOnsetFakeRunner) CancelRun(string) error { return nil }
func (r *taskOnsetFakeRunner) RespondToPermission(string, string) error {
	return nil
}
func (r *taskOnsetFakeRunner) CancelPermission(string) {}
func (r *taskOnsetFakeRunner) Shutdown() error         { return nil }

func taskOnsetFixture() TaskOnsetInput {
	return TaskOnsetInput{
		SourceKind:  "activity",
		SourceID:    "session-1",
		WindowStart: 1_000,
		CutoffAt:    4_000,
		Evidence: []TaskOnsetEvidence{
			{
				Ref:        "event:2",
				Kind:       "focus",
				SourceKind: "activity_event",
				SourceID:   "2",
				OccurredAt: 2_000,
				Summary:    "Focused the customer incident document.",
			},
			{
				Ref:        "event:1",
				Kind:       "app_switch",
				SourceKind: "activity_event",
				SourceID:   "1",
				OccurredAt: 1_500,
				Summary:    "Opened the incident workspace.",
			},
		},
	}
}
