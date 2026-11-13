package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// ── ActivityStore embedding integration ────────────────────────────────
//
// Adds semantic search to activity OCR data by embedding OCR frame text
// and searching via cosine similarity. Reuses the same EmbeddingProvider
// as the memory store.

const (
	activityEmbedBatchSize           = 4
	activityEmbedMaxFrames           = 96 // max frames to embed per session per run
	activityEmbedMaxBackfillSessions = 3
	activityEmbedMaxBackfillChecks   = 100
	activityEmbedBatchPause          = 750 * time.Millisecond
	activityEmbedSessionPause        = 500 * time.Millisecond
	activitySemanticTopK             = 100 // top-K OCR frames by cosine similarity
	activitySemanticMinScore         = 0.3
)

// SetEmbedder sets the embedding provider for semantic activity search.
// Called from App startup after the memory store initializes its provider.
func (s *ActivityStore) SetEmbedder(embedder EmbeddingProvider) {
	s.mu.Lock()
	s.embedder = embedder
	s.mu.Unlock()
}

// Embedder returns the current embedding provider (may be nil).
func (s *ActivityStore) Embedder() EmbeddingProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.embedder
}

// ── Embedding OCR frames ──────────────────────────────────────────────

// EmbedOCRFrames computes embeddings for OCR frames that don't have one yet.
// Called after OCR processing completes for a session, or as a backfill job.
type activityEmbeddingReadGate func(sessionID string) (bool, error)

func (a *App) activityEmbeddingGate(store *ActivityStore) activityEmbeddingReadGate {
	return func(sessionID string) (bool, error) {
		session, err := store.GetSessionByID(sessionID)
		if err != nil {
			return false, err
		}
		return a.activitySessionAllowsModelReadResult(store, *session)
	}
}

func (s *ActivityStore) EmbedOCRFrames(ctx context.Context, sessionID string, allowRead activityEmbeddingReadGate) error {
	s.mu.RLock()
	embedder := s.embedder
	s.mu.RUnlock()

	if embedder == nil {
		return nil
	}

	s.embeddingMu.Lock()
	defer s.embeddingMu.Unlock()

	// All providers, including remote embedders, use the current App privacy
	// gate. An unavailable gate or provenance read never permits transmission.
	if allowRead == nil {
		return fmt.Errorf("activity embedding privacy gate unavailable")
	}
	if allowed, err := allowRead(sessionID); err != nil || !allowed {
		return err
	}

	// Get frames without embeddings for this session. text != '' keeps
	// text-less box-cache rows (SaveOCRBoxesForSnapshot) out of the
	// embedding pipeline: they carry no semantics and would otherwise be
	// re-selected by every backfill pass.
	type frameRef struct {
		id   int64
		text string
		hash string
	}
	frames, err := func() ([]frameRef, error) {
		rows, err := s.db.QueryContext(ctx, `
			SELECT id, text FROM activity_ocr_frames
			WHERE session_id = ? AND text != '' AND (embedding IS NULL OR embedding = '')
			ORDER BY frame_time
			LIMIT ?`, sessionID, activityEmbedMaxFrames)
		if err != nil {
			return nil, fmt.Errorf("query unembedded frames: %w", err)
		}
		defer rows.Close()

		frames := make([]frameRef, 0)
		for rows.Next() {
			var f frameRef
			if err := rows.Scan(&f.id, &f.text); err != nil {
				return nil, err
			}
			f.hash = fmt.Sprintf("%x", sha256.Sum256([]byte(f.text)))[:16]
			frames = append(frames, f)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return frames, nil
	}()
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return nil
	}

	// Batch embed
	for lo := 0; lo < len(frames); lo += activityEmbedBatchSize {
		hi := min(lo+activityEmbedBatchSize, len(frames))
		batch := frames[lo:hi]

		texts := make([]string, len(batch))
		for i, f := range batch {
			texts[i] = redactActivityModelText(f.text)
		}

		if allowed, err := allowRead(sessionID); err != nil || !allowed {
			return err
		}
		embeddings, err := embedder.EmbedBatch(ctx, texts)
		if err != nil {
			log.Printf("[activity-embed] batch embed failed: %v", err)
			return err
		}

		// Privacy may change while a remote request is in flight. Do not publish
		// its result into the index after the source has become unavailable.
		if allowed, err := allowRead(sessionID); err != nil || !allowed {
			return err
		}

		// Store embeddings
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}

		stmt, err := tx.PrepareContext(
			ctx,
			`UPDATE activity_ocr_frames SET embedding = ?, embed_hash = ? WHERE id = ?`,
		)
		if err != nil {
			tx.Rollback()
			return err
		}

		for i, f := range batch {
			if i >= len(embeddings) || embeddings[i] == nil {
				continue
			}
			embJSON, err := json.Marshal(embeddings[i])
			if err != nil {
				continue
			}
			if _, err := stmt.ExecContext(ctx, string(embJSON), f.hash, f.id); err != nil {
				stmt.Close()
				tx.Rollback()
				return err
			}
		}
		if err := stmt.Close(); err != nil {
			tx.Rollback()
			return err
		}

		if err := tx.Commit(); err != nil {
			return err
		}

		if hi < len(frames) {
			if !sleepWithContext(ctx, activityEmbedBatchPause) {
				return ctx.Err()
			}
		}
	}

	log.Printf("[activity-embed] embedded %d OCR frames for session %s", len(frames), sessionID)
	return nil
}

