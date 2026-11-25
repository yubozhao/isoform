package main

import (
	"strings"
	"time"

	"wails-gui/internal/engagement"
)

// Read notifications aloud (computer-conformance answers v1.5.0 S22): "Says
// each notification out loud on this computer. Anyone nearby hears it." It is
// off until turned on (GetAutoSpeakNotificationsEnabled), and Closed Eyes and quiet hours
// silence it too, whatever lets the notification itself through. Every path
// that shows a notification on this computer calls speakNotification after
// the toast: the engagement kinds (engagementDesktop), NotifyUser and the
// automatic knowledge and memory notices. The frontend says the title with
// the on-device voice (useNotificationSpeech); nothing leaves the computer.

// notificationSpeechEvent carries the title to say.
const notificationSpeechEvent = "notification:speak"

// notificationSpeechAllowed is the whole rule: on, eyes open, and not quiet
// hours. Closed Eyes silences it like quiet hours do.
func notificationSpeechAllowed(on, closedEyes bool, quiet engagement.QuietWindow, now time.Time) bool {
	return on && !closedEyes && !quiet.Contains(now)
}

// speakNotification asks the frontend to say a notification's title, when
// the rule allows it now.
func (a *App) speakNotification(title string) {
	title = strings.TrimSpace(title)
	if a == nil || title == "" {
		return
	}
	var quiet engagement.QuietWindow
	if svc := a.engagementService(); svc != nil {
		quiet = svc.Prefs().Quiet
	}
	if !notificationSpeechAllowed(a.GetAutoSpeakNotificationsEnabled(), a.IsClosedEyesModeEnabled(), quiet, time.Now()) {
		return
	}
	a.emitBrowserEvent(notificationSpeechEvent, map[string]any{"title": title})
}
