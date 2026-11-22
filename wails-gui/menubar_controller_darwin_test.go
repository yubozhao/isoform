//go:build darwin

package main

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"wails-gui/storage"
)

func TestMenuBarWaitingClickSurvivesQueueChanges(t *testing.T) {
	app := mainChatTestApp(t)
	if app.ctx != nil {
		t.Fatal("fixture must not have a native runtime")
	}
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// A chat row's id is validated against the chat, not its display rank.
	for i, id := range []string{"older-a", "older-b", "older-c", "chosen"} {
		card := mainChatProactive{MomentID: id, Kind: "waiting_on_reply", State: "pending", Headline: "Someone is waiting on your reply.", Body: "Halcyon"}
		if _, err := app.store.AppendStreamItem(t.Context(), main.LocalID, storage.StreamItemRow{ID: id, Kind: "system", ToolName: mainChatProactiveTool, ToolInput: mainChatViewPayload(t, card), CreatedAt: time.Now().Add(time.Duration(i) * time.Second).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	menuBarPresentation.Lock()
	stopped := menuBarPresentation.stopped
	menuBarPresentation.stopped = false
	menuBarPresentation.Unlock()
	t.Cleanup(func() {
		menuBarPresentation.Lock()
		menuBarPresentation.stopped = stopped
		menuBarPresentation.Unlock()
	})
	events := app.eventBroadcaster.AddClient("menubar-navigation")
	defer app.eventBroadcaster.RemoveClient("menubar-navigation")
	if err := app.runMenuBarCommand("waiting:chosen"); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-events:
		var event struct {
			Event string   `json:"event"`
			Args  []string `json:"args"`
		}
		if json.Unmarshal(payload, &event) != nil || event.Event != "menubar:waiting" || len(event.Args) != 2 || event.Args[0] != main.LocalID || event.Args[1] != "chosen" {
			t.Fatalf("navigation=%s", payload)
		}
	default:
		t.Fatal("clicked row was dropped after its display rank changed")
	}
	if err := app.runMenuBarCommand("waiting:missing"); err == nil {
		t.Fatal("fabricated row id must fail validation")
	}
	select {
	case payload := <-events:
		t.Fatalf("invalid navigation=%s", payload)
	default:
	}
}

func TestMenuBarSettingsCommandOpensWithoutTopic(t *testing.T) {
	app := mainChatTestApp(t)
	if app.ctx != nil {
		t.Fatal("fixture must not have a native runtime")
	}
	menuBarPresentation.Lock()
	stopped := menuBarPresentation.stopped
	menuBarPresentation.stopped = false
	menuBarPresentation.Unlock()
	t.Cleanup(func() {
		menuBarPresentation.Lock()
		menuBarPresentation.stopped = stopped
		menuBarPresentation.Unlock()
	})
	events := app.eventBroadcaster.AddClient("menubar-settings")
	defer app.eventBroadcaster.RemoveClient("menubar-settings")

	if err := app.runMenuBarCommand("settings"); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-events:
		var event struct {
			Event string   `json:"event"`
			Args  []string `json:"args"`
		}
		if json.Unmarshal(payload, &event) != nil || event.Event != "settings:open" || len(event.Args) != 0 {
			t.Fatalf("settings navigation=%s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("settings command did not emit an event")
	}
}

func TestMenuBarSeesCommandOpensSheet(t *testing.T) {
	app := mainChatTestApp(t)
	menuBarPresentation.Lock()
	stopped := menuBarPresentation.stopped
	menuBarPresentation.stopped = false
	menuBarPresentation.Unlock()
	t.Cleanup(func() {
		menuBarPresentation.Lock()
		menuBarPresentation.stopped = stopped
		menuBarPresentation.Unlock()
	})
	events := app.eventBroadcaster.AddClient("menubar-sees")
	defer app.eventBroadcaster.RemoveClient("menubar-sees")
	if err := app.runMenuBarCommand("sees"); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-events:
		var event struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(payload, &event) != nil || event.Event != "menubar:sees" {
			t.Fatalf("sheet navigation=%s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("sees command did not emit an event")
	}
}

func TestMenuBarAccountWithdrawalInvalidatesPresentation(t *testing.T) {
	app := mainChatTestApp(t)
	if app.ctx != nil {
		t.Fatal("fixture must not have a native runtime")
	}
	previousApp := menuBarApp
	menuBarApp = app
	menuBarPresentation.Lock()
	stopped, revision := menuBarPresentation.stopped, menuBarPresentation.revision
	attention, presented := menuBarPresentation.attention, menuBarPresentation.presentedIDs
	accountPrivacy := menuBarPresentation.accounts
	menuBarPresentation.accounts = menuBarAccountPrivacy{}
	menuBarPresentation.stopped = false
	menuBarPresentation.Unlock()
	t.Cleanup(func() {
		menuBarApp = previousApp
		menuBarPresentation.Lock()
		menuBarPresentation.stopped, menuBarPresentation.revision = stopped, revision
		menuBarPresentation.attention, menuBarPresentation.presentedIDs = attention, presented
		menuBarPresentation.accounts = accountPrivacy
		menuBarPresentation.Unlock()
	})
	account := ModelAccount{Provider: "claude", Kind: "subscription", Name: "Bo", On: true}
	update := func(accounts []ModelAccount) {
		app.magpieModels.mu.Lock()
		app.magpieModels.accounts = accounts
		app.magpieModels.mu.Unlock()
		app.emitModelAccountsUpdated()
	}
	update([]ModelAccount{account})
	before := menuBarPresentationRevision()
	account.Windows = []QuotaWindow{{Used: 90}}
	update([]ModelAccount{account})
	if menuBarPresentationRevision() != before {
		t.Fatal("quota-only refresh must preserve the current presentation")
	}
	for _, accounts := range [][]ModelAccount{nil, {{Provider: account.Provider, Kind: account.Kind, Name: account.Name, On: false}}} {
		update([]ModelAccount{account})
		menuBarPresentation.Lock()
		menuBarPresentation.presentedIDs = []string{"private-row"}
		menuBarPresentation.attention.seen = map[string]bool{"private-row": true}
		menuBarPresentation.Unlock()
		before := menuBarPresentationRevision()
		update(accounts)
		if menuBarPresentationRevision() <= before {
			t.Fatal("account withdrawal must fence an in-flight popover read")
		}
		menuBarPresentation.Lock()
		retained := len(menuBarPresentation.presentedIDs) + len(menuBarPresentation.attention.seen)
		menuBarPresentation.Unlock()
		if retained != 0 {
			t.Fatal("account withdrawal must discard cached presentation ids")
		}
	}
}

// Make my brief now always answers in the app (it was silent during
// cooldown, while a brief ran, or on failure; polish sweep 2026-10-27).
func TestMenuBarMakeBriefAlwaysAnswers(t *testing.T) {
	app := mainChatTestApp(t)
	menuBarPresentation.Lock()
	stopped := menuBarPresentation.stopped
	menuBarPresentation.stopped = false
	menuBarPresentation.Unlock()
	t.Cleanup(func() {
		menuBarPresentation.Lock()
		menuBarPresentation.stopped = stopped
		menuBarPresentation.Unlock()
	})
	events := app.eventBroadcaster.AddClient("menubar-make")
	defer app.eventBroadcaster.RemoveClient("menubar-make")
	if err := app.runMenuBarCommand("make"); err != nil {
		t.Fatalf("a failed request must answer in the app, not a modal alert: %v", err)
	}
	select {
	case payload := <-events:
		var event struct {
			Event string   `json:"event"`
			Args  []string `json:"args"`
		}
		if json.Unmarshal(payload, &event) != nil || event.Event != "menubar:make-brief" || len(event.Args) != 2 || event.Args[0] == "" {
			t.Fatalf("make brief event=%s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("Make my brief now was silent")
	}
}

// Bo, 2026-10-27: "The menu bar item is still slow... preload all of that
// information when we first launch the app." Launch builds the popover's
// whole view once in the background, with no click: the waiting list and the
// brief row are ready before the first open.
func TestMenuBarLaunchPreloadBuildsViewWithoutClick(t *testing.T) {
	// The brief's offered work is open in Your work, so Next and the count
	// show it before the first click.
	app, _ := workAnswersTestApp(t)
	if app.ctx != nil {
		t.Fatal("fixture must not have a native runtime")
	}
	previousApp, previousReady := menuBarApp, menuBarReady.Load()
	menuBarApp = app
	menuBarReady.Store(true)
	menuBarPresentation.Lock()
	stopped, waiting, presented, ids := menuBarPresentation.stopped, menuBarPresentation.waiting, menuBarPresentation.presented, menuBarPresentation.presentedIDs
	menuBarPresentation.stopped, menuBarPresentation.waiting, menuBarPresentation.presented = false, menuBarWaitingMemory{}, false
	menuBarPresentation.Unlock()
	menuBarLaunchPreload = sync.Once{}
	t.Cleanup(func() {
		menuBarApp = previousApp
		menuBarReady.Store(previousReady)
		menuBarPresentation.Lock()
		menuBarPresentation.stopped, menuBarPresentation.waiting, menuBarPresentation.presented, menuBarPresentation.presentedIDs = stopped, waiting, presented, ids
		menuBarPresentation.Unlock()
	})

	app.preloadMenuBarView(t.Context())
	// A second call (another startup path) does not build it twice.
	app.preloadMenuBarView(t.Context())
	deadline := time.Now().Add(10 * time.Second)
	for {
		menuBarPresentation.Lock()
		done, held := menuBarPresentation.presented, menuBarPresentation.waiting
		shownIDs := append([]string(nil), menuBarPresentation.presentedIDs...)
		menuBarPresentation.Unlock()
		if done {
			if !held.held || held.waiting == 0 || held.next == nil || held.next.ID != held.ids[0] || !slices.Contains(held.ids, workTestPrepID) {
				t.Fatalf("preload did not hold the work list: %+v", held)
			}
			if !slices.Contains(shownIDs, workTestPrepID) {
				t.Fatalf("preload did not present the waiting ids: %v", shownIDs)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("launch did not build the menu bar view without a click")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Warm afterwards: the next status tick reuses the list.
	menuBarPresentation.Lock()
	again := menuBarPresentation.waiting.begin(time.Now(), app.doingNowLanguage())
	menuBarPresentation.Unlock()
	if again {
		t.Fatal("the tick after the preload read the waiting list again")
	}
}
