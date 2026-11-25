package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
)

// chatOffGoldenWallClock lists the keys whose values legitimately differ
// between two runs of the same fixtures; the comparison replaces them with a
// fixed marker. Every other field must match exactly. They are wall-clock
// stamps of the read itself, not of the fixtures, and the two runs are a
// second or so apart:
//   - the work list's generatedAt (work_list.go buildWorkList);
//   - the phone Today snapshot's generatedAt (mobile_today.go buildToday).
//
// The sealed briefs, the phone's decrypted briefs, Today's brief, Needs you
// and the menu bar are compared with nothing normalized; the menu bar's
// Chat-only fields are asserted on their own (chatOffGoldenMenuChatOnly).
var chatOffGoldenWallClock = map[string]bool{"generatedAt": true}

// chatOffGoldenMenuChatOnly lists the menu bar fields that belong to Chat and
// so differ by design (scope cut A2 hides the Quick chat row). They are taken
// out of the compared menu bar and asserted on their own: with Chat off each
// is absent or false.
var chatOffGoldenMenuChatOnly = []string{"quickChatOn"}

// chatOffGoldenHandoffs are the handoff fixtures of newTestMobileBriefService,
// saved again with fixed times. handoff.Store stamps an entry saved without
// times with the wall clock, so the shared fixtures tie within a millisecond
// and the work list, which shows the newest results first, could pick a
// different set in each run.
var chatOffGoldenHandoffs = []string{"h-morning", "h-evening", "h-on_request", "h1", "h-1", "h-2"}

// chatOffGoldenRun is everything one run reads back, each as normalized JSON.
type chatOffGoldenRun struct {
	reads map[string]any
	// mainChatAdded is how many main-chat stream rows the run wrote.
	mainChatAdded int
	// menuChatOnly holds the menu bar's Chat-only fields, by name.
	menuChatOnly map[string]any
}

// Scope cut 2026-11, PR A1 (docs/plans/scope-cut-2026-11.md, section 6): with
// Chat off, a morning and an evening brief build, seal and reach a paired test
// phone unchanged, and the work list, Today and the menu bar read the same.
// Both runs use the same fixed clock and fixtures; only the chat switch
// differs. The chat-off run must also post nothing to the main chat.
func TestChatOffBriefsReachThePhoneUnchanged(t *testing.T) {
	for _, extras := range []string{"", string(featureBriefExtras)} {
		t.Run("brief-extras="+extras, func(t *testing.T) {
			chatOffGoldenCompare(t, extras)
		})
	}
}

func chatOffGoldenCompare(t *testing.T, extras string) {
	t.Helper()
	var on, off chatOffGoldenRun
	t.Run("chat on", func(t *testing.T) { on = chatOffGoldenBuild(t, strings.TrimSuffix("chat,"+extras, ",")) })
	t.Run("chat off", func(t *testing.T) { off = chatOffGoldenBuild(t, extras) })
	if t.Failed() {
		return
	}
	names := make([]string, 0, len(on.reads))
	for name := range on.reads {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if diff := chatOffGoldenDiff("", on.reads[name], off.reads[name]); diff != "" {
			t.Errorf("%s differs with Chat off: %s", name, diff)
		}
	}
	// What differs by design is asserted, not ignored: with Chat on a
	// brief-ready moment posts to the main chat and with Chat off nothing
	// does; with Chat off the menu bar's Chat-only fields are off.
	if on.mainChatAdded == 0 {
		t.Error("the chat-on run posted nothing to the main chat, so the run proves nothing about Chat off")
	}
	if off.mainChatAdded != 0 {
		t.Errorf("the chat-off run added %d main-chat rows, want none", off.mainChatAdded)
	}
	for _, field := range chatOffGoldenMenuChatOnly {
		if value, ok := off.menuChatOnly[field]; ok && value != false {
			t.Errorf("menu bar %s with Chat off = %v, want absent or false", field, value)
		}
		if value, ok := on.menuChatOnly[field]; ok && value != true {
			t.Errorf("menu bar %s with Chat on = %v, want true", field, value)
		}
	}
}

