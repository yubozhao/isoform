package main

import (
	"fmt"
	"log"
	"os"
	"time"
)

// GetBackgroundListening returns whether background speech capture is enabled.
// Exposed as a Wails binding for the frontend settings UI.
func (a *App) GetBackgroundListening() (perfValue bool) {
	defer logPageBinding("GetBackgroundListening", time.Now(), &perfValue)
	on, _ := readAudioConfig().listening()
	return on
}

func (a *App) IsSystemAudioBackgroundListeningSupported() bool {
	return isBackgroundListeningSourceSupported(backgroundListeningSourceSystem)
}

// SetBackgroundListening enables or disables background speech capture.
// Exposed as a Wails binding for the frontend settings UI.
//
// When enabling, the speech engine is initialized BEFORE persisting the
// config so the setting doesn't get stuck in an inconsistent "enabled but
// not loaded" state if the engine fails to load.
func (a *App) SetBackgroundListening(enabled bool) error {
	if enabled && a.IsClosedEyesModeEnabled() {
		return fmt.Errorf("cannot enable background listening while observation is paused")
	}
	if enabled {
		if _, known := readAudioConfig().listening(); !known {
			return fmt.Errorf("cannot enable background listening: config unreadable")
		}
		if err := a.ensureWhisperServerRunning(); err != nil {
			return fmt.Errorf("cannot enable background listening: %v", err)
		}
	}

	if err := updateConfig(func(cfg *yansuConfig) {
		cfg.BackgroundListeningConfigured = true
		cfg.BackgroundListeningDisabled = !enabled
	}); err != nil {
		return err
	}

	if enabled {
		// The user asked for listening, so the OS may prompt now: the
		// microphone first and on its own, then the recorders (the system
		// audio tap prompts when it is created). Starts that happen on their
		// own (launch, unlock) never prompt.
		runPermissionRequest(func() {
			// A microphone the user switched off is not asked for.
			if backgroundMicrophoneEnabled() {
				requestMicrophoneBeforeListening()
			}
			a.startBackgroundListeningIfReady()
		})
	} else {
		a.stopBackgroundListening()
	}
	a.emitBrowserEvent("background-listening:changed", enabled)
	return nil
}

// backgroundMicrophoneEnabled reads the Microphone switch; absent means on.
func backgroundMicrophoneEnabled() bool {
	on, _ := readAudioConfig().microphone()
	return on
}

// setBackgroundMicrophoneEnabled is the Microphone switch on macOS. It pauses
// or resumes the microphone source of background listening while system
// audio keeps going; a running capture restarts with the new sources. It
// never prompts: a microphone not yet allowed is asked for from its Grant
// button.
func (a *App) setBackgroundMicrophoneEnabled(enabled bool) error {
	if err := updateConfig(func(cfg *yansuConfig) {
		cfg.BackgroundMicrophonePaused = !enabled
	}); err != nil {
		return err
	}
	log.Printf("[background-listening] microphone source switched %t by user", enabled)
	if a.isBackgroundListeningRunning() || (enabled && a.GetBackgroundListening()) {
		a.stopBackgroundListening()
		a.startBackgroundListeningIfReady()
	}
	a.emitBrowserEvent("background-listening:changed", a.GetBackgroundListening())
	return nil
}

func (a *App) SetBackgroundListeningSource(source string) error {
	normalized := normalizeBackgroundListeningSource(source)
	if normalized == backgroundListeningSourceSystem && !isBackgroundListeningSourceSupported(backgroundListeningSourceBoth) {
		return fmt.Errorf("system audio background listening requires macOS 14.2 or later")
	}

	log.Printf("[background-listening] SetBackgroundListeningSource(%s) ignored; background listening now captures microphone and system audio together when available", normalized)
	return nil
}

func hasPersistedConfig() bool {
	path, err := yansuConfigPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	if err == nil {
		return true
	}
	return !os.IsNotExist(err)
}

func resolveBackgroundListeningEnabled(cfg yansuConfig, configExists bool) bool {
	if cfg.BackgroundListeningConfigured {
		return !cfg.BackgroundListeningDisabled
	}
	return !configExists
}

func ensureBackgroundListeningDefaultForFreshInstall() {
	if hasPersistedConfig() {
		return
	}
	if err := updateConfig(func(cfg *yansuConfig) {
		cfg.BackgroundListeningConfigured = true
		cfg.BackgroundListeningDisabled = false
	}); err != nil {
		log.Printf("[background-listening] failed to persist fresh-install default: %v", err)
		return
	}
	log.Printf("[background-listening] enabled by default for fresh install")
}

// startBackgroundListeningIfReady starts background audio capture if the
// speech engine is loaded and the setting is enabled.
func (a *App) startBackgroundListeningIfReady() {
	a.backgroundAudioMu.Lock()
	defer a.backgroundAudioMu.Unlock()
	a.startBackgroundListeningIfReadyLocked()
}

