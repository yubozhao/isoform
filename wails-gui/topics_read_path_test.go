package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/privacy"
)

func newTopicsReadPathTestApp(t *testing.T) (*App, *topicsService) {
	t.Helper()
	isolateActivityPrivacyTest(t)
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	voiceApps := GetUserVoiceInputApps()
	t.Cleanup(func() { SetUserVoiceInputApps(voiceApps) })
	app := newPrivacyReadTestApp(t)
	return app, app.topicsSvc()
}

func readPathSnapshot(count int) *topicSnapshot {
	entries := make([]topicSnapshotEntry, 0, count)
	for i := 0; i < count; i++ {
		app := "com.example.normal"
		switch i {
		case 0:
			app = "com.example.private"
		case 1:
			app = "com.example.private.child"
		case 2:
			app = "com.example.voice"
		}
		refs := make([]topicSourceReference, 10)
		refs[0] = topicSourceReference{Kind: topicRefApp, Value: app}
		for j := 1; j < len(refs); j++ {
			refs[j] = topicSourceReference{Kind: topicRefSession, Value: fmt.Sprintf("session-%d-%d", i, j)}
		}
		entries = append(entries, topicSnapshotEntry{
			Topic: TopicView{ID: fmt.Sprintf("topic-%03d", i), Kind: TopicKindProject, Name: fmt.Sprintf("Research topic %03d", i), Status: TopicStatusToday},
			Refs:  refs,
		})
	}
	return &topicSnapshot{Version: topicSnapshotSchemaVersion, At: time.Now(), Topics: entries}
}

