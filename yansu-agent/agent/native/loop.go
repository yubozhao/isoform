package native

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"yansu-agent/agent/authority"
	"yansu-agent/agent/tools"
)

// synthToolUseID generates a fallback id for tool_use blocks whose
// upstream id field arrived empty. Pattern matches Anthropic's
// `toolu_<base62>` shape so downstream sub2api / OpenAI converters
// treat it as a normal id rather than a special-case marker. The
// `synth` infix lets dashboards filter for proxy-bug-induced ids when
// triaging future regressions.
//
// Why we synthesize rather than drop the block: dropping discards the
// model's tool-call intent, and on a stack where the proxy emits empty
// ids deterministically the next turn would just produce another
// empty-id block — turning the bug into an infinite silent-turn loop.
// A synthesized id is valid Anthropic-side, gets persisted into the
// matching tool_result via tu.ID, and sub2api will accept it because
// the same id round-trips through its converter.
func synthToolUseID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Failure here is impossible on a healthy machine (crypto/rand
		// reads /dev/urandom / BCryptGenRandom). Fall back to a time-
		// based id so we never return "" — that's the whole point.
		return fmt.Sprintf("toolu_synth_%d", time.Now().UnixNano())
	}
	return "toolu_synth_" + hex.EncodeToString(b[:])
}

// emitError emits a LoopEvent of type "error" with the trace id stamped
// both structurally (TraceID field) AND appended to the visible Error
// string as `[trace=<id>]`. The visible suffix matters because most
// downstream consumers (wails-gui error toast, generator logs,
// cron_exec lastError) only render the Error string — without the
// suffix an operator triaging a user-reported failure has no way to
// jump from the visible message to the matching PostHog event. When
// cfg.TraceID is empty (legacy callers that don't set it) the suffix
// is omitted so we don't emit `[trace=]` noise.
func emitError(cfg *LoopConfig, msg string) {
	if cfg.TraceID != "" {
		msg = msg + " [trace=" + cfg.TraceID + "]"
	}
	cfg.Emit(LoopEvent{Type: "error", Error: msg, TraceID: cfg.TraceID})
}

// emitErrorFrom emits err like emitError and carries the provider's HTTP
// status when err wraps an *HTTPError.
func emitErrorFrom(cfg *LoopConfig, msg string, err error) {
	if cfg.TraceID != "" {
		msg = msg + " [trace=" + cfg.TraceID + "]"
	}
	cfg.Emit(LoopEvent{Type: "error", Error: msg, TraceID: cfg.TraceID, Status: httpStatusOf(err)})
}

// httpStatusOf returns the HTTP status err carries, or 0.
func httpStatusOf(err error) int {
	var he *HTTPError
	if errors.As(err, &he) && he != nil {
		return he.Status
	}
	return 0
}

var ErrTokenBudgetExceeded = errors.New("native agent total token budget exceeded")

const requestInputTokenSafetyOverhead = 256

func addUsage(total *Usage, current *Usage) {
	if total == nil || current == nil {
		return
	}
	total.InputTokens += current.InputTokens
	total.OutputTokens += current.OutputTokens
	total.CacheReadInputTokens += current.CacheReadInputTokens
	total.CacheCreationInputTokens += current.CacheCreationInputTokens
}

func copyUsage(usage Usage) *Usage {
	copyValue := usage
	return &copyValue
}

// requestInputTokenUpperBound reserves one token per encoded request byte plus
// fixed provider framing. BPE tokenizers cannot emit more content tokens than
// the UTF-8 bytes they consume, so this is intentionally conservative and lets
// bounded unattended runs allocate output without overshooting on input that is
// only reported after the response arrives.
func requestInputTokenUpperBound(request *Request) (int, error) {
	if request == nil {
		return 0, errors.New("cannot estimate a nil native agent request")
	}
	copyRequest := *request
	copyRequest.MaxTokens = 0
	raw, err := json.Marshal(copyRequest)
	if err != nil {
		return 0, fmt.Errorf("encode native agent request for token budget: %w", err)
	}
	if len(raw) > int(^uint(0)>>1)-requestInputTokenSafetyOverhead {
		return 0, ErrTokenBudgetExceeded
	}
	return len(raw) + requestInputTokenSafetyOverhead, nil
}

const (
	// MaxTurns caps how many tool-use/tool-result loops a single task
	// may run before we abort. Acts as a backstop only — the per-tool
	// guards (toolErrorStreakLimit, the loop-guard soft/hard thresholds
	// in loop_guard.go) and MaxWallTime catch real runaway patterns long
	// before this fires. 100 was chosen because the previous value of 50
	// was truncating legitimately progressing research tasks mid-handoff
	// (distinct queries each turn, not stuck looping); 100 matches where
	// mainstream long-horizon agents have landed.
	MaxTurns = 100
	// MaxWallTime caps total per-task wall time.
	MaxWallTime = 60 * time.Minute
	// MaxOutputTokens is the per-turn max output budget.
	MaxOutputTokens = 8192
	// toolErrorStreakLimit is the default number of consecutive is_error
	// tool_results from the same tool that triggers a stop-nudge to the
	// model. 5 tolerates the occasional flake (rate limit, single dead link)
	// but cuts off a fabricate-and-fetch loop well before MaxTurns. Tools
	// with deterministic argument-shape failures can override this via
	// toolErrorStreakLimitForTool.
	toolErrorStreakLimit = 5
	// maxToolsUsedTracked caps the per-run telemetry tools_used array
	// so a runaway tool-spam loop can't blow up the PostHog event
	// payload size (1MB hard limit). 256 is roughly 2x the worst-case
	// well-behaved run; anything past that the run is already in
	// degraded territory and the cap is the least of its problems.
	// tool_failures_by_name has no cap (small per-name keyed map).
	maxToolsUsedTracked = 256

	// toolResultMaxBytes caps a single tool_result's text content.
	// Results above this are head+tail truncated before entering
	// messages. 32 KB is ~8k tokens — large enough for useful
	// WebSearch / WebFetch payloads, small enough that 4 results in
	// one turn stay well within Sonnet's 200k context.
	toolResultMaxBytes = 32 * 1024
	// toolResultKeptBytes is the total bytes retained after
	// truncation (head + tail). The split between head and tail
	// depends on the tool type — see truncationSplit.
	toolResultKeptBytes = 28 * 1024
)

// ErrSilentTurn is returned when the model repeatedly ends turns with no
// visible text and no tool calls after recovery nudges. The loop also emits an
// error event before returning this sentinel so UI callers can render the
// failure without relying solely on the Go return value.
var ErrSilentTurn = errors.New("agent ended without producing a response even after a recovery prompt; try rephrasing the question")

// ErrLoopGuardStuck is returned when the model ignores a hard loop-guard
// stop nudge and keeps issuing the same blocked tool pattern. This should
// fire before MaxTurns; MaxTurns is only the last-resort backstop.
var ErrLoopGuardStuck = errors.New("agent stopped after repeated tool-call loop")

// LoopConfig holds everything the loop needs besides the per-task input.
type LoopConfig struct {
	Client     LLMClient
	Registry   *tools.Registry
	History    HistoryLoader
	Permission *PermissionManager
	// SessionTrust caches "Allow for this chat" grants in-memory so the
	// loop can short-circuit subsequent permission_request emissions for
	// the same tool prefix in the same session. Nil disables session
	// trust (the loop always asks the Permission manager).
	SessionTrust *SessionTrust
	// OnPersistentAllow is called when the user picks "Always allow this
	// server" on a permission prompt. The prefix is the
	// PermissionPrompt.Prefix the tool advertised (e.g. "mcp__slack__").
	// wails-gui uses this to persist the trust to disk and refresh the
	// underlying MCP adapter so subsequent calls skip the prompt entirely
	// at the adapter layer (no event round-trip). Nil = drop the signal.
	OnPersistentAllow func(prefix string)
	// AutoApproveTools marks an unattended run, where there is no UI to
	// answer permission prompts. The configured authority evaluator still
	// runs first; deny and require_approval decisions block execution.
	// Only an allowed action skips its legacy prompt, without emitting
	// permission_request, calling Permission.Await or persisting trust.
	AutoApproveTools bool
	// AskBeforeRiskyCommands, when true, lets an attended run surface a
	// permission prompt for a Bash command the authority policy flags as
	// external, privileged or unclassifiable. When false (the default), an
	// attended run never prompts for Bash: a require-approval decision on the
	// Bash tool is downgraded to allow. Hard blocks (CheckProtectedPaths,
	// guard.RiskBlocked) still refuse, and unattended runs ignore this flag.
	AskBeforeRiskyCommands bool
	// AuthorityEvaluator decides whether normalized tool capabilities may run.
	// It is independent of prediction confidence and executes before legacy
	// per-tool permission checks.
	AuthorityEvaluator authority.Evaluator
	AuthorityContext   authority.Context
	AuthorityRecorder  func(authority.Evaluation)
	// BeforeToolExecution durably records an authorized dispatch before the
	// registry runs it. A recording failure prevents execution. Nil preserves
	// the native loop's existing behavior.
	BeforeToolExecution func(name string, call tools.ToolCall) error
	SystemText          string // custom system prompt (empty = default)

	// Emit delivers events back to the caller (typically the
	// wails-gui streaming bridge). It is called from the loop
	// goroutine, so the implementation must be thread-safe and
	// quick — buffering should happen on the caller side.
	Emit func(LoopEvent)

	// RunID uniquely identifies this task (stamped on every emitted
	// event).
	RunID string
	// TraceID is the cross-process correlation id for this LLM call.
	// Generated by the UI / caller, propagated onto outbound HTTP
	// requests via the x-yansu-trace-id header so the backend / proxy
	// layer can stitch the same trace together in Loki / PostHog.
	// Empty = no propagation (the loop still works, telemetry events
	// just won't have a trace_id field).
	TraceID string
	// Telemetry is the event-capture hook the loop calls at key
	// observability points (loop start/end, tool-pairing detection,
	// retry recovery, terminal errors). Nil falls back to NoOpTelemetry
	// so the loop body can call cfg.Telemetry.Capture unconditionally.
	Telemetry Telemetry
	// SessionID for permission requests (usually matches session tag).
	SessionID string
	// HistoryKey is the lookup key passed to HistoryLoader. Empty =
	// fall back to SessionID for backwards compatibility.
	HistoryKey string
	// WorkDir is the absolute cwd for tool execution.
	WorkDir string

	// ThinkingEnabled toggles extended-thinking mode.
	ThinkingEnabled bool
	ModelEffort     string
	// PlanMode restricts the tool set to read-only + TodoWrite +
	// ExitPlanMode and prepends the plan-mode system prompt.
	PlanMode *tools.PlanModeState
	// AllowedTools restricts the registry's list to this set when
	// non-empty.
	AllowedTools []string
	// DisabledTools removes the named tools from the available list
	// after AllowedTools / plan-mode filtering. Use it to drop a
	// specific tool the model misuses in a given flow without having to
	// enumerate everything else (e.g. drop TodoWrite in a tool-using
	// generation flow, where the model treats "I planned" as "I'm done").
	DisabledTools []string
	// DeniedBashPrefixes hard-blocks Bash commands whose normalized text
	// starts with any of these prefixes (checked per && / ; / | segment).
	// Unlike the permission gate, this cannot be bypassed by
	// AutoApproveTools or session trust — it is the unattended-session
	// backstop for externally-visible writes (git push, gh pr, ...).
	DeniedBashPrefixes []string
	// NoTools suppresses the tool list entirely (pure-text tasks).
	NoTools bool
	// ProgrammaticToolCalling exposes selected client tools to the
	// Anthropic code_execution container. The loop must then preserve
	// server_tool_use/code_execution_tool_result blocks and respond to
	// programmatic tool_use blocks with tool_result-only user messages.
	ProgrammaticToolCalling bool
	// NoSystem suppresses the system message entirely. The prompt
	// alone is sent, which is what focused small-model calls want.
	NoSystem bool

	// ModelID is the Anthropic model id.
	ModelID string
	// MaxTokens caps output per turn.
	MaxTokens int
	// MaxTotalTokens caps aggregate input, cache, and output usage across all
	// turns. Zero disables the aggregate cap. A response that crosses the cap
	// is retained for accounting, but its tool calls are not dispatched.
	MaxTotalTokens int

	// RecoveryNudgeText is the user message appended after a silent
	// turn to break the model out of a thinking-only loop. Empty falls
	// back to the conversational default. Tasks where calling tools IS
	// the work (e.g. tool-using generation) should override this with
	// "keep working with tools" phrasing — the default tells the model
	// to stop and summarise, which makes it abandon the work.
	RecoveryNudgeText string

	// MaxNudges caps the number of recovery nudges sent after silent
	// turns before the loop gives up and returns ErrSilentTurn. 0 falls
	// back to the default (2). Long tool-using flows where silent-turn
	// is recoverable (e.g. tool-using generation) can raise this so a
	// transient model stall doesn't doom the whole task.
	MaxNudges int

	// Prompt is the initial user message text.
	Prompt string
	// Images is optional image attachments on the user message.
	Images []ImageSource
	// PriorHistory is pre-loaded history; overrides HistoryLoader.
	PriorHistory []HistoryItem

	// ContextEngineCollector, when set, is invoked at each step.llm.* event
	// boundary to assemble a ContextEngineSnapshot — structural metrics
	// (L1) and tool schemas (L2) — that the
	// telemetry layer attaches to the event properties. Returning nil
	// suppresses CE fields for that step. The collector is expected to
	// be cheap (snapshot, not deep copy) since it runs on the loop
	// goroutine. Nil = no CE fields (L0 behaviour, default).
	//
	// Privacy: this is the L1+L2 plumbing referenced in
	// tmp/yansu-monitoring-impl-plan.md sections 4.4 and 11. The
	// collector caller is responsible for honoring the user's privacy
	// tier setting — yansu-agent emits whatever the snapshot contains.
	ContextEngineCollector ContextEngineCollector

	// Redactor is retained for caller compatibility. Upstream response bodies
	// are never sent to telemetry, even after redaction, so the loop does not
	// pass them through this callback.
	Redactor func(string) string
}

// LoopEvent is what the loop emits.
type LoopEvent struct {
	Type      string // run_created | assistant | thinking | tool_use_start | tool_use | tool_result | permission_request | done | error | plan_complete | compact_start | compact_end | compact_error
	Content   string
	ToolName  string
	ToolInput any
	ToolUseID string
	Error     string
	// TraceID is the run's cross-process correlation id, populated
	// only on error events. Lets consumers (UI toast, generator
	// log, cron_exec lastError) attach the trace id structurally
	// without parsing it back out of the Error string. The Error
	// string itself ALSO carries `[trace=<id>]` as a suffix so logs
	// scraped without the structured field still let an operator
	// jump straight to the PostHog event.
	TraceID string
	// Status is the provider's HTTP status on an error event, when the
	// error carries one (*HTTPError).
	Status int
	Usage  *Usage
	// PermissionOptions carries the tool-advertised choices on a
	// permission_request event. The runner forwards these verbatim to
	// the UI so MCP-style "Allow once / Allow for this chat / Always
	// allow this server / Reject" are preserved end-to-end.
	PermissionOptions []tools.PermissionOption
	// PermissionPrefix is the tool's PermissionPrompt.Prefix, echoed on
	// permission_request so downstream consumers know which prefix a
	// later "always allow" answer should attach to.
	PermissionPrefix string
	// ToolSucceeded is set only on the tool_result of a tool that passed
	// every gate, ran, and returned without an error. Denied, blocked,
	// skipped and failed calls leave it false.
	ToolSucceeded bool
	// Failed marks a tool_result whose tool did not do its work: it
	// errored, was refused or skipped, timed out, or exited non-zero.
	Failed bool
}