func (a *App) startBackgroundListeningIfReadyLocked() {
	if a.IsClosedEyesModeEnabled() {
		log.Printf("[background-listening] observation paused, skipping start")
		return
	}
	if a.isBackgroundListeningScreenLocked() {
		log.Printf("[background-listening] desktop locked, skipping recorder start")
		return
	}
	cfg := readAudioConfig()
	on, known := cfg.listening()
	a.audioConfigRetryPending = !known
	if !on {
		return
	}
	// Native capture and durable call audio do not depend on ASR readiness.
	// The scheduler resolves the current engine for each batch after a restart.
	srv := a.speechTranscriber()

	source := cfg.source()
	if source == backgroundListeningSourceNone {
		log.Printf("[background-listening] microphone switched off and no system audio on this Mac; nothing to capture")
		return
	}

	// Mark the engine as having an active background consumer so it does
	// not idle-timeout.
	if srv != nil {
		srv.SetBackgroundActive(true)
	}

	a.mu.Lock()
	if a.backgroundAudio != nil && a.backgroundAudio.isRunning() {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	log.Printf("[background-listening] creating capture")
	log.Printf("[background-listening] selected source=%s", source)

	capture := newBackgroundAudioCapture(source, srv,
		func(text string, src backgroundListeningSource, ctx speechCaptureContext) {
			a.injectSpeechEvent(text, src, ctx)
		},
		nil,
		a.shouldTranscribeBackgroundAudioChunk,
		a.annotateBackgroundAudioChunk,
		// A sleeping tap has no pre-roll. Keep it warm while automatic calls
		// are enabled so detection does not lose the remote opening words.
		a.meetingRecordingOn,
	)
	capture.onRecordingChunk = a.writeMeetingAudioChunk
	capture.sample = a.sampleCaptureState
	capture.resolveTranscriber = a.speechTranscriber
	capture.allowBatch = a.allowBackgroundAudioBatch
	if combined, ok := capture.recorder.(*combinedAudioRecorder); ok {
		combined.canStart = func(source backgroundListeningSource) bool {
			return readAudioConfig().allowsSource(source) && !a.IsClosedEyesModeEnabled() && !a.isBackgroundListeningScreenLocked()
		}
	}

	if err := capture.start(); err != nil {
		if srv != nil {
			srv.SetBackgroundActive(false)
		}
		log.Printf("[background-listening] failed to start: %v", err)
		return
	}
	// Config and lock state may change during native recorder startup.
	cfg = readAudioConfig()
	on, known = cfg.listening()
	a.audioConfigRetryPending = !known
	if !on || cfg.source() != source || a.isBackgroundListeningScreenLocked() {
		capture.stopDiscardingQueuedTranscription()
		if srv != nil {
			srv.SetBackgroundActive(false)
		}
		log.Printf("[background-listening] capture gates changed during recorder start; capture discarded")
		return
	}

	activeSources := capture.activeSources()
	if len(activeSources) > 0 {
		log.Printf("[background-listening] active sources=%v", activeSources)
	}
	for _, activeSource := range activeSources {
		if activeSource != backgroundListeningSourceSystem {
			continue
		}
		setSystemAudioPermissionGranted(true)
		break
	}

	a.mu.Lock()
	a.backgroundAudio = capture
	a.mu.Unlock()

	log.Printf("[background-listening] started")

	// Retry saved recordings when an engine is available, including calls
	// interrupted by ASR restart or a previous application shutdown.
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			a.recoverUntranscribedMeetings()
			select {
			case <-capture.stopCh:
				return
			case <-ticker.C:
			}
		}
	}()
}

// stopBackgroundListening stops background audio capture and clears the
// speech engine's "background active" flag.
func (a *App) stopBackgroundListening() {
	a.stopBackgroundListeningWithDrain(true)
}

func (a *App) stopBackgroundListeningForClosedEyes() {
	a.backgroundAudioMu.Lock()
	defer a.backgroundAudioMu.Unlock()

	a.mu.Lock()
	capture := a.backgroundAudio
	a.mu.Unlock()
	if capture == nil {
		log.Printf("[background-listening] observation paused; no active recorder to stop")
		return
	}

	a.stopBackgroundListeningWithDrainLocked(false)
	recordAudioCaptureEvent(AudioCaptureEvent{
		Kind:    "suspended",
		Outcome: "success",
		Message: "Paused audio because observation is paused.",
	})
	log.Printf("[background-listening] stopped at source while observation is paused")
}

func (a *App) resumeBackgroundListeningForClosedEyes() {
	a.backgroundAudioMu.Lock()
	defer a.backgroundAudioMu.Unlock()

	recordAudioCaptureEvent(AudioCaptureEvent{
		Kind:    "resume_attempt",
		Message: "Observation resumed; checking whether background audio may resume.",
	})
	a.startBackgroundListeningIfReadyLocked()

	a.mu.Lock()
	capture := a.backgroundAudio
	a.mu.Unlock()
	if capture != nil && capture.isRunning() {
		recordAudioCaptureEvent(AudioCaptureEvent{
			Kind:    "resumed",
			Outcome: "success",
			Message: "Native audio capture resumed after observation resumed.",
		})
		log.Printf("[background-listening] resumed after observation pause")
	}
}

func (a *App) stopBackgroundListeningWithDrain(drain bool) {
	a.backgroundAudioMu.Lock()
	defer a.backgroundAudioMu.Unlock()
	a.stopBackgroundListeningWithDrainLocked(drain)
}

func (a *App) stopBackgroundListeningWithDrainLocked(drain bool) {
	a.mu.Lock()
	capture := a.backgroundAudio
	a.backgroundAudio = nil
	a.mu.Unlock()

	if capture != nil {
		if drain {
			capture.stop()
		} else {
			capture.stopDiscardingQueuedTranscription()
		}
	}

	// Allow the server to idle-timeout now.
	if a.whisper != nil {
		a.whisper.mu.Lock()
		srv := a.whisper.server
		a.whisper.mu.Unlock()
		if srv != nil {
			srv.SetBackgroundActive(false)
		}
	}

	log.Printf("[background-listening] stopped")
}
