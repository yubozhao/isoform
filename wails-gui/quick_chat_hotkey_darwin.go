//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework ApplicationServices

#import <Cocoa/Cocoa.h>
#include <ApplicationServices/ApplicationServices.h>
#include <stdbool.h>
#include <stdint.h>

// Two routing paths. The mode flag is the only thing the callback
// branches on, so the hot path stays predictable when the user has
// rebound to a normal modifier+key combo.
enum {
    QC_MODE_OPT_OPT = 0, // default: double-tap Option (legacy behaviour)
    QC_MODE_COMBO   = 1, // user-configured modifier+key
};

// Forward declarations from Go (see exports at the bottom of this file).
extern void goQuickChatHotkeyTriggered(char *frontmostBundleID);
extern void goClosedEyesHotkeyTriggered(void);
extern int  goQuickChatHotkeyObserve(int keyDown, int blocked);
extern void goQuickChatHotkeyReset(void);
extern int  goQuickChatHotkeyRecording(void);
extern void goQuickChatHotkeyCaptureRecord(int keyCode,
                                             int cmd, int ctrl,
                                             int alt, int shift);
extern void goSpotlightDismissOnExternalKey(void);

// ── State ───────────────────────────────────────────────────────────────

static CFMachPortRef _hotkeyTap = NULL;
static CFRunLoopSourceRef _hotkeyTapSource = NULL;

// The tap's run loop. An event-tap callback fires on whatever run loop
// hosts its source, so this tap MUST NOT live on CFRunLoopGetMain():
// that run loop is shared with the Wails WebView, and because this is an
// active (event-consuming) tap the window server blocks keystroke
// delivery until the callback returns. A busy main thread therefore
// stalled — and on timeout, dropped — every keystroke session-wide,
// breaking IME composition in slow web-view inputs like DingTalk. Hosting
// the tap on a dedicated thread decouples keystroke delivery from
// main-thread stalls entirely (the same invariant the activity monitor's
// tap establishes). Written by the tap thread, read by the Go stop path.
static CFRunLoopRef _Atomic _hotkeyRunLoop = NULL;

// Set by Stop, read by the tap thread's run loop guard. Using a flag plus
// a while-loop around CFRunLoopRun (rather than relying on CFRunLoopStop
// alone) makes stop independent of timing: a stop requested before the
// thread enters CFRunLoopRun is caught by the loop guard instead of being
// lost. Reset on each Start so a restart re-arms cleanly. Plain int (not
// _Atomic-qualified) so the __atomic_* builtins apply, matching
// _spotlightDismissOnKey above.
static int _hotkeyStopRequested = 0;

// Signaled by the tap thread once teardown (disable, remove, release,
// null) has fully run, and waited on by Stop before it returns. This is
// what makes Stop synchronous: by the time Stop returns, _hotkeyTap is
// NULL, so a Stop→Start sequence cannot see a stale non-NULL tap and
// short-circuit into a "started" state with no live tap. Created fresh per
// Start cycle; the tap thread also holds a captured local copy so it stays
// valid even if a later Start overwrites this static.
static dispatch_semaphore_t _hotkeyTeardownDone = NULL;

// Target combo state. Read on every event by the callback and updated
// from Go via quickChatSetTargetCombo whenever the user picks a new
// hotkey. Plain ints + no locking: in the worst case one keystroke
// during an update sees a half-applied combo and just doesn't fire.
static int _mode          = QC_MODE_OPT_OPT;
static int _targetKeyCode = -1;
static int _targetCmd     = 0;
static int _targetCtrl    = 0;
static int _targetAlt     = 0;
static int _targetShift   = 0;

// Armed by Go when the spotlight pill is summoned (shown but not yet
// engaged). While armed, the next character keyDown — which necessarily
// goes to the user's app, since the non-key pill can't receive it —
// dismisses the pill (option C: typing elsewhere gets the overlay out of
// the way). One-shot: cleared on the first keyDown and on user engagement,
// re-armed on the next show. Written from Go goroutines (the setter) and
// read/cleared on the event-tap thread (the callback), so use relaxed
// atomics — we only need a torn-free flag, not ordering against other state.
static int _spotlightDismissOnKey = 0;

