package main

import (
	"testing"

	briefcore "wails-gui/internal/brief"
)

// rankedFixtureSelection is a morning selection with one lead item, which the
// writer must cite.
func rankedFixtureSelection() morningBriefSelection {
	return morningBriefSelection{Lead: &morningBriefCandidate{ID: "c1", Refs: []string{"e1"}}}
}

// A morning reply: one short done paragraph, then two next items. Next is the
// larger part, and the lead is cited in next.
const morningPartsReply = `{"paragraphs":[` +
	`{"text":"Shipped the draft yesterday.","items":["c1"],"part":"done"},` +
	`{"text":"Send the draft to Sam before the review, because Sam is waiting on it.","items":["c1"],"part":"next"},` +
	`{"text":"Book the review room for Thursday before noon.","items":["c1"],"part":"next"}]}`

// An evening reply: a reflective done part is the larger one, and the next part
// is one short item for tomorrow.
const eveningReflectiveParagraphs = `[` +
	`{"text":"You shipped the draft and closed two loops, but the review slipped again.","items":["c1"],"part":"done"},` +
	`{"text":"The review has slipped twice this week, which is becoming a pattern.","items":["c1"],"part":"done"},` +
	`{"text":"Plan the review for tomorrow morning.","items":["c1"],"part":"next"}]`

func TestMorningWriterProducesBothPartsDoneFirst(t *testing.T) {
	draft, err := parseMorningBriefDraft(morningPartsReply, rankedFixtureSelection())
	if err != nil {
		t.Fatalf("parseMorningBriefDraft: %v", err)
	}
	parts := mobileBriefNarrativePartsOf(draft.Paragraphs)
	if len(parts) != 2 || parts[0] != (MobileBriefNarrativePart{Kind: briefPartDone, Count: 1}) ||
		parts[1] != (MobileBriefNarrativePart{Kind: briefPartNext, Count: 2}) {
		t.Fatalf("parts = %+v, want done 1 then next 2", parts)
	}
	if draft.Paragraphs[0].Part != briefPartDone {
		t.Fatalf("first paragraph is %q, want done", draft.Paragraphs[0].Part)
	}
}

func TestMorningWriterPutsDoneFirstWhenTheModelWritesNextFirst(t *testing.T) {
	reply := `{"paragraphs":[` +
		`{"text":"Send the draft to Sam before the review, because Sam is waiting on it.","items":["c1"],"part":"next"},` +
		`{"text":"Shipped the draft yesterday.","items":["c1"],"part":"done"},` +
		`{"text":"Book the review room for Thursday before noon.","items":["c1"],"part":"next"}]}`
	draft, err := parseMorningBriefDraft(reply, rankedFixtureSelection())
	if err != nil {
		t.Fatalf("parseMorningBriefDraft: %v", err)
	}
	if draft.Paragraphs[0].Part != briefPartDone {
		t.Fatalf("the done paragraph did not move first: %+v", draft.Paragraphs)
	}
}

func TestMorningWriterRejectsDoneLargerThanNext(t *testing.T) {
	reply := `{"paragraphs":[` +
		`{"text":"Shipped the draft yesterday and reviewed the plan with Sam, then closed two loops and moved the release.","items":["c1"],"part":"done"},` +
		`{"text":"Book the room.","items":["c1"],"part":"next"}]}`
	if _, err := parseMorningBriefDraft(reply, rankedFixtureSelection()); err == nil {
		t.Fatal("a morning draft whose done part is larger was accepted")
	}
}

func TestMorningWriterRejectsClockTimeInDone(t *testing.T) {
	reply := `{"paragraphs":[` +
		`{"text":"Shipped the draft at 9:30 yesterday.","items":["c1"],"part":"done"},` +
		`{"text":"Send the draft to Sam before the review, because Sam is waiting on it.","items":["c1"],"part":"next"}]}`
	if _, err := parseMorningBriefDraft(reply, rankedFixtureSelection()); err == nil {
		t.Fatal("a clock time in the done part was accepted")
	}
}

func TestMorningWriterWithoutLabelsKeepsTheOldShape(t *testing.T) {
	reply := `{"paragraphs":[{"text":"Send the draft to Sam first.","items":["c1"]}]}`
	draft, err := parseMorningBriefDraft(reply, rankedFixtureSelection())
	if err != nil {
		t.Fatalf("an unlabelled reply was refused: %v", err)
	}
	if parts := mobileBriefNarrativePartsOf(draft.Paragraphs); parts != nil {
		t.Fatalf("an unlabelled reply got parts: %+v", parts)
	}
}

func TestMorningWriterRefusesMixedLabels(t *testing.T) {
	reply := `{"paragraphs":[` +
		`{"text":"Send the draft to Sam first.","items":["c1"],"part":"next"},` +
		`{"text":"Shipped the draft yesterday.","items":["c1"]}]}`
	if _, err := parseMorningBriefDraft(reply, rankedFixtureSelection()); err == nil {
		t.Fatal("a draft with some labelled paragraphs was accepted")
	}
}

