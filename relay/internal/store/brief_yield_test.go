package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"isoform-relay/internal/wire"
)

// An awake computer takes a made-ahead brief's push from the relay before its
// slot (PROTOCOL.md 8.2): the scheduled push is cancelled, the brief stays.
func TestBriefYieldCancelsTheScheduledPushAndKeepsTheBrief(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	if _, err := s.RegisterPush(ctx, dev.ID, strings.Repeat("ab", 32), "development", time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	env := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: 1, Kind: "brief", CT: ct(t, 300)}
	body, _ := env.Canonical()
	if err := s.PutBriefDueAt(ctx, hubID, env, body, wire.FormatTime(now), true, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT count(*) FROM push_outbox WHERE device_id=? AND state='queued'", dev.ID); n != 1 {
		t.Fatalf("queued pushes %d, want the scheduled one", n)
	}
	if err := s.YieldBriefPush(ctx, newHub(t, s), dev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another hub's yield = %v", err)
	}
	if err := s.YieldBriefPush(ctx, hubID, dev.ID); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "SELECT count(*) FROM push_outbox WHERE device_id=? AND state='queued'", dev.ID); n != 0 {
		t.Fatalf("queued pushes after the yield %d", n)
	}
	if n := count(t, s, "SELECT count(*) FROM briefs WHERE device_id=?", dev.ID); n != 1 {
		t.Fatalf("the brief went with its push: %d briefs", n)
	}
}
