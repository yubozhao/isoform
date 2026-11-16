package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Interface zoom (pass-access v1.0.0 rule 6): Cmd+ and Cmd- scale the whole
// main window from 90% to 150% in 10% steps, and Cmd+0 goes back to 100%.
// It is the WebView's own page zoom (WKWebView pageZoom on macOS), not CSS
// zoom, so layout, hit testing and popover placement stay right. The value
// is saved per user in the data folder, beside the theme, and applied at
// launch before the first page paints where the window allows.
const (
	interfaceZoomMin     = 90
	interfaceZoomMax     = 150
	interfaceZoomStep    = 10
	interfaceZoomDefault = 100
	interfaceZoomFile    = "interface-zoom.txt"
)

var (
	interfaceZoomMu sync.Mutex
	interfaceZoom   = interfaceZoomDefault
)

// clampInterfaceZoom snaps a percent to the nearest step inside the range.
func clampInterfaceZoom(percent int) int {
	snapped := ((percent + interfaceZoomStep/2) / interfaceZoomStep) * interfaceZoomStep
	switch {
	case snapped < interfaceZoomMin:
		return interfaceZoomMin
	case snapped > interfaceZoomMax:
		return interfaceZoomMax
	default:
		return snapped
	}
}

func interfaceZoomPath() string {
	dir := configDirOrFallback()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, interfaceZoomFile)
}

// loadInterfaceZoomFromDisk reads the saved zoom; anything missing or
// unreadable leaves 100%.
func loadInterfaceZoomFromDisk() {
	path := interfaceZoomPath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return
	}
	interfaceZoomMu.Lock()
	interfaceZoom = clampInterfaceZoom(v)
	interfaceZoomMu.Unlock()
}

func saveInterfaceZoom(percent int) {
	path := interfaceZoomPath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(strconv.Itoa(percent)), 0o644)
}

func currentInterfaceZoom() int {
	interfaceZoomMu.Lock()
	defer interfaceZoomMu.Unlock()
	return interfaceZoom
}

// GetInterfaceZoom returns the saved zoom in percent (90 to 150).
func (a *App) GetInterfaceZoom() int {
	return currentInterfaceZoom()
}

// SetInterfaceZoom saves the zoom (snapped to the range) and applies it to
// the main window. It returns the percent now in effect.
func (a *App) SetInterfaceZoom(percent int) int {
	v := clampInterfaceZoom(percent)
	interfaceZoomMu.Lock()
	changed := v != interfaceZoom
	interfaceZoom = v
	interfaceZoomMu.Unlock()
	if changed {
		saveInterfaceZoom(v)
	}
	setMainWebViewZoom(float64(v) / 100)
	return v
}

// applySavedInterfaceZoom applies the saved zoom once the main window's
// WebView exists. Wails creates it around startup, so it retries briefly.
func applySavedInterfaceZoom() {
	v := currentInterfaceZoom()
	if v == interfaceZoomDefault {
		return
	}
	go func() {
		for attempt := 0; attempt < 20; attempt++ {
			if setMainWebViewZoom(float64(v) / 100) {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()
}
