package privacy

import (
	"testing"
	"time"
)

func TestPauseStateZeroIsNotPaused(t *testing.T) {
	var s PauseState
	if got := s.Snapshot(); got.Enabled || !got.ResumeAt.IsZero() {
		t.Fatalf("zero snapshot=%+v, want not paused", got)
	}
}

func TestPauseStateTimedPauseKeepsDeadline(t *testing.T) {
	var s PauseState
	deadline := time.Date(2026, time.November, 15, 13, 0, 0, 0, time.UTC)
	s.Publish(true, deadline)
	if got := s.Snapshot(); !got.Enabled || !got.ResumeAt.Equal(deadline) {
		t.Fatalf("snapshot=%+v, want paused until %v", got, deadline)
	}
}

func TestPauseStateResumeClearsDeadline(t *testing.T) {
	var s PauseState
	s.Publish(true, time.Now().Add(time.Hour))
	s.Publish(false, time.Now().Add(time.Hour))
	if got := s.Snapshot(); got.Enabled || !got.ResumeAt.IsZero() {
		t.Fatalf("snapshot=%+v, want resumed with no deadline", got)
	}
}

func TestPauseStateStoreIsManual(t *testing.T) {
	var s PauseState
	s.Publish(true, time.Now().Add(time.Hour))
	s.Store(true)
	if got := s.Snapshot(); !got.Enabled || !got.ResumeAt.IsZero() || !s.Load() {
		t.Fatalf("snapshot=%+v, want a manual pause with no deadline", got)
	}
}
