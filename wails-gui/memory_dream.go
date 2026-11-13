package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/pii"
)

// Memory Dream System
//
// Periodically consolidates the markdown memory store by archiving exact
// duplicates, expired temporary memories, and near-duplicate clusters.
// Layered so we burn the cheapest signal first:
//
//   Layer 1: rule-based (no LLM, no embeddings)
//     1a) exact-content duplicates (NFC + collapsed whitespace + lowercase)
//     1b) explicitly-expired temporary memories
//   Layer 2: vector self-similarity (no LLM)
//     - cluster pairs with cosine >= similarityMergeThreshold
//     - keep the survivor (hasHistory > dimension tier > recency), archive
//       the rest
//   Layer 3: LLM merge for borderline clusters
//     - cosine in [llmMergeLow, similarityMergeThreshold)
//     - small batched Haiku prompt decides keep/merge/synthesize
//     - synthesis is rejected on insufficient fact coverage (code-side
//       backstop on the model's "zero info loss" promise)
//
// Trigger model: once a calendar day at the configured time.
//   - "scheduled": every minute we check whether today's dailyTime has
//     passed AND no successful run has happened yet today. If both, fire.
//   - That same logic handles the "missed last night" case — if the
//     machine was off, the next time the app boots after dailyTime we'll
//     run quietly.
//   - "manual": user-initiated from the memory UI.

// logMemoryDreamError keeps provider echoes and file paths out of the app log.
// Stage is a fixed operation kind supplied by the call site, never model text.
func logMemoryDreamError(stage, id string, err error) {
	log.Printf("[dream] stage=%s id=%s failed: %s", stage, id, activityPrivateLogField("error", err.Error()))
}

// DreamSettings controls the Dream cycle. Persisted to
// <configDir>/dream-settings.json and re-read on every cycle so a UI
// edit takes effect immediately.
type DreamSettings struct {
	// Enabled gates the entire scheduler. The user-initiated "Run now" path
	// ignores this; it's only consulted by the daily tick.
	Enabled bool `json:"enabled"`
	// DailyTime is the local-time wake slot in "HH:MM" 24h. 03:00 default
	// because most users aren't using their machine then.
	DailyTime string `json:"dailyTime"`
	// ArchiveRetentionDays bounds disk growth: archived entries older
	// than this are GC'd at the end of each cycle.
	ArchiveRetentionDays int `json:"archiveRetentionDays"`
	// SimilarityMergeThreshold is the Layer-2 cosine cut. ≥ this is
	// auto-merged with no LLM. 0.95 is deliberately strict — long
	// markdown memories that share most of their content but differ in
	// a sentence or two would otherwise lose that sentence.
	SimilarityMergeThreshold float64 `json:"similarityMergeThreshold"`
	// LLMMergeLow is the lower cosine bound for Layer 3 mediation.
	// Pairs in [LLMMergeLow, SimilarityMergeThreshold) go through Haiku.
	LLMMergeLow float64 `json:"llmMergeLow"`
	// LLMEnabled lets users turn off Layer 3 entirely (e.g. when they
	// don't want to spend tokens, or to debug L1/L2 in isolation).
	LLMEnabled bool `json:"llmEnabled"`
	// LLMBatchSize caps cluster size sent to a single Haiku call.
	LLMBatchSize int `json:"llmBatchSize"`
	// DryRunOnly forces every cycle into preview mode. The user opts
	// out of this once they've verified a few preview reports — a
	// conservative on-ramp to the destructive path.
	DryRunOnly bool `json:"dryRunOnly"`
}

// DefaultDreamSettings are conservative defaults chosen so the first cycle
// after enablement is unlikely to over-merge. The thresholds are tighter
// than Alma's (0.95/0.80 vs 0.95/0.75) because Yansu memories are full
// notes rather than one-sentence facts — false positives cost more.
var DefaultDreamSettings = DreamSettings{
	Enabled:                  true, // on by default; safe because DryRunOnly starts true (preview-only, never archives until the user opts out)
	DailyTime:                "03:00",
	ArchiveRetentionDays:     30,
	SimilarityMergeThreshold: 0.95,
	LLMMergeLow:              0.80,
	LLMEnabled:               true,
	LLMBatchSize:             8,
	DryRunOnly:               true, // first cycles are previews; user flips this off in settings
}

// DreamRun is one row in the run history. Persisted to
// <configDir>/dream-runs.json (last 50).
type DreamRun struct {
	ID                 string `json:"id"`
	Trigger            string `json:"trigger"`
	Status             string `json:"status"` // running | completed | failed | cancelled
	StartedAt          int64  `json:"startedAt"`
	EndedAt            int64  `json:"endedAt"`
	Examined           int    `json:"examined"`
	ArchivedExact      int    `json:"archivedExact"`
	ArchivedExpired    int    `json:"archivedExpired"`
	ArchivedSimilarity int    `json:"archivedSimilarity"`
	ArchivedLLM        int    `json:"archivedLlm"`
	SynthesizedCreated int    `json:"synthesizedCreated"`
	OversizedClusters  int    `json:"oversizedClusters"`
	RejectedSyntheses  int    `json:"rejectedSyntheses"`
	DryRun             bool   `json:"dryRun"`
	Error              string `json:"error,omitempty"`
}

// DreamStatus is the snapshot the UI polls.
type DreamStatus struct {
	Running      bool      `json:"running"`
	CurrentRunID string    `json:"currentRunId,omitempty"`
	LastRun      *DreamRun `json:"lastRun,omitempty"`
	NextRunMs    int64     `json:"nextRunMs"`
	ArchiveCount int       `json:"archiveCount"`
}

// DreamArchiveProposal is one entry in the preview report. The UI surfaces
// these so the user can verify what would be removed before clicking
// "commit".
type DreamArchiveProposal struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Reason     string `json:"reason"`
	MergedInto string `json:"mergedInto,omitempty"`
	Dimension  string `json:"dimension"`
}

// DreamSynthesisProposal is one merged memory the L3 path proposes.
type DreamSynthesisProposal struct {
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Dimension string   `json:"dimension"`
	SourceIDs []string `json:"sourceIds"`
}

// DreamPreview is what the dry-run path returns. UI renders it in a drawer
// alongside a "Commit this preview" button.
type DreamPreview struct {
	Run               DreamRun                 `json:"run"`
	ArchiveProposed   []DreamArchiveProposal   `json:"archiveProposed"`
	SynthesisProposed []DreamSynthesisProposal `json:"synthesisProposed"`
	Examined          int                      `json:"examined"`
	OversizedClusters int                      `json:"oversizedClusters"`
	RejectedSyntheses int                      `json:"rejectedSyntheses"`
	Skipped           string                   `json:"skipped,omitempty"`
}

