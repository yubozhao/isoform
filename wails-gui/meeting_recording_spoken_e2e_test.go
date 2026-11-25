//go:build !bindings && ((darwin && (amd64 || arm64) && !ios) || (windows && (amd64 || 386)))

package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/platform/activitymon"
)

// TestMeetingRecordingSpokenFixtureEndToEnd runs the real local recognizer on
// the checked-in synthetic two-channel speech. No device, app or TTS program is
// opened. Set YANSU_MEETING_E2E_MODELS to the standard verified model folder.
func TestMeetingRecordingSpokenFixtureEndToEnd(t *testing.T) {
	models := os.Getenv("YANSU_MEETING_E2E_MODELS")
	if models == "" {
		t.Skip("set YANSU_MEETING_E2E_MODELS to sense-voice/ and silero_vad.onnx")
	}
	newEngine := func() *sherpaTranscriber {
		tr, err := newSherpaTranscriber(sherpaPaths{root: models, senseVoice: filepath.Join(models, "sense-voice", "model.int8.onnx"), tokens: filepath.Join(models, "sense-voice", "tokens.txt"), silero: filepath.Join(models, "silero_vad.onnx")}, []string{"en"}, "en")
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	live := newEngine()
	defer func() { live.Stop() }()
	t.Setenv("HOME", t.TempDir())
	if err := updateConfig(func(cfg *yansuConfig) {
		cfg.BackgroundListeningConfigured = true
		cfg.AfterCallSpeechModelOff = true
	}); err != nil {
		t.Fatal(err)
	}
	saved := meetingFollowupModel
	meetingFollowupModel = func(*App, string) (string, error) { return `{"decisions":[],"followups":[]}`, nil }
	defer func() { meetingFollowupModel = saved }()
	app := mainChatTestApp(t)
	store := newLifecycleStore(t)
	app.whisper = &whisperState{server: live}
	app.activity = &activityMonitorState{store: store, buf: activitybuf.NewRingBuffer(64, time.Hour), capture: failingScreenCapture{}}
	app.onMeetingStarted(app.activity, meetingdetect.Result{IsMeeting: true, MeetingName: "Zoom", BundleID: "us.zoom.xos"})
	writer := app.activity.meetingAudioWriter
	fixture := filepath.Join("testdata", "call_recording", "two_channel.wav")
	info, err := readMeetingWAVInfo(fixture)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	frames := int(info.dataSize) / 4
	start := time.Now().Add(-offsetOf(frames))
	app.activity.meetingStartMs = start.UnixMilli()
	capture := &backgroundAudioCapture{annotateChunk: app.annotateBackgroundAudioChunk, shouldTranscribe: app.shouldTranscribeBackgroundAudioChunk, onRecordingChunk: app.writeMeetingAudioChunk}
	chunkSamples := 5 * meetingAudioSampleRate
	for from := 0; from < frames; from += chunkSamples {
		to := min(from+chunkSamples, frames)
		for channel, source := range []backgroundListeningSource{backgroundListeningSourceMicrophone, backgroundListeningSourceSystem} {
			samples := make([]int16, to-from)
			for i := range samples {
				at := int(info.dataOffset) + (from+i)*4 + channel*2
				samples[i] = int16(binary.LittleEndian.Uint16(raw[at:]))
			}
			chunk := timedChunk(source, start.Add(offsetOf(to)), samples)
			chunk.Duration = offsetOf(to - from)
			chunk.MicHolders = micInputSnapshot{Unknown: true}
			capture.forwardChunkDownstream(chunk)
			if from == 0 && channel == 0 {
				// Rotate between sibling channels, where the old writer swap
				// used to put the two sides in different recordings.
				app.activity.buf.Push(activitymon.ActivityEvent{Kind: activitymon.EventClick, Timestamp: start.Add(offsetOf(to)).UnixMilli(), AppName: "Zoom"})
				app.extractSession(meetingSliceTrigger)
				if app.activity.meetingAudioWriter != writer {
					t.Fatal("rotation replaced the recording")
				}
			}
		}
		if from == 0 {
			live.Stop()
			live = newEngine() // actual ASR teardown/recreation mid-call
			app.whisper.mu.Lock()
			app.whisper.server = live
			app.whisper.mu.Unlock()
		}
	}
	// Use the synchronous persistence primitives so completion requires no
	// polling, remote follow-up model or application lifecycle.
	app.activity.meetingAudioWriter = nil
	app.activity.inMeeting = false
	app.meetingRecordingMu.Lock()
	session, meta, ok := app.saveCallRecordingLocked(writer, 0, "meeting_ended")
	app.meetingRecordingMu.Unlock()
	if !ok {
		t.Fatal("call was not finalized")
	}
	app.saveAutomaticMeetingTranscript(session, nil, meta.App, 0, meta.MeetingID)
	row, err := store.GetSessionByID(session.ID)
	if err != nil || row == nil || row.TranscriptPath == "" {
		t.Fatalf("transcript missing: %+v %v", row, err)
	}
	transcript, err := os.ReadFile(row.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	body := normalizeBackgroundTranscript(stripTranscriptHeader(string(transcript)))
	previous := -1
	for _, line := range []string{"Let's begin the review.", "The recorder keeps both channels.", "Please send the report today.", "I will send it after lunch."} {
		normalized := normalizeBackgroundTranscript(line)
		at := strings.Index(body, normalized)
		if strings.Count(body, normalized) != 1 || at <= previous {
			t.Fatalf("spoken line lost/repeated/out of order: %q\n%s", line, transcript)
		}
		previous = at
	}
	recorded, err := readMeetingWAVInfo(filepath.Join(store.AudioDir(), row.AudioPath))
	if err != nil || recorded.channels != 2 || recorded.dataSize != info.dataSize {
		t.Fatalf("audio truncated: %+v %v", recorded, err)
	}
	entries, err := app.GetKnowledgeEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("Library did not retain one call: %+v %v", entries, err)
	}
	if len(mainChatReports(t, app)) != 0 {
		t.Fatal("call announced itself in chat")
	}
}
