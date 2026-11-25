package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/asr"
	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

// The fixture encodes each known line as a distinct PCM level on its own
// channel. The deterministic decoder reads those samples, not a supplied text
// result: lost, reordered or replayed samples change the transcript. This tests
// capture-to-memory plumbing; the optional native-model E2E checks ASR accuracy.
type recordingFixtureEngine struct {
	fakeLongForm
	failOnce bool
	calls    int
}

func (e *recordingFixtureEngine) Transcribe([]byte) (string, error)                   { return "", nil }
func (e *recordingFixtureEngine) TranscribeBackground([]byte, string) (string, error) { return "", nil }
func (e *recordingFixtureEngine) IsRunning() bool                                     { return true }
func (e *recordingFixtureEngine) SetBackgroundActive(bool)                            {}
func (e *recordingFixtureEngine) Stop()                                               {}
func (e *recordingFixtureEngine) TranscribeLongForm(r asr.SampleReader, source string) ([]asr.Segment, error) {
	e.calls++
	if e.failOnce {
		e.failOnce = false
		_, _ = r.ReadSamples() // restart after consuming part of a channel
		return nil, errors.New("fixture ASR restart")
	}
	return e.fakeLongForm.TranscribeLongForm(r, source)
}

func TestMeetingRecordingSyntheticEndToEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		ensureBackgroundListeningDefaultForFreshInstall()
		app := mainChatTestApp(t)
		store := newLifecycleStore(t)
		app.activity = &activityMonitorState{store: store, buf: activitybuf.NewRingBuffer(64, time.Hour), capture: failingScreenCapture{}}
		saved := meetingFollowupModel
		meetingFollowupModel = func(*App, string) (string, error) { return `{"decisions":[],"followups":[]}`, nil }
		t.Cleanup(func() { meetingFollowupModel = saved })
		script := []string{
			"We need the complete opening of this call.",
			"The remote channel has the second line.",
			"Keep this line across the activity rotation.",
			"The other side continues after the rotation.",
			"Keep this line while the speech engine restarts.",
			"The remote audio survives the restart too.",
			"Preserve the final microphone sentence.",
			"Thank you and goodbye.",
		}
		texts := map[int16]string{}
		for i, line := range script {
			texts[int16(2000+i*100)] = line
		}
		first := &recordingFixtureEngine{fakeLongForm: fakeLongForm{texts: texts}}
		app.whisper = &whisperState{server: first}
		app.onMeetingStarted(app.activity, meetingdetect.Result{IsMeeting: true, MeetingName: "Zoom", BundleID: "us.zoom.xos"})
		original := app.activity.meetingAudioWriter
		start := time.Now()
		capture := &backgroundAudioCapture{
			annotateChunk:    app.annotateBackgroundAudioChunk,
			shouldTranscribe: app.shouldTranscribeBackgroundAudioChunk,
			onRecordingChunk: app.writeMeetingAudioChunk,
		}
		scheduler := newBackgroundTranscriptionScheduler(first, nil)
		scheduler.start()
		capture.scheduler = scheduler
		defer func() { capture.scheduler.stop() }()
		var last capturedAudioChunk
		for i := range script {
			time.Sleep(2 * time.Second) // simulated capture time, no wall-clock wait
			end := start.Add(time.Duration(i+1) * 2 * time.Second)
			for _, source := range []backgroundListeningSource{backgroundListeningSourceMicrophone, backgroundListeningSourceSystem} {
				level := int16(0)
				if (i%2 == 0 && source == backgroundListeningSourceMicrophone) || (i%2 == 1 && source == backgroundListeningSourceSystem) {
					level = int16(2000 + i*100)
				}
				chunk := timedChunk(source, end, constSamples(2*meetingAudioSampleRate, level))
				chunk.Duration = 2 * time.Second
				chunk.MicHolders = micInputSnapshot{Unknown: true}
				capture.forwardChunkDownstream(chunk)
				last = chunk
				if i == 2 && source == backgroundListeningSourceMicrophone {
					app.activity.buf.Push(activitymon.ActivityEvent{Timestamp: end.UnixMilli(), Kind: activitymon.EventClick, AppBundleID: "us.zoom.xos", AppName: "Zoom"})
					app.extractSession(meetingSliceTrigger)
					if app.activity.meetingAudioWriter != original {
						t.Fatal("rotation replaced the call's recording")
					}
				}
			}
			if i == 3 {
				scheduler.stop()
				replacement := &recordingFixtureEngine{fakeLongForm: fakeLongForm{texts: texts}, failOnce: true}
				app.whisper.server = replacement
				capture.scheduler = newBackgroundTranscriptionScheduler(replacement, nil)
				capture.scheduler.start()
				scheduler = capture.scheduler
			}
		}
		// A recorder replay must not append the last remote fragment again.
		capture.forwardChunkDownstream(last)
		app.onMeetingEnded(app.activity)
		synctest.Wait()
		sessions := meetingSessions(t, store)
		var call ActivitySessionRow
		for _, s := range sessions {
			if s.AudioPath != "" {
				if call.ID != "" {
					t.Fatal("multiple audio records for one call")
				}
				call = s
			}
		}
		if call.AudioPath == "" {
			t.Fatal("call recording missing")
		}
		if call.TranscriptPath != "" || transcriptFileExists(call.ID) {
			t.Fatal("failed ASR published a partial transcript")
		}
		// Retry after the failed full-recording pass; the first channel is
		// re-read from sample zero, not appended to a partial result.
		app.recoverUntranscribedMeetings()
		callPtr, err := store.GetSessionByID(call.ID)
		if err != nil || callPtr == nil || callPtr.TranscriptPath == "" {
			t.Fatalf("retry did not finish the transcript: %+v %v", callPtr, err)
		}
		raw, err := os.ReadFile(callPtr.TranscriptPath)
		if err != nil {
			t.Fatal(err)
		}
		body := stripTranscriptHeader(string(raw))
		previous := -1
		for _, line := range script {
			at := strings.Index(body, line)
			if strings.Count(body, line) != 1 || at <= previous {
				t.Fatalf("line lost, repeated or out of order: %q\n%s", line, body)
			}
			previous = at
		}
		info, err := readMeetingWAVInfo(filepath.Join(store.AudioDir(), call.AudioPath))
		if err != nil || info.channels != 2 || info.dataSize != int64(len(script)*2*meetingAudioSampleRate*4) {
			t.Fatalf("incomplete stereo audio: %+v %v", info, err)
		}
		// The call's notes live with its activity, not in Knowledge: the raw
		// transcript no longer reaches memory through the Knowledge index;
		// what Isoform learns from the notes does (meeting_notes_learning.go).
		if entries, err := app.GetKnowledgeEntries(); err != nil || len(entries) != 0 {
			t.Fatalf("the call became Knowledge: %+v %v", entries, err)
		}
		notes, err := app.GetMeetingNotesForSessions([]string{call.ID})
		if err != nil || len(notes) != 1 {
			t.Fatalf("Activity must show one call: %+v %v", notes, err)
		}
		linked := false
		for _, part := range notes[0].Parts {
			linked = linked || (part.SessionID == call.ID && part.HasRecording && part.HasTranscript)
		}
		if !linked || len(notes[0].Parts) != len(readMeetingWAVMeta(store.AudioDir(), call.AudioPath).ActivitySessions) {
			t.Fatalf("the notes do not cover the recording's sessions: %+v", notes[0].Parts)
		}
		transcript, err := app.GetMeetingNotesTranscript(call.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range script {
			if strings.Count(transcript, line) != 1 {
				t.Fatalf("the notes' transcript lost/repeated %q", line)
			}
		}
		if path, err := app.meetingRecordingPath(call.ID); err != nil || path != filepath.Join(store.AudioDir(), call.AudioPath) {
			t.Fatalf("the notes' recording = %q %v", path, err)
		}
		if len(mainChatReports(t, app)) != 0 {
			t.Fatal("recording announced itself in chat")
		}
		in := mobileBriefInput{Location: time.Local, WindowStart: start.Add(-time.Hour), AsOf: start.Add(time.Hour)}
		collectMobileBriefRecall(app, &in)
		collectMobileBriefSessions(app, &in)
		if len(in.Learned) != 0 || len(in.Meetings) != 0 {
			t.Fatalf("call announced itself in Today: %+v %+v", in.Learned, in.Meetings)
		}
	})
}