static void quickChatSetSpotlightDismissOnKey(int v) {
    __atomic_store_n(&_spotlightDismissOnKey, v, __ATOMIC_RELAXED);
}

static char* copyFrontmostBundleIDExcludingYansu(void) {
    @autoreleasepool {
        NSString *selfBundleID = [[NSBundle mainBundle] bundleIdentifier];
        NSArray<NSRunningApplication *> *apps =
            [[NSWorkspace sharedWorkspace] runningApplications];

        for (NSRunningApplication *app in apps) {
            if (!app.active) continue;
            NSString *bid = app.bundleIdentifier;
            if (!bid) continue;
            if (selfBundleID && [bid isEqualToString:selfBundleID]) continue;
            const char *utf8 = [bid UTF8String];
            if (!utf8) continue;
            char *out = malloc(strlen(utf8) + 1);
            strcpy(out, utf8);
            return out;
        }
        return NULL;
    }
}

static void quickChatHandleTrigger(void) {
    char *bid = copyFrontmostBundleIDExcludingYansu();
    const char *forGo = bid != NULL ? bid : "";
    goQuickChatHotkeyTriggered((char *)forGo);
    if (bid) free(bid);
}

static bool closedEyesHotkeyMatches(CGKeyCode keyCode, CGEventFlags flags) {
    if ((int)keyCode != 14) return false; // E
    bool cmd   = (flags & kCGEventFlagMaskCommand)   != 0;
    bool ctrl  = (flags & kCGEventFlagMaskControl)   != 0;
    bool alt   = (flags & kCGEventFlagMaskAlternate) != 0;
    bool shift = (flags & kCGEventFlagMaskShift)     != 0;
    return cmd && ctrl && alt && !shift;
}

// comboHotkeyMatches reports whether this keyDown is the user-configured
// combo quick-chat hotkey. Used in two places that must agree: the combo
// trigger path, and the spotlight dismiss-on-key guard (so re-pressing the
// hotkey while the pill is shown re-summons instead of racing a dismiss
// against the re-show). Only meaningful in QC_MODE_COMBO; the opt-opt
// hotkey is a flagsChanged event, never a keyDown.
static bool comboHotkeyMatches(CGKeyCode keyCode, CGEventFlags flags) {
    if (_mode != QC_MODE_COMBO) return false;
    if (_targetKeyCode < 0) return false;
    if ((int)keyCode != _targetKeyCode) return false;
    int gotCmd   = (flags & kCGEventFlagMaskCommand)   ? 1 : 0;
    int gotCtrl  = (flags & kCGEventFlagMaskControl)   ? 1 : 0;
    int gotAlt   = (flags & kCGEventFlagMaskAlternate) ? 1 : 0;
    int gotShift = (flags & kCGEventFlagMaskShift)     ? 1 : 0;
    return gotCmd == _targetCmd && gotCtrl == _targetCtrl &&
           gotAlt == _targetAlt && gotShift == _targetShift;
}

