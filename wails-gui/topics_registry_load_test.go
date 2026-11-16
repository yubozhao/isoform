package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/privacy"
)

func TestTopicRegistryEmptyInterestsDoNotCollectHistoricalNames(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprint(populated), func(t *testing.T) {
			t.Setenv(topicsStateDirEnvVar, t.TempDir())
			app := &App{}
			configureTopicRegistryTest(app, interestTestNow)
			if populated {
				storeTestInterests(t, app, testInterest("research", "optical camera modules", "Current research"))
			}
			calls := 0
			svc := app.interestsSvc()
			svc.collect = func(from, now time.Time) interestInput {
				calls++
				return interestInput{From: from, Now: now}
			}
			want := 0
			if populated {
				want = 1
			}
			if got := svc.list(); len(got) != want || calls != want {
				t.Fatalf("interests=%d historical collections=%d want=%d", len(got), calls, want)
			}
		})
	}
}

func TestTopicRegistryBatchedMetadataKeepsCandidateReadGates(t *testing.T) {
	app, _, _ := registryChatTestApp(t)
	if _, err := app.activity.store.db.Exec(`ALTER TABLE activity_timeline_groups RENAME TO unavailable_topic_runs`); err != nil {
		t.Fatal(err)
	}
	oversized, _ := json.Marshal(make([]string, 65))
	for _, tc := range []struct {
		name, apps, runs string
		wantError        bool
	}{
		{name: "sensitive", apps: `{"com.agilebits.onepassword":1000}`, runs: `["unavailable"]`},
		{name: "oversized", apps: `{"Code":1000}`, runs: string(oversized)},
		{name: "eligible_control", apps: `{"Code":1000}`, runs: `["unavailable"]`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := app.filterTopicRegistryActivities(t.Context(), []ActivityRow{{ActiveMsByApp: tc.apps, RunIDs: tc.runs}})
			if (err != nil) != tc.wantError || len(rows) != 0 {
				t.Fatalf("candidate gate: rows=%d err=%v wantError=%t", len(rows), err, tc.wantError)
			}
		})
	}
}

// Real SQLite stores, not integration mocks. The ordinary fixture exceeds the
// collection row budget; the race build uses fewer rows to bound instrumentation
// cost while retaining the same goals, provenance and concurrent-writer guards.
func largeTopicRegistryFixture(t *testing.T) *App {
	return largeTopicRegistryFixtureSized(t, topicRegistryFixtureSessions)
}

