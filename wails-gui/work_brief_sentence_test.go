package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	briefcore "wails-gui/internal/brief"
)

// notRightTestBrief is the work test brief with two sentences, each resting on
// its own source, and the offer o1 under the first.
func notRightTestBrief() *MobileBriefV1 {
	brief := workTestBrief()
	brief.Narrative = []MobileBriefPara{
		{Text: "You met Lena about the plan.", Refs: []string{"e1"}},
		{Text: "The harbor checklist shipped.", Refs: []string{"e2"}},
	}
	brief.Evidence = []MobileBriefRef{
		{Ref: "e1", SourceKind: mobileBriefCalendarSource, SourceID: "cal-lena", Label: "Lena"},
		{Ref: "e2", SourceKind: mobileBriefRecapSource, SourceID: "act-harbor", Label: "Harbor"},
	}
	brief.Offers[0].Refs = []string{"e1"}
	return brief
}

func notRightTestApp(t *testing.T) *App {
	t.Helper()
	app, _ := workAnswersTestApp(t)
	app.mobileBriefSvc = &workTestBriefSource{mobileTestBriefSource{brief: notRightTestBrief()}}
	return app
}

func TestBriefSentenceIDsRoundTrip(t *testing.T) {
	id := workBriefSentenceID("2026-11-14-morning", 3)
	if id != "brief:2026-11-14-morning:p3" {
		t.Fatalf("id = %s", id)
	}
	if briefID, index, ok := workParseBriefSentenceID(id); !ok || briefID != "2026-11-14-morning" || index != 3 {
		t.Fatalf("parse = %s %d %v", briefID, index, ok)
	}
	for _, bad := range []string{"brief:b:offer:o1", "brief:b:p", "brief:b:p-1", "brief:b:p01", "topic:t:p1", "brief::p1"} {
		if _, _, ok := workParseBriefSentenceID(bad); ok {
			t.Errorf("%q parsed as a sentence", bad)
		}
	}
}

// Not right on a sentence (computer-brief v1.0.0 rules 7 and 8): a No with
// its reason or words, kept in Your answers, undone like any answer.
func TestNotRightOnABriefSentenceIsANoInYourAnswers(t *testing.T) {
	app := notRightTestApp(t)
	id := workBriefSentenceID("2026-11-14-morning", 0)
	if _, err := app.AnswerWork(workBriefSentenceID("2026-11-14-morning", 9), WorkAnswerInput{Action: workActionNo}); !errors.Is(err, errWorkItemGone) {
		t.Fatalf("a paragraph the brief doesn't have = %v", err)
	}
	if _, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionYes}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("a Yes on a sentence = %v, want invalid", err)
	}
	if _, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionNo, Reason: "not_now"}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("an offer's reason on a sentence = %v, want invalid", err)
	}
	item, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionNo, Reason: "wrong_person", After: "It was Sam, not Lena."})
	if err != nil || item.ID != id || item.Kind != workKindClaim || item.State != workStateAnswered || item.Noticed != "You met Lena about the plan." ||
		item.Answer == nil || item.Answer.Reason != "wrong_person" || item.Answer.Edited == nil || item.Answer.Edited.After != "It was Sam, not Lena." ||
		item.Answer.Surface != workSurfaceComputer || item.Answer.UndoUntil == "" {
		t.Fatalf("Not right = %+v %v", item, err)
	}
	if _, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionNo}); !errors.Is(err, errWorkItemChanged) {
		t.Fatalf("a second Not right = %v, want item changed", err)
	}
	page, err := app.GetAnswers(10, "")
	if err != nil || len(page.Answers) != 1 || page.Answers[0].ItemID != id || page.Answers[0].Source != workSourceBrief ||
		page.Answers[0].Reason != "wrong_person" || page.Answers[0].Title != "You met Lena about the plan." {
		t.Fatalf("Your answers = %+v %v", page.Answers, err)
	}
	// The sentence is never a row in Your work.
	list, _ := app.GetWorkList()
	for _, listed := range list.Items {
		if listed.ID == id {
			t.Fatalf("the sentence became a row: %+v", listed)
		}
	}
	item, err = app.UndoWork(id)
	if err != nil || item.State != workStateOpen || item.Answer != nil {
		t.Fatalf("Undo = %+v %v", item, err)
	}
	if got := app.workNotRightSentences(time.Time{}); len(got) != 0 {
		t.Fatalf("an undone Not right still stands: %v", got)
	}
}

// What was made from the sentence is marked for the person to check, and
// the next briefs leave out what the sentence rested on.
func TestNotRightMarksItsWorkAndKeepsItsSourcesOutOfTheNextBrief(t *testing.T) {
	app := notRightTestApp(t)
	if _, err := app.AnswerWork(workBriefSentenceID("2026-11-14-morning", 0), WorkAnswerInput{Action: workActionNo, Reason: "didnt_happen"}); err != nil {
		t.Fatal(err)
	}
	list, err := app.GetWorkList()
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, item := range list.Items {
		for _, part := range item.First {
			if part.Key == workFromNotRightKey {
				marked++
				if item.ID != workTestPrepID || part.Text != workFromNotRightText {
					t.Fatalf("marked the wrong item: %+v", item)
				}
			}
		}
	}
	if marked != 1 {
		t.Fatalf("items marked = %d, want the offer under the sentence: %+v", marked, list.Items)
	}

	sources := app.briefNotRightSources(time.Now())
	if !sources[mobileBriefSourceKey(mobileBriefCalendarSource, "cal-lena")] || sources[mobileBriefSourceKey(mobileBriefRecapSource, "act-harbor")] {
		t.Fatalf("not-right sources = %v", sources)
	}
	if later := app.briefNotRightSources(time.Now().Add(8 * 24 * time.Hour)); len(later) != 0 {
		t.Fatalf("a Not right from over a week ago still holds: %v", later)
	}

	in := newWriterTestInput()
	in.Meetings[0].SourceID = "cal-lena"
	in.Meetings[0].Title = "Lena about the plan"
	in.NotRight = sources
	m, _ := newBriefMaterial(in, buildMobileBriefV1(in), briefPartDone, "")
	for _, item := range m.Items {
		if strings.Contains(item.Title, "Lena") {
			t.Fatalf("the writer got what a Not right sentence rested on: %+v", item)
		}
	}
	in.NotRight = nil
	if m, _ := newBriefMaterial(in, buildMobileBriefV1(in), briefPartDone, ""); !strings.Contains(materialTitles(m.Items), "Lena") {
		t.Fatalf("without the Not right the meeting is missing: %v", materialTitles(m.Items))
	}
}

func materialTitles(items []briefcore.MaterialItem) string {
	titles := make([]string, 0, len(items))
	for _, item := range items {
		titles = append(titles, item.Title)
	}
	return strings.Join(titles, " | ")
}
