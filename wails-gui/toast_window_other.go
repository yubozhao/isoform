//go:build !darwin

package main

import (
	"log"

	"wails-gui/internal/platform/notify"
)

// Non-darwin fallback: the custom in-screen toast is macOS-only (requires
// NSWindow + WKWebView).  On Windows/Linux we delegate to the standard
// OS notification pipeline so the user still sees something.

func setToastClickHandler(fn func(clickData string)) {
	notify.SetClickHandler(fn)
}

func prewarmDesktopToast() { /* nothing to prewarm */ }

func showDesktopToast(title, body, iconPath, clickData string) {
	showDesktopToastWithOptions(title, body, iconPath, clickData, toastOptions{})
}

// showDesktopToastWithOptions delivers through the OS notifier, which has no
// action button of ours; the toast counts as shown once it was handed over.
func showDesktopToastWithOptions(title, body, iconPath, clickData string, options toastOptions) {
	opts := &notify.Options{IconPath: iconPath, ClickData: clickData}
	if err := notify.Send(title, body, opts); err != nil {
		log.Printf("[toast] fallback notify.Send failed: %v", err)
		return
	}
	if options.OnShown != nil {
		go options.OnShown()
	}
}

// closeDesktopToastsTagged has nothing to take away: the system shows these.
func closeDesktopToastsTagged([]string) {}
