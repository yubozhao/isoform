package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// These identities and revision hashes stay in local receipts. They are never
// model evidence or a phone/renderer presentation field.
type proactiveVoiceDependency struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

type proactiveVoiceDependencyContextKey struct{}

type proactiveVoiceDependencyCollector struct {
	items  map[string]proactiveVoiceDependency
	failed bool
}

func proactiveVoiceDependencyContext(ctx context.Context) (context.Context, *proactiveVoiceDependencyCollector) {
	collector := &proactiveVoiceDependencyCollector{items: map[string]proactiveVoiceDependency{}}
	return context.WithValue(ctx, proactiveVoiceDependencyContextKey{}, collector), collector
}

func proactiveVoiceRecordDependency(ctx context.Context, kind, id string, value any) bool {
	collector, _ := ctx.Value(proactiveVoiceDependencyContextKey{}).(*proactiveVoiceDependencyCollector)
	if collector == nil {
		return true
	}
	raw, err := json.Marshal(value)
	if err != nil || strings.TrimSpace(kind) == "" || strings.TrimSpace(id) == "" {
		collector.failed = true
		return false
	}
	dependency := proactiveVoiceDependency{Kind: kind, ID: id, Revision: agencyPayloadHash(raw)}
	key := kind + "\x00" + id
	if previous, found := collector.items[key]; found && previous.Revision != dependency.Revision {
		// A source changed during this read. A later read must establish a
		// coherent dependency list before any generated text can be delivered.
		collector.failed = true
		return false
	}
	collector.items[key] = dependency
	return true
}

func (collector *proactiveVoiceDependencyCollector) dependencies() ([]proactiveVoiceDependency, bool) {
	if collector.failed || len(collector.items) == 0 {
		return nil, false
	}
	out := make([]proactiveVoiceDependency, 0, len(collector.items))
	for _, dependency := range collector.items {
		out = append(out, dependency)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out, true
}

func (a *App) proactiveVoiceMarkdownDependencies(ctx context.Context, kind, id, content string) bool {
	provenance := memoryReadProvenanceFromContent(content)
	if provenance.ReadBlocked || !proactiveVoiceRecordDependency(ctx, kind, id, content) {
		return false
	}
	return a.proactiveVoiceSessionsAllowedContext(ctx, provenance.Sessions, provenance.Apps)
}

func (a *App) proactiveVoiceSessionsAllowedContext(ctx context.Context, ids, storedApps []string) bool {
	if len(ids) == 0 {
		return true
	}
	recordStreamPageSessions(ctx, ids)
	if a == nil || a.activity == nil || a.activity.store == nil || ctx.Err() != nil {
		return false
	}
	store := a.activity.store
	for _, id := range ids {
		before, beforeErr := store.activityPrivacySourceState(store.db, id)
		source, err := store.activityDependencyProvenance(id, storedApps)
		if err != nil {
			return false
		}
		allowed, err := a.activitySourceProvenanceAllowsRead(source)
		if err != nil || !allowed {
			return false
		}
		var revision int64
		if !source.Retained {
			state, err := store.activityPrivacySourceState(store.db, id)
			if beforeErr != nil || err != nil || state != before {
				return false
			}
			revision = state.revision
		}
		if !proactiveVoiceRecordDependency(ctx, "activity_session", id, struct {
			Source   activitySourceProvenance
			Revision int64
		}{source, revision}) {
			return false
		}
	}
	return true
}
