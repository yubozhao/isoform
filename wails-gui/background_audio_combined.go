package main

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// combinedAudioRecorder fans in chunks from multiple underlying recorders
// (typically mic + system_audio) and exposes them through the audioRecorder
// interface so captureLoop treats it identically to a single-source recorder.
//
// Originally lived as darwinCombinedAudioRecorder, but the fan-in logic is
// platform-independent — both macOS (CoreAudio mic + CoreAudio process tap)
// and Windows (WASAPI mic + WASAPI loopback) need the same behavior. The
// platform-specific bit is which recorders to start, supplied via newRecorders.
type combinedAudioRecorder struct {
	// newRecorders returns un-started candidate recorders in the order they
	// should be started. Start() walks the slice and retries transient failures
	// with bounded backoff. Permission-not-requested skips stay skipped; starts
	// outside a user request never prompt. Start order matters on macOS — system
	// audio first so the process tap is ready before the mic recorder starts
	// feeding chunks. On Windows the order is less load-bearing but kept
	// consistent for predictability.
	newRecorders func() []audioRecorder
	canStart     func(backgroundListeningSource) bool // existing product gates, checked around retries

	mu          sync.Mutex
	running     bool
	stopCh      chan struct{}
	chunkCh     chan capturedAudioChunk
	recorders   []audioRecorder
	readWG      sync.WaitGroup
	finalChunks []capturedAudioChunk
}

func primaryRecorderSource(recorder audioRecorder) backgroundListeningSource {
	source := backgroundListeningSourceMicrophone
	if sources := recorder.ActiveSources(); len(sources) > 0 {
		source = sources[0]
	}
	return source
}

func (r *combinedAudioRecorder) Start() error {
	candidates := r.newRecorders()
	started := make([]audioRecorder, 0, len(candidates))
	var retry []audioRecorder
	for _, recorder := range candidates {
		source := primaryRecorderSource(recorder)
		if r.canStart != nil && !r.canStart(source) {
			continue
		}
		err := recorder.Start()
		if errors.Is(err, errPermissionNotRequested) {
			// Not an error: the permission has not been asked for yet, and
			// this start is not allowed to ask. The recorder's own event
			// already says which permission is missing.
			log.Printf("[background-audio] %s recorder skipped: %v", source, err)
			continue
		}
		if err != nil {
			// Silent fallback to mic-only would mean the user sees no
			// system_audio events at all, and microphone-captured speaker
			// echo gets mis-attributed as the user's voice. Log loudly so
			// this surfaces in support reports and so the AudioCaptureEvent
			// stream can drive a UI indicator.
			log.Printf("[background-audio][CRITICAL] %s recorder failed to start; combined capture will retry it: %v", source, err)
			recordAudioCaptureEvent(AudioCaptureEvent{
				Source: string(source), Kind: "error", Outcome: "failed",
				Message: fmt.Sprintf("combined recorder could not start %s: %v", source, err),
			})
			retry = append(retry, recorder)
			continue
		}
		started = append(started, recorder)
	}

	if len(started) == 0 && len(retry) == 0 {
		return fmt.Errorf("failed to start any background audio source")
	}

	r.mu.Lock()
	r.running = true
	r.stopCh = make(chan struct{})
	r.chunkCh = make(chan capturedAudioChunk, len(candidates)*4)
	r.recorders = started
	r.finalChunks = nil
	r.mu.Unlock()

	for _, recorder := range started {
		r.readWG.Add(1)
		go r.readLoop(recorder)
	}

	for _, recorder := range retry {
		r.readWG.Add(1)
		go r.retrySource(recorder)
	}
	return nil
}

