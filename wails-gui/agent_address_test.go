package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"github.com/mark3labs/mcp-go/mcp"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"wails-gui/internal/destinations"
)

func addressTestApp(t *testing.T) (*App, string) {
	t.Helper()
	app := newAgentJoinTestApp(t)
	bin, args := fakeFrontLink(t)
	t.Setenv(frontLinkBinEnv, bin)
	t.Setenv(frontSuffixEnv, "")
	app.front.dir = t.TempDir()
	cert, _ := selfSignedFor(t, testFrontLabel+".agents.isoform.ai")
	app.front.certFunc = func(string, string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil }
	}
	t.Cleanup(app.shutdownAgentAddress)
	return app, args
}

func TestAgentAddressMigrationAndLocalJoinStayClosed(t *testing.T) {
	a, args := addressTestApp(t)
	ctx := context.Background()
	path, _ := a.connectedAgentsPath()
	if err := writeFrontJSON(path, []map[string]string{{"id": "old", "name": "Claude"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetUIState(ctx, frontEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	a.restoreAgentAddress()
	if _, found, err := a.store.LookupUIState(ctx, frontEnabledKey); err != nil || found {
		t.Fatalf("old switch present=%v, err=%v", found, err)
	}
	if a.SetFrontEnabled(true) == nil {
		t.Fatal("obsolete switch succeeded")
	}
	if _, err := a.StartAgentJoinForApp("muse", true); err == nil {
		t.Fatal("Muse started a join")
	}
	if _, err := a.StartAgentJoinForApp("claude", false); err == nil {
		t.Fatal("first open without acceptance")
	}
	local, err := a.StartAgentJoin()
	if err != nil || !local.Local {
		t.Fatalf("local join: %+v %v", local, err)
	}
	if status, _, _ := agentJoinCall(t, http.MethodPost, local.Link, `{"name":"Claude Code","device":"here","mayOpen":"tasks"}`); status != http.StatusAccepted {
		t.Fatal(status)
	}
	if err := a.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	if got := a.GetAgentAddress(); got.Open || len(got.Apps) != 0 {
		t.Fatalf("local/legacy agents opened address: %+v", got)
	}
	if len(a.GetConnectedAgents()) != 2 {
		t.Fatal("lost a local or legacy agent")
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatal("link process started")
	}
}

func TestAgentAddressLastDisconnectAndPendingJoin(t *testing.T) {
	a, _ := addressTestApp(t)
	for i, name := range []string{"Claude", "Gemini"} {
		start, err := a.StartAgentJoinForApp(strings.ToLower(name), i == 0)
		if err != nil {
			t.Fatal(err)
		}
		base := "https://" + testFrontLabel + ".agents.isoform.ai"
		if start.Local || !strings.HasPrefix(start.Link, base+"/j/") || !strings.HasPrefix(start.MCPAddress, base+"/mcp?join=") {
			t.Fatalf("join: %+v", start)
		}
		svc := a.agentJoinService()
		if _, err := svc.AttemptMCP(svc.MCPKey(), name, name, false); err != nil {
			t.Fatal(err)
		}
		if err := a.AnswerAgentJoin(true); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.GetAgentAddress(); !got.Open || strings.Join(got.Apps, ",") != "Claude,Gemini" {
		t.Fatalf("address: %+v", got)
	}
	a.stopFront()
	a.restoreAgentAddress()
	if !a.GetAgentAddress().Open {
		t.Fatal("address not restored")
	}
	saved, err := a.readConnectedAgents()
	if err != nil || len(saved) != 2 || !saved[0].ViaAddress || !saved[1].ViaAddress {
		t.Fatal("address route missing from saved connections", err)
	}
	if err := a.DisconnectAgent(saved[0].ID); err != nil {
		t.Fatal(err)
	}
	if !a.GetAgentAddress().Open {
		t.Fatal("closed with one left")
	}
	if _, err := a.StartAgentJoinForApp("grok", false); err != nil {
		t.Fatal(err)
	}
	if err := a.DisconnectAgent(saved[1].ID); err != nil {
		t.Fatal(err)
	}
	if !a.GetAgentAddress().Open {
		t.Fatal("closed while join waiting")
	}
	a.CancelAgentJoin()
	if a.GetAgentAddress().Open {
		t.Fatal("still open after cancellation")
	}

	if _, err := a.StartAgentJoinForApp("chatgpt", false); err != nil {
		t.Fatal(err)
	}
	svc := a.agentJoinService()
	if _, err := svc.AttemptMCP(svc.MCPKey(), "ChatGPT", "s", false); err != nil {
		t.Fatal(err)
	}
	if err := a.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	if err := a.DisconnectAgent(svc.Agents()[0].ID); err != nil {
		t.Fatal(err)
	}
	if a.GetAgentAddress().Open {
		t.Fatal("last disconnect kept address")
	}
}

func TestAgentAddressUnusedJoin(t *testing.T) {
	for _, reason := range []string{"denied", "expired"} {
		t.Run(reason, func(t *testing.T) {
			a, _ := addressTestApp(t)
			var clock atomic.Int64
			clock.Store(time.Now().UnixMilli())
			a.agentJoin.now = func() time.Time { return time.UnixMilli(clock.Load()) }
			start, err := a.StartAgentJoinForApp("claude", true)
			if err != nil {
				t.Fatal(err)
			}
			addr := a.front.listener.Addr().String()
			switch reason {
			case "denied":
				svc := a.agentJoinService()
				if _, err := svc.AttemptMCP(svc.MCPKey(), "Claude", "session", false); err != nil {
					t.Fatal(err)
				}
				if err := a.AnswerAgentJoin(false); err != nil {
					t.Fatal(err)
				}
			case "expired":
				clock.Store(start.ExpiresAt)
				a.front.expiry.Reset(time.Millisecond)
			}
			deadline := time.Now().Add(2 * time.Second)
			for a.frontHostname() != "" && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			a.front.lifecycle.Lock()
			defer a.front.lifecycle.Unlock()
			if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
				conn.Close()
				t.Fatal("unused address still listening")
			}
		})
	}
}

func TestAgentAddressCertificateFailureRollsBack(t *testing.T) {
	a, _ := addressTestApp(t)
	a.front.certFunc = func(string, string) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, errors.New("issuance failed") }
	}
	local, err := a.StartAgentJoin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.StartAgentJoinForApp("claude", true); err == nil {
		t.Fatal("failed certificate returned a join")
	}
	if a.GetAgentAddress().Open || a.front.cmd != nil || a.front.listener != nil {
		t.Fatal("front survived issuance failure")
	}
	if status, _, _ := agentJoinCall(t, http.MethodGet, local.Link, ""); status != http.StatusOK {
		t.Fatal("existing local join lost", status)
	}
}

func TestAgentAddressBridge(t *testing.T) {
	a, args := addressTestApp(t)
	t.Setenv(taskLinkBaseURLEnv, "https://bridge.example.test")
	start, err := a.StartAgentJoinForApp("claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.GetAgentAddress(); !got.Open || got.Service != "bridge.example.test" || got.Hostname != got.Service {
		t.Fatal(got)
	}
	u, _ := url.Parse(start.Link)
	target := a.taskLinks.listenAddr
	if status, _, page := agentJoinCall(t, http.MethodGet, "http://"+target+u.Path, ""); status != http.StatusOK || !strings.Contains(page, start.MCPAddress) {
		t.Fatal("bridge origin missing on join page", status)
	}
	a.CancelAgentJoin()
	if a.GetAgentAddress().Open {
		t.Fatal("bridge route still open")
	}
	if conn, err := net.DialTimeout("tcp", target, time.Second); err == nil {
		conn.Close()
		t.Fatal("idle bridge still listening")
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatal("bridge started native front")
	}
}

func TestAgentAddressGenericJoinThroughFront(t *testing.T) {
	a, _ := addressTestApp(t)
	if _, err := a.StartAgentJoinForApp("claude", true); err != nil {
		t.Fatal(err)
	}
	svc := a.agentJoinService()
	if _, err := svc.AttemptMCP(svc.MCPKey(), "Claude", "c", false); err != nil {
		t.Fatal(err)
	}
	if err := a.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	start, err := a.StartAgentJoin()
	if err != nil || !start.Local {
		t.Fatalf("local join: %v %v", start, err)
	}
	u, _ := url.Parse(start.Link)
	req, _ := http.NewRequest(http.MethodPost, "https://"+a.frontHostname()+u.Path, strings.NewReader(`{"name":"Remote","device":"another computer","mayOpen":"tasks"}`))
	// Dial only the fake link's local forwarding target.
	cert, _ := a.front.certFunc("", "")(nil)
	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)
	target := a.front.listener.Addr().String()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}}
	defer transport.CloseIdleConnections()
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal(resp.StatusCode)
	}
	if err := a.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	if got := a.GetAgentAddress(); strings.Join(got.Apps, ",") != "Claude,Remote" {
		t.Fatal(got)
	}
}

