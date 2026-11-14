package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
)

func TestLaunchFirstScreenDoesNotWaitForDeferredTaskOrRefresh(t *testing.T) {
	app := launchFixtureRows(t, 20, 50)
	app.launch = newLaunchStartup()
	app.launch.startWorker(t.Context())
	started, release := make(chan struct{}), make(chan struct{})
	app.deferLaunchTask("slow_fixture", func() { close(started); <-release })
	<-started
	t.Cleanup(app.launch.stop)
	t.Cleanup(func() { close(release) })
	svc := app.mobileBriefSvc.(*mobileBriefService)
	// A live refresh holds this mutex while gathering sources. The persisted
	// read must neither acquire it nor wait for the initializer worker.
	svc.todayMu.Lock()
	defer svc.todayMu.Unlock()
	done := make(chan error, 1)
	go func() {
		view, err := app.GetLatestBrief()
		if err == nil && (view.Brief == nil || view.Brief.BriefID == "") {
			err = context.Canceled
		}
		if err == nil {
			_, err = app.GetTodayDayShape(svc.now().In(time.Local).Format("2006-01-02"))
		}
		if err == nil {
			_, err = app.GetMainChatOverview()
		}
		if err == nil {
			_, err = app.LoadStreamItemsPage("session-0", -1, 50)
		}
		if err == nil {
			_, err = app.GetCaptureCounts(0)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("persisted first-screen reads waited for deferred work")
	}
}

func TestLaunchStoredBriefPrecedesRefreshAndKeepsCanonicalDocument(t *testing.T) {
	rig, svc, stored := cardRig(t)
	app := rig.app
	app.launch = newLaunchStartup()
	previous := todayBriefPermissionStatus
	todayBriefPermissionStatus = func(*App) PlatformPermissions { return PlatformPermissions{} }
	t.Cleanup(func() { todayBriefPermissionStatus = previous })
	path := filepath.Join(svc.store.dir, stored.BriefID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.handoffStore.Save(handoff.Entry{ID: "new-review", Title: "New launch review", Source: "manual", Status: handoff.StatusNeedsReview, CreatedAt: svc.now().UnixMilli(), UpdatedAt: svc.now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	view, err := app.GetLatestBrief()
	if err != nil || view.Brief == nil {
		t.Fatalf("stored view: %+v, %v", view, err)
	}
	wire, _ := json.Marshal(view)
	if strings.Contains(string(wire), "New launch review") {
		t.Fatal("launch synchronously refreshed sources")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("launch rewrote the canonical document")
	}
	// The normal source path remains the refresh owner and produces the same
	// current content after the first screen.
	app.launch.storedReads.Store(false)
	view, err = latestBriefAfterRefresh(t, app)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ = json.Marshal(view)
	if !strings.Contains(string(wire), "New launch review") {
		t.Fatal("background refresh lost current source content")
	}
}

func TestLaunchStoredBriefRechecksPrivacyAfterRestart(t *testing.T) {
	for _, reason := range []string{"closed_eyes", "deleted_source"} {
		t.Run(reason, func(t *testing.T) {
			rig, svc, brief := cardRig(t)
			app := rig.app
			app.launch = newLaunchStartup()
			// Exercise disk reload, not an in-process last-good object.
			svc.store = newMobileBriefStore(filepath.Dir(svc.store.dir))
			initial, ok := svc.storedLatestForLaunch()
			if !ok || len(initial.Sections.OpenLoops) == 0 {
				t.Fatal("missing readable persisted fixture")
			}
			switch reason {
			case "closed_eyes":
				app.closedEyes.enabled.Store(true)
			case "deleted_source":
				if err := app.handoffStore.Delete("memo"); err != nil {
					t.Fatal(err)
				}
			}
			got, ok := svc.storedLatestForLaunch()
			if !ok {
				t.Fatal("privacy gate destroyed document identity")
			}
			if reflect.DeepEqual(got, initial) {
				t.Fatalf("%s retained the pre-transition view", reason)
			}
			if reason == "closed_eyes" && (len(got.Narrative) != 0 || got.Audio != nil || len(got.Cards) != 0) {
				t.Fatal("closed eyes retained written or spoken content")
			}
			if canonical, ok := svc.store.load(brief.BriefID); !ok || len(canonical.Sections.OpenLoops) == 0 {
				t.Fatal("display clearing deleted canonical evidence")
			}
		})
	}
}

func TestLaunchInitializersWaitForPaintAndRunInOrder(t *testing.T) {
	l := newLaunchStartup()
	app := &App{launch: l}
	var order []int
	first := app.deferLaunchTask("fixture_first", func() { order = append(order, 1) })
	second := app.deferLaunchTask("fixture_second", func() { order = append(order, 2) })
	ready := make(chan bool, 1)
	go func() {
		ok := l.wait(t.Context(), time.Hour)
		if ok {
			l.startWorker(t.Context())
		}
		ready <- ok
	}()
	select {
	case <-first:
		t.Fatal("initializer ran before paint")
	default:
	}
	l.firstScreenPainted()
	if !<-ready {
		t.Fatal("paint did not release startup")
	}
	<-second
	l.stop()
	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("initializer order = %v", order)
	}
}

func TestLaunchCancellationFencesPendingInitializers(t *testing.T) {
	l := newLaunchStartup()
	app := &App{launch: l}
	ran := false
	done := app.deferLaunchTask("cancelled_fixture", func() { ran = true })
	l.stop()
	<-done
	l.startWorker(t.Context())
	if ran {
		t.Fatal("stopped startup ran a queued task")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if l.wait(ctx, time.Hour) {
		t.Fatal("cancelled startup continued")
	}
}

func TestLaunchStoredViewsDoNotSurviveSensitiveSourceWithdrawal(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	old := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(old) })
	pii.SetUserSensitiveApps(nil)
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-10 * time.Minute)
	if _, err := app.activity.store.InsertBrowserVisits([]BrowserVisitRow{
		{Browser: "Chrome", URL: "https://example.org/private-launch", Title: "Private launch research", VisitedAt: at.UnixMilli()},
	}); err != nil {
		t.Fatal(err)
	}
	input := mobileBriefInput{AsOf: now, WindowStart: now.Add(-time.Hour), Slot: mobileBriefSlotMorning, Location: time.UTC}
	collectMobileBriefWatching(app, &input)
	brief := buildMobileBriefV1(input)
	if len(brief.Sections.Watching) != 1 {
		t.Fatal("missing source fixture")
	}
	brief.NarrativeStatus = "ok"
	brief.Narrative = []MobileBriefPara{{Text: "Private launch research", Refs: []string{brief.Evidence[0].Ref}}}
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	svc := newMobileBriefService(app)
	svc.now, svc.location = func() time.Time { return now }, time.UTC
	if err := svc.store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	view, ok := svc.storedLatestForLaunch()
	if !ok || len(view.Sections.Watching) != 1 {
		t.Fatal("missing persisted last-good view")
	}
	pii.SetUserSensitiveApps(map[string]string{"com.google.Chrome": "Chrome"})
	app.emitBrowserEvent("sensitive-apps:updated")
	// A new store instance must not restore the last-good visible projection.
	svc.store = newMobileBriefStore(filepath.Dir(svc.store.dir))
	view, ok = svc.storedLatestForLaunch()
	wire, _ := json.Marshal(view)
	if !ok || len(view.Sections.Watching) != 0 || len(view.Narrative) != 0 || view.Audio != nil || strings.Contains(string(wire), "private-launch") || strings.Contains(string(wire), "Private launch research") {
		t.Fatalf("withdrawn persisted view reached the first screen: %s", wire)
	}
}

func TestLaunchWorkerCancellationDrainsQueuedTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	l := newLaunchStartup()
	app := &App{launch: l}
	started, release := make(chan struct{}), make(chan struct{})
	l.startWorker(ctx)
	first := app.deferLaunchTask("running_fixture", func() { close(started); <-release })
	<-started
	ran := false
	second := app.deferLaunchTask("pending_fixture", func() { ran = true })
	cancel()
	close(release)
	<-first
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("cancelled worker stranded a waiter")
	}
	l.stop()
	if ran {
		t.Fatal("cancelled initializer ran after the active task returned")
	}
	select {
	case <-app.deferLaunchTask("late_fixture", func() { t.Error("stopped worker admitted a late initializer") }):
	case <-time.After(time.Second):
		t.Fatal("late initializer stranded a waiter")
	}
}

