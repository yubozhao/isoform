package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"sync"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/speakerid"
	"wails-gui/storage"
)

// voiceEnrollmentState is the App-scoped lazy-loaded singleton holding
// the sherpa speaker-embedding extractor + the in-memory cache of
// enrolled Self embeddings (for verification on every mic chunk in the
// background-audio pipeline). The struct is created on first use and
// torn down when the App shuts down.
type voiceEnrollmentState struct {
	mu        sync.Mutex
	extractor *speakerid.Extractor
	// selfEmbeddings is the cached list of enrolled embeddings whose
	// principal is the Self. The verification path takes the BEST
	// similarity across this slice — averaging would smear together
	// clips taken in different environments (different mic, sick voice
	// day, etc.) and reduce robustness.
	selfEmbeddings [][]float32
	// modelPath is the path to the sherpa speaker model on disk. May
	// be empty when the model hasn't been downloaded yet.
	modelPath string
}

// ensureVoiceEnrollment lazy-loads the extractor + Self-embedding cache.
// Returns ErrVoiceModelMissing if the speaker model file isn't on disk
// (the user hasn't completed the asset download yet). Other errors are
// wrapped with context.
var errVoiceModelMissing = errors.New("voice: speaker embedding model not downloaded yet")

func (a *App) ensureVoiceEnrollment() (*voiceEnrollmentState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.voiceID != nil && a.voiceID.extractor != nil {
		return a.voiceID, nil
	}
	paths := defaultSherpaPaths()
	if paths.speakerEmbd == "" {
		return nil, fmt.Errorf("voice: speaker model path not configured on this platform")
	}
	if _, err := os.Stat(paths.speakerEmbd); err != nil {
		return nil, errVoiceModelMissing
	}
	ext, err := speakerid.New(speakerid.Config{Model: paths.speakerEmbd, NumThreads: 1})
	if err != nil {
		return nil, fmt.Errorf("voice: load extractor: %w", err)
	}
	state := &voiceEnrollmentState{extractor: ext, modelPath: paths.speakerEmbd}
	a.voiceID = state
	// Best-effort: refresh the Self-embedding cache so verification
	// works immediately. Failure here is logged but not fatal — the
	// extractor still works, and the cache refills on next enrollment.
	if err := a.refreshSelfEmbeddingCacheLocked(); err != nil {
		_ = err // logged inside
	}
	return state, nil
}

// refreshSelfEmbeddingCacheLocked rebuilds voiceID.selfEmbeddings from
// storage. Caller must hold a.mu.
func (a *App) refreshSelfEmbeddingCacheLocked() error {
	if a.voiceID == nil || a.store == nil {
		return nil
	}
	rows, err := a.store.ListVoiceEmbeddingsForKind(context.Background(), storage.PrincipalKindSelf)
	if err != nil {
		return fmt.Errorf("load self embeddings: %w", err)
	}
	cache := make([][]float32, 0, len(rows))
	for _, r := range rows {
		if len(r.Embedding) > 0 {
			cache = append(cache, r.Embedding)
		}
	}
	a.voiceID.mu.Lock()
	a.voiceID.selfEmbeddings = cache
	a.voiceID.mu.Unlock()
	return nil
}

// VoiceSampleDTO is the JSON shape the Settings UI receives when
// listing enrolled voice samples.
type VoiceSampleDTO struct {
	ID           int64   `json:"id"`
	PrincipalID  string  `json:"principalId"`
	Dim          int     `json:"dim"`
	SampleRate   int     `json:"sampleRate"`
	AudioSeconds float64 `json:"audioSeconds"`
	Label        string  `json:"label"`
	CapturedAt   int64   `json:"capturedAt"`
}

// SessionSpeakerClusterDTO is one detected voice in a session — either
// the enrolled Self (when speakerVerified is true on any utterance) or
// an unknown speaker labeled in first-seen order ("Speaker 1", etc.).
// Used by the Activity-detail page diarization chip row.
type SessionSpeakerClusterDTO struct {
	Label           string  `json:"label"` // "You" or "Speaker N"
	IsSelf          bool    `json:"isSelf"`
	UtteranceCount  int     `json:"utteranceCount"`
	FirstSeenAt     int64   `json:"firstSeenAt"`
	LastSeenAt      int64   `json:"lastSeenAt"`
	EventTimestamps []int64 `json:"eventTimestamps"` // event.Timestamp values belonging to this cluster
}

