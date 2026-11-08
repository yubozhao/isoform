//go:build windows

package main

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
	"wails-gui/internal/platform/cmdutil"
)

func getPlatformPermissions() PlatformPermissions {
	return PlatformPermissions{
		Platform:  "windows",
		Supported: true,
		Items: []PermissionItem{
			{
				Key:         "screen_capture",
				Label:       "Screen Capture",
				Description: "Uses Windows desktop capture. Can be disabled to save resources and protect privacy.",
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
				Granted:     windowsMicrophoneAllowed(),
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
			{
				Key: "notifications", Label: "Notifications",
				Description: "Managed in Windows notification settings.",
				Granted:     windowsNotificationsAllowed(), Actionable: true,
				ActionLabel: "Open Settings",
			},
		},
	}
}

func requestPermission(key string) bool {
	switch key {
	case "screen_capture", "screen_recording", "input_automation", "accessibility", "system_audio":
		return true
	case "microphone":
		if windowsMicrophoneAllowed() {
			return true
		}
		_ = openPermissionSettings(key)
	case "notifications":
		if windowsNotificationsAllowed() {
			return true
		}
		_ = openPermissionSettings(key)
	}
	return false
}

func openPermissionSettings(key string) error {
	var uri string
	switch key {
	case "microphone":
		uri = "ms-settings:privacy-microphone"
	case "notifications":
		uri = "ms-settings:notifications"
	case "system_audio":
		uri = "ms-settings:sound"
	default:
		return fmt.Errorf("no permission settings to open on Windows for key %q", key)
	}
	return cmdutil.Command("rundll32.exe", "url.dll,FileProtocolHandler", uri).Start()
}

func windowsMicrophoneAllowed() bool {
	const consent = `SOFTWARE\Microsoft\Windows\CurrentVersion\CapabilityAccessManager\ConsentStore\microphone`
	for _, location := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, consent}, {registry.CURRENT_USER, consent},
		{registry.CURRENT_USER, consent + `\NonPackaged`},
	} {
		key, err := registry.OpenKey(location.root, location.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, err := key.GetStringValue("Value")
		key.Close()
		if err == nil && strings.EqualFold(value, "Deny") {
			return false
		}
	}
	return true
}

func windowsNotificationsAllowed() bool {
	for _, location := range []struct{ path, value string }{
		{`SOFTWARE\Microsoft\Windows\CurrentVersion\PushNotifications`, "ToastEnabled"},
		{`SOFTWARE\Microsoft\Windows\CurrentVersion\Notifications\Settings\Yansu.Desktop`, "Enabled"},
	} {
		key, err := registry.OpenKey(registry.CURRENT_USER, location.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, err := key.GetIntegerValue(location.value)
		key.Close()
		if err == nil && value == 0 {
			return false
		}
	}
	return true
}

func openPermissionSettingsFromRect(key string, _ float64, _ float64, _ float64, _ float64) error {
	return openPermissionSettings(key)
}

func setSystemAudioPermissionGranted(_ bool) {}

// requestMicrophoneBeforeListening is a no-op here: only macOS shows a
// microphone dialog that must not overlap the system audio prompt.
func requestMicrophoneBeforeListening() {}
