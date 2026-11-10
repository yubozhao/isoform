package main

import (
	"context"
	"log"
	"strings"
	"testing"
	"time"
)

func mobileBriefVoiceFixture() MobileBriefV1 {
	asOf := time.Date(2026, 10, 16, 17, 0, 0, 0, time.UTC)
	return buildMobileBriefV1(mobileBriefInput{
		Slot: mobileBriefSlotEvening, WindowStart: asOf.Add(-10 * time.Hour), AsOf: asOf,
		Location: time.UTC, Timezone: "UTC",
		Moved: []mobileBriefCandidate{{
			SourceKind: "history", SourceID: "review", Title: "Pricing draft at 9:15 AM",
			Status: "needs_review", Detail: "Private draft text at 09:15", At: asOf.Add(-7 * time.Hour),
		}, {
			SourceKind: "history", SourceID: "outcome", Title: "Sent the pricing proposal at 10:00 AM",
			Status: "verified", At: asOf.Add(-6 * time.Hour),
		}},
		OpenLoops: []mobileBriefCandidate{{
			SourceKind: "purpose", SourceID: "pricing", Title: "Agree on the pricing plan",
			Kind: "commitment", Status: "active", Detail: "Private waiting condition", At: asOf.Add(-time.Hour),
		}},
		Meetings: []mobileBriefCandidate{{
			SourceKind: mobileBriefCalendarSource, SourceID: "design", Title: "Design review",
			Status: "upcoming", Kind: "meeting", At: asOf.Add(time.Hour),
		}, {
			SourceKind: mobileBriefCalendarSource, SourceID: "past", Title: "Team sync at 11:30 AM",
			Status: "done", Kind: "meeting", At: asOf.Add(-6 * time.Hour),
		}},
	})
}

func TestMobileBriefVoiceFixtureOmitsPastClockTimes(t *testing.T) {
	brief := mobileBriefVoiceFixture()
	prompt := mobileBriefTestPrompt(brief, "English")
	paragraphs := mobileBriefNarrativeTemplate(brief, "English")
	if len(paragraphs) != 4 {
		t.Fatalf("template paragraphs = %d, want forward work, Highlights, then closing offers", len(paragraphs))
	}
	// The hold needs the current reference as machine metadata. Keep checking
	// every source clock and every paragraph for past-time narration.
	text := strings.Replace(prompt, "asOf="+brief.AsOf, "asOf=<current reference>", 1)
	for _, paragraph := range paragraphs {
		text += "\n" + paragraph.Text
		if len(paragraph.Refs) == 0 {
			t.Fatal("a template paragraph must carry evidence")
		}
	}
	for _, past := range []string{"9:15 AM", "09:15", "11:30 AM", "11:30", "10:00 AM", "4:00 PM", "5:00 PM", "T17:00:00Z"} {
		if strings.Contains(text, past) {
			t.Errorf("brief voice carries past clock time %q", past)
		}
	}
	// The design review is later today: a clock, never the brief's own weekday.
	if !strings.Contains(text, "at 6:00 PM") || strings.Contains(text, "Friday") {
		t.Error("future preparation time must stay in the reader's local format")
	}
	for _, held := range []string{"Private draft text", "Private waiting condition"} {
		if strings.Contains(text, held) {
			t.Errorf("item details entered the voice: %q", held)
		}
	}
	if err := validateMobileBriefNarrativeVoice(brief, paragraphs, "English"); err != nil {
		t.Fatalf("the template violates its voice guard: %v", err)
	}
}

func TestMobileBriefNarrativePromptChiefOfStaffRulesAndOrder(t *testing.T) {
	prompt := mobileBriefTestPrompt(mobileBriefVoiceFixture(), "English")
	for _, rule := range []string{
		"chief of staff", `Write as "I" to "you"`, "computer and phone",
		"give no clock time for anything past or on now", "never write 9am, 9:00am or 12:30pm", `never "I saw", "I noticed" or "I can't tell"`,
		"no lists, headings, markdown, quotation marks, emoji or exclamation marks", "never guess beyond the facts given",
		"| Design review | at 6:00 PM",
	} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("prompt is missing rule %q", rule)
		}
	}
}

