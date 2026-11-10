package main

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"wails-gui/internal/pii"
)

// newSnapshotTestApp is the finds fixture with no launch or event delay, so a
// refresh the test schedules runs at once.
func newSnapshotTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	app := newTopicFindsTestApp(t)
	app.ctx = t.Context()
	svc := app.topicsSvc()
	svc.findsSnap.launchDelay = 0
	svc.findsSnap.eventDelay = 0
	// This launch has already refreshed once, so a read does not start the launch check.
	svc.findsSnap.lastRefresh = time.Now()
	return app
}

func TestTopicFindsRefreshRequiresAndStopsWithAppLifecycle(t *testing.T) {
	app := newTopicFindsTestApp(t)
	svc := app.topicsSvc()
	svc.scheduleFindsRefresh(app, time.Now().Add(time.Hour))
	svc.findsSnap.mu.Lock()
	running := svc.findsSnap.running
	svc.findsSnap.mu.Unlock()
	if running {
		t.Fatal("refresh started without an app lifecycle")
	}
	ctx, cancel := context.WithCancel(t.Context())
	app.ctx = ctx
	svc.scheduleFindsRefresh(app, time.Now().Add(time.Hour))
	svc.findsSnap.mu.Lock()
	done := svc.findsSnap.done
	svc.findsSnap.mu.Unlock()
	if done == nil {
		t.Fatal("refresh was not queued for the active lifecycle")
	}
	cancel()
	<-done
	svc.findsSnap.mu.Lock()
	running = svc.findsSnap.running
	due := svc.findsSnap.dueAt
	svc.findsSnap.mu.Unlock()
	if running || !due.IsZero() || svc.findsSnap.refreshes.Load() != 0 {
		t.Fatal("cancelled lifecycle retained or ran a queued refresh")
	}
}

// waitFindsRefreshIdle waits for the background refresh to finish, so a test
// does not race the writes of a refresh it started.
func waitFindsRefreshIdle(t *testing.T, svc *topicsService) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		svc.findsSnap.mu.Lock()
		idle := !svc.findsSnap.running
		svc.findsSnap.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the finds refresh did not finish")
}

func TestGetTopicFindsServesTheSavedViewWhileTheStampHolds(t *testing.T) {
	app := newSnapshotTestApp(t)
	svc := app.topicsSvc()
	stamp := app.savedCopyStamp(context.Background())
	if stamp == "" {
		t.Fatal("the privacy stamp should be readable in a test app")
	}
	saved := TopicFindsView{Enabled: true, Status: "ready", Items: []TopicFind{{ID: "saved-find", Title: "Saved find"}}}
	if err := svc.writeFindsSnapshot(stamp, "test-deep", saved); err != nil {
		t.Fatal(err)
	}
	view := getTopicFinds(t, app)
	if len(view.Items) != 1 || view.Items[0].ID != "saved-find" {
		t.Fatalf("the saved view was not served: %+v", view)
	}
	waitFindsRefreshIdle(t, svc)
}

func TestSavedFindsViewIsDeletedNotServedWhenTheStampChanges(t *testing.T) {
	app := newSnapshotTestApp(t)
	svc := app.topicsSvc()
	withdrawn := TopicFindsView{Enabled: true, Status: "ready", Items: []TopicFind{{ID: "withdrawn-find", Title: "Withdrawn"}}}
	if err := svc.writeFindsSnapshot("stamp-before-a-forget", "test-deep", withdrawn); err != nil {
		t.Fatal(err)
	}
	view := getTopicFinds(t, app)
	waitFindsRefreshIdle(t, svc)
	for _, item := range view.Items {
		if item.ID == "withdrawn-find" {
			t.Fatal("a view saved under an old stamp was served")
		}
	}
	if view.Status != topicFindsLoadingStatus {
		t.Fatalf("the placeholder should show while the refresh runs, got status %q", view.Status)
	}
	if _, err := os.Stat(svc.findsSnapshotPath()); err == nil {
		// The refresh may have written a new snapshot under the current stamp;
		// the withdrawn one must not survive.
		raw, _ := os.ReadFile(svc.findsSnapshotPath())
		if bytes.Contains(raw, []byte("withdrawn-find")) {
			t.Fatal("the withdrawn view is still on disk")
		}
	}
}

func TestSavedFindsViewIsNotWrittenWhenTheStampMovesDuringCompute(t *testing.T) {
	app := newSnapshotTestApp(t)
	svc := app.topicsSvc()
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(nil)
	svc.findsSnap.computeHook = func() {
		pii.SetUserSensitiveApps(map[string]string{"com.example.private": "work"})
	}
	t.Cleanup(func() { svc.findsSnap.computeHook = nil })
	app.refreshTopicFinds()
	if _, err := os.Stat(svc.findsSnapshotPath()); err == nil {
		t.Fatal("a view computed across a privacy change was saved")
	}
}

