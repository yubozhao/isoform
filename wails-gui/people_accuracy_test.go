package main

import (
	"context"
	"testing"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/storage"
)

// A Slack app carries the APP badge; normalizing the badge away used to
// leave a sender named GitHub that became a person on Who you know.
func TestSenderLabelerNeverCreatesPeopleForAppsAndBots(t *testing.T) {
	ctx := context.Background()
	s := newLabelerTestStore(t)
	lbl := newSenderLabeler(s)
	for _, raw := range []string{"GitHub APP", "Jira Cloud APP 10:25", "Slackbot", "Yansu BOT"} {
		if got := lbl.Label(ctx, "slack", raw, "build failed", "", 0); got[:9] != "[Unknown:" {
			t.Errorf("%q labeled %q", raw, got)
		}
	}
	lbl.Label(ctx, "slack", "Maya Chen", "hi", "", 0)
	rows, err := s.ListPrincipals(ctx)
	if err != nil || len(rows) != 1 || rows[0].DisplayName != "Maya Chen" {
		t.Fatalf("only the person is kept: %+v %v", rows, err)
	}
}

// Real data: two WeChat people whose names differed only by a doubled space.
func TestSenderLabelerFoldsSpacingVariantsIntoOnePerson(t *testing.T) {
	ctx := context.Background()
	s := newLabelerTestStore(t)
	lbl := newSenderLabeler(s)
	lbl.Label(ctx, "wechat", "Mei  Chen 10:25", "a", "", 0)
	lbl.Label(ctx, "wechat", "Mei Chen", "b", "", 0)
	rows, err := s.ListPrincipals(ctx)
	if err != nil || len(rows) != 1 || rows[0].DisplayName != "Mei Chen" {
		t.Fatalf("one person: %+v %v", rows, err)
	}
	stats, err := s.GetPrincipalStats(ctx, rows[0].ID)
	if err != nil || stats.TotalMessages != 2 {
		t.Fatalf("both sightings on one person: %+v %v", stats, err)
	}
}

// Profiles created before the fix stay split until the person merges them;
// Who you know has to suggest it even though chat handles carry no id.
func TestPeopleOverviewSuggestsSpacingVariantDuplicates(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	ctx := t.Context()
	// Profiles without capture lineage, so the read gates admit them.
	profile := func(app, name string) storage.PrincipalRow {
		t.Helper()
		p, err := a.store.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindAnonymous, DisplayName: name})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.store.BindAppHandle(ctx, p.ID, app, "", name); err != nil {
			t.Fatal(err)
		}
		return p
	}
	one, two := profile("wechat", "Mei  Chen"), profile("wechat", "mei chen")
	for _, id := range []string{one.ID, two.ID, two.ID} {
		if err := a.store.BumpPrincipalSighting(ctx, id, "wechat", "", time.Now().UnixMilli(), ""); err != nil {
			t.Fatal(err)
		}
	}
	// Different names on the same app, and the same name on another app,
	// are not suggested.
	profile("wechat", "MeiChen")
	profile("slack", "Mei Chen")
	got, err := a.ListPeopleOverview()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Duplicates) != 1 {
		t.Fatalf("groups=%+v", got.Duplicates)
	}
	g := got.Duplicates[0]
	if g.Reason != "same_name" || g.App != "wechat" || len(g.MemberIDs) != 2 || g.SuggestedTargetID != two.ID {
		t.Fatalf("group=%+v", g)
	}
}

// Clean up folds the existing split rows and removes platform accounts.
func TestCleanupAnonymousFoldsSpacingAndDropsSystemSenders(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	ctx := t.Context()
	for _, name := range []string{"Mei  Chen", "Mei Chen", "Slackbot"} {
		p, err := a.store.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindAnonymous, DisplayName: name, CreatedFrom: "auto_from_chat"})
		if err != nil {
			t.Fatal(err)
		}
		if err := a.store.BumpPrincipalSighting(ctx, p.ID, "slack", "", time.Now().UnixMilli(), ""); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := a.CleanupAnonymousPrincipals()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Merged != 1 || rep.PrunedImplausible != 1 {
		t.Fatalf("report=%+v", rep)
	}
	rows, err := a.store.ListPrincipals(ctx)
	if err != nil || len(rows) != 1 || rows[0].DisplayName != "Mei Chen" {
		t.Fatalf("left=%+v %v", rows, err)
	}
}

