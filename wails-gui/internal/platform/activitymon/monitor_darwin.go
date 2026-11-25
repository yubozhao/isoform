//go:build darwin

package activitymon

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework CoreGraphics -framework ApplicationServices -framework Cocoa -framework Carbon

#include <CoreGraphics/CoreGraphics.h>
#include <ApplicationServices/ApplicationServices.h>
#include <Carbon/Carbon.h>
#include "main_queue_darwin.h"
#import <Cocoa/Cocoa.h>
#include <stdbool.h>
#include <stdatomic.h>
#include <pthread.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>
#include "ax_value_darwin.h"

// ── C-side event queue ───────────────────────────────────────────────
// Events are pushed into a C ring buffer; Go drains it when woken via a
// self-pipe (see _notifyFd below). Avoids calling back into Go from
// event-source threads, which would conflict with cgo type semantics and
// risk blocking CGEventTap past its ~1s hard timeout.

#define MAX_EVENT_QUEUE 512

typedef struct {
    long long timestampMs;
    char kind[32];
    char appBundleID[256];
    char appName[256];
    char windowTitle[512];
    double clickX, clickY;
    char clickRole[128];
    char clickLabel[256];
    unsigned short keyCode;
    unsigned long long modifiers;
    char keyChar[16];
    char focusRole[128];
    char focusLabel[256];
    // Typed-text capture (IME-aware): tail-capped snapshot of the focused
    // text element's value at this event, plus whether the read succeeded
    // and whether a composing input method is active. See the typed-text
    // section below.
    char fieldText[1536];
    bool fieldTextValid;
    bool imeComposing;
} CActivityEvent;

static CActivityEvent _eventQueue[MAX_EVENT_QUEUE];
static int _eventQueueHead = 0;
static int _eventQueueTail = 0;
static pthread_mutex_t _eventQueueMutex = PTHREAD_MUTEX_INITIALIZER;

// Ring buffer health counters. Updated inside _eventQueueMutex in
// push_event; read under the same lock via get_ring_stats so the Go
// accessor observes a consistent (peak, drops) pair without needing
// a separate pair-atomic primitive. A persistent ring_drops > 0 or a
// peak approaching MAX_EVENT_QUEUE means the Go drain goroutine is
// falling behind producer bursts.
static int _ringPeakDepth = 0;                // high-water mark since start
static unsigned long long _ringDrops = 0;     // events dropped on queue full

// Self-pipe notification: when push_event transitions the queue from empty
// to non-empty, we write 1 byte so the Go drain goroutine (blocked on the
// pipe's read end) wakes up. Go owns the fd; -1 means "no consumer".
static int _notifyFd = -1;
static pthread_mutex_t _notifyFdMutex = PTHREAD_MUTEX_INITIALIZER;

static long long current_time_millis(void) {
    return (long long)((CFAbsoluteTimeGetCurrent() + kCFAbsoluteTimeIntervalSince1970) * 1000.0);
}

// set_notify_fd registers (fd >= 0) or clears (-1) the wake-up fd.
// The caller retains ownership of the fd; this function never closes it.
// On return with fd == -1, any in-flight write from push_event has already
// completed (serialized through _notifyFdMutex), so the caller can safely
// close the pipe without racing against a late write to a reused fd.
static void set_notify_fd(int fd) {
    pthread_mutex_lock(&_notifyFdMutex);
    _notifyFd = fd;
    if (fd >= 0) {
        // Defensive: Go's os.Pipe already sets O_NONBLOCK, but enforce it
        // so a pathological pipe-full scenario can never stall the
        // CGEventTap thread past its timeout.
        int flags = fcntl(fd, F_GETFL, 0);
        if (flags >= 0) fcntl(fd, F_SETFL, flags | O_NONBLOCK);
        // Prime one wakeup unconditionally. push_event only writes on
        // the ring's empty→non-empty transition, so any event already
        // sitting in the ring at registration time (pushed between tap
        // enable and this call, or by a previous session's in-flight
        // enrichment block) would otherwise suppress every future
        // wakeup and stall the drain goroutine permanently. A spurious
        // wake costs one no-op drain; a missed one kills the stream.
        char b = 1;
        ssize_t n = write(fd, &b, 1);
        (void)n;
    }
    pthread_mutex_unlock(&_notifyFdMutex);
}

static void push_event(CActivityEvent *evt) {
    if (evt->timestampMs == 0) evt->timestampMs = current_time_millis();

    pthread_mutex_lock(&_eventQueueMutex);
    bool wasEmpty = (_eventQueueHead == _eventQueueTail);
    int next = (_eventQueueTail + 1) % MAX_EVENT_QUEUE;
    if (next != _eventQueueHead) {
        memcpy(&_eventQueue[_eventQueueTail], evt, sizeof(CActivityEvent));
        _eventQueueTail = next;
        // Depth after insert. Peak is cheap to maintain here since we
        // already hold the queue lock.
        int depth = (_eventQueueTail - _eventQueueHead + MAX_EVENT_QUEUE) % MAX_EVENT_QUEUE;
        if (depth > _ringPeakDepth) _ringPeakDepth = depth;
    } else {
        _ringDrops++; // queue full, drop event
    }
    pthread_mutex_unlock(&_eventQueueMutex);

    // Wake the Go drain goroutine only on empty→non-empty transitions.
    // Coalescing means bursts generate at most one wakeup, yet the reader's
    // "drain until empty" loop still consumes every event.
    if (wasEmpty) {
        pthread_mutex_lock(&_notifyFdMutex);
        if (_notifyFd >= 0) {
            char b = 1;
            ssize_t n = write(_notifyFd, &b, 1);
            (void)n; // best-effort: EAGAIN means notification already pending
        }
        pthread_mutex_unlock(&_notifyFdMutex);
    }
}

// pop_event returns true if an event was dequeued.
static bool pop_event(CActivityEvent *out) {
    pthread_mutex_lock(&_eventQueueMutex);
    if (_eventQueueHead == _eventQueueTail) {
        pthread_mutex_unlock(&_eventQueueMutex);
        return false;
    }
    memcpy(out, &_eventQueue[_eventQueueHead], sizeof(CActivityEvent));
    _eventQueueHead = (_eventQueueHead + 1) % MAX_EVENT_QUEUE;
    pthread_mutex_unlock(&_eventQueueMutex);
    return true;
}

// get_ring_stats returns the ring buffer health counters as an out-pair.
// Held under _eventQueueMutex so (peak, drops) are read atomically with
// respect to any concurrent push_event, at negligible cost.
static void get_ring_stats(int *peak, unsigned long long *drops) {
    pthread_mutex_lock(&_eventQueueMutex);
    if (peak) *peak = _ringPeakDepth;
    if (drops) *drops = _ringDrops;
    pthread_mutex_unlock(&_eventQueueMutex);
}

// ── State ────────────────────────────────────────────────────────────
static CFMachPortRef _eventTap = NULL;
static CFRunLoopSourceRef _runLoopSource = NULL;
// Written by the tap thread, read from _axQueue blocks and the Go
// caller's thread (stop) — atomic for the same reason as
// _monitorRunning below. Lifecycle stays safe regardless: the NULL
// write only happens after CFRunLoopStop has taken effect, so a
// non-NULL read always refers to a live run loop.
static CFRunLoopRef _Atomic _tapRunLoop = NULL;
static id _workspaceObserver = nil;
static AXObserverRef _axObserver = NULL;
static pid_t _observedPID = 0;
// Written by the Go caller's thread (start/stop), read from the tap run
// loop and _axQueue blocks — atomic so the cross-thread flag is not a
// formal data race. Plain assignment/read syntax keeps seq_cst semantics.
static _Atomic bool _monitorRunning = false;

// _axQueue serializes every synchronous AX query (window title, element
// inspection, observer setup) plus the push_event calls that depend on
// them. AX queries are Mach IPC into the target app and can stall up to
// AX_TIMEOUT_SECS per call even with messaging timeouts installed, so
// they must NEVER run on the CGEventTap run loop: time spent there
// delays session-wide event delivery (even for a listen-only tap) and a
// slow target app's AX server — Qt/Chromium hybrid apps are repeat
// offenders — degrades every app's keyboard latency, with IME
// composition in web-view inputs as the first visible casualty.
//
// Serial on purpose: it preserves ordering among all enriched events
// (key/click/focus/app_switch now flow through one queue) and protects
// the non-atomic _axObserver/_observedPID/_titleCache* globals without
// extra locking. Created once in start_activity_monitor before any
// producer can dispatch to it; never destroyed (stop() may leave
// in-flight blocks, and an idle GCD queue costs nothing).
static dispatch_queue_t _axQueue = NULL;

