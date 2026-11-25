package main

import (
	"log"
	"time"
)

// GetAutomaticMeetingRecordingEnabled reports whether Yansu may automatically
// detect supported calls and save them as meeting records. This is the window's
// binding; Go code reads meetingRecordingOn.
func (a *App) GetAutomaticMeetingRecordingEnabled() (perfValue bool) {
	defer logPageBinding("GetAutomaticMeetingRecordingEnabled", time.Now(), &perfValue)
	return a.meetingRecordingOn()
}

const (
	meetingRecordingUnknown uint32 = iota // A zero-value App must read config before recording.
	meetingRecordingOff
	meetingRecordingEnabled
)

func (a *App) loadMeetingRecordingPreference() {
	a.meetingRecordingState.Store(meetingRecordingUnknown)
	a.meetingRecordingOn()
}

// A failed startup read stays pending. The next gate evaluation can observe
// a repaired config. A simultaneous settings change wins over a pending read.
func (a *App) meetingRecordingOn() bool {
	on, _ := a.meetingRecordingPreference()
	return on
}

func (a *App) meetingRecordingPreference() (on, known bool) {
	if a == nil {
		return true, true
	}
	state := a.meetingRecordingState.Load()
	if state == meetingRecordingUnknown {
		on, known := readAudioConfig().meeting()
		if !known {
			return false, false
		}
		resolved := meetingRecordingOff
		if on {
			resolved = meetingRecordingEnabled
		}
		a.meetingRecordingState.CompareAndSwap(meetingRecordingUnknown, resolved)
		state = a.meetingRecordingState.Load()
	}
	return state == meetingRecordingEnabled, true
}

// automaticMeetingRecordingEpoch snapshots the current preference generation.
// Audio chunks and speech events carry this value so work captured before an
// off/on cycle can never become a meeting after recording is re-enabled.
func (a *App) automaticMeetingRecordingEpoch() (uint64, bool) {
	if a == nil {
		return 0, true
	}
	for {
		epoch := a.meetingRecordingEpoch.Load()
		enabled := a.meetingRecordingOn()
		if epoch == a.meetingRecordingEpoch.Load() {
			return epoch, enabled
		}
	}
}

func (a *App) automaticMeetingRecordingEpochIsCurrent(epoch uint64) bool {
	if a == nil {
		return epoch == 0
	}
	return a.meetingRecordingOn() && a.meetingRecordingEpoch.Load() == epoch
}

// SetAutomaticMeetingRecordingEnabled persists and hot-applies the user's
// meeting-recording preference. Disabling aborts any in-progress automatic
// meeting audio and clears transient meeting classification state without
// stopping the independent background-listening recorder.
func (a *App) SetAutomaticMeetingRecordingEnabled(enabled bool) error {
	a.meetingRecordingMu.Lock()

	if err := updateConfig(func(cfg *yansuConfig) {
		cfg.AutomaticMeetingRecordingDisabled = !enabled
	}); err != nil {
		a.meetingRecordingMu.Unlock()
		return err
	}

	state := a.meetingRecordingState.Load()
	if state != meetingRecordingUnknown && (state == meetingRecordingEnabled) == enabled {
		a.meetingRecordingMu.Unlock()
		return nil
	}
	if enabled {
		// Publish the new generation before opening the gate. A reader can
		// briefly reject new work, but can never accept old work as current.
		a.meetingRecordingEpoch.Add(1)
		a.meetingRecordingState.Store(meetingRecordingEnabled)
	} else {
		// Close the gate before invalidating the generation. Durable meeting
		// writes take meetingRecordingMu, so none can outlive this setter.
		a.meetingRecordingState.Store(meetingRecordingOff)
		a.meetingRecordingEpoch.Add(1)
	}
	if !enabled {
		a.abortAutomaticMeetingRecording()
	}
	a.meetingRecordingMu.Unlock()

	a.emitBrowserEvent("automatic-meeting-recording:changed", enabled)
	log.Printf("[meeting-recording] automatic detection and recording enabled=%v", enabled)
	return nil
}

// abortAutomaticMeetingRecording makes an opt-out take effect immediately.
// Buffered activity remains available to the ordinary activity pipeline, but
// meeting-only audio files and classification state are discarded.
func (a *App) abortAutomaticMeetingRecording() {
	if a == nil || a.activity == nil {
		return
	}

	am := a.activity
	am.mu.Lock()
	meetingWriter := am.meetingAudioWriter
	candidateWriter := am.meetingCandidateAudioWriter
	am.meetingAudioWriter = nil
	am.meetingCandidateAudioWriter = nil

	am.inMeeting = false
	am.meetingAppName = ""
	am.meetingBundleID = ""
	am.meetingStartMs = 0
	am.meetingRecordingEpoch = 0
	am.meetingID = ""
	am.lastMeetingID = ""
	am.lastMeetingApp = ""
	am.lastMeetingPID = 0
	am.lastMeetingPaused = false
	am.lastMeetingEndMs = 0
	am.lastCallIdentityWrittenMs = 0
	am.lastMeetingEventMs = 0
	am.lastMeetingAudioMs = 0
	am.meetingCandidateActive = false
	am.meetingCandidateAppName = ""
	am.meetingCandidateStartMs = 0
	am.meetingCandidateRecordingEpoch = 0
	am.lastMeetingCandidateAudioMs = 0
	am.firstSpeechTime = 0

	if am.enabled && am.buf != nil {
		a.armActivityIdleTimerLocked(am, "idle_transition")
	}
	am.mu.Unlock()
	forgetCallIdentity(am)

	if meetingWriter != nil {
		meetingWriter.Abort()
	}
	if candidateWriter != nil && candidateWriter != meetingWriter {
		candidateWriter.Abort()
	}
}
