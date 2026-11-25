package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/platform/activitymon"
)

func zoomHolding() micInputSnapshot {
	return micInputSnapshot{Active: true, BundleIDs: []string{"us.zoom.xos"}}
}

func windowsLookup(titles ...string) func(meetingdetect.Result) (string, []activitymon.AppWindow) {
	return func(meetingdetect.Result) (string, []activitymon.AppWindow) {
		windows := make([]activitymon.AppWindow, 0, len(titles))
		for _, title := range titles {
			windows = append(windows, activitymon.AppWindow{Title: title})
		}
		return "zoom.us", windows
	}
}

func TestCallOnMicrophoneNeedsCallEvidence(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 14, 0, 0, 0, time.Local)

	// Zoom's audio test: the mic is held and only Settings is open. That
	// stays no call however long it lasts.
	tracker := &meetingMicHoldTracker{}
	for minute := 0; minute <= 5; minute++ {
		now := t0.Add(time.Duration(minute) * time.Minute)
		for step := 0; step < 4; step++ {
			if res := tracker.callOnMicrophone(zoomHolding(), now.Add(time.Duration(step)*15*time.Second), windowsLookup("Zoom Workplace", "Settings")); res.IsMeeting {
				t.Fatalf("a Settings window is not a call (minute %d)", minute)
			}
		}
	}

	// A call window with the hold sustained is a call.
	tracker = &meetingMicHoldTracker{}
	if res := tracker.callOnMicrophone(zoomHolding(), t0, windowsLookup("Zoom Workplace", "Zoom Meeting")); res.IsMeeting {
		t.Fatal("a call window needs the hold sustained first")
	}
	var res meetingdetect.Result
	for elapsed := 5 * time.Second; elapsed <= meetingMicHoldWithWindowMinimum+meetingCallEvidenceTTL; elapsed += 5 * time.Second {
		res = tracker.callOnMicrophone(zoomHolding(), t0.Add(elapsed), windowsLookup("Zoom Workplace", "Zoom Meeting"))
	}
	if !res.IsMeeting || res.MeetingName != "Zoom" {
		t.Fatalf("a Zoom Meeting window with a sustained hold is a call: %+v", res)
	}

	// No readable windows (Accessibility denied, or only the menu-bar
	// item): no hold is a call however long it lasts, an audio test
	// included.
	tracker = &meetingMicHoldTracker{}
	for elapsed := time.Duration(0); elapsed <= 10*time.Minute; elapsed += 5 * time.Second {
		if res := tracker.callOnMicrophone(zoomHolding(), t0.Add(elapsed), windowsLookup()); res.IsMeeting {
			t.Fatalf("a %s hold without windows is no call", elapsed)
		}
	}

	// The hold breaks: it starts over.
	if res := tracker.callOnMicrophone(micInputSnapshot{}, t0.Add(4*time.Minute), windowsLookup()); res.IsMeeting {
		t.Fatal("nobody holds the mic")
	}
	if res := tracker.callOnMicrophone(zoomHolding(), t0.Add(4*time.Minute+5*time.Second), windowsLookup()); res.IsMeeting {
		t.Fatal("a new hold starts from zero")
	}
}

func TestMeetingAudioWriterFillsLongGapsSparsely(t *testing.T) {
	writer, err := newMeetingAudioWriter(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Abort()
	t0 := time.Date(2026, 10, 15, 14, 0, 0, 0, time.Local)
	second := meetingAudioSampleRate
	writer.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, t0.Add(5*time.Second), constSamples(5*second, 1)))
	// The screen was locked for 45 minutes mid-call.
	resume := t0.Add(5*time.Second + 45*time.Minute)
	writer.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, resume.Add(5*time.Second), constSamples(5*second, 1)))

	want := int64((45*time.Minute + 10*time.Second) / time.Second * meetingAudioBytesPerSecond)
	if writer.micBytes != want {
		t.Fatalf("track length = %d bytes, want %d (the gap filled)", writer.micBytes, want)
	}
	info, err := os.Stat(writer.micFile.Name())
	if err != nil || info.Size() != want {
		t.Fatalf("track file size = %v %v, want %d", info.Size(), err, want)
	}
}

func TestMeetingRecordingChunkSilencesProtectedApps(t *testing.T) {
	chunk := capturedAudioChunk{
		Source:      backgroundListeningSourceMicrophone,
		WavData:     makeWAV(constSamples(100, 500)),
		CapturedApp: activitymon.AppInfo{BundleID: "com.1password.1password"},
	}
	kept := meetingRecordingChunk(chunk)
	if len(kept.WavData) != len(chunk.WavData) || wavChunkRMS(kept.WavData) != 0 {
		t.Fatalf("protected-app audio must become silence of the same length: rms=%v", wavChunkRMS(kept.WavData))
	}
	if wavChunkRMS(chunk.WavData) == 0 {
		t.Fatal("the original chunk must stay untouched for the live path")
	}
	chunk.CapturedApp.BundleID = "us.zoom.xos"
	if got := meetingRecordingChunk(chunk); wavChunkRMS(got.WavData) == 0 {
		t.Fatal("ordinary audio must be kept")
	}
}

func TestFinalizeMeetingTracksKeepsTracksWhenTheWAVFails(t *testing.T) {
	dir := t.TempDir()
	mic := filepath.Join(dir, "1-a.mic.pcm")
	sys := filepath.Join(dir, "1-a.sys.pcm")
	for _, p := range []string{mic, sys} {
		if err := os.WriteFile(p, make([]byte, 64), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := finalizeMeetingTracks(dir, "missing-day", "1-a", mic, sys, 64, 64); err == nil {
		t.Fatal("the WAV cannot be created in a missing folder")
	}
	for _, p := range []string{mic, sys} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("track %s must stay for the next start: %v", filepath.Base(p), err)
		}
	}
}

func newLifecycleStore(t *testing.T) *ActivityStore {
	t.Helper()
	// These fixtures supply their speech engine. A real model download from
	// a synctest bubble can leave net's global thread limiter tied to that
	// bubble and fatally break a later test's DNS call.
	wasOff := loadConfig().AfterCallSpeechModelOff
	if err := updateConfig(func(cfg *yansuConfig) { cfg.AfterCallSpeechModelOff = true }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := updateConfig(func(cfg *yansuConfig) { cfg.AfterCallSpeechModelOff = wasOff }); err != nil {
			t.Errorf("restore after-call model setting: %v", err)
		}
	})
	store, err := NewActivityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func meetingSessions(t *testing.T, store *ActivityStore) []ActivitySessionRow {
	t.Helper()
	sessions, err := store.GetSessionsByAge()
	if err != nil {
		t.Fatal(err)
	}
	var out []ActivitySessionRow
	for _, s := range sessions {
		if s.IsMeeting {
			out = append(out, s)
		}
	}
	return out
}

func TestMeetingEndWithNoEventsKeepsTheRecording(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newLifecycleStore(t)
	writer, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMeeting("Zoom", "us.zoom.xos", "meeting-1")
	writer.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(meetingAudioSampleRate, 10)))
	app := &App{
		activity: &activityMonitorState{
			buf:                activitybuf.NewRingBuffer(16, time.Minute),
			store:              store,
			inMeeting:          true,
			meetingAppName:     "Zoom",
			meetingBundleID:    "us.zoom.xos",
			meetingID:          "meeting-1",
			meetingAudioWriter: writer,
		},
	}

	app.extractSession("meeting_ended")

	if app.activity.meetingAudioWriter != nil || app.activity.inMeeting {
		t.Fatal("the ended call's recording must be detached")
	}
	sessions := meetingSessions(t, store)
	if len(sessions) != 1 || sessions[0].AudioPath == "" || sessions[0].MeetingApp != "Zoom" {
		t.Fatalf("a call with no events keeps its recording as a session: %+v", sessions)
	}
	if _, err := os.Stat(filepath.Join(store.AudioDir(), sessions[0].AudioPath)); err != nil {
		t.Fatalf("recording missing: %v", err)
	}
}

func TestMeetingSliceKeepsTheCallAndContinuesTheRecording(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newLifecycleStore(t)
	writer, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMeeting("Zoom", "us.zoom.xos", "meeting-2")
	writer.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(meetingAudioSampleRate, 10)))
	app := &App{
		activity: &activityMonitorState{
			buf:                activitybuf.NewRingBuffer(16, time.Minute),
			store:              store,
			inMeeting:          true,
			meetingAppName:     "Zoom",
			meetingBundleID:    "us.zoom.xos",
			meetingID:          "meeting-2",
			meetingStartMs:     time.Now().Add(-2 * time.Hour).UnixMilli(),
			meetingAudioWriter: writer,
			capture:            failingScreenCapture{},
		},
	}
	app.activity.buf.Push(activitymon.ActivityEvent{
		Timestamp:    time.Now().UnixMilli(),
		Kind:         activitymon.EventSpeech,
		AppName:      "Zoom",
		AppBundleID:  "us.zoom.xos",
		SpeechText:   "still talking two hours in",
		SpeechSource: "microphone",
	})
	sliceStart := time.Now().UnixMilli()

	app.extractSession(meetingSliceTrigger)

	am := app.activity
	if !am.inMeeting || am.meetingID != "meeting-2" || am.meetingAppName != "Zoom" {
		t.Fatalf("a 2-hour slice must not end the call: inMeeting=%v id=%q", am.inMeeting, am.meetingID)
	}
	if am.meetingStartMs < sliceStart {
		t.Fatal("activity rotation must advance its own boundary without changing the writer")
	}
	if am.meetingAudioWriter != writer {
		t.Fatal("activity rotation must retain the call's original recording")
	}
	am.meetingAudioWriter.Abort()
	sessions := meetingSessions(t, store)
	if len(sessions) != 1 || sessions[0].AudioPath != "" {
		t.Fatalf("activity slice must not publish a partial call recording: %+v", sessions)
	}
}