func TestTopicSnapshotReadUnder50ms(t *testing.T) {
	_, svc := newTopicsReadPathTestApp(t)
	pii.SetUserSensitiveApps(map[string]string{"com.example.private": "Private"})
	SetUserVoiceInputApps(map[string]string{"com.example.voice": "Voice"})
	if err := svc.snapshots.Replace(readPathSnapshot(100)); err != nil {
		t.Fatal(err)
	}
	svc.registryWorkerMu.Lock()
	svc.registryRefresh = make(chan struct{}, 1)
	svc.registryFullBuilding = true
	svc.registryWorkerMu.Unlock()
	t.Cleanup(func() {
		svc.registryWorkerMu.Lock()
		svc.registryRefresh = nil
		svc.registryFullBuilding = false
		svc.registryWorkerMu.Unlock()
	})

	latencies := make([]time.Duration, 20)
	for i := range latencies {
		started := time.Now()
		views, refreshing, firstRun := svc.todayViews()
		latencies[i] = time.Since(started)
		if firstRun || !refreshing || len(views) != 97 {
			t.Fatalf("read %d: topics=%d refreshing=%t firstRun=%t", i, len(views), refreshing, firstRun)
		}
		if !raceEnabled && latencies[i] >= 50*time.Millisecond {
			t.Fatalf("read %d took %s", i, latencies[i])
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("snapshot reads: p50=%s max=%s", latencies[len(latencies)/2], latencies[len(latencies)-1])
}

func TestTopicCaptureBurstsNeverClearView(t *testing.T) {
	app, svc := newTopicsReadPathTestApp(t)
	SetUserVoiceInputApps(map[string]string{"com.example.voice": "Voice"})
	if err := svc.snapshots.Replace(readPathSnapshot(4)); err != nil {
		t.Fatal(err)
	}
	requests := make(chan struct{}, 1)
	svc.registryWorkerMu.Lock()
	svc.registryRefresh = requests
	svc.registryWorkerMu.Unlock()
	for batch := 0; batch < 120; batch++ {
		for event := 0; event < 8; event++ {
			if event%2 == 0 {
				app.emitBrowserEvent("activity:session-saved")
			} else {
				app.emitBrowserEvent("activity:activities-updated")
			}
		}
	}
	view, err := app.GetTodayTopics()
	if err != nil || len(view.Topics) != 1 || view.Clear || !view.Stale || len(requests) != 0 {
		t.Fatalf("capture burst view=%+v err=%v queued=%d", view, err, len(requests))
	}
	svc.registryWorkerMu.Lock()
	started := svc.registryLastBuildStart
	svc.registryWorkerMu.Unlock()
	if !started.IsZero() {
		t.Fatalf("capture events started a build at %s", started)
	}
}

func TestTopicWithdrawalScopes(t *testing.T) {
	tests := []struct {
		name  string
		refs  []topicSourceReference
		scope privacy.Scope
		want  bool
	}{
		{"app exact and prefix", []topicSourceReference{{Kind: topicRefApp, Value: "COM.EXAMPLE.private.bundle"}}, privacy.Scope{Kind: privacy.App, ID: "com.example.private"}, true},
		{"window", []topicSourceReference{{Kind: topicRefWindow, Value: "private window"}}, privacy.Scope{Kind: privacy.Window, ID: "private window"}, true},
		{"session", []topicSourceReference{{Kind: topicRefSession, Value: "session-1"}}, privacy.Scope{Kind: privacy.Session, ID: "session-1"}, true},
		{"closed time overlap", []topicSourceReference{{Kind: topicRefTimeRange, Start: 100, End: 200}}, privacy.Scope{Kind: privacy.TimeRange, StartMs: 150, EndMs: 250}, true},
		{"open time overlap", []topicSourceReference{{Kind: topicRefTimeRange, Start: 100, End: 200}}, privacy.Scope{Kind: privacy.TimeRange, StartMs: 150}, true},
		{"half-open edge", []topicSourceReference{{Kind: topicRefTimeRange, Start: 100, End: 200}}, privacy.Scope{Kind: privacy.TimeRange, StartMs: 200, EndMs: 250}, false},
		{"account exact", []topicSourceReference{{Kind: topicRefAccount, Value: "discord"}}, privacy.Scope{Kind: privacy.Account, ID: "discord"}, true},
		{"account prefix", []topicSourceReference{{Kind: topicRefAccount, Value: "discord:guild:123"}}, privacy.Scope{Kind: privacy.Account, ID: "discord"}, true},
		{"account child only", []topicSourceReference{{Kind: topicRefAccount, Value: "discord:guild:123"}}, privacy.Scope{Kind: privacy.Account, ID: "discord:guild"}, true},
		{"activity source", []topicSourceReference{{Kind: topicRefSource, Value: "activity:work"}}, privacy.Scope{Kind: privacy.Source, SourceKind: "activity", ID: "work"}, true},
		{"memory source", []topicSourceReference{{Kind: topicRefSource, Value: "memory:note:version"}}, privacy.Scope{Kind: privacy.Source, SourceKind: "memory", ID: "note"}, true},
		{"file source", []topicSourceReference{{Kind: topicRefSource, Value: "file:123"}}, privacy.Scope{Kind: privacy.Source, SourceKind: "file", ID: "123"}, true},
		{"policy change", []topicSourceReference{{Kind: topicRefApp, Value: "anything"}}, privacy.Scope{Kind: privacy.PolicyChange}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := topicRefsIntersectScope(test.refs, test.scope); got != test.want {
				t.Fatalf("topicRefsIntersectScope()=%t, want %t", got, test.want)
			}
		})
	}

	app, svc := newTopicsReadPathTestApp(t)
	now := time.Now()
	snapshot := &topicSnapshot{Version: topicSnapshotSchemaVersion, At: now, Topics: []topicSnapshotEntry{
		{Topic: TopicView{ID: "removed", Name: "Removed topic"}, Refs: []topicSourceReference{{Kind: topicRefSource, Value: "activity:deleted"}}},
		{Topic: TopicView{ID: "survivor", Name: "Surviving topic"}, Refs: []topicSourceReference{{Kind: topicRefSource, Value: "activity:survivor"}}},
	}}
	if err := svc.snapshots.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	svc.registryWorkerMu.Lock()
	svc.registryRefresh = make(chan struct{}, 1)
	svc.registryFullBuilding = true
	svc.registryWorkerMu.Unlock()
	svc.applyPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: "activity", ID: "deleted"})
	view, err := app.GetTodayTopics()
	if err != nil || len(view.Topics) != 1 || view.Topics[0].ID != "survivor" || view.Clear {
		t.Fatalf("withdrawal getter=%+v err=%v", view, err)
	}
	persisted := newTopicSnapshotStore(filepath.Dir(svc.snapshots.path)).Load()
	if persisted == nil || len(persisted.Topics) != 1 || persisted.Topics[0].Topic.ID != "survivor" {
		t.Fatalf("persisted snapshot after withdrawal: %+v", persisted)
	}
	candidate := &topicSnapshot{Version: topicSnapshotSchemaVersion, At: now.Add(time.Minute), Topics: append(snapshot.Topics, topicSnapshotEntry{
		Topic: TopicView{ID: "other", Name: "Other topic"}, Refs: []topicSourceReference{{Kind: topicRefSource, Value: "activity:deleted:child"}},
	})}
	svc.commitBuiltSnapshot(candidate, true, false)
	persisted = svc.snapshots.Current()
	if persisted == nil || len(persisted.Topics) != 1 || persisted.Topics[0].Topic.ID != "survivor" {
		t.Fatalf("in-flight build republished withdrawn topic: %+v", persisted)
	}
	svc.registryWorkerMu.Lock()
	due := svc.registryNextRefresh
	svc.registryWorkerMu.Unlock()
	if due.Before(now.Add(topicWithdrawalRebuildDelay)) {
		t.Fatalf("withdrawal scheduled too early: due=%s start=%s", due, now)
	}
}

