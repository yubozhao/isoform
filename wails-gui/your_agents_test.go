package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/workonit"
)

func agentView(t *testing.T, agents YourAgents, id string) YourAgentView {
	t.Helper()
	for _, v := range agents.Agents {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("%s not in Your agents", id)
	return YourAgentView{}
}

// TestYourAgentsTwoChoices: the list holds all nine with their kind and how
// each is reached; a choice is empty until picked, takes only an agent for
// that kind of work, and is one setting read back by both devices.
func TestYourAgentsTwoChoices(t *testing.T) {
	app, _ := newWorkOnItTestApp(t, true)
	app.workOnIt().paste = &pasteDeps{lookPath: pasteLookPath("claude"), frontmost: func() string { return "" }, now: time.Now}
	agents, err := app.GetYourAgents()
	if err != nil {
		t.Fatal(err)
	}
	if len(agents.Agents) != 9 || agents.Choices[workonit.KindCode] != "" || agents.Choices[workonit.KindElse] != "" {
		t.Fatalf("fresh: %+v", agents)
	}
	for id, reach := range map[string]string{"codex": agentReachOnComputer, "claude": agentReachItsApp, "instinct": agentReachByText} {
		if got := agentView(t, agents, id).Reach; got != reach {
			t.Errorf("%s reach = %s, want %s", id, got, reach)
		}
	}
	if !agentView(t, agents, "claude-code").Found || agentView(t, agents, "cursor").Found {
		t.Error("found: Claude Code is on the PATH, Cursor isn't")
	}

	if _, err := app.SetYourAgent(workonit.KindCode, "claude"); err != errYourAgentWrongFor {
		t.Fatalf("Claude for code: %v", err)
	}
	if _, err := app.SetYourAgent("bogus", "codex"); err != errYourAgentBadKind {
		t.Fatalf("bad kind: %v", err)
	}
	if _, err := app.SetYourAgent(workonit.KindElse, "nobody"); err != errWorkOnItNoAgent {
		t.Fatalf("unknown agent: %v", err)
	}
	if _, err := app.SetYourAgent(workonit.KindCode, "codex"); err != nil {
		t.Fatal(err)
	}
	agents, err = app.SetYourAgent(workonit.KindElse, "grok")
	if err != nil {
		t.Fatal(err)
	}
	if agents.Choices[workonit.KindCode] != "codex" || agents.Choices[workonit.KindElse] != "grok" {
		t.Fatalf("choices = %v", agents.Choices)
	}
	if agents, _ = app.SetYourAgent(workonit.KindElse, ""); agents.Choices[workonit.KindElse] != "" {
		t.Fatal("clearing kept the choice")
	}
}

// TestWorkOnItDefaultIsYourAgent: the person's choice for the work's kind is
// the default when it can take the hand-off; otherwise the ranked top stays.
func TestWorkOnItDefaultIsYourAgent(t *testing.T) {
	app, _ := newWorkOnItTestApp(t, true)
	defaultOf := func(p WorkOnItPrepared) string {
		for _, v := range p.Agents {
			if v.Default {
				return v.ID
			}
		}
		return ""
	}
	prepared, err := app.PrepareWorkOnIt(briefInput())
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Kind != workonit.KindElse || defaultOf(prepared) != "claude" {
		t.Fatalf("no choice: kind=%s default=%s", prepared.Kind, defaultOf(prepared))
	}
	if _, err := app.SetYourAgent(workonit.KindElse, "chatgpt"); err != nil {
		t.Fatal(err)
	}
	if prepared, _ = app.PrepareWorkOnIt(briefInput()); defaultOf(prepared) != "chatgpt" {
		t.Fatalf("choice ignored: %s", defaultOf(prepared))
	}
	// Codex isn't installed here, so a code task keeps the ranked default.
	if _, err := app.SetYourAgent(workonit.KindCode, "codex"); err != nil {
		t.Fatal(err)
	}
	in := briefInput()
	in.Kind = workonit.KindCode
	if prepared, _ = app.PrepareWorkOnIt(in); prepared.Kind != workonit.KindCode || defaultOf(prepared) != "claude" {
		t.Fatalf("code: kind=%s default=%s", prepared.Kind, defaultOf(prepared))
	}
}

// TestWorkOnItKeepsEachHandoff: a send keeps exactly what went, when and to
// whom; Forget removes one or all; a withdrawn source takes its hand-offs.
func TestWorkOnItKeepsEachHandoff(t *testing.T) {
	app, seams := newWorkOnItTestApp(t, true)
	send := func(sourceID string) WorkOnItSent {
		in := briefInput()
		in.SourceID = sourceID
		prepared, err := app.PrepareWorkOnIt(in)
		if err != nil {
			t.Fatal(err)
		}
		sent, err := app.SendWorkOnIt(prepared.TaskID, "claude", workonit.MethodDeepLink)
		if err != nil {
			t.Fatal(err)
		}
		return sent
	}
	first := send("b1")
	seams.now = seams.now.Add(time.Minute)
	send("b2")
	list, err := app.GetWorkOnItHandoffs()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].SourceID != "b2" {
		t.Fatalf("hand-offs = %+v", list)
	}
	h := list[1]
	if h.ID != first.RecordID || h.AgentID != "claude" || h.Method != workonit.MethodDeepLink || h.Surface != "computer" || h.Kind != workonit.KindElse {
		t.Fatalf("kept = %+v", h)
	}
	if !strings.Contains(h.Text, "Draft the launch note") || !strings.Contains(h.Text, "One page") {
		t.Fatalf("text = %q", h.Text)
	}
	path, _ := workOnItHandoffsPath()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log file: %v %v", info, err)
	}

	app.workOnItPrivacyWithdrawal(privacyWithdrawal{Scope: privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: workonit.SourceBriefItem, ID: "b2"}, Retention: true})
	if list, _ = app.GetWorkOnItHandoffs(); len(list) != 2 {
		t.Fatal("a retention removal dropped a hand-off")
	}
	app.workOnItPrivacyWithdrawal(privacyWithdrawal{Scope: privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: workonit.SourceBriefItem, ID: "b2"}})
	if list, _ = app.GetWorkOnItHandoffs(); len(list) != 1 || list[0].SourceID != "b1" {
		t.Fatalf("after withdrawing b2: %+v", list)
	}
	if err := app.ForgetWorkOnItHandoff("nope"); err != errWorkOnItNoHandoff {
		t.Fatalf("forget unknown: %v", err)
	}
	if err := app.ForgetWorkOnItHandoff(first.RecordID); err != nil {
		t.Fatal(err)
	}
	send("b3")
	if err := app.ForgetWorkOnItHandoff(""); err != nil {
		t.Fatal(err)
	}
	if list, _ = app.GetWorkOnItHandoffs(); len(list) != 0 {
		t.Fatalf("forget all left %d", len(list))
	}
}

