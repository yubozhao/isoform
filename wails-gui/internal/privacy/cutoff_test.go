package privacy

import (
	"testing"
	"time"
)

func TestCutoffRefusesBuildsThatBeganBeforeIt(t *testing.T) {
	dir := t.TempDir()
	c := NewCutoff(dir)
	before := time.Now().Add(-time.Minute)
	if !c.Allows(before) {
		t.Fatal("with no cutoff every build may save")
	}
	at := time.Now()
	if err := c.Set(at); err != nil {
		t.Fatal(err)
	}
	if c.Allows(before) || c.CapturedAllowed(before) {
		t.Fatal("a build that began before the cutoff must not save")
	}
	if !c.Allows(at.Add(time.Second)) {
		t.Fatal("a build after the cutoff saves")
	}
	if err := c.Set(before); err != nil || !c.At().Equal(time.UnixMilli(at.UnixMilli())) && !c.At().Equal(at) {
		t.Fatalf("the cutoff must never move back: %v %v", c.At(), err)
	}
	// A restart reads it back.
	if got := NewCutoff(dir).At(); got.UnixMilli() != at.UnixMilli() {
		t.Fatalf("after restart = %v, want %v", got, at)
	}
}
