package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mobileBriefHighlightsFixture() MobileBriefV1 {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	in := mobileBriefInput{AsOf: now, Location: time.UTC, Timezone: "UTC"}
	// Recent routine observations must not crowd an older outcome or decision
	// out of the collector's eight rows or the three-line Highlights block.
	for i := range 12 {
		in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: mobileBriefRecapSource,
			SourceID: fmt.Sprint(i), Title: "Worked on notes at 09:30", Kind: "recap", Status: "done", At: now.Add(-time.Duration(i+1) * time.Minute)})
	}
	in.Moved = append(in.Moved,
		mobileBriefCandidate{SourceKind: "history", SourceID: "outcome", Title: "Sent the offer\nwith approval at 08:00", Status: "verified", At: now.Add(-6 * time.Hour)},
		mobileBriefCandidate{SourceKind: "history", SourceID: "decision", Title: "Chose the launch scope at 9 am", Kind: "decision", Status: "recorded", At: now.Add(-5 * time.Hour)},
		mobileBriefCandidate{SourceKind: "history", SourceID: "review", Title: "Review the launch draft", Status: "needs_review", At: now.Add(-time.Hour)},
		mobileBriefCandidate{SourceKind: "history", SourceID: "blocked", Title: "Choose the launch option", Status: "blocked", At: now.Add(-time.Hour)},
	)
	in.Doing = []mobileBriefCandidate{{SourceKind: "history", SourceID: "plan", Title: "Prepare the release", Status: "active"}}
	in.Attendance = []mobileBriefAttendanceObservation{{EventID: "meeting", Title: "Offer discussion at 10:00", State: "attended", ObservedAt: now.Add(-2 * time.Hour)}}
	in.Meetings = []mobileBriefCandidate{{SourceKind: mobileBriefCalendarSource, SourceID: "next", Title: "Next meeting", Status: "upcoming", At: now.Add(time.Hour)}}
	in.WorkingToward = []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "goal", Title: "Launch work at 09:15", Detail: "Reviewed options at 07:45", NextStep: "Review scope at 08:15", LastTouch: now.Add(-time.Hour)}}
	in.WaitingOnYou = []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "waiting", Title: "Dana", Status: "no_recent_touch", Detail: "PRIVATE_WAITING_DETAIL"}}
	in.Commitments = []mobileBriefCandidate{{SourceKind: "purpose", SourceID: "due", Title: "Send the contract", Status: "due_today", DueAt: now}}
	in.Watching = []mobileBriefCandidate{{SourceKind: mobileBriefWebSource, SourceID: "web", Title: "PRIVATE_BROWSING_LIST"}}
	in.Deliverables = []mobileBriefCandidate{{SourceKind: mobileBriefFileSource, SourceID: "file", Title: "PRIVATE_FILE_LIST", Status: "done"}}
	return buildMobileBriefV1(in)
}

func TestMobileBriefHighlightsSelectImportanceBeforeRecency(t *testing.T) {
	brief := mobileBriefHighlightsFixture()
	got := brief.Sections.Highlights
	if len(got) != 2 || got[0].Title != "Sent the offer with approval" || got[1].Title != "Chose the launch scope" {
		t.Fatalf("Highlights lost outcomes, decisions or people: %+v", got)
	}
	for _, item := range got {
		if len(item.Title) > mobileBriefHighlightBytes || strings.ContainsAny(item.Title, "\r\n") || mobileBriefDisplayClock.MatchString(item.Title) || item.Detail != "" || item.Context != nil || item.NextStep != "" {
			t.Fatalf("Highlights contains more than one short line: %+v", item)
		}
	}
	for _, section := range mobileBriefSectionLines(brief.Sections) {
		for _, item := range section.items {
			if mobileBriefFutureCalendarItem(brief, item) {
				continue
			}
			if mobileBriefDisplayClock.MatchString(item.Title + " " + item.Detail + " " + item.NextStep) {
				t.Fatalf("past display clock in %s: %+v", section.name, item)
			}
		}
	}
	// Source timestamps stay intact as provenance.
	if got[0].At != "2026-10-17T06:00:00Z" {
		t.Fatalf("lost provenance: %+v", got[0])
	}
}

