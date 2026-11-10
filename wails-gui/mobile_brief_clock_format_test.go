package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBriefGeneratedClockFormat(t *testing.T) {
	for _, language := range []string{"en", "zh-CN"} {
		for _, tc := range []struct{ input, en, zh string }{
			{"9am", "9:00 AM", "9:00"},
			{"12:30pm", "12:30 PM", "12:30"},
			{"9:00am", "9:00 AM", "9:00"},
			{"noon", "noon", "noon"},
		} {
			input := tc.input
			t.Run(language+"/"+input, func(t *testing.T) {
				hour, minute := 9, 0
				if input == "12:30pm" {
					hour, minute = 12, 30
				}
				want := tc.en
				if language == "zh-CN" {
					want = tc.zh
				}
				at := time.Date(2026, 10, 23, hour, minute, 0, 0, time.UTC)
				brief := MobileBriefV1{Slot: mobileBriefSlotOnRequest, AsOf: "2026-10-23T07:00:00Z", Timezone: "UTC",
					Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: mobileBriefCalendarSource, SourceID: "review"}, {Ref: "e2", SourceKind: "principal", SourceID: "dana", Label: "Dana"}},
					Sections: MobileBriefSections{Meetings: []MobileBriefItem{{Title: "Dana review", Status: "upcoming", Kind: "meeting", At: at.Format(time.RFC3339), EndAt: at.Add(time.Hour).Format(time.RFC3339), Refs: []string{"e1", "e2"}}}}}
				raw, err := json.Marshal(mobileBriefNarrativeReply{Paragraphs: []mobileBriefNarrativeReplyParagraph{{Text: "Dana review starts at " + input + ".", Refs: []string{"e1", "e2"}}}})
				if err != nil {
					t.Fatal(err)
				}
				paragraphs, _, err := mobileBriefNarrativeFromReply(brief, mobileBriefPicks(brief), string(raw), language)
				if err != nil || len(paragraphs) != 1 || paragraphs[0].Text != "Dana review starts at "+want+"." {
					t.Fatalf("generated paragraphs = %+v, err=%v, want clock %q", paragraphs, err, want)
				}
				cards := []MobileBriefCard{{ID: "review", Kind: mobileBriefCardAction, Mode: mobileBriefCardSend, Label: "Plan Dana at " + input, Message: "Plan Dana review at " + input, Refs: []string{"e1"}}}
				words := []mobileBriefCardWords{{ID: "review", Label: "Plan Dana at " + input, Message: "Plan Dana review at " + input}}
				// Both the no-model chip and the model-written chip use the clock.
				for _, written := range [][]mobileBriefCardWords{nil, words} {
					got := mobileBriefApplyCardWords(brief, cards, written, language)
					if len(got) != 1 || got[0].Label != "Plan Dana at "+want || got[0].Message != "Plan Dana review at "+want {
						t.Fatalf("chips = %+v, want clock %q", got, want)
					}
					if written != nil && got[0].Writer != mobileBriefCardWriterModel {
						t.Fatalf("normalization discarded a valid generated clock label: %+v", got[0])
					}
				}
			})
		}
	}
}

func TestBriefClockChipValidationAndShortening(t *testing.T) {
	for _, tc := range []struct {
		label string
		valid bool
	}{
		{"Plan Dana at 9:00 AM", true},
		{"Plan Dana at 12:30 PM", true},
		{"Plan Dana: today", false},
		{"Plan Dana at 00:00 PM", false},
	} {
		if got := mobileBriefCardLabelValid(tc.label, "en"); got != tc.valid {
			t.Errorf("label %q: valid=%v, want %v", tc.label, got, tc.valid)
		}
	}
	message := "Plan " + strings.Repeat("x", 26) + " at 9am"
	normalized := normalizeBriefText(message, "en", time.Time{}, false)
	label := mobileBriefQuestionChipLabel(normalized, "en")
	// A chip too long to read is not offered, never cut off (today-review v1.1.4 #5).
	if label != "" {
		t.Fatalf("oversized chip was cut instead of withheld: %q", label)
	}
}