// toolResultFailed reports a tool result that marks a failure: is_error,
// or a timeout or a non-zero exit code in its metadata (Bash).
func toolResultFailed(result tools.ToolResult) bool {
	if result.IsError {
		return true
	}
	if timedOut, _ := result.Metadata["timed_out"].(bool); timedOut {
		return true
	}
	if code, ok := result.Metadata["exit_code"].(int); ok && code != 0 {
		return true
	}
	return false
}

// Run drives one task through the loop. It blocks until the task ends
// (normal stop, error, max turns, or ctx cancellation).
func Run(ctx context.Context, cfg *LoopConfig) (runErr error) {
	if cfg.Emit == nil {
		return fmt.Errorf("LoopConfig.Emit is required")
	}
	if cfg.Client == nil {
		emitError(cfg, "agent is not configured (missing Anthropic client)")
		return fmt.Errorf("client required")
	}
	if cfg.Telemetry == nil {
		cfg.Telemetry = NoOpTelemetry{}
	}

	ctx, cancel := context.WithTimeout(ctx, MaxWallTime)
	defer cancel()
	// Stamp the trace id onto the request context so the HTTP layer
	// (doStream) can set x-yansu-trace-id on every outbound request
	// without LoopConfig having to leak into the anthropic Client.
	ctx = ContextWithTraceID(ctx, cfg.TraceID)
	// Stamp the run id too so doStream can set x-yansu-run-id on every
	// outbound request — the backend conversation capture (plan §12)
	// joins captured blobs to a trace by (trace_id, run_id, step_index).
	ctx = ContextWithRunID(ctx, cfg.RunID)
	// Internal probes may send facts that must not survive in local logs.
	// Keep the original errors for recovery and callers, but log only their
	// stable classification when the run suppresses agentloop capture.
	errorDiagnostic := func(err error) any {
		if err != nil && SuppressAgentloopFromContext(ctx) {
			return classifyStepError(err)
		}
		return err
	}

	runStart := time.Now()
	cfg.Telemetry.Capture("agentloop.run.started", map[string]any{
		"trace_id":         cfg.TraceID,
		"run_id":           cfg.RunID,
		"session_id":       cfg.SessionID,
		"model":            cfg.ModelID,
		"plan_mode":        cfg.PlanMode != nil && cfg.PlanMode.Active(),
		"thinking_enabled": cfg.ThinkingEnabled,
		"no_tools":         cfg.NoTools,
		"images_count":     len(cfg.Images),
	})

	// Per-run telemetry accumulators surfaced in the completed/error
	// epilogue. toolsUsed is the chronological list of tool names the
	// model invoked across all turns (duplicates kept on purpose so we
	// can see retries / repeats). toolFailuresByName cumulates the
	// per-tool error counter — different from the loop's existing
	// toolErrorStreaks which resets to 0 on first success, this is a
	// per-run cumulative count.
	//
	// Both are initialised to non-nil zero values (empty slice, empty
	// map) so JSON serialisation in the PostHog payload always emits
	// `[]` and `{}` rather than `null`. Lets HogQL queries like
	// `length(properties.tools_used) = 0` work without coalesce.
	toolsUsed := []string{}
	toolFailuresByName := map[string]int{}

	// Single observability epilogue covering all four exits:
	//   - panic    → agentloop.run.error{kind=panic}
	//   - non-nil error return → agentloop.run.error{kind=<class>}
	//   - nil error return → agentloop.run.completed
	// runErr is the named return; we read it after the function body
	// has populated it, including the panic-recovery path which sets
	// runErr below.
	defer func() {
		if p := recover(); p != nil {
			runErr = fmt.Errorf("panic: %v", p)
			// completion_reason aligns the run-terminal taxonomy with
			// lobehub ExecutionSnapshot.completionReason so dashboards
			// can render a pie chart of "how did runs end" without
			// translating between our kind/error_kind enums. Panics
			// always land in the "error" bucket (most coarse) — kind
			// still tells the story for drill-down.
			cfg.Telemetry.Capture("agentloop.run.error", map[string]any{
				"trace_id":              cfg.TraceID,
				"run_id":                cfg.RunID,
				"kind":                  "panic",
				"error_kind":            classifyStepError(runErr),
				"completion_reason":     classifyCompletionReason(runErr),
				"elapsed_ms":            time.Since(runStart).Milliseconds(),
				"tools_used":            toolsUsed,
				"tool_failures_by_name": toolFailuresByName,
			})
			emitError(cfg, fmt.Sprintf("internal error: %v", p))
			return
		}
		if runErr != nil {
			errProps := map[string]any{
				"trace_id":              cfg.TraceID,
				"run_id":                cfg.RunID,
				"kind":                  classifyLoopError(runErr),
				"completion_reason":     classifyCompletionReason(runErr),
				"elapsed_ms":            time.Since(runStart).Milliseconds(),
				"tools_used":            toolsUsed,
				"tool_failures_by_name": toolFailuresByName,
			}
			applyErrorTelemetryProperties(errProps, runErr)
			cfg.Telemetry.Capture("agentloop.run.error", errProps)
			return
		}
		cfg.Telemetry.Capture("agentloop.run.completed", map[string]any{
			"trace_id":              cfg.TraceID,
			"run_id":                cfg.RunID,
			"completion_reason":     "done",
			"elapsed_ms":            time.Since(runStart).Milliseconds(),
			"tools_used":            toolsUsed,
			"tool_failures_by_name": toolFailuresByName,
		})
	}()

	// Assemble message history.
	var history []HistoryItem
	if cfg.PriorHistory != nil {
		history = cfg.PriorHistory
	} else if cfg.History != nil {
		historyKey := cfg.HistoryKey
		if historyKey == "" {
			historyKey = cfg.SessionID
		}
		if historyKey != "" {
			h, err := cfg.History.LoadHistory(historyKey)
			if err != nil {
				emitError(cfg, fmt.Sprintf("load history: %v", err))
				return err
			}
			history = h
		}
	}

	// Append the new user message.
	userItem := HistoryItem{Kind: "user", Content: cfg.Prompt}
	if len(cfg.Images) > 0 {
		imgs := make([]map[string]string, 0, len(cfg.Images))
		for _, img := range cfg.Images {
			imgs = append(imgs, map[string]string{
				"media_type":  img.MediaType,
				"data_base64": img.Data,
			})
		}
		blob, _ := json.Marshal(imgs)
		userItem.Images = string(blob)
	}
	history = append(history, userItem)

	messages, err := BuildMessages(history)
	if err != nil {
		emitError(cfg, err.Error())
		return err
	}

	planModeActive := cfg.PlanMode != nil && cfg.PlanMode.Active()
	if planModeActive {
		// Phase 0.5 telemetry: explicit plan_mode state events so
		// dashboards can split plan-mode runs from normal runs (their
		// success / latency / tool usage profiles differ — plan mode
		// only exposes read-only tools so naturally has lower tool
		// success rates and faster median latency). Emitted at Run
		// entry rather than inside the loop because the initial state
		// is decided here once.
		cfg.Telemetry.Capture("agentloop.plan_mode.entered", map[string]any{
			"trace_id": cfg.TraceID,
			"run_id":   cfg.RunID,
			"trigger":  "initial",
		})
	}
	var systemBlocks []ContentBlock
	if !cfg.NoSystem {
		systemBlocks = BuildSystemBlocks(cfg.SystemText, planModeActive, cfg.WorkDir)
	}

	// Build tool definitions. Skipped when the caller explicitly opts
	// out (pure-text generation like suggestion chips / classification)
	// so we don't burn tokens on tool schemas the model won't use.
	var toolDefs []ToolDef
	programmaticToolCallingActive := false
	if !cfg.NoTools {
		toolList := cfg.Registry.List(cfg.AllowedTools)
		if planModeActive {
			// Intersect with read-only set.
			allowed := map[string]bool{"Read": true, "Grep": true, "Glob": true, "TodoWrite": true, "ExitPlanMode": true, "WebFetch": true, "WebSearch": true}
			filtered := toolList[:0]
			for _, t := range toolList {
				if allowed[t.Name()] {
					filtered = append(filtered, t)
				}
			}
			toolList = filtered
		}
		if len(cfg.DisabledTools) > 0 {
			disabled := make(map[string]struct{}, len(cfg.DisabledTools))
			for _, name := range cfg.DisabledTools {
				disabled[name] = struct{}{}
			}
			filtered := toolList[:0]
			for _, t := range toolList {
				if _, drop := disabled[t.Name()]; !drop {
					filtered = append(filtered, t)
				}
			}
			toolList = filtered
		}
		if cfg.ProgrammaticToolCalling && modelSupportsProgrammaticToolCalling(cfg.ModelID) {
			for _, t := range toolList {
				if supportsProgrammaticCalling(t.Name()) {
					programmaticToolCallingActive = true
					break
				}
			}
		}
		toolDefs = make([]ToolDef, 0, len(toolList)+1)
		if programmaticToolCallingActive {
			toolDefs = append(toolDefs, codeExecutionToolDef())
		}
		for _, t := range toolList {
			def := ToolDef{Name: t.Name(), Description: t.Description(), InputSchema: t.JSONSchema()}
			if programmaticToolCallingActive && supportsProgrammaticCalling(t.Name()) {
				def.AllowedCallers = []string{ProgrammaticToolCaller}
			}
			toolDefs = append(toolDefs, def)
		}
		if len(toolDefs) > 0 {
			toolDefs[len(toolDefs)-1].CacheControl = &CacheControl{Type: "ephemeral"}
		}
	}

	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = MaxOutputTokens
	}
	if cfg.MaxTotalTokens > 0 && maxTokens > cfg.MaxTotalTokens {
		maxTokens = cfg.MaxTotalTokens
	}

	cfg.Emit(LoopEvent{Type: "run_created"})

	// nudgesUsed counts the recovery user messages we've appended after
	// silent turns (no text, no tools). Capped at maxNudges so a
	// persistently-silent model can't spin the loop to MaxTurns. We
	// allow more than one nudge so a long tool-using session that goes
	// silent twice (once mid-flight, once at the end) still gets a
	// second recovery — this was the user-visible failure where Sonnet
	// would call several tools, then end_turn with only a thinking block.
	nudgesUsed := 0
	maxNudges := cfg.MaxNudges
	if maxNudges <= 0 {
		maxNudges = 2
	}
	// nudgeJustSent is true on the iteration that sends a request
	// containing a freshly-appended nudge. We disable extended thinking
	// on that one request: thinking-mode is exactly what makes Sonnet
	// fall back into another silent thinking-only turn, ignoring the
	// recovery prompt. Forcing thinking off makes it commit to text
	// (or another tool call).
	nudgeJustSent := false

	// Running token count drives auto-compaction. Updated after each
	// turn from the API's usage field; when it crosses the model's
	// auto-compact limit, the loop runs one summarise pass at the next
	// clean turn boundary (after tool_results are appended).
	runningTotalTokens := 0
	totalConsumedTokens := 0
	totalUsage := Usage{}
	autoCompactLimit := AutoCompactLimitForModel(cfg.ModelID)

	// toolErrorStreaks counts consecutive is_error results per tool
	// name. When a tool's streak hits toolErrorStreakLimit we inject a
	// user message asking the model to stop calling that tool. This is
	// the "give up gracefully" guard that prevents max-turn burns when a
	// model keeps trying URL after URL (or any tool) and they all fail —
	// observed in the wild with WebFetch when WebSearch is unavailable
	// and the model fabricates URLs from article titles.
	toolErrorStreaks := map[string]int{}
	toolErrorStreakTrips := map[string]int{}

	// directProgrammaticToolCalls counts direct calls to tools that are also
	// exposed to Anthropic code_execution. Some gateways/models still emit
	// direct calls even when allowed_callers asks for code_execution-mediated
	// calling, so we add one explicit batching nudge after a short streak.
	directProgrammaticToolCalls := 0
	directProgrammaticToolCounts := map[string]int{}
	directProgrammaticNudgeSent := false

	// uiAutomationToolCalls is a broad budget for browser/GUI automation
	// loops. Exact-input and numeric guards catch deterministic repeats;
	// this catches the harder case where the model keeps generating fresh
	// one-off CUA/AppleScript/screenshot probes that technically differ
	// but are still burning turns in the same stuck UI interaction.
	uiAutomationToolCalls := 0
	uiAutomationBudgetNudgeSent := false
	uiAutomationBudgetHardTrips := 0

	// aiAskToolCalls counts real `opencli <ai> ask|chat` executions across
	// the run (see ai_ask_budget_guard.go). The "ONE AI-source ask per
	// question" routing rule lives in the system prompt and the smart-search
	// skill, but prompt-only enforcement fails — this guard makes the budget
	// deterministic: 2nd ask executes with a stop-nudge, 3rd+ is blocked.
	// When the user's prompt explicitly names two or more AI adapters
	// ("问问 grok 和 gemini 对比下"), the allowance raises both thresholds
	// so an explicit multi-source comparison request is not blocked.
	aiAskToolCalls := 0
	aiAskBudgetNudgeSent := false
	aiAskAllowance := AIAskBudgetAllowance(cfg.Prompt)

	// loopGuardWindow tracks recent tool calls in a rolling window.
	// Only the last LoopWindowSize entries are considered when counting
	// duplicates — identical calls far apart (separated by many distinct
	// calls) fall out of the window and do not accumulate. The stable
	// JSON+SHA256 hash means the model cannot evade the guard by
	// re-ordering JSON keys between turns.
	loopGuardWindow := &LoopGuardWindow{}
	numericVariantGuardCounts := map[string]int{}
	numericVariantHardTrips := map[string]int{}
	searchNarrowingGuardStates := map[string]SearchNarrowingLoopState{}
	searchNarrowingHardTrips := map[string]int{}

	// cuaProgress tracks whether successive Computer Use snapshots advance
	// (window title / URL change). Catches the no-progress loop CheckForLoop
	// misses — different refs/coords each retry, identical resulting UI.
	cuaProgress := &CuaProgressState{}

	// cuaInventory drives the one-time "opening map" of already-open windows
	// injected into the first cua_driver result of the run (see
	// cua_inventory_prelude.go). Gives the model OpenBridge-style environment
	// awareness up front so it stops navigating by trial-and-error.
	cuaInventory := &CuaInventoryState{}

	// pairingRetryUsed enforces "retry once" on the per-turn tool-pairing
	// auto-repair path: if Anthropic 400s with a tool_use ↔ tool_result
	// mismatch, RepairOrphans runs and we retry the same turn ONCE. A
	// second 400 surfaces normally — the broken state is no longer in
	// the message slice, so retrying again is futile.
	pairingRetryUsed := false
	// byteBudgetRetryUsed enforces "retry once" on the request-too-large
	// recovery path: if the upstream 400s because the serialized request
	// exceeds its byte ceiling (e.g. Kimi/Moonshot's 2 MiB
	// "total message size exceeds limit 2097152"), we shrink the heaviest
	// image payloads (EnforceRequestByteBudget) and retry the same turn
	// ONCE. Reactive — NOT pre-emptive — so requests that the upstream
	// accepts (a 6 MB image on a 32 MB-limit model) are never needlessly
	// downscaled; we only intervene when the upstream actually rejects for
	// size, which makes the limit value itself irrelevant (different
	// sub2api-routed models enforce different ceilings).
	byteBudgetRetryUsed := false
	// contextOverflowRetryUsed enforces "retry once" on the token-window
	// overflow recovery path: if the upstream 400s because the request exceeds
	// the model's TOKEN window (sub2api returns "exceeded model token limit:
	// 262144"; current upstream DeepSeek), we compact history and retry the
	// same turn ONCE. classifyStepError already buckets this as context_overflow;
	// this is the missing ACTION on that bucket — without it a token-limit 400
	// still surfaces as a hard failure, because proactive auto-compaction
	// triggers on a last-turn lower-bound estimate and a single large new
	// payload (e.g. several big tool results in one turn) can jump the request
	// past the window in one step before it fires.
	contextOverflowRetryUsed := false
	// pairingJustRetried is set right after the retry continue and read
	// at the top of the next iteration: it lets us emit the
	// agent.tool_pairing.recovered telemetry event with succeeded=true
	// when the retry's API call returned 200, or succeeded=false when
	// the retry also errored. Read-and-clear, single-shot per loop run
	// since pairingRetryUsed caps retries at one.
	pairingJustRetried := false
	// collapseRetryUsed enforces "retry once" on the tool-collapse
	// fallback path: when RepairOrphans retry still fails (typically
	// sub2api mangling tool_call_id during format conversion),
	// CollapseToolBlocks converts all tool_use/tool_result blocks to
	// plain text and retries once more.
	collapseRetryUsed := false

	// apiErrorStreak tracks consecutive turns where the LLM API returned
	// a transient error (429/5xx) after exhausting Stream()'s 3 internal
	// retries. Acts as a cross-turn circuit breaker: backs off with
	// increasing delays and terminates the loop after
	// apiCircuitBreakerThreshold consecutive failures. Prevents the
	// amplification pattern where many concurrent agents each retry
	// against an already-overloaded upstream (see api_error_guard.go).
	apiErrorStreak := 0

	// modelFallbackUsed is set once when the loop detects a 503 "no
	// available accounts" — the requested model has no mapping on
	// sub2api. Retrying the same model is pointless; we switch to the
	// tier-aware modelFallbackTargetFor result and retry once. If the
	// fallback also fails the error flows into the normal circuit
	// breaker / hard-fail path.
	modelFallbackUsed := false

	// continuationAttempts counts consecutive max_tokens turns we've
	// auto-continued via "Resume directly" nudges. Reset on any
	// successful tool round (genuine progress means the model isn't
	// stuck mid-thought). Capped at maxContinuations so a runaway
	// length-cap loop terminates.
	continuationAttempts := 0
	const maxContinuations = 3
	codeExecutionContainerID := ""

	// nextStepPhase is the value that will be stamped onto the next
	// step.llm.* event's `phase` property — Phase 0.6 alignment with
	// lobehub StepSnapshot.context.phase. Set by the loop branches that
	// rearrange the next request (continuation / compact / nudge /
	// pairing-retry); read-and-reset at the top of each turn so the
	// label only sticks for the immediately-following LLM call. The
	// dashboard uses this to split "first attempt" from "recovery
	// attempt" — same elapsed_ms means very different things between
	// the two.
	nextStepPhase := "initial"

	for turn := 0; turn < MaxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			emitError(cfg, fmt.Sprintf("task interrupted: %v", err))
			return err
		}
		// Defensive sweep BEFORE every API call: reconcile tool_use ↔
		// tool_result pairing in BOTH directions. Cheap, idempotent,
		// and the universal bulletproof against the "session locked by
		// 400" failure mode where a single missing tool_use row in the
		// frontend-persisted history dooms every subsequent send. See
		// orphan_repair.go for the algorithm.
		if RepairOrphans(&messages) {
			log.Printf("[native-loop] turn=%d runID=%s RepairOrphans mutated messages — see [orphan-repair] log lines for ids", turn, cfg.RunID)
		}
		// Proactive tool ID regeneration: on the FIRST API call of a
		// new user message (turn == 0), re-generate all tool_use IDs
		// inherited from conversation history and update matching
		// tool_result references. This keeps the structured tool
		// context intact (the model never sees tool I/O as quotable
		// text) while ensuring the IDs are fresh and consistently
		// paired — sub2api can mangle stale IDs during Anthropic →
		// OpenAI conversion, causing 400s on follow-up turns. At
		// turn > 0, tool blocks are from the CURRENT run (just
		// created, properly paired) and left intact.
		if turn == 0 && HasToolBlocks(messages) {
			ReIDToolBlocks(&messages)
		}
		// Fold stale Computer-Use snapshots BEFORE building the request.
		// Each cua_driver get_window_state returns a full window AX tree
		// (tens of thousands of tokens for a rich web app); a long CUA run
		// accumulates them and 400s the 262144-token upstream. We keep only
		// the most recent few in full — older snapshots describe UI that has
		// already changed and whose refs no longer resolve. See
		// cua_snapshot_prune.go.
		if stats := PruneStaleCuaSnapshots(messages); stats.Touched() {
			log.Printf("[native-loop] turn=%d runID=%s folded %d stale cua snapshot(s), ~%d bytes freed", turn, cfg.RunID, stats.FoldedCount, stats.BytesFreed)
		}
		ApplyCachePrefix(messages)

		requestMaxTokens := maxTokens
		if cfg.MaxTotalTokens > 0 {
			remaining := cfg.MaxTotalTokens - totalConsumedTokens
			if remaining <= 0 {
				emitError(cfg, ErrTokenBudgetExceeded.Error())
				cfg.Emit(LoopEvent{Type: "done", Usage: copyUsage(totalUsage)})
				return ErrTokenBudgetExceeded
			}
			if requestMaxTokens > remaining {
				requestMaxTokens = remaining
			}
		}
		req := &Request{
			Model:     cfg.ModelID,
			Messages:  messages,
			System:    systemBlocks,
			Tools:     toolDefs,
			MaxTokens: requestMaxTokens,
		}
		if cfg.ModelEffort != "" {
			req.OutputConfig = &OutputConfig{Effort: cfg.ModelEffort}
		}
		if codeExecutionContainerID != "" {
			req.Container = codeExecutionContainerID
		}
		if cfg.ThinkingEnabled && !nudgeJustSent {
			if modelRequiresAdaptiveThinking(cfg.ModelID) {
				// Claude 4.7+ models 400 on {type:"enabled",
				// budget_tokens:N}; adaptive is the only on-mode and
				// takes no budget.
				req.Thinking = &ThinkingConfig{Type: "adaptive"}
			} else {
				req.Thinking = &ThinkingConfig{Type: "enabled", BudgetTokens: requestMaxTokens / 2}
			}
		}
		// B3-ζ: when we're about to send a recovery nudge AND the caller
		// has supplied a custom RecoveryNudgeText (signalling a
		// tool-using flow such as agent generation / auto-fix /
		// targeted-fix), force tool_choice=any. The proxy chain
		// (sub2api → claude.ai webapp) keeps thinking on regardless of
		// our Thinking:disabled config, so the model can still
		// thinking-only end_turn through repeated nudges and burn the
		// MaxNudges budget. Forcing tool_choice="any" requires the
		// model to emit at least one tool_use block, breaking the
		// thinking-only loop. Only applied on the nudge turn so chat
		// flows that genuinely should produce a text summary on
		// recovery (default RecoveryNudgeText) keep their existing
		// behaviour.
		if nudgeJustSent && cfg.RecoveryNudgeText != "" && len(toolDefs) > 0 {
			req.ToolChoice = &ToolChoice{Type: "any"}
			log.Printf("[native-loop] turn=%d runID=%s forcing tool_choice=any on recovery nudge", turn, cfg.RunID)
		}
		nudgeJustSent = false
		if cfg.MaxTotalTokens > 0 {
			remaining := cfg.MaxTotalTokens - totalConsumedTokens
			inputUpperBound, estimateErr := requestInputTokenUpperBound(req)
			if estimateErr != nil || inputUpperBound >= remaining {
				emitError(cfg, ErrTokenBudgetExceeded.Error())
				cfg.Emit(LoopEvent{Type: "done", Usage: copyUsage(totalUsage)})
				return ErrTokenBudgetExceeded
			}
			remainingOutput := remaining - inputUpperBound
			if requestMaxTokens > remainingOutput {
				requestMaxTokens = remainingOutput
				req.MaxTokens = requestMaxTokens
				if req.Thinking != nil && req.Thinking.Type == "enabled" {
					req.Thinking.BudgetTokens = requestMaxTokens / 2
				}
			}
		}

		turnStart := time.Now()
		log.Printf("[native-loop] turn=%d runID=%s model=%s messages=%d tools=%d max_tokens=%d", turn, cfg.RunID, cfg.ModelID, len(messages), len(toolDefs), requestMaxTokens)
		// Phase 0.6: attach a per-turn retry counter so Stream can
		// report how many transient-error retries the upstream took
		// before settling. We create a fresh counter each turn (not a
		// run-level one) so a 2-retry turn followed by a 0-retry turn
		// shows up as two distinct step events with the right values.
		turnCtx, retryCounter := ContextWithRetryCounter(ctx)
		// Stamp this turn's 0-based step index so doStream sets
		// x-yansu-step-index — lets the backend conversation capture
		// (plan §12) line each captured call up with the timeline step.
		turnCtx = ContextWithStepIndex(turnCtx, turn)
		// Step-level telemetry: emit started before the model call and
		// completed/error after, mirroring lobehub's stepType=call_llm
		// boundary. Properties are intentionally limited to counts and
		// metadata (no message bodies) so no user content leaves the
		// device — the analytics surface uses these for success rate,
		// latency, token consumption and error-pattern dashboards.
		thinkingEnabled := req.Thinking != nil && (req.Thinking.Type == "enabled" || req.Thinking.Type == "adaptive")
		// Read-and-reset phase: the label only describes the turn that
		// follows the qualifying branch; resetting to "initial" here means
		// any later same-run turn that isn't itself triggered by a recovery
		// branch goes back to neutral, matching how a human reader of the
		// trace would intuit it.
		stepPhase := nextStepPhase
		nextStepPhase = "initial"
		// Capture the Context Engine snapshot ONCE per LLM call and
		// reuse it across started/error/completed emissions so the
		// fields stay coherent for a given step (the collector might
		// pull mutable state — message counts, knowledge file list —
		// and we don't want those drifting between the started and
		// completed events for the same step).
		var ceSnap *ContextEngineSnapshot
		if cfg.ContextEngineCollector != nil {
			systemTextForSnap := ""
			if !cfg.NoSystem && len(systemBlocks) > 0 {
				systemTextForSnap = systemBlocks[0].Text
			}
			ceSnap = cfg.ContextEngineCollector(ContextEngineRequest{
				StepIndex:  turn,
				SystemText: systemTextForSnap,
				Messages:   messages,
				Tools:      toolDefs,
			})
		}
		startedProps := map[string]any{
			"trace_id":         cfg.TraceID,
			"run_id":           cfg.RunID,
			"step_index":       turn,
			"model":            cfg.ModelID,
			"messages_count":   len(messages),
			"tools_count":      len(toolDefs),
			"max_tokens":       requestMaxTokens,
			"thinking_enabled": thinkingEnabled,
			"phase":            stepPhase,
		}
		applySnapshotToProps(startedProps, ceSnap, turn)
		cfg.Telemetry.Capture("agentloop.step.llm.started", startedProps)
		turnResult, err := runOneTurn(turnCtx, cfg, req)
		stepElapsedMs := time.Since(turnStart).Milliseconds()
		externalRetryCount := int(retryCounter.Load())
		log.Printf("[native-loop] turn=%d runID=%s done in %s err=%v retries=%d", turn, cfg.RunID, time.Since(turnStart), errorDiagnostic(err), externalRetryCount)
		if err != nil {
			// step.llm.error always fires before any pairing-retry path
			// below so failed attempts that *are* later recovered still
			// show up as 1 error + 1 success in the dashboard (matching
			// how lobehub records retries as separate steps).
			errProps := map[string]any{
				"trace_id":             cfg.TraceID,
				"run_id":               cfg.RunID,
				"step_index":           turn,
				"model":                cfg.ModelID,
				"elapsed_ms":           stepElapsedMs,
				"phase":                stepPhase,
				"external_retry_count": externalRetryCount,
			}
			applyErrorTelemetryProperties(errProps, err)
			applySnapshotToProps(errProps, ceSnap, turn)
			cfg.Telemetry.Capture("agentloop.step.llm.error", errProps)
		} else if turnResult != nil {
			completedProps := map[string]any{
				"trace_id":             cfg.TraceID,
				"run_id":               cfg.RunID,
				"step_index":           turn,
				"model":                cfg.ModelID,
				"elapsed_ms":           stepElapsedMs,
				"stop_reason":          turnResult.StopReason,
				"tool_use_count":       len(turnResult.ToolUses),
				"phase":                stepPhase,
				"external_retry_count": externalRetryCount,
			}
			if u := turnResult.Usage; u != nil {
				completedProps["input_tokens"] = u.InputTokens
				completedProps["output_tokens"] = u.OutputTokens
				completedProps["cache_read_tokens"] = u.CacheReadInputTokens
				completedProps["cache_creation_tokens"] = u.CacheCreationInputTokens
			}
			applySnapshotToProps(completedProps, ceSnap, turn)
			cfg.Telemetry.Capture("agentloop.step.llm.completed", completedProps)
		}
		if turnResult != nil {
			if turnResult.ContainerID != "" {
				codeExecutionContainerID = turnResult.ContainerID
			}
			var inTok, outTok, cacheRead, cacheCreate int
			if u := turnResult.Usage; u != nil {
				inTok, outTok, cacheRead, cacheCreate = u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens
			}
			log.Printf("[native-loop][usage] turn=%d runID=%s stop_reason=%q output_tokens=%d input_tokens=%d cache_read=%d cache_create=%d max_tokens_cap=%d hit_cap=%v",
				turn, cfg.RunID, turnResult.StopReason, outTok, inTok, cacheRead, cacheCreate, requestMaxTokens, turnResult.StopReason == "max_tokens")
		}
		if err != nil {
			if ctx.Err() != nil {
				// The parent context is also done. Two distinct cases:
				//   1. User clicked Stop / navigated away → err is just
				//      context.Canceled, no transport info. Stay silent so
				//      we don't render a confusing "something went wrong"
				//      card on top of a user-intentional cancel.
				//   2. The HTTP transport cancelled the request context
				//      because the TLS handshake or dial tripped. The Go
				//      http client wraps that into an error like
				//      `Post "...": context canceled` which is distinct
				//      from the bare context.Canceled — it carries the
				//      URL + transport-level diagnostic. Surface it so the
				//      user actually sees a Connection problem card
				//      instead of an infinite loading spinner.
				if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "http") || strings.Contains(err.Error(), "Post ") {
					emitErrorFrom(cfg, err.Error(), err)
				}
				return ctx.Err()
			}
			// Request-body byte-limit 400 from the upstream? Shrink the
			// heaviest image payloads and retry the same turn ONCE. Reactive
			// by design: we only get here when the upstream actually rejected
			// the request for size, so requests that fit (a 6 MB image on a
			// high-limit model) sail through untouched — no pre-emptive
			// quality loss. See image_budget.go / EnforceRequestByteBudget.
			if classifyStepError(err) == "request_too_large" && !byteBudgetRetryUsed {
				stats := EnforceRequestByteBudget(messages)
				if stats.Touched() {
					log.Printf("[native-loop] turn=%d runID=%s request-too-large 400; shrank images (downsized=%d dropped=%d bytes %d→%d) and retrying once",
						turn, cfg.RunID, stats.DownsizedCount, stats.DroppedCount, stats.BytesBefore, stats.BytesAfter)
					cfg.Telemetry.Capture("agentloop.context.image_budget.applied", map[string]any{
						"trace_id":           cfg.TraceID,
						"run_id":             cfg.RunID,
						"step_index":         turn,
						"downsized_count":    stats.DownsizedCount,
						"dropped_count":      stats.DroppedCount,
						"image_bytes_before": stats.BytesBefore,
						"image_bytes_after":  stats.BytesAfter,
					})
					byteBudgetRetryUsed = true
					nextStepPhase = "post_byte_budget_retry"
					turn-- // re-attempt the same turn index
					continue
				}
				// Nothing left to shrink (e.g. the bulk is text history, not
				// images) — fall through and surface the error rather than
				// spin on a retry that would send the identical request.
				log.Printf("[native-loop] turn=%d runID=%s request-too-large 400 but no image payload to shrink; surfacing error", turn, cfg.RunID)
			}
			// Context-window TOKEN-limit 400? Compact history and retry the
			// same turn ONCE — symmetric to the byte-budget path above, but for
			// the token window instead of the 2 MiB wire limit. classifyStepError
			// already classifies this as context_overflow; there is no ACTION on
			// that bucket yet, so a token-limit 400 still surfaces as a hard
			// failure. This is that missing reactive net.
			if classifyStepError(err) == "context_overflow" && !contextOverflowRetryUsed {
				contextOverflowRetryUsed = true
				cfg.Telemetry.Capture("agentloop.context.overflow_retry", map[string]any{
					"trace_id":   cfg.TraceID,
					"run_id":     cfg.RunID,
					"step_index": turn,
				})
				// Reuse proactive compaction's event semantics. compact_start
				// shows the GUI a "compacting" state; compact_end / compact_error
				// are LOAD-BEARING — streaming.go persists a compaction boundary
				// on either, and history_loader.go drops everything before the
				// latest compact_boundary on the next session restore. Without
				// them this reactive compaction would live only in memory: the
				// current run recovers, but a restart/continue re-loads the
				// un-compacted history and can 400 again.
				cfg.Emit(LoopEvent{
					Type:    "compact_start",
					Content: "Context window exceeded; compacting conversation history and retrying.",
				})
				compactRes, compactErr := runAutoCompact(ctx, cfg.Client, cfg.ModelID, messages)
				if ctx.Err() != nil {
					// User cancelled mid-compaction. Surface the cancellation
					// like the send-error path above, rather than emitting a
					// misleading compact_error and doing a pointless
					// drop-oldest-half + retry that the next iteration aborts.
					return ctx.Err()
				}
				if compactErr != nil || compactRes == nil {
					log.Printf("[native-loop] turn=%d runID=%s context_overflow 400; auto-compact failed (%v), dropping oldest half and retrying once", turn, cfg.RunID, errorDiagnostic(compactErr))
					messages = dropOldestHalf(messages)
					cfg.Emit(LoopEvent{
						Type:  "compact_error",
						Error: fmt.Sprintf("context-window overflow; summarisation failed (%v), dropped oldest half of history", compactErr),
					})
				} else {
					log.Printf("[native-loop] turn=%d runID=%s context_overflow 400; compacted (summary=%d chars) and retrying once", turn, cfg.RunID, len(compactRes.SummaryText))
					messages = compactRes.NewMessages
					cfg.Emit(LoopEvent{
						Type:    "compact_end",
						Content: compactRes.SummaryText,
					})
				}
				runningTotalTokens = 0
				nextStepPhase = "post_context_overflow_retry"
				turn-- // re-attempt the same turn index
				continue
			}
			// Tool-pairing protocol error from Anthropic? Run another
			// RepairOrphans pass and retry the same turn ONCE. The
			// pre-call repair above is the primary defense; this catches
			// the case where state mutation between repair and send (or
			// a server-side change in pairing semantics) leaves a
			// residual orphan. After one retry we surface normally
			// because the broken state is no longer in messages and
			// re-retrying would be futile.
			if IsToolPairingError(err) && !pairingRetryUsed {
				log.Printf("[native-loop] turn=%d runID=%s tool-pairing 400 detected; running RepairOrphans and retrying once", turn, cfg.RunID)
				// Properties: classification (wording/orphan tool
				// name) PLUS a privacy-safe fingerprint of the messages
				// slice we just sent. The fingerprint is what lets us
				// answer "was there an empty tool_use_id in the request"
				// on the dashboard — directly distinguishing case (A)
				// "our messages had empty ids" from case (B) "upstream
				// cache state, our messages were clean." See
				// messagesFingerprint in telemetry_helpers.go for the
				// exact fields. Capped at < 200 bytes; no message
				// content / user data leaves the device.
				props := map[string]any{
					"trace_id":         cfg.TraceID,
					"run_id":           cfg.RunID,
					"turn":             turn,
					"wording":          classifyPairingWording(err),
					"orphan_tool_name": extractOrphanToolName(err, messages),
				}
				applyErrorTelemetryProperties(props, err)
				for k, v := range messagesFingerprint(messages) {
					props[k] = v
				}
				cfg.Telemetry.Capture("agentloop.tool_pairing.detected", props)
				// Local one-shot dump: write the FULL request messages
				// JSON to a per-trace file on disk so we can grep the
				// real payload when triaging the bug. PostHog only gets
				// the fingerprint (above); the raw body never leaves
				// the user's machine. dumpRequestBodyForPairingError
				// is best-effort — failures are logged, not surfaced.
				if !SuppressAgentloopFromContext(ctx) {
					dumpRequestBodyForPairingError(cfg, messages, err)
				}
				pairingRetryUsed = true
				pairingJustRetried = true
				RepairOrphans(&messages)
				nextStepPhase = "post_pairing_retry"
				turn-- // re-attempt the same turn index
				continue
			}
			// We are past the retry branch and have a non-recoverable
			// error. If the previous iteration set pairingJustRetried,
			// the retry itself failed — try one last fallback: strip
			// all tool_use/tool_result blocks so the sub2api proxy
			// never sees structured tool calls (which it can mangle
			// during Anthropic → OpenAI conversion). Context is lost,
			// but the conversation can continue.
			if pairingJustRetried && IsToolPairingError(err) && !collapseRetryUsed {
				collapseRetryUsed = true
				if CollapseToolBlocks(&messages) {
					log.Printf("[native-loop] turn=%d runID=%s RepairOrphans retry failed; collapsing tool blocks to text and retrying once", turn, cfg.RunID)
					cfg.Telemetry.Capture("agentloop.tool_pairing.recovered", map[string]any{
						"trace_id":  cfg.TraceID,
						"run_id":    cfg.RunID,
						"turn":      turn,
						"succeeded": false,
						"fallback":  "collapse_tools",
					})
					pairingJustRetried = false
					turn--
					continue
				}
				log.Printf("[native-loop] turn=%d runID=%s RepairOrphans retry failed, no tool blocks to collapse; surfacing error", turn, cfg.RunID)
			}
			if pairingJustRetried {
				cfg.Telemetry.Capture("agentloop.tool_pairing.recovered", map[string]any{
					"trace_id":  cfg.TraceID,
					"run_id":    cfg.RunID,
					"turn":      turn,
					"succeeded": false,
				})
				pairingJustRetried = false
			}

			// Model fallback: sub2api 503 "no available accounts" means the
			// requested model has no mapping — retrying is futile. Switch
			// to the tier-aware fallback model and retry the turn
			// immediately. Only attempted once; if the fallback model also
			// has no accounts, or the tier has no fallback (opus, custom
			// ids), we fall through to the circuit breaker / hard-fail path.
			if fallback := modelFallbackTargetFor(cfg.ModelID); isModelUnavailableError(err) && !modelFallbackUsed && fallback != "" && fallback != cfg.ModelID {
				modelFallbackUsed = true
				prev := cfg.ModelID
				cfg.ModelID = fallback
				newMax := MaxOutputTokensForModel(cfg.ModelID)
				if newMax > 0 {
					maxTokens = newMax
				}
				autoCompactLimit = AutoCompactLimitForModel(cfg.ModelID)
				log.Printf("[native-loop] turn=%d runID=%s model %s unavailable (503 no available accounts), falling back to %s", turn, cfg.RunID, prev, cfg.ModelID)
				cfg.Emit(LoopEvent{
					Type:    "status",
					Content: fmt.Sprintf("Model %s unavailable, falling back to %s", prev, cfg.ModelID),
				})
				cfg.Telemetry.Capture("agentloop.model_fallback", map[string]any{
					"trace_id":       cfg.TraceID,
					"run_id":         cfg.RunID,
					"step_index":     turn,
					"original_model": prev,
					"fallback_model": cfg.ModelID,
				})
				nextStepPhase = "post_model_fallback"
				turn--
				continue
			}

			// API circuit breaker: transient upstream errors (429/5xx) get
			// cross-turn retries with exponential backoff instead of an
			// immediate hard failure. A brief rate-limit window or a 503
			// blip no longer kills a long-running task; a persistently-
			// down upstream terminates cleanly after the threshold without
			// amplifying load.
			if isTransientAPIError(err) {
				apiErrorStreak++
				if apiErrorStreak >= apiCircuitBreakerThreshold {
					msg := fmt.Sprintf("API circuit breaker: %d consecutive transient failures, giving up (last: %v)", apiErrorStreak, errorDiagnostic(err))
					log.Printf("[native-loop] turn=%d runID=%s %s", turn, cfg.RunID, msg)
					errProps := map[string]any{
						"trace_id":   cfg.TraceID,
						"run_id":     cfg.RunID,
						"step_index": turn,
						"streak":     apiErrorStreak,
					}
					applyErrorTelemetryProperties(errProps, err)
					cfg.Telemetry.Capture("agentloop.api_circuit_breaker.tripped", errProps)
					emitErrorFrom(cfg, msg, err)
					return fmt.Errorf("%w: %s", ErrAPICircuitBreaker, msg)
				}
				backoff := apiCircuitBreakerBackoff(apiErrorStreak)
				log.Printf("[native-loop] turn=%d runID=%s API error streak=%d, backing off %s before retry (error: %v)", turn, cfg.RunID, apiErrorStreak, backoff, errorDiagnostic(err))
				cfg.Emit(LoopEvent{
					Type:    "status",
					Content: fmt.Sprintf("API temporarily unavailable, retrying in %s... (attempt %d/%d)", backoff, apiErrorStreak, apiCircuitBreakerThreshold),
				})
				errProps := map[string]any{
					"trace_id":   cfg.TraceID,
					"run_id":     cfg.RunID,
					"step_index": turn,
					"streak":     apiErrorStreak,
					"backoff_ms": backoff.Milliseconds(),
				}
				applyErrorTelemetryProperties(errProps, err)
				cfg.Telemetry.Capture("agentloop.api_circuit_breaker.backoff", errProps)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
				nextStepPhase = "post_api_circuit_breaker_retry"
				turn--
				continue
			}

			emitErrorFrom(cfg, err.Error(), err)
			return err
		}

		// Retry recovery success path: if the previous iteration set
		// pairingJustRetried and we now have a clean turnResult (no err
		// above), the retry succeeded. Emit recovered=true so the trace
		// shows the self-heal worked.
		if pairingJustRetried {
			cfg.Telemetry.Capture("agentloop.tool_pairing.recovered", map[string]any{
				"trace_id":  cfg.TraceID,
				"run_id":    cfg.RunID,
				"turn":      turn,
				"succeeded": true,
			})
			pairingJustRetried = false
		}

		if apiErrorStreak > 0 {
			log.Printf("[native-loop] turn=%d runID=%s API error streak reset (was %d)", turn, cfg.RunID, apiErrorStreak)
			cfg.Telemetry.Capture("agentloop.api_circuit_breaker.recovered", map[string]any{
				"trace_id":   cfg.TraceID,
				"run_id":     cfg.RunID,
				"step_index": turn,
				"streak":     apiErrorStreak,
			})
			apiErrorStreak = 0
		}

		addUsage(&totalUsage, turnResult.Usage)
		totalConsumedTokens += EstimateRequestTokens(turnResult.Usage)
		if cfg.MaxTotalTokens > 0 &&
			(totalConsumedTokens > cfg.MaxTotalTokens ||
				totalConsumedTokens >= cfg.MaxTotalTokens &&
					len(turnResult.ToolUses) > 0) {
			emitError(cfg, ErrTokenBudgetExceeded.Error())
			cfg.Emit(LoopEvent{Type: "done", Usage: copyUsage(totalUsage)})
			return ErrTokenBudgetExceeded
		}

		// Append assistant message (text + thinking + tool_use blocks)
		// to the conversation — we need it in messages even if there
		// were no tools, so the model can see its own prior output.
		messages = append(messages, Message{Role: "assistant", Content: turnResult.AssistantBlocks})

		// Update running token count from this turn's usage. We use
		// last-turn input+cache+output because that's what the next
		// request prefix will weigh; new user/assistant content for the
		// upcoming turn isn't counted yet (lower bound).
		if est := EstimateRequestTokens(turnResult.Usage); est > 0 {
			runningTotalTokens = est
		}

		// No tool uses → the model is trying to end the turn. Four cases:
		//   1. Visible text + natural stop → genuine completion, emit done.
		//   2. Visible text but stop_reason=max_tokens → answer was truncated
		//      mid-stream. Emit a truncation notice before done so the
		//      caller can surface "response was cut off" rather than
		//      silently presenting the partial answer as final.
		//   3. No text and hit max_tokens → truncated before any text
		//      emitted; surface a clear error.
		//   4. No text and stopped naturally (end_turn with only thinking,
		//      which Sonnet + extended-thinking does after some
		//      tool_results) → the user would see "agent went silent".
		//      Send one recovery nudge asking for a final answer; if that
		//      turn is ALSO silent, surface an error rather than looping.
		//
		// We do NOT auto-continue on max_tokens. A second turn would
		// duplicate the partial answer or drift in voice; the user can
		// re-prompt with "continue" if they want the rest, and the
		// truncation notice tells them why.
		if len(turnResult.ToolUses) == 0 {
			// Diagnostic for the residual silent-turn case: B3-β suppresses
			// the 12k-token thinking-block silent turns, but smaller silent
			// turns (a few hundred tokens) can still occur. Log every block
			// in the assistant message — type + content length — so we can
			// see whether the tokens are going into whitespace-only text,
			// truncated tool_use blocks (start without stop), or something
			// else entirely. Cheap (one log line per silent turn).
			if !turnHasText(turnResult.AssistantBlocks) {
				summary := describeAssistantBlocks(turnResult.AssistantBlocks)
				log.Printf("[native-loop] silent-turn diagnostic turn=%d runID=%s blocks=%d %s",
					turn, cfg.RunID, len(turnResult.AssistantBlocks), summary)
			}
			if turnHasText(turnResult.AssistantBlocks) {
				if turnResult.StopReason == "max_tokens" {
					// Auto-continue: instead of telling the user the
					// response was cut off and making them type
					// "continue", push a "Resume directly" nudge and
					// stream the rest of the answer in the same logical
					// turn. Capped at maxContinuations so a runaway
					// reply still terminates. Counter resets on any
					// successful tool round (genuine progress).
					if continuationAttempts < maxContinuations {
						continuationAttempts++
						log.Printf("[native-loop] turn=%d runID=%s max_tokens after text; auto-continuing %d/%d",
							turn, cfg.RunID, continuationAttempts, maxContinuations)
						// Phase 0.5 telemetry: continuation is "context
						// strategy" — when truncation is frequent it
						// usually means MaxTokens is too tight for the
						// task, or the response shape is too verbose.
						// Tracking output_tokens at the truncation
						// boundary lets dashboards spot the threshold.
						continuationProps := map[string]any{
							"trace_id":              cfg.TraceID,
							"run_id":                cfg.RunID,
							"step_index":            turn,
							"continuation_attempts": continuationAttempts,
							"max_continuations":     maxContinuations,
							"max_tokens_cap":        requestMaxTokens,
						}
						if turnResult.Usage != nil {
							continuationProps["output_tokens"] = turnResult.Usage.OutputTokens
						}
						cfg.Telemetry.Capture("agentloop.max_tokens.continuation", continuationProps)
						messages = append(messages, Message{
							Role: "user",
							Content: []ContentBlock{{
								Type: "text",
								Text: "Output token limit hit. Resume directly — no apology, no recap. Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces.",
							}},
						})
						nextStepPhase = "continuation"
						continue
					}
					cfg.Emit(LoopEvent{
						Type:  "error",
						Error: fmt.Sprintf("response still truncated after %d continuation attempts — ask a narrower question or break the task into smaller steps", maxContinuations),
					})
				}
				cfg.Emit(LoopEvent{Type: "done", Usage: copyUsage(totalUsage)})
				return nil
			}
			if turnResult.StopReason == "max_tokens" {
				cfg.Emit(LoopEvent{
					Type:  "error",
					Error: "response hit the max_tokens limit and was truncated before any text was emitted; try a shorter question or raise MaxTokens",
				})
				cfg.Emit(LoopEvent{Type: "done", Usage: copyUsage(totalUsage)})
				return nil
			}
			if nudgesUsed >= maxNudges {
				// We've already sent the maximum number of nudges and
				// the model is still silent. Don't loop forever.
				log.Printf("[native-loop] turn=%d runID=%s silent turn after %d nudges; emitting error (stop_reason=%s)",
					turn, cfg.RunID, nudgesUsed, turnResult.StopReason)
				cfg.Emit(LoopEvent{
					Type:  "error",
					Error: ErrSilentTurn.Error(),
				})
				cfg.Emit(LoopEvent{Type: "done", Usage: copyUsage(totalUsage)})
				return ErrSilentTurn
			}
			// Silent turn — append a recovery message that forces a
			// concrete text reply. Stronger phrasing than a polite
			// "please provide your final answer" because Sonnet with
			// extended thinking will otherwise just go think again.
			log.Printf("[native-loop] turn=%d runID=%s silent turn after tool use (stop_reason=%s); sending recovery nudge %d/%d",
				turn, cfg.RunID, turnResult.StopReason, nudgesUsed+1, maxNudges)
			nudgesUsed++
			nudgeJustSent = true
			nudgeText := cfg.RecoveryNudgeText
			customNudge := nudgeText != ""
			if nudgeText == "" {
				nudgeText = "Output your final answer to the user NOW as plain text — do not start another thinking block, do not call more tools unless absolutely necessary. " +
					"If the task succeeded, briefly summarise what you did and the result. " +
					"If anything failed, state in one sentence what went wrong and what the user could try next."
			}
			// Phase 0.5 telemetry: silent-turn recovery is the harness's
			// "error cognition" dimension. The pairing with the
			// next-turn step.llm.completed (on the same trace_id) lets
			// dashboards compute "nudge save rate" — how often the
			// recovery actually rescued the run. tool_choice_forced
			// mirrors the heuristic at the top of the loop body so we
			// can correlate forced-tool-choice runs with downstream
			// success.
			cfg.Telemetry.Capture("agentloop.recovery.nudge_sent", map[string]any{
				"trace_id":              cfg.TraceID,
				"run_id":                cfg.RunID,
				"step_index":            turn,
				"nudges_used":           nudgesUsed,
				"max_nudges":            maxNudges,
				"stop_reason":           turnResult.StopReason,
				"custom_nudge":          customNudge,
				"tool_choice_forced":    customNudge && len(toolDefs) > 0,
				"assistant_block_count": len(turnResult.AssistantBlocks),
			})
			messages = append(messages, Message{
				Role: "user",
				Content: []ContentBlock{{
					Type: "text",
					Text: nudgeText,
				}},
			})
			nextStepPhase = "post_nudge"
			continue
		}

		// Execute each tool_use block sequentially. Append results as
		// a single user message (Anthropic requires tool_results to be
		// grouped when they follow a single tool_use bundle).
		//
		// Loop-guard runs BEFORE each execute: if the (tool, input) hash
		// has already been seen at the soft (3) or hard (5) threshold,
		// short-circuit by pushing a synthetic [loop-guard] tool_result
		// and skipping execution entirely. The model reads the synthetic
		// result like any other tool reply and reliably pivots on the
		// next turn — saves tool execution cost AND breaks the
		// fabricate-and-retry pattern at the source.
		resultBlocks := make([]ContentBlock, 0, len(turnResult.ToolUses))
		hardTrippedTool := ""
		hardTrippedCount := 0
		numericVariantTrippedCount := 0
		searchNarrowingTrippedCount := 0
		uiAutomationBudgetTrippedCount := 0
		aiAskBudgetTrippedCount := 0
		cuaNoProgressTrippedCount := 0
		programmaticResultOnly := hasProgrammaticToolUse(turnResult.ToolUses)
		var loopGuardAbortErr error
		loopGuardAbortMsg := ""
		for _, tu := range turnResult.ToolUses {
			if programmaticToolCallingActive && !isProgrammaticToolUse(tu) && supportsProgrammaticCalling(tu.Name) {
				directProgrammaticToolCalls++
				directProgrammaticToolCounts[tu.Name]++
			}
			if loopGuardAbortErr != nil {
				blk := ContentBlock{
					Type:      "tool_result",
					ToolUseID: tu.ID,
					IsError:   true,
					Content:   "[loop-guard] Task is stopping because a repeated tool-call loop was detected. This tool was NOT executed.",
				}
				resultBlocks = append(resultBlocks, blk)
				if len(toolsUsed) < maxToolsUsedTracked {
					toolsUsed = append(toolsUsed, tu.Name)
				}
				if s, ok := blk.Content.(string); ok {
					cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: s, Failed: true})
				}
				continue
			}

			if !isProgrammaticToolUse(tu) {
				automationBudget := CheckUIAutomationBudget(uiAutomationToolCalls, tu.Name, tu.Input)
				if automationBudget.Counted {
					uiAutomationToolCalls = automationBudget.Count
				}
				switch automationBudget.Kind {
				case "nudge":
					if !uiAutomationBudgetNudgeSent {
						uiAutomationBudgetTrippedCount = automationBudget.Count
						uiAutomationBudgetNudgeSent = true
					}
				case "hard":
					log.Printf("[native-loop] turn=%d runID=%s tool=%s ui-automation-budget count=%d — skipping execution",
						turn, cfg.RunID, tu.Name, automationBudget.Count)
					synthetic := SyntheticUIAutomationBudgetBlockResult(tu.ID, automationBudget.Count)
					resultBlocks = append(resultBlocks, synthetic)
					if len(toolsUsed) < maxToolsUsedTracked {
						toolsUsed = append(toolsUsed, tu.Name)
					}
					if s, ok := synthetic.Content.(string); ok {
						cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: s, Failed: true})
					}
					cfg.Telemetry.Capture("agentloop.step.tool.completed", map[string]any{
						"trace_id":   cfg.TraceID,
						"run_id":     cfg.RunID,
						"step_index": turn,
						"tool_name":  tu.Name,
						"outcome":    "ui_automation_budget_guard_hard",
						"elapsed_ms": int64(0),
					})
					uiAutomationBudgetTrippedCount = automationBudget.Count
					uiAutomationBudgetHardTrips++
					if uiAutomationBudgetHardTrips > 1 {
						loopGuardAbortMsg = fmt.Sprintf("UI automation budget guard stopped repeated GUI/browser automation after %d tool calls", automationBudget.Count)
						loopGuardAbortErr = fmt.Errorf("%w: %s", ErrLoopGuardStuck, loopGuardAbortMsg)
					}
					continue
				}

				askBudget := CheckAIAskBudget(aiAskToolCalls, aiAskAllowance, tu.Name, tu.Input)
				if askBudget.Counted {
					aiAskToolCalls = askBudget.Count
				}
				switch askBudget.Kind {
				case "nudge":
					if !aiAskBudgetNudgeSent {
						aiAskBudgetTrippedCount = askBudget.Count
						aiAskBudgetNudgeSent = true
					}
				case "hard":
					log.Printf("[native-loop] turn=%d runID=%s tool=%s ai-ask-budget count=%d — skipping execution",
						turn, cfg.RunID, tu.Name, askBudget.Count)
					synthetic := SyntheticAIAskBudgetBlockResult(tu.ID, askBudget.Count)
					resultBlocks = append(resultBlocks, synthetic)
					if len(toolsUsed) < maxToolsUsedTracked {
						toolsUsed = append(toolsUsed, tu.Name)
					}
					if s, ok := synthetic.Content.(string); ok {
						cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: s, Failed: true})
					}
					cfg.Telemetry.Capture("agentloop.step.tool.completed", map[string]any{
						"trace_id":   cfg.TraceID,
						"run_id":     cfg.RunID,
						"step_index": turn,
						"tool_name":  tu.Name,
						"outcome":    "ai_ask_budget_guard_hard",
						"elapsed_ms": int64(0),
					})
					continue
				}
			}

			guard := CheckForLoop(loopGuardWindow, tu.Name, tu.Input)
			RecordToolCall(loopGuardWindow, tu.Name, tu.Input, guard.Hash)

			if guard.Kind == "soft" || guard.Kind == "hard" {
				// The input can be a private goal, file path or command, so the log
				// carries its size and hash only.
				log.Printf("[native-loop] turn=%d runID=%s tool=%s loop-guard=%s count=%d — skipping execution; blocked input_bytes=%d input_hash=%s",
					turn, cfg.RunID, tu.Name, guard.Kind, guard.Count, len(tu.Input), guard.Hash)
				synthetic := SyntheticLoopBlockResult(tu.ID, tu.Name, guard.Count)
				resultBlocks = append(resultBlocks, synthetic)
				// Count loop-guard-skipped calls in tools_used (model
				// intended to call the tool, system short-circuited).
				// Don't count as a failure — failure means tool *ran*
				// and returned is_error; this is a different category.
				if len(toolsUsed) < maxToolsUsedTracked {
					toolsUsed = append(toolsUsed, tu.Name)
				}
				if s, ok := synthetic.Content.(string); ok {
					cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: s, Failed: true})
				}
				// Loop-guard short-circuit emits step.tool.completed with
				// outcome=loop_guard_{soft,hard} so the dashboard can split
				// "tool actually ran" from "system blocked the call" — both
				// matter for harness health, but recommend different fixes.
				cfg.Telemetry.Capture("agentloop.step.tool.completed", map[string]any{
					"trace_id":   cfg.TraceID,
					"run_id":     cfg.RunID,
					"step_index": turn,
					"tool_name":  tu.Name,
					"outcome":    "loop_guard_" + guard.Kind,
					"elapsed_ms": int64(0),
				})
				if guard.Kind == "hard" {
					hardTrippedTool = tu.Name
					hardTrippedCount = guard.Count
					if guard.HardThreshold > 0 && guard.Count > guard.HardThreshold {
						loopGuardAbortMsg = fmt.Sprintf("loop guard stopped repeated %s call after %d identical attempts", tu.Name, guard.Count)
						loopGuardAbortErr = fmt.Errorf("%w: %s", ErrLoopGuardStuck, loopGuardAbortMsg)
					}
				}
				continue
			}

			variantGuard := CheckForNumericVariantLoop(numericVariantGuardCounts, tu.Name, tu.Input)
			if variantGuard.Kind == "hard" {
				log.Printf("[native-loop] turn=%d runID=%s tool=%s numeric-variant-loop count=%d — skipping execution",
					turn, cfg.RunID, tu.Name, variantGuard.Count)
				synthetic := SyntheticNumericVariantLoopBlockResult(tu.ID, variantGuard.Count)
				resultBlocks = append(resultBlocks, synthetic)
				if len(toolsUsed) < maxToolsUsedTracked {
					toolsUsed = append(toolsUsed, tu.Name)
				}
				if s, ok := synthetic.Content.(string); ok {
					cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: s, Failed: true})
				}
				numericVariantTrippedCount = variantGuard.Count
				numericVariantHardTrips[variantGuard.Family]++
				if numericVariantHardTrips[variantGuard.Family] > 1 {
					loopGuardAbortMsg = fmt.Sprintf("numeric variant loop guard stopped repeated %s numeric sweep after %d similar attempts", tu.Name, variantGuard.Count)
					loopGuardAbortErr = fmt.Errorf("%w: %s", ErrLoopGuardStuck, loopGuardAbortMsg)
				}
				continue
			}

			searchGuard := CheckForSearchNarrowingLoop(searchNarrowingGuardStates, tu.Name, tu.Input)
			if searchGuard.Kind == "hard" {
				log.Printf("[native-loop] turn=%d runID=%s tool=%s search-narrowing-loop count=%d filters=%d — skipping execution",
					turn, cfg.RunID, tu.Name, searchGuard.Count, searchGuard.FilterCount)
				synthetic := SyntheticSearchNarrowingLoopBlockResult(tu.ID, searchGuard.Count)
				resultBlocks = append(resultBlocks, synthetic)
				if len(toolsUsed) < maxToolsUsedTracked {
					toolsUsed = append(toolsUsed, tu.Name)
				}
				if s, ok := synthetic.Content.(string); ok {
					cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: s, Failed: true})
				}
				cfg.Telemetry.Capture("agentloop.step.tool.completed", map[string]any{
					"trace_id":   cfg.TraceID,
					"run_id":     cfg.RunID,
					"step_index": turn,
					"tool_name":  tu.Name,
					"outcome":    "search_narrowing_loop_guard_hard",
					"elapsed_ms": int64(0),
				})
				searchNarrowingTrippedCount = searchGuard.Count
				searchNarrowingHardTrips[searchGuard.Family]++
				if searchNarrowingHardTrips[searchGuard.Family] > 1 {
					loopGuardAbortMsg = fmt.Sprintf("search narrowing loop guard stopped repeated %s empty search after %d attempts", tu.Name, searchGuard.Count)
					loopGuardAbortErr = fmt.Errorf("%w: %s", ErrLoopGuardStuck, loopGuardAbortMsg)
				}
				continue
			}

			toolStart := time.Now()
			tu.publishToModel = true
			blk, outcome, err := executeToolUse(ctx, cfg, tu)
			toolElapsedMs := time.Since(toolStart).Milliseconds()
			if err != nil {
				// executeToolUse returns a non-nil err only on hard
				// failure paths the loop can't recover (ctx cancel,
				// permission Await failure that prevents user
				// interaction, etc.). Permission denials and tool
				// errors come back with err=nil and a specific outcome
				// so they don't abort the run.
				errProps := map[string]any{
					"trace_id":   cfg.TraceID,
					"run_id":     cfg.RunID,
					"step_index": turn,
					"tool_name":  tu.Name,
					"outcome":    outcome, // executeToolUse already chose the right label
					"elapsed_ms": toolElapsedMs,
				}
				applyErrorTelemetryProperties(errProps, err)
				cfg.Telemetry.Capture("agentloop.step.tool.completed", errProps)
				emitError(cfg, err.Error())
				return err
			}
			// First successful cua_driver result of the run: prepend the
			// open-window inventory so the model gets an opening map instead
			// of navigating by trial-and-error. The list_apps fetch is a call
			// like any other: it passes the run's authority (a run that may
			// not list apps never sees other apps' window titles), and a
			// refused or failed call gets no inventory.
			if outcome == toolOutcomeSuccess && !blk.IsError {
				blk = MaybeInjectInventory(cuaInventory, tu.Name, tu.Input, blk, func() string {
					return fetchCuaInventory(ctx, cfg)
				})
			}
			origSize := toolResultSizeBytes(blk)
			if truncateToolResult(&blk, tu.Name) {
				log.Printf("[native-loop] turn=%d runID=%s tool=%s tool_result truncated %d → %d bytes",
					turn, cfg.RunID, tu.Name, origSize, toolResultSizeBytes(blk))
			}
			resultBlocks = append(resultBlocks, blk)
			RecordNumericVariantResult(numericVariantGuardCounts, tu.Name, tu.Input, blk)
			RecordSearchNarrowingResult(searchNarrowingGuardStates, tu.Name, tu.Input, blk)
			// Nudge once per stuck streak, not every turn: cuaProgress.Nudged
			// stays set until RecordCuaProgress sees real progress (which
			// resets it), so a persistent loop doesn't spam the same nudge and
			// burn tokens. A fresh stall after progress re-arms it.
			if c := RecordCuaProgress(cuaProgress, tu.Name, tu.Input, blk); c >= CuaNoProgressThreshold && !cuaProgress.Nudged {
				cuaNoProgressTrippedCount = c
				cuaProgress.Nudged = true
			}
			// Per-run telemetry accumulators: chronological list of
			// every tool call (duplicates kept) + cumulative failure
			// counter per name. Capped at maxToolsUsedTracked to avoid
			// unbounded growth in runaway loops; failure counter has
			// no cap (small per-name keyed map).
			if len(toolsUsed) < maxToolsUsedTracked {
				toolsUsed = append(toolsUsed, tu.Name)
			}
			if blk.IsError {
				toolErrorStreaks[tu.Name]++
				toolFailuresByName[tu.Name]++
			} else {
				toolErrorStreaks[tu.Name] = 0
				toolErrorStreakTrips[tu.Name] = 0
			}
			// step.tool.completed for the executed path. outcome comes
			// directly from executeToolUse so permission_denied,
			// parse_error, registry_error, unknown_tool are kept
			// distinct from genuine tool_error — Phase 2 pattern
			// matching depends on this granularity.
			cfg.Telemetry.Capture("agentloop.step.tool.completed", map[string]any{
				"trace_id":          cfg.TraceID,
				"run_id":            cfg.RunID,
				"step_index":        turn,
				"tool_name":         tu.Name,
				"outcome":           outcome,
				"elapsed_ms":        toolElapsedMs,
				"result_size_bytes": toolResultSizeBytes(blk),
			})
		}
		if loopGuardAbortErr != nil {
			log.Printf("[native-loop] turn=%d runID=%s %s", turn, cfg.RunID, loopGuardAbortMsg)
			emitError(cfg, loopGuardAbortMsg)
			return loopGuardAbortErr
		}
		if nudgesUsed > 0 {
			log.Printf("[native-loop] turn=%d runID=%s tool progress observed; resetting silent-turn nudges from %d to 0",
				turn, cfg.RunID, nudgesUsed)
			nudgesUsed = 0
		}
		// Tool round succeeded — any consecutive max_tokens continuations
		// were chasing a stalled response, not a real progress series. Reset.
		continuationAttempts = 0

		// Streak guard: if any tool just hit the consecutive-failure
		// limit, attach a stop-nudge text block to the SAME user message
		// that carries the tool_results. We can't append a second user
		// message after the tool_results one — Anthropic requires
		// messages to alternate user/assistant strictly, and shipping
		// two user messages in a row is a spec violation that has been
		// tolerated by the current proxy / API but isn't safe to rely on.
		// One user message can carry both tool_result blocks and a
		// trailing text block, which the model will see as combined
		// guidance for the next turn.
		userContent := resultBlocks
		if name, streak, limit, tripped := pickToolErrorStreakOverLimit(toolErrorStreaks); tripped {
			toolErrorStreakTrips[name]++
			if toolErrorStreakTrips[name] > 1 {
				msg := fmt.Sprintf("tool error guard stopped repeated failed %s calls after %d consecutive errors", name, streak)
				log.Printf("[native-loop] turn=%d runID=%s %s", turn, cfg.RunID, msg)
				emitError(cfg, msg)
				return fmt.Errorf("%w: %s", ErrLoopGuardStuck, msg)
			}
			if programmaticResultOnly {
				log.Printf("[native-loop] turn=%d runID=%s tool=%s consecutive_errors=%d limit=%d; suppressing text stop nudge for programmatic tool_result-only response",
					turn, cfg.RunID, name, streak, limit)
			} else {
				log.Printf("[native-loop] turn=%d runID=%s tool=%s consecutive_errors=%d limit=%d; injecting stop nudge",
					turn, cfg.RunID, name, streak, limit)
				userContent = append(userContent, ContentBlock{
					Type: "text",
					Text: fmt.Sprintf("Notice: %d consecutive %s calls have failed. Stop calling %s now. "+
						"If you have enough information from earlier successful tool calls or general knowledge, write your final answer to the user as plain text. "+
						"If you do not have enough, briefly tell the user what you tried and why you stopped — do not call %s again in this task.",
						streak, name, name, name),
				})
				// Phase 0.5 telemetry: this is "tool orchestration" — the
				// most direct signal Phase 2 needs to flag unreliable tools.
				// A high stop_nudge_injected rate for tool X means X needs
				// review (better error messages, retries, or removal).
				cfg.Telemetry.Capture("agentloop.streak_guard.stop_nudge_injected", map[string]any{
					"trace_id":     cfg.TraceID,
					"run_id":       cfg.RunID,
					"step_index":   turn,
					"tool_name":    name,
					"streak":       streak,
					"streak_limit": limit,
				})
			}
			toolErrorStreaks[name] = 0
		}
		if programmaticToolCallingActive && !programmaticResultOnly && !directProgrammaticNudgeSent && directProgrammaticToolCalls >= programmaticNudgeAfter {
			log.Printf("[native-loop] turn=%d runID=%s direct_programmatic_tools=%d; injecting Programmatic batching nudge",
				turn, cfg.RunID, directProgrammaticToolCalls)
			userContent = append(userContent, ContentBlock{
				Type: "text",
				Text: programmaticBatchingNudgeText(directProgrammaticToolCalls, directProgrammaticToolCounts),
			})
			directProgrammaticNudgeSent = true
			cfg.Telemetry.Capture("agentloop.programmatic.direct_tool_nudge_injected", map[string]any{
				"trace_id":          cfg.TraceID,
				"run_id":            cfg.RunID,
				"step_index":        turn,
				"direct_tool_calls": directProgrammaticToolCalls,
				"tool_counts":       directProgrammaticToolCounts,
			})
		}
		if hardTrippedTool != "" {
			if programmaticResultOnly {
				log.Printf("[native-loop] turn=%d runID=%s tool=%s loop-guard hard-trip count=%d; suppressing text nudge for programmatic tool_result-only response",
					turn, cfg.RunID, hardTrippedTool, hardTrippedCount)
			} else {
				log.Printf("[native-loop] turn=%d runID=%s tool=%s loop-guard hard-trip count=%d; injecting stop nudge text",
					turn, cfg.RunID, hardTrippedTool, hardTrippedCount)
				userContent = append(userContent, ContentBlock{
					Type: "text",
					Text: HardLoopNudgeText(hardTrippedTool, hardTrippedCount),
				})
			}
		}
		if numericVariantTrippedCount > 0 {
			if programmaticResultOnly {
				log.Printf("[native-loop] turn=%d runID=%s numeric-variant-loop hard-trip count=%d; suppressing text nudge for programmatic tool_result-only response",
					turn, cfg.RunID, numericVariantTrippedCount)
			} else {
				log.Printf("[native-loop] turn=%d runID=%s numeric-variant-loop hard-trip count=%d; injecting stop nudge text",
					turn, cfg.RunID, numericVariantTrippedCount)
				userContent = append(userContent, ContentBlock{
					Type: "text",
					Text: NumericVariantLoopNudgeText(numericVariantTrippedCount),
				})
			}
		}
		if searchNarrowingTrippedCount > 0 {
			if programmaticResultOnly {
				log.Printf("[native-loop] turn=%d runID=%s search-narrowing-loop hard-trip count=%d; suppressing text nudge for programmatic tool_result-only response",
					turn, cfg.RunID, searchNarrowingTrippedCount)
			} else {
				log.Printf("[native-loop] turn=%d runID=%s search-narrowing-loop hard-trip count=%d; injecting stop nudge text",
					turn, cfg.RunID, searchNarrowingTrippedCount)
				userContent = append(userContent, ContentBlock{
					Type: "text",
					Text: SearchNarrowingLoopNudgeText(searchNarrowingTrippedCount),
				})
			}
		}
		if uiAutomationBudgetTrippedCount > 0 {
			if programmaticResultOnly {
				log.Printf("[native-loop] turn=%d runID=%s ui-automation-budget count=%d; suppressing text nudge for programmatic tool_result-only response",
					turn, cfg.RunID, uiAutomationBudgetTrippedCount)
			} else {
				log.Printf("[native-loop] turn=%d runID=%s ui-automation-budget count=%d; injecting stop nudge text",
					turn, cfg.RunID, uiAutomationBudgetTrippedCount)
				userContent = append(userContent, ContentBlock{
					Type: "text",
					Text: UIAutomationBudgetNudgeText(uiAutomationBudgetTrippedCount),
				})
				cfg.Telemetry.Capture("agentloop.ui_automation_budget.stop_nudge_injected", map[string]any{
					"trace_id":   cfg.TraceID,
					"run_id":     cfg.RunID,
					"step_index": turn,
					"count":      uiAutomationBudgetTrippedCount,
				})
			}
		}
		if aiAskBudgetTrippedCount > 0 {
			if programmaticResultOnly {
				log.Printf("[native-loop] turn=%d runID=%s ai-ask-budget count=%d; suppressing text nudge for programmatic tool_result-only response",
					turn, cfg.RunID, aiAskBudgetTrippedCount)
			} else {
				log.Printf("[native-loop] turn=%d runID=%s ai-ask-budget count=%d; injecting stop nudge text",
					turn, cfg.RunID, aiAskBudgetTrippedCount)
				userContent = append(userContent, ContentBlock{
					Type: "text",
					Text: AIAskBudgetNudgeText(aiAskBudgetTrippedCount),
				})
				cfg.Telemetry.Capture("agentloop.ai_ask_budget.stop_nudge_injected", map[string]any{
					"trace_id":   cfg.TraceID,
					"run_id":     cfg.RunID,
					"step_index": turn,
					"count":      aiAskBudgetTrippedCount,
				})
			}
		}
		if cuaNoProgressTrippedCount > 0 {
			if programmaticResultOnly {
				log.Printf("[native-loop] turn=%d runID=%s cua-no-progress count=%d; suppressing text nudge for programmatic tool_result-only response",
					turn, cfg.RunID, cuaNoProgressTrippedCount)
			} else {
				log.Printf("[native-loop] turn=%d runID=%s cua-no-progress count=%d; injecting stop nudge text",
					turn, cfg.RunID, cuaNoProgressTrippedCount)
				userContent = append(userContent, ContentBlock{
					Type: "text",
					Text: CuaNoProgressNudgeText(cuaNoProgressTrippedCount),
				})
				cfg.Telemetry.Capture("agentloop.cua_no_progress.stop_nudge_injected", map[string]any{
					"trace_id":   cfg.TraceID,
					"run_id":     cfg.RunID,
					"step_index": turn,
					"count":      cuaNoProgressTrippedCount,
				})
			}
		}
		messages = append(messages, Message{Role: "user", Content: userContent})

		// Roll the just-appended user turn (tool_results + any text /
		// nudge blocks) into runningTotalTokens BEFORE the L2 gate
		// below. Without this, a single big tool_result — typical
		// cua_driver screenshot + log output is ~200k chars — slips
		// past the gate because runningTotalTokens still reflects the
		// previous turn's upstream-reported usage. Real-world symptom:
		// `agentloop.step.llm.error` with body
		// "exceeded model token limit: 262144 (requested: 286049)" —
		// observed in the error_patterns table even though
		// ContextLimitForModel correctly returns 262144 via the
		// YANSU_CONTEXT_WINDOW_OVERRIDE env var injected by
		// wails-gui/auth.go::injectSub2APIEnv. The cap was right; the
		// estimator was a lower bound that didn't roll up THIS turn's
		// add. EstimateUserContentTokens fills that gap.
		runningTotalTokens += EstimateUserContentTokens(userContent)

		// Auto-compact at the clean turn boundary (after tool_results
		// are appended and before the next assistant turn). Pairing
		// invariants hold here, which the summarise / fallback paths
		// both rely on.
		if runningTotalTokens >= autoCompactLimit {
			cfg.Emit(LoopEvent{
				Type:    "compact_start",
				Content: "Compacting conversation history to fit the context window.",
			})
			// Phase 0.5 telemetry: auto-compact is one of the four
			// self-evolution dimensions called out in the harness design
			// (context strategy). The dashboard needs to know how often
			// users hit the context budget, how expensive the compact
			// itself is (it embeds another LLM call internally), and how
			// often summarisation fails and falls back to the lossy
			// drop-oldest-half path. Three events: started / completed /
			// fallback_dropped_oldest_half — kept distinct so HogQL
			// aggregations can compute "compact attempt rate" vs
			// "compact success rate" separately. runAutoCompact's
			// internal LLM request goes through cfg.Client (same
			// telemetry instance via the loop's HTTPClient wiring), so
			// the embedded call already emits its own step.llm.* pair —
			// no need to double-capture token usage here.
			messagesBefore := len(messages)
			cfg.Telemetry.Capture("agentloop.context.compact.started", map[string]any{
				"trace_id":          cfg.TraceID,
				"run_id":            cfg.RunID,
				"step_index":        turn,
				"input_token_count": runningTotalTokens,
				"compact_limit":     autoCompactLimit,
				"messages_count":    messagesBefore,
			})
			compactStart := time.Now()
			compactRes, compactErr := runAutoCompact(ctx, cfg.Client, cfg.ModelID, messages)
			if ctx.Err() != nil {
				// User cancelled mid-compaction: bail silently instead of
				// emitting a misleading compact_error (mirrors the send-error
				// path's ctx-cancel handling).
				return ctx.Err()
			}
			compactElapsedMs := time.Since(compactStart).Milliseconds()
			if compactErr != nil || compactRes == nil {
				log.Printf("[native-loop] runID=%s auto-compact failed: %v; falling back to drop-oldest-half", cfg.RunID, errorDiagnostic(compactErr))
				messages = dropOldestHalf(messages)
				cfg.Emit(LoopEvent{
					Type:  "compact_error",
					Error: fmt.Sprintf("summarisation failed (%v); dropped oldest half of history", compactErr),
				})
				// The fallback is a *lossy* degradation — old turns are
				// thrown away rather than summarised. Track separately so
				// dashboards can alert when the fallback rate climbs
				// (indicates the summariser prompt or model needs work).
				fallbackProps := map[string]any{
					"trace_id":   cfg.TraceID,
					"run_id":     cfg.RunID,
					"step_index": turn,
					"elapsed_ms": compactElapsedMs,
					"dropped":    messagesBefore - len(messages),
				}
				if compactErr != nil {
					applyErrorTelemetryProperties(fallbackProps, compactErr)
				} else {
					fallbackProps["error_kind"] = "nil_result"
				}
				cfg.Telemetry.Capture("agentloop.context.compact.fallback_dropped_oldest_half", fallbackProps)
			} else {
				messages = compactRes.NewMessages
				log.Printf("[native-loop] runID=%s auto-compact ok: summary=%d chars preserved=%d",
					cfg.RunID, len(compactRes.SummaryText), compactRes.PreservedCount)
				cfg.Emit(LoopEvent{
					Type:    "compact_end",
					Content: compactRes.SummaryText,
				})
				cfg.Telemetry.Capture("agentloop.context.compact.completed", map[string]any{
					"trace_id":        cfg.TraceID,
					"run_id":          cfg.RunID,
					"step_index":      turn,
					"elapsed_ms":      compactElapsedMs,
					"summary_chars":   len(compactRes.SummaryText),
					"preserved_count": compactRes.PreservedCount,
					"messages_before": messagesBefore,
					"messages_after":  len(compactRes.NewMessages),
				})
			}
			// Reset the count: post-compact we're well below threshold
			// and the next turn's usage field will repopulate accurately.
			runningTotalTokens = 0
			// Whichever branch ran (success or fallback), the next LLM
			// call is happening on a freshly reshaped message slice —
			// label it so dashboards can separate "first attempt with
			// fresh context" latency from "regular attempt" latency
			// (compact is typically followed by a slow turn because
			// cache is cold).
			nextStepPhase = "post_compact"
		}

		// Plan-mode state change: if ExitPlanMode was granted the
		// PlanMode flag is now off and next turn uses the full tool set.
		planModeWasActive := planModeActive
		planModeActive = cfg.PlanMode != nil && cfg.PlanMode.Active()
		if planModeWasActive && !planModeActive {
			// Phase 0.5 telemetry: a plan_mode exit on this turn means
			// the model called ExitPlanMode and the user / runtime
			// granted it. Paired with plan_mode.entered, this lets the
			// dashboard measure "plan-then-execute" pattern adoption.
			cfg.Telemetry.Capture("agentloop.plan_mode.exited", map[string]any{
				"trace_id":   cfg.TraceID,
				"run_id":     cfg.RunID,
				"step_index": turn,
				"trigger":    "exit_plan_mode_tool",
			})
		}
		systemBlocks = BuildSystemBlocks(cfg.SystemText, planModeActive, cfg.WorkDir)
		if planModeActive {
			allowed := map[string]bool{"Read": true, "Grep": true, "Glob": true, "TodoWrite": true, "ExitPlanMode": true, "WebFetch": true, "WebSearch": true}
			filteredDefs := toolDefs[:0]
			for _, d := range toolDefs {
				if allowed[d.Name] {
					filteredDefs = append(filteredDefs, d)
				}
			}
			toolDefs = filteredDefs
		}
	}
	emitError(cfg, fmt.Sprintf("max turns reached (%d); ending the task", MaxTurns))
	cfg.Emit(LoopEvent{Type: "done", Usage: copyUsage(totalUsage)})
	return fmt.Errorf("max turns reached")
}

