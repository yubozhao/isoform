package mcpread

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yansu-agent/isoformskill"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestServeStdioDiscoveryAndUnavailableApp(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"isoform-test","version":"1"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"activity_search","arguments":{"query":"project"}}}
`)
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Serve(ctx, Config{DataDir: t.TempDir(), Client: "codex"}, input, &output); err != nil {
		t.Fatal(err)
	}
	responses := map[int]json.RawMessage{}
	decoder := json.NewDecoder(&output)
	for {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		err := decoder.Decode(&response)
		if err == io.EOF {
			break
		}
		if err != nil || len(response.Error) != 0 {
			t.Fatalf("invalid JSON-RPC response: %+v, %v", response, err)
		}
		responses[response.ID] = response.Result
	}
	if !bytes.Contains(responses[1], []byte(`"name":"Isoform"`)) {
		t.Fatalf("initialize did not identify Isoform: %s", responses[1])
	}
	var initialized struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(responses[1], &initialized); err != nil {
		t.Fatal(err)
	}
	if strings.Count(initialized.Instructions, isoformskill.Content) != 1 ||
		!strings.Contains(initialized.Instructions, "## Read context through MCP") ||
		!strings.Contains(initialized.Instructions, "## The CLI") ||
		!strings.Contains(initialized.Instructions, isoformskill.ResultContract) {
		t.Fatal("user-connected MCP must supply the full manual, including its result contract")
	}
	var listed struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Annotations map[string]any `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(responses[2], &listed); err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"activity_search", "memory_search", "memory_list", "people_lookup", "calendar_today", "calendar_range", "brief_today", "playbook_status", "run_history", "portrait_draft", "portrait_keep"}
	if len(listed.Tools) != len(wantNames) {
		t.Fatalf("listed %d tools, want %d", len(listed.Tools), len(wantNames))
	}
	found := map[string]bool{}
	for _, tool := range listed.Tools {
		found[tool.Name] = true
		if tool.Name == "portrait_keep" {
			// The one write: never marked read-only, never destructive.
			if tool.Annotations["readOnlyHint"] != false || tool.Annotations["destructiveHint"] != false || !strings.Contains(tool.Description, "explicitly says keep") {
				t.Errorf("portrait_keep must be an explicit, non-destructive write: %+v", tool)
			}
			continue
		}
		if !strings.Contains(tool.Description, "untrusted data") || tool.Annotations["readOnlyHint"] != true || tool.Annotations["destructiveHint"] != false {
			t.Errorf("missing read-only trust boundary for %s: %+v", tool.Name, tool)
		}
	}
	for _, name := range wantNames {
		if !found[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	if !bytes.Contains(responses[3], []byte(`"isError":true`)) || !bytes.Contains(responses[3], []byte("Isoform is not running")) {
		t.Fatalf("unavailable app must return a clear tool error: %s", responses[3])
	}
}

func TestServeRejectsImplicitDataAndUnknownClients(t *testing.T) {
	for _, cfg := range []Config{{Client: "codex"}, {DataDir: "relative", Client: "codex"}, {DataDir: t.TempDir(), Client: "other"}} {
		var output bytes.Buffer
		if err := Serve(context.Background(), cfg, strings.NewReader(""), &output); err == nil {
			t.Fatalf("accepted unsafe config: %+v", cfg)
		}
		if output.Len() != 0 {
			t.Fatal("configuration errors must not contaminate MCP stdout")
		}
	}
}

func writeCredentialFiles(t *testing.T, root, endpoint string) string {
	t.Helper()
	dir := filepath.Join(root, "mcp-read")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"socket": endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "endpoint.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if endpoint == "read-test.sock" {
		listener, err := net.Listen("unix", filepath.Join(dir, endpoint))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		if err := os.Chmod(filepath.Join(dir, endpoint), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "codex.token")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCredentialsRequirePrivateClientTokenAndObserveRevocation(t *testing.T) {
	root := shortMCPReadHome(t)
	path := writeCredentialFiles(t, root, "read-test.sock")
	bridge := newBridge(Config{DataDir: root, Client: "codex"})
	defer bridge.client.CloseIdleConnections()
	endpoint, token, err := bridge.credentials()
	if err != nil || endpoint != filepath.Join(root, "mcp-read", "read-test.sock") || token != strings.Repeat("a", 64) {
		t.Fatalf("valid credential read: endpoint=%q, err=%v", endpoint, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	_, token, err = bridge.credentials()
	if err != nil || token != strings.Repeat("b", 64) {
		t.Fatalf("credential was cached after rotation: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bridge.credentials(); err == nil {
		t.Fatal("read token with unsafe permissions")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bridge.credentials(); err == nil {
		t.Fatal("revoked token was retained")
	}
	// Another assistant's token cannot substitute for the requested identity.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "claude-code.token"), []byte(strings.Repeat("c", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bridge.credentials(); err == nil {
		t.Fatal("missing token borrowed another assistant's access")
	}
}

func TestCredentialsRejectRemoteOriginsAndSymlinks(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1:43123", "http://example.com:43123", "http://localhost:43123", "http://127.0.0.1:43123/api", "http://127.0.0.1:43123?x=y", "http://user:pass@127.0.0.1:43123", "http://127.0.0.1"} {
		t.Run(endpoint, func(t *testing.T) {
			root := shortMCPReadHome(t)
			writeCredentialFiles(t, root, endpoint)
			bridge := newBridge(Config{DataDir: root, Client: "codex"})
			defer bridge.client.CloseIdleConnections()
			if _, _, err := bridge.credentials(); err == nil {
				t.Fatal("accepted unsafe endpoint")
			}
		})
	}
	root := shortMCPReadHome(t)
	path := writeCredentialFiles(t, root, "read-test.sock")
	if err := os.Rename(path, path+".target"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".target", path); err != nil {
		t.Fatal(err)
	}
	bridge := newBridge(Config{DataDir: root, Client: "codex"})
	defer bridge.client.CloseIdleConnections()
	if _, _, err := bridge.credentials(); err == nil {
		t.Fatal("accepted a symlink token")
	}
}

// TestMCPReadCLIHelper is a subprocess entry point for the actual assistant
// clients. It serves the production transport without opening any app store.
func TestMCPReadCLIHelper(t *testing.T) {
	if os.Getenv("ISOFORM_MCP_TEST_HELPER") != "1" {
		return
	}
	args := os.Args[len(os.Args)-2:]
	err := Serve(context.Background(), Config{DataDir: args[0], Client: args[1]}, os.Stdin, os.Stdout)
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestAssistantCLIListsMCPInTemporaryConfig(t *testing.T) {
	// Each subtest starts the real assistant CLI, so it runs only when asked:
	// YANSU_CLAUDE_INTEGRATION=1 for Claude Code, YANSU_CODEX_INTEGRATION=1
	// for Codex.
	for _, assistant := range []struct{ binary, client, optIn string }{
		{"claude", "claude-code", "YANSU_CLAUDE_INTEGRATION"},
		{"codex", "codex", "YANSU_CODEX_INTEGRATION"},
	} {
		t.Run(assistant.binary, func(t *testing.T) {
			if os.Getenv(assistant.optIn) != "1" {
				t.Skipf("set %s=1 to run against the real %s CLI", assistant.optIn, assistant.binary)
			}
			cli, err := exec.LookPath(assistant.binary)
			if err != nil {
				t.Skipf("%s not on PATH", assistant.binary)
			}
			// Short paths avoid assistant Unix-socket path length limits. Keep
			// HOME, working directory, configs, and inherited env fully isolated.
			home, err := os.MkdirTemp("", "mcp-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			for _, dir := range []string{"codex", "claude", "xdg", "tmp", "app"} {
				if err := os.Mkdir(filepath.Join(home, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			env := []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + home,
				"CODEX_HOME=" + filepath.Join(home, "codex"),
				"CLAUDE_CONFIG_DIR=" + filepath.Join(home, "claude"),
				"XDG_CONFIG_HOME=" + filepath.Join(home, "xdg"),
				"TMPDIR=" + filepath.Join(home, "tmp"), "LC_ALL=C",
				"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
				"ISOFORM_MCP_TEST_HELPER=1",
			}
			run := func(args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, cli, args...)
				cmd.Env, cmd.Dir = env, home
				cmd.WaitDelay = time.Second
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s %v failed: %v\n%s", assistant.binary, args, err, output)
				}
				return string(output)
			}
			args := []string{"mcp", "add"}
			if assistant.binary == "claude" {
				args = append(args, "--transport", "stdio", "--scope", "user")
			}
			args = append(args, "isoform-test", "--", executable, "-test.run=^TestMCPReadCLIHelper$", "--", filepath.Join(home, "app"), assistant.client)
			run(args...)
			listed := run("mcp", "list")
			if !strings.Contains(listed, "isoform-test") {
				t.Fatalf("temporary registration missing from list: %s", listed)
			}
			if assistant.binary == "claude" && !strings.Contains(listed, "Connected") {
				t.Fatalf("Claude could not initialize real stdio transport: %s", listed)
			}
		})
	}
}

func shortMCPReadHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "mcp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	return home
}

func TestStaleTCPDiscoveryNeverReceivesReadToken(t *testing.T) {
	root := shortMCPReadHome(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	writeCredentialFiles(t, root, "invalid")
	data, _ := json.Marshal(map[string]string{"url": "http://" + listener.Addr().String()})
	if err := os.WriteFile(filepath.Join(root, "mcp-read", "endpoint.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	b := newBridge(Config{DataDir: root, Client: "codex"})
	defer b.client.CloseIdleConnections()
	var call mcp.CallToolRequest
	call.Params.Name = "memory_list"
	result, err := b.call(context.Background(), call)
	if err != nil || !result.IsError {
		t.Fatalf("stale discovery accepted: %+v %v", result, err)
	}
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	connection, err := listener.Accept()
	if err == nil {
		connection.Close()
		t.Fatal("read client connected to stale TCP endpoint")
	}
}

func TestPortraitKeepSchemaBoundsTheCorrectedPortrait(t *testing.T) {
	var keep mcp.Tool
	for _, tool := range portraitTools() {
		if tool.Name == "portrait_keep" {
			keep = tool
		}
	}
	data, err := json.Marshal(keep.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	schema := string(data)
	for _, want := range []string{`"required":["sections"]`, `"maxItems":8`, `"maxLength":200`, `"claude_memory"`, `"wont_do"`, `"additionalProperties":false`} {
		if !strings.Contains(schema, want) {
			t.Errorf("portrait_keep schema lacks %s: %s", want, schema)
		}
	}
}