// speakerClusterMergeThreshold is the cosine-similarity floor above
// which a new mic chunk joins an existing cluster instead of creating
// a new one. Picked slightly above the Self-verification threshold:
// false splits ("you got grouped as two different speakers") are
// worse UX than false merges ("two similar voices conflated"). Tune
// later from real session data.
const speakerClusterMergeThreshold = 0.65

// ListSessionSpeakerClusters returns one entry per distinct voice the
// session contains, computed by chronological greedy clustering of
// every mic-source speech event's cached SpeechSpeakerEmbedding.
// Self utterances (SpeechSpeakerVerified=true) are folded into a
// single "You" cluster; everything else gets a "Speaker N" label in
// first-appearance order so the labels stay stable across reloads.
//
// Returns an empty slice (not error) when the session has no
// mic-source speech events or no events carry embeddings — typical of
// sessions captured before R20 or sessions that are pure system_audio.
func (a *App) ListSessionSpeakerClusters(sessionID string) ([]SessionSpeakerClusterDTO, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, fmt.Errorf("activity monitor not initialized")
	}
	events, err := a.activity.store.GetSessionEventsIncludingProtected(sessionID)
	if err != nil {
		return nil, err
	}
	type cluster struct {
		isSelf          bool
		centroid        []float32
		count           int
		firstSeen       int64
		lastSeen        int64
		eventTimestamps []int64
	}
	var (
		selfCluster *cluster
		others      []*cluster
	)
	for _, evt := range events {
		if evt.Kind != activitymon.EventSpeech {
			continue
		}
		if evt.SpeechSource != "" && evt.SpeechSource != "microphone" {
			continue
		}
		if len(evt.SpeechSpeakerEmbedding) == 0 {
			continue
		}
		// Self folding: any verified utterance contributes to the
		// single "You" cluster. We trust the verification verdict
		// over our own clustering for this case.
		isSelf := evt.SpeechSpeakerVerified != nil && *evt.SpeechSpeakerVerified
		if isSelf {
			if selfCluster == nil {
				selfCluster = &cluster{
					isSelf:    true,
					centroid:  append([]float32(nil), evt.SpeechSpeakerEmbedding...),
					firstSeen: evt.Timestamp,
				}
			}
			selfCluster.count++
			selfCluster.lastSeen = evt.Timestamp
			selfCluster.eventTimestamps = append(selfCluster.eventTimestamps, evt.Timestamp)
			// Running mean. Cheap, good enough for chunk counts in the dozens.
			n := float32(selfCluster.count)
			for i := range selfCluster.centroid {
				selfCluster.centroid[i] = (selfCluster.centroid[i]*(n-1) + evt.SpeechSpeakerEmbedding[i]) / n
			}
			continue
		}
		// Non-self: greedy assign to best matching existing cluster
		// above the merge threshold; otherwise start a new one.
		bestIdx := -1
		bestSim := -2.0
		for i, c := range others {
			s := speakerid.CosineSimilarity(evt.SpeechSpeakerEmbedding, c.centroid)
			if s > bestSim {
				bestSim = s
				bestIdx = i
			}
		}
		var target *cluster
		if bestIdx >= 0 && bestSim >= speakerClusterMergeThreshold {
			target = others[bestIdx]
		} else {
			target = &cluster{
				centroid:  append([]float32(nil), evt.SpeechSpeakerEmbedding...),
				firstSeen: evt.Timestamp,
			}
			others = append(others, target)
		}
		target.count++
		target.lastSeen = evt.Timestamp
		target.eventTimestamps = append(target.eventTimestamps, evt.Timestamp)
		n := float32(target.count)
		for i := range target.centroid {
			target.centroid[i] = (target.centroid[i]*(n-1) + evt.SpeechSpeakerEmbedding[i]) / n
		}
	}
	out := make([]SessionSpeakerClusterDTO, 0, len(others)+1)
	if selfCluster != nil {
		out = append(out, SessionSpeakerClusterDTO{
			Label:           "You",
			IsSelf:          true,
			UtteranceCount:  selfCluster.count,
			FirstSeenAt:     selfCluster.firstSeen,
			LastSeenAt:      selfCluster.lastSeen,
			EventTimestamps: selfCluster.eventTimestamps,
		})
	}
	for i, c := range others {
		out = append(out, SessionSpeakerClusterDTO{
			Label:           fmt.Sprintf("Speaker %d", i+1),
			IsSelf:          false,
			UtteranceCount:  c.count,
			FirstSeenAt:     c.firstSeen,
			LastSeenAt:      c.lastSeen,
			EventTimestamps: c.eventTimestamps,
		})
	}
	return out, nil
}