// With two calendar accounts, each flags only its own address as the
// current user, so on a meeting read from account A the person's account-B
// address sat among the guests: meeting prep named the person to themselves.
func TestCalendarSyncTreatsEveryOwnAddressAsMe(t *testing.T) {
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()
	src := &fakeCalendarSource{cals: []calendar.RawCalendar{{ID: "cal-work", Title: "Work", SourceIdentifier: "src-google", SourceTitle: "Google", SourceType: "caldav"}}}
	svc, st := newCalendarTestService(t, src, now)
	// The second address is also known to the directory as the person.
	self, err := st.EnsureSelf(ctx, "Me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BindAppHandle(ctx, self.ID, "email", "me@home.example", "me@home.example"); err != nil {
		t.Fatal(err)
	}
	src.list = []calendar.RawEvent{
		rawEventAt("home", "Dentist", now.Add(time.Hour), 30,
			calendar.RawAttendee{Name: "Me", Email: "me@home.example", Status: "accepted", IsMe: true}),
		rawEventAt("work", "Planning", now.Add(2*time.Hour), 30,
			calendar.RawAttendee{Name: "Me (work)", Email: "me@work.example", Status: "accepted", IsMe: true},
			calendar.RawAttendee{Name: "Me", Email: "Me@Home.example", Status: "pending"},
			calendar.RawAttendee{Name: "Ann", Email: "ann@example.com", Status: "accepted"}),
	}
	res, err := svc.Sync(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range res.Events {
		if ev.UID == "work" && ev.AttendeeCount() != 1 {
			t.Fatalf("synced event guests: %+v", ev.Attendees)
		}
	}
	events, err := st.ListCalendarEvents(ctx, 0, now.Add(48*time.Hour).UnixMilli(), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		for _, att := range ev.Attendees {
			if att.Email != "ann@example.com" && (!att.IsMe || att.PrincipalID != "") {
				t.Fatalf("%s: own address read as a guest: %+v", ev.Title, att)
			}
		}
		if ev.Title == "Planning" && ev.AttendeeCount != 1 {
			t.Fatalf("attendee count %d", ev.AttendeeCount)
		}
	}
	stats, err := st.GetPrincipalStats(ctx, self.ID)
	if err != nil || stats.TotalMessages != 0 {
		t.Fatalf("no calendar sighting on the person themselves: %+v %v", stats, err)
	}
}

// Rows written before the sync marked every own address read correctly.
func TestCalendarAttendeesReadEarlierOwnAddressAsMe(t *testing.T) {
	ctx := context.Background()
	src := &fakeCalendarSource{}
	_, st := newCalendarTestService(t, src, time.Now())
	start := time.Now().Add(time.Hour).UnixMilli()
	rows := []storage.CalendarEventRow{
		{ID: "a", UID: "a", Source: "eventkit", Title: "A", StartAt: start, EndAt: start + 1,
			Attendees: []storage.CalendarAttendeeRow{{EventID: "a", Email: "me@home.example", IsMe: true}}},
		{ID: "b", UID: "b", Source: "eventkit", Title: "B", StartAt: start, EndAt: start + 1,
			Attendees: []storage.CalendarAttendeeRow{{EventID: "b", Email: "me@work.example", IsMe: true}, {EventID: "b", Email: "ME@home.example", PrincipalID: "p-stale"}, {EventID: "b", Email: "ann@example.com"}}},
	}
	if err := st.UpsertCalendarEvents(ctx, rows, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetCalendarEvent(ctx, "b")
	if err != nil || !ok {
		t.Fatal(err)
	}
	others := 0
	for _, att := range got.Attendees {
		if !att.IsMe {
			others++
			if att.Email != "ann@example.com" {
				t.Fatalf("own address read as a guest: %+v", att)
			}
		} else if att.PrincipalID != "" {
			t.Fatalf("own address keeps no person link: %+v", att)
		}
	}
	if others != 1 {
		t.Fatalf("guests=%d %+v", others, got.Attendees)
	}
}
