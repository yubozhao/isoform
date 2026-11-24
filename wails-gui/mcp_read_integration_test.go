package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"wails-gui/internal/destinations"
	"wails-gui/internal/platform/activitymon"
	"yansu-agent/mcpread"
)

type mcpReadTestServer struct {
	URL    string
	client *http.Client
	bridge *hostBridge
}

func (s *mcpReadTestServer) Client() *http.Client { return s.client }
func (s *mcpReadTestServer) Close() {
	s.client.CloseIdleConnections()
	s.bridge.removeMCPReadEndpoint()
}

func newMCPReadHTTPTestServer(t *testing.T, app *App) *mcpReadTestServer {
	t.Helper()
	hb, err := newHostBridge(context.Background(), app, 0)
	if err != nil {
		t.Fatal(err)
	}
	app.bridge = hb
	if err := hb.publishMCPReadEndpoint(); err != nil {
		t.Fatal(err)
	}
	s := &mcpReadTestServer{URL: "http://isoform", bridge: hb, client: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", hb.mcpSocketPath)
		},
	}}}
	t.Cleanup(s.Close)
	return s
}

func mcpReadIssueTestToken(t *testing.T, assistant string) string {
	t.Helper()
	mcpReadCredentialMu.Lock()
	defer mcpReadCredentialMu.Unlock()
	token, err := issueMCPReadToken(assistant)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMCPReadAuthenticationAndRevocation(t *testing.T) {
	calls := observeDestinationCalls(t)
	// Exercise the real token/socket boundary with assistant cleanup unavailable.
	// No installed assistant CLI may run in this integration test.
	t.Setenv("PATH", t.TempDir())
	app := newMCPReadTestApp(t)
	s := newMCPReadHTTPTestServer(t, app)
	token := mcpReadIssueTestToken(t, "codex")
	otherToken := mcpReadIssueTestToken(t, "claude-code")
	request := func(clientID, credential string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, s.URL+"/api/mcp/read", strings.NewReader(`{"name":"memory_list","arguments":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(mcpReadClientHeader, clientID)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		resp, err := s.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	for name, credential := range map[string]string{"missing": "", "wrong": "wrong", "other client": otherToken, "host token": app.bridge.token} {
		t.Run(name, func(t *testing.T) {
			if got := request("codex", credential); got != 401 {
				t.Fatalf("status %d", got)
			}
		})
	}
	if got := request("codex", token); got != 200 {
		t.Fatalf("valid status %d", got)
	}
	if got := request("../codex", token); got != 401 {
		t.Fatalf("invalid client status %d", got)
	}
	path, _ := mcpReadTokenPath("codex")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token permissions: %v %v", info, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := request("codex", token); got != 401 {
		t.Fatalf("unsafe permissions status %d", got)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.RevokeMCPReadAssistant("codex"); err == nil || err.Error() != "access revoked; assistant CLI unavailable for MCP cleanup" {
		t.Fatalf("expected reported cleanup failure: %v", err)
	}
	if got := request("codex", token); got != 401 {
		t.Fatalf("revoked status %d", got)
	}
	if got := request("claude-code", otherToken); got != 200 {
		t.Fatalf("revoking Codex affected Claude: %d", got)
	}
	expectDestination(t, calls, destinations.AgentsMCPRead, "codex")
	expectDestination(t, calls, destinations.AgentsMCPRead, "claude-code")
	replacement := mcpReadIssueTestToken(t, "codex")
	if replacement == token {
		t.Fatal("reconnect reused revoked token")
	}
	if got := request("codex", token); got != 401 {
		t.Fatalf("stale token status %d", got)
	}
	app.closedEyes.enabled.Store(true)
	if got := request("codex", replacement); got != 423 {
		t.Fatalf("paused status %d", got)
	}
}

// The child is the actual MCP stdio implementation. The parent hosts the actual
// application route over a private Unix socket with SQLite stores; neither side is a mock.
func TestMCPReadStdioProcess(t *testing.T) {
	if os.Getenv("ISOFORM_MCP_STDIO_TEST") != "1" {
		return
	}
	err := mcpread.Serve(context.Background(), mcpread.Config{DataDir: os.Getenv("ISOFORM_MCP_TEST_DATA"), Client: "codex"}, os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMCPReadRealStdioRoundTrip(t *testing.T) {
	// Exercise the real token/socket boundary with assistant cleanup unavailable.
	// No installed assistant CLI may run in this integration test.
	t.Setenv("PATH", t.TempDir())
	app := newMCPReadTestApp(t)
	s := newMCPReadHTTPTestServer(t, app)
	mcpReadIssueTestToken(t, "codex")
	now := time.Now().Add(-time.Minute).UnixMilli()
	err := app.activity.store.SaveSession(ActivitySessionRow{ID: "stdio-evidence", StartTime: now, EndTime: now + 1000, AppNames: `["TextEdit"]`, TriageStatus: "done", CreatedAt: now, EventCount: 1}, []activitymon.ActivityEvent{{Timestamp: now, AppName: "TextEdit", AppBundleID: "com.apple.TextEdit", WindowTitle: "stdio evidence project"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.NewStdioMCPClient(executable, append(os.Environ(), "ISOFORM_MCP_STDIO_TEST=1", "ISOFORM_MCP_TEST_DATA="+configDirOrFallback()), "-test.run=^TestMCPReadStdioProcess$")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if stderr, ok := client.GetStderr(c); ok {
		go io.Copy(io.Discard, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var init mcp.InitializeRequest
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "Isoform integration test", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	listed, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil || len(listed.Tools) != 11 {
		t.Fatalf("tools/list: %+v, %v", listed, err)
	}
	var call mcp.CallToolRequest
	call.Params.Name = "activity_search"
	call.Params.Arguments = map[string]any{"query": "stdio", "limit": 100}
	result, err := c.CallTool(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if result.IsError || !strings.Contains(string(raw), "stdio evidence project") || !strings.Contains(string(raw), "untrusted") || !strings.Contains(string(raw), "source") {
		t.Fatalf("tools/call: %s", raw)
	}
	// The owner portrait over the same boundary: a draft writes nothing; keep
	// writes once and records the authorized client.
	portraitPath, _ := ownerPortraitPath()
	call.Params.Name, call.Params.Arguments = "portrait_draft", map[string]any{}
	result, err = c.CallTool(ctx, call)
	raw, _ = json.Marshal(result)
	if err != nil || result.IsError || !strings.Contains(string(raw), "works_on") || !strings.Contains(string(raw), "untrusted_observation") {
		t.Fatalf("portrait_draft: %s %v", raw, err)
	}
	if _, err := os.Stat(portraitPath); !os.IsNotExist(err) {
		t.Fatal("portrait_draft wrote the portrait")
	}
	call.Params.Name, call.Params.Arguments = "portrait_keep", map[string]any{"sections": []any{map[string]any{"id": "works_on", "lines": []any{map[string]any{"text": "Builds desktop apps", "source": "person"}}}}}
	result, err = c.CallTool(ctx, call)
	raw, _ = json.Marshal(result)
	if err != nil || result.IsError || !strings.Contains(string(raw), "Nothing was uploaded") {
		t.Fatalf("portrait_keep: %s %v", raw, err)
	}
	if kept, err := ownerPortraitRead(portraitPath); err != nil || kept.Version != 1 || kept.KeptFrom != "codex" {
		t.Fatalf("kept portrait: %+v %v", kept, err)
	}
	for _, invalid := range []struct {
		args    map[string]any
		message string
	}{
		{map[string]any{"from": "Oct 16", "to": "2026-10-16"}, "invalid from date"},
		{map[string]any{"from": "2026-10-01", "to": "2026-12-31"}, "at most 31 days"},
	} {
		call.Params.Name, call.Params.Arguments = "calendar_range", invalid.args
		result, err = c.CallTool(ctx, call)
		raw, _ = json.Marshal(result)
		if err != nil || !result.IsError || !strings.Contains(string(raw), invalid.message) {
			t.Fatalf("validation message: %s %v", raw, err)
		}
	}
	app.closedEyes.enabled.Store(true)
	result, err = c.CallTool(ctx, call)
	raw, _ = json.Marshal(result)
	if err != nil || !result.IsError || !strings.Contains(string(raw), "paused:") || !strings.Contains(string(raw), "Resume observation") {
		t.Fatalf("pause reason: %s %v", raw, err)
	}
	app.closedEyes.enabled.Store(false)
	call.Params.Name, call.Params.Arguments = "activity_search", map[string]any{"query": "stdio"}
	if _, err := app.RevokeMCPReadAssistant("codex"); err == nil || err.Error() != "access revoked; assistant CLI unavailable for MCP cleanup" {
		t.Fatalf("expected reported cleanup failure: %v", err)
	}
	result, err = c.CallTool(ctx, call)
	if err != nil || !result.IsError {
		t.Fatalf("stdio revoke: %+v, %v", result, err)
	}
	mcpReadIssueTestToken(t, "codex")
	s.Close()
	result, err = c.CallTool(ctx, call)
	raw, _ = json.Marshal(result)
	if err != nil || !result.IsError || !strings.Contains(string(raw), "not running") {
		t.Fatalf("closed app: %s, %v", raw, err)
	}
}

func TestMCPReadSettingsAreReadOnlyUntilConnect(t *testing.T) {
	app := newMCPReadTestApp(t)
	rows, err := app.GetMCPReadConnections()
	if err != nil || len(rows) != 2 {
		t.Fatalf("connections: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.Authorized || row.Connected || !strings.Contains(row.Command, "mcp") {
			t.Fatalf("initial connection: %+v", row)
		}
		path, _ := mcpReadTokenPath(row.Client)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("view issued token: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(configDirOrFallback(), "mcp-read")); !os.IsNotExist(err) {
		t.Fatalf("view changed disk: %v", err)
	}
	if _, err := app.ConnectMCPReadAssistant("unknown"); err == nil {
		t.Fatal("unknown assistant accepted")
	}
}