// Event-tap callback. The tap is created with both keyDown and
// flagsChanged masks so we can route to the right path without
// reinstalling the tap when the user switches modes.
static CGEventRef quickChatEventTapCallback(CGEventTapProxy proxy,
                                              CGEventType type,
                                              CGEventRef event,
                                              void *userInfo) {
    if (type == kCGEventTapDisabledByTimeout ||
        type == kCGEventTapDisabledByUserInput) {
        if (_hotkeyTap) CGEventTapEnable(_hotkeyTap, true);
        return event;
    }

    // Recording mode: capture any non-modifier keyDown, snapshot the
    // live modifier flags, report to Go, swallow. The renderer drives
    // this on/off via SetQuickChatHotkeyRecording.
    if (goQuickChatHotkeyRecording()) {
        if (type != kCGEventKeyDown) return event;
        CGKeyCode keyCode = (CGKeyCode)CGEventGetIntegerValueField(
            event, kCGKeyboardEventKeycode);
        CGEventFlags flags = CGEventGetFlags(event);
        int cmd   = (flags & kCGEventFlagMaskCommand)   ? 1 : 0;
        int ctrl  = (flags & kCGEventFlagMaskControl)   ? 1 : 0;
        int alt   = (flags & kCGEventFlagMaskAlternate) ? 1 : 0;
        int shift = (flags & kCGEventFlagMaskShift)     ? 1 : 0;
        goQuickChatHotkeyCaptureRecord((int)keyCode, cmd, ctrl, alt, shift);
        return NULL;
    }

    if (type == kCGEventKeyDown) {
        CGKeyCode keyCode = (CGKeyCode)CGEventGetIntegerValueField(
            event, kCGKeyboardEventKeycode);
        if (closedEyesHotkeyMatches(keyCode, CGEventGetFlags(event))) {
            dispatch_async(dispatch_get_main_queue(), ^{
                goClosedEyesHotkeyTriggered();
            });
            return NULL;
        }
    }

    // Spotlight option C: the pill is shown but not engaged (it's non-key),
    // so any character keyDown is going to the user's app — dismiss the pill
    // so it doesn't linger over their work. One-shot (cleared immediately);
    // the event is NOT consumed, so the keystroke still reaches the app.
    //
    // EXCEPT the quick-chat hotkey's own keydown (combo mode): that keydown
    // re-summons the pill, so dismissing it would race the re-show. Let it
    // fall through to the combo handler below.
    if (__atomic_load_n(&_spotlightDismissOnKey, __ATOMIC_RELAXED) &&
        type == kCGEventKeyDown) {
        CGKeyCode dismissKeyCode = (CGKeyCode)CGEventGetIntegerValueField(
            event, kCGKeyboardEventKeycode);
        if (!comboHotkeyMatches(dismissKeyCode, CGEventGetFlags(event))) {
            __atomic_store_n(&_spotlightDismissOnKey, 0, __ATOMIC_RELAXED);
            dispatch_async(dispatch_get_main_queue(), ^{
                goSpotlightDismissOnExternalKey();
            });
        }
    }

    if (_mode == QC_MODE_OPT_OPT) {
        // Legacy double-tap-Option path. Only react to flagsChanged
        // events on the two physical Option keys.
        if (type != kCGEventFlagsChanged) return event;

        CGEventFlags flags = CGEventGetFlags(event);
        CGKeyCode keyCode = (CGKeyCode)CGEventGetIntegerValueField(
            event, kCGKeyboardEventKeycode);
        // 58 = kVK_Option, 61 = kVK_RightOption.
        bool isOptionKey = (keyCode == 58) || (keyCode == 61);
        if (!isOptionKey) return event;

        CGEventFlags nonOptionModifiers =
            (flags & (kCGEventFlagMaskCommand | kCGEventFlagMaskControl |
                      kCGEventFlagMaskShift));
        bool blocked       = (nonOptionModifiers != 0);
        bool optionNowDown = (flags & kCGEventFlagMaskAlternate) != 0;

        if (goQuickChatHotkeyObserve(optionNowDown ? 1 : 0,
                                      blocked       ? 1 : 0)) {
            dispatch_async(dispatch_get_main_queue(), ^{
                quickChatHandleTrigger();
            });
        }
        // Listen-only behaviour for the legacy path: we never modify or
        // swallow Option events, matching the pre-change UX where
        // Option-as-modifier in other apps still works.
        return event;
    }

    // QC_MODE_COMBO: user-configured modifier+key. Consume the keystroke so
    // the chord doesn't fall through to the focused app.
    if (type != kCGEventKeyDown) return event;
    CGKeyCode keyCode = (CGKeyCode)CGEventGetIntegerValueField(
        event, kCGKeyboardEventKeycode);
    if (!comboHotkeyMatches(keyCode, CGEventGetFlags(event))) return event;

    dispatch_async(dispatch_get_main_queue(), ^{
        quickChatHandleTrigger();
    });
    return NULL; // swallow the keystroke
}

// Apply a new target combo. Mode 0 = double-tap Option (key fields
// ignored), mode 1 = modifier+key.
static void quickChatSetTargetCombo(int mode, int kc, int cmd, int ctrl, int alt, int shift) {
    _mode          = mode;
    _targetKeyCode = kc;
    _targetCmd     = cmd ? 1 : 0;
    _targetCtrl    = ctrl ? 1 : 0;
    _targetAlt     = alt ? 1 : 0;
    _targetShift   = shift ? 1 : 0;
    // Any half-completed double-tap is invalidated on mode switch.
    goQuickChatHotkeyReset();
}

