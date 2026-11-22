package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The sidebar's observing row, whose words this once matched, left with
// design S3/A5 (#1388). The menu bar keeps its own state words; this guards
// them in both languages.
func TestObservingMenuStatesSayTheState(t *testing.T) {
	until := time.Date(2026, 10, 22, 15, 15, 0, 0, time.Local)
	want := map[string][4]string{
		"en":    {"Observing in background", "Not observing", "Observing paused", "Observing paused until 3:15 PM"},
		"zh-CN": {"后台观察中", "未在观察", "观察已暂停", "观察已暂停至 15:15"},
	}
	for lang, words := range want {
		cases := []struct {
			view   DoingNow
			active bool
		}{
			{DoingNow{Kind: doingNowWatching}, true},
			{DoingNow{Kind: doingNowWorking}, false},
			{DoingNow{Kind: doingNowPaused}, true},
			{DoingNow{Kind: doingNowPaused, PausedUntil: until.UnixMilli()}, true},
		}
		for i, tc := range cases {
			if got := observingMenuText(tc.view, tc.active, lang); got != words[i] {
				t.Errorf("%s case %d: got %q, want %q", lang, i, got, words[i])
			}
		}
	}
}

func TestObservingMenuPauseUsesOneHourAndResumeClearsDeadline(t *testing.T) {
	app := newTimedPauseFixture(t)
	before := time.Now()
	app.toggleMenuBarObservation()
	deadline, err := time.Parse(time.RFC3339Nano, pauseDeadline(t, app))
	if err != nil || !app.IsClosedEyesModeEnabled() {
		t.Fatalf("menu pause did not persist: %v", err)
	}
	if deadline.Before(before.Add(time.Hour)) || deadline.After(time.Now().Add(time.Hour)) {
		t.Fatalf("deadline %v is not one hour from the menu activation", deadline)
	}
	app.toggleMenuBarObservation()
	if app.IsClosedEyesModeEnabled() || pauseDeadline(t, app) != "" {
		t.Fatal("menu resume left the pause active")
	}
}

// Calling the shared helper with an explicit language also guards its portable
// API: common code must never reach into a platform-only menuBarApp variable.
func TestObservingMenuActionNamesDurationAndLanguage(t *testing.T) {
	for _, tc := range []struct{ lang, pause, resume string }{
		{"en", "Pause observing for 1 hour", "Resume observing"},
		{"zh-CN", "暂停观察 1 小时", "恢复观察"},
	} {
		if got := menuBarObservationTitle(false, tc.lang); got != tc.pause {
			t.Fatalf("%s pause = %q", tc.lang, got)
		}
		if got := menuBarObservationTitle(true, tc.lang); got != tc.resume {
			t.Fatalf("%s resume = %q", tc.lang, got)
		}
	}
	if menuBarLanguage(nil) != "en" {
		t.Fatal("uninitialized menus must use English")
	}
}

func TestIndefiniteObservationToggleKeepsClosedEyesUntilResumed(t *testing.T) {
	app := newTimedPauseFixture(t)
	app.toggleClosedEyesModeFromHotkey()
	if !app.IsClosedEyesModeEnabled() || pauseDeadline(t, app) != "" {
		t.Fatal("indefinite toggle must pause without an automatic resume deadline")
	}
	app.toggleClosedEyesModeFromHotkey()
	if app.IsClosedEyesModeEnabled() {
		t.Fatal("indefinite toggle must resume on the next activation")
	}
}

func TestWindowsObservingTimedMenuDoesNotAdvertiseTheGlobalShortcut(t *testing.T) {
	windows, err := os.ReadFile("menubar_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(windows), "closedEyesHotkeySuffix") || strings.Contains(string(windows), "Ctrl+Alt+Shift+E") {
		t.Fatal("the Windows timed menu must not advertise the indefinite global toggle")
	}
}

func TestObservingMenuClockMatchesTheMergedClockFormat(t *testing.T) {
	when := time.Date(2026, 10, 22, 9, 0, 0, 0, time.Local)
	if got := observationMenuClock(when, "en"); got != "9:00 AM" {
		t.Fatal(got)
	}
	if got := observationMenuClock(when, "zh-CN"); got != "9:00" {
		t.Fatal(got)
	}
}