func TestMeetingAudioWriterReplayIdentityAndDistinctOverlap(t *testing.T) {
	w, err := newMeetingAudioWriter(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort()
	start := time.Now()
	first := timedChunk(backgroundListeningSourceMicrophone, start.Add(4*time.Second), constSamples(4*meetingAudioSampleRate, 1000))
	w.WriteCapturedChunk(first)
	w.WriteCapturedChunk(first)
	next := timedChunk(backgroundListeningSourceMicrophone, start.Add(6*time.Second), constSamples(4*meetingAudioSampleRate, 2000))
	w.WriteCapturedChunk(next)
	if w.micBytes != 8*meetingAudioBytesPerSecond {
		t.Fatalf("replay identity or unique overlapping delivery lost audio: %d", w.micBytes)
	}
}

func TestCaptureRecordsMicrophoneBeforeEchoPairing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := &stoppableFakeRecorder{chunks: make(chan capturedAudioChunk, 1)}
		recorded := 0
		b := &backgroundAudioCapture{recorder: recorder, onRecordingChunk: func(capturedAudioChunk) { recorded++ }, stopCh: make(chan struct{}), captureLoopDone: make(chan struct{}), running: true}
		go b.captureLoop()
		recorder.chunks <- timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 1000))
		synctest.Wait()
		if recorded != 1 {
			t.Fatal("microphone waits for sibling before reaching disk")
		}
		b.stopDiscardingQueuedTranscription()
		if recorded != 1 {
			t.Fatal("held microphone recorded twice")
		}
	})
}

