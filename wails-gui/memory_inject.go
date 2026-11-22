package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	memoryInjectMinScore           = 0.35
	memoryInjectContextBudgetChars = 48_000
	memoryInjectBudgetMarker       = "\n... (context budget reached)"
)

type memoryContextSelection struct {
	Results            []MemorySearchResult
	CandidateCount     int
	EmptyCount         int
	BelowScoreCount    int
	SelectedCount      int
	SelectedChars      int
	TruncatedCount     int
	BudgetOmittedCount int
}

type memoryContextResolution struct {
	Store               *MemoryStore
	Selection           memoryContextSelection
	DiscoveredCount     int
	CorrectedExclusions int
}

// memoryDimensionSectionOrder controls the ordering of dimension subgroups in
// the <memory-context> block. Recognized dimensions come first (in this order),
// followed by "custom" user-authored memories, and finally a "General" bucket
// for memories without a dimension tag.
var memoryDimensionSectionOrder = []struct {
	Key   string
	Title string
}{
	{"user_insight", "User Insights"},
	{"topic_highlight", "Topic Highlights"},
	{"deep_user_model", "Deep User Model"},
	{"agentic_memory", "AI Operational Memory"},
	{"custom", "Custom"},
	{"", "General"},
}

func buildMemoryContextSelection(
	results []MemorySearchResult,
	minScore float64,
	maxChars int,
) memoryContextSelection {
	selection := memoryContextSelection{
		Results:        []MemorySearchResult{},
		CandidateCount: len(results),
	}
	eligible := make([]MemorySearchResult, 0, len(results))
	for _, result := range results {
		if result.Score < minScore {
			selection.BelowScoreCount++
			continue
		}
		result.Snippet = strings.TrimSpace(result.Snippet)
		if result.Snippet == "" {
			selection.EmptyCount++
			continue
		}
		eligible = append(eligible, result)
	}

	selection.Results = make([]MemorySearchResult, 0, len(eligible))
	for index, result := range eligible {
		remaining := maxChars - selection.SelectedChars
		if remaining <= 0 {
			selection.BudgetOmittedCount += len(eligible) - index
			break
		}
		if len(result.Snippet) > remaining {
			contentBudget := remaining - len(memoryInjectBudgetMarker)
			if contentBudget <= 0 {
				selection.BudgetOmittedCount += len(eligible) - index
				break
			}
			snippet := strings.TrimSpace(
				truncateAtRuneBoundary(result.Snippet, contentBudget),
			)
			if snippet == "" {
				selection.BudgetOmittedCount += len(eligible) - index
				break
			}
			result.Snippet = snippet + memoryInjectBudgetMarker
			selection.TruncatedCount++
			selection.BudgetOmittedCount += len(eligible) - index - 1
		}
		selection.Results = append(selection.Results, result)
		selection.SelectedChars += len(result.Snippet)
		if selection.TruncatedCount > 0 {
			break
		}
	}
	selection.SelectedCount = len(selection.Results)
	return selection
}

func selectMemoryContextResults(
	results []MemorySearchResult,
	minScore float64,
	maxChars int,
) []MemorySearchResult {
	return buildMemoryContextSelection(results, minScore, maxChars).Results
}

func filterCorrectedAgencyLearningContext(
	results []MemorySearchResult,
	suppressed map[string]struct{},
) []MemorySearchResult {
	if len(suppressed) == 0 {
		return results
	}
	filtered := make([]MemorySearchResult, 0, len(results))
	for _, result := range results {
		id := strings.TrimSuffix(filepath.Base(strings.TrimSpace(result.Path)), filepath.Ext(result.Path))
		if _, blocked := suppressed[id]; blocked {
			continue
		}
		filtered = append(filtered, result)
	}
	return filtered
}

