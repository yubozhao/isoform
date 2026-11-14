package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	goruntime "runtime"
	"sync"

	"wails-gui/models"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// speechSynthesizer is the platform-agnostic contract the chat TTS feature
// depends on. The concrete implementation (sherpa MeloTTS) lives in the
// cgo-tagged tts_sherpa.go; unsupported platforms get a stub in
// tts_sherpa_other.go. Keeping the App coupled to this interface (not the
// concrete type) is what lets app.go compile on every platform.
type speechSynthesizer interface {
	// Synthesize converts text into a self-contained 16-bit PCM mono WAV blob.
	Synthesize(text string) ([]byte, error)
	Close()
}

// ttsState caches engines by manifest voice id, so changing the saved choice
// cannot reuse another voice. Engines load only from verified installed assets.
type ttsState struct {
	mu                sync.Mutex
	engines           map[string]speechSynthesizer
	loadMu            map[string]*sync.Mutex // per-voice load lock; serializes first load
	newEngine         func(string) (speechSynthesizer, error)
	emitEvent         func(context.Context, string, ...interface{}) // optional headless event sink
	autoPrewarmCancel context.CancelFunc
}

func (a *App) ensureTtsState() *ttsState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tts == nil {
		a.tts = &ttsState{
			engines: make(map[string]speechSynthesizer),
			loadMu:  make(map[string]*sync.Mutex),
		}
	}
	return a.tts
}

func (a *App) emitTtsEvent(name string, data ...interface{}) {
	st := a.ensureTtsState()
	st.mu.Lock()
	emit := st.emitEvent
	st.mu.Unlock()
	if emit == nil {
		emit = runtime.EventsEmit
	}
	emit(a.ctx, name, data...)
}

func (a *App) ttsDownloadProgress(lang string) sherpaDownloadProgress {
	return func(asset string, downloaded, total int64, bytesPerSec float64) {
		if a.ctx != nil {
			a.emitTtsEvent("tts:download-progress", map[string]any{
				"lang":        lang,
				"fileName":    asset,
				"downloaded":  downloaded,
				"total":       total,
				"bytesPerSec": bytesPerSec,
			})
		}
	}
}

// ttsModelPresentForLang reports whether the on-disk model for a language is
// already downloaded, without touching the network.
func (a *App) ttsModelPresentForLang(lang string) bool {
	manager, err := a.setupModelManager()
	if err != nil {
		return false
	}
	_, installed := manager.VoiceForLanguage(lang)
	return installed
}

func (a *App) emitTtsModelError(lang string, err error) {
	if a.ctx != nil {
		a.emitTtsEvent("tts:model-error", map[string]any{
			"lang":    lang,
			"message": err.Error(),
		})
	}
}

func (a *App) emitTtsModelCancelled(lang string) {
	if a.ctx != nil {
		a.emitTtsEvent("tts:model-cancelled", map[string]any{"lang": lang})
	}
}

// ttsContext returns the App's runtime context, falling back to a background
// context before startup so TTS work never operates on a nil context.
func (a *App) ttsContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func (st *ttsState) languageLoadMutex(lang string) *sync.Mutex {
	st.mu.Lock()
	defer st.mu.Unlock()
	lm := st.loadMu[lang]
	if lm == nil {
		lm = &sync.Mutex{}
		st.loadMu[lang] = lm
	}
	return lm
}

// ensureTtsVoiceAssets checks the manifest's installed assets. Callers hold
// the per-language load mutex; asset acquisition belongs to setup jobs.
func (a *App) ensureTtsVoiceAssets(ctx context.Context, lang string, progress sherpaDownloadProgress) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !a.ttsModelPresentForLang(lang) {
		return models.ErrNotReady
	}
	return nil
}

// startTtsVoiceDownload reports an already confirmed setup download. Briefs
// cannot authorize model work; an absent voice stays unavailable until setup
// confirms a manifest job.
func (a *App) startTtsVoiceDownload(ctx context.Context, lang string) bool {
	if ctx.Err() != nil {
		return false
	}
	manager, err := a.setupModelManager()
	if err != nil {
		return false
	}
	voice, installed := manager.VoiceForLanguage(lang)
	if installed || voice.ID == "" {
		return false
	}
	id := voice.ID
	for _, download := range manager.Downloads() {
		if download.ID == id && (download.State == "queued" || download.State == "downloading") {
			return true
		}
	}
	return false
}

