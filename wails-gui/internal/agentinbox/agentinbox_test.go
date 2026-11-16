package agentinbox

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	inbox    *Inbox
	now      time.Time
	off      bool
	taken    []Item
	resulted []Result
}

func newFixture() *fixture {
	f := &fixture{now: time.Unix(1_800_000_000, 0)}
	f.inbox = New(Hooks{
		Authenticate: func(r *http.Request) (string, bool) {
			switch r.Header.Get("Authorization") {
			case "Bearer tok-a":
				return "ag_a", true
			case "Bearer tok-b":
				return "ag_b", true
			}
			return "", false
		},
		Allowed: func() error {
			if f.off {
				return errors.New("off")
			}
			return nil
		},
		Taken:    func(i Item) { f.taken = append(f.taken, i) },
		Resulted: func(_ Item, r Result) { f.resulted = append(f.resulted, r) },
		Now:      func() time.Time { return f.now },
	})
	return f
}

func (f *fixture) call(method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.inbox.ServeHTTP(rec, req)
	return rec
}

func readItems(t *testing.T, rec *httptest.ResponseRecorder) []inboxItem {
	t.Helper()
	var out struct {
		Items []inboxItem `json:"items"`
		Note  string      `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Note == "" {
		t.Fatalf("inbox body %q: %v", rec.Body, err)
	}
	return out.Items
}

// TestEachAgentSeesOnlyItsWork: an agent reads only its own hand-offs, the
// first read marks them taken, and a result closes the item once.
func TestEachAgentSeesOnlyItsWork(t *testing.T) {
	f := newFixture()
	f.inbox.Add(Item{ID: "h1", AgentID: "ag_a", Text: "Write the test plan."})
	f.inbox.Add(Item{ID: "h2", AgentID: "ag_b", Text: "Compare three venues."})
	if rec := f.call(http.MethodGet, InboxPath, "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	items := readItems(t, f.call(http.MethodGet, InboxPath, "tok-a", ""))
	if len(items) != 1 || items[0].Task != "Write the test plan." || !strings.HasSuffix(items[0].ResultURL, ResultsPrefix+"h1") {
		t.Fatalf("a's inbox = %+v", items)
	}
	readItems(t, f.call(http.MethodGet, InboxPath, "tok-a", ""))
	if len(f.taken) != 1 {
		t.Fatalf("taken %d times", len(f.taken))
	}
	if rec := f.call(http.MethodPost, ResultsPrefix+"h1", "tok-b", `{"status":"done","summary":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("b posting a's result: %d", rec.Code)
	}
	result := `{"status":"done","summary":"14 cases.","files":[{"name":"test-plan.md","text":"# Plan"}]}`
	if rec := f.call(http.MethodPost, ResultsPrefix+"h1", "tok-a", result); rec.Code != http.StatusOK {
		t.Fatalf("result: %d %s", rec.Code, rec.Body)
	}
	if len(f.resulted) != 1 || f.resulted[0].Files[0].Name != "test-plan.md" || f.resulted[0].Summary != "14 cases." {
		t.Fatalf("resulted = %+v", f.resulted)
	}
	if rec := f.call(http.MethodPost, ResultsPrefix+"h1", "tok-a", result); rec.Code != http.StatusNotFound {
		t.Fatalf("second result: %d", rec.Code)
	}
	if items := readItems(t, f.call(http.MethodGet, InboxPath, "tok-a", "")); len(items) != 0 {
		t.Fatal("a done item is still in the inbox")
	}
}

func TestInboxPausedExpiresAndWithdraws(t *testing.T) {
	f := newFixture()
	f.inbox.Add(Item{ID: "h1", AgentID: "ag_a", SourceID: "s1", Text: "One."})
	f.inbox.Add(Item{ID: "h2", AgentID: "ag_a", SourceID: "s2", Text: "Two."})
	f.off = true
	if rec := f.call(http.MethodGet, InboxPath, "tok-a", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("paused: %d", rec.Code)
	}
	f.off = false
	if n := f.inbox.Withdraw(func(i Item) bool { return i.SourceID == "s1" }); n != 1 {
		t.Fatalf("withdrew %d", n)
	}
	f.now = f.now.Add(Lifetime + time.Minute)
	if items := f.inbox.Items(); len(items) != 0 {
		t.Fatalf("expired items kept: %+v", items)
	}
}

func TestParseResultFiles(t *testing.T) {
	for _, bad := range []string{
		`{"status":"done","summary":"x","files":[{"name":"../etc","text":"x"}]}`,
		`{"status":"done","summary":"x","files":[{"name":".hidden","text":"x"}]}`,
		`{"status":"done","summary":"x","files":[{"name":"a.md","text":"x"},{"name":"A.md","text":"y"}]}`,
		`{"status":"done","summary":"x","files":[{"name":"a.md","text":"` + strings.Repeat("x", MaxFileBytes+1) + `"}]}`,
		`{"status":"maybe","summary":"x"}`,
		`{"status":"done","summary":"x","run":"rm -rf"}`,
	} {
		if _, err := ParseResult([]byte(bad)); err == nil {
			t.Errorf("accepted %.80s", bad)
		}
	}
	if r, err := ParseResult([]byte(`{"status":"blocked","summary":"Needs a key.","links":["https://example.com/pr/1"]}`)); err != nil || r.Status != "blocked" || len(r.Links) != 1 {
		t.Fatalf("good: %+v %v", r, err)
	}
}
