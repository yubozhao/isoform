//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include "main_window_darwin.h"

static WKWebView *isoformFindWebView(NSView *view) {
    if (!view) return nil;
    if ([view isKindOfClass:[WKWebView class]]) return (WKWebView *)view;
    for (NSView *sub in view.subviews) {
        WKWebView *found = isoformFindWebView(sub);
        if (found) return found;
    }
    return nil;
}

// Sets the main window WebView's page zoom (macOS 11 and later). Returns 1
// when the WebView was found and zoomed, 0 otherwise. AppKit work runs on the
// main thread; the caller waits up to a second for it, never with
// dispatch_sync, so a busy or absent run loop (go test) cannot deadlock it.
static int isoformSetMainWebViewZoom(double zoom) {
    if (NSApp == nil || ![NSApp isRunning]) return 0;
    __block int ok = 0;
    void (^apply)(void) = ^{
        NSWindow *win = isoformMainWindow();
        WKWebView *web = isoformFindWebView(win.contentView);
        if (!web) return;
        if (@available(macOS 11.0, *)) {
            web.pageZoom = zoom;
            ok = 1;
        }
    };
    if ([NSThread isMainThread]) {
        apply();
        return ok;
    }
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    dispatch_async(dispatch_get_main_queue(), ^{
        apply();
        dispatch_semaphore_signal(done);
    });
    if (dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, (int64_t)NSEC_PER_SEC)) != 0) return 0;
    return ok;
}
*/
import "C"

func setMainWebViewZoom(zoom float64) bool {
	return C.isoformSetMainWebViewZoom(C.double(zoom)) == 1
}
