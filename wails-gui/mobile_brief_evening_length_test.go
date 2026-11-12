package main

import (
	"errors"
	"testing"
)

// eveningRichSelection has three distinct evidence refs across its items, so
// the day is not thin and the evening done part must be fuller.
func eveningRichSelection() morningBriefSelection {
	return morningBriefSelection{
		Lead:  &morningBriefCandidate{ID: "c1", Refs: []string{"e1", "e2"}},
		Needs: []morningBriefCandidate{{ID: "c2", Refs: []string{"e3"}}},
	}
}

// eveningThinSelection has one evidence ref: a quiet day.
func eveningThinSelection() morningBriefSelection {
	return morningBriefSelection{Lead: &morningBriefCandidate{ID: "c1", Refs: []string{"e1"}}}
}

// A full evening reply on a rich day: two done paragraphs of about ninety words
// and one next item. Each cites the lead or a need item.
const eveningFullReply = `{"paragraphs":[` +
	`{"text":"You shipped the draft for the review and closed two loops that had been open since Monday. The release notes moved forward as well, and the design pass on the brief card now has a clear owner. Most of the afternoon went to the review thread, which kept pulling attention away from the draft.","items":["c1"],"part":"done"},` +
	`{"text":"The contract question from Sam is still open and has slipped past its date. A pattern shows up again: the review thread interrupted the draft three times, and each interruption pulled the work off course for a while.","items":["c2"],"part":"done"},` +
	`{"text":"Plan the review for tomorrow morning.","items":["c1"],"part":"next"}]}`

// A thin evening reply: one short done paragraph, not shorter than the one next item.
const eveningThinReply = `{"paragraphs":[` +
	`{"text":"Shipped the draft and closed two loops yesterday.","items":["c1"],"part":"done"},` +
	`{"text":"Plan the review for tomorrow morning.","items":["c1"],"part":"next"}]}`

func TestEveningRichEvidenceDraftHasTwoDonePartsAndSeventyWords(t *testing.T) {
	selection := eveningRichSelection()
	if got := selectionEvidenceRefs(selection); got < eveningThinEvidenceRefs {
		t.Fatalf("fixture has %d evidence refs, want at least %d", got, eveningThinEvidenceRefs)
	}
	draft, err := parseRankedBriefDraft(eveningFullReply, selection, mobileBriefSlotEvening)
	if err != nil {
		t.Fatalf("a full evening draft was refused: %v", err)
	}
	count, words := eveningDoneStats(draft.Paragraphs)
	if count < 2 || words < 70 {
		t.Fatalf("done part = %d paragraphs and %d words, want at least 2 and 70", count, words)
	}
	if reason := eveningDoneLengthReason(draft.Paragraphs, selectionEvidenceRefs(selection)); reason != "" {
		t.Fatalf("a full evening draft failed the length check: %s", reason)
	}
}

func TestEveningThinEvidenceAllowsOneShortDonePart(t *testing.T) {
	selection := eveningThinSelection()
	if !eveningIsThin(selectionEvidenceRefs(selection)) {
		t.Fatal("a one-ref day is not thin")
	}
	draft, err := parseRankedBriefDraft(eveningThinReply, selection, mobileBriefSlotEvening)
	if err != nil {
		t.Fatalf("a thin evening draft with one short done paragraph was refused: %v", err)
	}
	if reason := eveningDoneLengthReason(draft.Paragraphs, selectionEvidenceRefs(selection)); reason != "" {
		t.Fatalf("a thin day failed the length check: %s", reason)
	}
	parts := mobileBriefNarrativePartsOf(draft.Paragraphs)
	if len(parts) != 2 || parts[0] != (MobileBriefNarrativePart{Kind: briefPartDone, Count: 1}) {
		t.Fatalf("thin evening parts = %+v, want done 1 then next 1", parts)
	}
	// A thin day still refuses a done part shorter than next, and a rich day
	// refuses one too.
	forward := `{"paragraphs":[` +
		`{"text":"Shipped the draft.","items":["c1"],"part":"done"},` +
		`{"text":"Send the draft to Sam before the review, because Sam is waiting on it.","items":["c1"],"part":"next"}]}`
	if _, err := parseRankedBriefDraft(forward, selection, mobileBriefSlotEvening); err != nil {
		t.Fatalf("part balance is a goal, not a gate: a thin evening day kept a done part shorter than next must be kept, got %v", err)
	}
	if _, err := parseRankedBriefDraft(forward, eveningRichSelection(), mobileBriefSlotEvening); err != nil {
		t.Fatalf("part balance is a goal, not a gate: a rich evening day kept a done part shorter than next must be kept, got %v", err)
	}
}

