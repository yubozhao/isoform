package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

func TestEngagementDigestBundlesTopFiveByScore(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	var bundles [][]engagementDigestItem
	rig.svc.deliver.Digest = func(_ context.Context, items []engagementDigestItem) (engagement.Channel, error) {
		bundles = append(bundles, items)
		return engagement.ChannelDesktop, nil
	}
	for i := 0; i < 7; i++ {
		_, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWaitingOnReply, SubjectID: fmt.Sprint(i), Stakes: float64(i+1) / 10,
			Params: map[string]string{"name": "Dana", "subject": "private thread"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	rig.svc.Pass(ctx)
	if len(bundles) != 0 {
		t.Fatal("digest went out before its scheduled period")
	}
	rig.clock = time.Date(2026, 10, 12, 13, 0, 0, 0, rig.clock.Location())
	rig.svc.Pass(ctx)
	if len(bundles) != 1 || len(bundles[0]) != 5 || len(rig.pushed)+len(rig.desktop) != 0 {
		t.Fatalf("bundles=%+v, individual deliveries=%d", bundles, len(rig.pushed)+len(rig.desktop))
	}
	for i, item := range bundles[0] {
		if item.Moment.SubjectID != fmt.Sprint(6-i) || item.Moment.ID == "" {
			t.Fatalf("item %d is not in score order: %+v", i, item)
		}
		stored, err := rig.store.GetEngagementMoment(ctx, item.Row.ID)
		if err != nil || stored.State != storage.EngagementStateSent || stored.Level != "passive" {
			t.Fatalf("digest row was not recorded: %+v %v", stored, err)
		}
	}
	if rig.svc.state.InterruptsToday != 0 || rig.svc.state.SentByKindToday[engagement.KindWaitingOnReply] != 5 {
		t.Fatalf("digest changed pacing: %+v", rig.svc.state)
	}
	rig.svc.Pass(ctx)
	if len(bundles) != 1 {
		t.Fatal("remaining moments made another bundle in the same period")
	}
	rig.clock = rig.clock.Add(5 * time.Hour)
	rig.svc.Pass(ctx)
	if len(bundles) != 2 || len(bundles[1]) != 2 {
		t.Fatalf("remaining moments did not wait for the next digest: %+v", bundles)
	}
}

func TestEngagementDigestRetryAndDailyCap(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	rig.svc.prefs.Enabled[engagement.KindGoalCheckIn] = true
	fail := true
	calls := 0
	rig.svc.deliver.Digest = func(_ context.Context, items []engagementDigestItem) (engagement.Channel, error) {
		calls++
		if len(items) != 1 {
			t.Fatalf("daily cap permits only one goal: %+v", items)
		}
		if fail {
			return engagement.ChannelNone, errors.New("store unavailable")
		}
		return engagement.ChannelDesktop, nil
	}
	for i := 0; i < 3; i++ {
		if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindGoalCheckIn, SubjectID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	rig.svc.Pass(ctx)
	rig.clock = time.Date(2026, 10, 12, 13, 0, 0, 0, rig.clock.Location())
	rig.svc.Pass(ctx)
	if calls != 1 || rig.svc.state.SentByKindToday[engagement.KindGoalCheckIn] != 0 {
		t.Fatal("failed bundle consumed its allowance")
	}
	fail = false
	rig.clock = rig.clock.Add(engagementRetryDelay)
	rig.svc.Pass(ctx)
	rig.svc.Pass(ctx)
	if calls != 2 || rig.svc.state.SentByKindToday[engagement.KindGoalCheckIn] != 1 {
		t.Fatalf("retry calls=%d, pace=%+v", calls, rig.svc.state)
	}
}

func TestEngagementMaturedDigestWaitsThroughQuietHours(t *testing.T) {
	for _, bundled := range []bool{false, true} {
		t.Run(fmt.Sprintf("bundled=%t", bundled), func(t *testing.T) {
			rig := newEngagementTestRig(t)
			ctx := context.Background()
			bundles := 0
			if bundled {
				rig.svc.deliver.Digest = func(_ context.Context, _ []engagementDigestItem) (engagement.Channel, error) {
					bundles++
					return engagement.ChannelDesktop, nil
				}
			}
			row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindCalendarConflict, SubjectID: "calendar", Params: map[string]string{"summary": "Two events overlap."}})
			if err != nil {
				t.Fatal(err)
			}
			rig.svc.Pass(ctx) // Scheduled for 13:00; the app next wakes at 23:00.
			rig.clock = time.Date(2026, 10, 12, 23, 0, 0, 0, rig.clock.Location())
			rig.svc.Pass(ctx)
			next := time.Date(2026, 10, 13, 8, 30, 0, 0, rig.clock.Location())
			stored, err := rig.store.GetEngagementMoment(ctx, row.ID)
			if err != nil || stored.State != storage.EngagementStateScheduled || stored.HoldUntil != next.UnixMilli() {
				t.Fatalf("late digest ignored the quiet window: %+v %v", stored, err)
			}
			rig.clock = next.Add(-30 * time.Minute)
			rig.svc.Pass(ctx)
			if bundles+len(rig.pushed)+len(rig.desktop) != 0 {
				t.Fatal("digest delivered before the next configured slot")
			}
			rig.clock = next
			rig.svc.Pass(ctx)
			if bundled {
				if bundles != 1 {
					t.Fatal("digest did not deliver at the next configured slot")
				}
				return
			}
			// Without a digest, a calendar conflict is a quiet line on Today at
			// the slot, never a toast or a push (notify v1.0.0 B3).
			stored, err = rig.store.GetEngagementMoment(ctx, row.ID)
			if err != nil || stored.State != storage.EngagementStateSent || stored.Channel != string(engagement.ChannelTodayQuiet) || len(rig.pushed)+len(rig.desktop) != 0 {
				t.Fatalf("at the slot: %+v pushed=%d desktop=%d %v", stored, len(rig.pushed), len(rig.desktop), err)
			}
		})
	}
}
