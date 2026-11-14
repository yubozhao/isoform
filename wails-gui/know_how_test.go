package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"

	"yansu-agent/agent"
)

type knowHowFakeCounter struct {
	ids  []string
	kind string
	err  error
}

func (c *knowHowFakeCounter) GetEntryUseCounts(kind string, ids []string) (map[string]int, error) {
	c.ids, c.kind = ids, kind
	if c.err != nil {
		return nil, c.err
	}
	return map[string]int{"older": 4}, nil
}
func writeKnowHowFixture(t *testing.T, id, content string) {
	t.Helper()
	dir, err := knowledgeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestKnowHowCardsMatchOwnerList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}
	ts := time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC)
	writeKnowHowFixture(t, "older", buildOKFKnowledgeMarkdown("older", "Earlier fix", "Restart the socket", ts, "activity", []string{"Terminal"}, []string{"one", "two"}, []string{"a", "b", "c", "d", "e", "f", "g"}, "fix"))
	writeKnowHowFixture(t, "newer", buildOKFKnowledgeMarkdown("newer", "New fact", "Description", ts.Add(time.Hour), "conversation", nil, nil, nil))
	writeKnowHowFixture(t, "unknown", strings.Replace(buildOKFKnowledgeMarkdown("unknown", "Unknown", "", ts, "conversation", nil, nil, nil, "fix"), "kind: fix", "kind: surprising", 1))
	counter := &knowHowFakeCounter{}
	app.knowHow.counter = counter
	entries, err := app.GetKnowledgeEntries()
	if err != nil {
		t.Fatal(err)
	}
	cards, err := app.ListKnowHow()
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != len(entries) || counter.kind != "knowledge" {
		t.Fatalf("entries/cards: %d/%d counter=%+v", len(entries), len(cards), counter)
	}
	for i, entry := range entries {
		card := cards[i]
		if card.ID != entry.ID || counter.ids[i] != entry.ID {
			t.Fatal("owner order changed")
		}
		switch card.ID {
		case "older":
			if card.Kind != "fix" || card.SavedAt != ts.UnixMilli() || card.ReuseCount != 4 || card.SessionCount != 2 || len(card.Points) != 6 || card.Takeaway != "Restart the socket" || card.FormatVersion != okfVersion || card.Source != "activity" || !reflect.DeepEqual(card.Apps, []string{"Terminal"}) {
				t.Fatalf("card=%+v", card)
			}
		default:
			if card.Kind != "reference" {
				t.Fatalf("missing/unknown kind=%s", card.Kind)
			}
		}
		if card.TopicID != "" || card.TopicName != "" {
			t.Fatal("absent registry invented a topic")
		}
	}
	app.knowHow.counter = nil
	cards, err = app.ListKnowHow()
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range cards {
		if card.ReuseCount != 0 {
			t.Fatal("an entry without stored uses returned a count")
		}
	}
}

func TestKnowHowListsEntriesWhenReuseCountsFail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}
	ts := time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC)
	writeKnowHowFixture(t, "older", buildOKFKnowledgeMarkdown("older", "Earlier fix", "Restart the socket", ts, "activity", nil, nil, []string{"a"}, "fix"))
	writeKnowHowFixture(t, "newer", buildOKFKnowledgeMarkdown("newer", "New fact", "Description", ts.Add(time.Hour), "conversation", nil, nil, nil))
	app.knowHow.counter = &knowHowFakeCounter{err: fmt.Errorf("invalid entry list")}
	cards, err := app.ListKnowHow()
	if err != nil {
		t.Fatalf("a use-log failure hid the entries: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("cards=%d, want 2", len(cards))
	}
	for _, card := range cards {
		if card.ReuseCount != -1 || card.Title == "" {
			t.Fatalf("card=%+v, want an unknown count and its content", card)
		}
	}
}

