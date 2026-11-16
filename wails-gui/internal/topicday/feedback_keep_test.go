package topicday

import "testing"

func TestKeepFeedbackRewritesTheLog(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, v := range []string{VerdictYes, VerdictNotMine, VerdictNo} {
		if err := s.AppendFeedback(Feedback{TopicID: "t-" + v, Verdict: v, ProposalTitle: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.KeepFeedback(func(f Feedback) (Feedback, bool) {
		f.ProposalTitle = ""
		return f, f.Verdict == VerdictNotMine
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Feedback()
	if err != nil || len(got) != 1 || got[0].TopicID != "t-not_mine" || got[0].ProposalTitle != "" {
		t.Fatalf("feedback = %+v %v", got, err)
	}
	if err := NewStore(t.TempDir()).KeepFeedback(func(f Feedback) (Feedback, bool) { return f, true }); err != nil {
		t.Fatal("an empty log must be fine")
	}
}

func TestForgetAllRemovesTheDailyStore(t *testing.T) {
	s := NewStore(t.TempDir() + "/daily")
	if err := s.AppendFeedback(Feedback{TopicID: "t", Verdict: VerdictNotMine}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetAll(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Feedback(); err != nil || len(got) != 0 {
		t.Fatalf("feedback after ForgetAll = %+v %v", got, err)
	}
	if _, ok := s.Load(); ok {
		t.Fatal("the daily topics must be gone")
	}
	if err := s.ForgetAll(); err != nil {
		t.Fatal("forgetting twice must be fine")
	}
}
