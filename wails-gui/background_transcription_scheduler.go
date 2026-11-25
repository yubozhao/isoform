package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wails-gui/internal/asr"
	"wails-gui/internal/platform/activitymon"
)

// Transcript content is private, including rejected recognition results.
// Only an explicit, process-local debugging opt-in may put it in app logs.
const backgroundTranscriptDebugEnv = "YANSU_DEBUG_TRANSCRIPTS"

func backgroundTranscriptDebugLogSuffix(text string) string {
	if os.Getenv(backgroundTranscriptDebugEnv) != "1" {
		return ""
	}
	return fmt.Sprintf(" text=%q", truncateForLog(text, 120))
}

const (
	backgroundTranscriptionTargetBatchDuration = 10 * time.Second
	backgroundTranscriptionMaxAdditionalWait   = 2 * time.Second
	// Prefer transcript completeness over freshness: if Whisper falls behind,
	// keep a larger backlog instead of dropping older meeting audio.
	backgroundTranscriptionMaxQueueDuration = 2 * time.Minute
	// backgroundTranscriptionStopDrainTimeout caps how long stop() waits
	// for the run-loop's in-flight processBatch + post-stop drain to
	// complete. asr.Transcriber.TranscribeBackground has no context
	// support and can hang on a stuck Whisper engine; without a cap,
	// capture.stop()/stopBackgroundListening() — and any UI flow that
	// awaits them — would deadlock. 60s favors preserving the final words of
	// a meeting when local transcription is slow; in the hung-engine case the
	// remaining batches are lost rather than
	// freezing the app.
	backgroundTranscriptionStopDrainTimeout = 60 * time.Second
)

type backgroundTranscriptionSchedulerConfig struct {
	targetBatchDuration              time.Duration
	maxAdditionalWaitAfterFirstChunk time.Duration
	maxQueueDurationPerSource        time.Duration
	stopDrainTimeout                 time.Duration
}

func defaultBackgroundTranscriptionSchedulerConfig() backgroundTranscriptionSchedulerConfig {
	return backgroundTranscriptionSchedulerConfig{
		targetBatchDuration:              backgroundTranscriptionTargetBatchDuration,
		maxAdditionalWaitAfterFirstChunk: backgroundTranscriptionMaxAdditionalWait,
		maxQueueDurationPerSource:        backgroundTranscriptionMaxQueueDuration,
		stopDrainTimeout:                 backgroundTranscriptionStopDrainTimeout,
	}
}

type scheduledAudioChunk struct {
	source          backgroundListeningSource
	wavData         []byte
	duration        time.Duration
	enqueuedAt      time.Time
	capturedAt      time.Time
	capturedApp     activitymon.AppInfo
	likelyEcho      bool
	otherMicUser    bool
	inMeeting       bool
	meetingApp      string
	meetingBundle   string
	candidate       bool
	candidateApp    string
	candidateReason string
	recordingEpoch  uint64
	micHolders      micInputSnapshot
	browserMeeting  string
}

