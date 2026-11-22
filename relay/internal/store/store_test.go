package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/push"
	"isoform-relay/internal/wire"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relay.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenMigratesLegacyGatewayGrantDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE push_devices (
		device_id TEXT PRIMARY KEY,
		token TEXT NOT NULL,
		environment TEXT NOT NULL,
		registered_at INTEGER NOT NULL
	);
	INSERT INTO push_devices(device_id,token,environment,registered_at)
	VALUES('legacy-device','legacy-token','development',123);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var grant string
	if err := s.db.QueryRow(`SELECT gateway_grant FROM push_devices WHERE device_id='legacy-device'`).Scan(&grant); err != nil {
		t.Fatal(err)
	}
	if grant != "" {
		t.Fatalf("legacy grant default %q, want empty", grant)
	}
}

func token(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func ct(t *testing.T, plaintext int) string {
	t.Helper()
	raw := make([]byte, plaintext+wire.AEADTag)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// newHub registers a hub with a fresh credential and returns its id.
func newHub(t *testing.T, s *Store) string {
	t.Helper()
	hubID := uuid.NewString()
	if err := s.AuthorizeHub(context.Background(), hubID, wire.Digest(token(t)), time.Now()); err != nil {
		t.Fatal(err)
	}
	return hubID
}

// seedDevice runs the pairing path through the store and returns the device.
func seedDevice(t *testing.T, s *Store, hubID string, expires time.Time) Device {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	pairingID, claim, deviceID := uuid.NewString(), token(t), uuid.NewString()
	if err := s.OpenPairing(ctx, hubID, pairingID, wire.Digest(claim), now.Add(10*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimPairing(ctx, pairingID, Claim{Hub: hubID, TokenHash: wire.Digest(claim), DeviceID: deviceID, DevicePub: token(t), CT: ct(t, 100)}, now); err != nil {
		t.Fatal(err)
	}
	bearerHash := wire.Digest(token(t))
	approval := Approval{PairingID: pairingID, DeviceID: deviceID, BearerHash: bearerHash, Scopes: []string{"brief:read", "push"}, ExpiresAt: expires, CT: ct(t, 100)}
	if err := s.ApprovePairing(ctx, hubID, approval, now); err != nil {
		t.Fatal(err)
	}
	return Device{ID: deviceID, HubID: hubID, BearerHash: bearerHash, Scopes: approval.Scopes, ExpiresAt: expires}
}

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenCreatesPrivateWALDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("database mode %v (%v)", info.Mode().Perm(), err)
	}
	var mode string
	if err = s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode %q (%v)", mode, err)
	}
	if count(t, s, "PRAGMA foreign_keys") != 1 {
		t.Fatal("foreign keys are not enforced")
	}
}

func TestPairingStateMachineRejectsWrongStates(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := uuid.NewString()
	now := time.Now()
	if err := s.AuthorizeHub(ctx, hubID, wire.Digest("hub-credential"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorizeHub(ctx, hubID, wire.Digest("other-credential"), now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("different credential accepted: %v", err)
	}
	claim := token(t)
	pairingID := uuid.NewString()
	if err := s.OpenPairing(ctx, hubID, pairingID, wire.Digest(claim), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := s.OpenPairing(ctx, hubID, pairingID, wire.Digest(claim), now.Add(time.Minute), now); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate pairing id accepted: %v", err)
	}
	if err := s.DeclinePairing(ctx, hubID, pairingID); !errors.Is(err, ErrConflict) {
		t.Fatalf("declined an open pairing: %v", err)
	}
	if _, err := s.PollPairing(ctx, pairingID, wire.Digest(claim), now); !errors.Is(err, ErrConflict) {
		t.Fatalf("polled an open pairing: %v", err)
	}
	wrong := Claim{Hub: hubID, TokenHash: wire.Digest(token(t)), DeviceID: uuid.NewString(), DevicePub: token(t), CT: ct(t, 80)}
	if _, err := s.ClaimPairing(ctx, pairingID, wrong, now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong claim token accepted: %v", err)
	}
	stale := Claim{Hub: hubID, TokenHash: wire.Digest(claim), DeviceID: uuid.NewString(), DevicePub: token(t), CT: ct(t, 80)}
	if _, err := s.ClaimPairing(ctx, pairingID, stale, now.Add(2*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired invitation claimed: %v", err)
	}
	if _, err := s.ClaimPairing(ctx, pairingID, stale, now); err != nil {
		t.Fatal(err)
	}
	other := stale
	other.DeviceID = uuid.NewString()
	if _, err := s.ClaimPairing(ctx, pairingID, other, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("second device claimed a single-use invitation: %v", err)
	}
	approval := Approval{PairingID: pairingID, DeviceID: other.DeviceID, BearerHash: wire.Digest(token(t)), ExpiresAt: now.Add(time.Hour), CT: ct(t, 80)}
	if err := s.ApprovePairing(ctx, hubID, approval, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("approved a device that did not claim: %v", err)
	}
	if err := s.ClosePairing(ctx, hubID, pairingID); err != nil {
		t.Fatal(err)
	}
	if status, err := s.PollPairing(ctx, pairingID, wire.Digest(claim), now); err != nil || status.State != "claimed" {
		t.Fatalf("closing a claimed pairing changed it: %+v %v", status, err)
	}
}

func TestRequestQueueCapAndExpiry(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	dev := seedDevice(t, s, newHub(t, s), now.Add(24*time.Hour))
	put := func(seq uint64) error {
		env := wire.Envelope{V: 1, Hub: dev.HubID, Dev: dev.ID, Dir: "d2h", Seq: seq, Kind: "req", CT: ct(t, 60)}
		body, err := env.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		return s.AddRequest(context.Background(), dev, env, body, now)
	}
	for seq := uint64(1); seq <= MaxQueued; seq++ {
		if err := put(seq); err != nil {
			t.Fatalf("request %d: %v", seq, err)
		}
	}
	if err := put(MaxQueued + 1); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("queue accepted beyond cap: %v", err)
	}
	// Once the hour passes the queue frees itself.
	later := now.Add(RequestTTL + time.Minute)
	entries, err := s.UnsentRequests(context.Background(), dev.ID, 0, later, MaxQueued)
	if err != nil || len(entries) != 0 {
		t.Fatalf("expired requests still deliverable: %d %v", len(entries), err)
	}
	if _, err = s.Sweep(context.Background(), later); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT count(*) FROM envelopes WHERE dir='d2h'"); n != 0 {
		t.Fatalf("sweep kept %d expired requests", n)
	}
}

func TestResponseQueueKeepsTheNewest(t *testing.T) {
	s := openTest(t)
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	for seq := uint64(1); seq <= MaxQueued+5; seq++ {
		env := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: seq, Kind: "res", CT: ct(t, 60)}
		body, _ := env.Canonical()
		if err := s.AddResponse(context.Background(), hubID, env, body); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, s, "SELECT count(*) FROM envelopes WHERE dir='h2d'"); n != MaxQueued {
		t.Fatalf("response queue holds %d, want %d", n, MaxQueued)
	}
	oldest := count(t, s, "SELECT min(seq) FROM envelopes WHERE dir='h2d'")
	if oldest != 6 {
		t.Fatalf("oldest retained response %d, want 6", oldest)
	}
	if err := s.AckResponses(context.Background(), dev.ID, 10); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT count(*) FROM envelopes WHERE dir='h2d'"); n != MaxQueued-5 {
		t.Fatalf("ack through 10 left %d responses", n)
	}
}