// ── Helper: get frontmost app info ───────────────────────────────────
static pid_t get_frontmost_app(char* bundleID, int bidLen,
                              char* appName, int nameLen) {
    @autoreleasepool {
        NSRunningApplication *front = activitymon_frontmost_app();
        if (front) {
            const char *bid = [front.bundleIdentifier UTF8String];
            const char *name = [front.localizedName UTF8String];
            if (bid) strncpy(bundleID, bid, bidLen - 1);
            if (name) strncpy(appName, name, nameLen - 1);
            return front.processIdentifier;
        }
        return 0;
    }
}

// ── Helper: get frontmost app PID ──────────────────────────────────
static pid_t frontmost_pid(void) {
    @autoreleasepool {
        NSRunningApplication *front = activitymon_frontmost_app();
        return front ? front.processIdentifier : 0;
    }
}

// ── Helper: resolve app identity from a pid ──────────────────────────
// Key/click enrichment runs on _axQueue, possibly seconds after the
// event when a slow AX server backs the queue up. Sampling the
// frontmost app at enrich time would then misattribute queued events
// to whatever app the user switched to meanwhile, so resolve identity
// from the event's target pid (captured at event time) and only fall
// back to frontmost when the tap carried no usable pid.
static void get_app_for_pid(pid_t pid, char* bundleID, int bidLen,
                            char* appName, int nameLen) {
    if (pid > 0) {
        @autoreleasepool {
            NSRunningApplication *app =
                [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
            if (app) {
                const char *bid = [app.bundleIdentifier UTF8String];
                const char *name = [app.localizedName UTF8String];
                if (bid) strncpy(bundleID, bid, bidLen - 1);
                if (name) strncpy(appName, name, nameLen - 1);
                if (bundleID[0] != '\0' || appName[0] != '\0') return;
            }
        }
    }
    get_frontmost_app(bundleID, bidLen, appName, nameLen);
}

// AX_TIMEOUT_SECS is the hard cap on any single AXUIElement... call.
// Without a timeout, AXUIElementCopyAttributeValue blocks via Mach IPC
// for as long as the target app's AX server takes to respond. Electron
// / CEF apps mid-render-restart and apps whose AXEnhancedUserInterface
// flag is being toggled can stall for 5–10 seconds — long enough to
// trip macOS's main-thread spin watchdog and SIGSEGV the whole process
// (see Yansu-2026-05-27-144221.ips). 500 ms is well under the watchdog
// threshold and well above the p99 of a healthy AX query.
#define AX_TIMEOUT_SECS 0.5f

// ── Helper: get window title via Accessibility ───────────────────────
static void get_window_title(pid_t pid, char* title, int titleLen) {
    AXUIElementRef app = AXUIElementCreateApplication(pid);
    if (!app) return;
    AXUIElementSetMessagingTimeout(app, AX_TIMEOUT_SECS);

    AXUIElementRef focusedWindow = NULL;
    AXError err = AXUIElementCopyAttributeValue(app, kAXFocusedWindowAttribute, (CFTypeRef*)&focusedWindow);
    if (err == kAXErrorSuccess && focusedWindow) {
        if (CFGetTypeID(focusedWindow) != AXUIElementGetTypeID()) {
            CFRelease(focusedWindow);
            CFRelease(app);
            return;
        }
        AXUIElementSetMessagingTimeout(focusedWindow, AX_TIMEOUT_SECS);
        CFStringRef titleRef = NULL;
        err = AXUIElementCopyAttributeValue(focusedWindow, kAXTitleAttribute, (CFTypeRef*)&titleRef);
        if (err == kAXErrorSuccess && titleRef) {
            ax_copy_string(titleRef, title, titleLen);
            CFRelease(titleRef);
        }
        CFRelease(focusedWindow);
    }
    CFRelease(app);
}

// ── Typed-text capture (IME-aware) ───────────────────────────────────
// A CGEventTap sees pre-IME physical keystrokes: under a composing input
// method (Chinese pinyin etc.) the characters the user commits never pass
// through the tap — only the raw pinyin letters and candidate-navigation
// keys do. To recover the committed text, keydown handling snapshots the
// focused text element's AX value at burst boundaries (burst-start and
// commit keydowns; the tap callback runs before the event is delivered to
// the app, so a Return snapshot still sees message text the app is about
// to clear on send). The Go side diffs consecutive snapshots into
// ActivityEvent.TypedText. Secure fields are excluded by the role
// allowlist here, and macOS additionally suppresses tap keystrokes
// entirely while a secure field holds focus.

// g_imeComposing caches whether a composing input method is active. It is
// WRITTEN only on the main thread (refresh_ime_state, driven by the TIS
// "selected input source changed" notification) and READ on the event-tap
// thread. Carbon TIS APIs are main-thread-affine, so the tap thread must
// never call them directly — it reads this cached flag instead. volatile so
// the tap thread observes the latest main-thread write (a one-event staleness
// at an input-source switch is harmless).
static volatile bool g_imeComposing = false;
static long long _lastKeydownMs = 0;

// refresh_ime_state samples the current keyboard input source. Source type
// kTISTypeKeyboardInputMode means a composing IME (pinyin, kana, hangul…)
// rather than a plain key layout. MAIN THREAD ONLY (TIS is main-thread-affine);
// invoked at observer setup and on each input-source-change notification.
static void refresh_ime_state(void) {
    TISInputSourceRef src = TISCopyCurrentKeyboardInputSource();
    bool ime = false;
    if (src) {
        CFStringRef type = (CFStringRef)TISGetInputSourceProperty(src, kTISPropertyInputSourceType);
        ime = (type && CFEqual(type, kTISTypeKeyboardInputMode));
        CFRelease(src);
    }
    g_imeComposing = ime;
}

// imeInputSourceChanged is the distributed-notification callback for
// kTISNotifySelectedKeyboardInputSourceChanged. Delivered on the main run
// loop (the observer is registered from the main queue), so it is safe to
// call the main-thread-affine TIS API here.
static void imeInputSourceChanged(CFNotificationCenterRef center, void *observer,
                                  CFNotificationName name, const void *object,
                                  CFDictionaryRef userInfo) {
    refresh_ime_state();
}

// setup_ime_observer registers the input-source-change observer and seeds the
// initial state. MAIN THREAD ONLY.
static void setup_ime_observer(void) {
    refresh_ime_state();
    CFNotificationCenterAddObserver(
        CFNotificationCenterGetDistributedCenter(),
        NULL,
        imeInputSourceChanged,
        kTISNotifySelectedKeyboardInputSourceChanged,
        NULL,
        CFNotificationSuspensionBehaviorCoalesce);
}

// copy_cfstring_tail copies the TAIL of s into buf (UTF-8, NUL-terminated).
// Typing appends at the end of a field, so when a long document overflows
// the cap the tail window keeps the caret region the differ cares about.
static void copy_cfstring_tail(CFStringRef s, char *buf, int bufLen) {
    buf[0] = 0;
    CFIndex len = CFStringGetLength(s);
    // 3 bytes per UTF-16 unit is the densest case that matters (CJK = 3
    // bytes / 1 unit; surrogate pairs = 4 bytes / 2 units).
    CFIndex take = (bufLen - 1) / 3;
    if (take <= 0) return;
    if (take > len) take = len;
    CFRange r = CFRangeMake(len - take, take);
    if (r.location > 0) {
        // Don't split a surrogate pair at the window start.
        UniChar c = CFStringGetCharacterAtIndex(s, r.location);
        if (c >= 0xDC00 && c <= 0xDFFF) { r.location++; r.length--; }
    }
    if (r.length <= 0) return;
    CFStringRef sub = CFStringCreateWithSubstring(NULL, s, r);
    if (!sub) return;
    if (!CFStringGetCString(sub, buf, bufLen, kCFStringEncodingUTF8)) buf[0] = 0;
    CFRelease(sub);
}

// read_focused_field_text snapshots the focused element's text value when
// it is a readable, non-secure text element. Returns true only on a
// successful read so the Go differ never compares snapshots from
// unreadable contexts (terminals, canvases, secure fields).
static bool read_focused_field_text(pid_t pid, char *buf, int bufLen) {
    buf[0] = 0;
    AXUIElementRef app = AXUIElementCreateApplication(pid);
    if (!app) return false;
    AXUIElementSetMessagingTimeout(app, AX_TIMEOUT_SECS);

    AXUIElementRef focused = NULL;
    AXError err = AXUIElementCopyAttributeValue(app, kAXFocusedUIElementAttribute, (CFTypeRef *)&focused);
    CFRelease(app);
    if (err != kAXErrorSuccess || !focused) return false;
    AXUIElementSetMessagingTimeout(focused, AX_TIMEOUT_SECS);

    bool ok = false;
    CFStringRef roleRef = NULL;
    if (AXUIElementCopyAttributeValue(focused, kAXRoleAttribute, (CFTypeRef *)&roleRef) == kAXErrorSuccess && roleRef) {
        bool textual = CFEqual(roleRef, CFSTR("AXTextField")) ||
                       CFEqual(roleRef, CFSTR("AXTextArea")) ||
                       CFEqual(roleRef, CFSTR("AXSearchField")) ||
                       CFEqual(roleRef, CFSTR("AXComboBox"));
        CFRelease(roleRef);
        if (textual) {
            CFTypeRef valRef = NULL;
            if (AXUIElementCopyAttributeValue(focused, kAXValueAttribute, &valRef) == kAXErrorSuccess && valRef) {
                if (CFGetTypeID(valRef) == CFStringGetTypeID()) {
                    copy_cfstring_tail((CFStringRef)valRef, buf, bufLen);
                    ok = true;
                }
                CFRelease(valRef);
            }
        }
    }
    CFRelease(focused);
    return ok;
}

// ── Window-title cache (confined to _axQueue — no locking) ──────────
// Keyboard events arrive at typing rate; querying the focused window
// title over AX for every keystroke multiplies Mach IPC against the
// target app and, when its AX server is slow, backs up _axQueue by up
// to 2×AX_TIMEOUT_SECS per keystroke. Titles are effectively static
// while the user types, so the high-rate keyboard path tolerates a
// short TTL. Callers that fire on title-changing transitions (app
// switch, focus change, clicks) pass maxAgeMs=0 to force a fresh query,
// which also reseeds the cache for the keystrokes that follow.
//
// An empty result is cached too — deliberate: a hung AX server gets
// probed once per TTL instead of once per keystroke.
#define TITLE_CACHE_TTL_MS 3000
static pid_t _titleCachePid = 0;
static long long _titleCacheAtMs = 0;
static char _titleCache[512];

static void get_window_title_cached(pid_t pid, char* title, int titleLen,
                                    long long maxAgeMs) {
    // Self-protect instead of relying on callers pre-zeroing the buffer:
    // get_window_title leaves it untouched on failure, and its content
    // is copied into _titleCache below.
    if (titleLen > 0) title[0] = '\0';
    long long now = current_time_millis();
    if (maxAgeMs > 0 && pid == _titleCachePid &&
        (now - _titleCacheAtMs) <= maxAgeMs) {
        strncpy(title, _titleCache, titleLen - 1);
        return;
    }
    get_window_title(pid, title, titleLen);
    _titleCachePid = pid;
    _titleCacheAtMs = now;
    memset(_titleCache, 0, sizeof(_titleCache));
    strncpy(_titleCache, title, sizeof(_titleCache) - 1);
}

// ── Helper: try to extract a text label from a single AX element ─────
// Returns a retained CFStringRef if found, NULL otherwise.
static CFStringRef copy_element_label(AXUIElementRef element) {
    CFStringRef labelRef = NULL;
    AXError err;

    // 1. Title (buttons, menu items, windows)
    err = AXUIElementCopyAttributeValue(element, kAXTitleAttribute, (CFTypeRef*)&labelRef);
    if (err == kAXErrorSuccess && ax_is_string(labelRef) && CFStringGetLength(labelRef) > 0) return labelRef;
    if (labelRef) { CFRelease(labelRef); labelRef = NULL; }

    // 2. Description (images, icons, toolbar items)
    err = AXUIElementCopyAttributeValue(element, kAXDescriptionAttribute, (CFTypeRef*)&labelRef);
    if (err == kAXErrorSuccess && ax_is_string(labelRef) && CFStringGetLength(labelRef) > 0) return labelRef;
    if (labelRef) { CFRelease(labelRef); labelRef = NULL; }

    // 3. Value (AXStaticText, text fields, checkboxes)
    CFTypeRef valRef = NULL;
    err = AXUIElementCopyAttributeValue(element, kAXValueAttribute, &valRef);
    if (err == kAXErrorSuccess && valRef) {
        if (CFGetTypeID(valRef) == CFStringGetTypeID()) {
            CFStringRef s = (CFStringRef)valRef;
            if (CFStringGetLength(s) > 0) return (CFStringRef)CFRetain(s);
        }
        CFRelease(valRef);
    }

    // 4. Help text
    err = AXUIElementCopyAttributeValue(element, kAXHelpAttribute, (CFTypeRef*)&labelRef);
    if (err == kAXErrorSuccess && ax_is_string(labelRef) && CFStringGetLength(labelRef) > 0) return labelRef;
    if (labelRef) { CFRelease(labelRef); labelRef = NULL; }

    // 5. Placeholder (empty text fields)
    err = AXUIElementCopyAttributeValue(element, kAXPlaceholderValueAttribute, (CFTypeRef*)&labelRef);
    if (err == kAXErrorSuccess && ax_is_string(labelRef) && CFStringGetLength(labelRef) > 0) return labelRef;
    if (labelRef) { CFRelease(labelRef); labelRef = NULL; }

    return NULL;
}

// Hard wall-clock budget for one click's element inspection.
// collect_children_text can fan out to hundreds of AX round-trips, each
// stalling up to AX_TIMEOUT_SECS against a slow-but-alive AX server —
// without a budget a single click could wedge _axQueue for minutes.
// The budget can overshoot by at most one in-flight AX call.
#define ELEMENT_INSPECT_BUDGET_MS 1000

// ── Helper: recursively collect text from children (depth-limited) ───
// Appends text from AXStaticText / AXTextField children into buf.
static void collect_children_text(AXUIElementRef element, char* buf, int bufLen,
                                  int depth, long long deadlineMs) {
    if (depth <= 0) return;
    if ((int)strlen(buf) >= bufLen - 2) return;
    if (current_time_millis() > deadlineMs) return;

    CFArrayRef children = NULL;
    AXError err = AXUIElementCopyAttributeValue(element, kAXChildrenAttribute, (CFTypeRef*)&children);
    if (err != kAXErrorSuccess || !children) return;

    CFIndex count = CFArrayGetCount(children);
    // Limit to first 20 children to avoid expensive traversals
    if (count > 20) count = 20;

    for (CFIndex i = 0; i < count; i++) {
        if (current_time_millis() > deadlineMs) break;
        AXUIElementRef child = (AXUIElementRef)CFArrayGetValueAtIndex(children, i);
        if (!child) continue;

        // Try to get text from this child
        CFStringRef text = copy_element_label(child);
        if (text) {
            int curLen = (int)strlen(buf);
            if (curLen > 0 && curLen < bufLen - 2) {
                strncat(buf, " ", bufLen - curLen - 1);
                curLen++;
            }
            if (curLen < bufLen - 1) {
                char tmp[512];
                if (CFStringGetCString(text, tmp, sizeof(tmp), kCFStringEncodingUTF8)) {
                    strncat(buf, tmp, bufLen - curLen - 1);
                }
            }
            CFRelease(text);
        }

        // Recurse into children
        collect_children_text(child, buf, bufLen, depth - 1, deadlineMs);
    }

    CFRelease(children);
}

// ── Helper: get AX element info at a screen position ─────────────────
static void get_element_at_position(pid_t pid, double x, double y,
                                     char* role, int roleLen,
                                     char* label, int labelLen) {
    long long deadline = current_time_millis() + ELEMENT_INSPECT_BUDGET_MS;

    AXUIElementRef app = AXUIElementCreateApplication(pid);
    if (!app) return;
    AXUIElementSetMessagingTimeout(app, AX_TIMEOUT_SECS);

    AXUIElementRef element = NULL;
    AXError err = AXUIElementCopyElementAtPosition(app, (float)x, (float)y, &element);
    CFRelease(app);
    if (err != kAXErrorSuccess || !element) return;
    AXUIElementSetMessagingTimeout(element, AX_TIMEOUT_SECS);

    // Role (e.g. AXButton, AXMenuItem, AXLink, AXStaticText)
    CFStringRef roleRef = NULL;
    err = AXUIElementCopyAttributeValue(element, kAXRoleAttribute, (CFTypeRef*)&roleRef);
    if (err == kAXErrorSuccess && roleRef) {
        ax_copy_string(roleRef, role, roleLen);
        CFRelease(roleRef);
    }

    // Try to get label directly from the clicked element
    CFStringRef labelRef = copy_element_label(element);
    if (labelRef) {
        ax_copy_string(labelRef, label, labelLen);
        CFRelease(labelRef);
    }

    // If still empty, collect text from child elements (2 levels deep).
    // Many buttons/cells contain child AXStaticText with the visible text.
    if (label[0] == '\0') {
        collect_children_text(element, label, labelLen, 2, deadline);
    }

    CFRelease(element);
}

extern int activityMonitorCaptureAllowed(void);

// Process the retained event on the serial AX queue. Neither a preference
// read nor an AX request belongs on the system input delivery thread.
static void processTapEvent(CGEventType type, CGEventRef event, long long ts) {
    if (!_monitorRunning || !activityMonitorCaptureAllowed()) return;
    switch (type) {
    case kCGEventLeftMouseDown:
    case kCGEventRightMouseDown: {
        CGPoint loc = CGEventGetLocation(event);
        pid_t pid = (pid_t)CGEventGetIntegerValueField(event, kCGEventTargetUnixProcessID);
        bool maybeBurstEnd = (pid > 0 && (ts - _lastKeydownMs) < 30000);
        CActivityEvent evt;
        memset(&evt, 0, sizeof(evt));
        evt.timestampMs = ts;
        strncpy(evt.kind, "click", sizeof(evt.kind) - 1);
        get_app_for_pid(pid, evt.appBundleID, sizeof(evt.appBundleID),
                        evt.appName, sizeof(evt.appName));
        // Fresh title (maxAge 0): a click can navigate or retitle
        // the window, and clicks are low-rate enough to afford it.
        if (pid > 0) {
            get_window_title_cached(pid, evt.windowTitle, sizeof(evt.windowTitle), 0);
        }
        evt.clickX = loc.x;
        evt.clickY = loc.y;
        if (pid > 0) {
            get_element_at_position(pid, loc.x, loc.y,
                                    evt.clickRole, sizeof(evt.clickRole),
                                    evt.clickLabel, sizeof(evt.clickLabel));
        }
        // Typed-text capture: snapshot the focused field so the Go differ
        // can recover committed text. Runs on _axQueue (not the tap
        // thread); the read reflects field state at dispatch time, which
        // for a send-button click may already be cleared.
        if (maybeBurstEnd) {
            evt.fieldTextValid = read_focused_field_text(pid, evt.fieldText, sizeof(evt.fieldText));
        }
        push_event(&evt);
        break;
    }
    case kCGEventKeyDown: {
        CActivityEvent evt;
        memset(&evt, 0, sizeof(evt));
        evt.timestampMs = ts;
        strncpy(evt.kind, "keyboard", sizeof(evt.kind) - 1);
        pid_t pid = (pid_t)CGEventGetIntegerValueField(event, kCGEventTargetUnixProcessID);

        evt.keyCode = (unsigned short)CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);
        evt.modifiers = (unsigned long long)CGEventGetFlags(event);

        _lastKeydownMs = ts;
        evt.imeComposing = g_imeComposing;
        if (pid > 0) {
            evt.fieldTextValid = read_focused_field_text(pid, evt.fieldText, sizeof(evt.fieldText));
        }

        CGEventFlags flags = (CGEventFlags)evt.modifiers;
        // Check if any non-shift modifier is held (Command, Control, Option).
        // Shift alone doesn't count as a "shortcut modifier".
        bool hasShortcutMod = (flags & kCGEventFlagMaskCommand) ||
                              (flags & kCGEventFlagMaskControl) ||
                              (flags & kCGEventFlagMaskAlternate);

        UniChar chars[4] = {0};
        UniCharCount len = 0;
        CGEventKeyboardGetUnicodeString(event, 4, &len, chars);

        if (!hasShortcutMod && len > 0 && chars[0] >= 32 && chars[0] < 0xF700) {
            // Plain character (no modifier shortcut) — store as-is
            @autoreleasepool {
                NSString *s = [NSString stringWithCharacters:chars length:len];
                const char *utf8 = [s UTF8String];
                if (utf8) strncpy(evt.keyChar, utf8, sizeof(evt.keyChar) - 1);
            }
        } else {
            // Modifier shortcut or special key — build readable representation
            // e.g. "⌘V", "⌃⌥Delete", "⇧Tab"
            @autoreleasepool {
                NSMutableString *combo = [NSMutableString string];
                if (flags & kCGEventFlagMaskControl)   [combo appendString:@"⌃"];
                if (flags & kCGEventFlagMaskAlternate)  [combo appendString:@"⌥"];
                if (flags & kCGEventFlagMaskShift)      [combo appendString:@"⇧"];
                if (flags & kCGEventFlagMaskCommand)    [combo appendString:@"⌘"];

                // Map common keyCode values to readable names
                const char *keyName = NULL;
                switch (evt.keyCode) {
                    case 36:  keyName = "Return"; break;
                    case 48:  keyName = "Tab"; break;
                    case 49:  keyName = "Space"; break;
                    case 51:  keyName = "Delete"; break;
                    case 53:  keyName = "Escape"; break;
                    case 117: keyName = "Fwd Delete"; break;
                    case 123: keyName = "Left"; break;
                    case 124: keyName = "Right"; break;
                    case 125: keyName = "Down"; break;
                    case 126: keyName = "Up"; break;
                    case 115: keyName = "Home"; break;
                    case 119: keyName = "End"; break;
                    case 116: keyName = "Page Up"; break;
                    case 121: keyName = "Page Down"; break;
                    default: break;
                }

                if (keyName) {
                    [combo appendFormat:@"%s", keyName];
                } else {
                    // Resolve key character from keyCode lookup table
                    // macOS keyCodes: 0=A, 1=S, 2=D, 3=F, ..., 11=B, 12=Q, ...
                    static const char kKeyCodeToChar[] =
                        "asdfhgzxcv" // 0-9
                        "\0bqweryt"  // 10-17
                        "123456=97"  // 18-26
                        "-80\0o]"    // 27-32
                        "u[ip\0l"    // 33-38
                        "j'k;\\,"   // 39-44
                        "/nm."       // 45-48
                        ;
                    if (evt.keyCode < sizeof(kKeyCodeToChar) && kKeyCodeToChar[evt.keyCode] != '\0') {
                        char upper = kKeyCodeToChar[evt.keyCode];
                        if (upper >= 'a' && upper <= 'z') upper -= 32;
                        [combo appendFormat:@"%c", upper];
                    } else if (len > 0 && chars[0] >= 0xF700) {
                        // Function key range
                        [combo appendFormat:@"F%d", (int)(chars[0] - 0xF704 + 1)];
                    }
                }

                if ([combo length] > 0) {
                    const char *utf8 = [combo UTF8String];
                    if (utf8) strncpy(evt.keyChar, utf8, sizeof(evt.keyChar) - 1);
                }
            }
        }
        get_app_for_pid(pid, evt.appBundleID, sizeof(evt.appBundleID),
                        evt.appName, sizeof(evt.appName));
        if (pid > 0) {
            get_window_title_cached(pid, evt.windowTitle, sizeof(evt.windowTitle),
                                    TITLE_CACHE_TTL_MS);
        }
        push_event(&evt);
        break;
    }
    default:
        break;
    }
}

