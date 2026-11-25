package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/clipboard"
)

// clipboardPermissionKey is the "clipboard" item in Settings > Observation:
// toggleable, on by default, with no macOS prompt behind it.
const clipboardPermissionKey = "clipboard"

// clipboardObserver watches what the user copies and records each copy as an
// activity event. A copy is a strong sign of what the user means to do next.
//
// What is read and kept:
//   - Every second, the pasteboard's change counter; on a change, its types.
//     Neither reads what was copied.
//   - Nothing more while the switch is off or observing is paused, and a
//     copy made meanwhile is never recorded later.
//   - Nothing at all for a copy marked concealed, transient or
//     auto-generated, for Isoform's own writes, or for a copy made while a
//     password manager, an app the user protects, Isoform itself or a
//     private browser window was in front, at the tick that saw it or the
//     one before.
//   - The text itself only when macOS lets Isoform read it without asking
//     (before macOS 15.4, or when the user chose Always Allow for Isoform
//     under Paste from Other Apps); it is reduced to a scrubbed preview and
//     the text is dropped.
//   - Only the kind of a copy made while Safari was in front: its private
//     windows are titled like any other, so they cannot be told apart.
type clipboardObserver struct {
	app     *App
	watcher *clipboard.Watcher
	self    string
	// front is the frontmost app when the current copy was seen, and
	// kindOnly whether the copy keeps only its kind; Accept and OnCopy run
	// one after the other on the watcher goroutine.
	front    activitymon.AppInfo
	kindOnly bool

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// startClipboardObserver starts the watcher once from startup on platforms
// that have one.
func (a *App) startClipboardObserver(ctx context.Context) {
	if a == nil || !clipboard.Supported() {
		return
	}
	obs := &clipboardObserver{app: a, self: clipboard.SelfBundleID()}
	watcher := &clipboard.Watcher{
		Board:   clipboard.SystemBoard{},
		Allowed: obs.allowed,
		Front:   obs.sampleFront,
		Accept:  obs.accept,
		OnCopy:  obs.record,
	}
	obs.watcher = watcher
	a.clipboard.Store(obs)
	runCtx, cancel := context.WithCancel(ctx)
	obs.mu.Lock()
	obs.cancel = cancel
	obs.mu.Unlock()
	obs.wg.Add(1)
	go func() {
		defer obs.wg.Done()
		watcher.Run(runCtx)
	}()
}

// stopClipboardObserver ends the watcher at shutdown.
func (a *App) stopClipboardObserver() {
	if a == nil {
		return
	}
	obs := a.clipboard.Load()
	if obs == nil {
		return
	}
	obs.mu.Lock()
	cancel := obs.cancel
	obs.cancel = nil
	obs.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	obs.wg.Wait()
}

// clipboardSwitchChanged applies the Settings switch at once. Whatever is on
// the pasteboard at that moment counts as seen, so a copy made while the
// switch was off is never recorded.
func (a *App) clipboardSwitchChanged(enabled bool) {
	if a == nil {
		return
	}
	obs := a.clipboard.Load()
	if obs == nil {
		return
	}
	obs.watcher.Rebaseline()
}

// clipboardPauseChanged runs when observing is paused or resumed, for the
// same reason.
func (a *App) clipboardPauseChanged() {
	if a == nil {
		return
	}
	if obs := a.clipboard.Load(); obs != nil {
		obs.watcher.Rebaseline()
	}
}

// sampleFront is the app in front at one tick, with whether a browser's
// window is private, so a copy made in a private window and followed by an
// app switch before the next tick is still refused.
func (o *clipboardObserver) sampleFront() clipboard.App {
	app := clipboard.FrontApp()
	if activitymon.IsBrowser(app.BundleID) {
		app.Private = o.app.browserWindowPrivate(app.BundleID)
	}
	return app
}

func (o *clipboardObserver) allowed() bool {
	return o.app.captureAllowed(context.Background(), "clipboard_observer", clipboardPermissionKey) && !o.app.IsClosedEyesModeEnabled()
}

// accept decides, before anything copied is read, whether the apps in front
// around the copy may be observed: the ones sampled at this tick and the
// last, then the frontmost window, read once for its title.
func (o *clipboardObserver) accept(types []string, seen []clipboard.App) bool {
	for _, app := range seen {
		if !clipboardAppAllowed(app, o.self) {
			return false
		}
	}
	o.front = activitymon.FrontmostAppInfo()
	o.kindOnly = clipboardKindOnly(seen, o.front)
	return clipboardSourceAllowed(o.front, o.self)
}

// clipboardKindOnlyBrowsers are the browsers whose private windows cannot be
// recognised: Safari titles a private window with the page title alone.
var clipboardKindOnlyBrowsers = map[string]bool{
	"com.apple.Safari":                  true,
	"com.apple.SafariTechnologyPreview": true,
}

// clipboardKindOnly reports whether a copy keeps only its kind, never a
// preview or a length: one of the apps in front around it is a browser whose
// private windows cannot be recognised.
func clipboardKindOnly(seen []clipboard.App, front activitymon.AppInfo) bool {
	if clipboardKindOnlyBrowsers[front.BundleID] {
		return true
	}
	for _, app := range seen {
		if clipboardKindOnlyBrowsers[app.BundleID] {
			return true
		}
	}
	return false
}

// clipboardAppAllowed reports whether a copy made while this app was in front
// may be recorded: not a password manager or a protected app, not Isoform.
func clipboardAppAllowed(app clipboard.App, self string) bool {
	if app.Private || (self != "" && app.BundleID == self) {
		return false
	}
	return !clipboard.SensitiveSource(app.BundleID, app.Name)
}

// clipboardSourceAllowed adds the window rule: not a private browser window.
func clipboardSourceAllowed(front activitymon.AppInfo, self string) bool {
	if !clipboardAppAllowed(clipboard.App{BundleID: front.BundleID, Name: front.AppName}, self) {
		return false
	}
	return !activitymon.IsPrivateBrowsing(front.WindowTitle)
}

func (o *clipboardObserver) record(c clipboard.Copy) {
	evt, ok := clipboardActivityEvent(c, o.front, o.kindOnly)
	if !ok {
		return
	}
	// The pause may have started while the copy was read.
	if !o.allowed() {
		return
	}
	// The activity store is set up at startup; a copy before that is not
	// worth racing its setup for.
	if o.app.activity == nil {
		return
	}
	o.app.recordSystemActivityEvent(evt)
}

// clipboardActivityEvent turns one copy into the event that is stored. The
// copied text never leaves this function: only its kind, length and scrubbed
// preview do, and with kindOnly only its kind. A copy whose own source marker
// names a sensitive app is dropped.
func clipboardActivityEvent(c clipboard.Copy, front activitymon.AppInfo, kindOnly bool) (activitymon.ActivityEvent, bool) {
	if c.Source != "" && clipboard.SensitiveSource(c.Source, "") {
		return activitymon.ActivityEvent{}, false
	}
	evt := activitymon.ActivityEvent{
		Timestamp:     c.At.UnixMilli(),
		Kind:          activitymon.EventClipboard,
		AppBundleID:   front.BundleID,
		AppName:       front.AppName,
		ClipboardKind: c.Kind,
	}
	if c.ContentRead && !kindOnly {
		switch c.Kind {
		case clipboard.KindText, clipboard.KindURL:
			evt.ClipboardPreview = clipboard.Preview(c.Text)
		}
		// The length of a withheld text (a password, a token) says too much.
		if evt.ClipboardPreview != "" {
			evt.ClipboardLength = c.Length
		}
	}
	return evt, true
}

// clipboardTimelineLine is the one sentence triage reads for a copy: what
// kind of thing was copied, where, and its preview when there is one.
func clipboardTimelineLine(evt activitymon.ActivityEvent) string {
	var b strings.Builder
	switch evt.ClipboardKind {
	case clipboard.KindText:
		b.WriteString("- Copied text")
		if evt.ClipboardLength > 0 {
			fmt.Fprintf(&b, " (about %d characters)", evt.ClipboardLength)
		}
	case clipboard.KindURL:
		b.WriteString("- Copied a link")
	case clipboard.KindFile:
		b.WriteString("- Copied a file")
	case clipboard.KindImage:
		b.WriteString("- Copied an image")
	default:
		b.WriteString("- Copied something")
	}
	if evt.AppName != "" {
		fmt.Fprintf(&b, " in %s", evt.AppName)
	}
	if evt.ClipboardPreview != "" {
		fmt.Fprintf(&b, ": %q", evt.ClipboardPreview)
	}
	b.WriteString(" (a copy is a sign of what the user means to use next)\n")
	return b.String()
}