func TestRecoverMeetingAudioTracksFinalizesCrashLeftovers(t *testing.T) {
	store := newLifecycleStore(t)
	audioDir := filepath.Join(t.TempDir(), "audio")
	day := filepath.Join(audioDir, "2026-10-15")
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	prefix := "1792000000000-deadbeef"
	mic := make([]byte, 2*meetingAudioBytesPerSecond)
	for i := range mic {
		mic[i] = 1
	}
	for name, data := range map[string][]byte{
		prefix + ".mic.pcm": mic,
		prefix + ".sys.pcm": make([]byte, meetingAudioBytesPerSecond),
	} {
		if err := os.WriteFile(filepath.Join(day, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	meta, _ := json.Marshal(meetingAudioMeta{App: "Zoom", BundleID: "us.zoom.xos", MeetingID: "m-9", StartMs: 1792000001000})
	if err := os.WriteFile(filepath.Join(day, prefix+".json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for _, name := range []string{".mic.pcm", ".sys.pcm"} {
		_ = os.Chtimes(filepath.Join(day, prefix+name), old, old)
	}

	// A second store view over the same database with its own audio folder.
	view := &ActivityStore{db: store.db, audioDir: audioDir}
	view.recoverMeetingAudioTracks(time.Now(), false)

	for _, name := range []string{".mic.pcm", ".sys.pcm", ".json"} {
		if _, err := os.Stat(filepath.Join(day, prefix+name)); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone after a clean finalize: %v", name, err)
		}
	}
	sessions := meetingSessions(t, store)
	if len(sessions) != 1 || sessions[0].MeetingApp != "Zoom" || sessions[0].StartTime != 1792000001000 || sessions[0].TriggerKind != recoveredRecordingTrigger {
		t.Fatalf("recovered session = %+v", sessions)
	}
	info, err := readMeetingWAVInfo(filepath.Join(audioDir, sessions[0].AudioPath))
	if err != nil || info.channels != 2 {
		t.Fatalf("recovered WAV = %+v %v", info, err)
	}
	if sessions[0].CreatedAt >= time.Now().UnixMilli() {
		t.Fatal("the row must predate this run so transcript recovery picks it up")
	}
	if meta := readMeetingWAVMeta(audioDir, sessions[0].AudioPath); meta.MeetingID != "m-9" || meta.App != "Zoom" {
		t.Fatalf("the recording keeps its sidecar, so its notes are keyed on the meeting: %+v", meta)
	}

	// A recording of this run is left alone.
	fresh := "1792000009000-cafecafe"
	if err := os.WriteFile(filepath.Join(day, fresh+".mic.pcm"), mic, 0o600); err != nil {
		t.Fatal(err)
	}
	view.recoverMeetingAudioTracks(time.Now().Add(-time.Minute), false)
	if _, err := os.Stat(filepath.Join(day, fresh+".mic.pcm")); err != nil {
		t.Fatal("a recording in progress must not be touched")
	}
}

func TestTranscriptMeetingIDRoundTrip(t *testing.T) {
	md, _ := buildMeetingTranscriptMarkdown("Zoom", ActivitySessionRow{ID: "session-123456"}, nil, nil)
	with := withTranscriptMeetingID(md, "meeting-xyz")
	if got := parseTranscriptMeetingID(with); got != "meeting-xyz" {
		t.Fatalf("meeting id = %q", got)
	}
	if session, _, _, _ := parseTranscriptMetadata(with); session != "session-123456" {
		t.Fatalf("session id = %q", session)
	}
	if parseTranscriptMeetingID(md) != "" || withTranscriptMeetingID(md, " ") != md {
		t.Fatal("no meeting id, no line")
	}
}

func TestOrphanRecoveryUsesTheMeetingKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	store := newLifecycleStore(t)
	app.activity = &activityMonitorState{store: store}
	stubMeetingNotesLearn(t)
	start := time.Now().Add(-time.Hour)
	session := ActivitySessionRow{ID: "slice-aaaa-1111", StartTime: start.UnixMilli(), EndTime: start.Add(10 * time.Minute).UnixMilli(), AppNames: "[]", TriageStatus: "dismissed", IsMeeting: true, MeetingApp: "Zoom", CreatedAt: start.UnixMilli()}
	if err := store.SaveSession(session, nil, nil); err != nil {
		t.Fatal(err)
	}
	md, _ := buildMeetingTranscriptMarkdown("Zoom", session, []meetingUtterance{{At: start, End: start.Add(5 * time.Second), Text: "Let's begin the review."}}, nil)
	md = withTranscriptMeetingID(md, "meeting-K")
	// Another part of the same call is already on Activity.
	earlier := ActivitySessionRow{ID: "slice-zzzz-0000", StartTime: start.Add(-20 * time.Minute).UnixMilli(), EndTime: start.Add(-15 * time.Minute).UnixMilli(), AppNames: "[]", TriageStatus: "dismissed", IsMeeting: true, MeetingApp: "Zoom", CreatedAt: start.UnixMilli()}
	if err := store.SaveSession(earlier, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.recordMeetingNotes(earlier, "Zoom", 4, "meeting-K", "", nil); err != nil {
		t.Fatal(err)
	}

	dir, err := transcriptDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, start.Format("2006-01-02")+"-zoom-"+session.ID[:8]+".md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	app.recoverOrphanTranscripts(t.Context())

	if keys, err := store.recentMeetingNotes(0); err != nil || len(keys) != 1 || keys[0] != "meeting-K" {
		t.Fatalf("recovery must not add a second meeting keyed by the session: %v %v", keys, err)
	}
	if r, _, _ := store.meetingNotesByKey("meeting-K"); len(r.Slices) != 2 || r.Slices[1].SessionID != session.ID || r.Slices[1].StartTime != session.StartTime {
		t.Fatalf("the recovered part joins its meeting with its exact span: %+v", r.Slices)
	}
	if entries, _ := app.GetKnowledgeEntries(); len(entries) != 0 {
		t.Fatalf("recovery wrote Knowledge: %d entries", len(entries))
	}
}

func TestDeletingMeetingNotesDeletesTheCall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	store := newLifecycleStore(t)
	app.activity = &activityMonitorState{store: store}

	day := filepath.Join(store.AudioDir(), "2026-10-15")
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	wavRel := filepath.Join("2026-10-15", "1792000000000-abcd1234.wav")
	if err := os.WriteFile(filepath.Join(store.AudioDir(), wavRel), []byte("RIFF"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(t.TempDir(), "transcript.md")
	if err := os.WriteFile(transcript, []byte("# Meeting"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Hour)
	session := ActivitySessionRow{ID: "call-bbbb-2222", StartTime: start.UnixMilli(), EndTime: start.Add(time.Minute).UnixMilli(), AppNames: "[]", TriageStatus: "dismissed", IsMeeting: true, MeetingApp: "Zoom", TranscriptPath: transcript, AudioPath: wavRel, CreatedAt: start.UnixMilli()}
	if err := store.SaveSession(session, nil, nil); err != nil {
		t.Fatal(err)
	}
	stubMeetingNotesLearn(t)
	key, err := app.recordMeetingNotes(session, "Zoom", 1, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := app.DeleteMeetingNotes(key); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(store.AudioDir(), wavRel), transcript} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s must be deleted with the notes: %v", filepath.Base(p), err)
		}
	}
	row, err := store.GetSessionByID(session.ID)
	if err != nil || row == nil || row.AudioPath != "" || row.TranscriptPath != "" {
		t.Fatalf("session still points at the call: %+v %v", row, err)
	}
	if !meetingNotesPublished(session.ID) {
		t.Fatal("the publish marker must stay so nothing writes the notes again")
	}
}

func TestMeetingTranscriptAllowedFollowsTheSettings(t *testing.T) {
	app := &App{}
	epoch, _ := app.automaticMeetingRecordingEpoch()
	session := ActivitySessionRow{ID: "s"}
	if !app.meetingTranscriptAllowed(session, &epoch) {
		t.Fatal("recording on: allowed")
	}
	session.TriageStatus = "protected"
	if app.meetingTranscriptAllowed(session, &epoch) {
		t.Fatal("a protected session is never written up")
	}
	app.meetingRecordingState.Store(meetingRecordingOff)
	if app.meetingTranscriptAllowed(ActivitySessionRow{ID: "s"}, &epoch) {
		t.Fatal("recording turned off: nothing more is written")
	}
}

func TestRecoverUntranscribedMeetingsWaitsForRecordingToBeOn(t *testing.T) {
	meetingTranscriptRecoveryMu.Lock()
	meetingTranscriptRecoveryDone = false
	meetingTranscriptRecoveryMu.Unlock()
	t.Cleanup(func() {
		meetingTranscriptRecoveryMu.Lock()
		meetingTranscriptRecoveryDone = false
		meetingTranscriptRecoveryMu.Unlock()
	})
	app := &App{activity: &activityMonitorState{store: newLifecycleStore(t)}}
	app.meetingRecordingState.Store(meetingRecordingOff)
	app.recoverUntranscribedMeetings()
	meetingTranscriptRecoveryMu.Lock()
	done := meetingTranscriptRecoveryDone
	meetingTranscriptRecoveryMu.Unlock()
	if done {
		t.Fatal("with recording off the one recovery pass must not be used up")
	}
}

// failingScreenCapture stands in for the screen: no screenshot this time.
type failingScreenCapture struct{}

func (failingScreenCapture) CaptureRaw() ([]byte, int, int, error) {
	return nil, 0, 0, os.ErrNotExist
}

func (failingScreenCapture) EncodeJPEG([]byte, int, int, int) ([]byte, error) {
	return nil, os.ErrNotExist
}

// callHarness drives the real capture path (annotate, the transcription
// gate, the recording writer, the transcription scheduler) and the meeting
// watcher's polls on simulated time, with the system lookups (the app in
// front, running apps, their windows, who holds the microphone) stood in
// for.
type callHarness struct {
	t       *testing.T
	app     *App
	store   *ActivityStore
	engine  *fakeTranscriber
	capture *backgroundAudioCapture
	watcher meetingWatcherState
	now     time.Time
	// clock is the time simulated so far; systemOffset shifts the system
	// audio chunks against the microphone's (the recorders run apart).
	clock        time.Duration
	systemOffset time.Duration

	// injected is every transcript handed to the activity, with its
	// capture context.
	injectedMu sync.Mutex
	injected   []speechCaptureContext

	front   activitymon.AppInfo
	holders micInputSnapshot
	// url is the address of the front browser tab; urlReads counts how
	// often it was read.
	url      string
	urlReads int
	apps     []activitymon.RunningApp
	// windows lists each running app's window titles; nil when
	// Accessibility is denied.
	windows map[string][]string
}

var (
	zoomApp   = activitymon.RunningApp{Name: "zoom.us", BundleID: "us.zoom.xos"}
	chromeApp = activitymon.RunningApp{Name: "Google Chrome", BundleID: "com.google.Chrome"}
)

func holding(bundleIDs ...string) micInputSnapshot {
	return micInputSnapshot{Active: len(bundleIDs) > 0, BundleIDs: bundleIDs}
}

func newCallHarness(t *testing.T) *callHarness {
	t.Setenv("HOME", t.TempDir())
	// This fixture explicitly exercises enabled listening, independent of
	// whether another fixture setting has already materialized the config.
	if err := updateConfig(func(cfg *yansuConfig) {
		cfg.BackgroundListeningConfigured = true
		cfg.BackgroundListeningDisabled = false
	}); err != nil {
		t.Fatal(err)
	}
	h := &callHarness{t: t, store: newLifecycleStore(t), engine: &fakeTranscriber{}, windows: map[string][]string{}}
	h.app = mainChatTestApp(t)
	// Finished calls now require a successful full-recording decode; live
	// speech alone must never stand in for audio the engine has not read.
	h.app.whisper = &whisperState{server: &recordingFixtureEngine{fakeLongForm: fakeLongForm{texts: map[int16]string{2000: "hello world"}}}}
	h.app.activity = &activityMonitorState{
		buf:     activitybuf.NewRingBuffer(1024, 2*time.Hour),
		store:   h.store,
		capture: failingScreenCapture{},
	}
	h.capture = &backgroundAudioCapture{
		shouldTranscribe: h.app.shouldTranscribeBackgroundAudioChunk,
		annotateChunk:    h.app.annotateBackgroundAudioChunk,
		onChunk:          h.app.writeMeetingAudioChunk,
	}
	h.capture.scheduler = newBackgroundTranscriptionScheduler(h.engine, func(text string, source backgroundListeningSource, ctx speechCaptureContext) {
		h.injectedMu.Lock()
		h.injected = append(h.injected, ctx)
		h.injectedMu.Unlock()
		h.app.injectSpeechEvent(text, source, ctx)
	})
	h.capture.scheduler.start()
	h.now = time.Now().Add(-45 * time.Minute)

	savedProbes, savedHolds := meetingProbes, meetingMicHolds
	meetingMicHolds = &meetingMicHoldTracker{}
	browserMeetings.mu.Lock()
	browserMeetings.last = map[string]browserMeetingLookup{}
	browserMeetings.mu.Unlock()
	meetingProbes.browserURL = func(string) activitymon.BrowserURLRead {
		h.injectedMu.Lock()
		h.urlReads++
		h.injectedMu.Unlock()
		return activitymon.BrowserURLRead{URL: h.url}
	}
	meetingProbes.frontmost = func() activitymon.AppInfo { return h.front }
	meetingProbes.runningApps = func() []activitymon.RunningApp { return h.apps }
	meetingProbes.appWindows = func(name string) []activitymon.AppWindow {
		var windows []activitymon.AppWindow
		for _, title := range h.windows[name] {
			windows = append(windows, activitymon.AppWindow{Title: title})
		}
		return windows
	}
	meetingProbes.micHolders = func() micInputSnapshot { return h.holders }
	t.Cleanup(func() {
		h.stopTimers()
		browserMeetings.pending.Wait()
		meetingProbes, meetingMicHolds = savedProbes, savedHolds
	})
	return h
}

// run feeds d of 5-second chunks from both sources through the capture path
// and polls the watcher every 15 seconds, as the app does. each, when set,
// changes the scene before a chunk.
// harnessStep is the resolution of the simulated time.
const harnessStep = 500 * time.Millisecond

func (h *callHarness) run(d time.Duration, each func(elapsed time.Duration)) {
	h.t.Helper()
	for elapsed := time.Duration(0); elapsed < d; elapsed += harnessStep {
		browserMeetings.pending.Wait()
		if each != nil {
			each(elapsed)
		}
		if h.clock%captureSampleInterval == 0 {
			h.app.sampleCaptureState(h.now)
		}
		if h.clock%backgroundAudioChunkDuration == 0 {
			h.feed(backgroundListeningSourceMicrophone)
		}
		if (h.clock+h.systemOffset)%backgroundAudioChunkDuration == 0 {
			h.feed(backgroundListeningSourceSystem)
		}
		if h.clock%meetingPollInterval == 10*time.Second {
			h.app.pollMeetingLifecycle(h.app.activity, &h.watcher, h.now, func() bool { return false })
		}
		h.now = h.now.Add(harnessStep)
		h.clock += harnessStep
	}
	browserMeetings.pending.Wait()
}

// feed sends one chunk ending now through the capture path.
func (h *callHarness) feed(source backgroundListeningSource) {
	chunk := capturedAudioChunk{
		Source:      source,
		WavData:     headedWAV(constSamples(meetingAudioSampleRate, 2000)),
		Duration:    backgroundAudioChunkDuration,
		CapturedAt:  h.now,
		CapturedApp: h.front,
	}
	chunk = withMicHolders(chunk, h.holders, h.now)
	if source == backgroundListeningSourceMicrophone {
		chunk.OtherMicUser = h.holders.Active
	}
	h.capture.forwardChunkDownstream(chunk)
}

func (h *callHarness) state() (inMeeting, candidate bool, app string) {
	h.app.activity.mu.Lock()
	defer h.app.activity.mu.Unlock()
	return h.app.activity.inMeeting, h.app.activity.meetingCandidateActive, h.app.activity.meetingAppName
}

func (h *callHarness) requireNoCall(when string) {
	h.t.Helper()
	h.app.activity.mu.Lock()
	inMeeting, candidate := h.app.activity.inMeeting, h.app.activity.meetingCandidateActive
	writer, candidateWriter := h.app.activity.meetingAudioWriter, h.app.activity.meetingCandidateAudioWriter
	h.app.activity.mu.Unlock()
	if inMeeting || candidate || writer != nil || candidateWriter != nil {
		h.t.Fatalf("%s: no call may start: inMeeting=%v candidate=%v writer=%v candidateWriter=%v", when, inMeeting, candidate, writer != nil, candidateWriter != nil)
	}
}

func (h *callHarness) stopTimers() {
	h.app.activity.mu.Lock()
	defer h.app.activity.mu.Unlock()
	if h.app.activity.idleTimer != nil {
		h.app.activity.idleTimer.Stop()
	}
	if h.app.activity.heartbeatTimer != nil {
		h.app.activity.heartbeatTimer.Stop()
	}
}

// finish drains transcription and ends the activity session.
func (h *callHarness) finish() []activitymon.ActivityEvent {
	h.capture.scheduler.stop()
	speech := h.app.activity.buf.Snapshot()
	h.app.extractSession("idle_transition")
	h.stopTimers()
	return speech
}

// requireOnlyDictation checks the end state of a scene with no call: the
// speech is kept as ordinary activity, never call speech, and nothing of a
// call is saved: no meeting session, no audio, no transcript, no Knowledge
// entry.
func (h *callHarness) requireOnlyDictation(speech []activitymon.ActivityEvent) {
	h.t.Helper()
	if h.engine.seenBlobs() == 0 {
		h.t.Fatal("the microphone is turned into text all day")
	}
	if len(speech) == 0 {
		h.t.Fatal("the speech must be kept as activity")
	}
	for _, evt := range speech {
		if evt.Kind != activitymon.EventSpeech {
			continue
		}
		if evt.SpeechInCall || evt.MeetingCandidate {
			h.t.Fatalf("speech outside a call is never call speech: %+v", evt)
		}
		if evt.SpeechSource != string(backgroundListeningSourceMicrophone) {
			h.t.Fatalf("the computer's own sound is off outside calls, got %s", evt.SpeechSource)
		}
	}
	if sessions := meetingSessions(h.t, h.store); len(sessions) != 0 {
		h.t.Fatalf("no meeting session may be saved: %+v", sessions)
	}
	all, err := h.store.GetSessionsByAge()
	if err != nil || len(all) == 0 {
		h.t.Fatalf("the speech is saved as ordinary activity: %d %v", len(all), err)
	}
	if files, _ := filepath.Glob(filepath.Join(h.store.AudioDir(), "*", "*")); len(files) != 0 {
		h.t.Fatalf("no call audio may be kept: %v", files)
	}
	dir, err := transcriptDir()
	if err != nil {
		h.t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.md")); len(files) != 0 {
		h.t.Fatalf("no transcript may be written: %v", files)
	}
	entries, err := h.app.GetKnowledgeEntries()
	if err != nil {
		h.t.Fatal(err)
	}
	if len(entries) != 0 {
		h.t.Fatalf("no meeting notes may reach Knowledge: %d entries", len(entries))
	}
	if keys, err := h.store.recentMeetingNotes(0); err != nil || len(keys) != 0 {
		h.t.Fatalf("no meeting notes may be written: %v %v", keys, err)
	}
}

// TestAcceptanceIdleZoomWhileDictatingIsNoCall is the user's own setup on
// 2026-10-15 and its neighbours. Zoom running and idle, its main window open
// but no meeting, while the user dictates into Claude, Codex and ChatGPT,
// which hold the microphone; Zoom in front for five minutes, its audio test
// included; a ChatGPT tab in Chrome titled "Microsoft Teams" taking
// dictation; Chrome dictation after a Google Meet call in Chrome ended; and
// Zoom on the microphone with Accessibility denied. None of it is a call: no
// meeting detection, no meeting audio, no transcript, no meeting notes, no
// Knowledge entry; the Meet call itself is recorded and ends when Chrome lets
// go of the microphone. The watcher's real polls run on every scene.
func TestAcceptanceIdleZoomWhileDictatingIsNoCall(t *testing.T) {
	dictation := []activitymon.AppInfo{
		{BundleID: "com.anthropic.claudefordesktop", AppName: "Claude", WindowTitle: "Claude"},
		{BundleID: "com.openai.codex", AppName: "Codex", WindowTitle: "Codex"},
		{BundleID: "com.openai.chat", AppName: "ChatGPT", WindowTitle: "ChatGPT"},
	}
	zoomIdle := activitymon.AppInfo{BundleID: "us.zoom.xos", AppName: "zoom.us", WindowTitle: "Zoom Workplace"}

	t.Run("dictating into Claude, Codex and ChatGPT while Zoom idles", func(t *testing.T) {
		h := newCallHarness(t)
		h.apps = []activitymon.RunningApp{zoomApp}
		h.windows["zoom.us"] = []string{"Zoom Workplace"}
		h.run(10*time.Minute, func(elapsed time.Duration) {
			app := dictation[int(elapsed/(100*time.Second))%len(dictation)]
			h.front, h.holders = app, holding(app.BundleID)
			if elapsed >= 5*time.Minute && elapsed < 6*time.Minute+40*time.Second {
				h.front = zoomIdle // Zoom in front while the dictation goes on.
			}
		})
		h.requireNoCall("dictation")
		speech := h.finish()
		h.requireOnlyDictation(speech)
		for _, evt := range speech {
			if evt.Kind == activitymon.EventSpeech && evt.SpeechDictationApp == "" {
				t.Fatalf("dictation must say which app it went to: %+v", evt)
			}
		}
	})

	t.Run("idle Zoom in front for five minutes, its audio test included", func(t *testing.T) {
		h := newCallHarness(t)
		h.apps = []activitymon.RunningApp{zoomApp}
		h.front = zoomIdle
		h.run(5*time.Minute, func(elapsed time.Duration) {
			h.windows["zoom.us"] = []string{"Zoom Workplace"}
			h.holders = holding(dictation[0].BundleID) // talking to Claude
			if elapsed >= 2*time.Minute && elapsed < 4*time.Minute {
				// Zoom's own audio test holds the microphone from Settings.
				h.windows["zoom.us"] = []string{"Zoom Workplace", "Settings"}
				h.holders = holding("us.zoom.xos")
			}
			if elapsed >= 4*time.Minute {
				h.holders = holding()
			}
		})
		h.requireNoCall("idle Zoom in front")
		h.requireOnlyDictation(h.finish())
	})

	t.Run("a ChatGPT tab titled Microsoft Teams taking dictation", func(t *testing.T) {
		h := newCallHarness(t)
		h.apps = []activitymon.RunningApp{chromeApp}
		h.windows["Google Chrome"] = []string{"Microsoft Teams"}
		h.front = activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "Microsoft Teams"}
		h.url = "https://chatgpt.com/c/68f0a1b2"
		h.holders = holding("com.google.Chrome.helper")
		h.app.handleActivityEvent(activitymon.ActivityEvent{
			Timestamp: h.now.UnixMilli(), Kind: activitymon.EventClick,
			AppBundleID: h.front.BundleID, AppName: h.front.AppName, WindowTitle: h.front.WindowTitle,
		})
		h.run(5*time.Minute, nil)
		h.requireNoCall("ChatGPT tab titled Microsoft Teams")
		speech := h.finish()
		h.requireOnlyDictation(speech)
		for _, evt := range speech {
			if evt.Kind == activitymon.EventSpeech && evt.SpeechDictationApp != "Google Chrome" {
				t.Fatalf("dictation into the browser is labeled with it: %+v", evt)
			}
		}
	})

	// A chat page can carry any title: only the tab's address says call.
	for _, scene := range []struct{ name, title, url string }{
		{"a ChatGPT chat titled Zoom Meeting prep taking dictation", "Zoom Meeting prep", "https://chatgpt.com/c/68f0-prep"},
		{"a Teams web Chat page taking dictation", "Chat | Microsoft Teams", "https://teams.microsoft.com/v2/#/conversations/19:abc"},
	} {
		t.Run(scene.name, func(t *testing.T) {
			h := newCallHarness(t)
			h.apps = []activitymon.RunningApp{chromeApp}
			h.windows["Google Chrome"] = []string{scene.title}
			h.front = activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: scene.title}
			h.url = scene.url
			h.holders = holding("com.google.Chrome.helper")
			h.app.handleActivityEvent(activitymon.ActivityEvent{
				Timestamp: h.now.UnixMilli(), Kind: activitymon.EventClick,
				AppBundleID: h.front.BundleID, AppName: h.front.AppName, WindowTitle: h.front.WindowTitle,
			})
			h.run(5*time.Minute, nil)
			h.requireNoCall(scene.name)
			h.requireOnlyDictation(h.finish())
		})
	}

	// Dictation half a minute after the hangup, and only ten seconds after:
	// the call page keeps its address after a hangup, so it is Chrome
	// letting go of the microphone that ends the call.
	for _, scene := range []struct {
		name string
		gap  time.Duration
	}{
		{"Chrome dictation after a Meet call ends", 30 * time.Second},
		{"Chrome dictation ten seconds after a Meet hangup", 10 * time.Second},
	} {
		scene := scene
		t.Run(scene.name, func(t *testing.T) {
			h := newCallHarness(t)
			h.apps = []activitymon.RunningApp{chromeApp}
			meet := activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "Meet - abc-defg-hij"}
			chat := activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "ChatGPT"}

			// The call: the Meet page in front, Chrome on the microphone.
			h.front, h.holders = meet, holding("com.google.Chrome.helper")
			h.url = "https://meet.google.com/abc-defg-hij"
			h.windows["Google Chrome"] = []string{"Meet - abc-defg-hij"}
			h.run(3*time.Minute, nil)
			if inMeeting, _, app := h.state(); !inMeeting || app != "Google Meet" {
				t.Fatalf("the Meet call is a call: inMeeting=%v app=%q", inMeeting, app)
			}

			// The call ends: Chrome lets go of the microphone; the Meet tab is
			// left open behind a ChatGPT window.
			h.front, h.holders = chat, holding()
			h.url = "https://chatgpt.com/"
			h.windows["Google Chrome"] = []string{"ChatGPT", "Meet - abc-defg-hij"}
			h.run(scene.gap, nil)
			dictationStart := h.now

			// Dictation into ChatGPT in Chrome: Chrome holds the microphone again.
			h.holders = holding("com.google.Chrome.helper")
			h.run(5*time.Minute, nil)
			h.requireNoCall("Chrome dictation after the Meet call")

			speech := h.finish()
			inCall := 0
			for _, ctx := range h.injected {
				callSpeech := ctx.InMeeting || ctx.MeetingCandidate
				if !ctx.CapturedAt.Before(dictationStart) && callSpeech {
					t.Fatalf("dictation after the call is never call speech: captured %s after the call", ctx.CapturedAt.Sub(dictationStart))
				}
				if ctx.CapturedAt.Before(dictationStart) && callSpeech {
					inCall++
				}
			}
			if inCall == 0 {
				t.Fatal("the Meet call's own speech is call speech")
			}
			for _, evt := range speech {
				if evt.Kind == activitymon.EventSpeech && evt.Timestamp >= dictationStart.UnixMilli() &&
					(evt.SpeechInCall || evt.SpeechSource == string(backgroundListeningSourceMicrophone) && evt.SpeechDictationApp == "") {
					t.Fatalf("the dictation is labeled with the app it went to, never the call: %+v", evt)
				}
			}
			sessions := meetingSessions(t, h.store)
			if len(sessions) != 1 || sessions[0].MeetingApp != "Google Meet" || sessions[0].AudioPath == "" {
				t.Fatalf("the Meet call is one meeting session with its recording: %+v", sessions)
			}
			// The recording holds the call and the moments after it, never the
			// dictation.
			info, err := os.Stat(filepath.Join(h.store.AudioDir(), sessions[0].AudioPath))
			if err != nil {
				t.Fatal(err)
			}
			recorded := time.Duration(info.Size()-44) * time.Second / time.Duration(meetingAudioSampleRate*2*2)
			if limit := 3*time.Minute + meetingMicHoldGap + backgroundAudioChunkDuration; recorded > limit {
				t.Fatalf("the recording runs %s, past the call (%s)", recorded, limit)
			}
			h.waitForTranscript()
		})
	}

	t.Run("an incognito Meet call is never read or recorded", func(t *testing.T) {
		h := newCallHarness(t)
		h.apps = []activitymon.RunningApp{chromeApp}
		title := "Meet - abc-defg-hij - Google Chrome - Incognito"
		h.windows["Google Chrome"] = []string{title}
		h.front = activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: title}
		h.url = "https://meet.google.com/abc-defg-hij"
		h.holders = holding("com.google.Chrome.helper")
		h.run(3*time.Minute, nil)
		h.requireNoCall("an incognito Meet call")
		if h.urlReads != 0 {
			t.Fatalf("a private window's address is never read: %d reads", h.urlReads)
		}
		speech := h.finish()
		if h.engine.seenBlobs() != 0 {
			t.Fatal("speech in a private window never reaches speech recognition: no words logged, no voice print")
		}
		for _, evt := range speech {
			if evt.Kind == activitymon.EventSpeech {
				t.Fatalf("nothing said in a private window is kept: %+v", evt)
			}
		}
		if sessions := meetingSessions(t, h.store); len(sessions) != 0 {
			t.Fatalf("no meeting session: %+v", sessions)
		}
		if files, _ := filepath.Glob(filepath.Join(h.store.AudioDir(), "*", "*")); len(files) != 0 {
			t.Fatalf("no call audio: %v", files)
		}
	})

	t.Run("a Safari private window on a Meet call is never read or recorded", func(t *testing.T) {
		h := newCallHarness(t)
		safari := activitymon.RunningApp{Name: "Safari", BundleID: "com.apple.Safari"}
		h.apps = []activitymon.RunningApp{safari}
		// Safari titles a private window with the page title alone.
		h.windows["Safari"] = []string{"Meet - abc-defg-hij"}
		h.front = activitymon.AppInfo{BundleID: "com.apple.Safari", AppName: "Safari", WindowTitle: "Meet - abc-defg-hij"}
		h.url = "https://meet.google.com/abc-defg-hij"
		h.holders = holding("com.apple.WebKit.GPU")
		h.run(3*time.Minute, nil)
		h.requireNoCall("a Meet call in Safari")
		if h.urlReads != 0 {
			t.Fatalf("Safari's tabs are never read: %d reads", h.urlReads)
		}
		h.finish()
		if sessions := meetingSessions(t, h.store); len(sessions) != 0 {
			t.Fatalf("no meeting session: %+v", sessions)
		}
		if files, _ := filepath.Glob(filepath.Join(h.store.AudioDir(), "*", "*")); len(files) != 0 {
			t.Fatalf("no call audio: %v", files)
		}
	})

	t.Run("Chrome dictation three seconds after a Meet hangup, the recorders 2.5 s apart", func(t *testing.T) {
		h := newCallHarness(t)
		h.systemOffset = 2500 * time.Millisecond
		h.apps = []activitymon.RunningApp{chromeApp}
		meet := activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "Meet - abc-defg-hij"}
		chat := activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "ChatGPT"}
		h.front, h.holders, h.url = meet, holding("com.google.Chrome.helper"), "https://meet.google.com/abc-defg-hij"
		h.windows["Google Chrome"] = []string{"Meet - abc-defg-hij"}
		h.run(3*time.Minute+500*time.Millisecond, nil)
		if inMeeting, _, _ := h.state(); !inMeeting {
			t.Fatal("the Meet call is a call")
		}
		// The hangup, half a second after a chunk: before the next chunk
		// of either recorder sees it, dictation in Chrome takes the
		// microphone again three seconds later.
		h.front, h.holders, h.url = chat, holding(), "https://chatgpt.com/"
		h.windows["Google Chrome"] = []string{"ChatGPT", "Meet - abc-defg-hij"}
		h.run(3*time.Second, nil)
		dictationStart := h.now
		h.holders = holding("com.google.Chrome.helper")
		h.run(2*time.Minute, nil)
		h.requireNoCall("Chrome dictation right after a Meet hangup")
		h.finish()
		for _, ctx := range h.injected {
			if !ctx.CapturedAt.Before(dictationStart) && (ctx.InMeeting || ctx.MeetingCandidate) {
				t.Fatalf("dictation after the hangup is never call speech: captured %s after it", ctx.CapturedAt.Sub(dictationStart))
			}
		}
		h.waitForTranscript()
	})

	t.Run("Zoom on the microphone with Accessibility denied", func(t *testing.T) {
		h := newCallHarness(t)
		h.apps = []activitymon.RunningApp{zoomApp}
		h.windows = nil // no window titles without Accessibility
		h.front = activitymon.AppInfo{BundleID: "us.zoom.xos", AppName: "zoom.us"}
		h.holders = holding("us.zoom.xos")
		h.run(10*time.Minute, func(elapsed time.Duration) {
			if elapsed >= 5*time.Minute {
				h.front = dictation[0]
			}
		})
		h.requireNoCall("Accessibility denied")
		speech := h.finish()
		for _, evt := range speech {
			if evt.Kind == activitymon.EventSpeech && (evt.SpeechInCall || evt.MeetingCandidate) {
				t.Fatalf("an unconfirmed hold is never call speech: %+v", evt)
			}
		}
		if sessions := meetingSessions(t, h.store); len(sessions) != 0 {
			t.Fatalf("no meeting session may be saved: %+v", sessions)
		}
		if files, _ := filepath.Glob(filepath.Join(h.store.AudioDir(), "*", "*")); len(files) != 0 {
			t.Fatalf("no call audio may be kept: %v", files)
		}
	})
}

