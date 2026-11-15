package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// closedEyesModeChangedEvent tells the frontend that observation was paused
// or resumed, whichever surface did it (menu bar, tray, hotkey, overlay or
// Settings), so the Settings switch stays in sync. The payload is the new
// paused state.
const closedEyesModeChangedEvent = "closed-eyes:changed"

const (
	// closedEyesResumeAtKey is the ui_state key holding a timed pause's
	// auto-resume deadline (RFC 3339, UTC). It survives a restart; an empty
	// value means no timed pause.
	closedEyesResumeAtKey = "closed_eyes_resume_at"
	// maxObservationPauseMinutes bounds a timed pause at one day.
	maxObservationPauseMinutes       = 24 * 60
	observationDeadlineCheckInterval = time.Second
)

// observationPauseSnapshot publishes the flag and deadline as one immutable value.
type observationPauseSnapshot struct {
	enabled  bool
	resumeAt time.Time
}

type observationPauseAtomic struct {
	state atomic.Pointer[observationPauseSnapshot]
}

func (s *observationPauseAtomic) snapshot() observationPauseSnapshot {
	if state := s.state.Load(); state != nil {
		return *state
	}
	return observationPauseSnapshot{}
}

func (s *observationPauseAtomic) Load() bool { return s.snapshot().enabled }

// Store retains the boolean control's manual-pause semantics.
func (s *observationPauseAtomic) Store(enabled bool) { s.publish(enabled, time.Time{}) }

func (s *observationPauseAtomic) publish(enabled bool, deadline time.Time) {
	if !enabled {
		deadline = time.Time{}
	}
	s.state.Store(&observationPauseSnapshot{enabled: enabled, resumeAt: deadline})
}

type closedEyesModeState struct {
	enabled         observationPauseAtomic
	policyPending   atomic.Bool
	mu              sync.Mutex
	activityStartMu sync.Mutex
	resumeActivity  bool
	resumeListening bool
	// resumeAt and resumeTimer are the timed pause ("Pause for an hour"):
	// resumeAt is the persisted auto-resume deadline and resumeTimer fires it.
	// A manual pause leaves both zero and ends only by a manual resume.
	resumeAt    time.Time
	resumeTimer *time.Timer
	generation  uint64
	stopping    bool
	// now is an optional wall clock for deterministic sleep/wake fixtures.
	now func() time.Time
}

// IsClosedEyesModeEnabled reports whether privacy mode is active.
func (a *App) IsClosedEyesModeEnabled() bool {
	if a == nil {
		return false
	}
	return a.observationPauseSnapshot().enabled
}

// Unknown persisted policy is a read hold, separate from a manual pause.
// Keep status and delivery reads atomic even while restoration owns mu.
func (a *App) observationPauseSnapshot() observationPauseSnapshot {
	if a.closedEyes.policyPending.Load() {
		return observationPauseSnapshot{enabled: true}
	}
	state := a.closedEyes.enabled.snapshot()
	if a.closedEyes.policyPending.Load() {
		return observationPauseSnapshot{enabled: true}
	}
	return state
}

// SetClosedEyesModeEnabled pauses or resumes observation. Exposed as a Wails
// binding for the Settings > Observation "Pause observing" switch.
func (a *App) SetClosedEyesModeEnabled(enabled bool) error {
	_, err := a.setClosedEyesModeEnabled(enabled)
	return err
}

// ToggleClosedEyesMode flips privacy mode and returns the new state.
func (a *App) ToggleClosedEyesMode() bool {
	enabled, err := a.toggleClosedEyesMode()
	if err != nil {
		log.Printf("[closed-eyes] toggle failed: %v", err)
		return a.IsClosedEyesModeEnabled()
	}
	return enabled
}

func (a *App) toggleClosedEyesMode() (bool, error) {
	return a.setClosedEyesModeEnabled(!a.IsClosedEyesModeEnabled())
}

func (a *App) setClosedEyesModeEnabled(enabled bool) (bool, error) {
	return a.setClosedEyesModeTimed(enabled, 0)
}

// setClosedEyesModeTimed routes timed and manual controls through one transition.
func (a *App) setClosedEyesModeTimed(enabled bool, resumeAfter time.Duration) (bool, error) {
	var deadline time.Time
	if enabled && resumeAfter > 0 {
		deadline = a.observationWallNow().Add(resumeAfter)
	}
	return a.setObservationPause(enabled, deadline, 0, false)
}

