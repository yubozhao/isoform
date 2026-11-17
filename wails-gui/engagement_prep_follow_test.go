package main

import (
	"context"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// Prep follows you once (notify v1.0.0 rule 5, B8).
func TestPrepThatRangOnTheComputerFollowsToThePhoneOnce(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	rig.presence.MacIdle = 10 * time.Second // at the computer
	row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindMeetingPrep, SubjectID: "ev-1",
		ExpiresAt: rig.clock.Add(15 * time.Minute), Params: map[string]string{"title": "Design review"}})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if len(rig.desktop) != 1 || len(rig.pushed) != 0 {
		t.Fatalf("at the computer: desktop=%d push=%d", len(rig.desktop), len(rig.pushed))
	}
	// Still at the computer: nothing moves.
	rig.svc.Pass(ctx)
	if len(rig.pushed) != 0 {
		t.Fatal("prep followed while the person was at the computer")
	}
	// The person leaves before the meeting: one silent push, once.
	rig.presence.ScreenLocked = true
	rig.clock = rig.clock.Add(5 * time.Minute)
	rig.svc.Pass(ctx)
	rig.svc.Pass(ctx)
	stored, _ := rig.store.GetEngagementMoment(ctx, row.ID)
	if len(rig.pushed) != 1 || stored.Channel != "desktop+push" {
		t.Fatalf("prep followed = %d pushes, %+v", len(rig.pushed), stored)
	}
}

func TestOpenedOrLatePrepDoesNotFollow(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		opened bool
		late   bool
	}{{"opened", true, false}, {"meeting started", false, true}} {
		rig := newEngagementTestRig(t)
		rig.presence.MacIdle = 10 * time.Second
		row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindMeetingPrep, SubjectID: "ev-" + c.name,
			ExpiresAt: rig.clock.Add(15 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		rig.svc.Pass(ctx)
		if c.opened {
			if err := rig.svc.Receipt(ctx, row.ID, "desktop", storage.EngagementReceiptOpened, "", rig.clock); err != nil {
				t.Fatal(err)
			}
		}
		if c.late {
			rig.clock = rig.clock.Add(20 * time.Minute)
		}
		rig.presence.ScreenLocked = true
		rig.svc.Pass(ctx)
		if len(rig.pushed) != 0 {
			t.Fatalf("%s: prep followed to the phone", c.name)
		}
	}
}

// Prep a person routed to the computer, or whose source was forgotten, stays
// where it rang.
func TestChosenOrForgottenPrepDoesNotFollow(t *testing.T) {
	ctx := context.Background()
	for _, forget := range []bool{false, true} {
		rig := newEngagementTestRig(t)
		rig.presence.MacIdle = 10 * time.Second
		m := engagement.Moment{Kind: engagement.KindMeetingPrep, SubjectID: "ev-x", ExpiresAt: rig.clock.Add(15 * time.Minute)}
		if !forget {
			m.Channels = []engagement.Channel{engagement.ChannelDesktop}
		}
		row, err := rig.svc.Submit(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		rig.svc.Pass(ctx)
		if forget {
			if err := rig.store.SetEngagementMomentParams(ctx, row.ID, "{}"); err != nil {
				t.Fatal(err)
			}
		}
		rig.presence.ScreenLocked = true
		rig.svc.Pass(ctx)
		if len(rig.pushed) != 0 {
			t.Fatalf("forget=%v: prep followed to the phone", forget)
		}
	}
}
