package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// User-configurable global hotkey for the Quick Chat Spotlight. Persisted
// to ~/.yansu-agent/quick_chat_hotkey.json so the value survives relaunch
// and is read by the platform hook on startup (and on every keystroke,
// so SetQuickChatHotkey takes effect without re-installing the hook).
//
// The combo shape mirrors the JS-side useKeybindings KeyCombo: a boolean
// for each of the four modifiers plus a lowercase `key` (e.g. "y",
// "space", "f5"). Character shortcuts require at least one modifier;
// the double-tap and Off sentinels do not represent character keys.

const quickChatHotkeyFile = "quick_chat_hotkey.json"

// QuickChatHotkeyConfig is the persisted shape. Field names are lowercase
// to match the JS KeyCombo so the renderer can round-trip the value
// verbatim via Wails.
type QuickChatHotkeyConfig struct {
	Meta  bool   `json:"meta"`
	Ctrl  bool   `json:"ctrl"`
	Alt   bool   `json:"alt"`
	Shift bool   `json:"shift"`
	Key   string `json:"key"`
}

var (
	quickChatHotkeyMuConfig sync.RWMutex
	quickChatHotkeyCurrent  QuickChatHotkeyConfig

	// Recording mode lets the renderer ask the OS-level hook to capture
	// the next keystroke and emit it back as a Wails event, instead of
	// triggering the configured combo. This is the only way to record
	// combos the OS would otherwise eat before they reach the renderer
	// (Alt+Space → system menu, Win+L → lock screen, etc.). Mirrors how
	// Raycast / Alfred / PowerToys handle binding recording.
	quickChatHotkeyRecording atomic.Bool
)

// quickChatHotkeyKeyOptOpt is the sentinel key value that represents
// the macOS "double-tap Option" default. It cannot be produced by a
// normal KeyboardEvent; it is selected directly in Settings or loaded as
// the platform default.
const quickChatHotkeyKeyOptOpt = "opt-opt"

// Off uses the existing key field, so old bindings keep their stored shape.
const quickChatHotkeyKeyOff = "off"

// defaultQuickChatHotkey returns the factory default — kept identical
// to the original Yansu defaults (Alt+Space on Windows, double-tap
// Option on macOS). If the default conflicts with another tool on a
// given machine, the user rebinds in settings rather than us changing
// the default for everyone.
func defaultQuickChatHotkey() QuickChatHotkeyConfig {
	switch runtime.GOOS {
	case "darwin":
		return QuickChatHotkeyConfig{Key: quickChatHotkeyKeyOptOpt}
	default:
		return QuickChatHotkeyConfig{Alt: true, Key: " "}
	}
}

func quickChatHotkeyPath() string {
	dir := configDirOrFallback()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, quickChatHotkeyFile)
}

// loadQuickChatHotkeyFromDisk runs once at startup before the hook
// installs itself. Falls back to the platform default whenever the file
// is missing, malformed, or fails validation — never returns an
// unusable config.
func loadQuickChatHotkeyFromDisk() {
	cfg := defaultQuickChatHotkey()
	path := quickChatHotkeyPath()
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var parsed QuickChatHotkeyConfig
			if jerr := json.Unmarshal(b, &parsed); jerr == nil {
				// NB: lowercase only, never TrimSpace — " " (space) is a
				// valid key and TrimSpace would collapse it to "".
				parsed.Key = strings.ToLower(parsed.Key)
				if validateQuickChatHotkey(parsed) == nil {
					cfg = parsed
				}
			}
		}
	}
	quickChatHotkeyMuConfig.Lock()
	quickChatHotkeyCurrent = cfg
	quickChatHotkeyMuConfig.Unlock()
	applyHotkeyToPlatform(cfg)
}

func currentQuickChatHotkey() QuickChatHotkeyConfig {
	quickChatHotkeyMuConfig.RLock()
	defer quickChatHotkeyMuConfig.RUnlock()
	if quickChatHotkeyCurrent.Key == "" {
		return defaultQuickChatHotkey()
	}
	return quickChatHotkeyCurrent
}

// validateQuickChatHotkey requires a non-empty character key plus at
// least one modifier. A bare key is
// disallowed because it would swallow normal typing — every mainstream
// launcher (Raycast, Alfred, PowerToys Run) imposes the same rule.
// The opt-opt and off sentinels are exempt: neither is a character shortcut.
func validateQuickChatHotkey(c QuickChatHotkeyConfig) error {
	if c.Key == "" {
		return errors.New("hotkey: key must not be empty")
	}
	if c.Key == quickChatHotkeyKeyOptOpt || c.Key == quickChatHotkeyKeyOff {
		return nil
	}
	if !(c.Meta || c.Ctrl || c.Alt || c.Shift) {
		return errors.New("hotkey: at least one modifier (Cmd/Ctrl/Alt/Shift) is required")
	}
	return nil
}

// GetQuickChatHotkey is the Wails binding the renderer calls on mount to
// hydrate the visible binding for the `quick-chat-global` action.
// tapQuickChatHotkey is the Quick chat key the global key tap matches: the
// saved one while Chat is on, none while it is off. The same tap carries
// Closed Eyes' fixed key (closedEyesHotkeyDisplay), so it runs either way.
func tapQuickChatHotkey() QuickChatHotkeyConfig {
	if chatFeatureErr() != nil {
		return QuickChatHotkeyConfig{Key: quickChatHotkeyKeyOff}
	}
	return currentQuickChatHotkey()
}

func (a *App) GetQuickChatHotkey() QuickChatHotkeyConfig {
	return currentQuickChatHotkey()
}

// SetQuickChatHotkeyRecording toggles recording mode. While on, the
// platform hook captures keystrokes with at least one modifier, emits
// them via the `quickChat:hotkeyRecorded` Wails event, and swallows
// them so the OS doesn't react. The renderer turns this on when the
// recorder UI opens and back off when it closes.
func (a *App) SetQuickChatHotkeyRecording(on bool) {
	// The recorder is Quick chat's; with Chat off the tap never swallows keys.
	quickChatHotkeyRecording.Store(on && chatFeatureErr() == nil)
}

// SetQuickChatHotkey persists a new combo and updates the live config
// the hook reads. No need to reinstall the OS hook — the callback reads
// the current value on each event.
func (a *App) SetQuickChatHotkey(c QuickChatHotkeyConfig) error {
	if err := chatFeatureErr(); err != nil {
		return err
	}
	c.Key = strings.ToLower(c.Key) // do NOT TrimSpace — " " (space) is valid
	if err := validateQuickChatHotkey(c); err != nil {
		return err
	}
	quickChatHotkeyMuConfig.Lock()
	quickChatHotkeyCurrent = c
	quickChatHotkeyMuConfig.Unlock()

	applyHotkeyToPlatform(c)

	path := quickChatHotkeyPath()
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		log.Printf("[quickchat] persist hotkey: %v", err)
		return err
	}
	return nil
}
