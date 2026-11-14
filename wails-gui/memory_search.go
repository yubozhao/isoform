package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"wails-gui/internal/pii"
)

type memorySearchOptions struct {
	mode           string
	resultLimit    int
	candidateLimit int
	applyMMR       bool
	filterMinScore bool
	strictErrors   bool
	contextSync    bool
	readFilter     func([]MemorySearchResult) ([]MemorySearchResult, error)
}

// ── Search constants (matching openclaw defaults) ───────────────────────

const (
	memoryDefaultMaxResults     = 20
	memoryDefaultMinScore       = 0.35
	memorySnippetMaxChars       = 700
	memoryDefaultVectorWeight   = 0.7
	memoryDefaultTextWeight     = 0.3
	memoryCandidateMultiplier   = 2.0
	memoryDefaultMMRLambda      = 0.7
	memoryTemporalDecayHalfLife = 30.0
	// Single-token queries at or below this length fall into the "short
	// query" path: FTS5 prefix matching ("you" → "you" *) instead of an
	// exact-token match. Without this, the unicode61 tokenizer treats
	// "YouTube" as one token "youtube" and a literal "you" query never
	// matches it.
	memoryShortQueryMaxLen = 4
)

// Dated memory path pattern: YYYY-MM-DD.md
var datedMemoryPathRe = regexp.MustCompile(`^(?:.*\/)?(\d{4})-(\d{2})-(\d{2})\.md$`)

// ── Search entry point ──────────────────────────────────────────────────
// Replicates openclaw's MemoryIndexManager.search():
//   - On-search sync if dirty
//   - Hybrid search (vector + FTS) when embedder available
//   - FTS-only fallback
//   - Temporal decay
//   - MMR reranking

func (s *MemoryStore) Search(ctx context.Context, query string, limit int) ([]MemorySearchResult, error) {
	if limit <= 0 {
		limit = memoryDefaultMaxResults
	}
	return s.search(ctx, query, memorySearchOptions{
		mode:           "bounded",
		resultLimit:    limit,
		candidateLimit: max(int(float64(limit)*memoryCandidateMultiplier), limit),
		applyMMR:       true,
		filterMinScore: true,
	})
}

// searchReadable bounds ordinary searches and expands only when protected
// matches consume the initial window. Source validation is cached for this
// operation, so repeated chunks and wider pages do not reread the same file.
func (s *MemoryStore) searchReadable(ctx context.Context, query string, limit int, allowRead func(string, string) bool) ([]MemorySearchResult, error) {
	if allowRead == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = memoryDefaultMaxResults
	}
	filterReads := cachedMemorySearchReadFilter(s, allowRead)
	return s.search(ctx, query, memorySearchOptions{
		mode: "private-read", resultLimit: limit,
		candidateLimit: max(limit, min(limit, math.MaxInt/10)*10),
		applyMMR:       true, filterMinScore: true,
		readFilter: func(results []MemorySearchResult) ([]MemorySearchResult, error) {
			filtered, err := filterReads(results)
			if err != nil {
				return nil, err
			}
			// Limit diversity ranking after protected candidates are excluded.
			sortByScore(filtered)
			if candidateLimit := max(limit, min(limit, math.MaxInt/2)*2); len(filtered) > candidateLimit {
				filtered = filtered[:candidateLimit]
			}
			return filtered, nil
		},
	})
}

// SearchContextCandidates returns the complete relevance-ranked candidate set
// for prompt budgeting. Unlike Search, it has no product-level result-count
// limit and does not apply MMR before corrected candidates can be removed.
func (s *MemoryStore) SearchContextCandidates(
	ctx context.Context,
	query string,
) ([]MemorySearchResult, error) {
	return s.search(ctx, query, memorySearchOptions{
		mode:         "context",
		strictErrors: true,
	})
}

