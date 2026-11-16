package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/asr"
	"wails-gui/internal/platform/activitymon"
)

func speechOf(axis int) activitymon.ActivityEvent {
	return activitymon.ActivityEvent{Kind: activitymon.EventSpeech, SpeechText: "words", SpeechSpeakerEmbedding: voiceOf(axis)}
}

func TestCarriedVoicesKeepTheirNumbersAndNewOnesGetTheNext(t *testing.T) {
	// The hour before heard Sam (speaker 1) and Dana (speaker 2).
	before := []activitymon.ActivityEvent{speechOf(0), speechOf(1), speechOf(0)}
	_, voices := labelMobileRecordingSpeakersFrom(before, nil)
	if len(voices) != 2 || voices[0].Number != 1 || voices[1].Number != 2 || voices[0].Count != 2 {
		t.Fatalf("voices %+v", voices)
	}
	// This hour Dana speaks first, then a new voice, then Sam.
	events := []activitymon.ActivityEvent{speechOf(1), speechOf(2), speechOf(0)}
	status, after := labelMobileRecordingSpeakersFrom(events, voices)
	if status != mobileOwnerVoiceNotEnrolled {
		t.Fatalf("status %q", status)
	}
	if events[0].SpeechSpeakerLabel != "speaker_2" || events[1].SpeechSpeakerLabel != "speaker_3" || events[2].SpeechSpeakerLabel != "speaker_1" {
		t.Fatalf("labels %q %q %q", events[0].SpeechSpeakerLabel, events[1].SpeechSpeakerLabel, events[2].SpeechSpeakerLabel)
	}
	if len(after) != 3 {
		t.Fatalf("voices after %+v", after)
	}
	// Without the carry the same hour would have numbered Dana first.
	fresh := []activitymon.ActivityEvent{speechOf(1), speechOf(2), speechOf(0)}
	labelMobileRecordingSpeakersFrom(fresh, nil)
	if fresh[0].SpeechSpeakerLabel != "speaker_1" {
		t.Fatalf("fresh label %q", fresh[0].SpeechSpeakerLabel)
	}
}

func TestAVoiceThatStayedSilentIsNotPassedOnButItsNumberIsNotReused(t *testing.T) {
	carried := []mobileRecordingVoice{{Number: 1, Centroid: voiceOf(0), Count: 3}, {Number: 2, Centroid: voiceOf(1), Count: 1}}
	events := []activitymon.ActivityEvent{speechOf(1), speechOf(3)}
	_, after := labelMobileRecordingSpeakersFrom(events, carried)
	if events[0].SpeechSpeakerLabel != "speaker_2" || events[1].SpeechSpeakerLabel != "speaker_3" {
		t.Fatalf("labels %q %q", events[0].SpeechSpeakerLabel, events[1].SpeechSpeakerLabel)
	}
	if len(after) != 2 || after[0].Number != 2 || after[1].Number != 3 {
		t.Fatalf("passed on %+v", after)
	}
	// A carried fingerprint of another size (a changed voice model) is ignored, not compared.
	odd := []mobileRecordingVoice{{Number: 1, Centroid: []float32{1, 0}, Count: 1}}
	one := []activitymon.ActivityEvent{speechOf(0)}
	labelMobileRecordingSpeakersFrom(one, odd)
	if one[0].SpeechSpeakerLabel != "speaker_2" {
		t.Fatalf("label %q", one[0].SpeechSpeakerLabel)
	}
}

