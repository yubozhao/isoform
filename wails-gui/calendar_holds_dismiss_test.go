//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"database/sql"
	"testing"
	"time"
)

func TestHoldOfferDismissalLastsOneDayAndCanBeUndone(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateCalendarHoldDismissals(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)
	c := &holdCoordinator{db: db, now: func() time.Time { return now }}
	thing := HoldThing{Title: "Plan", SourceKind: "task", SourceID: "t"}
	id, err := c.dismissOffer(thing)
	if err != nil {
		t.Fatal(err)
	}
	if dismissed, err := c.offerDismissed(thing, now); err != nil || !dismissed {
		t.Fatalf("dismissed=%v err=%v", dismissed, err)
	}
	if err := c.undoDismissOffer(id); err != nil {
		t.Fatal(err)
	}
	if dismissed, _ := c.offerDismissed(thing, now); dismissed {
		t.Fatal("undo did not restore offer")
	}
	id, err = c.dismissOffer(thing)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	if err := c.undoDismissOffer(id); err == nil {
		t.Fatal("accepted undo after five seconds")
	}
	if dismissed, _ := c.offerDismissed(thing, now.AddDate(0, 0, 1)); dismissed {
		t.Fatal("dismissal persisted into the next day")
	}
}