type activityEmbeddingCursor struct {
	startTime int64
	id        string
}

func (s *ActivityStore) listEmbeddingBackfillSessionIDs(ctx context.Context, allowRead activityEmbeddingReadGate) ([]string, error) {
	if allowRead == nil {
		return nil, fmt.Errorf("activity embedding privacy gate unavailable")
	}
	s.embeddingBackfillMu.Lock()
	defer s.embeddingBackfillMu.Unlock()
	// Bound provenance work even when the whole backlog is denied. A keyset
	// cursor lets later readable sessions progress and eventually revisits
	// denied sources after privacy settings change, without storing permission.
	candidates, err := s.embeddingBackfillCandidates(ctx, activityEmbedMaxBackfillChecks, s.embeddingBackfillAfter)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 && s.embeddingBackfillAfter.id != "" {
		s.embeddingBackfillAfter = activityEmbeddingCursor{}
		candidates, err = s.embeddingBackfillCandidates(ctx, activityEmbedMaxBackfillChecks, s.embeddingBackfillAfter)
		if err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, activityEmbedMaxBackfillSessions)
	// The query releases its rows before the gate uses the single connection.
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		allowed, err := allowRead(candidate.id)
		if err != nil {
			return nil, err
		}
		s.embeddingBackfillAfter = candidate
		if allowed {
			result = append(result, candidate.id)
		}
		if len(result) == activityEmbedMaxBackfillSessions {
			return result, nil
		}
	}
	if len(candidates) < activityEmbedMaxBackfillChecks {
		s.embeddingBackfillAfter = activityEmbeddingCursor{}
	}
	return result, nil
}

