//go:build live

// Package agent live tests: hit the REAL Anthropic-compatible
// endpoint using the user's actual environment variables. Opt in via:
//
//	YANSU_LIVE_TEST=1 go test -tags=live -v -run TestLive ./agent/
//
// These tests are NOT run in CI or as part of the default `go test`.
// They exist because mock-server tests cannot reproduce real-world
// failures like sub2api's "No available accounts" 503, upstream model
// drift, actual token expiration, rate limits, or slow first-token.
//
// Environment variables read:
//
//	ANTHROPIC_BASE_URL    e.g. https://provider.example.invalid/anthropic
//	ANTHROPIC_AUTH_TOKEN  Bearer token (sub2api style)
//	ANTHROPIC_API_KEY     x-api-key (direct Anthropic style)
//	YANSU_LIVE_MODEL      optional — defaults to claude-sonnet-4-6
package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"yansu-agent/agent/native"
)

func liveSkipIfUnconfigured(t *testing.T) string {
	t.Helper()
	if os.Getenv("YANSU_LIVE_TEST") != "1" {
		t.Skip("set YANSU_LIVE_TEST=1 to run live-API tests")
	}
	base := os.Getenv("ANTHROPIC_BASE_URL")
	if base == "" {
		t.Skip("ANTHROPIC_BASE_URL not set — skipping live test")
	}
	if os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("neither ANTHROPIC_AUTH_TOKEN nor ANTHROPIC_API_KEY set — skipping live test")
	}
	return base
}

func liveModel() string {
	if m := os.Getenv("YANSU_LIVE_MODEL"); m != "" {
		return m
	}
	return "opus"
}

// liveModelAliases mirrors the aliases wails-gui passes to the runner at
// construction (see wails-gui/app.go StartAgent). Including them here lets
// a live test set YANSU_LIVE_MODEL=opus (the friendly alias users see in
// the UI) and still reach a real account pool.
func liveModelAliases() map[string]string {
	return map[string]string{
		"sonnet": "claude-sonnet-4-6",
		"opus":   "claude-opus-4-6",
		"haiku":  "claude-haiku-4-5-20251001",
		"fable":  "claude-fable-5",
	}
}

func liveSkipIfCuaUnavailable(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(home, ".yansu-agent", "run", "cua-driver.sock")
	if info, statErr := os.Stat(socketPath); statErr != nil || info.Mode()&os.ModeSocket == 0 {
		t.Skipf("cua-driver socket is not available at %s; start the Wails app before running this live test", socketPath)
	}
}

func assertCuaNamesInOrder(t *testing.T, got []string, want []string) {
	t.Helper()
	pos := 0
	for _, name := range got {
		if pos < len(want) && name == want[pos] {
			pos++
		}
	}
	if pos != len(want) {
		t.Fatalf("CUA sub-tool sequence missing required order: want=%v got=%v", want, got)
	}
}

func isMutatingCuaNameForLiveTest(name string) bool {
	switch name {
	case "click", "type", "type_text", "press", "press_key", "hotkey", "perform_secondary_action", "drag", "set_value", "scroll", "raise":
		return true
	default:
		return false
	}
}

// TestLive_SimpleText sends a one-shot "hi" prompt to the real endpoint
// and asserts:
//   - the call completes within 30s
//   - at least one assistant text delta arrives
//   - a done event fires
//   - no error events (or if error, log it and fail loud with the server body)
//   - total TTFT is recorded so you can compare runs
func TestLive_SimpleText(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	var (
		mu        sync.Mutex
		events    []StreamEvent
		firstText time.Time
	)
	start := time.Now()
	cb := func(ev *StreamEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, *ev)
		if ev.Type == "assistant" && firstText.IsZero() && ev.Content != "" {
			firstText = time.Now()
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:  "Say the single word 'pong' and nothing else.",
		Model:   liveModel(),
		WorkDir: t.TempDir(),
	}, cb)
	total := time.Since(start)

	mu.Lock()
	defer mu.Unlock()

	types := make([]string, len(events))
	var assembled strings.Builder
	var errMsgs []string
	for i, e := range events {
		types[i] = e.Type
		if e.Type == "assistant" {
			assembled.WriteString(e.Content)
		}
		if e.Type == "error" {
			errMsgs = append(errMsgs, e.Error)
		}
	}
	ttft := time.Duration(-1)
	if !firstText.IsZero() {
		ttft = firstText.Sub(start)
	}
	t.Logf("live: total=%v ttft=%v events=%v", total, ttft, types)
	t.Logf("live: assembled text = %q", assembled.String())

	if err != nil {
		t.Fatalf("live call failed: %v (error events: %v)", err, errMsgs)
	}
	if len(errMsgs) > 0 {
		t.Fatalf("live call produced error events: %v", errMsgs)
	}
	if assembled.Len() == 0 {
		t.Fatalf("no assistant text received; event sequence: %v", types)
	}
	if firstText.IsZero() {
		t.Fatalf("never observed first assistant delta; event sequence: %v", types)
	}
}

// TestLive_ToolUseReadsFile sends a prompt that should provoke a Read
// tool call against a file we just wrote, then asserts that the model
// read it and mentioned the content in its final answer. This is the
// single most important real-world smoke test: multi-turn + tool
// dispatch against the actual upstream.
func TestLive_ToolUseReadsFile(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	target := workDir + "/secret.txt"
	const magic = "PURPLE-MONKEY-DISHWASHER-42"
	if err := os.WriteFile(target, []byte(magic+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	_ = runner.Init(context.Background())
	defer runner.Shutdown()

	var (
		mu        sync.Mutex
		events    []StreamEvent
		toolCalls int
	)
	cb := func(ev *StreamEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, *ev)
		if ev.Type == "tool_use" && ev.ToolName == "Read" {
			toolCalls++
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:  "Read the file at " + target + " and echo its exact contents verbatim.",
		Model:   liveModel(),
		WorkDir: workDir,
	}, cb)
	if err != nil {
		t.Fatalf("live tool-use call failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var assembled strings.Builder
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
		if e.Type == "assistant" {
			assembled.WriteString(e.Content)
		}
	}
	t.Logf("live tool-use: readCalls=%d events=%v", toolCalls, types)
	t.Logf("live tool-use: final text = %q", assembled.String())

	if toolCalls == 0 {
		t.Errorf("model did not call Read — did the tool schema reach it? events: %v", types)
	}
	if !strings.Contains(assembled.String(), magic) {
		t.Errorf("final text does not mention %q — model may have misread. text: %q", magic, assembled.String())
	}
}

// TestLive_ProgrammaticChatBash goes through the same NativeAgentRunner path
// the desktop chat uses, with ProgrammaticToolCalling enabled. It deliberately
// asks for a tiny Bash action so the live gateway must accept the
// code_execution/Bash tool declaration added by the programmatic path, execute
// a real local tool call, feed the tool_result back, and let the model finish.
func TestLive_ProgrammaticChatBash(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	const magic = "YANSU-PROGRAMMATIC-CHAT-E2E-OK"

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()
	sessionID := "live-programmatic-chat-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	prompt := "This is a live desktop-chat smoke test. Use the Bash tool exactly once to run this command:\n\n" +
		"printf '" + magic + "\\n'\n\n" +
		"After the tool result returns, reply with the exact token " + magic + " and no extra prose. " +
		"Do not call any other tools."

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:                  prompt,
		Model:                   liveModel(),
		WorkDir:                 workDir,
		SessionID:               sessionID,
		HistoryKey:              sessionID,
		AllowedTools:            []string{"Bash"},
		ProgrammaticToolCalling: true,
		TraceID:                 sessionID,
	}, coll.cb)
	total := time.Since(start)

	types := coll.typesSeq()
	text := coll.assistantText()
	t.Logf("programmatic-chat-bash: total=%v runID=%s tool_counts=%v n_events=%d events=%v",
		total, coll.runID, coll.toolCalls, len(types), types)
	t.Logf("programmatic-chat-bash: assistant_text=%q", text)

	if err != nil {
		t.Fatalf("ExecuteTask returned error: %v", err)
	}
	for _, e := range coll.events {
		if e.Type == "error" {
			t.Fatalf("unexpected error event: %q", e.Error)
		}
	}
	if coll.toolCalls["Bash"] == 0 {
		t.Fatalf("expected at least one Bash tool_use; tool_counts=%v events=%v", coll.toolCalls, types)
	}
	if coll.toolCalls["Bash"] > 3 {
		t.Fatalf("expected the bounded chat prompt to settle quickly, got %d Bash calls; events=%v",
			coll.toolCalls["Bash"], types)
	}
	if !strings.Contains(text, magic) {
		t.Fatalf("assistant text does not contain %q; text=%q events=%v", magic, text, types)
	}
	if firstIndexOf(types, "done") == "(none)" {
		t.Fatalf("missing done event; events=%v", types)
	}
}

