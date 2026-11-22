package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"wails-gui/internal/tasklink"
	"wails-gui/internal/workonit"
)

// newWorkOnItTaskLinkApp is the Work on it rig with task links on and a
// public bridge origin set, so task_link is offered.
func newWorkOnItTaskLinkApp(t *testing.T) (*App, *workOnItSeams) {
	t.Helper()
	app, seams := newWorkOnItTestApp(t, true)
	t.Setenv(taskLinkAddrEnv, "127.0.0.1:0")
	t.Setenv(taskLinkBaseURLEnv, "https://isoform-tasks.example.test")
	app.taskLinks.dir = t.TempDir()
	t.Cleanup(app.shutdownTaskLinks)
	return app, seams
}

func methodsOf(t *testing.T, app *App, agentID string) []string {
	t.Helper()
	agents, err := app.GetWorkOnItAgents()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.ID == agentID {
			return a.Methods
		}
	}
	return nil
}

func TestWorkOnItOffersTaskLinkFirstOnlyWithAPublicOrigin(t *testing.T) {
	app, _ := newWorkOnItTaskLinkApp(t)
	if m := methodsOf(t, app, "claude"); len(m) == 0 || m[0] != workonit.MethodTaskLink {
		t.Fatalf("claude methods = %v, want task_link first", m)
	}
	if m := methodsOf(t, app, "chatgpt"); len(m) == 0 || m[0] != workonit.MethodTaskLink {
		t.Fatalf("chatgpt methods = %v, want task_link first", m)
	}
	// Every agent can fetch a page, so every agent gets the link first.
	for _, id := range []string{"chatgpt", "grok", "muse"} {
		if m := methodsOf(t, app, id); len(m) == 0 || m[0] != workonit.MethodTaskLink {
			t.Fatalf("%s methods = %v, want task_link first", id, m)
		}
	}
	// Without a public origin a loopback link is useless to an agent.
	t.Setenv(taskLinkBaseURLEnv, "")
	for _, m := range methodsOf(t, app, "claude") {
		if m == workonit.MethodTaskLink {
			t.Fatal("task_link offered with no public origin")
		}
	}
}

// TestWorkOnItTaskLinkSendsOneLineAndCountsTakenAndDone: the agent gets one
// line with the link (never the task text); the link serves exactly the
// prepared text; the first read marks the record taken and the result marks
// it done.
func TestWorkOnItTaskLinkSendsOneLineAndCountsTakenAndDone(t *testing.T) {
	app, seams := newWorkOnItTaskLinkApp(t)
	prepared, err := app.PrepareWorkOnIt(briefInput())
	if err != nil {
		t.Fatal(err)
	}
	sent, err := app.SendWorkOnIt(prepared.TaskID, "claude", workonit.MethodTaskLink)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Method != workonit.MethodTaskLink || sent.Via != workonit.MethodDeepLink || sent.Copied {
		t.Fatalf("sent = %+v, want a task link by Claude's deep link", sent)
	}
	if len(seams.opened) != 1 || len(seams.copied) != 0 {
		t.Fatalf("opened=%v copied=%v", seams.opened, seams.copied)
	}
	u, err := url.Parse(seams.opened[0])
	if err != nil {
		t.Fatal(err)
	}
	line := u.Query().Get("q")
	if strings.Contains(line, "Draft the launch note") {
		t.Fatal("the task text must stay behind the link")
	}
	const base = "https://isoform-tasks.example.test" + tasklink.PathPrefix
	i := strings.Index(line, base)
	if i < 0 {
		t.Fatalf("no link in %q", line)
	}
	token := strings.Fields(line[i+len(base):])[0]
	token = strings.TrimRight(token, ".")
	if line != tasklink.Prompt(base+token) {
		t.Fatalf("line = %q", line)
	}

	rec := httptest.NewRecorder()
	app.taskLinkStore().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tasklink.PathPrefix+token, nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), prepared.Text) {
		t.Fatalf("GET = %d %q", rec.Code, rec.Body)
	}
	records, err := app.workOnItRead()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != sent.RecordID || records[0].Status != workonit.StatusTaken || records[0].Method != workonit.MethodTaskLink {
		t.Fatalf("after GET records = %+v", records)
	}

	req := httptest.NewRequest(http.MethodPost, tasklink.PathPrefix+token, strings.NewReader(`{"status":"done","summary":"Drafted it."}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.taskLinkStore().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	records, _ = app.workOnItRead()
	if records[0].Status != workonit.StatusDone || records[0].DoneAt == 0 {
		t.Fatalf("after POST records = %+v", records)
	}
	counts, err := app.GetWorkOnItCounts()
	if err != nil {
		t.Fatal(err)
	}
	if counts.Sent != 1 || counts.Taken != 1 || counts.Done != 1 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestWorkOnItTaskLinkBlockedIsNotDone(t *testing.T) {
	app, seams := newWorkOnItTaskLinkApp(t)
	prepared, err := app.PrepareWorkOnIt(briefInput())
	if err != nil {
		t.Fatal(err)
	}
	// ChatGPT has no deep link: the line goes on the clipboard.
	sent, err := app.SendWorkOnIt(prepared.TaskID, "chatgpt", workonit.MethodTaskLink)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Via != workonit.MethodOpenCopy || !sent.Copied || len(seams.copied) != 1 {
		t.Fatalf("sent = %+v copied = %v", sent, seams.copied)
	}
	line := seams.copied[0]
	i := strings.Index(line, tasklink.PathPrefix)
	token := strings.TrimRight(strings.Fields(line[i+len(tasklink.PathPrefix):])[0], ".")
	req := httptest.NewRequest(http.MethodPost, tasklink.PathPrefix+token, strings.NewReader(`{"status":"blocked","summary":"Need the venue name."}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.taskLinkStore().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d", rec.Code)
	}
	records, _ := app.workOnItRead()
	if len(records) != 1 || records[0].Status != workonit.StatusNotDone {
		t.Fatalf("records = %+v", records)
	}
}

func TestWorkOnItTaskLinkWithdrawnWhenTheLineCannotBeDelivered(t *testing.T) {
	app, _ := newWorkOnItTaskLinkApp(t)
	app.workOnIt().open = func(string) error { return errWorkOnItNoMethod }
	prepared, err := app.PrepareWorkOnIt(briefInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SendWorkOnIt(prepared.TaskID, "claude", workonit.MethodTaskLink); err == nil {
		t.Fatal("a failed delivery reported success")
	}
	if n := len(app.taskLinkStore().Links()); n != 0 {
		t.Fatalf("%d link(s) left open after a failed delivery", n)
	}
}
