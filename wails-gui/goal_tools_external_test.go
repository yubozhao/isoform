package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"yansu-agent/agent"
	"yansu-agent/agent/authority"
	"yansu-agent/agent/tools"
	"yansu-agent/agent/toolserver"
)

type goalExternalFixtureResult struct {
	Tools []struct {
		Name        string         `json:"name"`
		InputSchema map[string]any `json:"inputSchema"`
	} `json:"tools"`
	First json.RawMessage `json:"first"`
	Again json.RawMessage `json:"again"`
}

// Only the model/CLI is a fixture: it starts the real relay, discovers tools
// over MCP, and calls the desktop's real GoalCreate through the native gate.
func TestGoalCreateThroughExternalRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("controlled external tools require Unix process isolation")
	}
	for _, unattended := range []bool{false, true} {
		name := "attended creation and replay"
		if unattended {
			name = "unattended write denied"
		}
		t.Run(name, func(t *testing.T) {
			// A short path also stays below macOS's Unix socket path limit.
			home, err := os.MkdirTemp("/tmp", "ig-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			t.Setenv("HOME", home)
			t.Setenv("YANSU_AGENT_HOME", filepath.Join(home, "state"))
			bridge, store, now := mobileTestGoalBridge(t)
			main, err := bridge.app.ensureMainChat(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			fixture := filepath.Join(home, "claude")
			// The marker tells TestMain this child is a fixture, so it keeps
			// this test's HOME instead of making (and, through the fixture's
			// os.Exit, leaking) another temporary home.
			script := "#!/bin/sh\nYANSU_GOAL_TEST_FIXTURE=1 exec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run=^TestGoalExternalCLIFixture$ -- \"$@\"\n"
			if err := os.WriteFile(fixture, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			var evaluations []authority.Evaluation
			native := agent.NewNativeAgentRunner(agent.NativeRunnerConfig{
				DisableEnvFallback: true,
				AuthorityRecorder:  func(evaluation authority.Evaluation) { evaluations = append(evaluations, evaluation) },
			})
			t.Cleanup(func() { _ = native.Shutdown() })
			runner := agent.NewExternalAgentRunner(agent.ExternalAgentConfig{
				Format: "claude-code", Command: fixture, ToolShimCommand: fixture,
				ToolServerFactory: native.NewToolServer, ToolServerSupports: native.ToolServerSupports,
			})
			if err := runner.Init(context.Background()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runner.Shutdown() })
			input := &agent.TaskInput{
				Prompt:    "Create the goal we agreed on: Run a half marathon by June.",
				SessionID: main.LocalID, WorkDir: home,
				AllowedTools: []string{goalToolCreate}, ExtraTools: bridge.app.mainChatExtraTools(main.LocalID, promptOriginPhone)[:1],
				AutoApproveTools: unattended,
			}
			if len(input.ExtraTools) != 1 || input.ExtraTools[0].Name() != goalToolCreate {
				t.Fatalf("main chat goal tools = %v", input.ExtraTools)
			}
			input.ExtraTools[0].(*goalCreateTool).now = func() time.Time { return now }
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var events []agent.StreamEvent
			if _, err := runner.ExecuteTask(ctx, input, func(event *agent.StreamEvent) error {
				events = append(events, *event)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(home, "goal-fixture.json"))
			if err != nil {
				t.Fatal(err)
			}
			var result goalExternalFixtureResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Tools) != 1 || result.Tools[0].Name != goalToolCreate || result.Tools[0].InputSchema["type"] != "object" {
				t.Fatalf("CLI received tools = %+v", result.Tools)
			}
			if string(result.First) != string(result.Again) {
				t.Fatalf("replayed MCP call changed result: %s != %s", result.First, result.Again)
			}
			var callResult struct {
				IsError bool `json:"isError"`
			}
			if err := json.Unmarshal(result.First, &callResult); err != nil || callResult.IsError != unattended {
				t.Fatalf("MCP result = %s, %v", result.First, err)
			}
			wantGoals, wantDecision := 1, authority.DecisionAllow
			if unattended {
				wantGoals, wantDecision = 0, authority.DecisionDeny
			}
			if len(evaluations) != 1 || evaluations[0].Decision != wantDecision || evaluations[0].Action.Capability != "goals.create" {
				t.Fatalf("authority decisions = %+v", evaluations)
			}
			toolID, toolResults, actions := "", 0, 0
			for _, event := range events {
				switch event.Type {
				case "tool_use":
					if event.ToolName == goalToolCreate {
						toolID = event.ToolUseID
					}
				case "tool_result":
					if event.ToolName == goalToolCreate {
						toolResults++
						if event.ToolUseID != toolID || event.IsError != unattended {
							t.Fatalf("native tool result = %+v, call id = %q", event, toolID)
						}
					}
				case "action_started":
					actions++
				}
			}
			if toolID == "" || toolResults != 1 || actions != wantGoals || evaluations[0].ToolUseID != toolID {
				t.Fatalf("call id = %q, results = %d, actions = %d, evaluation = %+v", toolID, toolResults, actions, evaluations[0])
			}
			if !unattended {
				// The MCP call uses the same durable idempotency key as a native
				// dispatch: replaying that ID directly must not add another goal.
				replay, err := input.ExtraTools[0].Execute(context.Background(), tools.ToolCall{ID: toolID, Input: goalExternalFixtureInput()})
				if err != nil || replay.IsError {
					t.Fatalf("native replay = %+v, %v", replay, err)
				}
			}
			details, err := store.ListDetailsAll()
			if err != nil || len(details) != wantGoals {
				t.Fatalf("stored goals = %d, want %d: %v", len(details), wantGoals, err)
			}
			if !unattended && (details[0].Current.Title != "Run a half marathon by June" || !mobileGoalAddedByYou(details[0])) {
				t.Fatalf("stored goal = %+v", details[0])
			}
		})
	}
}

func goalExternalFixtureInput() map[string]any {
	return map[string]any{"text": "Run a half marathon by June", "kind": "goal", "category": "health"}
}

// TestGoalExternalCLIFixture runs only in a child of the headless runner test.
// Using this test binary also exercises the shipped relay without building or
// launching Isoform or depending on an installed assistant/model subscription.
func TestGoalExternalCLIFixture(t *testing.T) {
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
	if err := runGoalExternalCLIFixture(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runGoalExternalCLIFixture(args []string) error {
	switch args[0] {
	case "--version":
		fmt.Println("2.1.296 (Claude Code)")
		return nil
	case "--help":
		fmt.Println("--restricted --tools --strict-mcp-config --mcp-config --permission-mode --permission-prompts --allowedTools --no-session-persistence --input-format --output-format --include-partial-messages --include-hook-events")
		return nil
	case "tool-shim":
		if len(args) != 3 || args[1] != "--ticket-file" {
			return fmt.Errorf("unexpected shim arguments: %v", args)
		}
		return toolserver.Relay(context.Background(), args[2], os.Stdin, os.Stdout)
	}
	flags := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		flags[args[i]] = args[i+1]
	}
	if flags["--allowedTools"] != "mcp__isoform__GoalCreate" {
		return fmt.Errorf("CLI did not receive GoalCreate: %v", args)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		return err
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
	if _, err := call("init", "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "goal-test", "version": "1"}}); err != nil {
		return err
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return err
	}
	list, err := call("list", "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	var result goalExternalFixtureResult
	if err := json.Unmarshal(list, &result); err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"type": "system", "subtype": "init", "tools": []string{"mcp__isoform__GoalCreate"}, "plugins": []string{},
		"permissionMode": "dontAsk", "model": "claude-sonnet-4-5", "mcp_servers": []map[string]string{{"name": "isoform", "status": "connected"}},
	}); err != nil {
		return err
	}
	params := map[string]any{"name": goalToolCreate, "arguments": goalExternalFixtureInput()}
	if result.First, err = call("goal-call-1", "tools/call", params); err != nil {
		return err
	}
	if result.Again, err = call("goal-call-1", "tools/call", params); err != nil {
		return err
	}
	raw, err = json.Marshal(result)
	if err != nil {
		return err
	}
	if err := os.WriteFile("goal-fixture.json", raw, 0600); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "result": "Goal tool completed.", "is_error": false})
}
