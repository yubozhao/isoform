package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// setupPermissionOrder is the order setup's Turn on asks (setup v2, screen 3):
// the day, the calls, the apps, and Screen Recording last, because granting
// it makes macOS reopen Isoform. Notifications are asked on screen 5.
var setupPermissionOrder = []string{calendarPermissionKey, remindersPermissionKey, "contacts", mailPermissionKey, "microphone", "system_audio", "accessibility", "screen_recording"}

// setupPermissionStep is the onboarding step a reopen comes back to.
const setupPermissionStep = "sees"

// setupPermissionUnavailable is the answer for a prompt macOS could not show:
// Mail is asked about only while it runs, and Isoform never opens it.
const setupPermissionUnavailable = "unavailable"

type SetupPermissionAnswer struct {
	Key   string `json:"key"`
	State string `json:"state"`
}
type SetupPermissions struct {
	Platform            string                  `json:"platform,omitempty"`
	Answers             []SetupPermissionAnswer `json:"answers"`
	Running             bool                    `json:"running"`
	ResumeStep          string                  `json:"resumeStep"`
	Next                int                     `json:"next"`
	NotificationGranted *bool                   `json:"notificationGranted"`
}
type permissionSequence struct {
	mu      sync.Mutex
	running bool
}

func saveSetupJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".setup-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func setupPermissionsPath() string {
	return filepath.Join(configDirOrFallback(), "setup-permissions.json")
}
func loadSetupPermissions(path string) SetupPermissions {
	p := SetupPermissions{Answers: []SetupPermissionAnswer{}, ResumeStep: setupPermissionStep}
	raw, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	p.Running = false
	answers := make([]SetupPermissionAnswer, 0, len(setupPermissionOrder))
	for _, key := range setupPermissionOrder {
		answer := SetupPermissionAnswer{Key: key, State: "off"}
		for _, saved := range p.Answers {
			if saved.Key == key {
				answer = saved
				break
			}
		}
		answers = append(answers, answer)
	}
	p.Answers = answers
	return p
}
func sequencePermissions(path string, request func(string) bool, emit func(SetupPermissions), screenAtLaunch bool) error {
	return sequencePermissionsWith(path, request, func(string) bool { return true }, emit, screenAtLaunch)
}

// sequencePermissionsWith asks each key in order. askable reports whether
// macOS can show that prompt now; a key it can't is recorded unavailable.
func sequencePermissionsWith(path string, request func(string) bool, askable func(string) bool, emit func(SetupPermissions), screenAtLaunch bool) error {
	p := loadSetupPermissions(path)
	p.Running = true
	p.ResumeStep = setupPermissionStep
	// A process that exited inside an OS dialog repeats that key only when
	// the user explicitly presses Turn on all again; launch never prompts.
	for i, key := range setupPermissionOrder {
		p.Next = i
		if err := saveSetupJSON(path, p); err != nil {
			return err
		}
		state := setupPermissionUnavailable
		granted := false
		if askable(key) {
			granted = request(key)
			state = "denied"
			if granted {
				state = "on"
			}
		}
		if key == "screen_recording" && granted && !screenAtLaunch {
			state = "onAfterReopen"
		}
		answer := SetupPermissionAnswer{Key: key, State: state}
		found := false
		for j := range p.Answers {
			if p.Answers[j].Key == key {
				p.Answers[j] = answer
				found = true
			}
		}
		if !found {
			p.Answers = append(p.Answers, answer)
		}
		p.Next = i + 1
		if err := saveSetupJSON(path, p); err != nil {
			return err
		}
		emit(p)
	}
	p.Running = false
	if err := saveSetupJSON(path, p); err != nil {
		return err
	}
	emit(p)
	return nil
}
func (a *App) GetSetupPermissions() SetupPermissions {
	p := loadSetupPermissions(setupPermissionsPath())
	a.setupPermissions.mu.Lock()
	p.Running = a.setupPermissions.running
	a.setupPermissions.mu.Unlock()
	p.NotificationGranted = setupNotificationPermission()
	return setupPermissionsView(p, setupNativePermissions())
}

