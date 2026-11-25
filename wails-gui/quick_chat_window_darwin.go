//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// Go-side callbacks: fired when the user submits a prompt from the
// floating window, or when the window is dismissed.
extern void goQuickChatSubmit(char *prompt);
extern void goQuickChatClosed(void);
extern void goQuickChatRequestSuggestions(char *bundleID);

// ── Message handler bridges WKWebView JS → Go ───────────────────────
@interface YansuQuickChatMessageHandler : NSObject <WKScriptMessageHandler>
@end

@implementation YansuQuickChatMessageHandler
- (void)userContentController:(WKUserContentController *)uc
      didReceiveScriptMessage:(WKScriptMessage *)message {
    id body = message.body;
    if (![body isKindOfClass:[NSDictionary class]]) return;
    NSString *type = body[@"type"];
    if (!type) return;
    if ([type isEqualToString:@"submit"]) {
        NSString *prompt = body[@"prompt"];
        if (prompt && [prompt isKindOfClass:[NSString class]]) {
            goQuickChatSubmit((char *)[prompt UTF8String]);
        }
    } else if ([type isEqualToString:@"close"]) {
        goQuickChatClosed();
    } else if ([type isEqualToString:@"request-suggestions"]) {
        NSString *bundleID = body[@"bundleID"];
        const char *bid = bundleID ? [bundleID UTF8String] : "";
        goQuickChatRequestSuggestions((char *)bid);
    }
}
@end

// ── Floating non-activating panel ──────────────────────────────────
//
// NSWindowStyleMaskNonactivatingPanel lets the panel accept key events
// (so the text input works) without deactivating the user's current app.
// NSStatusWindowLevel floats above regular windows.
@interface YansuQuickChatPanel : NSPanel
@property (strong) WKWebView *webView;
@property (strong) YansuQuickChatMessageHandler *handler;
@property (strong) id localKeyMonitor;
@end

@implementation YansuQuickChatPanel
- (BOOL)canBecomeKeyWindow  { return YES; }
- (BOOL)canBecomeMainWindow { return NO; }

- (void)cancelOperation:(id)sender {
    // Esc key pressed — dismiss.
    goQuickChatClosed();
}
@end

static YansuQuickChatPanel *_quickChatPanel = nil;
// Opaque observer returned by NSNotificationCenter's block-based API. We
// must keep the reference so we can deregister it on close — passing the
// observed object to removeObserver: removes nothing.
static id _quickChatResignObserver = nil;

static void teardownQuickChatPanel(YansuQuickChatPanel *panel) {
    if (!panel) return;

    WKWebView *web = panel.webView;
    if (web) {
        WKUserContentController *uc = web.configuration.userContentController;
        [uc removeScriptMessageHandlerForName:@"quickChat"];
        [uc removeAllUserScripts];
        [web stopLoading];
        [web removeFromSuperview];
        panel.webView = nil;
    }
    panel.handler = nil;
    panel.localKeyMonitor = nil;
}

// Compute a frame centered on the main screen, slightly above center.
static NSRect quickChatTargetFrame(void) {
    NSScreen *screen = [NSScreen mainScreen];
    if (!screen) screen = [[NSScreen screens] firstObject];
    NSRect vf = screen.visibleFrame;
    CGFloat w = 560.0;
    CGFloat h = 360.0;
    CGFloat x = NSMidX(vf) - w / 2.0;
    // Bias slightly above true center so the panel isn't obscured by
    // the Dock or status-bar reflections.
    CGFloat y = NSMidY(vf) - h / 2.0 + vf.size.height * 0.10;
    return NSMakeRect(x, y, w, h);
}

