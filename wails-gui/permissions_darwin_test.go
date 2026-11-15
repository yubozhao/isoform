//go:build darwin

package main

import "testing"

func TestGetPlatformPermissions_ReportsDarwin(t *testing.T) {
	got := getPlatformPermissions()

	if got.Platform != "darwin" {
		t.Fatalf("got Platform=%q, want %q", got.Platform, "darwin")
	}
	if !got.Supported {
		t.Fatalf("got Supported=%v, want true", got.Supported)
	}
}

func TestGetPlatformPermissions_HasRequiredItems(t *testing.T) {
	got := getPlatformPermissions()

	if len(got.Items) < 4 {
		t.Fatalf("got %d items, want at least 4", len(got.Items))
	}

	keys := map[string]bool{}
	for _, item := range got.Items {
		keys[item.Key] = true
		switch item.Key {
		case "screen_recording", "accessibility":
			if !item.Required {
				t.Fatalf("item %q should be required", item.Key)
			}
		case "microphone", "notifications", "automation_messages", "automation_mail", "contacts", "calendars", "reminders", "messages_history":
			if item.Required {
				t.Fatalf("item %q should be optional", item.Key)
			}
		case "clipboard", "files":
			// A switch with no macOS permission behind it: never asked for.
			if item.Required || !item.Granted || !item.Toggleable || item.Actionable {
				t.Fatalf("%s item %+v", item.Key, item)
			}
			continue
		case "system_audio":
			if item.Required {
				t.Fatalf("item %q should be optional", item.Key)
			}
			if !darwinSystemAudioCaptureSupported() {
				t.Fatalf("item %q should not be present when system audio capture is unsupported", item.Key)
			}
		default:
			t.Fatalf("unexpected permission item %q", item.Key)
		}
		if !item.Actionable {
			t.Fatalf("item %q should be actionable on macOS", item.Key)
		}
	}

	if !keys["screen_recording"] {
		t.Fatal("missing screen_recording permission item")
	}
	if !keys["accessibility"] {
		t.Fatal("missing accessibility permission item")
	}
	if !keys["microphone"] {
		t.Fatal("missing microphone permission item")
	}
	if !keys["notifications"] {
		t.Fatal("missing notifications permission item")
	}
	if !keys["automation_messages"] {
		t.Fatal("missing automation_messages permission item")
	}
	if !keys["contacts"] {
		t.Fatal("missing contacts permission item")
	}
	if darwinSystemAudioCaptureSupported() && !keys["system_audio"] {
		t.Fatal("missing system_audio permission item")
	}
}

func TestGetPlatformPermissions_MatchesNativeValues(t *testing.T) {
	got := getPlatformPermissions()
	wantScreen := preflightScreenCaptureAccess()
	wantAccessibility := accessibilityAccess()
	wantMicrophone := microphoneAccess()
	wantNotifications := notificationAccess()
	wantSystemAudio := systemAudioAccess()

	for _, item := range got.Items {
		switch item.Key {
		case "screen_recording":
			if item.Granted != wantScreen {
				t.Fatalf("screen_recording: got Granted=%v, want %v", item.Granted, wantScreen)
			}
		case "accessibility":
			if item.Granted != wantAccessibility {
				t.Fatalf("accessibility: got Granted=%v, want %v", item.Granted, wantAccessibility)
			}
		case "microphone":
			if item.Granted != wantMicrophone {
				t.Fatalf("microphone: got Granted=%v, want %v", item.Granted, wantMicrophone)
			}
		case "notifications":
			if item.Granted != wantNotifications {
				t.Fatalf("notifications: got Granted=%v, want %v", item.Granted, wantNotifications)
			}
		case "system_audio":
			if item.Granted != wantSystemAudio {
				t.Fatalf("system_audio: got Granted=%v, want %v", item.Granted, wantSystemAudio)
			}
		case "contacts":
			if item.Granted != contactsAccessGranted() {
				t.Fatalf("contacts: got Granted=%v, want %v", item.Granted, contactsAccessGranted())
			}
		}
	}
}

func TestApp_GetPlatformPermissions_DelegatesToNativeCall(t *testing.T) {
	a := &App{}
	got := a.GetPlatformPermissions()
	want := getPlatformPermissions()

	if got.Platform != want.Platform {
		t.Fatalf("Platform mismatch: got %q, want %q", got.Platform, want.Platform)
	}
	if got.Supported != want.Supported {
		t.Fatalf("Supported mismatch: got %v, want %v", got.Supported, want.Supported)
	}
	if len(got.Items) != len(want.Items) {
		t.Fatalf("Items length mismatch: got %d, want %d", len(got.Items), len(want.Items))
	}
}

func TestApp_RequestPermission_ScreenRecording(t *testing.T) {
	a := &App{}
	if a.RequestPermission("screen_recording") {
		t.Fatal("RequestPermission(screen_recording) must not grant or prompt under go test")
	}
}

func TestApp_RequestPermission_Accessibility(t *testing.T) {
	a := &App{}
	if a.RequestPermission("accessibility") {
		t.Fatal("RequestPermission(accessibility) must not grant or prompt under go test")
	}
}

func TestNativePermissionRequestsStayBlockedUnderGoTest(t *testing.T) {
	release := allowPermissionPrompts()
	defer release()

	requests := map[string]func() bool{
		"screen_recording": requestScreenCapturePermission,
		"accessibility":    requestAccessibilityPermission,
		"microphone":       requestMicrophonePermission,
		"system_audio":     requestSystemAudioPermission,
		"notifications":    requestNotificationPermission,
		"automation_messages": func() bool {
			return textMessagesAutomation(true) == textAutomationAllowed
		},
		"mail": requestMailAutomation,
	}
	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			if request() {
				t.Fatal("native permission request must fail closed under go test")
			}
		})
	}

	authorizationRequests := map[string]func() (bool, error){
		"contacts":  requestContactsAuthorization,
		"calendars": requestCalendarAuthorization,
		"reminders": requestRemindersAuthorization,
	}
	for name, request := range authorizationRequests {
		t.Run(name, func(t *testing.T) {
			granted, err := request()
			if granted || err != errPermissionNotRequested {
				t.Fatalf("native authorization request = (%v, %v), want permission-not-requested", granted, err)
			}
		})
	}
}

func TestApp_RequestPermission_UnknownKey(t *testing.T) {
	a := &App{}
	got := a.RequestPermission("unknown_key")
	if got {
		t.Fatal("RequestPermission with unknown key should return false")
	}
}
