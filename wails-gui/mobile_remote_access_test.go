package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/remoteaccess"
)

func TestMobilePushGatewayConfigRequiresAValidPublicKey(t *testing.T) {
	oldURL, oldEnvironment, oldPublicKey := DefaultMobilePushGatewayURL, DefaultMobilePushGatewayEnvironment, DefaultMobilePushGatewayPublicKey
	defer func() {
		DefaultMobilePushGatewayURL, DefaultMobilePushGatewayEnvironment, DefaultMobilePushGatewayPublicKey = oldURL, oldEnvironment, oldPublicKey
	}()
	DefaultMobilePushGatewayURL = ""
	DefaultMobilePushGatewayEnvironment = ""
	DefaultMobilePushGatewayPublicKey = ""
	if gatewayURL, environment, publicKey, err := mobilePushGatewayConfig(); err != nil ||
		gatewayURL != "" || environment != "" || publicKey != "" {
		t.Fatalf("empty gateway config = %q %q %q %v", gatewayURL, environment, publicKey, err)
	}
	DefaultMobilePushGatewayURL = "https://push.example"
	if _, _, _, err := mobilePushGatewayConfig(); err == nil {
		t.Fatal("gateway URL accepted without a public key")
	}
	gatewayKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	DefaultMobilePushGatewayPublicKey = base64.RawURLEncoding.EncodeToString(gatewayKey.PublicKey().Bytes())
	gatewayURL, environment, publicKey, err := mobilePushGatewayConfig()
	if err != nil || gatewayURL != DefaultMobilePushGatewayURL || environment != "production" || publicKey != DefaultMobilePushGatewayPublicKey {
		t.Fatalf("valid gateway config = %q %q %q %v", gatewayURL, environment, publicKey, err)
	}
	DefaultMobilePushGatewayPublicKey = "not-a-key"
	if _, _, _, err := mobilePushGatewayConfig(); err == nil {
		t.Fatal("invalid gateway public key accepted")
	}
}