func TestEveningBalanceFixtureMakesDoneTheLargerPart(t *testing.T) {
	paragraphs := []MobileBriefPara{
		{Text: "You shipped the draft and closed two loops, but the review slipped again.", Refs: []string{"e1"}, Part: briefPartDone},
		{Text: "The review has slipped twice this week, which is becoming a pattern.", Refs: []string{"e1"}, Part: briefPartDone},
		{Text: "Plan the review for tomorrow morning.", Refs: []string{"e1"}, Part: briefPartNext},
	}
	ordered, err := orderRankedParts(paragraphs, briefcore.PartDone)
	if err != nil {
		t.Fatalf("an evening draft with a larger done part was refused: %v", err)
	}
	if len(ordered) != 3 || ordered[2].Part != briefPartNext {
		t.Fatalf("evening order = %+v", ordered)
	}
	forward := []MobileBriefPara{
		{Text: "Shipped the draft.", Refs: []string{"e1"}, Part: briefPartDone},
		{Text: "Send the draft to Sam before the review, because Sam is waiting on it.", Refs: []string{"e1"}, Part: briefPartNext},
	}
	if _, err := orderRankedParts(forward, briefcore.PartDone); err == nil {
		t.Fatal("an evening draft whose next part is larger was accepted")
	}
}

// The live evening path: the ranked writer with the evening slot accepts a
// reflective draft and refuses a forward one.
func TestEveningRankedWriterReflectsBack(t *testing.T) {
	reflective := `{"paragraphs":[` +
		`{"text":"You shipped the draft and closed two loops, but the review slipped again.","items":["c1"],"part":"done"},` +
		`{"text":"The review has slipped twice this week, which is becoming a pattern.","items":["c1"],"part":"done"},` +
		`{"text":"Plan the review for tomorrow morning.","items":["c1"],"part":"next"}]}`
	draft, err := parseRankedBriefDraft(reflective, rankedFixtureSelection(), mobileBriefSlotEvening)
	if err != nil {
		t.Fatalf("an evening reflective draft was refused: %v", err)
	}
	parts := mobileBriefNarrativePartsOf(draft.Paragraphs)
	if len(parts) != 2 || parts[0].Kind != briefPartDone || parts[0].Count != 2 || parts[1].Count != 1 {
		t.Fatalf("evening parts = %+v, want done 2 then next 1", parts)
	}
	forward := `{"paragraphs":[` +
		`{"text":"Shipped the draft yesterday.","items":["c1"],"part":"done"},` +
		`{"text":"Send the draft to Sam before the review, because Sam is waiting on it.","items":["c1"],"part":"next"},` +
		`{"text":"Book the review room for Thursday before noon.","items":["c1"],"part":"next"}]}`
	if _, err := parseRankedBriefDraft(forward, rankedFixtureSelection(), mobileBriefSlotEvening); err == nil {
		t.Fatal("an evening draft whose next part is larger was accepted")
	}
	if _, err := parseRankedBriefDraft(morningPartsReply, rankedFixtureSelection(), mobileBriefSlotMorning); err != nil {
		t.Fatalf("the morning draft was refused on the morning path: %v", err)
	}
}

// The evening empty and quiet lines name the evening, and the empty line is
// recognised as a static sentence so the reference check can skip it.
func TestEveningEmptyLineIsStaticAndNamesTheEvening(t *testing.T) {
	if got := rankedEmptyLine(mobileBriefSlotEvening, "en"); got != eveningEmptyEN {
		t.Fatalf("evening empty line = %q", got)
	}
	if got := rankedQuietLine(mobileBriefSlotEvening, "en"); got != eveningQuietEN {
		t.Fatalf("evening quiet line = %q", got)
	}
	brief := MobileBriefV1{rankedEdition: true, rankedEmpty: true, NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{{Text: eveningEmptyEN}}}
	if !mobileBriefStaticEmpty(brief) {
		t.Fatal("the evening empty line is not recognised as static")
	}
}

func TestNarrativePartsCountsFollowTheFinalNarrative(t *testing.T) {
	paragraphs := []MobileBriefPara{
		{Text: "a", Part: briefPartDone},
		{Text: "b", Part: briefPartNext},
		{Text: "c", Part: briefPartNext},
	}
	parts := mobileBriefNarrativePartsOf(paragraphs)
	if len(parts) != 2 || parts[0].Count != 1 || parts[1].Count != 2 {
		t.Fatalf("counts = %+v", parts)
	}
	if mobileBriefNarrativePartsOf([]MobileBriefPara{{Text: "a", Part: briefPartNext}, {Text: "b", Part: briefPartDone}}) != nil {
		t.Fatal("out-of-order labels produced parts")
	}
	if mobileBriefNarrativePartsOf([]MobileBriefPara{{Text: "a"}, {Text: "b", Part: briefPartNext}}) != nil {
		t.Fatal("an unlabelled paragraph produced parts")
	}
}