// MemoryDreamService owns the daily consolidation cycle.
//
// The service is single-flight: only one cycle (preview or real) runs at a
// time. Concurrent triggers return a no-op DreamPreview/DreamRun pointing
// at the in-flight run so the UI can attach to its progress events.
type MemoryDreamService struct {
	app *App

	settingsMu    sync.Mutex
	mu            sync.Mutex
	running       bool
	currentRun    *DreamRun
	aborted       bool
	cancelCycle   context.CancelFunc
	stopScheduler context.CancelFunc

	// History buffer: in-memory mirror of dream-runs.json. Kept small
	// (last 50). Loaded once on Start; mutations write-through.
	history    []DreamRun
	historyCap int
}

// memoryDreamHistoryCap is the number of recent runs we persist. Kept
// generous so users can scroll back a couple of months of nightly runs
// without losing context.
const memoryDreamHistoryCap = 50

// maxClusterSizeForLLM is the hub-defense cap on Layer 3. Clusters larger
// than this are skipped entirely — they're almost certainly transitive
// chains through a generic concept ("user", "App") rather than real
// redundancy, and partial LLM merging on a 50-member cluster could
// archive memories that don't actually share content.
const maxClusterSizeForLLM = 20

// NewMemoryDreamService wires the service to its App. Call Start(ctx) to
// begin the daily scheduling loop.
func NewMemoryDreamService(app *App) *MemoryDreamService {
	return &MemoryDreamService{
		app:        app,
		historyCap: memoryDreamHistoryCap,
	}
}

// Start launches the daily scheduling goroutine. Safe to call multiple
// times; subsequent calls no-op while a prior one is still running.
func (s *MemoryDreamService) Start(parent context.Context) {
	s.mu.Lock()
	if s.stopScheduler != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.stopScheduler = cancel
	s.mu.Unlock()

	s.loadHistory()

	// On startup, heal any "running" rows left over from a crashed
	// process. Without this they'd permanently confuse the scheduler
	// and the lastRun display.
	s.healZombies()

	go s.runScheduler(ctx)
}

// Stop tears the scheduler down. The Stop() of an in-flight cycle is
// best-effort: we mark aborted and cancel its context, then let it
// finish writing whatever it had already committed (no partial undo).
func (s *MemoryDreamService) Stop() {
	s.mu.Lock()
	if s.stopScheduler != nil {
		s.stopScheduler()
		s.stopScheduler = nil
	}
	if s.cancelCycle != nil {
		s.aborted = true
		s.cancelCycle()
	}
	s.mu.Unlock()
}

// runScheduler ticks once a minute and decides whether today's slot is due.
// The poll is cheap: it does a couple of file reads and a wall-clock
// comparison.
func (s *MemoryDreamService) runScheduler(ctx context.Context) {
	// Bootstrap tick after 5s so a "missed last night" scenario (machine
	// off when DailyTime passed) gets caught right after launch instead
	// of waiting up to a full minute.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	s.scheduleTick(ctx)

	ticker := time.NewTicker(cadenceMemoryDreamCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scheduleTick(ctx)
		}
	}
}

func (s *MemoryDreamService) scheduleTick(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	cfg := s.loadSettings()
	if !cfg.Enabled {
		return
	}
	hour, min, ok := parseDailyTime(cfg.DailyTime)
	if !ok {
		return
	}
	now := time.Now()
	if now.Hour()*60+now.Minute() < hour*60+min {
		return
	}
	todayKey := now.Format("2006-01-02")
	// Has any completed/failed run already finished today? Failed runs
	// also count: we don't want to hammer Haiku N times per day on a
	// stuck config. Manual re-runs always go through.
	for _, r := range s.recentHistorySnapshot() {
		if r.Status == "running" {
			continue
		}
		t := time.UnixMilli(r.StartedAt)
		if t.Format("2006-01-02") == todayKey {
			return
		}
	}

	go func() {
		if _, err := s.RunCycle(ctx, "scheduled", cfg.DryRunOnly); err != nil {
			logMemoryDreamError("scheduled_cycle", "", err)
		}
	}()
}

// RunCycle is the public entry point. trigger is "scheduled" or "manual".
// dryRun=true returns a preview without writing.
//
// The two semantics share most code so manual-preview, manual-commit, and
// scheduled-commit all go through the same layered pipeline.
func (s *MemoryDreamService) RunCycle(parent context.Context, trigger string, dryRun bool) (*DreamPreview, error) {
	s.mu.Lock()
	if s.running {
		current := s.currentRun
		s.mu.Unlock()
		return &DreamPreview{
			Run:     deref(current),
			Skipped: "A Dream cycle is already running. Wait for it to complete.",
		}, nil
	}
	run := &DreamRun{
		ID:        "dream-" + uuid.NewString(),
		Trigger:   trigger,
		Status:    "running",
		StartedAt: time.Now().UnixMilli(),
		DryRun:    dryRun,
	}
	s.running = true
	s.aborted = false
	s.currentRun = run
	ctx, cancel := context.WithCancel(parent)
	s.cancelCycle = cancel
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.currentRun = nil
		s.cancelCycle = nil
		s.mu.Unlock()
	}()

	s.appendHistory(*run) // initial "running" entry; updated on completion
	s.emit("memory-dream:started", map[string]any{"runId": run.ID, "trigger": trigger, "dryRun": dryRun})

	preview, err := s.executeCycle(ctx, run, dryRun)
	if err != nil {
		if errors.Is(err, context.Canceled) || s.aborted {
			run.Status = "cancelled"
		} else {
			run.Status = "failed"
			run.Error = err.Error()
		}
	} else {
		run.Status = "completed"
	}
	run.EndedAt = time.Now().UnixMilli()
	if preview != nil {
		preview.Run = *run
		preview.Examined = run.Examined
		preview.OversizedClusters = run.OversizedClusters
		preview.RejectedSyntheses = run.RejectedSyntheses
	}
	s.replaceHistory(*run)
	s.emit("memory-dream:completed", map[string]any{
		"runId":  run.ID,
		"status": run.Status,
		"dryRun": dryRun,
	})

	if !dryRun && run.Status == "completed" && s.app.telemetry != nil {
		s.app.telemetry.Capture("memory.consolidated", map[string]any{
			"run_id":              run.ID,
			"trigger":             run.Trigger,
			"examined":            run.Examined,
			"archived_exact":      run.ArchivedExact,
			"archived_expired":    run.ArchivedExpired,
			"archived_similarity": run.ArchivedSimilarity,
			"archived_llm":        run.ArchivedLLM,
			"synthesized_created": run.SynthesizedCreated,
			"duration_ms":         run.EndedAt - run.StartedAt,
		})
	}

	s.app.engagementMemoryDreamPreview(parent, dryRun, run, preview)
	return preview, nil
}

