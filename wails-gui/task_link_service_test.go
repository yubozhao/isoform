package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/privacy"
	"wails-gui/internal/tasklink"
)

const taskLinkTestText = "Draft the reply to Dana about the venue."

func newTaskLinkTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv(taskLinkAddrEnv, "127.0.0.1:0")
	t.Setenv(taskLinkBaseURLEnv, "")
	a := &App{}
	a.taskLinks.dir = t.TempDir()
	t.Cleanup(a.shutdownTaskLinks)
	return a
}

func taskLinkToken(t *testing.T, link string) string {
	t.Helper()
	i := strings.Index(link, tasklink.PathPrefix)
	if i < 0 {
		t.Fatalf("no /t/ in %q", link)
	}
	return link[i+len(tasklink.PathPrefix):]
}

func taskLinkGet(t *testing.T, a *App, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	a.taskLinkStore().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tasklink.PathPrefix+token, nil))
	return rec
}

func taskLinkPost(t *testing.T, a *App, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, tasklink.PathPrefix+token, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.taskLinkStore().ServeHTTP(rec, req)
	return rec
}

func TestTaskLinkCreateServesOnLoopbackAndRecordsTheResult(t *testing.T) {
	a := newTaskLinkTestApp(t)
	var taken, resulted int
	a.taskLinks.onTaken = func(tasklink.Link) { taken++ }
	a.taskLinks.onResult = func(tasklink.Link, tasklink.Result) { resulted++ }
	link, err := a.createTaskLink(tasklink.Link{ID: "task-1", AgentID: "claude", SourceKind: "brief_item", SourceID: "item-9", Text: taskLinkTestText})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "http://127.0.0.1:") {
		t.Fatalf("link = %q, want the loopback listener", link)
	}
	// The real listener answers the link.
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), taskLinkTestText) {
		t.Fatalf("GET = %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("a task link must not allow cross-origin reads")
	}
	if rec := taskLinkPost(t, a, taskLinkToken(t, link), `{"status":"done","summary":"Sent the draft.","links":["https://example.com/doc"]}`); rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	if taken != 1 || resulted != 1 {
		t.Fatalf("taken=%d resulted=%d, want 1 and 1", taken, resulted)
	}
	results, err := a.GetTaskLinkResults()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != "done" || results[0].Summary != "Sent the draft." || results[0].SourceID != "item-9" || results[0].TakenAt == 0 {
		t.Fatalf("results = %+v", results)
	}
	// Results keep what came back, never the task text.
	data, _ := os.ReadFile(filepath.Join(a.taskLinks.dir, taskLinksResultsFile))
	if strings.Contains(string(data), "Dana") {
		t.Fatal("the results file must not keep the task text")
	}
	if rec := taskLinkGet(t, a, taskLinkToken(t, link)); rec.Code != http.StatusGone {
		t.Fatalf("GET after the result = %d, want 410", rec.Code)
	}
}

func TestTaskLinkSurvivesARestartAsAHashOnly(t *testing.T) {
	a := newTaskLinkTestApp(t)
	link, err := a.createTaskLink(tasklink.Link{ID: "task-1", AgentID: "claude", Text: taskLinkTestText})
	if err != nil {
		t.Fatal(err)
	}
	token := taskLinkToken(t, link)
	data, err := os.ReadFile(filepath.Join(a.taskLinks.dir, taskLinksOpenFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) {
		t.Fatal("the open file must keep only the token's hash")
	}
	info, _ := os.Stat(filepath.Join(a.taskLinks.dir, taskLinksOpenFile))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("open file mode = %v, want 0600", info.Mode().Perm())
	}
	restarted := newTaskLinkTestApp(t)
	restarted.taskLinks.dir = a.taskLinks.dir
	if rec := taskLinkGet(t, restarted, token); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), taskLinkTestText) {
		t.Fatalf("GET after restart = %d %q", rec.Code, rec.Body)
	}
}

