package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A part the phone told this computer of before its upload was on the relay
// (PROTOCOL.md 24.2a). The phone hands a part to its system and says so in the
// same wake; the upload then goes on with the app asleep, and nobody is awake
// to say when it arrived. So the computer keeps what it was told and fetches
// the part when the relay has it. On 2026-11-19 four parts were whole on the
// relay for five hours because telling the computer needed the phone awake
// once more after each upload.
const (
	// mobileRecordingStateExpected answers a part that is kept here and not yet fetched.
	// It never means the computer has the part: the phone keeps its copy.
	mobileRecordingStateExpected = "expected"
	mobileRecordingExpectedFile  = "expected.json"
	// mobileRecordingExpectedMax bounds what is kept; the oldest give way.
	mobileRecordingExpectedMax = 256
	// mobileRecordingExpectedKeep is a day longer than a relay keeps an upload (48 hours).
	mobileRecordingExpectedKeep  = 72 * time.Hour
	mobileRecordingExpectedEvery = 30 * time.Second
)

// mobileExpectedPart is one request kept as it came, with who sent it.
type mobileExpectedPart struct {
	DeviceID    string                 `json:"deviceId"`
	Request     MobileRecordingRequest `json:"request"`
	AnnouncedAt time.Time              `json:"announcedAt"`
}

// mobileExpectedMu guards expected.json of every store. The file is read anew
// each time, so a store that was emptied forgets these too.
var mobileExpectedMu sync.Mutex

func (s *mobileRecordingStore) expectedPath() string {
	return filepath.Join(s.dir, mobileRecordingExpectedFile)
}

func (s *mobileRecordingStore) readExpectedLocked() []mobileExpectedPart {
	raw, err := os.ReadFile(s.expectedPath())
	if err != nil {
		return nil
	}
	var file struct {
		Parts []mobileExpectedPart `json:"parts"`
	}
	if json.Unmarshal(raw, &file) != nil {
		return nil
	}
	return file.Parts
}

