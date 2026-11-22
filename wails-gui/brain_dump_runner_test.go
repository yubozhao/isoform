package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"yansu-agent/agent"
)

func TestBrainDumpCodeWithoutRunnerBecomesReminder(t *testing.T) {
	raw := `{"items":[{"title":"Fix tests","who":"code","project":"project"}]}`
	items, err := decodeBrainDump(raw, "", time.UTC)
	if err != nil || len(items) != 1 || items[0].Who != "reminder" || !items[0].NoRunner || items[0].Project != "" {
		t.Fatalf("items = %+v, err = %v", items, err)
	}
	items, err = decodeBrainDump(raw, "codex", time.UTC)
	if err != nil || len(items) != 1 || items[0].Who != "codex" || items[0].NoRunner {
		t.Fatalf("items = %+v, err = %v", items, err)
	}
}

func TestBrainDumpRunnerDoesNotFallBack(t *testing.T) {
	for _, tc := range []struct{ who, backend string }{
		{"claude_code", "claude-code"}, {"codex", "codex"},
	} {
		entry := &handoff.Entry{ListItem: &handoff.ListItem{Who: tc.who}}
		expected := listHandoffBackend(entry)
		if expected != tc.backend {
			t.Fatalf("%s: backend = %q", tc.who, expected)
		}
		if err := checkListHandoffRoute(expected, &agent.StreamEvent{Type: "backend_route", Backend: tc.backend}); err != nil {
			t.Fatal(err)
		}
		for _, fallback := range []string{"native", "other"} {
			if err := checkListHandoffRoute(expected, &agent.StreamEvent{Type: "backend_route", Backend: fallback}); err == nil {
				t.Fatalf("%s fell back to %s", tc.who, fallback)
			}
		}
	}
}

