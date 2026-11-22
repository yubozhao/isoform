package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/asr"
)

// File name retained for git history continuity. The whisper.cpp implementation
// has been replaced by sherpa-onnx (see asr_sherpa.go); this file now hosts the
// engine-agnostic voice-input lifecycle and Wails bindings.

const (
	whisperStartupInitAttempts = 3
)

type whisperPhase string

const (
	whisperPhaseIdle        whisperPhase = ""
	whisperPhaseDownloading whisperPhase = "downloading"
	whisperPhaseBuilding    whisperPhase = "building"
)

// VoiceInputStatus reports the current state of the voice input system.
type VoiceInputStatus struct {
	Available   bool   `json:"available"`
	Ready       bool   `json:"ready"`
	Downloading bool   `json:"downloading"`
	Building    bool   `json:"building"`
	Error       string `json:"error,omitempty"`
}

// whisperState tracks voice-input readiness plus the loaded ASR engine.
// Name retained for cross-file compatibility; the engine itself is sherpa-onnx.
type whisperState struct {
	initMu       sync.Mutex
	mu           sync.Mutex
	ready        bool
	readyCh      chan struct{} // closed when ready; use select instead of polling
	initializing bool
	phase        whisperPhase
	err          error
	server       asr.Transcriber
}

func newWhisperState() *whisperState {
	return &whisperState{
		readyCh: make(chan struct{}),
	}
}

func (a *App) getWhisperState() *whisperState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.whisper
}

func (a *App) ensureWhisperState() *whisperState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.whisper == nil {
		a.whisper = newWhisperState()
	}
	return a.whisper
}

func (a *App) whisperSnapshot() (*whisperState, bool, bool, chan struct{}) {
	whisper := a.getWhisperState()
	if whisper == nil {
		return nil, false, false, nil
	}

	whisper.mu.Lock()
	defer whisper.mu.Unlock()

	return whisper, whisper.ready, whisper.initializing, whisper.readyCh
}

// ---------- App Wails bindings ----------

// GetVoiceInputStatus returns the current voice input readiness.
func (a *App) GetVoiceInputStatus() VoiceInputStatus {
	whisper := a.getWhisperState()
	if whisper == nil {
		return VoiceInputStatus{}
	}
	whisper.mu.Lock()
	defer whisper.mu.Unlock()
	return VoiceInputStatus{
		Available:   whisper.ready || whisper.initializing,
		Ready:       whisper.ready,
		Downloading: whisper.phase == whisperPhaseDownloading,
		Building:    whisper.phase == whisperPhaseBuilding,
		Error:       errString(whisper.err),
	}
}

func (a *App) prewarmVoiceInput() {
	delay := 2 * time.Second

	for attempt := 1; attempt <= whisperStartupInitAttempts; attempt++ {
		if err := a.InitVoiceInput(); err != nil {
			log.Printf("[asr] background init attempt %d/%d failed: %v", attempt, whisperStartupInitAttempts, err)
			if attempt == whisperStartupInitAttempts {
				return
			}

			a.emitVoiceEvent("status", fmt.Sprintf(
				"Voice input setup failed (%d/%d). Retrying in %s...",
				attempt,
				whisperStartupInitAttempts,
				delay.Round(time.Second),
			))
			time.Sleep(delay)
			delay *= 2
			continue
		}
		return
	}
}

// InitVoiceInput verifies installed assets and loads the in-process recognizer. Called lazily on first voice
// input attempt; emits progress events to the frontend.
func (a *App) InitVoiceInput() error {
	return a.initVoiceInput(false, a.loadVoiceInput)
}