func largeTopicRegistryFixtureSized(t *testing.T, sessions int) *App {
	t.Helper()
	t.Setenv(topicsStateDirEnvVar, t.TempDir())
	a := newThreadTestApp(t)
	configureTopicRegistryTest(a, topicRegistryTestNow)
	seedThreadMoment(t, a, threadFixtureMoment{id: "work", title: "Atlas", app: "Code", start: topicRegistryTestNow.Add(-time.Hour), end: topicRegistryTestNow.Add(-time.Minute), entities: []Entity{{Type: entityProject, Name: "Atlas"}}})
	_, err := a.activity.store.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<?)
        INSERT INTO activity_sessions(id,start_time,end_time,app_names,triage_status,trigger_kind,event_count,segment_count,created_at)
        SELECT 'capture-'||x,?,?, '[]','pending','test',0,0,? FROM n`, sessions, topicRegistryTestNow.Add(-time.Hour).UnixMilli(), topicRegistryTestNow.UnixMilli(), topicRegistryTestNow.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.activity.store.db.Exec(`INSERT INTO activity_events(session_id,timestamp,kind,data,sort_order) SELECT id,end_time,'window_focus','{}',0 FROM activity_sessions WHERE id LIKE 'capture-%'`); err != nil {
		t.Fatal(err)
	}

	if _, err := a.activity.store.db.Exec(`INSERT INTO activity_timeline_groups(id,first_event_row_id,last_event_row_id,app_key,app_name,start_ms,end_ms,active_ms,event_count,session_ids,status,summary_status,summarized_event_count,created_at,updated_at)
 SELECT 'load-run-'||s.id,e.id,e.id,'com.example.code','Code',s.start_time,s.end_time,1000,1,json_array(s.id),'closed','done',1,s.created_at,s.created_at FROM activity_sessions s JOIN activity_events e ON e.session_id=s.id WHERE s.id LIKE 'capture-%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.activity.store.db.Exec(`INSERT INTO activities(id,day_start_ms,start_ms,end_ms,title,status,run_ids,active_ms_by_app,entities,created_at,updated_at)
 SELECT 'load-'||s.id,seed.day_start_ms,s.start_time,?,seed.title,seed.status,json_array('load-run-'||s.id),seed.active_ms_by_app,seed.entities,s.created_at,s.created_at FROM activity_sessions s CROSS JOIN activities seed WHERE s.id LIKE 'capture-%' AND seed.id='work'`, topicRegistryTestNow.Add(-2*time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	purpose := NewPurposeMapStore(a.activity.store.db)
	for i := 0; i < 96; i++ {
		input, err := userPurposeCreateInput(userPurposeCreate{Key: fmt.Sprintf("load-%d", i), Origin: "test", Kind: PurposeKindGoal, Text: fmt.Sprintf("Goal %d", i)}, topicRegistryTestNow.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = purpose.Create(input); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func TestTopicRegistryLoadsWhileCaptureKeepsChangingRevision(t *testing.T) {
	a := largeTopicRegistryFixture(t)
	svc := a.topicsSvc()
	before, err := collectAndReadTopicViewsForTest(a)
	if err != nil || len(before) == 0 || !strings.Contains(fmt.Sprint(before), "Atlas") {
		t.Fatalf("snapshot build: topics=%d err=%v", len(before), err)
	}
	var writes atomic.Int32
	svc.registryCollectContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		t.Fatal("snapshot read attempted source revision validation")
		return parent, func() {}
	}
	for i := 0; i < 3; i++ {
		if err := a.activity.store.SaveSession(ActivitySessionRow{ID: fmt.Sprintf("live-%d", i), StartTime: topicRegistryTestNow.UnixMilli(), EndTime: topicRegistryTestNow.UnixMilli(), AppNames: `["Code"]`, TriageStatus: "pending", TriggerKind: "test", EventCount: 1, CreatedAt: topicRegistryTestNow.UnixMilli()}, []activitymon.ActivityEvent{{Timestamp: topicRegistryTestNow.UnixMilli(), Kind: "window_focus"}}, nil); err != nil {
			t.Fatal(err)
		}
		a.emitBrowserEvent("activity:session-saved")
		writes.Add(1)
		after, err := a.todayTopicViews()
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("snapshot read %d changed during capture churn: %d %v", i, len(after), err)
		}
	}
	if writes.Load() != 3 {
		t.Fatalf("writes=%d", writes.Load())
	}
}

func TestTopicRegistrySlowStoreReturnsLastGoodViews(t *testing.T) {
	a := largeTopicRegistryFixture(t)
	before, err := collectAndReadTopicViewsForTest(a)
	if err != nil {
		t.Fatal(err)
	}
	svc := a.topicsSvc()
	// A deterministic slow-store seam: an uncached collection would consume
	// its entire budget. A cache hit must not even start the expensive work.
	calls := 0
	svc.registryCollectContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		calls++
		ctx, cancel := context.WithDeadline(parent, time.Now().Add(-time.Second))
		return ctx, cancel
	}
	started := time.Now()
	after, err := a.todayTopicViews()
	t.Logf("large-store warm cached read: %s", time.Since(started))
	if err != nil || !reflect.DeepEqual(before, after) || calls != 0 {
		t.Fatalf("cached slow store: calls=%d err=%v", calls, err)
	}
	// Cold control proves that the same seam actually expires collection.
	if _, err := svc.collectRegistryViewsLocked(t.Context(), true); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("failed full refresh: calls=%d err=%v", calls, err)
	}
	after, err = a.todayTopicViews()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed refresh erased last good snapshot: %d %v", len(after), err)
	}
}

func TestTopicRegistryLogsUnderlyingFailureOncePerStepAndKind(t *testing.T) {
	for _, step := range []string{"revision", "activities", "goals", "snapshot"} {
		t.Run(step, func(t *testing.T) {
			a := largeTopicRegistryFixture(t)
			svc := a.topicsSvc()
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			t.Cleanup(func() { log.SetOutput(previous) })
			switch step {
			case "revision":
				svc.registryBeforeSnapshot = func() {
					// A file error includes its private path; logs must not.
					svc.registryObservedDB = nil
					a.activity.store.db.Close()
				}
			case "activities":
				svc.registryDuringCollect = func() {
					if _, err := a.activity.store.db.Exec(`DROP TABLE IF EXISTS activities`); err != nil {
						t.Fatal(err)
					}
				}
			case "goals":
				renamed := false
				svc.registryDuringCollect = func() {
					if renamed {
						return
					}
					if _, err := a.activity.store.db.Exec(`ALTER TABLE purpose_open_loop_events_v1 RENAME TO unavailable_goal_events`); err != nil {
						t.Fatal(err)
					}
					renamed = true
				}

			case "snapshot":
				svc.registryBeforePublish = func() {
					if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='session-work'`); err != nil {
						t.Fatal(err)
					}
				}
			}
			for i := 0; i < 2; i++ {
				if _, err := svc.collectRegistryViewsLocked(t.Context(), true); err == nil {
					t.Fatal("failure reported success")
				}
			}
			text := output.String()
			marker := "registry_read_failed step=" + step + " kind="
			if strings.Count(text, marker) != 1 {
				t.Fatalf("missing/duplicate diagnostic for %s: %s", step, text)
			}
			for _, secret := range []string{"Atlas", "Goal ", "session-work", svc.store.path} {
				if strings.Contains(text, secret) {
					t.Fatal("private content in diagnostics")
				}
			}
			if !strings.Contains(text, "elapsed_ms=") || !strings.Contains(text, "activities=") || !strings.Contains(text, "goals=") {
				t.Fatalf("missing timing/counts: %s", text)
			}
		})
	}
}

