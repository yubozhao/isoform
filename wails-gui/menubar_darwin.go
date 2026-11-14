//go:build darwin

package main

/*
#cgo LDFLAGS: -framework Cocoa -framework ServiceManagement

#import <Cocoa/Cocoa.h>
#import <ServiceManagement/ServiceManagement.h>
#include <objc/runtime.h>
#include <string.h>
#include "main_window_darwin.h"
#include "menubar_popover_darwin.h"
#include "setup_window_darwin.h"

// Forward declarations for Go callbacks.
extern void goMenuBarInitStep(char *step);
extern void goMenuBarInitResult(int attempt, char *error);
extern void goMenuBarOpen(void);
extern void goMenuBarOpenActivity(void);
extern void goMenuBarToggleActivity(void);
extern void goMenuBarToggleClosedEyes(void);
extern void goMenuBarQuit(void);
extern void goMenuBarSetLoginItem(_Bool enabled);

// ── Action handlers (plain C functions used as ObjC method IMPs) ──────

static void actionOpenApp(id self, SEL _cmd, id sender) {
	goMenuBarOpen();
}

static void actionOpenActivity(id self, SEL _cmd, id sender) {
	goMenuBarOpenActivity();
}

static void actionToggleActivity(id self, SEL _cmd, id sender) {
	goMenuBarToggleActivity();
}

static void actionToggleClosedEyes(id self, SEL _cmd, id sender) {
	goMenuBarToggleClosedEyes();
}

static void actionToggleLoginItem(id self, SEL _cmd, id sender) {
	NSMenuItem *item = (NSMenuItem *)sender;
	_Bool next = (item.state == NSControlStateValueOff);
	goMenuBarSetLoginItem(next);
}

static void actionQuitApp(id self, SEL _cmd, id sender) {
	goMenuBarQuit();
}

// Forward declarations.
static const char *_menubarSetup(NSArray<NSData *> *iconCopies);
static void installWindowCloseSwizzle(void);
static void installReopenSwizzle(void);
static int loginItemStatusSM(void);

// ── Runtime-registered delegate class (avoids duplicate symbol issues) ─

static Class _menuBarDelegateClass = NULL;

static void ensureMenuBarDelegateClass(void) {
	if (_menuBarDelegateClass) return;

	_menuBarDelegateClass = objc_allocateClassPair([NSObject class], "YansuMenuBarDelegate2", 0);
	if (!_menuBarDelegateClass) {
		_menuBarDelegateClass = objc_getClass("YansuMenuBarDelegate2");
		return;
	}

	class_addMethod(_menuBarDelegateClass, sel_registerName("openApp:"),
		(IMP)actionOpenApp, "v@:@");
	class_addMethod(_menuBarDelegateClass, sel_registerName("openActivity:"),
		(IMP)actionOpenActivity, "v@:@");
	class_addMethod(_menuBarDelegateClass, sel_registerName("toggleActivity:"),
		(IMP)actionToggleActivity, "v@:@");
	class_addMethod(_menuBarDelegateClass, sel_registerName("toggleClosedEyes:"),
		(IMP)actionToggleClosedEyes, "v@:@");
	class_addMethod(_menuBarDelegateClass, sel_registerName("toggleLoginItem:"),
		(IMP)actionToggleLoginItem, "v@:@");
	class_addMethod(_menuBarDelegateClass, sel_registerName("quitApp:"),
		(IMP)actionQuitApp, "v@:@");

	objc_registerClassPair(_menuBarDelegateClass);
}

// ── Static state ──────────────────────────────────────────────────────

static NSStatusItem *_statusItem = nil;
static NSMenuItem *_closedEyesItem = nil;
// The "doing now" line at the top of the menu, and the last text Go sent
// for it, kept so a line sent before the menu exists is not lost.
static NSMenuItem *_doingNowItem = nil;
static NSString *_doingNowTitle = nil;
static id _delegate = nil;
static IMP _originalWindowShouldClose = NULL;
static BOOL _windowCloseSwizzleInstalled = NO;
static BOOL _menuBarServicesReady = NO;

// ── Init / teardown ───────────────────────────────────────────────────

static void applyMenuBarStatus(int status) {
    // User-visible state is supplied by the localized Go projection.
    (void)status;
}

static void applyMenuBarLoginItemChecked(_Bool checked) {
	(void)checked;
}

static void applyMenuBarClosedEyesMode(_Bool enabled, const char *title) {
	if (!_closedEyesItem) {
		return;
	}
	_closedEyesItem.state = enabled ? NSControlStateValueOn : NSControlStateValueOff;
	if (title) {
		_closedEyesItem.title = [NSString stringWithUTF8String:title];
	}
}

// _menubarSetup creates the NSStatusItem + menu. Must be called on the
// main thread. Extracted so it can be invoked from multiple dispatch paths.
static const char *_menubarSetup(NSArray<NSData *> *iconCopies) {
	if (_statusItem) return NULL;
	goMenuBarInitStep("main queue entered");
	ensureMenuBarDelegateClass();
	_delegate = [[_menuBarDelegateClass alloc] init];
	if (!_delegate) return "delegate creation failed";
	goMenuBarInitStep("delegate created");

	_statusItem = [[[NSStatusBar systemStatusBar]
		statusItemWithLength:NSVariableStatusItemLength] retain];
	if (!_statusItem || !_statusItem.button) {
		if (_statusItem) [[NSStatusBar systemStatusBar] removeStatusItem:_statusItem];
		[_statusItem release]; _statusItem = nil;
		[_delegate release]; _delegate = nil;
		return "status item or button creation failed";
	}
	// Hidden until onboarding is finished or watching has started (setup_window.go).
	_statusItem.visible = isoformSetupMarkStartsVisible() ? YES : NO;
	goMenuBarInitStep("status item and button created");

	// Load icon image (used as template for menu bar appearance).
	NSImage *img = [[NSImage alloc] initWithSize:NSMakeSize(18, 18)];
	for (NSData *data in iconCopies) {
		NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithData:data];
		if (rep) {
			// All pixel densities represent the same 18-point template.
			[rep setSize:NSMakeSize(18, 18)];
			[img addRepresentation:rep];
			[rep release];
		}
	}
	if (img.representations.count == 0) {
		[img release];
		img = nil;
	} else {
		[img setTemplate:YES];
	}

	// Set icon or fall back to text title.
	if (img) {
		_statusItem.button.image = img;
	} else {
		_statusItem.button.title = @"Y";
	}

	// Build menu. Explicit retain since CGO compiles without ARC.
	NSMenu *menu = [[NSMenu alloc] init];
	[menu setAutoenablesItems:NO];

	// What Isoform is doing right now; choosing it opens the Activity log.
	_doingNowItem = [menu addItemWithTitle:(_doingNowTitle ? _doingNowTitle : @"Isoform")
		action:sel_registerName("openActivity:") keyEquivalent:@""];
	_doingNowItem.target = _delegate;
	_doingNowItem.enabled = _menuBarServicesReady;

	[menu addItem:[NSMenuItem separatorItem]];

	[menu addItemWithTitle:@"Open Isoform"
		action:sel_registerName("openApp:") keyEquivalent:@"i"];
	[[menu itemArray].lastObject setKeyEquivalentModifierMask:NSEventModifierFlagCommand | NSEventModifierFlagShift];
	[[menu itemArray].lastObject setTarget:_delegate];

	_closedEyesItem = [menu addItemWithTitle:@"Pause observing for 1 hour"
		action:sel_registerName("toggleClosedEyes:") keyEquivalent:@""];
	_closedEyesItem.target = _delegate;
	_closedEyesItem.enabled = _menuBarServicesReady;

	[menu addItem:[NSMenuItem separatorItem]];

	[menu addItemWithTitle:@"Quit Isoform"
		action:sel_registerName("quitApp:") keyEquivalent:@""];
	[[menu itemArray].lastObject setTarget:_delegate];

	[_statusItem setMenu:menu];
	goMenuBarInitStep("native menu created");
	if (!isoformMenuBarAttach(_statusItem)) {
		[menu release];
		[[NSStatusBar systemStatusBar] removeStatusItem:_statusItem];
		[_statusItem release]; _statusItem = nil;
		[_delegate release]; _delegate = nil;
		_closedEyesItem = nil; _doingNowItem = nil;
		[img release];
		return "button action attachment failed";
	}
	goMenuBarInitStep("button action attached");
	isoformSetupMarkItemSet(_statusItem);
	[menu release];
	applyMenuBarStatus(3);
	applyMenuBarClosedEyesMode(false, NULL);

	if (img) {
		[img release];
	}
	return NULL;
}

static void menubarInit(int attempt, const void *icon1, int len1, const void *icon2, int len2, const void *icon3, int len3) {
	NSLog(@"[menubar-objc] menubarInit called, icon bytes=%d, isMainThread=%d",
		len1 + len2 + len3, [NSThread isMainThread]);

	NSMutableArray<NSData *> *iconCopies = [[NSMutableArray alloc] initWithCapacity:3];
	const void *icons[] = {icon1, icon2, icon3};
	int lengths[] = {len1, len2, len3};
	for (int i = 0; i < 3; i++) {
		if (icons[i] && lengths[i] > 0) {
			NSData *data = [[NSData alloc] initWithBytes:icons[i] length:lengths[i]];
			[iconCopies addObject:data];
			[data release];
		}
	}

	dispatch_async(dispatch_get_main_queue(), ^{
		const char *error = NULL;
		@try {
			error = _menubarSetup(iconCopies);
		} @catch (NSException *exception) {
			NSLog(@"[menubar-objc] setup exception: %@", exception);
			isoformMenuBarDetach();
			isoformSetupMarkItemSet(NULL);
			if (_statusItem) [[NSStatusBar systemStatusBar] removeStatusItem:_statusItem];
			[_statusItem release]; _statusItem = nil;
			[_delegate release]; _delegate = nil;
			_closedEyesItem = nil; _doingNowItem = nil;
			error = "native setup exception (see stderr log)";
		}
		[iconCopies release];
		goMenuBarInitResult(attempt, (char *)error);
		if (error) return;

	});
}

static void menubarServicesReady(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		_menuBarServicesReady = YES;
		_doingNowItem.enabled = YES;
		_closedEyesItem.enabled = YES;
	});
	installWindowCloseSwizzle();
	installReopenSwizzle();
}

static void menubarCleanup(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		isoformMenuBarDetach();
		isoformSetupMarkItemSet(NULL);
		if (_statusItem) {
			[[NSStatusBar systemStatusBar] removeStatusItem:_statusItem];
			[_statusItem release];
			_statusItem = nil;
		}
		_closedEyesItem = nil;
		_doingNowItem = nil;
		[_delegate release];
		_delegate = nil;
	});
}

// ── Status updates ────────────────────────────────────────────────────

static void menubarSetStatus(int status) {
	dispatch_async(dispatch_get_main_queue(), ^{
		applyMenuBarStatus(status);
	});
}

static void menubarSetLoginItemChecked(_Bool checked) {
	dispatch_async(dispatch_get_main_queue(), ^{
		applyMenuBarLoginItemChecked(checked);
	});
}

static void menubarSetDoingNow(const char *title) {
	// Copied for the same reason as in menubarSetClosedEyesMode: Go frees
	// its CString as soon as this returns.
	if (!title) {
		return;
	}
	size_t len = strlen(title);
	char *titleCopy = malloc(len + 1);
	memcpy(titleCopy, title, len + 1);
	dispatch_async(dispatch_get_main_queue(), ^{
		NSString *next = [[NSString alloc] initWithUTF8String:titleCopy];
		free(titleCopy);
		if (!next) {
			return;
		}
		[_doingNowTitle release];
		_doingNowTitle = next;
		if (_doingNowItem) {
			_doingNowItem.title = next;
		}
	});
}

static void menubarSetClosedEyesMode(_Bool enabled, const char *title) {
	// The block runs asynchronously, so we must copy the C string the
	// caller passed in — Go will free its CString as soon as the cgo
	// call returns.
	char *titleCopy = NULL;
	if (title) {
		size_t len = strlen(title);
		titleCopy = malloc(len + 1);
		memcpy(titleCopy, title, len + 1);
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		applyMenuBarClosedEyesMode(enabled, titleCopy);
		if (titleCopy) {
			free(titleCopy);
		}
	});
}

// ── Activation policy ─────────────────────────────────────────────────

static void setActivationPolicyRegular(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
		// macOS 14+ introduced Cooperative Activation. activateIgnoringOtherApps:
		// is deprecated and the system may bring the window forward without
		// granting key-window status — causing typed input to go to whichever
		// app was previously key. Prefer -[NSApp activate] on 14+, and fall
		// back to the legacy call for older systems.
		if (@available(macOS 14.0, *)) {
			[NSApp activate];
		} else {
			[NSApp activateIgnoringOtherApps:YES];
		}
		// Wails' WindowShow only does orderFront:, which does not confer key
		// status. Explicitly make the main window key so keyboard input is
		// routed to it.
		NSWindow *win = isoformMainWindow();
		[win makeKeyAndOrderFront:nil];
	});
}

static void setActivationPolicyAccessory(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	});
}

// ── Window close interception ─────────────────────────────────────────
// Swizzle the Wails window delegate's windowShouldClose: so that
// clicking the red close button hides the window but keeps the Dock
// icon visible. The menu bar icon also stays. Clicking the Dock icon
// (applicationShouldHandleReopen) brings the window back.

static BOOL yansuWindowShouldClose(id self, SEL _cmd, id window) {
	// Hide ONLY this window via orderOut — deliberately NOT delegating to Wails'
	// default HideWindowOnClose, which calls [NSApp hide:nil] (a whole-app hide).
	//
	// Root cause of the "window pops back after a background toast" bug: once the
	// app is in the [NSApp hide:] state, showing any toast NSWindow makes AppKit
	// un-hide the whole application and bring the main window back on-screen — no
	// explicit WindowShow is involved, and a non-activating toast panel could not
	// prevent it. orderOut hides just the window and leaves
	// the app un-hidden, so toasts no longer resurrect it. Wails' Show path
	// (makeKeyAndOrderFront) still restores the window normally.
	[(NSWindow *)window orderOut:nil];
	NSLog(@"[menubar-objc] window close intercepted: orderOut (dock icon stays)");
	return NO; // never let AppKit actually close/destroy the window
}

static void attemptInstallWindowCloseSwizzle(int remainingAttempts) {
	if (_windowCloseSwizzleInstalled) {
		return;
	}

	// Only patch the Wails main window's delegate. A system or WebKit
	// panel must retain its own close lifecycle.
	NSWindow *win = isoformMainWindow();
	if (!win || !win.delegate) {
		if (remainingAttempts > 0) {
			dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.25 * NSEC_PER_SEC)),
				dispatch_get_main_queue(), ^{
					attemptInstallWindowCloseSwizzle(remainingAttempts - 1);
				});
			return;
		}
		NSLog(@"[menubar-objc] window close swizzle: no window/delegate found");
		return;
	}

	Class cls = [win.delegate class];
	Method m = class_getInstanceMethod(cls, @selector(windowShouldClose:));
	if (!m) {
		if (remainingAttempts > 0) {
			dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.25 * NSEC_PER_SEC)),
				dispatch_get_main_queue(), ^{
					attemptInstallWindowCloseSwizzle(remainingAttempts - 1);
				});
			return;
		}
		NSLog(@"[menubar-objc] window close swizzle: method not found on %s", class_getName(cls));
		return;
	}

	IMP current = method_getImplementation(m);
	if (current == (IMP)yansuWindowShouldClose) {
		_windowCloseSwizzleInstalled = YES;
		return;
	}

	_originalWindowShouldClose = current;
	method_setImplementation(m, (IMP)yansuWindowShouldClose);
	_windowCloseSwizzleInstalled = YES;
	NSLog(@"[menubar-objc] windowShouldClose: swizzle installed on %s", class_getName(cls));
}

static void installWindowCloseSwizzle(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		attemptInstallWindowCloseSwizzle(40);
	});
}

// ── Dock icon click (reopen) ─────────────────────────────────────────
// When the user clicks the Dock icon while the window is hidden,
// macOS sends applicationShouldHandleReopen:hasVisibleWindows: to the
// app delegate. We swizzle it to bring the window back.

static BOOL yansuApplicationShouldHandleReopen(id self, SEL _cmd, id app, BOOL hasVisibleWindows) {
	if (!hasVisibleWindows) {
		goMenuBarOpen();
	}
	return YES;
}

static BOOL _reopenSwizzleInstalled = NO;

static void installReopenSwizzle(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		if (_reopenSwizzleInstalled) return;

		id delegate = [NSApp delegate];
		if (!delegate) {
			NSLog(@"[menubar-objc] reopen swizzle: no app delegate found");
			return;
		}

		Class cls = [delegate class];
		SEL sel = @selector(applicationShouldHandleReopen:hasVisibleWindows:);
		if (!class_addMethod(cls, sel, (IMP)yansuApplicationShouldHandleReopen, "B@:@B")) {
			Method m = class_getInstanceMethod(cls, sel);
			if (m) {
				method_setImplementation(m, (IMP)yansuApplicationShouldHandleReopen);
			}
		}
		_reopenSwizzleInstalled = YES;
		NSLog(@"[menubar-objc] applicationShouldHandleReopen: swizzle installed on %s", class_getName(cls));
	});
}

static void performRealQuit(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp terminate:nil];
	});
}

// ── Login item (SMAppService, macOS 13+) ──────────────────────────────

static _Bool registerLoginItemSM(void) {
	if (@available(macOS 13.0, *)) {
		SMAppService *service = [SMAppService mainAppService];
		NSError *error = nil;
		BOOL ok = [service registerAndReturnError:&error];
		if (!ok && error) {
			NSLog(@"[loginitem] register failed: %@", error);
		}
		return ok;
	}
	return false;
}

static _Bool unregisterLoginItemSM(void) {
	if (@available(macOS 13.0, *)) {
		SMAppService *service = [SMAppService mainAppService];
		NSError *error = nil;
		BOOL ok = [service unregisterAndReturnError:&error];
		if (!ok && error) {
			NSLog(@"[loginitem] unregister failed: %@", error);
		}
		return ok;
	}
	return false;
}

// Returns: 0=notRegistered, 1=enabled, 2=requiresApproval, 3=notFound
static int loginItemStatusSM(void) {
	if (@available(macOS 13.0, *)) {
		return (int)[SMAppService mainAppService].status;
	}
	return -1;
}

// SMAppService registers its own main bundle. Delegate to the installed
// executable rather than attempting to register another app from this process.
static char *loginItemBundleExecutable(const char *path) {
	@autoreleasepool {
		NSBundle *bundle = [NSBundle bundleWithPath:[NSString stringWithUTF8String:path]];
		const char *executable = [[bundle executablePath] fileSystemRepresentation];
		return executable ? strdup(executable) : NULL;
	}
}

// ── Launch detection ──────────────────────────────────────────────────
// No C-level detection needed; Go checks os.Args for --background flag.
*/
import "C"

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// menuBarApp is set during startup() so CGO callbacks can reach the App.
var menuBarApp *App