static CGEventRef eventTapCallback(CGEventTapProxy proxy, CGEventType type,
                                   CGEventRef event, void *userInfo) {
    if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
        if (_eventTap) CGEventTapEnable(_eventTap, true);
        return event;
    }
    long long ts = current_time_millis();
    CFRetain(event);
    dispatch_async(_axQueue, ^{
        processTapEvent(type, event, ts);
        CFRelease(event);
    });
    return event;
}

// ── AXObserver callback ──────────────────────────────────────────────
// Fires on the tap run loop (its run loop source is added to
// _tapRunLoop), so it obeys the same hard constraint as
// eventTapCallback: no synchronous AX IPC here — a hung target would
// wedge the loop and stall event-tap servicing for clicks/keys exactly
// when the user clicks into an input field. Retain the element and do
// all queries on _axQueue.
static void axFocusCallback(AXObserverRef observer, AXUIElementRef element,
                            CFStringRef notification, void *userInfo) {
    if (!element || CFGetTypeID(element) != AXUIElementGetTypeID()) return;
    long long ts = current_time_millis();
    CFRetain(element);
    dispatch_async(_axQueue, ^{
        if (!_monitorRunning || !activityMonitorCaptureAllowed()) {
            CFRelease(element);
            return;
        }
        // Defensive timeout: the element's AXMessagingTimeout inherits
        // from the AXUIElementRef the observer was registered on, but we
        // set it again to cover elements freshly minted by AX
        // (kAXFocusedUIElement) which carry their own message ports.
        AXUIElementSetMessagingTimeout(element, AX_TIMEOUT_SECS);

        CActivityEvent evt;
        memset(&evt, 0, sizeof(evt));
        evt.timestampMs = ts;
        strncpy(evt.kind, "focus", sizeof(evt.kind) - 1);

        // The element knows which app it belongs to (local call, no
        // IPC) — more accurate than frontmost-at-enrich-time.
        pid_t epid = 0;
        AXUIElementGetPid(element, &epid);
        get_app_for_pid(epid, evt.appBundleID, sizeof(evt.appBundleID),
                        evt.appName, sizeof(evt.appName));

        CFStringRef roleRef = NULL;
        AXError err = AXUIElementCopyAttributeValue(element, kAXRoleAttribute, (CFTypeRef*)&roleRef);
        if (err == kAXErrorSuccess && roleRef) {
            ax_copy_string(roleRef, evt.focusRole, sizeof(evt.focusRole));
            CFRelease(roleRef);
        }

        CFStringRef labelRef = NULL;
        err = AXUIElementCopyAttributeValue(element, kAXTitleAttribute, (CFTypeRef*)&labelRef);
        if (err != kAXErrorSuccess || !labelRef) {
            err = AXUIElementCopyAttributeValue(element, kAXDescriptionAttribute, (CFTypeRef*)&labelRef);
        }
        if (err == kAXErrorSuccess && labelRef) {
            ax_copy_string(labelRef, evt.focusLabel, sizeof(evt.focusLabel));
            CFRelease(labelRef);
        }

        @autoreleasepool {
            pid_t tpid = epid;
            if (tpid <= 0) tpid = frontmost_pid();
            if (tpid > 0) {
                // Fresh title (maxAge 0): focus changes often retitle
                // the window (tab switches), and this reseeds the cache
                // for the keystrokes that typically follow.
                get_window_title_cached(tpid, evt.windowTitle,
                                        sizeof(evt.windowTitle), 0);
            }
        }

        push_event(&evt);
        CFRelease(element);
    });
}

