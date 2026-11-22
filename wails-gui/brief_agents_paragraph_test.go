package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func briefAgentsTestClock(t *testing.T) *time.Location {
	t.Helper()
	was := computerUses24Hour
	computerUses24Hour = func() bool { return false }
	t.Cleanup(func() { computerUses24Hour = was })
	return time.UTC
}

var briefAgentsTestAsOf = time.Date(2026, 11, 21, 18, 0, 0, 0, time.UTC)

func briefAgentsTestNews() BriefAgentNews {
	return BriefAgentNews{
		Results: []BriefAgentResult{{AgentName: "Codex", Title: "The test plan for the Halcyon import", LinkID: "wo_plan",
			At: briefAgentsTestAsOf.Add(-time.Hour).UnixMilli()}},
		Requests: []struct{}{},
		NoWord: []BriefAgentNoWord{{ID: "wo_outline", AgentName: "Claude", Title: "Q4 plan outline",
			Since: time.Date(2026, 11, 21, 16, 10, 0, 0, time.UTC).UnixMilli()}},
	}
}

// No paragraph when nothing happened with the agents (rule 1).
func TestBriefAgentsParagraphNoneWhenNothingHappened(t *testing.T) {
	loc := briefAgentsTestClock(t)
	brief := MobileBriefV1{AsOf: formatMobileBriefTime(briefAgentsTestAsOf), NarrativeStatus: "ok",
		Narrative: []MobileBriefPara{{Text: "You met Lena.", Refs: []string{"e1"}}}}
	empty := BriefAgentNews{Results: []BriefAgentResult{}, Requests: []struct{}{}, NoWord: []BriefAgentNoWord{}}
	if insertBriefAgentsParagraph(&brief, empty, "English", loc) || len(brief.Narrative) != 1 || len(brief.Evidence) != 0 {
		t.Fatalf("brief = %+v", brief)
	}
}

// Results, then hand-offs with no word and where to act, in the design's
// words (rules 2, 4 and 7).
func TestBriefAgentsParagraphResultsThenNoWord(t *testing.T) {
	loc := briefAgentsTestClock(t)
	text, named := briefAgentsParagraphText(briefAgentsTestNews(), "English", loc, briefAgentsTestAsOf)
	want := "Your agents: Codex finished the test plan for the Halcyon import. Claude has had Q4 plan outline since 4:10 PM, with no word yet. You can ask again or take it back from Your work."
	if text != want {
		t.Fatalf("text = %q\nwant %q", text, want)
	}
	if len(named) != 2 || named[0].ID != "wo_plan" || named[1].ID != "wo_outline" {
		t.Fatalf("named = %+v", named)
	}
	zh, _ := briefAgentsParagraphText(briefAgentsTestNews(), "zh-CN", loc, briefAgentsTestAsOf)
	if !strings.HasPrefix(zh, "你的智能体：Codex完成了") || !strings.Contains(zh, "从16:10起接手") || !strings.HasSuffix(zh, "或把它收回。") {
		t.Fatalf("zh = %q", zh)
	}
}

// Overnight, a morning brief leads with what finished (rule 3).
func TestBriefAgentsParagraphOvernightLeads(t *testing.T) {
	loc := briefAgentsTestClock(t)
	news := BriefAgentNews{Results: []BriefAgentResult{
		{AgentName: "Codex", Title: "Release notes", LinkID: "wo_new", At: briefAgentsTestAsOf.UnixMilli()},
		{AgentName: "Devin", Title: "The migration script", LinkID: "wo_night", At: briefAgentsTestAsOf.Add(-8 * time.Hour).UnixMilli(), Overnight: true},
	}}
	text, named := briefAgentsParagraphText(news, "English", loc, briefAgentsTestAsOf)
	if !strings.HasPrefix(text, "Your agents: Overnight: Devin finished the migration script. It's waiting for you to judge. Codex finished release notes.") {
		t.Fatalf("text = %q", text)
	}
	if named[0].ID != "wo_night" {
		t.Fatalf("named = %+v", named)
	}
}

// Only what Isoform knows: never "working", "almost" or a time it'll be
// done, whatever the hand-offs say (rule 4).
func TestBriefAgentsParagraphNeverGuesses(t *testing.T) {
	loc := briefAgentsTestClock(t)
	for i := 0; i < 40; i++ {
		news := BriefAgentNews{}
		for j := 0; j <= i%4; j++ {
			news.Results = append(news.Results, BriefAgentResult{AgentName: fmt.Sprintf("Agent %d", j), Title: fmt.Sprintf("Draft %d", i*10+j),
				Summary: "Still working on it, almost done, ready by 5 PM.", LinkID: fmt.Sprintf("wo_%d_%d", i, j), At: briefAgentsTestAsOf.UnixMilli(), Overnight: j%2 == 0})
		}
		for j := 0; j < i%3; j++ {
			news.NoWord = append(news.NoWord, BriefAgentNoWord{ID: fmt.Sprintf("nw_%d_%d", i, j), AgentName: "Claude", Title: fmt.Sprintf("Outline %d", j),
				Since: briefAgentsTestAsOf.Add(-time.Duration(3+j*24) * time.Hour).UnixMilli()})
		}
		for _, lang := range []string{"English", "zh-CN"} {
			text, _ := briefAgentsParagraphText(news, lang, loc, briefAgentsTestAsOf)
			lower := strings.ToLower(text)
			for _, word := range []string{"working", "almost", "will be done", "ready by", "正在", "快要", "差不多"} {
				if strings.Contains(lower, word) {
					t.Fatalf("%s fixture %d says %q: %q", lang, i, word, text)
				}
			}
		}
	}
}

