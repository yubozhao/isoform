package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/push"
)

// A withdraw push (PROTOCOL.md 9.4) is queued for the hub's own device only,
// and its claim carries the ids.
func TestWithdrawIsQueuedForTheHubsDeviceAndCarriesTheIDs(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	now := time.Now()
	if _, err := s.RegisterPushWithGatewayGrant(ctx, dev.ID, strings.Repeat("ab", 32), "development", "grant-value", now); err != nil {
		t.Fatal(err)
	}
	ids := []string{uuid.NewString(), "brief:2026-11-15-evening"}
	if err := s.AddWithdraw(ctx, newHub(t, s), dev.ID, ids, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another hub's device = %v", err)
	}
	if err := s.AddWithdraw(ctx, hubID, dev.ID, []string{"not an id"}, now); !errors.Is(err, push.ErrInvalidNotice) {
		t.Fatalf("a bad id = %v", err)
	}
	if err := s.AddWithdraw(ctx, hubID, dev.ID, ids, now); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimPush(ctx, now.Add(time.Second), time.Minute)
	if err != nil || claim == nil || claim.Kind != push.KindWithdraw {
		t.Fatalf("claim = %+v %v", claim, err)
	}
	if got := push.WithdrawIDs(claim.Notice.CT); len(got) != 2 || got[0] != ids[0] || got[1] != ids[1] {
		t.Fatalf("ids = %v", got)
	}
}

func TestWithdrawIDsAreBounded(t *testing.T) {
	if push.ValidWithdrawIDs(nil) || push.ValidWithdrawIDs([]string{"brief:x", "brief:x"}) || push.ValidWithdrawIDs([]string{"brief:has space"}) {
		t.Fatal("bad ids accepted")
	}
	many := make([]string, push.MaxWithdrawIDs+1)
	for i := range many {
		many[i] = uuid.NewString()
	}
	if push.ValidWithdrawIDs(many) || !push.ValidWithdrawIDs(many[:push.MaxWithdrawIDs]) {
		t.Fatal("the limit")
	}
}