// applyMemoryUserControls runs only at the injection boundary. SearchMemory
// keeps its existing read contract. Read current files so a stale indexed
// chunk cannot override an edit or resurrect a removed record.
func applyMemoryUserControls(dir string, results []MemorySearchResult, allowRead ...func(string, string) bool) ([]MemorySearchResult, error) {
	type currentMemory struct {
		content string
		title   string
		edited  bool
		blocked bool
	}
	current := make(map[string]currentMemory)
	editedTitles := make(map[string]bool)
	relevantObservedTitles := make(map[string]bool)
	for _, result := range results {
		if MemorySource(result.Source) != MemorySourceMemory {
			continue
		}
		if _, seen := current[result.Path]; seen {
			continue
		}
		// The index also supports nested memory directories. Keep valid
		// relative paths while rejecting traversal outside the memory root.
		if !filepath.IsLocal(filepath.FromSlash(result.Path)) {
			current[result.Path] = currentMemory{blocked: true}
			continue
		}
		data, err := readMemoryBody(filepath.Join(dir, result.Path))
		if os.IsNotExist(err) {
			current[result.Path] = currentMemory{blocked: true}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read memory for injection: %w", err)
		}
		content := string(data)
		id := strings.TrimSuffix(filepath.Base(result.Path), ".md")
		// Authorize the exact refreshed body, not the earlier indexed file.
		// An explicit edit can add protected dependencies during retrieval.
		if len(allowRead) > 0 && (allowRead[0] == nil || !allowRead[0](id, content)) {
			current[result.Path] = currentMemory{blocked: true}
			continue
		}
		forgotten, err := isMemoryForgotten(dir, id, content, time.Now())
		if err != nil {
			return nil, err
		}
		memory := currentMemory{
			content: content,
			title:   normalizeMemoryContent(extractTitle(content)),
			edited:  isUserEditedMemory(content),
			blocked: forgotten,
		}
		current[result.Path] = memory
		if memory.edited && !memory.blocked && memory.title != "" {
			editedTitles[memory.title] = true
		}
	}
	for _, result := range results {
		memory, exists := current[result.Path]
		if MemorySource(result.Source) == MemorySourceMemory && exists && !memory.blocked &&
			!memory.edited && memory.title != "" && result.Score >= memoryInjectMinScore {
			relevantObservedTitles[memory.title] = true
		}
	}
	filtered := make([]MemorySearchResult, 0, len(results))
	emittedEdits := make(map[string]bool)
	for _, result := range results {
		if MemorySource(result.Source) != MemorySourceMemory {
			filtered = append(filtered, result)
			continue
		}
		memory := current[result.Path]
		if memory.blocked || (!memory.edited && editedTitles[memory.title]) {
			continue
		}
		if memory.edited {
			if emittedEdits[result.Path] {
				continue
			}
			emittedEdits[result.Path] = true
			result.Snippet = memory.content
			result.StartLine = 1
			result.EndLine = strings.Count(memory.content, "\n") + 1
			// A correction may remove the exact words that made the old
			// observation relevant. Keep that correction when it displaces
			// a relevant observation of the same named subject.
			if relevantObservedTitles[memory.title] {
				result.Score = max(result.Score, memoryInjectMinScore)
			}
		}
		filtered = append(filtered, result)
	}
	// A relevant explicit edit gets the context budget before observations,
	// even when an observed record has a higher similarity score.
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		aEdited := MemorySource(a.Source) == MemorySourceMemory && current[a.Path].edited
		bEdited := MemorySource(b.Source) == MemorySourceMemory && current[b.Path].edited
		return aEdited && !bEdited
	})
	return filtered, nil
}

func (a *App) resolveMemoryContext(
	ctx context.Context,
	query string,
	validators ...func(*MemoryStore, []MemorySearchResult) error,
) (memoryContextResolution, error) {
	return a.resolveFilteredMemoryContext(ctx, query, nil, validators...)
}

