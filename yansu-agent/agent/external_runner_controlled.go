package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"yansu-agent/agent/toolserver"
	"yansu-agent/isoformskill"
	"yansu-agent/runtoken"
)

// ExternalAttemptError carries the server's action record. CLI text and stream
// events cannot prove that retrying an interrupted action would be safe.
type ExternalAttemptError struct {
	Cause      error
	Actions    int
	Unfinished bool
}

func (e *ExternalAttemptError) Error() string              { return e.Cause.Error() }
func (e *ExternalAttemptError) Unwrap() error              { return e.Cause }
func (e *ExternalAttemptError) ActionCount() int           { return e.Actions }
func (e *ExternalAttemptError) HasUnfinishedActions() bool { return e.Unfinished }

// SupportsExtraTools promises that turn-local tools reach the assistant through
// the gated server. A runner without a tool server cannot make this promise.
func (r *ExternalAgentRunner) SupportsExtraTools(input *TaskInput) bool {
	if extraToolsRequireNativeScope(input) {
		return false
	}
	ok, _ := r.RoutingEligibility(input)
	return ok
}

// RoutingEligibility is a cheap check. The version/help check is refreshed at
// run start; the server remains locked until the actual init event passes too.
func (r *ExternalAgentRunner) RoutingEligibility(input *TaskInput) (bool, string) {
	switch true {
	case r.cfg.ToolServerFactory == nil:
		return false, "Isoform tool server is unavailable"
	case !r.isClaude:
		return false, "Codex tool isolation has not been verified"
	case runtime.GOOS == "windows":
		return false, "assistant process isolation is unavailable"
	case r.cfg.DisableSandbox:
		return false, "sandbox bypass is incompatible with Isoform tools"
	case len(r.cfg.ExtraArgs) != 0:
		return false, "custom assistant arguments are incompatible with Isoform tools"
	case strings.TrimSpace(r.cfg.ToolShimCommand) == "":
		return false, "Isoform tool shim is unavailable"
	case !filepath.IsAbs(r.cfg.ToolShimCommand):
		return false, "Isoform tool shim requires an absolute path"
	case r.cfg.MaxBudgetUSD < 0:
		return false, "assistant budget must be positive"
	}
	if input != nil && input.MaxTotalTokens > 0 {
		return false, "token budget requires the native runner"
	}
	if r.cfg.ToolServerSupports != nil && !r.cfg.ToolServerSupports(input) {
		return false, "required tools are unavailable on the Isoform tool server"
	}
	return true, ""
}

// --safe-mode is deliberately absent: it also disables --mcp-config servers,
// so the Isoform tool server never connects. --restricted ignores user,
// project and local settings (their hooks and enabled plugins), and
// --strict-mcp-config drops every other MCP server; validateClaudeInit then
// proves the live profile before the server unlocks.
var controlledClaudeFlags = []string{
	"--restricted", "--tools", "--strict-mcp-config", "--mcp-config",
	"--permission-mode", "--permission-prompts", "--allowedTools", "--no-session-persistence",
	"--input-format", "--output-format", "--include-partial-messages", "--include-hook-events",
}

// hookEvent reports a hook lifecycle event. --restricted ignores the user,
// project and local settings files that could define a hook, and init proves
// no plugin beyond the CLI's own builtins. Managed (policy) settings still
// apply, so an administrator's hook can still run; either way its command has
// already run outside Isoform's ledger, so the run fails closed with an
// unknown outcome and is never replayed automatically.
func hookEvent(eventType, subtype string) bool {
	return eventType == "system" && strings.HasPrefix(subtype, "hook")
}

var errAssistantRanHook = errors.New("assistant ran a hook outside Isoform")

// ExternalControlProfile fails closed on CLI versions whose control flags are
// absent. A matching help page is a prerequisite, never proof of tool isolation:
// every live process must also pass validateClaudeInit before any call can run.
func ExternalControlProfile(ctx context.Context, cfg ExternalAgentConfig) error {
	_, err := ExternalControlProfileVersion(ctx, cfg)
	return err
}

