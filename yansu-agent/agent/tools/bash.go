package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"yansu-agent/agent/tools/guard"
	"yansu-agent/runtoken"
)

const (
	bashDefaultTimeout      = 2 * time.Minute
	bashMaxTimeout          = 10 * time.Minute
	bashOutputByteLimit     = 32 * 1024
	bashOutputLineLimit     = 3000
	bashOutputKeepHeadLines = 1500
	bashOutputKeepTailLines = 1500
	bashBackgroundRingKB    = 512
)

// BgShellDoneFunc is called when a background shell exits on its own
// (not via Kill/KillAll). shellID is the bg-shell identifier, exit is
// the process exit code, status is "completed" or "failed", and command
// is the original shell command string.
type BgShellDoneFunc func(shellID string, exit int, status string, command string)

// BashTool runs shell commands in the session's working directory.
// Each invocation spawns a fresh child process; we do not keep a
// persistent shell session.
type BashTool struct {
	OnWebSearch func(service string)
	// mu serializes Bash invocations within the same tool instance so
	// two commands in one session don't stomp on each other.
	mu sync.Mutex
	// bgMu guards the background-shell registry.
	bgMu   sync.Mutex
	bgs    map[string]*bgShell
	onDone BgShellDoneFunc
	// sandboxExecutable is a test seam; production always uses the system path.
	sandboxExecutable string
}

// SetOnDone registers a callback that fires whenever a background shell
// exits on its own (i.e. not killed via Kill/KillAll).
func (t *BashTool) SetOnDone(fn BgShellDoneFunc) {
	t.bgMu.Lock()
	defer t.bgMu.Unlock()
	t.onDone = fn
}

type bgShell struct {
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	status     string // running | completed | failed
	exit       int
	startedAt  time.Time
	endedAt    time.Time
	buf        *ringBuffer
	cursor     int  // mutex-guarded by outer bgMu
	killedByUs bool // true when Kill/KillAll triggered the exit
	command    string
}

// NewBashTool returns a fresh Bash tool with an empty background registry.
func NewBashTool() *BashTool { return &BashTool{bgs: make(map[string]*bgShell)} }

func (*BashTool) Name() string { return "Bash" }

func (*BashTool) Description() string {
	return "Execute a shell command in the session's working directory. " +
		"Each call starts a fresh subprocess (no persistent shell state). " +
		"Set run_in_background=true for long-running tasks and poll with BashOutput."
}

func (*BashTool) JSONSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"command", "description"},
		"properties": map[string]any{
			"command": map[string]any{"type": "string"},
			"description": map[string]any{
				"type":        "string",
				"description": "Short (5-10 word) description of what this command does in active voice, shown to the user in the UI. Examples: 'List files in current directory', 'Install package dependencies', 'Run unit tests'.",
			},
			"timeout":           map[string]any{"type": "integer", "description": "milliseconds, max 600000"},
			"run_in_background": map[string]any{"type": "boolean"},
		},
	}
}

func (*BashTool) NeedsPermission(input map[string]any) (bool, PermissionPrompt) {
	// Permission prompts are disabled by design. Truly dangerous
	// commands (rm -rf /, fork bombs, dd to /dev/...) are still hard-
	// blocked inside Execute via guard.ClassifyCommand.
	return false, PermissionPrompt{}
}

