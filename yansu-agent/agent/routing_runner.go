package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"yansu-agent/agent/authority"
	"yansu-agent/agent/native"
	"yansu-agent/agent/tools"
)

// Backend names the agent loop that executes a task.
type Backend string

const (
	BackendNative     Backend = "native"
	BackendExternal   Backend = "external" // Compatibility for callers selecting the first assistant.
	BackendClaudeCode Backend = "claude-code"
	BackendCodex      Backend = "codex"
)

// AssistantState is refreshed from the installed CLI and its sign-in/control probes.
type AssistantState struct {
	SignedIn       bool
	Available      bool
	ControlProfile bool
	Reason         string
	// Version is the CLI's reported version; a change re-checks a paused assistant.
	Version string
}

// RoutingPolicy selects assistants in the user's order, with the native loop last.
type RoutingPolicy struct {
	ManagedOrder []string
	Assistants   map[string]AssistantState
	// NativeOnly is the user's choice to have Isoform itself answer: no
	// assistant is tried.
	NativeOnly bool

	// These fields retain compatibility with callers configuring one assistant.
	ExternalFormat            string
	ExternalSignedIn          bool
	PreferExternalForHandoffs bool
	DefaultExternal           bool
}

// RouteCandidate records an eligible backend and why it was selected.
type RouteCandidate struct {
	Backend Backend
	Reason  string
}

// RoutingAgentRunner applies one route to chat, scheduled runs and playbooks.
// An assistant may fail over only when its tool ledger proves no action started.
type RoutingAgentRunner struct {
	native   AgentRunner
	external AgentRunner

	mu             sync.Mutex
	policy         RoutingPolicy
	policyProvider func(context.Context) RoutingPolicy
	observer       StreamCallback
	runScope       func(context.Context, *TaskInput) (context.Context, func(), error)
	activeScopes   map[*routedScope]bool
	stopped        bool
	assistants     map[string]AgentRunner
	ready          map[string]bool
	initLocks      map[string]*sync.Mutex
	cooldownUntil  map[string]time.Time
	cooldownProbe  map[string]bool
	breakers       map[string]*assistantBreaker
	healthObserver func()
	afterFunc      func(time.Duration, func()) *time.Timer
	probeCtx       context.Context
	probeCancel    context.CancelFunc
	now            func() time.Time
	runs           map[string]routedRun
	sessions       map[string]Backend
	sessionRefs    map[string]int
}

type routedRun struct {
	backend   Backend
	sessionID string
	endScope  func()
}

type routedScope struct{ end func() }