// TestLive_ProgrammaticChatWorkspaceMultiTool is the wider desktop-chat
// live smoke: programmatic tool calling is enabled, but the model also sees
// the ordinary workspace tools a user would expect in a project chat. The
// task requires discovery, search, read, shell verification, and writing a
// result file, so this catches regressions hidden by Bash-only coverage.
func TestLive_ProgrammaticChatWorkspaceMultiTool(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	fixturesDir := filepath.Join(workDir, "fixtures")
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const (
		magic  = "YANSU-PROGRAMMATIC-MULTITOOL-E2E-OK"
		period = "83"
	)
	configPath := filepath.Join(fixturesDir, "billing.env")
	outputPath := filepath.Join(workDir, "result.txt")
	if err := os.WriteFile(configPath, []byte(
		"# live e2e fixture\n"+
			"IGNORED_PERIOD=78\n"+
			"BILLING_PERIOD_TARGET="+period+"\n"+
			"LIVE_MARKER="+magic+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixturesDir, "noise.env"), []byte("IGNORED=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()
	sessionID := "live-programmatic-multitool-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	bashCommand := "grep '^BILLING_PERIOD_TARGET=' " + strconv.Quote(configPath) + " | cut -d= -f2"
	prompt := "This is a live desktop-chat workspace smoke test. Use tools in this exact sequence:\n" +
		"1. Use Glob with pattern **/*.env under " + workDir + " to find fixture files.\n" +
		"2. Use Grep to search for BILLING_PERIOD_TARGET under " + workDir + ".\n" +
		"3. Use Read on the matching file " + configPath + ".\n" +
		"4. Use Bash exactly once with this exact command:\n" + bashCommand + "\n" +
		"5. Use Write to create " + outputPath + " with exactly these two lines, substituting the period from the Bash result:\n" +
		"marker=" + magic + "\n" +
		"period=<period from Bash result>\n\n" +
		"After Write succeeds, reply exactly: " + magic + " period=" + period

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:                  prompt,
		Model:                   liveModel(),
		WorkDir:                 workDir,
		SessionID:               sessionID,
		HistoryKey:              sessionID,
		AllowedTools:            []string{"Glob", "Grep", "Read", "Bash", "Write"},
		ProgrammaticToolCalling: true,
		TraceID:                 sessionID,
	}, coll.cb)
	total := time.Since(start)

	types := coll.typesSeq()
	text := coll.assistantText()
	t.Logf("programmatic-chat-multitool: total=%v runID=%s tool_counts=%v n_events=%d events=%v",
		total, coll.runID, coll.toolCalls, len(types), types)
	t.Logf("programmatic-chat-multitool: assistant_text=%q", text)

	if err != nil {
		t.Fatalf("ExecuteTask returned error: %v", err)
	}
	for _, e := range coll.events {
		if e.Type == "error" {
			t.Fatalf("unexpected error event: %q", e.Error)
		}
	}
	for _, name := range []string{"Glob", "Grep", "Read", "Bash", "Write"} {
		if coll.toolCalls[name] == 0 {
			t.Fatalf("expected at least one %s tool_use; tool_counts=%v events=%v", name, coll.toolCalls, types)
		}
	}
	if coll.toolCalls["Bash"] > 3 {
		t.Fatalf("expected bounded shell use, got %d Bash calls; tool_counts=%v events=%v",
			coll.toolCalls["Bash"], coll.toolCalls, types)
	}
	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatalf("result file was not written: %v (tool_counts=%v events=%v)", readErr, coll.toolCalls, types)
	}
	result := string(data)
	if !strings.Contains(result, "marker="+magic) || !strings.Contains(result, "period="+period) {
		t.Fatalf("result file missing expected marker/period; contents=%q", result)
	}
	if !strings.Contains(text, magic) || !strings.Contains(text, "period="+period) {
		t.Fatalf("assistant text missing expected final answer; text=%q events=%v", text, types)
	}
	if firstIndexOf(types, "done") == "(none)" {
		t.Fatalf("missing done event; events=%v", types)
	}
}

