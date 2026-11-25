package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"time"
)

type PlatformPermissions struct {
	Platform  string           `json:"platform"`
	Items     []PermissionItem `json:"items"`
	Supported bool             `json:"supported"`
}

type PermissionItem struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Granted     bool   `json:"granted"`
	Required    bool   `json:"required"`
	Actionable  bool   `json:"actionable"`
	ActionLabel string `json:"actionLabel"`
	Toggleable  bool   `json:"toggleable"`
	Enabled     bool   `json:"enabled"`
	// EnabledUnknown is set when the switch's saved state couldn't be read.
	// Enabled is then meaningless: the row says it couldn't read the switch,
	// never on or off.
	EnabledUnknown bool `json:"enabledUnknown,omitempty"`
}

// permissionUIStateKey returns the ui_state key for a toggleable permission.
func permissionUIStateKey(permKey string) string {
	return "permission_enabled:" + permKey
}

// permissionsOffByDefault are the switches that stay off until the user
// turns them on. Recent files is one until a real Mac confirms that
// Spotlight never shows Isoform a folder macOS has not opened to it.
var permissionsOffByDefault = map[string]bool{
	filesPermissionKey:    true,
	messagesPermissionKey: true,
}

// isPermissionEnabled reads the stored toggle state for a permission.
// Returns the switch's default (on, except permissionsOffByDefault) if no
// stored value exists.
func (a *App) isPermissionEnabled(key string) bool {
	return a.isPermissionEnabledContext(context.Background(), key)
}

func (a *App) isPermissionEnabledContext(ctx context.Context, key string) bool {
	on, err := a.permissionSwitchRead(ctx, key)
	if err != nil {
		if key == messagesPermissionKey {
			if err != errSettingsDefaultsUnavailable {
				log.Printf("[permissions] read Messages switch: %v", err)
			}
			return false
		}
		return !permissionsOffByDefault[key]
	}
	return on
}

// permissionSwitchRead is the switch's saved state, or an error when it
// can't be read. Settings shows the error (a row never shows a switch it
// couldn't read as on); a switch never saved reads as its default.
func (a *App) permissionSwitchRead(ctx context.Context, key string) (bool, error) {
	if key == messagesPermissionKey {
		return a.messagesSwitchRead(ctx)
	}
	if a.store == nil {
		return false, errors.New("the store is not ready")
	}
	stateKey := permissionUIStateKey(key)
	if key == calendarPeekEnabledKey {
		// SetCalendarPeekEnabled stores this switch under its own key.
		stateKey = calendarPeekEnabledKey
	}
	val, err := a.store.GetUIState(ctx, stateKey)
	if err != nil {
		return false, err
	}
	if val == "" {
		return !permissionsOffByDefault[key], nil
	}
	return val != "false", nil
}

// A source that does bookkeeping on a known off must skip that work on a
// read failure. Other gates only need the enabled result.
type captureSwitch func() (on, known bool)

func (read captureSwitch) on() bool {
	on, known := read()
	return on && known
}

// captureAllowed records a single outage for the whole source, including
// sources that require two switches. A known off also ends an outage.
func (a *App) captureAllowed(ctx context.Context, source string, keys ...string) bool {
	on, _ := a.captureState(ctx, source, keys...)
	return on
}

func (a *App) captureState(ctx context.Context, source string, keys ...string) (bool, bool) {
	enabled := true
	for _, key := range keys {
		on, err := a.permissionSwitchRead(ctx, key)
		if err != nil {
			if _, reported := a.captureSwitchFailures.LoadOrStore(source, true); !reported {
				log.Printf("[capture] %s skipped: switch state unknown", source)
			}
			return false, false
		}
		enabled = enabled && on
	}
	a.captureSwitchFailures.Delete(source)
	return enabled, true
}

