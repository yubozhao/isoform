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

func TestCollectMobileBriefCalendarMorning(t *testing.T) {
	app, st := newLinkTestApp(t)
	loc := time.UTC
	asOf := time.Date(2026, 10, 14, 7, 0, 0, 0, loc)
	at := func(hhmm string) time.Time { return time.Date(2026, 10, 14, 0, 0, 0, 0, loc).Add(mustDuration(hhmm)) }
	standup := prepEvent("standup", "Standup", at("09:30"), 15, "Zoom", 2)
	standup.Attendees = []calendar.Attendee{{Name: "Ann", Email: "ann@example.com"}, {Name: "Bob", Email: "bob@example.com"}}
	review := prepEvent("review", "Design review", at("09:45"), 60, "Google Meet", 1)
	lunch := prepEvent("lunch", "Lunch with Cy", at("12:30"), 60, "", 1)
	lunch.Location = "Cafe Nero"
	invite := prepEvent("invite", "Offsite planning", at("16:00"), 60, "", 3)
	invite.MyStatus = calendar.StatusPending
	declined := prepEvent("declined", "Skip me", at("15:00"), 30, "Zoom", 1)
	declined.MyStatus = calendar.StatusDeclined
	tomorrow := prepEvent("tmrw", "Tomorrow", at("09:00").Add(24*time.Hour), 30, "Zoom", 1)
	tomorrow.Source = calendar.SourceEventKit
	tomorrow.Attendees = []calendar.Attendee{{Name: "Dana", Email: "dana@example.com"}, {Email: "private@example.com"}}
	tomorrowDeclined := prepEvent("tmrw-declined", "Declined tomorrow", at("10:00").Add(24*time.Hour), 30, "Zoom", 1)
	tomorrowDeclined.MyStatus = calendar.StatusDeclined
	for _, ev := range []calendar.Event{standup, review, lunch, invite, declined, tomorrow, tomorrowDeclined} {
		storeEvent(t, st, ev)
	}

	in := mobileBriefInput{Slot: mobileBriefSlotMorning, WindowStart: asOf.Add(-13 * time.Hour), AsOf: asOf, Location: loc}
	canReadTomorrow := app.morningBriefCalendarReadAllowed(context.Background())
	collectMobileBriefCalendar(app, &in)

	var titles []string
	byID := map[string]mobileBriefCandidate{}
	for _, c := range in.Meetings {
		titles = append(titles, c.Title)
		byID[c.SourceID] = c
	}
	if len(in.Meetings) != 5 {
		t.Fatalf("meetings %v", titles)
	}
	if got := byID["standup"]; got.SourceKind != "calendar" || got.Status != "upcoming" || got.Kind != "meeting" || got.Detail != "9:30 AM to 9:45 AM; Zoom; Ann, Bob" || !got.At.Equal(standup.Start) {
		t.Fatalf("standup %+v", got)
	}
	if got := byID["lunch"]; got.Detail != "12:30 PM to 1:30 PM; Cafe Nero; Ann" {
		t.Fatalf("lunch %+v", got)
	}
	if got := byID["invite"]; !strings.HasSuffix(got.Detail, "; invite not answered") || !got.rankedInvitePending {
		t.Fatalf("invite %+v", got)
	}
	if _, ok := byID["tmrw"]; ok {
		t.Fatal("tomorrow is not in today's brief")
	}
	if !canReadTomorrow && len(in.TomorrowMeetings) != 0 {
		t.Fatalf("tomorrow scoring candidates = %+v", in.TomorrowMeetings)
	}
	if canReadTomorrow && (len(in.TomorrowMeetings) != 1 || in.TomorrowMeetings[0].SourceID != "tmrw" || in.TomorrowMeetings[0].rankedOriginDigest == "" || !strings.Contains(in.TomorrowMeetings[0].Detail, "Dana")) {
		t.Fatalf("tomorrow scoring candidates = %+v", in.TomorrowMeetings)
	}
	stored, found, err := st.GetCalendarEvent(context.Background(), "tmrw")
	if err != nil || !found {
		t.Fatalf("tomorrow source: found=%v err=%v", found, err)
	}
	current := calendarEventFromRow(stored)
	prepared := mobileBriefCalendarCandidates(nil, []calendar.Event{current}, mobileBriefSlotMorning, asOf, loc, "")
	if len(prepared) != 1 {
		t.Fatalf("tomorrow preparation = %+v", prepared)
	}
	if len(prepared[0].rankedAttendees) != 1 || prepared[0].rankedAttendees[0] != "Dana" {
		t.Fatalf("scoring attendees = %+v", prepared[0].rankedAttendees)
	}
	rows := morningBriefGatherCandidates(mobileBriefInput{AsOf: asOf, Location: loc, TomorrowMeetings: prepared})
	if len(rows) != 1 || len(rows[0].Attendees) != 1 || rows[0].Attendees[0] != "Dana" {
		t.Fatalf("gathered attendees = %+v", rows)
	}
	digest := prepared[0].rankedSourceDigest
	if digest == "" || digest != morningBriefCalendarSourceDigest(current) {
		t.Fatal("tomorrow candidate lost its source fingerprint")
	}
	current.Title = "Changed"
	if digest == morningBriefCalendarSourceDigest(current) {
		t.Fatal("calendar edit retained the old source fingerprint")
	}
	if err := st.SetUIState(context.Background(), permissionUIStateKey(calendarPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	if app.morningBriefCalendarSourceReadable(context.Background(), "tmrw", digest) {
		t.Fatal("disabled calendar remained readable")
	}
	shape, ok := byID["2026-10-14"]
	if !ok || shape.SourceKind != "calendar_day" || shape.Title != "The shape of today" {
		t.Fatalf("day shape %+v", shape)
	}
	for _, want := range []string{"Your first meeting is Standup at 9:30 AM.", "You have 2 meetings back to back from 9:30 AM to 10:45 AM.", "You're free 9:00 AM to 9:30 AM, ", "The Offsite planning invite still needs your answer."} {
		if !strings.Contains(shape.Detail, want) {
			t.Errorf("day shape missing %q in %q", want, shape.Detail)
		}
	}

	// The builder renders them with evidence refs, newest first within the section.
	brief := buildMobileBriefV1(in)
	if len(brief.Sections.Meetings) != 3 || len(brief.Evidence) != 3 || mobileBriefEvidenceCounts(brief.Evidence)[mobileBriefCalendarSource] != 2 {
		t.Fatalf("brief meetings %d evidence %d", len(brief.Sections.Meetings), len(brief.Evidence))
	}
}

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
