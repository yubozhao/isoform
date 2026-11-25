package main

import "testing"

// The global key tap carries Closed Eyes' ⌘⌃⌥E, so Chat being off must not
// stop it: it starts either way and matches only Closed Eyes' key, with the
// Quick chat key and its recorder off.
func TestClosedEyesKeyTapStartsWithChatOff(t *testing.T) {
	app := &App{}
	for _, tc := range []struct {
		name    string
		set     func(*testing.T)
		wantOff bool
	}{
		{"chat off", chatOff, true},
		{"chat on", chatOn, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.set(t)
			if err := app.StartQuickChatHotkey(); isChatOff(err) {
				t.Fatalf("StartQuickChatHotkey = %v: the Closed Eyes key tap was refused", err)
			}
			if off := tapQuickChatHotkey().Key == quickChatHotkeyKeyOff; off != tc.wantOff {
				t.Fatalf("tap's Quick chat key off = %t, want %t", off, tc.wantOff)
			}
			app.SetQuickChatHotkeyRecording(true)
			defer app.SetQuickChatHotkeyRecording(false)
			if recording := quickChatHotkeyRecording.Load(); recording == tc.wantOff {
				t.Fatalf("recorder on = %t with %s", recording, tc.name)
			}
		})
	}
}
