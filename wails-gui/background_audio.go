package main

import (
	"encoding/binary"
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"wails-gui/internal/asr"
	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

const (
	// Keep chunks short enough that speech reaches the activity pipeline
	// quickly, while still giving the speech engine enough audio context.
	backgroundAudioChunkDuration = 5 * time.Second
	// Keep the noise gate local to short windows: averaging a whole chunk
	// dilutes brief, quiet speech with the silence around it.
	backgroundAudioNoiseFloor       = 100.0
	backgroundAudioRMSWindowSamples = 320 // 20 ms at the recorders' 16 kHz rate
	backgroundAudioSummaryInterval  = time.Minute
)

type backgroundListeningSource string

const (
	backgroundListeningSourceMicrophone backgroundListeningSource = "microphone"
	backgroundListeningSourceSystem     backgroundListeningSource = "system_audio"
	backgroundListeningSourceBoth       backgroundListeningSource = "both"
)

var backgroundAudioCommonFalsePositives = map[string]struct{}{
	"thank you":           {},
	"thanks":              {},
	"thanks for watching": {},
	"bye":                 {},
	"goodbye":             {},
	"see you":             {},
	"see you next time":   {},
	"as a male":           {},
}

// backgroundAudioCapture manages continuous audio recording, sending chunks
// to the speech engine for transcription, and feeding the resulting text
// into the activity event pipeline as "speech" events.
type backgroundAudioCapture struct {
	mu         sync.Mutex
	running    bool
	discarding bool
	stopCh     chan struct{}
	server     asr.Transcriber
	source     backgroundListeningSource
	recorder   audioRecorder // platform-specific audio capture
	scheduler  *backgroundTranscriptionScheduler
	// onSpeech receives each accepted transcript along with the audio
	// source that produced it — "microphone" for the local user's voice,
	// "system_audio" for remote speakers picked up from Mac output. The
	// transcript pipeline uses the source for speaker diarization.
	//
	// ctx carries the chunk-capture-time context (timestamp, frontmost
	// app, echo flag) so the resulting speech event is attributed to the
	// app that was active when the audio was recorded and the event
	// timestamp reflects the recording window, not whatever happens when
	// transcription finishes 10–30s later.
	onSpeech         func(text string, source backgroundListeningSource, ctx speechCaptureContext)
	onRecordingChunk func(capturedAudioChunk) // durable audio admission, before echo pairing
	onChunk          func(capturedAudioChunk) // called for every non-empty chunk (including silent ones)
	// shouldTranscribe gates which captured chunks are sent to ASR and meeting
	// audio storage. Nil preserves the historical behavior for tests and
	// unsupported builds.
	shouldTranscribe func(capturedAudioChunk) bool
	// annotateChunk attaches capture-time product context, such as the active
	// meeting identity, before the chunk is gated, persisted, or queued. This is
	// deliberately earlier than transcription completion because ASR can lag the
	// recorder by many seconds.
	annotateChunk func(capturedAudioChunk) capturedAudioChunk

	// sample, when set, runs every second while the capture runs: it
	// follows what is in front and who holds the microphone between chunk
	// ends (App.sampleCaptureState).
	micSnapshot        func() micInputSnapshot
	sample             func(time.Time)
	resolveTranscriber func() asr.Transcriber
	allowBatch         func([]scheduledAudioChunk) bool

	// captureLoopDone is closed by captureLoop on full exit, after any
	// pending mic chunk has been flushed through forwardChunkDownstream.
	// stop() waits on this before tearing the scheduler down so the
	// final held chunk reaches the transcription path, not just onChunk.
	captureLoopDone chan struct{}

	// lastSystemAudibleAtUnixNano is the wall-clock time (UnixNano) of the
	// last non-empty system_audio chunk observed by captureLoop. Used to
	// tag concurrent microphone chunks as likely acoustic echo. Stored as
	// int64 so we can access it lock-free from captureLoop's single
	// reader/writer goroutine via atomic ops.
	lastSystemAudibleAtUnixNano atomic.Int64
	// Signal counters and transitions are logged in bounded summaries instead
	// of once per chunk. Guarded by mu; reset whenever capture starts.
	signalStats map[backgroundListeningSource]backgroundAudioSignalStats

	// chunkSubs is the set of on-demand "tap" subscribers that receive a
	// copy of every captured chunk while they're registered. Used by the
	// voice-enrollment path so it can grab N seconds of mic audio
	// WITHOUT opening a second AudioUnit (which CoreAudio doesn't allow
	// — see voice_enrollment.go for the SIGSEGV history). Guarded by
	// chunkSubsMu so SubscribeChunks / UnsubscribeChunks can run
	// concurrently with the capture loop's forwardChunkDownstream
	// without locking the much-hotter main mu.
	chunkSubsMu sync.Mutex
	chunkSubs   []*chunkSubscription
}

type backgroundAudioSignalStats struct {
	chunks         uint64
	asrEligible    uint64
	silenceSkipped uint64
	bytes          uint64
	duration       time.Duration
	audible        bool
	lastSummary    time.Time
}

// chunkSubscription is one registered tap. ch is buffered so a slow
// consumer doesn't back up the capture loop; on overflow we drop the
// new chunk rather than block. wantSource filters by source — empty
// string means "all sources".
type chunkSubscription struct {
	ch         chan capturedAudioChunk
	wantSource backgroundListeningSource
}

// Capture identities are assigned once at the native read boundary. Delivery
// timestamps cannot distinguish a replay from distinct buffered PCM.
var audioCaptureIdentity atomic.Uint64

func nextAudioCaptureIdentity() uint64 { return audioCaptureIdentity.Add(1) }

type capturedAudioChunk struct {
	CaptureID          uint64
	CaptureEvidenceSet bool
	Source             backgroundListeningSource
	WavData            []byte
	Duration           time.Duration
	// MeetingRecordingEpoch binds automatic meeting work to the preference
	// generation that admitted it. A disable/re-enable cycle invalidates queued
	// chunks even if Whisper finishes after recording is turned back on.
	MeetingRecordingEpoch uint64

	// CapturedAt and CapturedApp record when the chunk was captured and
	// which app was frontmost at that moment. Downstream speech events use
	// these so attribution reflects the recording window, not whatever is
	// focused 10–30s later when transcription completes.
	CapturedAt  time.Time
	CapturedApp activitymon.AppInfo
	// ProtectedAppInFront: a protected app was in front at some point while
	// the chunk was recorded, even if another app was in front at its end.
	ProtectedAppInFront bool
	// FocusUnavailable excludes audio until focus can be observed successfully.
	FocusUnavailable bool
	// ListedAppInFront: likewise for an app the user listed under "Apps
	// Isoform does not listen to", in front or holding the microphone.
	ListedAppInFront bool
	// PrivateWindowInFront: a private (incognito) browser window was in
	// front at some point while the chunk was recorded.
	PrivateWindowInFront bool
	// BrowserMeeting is the call the browser in front was on, from its
	// active tab's address, when that browser held the microphone.
	BrowserMeeting string

	// LikelyEcho is set on microphone-source chunks when the system_audio
	// tap reported audible playback inside this chunk's recording window —
	// in that case mic input is overwhelmingly likely to be acoustic echo
	// of the speaker output rather than the user actually speaking. Always
	// false for system_audio chunks.
	LikelyEcho bool

	// OtherMicUser is set on microphone-source chunks when, at capture
	// time, at least one OTHER process on the system was actively reading
	// the audio input device. Detected via CoreAudio's process-object API
	// (macOS 14+) — see concurrent_mic_check_darwin.go. Always false for
	// system_audio chunks, on macOS < 14, and on non-darwin platforms.
	//
	// Used downstream by injectSpeechEvent as a soft-ambient signal: when
	// true AND we're not in a detected meeting, the resulting transcript
	// is marked LikelyEcho (treated as ambient by the triager) rather
	// than hard-dropped, since the user might genuinely be talking to
	// Yansu while another app happens to also have the mic open.
	OtherMicUser bool

	// MicHolders is who else read the microphone when the chunk was
	// captured, on both sources. MeetingAppOnMic names the supported native
	// meeting app (Zoom, Teams) among them; holding the microphone keeps a
	// running call alive and keeps its quiet stretches in the recording.
	// MeetingCallOnMic is set only when that hold also shows call evidence
	// (meeting_call_evidence.go), which is what may start a call. All empty
	// on platforms that cannot name the processes holding the microphone.
	MicHolders              micInputSnapshot
	MeetingAppOnMic         string
	MeetingAppOnMicBundleID string
	MeetingCallOnMic        string

	// CapturedInMeeting and the meeting identity are snapped when the audio
	// chunk is recorded/forwarded, before ASR batching. Whisper may finish
	// 5-30s later, after the user has switched apps or the lifecycle watcher has
	// briefly lost the meeting window, so downstream attribution must not depend
	// on live state at transcription-completion time.
	CapturedInMeeting       bool
	CapturedMeetingAppName  string
	CapturedMeetingBundleID string

	// CapturedMeetingCandidate is a conservative fallback signal used when the
	// lifecycle watcher has not confirmed a meeting but supported meeting-app
	// plus audio evidence is strong enough to keep the speech together for
	// holistic classification at session end.
	CapturedMeetingCandidate bool
	CandidateMeetingAppName  string
	CandidateMeetingReason   string
}

// speechCaptureContext bundles the chunk-recording-time metadata that
// rides along with each accepted transcript through the onSpeech
// callback chain. Bundled as a struct so the signature stays stable as
// new fields (e.g. speaker diarization, captured window bounds) get
// added later — the alternative of adding positional parameters one at
// a time has already churned the lambda signature multiple times in
// this PR.
type speechCaptureContext struct {
	// CapturedAt is the wall-clock time of the chunk that produced this
	// transcript (taken when the chunk left the recorder, not when
	// transcription completed). Used as the speech event timestamp so
	// downstream session/timeline bucketing reflects when the audio was
	// actually recorded.
	CapturedAt time.Time
	// App is the frontmost-app snapshot at chunk-capture time.
	App activitymon.AppInfo
	// LikelyEcho is true when the chunk overlapped audible speaker
	// output and is therefore very likely acoustic echo rather than the
	// user speaking.
	LikelyEcho bool
	// OtherMicUser is true when, at capture time, at least one OTHER
	// process on the system was actively reading the audio input device.
	// See capturedAudioChunk.OtherMicUser for the full rationale.
	OtherMicUser bool
	// MergedWAV is the WAV-encoded audio buffer that produced this
	// transcript (the same bytes that went to ASR). Populated by the
	// scheduler so the App-side speaker verification can extract a
	// fingerprint embedding without re-recording. Nil for legacy paths
	// / tests that don't set it; verification then degrades to "didn't
	// run" rather than failing.
	MergedWAV []byte
	// MeetingRecordingEpoch is copied from the captured audio batch and must
	// still match before its delayed transcript of a call may enter the
	// activity buffer.
	MeetingRecordingEpoch uint64
	// BrowserMeeting is the call page the browser in front was on, when it
	// held the microphone (capturedAudioChunk.BrowserMeeting).
	BrowserMeeting string
	// MicHolders is who else read the microphone while the batch was
	// captured; it names the app the user dictated into.
	MicHolders micInputSnapshot
	// Meeting context at chunk-capture time. Used to keep delayed transcripts
	// tied to the meeting that produced the audio, even if the live meeting
	// watcher changes state before ASR returns.
	InMeeting       bool
	MeetingAppName  string
	MeetingBundleID string
	// Candidate meeting context at chunk-capture time. This does not mark the
	// event as a meeting by itself; session extraction makes the final holistic
	// call after seeing the complete transcript/audio window.
	MeetingCandidate        bool
	CandidateMeetingAppName string
	CandidateMeetingReason  string
}

// microphoneEchoSuppressionWindow is how long after the last audible
// system-audio chunk we treat mic captures as likely acoustic echo. It is
// one chunk duration (so adjacent chunks overlap) plus a small buffer to
// cover latency between the speaker emitting sound and the tap reporting
// it. Tuning trade-off: too short → real echo slips through; too long →
// genuine user speech right after a meeting/video ends is suppressed.
const microphoneEchoSuppressionWindow = backgroundAudioChunkDuration + 2*time.Second

// systemAudioAudibleRMSThreshold is the minimum RMS amplitude (on the
// int16 PCM scale, range 0–32767) for a system_audio chunk to count as
// "the Mac speakers are actively playing audible content". The
// system_audio tap returns a full PCM chunk every cycle regardless of
// whether anything is playing, so we cannot treat "chunk non-empty" as
// "speakers audible" — silent playback chunks are just background noise
// floor. Empirical: total silence RMS ≈ 0, ambient noise floor < 100,
// quiet speech > 500, normal speech/media > 1000. 300 is a conservative
// "definitely something playing" floor that avoids false positives from
// device noise while still catching quiet videos.
const systemAudioAudibleRMSThreshold = 300.0

// wavChunkRMS returns the RMS amplitude of a WAV chunk's PCM payload on
// the int16 scale (0–32767). PCM is assumed to be 16-bit signed
// little-endian (the format produced by both recorders; see
// writeWAVHeader). Returns 0 for malformed or empty buffers.
func wavChunkRMS(wavData []byte) float64 {
	if len(wavData) <= wavHeaderSize {
		return 0
	}
	pcm := wavData[wavHeaderSize:]
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sumSq float64
	for i := 0; i+1 < len(pcm); i += 2 {
		sample := int16(binary.LittleEndian.Uint16(pcm[i : i+2]))
		f := float64(sample)
		sumSq += f * f
	}
	return math.Sqrt(sumSq / float64(n))
}

// wavChunkMaxWindowRMS finds the loudest 20 ms window in the recorders'
// mono int16 PCM. Sliding by one sample avoids an arbitrary window boundary
// diluting a short utterance. Smaller buffers use all available samples.
func wavChunkMaxWindowRMS(wavData []byte) float64 {
	if len(wavData) <= wavHeaderSize {
		return 0
	}
	pcm := wavData[wavHeaderSize:]
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	window := min(n, backgroundAudioRMSWindowSamples)
	var sumSq float64
	for i := 0; i < window; i++ {
		sample := float64(int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2])))
		sumSq += sample * sample
	}
	maxSumSq := sumSq
	for i := window; i < n; i++ {
		entering := float64(int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2])))
		leaving := float64(int16(binary.LittleEndian.Uint16(pcm[(i-window)*2 : (i-window)*2+2])))
		sumSq += entering*entering - leaving*leaving
		maxSumSq = max(maxSumSq, sumSq)
	}
	return math.Sqrt(maxSumSq / float64(window))
}