func (a *App) resolveFilteredMemoryContext(ctx context.Context, query string, filter func(*MemoryStore, []MemorySearchResult) ([]MemorySearchResult, error), validators ...func(*MemoryStore, []MemorySearchResult) error) (memoryContextResolution, error) {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return memoryContextResolution{}, nil
	}
	a.mu.Lock()
	store := a.memoryStore
	a.mu.Unlock()

	resolution := memoryContextResolution{Store: store}
	if store == nil {
		return resolution, nil
	}
	// Context retrieval has no result-count cap, but every matching source
	// must pass the same current privacy policy as other model memory reads.
	allowRead := func(_ string, content string) bool {
		return a.memorySourceAllowsRead(content)
	}
	filterReads := cachedMemorySearchReadFilter(store, allowRead)
	results, err := store.search(ctx, query, memorySearchOptions{
		mode: "context", strictErrors: true, readFilter: filterReads, contextSync: len(validators) > 0,
	})
	if err != nil {
		return memoryContextResolution{}, err
	}
	resolution.DiscoveredCount = len(results)
	if a.activity != nil && a.activity.store != nil {
		suppressed, suppressionErr := a.activity.store.
			ListCorrectedAgencyLearningTargetIDsContext(ctx)
		if suppressionErr != nil {
			return memoryContextResolution{}, fmt.Errorf("read memory corrections: %w", suppressionErr)
		} else {
			before := len(results)
			results = filterCorrectedAgencyLearningContext(results, suppressed)
			resolution.CorrectedExclusions = before - len(results)
		}
	}
	before := len(results)
	results, err = applyMemoryUserControls(store.baseDir, results, allowRead)
	if err != nil {
		return memoryContextResolution{}, err
	}
	resolution.CorrectedExclusions += before - len(results)
	// Explicit edits replace indexed snippets with the current file body.
	// Reapply redaction after that refresh and before prompt budgeting.
	results, err = cachedMemorySearchReadFilter(store, allowRead)(results)
	if err != nil {
		return memoryContextResolution{}, err
	}
	for _, validate := range validators {
		if err := validate(store, results); err != nil {
			return memoryContextResolution{}, err
		}
	}
	if filter != nil {
		results, err = filter(store, results)
		if err != nil {
			return memoryContextResolution{}, err
		}
	}
	resolution.Selection = buildMemoryContextSelection(
		results,
		memoryInjectMinScore,
		memoryInjectContextBudgetChars,
	)
	return resolution, nil
}

func memoryContextSelectionReceipt(
	resolution memoryContextResolution,
) string {
	selection := resolution.Selection
	return fmt.Sprintf(
		"<memory-context-selection discovered=\"%d\" corrected-excluded=\"%d\" selected=\"%d\" selected-chars=\"%d\" character-budget=\"%d\" below-relevance=\"%d\" empty=\"%d\" truncated=\"%d\" budget-omitted=\"%d\" />\n\n",
		resolution.DiscoveredCount,
		resolution.CorrectedExclusions,
		selection.SelectedCount,
		selection.SelectedChars,
		memoryInjectContextBudgetChars,
		selection.BelowScoreCount,
		selection.EmptyCount,
		selection.TruncatedCount,
		selection.BudgetOmittedCount,
	)
}

// injectMemoryContext searches the memory store for content relevant to the
// user's prompt and prepends matching snippets as context.
//
// The context is added to the USER message (not the system prompt) so that the
// cached prefix (system prompt + prior conversation turns) remains stable and
// the prompt cache is NOT invalidated.
//
// query is the RAW user prompt (no inject pollution) — use this for the
// vector search so the embedding model operates on a short focused
// string instead of a 10-30 KB composed blob. prompt is whatever the
// caller wants decorated; memory snippets are prepended to it.
func (a *App) injectMemoryContext(ctx context.Context, prompt, query string, validators ...func(*MemoryStore, []MemorySearchResult) error) string {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return prompt
	}
	q := strings.TrimSpace(query)
	if q == "" {
		q = strings.TrimSpace(prompt)
	}
	selfModelContext := a.selfModelContext(q)

	useEpoch := a.contextUseEpoch()
	searchStart := time.Now()
	resolution, err := a.resolveMemoryContext(ctx, q, validators...)
	if err != nil {
		log.Printf("[memory] inject search failed: %v", err)
		return selfModelContext + prompt
	}
	if resolution.Store == nil {
		return selfModelContext + prompt
	}
	log.Printf(
		"[memory] inject context: discovered=%d corrected=%d selected=%d chars=%d below_score=%d empty=%d truncated=%d budget_omitted=%d duration=%v",
		resolution.DiscoveredCount,
		resolution.CorrectedExclusions,
		resolution.Selection.SelectedCount,
		resolution.Selection.SelectedChars,
		resolution.Selection.BelowScoreCount,
		resolution.Selection.EmptyCount,
		resolution.Selection.TruncatedCount,
		resolution.Selection.BudgetOmittedCount,
		time.Since(searchStart),
	)
	return selfModelContext + a.renderMemorySelection(ctx, resolution, useEpoch) + prompt
}