// VoiceMatchResultDTO is returned by TestVoiceMatch so the Settings UI
// can show the user whether their fresh sample is recognized as the
// enrolled principal. Carries the raw cosine score, the configured
// threshold, the verdict, and per-enrolled-sample breakdown so the
// user understands which enrollment was the best match.
type VoiceMatchResultDTO struct {
	BestScore          float64                 `json:"bestScore"`
	BestSampleID       int64                   `json:"bestSampleId"`
	Threshold          float64                 `json:"threshold"`
	Verdict            string                  `json:"verdict"` // "match" | "no_match" | "no_enrollment"
	AudioSeconds       float64                 `json:"audioSeconds"`
	EnrolledSampleHits []VoiceMatchSampleScore `json:"enrolledSampleHits,omitempty"`
}

// VoiceMatchSampleScore is one comparison row in the test result —
// shows the user which of their enrolled clips matched and how well.
type VoiceMatchSampleScore struct {
	SampleID   int64   `json:"sampleId"`
	Label      string  `json:"label"`
	CapturedAt int64   `json:"capturedAt"`
	Score      float64 `json:"score"`
}

// TestVoiceMatch records a fresh microphone sample and reports how well
// it matches the principal's enrolled voice samples. Lets the user
// validate their enrollment without waiting for a real session and
// digging through SQL.
//
// durationSeconds is clamped to [3, 15]; we don't need a full 10s for
// a sanity check (3s gives a usable embedding, 5s is the sweet spot).
func (a *App) TestVoiceMatch(principalID string, durationSeconds float64) (VoiceMatchResultDTO, error) {
	if durationSeconds < 3 {
		durationSeconds = 3
	}
	if durationSeconds > 15 {
		durationSeconds = 15
	}
	store, err := a.requireStore()
	if err != nil {
		return VoiceMatchResultDTO{}, err
	}
	if _, err := store.GetPrincipal(context.Background(), principalID); err != nil {
		return VoiceMatchResultDTO{}, fmt.Errorf("principal %q: %w", principalID, err)
	}
	enrolled, err := store.ListVoiceEmbeddingsByPrincipal(context.Background(), principalID)
	if err != nil {
		return VoiceMatchResultDTO{}, err
	}
	if len(enrolled) == 0 {
		return VoiceMatchResultDTO{
			Threshold: speakerVerificationThreshold,
			Verdict:   "no_enrollment",
		}, nil
	}
	state, err := a.ensureVoiceEnrollment()
	if err != nil {
		return VoiceMatchResultDTO{}, err
	}

	a.mu.Lock()
	bg := a.backgroundAudio
	a.mu.Unlock()
	var (
		samples       []float32
		sampleRate    int
		secondsActual float64
	)
	// Background listening without its microphone source (switched off)
	// has no mic chunks to share, so the sample is recorded on its own.
	if bg != nil && bg.isRunning() && bg.capturesMicrophone() {
		samples, sampleRate, secondsActual, err = tapMicChunksFromBackground(bg, durationSeconds)
	} else {
		samples, sampleRate, secondsActual, err = recordMicPCM(durationSeconds)
	}
	if err != nil {
		return VoiceMatchResultDTO{}, fmt.Errorf("record mic: %w", err)
	}

	probe, err := state.extractor.ExtractFromPCM(samples, sampleRate)
	if err != nil {
		return VoiceMatchResultDTO{}, fmt.Errorf("extract embedding: %w", err)
	}

	result := VoiceMatchResultDTO{
		BestScore:    -2.0,
		Threshold:    speakerVerificationThreshold,
		AudioSeconds: secondsActual,
	}
	result.EnrolledSampleHits = make([]VoiceMatchSampleScore, 0, len(enrolled))
	for _, ref := range enrolled {
		score := speakerid.CosineSimilarity(probe, ref.Embedding)
		result.EnrolledSampleHits = append(result.EnrolledSampleHits, VoiceMatchSampleScore{
			SampleID:   ref.ID,
			Label:      ref.Label,
			CapturedAt: ref.CapturedAt,
			Score:      score,
		})
		if score > result.BestScore {
			result.BestScore = score
			result.BestSampleID = ref.ID
		}
	}
	if result.BestScore >= speakerVerificationThreshold {
		result.Verdict = "match"
	} else {
		result.Verdict = "no_match"
	}
	return result, nil
}

