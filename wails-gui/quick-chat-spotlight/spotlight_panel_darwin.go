//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// Tells Go to disarm the main app's dismiss-on-typing tap flag. Fired on
// every transition OUT of "shown and awaiting first input": the user
// engaged the pill (it became key) OR the pill hid (any path). A stale
// flag would otherwise make the next keystroke — including the quick-chat
// hotkey itself — race a dismiss against the re-show.
extern void goSpotlightDisarmDismiss(void);

// ── Floating non-activating panel ──────────────────────────────────
//
// NSWindowStyleMaskNonactivatingPanel lets the panel become key (so the
// text input and IME work) WITHOUT activating the spotlight process —
// the user's frontmost app keeps its menu bar / active state. This is
// the whole point of the native rewrite: Wails created a regular
// activating NSWindow that stole focus on every summon.
@interface YansuSpotlightPanel : NSPanel
@end

@implementation YansuSpotlightPanel
- (BOOL)canBecomeKeyWindow  { return YES; }
- (BOOL)canBecomeMainWindow { return NO; }
@end

// Bridges WKWebView JS → native. The React side posts
// window.webkit.messageHandlers.spotlight.postMessage({type, ...}) for
// the three window operations that used to be Wails bindings:
// hide / resize / autohide. Everything else (suggestions, submit,
// stream, theme, voice, ...) still goes over the host-bridge HTTP API
// and needs no native plumbing.
@interface YansuSpotlightBridge : NSObject <WKScriptMessageHandler>
@end

// Grants getUserMedia (voice input) inside the WebView. Wails v2 sets a
// UIDelegate that doesn't implement this, so WKWebView denies capture by
// default; the native panel owns its WKWebView and grants directly. The
// helper's Info.plist must carry NSMicrophoneUsageDescription and the
// signed bundle the com.apple.security.device.audio-input entitlement, or
// the OS still blocks (and a grant without the usage string would crash).
@interface YansuSpotlightUIDelegate : NSObject <WKUIDelegate>
@end

// A WKWebView that accepts the first click on an inactive window. The pill
// is summoned WITHOUT key focus (so it doesn't disturb the user's app), so
// the user's first click must both key the panel AND reach the textarea —
// otherwise it would take two clicks (one to key, one to focus) to type.
@interface YansuFirstMouseWebView : WKWebView
@end

// ── File-scope state (ARC-retained) ────────────────────────────────
static YansuSpotlightPanel *_panel = nil;
static WKWebView *_webView = nil;
static YansuSpotlightBridge *_bridge = nil;
static YansuSpotlightUIDelegate *_uiDelegate = nil;
static id _resignObserver = nil;
static id _becomeKeyObserver = nil;
static id _globalClickMonitor = nil;

// Readiness handshake: the WKWebView loads async, so the very first show
// after cold start can fire before React registers its spotlight:show
// listener. We dispatch the event only once the React side posts
// {type:"ready"}; a show that arrives earlier is buffered and flushed
// then, so the first summon still gets detection/suggestions/reset.
static BOOL _jsReady = NO;
static NSString *_pendingShowPayload = nil;

// Whether resigning key (user clicked another app) should auto-dismiss
// the panel. YES for the pill (transient capture surface, Raycast/Alfred
// UX), NO for the conversation view (long-lived chat — stays open while
// the user glances elsewhere). Reset to YES on every show.
static BOOL _autoHideOnBlur = YES;

// Pill / conversation dimensions + the bottom-anchor fraction, injected
// from Go (the canonical constants live in main.go) so the native side
// doesn't duplicate them.
static CGFloat _pillW = 768, _pillH = 88;
static CGFloat _convW = 768, _convH = 650;
static double  _bottomFraction = 0.88;

// jsonEscapeString returns a JSON-encoded representation of s including
// the surrounding quotes, safe to interpolate into a JS literal.
static NSString *jsonEscapeString(NSString *s) {
    NSData *data = [NSJSONSerialization dataWithJSONObject:@[s ?: @""]
                                                  options:0
                                                    error:NULL];
    if (!data) return @"\"\"";
    NSString *arr = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
    if (arr.length < 2) return @"\"\"";
    return [arr substringWithRange:NSMakeRange(1, arr.length - 2)];
}

