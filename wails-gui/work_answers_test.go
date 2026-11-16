package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/topicday"
	"wails-gui/internal/workonit"
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
	t.Cleanup(func() { dailyTopicTaskStart = started; answersLedgers.Delete(app) })
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

// A phone that reads Your work is remembered, so the computer pushes it only
// for its three kinds; a phone that never reads it keeps every push.
func TestMobileWorkReaderIsRemembered(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	bridge := &mobileBridge{app: app}
	device := mobileDeviceRecord{}
	device.DeviceID = "11111111-2222-4333-8444-555555555555"
	device.Scopes = []string{mobileScopeBriefRead}
	if bridge.workReaders()[device.DeviceID] {
		t.Fatal("a phone counted as a reader before reading")
	}
	bridge.serveWorkList(httptest.NewRecorder(), device)
	bridge.serveWorkList(httptest.NewRecorder(), device)
	readers := bridge.workReaders()
	if !readers[device.DeviceID] || len(readers) != 1 {
		t.Fatalf("readers = %v", readers)
	}
}

// The hand-off sheet's answer: exactly the work and the What it gets items the
// person kept go, removing a fixed item or naming an unknown agent is refused,
// and both are kept in the ledger (phone-agent v1.0.0 rule 3).
func TestWorkAnswerHandOffSheetFields(t *testing.T) {
	item := WorkItem{Gets: []WorkGets{{ID: "g1", Label: "The Q4 notes", Removable: true}, {ID: "g2", Label: "The meeting", Removable: false}}}
	text, err := workHandOffText("Plan the outline.", item, []string{"g1"})
	if err != nil || text != "Plan the outline.\n\nWhat it gets:\n- The meeting" {
		t.Fatalf("text = %q %v", text, err)
	}
	if _, err := workHandOffText("x", item, []string{"g2"}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("removing a fixed item = %v", err)
	}
	if _, err := workHandOffText("x", item, []string{"nope"}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("removing an unknown item = %v", err)
	}
	app, handoffs := workAnswersTestApp(t)
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionYes, AgentID: "someone-else"}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("an unknown agent = %v", err)
	}
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionNo, AgentID: "codex"}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("an agent on a No = %v", err)
	}
	got, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionYes, AgentID: "codex"})
	if err != nil || got.State != workStateWithAgent {
		t.Fatalf("Yes with an agent = %+v %v", got, err)
	}
	if entry, err := handoffs.Get(got.Ref.TaskID); err != nil || entry.Body != "Prepare." {
		t.Fatalf("what went = %+v %v", entry, err)
	}
	if standing := app.answersLedger().current()[workTestPrepID]; standing.AgentID != "codex" {
		t.Fatalf("the ledger lost the agent: %+v", standing)
	}
	list, _ := app.GetWorkList()
	for _, item := range list.Items {
		if item.Yes.Does == "agent" && item.Yes.Kind == "" {
			t.Fatalf("%s: offered work has no kind for the agent choice", item.ID)
		}
	}
}

// With B4, a Yes goes to the person's agent: a connected one through its
// inbox, the ones on this computer as a local task, kept in the hand-offs;
// an agent elsewhere that hasn't joined can't take it from here.
func TestWorkAnswerHandsOffToThePersonsAgent(t *testing.T) {
	app, handoffs := workAnswersTestApp(t)
	got, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionYes, AgentID: "codex"})
	if err != nil || got.State != workStateWithAgent || got.Agent == nil || got.Agent.Name != "Codex" {
		t.Fatalf("Yes to Codex = %+v %v", got, err)
	}
	if _, err := handoffs.Get(got.Ref.TaskID); err != nil {
		t.Fatalf("Codex's local task: %v", err)
	}
	kept, _ := app.GetWorkOnItHandoffs()
	if len(kept) != 1 || kept[0].Method != workonit.MethodLocalTask || kept[0].SourceKind != workonit.SourceWorkItem ||
		kept[0].SourceID != workTestPrepID || kept[0].AgentID != "codex" || kept[0].Surface != workSurfaceComputer {
		t.Fatalf("hand-offs = %+v", kept)
	}
	// Claude (an agent elsewhere) hasn't joined: nothing goes, nothing is kept.
	plan := "brief:2026-11-14-morning:offer:o2"
	if _, err := app.AnswerWork(plan, WorkAnswerInput{Action: workActionYes, AgentID: "claude"}); !errors.Is(err, errWorkAgentAway) {
		t.Fatalf("Yes to Claude, not joined = %v", err)
	}
	if _, standing := app.answersLedger().current()[plan]; standing {
		t.Fatal("an answer that sent nothing was kept")
	}
	if !workTopicIsCode([]string{"Visual Studio Code"}) || workTopicIsCode([]string{"Mail", "Slack"}) {
		t.Fatal("the code rule")
	}
}

