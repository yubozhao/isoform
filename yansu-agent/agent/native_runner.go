package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"yansu-agent/agent/authority"
	"yansu-agent/agent/native"
	"yansu-agent/agent/tools"
	"yansu-agent/agent/tools/guard"
)

// NativeAgentRunner is the single in-process implementation of the
// AgentRunner interface. It drives the Anthropic-native loop (see
// agent/native/loop.go) and owns the tool registry.
type NativeAgentRunner struct {
	mu sync.Mutex

	client       native.LLMClient
	registry     *tools.Registry
	bash         *tools.BashTool
	locks        *guard.FileLockManager
	perm         *native.PermissionManager
	sessionTrust *native.SessionTrust
	// onPersistentAllow is invoked when the loop reports the user picked
	// "Always allow this server" on a permission prompt. Set by callers
	// (e.g. wails-gui) to persist trust into the MCP trusted-server
	// file and refresh the underlying adapter. Nil = drop the signal.
	onPersistentAllow func(prefix string)
	history           native.HistoryLoader

	// Per-run cancel funcs for CancelRun.
	runs map[string]context.CancelFunc
	// Per-session plan-mode states (one per active session).
	planStates map[string]*tools.PlanModeState
	// Per-session todo lists.
	todos map[string]*tools.TodoList

	defaultModel           string
	systemText             string
	modelAliases           map[string]string
	backgroundModelAliases map[string]string
	backgroundModel        func() string
	interactiveModel       func(string) string
	modelEffort            func(string, string) string
	telemetry              native.Telemetry
	authorityRecorder      func(authority.Evaluation)

	ready bool
}

// resolveModel rewrites a short alias into the pinned API id using the
// configured alias table. Values not in the table pass through unchanged.
func (r *NativeAgentRunner) resolveModel(model string) string {
	return r.resolveModelForPriority(model, PriorityInteractive)
}

// resolveModelForPriority resolves model for a task priority. Background
// tasks consult BackgroundModelAliases first, then ModelAliases. Every other
// priority uses ModelAliases only.
func (r *NativeAgentRunner) resolveModelForPriority(model, priority string) string {
	if model == "" {
		return model
	}
	if priority != PriorityBackground && r.interactiveModel != nil {
		if id := r.interactiveModel(model); id != "" {
			return id
		}
	}
	if priority == PriorityBackground && r.backgroundModel != nil {
		if id := r.backgroundModel(); id != "" {
			return id
		}
	}
	if priority == PriorityBackground {
		if id, ok := r.backgroundModelAliases[model]; ok && id != "" {
			return id
		}
	}
	if r.modelAliases == nil {
		return model
	}
	if id, ok := r.modelAliases[model]; ok && id != "" {
		return id
	}
	return model
}