func TestEventQueueKeepsTheNewestEventsAndSparesResponses(t *testing.T) {
	s := openTest(t)
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	store := func(seq uint64, kind string) {
		t.Helper()
		env := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: seq, Kind: kind, CT: ct(t, 60)}
		body, _ := env.Canonical()
		if err := s.AddResponse(context.Background(), hubID, env, body); err != nil {
			t.Fatalf("store %s seq %d: %v", kind, seq, err)
		}
	}
	for seq := uint64(1); seq <= 5; seq++ {
		store(seq, "res")
	}
	// Seventy events after five responses: only the newest MaxEvents remain.
	lastEvent := uint64(5 + 70)
	for seq := uint64(6); seq <= lastEvent; seq++ {
		store(seq, "event")
	}
	if n := count(t, s, "SELECT count(*) FROM envelopes WHERE dir='h2d' AND kind='event'"); n != MaxEvents {
		t.Fatalf("event queue holds %d, want %d", n, MaxEvents)
	}
	if oldest, want := count(t, s, "SELECT min(seq) FROM envelopes WHERE dir='h2d' AND kind='event'"), int(lastEvent)-MaxEvents+1; oldest != want {
		t.Fatalf("oldest retained event %d, want %d", oldest, want)
	}
	if n := count(t, s, "SELECT count(*) FROM envelopes WHERE dir='h2d' AND kind='res'"); n != 5 {
		t.Fatalf("event burst evicted responses: %d of 5 left", n)
	}
	// Responses keep their own cap: a response burst trims responses, not events.
	for seq := lastEvent + 1; seq <= lastEvent+MaxQueued+3; seq++ {
		store(seq, "res")
	}
	if n := count(t, s, "SELECT count(*) FROM envelopes WHERE dir='h2d' AND kind='res'"); n != MaxQueued {
		t.Fatalf("response queue holds %d, want %d", n, MaxQueued)
	}
	if n := count(t, s, "SELECT count(*) FROM envelopes WHERE dir='h2d' AND kind='event'"); n != MaxEvents {
		t.Fatalf("response burst evicted events: %d left", n)
	}
	// The ack clears every event up to the last one and the responses below it;
	// the newest MaxQueued responses survive.
	if err := s.AckResponses(context.Background(), dev.ID, lastEvent); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT count(*) FROM envelopes WHERE dir='h2d'"); n != MaxQueued {
		t.Fatalf("ack through the last event left %d envelopes, want %d responses", n, MaxQueued)
	}
	if n := count(t, s, "SELECT count(*) FROM envelopes WHERE dir='h2d' AND kind='event'"); n != 0 {
		t.Fatalf("ack left %d events", n)
	}
}

