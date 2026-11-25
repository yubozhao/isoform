//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreGraphics -framework ApplicationServices -framework AppKit -framework AVFoundation -framework UserNotifications
#include <CoreGraphics/CoreGraphics.h>
#include <ApplicationServices/ApplicationServices.h>
#include <stdbool.h>
#include <stdlib.h>
#import <AVFoundation/AVFoundation.h>

// Forward declarations for AppKit/NSWorkspace usage via objc runtime.
#include <objc/runtime.h>
#include <objc/message.h>
#import <UserNotifications/UserNotifications.h>

bool preflight_screen_capture_access(void) {
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 101500
    if (@available(macOS 10.15, *)) {
        return CGPreflightScreenCaptureAccess();
    }
#endif
    return false;
}

bool request_screen_capture_access(void) {
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 101500
    if (@available(macOS 10.15, *)) {
        return CGRequestScreenCaptureAccess();
    }
#endif
    return false;
}

bool check_accessibility_access(void) {
    CFStringRef keys[1] = {kAXTrustedCheckOptionPrompt};
    CFTypeRef values[1] = {kCFBooleanFalse};

    CFDictionaryRef options = CFDictionaryCreate(
        NULL,
        (const void **)keys,
        (const void **)values,
        1,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );

    bool trusted = AXIsProcessTrustedWithOptions(options) != 0;
    if (options != NULL) {
        CFRelease(options);
    }

    return trusted;
}

bool request_accessibility_access(void) {
    CFStringRef keys[1] = {kAXTrustedCheckOptionPrompt};
    CFTypeRef values[1] = {kCFBooleanTrue};

    CFDictionaryRef options = CFDictionaryCreate(
        NULL,
        (const void **)keys,
        (const void **)values,
        1,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );

    bool trusted = AXIsProcessTrustedWithOptions(options) != 0;
    if (options != NULL) {
        CFRelease(options);
    }

    return trusted;
}

bool check_microphone_access(void) {
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 101400
    if (@available(macOS 10.14, *)) {
        AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
        switch (status) {
        case AVAuthorizationStatusAuthorized:
            return true;
        case AVAuthorizationStatusDenied:
        case AVAuthorizationStatusRestricted:
            return false;
        case AVAuthorizationStatusNotDetermined:
            // Status reads never create an input node or trigger a prompt.
            return false;
        }
    }
#endif

    return true;
}

bool request_microphone_access(void) {
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 101400
    if (@available(macOS 10.14, *)) {
        AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
        switch (status) {
        case AVAuthorizationStatusAuthorized:
            return true;
        case AVAuthorizationStatusDenied:
        case AVAuthorizationStatusRestricted:
            return false;
        case AVAuthorizationStatusNotDetermined: {
            __block bool granted = false;
            dispatch_semaphore_t sema = dispatch_semaphore_create(0);
            [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio
                                     completionHandler:^(BOOL allowed) {
                granted = allowed;
                dispatch_semaphore_signal(sema);
            }];
            dispatch_semaphore_wait(sema, DISPATCH_TIME_FOREVER);
            return granted;
        }
        }
    }
#endif

    return true;
}

// Returns true if the app has a valid bundle identifier (production .app build).
bool has_bundle_id(void) {
    NSString *bundleID = [[NSBundle mainBundle] bundleIdentifier];
    return bundleID != nil && [bundleID length] > 0;
}

// Check current notification authorization status.
// Returns 0 = notDetermined, 1 = denied, 2 = authorized, 3 = provisional.
//
// UNUserNotificationCenter dispatches its completion handler on an internal
// queue that can back up or never fire when the main run loop is wedged
// (common during onboarding when a system consent dialog is animating).
// We wait 500ms max; on timeout we return -1 (unknown) so the
// permissions UI renders a "Grant" action instead of blocking the whole
// GetPlatformPermissions RPC — the frontend can still complete rendering.
int check_notification_status(void) {
    if (!has_bundle_id()) {
        // No bundle -> UNUserNotificationCenter won't work; report as authorized
        // so the permission row doesn't appear during wails dev.
        return 2;
    }
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 101400
    if (@available(macOS 10.14, *)) {
        __block int status = 0;
        dispatch_semaphore_t sema = dispatch_semaphore_create(0);
        UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
        [center getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *settings) {
            status = (int)settings.authorizationStatus;
            dispatch_semaphore_signal(sema);
        }];
        dispatch_time_t deadline = dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.5 * NSEC_PER_SEC));
        if (dispatch_semaphore_wait(sema, deadline) != 0) {
            dispatch_release(sema);
            return -1; // timed out — the authorization state is unknown
        }
        dispatch_release(sema);
        return status;
    }