// The hand-off sheet's two steps (phone-map §2, item 3): the first tap records
// the Yes and sends nothing; Send hands it over, with the sheet's final
// choices. Closing the sheet leaves "Yes · not sent", and that Yes can be
// undone; a sent one can't.
func TestWorkAnswerYesThenSend(t *testing.T) {
	app, handoffs := workAnswersTestApp(t)
	hold := false
	item, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionYes, Send: &hold})
	if err != nil || item.State != workStateAnswered || item.Answer == nil || !item.Answer.NotSent || item.Answer.Line != "Prep for Lena: yes · not sent" {
		t.Fatalf("Yes, not sent = %+v %v", item, err)
	}
	if entries, _ := handoffs.List(); len(entries) != 0 {
		t.Fatalf("a Yes not sent started work: %+v", entries)
	}
	if stats := judgmentStatsAt(app.answersLedger().entries(), 1, time.Now()); stats.Days[0].Total.Accepted != 1 {
		t.Fatalf("the Yes is the judgment: %+v", stats.Days[0].Total)
	}
	sent, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionSend, AgentID: "codex"})
	if err != nil || sent.State != workStateWithAgent || sent.Agent == nil || sent.Agent.Name != "Codex" || sent.Ref.TaskID == "" {
		t.Fatalf("Send = %+v %v", sent, err)
	}
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionSend}); !errors.Is(err, errWorkItemChanged) {
		t.Fatalf("a second Send = %v", err)
	}
	if _, err := app.UndoWork(workTestPrepID); !errors.Is(err, errWorkUndoClosed) {
		t.Fatalf("Undo after Send = %v", err)
	}
	if page, _ := app.GetAnswers(50, ""); len(page.Answers) != 1 || page.Answers[0].Action != workActionYes {
		t.Fatalf("Your data shows the Yes once, not the send: %+v", page.Answers)
	}
	// A Yes kept for the sheet can be taken back before Send.
	plan := "brief:2026-11-14-morning:offer:o2"
	if _, err := app.AnswerWork(plan, WorkAnswerInput{Action: workActionYes, Send: &hold}); err != nil {
		t.Fatal(err)
	}
	if reopened, err := app.UndoWork(plan); err != nil || reopened.State != workStateOpen {
		t.Fatalf("Undo before Send = %+v %v", reopened, err)
	}
	if _, err := app.AnswerWork(plan, WorkAnswerInput{Action: workActionNo, Send: &hold}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("send:false on a No = %v", err)
	}
}

