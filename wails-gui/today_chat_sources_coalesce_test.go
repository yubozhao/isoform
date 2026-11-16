package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/privacy"
)

// Callers that arrive while one collection runs share its result: the
// collection runs once, and every caller gets the same sources (R24 part 3).
func TestTodayChatSourcesCoalesceConcurrentCallers(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	const callers = 8
	var collections atomic.Int64
	var entered atomic.Int64
	app.todayChatCollectionObserver = func() {
		collections.Add(1)
		// Hold the first collection until every caller has started, so the
		// others find it in flight instead of starting their own.
		deadline := time.Now().Add(5 * time.Second)
		for entered.Load() < callers && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
	}
	now := time.Now()
	var wg sync.WaitGroup
	results := make([]todayChatSources, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entered.Add(1)
			results[i] = app.collectTodayChatSources(context.Background(), now)
		}(i)
	}
	wg.Wait()
	if got := collections.Load(); got > 2 {
		t.Fatalf("%d concurrent callers ran %d collections, want at most 2", callers, got)
	}
	for i := 1; i < callers; i++ {
		if len(results[i].Memories) != len(results[0].Memories) || len(results[i].People) != len(results[0].People) {
			t.Fatalf("caller %d saw different sources than caller 0", i)
		}
	}
}

// While a refresh runs under a stamp, a caller under the same stamp gets the
// last result without starting a collection. A privacy change moves the stamp,
// so a caller after it never receives the result remembered under the old one.
func TestTodayChatSourcesStampChangeInvalidatesAtOnce(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	previous := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(previous) })
	pii.SetUserSensitiveApps(nil)
	var collections atomic.Int64
	release := make(chan struct{})
	app.todayChatCollectionObserver = func() {
		if collections.Add(1) == 2 {
			// The second collection stays in flight until released.
			<-release
		}
	}
	now := time.Now()
	ctx := context.Background()
	app.collectTodayChatSources(ctx, now) // remembers a result under stamp S1
	if n := collections.Load(); n != 1 {
		t.Fatalf("first read collected %d times, want 1", n)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.collectTodayChatSources(ctx, now) // refresh under S1, held open
	}()
	for collections.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	// Same stamp while the refresh runs: the last result, no new collection.
	app.collectTodayChatSources(ctx, now)
	if n := collections.Load(); n != 2 {
		t.Fatalf("a caller under the same stamp started a collection while one ran: %d", n)
	}
	// A withdrawal moves the stamp at once: the next caller must not be served
	// the result remembered under S1, so it collects again while S1 is held.
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Session, ID: "stamp-test-session"})
	app.collectTodayChatSources(ctx, now)
	if n := collections.Load(); n != 3 {
		t.Fatalf("a withdrawal did not start a fresh collection: %d", n)
	}
	// A sensitive-app change is part of the same stamp.
	pii.SetUserSensitiveApps(map[string]string{"com.example.secret": "work"})
	app.collectTodayChatSources(ctx, now)
	if n := collections.Load(); n != 4 {
		t.Fatalf("a sensitive-app change did not start a fresh collection: %d", n)
	}
	close(release)
	<-done
}

// Each caller owns its copy: writing to one result does not change another
// caller's result or the remembered one.
func TestTodayChatSourcesCallersGetIndependentCopies(t *testing.T) {
	app := newPrivacyReadTestApp(t)
	now := time.Now()
	first := app.collectTodayChatSources(context.Background(), now)
	first.Memories = append(first.Memories, todayChatRecord{Kind: "memory", ID: "injected"})
	if len(first.Memories) > 0 {
		first.Memories[0].Text = "changed by caller"
	}
	second := app.collectTodayChatSources(context.Background(), now)
	for _, record := range second.Memories {
		if record.ID == "injected" || record.Text == "changed by caller" {
			t.Fatalf("a caller's edit reached a later caller: %+v", record)
		}
	}
}
