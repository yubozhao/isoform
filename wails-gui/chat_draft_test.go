package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yansu-agent/agent/tools"
)

func draftCall(id string, input map[string]any) tools.ToolCall {
	return tools.ToolCall{ID: id, Input: input}
}

func TestDraftMessagePutsTheMessageOnACardAndSendsNothing(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rig.app.mainChatDraftTools(main.LocalID, promptOriginMac)
	if len(got) != 1 || got[0].Name() != chatDraftToolName {
		t.Fatalf("the main chat on the computer got %v", got)
	}
	tool := got[0]
	input := map[string]any{"to": "dana@example.com", "channel": "mail", "subject": "Launch memo", "text": "Hi Dana,\nThe memo is attached.\nBo"}
	result, err := tool.Execute(t.Context(), draftCall("toolu_1", input))
	if err != nil || result.IsError || !strings.Contains(result.Content, "Nothing was sent") {
		t.Fatalf("draft failed: %v %+v", err, result)
	}
	// A replayed call shows the card once.
	if again, _ := tool.Execute(t.Context(), draftCall("toolu_1", input)); again.IsError || !strings.Contains(again.Content, "already in the chat") {
		t.Fatalf("replay = %+v", again)
	}
	page, err := rig.app.store.LoadStreamItemsLatest(t.Context(), main.LocalID, 50)
	if err != nil {
		t.Fatal(err)
	}
	cards := 0
	for _, row := range page.Items {
		if row.ToolName != chatDraftCardTool {
			continue
		}
		cards++
		var payload chatDraftPayload
		if err := json.Unmarshal([]byte(row.ToolInput), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.To != "dana@example.com" || payload.Channel != "mail" || payload.Subject != "Launch memo" || payload.Text != input["text"] {
			t.Fatalf("card payload = %+v", payload)
		}
	}
	for _, row := range page.Items {
		if row.ToolName == chatDraftCardTool && mainChatRowClass(row, time.Now()) != MainChatRowConversation {
			t.Fatalf("the draft card is not in the conversation: %v", mainChatRowClass(row, time.Now()))
		}
	}
	if cards != 1 {
		t.Fatalf("draft cards = %d, want 1", cards)
	}
	if sends, err := rig.app.store.LoadOutboundSend(t.Context(), "toolu_1", ""); err != nil || sends != nil {
		t.Fatalf("a draft started a send: %+v %v", sends, err)
	}
}

func TestDraftMessageIsForComputerTurnsInTheMainChat(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []promptOrigin{promptOriginPhone, promptOriginPhoneAsk, promptOriginPanelAsk, promptOriginResume} {
		if got := rig.app.mainChatDraftTools(main.LocalID, origin); got != nil {
			t.Fatalf("origin %q got %v", origin, got)
		}
		if got := rig.app.injectChatDraftToolContext(main.LocalID, origin); got != "" {
			t.Fatalf("origin %q got the draft context", origin)
		}
	}
	if got := rig.app.mainChatDraftTools("some-other-chat", promptOriginMac); got != nil {
		t.Fatal("another chat got DraftMessage")
	}
	if got := rig.app.injectChatDraftToolContext(main.LocalID, promptOriginMac); !strings.Contains(got, "never send it yourself") || !strings.Contains(got, "call DraftMessage") {
		t.Fatalf("draft context = %q", got)
	}
}

func TestDraftMessageInput(t *testing.T) {
	for _, tc := range []struct {
		input  map[string]any
		reason string
		want   chatDraftPayload
	}{
		{map[string]any{"to": " Dana ", "text": "Thanks!", "subject": "ignored", "channel": "slack"}, "", chatDraftPayload{To: "Dana", Channel: "slack", Text: "Thanks!"}},
		{map[string]any{"to": "Dana", "text": "Hi", "channel": "fax"}, "", chatDraftPayload{To: "Dana", Channel: "other", Text: "Hi"}},
		{map[string]any{"text": "Hi"}, "say who", chatDraftPayload{}},
		{map[string]any{"to": "Dana", "text": "  "}, "empty", chatDraftPayload{}},
		{map[string]any{"to": "Dana", "text": strings.Repeat("x", chatDraftTextMax+1)}, "too long", chatDraftPayload{}},
	} {
		got, reason := chatDraftFromInput(tc.input)
		if tc.reason != "" {
			if !strings.Contains(reason, tc.reason) {
				t.Errorf("input %v: reason %q, want %q", tc.input["to"], reason, tc.reason)
			}
			continue
		}
		if reason != "" || got != tc.want {
			t.Errorf("input %v: got %+v %q", tc.input, got, reason)
		}
	}
}
