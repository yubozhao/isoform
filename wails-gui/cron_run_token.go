package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
	"yansu-agent/agent"
	"yansu-agent/agent/authority"
	"yansu-agent/runtoken"
)

type cronRunScopeKey struct{}

func cronRunScope(r *http.Request) (runtoken.Scope, bool) {
	scope, ok := r.Context().Value(cronRunScopeKey{}).(runtoken.Scope)
	return scope, ok
}

// issueAgentRunScope is installed once on the common desktop router, covering
// chat, scheduled agents, handoffs, playbooks and native fallback. No credential
// is written to disk or placed in TaskInput.Env or the process environment.
func (a *App) issueAgentRunScope(ctx context.Context, input *agent.TaskInput) (context.Context, func(), error) {
	a.mu.Lock()
	api := a.cronAPI
	a.mu.Unlock()
	if api == nil && !input.NoTools {
		return nil, nil, errors.New("run credential issuer unavailable")
	}
	// Every routed call counts once against the work that made it, for See
	// them (assistant_call_record.go); a call outside any piece of work is
	// recorded on its own.
	a.noteRoutedAssistantCall(ctx, input)
	// The credential has a maximum age without changing the runner's own
	// timeout rules (external runners apply their default to unbounded turns).
	ctx, cancelRun := context.WithCancel(ctx)
	tokenCtx, cancelToken := context.WithTimeout(ctx, executeTimeout)
	workspace := input.WorkDir
	if workspace == "" {
		workspace = os.TempDir()
	}
	runID := uuid.NewString()
	owner := input.CredentialSessionID
	if owner == "" {
		owner = input.HistoryKey
	}
	if owner == "" {
		owner = input.SessionID
	}
	if owner == "" {
		owner = runID
	}
	scope := runtoken.Scope{RunID: runID, SessionID: owner, Workspace: workspace,
		Grants: input.AuthorityGrants, Unattended: input.AutoApproveTools || input.Ephemeral || input.DenyExternalWrites}
	// Scope follows tool reach. A text-only or non-shell run has no HTTP API
	// permissions even if the external process obtains its own credential.
	if !input.NoTools && !input.PlanMode && !allowsTool(input.DisabledTools, "Bash") &&
		(len(input.AllowedTools) == 0 || allowsTool(input.AllowedTools, "Bash")) {
		for _, route := range []string{"/activity/sessions", "/activity/sessions/", "/activity/summary", "/activity/search", "/memory/list", "/memory/search", "/memory/show/", "/cron/jobs", "/cron/jobs/"} {
			scope.Endpoints = append(scope.Endpoints, runtoken.Endpoint{Method: http.MethodGet, Path: route})
		}
		if input.AuthorityShadowRunID == "" && (input.ExecutionMode == "" || input.ExecutionMode == authority.ExecutionModeNormal) {
			scope.Endpoints = append(scope.Endpoints, runtoken.Endpoint{Method: http.MethodPost, Path: "/cron/jobs"})
		}
	}
	token, revoke, err := a.runTokens.Mint(tokenCtx, scope)
	if err != nil {
		cancelToken()
		cancelRun()
		return nil, nil, err
	}
	var once sync.Once
	end := func() { once.Do(func() { revoke(); cancelToken(); cancelRun() }) }
	port := 0
	if api != nil {
		port = api.Port()
	}
	return runtoken.WithCredential(ctx, runtoken.Credential{Token: token, Port: port}), end, nil
}

// allowsTool is shared here only for exact tool-name membership; an empty list
// is interpreted by the caller according to AllowedTools/DisabledTools rules.
func allowsTool(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func cronScopePermits(scope runtoken.Scope, r *http.Request) bool {
	if !scope.Allows(r.Method, r.URL.Path) {
		return false
	}
	if !scope.Unattended {
		return true
	}
	capability := "schedule.read"
	effect := authority.EffectRead
	switch true {
	case strings.HasPrefix(r.URL.Path, "/activity/"):
		capability = "activity.read"
	case strings.HasPrefix(r.URL.Path, "/memory/"):
		capability = "memory.read"
	case r.Method == http.MethodPost:
		capability, effect = "schedule.write", authority.EffectExternalWrite
	}
	evaluation := authority.NewPolicy(scope.Grants).Evaluate(r.Context(), authority.Context{
		RunID: scope.RunID, SessionID: scope.SessionID, Unattended: true,
	}, authority.Action{Capability: capability, Effect: effect, Resource: scope.Workspace, ToolName: "scheduler_api"})
	return evaluation.Decision == authority.DecisionAllow
}

func cronScopeOwnsJob(scope runtoken.Scope, job CronJobState) bool {
	if job.SourceSessionID != scope.SessionID || job.TargetKind != "agent" || job.SurfaceKind != "" && job.SurfaceKind != "scheduled" {
		return false
	}
	raw, err := json.Marshal(job.TargetConfig)
	if err != nil {
		return false
	}
	var target cronTargetConfig
	if json.Unmarshal(raw, &target) != nil || target.HandoffID != "" || target.SOPID != "" {
		return false
	}
	workspace, err := runtoken.Workspace(target.ProjectDir)
	return err == nil && workspace == scope.Workspace
}

// Scope is server-side state. Ignore ambient active-session files and reject
// attempts to claim another session, workspace, hidden session or playbook.
func constrainScopedCronInput(scope runtoken.Scope, input *CronJobInput) error {
	if input.SourceSessionID != "" && input.SourceSessionID != scope.SessionID || input.SourcePlatform != "" ||
		input.TargetKind != "" && input.TargetKind != "agent" || input.SurfaceKind != "" && input.SurfaceKind != "scheduled" {
		return errors.New("job is outside run scope")
	}
	var target cronTargetConfig
	decoder := json.NewDecoder(strings.NewReader(string(input.TargetConfig)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return errors.New("invalid scoped job target")
	}
	if target.HandoffID != "" || target.SOPID != "" || target.SessionTag != "" {
		return errors.New("job is outside run scope")
	}
	if target.ProjectDir != "" {
		workspace, err := runtoken.Workspace(target.ProjectDir)
		if err != nil || workspace != scope.Workspace {
			return errors.New("job workspace is outside run scope")
		}
	}
	target.ProjectDir = scope.Workspace
	raw, err := json.Marshal(target)
	if err != nil {
		return err
	}
	input.TargetConfig, input.TargetKind, input.SourceSessionID = raw, "agent", scope.SessionID
	return nil
}
