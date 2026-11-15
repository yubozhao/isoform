//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/engagement"
)

func TestHoldWorkerResumesPendingWriteWithoutPage(t *testing.T) {
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
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	saved := make(chan struct{})
	store := &fakeHoldEventStore{calendars: []holdCalendar{{ID: "work"}}, onSave: func() { close(saved) }}
	c := &holdCoordinator{db: db, calendar: store, now: func() time.Time { return now }, verifyCoverage: func(holdEvent) error { return nil }}
	h := holdRecord{ID: "h", CalendarID: "work", Title: "Plan", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), SourceKind: "task", SourceID: "t"}
	if err := c.begin(h); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runHoldWorker(ctx, c, make(chan time.Time), make(chan struct{})) }()
	select {
	case <-saved:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not resume pending hold")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	record, err := c.record("h")
	if err != nil || record.State != "held" {
		t.Fatalf("state=%q err=%v", record.State, err)
	}
}

func TestHoldSlotCoveredRequiresWholeWindow(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	event := holdEvent{Start: start, End: start.Add(time.Hour)}
	quiet := engagement.QuietWindow{Start: 20 * time.Hour, End: 21 * time.Hour}
	if !holdSlotCovered(event, nil, quiet) {
		t.Fatal("rejected free hold")
	}
	busy := calendar.Event{Start: start.Add(30 * time.Minute), End: start.Add(time.Hour)}
	if holdSlotCovered(event, []calendar.Event{busy}, quiet) {
		t.Fatal("accepted partial coverage")
	}
	quiet = engagement.QuietWindow{Start: 10*time.Hour + 30*time.Minute, End: 11 * time.Hour}
	if holdSlotCovered(event, nil, quiet) {
		t.Fatal("accepted quiet hours")
	}
}