// Review findings on #1150: a Yes that went to an agent's inbox can't be
// undone (the agent keeps the work), a topic's Yes follows the same agent rule
// as the brief's, and a hand-off made from the computer's Work on it sheet
// counts as the item's Send.
func TestWorkAnswerHandOffRulesAfterReview(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	// An inbox hand-off closes Undo.
	if _, err := app.recordAnswer(AnswerRecord{ItemID: workTestPrepID, Kind: answerKindWork, Action: workActionYes, HandoffID: "rec-inbox",
		Surface: workSurfaceComputer, UndoUntil: time.Now().Add(time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.UndoWork(workTestPrepID); !errors.Is(err, errWorkUndoClosed) {
		t.Fatalf("Undo after an inbox hand-off = %v, want closed", err)
	}

	// The person's everything-else agent is Claude, which hasn't joined: a
	// topic's Yes sends nothing and leaves the proposal as it was.
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetYourAgent(workonit.KindElse, "claude"); err != nil {
		t.Fatal(err)
	}
	view, _ := app.GetDailyTopics()
	tide := view.Topics[1]
	topicID := workTopicProposalID(tide.ID, tide.Proposal.ID)
	if _, err := app.AnswerWork(topicID, WorkAnswerInput{Action: workActionYes}); !errors.Is(err, errWorkAgentAway) {
		t.Fatalf("a topic's Yes for Claude, not joined = %v", err)
	}
	if after, _ := app.GetDailyTopics(); after.Topics[1].Proposal.State != topicday.StateProposed {
		t.Fatalf("the topic moved without a hand-off: %q", after.Topics[1].Proposal.State)
	}

	// The computer's Work on it sheet handed it to Claude (a deep link): Send
	// with that record marks it sent, once, and nothing else goes.
	hold := false
	if _, err := app.AnswerWork(topicID, WorkAnswerInput{Action: workActionYes, Send: &hold}); err != nil {
		t.Fatal(err)
	}
	app.keepWorkOnItHandoff(workonit.Handoff{ID: "rec-link", SourceKind: workonit.SourceWorkItem, SourceID: topicID, Kind: workonit.KindElse,
		AgentID: "claude", Method: workonit.MethodDeepLink, Text: "x", SentAt: time.Now().UnixMilli(), Surface: workSurfaceComputer})
	if _, err := app.AnswerWork(topicID, WorkAnswerInput{Action: workActionSend, RecordID: "rec-other"}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("Send with an unknown record = %v", err)
	}
	sent, err := app.AnswerWork(topicID, WorkAnswerInput{Action: workActionSend, RecordID: "rec-link"})
	if err != nil || sent.State != workStateWithAgent {
		t.Fatalf("Send by the Work on it record = %+v %v", sent, err)
	}
	if after, _ := app.GetDailyTopics(); after.Topics[1].Proposal.State != topicday.StateRunning || after.Topics[1].Proposal.TaskID != "" {
		t.Fatalf("the topic after the hand-off = %+v", after.Topics[1].Proposal)
	}
	if _, err := app.UndoWork(topicID); !errors.Is(err, errWorkUndoClosed) {
		t.Fatalf("Undo after the hand-off = %v", err)
	}
}

// The hub's guard for #1150: a Yes on agent work with send:false opens the
// sheet and nothing leaves. No task starts, no hand-off is kept, the topic
// stays proposed, on the computer and from the phone, until the person
// confirms with Send.
func TestWorkAnswerSendFalseSendsNothingUntilTheSheetConfirms(t *testing.T) {
	app, handoffs := workAnswersTestApp(t)
	started := 0
	dailyTopicTaskStart = func(*App, string) error { started++; return nil }
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	view, _ := app.GetDailyTopics()
	harbor := view.Topics[0]
	topicID := workTopicProposalID(harbor.ID, harbor.Proposal.ID)
	nothingLeft := func(when string) {
		t.Helper()
		if entries, _ := handoffs.List(); len(entries) != 0 {
			t.Fatalf("%s: a task started: %+v", when, entries)
		}
		if kept, _ := app.GetWorkOnItHandoffs(); len(kept) != 0 {
			t.Fatalf("%s: a hand-off was kept: %+v", when, kept)
		}
		if started != 0 {
			t.Fatalf("%s: the topic's task started", when)
		}
		if after, _ := app.GetDailyTopics(); after.Topics[0].Proposal.State != topicday.StateProposed {
			t.Fatalf("%s: the topic moved: %q", when, after.Topics[0].Proposal.State)
		}
	}

	hold := false
	if got, err := app.AnswerWork(topicID, WorkAnswerInput{Action: workActionYes, AgentID: "codex", Send: &hold}); err != nil || got.Answer == nil || !got.Answer.NotSent {
		t.Fatalf("the computer's Yes, not sent = %+v %v", got, err)
	}
	nothingLeft("the computer's Yes")

	bridge := &mobileBridge{app: app, ctx: ctx}
	device := mobileDeviceRecord{}
	device.DeviceID = "11111111-2222-4333-8444-555555555555"
	device.Scopes = []string{mobileScopeBriefRead, mobileScopeBriefFeedback}
	post := func(id string, body map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/v1/work/"+id+"/answer", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		bridge.serveWorkAnswer(w, r, device, id)
		return w
	}
	if got := post(workTestPrepID, map[string]any{"idempotencyKey": "p1", "action": "edit", "before": "Prepare.", "after": "Prepare, briefly.",
		"send": false, "surface": "phone"}); got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte(`"notSent":true`)) {
		t.Fatalf("the phone's Edit, not sent = %d %s", got.Code, got.Body.String())
	}
	if _, err := app.GetWorkList(); err != nil {
		t.Fatal(err)
	}
	nothingLeft("the phone's Edit and a rebuild")

	// Send is the confirmation: now, and only now, the work goes.
	if got := post(workTestPrepID, map[string]any{"idempotencyKey": "p2", "action": "send", "agentId": "codex", "surface": "phone"}); got.Code != http.StatusOK {
		t.Fatalf("the phone's Send = %d %s", got.Code, got.Body.String())
	}
	entries, _ := handoffs.List()
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Body, "Prepare, briefly.") {
		t.Fatalf("what went after Send = %+v", entries)
	}
}