func TestMobilePushGatewayDerivesHubKey(t *testing.T) {
	hubPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gatewayPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hubPub, hubKey, err := mobilePushGatewayHubConfig(hubPrivate, base64.RawURLEncoding.EncodeToString(gatewayPrivate.PublicKey().Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	gatewayPub, err := ecdh.X25519().NewPublicKey(mustDecodeMobileTest(t, hubPub))
	if err != nil || gatewayPub == nil || hubKey == "" {
		t.Fatalf("derived hub config: %q %q %v", hubPub, hubKey, err)
	}
}

func mustDecodeMobileTest(t *testing.T, encoded string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type mobileTestRemoteAccess struct {
	snapshot remoteaccess.Snapshot
	starts   atomic.Int32
	stops    atomic.Int32
}

func (s *mobileTestRemoteAccess) Start(context.Context) error {
	s.starts.Add(1)
	return nil
}

func (s *mobileTestRemoteAccess) WaitReady(context.Context) (remoteaccess.Snapshot, error) {
	return s.snapshot, nil
}

func (s *mobileTestRemoteAccess) Snapshot() remoteaccess.Snapshot { return s.snapshot }

func (s *mobileTestRemoteAccess) Reset(context.Context) (remoteaccess.Snapshot, error) {
	s.snapshot.Ticket = "endpointabcdefghijklmnopqrstuvwxyz"
	return s.snapshot, nil
}

func (s *mobileTestRemoteAccess) Stop() { s.stops.Add(1) }

type failedThenReadyMobileRemoteAccess struct {
	mu       sync.Mutex
	snapshot remoteaccess.Snapshot
	starts   int
	stops    int
}

func (s *failedThenReadyMobileRemoteAccess) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stops == 0 {
		return errors.New("failed supervisor was not stopped before restart")
	}
	s.starts++
	s.snapshot = remoteaccess.Snapshot{State: remoteaccess.StateReady, RelayURL: "http://127.0.0.1:8791"}
	return nil
}

func (s *failedThenReadyMobileRemoteAccess) WaitReady(context.Context) (remoteaccess.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot, nil
}

func (s *failedThenReadyMobileRemoteAccess) Snapshot() remoteaccess.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

func (s *failedThenReadyMobileRemoteAccess) Reset(context.Context) (remoteaccess.Snapshot, error) {
	return s.Snapshot(), nil
}

func (s *failedThenReadyMobileRemoteAccess) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stops++
	s.snapshot = remoteaccess.Snapshot{State: remoteaccess.StateOff}
}

type retryingMobileRemoteAccess struct {
	mobileTestRemoteAccess
	waits          atomic.Int32
	firstWaitError error
}

func (s *retryingMobileRemoteAccess) WaitReady(context.Context) (remoteaccess.Snapshot, error) {
	if s.waits.Add(1) == 1 {
		return remoteaccess.Snapshot{State: remoteaccess.StateStarting}, s.firstWaitError
	}
	return s.snapshot, nil
}

type blockingMobileRemoteAccess struct {
	waitEntered chan struct{}
	release     chan struct{}
	stopOnce    sync.Once
}

func (s *blockingMobileRemoteAccess) Start(context.Context) error { return nil }

func (s *blockingMobileRemoteAccess) WaitReady(ctx context.Context) (remoteaccess.Snapshot, error) {
	close(s.waitEntered)
	select {
	case <-ctx.Done():
		return remoteaccess.Snapshot{State: remoteaccess.StateStarting}, ctx.Err()
	case <-s.release:
		return remoteaccess.Snapshot{State: remoteaccess.StateStarting}, context.Canceled
	}
}

func (s *blockingMobileRemoteAccess) Snapshot() remoteaccess.Snapshot {
	return remoteaccess.Snapshot{State: remoteaccess.StateStarting}
}

func (s *blockingMobileRemoteAccess) Reset(context.Context) (remoteaccess.Snapshot, error) {
	return s.Snapshot(), nil
}

func (s *blockingMobileRemoteAccess) Stop() {
	s.stopOnce.Do(func() { close(s.release) })
}

func TestStartConfiguredMobileBridgeDoesNotWaitForRemoteAccess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YANSU_MOBILE_STATE_DIR", dir)
	t.Setenv("ISOFORM_MOBILE_REMOTE_ACCESS", "1")
	t.Setenv("ISOFORM_RELAY_URL", "")
	t.Setenv("YANSU_RELAY_URL", "")
	if err := saveMobileRelayConfig(dir, mobileRelayConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	supervisor := &blockingMobileRemoteAccess{
		waitEntered: make(chan struct{}),
		release:     make(chan struct{}),
	}
	app := &App{
		remoteAccessFactory: func() mobileRemoteAccessSupervisor { return supervisor },
	}
	type startResult struct {
		bridge *mobileBridge
		err    error
	}
	started := make(chan startResult, 1)
	go func() {
		app.mobileBridgeMu.Lock()
		bridge, err := startConfiguredMobileBridge(app)
		if bridge != nil {
			app.mobileBridge = bridge
		}
		app.mobileBridgeMu.Unlock()
		started <- startResult{bridge: bridge, err: err}
	}()

	var result startResult
	select {
	case result = <-started:
	case <-time.After(time.Second):
		supervisor.Stop()
		result = <-started
		if result.bridge != nil {
			result.bridge.Stop()
		}
		t.Fatal("startup waited for remote access readiness")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.bridge == nil {
		t.Fatal("startConfiguredMobileBridge returned no bridge")
	}
	t.Cleanup(result.bridge.Stop)
	select {
	case <-supervisor.waitEntered:
	case <-time.After(time.Second):
		t.Fatal("remote access startup did not reach WaitReady")
	}
}

func TestEnableMobileConnectionUsingRelayClearsURLAndSwitchesRemoteAccess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YANSU_MOBILE_STATE_DIR", dir)
	t.Setenv("ISOFORM_MOBILE_REMOTE_ACCESS", "1")
	t.Setenv("ISOFORM_RELAY_URL", "")
	t.Setenv("YANSU_RELAY_URL", "")
	previousRelay := DefaultMobileRelayURL
	previousRemoteAccess := DefaultMobileRemoteAccess
	t.Cleanup(func() {
		DefaultMobileRelayURL = previousRelay
		DefaultMobileRemoteAccess = previousRemoteAccess
	})
	DefaultMobileRelayURL = ""
	DefaultMobileRemoteAccess = ""
	remoteRelay := newMobileTestRelay(t, "", "")
	explicitRelay := newMobileTestRelay(t, "", "")
	if err := saveMobileRelayConfig(dir, mobileRelayConfig{URL: "https://saved.example"}); err != nil {
		t.Fatal(err)
	}
	supervisor := &mobileTestRemoteAccess{
		snapshot: remoteaccess.Snapshot{State: remoteaccess.StateReady, RelayURL: remoteRelay.url()},
	}
	app := &App{remoteAccessFactory: func() mobileRemoteAccessSupervisor { return supervisor }}
	t.Cleanup(func() {
		if bridge := app.mobileBridgeForUse(); bridge != nil {
			bridge.Stop()
		}
	})

	status, err := app.EnableMobileConnectionUsingRelay("")
	if err != nil || !status.Enabled || status.SavedRelayURL != "" ||
		status.RelayURL != remoteRelay.url() || status.RemoteAccess == nil ||
		status.RemoteAccess.State != remoteaccess.StateReady {
		t.Fatalf("clearing the saved relay did not enable remote access: %+v %v", status, err)
	}
	if supervisor.starts.Load() != 1 {
		t.Fatalf("remote supervisor starts = %d, want 1", supervisor.starts.Load())
	}
	saved, err := loadMobileRelayConfig(dir)
	if err != nil || saved.URL != "" {
		t.Fatalf("cleared relay URL was persisted as %+v: %v", saved, err)
	}

	status, err = app.EnableMobileConnectionUsingRelay(explicitRelay.url())
	if err != nil || status.SavedRelayURL != explicitRelay.url() ||
		status.RelayURL != explicitRelay.url() || status.RemoteAccess != nil {
		t.Fatalf("explicit relay did not replace remote access: %+v %v", status, err)
	}
	// The supervisor keeps serving the direct path beside the explicit relay
	// (PROTOCOL.md section 21.4); it stops only when remote access is off.
	if supervisor.stops.Load() != 0 {
		t.Fatalf("remote supervisor stops = %d, want 0 while it serves the direct path", supervisor.stops.Load())
	}
	bridge := app.mobileBridgeForUse()
	mobileTestWaitFor(t, "the direct path beside the explicit relay", func() bool {
		link := bridge.direct.Load()
		return link != nil && link.origin == remoteRelay.url()
	})
	if _, err := app.DisableMobileConnection(); err != nil {
		t.Fatal(err)
	}
	if supervisor.stops.Load() != 1 || bridge.direct.Load() != nil {
		t.Fatalf("turning the connection off left the direct path running: stops %d", supervisor.stops.Load())
	}
}

func TestEnsureRemoteAccessRestartsFailedSupervisor(t *testing.T) {
	bridge, err := newMobileBridge(&App{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Stop)
	supervisor := &failedThenReadyMobileRemoteAccess{
		snapshot: remoteaccess.Snapshot{State: remoteaccess.StateFailed, LastError: "initial failure"},
	}
	bridge.mu.Lock()
	bridge.remoteAccess = supervisor
	bridge.mu.Unlock()

	snapshot, err := bridge.ensureRemoteAccess(context.Background())
	if err != nil || snapshot.State != remoteaccess.StateReady {
		t.Fatalf("ensureRemoteAccess = %+v, %v; want ready", snapshot, err)
	}
	supervisor.mu.Lock()
	starts, stops := supervisor.starts, supervisor.stops
	supervisor.mu.Unlock()
	if starts != 1 || stops != 1 {
		t.Fatalf("failed supervisor restarts: starts=%d stops=%d; want one stop before one start", starts, stops)
	}
	bridge.mu.Lock()
	remoteRelayURL := bridge.remoteRelayURL
	bridge.mu.Unlock()
	if remoteRelayURL != snapshot.RelayURL {
		t.Fatalf("remote relay URL = %q, want %q", remoteRelayURL, snapshot.RelayURL)
	}
}

func TestEnableMobileConnectionRetriesRemoteReadiness(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YANSU_MOBILE_STATE_DIR", dir)
	t.Setenv("ISOFORM_MOBILE_REMOTE_ACCESS", "1")
	t.Setenv("ISOFORM_RELAY_URL", "")
	t.Setenv("YANSU_RELAY_URL", "")
	previousRelay := DefaultMobileRelayURL
	previousRemoteAccess := DefaultMobileRemoteAccess
	t.Cleanup(func() {
		DefaultMobileRelayURL = previousRelay
		DefaultMobileRemoteAccess = previousRemoteAccess
	})
	DefaultMobileRelayURL = ""
	DefaultMobileRemoteAccess = ""

	relay := newMobileTestRelay(t, "", "")
	firstWaitError := errors.New("initial readiness failure")
	supervisor := &retryingMobileRemoteAccess{
		mobileTestRemoteAccess: mobileTestRemoteAccess{
			snapshot: remoteaccess.Snapshot{State: remoteaccess.StateReady, RelayURL: relay.url()},
		},
		firstWaitError: firstWaitError,
	}
	app := &App{remoteAccessFactory: func() mobileRemoteAccessSupervisor { return supervisor }}
	bridge, err := newMobileBridge(app, dir)
	if err != nil {
		t.Fatal(err)
	}
	bridge.remoteRetryBackoff = func(int) time.Duration { return time.Millisecond }
	app.mobileBridge = bridge
	t.Cleanup(bridge.Stop)

	if _, err := app.EnableMobileConnectionUsingRelay(""); !errors.Is(err, firstWaitError) {
		t.Fatalf("initial enable error = %v, want original readiness error", err)
	}
	select {
	case <-relay.conns:
	case <-time.After(5 * time.Second):
		t.Fatal("remote retry did not start the relay client")
	}
	if waits := supervisor.waits.Load(); waits != 2 {
		t.Fatalf("remote readiness waits = %d, want 2", waits)
	}
	bridge.mu.Lock()
	clientStarted := bridge.client != nil
	bridge.mu.Unlock()
	if !clientStarted {
		t.Fatal("remote retry did not install the relay client")
	}
}

func TestMobileRemoteAccessInvitationAndReset(t *testing.T) {
	t.Setenv("ISOFORM_MOBILE_REMOTE_ACCESS", "1")
	bridge, relay := mobileTestHub(t)
	relay.acceptRegistration(t, nil)
	mobileTestWaitFor(t, "registration before opening pairing", func() bool {
		return bridge.connectionStatus().RelayState == mobileRelayConnected
	})
	bridge.mu.Lock()
	bridge.relay.URL = ""
	bridge.remoteAccess = &mobileTestRemoteAccess{
		snapshot: remoteaccess.Snapshot{
			State: remoteaccess.StateReady, RelayURL: bridge.client.origin,
			Ticket: "endpointabcdefghijklmnopqrstuv",
		},
	}
	bridge.mu.Unlock()
	if err := bridge.openPairing(); err != nil {
		t.Fatal(err)
	}
	relay.nextFrame(t, "pairing_open")
	invitation := mobileTestDecodeInvitation(t, bridge.connectionStatus().ConnectionCode)
	if invitation.Relay != "" {
		t.Fatalf("remote invitation contains relay %q", invitation.Relay)
	}
	if invitation.Iroh != "endpointabcdefghijklmnopqrstuv" {
		t.Fatalf("remote invitation ticket = %q", invitation.Iroh)
	}
	if invitation.Warning != "" || bridge.connectionStatus().Warning != "" {
		t.Fatal("remote access emitted a loopback warning")
	}

	now := time.Now()
	bridge.mu.Lock()
	bridge.devices["phone"] = &mobileDeviceRecord{
		MobileDeviceView: MobileDeviceView{
			DeviceID: "phone", DeviceName: "Test iPhone",
			CreatedAt: mobileTimestamp(now), ExpiresAt: mobileTimestamp(now.Add(time.Hour)),
		},
	}
	if err := bridge.persistDevicesLocked(); err != nil {
		bridge.mu.Unlock()
		t.Fatal(err)
	}
	bridge.mu.Unlock()

	app := &App{mobileBridge: bridge}
	status, err := app.ResetMobileRemoteAccess()
	if err != nil {
		t.Fatal(err)
	}
	relay.nextFrame(t, "pairing_close")
	relay.nextFrame(t, "revoke")
	if len(status.Devices) != 0 || status.RemoteAccess == nil ||
		status.RemoteAccess.State != remoteaccess.StateReady {
		t.Fatalf("status after reset = %+v", status)
	}
}

func TestMobileRemotePairingRequiresTicket(t *testing.T) {
	t.Setenv("ISOFORM_MOBILE_REMOTE_ACCESS", "1")
	bridge, _ := mobileTestHub(t)
	bridge.mu.Lock()
	bridge.relay.URL = ""
	bridge.remoteAccess = &mobileTestRemoteAccess{snapshot: remoteaccess.Snapshot{State: remoteaccess.StateStarting}}
	bridge.mu.Unlock()
	if err := bridge.openPairing(); err != errMobileRemoteAccessNotReady {
		t.Fatalf("openPairing error = %v", err)
	}
}
