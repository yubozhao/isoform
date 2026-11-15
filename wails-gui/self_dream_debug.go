package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	selfDreamDebugCustomPrompt = "self-dream-debug-custom-v1"
	selfDreamDebugPromptMax    = 256_000
)

type SelfDreamDebugSnapshot struct {
	GeneratedAt int64                  `json:"generatedAt"`
	Runs        []SelfDreamRunSummary  `json:"runs"`
	Rollout     SelfDreamRolloutStatus `json:"rollout"`
}

func (a *App) GetSelfDreamDebugSnapshot(
	limit int,
) (SelfDreamDebugSnapshot, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return SelfDreamDebugSnapshot{
			GeneratedAt: time.Now().UnixMilli(),
			Runs:        []SelfDreamRunSummary{},
			Rollout: SelfDreamRolloutStatus{
				Reasons: []string{"Activity store is unavailable."},
			},
		}, nil
	}
	runs, err := a.activity.store.ListSelfDreamRunSummaries(limit)
	if err != nil {
		return SelfDreamDebugSnapshot{}, err
	}
	rollout, err := a.activity.store.SelfDreamRolloutStatus()
	if err != nil {
		return SelfDreamDebugSnapshot{}, err
	}
	return SelfDreamDebugSnapshot{
		GeneratedAt: time.Now().UnixMilli(),
		Runs:        runs,
		Rollout:     rollout,
	}, nil
}

func (a *App) GetSelfDreamDebugRun(
	runID string,
) (SelfDreamRunDetail, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return SelfDreamRunDetail{}, errors.New(
			"activity store is unavailable",
		)
	}
	return a.activity.store.GetSelfDreamRunDetail(runID)
}

// ReplaySelfDreamDebug reuses only the retained frozen input. It invokes the
// same no-tools parser and appends a diagnostic record with product mutation
// and learning eligibility fixed off by schema.
func (a *App) ReplaySelfDreamDebug(
	input ReplaySelfDreamDebugInput,
) (ReplaySelfDreamDebugResult, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return ReplaySelfDreamDebugResult{}, errors.New(
			"activity store is unavailable",
		)
	}
	if a.runner == nil {
		return ReplaySelfDreamDebugResult{}, errors.New(
			"Self Dream model runner is not initialized",
		)
	}
	detail, err := a.activity.store.GetSelfDreamRunDetail(input.RunID)
	if err != nil {
		return ReplaySelfDreamDebugResult{}, err
	}
	prompt := input.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = detail.Run.Prompt
	}
	if len(prompt) > selfDreamDebugPromptMax {
		return ReplaySelfDreamDebugResult{}, fmt.Errorf(
			"Self Dream replay prompt exceeds %d bytes",
			selfDreamDebugPromptMax,
		)
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = strings.TrimSpace(detail.Run.RequestedModel)
	}
	if model == "" {
		return ReplaySelfDreamDebugResult{}, errors.New(
			"configure an AI provider before replaying Self Dream",
		)
	}
	promptHash := sha256Hex([]byte(prompt))
	promptVersion := selfDreamDebugCustomPrompt
	if promptHash == detail.Run.PromptHash {
		promptVersion = SelfDreamPromptVersion
	}
	contextMatches := detail.Run.ContextHash != "" &&
		sha256Hex(
			[]byte(detail.Run.Context.CanonicalJSON)) ==
			detail.Run.ContextHash
	started := time.Now()
	ctx, cancel := context.WithTimeout(
		agencyContext(a),
		selfDreamModelTimeout,
	)
	response, raw, analysisErr := runSelfDreamAnalysis(
		ctx,
		a.runner,
		model,
		prompt,
	)
	cancel()
	record := SelfDreamReplayRecord{
		RunID:                detail.Run.ID,
		Status:               "completed",
		ContextHash:          detail.Run.ContextHash,
		ContextMatchesSource: contextMatches,
		PromptVersion:        promptVersion,
		Model:                model,
		PromptHash:           promptHash,
		RawOutput:            raw,
		RawOutputHash:        sha256Hex([]byte(raw)),
		Operations:           []SelfDreamOperationRecord{},
		DurationMs:           time.Since(started).Milliseconds(),
		CreatedAt:            time.Now().UnixMilli(),
	}
	if analysisErr != nil {
		record.Status = "failed"
		record.Error = analysisErr.Error()
	} else {
		now := time.Now().UnixMilli()
		drafts := detectDeterministicSelfDreamOperations(
			detail.Run.BaseSelf,
			detail.Run.Manifest,
		)
		drafts = append(drafts, response.Operations...)
		seen := make(map[string]struct{})
		for index, draft := range drafts {
			source := "model"
			if index < len(drafts)-len(response.Operations) {
				source = "deterministic"
			}
			operation := validateSelfDreamOperation(
				draft,
				source,
				detail.Run.BaseSelf,
				detail.Run.Manifest,
				now,
			)
			if _, duplicate := seen[operation.OperationHash]; duplicate {
				continue
			}
			seen[operation.OperationHash] = struct{}{}
			operation.RunID = detail.Run.ID
			operation.AttemptID = "debug-replay"
			operation.Ordinal = len(record.Operations)
			operation.ID = deterministicAuthorityID(
				"self-dream-debug-operation",
				detail.Run.ID+"\x00"+operation.OperationHash,
			)
			record.Operations = append(record.Operations, operation)
		}
	}
	persisted, err := a.activity.store.AddSelfDreamReplay(record)
	if err != nil {
		return ReplaySelfDreamDebugResult{}, err
	}
	return ReplaySelfDreamDebugResult{
		Replay: persisted,
		Prompt: prompt,
	}, nil
}
