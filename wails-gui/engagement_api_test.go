package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

func TestEngagementSettingsViewListsEveryKindWithDefaultsAndCounts(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if err := rig.svc.SetKindEnabled(ctx, engagement.KindWorkBlocked, false); err != nil {
		t.Fatal(err)
	}
	if err := rig.svc.MuteKind(ctx, engagement.KindPersonReappeared, rig.clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rig.svc.mu.Lock()
	rig.svc.paused[engagement.KindGoalCheckIn] = true
	rig.svc.mu.Unlock()
	sent, err := rig.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{Kind: "work_finished", DedupeKey: "work_finished:h1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.store.SetEngagementMomentState(ctx, sent.ID, storage.EngagementStateSent, "ok", storage.EngagementMomentRow{Channel: "push", SentAt: rig.clock.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := rig.store.RecordEngagementReceipt(ctx, storage.EngagementReceiptRow{MomentID: sent.ID, Channel: "inferred", State: storage.EngagementReceiptActed, Action: "item_completed", At: rig.clock.UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	view, err := engagementSettingsView(ctx, rig.svc)
	if err != nil {
		t.Fatal(err)
	}
	var all []engagement.Kind
	for _, kind := range engagement.AllKinds() {
		if !engagementKindSilent(kind) && kind != engagement.KindCapturePaused {
			all = append(all, kind)
		}
	}
	if !view.Available || len(view.Kinds) != len(all) {
		t.Fatalf("view lists %d kinds, want %d: %+v", len(view.Kinds), len(all), view)
	}
	byKind := map[string]EngagementKindView{}
	for i, kind := range view.Kinds {
		if kind.Kind != string(all[i]) {
			t.Fatalf("kind %d is %s, want %s (the order is the spec order)", i, kind.Kind, all[i])
		}
		byKind[kind.Kind] = kind
	}
	if k := byKind["work_blocked"]; k.Enabled || !k.DefaultOn {
		t.Fatalf("a kind turned off must show off: %+v", k)
	}
	if k := byKind["brief_ready"]; !k.Enabled || !k.DefaultOn {
		t.Fatalf("a default-on kind must show on: %+v", k)
	}
	if k := byKind["person_reappeared"]; k.Enabled || k.DefaultOn || k.MutedUntil == "" {
		t.Fatalf("a default-off muted kind: %+v", k)
	}
	if k := byKind["goal_check_in"]; !k.Paused {
		t.Fatalf("a paused kind must say so: %+v", k)
	}
	if k := byKind["work_finished"]; k.Sent != 1 || k.Acted != 1 || k.Dismissed != 0 {
		t.Fatalf("counts: %+v", k)
	}
	if view.QuietStart != "22:00" || view.QuietEnd != "08:00" || view.DailyInterruptBudget != 6 || view.StatsDays != 30 {
		t.Fatalf("global rules: %+v", view)
	}
	if len(view.DigestTimes) != 3 || view.DigestTimes[0] != "08:30" || view.DigestTimes[1] != "13:00" || view.DigestTimes[2] != "18:00" {
		t.Fatalf("digest times: %v", view.DigestTimes)
	}
	if engagementClock(0) != "00:00" || engagementClock(23*time.Hour+59*time.Minute) != "23:59" {
		t.Fatal("clock rendering")
	}
}

func TestEngagementSettingsBindings(t *testing.T) {
	bare := &App{}
	view, err := bare.GetEngagementSettings()
	if err != nil || view.Available || view.Kinds == nil || len(view.Kinds) != 0 || view.DigestTimes == nil {
		t.Fatalf("without a service the view says unavailable: %+v %v", view, err)
	}
	if _, err := bare.SetEngagementKindEnabled("brief_ready", false); !errors.Is(err, errEngagementUnavailable) {
		t.Fatalf("a switch without a service: %v", err)
	}

	rig := newEngagementTestRig(t)
	app := &App{store: rig.store, engagement: rig.svc}
	if _, err := app.SetEngagementKindEnabled("not_a_kind", true); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
	rig.svc.mu.Lock()
	rig.svc.paused[engagement.KindGoalCheckIn] = true
	rig.svc.mu.Unlock()
	view, err = app.SetEngagementKindEnabled(" goal_check_in ", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range view.Kinds {
		if kind.Kind == "goal_check_in" && (!kind.Enabled || kind.Paused) {
			t.Fatalf("turning a kind on must show it on and resumed: %+v", kind)
		}
	}
	prefs, err := rig.store.ListEngagementPrefs(context.Background())
	if err != nil || len(prefs) != 1 || prefs[0].Kind != "goal_check_in" || prefs[0].Enabled != 1 {
		t.Fatalf("the switch must be stored: %+v %v", prefs, err)
	}
	view, err = app.GetEngagementSettings()
	if err != nil || !view.Available {
		t.Fatalf("GetEngagementSettings: %+v %v", view, err)
	}
}

func TestMobileNotificationRoutes(t *testing.T) {
	bridge, store := mobileTestChatBridge(t)
	withoutPush := mobileTestDevice(mobileScopeDeskRead)
	if got := mobileTestChatCall(bridge, withoutPush, http.MethodGet, "/notifications", ""); got.Code != http.StatusForbidden {
		t.Fatalf("without the push scope: %d %s", got.Code, got.Body.String())
	}
	device := mobileTestDevice(mobileScopePush)
	if got := mobileTestChatCall(bridge, device, http.MethodGet, "/notifications", ""); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("before the service exists: %d %s", got.Code, got.Body.String())
	}
	bridge.app.engagement = newEngagementService(store, nil, engagementDelivery{})

	got := mobileTestChatCall(bridge, device, http.MethodGet, "/notifications", "")
	if got.Code != http.StatusOK {
		t.Fatalf("GET /notifications: %d %s", got.Code, got.Body.String())
	}
	var view EngagementSettingsView
	if err := json.Unmarshal(mobileTestDecodeReply(t, got).Data, &view); err != nil {
		t.Fatal(err)
	}
	// Three kinds, and nothing else, in this order (phone-rings v1.0.0 17c).
	if !view.Available || len(view.Kinds) != 3 || view.Kinds[0].Kind != "brief_ready" || view.Kinds[1].Kind != "meeting_prep" ||
		view.Kinds[2].Kind != "work_finished" || view.QuietStart != "22:00" {
		t.Fatalf("view: %+v", view)
	}

	for body, want := range map[string]int{
		`{"idempotencyKey":"n-bad","enabled":true}`: http.StatusBadRequest, // unknown kind below
	} {
		if got := mobileTestChatCall(bridge, device, http.MethodPost, "/notifications/not_a_kind", body); got.Code != want {
			t.Fatalf("unknown kind: %d %s", got.Code, got.Body.String())
		}
	}
	if got := mobileTestChatCall(bridge, device, http.MethodPost, "/notifications/goal_check_in", `{"idempotencyKey":"n-2"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled: %d %s", got.Code, got.Body.String())
	}
	if got := mobileTestChatCall(bridge, device, http.MethodPost, "/notifications/goal_check_in", `{"enabled":true}`); got.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d %s", got.Code, got.Body.String())
	}

	first := mobileTestChatCall(bridge, device, http.MethodPost, "/notifications/goal_check_in", `{"idempotencyKey":"n-1","enabled":true}`)
	if first.Code != http.StatusOK {
		t.Fatalf("switch on: %d %s", first.Code, first.Body.String())
	}
	if err := json.Unmarshal(mobileTestDecodeReply(t, first).Data, &view); err != nil {
		t.Fatal(err)
	}
	for _, kind := range view.Kinds {
		if kind.Kind == "goal_check_in" && !kind.Enabled {
			t.Fatalf("the answer must show the new state: %+v", kind)
		}
	}
	again := mobileTestChatCall(bridge, device, http.MethodPost, "/notifications/goal_check_in", `{"idempotencyKey":"n-1","enabled":false}`)
	if again.Code != http.StatusOK || again.Body.String() != first.Body.String() {
		t.Fatalf("a replayed switch must answer the stored result: %d %s", again.Code, again.Body.String())
	}
	prefs, err := store.ListEngagementPrefs(context.Background())
	if err != nil || len(prefs) != 1 || prefs[0].Enabled != 1 {
		t.Fatalf("a replay must not apply the second body: %+v %v", prefs, err)
	}

	if got := mobileRouteFamily(http.MethodPost, "/notifications/goal_check_in"); got != "POST /notifications/*" {
		t.Fatalf("route family: %q", got)
	}
	if got := mobileRouteFamily(http.MethodPost, "/notifications/a/b"); got != "" {
		t.Fatalf("a deeper path is not a route: %q", got)
	}
}

func TestEngagementRulesAreValidatedPersistedAndReloaded(t *testing.T) {
	if _, err := (&App{}).SetEngagementRules(EngagementRulesInput{QuietStart: "22:00", QuietEnd: "08:00"}); !errors.Is(err, errEngagementUnavailable) {
		t.Fatalf("rules without a service: %v", err)
	}
	rig := newEngagementTestRig(t)
	app := &App{store: rig.store, engagement: rig.svc}
	for _, bad := range []EngagementRulesInput{
		{QuietStart: "25:00", QuietEnd: "08:00", DailyInterruptBudget: 6},
		{QuietStart: "22:00", QuietEnd: "8", DailyInterruptBudget: 6},
		{QuietStart: "22:00", QuietEnd: "08:00", DailyInterruptBudget: -1},
		{QuietStart: "22:00", QuietEnd: "08:00", DailyInterruptBudget: 25},
	} {
		if _, err := app.SetEngagementRules(bad); err == nil {
			t.Fatalf("%+v must be refused", bad)
		}
	}
	view, err := app.SetEngagementRules(EngagementRulesInput{QuietStart: "23:30", QuietEnd: "07:00", DailyInterruptBudget: 3})
	if err != nil {
		t.Fatal(err)
	}
	if view.QuietStart != "23:30" || view.QuietEnd != "07:00" || view.DailyInterruptBudget != 3 {
		t.Fatalf("the view shows the new rules: %+v", view)
	}
	prefs := rig.svc.Prefs()
	if prefs.Quiet.Start != 23*time.Hour+30*time.Minute || prefs.Quiet.End != 7*time.Hour || prefs.DailyInterruptBudget != 3 {
		t.Fatalf("the service paces with the new rules: %+v", prefs)
	}

	fresh := newEngagementService(rig.store, nil, engagementDelivery{})
	if err := fresh.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded := fresh.Prefs()
	if reloaded.Quiet.Start != 23*time.Hour+30*time.Minute || reloaded.Quiet.End != 7*time.Hour || reloaded.DailyInterruptBudget != 3 {
		t.Fatalf("rules survive a restart: %+v", reloaded)
	}
	// The same start and end means no quiet hours; a zero budget means no cap.
	if _, err := app.SetEngagementRules(EngagementRulesInput{QuietStart: "09:00", QuietEnd: "09:00", DailyInterruptBudget: 0}); err != nil {
		t.Fatal(err)
	}
	prefs = rig.svc.Prefs()
	if prefs.Quiet.Contains(rig.clock) || prefs.DailyInterruptBudget != 0 {
		t.Fatalf("no quiet hours and no cap: %+v", prefs)
	}
}