func (s *ActivityStore) embeddingBackfillCandidates(ctx context.Context, limit int, after activityEmbeddingCursor) ([]activityEmbeddingCursor, error) {
	// Probe the small session table and use the existing session_id index for
	// each candidate. Scanning every OCR frame forces SQLite to read large
	// embedding payload pages even though only a few missing sessions are needed.
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.start_time, s.id
		FROM activity_sessions s
		WHERE COALESCE(s.triage_status, '') != 'protected'
		AND (? = '' OR (s.start_time, s.id) > (?, ?))
		AND EXISTS (
			SELECT 1
			FROM activity_ocr_frames o INDEXED BY idx_ocr_frames_session
			WHERE o.session_id = s.id
			AND o.text != ''
			AND (o.embedding IS NULL OR o.embedding = '')
		)
		ORDER BY s.start_time ASC, s.id ASC
		LIMIT ?`,
		after.id, after.startTime, after.id, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessionIDs := make([]activityEmbeddingCursor, 0, limit)
	for rows.Next() {
		var cursor activityEmbeddingCursor
		if err := rows.Scan(&cursor.startTime, &cursor.id); err != nil {
			return nil, err
		}
		sessionIDs = append(sessionIDs, cursor)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sessionIDs, nil
}

// BackfillEmbeddings processes older sessions that don't have embeddings yet.
// Runs as a background task.
func (s *ActivityStore) BackfillEmbeddings(ctx context.Context, allowRead activityEmbeddingReadGate) {
	s.mu.RLock()
	embedder := s.embedder
	s.mu.RUnlock()

	if embedder == nil {
		return
	}

	// The helper owns and closes its rows before EmbedOCRFrames requests the
	// store's single SQLite connection. Keeping those rows open deadlocks this
	// worker and starves every Activity-backed read.
	sessionIDs, err := s.listEmbeddingBackfillSessionIDs(ctx, allowRead)
	if err != nil {
		// Suppress noise when the database is closed during shutdown.
		if ctx.Err() == nil && !strings.Contains(err.Error(), "database is closed") {
			log.Printf("[activity-embed] backfill query failed: %v", err)
		}
		return
	}

	for _, sid := range sessionIDs {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := s.EmbedOCRFrames(ctx, sid, allowRead); err != nil {
			log.Printf("[activity-embed] backfill session %s failed: %v", sid, err)
		}

		if !sleepWithContext(ctx, activityEmbedSessionPause) {
			return
		}
	}

	if len(sessionIDs) > 0 {
		log.Printf("[activity-embed] backfilled %d sessions", len(sessionIDs))
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ── Semantic search ───────────────────────────────────────────────────

// ActivitySemanticResult represents a session matched by semantic search.
type ActivitySemanticResult struct {
	SessionID string  `json:"sessionId"`
	Score     float64 `json:"score"`
	MatchText string  `json:"matchText"` // best matching OCR snippet
	FrameTime float64 `json:"frameTime"` // timestamp of best match
}

// SemanticSearchSessions finds activity sessions whose OCR text is semantically
// similar to the query. Returns session IDs ranked by relevance.
func (s *ActivityStore) SemanticSearchSessions(ctx context.Context, query string, limit int) ([]ActivitySemanticResult, error) {
	s.mu.RLock()
	embedder := s.embedder
	s.mu.RUnlock()

	if embedder == nil {
		return nil, nil
	}

	// Embed the query
	queryVec, err := embedder.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// Load all embedded OCR frames, excluding protected sessions: their
	// frames may have been embedded before privacy gating existed and must
	// not be retrievable via semantic search. ("dismissed" is the normal
	// triage-complete state and is deliberately NOT excluded.)
	rows, err := s.db.Query(`
		SELECT f.id, f.session_id, f.frame_time, f.text, f.embedding
		FROM activity_ocr_frames f
		JOIN activity_sessions s ON f.session_id = s.id
		WHERE f.embedding IS NOT NULL AND f.embedding != ''
		AND COALESCE(s.triage_status, '') != 'protected'`)
	if err != nil {
		return nil, fmt.Errorf("query embedded frames: %w", err)
	}
	defer rows.Close()

	type scoredFrame struct {
		sessionID string
		frameTime float64
		text      string
		score     float64
	}
	var candidates []scoredFrame

	for rows.Next() {
		var id int64
		var sessionID, text, embJSON string
		var frameTime float64
		if err := rows.Scan(&id, &sessionID, &frameTime, &text, &embJSON); err != nil {
			continue
		}

		var emb []float64
		if err := json.Unmarshal([]byte(embJSON), &emb); err != nil {
			continue
		}

		score := cosineSimilarity(queryVec, emb)
		if score >= activitySemanticMinScore {
			candidates = append(candidates, scoredFrame{
				sessionID: sessionID,
				frameTime: frameTime,
				text:      text,
				score:     score,
			})
		}
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	// Truncate to top-K
	if len(candidates) > activitySemanticTopK {
		candidates = candidates[:activitySemanticTopK]
	}

	// Aggregate by session: keep best score and best matching frame per session
	type sessionAgg struct {
		bestScore float64
		bestText  string
		bestFrame float64
	}
	bySession := make(map[string]*sessionAgg)
	var sessionOrder []string

	for _, c := range candidates {
		agg, exists := bySession[c.sessionID]
		if !exists {
			agg = &sessionAgg{}
			bySession[c.sessionID] = agg
			sessionOrder = append(sessionOrder, c.sessionID)
		}
		if c.score > agg.bestScore {
			agg.bestScore = c.score
			agg.bestText = truncateText(c.text, 200)
			agg.bestFrame = c.frameTime
		}
	}

	// Sort sessions by best score
	sort.Slice(sessionOrder, func(i, j int) bool {
		return bySession[sessionOrder[i]].bestScore > bySession[sessionOrder[j]].bestScore
	})

	if len(sessionOrder) > limit {
		sessionOrder = sessionOrder[:limit]
	}

	results := make([]ActivitySemanticResult, len(sessionOrder))
	for i, sid := range sessionOrder {
		agg := bySession[sid]
		results[i] = ActivitySemanticResult{
			SessionID: sid,
			Score:     math.Round(agg.bestScore*1000) / 1000,
			MatchText: agg.bestText,
			FrameTime: agg.bestFrame,
		}
	}

	return results, nil
}

func truncateText(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// ── Background embedding worker ──────────────────────────────────────

// RunEmbeddingWorker periodically backfills embeddings for OCR frames.
func (s *ActivityStore) RunEmbeddingWorker(ctx context.Context, allowRead activityEmbeddingReadGate) {
	// Initial backfill after a short delay
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	s.BackfillEmbeddings(ctx, allowRead)

	// Periodic backfill
	ticker := time.NewTicker(cadenceActivityEmbedding)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.BackfillEmbeddings(ctx, allowRead)
		}
	}
}