func (s *MemoryStore) search(
	ctx context.Context,
	query string,
	options memorySearchOptions,
) ([]MemorySearchResult, error) {
	searchStart := time.Now()
	s.mu.Lock()
	dirty := s.dirty
	closed := s.closed
	db := s.db
	embedder := s.embedder
	s.mu.Unlock()

	if closed || db == nil {
		return nil, nil
	}
	if options.strictErrors {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("complete memory context search: %w", err)
		}
	}

	// On-search sync: if dirty, sync before searching (matching openclaw)
	if dirty {
		syncStart := time.Now()
		log.Printf("[memory] Search: dirty=true, starting Sync(\"search\")...")
		syncMemory := func() error {
			if options.contextSync {
				return s.syncContext(ctx, "search")
			}
			return s.Sync("search")
		}
		if err := syncMemory(); err != nil {
			log.Printf("[memory] on-search sync failed: %v", err)
			if options.strictErrors {
				return nil, fmt.Errorf(
					"sync complete memory context: %w",
					err,
				)
			}
		}
		log.Printf("[memory] Search: Sync(\"search\") completed in %v", time.Since(syncStart))
	} else {
		log.Printf("[memory] Search: dirty=false, skipping sync")
	}

	// Keyword/FTS search.
	//
	// Strategy:
	//   1. Strip English stop words first (so "where am i based" → "based").
	//      Natural-language queries include many stop words that are never in
	//      short memory snippets — ANDing them guarantees zero hits.
	//   2. Fall back to the raw query when stop-word stripping produces nothing
	//      (e.g. purely non-English or single-token queries).
	//   3. For single short tokens (≤ memoryShortQueryMaxLen chars) the FTS
	//      tokenizer's word-boundary rules make literal matches miss
	//      camel/concatenated forms (e.g. "you" does not match the single
	//      token "youtube" inside "YouTube"). Use FTS5 prefix matching to
	//      restore the substring-like behaviour users expect for short queries.
	//   4. Try AND first (precision); if it returns nothing, retry with OR
	//      (recall). Avoids noisy OR-by-default.
	keywords := extractKeywords(query)
	if strings.TrimSpace(keywords) == "" {
		keywords = query
	}
	tokens := ftsTokens(keywords)
	shortSingleToken := isShortSingleToken(tokens)

	// Embedding and the vector scan may not spend the keyword search's share
	// of a bounded caller budget: a busy embedder used to take all of it, and
	// the keyword search then failed at once, returning nothing at all.
	vectorCtx, cancelVector := memoryVectorContext(ctx, options.strictErrors)
	defer cancelVector()

	// Embed the query once even when protected candidates require wider pages.
	var queryVec []float64
	if embedder != nil && !shortSingleToken {
		var err error
		queryVec, err = func() ([]float64, error) {
			eCtx, cancel := context.WithTimeout(vectorCtx, embeddingQueryTimeout)
			defer cancel()
			return embedder.EmbedQuery(eCtx, query)
		}()
		if err != nil {
			log.Printf("[memory] query embedding failed, falling back to FTS-only: %v", err)
			if options.strictErrors {
				return nil, fmt.Errorf("embed complete memory context query: %w", err)
			}
			queryVec = nil
		}
	}

	candidateLimit := options.candidateLimit
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var vectorResults, keywordResults []MemorySearchResult
		if len(queryVec) > 0 {
			var err error
			vectorResults, err = s.searchVector(vectorCtx, db, queryVec, candidateLimit)
			if err != nil {
				log.Printf("[memory] vector search failed: %v", err)
				if options.strictErrors {
					return nil, fmt.Errorf("search complete memory context vectors: %w", err)
				}
			}
		}
		if shortSingleToken {
			// Single short token → prefix-FTS first so "you" matches "youtube".
			if ftsPrefix := buildFTSPrefixQuery(tokens); ftsPrefix != "" {
				var err error
				ftsStart := time.Now()
				keywordResults, err = s.searchKeyword(
					ctx,
					db,
					ftsPrefix,
					candidateLimit,
				)
				log.Printf("[memory] Search: searchKeyword(prefix) returned %d results in %v", len(keywordResults), time.Since(ftsStart))
				if err != nil {
					log.Printf("[memory] keyword prefix search failed: %v", err)
					if options.strictErrors {
						return nil, fmt.Errorf(
							"search complete memory context keywords: %w",
							err,
						)
					}
				}
			}
		} else if ftsAnd := buildFTSQueryFromTokens(tokens, "AND"); ftsAnd != "" {
			var err error
			ftsStart := time.Now()
			keywordResults, err = s.searchKeyword(
				ctx,
				db,
				ftsAnd,
				candidateLimit,
			)
			log.Printf("[memory] Search: searchKeyword(AND) returned %d results in %v", len(keywordResults), time.Since(ftsStart))
			if err != nil {
				log.Printf("[memory] keyword AND search failed: %v", err)
				if options.strictErrors {
					return nil, fmt.Errorf(
						"search complete memory context keywords: %w",
						err,
					)
				}
			}
			if len(keywordResults) == 0 {
				if ftsOr := buildFTSQueryFromTokens(tokens, "OR"); ftsOr != "" && ftsOr != ftsAnd {
					ftsOrStart := time.Now()
					keywordResults, err = s.searchKeyword(
						ctx,
						db,
						ftsOr,
						candidateLimit,
					)
					log.Printf("[memory] Search: searchKeyword(OR) returned %d results in %v", len(keywordResults), time.Since(ftsOrStart))
					if err != nil {
						log.Printf("[memory] keyword OR search failed: %v", err)
						if options.strictErrors {
							return nil, fmt.Errorf(
								"search complete memory context keywords: %w",
								err,
							)
						}
					}
				}
			}
		}

		// Merge results
		var results []MemorySearchResult
		if len(vectorResults) > 0 && len(keywordResults) > 0 {
			results = mergeHybridResults(vectorResults, keywordResults,
				memoryDefaultVectorWeight, memoryDefaultTextWeight)
		} else if len(vectorResults) > 0 {
			results = vectorResults
		} else {
			results = keywordResults
		}

		// Apply temporal decay (matching openclaw)
		results = applyTemporalDecay(results, s.baseDir, memoryTemporalDecayHalfLife)

		// Privacy filtering precedes diversity selection and the caller's result
		// limit, so protected hits cannot consume the readable result budget.
		if options.readFilter != nil {
			var err error
			results, err = options.readFilter(results)
			if err != nil {
				return nil, err
			}
		}

		readableCount := len(results)
		results, err := finalizeMemorySearchResults(
			ctx,
			results,
			len(vectorResults) > 0,
			options,
		)
		if err != nil {
			return nil, err
		}

		// Refill only when privacy exclusions leave too few readable candidates.
		// Both retrieval paths stay bounded; an exhausted page ends the search.
		if options.readFilter != nil && readableCount < options.resultLimit && candidateLimit > 0 &&
			(len(vectorResults) >= candidateLimit || len(keywordResults) >= candidateLimit) && candidateLimit <= math.MaxInt/2 {
			candidateLimit *= 2
			continue
		}

		log.Printf(
			"[memory] Search[%s]: returning %d results in %v (dirty=%v)",
			options.mode,
			len(results),
			time.Since(searchStart),
			dirty,
		)
		return results, nil
	}
}

