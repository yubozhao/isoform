package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ── Source types ────────────────────────────────────────────────────────

type MemorySource string

const (
	MemorySourceMemory    MemorySource = "memory"
	MemorySourceSessions  MemorySource = "sessions"
	MemorySourceKnowledge MemorySource = "knowledge"
)

// ── Configuration ──────────────────────────────────────────────────────
// Matches openclaw's memory search config subset relevant to desktop.

type MemoryStoreConfig struct {
	Provider string `json:"provider"` // "openai" | "none" | "" (auto-detect)
	APIKey   string `json:"apiKey"`
	BaseURL  string `json:"baseUrl"` // e.g. "https://api.openai.com/v1"
	Model    string `json:"model"`   // e.g. "text-embedding-3-small"
}

// ── Constants (matching openclaw defaults) ──────────────────────────────

const (
	// Sync intervals
	memorySyncIntervalMinutes = 5
	memoryWatchPollInterval   = 30 * time.Second

	// Chunking parameters (matching openclaw's chunkMarkdown)
	memoryChunkTokens  = 400
	memoryChunkOverlap = 80
)

// ── Result types ────────────────────────────────────────────────────────

type MemorySearchResult struct {
	Path      string  `json:"path"`
	Source    string  `json:"source"`
	StartLine int     `json:"startLine"`
	EndLine   int     `json:"endLine"`
	Score     float64 `json:"score"`
	Snippet   string  `json:"snippet"`
}

