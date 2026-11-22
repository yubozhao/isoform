package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"wails-gui/storage"
)

func quickChatReadyList() WorkList {
	return WorkList{GeneratedAt: "2026-11-21T09:40:00Z", Items: []WorkItem{
		{ID: "offer:1", Kind: workKindOffered, State: workStateOpen, Work: WorkBody{Title: "Prep for Lena"}, CreatedAt: "2026-11-21T08:00:00Z"},
		{ID: "prep:m1", Kind: workKindDone, Row: "prep", State: workStateOpen, Work: WorkBody{Title: "Prep for Lena", Text: "Lena asked: the Q4 numbers"},
			First:     []WorkTextPart{{Text: "For your meeting with Lena", Key: "for_meeting", At: "2026-11-21T15:00:00Z"}, {Key: "when", At: "2026-11-21T15:00:00Z"}},
			CreatedAt: "2026-11-21T14:00:00Z"},
		{ID: "result:h1", Kind: workKindResult, State: workStateBack, Work: WorkBody{Title: "Harbor checklist draft"}, CreatedAt: "2026-11-21T09:10:00Z"},
		{ID: "result:h2", Kind: workKindResult, State: workStateAnswered, Answer: &WorkAnswer{Action: "not_useful"}, Work: WorkBody{Title: "Budget summary"}, CreatedAt: "2026-11-21T09:00:00Z"},
		{ID: "prep:m2", Kind: workKindDone, State: workStateAnswered, Answer: &WorkAnswer{Action: "no"}, Work: WorkBody{Title: "Prep for Sam"}, CreatedAt: "2026-11-21T09:00:00Z"},
	}}
}

// Typing "prep me for Lena" shows the prep that's already made
// (redesign-work rule 15), not the open offer with the same title.
func TestQuickChatReadyFindsMadeItemByTitleWords(t *testing.T) {
	got := quickChatReadyMatch(quickChatReadyList(), "prep me for Lena's meeting", false)
	if got == nil || got.ID != "prep:m1" {
		t.Fatalf("match = %+v, want the made prep", got)
	}
	if got.Title != "Prep for Lena" || got.ForText != "For your meeting with Lena" || got.ForAt != "2026-11-21T15:00:00Z" {
		t.Fatalf("words = %+v", got)
	}
	// Its CreatedAt is after the list was built; it was made by then.
	if got.MadeAt != "2026-11-21T09:40:00Z" {
		t.Fatalf("madeAt = %q, want the list's build time", got.MadeAt)
	}
	if got := quickChatReadyMatch(quickChatReadyList(), "harbor", false); got == nil || got.ID != "result:h1" || got.MadeAt != "2026-11-21T09:10:00Z" {
		t.Fatalf("result match = %+v", got)
	}
}

func TestQuickChatReadyIgnoresOpenAndUnmadeItems(t *testing.T) {
	for _, text := range []string{"budget summary", "prep for sam", "what is on today", "", "the"} {
		if got := quickChatReadyMatch(quickChatReadyList(), text, false); got != nil {
			t.Fatalf("%q matched %+v", text, got)
		}
	}
	list := WorkList{Items: []WorkItem{{ID: "offer:1", Kind: workKindOffered, State: workStateOpen, Work: WorkBody{Title: "Prep for Lena"}}}}
	if got := quickChatReadyMatch(list, "prep for lena", false); got != nil {
		t.Fatalf("an open offer matched: %+v", got)
	}
}

func TestQuickChatReadyNothingWithClosedEyes(t *testing.T) {
	if got := quickChatReadyMatch(quickChatReadyList(), "prep for Lena", true); got != nil {
		t.Fatalf("Closed Eyes matched %+v", got)
	}
}

// Quick chat turns keep CiteSources, and the sources line reaches the panel
// as it is written (computer-chat v1.2.0 rule 5).
func TestQuickChatTurnKeepsSourcesAndMirrorsTheLine(t *testing.T) {
	app, tag, runner := spotlightMainRig(t)
	if _, err := app.startSpotlightSubmission("What did Sam say?", "", "", "req-src"); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-runner.inputs:
		found := false
		for _, tool := range input.ExtraTools {
			if tool.Name() == chatSourcesToolName {
				found = true
			}
		}
		if !found {
			t.Fatal("a Quick chat turn dropped CiteSources")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runner was not called")
	}
	waitForTagIdle(t, app, tag)
	events, cancel, err := app.spotlightStream.Subscribe(tag, app.spotlightStream.LatestSequence(tag))
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	blob, _ := json.Marshal(chatSourcesPayload{Sources: []chatSource{{Kind: "person", ID: "p1", Title: "Sam", Target: mainChatTarget{Kind: "person", ID: "p1"}}}})
	if app.appendMainChatItem(context.Background(), tag, storage.StreamItemRow{ID: "sources:x", Kind: "system", ToolName: chatSourcesLineTool, ToolInput: string(blob)}, "sources") == "" {
		t.Fatal("append failed")
	}
	select {
	case ev := <-events:
		var item StreamItemData
		if ev.Kind != "item" || json.Unmarshal([]byte(ev.Data), &item) != nil || item.ID != "sources:x" || item.ToolName != chatSourcesLineTool {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the sources line did not reach the panel")
	}
	// Other rows stay in Chat.
	app.mirrorMainChatRowToSpotlight(tag, storage.StreamItemRow{ID: "r", Kind: "system", ToolName: mainChatReportTool}, 1<<30)
	select {
	case ev := <-events:
		t.Fatalf("a report reached the panel: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}
