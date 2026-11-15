package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/interests"
)

type topicStageLog struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *topicStageLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *topicStageLog) count(stage string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.Buffer.String(), stage)
}

func TestTodayColdSettlesWithoutReadDrivenBuilds(t *testing.T) {
	a := populatedTodayTopicsFixture(t)
	svc := a.topicsSvc()
	svc.registryTestReadBudget = time.Duration(topicColdProofBudgetMillis) * time.Millisecond
	items := a.interestsSvc().store.load().Interests[:10]
	for i := 10; i < 62; i++ {
		item := testInterest(fmt.Sprintf("withheld-%02d", i), fmt.Sprintf("Withheld research %02d", i), "Research")
		item.Kind = interests.KindResearch
		item.Evidence = []interests.Ref{{Source: interests.SourceActivity, Key: fmt.Sprintf("activity:missing-%02d", i)}}
		items = append(items, item)
	}
	storeTestInterests(t, a, items...)
	if _, err := collectAndReadTopicViewsForTest(a); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	a.ctx = ctx
	a.eventBroadcaster = NewEventBroadcaster()
	events := a.eventBroadcaster.AddClient("settle")
	entered, collect := make(chan struct{}), make(chan struct{})
	chatEntered, chatRelease := make(chan struct{}), make(chan struct{})
	var startOnce, chatOnce, releaseOnce, chatReleaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(collect) }) }
	unblockChat := func() { chatReleaseOnce.Do(func() { close(chatRelease) }) }
	var builds atomic.Int32
	svc.registryBeforeSnapshot = func() {
		builds.Add(1)
		startOnce.Do(func() { close(entered) })
		select {
		case <-collect:
		case <-ctx.Done():
		}
	}
	var expire context.CancelFunc
	svc.registryChatContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		child, stop := context.WithCancel(parent)
		expire = stop
		return child, stop
	}
	svc.registryDuringChat = func(string) {
		chatOnce.Do(func() {
			close(chatEntered)
			select {
			case <-chatRelease:
			case <-ctx.Done():
			}
			expire() // Display completion must not depend on auxiliary chat finishing.
		})
	}
	var stages topicStageLog
	oldLog := log.Writer()
	log.SetOutput(&stages)
	t.Cleanup(func() { log.SetOutput(oldLog) })
	svc.startRegistryWorker(ctx)
	a.RefreshTodayTopics()
	t.Cleanup(func() { unblock(); unblockChat(); cancel(); svc.stopRegistryWorker() })
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("builder did not start")
	}
	svc.registryWorkerMu.Lock()
	queuedBeforeReads := len(svc.registryRefresh)
	svc.registryWorkerMu.Unlock()
	var first TodayTopicsView
	elapsed := coldReadBestOfThree(t, func() {
		var err error
		first, err = a.GetTodayTopics()
		if err != nil || !first.Refreshing || len(first.Topics) == 0 {
			t.Fatalf("first partial %+v err=%v", first, err)
		}
	})
	if elapsed >= 3*svc.registryTestReadBudget {
		t.Fatalf("first partial exceeded budget: best=%s", elapsed)
	}
	t.Logf("first partial count=%d best=%s", len(first.Topics), elapsed)
	for i := 0; i < 20; i++ {
		got, err := a.GetTodayTopics()
		if err != nil || !got.Refreshing || !reflect.DeepEqual(got.Topics, first.Topics) {
			t.Fatalf("unstable partial at %d: %+v %v", i, got, err)
		}
		if _, ok := a.topicRegistry().TopicByID(first.Topics[0].ID); !ok {
			t.Fatal("cold by-ID reader lost its verified display")
		}
		if _, ok := a.topicRegistry().TopicKeys(first.Topics[0].ID); !ok {
			t.Fatal("published snapshot lost its verified keys during refresh")
		}
	}
	if n := stages.count("stage=bootstrap "); n != 0 {
		t.Errorf("snapshot reads triggered %d bootstrap verifications", n)
	}
	svc.registryWorkerMu.Lock()
	queuedAfterReads := len(svc.registryRefresh)
	svc.registryWorkerMu.Unlock()
	if queuedAfterReads != queuedBeforeReads {
		t.Errorf("cold reads changed queued builds from %d to %d", queuedBeforeReads, queuedAfterReads)
	}
	unblock()
	select {
	case <-chatEntered:
	case <-time.After(15 * time.Second):
		t.Fatal("full collection did not reach chat")
	}
	full, err := a.GetTodayTopics()
	if err != nil || !full.Refreshing || len(full.Topics) != 12 || full.Clear {
		t.Errorf("computed full view not served during auxiliary chat: %+v %v", full, err)
	}
	if c := svc.loadRegistryCache(); c == nil || len(c.result.views) != 57 || len(c.result.projects) != 47 || svc.store.load().Today == nil {
		t.Error("57 full views and 47 projects were not published to cache and disk")
	}
	unblockChat()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case raw := <-events:
			var event BrowserEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			// The build announces its snapshot once before its chat phase, while
			// it still counts as refreshing, and again when it ends. Wait for the
			// end: reading on the first event raced the build's last steps.
			if event.Event != "topics:updated" || svc.registryPending() || svc.registryFullBuildRunning() {
				continue
			}
			for i := 0; i < 25; i++ {
				got, err := a.GetTodayTopics()
				if err != nil || got.Refreshing || !reflect.DeepEqual(got.Topics, full.Topics) {
					t.Fatalf("published read %d: %+v %v", i, got, err)
				}
				if _, ok := a.topicRegistry().TopicKeys(got.Topics[0].ID); !ok {
					t.Fatal("published keys unavailable")
				}
			}
			if builds.Load() != 1 {
				t.Fatalf("read loop ran %d builds; want one", builds.Load())
			}
			t.Logf("published=%d projects=47 builds=%d bootstrap_stages=%d", len(full.Topics), builds.Load(), stages.count("stage=bootstrap "))
			return
		case <-timer.C:
			t.Fatal("published view never settled after chat deadline")
		}
	}
}

