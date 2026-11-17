package main

import (
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/internal/workonit"
)

// Finished work on a phone that reads Your work says "Back from {agent}"
// with its one sentence (phone-agent v1.2.0 rule 10). The sentence passes the
// same privacy filter as everything that leaves the computer, and nothing
// from a Closed Eyes period goes in it.
func TestFinishedWorkSaysBackFromItsAgentWithItsSentence(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	now := time.Now()
	app.keepWorkOnItHandoff(workonit.Handoff{ID: "task-1", AgentID: "codex", Method: workonit.MethodLocalTask, SentAt: now.Add(-time.Hour).UnixMilli()})
	entry := handoff.Entry{ID: "task-1", Title: "The test plan", CreatedAt: now.Add(-time.Hour).UnixMilli(), Status: handoff.StatusCompleted,
		Thread: &handoff.Thread{Outcome: &handoff.Outcome{Params: map[string]string{"summary": "Wrote the test plan in docs/plan.md. It covers the three flows.\nNext: review."}}}}
	params := app.workFinishedParams(entry, now)
	if params["title"] != "The test plan" || params[engagementAgentParam] != "Codex" || params[engagementSentenceParam] != "Wrote the test plan in docs/plan.md." {
		t.Fatalf("params = %v", params)
	}
	// No hand-off record: the person's agent.
	if other := app.workFinishedParams(handoff.Entry{ID: "task-2", Title: "Other"}, now); other[engagementAgentParam] != "your agent" || other[engagementSentenceParam] != "" {
		t.Fatalf("without a record = %v", other)
	}

	// A pause during the run keeps the sentence out; so does one still open.
	app.saveObservationPauses([]observationPause{{Start: now.Add(-30 * time.Minute).UnixMilli(), End: now.Add(-20 * time.Minute).UnixMilli()}})
	if got := app.workFinishedParams(entry, now); got[engagementSentenceParam] != "" || got[engagementAgentParam] != "Codex" {
		t.Fatalf("a run with a pause in it = %v", got)
	}
	app.saveObservationPauses([]observationPause{{Start: now.Add(-3 * time.Hour).UnixMilli(), End: now.Add(-2 * time.Hour).UnixMilli()}})
	if got := app.workFinishedParams(entry, now); got[engagementSentenceParam] == "" {
		t.Fatal("a pause before the run kept the sentence out")
	}
	app.saveObservationPauses([]observationPause{{Start: now.Add(-time.Minute).UnixMilli()}})
	if got := app.workFinishedParams(entry, now); got[engagementSentenceParam] != "" {
		t.Fatalf("an open pause = %v", got)
	}

	// The phone's words: rendered from the params, filtered like every text.
	rig := newEngagementTestRig(t)
	spec, _ := engagement.Spec(engagement.KindWorkFinished)
	text := rig.svc.render(spec, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "task-1",
		Params: map[string]string{"title": "The test plan", engagementAgentParam: "Codex", engagementSentenceParam: "Sent the plan to person@example.com."}})
	if text.PhoneTitle != "Back from Codex" || text.PhoneBody == "" || strings.Contains(text.PhoneBody, "person@example.com") || text.PushBody != "Work finished." {
		t.Fatalf("the phone's words = %+v", text)
	}
	if bare := rig.svc.render(spec, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "task-1", Params: map[string]string{"title": "x"}}); bare.PhoneTitle != "" || bare.PhoneBody != "" {
		t.Fatalf("an older moment without an agent = %+v", bare)
	}
}

func TestEngagementFirstSentence(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "",
		"Done.":                          "Done.",
		"Wrote it. Then checked it.":     "Wrote it.",
		"Version 1.2 is out! Read more.": "Version 1.2 is out!",
		"First line\nsecond line.":       "First line",
		"写好了。然后检查了。":                     "写好了。",
		strings.Repeat("a", 300):         strings.Repeat("a", engagementSentenceMax) + "...",
	} {
		if got := engagementFirstSentence(in); got != want {
			t.Errorf("engagementFirstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}