// pickSpotlightScreen returns the screen the panel should land on. When
// followMouse is true we use the mouse location (matches Raycast/Alfred
// multi-monitor UX); otherwise the panel's current screen so resize
// transitions don't yank it to another display mid-conversation.
static NSScreen *pickSpotlightScreen(BOOL followMouse) {
    if (followMouse) {
        NSPoint mouse = [NSEvent mouseLocation];
        for (NSScreen *s in [NSScreen screens]) {
            if (NSPointInRect(mouse, s.frame)) return s;
        }
    }
    if (_panel != nil && _panel.screen != nil) return _panel.screen;
    return [NSScreen mainScreen];
}

// spotlightFrame centres the panel horizontally and anchors its bottom
// edge at (1 - _bottomFraction) of screen height above the screen
// origin (NSScreen coords, origin = bottom-left). Mirrors main.go's
// bottom-anchored chat-input dock layout.
static NSRect spotlightFrame(CGFloat w, CGFloat h, BOOL followMouse) {
    NSScreen *screen = pickSpotlightScreen(followMouse);
    NSRect sf = screen.frame;
    CGFloat x = sf.origin.x + (sf.size.width - w) / 2.0;
    CGFloat y = sf.origin.y + sf.size.height * (1.0 - _bottomFraction);
    return NSMakeRect(x, y, w, h);
}

static const CGFloat kReadyExtraHeight = 60;

static void resizeSpotlight(NSString *mode) {
    if (_panel == nil) return;
    CGFloat w = _pillW, h = _pillH;
    if ([mode isEqualToString:@"conversation"]) { w = _convW; h = _convH; }
    // Ready work first grows the pill window up by its row; keep in step
    // with main.go readyExtraHeight.
    else if ([mode isEqualToString:@"ready"]) { h = _pillH + kReadyExtraHeight; }
    // followMouse=NO: stay on the current screen so a wandering mouse
    // doesn't relocate the conversation mid-typing.
    [_panel setFrame:spotlightFrame(w, h, NO) display:YES];
}

// dispatchSpotlightShow fires the spotlight:show CustomEvent (carrying the
// frontmost-app context) into the React side. Caller must ensure the JS
// listener is registered (see the _jsReady handshake).
static void dispatchSpotlightShow(NSString *payload) {
    if (_webView == nil) return;
    NSString *js = [NSString stringWithFormat:
        @"window.dispatchEvent(new CustomEvent('spotlight:show',{detail:%@}));", payload];
    [_webView evaluateJavaScript:js completionHandler:nil];
}

// spotlightHideAndDisarm is the single hide path. Besides ordering the
// panel out, it disarms the main app's dismiss-on-key flag — EVERY hide
// must do this, or a stale flag races the next show (see the extern
// comment above). Centralised so no hide site forgets.
static void spotlightHideAndDisarm(void) {
    if (_panel) [_panel orderOut:nil];
    goSpotlightDisarmDismiss();
}

@implementation YansuSpotlightBridge
- (void)userContentController:(WKUserContentController *)uc
      didReceiveScriptMessage:(WKScriptMessage *)message {
    id body = message.body;
    if (![body isKindOfClass:[NSDictionary class]]) return;
    NSString *type = body[@"type"];
    if (![type isKindOfClass:[NSString class]]) return;
    if ([type isEqualToString:@"hide"]) {
        spotlightHideAndDisarm();
    } else if ([type isEqualToString:@"resize"]) {
        NSString *mode = body[@"mode"];
        if ([mode isKindOfClass:[NSString class]]) resizeSpotlight(mode);
    } else if ([type isEqualToString:@"autohide"]) {
        id enabled = body[@"enabled"];
        if ([enabled isKindOfClass:[NSNumber class]]) {
            _autoHideOnBlur = [enabled boolValue];
        }
    } else if ([type isEqualToString:@"ready"]) {
        _jsReady = YES;
        if (_pendingShowPayload != nil) {
            dispatchSpotlightShow(_pendingShowPayload);
            _pendingShowPayload = nil;
        }
    }
}
@end

@implementation YansuSpotlightUIDelegate
- (void)webView:(WKWebView *)webView
    requestMediaCapturePermissionForOrigin:(WKSecurityOrigin *)origin
            initiatedByFrame:(WKFrameInfo *)frame
                        type:(WKMediaCaptureType)type
             decisionHandler:(void (^)(WKPermissionDecision))decisionHandler {
    decisionHandler(WKPermissionDecisionGrant);
}
@end