// audioRecorder is the platform abstraction for capturing raw PCM audio.
// Implementations live in background_audio_darwin.go, background_audio_windows.go, etc.
type audioRecorder interface {
	// Start begins capturing PCM audio (16 kHz, mono, 16-bit signed LE).
	Start() error
	// ReadChunk blocks until chunkDuration of audio has been captured and
	// returns the raw PCM data as a WAV file (with header).
	ReadChunk(chunkDuration time.Duration) (capturedAudioChunk, error)
	// Stop ends the audio capture session.
	Stop()
	// ActiveSources reports which audio inputs are actively feeding this recorder.
	ActiveSources() []backgroundListeningSource
}

// newBackgroundAudioCapture creates a new background audio capture manager.
// The onSpeech callback is invoked with each transcribed text segment.
func normalizeBackgroundListeningSource(raw string) backgroundListeningSource {
	switch backgroundListeningSource(strings.TrimSpace(strings.ToLower(raw))) {
	case backgroundListeningSourceBoth:
		return backgroundListeningSourceBoth
	case backgroundListeningSourceSystem:
		return backgroundListeningSourceSystem
	default:
		return backgroundListeningSourceMicrophone
	}
}

func defaultBackgroundListeningSource() backgroundListeningSource {
	if isBackgroundListeningSourceSupported(backgroundListeningSourceBoth) {
		return backgroundListeningSourceBoth
	}
	if isBackgroundListeningSourceSupported(backgroundListeningSourceMicrophone) {
		return backgroundListeningSourceMicrophone
	}
	return backgroundListeningSourceMicrophone
}