func TestBrainDumpProjectChoicesUseCurrentHistory(t *testing.T) {
	dir := t.TempDir()
	first := HistoryProject{ID: "first", Name: "First", Path: dir, LastAt: 1}
	second := HistoryProject{ID: "second", Name: "Second", Path: t.TempDir(), LastAt: 2}
	projects := []HistoryProject{first, second, {ID: "bad", Name: "Bad", Path: "relative", LastAt: 3}}
	choices := brainDumpProjectChoices(projects)
	if len(choices) != 2 || choices[0].Label != "Second" || choices[1].Label != "First" {
		t.Fatalf("choices = %+v", choices)
	}
	selected, ok := brainDumpChoiceProject(choices[1].ID, projects)
	if !ok || selected.Path != dir {
		t.Fatalf("selected = %+v, %v", selected, ok)
	}
	projects[0].Path = filepath.Join(dir, "changed")
	if err := os.Mkdir(projects[0].Path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, ok := brainDumpChoiceProject(choices[1].ID, projects); ok {
		t.Fatal("a changed project folder retained the old choice")
	}
}

func TestBrainDumpProjectChoiceStartsHandoff(t *testing.T) {
	root := t.TempDir()
	t.Setenv(agentHomeEnv, root)
	if err := os.WriteFile(filepath.Join(root, aiProviderConfigFileName), []byte(`{"provider":"codex"}`), 0600); err != nil {
		t.Fatal(err)
	}
	rig := newHandoffTestRig(t)
	saveHistoryChoices(t, "codex")
	project := HistoryProject{Assistants: []string{"codex"}, ID: "project", Name: "Project", Path: t.TempDir(), LastAt: time.Now().UnixMilli()}
	rig.app.assistantHistory.loaded = true
	rig.app.assistantHistory.cache = assistantHistoryCache{Projects: []HistoryProject{project}}
	item := BrainDumpItem{ID: "work", Title: "Fix tests", Who: "codex"}
	entries, err := rig.app.stageBrainDumpSelection(t.Context(), "project-choice", []BrainDumpItem{item}, handoff.SourceMobile, time.Now())
	if err != nil || len(entries) != 1 {
		t.Fatalf("stage = %+v, %v", entries, err)
	}
	if err := rig.handoffs.PublishListBatch(entries); err != nil {
		t.Fatal(err)
	}
	bridge, err := newMobileBridge(rig.app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	turns := &mobileTestTurns{}
	mobileChatRunnerOverride = mobileTestMessageOverride(turns)
	t.Cleanup(func() { mobileTestResetChatRunner(t, bridge) })
	device := mobileTestDevice(mobileScopeHandoff, mobileScopeBriefRead)
	refused := mobileTestChatCall(bridge, device, http.MethodPost, "/handoffs/"+entries[0].ID+"/start", `{"idempotencyKey":"before-project"}`)
	if refused.Code != http.StatusConflict || turns.count() != 0 {
		t.Fatalf("start without project = %d %s", refused.Code, refused.Body.String())
	}
	choice := brainDumpProjectChoiceID(project)
	body := `{"idempotencyKey":"choose-project","action":"choose","eventId":"list-project","choiceId":"` + choice + `"}`
	response := mobileTestChatCall(bridge, device, http.MethodPost, "/tasks/"+entries[0].ID+"/actions", body)
	if response.Code != http.StatusOK {
		t.Fatalf("choice = %d %s", response.Code, response.Body.String())
	}
	mobileTestWaitFor(t, "handoff turn", func() bool { return turns.count() == 1 })
	saved, err := rig.handoffs.Get(entries[0].ID)
	if err != nil || saved.Workdir != project.Path || saved.Status != handoff.StatusInProgress || saved.Thread.Events[0].ChosenID != choice {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	replay := mobileTestChatCall(bridge, device, http.MethodPost, "/tasks/"+entries[0].ID+"/actions", body)
	if replay.Code != http.StatusOK || turns.count() != 1 {
		t.Fatalf("replay = %d %s, turns = %d", replay.Code, replay.Body.String(), turns.count())
	}
}

func TestBrainDumpKnownProjectStartsOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv(agentHomeEnv, root)
	if err := os.WriteFile(filepath.Join(root, aiProviderConfigFileName), []byte(`{"provider":"codex"}`), 0600); err != nil {
		t.Fatal(err)
	}
	rig := newHandoffTestRig(t)
	saveHistoryChoices(t, "codex")
	project := HistoryProject{Assistants: []string{"codex"}, ID: "project", Name: "Project", Path: t.TempDir(), LastAt: time.Now().UnixMilli()}
	rig.app.assistantHistory.loaded = true
	rig.app.assistantHistory.cache = assistantHistoryCache{Projects: []HistoryProject{project}}
	bridge, err := newMobileBridge(rig.app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	turns := &mobileTestTurns{}
	mobileChatRunnerOverride = mobileTestMessageOverride(turns)
	t.Cleanup(func() { mobileTestResetChatRunner(t, bridge) })
	body := `{"idempotencyKey":"add-code","items":[{"id":"work","title":"Fix tests","who":"codex","project":"project"}]}`
	device := mobileTestDevice(mobileScopeHandoff, mobileScopeBriefRead)
	first := mobileTestChatCall(bridge, device, http.MethodPost, "/tasks/add", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("add = %d %s", first.Code, first.Body.String())
	}
	mobileTestWaitFor(t, "handoff turn", func() bool { return turns.count() == 1 })
	if err := os.WriteFile(filepath.Join(root, aiProviderConfigFileName), []byte(`{"provider":"managed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	replay := mobileTestChatCall(bridge, device, http.MethodPost, "/tasks/add", body)
	entries, err := rig.handoffs.List()
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() || err != nil || len(entries) != 1 || entries[0].Workdir != project.Path || entries[0].Status != handoff.StatusInProgress || turns.count() != 1 {
		t.Fatalf("replay = %d %s; entries = %+v; turns = %d; err = %v", replay.Code, replay.Body.String(), entries, turns.count(), err)
	}
}