// waitForTranscript waits for the ended call's transcript, written in the
// background, so the test's folders are not removed under it.
func (h *callHarness) waitForTranscript() {
	h.t.Helper()
	dir, err := transcriptDir()
	if err != nil {
		h.t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if files, _ := filepath.Glob(filepath.Join(dir, "*.md")); len(files) > 0 {
			time.Sleep(200 * time.Millisecond)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatal("the ended call's transcript was not written")
}

// headedWAV is makeWAV with a real header, as the recorders write it.
func headedWAV(samples []int16) []byte {
	wav := makeWAV(samples)
	var header bytes.Buffer
	_ = writeWAVHeader(&header, uint32(len(samples)*2))
	copy(wav, header.Bytes())
	return wav
}

// leftoverTracks writes raw tracks and their sidecar as a crash or a failed
// finalize leaves them, an hour old.
func leftoverTracks(t *testing.T, day, prefix string, meta meetingAudioMeta) {
	t.Helper()
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	mic := make([]byte, meetingAudioBytesPerSecond)
	for i := range mic {
		mic[i] = 1
	}
	raw, _ := json.Marshal(meta)
	for name, data := range map[string][]byte{prefix + ".mic.pcm": mic, prefix + ".json": raw} {
		if err := os.WriteFile(filepath.Join(day, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(day, prefix+".mic.pcm"), old, old)
}

func TestRecoveredRecordingJoinsItsSessionAndMeeting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newLifecycleStore(t)
	audioDir := t.TempDir()
	day := filepath.Join(audioDir, "2026-10-15")
	view := &ActivityStore{db: store.db, audioDir: audioDir}

	// Finalize failed after the call's session was saved (its notes came
	// from the live speech): the recording joins that session.
	start := time.Now().Add(-2 * time.Hour)
	saved := ActivitySessionRow{ID: "slice-1", StartTime: start.UnixMilli(), EndTime: start.Add(time.Minute).UnixMilli(), AppNames: "[]", TriageStatus: "pending", IsMeeting: true, MeetingApp: "Zoom", TranscriptPath: "/tmp/t.md", CreatedAt: start.UnixMilli()}
	if err := store.SaveSession(saved, nil, nil); err != nil {
		t.Fatal(err)
	}
	leftoverTracks(t, day, "1792000000000-aaaa1111", meetingAudioMeta{App: "Zoom", MeetingID: "m-7", SessionID: "slice-1", StartMs: start.UnixMilli()})
	view.recoverMeetingAudioTracks(time.Now(), false)
	sessions := meetingSessions(t, store)
	if len(sessions) != 1 || sessions[0].ID != "slice-1" || sessions[0].AudioPath == "" {
		t.Fatalf("the recording joins its session, no second call: %+v", sessions)
	}

	// A crash: no session yet. The new session's recording keeps the
	// meeting id, which transcript recovery keys the notes on.
	leftoverTracks(t, day, "1792000100000-bbbb2222", meetingAudioMeta{App: "Zoom", MeetingID: "m-8", StartMs: start.Add(time.Hour).UnixMilli()})
	view.recoverMeetingAudioTracks(time.Now(), false)
	sessions = meetingSessions(t, store)
	if len(sessions) != 2 {
		t.Fatalf("a crashed call becomes a session: %+v", sessions)
	}
	for _, s := range sessions {
		if s.ID != "slice-1" && readMeetingWAVMeta(audioDir, s.AudioPath).MeetingID != "m-8" {
			t.Fatalf("recovered session lost its meeting: %+v", s)
		}
	}
}

func TestRecoveryFollowsTheProtectedRuleAndTheRecordingSetting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newLifecycleStore(t)
	audioDir := t.TempDir()
	day := filepath.Join(audioDir, "2026-10-15")
	view := &ActivityStore{db: store.db, audioDir: audioDir}

	leftoverTracks(t, day, "1792000000000-cccc3333", meetingAudioMeta{App: "Zoom", MeetingID: "m-1", Protected: true})
	view.recoverMeetingAudioTracks(time.Now(), false)
	if sessions := meetingSessions(t, store); len(sessions) != 1 || sessions[0].TriageStatus != "protected" {
		t.Fatalf("a call with a protected app in front stays protected: %+v", sessions)
	}

	// Call recording turned off: the leftovers are removed, as a recording
	// in progress is when it is turned off.
	app := &App{}
	app.meetingRecordingState.Store(meetingRecordingOff)
	leftoverTracks(t, day, "1792000200000-dddd4444", meetingAudioMeta{App: "Zoom", MeetingID: "m-2"})
	app.recoverMeetingAudio(view, time.Now())
	for _, name := range []string{".mic.pcm", ".json"} {
		if _, err := os.Stat(filepath.Join(day, "1792000200000-dddd4444"+name)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed while recording is off: %v", name, err)
		}
	}
	if sessions := meetingSessions(t, store); len(sessions) != 1 {
		t.Fatalf("no call may be recovered while recording is off: %+v", sessions)
	}
}

func TestDeletingMeetingNotesRemovesTheMeetingsLeftoverAudio(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	store := newLifecycleStore(t)
	live, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	live.SetMeeting("Zoom", "us.zoom.xos", "m-K")
	t.Cleanup(live.Abort)
	app.activity = &activityMonitorState{store: store, meetingAudioWriter: live}

	day := filepath.Join(store.AudioDir(), "2026-10-14")
	leftoverTracks(t, day, "1792000000000-eeee5555", meetingAudioMeta{App: "Zoom", MeetingID: "m-K"})
	leftoverTracks(t, day, "1792000000000-ffff6666", meetingAudioMeta{App: "Zoom", MeetingID: "m-other"})

	start := time.Now().Add(-time.Hour)
	session := ActivitySessionRow{ID: "slice-kkkk", StartTime: start.UnixMilli(), EndTime: start.Add(time.Minute).UnixMilli(), AppNames: "[]", TriageStatus: "dismissed", IsMeeting: true, MeetingApp: "Zoom", CreatedAt: start.UnixMilli()}
	if err := store.SaveSession(session, nil, nil); err != nil {
		t.Fatal(err)
	}
	// An old Knowledge entry the move left behind: deleting it deletes the
	// call, the way it always did.
	entryID := writeLegacyMeetingEntry(t, app, session, "m-K", "## Transcript\n\n[10:00:00] Hello.\n")
	if err := app.DeleteKnowledgeEntry(entryID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".mic.pcm", ".json"} {
		if _, err := os.Stat(filepath.Join(day, "1792000000000-eeee5555"+name)); !os.IsNotExist(err) {
			t.Fatalf("the meeting's leftover %s would come back as a call: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(day, "1792000000000-ffff6666"+name)); err != nil {
			t.Fatalf("another meeting's %s must stay: %v", name, err)
		}
	}
	if _, err := os.Stat(live.metaPath()); err != nil {
		t.Fatalf("the recording in progress is left alone: %v", err)
	}
}

func TestEmptyEndPersistHoldsTheRecordingFence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newLifecycleStore(t)
	app := &App{activity: &activityMonitorState{store: store}}
	epoch, _ := app.automaticMeetingRecordingEpoch()
	writer, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMeeting("Zoom", "us.zoom.xos", "m-1")
	writer.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(meetingAudioSampleRate, 10)))

	// Recording is being turned off: the setter holds the fence.
	app.meetingRecordingMu.Lock()
	done := make(chan struct{})
	go func() {
		app.persistEndedCallRecording(writer, epoch, "meeting_ended")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the persist must wait for the fence")
	case <-time.After(100 * time.Millisecond):
	}
	app.meetingRecordingState.Store(meetingRecordingOff)
	app.meetingRecordingEpoch.Add(1)
	app.meetingRecordingMu.Unlock()
	<-done

	if sessions := meetingSessions(t, store); len(sessions) != 0 {
		t.Fatalf("turned off during the persist: nothing may be saved: %+v", sessions)
	}
	if files, _ := filepath.Glob(filepath.Join(store.AudioDir(), "*", "*")); len(files) != 0 {
		t.Fatalf("turned off during the persist: no audio may stay: %v", files)
	}
}