#endif
    return 2; // pre-10.14: report as authorized (notifications unsupported)
}

// request_notification_access returns true when the user granted the
// authorization, false otherwise. This runs off the UI thread and waits for
// the actual answer so the sequencer never opens the next prompt early.
bool request_notification_access(void) {
    if (!has_bundle_id()) {
        return true;
    }
#if __MAC_OS_X_VERSION_MAX_ALLOWED >= 101400
    if (@available(macOS 10.14, *)) {
        __block bool granted = false;
        dispatch_semaphore_t sema = dispatch_semaphore_create(0);
        UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
        [center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionBadge | UNAuthorizationOptionSound)
            completionHandler:^(BOOL allowed, NSError *error) {
                granted = allowed;
                dispatch_semaphore_signal(sema);
            }];
        if (dispatch_semaphore_wait(sema, DISPATCH_TIME_FOREVER) != 0) {
            return false; // user didn't respond in time / dialog didn't appear
        }
        return granted;
    }
#endif
    return true; // pre-10.14: notifications unsupported, report success
}

// open_url_via_nsworkspace opens a URL string using NSWorkspace (in-process, no subprocess).
bool open_url_via_nsworkspace(const char *urlStr) {
    id pool = ((id (*)(id, SEL))objc_msgSend)((id)objc_getClass("NSAutoreleasePool"), sel_registerName("new"));

    id nsString = ((id (*)(id, SEL, const char*))objc_msgSend)(
        (id)objc_getClass("NSString"),
        sel_registerName("stringWithUTF8String:"),
        urlStr
    );

    id nsURL = ((id (*)(id, SEL, id))objc_msgSend)(
        (id)objc_getClass("NSURL"),
        sel_registerName("URLWithString:"),
        nsString
    );

    if (nsURL == nil) {
        ((void (*)(id, SEL))objc_msgSend)(pool, sel_registerName("drain"));
        return false;
    }

    id workspace = ((id (*)(id, SEL))objc_msgSend)(
        (id)objc_getClass("NSWorkspace"),
        sel_registerName("sharedWorkspace")
    );

    bool ok = ((bool (*)(id, SEL, id))objc_msgSend)(
        workspace,
        sel_registerName("openURL:"),
        nsURL
    );

    ((void (*)(id, SEL))objc_msgSend)(pool, sel_registerName("drain"));
    return ok;
}
*/
import "C"

import (
	"fmt"
	"log"
	"sync"
	"time"
	"unsafe"
)

// openURL opens a URL string using NSWorkspace (in-process, no subprocess).
// File paths (starting with /) are converted to file:// URLs automatically.
func openURL(urlStr string) bool {
	if len(urlStr) > 0 && urlStr[0] == '/' {
		urlStr = "file://" + urlStr
	}
	cStr := C.CString(urlStr)
	defer C.free(unsafe.Pointer(cStr))
	return bool(C.open_url_via_nsworkspace(cStr))
}

var screenRecordingSettingsURLs = []string{
	// Tested on macOS 26 (Tahoe) — navigates directly to Screen Recording sub-page.
	"x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_ScreenCapture",
	// Older formats — may only reach Privacy & Security top-level on macOS 26+.
	"x-apple.systempreferences:root=Security&path=Privacy&pane=ScreenCapture",
	"x-apple.systempreferences:com.apple.security?Privacy_ScreenCapture",
	"x-apple.systempreferences:com.apple.Settings.PrivacySecurityExtension?ScreenCapture",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurityExtension?ScreenCapture",
	// Generic fallbacks (open Privacy & Security top-level).
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Security.prefPane",
}

var accessibilitySettingsURLs = []string{
	// Tested on macOS 26 (Tahoe) — navigates directly to Accessibility sub-page.
	"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_Accessibility",
	// Older formats — may only reach Privacy & Security top-level on macOS 26+.
	"x-apple.systempreferences:root=Security&path=Privacy&pane=Accessibility",
	"x-apple.systempreferences:com.apple.security?Privacy_Accessibility",
	"x-apple.systempreferences:com.apple.Settings.PrivacySecurityExtension?Accessibility",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurityExtension?Accessibility",
	// Generic fallbacks (open Privacy & Security top-level).
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Security.prefPane",
}