type MemoryStoreStatus struct {
	Files        int    `json:"files"`
	Chunks       int    `json:"chunks"`
	Dirty        bool   `json:"dirty"`
	SyncReason   string `json:"syncReason"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	VectorReady  bool   `json:"vectorReady"`
	CachedEmbeds int    `json:"cachedEmbeds"`
}

// ── Internal types ──────────────────────────────────────────────────────

type memoryFileRecord struct {
	Path   string
	Source MemorySource
	Hash   string
	Mtime  int64
	Size   int64
}

type memoryChunk struct {
	StartLine int
	EndLine   int
	Text      string
	Hash      string
}

// ── MemoryStore ─────────────────────────────────────────────────────────
// Replicates openclaw's MemoryIndexManager with:
//   - File watching (polling-based, like chokidar)
//   - Periodic sync (cron-like interval timer)
//   - Dirty tracking with on-search sync
//   - Embedding pipeline (OpenAI-compatible) with cache
//   - Hybrid search (vector + FTS5 with weighted merge)
//   - MMR diversity reranking
//   - Temporal decay for recency-based scoring
//   - Diary/journal support (memory/YYYY-MM-DD.md files)
//   - Full reindex detection on provider/model/config changes

type MemoryStore struct {
	mu           sync.Mutex
	db           *sql.DB
	baseDir      string // memory directory (e.g. ~/.yansu-agent/memory)
	knowledgeDir string // knowledge directory (e.g. ~/.yansu-agent/knowledge)
	cfgDir       string // parent config directory

	// Configuration
	cfg MemoryStoreConfig

	// Embedding provider (nil = FTS-only mode)
	embedder EmbeddingProvider

	// Lifecycle
	closed bool
	cancel context.CancelFunc

	// Dirty tracking (like openclaw's dirty flag)
	dirty      bool
	lastReason string

	// syncMu guards Sync() so only one sync runs at a time. Callers that
	// lose the TryLock race skip their sync — the running one already
	// covers the dirty window.
	syncMu sync.Mutex

	// File state cache for change detection (polling watcher)
	fileHashes map[string]string // path → hash

	// Event callback
	onUpdated func()
}

// NewMemoryStore opens (or creates) the memory index database.
func NewMemoryStore(configDir string, cfg MemoryStoreConfig, onDownloadProgress ...downloadProgressFn) (*MemoryStore, error) {
	memDir := filepath.Join(configDir, "memory")
	if err := os.MkdirAll(memDir, 0700); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}
	knowledgeDir := filepath.Join(configDir, "knowledge")

	dbPath := filepath.Join(configDir, "memory.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open memory db: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	// Initialize all connections with per-connection PRAGMAs.
	// WAL is file-level (persists), busy_timeout is per-connection.
	for i := 0; i < 4; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("init connection %d: %w", i, err)
		}
		if _, err := conn.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
			conn.Close()
			db.Close()
			return nil, fmt.Errorf("enable WAL: %w", err)
		}
		if _, err := conn.ExecContext(context.Background(), "PRAGMA busy_timeout=5000"); err != nil {
			log.Printf("[memory] failed to set busy_timeout on conn %d: %v", i, err)
		}
		conn.Close()
	}

	// Resolve embedding provider
	var progressCb downloadProgressFn
	if len(onDownloadProgress) > 0 {
		progressCb = onDownloadProgress[0]
	}
	embedder := resolveEmbeddingProvider(configDir, cfg, progressCb)
	if embedder != nil {
		log.Printf("[memory] embedding provider: %s, model: %s", cfg.Provider, embedder.Model())
	} else {
		log.Printf("[memory] no embedding provider configured, using FTS-only mode")
	}

	s := &MemoryStore{
		db:           db,
		baseDir:      memDir,
		knowledgeDir: knowledgeDir,
		cfgDir:       configDir,
		cfg:          cfg,
		embedder:     embedder,
		dirty:        true, // Always dirty on first start (like openclaw)
		lastReason:   "startup",
		fileHashes:   make(map[string]string),
	}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate memory db: %w", err)
	}

	return s, nil
}

func (s *MemoryStore) migrate() error {
	// Create metadata table first (needed for schema versioning)
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS memory_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`); err != nil {
		return err
	}

	// Check schema version for FTS table migration
	var version string
	_ = s.db.QueryRow("SELECT value FROM memory_meta WHERE key = 'schema_version'").Scan(&version)
	needsReindex := version != "3"

	// Create/migrate main tables
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS memory_files (
			path   TEXT PRIMARY KEY,
			source TEXT NOT NULL DEFAULT 'memory',
			hash   TEXT NOT NULL,
			mtime  INTEGER NOT NULL,
			size   INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS memory_chunks (
			id         TEXT PRIMARY KEY,
			path       TEXT NOT NULL,
			source     TEXT NOT NULL DEFAULT 'memory',
			start_line INTEGER NOT NULL,
			end_line   INTEGER NOT NULL,
			hash       TEXT NOT NULL,
			model      TEXT NOT NULL DEFAULT '',
			text       TEXT NOT NULL,
			embedding  TEXT NOT NULL DEFAULT ''
		);

		CREATE INDEX IF NOT EXISTS idx_memory_chunks_path ON memory_chunks(path);

		CREATE TABLE IF NOT EXISTS memory_embedding_cache (
			provider     TEXT NOT NULL,
			model        TEXT NOT NULL,
			provider_key TEXT NOT NULL,
			hash         TEXT NOT NULL,
			embedding    TEXT NOT NULL,
			dims         INTEGER NOT NULL DEFAULT 0,
			updated_at   INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (provider, model, provider_key, hash)
		);

		CREATE INDEX IF NOT EXISTS idx_memory_embedding_cache_updated
			ON memory_embedding_cache(updated_at);
	`); err != nil {
		return err
	}

	// Ensure new columns exist on older databases (backward compatibility)
	ensureColumn(s.db, "memory_files", "source", "TEXT NOT NULL DEFAULT 'memory'")
	ensureColumn(s.db, "memory_chunks", "source", "TEXT NOT NULL DEFAULT 'memory'")
	ensureColumn(s.db, "memory_chunks", "model", "TEXT NOT NULL DEFAULT ''")
	ensureColumn(s.db, "memory_chunks", "embedding", "TEXT NOT NULL DEFAULT ''")

	// Create index on source (must come after ensureColumn for older DBs)
	s.db.Exec("CREATE INDEX IF NOT EXISTS idx_memory_chunks_source ON memory_chunks(source)")

	if needsReindex {
		// Drop old FTS table (may have different schema) and recreate
		s.db.Exec("DROP TABLE IF EXISTS memory_chunks_fts")
		// Clear indexed data so sync will re-process everything
		s.db.Exec("DELETE FROM memory_chunks")
		s.db.Exec("DELETE FROM memory_files")
	}

	// Create FTS5 virtual table with full schema
	if _, err := s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS memory_chunks_fts USING fts5(
			text,
			id UNINDEXED,
			path UNINDEXED,
			source UNINDEXED,
			model UNINDEXED,
			start_line UNINDEXED,
			end_line UNINDEXED,
			tokenize='unicode61 remove_diacritics 2'
		);
	`); err != nil {
		return fmt.Errorf("create FTS table: %w", err)
	}

	if needsReindex {
		s.storeMeta(s.db, "schema_version", "3")
	}

	return nil
}

func ensureColumn(db *sql.DB, table, column, colDef string) {
	_, _ = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colDef))
}

// ── Lifecycle ───────────────────────────────────────────────────────────
// Matches openclaw's MemoryIndexManager lifecycle:
//   - Initial sync on startup
//   - File watching via polling (like chokidar)
//   - Periodic sync at configurable intervals (cron-like)

func (s *MemoryStore) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	// Initial sync
	if err := s.Sync("startup"); err != nil {
		log.Printf("[memory] initial sync failed: %v", err)
	}

	// File watch ticker (polling-based, like chokidar with pollInterval)
	watchTicker := time.NewTicker(memoryWatchPollInterval)
	defer watchTicker.Stop()

	// Interval sync ticker (cron-like periodic sync)
	intervalTicker := time.NewTicker(time.Duration(memorySyncIntervalMinutes) * time.Minute)
	defer intervalTicker.Stop()
	firstWatch := true

	for {
		select {
		case <-ctx.Done():
			return
		case <-watchTicker.C:
			s.pollFileChanges(firstWatch)
			firstWatch = false
		case <-intervalTicker.C:
			if err := s.Sync("interval"); err != nil {
				log.Printf("[memory] interval sync failed: %v", err)
			}
		}
	}
}