// Setup keeps canonical keys while each platform supplies its own capabilities.
// Unimplemented integrations are not failed OS permission requests.
func setupPermissionsView(p SetupPermissions, perms PlatformPermissions) SetupPermissions {
	p.Platform = perms.Platform
	for i := range p.Answers {
		key := p.Answers[i].Key
		if perms.Platform == "windows" {
			switch key {
			case calendarPermissionKey, remindersPermissionKey, "contacts", mailPermissionKey:
				p.Answers[i].State = "unsupported"
				continue
			}
		}
	}
	for _, item := range perms.Items {
		for i := range p.Answers {
			if p.Answers[i].Key == item.Key {
				// Screen Recording allowed after launch reads as granted,
				// waiting for the reopen, though the sequence saw "no".
				reopen := item.Key == "screen_recording" && screenRecordingNeedsRelaunch()
				p.Answers[i].State = setupPermissionCurrentState(p.Answers[i].State, item.Granted || reopen, reopen)
			}
		}
	}
	return p
}

// setupNotificationStatus keeps a timed-out native read unknown.
func setupNotificationStatus(status int) *bool {
	switch status {
	case 0, 1:
		granted := false
		return &granted
	case 2, 3:
		granted := true
		return &granted
	default:
		return nil
	}
}

// setupNotificationGranted comes from the live setup probe, never the page snapshot.
func setupNotificationGranted(perms PlatformPermissions) *bool {
	for _, item := range perms.Items {
		if item.Key == "notifications" {
			granted := item.Granted
			return &granted
		}
	}
	return nil
}

// RequestAllPermissions returns after persisting the resume point. Answers
// arrive via setup:permissions. Denial never stops the remaining requests.
func (a *App) RequestAllPermissions() error {
	a.setupPermissions.mu.Lock()
	defer a.setupPermissions.mu.Unlock()
	if a.setupPermissions.running {
		return errors.New("permission sequence already running")
	}
	if a.store == nil {
		return errors.New("setup storage is not ready")
	}
	if err := a.store.SetUIState(context.Background(), "yansu.onboarding.step", setupPermissionStep); err != nil {
		return err
	}
	if err := saveSetupJSON(setupPermissionsPath(), SetupPermissions{Answers: []SetupPermissionAnswer{}, ResumeStep: setupPermissionStep}); err != nil {
		return err
	}
	a.setupPermissions.running = true
	go func() {
		defer func() {
			a.setupPermissions.mu.Lock()
			a.setupPermissions.running = false
			a.setupPermissions.mu.Unlock()
		}()
		var err error
		runPermissionRequest(func() {
			err = sequencePermissionsWith(setupPermissionsPath(), a.requestSetupPermission, setupPermissionAskable, func(p SetupPermissions) {
				p.NotificationGranted = setupNotificationPermission()
				a.emitBrowserEvent("setup:permissions", setupPermissionsView(p, setupNativePermissions()))
			}, setupScreenWasOnAtLaunch())
		})
		if err != nil {
			a.emitBrowserEvent("setup:permissions-error", err.Error())
		}
	}()
	return nil
}
func (a *App) requestSetupPermission(key string) bool {
	var granted bool
	switch key {
	case "contacts":
		// Setup's "Your day" row explains Contacts before Turn on, so this
		// is the one place outside the People sheet that shows its prompt.
		var err error
		granted, err = requestContactsAuthorization()
		if err != nil {
			log.Printf("[setup] contacts access request failed: %v", err)
		}
	default:
		granted = requestPermission(key)
	}
	if key == calendarPermissionKey && granted {
		a.calendarPermissionChanged(true, a.isPermissionEnabled(key))
	}
	if key == remindersPermissionKey && granted {
		a.remindersPermissionChanged(true, a.isPermissionEnabled(key))
	}
	return granted
}

func setupPermissionCurrentState(saved string, granted, reopen bool) string {
	if granted {
		if reopen {
			return "onAfterReopen"
		}
		return "on"
	}
	if saved == "on" || saved == "onAfterReopen" {
		return "off"
	}
	return saved
}