// The paragraph goes before the closing question, keeps the parts
// done-then-next, and each hand-off it names is evidence.
func TestBriefAgentsParagraphPlace(t *testing.T) {
	loc := briefAgentsTestClock(t)
	brief := MobileBriefV1{AsOf: formatMobileBriefTime(briefAgentsTestAsOf), NarrativeStatus: "ok",
		Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: "calendar", SourceID: "ev1", Label: "Lena"}},
		Narrative: []MobileBriefPara{
			{Text: "You met Lena about the import.", Refs: []string{"e1"}, Part: briefPartDone},
			{Text: "Tomorrow you meet Sam at 10 AM.", Refs: []string{"e1"}, Part: briefPartNext},
			{Text: "Want me to draft the follow-up?", Refs: []string{"e1"}, Part: briefPartNext},
		}}
	if !insertBriefAgentsParagraph(&brief, briefAgentsTestNews(), "English", loc) {
		t.Fatal("nothing inserted")
	}
	if len(brief.Narrative) != 4 || !strings.HasPrefix(brief.Narrative[2].Text, "Your agents: ") || brief.Narrative[3].Text != "Want me to draft the follow-up?" {
		t.Fatalf("narrative = %+v", brief.Narrative)
	}
	if parts := mobileBriefNarrativePartsOf(brief.Narrative); len(parts) != 2 || parts[1].Count != 3 {
		t.Fatalf("parts = %+v", parts)
	}
	index, named := briefAgentsParagraphIndex(brief)
	if index != 2 || !named["wo_plan"] || !named["wo_outline"] || len(brief.Evidence) != 3 || brief.Evidence[1].SourceKind != briefAgentSource {
		t.Fatalf("index %d named %v evidence %+v", index, named, brief.Evidence)
	}

	// With no closing question it goes last.
	plain := MobileBriefV1{AsOf: brief.AsOf, NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "You met Lena.", Refs: []string{"e1"}}}}
	insertBriefAgentsParagraph(&plain, briefAgentsTestNews(), "English", loc)
	if len(plain.Narrative) != 2 || !strings.HasPrefix(plain.Narrative[1].Text, "Your agents: ") || plain.Narrative[1].Part != "" {
		t.Fatalf("plain = %+v", plain.Narrative)
	}
}

// The voice reads the inserted paragraph, and every paragraph's sentence
// marks still point at its own text, the ones after it included.
func TestBriefAgentsParagraphAudioMarksStayAligned(t *testing.T) {
	loc := briefAgentsTestClock(t)
	brief := MobileBriefV1{AsOf: formatMobileBriefTime(briefAgentsTestAsOf), NarrativeStatus: "ok",
		Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: "calendar", SourceID: "ev1", Label: "Lena"}},
		Narrative: []MobileBriefPara{
			{Text: "You met Lena about the import. She asked about files.", Refs: []string{"e1"}},
			{Text: "Want me to draft the follow-up? It takes a minute.", Refs: []string{"e1"}},
		}}
	insertBriefAgentsParagraph(&brief, briefAgentsTestNews(), "English", loc)
	speech, _ := mobileBriefSpeech(brief, "en")
	if !strings.Contains(speech, "Codex finished the test plan") {
		t.Fatalf("speech = %q", speech)
	}
	segments := mobileBriefSpokenSegments(speech)
	starts := make([]float64, len(segments))
	for i := range starts {
		starts[i] = float64(i * 10)
	}
	total := float64(len(segments) * 10)
	brief.Audio = &MobileBriefAudio{DurationSeconds: total, Paragraphs: mobileBriefParagraphStarts(brief, "en", segments, starts, total)}
	marks := mobileBriefAudioMarks(brief)
	byParagraph := map[int][]string{}
	for _, m := range marks {
		text := brief.Narrative[m.Paragraph].Text
		if m.From < 0 || m.To > len(text) || m.From >= m.To {
			t.Fatalf("mark %+v out of %q", m, text)
		}
		byParagraph[m.Paragraph] = append(byParagraph[m.Paragraph], text[m.From:m.To])
	}
	if len(byParagraph) != len(brief.Narrative) {
		t.Fatalf("marks for %d of %d paragraphs: %+v", len(byParagraph), len(brief.Narrative), byParagraph)
	}
	if got := byParagraph[2]; len(got) != 2 || got[0] != "Want me to draft the follow-up?" || got[1] != "It takes a minute." {
		t.Fatalf("closing marks = %q", got)
	}
	if got := byParagraph[1]; len(got) == 0 || got[0] != "Your agents: Codex finished the test plan for the Halcyon import." {
		t.Fatalf("agents marks = %q", got)
	}
}

