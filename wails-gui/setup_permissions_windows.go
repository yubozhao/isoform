//go:build windows

package main

func setupNativePermissions() PlatformPermissions {
	view := getPlatformPermissions()
	for i := range view.Items {
		switch view.Items[i].Key {
		case "screen_capture":
			view.Items[i].Key = "screen_recording"
		case "input_automation":
			view.Items[i].Key = "accessibility"
		}
	}
	view.Items = append(view.Items, PermissionItem{Key: "system_audio", Granted: true})
	return view
}

func setupPermissionAskable(key string) bool {
	switch key {
	case "microphone", "system_audio", "accessibility", "screen_recording":
		return true
	}
	return false
}

func setupNotificationPermission() *bool {
	return setupNotificationGranted(getPlatformPermissions())
}