func TestTodaySourceBatchesMarkStaleWithoutAddingBuilds(t *testing.T) {
	a := populatedTodayTopicsFixture(t)
	svc := a.topicsSvc()
	if _, err := collectAndReadTopicViewsForTest(a); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	a.ctx = ctx
	a.eventBroadcaster = NewEventBroadcaster()
	events := a.eventBroadcaster.AddClient("coalesced")
	first, second := make(chan struct{}), make(chan struct{})
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	var onceFirst, onceSecond sync.Once
	unblockFirst := func() { onceFirst.Do(func() { close(releaseFirst) }) }
	unblockSecond := func() { onceSecond.Do(func() { close(releaseSecond) }) }
	var builds atomic.Int32
	svc.registryBeforeSnapshot = func() {
		switch n := builds.Add(1); n {
		case 1:
			close(first)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
			}
		case 2:
			close(second)
			select {
			case <-releaseSecond:
			case <-ctx.Done():
			}
		}
	}
	svc.startRegistryWorker(ctx)
	a.RefreshTodayTopics()
	t.Cleanup(func() { unblockFirst(); unblockSecond(); cancel(); svc.stopRegistryWorker() })
	select {
	case <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("first build missing")
	}
	svc.registryWorkerMu.Lock()
	queuedBeforeBurst := len(svc.registryRefresh)
	svc.registryWorkerMu.Unlock()
	for i := 0; i < 40; i++ {
		now := svc.now().UnixMilli() + int64(i)
		if err := a.activity.store.SaveSession(ActivitySessionRow{ID: fmt.Sprintf("settle-live-%d", i), StartTime: now, EndTime: now, AppNames: `["Code"]`, TriageStatus: "pending", TriggerKind: "test"}, nil, nil); err != nil {
			t.Fatal(err)
		}
		a.emitBrowserEvent("activity:session-saved")
	}
	svc.registryWorkerMu.Lock()
	queued := len(svc.registryRefresh)
	svc.registryWorkerMu.Unlock()
	if queued != queuedBeforeBurst || !svc.topicsStale() {
		t.Fatalf("source burst changed pending builds from %d to %d or failed to mark stale", queuedBeforeBurst, queued)
	}
	full, err := a.GetTodayTopics()
	if err != nil || !full.Refreshing || full.Clear || !full.Stale || len(full.Topics) != 12 {
		t.Fatalf("source burst changed the published view: %+v %v", full, err)
	}
	// Capture bursts only mark the published view stale; an explicit refresh
	// can queue a successor while the current build is still running.
	a.RefreshTodayTopics()
	svc.registryWorkerMu.Lock()
	queued = len(svc.registryRefresh)
	svc.registryWorkerMu.Unlock()
	if queued != 1 {
		t.Fatalf("explicit refresh queued %d builds; want one", queued)
	}
	unblockFirst()
	select {
	case <-second:
	case <-time.After(15 * time.Second):
		t.Fatal("coalesced successor missing")
	}
	full, err = a.GetTodayTopics()
	if err != nil || !full.Refreshing || full.Clear || len(full.Topics) != 12 {
		t.Fatalf("published view relabeled pending during next batch: %+v %v", full, err)
	}
	for i := 0; i < 20; i++ {
		got, err := a.GetTodayTopics()
		if err != nil || !got.Refreshing || got.Clear || !reflect.DeepEqual(got.Topics, full.Topics) {
			t.Fatalf("read %d: %+v %v", i, got, err)
		}
	}
	unblockSecond()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-events:
			if svc.registryPending() {
				continue
			}
			if builds.Load() != 2 {
				t.Fatalf("source burst plus reads ran %d builds", builds.Load())
			}
			return
		case <-timer.C:
			t.Fatal("successor did not settle")
		}
	}
}

