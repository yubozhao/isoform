package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mobileScopeRequestPost(t *testing.T, bridge *mobileBridge, device mobileDeviceRecord, target string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/mobile/v1/devices/"+target+"/scope-request", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	bridge.handleAuthorized(w, r, device)
	return w
}

// A paired phone gains chat:correct without pairing again, only through the
// person's Allow on the computer, and nothing else can be added this way
// (hub 2026-11-16).
func TestAPairedPhoneGainsOnlyChatCorrectThroughAllow(t *testing.T) {
	dir := t.TempDir()
	bridge, err := newMobileBridge(&App{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	phone, other := newMobileTestPhone(t), newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeChatRead, mobileScopeChatWrite})
	mobileTestAddDevice(t, bridge, other, []string{mobileScopeChatRead, mobileScopeChatWrite})
	device := *bridge.devices[phone.deviceID]

	for _, add := range [][]string{{mobileScopeOffersDecide}, {mobileScopeChatCorrect, mobileScopeMemoryFix}, {}, {"push"}} {
		if w := mobileScopeRequestPost(t, bridge, device, phone.deviceID, map[string]any{"idempotencyKey": "k", "add": add}); w.Code != http.StatusBadRequest {
			t.Fatalf("asking for %v = %d", add, w.Code)
		}
	}
	if w := mobileScopeRequestPost(t, bridge, device, other.deviceID, map[string]any{"idempotencyKey": "k", "add": []string{mobileScopeChatCorrect}}); w.Code != http.StatusForbidden {
		t.Fatalf("asking for another phone = %d", w.Code)
	}
	readOnly := device
	readOnly.Scopes = []string{mobileScopeChatRead}
	if w := mobileScopeRequestPost(t, bridge, readOnly, phone.deviceID, map[string]any{"idempotencyKey": "k", "add": []string{mobileScopeChatCorrect}}); w.Code != http.StatusForbidden {
		t.Fatalf("asking without chat:write = %d", w.Code)
	}

	if w := mobileScopeRequestPost(t, bridge, device, phone.deviceID, map[string]any{"idempotencyKey": "k1", "add": []string{mobileScopeChatCorrect}}); w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"waiting"`) {
		t.Fatalf("ask = %d %s", w.Code, w.Body.String())
	}
	status := bridge.connectionStatus()
	var asked *MobileDeviceView
	for i := range status.Devices {
		if status.Devices[i].DeviceID == phone.deviceID {
			asked = &status.Devices[i]
		}
	}
	if asked == nil || asked.ScopeRequest == nil || asked.ScopeRequest.State != mobileScopeRequestWaiting || mobileHasScope(asked.Scopes, mobileScopeChatCorrect) {
		t.Fatalf("Settings shows %+v", asked)
	}
	// Asking is not holding: the phone's turns still cannot correct.
	if mobileHasScope(bridge.devices[phone.deviceID].Scopes, mobileScopeChatCorrect) {
		t.Fatal("the scope was granted before Allow")
	}

	// Not now holds for a day.
	if err := bridge.confirmScopeRequest(phone.deviceID, false); err != nil {
		t.Fatal(err)
	}
	if w := mobileScopeRequestPost(t, bridge, device, phone.deviceID, map[string]any{"idempotencyKey": "k2", "add": []string{mobileScopeChatCorrect}}); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "declined") {
		t.Fatalf("asking again after Not now = %d %s", w.Code, w.Body.String())
	}
	if err := bridge.confirmScopeRequest(phone.deviceID, true); err == nil {
		t.Fatal("Allow answered a declined request")
	}

	// Once the hold ends, a new ask and Allow add chat:correct and nothing else.
	bridge.mu.Lock()
	held := bridge.scopeRequests[phone.deviceID]
	held.at = held.at.Add(-mobileScopeDeclineHold)
	bridge.scopeRequests[phone.deviceID] = held
	bridge.mu.Unlock()
	if w := mobileScopeRequestPost(t, bridge, device, phone.deviceID, map[string]any{"idempotencyKey": "k3", "add": []string{mobileScopeChatCorrect}}); w.Code != http.StatusAccepted {
		t.Fatalf("asking after the hold = %d", w.Code)
	}
	if err := bridge.confirmScopeRequest(phone.deviceID, true); err != nil {
		t.Fatal(err)
	}
	got := bridge.devices[phone.deviceID].Scopes
	if len(got) != 3 || !mobileHasScope(got, mobileScopeChatCorrect) || !mobileHasScope(got, mobileScopeChatWrite) {
		t.Fatalf("scopes after Allow = %v", got)
	}
	if others := bridge.devices[other.deviceID].Scopes; mobileHasScope(others, mobileScopeChatCorrect) {
		t.Fatal("Allow reached another phone")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "devices.json"))
	if err != nil || !strings.Contains(string(stored), mobileScopeChatCorrect) || strings.Contains(string(stored), "scopeRequest") {
		t.Fatalf("stored devices = %s %v", stored, err)
	}
	audit, err := os.ReadFile(filepath.Join(dir, "mobile_audit.jsonl"))
	if err != nil || strings.Count(string(audit), `"method":"CONFIRM"`) != 2 {
		t.Fatalf("audit = %s %v", audit, err)
	}
	updated := *bridge.devices[phone.deviceID]
	if w := mobileScopeRequestPost(t, bridge, updated, phone.deviceID, map[string]any{"idempotencyKey": "k4", "add": []string{mobileScopeChatCorrect}}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"granted"`) {
		t.Fatalf("asking when held = %d %s", w.Code, w.Body.String())
	}
	if err := bridge.confirmScopeRequest(phone.deviceID, true); err == nil {
		t.Fatal("Allow answered with nothing waiting")
	}
}