func TestTopicSnapshotReadsSkipSourceRevisionsAndHonorWithdrawal(t *testing.T) {
	a := largeTopicRegistryFixture(t)
	if _, err := collectAndReadTopicViewsForTest(a); err != nil {
		t.Fatal(err)
	}
	before, err := collectAndReadTopicViewsForTest(a)
	if err != nil || len(before) == 0 {
		t.Fatalf("snapshot control: %d %v", len(before), err)
	}
	svc := a.topicsSvc()
	if _, err := a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id='session-work'`); err != nil {
		t.Fatal(err)
	}
	svc.registryCollectContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		t.Fatal("snapshot read attempted source verification")
		return parent, func() {}
	}
	after, err := a.todayTopicViews()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("source revision changed snapshot read: %d %v", len(after), err)
	}
	svc.applyPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: "activity", ID: "work"})
	after, err = a.todayTopicViews()
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range after {
		if view.ID == before[0].ID {
			t.Fatalf("withdrawn source remained in snapshot: %+v", view)
		}
	}
}

func TestTopicRegistryBackgroundSlowRefreshDoesNotQueueCachedReaders(t *testing.T) {
	a := largeTopicRegistryFixture(t)
	svc := a.topicsSvc()
	var clock atomic.Int64
	clock.Store(topicRegistryTestNow.UnixMilli())
	svc.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	before, err := collectAndReadTopicViewsForTest(a)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	a.ctx = ctx
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	svc.registryCollectContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-parent.Done():
		}
		return parent, func() {}
	}
	t.Cleanup(func() { cancel(); svc.registryMu.Lock(); svc.registryMu.Unlock() })
	clock.Add(int64((6 * time.Second) / time.Millisecond))
	// Force a foreground request to own the mutex while its background
	// retry is still queued. It must reuse the cache rather than duplicate work.
	svc.registryMu.Lock()
	svc.retryRegistryInBackground()
	_, err = svc.registryViewsLocked(t.Context())
	svc.registryMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("background refresh not started")
	}
	done := make(chan error, 1)
	go func() {
		after, err := a.todayTopicViews()
		if err == nil && !reflect.DeepEqual(before, after) {
			err = errors.New("cached views changed")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cached reader queued behind slow store")
	}
	if calls.Load() != 1 {
		t.Fatal("background refresh not single flight")
	}
	// Cancellation, rather than a sleep, ends the slow store and its app-lifecycle
	// retry. The cached views remain intact; no late publication follows shutdown.
	cancel()
	svc.registryMu.Lock()
	if svc.registryRetrying {
		t.Error("background ownership retained after cancellation")
	}
	svc.registryMu.Unlock()
	if svc.loadRegistryCache() == nil {
		t.Fatal("failed refresh erased last good views")
	}
}

func TestTopicRegistryCacheRechecksTranscriptAndRecordingFiles(t *testing.T) {
	for _, source := range []string{"transcript", "sidecar"} {
		t.Run(source, func(t *testing.T) {
			a, _, _ := registryChatTestApp(t)
			store := a.activity.store
			path := filepath.Join(t.TempDir(), "transcript.md")
			transcript := withTranscriptActivitySources("# Meeting\n- **Session**: session-atlas\n## Transcript\nTest\n", []string{"session-atlas"})
			if err := os.WriteFile(path, []byte(transcript), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE activity_sessions SET transcript_path=?,audio_path='cache-test.wav' WHERE id='session-atlas'`, path); err != nil {
				t.Fatal(err)
			}
			sidecar := meetingWAVMetaPath(store.AudioDir(), "cache-test.wav")
			raw, err := json.Marshal(meetingAudioMeta{SessionID: "session-atlas", App: "Code"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sidecar, raw, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := collectAndReadTopicViewsForTest(a)
			if err != nil || len(before) != 1 {
				t.Fatalf("valid source: %d %v", len(before), err)
			}
			switch source {
			case "transcript":
				if err := os.WriteFile(path, []byte(strings.Replace(transcript, `["session-atlas"]`, `["session-atlas","missing-private-source"]`, 1)), 0600); err != nil {
					t.Fatal(err)
				}
			case "sidecar":
				raw, err = json.Marshal(meetingAudioMeta{SessionID: "session-atlas", App: "Code", Protected: true})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(sidecar, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			after, err := collectAndReadTopicViewsForTest(a)
			if len(after) != 0 || (source == "sidecar" && err != nil) {
				t.Fatalf("withdrawn %s served cached text: %d %v", source, len(after), err)
			}
		})
	}
}

