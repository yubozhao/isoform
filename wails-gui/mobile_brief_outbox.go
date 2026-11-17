package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// The brief outbox (PROTOCOL.md section 8.1) keeps, for each paired phone,
// the newest brief published to it until that phone says it saved it. It is
// on disk, so a brief made while the relay was away survives a restart.
const (
	mobileBriefOutboxFileName = "brief-outbox.json"
	// A brief the relay took is sent once more when the phone has not
	// confirmed it after this long.
	mobileBriefOutboxResendAfter = time.Hour
	mobileBriefOutboxExpiry      = 7 * 24 * time.Hour
)

type mobileBriefOutboxEntry struct {
	DeviceID string `json:"deviceId"`
	BriefID  string `json:"briefId"`
	AsOf     string `json:"asOf"`
	Version  string `json:"version"`
	Notify   bool   `json:"notify"`
	// DeliverAt is an early brief's slot time; the relay holds it until then.
	DeliverAt string `json:"deliverAt,omitempty"`
	QueuedAt  string `json:"queuedAt"`
	HandedAt  string `json:"handedAt,omitempty"`
	Resent    bool   `json:"resent,omitempty"`
	// Yielded is set once an awake computer asked the relay to drop this
	// early brief's scheduled push (mobile_brief_yield.go).
	Yielded bool `json:"yielded,omitempty"`
}

type mobileBriefOutboxFile struct {
	Version int                      `json:"version"`
	Entries []mobileBriefOutboxEntry `json:"entries"`
}

type mobileBriefOutbox struct {
	mu      sync.Mutex
	dir     string
	entries map[string]mobileBriefOutboxEntry
}

func loadMobileBriefOutbox(stateDir string) *mobileBriefOutbox {
	outbox := &mobileBriefOutbox{dir: stateDir, entries: map[string]mobileBriefOutboxEntry{}}
	if stateDir == "" {
		return outbox
	}
	data, err := os.ReadFile(filepath.Join(stateDir, mobileBriefOutboxFileName))
	if err != nil {
		return outbox
	}
	var file mobileBriefOutboxFile
	if json.Unmarshal(data, &file) != nil {
		log.Printf("[mobile-brief] the brief outbox could not be read; starting empty")
		return outbox
	}
	for _, entry := range file.Entries {
		if entry.DeviceID != "" && mobileBriefIDPattern.MatchString(entry.BriefID) {
			outbox.entries[entry.DeviceID] = entry
		}
	}
	return outbox
}

// persistLocked writes the outbox. The caller holds o.mu.
func (o *mobileBriefOutbox) persistLocked() {
	if o.dir == "" {
		return
	}
	file := mobileBriefOutboxFile{Version: 1, Entries: make([]mobileBriefOutboxEntry, 0, len(o.entries))}
	for _, entry := range o.entries {
		file.Entries = append(file.Entries, entry)
	}
	sort.Slice(file.Entries, func(i, j int) bool { return file.Entries[i].DeviceID < file.Entries[j].DeviceID })
	data, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = os.MkdirAll(o.dir, 0700)
	}
	if err == nil {
		err = writeMobileBriefFileAtomic(o.dir, mobileBriefOutboxFileName, data)
	}
	if err != nil {
		log.Printf("[mobile-brief] the brief outbox could not be saved: %v", err)
	}
}

// record queues a brief for a phone. A newer brief replaces an older one; an
// older brief never replaces a newer one, and a repeat keeps its first push.
func (o *mobileBriefOutbox) record(entry mobileBriefOutboxEntry) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if current, ok := o.entries[entry.DeviceID]; ok {
		if mobileBriefParseTime(entry.AsOf).Before(mobileBriefParseTime(current.AsOf)) {
			return
		}
		if current.BriefID == entry.BriefID && current.HandedAt != "" {
			entry.Notify = false
		}
	}
	o.entries[entry.DeviceID] = entry
	o.persistLocked()
}

// handed notes that the relay took the brief for a phone.
func (o *mobileBriefOutbox) handed(deviceID, briefID string, at time.Time) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.entries[deviceID]
	if !ok || entry.BriefID != briefID {
		return
	}
	if entry.HandedAt != "" {
		entry.Resent = true
	}
	entry.HandedAt = mobileTimestamp(at)
	o.entries[deviceID] = entry
	o.persistLocked()
}

// acknowledge clears a phone's entry when it saved that brief or a newer one.
func (o *mobileBriefOutbox) acknowledge(deviceID, briefID, asOf string) bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.entries[deviceID]
	if !ok {
		return false
	}
	if entry.BriefID != briefID && (asOf == "" || mobileBriefParseTime(asOf).Before(mobileBriefParseTime(entry.AsOf))) {
		return false
	}
	delete(o.entries, deviceID)
	o.persistLocked()
	return true
}

func (o *mobileBriefOutbox) drop(deviceID string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.entries[deviceID]; ok {
		delete(o.entries, deviceID)
		o.persistLocked()
	}
}

func (o *mobileBriefOutbox) entry(deviceID string) (mobileBriefOutboxEntry, bool) {
	if o == nil {
		return mobileBriefOutboxEntry{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	entry, ok := o.entries[deviceID]
	return entry, ok
}

// due lists the entries to send again now: never taken by the relay, or
// taken over an hour ago without a confirmation and not sent again yet.
// Entries past a week are dropped.
func (o *mobileBriefOutbox) due(now time.Time) []mobileBriefOutboxEntry {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []mobileBriefOutboxEntry
	expired := false
	for id, entry := range o.entries {
		queued := mobileBriefParseTime(entry.QueuedAt)
		if !queued.IsZero() && now.Sub(queued) > mobileBriefOutboxExpiry {
			delete(o.entries, id)
			expired = true
			continue
		}
		if entry.HandedAt == "" {
			out = append(out, entry)
			continue
		}
		if !entry.Resent && now.Sub(mobileBriefParseTime(entry.HandedAt)) >= mobileBriefOutboxResendAfter {
			entry.Notify = false
			out = append(out, entry)
		}
	}
	if expired {
		o.persistLocked()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out
}