var notificationSettingsURLs = []string{
	// Tested on macOS 26 (Tahoe) — navigates directly to Notifications sub-page.
	"x-apple.systempreferences:com.apple.preference.notifications",
	"x-apple.systempreferences:com.apple.Notifications-Settings.extension",
	// Generic fallbacks.
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Notifications.prefPane",
}

var automationSettingsURLs = []string{
	// Privacy & Security > Automation. The sub-page link is used where macOS
	// honours it; the generic links fall back to Privacy & Security.
	"x-apple.systempreferences:com.apple.preference.security?Privacy_Automation",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_Automation",
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Security.prefPane",
}

var contactsSettingsURLs = []string{
	// Privacy & Security > Contacts. The sub-page link is used where macOS
	// honours it; the generic links fall back to Privacy & Security.
	"x-apple.systempreferences:com.apple.preference.security?Privacy_Contacts",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_Contacts",
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Security.prefPane",
}

var calendarSettingsURLs = []string{
	// Privacy & Security > Calendars. The sub-page link is used where macOS
	// honours it; the generic links fall back to Privacy & Security.
	"x-apple.systempreferences:com.apple.preference.security?Privacy_Calendars",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_Calendars",
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Security.prefPane",
}

var fullDiskAccessSettingsURLs = []string{
	// Privacy & Security > Full Disk Access, with the same fallbacks.
	"x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_AllFiles",
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Security.prefPane",
}

var remindersSettingsURLs = []string{
	// Privacy & Security > Reminders. The sub-page link is used where macOS
	// honours it; the generic links fall back to Privacy & Security.
	"x-apple.systempreferences:com.apple.preference.security?Privacy_Reminders",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_Reminders",
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Security.prefPane",
}

var microphoneSettingsURLs = []string{
	// Tested on macOS 26 (Tahoe) — navigates directly to Microphone sub-page.
	"x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_Microphone",
	// Older formats — may only reach Privacy & Security top-level on macOS 26+.
	"x-apple.systempreferences:root=Security&path=Privacy&pane=Microphone",
	"x-apple.systempreferences:com.apple.security?Privacy_Microphone",
	"x-apple.systempreferences:com.apple.Settings.PrivacySecurityExtension?Microphone",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurityExtension?Microphone",
	// Generic fallbacks (open Privacy & Security top-level).
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Security.prefPane",
}

var systemAudioSettingsURLs = []string{
	// On current macOS releases system-audio capture lives under the same
	// Privacy & Security area as Screen & System Audio Recording.
	"x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_ScreenCapture",
	"x-apple.systempreferences:root=Security&path=Privacy&pane=ScreenCapture",
	"x-apple.systempreferences:com.apple.security?Privacy_ScreenCapture",
	"x-apple.systempreferences:com.apple.Settings.PrivacySecurityExtension?ScreenCapture",
	"x-apple.systempreferences:com.apple.settings.PrivacySecurityExtension?ScreenCapture",
	"x-apple.systempreferences:com.apple.preference.security",
	"/System/Library/PreferencePanes/Security.prefPane",
}