// Start the event tap. Returns true on success, false if Accessibility
// permission is missing or the tap could not be created.
static bool quickChatStartHotkey(void) {
    if (_hotkeyTap != NULL) return true;

    CGEventMask mask = CGEventMaskBit(kCGEventKeyDown) |
                       CGEventMaskBit(kCGEventFlagsChanged);
    _hotkeyTap = CGEventTapCreate(
        kCGSessionEventTap,
        kCGHeadInsertEventTap,
        kCGEventTapOptionDefault, // combo mode needs to consume events
        mask,
        quickChatEventTapCallback,
        NULL);
    if (!_hotkeyTap) return false;

    _hotkeyTapSource = CFMachPortCreateRunLoopSource(
        kCFAllocatorDefault, _hotkeyTap, 0);
    if (!_hotkeyTapSource) {
        CFRelease(_hotkeyTap);
        _hotkeyTap = NULL;
        return false;
    }

    // Host the tap on a dedicated run loop thread, never CFRunLoopGetMain()
    // (see _hotkeyRunLoop for why). The tap and source are created above so
    // this function still reports creation failure synchronously; only the
    // run-loop hosting is deferred. USER_INTERACTIVE QoS keeps the thread
    // scheduled promptly under load: this is an active tap, so the window
    // server waits for the callback, and a late-scheduled thread would add
    // latency to every keystroke.
    //
    // Lifecycle ownership: every operation on _hotkeyTap/_hotkeyTapSource
    // (install, enable, remove, release) happens ON this thread. Stop only
    // calls CFRunLoopStop (thread-safe) to unblock CFRunLoopRun; teardown
    // then runs here, after the run loop exits. This avoids cross-thread
    // release of objects the run loop is still using. The semaphore makes
    // Start block until the source is installed and _hotkeyRunLoop is
    // published, so a Stop that follows Start is always well-ordered
    // against install — closing the use-after-release window a bare
    // dispatch_async would leave open.
    __atomic_store_n(&_hotkeyStopRequested, 0, __ATOMIC_RELAXED);
    dispatch_semaphore_t ready = dispatch_semaphore_create(0);
    dispatch_semaphore_t teardownDone = dispatch_semaphore_create(0);
    _hotkeyTeardownDone = teardownDone;
    dispatch_async(
        dispatch_get_global_queue(QOS_CLASS_USER_INTERACTIVE, 0), ^{
            _hotkeyRunLoop = CFRunLoopGetCurrent();
            CFRunLoopAddSource(_hotkeyRunLoop, _hotkeyTapSource,
                               kCFRunLoopCommonModes);
            CGEventTapEnable(_hotkeyTap, true);
            dispatch_semaphore_signal(ready);

            // Run until Stop requests teardown. The guard catches a stop
            // requested before we entered CFRunLoopRun; CFRunLoopStop
            // returns it otherwise. CFRunLoopRun can also wake spuriously,
            // in which case we simply re-enter.
            while (!__atomic_load_n(&_hotkeyStopRequested, __ATOMIC_ACQUIRE)) {
                CFRunLoopRun();
            }

            CGEventTapEnable(_hotkeyTap, false);
            CFRunLoopRemoveSource(_hotkeyRunLoop, _hotkeyTapSource,
                                  kCFRunLoopCommonModes);
            CFRelease(_hotkeyTapSource);
            CFRelease(_hotkeyTap);
            _hotkeyTapSource = NULL;
            _hotkeyTap = NULL;
            _hotkeyRunLoop = NULL;
            // Publish teardown completion last: Stop waits on this and
            // relies on every global above already being NULL.
            dispatch_semaphore_signal(teardownDone);
        });
    dispatch_semaphore_wait(ready, DISPATCH_TIME_FOREVER);
    // This file is MRC (no -fobjc-arc in its cgo CFLAGS), so the semaphore's
    // create +1 must be released by hand. The dispatched block holds its own
    // captured retain until it finishes, so the object stays alive for the
    // signal above; this only drops the creator's reference. teardownDone is
    // released symmetrically in quickChatStopHotkey.
    dispatch_release(ready);
    return true;
}