// memorySearchKeywordReserve is the part of a bounded search budget kept for
// the keyword search, which takes milliseconds.
const memorySearchKeywordReserve = time.Second

// memoryVectorContext bounds the embedding and vector phase of a best-effort
// search so it ends memorySearchKeywordReserve before the caller's deadline.
// Strict (complete-context) searches need every phase and keep the full budget.
func memoryVectorContext(ctx context.Context, strict bool) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if strict || !ok || time.Until(deadline) <= 2*memorySearchKeywordReserve {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline.Add(-memorySearchKeywordReserve))
}

func finalizeMemorySearchResults(
	ctx context.Context,
	results []MemorySearchResult,
	vectorSearchActive bool,
	options memorySearchOptions,
) ([]MemorySearchResult, error) {
	mmrOrdered := false
	if options.applyMMR && vectorSearchActive && len(results) > 1 {
		reranked, err := applyMMRContext(ctx, results, memoryDefaultMMRLambda)
		if err != nil {
			if options.strictErrors {
				return nil, fmt.Errorf("apply memory diversity ranking: %w", err)
			}
			log.Printf(
				"[memory] Search: diversity ranking stopped; using relevance order: %v",
				err,
			)
		} else {
			results = reranked
			mmrOrdered = true
		}
	}

	if options.filterMinScore {
		minScore := memoryDefaultMinScore
		// Relax minScore for keyword-only results (matching openclaw).
		if !vectorSearchActive && minScore > memoryDefaultTextWeight {
			minScore = memoryDefaultTextWeight * 0.5
		}
		filtered := make([]MemorySearchResult, 0, len(results))
		for _, result := range results {
			if result.Score >= minScore {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}

	// A completed MMR pass is already ordered. Sorting it by relevance here
	// would erase diversity before the result budget is applied.
	if !mmrOrdered {
		sortByScore(results)
	}
	if options.resultLimit > 0 && len(results) > options.resultLimit {
		results = results[:options.resultLimit]
	}
	return results, nil
}

// ── Vector search ───────────────────────────────────────────────────────
// Fallback path (no sqlite-vec): cosine similarity in Go.
// Matches openclaw's searchVector fallback.

func (s *MemoryStore) searchVector(ctx context.Context, db *sql.DB, queryVec []float64, limit int) ([]MemorySearchResult, error) {
	model := ""
	if s.embedder != nil {
		model = s.embedder.Model()
	}

	// Load all chunks with embeddings for the current model
	rows, err := db.QueryContext(ctx, `
		SELECT id, path, source, start_line, end_line, text, embedding
		FROM memory_chunks
		WHERE model = ? AND embedding != ''`,
		model)
	if err != nil {
		return nil, fmt.Errorf("load chunks for vector search: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		result MemorySearchResult
		score  float64
	}
	var candidates []candidate

	for rows.Next() {
		var id, path, source, text, embJSON string
		var startLine, endLine int
		if err := rows.Scan(&id, &path, &source, &startLine, &endLine, &text, &embJSON); err != nil {
			return nil, err
		}

		var emb []float64
		if err := json.Unmarshal([]byte(embJSON), &emb); err != nil {
			continue
		}

		score := cosineSimilarity(queryVec, emb)
		candidates = append(candidates, candidate{
			result: MemorySearchResult{
				Path:      path,
				Source:    source,
				StartLine: startLine,
				EndLine:   endLine,
				Score:     score,
				Snippet:   truncateSnippet(text, memorySnippetMaxChars),
			},
			score: score,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort by score descending. Stable sorting preserves the database order for
	// ties while keeping complete-context discovery practical for large indexes.
	sort.SliceStable(candidates, func(left, right int) bool {
		return candidates[left].score > candidates[right].score
	})

	// Return top K
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}

	results := make([]MemorySearchResult, len(candidates))
	for i, c := range candidates {
		results[i] = c.result
	}
	return results, nil
}

// ── Keyword search (FTS5) ───────────────────────────────────────────────

func (s *MemoryStore) searchKeyword(ctx context.Context, db *sql.DB, ftsQuery string, limit int) ([]MemorySearchResult, error) {
	query := `
		SELECT id, path, source, start_line, end_line, text,
		       bm25(memory_chunks_fts) AS rank
		FROM memory_chunks_fts
		WHERE memory_chunks_fts MATCH ?
		ORDER BY rank ASC`
	args := []any{ftsQuery}
	if limit > 0 {
		query += "\nLIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	var results []MemorySearchResult
	for rows.Next() {
		var id, path, source, text string
		var startLine, endLine int
		var rank float64
		if err := rows.Scan(&id, &path, &source, &startLine, &endLine, &text, &rank); err != nil {
			return nil, fmt.Errorf("scan fts result: %w", err)
		}
		results = append(results, MemorySearchResult{
			Path:      path,
			StartLine: startLine,
			EndLine:   endLine,
			Source:    source,
			Score:     bm25RankToScore(rank),
			Snippet:   truncateSnippet(text, memorySnippetMaxChars),
		})
	}
	return results, rows.Err()
}

// ── Hybrid merge ────────────────────────────────────────────────────────
// Matches openclaw's mergeHybridResults:
//   - Merge by chunk path+line identity
//   - Weighted combination: vectorWeight * vectorScore + textWeight * textScore

func mergeHybridResults(vectorResults, keywordResults []MemorySearchResult,
	vectorWeight, textWeight float64) []MemorySearchResult {

	type mergedEntry struct {
		result      MemorySearchResult
		vectorScore float64
		textScore   float64
		hasVector   bool
		hasText     bool
	}

	// Key by path:startLine:endLine for dedup
	entryKey := func(r MemorySearchResult) string {
		return fmt.Sprintf("%s:%d:%d", r.Path, r.StartLine, r.EndLine)
	}

	merged := make(map[string]*mergedEntry)

	for _, r := range vectorResults {
		k := entryKey(r)
		merged[k] = &mergedEntry{
			result:      r,
			vectorScore: r.Score,
			hasVector:   true,
		}
	}

	for _, r := range keywordResults {
		k := entryKey(r)
		if entry, exists := merged[k]; exists {
			entry.textScore = r.Score
			entry.hasText = true
		} else {
			merged[k] = &mergedEntry{
				result:    r,
				textScore: r.Score,
				hasText:   true,
			}
		}
	}

	// Scoring policy:
	//   - Both signals present: weighted sum (rewards dual-agreement)
	//   - Vector-only: use vectorScore as-is
	//   - Text-only:   use textScore as-is
	//
	// The previous implementation always applied the weighted sum, which meant
	// text-only hits were capped at `textWeight` (0.3) — below the 0.35 min-score
	// filter, so they were always dropped. Using the single-signal score keeps
	// those hits in [0, 1] and lets the downstream filter operate on real values.
	var results []MemorySearchResult
	for _, entry := range merged {
		switch {
		case entry.hasVector && entry.hasText:
			entry.result.Score = vectorWeight*entry.vectorScore + textWeight*entry.textScore
		case entry.hasVector:
			entry.result.Score = entry.vectorScore
		default:
			entry.result.Score = entry.textScore
		}
		results = append(results, entry.result)
	}

	sortByScore(results)
	return results
}

// ── MMR (Max Marginal Relevance) ────────────────────────────────────────
// Replicates openclaw's mmr.ts:
//   - Greedy iterative selection
//   - Jaccard similarity for diversity
//   - Score normalization to [0, 1]

func applyMMR(results []MemorySearchResult, lambda float64) []MemorySearchResult {
	reranked, err := applyMMRContext(context.Background(), results, lambda)
	if err != nil {
		fallback := append([]MemorySearchResult(nil), results...)
		sortByScore(fallback)
		return fallback
	}
	return reranked
}

func applyMMRContext(
	ctx context.Context,
	results []MemorySearchResult,
	lambda float64,
) ([]MemorySearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Establish deterministic relevance and tie ordering before greedy
	// selection. Hybrid merge assembly may otherwise inherit map iteration
	// order for equal-score candidates.
	ordered := append([]MemorySearchResult(nil), results...)
	sortByScore(ordered)
	if len(ordered) <= 1 {
		return ordered, nil
	}

	// Clamp lambda to [0, 1]
	if lambda < 0 {
		lambda = 0
	}
	if lambda > 1 {
		lambda = 1
	}

	// Pure relevance: just sort by score
	if lambda == 1 {
		return ordered, nil
	}

	// Precompute token sets for each result
	tokenSets := make([]map[string]bool, len(ordered))
	for i, r := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tokenSets[i] = tokenize(r.Snippet)
	}

	// Normalize scores to [0, 1]
	minScore, maxScore := ordered[0].Score, ordered[0].Score
	for _, r := range ordered[1:] {
		if r.Score < minScore {
			minScore = r.Score
		}
		if r.Score > maxScore {
			maxScore = r.Score
		}
	}
	scoreRange := maxScore - minScore
	if scoreRange <= 0 {
		scoreRange = 1
	}

	normalizedScores := make([]float64, len(ordered))
	for i, r := range ordered {
		normalizedScores[i] = (r.Score - minScore) / scoreRange
	}

	// Greedy MMR selection. maxSimilarities caches each remaining candidate's
	// maximum similarity to the selected set. Each pair is compared once,
	// reducing the ranking pass from repeated cubic work to quadratic work.
	selected := make([]int, 0, len(ordered))
	isSelected := make([]bool, len(ordered))
	maxSimilarities := make([]float64, len(ordered))

	for len(selected) < len(ordered) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bestIdx := -1
		bestMMR := math.Inf(-1)

		for idx := range ordered {
			if isSelected[idx] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			relevance := normalizedScores[idx]
			mmrScore := lambda*relevance - (1-lambda)*maxSimilarities[idx]

			if bestIdx < 0 || mmrScore > bestMMR {
				bestMMR = mmrScore
				bestIdx = idx
			}
		}

		if bestIdx < 0 {
			break
		}
		selected = append(selected, bestIdx)
		isSelected[bestIdx] = true

		for idx := range ordered {
			if isSelected[idx] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			similarity := memoryJaccardSimilarity(tokenSets[idx], tokenSets[bestIdx])
			if similarity > maxSimilarities[idx] {
				maxSimilarities[idx] = similarity
			}
		}
	}

	reranked := make([]MemorySearchResult, len(selected))
	for i, idx := range selected {
		reranked[i] = ordered[idx]
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return reranked, nil
}

// tokenize extracts lowercase word tokens from text.
// Matches openclaw's mmr.ts tokenize.
func tokenize(text string) map[string]bool {
	tokens := make(map[string]bool)
	var word strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			word.WriteRune(r)
		} else {
			if word.Len() > 0 {
				tokens[word.String()] = true
				word.Reset()
			}
		}
	}
	if word.Len() > 0 {
		tokens[word.String()] = true
	}
	return tokens
}

// jaccardSimilarity computes Jaccard similarity between two token sets.
// Matches openclaw's jaccardSimilarity.
func memoryJaccardSimilarity(setA, setB map[string]bool) float64 {
	if len(setA) == 0 && len(setB) == 0 {
		return 1
	}
	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}

	intersection := 0
	for token := range setA {
		if setB[token] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// ── Temporal decay ──────────────────────────────────────────────────────
// Replicates openclaw's temporal-decay.ts exactly.

func toDecayLambda(halfLifeDays float64) float64 {
	if halfLifeDays <= 0 {
		return 0
	}
	return math.Ln2 / halfLifeDays
}

func calculateTemporalDecayMultiplier(ageInDays, halfLifeDays float64) float64 {
	lambda := toDecayLambda(halfLifeDays)
	if lambda <= 0 {
		return 1
	}
	return math.Exp(-lambda * math.Max(0, ageInDays))
}

// isEvergreenMemoryPath returns true for files that should never decay.
// Matches openclaw: MEMORY.md and non-dated files are evergreen.
func isEvergreenMemoryPath(relPath string) bool {
	relPath = filepath.ToSlash(relPath)
	if relPath == "MEMORY.md" || relPath == "memory.md" {
		return true
	}
	// Non-dated files in memory dir are evergreen
	if !strings.Contains(relPath, "/") {
		return !datedMemoryPathRe.MatchString(relPath)
	}
	return false
}

// extractTimestamp extracts a date from the file path or modification time.
func extractTimestamp(relPath, baseDir string) *time.Time {
	m := datedMemoryPathRe.FindStringSubmatch(relPath)
	if m != nil {
		t, err := time.Parse("2006-01-02", m[1]+"-"+m[2]+"-"+m[3])
		if err == nil {
			return &t
		}
	}

	if isEvergreenMemoryPath(relPath) {
		return nil
	}

	absPath := filepath.Join(baseDir, relPath)
	info, err := os.Stat(absPath)
	if err != nil {
		return nil
	}
	t := info.ModTime()
	return &t
}

// applyTemporalDecay applies temporal decay to search results.
func applyTemporalDecay(results []MemorySearchResult, baseDir string, halfLifeDays float64) []MemorySearchResult {
	if halfLifeDays <= 0 || len(results) == 0 {
		return results
	}

	now := time.Now()
	dayMs := 24.0 * 60 * 60 * 1000

	for i := range results {
		// Knowledge files are always evergreen — no temporal decay.
		if results[i].Source == string(MemorySourceKnowledge) {
			continue
		}
		ts := extractTimestamp(results[i].Path, baseDir)
		if ts == nil {
			continue // evergreen, no decay
		}
		ageMs := float64(now.UnixMilli() - ts.UnixMilli())
		ageInDays := math.Max(0, ageMs/dayMs)
		results[i].Score *= calculateTemporalDecayMultiplier(ageInDays, halfLifeDays)
	}

	return results
}

// ── FTS query building ──────────────────────────────────────────────────
// Matches openclaw's buildFtsQuery.

// buildFTSQuery builds an FTS5 MATCH expression by quoting each token and
// joining them with AND (precision-first). Callers wanting recall-first
// should use buildFTSQueryWithOp(raw, "OR").
func buildFTSQuery(raw string) string {
	return buildFTSQueryWithOp(raw, "AND")
}

// buildFTSQueryWithOp tokenises `raw` (Unicode-aware, letter/digit/underscore
// runs) and joins the quoted tokens with the given FTS5 operator.
// Returns "" when `raw` has no tokens.
func buildFTSQueryWithOp(raw, operator string) string {
	return buildFTSQueryFromTokens(ftsTokens(raw), operator)
}

// ftsTokens splits `raw` into FTS-style word tokens (letter / digit /
// underscore runs, Unicode-aware). Exposed so callers can both build queries
// and inspect token shape (e.g. "is this a single short token?") without
// re-tokenising.
func ftsTokens(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var words []string
	var word strings.Builder
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			word.WriteRune(r)
		} else {
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
		}
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words
}

// buildFTSQueryFromTokens joins already-tokenised words with the given FTS5
// operator, quoting each token. Returns "" for empty input.
func buildFTSQueryFromTokens(tokens []string, operator string) string {
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens))
	for _, w := range tokens {
		w = strings.ReplaceAll(w, `"`, `""`)
		parts = append(parts, fmt.Sprintf(`"%s"`, w))
	}
	return strings.Join(parts, " "+operator+" ")
}

// buildFTSPrefixQuery builds an FTS5 prefix query from `tokens`. Each token
// becomes `"token" *`, and tokens are joined with AND (precision-first).
// FTS5's prefix syntax matches any indexed token starting with the prefix,
// which is what lets "you" hit the single token "youtube" produced by the
// unicode61 tokenizer for the camel-cased word "YouTube".
//
// Note the literal space between the closing quote and the `*` — FTS5 parses
// `"token"*` as a phrase column filter, not a prefix. The space form
// (`"token" *`) is the documented prefix-token syntax.
func buildFTSPrefixQuery(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens))
	for _, w := range tokens {
		w = strings.ReplaceAll(w, `"`, `""`)
		parts = append(parts, fmt.Sprintf(`"%s" *`, w))
	}
	return strings.Join(parts, " AND ")
}

