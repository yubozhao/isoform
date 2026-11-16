package main

import (
	"context"
	"encoding/json"
	"time"

	"wails-gui/internal/privacy"
)

const voiceInputAppsUIStateKey = "voice_input_apps"

// GetVoiceInputApps returns the combined built-in + user-added list of
// voice-input applications for the settings UI. Cheap (in-memory) — the
// frontend separately calls ListSystemApps() for icons / localized names
// and enriches the displayed entries.
func (a *App) GetVoiceInputApps() (perfValue []VoiceInputAppEntry) {
	defer logPageBinding("GetVoiceInputApps", time.Now(), &perfValue)
	return AllVoiceInputAppEntries()
}

// AddVoiceInputApp adds a user-configured voice-input app and persists the
// change. Settings UI calls this when the user picks an app from the
// system app picker.
func (a *App) AddVoiceInputApp(bundleID, appName string) error {
	defer publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.App, ID: bundleID})
	AddUserVoiceInputApp(bundleID, appName)
	return a.persistUserVoiceInputApps()
}

// RemoveVoiceInputApp removes a user-configured voice-input app and
// persists the change. No-op when called with a built-in bundle ID
// (the underlying RemoveUserVoiceInputApp handles that).
func (a *App) RemoveVoiceInputApp(bundleID string) error {
	RemoveUserVoiceInputApp(bundleID)
	return a.persistUserVoiceInputApps()
}

// persistUserVoiceInputApps serialises the current user-list map to the
// ui_state store. Mirrors persistUserSensitiveApps in
// sensitive_apps_bindings.go so behavior across user-editable app lists
// stays consistent.
func (a *App) persistUserVoiceInputApps() error {
	apps := GetUserVoiceInputApps()
	data, err := json.Marshal(apps)
	if err != nil {
		return err
	}
	if a.store == nil {
		return nil
	}
	return a.store.SetUIState(context.Background(), voiceInputAppsUIStateKey, string(data))
}

// loadUserVoiceInputApps restores user-configured voice-input apps from the
// ui_state store. Called during App startup so the denylist is active
// before background listening starts.
func (a *App) loadUserVoiceInputApps() {
	if a.store == nil {
		return
	}
	raw, err := a.store.GetUIState(context.Background(), voiceInputAppsUIStateKey)
	if err != nil || raw == "" {
		return
	}
	var apps map[string]string
	if json.Unmarshal([]byte(raw), &apps) == nil {
		SetUserVoiceInputApps(apps)
	}
}