// ── Setup AX observer for a given PID ────────────────────────────────
static void setup_ax_observer(pid_t pid) {
    if (_axObserver) {
        CFRunLoopSourceRef src = AXObserverGetRunLoopSource(_axObserver);
        if (src && _tapRunLoop) {
            CFRunLoopRemoveSource(_tapRunLoop, src, kCFRunLoopDefaultMode);
        }
        CFRelease(_axObserver);
        _axObserver = NULL;
    }
    _observedPID = 0;

    // A workspace notification already in flight when the monitor is
    // stopped must not resurrect an observer after stop's teardown
    // block (serialized on this same queue) has run.
    if (!_monitorRunning || pid <= 0) return;

    AXError err = AXObserverCreate(pid, axFocusCallback, &_axObserver);
    if (err != kAXErrorSuccess || !_axObserver) return;

    AXUIElementRef app = AXUIElementCreateApplication(pid);
    if (!app) {
        CFRelease(_axObserver);
        _axObserver = NULL;
        return;
    }
    AXUIElementSetMessagingTimeout(app, AX_TIMEOUT_SECS);

    err = AXObserverAddNotification(_axObserver, app,
        kAXFocusedUIElementChangedNotification, NULL);
    CFRelease(app);

    if (err != kAXErrorSuccess) {
        CFRelease(_axObserver);
        _axObserver = NULL;
        return;
    }

    if (_tapRunLoop) {
        CFRunLoopAddSource(_tapRunLoop,
            AXObserverGetRunLoopSource(_axObserver),
            kCFRunLoopDefaultMode);
    }
    _observedPID = pid;
}

