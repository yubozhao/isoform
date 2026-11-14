//go:build darwin

package main

import (
	"runtime"
	"testing"
)

// Brief synthesis runs at utility QoS and leaves the thread as it found it,
// so chat read-aloud on a reused thread keeps its speed.
func TestRunAtUtilityQoSLowersOnlyTheWrappedWork(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	before := currentThreadQoSClass()
	var inside uint32
	runAtUtilityQoS(func() { inside = currentThreadQoSClass() })
	if inside != utilityQoSClass() {
		t.Fatalf("wrapped work ran at QoS %#x, want utility %#x", inside, utilityQoSClass())
	}
	if after := currentThreadQoSClass(); after != before {
		t.Fatalf("thread QoS after = %#x, want it restored to %#x", after, before)
	}
}