type backgroundTranscriptionScheduler struct {
	mu                 sync.Mutex
	cfg                backgroundTranscriptionSchedulerConfig
	transcriber        asr.Transcriber
	resolveTranscriber func() asr.Transcriber
	allowBatch         func([]scheduledAudioChunk) bool
	onSpeech           func(text string, source backgroundListeningSource, ctx speechCaptureContext)
	queues             map[backgroundListeningSource][]scheduledAudioChunk
	durations          map[backgroundListeningSource]time.Duration
	nextSource         int
	stopped            bool

	// abandoned is set by stop() when the doneCh wait times out (i.e.
	// Whisper is stuck and we returned early to unblock shutdown). Any
	// post-timeout transcript completion must NOT fire onSpeech because
	// the downstream activity pipeline may have started/finished
	// teardown by the time the leaked run-loop goroutine resumes. Read
	// inside processBatch and the drain loop.
	abandoned atomic.Bool

	notifyCh chan struct{}
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newBackgroundTranscriptionScheduler(transcriber asr.Transcriber, onSpeech func(text string, source backgroundListeningSource, ctx speechCaptureContext)) *backgroundTranscriptionScheduler {
	return newBackgroundTranscriptionSchedulerWithConfig(transcriber, onSpeech, defaultBackgroundTranscriptionSchedulerConfig())
}

func newBackgroundTranscriptionSchedulerWithConfig(transcriber asr.Transcriber, onSpeech func(text string, source backgroundListeningSource, ctx speechCaptureContext), cfg backgroundTranscriptionSchedulerConfig) *backgroundTranscriptionScheduler {
	if cfg.targetBatchDuration <= 0 {
		cfg.targetBatchDuration = backgroundAudioChunkDuration
	}
	if cfg.maxAdditionalWaitAfterFirstChunk < 0 {
		cfg.maxAdditionalWaitAfterFirstChunk = 0
	}
	if cfg.maxQueueDurationPerSource <= 0 {
		cfg.maxQueueDurationPerSource = backgroundTranscriptionMaxQueueDuration
	}
	if cfg.stopDrainTimeout <= 0 {
		cfg.stopDrainTimeout = backgroundTranscriptionStopDrainTimeout
	}
	return &backgroundTranscriptionScheduler{
		cfg:         cfg,
		transcriber: transcriber,
		onSpeech:    onSpeech,
		queues: map[backgroundListeningSource][]scheduledAudioChunk{
			backgroundListeningSourceMicrophone: nil,
			backgroundListeningSourceSystem:     nil,
		},
		durations: map[backgroundListeningSource]time.Duration{
			backgroundListeningSourceMicrophone: 0,
			backgroundListeningSourceSystem:     0,
		},
		notifyCh: make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

func (s *backgroundTranscriptionScheduler) start() {
	go s.run()
}

// stop signals the run-loop to drain any queued chunks and exit. It
// blocks until the run-loop has fully exited (doneCh closed), so by the
// time stop returns, all chunks enqueued before stop are either
// transcribed or have been processed through processBatch's normal
// error/skip paths. Used during shutdown so the captureLoop pendingMic
// flush isn't silently dropped.
//
// Bounded by cfg.stopDrainTimeout: asr.Transcriber.TranscribeBackground
// has no cancel/context, so a stuck Whisper engine could otherwise
// hang capture.stop() and the UI flow above it indefinitely. On
// timeout, in-flight batches are abandoned (the run-loop goroutine
// leaks until Whisper eventually returns or the process exits) and we
// log a CRITICAL line so it shows up in support reports.
func (s *backgroundTranscriptionScheduler) stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	close(s.stopCh)
	timeout := s.cfg.stopDrainTimeout
	s.mu.Unlock()

	if timeout <= 0 {
		<-s.doneCh
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.doneCh:
	case <-timer.C:
		// Mark abandoned BEFORE returning so that the leaked
		// run-loop goroutine (still stuck inside TranscribeBackground)
		// will skip onSpeech when/if it eventually resumes. Without
		// this flag, a late transcript could surface long after the
		// caller treats the scheduler as torn down.
		s.abandoned.Store(true)
		log.Printf("[background-audio][CRITICAL] scheduler stop drain timed out after %s; abandoning in-flight transcription to unblock shutdown", timeout)
	}
}

func (s *backgroundTranscriptionScheduler) discardAndStop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.abandoned.Store(true)
	for source := range s.queues {
		s.queues[source] = nil
		s.durations[source] = 0
	}
	close(s.stopCh)
	timeout := s.cfg.stopDrainTimeout
	s.mu.Unlock()

	if timeout <= 0 {
		<-s.doneCh
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.doneCh:
	case <-timer.C:
		log.Printf("[background-audio][CRITICAL] scheduler discard timed out after %s; abandoning in-flight transcription during privacy stop", timeout)
	}
}

func (s *backgroundTranscriptionScheduler) enqueue(chunk capturedAudioChunk) bool {
	if len(chunk.WavData) == 0 {
		return false
	}
	source := normalizeQueuedBackgroundSource(chunk.Source)
	duration := chunk.Duration
	if duration <= 0 {
		duration = backgroundAudioChunkDuration
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}

	item := scheduledAudioChunk{
		source:          source,
		wavData:         chunk.WavData,
		duration:        duration,
		enqueuedAt:      time.Now(),
		capturedAt:      chunk.CapturedAt,
		capturedApp:     chunk.CapturedApp,
		likelyEcho:      chunk.LikelyEcho,
		otherMicUser:    chunk.OtherMicUser,
		inMeeting:       chunk.CapturedInMeeting,
		meetingApp:      chunk.CapturedMeetingAppName,
		meetingBundle:   chunk.CapturedMeetingBundleID,
		candidate:       chunk.CapturedMeetingCandidate,
		candidateApp:    chunk.CandidateMeetingAppName,
		candidateReason: chunk.CandidateMeetingReason,
		recordingEpoch:  chunk.MeetingRecordingEpoch,
		micHolders:      chunk.MicHolders,
		browserMeeting:  chunk.BrowserMeeting,
	}
	s.queues[source] = append(s.queues[source], item)
	s.durations[source] += duration

	dropped := s.trimQueueLocked(source)
	activityPipelineMetrics.recordAudioQueued(source, len(s.queues[source]), s.durations[source])
	if dropped > 0 {
		activityPipelineMetrics.recordAudioDropped(source, dropped, len(s.queues[source]), s.durations[source])
		log.Printf("[background-audio] dropped %d queued chunk(s) for source=%s to keep queue under %s", dropped, source, s.cfg.maxQueueDurationPerSource)
	}
	s.signalLocked()
	return true
}

func (s *backgroundTranscriptionScheduler) run() {
	defer close(s.doneCh)

	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		batch, wait := s.nextBatch()
		if len(batch) > 0 {
			s.processBatch(batch)
			continue
		}

		if wait <= 0 {
			wait = time.Hour
		}
		timer.Reset(wait)
		select {
		case <-s.stopCh:
			// Drain any leftover queued chunks before exit. captureLoop's
			// stop-time pendingMic flush may have enqueued a chunk just
			// after s.stopped flipped but before this select observed
			// the stopCh close; either way, ignoring it would silently
			// drop the user's last bit of audio. Loop until both source
			// queues are empty.
			//
			// Bail out if stop() flips abandoned mid-drain (the caller
			// gave up waiting). Continuing would only burn CPU on
			// Whisper calls whose onSpeech results we'd discard anyway.
			for {
				if s.abandoned.Load() {
					return
				}
				batch := s.popDrainBatchLocked()
				if len(batch) == 0 {
					return
				}
				s.processBatch(batch)
			}
		case <-s.notifyCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (s *backgroundTranscriptionScheduler) nextBatch() ([]scheduledAudioChunk, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil, 0
	}

	now := time.Now()
	sources := []backgroundListeningSource{backgroundListeningSourceMicrophone, backgroundListeningSourceSystem}
	var earliestWait time.Duration
	for i := range sources {
		idx := (s.nextSource + i) % len(sources)
		source := sources[idx]
		q := s.queues[source]
		if len(q) == 0 {
			continue
		}
		waited := now.Sub(q[0].enqueuedAt)
		ready := s.durations[source] >= s.cfg.targetBatchDuration || waited >= s.cfg.maxAdditionalWaitAfterFirstChunk
		if !ready {
			remaining := s.cfg.maxAdditionalWaitAfterFirstChunk - waited
			if earliestWait == 0 || remaining < earliestWait {
				earliestWait = remaining
			}
			continue
		}

		batch, consumed := s.popBatchLocked(source)
		s.nextSource = (idx + 1) % len(sources)
		activityPipelineMetrics.recordAudioDequeued(source, len(s.queues[source]), s.durations[source], len(batch))
		log.Printf(
			"[background-audio] transcription batch ready: source=%s chunks=%d duration=%s bytes=%d queue_remaining=%s",
			source,
			len(batch),
			consumed,
			totalScheduledAudioBytes(batch),
			s.durations[source],
		)
		return batch, 0
	}
	return nil, earliestWait
}

// popDrainBatchLocked pops the next non-empty queue as a batch,
// ignoring the duration thresholds nextBatch enforces. Used exclusively
// by the stop-time drain in run() — outside that path, normal batching
// rules apply. Returns nil when all queues are empty.
//
// Despite the "Locked" suffix matching the file convention for helpers
// that assume the mutex is held, this function takes the mutex itself
// because it's called from run() outside any locked block.
func (s *backgroundTranscriptionScheduler) popDrainBatchLocked() []scheduledAudioChunk {
	s.mu.Lock()
	defer s.mu.Unlock()
	sources := []backgroundListeningSource{backgroundListeningSourceMicrophone, backgroundListeningSourceSystem}
	for _, source := range sources {
		if len(s.queues[source]) == 0 {
			continue
		}
		batch, consumed := s.popBatchLocked(source)
		activityPipelineMetrics.recordAudioDequeued(source, len(s.queues[source]), s.durations[source], len(batch))
		log.Printf(
			"[background-audio] stop-drain batch: source=%s chunks=%d duration=%s queue_remaining=%s",
			source,
			len(batch),
			consumed,
			s.durations[source],
		)
		return batch
	}
	return nil
}

func (s *backgroundTranscriptionScheduler) popBatchLocked(source backgroundListeningSource) ([]scheduledAudioChunk, time.Duration) {
	q := s.queues[source]
	if len(q) == 0 {
		return nil, 0
	}

	var consumed time.Duration
	count := 0
	epoch := q[0].recordingEpoch
	for count < len(q) {
		if q[count].recordingEpoch != epoch {
			break
		}
		if count > 0 && consumed >= s.cfg.targetBatchDuration {
			break
		}
		consumed += q[count].duration
		count++
		if consumed >= s.cfg.targetBatchDuration {
			break
		}
	}
	if count == 0 {
		count = 1
		consumed = q[0].duration
	}

	batch := append([]scheduledAudioChunk(nil), q[:count]...)
	remaining := append([]scheduledAudioChunk(nil), q[count:]...)
	s.queues[source] = remaining
	s.durations[source] -= consumed
	if s.durations[source] < 0 || len(remaining) == 0 {
		s.durations[source] = 0
	}
	return batch, consumed
}

// processBatch transcribes a batch and dispatches the resulting text
// through onSpeech. It does not consult stopCh after transcription
// returns — scheduler.stop() already synchronizes on doneCh, so any
// in-flight processBatch is allowed to finish (including onSpeech)
// before stop returns; this both delivers the user's last spoken words
// to the activity pipeline and matches the stop-drain semantics so the
// two code paths can't disagree.
func (s *backgroundTranscriptionScheduler) processBatch(batch []scheduledAudioChunk) {
	if len(batch) == 0 || (s.allowBatch != nil && !s.allowBatch(batch)) {
		return
	}
	source := batch[0].source
	transcriber := s.transcriber
	if s.resolveTranscriber != nil {
		transcriber = s.resolveTranscriber()
	}
	if transcriber == nil || !transcriber.IsRunning() {
		log.Printf("[background-audio] speech engine not available, dropping transcription batch: source=%s chunks=%d", source, len(batch))
		return
	}

	wavData, err := mergeScheduledWAVChunks(batch)
	if err != nil {
		log.Printf("[background-audio] merge transcription batch failed: source=%s chunks=%d err=%v", source, len(batch), err)
		return
	}

	start := activityPipelineMetrics.recordWhisperStart()
	text, err := transcriber.TranscribeBackground(wavData, string(source))
	activityPipelineMetrics.recordWhisperDone(start, err)
	if err != nil {
		log.Printf("[background-audio] transcription failed: source=%s chunks=%d err=%v", source, len(batch), err)
		return
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		activityPipelineMetrics.whisperEmptyTranscripts.Add(1)
		log.Printf(
			"[background-audio] empty transcript returned: source=%s chunks=%d bytes=%d",
			source,
			len(batch),
			len(wavData),
		)
		return
	}
	if shouldDropBackgroundTranscript(trimmed) {
		activityPipelineMetrics.whisperDroppedTranscripts.Add(1)
		charCount, wordCount := backgroundTranscriptLogFields(trimmed)
		log.Printf("[background-audio] dropped likely non-speech transcript: source=%s chunks=%d chars=%d words=%d%s", source, len(batch), charCount, wordCount, backgroundTranscriptDebugLogSuffix(trimmed))
		return
	}

	// If stop() already gave up on this scheduler (timeout fired and
	// returned), do not fire onSpeech for late transcripts — the
	// downstream activity pipeline may be torn down or in an unsafe
	// state. The transcript itself is acknowledged via the metric +
	// log so support reports still show what was captured; only the
	// callback that would leak into the activity buffer is skipped.
	if s.abandoned.Load() {
		charCount, wordCount := backgroundTranscriptLogFields(trimmed)
		log.Printf("[background-audio] dropping post-timeout transcript: source=%s chunks=%d chars=%d words=%d%s (scheduler abandoned)", source, len(batch), charCount, wordCount, backgroundTranscriptDebugLogSuffix(trimmed))
		return
	}
	activityPipelineMetrics.whisperAcceptedTranscripts.Add(1)
	charCount, wordCount := backgroundTranscriptLogFields(trimmed)
	log.Printf("[background-audio] accepted transcript: source=%s chunks=%d duration=%s bytes=%d chars=%d words=%d%s", source, len(batch), time.Since(start).Round(time.Millisecond), len(wavData), charCount, wordCount, backgroundTranscriptDebugLogSuffix(trimmed))
	if s.onSpeech != nil {
		// Attribute to the app frontmost when the earliest chunk in this
		// batch was captured. The batch covers a contiguous ~10s recording
		// window so taking the first chunk's snapshot keeps attribution
		// closest to when the audio actually began.
		ctx := speechCaptureContext{
			CapturedAt:            batch[0].capturedAt,
			App:                   batch[0].capturedApp,
			MergedWAV:             wavData,
			MeetingRecordingEpoch: batch[0].recordingEpoch,
		}
		// Conservative: if ANY chunk in the batch was tagged as likely
		// echo or captured while another process was using the mic,
		// propagate that flag to the whole transcribed segment. Whisper
		// merges adjacent chunks into one text so we cannot split the
		// flagged and non-flagged portions after the fact.
		for i := range batch {
			if batch[i].likelyEcho {
				ctx.LikelyEcho = true
			}
			if batch[i].otherMicUser {
				ctx.OtherMicUser = true
			}
			if !ctx.MicHolders.Active && batch[i].micHolders.Active {
				ctx.MicHolders = batch[i].micHolders
			}
			if ctx.BrowserMeeting == "" {
				ctx.BrowserMeeting = batch[i].browserMeeting
			}
			if !ctx.InMeeting && batch[i].inMeeting {
				ctx.InMeeting = true
				ctx.MeetingAppName = batch[i].meetingApp
				ctx.MeetingBundleID = batch[i].meetingBundle
			}
			if !ctx.MeetingCandidate && batch[i].candidate {
				ctx.MeetingCandidate = true
				ctx.CandidateMeetingAppName = batch[i].candidateApp
				ctx.CandidateMeetingReason = batch[i].candidateReason
			}
		}
		s.onSpeech(trimmed, source, ctx)
	}
}

func (s *backgroundTranscriptionScheduler) trimQueueLocked(source backgroundListeningSource) int {
	if s.cfg.maxQueueDurationPerSource <= 0 {
		return 0
	}
	dropped := 0
	for s.durations[source] > s.cfg.maxQueueDurationPerSource && len(s.queues[source]) > 1 {
		oldest := s.queues[source][0]
		s.queues[source] = s.queues[source][1:]
		s.durations[source] -= oldest.duration
		dropped++
	}
	if s.durations[source] < 0 || len(s.queues[source]) == 0 {
		s.durations[source] = 0
	}
	return dropped
}

func (s *backgroundTranscriptionScheduler) signalLocked() {
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

func normalizeQueuedBackgroundSource(source backgroundListeningSource) backgroundListeningSource {
	switch source {
	case backgroundListeningSourceSystem:
		return backgroundListeningSourceSystem
	default:
		return backgroundListeningSourceMicrophone
	}
}

func mergeScheduledWAVChunks(chunks []scheduledAudioChunk) ([]byte, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no chunks")
	}
	if len(chunks) == 1 {
		return chunks[0].wavData, nil
	}

	totalPCM := 0
	for _, chunk := range chunks {
		if len(chunk.wavData) <= wavHeaderSize {
			return nil, fmt.Errorf("invalid WAV chunk: source=%s bytes=%d", chunk.source, len(chunk.wavData))
		}
		if !bytes.Equal(chunk.wavData[0:4], []byte("RIFF")) || !bytes.Equal(chunk.wavData[8:12], []byte("WAVE")) {
			return nil, fmt.Errorf("invalid WAV header: source=%s", chunk.source)
		}
		totalPCM += len(chunk.wavData) - wavHeaderSize
	}

	var out bytes.Buffer
	out.Grow(wavHeaderSize + totalPCM)
	if err := writeWAVHeader(&out, uint32(totalPCM)); err != nil {
		return nil, err
	}
	for _, chunk := range chunks {
		out.Write(chunk.wavData[wavHeaderSize:])
	}
	return out.Bytes(), nil
}

func totalScheduledAudioBytes(chunks []scheduledAudioChunk) int {
	total := 0
	for _, chunk := range chunks {
		total += len(chunk.wavData)
	}
	return total
}