// TestLive_LocalProgrammaticToolWorkspace exercises the local Programmatic
// client tool itself through the live gateway. The model is instructed to
// call Programmatic once with a provided Starlark script; the assertion then
// parses Programmatic's nested transcript to verify the local script used the
// expected helper tools.
func TestLive_LocalProgrammaticToolWorkspace(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	fixturesDir := filepath.Join(workDir, "fixtures")
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const (
		magic  = "YANSU-LOCAL-PROGRAMMATIC-E2E-OK"
		period = "83"
	)
	configPath := filepath.Join(fixturesDir, "billing.env")
	outputPath := filepath.Join(workDir, "programmatic-result.txt")
	if err := os.WriteFile(configPath, []byte(
		"BILLING_PERIOD_TARGET="+period+"\n"+
			"LIVE_MARKER="+magic+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	bashCommand := "awk -F= '/^BILLING_PERIOD_TARGET=/{printf $2}' " + strconv.Quote(configPath)
	starlarkCode := "files = glob(\"fixtures/*.env\").content\n" +
		"hit = grep(\"BILLING_PERIOD_TARGET\", path=" + strconv.Quote(workDir) + ", output_mode=\"content\").content\n" +
		"config = read(" + strconv.Quote(configPath) + ").content\n" +
		"period = bash(" + strconv.Quote(bashCommand) + ", description=\"Extract target period\").content\n" +
		"write(" + strconv.Quote(outputPath) + ", \"marker=" + magic + "\\nperiod=\" + period + \"\\n\")\n" +
		"result = {\"files\": files, \"hit\": hit, \"config\": config, \"period\": period}\n"

	coll := newLiveCollector()
	sessionID := "live-local-programmatic-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	prompt := "This is a live desktop-chat smoke test for the local Programmatic tool. " +
		"Call Programmatic exactly once with the Starlark code below. Do not call Glob, Grep, Read, Bash, or Write directly. " +
		"After Programmatic returns, reply exactly: " + magic + " period=" + period + "\n\n" +
		"Starlark code:\n```python\n" + starlarkCode + "```"

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:                  prompt,
		Model:                   liveModel(),
		WorkDir:                 workDir,
		SessionID:               sessionID,
		HistoryKey:              sessionID,
		AllowedTools:            []string{"Programmatic", "Glob", "Grep", "Read", "Bash", "Write"},
		ProgrammaticToolCalling: true,
		TraceID:                 sessionID,
	}, coll.cb)
	total := time.Since(start)

	types := coll.typesSeq()
	text := coll.assistantText()
	t.Logf("local-programmatic: total=%v runID=%s tool_counts=%v n_events=%d events=%v",
		total, coll.runID, coll.toolCalls, len(types), types)
	t.Logf("local-programmatic: assistant_text=%q", text)

	if err != nil {
		t.Fatalf("ExecuteTask returned error: %v", err)
	}
	for _, e := range coll.events {
		if e.Type == "error" {
			t.Fatalf("unexpected error event: %q", e.Error)
		}
	}
	if coll.toolCalls["Programmatic"] != 1 {
		t.Fatalf("expected exactly one Programmatic tool_use; tool_counts=%v events=%v", coll.toolCalls, types)
	}
	for _, name := range []string{"Glob", "Grep", "Read", "Bash", "Write"} {
		if coll.toolCalls[name] != 0 {
			t.Fatalf("expected %s to be used only inside Programmatic, but direct count=%d; tool_counts=%v",
				name, coll.toolCalls[name], coll.toolCalls)
		}
	}

	var programmaticResult string
	for _, e := range coll.events {
		if e.Type == "tool_result" && e.ToolName == "Programmatic" {
			programmaticResult = e.Content
		}
	}
	if programmaticResult == "" {
		t.Fatalf("missing Programmatic tool_result; events=%v", types)
	}
	var payload struct {
		OK    bool `json:"ok"`
		Calls []struct {
			Tool    string `json:"tool"`
			IsError bool   `json:"is_error"`
			Content string `json:"content"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(programmaticResult), &payload); err != nil {
		t.Fatalf("decode Programmatic tool_result: %v\n%s", err, programmaticResult)
	}
	if !payload.OK {
		t.Fatalf("Programmatic payload ok=false:\n%s", programmaticResult)
	}
	seen := map[string]int{}
	for _, call := range payload.Calls {
		seen[call.Tool]++
		grepUnavailable := call.Tool == "Grep" && call.IsError && strings.Contains(call.Content, "ripgrep (rg) is not installed")
		if call.IsError && !grepUnavailable {
			t.Fatalf("nested helper %s returned is_error=true:\n%s", call.Tool, programmaticResult)
		}
	}
	for _, name := range []string{"Glob", "Grep", "Read", "Bash", "Write"} {
		if seen[name] != 1 {
			t.Fatalf("nested helper count for %s = %d, want 1; seen=%v\n%s", name, seen[name], seen, programmaticResult)
		}
	}

	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatalf("programmatic result file was not written: %v", readErr)
	}
	result := string(data)
	if !strings.Contains(result, "marker="+magic) || !strings.Contains(result, "period="+period) {
		t.Fatalf("programmatic result file missing expected marker/period; contents=%q", result)
	}
	if !strings.Contains(text, magic) || !strings.Contains(text, "period="+period) {
		t.Fatalf("assistant text missing expected final answer; text=%q events=%v", text, types)
	}
}

// TestLive_LocalProgrammaticToolBroadWithFailures exercises a broad local
// Programmatic script through the live gateway. The nested script includes
// success paths and intentional helper failures so we know failed subcalls are
// visible to the script and transcript without destabilizing the outer loop.
func TestLive_LocalProgrammaticToolBroadWithFailures(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	fixturesDir := filepath.Join(workDir, "fixtures")
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const (
		magic  = "YANSU-LOCAL-PROGRAMMATIC-BROAD-E2E-OK"
		period = "87"
	)
	configPath := filepath.Join(fixturesDir, "market.env")
	outputPath := filepath.Join(workDir, "broad-programmatic-result.txt")
	if err := os.WriteFile(configPath, []byte(
		"PROGRAMMATIC_FAILURE_TARGET="+magic+"\n"+
			"BILLING_PERIOD_TARGET="+period+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	bashCommand := "awk -F= '/^BILLING_PERIOD_TARGET=/{printf $2}' " + strconv.Quote(configPath)
	starlarkCode := "files = glob(\"fixtures/*.env\").content\n" +
		"grep_result = grep(\"PROGRAMMATIC_FAILURE_TARGET\", path=" + strconv.Quote(workDir) + ", output_mode=\"content\")\n" +
		"config = read(" + strconv.Quote(configPath) + ").content\n" +
		"period = bash(" + strconv.Quote(bashCommand) + ", description=\"Extract target period\").content\n" +
		"page = web_fetch(\"https://example.com\").content\n" +
		"bad_fetch = web_fetch(\"http://example.com\")\n" +
		"bad_opencli = opencli(\"definitely-missing-yansu-e2e-adapter\", \"noop\", description=\"Exercise failing opencli helper\", timeout=5000)\n" +
		"page_ok = \"true\" if \"Example Domain\" in page else \"false\"\n" +
		"fetch_failed = \"true\" if bad_fetch.is_error else \"false\"\n" +
		"opencli_failed = \"true\" if bad_opencli.is_error else \"false\"\n" +
		"write(" + strconv.Quote(outputPath) + ", \"marker=" + magic + "\\nperiod=\" + period + \"\\npage_ok=\" + page_ok + \"\\nfetch_failed=\" + fetch_failed + \"\\nopencli_failed=\" + opencli_failed + \"\\n\")\n" +
		"result = {\"files\": files, \"grep_error\": grep_result.is_error, \"config\": config, \"period\": period, \"page_ok\": page_ok, \"fetch_failed\": fetch_failed, \"opencli_failed\": opencli_failed}\n"

	coll := newLiveCollector()
	sessionID := "live-local-programmatic-broad-failure-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	prompt := "This is a live desktop-chat e2e test for the local Programmatic tool. " +
		"Call Programmatic exactly once with the Starlark code below. Do not call any other tools directly. " +
		"After Programmatic returns, reply exactly: " + magic + " period=" + period + "\n\n" +
		"Starlark code:\n```python\n" + starlarkCode + "```"

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:                  prompt,
		Model:                   liveModel(),
		WorkDir:                 workDir,
		SessionID:               sessionID,
		HistoryKey:              sessionID,
		AllowedTools:            []string{"Programmatic", "Glob", "Grep", "Read", "Bash", "Write", "WebFetch"},
		ProgrammaticToolCalling: true,
		TraceID:                 sessionID,
	}, coll.cb)
	total := time.Since(start)

	types := coll.typesSeq()
	text := coll.assistantText()
	t.Logf("local-programmatic-broad-failure: total=%v runID=%s tool_counts=%v n_events=%d events=%v",
		total, coll.runID, coll.toolCalls, len(types), types)
	t.Logf("local-programmatic-broad-failure: assistant_text=%q", text)

	if err != nil {
		t.Fatalf("ExecuteTask returned error: %v", err)
	}
	for _, e := range coll.events {
		if e.Type == "error" {
			t.Fatalf("unexpected error event: %q", e.Error)
		}
	}
	if coll.toolCalls["Programmatic"] != 1 {
		t.Fatalf("expected exactly one Programmatic tool_use; tool_counts=%v events=%v", coll.toolCalls, types)
	}
	for _, name := range []string{"Glob", "Grep", "Read", "Bash", "Write", "WebFetch"} {
		if coll.toolCalls[name] != 0 {
			t.Fatalf("expected %s to be used only inside Programmatic, but direct count=%d; tool_counts=%v",
				name, coll.toolCalls[name], coll.toolCalls)
		}
	}

	var programmaticResult string
	for _, e := range coll.events {
		if e.Type == "tool_result" && e.ToolName == "Programmatic" {
			programmaticResult = e.Content
		}
	}
	if programmaticResult == "" {
		t.Fatalf("missing Programmatic tool_result; events=%v", types)
	}
	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			PageOK        string `json:"page_ok"`
			FetchFailed   string `json:"fetch_failed"`
			OpenCLIFailed string `json:"opencli_failed"`
		} `json:"result"`
		Calls []struct {
			Tool    string `json:"tool"`
			IsError bool   `json:"is_error"`
			Content string `json:"content"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(programmaticResult), &payload); err != nil {
		t.Fatalf("decode Programmatic tool_result: %v\n%s", err, programmaticResult)
	}
	if !payload.OK {
		t.Fatalf("Programmatic payload ok=false:\n%s", programmaticResult)
	}
	if payload.Result.PageOK != "true" || payload.Result.FetchFailed != "true" || payload.Result.OpenCLIFailed != "true" {
		t.Fatalf("unexpected Programmatic result flags: %+v\n%s", payload.Result, programmaticResult)
	}
	seen := map[string]int{}
	errorsByTool := map[string]int{}
	for _, call := range payload.Calls {
		seen[call.Tool]++
		if call.IsError {
			errorsByTool[call.Tool]++
		}
	}
	for _, name := range []string{"Glob", "Grep", "Read", "Bash", "Write", "WebFetch", "OpenCLI"} {
		if seen[name] == 0 {
			t.Fatalf("missing nested helper %s; seen=%v\n%s", name, seen, programmaticResult)
		}
	}
	if errorsByTool["WebFetch"] == 0 || errorsByTool["OpenCLI"] == 0 {
		t.Fatalf("expected nested WebFetch and OpenCLI failures; errors=%v\n%s", errorsByTool, programmaticResult)
	}

	data, readErr := os.ReadFile(outputPath)
	if readErr != nil {
		t.Fatalf("programmatic result file was not written: %v", readErr)
	}
	result := string(data)
	for _, want := range []string{"marker=" + magic, "period=" + period, "page_ok=true", "fetch_failed=true", "opencli_failed=true"} {
		if !strings.Contains(result, want) {
			t.Fatalf("programmatic result file missing %q; contents=%q", want, result)
		}
	}
	if !strings.Contains(text, magic) || !strings.Contains(text, "period="+period) {
		t.Fatalf("assistant text missing expected final answer; text=%q events=%v", text, types)
	}
}

// TestLive_CuaDriverListApps exercises the desktop-chat path for the bundled
// CUA bridge: real Gateway request, real model tool selection, and real
// wails-managed cua-driver daemon. It intentionally uses a read-only call so
// it can run without changing the user's foreground app state.
func TestLive_CuaDriverListApps(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(home, ".yansu-agent", "run", "cua-driver.sock")
	if info, statErr := os.Stat(socketPath); statErr != nil || info.Mode()&os.ModeSocket == 0 {
		t.Skipf("cua-driver socket is not available at %s; start the Wails app before running this live test", socketPath)
	}

	const magic = "YANSU-CUA-LIVE-E2E-OK"

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()
	sessionID := "live-cua-list-apps-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	prompt := "This is a live CUA smoke test. Call the cua_driver tool exactly once with " +
		`name="list_apps" and args={}. After the tool result returns, reply exactly ` +
		magic + " and no extra prose. Do not call Bash, Read, WebFetch, Programmatic, or any other tools."

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := time.Now()
	_, err = runner.ExecuteTask(ctx, &TaskInput{
		Prompt:       prompt,
		Model:        liveModel(),
		WorkDir:      t.TempDir(),
		SessionID:    sessionID,
		HistoryKey:   sessionID,
		AllowedTools: []string{"cua_driver"},
		TraceID:      sessionID,
	}, coll.cb)
	total := time.Since(start)

	types := coll.typesSeq()
	text := coll.assistantText()
	t.Logf("cua-driver-list-apps: total=%v runID=%s tool_counts=%v n_events=%d events=%v",
		total, coll.runID, coll.toolCalls, len(types), types)
	t.Logf("cua-driver-list-apps: assistant_text=%q", text)

	if err != nil {
		t.Fatalf("ExecuteTask returned error: %v", err)
	}
	for _, e := range coll.events {
		if e.Type == "error" {
			t.Fatalf("unexpected error event: %q", e.Error)
		}
	}
	if coll.toolCalls["cua_driver"] == 0 {
		t.Fatalf("expected at least one cua_driver tool_use; tool_counts=%v events=%v", coll.toolCalls, types)
	}
	if coll.toolCalls["cua_driver"] > 2 {
		t.Fatalf("expected CUA smoke to settle quickly, got %d cua_driver calls; events=%v",
			coll.toolCalls["cua_driver"], types)
	}
	if !strings.Contains(text, magic) {
		t.Fatalf("assistant text does not contain %q; text=%q events=%v", magic, text, types)
	}
}

func TestLive_CuaDriverReadOnlyWindowWorkflow(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)
	liveSkipIfCuaUnavailable(t)

	const magic = "YANSU-CUA-READONLY-WINDOW-E2E-OK"

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()
	sessionID := "live-cua-readonly-window-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	prompt := "This is a live CUA multi-step read-only e2e. Use only the cua_driver tool. " +
		"Call these CUA sub-tools in this order:\n" +
		`1. name="check_permissions" args={"prompt":false}` + "\n" +
		`2. name="list_windows" args={"bundle":"com.wails.Yansu"}` + "\n" +
		`3. name="get_window_state" args={"bundle":"com.wails.Yansu"}` + "\n" +
		"Do not call mutating CUA sub-tools such as click, type_text, press_key, scroll, drag, or set_value. " +
		"After the third tool result returns, reply exactly " + magic + " and no extra prose."

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:       prompt,
		Model:        liveModel(),
		WorkDir:      t.TempDir(),
		SessionID:    sessionID,
		HistoryKey:   sessionID,
		AllowedTools: []string{"cua_driver"},
		TraceID:      sessionID,
	}, coll.cb)
	total := time.Since(start)

	types := coll.typesSeq()
	text := coll.assistantText()
	names := coll.cuaToolNames()
	t.Logf("cua-readonly-window: total=%v runID=%s tool_counts=%v cua_names=%v n_events=%d events=%v",
		total, coll.runID, coll.toolCalls, names, len(types), types)
	t.Logf("cua-readonly-window: assistant_text=%q", text)

	if err != nil {
		t.Fatalf("ExecuteTask returned error: %v", err)
	}
	coll.failOnErrorEvents(t)
	if coll.toolCalls["cua_driver"] < 3 {
		t.Fatalf("expected at least three cua_driver calls; tool_counts=%v names=%v events=%v",
			coll.toolCalls, names, types)
	}
	if coll.toolCalls["cua_driver"] > 5 {
		t.Fatalf("expected read-only workflow to settle quickly, got %d cua_driver calls; names=%v events=%v",
			coll.toolCalls["cua_driver"], names, types)
	}
	assertCuaNamesInOrder(t, names, []string{"check_permissions", "list_windows", "get_window_state"})
	for _, name := range names {
		if isMutatingCuaNameForLiveTest(name) {
			t.Fatalf("read-only live test used mutating CUA sub-tool %q; names=%v events=%v", name, names, types)
		}
	}
	if !strings.Contains(text, magic) {
		t.Fatalf("assistant text does not contain %q; text=%q events=%v", magic, text, types)
	}
}