// NativeRunnerConfig configures the runner at construction.
type NativeRunnerConfig struct {
	OnWebSearch func(service string)
	// BaseURL overrides the Anthropic API base (use sub2api's URL).
	BaseURL string
	// APIKey is the x-api-key header. Takes precedence over AuthToken
	// when both are set (the gateway decides).
	APIKey string
	// AuthToken sets Authorization: Bearer <token> for sub2api-style
	// gateways.
	AuthToken string
	// DisableEnvFallback prevents BaseURL/APIKey/AuthToken from being
	// back-filled from the ANTHROPIC_* environment when left empty. The
	// gateway path RELIES on that fallback (it injects the sub2api token /
	// URL into the env), but BYOA / BYOK must NOT inherit it: a user who
	// brings their own Anthropic key with an empty BaseURL would otherwise
	// be silently routed to the Yansu gateway (and tagged with the gateway
	// bearer token). Callers set this for BYOA so the config is the sole
	// source of truth and an empty BaseURL means "provider default".
	DisableEnvFallback bool
	// ProviderFormat selects the upstream wire protocol. "" or
	// "anthropic" uses the native Anthropic Messages client; "openai"
	// uses the translating OpenAI Chat Completions adapter. BYOA sets
	// this from the user's provider config so the same loop can drive an
	// OpenAI-compatible endpoint without any other call site changing.
	ProviderFormat string
	// DefaultModel is used when TaskInput.Model is empty. May itself
	// be a short alias — ModelAliases is applied before dispatch.
	DefaultModel string
	// SystemText overrides the default system prompt. Empty = default.
	SystemText string
	// History loader for resuming prior sessions. Nil = no history.
	History native.HistoryLoader
	// ModelAliases maps friendly short names (e.g. "opus", "sonnet",
	// "haiku") to the pinned API model ids the upstream accepts (e.g.
	// "claude-opus-4-6"). Every TaskInput.Model and DefaultModel is
	// resolved through this table before being sent. Values not in
	// the table pass through unchanged, so callers can freely mix
	// aliases and full ids. Letting the caller own the table keeps
	// business-specific model policy out of the runner.
	ModelAliases map[string]string
	// BackgroundModelAliases is consulted before ModelAliases for tasks with
	// Priority == PriorityBackground. It lets background work (summaries,
	// extraction, triage) resolve to a different route than interactive
	// chat, for example a route that never drains the foreground budget.
	// Values not in the table fall through to ModelAliases.
	BackgroundModelAliases map[string]string
	// BackgroundModel, when it returns a model, runs every background task
	// on it (the user chose to use their pick for background work too). It is
	// read per task, so a change applies to the next run without a restart.
	BackgroundModel func() string
	// InteractiveModel overrides a model per interactive task when nonempty.
	InteractiveModel func(string) string
	// ModelEffort selects a per-task gateway reasoning effort.
	ModelEffort func(string, string) string
	// Telemetry is the observability hook the underlying agent loop
	// uses to emit lifecycle events (loop start/end, tool-pairing
	// detection, retry recovery, terminal errors). Nil falls back to
	// native.NoOpTelemetry — callers without telemetry needs don't
	// have to wire anything up. wails-gui injects a PostHog-backed
	// implementation here so end-to-end LLM traces become queryable.
	Telemetry native.Telemetry

	// OnPersistentAllow is forwarded to every LoopConfig the runner
	// builds. wails-gui sets this to persist "Always allow this server"
	// grants into the MCP trusted-server file and refresh the running
	// adapter. Nil = the loop drops the signal (only session-scoped
	// trust takes effect).
	OnPersistentAllow func(prefix string)
	// AuthorityRecorder receives every normalized capability decision before
	// execution. The desktop app persists it in the local authority ledger.
	AuthorityRecorder func(authority.Evaluation)
}

