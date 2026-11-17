package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// Where it rings (notify v1.0.0 rules 1 to 4, B1, B2): one notification, on
// the device the person is at, once.

func whereTestBrief(t *testing.T, rig *engagementTestRig, id string) *storage.EngagementMomentRow {
	t.Helper()
	rig.svc.deliver.Report = func(engagement.Moment, engagementText) bool { return true }
	ctx := context.Background()
	row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindBriefReady, SubjectID: id,
		DedupeKey: engagement.DedupeKey(engagement.KindBriefReady, id), Params: map[string]string{"slot": "evening"}})
	if err != nil {
		t.Fatalf("submit: %+v %v", row, err)
	}
	rig.svc.Pass(ctx)
	stored, err := rig.store.GetEngagementMoment(ctx, row.ID)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	return stored
}

func TestTheBriefRingsOnTheComputerOnlyWhileThePersonIsAtIt(t *testing.T) {
	rig := newEngagementTestRig(t)
	rig.presence.MacIdle = 20 * time.Second // typing in another app
	stored := whereTestBrief(t, rig, "2026-10-12-evening")
	if stored.State != storage.EngagementStateSent || stored.Channel != "chat+desktop" || len(rig.desktop) != 1 || len(rig.pushed) != 0 {
		t.Fatalf("at the computer: %+v desktop=%d pushed=%d", stored, len(rig.desktop), len(rig.pushed))
	}
}

func TestTheBriefRingsOnThePhoneWhileTheComputerIsLocked(t *testing.T) {
	rig := newEngagementTestRig(t)
	rig.presence.ScreenLocked, rig.presence.MacIdle = true, 0
	stored := whereTestBrief(t, rig, "2026-10-12-evening")
	if stored.State != storage.EngagementStateSent || stored.Channel != "chat+push" || len(rig.pushed) != 1 || len(rig.desktop) != 0 {
		t.Fatalf("locked: %+v desktop=%d pushed=%d", stored, len(rig.desktop), len(rig.pushed))
	}
}

func TestTheBriefWaitsOnTheComputerWhenThePhoneCantBeReached(t *testing.T) {
	rig := newEngagementTestRig(t)
	rig.pushError = errors.New("the relay is offline")
	stored := whereTestBrief(t, rig, "2026-10-12-evening")
	if stored.State != storage.EngagementStateSent || stored.Channel != "chat+desktop" || len(rig.desktop) != 1 {
		t.Fatalf("push failed: %+v desktop=%d", stored, len(rig.desktop))
	}
	rig = newEngagementTestRig(t)
	rig.presence = engagement.Presence{ScreenLocked: true}
	if stored := whereTestBrief(t, rig, "2026-10-12-evening"); stored.Channel != "chat+desktop" || len(rig.desktop) != 1 {
		t.Fatalf("no phone, locked: %+v desktop=%d", stored, len(rig.desktop))
	}
}

// Finished work at the computer isn't a notification: it's a quiet line on
// Today. Away, the phone rings.
func TestFinishedWorkAtTheComputerIsAQuietLine(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		presence engagement.Presence
		channel  string
		pushes   int
	}{
		{"at the computer", engagement.Presence{PhoneRegistered: true, MacIdle: 10 * time.Second}, string(engagement.ChannelTodayQuiet), 0},
		{"away", engagement.Presence{PhoneRegistered: true, MacIdle: time.Hour}, string(engagement.ChannelPush), 1},
		{"away, no phone", engagement.Presence{MacIdle: time.Hour}, string(engagement.ChannelTodayQuiet), 0},
	} {
		rig := newEngagementTestRig(t)
		rig.presence = c.presence
		row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "task-" + c.name})
		if err != nil {
			t.Fatal(err)
		}
		rig.svc.Pass(ctx)
		stored, err := rig.store.GetEngagementMoment(ctx, row.ID)
		if err != nil || stored == nil || stored.State != storage.EngagementStateSent || stored.Channel != c.channel ||
			len(rig.pushed) != c.pushes || len(rig.desktop) != 0 {
			t.Fatalf("%s: %+v pushed=%d desktop=%d %v", c.name, stored, len(rig.pushed), len(rig.desktop), err)
		}
	}
}