func TestCallAudioOverlappingAProtectedAppIsSilenced(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newLifecycleStore(t)
	app := &App{activity: &activityMonitorState{
		buf:             activitybuf.NewRingBuffer(16, time.Minute),
		store:           store,
		inMeeting:       true,
		meetingAppName:  "Zoom",
		meetingBundleID: "us.zoom.xos",
	}}
	t0 := time.Now().Add(-time.Minute)
	app.activity.meetingStartMs = t0.Add(-time.Minute).UnixMilli()
	writer, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMeeting("Zoom", "us.zoom.xos", "m-1")
	app.activity.meetingAudioWriter = writer

	// 1Password in front for two seconds in the middle of a chunk; Zoom in
	// front again by the chunk's end.
	app.captureSpans.noteFront("com.1password.1password", false, t0.Add(-3*time.Second).UnixMilli())
	app.captureSpans.noteFront("us.zoom.xos", false, t0.Add(-time.Second).UnixMilli())

	chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:      backgroundListeningSourceMicrophone,
		WavData:     headedWAV(constSamples(5*meetingAudioSampleRate, 3000)),
		Duration:    backgroundAudioChunkDuration,
		CapturedAt:  t0,
		CapturedApp: activitymon.AppInfo{BundleID: "us.zoom.xos", AppName: "zoom.us", WindowTitle: "Zoom Meeting"},
		MicHolders:  holding("us.zoom.xos"),
	})
	if !chunk.ProtectedAppInFront || !chunk.CapturedInMeeting {
		t.Fatalf("the chunk overlapped 1Password: %+v", chunk.ProtectedAppInFront)
	}
	if kept := meetingRecordingChunk(chunk); !bytes.Equal(kept.WavData[wavHeaderSize:], make([]byte, len(kept.WavData)-wavHeaderSize)) {
		t.Fatal("audio recorded while a protected app was in front becomes silence")
	}
	app.writeMeetingAudioChunk(chunk)
	if !writer.metaSnapshot().Protected {
		t.Fatal("the recording remembers a protected app was in front")
	}

	// Later chunks no longer overlap it.
	later := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source:     backgroundListeningSourceMicrophone,
		Duration:   backgroundAudioChunkDuration,
		CapturedAt: t0.Add(10 * time.Second),
		MicHolders: holding("us.zoom.xos"),
	})
	if later.ProtectedAppInFront {
		t.Fatal("a chunk after the protected app left is not protected")
	}

	// The call ends with no events: its session follows the protected rule.
	app.activity.mu.Lock()
	app.activity.meetingAudioWriter = nil
	app.activity.mu.Unlock()
	epoch, _ := app.automaticMeetingRecordingEpoch()
	app.persistEndedCallRecording(writer, epoch, "meeting_ended")
	if sessions := meetingSessions(t, store); len(sessions) != 1 || sessions[0].TriageStatus != "protected" {
		t.Fatalf("the call is protected: %+v", sessions)
	}
}

