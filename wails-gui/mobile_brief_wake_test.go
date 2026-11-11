package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
)

func TestMobileBriefWakeRecoversMissedSlotThroughSystemSignals(t *testing.T) {
	for _, minute := range []int{20, 30} {
		t.Run(mobileBriefTestDay(7, minute).Format("15:04"), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(6, 0)}
			svc, gathers := newTestMobileBriefService(t, clock)
			t.Cleanup(svc.Stop)
			app := &App{mobileBriefSvc: svc}
			signals := newContextSignalService(app)
			signals.emit = nil // Exercise lifecycle wiring without starting activity capture.
			svc.Start(t.Context())
			// Wait until the initial launch check has completed at 06:00.
			mobileTestWaitFor(t, "the initial schedule check", func() bool {
				svc.runs.mu.Lock()
				defer svc.runs.mu.Unlock()
				file, _, _ := svc.runs.loadLocked()
				return file.FirstSeenAt != ""
			})
			signals.handleChange(t.Context(), activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: clock.Now()})
			clock.Set(mobileBriefTestDay(7, minute))
			svc.checkDue(t.Context())
			if gathers.Load() != 0 {
				t.Fatal("a due tick generated while the Mac was asleep")
			}
			signals.handleChange(t.Context(), activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now()})
			mobileTestWaitFor(t, "the brief recovered on wake", func() bool {
				runs := svc.runs.list()
				return svc.store.exists("2026-10-09-morning") && len(runs) == 1 && runs[0].Status == mobileBriefRunOK
			})
			svc.Stop()
			brief, _ := svc.store.latest()
			if brief.Generation == nil || brief.Generation.Reason != "mac_asleep" || brief.Generation.GeneratedAt != brief.AsOf || brief.Generation.ScheduledAt != formatMobileBriefTime(mobileBriefTestDay(7, 0)) {
				t.Fatalf("generation metadata = %+v", brief.Generation)
			}
			if brief.Generation.Message != "Your morning brief, a bit late." {
				t.Fatalf("reason = %q", brief.Generation.Message)
			}
			data, err := json.Marshal(brief)
			if err != nil || !strings.Contains(string(data), `"generation":{"reason":"mac_asleep"`) {
				t.Fatalf("wire metadata missing: %s, %v", data, err)
			}
			if gathers.Load() != 1 {
				t.Fatalf("gather ran %d times", gathers.Load())
			}
			if runs := svc.runs.list(); len(runs) != 1 || runs[0].Status != mobileBriefRunOK {
				t.Fatalf("recovered run = %+v", runs)
			}
		})
	}
}

func TestMobileBriefWakeReasonLocalized(t *testing.T) {
	for _, tc := range []struct{ slot, language, want string }{
		{mobileBriefSlotMorning, "en", "Your morning brief, a bit late."},
		{mobileBriefSlotEvening, "en", "Your evening brief, a bit late."},
		{mobileBriefSlotMorning, "zh-CN", "你的早间简报，稍晚送达。"},
		{mobileBriefSlotEvening, "zh-CN", "你的晚间简报，稍晚送达。"},
	} {
		if got := mobileBriefLateMessage(tc.slot, tc.language); got != tc.want {
			t.Fatalf("%s %s: %q, want %q", tc.slot, tc.language, got, tc.want)
		}
	}
}

func TestMobileBriefWakeRetriesAndDoesNotDuplicate(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(6, 0)}
	svc, gathers := newTestMobileBriefService(t, clock)
	t.Cleanup(svc.Stop)
	svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: clock.Now()})
	clock.Set(mobileBriefTestDay(7, 20))
	svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now()})
	base := svc.gather
	svc.gather = func(context.Context, string, time.Time, time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{}, errors.New("source unavailable")
	}
	svc.checkDue(t.Context())
	svc.gather = base
	svc.checkDue(t.Context())
	if gathers.Load() != 0 {
		t.Fatal("wake retry skipped its delay")
	}
	clock.Advance(mobileBriefRetryDelay)
	svc.checkDue(t.Context())
	svc.checkDue(t.Context())
	brief, ok := svc.store.latest()
	if !ok || brief.Generation == nil || gathers.Load() != 1 {
		t.Fatalf("retry did not retain the wake reason or duplicated work: %+v, gathers=%d", brief.Generation, gathers.Load())
	}
}