type turnResult struct {
	AssistantBlocks []ContentBlock
	ToolUses        []toolUseReq
	Usage           *Usage
	// StopReason is the API-reported reason the turn ended. Common values:
	// "end_turn" (natural completion), "tool_use" (model wants to call
	// tools), "max_tokens" (response truncated at the output cap),
	// "stop_sequence", "refusal". Surfacing this lets the outer loop
	// distinguish "model finished naturally" from "model bailed
	// silently" — the latter needs a recovery nudge (see loopMain).
	StopReason  string
	ContainerID string
}

type toolUseReq struct {
	publishToModel bool
	ID             string
	Name           string
	Input          string // raw partial_json accumulated from deltas
	Caller         *ToolCaller
}

func runOneTurn(ctx context.Context, cfg *LoopConfig, req *Request) (*turnResult, error) {
	var (
		// assistantBlocks must be a non-nil slice so it marshals as [] rather
		// than null when the model produces a completely empty turn (no text,
		// no thinking, no tool_use). The Anthropic API requires content to be
		// a valid list; null causes a 400 on the next request that includes
		// this silent assistant message in the history.
		assistantBlocks = []ContentBlock{}
		toolUses        []toolUseReq
		usage           *Usage
		stopReason      string
		containerID     string
	)
	// Per-block accumulators.
	currentText := &strings.Builder{}
	currentThinking := &strings.Builder{}
	currentThinkingSignature := &strings.Builder{}
	blockJSON := map[int]*strings.Builder{}
	toolBlocks := map[int]*ContentBlock{}
	preservedBlocks := map[int]*ContentBlock{}

	flushText := func() {
		if currentText.Len() > 0 {
			assistantBlocks = append(assistantBlocks, ContentBlock{Type: "text", Text: currentText.String()})
			currentText.Reset()
		}
	}
	flushThinking := func() {
		if currentThinking.Len() > 0 {
			if currentThinkingSignature.Len() > 0 {
				assistantBlocks = append(assistantBlocks, ContentBlock{
					Type:      "thinking",
					Thinking:  currentThinking.String(),
					Signature: currentThinkingSignature.String(),
				})
			}
			currentThinking.Reset()
			currentThinkingSignature.Reset()
		}
	}

	err := cfg.Client.Stream(ctx, req, func(ev *StreamEvent) error {
		switch ev.Type {
		case "message_start":
			if ev.Message != nil && ev.Message.Usage != nil {
				usage = ev.Message.Usage
			}
			if ev.Message != nil && ev.Message.Container != nil && ev.Message.Container.ID != "" {
				containerID = ev.Message.Container.ID
			}
			if ev.Container != nil && ev.Container.ID != "" {
				containerID = ev.Container.ID
			}
		case "content_block_start":
			if ev.ContentBlock == nil {
				return nil
			}
			switch ev.ContentBlock.Type {
			case "tool_use":
				blockJSON[ev.Index] = &strings.Builder{}
				b := *ev.ContentBlock
				// Source-of-truth defense against the sub2api / Kimi K2
				// proxy bug where content_block_start arrives with an
				// empty `id`. OpenAI-streaming tool_calls only carry the
				// id on the first delta; converters that pick the wrong
				// delta (or miss it entirely) leak "" here. An empty id
				// would propagate into assistantBlocks, then into the
				// next request's tool_use ↔ tool_result pair, and 400
				// the very next turn with the signature
				// `tool_call_id  is not found` (double space — the
				// template substituted ""). Synthesize a fallback so
				// downstream code, persistence and the next request all
				// see a non-empty id.
				if b.ID == "" {
					b.ID = synthToolUseID()
					log.Printf("[native-loop] runID=%s upstream content_block_start tool_use had empty id; synthesized id=%s tool=%s — likely sub2api/Kimi proxy bug",
						cfg.RunID, b.ID, b.Name)
					// Fire a PostHog event whenever we have to synthesize an
					// id. This is the only structural signal we have that
					// "upstream returned an empty tool_use id." Lets us
					// quantify how often the sub2api/Kimi proxy chain leaks
					// the bug vs. how often the matching 400 (`tool_call_id
					//  is not found`, double-space template signature) is
					// caused by something else entirely (server-side
					// previous_response_id cache state). If empty-id
					// observations correlate 1:1 with tool_call_id-not-found
					// errors per trace, the bug is in our message slice and
					// the synth fallback fixes it. If errors happen without
					// any matching observation, the bug is in the upstream
					// cache and we need sub2api/Kimi to fix it.
					if cfg.Telemetry != nil {
						cfg.Telemetry.Capture("agentloop.upstream_empty_tool_use_id", map[string]any{
							"trace_id":    cfg.TraceID,
							"run_id":      cfg.RunID,
							"tool_name":   b.Name,
							"synthesized": b.ID,
							"block_index": ev.Index,
							"model_id":    cfg.ModelID,
						})
					}
				}
				toolBlocks[ev.Index] = &b
				// The full `tool_use` event is deferred to content_block_stop
				// so persistence/checkpoint/generator-log consumers see the
				// parsed input (a previous early emit left tool_use rows in
				// the DB with an empty ToolInput, which then 400'd on history
				// reload because the Anthropic API requires tool_use.input
				// to be a JSON object).
				//
				// We do, however, emit a UI-only `tool_use_start` event
				// here so the chat surface can immediately render a
				// placeholder chip for the upcoming call. Without this,
				// the gap between "thinking ends" and "tool_use parsed"
				// (which is the entire input-JSON streaming window) shows
				// no movement in the UI, making fast tool runs look like
				// they jump straight from "nothing" to "Ran command" —
				// see wails-gui frontend tool-group-row.tsx for how this
				// is consumed. tool_use_start is explicitly NOT persisted
				// by the wails-gui bridge.
				cfg.Emit(LoopEvent{
					Type:      "tool_use_start",
					ToolName:  b.Name,
					ToolUseID: b.ID,
				})
			case "server_tool_use", "code_execution_tool_result":
				b := *ev.ContentBlock
				preservedBlocks[ev.Index] = &b
				blockJSON[ev.Index] = &strings.Builder{}
			case "text":
				// text block start; we'll accumulate deltas.
			case "thinking":
				// thinking block start; handled in deltas.
			default:
				// Unrecognised block type from a newer API surface
				// (redacted_thinking, mcp_tool_use, web_search_tool_result,
				// ...). Preserve it in the assistant message so newer API
				// protocol blocks can be replayed on the next request.
				log.Printf("[native-loop] unknown content_block type=%q index=%d — preserved", ev.ContentBlock.Type, ev.Index)
				b := *ev.ContentBlock
				preservedBlocks[ev.Index] = &b
				blockJSON[ev.Index] = &strings.Builder{}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				return nil
			}
			switch ev.Delta.Type {
			case "text_delta":
				currentText.WriteString(ev.Delta.Text)
				cfg.Emit(LoopEvent{Type: "assistant", Content: ev.Delta.Text})
			case "thinking_delta":
				currentThinking.WriteString(ev.Delta.Thinking)
				cfg.Emit(LoopEvent{Type: "thinking", Content: ev.Delta.Thinking})
			case "input_json_delta":
				if b, ok := blockJSON[ev.Index]; ok {
					b.WriteString(ev.Delta.PartialJSON)
				}
			case "signature_delta":
				currentThinkingSignature.WriteString(ev.Delta.Signature)
			default:
				// New delta variant emitted by a block type we don't
				// know about. Pair with the unknown-block log above
				// so a single failed run shows both the block type
				// and the delta shape.
				log.Printf("[native-loop] unknown content_block delta type=%q index=%d — ignored", ev.Delta.Type, ev.Index)
			}
		case "content_block_stop":
			// When a tool_use block finishes, parse the accumulated JSON.
			if b, ok := toolBlocks[ev.Index]; ok {
				flushText()
				flushThinking()
				rawJSON := ""
				if tj, ok := blockJSON[ev.Index]; ok {
					rawJSON = tj.String()
				}
				tu := toolUseReq{ID: b.ID, Name: b.Name, Input: rawJSON, Caller: b.Caller}
				toolUses = append(toolUses, tu)
				var parsed any
				if rawJSON != "" {
					if pr, err := Parse(rawJSON, ParseOptions{}); err == nil {
						parsed = pr.Value
					}
				}
				if parsed == nil {
					parsed = map[string]any{}
				}
				b.Input = parsed
				assistantBlocks = append(assistantBlocks, *b)
				cfg.Emit(LoopEvent{
					Type:      "tool_use",
					ToolName:  b.Name,
					ToolUseID: b.ID,
					ToolInput: parsed,
				})
			} else if b, ok := preservedBlocks[ev.Index]; ok {
				flushText()
				flushThinking()
				if bj, ok := blockJSON[ev.Index]; ok && bj.Len() > 0 {
					if pr, err := Parse(bj.String(), ParseOptions{}); err == nil {
						b.Input = pr.Value
					}
				}
				assistantBlocks = append(assistantBlocks, *b)
			} else {
				// A text or thinking block just ended — flush the
				// matching accumulator so tool_use blocks append in
				// proper order.
				flushText()
				flushThinking()
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				if usage == nil {
					usage = &Usage{}
				}
				usage.InputTokens += ev.Usage.InputTokens
				usage.OutputTokens += ev.Usage.OutputTokens
				usage.CacheReadInputTokens += ev.Usage.CacheReadInputTokens
				usage.CacheCreationInputTokens += ev.Usage.CacheCreationInputTokens
			}
		case "message_stop":
			// End of stream for this turn.
		case "error":
			if ev.Error != nil {
				return fmt.Errorf("stream error: %s", ev.Error.Message)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	flushText()
	flushThinking()

	return &turnResult{
		AssistantBlocks: assistantBlocks,
		ToolUses:        toolUses,
		Usage:           usage,
		StopReason:      stopReason,
		ContainerID:     containerID,
	}, nil
}

// turnHasText reports whether the assistant produced at least one
// non-empty text block this turn. Pure-thinking turns with no text
// and no tool uses are the silent-give-up case the outer loop must
// recover from (see the nudge path in the loop body).
func turnHasText(blocks []ContentBlock) bool {
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// describeAssistantBlocks renders a compact "type=size" summary for
// each block in the assistant message. Used by the silent-turn
// diagnostic in loopMain. For text/thinking, size is character count;
// for tool_use, it's the input field's JSON length when present.
func describeAssistantBlocks(blocks []ContentBlock) string {
	if len(blocks) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(blocks))
	for i, b := range blocks {
		size := 0
		switch b.Type {
		case "text":
			size = len(b.Text)
		case "thinking":
			size = len(b.Thinking)
		case "tool_use":
			if raw, err := json.Marshal(b.Input); err == nil {
				size = len(raw)
			}
		}
		parts = append(parts, fmt.Sprintf("%d:%s=%d", i, b.Type, size))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// Step-level outcome enum returned by executeToolUse to the loop's
// telemetry layer. Phase 0.5 added the fine-grained labels so the
// dashboard can distinguish "Bash genuinely failed" from "user denied
// permission" — both used to land as tool_error which masked a key
// pattern: the most-denied tools are candidates for auto-trust or UX
// improvement. Stable additive contract; new outcomes only get added.
const (
	toolOutcomeSuccess          = "success"
	toolOutcomeToolError        = "tool_error"
	toolOutcomeUnknownTool      = "unknown_tool"
	toolOutcomeParseError       = "parse_error"
	toolOutcomePermissionDenied = "permission_denied"
	toolOutcomePermissionError  = "permission_error"
	toolOutcomeRegistryError    = "registry_error"
)

func executeToolUse(ctx context.Context, cfg *LoopConfig, tu toolUseReq) (ContentBlock, string, error) {
	// Advertising no tools is not an execution boundary: a provider can still
	// return a tool_use block. Deny before registry lookup, permission checks,
	// and every synchronous tool, including tools that ignore cancellation.
	if cfg.NoTools {
		err := errors.New("tool execution is disabled for this run")
		return errorResult(tu.ID, err.Error()), toolOutcomePermissionDenied, err
	}
	if err := ctx.Err(); err != nil {
		return errorResult(tu.ID, err.Error()), toolOutcomePermissionDenied, err
	}
	tool := cfg.Registry.Get(tu.Name)
	if tool == nil {
		return errorResult(tu.ID, fmt.Sprintf("unknown tool: %s", tu.Name)), toolOutcomeUnknownTool, nil
	}

	parsed, parseErr := parseToolInput(tu.Input)
	warnings := []string{}
	if parsed.Warning != "" {
		warnings = append(warnings, parsed.Warning)
	}

	// Unattended-session deny gate. Checked BEFORE the permission gate so
	// neither AutoApproveTools nor session trust can bypass it.
	if len(cfg.DeniedBashPrefixes) > 0 && tu.Name == "Bash" {
		if cmd, ok := parsed.Input["command"].(string); ok {
			if denied := matchDeniedBashPrefix(cmd, cfg.DeniedBashPrefixes); denied != "" {
				msg := fmt.Sprintf("Blocked: %q matches the denied command prefix %q. "+
					"This unattended session cannot perform externally-visible write operations; "+
					"the user must run this themselves.", cmd, denied)
				cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: msg, Failed: true})
				return errorResult(tu.ID, msg), toolOutcomePermissionDenied, nil
			}
		}
	}

	// The app data folder is off limits before any approval is asked for.
	if err := tools.CheckProtectedPaths(tu.Name, parsed.Input, cfg.WorkDir); err != nil {
		cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: err.Error(), Failed: true})
		return errorResult(tu.ID, err.Error()), toolOutcomePermissionDenied, nil
	}

	authorityEvaluation := evaluateToolAuthority(ctx, cfg, tool, parsed.Input)
	// Attended chats run Bash without asking unless the user turned on "Ask
	// before running risky commands". The hard blocks above (CheckProtectedPaths
	// and, inside Execute, guard.RiskBlocked) already refused the commands that
	// are off limits in every mode, so downgrading the remaining require-approval
	// decision to allow only removes the prompt, never a block. Unattended runs
	// (AutoApproveTools) are never downgraded: they keep failing closed.
	tools.ApplyBashApprovalPreference(&authorityEvaluation, !cfg.AutoApproveTools && !cfg.AskBeforeRiskyCommands)
	emitAuthorityEvaluation(cfg, tu.ID, authorityEvaluation)
	switch authorityEvaluation.Decision {
	case authority.DecisionDeny:
		msg := fmt.Sprintf(
			"Blocked: this run is not allowed to %s (%s: %s). Tell the user plainly what you could not do.",
			authority.Describe(authorityEvaluation.Action),
			authorityEvaluation.Action.Capability,
			authorityEvaluation.Reason,
		)
		cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: msg, Failed: true})
		return errorResult(tu.ID, msg), toolOutcomePermissionDenied, nil
	case authority.DecisionRequireApproval:
		if cfg.AutoApproveTools {
			msg := fmt.Sprintf(
				"Blocked: this run is not allowed to %s without the user's approval, and nobody is watching it to approve (%s). Tell the user plainly what you could not do.",
				authority.Describe(authorityEvaluation.Action),
				authorityEvaluation.Action.Capability,
			)
			cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: msg, Failed: true})
			return errorResult(tu.ID, msg), toolOutcomePermissionDenied, nil
		}
	}

	// Permission gate combines semantic authority with the tool's legacy
	// confirmation contract. AutoApproveTools may bypass only a legacy prompt;
	// it never turns an authority require-approval decision into consent.
	needs, prompt := tool.NeedsPermission(parsed.Input)
	authorityNeedsApproval := authorityEvaluation.Decision == authority.DecisionRequireApproval
	if authorityNeedsApproval {
		needs = true
		if strings.TrimSpace(prompt.Reason) == "" {
			prompt = authorityPermissionPrompt(authorityEvaluation.Action)
		}
	}
	// An unattended action already allowed by authority skips only its
	// legacy prompt. No UI consent or session-trust entry is synthesized.
	if needs && cfg.AutoApproveTools && !authorityNeedsApproval {
		needs = false
	}
	// Session-scoped trust ("Allow for this chat") short-circuits the
	// per-call confirmation. We honour it even when the underlying tool
	// still reports NeedsPermission=true because the user has already
	// granted blanket consent for the prompt prefix in this session.
	authorityApproved := authorityEvaluation.Decision == authority.DecisionAllow
	trustTarget := tu.Name
	if authorityNeedsApproval && strings.TrimSpace(prompt.Prefix) != "" {
		trustTarget = prompt.Prefix
	}
	if needs && cfg.SessionTrust != nil && cfg.SessionTrust.IsAllowed(cfg.SessionID, trustTarget) {
		needs = false
		authorityApproved = true
	}
	if needs && cfg.Permission != nil {
		cfg.Emit(LoopEvent{
			Type:              "permission_request",
			ToolName:          tu.Name,
			ToolUseID:         tu.ID,
			ToolInput:         parsed.Input,
			Content:           prompt.Reason,
			PermissionOptions: prompt.Options,
			PermissionPrefix:  prompt.Prefix,
		})
		res, err := cfg.Permission.Await(ctx, cfg.SessionID, 0)
		if err != nil {
			msg := fmt.Sprintf("permission await: %v", err)
			cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: msg, Failed: true})
			return errorResult(tu.ID, msg), toolOutcomePermissionError, err
		}
		if !res.Granted {
			msg := "User rejected this tool invocation"
			cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: msg, Failed: true})
			return errorResult(tu.ID, msg), toolOutcomePermissionDenied, nil
		}
		authorityApproved = true
		// Record session-scoped trust before invoking persistent-allow,
		// so even if the OnPersistentAllow callback is slow / fails the
		// current session is still covered for subsequent calls.
		if res.SessionAlways && cfg.SessionTrust != nil {
			cfg.SessionTrust.Allow(cfg.SessionID, prompt.Prefix)
		}
		if res.Always && cfg.OnPersistentAllow != nil && prompt.Prefix != "" {
			cfg.OnPersistentAllow(prompt.Prefix)
		}
	}
	if needs && cfg.Permission == nil {
		msg := fmt.Sprintf(
			"Blocked: this run needs the user's approval to %s, but it cannot ask for it (%s). Tell the user plainly what you could not do.",
			authority.Describe(authorityEvaluation.Action),
			authorityEvaluation.Action.Capability,
		)
		cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: msg, Failed: true})
		return errorResult(tu.ID, msg), toolOutcomePermissionDenied, nil
	}

	if parseErr != nil {
		msg := fmt.Sprintf("Your tool_use input was not valid JSON: %s. Please re-emit the call with properly formatted JSON.", parseErr.Error())
		cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: msg, Failed: true})
		return errorResult(tu.ID, msg), toolOutcomeParseError, nil
	}

	call := tools.ToolCall{
		WorkDir: cfg.WorkDir,
		Input:   parsed.Input,
		ID:      tu.ID,
		Authority: &tools.AuthorityRuntime{
			Context:                  authorityEvaluation.Context,
			Evaluator:                cfg.AuthorityEvaluator,
			Evaluation:               &authorityEvaluation,
			Approved:                 authorityApproved,
			Recorded:                 true,
			DeniedBashPrefixes:       cfg.DeniedBashPrefixes,
			AllowBashWithoutApproval: !cfg.AutoApproveTools && !cfg.AskBeforeRiskyCommands,
			Record: func(evaluation authority.Evaluation) {
				emitAuthorityEvaluation(cfg, tu.ID, evaluation)
			},
		},
	}
	if cfg.BeforeToolExecution != nil {
		if err := cfg.BeforeToolExecution(tu.Name, call); err != nil {
			cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: err.Error(), Failed: true})
			return errorResult(tu.ID, err.Error()), toolOutcomeRegistryError, err
		}
	}
	result, err := cfg.Registry.Execute(ctx, tu.Name, call)
	if err != nil {
		cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: err.Error(), Failed: true})
		return errorResult(tu.ID, err.Error()), toolOutcomeRegistryError, nil
	}

	// MCP checks the same source fence in its transport. For direct model
	// reads, finish the check before publishing either success event or text.
	if reader, ok := cfg.Registry.Get(tu.Name).(tools.SourceReadTool); tu.publishToModel && ok && !result.IsError {
		release, err := reader.LockResultPublication(ctx, call)
		if err != nil {
			result = tools.ToolResult{IsError: true, Content: "Isoform source context changed before publication. Retry with a new call ID."}
		} else {
			release()
		}
	}

	// Emit tool_result event; the payload shape in the event is a
	// JSON-encoded string for the wails-gui bridge to pick up.
	payload := result.Content
	if len(result.Warnings) > 0 {
		payload += "\n[warnings: " + strings.Join(result.Warnings, "; ") + "]"
	}
	cfg.Emit(LoopEvent{Type: "tool_result", ToolName: tu.Name, ToolUseID: tu.ID, Content: payload, ToolSucceeded: !result.IsError, Failed: toolResultFailed(result)})

	// Plan-mode hand-off: ExitPlanMode being granted flips the flag off.
	if tu.Name == "ExitPlanMode" && cfg.PlanMode != nil {
		cfg.PlanMode.Set(false)
		cfg.Emit(LoopEvent{Type: "plan_complete", Content: result.Content})
	}

	outcome := toolOutcomeSuccess
	if result.IsError {
		outcome = toolOutcomeToolError
	}
	// Multi-block results (e.g. Read-image) are returned as-is.
	if len(result.Blocks) > 0 {
		return ContentBlock{Type: "tool_result", ToolUseID: tu.ID, Content: result.Blocks, IsError: result.IsError}, outcome, nil
	}
	return ContentBlock{Type: "tool_result", ToolUseID: tu.ID, Content: payload, IsError: result.IsError}, outcome, nil
}

