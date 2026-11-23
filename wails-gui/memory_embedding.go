package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ── EmbeddingProvider interface ─────────────────────────────────────────
// Matches openclaw's embedding provider abstraction.

// EmbeddingProvider generates vector embeddings for text.
type EmbeddingProvider interface {
	// EmbedBatch embeds multiple texts in a single API call.
	EmbedBatch(ctx context.Context, texts []string) ([][]float64, error)
	// EmbedQuery embeds a single query text (convenience wrapper).
	EmbedQuery(ctx context.Context, text string) ([]float64, error)
	// Provider returns a human-readable provider name (e.g. "local-onnx", "openai", "ollama").
	Provider() string
	// Model returns the embedding model name (e.g. "text-embedding-3-small").
	Model() string
	// ProviderKey returns a stable hash identifying this provider configuration.
	ProviderKey() string
}

// ── OpenAI-compatible embedding provider ────────────────────────────────

const (
	embeddingDefaultBaseURL = "https://api.openai.com/v1"
	embeddingDefaultModel   = "text-embedding-3-small"

	// Ollama defaults
	ollamaDefaultBaseURL = "http://localhost:11434/v1"
	ollamaDefaultModel   = "nomic-embed-text"
	ollamaDetectTimeout  = 2 * time.Second

	// Retry parameters (matching openclaw)
	embeddingMaxRetries    = 3
	embeddingBaseDelay     = 500 * time.Millisecond
	embeddingMaxDelay      = 8 * time.Second
	embeddingQueryTimeout  = 60 * time.Second
	embeddingBatchTimeout  = 2 * time.Minute
	embeddingBatchMaxTexts = 100

	// Embedding cache
	embeddingCacheMaxEntries = 10000
	embeddingCacheBatchSize  = 400
)