func TestSavedFindsViewIsOwnerOnly(t *testing.T) {
	app := newSnapshotTestApp(t)
	svc := app.topicsSvc()
	stamp := app.savedCopyStamp(context.Background())
	if err := svc.writeFindsSnapshot(stamp, "test-deep", TopicFindsView{Enabled: true, Status: "none"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(svc.findsSnapshotPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("the saved view has permissions %o, want 600", perm)
	}
}

func getTopicFinds(t *testing.T, app *App) TopicFindsView {
	t.Helper()
	view, err := app.GetTopicFinds()
	if err != nil {
		t.Fatal(err)
	}
	return view
}

// Reads within the window serve the saved view and do not recompute: no
// refresh is scheduled, however often Today asks.
func TestRepeatedFindsReadsWithinTheWindowDoNotRecompute(t *testing.T) {
	app := newSnapshotTestApp(t)
	svc := app.topicsSvc()
	stamp := app.savedCopyStamp(context.Background())
	if err := svc.writeFindsSnapshot(stamp, "test-deep", TopicFindsView{Enabled: true, Status: "ready", Items: []TopicFind{{ID: "fresh", Title: "Fresh"}}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if view := getTopicFinds(t, app); len(view.Items) != 1 || view.Items[0].ID != "fresh" {
			t.Fatalf("read %d did not serve the saved view: %+v", i, view)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := svc.findsSnap.refreshes.Load(); n != 0 {
		t.Fatalf("reads within the window recomputed %d times", n)
	}
}

// A view older than the refresh age refreshes on the next read.
func TestSavedFindsViewOlderThanTheWindowRefreshes(t *testing.T) {
	app := newSnapshotTestApp(t)
	svc := app.topicsSvc()
	stamp := app.savedCopyStamp(context.Background())
	if err := svc.writeFindsSnapshotAt(stamp, "test-deep", TopicFindsView{Enabled: true, Status: "ready"}, time.Now().Add(-2*topicFindsRefreshAge)); err != nil {
		t.Fatal(err)
	}
	getTopicFinds(t, app)
	waitFindsRefreshIdle(t, svc)
	if n := svc.findsSnap.refreshes.Load(); n != 1 {
		t.Fatalf("an old view refreshed %d times, want 1", n)
	}
}

// A source or topic change refreshes once; an unrelated event does not.
func TestSourceChangeEventRefreshesOnce(t *testing.T) {
	app := newSnapshotTestApp(t)
	svc := app.topicsSvc()
	app.noteTopicFindsSourceChanged("insight:saved")
	time.Sleep(20 * time.Millisecond)
	if n := svc.findsSnap.refreshes.Load(); n != 0 {
		t.Fatalf("an unrelated event refreshed %d times", n)
	}
	app.noteTopicFindsSourceChanged("topics:updated")
	app.noteTopicFindsSourceChanged("memory:updated")
	waitFindsRefreshIdle(t, svc)
	if n := svc.findsSnap.refreshes.Load(); n != 1 {
		t.Fatalf("a burst of source changes refreshed %d times, want 1", n)
	}
}

// A refresh that fails keeps the saved row as it is: the old view is still
// served, the file is unchanged, and no error, empty row or skeleton replaces it.
func TestAFailedRefreshKeepsTheSavedRow(t *testing.T) {
	app := newSnapshotTestApp(t)
	svc := app.topicsSvc()
	stamp := app.savedCopyStamp(context.Background())
	saved := TopicFindsView{Enabled: true, Status: "ready", Items: []TopicFind{{ID: "kept", Title: "Kept find"}}}
	if err := svc.writeFindsSnapshot(stamp, "test-deep", saved); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(svc.findsSnapshotPath())
	if err != nil {
		t.Fatal(err)
	}
	// Make the write fail: a directory where the temporary file goes.
	if err := os.Mkdir(svc.findsSnapshotPath()+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	app.refreshTopicFinds()
	after, err := os.ReadFile(svc.findsSnapshotPath())
	if err != nil {
		t.Fatal("the saved view was removed by a failed refresh: ", err)
	}
	if string(after) != string(before) {
		t.Fatal("a failed refresh changed the saved view")
	}
	view := getTopicFinds(t, app)
	if view.Status != "ready" || len(view.Items) != 1 || view.Items[0].ID != "kept" {
		t.Fatalf("a failed refresh replaced the saved row: %+v", view)
	}
}

// A saved finds view survives a restart on the same durable stamp.
func TestASavedFindsViewSurvivesARestart(t *testing.T) {
	app := newSnapshotTestApp(t)
	svc := app.topicsSvc()
	adoptTestBriefPrivacyEpoch(app)
	briefPrivacyChanged()
	stamp := app.savedCopyStamp(context.Background())
	if stamp == "" {
		t.Fatal("the durable stamp should be readable")
	}
	if err := svc.writeFindsSnapshot(stamp, "test-deep", TopicFindsView{Enabled: true, Status: "ready", Items: []TopicFind{{ID: "kept", Title: "Kept"}}}); err != nil {
		t.Fatal(err)
	}
	mobileBriefPrivacyEpoch.Store(0)
	mobileBriefReadEpoch.Store(0)
	mobileBriefPrivacyEpochMu.Lock()
	mobileBriefPrivacyEpochSavedDir = ""
	mobileBriefPrivacyEpochMu.Unlock()
	loadSavedCopyEpoch()
	view := getTopicFinds(t, app)
	if len(view.Items) != 1 || view.Items[0].ID != "kept" {
		t.Fatalf("the saved view did not survive a restart: %+v", view)
	}
	// The launch check after a restart may run; the view itself came from the copy.
	waitFindsRefreshIdle(t, svc)
}