// NewNativeAgentRunner constructs a runner from config.
func NewNativeAgentRunner(cfg NativeRunnerConfig) *NativeAgentRunner {
	clientOpts := []native.ClientOption{}
	// Back-fill from the gateway-injected ANTHROPIC_* env only when the
	// caller hasn't opted out. BYOA sets DisableEnvFallback so an empty
	// BaseURL/AuthToken can never silently resolve to the Yansu gateway.
	if !cfg.DisableEnvFallback {
		if cfg.BaseURL == "" {
			cfg.BaseURL = os.Getenv("ANTHROPIC_BASE_URL")
		}
		if cfg.APIKey == "" {
			cfg.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if cfg.AuthToken == "" {
			cfg.AuthToken = os.Getenv("ANTHROPIC_AUTH_TOKEN")
		}
	}
	if cfg.BaseURL != "" {
		clientOpts = append(clientOpts, native.WithBaseURL(cfg.BaseURL))
	}
	if cfg.APIKey != "" {
		clientOpts = append(clientOpts, native.WithAPIKey(cfg.APIKey))
	}
	if cfg.AuthToken != "" {
		clientOpts = append(clientOpts, native.WithAuthToken(cfg.AuthToken))
	}

	// Select the upstream transport. The OpenAI adapter translates the
	// Anthropic-shaped loop traffic into Chat Completions; everything
	// else (default) speaks Anthropic Messages natively.
	var client native.LLMClient
	if strings.EqualFold(cfg.ProviderFormat, "openai") {
		log.Printf("[native-runner] using OpenAI-compatible client (base=%q)", cfg.BaseURL)
		client = native.NewOpenAIClient(cfg.BaseURL, cfg.APIKey, cfg.AuthToken)
	} else {
		client = native.NewClient(clientOpts...)
	}

	locks := guard.NewFileLockManager(0)
	bash := tools.NewBashTool()
	bash.OnWebSearch = cfg.OnWebSearch

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadTool())
	registry.Register(tools.NewWriteTool(locks))
	registry.Register(tools.NewEditTool(locks))
	registry.Register(bash)
	registry.Register(tools.NewBashOutputTool(bash))
	registry.Register(tools.NewKillShellTool(bash))
	registry.Register(tools.NewGrepTool())
	registry.Register(tools.NewGlobTool())
	fetch := tools.NewWebFetchTool()
	fetch.OnWebSearch = cfg.OnWebSearch
	registry.Register(fetch)
	registry.Register(tools.NewWebSearchTool())
	registry.Register(tools.NewNotebookEditTool(locks))
	// cua_driver is a thin proxy to the cua-driver daemon Yansu spawns
	// at ~/.yansu-agent/run/cua-driver.sock. macOS-only at runtime
	// (Execute returns a friendly error elsewhere); registering it
	// unconditionally keeps the tool list stable across platforms.
	registry.Register(tools.NewCuaDriverTool())

	// Per-session state helpers (todo / plan) are constructed on-demand
	// so they don't leak across sessions.

	telemetry := cfg.Telemetry
	if telemetry == nil {
		telemetry = native.NoOpTelemetry{}
	}

	return &NativeAgentRunner{
		client:                 client,
		registry:               registry,
		bash:                   bash,
		locks:                  locks,
		perm:                   native.NewPermissionManager(),
		sessionTrust:           native.NewSessionTrust(),
		onPersistentAllow:      cfg.OnPersistentAllow,
		history:                cfg.History,
		runs:                   make(map[string]context.CancelFunc),
		planStates:             make(map[string]*tools.PlanModeState),
		todos:                  make(map[string]*tools.TodoList),
		defaultModel:           cfg.DefaultModel,
		systemText:             cfg.SystemText,
		modelAliases:           cfg.ModelAliases,
		backgroundModelAliases: cfg.BackgroundModelAliases,
		backgroundModel:        cfg.BackgroundModel,
		interactiveModel:       cfg.InteractiveModel,
		modelEffort:            cfg.ModelEffort,
		telemetry:              telemetry,
		authorityRecorder:      cfg.AuthorityRecorder,
	}
}

// SetOnPersistentAllow lets callers wire (or rewire) the
// "always allow this server" callback after construction. wails-gui
// calls this once the MCP runtime is available — by which point the
// runner may already be alive. Safe to call concurrently with running
// loops; new permission grants will pick up the latest callback.
func (r *NativeAgentRunner) SetOnPersistentAllow(fn func(prefix string)) {
	r.mu.Lock()
	r.onPersistentAllow = fn
	r.mu.Unlock()
}

// ClearSessionTrust drops the in-memory "Allow for this chat" entries
// for a session. Callers invoke this when a chat is closed / archived
// so the trust map can't grow unbounded across a long-running app.
func (r *NativeAgentRunner) ClearSessionTrust(sessionID string) {
	r.sessionTrust.ClearSession(sessionID)
}

// Registry returns the runner's base tool registry. The registry is
// the canonical source of truth for which tools the agent loop can
// dispatch; per-session scoped clones (registryForSession) snapshot
// from this base, so mutations to the base affect newly-started
// sessions but not in-flight ones. The MCP bridge calls ReplaceGroup
// on this registry to inject server-provided tools.
func (r *NativeAgentRunner) Registry() *tools.Registry {
	return r.registry
}

// BashTool returns the shared Bash tool instance so callers (e.g. the
// wails-gui app) can register a background-shell-done callback via
// SetOnDone.
func (r *NativeAgentRunner) BashTool() *tools.BashTool {
	return r.bash
}

// Init marks the runner ready. There's no connection to spin up —
// the HTTP client does per-request network I/O.
func (r *NativeAgentRunner) Init(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready = true
	return nil
}

// IsReady reports readiness.
func (r *NativeAgentRunner) IsReady() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready
}