// executeCycle is the pipeline body. Splits into discrete layers so each
// can be aborted between steps without leaving a half-applied state.
func (s *MemoryDreamService) executeCycle(ctx context.Context, run *DreamRun, dryRun bool) (*DreamPreview, error) {
	settings := s.loadSettings()
	preview := &DreamPreview{}

	candidates, err := s.gatherCandidates(ctx)
	if err != nil {
		return preview, err
	}
	run.Examined = len(candidates)
	preview.Examined = len(candidates)

	if s.isAborted() {
		return preview, context.Canceled
	}

	// Layer 1a: exact duplicates.
	groups := exactDuplicateGroups(candidates)
	for _, g := range groups {
		for _, dup := range g.Duplicates {
			if s.isAborted() {
				return preview, context.Canceled
			}
			if dryRun {
				preview.ArchiveProposed = append(preview.ArchiveProposed, DreamArchiveProposal{
					ID:         dup.ID,
					Title:      dup.Title,
					Reason:     string(ArchiveReasonExactDup),
					MergedInto: g.Survivor.ID,
					Dimension:  dup.Dimension,
				})
				run.ArchivedExact++
				continue
			}
			if err := s.archive(dup, run.ID, ArchiveReasonExactDup, g.Survivor.ID); err != nil {
				logMemoryDreamError("archive_exact", dup.ID, err)
				continue
			}
			run.ArchivedExact++
		}
	}
	s.emit("memory-dream:progress", map[string]any{"runId": run.ID, "stage": "exact", "archived": run.ArchivedExact})

	if s.isAborted() {
		return preview, context.Canceled
	}

	// Re-load candidates: L1a removed some live files. L1b/L2/L3 need the
	// post-dedup view so we don't try to operate on archived memories.
	candidates, err = s.gatherCandidates(ctx)
	if err != nil {
		return preview, err
	}

	// Layer 1b: expired temporary memories. Only acts on memories whose
	// frontmatter explicitly carries `- **ExpiresAt**: <ISO8601>` in the
	// past. We deliberately do NOT auto-age memories without an explicit
	// expiry — the dimension/durability schema doesn't yet model
	// "temporary" so a TTL sweep would be a guess, not a fact.
	for _, c := range candidates {
		if s.isAborted() {
			return preview, context.Canceled
		}
		if !memoryHasExpired(c) {
			continue
		}
		if dryRun {
			preview.ArchiveProposed = append(preview.ArchiveProposed, DreamArchiveProposal{
				ID:        c.ID,
				Title:     c.Title,
				Reason:    string(ArchiveReasonExpired),
				Dimension: c.Dimension,
			})
			run.ArchivedExpired++
			continue
		}
		if err := s.archive(c, run.ID, ArchiveReasonExpired, ""); err != nil {
			logMemoryDreamError("archive_expired", c.ID, err)
			continue
		}
		run.ArchivedExpired++
	}
	s.emit("memory-dream:progress", map[string]any{"runId": run.ID, "stage": "expired", "archived": run.ArchivedExpired})

	if s.isAborted() {
		return preview, context.Canceled
	}

	// Layers 2 + 3: similarity-driven clustering. Re-gather so L2 sees
	// the post-L1 set. Skip entirely when no embedder is available —
	// no embeddings means no similarity layers to run.
	candidates, err = s.gatherCandidates(ctx)
	if err != nil {
		return preview, err
	}
	if hasAnyEmbedding(candidates) {
		if err := s.runSimilarityLayers(run, settings, candidates, dryRun, preview); err != nil {
			return preview, err
		}
	} else {
		log.Printf("[dream] skipping L2/L3 — no document embeddings available")
	}

	// Archive retention GC (skip on dry-run — preview shouldn't mutate
	// state). 30-day default; archived entries past that are deleted to
	// keep disk usage bounded.
	if !dryRun {
		cutoff := time.Now().Add(-time.Duration(settings.ArchiveRetentionDays) * 24 * time.Hour).UnixMilli()
		if purged, err := purgeArchiveBefore(cutoff); err != nil {
			logMemoryDreamError("purge_archive", "", err)
		} else if purged > 0 {
			s.emit("memory-dream:progress", map[string]any{"runId": run.ID, "stage": "purge", "purged": purged})
		}
	}

	return preview, nil
}

// gatherCandidates reads the live memory directory, parses every entry,
// and (when an embedder is configured) rolls each entry's per-chunk
// embeddings up to a single doc-level vector. This is the only place that
// touches the filesystem and SQLite index inside a cycle; everything else
// works on the resulting slice.
func (s *MemoryDreamService) gatherCandidates(ctx context.Context) ([]DreamMemoryCandidate, error) {
	entries, err := s.app.GetMemoryEntries()
	if err != nil {
		return nil, err
	}
	allowRead, err := s.app.memoryReadFilter()
	if err != nil {
		return nil, err
	}
	dir, err := memoryDir()
	if err != nil {
		return nil, err
	}
	embedByPath, err := s.loadDocEmbeddings()
	if err != nil {
		// Continue without embeddings — L1 will still run. Yansu users
		// with no embedding provider configured shouldn't lose the
		// rule-based pass.
		logMemoryDreamError("load_embeddings", "", err)
		embedByPath = nil
	}
	out := make([]DreamMemoryCandidate, 0, len(entries))
	for _, e := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Skip daily diary files (YYYY-MM-DD.md). They're not
		// generated memories — they're a journal stream the user
		// owns. The generator never writes Dimension on them; using
		// that as the filter would be fragile, so we match the literal
		// date pattern.
		if isDateOnlyMemoryID(e.ID) || !allowRead(e.ID, e.Content) {
			continue
		}
		forgotten, err := isMemoryForgotten(dir, e.ID, e.Content, time.Now())
		if err != nil {
			return nil, err
		}
		// User edits are authoritative text, not material for an automatic
		// rewrite or expiry pass. Forgotten content stays out of every layer.
		if forgotten || isUserEditedMemory(e.Content) {
			continue
		}
		out = append(out, DreamMemoryCandidate{
			ID:         e.ID,
			Title:      e.Title,
			Content:    e.Content,
			Dimension:  e.Dimension,
			Source:     e.Source,
			UpdatedAt:  e.UpdatedAt,
			ModTime:    e.ModTime,
			HasHistory: e.HasHistory,
			Normalized: normalizeMemoryContent(e.Content),
			Embedding:  embedByPath[e.ID+".md"],
		})
	}
	return out, nil
}