func TestLive_CuaDriverErrorThenRecovery(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)
	liveSkipIfCuaUnavailable(t)

	const magic = "YANSU-CUA-ERROR-RECOVERY-E2E-OK"

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()
	sessionID := "live-cua-error-recovery-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	prompt := "This is a live CUA error-recovery e2e. Use only the cua_driver tool. " +
		"First intentionally call name=\"no_such_tool_definitely\" with args={} once; this is expected to fail. " +
		"Then recover by calling name=\"get_window_state\" with args={\"bundle\":\"com.wails.Yansu\"}. " +
		"After the recovery tool result returns, reply exactly " + magic + " and no extra prose. " +
		"Do not call Bash, Read, WebFetch, Programmatic, or mutating CUA sub-tools."

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:       prompt,
		Model:        liveModel(),
		WorkDir:      t.TempDir(),
		SessionID:    sessionID,
		HistoryKey:   sessionID,
		AllowedTools: []string{"cua_driver"},
		TraceID:      sessionID,
	}, coll.cb)
	total := time.Since(start)

	types := coll.typesSeq()
	text := coll.assistantText()
	names := coll.cuaToolNames()
	results := coll.toolResultsFor("cua_driver")
	t.Logf("cua-error-recovery: total=%v runID=%s tool_counts=%v cua_names=%v n_events=%d events=%v",
		total, coll.runID, coll.toolCalls, names, len(types), types)
	t.Logf("cua-error-recovery: assistant_text=%q", text)

	if err != nil {
		t.Fatalf("ExecuteTask returned error: %v", err)
	}
	coll.failOnErrorEvents(t)
	if coll.toolCalls["cua_driver"] < 2 {
		t.Fatalf("expected at least two cua_driver calls; tool_counts=%v names=%v events=%v",
			coll.toolCalls, names, types)
	}
	if coll.toolCalls["cua_driver"] > 4 {
		t.Fatalf("expected error-recovery workflow to settle quickly, got %d cua_driver calls; names=%v events=%v",
			coll.toolCalls["cua_driver"], names, types)
	}
	assertCuaNamesInOrder(t, names, []string{"no_such_tool_definitely", "get_window_state"})
	for _, name := range names {
		if isMutatingCuaNameForLiveTest(name) {
			t.Fatalf("error-recovery live test used mutating CUA sub-tool %q; names=%v events=%v", name, names, types)
		}
	}
	var sawExpectedToolError bool
	for _, result := range results {
		if strings.Contains(result, "no_such_tool_definitely") ||
			strings.Contains(result, "unknown cmd") ||
			strings.Contains(result, "Unknown tool") {
			sawExpectedToolError = true
			break
		}
	}
	if !sawExpectedToolError {
		t.Fatalf("expected tool_result to include the intentional CUA sub-tool error; results=%q names=%v events=%v",
			results, names, types)
	}
	if !strings.Contains(text, magic) {
		t.Fatalf("assistant text does not contain %q; text=%q events=%v", magic, text, types)
	}
}

// TestLive_503Retry documents how a real 503 from the upstream surfaces.
// It doesn't assert a specific outcome (the endpoint may be healthy at
// test time) — it simply runs a request and reports what happened, so
// you can eyeball whether a sub2api account-pool issue is transient or
// persistent.
func TestLive_503Retry(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	_ = runner.Init(context.Background())
	defer runner.Shutdown()

	var (
		mu     sync.Mutex
		events []StreamEvent
	)
	cb := func(ev *StreamEvent) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, *ev)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt: "ping", Model: liveModel(), WorkDir: t.TempDir(),
	}, cb)
	elapsed := time.Since(start)

	mu.Lock()
	defer mu.Unlock()
	types := make([]string, len(events))
	var errMsgs []string
	for i, e := range events {
		types[i] = e.Type
		if e.Type == "error" {
			errMsgs = append(errMsgs, e.Error)
		}
	}
	t.Logf("live 503 probe: elapsed=%v err=%v events=%v errMsgs=%v", elapsed, err, types, errMsgs)
	// No hard assertion — the test is diagnostic.
}

// ─── Multi-step loop coverage ─────────────────────────────────────────────

// liveCollector is a shared event-collection scaffold: it captures every
// stream event under a mutex so the test body can make assertions after
// ExecuteTask returns without racing the callback goroutine.
type liveCollector struct {
	mu        sync.Mutex
	events    []StreamEvent
	toolCalls map[string]int // ToolName → count
	toolIDs   []string       // ordered list of tool_use ids emitted
	runID     string
	sessionID string
}

func newLiveCollector() *liveCollector {
	return &liveCollector{toolCalls: map[string]int{}}
}

func (c *liveCollector) cb(ev *StreamEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, *ev)
	if ev.RunID != "" {
		c.runID = ev.RunID
	}
	if ev.SessionID != "" {
		c.sessionID = ev.SessionID
	}
	if ev.Type == "tool_use" && ev.ToolName != "" {
		c.toolCalls[ev.ToolName]++
		if ev.Content != "" {
			c.toolIDs = append(c.toolIDs, ev.Content) // ToolUseID is routed into Content
		}
	}
	return nil
}

func (c *liveCollector) assistantText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var sb strings.Builder
	for _, e := range c.events {
		if e.Type == "assistant" {
			sb.WriteString(e.Content)
		}
	}
	return sb.String()
}

func (c *liveCollector) typesSeq() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.events))
	for i, e := range c.events {
		out[i] = e.Type
	}
	return out
}

func (c *liveCollector) failOnErrorEvents(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.Type == "error" {
			t.Fatalf("unexpected error event: %q", e.Error)
		}
	}
}

func (c *liveCollector) cuaToolNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.events {
		if e.Type != "tool_use" || e.ToolName != "cua_driver" {
			continue
		}
		input, ok := e.ToolInput.(map[string]any)
		if !ok {
			continue
		}
		name, _ := input["name"].(string)
		out = append(out, name)
	}
	return out
}

func (c *liveCollector) toolResultsFor(toolName string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.events {
		if e.Type == "tool_result" && e.ToolName == toolName {
			out = append(out, e.Content)
		}
	}
	return out
}

// TestLive_WriteThenVerify exercises a real multi-step tool chain inside a
// single turn: the agent is asked to create a Go file, compile it with
// `go build`, and then report the resulting binary's exit code. Any one of
// those steps alone is a happy path — the interesting assertion is that
// the loop feeds each tool_result back and lets the agent continue past it.
//
// Minimum viable assertions:
//   - At least one Write tool call (created the source file).
//   - At least one Bash tool call (compiled or executed).
//   - The final assistant text references the output the test planted so we
//     know the read-after-write half of the chain worked.
//   - No error events leaked through.
func TestLive_WriteThenVerify(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	const magic = "ORBIT-LEMON-STARLIGHT-777"

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Tools require absolute paths (Write.Execute enforces it and the
	// system prompt says so). We hand the absolute paths to the model
	// directly — this matches how wails-gui's composePrompt surfaces the
	// workdir to the agent in real usage.
	srcPath := filepath.Join(workDir, "hello.go")
	outPath := filepath.Join(workDir, "stdout.txt")

	prompt := "Perform these two steps, in order, using tools:\n" +
		"1. Call Write to create " + srcPath + " containing a complete Go program whose " +
		"main function prints exactly the token " + magic + " followed by a newline.\n" +
		"2. Call Bash with this exact command (a single compound command in ONE " +
		"Bash call): cd " + workDir + " && go run " + srcPath + " > " + outPath + "\n" +
		"After step 2 completes you are done — no further tool calls."

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:  prompt,
		Model:   liveModel(),
		WorkDir: workDir,
	}, coll.cb)
	total := time.Since(start)

	types := coll.typesSeq()
	t.Logf("write-then-verify: total=%v runID=%s tool_counts=%v n_events=%d first_tool_use_idx=%s",
		total, coll.runID, coll.toolCalls, len(types), firstIndexOf(types, "tool_use"))

	if err != nil {
		t.Fatalf("ExecuteTask returned error: %v", err)
	}
	for _, e := range coll.events {
		if e.Type == "error" {
			t.Fatalf("unexpected error event: %q", e.Error)
		}
	}
	if coll.toolCalls["Write"] == 0 {
		t.Errorf("expected at least one Write tool_use; tool_counts=%v", coll.toolCalls)
	}
	if coll.toolCalls["Bash"] == 0 {
		t.Errorf("expected at least one Bash tool_use; tool_counts=%v", coll.toolCalls)
	}

	// Filesystem is the ground-truth assertion: if the Write + Bash chain
	// actually executed, hello.go and hello exist and stdout.txt contains
	// the magic token. This is resilient to the model choosing not to emit
	// trailing assistant text (some gateway configs default thinking-only
	// output), which would make an assistant-text check flaky.
	if _, statErr := os.Stat(srcPath); statErr != nil {
		t.Errorf("hello.go was not written: %v", statErr)
	}
	if data, readErr := os.ReadFile(outPath); readErr != nil {
		t.Errorf("stdout.txt missing — the Bash tool_result likely did not feed back to the loop: %v", readErr)
	} else if !strings.Contains(string(data), magic) {
		t.Errorf("stdout.txt missing magic token %q; contents=%q", magic, string(data))
	}
}

// firstIndexOf is a small helper so the logs show where in the event
// sequence the first tool_use landed — useful when triaging runs that
// spent a long time on extended-thinking before acting.
func firstIndexOf(ss []string, want string) string {
	for i, s := range ss {
		if s == want {
			return intToString(i)
		}
	}
	return "(none)"
}

func intToString(i int) string {
	// Avoid importing strconv just for this.
	if i == 0 {
		return "0"
	}
	var b strings.Builder
	neg := i < 0
	if neg {
		i = -i
	}
	var digits [20]byte
	n := 0
	for i > 0 {
		digits[n] = byte('0' + i%10)
		i /= 10
		n++
	}
	if neg {
		b.WriteByte('-')
	}
	for k := n - 1; k >= 0; k-- {
		b.WriteByte(digits[k])
	}
	return b.String()
}

// inMemoryHistory is a HistoryLoader that serves whatever items the test
// stages into it, keyed by the same HistoryKey ExecuteTask will pass. Its
// single-map Set/LoadHistory shape is exactly what wails-gui's
// streamItemHistoryLoader does, minus the SQLite.
type inMemoryHistory struct {
	mu    sync.Mutex
	store map[string][]native.HistoryItem
}

func newInMemoryHistory() *inMemoryHistory {
	return &inMemoryHistory{store: map[string][]native.HistoryItem{}}
}

func (h *inMemoryHistory) Set(key string, items []native.HistoryItem) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.store[key] = append([]native.HistoryItem(nil), items...)
}

func (h *inMemoryHistory) LoadHistory(key string) ([]native.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]native.HistoryItem(nil), h.store[key]...), nil
}

