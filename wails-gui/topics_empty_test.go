package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"wails-gui/internal/interests"
)

// Same persisted shape reported by the app runner, with real SQLite source
// provenance only. Recent unnamed work pushes genuine historical capture
// contributors out of the newest batch; file refresh dates grant no authority.
func populatedTodayTopicsFixture(t *testing.T) *App {
	t.Helper()
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	a := newThreadTestApp(t)
	now := time.Now()
	configureTopicRegistryTest(a, now)
	a.topicsSvc().registryTestReadBudget = time.Duration(topicColdProofBudgetMillis) * time.Millisecond
	var rows []ActivityRow
	var following []interests.Interest
	for i := 0; i < 47; i++ {
		id, name := fmt.Sprintf("work-%02d", i), fmt.Sprintf("Atlas%02d release", i)
		at := now.Add(-time.Duration(2+i%6) * 24 * time.Hour)
		rows = append(rows, seedThreadMoment(t, a, threadFixtureMoment{id: id, title: name, app: "Code", start: at.Add(-time.Minute), end: at, entities: []Entity{{Type: entityProject, Name: name}}}))
		if i < 15 {
			item := testInterest(fmt.Sprintf("interest-%02d", i), fmt.Sprintf("Optical camera modules %02d", i), "Current research")
			item.Kind = interests.KindResearch
			item.Evidence = []interests.Ref{{Key: "activity:" + id, Source: interests.SourceActivity, At: at.UnixMilli()}}
			following = append(following, item)
		}
	}
	storeTestInterests(t, a, following...)
	if _, err := a.interestsSvc().store.update(func(state *interestsStateFile) {
		state.RefreshedAt = now.Add(-time.Hour).UTC().Format(time.RFC3339)
		state.ConsolidatedAt = now.Add(-14 * 24 * time.Hour).UTC().Format(time.RFC3339)
		state.RunsOn, state.RunsToday = now.Format("2006-01-02"), 1
	}); err != nil {
		t.Fatal(err)
	}
	a.interestsSvc().readFilter = a.interestsReadFilter
	initial := buildTopicRegistry(topicRegistryData{activities: rows, now: now})
	if len(initial.projects) != 47 {
		t.Fatalf("fixture projects: %d", len(initial.projects))
	}
	if _, err := a.topicsSvc().store.update(func(state *topicsStateFile) {
		state.Projects = initial.projects
		state.ChatTopics = make([]topicChatSnapshot, 46)
		for i := range state.ChatTopics {
			state.ChatTopics[i].Topic = ChatTopic{ID: initial.projects[i].ID, Name: initial.projects[i].Name}
		}
		state.Today = &topicTodaySnapshot{Views: initial.views, State: "unverified", At: now, Until: now.Add(time.Hour)}
		state.Finds = topicFindsState{StartedOn: "fixture", StartedAt: "fixture", RunID: "fixture", RanOn: "fixture", Unavailable: true, Via: "fixture", Manual: true}
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		at := now.Add(-time.Duration(i+1) * time.Minute)
		seedThreadMoment(t, a, threadFixtureMoment{id: fmt.Sprintf("unnamed-%d", i), title: "Reading", app: "Code", start: at.Add(-time.Second), end: at})
	}
	// Thousands of captures over weeks and unclassified recent activities
	// keep the real source query window full; known projects need history lookup.
	if _, err := a.activity.store.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<3000)
        INSERT INTO activity_sessions(id,start_time,end_time,app_names,triage_status,trigger_kind,event_count,segment_count,created_at)
        SELECT 'empty-capture-'||x,?-((x%42)*86400000),?-((x%42)*86400000),'[]','pending','test',0,0,? FROM n`, now.Add(-time.Minute).UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.activity.store.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<3000)
        INSERT INTO activities(id,day_start_ms,start_ms,end_ms,title,status,run_ids,active_ms_by_app,entities,created_at,updated_at)
        SELECT 'recent-unclassified-'||x,seed.day_start_ms,seed.start_ms,seed.end_ms,seed.title,seed.status,seed.run_ids,seed.active_ms_by_app,seed.entities,seed.created_at,seed.updated_at
        FROM n CROSS JOIN activities seed WHERE seed.id='unnamed-0'`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(a.topicsSvc().store.path)
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Today      map[string]json.RawMessage
		Finds      map[string]json.RawMessage
		Projects   []json.RawMessage
		ChatTopics []json.RawMessage
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	if len(shape.Today) != 10 || len(shape.Finds) != 7 || len(shape.Projects) != 47 || len(shape.ChatTopics) != 46 || len(a.interestsSvc().store.load().Interests) != 15 {
		t.Fatalf("fixture shape: today=%d finds=%d projects=%d chat=%d interests=%d", len(shape.Today), len(shape.Finds), len(shape.Projects), len(shape.ChatTopics), len(a.interestsSvc().store.load().Interests))
	}
	return a
}

