package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// A card's voice verdict is a snapshot: a page of cards projected again reuses
// it, and each change signal drops it. A stream writer appending to a side chat
// all the while changes none of them.
func TestProactiveReadVerdictsAreReusedUntilASourceChanges(t *testing.T) {
	a, session, _ := registryChatTestApp(t)
	var live atomic.Int64
	gate := func(id, kind string) bool {
		return a.proactiveReadVerdict(t.Context(), proactiveReadVerdictKey{id: id, kind: kind, presentation: "p"}, func() bool {
			live.Add(1)
			time.Sleep(10 * time.Millisecond) // the source reload
			return true
		})
	}
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = a.store.AppendUserStreamItemAtomic(context.Background(), session, storage.StreamItemRow{ID: "w" + time.Now().Format("150405.000000000"), Kind: "user", Content: "writer words"})
			time.Sleep(5 * time.Millisecond)
		}
	}()
	defer func() { close(stop); writer.Wait() }()

	kind := string(engagement.KindBriefReady)
	project := func() time.Duration {
		start := time.Now()
		for i := 0; i < 48; i++ {
			gate(string(rune('a'+i)), kind)
		}
		return time.Since(start)
	}
	cold := project()
	warm := project()
	t.Logf("48 card verdicts with a stream writer: cold %s, warm %s", cold, warm)
	if live.Load() != 48 {
		t.Fatalf("the warm page ran %d gates; want none", live.Load()-48)
	}
	if warm >= 100*time.Millisecond {
		t.Fatalf("a warm page of verdicts took %s", warm)
	}

	changes := map[string]func(){
		"calendar permission switched": func() {
			if err := a.store.SetUIState(t.Context(), permissionUIStateKey(calendarPermissionKey), "false"); err != nil {
				t.Fatal(err)
			}
		},
		"calendar event rewritten": func() {
			row := storage.CalendarEventRow{ID: "verdict-event", Title: "Planning", StartAt: time.Now().UnixMilli(), EndAt: time.Now().Add(time.Hour).UnixMilli()}
			if err := a.store.UpsertCalendarEvents(t.Context(), []storage.CalendarEventRow{row}, time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
		},
		"privacy withdrawal": func() { briefPrivacyChanged() },
		"source event":       func() { noteMobileBriefReadCachesChanged() },
		"permission event":   func() { noteProactiveReadEvent("permissions:changed") },
		"memory write":       func() { a.invalidateMemoryExternalReads() },
	}
	for name, change := range changes {
		before := live.Load()
		change()
		gate("a", kind)
		if live.Load() != before+1 {
			t.Fatalf("%s did not drop the verdict", name)
		}
	}

	// A capture card keys on the live capture state.
	previous := proactiveVoiceCaptureSnapshots
	t.Cleanup(func() { proactiveVoiceCaptureSnapshots = previous })
	running := false
	proactiveVoiceCaptureSnapshots = func(*App) []captureSnapshot {
		return []captureSnapshot{{Kind: "screen", Granted: true, Enabled: true, Running: running}}
	}
	capture := string(engagement.KindCapturePaused)
	gate("capture", capture)
	before := live.Load()
	gate("capture", capture)
	if live.Load() != before {
		t.Fatal("an unchanged capture state did not reuse its verdict")
	}
	running = true
	gate("capture", capture)
	if live.Load() != before+1 {
		t.Fatal("capture recovery did not drop the verdict")
	}

	// A verdict reached under a cancelled context is not kept.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	key := proactiveReadVerdictKey{id: "cancelled", kind: kind, presentation: "p"}
	a.proactiveReadVerdict(ctx, key, func() bool { return false })
	before = live.Load()
	a.proactiveReadVerdict(t.Context(), key, func() bool { live.Add(1); return true })
	if live.Load() != before+1 {
		t.Fatal("a verdict from a cancelled read was kept")
	}
}