func (a *App) renderMemorySelection(ctx context.Context, resolution memoryContextResolution, useEpoch uint64) string {
	selection := resolution.Selection
	selected := selection.Results
	if len(selected) == 0 {
		return ""
	}

	var memoryResults, knowledgeResults []MemorySearchResult
	for _, r := range selected {
		switch MemorySource(r.Source) {
		case MemorySourceKnowledge:
			knowledgeResults = append(knowledgeResults, r)
		default:
			memoryResults = append(memoryResults, r)
		}
	}

	used := append([]MemorySearchResult{}, knowledgeResults...)
	var sb strings.Builder
	sb.WriteString(memoryContextSelectionReceipt(resolution))

	// Knowledge context includes the full matched chunks. Paths remain available
	// when an agent needs the rest of the source file.
	if len(knowledgeResults) > 0 {
		sb.WriteString("<knowledge-context>\n")
		sb.WriteString("The following relevant knowledge chunks are available. Use the source path when more surrounding content is needed.\n\n")
		for i, r := range knowledgeResults {
			absPath := resolution.Store.resolveAbsPath(r.Path, MemorySourceKnowledge)
			sb.WriteString(fmt.Sprintf("--- Knowledge %d (from %s, score=%.2f) ---\n", i+1, absPath, r.Score))
			sb.WriteString(strings.TrimSpace(r.Snippet))
			sb.WriteString("\n\n")
		}
		sb.WriteString("\n</knowledge-context>\n\n")
	}

	// Memory context: snippet mode (full chunk text), subgrouped by dimension.
	if len(memoryResults) > 0 {
		sb.WriteString("<memory-context>\n")
		sb.WriteString("The following are relevant notes from your memory. Entries labelled attribution=user_edit are explicit user statements and take precedence over conflicting observed or inferred notes.\n\n")

		// Subgroup by dimension. For each search result, look up the file's
		// dimension header (one tiny read per file) and bucket accordingly.
		buckets := make(map[string][]MemorySearchResult)
		dimensionByPath := make(map[string]string)
		for _, r := range memoryResults {
			dim, cached := dimensionByPath[r.Path]
			if !cached {
				dim = ReadMemoryFileDimension(r.Path)
				dimensionByPath[r.Path] = dim
			}
			buckets[dim] = append(buckets[dim], r)
		}

		memIdx := 0
		for _, section := range memoryDimensionSectionOrder {
			group := buckets[section.Key]
			if len(group) == 0 {
				continue
			}
			sb.WriteString(fmt.Sprintf("## %s\n\n", section.Title))
			for _, r := range group {
				snippet := strings.TrimSpace(r.Snippet)
				if snippet == "" {
					continue
				}
				memIdx++
				used = append(used, r)
				attribution := "observed"
				if isUserEditedMemory(r.Snippet) {
					attribution = "user_edit"
				}
				// The ref is what CiteSources accepts (chat_sources.go).
				sb.WriteString(fmt.Sprintf("--- Memory %d (from %s, ref=memory:%s, score=%.2f, attribution=%s) ---\n", memIdx, r.Path, memoryIDFromPath(r.Path), r.Score, attribution))
				sb.WriteString(snippet)
				sb.WriteString("\n\n")
			}
		}
		sb.WriteString("</memory-context>\n\n")
	}

	a.recordMemorySelectionUses(ctx, used, useEpoch)
	return sb.String()
}

// memoryBodyCache keeps the bytes of memory files read by the injection path, keyed on
// size and modification time: any edit changes one of them, so the next read misses.
// Only bytes are cached. Authorization still runs on every read, so a forget or a
// sensitive-app change takes effect at once (R24, 2026-11-05: 20 s of ReadFile).
var memoryBodyCache = struct {
	sync.Mutex
	entries map[string]memoryBodyEntry
}{entries: map[string]memoryBodyEntry{}}

type memoryBodyEntry struct {
	size int64
	mod  int64
	data []byte
}

const memoryBodyCacheLimit = 512

func readMemoryBody(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	size, mod := info.Size(), info.ModTime().UnixNano()
	memoryBodyCache.Lock()
	if e, ok := memoryBodyCache.entries[path]; ok && e.size == size && e.mod == mod {
		memoryBodyCache.Unlock()
		return append([]byte(nil), e.data...), nil
	}
	memoryBodyCache.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	memoryBodyCache.Lock()
	if len(memoryBodyCache.entries) >= memoryBodyCacheLimit {
		memoryBodyCache.entries = map[string]memoryBodyEntry{}
	}
	memoryBodyCache.entries[path] = memoryBodyEntry{size: size, mod: mod, data: append([]byte(nil), data...)}
	memoryBodyCache.Unlock()
	return data, nil
}