// setObservationPause holds ownership through persistence and the transition.
// expectedGeneration is nonzero only for timer callbacks; detached callbacks
// cannot resume a newer pause or start monitors during shutdown.
func (a *App) setObservationPause(enabled bool, deadline time.Time, expectedGeneration uint64, restoring bool) (bool, error) {
	if a == nil {
		return false, nil
	}
	a.clearPageSnapshots()
	changed := false
	withdrawnAt := time.Time{}
	chatTransitionAt := time.Time{}
	defer func() {
		if !chatTransitionAt.IsZero() {
			a.syncMainChatObservationPause(enabled, chatTransitionAt)
		}
		if changed {
			a.emitBrowserEvent(closedEyesModeChangedEvent, enabled)
		}
		if !withdrawnAt.IsZero() {
			scope := privacyWithdrawalScope{Kind: privacyWithdrawalTimeRange, StartMs: withdrawnAt.UnixMilli()}
			if !deadline.IsZero() {
				scope.EndMs = deadline.UnixMilli()
			}
			publishPrivacyWithdrawal(scope)
		}
	}()
	a.closedEyes.mu.Lock()
	defer a.closedEyes.mu.Unlock()
	if expectedGeneration != 0 && (a.closedEyes.generation != expectedGeneration || a.closedEyes.resumeTimer == nil) {
		return a.closedEyes.enabled.Load(), nil
	}
	if a.closedEyes.stopping {
		return a.closedEyes.enabled.Load(), errors.New("observation_stopping")
	}
	if enabled && !deadline.IsZero() && a.closedEyes.enabled.Load() && a.closedEyes.resumeAt.IsZero() {
		return true, errors.New("observation_manual_pause_active")
	}
	if expectedGeneration != 0 && a.observationWallNowLocked().Before(a.closedEyes.resumeAt) {
		a.armObservationDeadlineCheckLocked()
		return a.closedEyes.enabled.Load(), nil
	}
	deadlineChanged := !deadline.Equal(a.closedEyes.resumeAt)
	var persistenceErr error
	// Manual controls also clear a malformed or expired value from a prior
	// launch, even when no in-memory timer could be restored.
	if deadlineChanged || (deadline.IsZero() && a.store != nil) {
		if err := a.persistObservationResumeAt(deadline); err != nil {
			if !enabled || !deadline.IsZero() {
				return a.closedEyes.enabled.Load(), err
			}
			// A broken store must never prevent a manual privacy pause. Report
			// the durable-clear failure, but detach its callback and pause now.
			persistenceErr = err
		}
	}
	changed = deadlineChanged
	a.detachObservationResumeLocked()
	a.closedEyes.resumeAt = deadline
	if !deadline.IsZero() {
		a.armObservationDeadlineCheckLocked()
	}

	if enabled && !restoring && (!a.closedEyes.enabled.Load() || deadlineChanged) {
		withdrawnAt = a.observationWallNowLocked()
	}
	if a.closedEyes.enabled.Load() == enabled {
		a.closedEyes.enabled.publish(enabled, deadline)
		return enabled, persistenceErr
	}
	changed = true
	// Nothing copied while paused is ever noted, however short the pause.
	defer a.clipboardPauseChanged()
	// Readers that catch up later (the browser history) leave this time out.
	changedAt := a.observationWallNowLocked()
	a.recordObservationPause(enabled, changedAt)
	if !restoring {
		a.recordTodayObservationPauseStart(enabled, changedAt)
		chatTransitionAt = changedAt
	}

	if enabled {
		a.closedEyes.resumeActivity = a.isActivityMonitorRunning()
		a.closedEyes.resumeListening = a.isBackgroundListeningRunning()
		if restoring {
			a.closedEyes.resumeActivity = true
			a.closedEyes.resumeListening = true
		}
		a.closedEyes.enabled.publish(true, deadline)
		if a.closedEyes.resumeListening {
			a.stopBackgroundListeningForClosedEyes()
		}
		if a.closedEyes.resumeActivity {
			a.stopActivityMonitorForClosedEyes()
		}
		showClosedEyesOverlay(a)
		updateMenuBarClosedEyesMode(true)
		log.Printf("[closed-eyes] enabled (resume_activity=%t resume_background_listening=%t)",
			a.closedEyes.resumeActivity, a.closedEyes.resumeListening)
		return true, persistenceErr
	}

	resumeActivity := a.closedEyes.resumeActivity
	resumeBackgroundListening := a.closedEyes.resumeListening
	a.closedEyes.resumeActivity = false
	a.closedEyes.resumeListening = false
	// Before the flag clears, so a clipboard tick between the two never
	// takes a copy made in the pause's last second for a new one; the
	// deferred call covers a copy made while resuming.
	a.clipboardPauseChanged()
	a.closedEyes.enabled.publish(false, time.Time{})
	hideClosedEyesOverlay()
	updateMenuBarClosedEyesMode(false)

	if resumeActivity {
		a.startObservationActivityMonitor()
	}
	if resumeBackgroundListening {
		a.resumeBackgroundListeningForClosedEyes()
	}
	a.transcribeCallsKeptAtPause()
	log.Printf("[closed-eyes] disabled (resumed_activity=%t resumed_background_listening=%t)",
		resumeActivity, resumeBackgroundListening)
	return false, nil
}