// ExecuteTask starts a streaming task. The callback receives events
// that the caller translates into StreamEvents for the frontend.
func (r *NativeAgentRunner) ExecuteTask(ctx context.Context, input *TaskInput, callback StreamCallback) (string, error) {
	r.mu.Lock()
	if !r.ready {
		r.mu.Unlock()
		return "", fmt.Errorf("runner not initialized")
	}
	r.mu.Unlock()

	// Resolve PromptFunc.
	if input.Prompt == "" && input.PromptFunc != nil {
		p, h := input.PromptFunc()
		input.Prompt = p
		if h != "" && input.History == "" {
			input.History = h
		}
	}

	runID := "run_" + uuid.NewString()
	runCtx, cancel := context.WithCancel(ctx)
	runCtx = native.ContextWithBeforeDispatch(runCtx, input.BeforeDispatch)

	r.mu.Lock()
	r.runs[runID] = cancel
	sessionID := input.SessionID
	// When the caller doesn't pin a sessionID we mint a throwaway key. The
	// plan/todo maps keyed on this minted id are not reachable by any future
	// run (the caller never sees the id, so it can't be reused), so they must
	// be released in the defer below — otherwise every ephemeral run (cron
	// stateless runs, runEphemeralHaiku probes, generators, etc.) leaks
	// one PlanModeState + one TodoList into the runner for the process
	// lifetime.
	ephemeralSession := sessionID == ""
	if ephemeralSession {
		sessionID = "ephemeral_" + uuid.NewString()
	}
	planState, ok := r.planStates[sessionID]
	if !ok {
		planState = tools.NewPlanModeState(input.PlanMode)
		r.planStates[sessionID] = planState
	} else {
		planState.Set(input.PlanMode)
	}
	todoList, ok := r.todos[sessionID]
	if !ok {
		todoList = tools.NewTodoList()
		r.todos[sessionID] = todoList
	}
	r.mu.Unlock()

	// Add plan + todo tools bound to this session's state. We register
	// them in a local registry to keep the global one stateless.
	scopedReg := r.registryForSession(planState, todoList, input.AllowedTools)
	addExtraTools(scopedReg, input.ExtraTools)

	model := r.resolveModelForPriority(input.Model, input.Priority)
	if model == "" {
		model = r.resolveModelForPriority(r.defaultModel, input.Priority)
	}

	effort := ""
	if r.modelEffort != nil {
		effort = r.modelEffort(input.Model, input.Priority)
	}

	// Translate images.
	var loopImages []native.ImageSource
	for _, img := range input.Images {
		loopImages = append(loopImages, native.ImageSource{
			Type:      "base64",
			MediaType: img.MediaType,
			Data:      img.DataBase64,
		})
	}

	log.Printf("[native] ExecuteTask runID=%s traceID=%s session=%s model=%s workDir=%s baseURL=%s hasAuthToken=%v hasAPIKey=%v",
		runID, input.TraceID, sessionID, model, input.WorkDir, os.Getenv("ANTHROPIC_BASE_URL"),
		os.Getenv("ANTHROPIC_AUTH_TOKEN") != "", os.Getenv("ANTHROPIC_API_KEY") != "")

	// Emit run_created carrying the IDs up.
	_ = callback(&StreamEvent{Type: "run_created", RunID: runID, SessionID: sessionID, Backend: string(BackendNative)})

	defer func() {
		r.mu.Lock()
		delete(r.runs, runID)
		if ephemeralSession {
			delete(r.planStates, sessionID)
			delete(r.todos, sessionID)
		}
		r.mu.Unlock()
		cancel()
	}()

	// Per-task system override falls through to the runner's default
	// when the caller didn't set one.
	systemText := input.SystemText
	if systemText == "" {
		systemText = r.systemText
	}

	// Background / internal LLM calls (generators, ephemeral JSON
	// probes, etc.) opt out of agentloop.* telemetry via
	// input.SuppressTelemetry. We also blank out TraceID for these runs
	// AND stamp runCtx with a SuppressAgentloop flag that the HTTP layer
	// turns into the x-yansu-suppress-agentloop header — the backend's
	// telemetry layer reads that header and skips its agentloop.backend.*
	// emissions. Blanking TraceID alone is not enough on the backend
	// side: its trace_id middleware mints a fresh UUID when the header
	// is absent, which would make a plain "no trace_id → drop" rule
	// unreachable.
	//
	// runCtx (not the parent ctx) is the load-bearing wrap: runCtx was
	// derived from ctx via context.WithCancel above, so any value added
	// to ctx AFTER that derivation would not propagate to runCtx — and
	// runCtx is what native.Run sees and what doStream eventually reads
	// via SuppressAgentloopFromContext. Wrapping ctx too would be dead
	// code and a footgun for future maintainers.
	telemetrySink := r.telemetry
	loopTraceID := input.TraceID
	if input.SuppressTelemetry {
		telemetrySink = native.NoOpTelemetry{}
		loopTraceID = ""
		runCtx = native.ContextWithSuppressAgentloop(runCtx)
	}

	r.mu.Lock()
	onPersistentAllow := r.onPersistentAllow
	r.mu.Unlock()

	authorityEvaluator := input.AuthorityEvaluator
	if authorityEvaluator == nil {
		authorityEvaluator = authority.NewPolicy(input.AuthorityGrants)
	}
	cfg := &native.LoopConfig{
		Client:                 r.client,
		Registry:               scopedReg,
		History:                r.history,
		Permission:             r.perm,
		SessionTrust:           r.sessionTrust,
		OnPersistentAllow:      onPersistentAllow,
		AutoApproveTools:       input.AutoApproveTools,
		AskBeforeRiskyCommands: input.AskBeforeRiskyCommands,
		AuthorityEvaluator:     authorityEvaluator,
		AuthorityContext: authority.Context{
			Initiative: input.AuthorityInitiativeID,
			ShadowRun:  input.AuthorityShadowRunID,
			Mode:       input.ExecutionMode,
			Unattended: input.AutoApproveTools,
		},
		AuthorityRecorder:       r.authorityRecorder,
		SystemText:              systemText,
		NoSystem:                input.NoSystem,
		RunID:                   runID,
		TraceID:                 loopTraceID,
		Telemetry:               telemetrySink,
		SessionID:               sessionID,
		HistoryKey:              input.HistoryKey,
		WorkDir:                 input.WorkDir,
		ThinkingEnabled:         input.ThinkingEnabled,
		ModelEffort:             effort,
		PlanMode:                planState,
		AllowedTools:            input.AllowedTools,
		DisabledTools:           input.DisabledTools,
		DeniedBashPrefixes:      deniedBashPrefixes(input),
		NoTools:                 input.NoTools,
		ProgrammaticToolCalling: input.ProgrammaticToolCalling,
		ModelID:                 model,
		MaxTokens:               native.MaxOutputTokensForModel(model),
		MaxTotalTokens:          input.MaxTotalTokens,
		Prompt:                  input.Prompt,
		Images:                  loopImages,
		RecoveryNudgeText:       input.RecoveryNudgeText,
		MaxNudges:               input.MaxNudges,

		// Phase 1.5 — Context Engine snapshot plumbing. Nil-safe:
		// when input doesn't carry a collector (background/ephemeral
		// runs, suppressed telemetry, callers that don't opt in) the
		// loop just skips CE field emission. wails-gui is the
		// canonical caller that wires these.
		ContextEngineCollector: input.ContextEngineCollector,
		Redactor:               input.Redactor,
	}

	cfg.Emit = func(ev native.LoopEvent) {
		out := r.loopEventToStream(ev, runID, sessionID)
		if out != nil {
			_ = callback(out)
		}
	}
	actionsStarted := 0
	cfg.BeforeToolExecution = func(name string, call tools.ToolCall) error {
		actionsStarted++
		return callback(&StreamEvent{Type: "action_started", RunID: runID, SessionID: sessionID, ToolName: name, ToolUseID: call.ID, ActionsStarted: actionsStarted})
	}

	// The HTTP clients send the chat session id as X-Magpie-Session and
	// report the account that served each response. The route observer
	// forwards that as model_route on this run's callback. The observer runs
	// on the loop goroutine, so the callback sees the same ordering as the
	// loop's own events.
	runCtx = native.ContextWithSessionID(runCtx, sessionID)
	runCtx = native.ContextWithModelRouteObserver(runCtx, newModelRouteObserver(func(provider, servedModel string) {
		_ = callback(&StreamEvent{
			Type:        "model_route",
			RunID:       runID,
			SessionID:   sessionID,
			Provider:    provider,
			ServedModel: servedModel,
		})
	}))

	// Run the loop synchronously; callers (wails-gui/streaming.go) already
	// wrap this call in their own goroutine, and they rely on ExecuteTask
	// blocking until the run finishes so their `defer cancel()` doesn't
	// kill the context while the HTTP stream is still in flight.
	loopErr := native.Run(runCtx, cfg)
	_ = callback(&StreamEvent{Type: "close", RunID: runID})
	return runID, loopErr
}