// ExternalControlProfileVersion also returns the version the CLI reported, so
// a paused assistant is checked again after an update.
func ExternalControlProfileVersion(ctx context.Context, cfg ExternalAgentConfig) (string, error) {
	// A mismatch is a precondition: every run would fail the same way. A
	// failed read (a CLI mid-update, a slow disk) is not.
	mismatch := func(message string) error { return &AssistantPreconditionError{Cause: errors.New(message)} }
	if strings.EqualFold(strings.TrimSpace(cfg.Format), "codex") {
		return "", mismatch("Codex tool isolation has not been verified")
	}
	if cfg.DisableSandbox || len(cfg.ExtraArgs) != 0 {
		return "", mismatch("custom arguments or sandbox bypass cannot be used with Isoform tools")
	}
	bin := strings.TrimSpace(cfg.Command)
	if bin == "" {
		bin = "claude"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output := func(arg string) ([]byte, error) {
		cmd := exec.CommandContext(probeCtx, bin, arg)
		hideConsoleWindow(cmd)
		configureExternalProcess(cmd)
		cmd.Env = externalChildEnv(os.Environ(), cfg.ExtraEnv)
		cmd.WaitDelay = time.Second
		return cmd.Output()
	}
	raw, err := output("--version")
	if err != nil {
		return "", fmt.Errorf("read assistant version: %w", err)
	}
	version := strings.TrimSpace(string(raw))
	if !supportedClaudeVersion(version) {
		return version, mismatch("Claude Code 2.1.296 or newer is required for isolated tools")
	}
	help, err := output("--help")
	if err != nil {
		return version, fmt.Errorf("read assistant control flags: %w", err)
	}
	flags := append([]string(nil), controlledClaudeFlags...)
	if cfg.MaxBudgetUSD > 0 {
		flags = append(flags, "--max-budget-usd")
	}
	if cfg.Choice != nil {
		// Every chosen run carries an effort (High by default).
		flags = append(flags, "--effort")
	}
	for _, flag := range flags {
		if !regexp.MustCompile(regexp.QuoteMeta(flag) + `(?:[\s=,<]|$)`).Match(help) {
			return version, mismatch("Claude Code is missing required control flag " + flag)
		}
	}
	return version, nil
}

func supportedClaudeVersion(raw string) bool {
	m := regexp.MustCompile(`(?:^|\s)(\d+)\.(\d+)\.(\d+)(?:\s|$)`).FindStringSubmatch(strings.TrimSpace(raw))
	if len(m) != 4 {
		return false
	}
	minimum := []int{2, 1, 296}
	for i, want := range minimum {
		got, err := strconv.Atoi(m[i+1])
		if err != nil {
			return false
		}
		if got != want {
			return got > want
		}
	}
	return true
}

func (r *ExternalAgentRunner) checkControlProfile(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Serialize help/version probes, but let a cancelled waiter leave promptly
	// without consuming its reserved native fallback time behind another probe.
	select {
	case r.profileGate <- struct{}{}:
		defer func() { <-r.profileGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.profileCheckedAt.IsZero() && time.Since(r.profileCheckedAt) < time.Minute {
		return r.profileErr
	}
	err := ExternalControlProfile(ctx, r.cfg)
	// A cancelled caller tells us nothing about the assistant's health. Leave
	// the previous snapshot untouched so the next live run can probe again.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.profileErr = err
	r.profileCheckedAt = time.Now()
	return r.profileErr
}

func controlledClaudeArgs(input *TaskInput, cfg ExternalAgentConfig, configPath string, names []string) []string {
	mode := "dontAsk"
	if input.PlanMode {
		mode = "plan"
	}
	args := []string{
		"--print", "--restricted", "--tools", "",
		"--strict-mcp-config", "--mcp-config", configPath,
		"--permission-mode", mode, "--permission-prompts", "none",
		"--no-session-persistence",
		"--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--include-partial-messages",
		// No hook may run: any hook event in the stream fails the run closed.
		"--include-hook-events",
	}
	if cfg.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(cfg.MaxBudgetUSD, 'f', -1, 64))
	}
	if len(names) > 0 {
		args = append(args, "--allowedTools", strings.Join(names, ","))
	}
	model, effort := cfg.chosenModelAndEffort(input, true)
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	if input.SystemText != "" && !input.NoSystem {
		args = append(args, "--system-prompt", input.SystemText)
	}
	return args
}

type claudeInit struct {
	Type           string         `json:"type"`
	Subtype        string         `json:"subtype"`
	Tools          []string       `json:"tools"`
	Plugins        []claudePlugin `json:"plugins"`
	Model          string         `json:"model"`
	PermissionMode string         `json:"permissionMode"`
	MCPServers     []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
}

// claudePlugin is one entry of the init's plugin list. Claude Code reports the
// plugins compiled into its own binary (path "builtin", source "<name>@builtin")
// even with every customization off; they ship with the CLI the user signed
// in to and add no tools beyond the verified list. Anything else is a user or
// marketplace plugin, which Isoform does not admit.
type claudePlugin struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Source string `json:"source"`
}