// menuBarActivityPaused tracks whether the user manually paused activity.
var menuBarActivityPaused bool

var menuBarInit struct {
	sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	results chan menuBarInitResult
}
var menuBarReady atomic.Bool

func initMenuBarEarly(app *App) { initMenuBar(app) }
func menuBarServicesReady(_ *App) {
	menuBarReady.Store(true)
	C.menubarServicesReady()
}

//export goMenuBarInitStep
func goMenuBarInitStep(step *C.char) {
	log.Printf("[menubar] %s", C.GoString(step))
}

//export goMenuBarInitResult
func goMenuBarInitResult(attempt C.int, message *C.char) {
	if message == nil {
		log.Printf("[menubar] main-thread setup completed (attempt %d)", int(attempt))
	}
	var err error
	if message != nil {
		err = fmt.Errorf("%s", C.GoString(message))
		log.Printf("[menubar] main-thread setup failed (attempt %d): %v", int(attempt), err)
	}
	menuBarInit.Lock()
	results := menuBarInit.results
	if results != nil {
		select {
		case results <- menuBarInitResult{attempt: int(attempt), err: err}:
		default:
		}
	}
	menuBarInit.Unlock()
}

// initMenuBar schedules AppKit work before storage and optional services start.
func initMenuBar(app *App) {
	menuBarApp = app
	var icons [3][]byte
	for i, name := range []string{"assets/menubar-icon.png", "assets/menubar-icon@2x.png", "assets/menubar-icon@3x.png"} {
		data, err := embeddedMenuBarIcons.ReadFile(name)
		if err != nil {
			log.Printf("[menubar] loading template %s: %v", name, err)
		}
		icons[i] = data
	}
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan menuBarInitResult, 3)
	done := make(chan struct{})
	menuBarInit.Lock()
	menuBarInit.cancel, menuBarInit.done, menuBarInit.results = cancel, done, results
	menuBarInit.Unlock()
	go func() {
		defer close(done)
		err := runMenuBarInit(ctx, func(attempt int) {
			var pointers [3]unsafe.Pointer
			for i, data := range icons {
				if len(data) > 0 {
					pointers[i] = unsafe.Pointer(&data[0])
				}
			}
			C.menubarInit(C.int(attempt), pointers[0], C.int(len(icons[0])), pointers[1], C.int(len(icons[1])), pointers[2], C.int(len(icons[2])))
		}, results, menuBarInitAckTimeout, menuBarInitBackoffs)
		if err != nil && ctx.Err() == nil {
			log.Printf("[menubar] %v", err)
		}
		if err == nil {
			preloadMenuBarPopover(app)
		}
	}()
}