// loadDocEmbeddings pulls every memory's per-chunk embeddings out of the
// MemoryStore SQLite index and rolls them into a doc-level vector keyed
// by relative path (e.g. "memory-1736000000.md"). Returns nil when no
// embedder is configured — callers must tolerate this and skip L2/L3.
//
// We read the embeddings directly from the existing schema instead of
// re-embedding here. A re-embed would double-spend, and re-using the
// already-indexed vectors keeps Dream consistent with what the search
// path sees.
func (s *MemoryDreamService) loadDocEmbeddings() (map[string][]float64, error) {
	s.app.mu.Lock()
	store := s.app.memoryStore
	s.app.mu.Unlock()
	if store == nil {
		return nil, nil
	}
	store.mu.Lock()
	db := store.db
	embedder := store.embedder
	store.mu.Unlock()
	if db == nil || embedder == nil {
		return nil, nil
	}
	model := embedder.Model()

	rows, err := db.Query(`
		SELECT path, embedding FROM memory_chunks
		WHERE source = 'memory' AND model = ? AND embedding != ''`, model)
	if err != nil {
		return nil, fmt.Errorf("query chunk embeddings: %w", err)
	}
	defer rows.Close()

	byPath := map[string][][]float64{}
	for rows.Next() {
		var path, embJSON string
		if err := rows.Scan(&path, &embJSON); err != nil {
			return nil, err
		}
		var vec []float64
		if err := json.Unmarshal([]byte(embJSON), &vec); err != nil {
			continue
		}
		if len(vec) == 0 {
			continue
		}
		byPath[path] = append(byPath[path], vec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string][]float64{}
	for path, vecs := range byPath {
		out[path] = l2NormalizeCentroid(vecs)
	}
	return out, nil
}

// hasAnyEmbedding short-circuits the similarity layers when no candidate
// has a usable vector. Embedder may be configured but the index empty
// (fresh install, no sync yet) — running L2/L3 in that state would just
// burn CPU.
func hasAnyEmbedding(candidates []DreamMemoryCandidate) bool {
	for _, c := range candidates {
		if len(c.Embedding) > 0 {
			return true
		}
	}
	return false
}

// runSimilarityLayers builds the cluster graph and dispatches each cluster
// to L2 (auto-merge) or L3 (Haiku-mediated) based on the maximum pairwise
// cosine inside the cluster.
//
// Pairs joined when cosine >= LLMMergeLow. Pairs with cosine ≥
// SimilarityMergeThreshold drive the cluster into the L2 fast path.
// Everything in between goes to L3 (when LLMEnabled).
//
// Cross-dimension pairs are explicitly skipped at the pair level so a
// `topic_highlight` and a `user_insight` saying overlapping things don't
// land in the same cluster. We're conservative here on purpose — losing
// a "lens" by merging different dimensions is harder to detect than
// keeping a near-duplicate around.
func (s *MemoryDreamService) runSimilarityLayers(
	run *DreamRun,
	settings DreamSettings,
	candidates []DreamMemoryCandidate,
	dryRun bool,
	preview *DreamPreview,
) error {
	if len(candidates) < 2 {
		return nil
	}
	// Index by id for survivor lookups inside the cluster loop.
	byID := make(map[string]DreamMemoryCandidate, len(candidates))
	for _, c := range candidates {
		byID[c.ID] = c
	}

	uf := newUnionFind()
	type pair struct {
		a, b string
		sim  float64
	}
	var pairs []pair

	// O(n²) is fine — Dream runs at idle and our n is hundreds, low
	// thousands at worst. We yield every yieldEvery iterations so the
	// Wails event loop and HTTP server stay responsive while a cycle is
	// in flight.
	const yieldEvery = 64
	for i := 0; i < len(candidates); i++ {
		if i%yieldEvery == yieldEvery-1 && s.isAborted() {
			return context.Canceled
		}
		ai := candidates[i]
		if len(ai.Embedding) == 0 {
			continue
		}
		for j := i + 1; j < len(candidates); j++ {
			bj := candidates[j]
			if len(bj.Embedding) == 0 {
				continue
			}
			// Cross-dimension pair: never auto-cluster. Two
			// memories with different lenses on related content
			// are not the same memory.
			if ai.Dimension != bj.Dimension && ai.Dimension != "" && bj.Dimension != "" {
				continue
			}
			sim := cosineSimilarity64(ai.Embedding, bj.Embedding)
			if sim < settings.LLMMergeLow {
				continue
			}
			pairs = append(pairs, pair{ai.ID, bj.ID, sim})
			uf.union(ai.ID, bj.ID)
		}
	}
	if len(pairs) == 0 {
		return nil
	}

	clusters := map[string]map[string]struct{}{}
	clusterMaxSim := map[string]float64{}
	for _, p := range pairs {
		root := uf.find(p.a)
		set, ok := clusters[root]
		if !ok {
			set = map[string]struct{}{}
			clusters[root] = set
		}
		set[p.a] = struct{}{}
		set[p.b] = struct{}{}
		if p.sim > clusterMaxSim[root] {
			clusterMaxSim[root] = p.sim
		}
	}

	for root, idset := range clusters {
		if s.isAborted() {
			return context.Canceled
		}
		var members []DreamMemoryCandidate
		for id := range idset {
			if c, ok := byID[id]; ok {
				members = append(members, c)
			}
		}
		if len(members) < 2 {
			continue
		}
		// Hub defense: oversized clusters are almost always transitive
		// chains, not real redundancy. Skip wholesale.
		if len(members) > maxClusterSizeForLLM {
			run.OversizedClusters++
			log.Printf("[dream] skipping oversized cluster (size=%d root=%s); raise thresholds if recurrent", len(members), root)
			continue
		}

		maxSim := clusterMaxSim[root]
		if maxSim >= settings.SimilarityMergeThreshold {
			// Layer 2: tight cosine threshold. Tentatively pick the
			// survivor and check that every other cluster member is
			// substantially subsumed by it at the fact-token level.
			//
			// Embedding cosine is a useful but imperfect proxy for
			// content overlap: two memories sharing 80% of tokens but
			// each carrying a unique 20% can still embed at ≥ 0.95 if
			// their shared structure dominates the vector. Without
			// this check, the symmetric 80/20 case would silently
			// archive the loser and lose its 20% unique content.
			//
			// On any failure we demote the entire cluster to L3 so
			// Haiku can decide between synthesizing (preserving all
			// distinct facts) and keeping the cluster untouched. When
			// LLM is disabled, we conservatively skip the cluster —
			// archiving lossily here would defeat the whole purpose
			// of running the safety check.
			survivor := pickSurvivor(members)
			safeToFastMerge := true
			for _, m := range members {
				if m.ID == survivor.ID {
					continue
				}
				if !verifyDeletionSafety([]string{m.Content}, []string{survivor.Content}, deletionSafetyThreshold) {
					safeToFastMerge = false
					break
				}
			}
			if !safeToFastMerge {
				if !settings.LLMEnabled {
					log.Printf("[dream] L2 cluster has insufficient survivor coverage and LLM disabled; skipping (cluster=%d maxSim=%.3f)", len(members), maxSim)
					run.RejectedSyntheses++
					continue
				}
				log.Printf("[dream] L2 cluster failed survivor coverage; demoting to L3 (cluster=%d maxSim=%.3f)", len(members), maxSim)
				if err := s.runL3Cluster(run, settings, members, dryRun, preview); err != nil {
					return err
				}
				continue
			}
			for _, m := range members {
				if m.ID == survivor.ID {
					continue
				}
				if dryRun {
					preview.ArchiveProposed = append(preview.ArchiveProposed, DreamArchiveProposal{
						ID:         m.ID,
						Title:      m.Title,
						Reason:     string(ArchiveReasonSimilarityMerge),
						MergedInto: survivor.ID,
						Dimension:  m.Dimension,
					})
					run.ArchivedSimilarity++
					continue
				}
				if err := s.archive(m, run.ID, ArchiveReasonSimilarityMerge, survivor.ID); err != nil {
					logMemoryDreamError("archive_similar", m.ID, err)
					continue
				}
				run.ArchivedSimilarity++
			}
			continue
		}
		if !settings.LLMEnabled {
			continue
		}
		// Layer 3: Haiku-mediated. Batch if needed.
		if err := s.runL3Cluster(run, settings, members, dryRun, preview); err != nil {
			return err
		}
	}
	return nil
}

// runL3Cluster splits oversized-but-allowed clusters into Haiku-sized
// batches and dispatches each. Sorting by recency keeps the most-recently-
// updated memories (the likely top survivors) at the front of the first
// batch, so even if a batch boundary cuts a redundant subset, the user-
// edited entry isn't the one that gets demoted.
func (s *MemoryDreamService) runL3Cluster(
	run *DreamRun,
	settings DreamSettings,
	cluster []DreamMemoryCandidate,
	dryRun bool,
	preview *DreamPreview,
) error {
	if len(cluster) <= settings.LLMBatchSize {
		return s.runL3Merge(run, cluster, dryRun, preview)
	}
	sorted := append([]DreamMemoryCandidate(nil), cluster...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ai := sorted[i].UpdatedAt
		if ai == 0 {
			ai = sorted[i].ModTime
		}
		aj := sorted[j].UpdatedAt
		if aj == 0 {
			aj = sorted[j].ModTime
		}
		return ai > aj
	})
	for i := 0; i < len(sorted); i += settings.LLMBatchSize {
		end := i + settings.LLMBatchSize
		if end > len(sorted) {
			end = len(sorted)
		}
		batch := sorted[i:end]
		if len(batch) < 2 {
			continue
		}
		if err := s.runL3Merge(run, batch, dryRun, preview); err != nil {
			return err
		}
	}
	return nil
}

