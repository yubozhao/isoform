package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

// A task that finished today without an item of its own comes back to Your
// work as a result, judged Useful or Not useful (phone-map rule 9).
func TestWorkListBringsBackATaskThatFinishedToday(t *testing.T) {
	app, handoffs := workAnswersTestApp(t)
	saved, err := handoffs.Save(handoff.Entry{ID: "hf-plan", Title: "The test plan", Status: handoff.StatusInProgress, Source: handoff.SourceDesktop, Body: "Write it."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handoffs.UpdateStatus(saved.ID, handoff.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	list, err := app.GetWorkList()
	if err != nil {
		t.Fatal(err)
	}
	item := workTestItem(t, list, "result:hf-plan")
	if item.State != workStateBack || item.Kind != workKindResult || item.Source != workSourceAgent || item.Work.Title != "The test plan" ||
		list.BackFromAgents != 1 {
		t.Fatalf("a finished task = %+v (back %d)", item, list.BackFromAgents)
	}
	if _, err := app.AnswerWork(item.ID, WorkAnswerInput{Action: workActionYes}); !errors.Is(err, errWorkAnswerInvalid) && !errors.Is(err, errWorkItemChanged) {
		t.Fatalf("Yes on a result = %v, want refused", err)
	}
	judged, err := app.AnswerWork(item.ID, WorkAnswerInput{Action: workActionNotUseful})
	if err != nil || judged.State != workStateAnswered || judged.Answer.Action != workActionNotUseful {
		t.Fatalf("Not useful = %+v %v", judged, err)
	}
	// A task a Yes on the brief started is that item's result, not a second one.
	offer, err := app.AnswerWork(workTestPrepID, WorkAnswerInput{Action: workActionYes})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []handoff.Status{handoff.StatusInProgress, handoff.StatusCompleted} {
		if _, err := handoffs.UpdateStatus(offer.Ref.TaskID, status); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := app.GetWorkList()
	for _, other := range after.Items {
		if other.ID == "result:"+offer.Ref.TaskID {
			t.Fatalf("the brief's task came back twice: %+v", other)
		}
	}
}

// Prep is answered with Yes (kept with the meeting) or No (not needed); it
// can't be rewritten.
func TestWorkAnswersOnPrep(t *testing.T) {
	item := WorkItem{ID: "prep:m1", Source: workSourcePrep, Work: WorkBody{Title: "Prep for Lena"}}
	app := &App{}
	if _, err := app.dispatchWorkAnswer(t.Context(), item, WorkAnswerInput{Action: workActionEdit, After: "x"}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("Edit on prep = %v", err)
	}
	if task, err := app.dispatchWorkAnswer(t.Context(), item, WorkAnswerInput{Action: workActionNo, Reason: "not_needed"}); err != nil || task != "" {
		t.Fatalf("No on prep = %q %v", task, err)
	}
	if got := workAnswerLine("Prep for Lena", workActionNo, "not_needed", workStateAnswered); got != "Prep for Lena: not needed" {
		t.Fatalf("line = %q", got)
	}
}

// A brief offer sits under the first paragraph that cites its source, and no
// composed text carries a day or a clock (the Today owner's rule: the reader's
// client words times).
func TestWorkItemsPlaceOffersAndKeepTimesOutOfText(t *testing.T) {
	narrative := []MobileBriefPara{{Text: "A.", Refs: []string{"e1"}}, {Text: "B.", Refs: []string{"e2", "e3"}}}
	if got := workOfferParagraph(narrative, []string{"e3"}); got == nil || *got != 1 {
		t.Fatalf("paragraph = %v, want 1", got)
	}
	if got := workOfferParagraph(narrative, []string{"e9"}); got != nil {
		t.Fatalf("an offer citing nothing in the brief got paragraph %d", *got)
	}
	app, _ := workAnswersTestApp(t)
	list, err := app.buildWorkList(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		for _, part := range item.First {
			if (part.Key == "when" || part.Key == "clock") && (part.Text != "" || part.At == "") {
				t.Fatalf("%s: a time part = %+v", item.ID, part)
			}
			for _, word := range []string{"today", "tomorrow", " AM", " PM"} {
				if strings.Contains(part.Text, word) {
					t.Fatalf("%s: %q bakes in a day or clock", item.ID, part.Text)
				}
			}
		}
	}
}

// Day and clock words are filled at read time for the reader's now, so a list
// built on Sunday says "today" on Monday about Monday's meeting.
func TestWorkListWordsDaysForTheReadersNow(t *testing.T) {
	sunday := time.Date(2026, 11, 15, 21, 0, 0, 0, time.Local)
	meeting := time.Date(2026, 11, 16, 10, 0, 0, 0, time.Local)
	list := WorkList{Items: []WorkItem{{ID: "prep:m", First: []WorkTextPart{
		{Text: "For your meeting with Lena", Key: "for_meeting", At: meeting.Format(time.RFC3339)},
		{Key: "when", At: meeting.Format(time.RFC3339)},
		{Key: "clock", At: meeting.Format(time.RFC3339)},
	}}}}
	if got := workListWordedAt(list, sunday).Items[0].First[1].Text; got != "tomorrow" {
		t.Fatalf("on Sunday = %q", got)
	}
	monday := workListWordedAt(list, sunday.Add(12*time.Hour)).Items[0]
	if monday.First[1].Text != "today" || monday.First[2].Text != "10:00 AM" || monday.First[0].Text != "For your meeting with Lena" {
		t.Fatalf("on Monday = %+v", monday.First)
	}
	if list.Items[0].First[1].Text != "" {
		t.Fatal("wording at read time changed the snapshot")
	}
	for at, want := range map[time.Time]string{
		sunday.Add(-24 * time.Hour):    "yesterday",
		sunday.Add(3 * 24 * time.Hour): sunday.Add(3 * 24 * time.Hour).Format("Monday"),
		sunday.Add(9 * 24 * time.Hour): sunday.Add(9 * 24 * time.Hour).Format("Jan 2"),
	} {
		if got := workDayWord(at, sunday); got != want {
			t.Errorf("%v: %q, want %q", at, got, want)
		}
	}
}
