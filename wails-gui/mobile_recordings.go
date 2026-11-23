package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"wails-gui/internal/asr"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/cmdutil"
	"wails-gui/internal/privacy"
	"wails-gui/storage"
)

// Recordings from the watch and the phone (PROTOCOL.md section 24). The phone
// seals each recording part with a random key, puts the ciphertext on the
// relay (PUT /v1/uploads/{id}), and sends the key here in the sealed request
// POST /recordings. The computer fetches the part with its hub credential,
// opens and checks it, keeps it under the mobile state folder, deletes it from
// the relay, and transcribes it on this computer with the after-call speech
// engine (Qwen3-ASR when installed, else the live engine). The transcript is
// saved as an activity session of speech, so memory and the brief read it
// through the same privacy gates as speech heard on the computer. Audio never
// leaves the user's devices in the clear and is never sent to a model.

const (
	mobileRecordingMaxParts     = 64
	mobileRecordingMaxPartBytes = 16<<20 - chacha20poly1305.Overhead
	// mobileRecordingMaxDuration bounds one part's stated length.
	mobileRecordingMaxDuration = 4 * 60 * 60
	mobileRecordingFetchWait   = 2 * time.Minute
	mobileRecordingListMax     = 50
	// mobileRecordingAppName and mobileRecordingBundleID label the speech
	// events, so Activity and memory say where the words were heard.
	mobileRecordingAppName  = "Isoform on your watch"
	mobileRecordingBundleID = "com.isoform.companion.watchkitapp"
	mobileRecordingPhoneApp = "Isoform on your phone"
	mobileRecordingPhoneID  = "com.isoform.companion"
	mobileRecordingTrigger  = "watch_recording"
)

// Recording states the phone shows (24.3).
const (
	mobileRecordingReceived     = "received"
	mobileRecordingTranscribing = "transcribing"
	mobileRecordingTranscribed  = "transcribed"
	mobileRecordingFailed       = "failed"
	// mobileRecordingDeleted: the user deleted the recording (24.5). The row
	// stays so a late part is never imported again; nothing else is kept.
	mobileRecordingDeleted = "deleted"
)

var (
	errMobileRecordingInvalid = errors.New("the recording is not valid")
	errMobileRecordingSHA     = errors.New("the recording does not match its checksum")
	errMobileRecordingGone    = errors.New("the recording is no longer on the relay")
)

// mobileUploadAAD binds a sealed upload to its hub, its device and its id.
func mobileUploadAAD(hubID, deviceID, uploadID string) []byte {
	return []byte("yansu-upload-v1|" + hubID + "|" + deviceID + "|" + uploadID)
}

// openMobileUpload authenticates and decrypts a sealed upload.
func openMobileUpload(key, nonce []byte, hubID, deviceID, uploadID string, sealed []byte) ([]byte, error) {
	if len(key) != chacha20poly1305.KeySize || len(nonce) != chacha20poly1305.NonceSize {
		return nil, errMobileAttachmentKey
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, sealed, mobileUploadAAD(hubID, deviceID, uploadID))
	if err != nil {
		return nil, errMobileAttachmentRejected
	}
	return plaintext, nil
}

// sealMobileUpload is the phone's sealing, kept here so tests and vectors can
// check the computer's opener against it.
func sealMobileUpload(key, nonce []byte, hubID, deviceID, uploadID string, plaintext []byte) ([]byte, error) {
	if len(key) != chacha20poly1305.KeySize || len(nonce) != chacha20poly1305.NonceSize {
		return nil, errMobileAttachmentKey
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plaintext, mobileUploadAAD(hubID, deviceID, uploadID)), nil
}

// mobileUploadFetcher reads and removes a phone's upload on the relay. The
// relay client implements it; tests use a map.
type mobileUploadFetcher interface {
	downloadUpload(ctx context.Context, uploadID string) ([]byte, error)
	deleteUpload(ctx context.Context, uploadID string) error
}

// MobileRecordingRequest is the body of POST /recordings: one part of a
// recording, sealed on the relay.
type MobileRecordingRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	RecordingID    string `json:"recordingId"`
	Part           int    `json:"part"`
	// Last marks the final part. A recording still running does not know its
	// part count, so each finished part is sent as it is cut.
	Last            bool                      `json:"last"`
	Upload          mobileRecordingPartUpload `json:"upload"`
	SHA256          string                    `json:"sha256"`
	Bytes           int64                     `json:"bytes"`
	Mime            string                    `json:"mime"`
	StartedAt       string                    `json:"startedAt"`
	DurationSeconds float64                   `json:"durationSeconds"`
	Source          string                    `json:"source"`
	Title           string                    `json:"title,omitempty"`
	Device          *MobileRecordingContext   `json:"device,omitempty"`
	// Context is the wearer's heart rate and coarse location during this part
	// (24.4). Optional; sent only to a computer that advertises it.
	Context *MobileRecordingPartContext `json:"context,omitempty"`
	// Marks are the moments the wearer marked during this part, in seconds
	// from its start (24.9). Optional; sent only to a computer that advertises it.
	Marks []float64 `json:"marks,omitempty"`
	// Ahead is true when the phone says this before the upload is on the relay
	// (24.2a): the computer keeps the request and fetches when the relay has it.
	// Sent only to a computer that advertises it.
	Ahead bool `json:"ahead,omitempty"`
}

// MobileRecordingUpload names the sealed part on the relay and its key.
type MobileRecordingUpload struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Nonce string `json:"nonce"`
}

type mobileRecordingPartUpload struct {
	MobileRecordingUpload
	Relay string `json:"relay,omitempty"`
}

const (
	mobileRecordingRelayPinned = "pinned"
	mobileRecordingRelayDirect = "direct"
)

// MobileRecordingContext is optional, coarse context the watch knew. Nothing
// here is required, and none of it is location.
type MobileRecordingContext struct {
	Model string `json:"model,omitempty"`
}

// mobileRecordingPart is one stored part.
type mobileRecordingPart struct {
	Part            int     `json:"part"`
	UploadID        string  `json:"uploadId"`
	UploadRelay     string  `json:"uploadRelay,omitempty"`
	File            string  `json:"file"`
	Bytes           int64   `json:"bytes"`
	SHA256          string  `json:"sha256"`
	StartedAt       string  `json:"startedAt"`
	DurationSeconds float64 `json:"durationSeconds"`
	// Context is removed together with the audio.
	Context *MobileRecordingPartContext `json:"context,omitempty"`
	// Paused is set when observing was paused for some or all of the part:
	// its audio was deleted and nothing from the pause was transcribed
	// (mobile_recording_pauses.go).
	Paused bool `json:"paused,omitempty"`
	// PausedCut is set when a pause touched the part and the paused minutes
	// were cut out of its audio: the file is shorter than DurationSeconds and
	// its times no longer line up with the clock.
	PausedCut        bool                `json:"pausedCut,omitempty"`
	ConversationCuts []watchPartInterval `json:"conversationCuts,omitempty"`
	// Marks are the moments the wearer marked, seconds from the part's start.
	// Removed together with the audio.
	Marks []float64 `json:"marks,omitempty"`
}