func TestTaskLinkClosedEyesRefusesAndWithdraws(t *testing.T) {
	a := newTaskLinkTestApp(t)
	link, err := a.createTaskLink(tasklink.Link{ID: "task-1", AgentID: "claude", Text: taskLinkTestText})
	if err != nil {
		t.Fatal(err)
	}
	a.closedEyes.enabled.Publish(true, time.Time{})
	if _, err := a.createTaskLink(tasklink.Link{ID: "task-2", AgentID: "claude", Text: taskLinkTestText}); err != errTaskLinksClosedEyes {
		t.Fatalf("create under Closed Eyes = %v", err)
	}
	if rec := taskLinkGet(t, a, taskLinkToken(t, link)); rec.Code != http.StatusGone {
		t.Fatalf("GET under Closed Eyes = %d, want 410", rec.Code)
	}
	// The refused read spent the link: it stays gone after Closed Eyes ends.
	a.closedEyes.enabled.Publish(false, time.Time{})
	if rec := taskLinkGet(t, a, taskLinkToken(t, link)); rec.Code != http.StatusGone {
		t.Fatalf("GET after Closed Eyes = %d, want 410", rec.Code)
	}
	// No switch: with Closed Eyes off a new link is made again.
	if _, err := a.createTaskLink(tasklink.Link{ID: "task-3", AgentID: "claude", Text: taskLinkTestText}); err != nil {
		t.Fatalf("create after Closed Eyes = %v", err)
	}
}

func TestTaskLinkPrivacyWithdrawalSpendsOpenLinksButRetentionDoesNot(t *testing.T) {
	a := newTaskLinkTestApp(t)
	a.taskLinkStore()
	first, err := a.createTaskLink(tasklink.Link{ID: "task-1", AgentID: "claude", SourceKind: "brief_item", SourceID: "item-1", Text: taskLinkTestText})
	if err != nil {
		t.Fatal(err)
	}
	publishRetentionRemoval(privacy.Scope{Kind: privacy.Session, ID: "old-session"})
	if rec := taskLinkGet(t, a, taskLinkToken(t, first)); rec.Code != http.StatusOK {
		t.Fatalf("GET after retention cleanup = %d, want 200", rec.Code)
	}
	a.taskLinkPrivacyWithdrawal(privacy.Withdrawal{Scope: privacy.Scope{Kind: privacy.Source, SourceKind: "memory", ID: "m-1"}})
	if rec := taskLinkGet(t, a, taskLinkToken(t, first)); rec.Code != http.StatusGone {
		t.Fatalf("GET after a forget = %d, want 410", rec.Code)
	}
	data, _ := os.ReadFile(filepath.Join(a.taskLinks.dir, taskLinksOpenFile))
	if strings.Contains(string(data), "task-1") {
		t.Fatal("a withdrawn link must leave the open file")
	}
}

func TestTaskLinkWithdrawnSourceDropsItsResults(t *testing.T) {
	a := newTaskLinkTestApp(t)
	for _, src := range []string{"item-1", "item-2"} {
		link, err := a.createTaskLink(tasklink.Link{ID: "task-" + src, AgentID: "claude", SourceKind: "brief_item", SourceID: src, Text: taskLinkTestText})
		if err != nil {
			t.Fatal(err)
		}
		if rec := taskLinkPost(t, a, taskLinkToken(t, link), `{"status":"done","summary":"ok"}`); rec.Code != http.StatusOK {
			t.Fatalf("POST = %d", rec.Code)
		}
	}
	a.taskLinkPrivacyWithdrawal(privacy.Withdrawal{Scope: privacy.Scope{Kind: privacy.Source, SourceKind: "brief_item", ID: "item-1"}})
	results, err := a.GetTaskLinkResults()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SourceID != "item-2" {
		t.Fatalf("results = %+v, want only item-2", results)
	}
}

func TestTaskLinkListenerBindsOnlyLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8792", ":8792", "192.168.1.5:8792", "example.com:80"} {
		if err := loopbackListenAddr(addr); err == nil {
			t.Errorf("%s was accepted", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8792", "[::1]:8792", "localhost:8792"} {
		if err := loopbackListenAddr(addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	a := newTaskLinkTestApp(t)
	t.Setenv(taskLinkAddrEnv, "0.0.0.0:0")
	if err := a.startTaskLinkListener(); err != errTaskLinkNotLoopback {
		t.Fatalf("start on 0.0.0.0 = %v", err)
	}
}

func TestTaskLinkBaseURLMustBeAnHTTPSOrigin(t *testing.T) {
	a := newTaskLinkTestApp(t)
	t.Setenv(taskLinkBaseURLEnv, "https://tasks.example.test/")
	if base, err := a.taskLinkBaseURL(); err != nil || base != "https://tasks.example.test" {
		t.Fatalf("base = %q %v", base, err)
	}
	for _, bad := range []string{"http://example.com", "https://example.com/path", "https://example.com/?q=1", "not a url"} {
		t.Setenv(taskLinkBaseURLEnv, bad)
		if _, err := a.taskLinkBaseURL(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}
