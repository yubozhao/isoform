package main

import (
	"errors"
	"testing"
	"time"
)

// An agent's request to another is an item in Your work: Yes or No only, it
// counts to answer, the asking agent reads the answer, and after a day it
// ends by itself (phone-agent v1.2.0 rules 1 and 2).
func TestAnAgentsRequestIsAYesOrNoInYourWork(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	if _, err := app.createAgentRequest("codex", "codex", "", "Can I?", "", time.Time{}); !errors.Is(err, errAgentRequestInput) {
		t.Fatalf("an agent asking itself = %v", err)
	}
	request, err := app.createAgentRequest("codex", "claude", "rec-1", "Codex asks Claude to read the test plan.", "The plan's three open questions.", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	id := workRequestID(request.ID)
	list, err := app.buildWorkList(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item := workTestItem(t, list, id)
	if item.Kind != workKindRequest || item.State != workStateOpen || item.Request == nil || item.Request.From.Name != "Codex" ||
		item.Request.To.AgentID != "claude" || item.Request.Moves == "" || item.ExpiresAt == "" || item.Ref.HandoffID != "rec-1" || len(item.Reasons) != 0 {
		t.Fatalf("request item = %+v", item)
	}
	if list.ToAnswer < 1 {
		t.Fatalf("a request does not count to answer: %d", list.ToAnswer)
	}
	for _, refused := range []WorkAnswerInput{
		{Action: workActionEdit, After: "x"}, {Action: workActionNotMine}, {Action: workActionNo, Reason: "not_now"},
	} {
		if _, err := app.AnswerWork(id, refused); !errors.Is(err, errWorkAnswerInvalid) {
			t.Fatalf("%+v on a request = %v", refused, err)
		}
	}
	if answer, _ := app.agentRequestAnswer(request.ID); answer != "" {
		t.Fatalf("an unanswered request reads %q", answer)
	}
	got, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionYes})
	if err != nil || got.State != workStateAnswered || got.Answer == nil || got.Answer.Line != "Codex's request to Claude: allowed" || got.Answer.UndoUntil != "" {
		t.Fatalf("Yes = %+v %v", got, err)
	}
	if answer, at := app.agentRequestAnswer(request.ID); answer != agentRequestYes || at.IsZero() {
		t.Fatalf("the agent reads %q at %v", answer, at)
	}
	if _, err := app.UndoWork(id); !errors.Is(err, errWorkUndoClosed) {
		t.Fatalf("Undo of a request = %v", err)
	}
}

func TestAnUnansweredRequestRunsOutAfterADay(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	request, err := app.createAgentRequest("codex", "muse", "", "Codex asks Muse for the draft.", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(agentRequestWait + time.Minute)
	list, err := app.buildWorkList(t.Context(), later)
	if err != nil {
		t.Fatal(err)
	}
	item := workTestItem(t, list, workRequestID(request.ID))
	if item.State != workStateAnswered || item.Answer == nil || item.Answer.Action != agentRequestRanOut ||
		item.Answer.Line != "Codex's request to Muse ran out · nothing moved" {
		t.Fatalf("ran out = %+v", item)
	}
	if answer, _ := app.agentRequestAnswer(request.ID); answer != "" {
		// Read now, before the day passed, it still waits.
		t.Fatalf("answer before the day passed = %q", answer)
	}
}
