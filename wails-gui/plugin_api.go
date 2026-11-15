package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"wails-gui/internal/engagement"
	"wails-gui/internal/plugins"
)

// PluginInfo contains translation keys, never untranslated product copy.
type PluginInfo struct {
	ID              string `json:"id"`
	NameKey         string `json:"nameKey"`
	DescriptionKey  string `json:"descriptionKey"`
	Version         string `json:"version"`
	Kind            string `json:"kind"`
	Source          string `json:"source"`
	Enabled         bool   `json:"enabled"`
	Essential       bool   `json:"essential"`
	LockedReasonKey string `json:"lockedReasonKey"`
	ReadErrorKey    string `json:"readErrorKey"`
}

func builtinPluginRegistry() (*plugins.Registry, []plugins.Builtin) {
	registry := plugins.NewRegistry()
	builtins := plugins.BuiltinsForPlatform(runtime.GOOS)
	for _, builtin := range builtins {
		registry.Register(builtin.Manifest)
	}
	return registry, builtins
}

// ListPlugins only reads the existing switches. In particular it never starts
// services, provisions a schedule, or writes defaults when Settings opens.
func (a *App) ListPlugins() ([]PluginInfo, error) {
	if a == nil {
		return nil, errors.New("app is not ready")
	}
	a.pluginMu.Lock()
	defer a.pluginMu.Unlock()
	return a.listPlugins()
}

func (a *App) listPlugins() ([]PluginInfo, error) {
	_, builtins := builtinPluginRegistry()
	reader := plugins.ReaderFunc(a.LoadUIState)
	values := make([]PluginInfo, 0, len(builtins))
	for _, builtin := range builtins {
		enabled, err := a.builtinPluginEnabled(builtin, reader)
		view := PluginInfo{ID: builtin.ID, NameKey: builtin.NameKey, DescriptionKey: builtin.DescriptionKey,
			Version: builtin.Version, Kind: builtin.Kind, Source: builtin.Source, Enabled: enabled, Essential: builtin.Essential}
		if err != nil {
			view.Enabled = false
			view.ReadErrorKey = "plugins.readError"
		}
		if builtin.Essential {
			view.LockedReasonKey = "plugins.essentialLocked"
		}
		values = append(values, view)
	}
	return values, nil
}

func (a *App) builtinPluginEnabled(builtin plugins.Builtin, reader plugins.Reader) (bool, error) {
	if builtin.Essential {
		return true, nil
	}
	if builtin.StateKey != "" {
		raw, err := reader.Read(builtin.StateKey)
		return builtin.StoredOn(raw), err
	}
	switch builtin.ID {
	case "activity-monitor":
		return a.GetActivityMonitorStatus().Monitoring, nil
	case "background-listening":
		return a.GetBackgroundListening(), nil
	case "background-microphone":
		if microphoneSwitchIsSource() {
			return backgroundMicrophoneEnabled(), nil
		}
		return a.GetBackgroundListening(), nil
	case "meeting-recording":
		return !loadConfig().AutomaticMeetingRecordingDisabled, nil
	case "auto-speak":
		return a.GetAutoSpeakEnabled(), nil
	case "auto-speak-automation":
		return a.GetAutoSpeakAutomationEnabled(), nil
	case "auto-speak-notifications":
		return a.GetAutoSpeakNotificationsEnabled(), nil
	case "browser-bridge":
		return a.GetBrowserBridgeEnabled(), nil
	case "start-at-login":
		return a.GetStartAtLogin()
	case "topic-finds":
		dir, err := topicsStateDir()
		if err != nil {
			return false, err
		}
		return newTopicsStore(dir).load().enabled(), nil
	case "memory-dream", "memory-dream-llm":
		cfg, err := readMemoryDreamSettings()
		if builtin.ID == "memory-dream-llm" {
			return cfg.LLMEnabled, err
		}
		return cfg.Enabled, err
	case "text-channel":
		cfg, err := readPluginTextChannelConfig()
		return cfg.Enabled, err
	case "mobile-connection":
		dir, err := mobileStateDirectory()
		if err != nil {
			return false, err
		}
		cfg, err := loadMobileRelayConfig(dir)
		return cfg.Enabled, err
	case "mobile-brief", "brief-podcast":
		dir, err := mobileStateDirectory()
		if err != nil {
			return false, err
		}
		cfg := newMobileBriefConfigStore(dir).load()
		if builtin.ID == "brief-podcast" {
			return cfg.Feed, nil
		}
		return cfg.Enabled, nil
	default:
		if strings.HasPrefix(builtin.ID, "notify-") {
			kind := engagement.Kind(strings.TrimPrefix(builtin.ID, "notify-"))
			spec, ok := engagement.Spec(kind)
			if !ok {
				return false, fmt.Errorf("unknown notification kind %q", kind)
			}
			if svc := a.engagementService(); svc != nil {
				return svc.Prefs().KindEnabled(spec), nil
			}
			if a.store != nil {
				rows, err := a.store.ListEngagementPrefs(context.Background())
				if err != nil {
					return false, err
				}
				for _, row := range rows {
					if row.Kind == string(kind) && row.Enabled >= 0 {
						return row.Enabled == 1, nil
					}
				}
			}
			return spec.DefaultOn, nil
		}
		return false, fmt.Errorf("plugin %q has no reader", builtin.ID)
	}
}

