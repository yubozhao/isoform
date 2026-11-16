package main

import (
	"sync"
	"sync/atomic"
	"testing"
)

// Today's first reads arrive before the brief service has opened its store.
// The saved snapshot must still paint them: the stamp loads the saved privacy
// epoch itself instead of building every first read live (8 to 13 s each on
// 2026-11-15).
func TestDerivedSnapshotPaintsBeforeTheBriefServiceStarts(t *testing.T) {
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	app, _ := derivedDiskTestApp(t)
	var builds atomic.Int64
	readDerivedTestValue(t, app, derivedAboutYou, func() (derivedTestValue, error) {
		builds.Add(1)
		return derivedTestValue{Items: []string{"saved"}}, nil
	})
	if len(derivedDiskFiles(t)) != 1 {
		t.Fatal("snapshot was not saved")
	}
	// A new process: no brief store has loaded the epoch yet.
	mobileBriefPrivacyEpoch.SwapSavedDir("")
	savedCopyEpochLoad = sync.Once{}
	t.Cleanup(func() { savedCopyEpochLoad = sync.Once{} })

	restarted := restartedDerivedApp(t, app)
	got := readDerivedTestValue(t, restarted, derivedAboutYou, func() (derivedTestValue, error) {
		builds.Add(1)
		return derivedTestValue{Items: []string{"live"}}, nil
	})
	if len(got.Items) != 1 || got.Items[0] != "saved" || builds.Load() != 1 {
		t.Fatalf("before the brief service: got %v with %d builds, want the saved snapshot and no live build", got.Items, builds.Load())
	}
}
