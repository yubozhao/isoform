package topicday

import (
	"context"
	"testing"
	"time"
)

func no(topic, proposal, reason string, at time.Time) Feedback {
	return Feedback{TopicID: topic, ProposalID: proposal, Verdict: VerdictNo, Reason: reason, At: at.UnixMilli()}
}

// A topic's proposal rests when the person's own answers say it would be
// turned down again; the topic itself still shows.
func TestRestFollowsThePersonsReasons(t *testing.T) {
	day := 24 * time.Hour
	topic := func(seen time.Time) Topic {
		out := Topic{ID: "t1", Name: "Harbor launch"}
		if !seen.IsZero() {
			out.LastSeenAt = seen.Format(time.RFC3339)
		}
		return out
	}
	yesterday := testNow.Add(-day)
	for _, c := range []struct {
		name     string
		seen     time.Time
		feedback []Feedback
		want     string
	}{
		{"no answers", yesterday, nil, ""},
		{"already done, nothing new since", testNow.Add(-2 * day), []Feedback{no("t1", "p1", ReasonAlreadyDone, yesterday)}, RestAlreadyDone},
		{"already done, the topic moved since", testNow.Add(-time.Hour), []Feedback{no("t1", "p1", ReasonAlreadyDone, yesterday)}, ""},
		{"already done, last seen unknown, lately", time.Time{}, []Feedback{no("t1", "p1", ReasonAlreadyDone, yesterday)}, RestAlreadyDone},
		{"already done, last seen unknown, long ago", time.Time{}, []Feedback{no("t1", "p1", ReasonAlreadyDone, testNow.Add(-8*day))}, ""},
		{"not now, two days ago", yesterday, []Feedback{no("t1", "p1", ReasonNotNow, testNow.Add(-2*day))}, RestNotNow},
		{"not now, four days ago", yesterday, []Feedback{no("t1", "p1", ReasonNotNow, testNow.Add(-4*day))}, ""},
		{"one plain no", yesterday, []Feedback{no("t1", "p1", "", yesterday)}, ""},
		{"two nos in a row", yesterday, []Feedback{no("t1", "p1", ReasonNotUseful, testNow.Add(-2*day)), no("t1", "p2", ReasonMyself, yesterday)}, RestDeclined},
		{"two nos, a yes between", yesterday, []Feedback{no("t1", "p1", "", testNow.Add(-3*day)),
			{TopicID: "t1", ProposalID: "p2", Verdict: VerdictYes, At: testNow.Add(-2 * day).UnixMilli()}, no("t1", "p3", "", yesterday)}, ""},
		{"two nos, over a week ago", yesterday, []Feedback{no("t1", "p1", "", testNow.Add(-9*day)), no("t1", "p2", "", testNow.Add(-8*day))}, ""},
		{"another topic's nos", yesterday, []Feedback{no("t2", "p1", "", yesterday), no("t2", "p2", "", yesterday)}, ""},
		{"stale", testNow.Add(-15 * day), nil, RestStale},
		{"seen two weeks ago to the day", testNow.Add(-14 * day), nil, ""},
	} {
		if got := Rest(topic(c.seen), c.feedback, testNow); got != c.want {
			t.Errorf("%s: Rest = %q, want %q", c.name, got, c.want)
		}
	}
}

// An undo takes back the answer before it on its proposal, "not mine"
// included: the topic is the person's again.
func TestStandingDropsUndoneAnswers(t *testing.T) {
	at := testNow.Add(-time.Hour)
	feedback := []Feedback{
		no("t1", "p1", "", at),
		{TopicID: "t2", TopicName: "Tide tables", ProposalID: "p2", Verdict: VerdictNotMine, At: at.UnixMilli()},
		no("t1", "p3", ReasonNotUseful, at),
		{TopicID: "t1", ProposalID: "p3", Verdict: VerdictUndo, At: testNow.UnixMilli()},
		{TopicID: "t2", ProposalID: "p2", Verdict: VerdictUndo, At: testNow.UnixMilli()},
	}
	standing := Standing(feedback)
	if len(standing) != 1 || standing[0].ProposalID != "p1" {
		t.Fatalf("standing = %+v", standing)
	}
	if mine := NotMine(feedback); mine["t2"] || mine["tide tables"] {
		t.Fatalf("an undone not mine still hides the topic: %v", mine)
	}
	if Rest(Topic{ID: "t1", LastSeenAt: testNow.Format(time.RFC3339)}, standing, testNow) != "" {
		t.Fatal("an undone no still rests the topic")
	}
}

// The build asks only about topics whose proposal doesn't rest, counts the
// rested ones by why, and hands the proposer the standing feedback.
func TestBuildRestsProposalsTheAnswersRuleOut(t *testing.T) {
	cands := []Candidate{
		{ID: "t1", Kind: KindProject, Name: "Harbor launch", LastSeenAt: "2026-11-14T08:00:00Z"},
		{ID: "t2", Kind: KindSubject, Name: "Tide tables", LastSeenAt: "2026-11-13T08:00:00Z"},
		{ID: "t3", Kind: KindProject, Name: "Old plan", LastSeenAt: "2026-10-20T08:00:00Z"},
	}
	feedback := []Feedback{
		no("t2", "p0", ReasonAlreadyDone, testNow.Add(-2*time.Hour)),
		no("t1", "p9", "", testNow.Add(-time.Hour)),
		{TopicID: "t1", ProposalID: "p9", Verdict: VerdictUndo, At: testNow.UnixMilli()},
	}
	var asked []string
	var given []Feedback
	propose := func(_ context.Context, topics []Topic, fb []Feedback) (map[string]ProposalDraft, error) {
		out := map[string]ProposalDraft{}
		for _, t := range topics {
			asked = append(asked, t.ID)
			out[t.ID] = ProposalDraft{Title: "Next for " + t.Name, Prompt: "Do it"}
		}
		given = fb
		return out, nil
	}
	c, err := Build(context.Background(), "2026-11-14", testNow, cands, Choices{}, nil, feedback, propose, ids())
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "t1" {
		t.Fatalf("asked about %v, want only t1", asked)
	}
	if len(c.Topics) != 3 || c.Rested[RestAlreadyDone] != 1 || c.Rested[RestStale] != 1 || len(c.Rested) != 2 {
		t.Fatalf("topics %d, rested %v", len(c.Topics), c.Rested)
	}
	for _, topic := range c.Topics {
		if (topic.ID == "t1") != (topic.Proposal != nil) {
			t.Fatalf("topic %s proposal = %+v", topic.ID, topic.Proposal)
		}
	}
	if len(given) != 1 || given[0].TopicID != "t2" {
		t.Fatalf("the proposer got %+v, want the standing feedback", given)
	}
	if quiet, _ := Build(context.Background(), "2026-11-14", testNow, cands[:1], Choices{}, nil, nil, propose, ids()); quiet.Rested != nil {
		t.Fatalf("nothing rested, yet %v", quiet.Rested)
	}
}