// cleanupMenuBar removes the NSStatusItem.
func cleanupMenuBar() {
	menuBarInit.Lock()
	cancel, done := menuBarInit.cancel, menuBarInit.done
	menuBarInit.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	menuBarInit.Lock()
	menuBarInit.results = nil
	menuBarInit.Unlock()
	menuBarReady.Store(false)
	menuBarPresentation.Lock()
	menuBarPresentation.stopped = true
	menuBarPresentation.Unlock()
	// Join in-flight popover reads before shutdown closes the stores.
	menuBarPresentation.refreshMu.Lock()
	menuBarPresentation.refreshMu.Unlock()
	C.menubarCleanup()
}

// updateMenuBarStatus sets the activity status indicator.
// 0=recording, 1=paused, 2=no-permission, 3=stopped
func updateMenuBarStatus(status int) {
	switch status {
	case 1:
		menuBarActivityPaused = true
	default:
		menuBarActivityPaused = false
	}
	C.menubarSetStatus(C.int(status))
}

func updateMenuBarLoginItemChecked(checked bool) {
	C.menubarSetLoginItemChecked(C._Bool(checked))
}

// updateMenuBarDoingNow sets the "doing now" line at the top of the menu.
func updateMenuBarDoingNow(text string) {
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))
	C.menubarSetDoingNow(cText)
}