static void quickChatStopHotkey(void) {
    if (_hotkeyTap == NULL) {
        // Nothing running (never started, or already torn down). Still
        // reset the Go detector so a half-completed double-tap can't leak.
        goQuickChatHotkeyReset();
        return;
    }

    // Request teardown, wake the run loop, then block until the tap thread
    // has fully released the tap/source and nulled the globals. Returning
    // only after teardown completes keeps Stop synchronous, so an
    // immediately following Start sees _hotkeyTap == NULL and reinstalls a
    // live tap instead of short-circuiting on a stale pointer.
    __atomic_store_n(&_hotkeyStopRequested, 1, __ATOMIC_RELEASE);
    CFRunLoopRef rl = _hotkeyRunLoop;
    if (rl) {
        CFRunLoopStop(rl);
    }
    dispatch_semaphore_t teardownDone = _hotkeyTeardownDone;
    if (teardownDone) {
        dispatch_semaphore_wait(teardownDone, DISPATCH_TIME_FOREVER);
        _hotkeyTeardownDone = NULL;
        // MRC: drop the create +1 (see quickChatStartHotkey). The block has
        // already signaled and is ending, balancing its own captured retain.
        dispatch_release(teardownDone);
    }
    goQuickChatHotkeyReset();
}
*/
import "C"

import (
	"log"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"wails-gui/internal/platform/nativeprompt"
)

// StartQuickChatHotkey installs the global key tap: Closed Eyes' ⌘⌃⌥E
// always, and the configurable Quick chat key only while Chat is on
// (tapQuickChatHotkey). Requires Accessibility permission (already granted
// for activity monitoring). Returns an error if the event tap cannot be
// created.
func (a *App) StartQuickChatHotkey() error {
	if !nativeprompt.Allowed() {
		return &monitorError{"QuickChat hotkey is unavailable under go test"}
	}
	// Serialize against StopQuickChatHotkey so the C tap-lifecycle globals
	// are never driven concurrently. Held across the (briefly blocking)
	// cgo call: the tap thread that unblocks it never takes this lock, so
	// there is no deadlock.
	quickChatLifecycleMu.Lock()
	defer quickChatLifecycleMu.Unlock()

	quickChatHotkeyMu.Lock()
	quickChatApp = a
	quickChatHotkeyMu.Unlock()

	// Push the persisted combo into the cgo state before installing the
	// tap so the first event after install already routes correctly.
	applyQuickChatHotkeyDarwin(tapQuickChatHotkey())

	if !bool(C.quickChatStartHotkey()) {
		return &monitorError{"failed to create QuickChat event tap (check Accessibility permission)"}
	}
	log.Printf("[quickChat] global hotkey enabled (%s)", describeCombo(tapQuickChatHotkey()))
	return nil
}

// StopQuickChatHotkey tears down the event tap. Safe to call even when
// the hotkey wasn't started.
func (a *App) StopQuickChatHotkey() {
	// Serialize against StartQuickChatHotkey (see there). quickChatStopHotkey
	// blocks until the tap thread finishes teardown; that thread does not
	// take this lock, so holding it here is deadlock-free.
	quickChatLifecycleMu.Lock()
	defer quickChatLifecycleMu.Unlock()

	C.quickChatStopHotkey()
	quickChatHotkeyMu.Lock()
	quickChatApp = nil
	quickChatHotkeyMu.Unlock()
}

// applyHotkeyToPlatform is the SetQuickChatHotkey hook for darwin.
func applyHotkeyToPlatform(c QuickChatHotkeyConfig) {
	applyQuickChatHotkeyDarwin(c)
}

// setSpotlightDismissOnKey arms (true) or disarms (false) the event tap's
// dismiss-on-typing flag. Armed right after the spotlight pill is summoned;
// disarmed when the user clicks into the pill (engaged) or on the first
// external keystroke.
func setSpotlightDismissOnKey(on bool) {
	v := C.int(0)
	if on {
		v = 1
	}
	C.quickChatSetSpotlightDismissOnKey(v)
}

