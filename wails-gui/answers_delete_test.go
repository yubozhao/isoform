package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wails-gui/internal/privacy"
	"wails-gui/internal/topicday"
)

// Delete your answers: the ledger, the answered measures and the topic answers
// go; each item keeps its state without an Undo; Not mine stays; a privacy
// withdrawal for the answers goes out (computer-data v1.0.0 rule 4).
func TestDeleteAnswersRemovesEverythingLearnedAndKeepsEachItemsState(t *testing.T) {
	judgmentsDir, _ := judgmentsTestDir(t)
	app, _ := workAnswersTestApp(t)
	if _, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionNo, Reason: "not_now"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.recordAnswer(AnswerRecord{ItemID: "belief:a", Kind: answerKindBelief, Action: workActionYes, Surface: workSurfaceComputer}); err != nil {
		t.Fatal(err)
	}
	noteProposalAnswered(judgmentTopic, "p1", true)
	noteBriefActed()
	store, err := app.topicsDailyStore()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []topicday.Feedback{
		{TopicID: "t1", TopicName: "Halcyon", ProposalID: "p1", ProposalTitle: "Draft the plan", Verdict: topicday.VerdictNo, At: 1},
		{TopicID: "t2", TopicName: "Warriors", ProposalID: "p2", ProposalTitle: "Last three games", Verdict: topicday.VerdictNotMine, At: 2},
	} {
		if err := store.AppendFeedback(f); err != nil {
			t.Fatal(err)
		}
	}
	var withdrawn []privacyWithdrawal
	t.Cleanup(subscribePrivacyWithdrawals(func(w privacyWithdrawal) { withdrawn = append(withdrawn, w) }))

	if err := app.DeleteAnswers(); err != nil {
		t.Fatal(err)
	}

	page, err := app.GetAnswers(0, "")
	if err != nil || len(page.Answers) != 0 {
		t.Fatalf("answers after Delete = %+v %v", page.Answers, err)
	}
	stats, err := app.GetJudgmentStats(7)
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range stats.Days {
		if day.Total.Answered+day.Total.Accepted+day.Total.Declined+day.Total.Shown != 0 {
			t.Fatalf("judgment stats after Delete = %+v", stats)
		}
	}
	day := readJudgmentDay(judgmentsDir, judgmentsNow().Format("2006-01-02"))
	if c := day.Kinds[judgmentTopic]; c == nil || c.Answered != 0 || c.Accepted != 0 || day.BriefActed != 0 {
		t.Fatalf("daily measures kept answers: %+v %d", c, day.BriefActed)
	}
	feedback, err := store.Feedback()
	if err != nil {
		t.Fatal(err)
	}
	if len(feedback) != 1 || feedback[0].Verdict != topicday.VerdictNotMine || feedback[0].ProposalTitle != "" {
		t.Fatalf("topic answers after Delete = %+v, want only Not mine, without its proposal", feedback)
	}
	if !topicday.NotMine(feedback)["t2"] {
		t.Fatal("Not mine must keep its topic away")
	}

	// The item stays answered, and its Undo is gone.
	list, err := app.GetWorkList()
	if err != nil {
		t.Fatal(err)
	}
	item, ok := workFind(list, workTestPrepID)
	if !ok || item.State != workStateAnswered || item.Answer == nil || item.Answer.UndoUntil != "" {
		t.Fatalf("item after Delete = %+v", item)
	}
	if _, err := app.UndoWork(workTestPrepID); err == nil {
		if again, _ := app.GetWorkList(); func() bool { i, _ := workFind(again, workTestPrepID); return i.State == workStateOpen }() {
			t.Fatal("an Undo after Delete reopened the item")
		}
	}

	// Nothing the person wrote is left on disk.
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(app.answersLedger().path), answersSettledFileName))
	if strings.Contains(string(raw), "not_now") {
		t.Fatalf("the kept state holds the answer's reason: %s", raw)
	}
	found := false
	for _, w := range withdrawn {
		if w.Scope.Kind == privacy.Source && w.Scope.SourceKind == answersSourceKind {
			found = true
		}
	}
	if !found {
		t.Fatalf("no answers withdrawal was published: %+v", withdrawn)
	}

	// Delete runs again safely.
	if err := app.DeleteAnswers(); err != nil {
		t.Fatal(err)
	}
	if list, _ := app.GetWorkList(); func() bool { i, _ := workFind(list, workTestPrepID); return i.State != workStateAnswered }() {
		t.Fatal("a second Delete lost the item's state")
	}
}