func (a *App) initVoiceInput(wait bool, load func() (asr.Transcriber, error)) error {
	whisper := a.ensureWhisperState()
	if wait {
		whisper.initMu.Lock()
	} else if !whisper.initMu.TryLock() {
		return fmt.Errorf("voice input is already initializing")
	}
	defer whisper.initMu.Unlock()

	whisper.mu.Lock()
	if whisper.ready {
		whisper.mu.Unlock()
		return nil
	}
	whisper.initializing = true
	whisper.phase = whisperPhaseIdle
	whisper.err = nil
	whisper.mu.Unlock()

	a.emitVoiceStatus()

	defer func() {
		whisper.mu.Lock()
		whisper.initializing = false
		if !whisper.ready {
			whisper.phase = whisperPhaseIdle
		}
		whisper.mu.Unlock()
		a.emitVoiceStatus()
	}()

	transcriber, err := load()
	if err != nil {
		whisper.mu.Lock()
		whisper.err = err
		whisper.mu.Unlock()
		return err
	}

	whisper.mu.Lock()
	whisper.server = transcriber
	whisper.ready = true
	whisper.phase = whisperPhaseIdle
	whisper.err = nil
	select {
	case <-whisper.readyCh:
	default:
		close(whisper.readyCh)
	}
	whisper.mu.Unlock()

	// Sherpa is now confirmed working; safe to drop the legacy whisper.cpp
	// install (~554 MB) left over from pre-sherpa builds. No-op on fresh
	// installs.
	go cleanupLegacyWhisperData()

	return nil
}

// initInstalledVoiceInput is for arrival only. Wait for both model jobs before
// entering voice initialization, without invoking dictation's asset setup.
func (a *App) initInstalledVoiceInput() error {
	if err := requireInstalledSherpaAssets(defaultSherpaPaths()); err != nil {
		return err
	}
	return a.initVoiceInput(true, a.openVoiceInput)
}

// loadVoiceInput runs dictation's normal asset setup before opening the engine.
func (a *App) loadVoiceInput() (asr.Transcriber, error) {
	if err := ensureSherpaAssets(context.Background(), defaultSherpaPaths(), a.voiceDownloadProgress); err != nil {
		return nil, fmt.Errorf("sherpa-onnx assets: %w", err)
	}
	a.setVoiceInputPhase(whisperPhaseIdle)
	return a.openVoiceInput()
}

func (a *App) setVoiceInputPhase(phase whisperPhase) {
	whisper := a.ensureWhisperState()
	whisper.mu.Lock()
	changed := whisper.phase != phase
	whisper.phase = phase
	whisper.mu.Unlock()
	if changed {
		a.emitVoiceStatus()
	}
}

func (a *App) voiceDownloadProgress(asset string, downloaded, total int64, bytesPerSec float64) {
	a.setVoiceInputPhase(whisperPhaseDownloading)
	a.emitVoiceEvent("download", fmt.Sprintf("Downloading %s...", asset))
	if a.ctx != nil {
		a.emitBrowserEvent("voice:download-progress", map[string]any{
			"fileName":    asset,
			"downloaded":  downloaded,
			"total":       total,
			"bytesPerSec": bytesPerSec,
		})
	}
}

func (a *App) openVoiceInput() (asr.Transcriber, error) {
	transcriber, err := initSherpaTranscriber(a.currentUILanguage())
	if err != nil {
		return nil, fmt.Errorf("sherpa-onnx: %w", err)
	}
	return transcriber, nil
}

func (a *App) ensureVoiceInputReady() error {
	whisper, ready, initializing, _ := a.whisperSnapshot()
	if whisper == nil {
		return a.InitVoiceInput()
	}

	deadline := time.Now().Add(2 * time.Minute)
	for {
		if ready {
			return nil
		}
		if !initializing {
			return a.InitVoiceInput()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("voice input initialization timed out")
		}
		time.Sleep(250 * time.Millisecond)
		_, ready, initializing, _ = a.whisperSnapshot()
	}
}

// ensureWhisperServerRunning is retained as a thin proxy for legacy callers.
// With sherpa-onnx the engine is in-process, so just confirming voice input is
// initialized is sufficient.
func (a *App) ensureWhisperServerRunning() error {
	return a.ensureVoiceInputReady()
}