func updateMenuBarClosedEyesMode(enabled bool) {
	cTitle := C.CString(menuBarObservationTitle(enabled, menuBarLanguage(menuBarApp)))
	defer C.free(unsafe.Pointer(cTitle))
	C.menubarSetClosedEyesMode(C._Bool(enabled), cTitle)
}

func currentMenuBarStatus() int {
	if !accessibilityAccess() || !preflightScreenCaptureAccess() {
		return 2
	}
	if menuBarApp == nil || menuBarApp.activity == nil {
		if menuBarActivityPaused {
			return 1
		}
		return 3
	}
	if menuBarApp.activity.monitor.IsRunning() {
		return 0
	}
	if menuBarActivityPaused {
		return 1
	}
	return 3
}

// ── Activation policy ─────────────────────────────────────────────────

func switchToRegularApp() {
	C.setActivationPolicyRegular()
}

func switchToAccessoryApp() {
	C.setActivationPolicyAccessory()
}

// ── Login item ────────────────────────────────────────────────────────

// runLoginItemCommand runs before normal main startup, so the installed copy
// can manage its SMAppService without opening data or taking the instance lock.
func runLoginItemCommand(args []string) (exitCode int, handled bool) {
	action, handled := loginItemCommandAction(args)
	if !handled {
		return 0, false
	}
	if action == "" {
		log.Printf("[loginitem] expected %s register|unregister|status", loginItemCommandFlag)
		return 2, true
	}
	fmt.Println(localLoginItemOperation(action))
	return 0, true
}

