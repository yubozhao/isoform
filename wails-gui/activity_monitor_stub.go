//go:build !darwin && !windows

package main

// startActivityMonitorIfPermitted is a no-op on non-darwin/non-windows platforms.
func (a *App) startActivityMonitorIfPermitted() {}

func (a *App) startActivityMonitorIfPermittedWith(_, _ bool) {}

func activityMonitorOSGranted() bool { return false }
