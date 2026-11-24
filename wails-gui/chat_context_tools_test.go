package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"wails-gui/internal/pii"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"wails-gui/storage"
	"yansu-agent/agent"
	"yansu-agent/agent/tools"
)

func TestComposeExternalChatContextTierAndNativeControl(t *testing.T) {
	rig := newHandoffTestRig(t)
	rig.app.memoryStore = setupFixtureStore(t)
	seedHandoffBoundaryContext(t, rig.app)
	query := "Alice Example Halcyon"
	rig.app.activityContextCache = newActivityContextCache()
	external := rig.app.composePrompt(query, "chat", "", skillHubDomainChat, "zh-CN", string(promptOriginMac), "external-chat")
	for _, absent := range []string{"<memory-context>", "<knowledge-context>", "<self-model-context>", "<people-context>", "<recent-activity-context>", "<memory-cli>", "<activity-cli>", "<cua-tool>", "<yansu-context>", handoffPrivatePeople} {
		if strings.Contains(external, absent) {
			t.Errorf("external standing tier contains %q", absent)
		}
	}
	for _, want := range []string{query, chatContextToolHint, chatDeliverablesDirective, responseLanguageInstruction("Simplified Chinese")} {
		if !strings.Contains(external, want) {
			t.Errorf("external tier lost %q", want)
		}
	}
	rig.app.memoryStore.mu.Lock()
	dirty := rig.app.memoryStore.dirty
	rig.app.memoryStore.mu.Unlock()
	if !dirty || len(rig.app.activityContextCache.injectedTags) != 0 {
		t.Fatal("external standing tier collected automatic context")
	}
	native := rig.app.composePrompt(query, "chat", "", skillHubDomainChat, "English")
	for _, want := range []string{"<memory-context>", "<people-context>", "<memory-cli>", "<activity-cli>", "<yansu-context>", handoffPrivatePeople} {
		if !strings.Contains(native, want) {
			t.Errorf("native tier lost %q", want)
		}
	}
	if strings.Contains(native, chatContextToolHint) {
		t.Fatal("native prompt changed to the external tier")
	}
}