func localLoginItemOperation(action string) int {
	switch action {
	case "register":
		if bool(C.registerLoginItemSM()) {
			return 1
		}
	case "unregister":
		if bool(C.unregisterLoginItemSM()) {
			return 1
		}
	case "status":
		return int(C.loginItemStatusSM())
	}
	return 0
}

func preferredLoginItemBundle() string {
	home, _ := os.UserHomeDir()
	return loginItemBundlePath(home, appFlavor(), findAppBundle())
}

func loginItemOperation(action string) int {
	bundle := preferredLoginItemBundle()
	if bundle == "" || bundle == findAppBundle() {
		return localLoginItemOperation(action)
	}
	path := C.CString(bundle)
	defer C.free(unsafe.Pointer(path))
	executable := C.loginItemBundleExecutable(path)
	if executable == nil {
		log.Printf("[loginitem] cannot find installed executable in %s; reinstall with build-wails.sh --install", bundle)
		return -1
	}
	defer C.free(unsafe.Pointer(executable))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, C.GoString(executable), loginItemCommandFlag, action)
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		log.Printf("[loginitem] %s via installed bundle %s failed: %v", action, bundle, err)
		return -1
	}
	result, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		log.Printf("[loginitem] invalid %s response from %s; rebuild and reinstall with --install: %v", action, bundle, err)
		return -1
	}
	return result
}

