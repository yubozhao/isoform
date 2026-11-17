package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Work asked for in Chat becomes a hand-off card, never chat work
// (computer-chat v1.0.0 rule 4, phone-chat v1.0.0 rule 12).
func TestHandOffWorkPutsACardInTheChatAndRunsNothing(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rig.app.mainChatHandoffTools(main.LocalID, promptOriginMac)
	if len(got) != 1 || got[0].Name() != chatHandoffToolName {
		t.Fatalf("the main chat on the computer got %v", got)
	}
	tool := got[0]
	input := map[string]any{
		"title":  "Draft the Q4 plan outline",
		"detail": "Outline the Q4 plan for Monday from Friday's notes.",
		"done":   "An outline with goals, owners and dates.",
		"gets":   []any{"your notes from Friday", " ", "last quarter's plan"},
	}
	result, err := tool.Execute(t.Context(), draftCall("toolu_h1", input))
	if err != nil || result.IsError || !strings.Contains(result.Content, "Nothing was started") || !strings.Contains(result.Content, "That's work for your agent. Here's what would go:") {
		t.Fatalf("hand-off failed: %v %+v", err, result)
	}
	if again, _ := tool.Execute(t.Context(), draftCall("toolu_h1", input)); again.IsError || !strings.Contains(again.Content, "already in the chat") {
		t.Fatalf("replay = %+v", again)
	}
	page, err := rig.app.store.LoadStreamItemsLatest(t.Context(), main.LocalID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var cardID string
	for _, row := range page.Items {
		if row.ToolName != chatHandoffCardTool {
			continue
		}
		if cardID != "" {
			t.Fatal("the card was shown twice")
		}
		cardID = row.ID
		var payload chatHandoffPayload
		if err := json.Unmarshal([]byte(row.ToolInput), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Title != "Draft the Q4 plan outline" || payload.Done == "" || len(payload.Gets) != 2 || payload.Status != "" {
			t.Fatalf("card payload = %+v", payload)
		}
		if mainChatRowClass(row, time.Now()) != MainChatRowConversation {
			t.Fatal("the hand-off card is not in the conversation")
		}
	}
	if cardID == "" {
		t.Fatal("no hand-off card")
	}
	// No answers the card; the first answer stands.
	if status, err := rig.app.AnswerChatHandoffCard(cardID, chatHandoffDeclined); err != nil || status != chatHandoffDeclined {
		t.Fatalf("No = %q %v", status, err)
	}
	if status, err := rig.app.AnswerChatHandoffCard(cardID, chatHandoffHanded); err != nil || status != chatHandoffDeclined {
		t.Fatalf("a second answer changed the card: %q %v", status, err)
	}
	if _, err := rig.app.AnswerChatHandoffCard("not-a-card", chatHandoffDeclined); err == nil {
		t.Fatal("answered a row that is not a hand-off card")
	}
	if _, err := rig.app.AnswerChatHandoffCard(cardID, "run"); err == nil {
		t.Fatal("accepted an answer other than declined or handed")
	}
}

func TestHandOffWorkCarriesTheKindOfWork(t *testing.T) {
	code, _ := chatHandoffFromInput(map[string]any{"title": "Fix the login bug", "detail": "In isoform-dev.", "kind": "code"})
	other, _ := chatHandoffFromInput(map[string]any{"title": "Draft the plan", "detail": "x", "kind": "poem"})
	if code.Kind != "code" || other.Kind != "" {
		t.Fatalf("kinds = %q %q", code.Kind, other.Kind)
	}
}

func TestHandOffWorkRefusesAnEmptyCard(t *testing.T) {
	for _, input := range []map[string]any{
		{"detail": "x"},
		{"title": "Plan"},
		{"title": strings.Repeat("a", chatHandoffTitleMax+1), "detail": "x"},
	} {
		if _, reason := chatHandoffFromInput(input); reason == "" {
			t.Fatalf("accepted %v", input)
		}
	}
}

// Chat answers about the person's own day: every main-chat turn is told so,
// and only a turn typed on the computer gets the card.
func TestChatScopeNarrowsTheMainChat(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mac := rig.app.injectChatNarrowingContext(main.LocalID, promptOriginMac)
	for _, want := range []string{"<chat-scope>", "Call HandOffWork", "No general-assistant answers", "never make something up"} {
		if !strings.Contains(mac, want) {
			t.Fatalf("computer context lacks %q: %q", want, mac)
		}
	}
	// A phone turn gets the card too, now that the phone shows it (B10).
	phone := rig.app.injectChatNarrowingContext(main.LocalID, promptOriginPhone)
	if !strings.Contains(phone, "No general-assistant answers") || !strings.Contains(phone, "HandOffWork") {
		t.Fatalf("phone context = %q", phone)
	}
	if got := rig.app.mainChatHandoffTools(main.LocalID, promptOriginPhone); len(got) != 1 {
		t.Fatalf("a phone turn got %v", got)
	}
	if ask := rig.app.injectChatNarrowingContext(main.LocalID, promptOriginPhoneAsk); strings.Contains(ask, "HandOffWork") {
		t.Fatalf("a phone Ask was offered HandOffWork: %q", ask)
	}
	for _, origin := range []promptOrigin{promptOriginPhoneAsk, promptOriginPanelAsk, promptOriginResume} {
		if got := rig.app.mainChatHandoffTools(main.LocalID, origin); got != nil {
			t.Fatalf("origin %q got %v", origin, got)
		}
	}
	if got := rig.app.injectChatNarrowingContext("some-other-chat", promptOriginMac); got != "" {
		t.Fatal("another chat got the chat scope")
	}
	if got := rig.app.mainChatHandoffTools("some-other-chat", promptOriginMac); got != nil {
		t.Fatal("another chat got HandOffWork")
	}
}
