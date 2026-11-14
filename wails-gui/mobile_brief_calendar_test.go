package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

func TestTodayBriefCalendarEndTime(t *testing.T) {
	asOf := time.Date(2026, 10, 16, 7, 0, 0, 0, time.UTC)
	event := prepEvent("review", "Design review", asOf.Add(2*time.Hour), 30, "Zoom", 1)
	in := mobileBriefInput{
		Slot: mobileBriefSlotMorning, AsOf: asOf, Location: time.UTC,
		Meetings: mobileBriefCalendarCandidates(nil, []calendar.Event{event}, mobileBriefSlotMorning, asOf, time.UTC, "15:04"),
	}
	brief := buildMobileBriefV1(in)
	if len(brief.Sections.Meetings) != 1 {
		t.Fatalf("meetings = %+v", brief.Sections.Meetings)
	}
	got := brief.Sections.Meetings[0]
	if got.At != formatMobileBriefTime(event.Start) || got.EndAt != formatMobileBriefTime(event.End) {
		t.Fatalf("meeting lost the times needed to expire it at read time: %+v", got)
	}
}

func TestCollectMobileBriefCalendarEveningJoinsSessions(t *testing.T) {
	app, st := newLinkTestApp(t)
	loc := time.UTC
	asOf := time.Date(2026, 10, 14, 18, 0, 0, 0, loc)
	at := func(hhmm string) time.Time { return time.Date(2026, 10, 14, 0, 0, 0, 0, loc).Add(mustDuration(hhmm)) }
	done := prepEvent("done", "Pricing sync", at("10:00"), 30, "Zoom", 2)
	later := prepEvent("later", "Evening call", at("19:00"), 30, "Zoom", 1)
	storeEvent(t, st, done)
	storeEvent(t, st, later)
	ctx := context.Background()
	if err := st.SetCalendarEventSession(ctx, "done", "sess-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMeetingFollowup(ctx, storage.MeetingFollowupRow{SessionID: "sess-1", DecisionsJSON: `["ship"]`, FollowupsJSON: `[{"text":"deck","owner":"me"},{"text":"venue","owner":"other"}]`}); err != nil {
		t.Fatal(err)
	}

	in := mobileBriefInput{Slot: mobileBriefSlotEvening, WindowStart: asOf.Add(-11 * time.Hour), AsOf: asOf, Location: loc}
	collectMobileBriefCalendar(app, &in)
	if len(in.Meetings) != 2 {
		t.Fatalf("evening reviews the day and keeps the next meeting: %+v", in.Meetings)
	}
	got := in.Meetings[0]
	if got.SourceID != "done" || got.Status != "done" {
		t.Fatalf("done %+v", got)
	}
	if strings.Contains(got.Detail, "notes ready") || !strings.Contains(got.Detail, "1 decisions, 1 follow-ups for you") {
		t.Fatalf("outcome %q", got.Detail)
	}
	if strings.Contains(got.Detail, "10:00") || strings.Contains(got.Detail, "10:30") {
		t.Fatalf("past meeting repeats clock times: %q", got.Detail)
	}
}

func TestCollectMobileBriefCalendarWithoutStore(t *testing.T) {
	in := mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: time.Now(), Location: time.UTC}
	collectMobileBriefCalendar(&App{}, &in)
	if len(in.Meetings) != 0 {
		t.Fatal("no store means no calendar items")
	}
}

func TestMorningBriefCalendarOriginSeparatesEditsFromProvenance(t *testing.T) {
	start := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	event := prepEvent("origin", "Planning", start, 30, "", 1)
	event.Source = calendar.SourceEventKit
	origin := morningBriefCalendarOriginDigest(event, "session-a")
	content := morningBriefCalendarSourceDigest(event)
	event.Title, event.MyStatus = "Changed planning", calendar.StatusPending
	if morningBriefCalendarOriginDigest(event, "session-a") != origin || morningBriefCalendarSourceDigest(event) == content {
		t.Fatal("ordinary event edits changed its origin or retained its content fingerprint")
	}
	if morningBriefCalendarOriginDigest(event, "session-b") == origin {
		t.Fatal("changed session retained the old origin")
	}
	event.CalendarID = "different-calendar"
	if morningBriefCalendarOriginDigest(event, "session-a") == origin {
		t.Fatal("changed calendar retained the old origin")
	}
}

func TestPausedBriefCalendarProofKeepsPermissionGates(t *testing.T) {
	app, store := newLinkTestApp(t)
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous); app.closedEyes.enabled.Store(false) })
	pii.SetUserSensitiveApps(nil)
	if err := store.SetUIState(context.Background(), permissionUIStateKey(calendarPermissionKey), "true"); err != nil {
		t.Fatal(err)
	}
	app.closedEyes.enabled.Store(true)
	ctx := context.WithValue(context.Background(), pausedBriefReadKey{}, true)
	if !app.morningBriefCalendarReadAllowedWithGrant(ctx, true) {
		t.Fatal("selected brief could not verify permitted calendar while paused")
	}
	if app.morningBriefCalendarReadAllowedWithGrant(context.Background(), true) {
		t.Fatal("ordinary paused calendar read was allowed")
	}
	if app.morningBriefCalendarReadAllowedWithGrant(ctx, false) {
		t.Fatal("selected brief bypassed OS calendar permission")
	}
	if err := store.SetUIState(context.Background(), permissionUIStateKey(calendarPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	if app.morningBriefCalendarReadAllowedWithGrant(ctx, true) {
		t.Fatal("selected brief bypassed the calendar switch")
	}
}