func TestTopicRegistryCacheCannotAdoptChoiceAfterPublication(t *testing.T) {
	a, _, _ := registryChatTestApp(t)
	svc := a.topicsSvc()
	before, err := collectAndReadTopicViewsForTest(a)
	if err != nil || len(before) != 1 || before[0].PinnedToSidebar {
		t.Fatalf("original publication: %+v %v", before, err)
	}
	id := before[0].ID
	if _, err := svc.store.update(func(st *topicsStateFile) { st.SidebarPins = map[string]bool{id: true} }); err != nil {
		t.Fatal(err)
	}
	after, err := a.todayTopicViews()
	if err != nil || len(after) != 1 || after[0].ID != id || !after[0].PinnedToSidebar {
		t.Fatalf("late choice lost in cache: %+v %v", after, err)
	}
}

func TestTopicRegistryDiagnosticsSeparatePrivatePathErrorKinds(t *testing.T) {
	for _, cause := range []syscall.Errno{syscall.EACCES, syscall.ENOENT} {
		err := fmt.Errorf("source read: %w", &os.PathError{Op: "open", Path: "private-title/private-path", Err: cause})
		if got, want := topicRegistryErrorKind(err), fmt.Sprintf("os_%d", cause); got != want {
			t.Fatalf("kind=%s want=%s", got, want)
		}
	}
}

// Query rows with a live context first: cancellation must stop Go-side decoding
// too, rather than relying only on SQLite's QueryContext interrupt.
func TestTopicRegistryExpiredContextStopsRowReads(t *testing.T) {
	a := largeTopicRegistryFixture(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, tc := range []struct {
		name  string
		query string
		scan  func(context.Context, *sql.Rows) (int, error)
	}{
		{"activities", "SELECT " + activityColumns + " FROM activities", func(ctx context.Context, rows *sql.Rows) (int, error) {
			got, err := scanActivityRowsContext(ctx, rows)
			return len(got), err
		}},
		{"runs", "SELECT " + timelineRunColumns + " FROM activity_timeline_groups", func(ctx context.Context, rows *sql.Rows) (int, error) {
			got, err := scanTimelineRunRowsContext(ctx, rows)
			return len(got), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, readCtx := range []context.Context{ctx, t.Context()} {
				rows, err := a.activity.store.reader().QueryContext(t.Context(), tc.query)
				if err != nil {
					t.Fatal(err)
				}
				started := time.Now()
				count, err := tc.scan(readCtx, rows)
				elapsed := time.Since(started)
				rows.Close()
				if readCtx.Err() != nil {
					if !errors.Is(err, context.DeadlineExceeded) || count != 0 || elapsed > time.Second {
						t.Fatalf("expired scan: count=%d err=%v elapsed=%s", count, err, elapsed)
					}
				} else if err != nil || count != topicRegistryFixtureSessions+1 {
					t.Fatalf("live control: count=%d err=%v", count, err)
				}
			}
		})
	}
	for _, tc := range []struct {
		name string
		read func() error
	}{
		{"activities collection", func() error {
			_, _, _, err := a.topicRegistryActivitiesLimited(ctx, topicRegistryTestNow, nil)
			return err
		}},
		{"activity policy", func() error {
			_, err := a.filterTopicRegistryActivities(ctx, []ActivityRow{{ID: "work"}})
			return err
		}},
		{"goals collection", func() error {
			_, _, _, err := a.topicRegistryGoalsLimited(ctx, topicRegistryTestNow, nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			err := tc.read()
			if elapsed := time.Since(started); !errors.Is(err, context.DeadlineExceeded) || elapsed > time.Second {
				t.Fatalf("expired collection: err=%v elapsed=%s", err, elapsed)
			}
		})
	}
}
