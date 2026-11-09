package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"yansu-agent/agent"
)

type workProfileModel interface {
	Generate(context.Context, string) (string, error)
}

// workProfileBackgroundModel uses the same background-priority routing as
// know-how extraction, with no tools, persistent session, or telemetry.
type workProfileBackgroundModel struct {
	runner agent.AgentRunner
}

func (m workProfileBackgroundModel) Generate(ctx context.Context, prompt string) (string, error) {
	if m.runner == nil {
		return "", errors.New("background model unavailable")
	}
	var result strings.Builder
	_, err := m.runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            prompt,
		Model:             claudeCodeModelConfig().ChatModel,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
		case "error":
			return errors.New("background model stream failed")
		}
		return ctx.Err()
	})
	if err != nil {
		return "", err
	}
	return result.String(), nil
}

func workProfileGenerate(ctx context.Context, model workProfileModel, candidates []workProfileCandidate, fullName string, privateValues []string) (workProfileModelResult, error) {
	if model == nil || len(candidates) == 0 {
		return workProfileModelResult{}, errors.New("no work profile candidates or model")
	}
	for _, candidate := range candidates {
		clean, _ := workProfileClean(candidate.Text, fullName, privateValues)
		if clean == "" || clean != candidate.Text {
			return workProfileModelResult{}, errors.New("work profile candidate privacy check failed")
		}
	}
	prompt, err := workProfileModelInput(candidates)
	if err != nil {
		return workProfileModelResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for attempt := 0; attempt < 2; attempt++ {
		raw, callErr := model.Generate(ctx, prompt)
		if callErr == nil {
			if result, decodeErr := workProfileDecode(raw, candidates, fullName, privateValues); decodeErr == nil {
				return result, nil
			}
		}
		if ctx.Err() != nil {
			return workProfileModelResult{}, ctx.Err()
		}
	}
	return workProfileModelResult{}, errors.New("work profile generation failed")
}
