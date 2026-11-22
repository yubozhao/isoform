package main

import (
	"testing"
	"time"
)

func TestWatchPhoneQuietThresholds(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		ago       time.Duration
		heardAgo  time.Duration // 0: no watch audio known
		spans     []observationPause
		quiet     bool
		listening bool
	}{
		// Audio arrived within two hours before the phone's last contact: listening, 3 hours.
		{"audio 1h50 before contact, at 3 hours", 3 * time.Hour, 4*time.Hour + 50*time.Minute, nil, true, true},
		{"audio exactly 2h before contact, at 3 hours", 3 * time.Hour, 5 * time.Hour, nil, true, true},
		{"audio 2h10 before contact, at 3 hours", 3 * time.Hour, 5*time.Hour + 10*time.Minute, nil, false, false},
		{"audio just before, under 3 hours", 2*time.Hour + 59*time.Minute, 3 * time.Hour, nil, false, true},
		{"audio just before, at 3 hours", 3 * time.Hour, 3*time.Hour + 50*time.Minute, nil, true, true},
		// Setting on but no recent audio (watch off the wrist): not listening, 12 hours.
		{"setting on, audio long before, at 3 hours", 3 * time.Hour, 10 * time.Hour, nil, false, false},
		{"setting on, audio long before, under 12 hours", 11 * time.Hour, 20 * time.Hour, nil, false, false},
		{"setting on, audio long before, at 12 hours", 12 * time.Hour, 20 * time.Hour, nil, true, false},
		// No audio known: the off stretches decide, as before.
		{"no audio, no off, at 3 hours", 3 * time.Hour, 0, nil, true, true},
		{"no audio, off at the time, at 3 hours", 3 * time.Hour, 0, []observationPause{{Start: now.Add(-5 * time.Hour).UnixMilli()}}, false, false},
		{"no audio, off at the time, at 12 hours", 12 * time.Hour, 0, []observationPause{{Start: now.Add(-20 * time.Hour).UnixMilli()}}, true, false},
		{"no audio, off ended before, at 3 hours", 3 * time.Hour, 0, []observationPause{{Start: now.Add(-9 * time.Hour).UnixMilli(), End: now.Add(-8 * time.Hour).UnixMilli()}}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var heard time.Time
			if c.heardAgo > 0 {
				heard = now.Add(-c.heardAgo)
			}
			got := watchPhoneQuietAt(now.Add(-c.ago), heard, c.spans, now)
			if got.Quiet != c.quiet || got.Listening != c.listening {
				t.Fatalf("quiet=%v listening=%v, want quiet=%v listening=%v", got.Quiet, got.Listening, c.quiet, c.listening)
			}
			if got.Since != mobileTimestamp(now.Add(-c.ago)) {
				t.Fatalf("since = %q", got.Since)
			}
		})
	}
}

func TestWatchPhoneQuietNeverSeen(t *testing.T) {
	got := watchPhoneQuietAt(time.Time{}, time.Time{}, nil, time.Now())
	if got.Quiet || got.Since != "" || got.OffPending {
		t.Fatalf("never seen: %+v", got)
	}
}

func TestWatchListeningOffPendingUntilThePhoneIsInTouch(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	off := []observationPause{{Start: now.Add(-time.Hour).UnixMilli()}}
	if got := watchPhoneQuietAt(now.Add(-2*time.Hour), time.Time{}, off, now); !got.OffPending {
		t.Fatalf("phone last in touch before the off: want pending, got %+v", got)
	}
	if got := watchPhoneQuietAt(now.Add(-30*time.Minute), time.Time{}, off, now); got.OffPending {
		t.Fatalf("phone in touch after the off: want not pending, got %+v", got)
	}
	if got := watchPhoneQuietAt(time.Time{}, time.Time{}, off, now); !got.OffPending {
		t.Fatalf("never seen with listening off: want pending, got %+v", got)
	}
	closed := []observationPause{{Start: now.Add(-3 * time.Hour).UnixMilli(), End: now.Add(-time.Hour).UnixMilli()}}
	if got := watchPhoneQuietAt(now.Add(-2*time.Hour), time.Time{}, closed, now); got.OffPending {
		t.Fatalf("listening back on: want not pending, got %+v", got)
	}
}

func TestGetWatchPhoneQuietFailsWithoutAStore(t *testing.T) {
	if _, err := (&App{}).GetWatchPhoneQuiet(); err == nil {
		t.Fatal("want an error when the store is unavailable")
	}
}
