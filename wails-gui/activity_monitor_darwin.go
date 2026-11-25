//go:build darwin

package main

import "log"

// startActivityMonitorIfPermitted checks macOS permissions and starts
// activity monitoring if both Accessibility and Screen Recording are granted.
// The ActivityStore is always initialized so historical sessions remain
// queryable even when monitoring permissions are not granted.
// startActivityMonitorIfPermittedWith uses pre-fetched permission results to
// avoid redundant CGo calls when the caller already checked them.
func (a *App) startActivityMonitorIfPermittedWith(accessibilityGranted, screenCaptureGranted bool) {
	if a.activity == nil {
		a.initActivityMonitor()
	}

	if !accessibilityGranted || !screenCaptureGranted {
		log.Printf(
			"[activity] not started: accessibility=%t screenCapture=%t",
			accessibilityGranted,
			screenCaptureGranted,
		)
		updateMenuBarStatus(2) // no permission
		return
	}

	if err := a.startActivityMonitor(); err != nil {
		log.Printf("[activity] failed to start: %v", err)
		updateMenuBarStatus(3) // stopped
	}
}

func (a *App) startActivityMonitorIfPermitted() {
	a.startActivityMonitorIfPermittedWith(accessibilityAccess(), preflightScreenCaptureAccess())
}

func activityMonitorOSGranted() bool { return accessibilityAccess() && preflightScreenCaptureAccess() }