@implementation YansuFirstMouseWebView
- (BOOL)acceptsFirstMouse:(NSEvent *)event { return YES; }
@end

// spotlightPanelStart creates the panel + WKWebView on the main thread
// and loads the asset URL. The panel starts hidden — spotlightPanelShow
// surfaces it on the global hotkey. Called once, before the run loop.
static void spotlightPanelStart(const char *cURL, const char *cConfigJSON,
                                int pillW, int pillH, int convW, int convH,
                                double bottomFraction) {
    NSString *urlStr    = cURL        ? [NSString stringWithUTF8String:cURL]        : @"";
    NSString *configJS  = cConfigJSON ? [NSString stringWithUTF8String:cConfigJSON] : @"{}";

    _pillW = pillW; _pillH = pillH;
    _convW = convW; _convH = convH;
    _bottomFraction = bottomFraction;

    // Ensure the shared application exists and stays out of the Dock /
    // app switcher (accessory). Idempotent with the Go-side
    // configureSpotlightAppPresentation call.
    NSApplication *app = [NSApplication sharedApplication];
    [app setActivationPolicy:NSApplicationActivationPolicyAccessory];

    NSRect frame = spotlightFrame(_pillW, _pillH, NO);
    NSUInteger style = NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel;
    YansuSpotlightPanel *panel = [[YansuSpotlightPanel alloc]
        initWithContentRect:frame
                  styleMask:style
                    backing:NSBackingStoreBuffered
                      defer:NO];
    panel.opaque             = NO;
    panel.backgroundColor    = [NSColor clearColor];
    panel.hasShadow          = NO;
    panel.level              = NSStatusWindowLevel;
    panel.movableByWindowBackground = YES;
    panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces
                             | NSWindowCollectionBehaviorFullScreenAuxiliary
                             | NSWindowCollectionBehaviorTransient
                             | NSWindowCollectionBehaviorIgnoresCycle;
    panel.releasedWhenClosed = NO;
    panel.hidesOnDeactivate  = NO;

    WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
    WKUserContentController *uc     = [[WKUserContentController alloc] init];
    YansuSpotlightBridge *bridge    = [[YansuSpotlightBridge alloc] init];
    [uc addScriptMessageHandler:bridge name:@"spotlight"];

    // Inject the host-bridge port + IPC token before any app script runs
    // so the React side can read window.__spotlightConfig synchronously
    // (replaces the Wails GetConfig binding).
    NSString *configScript = [NSString stringWithFormat:@"window.__spotlightConfig=%@;", configJS];
    WKUserScript *cfgUS = [[WKUserScript alloc] initWithSource:configScript
        injectionTime:WKUserScriptInjectionTimeAtDocumentStart
        forMainFrameOnly:YES];
    [uc addUserScript:cfgUS];
    config.userContentController = uc;

    YansuFirstMouseWebView *web = [[YansuFirstMouseWebView alloc] initWithFrame:panel.contentView.bounds
                                        configuration:config];
    web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    web.wantsLayer       = YES;
    web.layer.backgroundColor = [[NSColor clearColor] CGColor];
    @try { [web setValue:@NO forKey:@"drawsBackground"]; }
    @catch (NSException *e) { }
    web.allowsBackForwardNavigationGestures = NO;

    // Grant getUserMedia (voice input) — WKWebView denies by default.
    YansuSpotlightUIDelegate *uiDelegate = [[YansuSpotlightUIDelegate alloc] init];
    web.UIDelegate = uiDelegate;

    [panel.contentView addSubview:web];

    _panel      = panel;
    _webView    = web;
    _bridge     = bridge;
    _uiDelegate = uiDelegate;

    // Auto-dismiss when the panel resigns key (user clicked the pill, then
    // clicked another app), but only while autoHideOnBlur is set (pill, not
    // conversation).
    _resignObserver = [[NSNotificationCenter defaultCenter]
        addObserverForName:NSWindowDidResignKeyNotification
                    object:_panel
                     queue:[NSOperationQueue mainQueue]
                usingBlock:^(NSNotification * _Nonnull note) {
            if (_autoHideOnBlur && _panel) spotlightHideAndDisarm();
        }];

    // The user clicked into the pill (it became key) — they're now typing
    // INTO quick chat. Tell the main app to disarm dismiss-on-typing.
    _becomeKeyObserver = [[NSNotificationCenter defaultCenter]
        addObserverForName:NSWindowDidBecomeKeyNotification
                    object:_panel
                     queue:[NSOperationQueue mainQueue]
                usingBlock:^(NSNotification * _Nonnull note) {
            goSpotlightDisarmDismiss();
        }];

    // Click-outside-to-dismiss. The pill is summoned WITHOUT key focus, so
    // the resign-key observer above never fires until the user clicks in.
    // A global monitor sees mouse-downs delivered to OTHER apps (never our
    // own panel), so any click elsewhere hides the pill. Mouse-event global
    // monitors need no accessibility permission (only key-event ones do).
    // Gated on autoHideOnBlur so the conversation view stays open while the
    // user glances at other apps.
    _globalClickMonitor = [NSEvent
        addGlobalMonitorForEventsMatchingMask:(NSEventMaskLeftMouseDown | NSEventMaskRightMouseDown)
                                      handler:^(NSEvent *event) {
            if (_panel && _panel.isVisible && _autoHideOnBlur) {
                spotlightHideAndDisarm();
            }
        }];

    NSURL *url = [NSURL URLWithString:urlStr];
    if (url) [web loadRequest:[NSURLRequest requestWithURL:url]];
}

