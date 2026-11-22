package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/workonit"
)

func newAgentHereTestApp(t *testing.T, signedIn bool) (*App, *[]string) {
	t.Helper()
	app, _ := newWorkOnItTestApp(t, true)
	app.workOnIt().paste = &pasteDeps{lookPath: pasteLookPath("claude", "codex"), frontmost: func() string { return "" }, now: time.Now}
	store, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.handoffStore = store
	var started []string
	prevStart, prevSigned := agentHereTaskStart, agentHereSignedIn
	agentHereTaskStart = func(_ *App, id string) error { started = append(started, id); return nil }
	agentHereSignedIn = func(string) (bool, error) { return signedIn, nil }
	t.Cleanup(func() { agentHereTaskStart, agentHereSignedIn = prevStart, prevSigned })
	return app, &started
}

// TestConnectAgentHereNeedsItInstalledAndSignedIn: Connect takes only a
// coding agent here that is installed and signed in, and shows it connected.
func TestConnectAgentHereNeedsItInstalledAndSignedIn(t *testing.T) {
	app, _ := newAgentHereTestApp(t, false)
	if _, err := app.ConnectAgentHere("claude"); !errors.Is(err, errAgentHereNotCoding) {
		t.Fatalf("Claude (not a coding agent here): %v", err)
	}
	if _, err := app.ConnectAgentHere("codex"); !errors.Is(err, errAgentHereSignedOut) {
		t.Fatalf("signed out: %v", err)
	}
	agentHereSignedIn = func(string) (bool, error) { return true, nil }
	app.workOnIt().paste = &pasteDeps{lookPath: pasteLookPath(), frontmost: func() string { return "" }, now: time.Now}
	if _, err := app.ConnectAgentHere("codex"); !errors.Is(err, errAgentHereNotFound) {
		t.Fatalf("not installed: %v", err)
	}
	app.workOnIt().paste = &pasteDeps{lookPath: pasteLookPath("codex"), frontmost: func() string { return "" }, now: time.Now}
	agents, err := app.ConnectAgentHere("codex")
	if err != nil {
		t.Fatal(err)
	}
	if v := agentView(t, agents, "codex"); !v.Connected || !v.ConnectedHere || v.Reach != agentReachConnectedComputer {
		t.Fatalf("codex = %+v", v)
	}
	if agents, _ = app.DisconnectAgentHere("codex"); agentView(t, agents, "codex").Connected {
		t.Fatal("still connected after Disconnect")
	}
}

// TestHandOffToAnAgentHereStartsAPinnedTask: a Yes for Codex connected here
// starts a task that must run on Codex, whatever the chat provider is; the
// hand-off keeps its task, and the Agents page reads its state.
func TestHandOffToAnAgentHereStartsAPinnedTask(t *testing.T) {
	app, started := newAgentHereTestApp(t, true)
	if _, err := app.ConnectAgentHere("codex"); err != nil {
		t.Fatal(err)
	}
	in := briefInput()
	in.Kind = workonit.KindCode
	prepared, err := app.PrepareWorkOnIt(in)
	if err != nil {
		t.Fatal(err)
	}
	var codex WorkOnItAgentView
	for _, v := range prepared.Agents {
		if v.ID == "codex" {
			codex = v
		}
	}
	if len(codex.Methods) == 0 || codex.Methods[0] != workonit.MethodConnected {
		t.Fatalf("Codex in the sheet = %+v", codex)
	}
	sent, err := app.SendWorkOnIt(prepared.TaskID, "codex", workonit.MethodConnected)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Via != "task" || len(*started) != 1 {
		t.Fatalf("sent = %+v, started %v", sent, *started)
	}
	entry, err := app.handoffStore.Get((*started)[0])
	if err != nil {
		t.Fatal(err)
	}
	if entry.Backend != "codex" || taskForcedBackend(&entry) != "codex" || !strings.Contains(entry.Body, "Draft the launch note") {
		t.Fatalf("task = backend %q forced %q body %q", entry.Backend, taskForcedBackend(&entry), entry.Body)
	}
	handoffs, _ := app.GetWorkOnItHandoffs()
	if len(handoffs) != 1 || handoffs[0].TaskID != entry.ID || handoffs[0].TaskState == "" {
		t.Fatalf("hand-offs = %+v", handoffs)
	}

	// A task that can't start is removed, and nothing is recorded.
	agentHereTaskStart = func(*App, string) error { return errors.New("no runner") }
	prepared, _ = app.PrepareWorkOnIt(in)
	if _, err := app.SendWorkOnIt(prepared.TaskID, "codex", workonit.MethodConnected); !errors.Is(err, errAgentHereNotStarted) {
		t.Fatalf("failed start: %v", err)
	}
	if handoffs, _ = app.GetWorkOnItHandoffs(); len(handoffs) != 1 {
		t.Fatalf("a failed start was kept: %d", len(handoffs))
	}

	// Closed Eyes stops it.
	app.closedEyes.enabled.Store(true)
	if app.connectedHandoffReady("codex") {
		t.Fatal("ready under Closed Eyes")
	}
}

func TestTaskForcedBackendNeverGuesses(t *testing.T) {
	for backend, want := range map[string]string{"claude-code": "claude-code", "codex": "codex", "native": "", "": ""} {
		entry := handoff.Entry{Backend: backend}
		if got := taskForcedBackend(&entry); got != want {
			t.Errorf("%q = %q, want %q", backend, got, want)
		}
	}
	if taskForcedBackend(nil) != "" {
		t.Fatal("nil entry")
	}
}

// TestUnreadSettingNeverRoutesAsNotConnected: when Your agents' settings
// can't be read in time, a Yes for an agent connected here fails with the
// read error; it never starts, never goes another way, and Settings gets the
// error, not "not connected" (hub review of #1385).
func TestUnreadSettingNeverRoutesAsNotConnected(t *testing.T) {
	app, started := newAgentHereTestApp(t, true)
	if _, err := app.ConnectAgentHere("codex"); err != nil {
		t.Fatal(err)
	}
	in := briefInput()
	in.Kind = workonit.KindCode
	prepared, err := app.PrepareWorkOnIt(in)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := app.GetWorkOnItHandoffs()
	app.yourAgentsReadHook = func(string) (string, error) { return "", errYourAgentsUnread }
	if _, err := app.SendWorkOnIt(prepared.TaskID, "codex", workonit.MethodConnected); !errors.Is(err, errYourAgentsUnread) {
		t.Fatalf("send with the setting unread: %v", err)
	}
	if len(*started) != 0 {
		t.Fatalf("a task started: %v", *started)
	}
	if after, _ := app.GetWorkOnItHandoffs(); len(after) != len(before) {
		t.Fatalf("a hand-off was recorded: %d then %d", len(before), len(after))
	}
	if _, err := app.GetYourAgents(); !errors.Is(err, errYourAgentsUnread) {
		t.Fatalf("GetYourAgents with the setting unread: %v", err)
	}
	if _, err := app.PrepareWorkOnIt(in); !errors.Is(err, errYourAgentsUnread) {
		t.Fatalf("prepare with the setting unread: %v", err)
	}
	app.yourAgentsReadHook = nil
	if _, err := app.GetYourAgents(); err != nil {
		t.Fatalf("read back: %v", err)
	}
}