func getPlatformPermissions() PlatformPermissions {
	// Independent status probes may wait on separate native services. Join
	// them once, rather than making page latency the sum of those waits.
	granted := readPermissionGrants(map[string]func() bool{
		"screen_recording":    func() bool { return preflightScreenCaptureAccess() },
		"accessibility":       func() bool { return accessibilityAccess() },
		"microphone":          func() bool { return microphoneAccess() },
		"notifications":       func() bool { return notificationAccess() },
		"automation_messages": func() bool { return sharedTextChannel().automationAllowed(false) },
		"contacts":            func() bool { return contactsAccessGranted() },
		"calendars":           func() bool { return calendarAccessGranted() },
		"reminders":           func() bool { return remindersAccessGranted() },
		"mail":                func() bool { return mailAutomationGranted() },
		"messages_history":    func() bool { return messagesHistoryAccess() },
	})
	items := []PermissionItem{
		{
			Key:         "screen_recording",
			Label:       "Screen Recording",
			Description: "Required to capture your screen during agent runs.",
			Granted:     granted["screen_recording"],
			Required:    true,
			Actionable:  true,
			ActionLabel: "Grant",
			Toggleable:  true,
			Enabled:     true, // default; overridden by stored state in GetPlatformPermissions
		},
		{
			Key:         "accessibility",
			Label:       "Accessibility",
			Description: "Required for keyboard and mouse automation.",
			Granted:     granted["accessibility"],
			Required:    true,
			Actionable:  true,
			ActionLabel: "Grant",
			Toggleable:  true,
			Enabled:     true, // default; overridden by stored state in GetPlatformPermissions
		},
		{
			// Toggleable: the switch pauses the microphone source of
			// background listening (system audio keeps going). The switch is
			// applied in GetPlatformPermissions from the config.
			Key:         "microphone",
			Label:       "Microphone",
			Description: "Optional for hearing your voice: voice typing in the composer and background listening.",
			Granted:     granted["microphone"],
			Required:    false,
			Actionable:  true,
			ActionLabel: "Grant",
			Toggleable:  true,
			Enabled:     true,
		},
	}

	if darwinSystemAudioCaptureSupported() {
		items = append(items, PermissionItem{
			Key:         "system_audio",
			Label:       "System Audio Recording",
			Description: "Optional for capturing your computer's output audio during background listening.",
			Granted:     systemAudioAccess(),
			Required:    false,
			Actionable:  true,
			ActionLabel: "Grant",
		})
	}

	items = append(items, PermissionItem{
		Key:         "notifications",
		Label:       "Notifications",
		Description: "Optional for desktop alerts when background tasks complete.",
		Granted:     granted["notifications"],
		Required:    false,
		Actionable:  true,
		ActionLabel: "Enable",
	})

	items = append(items, PermissionItem{
		Key:         "automation_messages",
		Label:       "Messages",
		Description: "Optional for sending you a text through Messages when Isoform cannot reach your phone.",
		Granted:     granted["automation_messages"],
		Required:    false,
		Actionable:  true,
		ActionLabel: "Grant",
	})

	items = append(items, PermissionItem{
		Key:         "contacts",
		Label:       "Contacts",
		Description: "Choose contacts to import from this computer on the Who you know page. Isoform reads names, phone numbers and email addresses.",
		Granted:     granted["contacts"],
		Required:    false,
		Actionable:  true,
		ActionLabel: "Grant",
	})

	// Toggleable so the user can pause reading without revoking access. The
	// switch is applied in GetPlatformPermissions from ui_state.
	items = append(items, PermissionItem{
		Key:         calendarPermissionKey,
		Label:       "Calendars",
		Description: "Optional for preparing you for meetings and planning your day. Events are kept on this computer; what a task needs is sent to the AI you chose and to your paired phone.",
		Granted:     granted["calendars"],
		Required:    false,
		Actionable:  true,
		ActionLabel: "Grant",
		Toggleable:  true,
		Enabled:     true,
	})

	// Toggleable like the calendars: the switch pauses reading without
	// revoking access.
	items = append(items, PermissionItem{
		Key:         remindersPermissionKey,
		Label:       "Reminders",
		Description: "Optional for keeping track of what you have to do. Open reminders count as your commitments, so Isoform can tell you when one is slipping. Reminders are kept on this computer; open ones can appear in your brief on your paired phone, and what a task needs is sent to the AI you chose.",
		Granted:     granted["reminders"],
		Required:    false,
		Actionable:  true,
		ActionLabel: "Grant",
		Toggleable:  true,
		Enabled:     true,
	})

	// Toggleable like the reminders: the switch pauses reading without
	// revoking access. macOS answers only while Mail runs, so Granted keeps
	// the last answer when Mail is closed.
	items = append(items, PermissionItem{
		Key:         mailPermissionKey,
		Label:       "Mail",
		Description: "Optional for knowing who is waiting on you. While Mail is open, Isoform reads the senders, recipients, subjects and dates of the last week's mail, never the messages themselves, and tells you when someone you know has been waiting on a reply for more than a day. Mail headers stay on this computer; who is waiting and the subject can appear on your paired phone and in a notification on this computer.",
		Granted:     granted["mail"],
		Required:    false,
		Actionable:  true,
		ActionLabel: "Grant",
		Toggleable:  true,
		Enabled:     true,
	})

	// Full Disk Access lets Isoform read the Messages database. macOS has no
	// prompt for it: Grant opens System Settings, and only when tapped.
	// Toggleable like Mail: the switch pauses reading and keeps what was kept.
	items = append(items, PermissionItem{
		Key:         messagesPermissionKey,
		Label:       "Messages history",
		Description: "Optional for knowing who asked you for something and what you promised. Isoform reads your recent Messages conversations with people you know and keeps only a sentence in its own words about each ask or promise, never the messages themselves. Needs Full Disk Access.",
		Granted:     granted["messages_history"],
		Required:    false,
		Actionable:  true,
		ActionLabel: "Grant",
		Toggleable:  true,
		Enabled:     true,
	})

	// No macOS permission sits behind recent files either: Spotlight's
	// metadata needs none, and a folder macOS has not opened to Isoform is
	// skipped rather than asked for. The switch starts off
	// (permissionsOffByDefault).
	items = append(items, PermissionItem{
		Key:         filesPermissionKey,
		Label:       "Recent files",
		Description: "Notes the names of the documents you open and edit in Documents, Desktop, Downloads and iCloud Drive, from Spotlight. Never what is in them. Hidden files, app data, files that hold passwords or keys, the folders you keep private and anything used with the apps you protect are left out. Off until you turn it on.",
		Granted:     true,
		Required:    false,
		Actionable:  false,
		Toggleable:  true,
		Enabled:     false,
	})

	return PlatformPermissions{
		Platform:  "darwin",
		Supported: true,
		Items:     items,
	}
}

