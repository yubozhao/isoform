package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A correction takes effect at once, says what changed and where, and Undo
// takes it back (phone-chat v1.0.0 rules 8 and 9).
func TestCorrectIsoformSavesTheCorrectionWithUndo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rig := newUpcomingToolsRig(t)
	ctx := context.Background()
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rig.app.mainChatCorrectionTools(main.LocalID, promptOriginMac)
	if len(got) != 1 || got[0].Name() != chatCorrectionToolName {
		t.Fatalf("the main chat on the computer got %v", got)
	}
	tool := got[0]
	input := map[string]any{"statement": "Halcyon goes with Priya now, not Lena.", "where": "Who you know › Lena, Priya"}
	result, err := tool.Execute(t.Context(), draftCall("toolu_c1", input))
	if err != nil || result.IsError || !strings.Contains(result.Content, "Changed: Who you know › Lena, Priya") {
		t.Fatalf("correction failed: %v %+v", err, result)
	}
	if again, _ := tool.Execute(t.Context(), draftCall("toolu_c1", input)); again.IsError || !strings.Contains(again.Content, "already saved") {
		t.Fatalf("replay = %+v", again)
	}
	page, err := rig.app.store.LoadStreamItemsLatest(t.Context(), main.LocalID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var lineID string
	var payload chatCorrectionPayload
	for _, row := range page.Items {
		if row.ToolName != chatCorrectionLineTool {
			continue
		}
		if lineID != "" {
			t.Fatal("the correction was saved twice")
		}
		lineID = row.ID
		if err := json.Unmarshal([]byte(row.ToolInput), &payload); err != nil {
			t.Fatal(err)
		}
		if mainChatRowClass(row, time.Now()) != MainChatRowConversation {
			t.Fatal("the correction is not in the conversation")
		}
	}
	if lineID == "" || payload.Where != "Who you know › Lena, Priya" || payload.Status != "" {
		t.Fatalf("line = %q %+v", lineID, payload)
	}
	// The correction is the person's own note, read by everything Isoform makes.
	entry, err := rig.app.GetMemoryEntry(payload.MemoryID)
	if err != nil || !strings.Contains(entry.Content, "Halcyon goes with Priya now, not Lena.") || !isUserEditedMemory(entry.Content) {
		t.Fatalf("memory = %+v %v", entry, err)
	}
	notes := 0
	entries, _ := rig.app.GetMemoryEntries()
	for _, e := range entries {
		if strings.Contains(e.Content, "Halcyon goes with Priya") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("correction notes = %d, want 1", notes)
	}

	// The correction is one judgment in the answers ledger, from Chat.
	standing, ok := answersStanding(rig.app.answersLedger().entries())["correction:"+lineID]
	if !ok || standing.Kind != answerKindCorrection || standing.Source != answerSourceChat || standing.Action != "edit" ||
		standing.Edited == nil || standing.Edited.After != "Halcyon goes with Priya now, not Lena." || standing.Title != "Who you know › Lena, Priya" {
		t.Fatalf("ledger = %+v %v", standing, ok)
	}
	if status, err := rig.app.UndoChatCorrection(lineID); err != nil || status != chatCorrectionUndone {
		t.Fatalf("Undo = %q %v", status, err)
	}
	// Undo answers in the thread, once.
	undo, err := rig.app.store.LoadStreamItem(ctx, "correction-undo:"+lineID)
	if err != nil || undo == nil || undo.ToolName != chatCorrectionUndoTool || !strings.Contains(undo.ToolInput, "Who you know › Lena, Priya") || mainChatRowClass(*undo, time.Now()) != MainChatRowConversation {
		t.Fatalf("undo reply = %+v %v", undo, err)
	}
	if _, ok := answersStanding(rig.app.answersLedger().entries())["correction:"+lineID]; ok {
		t.Fatal("Undo left the correction standing in the answers ledger")
	}
	if _, err := rig.app.GetMemoryEntry(payload.MemoryID); err == nil {
		t.Fatal("Undo left the correction in memory")
	}
	if status, err := rig.app.UndoChatCorrection(lineID); err != nil || status != chatCorrectionUndone {
		t.Fatalf("a second Undo = %q %v", status, err)
	}
	if _, err := rig.app.UndoChatCorrection("not-a-correction"); err == nil {
		t.Fatal("undid a row that is not a correction")
	}
}

func TestCorrectIsoformIsForComputerTurnsInTheMainChat(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := rig.app.injectChatCorrectionContext(main.LocalID, promptOriginMac); !strings.Contains(got, "call CorrectIsoform once") {
		t.Fatalf("computer context = %q", got)
	}
	// A phone turn saves a correction only when its phone holds chat:correct.
	t.Cleanup(func() { phoneTurnCorrections.Delete(rig.app) })
	rig.app.notePhoneTurnSender(mobileTestDevice(mobileScopeChatWrite))
	if got := rig.app.mainChatCorrectionTools(main.LocalID, promptOriginPhone); got != nil {
		t.Fatalf("a phone without chat:correct got %v", got)
	}
	rig.app.notePhoneTurnSender(mobileTestDevice(mobileScopeChatWrite, mobileScopeChatCorrect))
	if got := rig.app.mainChatCorrectionTools(main.LocalID, promptOriginPhone); len(got) != 1 {
		t.Fatalf("a phone with chat:correct got %v", got)
	}
	for _, origin := range []promptOrigin{promptOriginPhoneAsk, promptOriginPanelAsk, promptOriginResume} {
		if got := rig.app.mainChatCorrectionTools(main.LocalID, origin); got != nil {
			t.Fatalf("origin %q got %v", origin, got)
		}
		if got := rig.app.injectChatCorrectionContext(main.LocalID, origin); got != "" {
			t.Fatalf("origin %q got the corrections context", origin)
		}
	}
	if got := rig.app.mainChatCorrectionTools("some-other-chat", promptOriginMac); got != nil {
		t.Fatal("another chat got CorrectIsoform")
	}
	for _, input := range []map[string]any{{"where": "About you"}, {"statement": "x"}, {"statement": "x", "where": strings.Repeat("a", chatCorrectionWhereMax+1)}} {
		if _, reason := chatCorrectionFromInput(input); reason == "" {
			t.Fatalf("accepted %v", input)
		}
	}
}
