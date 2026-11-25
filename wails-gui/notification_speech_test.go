package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
)

// Read notifications aloud is off until turned on, and quiet hours silence it
// (computer-conformance answers v1.5.0 S22).
func TestNotificationSpeechAllowed(t *testing.T) {
	night := engagement.QuietWindow{Start: 22 * time.Hour, End: 8 * time.Hour}
	at := func(hour int) time.Time { return time.Date(2026, 11, 24, hour, 0, 0, 0, time.Local) }
	cases := []struct {
		name       string
		on         bool
		closedEyes bool
		quiet      engagement.QuietWindow
		now        time.Time
		want       bool
	}{
		{"off by default", false, false, engagement.QuietWindow{}, at(12), false},
		{"on, no quiet hours", true, false, engagement.QuietWindow{}, at(23), true},
		{"on, outside quiet hours", true, false, night, at(12), true},
		{"on, inside quiet hours after start", true, false, night, at(23), false},
		{"on, inside quiet hours past midnight", true, false, night, at(7), false},
		{"on, quiet hours just ended", true, false, night, at(8), true},
		{"off, outside quiet hours", false, false, night, at(12), false},
		{"on, Closed Eyes", true, true, engagement.QuietWindow{}, at(12), false},
		{"on, Closed Eyes outside quiet hours", true, true, night, at(12), false},
	}
	for _, c := range cases {
		if got := notificationSpeechAllowed(c.on, c.closedEyes, c.quiet, c.now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Every path that shows a notification on this computer asks to say it, so
// "each notification" stays true when a path is added or moved.
func TestEveryNotificationPathSpeaks(t *testing.T) {
	for file, after := range map[string]string{
		"engagement_app.go":                 "showDesktopToastWithOptions(title, body, a.notifyIconPath(), engagementClickData(m)",
		"app.go":                            "showDesktopToast(title, body, a.notifyIconPath(), clickData)\n",
		"engagement_notification_budget.go": "svc.deliverAutomaticDesktop(func() { showDesktopToast(title, body, a.notifyIconPath(), clickData) })",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		i := strings.Index(src, after)
		if i < 0 {
			t.Fatalf("%s: toast call not found", file)
		}
		rest := src[i:]
		if end := strings.Index(rest, "\n}\n"); end < 0 || !strings.Contains(rest[:end], "a.speakNotification(title)") {
			t.Errorf("%s: the toast's function doesn't call speakNotification", file)
		}
	}
}
