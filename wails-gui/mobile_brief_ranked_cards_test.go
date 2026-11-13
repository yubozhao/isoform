package main

import (
	"strings"
	"testing"
)

func TestRankedMorningClosingRequiresLeadAndGroundedAnswers(t *testing.T) {
	brief := MobileBriefV1{LocalDate: "2026-10-30", Timezone: "UTC", rankedEdition: true,
		Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: "purpose", SourceID: "plan", Label: "Launch plan"}, {Ref: "e2", SourceKind: "purpose", SourceID: "other", Label: "Other"}},
		Sections: MobileBriefSections{Commitments: []MobileBriefItem{{ID: "plan", Title: "Launch plan", Kind: "commitment", Refs: []string{"e1"}}}}}
	lead := morningBriefCandidate{ID: "plan", Refs: []string{"e1"}}
	selection := morningBriefSelection{Lead: &lead}
	draft := morningBriefDraft{Paragraphs: []MobileBriefPara{{Text: "Review the launch plan.", Refs: []string{"e1"}}, {Text: "Want to review the launch plan?", Refs: []string{"e1"}}},
		Answers: []mobileBriefCardWords{{Label: "Review the plan", Message: "Yes, review the launch plan."}, {Label: "Leave it", Message: "No, leave it."}}}
	if _, err := prepareRankedMorningDraft(brief, draft, selection, "English"); err != nil {
		t.Fatalf("grounded closing was rejected: %v", err)
	}
	missing := draft
	missing.Answers = missing.Answers[:1]
	// Since 2026-11-13 a closing that does not hold is dropped, not the brief.
	if got, err := prepareRankedMorningDraft(brief, missing, selection, "English"); err != nil || len(got.Paragraphs) != 1 {
		t.Fatalf("closing without two answers was kept or failed the brief: %v %+v", err, got.Paragraphs)
	} else {
		for _, card := range got.Cards {
			if card.AnswerTo != "" {
				t.Fatalf("an answer chip outlived its closing: %+v", card)
			}
		}
	}
	unrelated := draft
	unrelated.Paragraphs = append([]MobileBriefPara(nil), draft.Paragraphs...)
	unrelated.Paragraphs[1].Refs = []string{"e2"}
	if got, err := prepareRankedMorningDraft(brief, unrelated, selection, "English"); err != nil || len(got.Paragraphs) != 1 {
		t.Fatalf("closing about an unselected subject was kept or failed the brief: %v %+v", err, got.Paragraphs)
	}
	unproven := draft
	unproven.Paragraphs = append([]MobileBriefPara(nil), draft.Paragraphs...)
	unproven.Paragraphs[0].Text = "Skip the launch plan."
	if _, err := prepareRankedMorningDraft(brief, unproven, selection, "English"); err == nil {
		t.Fatal("unsupported skip advice was accepted")
	}
	hold := draft
	hold.Paragraphs = append([]MobileBriefPara(nil), draft.Paragraphs...)
	hold.Paragraphs[1].Text = "Want me to hold Monday morning for the plan?"
	if _, err := prepareRankedMorningDraft(brief, hold, selection, "English"); err == nil {
		t.Fatal("calendar hold closing was accepted")
	}
	if !strings.Contains(buildMorningBriefWriterPrompt("A", "English", "{}"), "Do not offer to hold") {
		t.Fatal("ranked writer was not instructed to withhold hold offers")
	}
	chip := draft
	chip.Paragraphs = append([]MobileBriefPara(nil), draft.Paragraphs...)
	chip.Paragraphs[0].chipWords = &mobileBriefCardWords{Label: "Hold Monday morning", Message: "Hold Monday morning for the launch plan."}
	got, err := prepareRankedMorningDraft(brief, chip, selection, "English")
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range got.Cards {
		if mobileCalendarHoldOffer.MatchString(card.Label) || mobileCalendarHoldOffer.MatchString(card.Message) {
			t.Fatalf("calendar hold chip survived: %+v", card)
		}
	}
}

func TestRankedChipAvoidsRawTaskTitles(t *testing.T) {
	for _, tc := range []struct {
		label, title string
		want         bool
	}{
		{"Find time for Make sure I pay Cali tax", "Make sure I pay Cali tax", true},
		{"Find time for the Cali tax", "Make sure I pay Cali tax", false},
		{"Open the Cali tax", "Cali tax", false},
		{"Review the plan", "Review the plan", true},
		{"安排支付加州税", "支付加州税", true},
	} {
		if got := morningBriefRawTaskLabel(tc.label, tc.title); got != tc.want {
			t.Fatalf("raw title %q in %q: got %v", tc.title, tc.label, got)
		}
	}
}