// ensureTtsReadyForLang loads the saved voice, or an installed language
// fallback. Missing assets return not ready without starting a download.
func (a *App) ensureTtsReadyForLang(ctx context.Context, lang string) (speechSynthesizer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manager, err := a.setupModelManager()
	if err != nil {
		return nil, err
	}
	voice, installed := manager.VoiceForLanguage(lang)
	if !installed {
		return nil, models.ErrNotReady
	}
	st := a.ensureTtsState()
	lm := st.languageLoadMutex(voice.ID)
	lm.Lock()
	defer lm.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, installed := manager.VoiceForLanguage(lang)
	if !installed || current.ID != voice.ID {
		return nil, models.ErrNotReady
	}
	st.mu.Lock()
	engine, factory := st.engines[voice.ID], st.newEngine
	st.mu.Unlock()
	if engine != nil {
		return engine, nil
	}
	if factory != nil {
		engine, err = factory(voice.ID)
	} else {
		switch voice.ID {
		case "kokoro-en":
			engine, err = newKokoroTTS(defaultKokoroPaths())
		case "melotts-zh-en":
			engine, err = newSherpaTTS(defaultTtsPaths())
		default:
			err = models.ErrNotReady
		}
	}
	if err != nil {
		return nil, fmt.Errorf("tts engine (%s): %w", voice.ID, err)
	}
	st.mu.Lock()
	st.engines[voice.ID] = engine
	st.mu.Unlock()
	if a.ctx != nil {
		a.emitTtsEvent("tts:model-ready", map[string]any{"lang": lang})
	}
	return engine, nil
}

// SynthesizeSpeech is the Wails binding the chat UI calls to read a message
// aloud. It strips markdown to plain speakable text, ensures the engine is
// loaded from already installed model assets, and returns base64-encoded WAV
// bytes. The frontend wraps the result in a data: URL and plays it.
func (a *App) SynthesizeSpeech(text string) (string, error) {
	clean := stripMarkdownForSpeech(speechDisplayText(text, a.modelDisplayLanguage()))
	if clean == "" {
		return "", fmt.Errorf("no speakable text")
	}
	lang := dominantSpeechLang(clean)
	engine, err := a.ensureTtsReadyForLang(a.ttsContext(), lang)
	if err != nil {
		a.emitTtsModelError(lang, err)
		return "", err
	}
	wav, err := engine.Synthesize(clean)
	if err != nil {
		// A per-chunk synthesis failure is not a model download/load problem —
		// the frontend pipeline skips the failed chunk and only surfaces an
		// error when every chunk fails. Don't emit a misleading "download
		// failed" toast here; reserve tts:model-error for ensureTtsReadyForLang.
		return "", fmt.Errorf("synthesize: %w", err)
	}
	return base64.StdEncoding.EncodeToString(wav), nil
}

// SplitSpeechText cleans markdown and splits the reply into sentence-sized
// chunks so the frontend can stream synthesis: speak the first sentence while
// later ones are still being generated, cutting time-to-first-audio on long
// replies. Returns an empty slice when nothing is speakable.
func (a *App) SplitSpeechText(text string) []string {
	chunks := splitSpeechSentences(speechDisplayText(text, a.modelDisplayLanguage()))
	if chunks == nil {
		return []string{}
	}
	return chunks
}

// PrewarmTts warms, in the background, the engine(s) for the language(s) that
// text will actually use — called on hover so the first click doesn't pay
// model-load latency. It routes the same way SynthesizeSpeech does (per chunk),
// so a Chinese-only message only warms MeloTTS and an English-only message only
// warms Kokoro. Download-free: a language whose model isn't on disk yet is
// skipped, so hovering never triggers a (large) download or a progress toast —
// the actual click handles that with visible progress. Fire-and-forget.
func (a *App) PrewarmTts(text string) {
	langs := map[string]struct{}{}
	for _, chunk := range splitSpeechSentences(text) {
		langs[dominantSpeechLang(chunk)] = struct{}{}
	}
	for lang := range langs {
		if !a.ttsModelPresentForLang(lang) {
			continue // not installed yet; setup must confirm acquisition
		}
		go func(l string) {
			if _, err := a.ensureTtsReadyForLang(a.ttsContext(), l); err != nil {
				log.Printf("[tts] prewarm %s failed: %v", l, err)
			}
		}(lang)
	}
}

// TtsModelStatus reports, per language, whether the on-disk voice model is
// already downloaded. The Read Aloud settings page uses this so the user can
// see which voices are installed before enabling a category (and whether
// turning one on requires its voice to be installed). No network access.
type TtsModelStatus struct {
	Zh bool `json:"zh"`
	En bool `json:"en"`
}

// GetTtsModelStatus returns the on-disk presence of each voice model.
func (a *App) GetTtsModelStatus() TtsModelStatus {
	return TtsModelStatus{
		Zh: a.ttsModelPresentForLang("zh"),
		En: a.ttsModelPresentForLang("en"),
	}
}

// GetAutoSpeakEnabled reports whether assistant replies should be read aloud
// automatically once complete. Defaults to false (unset).
func (a *App) GetAutoSpeakEnabled() bool {
	cfg := loadConfig()
	if cfg.AutoSpeakEnabled != nil {
		return *cfg.AutoSpeakEnabled
	}
	return false
}