// isShortSingleToken reports whether the keyword set boils down to one
// short word — the case where a literal FTS match is too restrictive and we
// should fall back to prefix matching.
func isShortSingleToken(tokens []string) bool {
	if len(tokens) != 1 {
		return false
	}
	// Use rune count, not byte length: a single CJK character is 3 bytes but
	// 1 rune, and we don't want to over-prefix short multi-byte tokens.
	runes := utf8.RuneCountInString(tokens[0])
	return runes > 0 && runes <= memoryShortQueryMaxLen
}

// ── Keyword extraction (stop word removal for FTS-only mode) ────────────
// Matches openclaw's extractKeywords / query-expansion.ts.

var englishStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "am": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "shall": true, "should": true,
	"may": true, "might": true, "can": true, "could": true, "must": true,
	"i": true, "me": true, "my": true, "mine": true, "myself": true,
	"we": true, "us": true, "our": true, "ours": true, "ourselves": true,
	"you": true, "your": true, "yours": true, "yourself": true, "yourselves": true,
	"he": true, "him": true, "his": true, "himself": true,
	"she": true, "her": true, "hers": true, "herself": true,
	"it": true, "its": true, "itself": true,
	"they": true, "them": true, "their": true, "theirs": true, "themselves": true,
	"this": true, "that": true, "these": true, "those": true,
	"what": true, "which": true, "who": true, "whom": true, "whose": true,
	"when": true, "where": true, "why": true, "how": true,
	"not": true, "no": true, "nor": true, "or": true, "and": true,
	"but": true, "if": true, "then": true, "else": true, "than": true,
	"too": true, "very": true, "just": true, "about": true,
	"of": true, "in": true, "on": true, "at": true, "to": true,
	"for": true, "with": true, "by": true, "from": true,
	"up": true, "out": true, "into": true, "over": true,
	"after": true, "before": true, "between": true, "under": true,
	"above": true, "below": true, "through": true, "during": true,
	"so": true, "as": true, "like": true, "also": true, "more": true,
	"some": true, "any": true, "all": true, "each": true, "every": true,
	"much": true, "many": true, "own": true, "other": true,
	"thing": true, "things": true, "something": true, "anything": true,
	"everything": true, "nothing": true,
}