func registerAsLoginItem() bool {
	return loginItemOperation("register") == 1
}

func unregisterLoginItem() bool {
	return loginItemOperation("unregister") == 1
}

func readStartAtLogin() (bool, error) {
	return loginStatusFromCode(loginItemOperation("status"))
}

func isLoginItemRegistered() bool {
	return loginItemOperation("status") == 1 // SMAppServiceStatusEnabled
}

func ensureStartAtLoginEnabled() {
	if loadConfig().StartAtLoginDisabled {
		updateMenuBarLoginItemChecked(false)
		return
	}
	// Re-register from the installed copy even if this worktree previously
	// registered the same bundle identifier, so the stored path is refreshed.
	if preferredLoginItemBundle() == findAppBundle() && isLoginItemRegistered() {
		updateMenuBarLoginItemChecked(true)
		return
	}
	if registerAsLoginItem() {
		log.Printf("[loginitem] enabled by default")
		updateMenuBarLoginItemChecked(true)
		return
	}
	log.Printf("[loginitem] default enable requested but registration did not report success")
	updateMenuBarLoginItemChecked(isLoginItemRegistered())
}

// ── Startup detection ─────────────────────────────────────────────────

func shouldStartHidden() bool {
	// Only start hidden when explicitly passed --background flag.
	// SMAppService login items inherit the app's launch arguments,
	// but we don't set --background there yet. For now, this is
	// only triggered by explicit command-line usage.
	for _, arg := range os.Args[1:] {
		if arg == "--background" {
			return true
		}
	}
	return false
}

