package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// The owner screens on the computer run against the real Agent of You server
// in development mode: simulated payments, deterministic models, a local
// sandbox, log-only mail, no accounts and no keys. Opt in with
// ISOFORM_SERVER_E2E=1; server/scripts/e2e-local.sh sets it.
func TestAgentOwnerAgainstLocalServer(t *testing.T) {
	if os.Getenv("ISOFORM_SERVER_E2E") != "1" {
		t.Skip("set ISOFORM_SERVER_E2E=1 to run the owner screens against a local server")
	}
	server := startLocalAgentServer(t)
	dir := t.TempDir()
	reservation := yourAgentEnv{stateFile: filepath.Join(dir, "your-agent.json")}
	const hub = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	const token = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA"
	if err := writeYourAgentStateLocked(reservation.stateFile, &yourAgentStateFile{Hub: hub, Token: token}); err != nil {
		t.Fatal(err)
	}
	client, err := newAgentServerClient(server.base)
	if err != nil {
		t.Fatal(err)
	}
	env := agentOwnerEnv{reservation: reservation, client: client, cachePath: filepath.Join(dir, "agent-owner-cache.json")}
	waitAgentOwnerRefreshes(t, env.cachePath)

	// The status screen reads cached first, then fills from the server.
	if _, err = client.do(context.Background(), http.MethodGet, "/v1/owner", nil, hub, token); err != nil {
		t.Fatalf("owner status: %v", err)
	}
	status := waitOwnerSnapshot(t, env, "status", "", func(v map[string]any) bool { return v["name"] != nil })
	agent, _ := status["name"].(string)
	if !strings.HasPrefix(agent, "dev.") {
		t.Fatalf("development owner name = %q", agent)
	}

	if _, err = agentOwnerAction(env, "join", "", "", nil); err != nil {
		t.Fatalf("join: %v", err)
	}
	profile, err := (agentOwnerProfileUploader{env}).UploadApprovedProfile(context.Background(), WorkProfileUpload{Lines: []string{"Writes clear reports"}, PublicSpecialties: []string{"reports"}})
	if err != nil || profile.Version < 1 {
		t.Fatalf("approve profile: %+v %v", profile, err)
	}
	settings := &AgentOwnerSettings{RateCentsPerMinute: 200, Speed: "best", Effort: "high", ApproveJobs: true, ReviewResults: true}
	if _, err = agentOwnerAction(env, "settings", "", "", settings); err != nil {
		t.Fatalf("settings: %v", err)
	}
	if _, err = agentOwnerAction(env, "on", "", "", nil); err == nil {
		t.Fatal("turned on before the payment terms were accepted")
	}
	if _, err = agentOwnerAction(env, "agree", "", "v1", nil); err != nil {
		t.Fatalf("payment terms: %v", err)
	}
	if _, err = agentOwnerAction(env, "on", "", "", nil); err != nil {
		t.Fatalf("turn on: %v", err)
	}
	waitOwnerSnapshot(t, env, "status", "", func(v map[string]any) bool { return v["state"] == "on" })

	// A visitor hires the agent on the website.
	visitor := server.visitor(t, agent, "Make a sample")
	server.must(t, "POST", "/v1/payments/setup", fmt.Sprintf(`{"chatId":%q}`, visitor.chat), visitor.cookie, "", 200)
	started := server.must(t, "POST", "/v1/chats/"+visitor.chat+"/go", `{"limitCents":1000}`, visitor.cookie, "e2e-go", 200)
	pass, _ := started["passId"].(string)

	// The jobs list shows it waiting, and the owner's needs list asks for approval.
	waitOwnerList(t, env, func(jobs []map[string]any) bool {
		for _, job := range jobs {
			if job["id"] == visitor.chat && job["state"] == "waiting_owner" {
				return true
			}
		}
		return false
	})
	jobs, _ := agentOwnerGet(env, "jobs", "")
	payouts, _ := agentOwnerGet(env, "payouts", "")
	statusData, _ := agentOwnerGet(env, "status", "")
	balance, _ := agentOwnerGet(env, "balance", "")
	if !hasAgentOwnerNeed(agentOwnerNeedsWithPayout(env, jobs.Data, payouts.Data, statusData.Data, balance.Data, time.Now()), "approval", visitor.chat) {
		t.Fatal("needs list does not ask for the waiting approval")
	}

	approval := waitOwnerCard(t, env, visitor.chat, "waiting_owner", "approval", -1)
	stale := int64(approval.version + 100)
	refused, err := agentOwnerDecision(env, AgentOwnerDecisionInput{Action: "accept", JobID: visitor.chat, PassID: approval.pass, DecisionVersion: &stale})
	if err != nil || refused.Accepted || refused.Error != "stale_decision" {
		t.Fatalf("stale approval was not refused: %+v %v", refused, err)
	}
	accepted, err := agentOwnerDecision(env, AgentOwnerDecisionInput{Action: "accept", JobID: visitor.chat, PassID: approval.pass, DecisionVersion: &approval.version})
	if err != nil || !accepted.Accepted {
		t.Fatalf("accept: %+v %v", accepted, err)
	}

	// The checked result waits for review; the owner opens its file, then sends it.
	review := waitOwnerCard(t, env, visitor.chat, "review", "review", -1)
	if len(review.files) == 0 {
		t.Fatal("review card names no files")
	}
	data, err := agentOwnerReviewFile(env, visitor.chat, review.files[0])
	if err != nil || len(data) == 0 {
		t.Fatalf("review file: %d bytes, %v", len(data), err)
	}
	sent, err := agentOwnerDecision(env, AgentOwnerDecisionInput{Action: "send", JobID: visitor.chat, PassID: review.pass, DecisionVersion: &review.version})
	if err != nil || !sent.Accepted {
		t.Fatalf("send: %+v %v", sent, err)
	}
	server.waitChat(t, visitor.chat, visitor.cookie, "delivered")
	receipt := server.must(t, "GET", "/v1/chats/"+visitor.chat+"/receipts/"+pass, "", visitor.cookie, "", 200)
	if charged, _ := receipt["chargedCents"].(float64); charged <= 0 {
		t.Fatalf("receipt charged nothing: %v", receipt)
	}
	waitOwnerSnapshot(t, env, "job", visitor.chat, func(v map[string]any) bool { return v["state"] == "delivered" })

	// The owner blocks the visitor from the job screen, then unblocks them.
	detail := waitOwnerSnapshot(t, env, "job", visitor.chat, func(v map[string]any) bool { return v["clientId"] != nil })
	clientID, _ := detail["clientId"].(string)
	if _, err = agentOwnerAction(env, "block", clientID, "", nil); err != nil {
		t.Fatalf("block: %v", err)
	}
	waitOwnerSnapshotRaw(t, env, "blocked", "", func(raw json.RawMessage) bool { return strings.Contains(string(raw), clientID) })
	if _, err = agentOwnerAction(env, "unblock", clientID, "", nil); err != nil {
		t.Fatalf("unblock: %v", err)
	}
	if _, err = agentOwnerAction(env, "pause", "", "", nil); err != nil {
		t.Fatalf("pause: %v", err)
	}
	waitOwnerSnapshot(t, env, "status", "", func(v map[string]any) bool { return v["state"] == "paused" })
}

