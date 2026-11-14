package main

// The replay window for phone requests (PROTOCOL.md section 4).
//
// A phone has two paths to this computer: its relay and the direct path
// (section 21.4). A request the relay held while the computer was unreachable
// can arrive after a newer request that came over the direct path. A single
// high-water mark dropped that older request as a replay, so the phone waited
// for an answer that never came. The hub now keeps, per phone, the highest
// sequence number it accepted and a bitmap of the 4096 numbers below it, like
// the IPsec and DTLS anti-replay window: a number inside the window that was
// not seen is accepted once, a number seen before or older than the window is
// a replay.
//
// The windows live in replay-windows.json, not devices.json, because
// devices.json is decoded strictly and an older build must still read it. The
// window is written first: after a crash between the two writes the window is
// ahead, never behind, so a request is never served twice. devices.json keeps
// lastAcceptedSeq as the high-water mark older builds use.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
)

const (
	mobileReplayWindowSize = 4096
	mobileReplayWindowFile = "replay-windows.json"
)

// mobileReplayWindow is one phone's window. Bit i of seen says top-i was
// accepted; bit 0 is top itself.
type mobileReplayWindow struct {
	top  uint64
	seen []byte
}

// newMobileReplayWindowAt starts a window whose every number up to top counts
// as seen: the state a single high-water mark at top described.
func newMobileReplayWindowAt(top uint64) *mobileReplayWindow {
	w := &mobileReplayWindow{top: top, seen: make([]byte, mobileReplayWindowSize/8)}
	for i := range w.seen {
		w.seen[i] = 0xff
	}
	return w
}

func (w *mobileReplayWindow) bit(i uint64) bool { return w.seen[i/8]&(1<<(i%8)) != 0 }
func (w *mobileReplayWindow) set(i uint64)      { w.seen[i/8] |= 1 << (i % 8) }

// fresh reports whether seq would be accepted.
func (w *mobileReplayWindow) fresh(seq uint64) bool {
	if seq == 0 {
		return false
	}
	if seq > w.top {
		return true
	}
	behind := w.top - seq
	return behind < mobileReplayWindowSize && !w.bit(behind)
}

// accept records seq and reports whether it was fresh. A replay changes nothing.
func (w *mobileReplayWindow) accept(seq uint64) bool {
	if !w.fresh(seq) {
		return false
	}
	if seq > w.top {
		w.shift(seq - w.top)
		w.top = seq
		w.set(0)
		return true
	}
	w.set(w.top - seq)
	return true
}

// shift moves the window up by n: bit i becomes bit i+n, and the new low bits
// (numbers above the old top) start unseen.
func (w *mobileReplayWindow) shift(n uint64) {
	if n >= mobileReplayWindowSize {
		for i := range w.seen {
			w.seen[i] = 0
		}
		return
	}
	next := make([]byte, len(w.seen))
	for i := uint64(0); i+n < mobileReplayWindowSize; i++ {
		if w.bit(i) {
			next[(i+n)/8] |= 1 << ((i + n) % 8)
		}
	}
	w.seen = next
}

func (w *mobileReplayWindow) clone() *mobileReplayWindow {
	return &mobileReplayWindow{top: w.top, seen: append([]byte(nil), w.seen...)}
}

type mobileReplayWindowEntry struct {
	DeviceID string `json:"deviceId"`
	Top      uint64 `json:"top"`
	Seen     string `json:"seen"`
}

type mobileReplayWindowFileV1 struct {
	Version int                       `json:"version"`
	Windows []mobileReplayWindowEntry `json:"windows"`
}

// loadMobileReplayWindows reads replay-windows.json and reconciles it with the
// device records. A device without a window, or whose record is ahead of its
// window (an older build accepted requests meanwhile), starts a full window at
// its high-water mark, which rejects exactly what the old check rejected.
func loadMobileReplayWindows(dir string, devices map[string]*mobileDeviceRecord) map[string]*mobileReplayWindow {
	saved := map[string]*mobileReplayWindow{}
	raw, err := os.ReadFile(filepath.Join(dir, mobileReplayWindowFile))
	switch {
	case err == nil:
		var file mobileReplayWindowFileV1
		if json.Unmarshal(raw, &file) == nil && file.Version == 1 {
			for _, entry := range file.Windows {
				seen, err := base64.RawURLEncoding.DecodeString(entry.Seen)
				if err != nil || len(seen) != mobileReplayWindowSize/8 {
					continue
				}
				saved[entry.DeviceID] = &mobileReplayWindow{top: entry.Top, seen: seen}
			}
		} else {
			log.Printf("[mobile-bridge] ignored an unreadable replay window file; windows restart at each phone's high-water mark")
		}
	case !errors.Is(err, os.ErrNotExist):
		log.Printf("[mobile-bridge] could not read the replay windows: %v", err)
	}
	windows := make(map[string]*mobileReplayWindow, len(devices))
	for id, record := range devices {
		if w := saved[id]; w != nil && w.top >= record.LastAcceptedSeq {
			windows[id] = w
			continue
		}
		windows[id] = newMobileReplayWindowAt(record.LastAcceptedSeq)
	}
	return windows
}

// persistReplayWindowsLocked writes the windows of the paired phones. The
// caller holds bridge.mu.
func (bridge *mobileBridge) persistReplayWindowsLocked(windows map[string]*mobileReplayWindow) error {
	file := mobileReplayWindowFileV1{Version: 1, Windows: make([]mobileReplayWindowEntry, 0, len(windows))}
	for id, w := range windows {
		if bridge.devices[id] == nil {
			continue
		}
		file.Windows = append(file.Windows, mobileReplayWindowEntry{DeviceID: id, Top: w.top, Seen: base64.RawURLEncoding.EncodeToString(w.seen)})
	}
	sort.Slice(file.Windows, func(i, j int) bool { return file.Windows[i].DeviceID < file.Windows[j].DeviceID })
	if err := ensureMobileStateDirectory(bridge.stateDir); err != nil {
		return err
	}
	return writeMobilePrivateJSON(filepath.Join(bridge.stateDir, mobileReplayWindowFile), file)
}

// replayWindowLocked is the device's window, starting one at its high-water
// mark when the device was paired after load. The caller holds bridge.mu.
func (bridge *mobileBridge) replayWindowLocked(record *mobileDeviceRecord) *mobileReplayWindow {
	if bridge.replayWindows == nil {
		bridge.replayWindows = map[string]*mobileReplayWindow{}
	}
	w := bridge.replayWindows[record.DeviceID]
	if w == nil || w.top < record.LastAcceptedSeq {
		w = newMobileReplayWindowAt(record.LastAcceptedSeq)
		bridge.replayWindows[record.DeviceID] = w
	}
	return w
}
