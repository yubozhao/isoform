//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"testing"
	"time"
)

func TestHoldDayShapeAcceptsProposalWindowOnly(t *testing.T) {
	var app *App
	today := time.Now().In(time.Local)
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	for _, offset := range []int{0, 2, 7} {
		date := day.AddDate(0, 0, offset).Format(time.DateOnly)
		shape, err := app.GetHoldDayShape(date)
		if err != nil || shape.Hold != nil || shape.CalendarConnected {
			t.Fatalf("date %s: shape=%+v err=%v", date, shape, err)
		}
	}
	for _, date := range []string{day.AddDate(0, 0, -1).Format(time.DateOnly), day.AddDate(0, 0, 8).Format(time.DateOnly), "invalid"} {
		if _, err := app.GetHoldDayShape(date); err == nil {
			t.Fatalf("accepted %q", date)
		}
	}
}
