package main

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"wails-gui/internal/destinations"

	wmcp "wails-gui/internal/mcp"
)

func TestBuildComposioStatusUsesBackendAppsMetadata(t *testing.T) {
	resp := composioStatusResponse{
		Configured: true,
		Apps: []ComposioAppMetadata{
			{App: "googlesuper", DisplayName: "Google Super", Configured: true},
			{App: "slackbot", DisplayName: "Slack", Configured: true},
			{App: "notion", DisplayName: "Notion", Configured: true},
		},
		Connections: []composioBackendConnection{
			{App: "slackbot", Connected: true, Status: "connected"},
			{App: "notion", Connected: true, Status: "connected"},
		},
	}

	status := buildComposioStatus(resp, map[string]bool{"slackbot": true, "notion": true}, nil)

	if len(status.SupportedApps) != 3 {
		t.Fatalf("expected 3 supported apps, got %d", len(status.SupportedApps))
	}
	if status.SupportedApps[1].App != "slackbot" || status.SupportedApps[1].DisplayName != "Slack" || !status.SupportedApps[1].Configured {
		t.Fatalf("expected slack metadata in supported apps, got %+v", status.SupportedApps[1])
	}
	if status.SupportedApps[2].App != "notion" || status.SupportedApps[2].DisplayName != "Notion" || !status.SupportedApps[2].Configured {
		t.Fatalf("expected notion metadata in supported apps, got %+v", status.SupportedApps[2])
	}
	if len(status.Connections) != 3 {
		t.Fatalf("expected 3 connections, got %d", len(status.Connections))
	}
	slack := status.Connections[1]
	if slack.App != "slackbot" || slack.DisplayName != "Slack" || !slack.Configured || !slack.Connected || !slack.MCPActive {
		t.Fatalf("expected connected active slack metadata, got %+v", slack)
	}
	if slack.Status != "connected" {
		t.Fatalf("expected connected status, got %q", slack.Status)
	}
	notion := status.Connections[2]
	if notion.App != "notion" || notion.DisplayName != "Notion" || !notion.Configured || !notion.Connected || !notion.MCPActive {
		t.Fatalf("expected connected active notion metadata, got %+v", notion)
	}
}

func TestManagedComposioAppAccessIsRetired(t *testing.T) {
	app := &App{}
	status := app.GetComposioStatus()
	if status.Configured || status.Authenticated || len(status.Connections) != 0 || len(status.SupportedApps) != 0 {
		t.Fatalf("retired status exposed managed app access: %+v", status)
	}
	if err := app.ConnectComposioApp(composioGoogleSuperApp); err == nil {
		t.Fatal("managed OAuth connect path should be disabled")
	}
	if err := app.ActivateComposioApp(composioGoogleSuperApp); err == nil {
		t.Fatal("managed MCP activation path should be disabled")
	}
	if tools := app.ListComposioTools(); len(tools) != 0 {
		t.Fatalf("retired managed tools = %+v", tools)
	}
}

func TestBuildComposioStatusFallsBackToGoogleSuper(t *testing.T) {
	status := buildComposioStatus(composioStatusResponse{Configured: true}, nil, nil)

	if len(status.SupportedApps) != 1 {
		t.Fatalf("expected fallback supported apps, got %d", len(status.SupportedApps))
	}
	if status.SupportedApps[0].App != "googlesuper" || status.SupportedApps[0].DisplayName != "Google Super" || !status.SupportedApps[0].Configured {
		t.Fatalf("expected googlesuper fallback metadata, got %+v", status.SupportedApps[0])
	}
	if len(status.Connections) != 1 || status.Connections[0].App != "googlesuper" {
		t.Fatalf("expected fallback connections, got %+v", status.Connections)
	}
}

func TestComposioManagedServerIDMapsSupportedApps(t *testing.T) {
	tests := map[string]string{
		composioGoogleSuperApp: composioGoogleSuperServerID,
		composioSlackBotApp:    composioSlackBotServerID,
		composioNotionApp:      composioNotionServerID,
		composioGitHubApp:      composioGitHubServerID,
		composioLinearApp:      composioLinearServerID,
	}
	for app, want := range tests {
		got, err := composioManagedServerID(app)
		if err != nil {
			t.Fatalf("composioManagedServerID(%q): %v", app, err)
		}
		if got != want {
			t.Fatalf("server id for %q = %q, want %q", app, got, want)
		}
	}
	if _, err := composioManagedServerID("slack"); err == nil {
		t.Fatal("expected unsupported app error")
	}
}

func TestComposioAppsNeedingActivationOnlyReturnsConnectedInactiveConfiguredApps(t *testing.T) {
	status := ComposioStatus{
		Configured:    true,
		Authenticated: true,
		Connections: []ComposioConnectionInfo{
			{App: "googlesuper", Configured: true, Connected: true, MCPActive: false},
			{App: "already-active", Configured: true, Connected: true, MCPActive: true},
			{App: "not-connected", Configured: true, Connected: false, MCPActive: false},
			{App: "not-configured", Configured: false, Connected: true, MCPActive: false},
		},
	}

	apps := composioAppsNeedingActivation(status)
	if len(apps) != 1 || apps[0] != "googlesuper" {
		t.Fatalf("apps needing activation = %+v, want [googlesuper]", apps)
	}
}

