package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"wails-gui/storage"
)

// Closed Eyes stops every topic and Daily News search, however it was
// started (docs/destinations.md, assistant_search): Find new returns its
// paused state, and a search already queued sends nothing when it runs.

func stubClaudeResearch(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	previous := runClaudeResearch
	runClaudeResearch = func(context.Context, string) (string, researchMeta, error) {
		calls.Add(1)
		return "", researchMeta{}, nil
	}
	t.Cleanup(func() { runClaudeResearch = previous })
	return &calls
}

func TestFindNewUnderClosedEyesIsPaused(t *testing.T) {
	app := newTopicFindsTestApp(t)
	calls := stubClaudeResearch(t)
	app.closedEyes.enabled.Store(true)

	if got, err := app.startTopicFinds("manual"); err != nil || got != "paused" {
		t.Fatalf("Find new under Closed Eyes = %q, %v; want paused", got, err)
	}
	if got, err := app.startTopicFinds("daily"); err != nil || got != "paused" {
		t.Fatalf("the daily search under Closed Eyes = %q, %v; want paused", got, err)
	}
	view, err := app.FindNewOnTopics()
	if err != nil || view.Status != "paused" {
		t.Fatalf("Find new's view under Closed Eyes = %+v, %v", view, err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("a search was sent %d times under Closed Eyes", n)
	}
}

func TestQueuedTopicAndNewsSearchesSendNothingUnderClosedEyes(t *testing.T) {
	calls := stubClaudeResearch(t)
	app := &App{}
	app.closedEyes.enabled.Store(true)
	for _, name := range []string{todayNewsJobName, topicFindsJobName} {
		job := &storage.CronJobRow{ID: "job-" + name, Name: name, SurfaceKind: todayNewsSurfaceKind, TargetConfig: "{}"}

		_, err := app.executeAgentJob(context.Background(), job)
		if !errors.Is(err, errTopicSearchPaused) || !errors.Is(err, errAgentNotReady) {
			t.Fatalf("%s under Closed Eyes ran: %v", name, err)
		}
		// The Claude Code path asks again just before the words leave.
		if _, handled, err := app.runResearchJob(context.Background(), job, researchViaClaudeCode); !handled || !errors.Is(err, errTopicSearchPaused) {
			t.Fatalf("%s through Claude Code under Closed Eyes: handled=%v err=%v", name, handled, err)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("a queued search was sent %d times under Closed Eyes", n)
	}
}

func TestTopicSearchJobIsOnlyTopicsAndNews(t *testing.T) {
	for _, tc := range []struct {
		job  *storage.CronJobRow
		want bool
	}{
		{&storage.CronJobRow{Name: todayNewsJobName, SurfaceKind: todayNewsSurfaceKind}, true},
		{&storage.CronJobRow{Name: topicFindsJobName, SurfaceKind: todayNewsSurfaceKind}, true},
		{&storage.CronJobRow{Name: openAnswerJobPrefix + "x", SurfaceKind: openAnswerSurfaceKind}, false},
		{&storage.CronJobRow{Name: "A playbook", SurfaceKind: "playbook"}, false},
		{nil, false},
	} {
		if got := isTopicSearchJob(tc.job); got != tc.want {
			t.Errorf("isTopicSearchJob(%+v) = %v", tc.job, got)
		}
	}
}
