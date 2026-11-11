package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNormalizeBriefText(t *testing.T) {
	near := time.Date(2026, 10, 17, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, input, language, want string
		spoken                      bool
		reference                   time.Time
	}{
		{"hour", "At 6:00 PM.", "en", "At 6pm.", true, near},
		{"half hour", "At 1:30 PM tomorrow", "en", "At 1:30pm tomorrow", true, near},
		{"seconds with period", "At 06:00:42 PM", "en", "At 6:00 PM", false, near},
		{"bare numbers", "At 01:30 then 13:30", "en", "At 01:30 then 13:30", false, near},
		{"aspect ratio", "Use 16:10 displays with 16:09 content", "en", "Use 16:10 displays with 16:09 content", true, near},
		{"media offsets", "Recording 1:05 to 1:10", "en", "Recording 1:05 to 1:10", true, near},
		{"zh source numbers", "使用16:10显示器，录音1:05到1:10", "zh-CN", "使用16:10显示器，录音1:05到1:10", true, near},
		{"midnight and noon", "12:00 AM, 12:00 PM", "en", "12am, 12pm", true, near},
		{"short already", "6pm and 1:30pm", "en", "6pm and 1:30pm", true, near},
		{"invalid hour", "25:00 and 00:00 PM", "en", "25:00 and 00:00 PM", true, near},
		{"near date", "Thu Oct 15", "en", "Thursday", true, near},
		{"yesterday", "Fri Oct 16", "en", "Yesterday", true, near},
		{"weekday comma", "Friday, October 16", "en", "Yesterday", true, near},
		{"today mid-sentence", "Due Sat Oct 17, 6:00 PM", "en", "Due today, 6pm", true, near},
		{"tomorrow", "The review is Sun Oct 18", "en", "The review is tomorrow", true, near},
		{"week boundary", "Sat Oct 10", "en", "October 10", true, near},
		{"far date", "Fri Oct 16", "en", "October 16", true, near.AddDate(0, 0, 8)},
		{"explicit year", "Fri Oct 16, 2020", "en", "October 16, 2020", true, near},
		{"invalid date", "Feb 30", "en", "Feb 30", true, near},
		{"year boundary", "Thu Dec 31", "en", "Yesterday", true, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"unknown reference", "Fri Oct 16", "en", "Fri Oct 16", true, time.Time{}},
		{"range", "11:04 AM to 05:06 PM", "en", "from 11:04am to 5:06pm", true, near},
		{"range from", "from 11:04 AM to 05:06 PM", "en", "from 11:04am to 5:06pm", true, near},
		{"range half hour", "1:30 PM to 2:45 PM", "en", "from 1:30pm to 2:45pm", true, near},
		{"short range", "11:04 AM to 11:06 AM", "en", "from 11:04am to 11:06am", true, near},
		{"written range exact", "11:04 AM to 05:06 PM", "en", "11:04 AM to 5:06 PM", false, near},
		{"duration", "1 h 33 min", "en", "an hour and 33 minutes", true, near},
		{"duration quarter", "2 h 15 min", "en", "two and a quarter hours", true, near},
		{"duration minutes", "90 min", "en", "an hour and a half", true, near},
		{"duration exact", "1 h 22 min", "en", "an hour and 22 minutes", true, near},
		{"duration zero", "0 h 0 min", "en", "zero minutes", true, near},
		{"duration singular", "1 min and 1 h", "en", "a minute and an hour", true, near},
		{"decimal duration", "1.5 hours", "en", "1.5 hours", true, near},
		{"fractional duration", "1/2 h", "en", "1/2 h", true, near},
		{"grouped duration", "1,005 h", "en", "1,005 h", true, near},
		{"zh hour", "6:00 PM", "zh-CN", "下午6点", true, near},
		{"zh half hour", "1:30 PM", "zh-CN", "下午1点半", true, near},
		{"zh minutes", "下午1:04:32", "zh-CN", "13:04", false, near},
		{"zh native", "下午6点，下午1点半", "zh-CN", "下午6点，下午1点半", true, near},
		{"zh qualified colon", "下午6:00，下午1:30", "zh-CN", "下午6点，下午1点半", true, near},
		{"zh noon midnight", "12:00 PM, 12:00 AM", "zh-CN", "中午12点, 凌晨12点", true, near},
		{"zh range", "11:04 AM to 05:06 PM", "zh-CN", "从上午11点4分到下午5点6分", true, near},
		{"zh native range", "从上午11点4分到下午5点6分", "zh-CN", "从上午11点4分到下午5点6分", true, near},
		{"zh duration", "1 h 33 min", "zh-CN", "1小时33分钟", true, near},
		{"zh native duration", "1小时33分钟", "zh-CN", "1小时33分钟", true, near},
		{"zh decimal duration", "1.5小时", "zh-CN", "1.5小时", true, near},
		{"zh near date", "Thu Oct 15", "zh-CN", "周四", true, near},
		{"previous week", "Submitted Mon Oct 12", "en", "Submitted October 12", true, near.AddDate(0, 0, 2)},
		{"last weekday", "Submitted Thu Oct 15", "en", "Submitted last Thursday", true, time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)},
		{"no on before yesterday", "Shipped on Fri Oct 16", "en", "Shipped yesterday", true, near},
		{"on before a weekday stays", "Ship on Thu Oct 15", "en", "Ship on Thursday", true, near},
		{"zh yesterday", "Fri Oct 16", "zh-CN", "昨天", true, near},
		{"zh today", "Sat Oct 17", "zh-CN", "今天", true, near},
		{"zh far date", "Fri Oct 16", "zh-CN", "10月16日", true, near.AddDate(0, 0, 8)},
		{"machine timestamp", "2026-10-16T18:00:00Z", "en", "2026-10-16T18:00:00Z", true, near},
		{"machine offset", "2026-10-16T18:00:00-07:00; at 6:00 PM", "en", "2026-10-16T18:00:00-07:00; at 6pm", true, near},
		{"url", "https://example.com/18:00 at 6:00 PM", "en", "https://example.com/18:00 at 6:00 PM", false, near},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeBriefText(tc.input, tc.language, tc.reference, tc.spoken)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if again := normalizeBriefText(got, tc.language, tc.reference, tc.spoken); again != got {
				t.Fatalf("normalizer is not idempotent: %q -> %q", got, again)
			}
		})
	}
}

