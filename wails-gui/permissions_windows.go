//go:build windows

package main

import "fmt"

func getPlatformPermissions() PlatformPermissions {
	return PlatformPermissions{
		Platform:  "windows",
		Supported: true,
		Items: []PermissionItem{
			{
				Key:         "screen_capture",
				Label:       "Screen Capture",
				Description: "Uses the Graphics Capture API. Can be disabled to save resources and protect privacy.",
				Granted:     true,
				Required:    false,
				Actionable:  false,
				ActionLabel: "",
				Toggleable:  true,
				Enabled:     true, // default; overridden by stored state in GetPlatformPermissions
			},
			{
				Key:         "input_automation",
				Label:       "Keyboard & Mouse Automation",
				Description: "Uses the SendInput API for keyboard and mouse control. Can be disabled to protect privacy.",
				Granted:     true,
				Required:    false,
				Actionable:  false,
				ActionLabel: "",
				Toggleable:  true,
				Enabled:     true, // default; overridden by stored state in GetPlatformPermissions
			},
			{
				Key:         "microphone",
				Label:       "Microphone",
				Description: "Uses the WASAPI microphone capture API to listen and create memories. Can be disabled to protect privacy.",
				Granted:     true,
				Required:    false,
				Actionable:  false,
				ActionLabel: "",
				Toggleable:  true,
				Enabled:     true, // default; overridden by background-listening state in GetPlatformPermissions
			},
			{
				Key:         "contacts",
				Label:       "Contacts",
				Description: "Import a vCard (.vcf) file from the Who you know page to add contacts from this computer.",
				Granted:     false,
				Required:    false,
				Actionable:  false,
				ActionLabel: "",
			},
		},
	}
}

func requestPermission(key string) bool {
	return false
}

func openPermissionSettings(key string) error {
	return fmt.Errorf("no permission settings to open on Windows for key %q", key)
}

func openPermissionSettingsFromRect(key string, _ float64, _ float64, _ float64, _ float64) error {
	return openPermissionSettings(key)
}

func setSystemAudioPermissionGranted(_ bool) {}

// requestMicrophoneBeforeListening is a no-op here: only macOS shows a
// microphone dialog that must not overlap the system audio prompt.
func requestMicrophoneBeforeListening() {}