// messagesSwitchRead is the stored Messages switch, its default when nothing
// is stored, or the read error.
func (a *App) messagesSwitchRead(ctx context.Context) (bool, error) {
	if a.settingsDefaultsHeldOff.Load() {
		return false, errSettingsDefaultsUnavailable
	}
	if a.store == nil {
		return false, fmt.Errorf("storage is not ready")
	}
	// Off the writer connection, so a busy background job can't stall it.
	value, found, err := a.store.LookupUIState(ctx, permissionUIStateKey(messagesPermissionKey))
	if err != nil {
		return false, err
	}
	if !found {
		return a.legacySettingsDefaults.Load() || !permissionsOffByDefault[messagesPermissionKey], nil
	}
	return value != "false", nil
}

func (a *App) GetPlatformPermissions() (perfValue PlatformPermissions) {
	defer logPageBinding("GetPlatformPermissions", time.Now(), &perfValue)
	perms := a.pageNativePermissions(func() PlatformPermissions {
		view := getPlatformPermissions()
		a.startCaptureFromLivePermissions(view)
		return view
	})

	// Apply stored toggle state for toggleable permissions.
	// The toggleable microphone permission mirrors the background-listening
	// setting — it's the same user-facing concept (capture audio for memories),
	// so we read from the single source of truth instead of a separate flag.
	for i := range perms.Items {
		if !perms.Items[i].Toggleable {
			continue
		}
		if perms.Items[i].Key == "microphone" {
			if microphoneSwitchIsSource() {
				perms.Items[i].Enabled = backgroundMicrophoneEnabled()
			} else {
				perms.Items[i].Enabled = a.GetBackgroundListening()
			}
		} else {
			on, err := a.permissionSwitchRead(context.Background(), perms.Items[i].Key)
			perms.Items[i].Enabled, perms.Items[i].EnabledUnknown = on, err != nil
		}
	}

	return perms
}

// Cached display state never authorizes capture. Only a live probe reaches here.
func (a *App) startCaptureFromLivePermissions(perms PlatformPermissions) {
	// Auto-start activity monitor when all required permissions become granted.
	// Pass the already-checked values to avoid redundant CGo calls.
	allGranted := true
	var a11y, screenCap bool
	for _, item := range perms.Items {
		switch item.Key {
		case "accessibility":
			a11y = item.Granted
		case "screen_recording":
			screenCap = item.Granted
		}
		if item.Required && !item.Granted {
			allGranted = false
		}
	}
	if allGranted && a != nil && a.ctx != nil {
		a.startActivityMonitorIfPermittedWith(a11y, screenCap)
	}

}

// microphoneSwitchIsSource reports whether the microphone item's switch is the
// microphone source of background listening (macOS, where system audio is a
// source of its own) rather than background listening as a whole.
func microphoneSwitchIsSource() bool {
	return runtime.GOOS == "darwin"
}

// RequestPermission asks the OS for one permission on the user's behalf.
// Requests run one at a time, so the user answers one dialog before the next
// appears (see permission_prompts.go).
func (a *App) RequestPermission(key string) (granted bool) {
	a.clearPageSnapshots()
	defer a.clearPageSnapshots()
	runPermissionRequest(func() { granted = requestPermission(key) })
	// The Grant button is the only place that asks macOS for calendar access;
	// once it is granted the reader starts without waiting for its next tick.
	if granted {
		a.notifyCapturePermissionGranted(key)
	}
	return granted
}

// A grant wakes the reader only after the app switch can be read. A failed
// read is neither a grant to capture nor a user request to switch off.
func (a *App) notifyCapturePermissionGranted(key string) {
	switch key {
	case calendarPermissionKey, remindersPermissionKey, mailPermissionKey, messagesPermissionKey:
		on, err := a.permissionSwitchRead(context.Background(), key)
		if err != nil {
			return
		}
		switch key {
		case calendarPermissionKey:
			a.calendarPermissionChanged(true, on)
		case remindersPermissionKey:
			a.remindersPermissionChanged(true, on)
		case mailPermissionKey:
			a.mailPermissionChanged(true, on)
		case messagesPermissionKey:
			a.imessagePermissionChanged(on)
		}
	}
}

func (a *App) OpenPermissionSettings(key string) error {
	return openPermissionSettings(key)
}

