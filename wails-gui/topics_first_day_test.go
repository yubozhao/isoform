package main

import "testing"

// A fresh data folder has no topics yet: Today's topics read is the first-run
// empty state, never an error (setup walk, 2026-11-16).
func TestTodayTopicsOnAFreshFolderAreTheFirstRunState(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	view, err := app.GetTodayTopics()
	if err != nil {
		t.Fatalf("a fresh folder's topics read failed: %v", err)
	}
	if len(view.Topics) != 0 || !view.FirstRun {
		t.Fatalf("fresh folder topics = %d, first run = %v", len(view.Topics), view.FirstRun)
	}
}
