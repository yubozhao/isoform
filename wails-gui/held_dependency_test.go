package main

import (
	"testing"
	"time"
)

// heldDependencyHang bounds a call that must not wait on a dependency the test
// holds open until after the call. Waiting would hang until the test releases
// it, so this is a hang guard, not a speed budget: a loaded machine can take
// hundreds of milliseconds and still be correct.
const heldDependencyHang = 5 * time.Second

// finishesWhileHeld runs f and fails the test if it has not returned within
// heldDependencyHang, the sign that it waited on the held dependency.
func finishesWhileHeld(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(heldDependencyHang):
		t.Fatalf("%s waited on a dependency the test holds open", what)
	}
}