func TestBackgroundSchedulerUsesReplacementEngine(t *testing.T) {
	old := newFakeBackgroundTranscriber("old words")
	fresh := newFakeBackgroundTranscriber("new words")
	var got string
	s := newBackgroundTranscriptionScheduler(old, func(text string, _ backgroundListeningSource, _ speechCaptureContext) { got = text })
	s.resolveTranscriber = func() asr.Transcriber { return fresh }
	s.processBatch([]scheduledAudioChunk{{source: backgroundListeningSourceMicrophone, wavData: headedWAV(constSamples(16000, 1000)), duration: time.Second}})
	if got != "new words" {
		t.Fatalf("stale ASR engine used: %q", got)
	}
}

// A source that returns one partial chunk when stopped, without a device.
type finalChunkRecorder struct {
	stop   chan struct{}
	once   sync.Once
	source backgroundListeningSource
}

func (r *finalChunkRecorder) Start() error { r.stop = make(chan struct{}); return nil }
func (r *finalChunkRecorder) Stop()        { r.once.Do(func() { close(r.stop) }) }
func (r *finalChunkRecorder) ActiveSources() []backgroundListeningSource {
	return []backgroundListeningSource{r.source}
}
func (r *finalChunkRecorder) ReadChunk(time.Duration) (capturedAudioChunk, error) {
	<-r.stop
	return timedChunk(r.source, time.Now(), constSamples(160, 1000)), nil
}
func TestCombinedCaptureDrainsBothFinalChunks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &combinedAudioRecorder{newRecorders: func() []audioRecorder {
			return []audioRecorder{&finalChunkRecorder{source: backgroundListeningSourceMicrophone}, &finalChunkRecorder{source: backgroundListeningSourceSystem}}
		}}
		b := &backgroundAudioCapture{recorder: r}
		var kept []capturedAudioChunk
		b.onRecordingChunk = func(c capturedAudioChunk) { kept = append(kept, c) }
		if err := b.start(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		b.stop()
		if len(kept) != 2 {
			t.Fatalf("final sibling chunks lost: %d", len(kept))
		}
	})
}

