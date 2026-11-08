//go:build windows

package main

import "testing"

func TestWindowsSetupRepairsLegacyMacPermissionDenials(t *testing.T) {
	p := SetupPermissions{Answers: []SetupPermissionAnswer{}}
	for _, key := range setupPermissionOrder {
		p.Answers = append(p.Answers, SetupPermissionAnswer{Key: key, State: "denied"})
	}
	p = setupPermissionsView(p, setupNativePermissions())
	states := map[string]string{}
	for _, a := range p.Answers {
		states[a.Key] = a.State
	}
	for _, key := range []string{"screen_recording", "accessibility", "system_audio"} {
		if states[key] != "on" {
			t.Errorf("Windows capability %s = %s", key, states[key])
		}
	}
	for _, key := range []string{calendarPermissionKey, remindersPermissionKey, "contacts", mailPermissionKey} {
		if states[key] != "unsupported" || setupPermissionAskable(key) {
			t.Errorf("unsupported integration %s = %s", key, states[key])
		}
	}
	if p.Platform != "windows" || setupNotificationPermission() == nil {
		t.Fatal("missing Windows setup metadata")
	}
}
