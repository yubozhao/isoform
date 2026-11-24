package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"yansu-agent/agent"
)

// Only Claude's model/CLI is scripted. The actual controlled runner consumes
// the composed tier, starts its actual per-run server, validates the MCP init,
// and runs real memory/People/activity handlers through the shipped relay.
func TestStreamComposedExternalChatThroughControlledToolServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("controlled assistant isolation requires Unix")
	}
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("native fallback=%v", fallback), func(t *testing.T) {
			app := newChatContextSourceApp(t)
			t.Cleanup(func() { app.flushAgentSegmentWrites(true) })
			seedHandoffBoundaryContext(t, app)
			main, err := app.ensureMainChat(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := filepath.Join(os.Getenv("HOME"), "scripted-claude")
			failure := ""
			if fallback {
				failure = "YANSU_CONTEXT_FAIL_BEFORE_TOOLS=1 "
			}
			script := "#!/bin/sh\nYANSU_GOAL_TEST_FIXTURE=1 " + failure + "exec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run=^TestChatContextExternalCLIFixture$ -- \"$@\"\n"
			if err := os.WriteFile(command, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var nativeBodies []map[string]any
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				nativeBodies = append(nativeBodies, body)
				mu.Unlock()
				writeHandoffModelSSE(w, "Native fallback completed.")
			}))
			t.Cleanup(model.Close)
			native := agent.NewNativeAgentRunner(agent.NativeRunnerConfig{BaseURL: model.URL, APIKey: "scripted-model", DefaultModel: "fixture", DisableEnvFallback: true})
			external := agent.NewExternalAgentRunner(agent.ExternalAgentConfig{Format: "claude-code", Command: command, ToolShimCommand: command, ToolServerFactory: native.NewToolServer, ToolServerSupports: native.ToolServerSupports})
			router := agent.NewRoutingAgentRunner(native, external, agent.RoutingPolicy{ExternalFormat: "claude-code", ExternalSignedIn: true, DefaultExternal: true})
			if err := router.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = router.Shutdown() })
			// Initialize only the normal send-path bookkeeping. The capture
			// runner is replaced before sending by the real router/runners.
			wireHandoffPromptCapture(t, app)
			app.runner = router
			var reply strings.Builder
			var routes []string
			router.SetObserver(func(event *agent.StreamEvent) error {
				mu.Lock()
				if event.Type == "assistant" {
					reply.WriteString(event.Content)
				}
				if event.Type == "backend_route" {
					routes = append(routes, event.Backend)
				}
				mu.Unlock()
				if event.Type == "tool_result" {
					return app.store.SetUIState(t.Context(), "context-script-result", event.ToolUseID)
				}
				return nil
			})
			if err := app.SendPromptWithAttachments("What do you remember about this project and Alice Example?", "", "fixture", "", main.LocalID, nil, nil, false, false, "", "", "", "en", ""); err != nil {
				t.Fatal(err)
			}
			waitForTagIdle(t, app, main.LocalID)
			mu.Lock()
			defer mu.Unlock()
			if len(routes) == 0 || routes[0] != "claude-code" {
				t.Fatalf("main send skipped external routing: %v", routes)
			}
			if fallback {
				if len(routes) != 2 || routes[1] != "native" || len(nativeBodies) != 1 {
					t.Fatalf("fallback routes=%v native requests=%d", routes, len(nativeBodies))
				}
				prompt := fmt.Sprint(nativeBodies[0]["messages"])
				for _, want := range []string{"<memory-context>", handoffPrivatePeople} {
					if !strings.Contains(prompt, want) {
						t.Errorf("native fallback lost %q", want)
					}
				}
				// Chat runs no commands (S25): no CLI manual on either tier.
				if strings.Contains(prompt, "<memory-cli>") {
					t.Error("native fallback still carries the memory CLI")
				}
				if strings.Contains(prompt, chatContextToolHint) {
					t.Fatal("native fallback received external tier")
				}
				for _, raw := range nativeBodies[0]["tools"].([]any) {
					name, _ := raw.(map[string]any)["name"].(string)
					if allowsTool(externalContextToolNames, name) {
						t.Fatalf("native registry acquired external tool %s", name)
					}
				}
			} else {
				if len(nativeBodies) != 0 {
					t.Fatal("external success called the native model")
				}
				for _, want := range []string{"project memory marker", handoffPrivatePeople, "project activity marker", "MemoryList"} {
					if !strings.Contains(reply.String(), want) {
						t.Errorf("same-turn answer lost %q: %s", want, reply.String())
					}
				}
			}
		})
	}
}