func evaluateToolAuthority(
	ctx context.Context,
	cfg *LoopConfig,
	tool tools.Tool,
	input map[string]any,
) authority.Evaluation {
	evaluator := cfg.AuthorityEvaluator
	if evaluator == nil {
		run := cfg.AuthorityContext
		run.RunID = cfg.RunID
		run.SessionID = cfg.SessionID
		run.Unattended = cfg.AutoApproveTools
		if run.Mode == authority.ExecutionModeShadow {
			return authority.NewPolicy(nil).Evaluate(
				ctx,
				run,
				tools.DescribeAuthority(tool, input, cfg.WorkDir),
			)
		}
		return authority.Evaluation{
			PolicyVersion: authority.PolicyVersion,
			Decision:      authority.DecisionAllow,
			Reason:        "authority evaluator not configured",
			Context:       run,
			Action:        tools.DescribeAuthority(tool, input, cfg.WorkDir),
		}
	}
	run := cfg.AuthorityContext
	run.RunID = cfg.RunID
	run.SessionID = cfg.SessionID
	run.Unattended = cfg.AutoApproveTools
	return evaluator.Evaluate(ctx, run, tools.DescribeAuthority(tool, input, cfg.WorkDir))
}

func authorityPermissionPrompt(action authority.Action) tools.PermissionPrompt {
	reason := strings.TrimSpace(action.ApprovalReason)
	if reason == "" {
		reason = fmt.Sprintf("Allow %s?", action.Capability)
	}
	prefix := strings.TrimSpace(action.ApprovalPrefix)
	if prefix == "" {
		prefix = action.ToolName
	}
	return tools.PermissionPrompt{
		Reason: reason,
		Prefix: prefix,
		Options: []tools.PermissionOption{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
			{ID: "allow_session", Name: "Allow for this chat", Kind: "allow_session"},
			{ID: "reject_once", Name: "Reject", Kind: "reject_once"},
		},
	}
}