// ── NSWorkspace app activation observer ──────────────────────────────
//
// NSWorkspace posts its notifications on the main thread. The earlier
// implementation used `queue:nil` AND did synchronous AX work inside
// the block — which meant a slow target app's AX server (Electron
// mid-renderer-restart, AXEnhancedUserInterface mid-toggle) would
// block the main thread long enough for macOS's spin watchdog to
// SIGSEGV the whole app (see Yansu-2026-05-27-144221.ips, faulting
// thread = com.apple.main-thread inside _AXUIElementCopyAttributeValue
// inside this observer block).
//
// Fix: dispatch the AX-touching work to a background queue. We still
// receive the notification on the main thread (NSWorkspace's contract),
// but the heavy work runs on a serial GCD queue so the main thread
// returns to its run loop within microseconds.
//
// The AX-touching work goes to _axQueue (see its declaration for the
// full rationale); serial dispatch preserves the "one observer at a
// time" invariant for setup_ax_observer without extra locking.
static void setup_workspace_observer(void) {
    @autoreleasepool {
        // A stop() racing this deferred registration must not leave a
        // live observer behind (it would double-register on restart).
        if (!_monitorRunning) return;
        pid_t self_pid = getpid();
        _workspaceObserver = [[[NSWorkspace sharedWorkspace] notificationCenter]
            addObserverForName:NSWorkspaceDidActivateApplicationNotification
            object:nil
            queue:nil
            usingBlock:^(NSNotification *note) {
                NSRunningApplication *app = note.userInfo[NSWorkspaceApplicationKey];
                if (!app) return;
                pid_t pid = app.processIdentifier;
                // Skip self-activations. Yansu observing itself adds noise to
                // the activity stream AND has caused recursive AX queries
                // back into our own process.
                if (pid == self_pid) return;
                long long ts = current_time_millis();
                dispatch_async(_axQueue, ^{
                    if (!_monitorRunning || !activityMonitorCaptureAllowed()) return;
                    NSString *bundleID = app.bundleIdentifier;
                    NSString *appName = app.localizedName;
                    CActivityEvent evt;
                    memset(&evt, 0, sizeof(evt));
                    evt.timestampMs = ts;
                    strncpy(evt.kind, "app_switch", sizeof(evt.kind) - 1);

                    const char *bid = [bundleID UTF8String];
                    const char *name = [appName UTF8String];
                    if (bid) strncpy(evt.appBundleID, bid, sizeof(evt.appBundleID) - 1);
                    if (name) strncpy(evt.appName, name, sizeof(evt.appName) - 1);
                    // Fresh title (maxAge 0): the window just changed.
                    // This also reseeds the title cache for the
                    // keystrokes that follow the app switch.
                    get_window_title_cached(pid, evt.windowTitle, sizeof(evt.windowTitle), 0);

                    push_event(&evt);

                    setup_ax_observer(pid);
                });
            }];
    }
}

