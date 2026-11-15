//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"context"
	"testing"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/storage"
)

func TestHoldSettingsExposeUncoveredBrowserCalendar(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	if err := app.store.UpsertCalendarSource(context.Background(), storage.CalendarSourceRow{ID: "web:google", Kind: calendar.SourceKindWeb, Provider: "google", LastSeenAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	got, err := app.holdSettings(&fakeHoldEventStore{}, true)
	if err != nil || len(got.BrowserOnly) != 1 || got.BrowserOnly[0] != "google" {
		t.Fatalf("settings=%+v err=%v", got, err)
	}
}

func TestHoldMeetingsDefaultUsesThirtyDaysAndMostUsedCalendar(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	calendars := []holdCalendar{{ID: "home", SourceID: "personal", IsDefault: true}, {ID: "work1", SourceID: "work"}, {ID: "work2", SourceID: "work"}}
	event := func(id, account string, age int, other bool) calendar.RawEvent {
		e := calendar.RawEvent{CalendarID: id, SourceID: account, Start: now.AddDate(0, 0, -age).Format(time.RFC3339)}
		if other {
			e.Attendees = []calendar.RawAttendee{{IsMe: true}, {IsMe: false}}
		}
		return e
	}
	events := []calendar.RawEvent{event("work1", "work", 1, true), event("work2", "work", 2, true), event("work2", "work", 3, false), event("home", "personal", 1, true), event("home", "personal", 31, true)}
	if got := holdMeetingsDefault(calendars, events, now); got != "work2" {
		t.Fatalf("default = %q", got)
	}
	if calendars[0].MeetingsAccount || !calendars[1].MeetingsAccount || !calendars[2].MeetingsAccount {
		t.Fatalf("account flags: %#v", calendars)
	}
	if got := holdMeetingsDefault(calendars[:1], events, now); got != "" {
		t.Fatalf("should fall back when meetings account is not writable: %q", got)
	}
}

func TestHoldSettingsDefaultAndWritableChoice(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	store := &fakeHoldEventStore{calendars: []holdCalendar{{ID: "home", IsDefault: true}, {ID: "work"}}}
	got, err := app.holdSettings(store, true)
	if err != nil || !got.OfferHolds || got.CalendarID != "home" {
		t.Fatalf("default: %#v %v", got, err)
	}
	if err := app.setHoldCalendar(store, true, "work"); err != nil {
		t.Fatal(err)
	}
	if err := app.SetOfferHolds(false); err != nil {
		t.Fatal(err)
	}
	got, err = app.holdSettings(store, true)
	if err != nil || got.OfferHolds || got.CalendarID != "work" {
		t.Fatalf("saved: %#v %v", got, err)
	}
	if err := app.setHoldCalendar(store, true, "readonly"); err == nil {
		t.Fatal("accepted a non-writable calendar")
	}
	if err := app.setHoldCalendar(store, false, "work"); err == nil {
		t.Fatal("accepted a choice without calendar access")
	}
	got, err = app.holdSettings(store, false)
	if err != nil || len(got.Calendars) != 0 || got.CalendarID != "" {
		t.Fatalf("exposed calendars without access: %#v %v", got, err)
	}
}
