package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wails-gui/internal/agentinbox"
	"wails-gui/internal/workonit"
)

// joinTestAgent joins an agent the way a real one does and returns its token.
func joinTestAgent(t *testing.T, app *App, name string) (string, string) {
	t.Helper()
	start, err := app.StartAgentJoin()
	if err != nil {
		t.Fatal(err)
	}
	if status, _, _ := agentJoinCall(t, http.MethodPost, start.Link, `{"name":"`+name+`","device":"this computer","mayOpen":"the repo"}`); status != http.StatusAccepted {
		t.Fatalf("answer: %d", status)
	}
	if err := app.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	_, st, _ := agentJoinCall(t, http.MethodGet, start.Link+"/status", "")
	if st["token"] == "" {
		t.Fatalf("no token: %v", st)
	}
	base := strings.TrimSuffix(start.Link, start.Link[strings.Index(start.Link, "/j/"):])
	return st["token"], base
}

func agentCall(t *testing.T, method, url, token, body string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

type inboxView struct {
	Items []struct {
		ID        string `json:"id"`
		Task      string `json:"task"`
		ResultURL string `json:"resultUrl"`
	} `json:"items"`
}

// TestConnectedAgentGetsTheWorkAndReportsBack: once Claude Code has joined,
// the sheet offers it the work directly; Send puts the task in its inbox;
// it reads the task, posts its result with a file, and the result lands
// with the task-link results, the file kept, the record done. Nothing is
// copied or opened.
func TestConnectedAgentGetsTheWorkAndReportsBack(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	seams := &workOnItSeams{}
	s := app.workOnIt()
	s.open = func(target string) error { seams.opened = append(seams.opened, target); return nil }
	s.copy = func(text string) error { seams.copied = append(seams.copied, text); return nil }
	token, base := joinTestAgent(t, app, "Claude Code")

	in := briefInput()
	in.Kind = workonit.KindCode
	prepared, err := app.PrepareWorkOnIt(in)
	if err != nil {
		t.Fatal(err)
	}
	var view WorkOnItAgentView
	for _, v := range prepared.Agents {
		if v.ID == "claude-code" {
			view = v
		}
	}
	if len(view.Methods) == 0 || view.Methods[0] != workonit.MethodConnected {
		t.Fatalf("Claude Code in the sheet = %+v (all: %+v)", view, prepared.Agents)
	}
	sent, err := app.SendWorkOnIt(prepared.TaskID, "claude-code", workonit.MethodConnected)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Via != "inbox" || sent.Copied || len(seams.opened)+len(seams.copied) != 0 {
		t.Fatalf("sent = %+v, opened %v, copied %v", sent, seams.opened, seams.copied)
	}

	status, raw := agentCall(t, http.MethodGet, base+agentinbox.InboxPath, token, "")
	var inbox inboxView
	if status != http.StatusOK || json.Unmarshal(raw, &inbox) != nil || len(inbox.Items) != 1 || !strings.Contains(inbox.Items[0].Task, "Draft the launch note") {
		t.Fatalf("inbox: %d %s", status, raw)
	}
	if status, _ := agentCall(t, http.MethodGet, base+agentinbox.InboxPath, "wrong", ""); status != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", status)
	}
	result := `{"status":"done","summary":"Drafted it.","files":[{"name":"launch-note.md","text":"# Launch"}]}`
	if status, body := agentCall(t, http.MethodPost, inbox.Items[0].ResultURL, token, result); status != http.StatusOK {
		t.Fatalf("result: %d %s", status, body)
	}
	results, _ := app.GetTaskLinkResults()
	if len(results) != 1 || results[0].Via != "inbox" || results[0].AgentID != "claude-code" || results[0].Files[0] != "launch-note.md" || results[0].LinkID != sent.RecordID {
		t.Fatalf("results = %+v", results)
	}
	dir, _ := app.agentFilesDir(sent.RecordID)
	if info, err := os.Stat(filepath.Join(dir, "launch-note.md")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", info, err)
	}
	records, _ := app.workOnItRead()
	if len(records) != 1 || records[0].Status != workonit.StatusDone {
		t.Fatalf("records = %+v", records)
	}
	handoffs, _ := app.GetWorkOnItHandoffs()
	if len(handoffs) != 1 || handoffs[0].Via != "inbox" || handoffs[0].Method != workonit.MethodConnected {
		t.Fatalf("hand-offs = %+v", handoffs)
	}
	var opened [][]string
	app.agentInbox.openFile = func(args []string) error { opened = append(opened, args); return nil }
	if err := app.OpenAgentFile(sent.RecordID, "launch-note.md"); err != nil || len(opened) != 1 || opened[0][0] != "-e" || filepath.Base(opened[0][1]) != "launch-note.md" {
		t.Fatalf("open: %v %v", err, opened)
	}
	for _, bad := range []string{"../connected.json", "other.md", ".source.json"} {
		if err := app.OpenAgentFile(sent.RecordID, bad); err != errAgentFileUnknown {
			t.Errorf("opened %q: %v", bad, err)
		}
	}
}