func TestRecordingTimelineKeepsTheTimeTheMacSlept(t *testing.T) {
	// The monotonic clock stops while the Mac sleeps: 40 minutes with the
	// lid closed read as 5 seconds on it, 40 minutes on the wall clock.
	if got := pickRecordingElapsed(5*time.Second, 40*time.Minute+5*time.Second); got != 40*time.Minute+5*time.Second {
		t.Fatalf("a closed lid must stay in the recording, got %s", got)
	}
	// The wall clock set back an hour: the monotonic clock is right.
	if got := pickRecordingElapsed(10*time.Minute, 10*time.Minute-time.Hour); got != 10*time.Minute {
		t.Fatalf("a clock change must not bend the recording, got %s", got)
	}
	// Ordinary jitter between the two: monotonic.
	if got := pickRecordingElapsed(time.Minute, time.Minute+500*time.Millisecond); got != time.Minute {
		t.Fatalf("jitter, got %s", got)
	}
	start := time.Now()
	if got := recordingElapsed(start, start.Add(3*time.Second)); got != 3*time.Second {
		t.Fatalf("awake, got %s", got)
	}
}

// runningMonitor stands in for the activity monitor while it runs.
type runningMonitor struct{ stopped bool }

func (m *runningMonitor) Start(context.Context, func(activitymon.ActivityEvent)) error { return nil }
func (m *runningMonitor) Stop()                                                        { m.stopped = true }
func (m *runningMonitor) IsRunning() bool                                              { return !m.stopped }