// runL3Merge calls Haiku on one batch and applies the delete/synthesize
// decisions, gated by the fact-coverage verifier. On any failure (parse,
// invariant violation, coverage gap) we leave the cluster untouched and
// log — partial application of an LLM merge is worse than no merge.
func (s *MemoryDreamService) runL3Merge(
	run *DreamRun,
	cluster []DreamMemoryCandidate,
	dryRun bool,
	preview *DreamPreview,
) error {
	if s.isAborted() {
		return context.Canceled
	}
	// A cycle can wait between gathering and dispatch. Recheck both the
	// captured dependency set and the current file so an edit or privacy
	// preference change cannot turn stale candidates into model input.
	allowRead, err := s.app.memoryReadFilter()
	if err != nil {
		return err
	}
	allowed := make([]DreamMemoryCandidate, 0, len(cluster))
	for _, candidate := range cluster {
		entry, readErr := s.app.GetMemoryEntry(candidate.ID)
		if readErr != nil || entry.Content != candidate.Content || !allowRead(candidate.ID, candidate.Content) || !allowRead(entry.ID, entry.Content) {
			continue
		}
		allowed = append(allowed, candidate)
	}
	cluster = allowed
	if len(cluster) < 2 {
		return nil
	}
	// Freeze privacy lineage before dispatch, using the same captured content
	// as the prompt. Later edits or archives cannot change these dependencies.
	provenances := make([]memoryReadProvenance, 0, len(cluster))
	for _, source := range cluster {
		provenances = append(provenances, memoryReadCapturedProvenance(filepath.Join(configDirOrFallback(), "memory"), source.ID, source.Content))
	}
	prompt := buildL3Prompt(cluster)
	raw, err := s.app.runLearningClaudeModel(prompt, s.app.lightTaskModel(), 60*time.Second)
	if err != nil {
		logMemoryDreamError("merge_model", "", err)
		return nil
	}
	valid := make(map[string]bool, len(cluster))
	for _, m := range cluster {
		valid[m.ID] = true
	}
	resp := parseMergeResponse(raw, valid)
	if len(resp.DeleteIDs) == 0 && len(resp.Synthesize) == 0 {
		return nil
	}

	// Hard invariant: the cluster MUST NOT vanish. If the LLM proposed
	// deleting every member without producing a synthesis, the safe
	// answer is to ignore the entire response.
	remaining := make([]DreamMemoryCandidate, 0, len(cluster))
	deletedSet := make(map[string]bool, len(resp.DeleteIDs))
	for _, id := range resp.DeleteIDs {
		deletedSet[id] = true
	}
	for _, m := range cluster {
		if !deletedSet[m.ID] {
			remaining = append(remaining, m)
		}
	}
	if len(remaining) == 0 && len(resp.Synthesize) == 0 {
		log.Printf("[dream] L3: model wanted to wipe cluster with no synthesis; ignoring")
		run.RejectedSyntheses++
		return nil
	}

	// Deletion-safety check: every memory the model wants to delete must
	// be substantially subsumed by at least ONE survivor (a kept memory
	// or a synthesized memory). Single-survivor semantics — not union —
	// because a union check would pass the symmetric N/M-overlap case
	// where neither survivor alone actually contains the deleted's
	// distinct content. See verifyDeletionSafety for the full argument.
	var sources []string
	for _, m := range cluster {
		if deletedSet[m.ID] {
			sources = append(sources, m.Content)
		}
	}
	if len(sources) > 0 {
		var survivors []string
		for _, m := range remaining {
			survivors = append(survivors, m.Content)
		}
		for _, syn := range resp.Synthesize {
			survivors = append(survivors, syn.Content)
		}
		if !verifyDeletionSafety(sources, survivors, deletionSafetyThreshold) {
			log.Printf("[dream] L3: deletion-safety check failed; rejecting (cluster=%d delete=%d synth=%d)", len(cluster), len(resp.DeleteIDs), len(resp.Synthesize))
			run.RejectedSyntheses++
			return nil
		}
	}

	// Synthesize first so we have a survivor id to point mergedInto at.
	var synthesizedIDs []string
	for _, syn := range resp.Synthesize {
		// A generated synthesis cannot become a user-authored custom memory.
		// Missing or custom dimensions fall back to a generated dimension.
		dim := syn.Dimension
		if dim == "" {
			rep := pickSurvivor(cluster)
			dim = rep.Dimension
		}
		dim = normalizeMemoryDimension(dim)
		// The model saw the entire cluster, including retained inputs. Its
		// deletion selection is not a complete provenance statement.
		sourceIDs := make([]string, 0, len(cluster))
		sourceRefs := make([]string, 0, len(cluster))
		for _, source := range cluster {
			sourceIDs = append(sourceIDs, source.ID)
			sourceRefs = append(sourceRefs, taskContextSourceRef(taskContextSourceMemoryKind, source.ID, source.ModTime, source.Content))
		}

		if dryRun {
			preview.SynthesisProposed = append(preview.SynthesisProposed, DreamSynthesisProposal{
				Title:     syn.Title,
				Content:   syn.Content,
				Dimension: dim,
				SourceIDs: sourceIDs,
			})
			continue
		}

		id, err := s.writeSynthesizedMemoryWithProvenance(syn.Title, syn.Content, dim, sourceIDs, provenances, sourceRefs...)
		if err != nil {
			logMemoryDreamError("write_synthesis", "", err)
			continue
		}
		synthesizedIDs = append(synthesizedIDs, id)
		run.SynthesizedCreated++
	}

	// MergedInto pointer: prefer first synthesized id; otherwise top
	// remaining survivor.
	mergedInto := ""
	if len(synthesizedIDs) > 0 {
		mergedInto = synthesizedIDs[0]
	} else if len(remaining) > 0 {
		mergedInto = pickSurvivor(remaining).ID
	}

	for _, id := range resp.DeleteIDs {
		// Find the source candidate; if it vanished between scan and
		// archive, skip.
		var src *DreamMemoryCandidate
		for i := range cluster {
			if cluster[i].ID == id {
				src = &cluster[i]
				break
			}
		}
		if src == nil {
			continue
		}
		if dryRun {
			preview.ArchiveProposed = append(preview.ArchiveProposed, DreamArchiveProposal{
				ID:         id,
				Title:      src.Title,
				Reason:     string(ArchiveReasonLLMMerge),
				MergedInto: mergedInto,
				Dimension:  src.Dimension,
			})
			run.ArchivedLLM++
			continue
		}
		if err := s.archive(*src, run.ID, ArchiveReasonLLMMerge, mergedInto); err != nil {
			logMemoryDreamError("archive_merge", id, err)
			continue
		}
		run.ArchivedLLM++
	}
	return nil
}