func (p claudePlugin) builtin() bool {
	return p.Name != "" && p.Path == "builtin" && p.Source == p.Name+"@builtin"
}

func validateClaudeInit(line []byte, expected []string, plan bool) (string, error) {
	var init claudeInit
	if err := json.Unmarshal(line, &init); err != nil {
		return "", fmt.Errorf("invalid assistant init: %w", err)
	}
	if init.Type != "system" || init.Subtype != "init" {
		return "", errors.New("assistant did not report its control profile")
	}
	if init.Tools == nil || init.Plugins == nil {
		return "", errors.New("assistant init omitted tools or plugins")
	}
	for _, plugin := range init.Plugins {
		if !plugin.builtin() {
			return "", errors.New("assistant loaded plugins outside Isoform")
		}
	}
	mode := "dontAsk"
	if plan {
		mode = "plan"
	}
	if init.PermissionMode != mode {
		return "", errors.New("assistant permission mode does not match the run")
	}
	allowed := make(map[string]bool, len(expected))
	for _, name := range expected {
		allowed[name] = true
	}
	seen := make(map[string]bool, len(init.Tools))
	for _, name := range init.Tools {
		if !allowed[name] || seen[name] {
			return "", fmt.Errorf("assistant exposed an unexpected tool %q", name)
		}
		seen[name] = true
	}
	if len(seen) != len(allowed) {
		return "", errors.New("assistant is missing required Isoform tools")
	}
	if len(init.MCPServers) != 1 || init.MCPServers[0].Name != "isoform" || init.MCPServers[0].Status != "connected" {
		return "", errors.New("assistant did not connect only to the Isoform tool server")
	}
	if strings.TrimSpace(init.Model) == "" {
		return "", errors.New("assistant init omitted the model")
	}
	return init.Model, nil
}

// A tool outside the init allowlist is an isolation failure, even if the
// assistant only claims to have run it. Its outcome cannot be proven by the
// server ledger, so automatic fallback must remain disabled.
func unexpectedClaudeTool(line []byte, allowed map[string]bool) string {
	var event struct {
		Message *ccMessage `json:"message"`
		Event   *struct {
			Block *ccBlock `json:"content_block"`
		} `json:"event"`
	}
	if json.Unmarshal(line, &event) != nil {
		return ""
	}
	var blocks []ccBlock
	if event.Message != nil {
		blocks = append(blocks, event.Message.Content...)
	}
	if event.Event != nil && event.Event.Block != nil {
		blocks = append(blocks, *event.Event.Block)
	}
	for _, block := range blocks {
		if block.Type == "tool_use" && !allowed[block.Name] {
			if block.Name == "" {
				return "<unnamed>"
			}
			return block.Name
		}
	}
	return ""
}

const (
	// DefaultExternalAttemptTimeout bounds startup and model deliberation before
	// the first action. This is a provisional healthy-model allowance, not a
	// measured model latency; local transport fixtures take under a second.
	DefaultExternalAttemptTimeout = 2 * time.Minute
	// DefaultExternalRunTimeout bounds background callers that supply only an
	// app-lifetime context, including runs that have already executed tools.
	DefaultExternalRunTimeout = 30 * time.Minute
	// Allow process-group shutdown, the one-second pipe wait and ledger close
	// before the native fallback receives its separately requested budget.
	externalFallbackTeardownAllowance = 5 * time.Second
)

// controlledAttemptDeadline bounds the pre-action phase without shortening an
// explicit caller deadline after an action. Admission and expiry share a lock
// so a timed-out attempt never records a started action or dispatches a tool.
type controlledAttemptDeadline struct {
	ctx               context.Context
	cancel            context.CancelCauseFunc
	mu                sync.Mutex
	timer             *time.Timer
	acting            bool
	preActionDeadline time.Time
	// limit is one pre-action window; windowEnd is the latest the window may
	// reach (the caller's deadline less the native fallback reserve), zero
	// when the caller set no deadline.
	limit     time.Duration
	windowEnd time.Time
}

