package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func observedBriefFixture() mobileBriefInput {
	now := time.Date(2026, 11, 9, 17, 0, 0, 0, time.UTC)
	in := mobileBriefInput{Slot: mobileBriefSlotEvening, WindowStart: now.Add(-10 * time.Hour), AsOf: now, Location: time.UTC}
	for i := 0; i < 8; i++ {
		in.Meetings = append(in.Meetings, mobileBriefCandidate{SourceKind: mobileBriefCalendarSource, SourceID: fmt.Sprintf("event-%d", i), Title: fmt.Sprintf("Meeting %d", i), Kind: "meeting", Status: "upcoming", At: now.Add(time.Duration(i+1) * time.Hour), URL: "https://example.com/call"})
	}
	for i := 0; i < 6; i++ {
		in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: fmt.Sprintf("summary-%d", i), Title: fmt.Sprintf("Work %d", i), Kind: "recap", Status: "done", At: now.Add(-time.Duration(i+1) * time.Hour)})
	}
	for i := 0; i < 7; i++ {
		in.Learned = append(in.Learned, mobileBriefCandidate{SourceKind: "memory", SourceID: fmt.Sprintf("memory-%d", i), Title: fmt.Sprintf("Learning %d", i), Kind: "memory", Status: "updated", At: now.Add(-time.Duration(i+1) * time.Minute)})
	}
	for i := 0; i < 4; i++ {
		in.Attendance = append(in.Attendance, mobileBriefAttendanceObservation{EventID: fmt.Sprintf("past-%d", i), Title: fmt.Sprintf("Past meeting %d", i), State: "attended", ObservedAt: now.Add(-time.Hour)})
	}
	in.Doing = []mobileBriefCandidate{{SourceKind: mobileBriefFileSource, SourceID: "file:1", Title: "Project notes", Kind: "document", Status: "edited", At: now.Add(-time.Hour)}}
	return in
}

func TestBriefObservedFixtureCounts(t *testing.T) {
	brief := buildMobileBriefV1(observedBriefFixture())
	counts := map[string]int{}
	for _, ref := range brief.Evidence {
		counts[ref.SourceKind]++
	}
	binding := map[string]int{}
	for _, ref := range mobileBriefModelReadEvidence(brief) {
		binding[ref.SourceKind]++
	}
	t.Logf("evidence=%v model_read=%v", counts, binding)
}

func TestBriefObservedEvidenceAndModelRead(t *testing.T) {
	brief := buildMobileBriefV1(observedBriefFixture())
	check := func(label string, refs []MobileBriefRef) {
		t.Helper()
		observed, calendar := 0, 0
		for _, ref := range refs {
			if mobileBriefCalendarShaped(ref.SourceKind) {
				if ref.SourceKind == mobileBriefCalendarSource {
					calendar++
				}
			} else {
				observed++
			}
		}
		if len(refs) == 0 || observed*100 < len(refs)*60 || calendar > 2 {
			t.Fatalf("%s: observed=%d calendar=%d total=%d", label, observed, calendar, len(refs))
		}
	}
	check("evidence", brief.Evidence)
	check("model read", mobileBriefModelReadEvidence(brief))
	evidence := mobileBriefEvidenceByRef(brief)
	for _, items := range [][]MobileBriefItem{brief.Sections.Review, brief.Sections.Decide, brief.Sections.Plan, brief.Sections.Highlights} {
		for _, item := range items {
			if len(item.Refs) > 0 && evidence[item.Refs[0]].SourceKind == mobileBriefCalendarSource && !mobileBriefMeetingHasObservation(item, evidence) {
				t.Fatal("calendar-only meeting appeared in a top section")
			}
		}
	}
	if brief.generationDebug == nil || brief.generationDebug.ReservedObservation == 0 || brief.generationDebug.EvidenceByKind[mobileBriefCalendarSource] != 2 {
		t.Fatalf("bad generation counts: %+v", brief.generationDebug)
	}
}