// writeSynthesizedMemory creates a new memory file under memory/ tagged
// with a Dream-synthesis frontmatter. The shared save path supplies locking,
// suppression, and index updates while retaining machine attribution.
func (s *MemoryDreamService) writeSynthesizedMemory(title, body, dimension string, sourceIDs []string, sourceRefs ...string) (string, error) {
	provenances := []memoryReadProvenance{}
	for _, id := range sourceIDs {
		entry, err := s.app.GetMemoryEntry(id)
		if err != nil {
			provenances = append(provenances, memoryReadProvenance{Version: 1, Withheld: true})
			continue
		}
		provenances = append(provenances, memoryReadProvenanceForFile(filepath.Join(configDirOrFallback(), "memory"), entry.ID, entry.Content))
	}
	return s.writeSynthesizedMemoryWithProvenance(title, body, dimension, sourceIDs, provenances, sourceRefs...)
}

func (s *MemoryDreamService) writeSynthesizedMemoryWithProvenance(title, body, dimension string, sourceIDs []string, provenances []memoryReadProvenance, sourceRefs ...string) (string, error) {
	now := time.Now()
	content := renderOKFDocument(map[string]any{"type": "Isoform Memory", "title": strings.TrimSpace(title), "timestamp": okfTimestamp(now), "source": "dream-synthesis", "dimension": normalizeMemoryDimension(dimension), "area": classifyMemoryArea(title+"\n"+body, dimension), "merged_from": sourceIDs, "merged_from_refs": sourceRefs, "read_provenance": memoryReadProvenanceJSON(mergeMemoryReadProvenance(provenances...))}, body)
	saved, err := s.app.saveMemoryEntry(fmt.Sprintf("memory-%d", now.UnixNano()), content, false)
	if err != nil {
		return "", err
	}
	return saved.ID, nil
}

// archive funnels through the file-based archive layer. Errors are
// returned so callers can log per-entry; we never throw on a single bad
// archive — the rest of the cycle should keep going.
func (s *MemoryDreamService) archive(c DreamMemoryCandidate, runID string, reason ArchiveReason, mergedInto string) error {
	// Refetch the entry fresh so frontmatter parse and ModTime are
	// accurate (the in-memory candidate was captured at scan time).
	entry, err := s.app.GetMemoryEntry(c.ID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // already gone — treat as no-op
		}
		return err
	}
	_, err = s.app.archiveMemoryFile(entry, runID, reason, mergedInto)
	if err == nil {
		s.app.markMemoryDirty("dream-archive")
	}
	return err
}

func (s *MemoryDreamService) isAborted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.aborted
}

// ── status / settings / history ─────────────────────────────────────────