func (t *BashTool) Execute(ctx context.Context, call ToolCall) (ToolResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cmd, _ := call.Input["command"].(string)
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ToolResult{}, fmt.Errorf("command is required")
	}
	if err := CheckProtectedPaths("Bash", call.Input, call.WorkDir); err != nil {
		return ToolResult{}, err
	}
	if err := enforcePersistenceApproval(ctx, "Bash", call); err != nil {
		return ToolResult{}, err
	}
	if err := enforceBashLauncherApproval(ctx, call); err != nil {
		return ToolResult{}, err
	}

	// Hard-block check — even if the permission gate is satisfied the
	// tool must refuse these.
	c := guard.ClassifyCommand(cmd)
	if c.Risk == guard.RiskBlocked {
		return ToolResult{}, fmt.Errorf("command is blocked by safety policy (%s)", c.Reason)
	}

	timeoutMs, _ := asInt(call.Input["timeout"])
	if timeoutMs <= 0 {
		timeoutMs = int(bashDefaultTimeout / time.Millisecond)
	}
	if timeoutMs > int(bashMaxTimeout/time.Millisecond) {
		timeoutMs = int(bashMaxTimeout / time.Millisecond)
	}

	runBG, _ := call.Input["run_in_background"].(bool)
	if !runBG && looksLikeLongRunningServerCommand(cmd) {
		return ToolResult{}, fmt.Errorf("foreground long-running server command blocked: start dev/server processes with run_in_background=true and poll with BashOutput, or run a finite command such as tests/build instead")
	}

	workDir := call.WorkDir
	if workDir == "" {
		// Fall back to OS temp so we never exec with an empty cwd.
		workDir = os.TempDir()
	}
	invocation, err := t.bashInvocation(ctx, call, cmd)
	if err != nil {
		return ToolResult{}, err
	}

	if runBG {
		return t.runBackground(ctx, cmd, workDir, invocation)
	}
	return t.runForeground(ctx, cmd, workDir, time.Duration(timeoutMs)*time.Millisecond, invocation)
}

// longRunningServerExecutables maps a known dev-server executable to the set
// of subcommands that start a long-running process. An entry of "" means the
// bare invocation (no subcommand) starts a server. Subcommands not listed
// (e.g. `vite build`, `next lint`) are treated as finite and allowed.
var longRunningServerExecutables = map[string]map[string]struct{}{
	"vite":  {"": {}, "dev": {}, "preview": {}, "serve": {}},
	"next":  {"dev": {}, "start": {}},
	"nuxt":  {"dev": {}, "preview": {}, "start": {}},
	"astro": {"dev": {}, "preview": {}},
}

// longRunningPackageScripts are npm-style script names that conventionally
// start a long-running process. Used with package managers and `bun run`.
var longRunningPackageScripts = map[string]struct{}{
	"dev":   {},
	"start": {},
}

// packageManagers lists the known JS package managers whose `<pm> <script>`
// invocations should be inspected for long-running scripts.
var packageManagers = map[string]struct{}{
	"npm":  {},
	"pnpm": {},
	"yarn": {},
}

// looksLikeLongRunningServerCommand inspects each shell segment as parsed argv
// to decide if it would start a long-running dev/preview/server process. It
// matches on argv shape (head + first non-flag arg), not on free-text
// substrings, so quoted/argument text like `grep "vite preview"` is not
// blocked, and finite invocations like `vite build` / `next build` pass.
func looksLikeLongRunningServerCommand(command string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(command), " "))
	if normalized == "" {
		return false
	}
	for _, segment := range splitShellCommandSegments(normalized) {
		if segmentLooksLikeLongRunningServer(segment) {
			return true
		}
	}
	return false
}

func segmentLooksLikeLongRunningServer(segment string) bool {
	fields := strings.Fields(strings.TrimSpace(segment))
	for len(fields) > 0 && isEnvAssignment(fields[0]) {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return false
	}
	head := fields[0]
	rest := fields[1:]

	// npx/bunx wrappers: peel and recurse on the inner invocation.
	if (head == "npx" || head == "bunx") && len(rest) > 0 {
		return segmentLooksLikeLongRunningServer(strings.Join(rest, " "))
	}

	if subs, ok := longRunningServerExecutables[head]; ok {
		_, blocked := subs[leadingSubcommand(rest)]
		return blocked
	}
	if _, ok := packageManagers[head]; ok {
		return packageManagerInvocationLooksLong(head, rest)
	}
	if head == "bun" {
		return bunInvocationLooksLong(rest)
	}
	if head == "python" || head == "python3" {
		return pythonInvocationLooksLong(rest)
	}
	return false
}

// leadingSubcommand returns the first positional arg, treating any leading
// flag (e.g. `--port 5173`) as the absence of a subcommand.
func leadingSubcommand(rest []string) string {
	if len(rest) == 0 {
		return ""
	}
	if strings.HasPrefix(rest[0], "-") {
		return ""
	}
	return rest[0]
}