func TestKnowHowGeneratorsAndUpdateKind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := newTranslationTestApp(t, &memoryTranslationFakeRunner{})
	triager := &ActivityTriager{app: app}
	generator := &MemoryGenerator{app: app}
	session := ActivitySessionRow{ID: "kind-session", StartTime: time.Now().UnixMilli(), AppNames: `["Terminal"]`}
	group := sessionGroup{Sessions: []ActivitySessionRow{session}}
	response := &knowledgeGenerationResponse{Title: "Socket fix", Description: "An error workaround", Kind: "fix", KnowledgePoints: []string{"ECONNREFUSED: restart the daemon"}}
	id, err := triager.saveKnowledge(session, group, response)
	if err != nil {
		t.Fatal(err)
	}
	check := func(id, kind string) {
		t.Helper()
		entry, err := app.GetKnowledgeEntry(id)
		if err != nil {
			t.Fatal(err)
		}
		doc, ok := parseOKFDocument(entry.Content)
		if !ok || okfString(doc.Frontmatter, "kind") != kind {
			t.Fatalf("kind %q not written: %s", kind, entry.Content)
		}
	}
	check(id, "fix")
	response.UpdateID, response.Kind = id, "bogus"
	if _, err := triager.updateKnowledge(session, group, response); err != nil {
		t.Fatal(err)
	}
	check(id, "fix")
	response.Kind = "gotcha"
	if _, err := triager.updateKnowledge(session, group, response); err != nil {
		t.Fatal(err)
	}
	check(id, "gotcha")
	for _, source := range []memorySource{memorySourceConversation, memorySourceSlack} {
		// A new lesson each time; the same lesson would fold into the first (W40).
		response.Title, response.UpdateID = "Socket fix from "+string(source), ""
		response.Kind = "decision"
		id := generator.saveKnowledgeFile(source, "source-id", response)
		check(id, "decision")
		response.UpdateID, response.Kind = id, ""
		if _, _, err := generator.updateKnowledgeFileDetailed(source, "source-id", response); err != nil {
			t.Fatal(err)
		}
		check(id, "decision")
		response.Kind = "how_to"
		if _, _, err := generator.updateKnowledgeFileDetailed(source, "source-id", response); err != nil {
			t.Fatal(err)
		}
		check(id, "how_to")
	}
	for _, kind := range []string{"decision", "how_to", "fix", "gotcha", "reference"} {
		checkID := "builder-" + kind
		writeKnowHowFixture(t, checkID, buildOKFKnowledgeMarkdown(checkID, "Title", "", time.Now(), "activity", nil, nil, nil, kind))
		check(checkID, kind)
	}
}

func TestKnowHowGenerationPromptsSpecifyKind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := &memoryTranslationFakeRunner{response: `{"action":"create","title":"Socket","kind":"fix","knowledgePoints":["ECONNREFUSED: restart"]}`}
	app := newTranslationTestApp(t, runner)
	triager := &ActivityTriager{app: app}
	if got, err := triager.runKnowledgeGeneration(context.Background(), "Socket error"); err != nil || got.Kind != "fix" {
		t.Fatalf("triage kind=%+v err=%v", got, err)
	}
	generator := &MemoryGenerator{app: app}
	if got, err := generator.runKnowledgeGeneration(context.Background(), "Socket error", ""); err != nil || got.Kind != "fix" {
		t.Fatalf("conversation kind=%+v err=%v", got, err)
	}
	for _, prompt := range runner.prompts {
		for _, required := range []string{`"kind": "decision|how_to|fix|gotcha|reference"`, "Decision rationale -> decision", "error with its workaround -> fix", "people/org, domain/business"} {
			if !strings.Contains(prompt, required) {
				t.Fatalf("prompt missing %q", required)
			}
		}
	}
}

func TestKnowHowBackfillOnlyKindOncePauseResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := newTranslationTestApp(t, &memoryTranslationFakeRunner{})
	original := buildOKFKnowledgeMarkdown("old", "Old fix", "Useful description", time.Now(), "conversation", nil, nil, []string{"one", "two", "three", "four", "five", "six"})
	original = strings.Replace(original, "type:", "# untouched comment\ntype:", 1)
	writeKnowHowFixture(t, "old", original)
	calls := 0
	classify := func(ctx context.Context, input []knowHowBackfillInput) (map[string]string, error) {
		calls++
		if len(input) != 1 || len(input[0].Points) != 5 {
			t.Fatalf("inputs=%+v", input)
		}
		return map[string]string{"old": "fix"}, nil
	}
	app.closedEyes.enabled.Store(true)
	if done, err := app.backfillKnowHowKinds(context.Background(), classify); done || err != nil || calls != 0 {
		t.Fatalf("paused done=%v calls=%d err=%v", done, calls, err)
	}
	app.closedEyes.enabled.Store(false)
	if done, err := app.backfillKnowHowKinds(context.Background(), classify); !done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
	entry, err := app.GetKnowledgeEntry("old")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Replace(entry.Content, "kind: fix\n", "", 1) != original {
		t.Fatal("backfill changed bytes beyond kind line")
	}
	dir, _ := knowledgeDir()
	if knowledgeEntryHasHistory(dir, "old") {
		t.Fatal("backfill created history")
	}
	if done, err := app.backfillKnowHowKinds(context.Background(), classify); !done || err != nil || calls != 1 {
		t.Fatalf("second run done=%v calls=%d err=%v", done, calls, err)
	}
	state, _ := app.store.GetUIState(context.Background(), knowHowBackfillKey)
	if state != "done" {
		t.Fatalf("state=%q", state)
	}
	for _, newline := range []string{"\n", "\r\n"} {
		input := strings.ReplaceAll(original, "\n", newline)
		patched, err := insertKnowHowKind(input, "fix")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Replace(patched, "kind: fix"+newline, "", 1) != input {
			t.Fatal("line endings changed")
		}
	}
}