func (s *mobileRecordingStore) writeExpectedLocked(parts []mobileExpectedPart) error {
	if len(parts) == 0 {
		if err := os.Remove(s.expectedPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(map[string]any{"version": 1, "parts": parts}, "", "  ")
	if err != nil {
		return err
	}
	return mobileRecordingWriteFile(s.expectedPath(), raw, 0600)
}

// An announcement can reach disk before the audio. Apply the same grants
// used at final receipt, without holding either grant during the relay fetch.
func (bridge *mobileBridge) expectRecordingPart(store *mobileRecordingStore, deviceID string, req MobileRecordingRequest, now time.Time) error {
	release := bridge.app.filterWatchContext(&req)
	defer release()
	return store.expect(deviceID, req, now)
}

// expect keeps a part's request until the relay has its upload. A part told
// again replaces what was kept for it.
func (s *mobileRecordingStore) expect(deviceID string, req MobileRecordingRequest, now time.Time) error {
	mobileExpectedMu.Lock()
	defer mobileExpectedMu.Unlock()
	kept := make([]mobileExpectedPart, 0, mobileRecordingExpectedMax)
	for _, part := range s.readExpectedLocked() {
		same := part.DeviceID == deviceID && part.Request.RecordingID == req.RecordingID && part.Request.Part == req.Part
		if !same && now.Sub(part.AnnouncedAt) < mobileRecordingExpectedKeep {
			kept = append(kept, part)
		}
	}
	kept = append(kept, mobileExpectedPart{DeviceID: deviceID, Request: req, AnnouncedAt: now.UTC()})
	if over := len(kept) - mobileRecordingExpectedMax; over > 0 {
		kept = kept[over:]
	}
	return s.writeExpectedLocked(kept)
}

func (s *mobileRecordingStore) expected() []mobileExpectedPart {
	mobileExpectedMu.Lock()
	defer mobileExpectedMu.Unlock()
	return s.readExpectedLocked()
}

// forgetExpected drops what was kept for one part.
func (s *mobileRecordingStore) forgetExpected(deviceID, recordingID string, part int) {
	mobileExpectedMu.Lock()
	defer mobileExpectedMu.Unlock()
	all := s.readExpectedLocked()
	kept := all[:0]
	for _, one := range all {
		if one.DeviceID != deviceID || one.Request.RecordingID != recordingID || one.Request.Part != part {
			kept = append(kept, one)
		}
	}
	if len(kept) == len(all) {
		return
	}
	if err := s.writeExpectedLocked(kept); err != nil {
		log.Printf("[recordings] expected parts not saved: %v", err)
	}
}

// startExpectedRecordingsLocked starts the one loop that fetches expected
// parts. The caller holds bridge.mu.
func (bridge *mobileBridge) startExpectedRecordingsLocked() {
	if bridge.expectedRunning || bridge.stopped {
		return
	}
	bridge.expectedRunning = true
	go func() {
		ticker := time.NewTicker(mobileRecordingExpectedEvery)
		defer ticker.Stop()
		for {
			bridge.takeExpectedRecordingParts(time.Now())
			select {
			case <-bridge.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// takeExpectedRecordingParts asks the relay once for every expected part and
// stores those it has. It returns how many it took.
func (bridge *mobileBridge) takeExpectedRecordingParts(now time.Time) int {
	bridge.mu.Lock()
	relays, identity, store := bridge.recordingRelaysLocked(), bridge.identity, bridge.recordingStoreLocked()
	paired := make(map[string]bool, len(bridge.devices))
	for id := range bridge.devices {
		paired[id] = true
	}
	bridge.mu.Unlock()
	if identity == nil || store == nil {
		return 0
	}
	if bridge.app != nil && bridge.app.IsClosedEyesModeEnabled() {
		// Nothing is fetched while observing is paused; the parts wait on the relay.
		return 0
	}
	return bridge.takeExpectedParts(relays, store, identity.HubID, paired, now)
}

func (bridge *mobileBridge) takeExpectedParts(relays mobileRecordingRelays, store *mobileRecordingStore, hubID string, paired map[string]bool, now time.Time) int {
	taken, dropped, waiting := 0, 0, 0
	for _, part := range store.expected() {
		req := part.Request
		if now.Sub(part.AnnouncedAt) >= mobileRecordingExpectedKeep || !paired[part.DeviceID] {
			store.forgetExpected(part.DeviceID, req.RecordingID, req.Part)
			dropped++
			continue
		}
		parent := bridge.ctx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, mobileRecordingFetchWait)
		view, complete, err := relays.receivePart(ctx, store, hubID, part.DeviceID, req)
		cancel()
		switch {
		case err == nil:
			bridge.recordingPartTaken(store, req, view, complete)
			store.forgetExpected(part.DeviceID, req.RecordingID, req.Part)
			taken++
		case errors.Is(err, errMobileRecordingGone):
			// Not on the relay yet: asked again at the next turn.
			waiting++
		case errors.Is(err, errMobileRecordingSHA), errors.Is(err, errMobileAttachmentRejected), errors.Is(err, errMobileAttachmentKey), errors.Is(err, errMobileRecordingInvalid):
			// The upload is there and is not this part. The phone still has the
			// part and sends it again under a new upload.
			log.Printf("[recordings] part %d of %s refused: %v", req.Part, req.RecordingID, err)
			store.forgetExpected(part.DeviceID, req.RecordingID, req.Part)
			dropped++
		default:
			log.Printf("[recordings] part %d of %s not received", req.Part, req.RecordingID)
			waiting++
		}
	}
	// One line for a pass that changed the list, so the log shows it drain. Counts only.
	if taken+dropped > 0 {
		log.Print(mobileExpectedPassLine(taken, dropped, waiting))
	}
	return taken
}

// recordingPartTaken is what follows a stored part, however it was asked for.
// It returns the view with the id the phone sent.
func (bridge *mobileBridge) recordingPartTaken(store *mobileRecordingStore, req MobileRecordingRequest, view MobileRecordingView, complete bool) MobileRecordingView {
	// The row that took the part is the recording itself, or its continuation
	// for a part that came after the recording was closed. The phone reads the
	// id it sent.
	stored := view.ID
	view.ID = req.RecordingID
	if bridge.app != nil {
		// Recorded while observing was paused here: gone before anything reads it.
		bridge.app.dropPausedMobileRecordingOnReceipt(store, stored)
	}
	if complete && bridge.app != nil {
		bridge.app.startMobileRecordingTranscription()
	}
	log.Printf("[recordings] part %d of %s taken: %s", req.Part, req.RecordingID, view.State)
	return view
}

func mobileExpectedPassLine(taken, dropped, waiting int) string {
	return fmt.Sprintf("[recordings] expected parts: took %d, dropped %d, %d still expected", taken, dropped, waiting)
}
