package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/proactivevoice"
	"wails-gui/storage"
)

func proactiveVoiceDeliveryBrief(t *testing.T, app *App) (engagement.Moment, string) {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	event := proactiveVoiceTestEvent(t, app, now)
	brief := MobileBriefV1{Version: 1, BriefID: now.Format("2006-01-02") + "-morning", Slot: mobileBriefSlotMorning, AsOf: now.Format(time.RFC3339),
		Evidence: []MobileBriefRef{{Ref: "e1", SourceKind: mobileBriefCalendarSource, SourceID: event.ID}},
		Sections: MobileBriefSections{Meetings: []MobileBriefItem{{Title: event.Title, Refs: []string{"e1"}}}}}
	dir := t.TempDir()
	store := newMobileBriefStore(dir)
	if err := store.save(brief, now); err != nil {
		t.Fatal(err)
	}
	// Built the way newMobileBriefService builds it, so the read path sees the
	// same stores production does.
	app.mobileBriefSvc = &mobileBriefService{app: app, store: store, config: newMobileBriefConfigStore(dir), runs: newMobileBriefRunStore(dir), now: func() time.Time { return now }}
	app.runner = proactiveVoiceTestRouter(&proactiveVoiceTestRunner{raw: `{"headline":"Design review is in your brief.","body":"Open your brief when you have a moment.","draft":null,"actions":["open"],"evidence":["b1"],"abstain":false,"reason":null}`})
	return engagement.Moment{ID: "voice-brief", Kind: engagement.KindBriefReady, SubjectID: brief.BriefID, CreatedAt: now}, event.Title
}

func proactiveVoicePhoneRead(t *testing.T, app *App, chatID string) (mobileTestChatDetail, string) {
	t.Helper()
	bridge, err := newMobileBridge(app, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Stop()
	recorder := mobileTestChatCall(bridge, mobileTestDevice(mobileScopeChatRead), http.MethodGet, "/chats/"+chatID+"?after=0", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("phone read: %d %s", recorder.Code, recorder.Body.String())
	}
	data := mobileTestDecodeReply(t, recorder).Data
	var detail mobileTestChatDetail
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatal(err)
	}
	return detail, string(data)
}