func (a *App) toggleClosedEyesModeFromHotkey() {
	enabled, err := a.toggleClosedEyesMode()
	if err != nil {
		log.Printf("[closed-eyes] hotkey toggle failed: %v", err)
		return
	}
	log.Printf("[closed-eyes] hotkey toggled mode to %t", enabled)
}

func (a *App) isActivityMonitorRunning() bool {
	if a == nil || a.activity == nil || a.activity.monitor == nil {
		return false
	}
	return a.activity.monitor.IsRunning()
}

func (a *App) isBackgroundListeningRunning() bool {
	if a == nil {
		return false
	}
	a.backgroundAudioMu.Lock()
	defer a.backgroundAudioMu.Unlock()
	a.mu.Lock()
	capture := a.backgroundAudio
	a.mu.Unlock()
	return capture != nil && capture.isRunning()
}

// PauseObservationFor pauses observation for 1-1440 minutes. The persisted
// UTC deadline survives restart; manual controls cancel it. Changes emit
// closed-eyes:changed with the existing boolean payload. A manual pause is
// preserved with observation_manual_pause_active instead of arming a timer.
func (a *App) PauseObservationFor(minutes int) error {
	if minutes < 1 || minutes > maxObservationPauseMinutes {
		return errors.New("observation_pause_minutes_out_of_range")
	}
	if a == nil || a.store == nil {
		return errors.New("observation_store_unavailable")
	}
	_, err := a.setClosedEyesModeTimed(true, time.Duration(minutes)*time.Minute)
	return err
}

// ResumeObservation resumes observation and clears any persisted timer.
func (a *App) ResumeObservation() error {
	_, err := a.setClosedEyesModeEnabled(false)
	return err
}

// detachObservationResumeLocked invalidates callbacks, including ones already
// waiting for the mutex. The caller owns closedEyes.mu.
func (a *App) detachObservationResumeLocked() {
	a.closedEyes.generation++
	if a.closedEyes.resumeTimer != nil {
		a.closedEyes.resumeTimer.Stop()
		a.closedEyes.resumeTimer = nil
	}
}

func (a *App) persistObservationResumeAt(at time.Time) error {
	if a.store == nil {
		return errors.New("observation_store_unavailable")
	}
	value := ""
	if !at.IsZero() {
		value = at.UTC().Format(time.RFC3339Nano)
	}
	return a.store.SetUIState(context.Background(), closedEyesResumeAtKey, value)
}

func (a *App) observationResumeAt() (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	state := a.observationPauseSnapshot()
	return state.resumeAt, !state.resumeAt.IsZero()
}

// restoreObservationPolicy validates the durable pause before storage is
// published. It performs no native UI, timer or capture work. A deadline that
// expires while paint/environment discovery is parked stays paused until the
// service phase can safely reconcile it.
func (a *App) restoreObservationPolicy() {
	if a == nil {
		return
	}
	a.closedEyes.mu.Lock()
	defer a.closedEyes.mu.Unlock()
	if a.closedEyes.stopping {
		return
	}
	a.closedEyes.policyPending.Store(true)
	defer a.closedEyes.policyPending.Store(false)
	manualPause := a.closedEyes.enabled.Load() && a.closedEyes.resumeAt.IsZero()
	a.detachObservationResumeLocked()
	a.closedEyes.resumeAt = time.Time{}
	a.closedEyes.resumeActivity, a.closedEyes.resumeListening = false, false
	paused := true
	var deadline time.Time
	if a.store != nil {
		raw, err := a.store.GetUIState(context.Background(), closedEyesResumeAtKey)
		switch true {
		case err != nil:
			log.Printf("[closed-eyes] unavailable resume deadline: %v", err)
		case raw == "":
			paused = manualPause
		default:
			at, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
			switch true {
			case parseErr != nil:
				log.Printf("[closed-eyes] invalid resume deadline: %v", parseErr)
			case at.After(a.observationWallNowLocked()):
				if !manualPause {
					deadline = at
				} else if err := a.persistObservationResumeAt(time.Time{}); err != nil {
					log.Printf("[closed-eyes] clear deadline superseded by manual pause: %v", err)
				}
			default:
				if err := a.persistObservationResumeAt(time.Time{}); err != nil {
					log.Printf("[closed-eyes] clear expired deadline: %v", err)
				} else {
					paused = manualPause
				}
			}
		}
	}
	if paused {
		a.clearPageSnapshots()
		a.recordObservationPause(true, time.Now())
	}
	a.closedEyes.resumeAt = deadline
	a.closedEyes.enabled.publish(paused, deadline)
}