func TestBriefSpeechPreparationAndSynthesisNormalizeTimes(t *testing.T) {
	for _, language := range []string{"en", "zh-CN"} {
		t.Run(language, func(t *testing.T) {
			brief := MobileBriefV1{AsOf: "2026-10-17T18:00:00Z", Timezone: "UTC", NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "Fri Oct 16, 6:00 PM; 1:30 PM. 1 h 33 min. 11:04 AM to 05:06 PM."}}}
			var received []string
			speaker := fakeSpeaker(language, testWAV(1), nil)
			speaker.synthesize = func(_ context.Context, _, text string) ([]byte, error) {
				received = append(received, text)
				return testWAV(1), nil
			}
			brief.BriefID = "2026-10-17-evening"
			if _, ok, reason := renderMobileBriefAudio(t.Context(), speaker, t.TempDir(), brief); !ok {
				t.Fatal(reason)
			}
			// The voice says clocks as words (R68, mobileBriefSpokenClocks); the screen keeps digits.
			want := "Yesterday, 6pm; one thirty in the afternoon. an hour and 33 minutes. from eleven oh four in the morning to five oh six in the evening."
			if briefChinese(language) {
				want = "昨天, 下午6点; 下午1点半. 1小时33分钟. 从上午11点4分到下午5点6分."
			}
			if got := strings.Join(received, " "); got != want {
				t.Fatalf("synthesizer received %q, want %q", got, want)
			}
		})
	}
	clean := stripMarkdownForSpeech("**Prepare at 6:00 PM**, then allow 1 h 33 min.")
	if strings.Contains(clean, "PM") || !strings.Contains(clean, "6pm") || !strings.Contains(clean, "an hour and 33 minutes") {
		t.Fatalf("read-aloud cleanup lost natural speech: %q", clean)
	}
}

