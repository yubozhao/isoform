package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"wails-gui/storage"
)

func correctionTestTool(t *testing.T, a *App, ref *TodayChatContextRef, origin promptOrigin) *chatCorrectionTool {
	t.Helper()
	main, err := a.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range a.mainChatTurnTools(main.LocalID, origin, &topicTurnBinding{correctionRef: ref}) {
		if c, ok := tool.(*chatCorrectionTool); ok {
			return c
		}
	}
	t.Fatal("missing correction tool")
	return nil
}

func submitTestCorrection(t *testing.T, c *chatCorrectionTool, about map[string]any) (string, chatCorrectionPayload) {
	t.Helper()
	input := map[string]any{"statement": "The corrected wording.", "where": "Your work", "undoneSentence": "The original wording is back."}
	if about != nil {
		input["about"] = about
	}
	r, err := c.Execute(t.Context(), draftCall("judgment", input))
	if err != nil || r.IsError {
		t.Fatalf("apply: %v %v", r, err)
	}
	id := "correction:" + c.tag + ":judgment"
	row, err := c.app.store.LoadStreamItem(t.Context(), id)
	if err != nil || row == nil {
		t.Fatalf("line: %v %v", row, err)
	}
	var p chatCorrectionPayload
	if err := json.Unmarshal([]byte(row.ToolInput), &p); err != nil {
		t.Fatal(err)
	}
	return id, p
}

func TestChatCorrectionBriefSentence(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	b := boundMemoryConversationFixture(t, a)
	b.Narrative = []MobileBriefPara{{Text: "The launch is ready.", Refs: []string{b.Evidence[0].Ref}}}
	bindConversationBriefFixture(t, a, &b)
	dir, err := mobileBriefStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := newMobileBriefStore(dir).save(b, time.Now()); err != nil {
		t.Fatal(err)
	}
	a.mobileBriefSvc = &workTestBriefSource{mobileTestBriefSource{brief: &b}}
	ref, err := a.PrepareTodayBriefParagraph(b.BriefID, 0, b.Narrative[0].Text)
	if err != nil {
		t.Fatal(err)
	}
	c := correctionTestTool(t, a, &ref, promptOriginMac)
	id, p := submitTestCorrection(t, c, nil)
	source := mobileBriefSourceKey(b.Evidence[0].SourceKind, b.Evidence[0].SourceID)
	target := workBriefSentenceID(b.BriefID, 0)
	answer := a.answersLedger().current()[target]
	if p.About == nil || p.About.ID != target || p.Applied != "not_right" || answer.Action != "no" || answer.Edited == nil || answer.Edited.After != p.Statement || !a.briefNotRightSources(time.Now())[source] {
		t.Fatalf("sentence: %+v %+v", p, answer)
	}
	// The ledger is sufficient even after observation pauses and the source goes away.
	a.closedEyes.enabled.Store(true)
	sourceSvc := a.mobileBriefSvc
	a.mobileBriefSvc = nil
	if _, err := a.UndoChatCorrection(id); err != nil {
		t.Fatal(err)
	}
	a.mobileBriefSvc = sourceSvc
	a.closedEyes.enabled.Store(false)
	if a.briefNotRightSources(time.Now())[source] {
		t.Fatal("undo still suppresses the sentence's source")
	}
	undo, err := a.store.LoadStreamItem(t.Context(), "correction-undo:"+id)
	if err != nil || undo == nil || p.UndoneSentence == "" || !strings.Contains(undo.ToolInput, `"sentence":"The original wording is back."`) {
		t.Fatalf("undo: %+v %v", undo, err)
	}
}

func TestChatCorrectionWorkAnswer(t *testing.T) {
	for _, later := range []bool{false, true} {
		a := newTodayContextTestApp(t, true)
		a.mobileBriefSvc = &workTestBriefSource{mobileTestBriefSource{brief: workTestBrief()}}
		a.notePhoneTurnSender(mobileTestDevice(mobileScopeChatWrite, mobileScopeChatCorrect))
		t.Cleanup(func() { phoneTurnCorrections.Delete(a) })
		c := correctionTestTool(t, a, &TodayChatContextRef{SourceKind: todayChatWorkKind, SourceID: workTestPrepID}, promptOriginPhone)
		id, p := submitTestCorrection(t, c, nil)
		entries := a.answersLedger().current()
		applied, judged := entries[workTestPrepID], entries["correction:"+id]
		if p.Applied != "edit" || p.MemoryID != "" || !applied.NotSent || applied.Surface != "phone" || applied.Edited == nil || applied.Edited.Before != "Lena asked two questions." || applied.Edited.After != p.Statement || judged.About == nil || judged.About.ID != workTestPrepID || judged.About.Kind != "work_item" || judged.Action != "edit" || judged.Surface != "phone" {
			t.Fatalf("edit: %+v %+v", applied, judged)
		}
		if later {
			if _, err := a.UndoWork(workTestPrepID); err != nil {
				t.Fatal(err)
			}
			if _, err := a.AnswerWork(workTestPrepID, WorkAnswerInput{Action: "no"}); err != nil {
				t.Fatal(err)
			}
			if _, err := a.UndoChatCorrection(id); !errors.Is(err, errWorkItemChanged) {
				t.Fatalf("later answer: %v", err)
			}
			row, err := a.store.LoadStreamItem(t.Context(), id)
			if err != nil || row == nil || strings.Contains(row.ToolInput, `"status":"undone"`) || a.answersLedger().current()[workTestPrepID].Action != "no" {
				t.Fatal("changed a later answer or the chat line")
			}
		} else {
			if _, err := a.undoChatCorrection(id, "phone"); err != nil {
				t.Fatal(err)
			}
			if _, ok := a.answersLedger().current()[workTestPrepID]; ok {
				t.Fatal("edit still stands")
			}
		}
	}
}

