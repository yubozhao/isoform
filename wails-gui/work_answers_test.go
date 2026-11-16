package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"wails-gui/internal/handoff"
	"wails-gui/internal/topicday"
)

func workAnswersTestApp(t *testing.T) (*App, *handoff.Store) {
	t.Helper()
	app, _, _ := dailyTopicsTestApp(t)
	handoffs, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.handoffStore = handoffs
	app.mobileBriefSvc = &workTestBriefSource{mobileTestBriefSource{brief: workTestBrief()}}
	started := dailyTopicTaskStart
	dailyTopicTaskStart = func(*App, string) error { return nil }
	t.Cleanup(func() { dailyTopicTaskStart = started; workAnswerStores.Delete(app) })
	return app, handoffs
}

const workTestPrepID = "brief:2026-11-14-morning:offer:o1"

// A Yes on the brief's offered work hands it to the agent as a task; when the
// task finishes the item comes back to be judged, and Useful closes it
// (phone-agent v1.0.0; phone-map rule 9).
func TestWorkAnswerYesHandsTheOfferToTheAgentAndItComesBack(t *testing.T) {
	app, handoffs := workAnswersTestApp(t)
	item, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionYes})
	if err != nil {
		t.Fatal(err)
	}
	if item.State != workStateWithAgent || item.Ref.TaskID == "" || item.Answer == nil || item.Answer.Surface != workSurfaceComputer ||
		item.Answer.Line != "Prep for Lena: with your agent" {
		t.Fatalf("after Yes = %+v (answer %+v)", item, item.Answer)
	}
	entry, err := handoffs.Get(item.Ref.TaskID)
	if err != nil || entry.Body != "Prepare." || entry.Thread == nil || entry.Thread.Origin.Kind != workBriefTaskOrigin {
		t.Fatalf("the task = %+v %v", entry, err)
	}
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionNo}); !errors.Is(err, errWorkItemChanged) {
		t.Fatalf("a second answer = %v, want item changed", err)
	}
	if _, err := app.UndoWork(workTestPrepID); !errors.Is(err, errWorkUndoClosed) {
		t.Fatalf("undoing a hand-off = %v, want closed", err)
	}

	for _, status := range []handoff.Status{handoff.StatusInProgress, handoff.StatusCompleted} {
		if _, err := handoffs.UpdateStatus(entry.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	list, err := app.buildWorkList(context.Background(), mustParseWorkTime(t, "2026-11-14T09:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	back := workTestItem(t, list, workTestPrepID)
	if back.State != workStateBack || back.Kind != workKindResult || list.BackFromAgents != 1 {
		t.Fatalf("a finished task's item = %+v (back %d)", back, list.BackFromAgents)
	}
	judged, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionUseful})
	if err != nil || judged.State != workStateAnswered || judged.Answer.Action != workActionUseful || judged.Ref.TaskID == "" {
		t.Fatalf("Useful = %+v %v", judged, err)
	}
	reopened, err := app.UndoWork(workTestPrepID)
	if err != nil || reopened.State != workStateBack {
		t.Fatalf("undoing Useful = %+v %v, want back", reopened, err)
	}
}

// No carries an optional reason from the item's own list; Undo puts the item
// back, once (phone-map rules 7 and 8).
func TestWorkAnswerNoWithAReasonAndUndo(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionNo, Reason: "was_true_once"}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("a claim's reason on offered work = %v, want invalid", err)
	}
	item, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionNo, Reason: "not_now"})
	if err != nil || item.State != workStateAnswered || item.Answer.Reason != "not_now" || item.Answer.Line != "Prep for Lena: not now" ||
		item.Answer.UndoUntil == "" {
		t.Fatalf("No (not now) = %+v %v", item, err)
	}
	list, _ := app.GetWorkList()
	if list.ToAnswer != 1 {
		t.Fatalf("an answered item still counts: %d to answer", list.ToAnswer)
	}
	item, err = app.UndoWork(workTestPrepID)
	if err != nil || item.State != workStateOpen || item.Answer != nil {
		t.Fatalf("Undo = %+v %v", item, err)
	}
	if _, err := app.UndoWork(workTestPrepID); !errors.Is(err, errWorkItemChanged) {
		t.Fatalf("a second Undo = %v, want item changed", err)
	}
}

