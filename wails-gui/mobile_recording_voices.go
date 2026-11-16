package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/speakerid"
)

// A voice keeps its number across the hour's edge.
//
// The watch keeps recordings by the clock hour, and voices are told apart
// one recording at a time. So without this, Sam can be "Speaker 1" until ten
// o'clock and "Speaker 2" after it, in the same talk. Each transcribed
// recording now leaves the voices it heard (an averaged fingerprint and a
// number each) beside its audio. A recording that begins where another one
// from the same device ended starts from those voices instead of from
// nothing, so the same voice gets the same number.
//
// The fingerprints live in the recording's own folder and go when its audio
// goes: on delete, on a deleted transcript, and at the end of the keep time.

const (
	mobileRecordingVoicesFile = "voices.json"
	// mobileRecordingVoiceCarryGap is how soon after one recording ended the
	// next must begin to carry its voices: the same two minutes of silence
	// that end a conversation.
	mobileRecordingVoiceCarryGap = 2 * time.Minute
	// mobileRecordingVoiceCarryOverlap allows for clocks: the next hour may
	// claim to begin a moment before the last one ended.
	mobileRecordingVoiceCarryOverlap = 5 * time.Second
)

// mobileRecordingVoice is one voice other than the owner's heard in a
// recording: its number ("speaker_<number>"), its averaged fingerprint and
// how many stretches of speech it was averaged from.
type mobileRecordingVoice struct {
	Number   int       `json:"number"`
	Centroid []float32 `json:"centroid"`
	Count    int       `json:"count"`
}

// labelMobileRecordingSpeakersFrom is labelMobileRecordingSpeakers that
// begins from the voices of the recording before. It returns the voices
// heard in these events (carried ones that stayed silent are left out), for
// the recording after.
func labelMobileRecordingSpeakersFrom(events []activitymon.ActivityEvent, carried []mobileRecordingVoice) (string, []mobileRecordingVoice) {
	type cluster struct {
		number   int
		centroid []float32
		count    int
		heard    bool
	}
	var others []*cluster
	next := 1
	for _, voice := range carried {
		if voice.Number < 1 || len(voice.Centroid) == 0 {
			continue
		}
		others = append(others, &cluster{number: voice.Number, centroid: append([]float32(nil), voice.Centroid...), count: max(voice.Count, 1)})
		if voice.Number >= next {
			next = voice.Number + 1
		}
	}
	judged, fingerprinted := false, false
	for i := range events {
		evt := &events[i]
		if evt.Kind != activitymon.EventSpeech || len(evt.SpeechSpeakerEmbedding) == 0 {
			continue
		}
		fingerprinted = true
		if evt.SpeechSpeakerVerified != nil {
			judged = true
			if *evt.SpeechSpeakerVerified {
				evt.SpeechSpeakerLabel = mobileRecordingSpeakerOwner
				continue
			}
		}
		best, bestSim := -1, -2.0
		for j, c := range others {
			if len(c.centroid) != len(evt.SpeechSpeakerEmbedding) {
				continue
			}
			if sim := speakerid.CosineSimilarity(evt.SpeechSpeakerEmbedding, c.centroid); sim > bestSim {
				best, bestSim = j, sim
			}
		}
		if best < 0 || bestSim < speakerClusterMergeThreshold {
			others = append(others, &cluster{number: next, centroid: append([]float32(nil), evt.SpeechSpeakerEmbedding...)})
			next++
			best = len(others) - 1
		}
		target := others[best]
		target.count++
		target.heard = true
		n := float32(target.count)
		for k := range target.centroid {
			target.centroid[k] = (target.centroid[k]*(n-1) + evt.SpeechSpeakerEmbedding[k]) / n
		}
		evt.SpeechSpeakerLabel = mobileRecordingSpeakerPrefix + strconv.Itoa(target.number)
	}
	var voices []mobileRecordingVoice
	for _, c := range others {
		if c.heard {
			voices = append(voices, mobileRecordingVoice{Number: c.number, Centroid: c.centroid, Count: c.count})
		}
	}
	switch {
	case judged:
		return mobileOwnerVoiceLabelled, voices
	case fingerprinted:
		return mobileOwnerVoiceNotEnrolled, voices
	default:
		return mobileOwnerVoiceUnavailable, voices
	}
}

// span is when a recording's audio began and ended, from its parts.
func (r *mobileRecording) span() (time.Time, time.Time, bool) {
	var start, end time.Time
	for _, part := range r.Received {
		at, err := time.Parse(time.RFC3339, part.StartedAt)
		if err != nil {
			return start, end, false
		}
		until := at.Add(time.Duration(part.DurationSeconds * float64(time.Second)))
		if start.IsZero() || at.Before(start) {
			start = at
		}
		if until.After(end) {
			end = until
		}
	}
	return start, end, !start.IsZero()
}

// saveVoices keeps the voices a recording heard beside its audio.
func (s *mobileRecordingStore) saveVoices(id string, voices []mobileRecordingVoice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return
	}
	rec := s.recordings[id]
	if rec == nil || rec.AudioRemoved || rec.State == mobileRecordingDeleted {
		return
	}
	path := filepath.Join(s.dir, rec.ID, mobileRecordingVoicesFile)
	if len(voices) == 0 {
		_ = os.Remove(path)
		return
	}
	raw, err := json.Marshal(voices)
	if err != nil {
		return
	}
	if err := mobileRecordingWriteFile(path, raw, 0o600); err != nil {
		log.Printf("[recordings] voices of %s not kept: %v", rec.ID, err)
	}
}

// carriedVoices are the voices of the recording that ended where rec begins:
// same device, same source, transcribed, with its audio still kept. Nil when
// there is none, and then numbering starts from one.
func (s *mobileRecordingStore) carriedVoices(rec mobileRecording) []mobileRecordingVoice {
	start, _, ok := rec.span()
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil
	}
	var before *mobileRecording
	var beforeEnd time.Time
	for _, other := range s.recordings {
		if other.ID == rec.ID || other.DeviceID != rec.DeviceID || other.Source != rec.Source ||
			other.State != mobileRecordingTranscribed || other.AudioRemoved {
			continue
		}
		_, end, ok := other.span()
		if !ok {
			continue
		}
		gap := start.Sub(end)
		if gap < -mobileRecordingVoiceCarryOverlap || gap > mobileRecordingVoiceCarryGap {
			continue
		}
		if before == nil || end.After(beforeEnd) {
			before, beforeEnd = other, end
		}
	}
	if before == nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, before.ID, mobileRecordingVoicesFile))
	if err != nil {
		return nil
	}
	var voices []mobileRecordingVoice
	if err := json.Unmarshal(raw, &voices); err != nil {
		return nil
	}
	return voices
}