func TestTopicWithdrawalRebuildWithinOneSecond(t *testing.T) {
	_, svc := newTopicsReadPathTestApp(t)
	if err := svc.snapshots.Replace(&topicSnapshot{
		Version: topicSnapshotSchemaVersion,
		At:      time.Now(),
		Topics:  []topicSnapshotEntry{{Topic: TopicView{ID: "kept", Name: "Kept topic"}}},
	}); err != nil {
		t.Fatal(err)
	}
	timer := make(chan time.Time, 1)
	svc.registryAfter = func(time.Duration) <-chan time.Time { return timer }
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{}, 1)
	svc.registryDuringCollect = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
	}
	svc.startRegistryWorker(ctx)
	defer func() {
		cancel()
		svc.stopRegistryWorker()
	}()
	svc.applyPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: "activity", ID: "gone"})
	timer <- time.Now()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("withdrawal did not start a rebuild within one second")
	}
}

func TestTopicRefreshIntervalPreservesWithdrawalDeadline(t *testing.T) {
	app, svc := newTopicsReadPathTestApp(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	if err := svc.snapshots.Replace(&topicSnapshot{
		Version: topicSnapshotSchemaVersion,
		At:      now,
		Topics: []topicSnapshotEntry{{
			Topic: TopicView{ID: "kept", Name: "Kept topic"},
			Refs:  []topicSourceReference{{Kind: topicRefSource, Value: "activity:kept"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetTopicRefreshInterval(120); err != nil {
		t.Fatal(err)
	}
	delays := make(chan time.Duration, 8)
	never := make(chan time.Time)
	svc.registryAfter = func(delay time.Duration) <-chan time.Time {
		delays <- delay
		return never
	}
	ctx, cancel := context.WithCancel(t.Context())
	svc.startRegistryWorker(ctx)
	defer func() {
		cancel()
		svc.stopRegistryWorker()
	}()
	readDelay := func() time.Duration {
		t.Helper()
		select {
		case delay := <-delays:
			return delay
		case <-time.After(time.Second):
			t.Fatal("registry timer was not rescheduled")
			return 0
		}
	}
	if delay := readDelay(); delay < time.Hour {
		t.Fatalf("initial background delay=%s", delay)
	}

	svc.applyPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: "activity", ID: "gone"})
	for i := 0; i < 2; i++ {
		if delay := readDelay(); delay > topicWithdrawalRebuildDelay {
			t.Fatalf("withdrawal delay %d=%s", i, delay)
		}
	}
	if _, err := app.SetTopicRefreshInterval(120); err != nil {
		t.Fatal(err)
	}
	if delay := readDelay(); delay > topicWithdrawalRebuildDelay {
		t.Fatalf("refresh interval replaced the pending withdrawal deadline: %s", delay)
	}
}

func TestTopicWithdrawalDuringBuildDropsRefLessCandidate(t *testing.T) {
	app, svc := newTopicsReadPathTestApp(t)
	policyHash, err := currentTopicPolicyHash()
	if err != nil {
		t.Fatal(err)
	}
	initial := &topicSnapshot{
		Version:    topicSnapshotSchemaVersion,
		PolicyHash: policyHash,
		At:         time.Now(),
		Topics: []topicSnapshotEntry{
			{Topic: TopicView{ID: "unproven", Name: "Unproven", PinnedToSidebar: true}, Unproven: true},
			{Topic: TopicView{ID: "survivor", Name: "Survivor", PinnedToSidebar: true}, Refs: []topicSourceReference{{Kind: topicRefApp, Value: "com.example.safe"}}},
		},
	}
	if err := svc.snapshots.Replace(initial); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.update(func(state *topicsStateFile) {
		state.SidebarPins = map[string]bool{"unproven": true, "survivor": true}
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	app.ctx = ctx
	entered, release := make(chan struct{}), make(chan struct{})
	svc.registryBeforeSnapshot = func() {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	svc.startRegistryWorker(ctx)
	app.RefreshTodayTopics()
	t.Cleanup(func() {
		close(release)
		cancel()
		svc.stopRegistryWorker()
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("build did not reach registryBeforeSnapshot")
	}

	svc.applyPrivacyWithdrawal(privacy.Scope{Kind: privacy.App, ID: "com.example.private"})
	candidate := &topicSnapshot{
		Version:    topicSnapshotSchemaVersion,
		PolicyHash: policyHash,
		At:         time.Now(),
		Topics: []topicSnapshotEntry{
			{Topic: TopicView{ID: "unproven", Name: "Unproven", PinnedToSidebar: true}},
			{Topic: TopicView{ID: "survivor", Name: "Survivor", PinnedToSidebar: true}, Refs: []topicSourceReference{{Kind: topicRefApp, Value: "com.example.safe"}}},
		},
	}
	svc.commitBuiltSnapshot(candidate, true, false)

	today, err := app.GetTodayTopics()
	if err != nil || len(today.Topics) != 1 || today.Topics[0].ID != "survivor" {
		t.Fatalf("Today after in-flight withdrawal: %+v %v", today, err)
	}
	sidebar, err := app.ListSidebarTopics()
	if err != nil || len(sidebar) != 1 || sidebar[0].ID != "survivor" {
		t.Fatalf("sidebar after in-flight withdrawal: %+v %v", sidebar, err)
	}
	phone, err := app.todayTopicViews()
	if err != nil || len(phone) != 1 || phone[0].ID != "survivor" {
		t.Fatalf("phone topics after in-flight withdrawal: %+v %v", phone, err)
	}
	persisted := newTopicSnapshotStore(filepath.Dir(svc.snapshots.path)).Load()
	if persisted == nil || len(persisted.Topics) != 1 || persisted.Topics[0].Topic.ID != "survivor" {
		t.Fatalf("persisted snapshot after in-flight withdrawal: %+v", persisted)
	}
}

func TestTopicRefreshTimer(t *testing.T) {
	app, svc := newTopicsReadPathTestApp(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	svc.registryWorkerMu.Lock()
	svc.registryRefresh = make(chan struct{}, 1)
	svc.registryScheduleChanged = make(chan struct{}, 1)
	svc.registryLastBuildStart = now
	svc.registryWorkerMu.Unlock()
	for _, test := range []struct{ input, want int }{{1, 5}, {30, 30}, {999, 120}, {0, 120}} {
		got, err := app.SetTopicRefreshInterval(test.input)
		if err != nil || got != test.want {
			t.Fatalf("SetTopicRefreshInterval(%d)=%d, %v; want %d", test.input, got, err, test.want)
		}
		stored := test.want
		if test.input == 0 {
			stored = 0
		}
		if minutes := svc.store.load().RefreshIntervalMinutes; minutes != stored {
			t.Fatalf("stored refresh interval=%d, want %d", minutes, stored)
		}
		svc.registryWorkerMu.Lock()
		due := svc.registryNextRefresh
		svc.registryWorkerMu.Unlock()
		if !due.Equal(now.Add(time.Duration(test.want) * time.Minute)) {
			t.Fatalf("interval %d scheduled at %s", test.want, due)
		}
	}
}

func TestTopicSnapshotChangeMatters(t *testing.T) {
	base := readPathSnapshot(4)
	base.Topics[0].Topic.Latest = &TopicMoment{Text: "one two three four five six seven eight nine ten"}
	cases := []struct {
		name   string
		change func(*topicSnapshot)
		want   bool
	}{
		{"identical", func(*topicSnapshot) {}, false},
		{"name", func(s *topicSnapshot) { s.Topics[0].Topic.Name += " edited" }, true},
		{"status", func(s *topicSnapshot) { s.Topics[0].Topic.Status = TopicStatusNewStories }, true},
		{"rank shift one", func(s *topicSnapshot) { s.Topics[0], s.Topics[1] = s.Topics[1], s.Topics[0] }, false},
		{"rank shift three", func(s *topicSnapshot) { s.Topics = append(s.Topics[1:], s.Topics[0]) }, true},
		{"id set", func(s *topicSnapshot) { s.Topics[0].Topic.ID = "replacement" }, true},
		{"summary change", func(s *topicSnapshot) {
			s.Topics[0].Topic.Latest = &TopicMoment{Text: "unrelated words completely changed"}
		}, true},
		{"summary stable", func(s *topicSnapshot) {
			s.Topics[0].Topic.Latest = &TopicMoment{Text: "one two three four five six seven eight nine eleven"}
		}, false},
		{"policy hash", func(s *topicSnapshot) { s.PolicyHash[0] = 1 }, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := *base
			candidate.Topics = append([]topicSnapshotEntry(nil), base.Topics...)
			for i := range candidate.Topics {
				candidate.Topics[i] = base.Topics[i]
			}
			test.change(&candidate)
			if got := topicSnapshotChangeMatters(base, &candidate); got != test.want {
				t.Fatalf("topicSnapshotChangeMatters()=%t, want %t", got, test.want)
			}
		})
	}
	if !topicSnapshotChangeMatters(nil, base) || !topicSnapshotChangeMatters(base, nil) {
		t.Fatal("nil/non-nil snapshot transition was not material")
	}
	nextChanged := *base
	nextChanged.Topics = append([]topicSnapshotEntry(nil), base.Topics...)
	nextChanged.Topics[0].Topic.Next = &TopicMoment{Text: "unrelated words completely changed with several distinct tokens"}
	if !topicSnapshotChangeMatters(base, &nextChanged) {
		t.Fatal("material Next summary change was not detected")
	}
}

func TestTopicTimerPublishesOnlyIfDifferent(t *testing.T) {
	_, svc := newTopicsReadPathTestApp(t)
	first := readPathSnapshot(1)
	if !svc.commitBuiltSnapshot(first, false, false) {
		t.Fatal("initial snapshot was not committed")
	}
	unchanged := *first
	unchanged.At = first.At.Add(time.Hour)
	if svc.commitBuiltSnapshot(&unchanged, true, false) {
		t.Fatal("unchanged timer build replaced snapshot")
	}
	if !svc.snapshots.Current().At.Equal(first.At) {
		t.Fatal("unchanged timer build advanced snapshot timestamp")
	}
}

func TestTopicManualRefreshAlwaysPublishes(t *testing.T) {
	_, svc := newTopicsReadPathTestApp(t)
	first := readPathSnapshot(1)
	if !svc.commitBuiltSnapshot(first, false, false) {
		t.Fatal("initial snapshot was not committed")
	}
	manual := *first
	manual.At = first.At.Add(time.Hour)
	if !svc.commitBuiltSnapshot(&manual, true, true) {
		t.Fatal("manual refresh did not replace snapshot")
	}
	if !svc.snapshots.Current().At.Equal(manual.At) {
		t.Fatal("manual refresh did not persist the candidate")
	}
}

func TestTopicColdStartShowsStoredSnapshot(t *testing.T) {
	app, svc := newTopicsReadPathTestApp(t)
	stored := &topicSnapshot{Version: topicSnapshotSchemaVersion, At: time.Now(), Topics: []topicSnapshotEntry{{
		Topic: TopicView{ID: "cold", Name: "Stored cold topic", Status: TopicStatusToday},
		Refs:  []topicSourceReference{{Kind: topicRefSource, Value: "activity:cold"}},
	}}}
	if err := svc.snapshots.Replace(stored); err != nil {
		t.Fatal(err)
	}
	restarted := &App{store: app.store, activity: app.activity}
	restartedSvc := restarted.topicsSvc()
	restartedSvc.registryWorkerMu.Lock()
	restartedSvc.registryRefresh = make(chan struct{}, 1)
	restartedSvc.registryFullBuilding = true
	restartedSvc.registryWorkerMu.Unlock()
	view, err := restarted.GetTodayTopics()
	if err != nil || len(view.Topics) != 1 || view.Clear || !view.Refreshing {
		t.Fatalf("cold start view=%+v err=%v", view, err)
	}
}