func (a *App) replaceAutoTtsPrewarmContext(enabled bool) context.Context {
	st := a.ensureTtsState()
	st.mu.Lock()
	defer st.mu.Unlock()

	if st.autoPrewarmCancel != nil {
		st.autoPrewarmCancel()
		st.autoPrewarmCancel = nil
	}
	if !enabled {
		return nil
	}

	ctx, cancel := context.WithCancel(a.ttsContext())
	st.autoPrewarmCancel = cancel
	return ctx
}

// GetAutoSpeakAutomationEnabled reports whether a short spoken announcement
// plays when a background automation run finishes. Defaults to false (unset).
func (a *App) GetAutoSpeakAutomationEnabled() bool {
	cfg := loadConfig()
	if cfg.AutoSpeakAutomationEnabled != nil {
		return *cfg.AutoSpeakAutomationEnabled
	}
	return false
}

// GetAutoSpeakNotificationsEnabled reports whether notification titles are read
// aloud when they fire. Defaults to false (unset).
func (a *App) GetAutoSpeakNotificationsEnabled() bool {
	cfg := loadConfig()
	if cfg.AutoSpeakNotificationsEnabled != nil {
		return *cfg.AutoSpeakNotificationsEnabled
	}
	return false
}

// anyAutoSpeakCategoryEnabled reports whether any auto-speak category is on, so
// the Chinese engine should stay prewarmed.
func (a *App) anyAutoSpeakCategoryEnabled() bool {
	return a.GetAutoSpeakEnabled() ||
		a.GetAutoSpeakAutomationEnabled() ||
		a.GetAutoSpeakNotificationsEnabled()
}

// defaultAutoSpeakLang picks which installed voice engine to prewarm when a
// category is first enabled. There is no message to language-route on yet, so
// we follow the user's UI language: a Chinese UI gets MeloTTS ("zh", ~160 MB),
// any other UI gets Kokoro ("en", ~320 MB). Actual playback still routes per
// message content and requires an installed voice for it; this only picks the
// most-likely-needed voice to warm up front.
func (a *App) defaultAutoSpeakLang() string {
	if a.currentUILanguage() == "zh-CN" {
		return "zh"
	}
	return "en"
}

// applyAutoSpeakPrewarm prewarms (or cancels prewarm of) the default voice
// engine based on whether any auto-speak category is currently enabled. Shared
// by all category setters so toggling any one of them keeps the warm-engine
// state and the first-load model download in sync.
func (a *App) applyAutoSpeakPrewarm() {
	enabled := a.anyAutoSpeakCategoryEnabled()
	lang := a.defaultAutoSpeakLang()
	prewarmCtx := a.replaceAutoTtsPrewarmContext(enabled)
	if !enabled {
		a.emitTtsModelCancelled(lang)
		return
	}
	if prewarmCtx != nil {
		go func() {
			if _, err := a.ensureTtsReadyForLang(prewarmCtx, lang); err != nil {
				if prewarmCtx.Err() != nil {
					return
				}
				log.Printf("[tts] prewarm failed: %v", err)
				a.emitTtsModelError(lang, err)
			}
		}()
	}
}

// SetAutoSpeakEnabled persists the chat auto-read-aloud preference. Enabling it
// prewarms the Chinese (default) engine in the background so the first
// auto-read doesn't stall on model load; the English engine still loads on
// demand the first time an English reply is spoken.
func (a *App) SetAutoSpeakEnabled(enabled bool) error {
	if err := updateConfig(func(c *yansuConfig) {
		v := enabled
		c.AutoSpeakEnabled = &v
	}); err != nil {
		return err
	}
	a.applyAutoSpeakPrewarm()
	return nil
}

// SetAutoSpeakAutomationEnabled persists the automation-completion announcement
// preference and keeps the Chinese prewarm state in sync.
func (a *App) SetAutoSpeakAutomationEnabled(enabled bool) error {
	if err := updateConfig(func(c *yansuConfig) {
		v := enabled
		c.AutoSpeakAutomationEnabled = &v
	}); err != nil {
		return err
	}
	a.applyAutoSpeakPrewarm()
	return nil
}

// SetAutoSpeakNotificationsEnabled persists the notification read-aloud
// preference and keeps the Chinese prewarm state in sync.
func (a *App) SetAutoSpeakNotificationsEnabled(enabled bool) error {
	if err := updateConfig(func(c *yansuConfig) {
		v := enabled
		c.AutoSpeakNotificationsEnabled = &v
	}); err != nil {
		return err
	}
	a.applyAutoSpeakPrewarm()
	return nil
}

// ttsNumThreads is how many CPU threads one voice engine uses. Kokoro costs
// about 1.1 CPU-seconds per spoken second on two threads and about 1.5 on four
// (measured 2026-11-13, Apple silicon), so four threads read a brief about a
// third faster on an idle computer; eight only spend more CPU. Small machines
// keep two.
var ttsNumThreads = ttsThreadsFor(goruntime.NumCPU())

func ttsThreadsFor(cpus int) int {
	if cpus >= 8 {
		return 4
	}
	return 2
}
