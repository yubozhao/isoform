package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"

	"yansu-agent/agent"
)

// Chat reads your day and runs no commands (computer-chat v1.2.0 rule 6;
// computer-conformance-answers v1.4.0 S25 and S19).

func TestChatTurnGetsNoCommandTools(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 1)}
	rig.app.runner = runner
	rig.app.tagCancels = map[string]context.CancelFunc{}
	rig.app.sessionTempDirs = map[string]string{}
	rig.app.streams = map[string]context.CancelFunc{}
	rig.app.runIDByTag = map[string]string{}
	rig.app.pendingTraceIDs = map[string]string{}
	if err := rig.app.executePrompt("what did I do this morning?", "", "m", "", main.LocalID, nil, false, false, "", nil, nil, "", "", promptOriginMac); err != nil {
		t.Fatal(err)
	}
	var input *agent.TaskInput
	select {
	case input = <-runner.inputs:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner was not called")
	}
	for _, name := range []string{"Bash", "BashOutput", "KillShell", "Write", "Edit", "NotebookEdit", "WebFetch", "WebSearch", "cua_driver"} {
		if !allowsTool(input.DisabledTools, name) {
			t.Fatalf("a Chat turn can still use %s", name)
		}
	}
	if input.AskBeforeRiskyCommands {
		t.Fatal("a Chat turn still asks before risky commands")
	}
	prompt, _ := input.PromptFunc()
	for _, banned := range []string{"<activity-cli>", "<memory-cli>", "cron add", "skill install", "RUNTIME ENVIRONMENT", "opencli", "run via Bash", "mcp install"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("Chat's prompt still names %q", banned)
		}
	}
	if !strings.Contains(prompt, "Chat runs no commands") {
		t.Fatal("Chat's prompt lacks its product context")
	}
	waitForTagIdle(t, rig.app, main.LocalID)
}

func TestChatExternalRulesNameNoCommands(t *testing.T) {
	rules := (&App{}).externalChatStandingRules("English")
	for _, banned := range []string{"yansu CLI", "RUNTIME ENVIRONMENT", "opencli", "Bash", "WebFetch"} {
		if strings.Contains(rules, banned) {
			t.Fatalf("external Chat rules still name %q", banned)
		}
	}
	for _, topic := range []string{"memory", "activity"} {
		got := (&chatContextTool{app: &App{}, name: "IsoformHelp"}).readHelp(topic)
		if got.IsError || strings.Contains(got.Content, "yansu ") || strings.Contains(got.Content, "Bash") {
			t.Fatalf("IsoformHelp(%q) still documents a CLI: %q", topic, got.Content)
		}
	}
}

func TestNoRiskyCommandsSettingRemains(t *testing.T) {
	for _, path := range []string{"risky_commands_config.go", "auth.go", "frontend/src/lib/wails-shim-app.ts", "quick-chat-spotlight/frontend/src/lib/wails-app-stub.ts", "frontend/src/i18n/locales/en/settings.json", "frontend/src/i18n/locales/zh-CN/settings.json", "frontend/src/components/settings/pages/about-settings.tsx"} {
		raw, err := os.ReadFile(filepath.FromSlash(path))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`(?i)risky_?commands|AskBeforeRisky`).Match(raw) {
			t.Fatalf("%s still carries the risky-commands setting", path)
		}
	}
}

func TestChatNativeReadsCapTheirLimit(t *testing.T) {
	a := newMCPReadTestApp(t)
	store := a.activity.store
	at := time.Date(2026, 5, 12, 9, 0, 0, 0, time.Local)
	for i := 0; i < chatNativeReadMaxLimit+5; i++ {
		id := fmt.Sprintf("session-%03d", i)
		ms := at.Add(time.Duration(i) * time.Minute).UnixMilli()
		row := ActivitySessionRow{ID: id, StartTime: ms, EndTime: ms + 1000, CreatedAt: ms, AppNames: `["Editor"]`, TriageStatus: "pending"}
		if err := store.SaveSession(row, []activitymon.ActivityEvent{{Timestamp: ms, AppName: "Editor", AppBundleID: "com.example.editor"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	list := nativeRead(t, a, "ActivityList", map[string]any{"limit": 500})
	if list.IsError || strings.Count(list.Content, "ref=activity:") != chatNativeReadMaxLimit {
		t.Fatalf("ActivityList read %d rows, want at most %d", strings.Count(list.Content, "ref=activity:"), chatNativeReadMaxLimit)
	}
}
