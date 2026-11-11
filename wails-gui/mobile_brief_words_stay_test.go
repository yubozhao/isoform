package main

import (
	"context"
	"testing"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/storage"
)

// A calendar event the person removes after the brief cited it (an edit, or the
// row cleaned up by retention) is not a privacy withdrawal. A Today refresh and a
// later read keep the brief's words, with the citations that still resolve
// (seal v2, rule 2). An event missing from the store altogether still withdraws
// its words (walkthrough v1.1.6); that case is not changed here.
func TestBriefTodayRefreshKeepsWordsWhenACitedEventIsRemoved(t *testing.T) {
	rig := newHandoffTestRig(t)
	now := mobileBriefTestDay(11, 0)
	addTodayRefreshSources(t, rig, now)
	event := calendar.Event{ID: "removed-event", Title: "Design review", Start: now.Add(2 * time.Hour), End: now.Add(3 * time.Hour)}
	row := calendarEventRow(event)
	row.DeletedAt = now.UnixMilli()
	if err := rig.app.store.UpsertCalendarEvents(context.Background(), []storage.CalendarEventRow{row}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	in := mobileBriefInput{AsOf: now.Add(-4 * time.Hour), Location: time.UTC}
	in.Meetings = append(in.Meetings, mobileBriefCandidate{SourceKind: mobileBriefCalendarSource, SourceID: event.ID, Title: event.Title, At: event.Start})
	base := buildMobileBriefV1(in)
	if len(base.Evidence) == 0 {
		t.Fatal("fixture cites nothing")
	}
	cited := base.Evidence[0]
	base.Narrative = []MobileBriefPara{{Text: "You have the design review this afternoon.", Refs: []string{cited.Ref}}}
	base.NarrativeStatus = "ok"
	refreshed := refreshMobileBriefToday(rig.app, base, now, time.UTC)
	if refreshed.NarrativeStatus != "ok" || len(refreshed.Narrative) != 1 || refreshed.Narrative[0].Text != "You have the design review this afternoon." {
		t.Fatalf("a removed event cleared the words: status=%s paragraphs=%d", refreshed.NarrativeStatus, len(refreshed.Narrative))
	}
}

// A build with no words is a failed build for its window. The next build reads
// from the last brief with words, not from the failed build's time.
func TestBriefFailedBuildDoesNotMoveTheWindowStart(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(9, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	text := mobileBriefStoredBrief(t, "2026-10-08", mobileBriefSlotEvening, 17)
	text.NarrativeStatus, text.Narrative = "ok", []MobileBriefPara{{Text: madeAheadWords()}}
	if err := svc.store.save(text, mobileBriefAsOfTime(text)); err != nil {
		t.Fatal(err)
	}
	failed := mobileBriefStoredBrief(t, "2026-10-09", mobileBriefSlotMorning, 7)
	failed.NarrativeStatus, failed.Narrative = "unavailable", nil
	if err := svc.store.save(failed, mobileBriefAsOfTime(failed)); err != nil {
		t.Fatal(err)
	}
	asOf := mobileBriefTestDay(9, 0)
	if got := svc.windowStart(asOf); !got.Equal(mobileBriefAsOfTime(text)) {
		t.Fatalf("a failed build moved the window start to %s, want the last brief with words (%s)", got, mobileBriefAsOfTime(text))
	}
}