// mobileRecording is one recording in the index.
type mobileRecording struct {
	ID        string                `json:"id"`
	DeviceID  string                `json:"deviceId"`
	Source    string                `json:"source"`
	Title     string                `json:"title,omitempty"`
	Mime      string                `json:"mime"`
	Parts     int                   `json:"parts"`
	Received  []mobileRecordingPart `json:"received"`
	State     string                `json:"state"`
	Error     string                `json:"error,omitempty"`
	SessionID string                `json:"sessionId,omitempty"`
	Words     int                   `json:"words"`
	// OwnerVoice says how the transcript's speech was labelled by speaker.
	OwnerVoice string `json:"ownerVoice,omitempty"`
	// AudioRemoved is set once the audio was deleted after its keep time.
	AudioRemoved       bool   `json:"audioRemoved,omitempty"`
	ReceivedAt         string `json:"receivedAt"`
	LastPartReceivedAt string `json:"lastPartReceivedAt,omitempty"`
	UpdatedAt          string `json:"updatedAt"`
	// ContinuationOf and FirstPart are set on a row that holds the parts that
	// arrived after the recording they belong to was closed: the recording's
	// id as the phone knows it, and the number its part 0 has there
	// (mobile_recording_late_parts.go).
	ContinuationOf string `json:"continuationOf,omitempty"`
	FirstPart      int    `json:"firstPart,omitempty"`
}

// MobileRecordingView is what the phone reads about a recording.
type MobileRecordingView struct {
	ID              string  `json:"id"`
	State           string  `json:"state"`
	PartsReceived   int     `json:"partsReceived"`
	Parts           int     `json:"parts"`
	DurationSeconds float64 `json:"durationSeconds"`
	StartedAt       string  `json:"startedAt"`
	Words           int     `json:"words"`
	// OwnerVoice is "labelled", "not_enrolled" or "unavailable" once the
	// recording is transcribed and something was said (24.3).
	OwnerVoice string `json:"ownerVoice,omitempty"`
	// Conversations are the short names of the conversations heard in this
	// recording, in order (24.7); absent until the computer has named any.
	Conversations []string `json:"conversations,omitempty"`
	ReceivedAt    string   `json:"receivedAt"`
	UpdatedAt     string   `json:"updatedAt"`
}

// complete reports whether the last part and every part before it are stored.
// Parts is 0 until the last part names the count.
func (r *mobileRecording) complete() bool { return r.Parts > 0 && len(r.Received) == r.Parts }

func (r *mobileRecording) view() MobileRecordingView {
	view := MobileRecordingView{ID: r.ID, State: r.State, PartsReceived: len(r.Received), Parts: r.Parts,
		Words: r.Words, OwnerVoice: r.OwnerVoice, ReceivedAt: r.ReceivedAt, UpdatedAt: r.UpdatedAt}
	for i, part := range r.Received {
		view.DurationSeconds += part.DurationSeconds
		if i == 0 || part.StartedAt < view.StartedAt {
			view.StartedAt = part.StartedAt
		}
	}
	return view
}

// mobileRecordingStore keeps the recordings index (recordings/index.json,
// 0600) and the part files beside it (0600, folder 0700).
type mobileRecordingStore struct {
	dir string
	now func() time.Time

	mu             sync.Mutex
	recordings     map[string]*mobileRecording
	loaded         bool
	cutUses        *storage.Store
	cutDB          *sql.DB
	forgetPreviews map[string][32]byte // guarded by watchCutMu

	// The devices paired now, as the bridge last said (setPaired). Its own
	// lock, so the bridge can set it while it holds its own.
	pairedMu    sync.Mutex
	paired      map[string]bool
	pairedKnown bool
}

// setPaired tells the store which devices are paired now.
func (s *mobileRecordingStore) setPaired(ids []string) {
	paired := make(map[string]bool, len(ids))
	for _, id := range ids {
		paired[id] = true
	}
	s.pairedMu.Lock()
	s.paired, s.pairedKnown = paired, true
	s.pairedMu.Unlock()
}

// claimLocked reports whether deviceID may read and add to rec. A recording
// belongs to the phone that sent it. A phone that pairs again gets a new
// device id, and on 2026-11-18 that made the computer refuse every later part
// of the recordings that phone had started ("the recording is not valid"). So a
// recording whose device is no longer paired passes to the paired phone that
// names it: the id is a random UUID, and only the phone that made the
// recording holds it. The recording's continuations pass with it. The caller
// saves the index.
func (s *mobileRecordingStore) claimLocked(rec *mobileRecording, deviceID string) bool {
	if rec.DeviceID == deviceID {
		return true
	}
	s.pairedMu.Lock()
	passes := s.pairedKnown && s.paired[deviceID] && !s.paired[rec.DeviceID]
	s.pairedMu.Unlock()
	if !passes {
		return false
	}
	from := rec.DeviceID
	root := rec.ID
	if rec.ContinuationOf != "" {
		root = rec.ContinuationOf
	}
	for _, other := range s.recordings {
		if other.DeviceID == from && (other.ID == root || other.ContinuationOf == root) {
			other.DeviceID = deviceID
		}
	}
	rec.DeviceID = deviceID
	log.Printf("[recordings] %s passes to the phone's new pairing", rec.ID)
	return true
}

func newMobileRecordingStore(stateDir string) *mobileRecordingStore {
	return &mobileRecordingStore{dir: filepath.Join(stateDir, "recordings"), now: time.Now}
}

