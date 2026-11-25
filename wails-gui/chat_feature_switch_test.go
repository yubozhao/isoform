package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// chatOff turns Chat off for one test; chatOn turns it back on.
func chatOff(t *testing.T) { t.Helper(); t.Setenv(featureSwitchesEnv, "") }
func chatOn(t *testing.T)  { t.Helper(); t.Setenv(featureSwitchesEnv, "chat") }

func isChatOff(err error) bool { return errors.Is(err, errFeatureOff(featureChat)) }

// Every entry point Chat has on the computer, off and on (scope cut
// 2026-11, A1). Off, each answers feature_off; on, none does.
func TestChatSwitchEveryEntryPoint(t *testing.T) {
	app := mainChatTestApp(t)
	ref := TodayChatContextRef{SourceKind: "brief", SourceID: "b1"}
	entries := map[string]func() error{
		"GetMainChat":                    func() error { _, err := app.GetMainChat(); return err },
		"GetMainChatOverview":            func() error { _, err := app.GetMainChatOverview(); return err },
		"ListMainChatWaitingItems":       func() error { _, err := app.ListMainChatWaitingItems("", 10); return err },
		"ListMainChatAnswerChips":        func() error { _, err := app.ListMainChatAnswerChips([]string{"i1"}); return err },
		"ListMainChatTopicItems":         func() error { _, err := app.ListMainChatTopicItems("topic-1", "", 10); return err },
		"GetMainChatSetup":               func() error { _, err := app.GetMainChatSetup("en"); return err },
		"AnswerMainChatSetup":            func() error { _, err := app.AnswerMainChatSetup("name", "Pat"); return err },
		"CloseMainChatSetupSteps":        func() error { return app.CloseMainChatSetupSteps([]string{"name"}) },
		"ReopenMainChatSetupSteps":       func() error { return app.ReopenMainChatSetupSteps([]string{"name"}) },
		"LoadMainChatProactiveItem":      func() error { _, err := app.LoadMainChatProactiveItem("m1"); return err },
		"ReturnToMainChatPlace":          func() error { _, err := app.ReturnToMainChatPlace("m1"); return err },
		"SetMainChatHideRoutine":         func() error { return app.SetMainChatHideRoutine(true) },
		"SendMainChat":                   func() error { _, err := app.SendMainChat("hi"); return err },
		"AskMainChat":                    func() error { _, err := app.AskMainChat("hi"); return err },
		"AskMainChatFromTodayChip":       func() error { _, err := app.AskMainChatFromTodayChip("hi"); return err },
		"SendMainChatFromTodayChip":      func() error { _, err := app.SendMainChatFromTodayChip("hi"); return err },
		"SendMainChatWithAttachments":    func() error { _, err := app.SendMainChatWithAttachments("hi", nil, nil); return err },
		"SendMainChatWithTopic":          func() error { _, err := app.SendMainChatWithTopic("hi", "topic-1"); return err },
		"SendMainChatWithContext":        func() error { _, err := app.SendMainChatWithContext("hi", ref); return err },
		"AskMainChatWithContext":         func() error { _, err := app.AskMainChatWithContext("hi", ref); return err },
		"SendMainChatProactiveDraft":     func() error { _, err := app.SendMainChatProactiveDraft("m1", "hi"); return err },
		"ResolveMainChatProactiveAction": func() error { _, err := app.ResolveMainChatProactiveAction("m1", "a1"); return err },
		"CorrectMainChatProactiveFact":   func() error { _, err := app.CorrectMainChatProactiveFact("m1", "no"); return err },
		"RecordMainChatProactiveReceipt": func() error { return app.RecordMainChatProactiveReceipt("m1", "seen") },
		"UndoChatCorrection":             func() error { _, err := app.UndoChatCorrection("i1"); return err },
		"AnswerChatHandoffCard":          func() error { _, err := app.AnswerChatHandoffCard("i1", "no"); return err },
		"BringBackToMainChat":            func() error { _, err := app.BringBackToMainChat("side-1"); return err },
		"AttachQuickChatContext":         func() error { return app.AttachQuickChatContext("spotlight_1", "com.example") },
		"SetQuickChatHotkey":             func() error { return app.SetQuickChatHotkey(app.GetQuickChatHotkey()) },
		"StartQuickChatHotkey":           func() error { return app.StartQuickChatHotkey() },
		"SendPrompt on a Quick chat ask": func() error {
			return app.SendPrompt("hi", "", "", "", "spotlight_1", false, false, "", "", "", "en", "")
		},
	}
	t.Run("off", func(t *testing.T) {
		chatOff(t)
		for name, call := range entries {
			if err := call(); !isChatOff(err) {
				t.Errorf("%s with Chat off: err=%v, want feature_off: chat", name, err)
			}
		}
		if waiting := app.GetMainChatWaiting(); !reflect.DeepEqual(waiting, MainChatWaitingState{}) {
			t.Errorf("GetMainChatWaiting with Chat off = %+v, want nothing", waiting)
		}
	})
	t.Run("on", func(t *testing.T) {
		chatOn(t)
		for name, call := range entries {
			switch name {
			case "SetQuickChatHotkey", "StartQuickChatHotkey":
				// These touch the computer's global hotkey; on, they are
				// covered by the Quick chat tests.
				continue
			}
			if err := call(); isChatOff(err) {
				t.Errorf("%s with Chat on answered feature_off", name)
			}
		}
	})
}