// VoiceEnrollmentStatusDTO summarises the enrollment state for a single
// principal — the Settings UI uses it to render the "X samples enrolled"
// badge + the "ready / needs enrollment" status without paying for a
// full list query.
type VoiceEnrollmentStatusDTO struct {
	PrincipalID      string `json:"principalId"`
	SampleCount      int    `json:"sampleCount"`
	LastCapturedAt   int64  `json:"lastCapturedAt"`
	ModelReady       bool   `json:"modelReady"`       // false when the speaker model isn't downloaded yet
	ModelMissingHint string `json:"modelMissingHint"` // user-facing reason when !ModelReady
}

// GetVoiceEnrollmentStatus returns the enrollment summary for the given
// principal. Returns a populated DTO even on errors (with sampleCount=0
// and ModelReady=false) so the UI can render unconditionally.
func (a *App) GetVoiceEnrollmentStatus(principalID string) (perfValue VoiceEnrollmentStatusDTO, perfErr error) {
	defer logPageBinding("GetVoiceEnrollmentStatus", time.Now(), &perfValue)
	out := VoiceEnrollmentStatusDTO{PrincipalID: principalID}
	store, err := a.requireStore()
	if err != nil {
		return out, err
	}
	rows, err := store.ListVoiceEmbeddingsByPrincipal(context.Background(), principalID)
	if err != nil {
		return out, err
	}
	out.SampleCount = len(rows)
	if len(rows) > 0 {
		out.LastCapturedAt = rows[0].CapturedAt
	}
	paths := defaultSherpaPaths()
	if paths.speakerEmbd != "" {
		if _, err := os.Stat(paths.speakerEmbd); err == nil {
			out.ModelReady = true
		} else {
			out.ModelMissingHint = "Speaker model not downloaded yet — start a Isoform session first so the model auto-downloads."
		}
	} else {
		out.ModelMissingHint = "Speaker model is not configured on this platform."
	}
	return out, nil
}

// ListVoiceSamples returns every enrolled sample for the principal,
// newest first. Embedding bytes are omitted from the DTO — UI only
// needs metadata.
func (a *App) ListVoiceSamples(principalID string) (perfValue []VoiceSampleDTO, perfErr error) {
	defer logPageBinding("ListVoiceSamples", time.Now(), &perfValue)
	store, err := a.requireStore()
	if err != nil {
		return nil, err
	}
	rows, err := store.ListVoiceEmbeddingsByPrincipal(context.Background(), principalID)
	if err != nil {
		return nil, err
	}
	out := make([]VoiceSampleDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, VoiceSampleDTO{
			ID:           r.ID,
			PrincipalID:  r.PrincipalID,
			Dim:          r.Dim,
			SampleRate:   r.SampleRate,
			AudioSeconds: r.AudioSeconds,
			Label:        r.Label,
			CapturedAt:   r.CapturedAt,
		})
	}
	return out, nil
}

// DeleteVoiceSample removes one enrolled sample by ID and refreshes the
// in-memory Self-embedding cache so verification stops using it.
func (a *App) DeleteVoiceSample(id int64) error {
	store, err := a.requireStore()
	if err != nil {
		return err
	}
	if err := store.DeleteVoiceEmbedding(context.Background(), id); err != nil {
		return err
	}
	a.mu.Lock()
	_ = a.refreshSelfEmbeddingCacheLocked()
	a.mu.Unlock()
	return nil
}