// Keep publication out of cold-reader assertions without relying on timing.
func holdTodayTopicsBuild(t *testing.T, a *App) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	a.ctx = ctx
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	a.topicsSvc().registryBeforeSnapshot = func() {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	a.topicsSvc().startRegistryWorker(ctx)
	a.RefreshTodayTopics() // This helper explicitly holds a build, even with fresh saved topics.
	t.Cleanup(func() { unblock(); cancel(); a.topicsSvc().stopRegistryWorker() })
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("builder never entered snapshot")
	}
	return unblock
}

func TestTodayColdCapturedHistoryVerifiesLabels(t *testing.T) {
	a := populatedTodayTopicsFixture(t)
	views, err := collectAndReadTopicSnapshotViewsForTest(a)
	if err != nil {
		t.Fatal(err)
	}
	following := 0
	for _, item := range a.interestsSvc().store.load().Interests {
		if topicViewsRepresentInterest(a, views, item.ID) {
			following++
		}
	}
	if following != 15 {
		t.Fatalf("historical followed labels verified: %d", following)
	}
	if got := a.interestsSvc().list(); len(got) != 15 {
		t.Fatalf("ordinary historical sources failed the full privacy verifier: %d", len(got))
	}
	if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id LIKE 'session-work-%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.topicsSvc().collectRegistryViewsLocked(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	response, err := a.GetTodayTopics()
	if err != nil || len(response.Topics) != 0 {
		t.Fatalf("protected historical sources must still withdraw: %+v %v", response, err)
	}
}

