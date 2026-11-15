//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"database/sql"
	"testing"
	"time"

	"wails-gui/storage"
)

func TestHoldStripMarksOnlyVisibleMatchingEvents(t *testing.T) {
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
	start, end := now.Add(time.Hour).UnixMilli(), now.Add(2*time.Hour).UnixMilli()
	_, err = db.Exec(`INSERT INTO holds (id, event_identifier, calendar_id, title, start, end, source_kind, source_id, subject_key, state, created_at) VALUES ('h', 'local', 'work', 'private stored title', ?, ?, 'brief', 'q4', 'brief:q4', 'held', ?)`, start, end, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	shape := TodayDayShape{Events: []TodayDayShapeEvent{{ID: "visible", Title: "Renamed in Calendar"}, {ID: "web"}, {ID: "stale"}}}
	rows := []storage.CalendarEventRow{
		{ID: "visible", Source: "eventkit", LocalID: "local", CalendarID: "work", StartAt: start, EndAt: end},
		{ID: "hidden", Source: "eventkit", LocalID: "local", CalendarID: "work", StartAt: start, EndAt: end},
		{ID: "web", Source: "web", LocalID: "local", CalendarID: "work", StartAt: start, EndAt: end},
		{ID: "stale", Source: "eventkit", LocalID: "local", CalendarID: "work", StartAt: start + 1, EndAt: end},
	}
	if err := markHeldStripEvents(&shape, rows, db, now); err != nil {
		t.Fatal(err)
	}
	if shape.Events[0].HoldID != "h" || shape.Events[0].Title != "Renamed in Calendar" || shape.Events[1].HoldID != "" || shape.Events[2].HoldID != "" {
		t.Fatalf("shape=%+v", shape)
	}
	shape.Events[0].HoldID = ""
	if err := markHeldStripEvents(&shape, rows, db, now.Add(2*time.Hour)); err != nil || shape.Events[0].HoldID != "" {
		t.Fatalf("started hold remains interactive: shape=%+v err=%v", shape, err)
	}
}
