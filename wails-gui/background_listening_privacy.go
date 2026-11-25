package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"wails-gui/internal/platform/activitymon"
)

// backgroundListeningPrivacyPollInterval is the screen check's cadence where the
// platform announces nothing (not macOS).
const backgroundListeningPrivacyPollInterval = time.Second

// backgroundListeningBackstopPollInterval is the check on macOS when no event
// came: a missed announcement costs at most this long, never forever.
const backgroundListeningBackstopPollInterval = 30 * time.Second

const (
	backgroundAudioSuspendedDesktopLocked = "desktop_locked"
	backgroundAudioSuspendedPrivacyPause  = "privacy_pause"
	backgroundAudioSuspendedDisabled      = "disabled"
	backgroundAudioSuspendedNotRunning    = "not_running"
	// backgroundAudioSuspendedMicrophoneOff is listening on with the
	// microphone switched off and no system audio on this Mac.
	backgroundAudioSuspendedMicrophoneOff = "microphone_paused"
)

type backgroundListeningLockAction uint8

const (
	backgroundListeningLockNoAction backgroundListeningLockAction = iota
	backgroundListeningLockSuspend
	backgroundListeningLockResume
)

type backgroundListeningLockState struct {
	initialized bool
	locked      bool
}

func (state *backgroundListeningLockState) observe(locked bool) backgroundListeningLockAction {
	if !state.initialized {
		state.initialized = true
		state.locked = locked
		if locked {
			return backgroundListeningLockSuspend
		}
		return backgroundListeningLockNoAction
	}
	if state.locked == locked {
		return backgroundListeningLockNoAction
	}
	state.locked = locked
	if locked {
		return backgroundListeningLockSuspend
	}
	return backgroundListeningLockResume
}

func (a *App) startBackgroundListeningPrivacyMonitor(ctx context.Context) {
	if a == nil || ctx == nil {
		return
	}
	a.mu.Lock()
	if a.audioScreenLocked == nil {
		a.audioScreenLocked = backgroundListeningDesktopProtected
	}
	a.mu.Unlock()
	go a.runBackgroundListeningPrivacyMonitor(ctx)
}

// subscribeScreenState is the screen-state ping source; tests replace it.
var subscribeScreenState = activitymon.SubscribeScreenState

// The monitor looks at two things only: the screen lock, and whether the
// frontmost app is the login window (both read by backgroundListeningDesktopProtected).
// Sensitive apps and private windows are not read here; they are handled where
// the activity monitor and the transcription filter read them.
//
// The check runs when the OS announces a lock, unlock, sleep, wake or app
// activation (source=event). On macOS a backstop poll (source=poll) catches an
// announcement that was missed. Every transition is logged with its source, so
// a poll-caused transition shows that an event was missed.
func (a *App) runBackgroundListeningPrivacyMonitor(ctx context.Context) {
	activitymon.StartSystemStateObservers()
	changes, stopChanges := subscribeScreenState()
	defer stopChanges()
	interval := backgroundListeningPrivacyPollInterval
	if activitymon.SystemStateEventsSupported {
		interval = backgroundListeningBackstopPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	state := backgroundListeningLockState{}
	reconcile := func(source string) {
		locked := a.isBackgroundListeningScreenLocked()
		action := state.observe(locked)
		if !locked {
			a.reconcileBackgroundAudioConfig()
		}
		switch action {
		case backgroundListeningLockSuspend:
			log.Printf("[listening-privacy] paused reason=lock source=%s", source)
			a.suspendBackgroundListeningForScreenLock()
		case backgroundListeningLockResume:
			log.Printf("[listening-privacy] resumed reason=lock source=%s", source)
			a.resumeBackgroundListeningAfterScreenUnlock()
		}
	}

	reconcile("start")
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			reconcile("event")
		case <-ticker.C:
			reconcile("poll")
		}
	}
}

// Use the existing privacy events and backstop to release active devices
// during a config outage, then retry a skipped start after repair.
func (a *App) reconcileBackgroundAudioConfig() {
	a.backgroundAudioMu.Lock()
	defer a.backgroundAudioMu.Unlock()
	_, known := readAudioConfig().listening()
	if !known {
		a.audioConfigRetryPending = true
		if a.isBackgroundListeningRunningLocked() {
			a.stopBackgroundListeningWithDrainLocked(false)
		}
		return
	}
	if a.audioConfigRetryPending {
		a.audioConfigRetryPending = false
		a.startBackgroundListeningIfReadyLocked()
	}
}

func (a *App) isBackgroundListeningScreenLocked() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	detector := a.audioScreenLocked
	a.mu.Unlock()
	return detector != nil && detector()
}

func backgroundListeningDesktopProtected() bool {
	if activitymon.IsScreenLocked() {
		return true
	}
	return backgroundListeningAppIsProtected(activitymon.FrontmostAppInfo())
}

func backgroundListeningAppIsProtected(frontmost activitymon.AppInfo) bool {
	appName := strings.ToLower(strings.TrimSpace(frontmost.AppName))
	bundleID := strings.ToLower(strings.TrimSpace(frontmost.BundleID))
	return appName == "loginwindow" ||
		bundleID == "com.apple.loginwindow" ||
		strings.HasSuffix(bundleID, ".loginwindow")
}

func (a *App) suspendBackgroundListeningForScreenLock() {
	if a == nil {
		return
	}
	a.stopBackgroundListeningWithDrain(false)
	recordAudioCaptureEvent(AudioCaptureEvent{
		Kind:    "suspended",
		Outcome: "success",
		Message: "Paused audio because the desktop is locked.",
	})
	log.Printf("[background-listening] suspended at source while desktop is locked")
}

func (a *App) resumeBackgroundListeningAfterScreenUnlock() {
	if a == nil || a.IsClosedEyesModeEnabled() || !a.GetBackgroundListening() {
		return
	}
	recordAudioCaptureEvent(AudioCaptureEvent{
		Kind:    "resume_attempt",
		Message: "Desktop unlocked; checking whether background audio may resume.",
	})
	if err := a.ensureWhisperServerRunning(); err != nil {
		recordAudioCaptureEvent(AudioCaptureEvent{
			Kind:    "error",
			Outcome: "failed",
			Message: fmt.Sprintf("Background audio could not resume after unlock: %v", err),
		})
		log.Printf("[background-listening] unlock resume failed: %v", err)
		return
	}
	a.startBackgroundListeningIfReady()
	if a.isBackgroundListeningRunning() {
		recordAudioCaptureEvent(AudioCaptureEvent{
			Kind:    "resumed",
			Outcome: "success",
			Message: "Native audio capture resumed after the desktop was unlocked.",
		})
	}
}

func (a *App) backgroundAudioSuspendedReason(locked, running bool) string {
	switch {
	case locked:
		return backgroundAudioSuspendedDesktopLocked
	case a != nil && a.IsClosedEyesModeEnabled():
		return backgroundAudioSuspendedPrivacyPause
	case a != nil && !a.GetBackgroundListening():
		return backgroundAudioSuspendedDisabled
	case !running && readAudioConfig().source() == backgroundListeningSourceNone:
		return backgroundAudioSuspendedMicrophoneOff
	case !running:
		return backgroundAudioSuspendedNotRunning
	default:
		return ""
	}
}
