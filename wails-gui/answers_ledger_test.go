package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
