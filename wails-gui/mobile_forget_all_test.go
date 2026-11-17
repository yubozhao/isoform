package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Forget everything on the phones (PROTOCOL.md 28): the relay first drops
// what is queued for the pairing, then the sealed forget_all goes; the job
// waits per phone until it confirms.
func TestForgetAllPurgesThenSendsAndWaitsForEachPhonesConfirm(t *testing.T) {
	bridge, relay := mobileTestHub(t)
	app := &App{}
	app.mobileBridge = bridge
	bridge.app = app
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead})
	relay.acceptRegistration(t, mobileTestListed(phone))

	var mu sync.Mutex
	confirmed := []string{}
	previous := forgetAllConfirmedHook
	forgetAllConfirmedHook = func(jobID, deviceID string, _ time.Time) {
		mu.Lock()
		defer mu.Unlock()
		confirmed = append(confirmed, jobID+"/"+deviceID)
	}
	t.Cleanup(func() { forgetAllConfirmedHook = previous })

	if _, err := app.forgetAllOnDevices(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("a bad job id was accepted")
	}
	jobID := uuid.NewString()
	pending, err := app.forgetAllOnDevices(context.Background(), jobID)
	if err != nil || len(pending) != 1 || pending[0] != phone.deviceID {
		t.Fatalf("pending = %v %v", pending, err)
	}
	purge := relay.nextFrame(t, "purge")
	var purgeFrame struct {
		DeviceID string `json:"deviceId"`
	}
	if json.Unmarshal(purge.raw, &purgeFrame) != nil || purgeFrame.DeviceID != phone.deviceID {
		t.Fatalf("purge = %s", purge.raw)
	}
	out := relay.nextFrame(t, "out")
	var outFrame struct {
		Env mobileEnvelope `json:"env"`
	}
	if err := json.Unmarshal(out.raw, &outFrame); err != nil || outFrame.Env.Kind != mobileKindForgetAll {
		t.Fatalf("out = %s %v", out.raw, err)
	}
	var plaintext mobileForgetAllPlaintext
	if err := json.Unmarshal(phone.openFromHub(t, bridge.identity.Public, bridge.identity.HubID, outFrame.Env), &plaintext); err != nil ||
		plaintext.V != 1 || plaintext.JobID != jobID || plaintext.At == "" {
		t.Fatalf("forget_all plaintext = %+v %v", plaintext, err)
	}
	if status := app.forgetAllStatus(jobID); status[phone.deviceID] != mobileForgetPending {
		t.Fatalf("status before the confirm = %v", status)
	}

	done := func(job string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"idempotencyKey": uuid.NewString(), "forgot": []string{"briefs", "answers"}})
		r := httptest.NewRequest(http.MethodPost, "/v1/forget-all/"+job+"/done", bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		device := mobileDeviceRecord{}
		device.DeviceID = phone.deviceID
		bridge.serveForgetAllDone(w, r, device, job)
		return w
	}
	if got := done(uuid.NewString()); got.Code != http.StatusNotFound {
		t.Fatalf("an unknown job = %d", got.Code)
	}
	if got := done(jobID); got.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", got.Code, got.Body.String())
	}
	if got := done(jobID); got.Code != http.StatusOK {
		t.Fatalf("a repeated confirm = %d", got.Code)
	}
	if status := app.forgetAllStatus(jobID); status[phone.deviceID] != mobileForgetConfirmed {
		t.Fatalf("status after the confirm = %v", status)
	}
	mu.Lock()
	if len(confirmed) != 1 || confirmed[0] != jobID+"/"+phone.deviceID {
		t.Fatalf("hook = %v, want once", confirmed)
	}
	mu.Unlock()
	// Starting the job again sends nothing to a phone that confirmed.
	if again, err := app.forgetAllOnDevices(context.Background(), jobID); err != nil || len(again) != 0 {
		t.Fatalf("again = %v %v", again, err)
	}
}