func TestKnowHowBackfillBoundsAndInterruptedPlan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := newTranslationTestApp(t, &memoryTranslationFakeRunner{})
	for i := 0; i < 505; i++ {
		id := fmt.Sprintf("entry-%03d", i)
		writeKnowHowFixture(t, id, buildOKFKnowledgeMarkdown(id, "Fact", "", time.Unix(int64(i), 0), "conversation", nil, nil, nil))
	}
	seen := map[string]bool{}
	calls := 0
	classify := func(ctx context.Context, input []knowHowBackfillInput) (map[string]string, error) {
		calls++
		if len(input) > 20 {
			t.Fatal("batch exceeds 20")
		}
		kinds := map[string]string{}
		for _, item := range input {
			seen[item.ID] = true
			kinds[item.ID] = "reference"
		}
		if calls == 2 {
			app.closedEyes.enabled.Store(true)
		}
		return kinds, nil
	}
	if done, err := app.backfillKnowHowKinds(context.Background(), classify); done || err != nil {
		t.Fatalf("interrupted done=%v err=%v", done, err)
	}
	app.closedEyes.enabled.Store(false)
	if done, err := app.backfillKnowHowKinds(context.Background(), classify); !done || err != nil {
		t.Fatalf("resumed done=%v err=%v", done, err)
	}
	if len(seen) != 500 {
		t.Fatalf("classified %d distinct files, want 500", len(seen))
	}
	entries, _ := app.GetKnowledgeEntries()
	count := 0
	for _, entry := range entries {
		doc, _ := parseOKFDocument(entry.Content)
		if okfString(doc.Frontmatter, "kind") != "" {
			count++
		}
	}
	if count != 500 {
		t.Fatalf("wrote %d kinds", count)
	}
}