func TestChatContextExternalCLIFixture(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	if err := runChatContextExternalCLI(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runChatContextExternalCLI(args []string) error {
	switch args[0] {
	case "--version", "--help", "tool-shim":
		return runGoalExternalCLIFixture(args)
	}
	flags := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		flags[args[i]] = args[i+1]
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	var message struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(input, &message); err != nil {
		return err
	}
	prompt := message.Message.Content
	if !strings.Contains(prompt, chatContextToolHint) {
		return fmt.Errorf("CLI did not receive the composed context-tool hint")
	}
	for _, block := range []string{"<memory-context>", "<people-context>", "<recent-activity-context>", "<memory-cli>", "<activity-cli>", "<cua-tool>", "<yansu-context>", handoffPrivatePeople} {
		if strings.Contains(prompt, block) {
			return fmt.Errorf("CLI received standing bundle %s", block)
		}
	}
	raw, err := os.ReadFile(flags["--mcp-config"])
	if err != nil {
		return err
	}
	var config struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return err
	}
	server := config.Servers["isoform"]
	shim := exec.Command(server.Command, server.Args...)
	stdin, err := shim.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	stdout, err := shim.StdoutPipe()
	if err != nil {
		return err
	}
	shim.Stderr = os.Stderr
	if err := shim.Start(); err != nil {
		return err
	}
	defer func() { _ = shim.Process.Kill(); _ = shim.Wait() }()
	encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(stdout)
	call := func(id, method string, params any) (json.RawMessage, error) {
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		var response struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			return nil, err
		}
		if len(response.Error) > 0 {
			return nil, fmt.Errorf("MCP %s: %s", method, response.Error)
		}
		return response.Result, nil
	}
	if _, err := call("init", "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "context-script", "version": "1"}}); err != nil {
		return err
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return err
	}
	listed, err := call("list", "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	var listing struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listed, &listing); err != nil {
		return err
	}
	var names []string
	for _, tool := range listing.Tools {
		names = append(names, "mcp__isoform__"+tool.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != flags["--allowedTools"] {
		return fmt.Errorf("advertised and allowed tools differ")
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"type": "system", "subtype": "init", "tools": names, "plugins": []string{}, "permissionMode": "dontAsk", "model": "claude-sonnet-4-5", "mcp_servers": []map[string]string{{"name": "isoform", "status": "connected"}},
	}); err != nil {
		return err
	}
	if os.Getenv("YANSU_CONTEXT_FAIL_BEFORE_TOOLS") == "1" {
		return fmt.Errorf("scripted pre-action external failure")
	}
	var reply strings.Builder
	for i, step := range []struct{ name, query string }{{"MemorySearch", "project"}, {"PeopleLookup", "Alice Example"}, {"RecentActivity", ""}, {"IsoformHelp", "memory"}} {
		arguments := map[string]any{}
		if step.query != "" {
			arguments["query"] = step.query
		}
		raw, err := call(fmt.Sprintf("context-%d", i), "tools/call", map[string]any{"name": step.name, "arguments": arguments})
		if err != nil {
			return err
		}
		var result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		if result.IsError {
			return fmt.Errorf("real %s handler refused: %s", step.name, raw)
		}
		for _, block := range result.Content {
			reply.WriteString(block.Text)
			reply.WriteString("\n")
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "result": reply.String(), "is_error": false})
}
