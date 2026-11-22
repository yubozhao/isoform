package main

import (
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

func TestGetYourDataSummaryCountsThisWeeksHandoffsOnly(t *testing.T) {
	store, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &App{handoffStore: store}
	old := time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	for _, entry := range []handoff.Entry{
		{ID: "this-week", Status: handoff.StatusInProgress, Backend: "codex"},
		{ID: "today", Status: handoff.StatusInProgress, Backend: "codex"},
		{ID: "app", Status: handoff.StatusInProgress},
		{ID: "last-month", Status: handoff.StatusInProgress, CreatedAt: old, UpdatedAt: old},
	} {
		if _, err := store.Save(entry); err != nil {
			t.Fatal(err)
		}
	}
	got, err := app.GetYourDataSummary()
	if err != nil {
		t.Fatal(err)
	}
	if got.HandoffsThisWeek != 3 {
		t.Fatalf("handoffs this week = %d, want 3", got.HandoffsThisWeek)
	}
	// Per agent, for "this week {n}: {agent} {k}" (computer-data v1.0.0 rule 5);
	// last month's isn't counted.
	if got.HandoffsByAgent["codex"] != 2 || got.HandoffsByAgent[""] != 1 || len(got.HandoffsByAgent) != 2 {
		t.Fatalf("handoffs by agent = %v, want codex 2 and the app's own 1", got.HandoffsByAgent)
	}
	if got.IgnoredApps < 1 || got.RetentionDays < 0 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestGetYourDataSummaryReadsZeroWithoutStores(t *testing.T) {
	got, err := (&App{}).GetYourDataSummary()
	if err != nil {
		t.Fatal(err)
	}
	if got.CapBytes <= 0 || got.UsedBytes != 0 {
		t.Fatalf("storage without stores = %+v", got)
	}
	if got.HandoffsThisWeek != 0 || got.PairedDevices != 0 || got.HandoffsByAgent == nil {
		t.Fatalf("summary without stores = %+v", got)
	}
}

func TestGetYourDataSummaryCountsKeptAnswers(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	first, _ := app.recordAnswer(AnswerRecord{ItemID: "belief:m1", Kind: answerKindBelief, Action: workActionYes, Surface: workSurfaceComputer})
	if _, err := app.recordAnswer(AnswerRecord{ItemID: "portrait", Kind: answerKindPortrait, Action: workActionNo, Surface: workSurfaceComputer}); err != nil {
		t.Fatal(err)
	}
	taken, _ := app.recordAnswer(AnswerRecord{ItemID: "belief:m2", Kind: answerKindBelief, Action: workActionNo, Surface: workSurfaceComputer})
	if err := app.undoAnswerFor("belief:m2", answerKindBelief, workSurfaceComputer); err != nil {
		t.Fatal(err)
	}
	got, err := app.GetYourDataSummary()
	if err != nil {
		t.Fatal(err)
	}
	if got.Answers != 2 {
		t.Fatalf("answers = %d, want 2 (the undone one %s is taken back)", got.Answers, taken.EntryID)
	}
	if got.AnswersSince != first.At {
		t.Fatalf("answers since = %q, want the first answer's %q", got.AnswersSince, first.At)
	}
}

// A read that fails is unknown, never off: without a store the Messages
// switch can't be read, so messagesOn is nil rather than false.
func TestGetYourDataSummaryLeavesAnUnreadableSwitchUnknown(t *testing.T) {
	got, err := (&App{}).GetYourDataSummary()
	if err != nil {
		t.Fatal(err)
	}
	if got.MessagesOn != nil {
		t.Fatalf("messagesOn = %v without a store; want unknown (nil)", *got.MessagesOn)
	}
	if got.CallsOn == nil {
		t.Fatal("callsOn is always readable")
	}
}