func (s *MemoryStore) pollFileChanges(firstWatch bool) {
	changed, removed := s.detectFileChanges()
	if len(removed) > 0 {
		// Only deleted files withdraw sources. Edits are not privacy changes.
		memorySourcesRemovedOnDisk(removed)
	}
	if changed {
		s.mu.Lock()
		s.dirty = true
		s.lastReason = "watch"
		s.mu.Unlock()
		if err := s.Sync("watch"); err != nil {
			log.Printf("[memory] watch sync failed: %v", err)
		}
	}
}

func (s *MemoryStore) Stop() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mu.Unlock()
}

// EmbeddingDestroyer is optionally implemented by embedding providers that
// hold native resources (e.g. ONNX sessions) requiring explicit cleanup.
type EmbeddingDestroyer interface {
	Destroy()
}

func (s *MemoryStore) Close() error {
	s.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.embedder != nil {
		if d, ok := s.embedder.(EmbeddingDestroyer); ok {
			d.Destroy()
		}
		s.embedder = nil
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *MemoryStore) SetOnUpdated(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onUpdated = fn
}

func (s *MemoryStore) MarkDirty(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty = true
	s.lastReason = reason
}

func (s *MemoryStore) Status() MemoryStoreStatus {
	s.mu.Lock()
	dirty := s.dirty
	reason := s.lastReason
	db := s.db
	embedder := s.embedder
	s.mu.Unlock()

	var files, chunks, cached int
	if db != nil {
		_ = db.QueryRow("SELECT COUNT(*) FROM memory_files").Scan(&files)
		_ = db.QueryRow("SELECT COUNT(*) FROM memory_chunks").Scan(&chunks)
		_ = db.QueryRow("SELECT COUNT(*) FROM memory_embedding_cache").Scan(&cached)
	}

	provider := "none"
	model := ""
	vectorReady := false
	if embedder != nil {
		provider = embedder.Provider()
		model = embedder.Model()
		vectorReady = true
	}

	return MemoryStoreStatus{
		Files:        files,
		Chunks:       chunks,
		Dirty:        dirty,
		SyncReason:   reason,
		Provider:     provider,
		Model:        model,
		VectorReady:  vectorReady,
		CachedEmbeds: cached,
	}
}

// resolveAbsPath returns the absolute path for a file record based on its source.
func (s *MemoryStore) resolveAbsPath(path string, source MemorySource) string {
	switch source {
	case MemorySourceKnowledge:
		return filepath.Join(s.knowledgeDir, path)
	default:
		return filepath.Join(s.baseDir, path)
	}
}

// ── Diary / Journal ─────────────────────────────────────────────────────
// Matches openclaw's dated memory files (memory/YYYY-MM-DD.md).

func (s *MemoryStore) SaveDiaryEntry(content string, preflight ...func(id, content string) error) error {
	now := time.Now()
	filename := now.Format("2006-01-02") + ".md"
	filePath := filepath.Join(s.baseDir, filename)

	existing, readErr := os.ReadFile(filePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}

	var newContent string
	if len(existing) == 0 {
		newContent = fmt.Sprintf("# %s\n\n%s\n", now.Format("2006-01-02"), content)
	} else {
		newContent = string(existing) + "\n" + content + "\n"
	}

	if len(preflight) > 0 {
		if err := preflight[0](now.Format("2006-01-02"), newContent); err != nil {
			return err
		}
	}
	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock := memoryFileLocks.Acquire(now.Format("2006-01-02"))
	defer unlock()
	current, err := os.ReadFile(filePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if string(current) != string(existing) {
		return fmt.Errorf("memory diary changed while checking a new observation; retry")
	}
	if err := checkMemoryWriteAllowed(s.baseDir, now.Format("2006-01-02"), newContent, true); err != nil {
		return err
	}
	if err := os.WriteFile(filePath, []byte(newContent), 0600); err != nil {
		return err
	}

	s.MarkDirty("diary")
	return nil
}

// ── Config I/O ──────────────────────────────────────────────────────────

func loadMemoryStoreConfig(configDir string) MemoryStoreConfig {
	var cfg MemoryStoreConfig

	// Try config file
	data, err := os.ReadFile(filepath.Join(configDir, "memory.json"))
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}

	// Override with env vars
	if key := os.Getenv("OPENAI_API_KEY"); key != "" && cfg.APIKey == "" {
		cfg.APIKey = key
		if cfg.Provider == "" {
			cfg.Provider = "openai"
		}
	}
	if url := os.Getenv("OPENAI_BASE_URL"); url != "" && cfg.BaseURL == "" {
		cfg.BaseURL = url
	}

	// If no provider is configured at all, auto-detect will try Ollama
	// (handled in resolveEmbeddingProvider)

	return cfg
}

func saveMemoryStoreConfig(configDir string, cfg MemoryStoreConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, "memory.json"), data, 0600)
}