// Retry transient startup failures instead of silently losing one side for the
// rest of the process. Permission-not-requested skips never enter this loop.
func (r *combinedAudioRecorder) retrySource(recorder audioRecorder) {
	defer r.readWG.Done()
	source := primaryRecorderSource(recorder)
	for failures := uint64(1); ; failures++ {
		r.mu.Lock()
		running, stopCh := r.running, r.stopCh
		r.mu.Unlock()
		if !running {
			return
		}
		timer := time.NewTimer(systemAudioResumeRetryDelay(failures))
		select {
		case <-stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}
		r.mu.Lock()
		running = r.running
		r.mu.Unlock()
		if !running || (r.canStart != nil && !r.canStart(source)) {
			return
		}
		if err := recorder.Start(); err != nil {
			if errors.Is(err, errPermissionNotRequested) {
				return
			}
			continue
		}
		allowed := r.canStart == nil || r.canStart(source)
		r.mu.Lock()
		if !r.running || !allowed {
			r.mu.Unlock()
			recorder.Stop()
			return
		}
		r.recorders = append(r.recorders, recorder)
		r.mu.Unlock()
		// readLoop owns its WaitGroup count independently.
		r.readWG.Add(1)
		r.readLoop(recorder)
		return
	}
}

func (r *combinedAudioRecorder) keepFinalChunk(chunk capturedAudioChunk) {
	if len(chunk.WavData) <= wavHeaderSize {
		return
	}
	r.mu.Lock()
	r.finalChunks = append(r.finalChunks, chunk)
	r.mu.Unlock()
}

func (r *combinedAudioRecorder) drainStoppedChunks() []capturedAudioChunk {
	r.mu.Lock()
	defer r.mu.Unlock()
	chunks := r.finalChunks
	r.finalChunks = nil
	// Partial in-flight tails may have arrived before older buffered chunks
	// were drained. Restore capture order before the writer's replay fence.
	sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].CapturedAt.Before(chunks[j].CapturedAt) })
	return chunks
}

func (r *combinedAudioRecorder) readLoop(recorder audioRecorder) {
	defer r.readWG.Done()
	// Retain source identity even when an idle system tap temporarily reports
	// no active inputs. A failed resume still belongs to system audio.
	source := primaryRecorderSource(recorder)

	for {
		chunk, err := recorder.ReadChunk(backgroundAudioChunkDuration)
		if err != nil {
			r.mu.Lock()
			running := r.running
			stopCh := r.stopCh
			r.mu.Unlock()
			if !running {
				return
			}
			log.Printf("[background-audio] %s chunk read failed: %v", source, err)
			select {
			case <-stopCh:
				return
			case <-time.After(1 * time.Second):
			}
			continue
		}

		r.mu.Lock()
		running := r.running
		stopCh := r.stopCh
		chunkCh := r.chunkCh
		r.mu.Unlock()
		if !running {
			r.keepFinalChunk(chunk)
			return
		}

		select {
		case <-stopCh:
			r.keepFinalChunk(chunk)
			return
		case chunkCh <- chunk:
		}
	}
}

func (r *combinedAudioRecorder) ReadChunk(_ time.Duration) (capturedAudioChunk, error) {
	r.mu.Lock()
	running := r.running
	stopCh := r.stopCh
	chunkCh := r.chunkCh
	r.mu.Unlock()

	if !running || chunkCh == nil {
		return capturedAudioChunk{}, fmt.Errorf("combined audio recorder not initialized")
	}

	select {
	case <-stopCh:
		return capturedAudioChunk{}, nil
	case chunk, ok := <-chunkCh:
		if !ok {
			return capturedAudioChunk{}, nil
		}
		return chunk, nil
	}
}

func (r *combinedAudioRecorder) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	stopCh := r.stopCh
	recorders := append([]audioRecorder(nil), r.recorders...)
	chunkCh := r.chunkCh
	r.recorders = nil
	r.stopCh = nil
	r.chunkCh = nil
	r.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	for i := len(recorders) - 1; i >= 0; i-- {
		recorders[i].Stop()
	}
	r.readWG.Wait()
	if chunkCh != nil {
		close(chunkCh)
		for chunk := range chunkCh {
			r.keepFinalChunk(chunk)
		}
	}
}

func (r *combinedAudioRecorder) ActiveSources() []backgroundListeningSource {
	r.mu.Lock()
	defer r.mu.Unlock()

	sources := make([]backgroundListeningSource, 0, len(r.recorders))
	for _, recorder := range r.recorders {
		sources = append(sources, recorder.ActiveSources()...)
	}
	return sources
}
