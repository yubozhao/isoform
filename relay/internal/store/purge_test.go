package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"isoform-relay/internal/wire"
)

// Before a forget_all, the relay drops every ciphertext queued for the
// pairing (PROTOCOL.md 28): envelopes both ways and pushes not yet sent. The
// pairing stays, and another hub can't purge it.
func TestPurgeDropsWhatIsQueuedForThePairingAndKeepsIt(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	now := time.Now()
	if _, err := s.RegisterPushWithGatewayGrant(ctx, dev.ID, strings.Repeat("ab", 32), "development", "grant-value", now); err != nil {
		t.Fatal(err)
	}
	env, body := noticeEnvelope(t, hubID, dev.ID, 4)
	if err := s.AddNoticeResponse(ctx, hubID, env, body, testNotice(t), now); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeDevice(ctx, newHub(t, s), dev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another hub's purge = %v", err)
	}
	if err := s.PurgeDevice(ctx, hubID, dev.ID); err != nil {
		t.Fatal(err)
	}
	var envelopes, queued, devices int
	if err := s.db.QueryRow(`SELECT count(*) FROM envelopes WHERE device_id=?`, dev.ID).Scan(&envelopes); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM push_outbox WHERE device_id=? AND state IN ('queued','inflight')`, dev.ID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM devices WHERE id=?`, dev.ID).Scan(&devices); err != nil {
		t.Fatal(err)
	}
	if envelopes != 0 || queued != 0 || devices != 1 {
		t.Fatalf("after purge: envelopes=%d queued pushes=%d devices=%d", envelopes, queued, devices)
	}
}

func TestForgetAllIsAnH2DEnvelopeKind(t *testing.T) {
	env := wire.Envelope{V: 1, Hub: "7c9e6679-7425-40de-944b-e07fc1f90ae7", Dev: "7c9e6679-7425-40de-944b-e07fc1f90ae8", Dir: "h2d", Seq: 1, Kind: "forget_all", CT: ct(t, 60)}
	if err := env.Validate(); err != nil {
		t.Fatalf("forget_all h2d = %v", err)
	}
	env.Dir = "d2h"
	if err := env.Validate(); err == nil {
		t.Fatal("forget_all d2h accepted")
	}
}
