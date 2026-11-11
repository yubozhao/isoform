package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── Sync ────────────────────────────────────────────────────────────────
// Replicates openclaw's MemoryManagerSyncOps.sync():
//   - File discovery and diffing
//   - Full reindex detection (model/provider/config changes)
//   - Chunking + embedding + FTS indexing
//   - Metadata persistence for change detection

func (s *MemoryStore) Sync(reason string) error {
	return s.syncContext(context.Background(), reason)
}

// syncContext gives an attended context read ownership of cancellation. The
// background/native Sync contract continues to use context.Background.
func (s *MemoryStore) syncContext(ctx context.Context, reason string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only one sync at a time. Late arrivals skip — the running sync
	// already covers the dirty window, and the next scheduled tick will
	// catch anything new.
	if !s.syncMu.TryLock() {
		return nil
	}
	defer s.syncMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	db := s.db
	indexMayHaveChanged := false
	// Per-file transactions can commit before cancellation. Repair the shared
	// keyword projection before releasing sync ownership, even for same-count
	// edits, so the next native read cannot resurrect withdrawn source text.
	defer func() {
		if err != nil && indexMayHaveChanged {
			if repairErr := rebuildMemoryFTS(context.Background(), db); repairErr != nil {
				err = fmt.Errorf("%w; repair canceled sync keyword index: %v", err, repairErr)
			}
		}
	}()
	// Clear dirty early so concurrent Search() callers don't pile on
	// with additional Sync("search") calls while this one is running.
	// If new writes arrive mid-sync, MarkDirty / detectFileChanges will
	// re-set the flag and the next tick picks it up.
	s.dirty = false
	s.lastReason = ""
	s.mu.Unlock()

	defer func() {
		if err != nil {
			s.mu.Lock()
			s.dirty = true
			s.lastReason = "retry-after-failure"
			s.mu.Unlock()
		}
	}()

	syncStart := time.Now()
	log.Printf("[memory] sync started (reason: %s)", reason)

	// Check if full reindex is needed (provider/model/config changed)
	fullReindex := s.needsFullReindex(db, ctx)
	if fullReindex {
		indexMayHaveChanged = true
		log.Printf("[memory] full reindex required (config changed)")
		if err := s.clearAllIndexData(db, ctx); err != nil {
			log.Printf("[memory] failed to clear index for full reindex: %v", err)
		}
	}

	// 1. Discover all .md files
	discoverStart := time.Now()
	diskFiles, err := s.discoverFiles(ctx)
	if err != nil {
		return fmt.Errorf("discover files: %w", err)
	}
	log.Printf("[memory] sync discover: %d files in %v", len(diskFiles), time.Since(discoverStart))

	// 2. Load indexed file records
	loadStart := time.Now()
	indexed, err := s.loadIndexedFiles(db, ctx)
	if err != nil {
		return fmt.Errorf("load indexed files: %w", err)
	}
	log.Printf("[memory] sync loadIndexed: %d files in %v", len(indexed), time.Since(loadStart))

	// 3. Diff: find added/changed/removed.
	// Use "source:path" as the map key to avoid collisions across sources.
	diskMap := make(map[string]memoryFileRecord, len(diskFiles))
	for _, f := range diskFiles {
		diskMap[fileKey(f)] = f
	}

	var addedOrChanged int
	var removed int

	// Remove files no longer on disk
	for key, idxFile := range indexed {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, exists := diskMap[key]; !exists {
			indexMayHaveChanged = true
			if err := s.removeFile(db, idxFile.Path, ctx); err != nil {
				log.Printf("[memory] failed to remove %s: %v", idxFile.Path, err)
			}
			removed++
		}
	}

	// Add or update changed files
	for key, diskFile := range diskMap {
		if err := ctx.Err(); err != nil {
			return err
		}
		idxFile, exists := indexed[key]
		if exists && idxFile.Hash == diskFile.Hash && !fullReindex {
			continue // unchanged
		}
		fileStart := time.Now()
		absPath := s.resolveAbsPath(diskFile.Path, diskFile.Source)
		content, err := readMemorySyncFile(ctx, absPath)
		if err != nil {
			log.Printf("[memory] failed to read %s: %v", absPath, err)
			continue
		}
		indexMayHaveChanged = true
		if err := s.indexFile(db, diskFile, string(content), ctx); err != nil {
			log.Printf("[memory] failed to index %s: %v", diskFile.Path, err)
		}
		log.Printf("[memory] sync indexFile %s in %v (len=%d)", diskFile.Path, time.Since(fileStart), len(content))
		addedOrChanged++
	}
	log.Printf("[memory] sync indexing done: %d files in %v", addedOrChanged, time.Since(syncStart))

	// Rebuild FTS from the chunks table when anything changed. We skip
	// per-row FTS delete-by-value in indexFile (it fails on drifted data
	// and causes a ~100ms rebuild loop every sync cycle), so the FTS table
	// accumulates orphaned entries from replaced chunks. A single bulk
	// rebuild here is cheaper than N failing per-row deletes and is only
	// triggered when files actually changed.
	if addedOrChanged > 0 || removed > 0 {
		s.rebuildFTS(db, ctx)
	} else if reason == "startup" {
		// On startup only, check and repair in case a prior crash left drift.
		s.repairFTSIfNeeded(db, ctx)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Store current metadata for future change detection
	s.storeCurrentMeta(db, ctx)
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	onUpdated := s.onUpdated
	s.mu.Unlock()

	log.Printf("[memory] sync complete: %d indexed, %d removed, %d total files",
		addedOrChanged, removed, len(diskFiles))

	if (addedOrChanged > 0 || removed > 0) && onUpdated != nil {
		onUpdated()
	}

	return nil
}

// fileKey returns a unique map key for a memoryFileRecord: "source:path".
func fileKey(f memoryFileRecord) string {
	return string(f.Source) + ":" + f.Path
}

// ── File discovery ──────────────────────────────────────────────────────

func (s *MemoryStore) discoverFiles(contexts ...context.Context) ([]memoryFileRecord, error) {
	ctx := memoryOperationContext(contexts)
	var files []memoryFileRecord

	// Scan memory directory
	if err := s.walkDir(s.baseDir, MemorySourceMemory, &files, ctx); err != nil {
		return nil, err
	}

	// Scan knowledge directory (may not exist yet)
	if s.knowledgeDir != "" {
		if _, err := os.Stat(s.knowledgeDir); err == nil {
			if err := s.walkDir(s.knowledgeDir, MemorySourceKnowledge, &files, ctx); err != nil {
				return nil, err
			}
		}
	}

	return files, nil
}

func (s *MemoryStore) walkDir(baseDir string, source MemorySource, out *[]memoryFileRecord, contexts ...context.Context) error {
	ctx := memoryOperationContext(contexts)
	return filepath.Walk(baseDir, func(absPath string, info os.FileInfo, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return nil
		}
		// Skip the per-entry snapshot tree (dir/.history/<id>/<ts>.md). Those
		// files are frozen copies written on every update; indexing them would
		// surface stale content in search and double-count in chunk stats.
		//
		// Same goes for the Dream archive (dir/.archive/<archive-id>.md): the
		// Dream service moves removed memories there, and re-indexing them
		// would resurface archived content in search results — exactly what
		// archiving was supposed to prevent.
		if info.IsDir() && (info.Name() == memoryHistoryDirName || info.Name() == memoryArchiveDirName) {
			return filepath.SkipDir
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		relPath, err := filepath.Rel(baseDir, absPath)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		content, err := readMemorySyncFile(ctx, absPath)
		if err != nil {
			return nil
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(content))

		*out = append(*out, memoryFileRecord{
			Path:   relPath,
			Source: source,
			Hash:   hash,
			Mtime:  info.ModTime().UnixMilli(),
			Size:   info.Size(),
		})
		return nil
	})
}

// ── Index operations ────────────────────────────────────────────────────

func (s *MemoryStore) loadIndexedFiles(db *sql.DB, contexts ...context.Context) (map[string]memoryFileRecord, error) {
	ctx := memoryOperationContext(contexts)
	rows, err := db.QueryContext(ctx, "SELECT path, source, hash, mtime, size FROM memory_files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]memoryFileRecord)
	for rows.Next() {
		var f memoryFileRecord
		var source string
		if err := rows.Scan(&f.Path, &source, &f.Hash, &f.Mtime, &f.Size); err != nil {
			return nil, err
		}
		f.Source = MemorySource(source)
		result[fileKey(f)] = f
	}
	return result, rows.Err()
}

func (s *MemoryStore) removeFile(db *sql.DB, path string, contexts ...context.Context) error {
	ctx := memoryOperationContext(contexts)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Skip per-row FTS delete (see indexFile comment). The caller's
	// rebuildFTS / repairFTSIfNeeded will reconcile the FTS table.
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_chunks WHERE path = ?", path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_files WHERE path = ?", path); err != nil {
		return err
	}

	return tx.Commit()
}

// indexFile chunks content, optionally embeds, and stores in DB + FTS.
func (s *MemoryStore) indexFile(db *sql.DB, file memoryFileRecord, content string, contexts ...context.Context) error {
	ctx := memoryOperationContext(contexts)
	if file.Source == MemorySourceMemory {
		forgotten, err := isMemoryForgotten(s.baseDir, strings.TrimSuffix(filepath.Base(file.Path), ".md"), content, time.Now())
		if err != nil {
			return err
		}
		if forgotten {
			return removeForgottenMemoryIndex(db, file.Path, []string{content})
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t0 := time.Now()
	chunks := chunkMarkdown(content, memoryChunkTokens, memoryChunkOverlap)
	log.Printf("[memory] indexFile %s: chunked into %d chunks in %v", file.Path, len(chunks), time.Since(t0))

	// Determine model name
	s.mu.Lock()
	embedder := s.embedder
	s.mu.Unlock()

	model := ""
	if embedder != nil {
		model = embedder.Model()
	}

	// Compute chunk hashes
	chunkHashes := make([]string, len(chunks))
	chunkTexts := make([]string, len(chunks))
	for i, c := range chunks {
		chunkHashes[i] = c.Hash
		chunkTexts[i] = c.Text
	}

	// Embed chunks (with cache) if provider is available
	var embeddings [][]float64
	if embedder != nil {
		embedStart := time.Now()
		embedCtx, cancel := context.WithTimeout(ctx, embeddingBatchTimeout)
		var err error
		embeddings, err = s.embedWithCache(embedCtx, db, chunkTexts, chunkHashes)
		cancel()
		log.Printf("[memory] indexFile %s: embedWithCache %d chunks in %v", file.Path, len(chunkTexts), time.Since(embedStart))
		if err != nil {
			log.Printf("[memory] embedding failed for %s, indexing without vectors: %v", file.Path, err)
			embeddings = nil
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	txStart := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	log.Printf("[memory] indexFile %s: db.BeginTx(ctx,nil) acquired in %v", file.Path, time.Since(txStart))

	// Remove old chunks for this file. We intentionally skip per-row FTS
	// delete-by-value here. FTS5's 'delete' command requires values to
	// match the original insert exactly; when they drift (schema migration,
	// encoding change, unicode tokenizer inconsistency) the command fails
	// with "SQL logic error" and leaves orphaned FTS rows. Those orphans
	// then trigger a full FTS rebuild on every sync cycle — a ~100ms
	// blocking operation that causes UI jank.
	//
	// Instead we let the caller (Sync) do a single repairFTSIfNeeded at the
	// end of the batch, which is cheaper than N per-file delete attempts
	// that fail anyway.
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_chunks WHERE path = ?", file.Path); err != nil {
		return err
	}

	// Upsert file record
	if _, err := tx.ExecContext(ctx,
		"INSERT OR REPLACE INTO memory_files (path, source, hash, mtime, size) VALUES (?, ?, ?, ?, ?)",
		file.Path, string(file.Source), file.Hash, file.Mtime, file.Size,
	); err != nil {
		return err
	}

	// Insert new chunks + FTS entries
	insertStart := time.Now()
	source := string(file.Source)
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunkID := fmt.Sprintf("%s:%d", file.Path, i)

		// Serialize embedding as JSON (empty string if no embedding)
		embJSON := ""
		if i < len(embeddings) && embeddings[i] != nil {
			if data, err := json.Marshal(embeddings[i]); err == nil {
				embJSON = string(data)
			}
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_chunks (id, path, source, start_line, end_line, hash, model, text, embedding)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			chunkID, file.Path, source, chunk.StartLine, chunk.EndLine,
			chunk.Hash, model, chunk.Text, embJSON,
		); err != nil {
			return err
		}
		// FTS rows are NOT inserted here — the caller (Sync) does a single
		// rebuildFTS after the whole batch to keep chunks ↔ FTS in lockstep
		// without per-row delete-by-value (which fails on drifted data).
	}
	log.Printf("[memory] indexFile %s: inserted %d chunks+FTS in %v", file.Path, len(chunks), time.Since(insertStart))

	commitStart := time.Now()
	err = tx.Commit()
	log.Printf("[memory] indexFile %s: tx.Commit() in %v (total=%v)", file.Path, time.Since(commitStart), time.Since(t0))
	return err
}

// ── File change detection (polling-based) ───────────────────────────────

func (s *MemoryStore) detectFileChanges() (changed bool, removed []briefWithdrawn) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false, nil
	}
	baseDir := s.baseDir
	knowledgeDir := s.knowledgeDir
	prevHashes := s.fileHashes
	s.mu.Unlock()

	// Walk and hash files WITHOUT holding s.mu so Search() callers are
	// not blocked during the (potentially slow) I/O phase.
	currentHashes := make(map[string]string)

	scanDir := func(dir string, source MemorySource) {
		if dir == "" {
			return
		}
		_ = filepath.Walk(dir, func(absPath string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			// Skip .history/ and .archive/ — mirrors discoverFiles()/walkDir().
			if info.IsDir() && (info.Name() == memoryHistoryDirName || info.Name() == memoryArchiveDirName) {
				return filepath.SkipDir
			}
			if info.IsDir() || !strings.HasSuffix(info.Name(), ".md") {
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			relPath, err := filepath.Rel(dir, absPath)
			if err != nil {
				return nil
			}
			key := string(source) + ":" + filepath.ToSlash(relPath)

			content, err := os.ReadFile(absPath)
			if err != nil {
				return nil
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(content))
			currentHashes[key] = hash
			return nil
		})
	}

	scanDir(baseDir, MemorySourceMemory)
	scanDir(knowledgeDir, MemorySourceKnowledge)

	// Diff against the previous snapshot. changed says any file differs and the
	// index must read it again. removed names the files that are gone: deleting a
	// source withdraws it from the briefs that cite it. An edited file is not a
	// withdrawal, and neither is a new file.
	changed = false
	for key, hash := range currentHashes {
		if prevHash, exists := prevHashes[key]; !exists || prevHash != hash {
			changed = true
		}
	}
	for key := range prevHashes {
		if _, exists := currentHashes[key]; !exists {
			changed = true
			removed = append(removed, memoryFileWithdrawn(key))
		}
	}

	if changed {
		s.mu.Lock()
		s.fileHashes = currentHashes
		s.mu.Unlock()
	}

	return changed, removed
}

// memoryFileWithdrawn names the source a removed memory or knowledge file was,
// from its snapshot key ("<source>:<relative path>"). The id is the file name
// without its .md extension, which is how a brief cites it.
func memoryFileWithdrawn(key string) briefWithdrawn {
	source, rel, _ := strings.Cut(key, ":")
	return briefWithdrawn{Kind: source, ID: strings.TrimSuffix(rel, ".md")}
}

// ── Full reindex detection ──────────────────────────────────────────────
// Matches openclaw's needsFullReindex: detects provider/model/config changes.

func (s *MemoryStore) needsFullReindex(db *sql.DB, contexts ...context.Context) bool {
	ctx := memoryOperationContext(contexts)
	meta := s.loadMeta(db, ctx)
	if len(meta) == 0 {
		// No metadata yet — first run, needs indexing
		return true
	}

	s.mu.Lock()
	embedder := s.embedder
	s.mu.Unlock()

	model := ""
	providerKey := ""
	if embedder != nil {
		model = embedder.Model()
		providerKey = embedder.ProviderKey()
	}

	if meta["model"] != model {
		return true
	}
	if meta["provider_key"] != providerKey {
		return true
	}
	if meta["chunk_tokens"] != fmt.Sprintf("%d", memoryChunkTokens) {
		return true
	}
	if meta["chunk_overlap"] != fmt.Sprintf("%d", memoryChunkOverlap) {
		return true
	}

	return false
}

func (s *MemoryStore) clearAllIndexData(db *sql.DB, contexts ...context.Context) error {
	ctx := memoryOperationContext(contexts)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Clear FTS (rebuild)
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_chunks_fts"); err != nil {
		// FTS might be empty, ignore
		log.Printf("[memory] clear FTS: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_chunks"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_files"); err != nil {
		return err
	}

	return tx.Commit()
}

// ── Metadata persistence ────────────────────────────────────────────────

func (s *MemoryStore) loadMeta(db *sql.DB, contexts ...context.Context) map[string]string {
	ctx := memoryOperationContext(contexts)
	result := make(map[string]string)
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM memory_meta")
	if err != nil {
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			result[k] = v
		}
	}
	return result
}

func (s *MemoryStore) storeMeta(db *sql.DB, key, value string, contexts ...context.Context) {
	ctx := memoryOperationContext(contexts)
	_, _ = db.ExecContext(ctx,
		"INSERT OR REPLACE INTO memory_meta (key, value) VALUES (?, ?)",
		key, value)
}

// rebuildFTS repopulates the FTS index from memory_chunks. Since indexFile
// no longer writes FTS rows (the per-row delete-by-value was broken), this
// is the only place that keeps FTS in sync. It first checks whether the FTS
// count matches the chunks count and skips the rebuild when they agree,
// making no-op syncs free.
func (s *MemoryStore) rebuildFTS(db *sql.DB, contexts ...context.Context) {
	ctx := memoryOperationContext(contexts)
	var chunkCount, ftsCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_chunks").Scan(&chunkCount); err != nil {
		log.Printf("[memory] FTS rebuild: count chunks failed: %v", err)
		return
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_chunks_fts").Scan(&ftsCount); err != nil {
		log.Printf("[memory] FTS rebuild: count FTS failed: %v", err)
		return
	}
	if chunkCount == ftsCount {
		return
	}

	start := time.Now()
	log.Printf("[memory] FTS rebuild: chunks=%d fts=%d, rebuilding", chunkCount, ftsCount)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[memory] FTS rebuild: begin tx failed: %v", err)
		return
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_chunks_fts"); err != nil {
		log.Printf("[memory] FTS rebuild: clear failed: %v", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_chunks_fts(text, id, path, source, model, start_line, end_line)
		SELECT text, id, path, source, model, start_line, end_line FROM memory_chunks
	`); err != nil {
		log.Printf("[memory] FTS rebuild: repopulate failed: %v", err)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[memory] FTS rebuild: commit failed: %v", err)
		return
	}
	log.Printf("[memory] FTS rebuild: %d entries in %v", chunkCount, time.Since(start))
}

// repairFTSIfNeeded detects FTS/chunks count mismatch (caused by prior FTS
// delete failures leaving orphaned entries) and rebuilds the FTS index.
func (s *MemoryStore) repairFTSIfNeeded(db *sql.DB, contexts ...context.Context) {
	ctx := memoryOperationContext(contexts)
	var chunkCount, ftsCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_chunks").Scan(&chunkCount); err != nil {
		log.Printf("[memory] FTS repair: failed to count chunks: %v", err)
		return
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_chunks_fts").Scan(&ftsCount); err != nil {
		log.Printf("[memory] FTS repair: failed to count FTS rows: %v", err)
		return
	}
	if chunkCount == ftsCount {
		return
	}

	log.Printf("[memory] FTS repair: chunk count (%d) != FTS count (%d), rebuilding", chunkCount, ftsCount)
	start := time.Now()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[memory] FTS repair: begin tx failed: %v", err)
		return
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_chunks_fts"); err != nil {
		log.Printf("[memory] FTS repair: clear failed: %v", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_chunks_fts(text, id, path, source, model, start_line, end_line)
		SELECT text, id, path, source, model, start_line, end_line FROM memory_chunks
	`); err != nil {
		log.Printf("[memory] FTS repair: repopulate failed: %v", err)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[memory] FTS repair: commit failed: %v", err)
		return
	}
	log.Printf("[memory] FTS repair: rebuilt %d entries in %v", chunkCount, time.Since(start))
}

