package main

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// menuBarPlayRequest is the popover's latest play click that the player has
// not answered yet. Its outcome line says how long the click took to play.
var menuBarPlayRequest struct {
	sync.Mutex
	briefID string
	at      time.Time
}

// noteMenuBarPlay records a play click from the popover.
func noteMenuBarPlay(briefID string, at time.Time) {
	menuBarPlayRequest.Lock()
	defer menuBarPlayRequest.Unlock()
	menuBarPlayRequest.briefID = briefID
	menuBarPlayRequest.at = at
}

// logMenuBarPlayOutcome writes the one outcome line for the pending play click
// when Today's player reports the same brief playing, failed or paused. Loading
// is not an outcome, so the click stays pending until the player settles.
func logMenuBarPlayOutcome(briefID, state string, now time.Time) {
	var result string
	switch state {
	case "playing":
		result = "started"
	case "failed":
		result = "failed:player"
	case "idle":
		result = "paused"
	default:
		return
	}
	menuBarPlayRequest.Lock()
	pending, at := menuBarPlayRequest.briefID, menuBarPlayRequest.at
	if pending == "" || pending != briefID {
		menuBarPlayRequest.Unlock()
		return
	}
	menuBarPlayRequest.briefID = ""
	menuBarPlayRequest.Unlock()
	log.Printf("[menubar] action=play brief=%s result=%s elapsed_ms=%d", briefID, result, now.Sub(at).Milliseconds())
}

// SetMenuBarBriefPlayback reports Today's brief player (one player for
// Today and the menu bar) so the menu bar popover shows the same playing
// state and position. It carries only the brief id, the state and the
// position; the audio stays in the app.
func (a *App) SetMenuBarBriefPlayback(briefID, state string, position, duration float64) {
	switch state {
	case "idle", "loading", "playing", "failed":
	default:
		return
	}
	if briefID != "" && !mobileValidBriefID(briefID) {
		return
	}
	logMenuBarPlayOutcome(briefID, state, time.Now())
	// Past briefs learn how far each brief was heard from the same reports.
	a.noteBriefHeard(briefID, position, duration, time.Now())
	data, err := json.Marshal(map[string]any{"briefId": briefID, "state": state, "position": position, "duration": duration})
	if err != nil {
		return
	}
	menuBarPlaybackChanged(string(data))
}
