package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/privacy"
)

func TestTodayTopicsFirstRunFlag(t *testing.T) {
	app, svc := newTopicsReadPathTestApp(t)
	first, err := app.GetTodayTopics()
	if err != nil || !first.FirstRun || first.Clear {
		t.Fatalf("first read=%+v err=%v", first, err)
	}

	pii.SetUserSensitiveApps(map[string]string{"com.example.private": "Private"})
	if err := svc.snapshots.Replace(&topicSnapshot{
		Version: topicSnapshotSchemaVersion,
		At:      time.Now(),
		Topics: []topicSnapshotEntry{{
			Topic: TopicView{ID: "filtered", Name: "Filtered topic"},
			Refs:  []topicSourceReference{{Kind: topicRefApp, Value: "com.example.private"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	published, err := app.GetTodayTopics()
	if err != nil || published.FirstRun || !published.Clear || len(published.Topics) != 0 {
		t.Fatalf("published empty read=%+v err=%v", published, err)
	}
}

func TestTodayTopicsLogPatternCapturesNeverClear(t *testing.T) {
	app, svc := newTopicsReadPathTestApp(t)
	pii.SetUserSensitiveApps(map[string]string{})
	SetUserVoiceInputApps(map[string]string{})
	svc.commitBuiltSnapshot(readPathSnapshot(12), true, false)
	initial, err := app.GetTodayTopics()
	if err != nil || len(initial.Topics) != 12 || initial.Clear {
		t.Fatalf("initial full build=%+v err=%v", initial, err)
	}

	requests := make(chan struct{}, 1)
	svc.registryWorkerMu.Lock()
	svc.registryRefresh = requests
	svc.registryFullBuilding = true
	initialBuildStart := svc.registryLastBuildStart
	svc.registryWorkerMu.Unlock()
	t.Cleanup(func() {
		svc.registryWorkerMu.Lock()
		svc.registryRefresh = nil
		svc.registryFullBuilding = false
		svc.registryWorkerMu.Unlock()
	})

	var logs bytes.Buffer
	priorOutput := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(priorOutput)

	var previousSessions int
	if err := app.activity.store.db.QueryRow(`SELECT COUNT(*) FROM activity_sessions`).Scan(&previousSessions); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.January, 2, 16, 2, 0, 0, time.UTC)
	for batch := 0; batch < 20; batch++ {
		at := base.Add(time.Duration(batch) * 9 * time.Second).UnixMilli()
		session := ActivitySessionRow{
			ID:           fmt.Sprintf("topic-read-capture-%02d", batch),
			StartTime:    at,
			EndTime:      at + 1_000,
			TriggerKind:  "test",
			AppNames:     `["Code"]`,
			TriageStatus: "pending",
		}
		if err := app.activity.store.SaveSession(session, nil, nil); err != nil {
			t.Fatalf("save capture batch %d: %v", batch, err)
		}
		app.emitBrowserEvent("activity:session-saved", session.ID)

		var sessionCount int
		if err := app.activity.store.db.QueryRow(`SELECT COUNT(*) FROM activity_sessions`).Scan(&sessionCount); err != nil {
			t.Fatal(err)
		}
		if sessionCount != previousSessions+1 {
			t.Fatalf("capture batch %d did not advance activity sessions: before=%d after=%d", batch, previousSessions, sessionCount)
		}
		previousSessions = sessionCount

		view, err := app.GetTodayTopics()
		if err != nil || len(view.Topics) != 12 || view.Clear || !view.Stale {
			t.Fatalf("capture batch %d read=%+v err=%v", batch, view, err)
		}
		svc.registryWorkerMu.Lock()
		buildStart := svc.registryLastBuildStart
		svc.registryWorkerMu.Unlock()
		if buildStart != initialBuildStart || len(requests) != 0 {
			t.Fatalf("capture batch %d triggered a build: start=%s queued=%d", batch, buildStart, len(requests))
		}
	}

	logText := logs.String()
	if strings.Contains(logText, "registry_read_failed") {
		t.Fatalf("registry read failed:\n%s", logText)
	}
	readLines := 0
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, "stage=read") {
			continue
		}
		readLines++
		if strings.Contains(line, "read_count=0") || strings.Contains(line, "clear=true") ||
			!strings.Contains(line, "read_count=12") || !strings.Contains(line, "clear=false") {
			t.Fatalf("unstable topic read log: %s", line)
		}
	}
	if readLines != 20 {
		t.Fatalf("stage=read lines=%d, want 20:\n%s", readLines, logText)
	}
}

func TestTodayTopicsReadLatencyUnder50ms(t *testing.T) {
	app, svc := newTopicsReadPathTestApp(t)
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

	var worst time.Duration
	for read := 0; read < 20; read++ {
		started := time.Now()
		view, err := app.GetTodayTopics()
		elapsed := time.Since(started)
		if elapsed > worst {
			worst = elapsed
		}
		if err != nil || len(view.Topics) != topicRegistryLimit || !view.Refreshing || view.FirstRun {
			t.Fatalf("read %d=%+v err=%v", read, view, err)
		}
		if !raceEnabled && elapsed >= 50*time.Millisecond {
			t.Fatalf("read %d took %s", read, elapsed)
		}
	}
	t.Logf("GetTodayTopics end-to-end worst=%s", worst)
}

func TestTopicWithdrawalAcrossTodayChatAndSearch(t *testing.T) {
	app, svc := newTopicsReadPathTestApp(t)
	pii.SetUserSensitiveApps(map[string]string{})
	SetUserVoiceInputApps(map[string]string{})
	if err := svc.snapshots.Replace(&topicSnapshot{
		Version: topicSnapshotSchemaVersion,
		At:      time.Now(),
		Topics: []topicSnapshotEntry{
			{
				Topic: TopicView{ID: "topic-a", Kind: TopicKindProject, Name: "Atlas", Status: TopicStatusToday, PinnedToSidebar: true},
				Refs:  []topicSourceReference{{Kind: topicRefApp, Value: "com.example.private"}},
			},
			{
				Topic: TopicView{ID: "topic-b", Kind: TopicKindProject, Name: "Beta", Status: TopicStatusToday, PinnedToSidebar: true},
				Refs:  []topicSourceReference{{Kind: topicRefApp, Value: "com.example.unrelated"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.update(func(state *topicsStateFile) {
		state.SidebarPins = map[string]bool{"topic-a": true, "topic-b": true}
	}); err != nil {
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

	initial, err := app.GetTodayTopics()
	if err != nil || len(initial.Topics) != 2 {
		t.Fatalf("initial Today/chat/search topics=%+v err=%v", initial, err)
	}
	initialSidebar, err := app.ListSidebarTopics()
	if err != nil || len(initialSidebar) != 2 {
		t.Fatalf("initial sidebar=%+v err=%v", initialSidebar, err)
	}
	initialRegistry, err := svc.registryViews(context.Background())
	if err != nil || len(initialRegistry.views) != 2 {
		t.Fatalf("initial chat registry=%+v err=%v", initialRegistry, err)
	}

	withdrawnAt := time.Now()
	svc.applyPrivacyWithdrawal(privacy.Scope{Kind: privacy.App, ID: "com.example.private"})
	view, err := app.GetTodayTopics()
	if err != nil || view.Clear || len(view.Topics) != 1 || view.Topics[0].ID != "topic-b" {
		t.Fatalf("withdrawn Today/chat/search topics=%+v err=%v", view, err)
	}
	sidebar, err := app.ListSidebarTopics()
	if err != nil || len(sidebar) != 1 || sidebar[0].ID != "topic-b" {
		t.Fatalf("withdrawn sidebar=%+v err=%v", sidebar, err)
	}
	registry, err := svc.registryViews(context.Background())
	if err != nil || len(registry.views) != 1 || registry.views[0].ID != "topic-b" {
		t.Fatalf("withdrawn chat registry=%+v err=%v", registry, err)
	}
	if _, ok := svc.TopicByID("topic-a"); ok {
		t.Fatal("withdrawn topic remains in registryViews-backed chat lookup")
	}
	if topic, ok := svc.TopicByID("topic-b"); !ok || topic.Name != "Beta" {
		t.Fatalf("surviving chat lookup=%+v ok=%t", topic, ok)
	}
	if time.Since(withdrawnAt) >= time.Second {
		t.Fatalf("withdrawal became visible after %s", time.Since(withdrawnAt))
	}
}
