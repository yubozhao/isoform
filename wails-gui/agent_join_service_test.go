package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"wails-gui/internal/agentjoin"
	"wails-gui/internal/workonit"
)

func newAgentJoinTestApp(t *testing.T) (*App, *atomic.Bool) {
	t.Helper()
	t.Setenv(taskLinkAddrEnv, "127.0.0.1:0")
	t.Setenv(taskLinkBaseURLEnv, "")
	app, _ := newWorkOnItTestApp(t, true)
	on := &atomic.Bool{}
	on.Store(true)
	app.taskLinks.dir = t.TempDir()
	app.taskLinks.enabled = on.Load
	app.agentJoin.dir = t.TempDir()
	t.Cleanup(app.shutdownTaskLinks)
	return app, on
}

func agentJoinCall(t *testing.T, method, url, body string) (int, map[string]string, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]string{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, string(raw)
}

// TestAgentJoinsThroughTheListener: Connect an agent makes the message; an
// agent reads the link on the computer's own listener, answers, and joins
// only after the person's Allow; it shows in Your agents as connected,
// survives a restart, and Disconnect ends it.
func TestAgentJoinsThroughTheListener(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	start, err := app.StartAgentJoin()
	if err != nil {
		t.Fatal(err)
	}
	if !start.Local || start.Message != agentjoin.Message(start.Link) || !strings.Contains(start.Link, agentjoin.PathPrefix) || !strings.Contains(start.MCPAddress, "/mcp?join=") || strings.Contains(start.Message, "/mcp") {
		t.Fatalf("start = %+v", start)
	}
	if status, _, page := agentJoinCall(t, http.MethodGet, start.Link, ""); status != http.StatusOK || !strings.Contains(page, "/a/inbox") {
		t.Fatalf("page: %d %q", status, page)
	}
	status, got, _ := agentJoinCall(t, http.MethodPost, start.Link, `{"name":"Claude Code","device":"this MacBook","mayOpen":"the isoform repo"}`)
	if status != http.StatusAccepted {
		t.Fatalf("answer: %d", status)
	}
	join := app.GetAgentJoin()
	if join == nil || join.State != agentjoin.StateRequested || join.Check != got["check"] {
		t.Fatalf("join = %+v", join)
	}
	if err := app.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	_, st, _ := agentJoinCall(t, http.MethodGet, start.Link+"/status", "")
	if st["token"] == "" {
		t.Fatalf("status = %v", st)
	}
	connected := app.GetConnectedAgents()
	if len(connected) != 1 || connected[0].CatalogID != "claude-code" || connected[0].MayOpen != "the isoform repo" {
		t.Fatalf("connected = %+v", connected)
	}
	path, _ := app.connectedAgentsPath()
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), st["token"]) {
		t.Fatalf("saved file: %v (token in the clear: %v)", err, strings.Contains(string(data), st["token"]))
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", info.Mode())
	}
	agents := app.yourAgents()
	if v := agentView(t, agents, "claude-code"); !v.Connected || v.Reach != agentReachConnectedComputer {
		t.Fatalf("Claude Code in Your agents = %+v", v)
	}

	// A restart restores the agent from the file.
	again := &App{}
	again.agentJoin.dir = app.agentJoin.dir
	if got := again.GetConnectedAgents(); len(got) != 1 || got[0].ID != connected[0].ID {
		t.Fatalf("after restart: %+v", got)
	}

	if err := app.DisconnectAgent(connected[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(app.GetConnectedAgents()) != 0 || agentView(t, app.yourAgents(), "claude-code").Connected {
		t.Fatal("still connected after Disconnect")
	}
}

// TestAgentJoinOffAndClosedEyes: no join link while task links are off or
// Closed Eyes is on, and an open link stops answering when either turns.
func TestAgentJoinOffAndClosedEyes(t *testing.T) {
	app, on := newAgentJoinTestApp(t)
	start, err := app.StartAgentJoin()
	if err != nil {
		t.Fatal(err)
	}
	app.closedEyes.enabled.Store(true)
	if _, err := app.StartAgentJoin(); err != errTaskLinksClosedEyes {
		t.Fatalf("start under Closed Eyes: %v", err)
	}
	if status, _, _ := agentJoinCall(t, http.MethodGet, start.Link, ""); status != http.StatusGone || app.GetAgentJoin() != nil {
		t.Fatalf("open link under Closed Eyes: %d", status)
	}
	app.closedEyes.enabled.Store(false)
	on.Store(false)
	if _, err := app.StartAgentJoin(); err != errTaskLinksOff {
		t.Fatalf("start with task links off: %v", err)
	}
}

// TestAnyOtherAgentKeepsItsOwnName: an agent Isoform doesn't know is listed
// under the name it gave itself; a known name maps to its catalog agent.
func TestAnyOtherAgentKeepsItsOwnName(t *testing.T) {
	for name, want := range map[string]string{"Claude Code": "claude-code", "claude": "claude", "Codex CLI": "codex", "Grok Bot": "grok", "Ana's Helper": ""} {
		if got := catalogIDForName(name); got != want {
			t.Errorf("%q = %q, want %q", name, got, want)
		}
	}
	app, _ := newAgentJoinTestApp(t)
	svc := app.agentJoinService()
	svc.Restore([]agentjoin.Agent{{ID: "ag_x", Name: "Ana's Helper", JoinedAt: 1}})
	v := agentView(t, app.yourAgents(), "ag_x")
	if v.Name != "Ana's Helper" || v.Kind != workonit.KindElse || v.Reach != agentReachConnected {
		t.Fatalf("other agent = %+v", v)
	}
}

// TestTaskLinkReadyWhileTheFrontRuns: the front makes task links reach
// agents anywhere, so Work on it offers them while it runs.
func TestTaskLinkReadyWhileTheFrontRuns(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	if app.taskLinkHandoffReady() {
		t.Fatal("ready with only loopback")
	}
	app.front.mu.Lock()
	app.front.running, app.front.info = true, frontLinkInfo{Hostname: testFrontLabel + ".agents.isoform.ai"}
	app.front.mu.Unlock()
	if !app.taskLinkHandoffReady() {
		t.Fatal("not ready while the front runs")
	}
}