// loopEventToStream maps the loop's internal events into the external
// StreamEvent shape. Returns nil to suppress events the frontend
// doesn't render (e.g. ping).
func (r *NativeAgentRunner) loopEventToStream(ev native.LoopEvent, runID, sessionID string) *StreamEvent {
	out := &StreamEvent{
		Type:      ev.Type,
		RunID:     runID,
		SessionID: sessionID,
	}
	switch ev.Type {
	case "run_created":
		// The outer runner already emitted this; swallow the loop's
		// emit so duplicates don't reach the UI.
		return nil
	case "assistant", "thinking":
		out.Content = ev.Content
	case "tool_use":
		out.ToolName = ev.ToolName
		if ev.ToolInput != nil {
			out.ToolInput = ev.ToolInput
		}
		out.Content = ev.ToolUseID
		out.ToolUseID = ev.ToolUseID
	case "tool_result":
		out.ToolName = ev.ToolName
		out.Content = ev.Content
		out.ToolUseID = ev.ToolUseID
		out.ToolSucceeded = ev.ToolSucceeded
		out.IsError = ev.Failed
	case "authority_decision":
		out.ToolName = ev.ToolName
		out.ToolInput = ev.ToolInput
		out.Content = ev.Content
		out.ToolUseID = ev.ToolUseID
	case "permission_request":
		out.ToolName = ev.ToolName
		out.ToolUseID = ev.ToolUseID
		out.Content = ev.Content
		if ev.ToolInput != nil {
			out.ToolInput = ev.ToolInput
		}
		// Forward the tool's advertised options verbatim so MCP-style
		// prompts keep their "Allow once / Allow for this chat / Always
		// allow this server / Reject" choices. Fall back to the legacy
		// two-button shape only when the tool didn't supply any — that's
		// the case for built-in file/bash tools that just want a yes/no.
		if len(ev.PermissionOptions) > 0 {
			converted := make([]PermissionOption, 0, len(ev.PermissionOptions))
			for _, opt := range ev.PermissionOptions {
				converted = append(converted, PermissionOption{
					ID:   opt.ID,
					Name: opt.Name,
					Kind: opt.Kind,
				})
			}
			out.PermissionOptions = converted
		} else {
			out.PermissionOptions = []PermissionOption{
				{ID: "acceptEdits", Name: "Accept", Kind: "allow_once"},
				{ID: "reject", Name: "Reject", Kind: "reject_once"},
			}
		}
	case "plan_complete":
		out.Content = ev.Content
	case "done":
		if ev.Usage != nil {
			out.Usage = &Usage{
				InputTokens:              ev.Usage.InputTokens,
				OutputTokens:             ev.Usage.OutputTokens,
				CacheReadInputTokens:     ev.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: ev.Usage.CacheCreationInputTokens,
			}
		}
	case "compact_start", "compact_end", "compact_error":
		// Compaction lifecycle. Content carries the status text on
		// start (e.g. "Compacting conversation history...") and the
		// summary on end; Error explains a fallback on _error.
		out.Content = ev.Content
		out.Error = ev.Error
	case "error":
		out.Error = ev.Error
		out.Status = ev.Status
	default:
		// Unknown event types are forwarded as-is so future additions
		// don't silently drop on the frontend.
	}
	return out
}