func TestPausingObservationMidCallKeepsTheCallSoFar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newLifecycleStore(t)
	app := mainChatTestApp(t)
	app.audioScreenLocked = func() bool { return false }
	writer, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMeeting("Zoom", "us.zoom.xos", "m-P")
	writer.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(2*meetingAudioSampleRate, 10)))
	epoch, _ := app.automaticMeetingRecordingEpoch()
	app.activity = &activityMonitorState{
		monitor:               &runningMonitor{},
		enabled:               true,
		buf:                   activitybuf.NewRingBuffer(16, time.Minute),
		store:                 store,
		capture:               failingScreenCapture{},
		inMeeting:             true,
		meetingAppName:        "Zoom",
		meetingBundleID:       "us.zoom.xos",
		meetingID:             "m-P",
		meetingStartMs:        time.Now().Add(-time.Minute).UnixMilli(),
		meetingRecordingEpoch: epoch,
		meetingAudioWriter:    writer,
	}

	if err := app.SetClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	app.activity.mu.Lock()
	inMeeting, current, lastID := app.activity.inMeeting, app.activity.meetingAudioWriter, app.activity.lastMeetingID
	app.activity.mu.Unlock()
	if inMeeting || current != nil {
		t.Fatalf("a pause ends the call cleanly: inMeeting=%v writer=%v", inMeeting, current != nil)
	}
	if !app.activity.lastMeetingPaused {
		t.Fatal("observation pause must retain its call continuation identity")
	}
	if lastID != "m-P" {
		t.Fatalf("a call still running after the pause continues under its meeting id, got %q", lastID)
	}
	sessions := meetingSessions(t, store)
	if len(sessions) != 1 || sessions[0].AudioPath == "" || sessions[0].TriggerKind != pausedCallTrigger {
		t.Fatalf("the call so far keeps its recording: %+v", sessions)
	}
	if _, err := os.Stat(filepath.Join(store.AudioDir(), sessions[0].AudioPath)); err != nil {
		t.Fatalf("recording missing: %v", err)
	}
	app.callsKeptAtPauseMu.Lock()
	waiting := len(app.callsKeptAtPause)
	app.callsKeptAtPauseMu.Unlock()
	if waiting != 1 {
		t.Fatalf("nothing is transcribed while paused; the call waits: %d", waiting)
	}

	app.closedEyes.mu.Lock()
	app.closedEyes.resumeActivity = false // the monitor stand-in cannot start
	app.closedEyes.mu.Unlock()
	if err := app.SetClosedEyesModeEnabled(false); err != nil {
		t.Fatal(err)
	}
	app.callsKeptAtPauseMu.Lock()
	waiting = len(app.callsKeptAtPause)
	app.callsKeptAtPauseMu.Unlock()
	if waiting != 0 {
		t.Fatal("resuming writes the paused call's transcript")
	}
}

func TestOrphanRecoveryAddsALaterSliceToItsMeetingsNotes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	store := newLifecycleStore(t)
	app.activity = &activityMonitorState{store: store}
	stubMeetingNotesLearn(t)
	start := time.Now().Add(-3 * time.Hour)
	first := ActivitySessionRow{ID: "slice-1111-aaaa", StartTime: start.UnixMilli(), EndTime: start.Add(2 * time.Hour).UnixMilli(), AppNames: "[]", TriageStatus: "dismissed", IsMeeting: true, MeetingApp: "Zoom", CreatedAt: start.UnixMilli()}
	_, words := buildMeetingTranscriptMarkdown("Zoom", first, []meetingUtterance{{At: start, End: start.Add(5 * time.Second), Text: "First hour of the review."}}, nil)
	if err := store.SaveSession(first, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.recordMeetingNotes(first, "Zoom", words, "meeting-L", "", nil); err != nil {
		t.Fatal(err)
	}

	// The app quit after the second slice's transcript was written, before
	// its notes were.
	later := start.Add(2 * time.Hour)
	second := ActivitySessionRow{ID: "slice-2222-bbbb", StartTime: later.UnixMilli(), EndTime: later.Add(10 * time.Minute).UnixMilli(), AppNames: "[]", TriageStatus: "dismissed", IsMeeting: true, MeetingApp: "Zoom", CreatedAt: later.UnixMilli()}
	if err := store.SaveSession(second, nil, nil); err != nil {
		t.Fatal(err)
	}
	var md string
	md, _ = buildMeetingTranscriptMarkdown("Zoom", second, []meetingUtterance{{At: later, End: later.Add(5 * time.Second), Text: "The wrap up after two hours."}}, nil)
	dir, err := transcriptDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, later.Format("2006-01-02")+"-zoom-"+second.ID[:8]+".md"), []byte(withTranscriptMeetingID(md, "meeting-L")), 0o600); err != nil {
		t.Fatal(err)
	}
	app.recoverOrphanTranscripts(t.Context())

	r, ok, err := store.meetingNotesByKey("meeting-L")
	if ids := r.SessionIDs(); err != nil || !ok || len(ids) != 2 || ids[0] != first.ID || ids[1] != second.ID {
		t.Fatalf("the later slice joins its meeting's notes: %+v %v", r, err)
	}
	if keys, _ := store.recentMeetingNotes(0); len(keys) != 1 {
		t.Fatalf("one meeting for the call: %v", keys)
	}
	// Recovered once, it is not recovered again.
	app.recoverOrphanTranscripts(t.Context())
	if r, _, _ := store.meetingNotesByKey("meeting-L"); len(r.Slices) != 2 {
		t.Fatalf("slices after a second recovery = %d", len(r.Slices))
	}
}

