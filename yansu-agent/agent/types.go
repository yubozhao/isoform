package agent

import (
	"context"

	"yansu-agent/agent/authority"
	"yansu-agent/agent/native"
	"yansu-agent/agent/tools"
)

// StreamEvent represents a streaming event from task execution.
// The frontend hooks (useAgentStreaming) consume this shape directly.
type StreamEvent struct {
	Type      string `json:"type"`
	RunID     string `json:"run_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Content   string `json:"content,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolInput any    `json:"tool_input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	// IsError marks a tool_result whose tool failed: it errored, was
	// refused, timed out or exited non-zero (native runner only).
	IsError           bool               `json:"is_error,omitempty"`
	AvailableCommands []AvailableCommand `json:"available_commands,omitempty"`
	Error             string             `json:"error,omitempty"`
	// Status is the provider's HTTP status on an error event, when the
	// provider answered. Callers classify causes from this number, never
	// from the words in Error.
	Status            int                `json:"status,omitempty"`
	PermissionOptions []PermissionOption `json:"permission_options,omitempty"`
	// Usage reports token counts and cache stats per turn when available.
	Usage *Usage `json:"usage,omitempty"`
	// Backend names the runner that owns the run (native, claude-code or codex).
	// Routing metadata is emitted before each attempt and on run_created.
	Backend           string   `json:"backend,omitempty"`
	CandidateBackends []string `json:"candidate_backends,omitempty"`
	FallbackFrom      []string `json:"fallback_from,omitempty"`
	BackendReason     string   `json:"backend_reason,omitempty"`
	// Blocked prevents automatic replay after an action or unknown outcome.
	Blocked bool `json:"blocked,omitempty"`
	// ActionsStarted comes from the durable server ledger, including calls
	// that returned an error. Finalizers must not replay work after an action.
	ActionsStarted int `json:"actions_started,omitempty"`
	// Provider and ServedModel are the upstream account and model that
	// served the run. Set on model_route, which the native runner emits when
	// the gateway reports X-Magpie-Provider and X-Magpie-Model.
	Provider    string `json:"provider,omitempty"`
	ServedModel string `json:"served_model,omitempty"`
	// ToolSucceeded is set on a native tool_result only when the tool ran
	// and returned without an error. Other runners never set it.
	ToolSucceeded bool `json:"tool_succeeded,omitempty"`
}

// Backend hints classify a task for the routing runner. An empty hint is an
// ordinary task that routes by the remaining rules.
const (
	BackendHintChat        = "chat"
	BackendHintHandoff     = "handoff"
	BackendHintCron        = "cron"
	BackendHintInitiative  = "initiative"
	BackendHintEphemeral   = "ephemeral"
	BackendHintComputerUse = "computer_use"
	// BackendHintLearning marks background learning calls (shadow task
	// evaluation, Purpose extraction, Dream memory merge). Their prompts
	// carry observed work and memory contents, so the routing runner keeps
	// them on the native runner no matter which assistant the user picked.
	BackendHintLearning = "learning"
)

// Task priorities select the model route. PriorityInteractive is the zero
// value, so existing callers keep the foreground route. PriorityBackground
// resolves models through NativeRunnerConfig.BackgroundModelAliases first.
const (
	PriorityInteractive = ""
	PriorityBackground  = "background"
)

// AvailableCommand describes a slash command surfaced to the user.
type AvailableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputHint   string `json:"input_hint,omitempty"`
}

// PermissionOption describes a selectable option in a permission request.
type PermissionOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // "allow_once", "allow_always", "reject_once", "reject_always"
}

// Usage mirrors Anthropic's message-level usage field.
type Usage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// StreamCallback is called for each streaming event during task execution.
type StreamCallback func(event *StreamEvent) error

// ImageAttachment represents an image to attach to the task prompt.
type ImageAttachment struct {
	MediaType  string // MIME type, e.g. "image/png", "image/jpeg"
	DataBase64 string // Base64-encoded image data
}

// ExternalWriteDenyPrefixes are the bash command prefixes that unattended
// sessions (TaskInput.DenyExternalWrites or Ephemeral) must never execute.
// They cover the externally-visible write operations that spend the user's
// identity or publish state outside the machine: pushing commits, creating/
// approving/merging PRs, and arbitrary GitHub API / release mutations.
// Enforced by both runners: the native loop denies matching Bash calls in
// executeToolUse; the external Claude Code runner translates them into
// --disallowedTools rules (deny rules hold even under bypassPermissions).
var ExternalWriteDenyPrefixes = []string{"git push", "gh pr", "gh api", "gh release"}

// deniedBashPrefixes returns the deny list that applies to input, or nil.
func deniedBashPrefixes(input *TaskInput) []string {
	if input.DenyExternalWrites || input.Ephemeral {
		return ExternalWriteDenyPrefixes
	}
	return nil
}