// A title the brief's privacy check rejects is never said: the sentence
// names a hand-off instead, and the evidence label is that too.
func TestBriefAgentsParagraphDropsAFailingTitle(t *testing.T) {
	loc := briefAgentsTestClock(t)
	for _, title := range []string{"Email jane.doe@example.com the contract", "Use code 482913 to sign in", "[REDACTED] follow-up", "Send [REDACTED:email] the notes"} {
		news := BriefAgentNews{Results: []BriefAgentResult{{AgentName: "Codex", Title: title, LinkID: "wo_x", At: briefAgentsTestAsOf.UnixMilli()}}}
		brief := MobileBriefV1{AsOf: formatMobileBriefTime(briefAgentsTestAsOf), NarrativeStatus: "ok", Narrative: []MobileBriefPara{{Text: "You met Lena.", Refs: []string{"e1"}}}}
		if !insertBriefAgentsParagraph(&brief, news, "English", loc) {
			t.Fatalf("%q: nothing inserted", title)
		}
		if got := brief.Narrative[1].Text; got != "Your agents: Codex finished a hand-off." {
			t.Fatalf("%q: text = %q", title, got)
		}
		if brief.Evidence[0].Label != "a hand-off" {
			t.Fatalf("%q: label = %q", title, brief.Evidence[0].Label)
		}
	}
}

// At most two results sit under the paragraph, with its ref, and they count
// toward the brief's five items (rule 5); the same items are Your work's
// (rule 6). A hand-off with no word is not an item (rule 7).
func TestWorkPlacesAtMostTwoAgentItemsUnderTheParagraph(t *testing.T) {
	phone := workBriefPhoneDoc{BriefID: "b1",
		Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: "calendar"}, {Ref: "e2", SourceKind: briefAgentSource, SourceID: "wo_a"},
			{Ref: "e3", SourceKind: briefAgentSource, SourceID: "wo_b"}, {Ref: "e4", SourceKind: briefAgentSource, SourceID: "wo_c"},
			{Ref: "e5", SourceKind: briefAgentSource, SourceID: "wo_quiet"}},
		Narrative: []MobileBriefPara{{Text: "You met Lena.", Refs: []string{"e1"}}, {Text: "Your agents: …", Refs: []string{"e2", "e3", "e4", "e5"}}}}
	results := func() []WorkItem {
		return []WorkItem{
			{ID: "result:wo_a", Kind: workKindResult, State: workStateBack, Ref: WorkRef{HandoffID: "wo_a"}},
			{ID: "result:wo_b", Kind: workKindResult, State: workStateAnswered, Ref: WorkRef{HandoffID: "wo_b"}},
			{ID: "result:wo_c", Kind: workKindResult, State: workStateBack, Ref: WorkRef{HandoffID: "wo_c"}},
			{ID: "with:wo_quiet", Kind: workKindOffered, State: workStateWithAgent, Ref: WorkRef{HandoffID: "wo_quiet"}},
		}
	}
	rest := results()
	workPlaceBriefAgentItems(phone, nil, rest)
	placed := 0
	for _, item := range rest {
		if item.Ref.Paragraph != nil {
			placed++
			if *item.Ref.Paragraph != 1 || item.Ref.BriefID != "b1" || item.Kind != workKindResult {
				t.Fatalf("placed %+v", item)
			}
		}
	}
	if placed != 2 || rest[2].Ref.Paragraph != nil || rest[3].Ref.Paragraph != nil {
		t.Fatalf("placed %d: %+v", placed, rest)
	}

	// Four of the brief's own offers leave room for one.
	offers := []WorkItem{}
	for i := 0; i < 4; i++ {
		offers = append(offers, WorkItem{ID: fmt.Sprintf("o%d", i), Kind: workKindOffered, Ref: WorkRef{BriefID: "b1", OfferID: fmt.Sprintf("o%d", i)}})
	}
	rest = results()
	workPlaceBriefAgentItems(phone, offers, rest)
	if rest[0].Ref.Paragraph == nil || rest[1].Ref.Paragraph != nil {
		t.Fatalf("with four offers: %+v", rest)
	}

	// A brief with no agents paragraph places nothing.
	rest = results()
	workPlaceBriefAgentItems(workBriefPhoneDoc{BriefID: "b1", Narrative: phone.Narrative[:1], Evidence: phone.Evidence[:1]}, rest)
	for _, item := range rest {
		if item.Ref.Paragraph != nil {
			t.Fatalf("placed without a paragraph: %+v", item)
		}
	}
}
