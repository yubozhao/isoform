package main

import "testing"

// A belief update is not a privacy action. Each phone Today rebuild reads every
// stored brief (todayBriefs), so dropping the projection on every
// memory:updated read about 60 sealed briefs per update, all day (2026-11-15).
// Only a memory update that advanced the privacy epoch (a forget or
// correction) drops it.
func TestPhoneTodayDropsOnlyOnPrivacyMemoryUpdates(t *testing.T) {
	app, _ := derivedTestApp(t)
	bridge := &mobileBridge{}
	bridge.today.build = func() (MobileTodayV1, error) { return MobileTodayV1{GeneratedAt: "built"}, nil }
	app.mobileBridgeMu.Lock()
	app.mobileBridge = bridge
	app.mobileBridgeMu.Unlock()
	t.Cleanup(func() {
		app.mobileBridgeMu.Lock()
		app.mobileBridge = nil
		app.mobileBridgeMu.Unlock()
	})
	drops := func() uint64 {
		bridge.today.mu.RLock()
		defer bridge.today.mu.RUnlock()
		return bridge.today.epoch
	}
	if _, err := bridge.rebuildToday(); err != nil {
		t.Fatal(err)
	}
	// The first update after launch has nothing to compare with: it drops.
	app.emitBrowserEvent("memory:updated", map[string]string{"reason": "index"})
	first := drops()
	if first == 0 {
		t.Fatal("the first memory:updated did not drop the phone Today projection")
	}
	for range 5 {
		app.emitBrowserEvent("memory:updated", map[string]string{"reason": "index"})
	}
	if got := drops(); got != first {
		t.Fatalf("belief updates dropped the phone Today projection %d times", got-first)
	}
	// A forget advances the privacy epoch before it emits memory:updated.
	briefPrivacyChanged(briefWithdrawn{Kind: string(MemorySourceMemory), ID: "forgotten"})
	app.emitBrowserEvent("memory:updated")
	if got := drops(); got != first+1 {
		t.Fatalf("a forget dropped the phone Today projection %d times, want 1", got-first)
	}
	// The privacy half of a person's own memory edit drops it every time.
	app.emitBrowserEvent("memory:withdrawn")
	app.emitBrowserEvent("memory:updated")
	if got := drops(); got != first+2 {
		t.Fatalf("memory:withdrawn dropped the phone Today projection %d times, want 1", got-first-1)
	}
	first++
	// Other privacy events still drop it every time.
	app.emitBrowserEvent("topics:withdrawn")
	if got := drops(); got != first+2 {
		t.Fatal("topics:withdrawn no longer drops the phone Today projection")
	}
}
