//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// Forward declarations — implemented on the Go side via //export.
extern void goToastEvent(char *id, char *type);

// ── Message handler bridging WKWebView JS postMessage to Go ──────────
@interface YansuToastMessageHandler : NSObject <WKScriptMessageHandler, WKNavigationDelegate>
@property (copy) NSString *toastId;
- (void)contentFailed;
@end

@implementation YansuToastMessageHandler
- (void)userContentController:(WKUserContentController *)uc
      didReceiveScriptMessage:(WKScriptMessage *)message {
    id body = message.body;
    NSString *type = nil;
    if ([body isKindOfClass:[NSDictionary class]]) {
        type = body[@"type"];
    }
    if (type == nil || self.toastId == nil) return;
    goToastEvent((char *)[self.toastId UTF8String], (char *)[type UTF8String]);
}

- (void)contentFailed {
    if (self.toastId) goToastEvent((char *)self.toastId.UTF8String, "failed");
}

- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView {
    [self contentFailed];
}

- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    [self contentFailed];
}

- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    [self contentFailed];
}
@end

// ── Borderless, non-activating toast panel ──────────────────────────
// An NSPanel (not a plain NSWindow) with NSWindowStyleMaskNonactivatingPanel:
// showing it must never activate Yansu or disturb its window ordering. As a
// plain NSWindow, creating/ordering the toast while the app sits in accessory
// mode with the main window ordered-out caused AppKit to surface the hidden
// main window (the "window pops to front after a background toast" bug — no
// explicit WindowShow was ever called). A non-activating panel is the Cocoa
// idiom for HUD/toast surfaces that stay out of the host app's way.
@interface YansuToastWindow : NSPanel
@property (strong) WKWebView *webView;
@property (strong) YansuToastMessageHandler *handler;
@property (copy) NSString *toastId;
@property BOOL closing;
@end

@implementation YansuToastWindow
- (BOOL)canBecomeKeyWindow   { return NO; }
- (BOOL)canBecomeMainWindow  { return NO; }
@end

// Retain table: toast id → YansuToastWindow*.  Accessed only from the main
// queue, so no additional locking is required.
static NSMutableDictionary<NSString *, YansuToastWindow *> *_toastWindows = nil;

static void ensureToastDict(void) {
    if (!_toastWindows) _toastWindows = [[NSMutableDictionary alloc] init];
}

static void teardownToastWindow(YansuToastWindow *win) {
    if (!win) return;
    win.handler.toastId = nil;

    WKWebView *web = win.webView;
    if (web) {
        web.navigationDelegate = nil;
        WKUserContentController *uc = web.configuration.userContentController;
        [uc removeScriptMessageHandlerForName:@"toast"];
        [uc removeAllUserScripts];
        [web stopLoading];
        [web removeFromSuperview];
        win.webView = nil;
    }
    win.handler = nil;
    win.toastId = nil;
}

// Resolve the screen to anchor toasts on: prefer the key window's screen
// (NSScreen.mainScreen), fall back to the mouse-cursor screen.
static NSScreen *toastTargetScreen(void) {
    NSScreen *s = [NSScreen mainScreen];
    if (s) return s;
    NSPoint p = [NSEvent mouseLocation];
    for (NSScreen *cand in [NSScreen screens]) {
        if (NSPointInRect(p, cand.frame)) return cand;
    }
    return [[NSScreen screens] firstObject];
}

// Compute the origin for the i-th visible toast (0 = top).  Cocoa's y-axis
// grows upward, so stacking means subtracting (height+gap) per index.
static NSPoint toastOriginForIndex(int index, CGFloat width, CGFloat height) {
    NSScreen *screen = toastTargetScreen();
    NSRect vf = screen.visibleFrame;
    // The window is larger than the visible card: toast.html pads the body
    // (8px top/left for the overlapping close button, 14px right / 20px
    // bottom for the CSS drop shadow), so a 0 right margin still leaves the
    // card ~14px from the screen edge.
    CGFloat rightMargin = 0.0;
    CGFloat topMargin   = 12.0;
    // Negative gap cancels the transparent shadow/padding zone between
    // cards, leaving a ~7px visual gap under a full-height card.
    CGFloat gap         = -22.0;
    CGFloat x = NSMaxX(vf) - width - rightMargin;
    CGFloat yTopOrigin = NSMaxY(vf) - height - topMargin;
    CGFloat y = yTopOrigin - (height + gap) * (CGFloat)index;
    return NSMakePoint(x, y);
}

