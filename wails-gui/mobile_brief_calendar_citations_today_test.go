package main

import (
	"strconv"
	"testing"
	"time"
)

// The 2026-11-15 morning brief cited five calendar events (today's and
// Monday's meetings). Five seconds after it was written, Today's live pass
// rebuilt its citations under the sections' two-meeting limit, read the other
// three as dropped sources, withdrew every written word and sealed the
// template: Today showed no brief. The words a brief was written with are
// cited up to the writer's own calendar limit.
func TestTodayKeepsAWrittenBriefThatCitesMoreThanTwoMeetings(t *testing.T) {
	now := time.Date(2026, 11, 15, 14, 50, 51, 0, time.UTC)
	base := MobileBriefV1{
		BriefID: "2026-11-15-morning", Slot: mobileBriefSlotMorning,
		LocalDate: "2026-11-15", AsOf: formatMobileBriefTime(now), WindowStart: formatMobileBriefTime(now.Add(-12 * time.Hour)),
		NarrativeStatus: "ok", Counts: MobileBriefCounts{OpenLoopsByKind: map[string]int{}},
	}
	for i := 1; i <= 5; i++ {
		base.Evidence = append(base.Evidence, MobileBriefRef{
			Ref: "e" + strconv.Itoa(i), SourceKind: mobileBriefCalendarSource, SourceID: "event-" + strconv.Itoa(i),
			Label: "Meeting " + strconv.Itoa(i), ObservedAt: formatMobileBriefTime(now.Add(time.Duration(i) * time.Hour)),
		})
	}
	base.Narrative = []MobileBriefPara{
		{Text: "Two meetings this afternoon.", Refs: []string{"e1", "e2"}},
		{Text: "Three on Monday.", Refs: []string{"e3", "e4", "e5"}},
	}
	updated := projectMobileBriefToday(base, mobileBriefInput{AsOf: now.Add(5 * time.Second), Today: &mobileBriefTodayInput{Context: map[string]MobileBriefItemContext{}, Unavailable: map[string]bool{}}})
	if updated.NarrativeStatus != "ok" || len(updated.Narrative) != 2 {
		t.Fatalf("Today withdrew a written brief over its calendar citations: status=%q paragraphs=%d", updated.NarrativeStatus, len(updated.Narrative))
	}
	cited := 0
	for _, ref := range updated.Evidence {
		if ref.SourceKind == mobileBriefCalendarSource {
			cited++
		}
	}
	if cited != 5 {
		t.Fatalf("calendar citations kept = %d, want 5", cited)
	}
}