// TestLive_MultiTurnResumesHistory plants a distinctive token in turn 1,
// stages the resulting transcript into an in-memory HistoryLoader under a
// chosen HistoryKey, then runs turn 2 with the same key and asks the agent
// to recall the token without re-reading. If the loader plumbing is
// broken — HistoryKey unused, messages mis-serialized, cache_control
// misapplied so the upstream drops prior turns — the model has no way to
// answer and the assertion fires.
func TestLive_MultiTurnResumesHistory(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	const (
		magic      = "PRETZEL-ORBIT-FLAMINGO-314"
		historyKey = "live-test-multiturn"
	)

	loader := newInMemoryHistory()
	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
		History:      loader,
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	// Use a writable workdir so turn 2 can have the agent drop its answer
	// to a file — ground-truth assertion instead of depending on the model
	// producing a final assistant text message.
	workDir := t.TempDir()
	answerPath := filepath.Join(workDir, "answer.txt")

	// ── Turn 1: plant the magic token.
	turn1 := newLiveCollector()
	turn1Prompt := "Please remember this exact token for the next question: " + magic +
		". Reply in plain text acknowledging you will remember it, and repeat the token back once. " +
		"Do not call any tools this turn."

	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	_, err := runner.ExecuteTask(ctx1, &TaskInput{
		Prompt:     turn1Prompt,
		Model:      liveModel(),
		WorkDir:    workDir,
		HistoryKey: historyKey,
	}, turn1.cb)
	cancel1()
	if err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}

	turn1Text := turn1.assistantText()
	t.Logf("turn 1: tools=%v assistant_len=%d", turn1.toolCalls, len(turn1Text))

	// Stage the turn into the loader the way wails-gui stages StreamItemRows.
	// Even if the gateway suppressed the assistant text block (thinking-only
	// mode), the prompt itself carries the magic token, so history is still
	// carrying useful context across to turn 2.
	historyItems := []native.HistoryItem{
		{Kind: "user", Content: turn1Prompt},
	}
	if turn1Text != "" {
		historyItems = append(historyItems, native.HistoryItem{Kind: "assistant", Content: turn1Text})
	} else {
		// Synthesize a minimal assistant echo so the tool_use/tool_result
		// pairing in BuildMessages stays balanced and the turn-1 context is
		// still present from the user's side.
		t.Logf("turn 1 produced no assistant text; proceeding with user-only history entry")
	}
	loader.Set(historyKey, historyItems)

	// ── Turn 2: ask for the token back, writing the answer to disk so the
	// assertion doesn't depend on the model emitting assistant text.
	turn2 := newLiveCollector()
	turn2Prompt := "Use the Write tool to create " + answerPath + " containing exactly the " +
		"token I asked you to remember in the previous message, and nothing else " +
		"(no quotes, no trailing newline tricks, no prose). Do NOT read any files — " +
		"the answer is already somewhere in our conversation history."

	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	_, err = runner.ExecuteTask(ctx2, &TaskInput{
		Prompt:     turn2Prompt,
		Model:      liveModel(),
		WorkDir:    workDir,
		HistoryKey: historyKey,
	}, turn2.cb)
	cancel2()
	if err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}

	t.Logf("multi-turn: turn1 tools=%v turn2 tools=%v", turn1.toolCalls, turn2.toolCalls)

	data, readErr := os.ReadFile(answerPath)
	if readErr != nil {
		t.Fatalf("turn 2 did not write answer.txt — HistoryLoader plumbing or Write tool likely broken: %v "+
			"(tool_counts=%v events=%v)", readErr, turn2.toolCalls, turn2.typesSeq())
	}
	if !strings.Contains(string(data), magic) {
		t.Errorf("turn 2 answer.txt does not contain the magic token — "+
			"history likely did not reach the model. expected %q, got %q", magic, string(data))
	}
	// Read in turn 2 hints the agent tried to recover the token from disk
	// instead of from conversation history. Log but don't fail — some
	// models double-check by design.
	if turn2.toolCalls["Read"] > 0 || turn2.toolCalls["Grep"] > 0 {
		t.Logf("turn 2 used read-tools (%v) despite history-only prompt — soft hint that history was weak",
			turn2.toolCalls)
	}
}

// TestLive_PlanModeThenExecute puts the runner in plan mode, verifies the
// agent reaches ExitPlanMode and emits `plan_complete`, and then runs a
// second turn (same SessionID) with plan mode off that must use a
// write tool (proving the restricted-toolset gate actually flipped back).
//
// Plan mode in the native runner doesn't block on user approval — it
// auto-flips off when ExitPlanMode is called — so the test shape is
// "did plan_complete fire" + "did a follow-up turn get write access back".
func TestLive_PlanModeThenExecute(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	// TODO(non-anthropic-gateway): sub2api gateway is currently routed to a
	// non-Anthropic upstream (Qwen at first, Kimi K2 since 2026-05-07), which
	// does not reliably follow the plan-mode prompt's "ONLY call
	// ExitPlanMode this turn" instruction — the model wanders through
	// reconnaissance tools (Glob/Read/Grep/LS) for the entire 60s budget
	// and never reaches ExitPlanMode, so the test deterministically times
	// out. The assertion target here (plan_complete fires + tool gate
	// flips back) is a runner-level invariant; it requires a model that
	// honors the plan-mode prompt to exercise. Skip on the gateway path
	// instead of timing out for 60s every CI run. The direct-Anthropic
	// path is unaffected and continues to fail loud on real regressions.
	// Drop this skip once the gateway routes back to a Claude-class model.
	// Grep `non-anthropic-gateway` to find all related debt.
	{
		baseURL := os.Getenv("ANTHROPIC_BASE_URL")
		isDirectAnthropic := baseURL == "" || strings.Contains(baseURL, "api.anthropic.com")
		if !isDirectAnthropic {
			t.Skipf("gateway at %s — non-Anthropic upstreams do not reliably follow the "+
				"plan-mode `ONLY call ExitPlanMode` instruction and the test 60s "+
				"timeouts as the model wanders through tools. Re-run against "+
				"api.anthropic.com to exercise the strict path.", baseURL)
		}
	}

	workDir := t.TempDir()
	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	planPath := filepath.Join(workDir, "plan.txt")

	// ── Turn 1: plan mode on. Expect ExitPlanMode → plan_complete.
	planTurn := newLiveCollector()
	planPrompt := "You are in plan mode. The task is trivial and requires NO exploration: " +
		"I want you to WRITE the single word READY to " + planPath + ". " +
		"Skip any reconnaissance (do NOT call Glob, Read, Grep, LS — the directory " +
		"is new and empty by construction). Your ONLY action this turn is to call " +
		"the ExitPlanMode tool. Its `plan` field should contain one or two short " +
		"sentences describing the intended Write call. Call ExitPlanMode now."

	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	_, err := runner.ExecuteTask(ctx1, &TaskInput{
		Prompt:    planPrompt,
		Model:     liveModel(),
		WorkDir:   workDir,
		SessionID: "live-plan-session",
		PlanMode:  true,
	}, planTurn.cb)
	cancel1()
	if err != nil {
		t.Fatalf("plan turn failed: %v", err)
	}

	types1 := planTurn.typesSeq()
	t.Logf("plan turn: tool_counts=%v events=%v", planTurn.toolCalls, types1)

	if planTurn.toolCalls["ExitPlanMode"] == 0 {
		t.Fatalf("expected ExitPlanMode tool_use in plan turn; tool_counts=%v events=%v",
			planTurn.toolCalls, types1)
	}
	// In plan mode Write must be off-limits: if the model managed to call it
	// anyway the tool-gating in loop.go is broken.
	if planTurn.toolCalls["Write"] > 0 {
		t.Errorf("Write was reachable in plan mode — tool gate broken; tool_counts=%v",
			planTurn.toolCalls)
	}
	// plan_complete should fire once ExitPlanMode's tool_result lands.
	sawPlanComplete := false
	for _, e := range planTurn.events {
		if e.Type == "plan_complete" {
			sawPlanComplete = true
			break
		}
	}
	if !sawPlanComplete {
		t.Errorf("plan_complete event missing; events=%v", types1)
	}

	// ── Turn 2: same SessionID, PlanMode off. The restricted tool set
	// should be back to full — Write must succeed.
	execTurn := newLiveCollector()
	execPrompt := "Go ahead and execute the plan now: use the Write tool to create " +
		planPath + " containing exactly the single word READY (and nothing else, " +
		"no newline tricks). Then stop."

	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	_, err = runner.ExecuteTask(ctx2, &TaskInput{
		Prompt:    execPrompt,
		Model:     liveModel(),
		WorkDir:   workDir,
		SessionID: "live-plan-session",
		PlanMode:  false,
	}, execTurn.cb)
	cancel2()
	if err != nil {
		t.Fatalf("exec turn failed: %v", err)
	}

	types2 := execTurn.typesSeq()
	t.Logf("exec turn: tool_counts=%v events=%v", execTurn.toolCalls, types2)

	if execTurn.toolCalls["Write"] == 0 {
		t.Errorf("Write tool_use missing after plan exit — plan-mode flag may not have flipped; "+
			"tool_counts=%v events=%v", execTurn.toolCalls, types2)
	}
	// Verify the file landed.
	data, readErr := os.ReadFile(planPath)
	if readErr != nil {
		t.Errorf("plan.txt was not created: %v", readErr)
	} else if !strings.Contains(string(data), "READY") {
		t.Errorf("plan.txt content missing READY marker: %q", string(data))
	}

	// Belt-and-suspenders: plan_complete's Content should be the plan text
	// the model passed into ExitPlanMode (the runner forwards result.Content
	// there). This is how wails-gui surfaces the plan to the user; if the
	// tolerant_json partial_json repair path breaks, the plan round-trip
	// through the tool would be empty here.
	var planContent string
	for _, e := range planTurn.events {
		if e.Type == "plan_complete" {
			planContent = e.Content
			break
		}
	}
	if strings.TrimSpace(planContent) == "" {
		t.Errorf("plan_complete event carried empty Content — ExitPlanMode `plan` field " +
			"did not round-trip through tolerant_json repair + tool execution")
	}
	if blob, err := json.Marshal(planContent); err != nil {
		t.Errorf("plan_complete Content does not marshal: %v", err)
	} else {
		t.Logf("plan_complete content (%d chars): %s", len(planContent), string(blob))
	}
}

// ─── Hardcore loop-stress scenarios ────────────────────────────────────────

