package main

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestPassiveMessagesPermissionBoundsAndCoalescesStalledOSRead(t *testing.T) {
	var probe passiveTextAutomationProbe
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	got := probe.read(20*time.Millisecond, func() textAutomationState {
		calls.Add(1)
		close(entered)
		<-release
		return textAutomationAllowed
	})
	if got != textAutomationUnknown {
		t.Fatal("a stalled OS read invented a permission decision")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native probe was not scheduled")
	}
	started := time.Now()
	got = probe.read(time.Second, func() textAutomationState {
		calls.Add(1)
		return textAutomationAllowed
	})
	if got != textAutomationUnknown || calls.Load() != 1 || time.Since(started) >= time.Second/2 {
		t.Fatal("a pending native read blocked or spawned another OS call")
	}
	probe.mu.Lock()
	done := probe.pending.done
	probe.mu.Unlock()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("native read did not finish after release")
	}
	probe.mu.Lock()
	retained := probe.pending != nil
	probe.mu.Unlock()
	if retained {
		t.Fatal("completed permission answer was retained as a cache")
	}
	if got := probe.read(time.Second, func() textAutomationState { return textAutomationNotAllowed }); got != textAutomationNotAllowed {
		t.Fatal("a late grant replaced the next live denial")
	}
}

func TestPassiveMessagesPermissionPreservesEveryLiveAnswer(t *testing.T) {
	for _, want := range []textAutomationState{textAutomationUnknown, textAutomationAllowed, textAutomationNotAllowed} {
		var probe passiveTextAutomationProbe
		if got := probe.read(time.Second, func() textAutomationState { return want }); got != want {
			t.Fatalf("live answer changed: got=%v want=%v", got, want)
		}
	}
}