static void spotlightPanelRunLoop(void) {
    [[NSApplication sharedApplication] run];
}

// spotlightPanelShow surfaces the pill on the screen under the mouse
// WITHOUT taking key focus, so the user's frontmost app is completely
// undisturbed — it keeps its key window and blinking caret. The user opts
// in to typing by clicking the pill, which keys it (the non-activating
// panel + acceptsFirstMouse webview key on the first click without
// activating the spotlight process). Then dispatches a `spotlight:show`
// CustomEvent carrying the frontmost-app context to the React side.
static void spotlightPanelShow(const char *cPayloadJSON) {
    NSString *payload = cPayloadJSON ? [NSString stringWithUTF8String:cPayloadJSON] : @"{}";
    dispatch_async(dispatch_get_main_queue(), ^{
        if (_panel == nil) return;
        _autoHideOnBlur = YES;
        [_panel setFrame:spotlightFrame(_pillW, _pillH, YES) display:YES];
        // orderFrontRegardless WITHOUT makeKeyWindow: float on top but do
        // not steal focus. canBecomeKeyWindow stays YES so a user click keys
        // it when they choose to type.
        [_panel orderFrontRegardless];
        // Show the panel immediately; only the JS event waits for React to
        // register its listener (buffer until the {type:"ready"} handshake).
        if (_jsReady) {
            dispatchSpotlightShow(payload);
        } else {
            _pendingShowPayload = payload;
        }
    });
}

static void spotlightPanelHide(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        spotlightHideAndDisarm();
    });
}

static void spotlightPanelQuit(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp terminate:nil];
    });
}
*/
import "C"

import "unsafe"

// startSpotlightPanel builds the native panel + webview and loads the
// asset URL. Must run on the main OS thread before runSpotlightPanelLoop.
func startSpotlightPanel(url, configJSON string, pillW, pillH, convW, convH int, bottomFraction float64) {
	cURL := C.CString(url)
	defer C.free(unsafe.Pointer(cURL))
	cCfg := C.CString(configJSON)
	defer C.free(unsafe.Pointer(cCfg))
	C.spotlightPanelStart(cURL, cCfg,
		C.int(pillW), C.int(pillH), C.int(convW), C.int(convH), C.double(bottomFraction))
}

// runSpotlightPanelLoop starts the Cocoa run loop. Blocks until quit.
func runSpotlightPanelLoop() { C.spotlightPanelRunLoop() }

// showSpotlightPanel surfaces the pill with the given frontmost-app
// context (a JSON object string). Safe to call from any goroutine.
func showSpotlightPanel(payloadJSON string) {
	cPayload := C.CString(payloadJSON)
	defer C.free(unsafe.Pointer(cPayload))
	C.spotlightPanelShow(cPayload)
}

// hideSpotlightPanel dismisses the panel. Safe from any goroutine.
func hideSpotlightPanel() { C.spotlightPanelHide() }

// quitSpotlightPanel terminates the helper process. Safe from any goroutine.
func quitSpotlightPanel() { C.spotlightPanelQuit() }