func (s *mobileRecordingStore) loadLocked() error {
	if s.loaded {
		return nil
	}
	if err := s.recoverWatchCutLocked(); err != nil {
		return err
	}
	s.recordings = map[string]*mobileRecording{}
	raw, err := os.ReadFile(filepath.Join(s.dir, "index.json"))
	if errors.Is(err, os.ErrNotExist) {
		s.loaded = true
		return nil
	}
	if err != nil {
		return err
	}
	var file struct {
		Version    int                `json:"version"`
		Recordings []*mobileRecording `json:"recordings"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("recordings index: %w", err)
	}
	for _, rec := range file.Recordings {
		if rec != nil && mobileValidUUID(rec.ID) {
			s.recordings[rec.ID] = rec
		}
	}
	s.loaded = true
	return nil
}

func (s *mobileRecordingStore) saveLocked() error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	list := make([]*mobileRecording, 0, len(s.recordings))
	for _, rec := range s.recordings {
		list = append(list, rec)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ReceivedAt < list[j].ReceivedAt })
	raw, err := json.MarshalIndent(map[string]any{"version": 1, "recordings": list}, "", "  ")
	if err != nil {
		return err
	}
	return mobileRecordingWriteFile(filepath.Join(s.dir, "index.json"), raw, 0600)
}

// storedPart returns the recorded upload for a repeat. A deleted recording
// returns its view alone, so a late part cannot bring it back.
func (s *mobileRecordingStore) storedPart(deviceID, recordingID string, part int) (MobileRecordingView, *mobileRecordingPart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return MobileRecordingView{}, nil, err
	}
	rec := s.recordings[recordingID]
	if rec == nil {
		return MobileRecordingView{}, nil, nil
	}
	if rec.DeviceID != deviceID {
		if !s.claimLocked(rec, deviceID) {
			return MobileRecordingView{}, nil, nil
		}
		if err := s.saveLocked(); err != nil {
			s.loaded = false
			return MobileRecordingView{}, nil, err
		}
	}
	if rec.State == mobileRecordingDeleted {
		// A part of a deleted recording is never fetched or stored again.
		return rec.view(), nil, nil
	}
	// A part past the end of a closed recording lives in its continuation.
	rec, part = s.rowForPartLocked(rec, part, false, "")
	if rec == nil {
		return MobileRecordingView{}, nil, nil
	}
	if rec.State == mobileRecordingDeleted {
		return rec.view(), nil, nil
	}
	for _, got := range rec.Received {
		if got.Part == part {
			return rec.view(), &got, nil
		}
	}
	return MobileRecordingView{}, nil, nil
}

// addPart writes a part's plaintext and records it. It reports whether the
// recording is now complete.
func (s *mobileRecordingStore) addPart(deviceID string, req MobileRecordingRequest, plaintext []byte) (MobileRecordingView, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return MobileRecordingView{}, false, err
	}
	now := mobileTimestamp(s.now())
	rec := s.recordings[req.RecordingID]
	if rec != nil && !s.claimLocked(rec, deviceID) {
		return MobileRecordingView{}, false, errMobileRecordingInvalid
	}
	if rec != nil && rec.State != mobileRecordingDeleted {
		// A part past the end of a recording that was closed without it (the
		// phone could not send for hours) goes to the recording's
		// continuation, under its number there.
		rec, req.Part = s.rowForPartLocked(rec, req.Part, true, now)
		req.RecordingID = rec.ID
	}
	if rec == nil {
		rec = &mobileRecording{ID: req.RecordingID, DeviceID: deviceID, Source: req.Source, Title: req.Title,
			Mime: req.Mime, State: mobileRecordingReceived, ReceivedAt: now}
	}
	for _, got := range rec.Received {
		if got.Part == req.Part {
			if got.SHA256 != req.SHA256 || got.Bytes != req.Bytes {
				return MobileRecordingView{}, false, errMobileRecordingSHA
			}
			return rec.view(), rec.complete(), nil
		}
	}
	// The last part fixes the count; no part may sit at or past it, and a
	// second, different last part is refused.
	parts := rec.Parts
	if req.Last {
		if parts > 0 && parts != req.Part+1 {
			return MobileRecordingView{}, false, errMobileRecordingInvalid
		}
		parts = req.Part + 1
	}
	if parts > 0 && req.Part >= parts {
		return MobileRecordingView{}, false, errMobileRecordingInvalid
	}
	for _, got := range rec.Received {
		if parts > 0 && got.Part >= parts {
			return MobileRecordingView{}, false, errMobileRecordingInvalid
		}
	}
	if rec.State != mobileRecordingReceived {
		// Already transcribing or done: a late part cannot change it.
		return MobileRecordingView{}, false, errMobileRecordingInvalid
	}
	folder := filepath.Join(s.dir, rec.ID)
	if err := os.MkdirAll(folder, 0700); err != nil {
		return MobileRecordingView{}, false, err
	}
	name := fmt.Sprintf("part-%02d%s", req.Part, mobileRecordingExtension(req.Mime))
	if err := mobileRecordingWriteFile(filepath.Join(folder, name), plaintext, 0600); err != nil {
		return MobileRecordingView{}, false, err
	}
	rec.Parts = parts
	s.recordings[rec.ID] = rec
	rec.Received = append(rec.Received, mobileRecordingPart{Part: req.Part, UploadID: req.Upload.ID, UploadRelay: req.Upload.Relay, File: name,
		Bytes: int64(len(plaintext)), SHA256: req.SHA256, StartedAt: req.StartedAt, DurationSeconds: req.DurationSeconds,
		Context: sanitizeMobileRecordingContext(req.Context),
		Marks:   sanitizeMobileRecordingMarks(req.Marks, req.DurationSeconds)})
	sort.Slice(rec.Received, func(i, j int) bool { return rec.Received[i].Part < rec.Received[j].Part })
	rec.LastPartReceivedAt = now
	rec.UpdatedAt = now
	if err := s.saveLocked(); err != nil {
		// A retry must read the committed index before acknowledging the part.
		s.loaded = false
		return MobileRecordingView{}, false, err
	}
	return rec.view(), rec.complete(), nil
}

// setState returns errWatchRecordingNotFound only when the index confirms
// the recording is missing or its audio has been removed.
func (s *mobileRecordingStore) setState(id, state, sessionID string, words int, failure string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		log.Printf("[recordings] index read failed: %v", err)
		return err
	}
	rec := s.recordings[id]
	if rec == nil || rec.AudioRemoved || rec.State == mobileRecordingDeleted {
		return errWatchRecordingNotFound
	}
	rec.State, rec.Error, rec.UpdatedAt = state, failure, mobileTimestamp(s.now())
	if sessionID != "" {
		rec.SessionID = sessionID
	}
	if words > 0 {
		rec.Words = words
	}
	if err := s.saveLocked(); err != nil {
		log.Printf("[recordings] index save failed: %v", err)
		return err
	}
	return nil
}

// setOwnerVoice records how a recording's speech was labelled by speaker.
func (s *mobileRecordingStore) setOwnerVoice(id, ownerVoice string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return
	}
	if rec := s.recordings[id]; rec != nil && rec.OwnerVoice != ownerVoice {
		rec.OwnerVoice = ownerVoice
		if err := s.saveLocked(); err != nil {
			log.Printf("[recordings] index save failed: %v", err)
		}
	}
}

// pending lists complete recordings not yet transcribed, oldest first. A
// recording left "transcribing" by a quit is pending again.
func (s *mobileRecordingStore) pending() []mobileRecording {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		log.Printf("[recordings] index read failed: %v", err)
		return nil
	}
	var out []mobileRecording
	changed := false
	for _, rec := range s.recordings {
		if rec.closeIfAbandoned(s.now()) {
			changed = true
		}
		if !rec.AudioRemoved && rec.complete() && (rec.State == mobileRecordingReceived || rec.State == mobileRecordingTranscribing) {
			copied := *rec
			copied.Received = append([]mobileRecordingPart(nil), rec.Received...)
			out = append(out, copied)
		}
	}
	if changed {
		if err := s.saveLocked(); err != nil {
			log.Printf("[recordings] index save failed: %v", err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt < out[j].ReceivedAt })
	return out
}

// mobileRecordingAbandonedAfter is how long the computer waits for a last
// part that never comes (the watch ran out of battery mid-recording).
const mobileRecordingAbandonedAfter = 6 * time.Hour

// closeIfAbandoned completes a recording whose last part never arrived: when
// its parts run from 0 without a gap and nothing came for
// mobileRecordingAbandonedAfter, what was received is the recording.
func (r *mobileRecording) closeIfAbandoned(now time.Time) bool {
	if r.Parts > 0 || len(r.Received) == 0 || r.State != mobileRecordingReceived {
		return false
	}
	updated, err := time.Parse(time.RFC3339, r.UpdatedAt)
	if err != nil || now.Sub(updated) < mobileRecordingAbandonedAfter {
		return false
	}
	for i, part := range r.Received {
		if part.Part != i {
			return false
		}
	}
	r.Parts = len(r.Received)
	return true
}

// list returns one device's recordings, newest first, or the named ones.
// Deleted recordings are listed only for a phone that asked for them (24.5).
func (s *mobileRecordingStore) list(deviceID string, ids []string, limit int, withDeleted bool) ([]MobileRecordingView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	var out []MobileRecordingView
	claimed := false
	for _, rec := range s.recordings {
		if len(wanted) > 0 && !wanted[rec.ID] {
			continue
		}
		if rec.DeviceID != deviceID {
			// Only a recording the phone names can pass to its new pairing.
			if len(wanted) == 0 || !s.claimLocked(rec, deviceID) {
				continue
			}
			claimed = true
		}
		if rec.State == mobileRecordingDeleted && !withDeleted {
			continue
		}
		out = append(out, rec.view())
	}
	if claimed {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt > out[j].ReceivedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// mobileRecordingAudioKeep is how long a transcribed recording's audio stays
// on the computer. The transcript in Activity is kept by Activity's own rules.
const mobileRecordingAudioKeep = 14 * 24 * time.Hour

var mobileRecordingLaunchOnce sync.Once

// retryFailedAndPrune makes failed recordings pending again and deletes the
// audio of recordings transcribed more than mobileRecordingAudioKeep ago. The
// index row stays, so the phone still reads the recording's state.
func (s *mobileRecordingStore) retryFailedAndPrune() {
	s.retryFailed(true)
}

func (s *mobileRecordingStore) retryFailed(launch bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return
	}
	changed := false
	for _, rec := range s.recordings {
		switch rec.State {
		case mobileRecordingFailed:
			if rec.AudioRemoved || (!launch && rec.Error != mobileSpeechNotReady) {
				continue
			}
			rec.State, rec.Error = mobileRecordingReceived, ""
			changed = true
		case mobileRecordingTranscribed:
			if !launch {
				continue
			}
			updated, err := time.Parse(time.RFC3339, rec.UpdatedAt)
			if err != nil || s.now().Sub(updated) < mobileRecordingAudioKeep || rec.AudioRemoved {
				continue
			}
			if err := os.RemoveAll(filepath.Join(s.dir, rec.ID)); err != nil {
				continue
			}
			rec.AudioRemoved = true
			rec.dropContext()
			changed = true
		}
	}
	if changed {
		if err := s.saveLocked(); err != nil {
			log.Printf("[recordings] index save failed: %v", err)
		}
	}
}

// forgetAudioOfSession deletes the audio of the recording whose transcript is
// the activity session sessionID ("rec-<recordingId>"). A transcript the user
// deletes, or that the activity retention removes, takes its audio with it:
// the audio is the rawer copy of the same words and must never outlive them.
// The index row stays, so a repeat of a part is still answered from it and the
// recording is never imported again.
func (s *mobileRecordingStore) forgetAudioOfSession(sessionID string) {
	id, ok := strings.CutPrefix(sessionID, "rec-")
	if !ok || !mobileValidUUID(id) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return
	}
	rec := s.recordings[id]
	if rec == nil || rec.AudioRemoved {
		return
	}
	if err := os.RemoveAll(filepath.Join(s.dir, rec.ID)); err != nil {
		log.Printf("[recordings] audio of %s not removed: %v", rec.ID, err)
		return
	}
	rec.AudioRemoved = true
	rec.dropContext()
	if err := s.saveLocked(); err != nil {
		log.Printf("[recordings] index save failed: %v", err)
	}
}

// onPrivacyWithdrawal follows the withdrawal and retention of transcript
// sessions (privacy_withdrawal.go).
func (s *mobileRecordingStore) onPrivacyWithdrawal(withdrawal privacy.Withdrawal) {
	if withdrawal.Scope.Kind == privacy.Session {
		s.forgetAudioOfSession(withdrawal.Scope.ID)
		// A name made from the withdrawn words goes too.
		if id, ok := strings.CutPrefix(withdrawal.Scope.ID, "rec-"); ok && mobileValidUUID(id) {
			dropWatchConversationNamesOf(s.dir, id)
			dropWatchSpeakerNamesOf(s.dir, id)
		}
	}
}

// partPaths are a recording's files in order.
func (s *mobileRecordingStore) partPaths(rec mobileRecording) []string {
	paths := make([]string, 0, len(rec.Received))
	for _, part := range rec.Received {
		paths = append(paths, filepath.Join(s.dir, rec.ID, part.File))
	}
	return paths
}

func mobileRecordingExtension(mime string) string {
	switch mime {
	case "audio/wav":
		return ".wav"
	default:
		return ".m4a"
	}
}

// validateMobileRecordingRequest checks every field before anything is fetched.
func validateMobileRecordingRequest(req MobileRecordingRequest, now time.Time) error {
	switch {
	case !mobileValidUUID(req.RecordingID), !mobileValidUUID(req.Upload.ID):
		return errMobileRecordingInvalid
	case req.Part < 0 || req.Part >= mobileRecordingMaxParts:
		return errMobileRecordingInvalid
	case req.Bytes < 1 || req.Bytes > mobileRecordingMaxPartBytes:
		return errMobileRecordingInvalid
	case req.DurationSeconds <= 0 || req.DurationSeconds > mobileRecordingMaxDuration:
		return errMobileRecordingInvalid
	case len(req.Title) > 200:
		return errMobileRecordingInvalid
	}
	if sum, err := hex.DecodeString(req.SHA256); err != nil || len(sum) != sha256.Size || strings.ToLower(req.SHA256) != req.SHA256 {
		return errMobileRecordingInvalid
	}
	switch req.Mime {
	case "audio/mp4", "audio/wav":
	default:
		return errMobileRecordingInvalid
	}
	switch req.Upload.Relay {
	case "", mobileRecordingRelayPinned, mobileRecordingRelayDirect:
	default:
		return errMobileRecordingInvalid
	}
	switch req.Source {
	case "watch", "phone":
	default:
		return errMobileRecordingInvalid
	}
	started, err := time.Parse(time.RFC3339, req.StartedAt)
	if err != nil || started.After(now.Add(5*time.Minute)) || started.Before(now.Add(-30*24*time.Hour)) {
		return errMobileRecordingInvalid
	}
	return nil
}

// The primary client serves the pinned relay or, on a ticket-only computer,
// the direct relay itself. A pinned computer can also have a direct client.
type mobileRecordingRelays struct {
	app         *App
	primaryName string
	primary     mobileUploadFetcher
	direct      mobileUploadFetcher
}

func (bridge *mobileBridge) recordingRelaysLocked() mobileRecordingRelays {
	relays := mobileRecordingRelays{app: bridge.app, primaryName: mobileRecordingRelayPinned}
	if mobileRemoteAccessMode(bridge.relay.URL) {
		relays.primaryName = mobileRecordingRelayDirect
	}
	if bridge.client != nil {
		relays.primary = bridge.client
	}
	if direct := bridge.direct.Load(); direct != nil {
		relays.direct = direct
	}
	return relays
}

func (relays mobileRecordingRelays) client(name string) mobileUploadFetcher {
	switch name {
	case relays.primaryName:
		return relays.primary
	case mobileRecordingRelayDirect:
		return relays.direct
	default:
		return nil
	}
}

func (relays mobileRecordingRelays) receivePart(ctx context.Context, store *mobileRecordingStore, hubID, deviceID string, req MobileRecordingRequest) (MobileRecordingView, bool, error) {
	view, stored, err := store.storedPart(deviceID, req.RecordingID, req.Part)
	if err != nil || view.State == mobileRecordingDeleted {
		// A tombstone acknowledges deletion without touching the upload.
		return view, false, err
	}
	if stored != nil {
		if stored.SHA256 != req.SHA256 || stored.Bytes != req.Bytes {
			return MobileRecordingView{}, false, errMobileRecordingSHA
		}
		// Retry cleanup only for the upload we actually opened and stored.
		client := relays.client(stored.UploadRelay)
		if client != nil && stored.UploadID == req.Upload.ID && (req.Upload.Relay == "" || req.Upload.Relay == stored.UploadRelay) {
			_ = client.deleteUpload(ctx, stored.UploadID)
		}
		return view, false, nil
	}
	key, keyErr := base64.RawURLEncoding.DecodeString(req.Upload.Key)
	nonce, nonceErr := base64.RawURLEncoding.DecodeString(req.Upload.Nonce)
	if keyErr != nil || nonceErr != nil {
		return MobileRecordingView{}, false, errMobileAttachmentKey
	}
	unnamed := req.Upload.Relay == ""
	if unnamed {
		req.Upload.Relay = relays.primaryName
	}
	client := relays.client(req.Upload.Relay)
	if client == nil {
		return MobileRecordingView{}, false, errMobileStoreUnavailable
	}
	sealed, err := client.downloadUpload(ctx, req.Upload.ID)
	if unnamed && errors.Is(err, errMobileRecordingGone) && relays.direct != nil && req.Upload.Relay == mobileRecordingRelayPinned {
		req.Upload.Relay, client = mobileRecordingRelayDirect, relays.direct
		sealed, err = client.downloadUpload(ctx, req.Upload.ID)
	}
	if err != nil {
		return MobileRecordingView{}, false, err
	}
	plaintext, err := openMobileUpload(key, nonce, hubID, deviceID, req.Upload.ID, sealed)
	if err != nil {
		return MobileRecordingView{}, false, err
	}
	sum := sha256.Sum256(plaintext)
	if int64(len(plaintext)) != req.Bytes || hex.EncodeToString(sum[:]) != req.SHA256 {
		return MobileRecordingView{}, false, errMobileRecordingSHA
	}
	release := relays.app.filterWatchContext(&req)
	view, complete, err := store.addPart(deviceID, req, plaintext)
	release()
	if err != nil {
		return MobileRecordingView{}, false, err
	}
	if err := client.deleteUpload(ctx, req.Upload.ID); err != nil {
		log.Printf("[recordings] relay copy of %s not removed yet", req.Upload.ID)
	}
	return view, complete, nil
}

// serveRecordingPost handles POST /recordings (24.2).
func (bridge *mobileBridge) serveRecordingPost(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var req MobileRecordingRequest
	if !mobileDecode(w, r, &req) {
		return
	}
	if _, valid := mobileKey(device.DeviceID, req.IdempotencyKey); !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if err := validateMobileRecordingRequest(req, time.Now()); err != nil {
		// Nothing of a request that is not valid is written, only that it came.
		log.Printf("[recordings] a part was refused: %v", err)
		mobileJSON(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	// From here every way this request ends is one line in the log. On 2026-11-19 four
	// finished parts sat on the relay for hours, and nothing here said whether the phone had
	// asked the computer to take them or what it was told.
	if bridge.app != nil && bridge.app.IsClosedEyesModeEnabled() {
		// The phone keeps the recording and sends it after observing resumes.
		log.Printf("[recordings] part %d of %s waits: observing is paused here", req.Part, req.RecordingID)
		mobileJSON(w, http.StatusConflict, map[string]string{"code": "observation_paused"}, "observing is paused on this computer")
		return
	}
	bridge.mu.Lock()
	relays, identity, store := bridge.recordingRelaysLocked(), bridge.identity, bridge.recordingStoreLocked()
	bridge.mu.Unlock()
	if identity == nil || store == nil {
		log.Printf("[recordings] part %d of %s waits: the computer is not ready", req.Part, req.RecordingID)
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	ctx, cancel := context.WithTimeout(bridge.ctx, mobileRecordingFetchWait)
	defer cancel()
	// Apply the choices at announcement and again before either disk write.
	release := bridge.app.filterWatchContext(&req)
	release()
	view, complete, err := relays.receivePart(ctx, store, identity.HubID, device.DeviceID, req)
	switch {
	case err == nil:
	case errors.Is(err, errMobileRecordingGone) && req.Ahead:
		// Told ahead of its upload: kept, and fetched when the relay has it. The
		// answer says "expected", never "received": the phone keeps the part.
		if err := bridge.expectRecordingPart(store, device.DeviceID, req, time.Now()); err != nil {
			log.Printf("[recordings] part %d of %s not kept as expected: %v", req.Part, req.RecordingID, err)
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
			return
		}
		bridge.mu.Lock()
		bridge.startExpectedRecordingsLocked()
		bridge.mu.Unlock()
		log.Printf("[recordings] part %d of %s expected", req.Part, req.RecordingID)
		mobileJSON(w, http.StatusAccepted, MobileRecordingView{ID: req.RecordingID, State: mobileRecordingStateExpected}, "")
		return
	case errors.Is(err, errMobileRecordingGone):
		log.Printf("[recordings] part %d of %s is not on the relay", req.Part, req.RecordingID)
		mobileJSON(w, http.StatusNotFound, map[string]string{"code": "upload_missing"}, err.Error())
		return
	case errors.Is(err, errMobileRecordingSHA), errors.Is(err, errMobileAttachmentRejected), errors.Is(err, errMobileAttachmentKey), errors.Is(err, errMobileRecordingInvalid):
		log.Printf("[recordings] part %d of %s refused: %v", req.Part, req.RecordingID, err)
		mobileJSON(w, http.StatusBadRequest, nil, err.Error())
		return
	default:
		log.Printf("[recordings] part %d of %s not received", req.Part, req.RecordingID)
		mobileJSON(w, http.StatusServiceUnavailable, nil, "the computer could not fetch the recording yet")
		return
	}
	// A part told ahead earlier and sent now in the usual way is no longer expected.
	store.forgetExpected(device.DeviceID, req.RecordingID, req.Part)
	view = bridge.recordingPartTaken(store, req, view, complete)
	mobileJSON(w, http.StatusAccepted, view, "")
}

// serveRecordingList handles GET /recordings?ids=a,b (24.3).
func (bridge *mobileBridge) serveRecordingList(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var ids []string
	if raw := strings.TrimSpace(r.URL.Query().Get("ids")); raw != "" {
		for _, id := range strings.Split(raw, ",") {
			if !mobileValidUUID(id) || len(ids) >= mobileRecordingListMax {
				mobileJSON(w, http.StatusBadRequest, nil, "the recording ids are not valid")
				return
			}
			ids = append(ids, id)
		}
	}
	bridge.mu.Lock()
	store := bridge.recordingStoreLocked()
	bridge.mu.Unlock()
	if store == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	views, err := store.list(device.DeviceID, ids, mobileRecordingListMax, r.URL.Query().Get("deleted") == "1")
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	if views == nil {
		views = []MobileRecordingView{}
	}
	// The names of what each hour heard (24.7). Like the transcript, they are
	// not given while observing is paused.
	if bridge.app != nil && !bridge.app.IsClosedEyesModeEnabled() {
		names := watchConversationNamesByRecording(store.dir)
		for i := range views {
			if views[i].State == mobileRecordingTranscribed {
				views[i].Conversations = names[views[i].ID]
			}
		}
	}
	mobileJSON(w, http.StatusOK, map[string]any{"recordings": views}, "")
}

// recordingStoreLocked returns the bridge's recording store, made on first use.
func (bridge *mobileBridge) recordingStoreLocked() *mobileRecordingStore {
	if bridge.stateDir == "" {
		return nil
	}
	mobileRecordingStoresMu.Lock()
	defer mobileRecordingStoresMu.Unlock()
	store := mobileRecordingStores[bridge.stateDir]
	if store == nil {
		store = newMobileRecordingStore(bridge.stateDir)
		mobileRecordingStores[bridge.stateDir] = store
		// The store lives for the process, as does this subscription.
		subscribePrivacyWithdrawals(store.onPrivacyWithdrawal)
	}
	// The mobile bridge can be initialized before activity storage is ready.
	if bridge.app != nil && bridge.app.activity != nil && bridge.app.activity.store != nil {
		store.mu.Lock()
		store.cutUses = bridge.app.store
		store.cutDB = bridge.app.activity.store.db
		store.mu.Unlock()
	}
	// The caller holds bridge.mu, so this is the set of paired devices now.
	paired := make([]string, 0, len(bridge.devices))
	for id := range bridge.devices {
		paired = append(paired, id)
	}
	store.setPaired(paired)
	return store
}

// One store per state folder, so a restarted bridge keeps the same lock.
var (
	mobileRecordingStoresMu sync.Mutex
	mobileRecordingStores   = map[string]*mobileRecordingStore{}
)

// deleteUpload removes a phone's upload the computer now holds.
func (c *mobileRelayClient) deleteUpload(ctx context.Context, uploadID string) error {
	resp, err := c.hubUploadRequest(ctx, http.MethodDelete, uploadID)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w (status %d)", errMobileAttachmentRejected, resp.StatusCode)
	}
	return nil
}

func (c *mobileRelayClient) hubUploadRequest(ctx context.Context, method, uploadID string) (*http.Response, error) {
	req, err := c.hubUploadNewRequest(ctx, method, uploadID)
	if err != nil {
		return nil, err
	}
	resp, err := mobileAttachmentHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the relay: %w", err)
	}
	return resp, nil
}

//isoform:destination relay
func (c *mobileRelayClient) hubUploadNewRequest(ctx context.Context, method, uploadID string) (*http.Request, error) {
	if !mobileValidUUID(uploadID) {
		return nil, errMobileRecordingInvalid
	}
	req, err := http.NewRequestWithContext(ctx, method, c.hubOrigin()+"/v1/hub/uploads/"+uploadID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.credential)
	req.Header.Set("X-Yansu-Hub", c.hubID)
	return req, nil
}

// mobileRecordingWriteFile writes data through a temporary file in the same
// folder, so a crash never leaves half a file under the final name.
func mobileRecordingWriteFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(name, mode)
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// mobileRecordingFFmpeg runs the decode; a variable so tests need no ffmpeg.
var mobileRecordingFFmpeg = func(ffmpegPath, in, out string) ([]byte, error) {
	return cmdutil.Command(ffmpegPath, "-nostdin", "-v", "error", "-y", "-i", in,
		"-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-bitexact", "-map_metadata", "-1", "-f", "wav", out).CombinedOutput()
}

// --- Transcription ---

const mobileSpeechNotReady = "speech is not set up on this computer"

// mobileRecordingTranscriber turns one part file into timed speech segments.
// The default decodes with the bundled ffmpeg and runs the after-call engine;
// tests replace it.
var mobileRecordingTranscriber = func(a *App, path string) ([]mobileRecordingSegment, error) {
	wavPath, cleanup, err := decodeMobileRecordingToWAV(installedFFmpegPath(), path)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	transcriber, release := a.afterCallTranscriber()
	defer release()
	if transcriber == nil {
		return nil, errors.New(mobileSpeechNotReady)
	}
	info, err := readMeetingWAVInfo(wavPath)
	if err != nil {
		return nil, err
	}
	reader, err := openMeetingWAVChannel(wavPath, info, 0)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	segments, err := transcriber.TranscribeLongForm(reader, string(backgroundListeningSourceMicrophone))
	if err != nil {
		return nil, err
	}
	return mobileRecordingSpeakerSegments(segments, func(seg asr.Segment) (*bool, []float32) {
		samples, err := readMeetingWAVSpan(wavPath, info, seg.Start, seg.End)
		if err != nil {
			return nil, nil
		}
		return a.verifySpeakerOnSamples(samples)
	}), nil
}

// decodeMobileRecordingToWAV decodes a part to a 16 kHz mono WAV in a
// temporary folder. cleanup is never nil.
func decodeMobileRecordingToWAV(ffmpegPath, path string) (string, func(), error) {
	noop := func() {}
	if ffmpegPath == "" {
		return "", noop, fmt.Errorf("ffmpeg is not installed")
	}
	dir, err := os.MkdirTemp("", "isoform-watch-audio-")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	wavPath := filepath.Join(dir, "part.wav")
	out, err := mobileRecordingFFmpeg(ffmpegPath, path, wavPath)
	if err != nil {
		cleanup()
		return "", noop, fmt.Errorf("ffmpeg decode: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return wavPath, cleanup, nil
}

// mobileRecordingSaver stores the transcript as an activity session. Tests
// replace it to check the events without an activity store.
var mobileRecordingSaver = func(a *App, session ActivitySessionRow, events []activitymon.ActivityEvent) error {
	if a.activity == nil || a.activity.store == nil {
		return errMobileStoreUnavailable
	}
	return a.activity.store.SaveSession(session, events, nil)
}

var mobileRecordingWorker sync.Mutex

// startMobileRecordingTranscription transcribes every complete recording not
// yet transcribed, one at a time, in the background. A second call while the
// worker runs is a no-op; the running worker picks the new one up.
func (a *App) startMobileRecordingTranscription() {
	bridge := a.mobileBridgeForUse()
	if bridge == nil || !bridge.trackBackground() {
		return
	}
	go func() {
		defer bridge.background.Done()
		bridge.mu.Lock()
		store := bridge.recordingStoreLocked()
		bridge.mu.Unlock()
		if store != nil {
			a.transcribeMobileRecordings(bridge.ctx, store)
			a.nameWatchConversations(bridge.ctx, store)
			a.noteWatchConversations(bridge.ctx, store)
		}
	}()
}

// resumeRecordingTranscription starts the worker for this bridge's waiting
// recordings. It is called after each relay registration.
func (bridge *mobileBridge) resumeRecordingTranscription() {
	if bridge.app == nil || bridge.stateDir == "" || !bridge.trackBackground() {
		return
	}
	go func() {
		defer bridge.background.Done()
		bridge.mu.Lock()
		store := bridge.recordingStoreLocked()
		bridge.mu.Unlock()
		// Once per launch: a recording that failed (speech not set up yet, a
		// decode that could not run) gets another try, and audio whose
		// transcript is old enough is removed.
		bridge.app.retryMobileRecordings(bridge.ctx, store, true)
		// Also names what an earlier launch left unnamed, and drops names of deleted hours.
		bridge.app.nameWatchConversations(bridge.ctx, store)
		bridge.app.noteWatchConversations(bridge.ctx, store)
	}()
}

// Hold the worker across requeue and drain. An arrival waits for an in-flight
// decode to record its failure before looking for speech-setup failures.
func (a *App) retryMobileRecordings(ctx context.Context, store *mobileRecordingStore, launch bool) {
	mobileRecordingWorker.Lock()
	defer mobileRecordingWorker.Unlock()
	if ctx.Err() != nil {
		return
	}
	if launch {
		mobileRecordingLaunchOnce.Do(store.retryFailedAndPrune)
	} else {
		store.retryFailed(false)
	}
	a.transcribeMobileRecordingsLocked(ctx, store)
}

// transcribeMobileRecordings is the worker loop.
func (a *App) transcribeMobileRecordings(ctx context.Context, store *mobileRecordingStore) {
	if !mobileRecordingWorker.TryLock() {
		return
	}
	defer mobileRecordingWorker.Unlock()
	a.transcribeMobileRecordingsLocked(ctx, store)
}

func (a *App) transcribeMobileRecordingsLocked(ctx context.Context, store *mobileRecordingStore) {
	for ctx.Err() == nil {
		pending := store.pending()
		if len(pending) == 0 {
			return
		}
		rec := pending[0]
		if a.IsClosedEyesModeEnabled() {
			// Paused: the recording waits on disk and is transcribed after resume.
			return
		}
		if err := store.setState(rec.ID, mobileRecordingTranscribing, "", 0, ""); err != nil {
			if errors.Is(err, errWatchRecordingNotFound) {
				continue
			}
			return
		}
		sessionID, words, err := a.transcribeMobileRecording(store, rec)
		if err != nil {
			log.Printf("[recordings] %s not transcribed: %v", rec.ID, err)
			if stateErr := store.setState(rec.ID, mobileRecordingFailed, "", 0, err.Error()); stateErr != nil && !errors.Is(stateErr, errWatchRecordingNotFound) {
				// setState logged the index error. Let a later worker retry.
				return
			}
			continue
		}
		switch err := store.setState(rec.ID, mobileRecordingTranscribed, sessionID, words, ""); err {
		case nil:
			log.Printf("[recordings] %s transcribed: %d words", rec.ID, words)
		case errWatchRecordingNotFound:
			// Deleted while it was transcribed: the transcript goes too.
			if sessionID != "" {
				a.deleteMobileRecordingSession(sessionID)
			}
		default:
			// The index is unavailable. Keep the saved transcript.
			return
		}
	}
}

// transcribeMobileRecording transcribes every part and saves one session.
func (a *App) transcribeMobileRecording(store *mobileRecordingStore, rec mobileRecording) (string, int, error) {
	paths := store.partPaths(rec)
	appName, bundleID := mobileRecordingAppName, mobileRecordingBundleID
	if rec.Source == "phone" {
		appName, bundleID = mobileRecordingPhoneApp, mobileRecordingPhoneID
	}
	// What was recorded while observing was paused, or after listening on the
	// watch was turned off, is never read. Without the pause log nothing is
	// read at all; the recording is tried again later.
	pauses, err := a.mobileRecordingDropSpans(rec.Source)
	if err != nil {
		return "", 0, fmt.Errorf("the pause log could not be read: %w", err)
	}
	var events []activitymon.ActivityEvent
	// A nonzero start is the earliest eligible part timestamp. It says
	// nothing about whether recognition heard speech in that part.
	var start, end int64
	words := 0
	for i, part := range rec.Received {
		overlap := mobileRecordingPartPause(part, pauses)
		if part.Paused || part.PausedCut || overlap == mobileRecordingPauseFull {
			// Its audio is gone, or goes below without being read; or its
			// paused minutes were cut out after its words were saved, and its
			// times no longer line up with the clock.
			continue
		}
		partStart, err := time.Parse(time.RFC3339, part.StartedAt)
		if err != nil {
			return "", 0, errMobileRecordingInvalid
		}
		partEnd := partStart.Add(time.Duration(part.DurationSeconds * float64(time.Second)))
		if start == 0 || partStart.UnixMilli() < start {
			start = partStart.UnixMilli()
		}
		if partEnd.UnixMilli() > end {
			end = partEnd.UnixMilli()
		}
		segments, err := mobileRecordingTranscriber(a, paths[i])
		if err != nil {
			return "", 0, err
		}
		if overlap == mobileRecordingPausePartial {
			segments = dropPausedMobileRecordingSegments(segments, partStart, pauses)
		}
		events = append(events, mobileRecordingSpeechEvents(segments, partStart, appName, bundleID)...)
	}
	// One pass over the whole recording, so a voice keeps its number across
	// parts; and it starts from the voices of the hour before when this
	// recording begins where that one ended (mobile_recording_voices.go).
	ownerVoice, voices := labelMobileRecordingSpeakersFrom(events, store.carriedVoices(rec))
	for _, evt := range events {
		words += len(strings.Fields(evt.SpeechText))
	}
	if a.IsClosedEyesModeEnabled() {
		return "", 0, errors.New("observing was paused during transcription")
	}
	if len(events) == 0 {
		// The decode succeeded with no eligible speech. Finish without a session.
		store.dropPausedAudio(rec.ID, pauses, false)
		return "", 0, nil
	}
	// Heart rate and place summarize a whole part, so a part a pause touched
	// lends none: its numbers would include the paused minutes.
	clear := make([]mobileRecordingPart, 0, len(rec.Received))
	for _, part := range rec.Received {
		if !part.Paused && !part.PausedCut && mobileRecordingPartPause(part, pauses) == mobileRecordingPauseNone {
			clear = append(clear, part)
		}
	}
	applyMobileRecordingContext(events, clear, appName)
	appNames, _ := json.Marshal([]string{appName})
	session := ActivitySessionRow{
		ID:           "rec-" + rec.ID,
		StartTime:    start,
		EndTime:      end,
		TriggerKind:  mobileRecordingTrigger,
		AppNames:     string(appNames),
		EventCount:   len(events),
		TriageStatus: "pending",
		CreatedAt:    time.Now().UnixMilli(),
	}
	if err := mobileRecordingSaver(a, session, events); err != nil {
		return "", 0, err
	}
	store.setOwnerVoice(rec.ID, ownerVoice)
	store.saveVoices(rec.ID, voices)
	// The words outside the pause are saved; the audio a pause touched goes now.
	store.dropPausedAudio(rec.ID, pauses, false)
	return session.ID, words, nil
}

// mobileRecordingSpeechEvents turns segments into speech events at their
// wall-clock times. Empty segments are dropped.
func mobileRecordingSpeechEvents(segments []mobileRecordingSegment, partStart time.Time, appName, bundleID string) []activitymon.ActivityEvent {
	events := make([]activitymon.ActivityEvent, 0, len(segments))
	for _, seg := range segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		var endAt int64
		if seg.End > seg.Start {
			endAt = partStart.Add(seg.End).UnixMilli()
		}
		events = append(events, activitymon.ActivityEvent{
			SpeechEndAt:  endAt,
			Timestamp:    partStart.Add(seg.Start).UnixMilli(),
			Kind:         activitymon.EventSpeech,
			AppBundleID:  bundleID,
			AppName:      appName,
			WindowTitle:  appName,
			SpeechText:   text,
			SpeechSource: string(backgroundListeningSourceMicrophone),
			// Who spoke: the verdict against the owner's enrolled voice and
			// the fingerprint the labels are grouped by.
			SpeechSpeakerVerified:  seg.OwnerVerified,
			SpeechSpeakerEmbedding: seg.Embedding,
		})
	}
	return events
}

// mobileRecordingSegment is one stretch of speech and who spoke it.
// OwnerVerified is nil when no verdict could be made: no enrolled voice, no
// voice model, or a stretch shorter than a second.
type mobileRecordingSegment struct {
	asr.Segment
	OwnerVerified *bool
	Embedding     []float32
}

// mobileRecordingSpeakerMaxSpan bounds how much of one segment is read for
// its voice fingerprint; a longer monologue is judged by its opening.
const mobileRecordingSpeakerMaxSpan = 30 * time.Second

// mobileRecordingSpeakerSegments attaches a speaker verdict to each segment
// that has words. verify gets the segment and answers as verifySpeakerOnWAV
// does.
func mobileRecordingSpeakerSegments(segments []asr.Segment, verify func(asr.Segment) (*bool, []float32)) []mobileRecordingSegment {
	out := make([]mobileRecordingSegment, 0, len(segments))
	for _, seg := range segments {
		item := mobileRecordingSegment{Segment: seg}
		if strings.TrimSpace(seg.Text) != "" && seg.End-seg.Start >= time.Second {
			span := seg
			if span.End-span.Start > mobileRecordingSpeakerMaxSpan {
				span.End = span.Start + mobileRecordingSpeakerMaxSpan
			}
			item.OwnerVerified, item.Embedding = verify(span)
		}
		out = append(out, item)
	}
	return out
}

// readMeetingWAVSpan reads the first channel of a 16 kHz PCM WAV between two
// offsets, without loading the rest of the file.
func readMeetingWAVSpan(path string, info meetingWAVInfo, start, end time.Duration) ([]float32, error) {
	frame := int64(2 * info.channels)
	first := int64(start.Seconds()*meetingAudioSampleRate) * frame
	last := int64(end.Seconds()*meetingAudioSampleRate) * frame
	if last > info.dataSize {
		last = info.dataSize / frame * frame
	}
	if first < 0 || first >= last {
		return nil, errMobileRecordingInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw := make([]byte, last-first)
	if _, err := f.ReadAt(raw, info.dataOffset+first); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	samples := make([]float32, len(raw)/int(frame))
	const scale = float32(1.0 / 32768.0)
	for i := range samples {
		at := i * int(frame)
		samples[i] = float32(int16(uint16(raw[at])|uint16(raw[at+1])<<8)) * scale
	}
	return samples, nil
}

// Speaker labels a watch recording's speech carries (ActivityEvent.SpeechSpeakerLabel).
const (
	mobileRecordingSpeakerOwner  = "you"
	mobileRecordingSpeakerPrefix = "speaker_"
)

// How a recording's speech was labelled, for the phone (24.3 `ownerVoice`).
const (
	// mobileOwnerVoiceLabelled: the owner has an enrolled voice and it was checked.
	mobileOwnerVoiceLabelled = "labelled"
	// mobileOwnerVoiceNotEnrolled: voices were told apart but nobody is "you",
	// because the owner has not added their voice.
	mobileOwnerVoiceNotEnrolled = "not_enrolled"
	// mobileOwnerVoiceUnavailable: no voice model on this computer, or nothing
	// long enough to tell; the speech carries no speaker labels.
	mobileOwnerVoiceUnavailable = "unavailable"
)

// labelMobileRecordingSpeakers writes SpeechSpeakerLabel on every event that
// has a voice fingerprint and reports how the recording was labelled. The
// owner's verified stretches are "you"; every other voice gets a number in the
// order it is first heard, grouped by the same greedy rule and threshold as
// the Activity page's speaker chips (ListSessionSpeakerClusters). Without an
// enrollment no stretch is verified, so every voice is a numbered speaker.
func labelMobileRecordingSpeakers(events []activitymon.ActivityEvent) string {
	status, _ := labelMobileRecordingSpeakersFrom(events, nil)
	return status
}