func TestMobileBriefHighlightsOrderAcrossPromptTemplateSpeechAndJSON(t *testing.T) {
	brief := mobileBriefHighlightsFixture()
	want := []string{"Decide", "Review", "Plan", "Waiting on you", "Due", "Day ahead", "Highlights"}
	var names []string
	for _, section := range mobileBriefNarrativeSections(brief) {
		if len(section.items) != 0 {
			names = append(names, section.name)
		}
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("sections = %v, want %v", names, want)
	}
	// Since backlog item 65 the written brief follows the picks' order, not
	// these sections; the template, speech and JSON below still do.
	prompt := mobileBriefTestPrompt(brief, "English")
	last := -1
	for _, lang := range []string{"English", "Simplified Chinese"} {
		paragraphs := mobileBriefNarrativeTemplate(brief, lang)
		if len(paragraphs) != 5 {
			t.Fatalf("%s template lost forward space: %+v", lang, paragraphs)
		}
		if err := validateMobileBriefNarrativeVoice(brief, paragraphs, lang); err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		if len(strings.Split(paragraphs[3].Text, "\n")) != 2 {
			t.Fatalf("missing Highlights lines: %+v", paragraphs[3])
		}
		brief.Narrative = paragraphs
		var texts, spokenTexts []string
		for _, p := range paragraphs {
			texts = append(texts, p.Text)
			spokenTexts = append(spokenTexts, normalizeBriefText(p.Text, lang, mobileBriefTextReference(brief), true))
		}
		// A template is never spoken (design R74): the written order stands, and nothing is read aloud.
		if spoken := mobileBriefSpokenText(brief, mobileBriefVoiceLang(lang)); spoken != "" {
			t.Fatalf("a template was spoken: %q", spoken)
		}
		_ = spokenTexts
		for _, held := range []string{"09:30", "08:00", "10:00", "09:15", "07:45", "08:15", "PRIVATE_WAITING_DETAIL", "PRIVATE_BROWSING_LIST", "PRIVATE_FILE_LIST"} {
			if strings.Contains(prompt+strings.Join(texts, ""), held) {
				t.Fatalf("held-back text returned: %s", held)
			}
		}
	}
	raw, err := json.Marshal(brief.Sections)
	if err != nil {
		t.Fatal(err)
	}
	last = -1
	for _, name := range []string{"decide", "review", "plan", "waitingOnYou", "commitments", "dayAhead", "highlights"} {
		at := strings.Index(string(raw), `"`+name+`":`)
		if at <= last {
			t.Fatalf("wire section %s at %d, after %d", name, at, last)
		}
		last = at
	}
}

func TestMobileBriefNarrativeRejectsLongOrLeadingReview(t *testing.T) {
	brief := mobileBriefHighlightsFixture()
	forward := MobileBriefPara{Text: "Choose the launch option.", Refs: brief.Sections.Decide[0].Refs}
	highlight := MobileBriefPara{Text: "Sent the offer.", Refs: brief.Sections.Highlights[0].Refs}
	for _, tc := range []struct {
		name       string
		paragraphs []MobileBriefPara
	}{
		{"leading recap", []MobileBriefPara{highlight, forward}},
		{"recap only with forward work", []MobileBriefPara{highlight}},
		{"four lines", []MobileBriefPara{forward, {Text: "One\nTwo\nThree\nFour", Refs: highlight.Refs}}},
		{"long line", []MobileBriefPara{forward, {Text: strings.Repeat("x", 161), Refs: highlight.Refs}}},
		{"past clock", []MobileBriefPara{forward, {Text: "Sent the offer at 08:00.", Refs: highlight.Refs}}},
		{"mixed recap and forward", []MobileBriefPara{{Text: "Choose, then a recap.", Refs: append(append([]string{}, forward.Refs...), highlight.Refs...)}}},
		{"review before decision", []MobileBriefPara{{Text: "Review the draft.", Refs: brief.Sections.Review[0].Refs}, forward}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMobileBriefNarrativeVoice(brief, tc.paragraphs, "English"); err == nil {
				t.Fatal("model reply should fall back to the forward-first template")
			}
		})
	}
	if err := validateMobileBriefNarrativeVoice(brief, []MobileBriefPara{forward, highlight}, "English"); err != nil {
		t.Fatal(err)
	}
}