func (s *MemoryStore) storeCurrentMeta(db *sql.DB, contexts ...context.Context) {
	ctx := memoryOperationContext(contexts)
	s.mu.Lock()
	embedder := s.embedder
	s.mu.Unlock()

	model := ""
	providerKey := ""
	if embedder != nil {
		model = embedder.Model()
		providerKey = embedder.ProviderKey()
	}

	s.storeMeta(db, "model", model, ctx)
	s.storeMeta(db, "provider_key", providerKey, ctx)
	s.storeMeta(db, "chunk_tokens", fmt.Sprintf("%d", memoryChunkTokens), ctx)
	s.storeMeta(db, "chunk_overlap", fmt.Sprintf("%d", memoryChunkOverlap), ctx)
}

func memoryOperationContext(contexts []context.Context) context.Context {
	if len(contexts) > 0 && contexts[0] != nil {
		return contexts[0]
	}
	return context.Background()
}

func readMemorySyncFile(ctx context.Context, path string) ([]byte, error) {
	if ctx.Done() == nil {
		return os.ReadFile(path)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(todayChatAdmissionReader{ctx: ctx, reader: file})
}

// Force an atomic projection refresh; counts alone cannot detect same-size edits.
// Callers own syncMu, including cancellation cleanup before ownership is released.
func rebuildMemoryFTS(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM memory_chunks_fts"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_chunks_fts(text,id,path,source,model,start_line,end_line)
        SELECT text,id,path,source,model,start_line,end_line FROM memory_chunks`); err != nil {
		return err
	}
	return tx.Commit()
}
