package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/remoteaccess"
	"wails-gui/storage"
)

// A desktop pinned to one relay also serves its direct-path relay. A phone
// paired through the pinned relay asks for direct-path credentials, then
// reaches the hub through the second relay alone, and loses that access when
// it is removed. Both relays are the real relay binary; the dumbpipe tunnel is
// replaced by a supervisor that reports the second relay's address, because
// the phone side of iroh is not in Go. Runs with YANSU_MOBILE_E2E=1.
func TestMobileDirectPathBesidePinnedRelay(t *testing.T) {
	if os.Getenv("YANSU_MOBILE_E2E") != "1" {
		t.Skip("set YANSU_MOBILE_E2E=1 to build and run the relay end-to-end check")
	}
	app, bridge, phone, pinned, direct, invitation, reply := mobileDirectE2EPairedPhone(t)

	// The same phone, now on the direct path only. Its sequence numbers and
	// keys carry over; only the relay and the bearer change.
	pinnedBearer := phone.bearer
	phone.relay, phone.bearer = direct, reply.Data.Bearer
	code, body := phone.request(t, "GET", "/status", "")
	if code != http.StatusOK || !strings.Contains(body, `"hubName"`) {
		t.Fatalf("status over the direct path: %d %s", code, body)
	}
	// An attachment (the spoken brief) is readable on the direct path too.
	bridge.mu.Lock()
	primary := bridge.client
	bridge.mu.Unlock()
	attachment, err := sealAndUploadAttachmentBytes(context.Background(), bridge.attachmentUploader(primary),
		invitation.Hub, []byte("spoken brief bytes"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	for _, relayURL := range []string{direct, pinned} {
		bearer := reply.Data.Bearer
		if relayURL == pinned {
			bearer = pinnedBearer
		}
		request, _ := http.NewRequest(http.MethodGet, relayURL+"/v1/attachments/"+attachment.ID, nil)
		request.Header.Set("Authorization", "Bearer "+bearer)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("attachment on %s: %d", relayURL, response.StatusCode)
		}
	}

	// And back to the pinned relay, which still answers.
	phone.relay, phone.bearer = pinned, pinnedBearer
	if code, body = phone.request(t, "GET", "/status", ""); code != http.StatusOK {
		t.Fatalf("status over the pinned relay after the direct path: %d %s", code, body)
	}

	dir := os.Getenv("YANSU_MOBILE_STATE_DIR")
	// Out of order across the paths: the pinned relay holds a request (the
	// computer was unreachable for it) while a newer one goes over the direct
	// path. Both are served; the held one replayed over the direct path is not.
	seal := func(seq uint64, path string) (mobileEnvelope, string) {
		id := uuid.NewString()
		plaintext, err := json.Marshal(map[string]string{"id": id, "method": "GET", "path": path, "query": "", "body": ""})
		if err != nil {
			t.Fatal(err)
		}
		env, err := sealMobileEnvelope(phone.keys.D2H, invitation.Hub, phone.deviceID, mobileDirD2H, seq, mobileKindRequest, plaintext)
		if err != nil {
			t.Fatal(err)
		}
		return env, id
	}
	post := func(relayURL, bearer string, env mobileEnvelope) {
		phone.relay, phone.bearer = relayURL, bearer
		if status, reply := phone.call(t, "POST", "/v1/requests", env, phone.authed()); status != http.StatusAccepted {
			t.Fatalf("queue request %d on %s: %d %s", env.Seq, relayURL, status, reply.Error)
		}
	}
	auditCount := func(path string) int {
		count := 0
		for _, line := range readMobileTestLines(t, filepath.Join(dir, "mobile_audit.jsonl")) {
			if strings.Contains(line, `"path":"`+path+`"`) {
				count++
			}
		}
		return count
	}
	heldEnv, heldID := seal(phone.sendSeq+1, "/devices")
	newerEnv, newerID := seal(phone.sendSeq+2, "/status")
	phone.sendSeq += 2
	post(direct, reply.Data.Bearer, newerEnv)
	if code, _ := phone.awaitResponse(t, newerID); code != http.StatusOK {
		t.Fatalf("the newer request over the direct path: %d", code)
	}
	post(pinned, pinnedBearer, heldEnv)
	if code, _ := phone.awaitResponse(t, heldID); code != http.StatusOK {
		t.Fatalf("the held request over the pinned relay after a newer one: %d", code)
	}
	served := auditCount("/devices")
	post(direct, reply.Data.Bearer, heldEnv)
	time.Sleep(3 * time.Second)
	if got := auditCount("/devices"); got != served {
		t.Fatalf("a replayed request was served again: %d audit lines, want %d", got, served)
	}
	phone.relay, phone.bearer = pinned, pinnedBearer

	if _, err := app.RevokeMobileDevice(phone.deviceID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	phone.relay, phone.bearer = direct, reply.Data.Bearer
	mobileTestWaitFor(t, "the direct path to refuse a removed phone", func() bool {
		status, _ := phone.call(t, "GET", "/v1/presence", nil, phone.authed())
		return status == http.StatusUnauthorized
	})
	if _, ok := bridge.directGrant(phone.deviceID); ok {
		t.Fatal("a removed phone keeps its direct-path grant")
	}
}

type mobileDirectE2EGrantReply struct {
	Data MobileRemoteAccessGrant `json:"data"`
}

// mobileDirectE2EPairedPhone pairs a phone through the pinned relay of a
// desktop that also serves its direct path, and gets that phone's direct-path
// grant. The phone is left pointed at the pinned relay.
func mobileDirectE2EPairedPhone(t *testing.T) (*App, *mobileBridge, *mobileE2EPhone, string, string, mobileInvitation, mobileDirectE2EGrantReply) {
	t.Helper()
	pinned := startMobileE2ERelay(t)
	direct := startMobileE2ERelay(t)
	dir := t.TempDir()
	t.Setenv("YANSU_MOBILE_STATE_DIR", dir)
	t.Setenv("ISOFORM_RELAY_URL", "")
	t.Setenv("ISOFORM_MOBILE_REMOTE_ACCESS", "1")
	app := &App{}
	store, err := storage.New(filepath.Join(dir, "yansu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app.store = store
	app.activeTags = map[string]bool{}
	ticket := "endpoint" + strings.Repeat("a", 40)
	supervisor := &mobileTestRemoteAccess{snapshot: remoteaccess.Snapshot{State: remoteaccess.StateReady, RelayURL: direct, Ticket: ticket}}
	app.remoteAccessFactory = func() mobileRemoteAccessSupervisor { return supervisor }
	t.Cleanup(func() {
		if bridge := app.mobileBridgeForUse(); bridge != nil {
			bridge.Stop()
		}
	})

	if status, err := app.EnableMobileConnection(pinned); err != nil || !status.Enabled {
		t.Fatalf("enable: %+v %v", status, err)
	}
	bridge := app.mobileBridgeForUse()
	mobileTestWaitFor(t, "the pinned relay and the direct path", func() bool {
		current, _ := app.GetMobileConnectionStatus()
		link := bridge.direct.Load()
		if current.RelayState != mobileRelayConnected || link == nil {
			return false
		}
		state, _ := link.snapshot()
		return state == mobileRelayConnected
	})

	status, err := mobileE2EOpenInvitation(app)
	if err != nil || status.ConnectionCode == "" {
		t.Fatalf("generate: %+v %v", status, err)
	}
	invitation := mobileTestDecodeInvitation(t, status.ConnectionCode)
	if invitation.Iroh != "" {
		t.Fatalf("a pinned-relay invitation carries a ticket: %q", invitation.Iroh)
	}
	phone := newMobileE2EPhone(t, pinned, invitation)
	phone.claim(t)
	mobileTestWaitFor(t, "a pending claim on the hub", func() bool {
		current, _ := app.GetMobileConnectionStatus()
		return current.PendingPairing != nil
	})
	pending, _ := app.GetMobileConnectionStatus()
	if _, err := app.ConfirmMobilePairing(pending.PendingPairing.PairingID, true); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	phone.awaitApproval(t)

	code, body := phone.request(t, "POST", "/remote-access", "{}")
	if code != http.StatusOK {
		t.Fatalf("remote access: %d %s", code, body)
	}
	var reply mobileDirectE2EGrantReply
	if err := json.Unmarshal([]byte(body), &reply); err != nil || reply.Data.Ticket != ticket || reply.Data.Bearer == "" {
		t.Fatalf("remote access reply: %v %s", err, body)
	}
	if reply.Data.Bearer == phone.bearer {
		t.Fatal("the direct path reuses the pinned relay's bearer")
	}

	return app, bridge, phone, pinned, direct, invitation, reply
}

// A phone that unpairs itself through the pinned relay loses the direct path
// at once too: the pinned relay deletes it and tells the hub, and the hub must
// revoke it at its own relay instead of leaving that bearer valid until the
// direct link next registers.
func TestMobileDirectPathRefusesAPhoneThatUnpairedOnThePinnedRelay(t *testing.T) {
	if os.Getenv("YANSU_MOBILE_E2E") != "1" {
		t.Skip("set YANSU_MOBILE_E2E=1 to build and run the relay end-to-end check")
	}
	_, bridge, phone, _, direct, _, reply := mobileDirectE2EPairedPhone(t)
	if code, _ := phone.call(t, "DELETE", "/v1/devices/me", nil, phone.authed()); code/100 != 2 {
		t.Fatalf("unpair on the pinned relay: %d", code)
	}
	mobileTestWaitFor(t, "the hub to drop the unpaired phone", func() bool {
		bridge.mu.Lock()
		defer bridge.mu.Unlock()
		return bridge.devices[phone.deviceID] == nil
	})
	if _, ok := bridge.directGrant(phone.deviceID); ok {
		t.Fatal("an unpaired phone keeps its direct-path grant")
	}
	phone.relay, phone.bearer = direct, reply.Data.Bearer
	mobileTestWaitFor(t, "the direct path to refuse a phone that unpaired", func() bool {
		status, _ := phone.call(t, "GET", "/v1/presence", nil, phone.authed())
		return status == http.StatusUnauthorized
	})
}