func TestAppsTheUserListedAreNotListenedTo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saved := GetUserVoiceInputApps()
	t.Cleanup(func() { SetUserVoiceInputApps(saved) })
	SetUserVoiceInputApps(map[string]string{"com.example.voicenotes": "Voice Notes"})
	app := &App{activity: &activityMonitorState{buf: activitybuf.NewRingBuffer(16, time.Minute)}}

	listed := capturedAudioChunk{
		Source:      backgroundListeningSourceMicrophone,
		WavData:     loudPCM(1000),
		CapturedAt:  time.Now(),
		CapturedApp: activitymon.AppInfo{BundleID: "com.example.voicenotes", AppName: "Voice Notes"},
	}
	if app.shouldTranscribeBackgroundAudioChunk(app.annotateBackgroundAudioChunk(listed)) {
		t.Fatal("an app the user listed is never turned into text")
	}
	// A built-in voice app the user did not list is heard and labeled.
	builtIn := listed
	builtIn.CapturedApp = activitymon.AppInfo{BundleID: "com.goodsnooze.MacWhisper", AppName: "MacWhisper"}
	if !app.shouldTranscribeBackgroundAudioChunk(app.annotateBackgroundAudioChunk(builtIn)) {
		t.Fatal("a built-in voice app the user did not list is heard")
	}
	if got := speechDictationTarget(builtIn.CapturedApp, micInputSnapshot{}, false); got != "MacWhisper" {
		t.Fatalf("labeled %q, want MacWhisper", got)
	}
}

func TestSpeechStillQueuedWhenListeningStopsIsNotKept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{activity: &activityMonitorState{buf: activitybuf.NewRingBuffer(16, time.Minute)}}
	t.Cleanup(func() {
		app.activity.mu.Lock()
		if app.activity.idleTimer != nil {
			app.activity.idleTimer.Stop()
		}
		app.activity.mu.Unlock()
	})
	setListening := func(listening, microphone bool) {
		t.Helper()
		if err := updateConfig(func(cfg *yansuConfig) {
			cfg.BackgroundListeningConfigured = true
			cfg.BackgroundListeningDisabled = !listening
			cfg.BackgroundMicrophonePaused = !microphone
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := speechCaptureContext{CapturedAt: time.Now()}

	setListening(false, true)
	app.injectSpeechEvent("what was still waiting", backgroundListeningSourceMicrophone, ctx)
	app.injectSpeechEvent("what was still waiting", backgroundListeningSourceSystem, ctx)
	if app.activity.buf.Len() != 0 {
		t.Fatal("Listen turned off: nothing still queued is kept")
	}
	setListening(true, false)
	app.injectSpeechEvent("said into the paused microphone", backgroundListeningSourceMicrophone, ctx)
	if app.activity.buf.Len() != 0 {
		t.Fatal("microphone turned off: its queued speech is not kept")
	}
	app.injectSpeechEvent("the computer's sound", backgroundListeningSourceSystem, ctx)
	if app.activity.buf.Len() != 0 {
		t.Fatal("Hear the computer is off: its queued sound outside calls is not kept")
	}
	if err := app.SetSystemAudioOutsideCallsEnabled(true); err != nil {
		t.Fatal(err)
	}
	app.injectSpeechEvent("the computer's sound", backgroundListeningSourceSystem, ctx)
	if app.activity.buf.Len() != 1 {
		t.Fatal("the computer's sound is not the microphone")
	}
}

func TestABrowserThatMayBeInACallIsNotDictation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}
	chrome := activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "Weekly sync"}
	onMic := holding("com.google.Chrome.helper")
	if !app.browserMayBeInACall(speechCaptureContext{App: chrome, MicHolders: onMic, BrowserMeeting: "Jitsi Meet"}) {
		t.Fatal("a browser on a call page is no dictation")
	}
	if app.browserMayBeInACall(speechCaptureContext{App: chrome, MicHolders: onMic}) {
		t.Fatal("a browser on any other page holding the microphone is web dictation")
	}
	app.meetingRecordingState.Store(meetingRecordingOff)
	if !app.browserMayBeInACall(speechCaptureContext{App: chrome, MicHolders: onMic}) {
		t.Fatal("with meetings off a browser's calls are never told apart: no dictation label")
	}
	claude := activitymon.AppInfo{BundleID: "com.anthropic.claudefordesktop", AppName: "Claude"}
	if app.browserMayBeInACall(speechCaptureContext{App: claude, MicHolders: holding(claude.BundleID)}) {
		t.Fatal("only browsers")
	}
}

// listApps puts apps under "Apps Isoform does not listen to" for one test.
func listApps(t *testing.T, apps map[string]string) {
	t.Helper()
	saved := GetUserVoiceInputApps()
	t.Cleanup(func() { SetUserVoiceInputApps(saved) })
	SetUserVoiceInputApps(apps)
}

func TestACallIsRecordedWholeWhateverAppIsInFront(t *testing.T) {
	h := newCallHarness(t)
	listApps(t, map[string]string{"com.anthropic.claudefordesktop": "Claude"})
	h.apps = []activitymon.RunningApp{zoomApp}
	h.windows["zoom.us"] = []string{"Zoom Meeting"}
	h.front = activitymon.AppInfo{BundleID: "us.zoom.xos", AppName: "zoom.us", WindowTitle: "Zoom Meeting"}
	h.holders = holding("us.zoom.xos")
	h.run(time.Minute, nil)
	if inMeeting, _, app := h.state(); !inMeeting || app != "Zoom" {
		t.Fatalf("the Zoom call is a call: inMeeting=%v app=%q", inMeeting, app)
	}
	// Claude, a listed app, in front during the call: the call goes on
	// being recorded and turned into text.
	h.front = activitymon.AppInfo{BundleID: "com.anthropic.claudefordesktop", AppName: "Claude", WindowTitle: "Claude"}
	chunk := capturedAudioChunk{
		Source: backgroundListeningSourceMicrophone, WavData: headedWAV(constSamples(meetingAudioSampleRate, 2000)),
		Duration: backgroundAudioChunkDuration, CapturedAt: h.now, CapturedApp: h.front,
	}
	chunk = h.app.annotateBackgroundAudioChunk(withMicHolders(chunk, h.holders, h.now))
	if !chunk.CapturedInMeeting || !h.app.shouldTranscribeBackgroundAudioChunk(chunk) {
		t.Fatal("a call is recorded whole, whatever app is in front")
	}
	h.finish()
}

func TestAListedCallAppsCallsAreLeftOut(t *testing.T) {
	h := newCallHarness(t)
	listApps(t, map[string]string{"us.zoom.xos": "zoom.us"})
	h.apps = []activitymon.RunningApp{zoomApp}
	h.windows["zoom.us"] = []string{"Zoom Meeting"}
	h.front = activitymon.AppInfo{BundleID: "us.zoom.xos", AppName: "zoom.us", WindowTitle: "Zoom Meeting"}
	h.holders = holding("us.zoom.xos")
	h.app.handleActivityEvent(activitymon.ActivityEvent{
		Timestamp: h.now.UnixMilli(), Kind: activitymon.EventClick,
		AppBundleID: h.front.BundleID, AppName: h.front.AppName, WindowTitle: h.front.WindowTitle,
	})
	h.run(3*time.Minute, func(elapsed time.Duration) {
		if elapsed >= time.Minute {
			h.front = activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes", WindowTitle: "Notes"}
		}
	})
	h.requireNoCall("a listed Zoom's call")
	if h.engine.seenBlobs() != 0 {
		t.Fatal("nothing of a listed call app's call is turned into text, whatever is in front")
	}
	h.finish()
	if files, _ := filepath.Glob(filepath.Join(h.store.AudioDir(), "*", "*")); len(files) != 0 {
		t.Fatalf("nothing of it is recorded: %v", files)
	}
}

func TestAListedAppInFrontForPartOfAChunkIsNotListenedTo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listApps(t, map[string]string{"com.example.wispr": "Wispr"})
	app := &App{activity: &activityMonitorState{buf: activitybuf.NewRingBuffer(16, time.Minute)}}
	end := time.Now()
	// Dictating into the listed app for three seconds, then into Notes:
	// the chunk ends with Notes in front.
	app.captureSpans.noteFront("com.example.wispr", false, end.Add(-4*time.Second).UnixMilli())
	app.captureSpans.noteFront("com.apple.Notes", false, end.Add(-time.Second).UnixMilli())
	chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source: backgroundListeningSourceMicrophone, WavData: loudPCM(1000), Duration: backgroundAudioChunkDuration,
		CapturedAt: end, CapturedApp: activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes"},
	})
	if !chunk.ListedAppInFront || app.shouldTranscribeBackgroundAudioChunk(chunk) {
		t.Fatal("a listed app in front for part of the chunk: not turned into text")
	}
	later := app.annotateBackgroundAudioChunk(capturedAudioChunk{
		Source: backgroundListeningSourceMicrophone, WavData: loudPCM(1000), Duration: backgroundAudioChunkDuration,
		CapturedAt: end.Add(10 * time.Second), CapturedApp: activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes"},
	})
	if later.ListedAppInFront || !app.shouldTranscribeBackgroundAudioChunk(later) {
		t.Fatal("after the listed app left the front, speech is heard again")
	}
	// A listed app holding the microphone from the background.
	held := later
	held.MicHolders = holding("com.example.wispr")
	if app.shouldTranscribeBackgroundAudioChunk(held) {
		t.Fatal("a listed app on the microphone: not turned into text")
	}
}