// extractKeywords removes stop words from a query for better FTS matching.
func extractKeywords(query string) string {
	words := strings.Fields(strings.ToLower(query))
	var keywords []string
	for _, w := range words {
		// Strip punctuation from edges
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		if w == "" || englishStopWords[w] {
			continue
		}
		keywords = append(keywords, w)
	}
	return strings.Join(keywords, " ")
}

// ── Score helpers ────────────────────────────────────────────────────────

// bm25RankToScore converts SQLite FTS5 bm25() rank into a [0, 1) relevance
// score (higher = more relevant).
//
// SQLite FTS5's bm25() returns a negative float for matching rows — more
// negative means more relevant. The previous implementation did
// `math.Max(0, rank)`, which collapsed every negative rank to 0 and then
// returned `1/(1+0) = 1.0`, erasing all BM25 ordering signal. Every FTS hit
// got the same score and downstream ranking became effectively random.
//
// Correct mapping: treat `-rank` as positive "relevance strength" and squash
// it into [0, 1) via `s = x / (1 + x)`. This preserves monotonicity
// (more-negative rank → higher score) and handles pathological values.
func bm25RankToScore(rank float64) float64 {
	if math.IsNaN(rank) || math.IsInf(rank, 0) {
		return 0
	}
	// Non-negative ranks in FTS5 are not expected for matches — treat as no signal.
	if rank >= 0 {
		return 0
	}
	strength := -rank
	return strength / (1.0 + strength)
}

func truncateSnippet(text string, maxLen int) string {
	// A cut through a credential can remove the pattern that identifies it.
	// Redact the complete chunk before enforcing the outgoing snippet limit.
	text, _ = pii.RedactText(text)
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}

func sortByScore(results []MemorySearchResult) {
	sort.SliceStable(results, func(left, right int) bool {
		leftResult := results[left]
		rightResult := results[right]
		if leftResult.Score != rightResult.Score {
			return leftResult.Score > rightResult.Score
		}
		if leftResult.Path != rightResult.Path {
			return leftResult.Path < rightResult.Path
		}
		if leftResult.Source != rightResult.Source {
			return leftResult.Source < rightResult.Source
		}
		if leftResult.StartLine != rightResult.StartLine {
			return leftResult.StartLine < rightResult.StartLine
		}
		if leftResult.EndLine != rightResult.EndLine {
			return leftResult.EndLine < rightResult.EndLine
		}
		return leftResult.Snippet < rightResult.Snippet
	})
}