func TestKnowHowBackfillRedactsAndRechecksSource(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	now := time.Now().UnixMilli()
	insertTestSession(t, app.activity.store, "allowed", "done", now-120000)
	insertTestSession(t, app.activity.store, "protected", "protected", now-60000)
	for _, id := range []string{"allowed", "protected"} {
		writeKnowHowFixture(t, id, buildOKFKnowledgeMarkdown(id, "Contact alice@example.com", "Email alice@example.com", time.Now(), "activity", nil, []string{id}, []string{"Ask alice@example.com"}))
	}
	classify := func(ctx context.Context, input []knowHowBackfillInput) (map[string]string, error) {
		if len(input) != 1 || input[0].ID != "allowed" {
			t.Fatalf("privacy input=%+v", input)
		}
		raw, _ := json.Marshal(input)
		if strings.Contains(string(raw), "alice@example.com") {
			t.Fatal("PII sent to model")
		}
		if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='allowed'`); err != nil {
			t.Fatal(err)
		}
		return map[string]string{"allowed": "reference"}, nil
	}
	if _, err := app.backfillKnowHowKinds(context.Background(), classify); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"allowed", "protected"} {
		entry, _ := app.GetKnowledgeEntry(id)
		doc, _ := parseOKFDocument(entry.Content)
		if okfString(doc.Frontmatter, "kind") != "" {
			t.Fatal("withdrawn source was rewritten")
		}
	}
}

func TestKnowHowShadowKindDoesNotChangeProvenance(t *testing.T) {
	legacy := buildOKFKnowledgeMarkdown("shadow", "Pattern", "", time.Now(), "shadow_task_evaluation", nil, nil, []string{"Keep evidence"})
	classified, err := insertKnowHowKind(legacy, "how_to")
	if err != nil {
		t.Fatal(err)
	}
	if !knowHowDocumentsEqualExceptKind(legacy, classified) {
		t.Fatal("classification invalidated verified document")
	}
	if knowHowDocumentsEqualExceptKind(legacy, strings.Replace(classified, "Keep evidence", "Invent evidence", 1)) {
		t.Fatal("changed evidence passed verification")
	}
}

// The fixture uses actual Activity and memory SQLite stores. Only the embedding
// provider is counted; local matching must not call even a configured provider.
type knowHowCountingEmbedder struct{ calls int }

func (p *knowHowCountingEmbedder) EmbedQuery(context.Context, string) ([]float64, error) {
	p.calls++
	return nil, fmt.Errorf("remote call forbidden")
}
func (p *knowHowCountingEmbedder) EmbedBatch(context.Context, []string) ([][]float64, error) {
	p.calls++
	return nil, fmt.Errorf("remote call forbidden")
}
func (*knowHowCountingEmbedder) Provider() string    { return "openai" }
func (*knowHowCountingEmbedder) Model() string       { return "fake" }
func (*knowHowCountingEmbedder) ProviderKey() string { return "fake" }

func knowHowMatchFixture(t *testing.T) (*App, *knowHowCountingEmbedder) {
	t.Helper()
	app := newPrivacyReadTestApp(t)
	root, err := flavorDataDir()
	if err != nil {
		t.Fatal(err)
	}
	memory, err := NewMemoryStore(root, MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { memory.Close() })
	app.memoryStore = memory
	now := time.Now().Add(-time.Minute).UnixMilli()
	insertTestSession(t, app.activity.store, "old-session", "done", now-3600000)
	session := ActivitySessionRow{ID: "current-session", StartTime: now, EndTime: now + 1000, AppNames: `["Terminal"]`, EventCount: 1, TriageStatus: "done"}
	event := activitymon.ActivityEvent{AppName: "Terminal", AppBundleID: "com.apple.Terminal", Timestamp: now, Kind: activitymon.EventClick, ClickLabel: "Run"}
	saveTimelineTestSession(t, app.activity.store, session, []activitymon.ActivityEvent{event})
	runs, err := app.activity.store.GetTimelineRunsInRange(now-1000, now+2000)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	runs[0].EvidenceTokens = []ActivityEvidenceToken{{Kind: "error", Value: "ECONNREFUSED", LastSeenMs: now}, {Kind: "path", Value: "/tmp/service.sock", LastSeenMs: now}}
	if err := persistTimelineRunEvidence(app.activity.store.db, runs[0]); err != nil {
		t.Fatal(err)
	}
	writeKnowHowFixture(t, "earlier-fix", buildOKFKnowledgeMarkdown("earlier-fix", "ECONNREFUSED socket workaround", "Restart the service", time.UnixMilli(now-3600000), "activity", []string{"Code"}, []string{"old-session"}, []string{"For ECONNREFUSED on /tmp/service.sock, restart the daemon"}, "fix"))
	if err := memory.Sync("fixture"); err != nil {
		t.Fatal(err)
	}
	provider := &knowHowCountingEmbedder{}
	memory.embedder = provider
	memory.cfg = MemoryStoreConfig{Provider: "openai", APIKey: "fixture"}
	return app, provider
}

func TestKnowHowMatchFreshErrorDismissalAndNoRemoteVector(t *testing.T) {
	app, provider := knowHowMatchFixture(t)
	match, err := app.GetKnowHowMatch()
	if err != nil || match == nil {
		t.Fatalf("match=%+v err=%v", match, err)
	}
	if match.EntryID != "earlier-fix" || match.Kind != "fix" || match.CurrentApp != "Terminal" || match.PreviousAt == 0 || len(match.Signature) != 64 {
		t.Fatalf("match=%+v", match)
	}
	again, err := app.GetKnowHowMatch()
	if err != nil || !reflect.DeepEqual(again, match) {
		t.Fatalf("cache=%+v err=%v", again, err)
	}
	if provider.calls != 0 {
		t.Fatal("remote embedding called")
	}
	if err := app.DismissKnowHowMatch(match.EntryID, match.Signature); err != nil {
		t.Fatal(err)
	}
	app.knowHow.cached = nil
	app.knowHow.cacheKey = ""
	if got, err := app.GetKnowHowMatch(); err != nil || got != nil {
		t.Fatalf("dismissed match=%+v err=%v", got, err)
	}
	if err := app.DismissKnowHowMatch("../escape", match.Signature); err == nil {
		t.Fatal("unsafe id accepted")
	}
	if err := app.DismissKnowHowMatch(match.EntryID, "error text"); err == nil {
		t.Fatal("raw error accepted as signature")
	}
}

func TestKnowHowMatchPrivacyAndSameSession(t *testing.T) {
	for _, scenario := range []string{"same-session", "closed-eyes", "protected", "sensitive", "private-window", "paused", "withdrawn-knowledge", "generic-error", "stale-token", "reference", "deleted", "stale-index"} {
		t.Run(scenario, func(t *testing.T) {
			app, provider := knowHowMatchFixture(t)
			// Warm the cache first, then withdraw evidence without waiting for TTL.
			if got, err := app.GetKnowHowMatch(); got == nil || err != nil {
				t.Fatalf("control=%+v err=%v", got, err)
			}
			store := app.activity.store
			switch scenario {
			case "same-session", "withdrawn-knowledge", "reference", "stale-index":
				entry, _ := app.GetKnowledgeEntry("earlier-fix")
				content := entry.Content
				switch scenario {
				case "same-session":
					content = strings.ReplaceAll(content, "old-session", "current-session")
				case "withdrawn-knowledge":
					if _, err := store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='old-session'`); err != nil {
						t.Fatal(err)
					}
				case "reference":
					content = strings.ReplaceAll(content, "kind: fix", "kind: reference")
				case "stale-index":
					content = strings.ReplaceAll(content, "ECONNREFUSED", "ENOENT")
				}
				writeKnowHowFixture(t, entry.ID, content)
			case "closed-eyes":
				app.closedEyes.enabled.Store(true)
			case "protected":
				if _, err := store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='current-session'`); err != nil {
					t.Fatal(err)
				}
			case "sensitive":
				prior := pii.GetUserSensitiveApps()
				t.Cleanup(func() { pii.SetUserSensitiveApps(prior) })
				pii.SetUserSensitiveApps(map[string]string{"com.apple.Terminal": "Terminal"})
			case "private-window":
				if _, err := store.db.Exec(`UPDATE activity_events SET data='{"appName":"Terminal","privateWindow":true}' WHERE session_id='current-session'`); err != nil {
					t.Fatal(err)
				}
			case "paused":
				app.saveObservationPauses([]observationPause{{Start: time.Now().Add(-5 * time.Minute).UnixMilli(), End: time.Now().UnixMilli()}})
			case "generic-error", "stale-token":
				runs, _ := store.GetTimelineRunsInRange(0, 0)
				if scenario == "generic-error" {
					runs[0].EvidenceTokens[0].Value = "Error"
				} else {
					runs[0].EvidenceTokens[0].LastSeenMs = time.Now().Add(-31 * time.Minute).UnixMilli()
				}
				if err := persistTimelineRunEvidence(store.db, runs[0]); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				dir, _ := knowledgeDir()
				if err := os.Remove(filepath.Join(dir, "earlier-fix.md")); err != nil {
					t.Fatal(err)
				}
			}
			if match, err := app.GetKnowHowMatch(); err != nil || match != nil {
				t.Fatalf("%s match=%+v err=%v", scenario, match, err)
			}
			if provider.calls != 0 {
				t.Fatal("privacy path called provider")
			}
		})
	}
}

func TestKnowHowMatchConcurrentReads(t *testing.T) {
	app, provider := knowHowMatchFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if match, err := app.GetKnowHowMatch(); err != nil || match == nil {
				t.Errorf("match=%+v err=%v", match, err)
			}
		}()
	}
	wg.Wait()
	if provider.calls != 0 {
		t.Fatal("concurrent reads called provider")
	}
}

func TestKnowHowScoreCalibration(t *testing.T) {
	for _, fixture := range []struct {
		name, text string
		match      bool
	}{
		{"same code and path", "For ECONNREFUSED on /tmp/service.sock restart daemon", true},
		{"same specific code", "ECONNREFUSED means the daemon is down", true},
		{"same path different error", "ENOENT on /tmp/service.sock", false},
		{"generic prose", "A socket error needs a restart", false},
		{"substring code", "ECONNREFUSED_OTHER is another code", false},
	} {
		score := knowHowErrorScore(KnowHowCard{Title: fixture.text}, []string{"econnrefused"}, []string{"/tmp/service.sock"})
		if (score >= 0.75) != fixture.match {
			t.Errorf("%s score=%f", fixture.name, score)
		}
	}
}

func TestKnowHowShadowAndRoutineGenerators(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}
	candidate := testShadowTaskLearningProjection().Candidates[0]
	candidate.TargetKind = ShadowTaskLearningTargetKnowledge
	id, reason := app.applyQualifiedShadowTaskLearningCandidate(candidate, 1000)
	if reason != "" {
		t.Fatal(reason)
	}
	entry, err := app.GetKnowledgeEntry(id)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := parseOKFDocument(entry.Content)
	if okfString(doc.Frontmatter, "kind") != "how_to" {
		t.Fatal("shadow kind missing")
	}
	writeKnowHowFixture(t, id, strings.Replace(entry.Content, "kind: how_to", "kind: gotcha", 1))
	if _, reason := app.applyQualifiedShadowTaskLearningCandidate(candidate, 1000); reason != "" {
		t.Fatal(reason)
	}
	entry, _ = app.GetKnowledgeEntry(id)
	doc, _ = parseOKFDocument(entry.Content)
	dir, _ := knowledgeDir()
	if okfString(doc.Frontmatter, "kind") != "gotcha" || knowledgeEntryHasHistory(dir, id) {
		t.Fatal("shadow retry changed classification or made history")
	}
	for _, lang := range []string{"en", "zh-CN"} {
		content := noticedRoutineKnowHow(NoticedRoutineDraft{Name: "Release", Steps: []string{"Run checks"}}, "2026-10-21", time.Now(), lang)
		doc, _ := parseOKFDocument(content)
		if okfString(doc.Frontmatter, "kind") != "how_to" {
			t.Fatal("routine kind missing")
		}
	}
}

func TestKnowHowBackfillConcurrentEditAndInvalidResult(t *testing.T) {
	for _, scenario := range []string{"edit", "invalid", "pause"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			app := newTranslationTestApp(t, &memoryTranslationFakeRunner{})
			original := buildOKFKnowledgeMarkdown("old", "Fix", "", time.Now(), "conversation", nil, nil, []string{"original"})
			writeKnowHowFixture(t, "old", original)
			edited := strings.Replace(original, "original", "user edit", 1)
			classify := func(context.Context, []knowHowBackfillInput) (map[string]string, error) {
				switch scenario {
				case "edit":
					writeKnowHowFixture(t, "old", edited)
				case "invalid":
					return map[string]string{"old": "made_up"}, nil
				case "pause":
					app.closedEyes.enabled.Store(true)
				}
				return map[string]string{"old": "fix"}, nil
			}
			done, err := app.backfillKnowHowKinds(context.Background(), classify)
			if done || (scenario != "pause" && err == nil) {
				t.Fatalf("done=%v err=%v", done, err)
			}
			entry, _ := app.GetKnowledgeEntry("old")
			want := original
			if scenario == "edit" {
				want = edited
			}
			if entry.Content != want {
				t.Fatal("classification overwrote newer content or published after pause")
			}
			app.closedEyes.enabled.Store(false)
			if done, err := app.backfillKnowHowKinds(context.Background(), func(context.Context, []knowHowBackfillInput) (map[string]string, error) {
				return map[string]string{"old": "fix"}, nil
			}); !done || err != nil {
				t.Fatalf("recovery done=%v err=%v", done, err)
			}
		})
	}
}

func TestKnowHowBackfillRunnerStrictJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := &memoryTranslationFakeRunner{response: "```json\n{\"old\":\"fix\"}\n```"}
	app := newTranslationTestApp(t, runner)
	if _, err := app.classifyKnowHowKinds(context.Background(), []knowHowBackfillInput{{ID: "old", Title: "Fix"}}); err == nil {
		t.Fatal("non-JSON response accepted")
	}
	runner.response = `{"old":"fix"}`
	kinds, err := app.classifyKnowHowKinds(context.Background(), []knowHowBackfillInput{{ID: "old", Title: "Fix"}})
	if err != nil || kinds["old"] != "fix" {
		t.Fatalf("kinds=%v err=%v", kinds, err)
	}
}

