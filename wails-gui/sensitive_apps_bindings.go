package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/privacy"
)

// GetSensitiveApps returns the stored sensitive-apps list. This call is
// intentionally cheap — it reads only the in-memory pii state and does NOT
// trigger a /Applications scan. The frontend is responsible for asynchronously
// fetching ListSystemApps() and enriching the displayed entries with current-
// locale display names and icons. This split lets the saved-apps list render
// instantly on settings open, with icons popping in once the system scan
// completes (or immediately if its cache is already warm).
func (a *App) GetSensitiveApps() (perfValue []pii.SensitiveAppEntry) {
	defer logPageBinding("GetSensitiveApps", time.Now(), &perfValue)
	return pii.GetAllSensitiveApps()
}

// AddSensitiveApp adds a user-configured sensitive app and persists the change.
func (a *App) AddSensitiveApp(bundlePrefix, category string) error {
	// The in-memory policy changes even if persistence fails. Notify readers
	// after all withdrawal work, including the deferred Mail cleanup.
	defer a.emitBrowserEvent("sensitive-apps:updated")
	defer publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.App, ID: bundlePrefix})
	a.browserHistoryProtectionChanged()
	pii.AddUserSensitiveApp(bundlePrefix, category)
	// Proposed know-how whose evidence touches the app is withdrawn at once;
	// the same read gate keeps it from ever being proposed again.
	a.withdrawKnowHowCandidatesForProtection()
	// Documents used with the app are deleted at the next pass, and what
	// was kept from Mail at once when the app is Mail.
	defer a.filesProtectionChanged()
	defer a.mailProtectionChanged()
	defer a.imessageProtectionChanged()
	return errors.Join(a.scrubSensitiveTopics(), a.persistUserSensitiveApps())
}

// RemoveSensitiveApp removes a user-configured sensitive app and persists the change.
func (a *App) RemoveSensitiveApp(bundlePrefix string) error {
	defer a.emitBrowserEvent("sensitive-apps:updated")
	// A browser protected until now is read again only from this moment.
	a.browserHistoryProtectionChanged()
	// So is a document used with the app.
	a.filesProtectionLifted()
	pii.RemoveUserSensitiveApp(bundlePrefix)
	return a.persistUserSensitiveApps()
}

// ListSystemApps returns applications found in /Applications with their
// display names and bundle identifiers for the frontend app picker.
func (a *App) ListSystemApps() (perfValue []SystemApp) {
	defer logPageBinding("ListSystemApps", time.Now(), &perfValue)
	return listSystemApps()
}

// GetAppIconsForBundleIDs resolves native app icons (64x64 base64 PNG data
// URLs) for the given bundle ids, keyed by bundle id. Bundle ids the system
// cannot resolve are omitted, so callers can treat an absent key as a
// definitive miss and apply their own fallback. Unlike ListSystemApps this
// needs no directory scan, making it cheap enough for on-demand timeline use.
func (a *App) GetAppIconsForBundleIDs(bundleIDs []string) map[string]string {
	return appIconsForBundleIDs(bundleIDs)
}

// persistUserSensitiveApps serialises the current user-sensitive-apps map
// to the ui_state store.
func (a *App) persistUserSensitiveApps() error {
	apps := pii.GetUserSensitiveApps()
	data, err := json.Marshal(apps)
	if err != nil {
		return err
	}
	if a.store == nil {
		return nil
	}
	return a.store.SetUIState(context.Background(), "sensitive_apps", string(data))
}

// loadUserSensitiveApps restores user-configured sensitive apps from the
// ui_state store. Called during startup.
func (a *App) loadUserSensitiveApps() {
	if a.store == nil {
		return
	}
	raw, err := a.store.GetUIState(context.Background(), "sensitive_apps")
	if err != nil || raw == "" {
		return
	}
	var apps map[string]string
	if json.Unmarshal([]byte(raw), &apps) == nil {
		pii.SetUserSensitiveApps(apps)
		if err := a.scrubSensitiveTopics(); err != nil {
			log.Printf("[topics] sensitive policy withdrawal failed: %v", err)
		}
		// Entries covered by the hardcoded defaults are pruned by
		// SetUserSensitiveApps (legacy configs saved before those apps
		// became defaults). Persist the pruned map so the stale entries
		// don't linger in the store.
		if len(pii.GetUserSensitiveApps()) != len(apps) {
			_ = a.persistUserSensitiveApps()
		}
	}
}
