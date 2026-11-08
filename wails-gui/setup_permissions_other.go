//go:build !darwin

package main

func setupNativePermissions() PlatformPermissions { return getPlatformPermissions() }

func setupPermissionAskable(string) bool { return true }

func setupNotificationPermission() *bool {
	return setupNotificationGranted(getPlatformPermissions())
}