// ── Start monitoring ─────────────────────────────────────────────────
bool start_activity_monitor(void) {
    if (_monitorRunning) return true;

    // Discard events left over from a previous session: enrichment
    // blocks still in flight on _axQueue at Stop() time push into the
    // ring after the notify fd is cleared. Those events belong to the
    // stopped session and must not leak into this one.
    pthread_mutex_lock(&_eventQueueMutex);
    _eventQueueHead = _eventQueueTail;
    pthread_mutex_unlock(&_eventQueueMutex);

    // Must exist before the tap or any observer can fire: every
    // callback dispatches its AX enrichment onto this queue.
    if (_axQueue == NULL) {
        _axQueue = dispatch_queue_create("com.yansu.activitymon.ax-enrich",
                                         DISPATCH_QUEUE_SERIAL);
    }

    CGEventMask mask = (1 << kCGEventLeftMouseDown) |
                       (1 << kCGEventRightMouseDown) |
                       (1 << kCGEventKeyDown);

    _eventTap = CGEventTapCreate(
        kCGSessionEventTap,
        kCGHeadInsertEventTap,
        kCGEventTapOptionListenOnly,
        mask,
        eventTapCallback,
        NULL
    );

    if (!_eventTap) return false;

    _runLoopSource = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, _eventTap, 0);
    if (!_runLoopSource) {
        CFRelease(_eventTap);
        _eventTap = NULL;
        return false;
    }

    _monitorRunning = true;

    dispatch_async(dispatch_get_global_queue(QOS_CLASS_UTILITY, 0), ^{
        _tapRunLoop = CFRunLoopGetCurrent();
        CFRunLoopAddSource(_tapRunLoop, _runLoopSource, kCFRunLoopDefaultMode);
        CGEventTapEnable(_eventTap, true);

        // Register the workspace observer only AFTER _tapRunLoop is
        // published (the dispatch chain orders the write), so an app
        // switch arriving right after start cannot reach
        // setup_ax_observer while _tapRunLoop is still NULL — that
        // would silently skip adding the focus observer's run loop
        // source and lose focus events until the next app switch.
        dispatch_async(dispatch_get_main_queue(), ^{
            setup_workspace_observer();
            // IME observer (TIS is main-thread-affine): seeds composing-state
            // for IME-aware typed-text capture and watches input-source changes.
            setup_ime_observer();
        });

        @autoreleasepool {
            NSRunningApplication *front = activitymon_frontmost_app();
            if (front) {
                // setup_ax_observer makes synchronous AX calls
                // (AXObserverCreate/AddNotification) — keep them off
                // this run loop, and queue-confine all mutations of
                // _axObserver/_observedPID.
                pid_t fpid = front.processIdentifier;
                dispatch_async(_axQueue, ^{
                    setup_ax_observer(fpid);
                });
            }
        }

        CFRunLoopRun();
        _tapRunLoop = NULL;
    });

    return true;
}

// ── Stop monitoring ──────────────────────────────────────────────────
void stop_activity_monitor(void) {
    _monitorRunning = false;

    if (_workspaceObserver) {
        dispatch_async(dispatch_get_main_queue(), ^{
            [[[NSWorkspace sharedWorkspace] notificationCenter] removeObserver:_workspaceObserver];
            _workspaceObserver = nil;
        });
    }

    // Disable the tap BEFORE the _axQueue barrier below, so the callback
    // stops producing new enrichment blocks and the barrier flushes
    // everything already dispatched. (Events already queued on the tap's
    // mach port can still fire the callback briefly after disable — the
    // _monitorRunning guard at the top of each enrichment block catches
    // those.)
    if (_eventTap) {
        CGEventTapEnable(_eventTap, false);
    }

    // _axObserver/_observedPID are queue-confined to _axQueue; tear them
    // down there too. dispatch_sync is safe (stop never runs on _axQueue)
    // and keeps the ordering guarantee that the observer is gone before
    // the run loop below is stopped. It also acts as a barrier: any
    // in-flight setup_ax_observer block runs before this one, and any
    // dispatched after sees _monitorRunning == false and bails.
    if (_axQueue != NULL) {
        dispatch_sync(_axQueue, ^{
            if (_axObserver) {
                if (_tapRunLoop) {
                    CFRunLoopSourceRef src = AXObserverGetRunLoopSource(_axObserver);
                    if (src) CFRunLoopRemoveSource(_tapRunLoop, src, kCFRunLoopDefaultMode);
                }
                CFRelease(_axObserver);
                _axObserver = NULL;
                _observedPID = 0;
            }
        });
    }

    // Drop whatever the flushed blocks pushed after Go cleared the
    // notify fd: nothing will ever drain those events, and they must
    // not leak into the next session. (start_activity_monitor clears
    // again as a second line of defense.)
    pthread_mutex_lock(&_eventQueueMutex);
    _eventQueueHead = _eventQueueTail;
    pthread_mutex_unlock(&_eventQueueMutex);
    if (_runLoopSource && _tapRunLoop) {
        CFRunLoopRemoveSource(_tapRunLoop, _runLoopSource, kCFRunLoopDefaultMode);
    }
    if (_tapRunLoop) {
        CFRunLoopStop(_tapRunLoop);
    }
    if (_runLoopSource) {
        CFRelease(_runLoopSource);
        _runLoopSource = NULL;
    }
    if (_eventTap) {
        CFRelease(_eventTap);
        _eventTap = NULL;
    }
}

bool is_monitor_running(void) {
    return _monitorRunning;
}

// ── Virtual desktop bounds ───────────────────────────────────────────
// Computes the bounding rect of all active displays (the virtual desktop).
// This is needed because CGWindowListCreateImage(CGRectInfinite, ...) captures
// the entire virtual desktop, so window coordinates must be normalized relative
// to the full virtual rect, not just the main display.
static CGRect get_virtual_desktop_rect(void) {
    uint32_t displayCount = 0;
    CGGetActiveDisplayList(0, NULL, &displayCount);
    if (displayCount == 0) {
        CGDirectDisplayID mainDisplay = CGMainDisplayID();
        return CGRectMake(0, 0,
            (double)CGDisplayPixelsWide(mainDisplay),
            (double)CGDisplayPixelsHigh(mainDisplay));
    }

    CGDirectDisplayID displays[32];
    if (displayCount > 32) displayCount = 32;
    CGGetActiveDisplayList(displayCount, displays, &displayCount);

    CGRect virtualRect = CGRectNull;
    for (uint32_t i = 0; i < displayCount; i++) {
        virtualRect = CGRectUnion(virtualRect, CGDisplayBounds(displays[i]));
    }
    return virtualRect;
}

// ── Frontmost window bounds ──────────────────────────────────────────
// Returns the screen-space bounds of the frontmost application's main window.
// Uses CGWindowListCopyWindowInfo to find windows owned by the frontmost app.
// Returns 0,0,0,0 if the window cannot be determined.
void get_frontmost_window_bounds(double* outX, double* outY, double* outW, double* outH,
                                  double* screenW, double* screenH) {
    @autoreleasepool {
        *outX = 0; *outY = 0; *outW = 0; *outH = 0;
        *screenW = 0; *screenH = 0;

        // Get virtual desktop bounds for normalizing coordinates across all displays.
        CGRect vd = get_virtual_desktop_rect();
        *screenW = vd.size.width;
        *screenH = vd.size.height;

        NSRunningApplication *frontApp = activitymon_frontmost_app();
        if (!frontApp) return;
        pid_t pid = [frontApp processIdentifier];

        CFArrayRef windowList = CGWindowListCopyWindowInfo(
            kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
            kCGNullWindowID
        );
        if (!windowList) return;

        CFIndex count = CFArrayGetCount(windowList);
        double bestArea = 0; // pick the largest matching window, not the first
        for (CFIndex i = 0; i < count; i++) {
            CFDictionaryRef info = (CFDictionaryRef)CFArrayGetValueAtIndex(windowList, i);

            // Match by PID
            CFNumberRef pidRef = (CFNumberRef)CFDictionaryGetValue(info, kCGWindowOwnerPID);
            if (!pidRef) continue;
            pid_t winPid = 0;
            CFNumberGetValue(pidRef, kCFNumberIntType, &winPid);
            if (winPid != pid) continue;

            // Skip non-normal windows (menus, tooltips, etc.)
            CFNumberRef layerRef = (CFNumberRef)CFDictionaryGetValue(info, kCGWindowLayer);
            if (layerRef) {
                int layer = 0;
                CFNumberGetValue(layerRef, kCFNumberIntType, &layer);
                if (layer != 0) continue;
            }

            CFDictionaryRef bounds = (CFDictionaryRef)CFDictionaryGetValue(info, kCGWindowBounds);
            if (!bounds) continue;

            CGRect rect;
            if (!CGRectMakeWithDictionaryRepresentation(bounds, &rect)) continue;

            // Pick the LARGEST normal-layer window, not the first. cmux (and
            // similar apps) put a thin overlay window ahead of the real window in
            // z-order — e.g. a title strip at h≈1.7% — so taking the first match
            // yielded a sliver bounds that defeated focus-cropping. The main
            // window is reliably the largest.
            double area = rect.size.width * rect.size.height;
            if (area > bestArea) {
                bestArea = area;
                // Offset by virtual desktop origin so the Go side normalizes correctly.
                *outX = rect.origin.x - vd.origin.x;
                *outY = rect.origin.y - vd.origin.y;
                *outW = rect.size.width;
                *outH = rect.size.height;
            }
        }

        CFRelease(windowList);
    }
}

