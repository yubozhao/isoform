package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A key in the app's environment never sends memory text off the computer:
// with OPENAI_API_KEY and OPENAI_BASE_URL set and no provider chosen in
// Settings, the embedder is local (ONNX) or off, and the OpenAI
// endpoint is never called (docs/destinations.md, embeddings_api).
func TestEmbeddingsIgnoreOpenAIKeyInEnvironment(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "sk-test-environment-only")
	t.Setenv("OPENAI_BASE_URL", server.URL)

	// Route even a probe of the hard-coded default Ollama port to the
	// test server, so the original automatic fallback would fail this test.
	transport := http.DefaultTransport
	http.DefaultTransport = updateRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		forwarded := r.Clone(r.Context())
		forwarded.URL.Host = server.Listener.Addr().String()
		return transport.RoundTrip(forwarded)
	})
	defer func() { http.DefaultTransport = transport }()
	dir := t.TempDir()
	for _, choice := range []string{"", "automatic"} {
		if err := saveMemoryStoreConfig(dir, MemoryStoreConfig{Provider: choice, BaseURL: server.URL}); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadMemoryStoreConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		provider := resolveEmbeddingProvider(dir, cfg, nil)
		if findingChoice(cfg.Provider) != "local" || provider != nil && provider.Provider() != "local-onnx" {
			t.Fatal("an automatic setting chose a service")
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the environment's OpenAI endpoint was called %d times", n)
	}
}

func TestLegacyOpenAIWithoutSavedKeySearchesOnDevice(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	dir := t.TempDir()
	t.Setenv(agentHomeEnv, dir)
	t.Setenv("OPENAI_API_KEY", "fixture-environment-only")
	t.Setenv("OPENAI_BASE_URL", server.URL)
	if err := saveMemoryStoreConfig(dir, MemoryStoreConfig{Provider: "openai", BaseURL: server.URL}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadMemoryStoreConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewMemoryStore(dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Give keyword ranking both matching and unrelated documents.
	for name, content := range map[string]string{"fixture.md": fixtureHalcyonMemory, "cooking.md": fixtureUnrelatedMemory, "recipes.md": fixtureRecipeMemory, "playlist.md": fixtureYouTubeMemory} {
		if err := os.WriteFile(filepath.Join(dir, "memory", name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := store.Search(context.Background(), "Halcyon", 10)
	if err != nil || !containsPath(hits, "fixture.md") {
		t.Fatalf("on-device search returned %d hits, err %v", len(hits), err)
	}
	app := &App{memoryStore: store}
	view, err := app.GetFindingProvider()
	if err != nil || view.Choice != "openai" || view.HasOpenAIKey {
		t.Fatalf("saved choice %+v, err %v", view, err)
	}
	if cfg.effectiveFindingProvider() != "local" {
		t.Error("keyless search did not resolve to local")
	}
	summary, err := app.GetYourDataSummary()
	if err != nil || summary.FindingLeaves != nil {
		t.Fatalf("unexpected destination %+v, err %v", summary.FindingLeaves, err)
	}
	cfg.APIKey = "fixture-unsaved"
	if err := app.SaveMemoryConfig(cfg); err == nil {
		t.Fatal("legacy binding saved a new OpenAI key")
	}
	if app.TestEmbedding(cfg).Success {
		t.Error("tested OpenAI before saving a key")
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("keyless OpenAI setting sent %d requests", n)
	}
}