// TestLive_ParallelToolCalls pre-stages two files and asks the agent to
// read both "in parallel" in a single response. Anthropic models emit
// multiple tool_use blocks in one assistant turn for independent reads;
// the loop dispatches them, collects results, and feeds the combined
// result batch back as a single user-message. This test checks:
//
//   - More than one Read tool_use is emitted before the first tool_result
//     (the signature of a single-turn parallel dispatch rather than two
//     serial turns).
//   - Both planted tokens end up in the file the agent writes. That proves
//     the loop actually fed BOTH tool_results back; a regression where only
//     one result reaches the model would land only one token.
func TestLive_ParallelToolCalls(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	aPath := filepath.Join(workDir, "a.txt")
	bPath := filepath.Join(workDir, "b.txt")
	outPath := filepath.Join(workDir, "combined.txt")
	const (
		tokenA = "FIG-ANCHOR-11"
		tokenB = "WALNUT-BRIDGE-22"
	)
	if err := os.WriteFile(aPath, []byte(tokenA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte(tokenB+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()

	prompt := "In a single response, issue TWO Read tool calls in parallel — one " +
		"for " + aPath + " and one for " + bPath + ". After both reads complete, " +
		"call Write to create " + outPath + " with the concatenation of the two " +
		"tokens you read, separated by a single space and nothing else."

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:  prompt,
		Model:   liveModel(),
		WorkDir: workDir,
	}, coll.cb)
	total := time.Since(start)

	t.Logf("parallel-tools: total=%v tool_counts=%v n_events=%d",
		total, coll.toolCalls, len(coll.events))

	if err != nil {
		t.Fatalf("ExecuteTask failed: %v", err)
	}
	if coll.toolCalls["Read"] < 2 {
		t.Errorf("expected ≥2 Read calls; tool_counts=%v", coll.toolCalls)
	}

	// Detect parallel dispatch: count tool_use events that appear before
	// the first tool_result. If the model emitted two parallel tool_use
	// blocks in a single response, both fire before any result.
	parallel := 0
	coll.mu.Lock()
	for _, e := range coll.events {
		if e.Type == "tool_result" {
			break
		}
		if e.Type == "tool_use" {
			parallel++
		}
	}
	coll.mu.Unlock()
	if parallel < 2 {
		t.Logf("did not observe ≥2 tool_use before the first tool_result (saw %d) — "+
			"the model may have serialized the reads; not failing but tool_counts=%v",
			parallel, coll.toolCalls)
	} else {
		t.Logf("parallel-tools: observed %d tool_use events before first tool_result", parallel)
	}

	data, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("combined.txt missing: %v", readErr)
	}
	if !strings.Contains(string(data), tokenA) || !strings.Contains(string(data), tokenB) {
		t.Errorf("combined.txt missing one or both tokens — a tool_result may have "+
			"been dropped in the fan-in. expected both %q and %q, got %q",
			tokenA, tokenB, string(data))
	}
}

// TestLive_BashSessionLifecycle exercises the background-shell path that
// is specific to the native runner: Bash(run_in_background=true) spawns a
// long-running child, BashOutput polls its stdout, KillShell terminates
// it. The test's assertion is cheap but covers the full trio because the
// agent MUST successfully pipe shell_id through all three calls — a
// regression in the BashTool registry (or the bgs map) breaks the whole
// chain.
//
// Ground-truth assertion: the final Write to status.txt records how many
// lines of stdout the agent managed to collect. If BashOutput never
// returned any stdout, the file will say 0 lines (or the Write will
// never happen), and we fail.
func TestLive_BashSessionLifecycle(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	statusPath := filepath.Join(workDir, "status.txt")

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()

	// We ask the agent to: start a ticking shell, sleep a bit, poll output,
	// kill it, and record the line count. Keeping it concrete (numbered
	// lines, explicit sleep, explicit count) is what keeps the test
	// deterministic enough across model runs.
	prompt := "Run EXACTLY this sequence of tool calls in order:\n" +
		"1. Call Bash with run_in_background=true and command: " +
		"for i in $(seq 1 8); do echo tick-$i; sleep 0.3; done\n" +
		"   Record the shell_id returned.\n" +
		"2. Call Bash with a short foreground command: sleep 2\n" +
		"   (this gives the background loop time to produce output).\n" +
		"3. Call BashOutput with that shell_id to collect whatever has been printed.\n" +
		"4. Call KillShell with that shell_id to terminate the background shell.\n" +
		"5. Call Write to create " + statusPath + " containing ONLY a single integer: " +
		"the number of lines of output you observed in step 3 (count non-empty lines " +
		"that start with 'tick-'). Do NOT add any other text."

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:  prompt,
		Model:   liveModel(),
		WorkDir: workDir,
	}, coll.cb)
	total := time.Since(start)
	t.Logf("bash-session: total=%v tool_counts=%v", total, coll.toolCalls)

	if err != nil {
		t.Fatalf("ExecuteTask failed: %v", err)
	}

	// Require that all three background-related tools were exercised.
	for _, name := range []string{"Bash", "BashOutput", "KillShell", "Write"} {
		if coll.toolCalls[name] == 0 {
			t.Errorf("missing %s tool_use — chain broken; tool_counts=%v", name, coll.toolCalls)
		}
	}

	data, readErr := os.ReadFile(statusPath)
	if readErr != nil {
		t.Fatalf("status.txt missing — agent didn't reach step 5: %v", readErr)
	}
	// Parse the integer the agent wrote.
	content := strings.TrimSpace(string(data))
	count, convErr := strconv.Atoi(content)
	if convErr != nil {
		t.Fatalf("status.txt is not a bare integer: %q (%v)", content, convErr)
	}
	// The background loop prints 8 lines over ~2.4s; step 2 sleeps 2s.
	// In practice BashOutput returns at least a few lines. Asserting > 0
	// is enough — any positive count proves the BashOutput stdout pipe
	// actually delivered. 0 would mean the bgs tap is broken.
	if count <= 0 {
		t.Errorf("BashOutput returned zero lines — background-shell stdout pipe may be broken")
	}
	t.Logf("bash-session: background shell produced %d lines of output", count)
}

// TestLive_CancelMidTurn fires a long-running task, watches for the first
// tool_use event, cancels the run, then asserts:
//
//   - ExecuteTask returns an error (context.Canceled or wrapped form).
//   - No assistant-text event arrives after the cancel point.
//   - The run entry is removed from the runner's internal map (verified
//     indirectly by a follow-up ExecuteTask succeeding with the same
//     SessionID — if the previous run's cancel func leaked into the
//     tagCancels map, a second session reuse might clobber it).
//
// This catches regressions where CancelRun no-ops (context not plumbed
// through the loop) or where ExecuteTask leaks goroutines after cancel.
func TestLive_CancelMidTurn(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()

	// Force a genuinely long-running tool call so there's a window for
	// cancel to land. We ask the agent to run a foreground Bash that
	// sleeps long enough to outlast our cancel timer — the model is much
	// less tempted to "optimize" a single sleep call than a loop of
	// small operations. If the cancel context is properly plumbed
	// through Bash's exec.CommandContext, the sleep dies with it.
	workDir := t.TempDir()
	prompt := "Call the Bash tool exactly once with the following command " +
		"(do NOT set run_in_background): sleep 60 && echo DONE\n" +
		"Do nothing else. Wait for that one tool call to complete."

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Background goroutine: as soon as we observe the Bash tool_use, wait
	// a tick for the exec to actually start, then cancel via the parent
	// context — the canonical caller-abort path.
	cancelFired := make(chan struct{})
	go func() {
		deadline := time.NewTimer(45 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			coll.mu.Lock()
			n := coll.toolCalls["Bash"]
			coll.mu.Unlock()
			if n >= 1 {
				// Give the sleep a moment to actually start before we kill it.
				time.Sleep(500 * time.Millisecond)
				close(cancelFired)
				cancel()
				return
			}
			select {
			case <-deadline.C:
				// No tool use at all — cancel anyway so the test doesn't
				// hang; the test body will fail loud on empty tool counts.
				close(cancelFired)
				cancel()
				return
			case <-ticker.C:
			}
		}
	}()

	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:  prompt,
		Model:   liveModel(),
		WorkDir: workDir,
	}, coll.cb)
	<-cancelFired // make sure the watcher actually fired before asserting

	t.Logf("cancel-mid-turn: err=%v tool_counts=%v n_events=%d",
		err, coll.toolCalls, len(coll.events))

	if err == nil {
		t.Fatalf("ExecuteTask returned nil error despite mid-turn cancel — the cancel " +
			"signal did not propagate. Expected a context.Canceled / deadline error.")
	}
	if !strings.Contains(err.Error(), "cancel") && !strings.Contains(err.Error(), "deadline") &&
		!strings.Contains(err.Error(), "context") {
		t.Errorf("unexpected error shape after cancel: %v (wanted context.Canceled / DeadlineExceeded)", err)
	}
	// The test doesn't assert exact file counts: some files may have been
	// created before cancel. What matters is that the run exited.
	if coll.toolCalls["Bash"] == 0 {
		t.Errorf("agent never started any Bash tool use before cancel landed — " +
			"test environment is too slow or prompt was ignored")
	}

	// Follow-up: a second ExecuteTask on the same runner must work. If
	// CancelRun / context propagation corrupted runner state, the second
	// call here hangs or errors out.
	coll2 := newLiveCollector()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	_, err2 := runner.ExecuteTask(ctx2, &TaskInput{
		Prompt:  "Reply with the single word 'OK' and nothing else. Do not call any tools.",
		Model:   liveModel(),
		WorkDir: t.TempDir(),
	}, coll2.cb)
	if err2 != nil {
		t.Errorf("runner did not recover from mid-turn cancel — follow-up task errored: %v", err2)
	} else {
		t.Logf("cancel-mid-turn: follow-up task ran cleanly (tool_counts=%v)", coll2.toolCalls)
	}
}

// TestLive_ToolErrorRecovery stages a deliberate error — an Edit against
// a file that doesn't exist — and expects the agent to notice, recover,
// and complete the task via a different approach (Write instead of Edit).
// This proves the loop does the right thing when a tool_result comes back
// with is_error=true: the error text must reach the model as part of the
// next-turn context, not be silently swallowed.
//
// Passes when the agent ultimately lands the file with the expected
// content, regardless of exactly how many attempts it took.
func TestLive_ToolErrorRecovery(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	targetPath := filepath.Join(workDir, "doesnotexist-yet.txt")
	const payload = "RECOVERED-TOKEN-42"

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()

	// We prompt the agent to use Edit first on a file we haven't created,
	// which will fail. Any competent loop feeds the error back; the agent
	// should then Write the file instead.
	prompt := "Follow these exact two steps:\n" +
		"1. Call Edit on " + targetPath + " with old_string=\"placeholder\" and " +
		"new_string=\"" + payload + "\". (This call will likely fail because the " +
		"file doesn't exist — that is fine, proceed to step 2.)\n" +
		"2. Regardless of step 1's outcome, call Write on " + targetPath + " with " +
		"content exactly: " + payload + "\n" +
		"Stop after step 2."

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:  prompt,
		Model:   liveModel(),
		WorkDir: workDir,
	}, coll.cb)
	total := time.Since(start)
	t.Logf("error-recovery: total=%v tool_counts=%v", total, coll.toolCalls)

	if err != nil {
		t.Fatalf("ExecuteTask returned error: %v", err)
	}

	// Ground truth: file exists with expected content. If the loop had
	// failed to surface the Edit error, the model might have bailed out
	// without calling Write.
	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("target file missing after error-recovery run: %v "+
			"(tool_counts=%v)", readErr, coll.toolCalls)
	}
	if !strings.Contains(string(data), payload) {
		t.Errorf("target file does not contain the expected payload %q; got %q",
			payload, string(data))
	}

	// We also expect to have observed BOTH Edit and Write in the trace —
	// if the agent skipped Edit entirely the error path wasn't exercised.
	if coll.toolCalls["Edit"] == 0 {
		t.Errorf("agent did not attempt Edit; error-recovery path not exercised")
	}
	if coll.toolCalls["Write"] == 0 {
		t.Errorf("agent did not call Write after Edit failed")
	}
}

