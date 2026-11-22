package main

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"
)

// computer-trouble v1.0.0: something still wrong, that nobody chose, that
// keeps Isoform from doing what you expect. Said in one place (a line under
// Today's date and the menu bar's first row), never as a notification, and
// gone by itself once fixed. A choice (Closed Eyes, sleep, a watch off the
// wrist) is never trouble.

const (
	troubleScreen    = "screen"
	troubleSignedOut = "signed_out"
	troubleOffline   = "offline"
	troublePhone     = "phone"

	// troublePhoneSilence is how long a paired phone may stay silent
	// before it counts (notify v1.0.0 rule 3).
	troublePhoneSilence = 30 * time.Minute
)

// troubleOrder is rule 1's order, by what each costs you.
var troubleOrder = map[string]int{troubleScreen: 0, troubleSignedOut: 1, troubleOffline: 2, troublePhone: 3}

// TroubleLine is one kind of trouble: what is wrong, since when (RFC 3339),
// for a sign-out which assistant, and the one fix there is to press.
type TroubleLine struct {
	Kind      string `json:"kind"`
	Since     string `json:"since"`
	Assistant string `json:"assistant,omitempty"`
	Fix       string `json:"fix,omitempty"`
	// Never is set on the screen's trouble when Isoform has never had Screen
	// Recording: "hasn't seen your screen yet", with no since (design S8).
	Never bool `json:"never,omitempty"`
}

// TroubleList is GetTrouble's answer: what is wrong now, ranked by rule 1.
type TroubleList struct {
	Items []TroubleLine `json:"items"`
}

const (
	troubleFixOpenSettings = "open_settings"
	troubleFixSignIn       = "sign_in"
)

// troubleSince remembers when Isoform first saw each kind still wrong, in
// memory only, so "since {time}" holds across reads and clears once fixed.
type troubleSince struct {
	mu    sync.Mutex
	first map[string]time.Time
}

func (s *troubleSince) at(kind string, wrong bool, now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first == nil {
		s.first = map[string]time.Time{}
	}
	if !wrong {
		delete(s.first, kind)
		return time.Time{}
	}
	if at, ok := s.first[kind]; ok {
		return at
	}
	s.first[kind] = now
	return now
}

var troubleFirstSeen troubleSince

// troubleScreenOff is true when the person turned on watching the screen
// but macOS has Screen Recording off for Isoform (often after an update).
func troubleScreenOff(items []PermissionItem) bool {
	for _, item := range items {
		if troubleScreenKeys[item.Key] && item.Toggleable && item.Enabled && !item.Granted {
			return true
		}
	}
	return false
}

// troubleScreenGrantedKey is set the first time a check sees Screen
// Recording granted and never cleared, so the screen's trouble can say
// "since {time}" only to someone whose screen Isoform has seen.
const troubleScreenGrantedKey = "isoform.screen.grantedOnce"

// troubleScreenGranted is true when Isoform has Screen Recording now.
func troubleScreenGranted(items []PermissionItem) bool {
	for _, item := range items {
		if troubleScreenKeys[item.Key] && item.Granted {
			return true
		}
	}
	return false
}

// troubleScreenNever reads whether Screen Recording was never granted,
// recording the grant the first time it is seen. A failed read keeps the
// "since" form: Isoform doesn't claim what it doesn't know.
func (a *App) troubleScreenNever(items []PermissionItem) bool {
	if a == nil || a.store == nil {
		return false
	}
	ctx := context.Background()
	if troubleScreenGranted(items) {
		if done, err := a.store.GetUIState(ctx, troubleScreenGrantedKey); err != nil || done != "true" {
			if err := a.store.SetUIState(ctx, troubleScreenGrantedKey, "true"); err != nil {
				log.Printf("[trouble] saving the screen grant: %v", err)
			}
		}
		return false
	}
	done, err := a.store.GetUIState(ctx, troubleScreenGrantedKey)
	if err != nil {
		log.Printf("[trouble] reading the screen grant: %v", err)
		return false
	}
	return done != "true"
}

// troubleScreenKeys are the permission keys that let Isoform see the screen.
var troubleScreenKeys = map[string]bool{"screen_recording": true, "screen_capture": true, "pipewire": true}

// troublePhoneSilentSince is when the last paired phone checked in, when every
// paired phone has been silent past troublePhoneSilence; zero otherwise.
func troublePhoneSilentSince(devices []MobileDeviceView, now time.Time) time.Time {
	var latest time.Time
	for _, device := range devices {
		seen, err := time.Parse(time.RFC3339, device.LastSeenAt)
		if err != nil {
			continue
		}
		if seen.After(latest) {
			latest = seen
		}
	}
	if len(devices) == 0 || latest.IsZero() || now.Sub(latest) < troublePhoneSilence {
		return time.Time{}
	}
	return latest
}

// GetTrouble lists what is wrong now, in rule 1's order. Empty when nothing is.
func (a *App) GetTrouble() TroubleList {
	return TroubleList{Items: a.troubleLines(time.Now())}
}

// OpenTroubleFix presses a trouble's one fix: for the screen, the exact
// Screen Recording pane. A sign-out keeps its own sign-in action.
func (a *App) OpenTroubleFix(kind string) error {
	switch kind {
	case troubleScreen:
		return a.OpenPermissionSettings("screen_recording")
	default:
		return nil
	}
}

func (a *App) troubleLines(now time.Time) []TroubleLine {
	out := []TroubleLine{}
	if a == nil {
		return out
	}
	if a.IsClosedEyesModeEnabled() {
		// Closed Eyes is a choice: the screen being unseen then isn't trouble.
		troubleFirstSeen.at(troubleScreen, false, now)
	} else {
		items := a.troublePermissionItems()
		never := a.troubleScreenNever(items)
		if at := troubleFirstSeen.at(troubleScreen, troubleScreenOff(items), now); !at.IsZero() {
			out = append(out, TroubleLine{Kind: troubleScreen, Since: at.UTC().Format(time.RFC3339), Fix: troubleFixOpenSettings, Never: never})
		}
	}
	if assistant, ok := a.signOut.firstConfirmed(routeOrder(loadAIProviderConfig())); ok {
		since := a.signOut.confirmedSince(assistant)
		if since.IsZero() {
			since = now
		}
		out = append(out, TroubleLine{Kind: troubleSignedOut, Since: since.UTC().Format(time.RFC3339), Assistant: assistant, Fix: troubleFixSignIn})
	}
	// No internet, from the watch's last reading (trouble_watch.go).
	if at := troubleFirstSeen.at(troubleOffline, troubleNetworkDown.Load(), now); !at.IsZero() {
		out = append(out, TroubleLine{Kind: troubleOffline, Since: at.UTC().Format(time.RFC3339)})
	}
	if status, err := a.GetMobileConnectionStatus(); err == nil && status.Enabled {
		if since := troublePhoneSilentSince(status.Devices, now); !since.IsZero() {
			out = append(out, TroubleLine{Kind: troublePhone, Since: since.UTC().Format(time.RFC3339)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return troubleOrder[out[i].Kind] < troubleOrder[out[j].Kind] })
	return out
}

// troublePermissionItems reads the permissions the way GetPlatformPermissions
// does, with the person's switches applied, but never starts capture: the
// check only looks.
func (a *App) troublePermissionItems() []PermissionItem {
	items := getPlatformPermissions().Items
	for i := range items {
		if items[i].Toggleable && troubleScreenKeys[items[i].Key] {
			items[i].Enabled = a.isPermissionEnabled(items[i].Key)
		}
	}
	return items
}
