//go:build darwin

package main

/*
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#include <objc/runtime.h>
#include <stdlib.h>
extern void goAppMenuAction(int action);
static void isoformMenuAction(id self, SEL cmd, id sender) {
    goAppMenuAction((int)[(NSMenuItem*)sender tag]);
}
// Tags match app_menu.go's appMenu* actions.
enum { kAbout = 0, kCheck = 1, kSettings = 2, kRestart = 3, kShortcuts = 4, kIsoformHelp = 5, kFeedback = 6 };
// The Help menu's own tag on the main menu bar, so a refresh replaces it.
static const NSInteger kHelpMenuTag = 0x150F0;

static id isoformMenuTarget(void) {
    Class cls = NSClassFromString(@"IsoformAppMenuTarget");
    if (!cls) {
        cls = objc_allocateClassPair([NSObject class], "IsoformAppMenuTarget", 0);
        class_addMethod(cls, @selector(isoformMenuAction:), (IMP)isoformMenuAction, "v@:@");
        objc_registerClassPair(cls);
    }
    static id target = nil;
    if (!target) target = [[cls alloc] init];
    return target;
}

static NSMenuItem *isoformMenuItem(NSString *title, NSInteger tag, NSString *key) {
    NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:title action:@selector(isoformMenuAction:) keyEquivalent:key];
    [item setTarget:isoformMenuTarget()];
    [item setTag:tag];
    if ([key length]) [item setKeyEquivalentModifierMask:NSEventModifierFlagCommand];
    return item;
}

static void configureIsoformAppMenu(const char *about, const char *update, int updateTag, const char *settings,
                                    const char *help, const char *shortcuts, const char *isoformHelp, const char *feedback) {
    NSString *a = [NSString stringWithUTF8String:about];
    NSString *u = [NSString stringWithUTF8String:update];
    NSString *s = [NSString stringWithUTF8String:settings];
    NSString *h = [NSString stringWithUTF8String:help];
    NSString *k = [NSString stringWithUTF8String:shortcuts];
    NSString *i = [NSString stringWithUTF8String:isoformHelp];
    NSString *f = [NSString stringWithUTF8String:feedback];
    NSInteger ut = updateTag;
    dispatch_async(dispatch_get_main_queue(), ^{
        NSMenu *mainMenu = [NSApp mainMenu];
        NSMenu *menu = [[mainMenu itemAtIndex:0] submenu];
        if (!menu) return;
        // Keep all native Hide, Show All and Quit items and their selectors.
        while ([menu numberOfItems] && [[menu itemAtIndex:0] action] != @selector(hide:)) {
            [menu removeItemAtIndex:0];
        }
        NSInteger position = 0;
        NSMenuItem *items[3] = { isoformMenuItem(a, kAbout, @""), isoformMenuItem(u, ut, @""), isoformMenuItem(s, kSettings, @",") };
        for (NSInteger n = 0; n < 3; n++) {
            if (n == 2) [menu insertItem:[NSMenuItem separatorItem] atIndex:position++];
            [menu insertItem:items[n] atIndex:position++];
            [items[n] release];
        }
        [menu insertItem:[NSMenuItem separatorItem] atIndex:position];

        // The Help menu: Keyboard Shortcuts, Isoform Help, Send Feedback…
        // (design A5). Keyboard Shortcuts has no key equivalent here, so ⌘/
        // stays the app's own shortcut and follows a key the person changes.
        NSInteger old = [mainMenu indexOfItemWithTag:kHelpMenuTag];
        if (old >= 0) [mainMenu removeItemAtIndex:old];
        NSMenuItem *top = [[NSMenuItem alloc] initWithTitle:h action:nil keyEquivalent:@""];
        [top setTag:kHelpMenuTag];
        NSMenu *helpMenu = [[NSMenu alloc] initWithTitle:h];
        NSMenuItem *helpItems[3] = { isoformMenuItem(k, kShortcuts, @""), isoformMenuItem(i, kIsoformHelp, @""), isoformMenuItem(f, kFeedback, @"") };
        for (NSInteger n = 0; n < 3; n++) {
            [helpMenu addItem:helpItems[n]];
            [helpItems[n] release];
        }
        [top setSubmenu:helpMenu];
        [mainMenu addItem:top];
        [NSApp setHelpMenu:helpMenu];
        [helpMenu release];
        [top release];
    });
}
*/
import "C"
import (
	"sync/atomic"
	"unsafe"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var appMenuApp atomic.Pointer[App]

func (a *App) refreshAppMenu() {
	appMenuApp.Store(a)
	language := a.doingNowLanguage()
	w := appMenuWords(language)
	update, updateTag := w.Check, appMenuCheck
	if a.appMenuUpdateReady() {
		update, updateTag = w.Restart, appMenuRestart
	}
	strs := []*C.char{
		C.CString(w.About), C.CString(update), C.CString(w.Settings),
		C.CString(w.Help), C.CString(w.Shortcuts), C.CString(w.IsoformHelp), C.CString(w.Feedback),
	}
	defer func() {
		for _, s := range strs {
			C.free(unsafe.Pointer(s))
		}
	}()
	C.configureIsoformAppMenu(strs[0], strs[1], C.int(updateTag), strs[2], strs[3], strs[4], strs[5], strs[6])
}

//export goAppMenuAction
func goAppMenuAction(action C.int) {
	a := appMenuApp.Load()
	if a == nil || a.ctx == nil {
		return
	}
	go func() {
		show := func() {
			switchToRegularApp()
			wailsRuntime.WindowShow(a.ctx)
			wailsRuntime.WindowUnminimise(a.ctx)
		}
		dispatchAppMenuAction(int(action), appMenuActions{
			open: func(topic string) {
				show()
				wailsRuntime.EventsEmit(a.ctx, "settings:open", topic)
			},
			check:   func() { a.CheckForUpdates() },
			restart: func() { _ = a.RestartToUpdate() },
			feedback: func() {
				wailsRuntime.BrowserOpenURL(a.ctx, appMenuFeedbackURL(a.doingNowLanguage(), Version))
			},
		})
	}()
}
