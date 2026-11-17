package main

import (
	"testing"
	"time"
)

// The relay's push for a made-ahead brief yields once, shortly before its
// slot, and never for a brief that owes no push or whose slot is far.
func TestMadeAheadBriefPushesYieldOnceNearTheSlot(t *testing.T) {
	outbox := loadMobileBriefOutbox(t.TempDir())
	slot := time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)
	outbox.record(mobileBriefOutboxEntry{DeviceID: "d1", BriefID: "2026-11-16-evening", Notify: true, DeliverAt: slot.Format(time.RFC3339), QueuedAt: mobileTimestamp(slot.Add(-time.Hour))})
	outbox.record(mobileBriefOutboxEntry{DeviceID: "d2", BriefID: "2026-11-16-evening", Notify: false, DeliverAt: slot.Format(time.RFC3339), QueuedAt: mobileTimestamp(slot.Add(-time.Hour))})
	outbox.record(mobileBriefOutboxEntry{DeviceID: "d3", BriefID: "2026-11-16-evening", Notify: true, QueuedAt: mobileTimestamp(slot.Add(-time.Hour))})
	if early := outbox.takeYields(slot.Add(-10*time.Minute), mobileBriefYieldLead); len(early) != 0 {
		t.Fatalf("yielded ten minutes early: %+v", early)
	}
	got := outbox.takeYields(slot.Add(-time.Minute), mobileBriefYieldLead)
	if len(got) != 1 || got[0].DeviceID != "d1" {
		t.Fatalf("yields = %+v, want d1 only", got)
	}
	if again := outbox.takeYields(slot.Add(-30*time.Second), mobileBriefYieldLead); len(again) != 0 {
		t.Fatalf("yielded twice: %+v", again)
	}
	if entry, ok := outbox.entry("d1"); !ok || !entry.Yielded {
		t.Fatalf("d1 not marked: %+v", entry)
	}
}