func TestTodayColdSemanticCapturedHistoryVerifiesLabels(t *testing.T) {
	a := populatedTodayTopicsFixture(t)
	stored := a.interestsSvc().store.load()
	for i := range stored.Interests {
		id := fmt.Sprintf("semantic-%02d", i)
		session := fmt.Sprintf("session-work-%02d", i)
		at := stored.Interests[i].Evidence[0].At
		refs, _ := json.Marshal(semanticSourceRefs{SessionIDs: []string{session}})
		if err := a.activity.store.InsertSemanticEvent(SemanticEventRow{ID: id, Title: "Camera research", Apps: `["Code"]`, SourceRefs: string(refs), StartMs: at - 60000, EndMs: at}); err != nil {
			t.Fatal(err)
		}
		stored.Interests[i].Evidence[0].Key = "semantic:" + id
	}
	if _, err := a.interestsSvc().store.update(func(state *interestsStateFile) { state.Interests = stored.Interests }); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]bool {
		t.Helper()
		following := map[string]bool{}
		views, err := collectAndReadTopicSnapshotViewsForTest(a)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range stored.Interests {
			if topicViewsRepresentInterest(a, views, item.ID) {
				following[item.ID] = true
			}
		}
		return following
	}
	if got := read(); len(got) != 15 {
		t.Fatalf("historical semantic labels verified: %d", len(got))
	}
	if got := a.interestsSvc().list(); len(got) != 15 {
		t.Fatalf("ordinary semantic captures failed full privacy verification: %d", len(got))
	}
	// A missing snapshot cannot be authorized just because its session is
	// readable; the complete original capture closure must still resolve.
	if _, err := a.activity.store.db.Exec(`UPDATE activity_semantic_events SET source_refs='{"session_ids":["session-work-00"],"snapshot_ids":[987654321]}' WHERE id='semantic-00'`); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 14 || got[stored.Interests[0].ID] || !got[stored.Interests[1].ID] {
		t.Fatalf("missing semantic snapshot was admitted, or unrelated labels lost: %+v", got)
	}
	if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id LIKE 'session-work-%'`); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("protected semantic captures leaked %d labels", len(got))
	}
}

func TestTodayLastTopicRemovalIsAuthoritativeEmpty(t *testing.T) {
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	a := newThreadTestApp(t)
	now := time.Now()
	configureTopicRegistryTest(a, now)
	seedThreadMoment(t, a, threadFixtureMoment{id: "reading", title: "Reading", app: "Code", start: now.Add(-time.Hour), end: now.Add(-time.Minute)})
	item := testInterest("last-followed", "Optical camera modules", "Current research")
	item.Evidence = []interests.Ref{{Key: "activity:reading", Source: interests.SourceActivity, At: now.Add(-time.Minute).UnixMilli()}}
	storeTestInterests(t, a, item)
	a.interestsSvc().readFilter = a.interestsReadFilter
	if _, err := collectAndReadTopicViewsForTest(a); err != nil {
		t.Fatal(err)
	}
	first, err := a.GetTodayTopics()
	if err != nil || len(first.Topics) != 1 || !first.HasTopics {
		t.Fatalf("readable control: %+v %v", first, err)
	}
	if _, err := a.UpdateUserTopic(UserTopicUpdate{ID: item.ID, Action: "remove"}); err != nil {
		t.Fatal(err)
	}
	if len(a.interestsSvc().store.load().Interests) != 1 || !a.topicsSvc().store.load().Overrides[item.ID].Removed {
		t.Fatal("removal must retain the stored interest for Restore")
	}
	last, err := a.GetTodayTopics()
	if err != nil || last.Refreshing || last.HasTopics || len(last.Topics) != 0 {
		t.Fatalf("last removal is not withheld: %+v %v", last, err)
	}
	if _, err := a.UpdateUserTopic(UserTopicUpdate{ID: item.ID, Action: "restore"}); err != nil {
		t.Fatal(err)
	}
	restored, err := a.GetTodayTopics()
	if err != nil || restored.Refreshing || !restored.HasTopics || len(restored.Topics) != 1 {
		t.Fatalf("Restore must recover the retained topic: %+v %v", restored, err)
	}
}

func TestTodayFullBuildKeepsLargeReadableFollowingClosure(t *testing.T) {
	a := populatedTodayTopicsFixture(t)
	stored := a.interestsSvc().store.load()
	stored.Interests[0].Evidence = nil
	for i := 1; i <= topicTodayHistoryLimit+1; i++ {
		stored.Interests[0].Evidence = append(stored.Interests[0].Evidence, interests.Ref{Key: fmt.Sprintf("activity:recent-unclassified-%d", i), Source: interests.SourceActivity, At: a.topicsSvc().now().Add(-time.Minute).UnixMilli()})
	}
	if _, err := a.interestsSvc().store.update(func(state *interestsStateFile) { state.Interests = stored.Interests }); err != nil {
		t.Fatal(err)
	}
	views, err := collectAndReadTopicSnapshotViewsForTest(a)
	if err != nil {
		t.Fatal(err)
	}
	hasLarge := topicViewsRepresentInterest(a, views, stored.Interests[0].ID)
	hasValid := topicViewsRepresentInterest(a, views, stored.Interests[1].ID)
	if !hasLarge || !hasValid {
		t.Fatalf("full build omitted the large or valid following label: large=%t valid=%t views=%+v", hasLarge, hasValid, views)
	}
}

func TestTodaySnapshotReadsWhileRefreshWaitsOnPrivacyStore(t *testing.T) {
	a := populatedTodayTopicsFixture(t)
	svc := a.topicsSvc()
	if _, err := collectAndReadTopicViewsForTest(a); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	a.ctx = ctx
	a.eventBroadcaster = NewEventBroadcaster()
	events := a.eventBroadcaster.AddClient("fixture")
	entered, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	svc.registryDuringCollect = func() {
		once.Do(func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	}
	svc.startRegistryWorker(ctx)
	a.RefreshTodayTopics()
	t.Cleanup(func() { unblock(); cancel(); svc.stopRegistryWorker() })
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("builder never started")
	}
	// Ordinary historical labels now verify on bootstrap. Keep this guard's
	// withheld-all case by deterministically making pause history unavailable
	// until the bounded read finishes, then let full publication proceed.
	releasePrivacy := blockStateStoreReads(t, a.store)
	t.Cleanup(releasePrivacy)
	var first TodayTopicsView
	elapsed := coldReadBestOfThree(t, func() {
		var err error
		first, err = a.GetTodayTopics()
		if err != nil || len(first.Topics) == 0 || !first.Refreshing || !first.HasTopics || first.Clear {
			t.Fatalf("stored snapshot during refresh: %+v err=%v", first, err)
		}
	})
	releasePrivacy()
	if elapsed >= 3*time.Duration(topicColdProofBudgetMillis)*time.Millisecond {
		t.Fatalf("withheld bootstrap exceeded budget: best=%s", elapsed)
	}
	t.Logf("stored snapshot read: topics=%d refreshing=%t best=%s", len(first.Topics), first.Refreshing, elapsed)
	unblock()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case raw := <-events:
			var event BrowserEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Event != "topics:updated" {
				continue
			}
			full, err := a.GetTodayTopics()
			if err != nil {
				t.Fatal(err)
			}
			// A coalesced successor may briefly hold the read fence. A partial
			// read is not authoritative emptiness; wait for its publication.
			if full.Refreshing {
				continue
			}
			if len(full.Topics) != topicRegistryLimit || !full.HasTopics || len(svc.store.load().Projects) != 47 {
				t.Fatalf("published shape: %+v", full)
			}
			t.Logf("published full topics=%d refreshing=%t", len(full.Topics), full.Refreshing)
			return
		case <-deadline.C:
			t.Fatal("full topics never replaced the withheld bootstrap")
		}
	}
}

func TestTodayDistinguishesKnownWithheldFromAnEmptyStore(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(fmt.Sprint(known), func(t *testing.T) {
			var a *App
			if known {
				a = populatedTodayTopicsFixture(t)
				if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id LIKE 'session-work-%'`); err != nil {
					t.Fatal(err)
				}
				if got := a.interestsSvc().list(); len(got) != 0 {
					t.Fatalf("privacy control admitted %d interests", len(got))
				}
			} else {
				t.Setenv(topicsStateDirEnvVar, t.TempDir())
				a = newThreadTestApp(t)
				configureTopicRegistryTest(a, time.Now())
				if _, err := a.topicsSvc().store.update(func(state *topicsStateFile) {
					state.Projects = []topicProject{{ID: "forgotten-reservation"}}
				}); err != nil {
					t.Fatal(err)
				}
			}
			response, err := a.GetTodayTopics()
			if err != nil || response.Refreshing || len(response.Topics) != 0 {
				t.Fatalf("completed read: %+v %v", response, err)
			}
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				HasTopics bool `json:"hasTopics"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.HasTopics != known {
				t.Fatalf("known=%t full empty response lost record presence: %s", known, raw)
			}
		})
	}
}
