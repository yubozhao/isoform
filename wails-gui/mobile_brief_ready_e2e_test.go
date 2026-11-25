package main

// Replay of 2026-11-22 through the real relay binary: a paired phone fetches a
// brief, the relay restarts (as the hub restarted it at 19:31 to swap a build),
// a newer brief is made, and the phone gets it with no app kill and no new
// pairing. The computer asks the relay for one silent ready wake per brief
// version (PROTOCOL.md 9.5); the relay here has no APNs, so the test reads the
// queued wakes from the relay's own database (counts only). Opt-in, like the
// relay end-to-end check: YANSU_MOBILE_E2E=1.

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"wails-gui/storage"
)

type mobileE2ERelayProcess struct {
	binary, address, stateDir string
	cmd                       *exec.Cmd
}

func (r *mobileE2ERelayProcess) start(t *testing.T) {
	t.Helper()
	r.cmd = exec.Command(r.binary)
	r.cmd.Env = append(os.Environ(), "ISOFORM_RELAY_ADDR="+r.address, "ISOFORM_RELAY_STATE_DIR="+r.stateDir)
	r.cmd.Stdout, r.cmd.Stderr = os.Stderr, os.Stderr
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	mobileTestWaitFor(t, "a healthy relay", func() bool {
		response, err := http.Get("http://" + r.address + "/v1/health")
		if err != nil {
			return false
		}
		response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
}

func (r *mobileE2ERelayProcess) stop() {
	if r.cmd == nil || r.cmd.Process == nil {
		return
	}
	_ = r.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = r.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = r.cmd.Process.Kill()
		<-done
	}
	r.cmd = nil
}

// readyWakes counts the ready wakes the relay queued for a device, in any state.
func (r *mobileE2ERelayProcess) readyWakes(t *testing.T, deviceID string) int {
	t.Helper()
	db := r.open(t)
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM push_outbox WHERE device_id=? AND kind='ready'", deviceID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// registerPush gives the device a push registration, as the phone's own
// registration would on a relay with APNs. The token is synthetic.
func (r *mobileE2ERelayProcess) registerPush(t *testing.T, deviceID string) {
	t.Helper()
	db := r.open(t)
	defer db.Close()
	if _, err := db.Exec("INSERT INTO push_devices(device_id,token,environment,gateway_grant,registered_at) VALUES(?,?,?,?,?)",
		deviceID, strings.Repeat("ab", 32), "development", "", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func (r *mobileE2ERelayProcess) open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(r.stateDir, "relay.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMobileBriefReadyAcrossARelayRestart(t *testing.T) {
	if os.Getenv("YANSU_MOBILE_E2E") != "1" {
		t.Skip("set YANSU_MOBILE_E2E=1 to build and run the relay end-to-end check")
	}
	binary := filepath.Join(t.TempDir(), "isoform-relay")
	build := exec.Command("go", "build", "-o", binary, "./cmd/isoform-relay")
	build.Dir = filepath.Join("..", "relay")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build relay: %v\n%s", err, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relay := &mobileE2ERelayProcess{binary: binary, address: listener.Addr().String(), stateDir: t.TempDir()}
	listener.Close()
	relay.start(t)
	t.Cleanup(relay.stop)
	relayURL := "http://" + relay.address

	dir := t.TempDir()
	t.Setenv("YANSU_MOBILE_STATE_DIR", dir)
	t.Setenv("ISOFORM_RELAY_URL", "")
	app := &App{}
	store, err := storage.New(filepath.Join(dir, "yansu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app.store = store
	app.activeTags = map[string]bool{}
	svc := mobileBriefServiceFactory(app)
	app.mobileBriefSvc = svc
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	t.Cleanup(func() {
		cancel()
		svc.Stop()
		if bridge := app.mobileBridgeForUse(); bridge != nil {
			bridge.Stop()
		}
	})
	connected := func() bool {
		current, _ := app.GetMobileConnectionStatus()
		return current.RelayState == mobileRelayConnected
	}
	if status, err := app.EnableMobileConnection(relayURL); err != nil || !status.Enabled {
		t.Fatalf("enable: %+v %v", status, err)
	}
	mobileTestWaitFor(t, "a connected relay", connected)

	status, err := mobileE2EOpenInvitation(app)
	if err != nil || status.ConnectionCode == "" {
		t.Fatalf("generate: %+v %v", status, err)
	}
	phone := newMobileE2EPhone(t, relayURL, mobileTestDecodeInvitation(t, status.ConnectionCode))
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
	relay.registerPush(t, phone.deviceID)

	// Last night's brief reaches the phone, with one ready wake.
	evening := mobileE2ESampleBrief(status.HubName)
	evening.BriefID, evening.Slot = "2026-11-21-evening", "evening"
	evening.AsOf = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	before := relay.readyWakes(t, phone.deviceID)
	if err := app.publishMobileBrief(evening, false); err != nil {
		t.Fatal(err)
	}
	if got := phone.awaitBrief(t, 30*time.Second); got.BriefID != evening.BriefID {
		t.Fatalf("first brief = %s", got.BriefID)
	}
	mobileTestWaitFor(t, "one ready wake for the brief", func() bool { return relay.readyWakes(t, phone.deviceID) == before+1 })

	// A relaunch saves the same brief again: no second wake.
	if err := app.publishMobileBrief(evening, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if got := relay.readyWakes(t, phone.deviceID); got != before+1 {
		t.Fatalf("the same brief woke the phone again: %d wakes", got-before)
	}

	// The relay restarts; the computer reconnects by itself.
	relay.stop()
	mobileTestWaitFor(t, "the computer seeing the relay gone", func() bool { return !connected() })
	relay.start(t)
	mobileTestWaitFor(t, "the computer reconnected", connected)

	// This morning's brief: a new wake, and the phone's next read (an open, a
	// scheduled wake or the ready push) gets it with the pairing it had.
	morning := mobileE2ESampleBrief(status.HubName)
	morning.BriefID, morning.Slot = "2026-11-22-morning", "morning"
	morning.AsOf = time.Now().UTC().Format(time.RFC3339)
	if err := app.publishMobileBrief(morning, false); err != nil {
		t.Fatal(err)
	}
	mobileTestWaitFor(t, "a ready wake for the newer brief", func() bool { return relay.readyWakes(t, phone.deviceID) == before+2 })
	if got := phone.awaitBrief(t, 30*time.Second); got.BriefID != morning.BriefID {
		t.Fatalf("after the restart the phone got %s", got.BriefID)
	}
}
