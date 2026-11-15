package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
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

// Yes saves the proposal as a Task and starts it; the task's result comes back
// to the topic. No and "not mine" are feedback, and a topic that is not mine
// is left out of the next copy. Every answer is in the feedback log.
func TestDailyTopicAnswers(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	handoffs, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.handoffStore = handoffs
	var started []string
	previous := dailyTopicTaskStart
	dailyTopicTaskStart = func(_ *App, id string) error { started = append(started, id); return nil }
	t.Cleanup(func() { dailyTopicTaskStart = previous })
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	view, _ := app.GetDailyTopics()
	harbor, tide := view.Topics[0], view.Topics[1]

	got, err := app.AnswerDailyTopicProposal(harbor.ID, harbor.Proposal.ID, topicday.VerdictYes)
	if err != nil || got.Proposal.State != topicday.StateRunning || len(started) != 1 || got.Proposal.TaskID != started[0] {
		t.Fatalf("yes = %+v %v; started %v", got.Proposal, err, started)
	}
	entry, err := handoffs.Get(started[0])
	if err != nil || entry.Body != "Write a launch checklist for the Harbor launch." || entry.Thread == nil || entry.Thread.Origin.Kind != topicsDailyTaskOrigin {
		t.Fatalf("task = %+v %v", entry, err)
	}
	if _, err := app.AnswerDailyTopicProposal(harbor.ID, harbor.Proposal.ID, topicday.VerdictNo); err == nil {
		t.Fatal("an answered proposal was answered again")
	}
	// The task finishes: its result comes back to the topic.
	for _, status := range []handoff.Status{handoff.StatusInProgress, handoff.StatusNeedsReview} {
		if _, err := handoffs.UpdateStatus(entry.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	entry, _ = handoffs.Get(entry.ID)
	entry.Thread.Outcome = &handoff.Outcome{Kind: handoff.OutcomeCompleted, Params: map[string]string{"summary": "Checklist drafted"}}
	if _, err := handoffs.Save(entry); err != nil {
		t.Fatal(err)
	}
	view, _ = app.GetDailyTopics()
	if p := view.Topics[0].Proposal; p.State != topicday.StateDone || p.Result != "Checklist drafted" {
		t.Fatalf("result did not come back: %+v", p)
	}

	if _, err := app.AnswerDailyTopicProposal(tide.ID, tide.Proposal.ID, topicday.VerdictNotMine); err != nil {
		t.Fatal(err)
	}
	store, _ := app.topicsDailyStore()
	feedback, _ := store.Feedback()
	if len(feedback) != 2 || feedback[0].Verdict != topicday.VerdictYes || feedback[1].Verdict != topicday.VerdictNotMine {
		t.Fatalf("feedback = %+v", feedback)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	view, _ = app.GetDailyTopics()
	for _, topic := range view.Topics {
		if topic.ID == tide.ID {
			t.Fatal("a not-mine topic came back in the next copy")
		}
	}
}