func newControlledAttemptDeadline(parent context.Context, limit time.Duration) *controlledAttemptDeadline {
	ctx, cancel := context.WithCancelCause(parent)
	d := &controlledAttemptDeadline{ctx: ctx, cancel: cancel}
	if limit <= 0 {
		limit = DefaultExternalAttemptTimeout
	}
	now := time.Now()
	if deadline, ok := parent.Deadline(); ok {
		window := deadline.Sub(now)
		if reserve := NativeFallbackBudget(parent); reserve > 0 {
			window -= reserve + externalFallbackTeardownAllowance
		}
		if window < limit {
			limit = window
		}
		d.windowEnd = now.Add(window)
	}
	d.limit = limit
	d.preActionDeadline = now.Add(limit)
	if limit <= 0 {
		cancel(fmt.Errorf("insufficient time before native fallback budget: %w", context.DeadlineExceeded))
		return d
	}
	d.timer = time.AfterFunc(limit, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		// noteProgress may have moved the deadline while this call waited.
		if !d.acting && !time.Now().Before(d.preActionDeadline) {
			d.cancel(fmt.Errorf("assistant used its pre-action time allowance: %w", context.DeadlineExceeded))
		}
	})
	return d
}

// noteProgress moves the pre-action deadline one window past now while the
// assistant streams its answer. Text arriving shows a live CLI, so a long
// written answer is not cut off when the first window ends; a CLI that goes
// silent still ends one window after its last text. The window never passes
// the native fallback reserve.
func (d *controlledAttemptDeadline) noteProgress() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.acting || d.timer == nil || d.limit <= 0 || d.ctx.Err() != nil {
		return
	}
	next := time.Now().Add(d.limit)
	if !d.windowEnd.IsZero() && next.After(d.windowEnd) {
		next = d.windowEnd
	}
	if !next.After(d.preActionDeadline) {
		return
	}
	d.preActionDeadline = next
	d.timer.Reset(time.Until(next))
}

func (d *controlledAttemptDeadline) admitAction(start func() error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx.Err() != nil {
		return context.Cause(d.ctx)
	}
	// The timer goroutine may not have acquired the lock yet. Honor the actual
	// expiry time too, so late admission cannot win merely through scheduling.
	if !d.acting && !time.Now().Before(d.preActionDeadline) {
		err := fmt.Errorf("assistant used its pre-action time allowance: %w", context.DeadlineExceeded)
		d.cancel(err)
		return err
	}
	if err := start(); err != nil {
		return err
	}
	d.acting = true
	if d.timer != nil {
		d.timer.Stop()
	}
	return nil
}

func (d *controlledAttemptDeadline) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
	d.cancel(context.Canceled)
}

