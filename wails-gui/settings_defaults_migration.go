package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"wails-gui/storage"
)

var errSettingsDefaultsUnavailable = errors.New("settings could not be read at startup; reopen the app to retry")

// Keep the store available even if a preference is unreadable. The hold is
// process-local so the next startup attempts the same reads again.
func (a *App) restoreSettingsDefaults(ctx context.Context, store *storage.Store) {
	telemetryTierSaveMu.Lock()
	defer telemetryTierSaveMu.Unlock()
	tier, err := a.migrateSettingsDefaults(ctx, store)
	if err != nil {
		a.settingsDefaultsHeldOff.Store(true)
		tier = TelemetryTierOff
		log.Print("[settings] preferences unavailable; holding Messages, proposals and telemetry off until relaunch")
	}
	telemetryTierMu.Lock()
	telemetryTier = tier
	telemetryTierMu.Unlock()
}

// New stores seed the completion marker when creating ui_state. An older
// store without it keeps the previous defaults until every choice is durable.
func (a *App) migrateSettingsDefaults(ctx context.Context, store *storage.Store) (string, error) {
	marker, marked, err := store.LookupUIState(ctx, storage.SettingsDefaultsMarker)
	if err != nil {
		return "", fmt.Errorf("read settings defaults marker: %w", err)
	}
	fresh := marker == storage.SettingsDefaultsFresh
	if marked && !fresh && marker != "true" {
		return "", fmt.Errorf("invalid settings defaults marker")
	}
	pending := !marked
	path := telemetryTierPath()
	tier, saved, err := readTelemetryTier(path, fresh)
	if err != nil {
		return "", fmt.Errorf("read telemetry tier: %w", err)
	}
	if !saved {
		tier = defaultTelemetryTier
		if pending {
			tier = TelemetryTierDetailed
		}
	}
	if pending {
		// Read every choice before writing any.
		type preference struct{ key, value string }
		var missing []preference
		for _, pref := range []preference{
			{permissionUIStateKey(messagesPermissionKey), "true"},
			{topicsDailyEnabledKey, "false"},
		} {
			_, found, err := store.LookupUIState(ctx, pref.key)
			if err != nil {
				return "", fmt.Errorf("read setting %s: %w", pref.key, err)
			}
			if !found {
				missing = append(missing, pref)
			}
		}
		if !saved {
			err = persistTelemetryTier(path, tier, true)
			if errors.Is(err, os.ErrExist) {
				// A concurrent save won. Resolve the actual choice before capture.
				tier, saved, err = readTelemetryTier(path, fresh)
				if err != nil {
					return "", fmt.Errorf("read concurrently saved telemetry tier: %w", err)
				}
				if !saved {
					return "", errors.New("concurrently saved telemetry tier disappeared")
				}
			}
		}
		if err == nil {
			for _, pref := range missing {
				if err = store.SetUIStateIfAbsent(ctx, pref.key, pref.value); err != nil {
					break
				}
			}
		}
		if err == nil {
			err = store.SetUIState(ctx, storage.SettingsDefaultsMarker, "true")
		}
		if err != nil {
			log.Printf("[settings] keeping previous defaults; migration will retry next launch: %v", err)
		}
	}
	a.legacySettingsDefaults.Store(pending && err != nil)
	return tier, nil
}