func emitAuthorityEvaluation(cfg *LoopConfig, toolUseID string, evaluation authority.Evaluation) {
	if cfg == nil || cfg.Emit == nil {
		return
	}
	if evaluation.ToolUseID == "" {
		evaluation.ToolUseID = toolUseID
	}
	if cfg.AuthorityRecorder != nil {
		cfg.AuthorityRecorder(evaluation)
	}
	content, err := json.Marshal(evaluation)
	if err != nil {
		content = []byte(fmt.Sprintf(
			`{"policy_version":%q,"decision":%q,"reason":"authority evaluation could not be encoded"}`,
			authority.PolicyVersion,
			evaluation.Decision,
		))
	}
	cfg.Emit(LoopEvent{
		Type:      "authority_decision",
		Content:   string(content),
		ToolName:  evaluation.Action.ToolName,
		ToolInput: evaluation.Action,
		ToolUseID: toolUseID,
	})
}

// pickKeyOverLimit returns the first key in counts whose value has
// reached limit. Used by two guards in this file: the per-tool
// consecutive-error streak (key = tool name) and the per-(tool, input)
// cumulative same-input counter (key = "toolName|input"). Map iteration
// order is randomised in Go, so when two keys trip on the same turn we
// pick one non-deterministically — that's fine, the next turn will
// trip the other if it keeps misbehaving. Returns tripped=false when
// no key is over the limit.
func pickKeyOverLimit(counts map[string]int, limit int) (key string, count int, tripped bool) {
	for k, c := range counts {
		if c >= limit {
			return k, c, true
		}
	}
	return "", 0, false
}