func TestAgentAddressSupervisorReportsOutageAndRestores(t *testing.T) {
	a, _ := addressTestApp(t)
	a.front.restartWait = 100 * time.Millisecond
	if _, err := a.StartAgentJoinForApp("claude", true); err != nil {
		t.Fatal(err)
	}
	svc := a.agentJoinService()
	if _, err := svc.AttemptMCP(svc.MCPKey(), "Claude", "s", false); err != nil {
		t.Fatal(err)
	}
	if err := a.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	a.front.mu.Lock()
	proc := a.front.cmd.Process
	a.front.mu.Unlock()
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	for _, open := range []bool{false, true} {
		deadline := time.Now().Add(3 * time.Second)
		for {
			got := a.GetAgentAddress()
			if got.Open == open && len(got.Apps) == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("waiting for open=%v, got %+v", open, got)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestAgentAddressMissingLinkDoesNotStartListener(t *testing.T) {
	a, args := addressTestApp(t)
	t.Setenv(frontLinkBinEnv, args+".missing")
	if _, err := a.StartAgentJoinForApp("claude", true); err == nil {
		t.Fatal("missing link opened")
	}
	if a.front.listener != nil || a.taskLinks.server != nil || a.GetAgentJoin() != nil {
		t.Fatal("startup left a listener or join")
	}
}

func TestAgentAddressConnectedHereStaysClosed(t *testing.T) {
	a, _ := newAgentHereTestApp(t, true)
	bin, args := fakeFrontLink(t)
	t.Setenv(frontLinkBinEnv, bin)
	if _, err := a.ConnectAgentHere("codex"); err != nil {
		t.Fatal(err)
	}
	a.restoreAgentAddress()
	if got := a.GetAgentAddress(); got.Open || len(got.Apps) != 0 {
		t.Fatal(got)
	}
	if _, err := os.Stat(args); !os.IsNotExist(err) {
		t.Fatal("coding agent started public front")
	}
}

// What leaves through the agent address: answered calls from agents in their
// own apps are counted, refusals and agents on this computer aren't; the
// leaves row shows while the address is open, and its Stop disconnects only
// the address agents and closes the address.
func TestAgentAddressLeavesCountAndStop(t *testing.T) {
	calls := observeDestinationCalls(t)
	a, _ := addressTestApp(t)
	if _, ok := destinations.Lookup(destinations.AgentAddress); !ok {
		t.Fatal("agent address isn't a listed destination")
	}
	if out, err := a.yourDataLeaves(); err != nil || out.Address != nil {
		t.Fatalf("closed address has a row: %+v %v", out.Address, err)
	}
	svc := a.agentJoinService()
	if _, err := a.StartAgentJoinForApp("claude", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttemptMCP(svc.MCPKey(), "Claude", "s1", false); err != nil {
		t.Fatal(err)
	}
	if err := a.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.StartAgentJoin(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttemptMCP(svc.MCPKey(), "Codex", "s2", false); err != nil {
		t.Fatal(err)
	}
	if err := a.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	var viaAddress, here string
	for _, ag := range svc.Agents() {
		switch ag.Name {
		case "Claude":
			viaAddress = ag.ID
		case "Codex":
			here = ag.ID
		}
	}
	if viaAddress == "" || here == "" {
		t.Fatalf("agents: %+v", svc.Agents())
	}
	a.recordAddressAnswer(here, mcp.NewToolResultText("ok"))
	a.recordAddressAnswer(viaAddress, mcp.NewToolResultError("refused"))
	a.recordAddressAnswer(viaAddress, mcp.NewToolResultText("ok"))
	expectDestination(t, calls, destinations.AgentAddress, "")
	select {
	case c := <-calls:
		t.Fatalf("counted more than the one answered address call: %+v", c)
	case <-time.After(200 * time.Millisecond):
	}

	out, err := a.yourDataLeaves()
	if err != nil || out.Address == nil || !out.Address.Open || out.Address.Service != "Isoform's front" || strings.Join(out.Address.Apps, ",") != "Claude" {
		t.Fatalf("open address row: %+v %v", out.Address, err)
	}

	// A join waiting for the address is ended too; the Stop closes it.
	if _, err := a.StartAgentJoinForApp("gemini", false); err != nil {
		t.Fatal(err)
	}
	if err := a.CloseAgentAddress(); err != nil {
		t.Fatal(err)
	}
	if a.GetAgentAddress().Open || svc.NeedsAddress() || a.GetAgentJoin() != nil {
		t.Fatalf("address still open after Stop: %+v", a.GetAgentAddress())
	}
	left := svc.Agents()
	if len(left) != 1 || left[0].ID != here {
		t.Fatalf("Stop disconnected an agent on this computer: %+v", left)
	}
}

// Stop leaves a loopback join for an agent on this computer alone.
func TestAgentAddressStopKeepsLocalJoin(t *testing.T) {
	a, _ := addressTestApp(t)
	if _, err := a.StartAgentJoin(); err != nil {
		t.Fatal(err)
	}
	if err := a.CloseAgentAddress(); err != nil {
		t.Fatal(err)
	}
	if a.GetAgentJoin() == nil {
		t.Fatal("Stop ended a local join")
	}
}

// Any other agent that runs somewhere else opens the address (v1.5.0 G11),
// asks for Let's Encrypt's agreement only the first time (G10), and is
// named among the address's apps once it joins.
func TestAgentAddressOtherAgentSomewhereElse(t *testing.T) {
	a, _ := addressTestApp(t)
	if !a.GetAgentAddress().NeedsTerms {
		t.Fatal("terms not asked before the first open")
	}
	if _, err := a.StartAgentJoinForApp("other", false); err == nil {
		t.Fatal("first open without acceptance")
	}
	start, err := a.StartAgentJoinForApp("other", true)
	if err != nil {
		t.Fatal(err)
	}
	if start.Local || !strings.HasPrefix(start.Link, "https://"+testFrontLabel+".agents.isoform.ai/j/") {
		t.Fatalf("join: %+v", start)
	}
	if a.GetAgentAddress().NeedsTerms {
		t.Fatal("terms asked again after acceptance")
	}
	svc := a.agentJoinService()
	if _, err := svc.AttemptMCP(svc.MCPKey(), "Devin", "the cloud", false); err != nil {
		t.Fatal(err)
	}
	if err := a.AnswerAgentJoin(true); err != nil {
		t.Fatal(err)
	}
	if got := a.GetAgentAddress(); !got.Open || strings.Join(got.Apps, ",") != "Devin" {
		t.Fatalf("address: %+v", got)
	}
	if err := a.DisconnectAgent(svc.Agents()[0].ID); err != nil {
		t.Fatal(err)
	}
	if a.GetAgentAddress().Open {
		t.Fatal("last disconnect kept the address")
	}
}

func TestAgentAddressBridgeNeedsNoTerms(t *testing.T) {
	a, _ := addressTestApp(t)
	t.Setenv(taskLinkBaseURLEnv, "https://tasks.example.test")
	if a.GetAgentAddress().NeedsTerms {
		t.Fatal("a configured bridge asked for Let's Encrypt's agreement")
	}
}
