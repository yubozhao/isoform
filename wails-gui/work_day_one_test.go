package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Day one (computer-setup v1.0.0 rule 6, computer-today v1.1.0 rule 2): real
// work from the calendar, the mail and what's been seen, before the first
// brief; Yes hands it to the agent, No takes a reason, and it ends with the
// first day.
func TestDayOneOffersRealWorkFromCalendarMailAndWhatWasSeen(t *testing.T) {
	app, handoffs := workAnswersTestApp(t)
	ctx := context.Background()
	now := time.Now()
	if err := app.store.SetUIState(ctx, firstWeekInstallKey, now.Add(-2*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	previous := workDayOneCollect
	t.Cleanup(func() { workDayOneCollect = previous })
	collected := 0
	workDayOneCollect = func(context.Context, *App, time.Time, time.Time) (mobileBriefInput, error) {
		collected++
		return mobileBriefInput{
			Meetings: []mobileBriefCandidate{
				{SourceKind: mobileBriefCalendarSource, SourceID: "cal-lena", Title: "Q4 plan review", Person: "Lena", At: now.Add(3 * time.Hour)},
				{SourceKind: mobileBriefCalendarSource, SourceID: "cal-past", Title: "Standup", At: now.Add(-time.Hour)},
			},
			WaitingOnYou: []mobileBriefCandidate{{SourceKind: "mail_thread", SourceID: "m-1", Title: "The contract", Person: "Sam", Detail: "Asked for your answer by Friday."}},
			Moved:        []mobileBriefCandidate{{SourceKind: mobileBriefRecapSource, SourceID: "act-1", Title: "The harbor checklist", Detail: "Half done."}},
			Noticed:      []mobileBriefCandidate{{SourceKind: "web", SourceID: "w-1", Title: "A web page", EvidenceUnavailable: true}},
		}, nil
	}
	list, err := app.buildWorkList(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	origins := map[string]WorkItem{}
	for _, item := range list.Items {
		if item.Source == workSourceDayOne {
			origins[item.Ref.Origin] = item
		}
	}
	if len(origins) != 3 || list.DayOne != 3 {
		t.Fatalf("day one = %d items (dayOne %d): %+v", len(origins), list.DayOne, origins)
	}
	meeting := origins[workDayOneCalendar]
	if meeting.Work.Title != "Prepare for Q4 plan review" || meeting.First[0].Text != "Day one · from your calendar" || meeting.Yes.Does != "agent" {
		t.Fatalf("calendar item = %+v", meeting)
	}
	if origins[workDayOneMail].Work.Title != "Reply to Sam" || origins[workDayOneSeen].Work.Title != "Pick up The harbor checklist" {
		t.Fatalf("mail and seen = %+v / %+v", origins[workDayOneMail], origins[workDayOneSeen])
	}
	// Read again within the half hour: no new gather.
	if _, err := app.buildWorkList(ctx, now.Add(time.Minute)); err != nil || collected != 1 {
		t.Fatalf("gathered %d times", collected)
	}
	if _, err := app.AnswerWork(meeting.ID, WorkAnswerInput{Action: workActionNotMine}); !errors.Is(err, errWorkAnswerInvalid) {
		t.Fatalf("Not mine on day one = %v", err)
	}
	got, err := app.AnswerWork(meeting.ID, WorkAnswerInput{Action: workActionYes, AgentID: "codex"})
	if err != nil || got.State != workStateWithAgent {
		t.Fatalf("Yes = %+v %v", got, err)
	}
	if entries, _ := handoffs.List(); len(entries) != 1 {
		t.Fatalf("the task didn't start: %+v", entries)
	}
	no, err := app.AnswerWork(origins[workDayOneMail].ID, WorkAnswerInput{Action: workActionNo, Reason: "myself"})
	if err != nil || no.State != workStateAnswered {
		t.Fatalf("No = %+v %v", no, err)
	}
	// The first day ends: day one leaves Your work.
	if later, _ := app.buildWorkList(ctx, now.Add(23*time.Hour)); later.DayOne != 0 {
		for _, item := range later.Items {
			if item.Source == workSourceDayOne {
				t.Fatalf("day one stayed after the first day: %+v", item)
			}
		}
	}
}