// A turn on the main chat or a side chat is refused with Chat off; a task's
// turn is not, so local tasks and hand-offs still run.
func TestChatSwitchRefusesOnlyChatSessions(t *testing.T) {
	app := mainChatTestApp(t)
	ctx := context.Background()
	main, err := app.ensureMainChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []storage.SessionRow{
		{LocalID: "side-1", Kind: storage.SessionKindSide, ParentID: main.LocalID, ProjectID: main.ProjectID, WorkspaceID: main.WorkspaceID},
		{LocalID: "task-1", ProjectID: main.ProjectID, WorkspaceID: main.WorkspaceID},
	} {
		if err := app.store.SaveSession(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	chatOff(t)
	for tag, refused := range map[string]bool{main.LocalID: true, "side-1": true, "spotlight_abc": true, "task-1": false, "handoff-unsaved": false} {
		if got := isChatOff(app.chatSessionRefused(tag)); got != refused {
			t.Errorf("%s refused=%v, want %v", tag, got, refused)
		}
	}
	if err := app.SendPrompt("hi", t.TempDir(), "", "", main.LocalID, false, false, "", "", "", "en", ""); !isChatOff(err) {
		t.Errorf("SendPrompt on the main chat with Chat off: err=%v", err)
	}
	if _, err := app.SendPromptWithTodayContext("hi", "", "", "", main.LocalID, nil, nil, false, false, "en", "", StreamItemData{ID: "u1", Kind: "user"}, TodayChatContextRef{SourceKind: "brief", SourceID: "b1"}); !isChatOff(err) {
		t.Errorf("SendPromptWithTodayContext on the main chat with Chat off: err=%v", err)
	}
	chatOn(t)
	for _, tag := range []string{main.LocalID, "side-1", "spotlight_abc"} {
		if err := app.chatSessionRefused(tag); err != nil {
			t.Errorf("%s refused with Chat on: %v", tag, err)
		}
	}
}

// With Chat off its phone routes are not there and /status names no chat
// flag; every other route answers as before.
func TestChatSwitchPhoneRoutes(t *testing.T) {
	bridge := &mobileBridge{}
	chatRoutes := []struct{ method, path string }{
		{http.MethodGet, "/chats"},
		{http.MethodPost, "/chats"},
		{http.MethodGet, "/chats/main"},
		{http.MethodPost, "/chats/main/messages"},
		{http.MethodPost, "/chats/c1/offer-messages"},
		{http.MethodPost, "/chats/c1/brief-messages"},
		{http.MethodPost, "/chats/c1/brief-opener"},
		{http.MethodPost, "/chats/c1/brief-paragraph"},
		{http.MethodPost, "/chats/c1/cancel"},
		{http.MethodPost, "/chats/c1/permissions/r1"},
		{http.MethodPost, "/chats/c1/proactive/actions"},
		{http.MethodPost, "/chats/c1/proactive/m1/receipts"},
		{http.MethodPost, "/chats/c1/proactive/m1/actions"},
		{http.MethodPost, "/chats/c1/items/i1/handoff"},
		{http.MethodPost, "/chats/c1/items/i1/correction/undo"},
		{http.MethodPost, "/briefs/b1/conversation"},
		{http.MethodGet, "/briefs/b1/ask-answer"},
	}
	kept := []struct{ method, path string }{
		{http.MethodPost, "/handoffs/h1/start"},
		{http.MethodGet, "/briefs/b1"},
		{http.MethodGet, "/topics/t1"},
	}
	call := func(method, path string) int {
		recorder := httptest.NewRecorder()
		// A device with no scopes: a route that is there answers 403.
		bridge.handleAuthorized(recorder, httptest.NewRequest(method, mobileAPIPrefix+path, nil), mobileTestDevice())
		return recorder.Code
	}
	chatOff(t)
	for _, route := range chatRoutes {
		if code := call(route.method, route.path); code != http.StatusNotFound {
			t.Errorf("%s %s with Chat off = %d, want 404", route.method, route.path, code)
		}
	}
	for _, route := range kept {
		if mobileChatRoute(route.method, route.path) {
			t.Errorf("%s %s is not Chat's", route.method, route.path)
		}
		if code := call(route.method, route.path); code == http.StatusNotFound {
			t.Errorf("%s %s with Chat off = 404", route.method, route.path)
		}
	}
	chatOn(t)
	for _, route := range chatRoutes {
		if code := call(route.method, route.path); code != http.StatusForbidden {
			t.Errorf("%s %s with Chat on = %d, want 403 (the route is there)", route.method, route.path, code)
		}
	}
}

func TestChatSwitchStatusFlags(t *testing.T) {
	read := func() map[string]any {
		recorder := httptest.NewRecorder()
		(&mobileBridge{}).serveStatus(recorder)
		var body struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data
	}
	chatOff(t)
	off := read()
	for _, flag := range []string{"chatMainSend", "chatB10", "scopeRequest", "briefAsk"} {
		if _, ok := off[flag]; ok {
			t.Errorf("/status with Chat off names %s", flag)
		}
	}
	chatOn(t)
	if on := read(); on["chatMainSend"] != true || on["briefAsk"] != true {
		t.Errorf("/status with Chat on lacks chatMainSend or briefAsk: %v", on)
	}
}

// Nothing reaches the main chat with Chat off: reports, Engagement's Report
// sink, a texted free-text reply and topic tags all do nothing.
func TestChatSwitchSinksPostNothing(t *testing.T) {
	app := mainChatTestApp(t)
	ctx := context.Background()
	main, err := app.ensureMainChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := func() int {
		page, err := app.store.LoadStreamItemsLatest(ctx, main.LocalID, 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Items)
	}
	before := count()
	chatOff(t)
	if id := app.postToMainChat("A report", mainChatReport{Source: mainChatSourceSchedule}); id != "" {
		t.Errorf("postToMainChat with Chat off posted %q", id)
	}
	if !app.postEngagementReport(engagement.Moment{Kind: engagement.Kind("brief_ready"), ID: "m1"}, engagementText{}) {
		t.Error("the Report sink with Chat off asked to retry")
	}
	sms := newSMSRig(t)
	sms.subscribe(t)
	if !app.handleSMSReplyWith(sms.ch, "reply-off", "Please send the files to my new address", time.Now()) {
		t.Error("a texted reply with Chat off was not handled")
	}
	if err := app.tagMainChatRowTopics(ctx, "i1", []mainChatEvidenceRef{{}}); err != nil {
		t.Errorf("topic tagging with Chat off: %v", err)
	}
	if tools := app.mainChatExtraTools(main.LocalID, promptOriginMac); len(tools) != 0 {
		t.Errorf("the main chat offers %d tools with Chat off", len(tools))
	}
	app.ensureMainChatIntroOnStartup()
	if after := count(); after != before {
		t.Fatalf("the main chat has %d rows with Chat off, had %d", after, before)
	}
	chatOn(t)
	if id := app.postToMainChat("A report", mainChatReport{Source: mainChatSourceSchedule}); id == "" {
		t.Error("postToMainChat with Chat on posted nothing")
	}
}

// With Chat off no Quick chat helper, hotkey, chat-app scanner or bot starts.
func TestChatSwitchStartsNoChatGoroutine(t *testing.T) {
	chatOff(t)
	app := &App{ctx: context.Background(), spotlight: newSpotlightSupervisor(0)}
	before := runtime.NumGoroutine()
	app.startQuickChat()
	app.startChatApps()
	time.Sleep(50 * time.Millisecond)
	if app.slackScanner != nil || app.telegramScanner != nil || app.discordScanner != nil {
		t.Fatal("a chat-app scanner was made with Chat off")
	}
	if app.spotlight.IsReady() {
		t.Fatal("the Quick chat helper started with Chat off")
	}
	// The orphan cleanup may run once and end; nothing stays running.
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines %d -> %d with Chat off", before, after)
	}
}
