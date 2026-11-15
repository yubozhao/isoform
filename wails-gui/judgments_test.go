package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func judgmentsTestDir(t *testing.T) (string, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	clock := time.Date(2026, 11, 14, 9, 0, 0, 0, time.Local)
	previousDir, previousNow := judgmentsDirHook, judgmentsNow
	judgmentsDirHook = func() (string, error) { return dir, nil }
	judgmentsNow = func() time.Time { return clock }
	judgmentsMu.Lock()
	judgmentsDay = nil
	judgmentsMu.Unlock()
	t.Cleanup(func() {
		judgmentsDirHook, judgmentsNow = previousDir, previousNow
		judgmentsMu.Lock()
		judgmentsDay = nil
		judgmentsMu.Unlock()
	})
	return dir, &clock
}

// The day's measures count each proposal once, however often it is shown or
// read, and the day's one line lands in summary.log when the next day starts.
func TestJudgmentsCountADayAndWriteItsLine(t *testing.T) {
	dir, clock := judgmentsTestDir(t)
	for i := 0; i < 5; i++ { // the same offers read five times
		noteProposalMade(judgmentOffer, "offer-1")
		noteProposalMade(judgmentOffer, "offer-2")
	}
	noteProposalMade(judgmentTopic, "p1")
	noteProposalMade(judgmentTopic, "p2")
	noteProposalAnswered(judgmentTopic, "p1", true)
	noteProposalAnswered(judgmentTopic, "p1", true) // a repeat answer counts once
	noteProposalAnswered(judgmentTopic, "p2", false)
	noteProposalMade(judgmentCard, "m1")
	noteProposalAnswered(judgmentCard, "m1", true)
	noteBriefActed()
	noteBriefActed()

	*clock = clock.Add(24 * time.Hour)
	noteProposalMade(judgmentTopic, "p3")
	raw, err := os.ReadFile(filepath.Join(dir, "summary.log"))
	if err != nil {
		t.Fatal(err)
	}
	want := "day=2026-11-14 topic=2/2/1 offer=2/0/0 card=1/1/1 total=5/3/2 brief_acted=2 (made/answered/accepted)\n"
	if string(raw) != want {
		t.Fatalf("summary.log = %q\nwant %q", raw, want)
	}
	// The day file holds counts and hashes only.
	day, _ := os.ReadFile(filepath.Join(dir, "2026-11-14.json"))
	if strings.Contains(string(day), "offer-1") || strings.Contains(string(day), "p1\"") {
		t.Fatalf("a proposal ID was stored as it is: %s", day)
	}
	// A launch closes a day only once.
	judgmentsMu.Lock()
	judgmentsDay = nil
	judgmentsMu.Unlock()
	closeJudgmentDaysAtLaunch()
	raw, _ = os.ReadFile(filepath.Join(dir, "summary.log"))
	if strings.Count(string(raw), "day=2026-11-14") != 1 {
		t.Fatalf("a day was closed twice: %q", raw)
	}
}
