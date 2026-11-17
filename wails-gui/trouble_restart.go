package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// A stop that fixed itself (computer-trouble v1.0.0 rule 2): when Isoform
// stops without quitting (a crash) and starts again, the minutes between are
// only a gap in Activity, "Isoform restarted · not seen, 5:03 to 5:13 PM".
// Never trouble, never a notification. The trouble watch notes every minute
// that Isoform is running; a quit, by hand or by the system, notes it ended
// cleanly. At the first check after a start, a note that didn't end cleanly
// is a restart.

const (
	troubleAliveFile = "trouble-alive.json"
	troubleRestarted = "restarted"
	// troubleRestartMin: a stop shorter than two missed checks leaves no gap.
	troubleRestartMin = 2 * troubleWatchEvery
	// troubleRestartMax: a longer stop is someone quitting and coming back
	// later, not a stop that fixed itself.
	troubleRestartMax = 2 * time.Hour
)

type troubleAlive struct {
	At    string `json:"at"`
	Clean bool   `json:"clean,omitempty"`
}

var (
	// troubleBootTime is when the computer started; tests replace it. Zero
	// when unknown.
	troubleBootTime = systemBootTime
	// troubleRestartChecked is set once the first check after a start ran.
	troubleRestartChecked atomic.Bool
)

func readTroubleAlive() (troubleAlive, bool) {
	dir, err := flavorDataDir()
	if err != nil {
		return troubleAlive{}, false
	}
	data, err := os.ReadFile(filepath.Join(dir, troubleAliveFile))
	if err != nil {
		return troubleAlive{}, false
	}
	var alive troubleAlive
	if json.Unmarshal(data, &alive) != nil || alive.At == "" {
		return troubleAlive{}, false
	}
	return alive, true
}

func writeTroubleAlive(alive troubleAlive) {
	dir, err := flavorDataDir()
	if err != nil {
		return
	}
	data, err := json.Marshal(alive)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[trouble] could not note that Isoform is running: %v", err)
		return
	}
	path := filepath.Join(dir, troubleAliveFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[trouble] could not note that Isoform is running: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("[trouble] could not note that Isoform is running: %v", err)
	}
}

// noteTroubleAlive runs with every trouble check: the first one after a
// start looks for a restart, then each notes that Isoform is running.
func (a *App) noteTroubleAlive(now time.Time) {
	if a == nil || a.store == nil {
		return
	}
	if troubleRestartChecked.CompareAndSwap(false, true) {
		a.recordTroubleRestart(now)
	}
	writeTroubleAlive(troubleAlive{At: mobileTimestamp(now)})
}

// recordTroubleRestart keeps the stop as a gap when the last run didn't end
// cleanly, the computer kept running, and it lasted two minutes to two hours.
func (a *App) recordTroubleRestart(now time.Time) {
	last, ok := readTroubleAlive()
	if !ok || last.Clean {
		return
	}
	at, err := time.Parse(time.RFC3339, last.At)
	if err != nil {
		return
	}
	stopped := now.Sub(at)
	if stopped < troubleRestartMin || stopped > troubleRestartMax {
		return
	}
	if boot := troubleBootTime(); !boot.IsZero() && boot.After(at) {
		// The computer itself restarted (or lost power): Isoform didn't stop.
		return
	}
	ctx := context.Background()
	var gaps []troubleGap
	if raw, err := a.store.GetUIState(ctx, troubleGapsKey); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &gaps)
	}
	gaps = append(gaps, troubleGap{Kind: troubleRestarted, From: last.At, To: mobileTimestamp(now)})
	raw, err := json.Marshal(gaps)
	if err != nil {
		return
	}
	if err := a.store.SetUIState(ctx, troubleGapsKey, string(raw)); err != nil {
		log.Printf("[trouble] the restart gap was not kept: %v", err)
		return
	}
	log.Printf("[trouble] Isoform restarted; not seen for %s", stopped.Round(time.Minute))
}

// noteTroubleQuit marks this run as ended cleanly: a quit leaves no gap.
func noteTroubleQuit(now time.Time) {
	writeTroubleAlive(troubleAlive{At: mobileTimestamp(now), Clean: true})
}