type retryFixtureRecorder struct {
	finalChunkRecorder
	attempts int
}

func (r *retryFixtureRecorder) Start() error {
	r.attempts++
	if r.attempts == 1 {
		return errors.New("fixture device temporarily unavailable")
	}
	return r.finalChunkRecorder.Start()
}
func TestCombinedCaptureRetriesMissingSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := &retryFixtureRecorder{finalChunkRecorder: finalChunkRecorder{source: backgroundListeningSourceSystem}}
		r := &combinedAudioRecorder{newRecorders: func() []audioRecorder { return []audioRecorder{source} }}
		if err := r.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if source.attempts != 2 || len(r.ActiveSources()) != 1 {
			t.Fatalf("missing source was never recovered: attempts=%d active=%v", source.attempts, r.ActiveSources())
		}
		r.Stop()
	})
}

func TestMeetingIdentityDoesNotReuseAnOldCallInTheSameProcess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gap    time.Duration
		resume bool
		paused bool
	}{
		{"short interruption", time.Minute, true, false},
		{"different call hours later", 2 * time.Hour, false, false},
		{"same call after a known privacy pause", 2 * time.Hour, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			saved := meetingProbes.runningApps
			meetingProbes.runningApps = func() []activitymon.RunningApp { return []activitymon.RunningApp{{BundleID: "us.zoom.xos", PID: 42}} }
			defer func() { meetingProbes.runningApps = saved }()
			app := mainChatTestApp(t)
			app.activity = &activityMonitorState{store: newLifecycleStore(t), buf: activitybuf.NewRingBuffer(16, time.Hour), lastMeetingID: "old-call", lastMeetingApp: "Zoom", lastMeetingPID: 42, lastMeetingPaused: tc.paused, lastMeetingEndMs: time.Now().Add(-tc.gap).UnixMilli()}
			app.onMeetingStarted(app.activity, meetingdetect.Result{IsMeeting: true, MeetingName: "Zoom", BundleID: "us.zoom.xos"})
			defer app.activity.meetingAudioWriter.Abort()
			if got := app.activity.meetingID == "old-call"; got != tc.resume {
				t.Fatalf("resume=%v, want %v", got, tc.resume)
			}
		})
	}
}

func TestMeetingRecordingPrivacyPersistsAcrossActivityRotation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	store := newLifecycleStore(t)
	app.activity = &activityMonitorState{store: store, buf: activitybuf.NewRingBuffer(16, time.Hour), capture: failingScreenCapture{}}
	app.onMeetingStarted(app.activity, meetingdetect.Result{IsMeeting: true, MeetingName: "Zoom", BundleID: "us.zoom.xos"})
	chunk := timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 2000))
	chunk.CapturedInMeeting = true
	chunk.ProtectedAppInFront = true
	app.writeMeetingAudioChunk(chunk)
	app.activity.buf.Push(activitymon.ActivityEvent{Kind: activitymon.EventClick, Timestamp: time.Now().UnixMilli(), AppName: "Zoom"})
	app.extractSession(meetingSliceTrigger)
	app.activity.buf.Push(activitymon.ActivityEvent{Kind: activitymon.EventClick, Timestamp: time.Now().UnixMilli(), AppName: "Zoom"})
	app.onMeetingEnded(app.activity)
	for _, s := range meetingSessions(t, store) {
		if s.AudioPath != "" && s.TriageStatus != "protected" {
			t.Fatalf("rotation lost the full recording's privacy boundary: %+v", s)
		}
	}
}

