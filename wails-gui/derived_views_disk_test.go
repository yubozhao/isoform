package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/storage"
)

// derivedDiskTestApp is derivedTestApp with the saved privacy epoch loaded,
// as after launch, so snapshots are saved to disk. The epoch globals are
// shared with other tests and restored afterwards.
func derivedDiskTestApp(t *testing.T) (*App, *derivedViews) {
	t.Helper()
	app, d := derivedTestApp(t)
	mobileBriefPrivacyEpochMu.Lock()
	savedDir := mobileBriefPrivacyEpochSavedDir
	mobileBriefPrivacyEpochSavedDir = t.TempDir()
	mobileBriefPrivacyEpochMu.Unlock()
	t.Cleanup(func() {
		mobileBriefPrivacyEpochMu.Lock()
		mobileBriefPrivacyEpochSavedDir = savedDir
		mobileBriefPrivacyEpochMu.Unlock()
	})
	return app, d
}

func derivedDiskFiles(t *testing.T) []string {
	t.Helper()
	dir, err := derivedDiskDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	return files
}

// restartedDerivedApp is a new process on the same data: a fresh App on the
// same state store, before its scheduler has started.
func restartedDerivedApp(t *testing.T, app *App) *App {
	t.Helper()
	return &App{store: app.store, activeTags: map[string]bool{}, eventBroadcaster: NewEventBroadcaster()}
}

func TestDerivedSnapshotPaintsTheFirstVisitAfterARestart(t *testing.T) {
	app, _ := derivedDiskTestApp(t)
	var builds atomic.Int64
	build := func() (derivedTestValue, error) {
		builds.Add(1)
		return derivedTestValue{Items: []string{"saved"}}, nil
	}
	readDerivedTestValue(t, app, derivedAboutYou, build)
	files := derivedDiskFiles(t)
	if len(files) != 1 {
		t.Fatalf("saved snapshots = %v, want one", files)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot file mode = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(files[0]))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot folder mode = %v err=%v, want 0700", dirInfo.Mode().Perm(), err)
	}

	restarted := restartedDerivedApp(t, app)
	got := readDerivedTestValue(t, restarted, derivedAboutYou, func() (derivedTestValue, error) {
		builds.Add(1)
		return derivedTestValue{Items: []string{"live"}}, nil
	})
	if len(got.Items) != 1 || got.Items[0] != "saved" || builds.Load() != 1 {
		t.Fatalf("after restart got %v with %d builds, want the saved snapshot and no live build", got.Items, builds.Load())
	}
}

func TestDerivedSnapshotForgetDeletesTheSavedFile(t *testing.T) {
	app, _ := derivedDiskTestApp(t)
	readDerivedTestValue(t, app, derivedAboutYou, func() (derivedTestValue, error) {
		return derivedTestValue{Items: []string{"belief"}}, nil
	})
	if len(derivedDiskFiles(t)) != 1 {
		t.Fatal("snapshot was not saved")
	}
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := recordMemoryTombstone(dir, "forgotten", []string{"# Forgotten\n\nA belief."}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if files := derivedDiskFiles(t); len(files) != 0 {
		t.Fatalf("a forget left saved snapshots: %v", files)
	}
}

func TestDerivedSnapshotPrivacyActionsDeleteTheSavedFile(t *testing.T) {
	for name, act := range map[string]func(*App){
		"withdrawal":      func(*App) { briefPrivacyChanged() },
		"page clear":      func(a *App) { a.clearPageSnapshots() },
		"language change": func(a *App) { a.noteUILanguageChanged() },
	} {
		t.Run(name, func(t *testing.T) {
			app, _ := derivedDiskTestApp(t)
			readDerivedTestValue(t, app, derivedKnowHow, func() (derivedTestValue, error) {
				return derivedTestValue{Items: []string{"how-to"}}, nil
			})
			if len(derivedDiskFiles(t)) != 1 {
				t.Fatal("snapshot was not saved")
			}
			act(app)
			if files := derivedDiskFiles(t); len(files) != 0 {
				t.Fatalf("%s left saved snapshots: %v", name, files)
			}
		})
	}
}

// A saved copy whose stamp no longer matches is never served and is deleted,
// even when the deletion hook was missed (a crash between the change and the
// delete).
func TestDerivedSnapshotStaleStampIsNeverServed(t *testing.T) {
	app, _ := derivedDiskTestApp(t)
	readDerivedTestValue(t, app, derivedPeopleOverview, func() (derivedTestValue, error) {
		return derivedTestValue{Items: []string{"before"}}, nil
	})
	if len(derivedDiskFiles(t)) != 1 {
		t.Fatal("snapshot was not saved")
	}
	// The language is part of the durable stamp; change it without the hook.
	if err := app.store.SetUIState(context.Background(), uiLanguageStateKey, "zh-CN"); err != nil {
		t.Fatal(err)
	}
	restarted := restartedDerivedApp(t, app)
	got := readDerivedTestValue(t, restarted, derivedPeopleOverview, func() (derivedTestValue, error) {
		return derivedTestValue{Items: []string{"live"}}, nil
	})
	if len(got.Items) != 1 || got.Items[0] != "live" {
		t.Fatalf("a stale saved snapshot was served: %v", got.Items)
	}
	if files := derivedDiskFiles(t); len(files) != 0 {
		t.Fatalf("a stale saved snapshot was kept: %v", files)
	}
}

func TestDerivedSnapshotNothingSavedOrServedInClosedEyes(t *testing.T) {
	app, _ := derivedDiskTestApp(t)
	readDerivedTestValue(t, app, derivedAboutYou, func() (derivedTestValue, error) {
		return derivedTestValue{Items: []string{"saved"}}, nil
	})
	app.closedEyes.enabled.Store(true)
	t.Cleanup(func() { app.closedEyes.enabled.Store(false) })
	if _, _, ok := app.derivedDiskLoad(derivedAboutYou, "test"); ok {
		t.Fatal("a saved snapshot was served while Closed Eyes is on")
	}
	if _, ok := app.derivedDiskStamp(); ok {
		t.Fatal("snapshots could be saved while Closed Eyes is on")
	}
}

func TestDerivedSnapshotSwitchKeepsSnapshotsInMemory(t *testing.T) {
	t.Setenv(derivedSnapshotsSwitch, "memory")
	app, _ := derivedDiskTestApp(t)
	readDerivedTestValue(t, app, derivedAboutYou, func() (derivedTestValue, error) {
		return derivedTestValue{Items: []string{"memory only"}}, nil
	})
	if files := derivedDiskFiles(t); len(files) != 0 {
		t.Fatalf("the switch is off but snapshots were saved: %v", files)
	}
}

// With the switch on the scheduler admits snapshots before the first paint
// (a window that launched hidden); with it off it waits for the paint.
func TestDerivedSchedulerAdmitsBeforeFirstPaint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetUIState(context.Background(), onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	app := &App{store: store, activeTags: map[string]bool{}, eventBroadcaster: NewEventBroadcaster()}
	d := app.derivedViews()
	if !d.admit(context.Background()) {
		t.Fatal("scheduler waited for the first paint with the switch on")
	}
	t.Setenv(derivedSnapshotsSwitch, "memory")
	if d.admit(context.Background()) {
		t.Fatal("scheduler admitted before the first paint with the switch off")
	}
}