func TestBriefObservedCalendarFallbackAndTie(t *testing.T) {
	in := observedBriefFixture()
	in.Moved, in.Doing, in.Learned, in.Attendance = nil, nil, nil, nil
	calendarOnly := buildMobileBriefV1(in)
	if len(calendarOnly.Sections.DayAhead) != 2 || len(calendarOnly.Sections.Plan) != 0 {
		t.Fatalf("calendar fallback sections: %+v", calendarOnly.Sections)
	}
	if paragraphs := mobileBriefNarrativeTemplate(calendarOnly, "English"); len(paragraphs) != 1 || len(paragraphs[0].Refs) != 1 || strings.Contains(paragraphs[0].Text, "Meeting") {
		t.Fatalf("calendar-only item opened the narrative: %+v", paragraphs)
	}
	calendarOnly.Narrative = mobileBriefNarrativeTemplate(calendarOnly, "English")
	calendarOnly.NarrativeStatus = "skipped"
	if published := mobileBriefForPublication(calendarOnly); published.NarrativeStatus == "quiet" {
		t.Fatal("calendar-only fallback was suppressed before delivery")
	}
	in.Doing = []mobileBriefCandidate{{SourceKind: mobileBriefFileSource, SourceID: "file:matching", Title: in.Meetings[0].Title, Kind: "document", Status: "edited", At: in.AsOf.Add(-time.Hour)}}
	tied := buildMobileBriefV1(in)
	if len(tied.Sections.Plan) == 0 || !mobileBriefMeetingHasObservation(tied.Sections.Plan[0], mobileBriefEvidenceByRef(tied)) {
		t.Fatalf("observed document did not anchor meeting: %+v", tied.Sections.Plan)
	}
}

func TestBriefObservedGenerationDebugLocalOnly(t *testing.T) {
	brief := buildMobileBriefV1(observedBriefFixture())
	data, err := marshalStoredMobileBrief(brief)
	if err != nil {
		t.Fatal(err)
	}
	var restored MobileBriefV1
	if err := unmarshalStoredMobileBrief(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.generationDebug == nil || restored.generationDebug.EvidenceByKind["calendar"] != 2 {
		t.Fatal("generation counts did not survive storage")
	}
	wire, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "generationDebug") {
		t.Fatal("generation counts appeared on the phone wire")
	}
	debug, err := json.Marshal(restored.generationDebug)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"Project notes", "Meeting", "Work"} {
		if strings.Contains(string(debug), content) {
			t.Fatal("generation counts include source content")
		}
	}
	unknown := mobileBriefEvidenceCounts([]MobileBriefRef{{SourceKind: "PRIVATE_SOURCE_TEXT"}})
	if unknown["other"] != 1 || unknown["PRIVATE_SOURCE_TEXT"] != 0 {
		t.Fatal("an unknown source label entered the generation diagnostic")
	}
	for _, kind := range []string{mobileBriefCalendarSource, mobileBriefAttendanceSource, mobileBriefDayShapeSource, mobileBriefMeetingAppSource} {
		if !mobileBriefCalendarShaped(kind) {
			t.Fatalf("calendar-shaped source counted as observation: %s", kind)
		}
	}
}

func TestBriefObservedRejectsCalendarOnlyOpening(t *testing.T) {
	brief := buildMobileBriefV1(observedBriefFixture())
	var calendarRef string
	for _, ref := range brief.Evidence {
		if ref.SourceKind == mobileBriefCalendarSource {
			calendarRef = ref.Ref
			break
		}
	}
	if calendarRef == "" {
		t.Fatal("missing calendar fixture")
	}
	raw := fmt.Sprintf(`{"paragraphs":[{"text":"The meeting is next.","refs":[%q]}]}`, calendarRef)
	if _, _, err := mobileBriefNarrativeFromReply(brief, mobileBriefPicks(brief), raw, "English"); err == nil {
		t.Fatal("calendar-only opening was accepted")
	}
}

func TestBriefObservedCalendarCapIncludesReservedSources(t *testing.T) {
	in := observedBriefFixture()
	in.rankedEvidence = []MobileBriefRef{
		{SourceKind: mobileBriefCalendarSource, SourceID: "reserved-one", Label: "One"},
		{SourceKind: mobileBriefCalendarSource, SourceID: "reserved-two", Label: "Two"},
	}
	brief := buildMobileBriefV1(in)
	if got := mobileBriefEvidenceCounts(brief.Evidence)[mobileBriefCalendarSource]; got != mobileBriefCalendarLimit {
		t.Fatalf("calendar refs across reservations = %d", got)
	}
}

func TestBriefObservedMeetingTieRequiresExactPerson(t *testing.T) {
	in := observedBriefFixture()
	in.Moved, in.Doing, in.Learned, in.Attendance = nil, nil, nil, nil
	in.Meetings[0].rankedAttendees = []string{"Morgan Lee"}
	in.People = []mobileBriefCandidate{{SourceKind: "principal", SourceID: "person", Title: "Morgan", At: in.AsOf.Add(-time.Hour)}}
	if got := buildMobileBriefV1(in).Sections.Plan; len(got) != 0 {
		t.Fatalf("partial name tied a meeting: %+v", got)
	}
	in.People[0].Title = "morgan lee"
	brief := buildMobileBriefV1(in)
	if len(brief.Sections.Plan) != 1 || !mobileBriefMeetingHasObservation(brief.Sections.Plan[0], mobileBriefEvidenceByRef(brief)) {
		t.Fatalf("shared person did not tie the meeting: %+v", brief.Sections.Plan)
	}
}
