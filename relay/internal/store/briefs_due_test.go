package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"isoform-relay/internal/wire"
)

func TestBriefStateTransitions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	const dueAt = int64(1000)
	tests := []struct {
		name                  string
		deliverAt             int64
		before, after         int64
		wantBefore, wantAfter briefState
	}{
		{"immediate stays due", 0, 1, 2, briefDue, briefDue},
		{"held stays held", dueAt, 998, 999, briefHeld, briefHeld},
		{"held becomes due at boundary", dueAt, 999, 1000, briefHeld, briefDue},
		{"due stays due", dueAt, 1000, 1001, briefDue, briefDue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, step := range []struct {
				now  int64
				want briefState
			}{{tt.before, tt.wantBefore}, {tt.after, tt.wantAfter}} {
				var got briefState
				err := s.db.QueryRowContext(ctx, "SELECT "+briefStateAt()+" FROM (SELECT ? AS deliver_at)", step.now, tt.deliverAt).Scan(&got)
				if err != nil || got != step.want {
					t.Fatalf("state at %d = %q, %v; want %q", step.now, got, err, step.want)
				}
			}
		})
	}
}

func TestHeldAmendmentsKeepDueBriefUntilReleaseAndCleanup(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	now := time.Now().UTC().Truncate(time.Millisecond)
	dev := seedDevice(t, s, hubID, now.Add(72*time.Hour))
	other := seedDevice(t, s, hubID, now.Add(72*time.Hour))
	dueAt := now.Add(time.Hour)
	put := func(deviceID string, seq uint64, deliverAt time.Time) {
		t.Helper()
		env := wire.Envelope{V: 1, Hub: hubID, Dev: deviceID, Dir: "h2d", Seq: seq, Kind: "brief", CT: ct(t, 300)}
		body, err := env.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.PutBriefDueAt(ctx, hubID, env, body, wire.FormatTime(now), false, now, deliverAt); err != nil {
			t.Fatal(err)
		}
	}
	put(dev.ID, 1, time.Time{})
	put(other.ID, 1, time.Time{})
	for seq := uint64(2); seq <= uint64(KeptHeldBriefs+3); seq++ {
		put(dev.ID, seq, dueAt)
	}
	if n := count(t, s, "SELECT count(*) FROM briefs WHERE device_id=?", dev.ID); n != KeptHeldBriefs+1 {
		t.Fatalf("retained %d briefs, want one due and %d held", n, KeptHeldBriefs)
	}
	for _, at := range []time.Time{now, dueAt.Add(-time.Millisecond)} {
		latest, err := s.LatestBrief(ctx, dev.ID, at)
		if err != nil || latest.Version != 1 {
			t.Fatalf("latest before release = %+v, %v; want version 1", latest, err)
		}
	}
	if _, err := s.Sweep(ctx, now); err != nil {
		t.Fatal(err)
	}
	latest, err := s.LatestBrief(ctx, dev.ID, now)
	if err != nil || latest.Version != 1 {
		t.Fatalf("latest after sweep = %+v, %v; want version 1", latest, err)
	}
	latest, err = s.LatestBrief(ctx, dev.ID, dueAt)
	if err != nil || latest.Version != uint64(KeptHeldBriefs+3) {
		t.Fatalf("latest at release = %+v, %v", latest, err)
	}
	swept, err := s.Sweep(ctx, dueAt)
	if err != nil || swept.Briefs != 1 {
		t.Fatalf("release sweep = %+v, %v; want one superseded brief removed", swept, err)
	}
	if n := count(t, s, "SELECT count(*) FROM briefs WHERE device_id=?", dev.ID); n != KeptBriefs {
		t.Fatalf("after release retained %d, want %d", n, KeptBriefs)
	}
	if n := count(t, s, "SELECT count(*) FROM briefs WHERE device_id=?", other.ID); n != 1 {
		t.Fatalf("other device retained %d briefs, want 1", n)
	}
}

func TestHeldBriefIsHiddenUntilDueAndItsPushWaits(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(72*time.Hour))
	if _, err := s.RegisterPush(ctx, dev.ID, strings.Repeat("71", 32), "development", time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	dueAt := now.Add(2 * time.Hour)
	put := func(seq uint64, notify bool, deliverAt time.Time) {
		t.Helper()
		env := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: seq, Kind: "brief", CT: ct(t, 300)}
		body, _ := env.Canonical()
		if err := s.PutBriefDueAt(ctx, hubID, env, body, wire.FormatTime(now), notify, now, deliverAt); err != nil {
			t.Fatal(err)
		}
	}
	put(1, false, time.Time{})
	put(2, true, dueAt)

	// The newer held brief does not hide the older brief that is due now.
	latest, err := s.LatestBrief(ctx, dev.ID, now)
	if err != nil || latest.Version != 1 {
		t.Fatalf("latest before the due time %+v %v, want version 1", latest, err)
	}
	if listed, err := s.ListBriefs(ctx, dev.ID, 7, now); err != nil || len(listed) != 1 {
		t.Fatalf("listed before the due time %d (%v), want only version 1", len(listed), err)
	}
	if claim, err := s.ClaimPush(ctx, now.Add(time.Minute), time.Minute); err != nil || claim != nil {
		t.Fatalf("held brief's push was claimed early: %+v %v", claim, err)
	}

	// At the due time the brief is visible and its push is claimable.
	latest, err = s.LatestBrief(ctx, dev.ID, dueAt.Add(-time.Second))
	if err != nil || latest.Version != 1 {
		t.Fatalf("one second before the due time %+v %v, want version 1", latest, err)
	}
	latest, err = s.LatestBrief(ctx, dev.ID, dueAt)
	if err != nil || latest.Version != 2 {
		t.Fatalf("at the due time %+v %v, want version 2", latest, err)
	}
	claim, err := s.ClaimPush(ctx, dueAt, time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("no push at the due time: %v", err)
	}
	if claim.Kind != "brief" || claim.EventID != "2" {
		t.Fatalf("claim %+v, want the brief push for version 2", claim)
	}
}

func TestImmediateBriefIsVisibleWhateverClockTheReadUses(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hubID := newHub(t, s)
	dev := seedDevice(t, s, hubID, time.Now().Add(24*time.Hour))
	stored := time.Now()
	env := wire.Envelope{V: 1, Hub: hubID, Dev: dev.ID, Dir: "h2d", Seq: 1, Kind: "brief", CT: ct(t, 300)}
	body, _ := env.Canonical()
	if err := s.PutBrief(ctx, hubID, env, body, wire.FormatTime(stored), false, stored); err != nil {
		t.Fatal(err)
	}
	// A reader whose clock is behind the writer still sees a brief that is not held.
	if _, err := s.LatestBrief(ctx, dev.ID, stored.Add(-time.Hour)); err != nil {
		t.Fatalf("immediate brief hidden from an earlier clock: %v", err)
	}
}