func TestTodayPartialCacheWithdrawsChangedSourceFiles(t *testing.T) {
	for _, source := range []string{"transcript", "sidecar", "semantic", "retained_sidecar", "retained_large_header"} {
		t.Run(source, func(t *testing.T) {
			a, _, _ := registryChatTestApp(t)
			a.interestsSvc().readFilter = a.interestsReadFilter
			store := a.activity.store
			path := filepath.Join(t.TempDir(), "transcript.md")
			transcript := withTranscriptActivitySources("# Meeting\n- **Session**: session-atlas\n## Transcript\nTest\n", []string{"session-atlas"})
			if source == "retained_sidecar" {
				transcript = "- **Audio**: activity/audio/partial-test.wav\n" + transcript
			}
			if source == "retained_large_header" {
				ids := make([]string, 5000)
				for i := range ids {
					ids[i] = "session-atlas"
				}
				transcript = withTranscriptActivitySources("# Meeting\n- **Session**: session-atlas\n- **Audio**: activity/audio/partial-test.wav\n## Transcript\nTest\n", ids)
			}
			if err := os.WriteFile(path, []byte(transcript), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE activity_sessions SET transcript_path=?,audio_path='partial-test.wav' WHERE id='session-atlas'`, path); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(source, "retained_") {
				if _, err := store.db.Exec(`UPDATE activity_sessions SET audio_path='' WHERE id='session-atlas'`); err != nil {
					t.Fatal(err)
				}
			}
			sidecar := meetingWAVMetaPath(store.AudioDir(), "partial-test.wav")
			writeSidecar := func(protected bool) {
				t.Helper()
				raw, err := json.Marshal(meetingAudioMeta{SessionID: "session-atlas", App: "Code", Protected: protected})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(sidecar, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeSidecar(false)
			if source == "semantic" {
				now := a.topicsSvc().now()
				refs, _ := json.Marshal(semanticSourceRefs{SessionIDs: []string{"session-atlas"}})
				if err := store.InsertSemanticEvent(SemanticEventRow{ID: "historical-semantic", Title: "Research", Apps: `["Code"]`, SourceRefs: string(refs), StartMs: now.Add(-time.Hour).UnixMilli(), EndMs: now.Add(-30 * time.Minute).UnixMilli()}); err != nil {
					t.Fatal(err)
				}
				item := testInterest("semantic-following", "Optical camera modules", "Research")
				item.Kind = interests.KindResearch
				item.Evidence = []interests.Ref{{Source: interests.SourceActivity, Key: "semantic:historical-semantic", At: now.Add(-time.Hour).UnixMilli()}}
				storeTestInterests(t, a, item)
				for i := 0; i < 8; i++ {
					at := now.Add(-time.Duration(i+1) * time.Second)
					seedThreadMoment(t, a, threadFixtureMoment{id: fmt.Sprintf("unnamed-%d", i), title: "Reading", app: "Code", start: at.Add(-time.Second), end: at})
				}
			}
			full, err := collectAndReadTopicViewsForTest(a)
			if err != nil || len(full) != 1 {
				t.Fatalf("explicit build did not verify source closure: %+v %v", full, err)
			}
			if source == "semantic" && topicViewsRepresentInterest(a, full, "semantic-following") {
				t.Fatalf("transcript-backed semantic source was admitted: %+v", full)
			}
			unblock := holdTodayTopicsBuild(t, a)
			before, err := a.GetTodayTopics()
			if err != nil || len(before.Topics) != 1 || !before.Refreshing {
				t.Fatalf("readable control: %+v %v", before, err)
			}
			if source != "sidecar" && !strings.HasPrefix(source, "retained_") {
				if err := os.WriteFile(path, []byte(strings.Replace(transcript, `["session-atlas"]`, `["session-atlas","missing-private-source"]`, 1)), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeSidecar(true)
			}
			after, err := a.GetTodayTopics()
			if err != nil || len(after.Topics) != 1 || after.Clear {
				t.Fatalf("out-of-band change altered saved snapshot: %+v %v", after, err)
			}
			unblock()
			a.topicsSvc().stopRegistryWorker()
			rebuilt, err := collectAndReadTopicViewsForTest(a)
			if err != nil || len(rebuilt) != 0 {
				t.Fatalf("next explicit build retained changed source: %+v %v", rebuilt, err)
			}
			after, err = a.GetTodayTopics()
			if err != nil || len(after.Topics) != 0 || !after.Clear {
				t.Fatalf("changed source survived explicit build: %+v %v", after, err)
			}
		})
	}
}

func TestTodaySnapshotKeepsHeaderViewUntilExplicitBuild(t *testing.T) {
	a, _, _ := registryChatTestApp(t)
	store := a.activity.store
	path := filepath.Join(t.TempDir(), "transcript.md")
	transcript := withTranscriptActivitySources("# Meeting\n- **Session**: session-atlas\n## Transcript\nTest\n", []string{"session-atlas"})
	writeHeader := func(audio string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("- **Audio**: activity/audio/"+audio+"\n"+transcript), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeHeader("old.wav")
	if _, err := store.db.Exec(`UPDATE activity_sessions SET transcript_path=?,audio_path='' WHERE id='session-atlas'`, path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old.wav", "new.wav"} {
		raw, _ := json.Marshal(meetingAudioMeta{SessionID: "session-atlas", App: "Code"})
		if err := os.WriteFile(meetingWAVMetaPath(store.AudioDir(), name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	views, err := collectAndReadTopicViewsForTest(a)
	if err != nil || len(views) != 1 {
		t.Fatalf("explicit build: %+v %v", views, err)
	}
	writeHeader("new.wav")
	first, err := a.GetTodayTopics()
	if err != nil || len(first.Topics) != 1 {
		t.Fatalf("out-of-band header change altered saved snapshot: %+v %v", first, err)
	}
	views, err = collectAndReadTopicViewsForTest(a)
	if err != nil || len(views) != 1 {
		t.Fatalf("stable new header did not recover: %+v %v", views, err)
	}
	raw, _ := json.Marshal(meetingAudioMeta{SessionID: "session-atlas", App: "Code", Protected: true})
	if err := os.WriteFile(meetingWAVMetaPath(store.AudioDir(), "new.wav"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	first, err = a.GetTodayTopics()
	if err != nil || len(first.Topics) != 1 {
		t.Fatalf("protected sidecar changed snapshot before build: %+v %v", first, err)
	}
	views, err = collectAndReadTopicViewsForTest(a)
	if err != nil || len(views) != 0 {
		t.Fatalf("explicit build retained protected sidecar: %+v %v", views, err)
	}
	last, err := a.GetTodayTopics()
	if err != nil || len(last.Topics) != 0 || !last.Clear {
		t.Fatalf("protected sidecar survived explicit build: %+v %v", last, err)
	}
}
