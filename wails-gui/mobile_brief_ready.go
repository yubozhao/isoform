package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Every brief that is ready wakes each paired phone once with a silent
// "ready" push (PROTOCOL.md 9.5; hub decision 2026-11-22). The push carries no
// brief content: the relay sends a background push naming only the hub, and
// the phone fetches the newest brief it is due. It is separate from Notify,
// which stays the alerting push and its own rule.
//
// One wake per brief version per phone: the ledger below is on disk, so a
// relaunch that saves the same brief again (seven times on 2026-11-22) does not
// wake the phone again. A new version (an amended brief) is a new wake.

const mobileBriefReadyFileName = "brief-ready.json"

// mobileBriefReadyPaused reports Closed Eyes, during which no phone is woken;
// tests replace it.
var mobileBriefReadyPaused = func(a *App) bool { return a.IsClosedEyesModeEnabled() }

type mobileBriefReadyFile struct {
	Version int               `json:"version"`
	Woken   map[string]string `json:"woken"`
}

// mobileBriefReadyLedger remembers, per phone, the brief version it was last
// woken for. It holds device ids and brief ids only.
type mobileBriefReadyLedger struct {
	mu    sync.Mutex
	dir   string
	woken map[string]string
}

func loadMobileBriefReadyLedger(stateDir string) *mobileBriefReadyLedger {
	ledger := &mobileBriefReadyLedger{dir: stateDir, woken: map[string]string{}}
	if stateDir == "" {
		return ledger
	}
	data, err := os.ReadFile(filepath.Join(stateDir, mobileBriefReadyFileName))
	if err != nil {
		return ledger
	}
	var file mobileBriefReadyFile
	if json.Unmarshal(data, &file) != nil || file.Woken == nil {
		log.Printf("[mobile-brief] the ready ledger could not be read; starting empty")
		return ledger
	}
	ledger.woken = file.Woken
	return ledger
}

func mobileBriefReadyKey(briefID, version string) string { return briefID + "@" + version }

// claim reports whether this phone still needs a wake for this brief version
// and, when it does, records it as taken. The caller undoes a failed send
// with release.
func (l *mobileBriefReadyLedger) claim(deviceID, key string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.woken[deviceID] == key {
		return false
	}
	l.woken[deviceID] = key
	l.persistLocked()
	return true
}

func (l *mobileBriefReadyLedger) release(deviceID, key, previous string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.woken[deviceID] != key {
		return
	}
	if previous == "" {
		delete(l.woken, deviceID)
	} else {
		l.woken[deviceID] = previous
	}
	l.persistLocked()
}

func (l *mobileBriefReadyLedger) last(deviceID string) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.woken[deviceID]
}

// drop forgets an unpaired phone.
func (l *mobileBriefReadyLedger) drop(deviceID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.woken[deviceID]; ok {
		delete(l.woken, deviceID)
		l.persistLocked()
	}
}

// persistLocked writes the ledger. The caller holds l.mu.
func (l *mobileBriefReadyLedger) persistLocked() {
	if l.dir == "" {
		return
	}
	data, err := json.MarshalIndent(mobileBriefReadyFile{Version: 1, Woken: l.woken}, "", "  ")
	if err == nil {
		err = os.MkdirAll(l.dir, 0700)
	}
	if err == nil {
		err = writeMobileBriefFileAtomic(l.dir, mobileBriefReadyFileName, data)
	}
	if err != nil {
		log.Printf("[mobile-brief] the ready ledger could not be saved: %v", err)
	}
}

// briefHanded notes that the relay took a brief for a phone, then wakes that
// phone once for this brief version. The ready frame follows the brief frame
// on the same connection, so the relay has stored the brief before it queues
// the wake. A relay without the frame refuses it and stays connected; the
// phone's own reads stay the guarantee.
func (bridge *mobileBridge) briefHanded(client *mobileRelayClient, deviceID, briefID string) {
	if bridge == nil || briefID == "" {
		return
	}
	bridge.briefOutbox.handed(deviceID, briefID, time.Now())
	entry, ok := bridge.briefOutbox.entry(deviceID)
	if !ok || entry.BriefID != briefID || client == nil {
		return
	}
	bridge.mu.Lock()
	record := bridge.devices[deviceID]
	pushable := record != nil && mobileHasScope(record.Scopes, mobileScopePush)
	bridge.mu.Unlock()
	if !pushable || (bridge.app != nil && mobileBriefReadyPaused(bridge.app)) {
		return
	}
	key := mobileBriefReadyKey(entry.BriefID, entry.Version)
	previous := bridge.briefReady.last(deviceID)
	if !bridge.briefReady.claim(deviceID, key) {
		return
	}
	frame := map[string]any{"t": "ready", "deviceId": deviceID}
	if entry.DeliverAt != "" {
		frame["deliverAt"] = entry.DeliverAt
	}
	if err := client.send(frame); err != nil {
		bridge.briefReady.release(deviceID, key, previous)
		log.Printf("[mobile-brief] ready wake not sent: %v", err)
		return
	}
	log.Printf("[mobile-brief] ready wake queued for 1 phone (held=%t)", entry.DeliverAt != "")
}
