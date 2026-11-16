package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"wails-gui/internal/engagement"
)

func TestMobileChatEngagementScopesReceiptsAndMute(t *testing.T) {
	bridge, store := mobileTestChatBridge(t)
	app := bridge.app
	app.engagement = newEngagementService(store, nil, engagementDelivery{})
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// A brief's card is not a phone chat item (pass-questions v1.0.0 rule
	// 8); any other card carries the same receipts and mute.
	item, ok := app.proactiveItem(engagement.Moment{ID: "phone-card", Kind: engagement.KindBriefMissed, SubjectID: "brief-1"}, engagementText{})
	if !ok || app.postProactiveItem(item, item.MomentID) == "" {
		t.Fatal("card not saved")
	}
	path := "/chats/" + main.LocalID + "/proactive/" + item.MomentID
	reader, writer := mobileTestDevice(mobileScopeChatRead), mobileTestDevice(mobileScopeChatWrite, mobileScopePush)
	for _, tc := range []struct {
		name, path, body string
		device           mobileDeviceRecord
		status           int
	}{
		{"receipt needs read", path + "/receipts", `{"idempotencyKey":"no-read","state":"opened"}`, writer, http.StatusForbidden},
		{"action needs write", path + "/actions", `{"idempotencyKey":"no-write","actionId":"mute_kind"}`, reader, http.StatusForbidden},
		{"cannot claim acted", path + "/receipts", `{"idempotencyKey":"acted","state":"acted"}`, reader, http.StatusBadRequest},
		{"wrong chat", "/chats/" + mobileTestChatID + "/proactive/" + item.MomentID + "/receipts", `{"idempotencyKey":"wrong-chat","state":"opened"}`, reader, http.StatusNotFound},
		{"missing card", "/chats/" + main.LocalID + "/proactive/absent/actions", `{"idempotencyKey":"absent","actionId":"mute_kind"}`, writer, http.StatusNotFound},
		{"unknown action", path + "/actions", `{"idempotencyKey":"unknown","actionId":"send"}`, writer, http.StatusForbidden},
		{"mute needs push", path + "/actions", `{"idempotencyKey":"no-push","actionId":"mute_kind"}`, mobileTestDevice(mobileScopeChatWrite), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := mobileTestChatCall(bridge, tc.device, http.MethodPost, tc.path, tc.body)
			if recorder.Code != tc.status {
				t.Fatalf("response = %d %s, want %d", recorder.Code, recorder.Body.String(), tc.status)
			}
		})
	}
	for _, state := range []string{"delivered", "opened"} {
		body := fmt.Sprintf(`{"idempotencyKey":%q,"state":%q}`, state, state)
		first := mobileTestChatCall(bridge, reader, http.MethodPost, path+"/receipts", body)
		repeat := mobileTestChatCall(bridge, reader, http.MethodPost, path+"/receipts", body)
		if first.Code != http.StatusOK || first.Body.String() != repeat.Body.String() {
			t.Fatalf("%s receipt did not replay: %d %s / %d %s", state, first.Code, first.Body.String(), repeat.Code, repeat.Body.String())
		}
	}
	page := mobileTestChatCall(bridge, reader, http.MethodGet, "/chats/"+main.LocalID, "")
	if !strings.Contains(page.Body.String(), `"mute_kind"`) || !strings.Contains(page.Body.String(), "静音此类消息") {
		t.Fatalf("phone JSON lacks localized mute action: %s", page.Body.String())
	}
	body := `{"idempotencyKey":"mute","actionId":"mute_kind"}`
	first := mobileTestChatCall(bridge, writer, http.MethodPost, path+"/actions", body)
	repeat := mobileTestChatCall(bridge, writer, http.MethodPost, path+"/actions", body)
	if first.Code != http.StatusOK || first.Body.String() != repeat.Body.String() || !strings.Contains(first.Body.String(), `"state":"pending"`) {
		t.Fatalf("mute replay = %d %s / %d %s", first.Code, first.Body.String(), repeat.Code, repeat.Body.String())
	}
	states := engagementReceiptStates(t, app, item.MomentID, string(engagement.ChannelChat))
	pushStates := engagementReceiptStates(t, app, item.MomentID, string(engagement.ChannelPush))
	if !states["delivered"] || !states["opened"] || !pushStates["muted"] || states["acted"] {
		t.Fatalf("phone chat receipts = %v, push actions = %v", states, pushStates)
	}
	if app.engagement.Prefs().Enabled[engagement.KindBriefReady] {
		t.Fatal("phone mute did not turn kind off")
	}
}

func TestMobileProactiveMuteRouteCannotGrantOtherCardActions(t *testing.T) {
	bridge, store := mobileTestChatBridge(t)
	app := bridge.app
	app.engagement = newEngagementService(store, nil, engagementDelivery{})
	main, err := app.ensureMainChat(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	card := mainChatProactive{MomentID: "suggestion-actions", Kind: string(engagement.KindSuggestion), State: "pending",
		ActionIDs: []string{"do_once", "add_to_upcoming", "approve"}, Target: mainChatTarget{Kind: "suggestion", ID: "suggestion-1"}}
	if app.postProactiveItem(card, card.MomentID) == "" {
		t.Fatal("suggestion card not saved")
	}
	path := "/chats/" + main.LocalID + "/proactive/" + card.MomentID + "/actions"
	for _, scopes := range [][]string{{mobileScopeChatWrite}, {mobileScopeChatWrite, mobileScopePush, mobileScopeHandoff}} {
		device := mobileTestDevice(scopes...)
		for _, action := range card.ActionIDs {
			body := fmt.Sprintf(`{"idempotencyKey":%q,"actionId":%q}`, action, action)
			response := mobileTestChatCall(bridge, device, http.MethodPost, path, body)
			if response.Code != http.StatusForbidden {
				t.Fatalf("generic action route accepted %s with %v: %d %s", action, scopes, response.Code, response.Body.String())
			}
		}
	}
	stored := mainChatProactiveItems(t, app)[0]
	if stored.State != "pending" || stored.KindMuted {
		t.Fatalf("refused action changed the card: %+v", stored)
	}
	states := engagementReceiptStates(t, app, card.MomentID, string(engagement.ChannelPush))
	if len(states) != 0 {
		t.Fatalf("refused action created an engagement receipt: %v", states)
	}
}
