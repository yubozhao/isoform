package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"yansu-agent/agent/authority"
	"yansu-agent/agent/native"
	"yansu-agent/agent/toolserver"
)

var ErrShadowRuntimeRequiresNative = errors.New("shadow runtime requires the native policy-controlled runner")
var ErrAuthorityRuntimeRequiresNative = errors.New("scoped authority requires the native policy-controlled runner")
var ErrExtraToolsRequireNative = errors.New("task tools require a supported Isoform tool bridge or the native runner")
var ErrDispatchGuardRequiresNative = errors.New("source-bound context requires the native runner's per-request read checks")
var ErrLearningRunsRequireNative = errors.New("background learning runs require the native runner")

// ExternalAgentRunner is the "bring your own agent" (BYOA, B-class)
// implementation of AgentRunner. Instead of driving the in-process
// Anthropic-native loop, it shells out to a user-installed coding agent
// CLI (Claude Code or Codex) and translates that CLI's JSON event stream
// into the StreamEvent vocabulary the GUI consumes. Controlled runs use the
// user's subscription login and the desktop's per-run gated MCP tools. User
// settings, built-in tools and session persistence are disabled on that path.
//
// Each turn starts a fresh controlled process with Isoform's retained history.
// A per-run gated tool server is required, including for direct callers. There
// is no CLI session persistence or resume transport.
type ExternalAgentRunner struct {
	cfg      ExternalAgentConfig
	isClaude bool

	mu sync.Mutex
	// runs maps a runID to a cancel func that stops its in-flight turn.
	runs             map[string]context.CancelFunc
	binary           string
	ready            bool
	profileGate      chan struct{}
	profileCheckedAt time.Time
	profileErr       error
}

// ExternalAgentConfig configures the external agent runner.
type ExternalAgentConfig struct {
	// Format selects the CLI protocol: "claude-code" (default) or "codex".
	Format string
	// Command is the binary path/name. Empty falls back to the protocol's
	// default ("claude" / "codex"), resolved on PATH.
	Command string
	// Model is the default model passed to the CLI (--model / -m) when a task
	// does not resolve to a more specific one. Empty lets the CLI use its own
	// default.
	Model string
	// ModelAliases remaps the per-task role alias the caller emits
	// ("sonnet"/"opus" for chat/code, "haiku" for light/background work) onto
	// the user-configured BYOA models, mirroring NativeRunnerConfig.ModelAliases.
	// This is what lets a cheap light model serve background tasks (summaries,
	// titles, extraction) while chat/code use the big model — the same
	// big/small-brain split BYOK gets. Empty (or an unmapped task model) falls
	// through to the task model verbatim, then to Model.
	ModelAliases map[string]string
	// PermissionMode (claude-code only) sets --permission-mode. Empty uses
	// "dontAsk" so headless runs cannot grant themselves approval.
	PermissionMode string
	// ExtraArgs are rejected: custom arguments could bypass controlled execution.
	ExtraArgs []string
	// ExtraEnv passes through the same environment allowlist as inherited values.
	ExtraEnv []string
	// DisableSandbox is rejected: controlled runs cannot bypass the sandbox.
	DisableSandbox bool
	// ToolServerFactory creates the desktop-owned, per-run gated tool server.
	// Required for all runs; the CLI has no built-in tools or user MCP passthrough.
	ToolServerFactory func(context.Context, *TaskInput, string, StreamCallback) (*toolserver.Server, error)
	// ToolServerSupports checks task tool coverage before a run is selected.
	ToolServerSupports func(*TaskInput) bool
	// Permission responses return to the desktop gate for attended runs.
	RespondToPermission func(string, string) error
	CancelPermission    func(string)
	// ToolShimCommand is the absolute path to the bundled yansu CLI.
	ToolShimCommand string
	// AttemptTimeout caps the time before the first action, including preflight.
	// Zero selects DefaultExternalAttemptTimeout. The first authorized action
	// disarms this timer; the run's deadline and cancellation remain in force.
	AttemptTimeout time.Duration
	// RunTimeout bounds runs whose caller has no deadline. Zero selects
	// DefaultExternalRunTimeout. An explicit caller deadline takes precedence.
	RunTimeout time.Duration
	// MaxBudgetUSD optionally caps a controlled Claude attempt. Zero adds no cap.
	MaxBudgetUSD float64
	// Choice returns the model and effort for one task, read at run time so
	// a Settings change applies to the next run. The desktop decides: the
	// user's pick for foreground work, a lighter default for background
	// work. Empty fields keep the task's own model and the agent's default.
	Choice func(*TaskInput) AgentChoice
}