// SendPrompt supplies both tiers only for supported, user-started main chat.
// Its real TaskInput then runs through the same NewToolServer used by Claude.
func TestStreamExternalChatComposedPromptAndToolServerSameTurnChatNativeReads(t *testing.T) {
	app := newMCPReadTestApp(t)
	app.memoryStore = setupFixtureStore(t)
	seedHandoffBoundaryContext(t, app)
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	capture := wireHandoffPromptCapture(t, app)
	if err := app.SendPromptWithAttachments("Where am I based, and who is Alice Example?", "", "m", "", main.LocalID, nil, nil, false, false, "", "", "", "en", ""); err != nil {
		t.Fatal(err)
	}
	var input *agent.TaskInput
	select {
	case input = <-capture.inputs:
	case <-time.After(5 * time.Second):
		t.Fatal("no main-chat task")
	}
	waitForTagIdle(t, app, main.LocalID)
	if input.ExternalPromptFunc == nil {
		t.Fatal("main chat did not wire the external tier")
	}
	external, history := input.ExternalPromptFunc()
	if !strings.Contains(external, chatContextToolHint) || strings.Contains(external, handoffPrivatePeople) || strings.Contains(external, "<memory-cli>") {
		t.Fatal("bad composed external prompt")
	}
	nativePrompt, _ := input.PromptFunc()
	if !strings.Contains(nativePrompt, chatNativeReadHint) || !strings.Contains(external, chatNativeReadHint) {
		t.Fatal("main-chat read hint missing")
	}
	nativeNames := map[string]bool{}
	for _, tool := range input.ExtraTools {
		nativeNames[tool.Name()] = true
	}
	for _, name := range chatNativeReadNames {
		if !nativeNames[name] {
			t.Fatalf("native main chat missing %s", name)
		}
	}
	for _, tool := range input.ExternalExtraTools {
		if nativeNames[tool.Name()] {
			t.Fatalf("duplicate external tool %s", tool.Name())
		}
	}
	task := *input
	task.Prompt, task.History, task.PromptFunc = external, history, nil
	task.ExtraTools = append(append(task.ExtraTools[:0:0], task.ExtraTools...), task.ExternalExtraTools...)
	c := chatContextMCPClient(t, &task, func(event *agent.StreamEvent) error {
		if event.Type == "tool_result" {
			return app.store.SetUIState(t.Context(), "context-test-event", event.ToolUseID)
		}
		return nil
	})
	listed, err := c.ListTools(t.Context(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range chatContextToolNames {
		found := false
		for _, tool := range listed.Tools {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("composed hint advertises missing tool %s", name)
		}
	}
	// A scripted executor follows the hint in this composed prompt and gets
	// full relevant chunks and relationship notes in the very same run.
	memory := chatContextMCPCall(t, c, "MemorySearch", map[string]any{"query": "where am I based"})
	for _, want := range []string{"Halcyon", "August 2 to September 12"} {
		if !strings.Contains(memory, want) {
			t.Errorf("full memory result lost %q: %s", want, memory)
		}
	}
	people := chatContextMCPCall(t, c, "PeopleLookup", map[string]any{"query": "Alice Example"})
	if !strings.Contains(people, handoffPrivatePeople) {
		t.Fatalf("People relationship notes were lost: %s", people)
	}
	help := chatContextMCPCall(t, c, "IsoformHelp", map[string]any{"query": "memory"})
	// Chat reads through its own tools and runs no commands (S25): the
	// manual names MemoryList, never a CLI, and the native tier has no CLI block.
	if !strings.Contains(help, "MemoryList") || strings.Contains(help, "yansu memory") {
		t.Fatalf("on-demand manual missing: %s", help)
	}
	native, _ := input.PromptFunc()
	if !strings.Contains(native, handoffPrivatePeople) || strings.Contains(native, "<memory-cli>") {
		t.Fatal("native fallback lost its original tier, or still names the memory CLI")
	}
}

func chatContextMCPClient(t *testing.T, input *agent.TaskInput, observe ...func(*agent.StreamEvent) error) *client.Client {
	t.Helper()
	native := agent.NewNativeAgentRunner(agent.NativeRunnerConfig{DisableEnvFallback: true})
	t.Cleanup(func() { _ = native.Shutdown() })
	server, err := native.NewToolServer(t.Context(), input, "chat-context-test", func(event *agent.StreamEvent) error {
		if len(observe) > 0 {
			return observe[0](event)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	server.SetAssistantPID(os.Getpid())
	server.Unlock()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.NewStdioMCPClient(binary, []string{"YANSU_GOAL_TEST_FIXTURE=1"}, "-test.run=^TestGoalExternalCLIFixture$", "--", "tool-shim", "--ticket-file", server.TicketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, err = c.Initialize(t.Context(), mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION, ClientInfo: mcp.Implementation{Name: "scripted-chat-context", Version: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func chatContextMCPCall(t *testing.T, c *client.Client, name string, args map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	result, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	if result.IsError {
		text.WriteString("isError: true\n")
	}
	for _, content := range result.Content {
		if block, ok := content.(mcp.TextContent); ok {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func newChatContextSourceApp(t *testing.T) *App {
	t.Helper()
	app := newMCPReadTestApp(t)
	at := seedMCPReadActivity(t, app, "context-source", "Editor", "com.example.editor", "project activity marker", "project OCR marker", "done")
	if _, err := app.activity.store.db.Exec(`UPDATE activity_events SET kind='focus' WHERE session_id='context-source'`); err != nil {
		t.Fatal(err)
	}
	writeMCPReadMemory(t, "context-memory", "context-source", "project memory marker with the complete relationship planning detail")
	for name, body := range map[string]string{"cooking.md": fixtureUnrelatedMemory, "recipes.md": fixtureRecipeMemory, "halcyon.md": fixtureHalcyonMemory, "youtube.md": fixtureYouTubeMemory} {
		if err := os.WriteFile(filepath.Join(configDirOrFallback(), "memory", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	app.memoryStore, err = NewMemoryStore(configDirOrFallback(), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.memoryStore.Close() })
	p, err := app.store.CreatePrincipal(t.Context(), storage.PrincipalRow{ID: "context-person", Kind: storage.PrincipalKindContact, DisplayName: "Project Person", CreatedFrom: "activity", Notes: "project relationship marker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.BumpPrincipalSighting(t.Context(), p.ID, "Editor", "context-source", at, ""); err != nil {
		t.Fatal(err)
	}
	app.activityContextCache = newActivityContextCache()
	privacy, allowed := app.activityContextPrivacy()
	if !allowed {
		t.Fatal("fixture privacy unavailable")
	}
	app.activityContextCache.summary = "project summary marker"
	app.activityContextCache.generatedAt = time.Now()
	app.activityContextCache.privacy = privacy

	return app
}

func newChatContextSourceFixture(t *testing.T) (*App, *client.Client) {
	app := newChatContextSourceApp(t)
	return app, chatContextMCPClient(t, &agent.TaskInput{WorkDir: os.Getenv("HOME"), ExtraTools: app.chatContextTools("chat")})
}

func TestToolServerChatContextResultsPrivacyAndRetry(t *testing.T) {
	for _, gate := range []string{"readable", "closed eyes", "sensitive app", "private source", "protected", "paused"} {
		t.Run(gate, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			app, c := newChatContextSourceFixture(t)
			switch gate {
			case "closed eyes":
				app.closedEyes.enabled.Store(true)
			case "sensitive app":
				if err := app.store.SetUIState(t.Context(), "sensitive_apps", `{"com.example.editor":"Editor"}`); err != nil {
					t.Fatal(err)
				}
				// The model readers share the live capture policy too.
				pii.SetUserSensitiveApps(map[string]string{"com.example.editor": "Editor"})
			case "private source":
				if _, err := app.activity.store.db.Exec(`UPDATE activity_events SET data=json_set(data,'$.windowTitle','Private Browsing') WHERE session_id='context-source'`); err != nil {
					t.Fatal(err)
				}
			case "protected":
				if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='context-source'`); err != nil {
					t.Fatal(err)
				}
			case "paused":
				if err := app.store.SetUIState(t.Context(), permissionUIStateKey("screen_recording"), "false"); err != nil {
					t.Fatal(err)
				}
			}
			for _, tc := range []struct{ name, query, marker string }{
				{"MemorySearch", "project", "project memory marker"},
				{"PeopleLookup", "Project Person", "project relationship marker"},
				{"RecentActivity", "", "project activity marker"},
				{"IsoformHelp", "memory", "MemoryList"},
			} {
				result := chatContextMCPCall(t, c, tc.name, map[string]any{"query": tc.query})
				if gate == "readable" {
					if !strings.Contains(result, tc.marker) {
						t.Errorf("%s lost result: %s", tc.name, result)
					}
				} else {
					for _, marker := range []string{"project memory marker", "project relationship marker", "project summary marker", "project activity marker", "project OCR marker"} {
						if strings.Contains(result, marker) {
							t.Errorf("%s leaked through %s: %s", tc.name, gate, result)
						}
					}
				}
			}
			if gate == "closed eyes" {
				app.closedEyes.enabled.Store(false)
				result := chatContextMCPCall(t, c, "RecentActivity", map[string]any{})
				if !strings.Contains(result, "project activity marker") {
					t.Fatalf("refused read consumed recoverable activity context: %s", result)
				}
				if len(app.activityContextCache.injectedTags) != 0 {
					t.Fatal("tool consumed native one-shot tag")
				}
			}
		})
	}
}

func TestContextExternalTierLeavesUnsupportedCasesUnchanged(t *testing.T) {
	for _, mutate := range []func(*agent.TaskInput){
		func(in *agent.TaskInput) { in.NoTools = true },
		func(in *agent.TaskInput) { in.PlanMode = true },
		func(in *agent.TaskInput) { in.AllowedTools = []string{"Bash"} },
		func(in *agent.TaskInput) { in.DisabledTools = []string{"PeopleLookup"} },
	} {
		input := &agent.TaskInput{}
		mutate(input)
		if mainChatContextToolsAllowed(input) {
			t.Fatal("restricted task was narrowed without replacement tools")
		}
	}
}

// This decorator only orders a real store mutation after the real reader has
// completed, before the real tool-server publication fence. No result is faked.
type mutatingChatContextRead struct {
	*chatContextTool
	mutate func()
}

func (t *mutatingChatContextRead) LockResultPublication(ctx context.Context, call tools.ToolCall) (func(), error) {
	if t.mutate != nil {
		mutate := t.mutate
		t.mutate = nil
		mutate()
	}
	return t.chatContextTool.LockResultPublication(ctx, call)
}

func TestToolServerChatContextWithdrawalAtPublication(t *testing.T) {
	for _, scenario := range []string{"delete memory", "forget memory", "edit person", "withdraw activity", "closed eyes", "sensitive policy"} {
		t.Run(scenario, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			app := newChatContextSourceApp(t)
			name, query, old := "MemorySearch", "project", "project memory marker"
			mutate := func() {}
			switch scenario {
			case "delete memory":
				mutate = func() {
					if err := app.DeleteMemoryEntry("context-memory"); err != nil {
						t.Fatal(err)
					}
				}
			case "forget memory":
				preview, err := app.GetMemoryForgetPreview("context-memory")
				if err != nil {
					t.Fatal(err)
				}
				mutate = func() {
					if err := app.ForgetMemoryEntry("context-memory", preview.ConfirmationToken); err != nil {
						t.Fatal(err)
					}
				}
			case "edit person":
				name, query, old = "PeopleLookup", "Project Person", "project relationship marker"
				mutate = func() {
					p, err := app.store.GetPrincipal(t.Context(), "context-person")
					if err != nil {
						t.Fatal(err)
					}
					p.Notes = "updated relationship marker"
					if _, err := app.store.UpdatePrincipal(t.Context(), p); err != nil {
						t.Fatal(err)
					}
				}
			case "withdraw activity":
				name, query, old = "RecentActivity", "", "project activity marker"
				mutate = func() {
					if err := app.activity.store.DeleteSession("context-source"); err != nil {
						t.Fatal(err)
					}
				}
			case "closed eyes":
				mutate = func() { app.closedEyes.enabled.Store(true) }
			case "sensitive policy":
				mutate = func() { pii.SetUserSensitiveApps(map[string]string{"com.example.editor": "Editor"}) }
			}
			tool := &mutatingChatContextRead{chatContextTool: &chatContextTool{app: app, name: name}, mutate: mutate}
			var events []agent.StreamEvent
			c := chatContextMCPClient(t, &agent.TaskInput{WorkDir: os.Getenv("HOME"), ExtraTools: []tools.Tool{tool}}, func(event *agent.StreamEvent) error {
				if event.Type == "tool_result" {
					events = append(events, *event)
				}
				return nil
			})
			result := chatContextMCPCall(t, c, name, map[string]any{"query": query})
			if strings.Contains(result, old) || !strings.Contains(result, "isError: true") {
				t.Fatalf("stale context published: %s", result)
			}
			if len(events) != 1 || strings.Contains(events[0].Content, old) || !events[0].IsError {
				t.Fatalf("stale buffered event published: %+v", events)
			}
			// A fresh ID can recover after a refused read without consuming a
			// one-shot activity tag or reusing a cached source selection.
			if scenario == "closed eyes" {
				app.closedEyes.enabled.Store(false)
			}
			if scenario == "sensitive policy" {
				pii.SetUserSensitiveApps(nil)
			}
			retry := chatContextMCPCall(t, c, name, map[string]any{"query": query})
			switch scenario {
			case "delete memory", "forget memory", "withdraw activity":
				if strings.Contains(retry, old) {
					t.Fatalf("withdrawn source resurrected: %s", retry)
				}
			case "edit person":
				if strings.Contains(retry, old) || !strings.Contains(retry, "updated relationship marker") {
					t.Fatalf("retry lost current People notes: %s", retry)
				}
			default:
				if !strings.Contains(retry, old) {
					t.Fatalf("retry lost restored source: %s", retry)
				}
			}
		})
	}
}

func TestStreamExternalChatContextWiringKeepsUnsupportedCases(t *testing.T) {
	for _, scenario := range []string{"computer", "phone", "plan", "restricted tools", "custom composer", "resume", "focused screen"} {
		t.Run(scenario, func(t *testing.T) {
			app := newMCPReadTestApp(t)
			main, err := app.ensureMainChat(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			capture := wireHandoffPromptCapture(t, app)
			origin, plan := promptOriginMac, false
			var allowed []string
			var composer func(string, string) string
			wantExternal := false
			switch scenario {
			case "computer":
				wantExternal = true
			case "phone":
				origin = promptOriginPhone
				wantExternal = true
			case "plan":
				plan = true
			case "restricted tools":
				allowed = []string{"Bash"}
			case "custom composer":
				composer = func(prompt, language string) string { return "item-specific context: " + prompt }
			case "resume":
				origin = promptOriginResume
			case "focused screen":
				stashFocusedAppContext(main.LocalID, &focusedAppSnapshot{AppName: "Editor", CapturedAt: time.Now()})
				t.Cleanup(func() { focusedAppStash.Delete(main.LocalID) })
			}
			if err := app.executePrompt("Help with my work", "", "m", "", main.LocalID, nil, plan, false, "en", allowed, composer, "", "", origin); err != nil {
				t.Fatal(err)
			}
			var input *agent.TaskInput
			select {
			case input = <-capture.inputs:
			case <-time.After(5 * time.Second):
				t.Fatal("no task captured")
			}
			waitForTagIdle(t, app, main.LocalID)
			if (input.ExternalPromptFunc != nil) != wantExternal {
				t.Fatalf("external composer present=%v, want %v", input.ExternalPromptFunc != nil, wantExternal)
			}
			if wantExternal && len(input.ExternalExtraTools) != len(externalContextToolNames) {
				t.Fatal("external tier lost its readers")
			}
			if !wantExternal && len(input.ExternalExtraTools) != 0 {
				t.Fatal("unchanged case received external context tools")
			}
			if scenario == "focused screen" && !hasFocusedAppContext(main.LocalID) {
				t.Fatal("tier selection consumed focused-screen context")
			}
		})
	}
}

func TestStreamContextToolResultsNeverReplayIntoNativeHistory(t *testing.T) {
	store := newHistoryTestStore(t, "context-history")
	rows := []storage.StreamItemRow{{ID: "user", Kind: "user", Content: "Help with this project"}, {ID: "answer", Kind: "assistant", Content: "I can help."}}
	for i, name := range chatContextToolNames {
		id := fmt.Sprintf("context-use-%d", i)
		rows = append(rows,
			storage.StreamItemRow{ID: id, Kind: "tool_use", ToolName: name, ToolUseID: id, ToolInput: `{"query":"private lookup"}`},
			storage.StreamItemRow{ID: id + "-result", Kind: "tool_result", ToolUseID: id, Content: "withdrawn context marker"})
	}
	rows = append(rows,
		storage.StreamItemRow{ID: "ordinary-use", Kind: "tool_use", ToolName: "Read", ToolUseID: "ordinary", ToolInput: `{"path":"/task/file"}`},
		storage.StreamItemRow{ID: "ordinary-result", Kind: "tool_result", ToolUseID: "ordinary", Content: "ordinary task content"})
	appendItems(t, store, "context-history", rows)
	history, err := (&streamItemHistoryLoader{store: store}).LoadHistory("context-history")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 4 {
		t.Fatalf("native history changed unrelated rows: %+v", history)
	}
	for _, item := range history {
		if strings.Contains(item.Content, "withdrawn context marker") || allowsTool(chatContextToolNames, item.ToolName) {
			t.Fatalf("external context replayed without a fresh source read: %+v", item)
		}
	}
	if history[2].ToolName != "Read" || history[3].Content != "ordinary task content" {
		t.Fatal("ordinary native tool history changed")
	}
}

func TestToolServerMemoryContextRechecksWarmIndexSourceBytes(t *testing.T) {
	for _, kind := range []string{"knowledge", "observed memory"} {
		for _, busy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/busy=%v", kind, busy), func(t *testing.T) {
				app, c := newChatContextSourceFixture(t)
				var path string
				write := func(marker string) {
					if kind == "knowledge" {
						if err := os.MkdirAll(app.memoryStore.knowledgeDir, 0700); err != nil {
							t.Fatal(err)
						}
						path = filepath.Join(app.memoryStore.knowledgeDir, "freshness.md")
						if err := os.WriteFile(path, []byte("# Freshness\n\nfreshness "+marker+"\n"), 0600); err != nil {
							t.Fatal(err)
						}
					} else {
						writeMCPReadMemory(t, "freshness", "context-source", "freshness "+marker)
					}
				}
				write("removed old source detail")
				if err := app.memoryStore.Sync("test-warm-index"); err != nil {
					t.Fatal(err)
				}
				if result := chatContextMCPCall(t, c, "MemorySearch", map[string]any{"query": "freshness"}); !strings.Contains(result, "removed old source detail") {
					t.Fatalf("invalid warm control: %s", result)
				}
				// The watcher has not marked the index dirty: both read stamps
				// will see the new file, while the index still has old text.
				write("current replacement detail")
				if busy {
					app.memoryStore.syncMu.Lock()
				}
				result := chatContextMCPCall(t, c, "MemorySearch", map[string]any{"query": "freshness"})
				if busy {
					app.memoryStore.syncMu.Unlock()
				}
				if strings.Contains(result, "removed old source detail") {
					t.Fatalf("stale indexed text escaped: %s", result)
				}
				if busy {
					if !strings.Contains(result, "isError: true") {
						t.Fatalf("busy stale index did not refuse: %s", result)
					}
					result = chatContextMCPCall(t, c, "MemorySearch", map[string]any{"query": "freshness"})
				}
				if !strings.Contains(result, "current replacement detail") {
					t.Fatalf("same-run refresh lost current source: %s", result)
				}
			})
		}
	}
}

func TestToolServerContextPublicationOwnsActivityWithdrawalFence(t *testing.T) {
	app := newChatContextSourceApp(t)
	tool := &chatContextTool{app: app, name: "RecentActivity"}
	call := tools.ToolCall{ID: "read-before-withdrawal", Input: map[string]any{}}
	result, err := tool.Execute(t.Context(), call)
	if err != nil || result.IsError || !strings.Contains(result.Content, "project activity marker") {
		t.Fatalf("readable control: %+v %v", result, err)
	}
	release, err := tool.LockResultPublication(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if app.activity.store.readPrivacyMu.TryLock() {
		app.activity.store.readPrivacyMu.Unlock()
		t.Fatal("publication left the withdrawal mutex unlocked after the final stamp")
	}
	entered, done := make(chan struct{}), make(chan error, 1)
	before := app.activity.store.readPrivacyRevision.Load()
	go func() { close(entered); done <- app.activity.store.DeleteSession("context-source") }()
	<-entered
	// DeleteSession cannot increment to an odd in-flight revision after the
	// final stamp while the transport constructs its response under this fence.
	if revision := app.activity.store.readPrivacyRevision.Load(); revision != before || revision%2 != 0 {
		t.Fatal("withdrawal crossed owned publication fence")
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("withdrawal did not recover after publication")
	}
	result, err = tool.Execute(t.Context(), tools.ToolCall{ID: "read-after-withdrawal", Input: map[string]any{}})
	if err != nil || strings.Contains(result.Content, "project activity marker") {
		t.Fatalf("withdrawn activity survived a new read: %+v %v", result, err)
	}
}

func TestToolServerContextHelpSurvivesPausedObservation(t *testing.T) {
	for _, gate := range []string{"closed eyes", "screen recording", "accessibility"} {
		t.Run(gate, func(t *testing.T) {
			app, c := newChatContextSourceFixture(t)
			switch gate {
			case "closed eyes":
				app.closedEyes.enabled.Store(true)
			case "screen recording":
				if err := app.store.SetUIState(t.Context(), permissionUIStateKey("screen_recording"), "false"); err != nil {
					t.Fatal(err)
				}
			case "accessibility":
				if err := app.store.SetUIState(t.Context(), permissionUIStateKey("accessibility"), "false"); err != nil {
					t.Fatal(err)
				}
			}
			for _, topic := range []string{"memory", "activity", "isoform"} {
				result := chatContextMCPCall(t, c, "IsoformHelp", map[string]any{"query": topic})
				if strings.Contains(result, "isError: true") || !strings.Contains(result, "Isoform documentation:") {
					t.Fatalf("static %s help was blocked: %s", topic, result)
				}
				for _, private := range []string{"project memory marker", "project relationship marker", "project activity marker", "CONNECTED INTEGRATIONS ("} {
					if strings.Contains(result, private) {
						t.Fatalf("help included personal source data: %s", result)
					}
				}
			}
			for _, name := range []string{"MemorySearch", "PeopleLookup", "RecentActivity"} {
				result := chatContextMCPCall(t, c, name, map[string]any{"query": "project"})
				if !strings.Contains(result, "isError: true") {
					t.Fatalf("personal %s read bypassed paused observation: %s", name, result)
				}
			}
		})
	}
}

func TestToolServerMemoryContextKeepsMultipleChunksOnOneSourceLine(t *testing.T) {
	app, c := newChatContextSourceFixture(t)
	if err := os.MkdirAll(app.memoryStore.knowledgeDir, 0700); err != nil {
		t.Fatal(err)
	}
	body := "firstneedle useful fact " + strings.Repeat("readable filler ", memoryChunkTokens*3)
	if err := os.WriteFile(filepath.Join(app.memoryStore.knowledgeDir, "long-line.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	result := chatContextMCPCall(t, c, "MemorySearch", map[string]any{"query": "firstneedle"})
	if !strings.Contains(result, "firstneedle useful fact") {
		t.Fatalf("valid chunk sharing a line range was lost: %s", result)
	}
}

type cancelContextRepairEmbedder struct {
	activityEmbeddingTestProvider
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *cancelContextRepairEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	e.once.Do(func() { close(e.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
		return nil, fmt.Errorf("test repair released")
	}
}

func TestToolServerMemoryContextRepairCancellationReleasesSync(t *testing.T) {
	app := newChatContextSourceApp(t)
	if err := os.MkdirAll(app.memoryStore.knowledgeDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(app.memoryStore.knowledgeDir, "cancel-repair.md")
	if err := os.WriteFile(path, []byte("# Freshness\n\nfreshness removed old source detail\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.memoryStore.Sync("warm-cancel-repair"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Freshness\n\nfreshness current replacement detail\n"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &cancelContextRepairEmbedder{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(provider.release)
	app.memoryStore.mu.Lock()
	app.memoryStore.embedder = provider
	app.memoryStore.mu.Unlock()
	tool := &chatContextTool{app: app, name: "MemorySearch"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan tools.ToolResult, 1)
	go func() {
		result, _ := tool.Execute(ctx, tools.ToolCall{ID: "cancel-source-repair", Input: map[string]any{"query": "freshness"}})
		done <- result
	}()
	select {
	case <-provider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("repair did not reach its embedding batch")
	}
	cancel()
	select {
	case result := <-done:
		if !result.IsError || strings.Contains(result.Content, "removed old source detail") {
			t.Fatalf("canceled repair published source text: %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("repair ignored tool cancellation")
	}
	if !app.memoryStore.syncMu.TryLock() {
		t.Fatal("canceled repair kept global sync ownership")
	}
	app.memoryStore.syncMu.Unlock()
	app.memoryStore.mu.Lock()
	app.memoryStore.embedder = nil
	app.memoryStore.mu.Unlock()
	result, err := tool.Execute(t.Context(), tools.ToolCall{ID: "fresh-after-cancel", Input: map[string]any{"query": "freshness"}})
	if err != nil || result.IsError || !strings.Contains(result.Content, "current replacement detail") {
		t.Fatalf("repair did not recover: %+v %v", result, err)
	}
}

func TestToolServerMemoryContextRefusesUntrackedSourceLinks(t *testing.T) {
	app := newChatContextSourceApp(t)
	if err := os.MkdirAll(app.memoryStore.knowledgeDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(app.memoryStore.knowledgeDir, "linked.md")
	body := "# Link source\n\nlinkneedle private source marker\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.memoryStore.Sync("warm-linked-source"); err != nil {
		t.Fatal(err)
	}
	control, err := (&chatContextTool{app: app, name: "MemorySearch"}).Execute(t.Context(), tools.ToolCall{ID: "regular-source-control", Input: map[string]any{"query": "linkneedle"}})
	if err != nil || control.IsError || !strings.Contains(control.Content, "private source marker") {
		t.Fatalf("invalid regular-source control: %+v %v", control, err)
	}
	target := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(target, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	tool := &mutatingChatContextRead{chatContextTool: &chatContextTool{app: app, name: "MemorySearch"}, mutate: func() {
		text := renderOKFDocument(map[string]any{"type": "Isoform Memory", "apps": []string{"1Password"}}, "linkneedle private source marker")
		if err := os.WriteFile(target, []byte(text), 0600); err != nil {
			t.Error(err)
		}
	}}
	c := chatContextMCPClient(t, &agent.TaskInput{WorkDir: os.Getenv("HOME"), ExtraTools: []tools.Tool{tool}})
	result := chatContextMCPCall(t, c, "MemorySearch", map[string]any{"query": "linkneedle"})
	if strings.Contains(result, "private source marker") {
		t.Fatalf("untracked linked source was published: %s", result)
	}
}

func TestMemoryContextRepairCacheDatabaseWorkHonorsCancellation(t *testing.T) {
	app := newChatContextSourceApp(t)
	store := app.memoryStore
	store.mu.Lock()
	store.embedder = &activityEmbeddingTestProvider{}
	store.mu.Unlock()
	store.db.SetMaxOpenConns(1)
	conn, err := store.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := store.loadEmbeddingCache(store.db, []string{"not-cached"}, ctx)
		if err == nil {
			done <- fmt.Errorf("canceled cache read succeeded")
			return
		}
		if err := store.storeEmbeddingCache(store.db, []embeddingCacheEntry{{Hash: "not-cached", Embedding: []float64{1, 0}}}, ctx); err == nil {
			done <- fmt.Errorf("canceled cache write succeeded")
			return
		}
		store.pruneEmbeddingCache(store.db, ctx)
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cache DB operation ignored cancellation while its connection was held")
	}
}