// startObservationResumeTimer runs after native menu setup and before service
// startup. Until here no deadline callback can start capture ahead of paint.
func (a *App) startObservationResumeTimer() {
	if a == nil {
		return
	}
	a.closedEyes.mu.Lock()
	if a.closedEyes.stopping || !a.closedEyes.enabled.Load() {
		a.closedEyes.mu.Unlock()
		return
	}
	a.closedEyes.resumeActivity, a.closedEyes.resumeListening = true, true
	if !a.closedEyes.resumeAt.IsZero() {
		a.armObservationDeadlineCheckLocked()
	}
	if a.closedEyes.resumeAt.IsZero() || a.observationWallNowLocked().Before(a.closedEyes.resumeAt) {
		showClosedEyesOverlay(a)
		updateMenuBarClosedEyesMode(true)
	}
	a.closedEyes.mu.Unlock()
	// Reconcile a deadline that elapsed during deferred startup immediately.
	a.checkObservationDeadline()
}

// Retained for callers restoring a pause with native services already ready.
func (a *App) restoreObservationResumeTimer() {
	a.restoreObservationPolicy()
	a.startObservationResumeTimer()
}

// stopObservationResumeTimer fences callbacks before service teardown. The
// persisted deadline remains for the next launch.
func (a *App) stopObservationResumeTimer() {
	if a == nil {
		return
	}
	a.closedEyes.mu.Lock()
	defer a.closedEyes.mu.Unlock()
	a.closedEyes.stopping = true
	a.detachObservationResumeLocked()
}

// startObservationActivityMonitor serializes startup with a restored deadline
// expiring during startup. Both paths may initialize the activity store and
// monitor; the second must see the first path's completed initialization.
func (a *App) startObservationActivityMonitor() {
	a.closedEyes.activityStartMu.Lock()
	defer a.closedEyes.activityStartMu.Unlock()
	a.startActivityMonitorIfPermitted()
}

// observationWallNowLocked strips the monotonic reading: elapsed awake time
// must not extend a deadline across sleep. The caller owns closedEyes.mu.
func (a *App) observationWallNowLocked() time.Time {
	if a.closedEyes.now != nil {
		return a.closedEyes.now().UTC()
	}
	return time.Now().UTC()
}

func (a *App) observationWallNow() time.Time {
	if a == nil {
		return time.Now().UTC()
	}
	a.closedEyes.mu.Lock()
	defer a.closedEyes.mu.Unlock()
	return a.observationWallNowLocked()
}

// armObservationDeadlineCheckLocked polls the wall deadline at most a second
// apart, including after sleep when a duration timer's clock has stopped.
func (a *App) armObservationDeadlineCheckLocked() {
	delay := min(observationDeadlineCheckInterval, max(0, a.closedEyes.resumeAt.Sub(a.observationWallNowLocked())))
	a.scheduleObservationDeadlineCheckLocked(delay)
}

func (a *App) scheduleObservationDeadlineCheckLocked(delay time.Duration) {
	if a.closedEyes.resumeTimer != nil {
		a.closedEyes.resumeTimer.Stop()
	}
	generation := a.closedEyes.generation
	a.closedEyes.resumeTimer = time.AfterFunc(delay, func() { a.checkObservationDeadlineGeneration(generation) })
}

func (a *App) checkObservationDeadlineGeneration(generation uint64) {
	if _, err := a.setObservationPause(false, time.Time{}, generation, false); err != nil {
		log.Printf("[closed-eyes] timed resume: %v", err)
		a.closedEyes.mu.Lock()
		defer a.closedEyes.mu.Unlock()
		// Persistence may recover on the next check. Never retry a manual
		// control, replaced pause or shutdown that now owns this state.
		if !a.closedEyes.stopping && a.closedEyes.generation == generation && a.closedEyes.resumeTimer != nil {
			a.scheduleObservationDeadlineCheckLocked(observationDeadlineCheckInterval)
		}
	}
}

// checkObservationDeadline is also called by the existing native wake path;
// the periodic check handles platforms or full event queues that miss a wake.
func (a *App) checkObservationDeadline() {
	a.closedEyes.mu.Lock()
	generation := a.closedEyes.generation
	active := a.closedEyes.resumeTimer != nil && !a.closedEyes.stopping
	a.closedEyes.mu.Unlock()
	if active {
		a.checkObservationDeadlineGeneration(generation)
	}
}