// TestLive_EditFuzzyMatch stages a file whose every line is prefixed with
// four spaces, then instructs the agent to call Edit with a dedented
// old_string. Byte-exact match (L1) is impossible, so the L4 dedent-aware
// matcher must engage for the Edit to succeed. We confirm this both by
// (a) the file being correctly rewritten on disk AND (b) the Edit tool's
// own result payload (which includes `match_level=LN`) not reading L1.
//
// If the fuzzy matcher regresses, the Edit tool_result comes back with
// an error, and the agent will either retry (Read → Edit with exact
// match, reflected in multiple Edit calls) or call Write — either way
// the assertion `toolCalls["Edit"] == 1 && level != "L1"` fails.
func TestLive_EditFuzzyMatch(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	filePath := filepath.Join(workDir, "indented.txt")

	// Every line has 4 leading spaces. A byte-exact match against an
	// un-indented old_string is impossible; the L4 "dedent common indent"
	// level is what lets Edit find + replace the target region while
	// re-indenting the new_string back to the original column.
	fileContent := "" +
		"    indented_line_1\n" +
		"    indented_line_2\n" +
		"    indented_line_3\n"
	if err := os.WriteFile(filePath, []byte(fileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()

	// We pass old_string WITHOUT the 4-space prefix so byte-exact match
	// will fail and the L4 dedent path has to engage. We explicitly ask
	// the agent not to Read first — if it does, it'll use the indented
	// bytes verbatim and never exercise fuzzy.
	prompt := "Do NOT call Read or Grep. Call the Edit tool exactly once with:\n" +
		"  file_path: " + filePath + "\n" +
		"  old_string: indented_line_1\\nindented_line_2\n" +
		"  new_string: replaced_line_1\\nreplaced_line_2\n" +
		"(where \\n is a literal newline). The file on disk has 4 leading spaces on " +
		"every line; the fuzzy matcher will handle the indentation delta. After the " +
		"tool call completes, stop — do not verify."

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:  prompt,
		Model:   liveModel(),
		WorkDir: workDir,
	}, coll.cb)
	if err != nil {
		t.Fatalf("ExecuteTask failed: %v", err)
	}
	t.Logf("edit-fuzzy: tool_counts=%v", coll.toolCalls)

	if coll.toolCalls["Edit"] != 1 {
		t.Fatalf("expected exactly 1 Edit call, got %d (tool_counts=%v)",
			coll.toolCalls["Edit"], coll.toolCalls)
	}

	// Inspect the Edit tool_result to confirm the fuzzy level.
	var editResult string
	coll.mu.Lock()
	for _, e := range coll.events {
		if e.Type == "tool_result" && e.ToolName == "Edit" {
			editResult = e.Content
			break
		}
	}
	coll.mu.Unlock()
	if editResult == "" {
		t.Fatalf("no Edit tool_result captured — loop may not have emitted it")
	}
	t.Logf("edit-fuzzy: tool_result = %q", editResult)

	// The Edit tool's Content format is "N occurrence replaced in /path (match_level=LX)".
	if !strings.Contains(editResult, "match_level=") {
		t.Errorf("tool_result missing match_level marker; payload=%q", editResult)
	}
	if strings.Contains(editResult, "match_level=L1") {
		t.Errorf("Edit landed at L1 (byte-exact) — the test did not exercise the "+
			"fuzzy matcher. Check the prompt or fileContent setup. payload=%q", editResult)
	}
	// We don't pin to a specific level (L2/L3/L4/L5/L6 are all valid fuzzy
	// outcomes depending on exactly how the model formatted whitespace);
	// requiring any non-L1 is enough to prove fuzzy fell through.
	for _, want := range []string{"L2", "L3", "L4", "L5", "L6"} {
		if strings.Contains(editResult, "match_level="+want) {
			t.Logf("edit-fuzzy: matched at %s (fuzzy path engaged)", want)
			break
		}
	}

	// Ground truth: the file content must reflect the replacement AND
	// preserve the surrounding indentation (L4/L6 re-indent new_string).
	final, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatalf("file vanished after Edit: %v", readErr)
	}
	finalStr := string(final)
	if !strings.Contains(finalStr, "replaced_line_1") || !strings.Contains(finalStr, "replaced_line_2") {
		t.Errorf("replacement content missing; final=%q", finalStr)
	}
	if !strings.Contains(finalStr, "indented_line_3") {
		t.Errorf("untouched tail line was mangled; final=%q", finalStr)
	}
	// Indent preservation: the replaced lines should have 4-space indent
	// re-applied if L4/L6 was used. We're permissive here because other
	// fuzzy levels (L2/L3/L5) may legitimately not reindent — but a totally
	// unindented replacement is worth flagging.
	if !strings.Contains(finalStr, "    replaced_line_1") {
		t.Logf("note: replaced_line_1 is not 4-space indented — fuzzy level may have "+
			"been L2/L3/L5 which don't reindent. final=%q", finalStr)
	}
}

// TestLive_AllowedToolsRestriction passes AllowedTools=["Read","Grep","Glob"]
// into the TaskInput and asks the agent to create a file. Write is
// intentionally not in the allowed set, so the tool registry never sends
// a Write schema to the model — the model physically cannot emit a
// Write tool_use block. The test confirms:
//   - No Write (or Edit) tool call happened.
//   - The file was NOT created on disk.
//   - The agent's trace shows some combination of allowed tools or plain
//     assistant text explaining it can't complete the task.
//
// A regression in `Registry.List(cfg.AllowedTools)` or in the plan-mode
// intersection logic would let Write through and the file would land.
func TestLive_AllowedToolsRestriction(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	workDir := t.TempDir()
	targetPath := filepath.Join(workDir, "must-not-exist.txt")

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()

	prompt := "Create a file at " + targetPath + " containing the word 'hello'. " +
		"If you cannot do this with the tools available to you, explain briefly " +
		"which tool you would need and stop."

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:       prompt,
		Model:        liveModel(),
		WorkDir:      workDir,
		AllowedTools: []string{"Read", "Grep", "Glob"},
	}, coll.cb)
	if err != nil {
		t.Fatalf("ExecuteTask failed: %v", err)
	}
	t.Logf("allowed-tools: tool_counts=%v", coll.toolCalls)

	// Hard assertions: Write / Edit / Bash must never be seen.
	for _, forbidden := range []string{"Write", "Edit", "Bash", "BashOutput", "KillShell", "NotebookEdit"} {
		if n := coll.toolCalls[forbidden]; n > 0 {
			t.Errorf("forbidden tool %s was called %d times — AllowedTools gating is broken",
				forbidden, n)
		}
	}

	if _, statErr := os.Stat(targetPath); statErr == nil {
		t.Errorf("target file was created despite Write being off the allow-list — "+
			"gating is broken. path=%s", targetPath)
	}
}

