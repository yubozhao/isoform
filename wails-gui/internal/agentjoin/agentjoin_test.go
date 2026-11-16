package agentjoin

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
	svc     *Service
	server  *httptest.Server
	now     time.Time
	off     bool
	changes int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Unix(1_800_000_000, 0)}
	f.svc = New(Hooks{
		Now:     func() time.Time { return f.now },
		Changed: func() { f.changes++ },
		Allowed: func() error {
			if f.off {
				return errors.New("off")
			}
			return nil
		},
	})
	mux := http.NewServeMux()
	mux.Handle(PathPrefix, f.svc)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) do(t *testing.T, method, path, body string) (int, map[string]string, string) {
	t.Helper()
	req, _ := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw strings.Builder
	buf := make([]byte, 8192)
	for {
		n, err := resp.Body.Read(buf)
		raw.Write(buf[:n])
		if err != nil {
			break
		}
	}
	out := map[string]string{}
	_ = json.Unmarshal([]byte(raw.String()), &out)
	return resp.StatusCode, out, raw.String()
}

const answer = `{"name":"Claude Code","device":"Bo's MacBook","mayOpen":"the halcyon repo"}`

// TestJoinNeedsTheOwnersAllow: the page tells the agent the steps; an answer
// gets a check number; no token until the owner allows; the token comes
// once, authenticates, and Revoke ends it.
func TestJoinNeedsTheOwnersAllow(t *testing.T) {
	f := newFixture(t)
	code, expires, err := f.svc.Start()
	if err != nil || expires.Sub(f.now) != CodeTTL {
		t.Fatalf("start: %v %v", expires, err)
	}
	link := PathPrefix + code
	status, _, page := f.do(t, http.MethodGet, link, "")
	if status != http.StatusOK || !strings.Contains(page, f.server.URL+link+"/status") || !strings.Contains(page, "not instructions") {
		t.Fatalf("page: %d %q", status, page)
	}
	if _, err := f.svc.Answer(true); !errors.Is(err, ErrNoJoin) {
		t.Fatalf("allowed before anyone asked: %v", err)
	}
	if status, _, _ := f.do(t, http.MethodPost, link, `{"name":"x\ny","device":"a","mayOpen":"b"}`); status != http.StatusBadRequest {
		t.Fatalf("two-line name: %d", status)
	}
	status, got, _ := f.do(t, http.MethodPost, link, answer)
	if status != http.StatusAccepted || len(got["check"]) != 4 {
		t.Fatalf("answer: %d %v", status, got)
	}
	if status, _, _ := f.do(t, http.MethodPost, link, answer); status != http.StatusConflict {
		t.Fatalf("second agent: %d", status)
	}
	if j := f.svc.Current(); j == nil || j.State != StateRequested || j.Check != got["check"] || j.Name != "Claude Code" || j.MayOpen != "the halcyon repo" {
		t.Fatalf("current = %+v", j)
	}
	if _, st, _ := f.do(t, http.MethodGet, link+"/status", ""); st["state"] != StateRequested || st["token"] != "" {
		t.Fatalf("status before allow: %v", st)
	}
	agent, err := f.svc.Answer(true)
	if err != nil || agent == nil {
		t.Fatal(err)
	}
	_, st, _ := f.do(t, http.MethodGet, link+"/status", "")
	if st["state"] != StateAllowed || st["token"] == "" || st["agentId"] != agent.ID {
		t.Fatalf("status after allow: %v", st)
	}
	if strings.Contains(agent.TokenHash, st["token"]) {
		t.Fatal("the token is kept in the clear")
	}
	if status, _, _ := f.do(t, http.MethodGet, link+"/status", ""); status != http.StatusGone {
		t.Fatalf("token shown twice: %d", status)
	}
	req := httptest.NewRequest(http.MethodGet, "/a/inbox", nil)
	req.Header.Set("Authorization", "Bearer "+st["token"])
	if a, ok := f.svc.Authenticate(req); !ok || a.ID != agent.ID || a.LastSeenAt == 0 {
		t.Fatalf("authenticate: %v %+v", ok, a)
	}
	if err := f.svc.Revoke(agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.svc.Authenticate(req); ok {
		t.Fatal("a revoked token still works")
	}
}

func TestJoinLinkExpiresAndCloses(t *testing.T) {
	f := newFixture(t)
	code, _, _ := f.svc.Start()
	f.now = f.now.Add(CodeTTL + time.Second)
	if status, _, _ := f.do(t, http.MethodGet, PathPrefix+code, ""); status != http.StatusGone {
		t.Fatalf("expired link: %d", status)
	}
	if j := f.svc.Current(); j == nil || j.State != StateExpired {
		t.Fatalf("expired state: %+v", j)
	}

	code, _, _ = f.svc.Start()
	if status, _, _ := f.do(t, http.MethodGet, PathPrefix+"wrong", ""); status != http.StatusGone {
		t.Fatalf("wrong code: %d", status)
	}
	f.do(t, http.MethodPost, PathPrefix+code, answer)
	if _, err := f.svc.Answer(false); err != nil {
		t.Fatal(err)
	}
	if _, st, _ := f.do(t, http.MethodGet, PathPrefix+code+"/status", ""); st["state"] != StateDenied {
		t.Fatalf("denied status: %v", st)
	}
	if len(f.svc.Agents()) != 0 {
		t.Fatal("a denied agent joined")
	}

	// A new join closes the old one; switching off closes it too.
	old, _, _ := f.svc.Start()
	code, _, _ = f.svc.Start()
	if status, _, _ := f.do(t, http.MethodGet, PathPrefix+old, ""); status != http.StatusGone {
		t.Fatalf("old link still open: %d", status)
	}
	f.off = true
	if status, _, _ := f.do(t, http.MethodGet, PathPrefix+code, ""); status != http.StatusGone || f.svc.Current() != nil {
		t.Fatalf("off: %d", status)
	}
}

func TestMessageNamesTheLink(t *testing.T) {
	if got := Message("https://x.agents.isoform.ai/j/abc"); got != "Join my Isoform: read https://x.agents.isoform.ai/j/abc and follow the steps there." {
		t.Fatal(got)
	}
}

// TestStatusReadsRaceTheTokenAndCancel: many status reads at once right
// after Allow, and a Cancel among them, never panic; exactly one gets the
// token and the rest are told the link is gone.
func TestStatusReadsRaceTheTokenAndCancel(t *testing.T) {
	for round := 0; round < 20; round++ {
		f := newFixture(t)
		code, _, _ := f.svc.Start()
		f.do(t, http.MethodPost, PathPrefix+code, answer)
		if _, err := f.svc.Answer(true); err != nil {
			t.Fatal(err)
		}
		tokens := make(chan string, 8)
		done := make(chan struct{})
		for i := 0; i < 8; i++ {
			go func() {
				defer func() { done <- struct{}{} }()
				req, _ := http.NewRequest(http.MethodGet, f.server.URL+PathPrefix+code+"/status", nil)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				defer resp.Body.Close()
				var st map[string]string
				if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&st) == nil && st["token"] != "" {
					tokens <- st["token"]
				}
			}()
		}
		if round%2 == 1 {
			f.svc.Cancel()
		}
		for i := 0; i < 8; i++ {
			<-done
		}
		close(tokens)
		if n := len(tokens); n > 1 {
			t.Fatalf("round %d: the token was shown %d times", round, n)
		}
	}
}