func packageManagerInvocationLooksLong(pm string, rest []string) bool {
	sub := leadingSubcommand(rest)
	if sub == "" {
		return false
	}
	if pm == "npm" {
		switch sub {
		case "start":
			return true
		case "run":
			return packageScriptIsLongRunning(rest[1:])
		}
		return false
	}
	// pnpm / yarn allow both `<pm> <script>` and `<pm> run <script>`.
	if sub == "run" {
		return packageScriptIsLongRunning(rest[1:])
	}
	_, ok := longRunningPackageScripts[sub]
	return ok
}

func packageScriptIsLongRunning(args []string) bool {
	_, ok := longRunningPackageScripts[leadingSubcommand(args)]
	return ok
}

func bunInvocationLooksLong(rest []string) bool {
	args := make([]string, 0, len(rest))
	for _, a := range rest {
		if strings.HasPrefix(a, "-") {
			continue
		}
		args = append(args, a)
	}
	if len(args) == 0 {
		return false
	}
	// Treat a leading `run` as a no-op for matching purposes so that
	// `bun server.ts`, `bun --hot server.ts`, and `bun run server.ts` are
	// handled uniformly.
	if args[0] == "run" {
		args = args[1:]
		if len(args) == 0 {
			return false
		}
	}
	if _, ok := longRunningPackageScripts[args[0]]; ok {
		return true
	}
	for _, a := range args {
		if isServerEntryPath(a) {
			return true
		}
	}
	return false
}

func pythonInvocationLooksLong(rest []string) bool {
	for i := 0; i+1 < len(rest); i++ {
		if rest[i] == "-m" && rest[i+1] == "http.server" {
			return true
		}
	}
	return false
}

// isServerEntryPath returns true if arg's basename is server.{ts,js,mjs}.
func isServerEntryPath(arg string) bool {
	if arg == "" {
		return false
	}
	base := arg
	if i := strings.LastIndexByte(arg, '/'); i >= 0 {
		base = arg[i+1:]
	}
	switch base {
	case "server.ts", "server.js", "server.mjs":
		return true
	}
	return false
}