// TranscribeAudio accepts base64-encoded WAV audio and returns the
// transcribed text. Automatically initializes the speech engine on first call.
func (a *App) TranscribeAudio(wavBase64 string) (string, error) {
	if a.IsClosedEyesModeEnabled() {
		return "", fmt.Errorf("cannot transcribe audio while observation is paused")
	}
	if err := a.ensureVoiceInputReady(); err != nil {
		return "", err
	}

	whisper := a.ensureWhisperState()
	whisper.mu.Lock()
	srv := whisper.server
	whisper.mu.Unlock()

	cleanedBase64, stats := normalizeAudioBase64Input(wavBase64)
	if stats.changed {
		log.Printf(
			"[voice-input] normalized audio base64 before decode: rawLen=%d cleanedLen=%d strippedPrefix=%t trimmedQuotes=%t nulBytes=%d whitespace=%d spaces=%d rawPrefix=% x cleanedPrefix=%q",
			len(wavBase64),
			len(cleanedBase64),
			stats.strippedPrefix,
			stats.trimmedQuotes,
			stats.nulBytes,
			stats.whitespaceBytes,
			stats.spaceBytes,
			prefixBytes(wavBase64, 8),
			prefixString(cleanedBase64, 16),
		)
	}

	wavData, err := base64.StdEncoding.DecodeString(cleanedBase64)
	if err != nil {
		log.Printf(
			"[voice-input] base64 decode failed: rawLen=%d cleanedLen=%d rawPrefix=% x cleanedPrefix=%q err=%v",
			len(wavBase64),
			len(cleanedBase64),
			prefixBytes(wavBase64, 8),
			prefixString(cleanedBase64, 16),
			err,
		)
		return "", fmt.Errorf("decode base64: %w", err)
	}

	if srv == nil || !srv.IsRunning() {
		return "", fmt.Errorf("speech engine not ready")
	}
	text, err := srv.Transcribe(wavData)
	if err != nil {
		return "", err
	}

	trimmed := strings.TrimSpace(text)
	if a.telemetry != nil {
		a.telemetry.Capture("voice.transcription.completed", map[string]any{
			"text_length": len(trimmed),
			"audio_bytes": len(wavData),
		})
	}

	return trimmed, nil
}

func (a *App) emitVoiceStatus() {
	if a.ctx != nil {
		a.emitBrowserEvent("voice:status", a.GetVoiceInputStatus())
	}
}

func (a *App) emitVoiceEvent(kind, message string) {
	if a.ctx != nil {
		a.emitBrowserEvent("voice:event", map[string]string{
			"kind":    kind,
			"message": message,
		})
	}
}

// ---------- Audio base64 normalization ----------

type audioBase64NormalizationStats struct {
	changed         bool
	strippedPrefix  bool
	trimmedQuotes   bool
	nulBytes        int
	whitespaceBytes int
	spaceBytes      int
}

func normalizeAudioBase64Input(raw string) (string, audioBase64NormalizationStats) {
	stats := audioBase64NormalizationStats{}
	normalized := strings.TrimSpace(raw)
	if normalized != raw {
		stats.changed = true
	}

	trimmedQuotes := strings.Trim(normalized, "\"")
	if trimmedQuotes != normalized {
		normalized = trimmedQuotes
		stats.changed = true
		stats.trimmedQuotes = true
	}

	if strings.HasPrefix(normalized, "data:") {
		if comma := strings.IndexByte(normalized, ','); comma >= 0 {
			normalized = normalized[comma+1:]
			stats.changed = true
			stats.strippedPrefix = true
		}
	}

	stats.nulBytes = strings.Count(normalized, "\x00")
	if stats.nulBytes > 0 {
		normalized = strings.ReplaceAll(normalized, "\x00", "")
		stats.changed = true
	}

	stats.whitespaceBytes = strings.Count(normalized, "\n") + strings.Count(normalized, "\r") + strings.Count(normalized, "\t")
	if stats.whitespaceBytes > 0 {
		normalized = strings.NewReplacer("\n", "", "\r", "", "\t", "").Replace(normalized)
		stats.changed = true
	}

	stats.spaceBytes = strings.Count(normalized, " ")
	if stats.spaceBytes > 0 {
		normalized = strings.ReplaceAll(normalized, " ", "+")
		stats.changed = true
	}

	return normalized, stats
}

// ---------- Helpers ----------

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func prefixBytes(value string, limit int) []byte {
	if limit <= 0 || len(value) == 0 {
		return nil
	}
	if len(value) <= limit {
		return []byte(value)
	}
	return []byte(value[:limit])
}

func prefixString(value string, limit int) string {
	if limit <= 0 || len(value) == 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// truncateForLog clips long strings for log output, appending an ellipsis
// marker when truncation occurs. Used by other modules besides voice input.
func truncateForLog(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "...(truncated)"
}
