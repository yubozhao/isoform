package topicday

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

func ids() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("p%d", n) }
}

func candidates() []Candidate {
	return []Candidate{
		{ID: "t1", Kind: KindProject, Name: "Harbor launch", LastSeenAt: "2026-11-14T08:00:00Z", Apps: []string{"Code"}},
		{ID: "t2", Kind: KindSubject, Name: "Tide tables", LastSeenAt: "2026-11-13T08:00:00Z"},
		{ID: "t3", Kind: KindProject, Name: "Private ledger", LastSeenAt: "2026-11-12T08:00:00Z", Apps: []string{"Ledger"}},
		{ID: "t4", Kind: KindProject, Name: "Old plan", LastSeenAt: "2026-11-11T08:00:00Z"},
		{ID: "t5", Kind: "unknown", Name: "Odd", LastSeenAt: "2026-11-10T08:00:00Z"},
	}
}

func TestBuildHonorsThePersonsChoices(t *testing.T) {
	choices := Choices{Forgotten: map[string]bool{"old plan": true}, ExcludedApps: []string{"ledger"}}
	propose := func(_ context.Context, topics []Topic, _ []Feedback) (map[string]ProposalDraft, error) {
		out := map[string]ProposalDraft{}
		for _, t := range topics {
			out[t.ID] = ProposalDraft{Title: "Next for " + t.Name, Prompt: "Do the next step on " + t.Name}
		}
		return out, nil
	}
	c, err := Build(context.Background(), "2026-11-14", testNow, candidates(), choices, nil, nil, propose, ids())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Topics) != 2 || c.Topics[0].ID != "t1" || c.Topics[1].ID != "t2" {
		t.Fatalf("topics = %+v; want t1, t2 (forgotten, excluded-app and unknown-kind topics left out)", c.Topics)
	}
	for _, topic := range c.Topics {
		if topic.Proposal == nil || topic.Proposal.State != StateProposed || topic.Proposal.ID == "" {
			t.Fatalf("topic %s has no proposal: %+v", topic.ID, topic.Proposal)
		}
	}
	if _, err := Build(context.Background(), "2026-11-14", testNow, candidates(), Choices{ClosedEyes: true}, nil, nil, propose, ids()); !errors.Is(err, ErrClosedEyes) {
		t.Fatalf("Closed Eyes built a copy: %v", err)
	}
}

func TestBuildKeepsARunningProposalAndSurvivesAProposerError(t *testing.T) {
	previous := &Copy{Topics: []Topic{{ID: "t1", Proposal: &Proposal{ID: "old", Title: "Running", Prompt: "x", State: StateRunning, TaskID: "hf_1"}}}}
	calls := 0
	failing := func(context.Context, []Topic, []Feedback) (map[string]ProposalDraft, error) {
		calls++
		return nil, errors.New("model unavailable")
	}
	c, err := Build(context.Background(), "2026-11-14", testNow, candidates()[:2], Choices{}, previous, nil, failing, ids())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("the proposer ran %d times; want once for the whole build", calls)
	}
	if p := c.Topics[0].Proposal; p == nil || p.ID != "old" || p.TaskID != "hf_1" {
		t.Fatalf("a running proposal was lost: %+v", p)
	}
	if c.Topics[1].Proposal != nil {
		t.Fatal("a proposer error made a proposal")
	}
}

func TestFilterIsTheSameForAnythingLeaving(t *testing.T) {
	topics := []Topic{{ID: "a", Name: "Harbor"}, {ID: "b", Name: "Ledger", Apps: []string{"Ledger"}}, {ID: "c", Name: "Mine?"}}
	got := Filter(topics, Choices{Forgotten: NotMine([]Feedback{{TopicID: "c", TopicName: "Mine?", Verdict: VerdictNotMine}}), ExcludedApps: []string{"Ledger"}})
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("filtered = %+v", got)
	}
	if len(Filter(topics, Choices{ClosedEyes: true})) != 0 {
		t.Fatal("Closed Eyes let a topic leave")
	}
}

func TestStoreSavesAnswersAndTaskResults(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, ok := s.Load(); ok {
		t.Fatal("an empty store loaded a copy")
	}
	c := Copy{Version: CopyVersion, Day: "2026-11-14", Topics: []Topic{{ID: "t1", Name: "Harbor", Proposal: &Proposal{ID: "p1", Title: "Draft", Prompt: "x", State: StateProposed}}}}
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateProposal("t1", "yesterday", func(*Topic, *Proposal) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an old proposal ID changed today's proposal: %v", err)
	}
	if _, err := s.UpdateProposal("t1", "p1", func(_ *Topic, p *Proposal) error { p.State, p.TaskID = StateRunning, "hf_9"; return nil }); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.UpdateByTask("hf_9", func(p *Proposal) bool { p.State, p.Result = StateDone, "Drafted"; return true }); !ok || err != nil {
		t.Fatalf("task result not saved: %v %v", ok, err)
	}
	got, _ := s.Load()
	if p := got.Topics[0].Proposal; p.State != StateDone || p.Result != "Drafted" {
		t.Fatalf("proposal = %+v", p)
	}
	for _, v := range []string{VerdictNo, VerdictNotMine} {
		if err := s.AppendFeedback(Feedback{TopicID: "t1", TopicName: "Harbor", ProposalID: "p1", Verdict: v, At: 1}); err != nil {
			t.Fatal(err)
		}
	}
	feedback, err := s.Feedback()
	if err != nil || len(feedback) != 2 || !NotMine(feedback)["harbor"] {
		t.Fatalf("feedback = %+v %v", feedback, err)
	}
}