// A topic's proposal is answered through the topic store, so Topics and Your
// work agree; its Undo puts the proposal back.
func TestWorkAnswerOnATopicGoesThroughTheTopicStore(t *testing.T) {
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
	item, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionNo, Reason: "not_useful"})
	if err != nil || item.State != workStateAnswered || item.Answer.Action != workActionNo || item.Answer.Reason != "not_useful" ||
		item.Answer.Surface != workSurfaceComputer {
		t.Fatalf("No = %+v %v", item, err)
	}
	after, _ := app.GetDailyTopics()
	if after.Topics[1].Proposal.State != topicday.StateDeclined {
		t.Fatalf("the topic store says %q", after.Topics[1].Proposal.State)
	}
	if _, err := app.UndoWork(id); err != nil {
		t.Fatal(err)
	}
	after, _ = app.GetDailyTopics()
	if after.Topics[1].Proposal.State != topicday.StateProposed {
		t.Fatalf("after Undo the topic store says %q", after.Topics[1].Proposal.State)
	}
	if _, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionEdit, After: "Do it my way"}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("Edit on a topic proposal = %v, want invalid", err)
	}
	// Not mine takes the topic out of Your work, as it does on Topics; the
	// answer still comes back with its line.
	gone, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionNotMine})
	if err != nil || gone.State != workStateAnswered || gone.Answer == nil || gone.Answer.Line != "Summarize this week's tide research: not yours" {
		t.Fatalf("Not mine = %+v %v", gone, err)
	}
	if list, _ := app.GetWorkList(); len(list.Items) != 3 {
		t.Fatalf("a topic that isn't the person's is still listed: %d items", len(list.Items))
	}
}

// The phone's answers go through the same function, receipted by key, with
// surface "phone"; an answer older than the last one applied to the item is
// refused with the item as it is.
func TestWorkAnswerFromThePhoneIsReceiptedAndInOrder(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	bridge := &mobileBridge{app: app, ctx: context.Background()}
	device := mobileDeviceRecord{}
	device.DeviceID = "11111111-2222-4333-8444-555555555555"
	device.Scopes = []string{mobileScopeBriefRead, mobileScopeBriefFeedback}
	post := func(body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/v1/work/"+workTestPrepID+"/answer", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		bridge.serveWorkAnswer(w, r, device, workTestPrepID)
		return w
	}
	first := post(map[string]any{"idempotencyKey": "k1", "action": "no", "reason": "not_now", "surface": "phone", "seq": 2})
	if first.Code != http.StatusOK {
		t.Fatalf("first answer = %d %s", first.Code, first.Body.String())
	}
	var answered struct {
		Data struct {
			Item WorkItem `json:"item"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &answered); err != nil || answered.Data.Item.Answer == nil ||
		answered.Data.Item.Answer.Surface != workSurfacePhone || answered.Data.Item.State != workStateAnswered {
		t.Fatalf("the phone's answer = %s", first.Body.String())
	}
	if again := post(map[string]any{"idempotencyKey": "k1", "action": "no", "reason": "not_now", "surface": "phone", "seq": 2}); again.Code != http.StatusOK || again.Body.String() != first.Body.String() {
		t.Fatalf("a retried key did not replay the first outcome: %d %s", again.Code, again.Body.String())
	}
	if stale := post(map[string]any{"idempotencyKey": "k0", "action": "undo", "surface": "phone", "seq": 1}); stale.Code != http.StatusConflict {
		t.Fatalf("an older answer = %d %s, want 409", stale.Code, stale.Body.String())
	}
	if undo := post(map[string]any{"idempotencyKey": "k3", "action": "undo", "surface": "phone", "seq": 3}); undo.Code != http.StatusOK {
		t.Fatalf("a newer Undo = %d %s", undo.Code, undo.Body.String())
	}
	if gone := httptest.NewRecorder(); true {
		raw, _ := json.Marshal(map[string]any{"idempotencyKey": "k4", "action": "yes"})
		r := httptest.NewRequest(http.MethodPost, "/v1/work/brief:x:offer:none/answer", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		bridge.serveWorkAnswer(gone, r, device, "brief:x:offer:none")
		if gone.Code != http.StatusNotFound {
			t.Fatalf("an unknown item = %d, want 404", gone.Code)
		}
	}
}