// ── Visible sensitive app windows ────────────────────────────────────
// Scans all on-screen windows and returns normalized bounds for every
// window whose owning app bundle ID matches a sensitive prefix.
// maxResults caps the output; bundlePrefixes is a NULL-terminated array.
typedef struct {
    double x, y, w, h;
} normalized_rect;

int find_sensitive_windows(const char** bundlePrefixes, int prefixCount,
                           normalized_rect* outRects, int maxResults) {
    @autoreleasepool {
        // Use virtual desktop bounds so coordinates are correct on multi-monitor setups.
        CGRect vd = get_virtual_desktop_rect();
        double screenW = vd.size.width;
        double screenH = vd.size.height;
        if (screenW == 0 || screenH == 0) return 0;

        CFArrayRef windowList = CGWindowListCopyWindowInfo(
            kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
            kCGNullWindowID
        );
        if (!windowList) return 0;

        // Build a PID set of sensitive apps by scanning running applications.
        NSArray<NSRunningApplication*> *runningApps = [[NSWorkspace sharedWorkspace] runningApplications];
        NSMutableSet<NSNumber*> *sensitivePIDs = [NSMutableSet set];
        for (NSRunningApplication *app in runningApps) {
            NSString *bid = app.bundleIdentifier;
            if (!bid) continue;
            for (int p = 0; p < prefixCount; p++) {
                NSString *prefix = [NSString stringWithUTF8String:bundlePrefixes[p]];
                if ([bid.lowercaseString hasPrefix:prefix.lowercaseString]) {
                    [sensitivePIDs addObject:@(app.processIdentifier)];
                    break;
                }
            }
        }

        int found = 0;
        CFIndex count = CFArrayGetCount(windowList);
        for (CFIndex i = 0; i < count && found < maxResults; i++) {
            CFDictionaryRef info = (CFDictionaryRef)CFArrayGetValueAtIndex(windowList, i);

            CFNumberRef pidRef = (CFNumberRef)CFDictionaryGetValue(info, kCGWindowOwnerPID);
            if (!pidRef) continue;
            pid_t winPid = 0;
            CFNumberGetValue(pidRef, kCFNumberIntType, &winPid);
            if (![sensitivePIDs containsObject:@(winPid)]) continue;

            // Skip non-normal windows.
            CFNumberRef layerRef = (CFNumberRef)CFDictionaryGetValue(info, kCGWindowLayer);
            if (layerRef) {
                int layer = 0;
                CFNumberGetValue(layerRef, kCFNumberIntType, &layer);
                if (layer != 0) continue;
            }

            CFDictionaryRef bounds = (CFDictionaryRef)CFDictionaryGetValue(info, kCGWindowBounds);
            if (!bounds) continue;
            CGRect rect;
            if (!CGRectMakeWithDictionaryRepresentation(bounds, &rect)) continue;
            if (rect.size.width < 10 || rect.size.height < 10) continue;

            outRects[found].x = (rect.origin.x - vd.origin.x) / screenW;
            outRects[found].y = (rect.origin.y - vd.origin.y) / screenH;
            outRects[found].w = rect.size.width / screenW;
            outRects[found].h = rect.size.height / screenH;
            found++;
        }

        CFRelease(windowList);
        return found;
    }
}

// ── Screenshot capture ───────────────────────────────────────────────
// CGWindowListCreateImage was marked unavailable in macOS 15 SDK.
// We declare it ourselves — the symbol still exists in the dylib.
CG_EXTERN CGImageRef CGWindowListCreateImage(CGRect screenBounds,
    CGWindowListOption listOption, CGWindowID windowID,
    CGWindowImageOption imageOption)
    __attribute__((availability(macos,introduced=10.5)));

void* capture_screen(int targetWidth, int targetHeight,
                     int* outWidth, int* outHeight, int* outBytesPerRow) {
    CGImageRef image = CGWindowListCreateImage(
        CGRectInfinite,
        kCGWindowListOptionOnScreenOnly,
        kCGNullWindowID,
        kCGWindowImageBoundsIgnoreFraming | kCGWindowImageNominalResolution
    );
    if (!image) return NULL;

    size_t srcW = CGImageGetWidth(image);
    size_t srcH = CGImageGetHeight(image);

    int tw = targetWidth;
    int th = targetHeight;
    if (tw < 0 || th < 0) {
        // Negative means full resolution (no downscale)
        tw = (int)srcW;
        th = (int)srcH;
    } else if (tw == 0 || th == 0) {
        // Zero means quarter resolution (legacy for HEVC video)
        tw = (int)(srcW / 4);
        th = (int)(srcH / 4);
    }
    // Ensure even dimensions (required by video encoders)
    tw = (tw + 1) & ~1;
    th = (th + 1) & ~1;

    size_t bytesPerRow = tw * 4;
    void *data = malloc(bytesPerRow * th);
    if (!data) {
        CGImageRelease(image);
        return NULL;
    }

    // Use BGRA format (kCGImageAlphaPremultipliedFirst + kCGBitmapByteOrder32Little)
    // to match kCVPixelFormatType_32BGRA expected by VideoToolbox
    CGColorSpaceRef cs = CGColorSpaceCreateDeviceRGB();
    CGContextRef ctx = CGBitmapContextCreate(data, tw, th, 8, bytesPerRow,
        cs, kCGImageAlphaPremultipliedFirst | kCGBitmapByteOrder32Little);
    CGColorSpaceRelease(cs);

    if (!ctx) {
        free(data);
        CGImageRelease(image);
        return NULL;
    }

    CGContextSetInterpolationQuality(ctx, kCGInterpolationMedium);
    CGContextDrawImage(ctx, CGRectMake(0, 0, tw, th), image);
    CGContextRelease(ctx);
    CGImageRelease(image);

    *outWidth = tw;
    *outHeight = th;
    *outBytesPerRow = (int)bytesPerRow;
    return data;
}
*/
import "C"

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"wails-gui/internal/platform/nativeprompt"
)

const stateEventCoalesceInterval = 250 * time.Millisecond

// Package-level health counters for the event pipeline. Exposed via
// GetStats() for the perf snapshot. Values survive a Stop/Start cycle
// on purpose: a restart-on-recover scenario should still show history.
var (
	statsEventsDrained atomic.Uint64 // total events delivered to a callback
	statsWakeups       atomic.Uint64 // pipe reads that woke the drain goroutine
)

// darwinMonitor implements Monitor using macOS CGEventTap, NSWorkspace, and AXObserver.
type darwinMonitor struct {
	allowed  func() bool
	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	callback func(ActivityEvent)
	// Self-pipe used to wake the drain goroutine when the C event queue
	// transitions from empty to non-empty. Go owns both fds.
	notifyR *os.File
	notifyW *os.File
}

// FrontmostAppBundleID returns the bundle ID of the current frontmost application.
func FrontmostAppBundleID() string {
	var bid [256]C.char
	var name [256]C.char
	C.get_frontmost_app(&bid[0], 256, &name[0], 256)
	return C.GoString(&bid[0])
}

// FrontmostAppPID returns the process id of the current frontmost
// application, or 0 when there is none.
func FrontmostAppPID() int {
	return int(C.frontmost_pid())
}

// FrontmostAppName returns the localized name of the current frontmost
// application. It's a fallback identity for apps whose bundleIdentifier is nil
// (e.g. Clash Verge, command-line tools), so they don't collapse to "?".
func FrontmostAppName() string {
	var bid [256]C.char
	var name [256]C.char
	C.get_frontmost_app(&bid[0], 256, &name[0], 256)
	return C.GoString(&name[0])
}