func TestNormalizeBriefActivityText(t *testing.T) {
	for _, tc := range []struct{ input, language, want string }{
		{"11:04 AM to 05:06 PM; 1 h 33 min", "en", "from 11am to 5pm; an hour and a half"},
		{"11:04 AM to 05:06 PM; 1 h 33 min", "zh-CN", "从上午11点到下午5点; 一个半小时"},
		{"从上午11点4分到下午5点6分；1小时33分钟", "zh-CN", "从上午11点到下午5点；一个半小时"},
		{"11:04 AM to 11:06 AM; 1 h 22 min", "en", "from 11:04am to 11:06am; an hour and 22 minutes"},
	} {
		if got := normalizeBriefActivityText(tc.input, tc.language, time.Time{}); got != tc.want {
			t.Errorf("activity %q (%s): got %q, want %q", tc.input, tc.language, got, tc.want)
		}
	}
}

func TestBriefClockMetadataUsesSharedExactTimes(t *testing.T) {
	for _, tc := range []struct{ layout, want string }{
		{"3:04pm", "Sat Oct 17, 1:04 PM"},
		{"15:04", "Sat Oct 17, 13:04"},
	} {
		if got := mobileBriefLocalTime("2026-10-17T13:04:32Z", time.UTC, tc.layout); got != tc.want {
			t.Errorf("known metadata %s: got %q, want %q", tc.layout, got, tc.want)
		}
	}
	start := time.Date(2026, 10, 17, 11, 4, 32, 0, time.UTC)
	end := time.Date(2026, 10, 17, 17, 6, 42, 0, time.UTC)
	if got := mobileBriefTimeRange(start.UnixMilli(), end.UnixMilli(), nil, "15:04"); got != "11:04 AM to 5:06 PM" {
		t.Fatalf("known range metadata: %q", got)
	}
}

func TestBriefParagraphSpeechSaysDurationsExactly(t *testing.T) {
	brief := MobileBriefV1{
		AsOf: "2026-10-17T18:00:00Z",
		Evidence: []MobileBriefRef{
			{Ref: "e1", SourceKind: mobileBriefRecapSource, SourceID: "recap", Label: "Observed work"},
			{Ref: "planned", SourceKind: mobileBriefCalendarSource, SourceID: "meeting", Label: "Interview"},
		},
		Sections: MobileBriefSections{
			Moved:    []MobileBriefItem{{Title: "Observed work", Kind: "recap", Status: "done", Refs: []string{"e1"}}},
			Meetings: []MobileBriefItem{{Title: "Interview", At: "2026-10-18T11:05:00Z", Status: "upcoming", Refs: []string{"planned"}}},
		},
	}
	for _, language := range []string{"en", "zh-CN"} {
		for _, refs := range [][]string{{"e1"}, {"e1", "planned"}, {"planned"}, nil} {
			paragraph := MobileBriefPara{Text: "1 h 33 min", Refs: refs}
			want := "an hour and 33 minutes"
			if briefChinese(language) {
				want = "1小时33分钟"
			}
			// An observed recap is activity only (mobile_brief_anchor.go): it
			// stays under "What you worked on" and is never a Highlight of the
			// brief text, so no paragraph about it rounds its duration.
			if got := mobileBriefParagraphSpeech(brief, paragraph, language); got != want {
				t.Errorf("refs %v (%s): got %q, want %q", refs, language, got, want)
			}
		}
	}
}
