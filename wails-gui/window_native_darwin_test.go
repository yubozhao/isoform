//go:build darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Compile the actual monitor callbacks and geometry helpers into a headless
// Cocoa harness. No NSApplication, NSWindow, AX permission or capture is opened.
func TestWindowMonitorNativeGuards(t *testing.T) {
	source, err := os.ReadFile("internal/platform/activitymon/monitor_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	_, preamble, ok := strings.Cut(string(source), "/*\n")
	if !ok {
		t.Fatal("missing monitor cgo preamble")
	}
	preamble, _, ok = strings.Cut(preamble, "\n*/")
	if !ok {
		t.Fatal("unterminated monitor cgo preamble")
	}
	menubar, err := os.ReadFile("menubar_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	_, restore, ok := strings.Cut(string(menubar), "static void setActivationPolicyRegular(void) {")
	if !ok {
		t.Fatal("missing main window restore function")
	}
	restore, _, ok = strings.Cut(restore, "static void setActivationPolicyAccessory(void)")
	if !ok {
		t.Fatal("missing main window restore boundary")
	}
	restore = "static void setActivationPolicyRegular(void) {" + restore
	var lines []string
	for _, line := range strings.Split(preamble, "\n") {
		if !strings.HasPrefix(line, "#cgo ") {
			lines = append(lines, line)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "native.m")
	harness := strings.Replace(windowNativeHarness, "/*__RESTORE_MAIN_WINDOW__*/", restore, 1)
	if err := os.WriteFile(path, []byte(windowAXFixtures+strings.Join(lines, "\n")+harness), 0600); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	bin := filepath.Join(dir, "native")
	cmd := exec.CommandContext(ctx, "/usr/bin/clang", "-x", "objective-c", "-fblocks", "-I", wd,
		"-I", filepath.Join(wd, "internal/platform/activitymon"), path, filepath.Join(wd, "internal/platform/activitymon/main_queue_darwin.m"), "-o", bin,
		"-framework", "Cocoa", "-framework", "ApplicationServices", "-framework", "Carbon")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile native harness: %v\n%s", err, out)
	}
	for _, name := range []string{"CapturePermission", "GeometryAndCallbacks", "MainWindowRestore", "FocusedWindowType", "TitleLabelType", "DescriptionLabelType", "HelpLabelType", "PlaceholderLabelType"} {
		t.Run(name, func(t *testing.T) {
			if out, err := exec.CommandContext(ctx, bin, name).CombinedOutput(); err != nil {
				t.Fatalf("native guards: %v\n%s", err, out)
			}
		})
	}
}

// Unit fixtures intercept AX attribute transport, never query another process,
// and feed real retained CoreFoundation objects to the production callers.
// Cocoa headers are imported before macros so SDK declarations stay untouched.
const windowAXFixtures = `
static int captureAllowed = 1;
int activityMonitorCaptureAllowed(void) { return captureAllowed; }
#import <Cocoa/Cocoa.h>
#include <ApplicationServices/ApplicationServices.h>
#include <assert.h>
static CFMutableDictionaryRef guardAttributes;
static int guardTitleQueries;
static AXError guardCopyAttributeValue(AXUIElementRef element, CFStringRef attribute, CFTypeRef *value) {
    assert(element && CFGetTypeID(element) == AXUIElementGetTypeID());
    if (CFEqual(attribute, kAXTitleAttribute)) guardTitleQueries++;
    *value = guardAttributes ? CFDictionaryGetValue(guardAttributes, attribute) : NULL;
    if (!*value) return kAXErrorNoValue;
    CFRetain(*value);
    return kAXErrorSuccess;
}
static AXError guardSetMessagingTimeout(AXUIElementRef element, float seconds) {
    assert(element && CFGetTypeID(element) == AXUIElementGetTypeID());
    return kAXErrorSuccess;
}
#define AXUIElementCopyAttributeValue guardCopyAttributeValue
#define AXUIElementSetMessagingTimeout guardSetMessagingTimeout
`

const windowNativeHarness = `
#include "window_state_darwin.h"
#include "capture_geometry_darwin.h"
#include <assert.h>

@interface GuardScreen : NSObject
@end
@implementation GuardScreen
- (NSRect)visibleFrame { assert([NSThread isMainThread]); return NSMakeRect(100, 50, 1600, 1000); }
@end

@interface GuardWindow : NSObject {
@public
    id testScreen;
    NSRect testFrame;
    BOOL testMini, testZoom;
    int testRestoreCount;
}
@end
@implementation GuardWindow
- (BOOL)isMiniaturized { assert([NSThread isMainThread]); return testMini; }
- (BOOL)isZoomed { assert([NSThread isMainThread]); return testZoom; }
- (NSScreen *)screen { assert([NSThread isMainThread]); return testScreen; }
- (NSRect)frame { assert([NSThread isMainThread]); return testFrame; }
- (void)makeKeyAndOrderFront:(id)sender { assert([NSThread isMainThread]); testRestoreCount++; }
@end

@interface WailsWindow : GuardWindow
@end
@implementation WailsWindow
@end

@interface GuardApplication : NSObject {
@public
    NSArray *testWindows;
    id testKeyWindow;
}
@end
@implementation GuardApplication
- (NSArray *)windows { assert([NSThread isMainThread]); return testWindows; }
- (NSWindow *)keyWindow { return testKeyWindow; }
- (NSWindow *)mainWindow { return nil; }
- (void)setActivationPolicy:(NSApplicationActivationPolicy)policy { assert([NSThread isMainThread]); }
- (void)activate { assert([NSThread isMainThread]); }
- (void)activateIgnoringOtherApps:(BOOL)ignore { assert([NSThread isMainThread]); }
@end

/*__RESTORE_MAIN_WINDOW__*/

static void drain_main_queue(void) {
    __block BOOL done = NO;
    dispatch_async(dispatch_get_main_queue(), ^{ done = YES; });
    CFAbsoluteTime deadline = CFAbsoluteTimeGetCurrent() + 5;
    while (!done) {
        assert(CFAbsoluteTimeGetCurrent() < deadline);
        CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.01, true);
    }
}

static void check_main_window_restore(void) {
    GuardApplication *app = [GuardApplication new];
    GuardWindow *panel = [GuardWindow new];
    GuardWindow *mainWindow = [WailsWindow new];
    app->testWindows = @[panel, mainWindow];
    app->testKeyWindow = panel;
    NSApp = (NSApplication *)app;
    setActivationPolicyRegular();
    drain_main_queue();
    assert(mainWindow->testRestoreCount == 1 && panel->testRestoreCount == 0);
    // No key/main window while inactive: the first auxiliary still must
    // not be used. No Wails window during startup: restore nothing.
    app->testKeyWindow = nil;
    setActivationPolicyRegular();
    drain_main_queue();
    assert(mainWindow->testRestoreCount == 2 && panel->testRestoreCount == 0);
    app->testWindows = @[panel];
    setActivationPolicyRegular();
    drain_main_queue();
    assert(mainWindow->testRestoreCount == 2 && panel->testRestoreCount == 0);
    NSApp = nil;
    [mainWindow release]; [panel release]; [app release];
}

static void assert_label(AXUIElementRef element, CFStringRef expected) {
    CFStringRef result = copy_element_label(element);
    if (expected) {
        assert(result && CFGetTypeID(result) == CFStringGetTypeID());
        assert(CFEqual(result, expected));
    } else {
        assert(!result);
    }
    if (result) CFRelease(result);
}

static void check_ax_caller(const char *name) {
    guardAttributes = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    // Construct a local AX object without messaging any app or requiring AX
    // access. All attribute/timeout calls above use deterministic unit fixtures.
    AXUIElementRef element = AXUIElementCreateApplication(getpid());
    const void *item = kCFBooleanTrue;
    CFArrayRef wrongType = CFArrayCreate(NULL, &item, 1, &kCFTypeArrayCallBacks);
    if (strcmp(name, "FocusedWindowType") == 0) {
        char title[64] = "";
        CFDictionarySetValue(guardAttributes, kAXFocusedWindowAttribute, wrongType);
        CFDictionarySetValue(guardAttributes, kAXTitleAttribute, CFSTR("valid title"));
        get_window_title(getpid(), title, sizeof(title));
        assert(title[0] == 0 && guardTitleQueries == 0);
        CFDictionarySetValue(guardAttributes, kAXFocusedWindowAttribute, element);
        get_window_title(getpid(), title, sizeof(title));
        assert(strcmp(title, "valid title") == 0 && guardTitleQueries == 1);
    } else {
        CFStringRef attribute = NULL, fallback = NULL;
        if (strcmp(name, "TitleLabelType") == 0) {
            attribute = kAXTitleAttribute; fallback = kAXDescriptionAttribute;
        } else if (strcmp(name, "DescriptionLabelType") == 0) {
            attribute = kAXDescriptionAttribute; fallback = kAXValueAttribute;
        } else if (strcmp(name, "HelpLabelType") == 0) {
            attribute = kAXHelpAttribute; fallback = kAXPlaceholderValueAttribute;
        } else {
            assert(strcmp(name, "PlaceholderLabelType") == 0);
            attribute = kAXPlaceholderValueAttribute;
        }
        // Wrong type must fall through to the next valid label, or to NULL
        // for the final attribute. Check the returned type before CFString use.
        CFDictionarySetValue(guardAttributes, attribute, wrongType);
        if (fallback) CFDictionarySetValue(guardAttributes, fallback, CFSTR("fallback"));
        assert_label(element, fallback ? CFSTR("fallback") : NULL);
        // Each corresponding valid control must still win over the fallback.
        CFDictionarySetValue(guardAttributes, attribute, CFSTR("primary"));
        assert_label(element, CFSTR("primary"));
        CFDictionarySetValue(guardAttributes, attribute, CFSTR(""));
        assert_label(element, fallback ? CFSTR("fallback") : NULL);
    }
    CFRelease(wrongType);
    CFRelease(element);
    CFRelease(guardAttributes);
    guardAttributes = NULL;
}

int main(int argc, char **argv) {
    @autoreleasepool {
        assert(argc == 2);
        if (strcmp(argv[1], "CapturePermission") == 0) {
            _monitorRunning = true;
            CGEventRef key = CGEventCreateKeyboardEvent(NULL, 0, true);
            assert(key);
            CGEventSetIntegerValueField(key, kCGEventTargetUnixProcessID, getpid());
            CActivityEvent captured;
            captureAllowed = 0;
            processTapEvent(kCGEventKeyDown, key, 100);
            assert(!pop_event(&captured) && _lastKeydownMs == 0);
            captureAllowed = 1;
            processTapEvent(kCGEventKeyDown, key, 101);
            assert(pop_event(&captured) && captured.timestampMs == 101);
            assert(strcmp(captured.kind, "keyboard") == 0);
            CFRelease(key);
            return 0;
        }
        if (strcmp(argv[1], "MainWindowRestore") == 0) {
            check_main_window_restore();
            return 0;
        }
        if (strcmp(argv[1], "GeometryAndCallbacks") != 0) {
            check_ax_caller(argv[1]);
            return 0;
        }
        // The real observer callback must tolerate missing/invalid elements
        // without retaining NULL, dispatching work or touching another app.
        axFocusCallback(NULL, NULL, NULL, NULL);
        axFocusCallback(NULL, (AXUIElementRef)kCFBooleanFalse, NULL, NULL);
        assert(_eventQueueHead == _eventQueueTail);
        char out[32] = "stale";
        assert(!ax_copy_string(NULL, out, sizeof(out)) && out[0] == 0);
        assert(!ax_copy_string(kCFBooleanTrue, out, sizeof(out)) && out[0] == 0);
        assert(ax_copy_string(CFSTR("window"), out, sizeof(out)) && strcmp(out, "window") == 0);
        assert(!ax_copy_string(CFSTR("too long"), out, 2) && out[0] == 0);
        assert(!ax_copy_string(CFSTR("window"), NULL, 0));

        assert(!isoformWindowStateForWindow(nil).valid);
        GuardWindow *window = [WailsWindow new];
        assert(!isoformWindowStateForWindow((NSWindow *)window).valid);
        GuardScreen *screen = [GuardScreen new];
        window->testScreen = screen;
        window->testFrame = NSMakeRect(150, 250, 800, 600);
        window->testZoom = YES;
        IsoformWindowState state = isoformWindowStateForWindow((NSWindow *)window);
        assert(state.valid && state.x == 50 && state.y == 200);
        assert(state.width == 800 && state.height == 600 && state.maximised);
        window->testMini = YES;
        assert(!isoformWindowStateForWindow((NSWindow *)window).valid);
        window->testMini = NO;
        window->testFrame.origin.x = NAN;
        assert(!isoformWindowStateForWindow((NSWindow *)window).valid);
        window->testScreen = nil;
        assert(!isoformWindowStateForWindow((NSWindow *)window).valid);
        assert(!isoformReadWindowState().valid);

        // A fake application and window exercise actual selection/dispatch
        // without instantiating NSApplication or NSWindow. Skip other panels.
        GuardApplication *app = [GuardApplication new];
        NSObject *panel = [NSObject new];
        window->testFrame = NSMakeRect(150, 250, 800, 600);
        window->testScreen = screen;
        app->testWindows = @[panel, window];
        NSApp = (NSApplication *)app;
        assert(isoformMainWindow() == (NSWindow *)window);
        // An auxiliary window can precede the hidden main window, or be
        // the only window during startup. Neither case may restore it.
        app->testWindows = @[panel];
        assert(isoformMainWindow() == nil);
        app->testWindows = @[];
        assert(isoformMainWindow() == nil);
        app->testWindows = @[panel, window];
        state = isoformReadWindowState();
        assert(state.valid && state.x == 50 && state.y == 200 && state.maximised);
        dispatch_semaphore_t done = dispatch_semaphore_create(0);
        dispatch_async(dispatch_get_global_queue(QOS_CLASS_UTILITY, 0), ^{
            IsoformWindowState background = isoformReadWindowState();
            assert(background.valid && background.width == 800 && background.height == 600);
            dispatch_semaphore_signal(done);
        });
        CFAbsoluteTime deadline = CFAbsoluteTimeGetCurrent() + 5;
        while (dispatch_semaphore_wait(done, DISPATCH_TIME_NOW) != 0) {
            assert(CFAbsoluteTimeGetCurrent() < deadline);
            CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.01, true);
        }
        dispatch_release(done);
        NSApp = nil;
        [app release];
        [panel release];
        [screen release];
        [window release];

        int w = 0, h = 0;
        assert(capture_pixel_dimensions(800, 600, 2, &w, &h) && w == 1600 && h == 1200);
        assert(capture_pixel_dimensions(800, 600, 1, &w, &h) && w == 800 && h == 600);
        assert(!capture_pixel_dimensions(0, 600, 2, &w, &h));
        assert(!capture_pixel_dimensions(-1, 600, 2, &w, &h));
        assert(!capture_pixel_dimensions(INFINITY, 600, 2, &w, &h));
        assert(!capture_pixel_dimensions(800, 600, NAN, &w, &h));
        assert(!capture_pixel_dimensions(INT_MAX, 600, 2, &w, &h));
    }
    return 0;
}
`