func TestProactiveVoicePhoneWithdrawsPersistedBriefAfterCalendarRevocation(t *testing.T) {
	proactiveVoiceFallbackTestHome(t)
	app := mainChatTestApp(t)
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{})
	moment, title := proactiveVoiceDeliveryBrief(t, app)
	if !app.postEngagementReport(moment, engagementText{}) {
		t.Fatal("brief was not persisted")
	}
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored := mainChatProactiveItems(t, app)
	if len(stored) != 1 || stored[0].VoiceVersion == "" || stored[0].VoiceReceipt == "" {
		t.Fatalf("fixture did not persist validated copy: %+v", stored)
	}
	receipt, err := app.store.GetUIState(t.Context(), proactiveVoiceReceiptPrefix+stored[0].VoiceReceipt)
	if err != nil || !strings.Contains(receipt, "calendar-local-only") || strings.Contains(receipt, title) {
		t.Fatalf("missing local provenance or copied text: %s %v", receipt, err)
	}
	// The brief lives on Today: its card is not a phone chat item
	// (pass-questions v1.0.0 rule 8), so nothing of it reaches the phone.
	assertNoPhoneBriefCard := func() {
		t.Helper()
		got, wire := proactiveVoicePhoneRead(t, app, main.LocalID)
		if len(got.Items) != 0 || strings.Contains(wire, title) || strings.Contains(wire, "voiceReceipt") || strings.Contains(wire, "calendar-local-only") {
			t.Fatalf("the brief card reached the phone's chat: %s", wire)
		}
	}
	assertNoPhoneBriefCard()
	assertDesktopCopy := func(wantMuted bool) {
		t.Helper()
		page, err := app.LoadStreamItemsPage(main.LocalID, -1, 50)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("desktop read: %+v %v", page, err)
		}
		var card mainChatProactive
		if err := json.Unmarshal([]byte(page.Items[0].ToolInput), &card); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(page.Items[0].Content, title) || card.VoiceVersion != stored[0].VoiceVersion || card.KindMuted != wantMuted || card.VoiceReceipt != "" {
			t.Fatalf("desktop lost generated copy or current preference: %+v", page.Items[0])
		}
	}
	assertDesktopCopy(false)
	if state, err := app.ResolveMainChatProactiveAction(moment.ID, engagement.ActionMuteKind); err != nil || state != stored[0].State {
		t.Fatalf("mute changed the reminder state: %q %v", state, err)
	}
	if !engagementReceiptStates(t, app, moment.ID, string(engagement.ChannelDesktop))["muted"] {
		t.Fatal("mute receipt missing")
	}
	assertNoPhoneBriefCard()
	assertDesktopCopy(true)
	if err := app.store.SetUIState(t.Context(), permissionUIStateKey(calendarPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	if _, reason := app.loadProactiveVoiceSource(t.Context(), moment, time.Now(), "en"); reason == "" {
		t.Fatal("withdrawal still admits calendar source")
	}
	assertNoPhoneBriefCard()
	page, err := app.LoadStreamItemsPage(main.LocalID, -1, 50)
	raw, _ := json.Marshal(page)
	if err != nil || len(page.Items) != 1 || strings.Contains(string(raw), title) || strings.Contains(string(raw), "voiceReceipt") {
		t.Fatalf("desktop retained revoked detail: %s %v", raw, err)
	}
	if got := mainChatProactiveItems(t, app); len(got) != 1 || got[0].VoiceReceipt != stored[0].VoiceReceipt {
		t.Fatal("delivery rewrote history")
	}
}

func TestProactiveVoicePhoneWithdrawsDigestDraftAndEveryLocale(t *testing.T) {
	proactiveVoiceFallbackTestHome(t)
	app := mainChatTestApp(t)
	moment, title := proactiveVoiceDeliveryBrief(t, app)
	child, ok := app.proactiveItem(moment, engagementText{})
	if !ok || child.VoiceVersion == "" {
		t.Fatal("generated fixture unavailable")
	}
	// Legacy generated drafts cannot prove provenance. Clear every representation.
	draft := mainChatProactive{MomentID: "legacy-draft", Kind: "waiting_on_reply", Headline: "Dana private", Body: "Private body", VoiceVersion: proactivevoice.PromptVersion, Draft: true,
		DraftContent: &mainChatProactiveDraft{To: "private@example.com", Subject: "Private subject", Text: "Private draft"},
		ActionIDs:    []string{"copy_draft"}, ActionLabels: map[string]string{"copy_draft": "Private action"}, State: "pending",
		Localized: map[string]mainChatProactiveCopy{"en": {Body: "Private English"}, "zh-CN": {Body: "Private Chinese"}, "other": {Body: "Private extra"}}}
	digest := mainChatProactive{MomentID: "digest-voice", Kind: "digest", Headline: title, Body: title, State: "pending", Items: []mainChatProactive{child, draft}}
	if app.postProactiveItem(digest, "digest-voice") == "" {
		t.Fatal("digest persistence failed")
	}
	if err := app.store.SetUIState(t.Context(), permissionUIStateKey(calendarPermissionKey), "false"); err != nil {
		t.Fatal(err)
	}
	main, _ := app.ensureMainChat(t.Context())
	detail, wire := proactiveVoicePhoneRead(t, app, main.LocalID)
	for _, denied := range []string{title, "Private", "Dana private", "private@example.com", "copy_draft", "voiceReceipt"} {
		if strings.Contains(wire, denied) {
			t.Fatalf("digest retained %q: %s", denied, wire)
		}
	}
	if len(detail.Items) != 1 || detail.Items[0].Proactive == nil || len(detail.Items[0].Proactive.Items) != 2 || detail.Items[0].Proactive.Items[1].DraftContent != nil {
		t.Fatalf("digest identity/children lost: %s", wire)
	}
}

func TestProactiveVoicePhoneWithdrawsCaptureStatusAfterRecovery(t *testing.T) {
	proactiveVoiceFallbackTestHome(t)
	previous := proactiveVoiceCaptureSnapshots
	t.Cleanup(func() { proactiveVoiceCaptureSnapshots = previous })
	off := true
	proactiveVoiceCaptureSnapshots = func(*App) []captureSnapshot {
		return []captureSnapshot{{Kind: "screen", Granted: true, Enabled: !off, Running: !off}}
	}
	app := mainChatTestApp(t)
	now := time.Now().Truncate(time.Millisecond)
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{})
	app.engagement.now = func() time.Time { return now }
	app.runner = proactiveVoiceTestRouter(&proactiveVoiceTestRunner{raw: `{"headline":"Screen capture has been off for 20 minutes.","body":"Turn it back on from the menu bar.","draft":null,"actions":["open_settings"],"evidence":["m1"],"abstain":false,"reason":null}`})
	m := engagement.Moment{ID: "capture-status", Kind: engagement.KindCapturePaused, SubjectID: "screen", CreatedAt: now.Add(-20 * time.Minute)}
	if !app.postEngagementReport(m, engagementText{}) {
		t.Fatal("status persistence failed")
	}
	main, _ := app.ensureMainChat(t.Context())
	before, wire := proactiveVoicePhoneRead(t, app, main.LocalID)
	if len(before.Items) != 1 || before.Items[0].Status == nil || len(before.Items[0].Status.Entries) != 1 || !strings.Contains(wire, "20 minutes") || strings.Contains(wire, "voiceReceipt") {
		t.Fatalf("generated status unavailable: %s", wire)
	}
	entry := before.Items[0].Status.Entries[0]
	if entry.MomentID != m.ID || entry.SentAt != now.UnixMilli() {
		t.Fatalf("generated status lost delivery attribution: %+v", entry)
	}
	off = false
	after, wire := proactiveVoicePhoneRead(t, app, main.LocalID)
	if len(after.Items) != 1 || after.Items[0].Status == nil || len(after.Items[0].Status.Entries) != 1 || strings.Contains(wire, "20 minutes") || strings.Contains(wire, "menu bar") || after.Items[0].Text != "Isoform stopped watching for a while." || after.Items[0].Status.Entries[0].Localized["zh-CN"] != "Isoform 暂停观察了一会儿。" {
		t.Fatalf("recovered capture retained generated status: %s", wire)
	}
	withdrawn := after.Items[0].Status.Entries[0]
	if withdrawn.ID != entry.ID || withdrawn.MomentID != entry.MomentID || withdrawn.SentAt != entry.SentAt {
		t.Fatalf("withdrawal changed delivery attribution: before=%+v after=%+v", entry, withdrawn)
	}
}

