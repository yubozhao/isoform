package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/topicday"
)

// The replay counts each answer against the ones before it and prints counts
// only: no topic name, title or id ever reaches the output.
func TestReplayCountsOnlyAndAppliesTheRules(t *testing.T) {
	data := t.TempDir()
	store := topicday.NewStore(filepath.Join(data, "topics-daily"))
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	add := func(f topicday.Feedback) {
		t.Helper()
		f.TopicName, f.ProposalTitle = "Secret Harbor", "Draft the secret plan"
		if err := store.AppendFeedback(f); err != nil {
			t.Fatal(err)
		}
	}
	day := func(n int) int64 { return base.Add(time.Duration(n) * 24 * time.Hour).UnixMilli() }
	add(topicday.Feedback{TopicID: "t1", ProposalID: "p1", Verdict: topicday.VerdictNo, At: day(0)})
	add(topicday.Feedback{TopicID: "t1", ProposalID: "p2", Verdict: topicday.VerdictNo, At: day(1)})
	// Rested by "declined": two Nos in a row the day before.
	add(topicday.Feedback{TopicID: "t1", ProposalID: "p3", Verdict: topicday.VerdictYes, At: day(2)})
	add(topicday.Feedback{TopicID: "t2", ProposalID: "p4", Verdict: topicday.VerdictYes, At: day(0)})
	// The ledger says p5's No was not now, and p6's No was undone.
	add(topicday.Feedback{TopicID: "t3", ProposalID: "p5", Verdict: topicday.VerdictNo, At: day(0)})
	add(topicday.Feedback{TopicID: "t3", ProposalID: "p7", Verdict: topicday.VerdictNo, At: day(1)})
	add(topicday.Feedback{TopicID: "t4", ProposalID: "p6", Verdict: topicday.VerdictNo, At: day(0)})
	if err := os.MkdirAll(filepath.Join(data, "answers"), 0o700); err != nil {
		t.Fatal(err)
	}
	var ledger bytes.Buffer
	for _, r := range []ledgerRecord{
		{EntryID: "e1", ItemID: "topic:t3:p5", Source: "topic", Action: "no", Reason: topicday.ReasonNotNow},
		{EntryID: "e2", ItemID: "topic:t4:p6", Source: "topic", Action: "no"},
		{EntryID: "e3", ItemID: "topic:t4:p6", Source: "topic", Action: "undo", Undoes: "e2"},
	} {
		raw, _ := json.Marshal(r)
		ledger.Write(append(raw, '\n'))
	}
	if err := os.WriteFile(filepath.Join(data, "answers", "ledger.jsonl"), ledger.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := replay(data, base.Add(10*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c.Answered != 6 || c.Yes != 2 || c.No != 4 || c.Reasons[topicday.ReasonNotNow] != 1 || c.Reasons["none"] != 3 {
		t.Fatalf("counts = %+v", c)
	}
	// p3 rests (declined), p7 rests (not now a day after it).
	if c.Rested[topicday.RestDeclined] != 1 || c.Rested[topicday.RestNotNow] != 1 || c.AfterAnswered != 4 || c.AfterYes != 1 {
		t.Fatalf("replay = %+v", c)
	}
	var out bytes.Buffer
	write(&out, c)
	for _, secret := range []string{"Secret", "secret", "t1", "p1", data} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("the output carries %q:\n%s", secret, out.String())
		}
	}
	if !strings.Contains(out.String(), "accept_before=33%") || !strings.Contains(out.String(), "accept_after=25%") {
		t.Fatalf("output:\n%s", out.String())
	}
	if empty, err := replay(t.TempDir(), time.Now()); err != nil || empty.Answered != 0 {
		t.Fatalf("an empty folder = %+v %v", empty, err)
	}
}