// newModelRouteObserver returns a native.ModelRouteObserver that calls emit
// only when the served route differs from the last one reported for the run.
// A run that stays on one account reports it once. A failover to another
// account during the run reports the new account.
func newModelRouteObserver(emit func(provider, servedModel string)) native.ModelRouteObserver {
	var mu sync.Mutex
	var lastProvider, lastModel string
	return func(provider, servedModel string) {
		mu.Lock()
		changed := provider != lastProvider || servedModel != lastModel
		lastProvider, lastModel = provider, servedModel
		mu.Unlock()
		if changed {
			emit(provider, servedModel)
		}
	}
}

// CancelRun cancels the context driving the named run.
func (r *NativeAgentRunner) CancelRun(runID string) error {
	r.mu.Lock()
	cancel, ok := r.runs[runID]
	delete(r.runs, runID)
	r.mu.Unlock()
	if !ok {
		return nil
	}
	cancel()
	return nil
}

// RespondToPermission routes the user's choice to the loop's pending
// permission.
func (r *NativeAgentRunner) RespondToPermission(sessionID, optionID string) error {
	return r.perm.Respond(sessionID, optionID)
}

// CancelPermission aborts a pending permission without stopping the run.
func (r *NativeAgentRunner) CancelPermission(sessionID string) {
	r.perm.Cancel(sessionID)
}