// backgroundListeningSourceNone means nothing is left to capture: the
// microphone is switched off and the Mac cannot capture system audio.
const backgroundListeningSourceNone backgroundListeningSource = ""

func resolveBackgroundListeningSource(cfg yansuConfig) backgroundListeningSource {
	source := defaultBackgroundListeningSource()
	if !cfg.BackgroundMicrophonePaused {
		return source
	}
	return backgroundListeningSourceWithoutMicrophone(source)
}

// backgroundListeningSourceWithoutMicrophone is what remains of a source once
// the microphone is switched off.
func backgroundListeningSourceWithoutMicrophone(source backgroundListeningSource) backgroundListeningSource {
	switch source {
	case backgroundListeningSourceBoth, backgroundListeningSourceSystem:
		return backgroundListeningSourceSystem
	default:
		return backgroundListeningSourceNone
	}
}

func newBackgroundAudioCapture(source backgroundListeningSource, server asr.Transcriber, onSpeech func(text string, source backgroundListeningSource, ctx speechCaptureContext), onChunk func(capturedAudioChunk), shouldTranscribe func(capturedAudioChunk) bool, annotateChunk func(capturedAudioChunk) capturedAudioChunk, keepActive func() bool) *backgroundAudioCapture {
	return &backgroundAudioCapture{
		server:           server,
		stopCh:           make(chan struct{}),
		source:           source,
		recorder:         withSystemAudioIdlePause(newPlatformAudioRecorder(source), keepActive),
		onSpeech:         onSpeech,
		onChunk:          onChunk,
		shouldTranscribe: shouldTranscribe,
		annotateChunk:    annotateChunk,
	}
}

