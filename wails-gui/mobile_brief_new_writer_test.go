package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	briefcore "wails-gui/internal/brief"
)

func newWriterTestInput() mobileBriefInput {
	loc := time.UTC
	asOf := time.Date(2026, 11, 12, 12, 36, 0, 0, loc)
	in := mobileBriefInput{Slot: mobileBriefSlotOnRequest, WindowStart: asOf.Add(-14 * time.Hour), AsOf: asOf, Location: loc, Timezone: "UTC"}
	for i := 0; i < 12; i++ {
		in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: fmt.Sprintf("act-%d", i), Title: fmt.Sprintf("Own work item %d", i),
			Status: "done", Kind: "recap", At: asOf.Add(-time.Duration(i+1) * time.Hour), Apps: []string{"Safari"}})
	}
	in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: "act-agent", Title: "Fix the brief in Claude Code",
		Status: "done", Kind: "recap", At: asOf.Add(-30 * time.Minute), Apps: []string{"Claude"}})
	in.Meetings = append(in.Meetings, mobileBriefCandidate{SourceKind: mobileBriefCalendarSource, SourceID: "cal-1", Title: "Coffee with Vaishant", Status: "upcoming", Kind: "meeting", At: asOf.Add(2 * time.Hour)})
	in.Learned = append(in.Learned, mobileBriefCandidate{SourceKind: "memory", SourceID: "mem-1", Title: "You send a report to the hub after opening each PR", Status: "updated", Kind: "memory", At: asOf.Add(-time.Hour)})
	return in
}

func TestNewBriefMaterialKeepsTheWholeDayAndMarksAgents(t *testing.T) {
	in := newWriterTestInput()
	brief := buildMobileBriefV1(in)
	m, sources := newBriefMaterial(in, brief, briefPartDone, "")
	counts := map[string]int{}
	for _, item := range m.Items {
		counts[item.Group]++
		if strings.Contains(item.Title, "Claude Code") && (!item.Agent || item.Group != briefcore.GroupAgents) {
			t.Fatalf("agent work not marked: %+v", item)
		}
		if strings.Contains(item.Title, "report to the hub") {
			t.Fatalf("a habit learned from agent sessions reached the writer")
		}
		if item.Group != briefcore.GroupAhead && strings.Contains(item.When, ":") {
			t.Fatalf("past item carries a clock time: %+v", item)
		}
	}
	if counts[briefcore.GroupDone] != 12 {
		t.Fatalf("done items = %d, want all 12 (section caps must not drop them)", counts[briefcore.GroupDone])
	}
	if counts[briefcore.GroupAhead] != 1 || counts[briefcore.GroupAgents] != 1 {
		t.Fatalf("counts %v", counts)
	}
	if len(sources) != len(m.Items) {
		t.Fatalf("sources %d for %d items", len(sources), len(m.Items))
	}
}

func TestComposeNewBriefOneCallCitesRealEvidence(t *testing.T) {
	in := newWriterTestInput()
	brief := buildMobileBriefV1(in)
	before := len(brief.Evidence)
	s := &mobileBriefService{}
	calls := 0
	ask := func(_ context.Context, prompt string) (string, error) {
		calls++
		var lastDone, agent, ahead string
		m, _ := newBriefMaterial(in, brief, briefPartDone, "")
		for _, item := range m.Items {
			switch {
			case item.Title == "Own work item 11":
				lastDone = item.ID
			case item.Agent:
				agent = item.ID
			case item.Group == briefcore.GroupAhead:
				ahead = item.ID
			}
		}
		reply, _ := json.Marshal(map[string][]map[string]any{
			"done": {
				{"text": "You got through a full day of your own work, down to the last item.", "refs": []string{lastDone}},
				{"text": "Your agents fixed the brief in Claude Code.", "refs": []string{agent}},
			},
			"next": {
				{"text": "Coffee with Vaishant is up next.", "refs": []string{ahead}},
				{"text": "Check on the agents.", "refs": []string{agent}},
			},
		})
		return string(reply), nil
	}
	out, paragraphs, binding, err := s.composeNewBrief(context.Background(), in, brief, nil, ask)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("model calls = %d, want 1", calls)
	}
	if binding != nil {
		t.Fatalf("no app: no binding expected")
	}
	if len(paragraphs) != 3 {
		t.Fatalf("paragraphs = %d, want 3 (the agent-only next step dropped)", len(paragraphs))
	}
	if paragraphs[0].Part != briefPartDone || paragraphs[2].Part != briefPartNext {
		t.Fatalf("parts not labelled: %+v", paragraphs)
	}
	byRef := mobileBriefEvidenceByRef(out)
	for _, p := range paragraphs {
		for _, ref := range p.Refs {
			if _, ok := byRef[ref]; !ok {
				t.Fatalf("paragraph cites %s, which has no evidence", ref)
			}
		}
	}
	// "Own work item 11" is past the moved section's cap, so it gets new evidence.
	if len(out.Evidence) <= before {
		t.Fatalf("no evidence reserved for an item outside the sections")
	}
	if got := mobileBriefNarrativePartsOf(paragraphs); len(got) != 2 || got[0].Count != 2 || got[1].Count != 1 {
		t.Fatalf("parts %+v", got)
	}
}

