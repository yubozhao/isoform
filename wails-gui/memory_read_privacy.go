package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
)

// filterMemorySearchReads uses full source provenance, because a matching
// chunk can be far below its file's Apps and Activity Session metadata.
func (a *App) filterMemorySearchReads(store *MemoryStore, results []MemorySearchResult) ([]MemorySearchResult, error) {
	allowRead, err := a.memoryReadFilter()
	if err != nil {
		return nil, err
	}
	return cachedMemorySearchReadFilter(store, allowRead)(results)
}

func cachedMemorySearchReadFilter(store *MemoryStore, allowRead func(string, string) bool) func([]MemorySearchResult) ([]MemorySearchResult, error) {
	allowedPaths := map[string]bool{}
	return func(results []MemorySearchResult) ([]MemorySearchResult, error) {
		if allowRead == nil {
			return nil, nil
		}
		filtered := make([]MemorySearchResult, 0, len(results))
		for _, result := range results {
			path := store.resolveAbsPath(result.Path, MemorySource(result.Source))
			allowed, seen := allowedPaths[path]
			if !seen {
				content, err := os.ReadFile(path)
				id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
				allowed = err == nil && allowRead(id, string(content))
				allowedPaths[path] = allowed
			}
			if !allowed {
				continue
			}
			result.Snippet, _ = pii.RedactText(result.Snippet)
			filtered = append(filtered, result)
		}
		return filtered, nil
	}
}

// Build one current read policy per operation. The local Memory page retains
// its direct access; model and CLI readers must authorize each full source.
func (a *App) memoryReadFilter() (func(string, string) bool, error) {
	return a.memoryReadFilterContext(context.Background())
}

func (a *App) memoryReadFilterContext(ctx context.Context) (func(string, string) bool, error) {
	return a.memoryReadFilterWithDependencyContext(ctx, nil)
}

// memoryReadFilterWithDependencyContext is memoryReadFilterContext with the
// activity-session check supplied by the caller, so one read can check each
// session a set of notes shares once. nil checks every session again.
func (a *App) memoryReadFilterWithDependencyContext(ctx context.Context, allowsDependency func(string, []string) bool) (func(string, string) bool, error) {
	if a == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) {
		return func(string, string) bool { return false }, nil
	}
	var suppressed map[string]struct{}
	if a.activity != nil && a.activity.store != nil {
		var err error
		suppressed, err = a.activity.store.ListCorrectedAgencyLearningTargetIDs()
		if err != nil {
			return nil, fmt.Errorf("read memory corrections: %w", err)
		}
	}
	gate := a.memorySourceReadGateContext(ctx)
	return func(id, content string) bool {
		if _, corrected := suppressed[id]; corrected {
			return false
		}
		if allowsDependency == nil {
			return gate.allows(content)
		}
		if a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) {
			return false
		}
		return a.memorySourceAllowsReadWithDependency(content, allowsDependency)
	}, nil
}

// memorySourceReadGate is memorySourceAllowsReadContext for one read of many
// notes. On first use it reads the privacy state of every session behind the
// current memory notes at once, so each note's check is a lookup. It is made
// per read and keeps nothing past it.
type memorySourceReadGate struct {
	a        *App
	ctx      context.Context
	deps     *activityDependencyReadGate
	once     sync.Once
	snapshot *memoryExternalSnapshot
}

func (a *App) memorySourceReadGateContext(ctx context.Context) *memorySourceReadGate {
	return &memorySourceReadGate{a: a, ctx: ctx, deps: a.newActivityDependencyReadGate(ctx)}
}

func (g *memorySourceReadGate) prefetch() {
	if g.a == nil {
		return
	}
	snapshot, err := g.a.memorySnapshot()
	if err != nil {
		return
	}
	g.snapshot = snapshot
	var sessions []string
	for _, deps := range snapshot.readDependencies() {
		sessions = append(sessions, deps.sessions...)
	}
	g.deps.prefetch(sessions)
}

// dependencies returns the note's parsed read dependencies, from the
// snapshot when content is a current note.
func (g *memorySourceReadGate) dependencies(content string) memoryReadDependencies {
	g.once.Do(g.prefetch)
	if g.snapshot != nil {
		if deps, ok := g.snapshot.readDependenciesOf(content); ok {
			return deps
		}
	}
	return memoryReadDependenciesOf(content)
}

func (g *memorySourceReadGate) frontmatter(content string) memoryFrontmatter {
	return g.dependencies(content).fm
}

func (g *memorySourceReadGate) allows(content string) bool {
	a := g.a
	if a == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(g.ctx) {
		return false
	}
	return a.memoryReadDependenciesAllow(g.dependencies(content), g.deps.allows)
}

type memoryEntryReadFilter func(MemoryEntry) (bool, error)

func (a *App) memoryEntriesForExternalRead() ([]MemoryEntry, error) {
	return a.filteredExternalMemories(nil)
}

