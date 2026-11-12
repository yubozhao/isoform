package main

import (
	"strings"
	"testing"
	"time"
)

func TestRankedMorningUsesDistinctSourceBackedItems(t *testing.T) {
	done := morningBriefCandidate{ID: "done", SourceKind: mobileBriefRecapSource, BriefRole: "done", Refs: []string{"e1"}}
	first := morningBriefCandidate{ID: "commitment", BriefRole: "next", Refs: []string{"e2"}}
	second := morningBriefCandidate{ID: "loop", BriefRole: "next", Refs: []string{"e3"}}
	meeting := morningBriefCandidate{ID: "meeting", BriefRole: "next", Refs: []string{"e4"}}
	selection := morningBriefSelection{Lead: &first, Needs: []morningBriefCandidate{second, meeting}, Handled: &done}
	before := `{"paragraphs":[{"part":"next","text":"Review the commitment because it is due.","items":["commitment"]}]}`
	if _, err := parseMorningBriefDraft(before, selection); err != errMorningBriefTooFewNext {
		t.Fatalf("underfilled draft error = %v", err)
	}
	repeated := `{"paragraphs":[{"part":"done","text":"Yesterday the release moved.","items":["done"]},{"part":"next","text":"Review the commitment because it is due.","items":["commitment"]},{"part":"next","text":"The commitment is due soon.","items":["commitment"]}]}`
	if _, err := parseMorningBriefDraft(repeated, selection); err != errMorningBriefTooFewNext {
		t.Fatalf("repeated item error = %v", err)
	}
	question := `{"paragraphs":[{"part":"done","text":"Yesterday the release moved.","items":["done"]},{"part":"next","text":"Review the commitment because it is due.","items":["commitment"]},{"part":"next","text":"The commitment is due soon.","items":["commitment"]},{"part":"next","text":"Review the loop?","items":["loop"]}]}`
	if _, err := parseMorningBriefDraft(question, selection); err != errMorningBriefTooFewNext {
		t.Fatalf("question counted as an item: %v", err)
	}
	after := `{"paragraphs":[{"part":"done","text":"Yesterday the release moved.","items":["done"]},{"part":"next","text":"Review the commitment because it is due.","items":["commitment"]},{"part":"next","text":"Close the open loop because the reply is waiting.","items":["loop"]}]}`
	draft, err := parseMorningBriefDraft(after, selection)
	if err != nil {
		t.Fatal(err)
	}
	parts := mobileBriefNarrativePartsOf(draft.Paragraphs)
	if len(parts) != 2 || parts[0].Count != 1 || parts[1].Count != 2 {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestRankedMorningDoesNotTurnObservationIntoNext(t *testing.T) {
	lead := morningBriefCandidate{ID: "real", BriefRole: "next", Refs: []string{"e1"}}
	automated := morningBriefCandidate{ID: "auto", SourceKind: mobileBriefRecapSource, BriefRole: "done", Refs: []string{"e2"}}
	sel := morningBriefSelection{Lead: &lead, Handled: &automated}
	raw := `{"paragraphs":[{"part":"next","text":"Run the hourly check.","items":["auto","real"]}]}`
	if _, err := parseMorningBriefDraft(raw, sel); err == nil {
		t.Fatal("observation became a next step")
	}
}

func TestRankedRolesKeepMeetingsAndObservationsSeparate(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	in := mobileBriefInput{AsOf: now, Meetings: []mobileBriefCandidate{{SourceKind: mobileBriefCalendarSource, SourceID: "meeting", Kind: "meeting", Status: "upcoming", At: now.Add(time.Hour)}}, Commitments: []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "commit", Kind: "commitment"}}}
	for i := 0; i < 8; i++ {
		in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: string(rune('a' + i)), Kind: "recap", Title: "Observed work", At: now.Add(-time.Hour)})
	}
	inputs := []morningBriefBoundInput{{Kind: mobileBriefRecapSource, ID: "a"}, {Kind: mobileBriefCalendarSource, ID: "meeting"}, {Kind: "purpose", ID: "commit"}}
	got := morningBriefAssignRoles(morningBriefObservationFirst(inputs), in)
	if len(got) != 3 || got[0].Candidate.BriefRole != "done" || got[0].Candidate.LastTouch != formatMobileBriefTime(now.Add(-time.Hour)) || got[1].Candidate.BriefRole != "next" || got[2].Candidate.BriefRole != "next" {
		t.Fatalf("roles = %+v", got)
	}
	in.Meetings[0].At = now.Add(-time.Hour)
	past := morningBriefAssignRoles(inputs, in)
	if past[1].Candidate.BriefRole != "" {
		t.Fatal("a past meeting became an upcoming item")
	}
	if !strings.Contains(rankedPartsPromptRule(mobileBriefSlotMorning), "automated activity") {
		t.Fatal("writer lacks automation boundary")
	}
}

