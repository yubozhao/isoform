//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"testing"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/engagement"
)

func TestHoldWorkHoursAndWeekendRules(t *testing.T) {
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC) // Friday
	activity := []holdActivity{{Start: now.AddDate(0, 0, -2).Add(-11 * time.Hour), End: now.AddDate(0, 0, -2).Add(-time.Hour)}, {Start: now.AddDate(0, 0, -6).Add(-8 * time.Hour), End: now.AddDate(0, 0, -6).Add(-5 * time.Hour)}}
	profile := inferHoldWorkHours(activity, now)
	if profile.Start != 7*time.Hour || profile.End != 17*time.Hour || !profile.Worked[time.Saturday] {
		t.Fatalf("profile=%#v", profile)
	}
	quiet := engagement.QuietWindow{Start: 22 * time.Hour, End: 6 * time.Hour}
	windows, err := holdTimeWindows(nil, now, time.Time{}, time.Hour, quiet, profile)
	if err != nil || len(windows) == 0 || windows[0].Start != "2026-10-31T07:00:00Z" {
		t.Fatalf("windows=%#v err=%v", windows, err)
	}
	defaults := inferHoldWorkHours(nil, now)
	windows, err = holdTimeWindows(nil, now, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), time.Hour, quiet, defaults)
	if err != nil || len(windows) == 0 || windows[0].Start != "2026-10-31T09:00:00Z" {
		t.Fatalf("due weekend windows=%#v err=%v", windows, err)
	}
	windows, err = holdTimeWindows(nil, now, time.Time{}, time.Hour, quiet, defaults)
	if err != nil || len(windows) == 0 || windows[0].Start != "2026-11-02T09:00:00Z" {
		t.Fatalf("default windows=%#v err=%v", windows, err)
	}
}

func TestHoldWeekendDeadlineOnlyWhenNoEarlierSlot(t *testing.T) {
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	due := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	quiet := engagement.QuietWindow{Start: 22 * time.Hour, End: 6 * time.Hour}
	windows, err := holdTimeWindows(nil, now, due, time.Hour, quiet, inferHoldWorkHours(nil, now))
	if err != nil || len(windows) != 1 || windows[0].Start != "2026-10-30T09:00:00Z" {
		t.Fatalf("windows=%#v err=%v", windows, err)
	}
}

func TestHoldTimeOnDayKeepsLocalHourAcrossDaylightSaving(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2027, 3, 14, 0, 0, 0, 0, loc)
	if got := holdTimeOnDay(day, 9*time.Hour); got.Hour() != 9 {
		t.Fatalf("local time=%s", got)
	}
}

func TestHoldWorkHoursExcludeFirstRunSessions(t *testing.T) {
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	rows := []ActivitySessionRow{{ID: "guided", StartTime: now.Add(-16 * time.Hour).UnixMilli(), EndTime: now.Add(-time.Hour).UnixMilli()}, {ID: "normal", StartTime: now.Add(-9 * time.Hour).UnixMilli(), EndTime: now.Add(-2 * time.Hour).UnixMilli()}}
	activity := holdActivityFromSessions(rows, []FirstRunExclusionRecord{{SourceKind: "activity_session", SourceID: "guided"}})
	profile := inferHoldWorkHours(activity, now)
	if len(activity) != 1 || profile.Start != 9*time.Hour || profile.End != 16*time.Hour {
		t.Fatalf("activity=%#v profile=%#v", activity, profile)
	}
}

func TestHoldAlternativesStopAfterTwoFollowingWorkdays(t *testing.T) {
	now := time.Date(2026, 10, 30, 18, 0, 0, 0, time.UTC)
	quiet := engagement.QuietWindow{Start: 22 * time.Hour, End: 6 * time.Hour}
	busy := []calendar.Event{{Start: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 3, 18, 0, 0, 0, time.UTC)}}
	windows, err := holdTimeWindows(busy, now, time.Time{}, time.Hour, quiet, inferHoldWorkHours(nil, now))
	if err != nil || len(windows) != 0 {
		t.Fatalf("offered beyond next two workdays: %#v err=%v", windows, err)
	}
}
