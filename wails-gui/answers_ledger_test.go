package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// The answers ledger is append-only: an Undo is its own entry, nothing is
// rewritten, and what was answered survives a restart (design A37).
func TestAnswersLedgerIsAppendOnlyAndSurvivesARestart(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionNo, Reason: "not_now"}); err != nil {
		t.Fatal(err)
	}
	path := app.answersLedger().path
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.UndoWork(workTestPrepID); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) || bytes.Count(after, []byte("\n")) != 2 {
		t.Fatalf("an Undo rewrote the ledger instead of adding to it:\n%s\n---\n%s", before, after)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the ledger is not private: %v %v", info.Mode(), err)
	}
	// A fresh read (a restart) sees the same answers.
	answersLedgers.Delete(app)
	entries := app.answersLedger().entries()
	if len(entries) != 2 || entries[0].Action != workActionNo || entries[0].Reason != "not_now" || entries[0].Surface != workSurfaceComputer ||
		entries[0].Title != "Prep for Lena" || entries[1].Action != workActionUndo || entries[1].Undoes != entries[0].EntryID {
		t.Fatalf("entries after a restart = %+v", entries)
	}
	if _, standing := app.answersLedger().current()[workTestPrepID]; standing {
		t.Fatal("an undone answer still stands")
	}
	// A torn last line is skipped, never repaired in place.
	if err := os.WriteFile(path, append(after, []byte(`{"v":1,"id":"half`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := len(app.answersLedger().entries()); got != 2 {
		t.Fatalf("a torn line changed the answers: %d", got)
	}
}

// Your data reads every answer newest first, marked when undone, a page at a
// time, on the computer and on the phone.
func TestAnswersPageNewestFirstWithUndone(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	first, _ := app.recordAnswer(AnswerRecord{ItemID: "belief:m1", Kind: answerKindBelief, Title: "Prefers mornings", Action: workActionYes, Surface: workSurfaceComputer})
	if err := app.undoAnswerFor("belief:m1", answerKindBelief, workSurfaceComputer); err != nil {
		t.Fatal(err)
	}
	second, _ := app.recordAnswer(AnswerRecord{ItemID: "portrait", Kind: answerKindPortrait, Action: workActionNo, Note: "Not a runner", Surface: workSurfaceComputer})
	third, _ := app.recordAnswer(AnswerRecord{ItemID: "belief:m2", Kind: answerKindBelief, Action: workActionNo, Surface: workSurfaceComputer})
	page, err := app.GetAnswers(2, "")
	if err != nil || len(page.Answers) != 2 || page.Answers[0].EntryID != third.EntryID || page.Answers[1].EntryID != second.EntryID || page.Next != second.EntryID {
		t.Fatalf("first page = %+v %v", page, err)
	}
	rest, _ := app.GetAnswers(2, page.Next)
	if len(rest.Answers) != 1 || rest.Answers[0].EntryID != first.EntryID || !rest.Answers[0].Undone || rest.Next != "" {
		t.Fatalf("second page = %+v", rest)
	}
	bridge := &mobileBridge{app: app}
	w := httptest.NewRecorder()
	bridge.serveAnswers(w, httptest.NewRequest(http.MethodGet, "/v1/answers?limit=10", nil))
	var response struct {
		Data AnswersPage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || len(response.Data.Answers) != 3 {
		t.Fatalf("GET /answers = %d %s", w.Code, w.Body.String())
	}
	if bad := httptest.NewRecorder(); true {
		bridge.serveAnswers(bad, httptest.NewRequest(http.MethodGet, "/v1/answers?limit=900", nil))
		if bad.Code != http.StatusBadRequest {
			t.Fatalf("a limit over 200 = %d", bad.Code)
		}
	}
}

// That's me and Not quite on a belief, and their Undo, are kept in the ledger
// with the belief's words.
func TestAnswersLedgerKeepsBeliefAnswers(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	app.ledgerBeliefAnswer("m7", "# Mornings\n\n## Memory Points\n\n- Does deep work before noon\n", workActionYes)
	app.ledgerBeliefUndo("m7")
	app.ledgerBeliefAnswer("m7", "# Mornings\n\n## Memory Points\n\n- Does deep work before noon\n", workActionNo)
	entries := app.answersLedger().entries()
	if len(entries) != 3 || entries[0].ItemID != "belief:m7" || entries[0].Kind != answerKindBelief || !strings.Contains(entries[0].Title, "deep work") ||
		entries[1].Undoes != entries[0].EntryID || entries[2].Action != workActionNo {
		t.Fatalf("belief entries = %+v", entries)
	}
	if standing := app.answersLedger().current()["belief:m7"]; standing.Action != workActionNo {
		t.Fatalf("standing = %+v", standing)
	}
}

// Each item is recorded once, the first time it is put in front of the
// person, with its source and surface; shown entries are measures, never
// answers (hub, the thesis's daily measures).
func TestAnswersLedgerRecordsWhatWasShownOnce(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	if _, err := app.GetWorkList(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetWorkList(); err != nil {
		t.Fatal(err)
	}
	bridge := &mobileBridge{app: app}
	device := mobileDeviceRecord{}
	device.DeviceID = "11111111-2222-4333-8444-555555555555"
	device.Scopes = []string{mobileScopeBriefRead}
	bridge.serveWorkList(httptest.NewRecorder(), device)
	shown := 0
	for _, record := range app.answersLedger().entries() {
		if record.Action == answerActionShown {
			shown++
			if record.Source != workSourceBrief || record.Surface != workSurfaceComputer {
				t.Fatalf("a shown entry = %+v", record)
			}
		}
	}
	if shown != 2 {
		t.Fatalf("shown entries = %d, want one per item (2), once", shown)
	}
	if _, standing := app.answersLedger().current()[workTestPrepID]; standing {
		t.Fatal("being shown counted as an answer")
	}
	if page, _ := app.GetAnswers(50, ""); len(page.Answers) != 0 {
		t.Fatalf("Your data listed a shown entry: %+v", page.Answers)
	}
	// Internal reads (Today's version, a notice's item) record nothing.
	answersLedgers.Delete(app)
	if _, err := app.workListNow(); err != nil {
		t.Fatal(err)
	}
	if got := len(app.answersLedger().entries()); got != 2 {
		t.Fatalf("an internal read recorded something: %d entries", got)
	}
}

// Forget deletes one answer for good, with its Undo; the rest of the ledger
// stays as it was, and the item can be answered again.
func TestForgetAnswerDeletesItForGood(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	keep, _ := app.recordAnswer(AnswerRecord{ItemID: "belief:keep", Kind: answerKindBelief, Action: workActionYes, Surface: workSurfaceComputer})
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionNo, Reason: "not_now"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.UndoWork(workTestPrepID); err != nil {
		t.Fatal(err)
	}
	var no AnswerRecord
	for _, record := range app.answersLedger().entries() {
		if record.ItemID == workTestPrepID && record.Action == workActionNo {
			no = record
		}
	}
	if err := app.ForgetAnswer(no.EntryID); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(app.answersLedger().path)
	if strings.Contains(string(raw), no.EntryID) || strings.Contains(string(raw), "not_now") {
		t.Fatalf("the forgotten answer or its Undo is still on disk:\n%s", raw)
	}
	if !strings.Contains(string(raw), keep.EntryID) {
		t.Fatal("forgetting one answer lost another")
	}
	if err := app.ForgetAnswer(no.EntryID); !errors.Is(err, errAnswerNotFound) {
		t.Fatalf("forgetting twice = %v", err)
	}
	if item, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionYes}); err != nil || item.State != workStateWithAgent {
		t.Fatalf("answering again after Forget = %+v %v", item, err)
	}
	bridge := &mobileBridge{app: app}
	w := httptest.NewRecorder()
	bridge.serveForgetAnswer(w, keep.EntryID)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /answers = %d %s", w.Code, w.Body.String())
	}
}

// The daily measures count what was shown, answered and accepted, per source,
// today first; an undone answer counts as undone, not answered.
func TestJudgmentStatsCountsByDayAndSource(t *testing.T) {
	now := time.Date(2026, 11, 15, 18, 0, 0, 0, time.Local)
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	entries := []AnswerRecord{
		{EntryID: "s1", ItemID: "brief:b:offer:1", Source: "brief", Action: answerActionShown, At: at(-time.Hour)},
		{EntryID: "s2", ItemID: "brief:b:offer:2", Source: "brief", Action: answerActionShown, At: at(-time.Hour)},
		{EntryID: "s3", ItemID: "topic:t:p", Source: "topic", Action: answerActionShown, At: at(-time.Hour)},
		{EntryID: "a1", ItemID: "brief:b:offer:1", Source: "brief", Action: workActionYes, At: at(-50 * time.Minute)},
		{EntryID: "a2", ItemID: "brief:b:offer:2", Source: "brief", Action: workActionNo, At: at(-40 * time.Minute)},
		{EntryID: "u2", ItemID: "brief:b:offer:2", Action: workActionUndo, Undoes: "a2", At: at(-39 * time.Minute)},
		{EntryID: "a3", ItemID: "topic:t:p", Source: "topic", Action: workActionNotMine, At: at(-24 * time.Hour)},
		{EntryID: "a4", ItemID: "belief:m", Source: answerSourceAboutYou, Action: workActionYes, At: at(-30 * time.Minute)},
	}
	stats := judgmentStatsAt(entries, 2, now)
	if len(stats.Days) != 2 || stats.Days[0].Day != "2026-11-15" || stats.Days[1].Day != "2026-11-14" {
		t.Fatalf("days = %+v", stats.Days)
	}
	today := stats.Days[0]
	brief := today.BySource["brief"]
	if brief.Shown != 2 || brief.Answered != 1 || brief.Accepted != 1 || brief.Undone != 1 || brief.Declined != 0 {
		t.Fatalf("brief today = %+v", brief)
	}
	if today.Total.Shown != 3 || today.Total.Answered != 2 || today.BySource[answerSourceAboutYou].Accepted != 1 {
		t.Fatalf("today = %+v", today)
	}
	if got := stats.Days[1].BySource["topic"]; got.NotMine != 1 || got.Answered != 1 {
		t.Fatalf("yesterday's topic = %+v", got)
	}
}

// Your answers offers Undo only on a Your work answer that still stands and
// can still be taken back until the next brief (redesign-activity v1.0.0
// rule 10): never on About you's answers, an undone one, one that sent work
// on, an agent's request, or after the brief closed it.
func TestAnswersPageMarksWhatCanBeUndone(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionNo, Reason: "not_now"}); err != nil {
		t.Fatal(err)
	}
	app.recordAnswer(AnswerRecord{ItemID: "belief:m1", Kind: answerKindBelief, Action: workActionYes, Surface: workSurfaceComputer})
	page, _ := app.GetAnswers(10, "")
	if len(page.Answers) != 2 || page.Answers[0].CanUndo || !page.Answers[1].CanUndo {
		t.Fatalf("before Undo = %+v", page.Answers)
	}
	if _, err := app.UndoWork(workTestPrepID); err != nil {
		t.Fatal(err)
	}
	page, _ = app.GetAnswers(10, "")
	if len(page.Answers) != 2 || page.Answers[1].CanUndo || !page.Answers[1].Undone {
		t.Fatalf("after Undo = %+v", page.Answers)
	}

	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	open := AnswerRecord{EntryID: "e1", ItemID: "w1", Kind: answerKindWork, Action: workActionYes, UndoUntil: now.Add(time.Hour).Format(time.RFC3339)}
	standing := map[string]AnswerRecord{"w1": open}
	if !answerCanUndo(open, standing, now) {
		t.Fatal("an open answer can't be undone")
	}
	closed := open
	closed.UndoUntil = now.Add(-time.Minute).Format(time.RFC3339)
	sent := open
	sent.TaskID = "t1"
	older := open
	older.EntryID = "e0"
	request := open
	request.ItemID = workRequestID("r1")
	for name, record := range map[string]AnswerRecord{"closed": closed, "sent": sent, "older": older, "request": request} {
		if answerCanUndo(record, map[string]AnswerRecord{record.ItemID: map[bool]AnswerRecord{true: open, false: record}[name == "older"]}, now) {
			t.Fatalf("%s answer offered Undo", name)
		}
	}
}
