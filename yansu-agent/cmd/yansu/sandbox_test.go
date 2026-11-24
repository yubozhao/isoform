package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestSandboxCommandAllowlist(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--help"}, {"--version"}, {"help", "cron"},
		{"cron", "list"}, {"cron", "run", "job"}, {"activity", "list"}, {"memory", "list"},
		{"skill", "list"}, {"skill", "sources"}, {"skill", "search", "--source", "installed", "calendar"},
		{"mcp", "list"}, {"mcp", "sources"}, {"mcp", "tools", "server"}, {"mcp", "registry", "list"}, {"mcp", "registry", "search", "git"},
	} {
		if !sandboxCommandAllowed(args) {
			t.Errorf("local API/help command refused: %v", args)
		}
	}
	for _, args := range [][]string{
		{"status"}, {"whoami"}, {"clone", "x"}, {"hook", "install"},
		{"hook", "run"}, {"push"}, {"push", "--knowledge-only"}, {"login"},
		{"knowledge", "list"}, {"daemon", "start"}, {"stage", "edit", "x"},
		{"--version", "status"}, {"unknown"},
		{"mcp", "install", "--source", "manual", "--id", "probe", "--command", "/bin/sh"},
		{"mcp", "install", "--source", "manual", "--id", "probe", "--command", "/bin/sh", "--no-auto-start"},
		{"mcp", "install", "--source", "local", "--ref", "cursor:probe"},
		{"mcp", "enable", "probe"}, {"mcp", "restart", "probe"}, {"mcp", "disable", "probe"},
		{"mcp", "stop", "probe"}, {"mcp", "uninstall", "probe"}, {"mcp", "registry", "refresh"},
		{"mcp", "discover"}, {"mcp", "tail", "probe"},
		{"skill", "install", "--source", "local", "--yes", "/tmp/probe"},
		{"skill", "enable", "probe"}, {"skill", "disable", "probe"},
		{"skill", "domains", "probe", "--chat", "true"}, {"skill", "uninstall", "probe"},
	} {
		if sandboxCommandAllowed(args) {
			t.Errorf("general command permitted: %v", args)
		}
	}
}

func TestSandboxMCPListOmitsConfigurationAndErrorBodies(t *testing.T) {
	body, err := sandboxMCPListResponse([]byte(`[{"id":"probe","name":"Probe","health":"healthy","enabled":true,"trusted":false,"tools":[{"name":"fake-private-value"}],"env":{"API_KEY":"fake-private-value"},"args":["fake-private-value"],"url":"fake-private-value","last_error":"fake-private-value"}]`), http.StatusOK)
	if err != nil || strings.Contains(string(body), "fake-private-value") || !strings.Contains(string(body), `"tool_count":1`) {
		t.Fatalf("unsafe MCP projection: %s %v", body, err)
	}
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusBadRequest, http.StatusInternalServerError} {
		body, err = sandboxMCPListResponse([]byte("fake-private-value"), status)
		if err == nil || len(body) != 0 || strings.Contains(err.Error(), "fake-private-value") {
			t.Fatalf("MCP error body exposed: %s %v", body, err)
		}
	}
}
