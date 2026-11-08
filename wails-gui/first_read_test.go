package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

func TestFirstReadBuilder(t *testing.T) {
	if buildFirstRead(emptyPortrait("learning"), firstReadFacts{Person: "Sam", Tool: "Claude Code"}, 3, "en") != nil {
		t.Fatal("a portrait with no checked sentences produced a first read")
	}
	blank := emptyPortrait("ready")
	blank.Sentences = []AboutYouPortraitSentence{{Parts: []AboutYouPortraitPart{{Text: "  "}}}}
	if buildFirstRead(blank, firstReadFacts{}, 1, "en") != nil {
		t.Fatal("a blank sentence produced a first read")
	}
	portrait := emptyPortrait("ready")
	portrait.Sentences = []AboutYouPortraitSentence{
		{Parts: []AboutYouPortraitPart{{Text: "You build "}, {Text: "carefully."}}},
		{Parts: []AboutYouPortraitPart{{Text: "You work with Sam."}}},
		{Parts: []AboutYouPortraitPart{{Text: "A third sentence."}}},
	}
	got := buildFirstRead(portrait, firstReadFacts{Tool: "Claude Code"}, 0, "en")
	if got == nil || len(got.Sentences) != 2 || got.Sentences[0] != "You build carefully." || len(got.Help) != 0 || got.OfferCount != 0 {
		t.Fatalf("unexpected first read: %+v", got)
	}
	got = buildFirstRead(portrait, firstReadFacts{Person: "Sam", Tool: "Claude Code", BriefTime: "7:30 AM"}, 5, "en")
	if len(got.Help) != 3 || !strings.Contains(got.Help[0], "Sam") || !strings.Contains(got.Help[1], "7:30 AM") || !strings.Contains(got.Help[2], "Claude Code") || got.OfferCount != 3 {
		t.Fatalf("specific facts or offer cap lost: %+v", got)
	}
	got = buildFirstRead(portrait, firstReadFacts{Person: "Sam", Tool: "Claude Code"}, -1, "zh-CN")
	if len(got.Help) != 2 || !strings.Contains(got.Help[0], "Sam") || got.OfferCount != 0 {
		t.Fatalf("Chinese first read: %+v", got)
	}
}

func TestFirstReadProjectsSpecificHelpAndOffersToPhone(t *testing.T) {
	card := mainChatProactive{MomentID: "first-read:projection", Kind: "first_read", Headline: "What I see so far", Body: "You build.", State: "pending",
		Sentences: []string{"You build."}, Helps: []string{"Help Sam.", "Brief at 7:30."}, OfferCount: 2,
		ActionIDs: []string{}, ActionLabels: map[string]string{}, Target: mainChatTarget{Kind: mainChatTargetNone}}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	view, ok := mobileChatItemFromRow(storage.StreamItemRow{Kind: "system", ToolName: mainChatProactiveTool, ToolInput: string(raw)})
	if !ok || view.Proactive == nil || len(view.Proactive.Sentences) != 1 || len(view.Proactive.Helps) != 2 || view.Proactive.OfferCount != 2 {
		t.Fatalf("phone lost first-read fields: %+v", view)
	}
}

