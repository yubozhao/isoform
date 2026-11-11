package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMobileBriefBudgetFixtureDays(t *testing.T) {
	for _, language := range []string{"en", "zh-CN"} {
		t.Run(language, func(t *testing.T) {
			brief := mobileBriefVoiceFixture()
			sections := mobileBriefNarrativeSections(brief)
			var forward []MobileBriefPara
			var highlightRef string
			for _, section := range sections {
				if len(section.items) == 0 {
					continue
				}
				if section.name == "Highlights" {
					highlightRef = section.items[0].Refs[0]
					continue
				}
				text := strings.TrimSpace(strings.Repeat("Review the proposed scope before choosing your next step. ", 10))
				if briefChinese(language) {
					text = "Next scope check " + strings.Repeat("审阅计划后再决定下一步。", 16)
				}
				forward = append(forward, MobileBriefPara{Text: text, Refs: section.items[0].Refs})
			}
			if len(forward) != 3 || highlightRef == "" {
				t.Fatal("fixture must exercise three forward groups plus Highlights")
			}
			line := "Progress on the proposed scope and the options for the next release."
			if briefChinese(language) {
				line = "下一次发布计划和备选方案的工作取得了进展。"
			}
			original := append(append([]MobileBriefPara{}, forward...), MobileBriefPara{
				Text: "Highlights: " + strings.Join([]string{line, line, line}, "\n"), Refs: []string{highlightRef},
			})
			raw, err := json.Marshal(struct {
				Paragraphs []MobileBriefPara `json:"paragraphs"`
			}{original})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseMobileBriefNarrative(string(raw), mobileBriefNarrativeInputEvidence(brief))
			if err != nil {
				t.Fatal(err)
			}
			// The dense day starts above the combined cap, within each wire
			// paragraph's byte bound, and should lose only retrospective lines.
			if mobileBriefNarrativeWords(parsed) <= mobileBriefMaxWords {
				t.Fatalf("dense fixture does not exceed budget: %d", mobileBriefNarrativeWords(parsed))
			}
			got := prepareMobileBriefNarrative(brief, parsed, language)
			words := mobileBriefNarrativeWords(got)
			if words < mobileBriefMinWords || words > mobileBriefMaxWords {
				t.Fatalf("dense day is %d words: %+v", words, got)
			}
			if !reflect.DeepEqual(got[:3], forward) {
				t.Fatal("trimming changed a forward paragraph")
			}
			if err := validateMobileBriefNarrativeVoice(brief, got, language); err != nil {
				t.Fatalf("budgeted day failed voice validation: %v", err)
			}
			if !reflect.DeepEqual(parsed, original) {
				t.Fatal("trimming mutated the original document")
			}
			brief.Narrative = original
			if spoken := mobileBriefSpokenText(brief, language); spoken == "" && brief.NarrativeStatus == "ok" {
				t.Fatal("shown model text was not spoken")
			}
			prompt := mobileBriefTestPrompt(brief, language)
			if !strings.Contains(prompt, "150 to 300 words") && !strings.Contains(prompt, "two sentences") {
				t.Fatal("prompt omitted the combined reading budget")
			}
		})
	}
}

func TestMobileBriefBudgetOversizedForwardUsesWholeTemplate(t *testing.T) {
	brief := mobileBriefVoiceFixture()
	original := []MobileBriefPara{{Text: strings.Repeat("Review the proposed scope. ", 150), Refs: brief.Sections.Decide[0].Refs}}
	for _, language := range []string{"en", "zh-CN"} {
		got := prepareMobileBriefNarrative(brief, original, language)
		// Over-long model text is trimmed to whole sentences within the limit (design R74 v1.0.1):
		// never cut mid-sentence, never replaced by the template.
		if len(got) == 0 || mobileBriefNarrativeWords(got) > mobileBriefMaxWords {
			t.Fatalf("oversized model text was not trimmed to whole sentences: %+v", got)
		}
		for _, p := range got {
			if len(p.Text) > mobileBriefNarrativeMaxBytes || !strings.HasSuffix(p.Text, ".") {
				t.Fatalf("trimmed paragraph is over the limit or cut mid-sentence: %q", p.Text)
			}
		}
		if err := validateMobileBriefNarrativeVoice(brief, got, language); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMobileBriefBudgetTemplatesAndSparseDays(t *testing.T) {
	for name, brief := range map[string]MobileBriefV1{
		"morning": buildMobileBriefV1(mobileBriefGoldenInput()),
		"evening": mobileBriefVoiceFixture(),
		"empty":   {},
	} {
		for _, language := range []string{"en", "zh-CN"} {
			t.Run(name+"/"+language, func(t *testing.T) {
				got := mobileBriefNarrativeTemplate(brief, language)
				if mobileBriefNarrativeWords(got) > mobileBriefMaxWords {
					t.Fatal("fallback exceeds reading budget")
				}
				if name == "empty" && (len(got) != 1 || len(got[0].Text) == 0) {
					t.Fatalf("empty day should open with exactly its one sentence (design R74): %+v", got)
				}
				if err := validateMobileBriefNarrativeVoice(brief, got, language); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestMobileBriefBudgetSpeechExpansionAndEmptyLegacyParagraphs(t *testing.T) {
	brief := mobileBriefVoiceFixture()
	brief.NarrativeStatus = "ok" // only the model's ok text is spoken (design R74)
	// The written form fits; spoken duration words expand beyond the cap.
	refs := brief.Sections.Decide[0].Refs
	brief.Narrative = []MobileBriefPara{
		{Text: strings.Repeat("1 h 33 min. ", 22), Refs: refs},
		{Text: strings.Repeat("1 h 33 min. ", 22), Refs: refs},
		{Text: strings.Repeat("1 h 33 min. ", 22), Refs: refs},
	}
	// Spoken expansion must not drop the written paragraphs.
	if got := mobileBriefSpokenText(brief, "en"); len(mobileBriefSpokenSegments(got)) != len(brief.Narrative) {
		t.Fatalf("spoken expansion omitted paragraphs: %q", got)
	}
	brief.Narrative = []MobileBriefPara{{Text: " \n "}}
	if got := mobileBriefSpokenText(brief, "en"); got != "" {
		t.Fatalf("a template fallback was spoken (design R74): %q", got)
	}
	// Localization must not break the existing per-paragraph byte contract.
	// "6 PM" is written "18:00" in Chinese, one byte longer.
	brief.Narrative = []MobileBriefPara{{Text: strings.Repeat("6 PM ", 110), Refs: refs}}
	if len(brief.Narrative[0].Text) > mobileBriefNarrativeMaxBytes {
		t.Fatal("fixture must fit the original byte bound")
	}
	// Over-long localized text is trimmed to whole sentences and kept, not reported unavailable (design R74 v1.0.1).
	if out, _ := fitMobileBriefNarrative(brief, brief.Narrative, "zh-CN"); len(out) == 0 {
		t.Fatal("over-long localized text was dropped instead of trimmed")
	}
	for _, paragraph := range prepareMobileBriefNarrative(brief, brief.Narrative, "zh-CN") {
		if len(paragraph.Text) > mobileBriefNarrativeMaxBytes {
			t.Fatal("localized narrative exceeded the wire byte limit")
		}
	}
}