// Status returns a snapshot for the frontend poller.
func (s *MemoryDreamService) Status() DreamStatus {
	s.mu.Lock()
	running := s.running
	var current string
	if s.currentRun != nil {
		current = s.currentRun.ID
	}
	var lastCopy DreamRun
	hasLast := false
	for i := len(s.history) - 1; i >= 0; i-- {
		if s.history[i].Status != "running" {
			lastCopy = s.history[i]
			hasLast = true
			break
		}
	}
	s.mu.Unlock()

	cfg := s.loadSettings()
	var last *DreamRun
	if hasLast {
		last = &lastCopy
	}
	archiveCount, _ := countArchivedMemories()
	return DreamStatus{
		Running:      running,
		CurrentRunID: current,
		LastRun:      last,
		NextRunMs:    computeNextRunMs(cfg, hasLast, lastCopy),
		ArchiveCount: archiveCount,
	}
}

// RecentRuns returns the most recent run rows for the history drawer.
func (s *MemoryDreamService) RecentRuns(limit int) []DreamRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.history) {
		limit = len(s.history)
	}
	out := make([]DreamRun, 0, limit)
	for i := len(s.history) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.history[i])
	}
	return out
}

func (s *MemoryDreamService) recentHistorySnapshot() []DreamRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DreamRun, len(s.history))
	copy(out, s.history)
	return out
}

func (s *MemoryDreamService) loadSettings() DreamSettings {
	cfg, err := readMemoryDreamSettings()
	if err != nil {
		return DefaultDreamSettings
	}
	return cfg
}

// readMemoryDreamSettings preserves loadSettings semantics while allowing the
// Plugins page to report unreadable settings instead of guessing their state.
func readMemoryDreamSettings() (DreamSettings, error) {
	path, err := dreamSettingsPath()
	if err != nil {
		return DreamSettings{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultDreamSettings, nil
	}
	if err != nil {
		return DreamSettings{}, err
	}
	var cfg DreamSettings
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DreamSettings{}, err
	}
	// Backfill missing/invalid values with defaults so a partial settings
	// blob (e.g. user manually edited dailyTime but never touched the
	// thresholds) still gets sane numbers.
	if strings.TrimSpace(cfg.DailyTime) == "" {
		cfg.DailyTime = DefaultDreamSettings.DailyTime
	}
	if cfg.ArchiveRetentionDays <= 0 {
		cfg.ArchiveRetentionDays = DefaultDreamSettings.ArchiveRetentionDays
	}
	if cfg.SimilarityMergeThreshold <= 0 || cfg.SimilarityMergeThreshold > 1 {
		cfg.SimilarityMergeThreshold = DefaultDreamSettings.SimilarityMergeThreshold
	}
	if cfg.LLMMergeLow <= 0 || cfg.LLMMergeLow >= cfg.SimilarityMergeThreshold {
		cfg.LLMMergeLow = math.Min(DefaultDreamSettings.LLMMergeLow, cfg.SimilarityMergeThreshold-0.01)
	}
	if cfg.LLMBatchSize <= 0 || cfg.LLMBatchSize > 32 {
		cfg.LLMBatchSize = DefaultDreamSettings.LLMBatchSize
	}
	return cfg, nil
}

// SaveSettings persists user-supplied settings to disk. We validate and
// clamp here, not in the frontend, so manual edits to the JSON file can't
// produce a config that breaks the cycle (e.g. SimilarityMergeThreshold=0
// would archive every pair).
func (s *MemoryDreamService) SaveSettings(cfg DreamSettings) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if _, _, ok := parseDailyTime(cfg.DailyTime); !ok {
		cfg.DailyTime = DefaultDreamSettings.DailyTime
	}
	if cfg.ArchiveRetentionDays <= 0 {
		cfg.ArchiveRetentionDays = DefaultDreamSettings.ArchiveRetentionDays
	}
	// Clamp thresholds into safe ranges. Anything below 0.6 cosine is
	// noise from short content; anything ≥ 1.0 would never fire.
	if cfg.SimilarityMergeThreshold < 0.7 {
		cfg.SimilarityMergeThreshold = 0.7
	}
	if cfg.SimilarityMergeThreshold > 0.999 {
		cfg.SimilarityMergeThreshold = 0.999
	}
	if cfg.LLMMergeLow < 0.6 {
		cfg.LLMMergeLow = 0.6
	}
	if cfg.LLMMergeLow >= cfg.SimilarityMergeThreshold {
		cfg.LLMMergeLow = cfg.SimilarityMergeThreshold - 0.01
	}
	if cfg.LLMBatchSize <= 0 {
		cfg.LLMBatchSize = DefaultDreamSettings.LLMBatchSize
	}
	if cfg.LLMBatchSize > 32 {
		cfg.LLMBatchSize = 32
	}
	path, err := dreamSettingsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// SetSwitch changes only one boolean in the stored JSON object. Other known
