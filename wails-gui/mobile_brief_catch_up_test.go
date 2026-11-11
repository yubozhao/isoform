package main

import (
	"context"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/platform/activitymon"
)

// catchUpRig is a schedule watched since the start of the test day, fixed or
// adaptive, whose missed notices can be counted.
func catchUpRig(t *testing.T, clock *fakeMobileBriefClock, adaptive bool) (*mobileBriefService, *engagementTestRig, *mobileBriefTimingInput) {
	t.Helper()
	svc, rig := missedBriefRig(t, clock)
	// A build with no written words does not stand in for its slot, so these
	// rigs write words the way a model brief does (design R74).
	svc.compose = testModelCompose
	input := &mobileBriefTimingInput{}
	if adaptive {
		enableTestAdaptiveBrief(t, svc, input)
	}
	return svc, rig, input
}

func catchUpMode(adaptive bool) string {
	if adaptive {
		return "adaptive"
	}
	return "fixed"
}

func TestMobileBriefFirstDeliveryUsesCompletedRunTiming(t *testing.T) {
	for _, adaptive := range []bool{false, true} {
		t.Run(catchUpMode(adaptive), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(6, 50)}
			svc, _, _ := catchUpRig(t, clock, adaptive)
			svc.app.mobileBriefSvc = svc
			var deliveries []MobileBriefV1
			svc.publish = func(brief MobileBriefV1, _ bool) error {
				deliveries = append(deliveries, brief)
				return nil
			}
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: clock.Now()})
			clock.Set(mobileBriefTestDay(7, 40))
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now()})
			svc.checkDue(context.Background())
			if len(deliveries) != 1 || deliveries[0].Generation == nil || deliveries[0].Generation.Reason != mobileBriefMissedAsleep {
				t.Fatalf("first delivery did not use the completed run: %+v", deliveries)
			}
			if runs := svc.runs.list(); len(runs) != 1 || runs[0].Status != mobileBriefRunOK {
				t.Fatalf("completed runs = %+v", runs)
			}
		})
	}
}

// Waking 20 minutes after the configured evening time still makes the brief.
func TestMobileBriefSleptThroughTheEveningIsMadeOnWake(t *testing.T) {
	for _, adaptive := range []bool{false, true} {
		t.Run(catchUpMode(adaptive), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(16, 0)}
			svc, rig, _ := catchUpRig(t, clock, adaptive)
			ctx := context.Background()
			if _, err := svc.generate(ctx, mobileBriefSlotMorning, mobileBriefReasonSchedule); err != nil {
				t.Fatal(err)
			}
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: mobileBriefTestDay(17, 0)})
			clock.Set(mobileBriefTestDay(18, 10))
			svc.checkDue(ctx)
			if svc.store.exists("2026-10-09-evening") {
				t.Fatal("a brief was made while the computer slept")
			}
			clock.Set(mobileBriefTestDay(18, 20))
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now()})
			svc.checkDue(ctx)
			evening, ok := svc.store.load("2026-10-09-evening")
			if !ok || evening.AsOf != formatMobileBriefTime(mobileBriefTestDay(18, 20)) {
				t.Fatalf("the evening brief was not made on waking: %+v", evening.AsOf)
			}
			if evening.Generation == nil || evening.Generation.Reason != "mac_asleep" || evening.Generation.Message != "Your evening brief, a bit late." {
				t.Fatalf("the late evening is not labelled: %+v", evening.Generation)
			}
			svc.checkDue(ctx)
			var eveningRuns int
			for _, run := range svc.runs.list() {
				if run.BriefID == evening.BriefID && run.Status == mobileBriefRunOK {
					eveningRuns++
				}
			}
			if eveningRuns != 1 {
				t.Fatalf("the evening was generated %d times", eveningRuns)
			}
			if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 0 {
				t.Fatalf("a brief made on waking was announced missed: %+v", moments)
			}
		})
	}
}

// Asleep from 17:00 until the next morning: the evening is skipped with one
// missed notice, and the morning brief is made.
func TestMobileBriefSleptUntilMorningSkipsTheEveningOnce(t *testing.T) {
	for _, adaptive := range []bool{false, true} {
		t.Run(catchUpMode(adaptive), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(16, 0)}
			svc, rig, _ := catchUpRig(t, clock, adaptive)
			ctx := context.Background()
			if _, err := svc.generate(ctx, mobileBriefSlotMorning, mobileBriefReasonSchedule); err != nil {
				t.Fatal(err)
			}
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: mobileBriefTestDay(17, 0)})
			clock.Set(mobileBriefTestDay(7, 30).AddDate(0, 0, 1))
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now()})
			svc.checkDue(ctx)
			svc.checkDue(ctx)
			if svc.store.exists("2026-10-09-evening") {
				t.Fatal("yesterday's evening was made the next morning")
			}
			moments := openMoments(t, rig.store, string(engagement.KindBriefMissed))
			if len(moments) != 1 || moments[0].SubjectID != "2026-10-09-evening" {
				t.Fatalf("want one missed notice for the evening, got %+v", moments)
			}
			if !svc.store.exists("2026-10-10-morning") {
				t.Fatal("the morning brief was not made")
			}
		})
	}
}

