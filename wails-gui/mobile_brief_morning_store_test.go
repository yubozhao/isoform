package main

import (
	"testing"
	"time"
)

// 2026-11-13: the made-ahead copy from 01:07 made the adaptive 07:00 build
// look delivered, so every build after it was thrown away. A made-ahead copy
// never counts as the slot's brief.
func TestMadeAheadCopyDoesNotBlockTheRealBuild(t *testing.T) {
	svc, _ := newTestMobileBriefService(t, &fakeMobileBriefClock{now: mobileBriefTestDay(6, 50)})
	now := mobileBriefTestDay(1, 7)
	ahead := buildMobileBriefV1(mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: now, WindowStart: now.Add(-time.Hour), Location: time.UTC})
	ahead.Generation = &MobileBriefGeneration{Reason: mobileBriefGenerationMadeAhead, ScheduledAt: formatMobileBriefTime(mobileBriefTestDay(7, 0)), GeneratedAt: formatMobileBriefTime(now)}
	if err := svc.store.saveDocument(ahead, now); err != nil {
		t.Fatal(err)
	}
	if svc.finalCopyExists(ahead.BriefID) {
		t.Fatal("a made-ahead copy blocked the slot's real build")
	}
	final := ahead
	final.Generation = nil
	if err := svc.store.saveDocument(final, now); err != nil {
		t.Fatal(err)
	}
	if !svc.finalCopyExists(final.BriefID) {
		t.Fatal("a real copy did not count")
	}
}
