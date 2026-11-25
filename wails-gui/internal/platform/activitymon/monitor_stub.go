//go:build !darwin && !windows

package activitymon

import "context"

type stubMonitor struct{}

// FrontmostAppBundleID returns empty string on non-darwin platforms.
func FrontmostAppBundleID() string { return "" }

// FrontmostAppPID returns 0 on non-darwin/windows platforms.
func FrontmostAppPID() int { return 0 }

// FrontmostAppName returns empty string on non-darwin/windows platforms.
func FrontmostAppName() string { return "" }

// FrontmostAppInfo returns empty AppInfo on non-darwin platforms.
func FrontmostAppInfo() AppInfo { return AppInfo{} }

// FrontmostWindowBounds returns zero bounds on non-darwin platforms.
func FrontmostWindowBounds() WindowBounds { return WindowBounds{} }

// FindVisibleSensitiveWindows returns nil on non-darwin platforms.
func FindVisibleSensitiveWindows(_ []string) []WindowBounds { return nil }

// New creates a no-op Monitor on non-darwin platforms.
func New(allowed func() bool) Monitor {
	return &stubMonitor{}
}

func (m *stubMonitor) Start(_ context.Context, _ func(ActivityEvent)) error {
	return nil
}

func (m *stubMonitor) Stop() {}

func (m *stubMonitor) IsRunning() bool {
	return false
}

// CaptureScreen is a no-op on non-darwin platforms.
func CaptureScreen(_, _ int) ([]byte, int, int, error) {
	return nil, 0, 0, nil
}

// GetStats returns zero counters on non-darwin platforms (no C ring
// buffer to instrument). Preserves the cross-platform call site in the
// perf snapshot.
func GetStats() Stats { return Stats{} }