type openaiProvider struct {
	store    *MemoryStore // nil for a transient Settings connection test
	apiKey   string
	baseURL  string
	model    string
	client   *http.Client
	provKey  string
	provName string
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponseData struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type embeddingResponse struct {
	Data  []embeddingResponseData `json:"data"`
	Model string                  `json:"model"`
	Error *embeddingErrorDetail   `json:"error"`
}

type embeddingErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

//isoform:destination embeddings_api,local
func newOpenAIProvider(provName, apiKey, baseURL, model string) *openaiProvider {
	if baseURL == "" {
		baseURL = embeddingDefaultBaseURL
	}
	if model == "" {
		model = embeddingDefaultModel
	}
	baseURL = strings.TrimRight(baseURL, "/")

	provKey := computeProviderKey("openai", baseURL, model)

	return &openaiProvider{
		apiKey:   apiKey,
		baseURL:  baseURL,
		model:    model,
		client:   &http.Client{Timeout: embeddingBatchTimeout, CheckRedirect: stopFindingRedirect},
		provKey:  provKey,
		provName: provName,
	}
}

func (p *openaiProvider) Provider() string    { return p.provName }
func (p *openaiProvider) Model() string       { return p.model }
func (p *openaiProvider) ProviderKey() string { return p.provKey }

func (p *openaiProvider) EmbedQuery(ctx context.Context, text string) ([]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, embeddingQueryTimeout)
	defer cancel()

	results, err := p.embedBatchRaw(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	return results[0], nil
}

func (p *openaiProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Split into sub-batches if needed
	var allResults [][]float64
	for lo := 0; lo < len(texts); lo += embeddingBatchMaxTexts {
		hi := min(lo+embeddingBatchMaxTexts, len(texts))
		batch := texts[lo:hi]

		results, err := p.embedBatchWithRetry(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("embed batch [%d:%d]: %w", lo, hi, err)
		}
		allResults = append(allResults, results...)
	}

	return allResults, nil
}

func (p *openaiProvider) embedBatchWithRetry(ctx context.Context, texts []string) ([][]float64, error) {
	var lastErr error
	for attempt := range embeddingMaxRetries {
		results, err := p.embedBatchRaw(ctx, texts)
		if err == nil {
			return results, nil
		}
		lastErr = err
		if !isRetryableError(err) {
			return nil, err
		}

		delay := time.Duration(float64(embeddingBaseDelay) * math.Pow(2, float64(attempt))) //nolint:gosec
		if delay > embeddingMaxDelay {
			delay = embeddingMaxDelay
		}
		// Add jitter (0-20%) matching openclaw
		jitter := time.Duration(float64(delay) * 0.2 * rand.Float64())
		delay += jitter

		log.Printf("[memory] embedding retry %d/%d after %v: %v", attempt+1, embeddingMaxRetries, delay, err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

//isoform:destination embeddings_api,local
func (p *openaiProvider) embedBatchRaw(ctx context.Context, texts []string) ([][]float64, error) {
	reqBody := embeddingRequest{
		Model: p.model,
		Input: texts,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := p.baseURL + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	if p.store != nil {
		findingSendMu.RLock()
		defer findingSendMu.RUnlock()
		if err := p.authorizeFindingCall(ctx); err != nil {
			return nil, err
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &embeddingAPIError{
			StatusCode: resp.StatusCode,
		}
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if embResp.Error != nil {
		return nil, fmt.Errorf("embedding service reported an error")
	}

	if len(embResp.Data) != len(texts) {
		return nil, fmt.Errorf("expected %d embeddings, got %d", len(texts), len(embResp.Data))
	}

	// Sort by index to ensure correct ordering
	sort.Slice(embResp.Data, func(i, j int) bool {
		return embResp.Data[i].Index < embResp.Data[j].Index
	})

	results := make([][]float64, len(embResp.Data))
	for i, d := range embResp.Data {
		results[i] = d.Embedding
	}
	return results, nil
}

// ── Error handling ──────────────────────────────────────────────────────

type embeddingAPIError struct {
	StatusCode int
}

func (e *embeddingAPIError) Error() string {
	return fmt.Sprintf("embedding API error (status %d)", e.StatusCode)
}

// isRetryableError returns true for transient errors (rate-limit, 5xx).
// Matches openclaw's retry logic.
func isRetryableError(err error) bool {
	if apiErr, ok := err.(*embeddingAPIError); ok {
		return apiErr.StatusCode == 429 || apiErr.StatusCode >= 500
	}
	return false
}

// ── Provider resolution ─────────────────────────────────────────────────

// A missing OpenAI credential uses the same on-device search as the default.
func resolveEmbeddingProvider(configDir string, cfg MemoryStoreConfig, onProgress downloadProgressFn) EmbeddingProvider {
	provider := cfg.effectiveFindingProvider()
	baseURL := cfg.BaseURL
	model := cfg.Model

	if (provider == "ollama" || provider == "openai") && baseURL != "" {
		if _, err := findingURL(baseURL); err != nil {
			return nil
		}
	}
	switch provider {
	case "openai":
		return newOpenAIProvider("openai", strings.TrimSpace(cfg.APIKey), baseURL, model)
	case "ollama":
		ollamaURL := baseURL
		if ollamaURL == "" {
			ollamaURL = ollamaDefaultBaseURL
		}
		ollamaModel := model
		if ollamaModel == "" {
			ollamaModel = ollamaDefaultModel
		}
		if !detectOllama(ollamaURL) {
			log.Printf("[memory] ollama provider requested but not reachable at %s", ollamaURL)
			return nil
		}
		if !ensureOllamaModel(ollamaURL, ollamaModel) {
			return nil
		}
		return newOpenAIProvider("ollama", "ollama", ollamaURL, ollamaModel)
	case "local":
		p, err := newLocalONNXProvider(configDir, onProgress)
		if err != nil {
			log.Printf("[memory] local ONNX provider failed: %v", err)
			return nil
		}
		return p
	case "none":
		return nil
	default:
		log.Printf("[memory] unknown embedding provider: %s, falling back to FTS-only", provider)
		return nil
	}
}

// ── Ollama auto-detection ───────────────────────────────────────────────

// detectOllama checks if Ollama is running at the given base URL.
// The base URL should be the OpenAI-compatible endpoint (e.g. http://localhost:11434/v1).
//
//isoform:destination embeddings_api,local
func detectOllama(baseURL string) bool {
	// Ollama's native API is at the root, not /v1
	rootURL := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")

	client := &http.Client{Timeout: ollamaDetectTimeout, CheckRedirect: stopFindingRedirect}
	resp, err := client.Get(rootURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type ollamaTagsResponse struct {
	Models []ollamaModel `json:"models"`
}

type ollamaModel struct {
	Name string `json:"name"`
}

// ollamaHasModel checks if a specific model is already pulled in Ollama.
//
//isoform:destination embeddings_api,local
func ollamaHasModel(baseURL, model string) bool {
	rootURL := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")

	client := &http.Client{Timeout: ollamaDetectTimeout, CheckRedirect: stopFindingRedirect}
	resp, err := client.Get(rootURL + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return false
	}

	// Ollama model names can include ":latest" suffix
	for _, m := range tags.Models {
		name := m.Name
		if name == model || name == model+":latest" || strings.TrimSuffix(name, ":latest") == model {
			return true
		}
	}
	return false
}

// ensureOllamaModel checks availability without pulling anything. Ollama owns
// its model installation; missing weights keep memory in its FTS-only state.
func ensureOllamaModel(baseURL, model string) bool {
	if !ollamaHasModel(baseURL, model) {
		log.Printf("[memory] Ollama model %q is not ready; install it in Ollama", model)
		return false
	}
	return true
}

// computeProviderKey creates a stable hash for provider config change detection.
// Matches openclaw's computeProviderKey.
func computeProviderKey(provider, baseURL, model string) string {
	data := fmt.Sprintf("%s|%s|%s", provider, baseURL, model)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(data)))[:16]
}

// ── Cosine similarity ───────────────────────────────────────────────────
// Matches openclaw's cosineSimilarity implementation.

func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := min(len(a), len(b))
	var dot, normA, normB float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// ── Embedding cache (SQLite-backed, matching openclaw) ──────────────────

type embeddingCacheEntry struct {
	Hash      string
	Embedding []float64
}

// loadEmbeddingCache loads cached embeddings for the given content hashes.
// Batches queries in groups of 400 (matching openclaw).
func (s *MemoryStore) loadEmbeddingCache(db *sql.DB, hashes []string, contexts ...context.Context) (map[string][]float64, error) {
	ctx := memoryOperationContext(contexts)
	if s.embedder == nil || len(hashes) == 0 {
		return nil, nil
	}

	// Deduplicate
	seen := make(map[string]bool, len(hashes))
	var unique []string
	for _, h := range hashes {
		if !seen[h] {
			seen[h] = true
			unique = append(unique, h)
		}
	}

	result := make(map[string][]float64, len(unique))
	provKey := s.embedder.ProviderKey()
	model := s.embedder.Model()

	for start := 0; start < len(unique); start += embeddingCacheBatchSize {
		end := min(start+embeddingCacheBatchSize, len(unique))
		batch := unique[start:end]

		placeholders := make([]string, len(batch))
		args := make([]any, 0, len(batch)+3)
		args = append(args, "openai", model, provKey)
		for i, h := range batch {
			placeholders[i] = "?"
			args = append(args, h)
		}

		query := fmt.Sprintf(
			`SELECT hash, embedding FROM memory_embedding_cache
			 WHERE provider = ? AND model = ? AND provider_key = ?
			   AND hash IN (%s)`,
			strings.Join(placeholders, ","),
		)

		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("query embedding cache: %w", err)
		}

		for rows.Next() {
			var hash, embJSON string
			if err := rows.Scan(&hash, &embJSON); err != nil {
				rows.Close()
				return nil, err
			}
			var emb []float64
			if err := json.Unmarshal([]byte(embJSON), &emb); err == nil {
				result[hash] = emb
			}
		}
		rows.Close()
	}

	return result, nil
}

// storeEmbeddingCache stores embeddings in the cache.
func (s *MemoryStore) storeEmbeddingCache(db *sql.DB, entries []embeddingCacheEntry, contexts ...context.Context) error {
	ctx := memoryOperationContext(contexts)
	if s.embedder == nil || len(entries) == 0 {
		return nil
	}

	provKey := s.embedder.ProviderKey()
	model := s.embedder.Model()
	now := time.Now().UnixMilli()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO memory_embedding_cache
		(provider, model, provider_key, hash, embedding, dims, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, entry := range entries {
		embJSON, err := json.Marshal(entry.Embedding)
		if err != nil {
			continue
		}
		_, err = stmt.ExecContext(ctx, "openai", model, provKey, entry.Hash,
			string(embJSON), len(entry.Embedding), now)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// pruneEmbeddingCache evicts oldest entries if cache exceeds max size.
// Matches openclaw's LRU cache eviction.
func (s *MemoryStore) pruneEmbeddingCache(db *sql.DB, contexts ...context.Context) {
	ctx := memoryOperationContext(contexts)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_embedding_cache").Scan(&count); err != nil {
		return
	}
	if count <= embeddingCacheMaxEntries {
		return
	}

	excess := count - embeddingCacheMaxEntries
	_, err := db.ExecContext(ctx, `
		DELETE FROM memory_embedding_cache
		WHERE rowid IN (
			SELECT rowid FROM memory_embedding_cache
			ORDER BY updated_at ASC
			LIMIT ?
		)`, excess)
	if err != nil {
		log.Printf("[memory] cache prune failed: %v", err)
	}
}

// embedWithCache embeds texts, using cache where possible.
// Returns embeddings in the same order as input texts.
// uncachedHashes should correspond 1:1 with texts.
func (s *MemoryStore) embedWithCache(ctx context.Context, db *sql.DB, texts []string, hashes []string) ([][]float64, error) {
	if s.embedder == nil {
		return nil, nil
	}

	t0 := time.Now()

	// Load cached
	cacheLoadStart := time.Now()
	cached, err := s.loadEmbeddingCache(db, hashes, ctx)
	if err != nil {
		log.Printf("[memory] cache load failed: %v", err)
		cached = nil
	}
	log.Printf("[memory] embedWithCache: loadCache %d hashes in %v (hits=%d)", len(hashes), time.Since(cacheLoadStart), len(cached))

	// Find uncached indices
	var uncachedIdx []int
	var uncachedTexts []string
	for i, h := range hashes {
		if cached != nil {
			if _, ok := cached[h]; ok {
				continue
			}
		}
		uncachedIdx = append(uncachedIdx, i)
		uncachedTexts = append(uncachedTexts, texts[i])
	}

	// Embed uncached
	var freshEmbeddings [][]float64
	if len(uncachedTexts) > 0 {
		embedStart := time.Now()
		log.Printf("[memory] embedWithCache: starting EmbedBatch for %d uncached texts (waiting for semaphore...)", len(uncachedTexts))
		freshEmbeddings, err = s.embedder.EmbedBatch(ctx, uncachedTexts)
		log.Printf("[memory] embedWithCache: EmbedBatch %d texts in %v (err=%v)", len(uncachedTexts), time.Since(embedStart), err)
		if err != nil {
			return nil, err
		}
	} else {
		log.Printf("[memory] embedWithCache: all %d texts cache-hit, skipping ONNX", len(texts))
	}

	// Store fresh embeddings in cache
	if len(freshEmbeddings) > 0 {
		storeStart := time.Now()
		var cacheEntries []embeddingCacheEntry
		for i, idx := range uncachedIdx {
			if i < len(freshEmbeddings) {
				cacheEntries = append(cacheEntries, embeddingCacheEntry{
					Hash:      hashes[idx],
					Embedding: freshEmbeddings[i],
				})
			}
		}
		if storeErr := s.storeEmbeddingCache(db, cacheEntries, ctx); storeErr != nil {
			log.Printf("[memory] cache store failed: %v", storeErr)
		}
		log.Printf("[memory] embedWithCache: storeCache %d entries in %v", len(cacheEntries), time.Since(storeStart))
	}

	// Assemble results in order
	results := make([][]float64, len(texts))
	freshIdx := 0
	for i, h := range hashes {
		if cached != nil {
			if emb, ok := cached[h]; ok {
				results[i] = emb
				continue
			}
		}
		if freshIdx < len(freshEmbeddings) {
			results[i] = freshEmbeddings[freshIdx]
			freshIdx++
		}
	}

	// Prune cache if needed
	pruneStart := time.Now()
	s.pruneEmbeddingCache(db, ctx)
	log.Printf("[memory] embedWithCache: pruneCache in %v (total=%v)", time.Since(pruneStart), time.Since(t0))

	return results, nil
}

// forgottenMemoryChunkHashes covers indexed vectors and all live/history
// variants, including cache rows left behind by an earlier edit or reindex.
func forgottenMemoryChunkHashes(db *sql.DB, path string, contents []string, additionalPaths ...string) ([]string, error) {
	set := map[string]bool{}
	for _, content := range contents {
		for _, chunk := range chunkMarkdown(content, memoryChunkTokens, memoryChunkOverlap) {
			set[chunk.Hash] = true
		}
	}
	scope, args := forgottenMemoryIndexScope(path, additionalPaths)
	rows, err := db.Query("SELECT hash FROM memory_chunks WHERE "+scope, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		set[hash] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hashes := make([]string, 0, len(set))
	for hash := range set {
		hashes = append(hashes, hash)
	}
	return hashes, nil
}

func forgottenMemoryIndexScope(path string, additionalPaths []string) (string, []any) {
	paths := append([]string{path}, additionalPaths...)
	seen := make(map[string]bool, len(paths))
	var placeholders []string
	var args []any
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		placeholders = append(placeholders, "?")
		args = append(args, path)
	}
	args = append(args, string(MemorySourceMemory))
	return "(path IN (" + strings.Join(placeholders, ",") + ") AND source = ?)", args
}

// Shared cache rows remain useful to records outside the complete deletion
// scope. Both preview and deletion use this scope, counting each cache row once.
func forgottenMemoryCacheScope(indexScope string) string {
	return ` WHERE hash = ? AND NOT EXISTS (
	SELECT 1 FROM memory_chunks
	WHERE memory_chunks.hash = memory_embedding_cache.hash AND NOT ` + indexScope + `
)`
}

func countForgottenMemoryEmbeddings(db *sql.DB, path string, contents []string, additionalPaths ...string) (int, error) {
	hashes, err := forgottenMemoryChunkHashes(db, path, contents, additionalPaths...)
	if err != nil {
		return 0, err
	}
	var count int
	scope, args := forgottenMemoryIndexScope(path, additionalPaths)
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_chunks WHERE "+scope+" AND embedding != ''", args...).Scan(&count); err != nil {
		return 0, err
	}
	for _, hash := range hashes {
		var cached int
		if err := db.QueryRow("SELECT COUNT(*) FROM memory_embedding_cache"+forgottenMemoryCacheScope(scope), append([]any{hash}, args...)...).Scan(&cached); err != nil {
			return 0, err
		}
		count += cached
	}
	return count, nil
}

func removeForgottenMemoryIndex(db *sql.DB, path string, contents []string, additionalPaths ...string) error {
	hashes, err := forgottenMemoryChunkHashes(db, path, contents, additionalPaths...)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteForgottenMemoryIndex(tx, hashes, append([]string{path}, additionalPaths...)); err != nil {
		return err
	}
	return tx.Commit()
}
func deleteForgottenMemoryIndex(tx *sql.Tx, hashes, paths []string) error {
	scope, args := forgottenMemoryIndexScope(paths[0], paths[1:])
	for _, hash := range hashes {
		if _, err := tx.Exec("DELETE FROM memory_embedding_cache"+forgottenMemoryCacheScope(scope), append([]any{hash}, args...)...); err != nil {
			return err
		}
	}
	for _, table := range []string{"memory_chunks_fts", "memory_chunks", "memory_files"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE "+scope, args...); err != nil {
			return err
		}
	}
	return nil
}