func TestTheMomentsInsideAChunkCount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listApps(t, map[string]string{"com.example.wispr": "Wispr"})
	saved := meetingProbes
	t.Cleanup(func() { meetingProbes = saved })
	front, holders := activitymon.AppInfo{}, micInputSnapshot{}
	meetingProbes.frontmost = func() activitymon.AppInfo { return front }
	meetingProbes.micHolders = func() micInputSnapshot { return holders }
	notes := activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes", WindowTitle: "Notes"}

	// No activity monitor at all (Watch my screen off): the sampler alone
	// follows the moments.
	scene := func(name string, during func()) {
		t.Run(name, func(t *testing.T) {
			app := &App{}
			end := time.Now()
			front, holders = notes, micInputSnapshot{}
			during()
			app.sampleCaptureState(end.Add(-4 * time.Second))
			front, holders = notes, micInputSnapshot{}
			app.sampleCaptureState(end.Add(-2 * time.Second))
			chunk := app.annotateBackgroundAudioChunk(capturedAudioChunk{
				Source: backgroundListeningSourceMicrophone, WavData: loudPCM(1000), Duration: backgroundAudioChunkDuration,
				CapturedAt: end, CapturedApp: notes,
			})
			if app.shouldTranscribeBackgroundAudioChunk(chunk) {
				t.Fatal("the chunk must not be turned into text")
			}
			later := chunk
			later.CapturedAt = end.Add(10 * time.Second)
			later.ListedAppInFront, later.PrivateWindowInFront = false, false
			later = app.annotateBackgroundAudioChunk(later)
			if !app.shouldTranscribeBackgroundAudioChunk(later) {
				t.Fatal("a chunk after it is heard again")
			}
		})
	}
	scene("a listed app in front for part of the chunk", func() {
		front = activitymon.AppInfo{BundleID: "com.example.wispr", AppName: "Wispr"}
	})
	scene("a listed app on the microphone for part of the chunk", func() {
		holders = holding("com.example.wispr")
	})
	scene("a private window in front for part of the chunk", func() {
		front = activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "Inbox - Google Chrome - Incognito"}
	})
}

func TestFrontSpanEndsWhereItEnded(t *testing.T) {
	var f frontSpan
	f.note(true, 6000)
	f.note(false, 8000)
	if f.overlaps(0, 5000) {
		t.Fatal("a stretch from 6 s to 8 s does not touch a chunk from 0 to 5 s")
	}
	if !f.overlaps(5000, 10000) || !f.overlaps(7000, 7500) {
		t.Fatal("it touches the chunks it falls in")
	}
}

func TestFollowUpsAreNotWrittenAfterRecordingIsTurnedOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	saved := meetingFollowupModel
	t.Cleanup(func() { meetingFollowupModel = saved })
	meetingFollowupModel = func(*App, string) (string, error) {
		return `{"decisions":["Ship Friday"],"followups":[{"text":"Send the deck","owner":"me"}]}`, nil
	}
	session := ActivitySessionRow{ID: "call-followups-1", EndTime: time.Now().UnixMilli()}
	transcript := "## Transcript\n\n[10:00:00] Me: I will send the deck.\n"
	turnedOff := func(func()) bool { return false }
	if _, err := app.extractMeetingFollowups(t.Context(), session, transcript, nil, turnedOff); err == nil {
		t.Fatal("turned off during the model call: the pass reports it")
	}
	if _, ok, _ := app.store.GetMeetingFollowup(t.Context(), session.ID); ok {
		t.Fatal("no follow-up row after recording was turned off")
	}
	stillOn := func(write func()) bool { write(); return true }
	if _, err := app.extractMeetingFollowups(t.Context(), session, transcript, nil, stillOn); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := app.store.GetMeetingFollowup(t.Context(), session.ID); !ok {
		t.Fatal("still on: the follow-ups are written")
	}
}

func TestEveryStretchInsideAChunkCounts(t *testing.T) {
	listApps(t, map[string]string{"com.example.wispr": "Wispr"})
	end := time.Now().UnixMilli()
	start := end - backgroundAudioChunkDuration.Milliseconds()
	notes := "com.apple.Notes"
	for _, kind := range []struct {
		name string
		on   func(c *captureSpans, ts int64)
		off  func(c *captureSpans, ts int64)
		got  func(protected, listed, private bool) bool
	}{
		{"a protected app in front",
			func(c *captureSpans, ts int64) { c.noteFront("com.1password.1password", false, ts) },
			func(c *captureSpans, ts int64) { c.noteFront(notes, false, ts) },
			func(p, _, _ bool) bool { return p }},
		{"a listed app in front",
			func(c *captureSpans, ts int64) { c.noteFront("com.example.wispr", false, ts) },
			func(c *captureSpans, ts int64) { c.noteFront(notes, false, ts) },
			func(_, l, _ bool) bool { return l }},
		{"a listed app on the microphone",
			func(c *captureSpans, ts int64) { c.noteHolders(holding("com.example.wispr"), ts) },
			func(c *captureSpans, ts int64) { c.noteHolders(micInputSnapshot{}, ts) },
			func(_, l, _ bool) bool { return l }},
		{"a private window in front",
			func(c *captureSpans, ts int64) { c.noteFront("com.google.Chrome", true, ts) },
			func(c *captureSpans, ts int64) { c.noteFront(notes, false, ts) },
			func(_, _, pr bool) bool { return pr }},
	} {
		t.Run(kind.name, func(t *testing.T) {
			var c captureSpans
			// One stretch inside the chunk, then a second that begins and
			// ends after the chunk but before it is annotated (the
			// microphone chunk waits for its system audio sibling).
			kind.on(&c, start+1000)
			kind.off(&c, start+2000)
			kind.on(&c, end+1000)
			kind.off(&c, end+2000)
			if !kind.got(c.during(start, end)) {
				t.Fatal("the stretch inside the chunk is forgotten once a later one ends")
			}
			// A chunk between the two stretches is untouched.
			if kind.got(c.during(start+2500, start+3500)) {
				t.Fatal("a chunk between the stretches is not in either")
			}
		})
	}
}

func TestABrowserCallKeepsShortMicGaps(t *testing.T) {
	meet := activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "Meet - abc-defg-hij"}
	docs := activitymon.AppInfo{BundleID: "com.google.Chrome", AppName: "Google Chrome", WindowTitle: "Notes - Google Docs"}
	for _, scene := range []struct {
		name  string
		front activitymon.AppInfo
		url   string
		gap   time.Duration
	}{
		{"a one-second drop while another tab is in front", docs, "https://docs.google.com/document/d/x", time.Second},
		{"a two-second device switch while another tab is in front", docs, "https://docs.google.com/document/d/x", 2 * time.Second},
		{"an eight-second muted stretch on the call page", meet, "https://meet.google.com/abc-defg-hij", 8 * time.Second},
	} {
		t.Run(scene.name, func(t *testing.T) {
			h := newCallHarness(t)
			h.systemOffset = 2500 * time.Millisecond
			h.apps = []activitymon.RunningApp{chromeApp}
			h.windows["Google Chrome"] = []string{"Meet - abc-defg-hij"}
			h.front, h.holders, h.url = meet, holding("com.google.Chrome.helper"), "https://meet.google.com/abc-defg-hij"
			h.run(2*time.Minute, nil)
			h.app.activity.mu.Lock()
			callID, callStart := h.app.activity.meetingID, h.app.activity.meetingStartMs
			h.app.activity.mu.Unlock()
			if callID == "" {
				t.Fatal("the Meet call is a call")
			}
			h.front, h.url = scene.front, scene.url
			h.run(30*time.Second, nil)
			h.holders = holding()
			h.run(scene.gap, nil)
			h.holders = holding("com.google.Chrome.helper")
			h.run(time.Minute, func(time.Duration) {
				h.app.activity.mu.Lock()
				released, id, start := h.app.activity.meetingBrowserMicReleased, h.app.activity.meetingID, h.app.activity.meetingStartMs
				h.app.activity.mu.Unlock()
				if released || id != callID || start != callStart {
					t.Fatalf("a short gap in a call is still the same call: released=%v id=%q start=%d", released, id, start)
				}
			})
			h.finish()
		})
	}
}

// Speech the transcriber finishes after a call ended still says it was
// captured in the call. Inside the saved call's span it stays activity and
// never becomes a second, overlapping meeting session; in-call speech after
// that span is still inferred as a meeting.
func TestLateSpeechOfAnEndedCallIsNotASecondMeeting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newLifecycleStore(t)
	writer, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-10 * time.Minute)
	writer.SetMeeting("Zoom", "us.zoom.xos", "meeting-late")
	writer.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, start, constSamples(meetingAudioSampleRate, 10)))
	app := &App{activity: &activityMonitorState{
		buf:                activitybuf.NewRingBuffer(64, 2*time.Hour),
		store:              store,
		inMeeting:          true,
		meetingAppName:     "Zoom",
		meetingBundleID:    "us.zoom.xos",
		meetingID:          "meeting-late",
		meetingStartMs:     start.UnixMilli(),
		meetingAudioWriter: writer,
		capture:            failingScreenCapture{},
	}}
	speech := func(at time.Time, text string) activitymon.ActivityEvent {
		return activitymon.ActivityEvent{Timestamp: at.UnixMilli(), Kind: activitymon.EventSpeech, AppName: "Zoom",
			AppBundleID: "us.zoom.xos", WindowTitle: "Zoom Meeting", SpeechText: text, SpeechSource: "microphone", SpeechInCall: true}
	}
	app.activity.buf.Push(speech(start.Add(time.Minute), "the call starts"))
	app.activity.buf.Push(speech(start.Add(8*time.Minute), "the call ends soon"))
	app.extractSession("meeting_ended")
	if got := meetingSessions(t, store); len(got) != 1 {
		t.Fatalf("the call is one meeting session: %+v", got)
	}

	// The transcriber returns a chunk from the middle of the call.
	app.activity.buf.Push(speech(start.Add(4*time.Minute), "a sentence transcribed late"))
	app.extractSession("idle_transition")
	if got := meetingSessions(t, store); len(got) != 1 {
		t.Fatalf("late speech of the ended call became a second meeting session: %+v", got)
	}
	all, err := store.GetSessionsByAge()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("the late speech is kept as activity: %d sessions", len(all))
	}

	// In-call speech after the saved call's span is still a meeting.
	app.activity.buf.Push(speech(time.Now().Add(time.Minute), "a new call"))
	app.extractSession("idle_transition")
	if got := meetingSessions(t, store); len(got) != 2 {
		t.Fatalf("in-call speech after the span is still inferred as a meeting: %d meeting sessions", len(got))
	}
	waitForMeetingTranscripts(t, app, 1)
}

// waitForMeetingTranscripts waits until the ended calls' transcripts, saved
// in the background, are written and no transcript save is still running:
// that work also touches the call's audio in the store's folder, which the
// test's cleanup removes.
func waitForMeetingTranscripts(t *testing.T, app *App, want int) {
	t.Helper()
	dir, err := transcriptDir()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		busy := false
		app.meetingTranscriptActive.Range(func(any, any) bool { busy = true; return false })
		if len(files) >= want && !busy {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the ended calls' transcripts were not written (want %d)", want)
}
