package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"yansu-agent/agent"
	"yansu-agent/agent/authority"
)

const (
	shadowExecutionRuntimeVersion = "shadow-runtime-v1"
	shadowExecutionTimeout        = 10 * time.Minute
	shadowExecutionMaxOutputBytes = 64 * 1024
	shadowExecutionMaxArtifacts   = 64
	shadowExecutionArtifactBytes  = 64 * 1024
	shadowExecutionTotalArtifacts = 512 * 1024
)

type ShadowExecutionAction struct {
	Sequence  int    `json:"sequence"`
	Type      string `json:"type"`
	ToolName  string `json:"toolName,omitempty"`
	ToolUseID string `json:"toolUseId,omitempty"`
	InputJSON string `json:"inputJson,omitempty"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
}

type ShadowExecutionArtifact struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
}

type ShadowExecutionAttempt struct {
	ID              string                       `json:"id"`
	ShadowRunID     string                       `json:"shadowRunId"`
	IdempotencyKey  string                       `json:"idempotencyKey"`
	RuntimeVersion  string                       `json:"runtimeVersion"`
	Status          string                       `json:"status"`
	PolicyVersion   string                       `json:"policyVersion"`
	AgentRunID      string                       `json:"agentRunId,omitempty"`
	Model           string                       `json:"model,omitempty"`
	PlanHash        string                       `json:"planHash"`
	Prompt          string                       `json:"prompt"`
	Plan            ShadowCandidateExecutionPlan `json:"plan"`
	Decisions       []authority.Evaluation       `json:"decisions"`
	Actions         []ShadowExecutionAction      `json:"actions"`
	Artifacts       []ShadowExecutionArtifact    `json:"artifacts"`
	AssistantOutput string                       `json:"assistantOutput,omitempty"`
	Error           string                       `json:"error,omitempty"`
	StartedAt       int64                        `json:"startedAt"`
	CompletedAt     int64                        `json:"completedAt"`
	CreatedAt       int64                        `json:"createdAt"`
}

func normalizeShadowExecutionAttempt(input ShadowExecutionAttempt) (ShadowExecutionAttempt, error) {
	input.ShadowRunID = strings.TrimSpace(input.ShadowRunID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RuntimeVersion = strings.TrimSpace(input.RuntimeVersion)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.PolicyVersion = strings.TrimSpace(input.PolicyVersion)
	input.AgentRunID = strings.TrimSpace(input.AgentRunID)
	input.Model = strings.TrimSpace(input.Model)
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Error = strings.TrimSpace(input.Error)
	if input.ShadowRunID == "" || input.IdempotencyKey == "" {
		return ShadowExecutionAttempt{}, errors.New("shadow execution attempt requires run and idempotency keys")
	}
	if input.RuntimeVersion == "" {
		input.RuntimeVersion = shadowExecutionRuntimeVersion
	}
	switch input.Status {
	case "completed", "error", "unsupported_runner":
	default:
		return ShadowExecutionAttempt{}, fmt.Errorf("unsupported shadow execution status %q", input.Status)
	}
	if input.PolicyVersion == "" {
		input.PolicyVersion = authority.ShadowPolicyVersion
	}
	if input.Prompt == "" {
		return ShadowExecutionAttempt{}, errors.New("shadow execution attempt requires the exact prompt")
	}
	planPayload, err := json.Marshal(input.Plan)
	if err != nil {
		return ShadowExecutionAttempt{}, fmt.Errorf("encode shadow execution plan: %w", err)
	}
	expectedPlanHash := sha256Hex(planPayload)
	if input.PlanHash != "" && input.PlanHash != expectedPlanHash {
		return ShadowExecutionAttempt{}, errors.New("shadow execution plan hash does not match")
	}
	input.PlanHash = expectedPlanHash
	if input.StartedAt == 0 {
		return ShadowExecutionAttempt{}, errors.New("shadow execution attempt requires a stable start timestamp")
	}
	if input.CompletedAt == 0 {
		return ShadowExecutionAttempt{}, errors.New("shadow execution attempt requires a stable completion timestamp")
	}
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.CreatedAt == 0 {
		input.CreatedAt = input.CompletedAt
	}
	if input.Decisions == nil {
		input.Decisions = []authority.Evaluation{}
	}
	if input.Actions == nil {
		input.Actions = []ShadowExecutionAction{}
	}
	if input.Artifacts == nil {
		input.Artifacts = []ShadowExecutionArtifact{}
	}
	for index := range input.Actions {
		input.Actions[index].Sequence = index + 1
		input.Actions[index].Type = strings.TrimSpace(input.Actions[index].Type)
		input.Actions[index].ToolName = strings.TrimSpace(input.Actions[index].ToolName)
		input.Actions[index].ToolUseID = strings.TrimSpace(input.Actions[index].ToolUseID)
		input.Actions[index].InputJSON = truncateShadowExecutionText(input.Actions[index].InputJSON)
		input.Actions[index].Output = truncateShadowExecutionText(input.Actions[index].Output)
		input.Actions[index].Error = truncateShadowExecutionText(input.Actions[index].Error)
	}
	input.AssistantOutput = truncateShadowExecutionText(input.AssistantOutput)
	input.Error = truncateShadowExecutionText(input.Error)
	return input, nil
}

func truncateShadowExecutionText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= shadowExecutionMaxOutputBytes {
		return value
	}
	return value[:shadowExecutionMaxOutputBytes] + "\n... <shadow runtime output truncated> ..."
}

func (s *ActivityStore) AddShadowExecutionAttempt(input ShadowExecutionAttempt) (ShadowExecutionAttempt, bool, error) {
	if s == nil || s.db == nil {
		return ShadowExecutionAttempt{}, false, errors.New("activity store unavailable")
	}
	input, err := normalizeShadowExecutionAttempt(input)
	if err != nil {
		return ShadowExecutionAttempt{}, false, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ShadowExecutionAttempt{}, false, fmt.Errorf("encode shadow execution attempt: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO shadow_execution_attempts (
			id, shadow_run_id, idempotency_key, runtime_version, status,
			policy_version, agent_run_id, plan_hash, attempt_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.ID,
		input.ShadowRunID,
		input.IdempotencyKey,
		input.RuntimeVersion,
		input.Status,
		input.PolicyVersion,
		input.AgentRunID,
		input.PlanHash,
		string(payload),
		input.CreatedAt,
	)
	if err == nil {
		return input, true, nil
	}
	existing, found, getErr := s.GetShadowExecutionAttemptByIdempotencyKey(input.IdempotencyKey)
	if getErr != nil {
		return ShadowExecutionAttempt{}, false, fmt.Errorf("save shadow execution attempt: %w", err)
	}
	if found {
		return existing, false, nil
	}
	return ShadowExecutionAttempt{}, false, fmt.Errorf("save shadow execution attempt: %w", err)
}

func scanShadowExecutionAttempt(scanner interface{ Scan(dest ...any) error }) (ShadowExecutionAttempt, error) {
	var id, raw string
	var createdAt int64
	if err := scanner.Scan(&id, &raw, &createdAt); err != nil {
		return ShadowExecutionAttempt{}, err
	}
	var item ShadowExecutionAttempt
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return ShadowExecutionAttempt{}, fmt.Errorf("decode shadow execution attempt %s: %w", id, err)
	}
	item.ID = id
	item.CreatedAt = createdAt
	if item.Decisions == nil {
		item.Decisions = []authority.Evaluation{}
	}
	if item.Actions == nil {
		item.Actions = []ShadowExecutionAction{}
	}
	if item.Artifacts == nil {
		item.Artifacts = []ShadowExecutionArtifact{}
	}
	return item, nil
}

func (s *ActivityStore) GetShadowExecutionAttemptByIdempotencyKey(key string) (ShadowExecutionAttempt, bool, error) {
	if s == nil || s.db == nil {
		return ShadowExecutionAttempt{}, false, errors.New("activity store unavailable")
	}
	item, err := scanShadowExecutionAttempt(s.db.QueryRow(`
		SELECT id, attempt_json, created_at
		FROM shadow_execution_attempts
		WHERE idempotency_key = ?`,
		strings.TrimSpace(key),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ShadowExecutionAttempt{}, false, nil
	}
	return item, err == nil, err
}

func (s *ActivityStore) ListShadowExecutionAttempts(shadowRunID string) ([]ShadowExecutionAttempt, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store unavailable")
	}
	rows, err := s.db.Query(`
		SELECT id, attempt_json, created_at
		FROM shadow_execution_attempts
		WHERE shadow_run_id = ?
		ORDER BY created_at ASC, rowid ASC`,
		strings.TrimSpace(shadowRunID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShadowExecutionAttempt, 0)
	for rows.Next() {
		item, err := scanShadowExecutionAttempt(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (a *App) executeShadowCandidatePlan(
	ctx context.Context,
	run ShadowRun,
) (ShadowExecutionAttempt, bool, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return ShadowExecutionAttempt{}, false, errors.New("activity store unavailable")
	}
	if run.Mode == "task_mirror" &&
		!taskWorkflowProductionShadowEligible(run.CapabilityKey) {
		return ShadowExecutionAttempt{}, false, fmt.Errorf(
			"%w: workflow %q; enabled workflows are %s",
			ErrTaskWorkflowOutsideProductionRollout,
			run.CapabilityKey,
			taskWorkflowProductionShadowScope(),
		)
	}
	if run.Mode == "task_mirror" {
		if err := a.activity.store.taskEpisodeRepeatedOnsetError(
			run.TaskEpisodeID,
		); err != nil {
			return ShadowExecutionAttempt{}, false, fmt.Errorf(
				"shadow execution excluded: %w",
				err,
			)
		}
	}
	plan := run.Prediction.CandidateExecutionPlan
	if plan == nil {
		return ShadowExecutionAttempt{}, false, errors.New("shadow run has no candidate execution plan")
	}
	idempotencyKey := run.ID + ":" + shadowExecutionRuntimeVersion
	if existing, found, err := a.activity.store.GetShadowExecutionAttemptByIdempotencyKey(idempotencyKey); err != nil {
		return ShadowExecutionAttempt{}, false, err
	} else if found {
		return existing, false, nil
	}

	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	if runner == nil {
		return ShadowExecutionAttempt{}, false, errors.New("agent runner unavailable")
	}

	startedAt := time.Now().UnixMilli()
	model := a.chatTaskModel()
	if run.Mode == "task_mirror" && strings.TrimSpace(run.Model) != "" {
		model = strings.TrimSpace(run.Model)
	}
	workDir, err := os.MkdirTemp("", "yansu-shadow-"+run.ID+"-")
	if err != nil {
		return ShadowExecutionAttempt{}, false, fmt.Errorf("create isolated shadow workspace: %w", err)
	}
	defer os.RemoveAll(workDir)
	var output strings.Builder
	var streamError string
	decisions := make([]authority.Evaluation, 0)
	actions := make([]ShadowExecutionAction, 0)
	noTools := run.Mode == "task_mirror" &&
		taskWorkflowNoTools(run.CapabilityKey)
	agentRunID, executionErr := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:                agent.PriorityBackground,
		Prompt:                  plan.Prompt,
		WorkDir:                 workDir,
		Model:                   model,
		Ephemeral:               true,
		NoTools:                 noTools,
		AutoApproveTools:        !noTools,
		DenyExternalWrites:      true,
		ProgrammaticToolCalling: !noTools,
		SuppressTelemetry:       true,
		ExecutionMode:           authority.ExecutionModeShadow,
		AuthorityShadowRunID:    run.ID,
	}, func(event *agent.StreamEvent) error {
		if event == nil {
			return nil
		}
		switch event.Type {
		case "assistant":
			if output.Len() < shadowExecutionMaxOutputBytes {
				output.WriteString(event.Content)
			}
		case "authority_decision":
			var evaluation authority.Evaluation
			if err := json.Unmarshal([]byte(event.Content), &evaluation); err == nil {
				decisions = append(decisions, evaluation)
			}
		case "tool_use":
			inputJSON := ""
			if event.ToolInput != nil {
				if payload, err := json.Marshal(event.ToolInput); err == nil {
					inputJSON = string(payload)
				}
			}
			actions = append(actions, ShadowExecutionAction{
				Type:      "tool_use",
				ToolName:  event.ToolName,
				ToolUseID: event.ToolUseID,
				InputJSON: inputJSON,
			})
		case "tool_result":
			actions = append(actions, ShadowExecutionAction{
				Type:      "tool_result",
				ToolName:  event.ToolName,
				ToolUseID: event.ToolUseID,
				Output:    event.Content,
				Error:     event.Error,
			})
		case "error":
			if streamError == "" {
				streamError = event.Error
			}
		}
		return nil
	})

	status := "completed"
	errorText := ""
	switch {
	case errors.Is(executionErr, agent.ErrShadowRuntimeRequiresNative):
		status = "unsupported_runner"
		errorText = executionErr.Error()
	case executionErr != nil:
		status = "error"
		errorText = executionErr.Error()
	case streamError != "":
		status = "error"
		errorText = streamError
	}
	completedAt := time.Now().UnixMilli()
	artifacts, artifactErr := captureShadowExecutionArtifacts(workDir)
	if artifactErr != nil {
		if errorText == "" {
			errorText = "capture shadow artifacts: " + artifactErr.Error()
		} else {
			errorText += "\ncapture shadow artifacts: " + artifactErr.Error()
		}
		if status == "completed" {
			status = "error"
		}
	}
	attempt, added, err := a.activity.store.AddShadowExecutionAttempt(ShadowExecutionAttempt{
		ShadowRunID:     run.ID,
		IdempotencyKey:  idempotencyKey,
		RuntimeVersion:  shadowExecutionRuntimeVersion,
		Status:          status,
		PolicyVersion:   authority.ShadowPolicyVersion,
		AgentRunID:      agentRunID,
		Model:           model,
		Prompt:          plan.Prompt,
		Plan:            *plan,
		Decisions:       decisions,
		Actions:         actions,
		Artifacts:       artifacts,
		AssistantOutput: output.String(),
		Error:           errorText,
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
		CreatedAt:       completedAt,
	})
	if err != nil {
		return ShadowExecutionAttempt{}, false, err
	}
	if added {
		a.emitBrowserEvent("shadow_agent:runtime", attempt)
		// Task mirrors are not capability evidence until the user's observed
		// work has been compared with the isolated attempt. Legacy prediction
		// runs retain their existing qualification path during migration.
		if run.Mode != "task_mirror" {
			a.qualifyLatestShadowCapability(run.CapabilityKey)
		}
	}
	return attempt, added, nil
}

func captureShadowExecutionArtifacts(root string) ([]ShadowExecutionArtifact, error) {
	root = filepath.Clean(root)
	artifacts := make([]ShadowExecutionArtifact, 0)
	includedBytes := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		if len(artifacts) >= shadowExecutionMaxArtifacts {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hasher := sha256.New()
		if _, err := io.Copy(hasher, file); err != nil {
			file.Close()
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			file.Close()
			return err
		}
		remaining := shadowExecutionTotalArtifacts - includedBytes
		contentLimit := min(shadowExecutionArtifactBytes, remaining)
		content := []byte(nil)
		if contentLimit > 0 {
			content, err = io.ReadAll(io.LimitReader(file, int64(contentLimit)+1))
			if err != nil {
				file.Close()
				return err
			}
		}
		if err := file.Close(); err != nil {
			return err
		}
		truncated := len(content) > contentLimit || info.Size() > int64(contentLimit)
		if len(content) > contentLimit {
			content = content[:contentLimit]
		}
		includedBytes += len(content)
		artifact := ShadowExecutionArtifact{
			Path:      filepath.ToSlash(relative),
			SizeBytes: info.Size(),
			SHA256:    fmt.Sprintf("%x", hasher.Sum(nil)),
			Truncated: truncated,
		}
		if utf8.Valid(content) {
			artifact.Content = string(content)
		} else {
			artifact.Binary = true
		}
		artifacts = append(artifacts, artifact)
		return nil
	})
	return artifacts, err
}

func (c *ActivityTriager) dispatchShadowCandidateExecution(parent context.Context, run ShadowRun) {
	if c == nil || c.app == nil || run.Prediction.CandidateExecutionPlan == nil {
		return
	}
	if run.Mode == "task_mirror" &&
		!taskWorkflowProductionShadowEligible(run.CapabilityKey) {
		log.Printf(
			"[shadow-runtime] run=%s skipped: workflow %q is outside production rollout [%s]",
			run.ID,
			run.CapabilityKey,
			taskWorkflowProductionShadowScope(),
		)
		return
	}
	baseCtx := parent
	if baseCtx == nil {
		baseCtx = c.app.ctx
	}
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	baseCtx = taskShadowExecutionParent(baseCtx)
	if run.Mode == "task_mirror" &&
		!c.app.loadTriageFeatureFlags(baseCtx).Shadow {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if c.shadowExecutionSem != nil {
			select {
			case c.shadowExecutionSem <- struct{}{}:
				defer func() { <-c.shadowExecutionSem }()
			case <-baseCtx.Done():
				return
			}
		}
		if run.Mode == "task_mirror" &&
			!c.app.loadTriageFeatureFlags(baseCtx).Shadow {
			return
		}
		runCtx, cancel := context.WithTimeout(baseCtx, shadowExecutionTimeout)
		defer cancel()
		if _, _, err := c.app.executeShadowCandidatePlan(runCtx, run); err != nil {
			log.Printf("[shadow-runtime] run=%s failed: %v", run.ID, err)
			return
		}
		if run.Mode == "task_mirror" {
			if _, _, err := c.app.tryEvaluateShadowTaskRun(runCtx, run.ID); err != nil &&
				!errors.Is(err, ErrShadowTaskAwaitingObservedOutcome) {
				log.Printf("[shadow-task-evaluation] run=%s deferred: %v", run.ID, err)
			}
		}
	}()
}