func TestBriefsKeepSevenAndQueueOnePushEach(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	if _, err := s.RegisterPush(ctx, dev.ID, strings.Repeat("ab", 32), "development", time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	put := func(seq uint64, notify bool) {
		t.Helper()
		env := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: seq, Kind: "brief", CT: ct(t, 300)}
		body, _ := env.Canonical()
		if err := s.PutBrief(ctx, hubID, env, body, wire.FormatTime(now), notify, now); err != nil {
			t.Fatal(err)
		}
	}
	for seq := uint64(1); seq <= 9; seq++ {
		put(seq, true)
	}
	put(9, true)
	if n := count(t, s, "SELECT count(*) FROM briefs WHERE device_id=?", dev.ID); n != KeptBriefs {
		t.Fatalf("briefs retained %d, want %d", n, KeptBriefs)
	}
	if n := count(t, s, "SELECT count(*) FROM push_outbox WHERE device_id=?", dev.ID); n != 9 {
		t.Fatalf("push rows %d, want one per brief", n)
	}
	latest, err := s.LatestBrief(ctx, dev.ID, now)
	if err != nil || latest.Version != 9 {
		t.Fatalf("latest %+v %v", latest, err)
	}
}

func TestRemovingADeviceDeletesEverythingStoredForIt(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	if _, err := s.RegisterPush(ctx, dev.ID, strings.Repeat("cd", 32), "development", time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	env := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "d2h", Seq: 1, Kind: "req", CT: ct(t, 60)}
	body, _ := env.Canonical()
	if err := s.AddRequest(ctx, dev, env, body, now); err != nil {
		t.Fatal(err)
	}
	brief := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: 2, Kind: "brief", CT: ct(t, 300)}
	briefBody, _ := brief.Canonical()
	if err := s.PutBrief(ctx, hubID, brief, briefBody, wire.FormatTime(now), true, now); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RevokeDevice(ctx, uuid.NewString(), dev.ID); err != nil || n {
		t.Fatalf("revoke through another hub: %v %v", n, err)
	}
	if removed, err := s.RevokeDevice(ctx, hubID, dev.ID); err != nil || !removed {
		t.Fatalf("revoke: %v %v", removed, err)
	}
	for _, query := range []string{
		"SELECT count(*) FROM envelopes WHERE device_id=?",
		"SELECT count(*) FROM briefs WHERE device_id=?",
		"SELECT count(*) FROM push_devices WHERE device_id=?",
		"SELECT count(*) FROM push_outbox WHERE device_id=?",
		"SELECT count(*) FROM devices WHERE id=?",
	} {
		if n := count(t, s, query, dev.ID); n != 0 {
			t.Fatalf("%s = %d after removal", query, n)
		}
	}
	if n := count(t, s, "SELECT count(*) FROM pairings WHERE device_id=? AND state='approved'", dev.ID); n != 0 {
		t.Fatal("approved pairing naming the removed device survived")
	}
}