void goShowToast(const char *cID, const char *cHTML, int index, int width, int height) {
    NSString *toastId = [NSString stringWithUTF8String:cID];
    NSString *html    = [NSString stringWithUTF8String:cHTML];
    dispatch_async(dispatch_get_main_queue(), ^{
        ensureToastDict();

        NSPoint origin = toastOriginForIndex(index, (CGFloat)width, (CGFloat)height);
        NSRect frame   = NSMakeRect(origin.x, origin.y, (CGFloat)width, (CGFloat)height);

        YansuToastWindow *win = [[YansuToastWindow alloc]
            initWithContentRect:frame
            styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
            backing:NSBackingStoreBuffered
            defer:NO];
        win.toastId             = toastId;
        win.opaque              = NO;
        win.backgroundColor     = [NSColor clearColor];
        win.hasShadow           = NO;                   // shadow is drawn by CSS
        win.level               = NSStatusWindowLevel;  // above floating, below modal
        win.hidesOnDeactivate   = NO;                   // persist when Yansu is inactive
        win.ignoresMouseEvents  = NO;
        win.collectionBehavior  = NSWindowCollectionBehaviorCanJoinAllSpaces
                                | NSWindowCollectionBehaviorFullScreenAuxiliary
                                | NSWindowCollectionBehaviorTransient
                                | NSWindowCollectionBehaviorIgnoresCycle;
        win.releasedWhenClosed  = NO;
        win.alphaValue          = 0.0;                  // revealed on "ready"
        win.movable             = NO;

        WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
        WKUserContentController *uc    = [[WKUserContentController alloc] init];

        YansuToastMessageHandler *h = [[YansuToastMessageHandler alloc] init];
        h.toastId = toastId;
        [uc addScriptMessageHandler:h name:@"toast"];
        config.userContentController = uc;
        config.websiteDataStore      = [WKWebsiteDataStore nonPersistentDataStore];

        WKWebView *web = [[WKWebView alloc]
            initWithFrame:win.contentView.bounds
            configuration:config];
        web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
        web.wantsLayer       = YES;
        web.layer.backgroundColor = [[NSColor clearColor] CGColor];
        // Private-ish but widely relied upon: make the WebKit compositor
        // transparent so the window's clear background shows through.
        // Ignore if drawsBackground KVC is unsupported on this macOS version.
        @try { [web setValue:@NO forKey:@"drawsBackground"]; }
        @catch (NSException *e) { }
        web.allowsBackForwardNavigationGestures = NO;
        web.navigationDelegate = h;

        win.webView = web;
        win.handler = h;

        [win.contentView addSubview:web];
        [_toastWindows setObject:win forKey:toastId];
        [web release];
        [h release];
        [uc release];
        [config release];
        [win release];

        [web loadHTMLString:html baseURL:nil];
    });
}

void goRevealToast(const char *cID) {
    NSString *toastId = [NSString stringWithUTF8String:cID];
    dispatch_async(dispatch_get_main_queue(), ^{
        YansuToastWindow *win = _toastWindows[toastId];
        if (!win || win.closing) return;
        // The card already fades/slides in via CSS after reveal. An AppKit
        // alpha animation can leave a transparent panel ordered on screen.
        win.alphaValue = 1.0;
        [win orderFrontRegardless];
    });
}

void goMoveToast(const char *cID, int index, int width, int height) {
    NSString *toastId = [NSString stringWithUTF8String:cID];
    dispatch_async(dispatch_get_main_queue(), ^{
        YansuToastWindow *win = _toastWindows[toastId];
        if (!win) return;
        NSPoint origin = toastOriginForIndex(index, (CGFloat)width, (CGFloat)height);
        NSRect target = NSMakeRect(origin.x, origin.y, (CGFloat)width, (CGFloat)height);
        [NSAnimationContext runAnimationGroup:^(NSAnimationContext *ctx) {
            ctx.duration = 0.20;
            [win.animator setFrame:target display:YES];
        } completionHandler:nil];
    });
}

void goBeginDismissToast(const char *cID) {
    NSString *toastId = [NSString stringWithUTF8String:cID];
    dispatch_async(dispatch_get_main_queue(), ^{
        YansuToastWindow *win = _toastWindows[toastId];
        if (!win) return;
        win.closing = YES;
        [win.webView evaluateJavaScript:@"window.__toastFadeOut && window.__toastFadeOut();"
                       completionHandler:nil];
        [NSAnimationContext runAnimationGroup:^(NSAnimationContext *ctx) {
            ctx.duration = 0.16;
            win.animator.alphaValue = 0.0;
        } completionHandler:nil];
    });
}