func TestRankedMorningRetryReceivesTheReasonOnce(t *testing.T) {
	calls := 0
	_, err := writeRankedMorningWithRetry("Existing critique.", func(reason string) (morningBriefDraft, error) {
		calls++
		if calls == 1 {
			return morningBriefDraft{}, errMorningBriefTooFewNext
		}
		if !strings.Contains(reason, morningBriefTooFewNextReason) || !strings.Contains(reason, "Existing critique.") {
			t.Fatalf("retry reason = %q", reason)
		}
		return morningBriefDraft{}, errMorningBriefTooFewNext
	})
	if calls != 2 || err != errMorningBriefTooFewNext {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRankedSelectionRetainsRealWorkAndDone(t *testing.T) {
	items := []morningBriefCandidate{
		{ID: "observed", SourceKind: mobileBriefRecapSource, BriefRole: "done", Confidence: 1},
		{ID: "commitment", BriefRole: "next", Confidence: 1},
		{ID: "loop", BriefRole: "next", Confidence: 1},
		{ID: "meeting", BriefRole: "next", Confidence: 1},
	}
	got := morningBriefPlainOrderSelection(items)
	if got.Lead == nil || got.Lead.BriefRole != "next" || len(got.Needs) != 2 || got.Handled == nil || got.Handled.ID != "observed" {
		t.Fatalf("selection=%+v", got)
	}
}

func TestRankedMorningFixtureWithHourlyObservedActivity(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	in := mobileBriefInput{AsOf: now,
		Commitments: []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "commitment", Kind: "commitment"}},
		OpenLoops:   []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "loop", Kind: "goal"}},
		Meetings:    []mobileBriefCandidate{{SourceKind: mobileBriefCalendarSource, SourceID: "meeting", Kind: "meeting", Status: "upcoming", At: now.Add(time.Hour)}},
	}
	var inputs []morningBriefBoundInput
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: id, Kind: "recap", Title: "Release work", At: now.Add(-24 * time.Hour)})
		inputs = append(inputs, morningBriefBoundInput{Kind: mobileBriefRecapSource, ID: id, Candidate: morningBriefCandidate{ID: id, SourceKind: mobileBriefRecapSource, Confidence: 1, Refs: []string{id}}})
	}
	for i := 0; i < 3; i++ {
		id := string(rune('x' + i))
		in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: id, Kind: "recap", Title: "Check open pull requests", At: now.Add(-time.Duration(i) * time.Hour)})
		inputs = append(inputs, morningBriefBoundInput{Kind: mobileBriefRecapSource, ID: id, Candidate: morningBriefCandidate{ID: id, SourceKind: mobileBriefRecapSource, Confidence: 1, Refs: []string{id}}})
	}
	for _, source := range []struct{ kind, id string }{{"purpose", "commitment"}, {"purpose", "loop"}, {mobileBriefCalendarSource, "meeting"}} {
		inputs = append(inputs, morningBriefBoundInput{Kind: source.kind, ID: source.id, Candidate: morningBriefCandidate{ID: source.id, SourceKind: source.kind, Confidence: 1, Refs: []string{source.id}}})
	}
	var candidates []morningBriefCandidate
	for _, row := range morningBriefAssignRoles(inputs, in) {
		candidates = append(candidates, row.Candidate)
	}
	selection := morningBriefPlainOrderSelection(candidates)
	before := `{"paragraphs":[{"part":"next","text":"The one thing on deck is the hourly check on open pull requests and whether the public copy is current. Nothing else needs you beforehand.","items":["x"]}]}`
	if _, err := parseMorningBriefDraft(before, selection); err == nil {
		t.Fatal("underfilled automated draft was accepted")
	}
	after := `{"paragraphs":[{"part":"done","text":"Yesterday the release work moved.","items":["a"]},{"part":"next","text":"Review the commitment because it is due.","items":["commitment"]},{"part":"next","text":"Close the open loop because the reply is waiting.","items":["loop"]},{"part":"next","text":"The meeting is ahead, so keep it in view.","items":["meeting"]}]}`
	draft, err := parseMorningBriefDraft(after, selection)
	if err != nil {
		t.Fatal(err)
	}
	parts := mobileBriefNarrativePartsOf(draft.Paragraphs)
	if len(parts) != 2 || parts[0].Count != 1 || parts[1].Count < 2 {
		t.Fatalf("parts=%+v", parts)
	}
	for _, paragraph := range draft.Paragraphs {
		if paragraph.Part == briefPartNext && strings.Contains(paragraph.Text, "pull requests") {
			t.Fatal("automation became a next item")
		}
	}
}

func TestRankedMorningRejectsLeadOnlyFallbackWhenMaterialExists(t *testing.T) {
	candidates := []morningBriefCandidate{
		{SourceKind: "purpose", SourceID: "commitment", BriefRole: "next", Confidence: 1},
		{SourceKind: "purpose", SourceID: "loop", BriefRole: "next", Confidence: 1},
		{SourceKind: mobileBriefRecapSource, SourceID: "done", BriefRole: "done", Confidence: 1},
	}
	brief := MobileBriefV1{Evidence: []MobileBriefRef{
		{Ref: "e1", SourceKind: "purpose", SourceID: "commitment"},
		{Ref: "e2", SourceKind: "purpose", SourceID: "loop"},
		{Ref: "e3", SourceKind: mobileBriefRecapSource, SourceID: "done"},
	}}
	fallback := morningBriefDraft{Paragraphs: []MobileBriefPara{{Text: "Review the commitment because it is due.", Refs: []string{"e1"}}}}
	if morningBriefFinalCoverage(candidates, brief, fallback) {
		t.Fatal("lead-only fallback passed rich-day coverage")
	}
	draft := morningBriefDraft{Paragraphs: []MobileBriefPara{
		{Text: "Yesterday the release moved.", Refs: []string{"e3"}, Part: briefPartDone},
		{Text: "Review the commitment because it is due.", Refs: []string{"e1"}, Part: briefPartNext},
		{Text: "Close the loop because a reply is waiting.", Refs: []string{"e2"}, Part: briefPartNext},
	}}
	if !morningBriefFinalCoverage(candidates, brief, draft) {
		t.Fatal("well-covered draft was rejected")
	}
	if !morningBriefFinalCoverage(candidates[:1], brief, fallback) {
		t.Fatal("thin day was forced to pad")
	}
}
