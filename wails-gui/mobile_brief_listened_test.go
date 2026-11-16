package main

import (
	"fmt"
	"testing"
	"time"
)

// Read-along (computer-brief v1.0.0 rule 3): each timed paragraph says where
// the voice stops reading it, before the pause, and the last one ends with
// the recording.
func TestParagraphStartsCarryWhereEachParagraphEnds(t *testing.T) {
	brief := MobileBriefV1{BriefID: "2026-11-15-evening", NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{{Text: "The first paragraph."}, {Text: "The last paragraph."}}}
	segments := []string{
		mobileBriefParagraphSpoken(brief, brief.Narrative[0], "en"),
		mobileBriefParagraphSpoken(brief, brief.Narrative[1], "en"),
	}
	got := mobileBriefParagraphStarts(brief, "en", segments, []float64{0, 2.45}, 4)
	want := []MobileBriefAudioParagraph{
		{Source: mobileBriefAudioSourceNarrative, Index: 0, StartSeconds: 0, EndSeconds: 2},
		{Source: mobileBriefAudioSourceNarrative, Index: 1, StartSeconds: 2.45, EndSeconds: 4},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("paragraphs = %v, want %v", got, want)
	}
}

// A recording made before ends were stored gets them at read: the next start
// less the pause, or the end of the recording. A stored end stays.
func TestOlderRecordingGetsParagraphEndsAtRead(t *testing.T) {
	audio := &MobileBriefAudio{Status: mobileBriefAudioStatusOK, DurationSeconds: 6, binding: "en:x",
		Paragraphs: []MobileBriefAudioParagraph{{Index: 0, StartSeconds: 0}, {Index: 1, StartSeconds: 2.45, EndSeconds: 4}, {Index: 2, StartSeconds: 4.45}}}
	got := mobileBriefAudioWithParagraphEnds(audio)
	ends := []float64{got.Paragraphs[0].EndSeconds, got.Paragraphs[1].EndSeconds, got.Paragraphs[2].EndSeconds}
	if fmt.Sprint(ends) != fmt.Sprint([]float64{2, 4, 6}) || got.binding != "en:x" {
		t.Fatalf("ends = %v binding=%q", ends, got.binding)
	}
	if audio.Paragraphs[0].EndSeconds != 0 {
		t.Fatal("the stored recording was changed in place")
	}
}

// Past briefs (computer-brief v1.0.0 rule 9): a brief is not played until the
// player reaches 5 s, part heard after, played near the end, and never goes
// back to a lesser state.
func TestBriefListenedStateMovesOnlyForward(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	now := time.Date(2026, 11, 15, 21, 0, 0, 0, time.UTC)
	id := "2026-11-15-evening"
	steps := []struct {
		position float64
		changed  bool
		want     string
	}{
		{2, false, ""},
		{6, true, briefListenedPartHeard},
		{30, false, briefListenedPartHeard},
		{98, true, briefListenedPlayed},
		{0, false, briefListenedPlayed},
	}
	for _, step := range steps {
		if changed := app.noteBriefHeard(id, step.position, 100, now); changed != step.changed {
			t.Fatalf("at %.0f s: changed=%v, want %v", step.position, changed, step.changed)
		}
		if got := app.briefListenedRecords()[id].State; got != step.want {
			t.Fatalf("at %.0f s: state %q, want %q", step.position, got, step.want)
		}
	}
	if app.noteBriefHeard("../not-a-brief", 99, 100, now) {
		t.Fatal("an invalid brief id was recorded")
	}
}

// Each past brief counts its items by the ids Your work uses: an answered
// item is answered here too, an undone one is to answer again.
func TestPastBriefAnswerCountsUseYourWorkIDs(t *testing.T) {
	brief := MobileBriefV1{BriefID: "2026-11-15-morning", NarrativeStatus: "ok",
		Offers: []MobileBriefOffer{{ID: "a", Label: "Prep me"}, {ID: "b", Label: "Draft it"}, {ID: "", Label: "No id"}}}
	answers := map[string]AnswerRecord{workBriefOfferID(brief.BriefID, "a"): {Action: workActionYes}}
	if toAnswer, answered := pastBriefAnswerCounts(brief, "English", answers); toAnswer != 1 || answered != 1 {
		t.Fatalf("toAnswer=%d answered=%d, want 1 and 1", toAnswer, answered)
	}
}

// Past briefs' rows carry the listened state, learned from the player's
// reports, and the item counts.
func TestPastBriefRowsCarryListenedState(t *testing.T) {
	app, svc, built := builtBriefFixture(t)
	app.mobileBriefSvc = svc
	rows := app.withPastBriefState(svc.ListMobileBriefs(10))
	if len(rows) == 0 || rows[0].BriefID != built.BriefID || rows[0].Listened != briefListenedNotPlayed || rows[0].Played {
		t.Fatalf("before playing: %+v", rows)
	}
	app.SetMenuBarBriefPlayback(built.BriefID, "playing", 99, 100)
	rows = app.withPastBriefState(svc.ListMobileBriefs(10))
	if rows[0].Listened != briefListenedPlayed || !rows[0].Played {
		t.Fatalf("after playing to the end: %+v", rows[0])
	}
}

// Read-along marks split each timed paragraph into its sentences, as UTF-16
// offsets into the shown text, and share its span out by length.
func TestReadAlongMarksSplitTheParagraphIntoSentences(t *testing.T) {
	brief := MobileBriefV1{NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{{Text: "Lena moved it. Prep is ready – read it first."}},
		Audio: &MobileBriefAudio{Status: mobileBriefAudioStatusOK, DurationSeconds: 9,
			Paragraphs: []MobileBriefAudioParagraph{{Source: mobileBriefAudioSourceNarrative, Index: 0, StartSeconds: 0, EndSeconds: 9}}}}
	marks := mobileBriefAudioMarks(brief)
	if len(marks) != 2 || marks[0].From != 0 || marks[0].To != 14 || marks[1].From != 15 || marks[1].To != 45 {
		t.Fatalf("marks = %+v", marks)
	}
	if marks[0].Start != 0 || marks[1].End != 9 || marks[0].End != marks[1].Start || marks[0].End <= 0 || marks[0].End >= 4.5 {
		t.Fatalf("times = %+v", marks)
	}
}