func TestComposeNewBriefErrorLeavesTheOldPath(t *testing.T) {
	in := newWriterTestInput()
	brief := buildMobileBriefV1(in)
	s := &mobileBriefService{}
	_, _, _, err := s.composeNewBrief(context.Background(), in, brief, nil, func(context.Context, string) (string, error) { return "no json here", nil })
	if err == nil {
		t.Fatal("an unusable reply must return an error so the old writer runs")
	}
}

func TestNewBriefSettingDefaultsOnAndSurvivesScheduleSave(t *testing.T) {
	store := newMobileBriefConfigStore(t.TempDir())
	if !store.load().NewBrief {
		t.Fatal("newBrief must default to on")
	}
	settings := store.load()
	settings.NewBrief = false
	if err := store.save(settings); err != nil {
		t.Fatal(err)
	}
	if store.load().NewBrief {
		t.Fatal("newBrief false did not persist")
	}
}

func TestNewBriefDropsParagraphsWithUnboundRefsNotTheBrief(t *testing.T) {
	paragraphs := []MobileBriefPara{
		{Text: "Done.", Refs: []string{"e1", "e2"}, Part: briefPartDone},
		{Text: "Also done.", Refs: []string{"e1"}, Part: briefPartDone},
		{Text: "Next.", Refs: []string{"e3"}, Part: briefPartNext},
	}
	out, codes := newBriefWithoutUnbound(paragraphs, map[string]string{"e2": "summary"}, nil)
	if len(out) != 2 || out[0].Text != "Also done." || out[1].Text != "Next." {
		t.Fatalf("a paragraph citing an unbound source must go whole: %+v", out)
	}
	if strings.Join(codes, ",") != "paragraph_unbound_summary" {
		t.Fatalf("codes %v", codes)
	}
}

func TestNewBriefMaterialCarriesConversationsOutsideSections(t *testing.T) {
	in := newWriterTestInput()
	in.Conversations = []mobileBriefCandidate{
		{SourceKind: mobileBriefRecapSource, SourceID: "chat-1", Title: "Message Xiaotian about Product Hunt groups", Status: "done", Kind: "conversation", At: in.AsOf.Add(-2 * time.Hour), Apps: []string{"WeChat"}},
		{SourceKind: mobileBriefRecapSource, SourceID: "chat-2", Title: "Coffee plan in #random", Status: "needs_reply", Kind: "conversation", At: in.AsOf.Add(-time.Hour), Apps: []string{"Slack"}, NextStep: "Reply with a time"},
	}
	brief := buildMobileBriefV1(in)
	for _, ref := range brief.Evidence {
		if strings.HasPrefix(ref.SourceID, "chat-") {
			t.Fatal("a conversation reached a section")
		}
	}
	m, _ := newBriefMaterial(in, brief, briefPartDone, "")
	groups := map[string]string{}
	for _, item := range m.Items {
		groups[item.Title] = item.Group
	}
	if groups["Message Xiaotian about Product Hunt groups"] != briefcore.GroupDone || groups["Coffee plan in #random"] != briefcore.GroupFollowUp {
		t.Fatalf("conversation groups %v", groups)
	}
	excluded := mobileBriefWithoutExcluded(in, []string{"xiaotian"})
	if len(excluded.Conversations) != 1 {
		t.Fatalf("a stated exclusion must remove the conversation: %d left", len(excluded.Conversations))
	}
}

func TestNewWriterShortBriefIsNotQuiet(t *testing.T) {
	in := newWriterTestInput()
	in.Slot = mobileBriefSlotMorning
	brief := buildMobileBriefV1(in)
	brief.NarrativeStatus, brief.writerItems = "ok", 3
	brief.Narrative = []MobileBriefPara{{Text: "A quiet day: coffee with Vaishant is the one thing ahead.", Refs: []string{brief.Evidence[0].Ref}, Part: briefPartNext}}
	if got := mobileBriefForPublication(brief); got.NarrativeStatus == "quiet" || len(got.Narrative) == 0 {
		t.Fatalf("a short new-writer brief was made quiet: %s", got.NarrativeStatus)
	}
	brief.writerItems = 0
	if got := mobileBriefForPublication(brief); got.NarrativeStatus != "quiet" {
		t.Fatalf("the old writer's word floor changed: %s", got.NarrativeStatus)
	}
}

