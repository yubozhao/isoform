//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"testing"
	"time"

	"wails-gui/internal/calendar"
)

func TestHoldSlotsHonorLengthDueAndHalfHours(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 5, 0, 0, time.UTC)
	windows := []FindTimeRange{{Start: "2026-11-02T09:00:00Z", End: "2026-11-02T16:00:00Z"}}
	due := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	slots, err := holdSlots(windows, now, due, 70*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 3 || slots[0].Start != "2026-11-02T09:30:00Z" || slots[0].End != "2026-11-02T11:00:00Z" || slots[2].End != "2026-11-02T14:00:00Z" {
		t.Fatalf("slots: %#v", slots)
	}
	slots, err = holdSlots(windows, now, time.Time{}, 4*time.Hour)
	if err != nil || len(slots) == 0 || slots[0].End != "2026-11-02T11:30:00Z" {
		t.Fatalf("capped slots: %#v, %v", slots, err)
	}
	slots, err = holdSlots(windows, now, time.Time{}, 0)
	if err != nil || len(slots) != 4 || slots[0].End != "2026-11-02T10:30:00Z" {
		t.Fatalf("default slots: %#v, %v", slots, err)
	}
	if _, err := holdSlots([]FindTimeRange{{Start: "bad"}}, now, due, time.Hour); err == nil {
		t.Fatal("accepted invalid window")
	}
}

func TestHoldMeetingGapsLeaveTenMinutes(t *testing.T) {
	windows := []FindTimeRange{{Start: "2026-11-02T09:00:00Z", End: "2026-11-02T10:30:00Z"}}
	meeting := calendar.Event{Start: time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC), End: time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC), Attendees: []calendar.Attendee{{Email: "someone@example.test"}}}
	got, err := holdMeetingGaps(windows, []calendar.Event{meeting})
	if err != nil || len(got) != 1 || got[0].End != "2026-11-02T10:00:00Z" {
		t.Fatalf("windows=%#v err=%v", got, err)
	}
	meeting.Availability = "free"
	got, err = holdMeetingGaps(windows, []calendar.Event{meeting})
	if err != nil || got[0].End != windows[0].End {
		t.Fatalf("free meeting windows=%#v err=%v", got, err)
	}
}