// EnrollVoiceSample records `durationSeconds` of microphone audio,
// extracts a speaker embedding, and saves it as one enrollment sample
// for `principalID`. Returns the new sample's metadata. Refreshes the
// Self-embedding cache after persistence.
//
// Caps duration to [3, 30] seconds — under 3 yields noisy embeddings;
// over 30 wastes UI time without improving robustness.
func (a *App) EnrollVoiceSample(principalID string, durationSeconds float64) (VoiceSampleDTO, error) {
	a.voiceMemoryMu.Lock()
	voiceGeneration := a.voiceMemoryGeneration
	a.voiceMemoryMu.Unlock()
	if durationSeconds < 3 {
		durationSeconds = 3
	}
	if durationSeconds > 30 {
		durationSeconds = 30
	}
	store, err := a.requireStore()
	if err != nil {
		return VoiceSampleDTO{}, err
	}
	if _, err := store.GetPrincipal(context.Background(), principalID); err != nil {
		return VoiceSampleDTO{}, fmt.Errorf("principal %q: %w", principalID, err)
	}
	state, err := a.ensureVoiceEnrollment()
	if err != nil {
		return VoiceSampleDTO{}, err
	}

	// CoreAudio doesn't allow two AudioUnit instances bound to the same
	// input device. When background-audio capture is already running we
	// MUST share its existing recorder rather than opening a second one
	// — tapMicChunksFromBackground subscribes to the existing chunk
	// stream for the duration. When background is off we spin up our
	// own one-shot recorder.
	a.mu.Lock()
	bg := a.backgroundAudio
	a.mu.Unlock()

	var (
		samples       []float32
		sampleRate    int
		secondsActual float64
	)
	if bg != nil && bg.isRunning() && bg.capturesMicrophone() {
		samples, sampleRate, secondsActual, err = tapMicChunksFromBackground(bg, durationSeconds)
	} else {
		samples, sampleRate, secondsActual, err = recordMicPCM(durationSeconds)
	}
	if err != nil {
		return VoiceSampleDTO{}, fmt.Errorf("record mic: %w", err)
	}
	emb, err := state.extractor.ExtractFromPCM(samples, sampleRate)
	if err != nil {
		return VoiceSampleDTO{}, fmt.Errorf("extract embedding: %w", err)
	}
	a.voiceMemoryMu.Lock()
	defer a.voiceMemoryMu.Unlock()
	if a.voiceMemoryGeneration != voiceGeneration {
		return VoiceSampleDTO{}, errors.New("voice enrollment was forgotten")
	}
	saved, err := store.CreateVoiceEmbedding(context.Background(), storage.VoiceEmbeddingRow{
		PrincipalID:  principalID,
		Embedding:    emb,
		Dim:          len(emb),
		SampleRate:   sampleRate,
		AudioSeconds: secondsActual,
		Label:        fmt.Sprintf("enrolled %s", time.Now().Format("2006-01-02 15:04")),
	})
	if err != nil {
		return VoiceSampleDTO{}, err
	}
	a.mu.Lock()
	_ = a.refreshSelfEmbeddingCacheLocked()
	a.mu.Unlock()

	if a.telemetry != nil {
		a.telemetry.Capture("voice.enrollment.completed", map[string]any{
			"sample_rate":   saved.SampleRate,
			"audio_seconds": saved.AudioSeconds,
			"embedding_dim": saved.Dim,
		})
	}

	return VoiceSampleDTO{
		ID:           saved.ID,
		PrincipalID:  saved.PrincipalID,
		Dim:          saved.Dim,
		SampleRate:   saved.SampleRate,
		AudioSeconds: saved.AudioSeconds,
		Label:        saved.Label,
		CapturedAt:   saved.CapturedAt,
	}, nil
}

// speakerVerificationThreshold is the cosine-similarity cutoff above
// which a mic chunk is considered "matches enrolled Self". Picked
// conservatively (lower than the 0.7-0.75 the literature suggests) so
// the user isn't constantly flagged as "non-self" on noisy clips with
// the trade-off of letting through some genuine non-self speech with
// similar voice characteristics. Calibrate from real enrollment data
// over time.
const speakerVerificationThreshold = 0.62

