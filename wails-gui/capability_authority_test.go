package main

import (
	"context"
	"path/filepath"
	"testing"

	"wails-gui/storage"
)

func newAuthorityTestApp(t *testing.T, activityStore *ActivityStore) *App {
	t.Helper()
	setTestHome(t)
	stateStore, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stateStore.Close() })
	scheduler := NewCronScheduler(stateStore, func(string, any) {}, func(string, string) {}, nil)
	if err := scheduler.loadJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(scheduler.loadedCh)
	return &App{
		store:         stateStore,
		cronScheduler: scheduler,
		activity:      &activityMonitorState{store: activityStore},
	}
}