func (r *ExternalAgentRunner) executeControlled(ctx context.Context, input *TaskInput, callback StreamCallback) (runID string, retErr error) {
	if ok, reason := r.RoutingEligibility(input); !ok {
		return "", errors.New(reason)
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		limit := r.cfg.RunTimeout
		if limit <= 0 {
			limit = DefaultExternalRunTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	attempt := newControlledAttemptDeadline(ctx, r.cfg.AttemptTimeout)
	runCtx := toolserver.WithActionAdmission(attempt.ctx, attempt.admitAction)
	cancel := attempt.stop
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return "", &ExternalAttemptError{Cause: context.Cause(runCtx)}
	}
	if err := r.checkControlProfile(runCtx); err != nil {
		if runCtx.Err() != nil {
			return "", &ExternalAttemptError{Cause: context.Cause(runCtx)}
		}
		// A version or flag mismatch arrives as an AssistantPreconditionError.
		return "", err
	}
	prompt, history := input.Prompt, input.History
	if input.PromptFunc != nil {
		resolved, resolvedHistory := input.PromptFunc()
		if strings.TrimSpace(resolved) != "" {
			prompt = resolved
		}
		// An empty reload after rewind clears any previously supplied history.
		history = resolvedHistory
	}
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("prompt is required")
	}
	if history != "" {
		prompt = history + "\n\n" + prompt
	}
	prompt = isoformskill.Prompt(prompt)
	message, err := buildClaudeUserMessage(prompt, input.Images)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	binary := r.binary
	r.mu.Unlock()
	if binary == "" {
		return "", errors.New("external agent not initialized")
	}
	runID = uuid.NewString()
	runInput := *input
	if runInput.SessionID == "" {
		runInput.SessionID = runID
	}
	input = &runInput
	r.mu.Lock()
	r.runs[runID] = cancel
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.runs, runID); r.mu.Unlock() }()
	var emitMu sync.Mutex
	var callbackErr error
	var unknownAction bool
	var output strings.Builder
	var usage *Usage
	emit := func(ev *StreamEvent) error {
		emitMu.Lock()
		defer emitMu.Unlock()
		ev.RunID = runID
		ev.SessionID = input.SessionID
		if ev.Type == "assistant" {
			output.WriteString(ev.Content)
		}
		if (ev.Type == "assistant" || ev.Type == "thinking") && ev.Content != "" {
			attempt.noteProgress()
		}
		if callback == nil {
			return nil
		}
		err := callback(ev)
		if err != nil && callbackErr == nil {
			callbackErr = err
			cancel()
		}
		return err
	}
	server, err := r.cfg.ToolServerFactory(runCtx, input, runID, emit)
	if err != nil {
		return runID, &ExternalAttemptError{Cause: err}
	}
	initialized := false
	defer func() {
		// Setup can expire before cmd.Start or the stream loop. Preserve the
		// deadline cause so its empty ledger still permits native fallback.
		if runCtx.Err() != nil {
			retErr = context.Cause(runCtx)
			if initialized && errors.Is(retErr, context.DeadlineExceeded) {
				retErr = &answerDeadlineError{Cause: retErr}
			}
		}
		closeErr := server.Close()
		count, unfinished, snapshotErr := server.Snapshot()
		unfinished = unfinished || unknownAction
		if retErr == nil {
			retErr = closeErr
		}
		if snapshotErr != nil {
			retErr = errors.Join(retErr, snapshotErr)
			unfinished = true
		}
		if unfinished {
			retErr = errors.Join(retErr, errors.New("assistant action has an unknown outcome"))
		}
		emitMu.Lock()
		if callbackErr != nil {
			retErr = errors.Join(retErr, callbackErr)
		}
		emitMu.Unlock()
		if retErr == nil {
			retErr = emit(&StreamEvent{Type: "done", Usage: usage, ActionsStarted: count})
		}
		if retErr != nil {
			retErr = &ExternalAttemptError{Cause: retErr, Actions: count, Unfinished: unfinished}
			_ = emit(&StreamEvent{Type: "error", Error: retErr.Error(), ActionsStarted: count})
		}
		_ = emit(&StreamEvent{Type: "close", ActionsStarted: count})
	}()
	if err := emit(&StreamEvent{Type: "run_created", Backend: string(BackendClaudeCode)}); err != nil {
		return runID, err
	}
	configPath, names, err := r.writeClaudeMCPConfig(server)
	if err != nil {
		return runID, err
	}
	defer os.Remove(configPath)
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	cmd := exec.CommandContext(runCtx, binary, controlledClaudeArgs(input, r.cfg, configPath, names)...)
	hideConsoleWindow(cmd)
	configureExternalProcess(cmd)
	cmd.WaitDelay = time.Second
	cmd.Env = runtoken.ChildEnv(runCtx, r.childEnv(input.Env))
	cmd.Dir = input.WorkDir
	if err := checkTaskDispatch(runCtx, input); err != nil {
		return runID, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return runID, err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return runID, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return runID, fmt.Errorf("start Claude Code: %w", err)
	}
	reportProcessStarted(cmd.Process.Pid)
	defer reportProcessExited(cmd.Process.Pid)
	// Reap descendants even if the assistant exits cleanly with its shim alive.
	defer killExternalProcess(cmd)
	server.SetAssistantPID(cmd.Process.Pid)
	go func() { defer stdin.Close(); _, _ = io.WriteString(stdin, message) }()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	completed, limited := false, false
	var parseErr error
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var envelope struct {
			Type    string          `json:"type"`
			Subtype string          `json:"subtype"`
			IsError bool            `json:"is_error"`
			Result  string          `json:"result"`
			Errors  []string        `json:"errors"`
			Error   json.RawMessage `json:"error"`
			Limit   *struct {
				Status string `json:"status"`
			} `json:"rate_limit_info"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			parseErr = fmt.Errorf("invalid assistant stream: %w", err)
			break
		}
		if hookEvent(envelope.Type, envelope.Subtype) {
			unknownAction = true
			parseErr = &AssistantPreconditionError{Cause: errAssistantRanHook}
			break
		}
		if envelope.Type == "rate_limit_event" && envelope.Limit != nil && envelope.Limit.Status == "rejected" {
			limited = true
		}
		if name := unexpectedClaudeTool(line, allowed); name != "" {
			unknownAction = true
			parseErr = fmt.Errorf("assistant attempted a tool outside its verified profile: %s", name)
			break
		}
		if envelope.Type == "result" && envelope.IsError {
			message := strings.TrimSpace(envelope.Result)
			if message == "" {
				message = strings.Join(envelope.Errors, "; ")
			}
			if message == "" {
				message = "assistant returned an error result"
			}
			parseErr = errors.New(message)
			break
		}
		if envelope.Type == "error" {
			message := assistantErrorMessage(envelope.Error)
			if strings.TrimSpace(message) == "" {
				message = "assistant returned an error event"
			}
			parseErr = errors.New(message)
			break
		}
		if !initialized && envelope.Type == "system" && envelope.Subtype != "init" {
			// Claude Code 2.1.296 announces UI and status events (ui_invalidate)
			// before its init. They carry no output or tool use (hook events
			// failed above); anything else before init still fails the
			// control profile below.
			continue
		}
		if !initialized {
			model, err := validateClaudeInit(line, names, input.PlanMode)
			if err != nil {
				parseErr = &AssistantPreconditionError{Cause: err}
				break
			}
			if err := emit(&StreamEvent{Type: "model_route", Provider: string(BackendClaudeCode), ServedModel: model}); err != nil {
				parseErr = err
				break
			}
			server.Unlock()
			initialized = true
			continue
		}
		if envelope.Type == "system" && envelope.Subtype == "init" {
			parseErr = errors.New("assistant changed its control profile during the run")
			break
		}
		// Tool events come exclusively from the desktop gate, with native names,
		// IDs and IsError. Dropping CLI echoes prevents duplicate actions/history.
		filteredEmit := func(ev *StreamEvent) error {
			switch ev.Type {
			case "tool_use_start", "tool_use", "tool_result":
				return nil
			case "done":
				usage = ev.Usage
				return nil
			}
			return emit(ev)
		}
		done, err := parseClaudeLine(line, filteredEmit)
		if err != nil {
			parseErr = err
			break
		}
		if done {
			completed = true
			emitMu.Lock()
			hasOutput := strings.TrimSpace(output.String()) != ""
			emitMu.Unlock()
			if !hasOutput && strings.TrimSpace(envelope.Result) != "" {
				if err := emit(&StreamEvent{Type: "assistant", Content: envelope.Result}); err != nil {
					parseErr = err
					break
				}
			}
		}
	}
	if parseErr != nil {
		_ = killExternalProcess(cmd)
	}
	waitErr := cmd.Wait()
	switch true {
	case runCtx.Err() != nil:
		retErr = context.Cause(runCtx)
	case parseErr != nil:
		retErr = parseErr
	case scanner.Err() != nil:
		retErr = scanner.Err()
	case waitErr != nil:
		retErr = fmt.Errorf("Claude Code: %w: %s", waitErr, truncateOutput(strings.TrimSpace(stderr.String()), 800))
	case !initialized || !completed:
		retErr = errors.New("assistant exited without a verified completed result")
	}
	if retErr != nil && limited {
		// The CLI's own rate-limit event, not its prose, says the account
		// is out of usage: cool down instead of counting an error.
		retErr = &AssistantUsageLimitError{Cause: retErr}
	}
	emitMu.Lock()
	if retErr == nil && strings.TrimSpace(output.String()) == "" {
		retErr = errors.New("assistant returned an empty result")
	}
	emitMu.Unlock()
	return runID, retErr
}

// writeClaudeMCPConfig points the CLI at this run's tool server and nothing else.
func (r *ExternalAgentRunner) writeClaudeMCPConfig(server *toolserver.Server) (string, []string, error) {
	names := make([]string, 0, len(server.ToolNames()))
	for _, name := range server.ToolNames() {
		names = append(names, "mcp__isoform__"+name)
	}
	sort.Strings(names)
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"isoform": map[string]any{
		"command": r.cfg.ToolShimCommand, "args": []string{"tool-shim", "--ticket-file", server.TicketPath()},
	}}})
	if err != nil {
		return "", nil, err
	}
	configPath := filepath.Join(filepath.Dir(server.TicketPath()), "claude-mcp.json")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		return "", nil, err
	}
	return configPath, names, nil
}

// healthProbePrompt is sent only because Claude Code reports its init after
// the first message. The probe stops the CLI as soon as init is verified.
const healthProbePrompt = "Reply with OK."

// ProbeHealth starts the CLI with the controlled profile, verifies its init
// against a tool server with no tools and stops it there. It runs no task and
// never unlocks the server, so nothing can act.
func (r *ExternalAgentRunner) ProbeHealth(ctx context.Context) error {
	input := &TaskInput{Prompt: healthProbePrompt, NoTools: true}
	if ok, reason := r.RoutingEligibility(input); !ok {
		return errors.New(reason)
	}
	if err := ExternalControlProfile(ctx, r.cfg); err != nil {
		return err
	}
	r.mu.Lock()
	binary := r.binary
	r.mu.Unlock()
	if binary == "" {
		return errors.New("external agent not initialized")
	}
	work, err := os.MkdirTemp("", "isoform-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	input.WorkDir = work
	runID := "probe-" + uuid.NewString()
	server, err := r.cfg.ToolServerFactory(probeCtx, input, runID, func(*StreamEvent) error { return nil })
	if err != nil {
		return err
	}
	defer server.Close()
	configPath, names, err := r.writeClaudeMCPConfig(server)
	if err != nil {
		return err
	}
	defer os.Remove(configPath)
	message, err := buildClaudeUserMessage(healthProbePrompt, nil)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(probeCtx, binary, controlledClaudeArgs(input, r.cfg, configPath, names)...)
	hideConsoleWindow(cmd)
	configureExternalProcess(cmd)
	cmd.WaitDelay = time.Second
	cmd.Env = r.childEnv(nil)
	cmd.Dir = work
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Claude Code: %w", err)
	}
	reportProcessStarted(cmd.Process.Pid)
	defer reportProcessExited(cmd.Process.Pid)
	defer killExternalProcess(cmd)
	server.SetAssistantPID(cmd.Process.Pid)
	go func() { defer stdin.Close(); _, _ = io.WriteString(stdin, message) }()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	probeErr := errors.New("assistant exited before reporting its control profile")
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var envelope struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			probeErr = fmt.Errorf("invalid assistant stream: %w", err)
			break
		}
		if hookEvent(envelope.Type, envelope.Subtype) {
			probeErr = errAssistantRanHook
			break
		}
		if envelope.Type == "system" && envelope.Subtype != "init" {
			continue
		}
		_, probeErr = validateClaudeInit(line, names, false)
		break
	}
	_ = killExternalProcess(cmd)
	_ = cmd.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if probeErr != nil && stderr.Len() > 0 {
		probeErr = fmt.Errorf("%w: %s", probeErr, truncateOutput(strings.TrimSpace(stderr.String()), 400))
	}
	return probeErr
}

// The user's subscription login lives in CLI auth storage. Model endpoint,
// token, API-key, runtime injection and desktop bridge variables are omitted,
// including attempts to reintroduce them through task or provider settings.
func externalChildEnv(groups ...[]string) []string {
	allowed := map[string]bool{
		"HOME": true, "USER": true, "LOGNAME": true, "PATH": true, "SHELL": true,
		"TMPDIR": true, "TMP": true, "TEMP": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true,
		"SYSTEMROOT": true, "WINDIR": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
		"CLAUDE_CONFIG_DIR": true, "CODEX_HOME": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true,
	}
	values := map[string]string{}
	for _, group := range groups {
		for _, entry := range group {
			key, value, ok := strings.Cut(entry, "=")
			if ok && allowed[key] {
				values[key] = value
			}
		}
	}
	values["DISABLE_TELEMETRY"] = "1"
	values["DISABLE_ERROR_REPORTING"] = "1"
	values["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

// assistantErrorMessage accepts either a JSON string or an object with a message.
func assistantErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil && strings.TrimSpace(obj.Message) != "" {
		return strings.TrimSpace(obj.Message)
	}
	return strings.TrimSpace(string(raw))
}