void goCloseToast(const char *cID) {
    NSString *toastId = [NSString stringWithUTF8String:cID];
    dispatch_async(dispatch_get_main_queue(), ^{
        YansuToastWindow *win = _toastWindows[toastId];
        if (!win) return;
        teardownToastWindow(win);
        [win orderOut:nil];
        [win close];
        [_toastWindows removeObjectForKey:toastId];
    });
}

// Pre-warm helper: loads about:blank into a throwaway WKWebView so the WebKit
// machinery (network stack, JIT, font cache) is ready before the first real
// toast arrives.  The view is released immediately — its effect is process
// warmup, not reuse.
void goPrewarmToastWebView(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
        cfg.websiteDataStore = [WKWebsiteDataStore nonPersistentDataStore];
        WKWebView *w = [[WKWebView alloc] initWithFrame:NSMakeRect(0,0,1,1) configuration:cfg];
        [cfg release];
        [w loadHTMLString:@"<!doctype html><meta charset=utf-8>" baseURL:nil];
        // Hold briefly so the load can kick off, then release the helper view.
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.5 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), ^{
                           [w stopLoading];
                           [w release];
                       });
    });
}
*/
import "C"

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"
)

//go:embed toast_window_assets/toast.html
var toastHTMLTemplate string

// Window geometry must stay in sync with the body padding in
// toast_window_assets/toast.html: the card is inset 8px (top/left, close
// button room) and 14px/20px (right/bottom, shadow bleed room), so the
// visible card is 352pt wide and up to ~86pt tall.
const (
	toastWindowWidth  = 374
	toastWindowHeight = 114
	toastMaxVisible   = 5
	toastDuration     = 4 * time.Second
	toastFadeDuration = 170 * time.Millisecond
	toastReadyTimeout = 5 * time.Second
)

type toastRequest struct {
	id        string
	title     string
	body      string
	iconPath  string
	clickData string
	options   toastOptions
}

type toastInstance struct {
	*toastRequest
	idx       int
	timer     *time.Timer
	paused    bool
	ready     bool
	startAt   time.Time
	remaining time.Duration
}

type toastQueueState struct {
	mu      sync.Mutex
	visible []*toastInstance
	pending []*toastRequest
	counter uint64
	clickFn func(clickData string)
}

var toastState = &toastQueueState{}

// iconCache memoises the base64 data-URL for the app icon so we don't hit
// disk on every toast.  The key is the source path; the value is the
// `data:image/...;base64,...` URL ready to drop into an <img src>.
var iconCache sync.Map

// setToastClickHandler registers the callback invoked when the user clicks a
// toast body.  The clickData is whatever was passed to showDesktopToast.
func setToastClickHandler(fn func(clickData string)) {
	toastState.mu.Lock()
	toastState.clickFn = fn
	toastState.mu.Unlock()
}

// prewarmDesktopToast warms the WebKit subsystem so the first toast's
// content paints without a noticeable delay.  Safe to call multiple times;
// each call is cheap.
func prewarmDesktopToast() {
	C.goPrewarmToastWebView()
}

// showDesktopToast enqueues a toast.  Up to toastMaxVisible are shown at
// once; excess requests wait in the pending queue and promote as slots open.
func showDesktopToast(title, body, iconPath, clickData string) {
	showDesktopToastWithOptions(title, body, iconPath, clickData, toastOptions{})
}

// showDesktopToastWithOptions enqueues a toast that may carry one action
// button and reports what the user did with it through the hooks.
func showDesktopToastWithOptions(title, body, iconPath, clickData string, options toastOptions) {
	// Locked notifications deliberately carry only a generic title. Reject
	// only requests with no text at all, before creating a native panel.
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return
	}
	// Respect the system toggle: if the user turned Yansu's notifications OFF
	// in System Settings, stay silent instead of bypassing it with our
	// in-house toast. User-initiated toasts (e.g. the settings "Test" button)
	// gate this themselves before calling, so they still surface a result.
	if notificationExplicitlyDenied() {
		log.Printf("[toast] suppressed (notifications disabled in System Settings) title=%q", title)
		return
	}

	req := &toastRequest{
		title:     title,
		body:      body,
		iconPath:  iconPath,
		clickData: clickData,
		options:   options,
	}

	toastState.mu.Lock()
	toastState.counter++
	req.id = fmt.Sprintf("toast-%d-%d", time.Now().UnixNano(), toastState.counter)
	if len(toastState.visible) >= toastMaxVisible {
		toastState.pending = append(toastState.pending, req)
		toastState.mu.Unlock()
		log.Printf("[toast] queued (pending=%d) id=%s title=%q", len(toastState.pending), req.id, title)
		return
	}
	inst := &toastInstance{toastRequest: req, idx: len(toastState.visible)}
	toastState.visible = append(toastState.visible, inst)
	toastState.mu.Unlock()

	log.Printf("[toast] showing id=%s idx=%d title=%q", inst.id, inst.idx, title)
	presentToast(inst)
}