func TestComposioAppsNeedingActivationSkipsUnauthenticatedOrUnconfiguredStatus(t *testing.T) {
	base := ComposioStatus{
		Configured:    true,
		Authenticated: true,
		Connections: []ComposioConnectionInfo{
			{App: "googlesuper", Configured: true, Connected: true, MCPActive: false},
		},
	}
	if apps := composioAppsNeedingActivation(ComposioStatus{Configured: false, Authenticated: true, Connections: base.Connections}); len(apps) != 0 {
		t.Fatalf("unconfigured status should not activate apps, got %+v", apps)
	}
	if apps := composioAppsNeedingActivation(ComposioStatus{Configured: true, Authenticated: false, Connections: base.Connections}); len(apps) != 0 {
		t.Fatalf("unauthenticated status should not activate apps, got %+v", apps)
	}
}

func TestComposioActiveFromInfoUsesManagedManagerHealth(t *testing.T) {
	infos := []wmcp.ServerInfo{
		{ID: composioSlackBotServerID, Health: wmcp.HealthHealthy},
	}
	if !composioActiveFromInfo(composioSlackBotApp, infos) {
		t.Fatal("expected healthy managed server to mark Composio MCP active")
	}
	infos[0].Health = wmcp.HealthUnhealthy
	if composioActiveFromInfo(composioSlackBotApp, infos) {
		t.Fatal("unhealthy managed server should not be MCP active")
	}
}

func TestComposioManagedSpecBuildsHTTPManagerSpec(t *testing.T) {
	spec, err := composioManagedSpec(composioGoogleSuperApp, "https://example.test/mcp", map[string]string{"Authorization": "Bearer test"})
	if err != nil {
		t.Fatalf("composioManagedSpec: %v", err)
	}
	if spec.ID != composioGoogleSuperServerID || spec.Transport != mcpTransportHTTP || spec.URL != "https://example.test/mcp" {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	if spec.Headers["Authorization"] != "Bearer test" {
		t.Fatalf("expected auth header to be copied, got %+v", spec.Headers)
	}
}

func TestNormalizeComposioToolServerSupportsSlackbot(t *testing.T) {
	for _, input := range []string{composioSlackBotServerID, "composio:" + composioSlackBotApp} {
		got, err := normalizeComposioToolServer(input)
		if err != nil {
			t.Fatalf("normalizeComposioToolServer(%q): %v", input, err)
		}
		if got != composioSlackBotServerID {
			t.Fatalf("normalized server = %q, want %q", got, composioSlackBotServerID)
		}
	}
}

func TestNormalizeComposioToolServerSupportsNotion(t *testing.T) {
	for _, input := range []string{composioNotionServerID, "composio:" + composioNotionApp} {
		got, err := normalizeComposioToolServer(input)
		if err != nil {
			t.Fatalf("normalizeComposioToolServer(%q): %v", input, err)
		}
		if got != composioNotionServerID {
			t.Fatalf("normalized server = %q, want %q", got, composioNotionServerID)
		}
	}
}

func TestNormalizeComposioToolServerSupportsGitHub(t *testing.T) {
	for _, input := range []string{composioGitHubServerID, "composio:" + composioGitHubApp} {
		got, err := normalizeComposioToolServer(input)
		if err != nil {
			t.Fatalf("normalizeComposioToolServer(%q): %v", input, err)
		}
		if got != composioGitHubServerID {
			t.Fatalf("normalized server = %q, want %q", got, composioGitHubServerID)
		}
	}
}

func TestNormalizeComposioToolServerSupportsLinear(t *testing.T) {
	for _, input := range []string{composioLinearServerID, "composio:" + composioLinearApp} {
		got, err := normalizeComposioToolServer(input)
		if err != nil {
			t.Fatalf("normalizeComposioToolServer(%q): %v", input, err)
		}
		if got != composioLinearServerID {
			t.Fatalf("normalized server = %q, want %q", got, composioLinearServerID)
		}
	}
}

func TestComposioToolCallRecordsOnlyDestination(t *testing.T) {
	app := newBindingsTestApp(t)
	if err := app.store.StartDestinationRecorder(); err != nil {
		t.Fatal(err)
	}
	srv := server.NewMCPServer("apps", "1")
	srv.AddTool(mcp.NewTool("lookup"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	})
	httpServer := server.NewTestStreamableHTTPServer(srv)
	defer httpServer.Close()
	spec, err := composioManagedSpec(composioNotionApp, httpServer.URL, map[string]string{"x-api-key": "private"})
	if err != nil {
		t.Fatal(err)
	}
	manager := wmcp.New()
	defer manager.ShutdownAll(context.Background())
	if err := manager.Spawn(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CallTool(t.Context(), spec.ID, "lookup", map[string]any{"query": "private"}); err != nil {
		t.Fatal(err)
	}
	rows, err := app.store.WeeklyDestinationCounts(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	for _, row := range rows {
		if row.Incomplete {
			continue
		}
		if row.ID != destinations.Composio || row.Detail != "" {
			t.Fatalf("unexpected destination or detail: %+v", row)
		}
		calls += row.Calls
	}
	if calls != 1 {
		t.Fatalf("one tool call recorded %d sends", calls)
	}
}
