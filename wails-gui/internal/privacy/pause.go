package privacy

import (
	"sync/atomic"
	"time"
)

// PauseSnapshot is the Closed Eyes state as one immutable value: whether
// observation is paused and, for a timed pause, when it resumes on its own.
type PauseSnapshot struct {
	Enabled  bool
	ResumeAt time.Time
}

// PauseState publishes the Closed Eyes flag and deadline together, so a reader
// never sees one without the other. The zero value is not paused.
type PauseState struct {
	state atomic.Pointer[PauseSnapshot]
}

// Snapshot returns the current flag and deadline.
func (s *PauseState) Snapshot() PauseSnapshot {
	if state := s.state.Load(); state != nil {
		return *state
	}
	return PauseSnapshot{}
}

// Load reports whether observation is paused.
func (s *PauseState) Load() bool { return s.Snapshot().Enabled }

// Store sets a manual pause (no deadline) or a resume.
func (s *PauseState) Store(enabled bool) { s.Publish(enabled, time.Time{}) }

// Publish sets the flag and deadline together. A resume clears the deadline.
func (s *PauseState) Publish(enabled bool, deadline time.Time) {
	if !enabled {
		deadline = time.Time{}
	}
	s.state.Store(&PauseSnapshot{Enabled: enabled, ResumeAt: deadline})
}