// applyQuickChatHotkeyDarwin maps the Go config into the cgo state and
// picks between the double-tap-Option legacy path and the
// modifier+key path.
func applyQuickChatHotkeyDarwin(c QuickChatHotkeyConfig) {
	if c.Key == quickChatHotkeyKeyOff {
		C.quickChatSetTargetCombo(1, -1, 0, 0, 0, 0)
		return
	}
	if c.Key == quickChatHotkeyKeyOptOpt {
		C.quickChatSetTargetCombo(0, -1, 0, 0, 0, 0)
		return
	}
	kc, ok := cgKeyCodeForKey(c.Key)
	if !ok {
		// Unknown key — disable matching by setting an impossible
		// keycode in combo mode. The next valid SetQuickChatHotkey
		// re-enables.
		C.quickChatSetTargetCombo(1, -1, 0, 0, 0, 0)
		return
	}
	cmd := C.int(0)
	if c.Meta {
		cmd = 1
	}
	ctrl := C.int(0)
	if c.Ctrl {
		ctrl = 1
	}
	alt := C.int(0)
	if c.Alt {
		alt = 1
	}
	shift := C.int(0)
	if c.Shift {
		shift = 1
	}
	C.quickChatSetTargetCombo(1, C.int(kc), cmd, ctrl, alt, shift)
}

// cgKeyCodeForKey maps the JS-side `key` string to a macOS Carbon
// virtual key code (kVK_*). Values are stable across keyboard layouts;
// for layout-aware mapping we'd need UCKeyTranslate, but the recorder
// captures by physical key so the JS `key` and kVK already correspond.
func cgKeyCodeForKey(key string) (int, bool) {
	key = strings.ToLower(key) // do NOT TrimSpace — " " (space) is valid
	switch key {
	case "a":
		return 0, true
	case "s":
		return 1, true
	case "d":
		return 2, true
	case "f":
		return 3, true
	case "h":
		return 4, true
	case "g":
		return 5, true
	case "z":
		return 6, true
	case "x":
		return 7, true
	case "c":
		return 8, true
	case "v":
		return 9, true
	case "b":
		return 11, true
	case "q":
		return 12, true
	case "w":
		return 13, true
	case "e":
		return 14, true
	case "r":
		return 15, true
	case "y":
		return 16, true
	case "t":
		return 17, true
	case "o":
		return 31, true
	case "u":
		return 32, true
	case "i":
		return 34, true
	case "p":
		return 35, true
	case "l":
		return 37, true
	case "j":
		return 38, true
	case "k":
		return 40, true
	case "n":
		return 45, true
	case "m":
		return 46, true
	case "1":
		return 18, true
	case "2":
		return 19, true
	case "3":
		return 20, true
	case "4":
		return 21, true
	case "5":
		return 23, true
	case "6":
		return 22, true
	case "7":
		return 26, true
	case "8":
		return 28, true
	case "9":
		return 25, true
	case "0":
		return 29, true
	case "-":
		return 27, true
	case "=":
		return 24, true
	case "[":
		return 33, true
	case "]":
		return 30, true
	case "\\":
		return 42, true
	case ";":
		return 41, true
	case "'":
		return 39, true
	case ",":
		return 43, true
	case ".":
		return 47, true
	case "/":
		return 44, true
	case "`":
		return 50, true
	case " ", "space":
		return 49, true
	case "enter", "return":
		return 36, true
	case "tab":
		return 48, true
	case "escape", "esc":
		return 53, true
	case "backspace":
		return 51, true
	case "delete":
		return 117, true
	case "home":
		return 115, true
	case "pageup":
		return 116, true
	case "end":
		return 119, true
	case "pagedown":
		return 121, true
	case "arrowleft":
		return 123, true
	case "arrowright":
		return 124, true
	case "arrowdown":
		return 125, true
	case "arrowup":
		return 126, true
	case "f1":
		return 122, true
	case "f2":
		return 120, true
	case "f3":
		return 99, true
	case "f4":
		return 118, true
	case "f5":
		return 96, true
	case "f6":
		return 97, true
	case "f7":
		return 98, true
	case "f8":
		return 100, true
	case "f9":
		return 101, true
	case "f10":
		return 109, true
	case "f11":
		return 103, true
	case "f12":
		return 111, true
	}
	return 0, false
}

