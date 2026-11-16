//go:build !darwin

package main

// The interface zoom is macOS-only for now (WKWebView pageZoom); elsewhere
// the value is still saved so it applies once the platform supports it.
func setMainWebViewZoom(float64) bool { return false }