func TestChatCorrectionLinkedPrep(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	if err := a.store.SetUIState(t.Context(), permissionUIStateKey(calendarPermissionKey), "true"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	person := peopleTestContact(t, a, "Mei Chen")
	peopleTestPurpose(t, a, "Send the launch outline", person.DisplayName, PurposeOwnerUser, time.Time{})
	event := beforeYouTalkEvent("prep-event", person.ID, now.Add(20*time.Minute), now.Add(50*time.Minute))
	if err := a.store.UpsertCalendarEvents(t.Context(), []storage.CalendarEventRow{event}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	id, p := submitTestCorrection(t, correctionTestTool(t, a, nil, promptOriginMac), map[string]any{"kind": "work_item", "id": workPrepID(event.ID)})
	note, err := a.GetMemoryEntry(p.MemoryID)
	if err != nil || p.Applied != "note" || p.About.ID != workPrepID(event.ID) || !strings.Contains(note.Content, p.About.ID) {
		t.Fatalf("linked prep: %+v %v", p, err)
	}
	if _, err := a.UndoChatCorrection(id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GetMemoryEntry(p.MemoryID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("note after undo: %v", err)
	}
}

func TestChatCorrectionPersonReadGate(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	person := peopleTestContact(t, a, "Mei Chen")
	c := correctionTestTool(t, a, nil, promptOriginMac)
	id, p := submitTestCorrection(t, c, map[string]any{"kind": "person", "id": person.ID})
	note, err := a.GetMemoryEntry(p.MemoryID)
	if err != nil || !strings.Contains(note.Content, person.ID) || !strings.Contains(a.injectPeopleContext(person.DisplayName), p.Statement) {
		t.Fatalf("linked profile: %+v %v", p, err)
	}
	detail, err := a.GetPersonDetail(person.ID)
	if err != nil || !strings.Contains(detail.Notes, p.Statement) {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	profile, err := a.store.GetPrincipal(t.Context(), person.ID)
	if err != nil {
		t.Fatal(err)
	}
	prior := profile.CreatedFrom
	profile.CreatedFrom = "auto_test"
	if _, err := a.store.UpdatePrincipal(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.injectPeopleContext(person.DisplayName), p.Statement) || a.memorySourceAllowsRead(note.Content) {
		t.Fatal("withdrawn profile exposed a correction")
	}
	rejected, err := c.Execute(t.Context(), draftCall("hidden", map[string]any{"where": "Who you know", "statement": "Hidden target", "about": map[string]any{"kind": "person", "id": person.ID}}))
	if err == nil && !rejected.IsError {
		t.Fatal("accepted a hidden person")
	}
	profile.CreatedFrom = prior
	if _, err := a.store.UpdatePrincipal(t.Context(), profile); err != nil {
		t.Fatal(err)
	}
	a.closedEyes.enabled.Store(true)
	if a.injectPeopleContext(person.DisplayName) != "" {
		t.Fatal("paused profile was read")
	}
	a.closedEyes.enabled.Store(false)
	if _, err := a.UndoChatCorrection(id); err != nil {
		t.Fatal(err)
	}
	stored, err := a.store.GetPrincipal(t.Context(), person.ID)
	if err != nil || stored.Notes != profile.Notes {
		t.Fatalf("principal notes after Undo: %v", err)
	}
	detail, err = a.GetPersonDetail(person.ID)
	if err != nil || strings.Contains(detail.Notes, p.Statement) {
		t.Fatalf("detail kept the correction after Undo: %v", err)
	}
	if strings.Contains(a.injectPeopleContext(person.DisplayName), p.Statement) {
		t.Fatal("undone profile correction was read")
	}
}

func TestChatCorrectionInvalidTargetWritesNothing(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	c := correctionTestTool(t, a, nil, promptOriginMac)
	for _, kind := range []string{"work_item", "person", "brief_sentence", "unknown"} {
		r, err := c.Execute(t.Context(), draftCall("invalid", map[string]any{"statement": "A correction.", "where": "A page", "about": map[string]any{"kind": kind, "id": "missing"}}))
		if err == nil && !r.IsError {
			t.Fatal("invalid target accepted")
		}
	}
	entries, err := a.GetMemoryEntries()
	if err != nil || len(entries) != 0 || len(a.answersLedger().entries()) != 0 {
		t.Fatal("invalid target wrote a correction")
	}
}

func TestChatCorrectionExpiredUndoKeepsTheLine(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	a.mobileBriefSvc = &workTestBriefSource{mobileTestBriefSource{brief: workTestBrief()}}
	id, p := submitTestCorrection(t, correctionTestTool(t, a, nil, promptOriginMac), map[string]any{"kind": "work_item", "id": workTestPrepID})
	ledger := a.answersLedger()
	entries, err := ledger.readEntries()
	if err != nil {
		t.Fatal(err)
	}
	var data strings.Builder
	encoder := json.NewEncoder(&data)
	for _, record := range entries {
		if record.EntryID == p.EntryID {
			record.UndoUntil = mobileTimestamp(time.Now().Add(-time.Minute))
		}
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(ledger.path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UndoChatCorrection(id); !errors.Is(err, errWorkUndoClosed) {
		t.Fatalf("expired undo: %v", err)
	}
	row, err := a.store.LoadStreamItem(t.Context(), id)
	if err != nil || row == nil || strings.Contains(row.ToolInput, `"status":"undone"`) {
		t.Fatal("expired undo changed the line")
	}
	reply, err := a.store.LoadStreamItem(t.Context(), "correction-undo:"+id)
	if err != nil || reply != nil {
		t.Fatalf("expired undo reply: %v %v", reply, err)
	}
}

func TestChatCorrectionFailedReadIsAnError(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	c := correctionTestTool(t, a, nil, promptOriginMac)
	// Both the brief reader and the judgment ledger can return OS errors.
	// Their local paths must never reach the model.
	dir := t.TempDir()
	a.mobileBriefSvc = &mobileBriefService{store: newMobileBriefStore(dir)}
	result, applyErr := c.Execute(t.Context(), draftCall("missing-brief", map[string]any{"statement": "A correction.", "where": "The brief", "about": map[string]any{"kind": "brief_sentence", "id": workBriefSentenceID("2026-11-14-morning", 0)}}))
	if applyErr != nil || !result.IsError || strings.Contains(result.Content, dir) {
		t.Fatal("the brief read must return a fixed error to the model")
	}
	ledger := a.answersLedger()
	ledger.path = t.TempDir()
	r, err := c.Execute(t.Context(), draftCall("broken", map[string]any{"statement": "A correction.", "where": "About you"}))
	if err != nil || !r.IsError || strings.Contains(r.Content, ledger.path) {
		t.Fatal("unreadable ledger must return a fixed error to the model")
	}
	notes, err := a.GetMemoryEntries()
	if err != nil || len(notes) != 0 {
		t.Fatalf("failed read wrote a note: %v", err)
	}
}

func TestChatCorrectionClosedEyesPersonalNoteAndPhoneUndo(t *testing.T) {
	a := newTodayContextTestApp(t, true)
	a.closedEyes.enabled.Store(true)
	a.notePhoneTurnSender(mobileTestDevice(mobileScopeChatWrite, mobileScopeChatCorrect))
	t.Cleanup(func() { phoneTurnCorrections.Delete(a) })
	c := correctionTestTool(t, a, nil, promptOriginPhone)
	id, p := submitTestCorrection(t, c, nil)
	note, err := a.GetMemoryEntry(p.MemoryID)
	if err != nil || !strings.Contains(note.Content, p.Statement) {
		t.Fatal("paused correction did not save the person's words")
	}
	bridge := &mobileBridge{app: a, ctx: t.Context(), chat: newMobileChatState(nil)}
	reply := chatB10Post(t, bridge, "/chats/"+c.tag+"/items/"+id+"/correction/undo", map[string]any{"idempotencyKey": "paused-undo"})
	if reply.Code != http.StatusOK || !strings.Contains(reply.Body.String(), `"status":"undone"`) {
		t.Fatalf("phone Undo: %d %s", reply.Code, reply.Body.String())
	}
	if _, err := a.GetMemoryEntry(p.MemoryID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("note after Undo: %v", err)
	}
}

func TestChatCorrectionPhoneGoneAttachmentStillStartsTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bridge, _ := mobileTestChatBridge(t)
	main, err := bridge.app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	turns := &mobileTestTurns{}
	mobileChatRunnerOverride = mobileTestMessageOverride(turns)
	t.Cleanup(func() { mobileTestResetChatRunner(t, bridge); phoneTurnCorrections.Delete(bridge.app) })
	device := mobileTestDevice(mobileScopeChatWrite, mobileScopeChatRead, mobileScopeChatCorrect)
	reply := mobileTestChatCall(bridge, device, http.MethodPost, "/chats/"+main.LocalID+"/messages", `{"idempotencyKey":"gone","text":"My correction still matters.","about":{"kind":"item","id":"prep:gone"}}`)
	if reply.Code != http.StatusAccepted {
		t.Fatalf("send: %d %s", reply.Code, reply.Body.String())
	}
	mobileTestWaitFor(t, "plain turn", func() bool { return turns.count() == 1 })
	turns.mu.Lock()
	defer turns.mu.Unlock()
	if turns.prompts[0] != "My correction still matters." {
		t.Fatal("lost the person's original message")
	}
}
