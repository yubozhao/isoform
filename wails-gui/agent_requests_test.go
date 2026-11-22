package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"wails-gui/internal/handoff"
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
	if err != nil || got.State != workStateOpen || got.Request.SendError != agentRequestUnavailable || got.Answer == nil || got.Answer.Action != agentRequestYes || got.Answer.UndoUntil != "" {
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

// A local coding agent returns the outcome of the one pinned task.
func TestAgentRequestLocalTaskAnswer(t *testing.T) {
	app, started := newAgentHereTestApp(t, true)
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	if _, err := app.ConnectAgentHere("codex"); err != nil {
		t.Fatal(err)
	}
	target, name, err := app.agentRequestTarget("CoDeX", "asker")
	if err != nil || target != "codex" || name != "Codex" {
		t.Fatalf("target: %q %q %v", target, name, err)
	}
	r, err := app.createAgentRequest("asker", target, "private-parent", "Atlas wants Codex to review the plan", "review the plan", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.SendAgentRequestAgain(r.ID); !errors.Is(err, errAgentRequestNotFailed) {
		t.Fatalf("unanswered retry: %v", err)
	}
	if _, err := app.DisconnectAgentHere(target); err != nil {
		t.Fatal(err)
	}
	itemID := workRequestID(r.ID)
	item, err := app.AnswerWork(itemID, WorkAnswerInput{Action: workActionYes})
	if err != nil || item.State != workStateOpen || item.Request.SendError != agentRequestUnavailable {
		t.Fatalf("disconnected Yes: %+v %v", item, err)
	}
	failed, _ := app.getAgentRequest(r.ID)
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": r.ID}
	answer := func() string {
		t.Helper()
		got := app.getAgentAnswer(t.Context(), r.From, req)
		if got.IsError {
			t.Fatalf("get_answer: %+v", got)
		}
		return got.Content[0].(mcp.TextContent).Text
	}
	if got := answer(); !strings.Contains(got, "couldn't be reached") {
		t.Fatal(got)
	}
	if _, err := app.ConnectAgentHere(target); err != nil {
		t.Fatal(err)
	}
	agentHereSignedIn = func(backend string) (bool, error) {
		if backend != "codex" {
			t.Fatalf("sign-in checked for %q", backend)
		}
		return false, nil
	}
	if err := app.SendAgentRequestAgain(r.ID); !errors.Is(err, errAgentHereSignedOut) || len(*started) != 0 {
		t.Fatalf("signed-out retry: started %v, err %v", *started, err)
	}
	checkReason := func(want string) {
		t.Helper()
		item := workTestItem(t, WorkList{Items: app.workRequestItems(time.Now())}, itemID)
		if item.Request.SendReason != want || item.State != workStateOpen {
			t.Fatalf("request = %+v, state %s, want %s", item.Request, item.State, want)
		}
	}
	checkReason("signed_out")
	agentHereSignedIn = func(string) (bool, error) { return true, nil }
	startTask := agentHereTaskStart
	agentHereTaskStart = func(*App, string) error { return errors.New("runner unavailable") }
	if err := app.SendAgentRequestAgain(r.ID); !errors.Is(err, errAgentHereNotStarted) {
		t.Fatal(err)
	}
	checkReason("not_running")
	agentHereTaskStart = startTask
	app.closedEyes.enabled.Store(true)
	if err := app.SendAgentRequestAgain(r.ID); !errors.Is(err, errWorkOnItClosedEyes) || len(*started) != 0 {
		t.Fatalf("Closed Eyes retry: started %v, err %v", *started, err)
	}
	checkReason("closed_eyes")
	app.closedEyes.enabled.Store(false)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- app.SendAgentRequestAgain(itemID)
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first == nil {
		first, second = second, first
	}
	if !errors.Is(first, errAgentRequestNotFailed) || second != nil {
		t.Fatalf("double click: %v, %v", first, second)
	}
	r, _ = app.getAgentRequest(r.ID)
	if len(*started) != 1 || (*started)[0] != r.TargetTaskID || r.SendError != "" || r.SendReason != "" || r.AnsweredAt != failed.AnsweredAt {
		t.Fatalf("started %v, request %+v", *started, r)
	}
	handoffs, err := app.GetWorkOnItHandoffs()
	if err != nil || len(handoffs) != 1 || handoffs[0].ID != r.TargetHandoffID || handoffs[0].Text != r.Moves || handoffs[0].Title != r.Sentence {
		t.Fatalf("retry hand-offs: %+v, %v", handoffs, err)
	}
	list, err := app.buildWorkList(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item = workTestItem(t, list, itemID)
	if item.Request.SendError != "" || item.State != workStateAnswered || !strings.Contains(answer(), "Codex has it") {
		t.Fatalf("after retry: %+v, %s", item, answer())
	}
	// A restart between dispatch and its receipt must not offer a duplicate.
	if err := updateAgentRequest(r.ID, time.Now(), func(saved *AgentRequest) error {
		saved.SendError = agentRequestUnconfirmed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.SendAgentRequestAgain(r.ID); !errors.Is(err, errAgentRequestNotFailed) {
		t.Fatalf("already delivered reservation: %v", err)
	}
	task, err := app.handoffStore.Get(r.TargetTaskID)
	if err != nil || task.Body != r.Moves || task.Title != r.Sentence || task.Backend != "codex" {
		t.Fatalf("task: %+v %v", task, err)
	}
	if _, err := app.handoffStore.UpdateStatus(task.ID, handoff.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	_, err = app.handoffStore.MutateIfStatus(task.ID, nil, func(e *handoff.Entry) {
		e.Status = handoff.StatusCompleted
		e.Thread.Outcome = &handoff.Outcome{Params: map[string]string{"summary": "Reviewed."}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := answer(); !strings.Contains(got, "Reviewed.") {
		t.Fatalf("local answer: %s", got)
	}
}

func TestAgentRequestRetryUntilRequestExpiry(t *testing.T) {
	app, started := newAgentHereTestApp(t, true)
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	if _, err := app.ConnectAgentHere("codex"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond)
	r, err := app.createAgentRequest("asker", "codex", "", "Review the plan", "the approved text", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Use a stored request with a longer window than the default day.
	end := now.Add(26 * time.Hour)
	if err := updateAgentRequest(r.ID, now, func(saved *AgentRequest) error {
		saved.ExpiresAt = end.UnixMilli()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	agentHereSignedIn = func(string) (bool, error) { return false, nil }
	itemID := workRequestID(r.ID)
	initial, err := app.AnswerWork(itemID, WorkAnswerInput{Action: workActionYes})
	if err != nil || initial.Request.SendReason != "signed_out" {
		t.Fatalf("signed-out Yes = %+v, %v", initial, err)
	}
	saved, _ := app.getAgentRequest(r.ID)
	retryAt := time.UnixMilli(saved.AnsweredAt).Add(25 * time.Hour)
	later := func(at time.Time) WorkList {
		t.Helper()
		list, err := app.buildWorkList(t.Context(), at)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	item := workTestItem(t, later(retryAt), itemID)
	if item.State != workStateOpen || item.Request.SendReason != "signed_out" {
		t.Fatalf("open until expiry: %+v", item)
	}
	for _, at := range []time.Time{end, end.Add(time.Hour)} {
		item := workTestItem(t, later(at), itemID)
		if item.State != workStateAnswered || item.Answer == nil || item.Answer.Action != agentRequestRanOut ||
			item.Answer.At != item.ExpiresAt || item.Answer.Line != item.Request.From.Name+"'s request to Codex ran out · nothing moved" ||
			item.Request.SendReason != "" || item.Request.SendError != "" {
			t.Fatalf("expired: %+v, answer %+v", item, item.Answer)
		}
		if err := app.sendAgentRequest(r.ID, workSurfaceComputer, at, true); !errors.Is(err, errAgentRequestNotFailed) {
			t.Fatalf("retry at %v: %v", at, err)
		}
	}
	if _, found := workFind(later(end.Add(24*time.Hour+time.Millisecond)), itemID); found {
		t.Fatal("kept beyond 24 hours after expiry")
	}
	agentHereSignedIn = func(string) (bool, error) { return true, nil }
	if err := app.sendAgentRequest(r.ID, workSurfaceComputer, retryAt, true); err != nil || len(*started) != 1 {
		t.Fatalf("retry 25 hours after Yes: started %v, err %v", *started, err)
	}
}