// jsonEscapeString returns a JSON-encoded representation of s (including the
// surrounding double quotes) so it can be interpolated into a JS literal
// without breaking the script when s contains quotes, backslashes, or
// control characters.
static NSString *jsonEscapeString(NSString *s) {
    NSData *data = [NSJSONSerialization
        dataWithJSONObject:@[s ?: @""]
                   options:0
                     error:NULL];
    if (!data) return @"\"\"";
    NSString *arr = [[[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding] autorelease];
    if (arr.length < 2) return @"\"\"";
    // Strip the leading "[" and trailing "]" to get just the encoded string.
    return [arr substringWithRange:NSMakeRange(1, arr.length - 2)];
}

// Show the panel with the given bundleID as context. If the panel
// already exists, reposition + focus it instead of recreating.
//
// cHTML is the panel's full HTML document. It is supplied by the Go
// side (via go:embed) so the markup, styles, and JS bridge live in a
// single shared asset that the Windows port can reuse.
void goShowQuickChatWindow(const char *cBundleID, const char *cHTML) {
    NSString *bundleID = cBundleID ? [NSString stringWithUTF8String:cBundleID] : @"";
    NSString *htmlDoc  = cHTML     ? [NSString stringWithUTF8String:cHTML]     : @"";
    dispatch_async(dispatch_get_main_queue(), ^{
        NSString *bidLiteral = jsonEscapeString(bundleID);
        if (_quickChatPanel) {
            // Already open — just refresh and refocus.
            [_quickChatPanel setFrame:quickChatTargetFrame() display:YES];
            [_quickChatPanel orderFrontRegardless];
            [_quickChatPanel makeKeyWindow];
            NSString *js = [NSString stringWithFormat:
                @"window.__quickChatBundleID=%@;"
                 "window.__quickChatLoading && window.__quickChatLoading();"
                 "window.__quickChatFocus && window.__quickChatFocus();"
                 "window.webkit.messageHandlers.quickChat.postMessage({type:'request-suggestions',bundleID:%@});",
                bidLiteral, bidLiteral];
            [_quickChatPanel.webView evaluateJavaScript:js completionHandler:nil];
            return;
        }

        NSRect frame = quickChatTargetFrame();
        NSUInteger style = NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel;
        YansuQuickChatPanel *panel = [[YansuQuickChatPanel alloc]
            initWithContentRect:frame
            styleMask:style
            backing:NSBackingStoreBuffered
            defer:NO];
        panel.opaque             = NO;
        panel.backgroundColor    = [NSColor clearColor];
        panel.hasShadow          = YES;
        panel.level              = NSStatusWindowLevel;
        panel.movableByWindowBackground = YES;
        panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces
                                 | NSWindowCollectionBehaviorFullScreenAuxiliary
                                 | NSWindowCollectionBehaviorTransient
                                 | NSWindowCollectionBehaviorIgnoresCycle;
        panel.releasedWhenClosed = NO;
        panel.hidesOnDeactivate  = NO;

        WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
        WKUserContentController *uc    = [[WKUserContentController alloc] init];
        YansuQuickChatMessageHandler *h = [[YansuQuickChatMessageHandler alloc] init];
        [uc addScriptMessageHandler:h name:@"quickChat"];
        config.userContentController = uc;
        config.websiteDataStore      = [WKWebsiteDataStore nonPersistentDataStore];

        WKWebView *web = [[WKWebView alloc]
            initWithFrame:panel.contentView.bounds
            configuration:config];
        web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
        web.wantsLayer       = YES;
        web.layer.backgroundColor = [[NSColor clearColor] CGColor];
        @try { [web setValue:@NO forKey:@"drawsBackground"]; }
        @catch (NSException *e) { }
        web.allowsBackForwardNavigationGestures = NO;

        panel.webView = web;
        panel.handler = h;
        [panel.contentView addSubview:web];

        // Inject initial bundle ID before loading the HTML.
        NSString *preScript = [NSString stringWithFormat:
            @"window.__quickChatBundleID = %@;", bidLiteral];
        WKUserScript *us = [[WKUserScript alloc] initWithSource:preScript
            injectionTime:WKUserScriptInjectionTimeAtDocumentStart
            forMainFrameOnly:YES];
        [uc addUserScript:us];
        [us release];

        [web loadHTMLString:htmlDoc baseURL:nil];

        _quickChatPanel = panel;
        [web release];
        [h release];
        [uc release];
        [config release];

        // Dismiss when the window loses key status (user clicked
        // another app). We add this AFTER ordering front so we don't
        // immediately fire on the initial makeKey call. The block-based
        // API returns an opaque observer object we must retain and pass
        // to removeObserver: in goCloseQuickChatWindow — otherwise the
        // observer leaks and accumulates across open/close cycles.
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.15 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), ^{
            if (_quickChatPanel) {
                if (_quickChatResignObserver) {
                    [[NSNotificationCenter defaultCenter]
                        removeObserver:_quickChatResignObserver];
                    [_quickChatResignObserver release];
                }
                _quickChatResignObserver = [[NSNotificationCenter defaultCenter]
                    addObserverForName:NSWindowDidResignKeyNotification
                    object:_quickChatPanel
                    queue:[NSOperationQueue mainQueue]
                    usingBlock:^(NSNotification * _Nonnull note) {
                        goQuickChatClosed();
                    }];
                [_quickChatResignObserver retain];
            }
        });

        [panel orderFrontRegardless];
        [panel makeKeyWindow];
    });
}