func TestPushTokenMovesAndInvalidTokenIsForgotten(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	first := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	second := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	shared := strings.Repeat("ef", 32)
	if _, err := s.RegisterPush(ctx, first.ID, shared, "development", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterPush(ctx, second.ID, shared, "development", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT count(*) FROM push_devices WHERE token=?", shared); n != 1 {
		t.Fatalf("token held by %d devices", n)
	}
	if _, err := s.RegisterPush(ctx, second.ID, shared, "production", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT count(*) FROM push_devices WHERE device_id=?", second.ID); n != 1 {
		t.Fatal("environment change dropped the registration")
	}
}

func TestSweepRemovesExpiredAndFinishedRows(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	now := time.Now()
	live := seedDevice(t, s, hubID, now.Add(48*time.Hour))
	gone := seedDevice(t, s, hubID, now.Add(time.Minute))
	if _, err := s.RegisterPush(ctx, live.ID, strings.Repeat("01", 32), "development", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO push_outbox(device_id,event_id,notification_id,state,created_at,expires_at,next_at) VALUES(?,?,?,'accepted',?,?,?)", live.ID, "1", uuid.NewString(), now.Add(-48*time.Hour).UnixMilli(), now.Add(-47*time.Hour).UnixMilli(), now.Add(-48*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO pairings(id,hub_id,claim_hash,state,expires_at,created_at) VALUES(?,?,?,'open',?,?)", uuid.NewString(), hubID, wire.Digest("stale"), now.Add(-time.Minute).UnixMilli(), now.Add(-time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	swept, err := s.Sweep(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if swept.Devices != 1 || swept.Pairings < 1 || swept.Pushes != 1 {
		t.Fatalf("sweep counts %+v", swept)
	}
	if n := count(t, s, "SELECT count(*) FROM devices WHERE id=?", gone.ID); n != 0 {
		t.Fatal("expired device survived the sweep")
	}
	if n := count(t, s, "SELECT count(*) FROM devices WHERE id=?", live.ID); n != 1 {
		t.Fatal("live device removed by the sweep")
	}
}

func TestPushLeaseLifecycleAndRetryLimit(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(72*time.Hour))
	if _, err := s.RegisterPush(ctx, dev.ID, strings.Repeat("23", 32), "development", time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	env := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: 3, Kind: "brief", CT: ct(t, 300)}
	body, _ := env.Canonical()
	if err := s.PutBrief(ctx, hubID, env, body, wire.FormatTime(now), true, now); err != nil {
		t.Fatal(err)
	}
	at := now
	for attempt := 1; attempt <= push.MaxAttempts; attempt++ {
		claim, err := s.ClaimPush(ctx, at, time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("attempt %d: no claim (%v)", attempt, err)
		}
		if claim.Attempts != attempt {
			t.Fatalf("attempt count %d, want %d", claim.Attempts, attempt)
		}
		if err = s.FinishPush(ctx, *claim, push.Retry, at); err != nil {
			t.Fatal(err)
		}
		// Each retry waits the doubling backoff; the outbox expires after a day.
		at = at.Add(push.RetryBase*time.Duration(1<<(attempt-1)) + time.Second)
	}
	if claim, err := s.ClaimPush(ctx, at.Add(24*time.Hour), time.Minute); err != nil || claim != nil {
		t.Fatalf("exhausted notification retried: %+v %v", claim, err)
	}
	if n := count(t, s, "SELECT count(*) FROM push_outbox WHERE state='exhausted'"); n != 1 {
		t.Fatalf("exhausted rows %d", n)
	}
}

func TestStaleTokenInvalidClaimPreservesRenewedGatewayRegistration(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(72*time.Hour))
	token := strings.Repeat("ab", 32)
	now := time.Now().UTC()
	if _, err := s.RegisterPushWithGatewayGrant(ctx, dev.ID, token, "development", "old-grant", now); err != nil {
		t.Fatal(err)
	}
	first := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: 11, Kind: "brief", CT: ct(t, 300)}
	firstBody, _ := first.Canonical()
	if err := s.PutBrief(ctx, hubID, first, firstBody, wire.FormatTime(now), true, now); err != nil {
		t.Fatal(err)
	}
	oldClaim, err := s.ClaimPush(ctx, now.Add(time.Second), time.Minute)
	if err != nil || oldClaim == nil || oldClaim.GatewayGrant != "old-grant" {
		t.Fatalf("claim old registration: %+v %v", oldClaim, err)
	}
	if _, err := s.RegisterPushWithGatewayGrant(ctx, dev.ID, token, "development", "new-grant", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	second := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: 12, Kind: "brief", CT: ct(t, 300)}
	secondBody, _ := second.Canonical()
	if err := s.PutBrief(ctx, hubID, second, secondBody, wire.FormatTime(now.Add(3*time.Second)), true, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishPush(ctx, *oldClaim, push.TokenInvalid, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	var storedGrant string
	if err := s.db.QueryRow("SELECT gateway_grant FROM push_devices WHERE device_id=? AND token=?", dev.ID, token).Scan(&storedGrant); err != nil {
		t.Fatal(err)
	}
	if storedGrant != "new-grant" {
		t.Fatalf("renewed grant = %q, want new grant", storedGrant)
	}
	nextClaim, err := s.ClaimPush(ctx, now.Add(5*time.Second), time.Minute)
	if err != nil || nextClaim == nil || nextClaim.GatewayGrant != "new-grant" {
		t.Fatalf("new registration work was canceled: claim=%+v err=%v", nextClaim, err)
	}
}

// releaseOneOutbox is push_outbox as the first release created it, before the
// kind column existed.
const releaseOneOutbox = `
CREATE TABLE push_outbox (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	event_id TEXT NOT NULL,
	notification_id TEXT NOT NULL UNIQUE,
	state TEXT NOT NULL CHECK (state IN ('queued','inflight','accepted','permanent','exhausted','cancelled')),
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	next_at INTEGER NOT NULL,
	attempts INTEGER NOT NULL DEFAULT 0,
	lease_until INTEGER NOT NULL DEFAULT 0,
	generation INTEGER NOT NULL DEFAULT 0,
	UNIQUE(device_id, event_id)
);
CREATE INDEX push_outbox_due ON push_outbox(state, next_at);
`

func TestOpenAddsKindToReleaseOneOutbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.sqlite")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err = old.Exec(releaseOneOutbox); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec("INSERT INTO push_outbox(device_id,event_id,notification_id,state,created_at,expires_at,next_at) VALUES(?,?,?,'queued',?,?,?)", uuid.NewString(), "3", uuid.NewString(), now.UnixMilli(), now.Add(time.Hour).UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	// The second open finds the column already present and must leave it alone.
	for range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if n := count(t, s, "SELECT count(*) FROM pragma_table_info('push_outbox') WHERE name='kind'"); n != 1 {
			t.Fatalf("kind column count %d", n)
		}
		if n := count(t, s, "SELECT count(*) FROM push_outbox WHERE kind='brief'"); n != 1 {
			t.Fatalf("release-one row does not read as brief: %d", n)
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEventPushQueuesOncePerNewEnvelope(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	registered := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	bare := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	now := time.Now()
	if _, err := s.RegisterPush(ctx, registered.ID, strings.Repeat("ce", 32), "development", now); err != nil {
		t.Fatal(err)
	}
	env := wire.Envelope{V: 1, Hub: hubID, Dev: registered.ID, Dir: "h2d", Seq: 4, Kind: "event", CT: ct(t, 60)}
	body, _ := env.Canonical()
	if err := s.AddResponseWithPush(ctx, hubID, env, body, push.KindNeedsYou, now); err != nil {
		t.Fatal(err)
	}
	var kind, eventID string
	if err := s.db.QueryRow("SELECT kind,event_id FROM push_outbox WHERE device_id=?", registered.ID).Scan(&kind, &eventID); err != nil || kind != push.KindNeedsYou || eventID != "event:4" {
		t.Fatalf("queued push kind %q event %q (%v)", kind, eventID, err)
	}
	if err := s.AddResponseWithPush(ctx, hubID, env, body, push.KindNeedsYou, now); err != nil {
		t.Fatalf("identical retry refused: %v", err)
	}
	conflicting := wire.Envelope{V: 1, Hub: hubID, Dev: registered.ID, Dir: "h2d", Seq: 4, Kind: "event", CT: ct(t, 60)}
	conflictBody, _ := conflicting.Canonical()
	if err := s.AddResponseWithPush(ctx, hubID, conflicting, conflictBody, push.KindNeedsYou, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("different envelope under a used sequence: %v", err)
	}
	if n := count(t, s, "SELECT count(*) FROM push_outbox WHERE device_id=?", registered.ID); n != 1 {
		t.Fatalf("pushes queued for one event: %d", n)
	}
	// A device without a registration stores the event and queues nothing.
	bareEnv := wire.Envelope{V: 1, Hub: hubID, Dev: bare.ID, Dir: "h2d", Seq: 4, Kind: "event", CT: ct(t, 60)}
	bareBody, _ := bareEnv.Canonical()
	if err := s.AddResponseWithPush(ctx, hubID, bareEnv, bareBody, push.KindNeedsYou, now); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT count(*) FROM push_outbox WHERE device_id=?", bare.ID); n != 0 {
		t.Fatalf("push queued without a registration: %d", n)
	}
	// A device of another hub is refused, and the refusal leaves no push behind.
	other := newHub(t, s)
	stray := wire.Envelope{V: 1, Hub: other, Dev: registered.ID, Dir: "h2d", Seq: 5, Kind: "event", CT: ct(t, 60)}
	strayBody, _ := stray.Canonical()
	if err := s.AddResponseWithPush(ctx, other, stray, strayBody, push.KindNeedsYou, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("device of another hub accepted: %v", err)
	}
	if n := count(t, s, "SELECT count(*) FROM push_outbox WHERE device_id=?", registered.ID); n != 1 {
		t.Fatalf("refused event queued a push: %d", n)
	}
	if err := push.Enqueue(ctx, s.db, registered.ID, "x", "mystery", now); !errors.Is(err, push.ErrUnknownKind) {
		t.Fatalf("unknown push kind enqueued: %v", err)
	}
}

func TestCheckpointTruncatesTheWriteAheadLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.sqlite")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	for range 200 {
		if _, err := st.db.ExecContext(ctx, "INSERT INTO hubs(id, credential_hash, last_seen_at, created_at) VALUES(?, ?, 0, 0)", uuid.NewString(), uuid.NewString()); err != nil {
			t.Fatal(err)
		}
	}
	wal := path + "-wal"
	before, err := os.Stat(wal)
	if err != nil || before.Size() == 0 {
		t.Fatalf("write-ahead log before checkpoint: %v %v", before, err)
	}
	if err := st.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(wal)
	if err != nil || after.Size() != 0 {
		t.Fatalf("write-ahead log after checkpoint: size %d, want 0 (%v)", after.Size(), err)
	}
}

// A phone request waits for a sleeping computer for a day (PROTOCOL.md section 23):
// it is still deliverable after two hours and gone after RequestTTL.
func TestRequestWaitsForASleepingComputerForADay(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	dev := seedDevice(t, s, newHub(t, s), now.Add(3*RequestTTL))
	env := wire.Envelope{V: 1, Hub: dev.HubID, Dev: dev.ID, Dir: "d2h", Seq: 1, Kind: "req", CT: ct(t, 60)}
	body, err := env.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddRequest(context.Background(), dev, env, body, now); err != nil {
		t.Fatal(err)
	}
	entries, err := s.UnsentRequests(context.Background(), dev.ID, 0, now.Add(2*time.Hour), MaxQueued)
	if err != nil || len(entries) != 1 {
		t.Fatalf("a request was not kept for two hours: %d %v", len(entries), err)
	}
	if RequestTTL != 24*time.Hour {
		t.Fatalf("RequestTTL = %s, want 24h", RequestTTL)
	}
	entries, err = s.UnsentRequests(context.Background(), dev.ID, 0, now.Add(RequestTTL-time.Minute), MaxQueued)
	if err != nil || len(entries) != 1 {
		t.Fatalf("a request was dropped before a day: %d %v", len(entries), err)
	}
	entries, err = s.UnsentRequests(context.Background(), dev.ID, 0, now.Add(RequestTTL+time.Minute), MaxQueued)
	if err != nil || len(entries) != 0 {
		t.Fatalf("a request older than a day was still deliverable: %d %v", len(entries), err)
	}
}

// A device stored before the paired column existed takes 0: a grant may still
// replace its bearer, as before. A device a pairing approval creates is marked.
func TestOpenMigratesLegacyDevicesAsNotPairedHere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-devices.sqlite")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE devices (
		id TEXT PRIMARY KEY,
		hub_id TEXT NOT NULL,
		bearer_hash TEXT NOT NULL UNIQUE,
		scopes TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL DEFAULT 0
	);
	INSERT INTO devices(id,hub_id,bearer_hash,scopes,created_at,expires_at,last_seen_at)
	VALUES('legacy-device','legacy-hub','` + strings.Repeat("a", 64) + `','["presence:read"]',1,9999999999999,0);`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var paired int
	if err := s.db.QueryRow(`SELECT paired FROM devices WHERE id='legacy-device'`).Scan(&paired); err != nil {
		t.Fatal(err)
	}
	if paired != 0 {
		t.Fatalf("legacy device paired %d, want 0", paired)
	}
	replacement := Device{ID: "legacy-device", BearerHash: strings.Repeat("b", 64), Scopes: []string{"presence:read"}, ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.GrantDevice(context.Background(), "legacy-hub", replacement, time.Now()); err != nil {
		t.Fatalf("a grant over a legacy device: %v", err)
	}
}