// TaskInput represents input for executing a task with the Anthropic-native loop.
type TaskInput struct {
	Title       string
	Description string
	Prompt      string
	WorkDir     string
	Model       string
	// MaxTotalTokens caps aggregate input, cache, and output tokens reported
	// across the native loop. Zero keeps the runner default. The native loop
	// also clamps each response's output allowance to the remaining budget and
	// stops before dispatching tools after an over-budget model turn.
	MaxTotalTokens int
	SessionID      string
	// HistoryKey is the persistence key the configured HistoryLoader uses
	// to fetch prior conversation items. It's decoupled from SessionID
	// because the caller's persistence primary key (e.g. wails-gui's
	// sessionLocalID / tag) often differs from the transient SessionID
	// the native loop propagates to the UI. Empty = no history lookup.
	HistoryKey string
	// CredentialSessionID binds scheduler ownership without loading history
	// or resuming a stateless automation's model conversation.
	CredentialSessionID string `json:"-"`
	Env                 []string
	Images              []ImageAttachment
	PlanMode            bool
	ThinkingEnabled     bool
	// History is a pre-formatted prior conversation summary injected as context
	// when the loop cannot resume from stream_items (or the caller wants extra).
	History string
	// Ephemeral marks this as a one-shot session that should not be persisted
	// to stream_items (memory generator probes, activity triage, etc).
	Ephemeral bool
	// DenyExternalWrites hard-blocks externally-visible write commands
	// (see ExternalWriteDenyPrefixes) at the runner layer, regardless of
	// permission mode or AutoApproveTools. Set it for every unattended
	// entry point where nobody reviews actions before they take effect:
	// IM relay sessions, cron/automation runs. Ephemeral sessions are
	// denied automatically — background observers (triage, scanners,
	// generators) must never publish anything on the user's behalf.
	DenyExternalWrites bool
	// PromptFunc, when non-nil, is awaited before the first model call and may
	// overlap with session bootstrap. Its returned values replace Prompt/History.
	PromptFunc func() (prompt string, history string) `json:"-"`
	// ExternalPromptFunc opts into route-specific composition. The router resolves
	// only the selected tier, and keeps native fallback's PromptFunc separate.
	ExternalPromptFunc func() (prompt string, history string) `json:"-"`
	// ExternalExtraTools accompanies that tier only; the native task is unchanged.
	ExternalExtraTools []tools.Tool `json:"-"`
	// BeforeDispatch rechecks read authority before native model requests,
	// including initialization delays, fallback and HTTP retries. External
	// runners can check only before process launch; source-bound callers must
	// also set RequireNativeDispatch. Nil preserves existing behavior. The hook
	// must not modify the task or include private text in its refusal error.
	BeforeDispatch func(context.Context) error `json:"-"`
	// RequireNativeDispatch keeps source-bound context on transports that can
	// recheck BeforeDispatch before every model request. External CLIs cannot
	// enforce it after startup. This restriction overrides backend preferences
	// and applies to the whole attempt, including retries and fallback.
	RequireNativeDispatch bool `json:"-"`
	// AllowedTools restricts the tools available to the model for this task.
	// Empty means "all registered tools".
	AllowedTools []string
	// ExtraTools adds tools to this task's registry only, beside the
	// runner's own; no other run sees them. The desktop gives its main chat
	// the Upcoming tools this way. A tool whose name is already registered
	// is skipped, so an extra tool can never replace a built-in one. External
	// runners expose them through the per-run gated MCP server; unsupported
	// runners fall back to the native loop for the turn.
	ExtraTools []tools.Tool `json:"-"`
	// DisabledTools removes the named tools from the available list
	// after AllowedTools filtering. Use it to drop a specific tool the
	// model misuses in a given flow without enumerating everything else
	// (e.g. drop TodoWrite in tool-using generation, where the model
	// treats "I planned" as "I'm done" and stops calling Write).
	DisabledTools []string
	// NoTools disables the tool list entirely for this task. Use for
	// pure-text generations (e.g. suggestion chips, classification,
	// JSON extraction) where sending tool definitions is wasteful
	// token noise and can distract small/cheap models.
	NoTools bool
	// ProgrammaticToolCalling enables Anthropic code_execution-mediated
	// calls for selected client tools. This is intended for bounded
	// multi-call workflows where the model can loop/filter locally
	// instead of sampling a new assistant turn for each tool call.
	ProgrammaticToolCalling bool
	// SystemText overrides the runner's default system prompt for this
	// task. Empty keeps the runner default. Set this for focused tasks
	// where the prompt itself is fully self-contained and the generic
	// "you are a coding assistant" wrapper would conflict.
	SystemText string
	// NoSystem suppresses the system message entirely for this task —
	// stronger than SystemText="". Use when the prompt alone is
	// sufficient (small cheap models doing one-shot JSON output).
	NoSystem bool
	// RecoveryNudgeText overrides the default silent-turn recovery
	// prompt for this task. Set this for tasks where the work IS tool
	// calls (tool-using generation, iteration) — the default nudge tells
	// the model to stop calling tools and summarise, which causes it to
	// abandon the work and report "I was interrupted before writing
	// any files". Empty keeps the default conversational nudge.
	RecoveryNudgeText string
	// MaxNudges caps the number of recovery nudges sent after silent
	// turns before the loop returns ErrSilentTurn. 0 falls back to the
	// default (2). Long tool-using flows where silent-turn is
	// recoverable (e.g. tool-using generation) can raise this so a
	// transient model stall doesn't doom the whole task.
	MaxNudges int
	// TraceID is the cross-process correlation id for this LLM call.
	// Generated by the UI / caller (typically crypto.randomUUID at the
	// moment of user send), propagated through the agent loop and
	// stamped onto outbound HTTP requests as the x-yansu-trace-id
	// header. Empty = no correlation propagation; the call still works,
	// telemetry events just won't carry a trace_id field.
	TraceID string
	// SuppressTelemetry marks this run as a background / internal LLM
	// invocation that should NOT emit agentloop.* events. Background
	// generators, ephemeral JSON-extraction probes, and similar
	// internal uses of the runner set this so they don't pollute the
	// PostHog agentloop dashboards with runs the user never initiated.
	// When true, the runner swaps in native.NoOpTelemetry and skips
	// stamping x-yansu-trace-id on outbound HTTP requests, so neither
	// the wails-gui loop nor the backend sub2api proxy emits
	// agentloop.* events for this run.
	SuppressTelemetry bool

	// BackendHint classifies the task for RoutingAgentRunner (see the
	// BackendHint constants). Empty means no hint.
	BackendHint string
	// Priority selects the model route (see the Priority constants).
	// PriorityBackground resolves models through
	// NativeRunnerConfig.BackgroundModelAliases first.
	Priority string
	// ForceBackend asks RoutingAgentRunner to run a chat turn on a specific
	// backend. "external" runs the turn on the signed-in CLI when one is
	// available. Empty or any other value keeps the default route. Set it only
	// for an explicit per-turn request from the user.
	ForceBackend string

	// AutoApproveTools marks this run as fully unattended (cron jobs,
	// automations, background scheduled tasks) — there is no human in
	// front of the UI who can answer a permission prompt. When true,
	// the loop short-circuits every tool's NeedsPermission check and
	// proceeds as if the user had clicked "Allow once": no
	// permission_request event is emitted, no Permission.Await is
	// performed, and no session/persistent trust is recorded. This
	// closes a race between the loop emitting the event and any async
	// RespondToPermission caller — the loop simply never blocks on
	// permission for these runs.
	AutoApproveTools bool

	// AskBeforeRiskyCommands makes an attended chat ask before Bash commands
	// that are not plain reads or workspace writes (external sends, privileged
	// or unclassifiable commands). Off (the default), an attended chat runs
	// every Bash command without asking, as the app did before the guard work.
	// The hard blocks (Isoform's data folder, private keys, credential stores,
	// guard.RiskBlocked, keychain dumps) refuse regardless, and unattended runs
	// ignore this flag: they always fail closed and keep the deny list.
	AskBeforeRiskyCommands bool

	// AuthorityInitiativeID links policy decisions to the initiative whose
	// approved execution plan started this run. Empty means no initiative.
	AuthorityInitiativeID string `json:"-"`
	// AuthorityGrants are explicit, caller-provided capability scopes for this
	// run. They are never derived from model confidence or memory.
	AuthorityGrants []authority.Grant `json:"-"`
	// AuthorityEvaluator optionally supplies a live evaluator. Initiative runs
	// use it to recheck an append-only operating envelope immediately before
	// each tool dispatch so revocation does not wait for the next run.
	AuthorityEvaluator authority.Evaluator `json:"-"`
	// ExecutionMode selects the deterministic runtime policy overlay. Shadow
	// mode permits read-only tools and denies every write, privileged,
	// unknown, or opaque side effect before grants and legacy permissions.
	ExecutionMode authority.ExecutionMode `json:"-"`
	// AuthorityShadowRunID links every policy decision from a shadow execution
	// back to the immutable prediction that produced the candidate plan.
	AuthorityShadowRunID string `json:"-"`

	// ContextEngineCollector, when set, is invoked at each step.llm.*
	// emission boundary to assemble a snapshot of the structural
	// context engine state — system prompt length, message counts by
	// role, tool names, user memory + knowledge counters, and (at the
	// detailed tier) tool schemas + system prompt template name. The
	// runner threads it into native.LoopConfig.ContextEngineCollector.
	// Nil = no CE fields emitted (L0 / off tier behaviour).
	//
	// The collector is expected to honour the user's privacy tier
	// (off / basic / detailed) — yansu-agent emits whatever the
	// snapshot contains.
	ContextEngineCollector native.ContextEngineCollector `json:"-"`

	// Redactor is retained for caller compatibility and threaded into the
	// loop. Upstream response bodies are never exported to telemetry, even
	// after redaction, so the loop no longer calls it for error bodies.
	Redactor func(string) string `json:"-"`

	// Caller names the code path asking for this run, for the routing log
	// and cost attribution. Code identifiers only, never content. Empty means
	// CallerLabel derives it from the stack.
	Caller string
}

var ErrTokenBudgetExceeded = native.ErrTokenBudgetExceeded
