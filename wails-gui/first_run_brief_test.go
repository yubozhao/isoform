package main

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

type firstBriefTestSource struct {
	mobileTestBriefSource
	requests atomic.Int32
}

func (s *firstBriefTestSource) RequestMobileBrief(reason string) (MobileBriefRequestResult, error) {
	s.requests.Add(1)
	return MobileBriefRequestResult{Status: "started"}, nil
}

// Computer setup v1.0.0 rule 10 and its proofs: set up at 2 PM, the first
// brief is the 6:00 PM one; at 5:30 PM it is made at 6:30 PM; late in the
// evening it is an hour later, never tomorrow.
func TestFirstBriefTimeFollowsSetupRule10(t *testing.T) {
	times := map[string]string{"morning": "07:00", "evening": "18:00"}
	day := func(h, m int) time.Time { return time.Date(2026, 11, 16, h, m, 0, 0, time.Local) }
	for _, c := range []struct {
		at   time.Time
		want time.Time
		slot string
	}{
		{day(14, 0), day(18, 0), "evening"},
		{day(17, 30), day(18, 30), ""},
		{day(21, 0), day(22, 0), ""},
		{day(5, 0), day(7, 0), "morning"},
		{day(6, 30), day(7, 30), ""},
		{day(23, 30), day(23, 30).Add(time.Hour), ""},
	} {
		got := firstBriefAt(c.at, times)
		if got.At != c.want.Format(time.RFC3339) || got.Slot != c.slot {
			t.Errorf("set up at %s: first brief %+v, want %s %q", c.at.Format("15:04"), got, c.want.Format(time.RFC3339), c.slot)
		}
	}
	if got := firstBriefAt(day(14, 0), nil); got.At != day(15, 0).Format(time.RFC3339) || got.Slot != "" {
		t.Errorf("with the schedule off: %+v, want an hour after setup", got)
	}
}

// Finishing setup saves the time and asks for nothing at once; the pass makes
// the first brief when its time comes, once, and the band's time clears then.
func TestFirstBriefComesAtItsSavedTimeOnce(t *testing.T) {
	app := mainChatTestApp(t)
	source := &firstBriefTestSource{}
	app.mobileBridgeMu.Lock()
	app.mobileBriefSvc = source
	app.mobileBridgeMu.Unlock()
	ctx := t.Context()
	if err := app.store.SetUIState(ctx, firstWeekPendingKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveUIState(onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	first, err := app.GetFirstBriefTime()
	if err != nil || first.At == "" {
		t.Fatalf("no first brief time after setup: %+v %v", first, err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := source.requests.Load(); got != 0 {
		t.Fatalf("a first brief was asked for at once: %d", got)
	}
	at, _ := time.Parse(time.RFC3339, first.At)
	app.firstBriefDue(ctx, at.Add(-time.Minute))
	if got := source.requests.Load(); got != 0 {
		t.Fatalf("asked for before its time: %d", got)
	}
	late := at.Add(firstBriefSlotGrace + time.Minute)
	app.firstBriefDue(ctx, late)
	app.firstBriefDue(ctx, late.Add(time.Minute))
	deadline := time.Now().Add(5 * time.Second)
	for source.requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if got := source.requests.Load(); got != 1 {
		t.Fatalf("first brief requests = %d, want 1", got)
	}
	if first, _ := app.GetFirstBriefTime(); first.At != "" {
		t.Fatalf("the band still shows a time after the first brief: %+v", first)
	}
	// Saving setup again (a restart, another step) keeps the first answer.
	raw, _ := app.store.GetUIState(ctx, firstBriefTimeKey)
	var saved FirstBriefTime
	if json.Unmarshal([]byte(raw), &saved) != nil || saved.At != at.Format(time.RFC3339) {
		t.Fatalf("saved time changed: %s", raw)
	}
}

func TestNoFirstBriefOnUpgrade(t *testing.T) {
	app := mainChatTestApp(t)
	source := &firstBriefTestSource{}
	app.mobileBridgeMu.Lock()
	app.mobileBriefSvc = source
	app.mobileBridgeMu.Unlock()
	if err := app.SaveUIState(onboardingStateKey, `{"completed":true}`); err != nil {
		t.Fatal(err)
	}
	app.firstBriefDue(t.Context(), time.Now().Add(48*time.Hour))
	time.Sleep(50 * time.Millisecond)
	if got := source.requests.Load(); got != 0 {
		t.Fatalf("an upgrade asked for a first brief: %d", got)
	}
	if first, _ := app.GetFirstBriefTime(); first.At != "" {
		t.Fatalf("an upgrade shows a first brief time: %+v", first)
	}
}
