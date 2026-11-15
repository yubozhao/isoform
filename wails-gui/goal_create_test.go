//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"errors"
	"testing"
)

// About you v1.0.0 Y3: Add a goal opens a field in place and adds the goal at
// once, under Tracking.
func TestCreateGoalAddsATrackedGoalOnce(t *testing.T) {
	bridge, store, _ := mobileTestGoalBridge(t)
	app := bridge.app
	added, err := app.CreateGoal(GoalCreateInput{Text: "  Run a half marathon in March ", Key: "add-1"})
	if err != nil {
		t.Fatal(err)
	}
	if added.ID == "" || added.Title != "Run a half marathon in March" || added.State != PurposeStateActive {
		t.Fatalf("added = %+v", added)
	}
	again, err := app.CreateGoal(GoalCreateInput{Text: "Run a half marathon in March", Key: "add-1"})
	if err != nil || again.ID != added.ID {
		t.Fatalf("retry = %+v, %v; want the same goal", again, err)
	}
	if _, err := store.GetDetail(added.ID); err != nil {
		t.Fatalf("the goal is not in the ledger: %v", err)
	}
	page, err := app.ListGoals()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, goal := range page.Tracking {
		found = found || goal.ID == added.ID
	}
	if !found {
		t.Fatalf("the added goal is not under Tracking: %+v", page.Tracking)
	}
}

func TestCreateGoalRefusesBlankText(t *testing.T) {
	bridge, _, _ := mobileTestGoalBridge(t)
	for _, input := range []GoalCreateInput{{Text: "   ", Key: "k"}, {Text: "A goal", Key: ""}} {
		if _, err := bridge.app.CreateGoal(input); !errors.Is(err, errGoalMoveInvalid) {
			t.Fatalf("CreateGoal(%+v) = %v, want goal_invalid", input, err)
		}
	}
}
