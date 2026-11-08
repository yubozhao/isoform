package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestModelGatewayManifestAndUnconfirmedDownload(t *testing.T) {
	for _, platform := range []string{"windows/amd64", "windows/arm64"} {
		e, err := magpieDownloadEntry(platform)
		if err != nil || e.SizeBytes <= 0 || len(e.SHA256) != 64 || !strings.HasPrefix(e.Source, "https://github.com/yetone/magpie-releases/releases/download/v"+e.Version+"/") {
			t.Fatalf("invalid manifest for %s: %+v, %v", platform, e, err)
		}
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	app := &App{}
	plan := ToolDownloadPlan{Entry: ToolDownloadEntry{ID: "model-gateway", Source: server.URL}, Folder: filepath.Join(t.TempDir(), "gateway")}
	if err := app.StartToolDownload(plan); err == nil || requests.Load() != 0 {
		t.Fatal("unconfirmed gateway download accessed the network")
	}
}

func TestModelGatewayDownloadVerifiesBeforePublish(t *testing.T) {
	payload := []byte("fixture gateway executable")
	hash := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	for _, tc := range []struct {
		name, hash string
		size       int64
		valid      bool
	}{
		{"valid", hex.EncodeToString(hash[:]), int64(len(payload)), true},
		{"wrong hash", strings.Repeat("0", 64), int64(len(payload)), false},
		{"wrong size", hex.EncodeToString(hash[:]), int64(len(payload)) - 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folder := t.TempDir()
			plan := ToolDownloadPlan{Entry: ToolDownloadEntry{Source: server.URL, SizeBytes: tc.size, SHA256: tc.hash}, Folder: folder}
			err := downloadMagpieTool(plan, nil)
			if (err == nil) != tc.valid {
				t.Fatalf("download: %v", err)
			}
			installed, readErr := os.ReadFile(filepath.Join(folder, "magpie-cli.exe"))
			if tc.valid && (readErr != nil || string(installed) != string(payload)) {
				t.Fatalf("not installed: %v", readErr)
			}
			if !tc.valid && !os.IsNotExist(readErr) {
				t.Fatal("invalid executable was published")
			}
			left, _ := filepath.Glob(filepath.Join(folder, ".magpie-download-*"))
			if len(left) != 0 {
				t.Fatal("temporary download left behind")
			}
		})
	}
}