// Shutdown cancels everything and frees resources.
func (r *NativeAgentRunner) Shutdown() error {
	startedAt := time.Now()
	log.Printf("[native-shutdown] starting")

	lockStartedAt := time.Now()
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.runs))
	for _, c := range r.runs {
		cancels = append(cancels, c)
	}
	r.runs = make(map[string]context.CancelFunc)
	r.ready = false
	r.mu.Unlock()
	log.Printf("[native-shutdown] runner lock released: runs=%d lock_wait=%s", len(cancels), time.Since(lockStartedAt))

	for _, c := range cancels {
		c()
	}
	log.Printf("[native-shutdown] run contexts cancelled: count=%d elapsed=%s", len(cancels), time.Since(startedAt))

	log.Printf("[native-shutdown] stopping background shells")
	r.bash.KillAll()
	log.Printf("[native-shutdown] completed in %s", time.Since(startedAt))
	return nil
}

// registryForSession clones the base registry and adds per-session
// tools (TodoWrite, ExitPlanMode) bound to this session's state.
func (r *NativeAgentRunner) registryForSession(plan *tools.PlanModeState, todo *tools.TodoList, allowed []string) *tools.Registry {
	scoped := tools.NewRegistry()
	for _, t := range r.registry.List(nil) {
		scoped.Register(t)
	}
	scoped.Register(tools.NewTodoWriteTool(todo))
	scoped.Register(tools.NewExitPlanModeTool(plan))
	if shouldRegisterProgrammaticTool(allowed) {
		scoped.Register(tools.NewProgrammaticTool(scoped, allowedProgrammaticHelpers(allowed)))
	}
	return scoped
}

// addExtraTools registers a task's own tools on its scoped registry. A nil
// tool or one whose name is taken is skipped and logged, so a task's tools
// never replace a built-in one.
func addExtraTools(scoped *tools.Registry, extra []tools.Tool) {
	for _, t := range extra {
		if t == nil {
			continue
		}
		if scoped.Get(t.Name()) != nil {
			log.Printf("[native] extra tool %s skipped: the name is taken", t.Name())
			continue
		}
		scoped.Register(t)
	}
}

func shouldRegisterProgrammaticTool(allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, name := range allowed {
		if name == "Programmatic" {
			return true
		}
	}
	return false
}

func allowedProgrammaticHelpers(allowed []string) []string {
	helperNames := []string{"Glob", "Grep", "Read", "Bash", "Write", "WebFetch", "OpenCLI"}
	if len(allowed) == 0 {
		return helperNames
	}
	allowedSet := map[string]bool{}
	for _, name := range allowed {
		allowedSet[name] = true
	}
	out := make([]string, 0, len(helperNames))
	for _, name := range helperNames {
		if name == "OpenCLI" {
			if allowedSet["Bash"] {
				out = append(out, name)
			}
			continue
		}
		if allowedSet[name] {
			out = append(out, name)
		}
	}
	return out
}

// MarshalEventInput is a helper for tests / external call sites that
// need to encode a tool input map as JSON.
func MarshalEventInput(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
