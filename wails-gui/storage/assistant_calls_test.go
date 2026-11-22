package storage

import (
	"context"
	"testing"
	"time"
)

func TestAssistantCallRecordJoinsCountsAndForgets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// A stretch of the day: two screen sessions and three WeChat windows.
	must(s.RecordAssistantCall(ctx, AssistantCall{Purpose: AssistantCallDay, At: 1000, SpanStart: 900, SpanEnd: 1000,
		Sources: []AssistantCallSource{{Kind: "screen", Ref: "sess-1"}, {Kind: "screen", Ref: "sess-2"}, {Kind: "wechat", Count: 3}}}))
	// A brief with four mail threads.
	must(s.RecordAssistantCall(ctx, AssistantCall{Purpose: AssistantCallBrief, Ref: "brief-1", At: 2000,
		Sources: []AssistantCallSource{{Kind: "mail", Ref: "m1"}, {Kind: "mail", Ref: "m2"}, {Kind: "mail", Ref: "m3"}, {Kind: "mail", Ref: "m4"}, {Kind: "screen", Ref: "sess-2"}}}))
	// An answer that read twice is one answer.
	must(s.RecordAssistantCall(ctx, AssistantCall{Purpose: AssistantCallAnswer, Ref: "item-1", At: 3000,
		Sources: []AssistantCallSource{{Kind: "memory", Ref: "mem-1"}}}))
	must(s.RecordAssistantCall(ctx, AssistantCall{Purpose: AssistantCallAnswer, Ref: "item-1", At: 3100,
		Sources: []AssistantCallSource{{Kind: "memory", Ref: "mem-1"}, {Kind: "people", Ref: "p-1"}}}))
	must(s.RecordAssistantCall(ctx, AssistantCall{Purpose: AssistantCallProposal, Ref: "prop-1", At: 4000,
		Sources: []AssistantCallSource{{Kind: "topic_names"}}}))

	counts, err := s.CountAssistantCalls(ctx, 0)
	must(err)
	if counts[AssistantCallDay] != 1 || counts[AssistantCallBrief] != 1 || counts[AssistantCallAnswer] != 1 || counts[AssistantCallProposal] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	calls, err := s.ListAssistantCalls(ctx, 0)
	must(err)
	if len(calls) != 4 || calls[0].Purpose != AssistantCallProposal {
		t.Fatalf("calls = %+v", calls)
	}
	answer := calls[1]
	if answer.Times != 2 || answer.At != 3100 || len(answer.Sources) != 2 || answer.Sources[0] != (AssistantCallSource{Kind: "memory", Ref: "mem-1", Count: 2}) {
		t.Fatalf("answer = %+v", answer)
	}
	if day := calls[3]; len(day.Sources) != 3 || day.Sources[2] != (AssistantCallSource{Kind: "wechat", Count: 3}) || day.SpanStart != 900 {
		t.Fatalf("day = %+v", day)
	}
	if since, _ := s.CountAssistantCalls(ctx, 2500); since[AssistantCallDay] != 0 || since[AssistantCallAnswer] != 1 {
		t.Fatalf("counts since = %v", since)
	}

	// Forgetting a screen session takes the stretch and the brief written
	// from it; the answer and proposal stay.
	n, err := s.ForgetAssistantCallsForSource(ctx, "screen", "sess-2")
	must(err)
	if n != 2 {
		t.Fatalf("forgot %d entries for sess-2, want 2", n)
	}
	calls, _ = s.ListAssistantCalls(ctx, 0)
	if len(calls) != 2 {
		t.Fatalf("after forgetting a session: %+v", calls)
	}
	var orphans int
	must(s.db.QueryRow(`SELECT COUNT(*) FROM assistant_call_sources WHERE call_id NOT IN (SELECT id FROM assistant_calls)`).Scan(&orphans))
	if orphans != 0 {
		t.Fatalf("%d sources left without their call", orphans)
	}
	if n, _ := s.ForgetAssistantCall(ctx, AssistantCallAnswer, "item-1"); n != 1 {
		t.Fatalf("forgetting the answer removed %d", n)
	}
	if n, _ := s.ForgetAssistantCallsBefore(ctx, 5000); n != 1 {
		t.Fatalf("aging out removed %d", n)
	}
	must(s.RecordAssistantCall(ctx, AssistantCall{Purpose: AssistantCallDay, At: 6000, Sources: []AssistantCallSource{{Kind: "watch", Ref: "rec-1"}}}))
	must(s.ForgetAllAssistantCalls(ctx))
	if calls, _ := s.ListAssistantCalls(ctx, 0); len(calls) != 0 {
		t.Fatalf("Forget everything left %+v", calls)
	}
}

// The record holds names and ids, never text: prose in a ref, an unknown
// kind or purpose is refused.
func TestAssistantCallRecordRefusesText(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for name, call := range map[string]AssistantCall{
		"prose ref":       {Purpose: AssistantCallAnswer, Ref: "what did Priya ask me", At: 1},
		"prose source":    {Purpose: AssistantCallDay, At: 1, Sources: []AssistantCallSource{{Kind: "screen", Ref: "Quarterly numbers for Priya"}}},
		"unknown kind":    {Purpose: AssistantCallDay, At: 1, Sources: []AssistantCallSource{{Kind: "screen text"}}},
		"unknown purpose": {Purpose: "chat", At: 1},
		"no time":         {Purpose: AssistantCallDay},
	} {
		if err := s.RecordAssistantCall(ctx, call); err == nil {
			t.Errorf("%s: kept", name)
		}
	}
	if calls, _ := s.ListAssistantCalls(ctx, 0); len(calls) != 0 {
		t.Fatalf("refused calls were kept: %+v", calls)
	}
}

func TestAssistantCallReadsDuringPendingDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	source := AssistantCallSource{Kind: "screen", Ref: "session-1", Count: 1}
	if err := s.RecordAssistantCall(ctx, AssistantCall{Purpose: AssistantCallDay, At: 1000, Sources: []AssistantCallSource{source}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM assistant_calls"); err != nil {
		t.Fatal(err)
	}

	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	counts, err := s.CountAssistantCalls(readCtx, 0)
	if err != nil || counts[AssistantCallDay] != 1 {
		t.Fatalf("committed assistant call count = %v, err = %v", counts, err)
	}
	calls, err := s.ListAssistantCalls(readCtx, 0)
	if err != nil || len(calls) != 1 || calls[0].Purpose != AssistantCallDay || len(calls[0].Sources) != 1 || calls[0].Sources[0] != source {
		t.Fatalf("committed assistant call and sources = %+v, err = %v", calls, err)
	}
}
