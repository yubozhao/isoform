//go:build windows

package main

import (
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Quick Chat on Windows uses the in-app overlay path rather than the
// floating non-activating panel macOS has. Reasoning, in short:
//
// Every mainstream Windows app that ships a "summon a chat box" UX
// either uses Electron or Tauri/WRY (which are sizeable C++ / Rust
// projects with years of testing for the Win32 + WebView2 quirks
// that come with embedding a webview into a non-activating popup),
// or sidesteps webviews entirely with WPF (PowerToys Run, Flow
// Launcher). A from-scratch Win32 + go-webview2 implementation lives
// in nobody-else's-codebase territory and we kept hitting layered
// issues — CO_E_NOTINITIALIZED on the panel thread, then STA cross-
// thread crashes from chromium.Eval, then invisible-but-rendering
// windows. The cost/benefit no longer made sense.
//
// In exchange for losing the macOS-style "doesn't disturb your
// current app" trick on Windows, we get a feature that actually
// works today: Alt+Space brings Yansu forward and opens the
// renderer's existing QuickChatOverlay component, which already
// handles app-specific suggestions and prompt submission. This is
// the same path the in-app Ctrl+J shortcut uses, so there's exactly
// one renderer flow to maintain.
//
// pushQuickChatSuggestions / pushQuickChatLoading are no-ops on
// Windows because the in-app overlay fetches its own suggestions via
// GetQuickChatSuggestionsForApp. The signatures are kept for parity
// with the macOS implementation so callers don't have to branch.

// ShowQuickChatWindow is invoked by the global Alt+Space hotkey. It
// activates the Yansu main window and emits the renderer event the
// overlay listens for, with NativeWindow=false so QuickChatOverlay
// opens (the same path the renderer's in-app Ctrl+J shortcut takes).
func (a *App) ShowQuickChatWindow(bundleID string) {
	if chatFeatureErr() != nil {
		return
	}
	quickChatWindowMu.Lock()
	quickChatWindowApp = a
	quickChatWindowOpen = true
	lastQuickChatBundleID = bundleID
	quickChatWindowMu.Unlock()

	if a.ctx == nil {
		return
	}
	wailsruntime.WindowShow(a.ctx)
	wailsruntime.Show(a.ctx)
	wailsruntime.EventsEmit(a.ctx, "quickChat:invoke",
		QuickChatInvokeEvent{FrontmostBundleID: bundleID, NativeWindow: false})
}

// CloseQuickChatWindow is a no-op on Windows. The in-app overlay's
// own state (open/closed) is owned by the renderer; the close
// transition runs through QuickChatOverlay's onOpenChange.
func (a *App) CloseQuickChatWindow() {
	quickChatWindowMu.Lock()
	quickChatWindowOpen = false
	quickChatWindowMu.Unlock()
}

// pushQuickChatSuggestions is a no-op on Windows. The renderer's
// QuickChatOverlay calls GetQuickChatSuggestionsForApp directly, so
// no Go-side push is needed.
func (a *App) pushQuickChatSuggestions(_ []RecommendedQuestion, _ string) {}

// pushQuickChatLoading is a no-op on Windows. See pushQuickChatSuggestions.
func (a *App) pushQuickChatLoading(_ string) {}