func TestKnowHowMatchOverlappingMomentAndNoError(t *testing.T) {
	app, _ := knowHowMatchFixture(t)
	if _, err := app.activity.store.db.Exec(`UPDATE activity_timeline_groups SET start_ms=?`, time.Now().Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if match, err := app.GetKnowHowMatch(); err != nil || match == nil {
		t.Fatalf("long moment's fresh error lost: match=%+v err=%v", match, err)
	}
	if _, err := app.activity.store.db.Exec(`UPDATE activity_timeline_groups SET evidence_tokens='[]'`); err != nil {
		t.Fatal(err)
	}
	if match, err := app.GetKnowHowMatch(); err != nil || match != nil {
		t.Fatalf("no-error match=%+v err=%v", match, err)
	}
}

func TestKnowHowBindingsAllowlisted(t *testing.T) {
	for _, method := range []string{"ListKnowHow", "GetKnowHowMatch", "DismissKnowHowMatch"} {
		if !browserRPCAllowlist[method] {
			t.Fatalf("missing preview binding %s", method)
		}
	}
}

func TestKnowHowBackfillLegacyMetadataPreservesOwnerAndPrivacy(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	now := time.Now().UnixMilli()
	insertTestSession(t, app.activity.store, "legacy-session", "done", now-3600000)
	original := "# Legacy fix\n\n- **Date**: 2026-10-19 12:30\n- **Source**: activity\n- **Apps**: Terminal\n- **Activity Session**:\n  - legacy-session\n\n## Description\n\nRestart the socket.\n\n## Key Insights\n\n- ECONNREFUSED: restart the daemon\n"
	writeKnowHowFixture(t, "legacy", original)
	before, _ := app.GetKnowledgeEntry("legacy")
	beforeFM := parseMemoryFrontmatter(original)
	if done, err := app.backfillKnowHowKinds(context.Background(), func(context.Context, []knowHowBackfillInput) (map[string]string, error) {
		return map[string]string{"legacy": "fix"}, nil
	}); !done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
	after, _ := app.GetKnowledgeEntry("legacy")
	if strings.Replace(after.Content, "- **Kind**: fix\n", "", 1) != original {
		t.Fatal("legacy backfill changed bytes beyond its kind metadata line")
	}
	afterFM := parseMemoryFrontmatter(after.Content)
	if !reflect.DeepEqual(beforeFM, afterFM) {
		t.Fatalf("legacy source privacy changed: before=%+v after=%+v", beforeFM, afterFM)
	}
	before.Content, after.Content = "", ""
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("legacy owner fields changed: before=%+v after=%+v", before, after)
	}
	cards, err := app.ListKnowHow()
	if err != nil || len(cards) != 1 || cards[0].Kind != "fix" || cards[0].SessionCount != 1 || len(cards[0].Points) != 1 {
		t.Fatalf("cards=%+v err=%v", cards, err)
	}
}