func readPluginTextChannelConfig() (TextChannelConfig, error) {
	dir, err := textChannelDir()
	if err != nil {
		return TextChannelConfig{}, err
	}
	return loadTextChannelConfig(dir)
}

// SetBuiltinOn delegates every write to an existing setter. Dependencies are
// enabled first; dependents are disabled first. An error stops the plan and is
// returned; already-applied legacy setters retain their own persistence rules.
func (a *App) SetBuiltinOn(id string, enabled bool) ([]PluginInfo, error) {
	if a == nil {
		return nil, errors.New("app is not ready")
	}
	a.pluginMu.Lock()
	defer a.pluginMu.Unlock()
	registry, builtins := builtinPluginRegistry()
	// Validate before reading anything, so an essential cannot be changed even
	// while an unrelated settings file is unreadable.
	if err := registry.ValidateSet(id, enabled); err != nil {
		return nil, err
	}
	views, err := a.listPlugins()
	if err != nil {
		return nil, err
	}
	current := make(map[string]bool, len(views))
	unreadable := make(map[string]bool)
	for _, view := range views {
		current[view.ID] = view.Enabled
		if view.ReadErrorKey != "" {
			unreadable[view.ID] = true
			// Force an affected unknown row into the plan, even when turning
			// off. Its state cannot safely be inferred from the display value.
			current[view.ID] = !enabled
		}
	}
	plan, err := registry.SetPlan(id, enabled, current)
	if err != nil {
		return nil, err
	}
	for _, change := range plan {
		if unreadable[change.ID] {
			return nil, fmt.Errorf("plugin settings are unreadable: %s", change.ID)
		}
	}
	for _, change := range plan {
		for _, builtin := range builtins {
			if builtin.ID == change.ID {
				if err := a.applyBuiltinPlugin(builtin, change.On); err != nil {
					return nil, fmt.Errorf("set plugin %s: %w", builtin.ID, err)
				}
				break
			}
		}
	}
	a.emitBrowserEvent("plugins:changed")
	return a.listPlugins()
}

func (a *App) applyBuiltinPlugin(builtin plugins.Builtin, enabled bool) error {
	if builtin.Essential {
		if !enabled {
			return fmt.Errorf("essential plugin %q cannot be turned off", builtin.ID)
		}
		return nil
	}
	if builtin.Setter == "SaveUIState" {
		if a.store == nil {
			return errors.New("the store is not ready")
		}
		return a.SaveUIState(builtin.StateKey, strconv.FormatBool(enabled))
	}
	if strings.HasPrefix(builtin.StateKey, "permission_enabled:") {
		return a.SetPermissionEnabled(strings.TrimPrefix(builtin.StateKey, "permission_enabled:"), enabled)
	}
	switch builtin.ID {
	case "calendar-peek":
		return a.SetCalendarPeekEnabled(enabled)
	case "activity-monitor":
		if enabled && (a.IsClosedEyesModeEnabled() || !a.screenWatchingEnabled()) {
			return errors.New("screen observation is paused or switched off in Observation")
		}
		return a.SetActivityMonitorEnabled(enabled)
	case "background-listening":
		return a.SetBackgroundListening(enabled)
	case "background-microphone":
		return a.SetPermissionEnabled("microphone", enabled)
	case "meeting-recording":
		return a.SetAutomaticMeetingRecordingEnabled(enabled)
	case "auto-speak":
		return a.SetAutoSpeakEnabled(enabled)
	case "auto-speak-automation":
		return a.SetAutoSpeakAutomationEnabled(enabled)
	case "auto-speak-notifications":
		return a.SetAutoSpeakNotificationsEnabled(enabled)
	case "browser-bridge":
		return a.SetBrowserBridgeEnabled(enabled)
	case "start-at-login":
		return a.SetStartAtLogin(enabled)
	case "topic-finds":
		_, err := a.SetTopicFindsEnabled(enabled)
		return err
	case "memory-dream", "memory-dream-llm":
		if a.memoryDream == nil {
			return errors.New("dream service not initialized")
		}
		field := "enabled"
		if builtin.ID == "memory-dream-llm" {
			field = "llmEnabled"
		}
		return a.memoryDream.SetSwitch(field, enabled)
	case "text-channel":
		cfg, err := readPluginTextChannelConfig()
		if err != nil {
			return err
		}
		_, err = a.SetTextChannel(TextChannelInput{Enabled: enabled, Number: cfg.Number, IncludeNames: cfg.IncludeNames})
		return err
	case "mobile-connection":
		if enabled {
			_, err := a.EnableMobileConnection("")
			return err
		}
		_, err := a.DisableMobileConnection()
		return err
	case "mobile-brief", "brief-podcast":
		cfg, err := a.GetMobileBriefSchedule()
		if err != nil {
			return err
		}
		if builtin.ID == "brief-podcast" {
			cfg.FeedEnabled = enabled
		} else {
			cfg.Enabled = enabled
		}
		_, err = a.SetMobileBriefSchedule(cfg)
		return err
	default:
		if strings.HasPrefix(builtin.ID, "notify-") {
			_, err := a.SetEngagementKindEnabled(strings.TrimPrefix(builtin.ID, "notify-"), enabled)
			return err
		}
		return fmt.Errorf("plugin %q has no setter", builtin.ID)
	}
}
