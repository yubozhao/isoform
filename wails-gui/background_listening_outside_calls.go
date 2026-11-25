package main

import (
	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/platform/activitymon"
)

// Listening outside calls. The microphone is turned into text all day, on
// this computer, by the live speech engine: it becomes ordinary activity
// (speech events), never a call, a recording, meeting notes or a Knowledge
// note. Dictation into another app is kept and labeled with that app. The
// computer's own sound outside calls (videos, podcasts) is left out unless
// its own switch is on. No audio is kept outside calls: only a call's
// chunks reach a recording (writeMeetingAudioChunk). Every gate still
// applies: Listen and the microphone switch (no capture at all), pause,
// the locked screen, protected apps and private browser windows.

// listenOutsideCalls reports whether a chunk captured outside any call may
// be turned into text.
func (cfg audioCaptureConfig) listenOutsideCalls(source backgroundListeningSource) bool {
	switch source {
	case backgroundListeningSourceMicrophone:
		return true
	case backgroundListeningSourceSystem:
		on, _ := cfg.outsideCalls()
		return on
	default:
		return false
	}
}

// GetSystemAudioOutsideCallsEnabled reports the switch that turns the
// computer's own sound into text outside calls. Off unless the user turns
// it on.
func (a *App) GetSystemAudioOutsideCallsEnabled() bool {
	if a == nil {
		return false
	}
	on, _ := readAudioConfig().outsideCalls()
	return on
}

// SetSystemAudioOutsideCallsEnabled persists the switch.
func (a *App) SetSystemAudioOutsideCallsEnabled(enabled bool) error {
	return updateConfig(func(cfg *yansuConfig) {
		cfg.BackgroundSystemAudioOutsideCalls = enabled
	})
}

// speechDictationTarget names the app the user was dictating into: a
// voice-typing helper in front, or another app holding the microphone
// (Claude, Codex, ChatGPT voice). A meeting app on the microphone is never
// dictation. Empty when the user was simply speaking.
func speechDictationTarget(front activitymon.AppInfo, holders micInputSnapshot, otherMicUser bool) string {
	if isVoiceInputApp(front.BundleID) {
		return dictationAppName(front.AppName, front.BundleID)
	}
	if !otherMicUser && !holders.Active {
		return ""
	}
	var other []string
	for _, id := range holders.BundleIDs {
		if meetingdetect.IsKnownMeetingBundle(id) {
			continue
		}
		if bundlesMatch(id, front.BundleID) {
			return dictationAppName(front.AppName, front.BundleID)
		}
		other = append(other, id)
	}
	if len(other) > 0 {
		return dictationAppName("", other[0])
	}
	if len(holders.BundleIDs) == 0 && !meetingdetect.IsKnownMeetingBundle(front.BundleID) {
		// The holders cannot be named: the app in front took the words.
		return dictationAppName(front.AppName, front.BundleID)
	}
	return ""
}

func dictationAppName(name, bundleID string) string {
	if name != "" {
		return name
	}
	if pretty := bundleIDPrettyName(bundleID); pretty != "" {
		return pretty
	}
	return bundleID
}

// browserMayBeInACall reports whether the browser in front, holding the
// microphone, may be in a call rather than taking dictation: it was on a
// call page, or meeting recording is off, so its calls are never told
// apart. Its words are then not labeled as dictation.
func (a *App) browserMayBeInACall(ctx speechCaptureContext) bool {
	if !meetingdetect.IsBrowserBundle(ctx.App.BundleID) || !ctx.MicHolders.couldHold(ctx.App.BundleID) {
		return false
	}
	return ctx.BrowserMeeting != "" || !a.meetingRecordingOn()
}