// FrontmostAppInfo returns the bundle ID, app name, and window title of the
// current frontmost application. Thread-safe — can be called from any goroutine.
func FrontmostAppInfo() AppInfo {
	var bid [256]C.char
	var name [256]C.char
	pid := C.get_frontmost_app(&bid[0], 256, &name[0], 256)
	var title [512]C.char
	if pid > 0 {
		C.get_window_title(pid, &title[0], 512)
	}
	front := AppInfo{
		BundleID: C.GoString(&bid[0]), AppName: C.GoString(&name[0]),
		WindowTitle: C.GoString(&title[0]), FocusUnavailable: pid <= 0,
	}
	// A browser whose title could not be read cannot be cleared as public.
	front.FocusUnavailable = front.FocusUnavailable || (IsBrowser(front.BundleID) && front.WindowTitle == "")
	return front
}

// FrontmostWindowBounds returns the screen-normalized bounds of the frontmost
// application's main window. Returns zero bounds if unavailable.
func FrontmostWindowBounds() WindowBounds {
	var x, y, w, h, sw, sh C.double
	C.get_frontmost_window_bounds(&x, &y, &w, &h, &sw, &sh)
	if sw == 0 || sh == 0 || w == 0 || h == 0 {
		return WindowBounds{}
	}
	return WindowBounds{
		X: float64(x) / float64(sw),
		Y: float64(y) / float64(sh),
		W: float64(w) / float64(sw),
		H: float64(h) / float64(sh),
	}
}

// FindVisibleSensitiveWindows returns the normalized bounds of all on-screen
// windows that belong to any app whose bundle ID matches one of the given
// prefixes. This catches sensitive app windows even when they are not frontmost.
func FindVisibleSensitiveWindows(bundlePrefixes []string) []WindowBounds {
	if len(bundlePrefixes) == 0 {
		return nil
	}

	cPrefixes := make([]*C.char, len(bundlePrefixes))
	for i, p := range bundlePrefixes {
		cPrefixes[i] = C.CString(p)
	}
	defer func() {
		for _, cp := range cPrefixes {
			C.free(unsafe.Pointer(cp))
		}
	}()

	const maxResults = 16
	var rects [maxResults]C.normalized_rect
	n := C.find_sensitive_windows(&cPrefixes[0], C.int(len(cPrefixes)),
		&rects[0], C.int(maxResults))

	if n == 0 {
		return nil
	}

	result := make([]WindowBounds, int(n))
	for i := 0; i < int(n); i++ {
		result[i] = WindowBounds{
			X: float64(rects[i].x),
			Y: float64(rects[i].y),
			W: float64(rects[i].w),
			H: float64(rects[i].h),
		}
	}
	return result
}

// New creates a new platform-specific Monitor.
func New(allowed func() bool) Monitor {
	return &darwinMonitor{allowed: allowed}
}

// GetStats returns a point-in-time snapshot of the event pipeline's
// internal health counters. Safe to call concurrently; reads are
// essentially free (two atomic loads plus one brief mutex pair on the
// C side).
func GetStats() Stats {
	var cpeak C.int
	var cdrops C.ulonglong
	C.get_ring_stats(&cpeak, &cdrops)
	return Stats{
		EventsDrained: statsEventsDrained.Load(),
		Wakeups:       statsWakeups.Load(),
		RingPeakDepth: int(cpeak),
		RingDrops:     uint64(cdrops),
	}
}

func (m *darwinMonitor) Start(ctx context.Context, callback func(ActivityEvent)) error {
	if !nativeprompt.Allowed() {
		return &monitorError{"activity monitor is unavailable under go test"}
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.callback = callback
	m.running = true
	m.mu.Unlock()

	nativeCaptureGate.Store(&m.allowed)
	if !bool(C.start_activity_monitor()) {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		return &monitorError{"failed to start activity monitor (check Accessibility permission)"}
	}

	// Self-pipe: C writes 1 byte whenever push_event brings the queue from
	// empty to non-empty; the drain goroutine blocks on r.Read so it
	// consumes zero CPU while idle and wakes within a syscall of an event
	// arriving. Replaces the previous 10ms polling ticker.
	r, w, err := os.Pipe()
	if err != nil {
		C.stop_activity_monitor()
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		return err
	}

	// Hand the writer fd to C. Go retains ownership; C will never close it.
	C.set_notify_fd(C.int(w.Fd()))

	ctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	m.notifyR = r
	m.notifyW = w
	m.mu.Unlock()

	// Drain goroutine: blocked in netpoller while idle.
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := r.Read(buf); err != nil {
				return
			}
			statsWakeups.Add(1)
			m.drainEvents()
		}
	}()

	// Honor external ctx cancellation by routing through Stop so fd
	// teardown ordering matches the manual-Stop path.
	go func() {
		<-ctx.Done()
		m.Stop()
	}()

	return nil
}

// drainEvents pops all pending events from the C queue and dispatches them.
func (m *darwinMonitor) drainEvents() {
	cb := m.getCallback()
	if cb == nil {
		return
	}

	events := make([]ActivityEvent, 0, 32)
	var cevt C.CActivityEvent
	for C.pop_event(&cevt) {
		statsEventsDrained.Add(1)
		events = append(events, activityEventFromC(cevt))
	}

	for _, evt := range coalesceStateEvents(events, stateEventCoalesceInterval) {
		cb(evt)
	}
}

func activityEventFromC(cevt C.CActivityEvent) ActivityEvent {
	timestamp := int64(cevt.timestampMs)
	if timestamp == 0 {
		timestamp = time.Now().UnixMilli()
	}

	keyChar := C.GoString(&cevt.keyChar[0])
	var keyChars []string
	if keyChar != "" {
		keyChars = []string{keyChar}
	}
	return ActivityEvent{
		Timestamp:      timestamp,
		Kind:           EventKind(C.GoString(&cevt.kind[0])),
		AppBundleID:    C.GoString(&cevt.appBundleID[0]),
		AppName:        C.GoString(&cevt.appName[0]),
		WindowTitle:    C.GoString(&cevt.windowTitle[0]),
		ClickX:         float64(cevt.clickX),
		ClickY:         float64(cevt.clickY),
		ClickRole:      C.GoString(&cevt.clickRole[0]),
		ClickLabel:     C.GoString(&cevt.clickLabel[0]),
		KeyCode:        uint16(cevt.keyCode),
		Modifiers:      uint64(cevt.modifiers),
		KeyChars:       keyChars,
		FocusRole:      C.GoString(&cevt.focusRole[0]),
		FocusLabel:     C.GoString(&cevt.focusLabel[0]),
		FieldText:      C.GoString(&cevt.fieldText[0]),
		FieldTextValid: bool(cevt.fieldTextValid),
		IMEComposing:   bool(cevt.imeComposing),
	}
}

func (m *darwinMonitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	cancel := m.cancel
	m.cancel = nil
	r := m.notifyR
	w := m.notifyW
	m.notifyR = nil
	m.notifyW = nil
	m.mu.Unlock()

	// Teardown ordering is load-bearing:
	//   1. set_notify_fd(-1) makes C forget the fd and, via its mutex,
	//      waits for any in-flight push_event write to drain. After this
	//      call returns, no C thread holds a reference to our pipe fd.
	//   2. stop_activity_monitor() tears down the OS event sources so no
	//      new push_event calls will happen.
	//   3. Close the pipe. w.Close() also unblocks the drain goroutine
	//      with EOF. Only now is it safe for the OS to recycle the fd
	//      number.
	C.set_notify_fd(-1)
	C.stop_activity_monitor()
	if w != nil {
		w.Close()
	}
	if r != nil {
		r.Close()
	}
	if cancel != nil {
		cancel()
	}
}

func (m *darwinMonitor) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *darwinMonitor) getCallback() func(ActivityEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callback
}

type monitorError struct {
	msg string
}

func (e *monitorError) Error() string {
	return e.msg
}

// CaptureScreen captures the current screen content at reduced resolution.
// Returns raw RGBA pixel data and dimensions.
func CaptureScreen(targetWidth, targetHeight int) (pixels []byte, width, height int, err error) {
	var outW, outH, outBPR C.int
	data := C.capture_screen(C.int(targetWidth), C.int(targetHeight),
		&outW, &outH, &outBPR)
	if data == nil {
		return nil, 0, 0, &monitorError{"screen capture failed"}
	}
	defer C.free(data)

	w := int(outW)
	h := int(outH)
	bpr := int(outBPR)
	totalBytes := bpr * h

	pixels = AcquireCaptureBuffer(totalBytes)
	copy(pixels, unsafe.Slice((*byte)(data), totalBytes))

	return pixels, w, h, nil
}