func TestMobileBriefNarrativeVoiceOnlyAllowsCitedFutureClocks(t *testing.T) {
	brief := mobileBriefVoiceFixture()
	var futureRef, pastRef string
	for _, ref := range brief.Evidence {
		switch ref.SourceID {
		case "design":
			futureRef = ref.Ref
		case "review":
			pastRef = ref.Ref
		}
	}
	for _, tc := range []struct {
		name, text, ref string
		valid           bool
	}{
		{"future", "You have Design review at 6:00 PM. Prepare what you need.", futureRef, true},
		{"no clock", "Review your pricing draft before you choose the next step.", pastRef, true},
		{"past", "Your pricing draft was ready at 9:15 AM.", pastRef, false},
		{"wrong ref", "You have Design review at 6:00 PM.", pastRef, false},
		{"invented time", "You have Design review at 7:00 PM.", futureRef, false},
		{"unsupported format", "You have Design review at 18:00.", futureRef, false},
		{"exclamation", "Review your pricing draft!", pastRef, false},
		{"platform name", "Review the draft on your Mac.", pastRef, false},
		{"decorative symbol", "Review your pricing draft \u2726", pastRef, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMobileBriefNarrativeVoice(brief, []MobileBriefPara{{Text: tc.text, Refs: []string{tc.ref}}}, "English")
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}

func TestMobileBriefOwnVoiceRewritesOnlyDeviceReferences(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"on your Mac", "Open it on your Mac.", "Open it on your computer."},
		{"this MacBook Pro", "this MacBook Pro", "this computer"},
		{"your iPhone", "your iPhone", "your phone"},
		{"this Mac", "this Mac", "this computer"},
		{"my MacBook", "my MacBook", "my computer"},
		{"the user's MacBook Air", "the user's MacBook Air", "the user's computer"},
		{"your iMac", "your iMac", "your computer"},
		{"your Mac mini", "Your Mac mini is asleep.", "Your computer is asleep."},
		{"your Mac Pro", "Your Mac Pro is ready.", "Your computer is ready."},
		{"your Mac Studio", "Your Mac Studio is ready.", "Your computer is ready."},
		{"Mac Air work name", "Your Mac Air is ready.", "Your Mac Air is ready."},
		{"Mac app work name", "Your Mac App Store update", "Your Mac App Store update"},
		{"Mac Store work name", "Your Mac Store update", "Your Mac Store update"},
		{"iPhone app work name", "your iPhone app", "your iPhone app"},
		{"Mac version work name", "your Mac 2 is public", "your Mac 2 is public"},
		{"MacBook version work name", "your MacBook Pro 1.0.41", "your MacBook Pro 1.0.41"},
		{"Mac mini version work name", "your Mac mini 1.0.41", "your Mac mini 1.0.41"},
		{"Mac Pro version work name", "your Mac Pro 1.0.41", "your Mac Pro 1.0.41"},
		{"Mac Studio version work name", "your Mac Studio 1.0.41", "your Mac Studio 1.0.41"},
		{"Chinese computer", "你的 Mac", "你的电脑"},
		{"Chinese computer in a sentence", "在你的 MacBook Pro 上打开。", "在你的电脑上打开。"},
		{"Chinese Mac computer", "你的Mac电脑", "你的电脑"},
		{"Chinese Mac mini", "你的Mac mini睡着了。", "你的电脑睡着了。"},
		{"Chinese release name", "你的 Mac 1.0.41", "你的 Mac 1.0.41"},
		{"Chinese phone", "你的 iPhone", "你的手机"},
		{"release name", "Mac 1.0.41 is public", "Mac 1.0.41 is public"},
		{"app name", "the iPhone app", "the iPhone app"},
		{"platform name", "macOS 15", "macOS 15"},
		{"store name", "Mac App Store", "Mac App Store"},
		{"straight quoted title", `The document "Your Mac setup" is ready.`, `The document "Your Mac setup" is ready.`},
		{"curly quoted title", "The document “Your Mac setup. Guide” is ready.", "The document “Your Mac setup. Guide” is ready."},
		{"unclean quotation", `The document "Your Mac setup is ready.`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mobileBriefOwnVoiceText(tc.input); got != tc.want {
				t.Fatalf("mobileBriefOwnVoiceText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}

	brief := MobileBriefV1{
		Evidence: []MobileBriefRef{{Ref: "e1", Label: "Your Mac setup"}},
		Sections: MobileBriefSections{Moved: []MobileBriefItem{{Title: "Your Mac setup", Refs: []string{"e1"}}}},
	}
	protected := mobileBriefQuotedContent(brief)
	input := "Review Your Mac setup on your Mac. The document “Your Mac setup. Guide” is ready."
	want := "Review Your Mac setup on your computer. The document “Your Mac setup. Guide” is ready."
	if got := mobileBriefOwnVoiceText(input, protected...); got != want {
		t.Fatalf("source title was not preserved: got %q, want %q", got, want)
	}
}

func TestMobileBriefOwnVoiceProtectedTitleMatchesCaseInsensitive(t *testing.T) {
	for _, tc := range []struct{ name, title, input, want string }{
		{"lower initial", "Your Mac setup", "Review your Mac setup on your Mac.", "Review your Mac setup on your computer."},
		{"upper device", "Your Mac setup", "Review YOUR MAC SETUP on your Mac.", "Review YOUR MAC SETUP on your computer."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mobileBriefOwnVoiceText(tc.input, tc.title); got != tc.want {
				t.Fatalf("mobileBriefOwnVoiceText(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestMobileBriefOwnVoiceSentenceSplitOddInput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		text      string
		protected []string
		wantVoice string
	}{
		{"invalid byte before span", "Before \xff. \"Your Mac. setup\" on your Mac. After", []string{"Your Mac. setup"}, "your computer"},
		{"invalid byte in span", "\"Your Mac \xff. setup\" on your Mac.", []string{"Your Mac \xff. setup"}, "your computer"},
		{"truncated encoding in span", "\"Your Mac \xe2\x82. setup\" on your Mac.", []string{"Your Mac \xe2\x82. setup"}, "your computer"},
		{"Chinese sentence in span", "“你的 Mac。设置” 在你的 Mac 上。", []string{"你的 Mac。设置"}, "你的电脑"},
		{"newline in span", "\"Your Mac\nsetup\" on your Mac. Next", []string{"Your Mac\nsetup"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentences := splitMobileBriefOwnVoiceSentences(tc.text, tc.protected)
			if got := strings.Join(sentences, ""); got != tc.text {
				t.Fatalf("sentence slices changed the input: %q, want %q", got, tc.text)
			}
			if tc.wantVoice != "" {
				if got := mobileBriefOwnVoiceText(tc.text, tc.protected...); !strings.Contains(got, tc.wantVoice) {
					t.Fatalf("rewritten odd input = %q, want it to contain %q", got, tc.wantVoice)
				}
			}
		})
	}
}

func TestMobileBriefWithOwnVoiceCoversWeeklyAndComputedLines(t *testing.T) {
	brief := MobileBriefV1{
		Narrative: []MobileBriefPara{{Text: "Open it on your Mac.", Hold: &TodayDayShapeHold{Label: "Open on your Mac"}}},
		WeekLead:  "Open it on your Mac.", WeekLeadFacts: "facts",
		Generation: &MobileBriefGeneration{Message: "Open it on your Mac."},
		Audio:      &MobileBriefAudio{StatusMessage: "Open it on your Mac."},
		DayShape: &MobileBriefDay{TodayDayShape: &TodayDayShape{
			Hold: &TodayDayShapeHold{Label: "Open on your Mac"},
		}},
		YourWeek: &MobileBriefWeek{
			Headline: "Open it on your Mac.",
			Open:     []MobileBriefWeekOpen{{Title: "Your Mac setup", Label: "Open on your Mac", CardID: "card"}},
			Learned: []MobileBriefWeekLearned{
				{Kind: yourWeekLearnedMute, Text: "Open it on your Mac."},
				{Kind: yourWeekLearnedMemory, Text: "Your Mac setup"},
			},
			Ask: &MobileBriefWeekAsk{Text: "Open it on your Mac?", Message: "Open it on your iPhone.", Hold: &TodayDayShapeHold{Label: "Open on your Mac"}},
		},
		WeeklyRecap: []MobileBriefPara{
			{Role: yourWeekParagraphLead, Text: "Open it on your Mac.", Hold: &TodayDayShapeHold{Label: "Open on your Mac"}},
			{Role: yourWeekParagraphLearned, Text: "Open it on your Mac."},
			{Role: yourWeekParagraphLearned, Text: "Your Mac setup"},
			{Role: yourWeekParagraphAsk, Text: "Open it on your Mac?"},
		},
		Sections: MobileBriefSections{Moved: []MobileBriefItem{{
			Title: "Your Mac setup", Detail: "Open it on your Mac.", NextStep: "Open it on your iPhone.",
			AttentionReason: "Open it on your Mac.",
			Outcome: &ActivityThreadOutcome{
				Summary: "Open it on your Mac.", NextStep: "Open it on your iPhone.", Question: "Open it on your Mac?", UserActed: "Open it on your Mac.",
			},
		}}},
		MeetingAttendance: []MobileBriefAttendance{{Reason: "Open it on your Mac."}},
	}
	got := mobileBriefWithOwnVoice(brief)
	item := got.Sections.Moved[0]
	for _, tc := range []struct{ name, got, want string }{
		{"narrative", got.Narrative[0].Text, "Open it on your computer."},
		{"narrative hold", got.Narrative[0].Hold.Label, "Open on your computer"},
		{"audio status", got.Audio.StatusMessage, "Open it on your computer."},
		{"day hold", got.DayShape.Hold.Label, "Open on your computer"},
		{"week lead", got.WeekLead, "Open it on your computer."},
		{"week headline", got.YourWeek.Headline, "Open it on your computer."},
		{"week open title", got.YourWeek.Open[0].Title, "Your Mac setup"},
		{"week open chip", got.YourWeek.Open[0].Label, "Open on your computer"},
		{"week mute line", got.YourWeek.Learned[0].Text, "Open it on your computer."},
		{"week memory title", got.YourWeek.Learned[1].Text, "Your Mac setup"},
		{"week ask", got.YourWeek.Ask.Text, "Open it on your computer?"},
		{"week ask message", got.YourWeek.Ask.Message, "Open it on your phone."},
		{"week ask hold", got.YourWeek.Ask.Hold.Label, "Open on your computer"},
		{"weekly lead", got.WeeklyRecap[0].Text, "Open it on your computer."},
		{"weekly lead hold", got.WeeklyRecap[0].Hold.Label, "Open on your computer"},
		{"weekly mute line", got.WeeklyRecap[1].Text, "Open it on your computer."},
		{"weekly learned title", got.WeeklyRecap[2].Text, "Your Mac setup"},
		{"weekly ask", got.WeeklyRecap[3].Text, "Open it on your computer?"},
		{"item title", item.Title, "Your Mac setup"},
		{"item detail", item.Detail, "Open it on your computer."},
		{"item next step", item.NextStep, "Open it on your phone."},
		{"item attention", item.AttentionReason, "Open it on your computer."},
		{"outcome summary", item.Outcome.Summary, "Open it on your computer."},
		{"outcome next step", item.Outcome.NextStep, "Open it on your phone."},
		{"outcome question", item.Outcome.Question, "Open it on your computer?"},
		{"outcome user action", item.Outcome.UserActed, "Open it on your computer."},
		{"attendance reason", got.MeetingAttendance[0].Reason, "Open it on your computer."},
		{"generation message", got.Generation.Message, "Open it on your computer."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestMobileBriefWithOwnVoiceKeepsUncleanActions(t *testing.T) {
	const unclean = `Open "Your Mac Studio writer-only-marker`
	const clean = "Open it on your Mac."
	const rewritten = "Open it on your computer."
	for _, tc := range []struct {
		name, kind string
		brief      MobileBriefV1
		fields     func(MobileBriefV1) []string
		want       []string
	}{
		{
			name: "card label", kind: mobileBriefCardItem,
			brief: MobileBriefV1{Cards: []MobileBriefCard{{ID: "card", Kind: mobileBriefCardItem, Label: unclean, Message: clean}}},
			fields: func(got MobileBriefV1) []string {
				if len(got.Cards) != 1 {
					return nil
				}
				return []string{got.Cards[0].Label, got.Cards[0].Message}
			},
			want: []string{unclean, rewritten},
		},
		{
			name: "card message", kind: mobileBriefCardItem,
			brief: MobileBriefV1{Cards: []MobileBriefCard{{ID: "card", Kind: mobileBriefCardItem, Label: clean, Message: unclean}}},
			fields: func(got MobileBriefV1) []string {
				if len(got.Cards) != 1 {
					return nil
				}
				return []string{got.Cards[0].Label, got.Cards[0].Message}
			},
			want: []string{rewritten, unclean},
		},
		{
			name: "offer label", kind: "review",
			brief: MobileBriefV1{Offers: []MobileBriefOffer{{ID: "offer", Kind: "review", Label: unclean, Prompt: clean, Text: clean}}},
			fields: func(got MobileBriefV1) []string {
				if len(got.Offers) != 1 {
					return nil
				}
				return []string{got.Offers[0].Label, got.Offers[0].Prompt, got.Offers[0].Text}
			},
			want: []string{unclean, rewritten, rewritten},
		},
		{
			name: "offer prompt", kind: "review",
			brief: MobileBriefV1{Offers: []MobileBriefOffer{{ID: "offer", Kind: "review", Label: clean, Prompt: unclean, Text: clean}}},
			fields: func(got MobileBriefV1) []string {
				if len(got.Offers) != 1 {
					return nil
				}
				return []string{got.Offers[0].Label, got.Offers[0].Prompt, got.Offers[0].Text}
			},
			want: []string{rewritten, unclean, rewritten},
		},
		{
			name: "week open chip", kind: "week_open",
			brief: MobileBriefV1{YourWeek: &MobileBriefWeek{Open: []MobileBriefWeekOpen{{Title: "Launch", Label: unclean, CardID: "card"}}}},
			fields: func(got MobileBriefV1) []string {
				if got.YourWeek == nil || len(got.YourWeek.Open) != 1 {
					return nil
				}
				return []string{got.YourWeek.Open[0].Label, got.YourWeek.Open[0].CardID}
			},
			want: []string{unclean, "card"},
		},
		{
			name: "week ask", kind: "week_ask",
			brief: MobileBriefV1{YourWeek: &MobileBriefWeek{Ask: &MobileBriefWeekAsk{Text: unclean, Message: clean, CardID: "card"}}},
			fields: func(got MobileBriefV1) []string {
				if got.YourWeek == nil || got.YourWeek.Ask == nil {
					return nil
				}
				return []string{got.YourWeek.Ask.Text, got.YourWeek.Ask.Message, got.YourWeek.Ask.CardID}
			},
			want: []string{unclean, rewritten, "card"},
		},
		{
			name: "hold chip", kind: "hold",
			brief: MobileBriefV1{DayShape: &MobileBriefDay{TodayDayShape: &TodayDayShape{Hold: &TodayDayShapeHold{Label: unclean}}}},
			fields: func(got MobileBriefV1) []string {
				if got.DayShape == nil || got.DayShape.TodayDayShape == nil || got.DayShape.Hold == nil {
					return nil
				}
				return []string{got.DayShape.Hold.Label}
			},
			want: []string{unclean},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged strings.Builder
			previous := log.Writer()
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(previous) })

			fields := tc.fields(mobileBriefWithOwnVoice(tc.brief))
			if len(fields) != len(tc.want) {
				t.Fatalf("action fields = %q, want %q", fields, tc.want)
			}
			for i := range tc.want {
				if fields[i] != tc.want[i] {
					t.Errorf("field %d = %q, want %q", i, fields[i], tc.want[i])
				}
			}
			logs := logged.String()
			if !strings.Contains(logs, "kind="+tc.kind) {
				t.Errorf("log does not identify the action kind %q: %q", tc.kind, logs)
			}
			if strings.Contains(logs, "writer-only-marker") || strings.Contains(logs, "Mac Studio") {
				t.Errorf("log contains label contents: %q", logs)
			}
		})
	}
}

func TestMobileBriefStoredReadsDoNotRewriteExistingBrief(t *testing.T) {
	const id = "2026-10-09-on_request-090000"
	original := MobileBriefV1{
		BriefID: id, LocalDate: "2026-10-09", Slot: mobileBriefSlotOnRequest, AsOf: "2026-10-09T09:00:00Z",
		Narrative: []MobileBriefPara{{Text: "Open it on your Mac Studio."}},
		Cards:     []MobileBriefCard{{ID: "card", Kind: mobileBriefCardItem, Label: "Open it on your Mac Pro"}},
		Offers:    []MobileBriefOffer{{ID: "offer", Kind: "review", Label: "Open it on your Mac mini"}},
	}
	store := newMobileBriefStore(t.TempDir())
	if err := store.saveDocument(original, mobileBriefAsOfTime(original)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		read func() (MobileBriefV1, bool)
	}{
		{name: "by ID", read: func() (MobileBriefV1, bool) { return store.load(id) }},
		{name: "latest", read: store.latest},
		{name: "collection", read: func() (MobileBriefV1, bool) {
			briefs := store.briefs()
			if len(briefs) != 1 {
				return MobileBriefV1{}, false
			}
			return briefs[0], true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.read()
			if !ok || len(got.Narrative) != 1 || len(got.Cards) != 1 || len(got.Offers) != 1 {
				t.Fatalf("stored brief did not survive its read: %+v", got)
			}
			if got.Narrative[0].Text != original.Narrative[0].Text || got.Cards[0].Label != original.Cards[0].Label || got.Offers[0].Label != original.Offers[0].Label {
				t.Fatalf("read rewrote an existing brief: %+v", got)
			}
		})
	}
}

func TestMobileBriefAssemblyUsesOwnVoiceAndKeepsWorkNames(t *testing.T) {
	asOf := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	brief := buildMobileBriefV1(mobileBriefInput{
		Slot: mobileBriefSlotOnRequest, WindowStart: asOf.Add(-time.Hour), AsOf: asOf, Location: time.UTC,
		Moved: []mobileBriefCandidate{{
			SourceKind: "history", SourceID: "voice-work", Title: "Your Mac setup",
			Detail: "Open it on your Mac.", NextStep: "Open it on your iPhone.",
			Status: "needs_review", At: asOf.Add(-time.Hour),
		}},
	})
	refs := []string{brief.Evidence[0].Ref}
	brief.Narrative = []MobileBriefPara{
		{Text: "Open it on your Mac. Mac 1.0.41 is public. Review Your Mac setup.", Refs: refs},
		{Text: "Want to review Your Mac setup on your Mac?", Refs: refs},
	}
	brief.NarrativeStatus = "unavailable"
	_, brief.Offers = mobileBriefClosing(brief, "English")
	brief.Cards = mobileBriefApplyCardWords(brief, mobileBriefPatternCards(brief, "English"), []mobileBriefCardWords{{
		ID: mobileBriefOpenerCardID, Message: "I'd start with Your Mac setup on your Mac. Want to begin there?",
	}}, "English")

	assembled := mobileBriefWithOwnVoice(brief)
	if len(assembled.Narrative) != 2 {
		t.Fatalf("assembled brief narrative = %+v", assembled.Narrative)
	}
	if got, want := assembled.Narrative[0].Text, "Open it on your computer. Mac 1.0.41 is public. Review Your Mac setup."; got != want {
		t.Fatalf("narrative = %q, want %q", got, want)
	}
	if got, want := assembled.Narrative[1].Text, "Want to review Your Mac setup on your computer?"; got != want {
		t.Fatalf("closing = %q, want %q", got, want)
	}
	if len(assembled.Sections.Moved) != 1 {
		t.Fatalf("section rows = %+v", assembled.Sections.Moved)
	}
	item := assembled.Sections.Moved[0]
	if item.Title != "Your Mac setup" || item.Detail != "Open it on your computer." || item.NextStep != "Open it on your phone." {
		t.Fatalf("section row was not rewritten safely: %+v", item)
	}
	if len(assembled.Evidence) != 1 || assembled.Evidence[0].Label != "Your Mac setup" {
		t.Fatalf("source evidence changed: %+v", assembled.Evidence)
	}
	foundOpener := false
	for _, card := range assembled.Cards {
		if card.ID != mobileBriefOpenerCardID {
			continue
		}
		foundOpener = true
		want := "I'd start with Your Mac setup on your computer. Want to begin there?"
		if card.Message != want {
			t.Fatalf("opener = %q, want %q", card.Message, want)
		}
	}
	if !foundOpener {
		t.Fatal("the assembled brief lost its opener")
	}
	if len(assembled.Offers) == 0 {
		t.Fatal("the assembled brief lost its offers")
	}
	protected := mobileBriefQuotedContent(assembled)
	for _, offer := range assembled.Offers {
		text := offer.Label + "\n" + offer.Prompt + "\n" + offer.Text
		if rewritten, clean := rewriteMobileBriefOwnVoice(text, protected...); !clean || rewritten != text {
			t.Fatalf("offer needs voice repair: clean=%v before=%q after=%q", clean, text, rewritten)
		}
	}
	// Only the model's ok text is spoken (design R74): the assembled brief is model text, so it reads aloud.
	assembled.NarrativeStatus = "ok"
	spoken := mobileBriefSpokenText(assembled, "en")
	for _, want := range []string{"on your computer", "Mac 1.0.41 is public", "Your Mac setup"} {
		if !strings.Contains(spoken, want) {
			t.Errorf("spoken brief does not contain %q: %q", want, spoken)
		}
	}
	if rewritten, clean := rewriteMobileBriefOwnVoice(spoken, protected...); !clean || rewritten != spoken {
		t.Fatalf("spoken brief needs voice repair: clean=%v before=%q after=%q", clean, spoken, rewritten)
	}
}

func TestMobileBriefNarrativeTemplateQualifiesAttendance(t *testing.T) {
	for _, state := range []string{"attended", "skipped", "unknown"} {
		t.Run(state, func(t *testing.T) {
			brief := MobileBriefV1{
				AsOf: "2026-10-16T17:00:00Z", Timezone: "UTC",
				Evidence:          []MobileBriefRef{{Ref: "e1", SourceKind: mobileBriefAttendanceSource, SourceID: "sync", Label: "Team sync"}},
				MeetingAttendance: []MobileBriefAttendance{{EventID: "sync", Title: "Team sync at 11:30 AM", State: state, Scope: "computer", Refs: []string{"e1"}}},
			}
			paragraphs := mobileBriefNarrativeTemplate(brief, "English")
			if len(paragraphs) != 1 || paragraphs[0].Text != mobileBriefOpeningSentence(brief, "English") {
				t.Fatalf("calendar-shaped attendance opened the narrative: %+v", paragraphs)
			}
			if prompt := mobileBriefTestPrompt(brief, "English"); strings.Contains(prompt, "Team sync") {
				t.Fatal("past attendance reached the prompt")
			}
		})
	}
}

func TestMobileBriefNarrativeTemplateNoModelKeepsVoiceAndPrivacy(t *testing.T) {
	brief := mobileBriefVoiceFixture()
	paragraphs, status, _ := composeMobileBriefNarrative(context.Background(), nil, brief, nil)
	if status != "unavailable" || len(paragraphs) == 0 {
		t.Fatalf("no-model brief: status=%q paragraphs=%+v", status, paragraphs)
	}
	for _, language := range []string{"English", "Simplified Chinese"} {
		paragraphs := mobileBriefNarrativeTemplate(brief, language)
		if len(paragraphs) == 0 {
			t.Fatalf("%s template is empty", language)
		}
		if err := validateMobileBriefNarrativeVoice(brief, paragraphs, language); err != nil {
			t.Fatalf("%s template violates the voice: %v", language, err)
		}
	}
	// Old briefs can retain browsing/file items, but neither template nor
	// model may revive them as review or plan context.
	brief.Sections.Moved = append(brief.Sections.Moved, MobileBriefItem{Title: "Private filename", Refs: []string{"file"}})
	brief.Sections.OpenLoops = append(brief.Sections.OpenLoops, MobileBriefItem{Title: "Private search", Kind: "commitment", Refs: []string{"web"}})
	brief.Evidence = append(brief.Evidence,
		MobileBriefRef{Ref: "file", SourceKind: mobileBriefFileSource, SourceID: "f", Label: "Private filename"},
		MobileBriefRef{Ref: "web", SourceKind: mobileBriefWebSource, SourceID: "w", Label: "Private search"},
	)
	text := mobileBriefTestPrompt(brief, "English")
	for _, paragraph := range mobileBriefNarrativeTemplate(brief, "English") {
		text += paragraph.Text
	}
	for _, held := range []string{"Private filename", "Private search"} {
		if strings.Contains(text, held) {
			t.Fatalf("held-back source returned to the voice: %q", held)
		}
	}
}

func TestMobileBriefWithoutClockTimes(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"Team sync at 11:30 AM", "Team sync"},
		{"Design review 09:30 to 10:00", "Design review"},
		{"Review at 9 am", "Review"},
		{"下午3点讨论方案", "讨论方案"},
		{"08:00 to 09:30", ""},
		{"09:30 to 10:00, 30 min", "30 min"},
		{"Q3 pricing review", "Q3 pricing review"},
		{"Who the note came from", "Who the note came from"},
	} {
		if got := mobileBriefWithoutClockTimes(tc.input); got != tc.want {
			t.Errorf("without clocks %q = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestMobileBriefNarrativePeopleAreBoundedContextOnly(t *testing.T) {
	brief := mobileBriefVoiceFixture()
	for _, name := range []string{"Dana", "Priya", "Chen", "Mei"} {
		brief.Sections.People = append(brief.Sections.People, MobileBriefItem{Title: name, Detail: "Private conversation", Refs: []string{name}})
		brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: name, SourceKind: "principal", SourceID: name, Label: name})
	}
	brief.Sections.People = append(brief.Sections.People, MobileBriefItem{Title: "Private mail subject", Refs: []string{"mail"}})
	brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: "mail", SourceKind: "mail", SourceID: "thread", Label: "Private mail subject"})
	// Since backlog item 65 a sighting is no part of the written brief: the
	// prompt is the same with or without them.
	if prompt := mobileBriefTestPrompt(brief, "English"); prompt != mobileBriefTestPrompt(mobileBriefVoiceFixture(), "English") {
		t.Fatalf("people context reached the prompt:\n%s", prompt)
	}
	brief.Sections = MobileBriefSections{People: brief.Sections.People}
	// Sightings are never counted or named; with nothing else the brief opens with the quiet sentence (design v1.0.1).
	paragraphs, status, _ := composeMobileBriefNarrative(context.Background(), nil, brief, nil)
	if status != "skipped" || len(paragraphs) != 1 || paragraphs[0].Text != mobileBriefOpeningSentence(brief, "English") {
		t.Fatalf("sightings alone did not leave only the quiet opening: %+v %s", paragraphs, status)
	}
	for _, p := range paragraphs {
		for _, name := range []string{"Dana", "Priya", "Chen", "Mei", "Private mail subject"} {
			if strings.Contains(p.Text, name) {
				t.Fatalf("a sighting was named in the brief: %q", p.Text)
			}
		}
	}
}

// The chief of staff prompt and the Purpose Map meet in one prompt. Working-toward
// items reach the model with their bounded progress and map step; a commitment
// already reported in main chat, an item backed by browsing, and the content-free
// fallback for purged evidence never do.
func TestMobileBriefNarrativePromptCarriesThePurposeMapWithItsHoldbacks(t *testing.T) {
	brief := mobileBriefVoiceFixture()
	brief.Evidence = append(brief.Evidence,
		MobileBriefRef{Ref: "p1", SourceKind: "purpose", SourceID: "goal-a", Label: "Ship pricing page"},
		MobileBriefRef{Ref: "a1", SourceKind: "activity", SourceID: "run-a", Label: "Drafted pricing copy"},
		MobileBriefRef{Ref: "p2", SourceKind: "purpose", SourceID: "commitment-b", Label: "Reply to Dana"},
		MobileBriefRef{Ref: "p3", SourceKind: "purpose", SourceID: "goal-c", Label: "Browsing goal"},
		MobileBriefRef{Ref: "w1", SourceKind: mobileBriefWebSource, SourceID: "site", Label: "private-site.example"},
	)
	brief.Sections.WorkingToward = []MobileBriefItem{
		{Title: "Ship pricing page", Detail: "Drafted pricing copy", NextStep: "Review the headline", Refs: []string{"p1", "a1"}},
		{Title: "Browsing goal", Detail: "Visited private-site.example", Refs: []string{"p3", "w1"}},
		{Title: "Purged goal", Detail: "Evidence no longer on this computer.", EvidenceUnavailable: true},
	}
	brief.Sections.Commitments = []MobileBriefItem{
		{Title: "Reply to Dana", Status: "due_today", Refs: []string{"p2"}, ChatMessageID: "message-1", ChatSessionID: "chat-1"},
	}
	prompt := mobileBriefTestPrompt(brief, "English")
	// A goal reaches the prompt only as a pick that moved, never with its
	// progress text (mobile_brief_picks.go).
	for _, withheld := range []string{"Browsing goal", "private-site.example", "Purged goal", "Evidence no longer on this computer", "Reply to Dana", "Drafted pricing copy"} {
		if strings.Contains(prompt, withheld) {
			t.Errorf("prompt discloses %q", withheld)
		}
	}
	for _, ref := range mobileBriefNarrativeInputEvidence(brief) {
		if ref.Ref == "w1" || ref.Ref == "p2" {
			t.Errorf("evidence %s must not be citable", ref.Ref)
		}
	}
}