func TestProactiveVoicePhoneFailsClosedOnMissingReceiptAndBrokenPayload(t *testing.T) {
	proactiveVoiceFallbackTestHome(t)
	for _, tc := range []struct{ name, input string }{
		{"missing receipt", `{"momentId":"m","kind":"brief_ready","headline":"Private headline","body":"Private body","voiceVersion":"v1","state":"pending"}`},
		{"malformed", `{"momentId":`},
		{"oversized", strings.Repeat(" ", mobileChatPageBudget+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge, store := mobileTestChatBridge(t)
			_, err := store.AppendStreamItem(t.Context(), mobileTestChatID, storage.StreamItemRow{ID: "broken", Kind: "system", ToolName: mainChatProactiveTool, Content: "Private fallback", ToolInput: tc.input})
			if err != nil {
				t.Fatal(err)
			}
			detail, wire := proactiveVoicePhoneRead(t, bridge.app, mobileTestChatID)
			if strings.Contains(wire, "Private") || len(detail.Items) != 1 || detail.Cursor != 1 {
				t.Fatalf("unsafe fallback or stuck cursor: %s", wire)
			}
		})
	}
}

func TestProactiveVoiceReceiptRejectsReplacedCalendarOrigin(t *testing.T) {
	proactiveVoiceFallbackTestHome(t)
	app := newPrivacyReadTestApp(t)
	now := time.Now()
	event := proactiveVoiceTestEvent(t, app, now)
	for _, id := range []string{"original", "replacement"} {
		insertTestSession(t, app.activity.store, id, "done", 1000)
	}
	row, _, _ := app.store.GetCalendarEvent(t.Context(), event.ID)
	row.SessionID = "original"
	if err := app.store.UpsertCalendarEvents(t.Context(), []storage.CalendarEventRow{row}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	m := meetingPrepMoment(event, now, time.Local)
	source, reason := app.loadProactiveVoiceSource(t.Context(), m, now, "en")
	job, addresses, assembly := assembleProactiveVoiceJob(string(m.Kind), "en", true, now, source)
	item := mainChatProactive{MomentID: "replacement-check", Kind: string(m.Kind), Headline: event.Title, Body: "Open the calendar.", State: "pending", VoiceVersion: proactivevoice.PromptVersion}
	if reason != "" || assembly != "" || !app.saveProactiveVoiceReceipt(t.Context(), m, now, job, addresses, source.Provenance, &item) {
		t.Fatalf("receipt setup: %s %s", reason, assembly)
	}
	if !app.proactiveVoiceReceiptAllowsRead(t.Context(), item.VoiceReceipt, item.Kind, proactiveVoicePresentationHash(item), false) {
		t.Fatal("unchanged source rejected")
	}
	row.SessionID = "replacement"
	if err := app.store.UpsertCalendarEvents(t.Context(), []storage.CalendarEventRow{row}, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if app.proactiveVoiceReceiptAllowsRead(t.Context(), item.VoiceReceipt, item.Kind, proactiveVoicePresentationHash(item), false) {
		t.Fatal("unchanged title concealed replaced input origin")
	}
}

func TestProactiveVoiceLegacyReplyNotNowPreservesCopyReceipt(t *testing.T) {
	// Reconstruct the RC 1 hash before adding the catalog-owned local action.
	item := mainChatProactive{MomentID: "legacy", Kind: "waiting_on_reply", Headline: "Someone is waiting on your reply.", Body: "A checked source line", Draft: true, DraftContent: &mainChatProactiveDraft{Text: "A checked draft"}, ActionIDs: []string{"open", "dismiss", "mute_kind"}, ActionLabels: map[string]string{"open": "Open Mail", "dismiss": "Dismiss", "mute_kind": "Mute this kind"}, Localized: map[string]mainChatProactiveCopy{}}
	for _, lang := range engagement.Languages {
		catalog, _ := engagement.Copy(engagement.KindWaitingOnReply, lang)
		item.Localized[lang] = mainChatProactiveCopy{Headline: catalog.PushBody, Body: catalog.PushBody, ActionLabels: map[string]string{"open": catalog.Actions["open"], "dismiss": catalog.Actions["dismiss"], "mute_kind": catalog.Actions["mute_kind"]}}
	}
	oldHash := proactiveVoiceHash(item)
	normalizeMainChatProactiveActions(&item)
	item.State = "deferred"
	before := proactiveVoiceHash(item)
	if got := proactiveVoicePresentationHash(item); got != oldHash {
		t.Fatal("local action hydration invalidated legacy copy proof")
	}
	if proactiveVoiceHash(item) != before {
		t.Fatal("hash mutated the caller's card")
	}
	changed := item
	changed.Body = "Changed source line"
	if proactiveVoicePresentationHash(changed) == oldHash {
		t.Fatal("body change bypassed source receipt")
	}
	changed = item
	changed.Headline = "Changed headline"
	if proactiveVoicePresentationHash(changed) == oldHash {
		t.Fatal("headline change bypassed source receipt")
	}
	changed = item
	changed.DraftContent = &mainChatProactiveDraft{Text: "Changed draft"}
	if proactiveVoicePresentationHash(changed) == oldHash {
		t.Fatal("draft change bypassed source receipt")
	}
}
