// Package notify provides cross-platform desktop notifications.
// Platform-specific implementations live in build-tagged files.
package notify

import "sync"

var (
	clickMu      sync.Mutex
	clickHandler ClickHandler
)

// ClickHandler is called when the user clicks a delivered desktop notification.
// The clickData argument is the same string that was passed in Options.ClickData.
type ClickHandler func(clickData string)

// Options configures optional notification behaviour.
type Options struct {
	// IconPath is the absolute path to a .png or .ico file shown beside the
	// notification. Leave empty to use the OS default icon.
	IconPath string

	// ClickData is an opaque string attached to the notification and returned
	// via the registered ClickHandler when the user clicks the notification.
	// Leave empty if no click handling is needed.
	ClickData string
}
