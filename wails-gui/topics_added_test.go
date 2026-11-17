package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Topics v1.0.0 rule 30: a topic the person names is a topic at once, with its
// first proposal, without waiting for two days of work or the next build. The
// same name twice is one topic, and an empty name is refused.
func TestAddedTopicGetsItsFirstProposalAtOnce(t *testing.T) {
	app, runner, _ := dailyTopicsTestApp(t)
	if _, err := app.topicsSvc().update(UserTopicUpdate{Action: "add", Label: "  "}); err == nil {
		t.Fatal("an empty topic name was accepted")
	}
	for _, label := range []string{"Warriors season", "warriors  season"} {
		if _, err := app.topicsSvc().update(UserTopicUpdate{Action: "add", Label: label}); err != nil {
			t.Fatal(err)
		}
	}
	added := app.topicsSvc().store.load().Added
	if len(added) != 1 || added[0].Label != "Warriors season" || !strings.HasPrefix(added[0].ID, addedTopicPrefix) {
		t.Fatalf("added topics = %+v", added)
	}
	runner.response = `{"` + added[0].ID + `":{"title":"Recap the last three games","why":"You follow the season.","prompt":"Recap the Warriors' last three games."}}`
	app.proposeAddedTopics(context.Background())
	store, err := app.topicsDailyStore()
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := store.Load()
	if !ok || len(saved.Topics) == 0 || saved.Topics[0].ID != added[0].ID || saved.Topics[0].Proposal == nil ||
		saved.Topics[0].Proposal.Title != "Recap the last three games" {
		t.Fatalf("the named topic has no first proposal: %+v", saved.Topics)
	}
	calls := runner.calls
	app.proposeAddedTopics(context.Background())
	if runner.calls != calls {
		t.Fatal("a topic already in today's copy was proposed again")
	}
}

// The daily build gets the named topics as candidates (rule 32), with no apps,
// so an excluded app never drops them.
func TestAddedTopicsAreDailyBuildCandidates(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	if _, err := app.topicsSvc().update(UserTopicUpdate{Action: "add", Label: "Kitchen remodel"}); err != nil {
		t.Fatal(err)
	}
	got := app.addedTopicCandidates()
	if len(got) != 1 || got[0].Name != "Kitchen remodel" || len(got[0].Apps) != 0 || got[0].LastSeenAt == "" {
		t.Fatalf("candidates = %+v", got)
	}
}

// The hub's guard for rule 32: a topic the person added today is still there
// after tomorrow's build, first, with a proposal of its own.
func TestAnAddedTopicKeepsItsPlaceInTheNextDaysBuild(t *testing.T) {
	app, runner, clock := dailyTopicsTestApp(t)
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.topicsSvc().update(UserTopicUpdate{Action: "add", Label: "Kitchen remodel"}); err != nil {
		t.Fatal(err)
	}
	added := app.topicsSvc().store.load().Added[0]
	runner.response = `{"` + added.ID + `":{"title":"Price the cabinets","prompt":"Compare three cabinet quotes."},` +
		`"t1":{"title":"Draft the harbor launch checklist","prompt":"Write it."},"t2":{"title":"Summarize the tide research","prompt":"Summarize it."}}`
	*clock = clock.Add(24 * time.Hour)
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := app.topicsDailyStore()
	if err != nil {
		t.Fatal(err)
	}
	saved, ok := store.Load()
	if !ok || len(saved.Topics) == 0 || saved.Topics[0].ID != added.ID || !saved.Topics[0].Named || saved.Topics[0].Proposal == nil ||
		saved.Topics[0].Proposal.Title != "Price the cabinets" {
		t.Fatalf("the next day's copy lost the named topic: %+v", saved.Topics)
	}
}