func TestMobileBriefWakeOnlyAttributesObservedSleep(t *testing.T) {
	for _, kind := range []activitymon.SystemStateKind{activitymon.SystemStateUnlocked, activitymon.SystemStateWake} {
		t.Run(string(kind), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 20)}
			svc, gathers := newTestMobileBriefService(t, clock)
			t.Cleanup(svc.Stop)
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: kind, At: clock.Now()})
			svc.checkDue(t.Context())
			// The morning's window is open until the evening: it is made, but
			// never labelled as slept through.
			if latest, ok := svc.store.latest(); gathers.Load() != 1 || !ok || latest.Generation != nil {
				t.Fatal("an unlock or wake without observed sleep claimed a slept-through slot")
			}
		})
	}

	settings := defaultMobileBriefSettings()
	windows := []mobileBriefWakeWindow{
		{SleepAt: mobileBriefTestDay(5, 0), WakeAt: mobileBriefTestDay(6, 0)},
		{SleepAt: mobileBriefTestDay(8, 0), WakeAt: mobileBriefTestDay(14, 0)},
	}
	none := func(string) bool { return false }
	if _, ok := mobileBriefWakeSlot(mobileBriefTestDay(14, 0), time.UTC, settings, none, windows); ok {
		t.Fatal("the awake gap between naps was counted as sleep")
	}
	windows = []mobileBriefWakeWindow{{SleepAt: mobileBriefTestDay(6, 0), WakeAt: mobileBriefTestDay(14, 0)}}
	settings.EnabledAt = formatMobileBriefTime(mobileBriefTestDay(8, 0))
	if _, ok := mobileBriefWakeSlot(mobileBriefTestDay(14, 0), time.UTC, settings, none, windows); ok {
		t.Fatal("a slot before the schedule was enabled was recovered")
	}
}

func TestMobileBriefDroppedWakeRecoversOnNextTick(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(6, 0)}
	svc, gathers := newTestMobileBriefService(t, clock)
	t.Cleanup(svc.Stop)
	svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: clock.Now()})
	clock.Set(mobileBriefTestDay(7, 20))
	// The native callback observed wake, but the full event stream dropped it.
	svc.sleepState = func() activitymon.SystemStateChange {
		return activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now(), SleepAt: mobileBriefTestDay(6, 0)}
	}
	svc.checkDue(t.Context())
	brief, ok := svc.store.latest()
	if !ok || gathers.Load() != 1 || brief.Generation == nil || brief.Generation.Reason != "mac_asleep" {
		t.Fatalf("a lost wake stopped the schedule: brief=%+v gathers=%d", brief, gathers.Load())
	}
	clock.Set(mobileBriefTestDay(18, 0))
	svc.checkDue(t.Context())
	if gathers.Load() != 2 {
		t.Fatalf("later scheduled brief did not run: gathers=%d", gathers.Load())
	}
}

func TestMobileBriefOldWakeDoesNotEndNewSleep(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(6, 0)}
	svc, gathers := newTestMobileBriefService(t, clock)
	t.Cleanup(svc.Stop)
	svc.sleepState = func() activitymon.SystemStateChange {
		return activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now().Add(-time.Minute)}
	}
	svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: clock.Now()})
	svc.checkDue(t.Context())
	svc.mu.Lock()
	sleeping := svc.sleeping
	svc.mu.Unlock()
	if !sleeping || gathers.Load() != 0 {
		t.Fatal("a stale wake observation ended a later sleep")
	}
}

func TestMobileRelaySystemSignalsOnlyRetryOnWake(t *testing.T) {
	client := &mobileRelayClient{retryNow: make(chan struct{}, 1), timing: mobileRelayTimes}
	attempt, cancel := context.WithCancel(t.Context())
	defer cancel()
	client.cancelDial = cancel
	app := &App{mobileBridge: &mobileBridge{client: client}}
	signals := newContextSignalService(app)
	signals.emit = nil
	signals.handleChange(t.Context(), activitymon.SystemStateChange{Kind: activitymon.SystemStateUnlocked, At: mobileBriefTestDay(8, 0)})
	if attempt.Err() != nil || len(client.retryNow) != 0 {
		t.Fatal("unlock retried the relay; only a wake should reset it")
	}
	signals.handleChange(t.Context(), activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: mobileBriefTestDay(8, 1)})
	if attempt.Err() == nil || len(client.retryNow) != 1 {
		t.Fatal("wake did not reset the pending relay attempt")
	}
}

func TestMobileBriefDroppedEventsDoNotMergeSeparateSleeps(t *testing.T) {
	for _, source := range []string{"next tick", "delivered wake"} {
		t.Run(source, func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(5, 0)}
			svc, gathers := newTestMobileBriefService(t, clock)
			t.Cleanup(svc.Stop)
			svc.systemStateChanged(activitymon.SystemStateChange{Kind: activitymon.SystemStateSleep, At: clock.Now()})
			// Wake at 06:00 and sleep at 08:00 were dropped; the 07:00 slot
			// must not be attributed to the later sleep.
			clock.Set(mobileBriefTestDay(14, 0))
			wake := activitymon.SystemStateChange{Kind: activitymon.SystemStateWake, At: clock.Now(), SleepAt: mobileBriefTestDay(8, 0)}
			switch source {
			case "next tick":
				svc.sleepState = func() activitymon.SystemStateChange { return wake }
			case "delivered wake":
				svc.systemStateChanged(wake)
			}
			svc.checkDue(t.Context())
			if _, ok := svc.store.latest(); gathers.Load() != 0 || ok {
				t.Fatal("the expired morning slot was recovered after its grace")
			}
			svc.mu.Lock()
			sleeping := svc.sleeping
			svc.mu.Unlock()
			if sleeping {
				t.Fatal("the latest wake left the schedule asleep")
			}
		})
	}
}
