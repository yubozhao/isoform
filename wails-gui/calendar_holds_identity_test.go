//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"database/sql"
	"testing"
	"time"
)

func TestHoldIdentityLinksCommitmentAcrossSurfaces(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateCalendarHolds(tx); err != nil {
		t.Fatal(err)
	}
	if err := migrateCalendarHoldActiveSource(tx); err != nil {
		t.Fatal(err)
	}
	if err := migrateCalendarHoldDismissals(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	store := &fakeHoldEventStore{calendars: []holdCalendar{{ID: "work"}}}
	c := &holdCoordinator{db: db, calendar: store, now: func() time.Time { return now }, verifyCoverage: func(holdEvent) error { return nil }}
	promise := HoldThing{Title: "Plan", SourceKind: "promise", SourceID: "purpose-id"}
	week := HoldThing{Title: "Plan", SourceKind: "week", SourceID: mobileBriefItemKey("commitments", "purpose", promise.SourceID)}
	brief := HoldThing{Title: "Plan", SourceKind: "brief", SourceID: week.SourceID}
	if holdSubjectKey(promise) != holdSubjectKey(week) || holdSubjectKey(week) != holdSubjectKey(brief) {
		t.Fatal("same commitment did not get one identity")
	}
	h := holdRecord{ID: "h", CalendarID: "work", Title: "Plan", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), SourceKind: promise.SourceKind, SourceID: promise.SourceID}
	if err := c.begin(h); err != nil {
		t.Fatal(err)
	}
	existing, err := c.activeForSource(week, now)
	if err != nil || existing == nil || existing.ID != "h" || existing.SourceKind != "promise" || existing.SourceID != "purpose-id" {
		t.Fatalf("existing=%#v err=%v", existing, err)
	}
	h.ID, h.SourceKind, h.SourceID = "duplicate", week.SourceKind, week.SourceID
	if err := c.begin(h); err == nil {
		t.Fatal("accepted the same commitment from another surface")
	}
	if _, err := c.dismissOffer(brief); err != nil {
		t.Fatal(err)
	}
	if dismissed, err := c.offerDismissed(promise, now); err != nil || !dismissed {
		t.Fatalf("dismissed=%v err=%v", dismissed, err)
	}
}