func requestPermission(key string) bool {
	switch key {
	case "screen_recording":
		return requestScreenCapturePermission()
	case "accessibility":
		return requestAccessibilityPermission()
	case "microphone":
		return requestMicrophonePermission()
	case "system_audio":
		return requestSystemAudioPermission()
	case "notifications":
		return requestNotificationPermission()
	case "automation_messages":
		return sharedTextChannel().RequestMessagesAutomation()
	case "contacts":
		// Contacts access requires the People consent sheet. Generic Settings,
		// onboarding and chat permission actions must never show this prompt.
		return contactsAccessGranted()
	case calendarPermissionKey:
		granted, err := requestCalendarAuthorization()
		if err != nil {
			log.Printf("[permissions] calendar access request failed: %v", err)
		}
		return granted
	case remindersPermissionKey:
		granted, err := requestRemindersAuthorization()
		if err != nil {
			log.Printf("[permissions] reminders access request failed: %v", err)
		}
		return granted
	case messagesPermissionKey:
		// macOS has no prompt for Full Disk Access; report what it allows.
		return messagesHistoryAccess()
	case mailPermissionKey:
		// macOS asks only about a running Mail, and Isoform never opens it.
		if !mailRunning() {
			log.Printf("[permissions] Mail is not open; open Mail to allow reading it")
			return false
		}
		return requestMailAutomation()
	default:
		return false
	}
}

func openPermissionSettings(key string) error {
	return openPermissionSettingsFromRect(key, 0, 0, 0, 0)
}

func openPermissionSettingsFromRect(key string, srcX, srcY, srcW, srcH float64) error {
	// The drag overlay belongs to the Screen Recording and Accessibility
	// lists only; any other pane opened from Isoform closes it.
	if !hasPermissionOverlay(key) {
		dismissPermissionOverlay()
	}
	switch key {
	case "screen_recording":
		presentPermissionOverlay("screen_recording", srcX, srcY, srcW, srcH)
		return openSystemPreferences(screenRecordingSettingsURLs)
	case "accessibility":
		presentPermissionOverlay("accessibility", srcX, srcY, srcW, srcH)
		return openSystemPreferences(accessibilitySettingsURLs)
	case "microphone":
		return openMicrophoneSettings()
	case "system_audio":
		return openSystemAudioSettings()
	case "notifications":
		return openNotificationSettings()
	case "automation_messages":
		return openAutomationSettings()
	case "contacts":
		return openContactsSettings()
	case calendarPermissionKey:
		return openCalendarSettings()
	case remindersPermissionKey:
		return openSystemPreferences(remindersSettingsURLs)
	case mailPermissionKey:
		return openAutomationSettings()
	case messagesPermissionKey:
		return openSystemPreferences(fullDiskAccessSettingsURLs)
	default:
		return fmt.Errorf("unknown permission key: %s", key)
	}
}

func preflightScreenCaptureAccess() bool {
	return bool(C.preflight_screen_capture_access())
}

func requestScreenCapturePermission() bool {
	if !permissionPromptsAllowed() {
		return false
	}
	return bool(C.request_screen_capture_access())
}

func accessibilityAccess() bool {
	return bool(C.check_accessibility_access())
}

func requestAccessibilityPermission() bool {
	if !permissionPromptsAllowed() {
		return false
	}
	return bool(C.request_accessibility_access())
}

func microphoneAccess() bool {
	return bool(C.check_microphone_access())
}

func requestMicrophonePermission() bool {
	if !permissionPromptsAllowed() {
		return false
	}
	return bool(C.request_microphone_access())
}