func TestBriefRetainedClockChipShortensAfterNormalization(t *testing.T) {
	a := &App{}
	brief := MobileBriefV1{BriefID: "retained-clock", NarrativeStatus: "ok",
		Evidence:  []MobileBriefRef{{Ref: "e1", SourceKind: "activity", SourceID: "review"}},
		Sections:  MobileBriefSections{WaitingOnYou: []MobileBriefItem{{ID: "waiting-1", Title: "Dana", Kind: "waiting_on_reply", Refs: []string{"e1"}}}},
		Narrative: []MobileBriefPara{{Text: "Dana is waiting for the project notes.", Refs: []string{"e1"}}}}
	questions := cardsByKind(mobileBriefPatternCards(brief, "English"), mobileBriefCardQuestion)
	if len(questions) == 0 {
		t.Fatal("fixture has no question chip")
	}
	card := questions[0]
	card.Label = "Plan " + strings.Repeat("x", 26) + " at 9am"
	card.Message, card.Writer = card.Label, mobileBriefCardWriterModel
	brief.Cards = []MobileBriefCard{card}
	got := a.mobileBriefWithCardsFrom(t.Context(), brief, &todayChatSources{})
	kept, ok := mobileBriefFindCard(got, card.ID)
	if ok && (strings.HasSuffix(kept.Label, "…") || mobileBriefChipLabelWidth(kept.Label) > mobileBriefChipLabelMaxWidth || kept.Facts != card.Facts) {
		t.Fatalf("retained chip kept an oversized or cut label: %+v", kept)
	}
}

func TestBriefPromptUsesSharedClockRules(t *testing.T) {
	for _, language := range []string{"English", "zh-CN"} {
		prompt := mobileBriefTestPrompt(briefPicksFixture(), language)
		for _, want := range []string{briefShortClock(9, 0, language), briefShortClock(12, 30, language), "every generated field", "shape of today", "next meetings", "chip labels and prepared messages, including answers", "Leave words such as noon alone"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s prompt missing %q", language, want)
			}
		}
	}
}

