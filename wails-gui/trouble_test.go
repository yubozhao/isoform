package main

import (
	"testing"
	"time"
)

func TestTroubleScreenOffOnlyWhenWantedButNotGranted(t *testing.T) {
	for _, tc := range []struct {
		items []PermissionItem
		want  bool
	}{
		{nil, false},
		{[]PermissionItem{{Key: "screen_recording", Toggleable: true, Enabled: true, Granted: true}}, false},
		{[]PermissionItem{{Key: "screen_recording", Toggleable: true, Enabled: false, Granted: false}}, false},
		{[]PermissionItem{{Key: "screen_recording", Toggleable: true, Enabled: true, Granted: false}}, true},
		{[]PermissionItem{{Key: "microphone", Toggleable: true, Enabled: true, Granted: false}}, false},
	} {
		if got := troubleScreenOff(tc.items); got != tc.want {
			t.Errorf("%+v: screen off = %v, want %v", tc.items, got, tc.want)
		}
	}
}

func TestTroubleSinceHoldsUntilFixed(t *testing.T) {
	var s troubleSince
	first := time.Date(2026, 11, 15, 14, 10, 0, 0, time.UTC)
	if got := s.at(troubleScreen, true, first); !got.Equal(first) {
		t.Fatalf("first seen = %v", got)
	}
	if got := s.at(troubleScreen, true, first.Add(time.Hour)); !got.Equal(first) {
		t.Fatalf("still wrong keeps the first time, got %v", got)
	}
	if got := s.at(troubleScreen, false, first.Add(2*time.Hour)); !got.IsZero() {
		t.Fatalf("fixed = %v", got)
	}
	later := first.Add(3 * time.Hour)
	if got := s.at(troubleScreen, true, later); !got.Equal(later) {
		t.Fatalf("wrong again starts over, got %v", got)
	}
}

func TestTroublePhoneSilentSince(t *testing.T) {
	now := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC)
	recent := now.Add(-10 * time.Minute).Format(time.RFC3339)
	old := now.Add(-2 * time.Hour).Format(time.RFC3339)
	if !troublePhoneSilentSince(nil, now).IsZero() {
		t.Fatal("no phone paired is not trouble")
	}
	if !troublePhoneSilentSince([]MobileDeviceView{{LastSeenAt: old}, {LastSeenAt: recent}}, now).IsZero() {
		t.Fatal("one phone checked in recently")
	}
	if got := troublePhoneSilentSince([]MobileDeviceView{{LastSeenAt: old}}, now); got.Format(time.RFC3339) != old {
		t.Fatalf("silent since = %v, want %s", got, old)
	}
}

func TestGetTroubleOrdersByCostAndSkipsClosedEyesScreen(t *testing.T) {
	app := &App{}
	got := app.GetTrouble().Items
	for i := 1; i < len(got); i++ {
		if troubleOrder[got[i-1].Kind] > troubleOrder[got[i].Kind] {
			t.Fatalf("out of order: %+v", got)
		}
	}
}

func TestMenuBarTroubleRowSaysTheFirstWithItsOneFix(t *testing.T) {
	w := menuBarWords("en")
	since := time.Date(2026, 11, 15, 14, 10, 0, 0, time.Local).UTC().Format(time.RFC3339)
	if menuBarTroubleRow(w, "en", nil) != nil {
		t.Fatal("no trouble, no row")
	}
	row := menuBarTroubleRow(w, "en", []TroubleLine{{Kind: troubleScreen, Since: since}, {Kind: troublePhone, Since: since}})
	if row == nil || row.Line != "Isoform hasn't seen your screen since 2:10 PM. Screen Recording is off for Isoform." || row.Command != "openScreenSettings" || row.SignIn != "Open System Settings" {
		t.Fatalf("screen row = %+v", row)
	}
	phone := menuBarTroubleRow(w, "en", []TroubleLine{{Kind: troublePhone, Since: since}})
	if phone == nil || phone.Command != "" || phone.Line != "Your phone hasn't checked in since 2:10 PM. Notifications show here until it does." {
		t.Fatalf("phone row = %+v (no button: nothing to press)", phone)
	}
	signed := menuBarTroubleRow(w, "en", []TroubleLine{{Kind: troubleSignedOut, Since: since, Assistant: "claude-code"}})
	if signed == nil || signed.Command != "signIn:claude-code" {
		t.Fatalf("signed-out row = %+v", signed)
	}
}