// The 2026-11-13 made-ahead morning: 39 items and next paragraphs that cite
// calendar events. The section builder keeps two calendar rows, so the words
// used to lose every other event and the slot went quiet.
func TestMorningWithManyCalendarCitationsIsShownWithBothParts(t *testing.T) {
	loc := time.UTC
	asOf := time.Date(2026, 11, 13, 6, 55, 0, 0, loc)
	in := mobileBriefInput{Slot: mobileBriefSlotMorning, WindowStart: asOf.Add(-16 * time.Hour), AsOf: asOf, Location: loc, Timezone: "UTC"}
	for i := 0; i < 30; i++ {
		in.Moved = append(in.Moved, mobileBriefCandidate{SourceKind: mobileBriefRecapSource, SourceID: fmt.Sprintf("act-%d", i), Title: fmt.Sprintf("Finished part %d of the launch checklist", i),
			Status: "done", Kind: "recap", At: asOf.Add(-time.Duration(10+i) * time.Hour / 3), Apps: []string{"Safari"}})
	}
	for i := 0; i < 6; i++ {
		in.Meetings = append(in.Meetings, mobileBriefCandidate{SourceKind: mobileBriefCalendarSource, SourceID: fmt.Sprintf("cal-%d", i), Title: fmt.Sprintf("Meeting %d with the design team", i),
			Status: "upcoming", Kind: "meeting", At: asOf.Add(time.Duration(2+i) * time.Hour)})
	}
	for i := 0; i < 3; i++ {
		in.Noticed = append(in.Noticed, mobileBriefCandidate{SourceKind: mobileBriefNoticedSource, SourceID: fmt.Sprintf("n-%d", i), Title: fmt.Sprintf("Send follow-up %d", i), Status: "follow_up", Kind: "noticed", At: asOf.Add(-2 * time.Hour)})
	}
	brief := buildMobileBriefV1(in)
	m, _ := newBriefMaterial(in, brief, briefPartNext, "")
	if len(m.Items) != 39 {
		t.Fatalf("items = %d, want 39", len(m.Items))
	}
	var done, calendar []string
	for _, item := range m.Items {
		switch item.Group {
		case briefcore.GroupDone:
			done = append(done, item.ID)
		case briefcore.GroupAhead:
			calendar = append(calendar, item.ID)
		}
	}
	ask := func(context.Context, string) (string, error) {
		reply, _ := json.Marshal(map[string][]map[string]any{
			"done": {{"text": "Yesterday you worked through most of the launch checklist.", "refs": done[:3]}},
			"next": {
				{"text": "Today starts with the design team at eight, so bring the checklist status.", "refs": []string{calendar[3]}},
				{"text": "Two more design meetings follow before noon; keep the afternoon for the follow-ups.", "refs": []string{calendar[4], calendar[5]}},
			},
		})
		return string(reply), nil
	}
	s := &mobileBriefService{}
	out, paragraphs, _, err := s.composeNewBrief(context.Background(), in, brief, nil, ask)
	if err != nil {
		t.Fatal(err)
	}
	out.Narrative, out.NarrativeStatus = paragraphs, "ok"
	published := mobileBriefWithNarrativeParts(mobileBriefForPublication(out))
	if published.NarrativeStatus == "quiet" || len(published.Narrative) != 3 {
		t.Fatalf("the morning was not shown whole: status=%s paragraphs=%d", published.NarrativeStatus, len(published.Narrative))
	}
	parts := mobileBriefNarrativePartsOf(published.Narrative)
	if len(parts) != 2 || parts[0].Kind != briefPartDone || parts[1].Kind != briefPartNext || parts[1].Count != 2 {
		t.Fatalf("parts %+v", parts)
	}
	if n := mobileBriefEvidenceCounts(out.Evidence)[mobileBriefCalendarSource]; n <= mobileBriefCalendarLimit {
		t.Fatalf("calendar evidence = %d; the words' events were not cited", n)
	}
}

// A made-ahead morning reads its own day: the evening it was made is yesterday.
func TestMadeAheadMorningUsesTheReadersDayWords(t *testing.T) {
	loc := time.UTC
	asOf := time.Date(2026, 11, 12, 21, 59, 0, 0, loc)
	in := mobileBriefInput{Slot: mobileBriefSlotMorning, WindowStart: asOf.Add(-6 * time.Hour), AsOf: asOf, BriefDay: time.Date(2026, 11, 13, 0, 0, 0, 0, loc), Location: loc}
	in.Moved = []mobileBriefCandidate{{SourceKind: mobileBriefRecapSource, SourceID: "act-1", Title: "Reviewed the brief fix", Status: "done", Kind: "recap", At: asOf.Add(-time.Hour), Apps: []string{"Safari"}}}
	m, _ := newBriefMaterial(in, buildMobileBriefV1(in), briefPartNext, "")
	if len(m.Items) != 1 || m.Items[0].When != "yesterday" {
		t.Fatalf("made-ahead day word = %+v", m.Items)
	}
}