func TestBriefEveryGeneratedFieldUsesDisplayClocks(t *testing.T) {
	for _, language := range []string{"en", "zh-CN"} {
		t.Run(language, func(t *testing.T) {
			const raw = "Meet at 9am, 12pm and 1:50pm, or 9:00am; noon stays free."
			want := "Meet at 9:00 AM, 12:00 PM and 1:50 PM, or 9:00 AM; noon stays free."
			if language == "zh-CN" {
				want = "Meet at 9:00, 12:00 and 13:50, or 9:00; noon stays free."
			}
			item := MobileBriefItem{ID: "meeting-at-9am", Kind: "day_shape", Title: raw, Detail: raw, NextStep: raw,
				At: "2026-10-23T09:00:00Z", URL: "https://example.com/9am", AttentionReason: raw,
				Context: &MobileBriefItemContext{Preview: raw}, Outcome: &ActivityThreadOutcome{Summary: raw, NextStep: raw}}
			brief := MobileBriefV1{BriefID: "display-clock", Slot: mobileBriefSlotOnRequest, AsOf: "2026-10-23T07:00:00Z", Timezone: "UTC", NarrativeStatus: "ok",
				Sections: MobileBriefSections{Meetings: []MobileBriefItem{item}, DayAhead: []MobileBriefItem{item}}, Upcoming: []MobileBriefItem{item},
				Narrative: []MobileBriefPara{{Text: raw}}, WeeklyRecap: []MobileBriefPara{{Text: raw}},
				Cards:  []MobileBriefCard{{ID: "answer-9am", Kind: mobileBriefCardAction, Label: "Plan at 9am", Message: raw}},
				Offers: []MobileBriefOffer{{Label: "Plan at 9am", Prompt: raw, Text: raw}}, Evidence: []MobileBriefRef{{Label: raw, SourceID: "source-9am"}},
				MeetingAttendance: []MobileBriefAttendance{{Title: raw, Reason: raw}}, Generation: &MobileBriefGeneration{Message: raw}}
			before, _ := json.Marshal(brief)
			got := mobileBriefWithDisplayTimes(brief, language)
			for field, value := range map[string]string{
				"shape of today": got.Sections.Meetings[0].Detail, "day ahead": got.Sections.DayAhead[0].Detail,
				"next meeting title": got.Upcoming[0].Title, "next meeting detail": got.Upcoming[0].Detail, "next step": got.Upcoming[0].NextStep,
				"attention reason": got.Upcoming[0].AttentionReason, "preview": got.Upcoming[0].Context.Preview, "outcome": got.Upcoming[0].Outcome.Summary,
				"paragraph": got.Narrative[0].Text, "weekly recap": got.WeeklyRecap[0].Text, "answer": got.Cards[0].Message,
				"offer request": got.Offers[0].Prompt, "offer text": got.Offers[0].Text, "evidence label": got.Evidence[0].Label,
				"attendance": got.MeetingAttendance[0].Reason, "generation message": got.Generation.Message,
			} {
				if value != want {
					t.Errorf("%s = %q, want %q", field, value, want)
				}
			}
			label := "Plan at " + briefShortClock(9, 0, language)
			if got.Cards[0].Label != label || got.Offers[0].Label != label {
				t.Fatalf("chip labels: %+v, %+v", got.Cards, got.Offers)
			}
			if got.Upcoming[0].ID != item.ID || got.Upcoming[0].At != item.At || got.Upcoming[0].URL != item.URL || got.Evidence[0].SourceID != "source-9am" {
				t.Fatal("display normalization touched machine fields")
			}
			after, _ := json.Marshal(brief)
			if string(before) != string(after) || !reflect.DeepEqual(mobileBriefWithDisplayTimes(got, language), got) {
				t.Fatal("normalization mutated the stored input or was not idempotent")
			}
			// Exercise generation, including the shape branch that formerly
			// bypassed the upcoming-meeting normalizer.
			in := mobileBriefInput{Slot: mobileBriefSlotOnRequest, Language: language, AsOf: mobileBriefAsOfTime(brief), Location: time.UTC,
				Meetings: []mobileBriefCandidate{{SourceKind: mobileBriefDayShapeSource, SourceID: "shape", Kind: "day_shape", Title: "The shape of today", Detail: raw}}}
			built := buildMobileBriefV1(in)
			if len(built.Sections.Meetings) != 1 || built.Sections.Meetings[0].Detail != want {
				t.Fatalf("generated shape: %+v", built.Sections.Meetings)
			}
			// Preparation owns the clock formatting in both locales. Once the
			// generated field is stored, readers return its exact prose.
			a, settings := newLinkTestApp(t)
			if err := settings.SetUIState(t.Context(), uiLanguageStateKey, language); err != nil {
				t.Fatal(err)
			}
			store := newMobileBriefStore(t.TempDir())
			if err := store.save(built, mobileBriefAsOfTime(brief)); err != nil {
				t.Fatal(err)
			}
			stored, found := store.latest()
			if !found {
				t.Fatal("generated shape was not persisted")
			}
			read := a.mobileBriefWithCardsFrom(t.Context(), stored, &todayChatSources{})
			if read.Sections.Meetings[0].Detail != stored.Sections.Meetings[0].Detail || read.Sections.Meetings[0].Detail != want {
				t.Fatalf("prepared prose changed after storage: %+v", read.Sections.Meetings)
			}
			read = a.mobileBriefWithCardsFrom(t.Context(), brief, &todayChatSources{})
			if read.Sections.Meetings[0].Detail != want {
				t.Fatalf("retained legacy shape: %+v", read.Sections.Meetings)
			}
		})
	}
}

func TestBriefClockNormalizationKeepsOnlyValidClosingAnswers(t *testing.T) {
	brief := closingAnswerFixture(t, []mobileBriefCardWords{{Label: "Draft the note to Sam", Message: "Yes, draft the note to Sam about the shoe insert follow-up."}})
	brief.Narrative[len(brief.Narrative)-1].Text = "Want me to draft the note to Sam at 9am?"
	digest := mobileBriefClosingDigest(brief)
	brief.Cards = []MobileBriefCard{
		{ID: "answer", Kind: mobileBriefCardAction, Mode: mobileBriefCardSend, Label: "Draft for 9am", Message: "Draft the note to Sam at 9am.",
			Refs: mobileBriefClosingParagraph(brief).Refs, Writer: mobileBriefCardWriterModel, AnswerTo: digest, Facts: digest},
		{ID: "stale", Kind: mobileBriefCardAction, Mode: mobileBriefCardSend, Label: "Other", Message: "Other", AnswerTo: "stale", Facts: "stale"},
	}
	got := mobileBriefWithDisplayTimes(brief, "en")
	if !mobileBriefAnswerMatches(got, got.Cards[0]) || mobileBriefAnswerMatches(got, got.Cards[1]) || got.Cards[0].Message != "Draft the note to Sam at 9:00 AM." {
		t.Fatalf("answer binding lost or stale answer promoted: %+v", got.Cards)
	}
}