func TestEveningShortDraftTriggersExactlyOneRetry(t *testing.T) {
	short := MobileBriefPara{Text: "Shipped the draft.", Part: briefPartDone}
	plan := MobileBriefPara{Text: "Plan the review for tomorrow morning.", Part: briefPartNext}
	full := []MobileBriefPara{
		{Text: "You shipped the draft and closed two loops that had been open since Monday. The release notes moved forward as well, and the design pass now has a clear owner. Most of the afternoon went to the review thread, which kept pulling attention away from the draft.", Part: briefPartDone},
		{Text: "The contract question from Sam is still open and has slipped past its date. A pattern shows up again, and the review thread interrupted the draft three times.", Part: briefPartDone},
		plan,
	}
	var reasons []string
	attempt := func(reason string) (morningBriefDraft, error) {
		reasons = append(reasons, reason)
		if len(reasons) == 1 {
			return morningBriefDraft{Paragraphs: []MobileBriefPara{short, plan}}, nil
		}
		return morningBriefDraft{Paragraphs: full}, nil
	}
	draft, err := writeRankedWithLengthRetry(briefPartDone, 3, attempt)
	if err != nil {
		t.Fatalf("writeRankedWithLengthRetry: %v", err)
	}
	if len(reasons) != 2 {
		t.Fatalf("writer ran %d times, want exactly two (one retry)", len(reasons))
	}
	if reasons[0] != "" || reasons[1] == "" {
		t.Fatalf("reasons = %q, want no reason first and a reason on the retry", reasons)
	}
	if count, _ := eveningDoneStats(draft.Paragraphs); count != 2 {
		t.Fatalf("kept draft has %d done paragraphs, want the retry's 2", count)
	}
}

func TestEveningRetryKeepsTheBestDraftNeverEmpty(t *testing.T) {
	plan := MobileBriefPara{Text: "Plan the review for tomorrow morning.", Part: briefPartNext}
	first := morningBriefDraft{Paragraphs: []MobileBriefPara{
		{Text: "Shipped the draft and closed two loops before the review started.", Part: briefPartDone}, plan}}
	t.Run("failed retry keeps the first draft", func(t *testing.T) {
		calls := 0
		attempt := func(reason string) (morningBriefDraft, error) {
			calls++
			if calls == 1 {
				return first, nil
			}
			return morningBriefDraft{}, errors.New("writer failed")
		}
		draft, err := writeRankedWithLengthRetry(briefPartDone, 3, attempt)
		if err != nil || calls != 2 || len(draft.Paragraphs) != len(first.Paragraphs) || draft.Paragraphs[0].Text != first.Paragraphs[0].Text {
			t.Fatalf("draft=%+v err=%v calls=%d, want the first draft after one failed retry", draft, err, calls)
		}
	})
	t.Run("a shorter retry does not replace a longer first draft", func(t *testing.T) {
		shorter := morningBriefDraft{Paragraphs: []MobileBriefPara{
			{Text: "Shipped it.", Part: briefPartDone}, plan}}
		calls := 0
		attempt := func(reason string) (morningBriefDraft, error) {
			calls++
			if calls == 1 {
				return first, nil
			}
			return shorter, nil
		}
		draft, err := writeRankedWithLengthRetry(briefPartDone, 3, attempt)
		if err != nil || len(draft.Paragraphs) == 0 || draft.Paragraphs[0].Text != first.Paragraphs[0].Text {
			t.Fatalf("draft=%+v err=%v, want the longer first draft kept", draft, err)
		}
	})
	t.Run("a failed first attempt is returned for the pipeline to retry", func(t *testing.T) {
		calls := 0
		attempt := func(reason string) (morningBriefDraft, error) {
			calls++
			return morningBriefDraft{}, errors.New("parse failed")
		}
		if _, err := writeRankedWithLengthRetry(briefPartDone, 3, attempt); err == nil || calls != 1 {
			t.Fatalf("err=%v calls=%d, want the first error and no length retry", err, calls)
		}
	})
}

// The morning path makes one attempt and never adds a length reason.
func TestMorningWriterHasNoLengthRetry(t *testing.T) {
	calls := 0
	attempt := func(reason string) (morningBriefDraft, error) {
		calls++
		if reason != "" {
			t.Fatalf("morning attempt got a length reason %q", reason)
		}
		return morningBriefDraft{Paragraphs: []MobileBriefPara{{Text: "Send the draft.", Part: briefPartNext}}}, nil
	}
	if _, err := writeRankedWithLengthRetry(briefPartNext, 3, attempt); err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one morning attempt", err, calls)
	}
}
