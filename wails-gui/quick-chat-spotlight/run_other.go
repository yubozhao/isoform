//go:build !darwin

package main

import (
	"context"
	"log"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// run is the Windows/Linux shell: a regular Wails window. (macOS uses a
// native non-activating NSPanel — see run_darwin.go.)
func run(a *app) {
	appOptions := &options.App{
		Title: spotlightWindowTitle,
		// Initial size = pill. The first /show resets it; the frontend
		// resizes via ResizeMode() once the user submits.
		Width:  pillWidth,
		Height: pillHeight,
		// Frameless because the panel paints its own chrome.
		Frameless: true,
		// AlwaysOnTop so the panel stays visible over the user's previous
		// app even after the WebView is loaded.
		AlwaysOnTop: true,
		// Start hidden — we only show when /show fires. HideWindowOnClose
		// keeps us alive between presses; the supervisor's /quit is the
		// only path to actual exit.
		StartHidden:       true,
		HideWindowOnClose: true,
		DisableResize:     true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// The hidden WebView resolves the shared panel token and calls
		// SetSurfaceColor before the helper is shown.
		OnStartup: a.startup,
		Bind:      []any{a},
	}

	switch runtime.GOOS {
	case "windows":
		appOptions.Windows = &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			BackdropType:         windows.None,
		}
	}

	if err := wails.Run(appOptions); err != nil {
		log.Fatalf("wails run: %v", err)
	}
}

// startup runs once after Wails has booted. Stash the context, do the
// one-time Win32 setup, register the focus-loss auto-hide callback, then
// start the IPC server bound to the app's lifetime.
func (a *app) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()

	hideFromTaskbar(spotlightWindowTitle)
	setSpotlightTitle(spotlightWindowTitle)
	configureSpotlightAppPresentation()
	clearWindowBackgroundForTransparency()
	setOnWindowDeactivated(func() {
		a.mu.Lock()
		c := a.ctx
		auto := a.autoHideOnBlur
		a.mu.Unlock()
		if c == nil || !auto {
			return
		}
		wailsruntime.WindowHide(c)
	})

	go a.serveIPC(ctx)
}

// applySpotlightFrame resizes + repositions the window (centered,
// bottom-anchored) in one OS-level operation where possible. Win32 uses
// SetWindowPos (atomic); other platforms fall back to Wails' two-step.
func applySpotlightFrame(ctx context.Context, w, h int, followMouse bool) {
	if setSpotlightFrameWin32(spotlightWindowTitle, w, h) {
		return
	}
	if setSpotlightFrameDarwin(spotlightWindowTitle, w, h, spotlightBottomFraction, followMouse) {
		return
	}
	screens, err := wailsruntime.ScreenGetAll(ctx)
	if err != nil || len(screens) == 0 {
		wailsruntime.WindowSetSize(ctx, w, h)
		wailsruntime.WindowCenter(ctx)
		return
	}
	target := screens[0]
	found := false
	for _, s := range screens {
		if s.IsCurrent {
			target = s
			found = true
			break
		}
	}
	if !found {
		for _, s := range screens {
			if s.IsPrimary {
				target = s
				break
			}
		}
	}
	sw, sh := target.Size.Width, target.Size.Height
	if sw <= 0 || sh <= 0 {
		wailsruntime.WindowSetSize(ctx, w, h)
		wailsruntime.WindowCenter(ctx)
		return
	}
	x := max((sw-w)/2, 0)
	y := max(int(float64(sh)*spotlightBottomFraction)-h, 0)
	wailsruntime.WindowSetSize(ctx, w, h)
	wailsruntime.WindowSetPosition(ctx, x, y)
}

// SetAutoHideOnBlur toggles whether the window auto-hides on focus loss.
func (a *app) SetAutoHideOnBlur(enabled bool) {
	a.mu.Lock()
	a.autoHideOnBlur = enabled
	a.mu.Unlock()
}

// SetSurfaceColor updates the Win32 window background to match the theme.
// No-op off Windows.
func (a *app) SetSurfaceColor(r, g, b uint8) {
	if runtime.GOOS != "windows" {
		return
	}
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	wailsruntime.WindowSetBackgroundColour(ctx, r, g, b, 255)
}

// SetDarkMode tells DWM to render the window frame in the dark palette.
func (a *app) SetDarkMode(dark bool) {
	a.mu.Lock()
	a.darkMode = dark
	a.mu.Unlock()
	applyDwmDarkMode(spotlightWindowTitle, dark)
}

// HideWindow lets the frontend dismiss the panel without window.close.
func (a *app) HideWindow() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	wailsruntime.WindowHide(ctx)
}

// ResizeMode resizes the window between "pill" and "conversation",
// bottom-anchored, atomically where possible.
func (a *app) ResizeMode(mode string) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	target := struct{ w, h int }{pillWidth, pillHeight}
	switch mode {
	case "conversation":
		target.w, target.h = conversationWidth, conversationHeight
	case "ready":
		target.h = pillHeight + readyExtraHeight
	}

	a.mu.Lock()
	startW, startH := a.lastSizeW, a.lastSizeH
	a.lastSizeW = target.w
	a.lastSizeH = target.h
	a.mu.Unlock()

	if startW == target.w && startH == target.h {
		return
	}
	// followMouse=false: stay on the panel's current screen so a
	// wandering mouse doesn't relocate the conversation mid-typing.
	applySpotlightFrame(ctx, target.w, target.h, false)
}

// presentPanel surfaces the pill with the given frontmost-app context.
func (a *app) presentPanel(p showPayload) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	// followMouse=true: jump the pill to the screen the user's mouse is
	// on, matching Raycast / Alfred multi-monitor UX.
	applySpotlightFrame(ctx, pillWidth, pillHeight, true)
	hideFromTaskbar(spotlightWindowTitle)
	wailsruntime.WindowShow(ctx)
	clearWindowBackgroundForTransparency()
	wailsruntime.WindowSetAlwaysOnTop(ctx, true)
	applyDwmRoundedFrameless(spotlightWindowTitle)
	a.mu.Lock()
	dark := a.darkMode
	a.mu.Unlock()
	applyDwmDarkMode(spotlightWindowTitle, dark)
	// On Windows, WindowShow doesn't grab keyboard focus — force it.
	forceWindowFocus(spotlightWindowTitle)
	wailsruntime.EventsEmit(ctx, "spotlight:show", p)
}

// dismissPanel hides the panel.
func (a *app) dismissPanel() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	wailsruntime.EventsEmit(ctx, "spotlight:hide", nil)
	wailsruntime.WindowHide(ctx)
}

// quitApp exits the helper process.
func (a *app) quitApp() {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		wailsruntime.Quit(ctx)
	}
}
