package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	var reply struct {
		Data MobileRemoteAccessGrant `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &reply); err != nil || reply.Data.Ticket != ticket || reply.Data.Bearer == "" {
		t.Fatalf("remote access reply: %v %s", err, body)
	}
	if reply.Data.Bearer == phone.bearer {
		t.Fatal("the direct path reuses the pinned relay's bearer")
	}

	// The same phone, now on the direct path only. Its sequence numbers and
	// keys carry over; only the relay and the bearer change.
	pinnedBearer := phone.bearer
	phone.relay, phone.bearer = direct, reply.Data.Bearer
	code, body = phone.request(t, "GET", "/status", "")
	if code != http.StatusOK || !strings.Contains(body, `"hubName"`) {
		t.Fatalf("status over the direct path: %d %s", code, body)
	}
	// And back to the pinned relay, which still answers.
	phone.relay, phone.bearer = pinned, pinnedBearer
	if code, body = phone.request(t, "GET", "/status", ""); code != http.StatusOK {
		t.Fatalf("status over the pinned relay after the direct path: %d %s", code, body)
	}

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