// AgentChoice is the user's model and reasoning effort for one agent. The
// model is the agent's own id (no magpie provider prefix); empty fields keep
// the agent's defaults.
type AgentChoice struct {
	Model  string
	Effort string
}

// chosenModelAndEffort applies the desktop's choice for this task.
func (c ExternalAgentConfig) chosenModelAndEffort(input *TaskInput, passthroughUnmapped bool) (string, string) {
	model := c.resolveModel(input.Model, passthroughUnmapped)
	if c.Choice == nil {
		return model, ""
	}
	choice := c.Choice(input)
	if m := strings.TrimSpace(choice.Model); m != "" {
		model = m
	}
	return model, strings.TrimSpace(choice.Effort)
}

// resolveModel maps a per-task model to the concrete model spawned on the CLI.
// It first tries a ModelAliases remap of the task's role alias (so "haiku" ->
// configured light model, "sonnet"/"opus" -> configured big model). On a miss,
// passthroughUnmapped controls what happens with the raw task model:
//
//   - true  (Claude Code): pass it through verbatim — the task models ARE
//     Claude's own tier aliases ("sonnet"/"opus"/"haiku"), which the Claude CLI
//     understands, so an unconfigured user still gets sensible per-role tiers.
//   - false (Codex): DROP it and fall back to the configured Model. Those tier
//     aliases are Claude vocabulary and are meaningless to Codex; passing
//     "-m sonnet" would fail or pick the wrong model. With no configured model
//     this returns "" so the caller omits -m and Codex uses its own default.
func (c ExternalAgentConfig) resolveModel(taskModel string, passthroughUnmapped bool) string {
	t := strings.TrimSpace(taskModel)
	if t != "" {
		if mapped, ok := c.ModelAliases[t]; ok {
			if m := strings.TrimSpace(mapped); m != "" {
				return m
			}
		}
		if passthroughUnmapped {
			return t
		}
	}
	return strings.TrimSpace(c.Model)
}

// Compile-time guarantee that the external runner satisfies the same
// interface the GUI programs against, so it drops into StartAgent's runner
// slot interchangeably with NativeAgentRunner.
var _ AgentRunner = (*ExternalAgentRunner)(nil)

// NewExternalAgentRunner constructs a runner for the configured format.
func NewExternalAgentRunner(cfg ExternalAgentConfig) *ExternalAgentRunner {
	r := &ExternalAgentRunner{
		cfg:         cfg,
		runs:        make(map[string]context.CancelFunc),
		profileGate: make(chan struct{}, 1),
	}
	r.isClaude = !strings.EqualFold(strings.TrimSpace(cfg.Format), "codex")
	return r
}

func (r *ExternalAgentRunner) agentName() string {
	if r.isClaude {
		return "claude"
	}
	return "codex"
}

// Init resolves the CLI binary on PATH. A missing binary is a hard error
// so StartAgent surfaces a clear "install the CLI" message instead of
// failing cryptically on the first chat send.
func (r *ExternalAgentRunner) Init(_ context.Context) error {
	bin := strings.TrimSpace(r.cfg.Command)
	defaultBin := "claude"
	if !r.isClaude {
		defaultBin = "codex"
	}
	if bin == "" {
		bin = defaultBin
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("external agent %q (%s) not found on PATH: %w", bin, r.agentName(), err)
	}
	r.mu.Lock()
	r.binary = resolved
	r.ready = true
	r.mu.Unlock()
	log.Printf("[external-agent] ready: format=%s binary=%s", r.agentName(), resolved)
	return nil
}

// IsReady reports whether the binary was resolved.
func (r *ExternalAgentRunner) IsReady() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready
}

// externalSignInTimeout bounds one sign-in probe. It is a variable so tests
// can shorten it.
var externalSignInTimeout = 10 * time.Second