func presentToast(inst *toastInstance) {
	html := renderToastHTML(inst.toastRequest)

	cID := C.CString(inst.id)
	cHTML := C.CString(html)
	defer C.free(unsafe.Pointer(cID))
	defer C.free(unsafe.Pointer(cHTML))
	toastState.mu.Lock()
	defer toastState.mu.Unlock()
	if toastFindVisible(inst.id) != inst {
		return
	}
	C.goShowToast(cID, cHTML, C.int(inst.idx), C.int(toastWindowWidth), C.int(toastWindowHeight))

	// Never order an unready WebView on screen. Release its queue slot if
	// loading fails; apply this watchdog to promoted requests as well.
	if !inst.ready {
		inst.timer = time.AfterFunc(toastReadyTimeout, func() { handleToastLoadTimeout(inst.id) })
	}
}

func renderToastHTML(req *toastRequest) string {
	// The toast must match the app's selected theme (which the user can set
	// independently of the OS appearance), so resolve it here rather than
	// letting the WebView follow prefers-color-scheme.
	theme := "light"
	if currentThemePreference() == "onedark" {
		theme = "dark"
	}
	payload := map[string]any{
		"title": req.title,
		"body":  req.body,
		"theme": theme,
	}
	if len(req.options.Actions) > 0 {
		payload["actions"] = req.options.Actions
	} else if label := strings.TrimSpace(req.options.ActionLabel); label != "" {
		payload["action"] = label
	}
	if url := iconDataURL(req.iconPath); url != "" {
		payload["icon"] = url
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte("null")
	}
	return strings.Replace(strings.Replace(toastHTMLTemplate, "/*__BRAND_TOKENS__*/", brandThemeCSS(), 1),
		"/*__TOAST_DATA__*/ null",
		string(encoded), 1)
}