// capturesMicrophone reports whether this capture records the microphone.
func (b *backgroundAudioCapture) capturesMicrophone() bool {
	if b == nil {
		return false
	}
	return b.source == backgroundListeningSourceMicrophone || b.source == backgroundListeningSourceBoth
}

func (b *backgroundAudioCapture) activeSources() []backgroundListeningSource {
	if b == nil || b.recorder == nil {
		return nil
	}
	return b.recorder.ActiveSources()
}

func normalizeBackgroundTranscript(text string) string {
	var b strings.Builder
	lastSpace := true

	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			b.WriteRune(r)
			lastSpace = false
		case !lastSpace:
			b.WriteByte(' ')
			lastSpace = true
		}
	}

	return strings.TrimSpace(b.String())
}

func isRepetitiveTranscript(tokens []string) bool {
	if len(tokens) < 4 {
		return false
	}

	counts := make(map[string]int, len(tokens))
	maxCount := 0
	for _, token := range tokens {
		counts[token]++
		if counts[token] > maxCount {
			maxCount = counts[token]
		}
	}

	return len(counts) <= 2 && maxCount*4 >= len(tokens)*3
}

func shouldDropBackgroundTranscript(text string) bool {
	normalized := normalizeBackgroundTranscript(text)
	if normalized == "" {
		return true
	}
	if _, ok := backgroundAudioCommonFalsePositives[normalized]; ok {
		return true
	}

	tokens := strings.Fields(normalized)
	if isRepetitiveTranscript(tokens) {
		return true
	}

	return false
}

func backgroundTranscriptLogFields(text string) (int, int) {
	trimmed := strings.TrimSpace(text)
	normalized := normalizeBackgroundTranscript(trimmed)
	return utf8.RuneCountInString(trimmed), len(strings.Fields(normalized))
}

// start begins the background capture loop. Audio above the noise floor
// reaches the speech engine, which uses VAD so only speech produces text.
func (b *backgroundAudioCapture) start() error {
	b.mu.Lock()
	if b.running {
		log.Printf("[background-audio] already running, skipping start")
		b.mu.Unlock()
		return nil
	}

	log.Printf("[background-audio] starting recorder...")
	if err := b.recorder.Start(); err != nil {
		log.Printf("[background-audio] recorder.Start() failed: %v", err)
		b.mu.Unlock()
		return err
	}

	b.running = true
	b.discarding = false
	b.signalStats = nil
	b.stopCh = make(chan struct{})
	b.captureLoopDone = make(chan struct{})
	b.scheduler = newBackgroundTranscriptionScheduler(b.server, b.onSpeech)
	b.scheduler.resolveTranscriber = b.resolveTranscriber
	b.scheduler.allowBatch = b.allowBatch
	b.scheduler.start()
	stopCh := b.stopCh
	b.mu.Unlock()

	go b.captureLoop()
	if b.sample != nil {
		go func() {
			ticker := time.NewTicker(captureSampleInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case now := <-ticker.C:
					b.sample(now)
				}
			}
		}()
	}

	log.Printf("[background-audio] started")
	return nil
}

// stop ends the background capture loop. Shutdown order matters: we
// must keep the scheduler reachable until captureLoop's defer has
// flushed any held pendingMic chunk through forwardChunkDownstream,
// otherwise the final mic chunk only reaches onChunk (meeting recorder)
// and is silently dropped from the transcription path.
func (b *backgroundAudioCapture) stop() {
	b.stopWithDrain(true)
}

func (b *backgroundAudioCapture) stopDiscardingQueuedTranscription() {
	b.stopWithDrain(false)
}

func (b *backgroundAudioCapture) stopWithDrain(drain bool) {
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return
	}
	b.running = false
	b.discarding = !drain
	captureDone := b.captureLoopDone
	close(b.stopCh)
	scheduler := b.scheduler
	if !drain {
		b.scheduler = nil
	}
	b.mu.Unlock()

	// Stop the recorder first so a ReadChunk currently blocking inside
	// captureLoop returns immediately (the next iteration sees stopCh
	// and exits). Without this, captureLoop could wait up to one
	// chunkDuration before noticing the stop signal.
	b.recorder.Stop()

	// Wait for captureLoop to fully exit, including its defer that
	// flushes pendingMic into the scheduler.
	if captureDone != nil {
		<-captureDone
	}
	// A combined recorder may still own queued or partial sibling chunks
	// after its producer threads stop. Drain them only on ordinary shutdown;
	// lock and privacy stops continue to discard them all.
	if tail, ok := b.recorder.(interface{ drainStoppedChunks() []capturedAudioChunk }); ok {
		for _, chunk := range tail.drainStoppedChunks() {
			if drain {
				b.forwardChunkDownstream(b.enrichCapturedChunk(chunk))
			}
		}
	}

	b.mu.Lock()
	if drain {
		scheduler = b.scheduler
		b.scheduler = nil
	}
	b.discarding = false
	b.mu.Unlock()

	if scheduler != nil {
		if drain {
			scheduler.stop()
		} else {
			scheduler.discardAndStop()
		}
	}
	b.logCaptureSummaries()
	log.Printf("[background-audio] stopped")
}

// isRunning reports whether the capture loop is active.
func (b *backgroundAudioCapture) isRunning() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}