// and future settings retain their values, including values the full editor
// would clamp or backfill when explicitly saved.
func (s *MemoryDreamService) SetSwitch(field string, enabled bool) error {
	switch field {
	case "enabled", "llmEnabled":
	default:
		return fmt.Errorf("unknown memory review switch %q", field)
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	path, err := dreamSettingsPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = json.Marshal(DefaultDreamSettings)
	}
	if err != nil {
		return err
	}
	var stored map[string]json.RawMessage
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	if stored == nil {
		return errors.New("memory review settings must be a JSON object")
	}
	stored[field], err = json.Marshal(enabled)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func (s *MemoryDreamService) appendHistory(run DreamRun) {
	s.mu.Lock()
	s.history = append(s.history, run)
	if len(s.history) > s.historyCap {
		s.history = s.history[len(s.history)-s.historyCap:]
	}
	s.persistHistoryLocked()
	s.mu.Unlock()
}

// replaceHistory replaces the run row by id (used when a "running" row
// transitions to "completed"/"failed"/"cancelled"). Falls back to append
// if not found — defensive against history file corruption.
func (s *MemoryDreamService) replaceHistory(run DreamRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.history {
		if s.history[i].ID == run.ID {
			s.history[i] = run
			s.persistHistoryLocked()
			return
		}
	}
	s.history = append(s.history, run)
	if len(s.history) > s.historyCap {
		s.history = s.history[len(s.history)-s.historyCap:]
	}
	s.persistHistoryLocked()
}

func (s *MemoryDreamService) loadHistory() {
	path, err := dreamHistoryPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rows []DreamRun
	if err := json.Unmarshal(data, &rows); err != nil {
		return
	}
	s.mu.Lock()
	s.history = rows
	if len(s.history) > s.historyCap {
		s.history = s.history[len(s.history)-s.historyCap:]
	}
	s.mu.Unlock()
}

func (s *MemoryDreamService) persistHistoryLocked() {
	path, err := dreamHistoryPath()
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(s.history, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

// healZombies marks any pre-existing "running" rows as failed. Without
// this, a process crash mid-cycle would leave a row permanently stuck and
// scheduleTick would think today's slot is in flight forever.
func (s *MemoryDreamService) healZombies() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	healed := 0
	for i := range s.history {
		if s.history[i].Status == "running" {
			s.history[i].Status = "failed"
			s.history[i].Error = "interrupted"
			s.history[i].EndedAt = now
			healed++
		}
	}
	if healed > 0 {
		s.persistHistoryLocked()
		log.Printf("[dream] healed %d interrupted run row(s)", healed)
	}
}

func (s *MemoryDreamService) emit(event string, data any) {
	if s.app == nil || s.app.ctx == nil {
		return
	}
	s.app.emitBrowserEvent(event, data)
}

// ── helpers ─────────────────────────────────────────────────────────────

func parseDailyTime(s string) (hour, minute int, ok bool) {
	s = strings.TrimSpace(s)
	if len(s) < 4 || !strings.Contains(s, ":") {
		return 0, 0, false
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err := parseInt(parts[0])
	if err != nil {
		return 0, 0, false
	}
	m, err := parseInt(parts[1])
	if err != nil {
		return 0, 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func parseInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	return v, nil
}

// computeNextRunMs returns the wall-clock ms of the next expected slot.
// Today if today's slot hasn't been claimed; tomorrow otherwise.
func computeNextRunMs(cfg DreamSettings, hasLast bool, last DreamRun) int64 {
	return computeNextRunMsAt(cfg, hasLast, last, time.Now())
}

func computeNextRunMsAt(cfg DreamSettings, hasLast bool, last DreamRun, now time.Time) int64 {
	hour, min, ok := parseDailyTime(cfg.DailyTime)
	if !ok {
		hour, min = 3, 0
	}
	slot := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, time.Local)
	if now.After(slot) {
		// Today's slot already passed; next is tomorrow unless we
		// haven't run today (then it's "any minute now").
		if hasLast && time.UnixMilli(last.StartedAt).Format("2006-01-02") == now.Format("2006-01-02") {
			slot = slot.AddDate(0, 0, 1)
		} else {
			slot = now
		}
	}
	return slot.UnixMilli()
}

func dreamSettingsPath() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "dream-settings.json"), nil
}

func dreamHistoryPath() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "dream-runs.json"), nil
}

func deref(r *DreamRun) DreamRun {
	if r == nil {
		return DreamRun{}
	}
	return *r
}

// memoryHasExpired reads the candidate's frontmatter for an `ExpiresAt`
// field and returns true when it parses to a past time. Conservative: any
// parse failure leaves the memory in place.
func memoryHasExpired(c DreamMemoryCandidate) bool {
	const key = "- **ExpiresAt**:"
	for _, raw := range strings.SplitN(c.Content, "\n", 40) {
		line := strings.TrimSpace(raw)
		rest, ok := strings.CutPrefix(line, key)
		if !ok {
			continue
		}
		val := strings.TrimSpace(rest)
		if val == "" {
			return false
		}
		// Accept both ISO 8601 and the human "2006-01-02 15:04" form.
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t.Before(time.Now())
		}
		if t, err := time.ParseInLocation("2006-01-02 15:04", val, time.Local); err == nil {
			return t.Before(time.Now())
		}
		return false
	}
	return false
}

// isDateOnlyMemoryID matches the YYYY-MM-DD.md diary files produced by
// SaveMemoryDiary. Dream skips these — they're an append-only journal,
// not generated memories.
func isDateOnlyMemoryID(id string) bool {
	if len(id) != 10 {
		return false
	}
	if id[4] != '-' || id[7] != '-' {
		return false
	}
	for i, r := range id {
		if i == 4 || i == 7 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// buildL3Prompt assembles the Haiku merge prompt. We pre-truncate each
// memory to dreamL3MaxCharsPerMemory so a single multi-kilobyte note doesn't
// blow the prompt budget. The model is reminded that any deletion must
// be accompanied by a synthesis that contains every distinct fact —
// the same invariant the fact-coverage verifier checks in code.
func buildL3Prompt(cluster []DreamMemoryCandidate) string {
	var b strings.Builder
	b.WriteString(dreamL3SystemPrompt)
	b.WriteString("\n\nCluster of related memories:\n\n")
	for _, m := range cluster {
		fmt.Fprintf(&b, "=== id: %s ===\n", m.ID)
		fmt.Fprintf(&b, "Title: %s\n", m.Title)
		fmt.Fprintf(&b, "Dimension: %s\n", m.Dimension)
		content, _ := pii.RedactText(stripMemoryFrontmatter(m.Content))
		content = strings.TrimSpace(content)
		if len(content) > dreamL3MaxCharsPerMemory {
			content = content[:dreamL3MaxCharsPerMemory] + "\n[...truncated]"
		}
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	b.WriteString("Respond now with the JSON object only. No prose, no fences.\n")
	prompt, _ := pii.RedactText(b.String())
	return prompt
}

const dreamL3MaxCharsPerMemory = 1500

const dreamL3SystemPrompt = `You are a memory-consolidation assistant. The user has multiple memory notes that share enough content to be candidates for merging. Decide what to do with them.

You may DELETE redundant memories (when every distinct fact survives elsewhere) and SYNTHESIZE one or more merged memories (a single note that subsumes a set of overlapping ones). Anything you neither delete nor synthesize stays untouched.

HARD CONSTRAINTS (these are non-negotiable):
- ZERO information loss. Every distinct fact that appears in the cluster MUST survive — either inside a kept memory or inside a synthesized memory.
- DO NOT MERGE ACROSS DIMENSIONS. If two memories have different Dimension values, they are different lenses on related content; leave them alone.
- The cluster must NOT become empty. At least one memory (original or synthesized) must remain.
- DO NOT invent facts. Synthesis content must contain only information present in the cluster.
- Synthesis text should read like a memory note: a brief Markdown body with a concrete subject and concrete facts. No headers, no bullet lists unless the originals used them.

When in doubt, output {"delete": [], "synthesize": []}. The user prefers a few un-merged near-duplicates over a single over-aggressive merge that loses signal.

Output strictly this JSON shape and nothing else:
{
  "delete": ["<memory-id>", ...],
  "synthesize": [
    {"title": "...", "content": "...", "dimension": "<one of: user_insight | topic_highlight | deep_user_model | agentic_memory | custom>"}
  ]
}`
