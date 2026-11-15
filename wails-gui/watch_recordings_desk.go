package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/platform/activitymon"
)

// Watch recordings on the computer's own screens and their detail for the
// phone (PROTOCOL.md 24.5). The Activity page lists the recordings the
// computer received, plays their audio while it is kept, shows what was said
// with who said it, and deletes one everywhere. The phone reads the same
// transcript and asks for the same delete through two sealed routes.

// WatchRecording is one recording as the Activity page shows it.
type WatchRecording struct {
	ID              string  `json:"id"`
	Source          string  `json:"source"`
	State           string  `json:"state"`
	StartedAt       string  `json:"startedAt"`
	DurationSeconds float64 `json:"durationSeconds"`
	Words           int     `json:"words"`
	OwnerVoice      string  `json:"ownerVoice"`
	// SessionID is the activity session that holds the transcript, when one
	// was saved; the Screen timeline shows it at the recording's time.
	SessionID string               `json:"sessionId"`
	HasAudio  bool                 `json:"hasAudio"`
	Parts     []WatchRecordingPart `json:"parts"`
}

// WatchRecordingPart is one stored piece of a recording.
type WatchRecordingPart struct {
	Part            int     `json:"part"`
	StartedAt       string  `json:"startedAt"`
	DurationSeconds float64 `json:"durationSeconds"`
	HasAudio        bool    `json:"hasAudio"`
	// Place is a name only; coordinates never leave the store.
	Place string `json:"place"`
	// HeartRate is "", "calm", "raised" or "high".
	HeartRate    string `json:"heartRate"`
	HeartRateAvg int    `json:"heartRateAvg"`
	HeartRateMax int    `json:"heartRateMax"`
}