// PermissionSourceRect describes a screen rectangle (top-left origin, in
// CSS/AppKit points) used as the launch source for the Permission overlay
// arc-in animation.
type PermissionSourceRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// OpenPermissionSettingsFromRect opens the System Settings permission panel
// for `key` and, on macOS, presents the drag-source overlay arcing in from
// the supplied source rectangle (typically the screen-space frame of the
// button the user clicked).
func (a *App) OpenPermissionSettingsFromRect(key string, rect PermissionSourceRect) error {
	return openPermissionSettingsFromRect(key, rect.X, rect.Y, rect.Width, rect.Height)
}

// SetPermissionEnabled toggles a permission on or off (only for toggleable permissions).
func (a *App) SetPermissionEnabled(key string, enabled bool) error {
	if key == messagesPermissionKey && a.settingsDefaultsHeldOff.Load() {
		return errSettingsDefaultsUnavailable
	}
	a.clearPageSnapshots()
	defer a.clearPageSnapshots()
	perms := getPlatformPermissions()
	found := false
	for _, item := range perms.Items {
		if item.Key == key && item.Toggleable {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("permission %q is not toggleable", key)
	}

	// On macOS the microphone switch pauses only the microphone source of
	// background listening; system audio keeps going. Elsewhere it mirrors
	// background listening — route through SetBackgroundListening so there's
	// a single source of truth and the usual side-effects (whisper warm-up,
	// closed-eyes guard, change event) happen consistently. Either way, skip
	// the generic ui_state persistence path.
	if key == "microphone" {
		var err error
		if microphoneSwitchIsSource() {
			err = a.setBackgroundMicrophoneEnabled(enabled)
		} else {
			err = a.SetBackgroundListening(enabled)
		}
		if err != nil {
			return err
		}
		a.emitBrowserEvent("permissions:changed")
		return nil
	}

	if a.store != nil {
		val := "true"
		if !enabled {
			val = "false"
		}
		if err := a.store.SetUIState(context.Background(), permissionUIStateKey(key), val); err != nil {
			return fmt.Errorf("failed to save permission state: %w", err)
		}
	}

	// Apply the change to the activity monitor.
	switch key {
	case "screen_capture", "screen_recording", "pipewire":
		if enabled {
			log.Printf("[permissions] %s enabled by user", key)
			a.startActivityMonitorIfPermitted()
		} else {
			log.Printf("[permissions] %s disabled by user", key)
			a.stopActivityMonitor()
		}
		a.browserHistoryWatchingChanged(enabled)
	case "input_automation", "accessibility", "uinput":
		if enabled {
			log.Printf("[permissions] %s enabled by user", key)
			a.startActivityMonitorIfPermitted()
		} else {
			log.Printf("[permissions] %s disabled by user", key)
			a.stopActivityMonitor()
		}
		a.browserHistoryWatchingChanged(enabled)
	case calendarPermissionKey:
		log.Printf("[permissions] %s reading switched %t by user", key, enabled)
		a.calendarPermissionChanged(calendarAccessGranted(), enabled)
	case remindersPermissionKey:
		log.Printf("[permissions] %s reading switched %t by user", key, enabled)
		a.remindersPermissionChanged(remindersAccessGranted(), enabled)
	case clipboardPermissionKey:
		log.Printf("[permissions] %s noting switched %t by user", key, enabled)
		a.clipboardSwitchChanged(enabled)
	case filesPermissionKey:
		log.Printf("[permissions] %s reading switched %t by user", key, enabled)
		a.filesSwitchChanged(enabled)
	case mailPermissionKey:
		log.Printf("[permissions] %s reading switched %t by user", key, enabled)
		a.mailPermissionChanged(mailAllowedCache.Load() == 1, enabled)
	case messagesPermissionKey:
		log.Printf("[permissions] %s reading switched %t by user", key, enabled)
		a.imessagePermissionChanged(enabled)
	}

	a.emitBrowserEvent("permissions:changed")

	return nil
}