func pickToolErrorStreakOverLimit(counts map[string]int) (key string, count int, limit int, tripped bool) {
	for k, c := range counts {
		limit := toolErrorStreakLimitForTool(k)
		if c >= limit {
			return k, c, limit, true
		}
	}
	return "", 0, 0, false
}

// truncationSplit returns (headBytes, tailBytes) for the given tool.
// Tail-heavy tools (Bash, BashOutput, Programmatic) put errors and
// final results at the end; head-heavy tools (WebSearch, WebFetch,
// Read, Grep, Glob) put titles and structured data at the beginning.
func truncationSplit(toolName string) (int, int) {
	switch toolName {
	case "Bash", "BashOutput", "Programmatic":
		return 4 * 1024, toolResultKeptBytes - 4*1024
	case "WebSearch", "WebFetch", "Read", "Grep", "Glob":
		return toolResultKeptBytes - 4*1024, 4 * 1024
	default:
		half := toolResultKeptBytes / 2
		return half, toolResultKeptBytes - half
	}
}

// truncateToolResult caps an oversized tool_result block's string
// content to toolResultMaxBytes, keeping the head and tail so the
// model retains context. The head/tail ratio is tool-type-aware:
// command output (Bash) keeps more tail (errors at end), while
// search/read results keep more head (titles/summaries at start).
// Multi-block results ([]ContentBlock, e.g. image+text) are left
// untouched — the image-budget pass handles those separately.
// Returns true when truncation occurred.
func truncateToolResult(blk *ContentBlock, toolName string) bool {
	s, ok := blk.Content.(string)
	if !ok || len(s) <= toolResultMaxBytes {
		return false
	}
	origLen := len(s)
	headBytes, tailBytes := truncationSplit(toolName)
	head := s[:headBytes]
	tail := s[len(s)-tailBytes:]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	notice := fmt.Sprintf(
		"\n\n[tool_result truncated: %d bytes from %s — kept first %d + last %d bytes; re-run the tool with a narrower query if you need the middle]\n\n",
		origLen, toolName, len(head), len(tail),
	)
	blk.Content = head + notice + tail
	return true
}