// Sam talks at 9:59 and again at 10:01. The watch cut the recording at ten.
// Sam is Speaker 1 in both hours although someone else speaks first after
// ten; a recording made after a break starts from one again; and the
// fingerprints go with the audio.
func TestAVoiceKeepsItsNumberAcrossTheHoursEdge(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	nine, ten, afternoon := uuid.NewString(), uuid.NewString(), uuid.NewString()
	day := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	add := func(id string, start time.Time, seconds float64) {
		req := sealedRecordingPart(t, uploads, "dev-a", id, 0, true, []byte("audio "+id), start)
		req.DurationSeconds = seconds
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
			t.Fatal(err)
		}
	}
	oldT, oldS, oldP := mobileRecordingTranscriber, mobileRecordingSaver, mobileRecordingPauses
	t.Cleanup(func() { mobileRecordingTranscriber, mobileRecordingSaver, mobileRecordingPauses = oldT, oldS, oldP })
	mobileRecordingPauses = func(*App) ([]observationPause, error) { return nil, nil }
	// Who speaks in each recording, in order, by voice axis.
	script := map[string][]int{nine: {0, 1}, ten: {1, 0}, afternoon: {1}}
	current := ""
	mobileRecordingTranscriber = func(_ *App, path string) ([]mobileRecordingSegment, error) {
		var segments []asr.Segment
		for i := range script[current] {
			segments = append(segments, asr.Segment{Start: time.Duration(i*10) * time.Second, End: time.Duration(i*10+3) * time.Second, Text: "words"})
		}
		next := 0
		return mobileRecordingSpeakerSegments(segments, func(asr.Segment) (*bool, []float32) {
			axis := script[current][next]
			next++
			return nil, voiceOf(axis)
		}), nil
	}
	saved := map[string][]string{}
	mobileRecordingSaver = func(_ *App, session ActivitySessionRow, events []activitymon.ActivityEvent) error {
		for _, evt := range events {
			saved[session.ID] = append(saved[session.ID], evt.SpeechSpeakerLabel)
		}
		return nil
	}
	app := &App{}
	run := func(id string, start time.Time, seconds float64) {
		add(id, start, seconds)
		current = id
		app.transcribeMobileRecordings(context.Background(), store)
	}
	run(nine, day.Add(9*time.Hour+40*time.Minute), 1200) // 9:40 to 10:00
	run(ten, day.Add(10*time.Hour), 3600)                // 10:00 to 11:00, begins where 9 AM ended
	run(afternoon, day.Add(15*time.Hour), 600)           // hours later

	if got := saved["rec-"+nine]; len(got) != 2 || got[0] != "speaker_1" || got[1] != "speaker_2" {
		t.Fatalf("9 AM labels %v", got)
	}
	// After ten the other voice speaks first; Sam (axis 0) is still Speaker 1.
	if got := saved["rec-"+ten]; len(got) != 2 || got[0] != "speaker_2" || got[1] != "speaker_1" {
		t.Fatalf("10 AM labels %v; a voice changed its number at the hour's edge", got)
	}
	// After a break nothing is carried: the first voice heard is Speaker 1.
	if got := saved["rec-"+afternoon]; len(got) != 1 || got[0] != "speaker_1" {
		t.Fatalf("afternoon labels %v", got)
	}

	voices := filepath.Join(store.dir, ten, mobileRecordingVoicesFile)
	info, err := os.Stat(voices)
	if err != nil {
		t.Fatalf("voices of 10 AM not kept: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	// The fingerprints go with the audio: when the hour's transcript is forgotten...
	store.forgetAudioOfSession("rec-" + ten)
	if _, err := os.Stat(voices); err == nil {
		t.Fatal("voice fingerprints outlived the recording's audio")
	}
	// ...and when the hour is deleted ("Delete this hour", from any device).
	nineVoices := filepath.Join(store.dir, nine, mobileRecordingVoicesFile)
	if _, err := os.Stat(nineVoices); err != nil {
		t.Fatalf("voices of 9 AM not kept: %v", err)
	}
	if _, _, err := store.markDeleted(nine, "dev-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(nineVoices); err == nil {
		t.Fatal("voice fingerprints outlived a deleted hour")
	}
	// A deleted hour lends its voices to nothing, and takes none afterwards.
	store.saveVoices(nine, []mobileRecordingVoice{{Number: 1, Centroid: voiceOf(0), Count: 1}})
	if _, err := os.Stat(nineVoices); err == nil {
		t.Fatal("voices were written for a deleted hour")
	}
}

// An hour recorded wholly while observing was paused is never read, so no
// voice from it is kept either.
func TestAnHourDroppedByAPauseLeavesNoVoices(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	id := uuid.NewString()
	started := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)
	req := sealedRecordingPart(t, uploads, "dev-a", id, 0, true, []byte("audio"), started)
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}
	oldT, oldS, oldP := mobileRecordingTranscriber, mobileRecordingSaver, mobileRecordingPauses
	t.Cleanup(func() { mobileRecordingTranscriber, mobileRecordingSaver, mobileRecordingPauses = oldT, oldS, oldP })
	mobileRecordingPauses = func(*App) ([]observationPause, error) {
		return []observationPause{{Start: started.Add(-time.Hour).UnixMilli(), End: started.Add(2 * time.Hour).UnixMilli()}}, nil
	}
	mobileRecordingTranscriber = func(*App, string) ([]mobileRecordingSegment, error) {
		t.Fatal("audio from a pause was read")
		return nil, nil
	}
	app := &App{}
	app.transcribeMobileRecordings(context.Background(), store)
	if _, err := os.Stat(filepath.Join(store.dir, id, mobileRecordingVoicesFile)); err == nil {
		t.Fatal("voices were kept from an hour dropped by a pause")
	}
	if left, _ := filepath.Glob(filepath.Join(store.dir, id, "*")); len(left) != 0 {
		t.Fatalf("left in the hour's folder: %v", left)
	}
}