// verifySpeakerOnWAV extracts an embedding from the merged WAV bytes
// of a transcribed chunk and compares it to every enrolled Self
// embedding. Returns:
//
//	*true  → max similarity ≥ threshold (the user is speaking)
//	*false → max similarity < threshold (someone else used the mic)
//	nil    → couldn't verify (no enrollment, no model, extract error,
//	         malformed WAV) — callers must treat nil as "didn't run"
//	         and NOT as a non-self flag.
//
// Safe to call from the audio scheduler hot path: ~30-50ms CPU per
// invocation, mutex-guarded inside the extractor.
// verifySpeakerOnWAV returns (verifiedSelf, embedding). verifiedSelf is
// the cosine-vs-Self verdict (see top-level doc on the *bool semantics).
// embedding is the extracted speaker fingerprint — non-nil whenever
// extraction succeeded, regardless of whether a Self enrollment exists
// to compare against. The diarization view uses the embedding even when
// no Self is enrolled (it can still cluster "Speaker 1 / Speaker 2");
// the verification verdict is independent.
func (a *App) verifySpeakerOnWAV(wav []byte) (*bool, []float32) {
	a.mu.Lock()
	st := a.voiceID
	a.mu.Unlock()
	if st == nil {
		s, err := a.ensureVoiceEnrollment()
		if err != nil {
			log.Printf("[voice-verify] skip: extractor unavailable (err=%v) — no verification on this chunk", err)
			return nil, nil
		}
		st = s
	}
	body, err := stripWavHeader(wav)
	if err != nil || len(body) < 4 {
		log.Printf("[voice-verify] skip: bad WAV header (bytes=%d err=%v)", len(wav), err)
		return nil, nil
	}
	return a.verifySpeakerOnSamplesWith(st, pcm16ToFloat32(body))
}

// verifySpeakerOnSamples is verifySpeakerOnWAV for 16 kHz mono samples that
// are already decoded (a stretch of a recording from the watch).
func (a *App) verifySpeakerOnSamples(samples []float32) (*bool, []float32) {
	a.mu.Lock()
	st := a.voiceID
	a.mu.Unlock()
	if st == nil {
		s, err := a.ensureVoiceEnrollment()
		if err != nil {
			return nil, nil
		}
		st = s
	}
	return a.verifySpeakerOnSamplesWith(st, samples)
}

func (a *App) verifySpeakerOnSamplesWith(st *voiceEnrollmentState, samples []float32) (*bool, []float32) {
	if len(samples) < 16000 { // < 1 second; embedding will be junk
		log.Printf("[voice-verify] skip: audio too short for reliable embedding (samples=%d, need ≥16000 = 1s)", len(samples))
		return nil, nil
	}
	emb, err := st.extractor.ExtractFromPCM(samples, 16000)
	if err != nil || len(emb) == 0 {
		log.Printf("[voice-verify] skip: extractor failed (dim=%d err=%v)", len(emb), err)
		return nil, nil
	}
	// Embedding always returned now. Verification only happens when we
	// have at least one Self enrollment to compare against.
	st.mu.Lock()
	enrolled := append([][]float32(nil), st.selfEmbeddings...)
	st.mu.Unlock()
	if len(enrolled) == 0 {
		// No verdict, but embedding is still useful for diarization.
		return nil, emb
	}
	best := -2.0
	for _, ref := range enrolled {
		s := speakerid.CosineSimilarity(emb, ref)
		if s > best {
			best = s
		}
	}
	ok := best >= speakerVerificationThreshold
	verdict := "non-self"
	if ok {
		verdict = "SELF"
	}
	log.Printf("[voice-verify] %s: score=%.3f threshold=%.2f enrolled=%d audio_seconds=%.1f",
		verdict, best, speakerVerificationThreshold, len(enrolled), float64(len(samples))/16000.0)
	return &ok, emb
}

