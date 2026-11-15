package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/topicday"
)

func dailyTopicsTestApp(t *testing.T) (*App, *memoryTranslationFakeRunner, *time.Time) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	runner := &memoryTranslationFakeRunner{response: `{"t1":{"title":"Draft the harbor launch checklist","why":"The launch is open.","prompt":"Write a launch checklist for the Harbor launch."},"t2":{"title":"Summarize this week's tide research","prompt":"Summarize the tide research."}}`}
	app := newTranslationTestApp(t, runner)
	clock := time.Date(2026, 11, 14, 6, 0, 0, 0, time.Local)
	previousNow := topicsDailyNow
	topicsDailyNow = func() time.Time { return clock }
	previousHook := topicsDailyCandidatesHook
	topicsDailyCandidatesHook = func(*App) []topicday.Candidate {
		return []topicday.Candidate{
			{ID: "t1", Kind: topicday.KindProject, Name: "Harbor launch", LastSeenAt: "2026-11-14T05:00:00Z", Next: "Ship the checklist"},
			{ID: "t2", Kind: topicday.KindSubject, Name: "Tide tables", LastSeenAt: "2026-11-13T05:00:00Z"},
		}
	}
	t.Cleanup(func() { topicsDailyNow, topicsDailyCandidatesHook = previousNow, previousHook })
	return app, runner, &clock
}

// The copy is built once a day: a day of hourly checks, and a restart, call the
// model once. Off by default, it calls nothing.
func TestDailyTopicsBuildOnceADay(t *testing.T) {
	app, runner, clock := dailyTopicsTestApp(t)
	ctx := context.Background()
	for hour := 0; hour < 3; hour++ {
		app.maybeBuildDailyTopics(ctx, "tick")
	}
	if runner.calls != 0 {
		t.Fatalf("the switch is off, yet the model was called %d times", runner.calls)
	}
	// Set the switch directly: SetDailyTopicsEnabled also starts a build in
	// the background, which would race the counted checks below.
	if err := app.store.SetUIState(context.Background(), topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	start := *clock
	for hour := 0; hour < 17; hour++ { // 06:00 to 22:00, one check an hour
		*clock = start.Add(time.Duration(hour) * time.Hour)
		app.maybeBuildDailyTopics(ctx, "tick")
	}
	if runner.calls != 1 {
		t.Fatalf("a day of hourly checks called the model %d times; want 1", runner.calls)
	}
	view, err := app.GetDailyTopics()
	if err != nil || !view.Enabled || len(view.Topics) != 2 {
		t.Fatalf("view = %+v %v", view, err)
	}
	if p := view.Topics[0].Proposal; p == nil || p.State != topicday.StateProposed || !strings.HasPrefix(p.Title, "Draft") {
		t.Fatalf("proposal = %+v", p)
	}
	// The next day builds again, once.
	*clock = start.Add(26 * time.Hour)
	app.maybeBuildDailyTopics(ctx, "tick")
	app.maybeBuildDailyTopics(ctx, "tick")
	if runner.calls != 2 {
		t.Fatalf("the next day called the model %d times in all; want 2", runner.calls)
	}
}

// The person's choices apply at build time: Closed Eyes builds nothing, and a
// topic they said is not theirs is left out of the next copy.
func TestDailyTopicsHonorClosedEyesAndNotMine(t *testing.T) {
	app, runner, _ := dailyTopicsTestApp(t)
	// Set the switch directly: SetDailyTopicsEnabled also starts a build in
	// the background, which would race the counted checks below.
	if err := app.store.SetUIState(context.Background(), topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	app.closedEyes.enabled.Store(true)
	if err := app.buildDailyTopics(context.Background()); err != topicday.ErrClosedEyes || runner.calls != 0 {
		t.Fatalf("Closed Eyes built a copy: %v, %d calls", err, runner.calls)
	}
	app.closedEyes.enabled.Store(false)
	store, _ := app.topicsDailyStore()
	if err := store.AppendFeedback(topicday.Feedback{TopicID: "t2", TopicName: "Tide tables", Verdict: topicday.VerdictNotMine, At: 1}); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, _ := app.GetDailyTopics()
	if len(view.Topics) != 1 || view.Topics[0].ID != "t1" {
		t.Fatalf("a not-mine topic was kept: %+v", view.Topics)
	}
	if strings.Contains(runner.lastPrompt, "Tide tables") {
		t.Fatal("a not-mine topic was sent to the model")
	}
	// The window never sees the task's instructions.
	if strings.Contains(strings.Join([]string{view.Topics[0].Proposal.Title, view.Topics[0].Proposal.Why}, " "), "Write a launch checklist") {
		t.Fatal("the proposal's prompt reached the view")
	}
}
