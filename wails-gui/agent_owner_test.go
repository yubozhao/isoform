package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentOwnerStreamRefreshesOnEvents(t *testing.T) {
	changed := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/owner/chats/job-1/events" {
			if r.Header.Get("Last-Event-ID") != "6" || r.Header.Get("X-Isoform-Hub") != "hub-a" || r.Header.Get("Authorization") != "Bearer test-token" {
				t.Error("invalid stream request")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("id: 7\nevent: state\ndata: {}\n\n"))
			return
		}
		_, _ = w.Write([]byte(`{"state":"working"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json"), emit: func() { changed <- struct{}{} }}
	waitAgentOwnerRefreshes(t, env.cachePath)
	if last := agentOwnerStream(context.Background(), env, agentOwnerIdentity(server.URL, "hub-a"), "job-1", "6"); last != "7" {
		t.Fatalf("last event: %q", last)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("stream event did not refresh snapshots")
	}
}

func TestAgentOwnerWatchStopsWhenJobCloses(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json"), ctx: ctx}
	identity := agentOwnerIdentity(server.URL, "hub-a")
	agentOwnerWatchJobs(env, identity, json.RawMessage(`[{"id":"job-1","state":"working"}]`))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not start")
	}
	agentOwnerWatchJobs(env, identity, json.RawMessage(`[{"id":"job-1","state":"closed"}]`))
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("closed job stream did not stop")
	}
}

func TestAgentOwnerBlockedSnapshotRoute(t *testing.T) {
	path, err := agentOwnerPath("blocked", "")
	if err != nil || path != "/v1/owner/clients/blocked" {
		t.Fatalf("blocked snapshot route: %q, %v", path, err)
	}
}

func TestAgentOwnerPayoutNeedCadence(t *testing.T) {
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json")}
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	jobs, payouts, status, balance := json.RawMessage(`[]`), json.RawMessage(`{"status":"not_started"}`), json.RawMessage(`{"state":"on"}`), json.RawMessage(`{"balanceCents":1234}`)
	needs := agentOwnerNeedsWithPayout(env, jobs, payouts, status, balance, now)
	if len(needs) != 1 || needs[0].AmountCents == nil || *needs[0].AmountCents != 1234 {
		t.Fatalf("initial need: %+v", needs)
	}
	if err := agentOwnerMarkPayoutNeedShown(env, now); err != nil {
		t.Fatal(err)
	}
	if needs := agentOwnerNeedsWithPayout(env, jobs, payouts, status, balance, now.Add(29*24*time.Hour)); len(needs) != 0 {
		t.Fatalf("shown too soon: %+v", needs)
	}
	if needs := agentOwnerNeedsWithPayout(env, jobs, payouts, status, balance, now.Add(30*24*time.Hour)); len(needs) != 1 {
		t.Fatalf("not shown after 30 days: %+v", needs)
	}
}

func TestAgentOwnerDraftNeedUsesLocalSignal(t *testing.T) {
	if got := agentOwnerAddDraftNeed(nil, ""); len(got) != 0 {
		t.Fatalf("empty draft: %+v", got)
	}
	got := agentOwnerAddDraftNeed([]AgentOwnerNeed{{Kind: "review", ID: "job-1"}}, "draft-1")
	if len(got) != 2 || got[1].Kind != "draft" || got[1].ID != "draft-1" {
		t.Fatalf("draft need: %+v", got)
	}
}

func TestAgentOwnerVersionedDecisionsAndPhoneConflict(t *testing.T) {
	var legacy atomic.Bool
	var noPass atomic.Bool
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if noPass.Load() {
				_, _ = w.Write([]byte(`{"passes":[]}`))
				return
			}
			if legacy.Load() {
				_, _ = w.Write([]byte(`{"review":{"passId":"pass-1"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"passes":[{"id":"pass-1","decisionVersion":2}]}`))
			return
		}
		requests.Add(1)
		var body struct {
			PassID          string `json:"passId"`
			DecisionVersion *int64 `json:"decisionVersion"`
			Note            string `json:"note"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.PassID != "pass-1" || body.DecisionVersion == nil || *body.DecisionVersion != 2 || body.Note != "adjust" {
			t.Error("incorrect decision payload")
		}
		if strings.HasSuffix(r.URL.Path, "/redo") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"stale_decision","current":{"passId":"pass-2","state":"review","decisionVersion":3}}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"working"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json")}
	waitAgentOwnerRefreshes(t, env.cachePath)
	version := int64(2)
	for _, action := range []string{"accept", "decline", "send", "redo"} {
		result, err := agentOwnerDecision(env, AgentOwnerDecisionInput{Action: action, JobID: "job-1", PassID: "pass-1", DecisionVersion: &version, Note: "adjust"})
		if err != nil {
			t.Fatal(err)
		}
		if action == "redo" {
			if result.Accepted || result.Error != "stale_decision" || result.Current == nil || result.Current.PassID != "pass-2" || result.Current.DecisionVersion != 3 {
				t.Fatalf("stale result: %+v", result)
			}
		} else if !result.Accepted {
			t.Fatalf("decision rejected: %+v", result)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/agent-owner/decision", strings.NewReader(`{"action":"redo","jobId":"job-1","passId":"pass-1","decisionVersion":2,"note":"adjust"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	serveAgentOwnerWriteEnv(w, req, env, true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"passId":"pass-2"`) {
		t.Fatalf("phone conflict: %d %s", w.Code, w.Body.String())
	}
	legacy.Store(true)
	before := requests.Load()
	if _, err := agentOwnerDecision(env, AgentOwnerDecisionInput{Action: "send", JobID: "job-1", PassID: "pass-1", DecisionVersion: &version}); err == nil || requests.Load() != before {
		t.Fatal("legacy server accepted unsafe decision")
	}
	if _, err := agentOwnerDecision(env, AgentOwnerDecisionInput{Action: "accept", JobID: "job-1", PassID: "pass-1"}); err == nil {
		t.Fatal("accepted missing version")
	}
	legacy.Store(false)
	noPass.Store(true)
	result, err := agentOwnerDecision(env, AgentOwnerDecisionInput{Action: "accept", JobID: "job-1", PassID: "pass-1", DecisionVersion: &version})
	if err != nil || result.Accepted || result.Error != "stale_decision" || result.Current != nil {
		t.Fatalf("missing pass: %+v %v", result, err)
	}
}

func TestAgentOwnerPhoneWritesRequireScopes(t *testing.T) {
	bridge := &mobileBridge{}
	for _, scopes := range [][]string{nil, {mobileScopeBriefRead}, {mobileScopeChatWrite}} {
		w := httptest.NewRecorder()
		bridge.handleAuthorized(w, httptest.NewRequest(http.MethodPost, "/mobile/v1/agent-owner/decision", nil), mobileTestDevice(scopes...))
		if w.Code != http.StatusForbidden {
			t.Fatalf("write without both scopes: %d", w.Code)
		}
	}
}

func TestAgentOwnerPhoneSettingsAndAvailability(t *testing.T) {
	seen := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			seen <- r.Method + " " + r.URL.Path
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/owner" {
			_, _ = w.Write([]byte(`{"paymentAgreement":{"termsVersion":"v1","acceptedAt":"2026-11-09"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"ok"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json")}
	waitAgentOwnerRefreshes(t, env.cachePath)
	for _, tc := range []struct{ body, path string }{
		{`{"action":"pause"}`, "POST /v1/owner/pause"},
		{`{"action":"on"}`, "POST /v1/owner/on"},
		{`{"action":"settings","settings":{"rateCentsPerMinute":100,"speed":"fast","effort":"low","approveJobs":true,"reviewResults":false}}`, "PUT /v1/owner/settings"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/agent-owner/action", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		serveAgentOwnerWriteEnv(w, r, env, false)
		if w.Code != http.StatusOK {
			t.Fatalf("phone action: %d %s", w.Code, w.Body.String())
		}
		if got := <-seen; got != tc.path {
			t.Fatalf("phone route: %s", got)
		}
	}
}

func TestAgentOwnerPaymentAgreementAndOn(t *testing.T) {
	var agreed atomic.Bool
	var turnedOn atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/owner/payment-agreement":
			var body struct {
				AcceptTerms  bool   `json:"acceptTerms"`
				TermsVersion string `json:"termsVersion"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || !body.AcceptTerms || body.TermsVersion != "v1" {
				t.Error("incorrect payment agreement")
			}
			agreed.Store(true)
			_, _ = w.Write([]byte(`{"termsVersion":"v1","acceptedAt":"2026-11-09"}`))
		case "/v1/owner":
			if agreed.Load() {
				_, _ = w.Write([]byte(`{"paymentAgreement":{"termsVersion":"v1","acceptedAt":"2026-11-09"}}`))
			} else {
				_, _ = w.Write([]byte(`{"state":"setting_up"}`))
			}
		case "/v1/owner/on":
			turnedOn.Add(1)
			_, _ = w.Write([]byte(`{"state":"on"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json")}
	waitAgentOwnerRefreshes(t, env.cachePath)
	if _, err = agentOwnerAction(env, "on", "", "", nil); err == nil || turnedOn.Load() != 0 {
		t.Fatal("turned on without a recorded agreement")
	}
	if _, err = agentOwnerAction(env, "agree", "", "v1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = agentOwnerAction(env, "on", "", "", nil); err != nil || turnedOn.Load() != 1 {
		t.Fatalf("turn on after agreement: %v", err)
	}
}

func waitAgentOwnerRefreshes(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		deadline := time.Now().Add(6 * time.Second)
		for {
			agentOwnerCacheMu.Lock()
			running := false
			for key := range agentOwnerRefreshing {
				if strings.HasPrefix(key, path+"\x00") {
					running = true
				}
			}
			agentOwnerCacheMu.Unlock()
			if !running {
				return
			}
			if time.Now().After(deadline) {
				t.Error("owner refresh did not finish")
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func TestAgentOwnerCachedFirstAndIdentity(t *testing.T) {
	requests := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"state":"on"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 2)
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json"), emit: func() { changed <- struct{}{} }}
	waitAgentOwnerRefreshes(t, env.cachePath)
	view, err := agentOwnerGet(env, "status", "")
	if err != nil || view.Available {
		t.Fatalf("initial cache: %+v %v", view, err)
	}
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	close(release)
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not finish")
	}
	view, err = agentOwnerGet(env, "status", "")
	if err != nil || !view.Available || string(view.Data) != `{"state":"on"}` {
		t.Fatalf("cached response: %+v %v", view, err)
	}
	info, err := os.Stat(env.cachePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("cache permissions")
	}
	agentOwnerCacheMu.Lock()
	other := agentOwnerReadCache(env.cachePath, agentOwnerIdentity(server.URL, "hub-b"))
	agentOwnerCacheMu.Unlock()
	if len(other.Entries) != 0 {
		t.Fatal("cache crossed identities")
	}
}

func TestAgentOwnerUploadApprovedProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var input struct {
				Lines           []string `json:"lines"`
				LearnedThrough  string   `json:"learnedThrough"`
				ExpectedVersion int      `json:"expectedVersion"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Lines) != 1 || input.ExpectedVersion != 3 || input.LearnedThrough != "2026-11-09" {
				t.Error("invalid profile payload")
			}
		}
		_, _ = w.Write([]byte(`{"version":4}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json")}
	waitAgentOwnerRefreshes(t, env.cachePath)
	_, err = agentOwnerUploadProfile(env, []string{"line"}, []string{"specialty"}, "2026-11-09", 3)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentOwnerActionUsesDocumentedRoute(t *testing.T) {
	called := make(chan string, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			called <- r.URL.Path
			var body struct {
				Note string `json:"note"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Note != "adjust" {
				t.Error("missing review note")
			}
		}
		_, _ = w.Write([]byte(`{"state":"working"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json")}
	waitAgentOwnerRefreshes(t, env.cachePath)
	if _, err := agentOwnerAction(env, "redo", "job-1", "adjust", nil); err == nil {
		t.Fatal("accepted an unversioned decision")
	}
	select {
	case path := <-called:
		t.Fatalf("unversioned action reached server: %s", path)
	default:
	}
	if _, err := agentOwnerAction(env, "redo", "../other", "adjust", nil); err == nil {
		t.Fatal("accepted unsafe job id")
	}
	if _, err := agentOwnerAction(env, "anything", "", "", nil); err == nil {
		t.Fatal("accepted unknown action")
	}
	if _, err := agentOwnerAction(env, "on", "", "", nil); err == nil {
		t.Fatal("enabled before an agreement contract exists")
	}
}

func TestAgentOwnerPayoutOnboarding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct {
				AcceptTerms  bool   `json:"acceptTerms"`
				TermsVersion string `json:"termsVersion"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.AcceptTerms || body.TermsVersion != "v1" {
				t.Error("missing terms acceptance")
			}
			_, _ = w.Write([]byte(`{"url":"https://connect.example.test/setup"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json")}
	waitAgentOwnerRefreshes(t, env.cachePath)
	link, err := agentOwnerPayoutOnboarding(env, "v1")
	if err != nil || link != "https://connect.example.test/setup" {
		t.Fatalf("onboarding link: %v", err)
	}
	if _, err := agentOwnerPayoutOnboarding(env, ""); err == nil {
		t.Fatal("accepted empty terms")
	}
}

func TestAgentOwnerActionDiscardsOlderRefresh(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	changed := make(chan struct{}, 8)
	var statusCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/owner" {
			if statusCalls.Add(1) == 1 {
				close(started)
				<-release
				_, _ = w.Write([]byte(`{"state":"on"}`))
			} else {
				_, _ = w.Write([]byte(`{"state":"paused"}`))
			}
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json"), emit: func() { changed <- struct{}{} }}
	waitAgentOwnerRefreshes(t, env.cachePath)
	if _, err := agentOwnerGet(env, "status", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("old refresh did not start")
	}
	if _, err := agentOwnerAction(env, "pause", "", "", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		agentOwnerCacheMu.Lock()
		cache := agentOwnerReadCache(env.cachePath, agentOwnerIdentity(server.URL, "hub-a"))
		value := string(cache.Entries["/v1/owner"])
		agentOwnerCacheMu.Unlock()
		if value == `{"state":"paused"}` {
			break
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("new refresh did not complete")
		}
	}
	releaseOnce.Do(func() { close(release) })
	// The earlier request is allowed to finish before inspecting the cache.
	deadline = time.After(5 * time.Second)
	for {
		agentOwnerCacheMu.Lock()
		oldRunning := false
		for key := range agentOwnerRefreshing {
			if strings.Contains(key, env.cachePath) && strings.HasSuffix(key, "\x000") {
				oldRunning = true
			}
		}
		cache := agentOwnerReadCache(env.cachePath, agentOwnerIdentity(server.URL, "hub-a"))
		agentOwnerCacheMu.Unlock()
		if !oldRunning {
			if string(cache.Entries["/v1/owner"]) != `{"state":"paused"}` {
				t.Fatal("old refresh replaced newer state")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("old refresh did not finish")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestAgentOwnerNeedsUsesCachedStates(t *testing.T) {
	needs := agentOwnerNeeds(json.RawMessage(`[{"id":"a","state":"waiting_owner"},{"id":"b","state":"review"},{"id":"c","state":"working"}]`), json.RawMessage(`{"status":"not_started"}`), json.RawMessage(`{"state":"on"}`))
	if len(needs) != 3 || needs[0].Kind != "approval" || needs[0].ID != "a" || needs[1].Kind != "review" || needs[1].ID != "b" || needs[2].Kind != "payouts" {
		t.Fatalf("unexpected needs: %+v", needs)
	}
	if len(agentOwnerNeeds(nil, nil, nil)) != 0 {
		t.Fatal("invented uncached needs")
	}
}

func TestAgentOwnerReviewFileDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1/owner/chats/job/review/files/folder%2Freport.bin" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Isoform-Hub") != "hub-a" {
			t.Errorf("unexpected review file request: %s", r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte{0, 1, 2, 255})
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client}
	data, err := agentOwnerReviewFile(env, "job", "folder/report.bin")
	if err != nil || string(data) != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("file bytes = %v, error = %v", data, err)
	}
	for _, name := range []string{"", "../file", "/file", "folder/../file", "folder\\file"} {
		if _, err := agentOwnerReviewFilePath("job", name); err == nil {
			t.Errorf("accepted unsafe name %q", name)
		}
	}
}

func TestAgentOwnerPhoneReadScopeAndCache(t *testing.T) {
	bridge := &mobileBridge{}
	for _, route := range []string{"status", "jobs", "job", "balance"} {
		recorder := httptest.NewRecorder()
		bridge.handleAuthorized(recorder, httptest.NewRequest(http.MethodGet, mobileAPIPrefix+"/agent-owner/"+route, nil), mobileDeviceRecord{})
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s without scope: %d", route, recorder.Code)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"paused"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "state.json")}
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: "hub-a", Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "cache.json")}
	waitAgentOwnerRefreshes(t, env.cachePath)
	cache := agentOwnerCache{Identity: agentOwnerIdentity(server.URL, "hub-a"), Entries: map[string]json.RawMessage{"/v1/owner": json.RawMessage(`{"state":"on"}`)}}
	if err := agentOwnerWriteCache(env.cachePath, cache); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	serveAgentOwnerReadEnv(recorder, env, "status", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"state":"on"`) || strings.Contains(recorder.Body.String(), "test-token") {
		t.Fatalf("unexpected cached response: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	serveAgentOwnerReadEnv(recorder, env, "job", "../bad")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid job: %d", recorder.Code)
	}
}
