package main

import (
	"context"
	"testing"

	"wails-gui/internal/topicday"
)

// A No from Your work keeps its reason for the next build, and an Undo takes
// the answer back there too: an undone No is no longer a decline.
func TestTopicAnswersReachTheNextBuildWithTheirReasons(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	view, _ := app.GetDailyTopics()
	tide := view.Topics[1]
	id := workTopicProposalID(tide.ID, tide.Proposal.ID)
	if _, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionNo, Reason: topicday.ReasonAlreadyDone}); err != nil {
		t.Fatal(err)
	}
	store, _ := app.topicsDailyStore()
	feedback, _ := store.Feedback()
	if n := len(feedback); n == 0 || feedback[n-1].Verdict != topicday.VerdictNo || feedback[n-1].Reason != topicday.ReasonAlreadyDone {
		t.Fatalf("feedback = %+v", feedback)
	}
	if _, err := app.UndoWork(id); err != nil {
		t.Fatal(err)
	}
	feedback, _ = store.Feedback()
	if standing := topicday.Standing(feedback); len(standing) != 0 {
		t.Fatalf("after Undo the standing feedback = %+v", standing)
	}
	// The proposal is offered again, and a later No counts once.
	if _, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionNo, Reason: topicday.ReasonNotUseful}); err != nil {
		t.Fatal(err)
	}
	if standing := topicday.Standing(mustFeedback(t, store)); len(standing) != 1 || standing[0].Reason != topicday.ReasonNotUseful {
		t.Fatalf("standing after a second No = %+v", standing)
	}
	if line := topicsDailyRestedLine(map[string]int{topicday.RestStale: 2}); line != "already_done:0,not_now:0,declined:0,stale:2" {
		t.Fatalf("rested line = %q", line)
	}
}

func mustFeedback(t *testing.T, store *topicday.Store) []topicday.Feedback {
	t.Helper()
	feedback, err := store.Feedback()
	if err != nil {
		t.Fatal(err)
	}
	return feedback
}
