package main

import (
	"encoding/json"
	"testing"
	"time"
)

// Every brief that is ready wakes each paired phone once (PROTOCOL.md 9.5): a
// ready frame follows the brief frame, naming only the device. The same brief
// version saved again (as each relaunch does) never wakes it twice, across a
// relaunch too; a newer brief does. No wake under Closed Eyes, and none for a
// phone without the push scope.
func TestMobileBriefReadyWakesEachPhoneOncePerBriefVersion(t *testing.T) {
	paused := false
	saved := mobileBriefReadyPaused
	mobileBriefReadyPaused = func(*App) bool { return paused }
	t.Cleanup(func() { mobileBriefReadyPaused = saved })

	bridge, relay := mobileTestHub(t)
	phone := newMobileTestPhone(t)
	quiet := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead, mobileScopePush})
	mobileTestAddDevice(t, bridge, quiet, []string{mobileScopeBriefRead})
	relay.acceptRegistration(t, mobileTestListed(phone, quiet))

	morning := MobileBriefV1{Version: 1, BriefID: "2026-11-22-morning", Slot: "morning", AsOf: "2026-11-22T14:50:40Z", HubName: "Mac"}
	if err := bridge.publishBrief(morning, false); err != nil {
		t.Fatal(err)
	}
	ready := relay.nextFrame(t, "ready")
	var wake map[string]any
	if err := json.Unmarshal(ready.raw, &wake); err != nil {
		t.Fatal(err)
	}
	if len(wake) != 2 || wake["t"] != "ready" || wake["deviceId"] != phone.deviceID {
		t.Fatalf("the ready frame must name only the device: %s", ready.raw)
	}
	relay.expectNoFrame(t, "ready", 200*time.Millisecond)

	// The relaunch re-save: the same brief version, no second wake.
	if err := bridge.publishBrief(morning, false); err != nil {
		t.Fatal(err)
	}
	relay.expectNoFrame(t, "ready", 200*time.Millisecond)
	entry, _ := bridge.briefOutbox.entry(phone.deviceID)
	if loadMobileBriefReadyLedger(bridge.briefOutbox.dir).claim(phone.deviceID, mobileBriefReadyKey(morning.BriefID, entry.Version)) {
		t.Fatal("a relaunch would wake the phone again for the same brief")
	}

	// Closed Eyes: no wake.
	paused = true
	evening := MobileBriefV1{Version: 1, BriefID: "2026-11-22-evening", Slot: "evening", AsOf: "2026-11-23T01:50:00Z", HubName: "Mac"}
	if err := bridge.publishBrief(evening, true); err != nil {
		t.Fatal(err)
	}
	relay.expectNoFrame(t, "ready", 200*time.Millisecond)

	// A newer brief with eyes open wakes the phone again.
	paused = false
	onRequest := MobileBriefV1{Version: 1, BriefID: "2026-11-22-on_request-190000", Slot: "on_request", AsOf: "2026-11-23T03:00:00Z", HubName: "Mac"}
	if err := bridge.publishBrief(onRequest, false); err != nil {
		t.Fatal(err)
	}
	if frame := relay.nextFrame(t, "ready"); !json.Valid(frame.raw) {
		t.Fatal("no wake for the newer brief")
	}
	relay.expectNoFrame(t, "ready", 200*time.Millisecond)
}