func TestMeetingRecoveryRetriesAnOldPendingRecording(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	store := newLifecycleStore(t)
	app.activity = &activityMonitorState{store: store}
	engine := &recordingFixtureEngine{fakeLongForm: fakeLongForm{texts: map[int16]string{2000: "This call still needs its full transcript."}}}
	app.whisper = &whisperState{server: engine}
	saved := meetingFollowupModel
	meetingFollowupModel = func(*App, string) (string, error) { return `{"decisions":[],"followups":[]}`, nil }
	defer func() { meetingFollowupModel = saved }()
	w, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	w.SetMeeting("Zoom", "us.zoom.xos", "old-pending-call")
	w.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now().Add(-7*24*time.Hour), constSamples(16000, 2000)))
	session, _, ok := app.saveCallRecordingLocked(w, 0, "meeting_ended")
	if !ok {
		t.Fatal("recording was not saved")
	}
	app.meetingRecordingState.Store(meetingRecordingOff)
	app.recoverUntranscribedMeetings()
	if engine.calls != 0 {
		t.Fatal("recording opt-out allowed recovery")
	}
	app.meetingRecordingState.Store(meetingRecordingEnabled)
	app.recoverUntranscribedMeetings()
	row, err := store.GetSessionByID(session.ID)
	if err != nil || row.TranscriptPath == "" {
		t.Fatalf("old pending recording aged out: %+v %v", row, err)
	}
}

func TestCombinedCapturePrivacyStopDiscardsFinalChunks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &combinedAudioRecorder{newRecorders: func() []audioRecorder {
			return []audioRecorder{&finalChunkRecorder{source: backgroundListeningSourceMicrophone}, &finalChunkRecorder{source: backgroundListeningSourceSystem}}
		}}
		b := &backgroundAudioCapture{recorder: r}
		kept := 0
		b.onRecordingChunk = func(capturedAudioChunk) { kept++ }
		if err := b.start(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		b.stopDiscardingQueuedTranscription()
		if kept != 0 {
			t.Fatal("privacy stop retained a final native chunk")
		}
	})
}

type queuedTailRecorder struct {
	finalChunkRecorder
	reads int
	start time.Time
}

func (r *queuedTailRecorder) ReadChunk(time.Duration) (capturedAudioChunk, error) {
	r.reads++
	if r.reads > 1 {
		<-r.stop
	}
	return timedChunk(r.source, r.start.Add(time.Duration(r.reads)*time.Second), constSamples(16000, int16(1000*r.reads))), nil
}
func TestCombinedCaptureShutdownKeepsQueuedOpeningBeforeTail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		r := &combinedAudioRecorder{newRecorders: func() []audioRecorder {
			return []audioRecorder{
				&queuedTailRecorder{finalChunkRecorder: finalChunkRecorder{source: backgroundListeningSourceMicrophone}, start: start},
				&queuedTailRecorder{finalChunkRecorder: finalChunkRecorder{source: backgroundListeningSourceSystem}, start: start},
			}
		}}
		if err := r.Start(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait() // one buffered chunk and one in-flight tail per source
		r.Stop()
		w, err := newMeetingAudioWriter(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer w.Abort()
		for _, chunk := range r.drainStoppedChunks() {
			w.WriteCapturedChunk(chunk)
		}
		if w.micBytes != 2*meetingAudioBytesPerSecond || w.sysBytes != 2*meetingAudioBytesPerSecond {
			t.Fatalf("shutdown reordered the queue behind its tail: mic=%d system=%d", w.micBytes, w.sysBytes)
		}
	})
}

func TestLockedCallEndKeepsKnownPauseIdentity(t *testing.T) {
	app := &App{audioScreenLocked: func() bool { return true }, activity: &activityMonitorState{buf: activitybuf.NewRingBuffer(16, time.Hour), inMeeting: true, meetingID: "locked-call", meetingAppName: "Zoom"}}
	app.onMeetingEnded(app.activity)
	if !app.activity.lastMeetingPaused || app.activity.lastMeetingID != "locked-call" {
		t.Fatal("lock boundary lost the call's continuation identity")
	}
	app.abortAutomaticMeetingRecording()
	if app.activity.lastMeetingPaused || app.activity.lastMeetingID != "" {
		t.Fatal("recording opt-out retained a paused call identity")
	}
}