// ── Real quit ─────────────────────────────────────────────────────────

func performRealQuit() {
	C.performRealQuit()
}

// ── CGO callbacks ─────────────────────────────────────────────────────

// The menu item actions and the Dock reopen swizzle call these exports on
// the main thread inside AppKit's per-event autorelease pool. Wails runtime
// calls must not run there (see goAudioCaptureEvent), so the work moves to a
// goroutine.

//export goMenuBarOpen
func goMenuBarOpen() {
	if menuBarApp == nil || menuBarApp.ctx == nil {
		return
	}
	ctx := menuBarApp.ctx
	go func() {
		switchToRegularApp()
		runtime.WindowShow(ctx)
		runtime.WindowUnminimise(ctx)
	}()
}

// goMenuBarOpenActivity runs when the "doing now" line is chosen: it shows
// the window and asks the frontend to open the Activity log.
//
//export goMenuBarOpenActivity
func goMenuBarOpenActivity() {
	if menuBarApp == nil || menuBarApp.ctx == nil {
		return
	}
	app := menuBarApp
	ctx := app.ctx
	go func() {
		switchToRegularApp()
		runtime.WindowShow(ctx)
		runtime.WindowUnminimise(ctx)
		app.emitBrowserEvent(activityOpenEvent)
	}()
}

