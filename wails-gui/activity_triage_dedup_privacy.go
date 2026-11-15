package main

import (
	"context"
	"log"
	"strings"
	"time"

	"wails-gui/internal/pii"
)

func (a *App) loadRelevantMemorySummaryWithBudget(store *MemoryStore, query string, maxChars int) string {
	return a.loadModelDedupSummary(store, query, maxChars, MemorySourceMemory)
}

func (a *App) loadRelevantKnowledgeSummaryWithBudget(store *MemoryStore, query string, maxChars int) string {
	return a.loadModelDedupSummary(store, query, maxChars, MemorySourceKnowledge)
}

func (a *App) loadExistingMemorySummaryWithBudget(maxChars int) string {
	return a.loadModelDedupSummary(nil, "", maxChars, MemorySourceMemory)
}

func (a *App) loadExistingKnowledgeSummaryWithBudget(maxChars int) string {
	return a.loadModelDedupSummary(nil, "", maxChars, MemorySourceKnowledge)
}

// Entry-count callers keep their legacy selection budget. Privacy is checked
// before an entry consumes a slot, and the seed writer applies its byte cap.
func (a *App) loadRelevantMemorySummary(store *MemoryStore, query string, maxEntries int) string {
	return a.loadModelDedupSummaryWithLimits(store, query, 0, maxEntries, MemorySourceMemory)
}

func (a *App) loadRelevantKnowledgeSummary(store *MemoryStore, query string, maxEntries int) string {
	return a.loadModelDedupSummaryWithLimits(store, query, 0, maxEntries, MemorySourceKnowledge)
}

// Keep relevance ordering and recency fallback, but authorize each complete
// source before its title or bullets consume any of the model's context budget.
func (a *App) loadModelDedupSummary(store *MemoryStore, query string, maxChars int, source MemorySource) string {
	return a.loadModelDedupSummaryWithLimits(store, query, maxChars, 0, source)
}

func (a *App) loadModelDedupSummaryWithLimits(store *MemoryStore, query string, maxChars, maxEntries int, source MemorySource) string {
	return a.loadModelDedupSummaryWithCapture(store, query, maxChars, maxEntries, source, nil)
}

// Capture the exact source snapshot only when its rendered text contributes to
// the result. Authoring callers retain these dependencies separately from the
// redacted prompt so later edits cannot lend authority to a stale seed.
func (a *App) loadModelDedupSummaryWithCapture(store *MemoryStore, query string, maxChars, maxEntries int, source MemorySource, onSelected func(id, content string)) string {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return ""
	}
	readAllowed, err := a.memoryReadFilter()
	if err != nil {
		log.Printf("[triager] dedup privacy lookup failed: %v", err)
		return ""
	}
	selected := 0
	allowRead := func(id, content string) bool {
		if maxEntries > 0 && selected >= maxEntries {
			return false
		}
		if !readAllowed(id, content) {
			return false
		}
		// A hidden file is never offered as an update target: new evidence
		// would land where no page shows it.
		return !learnedHidden(content)
	}
	emitted := func(id, content string) {
		selected++
		if onSelected != nil {
			onSelected(id, content)
		}
	}
	fallback := func() string {
		if source == MemorySourceKnowledge && maxEntries > 0 && strings.TrimSpace(query) != "" {
			return a.loadRelevantKnowledgeSummaryFallbackWithCapture(query, maxChars, maxEntries, emitted)
		}
		var dir string
		var err error
		switch source {
		case MemorySourceKnowledge:
			dir, err = knowledgeDir()
		default:
			dir, err = memoryDir()
		}
		if err != nil {
			return ""
		}
		return loadDedupDirectorySummaryWithReadCapture(dir, maxChars, allowRead, emitted)
	}
	if store == nil || strings.TrimSpace(query) == "" {
		return fallback()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	query, _ = pii.RedactText(query)
	results, err := store.Search(ctx, query, dedupRecallCandidateScanLimit)
	if err != nil || len(results) == 0 {
		if err != nil {
			log.Printf("[triager] dedup recall unavailable, using filtered directory scan: %v", err)
		}
		return fallback()
	}
	out := dedupFromResultsWithReadCapture(results, source, maxChars, func(path string) string {
		return store.resolveAbsPath(path, source)
	}, allowRead, emitted)
	if out == "" {
		return fallback()
	}
	return out
}