// WatchRecordingLine is one stretch of speech. Speaker is "you", "speaker_N"
// or empty when voices could not be told apart.
type WatchRecordingLine struct {
	At      int64  `json:"at"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

var errWatchRecordingNotFound = errors.New("recording not found")

// watchRecordingStore is the recording store of the running mobile bridge, or
// nil when no phone was ever paired.
func (a *App) watchRecordingStore() *mobileRecordingStore {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return nil
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return bridge.recordingStoreLocked()
}

// all returns a copy of every recording that was not deleted, newest first.
func (s *mobileRecordingStore) all() ([]mobileRecording, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	out := make([]mobileRecording, 0, len(s.recordings))
	for _, rec := range s.recordings {
		if rec.State == mobileRecordingDeleted {
			continue
		}
		copied := *rec
		copied.Received = append([]mobileRecordingPart(nil), rec.Received...)
		out = append(out, copied)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].view().StartedAt > out[j].view().StartedAt })
	return out, nil
}

// get returns a copy of one recording. deviceID, when set, must own it.
func (s *mobileRecordingStore) get(id, deviceID string) (mobileRecording, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return mobileRecording{}, false
	}
	rec := s.recordings[id]
	if rec == nil || (deviceID != "" && rec.DeviceID != deviceID) {
		return mobileRecording{}, false
	}
	copied := *rec
	copied.Received = append([]mobileRecordingPart(nil), rec.Received...)
	return copied, true
}

// markDeleted removes a recording's audio and context and leaves a row that
// only says it was deleted. It returns the transcript's session, if any.
func (s *mobileRecordingStore) markDeleted(id, deviceID string) (string, MobileRecordingView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return "", MobileRecordingView{}, err
	}
	rec := s.recordings[id]
	if rec == nil || (deviceID != "" && rec.DeviceID != deviceID) {
		return "", MobileRecordingView{}, errWatchRecordingNotFound
	}
	if rec.State == mobileRecordingDeleted {
		return "", rec.view(), nil
	}
	if err := os.RemoveAll(filepath.Join(s.dir, rec.ID)); err != nil {
		return "", MobileRecordingView{}, err
	}
	sessionID := rec.SessionID
	if sessionID == "" {
		// A transcript being saved right now is named the same way.
		sessionID = "rec-" + rec.ID
	}
	rec.State, rec.Error, rec.AudioRemoved = mobileRecordingDeleted, "", true
	rec.Title, rec.Words, rec.OwnerVoice, rec.SessionID = "", 0, "", ""
	// The parts' times stay so a repeat of a part is still recognized; their
	// files, sizes and context are gone.
	for i := range rec.Received {
		rec.Received[i].Context = nil
	}
	rec.UpdatedAt = mobileTimestamp(s.now())
	if err := s.saveLocked(); err != nil {
		return "", MobileRecordingView{}, err
	}
	return sessionID, rec.view(), nil
}

// deleteMobileRecordingSession deletes the transcript's activity session the
// way a user deletion does, so memory and the brief withdraw what they read.
func (a *App) deleteMobileRecordingSession(sessionID string) {
	if sessionID == "" || a.activity == nil || a.activity.store == nil {
		return
	}
	if err := a.activity.store.DeleteSession(sessionID); err != nil {
		log.Printf("[recordings] transcript %s not deleted: %v", sessionID, err)
	}
}

// deleteWatchRecording deletes one recording on this computer: its audio, its
// heart rate and place, and its transcript with what was learned from it.
func (a *App) deleteWatchRecording(store *mobileRecordingStore, id, deviceID string) (MobileRecordingView, error) {
	if store == nil || !mobileValidUUID(id) {
		return MobileRecordingView{}, errWatchRecordingNotFound
	}
	sessionID, view, err := store.markDeleted(id, deviceID)
	if err != nil {
		return MobileRecordingView{}, err
	}
	a.deleteMobileRecordingSession(sessionID)
	return view, nil
}

func watchRecordingEntry(store *mobileRecordingStore, rec mobileRecording) WatchRecording {
	view := rec.view()
	out := WatchRecording{ID: rec.ID, Source: rec.Source, State: rec.State, StartedAt: view.StartedAt,
		DurationSeconds: view.DurationSeconds, Words: rec.Words, OwnerVoice: rec.OwnerVoice,
		SessionID: rec.SessionID, Parts: make([]WatchRecordingPart, 0, len(rec.Received))}
	for _, part := range rec.Received {
		entry := WatchRecordingPart{Part: part.Part, StartedAt: part.StartedAt, DurationSeconds: part.DurationSeconds}
		if !rec.AudioRemoved {
			if _, err := os.Stat(filepath.Join(store.dir, rec.ID, part.File)); err == nil {
				entry.HasAudio, out.HasAudio = true, true
			}
		}
		if ctx := part.Context; ctx != nil {
			if ctx.Location != nil {
				entry.Place = ctx.Location.Place
			}
			if hr := ctx.HeartRate; hr != nil {
				entry.HeartRate, entry.HeartRateAvg, entry.HeartRateMax = mobileRecordingIntensity(hr.Avg), int(hr.Avg), int(hr.Max)
			}
		}
		out.Parts = append(out.Parts, entry)
	}
	return out
}

// GetWatchRecordings lists the recordings this computer received from the
// watch and the phone, newest first.
func (a *App) GetWatchRecordings() ([]WatchRecording, error) {
	out := []WatchRecording{}
	store := a.watchRecordingStore()
	if store == nil {
		return out, nil
	}
	recordings, err := store.all()
	if err != nil {
		return nil, err
	}
	for _, rec := range recordings {
		out = append(out, watchRecordingEntry(store, rec))
	}
	return out, nil
}

// watchRecordingLines reads what a recording heard. A transcript marked
// private reads as nothing, as it does everywhere else.
func (a *App) watchRecordingLines(sessionID string) []WatchRecordingLine {
	lines := []WatchRecordingLine{}
	if sessionID == "" || a.activity == nil || a.activity.store == nil {
		return lines
	}
	events, err := a.activity.store.GetSessionEvents(sessionID)
	if err != nil {
		return lines
	}
	for _, evt := range events {
		if evt.Kind != activitymon.EventSpeech || strings.TrimSpace(evt.SpeechText) == "" {
			continue
		}
		lines = append(lines, WatchRecordingLine{At: evt.Timestamp, Speaker: evt.SpeechSpeakerLabel, Text: strings.TrimSpace(evt.SpeechText)})
	}
	return lines
}

// GetWatchRecordingTranscript returns what one recording heard, in order.
func (a *App) GetWatchRecordingTranscript(id string) ([]WatchRecordingLine, error) {
	store := a.watchRecordingStore()
	if store == nil || !mobileValidUUID(id) {
		return nil, errWatchRecordingNotFound
	}
	rec, ok := store.get(id, "")
	if !ok || rec.State == mobileRecordingDeleted {
		return nil, errWatchRecordingNotFound
	}
	return a.watchRecordingLines(rec.SessionID), nil
}

// DeleteWatchRecording deletes one recording from this computer. The phone
// reads the deletion at its next check and removes its own copy and the
// watch's.
func (a *App) DeleteWatchRecording(id string) error {
	_, err := a.deleteWatchRecording(a.watchRecordingStore(), id, "")
	return err
}

// handleActivityRecordingAudio serves one part of a recording to the Activity
// page's player. GET /api/activity/recording?id=<recording id>&part=<n>
func (a *App) handleActivityRecordingAudio(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	number, err := strconv.Atoi(r.URL.Query().Get("part"))
	store := a.watchRecordingStore()
	if store == nil || !mobileValidUUID(id) || err != nil {
		a.bridge.writeError(w, http.StatusBadRequest, "id and part parameters required")
		return
	}
	rec, ok := store.get(id, "")
	if !ok || rec.AudioRemoved || rec.State == mobileRecordingDeleted {
		a.bridge.writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	for _, part := range rec.Received {
		if part.Part != number {
			continue
		}
		// The file name comes from the index, never from the request.
		path := filepath.Join(store.dir, rec.ID, filepath.Base(part.File))
		if _, err := os.Stat(path); err != nil {
			break
		}
		w.Header().Set("Content-Type", rec.Mime)
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, path)
		return
	}
	a.bridge.writeError(w, http.StatusNotFound, "recording not found")
}

// Limits of GET /recordings/transcript: the answer travels in one sealed
// envelope, so a long recording is cut and says so.
const (
	mobileRecordingTranscriptMaxLines = 300
	mobileRecordingTranscriptMaxBytes = 48 << 10
)

// MobileRecordingTranscript is the answer of GET /recordings/transcript.
type MobileRecordingTranscript struct {
	ID        string                          `json:"id"`
	State     string                          `json:"state"`
	Lines     []MobileRecordingTranscriptLine `json:"lines"`
	Truncated bool                            `json:"truncated,omitempty"`
}

// MobileRecordingTranscriptLine is one stretch of speech for the phone.
type MobileRecordingTranscriptLine struct {
	At      string `json:"at"`
	Speaker string `json:"speaker,omitempty"`
	Text    string `json:"text"`
}

// serveRecordingTranscript handles GET /recordings/transcript?id=<uuid> (24.5).
func (bridge *mobileBridge) serveRecordingTranscript(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	bridge.mu.Lock()
	store := bridge.recordingStoreLocked()
	bridge.mu.Unlock()
	if store == nil || bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	if !mobileValidUUID(id) {
		mobileJSON(w, http.StatusBadRequest, nil, "the recording id is not valid")
		return
	}
	rec, ok := store.get(id, device.DeviceID)
	if !ok || rec.State == mobileRecordingDeleted {
		mobileJSON(w, http.StatusNotFound, nil, errMobileNotFound.Error())
		return
	}
	out := MobileRecordingTranscript{ID: rec.ID, State: rec.State, Lines: []MobileRecordingTranscriptLine{}}
	if bridge.app.IsClosedEyesModeEnabled() {
		// Paused: nothing that was heard is read out, here as elsewhere.
		mobileJSON(w, http.StatusOK, out, "")
		return
	}
	size := 0
	for _, line := range bridge.app.watchRecordingLines(rec.SessionID) {
		size += len(line.Text)
		if len(out.Lines) >= mobileRecordingTranscriptMaxLines || size > mobileRecordingTranscriptMaxBytes {
			out.Truncated = true
			break
		}
		out.Lines = append(out.Lines, MobileRecordingTranscriptLine{
			At: mobileTimestamp(time.UnixMilli(line.At)), Speaker: line.Speaker, Text: line.Text})
	}
	mobileJSON(w, http.StatusOK, out, "")
}

// MobileRecordingDeleteRequest is the body of POST /recordings/delete.
type MobileRecordingDeleteRequest struct {
	RecordingID string `json:"recordingId"`
}

// serveRecordingDelete handles POST /recordings/delete (24.5). It is safe to
// repeat: a recording already deleted answers the same way.
func (bridge *mobileBridge) serveRecordingDelete(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var req MobileRecordingDeleteRequest
	if !mobileDecode(w, r, &req) {
		return
	}
	bridge.mu.Lock()
	store := bridge.recordingStoreLocked()
	bridge.mu.Unlock()
	if store == nil || bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	if !mobileValidUUID(req.RecordingID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the recording id is not valid")
		return
	}
	view, err := bridge.app.deleteWatchRecording(store, req.RecordingID, device.DeviceID)
	switch {
	case err == nil:
		mobileJSON(w, http.StatusOK, view, "")
	case errors.Is(err, errWatchRecordingNotFound):
		mobileJSON(w, http.StatusNotFound, nil, errMobileNotFound.Error())
	default:
		log.Printf("[recordings] %s not deleted: %v", req.RecordingID, err)
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
	}
}
