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

func TestBriefObservedTitleOnlyAndRankedOrder(t *testing.T) {
	in := observedBriefFixture()
	in.Doing[0].Detail = "PRIVATE_DOCUMENT_CONTENT"
	in.Doing[0].NextStep = "PRIVATE_DOCUMENT_NEXT_STEP"
	in.Watching = []mobileBriefCandidate{{SourceKind: mobileBriefWebSource, SourceID: "web:1", Title: "PRIVATE_BROWSING_TITLE", At: in.AsOf.Add(-time.Hour)}}
	brief := buildMobileBriefV1(in)
	prompt := mobileBriefTestPrompt(brief, "English")
	if !strings.Contains(prompt, "Project notes") {
		t.Fatal("Today document title was not offered")
	}
	for _, private := range []string{"PRIVATE_DOCUMENT_CONTENT", "PRIVATE_DOCUMENT_NEXT_STEP", "PRIVATE_BROWSING_TITLE"} {
		if strings.Contains(prompt, private) {
			t.Fatalf("private detail reached prompt: %s", private)
		}
	}
	for _, offer := range mobileBriefConversationOffers(brief, "English") {
		for _, ref := range offer.Refs {
			if mobileBriefEvidenceByRef(brief)[ref].SourceKind == mobileBriefFileSource {
				t.Fatal("document sighting created an offer")
			}
		}
	}
	inputs := []morningBriefBoundInput{{Kind: mobileBriefCalendarSource, ID: "one"}, {Kind: "memory", ID: "learned"}, {Kind: mobileBriefCalendarSource, ID: "two"}}
	got := morningBriefObservationFirst(inputs)
	if len(got) != 3 || got[0].Kind != "memory" || got[1].Kind != mobileBriefCalendarSource || got[2].Kind != mobileBriefCalendarSource {
		t.Fatalf("ranked input order = %+v", got)
	}
	got = morningBriefObservationFirst([]morningBriefBoundInput{{Kind: mobileBriefCalendarSource}, {Kind: mobileBriefCalendarSource}, {Kind: mobileBriefCalendarSource}})
	if len(got) != 2 {
		t.Fatalf("calendar fallback = %+v", got)
	}
}

func TestBriefObservedFileBindingWithdrawsWhenPermissionChanges(t *testing.T) {
	app := newTodayContextTestApp(t, true)
	now := time.Now()
	ref := saveTodayContextFileBrief(t, app, "Edited", now)
	stateDir, err := mobileBriefStateDir()
	if err != nil {
		t.Fatal(err)
	}
	brief, ok := newMobileBriefStore(stateDir).load(ref.BriefID)
	if !ok || len(brief.Sections.Doing) != 1 {
		t.Fatal("fixture file brief unavailable")
	}
	fileRef := mobileBriefEvidenceByRef(brief)[brief.Sections.Doing[0].Refs[0]]
	sources := app.collectTodayChatSources(t.Context(), time.Now())
	digest, ok := app.mobileBriefModelSourceDigest(t.Context(), fileRef, brief, sources)
	if !ok {
		t.Fatal("readable document was not bound")
	}
	candidate := morningBriefCandidate{ID: mobileBriefItemKey("candidate", fileRef.SourceKind, fileRef.SourceID),
		SourceKind: fileRef.SourceKind, SourceID: fileRef.SourceID, SourceDigest: digest, Title: fileRef.Label, Confidence: 1}
	bound := app.morningBriefBindRankedInputs(t.Context(), mobileBriefAsOfTime(brief), []morningBriefCandidate{candidate})
	if len(bound) != 1 {
		t.Fatal("ranked morning did not bind the document")
	}
	revision, err := app.readMobileBriefGatherRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer revision.close()
	collected := mobileBriefInput{Slot: brief.Slot, WindowStart: mobileBriefParseTime(brief.WindowStart), AsOf: mobileBriefAsOfTime(brief), Location: time.Local}
	collectMobileBriefDoingContext(t.Context(), app, &collected, false)
	collectedBrief := buildMobileBriefV1(collected)
	revision.cite(collectedBrief)
	binding := app.captureMobileBriefModelSources(t.Context(), collectedBrief, revision)
	if binding == nil || len(binding.Sources) != 1 || binding.Sources[0].Evidence.SourceKind != mobileBriefFileSource {
		t.Fatalf("ordinary model read did not bind the document: %+v", binding)
	}
	if err := app.store.SetUIState(t.Context(), filesPrivateFoldersKey, "invalid"); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.mobileBriefModelSourceDigest(t.Context(), fileRef, brief, sources); ok {
		t.Fatal("document remained readable with an invalid private-folder policy")
	}
	if err := app.store.SetUIState(t.Context(), filesPrivateFoldersKey, "[]"); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetUIState(t.Context(), permissionUIStateKey(filesPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.mobileBriefModelSourceDigest(t.Context(), fileRef, brief, sources); ok {
		t.Fatal("document remained readable after permission withdrawal")
	}
	if app.morningBriefRankedInputsReadable(t.Context(), mobileBriefAsOfTime(brief), bound) {
		t.Fatal("ranked morning retained a withdrawn document")
	}
	delivered := app.mobileBriefForDelivery(brief)
	for _, evidence := range delivered.Evidence {
		if evidence.SourceKind == mobileBriefFileSource {
			t.Fatal("document remained in the brief after permission withdrawal")
		}
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