// captureLoop reads audio chunks and forwards them to the transcription
// pipeline. A single mic chunk may be held briefly so that its sibling
// system_audio chunk (from the same recording window) has a chance to
// arrive and drive echo classification before forwarding.
func (b *backgroundAudioCapture) captureLoop() {
	// captureLoopDone signals stop() that this goroutine has finished
	// — including the pendingMic flush below. Closed last via defer so
	// stop() can safely tear the scheduler down only after the held
	// chunk has been enqueued.
	defer func() {
		b.mu.Lock()
		done := b.captureLoopDone
		b.mu.Unlock()
		if done != nil {
			close(done)
		}
	}()

	// pendingMic holds the most recent mic chunk that hasn't yet been
	// paired with a sibling system_audio chunk. Single-slot rather than
	// a queue because the two readLoops in darwinCombinedAudioRecorder
	// alternate (one chunk per source per ~5s window) so by the time a
	// second mic chunk arrives, the previous pair has resolved one way
	// or the other. Flushed on any of:
	//   * sibling system chunk arrives (echo decided, both forwarded)
	//   * another mic chunk arrives (no sibling came; flush old, hold new)
	//   * captureLoop exits (flush whatever is held)
	var pendingMic *capturedAudioChunk
	defer func() {
		if pendingMic != nil {
			b.enqueueCapturedChunk(*pendingMic)
		}
	}()

	for {
		select {
		case <-b.stopCh:
			return
		default:
		}

		chunk, err := b.recorder.ReadChunk(backgroundAudioChunkDuration)
		if err != nil {
			b.mu.Lock()
			running := b.running
			b.mu.Unlock()
			if !running {
				return
			}
			log.Printf("[background-audio] read chunk failed: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		if len(chunk.WavData) == 0 {
			// recorder.Stop() can also surface as an empty chunk (its
			// internal channel is closed). Re-check stopCh here so we
			// exit promptly instead of spinning on continuous empties.
			select {
			case <-b.stopCh:
				return
			default:
				continue
			}
		}

		chunk = b.enrichCapturedChunk(chunk)

		// Commit admitted audio before holding the microphone for echo pairing.
		// Otherwise a call ending before its sibling arrived lost this chunk.
		var accepted bool
		chunk, accepted = b.recordCapturedChunk(chunk)
		if !accepted {
			continue
		}

		// Tag likely acoustic echo. When the system_audio tap reports a
		// chunk whose PCM is audibly loud, the Mac speakers are actively
		// playing content; any mic chunk captured within the
		// microphoneEchoSuppressionWindow of that playback is mostly
		// mic-picking-up-speakers, not the user speaking. We use
		// CapturedAt (set in the recorder goroutine) for window
		// comparison so the result is immune to consumer-side ordering
		// between the two readLoops.
		//
		// Fail-safes:
		//   * No system_audio tap (perm denied / API failure) →
		//     lastSystemAudibleAtUnixNano stays 0 → nothing marked.
		//   * Tap runs but speakers silent → RMS below threshold →
		//     marker not updated → no false positives.
		chunkCapturedNano := chunk.CapturedAt.UnixNano()
		switch chunk.Source {
		case backgroundListeningSourceSystem:
			if wavChunkRMS(chunk.WavData) >= systemAudioAudibleRMSThreshold {
				b.lastSystemAudibleAtUnixNano.Store(chunkCapturedNano)
				// Retroactively mark any held mic chunk whose recording
				// window overlapped this audible system chunk's window.
				// This closes the "first playback window" race where a
				// mic chunk was consumed from the channel before its
				// sibling system_audio chunk could update the marker.
				if pendingMic != nil {
					delta := chunkCapturedNano - pendingMic.CapturedAt.UnixNano()
					if delta < 0 {
						delta = -delta
					}
					if delta <= microphoneEchoSuppressionWindow.Nanoseconds() {
						pendingMic.LikelyEcho = true
					}
				}
			}
			// Flush the held mic chunk (its echo decision is now final,
			// whether or not this system chunk was audible).
			if pendingMic != nil {
				b.enqueueCapturedChunk(*pendingMic)
				pendingMic = nil
			}
			b.enqueueCapturedChunk(chunk)
		case backgroundListeningSourceMicrophone:
			// Optimistic check: if a system marker already exists in
			// window (the "sibling already arrived" case), mark now.
			// The hold-step below catches the inverse "sibling not yet
			// arrived" case.
			lastSpeakerNano := b.lastSystemAudibleAtUnixNano.Load()
			if lastSpeakerNano > 0 {
				delta := chunkCapturedNano - lastSpeakerNano
				if delta < 0 {
					delta = -delta
				}
				if delta <= microphoneEchoSuppressionWindow.Nanoseconds() {
					chunk.LikelyEcho = true
				}
			}
			// Flush previous pending mic (no sibling system_audio
			// arrived between it and this new mic chunk; further hold
			// is pointless and would just stall transcription).
			if pendingMic != nil {
				b.enqueueCapturedChunk(*pendingMic)
			}
			// Hold this mic chunk so the next system chunk gets a
			// chance to retroactively mark it. The hold is bounded by
			// the next chunk arrival (≤5s under normal operation), so
			// transcription latency for mic chunks goes from "next
			// scheduler tick" to "next-chunk-arrival + scheduler tick".
			held := chunk
			pendingMic = &held
		}
	}
}

// enrichCapturedChunk is shared by normal reads and ordinary shutdown tails.
// Already captured evidence must survive queueing; sampling it again could
// relabel a call as later dictation (or miss a microphone-holder exclusion).
func (b *backgroundAudioCapture) enrichCapturedChunk(chunk capturedAudioChunk) capturedAudioChunk {
	if chunk.CapturedAt.IsZero() {
		chunk.CapturedAt = time.Now()
	}
	if chunk.CapturedApp == (activitymon.AppInfo{}) {
		chunk.CapturedApp = activitymon.FrontmostAppInfo()
	}
	if !chunk.CaptureEvidenceSet {
		var holders micInputSnapshot
		if b.micSnapshot != nil {
			holders = b.micSnapshot()
		} else {
			holders = otherMicInputApps()
		}
		chunk = withMicHolders(chunk, holders, time.Now())
	}
	return chunk
}

// forwardChunkDownstream pushes a fully-decided chunk into the meeting
// audio writer (if any) and the transcription scheduler. Extracted so
// the captureLoop can forward chunks at two different points (system
// chunks immediately, mic chunks after pairing).
func (b *backgroundAudioCapture) forwardChunkDownstream(chunk capturedAudioChunk) {
	if chunk, accepted := b.recordCapturedChunk(chunk); accepted {
		b.enqueueCapturedChunk(chunk)
	}
}

func (b *backgroundAudioCapture) recordCapturedChunk(chunk capturedAudioChunk) (capturedAudioChunk, bool) {
	b.mu.Lock()
	discarding := b.discarding
	b.mu.Unlock()
	if discarding {
		log.Printf("[background-audio] dropping chunk during privacy stop: source=%s bytes=%d", chunk.Source, len(chunk.WavData))
		return chunk, false
	}
	if b.annotateChunk != nil {
		chunk = b.annotateChunk(chunk)
	}
	if b.shouldTranscribe != nil && !b.shouldTranscribe(chunk) {
		return chunk, false
	}
	if b.onRecordingChunk != nil {
		b.onRecordingChunk(chunk)
	}
	// Fan out to any on-demand tap subscribers BEFORE the scheduler
	// enqueue so a tap can still get the chunk even if the scheduler is
	// momentarily backed up. Send is non-blocking — a slow consumer's
	// dropped chunks are preferable to stalling the capture loop.
	b.chunkSubsMu.Lock()
	subs := append([]*chunkSubscription(nil), b.chunkSubs...)
	b.chunkSubsMu.Unlock()
	for _, sub := range subs {
		if sub.wantSource != "" && sub.wantSource != chunk.Source {
			continue
		}
		select {
		case sub.ch <- chunk:
		default:
			// Drop on full — the tap's own buffer size is the contract.
		}
	}
	return chunk, true
}

// enqueueCapturedChunk handles only live ASR; recording already owns the bytes.
func (b *backgroundAudioCapture) enqueueCapturedChunk(chunk capturedAudioChunk) {
	b.mu.Lock()
	discarding := b.discarding
	b.mu.Unlock()
	if discarding {
		return
	}
	if b.onChunk != nil {
		b.onChunk(chunk)
	}
	// Preserve silence in recording/tap consumers so their timelines stay
	// aligned, but never spend ASR work on noise-floor audio.
	if !b.logChunkSignalChange(chunk) {
		return
	}
	b.mu.Lock()
	scheduler := b.scheduler
	b.mu.Unlock()
	if scheduler == nil || !scheduler.enqueue(chunk) {
		log.Printf("[background-audio] transcription scheduler unavailable, skipping chunk: source=%s bytes=%d", chunk.Source, len(chunk.WavData))
	}
}

// subscribeChunks registers a temporary subscriber that receives every
// chunk matching wantSource (or every chunk when wantSource is empty)
// for as long as the returned unsubscribe func hasn't been called.
// bufferSize sets the channel buffer — chunks arrive every ~5s on
// macOS, so 4 is plenty for a 10-15s enrollment with margin. The
// caller MUST invoke unsubscribe to avoid leaking the subscription.
func (b *backgroundAudioCapture) subscribeChunks(
	wantSource backgroundListeningSource,
	bufferSize int,
) (<-chan capturedAudioChunk, func()) {
	if bufferSize <= 0 {
		bufferSize = 4
	}
	sub := &chunkSubscription{
		ch:         make(chan capturedAudioChunk, bufferSize),
		wantSource: wantSource,
	}
	b.chunkSubsMu.Lock()
	b.chunkSubs = append(b.chunkSubs, sub)
	b.chunkSubsMu.Unlock()
	unsubscribe := func() {
		b.chunkSubsMu.Lock()
		for i, s := range b.chunkSubs {
			if s == sub {
				b.chunkSubs = append(b.chunkSubs[:i], b.chunkSubs[i+1:]...)
				break
			}
		}
		b.chunkSubsMu.Unlock()
		// Drain so any in-flight send doesn't block (subscribers can be
		// re-subscribed safely since the channel isn't closed here).
		for {
			select {
			case <-sub.ch:
			default:
				return
			}
		}
	}
	return sub.ch, unsubscribe
}

func (b *backgroundAudioCapture) logChunkSignalChange(chunk capturedAudioChunk) bool {
	rms := wavChunkMaxWindowRMS(chunk.WavData)
	audible := rms >= backgroundAudioNoiseFloor
	now := chunk.CapturedAt
	if now.IsZero() {
		now = time.Now()
	}
	b.mu.Lock()
	if b.signalStats == nil {
		b.signalStats = make(map[backgroundListeningSource]backgroundAudioSignalStats)
	}
	stats := b.signalStats[chunk.Source]
	wasAudible := stats.audible
	stats.audible = audible
	stats.chunks++
	stats.bytes += uint64(len(chunk.WavData))
	stats.duration += chunk.Duration
	if audible {
		stats.asrEligible++
	} else {
		stats.silenceSkipped++
	}
	if stats.lastSummary.IsZero() {
		stats.lastSummary = now
	}
	summaryDue := now.Sub(stats.lastSummary) >= backgroundAudioSummaryInterval
	if summaryDue {
		stats.lastSummary = now
	}
	b.signalStats[chunk.Source] = stats
	b.mu.Unlock()
	if audible && !wasAudible {
		log.Printf("[background-audio] first sound after silence: source=%s bytes=%d duration=%s max_window_rms=%.0f chunks=%d silence_skipped=%d", chunk.Source, len(chunk.WavData), chunk.Duration, rms, stats.chunks, stats.silenceSkipped)
	}
	if summaryDue {
		logBackgroundAudioSignalSummary(chunk.Source, stats)
	}
	return audible
}

func logBackgroundAudioSignalSummary(source backgroundListeningSource, stats backgroundAudioSignalStats) {
	log.Printf("[background-audio] capture summary: source=%s chunks=%d asr_eligible=%d silence_skipped=%d bytes=%d duration=%s", source, stats.chunks, stats.asrEligible, stats.silenceSkipped, stats.bytes, stats.duration)
}

func (b *backgroundAudioCapture) logCaptureSummaries() {
	b.mu.Lock()
	stats := make(map[backgroundListeningSource]backgroundAudioSignalStats, len(b.signalStats))
	for source, summary := range b.signalStats {
		stats[source] = summary
	}
	b.mu.Unlock()
	for source, summary := range stats {
		logBackgroundAudioSignalSummary(source, summary)
	}
}

// injectSpeechEvent creates an ActivityEvent of kind "speech" and pushes it
// into the activity ring buffer so it flows through the standard
// session -> triager -> memory pipeline.
//
// `source` identifies which audio capture produced the transcript —
// "microphone" = local user's voice, "system_audio" = remote speakers
// captured from the Mac's output. The transcript renderer reads this
// to label each line "Me:" vs "Other:" for speaker diarization.
//
// To prevent data loss during long continuous sessions (e.g. multi-hour
// meetings), a periodic flush forces an extractSession every
// speechFlushInterval so speech events are persisted before the ring
// buffer's time-window eviction can discard them.
func (a *App) injectSpeechEvent(text string, source backgroundListeningSource, ctx speechCaptureContext) {
	// Call speech belongs to the recording generation that captured it;
	// speech outside calls only needs listening itself to be allowed.
	callSpeech := ctx.InMeeting || ctx.MeetingCandidate
	if (callSpeech && !a.automaticMeetingRecordingEpochIsCurrent(ctx.MeetingRecordingEpoch)) || a.IsClosedEyesModeEnabled() || a.isBackgroundListeningScreenLocked() {
		return
	}
	// Turning Listen or the microphone off drains the queue: what was still
	// waiting for speech recognition is not kept.
	cfg := readAudioConfig()
	if !cfg.allowsSource(source) {
		return
	}
	// Likewise the computer's own sound outside calls, after its switch is
	// turned off.
	if source == backgroundListeningSourceSystem && !callSpeech && !cfg.listenOutsideCalls(source) {
		return
	}
	if a.activity == nil {
		a.initActivityMonitor()
	}
	a.ensureActivityTriagerStarted()
	if a.activity == nil || a.activity.buf == nil {
		return
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}

	// Dictation (into Claude, Codex, ChatGPT, or through a voice-typing
	// helper such as Wispr Flow) is kept and labeled with the app the words
	// went to, so the activity reads "dictated to Claude", never "a call".
	// ctx.App is the recording-moment snapshot (set in captureLoop), not the
	// transcription-completion one.
	dictationApp := ""
	if source == backgroundListeningSourceMicrophone && !callSpeech && !a.browserMayBeInACall(ctx) {
		dictationApp = speechDictationTarget(ctx.App, ctx.MicHolders, ctx.OtherMicUser)
	}

	// nowMs drives flush-interval / lastSpeech bookkeeping (all of which
	// reasons about "how long since speech-related activity"); always use
	// wall clock for those. eventTsMs is the value persisted on the
	// ActivityEvent itself and must reflect recording time so events fall
	// into the session they actually belong to — transcription latency is
	// up to 30s, which is enough to misroute events across session
	// boundaries.
	nowMs := time.Now().UnixMilli()
	eventTsMs := nowMs
	if !ctx.CapturedAt.IsZero() {
		eventTsMs = ctx.CapturedAt.UnixMilli()
	}

	// Prefer the app snapshot taken when the audio chunk was captured —
	// transcription is batched up to 30s, so the live frontmost is often
	// unrelated to where the audio actually came from. Only fall back to
	// live frontmost when no captured context was supplied (legacy paths,
	// tests).
	appInfo := ctx.App
	if appInfo.BundleID == "" && appInfo.AppName == "" && appInfo.WindowTitle == "" {
		appInfo = activitymon.FrontmostAppInfo()
	}

	// Read meeting state owned by the lifecycle watcher, then let the
	// chunk-capture snapshot override it when ASR returns late. The watcher
	// polls active meeting windows independently, but transcription completion
	// can lag far enough that live state may no longer reflect the audio source.
	// Speech the capture did not place in the call (dictation after the
	// call's app let go of the microphone) is never tagged with the call.
	a.activity.mu.Lock()
	liveInMeeting := a.activity.inMeeting && a.activity.meetingRecordingEpoch == ctx.MeetingRecordingEpoch
	meetingAppName := a.activity.meetingAppName
	meetingBundleID := a.activity.meetingBundleID
	firstSpeech := a.activity.firstSpeechTime
	candidateCall := a.activity.meetingCandidateActive && a.activity.meetingCandidateRecordingEpoch == ctx.MeetingRecordingEpoch
	lastMeetingEndMs := a.activity.lastMeetingEndMs
	a.activity.mu.Unlock()
	// Transcription runs behind capture: speech of a call that has already
	// ended and been saved arrives after it. The call's recording holds that
	// audio; the words stay ordinary activity, so they never turn the next
	// session into a call of their own.
	if callSpeech && !liveInMeeting && !candidateCall && lastMeetingEndMs > 0 && eventTsMs <= lastMeetingEndMs {
		callSpeech = false
		ctx.InMeeting = false
		ctx.MeetingCandidate = false
	}
	inMeeting := callSpeech && liveInMeeting
	if ctx.InMeeting && meetingdetect.IsTranscriptionSupportedMeetingName(ctx.MeetingAppName) {
		inMeeting = true
		if ctx.MeetingAppName != "" {
			meetingAppName = ctx.MeetingAppName
		}
		if ctx.MeetingBundleID != "" {
			meetingBundleID = ctx.MeetingBundleID
		}
	}

	// Soft-ambient mark: when the chunk was captured while another process
	// on the system was actively reading the mic AND we're not in a
	// detected meeting, the audio is probably the user dictating into
	// another app (Wispr/Typeless/ChatGPT desktop voice/etc.) rather than
	// addressing Yansu. Conservative — we set LikelyEcho (treated as
	// ambient by isLikelyNonUserSpeech) instead of hard-dropping because
	// the user MIGHT be genuinely talking to Yansu while another app
	// happens to also have the mic open. In a known meeting we trust the
	// meeting context and leave the flag alone. Only applies to the
	// microphone source; system_audio chunks never set OtherMicUser.
	if source == backgroundListeningSourceMicrophone && ctx.OtherMicUser && !inMeeting && !ctx.MeetingCandidate && !ctx.LikelyEcho && dictationApp == "" {
		ctx.LikelyEcho = true
		log.Printf(
			"[background-audio] marked speech as ambient: another app using mic and not in meeting (app=%q bundle=%s)",
			appInfo.AppName, appInfo.BundleID,
		)
	}

	// During meetings use a longer flush interval (2h vs 10m default) so the
	// ring-buffer safety valve doesn't fragment a long call. A candidate call
	// (the watcher has not confirmed it yet) counts too: the 10-minute flush
	// cut such calls into 10-minute transcripts.
	callInProgress := inMeeting || candidateCall || ctx.MeetingCandidate
	flushInterval := speechFlushInterval
	if callInProgress {
		flushInterval = meetingFlushInterval
	}

	if firstSpeech > 0 && nowMs-firstSpeech >= flushInterval.Milliseconds() {
		triggerKind := "speech_idle_transition"
		if callInProgress {
			triggerKind = meetingSliceTrigger
		}
		log.Printf("[background-audio] periodic flush: speech accumulating for %s, flushing to DB (meeting=%v)",
			time.Duration(nowMs-firstSpeech)*time.Millisecond, callInProgress)
		// A meeting_flush ends a slice of the call, not the call: the
		// meeting state, its recording generation and the recording itself
		// continue (extractSession).
		a.extractSession(triggerKind)
	}
	appName := appInfo.AppName
	if appName == "" {
		appName = "Background audio"
	}
	windowTitle := appInfo.WindowTitle
	// A private or incognito window's title is never stored, here either.
	if windowTitle == "" || activitymon.IsPrivateBrowsing(windowTitle) {
		windowTitle = "Background listening"
	}

	// When the watcher has flagged us as in a meeting, tag the speech event
	// with the meeting app rather than whatever was frontmost. This keeps
	// detectMeetingInEvents accurate even when the user takes notes in
	// another window during a call.
	evtBundleID := appInfo.BundleID
	evtAppName := appName
	if inMeeting && meetingBundleID != "" {
		evtBundleID = meetingBundleID
		if meetingAppName != "" {
			evtAppName = meetingAppName
		}
	}

	// Voice fingerprint check + embedding cache. Both apply to mic-
	// source chunks only. Verified verdict drives isLikelyNonUserSpeech
	// (so received-mode hard gate kicks in); embedding is cached on the
	// event so the Activity-detail diarization view can cluster voices
	// after the fact without re-running extraction.
	var (
		speakerVerified  *bool
		speakerEmbedding []float32
	)
	if source == backgroundListeningSourceMicrophone {
		if len(ctx.MergedWAV) > 44 {
			speakerVerified, speakerEmbedding = a.verifySpeakerOnWAV(ctx.MergedWAV)
		} else {
			log.Printf("[voice-verify] skip: scheduler delivered no MergedWAV (bytes=%d) — pipeline plumbing bug", len(ctx.MergedWAV))
		}
	}

	// Drop speech text when the frontmost app is protected, matching the
	// AX-text and typed-text gates. Check the REAL frontmost bundle
	// (appInfo.BundleID), not evtBundleID — during meetings evtBundleID is
	// rewritten to the meeting app (Zoom/Teams) above, which would defeat
	// the check while the user dictates into a protected app mid-call.
	speechText := trimmed
	if _, isSensitive := pii.IsSensitiveApp(appInfo.BundleID); isSensitive {
		speechText = ""
	}
	// Speech while a private or incognito window is in front (dictation into
	// it, a video in it) is kept no more than typing there, unless a call
	// that started elsewhere is running: no call starts in a private window
	// (browserWindowOpenToCalls), so a running call always did.
	if !inMeeting && activitymon.IsPrivateBrowsing(appInfo.WindowTitle) {
		speechText = ""
	}

	evt := activitymon.ActivityEvent{
		Timestamp:              eventTsMs,
		Kind:                   activitymon.EventSpeech,
		AppBundleID:            evtBundleID,
		AppName:                evtAppName,
		WindowTitle:            windowTitle,
		SpeechText:             speechText,
		SpeechSource:           string(source),
		SpeechLikelyEcho:       ctx.LikelyEcho,
		SpeechInCall:           callSpeech,
		SpeechDictationApp:     dictationApp,
		SpeechSpeakerVerified:  speakerVerified,
		SpeechSpeakerEmbedding: speakerEmbedding,
		MeetingRecordingEpoch:  ctx.MeetingRecordingEpoch,
	}
	if ctx.MeetingCandidate && meetingdetect.IsTranscriptionSupportedMeetingName(ctx.CandidateMeetingAppName) {
		evt.MeetingCandidate = true
		evt.MeetingCandidateApp = ctx.CandidateMeetingAppName
		evt.MeetingCandidateReason = ctx.CandidateMeetingReason
	}

	// Serialize the final admission with preference changes. Expensive ASR and
	// speaker verification happen before this short fence; if the user toggled
	// recording while they ran, the captured generation is rejected here.
	a.meetingRecordingMu.Lock()
	defer a.meetingRecordingMu.Unlock()
	if evt.SpeechInCall && !a.automaticMeetingRecordingEpochIsCurrent(ctx.MeetingRecordingEpoch) {
		return
	}
	a.activity.buf.Push(evt)
	a.activity.mu.Lock()
	a.activity.lastEventTime = evt.Timestamp
	a.activity.lastSpeechMs = nowMs
	if a.activity.firstSpeechTime == 0 {
		a.activity.firstSpeechTime = nowMs
	}
	a.armActivityIdleTimerLocked(a.activity, "speech_idle_transition")
	a.activity.mu.Unlock()
	charCount, wordCount := backgroundTranscriptLogFields(trimmed)
	log.Printf("[background-audio] queued speech event: chars=%d words=%d", charCount, wordCount)
}
