package main

import (
	"testing"
	"time"
)

// A stop that fixed itself is only a gap in Activity (computer-trouble
// v1.0.0 rule 2): a run that didn't end cleanly leaves "restarted" from its
// last check to the next start. A quit, a restart of the computer itself, a
// stop under two minutes or over two hours, and a first start leave none.
func TestTroubleRestartIsAGapOnlyAfterAStopThatFixedItself(t *testing.T) {
	app, _, _ := dailyTopicsTestApp(t)
	previousBoot := troubleBootTime
	t.Cleanup(func() { troubleBootTime = previousBoot; troubleRestartChecked.Store(false) })
	now := time.Date(2026, 11, 16, 17, 13, 0, 0, time.Local)
	window := func() []TroubleStretch {
		stretches, err := app.troubleStretches(now.Add(-3*time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		return stretches
	}
	start := func(last *troubleAlive, boot time.Time) {
		t.Helper()
		troubleBootTime = func() time.Time { return boot }
		troubleRestartChecked.Store(false)
		if last != nil {
			writeTroubleAlive(*last)
		}
		app.noteTroubleAlive(now)
	}
	bootedYesterday := now.Add(-24 * time.Hour)

	// The first start ever: nothing to compare with.
	start(nil, bootedYesterday)
	if got := window(); len(got) != 0 {
		t.Fatalf("a first start left %+v", got)
	}
	if alive, ok := readTroubleAlive(); !ok || alive.Clean || alive.At != mobileTimestamp(now) {
		t.Fatalf("the running note = %+v %v", alive, ok)
	}
	for name, c := range map[string]struct {
		last troubleAlive
		boot time.Time
	}{
		"a quit":                 {troubleAlive{At: mobileTimestamp(now.Add(-10 * time.Minute)), Clean: true}, bootedYesterday},
		"the computer restarted": {troubleAlive{At: mobileTimestamp(now.Add(-10 * time.Minute))}, now.Add(-5 * time.Minute)},
		"under two minutes":      {troubleAlive{At: mobileTimestamp(now.Add(-time.Minute))}, bootedYesterday},
		"over two hours":         {troubleAlive{At: mobileTimestamp(now.Add(-3 * time.Hour))}, bootedYesterday},
	} {
		start(&c.last, c.boot)
		if got := window(); len(got) != 0 {
			t.Fatalf("%s left %+v", name, got)
		}
	}

	// A crash at 5:03 and a start at 5:13: one gap, 5:03 to 5:13.
	crashed := now.Add(-10 * time.Minute)
	start(&troubleAlive{At: mobileTimestamp(crashed)}, bootedYesterday)
	got := window()
	if len(got) != 1 || got[0].Cause != troubleRestarted || got[0].StartMs != crashed.UnixMilli() || got[0].EndMs != now.UnixMilli() {
		t.Fatalf("the restart gap = %+v", got)
	}
	// Only the first check after a start looks; later checks keep it as is.
	app.noteTroubleAlive(now.Add(time.Minute))
	if again := window(); len(again) != 1 {
		t.Fatalf("a later check changed the gaps: %+v", again)
	}
	// Trouble ending later doesn't touch a restart gap.
	app.recordTroubleGaps(nil, now.Add(2*time.Minute))
	if after := window(); len(after) != 1 || after[0].EndMs != now.UnixMilli() {
		t.Fatalf("the restart gap after a trouble check = %+v", after)
	}
	// A quit is noted as clean.
	noteTroubleQuit(now.Add(3 * time.Minute))
	if alive, ok := readTroubleAlive(); !ok || !alive.Clean {
		t.Fatalf("the quit note = %+v %v", alive, ok)
	}
	if boot := systemBootTime(); !boot.IsZero() && boot.After(time.Now()) {
		t.Fatalf("the computer started in the future: %v", boot)
	}
}
