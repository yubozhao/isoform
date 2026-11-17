package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

func TestST82CapCountsAllAutomaticLevelsAndSurvivesRestart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     engagement.Kind
		deadline bool
	}{
		// Under notify v1.0.0 B3 no automatic passive kind rings (passive
		// moments are quiet lines on Today), so the cap counts these two.
		{"active", engagement.KindWorkFinished, false},
		{"time-sensitive", engagement.KindMeetingPrep, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newEngagementTestRig(t)
			ctx := context.Background()
			if err := rig.svc.SetRules(ctx, engagement.QuietWindow{}, 1); err != nil {
				t.Fatal(err)
			}
			m := engagement.Moment{Kind: tc.kind, SubjectID: "first", HardDeadline: tc.deadline, DueAt: rig.clock.Add(time.Hour)}
			if _, err := rig.svc.Submit(ctx, m); err != nil {
				t.Fatal(err)
			}
			rig.svc.Pass(ctx)
			if len(rig.pushed) != 1 {
				t.Fatalf("first notice = %d", len(rig.pushed))
			}
			fresh := newEngagementService(rig.store, rig.svc.presence, rig.svc.deliver)
			fresh.now = rig.svc.now
			if err := fresh.load(ctx); err != nil {
				t.Fatal(err)
			}
			rig.svc = fresh
			next, err := fresh.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "second"})
			if err != nil {
				t.Fatal(err)
			}
			fresh.Pass(ctx)
			got, err := rig.store.GetEngagementMoment(ctx, next.ID)
			if err != nil || got.State != storage.EngagementStateScheduled || got.Reason != "budget" || len(rig.pushed) != 1 {
				t.Fatalf("cap: row=%+v sends=%d err=%v", got, len(rig.pushed), err)
			}
			app := &App{store: rig.store, engagement: fresh}
			summary, err := app.GetEngagementWeekSummary()
			if err != nil || summary.Sent != 1 {
				t.Fatalf("summary=%+v err=%v", summary, err)
			}
		})
	}
}

func TestST82ChatOnlyAndRequestedReminderDoNotConsumeCap(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if err := rig.svc.SetRules(ctx, engagement.QuietWindow{}, 1); err != nil {
		t.Fatal(err)
	}
	app := &App{store: rig.store, engagement: rig.svc, eventBroadcaster: NewEventBroadcaster()}
	rig.svc.deliver.Report = app.postEngagementReport
	for _, kind := range []engagement.Kind{engagement.KindWorkFinished, engagement.KindReminder} {
		if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: kind, SubjectID: string(kind), Params: map[string]string{"title": "Example"}}); err != nil {
			t.Fatal(err)
		}
		rig.svc.Pass(ctx)
	}
	if len(rig.pushed) != 1 {
		t.Fatalf("requested reminder deliveries = %d", len(rig.pushed))
	}
	if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindMeetingPrep, SubjectID: "automatic"}); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	summary, err := app.GetEngagementWeekSummary()
	if err != nil || len(rig.pushed) != 2 || summary.Sent != 1 {
		t.Fatalf("automatic=%d summary=%+v err=%v", len(rig.pushed), summary, err)
	}
}

func TestST82CapturePauseIsNeverANotificationKind(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	app := &App{store: rig.store, engagement: rig.svc, eventBroadcaster: NewEventBroadcaster()}
	rig.svc.deliver.Report = app.postEngagementReport
	m := engagement.Moment{Kind: engagement.KindCapturePaused, SubjectID: "screen", CreatedAt: rig.clock.Add(-2 * time.Hour), Params: map[string]string{"what": "Screen capture", "cause": "stopped", "duration": "2 hours"}}
	if _, err := rig.svc.Submit(ctx, m); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if len(rig.pushed)+len(rig.desktop) != 0 {
		t.Fatal("capture stop sent a notification")
	}
	view, err := engagementSettingsView(ctx, rig.svc)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range view.Kinds {
		if kind.Kind == string(engagement.KindCapturePaused) {
			t.Fatal("capture pause remains in Settings")
		}
	}
}

