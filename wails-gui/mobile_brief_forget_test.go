package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// Forget everything removes every stored brief and every copy of it, moves
// the privacy epoch first, keeps only the epoch and its withdrawal log, and
// can run again.
func TestForgetAllBriefsRemovesEveryCopy(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	brief, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSchedule)
	if err != nil {
		t.Fatal(err)
	}
	dir := svc.store.dir
	for _, name := range []string{brief.BriefID + mobileBriefBuiltExt, brief.BriefID + mobileBriefFinishedReadExt, brief.BriefID + mobileBriefAudioExtM4A, mobileBriefLatestShownName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stateDir := filepath.Dir(dir)
	if err := os.WriteFile(filepath.Join(stateDir, mobileBriefFeedbackFileName), []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	mobileBriefLive.Store(svc)
	t.Cleanup(func() { mobileBriefLive.Store(nil) })
	before := mobileBriefPrivacyEpoch.Load()

	app := &App{}
	if err := app.forgetAllBriefs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mobileBriefPrivacyEpoch.Load() <= before {
		t.Fatal("the privacy epoch did not move")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	for _, name := range left {
		if name != mobileBriefPrivacyEpochFile && name != mobileBriefWithdrawalLogFile {
			t.Fatalf("left after forgetting: %v", left)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, mobileBriefFeedbackFileName)); !os.IsNotExist(err) {
		t.Fatalf("brief answers kept: %v", err)
	}
	if _, ok := svc.store.latest(); ok {
		t.Fatal("a brief still reads as the latest")
	}
	if _, ok := svc.store.load(brief.BriefID); ok {
		t.Fatal("the forgotten brief still loads")
	}
	if err := app.forgetAllBriefs(context.Background()); err != nil {
		t.Fatalf("a second run failed: %v", err)
	}
}

// Retention removes a brief's sealed copy with the brief: the sealed copy
// holds the brief's words, and copies 19 days old were still on disk
// (2026-11-15, retention is 14 days).
func TestRetentionRemovesSealedCopies(t *testing.T) {
	dir := t.TempDir()
	store := &mobileBriefStore{dir: dir}
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.Local)
	old, recent := "2026-10-27-on_request-195131", "2026-11-15-evening"
	for _, name := range []string{old + mobileBriefFinishedReadExt, old + ".json", recent + mobileBriefFinishedReadExt} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	store.sweepLocked(now)
	store.mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, old+mobileBriefFinishedReadExt)); !os.IsNotExist(err) {
		t.Fatalf("an expired sealed copy was kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, recent+mobileBriefFinishedReadExt)); err != nil {
		t.Fatalf("a recent sealed copy was removed: %v", err)
	}
}