void goCloseQuickChatWindow(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (_quickChatResignObserver) {
            [[NSNotificationCenter defaultCenter]
                removeObserver:_quickChatResignObserver];
            [_quickChatResignObserver release];
            _quickChatResignObserver = nil;
        }
        if (!_quickChatPanel) return;
        YansuQuickChatPanel *panel = _quickChatPanel;
        _quickChatPanel = nil;
        teardownQuickChatPanel(panel);
        [panel orderOut:nil];
        [panel close];
        [panel release];
    });
}

void goPushQuickChatSuggestions(const char *cJSON, const char *cLabel) {
    NSString *json  = cJSON  ? [NSString stringWithUTF8String:cJSON]  : @"[]";
    NSString *label = cLabel ? [NSString stringWithUTF8String:cLabel] : @"";
    dispatch_async(dispatch_get_main_queue(), ^{
        if (!_quickChatPanel) return;
        NSString *labelLiteral = jsonEscapeString(label);
        NSString *js = [NSString stringWithFormat:
            @"window.__quickChatSetLabel && window.__quickChatSetLabel(%@);"
             "window.__quickChatSetSuggestions && window.__quickChatSetSuggestions(%@);",
             labelLiteral, json];
        [_quickChatPanel.webView evaluateJavaScript:js completionHandler:nil];
    });
}

// goPushQuickChatLoading sets the panel label and re-applies the skeleton
// while the per-app suggestion fetch is in flight, so the user sees what
// app the chips will be about instead of an unlabeled "Suggestions" header.
void goPushQuickChatLoading(const char *cLabel) {
    NSString *label = cLabel ? [NSString stringWithUTF8String:cLabel] : @"";
    dispatch_async(dispatch_get_main_queue(), ^{
        if (!_quickChatPanel) return;
        NSString *labelLiteral = jsonEscapeString(label);
        NSString *js = [NSString stringWithFormat:
            @"window.__quickChatSetLabel && window.__quickChatSetLabel(%@);"
             "window.__quickChatLoading && window.__quickChatLoading();",
             labelLiteral];
        [_quickChatPanel.webView evaluateJavaScript:js completionHandler:nil];
    });
}
*/
import "C"

import (
	"encoding/json"
	"unsafe"
)

// ShowQuickChatWindow opens the separate floating panel with the given
// bundle ID as context. Safe to call from any goroutine; the actual
// Cocoa work is dispatched to the main queue.
func (a *App) ShowQuickChatWindow(bundleID string) {
	if chatFeatureErr() != nil {
		return
	}
	quickChatWindowMu.Lock()
	quickChatWindowApp = a
	quickChatWindowOpen = true
	lastQuickChatBundleID = bundleID
	quickChatWindowMu.Unlock()

	cBundleID := C.CString(bundleID)
	defer C.free(unsafe.Pointer(cBundleID))
	cHTML := C.CString(quickChatHTMLDoc)
	defer C.free(unsafe.Pointer(cHTML))
	C.goShowQuickChatWindow(cBundleID, cHTML)
}

// CloseQuickChatWindow tears down the floating panel if it's open.
func (a *App) CloseQuickChatWindow() {
	quickChatWindowMu.Lock()
	quickChatWindowOpen = false
	quickChatWindowMu.Unlock()
	C.goCloseQuickChatWindow()
}

// pushQuickChatSuggestions sends a fresh list of suggestions to the JS
// side of the floating panel along with an optional label override.
func (a *App) pushQuickChatSuggestions(suggestions []RecommendedQuestion, label string) {
	data, err := json.Marshal(suggestions)
	if err != nil {
		return
	}
	cJSON := C.CString(string(data))
	defer C.free(unsafe.Pointer(cJSON))
	cLabel := C.CString(label)
	defer C.free(unsafe.Pointer(cLabel))
	C.goPushQuickChatSuggestions(cJSON, cLabel)
}

// pushQuickChatLoading paints the skeleton state with a context-aware label
// (e.g. "Reading Discord…") while the suggestion fetch runs, so the user
// has feedback that we're tailoring to the current app.
func (a *App) pushQuickChatLoading(label string) {
	cLabel := C.CString(label)
	defer C.free(unsafe.Pointer(cLabel))
	C.goPushQuickChatLoading(cLabel)
}