func TestST82MissedBriefIsIdempotentAfterSendAndRestart(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	app := &App{store: rig.store, engagement: rig.svc}
	id := "2026-10-12-morning"
	if !app.produceBriefMissed(id, "morning") {
		t.Fatal("first submission failed")
	}
	rig.svc.Pass(ctx)
	fresh := newEngagementService(rig.store, rig.svc.presence, rig.svc.deliver)
	fresh.now = rig.svc.now
	if err := fresh.load(ctx); err != nil {
		t.Fatal(err)
	}
	app.engagement = fresh
	for i := 0; i < 3; i++ {
		if !app.produceBriefMissed(id, "morning") {
			t.Fatalf("repeat %d was not acknowledged", i)
		}
		fresh.Pass(ctx)
	}
	rows := openMoments(t, rig.store, string(engagement.KindBriefMissed))
	// One row however often it is produced; a missed brief is a quiet line
	// on Today, never a push (B3).
	if len(rows) != 1 || len(rig.pushed) != 0 {
		t.Fatalf("rows=%s sends=%d", fmt.Sprint(rows), len(rig.pushed))
	}
	if !mobileValidUUID(rows[0].ID) {
		t.Fatalf("notice ID is not phone-compatible: %s", rows[0].ID)
	}
}

func TestST82StandaloneNotificationsShareBudget(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if err := rig.svc.SetRules(ctx, engagement.QuietWindow{}, 1); err != nil {
		t.Fatal(err)
	}
	sent := 0
	if !rig.svc.deliverAutomaticDesktop(func() { sent++ }) {
		t.Fatal("first standalone was blocked")
	}
	if rig.svc.deliverAutomaticDesktop(func() { sent++ }) {
		t.Fatal("standalone bypassed budget")
	}
	if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "after-standalone"}); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	summary, err := (&App{store: rig.store, engagement: rig.svc}).GetEngagementWeekSummary()
	if err != nil || sent != 1 || len(rig.pushed) != 0 || summary.Sent != 1 {
		t.Fatalf("sent=%d push=%d summary=%+v err=%v", sent, len(rig.pushed), summary, err)
	}
}

func TestST82FailedSendDoesNotSpendBudget(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if err := rig.svc.SetRules(ctx, engagement.QuietWindow{}, 1); err != nil {
		t.Fatal(err)
	}
	rig.pushError = context.DeadlineExceeded
	if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "first"}); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	rig.pushError = nil
	if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "second"}); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if len(rig.pushed) != 1 {
		t.Fatalf("successful notices=%d", len(rig.pushed))
	}
}

func TestST82MissedBriefConcurrentSubmissionsShareOneMoment(t *testing.T) {
	rig := newEngagementTestRig(t)
	app := &App{store: rig.store, engagement: rig.svc}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); app.produceBriefMissed("2026-10-12-evening", "evening") }()
	}
	wg.Wait()
	if got := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(got) != 1 {
		t.Fatalf("duplicate missed brief moments: %+v", got)
	}
}

func TestST82PartialDeliveryRetriesSameMomentWithoutNewBudgetSlot(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if err := rig.svc.SetRules(ctx, engagement.QuietWindow{}, 1); err != nil {
		t.Fatal(err)
	}
	rig.pushError = context.DeadlineExceeded
	row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "partial", Channels: []engagement.Channel{engagement.ChannelDesktop, engagement.ChannelPush}})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if len(rig.desktop) != 1 || len(rig.pushed) != 0 {
		t.Fatal("first channel did not deliver")
	}
	rig.pushError = nil
	rig.clock = rig.clock.Add(engagementRetryDelay)
	if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "other"}); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	got, err := rig.store.GetEngagementMoment(ctx, row.ID)
	summary, sumErr := (&App{store: rig.store, engagement: rig.svc}).GetEngagementWeekSummary()
	if err != nil || sumErr != nil || got.State != storage.EngagementStateSent || len(rig.desktop) != 1 || len(rig.pushed) != 1 || summary.Sent != 1 {
		t.Fatalf("row=%+v desktop=%d push=%d summary=%+v errors=%v %v", got, len(rig.desktop), len(rig.pushed), summary, err, sumErr)
	}
}

func TestST82PhonePacingUsesAutomaticNotificationReceipts(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if _, err := rig.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{Kind: "brief_missed", DedupeKey: "brief-fixture", State: storage.EngagementStateSent, Channel: "push", Level: "passive", CreatedAt: rig.clock.UnixMilli(), SentAt: rig.clock.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{Kind: "reminder", DedupeKey: "reminder-fixture", State: storage.EngagementStateSent, Channel: "push", Level: "time-sensitive", CreatedAt: rig.clock.UnixMilli(), SentAt: rig.clock.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if got := mobileTodayPacing(rig.svc, rig.clock); got == nil || got.ActiveToday != 1 {
		t.Fatalf("phone pacing=%+v", got)
	}
}

func TestST82DoesNotSendWhileSavedPreferencesAreUnavailable(t *testing.T) {
	rig := newEngagementTestRig(t)
	rig.svc.loadPending = true
	if err := rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	delivered := false
	if rig.svc.deliverAutomaticDesktop(func() { delivered = true }) || delivered {
		t.Fatal("sent before saved budget was read")
	}
}
