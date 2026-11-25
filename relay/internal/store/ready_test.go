package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"isoform-relay/internal/push"
)

// A ready wake (PROTOCOL.md 9.5) is queued for the hub's own device only, a
// newer one replaces one still queued, and a held brief's wake waits for its
// slot.
func TestReadyWakeIsQueuedOncePerDeviceAndWaitsForAHeldBrief(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	now := time.Now()
	if err := s.AddReady(ctx, hubID, dev.ID, now, now); err != nil {
		t.Fatalf("no registration = %v", err)
	}
	if claim, err := s.ClaimPush(ctx, now.Add(time.Second), time.Minute); err != nil || claim != nil {
		t.Fatalf("a device without a push token got a wake: %+v %v", claim, err)
	}
	if _, err := s.RegisterPushWithGatewayGrant(ctx, dev.ID, strings.Repeat("ab", 32), "development", "grant-value", now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddReady(ctx, newHub(t, s), dev.ID, now, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another hub's device = %v", err)
	}
	slot := now.Add(10 * time.Minute)
	if err := s.AddReady(ctx, hubID, dev.ID, now, slot); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimPush(ctx, now.Add(time.Second), time.Minute); err != nil || claim != nil {
		t.Fatalf("a held brief's wake went before its slot: %+v %v", claim, err)
	}
	// A second brief stored meanwhile replaces the queued wake: one wake.
	if err := s.AddReady(ctx, hubID, dev.ID, now, now); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimPush(ctx, now.Add(time.Second), time.Minute)
	if err != nil || claim == nil || claim.Kind != push.KindReady || claim.Notice != (push.Notice{}) {
		t.Fatalf("claim = %+v %v", claim, err)
	}
	if next, err := s.ClaimPush(ctx, slot.Add(time.Second), time.Minute); err != nil || next != nil {
		t.Fatalf("the replaced wake was still sent: %+v %v", next, err)
	}
}