// describeCombo renders the user-visible name for log output. macOS uses
// glyphs in the UI (⌘⇧Y); raw words are easier to grep in logs.
func describeCombo(c QuickChatHotkeyConfig) string {
	if c.Key == quickChatHotkeyKeyOptOpt {
		return "double-tap Option"
	}
	parts := []string{}
	if c.Ctrl {
		parts = append(parts, "Control")
	}
	if c.Alt {
		parts = append(parts, "Option")
	}
	if c.Shift {
		parts = append(parts, "Shift")
	}
	if c.Meta {
		parts = append(parts, "Command")
	}
	parts = append(parts, strings.ToUpper(c.Key))
	return strings.Join(parts, "+")
}

//export goQuickChatHotkeyObserve
func goQuickChatHotkeyObserve(keyDown C.int, blocked C.int) C.int {
	if quickChatHotkeyDetector.observe(keyDown != 0, blocked != 0, time.Now()) {
		return 1
	}
	return 0
}

//export goQuickChatHotkeyReset
func goQuickChatHotkeyReset() {
	quickChatHotkeyDetector.reset()
}

//export goQuickChatHotkeyRecording
func goQuickChatHotkeyRecording() C.int {
	if quickChatHotkeyRecording.Load() {
		return 1
	}
	return 0
}

//export goQuickChatHotkeyCaptureRecord
func goQuickChatHotkeyCaptureRecord(keyCode, cmd, ctrl, alt, shift C.int) {
	// Bare modifier presses are filtered here rather than in the C
	// callback so the keycode→name table stays in Go. The recorder UX
	// matches Windows: commit on the first non-modifier key.
	if isModifierKeyCode(int(keyCode)) {
		return
	}
	key, ok := jsKeyForCGKeyCode(int(keyCode))
	if !ok {
		return
	}
	go func() {
		quickChatHotkeyMu.Lock()
		app := quickChatApp
		quickChatHotkeyMu.Unlock()
		if app == nil || app.ctx == nil {
			return
		}
		combo := QuickChatHotkeyConfig{
			Meta:  cmd != 0,
			Ctrl:  ctrl != 0,
			Alt:   alt != 0,
			Shift: shift != 0,
			Key:   key,
		}
		wailsruntime.EventsEmit(app.ctx, "quickChat:hotkeyRecorded", combo)
	}()
}

// isModifierKeyCode lists the bare-modifier kVK_* values. We skip them
// during recording so a chord can be built up modifier-first.
func isModifierKeyCode(kc int) bool {
	switch kc {
	case 54, 55, // kVK_RightCommand, kVK_Command
		56, 60, // kVK_Shift, kVK_RightShift
		58, 61, // kVK_Option, kVK_RightOption
		59, 62, // kVK_Control, kVK_RightControl
		57, 63: // kVK_CapsLock, kVK_Function
		return true
	}
	return false
}

