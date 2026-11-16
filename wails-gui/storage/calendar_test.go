package storage

import (
	"context"
	"testing"
)

func calendarEventFixture(id string, start, end int64) CalendarEventRow {
	return CalendarEventRow{
		ID: id, UID: "uid-" + id, Source: "eventkit", Provider: "google", Title: "Event " + id,
		StartAt: start, EndAt: end, Availability: "busy", Status: "confirmed", MyStatus: "accepted",
		ConferenceURL: "https://zoom.us/j/" + id, ConferenceHost: "Zoom", AttendeeCount: 1,
		Attendees: []CalendarAttendeeRow{
			{Email: "me@example.com", Name: "Me", Role: "required", Status: "accepted", IsMe: true},
			{Email: "ann@example.com", Name: "Ann", Role: "required", Status: "accepted", PrincipalID: "p-ann"},
		},
	}
}

func TestCalendarEventsUpsertListAndVanish(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := calendarEventFixture("a", 1_000, 2_000)
	b := calendarEventFixture("b", 3_000, 4_000)
	if err := s.UpsertCalendarEvents(ctx, []CalendarEventRow{a, b, {ID: "  "}}, 10); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	events, err := s.ListCalendarEvents(ctx, 0, 10_000, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 2 || events[0].ID != "a" || events[1].ID != "b" {
		t.Fatalf("events %+v", events)
	}
	if len(events[0].Attendees) != 2 || !events[0].Attendees[0].IsMe || events[0].Attendees[1].PrincipalID != "p-ann" {
		t.Fatalf("attendees %+v", events[0].Attendees)
	}
	if events[0].FirstSeenAt != 10 || events[0].UpdatedAt != 10 || events[0].ConferenceHost != "Zoom" {
		t.Fatalf("row %+v", events[0])
	}

	// A second sync keeps first_seen_at and the session link, updates the
	// rest and replaces the attendees.
	if err := s.SetCalendarEventSession(ctx, "a", "session-1"); err != nil {
		t.Fatalf("set session: %v", err)
	}
	a2 := a
	a2.Title = "Renamed"
	a2.Attendees = a.Attendees[:1]
	if err := s.UpsertCalendarEvents(ctx, []CalendarEventRow{a2}, 20); err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	got, ok, err := s.GetCalendarEvent(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	if got.Title != "Renamed" || got.FirstSeenAt != 10 || got.UpdatedAt != 20 || got.SessionID != "session-1" || len(got.Attendees) != 1 {
		t.Fatalf("updated row %+v", got)
	}
	linked, err := s.ListCalendarEventsForSession(ctx, "session-1")
	if err != nil || len(linked) != 1 || linked[0].ID != "a" {
		t.Fatalf("for session: %+v %v", linked, err)
	}

	// b vanished from the window; a was seen.
	changed, err := s.MarkCalendarEventsVanished(ctx, "eventkit", "", 0, 10_000, map[string]bool{"a": true}, 30)
	if err != nil || changed != 1 {
		t.Fatalf("vanish: %d %v", changed, err)
	}
	live, _ := s.ListCalendarEvents(ctx, 0, 10_000, false)
	if len(live) != 1 || live[0].ID != "a" {
		t.Fatalf("live %+v", live)
	}
	all, _ := s.ListCalendarEvents(ctx, 0, 10_000, true)
	if len(all) != 2 || all[1].DeletedAt != 30 {
		t.Fatalf("all %+v", all)
	}
	// Seen again: undeleted.
	if err := s.UpsertCalendarEvents(ctx, []CalendarEventRow{b}, 40); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	live, _ = s.ListCalendarEvents(ctx, 0, 10_000, false)
	if len(live) != 2 {
		t.Fatalf("b should be live again: %+v", live)
	}
	// Another source's events are never touched by this source's vanish pass.
	web := calendarEventFixture("w", 5_000, 6_000)
	web.Source = "web"
	_ = s.UpsertCalendarEvents(ctx, []CalendarEventRow{web}, 50)
	changed, _ = s.MarkCalendarEventsVanished(ctx, "eventkit", "", 0, 10_000, map[string]bool{"a": true, "b": true}, 60)
	if changed != 0 {
		t.Fatalf("web event must survive an eventkit vanish pass: %d", changed)
	}
	// A provider filter leaves the other provider's web events alone.
	outlook := calendarEventFixture("o", 5_000, 6_000)
	outlook.Source, outlook.Provider = "web", "outlook"
	_ = s.UpsertCalendarEvents(ctx, []CalendarEventRow{outlook}, 55)
	changed, _ = s.MarkCalendarEventsVanished(ctx, "web", "google", 0, 10_000, map[string]bool{}, 65)
	if changed != 1 {
		t.Fatalf("only the google web event vanishes: %d", changed)
	}
	if row, _, _ := s.GetCalendarEvent(ctx, "o"); row.DeletedAt != 0 {
		t.Fatalf("outlook event must stay live: %+v", row)
	}
	_ = s.UpsertCalendarEvents(ctx, []CalendarEventRow{web}, 66)
	_, _ = s.PruneCalendarEvents(ctx, 0)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM calendar_events WHERE id = 'o'`); err != nil {
		t.Fatal(err)
	}

	// Pruning drops old events without a session only.
	if err := s.SetCalendarEventSession(ctx, "b", "session-2"); err != nil {
		t.Fatal(err)
	}
	removed, err := s.PruneCalendarEvents(ctx, 7_000)
	if err != nil || removed != 1 {
		t.Fatalf("prune: %d %v", removed, err)
	}
	if _, ok, _ := s.GetCalendarEvent(ctx, "w"); ok {
		t.Fatal("the web event without a session should be pruned")
	}
	if _, ok, _ := s.GetCalendarEvent(ctx, "a"); !ok {
		t.Fatal("a linked event is kept")
	}
}

func TestCalendarSources(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertCalendarSource(ctx, CalendarSourceRow{ID: "eventkit:g", Kind: "eventkit", Provider: "google", Title: "Google", DetectedBy: "eventkit", Calendars: 2, LastSeenAt: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.UpsertCalendarSource(ctx, CalendarSourceRow{ID: "web:google", Kind: "web", Provider: "google", Title: "calendar.google.com", DetectedBy: "history", FirstSeenAt: 50, LastSeenAt: 90}); err != nil {
		t.Fatalf("upsert web: %v", err)
	}
	// Seen again later by another signal: first sighting and detection kept.
	if err := s.UpsertCalendarSource(ctx, CalendarSourceRow{ID: "web:google", Kind: "web", Provider: "google", DetectedBy: "screen", LastSeenAt: 200, LastReadAt: 150}); err != nil {
		t.Fatalf("upsert web again: %v", err)
	}
	if err := s.TouchCalendarSourceRead(ctx, "eventkit:g", 120); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchCalendarSourceRead(ctx, "eventkit:g", 110); err != nil {
		t.Fatal(err)
	}
	sources, err := s.ListCalendarSources(ctx)
	if err != nil || len(sources) != 2 {
		t.Fatalf("list: %+v %v", sources, err)
	}
	ek, web := sources[0], sources[1]
	if ek.ID != "eventkit:g" || ek.FirstSeenAt != 100 || ek.LastReadAt != 120 || ek.Calendars != 2 {
		t.Fatalf("eventkit %+v", ek)
	}
	if web.FirstSeenAt != 50 || web.LastSeenAt != 200 || web.DetectedBy != "history" || web.Title != "calendar.google.com" || web.LastReadAt != 150 {
		t.Fatalf("web %+v", web)
	}
	if err := s.DeleteCalendarSourcesOfKind(ctx, "eventkit", map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	sources, _ = s.ListCalendarSources(ctx)
	if len(sources) != 1 || sources[0].Kind != "web" {
		t.Fatalf("after delete %+v", sources)
	}
	if err := s.UpsertCalendarSource(ctx, CalendarSourceRow{Kind: "web"}); err == nil {
		t.Fatal("an empty id must be rejected")
	}
}

func TestMeetingFollowups(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, ok, err := s.GetMeetingFollowup(ctx, "none"); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
	row := MeetingFollowupRow{SessionID: "sess", EventID: "ev", Title: "Pricing sync", DecisionsJSON: `["ship"]`, FollowupsJSON: `[{"text":"send deck","owner":"me"}]`}
	if err := s.SaveMeetingFollowup(ctx, row); err != nil {
		t.Fatalf("save: %v", err)
	}
	row.DecisionsJSON = `["ship","hire"]`
	if err := s.SaveMeetingFollowup(ctx, row); err != nil {
		t.Fatalf("save again: %v", err)
	}
	got, ok, err := s.GetMeetingFollowup(ctx, "sess")
	if err != nil || !ok || got.DecisionsJSON != `["ship","hire"]` || got.CreatedAt <= 0 || got.Title != "Pricing sync" {
		t.Fatalf("get: %+v %v %v", got, ok, err)
	}
	if err := s.SaveMeetingFollowup(ctx, MeetingFollowupRow{}); err == nil {
		t.Fatal("a session id is required")
	}
}

func TestDeleteAllCalendarEventsKeepsTheSources(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertCalendarEvents(ctx, []CalendarEventRow{calendarEventFixture("a", 1_000, 2_000), calendarEventFixture("b", 3_000, 4_000)}, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCalendarSource(ctx, CalendarSourceRow{ID: "src", Kind: "eventkit", Title: "Work", FirstSeenAt: 1, LastSeenAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAllCalendarEvents(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListCalendarEvents(ctx, 0, 10_000, true)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after delete = %d %v", len(events), err)
	}
	var attendees, sources int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM calendar_event_attendees`).Scan(&attendees)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM calendar_sources`).Scan(&sources)
	if attendees != 0 || sources != 1 {
		t.Fatalf("attendees = %d, sources = %d; want 0 and 1", attendees, sources)
	}
}
