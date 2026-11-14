//go:build !bindings && ((darwin && (amd64 || arm64) && !ios) || (windows && (amd64 || 386)))

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// sherpaTTS is the in-process MeloTTS (zh_en VITS) engine, loaded via cgo.
// Generate is not documented as thread-safe, so we serialize calls.
type sherpaTTS struct {
	mu  sync.Mutex
	tts *sherpa.OfflineTts
}

// newSherpaTTS loads the MeloTTS model. Caller must ensure model files exist
// (see ensureTtsAssets). Note: this binding does not map dict_dir, so Chinese
// word segmentation uses MeloTTS's built-in fallback — verified acceptable for
// reading assistant replies aloud.
func newSherpaTTS(paths ttsPaths) (speechSynthesizer, error) {
	for _, p := range []string{paths.model, paths.lexicon, paths.tokens} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("tts model file missing: %s", p)
		}
	}

	cfg := &sherpa.OfflineTtsConfig{}
	cfg.Model.Vits.Model = paths.model
	cfg.Model.Vits.Lexicon = paths.lexicon
	cfg.Model.Vits.Tokens = paths.tokens
	cfg.Model.Vits.NoiseScale = 0.667
	cfg.Model.Vits.NoiseScaleW = 0.8
	cfg.Model.Vits.LengthScale = 1.0
	cfg.Model.NumThreads = ttsNumThreads
	// Rule FSTs handle inverse text normalization (dates/numbers/phone). Only
	// pass the ones that actually extracted, comma-joined.
	var fsts []string
	for _, f := range []string{paths.phoneFst, paths.dateFst, paths.numberFst} {
		if fileExistsAndNonEmpty(f) {
			fsts = append(fsts, f)
		}
	}
	cfg.RuleFsts = strings.Join(fsts, ",")
	cfg.MaxNumSentences = 2

	engine := sherpa.NewOfflineTts(cfg)
	if engine == nil {
		return nil, errors.New("sherpa: NewOfflineTts returned nil")
	}
	return &sherpaTTS{tts: engine}, nil
}

// newKokoroTTS loads the Kokoro English model. Caller must ensure files exist
// (see ensureKokoroAssets). Kokoro uses espeak-ng-data for g2p (no dict_dir),
// giving noticeably better English than MeloTTS zh_en. Reuses sherpaTTS.
func newKokoroTTS(paths kokoroPaths) (speechSynthesizer, error) {
	for _, p := range []string{paths.model, paths.voices, paths.tokens} {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("kokoro model file missing: %s", p)
		}
	}

	cfg := &sherpa.OfflineTtsConfig{}
	cfg.Model.Kokoro.Model = paths.model
	cfg.Model.Kokoro.Voices = paths.voices
	cfg.Model.Kokoro.Tokens = paths.tokens
	cfg.Model.Kokoro.DataDir = paths.dataDir
	cfg.Model.Kokoro.LengthScale = 1.0
	cfg.Model.NumThreads = ttsNumThreads
	cfg.MaxNumSentences = 2

	engine := sherpa.NewOfflineTts(cfg)
	if engine == nil {
		return nil, errors.New("sherpa: NewOfflineTts(kokoro) returned nil")
	}
	return &sherpaTTS{tts: engine}, nil
}

// Synthesize generates speech for text and returns a 16-bit PCM mono WAV blob.
func (t *sherpaTTS) Synthesize(text string) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tts == nil {
		return nil, errors.New("tts engine is closed")
	}
	audio := t.tts.Generate(text, 0, 1.0)
	if audio == nil || len(audio.Samples) == 0 {
		return nil, errors.New("tts produced no audio")
	}
	// Normalize peak so every clip plays at a consistent loudness regardless of
	// length or engine. Short MeloTTS clips (e.g. "行胜于言") come out noticeably
	// quieter than long ones or than Kokoro otherwise. Edge fades then pin both
	// ends to zero so clip boundaries can't click.
	samples := applyEdgeFades(normalizePeak(audio.Samples, 0.95), audio.SampleRate)
	return encodeWAV(samples, audio.SampleRate), nil
}

func (t *sherpaTTS) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tts != nil {
		sherpa.DeleteOfflineTts(t.tts)
		t.tts = nil
	}
}