type privacyRetryRecorder struct {
	retryFixtureRecorder
	afterStart func()
}

func (r *privacyRetryRecorder) Start() error {
	err := r.retryFixtureRecorder.Start()
	if err == nil && r.afterStart != nil {
		r.afterStart()
	}
	return err
}
func TestCombinedRetryRespectsPrivacyBeforeAndAfterStart(t *testing.T) {
	for _, phase := range []string{"before retry", "during native start"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var allowed atomic.Bool
				allowed.Store(true)
				source := &privacyRetryRecorder{retryFixtureRecorder: retryFixtureRecorder{finalChunkRecorder: finalChunkRecorder{source: backgroundListeningSourceMicrophone}}}
				if phase == "during native start" {
					source.afterStart = func() { allowed.Store(false) }
				}
				r := &combinedAudioRecorder{newRecorders: func() []audioRecorder { return []audioRecorder{source} }, canStart: func(backgroundListeningSource) bool { return allowed.Load() }}
				if err := r.Start(); err != nil {
					t.Fatal(err)
				}
				if phase == "before retry" {
					allowed.Store(false)
				}
				time.Sleep(time.Second)
				synctest.Wait()
				if len(r.ActiveSources()) != 0 {
					t.Fatal("privacy-closed retry published native capture")
				}
				if phase == "before retry" && source.attempts != 1 {
					t.Fatal("privacy-closed retry opened native capture")
				}
				if phase == "during native start" {
					select {
					case <-source.stop:
					default:
						t.Fatal("privacy change during start did not stop the native source")
					}
				}
				r.Stop()
			})
		})
	}
}

func TestMeetingActivityRotationsKeepRangesAndProvenance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		app := mainChatTestApp(t)
		store := newLifecycleStore(t)
		app.activity = &activityMonitorState{store: store, buf: activitybuf.NewRingBuffer(16, time.Hour), capture: failingScreenCapture{}}
		app.whisper = &whisperState{server: &recordingFixtureEngine{fakeLongForm: fakeLongForm{texts: map[int16]string{2000: "Keep both parts of this call."}}}}
		app.onMeetingStarted(app.activity, meetingdetect.Result{IsMeeting: true, MeetingName: "Zoom", BundleID: "us.zoom.xos"})
		start := time.Now()
		for part := 0; part < 2; part++ {
			time.Sleep(10 * time.Second)
			chunk := timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 2000))
			chunk.CapturedInMeeting = true
			app.writeMeetingAudioChunk(chunk)
			app.activity.buf.Push(activitymon.ActivityEvent{Kind: activitymon.EventClick, Timestamp: time.Now().UnixMilli(), AppName: "Zoom", AppBundleID: "us.zoom.xos"})
			if part == 0 {
				app.extractSession(meetingSliceTrigger)
			} else {
				app.onMeetingEnded(app.activity)
			}
		}
		synctest.Wait()
		rows, err := store.GetSessionsByAge()
		if err != nil || len(rows) != 2 {
			t.Fatalf("activity slices: %v %v", rows, err)
		}
		total := (rows[0].EndTime - rows[0].StartTime) + (rows[1].EndTime - rows[1].StartTime)
		if total != time.Now().Sub(start).Milliseconds() || rows[1].StartTime != rows[0].EndTime {
			t.Fatalf("activity rotations overlap or leave a gap: %+v", rows)
		}
		meta := readMeetingWAVMeta(store.AudioDir(), rows[1].AudioPath)
		if len(meta.ActivitySessions) != 2 {
			t.Fatalf("recording lost its full source envelope: %+v", meta)
		}
		notes, err := app.GetMeetingNotesForSessions([]string{rows[0].ID})
		if err != nil || len(notes) != 1 || len(notes[0].Parts) != 2 {
			t.Fatalf("the notes lost capture lineage: %+v %v", notes, err)
		}
		if transcript, err := app.GetMeetingNotesTranscript(rows[1].ID); err != nil || !strings.Contains(transcript, "Keep both parts of this call.") {
			t.Fatalf("the archive was clipped to the last rotation: %v", err)
		}
		// Saving the same slice again must retain the source-only slice.
		if _, err := app.recordMeetingNotes(rows[1], "Zoom", 12, meta.MeetingID, "", nil); err != nil {
			t.Fatal(err)
		}
		if notes, err := app.GetMeetingNotesForSessions([]string{rows[1].ID}); err != nil || len(notes) != 1 || len(notes[0].Parts) != 2 {
			t.Fatalf("retry lost source lineage: %+v %v", notes, err)
		}
		if _, err := store.db.Exec(`UPDATE activity_sessions SET triage_status = 'protected' WHERE id = ?`, rows[0].ID); err != nil {
			t.Fatal(err)
		}
		if notes, _ := app.GetMeetingNotesForSessions([]string{rows[1].ID}); len(notes) != 0 {
			t.Fatal("Activity released a call whose earlier rotation became protected")
		}
		if _, err := app.GetMeetingNotesTranscript(rows[1].ID); err == nil {
			t.Fatal("the transcript of a call whose earlier rotation became protected was readable")
		}
	})
}

