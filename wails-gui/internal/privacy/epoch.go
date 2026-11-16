package privacy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Saved file names, kept beside the briefs.
const (
	EpochFile = "privacy-epoch"
	LogFile   = "withdrawals.json"
	// LogLimit is how many recent changes the withdrawal log keeps.
	LogLimit = 500
)

// WithdrawnKey names one source a privacy action withdrew, by the kind and id
// the brief cites it with. An empty kind matches any kind with that id.
type WithdrawnKey struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id"`
	// Text is the wording a correction leaves in place of a withdrawn belief
	// (empty drops the belief). Only the belief correction sets it.
	Text string `json:"text,omitempty"`
}

// LogEntry is one privacy epoch change. Keys is empty when the change did not
// name its sources (an app, a pause, an account or a session): a reader then
// cannot tell what to remove and must check everything again.
type LogEntry struct {
	Epoch uint64         `json:"epoch"`
	Keys  []WithdrawnKey `json:"keys,omitempty"`
}

// Log holds the recent privacy changes. Floor is the epoch the log starts
// after: a copy made at or after Floor can be amended from the entries alone;
// an older copy cannot.
type Log struct {
	Floor   uint64     `json:"floor"`
	Entries []LogEntry `json:"entries"`
}

// WriteFile saves one file atomically in dir.
type WriteFile func(dir, name string, data []byte) error

// Epoch counts privacy changes and keeps the log of what each one withdrew.
// A saved copy is valid while the epoch it was made under is still current.
// The epoch and its log are saved in one directory once it is adopted, so a
// restart does not make every copy stale. The in-memory epoch never goes back.
type Epoch struct {
	value atomic.Uint64
	// mu orders the in-memory epoch and log with their saved copies.
	mu       sync.Mutex
	savedDir string
	log      Log
	write    WriteFile
}

// NewEpoch returns an epoch at zero that saves its files with write.
func NewEpoch(write WriteFile) *Epoch {
	return &Epoch{write: write}
}

// Load returns the current epoch.
func (e *Epoch) Load() uint64 { return e.value.Load() }

// Store sets the epoch, for tests that start from a known value.
func (e *Epoch) Store(value uint64) { e.value.Store(value) }

// Advance records a privacy change that withdrew keys and returns the new
// epoch. No keys means the change named no sources. The log is saved before
// the epoch, so a saved epoch always has its entry.
func (e *Epoch) Advance(keys ...WithdrawnKey) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	value := e.value.Add(1)
	e.log.Entries = append(e.log.Entries, LogEntry{Epoch: value, Keys: keys})
	if over := len(e.log.Entries) - LogLimit; over > 0 {
		e.log.Floor = e.log.Entries[over-1].Epoch
		e.log.Entries = append([]LogEntry(nil), e.log.Entries[over:]...)
	}
	if dir := e.savedDir; dir != "" && e.write != nil {
		if data, err := json.Marshal(e.log); err == nil {
			_ = e.write(dir, LogFile, data)
		}
		_ = e.write(dir, EpochFile, []byte(strconv.FormatUint(value, 10)))
	}
	return value
}

// Adopt loads the saved epoch and log from dir the first time it is opened,
// and saves later changes there.
func (e *Epoch) Adopt(dir string) {
	if dir == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.savedDir == dir {
		return
	}
	e.savedDir = dir
	if data, err := os.ReadFile(filepath.Join(dir, EpochFile)); err == nil {
		if saved, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
			for {
				current := e.value.Load()
				if saved <= current || e.value.CompareAndSwap(current, saved) {
					break
				}
			}
		}
	}
	// The log is loaded with the epoch. Without a log (an older build), it starts
	// at the current epoch, so every older copy is checked again in full.
	e.log = Log{Floor: e.value.Load()}
	if data, err := os.ReadFile(filepath.Join(dir, LogFile)); err == nil {
		var log Log
		if json.Unmarshal(data, &log) == nil {
			e.log = log
		}
	}
	// An epoch newer than the log's last entry means a change was saved without
	// its entry; the log cannot answer for it.
	last := e.log.Floor
	if n := len(e.log.Entries); n > 0 {
		last = e.log.Entries[n-1].Epoch
	}
	if e.value.Load() > last {
		e.log = Log{Floor: e.value.Load()}
	}
}

// Adopted reports whether a saved epoch has been loaded, with the epoch read
// under the same lock.
func (e *Epoch) Adopted() (bool, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.savedDir != "", e.value.Load()
}

// WithdrawnSince returns the sources withdrawn after epoch. It reports false
// when the log cannot answer: the copy is older than the log, or a change in
// between named no sources.
func (e *Epoch) WithdrawnSince(epoch uint64) ([]WithdrawnKey, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if epoch < e.log.Floor {
		return nil, false
	}
	var keys []WithdrawnKey
	for _, entry := range e.log.Entries {
		if entry.Epoch <= epoch {
			continue
		}
		if len(entry.Keys) == 0 {
			return nil, false
		}
		keys = append(keys, entry.Keys...)
	}
	return keys, true
}

// AllKeys returns every keyed withdrawal in the log. The log keeps the last
// changes, so a withdrawal older than its floor is not listed.
func (e *Epoch) AllKeys() []WithdrawnKey {
	e.mu.Lock()
	defer e.mu.Unlock()
	var keys []WithdrawnKey
	for _, entry := range e.log.Entries {
		keys = append(keys, entry.Keys...)
	}
	return keys
}

// SwapSavedDir replaces the adopted directory and returns the previous one,
// for tests ("" means nothing is adopted and nothing is saved).
func (e *Epoch) SwapSavedDir(dir string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	prev := e.savedDir
	e.savedDir = dir
	return prev
}

// SwapLog replaces the log and returns the previous one, for tests.
func (e *Epoch) SwapLog(log Log) Log {
	e.mu.Lock()
	defer e.mu.Unlock()
	prev := e.log
	e.log = log
	return prev
}