// ExternalAgentSignedIn reports whether the CLI described by cfg has a usable
// sign-in. It runs the CLI's own status command and reads the answer:
//
//   - Claude Code: `claude auth status` prints JSON with a loggedIn field.
//   - Codex: `codex login status` prints "Logged in using ChatGPT" when the
//     ChatGPT sign-in is active. A non-zero exit means signed out.
//
// A missing binary or a timed-out probe returns an error. A signed-out CLI
// returns (false, nil). The probe does not change execution behavior.
func ExternalAgentSignedIn(cfg ExternalAgentConfig) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), externalSignInTimeout)
	defer cancel()

	if strings.EqualFold(strings.TrimSpace(cfg.Format), "codex") {
		bin := strings.TrimSpace(cfg.Command)
		if bin == "" {
			bin = "codex"
		}
		cmd := exec.CommandContext(ctx, bin, "login", "status")
		hideConsoleWindow(cmd)
		configureExternalProcess(cmd)
		cmd.Env = externalChildEnv(os.Environ(), cfg.ExtraEnv)
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return false, fmt.Errorf("codex login status timed out after %s", externalSignInTimeout)
		}
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return false, nil
			}
			return false, fmt.Errorf("codex login status: %w", err)
		}
		return strings.Contains(strings.ToLower(string(out)), "logged in using chatgpt"), nil
	}

	bin := strings.TrimSpace(cfg.Command)
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, "auth", "status")
	hideConsoleWindow(cmd)
	configureExternalProcess(cmd)
	cmd.Env = externalChildEnv(os.Environ(), cfg.ExtraEnv)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return false, fmt.Errorf("claude auth status timed out after %s", externalSignInTimeout)
	}
	var status struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if jsonErr := json.Unmarshal(out, &status); jsonErr == nil {
		return status.LoggedIn && status.AuthMethod == "claude.ai", nil
	}
	if err != nil {
		return false, fmt.Errorf("claude auth status: %w", err)
	}
	return false, fmt.Errorf("claude auth status: unexpected output")
}

// ExecuteTask dispatches to the per-format transport.
func (r *ExternalAgentRunner) ExecuteTask(ctx context.Context, input *TaskInput, callback StreamCallback) (string, error) {
	if input == nil {
		return "", fmt.Errorf("nil task input")
	}
	if input.RequireNativeDispatch {
		return "", ErrDispatchGuardRequiresNative
	}
	mode := input.ExecutionMode
	if mode == "" {
		mode = authority.ExecutionModeNormal
	}
	if mode != authority.ExecutionModeNormal {
		return "", ErrShadowRuntimeRequiresNative
	}
	if input.BackendHint == BackendHintLearning {
		return "", ErrLearningRunsRequireNative
	}
	if taskHasExtraTools(input) && !r.SupportsExtraTools(input) {
		return "", ErrExtraToolsRequireNative
	}
	if r.cfg.ToolServerFactory == nil && (strings.TrimSpace(input.AuthorityInitiativeID) != "" || len(input.AuthorityGrants) > 0) {
		return "", ErrAuthorityRuntimeRequiresNative
	}
	return r.executeControlled(ctx, input, callback)
}

func checkTaskDispatch(ctx context.Context, input *TaskInput) error {
	if input != nil && input.BeforeDispatch != nil {
		if err := input.BeforeDispatch(ctx); err != nil {
			return &native.DispatchDeniedError{Cause: err}
		}
	}
	return nil
}

func buildClaudeUserMessage(prompt string, images []ImageAttachment) (string, error) {
	var content any
	if len(images) == 0 {
		content = prompt
	} else {
		parts := make([]map[string]any, 0, len(images)+1)
		if prompt != "" {
			parts = append(parts, map[string]any{"type": "text", "text": prompt})
		}
		for _, img := range images {
			parts = append(parts, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": img.MediaType,
					"data":       img.DataBase64,
				},
			})
		}
		content = parts
	}
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// Claude Code stream decoding for controlled runs.

type ccLine struct {
	Type         string     `json:"type"`
	Subtype      string     `json:"subtype"`
	Message      *ccMessage `json:"message"`
	Event        *ccEvent   `json:"event"`
	Result       string     `json:"result"`
	IsError      bool       `json:"is_error"`
	Usage        *ccUsage   `json:"usage"`
	TotalCostUSD float64    `json:"total_cost_usd"`
	NumTurns     int        `json:"num_turns"`
}

type ccEvent struct {
	Type  string   `json:"type"`
	Delta *ccDelta `json:"delta"`
}

type ccDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

type ccMessage struct {
	Role    string    `json:"role"`
	Content []ccBlock `json:"content"`
	Usage   *ccUsage  `json:"usage"`
}

type ccBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type ccUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// parse translates one Claude Code stream-json line. Text/thinking are
// emitted incrementally from stream_event deltas (token-level streaming);
// the message-level "assistant" event is used ONLY for tool_use blocks (its
// text/thinking are skipped to avoid double-rendering what the deltas
// already streamed). "result" ends the turn.
func parseClaudeLine(line []byte, emit func(*StreamEvent) error) (bool, error) {
	var l ccLine
	if err := json.Unmarshal(line, &l); err != nil {
		log.Printf("[external-agent][claude] skip unparseable line: %v", err)
		return false, nil
	}

	switch l.Type {
	case "stream_event":
		if l.Event == nil || l.Event.Type != "content_block_delta" || l.Event.Delta == nil {
			return false, nil
		}
		switch l.Event.Delta.Type {
		case "text_delta":
			if l.Event.Delta.Text != "" {
				if err := emit(&StreamEvent{Type: "assistant", Content: l.Event.Delta.Text}); err != nil {
					return false, err
				}
			}
		case "thinking_delta":
			if l.Event.Delta.Thinking != "" {
				if err := emit(&StreamEvent{Type: "thinking", Content: l.Event.Delta.Thinking}); err != nil {
					return false, err
				}
			}
		}
		return false, nil

	case "assistant":
		if l.Message == nil {
			return false, nil
		}
		for _, b := range l.Message.Content {
			if b.Type != "tool_use" {
				continue // text/thinking already streamed via stream_event
			}
			var toolInput any
			if len(b.Input) > 0 {
				_ = json.Unmarshal(b.Input, &toolInput)
			}
			if toolInput == nil {
				toolInput = map[string]any{}
			}
			if err := emit(&StreamEvent{Type: "tool_use_start", ToolName: b.Name, ToolUseID: b.ID}); err != nil {
				return false, err
			}
			if err := emit(&StreamEvent{Type: "tool_use", ToolName: b.Name, ToolUseID: b.ID, ToolInput: toolInput}); err != nil {
				return false, err
			}
		}
		return false, nil

	case "user":
		if l.Message == nil {
			return false, nil
		}
		for _, b := range l.Message.Content {
			if b.Type == "tool_result" {
				if err := emit(&StreamEvent{Type: "tool_result", ToolUseID: b.ToolUseID, Content: rawToText(b.Content), IsError: b.IsError}); err != nil {
					return false, err
				}
			}
		}
		return false, nil

	case "result":
		if l.TotalCostUSD > 0 || l.NumTurns > 0 {
			log.Printf("[external-agent][claude] turn complete: cost=$%.4f turns=%d", l.TotalCostUSD, l.NumTurns)
		}
		if l.IsError && strings.TrimSpace(l.Result) != "" {
			if err := emit(&StreamEvent{Type: "error", Error: l.Result}); err != nil {
				return false, err
			}
		}
		done := &StreamEvent{Type: "done"}
		if l.Usage != nil {
			done.Usage = &Usage{
				InputTokens:              l.Usage.InputTokens,
				OutputTokens:             l.Usage.OutputTokens,
				CacheReadInputTokens:     l.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: l.Usage.CacheCreationInputTokens,
				CostUSD:                  l.TotalCostUSD,
			}
		} else if l.TotalCostUSD > 0 {
			done.Usage = &Usage{CostUSD: l.TotalCostUSD}
		}
		if err := emit(done); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
}

// ── Runner lifecycle ─────────────────────────────────────────────────────

// CancelRun stops the in-flight turn for runID by killing its process.
func (r *ExternalAgentRunner) CancelRun(runID string) error {
	r.mu.Lock()
	cancel := r.runs[runID]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// RespondToPermission forwards the existing permission card to the run gate.
func (r *ExternalAgentRunner) RespondToPermission(requestID, optionID string) error {
	if r.cfg.ToolServerFactory != nil && r.cfg.RespondToPermission != nil {
		return r.cfg.RespondToPermission(requestID, optionID)
	}
	return nil
}

func (r *ExternalAgentRunner) CancelPermission(requestID string) {
	if r.cfg.ToolServerFactory != nil && r.cfg.CancelPermission != nil {
		r.cfg.CancelPermission(requestID)
	}
}

// Shutdown cancels every in-flight run.
func (r *ExternalAgentRunner) Shutdown() error {
	r.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.runs))
	for _, c := range r.runs {
		cancels = append(cancels, c)
	}
	r.ready = false
	r.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	return nil
}

// ── Shared helpers ───────────────────────────────────────────────────────

// rawToText renders a tool_result content field (a JSON string, or an
// array of {type:"text",text:...} blocks, or arbitrary JSON) into plain
// text for the StreamEvent.Content field.
func rawToText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Text != "" {
				b.WriteString(blk.Text)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return string(raw)
}

func truncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// childEnv keeps desktop credentials and model billing overrides out of child
// processes. Both configured and per-task entries pass through the same list.
func (r *ExternalAgentRunner) childEnv(taskEnv []string) []string {
	return externalChildEnv(os.Environ(), r.cfg.ExtraEnv, taskEnv)
}