func TestLaunchCancellationAllowsForegroundFlushBeforeJoin(t *testing.T) {
	l := newLaunchStartup()
	app := &App{launch: l}
	started, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
		l.stop()
	}()
	l.startWorker(t.Context())
	app.deferLaunchTask("native_warmup_fixture", func() { close(started); <-release })
	<-started
	cancelled := make(chan struct{})
	var join <-chan struct{}
	go func() { join = l.cancelPending(); close(cancelled) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked the foreground flush phase")
	}
	if !l.isStopped() {
		t.Fatal("cancellation did not fence new initializers")
	}
	select {
	case <-join:
		t.Fatal("fixture initializer unexpectedly finished before flush")
	default:
	}
	close(release)
	released = true
	<-join
}

func TestLaunchMaintenanceWaitsForInitializerWorker(t *testing.T) {
	app := launchFixtureRows(t, 1, 1)
	app.launch = newLaunchStartup()
	path := filepath.Join(app.activity.store.SnapshotsDir(), "2000-01-01", "orphan.jpg")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("orphan fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(filepath.Dir(path), "current-run.jpg")
	if err := os.WriteFile(current, []byte("in-flight fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
		app.launch.stop()
	}()
	app.launch.startWorker(t.Context())
	app.deferLaunchTask("blocked_warmup_fixture", func() { close(started); <-release })
	<-started
	done := app.startActivityMaintenance()
	if done == nil {
		t.Fatal("maintenance did not register its completion")
	}
	select {
	case <-done:
		t.Fatal("maintenance bypassed the blocked initializer")
	default:
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("recovery touched stored artifacts before its turn: %v", err)
	}
	close(release)
	released = true
	awaitStorageStartup(t, done)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("queued recovery did not remove the orphan: %v", err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("delayed recovery touched a current-run write: %v", err)
	}
}

// A sealed brief keeps showing when a store is unavailable at read time: a read
// reads no store, so a closed policy store cannot hide it. Privacy changes reach
// the brief only through the privacy hook and the print (intended behavior change
// in brief-rebuild PR 1; the old read re-gated and hid the brief).
func TestLaunchStoredBriefKeepsShowingWhenPolicyStoreIsClosed(t *testing.T) {
	rig, svc, brief := cardRig(t)
	app := rig.app
	app.launch = newLaunchStartup()
	svc.store = newMobileBriefStore(filepath.Dir(svc.store.dir))
	initial, ok := svc.storedLatestForLaunch()
	if !ok || len(initial.Sections.OpenLoops) == 0 {
		t.Fatal("missing readable persisted fixture")
	}
	if err := app.store.Close(); err != nil {
		t.Fatal(err)
	}
	got, ok := svc.storedLatestForLaunch()
	// The brief still shows. Web-source items are left out while the policy store is
	// closed (their hold cannot be confirmed); everything else stays.
	if !ok || got.BriefID != brief.BriefID || len(got.Sections.OpenLoops) != len(initial.Sections.OpenLoops) {
		t.Fatalf("a closed policy store hid the sealed brief: ok=%v id=%s", ok, got.BriefID)
	}
}

// Long launch work (the know-how backfill makes model calls and sleeps a
// minute between retries) must not hold the tasks queued behind it: it held
// the credential probe about three minutes on every launch.
func TestBackgroundLaunchTaskDoesNotHoldTheQueue(t *testing.T) {
	l := newLaunchStartup()
	app := &App{launch: l}
	l.startWorker(t.Context())
	defer l.stop()
	release := make(chan struct{})
	started := make(chan struct{})
	defer close(release)
	app.deferBackgroundLaunchTask("fixture_long", func() {
		close(started)
		<-release
	})
	next := app.deferLaunchTask("fixture_next", func() {})
	select {
	case <-next:
	case <-time.After(2 * time.Second):
		t.Fatal("a background launch task held the queue")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background launch task never started")
	}
}