func (a *App) filteredExternalMemories(filter memoryEntryReadFilter) ([]MemoryEntry, error) {
	allowRead, err := a.memoryReadFilter()
	if err != nil {
		return nil, err
	}
	snapshot, err := a.memoryExternalSnapshot()
	if err != nil {
		return nil, err
	}
	allowed := snapshot.allowed(allowRead)
	current := allowed[:0]
	for _, i := range allowed {
		entry := snapshot.entries[i].raw
		forgotten, err := snapshot.forgotten(entry.ID, entry.Content, time.Now())
		if err != nil {
			return nil, err
		}
		if forgotten {
			continue
		}
		if filter != nil {
			ok, err := filter(entry)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		current = append(current, i)
	}
	return snapshot.redactedEntries(current), nil
}

func (a *App) memoryEntryForExternalRead(id string) (MemoryEntry, error) {
	return a.filteredExternalMemory(id, nil)
}

func (a *App) filteredExternalMemory(id string, filter memoryEntryReadFilter) (MemoryEntry, error) {
	allowRead, err := a.memoryReadFilter()
	if err != nil {
		return MemoryEntry{}, err
	}
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return MemoryEntry{}, err
	}
	if !allowRead(entry.ID, entry.Content) || !memoryInUse(entry.Content) {
		return MemoryEntry{}, os.ErrNotExist
	}
	dir, err := memoryDir()
	if err != nil {
		return MemoryEntry{}, err
	}
	forgotten, err := isMemoryForgotten(dir, id, entry.Content, time.Now())
	if err != nil {
		return MemoryEntry{}, err
	}
	if forgotten {
		return MemoryEntry{}, os.ErrNotExist
	}
	if filter != nil {
		ok, err := filter(entry)
		if err != nil {
			return MemoryEntry{}, err
		}
		if !ok {
			return MemoryEntry{}, os.ErrNotExist
		}
	}
	return redactMemoryEntryForExternalRead(entry), nil
}

func redactMemoryEntryForExternalRead(entry MemoryEntry) MemoryEntry {
	entry.Title, _ = pii.RedactText(entry.Title)
	entry.Content, _ = pii.RedactText(entry.Content)
	entry.Apps = append([]string(nil), entry.Apps...)
	for i := range entry.Apps {
		entry.Apps[i], _ = pii.RedactText(entry.Apps[i])
	}
	return entry
}

func (a *App) memorySourceAllowsRead(content string) bool {
	return a.memorySourceAllowsReadContext(context.Background(), content)
}

func (a *App) memorySourceAllowsReadContext(ctx context.Context, content string) bool {
	if a == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) {
		return false
	}
	return a.memorySourceAllowsReadWithDependency(content, func(id string, apps []string) bool {
		return a.activityDependencyAllowsModelReadWithCacheContext(ctx, id, apps, nil)
	})
}

// Shared source checks also serve the owner-only stored portrait reader.
func (a *App) memorySourceAllowsReadWithDependency(content string, allowsDependency func(string, []string) bool) bool {
	return a.memoryReadDependenciesAllow(memoryReadDependenciesOf(content), allowsDependency)
}

// memoryReadDependencies is what a note's source check reads from the note
// itself: its apps and sessions, with a valid read provenance merged in.
type memoryReadDependencies struct {
	fm               memoryFrontmatter
	blocked          bool
	apps, sessions   []string
	correctionPerson string
}

func memoryReadDependenciesOf(content string) memoryReadDependencies {
	fm := parseMemoryFrontmatter(content)
	apps, sessions := fm.Apps, fm.ActivitySessionIDs
	if fm.ReadProvenance != "" {
		// Writers flatten dependencies when updating or dreaming. Validate
		// explicit envelopes here, but retain v1 conversation dependencies:
		// MCP excludes their ambiguous connector lineage from sharing while
		// internal recall still needs their concrete app/session privacy gates.
		var provenance memoryReadProvenance
		if _, yamlHeader := parseOKFDocument(content); !yamlHeader || json.Unmarshal([]byte(fm.ReadProvenance), &provenance) != nil || (provenance.Version != 1 && provenance.Version != 2) || provenance.ReadBlocked {
			return memoryReadDependencies{fm: fm, blocked: true}
		}
		apps = compactStrings(append(slices.Clone(apps), provenance.Apps...))
		sessions = compactStrings(append(slices.Clone(sessions), provenance.Sessions...))
	}
	deps := memoryReadDependencies{fm: fm, apps: apps, sessions: sessions}
	if strings.Contains(content, "chat-person:") {
		if doc, ok := parseOKFDocument(content); ok {
			for _, source := range okfStringSlice(doc.Frontmatter, "source_ids") {
				if id, linked := strings.CutPrefix(source, "chat-person:"); linked {
					deps.correctionPerson = id
					break
				}
			}
		}
	}
	return deps
}

func (a *App) memoryReadDependenciesAllow(deps memoryReadDependencies, allowsDependency func(string, []string) bool) bool {
	if deps.correctionPerson != "" {
		if a == nil || a.store == nil || a.IsClosedEyesModeEnabled() {
			return false
		}
		ctx := context.Background()
		p, err := a.store.GetPrincipal(ctx, deps.correctionPerson)
		if err != nil || !a.peopleProfileAllowsModelRead(ctx, p, nil) {
			return false
		}
	}
	if deps.blocked {
		return false
	}
	apps, sessions := deps.apps, deps.sessions
	for _, name := range apps {
		if activitySourceAppIsSensitive(name, name) {
			return false
		}
	}
	for _, id := range sessions {
		if a.activity == nil || a.activity.store == nil {
			return false
		}
		if !allowsDependency(id, apps) {
			return false
		}
	}
	return true
}