func toolErrorStreakLimitForTool(name string) int {
	switch name {
	case "cua_driver":
		return 3
	default:
		return toolErrorStreakLimit
	}
}

// truncateForLog clips s to at most max bytes for log output, appending a
// marker when truncated. Used to surface (bounded) tool inputs in diagnostics.
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

type parsedInput struct {
	Input   map[string]any
	Warning string
}

// matchDeniedBashPrefix reports the first denied prefix that a bash command
// matches, or "". It delegates to tools.DeniedBashCommandMatch, which parses
// the whole script with a real shell parser and unwraps wrappers, so chained,
// newline-separated, substituted and `sh -c`-wrapped forms are all caught, not
// just a first-word match on each ;|& segment.
func matchDeniedBashPrefix(command string, prefixes []string) string {
	return tools.DeniedBashCommandMatch(command, prefixes)
}

func parseToolInput(raw string) (parsedInput, error) {
	if strings.TrimSpace(raw) == "" {
		return parsedInput{Input: map[string]any{}}, nil
	}
	pr, err := Parse(raw, ParseOptions{})
	if err != nil {
		return parsedInput{}, err
	}
	m, ok := pr.Value.(map[string]any)
	if !ok {
		return parsedInput{}, fmt.Errorf("tool input must decode to a JSON object, got %T", pr.Value)
	}
	return parsedInput{Input: m, Warning: pr.Warning}, nil
}

func errorResult(id, msg string) ContentBlock {
	return ContentBlock{
		Type:      "tool_result",
		ToolUseID: id,
		Content:   msg,
		IsError:   true,
	}
}
