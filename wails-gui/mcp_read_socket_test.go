package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPReadPrivateSocketLifecycleAndTCPSeparation(t *testing.T) {
	a := newMCPReadTestApp(t)
	dir, err := ensureMCPReadDirectory()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := filepath.Join(dir, "endpoint.json")
	if err := os.WriteFile(endpoint, []byte(`{"url":"http://127.0.0.1:43123"}`), 0600); err != nil {
		t.Fatal(err)
	}
	hb, err := startHostBridge(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	a.bridge = hb
	t.Cleanup(hb.Stop)
	token := mcpReadIssueTestToken(t, "codex")
	data, err := os.ReadFile(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	var discovery map[string]string
	if json.Unmarshal(data, &discovery) != nil || discovery["socket"] == "" || discovery["url"] != "" {
		t.Fatalf("TCP discovery survived startup: %s", data)
	}
	info, err := os.Lstat(hb.mcpSocketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe socket: %v %v", info, err)
	}
	req, err := http.NewRequest(http.MethodPost, hb.URL()+"/api/mcp/read", strings.NewReader(`{"name":"memory_list"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(mcpReadClientHeader, "codex")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("MCP exposed over TCP: %d", response.StatusCode)
	}
	firstSocket := hb.mcpSocketPath
	hb.Stop()
	for _, path := range []string{endpoint, firstSocket} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("shutdown left endpoint: %s %v", path, err)
		}
	}
	restarted, err := startHostBridge(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Stop)
	if restarted.mcpSocketPath == firstSocket {
		t.Fatal("restart reused old socket name")
	}
	hb.Stop()
	if _, err := os.Stat(endpoint); err != nil {
		t.Fatalf("old shutdown removed new discovery: %v", err)
	}
}

// Launches that ended without a clean shutdown leave read sockets behind; the
// next launch removes the dead ones and keeps one something still answers on.
func TestMCPReadPublishRemovesStaleSockets(t *testing.T) {
	a := newMCPReadTestApp(t)
	dir, err := ensureMCPReadDirectory()
	if err != nil {
		t.Fatal(err)
	}
	var dead []string
	for _, name := range []string{"read-deadaaaaaaaa.sock", "read-deadbbbbbbbb.sock"} {
		path := filepath.Join(dir, name)
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		listener.(*net.UnixListener).SetUnlinkOnClose(false)
		listener.Close()
		dead = append(dead, path)
	}
	live := filepath.Join(dir, "read-livecccccccc.sock")
	liveListener, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { liveListener.Close() })
	plain := filepath.Join(dir, "read-notasocket.sock")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	hb, err := startHostBridge(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	a.bridge = hb
	t.Cleanup(hb.Stop)
	for _, path := range dead {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("stale socket survived launch: %s %v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{live, plain, hb.mcpSocketPath} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("launch removed %s: %v", filepath.Base(path), err)
		}
	}
}
