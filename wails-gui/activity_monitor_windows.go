//go:build windows

package main

import "log"

func (a *App) startActivityMonitorIfPermittedWith(_, _ bool) {
	a.startActivityMonitorIfPermitted()
}

// startActivityMonitorIfPermitted starts activity monitoring on Windows.
// Unlike macOS, no special permissions are needed — low-level hooks and
// BitBlt screen capture work for standard-privilege processes.
// However, the user may disable screen capture via the Permissions toggle.
func (a *App) startActivityMonitorIfPermitted() {
	// Always initialize so the store is available for reading historical sessions.
	if a.activity == nil {
		a.initActivityMonitor()
	}

	if err := a.startActivityMonitor(); err != nil {
		// Non-fatal: log and continue without activity monitoring
		log.Printf("[activity] failed to start: %v", err)
	}
}

func activityMonitorOSGranted() bool { return true }