// TestYourAgentsPhoneRoutes: the phone reads the same setting and changes it
// with a receipted POST.
func TestYourAgentsPhoneRoutes(t *testing.T) {
	app, _ := newWorkOnItTestApp(t, true)
	bridge := &mobileBridge{app: app, ctx: t.Context()}
	device := mobileDeviceRecord{MobileDeviceView: MobileDeviceView{DeviceID: "phone-1"}}
	post := func(body []byte) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/agents/choice", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"idempotencyKey": "k1", "kind": "code", "agentId": "codex"})
	bridge.serveYourAgentChoice(rec, post(body), device)
	if rec.Code != http.StatusOK {
		t.Fatalf("choice: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	body, _ = json.Marshal(map[string]string{"idempotencyKey": "k2", "kind": "code", "agentId": "grok"})
	bridge.serveYourAgentChoice(rec, post(body), device)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Grok for code: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	bridge.serveYourAgents(rec)
	var got struct {
		Data YourAgents `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	if got.Data.Choices["code"] != "codex" || len(got.Data.Agents) != 9 {
		t.Fatalf("GET /agents = %s", rec.Body)
	}
	if computer, _ := app.GetYourAgents(); computer.Choices["code"] != "codex" {
		t.Fatal("the computer doesn't see the phone's choice")
	}
}
