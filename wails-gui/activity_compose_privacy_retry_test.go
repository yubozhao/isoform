package main

import (
	"context"
	"testing"

	"wails-gui/internal/platform/activitymon"
)

func TestComposerPrivacyReadFailureRemainsRetryable(t *testing.T) {
	isolateActivityPrivacyTest(t)
	app := newPrivacyReadTestApp(t)
	store := app.activity.store
	runner := &activityPrivacyTaskRecorder{}
	app.runner = runner
	session := ActivitySessionRow{ID: "retry-source", StartTime: 1000, EndTime: 61_000, AppNames: `["Notes"]`, TriageStatus: "done"}
	if err := store.SaveSession(session, []activitymon.ActivityEvent{{Kind: activitymon.EventFocus, AppName: "Notes", Timestamp: 2000, WindowTitle: "Project notes"}}, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := store.GetSessionEventRows(session.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("source events: rows=%v err=%v", rows, err)
	}
	run := testRun("tg_privacy_retry", "Notes", 1000, 61_000, 60_000, 1)
	run.SessionIDs = []string{session.ID}
	run.FirstEventRowID, run.LastEventRowID, run.EventCount = rows[0].ID, rows[0].ID, 1
	insertComposableTimelineRun(t, store, run, ActivityTimelineGroupSummary{Title: "Project notes"})
	// Keep the screen switches readable so the composer reaches the pause log.
	if err := app.store.SetUIState(t.Context(), observationPausesKey, "invalid JSON"); err != nil {
		t.Fatal(err)
	}
	triager := &ActivityTriager{app: app}
	if changed, err := triager.composeRun(context.Background(), run, 62_000); err == nil || changed {
		t.Fatalf("privacy read failure was swallowed: changed=%v err=%v", changed, err)
	}
	if len(runner.inputs) != 0 {
		t.Fatal("unverified privacy reached a model")
	}
	ready, err := store.GetComposerReadyRuns(10, 0, 0)
	if err != nil || len(ready) != 1 || ready[0].ID != run.ID {
		t.Fatalf("read failure permanently removed run from queue: ready=%v err=%v", ready, err)
	}
	if err := app.store.SetUIState(t.Context(), observationPausesKey, "[]"); err != nil {
		t.Fatal(err)
	}
	if _, err := triager.composeRun(context.Background(), run, 63_000); err != nil {
		t.Fatalf("retry after privacy source recovered: %v", err)
	}
	if len(runner.inputs) != 1 {
		t.Fatalf("recoverable run never retried: calls=%d", len(runner.inputs))
	}
}