// SetRunScope installs the desktop's credential issuer. The scope covers one
// logical turn, including safe pre-action fallback, and ends before returning.
func (r *RoutingAgentRunner) SetRunScope(issue func(context.Context, *TaskInput) (context.Context, func(), error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runScope = issue
}

var _ AgentRunner = (*RoutingAgentRunner)(nil)

func NewRoutingAgentRunner(nativeRunner, externalRunner AgentRunner, policy RoutingPolicy) *RoutingAgentRunner {
	assistants := map[string]AgentRunner{}
	if externalRunner != nil {
		format := policy.ExternalFormat
		if format == "" {
			format = string(BackendExternal)
		}
		assistants[format] = externalRunner
	}
	r := NewOrderedRoutingAgentRunner(nativeRunner, assistants, policy)
	r.external = externalRunner
	return r
}

// NewOrderedRoutingAgentRunner accepts the independently configured assistant CLIs.
func NewOrderedRoutingAgentRunner(nativeRunner AgentRunner, assistants map[string]AgentRunner, policy RoutingPolicy) *RoutingAgentRunner {
	r := &RoutingAgentRunner{
		native: nativeRunner, policy: cloneRoutingPolicy(policy),
		assistants: make(map[string]AgentRunner), ready: make(map[string]bool),
		initLocks: make(map[string]*sync.Mutex), cooldownUntil: make(map[string]time.Time),
		cooldownProbe: make(map[string]bool), now: time.Now,
		runs: make(map[string]routedRun), sessions: make(map[string]Backend), sessionRefs: make(map[string]int),
	}
	for format, runner := range assistants {
		if runner != nil {
			r.assistants[format] = runner
			r.initLocks[format] = &sync.Mutex{}
		}
	}
	return r
}

func cloneRoutingPolicy(policy RoutingPolicy) RoutingPolicy {
	policy.ManagedOrder = append([]string(nil), policy.ManagedOrder...)
	if policy.Assistants != nil {
		states := make(map[string]AssistantState, len(policy.Assistants))
		for format, state := range policy.Assistants {
			states[format] = state
		}
		policy.Assistants = states
	}
	return policy
}

// SetPolicy refreshes account order or sign-in state without affecting active runs.
func (r *RoutingAgentRunner) SetPolicy(policy RoutingPolicy) {
	r.mu.Lock()
	r.policy = cloneRoutingPolicy(policy)
	r.reprobeOnChangeLocked()
	r.mu.Unlock()
}

// SetPolicyProvider supplies the cached, single-flight account probe used at run start.
func (r *RoutingAgentRunner) SetPolicyProvider(provider func(context.Context) RoutingPolicy) {
	r.mu.Lock()
	r.policyProvider = provider
	r.mu.Unlock()
}

// SetObserver records best-effort provenance before events reach a consumer.
// Observer errors are logged and never control dispatch or fallback. The tool
// server's durable action ledger remains the mandatory execution boundary.
func (r *RoutingAgentRunner) SetObserver(observer func(*StreamEvent) error) {
	r.mu.Lock()
	r.observer = observer
	r.mu.Unlock()
}

// RefreshPolicy applies the current cached account probes and user order.
func (r *RoutingAgentRunner) RefreshPolicy(ctx context.Context) {
	r.mu.Lock()
	provider := r.policyProvider
	r.mu.Unlock()
	if provider != nil {
		r.SetPolicy(provider(ctx))
	}
}

func assistantOrder(policy RoutingPolicy) []string {
	order := append([]string(nil), policy.ManagedOrder...)
	if len(order) == 0 {
		if policy.ExternalFormat != "" {
			order = append(order, policy.ExternalFormat)
		} else {
			order = append(order, string(BackendClaudeCode), string(BackendCodex))
		}
	}
	seen := make(map[string]bool)
	result := make([]string, 0, len(order))
	for _, format := range order {
		if format != "" && !seen[format] {
			seen[format] = true
			result = append(result, format)
		}
	}
	return result
}

func assistantState(policy RoutingPolicy, format string) AssistantState {
	if state, ok := policy.Assistants[format]; ok {
		return state
	}
	if format == policy.ExternalFormat {
		return AssistantState{SignedIn: policy.ExternalSignedIn, Available: true, ControlProfile: true}
	}
	return AssistantState{}
}

// chooseRoute is pure. Source-bound reads require native per-request checks;
// tool restrictions otherwise belong to the shared tool server. Unsupported
// control profiles and tools are represented in eligibility before routing.
func chooseRoute(input *TaskInput, policy RoutingPolicy, available map[string]bool, mode authority.ExecutionMode) []RouteCandidate {
	native := func(reason string) []RouteCandidate {
		return []RouteCandidate{{Backend: BackendNative, Reason: reason}}
	}
	if input == nil {
		return native("nil task input")
	}
	if input.RequireNativeDispatch {
		return native("source-bound context requires per-request read checks")
	}
	switch mode {
	case authority.ExecutionModeNormal, "":
	default:
		return native("execution mode " + string(mode) + " requires the native runner")
	}
	if input.BackendHint == BackendHintLearning {
		return native("background learning stays on the native runner")
	}
	if input.ForceBackend == string(BackendNative) {
		return native("the user requested the native runner")
	}
	if policy.NativeOnly && input.ForceBackend == "" {
		return native("the user chose Isoform to answer")
	}
	if extraToolsRequireNativeScope(externalTaskInput(input)) {
		return native("screen-level or authority-scoped turn tools require the native runner")
	}
	order := assistantOrder(policy)
	switch input.ForceBackend {
	case string(BackendClaudeCode), string(BackendCodex):
		order = append([]string{input.ForceBackend}, order...)
	}
	seen := make(map[string]bool)
	var candidates []RouteCandidate
	var skipped []string
	for _, format := range order {
		if seen[format] {
			continue
		}
		seen[format] = true
		state := assistantState(policy, format)
		reason := state.Reason
		switch true {
		case !state.Available || !available[format]:
			if reason == "" {
				reason = "not installed or unavailable"
			}
		case !state.SignedIn:
			reason = "signed out"
		case !state.ControlProfile:
			if reason == "" {
				reason = "tool control profile has not passed"
			}
		default:
			candidates = append(candidates, RouteCandidate{Backend: Backend(format), Reason: "signed-in assistant in the user's order"})
			continue
		}
		skipped = append(skipped, format+": "+reason)
	}
	reason := "Isoform is the backup after the user's assistants"
	if len(skipped) > 0 {
		reason += "; " + strings.Join(skipped, "; ")
	}
	return append(candidates, RouteCandidate{Backend: BackendNative, Reason: reason})
}

// chooseBackend preserves the old single-assistant selector for callers/tests.
func chooseBackend(input *TaskInput, policy RoutingPolicy, hasExternal bool, mode authority.ExecutionMode) (Backend, string) {
	route := chooseRoute(input, policy, map[string]bool{policy.ExternalFormat: hasExternal}, mode)
	if route[0].Backend != BackendNative {
		return BackendExternal, route[0].Reason
	}
	return route[0].Backend, route[0].Reason
}

func (r *RoutingAgentRunner) ExternalRunner() (string, bool) {
	r.mu.Lock()
	policy := cloneRoutingPolicy(r.policy)
	available := r.availabilityLocked()
	r.mu.Unlock()
	for _, candidate := range chooseRoute(&TaskInput{BackendHint: BackendHintChat}, policy, available, authority.ExecutionModeNormal) {
		if candidate.Backend != BackendNative {
			return string(candidate.Backend), true
		}
	}
	return "", false
}

func (r *RoutingAgentRunner) availabilityLocked() map[string]bool {
	available := make(map[string]bool, len(r.ready))
	for format, ready := range r.ready {
		available[format] = ready && !r.now().Before(r.cooldownUntil[format]) && !r.cooldownProbe[format] && !r.pausedLocked(format)
	}
	return available
}

func (r *RoutingAgentRunner) Native() AgentRunner {
	if r == nil {
		return nil
	}
	return r.native
}

func NativeRunnerOf(runner AgentRunner) (*NativeAgentRunner, bool) {
	switch r := runner.(type) {
	case *NativeAgentRunner:
		return r, r != nil
	case *RoutingAgentRunner:
		if r != nil {
			return NativeRunnerOf(r.native)
		}
	}
	return nil, false
}

func allowsTool(allowed []string, name string) bool {
	for _, tool := range allowed {
		if tool == name {
			return true
		}
	}
	return false
}

func (r *RoutingAgentRunner) ensureInitialized(ctx context.Context, format string) error {
	lock := r.initLocks[format]
	if lock == nil {
		return fmt.Errorf("assistant %s is not configured", format)
	}
	lock.Lock()
	defer lock.Unlock()
	r.mu.Lock()
	ready := r.ready[format]
	r.mu.Unlock()
	if ready {
		return nil
	}
	err := r.assistants[format].Init(ctx)
	r.mu.Lock()
	if err == nil && r.stopped {
		// Shutdown landed while Init ran (Init ignores cancellation): the
		// router is down, so the assistant is not ready.
		err = context.Canceled
	}
	r.ready[format] = err == nil
	r.mu.Unlock()
	return err
}

func (r *RoutingAgentRunner) Init(ctx context.Context) error {
	r.mu.Lock()
	r.stopped = false
	r.mu.Unlock()
	r.RefreshPolicy(ctx)
	if err := r.native.Init(ctx); err != nil {
		return err
	}
	for format := range r.assistants {
		if err := r.ensureInitialized(ctx, format); err != nil {
			log.Printf("[routing] %s unavailable: %v", format, err)
		}
	}
	return nil
}

func (r *RoutingAgentRunner) IsReady() bool { return r.native.IsReady() }

// actionLedgerError is supplied by controlled runners after the tool server has
// stopped. A model's tool events or prose can never prove that its ledger is empty.
type actionLedgerError interface {
	error
	ActionCount() int
	HasUnfinishedActions() bool
}

// BlockedRunError prevents replay after an action or an unproven outcome.
type BlockedRunError struct {
	Backend        Backend
	Actions        int
	UnknownOutcome bool
	Cause          error
}

func (e *BlockedRunError) Error() string {
	name := "assistant"
	if e.Backend == BackendNative {
		name = "Isoform"
	}
	action := "actions"
	if e.Actions == 1 {
		action = "action"
	}
	progress := fmt.Sprintf("%s stopped after %d %s", name, e.Actions, action)
	if e.Actions == 0 {
		progress = name + " stopped before any actions"
		if e.UnknownOutcome {
			progress = name + " stopped"
		}
	}
	if e.UnknownOutcome {
		progress += "; unknown outcome"
	}
	return fmt.Sprintf("%s: %v", progress, e.Cause)
}
func (e *BlockedRunError) Unwrap() error { return e.Cause }

func retryAfterFailure(err error, sawEvent bool, observedActions int) (bool, int, bool) {
	var ledger actionLedgerError
	var denied *native.DispatchDeniedError
	if errors.As(err, &denied) {
		if errors.As(err, &ledger) {
			return false, ledger.ActionCount(), ledger.HasUnfinishedActions()
		}
		return false, observedActions, false
	}
	if errors.As(err, &ledger) {
		count, unfinished := ledger.ActionCount(), ledger.HasUnfinishedActions()
		return count == 0 && !unfinished, count, unfinished
	}
	if !sawEvent {
		return true, 0, false
	}
	return false, observedActions, true
}

// quotaFailure recognizes explicit machine error names; ambiguous prose does not
// put a user's subscription into cooldown.
func quotaFailure(err error) bool {
	if err == nil {
		return false
	}
	var limit *AssistantUsageLimitError
	if errors.As(err, &limit) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"rate_limit_error", "rate_limit_exceeded", "insufficient_quota", "overloaded_error", "http 429", "status code: 429"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// beginAttempt grants only one post-cooldown probe across concurrent runs. An
// assistant another run has just paused is not attempted.
func (r *RoutingAgentRunner) beginAttempt(format string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, cooling := r.cooldownUntil[format]
	if r.now().Before(until) || r.cooldownProbe[format] || r.pausedLocked(format) {
		return false
	}
	if cooling {
		r.cooldownProbe[format] = true
	}
	return true
}

func (r *RoutingAgentRunner) finishAttempt(format string, err error) {
	r.mu.Lock()
	probing := r.cooldownProbe[format]
	delete(r.cooldownProbe, format)
	changed := false
	switch true {
	case quotaFailure(err):
		r.cooldownUntil[format] = r.now().Add(30 * time.Minute)
		changed = true
	case errors.Is(err, errAttemptNotJudged):
		// The probe slot is free again; the cool-down has already passed.
	case probing:
		delete(r.cooldownUntil, format)
		changed = true
	}
	if r.recordOutcomeLocked(format, err) {
		changed = true
	}
	r.mu.Unlock()
	if changed {
		r.notifyHealth()
	}
}

func (r *RoutingAgentRunner) ExecuteTask(ctx context.Context, input *TaskInput, callback StreamCallback) (string, error) {
	if input == nil {
		return "", fmt.Errorf("nil task input")
	}
	callerLabel := CallerLabel(input)
	if callback == nil {
		callback = func(*StreamEvent) error { return nil }
	}
	r.mu.Lock()
	observer := r.observer
	r.mu.Unlock()
	observe := func(event *StreamEvent) {
		if observer != nil {
			// A private run (SuppressTelemetry: memory canonicalization, internal
			// probes) may carry user text in its error (an upstream HTTP body):
			// route history keeps the failure, never that text.
			if input.SuppressTelemetry && event.Error != "" {
				private := *event
				private.Error = "error details withheld for a private run"
				event = &private
			}
			if err := observer(event); err != nil {
				log.Printf("[routing] provenance unavailable backend=%s run_id=%s event=%s: %v", event.Backend, event.RunID, event.Type, err)
			}
		}
	}
	endScope := func() {}
	consumer := callback
	callback = func(event *StreamEvent) error {
		observe(event)
		err := consumer(event)
		if err != nil {
			// Some backends ignore callback errors. End the credential and
			// cancel its run context before returning control to the backend.
			endScope()
		}
		return err
	}
	// Legacy callers resolve once for fallback. Route-aware chat defers each
	// tier until that backend is selected, without collecting unused context.
	resolved := *input
	if resolved.PromptFunc != nil && resolved.ExternalPromptFunc == nil {
		prompt, history := resolved.PromptFunc()
		if strings.TrimSpace(prompt) != "" {
			resolved.Prompt = prompt
		}
		// A reload after a full rewind can legitimately return no history.
		resolved.History = history
		resolved.PromptFunc = nil
	}
	input = &resolved
	r.mu.Lock()
	issue := r.runScope
	r.mu.Unlock()
	if issue != nil {
		var err error
		ctx, endScope, err = issue(ctx, input)
		if err != nil {
			return "", err
		}
		lease := &routedScope{end: endScope}
		r.mu.Lock()
		if r.stopped {
			r.mu.Unlock()
			endScope()
			return "", context.Canceled
		}
		if r.activeScopes == nil {
			r.activeScopes = make(map[*routedScope]bool)
		}
		r.activeScopes[lease] = true
		r.mu.Unlock()
		defer func() {
			endScope()
			r.mu.Lock()
			delete(r.activeScopes, lease)
			r.mu.Unlock()
		}()
	}
	r.RefreshPolicy(ctx)
	r.mu.Lock()
	policy := cloneRoutingPolicy(r.policy)
	r.mu.Unlock()
	if policy.Assistants == nil {
		policy.Assistants = make(map[string]AssistantState)
	}
	for format, runner := range r.assistants {
		if input.RequireNativeDispatch {
			break
		}
		state := assistantState(policy, format)
		if state.Available && state.SignedIn && state.ControlProfile {
			if err := r.ensureInitialized(ctx, format); err != nil {
				state.Available, state.Reason = false, err.Error()
			} else if eligibility, ok := runner.(interface {
				RoutingEligibility(*TaskInput) (bool, string)
			}); ok {
				state.Available, state.Reason = eligibility.RoutingEligibility(externalTaskInput(input))
			}
			if state.Available && taskHasExtraTools(externalTaskInput(input)) {
				toolsRunner, ok := runner.(ExtraToolsRunner)
				if !ok || !toolsRunner.SupportsExtraTools(externalTaskInput(input)) {
					state.Available = false
					state.Reason = "assistant cannot carry this turn's Isoform tools"
				}
			}
		}
		policy.Assistants[format] = state
	}
	r.mu.Lock()
	available := r.availabilityLocked()
	for format, until := range r.cooldownUntil {
		if r.now().Before(until) || r.cooldownProbe[format] {
			state := assistantState(policy, format)
			state.Reason = "assistant is in cooldown"
			policy.Assistants[format] = state
		}
	}
	for format, b := range r.breakers {
		if b.paused {
			state := assistantState(policy, format)
			state.Reason = pausedRouteReason(b.code)
			policy.Assistants[format] = state
		}
	}
	r.mu.Unlock()
	route := chooseRoute(input, policy, available, input.ExecutionMode)
	candidates := make([]string, 0, len(route))
	for _, candidate := range route {
		candidates = append(candidates, string(candidate.Backend))
	}
	var fallbackFrom, fallbackReasons []string
	var lastRunID string
	var nativeResolved, externalResolved *TaskInput
	for _, candidate := range route {
		if err := ctx.Err(); err != nil {
			return lastRunID, err
		}
		backend := candidate.Backend
		if backend != BackendNative && !r.beginAttempt(string(backend)) {
			r.mu.Lock()
			skip := "assistant is in cooldown"
			if b := r.breakers[string(backend)]; b != nil && b.paused {
				skip = pausedRouteReason(b.code)
			}
			r.mu.Unlock()
			fallbackReasons = append(fallbackReasons, string(backend)+": "+skip)
			continue
		}
		reason := candidate.Reason
		if len(fallbackReasons) > 0 {
			reason += "; " + strings.Join(fallbackReasons, "; ")
		}
		tag := func(event *StreamEvent) {
			event.Backend = string(backend)
			event.CandidateBackends = append([]string(nil), candidates...)
			event.FallbackFrom = append([]string(nil), fallbackFrom...)
			event.BackendReason = reason
		}
		routeEvent := &StreamEvent{Type: "backend_route", SessionID: input.SessionID}
		tag(routeEvent)
		if err := callback(routeEvent); err != nil {
			if backend != BackendNative {
				r.finishAttempt(string(backend), errAttemptNotJudged)
			}
			return lastRunID, err
		}
		log.Printf("[routing] backend=%s reason=%q hint=%q caller=%s", backend, reason, input.BackendHint, callerLabel)
		var eventMu sync.Mutex
		var provisional, terminal []*StreamEvent
		var callbackErr, streamErr error
		var attached []string
		sawEvent, observedActions := false, 0
		ledgerActions := 0
		committed := backend == BackendNative
		permissionShown := false
		startedActions := make(map[string]bool)
		finishedActions := make(map[string]bool)
		nativeUnknownOutcome := func() bool {
			if len(startedActions) != ledgerActions {
				return true
			}
			for id := range startedActions {
				if !finishedActions[id] {
					return true
				}
			}
			return false
		}
		flushProvisional := func() error {
			for _, event := range provisional {
				if callbackErr != nil {
					break
				}
				callbackErr = callback(event)
			}
			provisional = nil
			return callbackErr
		}
		routed := func(event *StreamEvent) error {
			if event == nil {
				return nil
			}
			eventMu.Lock()
			defer eventMu.Unlock()
			sawEvent = true
			if event.ActionsStarted > ledgerActions {
				ledgerActions = event.ActionsStarted
			}
			// Keep tracking native completion even if a disconnected consumer
			// has stopped accepting events. A returned tool result is known.
			if backend == BackendNative {
				switch event.Type {
				case "action_started":
					if event.ToolUseID != "" {
						startedActions[event.ToolUseID] = true
					}
				case "tool_result":
					if startedActions[event.ToolUseID] {
						finishedActions[event.ToolUseID] = true
					}
				}
			}
			if callbackErr != nil {
				return callbackErr
			}
			copyEvent := *event
			tag(&copyEvent)
			switch copyEvent.Type {
			case "run_created":
				r.attach(copyEvent.RunID, copyEvent.SessionID, backend, endScope)
				attached = append(attached, copyEvent.RunID)
				callbackErr = callback(&copyEvent)
				return callbackErr
			case "tool_use":
				observedActions++
			case "error":
				streamErr = errors.New(copyEvent.Error)
			}
			switch copyEvent.Type {
			case "error", "done", "close":
				terminal = append(terminal, &copyEvent)
				return nil
			case "action_started":
				if ledgerActions > 0 {
					committed = true
					if err := flushProvisional(); err != nil {
						return err
					}
				}
			case "permission_request":
				// The gate is waiting for the user, so this prompt cannot wait
				// for completion. Expose its context in order and commit this
				// attempt: replaying a visible approval on a backup is forbidden.
				committed = true
				if err := flushProvisional(); err != nil {
					return err
				}
				permissionShown = true
			default:
				if !committed {
					// Tool cards and denials are provisional just like prose.
					// One FIFO preserves source order and can be discarded whole.
					provisional = append(provisional, &copyEvent)
					return nil
				}
			}
			callbackErr = callback(&copyEvent)
			return callbackErr
		}
		attemptCtx := ctx
		cancelAttempt := func() {}
		if budget := NativeFallbackBudget(ctx); backend == BackendNative && budget > 0 {
			attemptCtx, cancelAttempt = context.WithTimeout(ctx, budget)
		}
		if err := checkTaskDispatch(attemptCtx, input); err != nil {
			cancelAttempt()
			if backend != BackendNative {
				// No provider request was attempted. Release any cooldown probe
				// without treating a read-policy refusal as a provider failure.
				r.finishAttempt(string(backend), errAttemptNotJudged)
			}
			return lastRunID, err
		}
		attemptInput := input
		if backend != BackendNative {
			attemptInput = externalTaskInput(input)
		}
		if input.ExternalPromptFunc != nil {
			cached := &nativeResolved
			compose := input.PromptFunc
			if backend != BackendNative {
				cached, compose = &externalResolved, input.ExternalPromptFunc
			}
			if *cached == nil {
				copy := *input
				if backend != BackendNative {
					copy = *externalTaskInput(input)
				}
				if compose != nil {
					copy.Prompt, copy.History = compose()
				}
				copy.PromptFunc, copy.ExternalPromptFunc = nil, nil
				copy.ExternalExtraTools = nil
				*cached = &copy
			}
			attemptInput = *cached
		}
		runID, err := r.runnerFor(backend).ExecuteTask(attemptCtx, attemptInput, routed)
		attemptErr := attemptCtx.Err()
		cancelAttempt()
		lastRunID = runID
		observeFailure := func(cause error, blocked bool, actions int) {
			event := &StreamEvent{Type: "error", RunID: runID, SessionID: input.SessionID, Error: cause.Error(), Status: providerHTTPStatus(cause), IsError: true, Blocked: blocked, ActionsStarted: actions}
			tag(event)
			observe(event)
		}
		for _, id := range attached {
			r.detach(id)
		}
		r.detach(runID)
		if err == nil {
			err = streamErr
		}
		if err == nil {
			err = attemptErr
		}
		if backend != BackendNative {
			outcome := err
			if callbackErr != nil || ctx.Err() != nil {
				// The consumer left or the caller cancelled: no verdict.
				outcome = errAttemptNotJudged
			}
			r.finishAttempt(string(backend), outcome)
		}
		if err == nil {
			if callbackErr != nil {
				err = callbackErr
			} else if ctx.Err() != nil {
				err = ctx.Err()
			}
		}
		if err != nil && (backend != BackendNative || ledgerActions > 0) {
			retry, actions, unknown := retryAfterFailure(err, sawEvent, observedActions)
			if backend == BackendNative {
				retry, actions, unknown = false, ledgerActions, nativeUnknownOutcome()
			}
			if ledgerActions > actions {
				// An authoritative dispatch receipt cannot be erased by a
				// conflicting final snapshot. Treat the discrepancy as unknown.
				retry, actions, unknown = false, ledgerActions, true
			}
			if retry && permissionShown {
				// No action ran, but the user has already interacted with this
				// attempt. Fail normally without fabricating a blocked action.
				event := &StreamEvent{Type: "error", RunID: runID, SessionID: input.SessionID, Error: err.Error(), Status: providerHTTPStatus(err), IsError: true}
				tag(event)
				_ = callback(event)
				closeEvent := &StreamEvent{Type: "close", RunID: runID, SessionID: input.SessionID}
				tag(closeEvent)
				_ = callback(closeEvent)
				return runID, err
			}
			if retry {
				// Failed provisional output is hidden from consumers. Attempt
				// terminal provenance independently of the safe fallback.
				observeFailure(err, false, 0)
				if errors.Is(err, context.Canceled) {
					return runID, err
				}
				if callbackErr != nil {
					return runID, callbackErr
				}
				if ctx.Err() != nil {
					return runID, ctx.Err()
				}
				fallbackFrom = append(fallbackFrom, string(backend))
				fallbackReasons = append(fallbackReasons, string(backend)+": "+err.Error())
				continue
			}
			blocked := &BlockedRunError{Backend: backend, Actions: actions, UnknownOutcome: unknown, Cause: err}
			_ = flushProvisional()
			event := &StreamEvent{Type: "error", RunID: runID, SessionID: input.SessionID, Error: blocked.Error(), IsError: true, Blocked: true, ActionsStarted: actions}
			tag(event)
			// The typed error remains authoritative when a disconnected consumer
			// cannot receive the final event (including cancellation after a write).
			_ = callback(event)
			closeEvent := &StreamEvent{Type: "close", RunID: runID, SessionID: input.SessionID, Blocked: true}
			tag(closeEvent)
			_ = callback(closeEvent)
			return runID, blocked
		}
		// The consumer left or the caller cancelled before the buffered end
		// of the run was delivered. Record that end, or its route stays
		// "running" forever.
		if callbackErr != nil {
			observeFailure(callbackErr, false, 0)
			return runID, callbackErr
		}
		if ctx.Err() != nil {
			observeFailure(ctx.Err(), false, 0)
			return runID, ctx.Err()
		}
		for _, event := range append(provisional, terminal...) {
			if emitErr := callback(event); emitErr != nil {
				if ledgerActions > 0 || (backend != BackendNative && observedActions > 0) {
					actions, unknown := ledgerActions, false
					if backend == BackendNative {
						unknown = nativeUnknownOutcome()
					}
					if actions == 0 {
						actions, unknown = observedActions, true
					}
					blocked := &BlockedRunError{Backend: backend, Actions: actions, UnknownOutcome: unknown, Cause: emitErr}
					observeFailure(blocked, true, actions)
					return runID, blocked
				}
				observeFailure(emitErr, false, 0)
				return runID, emitErr
			}
		}
		if err != nil {
			observeFailure(err, false, 0)
		}
		return runID, err
	}
	return lastRunID, fmt.Errorf("no backend could execute the task")
}

func (r *RoutingAgentRunner) attach(runID, sessionID string, backend Backend, endScope ...func()) {
	if runID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.runs[runID]; ok {
		return
	}
	r.runs[runID] = routedRun{backend: backend, sessionID: sessionID}
	if len(endScope) > 0 {
		run := r.runs[runID]
		run.endScope = endScope[0]
		r.runs[runID] = run
	}
	if sessionID != "" {
		r.sessions[sessionID] = backend
		r.sessionRefs[sessionID]++
	}
}

func (r *RoutingAgentRunner) detach(runID string) {
	if runID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return
	}
	delete(r.runs, runID)
	if run.sessionID != "" {
		r.sessionRefs[run.sessionID]--
		if r.sessionRefs[run.sessionID] <= 0 {
			delete(r.sessionRefs, run.sessionID)
			delete(r.sessions, run.sessionID)
		}
	}
}

func (r *RoutingAgentRunner) runnerFor(backend Backend) AgentRunner {
	if backend == BackendExternal && r.external != nil {
		return r.external
	}
	if runner := r.assistants[string(backend)]; runner != nil {
		return runner
	}
	return r.native
}

func (r *RoutingAgentRunner) runnerForSession(sessionID string) AgentRunner {
	r.mu.Lock()
	backend := r.sessions[sessionID]
	r.mu.Unlock()
	return r.runnerFor(backend)
}

func (r *RoutingAgentRunner) CancelRun(runID string) error {
	r.mu.Lock()
	run := r.runs[runID]
	r.mu.Unlock()
	if run.endScope != nil {
		run.endScope()
	}
	return r.runnerFor(run.backend).CancelRun(runID)
}

func (r *RoutingAgentRunner) RespondToPermission(sessionID, optionID string) error {
	// Every assistant tool executes through the native permission manager.
	return r.native.RespondToPermission(sessionID, optionID)
}

func (r *RoutingAgentRunner) CancelPermission(sessionID string) {
	r.native.CancelPermission(sessionID)
}

func (r *RoutingAgentRunner) Shutdown() error {
	r.mu.Lock()
	r.stopped = true
	var scopes []func()
	for scope := range r.activeScopes {
		scopes = append(scopes, scope.end)
	}
	for format := range r.ready {
		r.ready[format] = false
	}
	for _, b := range r.breakers {
		if b.timer != nil {
			b.timer.Stop()
			b.timer = nil
		}
		if b.probing {
			r.abandonProbeLocked(b)
		}
	}
	if r.probeCancel != nil {
		r.probeCancel()
		r.probeCtx, r.probeCancel = nil, nil
	}
	r.mu.Unlock()
	for _, end := range scopes {
		end()
	}
	first := r.native.Shutdown()
	for _, runner := range r.assistants {
		if err := runner.Shutdown(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// externalTaskInput never mutates the caller's native registry or tool slice.
func externalTaskInput(input *TaskInput) *TaskInput {
	if len(input.ExternalExtraTools) == 0 {
		return input
	}
	copy := *input
	copy.ExtraTools = append(append([]tools.Tool(nil), input.ExtraTools...), input.ExternalExtraTools...)
	copy.ExternalExtraTools = nil
	return &copy
}

// providerHTTPStatus returns the provider's HTTP status that err carries, or 0.
func providerHTTPStatus(err error) int {
	var he *native.HTTPError
	if errors.As(err, &he) && he != nil {
		return he.Status
	}
	return 0
}