func TestFirstReadMobilePairingProjection(t *testing.T) {
	app, service, _, _ := newPortraitTestRig(t, 20)
	service.check(t.Context(), time.Now())
	portrait := mustPortraitRead(t, app, "ready")
	chat := mainChatTestApp(t)
	app.store, app.eventBroadcaster, app.activeTags = chat.store, chat.eventBroadcaster, chat.activeTags
	bridge, err := newMobileBridge(app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	device := mobileTestDevice(mobileScopeChatRead, mobileScopeOffersRead)
	device.CreatedAt = "2026-10-30T12:00:00Z"
	if app.postFirstRead(t.Context(), device.MobileDeviceView, portrait) == "" {
		t.Fatal("first read was not posted")
	}
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	read := func(phone mobileDeviceRecord, firstRead bool) mobileTestChatDetail {
		t.Helper()
		path := "/chats/" + main.LocalID + "?after=0"
		if firstRead {
			path += "&firstRead=1"
		}
		recorder := mobileTestChatCall(bridge, phone, http.MethodGet, path, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("chat: %d %s", recorder.Code, recorder.Body.String())
		}
		var detail mobileTestChatDetail
		if err := json.Unmarshal(mobileTestDecodeReply(t, recorder).Data, &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	shown := read(device, true)
	if len(shown.Items) != 1 || shown.Items[0].Proactive == nil || len(shown.Items[0].Proactive.Sentences) != 2 {
		t.Fatalf("paired phone: %+v", shown)
	}
	other := device
	other.CreatedAt = "2026-10-31T12:00:00Z"
	if hidden := read(other, true); len(hidden.Items) != 0 || hidden.Cursor != shown.Cursor {
		t.Fatalf("other pairing received a card or lost cursor: %+v", hidden)
	}
	if legacy := read(device, false); len(legacy.Items) != 0 || legacy.Cursor != shown.Cursor {
		t.Fatalf("older phone received an unsupported card: %+v", legacy)
	}
}

func firstReadAnswerTestRig(t *testing.T) (*App, *AboutYouPortraitService, mobileDeviceRecord, *time.Time) {
	t.Helper()
	app, service, _, _ := newPortraitTestRig(t, 20)
	now := time.Now()
	service.now = func() time.Time { return now }
	service.check(t.Context(), now)
	portrait := mustPortraitRead(t, app, "ready")
	chat := mainChatTestApp(t)
	app.store, app.eventBroadcaster, app.activeTags = chat.store, chat.eventBroadcaster, chat.activeTags
	device := mobileTestDevice(mobileScopeChatRead, mobileScopeChatWrite, mobileScopeMemoryFix)
	device.CreatedAt = "2026-10-30T12:00:00Z"
	if app.postFirstRead(t.Context(), device.MobileDeviceView, portrait) == "" {
		t.Fatal("first read was not posted")
	}
	return app, service, device, &now
}

func TestFirstReadConfirmAndUndo(t *testing.T) {
	app, _, device, now := firstReadAnswerTestRig(t)
	moment := firstReadMoment(device.MobileDeviceView)
	result, err := app.AnswerFirstRead(moment, "confirm_portrait", "")
	if err != nil || result.State != "acted" || result.ConfirmedAt != now.UnixMilli() {
		t.Fatalf("confirm: %+v %v", result, err)
	}
	if portrait := mustPortraitRead(t, app, "ready"); portrait.SaidAt != result.ConfirmedAt {
		t.Fatalf("portrait was not confirmed: %+v", portrait)
	}
	if repeat, err := app.AnswerFirstRead(moment, "confirm_portrait", ""); err != nil || repeat != result {
		t.Fatalf("confirmation retry: %+v %v", repeat, err)
	}
	*now = now.Add(4 * time.Second)
	if result, err := app.AnswerFirstRead(moment, "undo_portrait", ""); err != nil || result.State != "pending" {
		t.Fatalf("undo: %+v %v", result, err)
	}
	if portrait := mustPortraitRead(t, app, "ready"); portrait.SaidAt != 0 {
		t.Fatalf("undo did not restore portrait: %+v", portrait)
	}
	if _, err := app.AnswerFirstRead(moment, "confirm_portrait", ""); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(6 * time.Second)
	if _, err := app.AnswerFirstRead(moment, "undo_portrait", ""); err == nil {
		t.Fatal("undo remained available after five seconds")
	}
}

func TestFirstReadUndoPreservesPriorPortraitAnswer(t *testing.T) {
	app, service, device, now := firstReadAnswerTestRig(t)
	portrait := mustPortraitRead(t, app, "ready")
	if err := service.setSaid(portrait.GeneratedAt, true); err != nil {
		t.Fatal(err)
	}
	previous := now.UnixMilli()
	*now = now.Add(time.Second)
	moment := firstReadMoment(device.MobileDeviceView)
	if _, err := app.AnswerFirstRead(moment, "confirm_portrait", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AnswerFirstRead(moment, "undo_portrait", ""); err != nil {
		t.Fatal(err)
	}
	if portrait := mustPortraitRead(t, app, "ready"); portrait.SaidAt != previous {
		t.Fatalf("undo removed an earlier answer: %+v", portrait)
	}
}

func TestFirstReadRejectsChangedPortrait(t *testing.T) {
	app, _, device, _ := firstReadAnswerTestRig(t)
	path, err := portraitPath()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := readPortrait(path)
	if err != nil {
		t.Fatal(err)
	}
	doc.Sentences = nil
	if err := writePortrait(path, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AnswerFirstRead(firstReadMoment(device.MobileDeviceView), "confirm_portrait", ""); err == nil {
		t.Fatal("confirmed sentences that are no longer readable")
	}
}

func TestFirstReadCorrectionReceiptedAndSaved(t *testing.T) {
	app, _, device, _ := firstReadAnswerTestRig(t)
	bridge, err := newMobileBridge(app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	moment := firstReadMoment(device.MobileDeviceView)
	body, _ := json.Marshal(map[string]string{"idempotencyKey": "correction-1", "momentId": moment,
		"action": "correct_portrait", "text": "I work best later in the day."})
	call := func(phone mobileDeviceRecord, payload string) int {
		return mobileTestChatCall(bridge, phone, http.MethodPost, "/first-read/answer", payload).Code
	}
	withoutMemory := device
	withoutMemory.Scopes = []string{mobileScopeChatWrite}
	if status := call(withoutMemory, string(body)); status != http.StatusForbidden {
		t.Fatalf("missing memory scope: %d", status)
	}
	if status := call(device, string(body)); status != http.StatusOK {
		t.Fatalf("correction: %d", status)
	}
	if status := call(device, string(body)); status != http.StatusOK {
		t.Fatalf("retry: %d", status)
	}
	changed, _ := json.Marshal(map[string]string{"idempotencyKey": "correction-1", "momentId": moment,
		"action": "correct_portrait", "text": "A different correction."})
	if status := call(device, string(changed)); status != http.StatusConflict {
		t.Fatalf("receipt key reused for different words: %d", status)
	}
	entry, err := app.GetMemoryEntry("first-read-correction-" + engagement.HashPart(moment))
	if err != nil || !strings.Contains(entry.Content, "I work best later in the day.") {
		t.Fatalf("portrait correction: %+v %v", entry, err)
	}
	main, _ := app.ensureMainChat(t.Context())
	rows, err := app.store.LoadStreamItems(t.Context(), main.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range rows {
		if row.Kind == "user" && row.Content == "I work best later in the day." {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("correction messages: %d", count)
	}
	other := device
	other.CreatedAt = "2026-10-31T12:00:00Z"
	if status := call(other, string(body)); status != http.StatusBadRequest {
		t.Fatalf("another pairing answered the card: %d", status)
	}
}

func TestFirstReadDeliveryOncePerPairingAndPhoneProjection(t *testing.T) {
	app, service, _, path := newPortraitTestRig(t, 20)
	service.check(context.Background(), time.Now())
	portrait := mustPortraitRead(t, app, "ready")
	chat := mainChatTestApp(t)
	app.store, app.eventBroadcaster, app.activeTags = chat.store, chat.eventBroadcaster, chat.activeTags
	device := MobileDeviceView{DeviceID: "phone-1", CreatedAt: "2026-10-30T12:00:00Z"}
	if id := app.postFirstRead(t.Context(), device, emptyPortrait("learning")); id != "" {
		t.Fatalf("posted without sentences: %s", id)
	}
	first := app.postFirstRead(t.Context(), device, portrait)
	if first == "" || app.postFirstRead(t.Context(), device, portrait) != "" {
		t.Fatal("first pairing was not delivered exactly once")
	}
	device.CreatedAt = "2026-10-31T12:00:00Z"
	if id := app.postFirstRead(t.Context(), device, portrait); id == "" || id == first {
		t.Fatal("a new pairing did not get its own first read")
	}
	rows := mainChatStructuredRows(t, app, mainChatProactiveTool)
	if len(rows) != 2 || strings.Contains(rows[0].ToolInput, portraitParagraph(portrait)) || rows[0].Content != "" {
		t.Fatalf("wrong rows or portrait copied into storage: %+v", rows)
	}
	for _, sentence := range buildFirstRead(portrait, firstReadFacts{}, 0, "en").Sentences {
		if strings.Contains(rows[0].ToolInput, sentence) {
			t.Fatal("a portrait sentence was copied into storage")
		}
	}
	row := rows[0]
	row.Content, row.ToolInput = app.projectProactiveVoiceDelivery(t.Context(), row.ToolName, row.Content, row.ToolInput)
	view, ok := mobileChatItemFromRow(row)
	if !ok || view.Kind != "proactive" || view.Proactive == nil || view.Proactive.Kind != "first_read" || len(view.Proactive.Sentences) != 2 {
		t.Fatalf("phone projection: %+v", view)
	}
	// The old phone decodes the existing proactive envelope and ignores its
	// additional fields and unknown action IDs.
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Kind      string `json:"kind"`
		Proactive struct {
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"proactive"`
	}
	if err := json.Unmarshal(raw, &old); err != nil || old.Kind != "proactive" || old.Proactive.Kind != "first_read" || old.Proactive.Body == "" {
		t.Fatalf("legacy envelope: %+v %v", old, err)
	}
	doc, err := readPortrait(path)
	if err != nil {
		t.Fatal(err)
	}
	doc.Sentences = nil
	if err := writePortrait(path, doc); err != nil {
		t.Fatal(err)
	}
	_, input := app.projectProactiveVoiceDelivery(t.Context(), rows[0].ToolName, rows[0].Content, rows[0].ToolInput)
	if strings.Contains(input, view.Proactive.Sentences[0]) {
		t.Fatal("withdrawn portrait survived replay")
	}
}

func TestFirstReadWaitsForFirstBriefChatDelivery(t *testing.T) {
	app, rig := newJourneyRig(t)
	ctx := context.Background()
	finishJourneyOnboarding(t, app, rig.clock)
	rig.svc.Pass(ctx) // welcome
	rig.clock = rig.clock.AddDate(0, 0, 1)
	brief := MobileBriefV1{BriefID: "first-morning", Slot: mobileBriefSlotMorning}
	if !app.produceFirstWeekBrief(brief) {
		t.Fatal("first brief was not submitted")
	}
	if !app.firstReadWaitsForBrief(ctx, brief) {
		t.Fatal("first read overtook a pending first brief")
	}
	rig.svc.Pass(ctx)
	if app.firstReadWaitsForBrief(ctx, brief) {
		t.Fatal("first read remained blocked after the brief was delivered")
	}
}

func TestFirstReadCorrectionUndoRestoresButtons(t *testing.T) {
	app, service, device, now := firstReadAnswerTestRig(t)
	moment := firstReadMoment(device.MobileDeviceView)
	const words = "I work best later in the day."
	if result, err := app.AnswerFirstRead(moment, "correct_portrait", words); err != nil || result.State != "corrected" {
		t.Fatalf("correction: %+v %v", result, err)
	}
	noteID := "first-read-correction-" + engagement.HashPart(moment)
	if _, err := app.GetMemoryEntry(noteID); err != nil {
		t.Fatalf("correction memory missing before undo: %v", err)
	}
	*now = now.Add(6 * time.Second)
	if _, err := app.AnswerFirstRead(moment, "undo_portrait", ""); err == nil {
		t.Fatal("correction undo remained available after five seconds")
	}
	*now = now.Add(-6 * time.Second)
	if result, err := app.AnswerFirstRead(moment, "undo_portrait", ""); err != nil || result.State != "pending" {
		t.Fatalf("correction undo: %+v %v", result, err)
	}
	if _, err := app.GetMemoryEntry(noteID); err == nil {
		t.Fatal("undo left the correction memory behind")
	}
	if service.noteID == noteID {
		t.Fatal("undo left the pending portrait note behind")
	}
	main, _ := app.ensureMainChat(t.Context())
	rows, err := app.store.LoadStreamItems(t.Context(), main.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Kind == "user" && row.Content == words {
			t.Fatal("undo left the correction message in the chat")
		}
	}
	row, err := app.store.FindProactiveStreamItem(t.Context(), main.LocalID, moment)
	if err != nil || row == nil {
		t.Fatalf("card lost: %v", err)
	}
	var card mainChatProactive
	if err := json.Unmarshal([]byte(row.ToolInput), &card); err != nil || card.State != "pending" || card.FirstReadCorrectedAt != 0 || card.FirstReadCorrectionHash != "" {
		t.Fatalf("card after undo: %+v %v", card, err)
	}
	if repeat, err := app.AnswerFirstRead(moment, "undo_portrait", ""); err != nil || repeat.State != "pending" {
		t.Fatalf("repeated undo: %+v %v", repeat, err)
	}
	if result, err := app.AnswerFirstRead(moment, "correct_portrait", "Mornings instead."); err != nil || result.State != "corrected" {
		t.Fatalf("answering again after undo: %+v %v", result, err)
	}
}

func TestFirstReadBlankCorrectionSavesNothing(t *testing.T) {
	app, _, device, now := firstReadAnswerTestRig(t)
	moment := firstReadMoment(device.MobileDeviceView)
	if result, err := app.AnswerFirstRead(moment, "correct_portrait", ""); err != nil || result.State != "corrected" {
		t.Fatalf("blank correction: %+v %v", result, err)
	}
	if portrait := mustPortraitRead(t, app, "ready"); portrait.SaidAt != 0 {
		t.Fatalf("blank correction left the read confirmed: %+v", portrait)
	}
	if _, err := app.GetMemoryEntry("first-read-correction-" + engagement.HashPart(moment)); err == nil {
		t.Fatal("blank correction saved a memory entry")
	}
	main, _ := app.ensureMainChat(t.Context())
	rows, err := app.store.LoadStreamItems(t.Context(), main.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Kind == "user" {
			t.Fatalf("blank correction added a chat message: %q", row.Content)
		}
	}
	if result, err := app.AnswerFirstRead(moment, "undo_portrait", ""); err != nil || result.State != "pending" {
		t.Fatalf("undo blank correction: %+v %v", result, err)
	}
	*now = now.Add(time.Second)
	if _, err := app.AnswerFirstRead(moment, "correct_portrait", ""); err != nil {
		t.Fatalf("answering blank again: %v", err)
	}
}
