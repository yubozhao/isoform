package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Exercise the resolved prompt and parser through the narrative caller used
// during generation, retaining the target's sanitization before voice repair.
func TestTopicNamesIntegrationNarrativeCallers(t *testing.T) {
	for _, language := range []string{"en", "zh-CN"} {
		t.Run(language, func(t *testing.T) {
			at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
			brief := MobileBriefV1{Slot: mobileBriefSlotOnRequest, AsOf: "2026-10-23T07:00:00Z", Timezone: "UTC",
				Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: mobileBriefCalendarSource, SourceID: "review"}, {Ref: "e2", SourceKind: "principal", SourceID: "dana", Label: "Dana"}},
				Sections: MobileBriefSections{Meetings: []MobileBriefItem{{Title: "Dana review", Kind: "meeting", Status: "upcoming",
					At: at.Format(time.RFC3339), EndAt: at.Add(time.Hour).Format(time.RFC3339), Refs: []string{"e1", "e2"}}}}}
			prompt := mobileBriefTestPrompt(brief, language)
			for _, rule := range []string{"every generated field", briefShortClock(9, 0, language), "copy a time still ahead using the same time as the day line", "give no clock time for anything past or on now"} {
				if !strings.Contains(prompt, rule) {
					t.Fatalf("resolved prompt lost rule %q", rule)
				}
			}
			read := func(text string) string {
				t.Helper()
				raw, err := json.Marshal(mobileBriefNarrativeReply{Paragraphs: []mobileBriefNarrativeReplyParagraph{{Text: text, Refs: []string{"e1"}}}})
				if err != nil {
					t.Fatal(err)
				}
				paras, _, err := mobileBriefNarrativeFromReply(brief, mobileBriefPicks(brief), string(raw), language)
				if err != nil || len(paras) != 1 {
					t.Fatalf("narrative caller: paragraphs=%d err=%v", len(paras), err)
				}
				return paras[0].Text
			}
			normal := "Dana review starts at " + briefShortClock(9, 0, language) + "."
			if got := read(normal); got != normal {
				t.Fatalf("normal generated prose changed: %q", got)
			}
			secret := "sk-proj-" + strings.Repeat("a", 48)
			if got := read("The review code is " + secret + "."); strings.Contains(got, secret) || !strings.Contains(got, "[REDACTED") {
				t.Fatal("resolved narrative caller bypassed source sanitization")
			}
		})
	}
}