func hasAgentOwnerNeed(needs []AgentOwnerNeed, kind, id string) bool {
	for _, need := range needs {
		if need.Kind == kind && need.ID == id {
			return true
		}
	}
	return false
}

type ownerCard struct {
	pass    string
	version int64
	files   []string
}

// waitOwnerCard reads the job screen until it shows the named card with a
// version newer than previous.
func waitOwnerCard(t *testing.T, env agentOwnerEnv, job, state, card string, previous int64) ownerCard {
	t.Helper()
	var found ownerCard
	waitOwnerSnapshot(t, env, "job", job, func(v map[string]any) bool {
		if v["state"] != state {
			return false
		}
		c, ok := v[card].(map[string]any)
		if !ok {
			return false
		}
		version, ok := c["decisionVersion"].(float64)
		if !ok || int64(version) <= previous {
			return false
		}
		found = ownerCard{version: int64(version)}
		found.pass, _ = c["passId"].(string)
		if files, ok := c["files"].([]any); ok {
			for _, raw := range files {
				if f, ok := raw.(map[string]any); ok {
					if name, ok := f["path"].(string); ok {
						found.files = append(found.files, name)
					} else if name, ok := f["name"].(string); ok {
						found.files = append(found.files, name)
					}
				}
			}
		}
		return found.pass != ""
	})
	return found
}

func waitOwnerList(t *testing.T, env agentOwnerEnv, ok func([]map[string]any) bool) {
	t.Helper()
	waitOwnerSnapshotRaw(t, env, "jobs", "", func(raw json.RawMessage) bool {
		var jobs []map[string]any
		return json.Unmarshal(raw, &jobs) == nil && ok(jobs)
	})
}

func waitOwnerSnapshot(t *testing.T, env agentOwnerEnv, kind, id string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	var value map[string]any
	waitOwnerSnapshotRaw(t, env, kind, id, func(raw json.RawMessage) bool {
		value = nil
		return json.Unmarshal(raw, &value) == nil && ok(value)
	})
	return value
}