func systemAudioAccess() bool {
	if !darwinSystemAudioCaptureSupported() {
		return false
	}
	granted, _ := readAudioConfig().systemPermission()
	return granted
}

func setSystemAudioPermissionGranted(granted bool) {
	if err := updateConfig(func(cfg *yansuConfig) {
		cfg.SystemAudioPermissionGranted = granted
	}); err != nil {
		log.Printf("[permissions] failed to persist system audio permission state: %v", err)
	}
}

func requestSystemAudioPermission() bool {
	if !permissionPromptsAllowed() || !darwinSystemAudioCaptureSupported() {
		return false
	}

	recorder := newDarwinSystemAudioRecorder()
	if recorder == nil {
		setSystemAudioPermissionGranted(false)
		return false
	}
	if err := recorder.Start(); err != nil {
		log.Printf("[permissions] system audio permission request failed: %v", err)
		setSystemAudioPermissionGranted(false)
		return false
	}
	recorder.Stop()
	setSystemAudioPermissionGranted(true)
	return true
}

func openScreenRecordingSettings() error {
	return openSystemPreferences(screenRecordingSettingsURLs)
}

func openAccessibilitySettings() error {
	return openSystemPreferences(accessibilitySettingsURLs)
}

func openMicrophoneSettings() error {
	return openSystemPreferences(microphoneSettingsURLs)
}

func openSystemAudioSettings() error {
	return openSystemPreferences(systemAudioSettingsURLs)
}

func setupNotificationPermission() *bool {
	return setupNotificationStatus(int(C.check_notification_status()))
}

func notificationAccess() bool {
	// authorizationStatus: 0=notDetermined, 1=denied, 2=authorized, 3=provisional
	return int(C.check_notification_status()) == 2
}

// notifDeniedTTL bounds how often we re-query the OS notification setting.
// The status only changes when the user leaves the app for System Settings,
// so a short cache keeps repeated toasts off the (semaphore-bounded) cgo
// call while staying fresh enough to react when they flip the toggle back.
const notifDeniedTTL = 5 * time.Second

var (
	notifDeniedMu     sync.Mutex
	notifDeniedCached bool
	notifDeniedAt     time.Time
)

// notificationExplicitlyDenied reports whether the user has explicitly turned
// Yansu's notifications OFF in System Settings (authorizationStatus == denied).
// notDetermined / authorized / provisional all return false so we only stay
// silent for users who actively said "no" — users who were never prompted
// (the common case, since the in-house toast bypasses the OS prompt) keep
// receiving toasts as before.
func notificationExplicitlyDenied() bool {
	notifDeniedMu.Lock()
	if !notifDeniedAt.IsZero() && time.Since(notifDeniedAt) < notifDeniedTTL {
		cached := notifDeniedCached
		notifDeniedMu.Unlock()
		return cached
	}
	notifDeniedMu.Unlock()

	// Query OUTSIDE the lock: check_notification_status() can block up to 2s
	// (its semaphore deadline) when the main runloop is wedged, and holding the
	// mutex across it would stall every concurrent caller — even ones that would
	// have hit the cache. The trade-off is no single-flight dedup (simultaneous
	// stale-cache callers each run the query), which is harmless here: it's an
	// idempotent status read, UNUserNotificationCenter tolerates concurrent
	// getNotificationSettings, and the 5s TTL keeps the query rare.
	// authorizationStatus: 0=notDetermined, 1=denied, 2=authorized, 3=provisional
	denied := int(C.check_notification_status()) == 1

	notifDeniedMu.Lock()
	notifDeniedCached = denied
	notifDeniedAt = time.Now()
	notifDeniedMu.Unlock()

	return denied
}

func requestNotificationPermission() bool {
	if !permissionPromptsAllowed() {
		return false
	}
	return bool(C.request_notification_access())
}

func openNotificationSettings() error {
	return openSystemPreferences(notificationSettingsURLs)
}

func openAutomationSettings() error {
	return openSystemPreferences(automationSettingsURLs)
}

func openContactsSettings() error {
	return openSystemPreferences(contactsSettingsURLs)
}

func openCalendarSettings() error {
	return openSystemPreferences(calendarSettingsURLs)
}

func openSystemPreferences(urls []string) error {
	for _, url := range urls {
		log.Printf("[permissions] trying to open: %s", url)
		if openURL(url) {
			return nil
		}
	}
	return fmt.Errorf("failed to open system preferences with any of the %d candidate URLs", len(urls))
}