// isEnvAssignment reports whether s is a shell env-var assignment like
// `FOO=bar`. Used to skip leading env prefixes in `FOO=bar vite ...`.
func isEnvAssignment(s string) bool {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range s[:eq] {
		switch {
		case r == '_':
		case 'a' <= r && r <= 'z':
		case 'A' <= r && r <= 'Z':
		case '0' <= r && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func splitShellCommandSegments(command string) []string {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n")
	return strings.Split(replacer.Replace(command), "\n")
}

func (t *BashTool) recordWebSearch(command string) {
	if t.OnWebSearch == nil {
		return
	}
	resource, ok := opencliResource(command)
	if !ok {
		return
	}
	switch resource {
	case "opencli:grok:ask/", "opencli:gemini:ask/", "opencli:doubao:ask/":
		t.OnWebSearch(strings.Split(resource, ":")[1])
	}
}

func (t *BashTool) runForeground(ctx context.Context, command, workDir string, timeout time.Duration, invocation bashInvocation) (ToolResult, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ec := exec.CommandContext(cmdCtx, invocation.executable, invocation.args...)
	ec.Dir = workDir
	ec.Env = runtoken.ChildEnv(ctx, guard.ScrubSensitiveEnv(os.Environ()))
	configureProcessGroup(ec)

	var buf bytes.Buffer
	ec.Stdout = &buf
	ec.Stderr = &buf

	if err := ec.Start(); err != nil {
		return ToolResult{}, fmt.Errorf("start: %w", err)
	}

	t.recordWebSearch(command)
	waitErr := make(chan error, 1)
	go func() { waitErr <- ec.Wait() }()

	select {
	case err := <-waitErr:
		return buildBashResult(buf.Bytes(), ec, err, false), nil
	case <-cmdCtx.Done():
		// Timeout or parent cancellation — SIGTERM the group, then SIGKILL.
		killProcessGroup(ec, syscall.SIGTERM)
		select {
		case err := <-waitErr:
			res := buildBashResult(buf.Bytes(), ec, err, true)
			return res, nil
		case <-time.After(5 * time.Second):
			killProcessGroup(ec, syscall.SIGKILL)
			<-waitErr
			res := buildBashResult(buf.Bytes(), ec, errors.New("killed after timeout"), true)
			return res, nil
		}
	}
}

func (t *BashTool) runBackground(runCtx context.Context, command, workDir string, invocation bashInvocation) (ToolResult, error) {
	id := fmt.Sprintf("bg-%d", time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())

	ec := exec.Command(invocation.executable, invocation.args...)
	ec.Dir = workDir
	ec.Env = runtoken.ChildEnv(runCtx, guard.ScrubSensitiveEnv(os.Environ()))
	configureProcessGroup(ec)

	ring := newRingBuffer(bashBackgroundRingKB * 1024)
	pr, pw := io.Pipe()
	ec.Stdout = pw
	ec.Stderr = pw

	go func() {
		// Drain the pipe into the ring buffer.
		_, _ = io.Copy(ring, pr)
	}()

	if err := ec.Start(); err != nil {
		cancel()
		pw.Close() // unblock the drain goroutine
		return ToolResult{}, fmt.Errorf("start: %w", err)
	}

	t.recordWebSearch(command)
	entry := &bgShell{
		cmd:       ec,
		cancel:    cancel,
		status:    "running",
		startedAt: time.Now(),
		buf:       ring,
		command:   command,
	}

	t.bgMu.Lock()
	t.bgs[id] = entry
	t.bgMu.Unlock()

	go func() {
		err := ec.Wait()
		_ = pw.Close()

		t.bgMu.Lock()
		entry.endedAt = time.Now()
		if err == nil {
			entry.status = "completed"
			entry.exit = 0
		} else if ctx.Err() != nil {
			entry.status = "failed"
			entry.exit = -1
		} else {
			entry.status = "failed"
			if ee, ok := err.(*exec.ExitError); ok {
				entry.exit = ee.ExitCode()
			}
		}
		onDone := t.onDone
		shouldNotify := !entry.killedByUs && onDone != nil
		doneStatus := entry.status
		doneExit := entry.exit
		doneCmd := entry.command
		t.bgMu.Unlock()

		if shouldNotify {
			onDone(id, doneExit, doneStatus, doneCmd)
		}
	}()

	return ToolResult{
		Content:  fmt.Sprintf("Background shell started: shell_id=%s pid=%d. Use BashOutput to read output.", id, ec.Process.Pid),
		Metadata: map[string]any{"shell_id": id, "pid": ec.Process.Pid},
	}, nil
}

// ShellStatus reports the current state of a background shell.
type ShellStatus struct {
	ID        string
	Status    string
	Exit      int
	Started   time.Time
	Ended     time.Time
	Output    string
	Truncated bool
}

// Output returns the increment of stdout+stderr from a background shell
// since the last poll. Used by the BashOutput tool.
func (t *BashTool) Output(id string) (ShellStatus, error) {
	t.bgMu.Lock()
	defer t.bgMu.Unlock()
	entry, ok := t.bgs[id]
	if !ok {
		return ShellStatus{}, fmt.Errorf("unknown shell_id: %s", id)
	}
	data, truncated := entry.buf.ReadFrom(entry.cursor)
	entry.cursor += len(data)
	return ShellStatus{
		ID: id, Status: entry.status, Exit: entry.exit,
		Started: entry.startedAt, Ended: entry.endedAt,
		Output: string(data), Truncated: truncated,
	}, nil
}

// Kill terminates a background shell.
func (t *BashTool) Kill(id string) error {
	t.bgMu.Lock()
	entry, ok := t.bgs[id]
	if !ok {
		t.bgMu.Unlock()
		return fmt.Errorf("unknown shell_id: %s", id)
	}
	if entry.status != "running" {
		t.bgMu.Unlock()
		return nil
	}
	entry.killedByUs = true
	t.bgMu.Unlock()
	entry.cancel()
	killProcessGroup(entry.cmd, syscall.SIGTERM)
	time.Sleep(50 * time.Millisecond)
	killProcessGroup(entry.cmd, syscall.SIGKILL)
	return nil
}

// KillAll tears down every background shell this tool has spawned.
// Called by NativeAgentRunner.Shutdown so zombies don't persist.
func (t *BashTool) KillAll() {
	t.bgMu.Lock()
	type shellSnapshot struct {
		id     string
		pid    int
		status string
	}
	shells := make([]shellSnapshot, 0, len(t.bgs))
	for id, entry := range t.bgs {
		pid := 0
		if entry.cmd != nil && entry.cmd.Process != nil {
			pid = entry.cmd.Process.Pid
		}
		shells = append(shells, shellSnapshot{id: id, pid: pid, status: entry.status})
	}
	t.bgMu.Unlock()

	log.Printf("[bash-shutdown] stopping background shells: count=%d", len(shells))
	for _, shell := range shells {
		startedAt := time.Now()
		log.Printf("[bash-shutdown] stopping shell: id=%s pid=%d status=%s", shell.id, shell.pid, shell.status)
		err := t.Kill(shell.id)
		log.Printf("[bash-shutdown] shell stopped: id=%s pid=%d elapsed=%s err=%v", shell.id, shell.pid, time.Since(startedAt), err)
	}
}

func shellInvocation(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", command}
	}
	return "/bin/sh", []string{"-c", command}
}

func buildBashResult(raw []byte, ec *exec.Cmd, runErr error, timedOut bool) ToolResult {
	out := string(raw)
	if !utf8.Valid(raw) {
		out = strings.ToValidUTF8(out, "�")
	}
	truncated := false
	if len(out) > bashOutputByteLimit || strings.Count(out, "\n") > bashOutputLineLimit {
		out, truncated = truncateOutput(out)
	}

	exitCode := 0
	if ec.ProcessState != nil {
		exitCode = ec.ProcessState.ExitCode()
	} else if runErr != nil {
		exitCode = -1
	}

	var prefix string
	if timedOut {
		prefix = "[command timed out]\n"
	}
	if runErr != nil && !timedOut {
		if _, ok := runErr.(*exec.ExitError); !ok {
			prefix = fmt.Sprintf("[execution error: %v]\n", runErr)
		}
	}

	content := prefix + out
	if truncated {
		content += "\n(output truncated)"
	}
	if exitCode != 0 {
		content += fmt.Sprintf("\n(exit code: %d)", exitCode)
	}
	return ToolResult{
		Content: content,
		Metadata: map[string]any{
			"exit_code": exitCode,
			"truncated": truncated,
			"timed_out": timedOut,
		},
	}
}

func truncateOutput(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	if len(lines) <= bashOutputKeepHeadLines+bashOutputKeepTailLines {
		// Size-based truncation only.
		if len(s) <= bashOutputByteLimit {
			return s, false
		}
		return s[:bashOutputByteLimit/2] + "\n... <output truncated> ...\n" + s[len(s)-bashOutputByteLimit/2:], true
	}
	head := strings.Join(lines[:bashOutputKeepHeadLines], "\n")
	tail := strings.Join(lines[len(lines)-bashOutputKeepTailLines:], "\n")
	return head + "\n... <" + fmt.Sprintf("%d", len(lines)-bashOutputKeepHeadLines-bashOutputKeepTailLines) + " lines truncated> ...\n" + tail, true
}

// ringBuffer is an append-only buffer capped at `cap` bytes; writes
// beyond `cap` overwrite the oldest data. It supports read-from-cursor
// so BashOutput can poll the incremental tail.
type ringBuffer struct {
	mu   sync.Mutex
	data []byte
	cap  int
	// total bytes written (never shrinks); used to answer "how many
	// bytes have been dropped from the head?".
	total int
}

func newRingBuffer(cap int) *ringBuffer {
	return &ringBuffer{data: make([]byte, 0, cap), cap: cap}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total += len(p)
	combined := append(r.data, p...)
	if len(combined) > r.cap {
		combined = combined[len(combined)-r.cap:]
	}
	r.data = combined
	return len(p), nil
}

// ReadFrom returns the bytes written since the absolute cursor position,
// along with a boolean indicating whether any data was dropped (cursor
// lagged beyond the ring's head).
func (r *ringBuffer) ReadFrom(cursor int) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	head := r.total - len(r.data)
	dropped := false
	if cursor < head {
		cursor = head
		dropped = true
	}
	if cursor >= r.total {
		return nil, dropped
	}
	start := cursor - head
	out := make([]byte, r.total-cursor)
	copy(out, r.data[start:])
	return out, dropped
}