// jsKeyForCGKeyCode is the inverse of cgKeyCodeForKey for the recorder
// path. Mirrors the same name conventions as Windows so the persisted
// config moves across platforms unchanged.
func jsKeyForCGKeyCode(kc int) (string, bool) {
	switch kc {
	case 0:
		return "a", true
	case 1:
		return "s", true
	case 2:
		return "d", true
	case 3:
		return "f", true
	case 4:
		return "h", true
	case 5:
		return "g", true
	case 6:
		return "z", true
	case 7:
		return "x", true
	case 8:
		return "c", true
	case 9:
		return "v", true
	case 11:
		return "b", true
	case 12:
		return "q", true
	case 13:
		return "w", true
	case 14:
		return "e", true
	case 15:
		return "r", true
	case 16:
		return "y", true
	case 17:
		return "t", true
	case 31:
		return "o", true
	case 32:
		return "u", true
	case 34:
		return "i", true
	case 35:
		return "p", true
	case 37:
		return "l", true
	case 38:
		return "j", true
	case 40:
		return "k", true
	case 45:
		return "n", true
	case 46:
		return "m", true
	case 18:
		return "1", true
	case 19:
		return "2", true
	case 20:
		return "3", true
	case 21:
		return "4", true
	case 23:
		return "5", true
	case 22:
		return "6", true
	case 26:
		return "7", true
	case 28:
		return "8", true
	case 25:
		return "9", true
	case 29:
		return "0", true
	case 27:
		return "-", true
	case 24:
		return "=", true
	case 33:
		return "[", true
	case 30:
		return "]", true
	case 42:
		return "\\", true
	case 41:
		return ";", true
	case 39:
		return "'", true
	case 43:
		return ",", true
	case 47:
		return ".", true
	case 44:
		return "/", true
	case 50:
		return "`", true
	case 49:
		return " ", true
	case 36:
		return "enter", true
	case 48:
		return "tab", true
	case 53:
		return "escape", true
	case 51:
		return "backspace", true
	case 117:
		return "delete", true
	case 115:
		return "home", true
	case 116:
		return "pageup", true
	case 119:
		return "end", true
	case 121:
		return "pagedown", true
	case 123:
		return "arrowleft", true
	case 124:
		return "arrowright", true
	case 125:
		return "arrowdown", true
	case 126:
		return "arrowup", true
	case 122:
		return "f1", true
	case 120:
		return "f2", true
	case 99:
		return "f3", true
	case 118:
		return "f4", true
	case 96:
		return "f5", true
	case 97:
		return "f6", true
	case 98:
		return "f7", true
	case 100:
		return "f8", true
	case 101:
		return "f9", true
	case 109:
		return "f10", true
	case 103:
		return "f11", true
	case 111:
		return "f12", true
	}
	return "", false
}

//export goQuickChatHotkeyTriggered
func goQuickChatHotkeyTriggered(cBundleID *C.char) {
	bundleID := C.GoString(cBundleID)

	go func() {
		quickChatHotkeyMu.Lock()
		app := quickChatApp
		quickChatHotkeyMu.Unlock()
		if app == nil || app.ctx == nil {
			return
		}

		app.openQuickChat(bundleID)
	}()
}

// goSpotlightDismissOnExternalKey is called from the event tap when the
// user types while the spotlight pill is shown but not engaged (option C):
// the keystroke is going to the user's app, so hide the pill. The tap has
// already cleared the one-shot flag and let the keystroke through.
//
//export goSpotlightDismissOnExternalKey
func goSpotlightDismissOnExternalKey() {
	go func() {
		quickChatHotkeyMu.Lock()
		app := quickChatApp
		quickChatHotkeyMu.Unlock()
		if app == nil || app.spotlight == nil {
			return
		}
		app.spotlight.Hide()
	}()
}

//export goClosedEyesHotkeyTriggered
func goClosedEyesHotkeyTriggered() {
	go func() {
		quickChatHotkeyMu.Lock()
		app := quickChatApp
		quickChatHotkeyMu.Unlock()
		if app == nil {
			return
		}
		app.toggleMenuBarObservation()
	}()
}

// openQuickChat opens Quick chat over the app in front, the one path for the
// hotkey and the menu bar: the spotlight pill with its screen chip, else the
// in-app panel. The pill's questions go to the one Chat thread.
func (a *App) openQuickChat(bundleID string) {
	if a == nil || a.ctx == nil || chatFeatureErr() != nil {
		return
	}
	appName := bundleIDPrettyName(bundleID)
	if a.spotlight != nil {
		// The screen chip (computer-chat v1.0.0 rule 8): none with Watch my
		// screen off, Closed Eyes on, or for a sensitive app.
		chipBundle, chipName, chipTitle := a.spotlightScreenChip(bundleID, appName)
		if err := a.spotlight.Show(chipBundle, chipName, chipTitle); err == nil {
			// Arm dismiss-on-typing: the pill is now shown but not
			// engaged, so typing in the user's app should dismiss it.
			setSpotlightDismissOnKey(true)
			return
		} else if err != ErrSpotlightUnavailable {
			log.Printf("[quickchat] spotlight.Show failed: %v — falling back to NSPanel", err)
		}
	}
	a.ShowQuickChatWindow(bundleID)
	wailsruntime.EventsEmit(a.ctx, "quickChat:invoke",
		QuickChatInvokeEvent{FrontmostBundleID: bundleID, NativeWindow: true})
}