// TestDisconnectAndWithdrawalSpendTheInbox: Disconnect drops the agent's
// waiting work and its token; any privacy withdrawal spends waiting work,
// and a withdrawn source's files go.
func TestDisconnectAndWithdrawalSpendTheInbox(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	token, base := joinTestAgent(t, app, "Ana's Helper")
	joined := app.GetConnectedAgents()[0]
	send := func(sourceID string) string {
		in := briefInput()
		in.SourceID = sourceID
		prepared, err := app.PrepareWorkOnIt(in)
		if err != nil {
			t.Fatal(err)
		}
		sent, err := app.SendWorkOnIt(prepared.TaskID, joined.ID, workonit.MethodConnected)
		if err != nil {
			t.Fatal(err)
		}
		return sent.RecordID
	}
	first := send("b1")
	agentCall(t, http.MethodPost, base+agentinbox.ResultsPrefix+first, token, `{"status":"done","summary":"ok","files":[{"name":"a.md","text":"x"}]}`)
	send("b2")
	app.agentInboxPrivacyWithdrawal(privacyWithdrawal{Scope: privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: workonit.SourceBriefItem, ID: "b1"}})
	if n := len(app.agentInboxService().Items()); n != 0 {
		t.Fatalf("%d waiting after a withdrawal", n)
	}
	if dir, _ := app.agentFilesDir(first); func() bool { _, err := os.Stat(dir); return err == nil }() {
		t.Fatal("a withdrawn source's files were kept")
	}
	send("b3")
	if err := app.DisconnectAgent(joined.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(app.agentInboxService().Items()); n != 0 {
		t.Fatalf("%d waiting after Disconnect", n)
	}
	if status, _ := agentCall(t, http.MethodGet, base+agentinbox.InboxPath, token, ""); status != http.StatusUnauthorized {
		t.Fatalf("token after Disconnect: %d", status)
	}
}

// TestPhoneHandsOffToAConnectedAgent: the phone's Yes goes to the person's
// agent for the work when it is connected, and is refused otherwise.
func TestPhoneHandsOffToAConnectedAgent(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	bridge := &mobileBridge{app: app, ctx: t.Context()}
	device := mobileDeviceRecord{MobileDeviceView: MobileDeviceView{DeviceID: "phone-1"}}
	post := func(key string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"idempotencyKey": key, "kind": "code", "sourceKind": workonit.SourceBriefItem, "sourceId": "b1", "title": "Write the test plan"})
		r := httptest.NewRequest(http.MethodPost, "/agents/handoffs", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		bridge.serveAgentHandoff(rec, r, device)
		return rec
	}
	if _, err := app.SetYourAgent(workonit.KindCode, "codex"); err != nil {
		t.Fatal(err)
	}
	if rec := post("k1"); rec.Code != http.StatusConflict {
		t.Fatalf("not connected: %d %s", rec.Code, rec.Body)
	}
	joinTestAgent(t, app, "Codex")
	rec := post("k2")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"agentId":"codex"`) {
		t.Fatalf("connected: %d %s", rec.Code, rec.Body)
	}
	items := app.agentInboxService().Items()
	if len(items) != 1 || !strings.Contains(items[0].Text, "Write the test plan") {
		t.Fatalf("inbox = %+v", items)
	}
	handoffs, _ := app.GetWorkOnItHandoffs()
	if len(handoffs) != 1 || handoffs[0].Surface != "phone" || handoffs[0].Kind != workonit.KindCode {
		t.Fatalf("hand-offs = %+v", handoffs)
	}
}

// TestAgentInboxSurvivesARestart: after a restart the listener comes back
// for a joined agent with no open task link, its waiting hand-off is still
// there, and a withdrawal before anything touched the inbox spends it.
func TestAgentInboxSurvivesARestart(t *testing.T) {
	app, on := newAgentJoinTestApp(t)
	token, _ := joinTestAgent(t, app, "Ana's Helper")
	joined := app.GetConnectedAgents()[0]
	prepared, err := app.PrepareWorkOnIt(briefInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SendWorkOnIt(prepared.TaskID, joined.ID, workonit.MethodConnected); err != nil {
		t.Fatal(err)
	}
	app.shutdownTaskLinks()

	// The same data folders, a fresh app: the launch task alone.
	restart := func() *App {
		again, _ := newWorkOnItTestApp(t, true)
		again.taskLinks.dir, again.taskLinks.enabled, again.agentJoin.dir = app.taskLinks.dir, on.Load, app.agentJoin.dir
		t.Cleanup(again.shutdownTaskLinks)
		again.startTaskLinksIfEnabled()
		return again
	}
	again := restart()
	base, err := again.taskLinkBaseURL()
	if err != nil {
		t.Fatal(err)
	}
	status, raw := agentCall(t, http.MethodGet, base+agentinbox.InboxPath, token, "")
	var inbox inboxView
	if status != http.StatusOK || json.Unmarshal(raw, &inbox) != nil || len(inbox.Items) != 1 {
		t.Fatalf("inbox after a restart: %d %s", status, raw)
	}
	again.shutdownTaskLinks()

	third := restart()
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: workonit.SourceBriefItem, ID: "b1"})
	if n := len(third.agentInboxService().Items()); n != 0 {
		t.Fatalf("%d waiting after a withdrawal at launch", n)
	}
}

// TestAgentFilesNeverRun: a file an agent posts opens only as text, in
// TextEdit; anything that could run or open something (.terminal, .command,
// .app, .webloc, .fileloc, .scpt, .workflow, .html) is only shown in Finder.
func TestAgentFilesNeverRun(t *testing.T) {
	for _, name := range []string{"notes.md", "plan.TXT", "data.csv", "out.json", "fix.patch"} {
		if args := agentFileOpenArgs("/x/" + name); args[0] != "-e" {
			t.Errorf("%s opened with %v", name, args)
		}
	}
	for _, name := range []string{"run.terminal", "go.command", "x.app", "site.webloc", "path.fileloc", "s.scpt", "w.workflow", "page.html", "noext", "x.sh", "x.py"} {
		if args := agentFileOpenArgs("/x/" + name); args[0] != "-R" {
			t.Errorf("%s opened with %v, want only Finder", name, args)
		}
	}
}
