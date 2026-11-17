package main

import (
	"testing"
	"time"
)

// No internet is kind 3 (computer-trouble v1.0.0 rule 1), read from the
// watch's last check, never on the read path.
func TestNoInternetIsATroubleLineWhileTheNetworkIsDown(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	t.Cleanup(func() {
		troubleNetworkDown.Store(false)
		troubleFirstSeen.at(troubleOffline, false, time.Now())
	})
	now := time.Now()
	troubleNetworkDown.Store(true)
	found := ""
	for _, line := range app.troubleLines(now) {
		if line.Kind == troubleOffline {
			found = line.Since
		}
	}
	if found == "" {
		t.Fatal("no internet line while the network is down")
	}
	troubleNetworkDown.Store(false)
	for _, line := range app.troubleLines(now.Add(time.Minute)) {
		if line.Kind == troubleOffline {
			t.Fatal("the line stayed after the network came back")
		}
	}
}

// Activity keeps the hours as gaps (rule 7): one opens when a kind starts and
// closes when it ends; a phone not checking in leaves none.
func TestTroubleLeavesGapsForActivity(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	start := time.Date(2026, 11, 16, 14, 10, 0, 0, time.UTC)
	app.recordTroubleGaps([]TroubleLine{{Kind: troubleScreen, Since: start.Format(time.RFC3339)}, {Kind: troublePhone, Since: start.Format(time.RFC3339)}}, start)
	app.recordTroubleGaps([]TroubleLine{{Kind: troubleScreen, Since: start.Format(time.RFC3339)}}, start.Add(time.Hour))
	gaps := app.troubleGapsBetween(start.Add(-time.Hour), start.Add(5*time.Hour))
	if len(gaps) != 1 || gaps[0].Kind != troubleScreen || gaps[0].To != "" {
		t.Fatalf("an open gap = %+v", gaps)
	}
	end := start.Add(4*time.Hour + 30*time.Minute)
	app.recordTroubleGaps(nil, end)
	gaps = app.troubleGapsBetween(start.Add(-time.Hour), start.Add(5*time.Hour))
	if len(gaps) != 1 || gaps[0].To != mobileTimestamp(end) {
		t.Fatalf("a closed gap = %+v", gaps)
	}
	if outside := app.troubleGapsBetween(end.Add(time.Hour), end.Add(2*time.Hour)); len(outside) != 0 {
		t.Fatalf("a gap outside the range = %+v", outside)
	}
	stretches, err := app.troubleStretches(start.Add(-time.Hour).UnixMilli(), start.Add(5*time.Hour).UnixMilli())
	if err != nil || len(stretches) != 1 || stretches[0].Cause != troubleScreen || stretches[0].StartMs != start.UnixMilli() || stretches[0].EndMs != end.UnixMilli() {
		t.Fatalf("stretches = %+v %v", stretches, err)
	}
}

// The brief ends with what it's missing, and its notification carries it
// (rule 5); the phone shows only the screen and the sign-out (rule 8).
func TestTheBriefAndThePhoneSayTheTrouble(t *testing.T) {
	since := time.Date(2026, 11, 16, 22, 10, 0, 0, time.UTC).Format(time.RFC3339)
	lines := []TroubleLine{{Kind: troubleScreen, Since: since}, {Kind: troubleOffline, Since: since}, {Kind: troublePhone, Since: since}}
	missing := briefMissingFrom(lines)
	if missing == nil || missing.Kind != troubleScreen || missing.Since != since {
		t.Fatalf("missing = %+v", missing)
	}
	if briefMissingFrom([]TroubleLine{{Kind: troublePhone, Since: since}}) != nil {
		t.Fatal("a phone not checking in is not missing from a brief")
	}
	if line := briefTroubleLine(missing); line == "" || line[:31] != "Isoform hasn't seen your screen" {
		t.Fatalf("notification line = %q", line)
	}
	phone := mobileTodayTroubleLines(lines)
	if len(phone) != 1 || phone[0].Kind != troubleScreen {
		t.Fatalf("phone trouble = %+v", phone)
	}
	if params := briefReadyParams(MobileBriefV1{Slot: "evening", Missing: missing}, false); params[briefTroubleParam] == "" {
		t.Fatalf("brief notification params = %v", params)
	}
}