// chatOffGoldenBuild runs one day with the feature switches set to features:
// a phone paired through the local fake relay, a morning and an evening brief
// built by the real service on a fixed clock, each delivered to the phone and
// handed to the engagement service as in production, then the computer's
// reads.
func chatOffGoldenBuild(t *testing.T, features string) chatOffGoldenRun {
	t.Helper()
	t.Setenv(featureSwitchesEnv, features)
	ctx := context.Background()

	clock := &fakeMobileBriefClock{now: chatOffGoldenAt(7, 20)}
	svc, _ := newTestMobileBriefService(t, clock)
	// A written reading ("ok"), as a model reply would be, cited to the
	// brief's own evidence: the fixed lifecycle reading of the service tests.
	svc.compose = func(_ context.Context, _ *App, brief MobileBriefV1, _ []MobileBriefCard) ([]MobileBriefPara, string, []mobileBriefCardWords) {
		refs := make([]string, 0, len(brief.Evidence))
		for _, ref := range brief.Evidence {
			refs = append(refs, ref.Ref)
		}
		parts := strings.SplitN(mobileBriefPublishableTestReading, " The review can", 2)
		return []MobileBriefPara{{Text: parts[0], Refs: refs}, {Text: "The review can" + parts[1], Refs: refs}}, "ok", nil
	}
	work, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range chatOffGoldenHandoffs {
		at := chatOffGoldenAt(6, index).UnixMilli()
		if _, err := work.Save(handoff.Entry{ID: id, Title: "Shipped the plan", Source: "manual", Status: handoff.StatusCompleted, CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	svc.app.handoffStore = work
	rig := newEngagementTestRig(t)
	app := svc.app
	// One store for the app and its engagement moments, as in production
	// (newJourneyRig); the brief ready moment reports through the real
	// main-chat poster.
	app.store = rig.store
	app.engagement = rig.svc
	app.eventBroadcaster = NewEventBroadcaster()
	app.activeTags = map[string]bool{}
	app.mobileBriefSvc = svc
	rig.svc.deliver.Report = app.postEngagementReport
	rig.clock = clock.Now()

	// The phone: the bridge of mobileTestHub, on this app, with one paired
	// device registered at the local fake relay.
	dir := t.TempDir()
	identity, err := createMobileHubIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	relay := newMobileTestRelay(t, identity.Credential, identity.HubID)
	bridge, err := newMobileBridge(app, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	bridge.mu.Lock()
	bridge.identity = &identity
	bridge.mu.Unlock()
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	if err := bridge.enableRelay(relay.url(), false); err != nil {
		t.Fatal(err)
	}
	relay.acceptRegistration(t, mobileTestListed(phone))
	mobileTestWaitFor(t, "the relay connection", func() bool {
		return bridge.connectionStatus().RelayState == mobileRelayConnected
	})
	app.mobileBridgeMu.Lock()
	app.mobileBridge = bridge
	app.mobileBridgeMu.Unlock()

	before := chatOffGoldenMainChatRows(t, app)
	run := chatOffGoldenRun{reads: map[string]any{}}
	for _, step := range []struct {
		slot string
		at   time.Time
	}{
		{mobileBriefSlotMorning, chatOffGoldenAt(7, 20)},
		{mobileBriefSlotEvening, chatOffGoldenAt(18, 10)},
	} {
		clock.Set(step.at)
		rig.clock = step.at
		built, err := svc.generate(ctx, step.slot, mobileBriefReasonSchedule)
		if err != nil {
			t.Fatalf("%s brief: %v", step.slot, err)
		}
		if built.NarrativeStatus != "ok" {
			t.Fatalf("%s brief status = %q, want written words", step.slot, built.NarrativeStatus)
		}
		sealed, ok, err := svc.finishedBrief(built.BriefID)
		if err != nil || !ok {
			t.Fatalf("%s sealed copy: found %v, error %v", step.slot, ok, err)
		}
		run.reads[step.slot+" sealed"] = chatOffGoldenJSON(t, sealed, nil)
		run.reads[step.slot+" on the phone"] = chatOffGoldenPhoneBrief(t, relay, phone, identity, built.BriefID)
		// The brief ready moment waits at most for the recording; past that
		// hold the engagement pass delivers it, its chat card included.
		rig.clock = step.at.Add(mobileBriefNotifyHoldMax + time.Minute)
		rig.svc.Pass(ctx)
	}

	// A read that fails is kept as its error, so every read is compared and
	// a failure shows up as a difference between the runs.
	read := func(name string, value any, err error, normalized map[string]bool) {
		if err != nil {
			run.reads[name] = map[string]any{"error": err.Error()}
			return
		}
		run.reads[name] = chatOffGoldenJSON(t, value, normalized)
	}
	list, err := app.GetWorkList()
	read("work list", list, err, chatOffGoldenWallClock)
	today, err := app.GetLatestBrief()
	read("Today brief", today, err, nil)
	needs, err := app.GetNeedsYou(50)
	read("Today needs you", needs, err, nil)
	phoneToday, err := bridge.rebuildToday()
	read("phone Today snapshot", phoneToday, err, chatOffGoldenWallClock)
	// The menu bar's state line reads the watch word that the previous
	// refresh stored in a package global (menuBarWatch), so the first read in
	// a process differs from every later one. One warm-up read makes both
	// runs read the steady state, as the five-second tick does.
	if _, err := app.menuBarView("en", true); err != nil {
		t.Fatalf("menu bar warm-up: %v", err)
	}
	menu, err := app.menuBarView("en", true)
	read("menu bar", menu, err, nil)
	run.menuChatOnly = map[string]any{}
	if fields, ok := run.reads["menu bar"].(map[string]any); ok {
		for _, field := range chatOffGoldenMenuChatOnly {
			if value, present := fields[field]; present {
				run.menuChatOnly[field] = value
				delete(fields, field)
			}
		}
	}
	run.mainChatAdded = chatOffGoldenMainChatRows(t, app) - before
	return run
}

// chatOffGoldenAt is a fixed time of today's date. The brief service runs on
// this clock, but the phone's Today snapshot (mobile_today.go buildToday)
// reads the wall clock and its day shape accepts only today or tomorrow, so
// the fixture day is the real one rather than mobileBriefTestDay's.
func chatOffGoldenAt(hour, minute int) time.Time {
	year, month, day := time.Now().Date()
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

// chatOffGoldenPhoneBrief waits for the brief frame of briefID at the relay
// and returns what the phone decrypts from it. Earlier frames (a resend of
// the previous brief) are skipped.
func chatOffGoldenPhoneBrief(t *testing.T, relay *mobileTestRelay, phone *mobileTestPhone, identity mobileHubIdentity, briefID string) any {
	t.Helper()
	for range 8 {
		var frame mobileBriefFrame
		if err := json.Unmarshal(relay.nextFrame(t, "brief").raw, &frame); err != nil {
			t.Fatal(err)
		}
		plaintext := phone.openFromHub(t, identity.Public, identity.HubID, frame.Env)
		var header struct {
			BriefID string `json:"briefId"`
		}
		if err := json.Unmarshal(plaintext, &header); err != nil {
			t.Fatalf("the phone could not read the brief: %v", err)
		}
		if header.BriefID == briefID {
			return chatOffGoldenDecode(t, plaintext)
		}
	}
	t.Fatalf("the phone never received %s", briefID)
	return nil
}

// chatOffGoldenMainChatRows counts the main chat's stream rows without
// creating the main chat.
func chatOffGoldenMainChatRows(t *testing.T, app *App) int {
	t.Helper()
	ctx := context.Background()
	main, err := app.store.LoadMainSession(ctx)
	if err != nil || main == nil {
		return 0
	}
	rows, err := app.store.LoadStreamItems(ctx, main.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func chatOffGoldenJSON(t *testing.T, value any, normalized map[string]bool) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return chatOffGoldenNormalize(chatOffGoldenDecode(t, raw), normalized)
}

func chatOffGoldenDecode(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func chatOffGoldenNormalize(value any, normalized map[string]bool) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, inner := range typed {
			if normalized[key] {
				typed[key] = "normalized"
				continue
			}
			typed[key] = chatOffGoldenNormalize(inner, normalized)
		}
		return typed
	case []any:
		for index, inner := range typed {
			typed[index] = chatOffGoldenNormalize(inner, normalized)
		}
		return typed
	default:
		return value
	}
}

// chatOffGoldenDiff names the first path where two decoded JSON values
// differ. It prints the values cut short, so a failure shows where without
// printing a brief's words.
func chatOffGoldenDiff(path string, left, right any) string {
	switch l := left.(type) {
	case map[string]any:
		r, ok := right.(map[string]any)
		if !ok {
			return path + ": object vs " + chatOffGoldenShort(right)
		}
		keys := map[string]bool{}
		for key := range l {
			keys[key] = true
		}
		for key := range r {
			keys[key] = true
		}
		sorted := make([]string, 0, len(keys))
		for key := range keys {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		for _, key := range sorted {
			if diff := chatOffGoldenDiff(path+"."+key, l[key], r[key]); diff != "" {
				return diff
			}
		}
		return ""
	case []any:
		r, ok := right.([]any)
		if !ok || len(r) != len(l) {
			return fmt.Sprintf("%s: %d items vs %s", path, len(l), chatOffGoldenShort(right))
		}
		for index := range l {
			if diff := chatOffGoldenDiff(fmt.Sprintf("%s[%d]", path, index), l[index], r[index]); diff != "" {
				return diff
			}
		}
		return ""
	default:
		if fmt.Sprint(left) == fmt.Sprint(right) && fmt.Sprintf("%T", left) == fmt.Sprintf("%T", right) {
			return ""
		}
		return path + ": " + chatOffGoldenShort(left) + " vs " + chatOffGoldenShort(right)
	}
}

func chatOffGoldenShort(value any) string {
	text := fmt.Sprint(value)
	if items, ok := value.([]any); ok {
		text = fmt.Sprintf("%d items", len(items))
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 40 {
		text = text[:40] + "..."
	}
	return text
}