func TestKnowHowMatchSearchesPointsBeyondCardLimit(t *testing.T) {
	app, provider := knowHowMatchFixture(t)
	entry, _ := app.GetKnowledgeEntry("earlier-fix")
	original := parseKnowledgeFile("earlier-fix.md", entry.Content)
	content := buildOKFKnowledgeMarkdown("earlier-fix", "Socket workaround", "Restart the service", time.UnixMilli(parseOKFTimestampMs(original.Date)), "activity", []string{"Terminal"}, []string{"old-session"}, []string{"one", "two", "three", "four", "five", "six", "For ECONNREFUSED on /tmp/service.sock, restart daemon"}, "fix")
	writeKnowHowFixture(t, "earlier-fix", content)
	// The index may still have the old chunk. Current-file scoring must
	// preserve matches in points beyond the card's six-point presentation.
	match, err := app.GetKnowHowMatch()
	if err != nil || match == nil {
		t.Fatalf("match=%+v err=%v", match, err)
	}
	if provider.calls != 0 {
		t.Fatal("point search called remote embedder")
	}
}

func TestKnowHowKindDoesNotChangeMemoryGenerationPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runner := &memoryTranslationFakeRunner{response: `{"action":"skip","memoryPoints":[]}`}
	app := newTranslationTestApp(t, runner)
	triager := &ActivityTriager{app: app}
	if _, err := triager.runMemoryGeneration(context.Background(), "fixture", "No new beliefs", SessionContentProfile{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(runner.lastPrompt, `"kind":`) || strings.Contains(runner.lastPrompt, "## kind field rules:") {
		t.Fatal("knowledge classification changed the memory prompt")
	}
}

type knowHowBlockingRunner struct {
	memoryTranslationFakeRunner
	entered chan struct{}
	release chan struct{}
}

func (r *knowHowBlockingRunner) ExecuteTask(ctx context.Context, input *agent.TaskInput, callback agent.StreamCallback) (string, error) {
	r.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-r.release:
	}
	return "fixture", callback(&agent.StreamEvent{Type: "assistant", Content: `{"old":"reference"}`})
}
func TestKnowHowBackfillWorkerSingleFlight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := newTranslationTestApp(t, &memoryTranslationFakeRunner{})
	runner := &knowHowBlockingRunner{entered: make(chan struct{}, 2), release: make(chan struct{})}
	app.runner = runner
	writeKnowHowFixture(t, "old", buildOKFKnowledgeMarkdown("old", "Fact", "", time.Now(), "conversation", nil, nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { defer close(finished); app.runKnowHowKindBackfill(ctx) }()
	select {
	case <-runner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("backfill did not start")
	}
	app.runKnowHowKindBackfill(ctx)
	close(runner.release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("backfill did not finish")
	}
	if len(runner.entered) != 0 {
		t.Fatal("single flight invoked a second classifier")
	}
}

func TestKnowHowMatchPublicationStaysBoundToOrigin(t *testing.T) {
	app, _ := knowHowMatchFixture(t)
	match, err := app.GetKnowHowMatch()
	if err != nil || match == nil {
		t.Fatalf("control=%+v err=%v", match, err)
	}
	originID := app.knowHow.cacheRunID
	runs, err := app.knowHowRecentRuns(context.Background(), time.Now())
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	now := time.Now().Add(-30 * time.Second).UnixMilli()
	session := ActivitySessionRow{ID: "other-current", StartTime: now, EndTime: now + 1000, AppNames: `["Code"]`, EventCount: 1, TriageStatus: "done"}
	saveTimelineTestSession(t, app.activity.store, session, []activitymon.ActivityEvent{{AppName: "Code", AppBundleID: "com.microsoft.VSCode", Timestamp: now, Kind: activitymon.EventClick, ClickLabel: "Run"}})
	otherRuns, err := app.knowHowRecentRuns(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var other ActivityTimelineRunRow
	for _, run := range otherRuns {
		if run.AppName == "Code" {
			other = run
		}
	}
	if other.ID == "" {
		t.Fatal("missing other moment")
	}
	other.EvidenceTokens = runs[0].EvidenceTokens
	runs = append(runs, other)
	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='current-session'`); err != nil {
		t.Fatal(err)
	}
	entry, err := app.GetKnowledgeEntry(match.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	// A second readable app with the same signature cannot authorize the
	// withdrawn origin or preserve its app name in the published offer.
	got, err := app.publishKnowHowMatch(context.Background(), match, runs, map[string]KnowledgeEntry{entry.ID: entry}, originID)
	if err != nil || got != nil {
		t.Fatalf("withdrawn origin published: %+v err=%v", got, err)
	}
}

type knowHowFailingRunner struct {
	memoryTranslationFakeRunner
	calls atomic.Int64
}

func (r *knowHowFailingRunner) ExecuteTask(ctx context.Context, input *agent.TaskInput, callback agent.StreamCallback) (string, error) {
	r.calls.Add(1)
	return "", errors.New("gateway is not answering")
}

// The backfill failed on every launch on Bo's computer, and the app restarts
// many times a day: three classifier calls per launch, forever. Over a
// simulated day with a launch every hour and a failing model, it now calls the
// model once, and again only the next day.
func TestKnowHowBackfillCallsTheModelOnceADayWhileItFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := newTranslationTestApp(t, &memoryTranslationFakeRunner{})
	runner := &knowHowFailingRunner{}
	app.runner = runner
	writeKnowHowFixture(t, "old", buildOKFKnowledgeMarkdown("old", "Fact", "", time.Now(), "conversation", nil, nil, nil))
	start := time.Now()
	clock := start
	previous := knowHowBackfillNow
	knowHowBackfillNow = func() time.Time { return clock }
	t.Cleanup(func() { knowHowBackfillNow = previous })
	for hour := 0; hour < 24; hour++ {
		clock = start.Add(time.Duration(hour) * time.Hour)
		app.runKnowHowKindBackfill(context.Background())
	}
	if n := runner.calls.Load(); n != 1 {
		t.Fatalf("a failing backfill called the model %d times in a day of hourly launches; want 1", n)
	}
	clock = start.Add(cadenceKnowHowBackfill)
	app.runKnowHowKindBackfill(context.Background())
	if n := runner.calls.Load(); n != 2 {
		t.Fatalf("the next day's launch did not try again (%d calls)", n)
	}
}