// waitOwnerSnapshotRaw reads a screen the way the app does: the cached value
// now, with a refresh started in the background.
func waitOwnerSnapshotRaw(t *testing.T, env agentOwnerEnv, kind, id string, ok func(json.RawMessage) bool) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := agentOwnerGet(env, kind, id)
		if err != nil {
			t.Fatalf("%s snapshot: %v", kind, err)
		}
		if snapshot.Available && ok(snapshot.Data) {
			return snapshot.Data
		}
		time.Sleep(50 * time.Millisecond)
	}
	snapshot, _ := agentOwnerGet(env, kind, id)
	t.Fatalf("%s %s never showed the expected state; last: %s", kind, id, snapshot.Data)
	return nil
}

type localAgentServer struct {
	base   string
	http   *http.Client
	log    *lockedLog
	origin string
}

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) codes() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var codes []string
	for _, m := range regexp.MustCompile(`local dev verification code: ([0-9]{6})`).FindAllStringSubmatch(l.b.String(), -1) {
		codes = append(codes, m[1])
	}
	return codes
}

// startLocalAgentServer builds ../server and runs it in development mode on a
// free loopback port with its own data folder.
func startLocalAgentServer(t *testing.T) *localAgentServer {
	t.Helper()
	serverDir, err := filepath.Abs(filepath.Join("..", "server"))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "isoform-server")
	build := exec.Command("go", "build", "-o", binary, "./cmd/isoform-server")
	build.Dir = serverDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	s := &localAgentServer{base: "http://" + addr, log: &lockedLog{}, origin: "http://localhost:8788",
		http: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}}
	cmd := exec.Command(binary, "serve")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(),
		"ISOFORM_DEV_MODE=1", "ISOFORM_SERVER_ADDR=" + addr, "ISOFORM_SERVER_PUBLIC_URL=" + s.base,
		"ISOFORM_SERVER_DATA=" + t.TempDir(), "ISOFORM_WEB_ORIGINS=" + s.origin,
	}
	cmd.Stdout, cmd.Stderr = s.log, s.log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.http.CloseIdleConnections()
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("local server did not stop")
		}
		if t.Failed() {
			s.log.mu.Lock()
			t.Logf("server log:\n%s", s.log.b.String())
			s.log.mu.Unlock()
		}
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := s.http.Get(s.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("local server did not start")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (s *localAgentServer) request(t *testing.T, method, path, body string, cookie *http.Cookie, key string) (int, map[string]any, *http.Cookie) {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", s.origin)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if len(data) > 0 && json.Unmarshal(data, &result) != nil {
		t.Fatalf("%s %s: invalid response %s", method, path, data)
	}
	var next *http.Cookie
	if cookies := resp.Cookies(); len(cookies) > 0 {
		next = cookies[0]
	}
	return resp.StatusCode, result, next
}

func (s *localAgentServer) must(t *testing.T, method, path, body string, cookie *http.Cookie, key string, want int) map[string]any {
	t.Helper()
	status, result, _ := s.request(t, method, path, body, cookie, key)
	if status != want {
		t.Fatalf("%s %s: status %d, want %d, response %v", method, path, status, want, result)
	}
	return result
}

type localVisitor struct {
	chat   string
	cookie *http.Cookie
}

// visitor writes a request on the agent's page, verifies the emailed code from
// the server log, and waits for the quote.
func (s *localAgentServer) visitor(t *testing.T, agent, text string) localVisitor {
	t.Helper()
	status, created, anonymous := s.request(t, "POST", "/v1/web/chats", fmt.Sprintf(`{"agent":%q}`, agent), nil, "")
	if status != 201 {
		t.Fatalf("create chat: %d %v", status, created)
	}
	chat, _ := created["id"].(string)
	s.must(t, "POST", "/v1/web/chats/"+chat+"/draft", fmt.Sprintf(`{"text":%q}`, text), anonymous, "", 200)
	previous := len(s.log.codes())
	s.must(t, "POST", "/v1/web/identity/code", fmt.Sprintf(`{"chatId":%q,"name":"Client","email":"client@example.test"}`, chat), anonymous, "", 200)
	var code string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && code == ""; time.Sleep(10 * time.Millisecond) {
		if codes := s.log.codes(); len(codes) > previous {
			code = codes[len(codes)-1]
		}
	}
	if code == "" {
		t.Fatal("verification code missing from the server log")
	}
	status, result, cookie := s.request(t, "POST", "/v1/web/identity/verify", fmt.Sprintf(`{"chatId":%q,"code":%q}`, chat, code), anonymous, "")
	if status != 200 || cookie == nil {
		t.Fatalf("verify: %d %v", status, result)
	}
	v := localVisitor{chat: chat, cookie: cookie}
	s.waitChat(t, chat, cookie, "quote_ready")
	return v
}

func (s *localAgentServer) waitChat(t *testing.T, chat string, cookie *http.Cookie, state string) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		status, result, _ := s.request(t, "GET", "/v1/chats/"+chat, "", cookie, "")
		if status == 200 && result["state"] == state {
			return result
		}
	}
	t.Fatalf("chat did not reach %s", state)
	return nil
}