func iconDataURL(path string) string {
	if path == "" {
		return ""
	}
	if cached, ok := iconCache.Load(path); ok {
		return cached.(string)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	mime := http.DetectContentType(data)
	url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	iconCache.Store(path, url)
	return url
}

// Event dispatch from the Obj-C side lives in toast_window_export_darwin.go
// so the //export directive doesn't share a translation unit with the
// @implementation blocks above (cgo would otherwise emit them twice).
func handleToastEvent(id, typ string) {
	switch typ {
	case "ready":
		handleToastReady(id)
	case "click":
		handleToastClick(id)
	case "action":
		handleToastAction(id)
	default:
		if action, ok := strings.CutPrefix(typ, "action:"); ok {
			handleToastActionID(id, action)
		}
	case "close":
		handleToastDismiss(id)
	case "failed":
		handleToastClose(id)
	case "pause":
		handleToastPause(id)
	case "resume":
		handleToastResume(id)
	}
}

func handleToastReady(id string) {
	toastState.mu.Lock()
	inst := toastFindVisible(id)
	if inst == nil || inst.ready || (strings.TrimSpace(inst.title) == "" && strings.TrimSpace(inst.body) == "") {
		toastState.mu.Unlock()
		return
	}
	if inst.timer != nil {
		inst.timer.Stop()
	}
	inst.ready = true
	inst.startAt = time.Now()
	inst.remaining = toastDuration
	inst.timer = time.AfterFunc(toastDuration, func() { handleToastClose(id) })
	shown := inst.options.OnShown
	toastState.mu.Unlock()

	cID := C.CString(id)
	defer C.free(unsafe.Pointer(cID))
	C.goRevealToast(cID)
	if shown != nil {
		go shown()
	}
}

func handleToastClick(id string) {
	toastState.mu.Lock()
	inst := toastFindVisible(id)
	var clickData string
	var opened func()
	clickFn := toastState.clickFn
	if inst != nil {
		clickData = inst.clickData
		opened = inst.options.OnOpened
	}
	toastState.mu.Unlock()

	if opened != nil {
		go opened()
	}
	if clickFn != nil && clickData != "" {
		go clickFn(clickData)
	}
	handleToastClose(id)
}

// handleToastAction runs the toast's one action button. Without an action
// hook the button behaves like a click on the body.
func handleToastAction(id string) {
	toastState.mu.Lock()
	inst := toastFindVisible(id)
	var clickData string
	var acted func()
	clickFn := toastState.clickFn
	if inst != nil {
		clickData = inst.clickData
		acted = inst.options.OnActed
	}
	toastState.mu.Unlock()

	if acted != nil {
		go acted()
	}
	if clickFn != nil && clickData != "" {
		go clickFn(clickData)
	}
	handleToastClose(id)
}

// handleToastActionID runs one of a notification's actions by its id: the
// hook hears which, and only "open" also opens the app at the notification.
func handleToastActionID(id, action string) {
	toastState.mu.Lock()
	inst := toastFindVisible(id)
	var clickData string
	var onAction func(string)
	clickFn := toastState.clickFn
	if inst != nil {
		clickData = inst.clickData
		onAction = inst.options.OnAction
	}
	toastState.mu.Unlock()
	if inst == nil {
		return
	}
	if onAction != nil {
		go onAction(action)
	}
	if toastActionOpens(action) && clickFn != nil && clickData != "" {
		go clickFn(clickData)
	}
	handleToastClose(id)
}

// handleToastDismiss is the close control: the user sent the toast away
// without reading it. A toast that times out reports nothing.
func handleToastDismiss(id string) {
	toastState.mu.Lock()
	inst := toastFindVisible(id)
	var dismissed func()
	if inst != nil {
		dismissed = inst.options.OnDismissed
	}
	toastState.mu.Unlock()
	if dismissed != nil {
		go dismissed()
	}
	handleToastClose(id)
}

func handleToastPause(id string) {
	toastState.mu.Lock()
	defer toastState.mu.Unlock()
	inst := toastFindVisible(id)
	if inst == nil || inst.paused || inst.timer == nil {
		return
	}
	if inst.timer.Stop() {
		elapsed := time.Since(inst.startAt)
		if elapsed < inst.remaining {
			inst.remaining -= elapsed
		} else {
			inst.remaining = 0
		}
	}
	inst.paused = true
}

func handleToastResume(id string) {
	toastState.mu.Lock()
	defer toastState.mu.Unlock()
	inst := toastFindVisible(id)
	if inst == nil || !inst.paused {
		return
	}
	if inst.remaining <= 0 {
		inst.remaining = 500 * time.Millisecond
	}
	inst.startAt = time.Now()
	inst.paused = false
	inst.timer = time.AfterFunc(inst.remaining, func() { handleToastClose(id) })
}

func handleToastClose(id string) {
	closeToast(id, false)
}

func handleToastLoadTimeout(id string) {
	closeToast(id, true)
}

func closeToast(id string, onlyUnready bool) {
	toastState.mu.Lock()
	inst := toastFindVisible(id)
	if inst == nil || (onlyUnready && inst.ready) {
		toastState.mu.Unlock()
		return
	}
	if inst.timer != nil {
		inst.timer.Stop()
	}

	newVisible := toastState.visible[:0:len(toastState.visible)]
	for _, v := range toastState.visible {
		if v.id != id {
			newVisible = append(newVisible, v)
		}
	}
	toastState.visible = newVisible
	for i, v := range toastState.visible {
		v.idx = i
	}

	var promote *toastInstance
	if len(toastState.pending) > 0 && len(toastState.visible) < toastMaxVisible {
		req := toastState.pending[0]
		toastState.pending = toastState.pending[1:]
		promote = &toastInstance{toastRequest: req, idx: len(toastState.visible)}
		toastState.visible = append(toastState.visible, promote)
	}

	repack := make([]*toastInstance, 0, len(toastState.visible))
	for _, v := range toastState.visible {
		if v != promote {
			repack = append(repack, v)
		}
	}
	toastState.mu.Unlock()

	cID := C.CString(id)
	C.goBeginDismissToast(cID)
	C.free(unsafe.Pointer(cID))

	time.AfterFunc(toastFadeDuration, func() {
		cID2 := C.CString(id)
		C.goCloseToast(cID2)
		C.free(unsafe.Pointer(cID2))
	})

	for _, v := range repack {
		cVID := C.CString(v.id)
		C.goMoveToast(cVID, C.int(v.idx), C.int(toastWindowWidth), C.int(toastWindowHeight))
		C.free(unsafe.Pointer(cVID))
	}

	if promote != nil {
		presentToast(promote)
	}
}

// toastFindVisible must be called with toastState.mu held.
func toastFindVisible(id string) *toastInstance {
	for _, v := range toastState.visible {
		if v.id == id {
			return v
		}
	}
	return nil
}

// debugToastCount is intentionally exported for ad-hoc debugging via log.
func debugToastCount() {
	toastState.mu.Lock()
	defer toastState.mu.Unlock()
	log.Printf("[toast] visible=%d pending=%d", len(toastState.visible), len(toastState.pending))
}
