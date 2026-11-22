package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFindingProviderSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(agentHomeEnv, dir)
	t.Setenv("OPENAI_API_KEY", "environment-only")
	app := &App{}
	view, err := app.GetFindingProvider()
	if err != nil || view.Choice != "local" || view.HasOpenAIKey || view.LocalModelDownloaded || view.LocalModelBytes <= 0 {
		t.Fatalf("first run = %+v, %v", view, err)
	}
	if _, err = app.SetFindingProvider("openai", "", ""); err == nil {
		t.Fatal("accepted OpenAI without a saved or supplied key")
	}
	const key = "test-secret-only"
	for _, choice := range []string{"openai", "local", "openai"} {
		input := ""
		if !view.HasOpenAIKey {
			input = key
		}
		view, err = app.SetFindingProvider(choice, input, "")
		if err != nil || view.Choice != choice || !view.HasOpenAIKey {
			t.Fatalf("set %s: %+v, %v", choice, view, err)
		}
		got, err := app.GetFindingProvider()
		if err != nil || got != view {
			t.Fatalf("get %s: %+v, %v", choice, got, err)
		}
		raw, err := json.Marshal(got)
		if err != nil || strings.Contains(string(raw), key) {
			t.Fatal("unsafe provider view")
		}
		legacy, err := app.GetMemoryConfig()
		if err != nil || legacy.APIKey != "" {
			t.Fatal("legacy binding exposed a key or failed")
		}
		saved, err := loadMemoryStoreConfig(dir)
		if err != nil || saved.APIKey != key {
			t.Fatal("saved key was lost")
		}
	}
	defer app.memoryStore.Close()
	if err := os.WriteFile(filepath.Join(dir, "memory.json"), []byte(`{"provider":"openai",`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetFindingProvider(); err == nil {
		t.Fatal("unreadable choice became a default")
	}
	if _, err := app.SetFindingProvider("local", "", ""); err == nil {
		t.Fatal("overwrote unreadable config")
	}
	if _, err := app.GetYourDataSummary(); err == nil {
		t.Fatal("unreadable choice hid the destination")
	}
}

func TestFindingLeavesDestinations(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(agentHomeEnv, dir)
	app := &App{}
	for _, tc := range []struct{ provider, address, service string }{
		{"", "", ""}, {"local", "", ""}, {"ollama", "", ""}, {"ollama", "http://LOCALHOST:11434", ""},
		{"ollama", "http://127.50.3.2:11434", ""}, {"ollama", "http://[::1]:11434", ""},
		{"openai", "", "openai"}, {"ollama", "http://localhost.example:11434/v1", "ollama"}, {"ollama", "http://192.168.1.10", "ollama"},
	} {
		if err := saveMemoryStoreConfig(dir, MemoryStoreConfig{Provider: tc.provider, BaseURL: tc.address, APIKey: "test-saved"}); err != nil {
			t.Fatal(err)
		}
		got, err := app.GetYourDataSummary()
		if err != nil {
			t.Fatal(err)
		}
		row := got.FindingLeaves
		if tc.service == "" && row != nil || tc.service != "" && (row == nil || row.Service != tc.service || row.Address != tc.address || row.ThisWeek != nil) {
			t.Errorf("%s %s: %+v", tc.provider, tc.address, row)
		}
		if tc.provider == "ollama" {
			view, err := app.GetFindingProvider()
			if err != nil || view.Address == "" || view.OnThisComputer == nil || *view.OnThisComputer != (tc.service == "") {
				t.Errorf("Ollama view %+v, %v", view, err)
			}
		}
	}
}

func TestFindingCallsCountAndRevocation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(agentHomeEnv, dir)
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1]}]}`)
	}))
	defer server.Close()
	cfg := MemoryStoreConfig{Provider: "openai", APIKey: "test-key", BaseURL: server.URL}
	if err := saveMemoryStoreConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := NewMemoryStore(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec("INSERT INTO finding_embedding_calls VALUES ('openai','',?,15)", time.Now().Add(-8*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.embedder.EmbedQuery(ctx, "search"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.embedder.EmbedBatch(ctx, []string{"memory"}); err != nil {
		t.Fatal(err)
	}
	app := &App{memoryStore: store}
	summary, err := app.GetYourDataSummary()
	if err != nil || summary.FindingLeaves == nil || summary.FindingLeaves.ThisWeek == nil || *summary.FindingLeaves.ThisWeek != 2 {
		t.Fatalf("weekly count %+v, %v", summary.FindingLeaves, err)
	}
	if _, err := store.db.Exec("DROP TABLE finding_embedding_calls"); err != nil {
		t.Fatal(err)
	}
	summary, err = app.GetYourDataSummary()
	if err != nil || summary.FindingLeaves == nil || summary.FindingLeaves.ThisWeek != nil {
		t.Fatal("failed count was not omitted")
	}
	cfg.APIKey = "" // The selected service and address remain unchanged.
	if err := saveMemoryStoreConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := store.embedder.EmbedQuery(ctx, "revoked"); err == nil || !strings.Contains(err.Error(), "no longer selected") || sends.Load() != 2 {
		t.Fatal("old provider retained authority")
	}
}

func TestFindingRedirectKeepsTextOnChosenService(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed embedding redirect") }))
	defer other.Close()
	chosen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer chosen.Close()
	p := newOpenAIProvider("ollama", "ollama", chosen.URL, "test")
	if _, err := p.EmbedQuery(context.Background(), "private"); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestFindingRemoteOllamaCountsOnlySelectedAddress(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(agentHomeEnv, dir)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"nomic-embed-text"}]}`)
		case "/v1/embeddings":
			fmt.Fprint(w, `{"data":[{"index":0,"embedding":[1]}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	transport := http.DefaultTransport
	http.DefaultTransport = updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		forwarded := r.Clone(r.Context())
		forwarded.URL.Host = server.Listener.Addr().String()
		return transport.RoundTrip(forwarded)
	})
	defer func() { http.DefaultTransport = transport }()
	app := &App{}
	for i, address := range []string{"http://first.example/v1", "http://second.example/v1"} {
		_, err := app.SetFindingProvider("ollama", "", address)
		if err != nil {
			t.Fatal(err)
		}
		if app.memoryStore.embedder == nil || app.memoryStore.onDeviceEmbedder() != nil {
			t.Fatal("remote provider was unavailable or shared for on-device tasks")
		}
		if i == 0 {
			if _, err = app.memoryStore.embedder.EmbedQuery(context.Background(), "search"); err != nil {
				t.Fatal(err)
			}
		}
		got, err := app.GetYourDataSummary()
		row := got.FindingLeaves
		if err != nil || row == nil || row.Address != address || row.ThisWeek == nil || *row.ThisWeek != 1-i {
			t.Fatalf("remote Ollama summary: %+v, %v", row, err)
		}
	}
	app.memoryStore.Close()
}