// Waking at 21:00, three hours after the evening slot, still makes the
// evening brief: its window stays open until midnight.
func TestMobileBriefWakeBeforeMidnightStillMakesEvening(t *testing.T) {
	for _, adaptive := range []bool{false, true} {
		t.Run(catchUpMode(adaptive), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(16, 0)}
			svc, rig, _ := catchUpRig(t, clock, adaptive)
			ctx := context.Background()
			if _, err := svc.generate(ctx, mobileBriefSlotMorning, mobileBriefReasonSchedule); err != nil {
				t.Fatal(err)
			}
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: mobileBriefTestDay(17, 0)})
			clock.Set(mobileBriefTestDay(21, 0))
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now()})
			svc.checkDue(ctx)
			svc.checkDue(ctx)
			evening, ok := svc.store.load("2026-10-09-evening")
			if !ok || evening.Generation == nil || evening.Generation.Reason != "mac_asleep" {
				t.Fatalf("the evening was not made on waking before midnight: ok=%v generation=%+v", ok, evening.Generation)
			}
			if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 0 {
				t.Fatalf("an evening made on waking was announced missed: %+v", moments)
			}
		})
	}
}

func TestMobileBriefConfiguredMorningSlotFiresDuringQuietHours(t *testing.T) {
	for _, adaptive := range []bool{false, true} {
		t.Run(catchUpMode(adaptive), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
			svc, _, input := catchUpRig(t, clock, adaptive)
			input.Quiet = engagement.QuietWindow{Start: 22 * time.Hour, End: 7*time.Hour + 30*time.Minute}
			svc.checkDue(context.Background())
			morning, ok := svc.store.load("2026-10-09-morning")
			if !ok || morning.AsOf != formatMobileBriefTime(mobileBriefTestDay(7, 0)) {
				t.Fatalf("configured morning slot did not fire at 07:00: %+v", morning.AsOf)
			}
		})
	}
}

// The configured time wins over later activity or meetings.
func TestMobileBriefAdaptiveEveningRunsAtConfiguredTimeWhileActivityAndMeetingContinue(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(17, 55)}
	svc, rig, input := catchUpRig(t, clock, true)
	ctx := context.Background()
	if _, err := svc.generate(ctx, mobileBriefSlotMorning, mobileBriefReasonSchedule); err != nil {
		t.Fatal(err)
	}
	for at := mobileBriefTestDay(17, 55); !at.After(mobileBriefTestDay(18, 5)); at = at.Add(5 * time.Minute) {
		clock.Set(at)
		input.Today = mobileBriefTimingDay{Date: "2026-10-09", LastActivity: at, LastMeeting: mobileBriefTestDay(20, 0)}
		svc.checkDue(ctx)
		// A scheduled build starts ten minutes before its 18:00 slot.
		if at.Before(mobileBriefTestDay(17, 50)) && svc.store.exists("2026-10-09-evening") {
			t.Fatalf("the evening brief was built before its build time at %s", at.Format("15:04"))
		}
	}
	evening, ok := svc.store.load("2026-10-09-evening")
	// The build starts at the first check at or after ten minutes before the
	// 18:00 slot, which is 17:55 on this five-minute walk.
	if !ok || evening.AsOf != formatMobileBriefTime(mobileBriefTestDay(17, 55)) || evening.Generation != nil {
		t.Fatalf("the evening brief was not built at its build time: %+v", evening.AsOf)
	}
	if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 0 {
		t.Fatalf("the evening was announced missed: %+v", moments)
	}
}

func TestMobileBriefWakeDoesNotRepeatRequestedBrief(t *testing.T) {
	for _, adaptive := range []bool{false, true} {
		t.Run(catchUpMode(adaptive), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(6, 50)}
			svc, _, _ := catchUpRig(t, clock, adaptive)
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: clock.Now()})
			clock.Set(mobileBriefTestDay(9, 0))
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now()})
			if _, err := svc.generate(context.Background(), mobileBriefSlotOnRequest, mobileBriefReasonSettings); err != nil {
				t.Fatal(err)
			}
			svc.checkDue(context.Background())
			if svc.store.exists("2026-10-09-morning") {
				t.Fatal("wake recovery repeated the requested brief")
			}
		})
	}
}