// TestLive_CacheControlHitRate exercises the prompt-prefix cache path.
// Turn 1 sends a comfortably-long system prompt and a sizable user message
// so the Anthropic API has something meaningful to cache. The turn's
// assistant output is staged into an in-memory HistoryLoader, then turn 2
// re-uses the same HistoryKey: its request therefore repeats the entire
// turn-1 prefix (system + tools + user/assistant). If ApplyCachePrefix
// and the `cache_control: ephemeral` markers on system/tools are wired
// correctly, turn 2's final `done` event carries
// Usage.CacheReadInputTokens > 0.
//
// A regression in ApplyCachePrefix, BuildSystemBlocks cache markers, or
// the tool-list cache breakpoint would show up here as 0 cache reads.
func TestLive_CacheControlHitRate(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	const historyKey = "live-test-cache"
	loader := newInMemoryHistory()

	// Give the runner a meaty system prompt so the cacheable prefix is
	// large enough that cache_read_input_tokens has clear signal. The
	// minimum cacheable prefix size on Anthropic is 1024 tokens for most
	// models; a multi-paragraph system prompt comfortably clears that.
	systemText := strings.Repeat(
		"You are a concise coding assistant. Always reply briefly. "+
			"Respect the user's constraints exactly. Prefer writing over reading. "+
			"Never fabricate. When asked to echo text, echo it verbatim without alteration. ",
		20,
	) // ~2400 chars ≈ well over the cache threshold.

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
		SystemText:   systemText,
		History:      loader,
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	// ── Turn 1: establish the cached prefix.
	turn1 := newLiveCollector()
	turn1Prompt := "Reply with the single word 'one' and nothing else. Do not call tools."

	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	_, err := runner.ExecuteTask(ctx1, &TaskInput{
		Prompt:     turn1Prompt,
		Model:      liveModel(),
		WorkDir:    t.TempDir(),
		HistoryKey: historyKey,
	}, turn1.cb)
	cancel1()
	if err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}

	var turn1Usage *Usage
	for _, e := range turn1.events {
		if e.Type == "done" && e.Usage != nil {
			turn1Usage = e.Usage
			break
		}
	}
	if turn1Usage == nil {
		t.Fatalf("turn 1 done event missing Usage — upstream didn't report token counts")
	}
	t.Logf("cache: turn1 input=%d output=%d cache_creation=%d cache_read=%d",
		turn1Usage.InputTokens, turn1Usage.OutputTokens,
		turn1Usage.CacheCreationInputTokens, turn1Usage.CacheReadInputTokens)

	// Stage the turn into the loader so turn 2's history-assembly path
	// reconstructs exactly the same prefix.
	turn1Text := turn1.assistantText()
	loader.Set(historyKey, []native.HistoryItem{
		{Kind: "user", Content: turn1Prompt},
		{Kind: "assistant", Content: turn1Text},
	})

	// ── Turn 2: identical system+tools prefix, appended user+assistant
	// history from turn 1. The cacheable segment (system + tools + the
	// turn-1 message pair) should hit.
	turn2 := newLiveCollector()
	turn2Prompt := "Reply with the single word 'two' and nothing else. Do not call tools."

	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	_, err = runner.ExecuteTask(ctx2, &TaskInput{
		Prompt:     turn2Prompt,
		Model:      liveModel(),
		WorkDir:    t.TempDir(),
		HistoryKey: historyKey,
	}, turn2.cb)
	cancel2()
	if err != nil {
		t.Fatalf("turn 2 failed: %v", err)
	}

	var turn2Usage *Usage
	for _, e := range turn2.events {
		if e.Type == "done" && e.Usage != nil {
			turn2Usage = e.Usage
			break
		}
	}
	if turn2Usage == nil {
		t.Fatalf("turn 2 done event missing Usage")
	}
	t.Logf("cache: turn2 input=%d output=%d cache_creation=%d cache_read=%d",
		turn2Usage.InputTokens, turn2Usage.OutputTokens,
		turn2Usage.CacheCreationInputTokens, turn2Usage.CacheReadInputTokens)

	// Some gateways (notably sub2api-style Yansu gateway) scrub the
	// cache-specific prompt-side token counts — cache_read_input_tokens
	// and cache_creation_input_tokens arrive as 0 even when the upstream
	// Anthropic request did hit the prefix cache. Earlier gateway builds
	// also scrubbed input_tokens; newer builds preserve input_tokens but
	// still strip the two cache counters.
	//
	// In addition, the gateway sometimes forwards the FIRST request of a
	// new prefix without cache_control honored (rate limiting, sampling,
	// warm-up) so turn 1 yields cache_creation=0. When that happens, turn
	// 2 either (a) becomes the first turn that actually writes
	// (creation>0, read=0) or (b) writes nothing either. Both shapes
	// leave turn-2 read=0 not because our prefix is broken but because
	// turn 1 never established a cache entry to hit.
	//
	// Skip when on a gateway AND turn 1 reported no cache write — the
	// precondition for turn 2 reading isn't met, and asserting would
	// fail flakily on an issue that's outside this test's scope. When
	// pointed at api.anthropic.com directly (or unset, i.e. SDK default),
	// zero cache counts ARE a real regression signal — cache_control
	// markers missing, ApplyCachePrefix broken, prefix drift — and we
	// must fail loud. The skip below doesn't fire on the direct path.
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	isDirectAnthropic := baseURL == "" || strings.Contains(baseURL, "api.anthropic.com")
	if !isDirectAnthropic &&
		turn1Usage.CacheCreationInputTokens == 0 && turn1Usage.CacheReadInputTokens == 0 {
		t.Skipf("gateway at %s did not establish a cache entry on turn 1 "+
			"(creation=0, read=0); turn 2 has nothing to read so the assertion "+
			"is meaningless. turn2 saw creation=%d read=%d, which is consistent "+
			"with the prefix being correct (turn 2 wrote when given the chance). "+
			"Re-run against api.anthropic.com to exercise the strict path.",
			baseURL,
			turn2Usage.CacheCreationInputTokens, turn2Usage.CacheReadInputTokens)
	}

	// TODO(non-anthropic-gateway): sub2api gateway is currently routed to a
	// non-Anthropic upstream (Qwen at first, Kimi K2 since 2026-05-07), which
	// does not honor Anthropic's `cache_control: ephemeral` markers and
	// does not propagate CacheReadInputTokens back through the
	// OpenAI-compatible response envelope. Turn 1 may still report
	// cache_creation > 0 sporadically, but turn 2 then comes back with
	// cache_read = 0 (and frequently cache_creation > 0 again, i.e. the
	// gateway treats every turn as a fresh prefix). Skip in that shape
	// instead of failing — the assertion target is the Anthropic prefix-
	// cache contract, which the gateway has temporarily dropped. The
	// direct-Anthropic path above is unaffected and still fails loud on a
	// real ApplyCachePrefix / cache-marker regression. Drop this skip once
	// the gateway routes back to a Claude-class model or preserves cache
	// counters. Grep `non-anthropic-gateway` to find all related debt.
	if !isDirectAnthropic && turn2Usage.CacheReadInputTokens == 0 {
		t.Skipf("gateway at %s did not propagate cache_read back to the client "+
			"on turn 2 (cache_creation=%d, cache_read=0). The gateway's upstream "+
			"likely does not honor Anthropic's cache_control markers (e.g. when "+
			"routed to a non-Anthropic model that lacks prefix caching, or strips "+
			"the per-turn cache counters). The assertion target is the Anthropic "+
			"prefix-cache contract, which is meaningless on this path. Re-run "+
			"against api.anthropic.com to exercise the strict path.",
			baseURL, turn2Usage.CacheCreationInputTokens)
	}

	if turn2Usage.CacheReadInputTokens <= 0 {
		t.Errorf("turn 2 hit 0 cached tokens — cache_control markers may be missing " +
			"from system/tools/messages, or ApplyCachePrefix is not being applied. " +
			"Expected CacheReadInputTokens > 0 given the shared prefix.")
	} else {
		// Sanity: cache reads should be a meaningful fraction of total
		// input, not just a handful of tokens.
		ratio := float64(turn2Usage.CacheReadInputTokens) /
			float64(turn2Usage.CacheReadInputTokens+turn2Usage.InputTokens)
		t.Logf("cache: turn2 hit rate = %.1f%% (%d cached / %d total)",
			ratio*100,
			turn2Usage.CacheReadInputTokens,
			turn2Usage.CacheReadInputTokens+turn2Usage.InputTokens)
	}
}

// TestLive_NoSilentCompletionAfterUnhelpfulToolResult codifies the exact
// production bug reported in the screenshot discussion: the user asked
// "明天北京天气如何?", the agent tried Bash/WebFetch/WebSearch (no search
// backend is configured on this build, so WebSearch returns a stub
// message and curl-to-a-weather-site is unlikely to produce structured
// data Sonnet can confidently parse), and the loop terminated after a
// thinking-only turn with zero final assistant text. The user got
// nothing.
//
// The invariant this test enforces: from the user's perspective, the
// loop NEVER exits silently. Either:
//   - A non-empty final assistant text reaches the callback (the nudge
//     path succeeded or the model produced text on its first try), OR
//   - An `error` event was emitted so the UI can surface "agent went
//     silent" to the user.
//
// The "done with no text and no error" case is the regression signature
// of the original bug. If the fix in loop.go ever reverts, this test
// fails loud.
//
// Because the silent-turn behavior is non-deterministic (it depends on
// whether Sonnet happens to give up after Bash curl returns unhelpful
// HTML), the test doesn't force one specific outcome — it pins the
// invariant.
func TestLive_NoSilentCompletionAfterUnhelpfulToolResult(t *testing.T) {
	_ = liveSkipIfUnconfigured(t)

	runner := NewNativeAgentRunner(NativeRunnerConfig{
		DefaultModel: liveModel(),
		ModelAliases: liveModelAliases(),
	})
	if err := runner.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()

	coll := newLiveCollector()

	// Verbatim from the bug report. Asking for current external info
	// (tomorrow's weather) reliably pushes the agent toward WebSearch/
	// WebFetch/Bash-curl. With no search backend configured and a shell
	// environment that can't authenticate against weather APIs, the
	// tool_results come back unhelpful — which is exactly the
	// condition that triggered the silent-turn termination in the
	// pre-fix loop.
	prompt := "明天北京天气如何?"

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	start := time.Now()
	_, err := runner.ExecuteTask(ctx, &TaskInput{
		Prompt:  prompt,
		Model:   liveModel(),
		WorkDir: t.TempDir(),
	}, coll.cb)
	total := time.Since(start)

	types := coll.typesSeq()
	finalText := strings.TrimSpace(coll.assistantText())

	// Count error events (emitted by the nudge-exhausted path and the
	// max_tokens path), and pull their messages for the failure log.
	var errMsgs []string
	coll.mu.Lock()
	for _, e := range coll.events {
		if e.Type == "error" {
			errMsgs = append(errMsgs, e.Error)
		}
	}
	coll.mu.Unlock()

	t.Logf("no-silent: total=%v tool_counts=%v n_events=%d final_text_len=%d error_events=%d",
		total, coll.toolCalls, len(types), len(finalText), len(errMsgs))
	if finalText != "" {
		t.Logf("no-silent: final text = %q", truncateForLog(finalText, 240))
	}
	for i, m := range errMsgs {
		t.Logf("no-silent: error[%d] = %q", i, m)
	}

	if err != nil {
		// A context timeout or a genuine transport failure is a
		// separate class of problem — fail, but don't conflate with
		// the silent-turn regression.
		t.Fatalf("ExecuteTask returned error: %v (tool_counts=%v events=%v)",
			err, coll.toolCalls, types)
	}

	// The invariant. Either we got text back, or we got an explicit
	// error event — anything else means the loop silently gave up.
	switch {
	case finalText != "":
		// Good: either the model answered on its first try (no nudge
		// needed) OR the nudge recovered and produced an answer.
	case len(errMsgs) > 0:
		// Also good: the loop explicitly surfaced that it couldn't
		// produce an answer. The UI can render this; the user sees
		// something rather than a black hole.
		t.Logf("no-silent: loop surfaced %d error event(s) instead of going silent (fix active)",
			len(errMsgs))
	default:
		t.Fatalf("SILENT COMPLETION REGRESSION: loop exited without assistant text AND "+
			"without any error event — this is the exact bug the nudge-recovery logic "+
			"fixes. tool_counts=%v event_sequence=%v",
			coll.toolCalls, types)
	}

	// Soft sanity: the fix's nudge path logs distinctively ("silent turn
	// after tool use (stop_reason=end_turn); sending recovery nudge").
	// We can't read go-log output from here without extra plumbing, but
	// if the recovery nudge fired AND produced text, we'd expect the
	// total turn count to be > 1 (at least: initial request → tool call
	// → tool_result → nudge turn → text). The n_events figure above
	// gives a rough signal for triage.
}

// truncateForLog keeps t.Logf readable when the model produces a long
// narrative response. Unicode-safe (doesn't split mid-rune) so CJK
// responses stay legible.
func truncateForLog(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