// menuBarToggleActivityMu runs one menu bar activity toggle at a time, as the
// main thread did before the toggles moved to goroutines.
var menuBarToggleActivityMu sync.Mutex

//export goMenuBarToggleActivity
func goMenuBarToggleActivity() {
	if menuBarApp == nil {
		return
	}
	// Starting the monitor emits activity:status and stopping it can extract
	// a session, both through the Wails runtime.
	go toggleActivityFromMenuBar()
}

func toggleActivityFromMenuBar() {
	menuBarToggleActivityMu.Lock()
	defer menuBarToggleActivityMu.Unlock()

	// If no permissions, open System Settings.
	switch currentMenuBarStatus() {
	case 2:
		if !accessibilityAccess() {
			if err := openAccessibilitySettings(); err != nil {
				log.Printf("[menubar] failed to open accessibility settings: %v", err)
			}
			return
		}
		if err := openScreenRecordingSettings(); err != nil {
			log.Printf("[menubar] failed to open screen recording settings: %v", err)
		}
	case 1, 3:
		if menuBarApp.activity == nil {
			menuBarApp.initActivityMonitor()
		}
		if err := menuBarApp.startActivityMonitor(); err != nil {
			log.Printf("[menubar] failed to resume activity: %v", err)
			updateMenuBarStatus(3)
			return
		}
	case 0:
		menuBarApp.stopActivityMonitor()
		updateMenuBarStatus(1)
	}
}

//export goMenuBarToggleClosedEyes
func goMenuBarToggleClosedEyes() {
	if menuBarApp == nil {
		return
	}
	go menuBarApp.toggleMenuBarObservation()
}

//export goMenuBarQuit
func goMenuBarQuit() {
	performRealQuit()
}

//export goMenuBarSetLoginItem
func goMenuBarSetLoginItem(enabled C._Bool) {
	e := bool(enabled)
	go func() {
		var ok bool
		if e {
			ok = registerAsLoginItem()
		} else {
			ok = unregisterLoginItem()
		}
		if !ok {
			log.Printf("[menubar] failed to update start-at-login to %t", e)
			updateMenuBarLoginItemChecked(isLoginItemRegistered())
			return
		}
		if err := updateConfig(func(c *yansuConfig) {
			c.StartAtLoginDisabled = !e
		}); err != nil {
			log.Printf("[menubar] failed to update config for login item: %v", err)
		}
		updateMenuBarLoginItemChecked(e)
	}()
}
