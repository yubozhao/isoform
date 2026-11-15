//go:build offfeatures

// Feature being deleted (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"testing"
	"time"
)

// TestBumpBackoffLocked covers the exponential backoff curve we use to
// throttle spawn retries after a failure. The contract: 0 → initial,
// then double on each call, capped at max. Anything else and a wedged
// daemon could either fork-bomb (no growth) or wait forever (no cap).
func TestBumpBackoffLocked(t *testing.T) {
	d := &CuaDriverDaemon{}

	want := []time.Duration{
		cuaDriverSpawnInitialBackoff, // first failure → 1s
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		cuaDriverSpawnMaxBackoff, // 32s would exceed cap → 30s
		cuaDriverSpawnMaxBackoff, // stays at cap
		cuaDriverSpawnMaxBackoff,
	}
	for i, w := range want {
		d.bumpBackoffLocked()
		if d.backoff != w {
			t.Errorf("after bump #%d: backoff=%v want %v", i+1, d.backoff, w)
		}
		if d.nextSpawnOK.IsZero() {
			t.Errorf("after bump #%d: nextSpawnOK should be set", i+1)
		}
	}
}

// TestBumpBackoffFromZeroAfterReset confirms we restart the curve at
// the initial value after a successful spawn cleared backoff to 0,
// not from the previous max — otherwise a single bad day would
// permanently keep the daemon in 30s mode.
func TestBumpBackoffFromZeroAfterReset(t *testing.T) {
	d := &CuaDriverDaemon{backoff: cuaDriverSpawnMaxBackoff}
	d.backoff = 0 // simulate reset after successful spawn
	d.bumpBackoffLocked()
	if d.backoff != cuaDriverSpawnInitialBackoff {
		t.Errorf("post-reset bump: backoff=%v want %v", d.backoff, cuaDriverSpawnInitialBackoff)
	}
}

// TestRemainingBackoffLocked checks the three states EnsureRunning
// branches on: (a) never bumped → no wait, (b) bumped recently → wait
// until nextSpawnOK, (c) bumped but window already past → no wait.
func TestRemainingBackoffLocked(t *testing.T) {
	t.Run("zero state means no wait", func(t *testing.T) {
		d := &CuaDriverDaemon{}
		if got := d.remainingBackoffLocked(); got != 0 {
			t.Errorf("got %v want 0", got)
		}
	})

	t.Run("future window returns positive duration", func(t *testing.T) {
		d := &CuaDriverDaemon{nextSpawnOK: time.Now().Add(2 * time.Second)}
		got := d.remainingBackoffLocked()
		if got <= 0 || got > 2*time.Second {
			t.Errorf("got %v want (0,2s]", got)
		}
	})

	t.Run("expired window returns zero", func(t *testing.T) {
		d := &CuaDriverDaemon{nextSpawnOK: time.Now().Add(-1 * time.Hour)}
		if got := d.remainingBackoffLocked(); got != 0 {
			t.Errorf("got %v want 0", got)
		}
	})
}