// tapMicChunksFromBackground subscribes to the live background-audio
// capture's chunk stream for ~durationSeconds, concatenates the PCM
// bodies from microphone-source chunks, and returns the merged
// samples. Used when background listening is on so we don't try to
// open a second AudioUnit on the same input device — see
// voice_enrollment.go's CoreAudio crash history (commits a67d97f11,
// 581dc0cc8) for the why.
//
// Capture cadence on macOS is ~5s per chunk, so a 10s enrollment
// typically collects 2-3 chunks. We grant up to durationSeconds + 6
// total wait so a chunk that started just before subscription has a
// chance to land.
func tapMicChunksFromBackground(bg *backgroundAudioCapture, durationSeconds float64) ([]float32, int, float64, error) {
	if bg == nil {
		return nil, 0, 0, fmt.Errorf("nil background capture")
	}
	ch, unsubscribe := bg.subscribeChunks(backgroundListeningSourceMicrophone, 8)
	defer unsubscribe()

	const sampleRate = 16000
	deadline := time.Now().Add(time.Duration(durationSeconds+6) * time.Second)
	targetSamples := int(float64(sampleRate) * durationSeconds)
	var samples []float32
collectLoop:
	for len(samples) < targetSamples {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		timer := time.NewTimer(remaining)
		select {
		case chunk, ok := <-ch:
			timer.Stop()
			if !ok {
				return nil, 0, 0, fmt.Errorf("subscription channel closed")
			}
			body, err := stripWavHeader(chunk.WavData)
			if err != nil {
				continue
			}
			samples = append(samples, pcm16ToFloat32(body)...)
		case <-timer.C:
			// Deadline hit before another chunk arrived — labeled
			// break exits the FOR loop (a plain `break` would only
			// exit the select and we'd spin until next chunk).
			break collectLoop
		}
	}
	if len(samples) == 0 {
		return nil, 0, 0, fmt.Errorf("no mic audio received in %.0fs (is background listening capturing mic?)", durationSeconds+6)
	}
	// Trim to exactly the requested duration if we overshot — keeps
	// embedding extraction time consistent across enrollments.
	if len(samples) > targetSamples {
		samples = samples[:targetSamples]
	}
	return samples, sampleRate, float64(len(samples)) / float64(sampleRate), nil
}

// recordMicPCM spins up a one-shot microphone recorder, captures
// approximately `durationSeconds` of audio, and returns the
// concatenated float32 PCM samples + sample rate + actual duration.
// Reads in 1-second chunks so a future progress callback hook only
// has to subscribe to per-chunk events.
//
// Uses a fresh `newPlatformAudioRecorder(microphone)` instance —
// independent of the background-audio capture loop, so enrollment
// works even when the user has background listening turned off.
func recordMicPCM(durationSeconds float64) ([]float32, int, float64, error) {
	recorder := newPlatformAudioRecorder(backgroundListeningSourceMicrophone)
	if recorder == nil {
		return nil, 0, 0, fmt.Errorf("microphone capture not supported on this platform")
	}
	if err := recorder.Start(); err != nil {
		return nil, 0, 0, fmt.Errorf("start recorder: %w", err)
	}
	defer recorder.Stop()

	const sampleRate = 16000
	chunkDuration := 1 * time.Second
	totalChunks := int(math.Ceil(durationSeconds))
	var samples []float32
	for i := 0; i < totalChunks; i++ {
		chunk, err := recorder.ReadChunk(chunkDuration)
		if err != nil {
			if len(samples) > 0 {
				break // partial capture is better than nothing if the user yanks the mic
			}
			return nil, 0, 0, fmt.Errorf("read chunk %d: %w", i, err)
		}
		body, err := stripWavHeader(chunk.WavData)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("chunk %d: %w", i, err)
		}
		samples = append(samples, pcm16ToFloat32(body)...)
	}
	if len(samples) == 0 {
		return nil, 0, 0, fmt.Errorf("no audio captured")
	}
	return samples, sampleRate, float64(len(samples)) / float64(sampleRate), nil
}

// stripWavHeader assumes a standard 44-byte RIFF/WAVE header — the
// shape newPlatformAudioRecorder.ReadChunk emits. Returns the raw PCM
// body or an error if the header is missing.
func stripWavHeader(wav []byte) ([]byte, error) {
	if len(wav) < 44 {
		return nil, fmt.Errorf("wav too short: %d bytes", len(wav))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		// Fallback: if the recorder is returning raw PCM (some platforms
		// do), treat the whole buffer as body. Better than failing.
		return wav, nil
	}
	return wav[44:], nil
}

// pcm16ToFloat32 converts little-endian signed 16-bit PCM bytes into
// the float32 [-1, 1] representation sherpa-onnx expects.
func pcm16ToFloat32(pcm []byte) []float32 {
	n := len(pcm) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(pcm[i*2 : i*2+2]))
		out[i] = float32(s) / 32768.0
	}
	return out
}