func TestRotatedCallRechecksEverySourceBeforeASR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	store := newLifecycleStore(t)
	engine := &recordingFixtureEngine{fakeLongForm: fakeLongForm{texts: map[int16]string{2000: "Earlier source is now private."}}}
	app.whisper = &whisperState{server: engine}
	app.activity = &activityMonitorState{store: store}
	source := ActivitySessionRow{ID: "earlier-rotation", StartTime: time.Now().Add(-time.Minute).UnixMilli(), EndTime: time.Now().UnixMilli(), AppNames: `["com.example.earlier"]`, TriageStatus: "pending"}
	if err := store.SaveSession(source, nil, nil); err != nil {
		t.Fatal(err)
	}
	w, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	w.SetMeeting("Zoom", "us.zoom.xos", "source-envelope-call")
	w.NoteActivitySession(source.ID)
	w.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 2000)))
	session, meta, ok := app.saveCallRecordingLocked(w, 0, "meeting_ended")
	if !ok {
		t.Fatal("recording was not saved")
	}
	previous := pii.GetUserSensitiveApps()
	defer pii.SetUserSensitiveApps(previous)
	pii.SetUserSensitiveApps(map[string]string{"com.example.earlier": "Earlier"})
	app.saveAutomaticMeetingTranscript(session, nil, meta.App, 0, meta.MeetingID)
	if engine.calls != 0 || transcriptFileExists(session.ID) {
		t.Fatal("a final slice authorized ASR over an earlier protected input")
	}
}

func TestSilentCallStillArchivesItsAudioReference(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	store := newLifecycleStore(t)
	app.activity = &activityMonitorState{store: store}
	app.whisper = &whisperState{server: &recordingFixtureEngine{}}
	w, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	w.SetMeeting("Zoom", "us.zoom.xos", "silent-audio-call")
	w.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 0)))
	session, meta, ok := app.saveCallRecordingLocked(w, 0, "meeting_ended")
	if !ok {
		t.Fatal("silent audio was not saved")
	}
	app.saveAutomaticMeetingTranscript(session, nil, meta.App, 0, meta.MeetingID)
	notes, err := app.GetMeetingNotesForSessions([]string{session.ID})
	if err != nil || len(notes) != 1 || len(notes[0].Parts) != 1 || !notes[0].Parts[0].HasRecording {
		t.Fatalf("a successful empty decode lost the recording on Activity: %+v %v", notes, err)
	}
	row, err := store.GetSessionByID(session.ID)
	if err != nil || row == nil || row.TranscriptPath == "" {
		t.Fatalf("empty archive is not durable: %+v %v", row, err)
	}
}
