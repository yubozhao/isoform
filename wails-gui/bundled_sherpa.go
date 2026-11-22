package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"wails-gui/models"
)

// Download URLs. We try the Yansu R2 mirror first (fast everywhere, including
// regions where Hugging Face / GitHub release CDNs are flaky) and fall back
// to upstream if the mirror is down. Each asset can be overridden by an env
// var; when set, the env override takes precedence over both defaults.
var (
	senseVoiceModelMirrorURLs = []string{
		"https://model-assets.yansu.app/sherpa/model.int8.onnx",
		"https://huggingface.co/csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17/resolve/main/model.int8.onnx",
	}
	senseVoiceTokensMirrorURLs = []string{
		"https://model-assets.yansu.app/sherpa/tokens.txt",
		"https://huggingface.co/csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17/resolve/main/tokens.txt",
	}
	sileroVadMirrorURLs = []string{
		"https://model-assets.yansu.app/sherpa/silero_vad.onnx",
		"https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx",
	}
	// 3D-Speaker CAM++ trained on CN-Celeb + VoxCeleb. ~28 MB. Works on
	// Chinese AND English (multilingual training set). The de-facto
	// default for sherpa-onnx speaker verification in mixed-locale apps.
	speakerEmbeddingMirrorURLs = []string{
		"https://model-assets.yansu.app/sherpa/3dspeaker_campplus.onnx",
		"https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/3dspeaker_speech_campplus_sv_zh-cn_16k-common.onnx",
	}
)

const (
	envSenseVoiceModelURL  = "YANSU_SHERPA_SENSE_VOICE_MODEL_URL"
	envSenseVoiceTokensURL = "YANSU_SHERPA_SENSE_VOICE_TOKENS_URL"
	envSileroVadURL        = "YANSU_SHERPA_SILERO_VAD_URL"
	envSpeakerEmbeddingURL = "YANSU_SHERPA_SPEAKER_EMBEDDING_URL"

	// SHA-256 of the canonical upstream files. We verify every download
	// against these so a corrupt mirror, partial download, or in-flight
	// tamper can't crash sherpa-onnx with a model-load abort. Hashes were
	// captured from upstream Hugging Face / GitHub release after manual
	// verification on 2026-05-06.
	sha256SenseVoiceModel  = "c71f0ce00bec95b07744e116345e33d8cbbe08cef896382cf907bf4b51a2cd51"
	sha256SenseVoiceTokens = "f449eb28dc567533d7fa59be34e2abca8784f771850c78a47fb731a31429a1dc" // gitleaks:allow
	sha256SileroVad        = "9e2449e1087496d8d4caba907f23e0bd3f78d91fa552479bb9c23ac09cbb1fd6"
	// CAM++ 16k-common (upstream release asset 198893103), verified from
	// upstream bytes. Diarization pins both models, including mirror bytes.
	sha256SpeakerEmbedding    = "f682b514c05d947ee3fa91cd6ec6c5c7543479a128373fa29b1faedccd21fd11"
	sha256SpeakerSegmentation = "d582f4b4c6b48205de7e0643c57df0df5615a3c176189be3fc461e9d18827b5d"
	speakerSegmentationURL    = "https://huggingface.co/csukuangfj/sherpa-onnx-pyannote-segmentation-3-0/resolve/9403a6902bb58e3d5ae8c7e77c3422de279db2e0/model.int8.onnx"
)

// sherpaDownloadProgress is a callback invoked while downloading a single asset.
// total < 0 means the server didn't advertise Content-Length.
type sherpaDownloadProgress func(asset string, downloaded, total int64, bytesPerSec float64)

// ensureSherpaAssets is the voice setup entry point. This build delegates
// downloads to the setup model manager.
func ensureSherpaAssets(ctx context.Context, paths sherpaPaths, progress sherpaDownloadProgress) error {
	return requireInstalledSherpaAssets(paths)
}

// requireInstalledSherpaAssets verifies the local recognizer and VAD pack.
func requireInstalledSherpaAssets(paths sherpaPaths) error {
	for _, path := range []string{paths.senseVoice, paths.tokens, paths.silero, paths.speakerEmbd, paths.segmentation} {
		if path != "" && !fileExistsAndNonEmpty(path) {
			return models.ErrNotReady
		}
	}
	return models.Require(configDirOrFallback(), "sensevoice", "speaker-pack")
}

// sha256OrEmpty returns the canonical SHA-256 unless the user has overridden
// the URL via env, in which case verification is skipped (the override is
// presumed to point at intentionally different bytes — e.g., a private
// mirror, a smaller quantized variant, or a fine-tuned model).
func sha256OrEmpty(hashHex string, isOverride bool) string {
	if isOverride {
		return ""
	}
	return hashHex
}

func fileExistsAndNonEmpty(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Size() > 0
}

// sherpaDownloadIdleTimeout ends a download that receives nothing for this
// long. A slow download that keeps moving runs as long as it needs (a fixed
// total limit failed every slower link on the large models), and an
// interrupted one resumes from its .partial file next time.
var sherpaDownloadIdleTimeout = 90 * time.Second

// downloadSherpaAsset streams url into dest atomically and (when expectedSHA256
// is non-empty) verifies the SHA-256 hex digest matches before renaming the
// .partial file into place. A mismatch discards the partial so the caller
// can fall back to the next mirror — this prevents a corrupt or tampered
// model from reaching disk and crashing sherpa-onnx with a C-side abort at
// load time. An interrupted download keeps its .partial file, and the next
// attempt asks the server for the rest (an HTTP Range request); a server
// that does not resume starts the file over. A resume is safe only when the
// server can tell the file is still the one the partial came from (If-Range
// with the ETag or Last-Modified kept beside it) and the whole is checked
// against its SHA-256; otherwise the download starts over.
//
//isoform:destination metadata
func downloadSherpaAsset(ctx context.Context, name, url, dest, expectedSHA256 string, progress sherpaDownloadProgress) error {
	dlCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	idle := time.AfterFunc(sherpaDownloadIdleTimeout, cancel)
	defer idle.Stop()

	tmp := dest + ".partial"
	validatorPath := tmp + sherpaResumeValidatorSuffix
	var have int64
	if st, err := os.Stat(tmp); err == nil && !st.IsDir() {
		have = st.Size()
	}
	validator := ""
	if have > 0 {
		if raw, err := os.ReadFile(validatorPath); err == nil {
			validator = strings.TrimSpace(string(raw))
		}
		if validator == "" || expectedSHA256 == "" {
			os.Remove(tmp)
			os.Remove(validatorPath)
			have = 0
		}
	}

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		req.Header.Set("If-Range", validator)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// The whole file (a server that does not resume, or a file that
		// changed): remember what identifies it for a later resume.
		have = 0
		if v := sherpaResumeValidator(resp.Header); v != "" {
			_ = os.WriteFile(validatorPath, []byte(v), 0o644)
		} else {
			os.Remove(validatorPath)
		}
	case http.StatusPartialContent:
		if !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", have)) {
			os.Remove(tmp)
			return fmt.Errorf("resume answered with %q", resp.Header.Get("Content-Range"))
		}
	case http.StatusRequestedRangeNotSatisfiable:
		// The partial is as long as the file or longer: start over.
		os.Remove(tmp)
		return fmt.Errorf("HTTP %s", resp.Status)
	default:
		return fmt.Errorf("HTTP %s", resp.Status)
	}

	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if have > 0 {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	out, err := os.OpenFile(tmp, flags, 0o644)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer out.Close()

	var hasher hash.Hash
	if expectedSHA256 != "" {
		hasher = sha256.New()
		if have > 0 {
			existing, err := os.Open(tmp)
			if err != nil {
				return err
			}
			_, err = io.CopyN(hasher, existing, have)
			existing.Close()
			if err != nil {
				os.Remove(tmp)
				return fmt.Errorf("read partial: %w", err)
			}
		}
	}

	total := resp.ContentLength
	if total > 0 {
		total += have
	}
	pr := &sherpaProgressReader{
		r:        resp.Body,
		total:    total,
		read:     have,
		name:     name,
		callback: progress,
		started:  time.Now(),
		lastTick: time.Now(),
		hasher:   hasher,
		onRead:   func() { idle.Reset(sherpaDownloadIdleTimeout) },
	}
	if _, err := io.Copy(out, pr); err != nil {
		// The partial stays for the next attempt to resume.
		return fmt.Errorf("download body (%d bytes kept to resume): %w", pr.read, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	if hasher != nil {
		got := hex.EncodeToString(hasher.Sum(nil))
		if got != expectedSHA256 {
			os.Remove(tmp)
			os.Remove(validatorPath)
			return fmt.Errorf("checksum mismatch: got sha256=%s want sha256=%s (corrupt or tampered download from %s)", got, expectedSHA256, url)
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("rename to %s: %w", dest, err)
	}
	os.Remove(validatorPath)
	if progress != nil {
		progress(name, pr.read, pr.total, 0)
	}
	return nil
}

// sherpaResumeValidatorSuffix names the file beside a partial download that
// keeps what identifies the file it came from.
const sherpaResumeValidatorSuffix = ".validator"

// sherpaResumeValidator is the If-Range value for a later resume: a strong
// ETag, else Last-Modified.
func sherpaResumeValidator(h http.Header) string {
	if etag := strings.TrimSpace(h.Get("ETag")); etag != "" && !strings.HasPrefix(etag, "W/") {
		return etag
	}
	return strings.TrimSpace(h.Get("Last-Modified"))
}

type sherpaProgressReader struct {
	r        io.Reader
	total    int64
	read     int64
	name     string
	callback sherpaDownloadProgress
	started  time.Time
	lastTick time.Time
	hasher   hash.Hash // optional; when non-nil, fed the same bytes io.Copy writes to disk
	onRead   func()    // optional; called on every read that brings data
}

func (p *sherpaProgressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 && p.hasher != nil {
		p.hasher.Write(buf[:n])
	}
	if n > 0 && p.onRead != nil {
		p.onRead()
	}
	p.read += int64(n)
	now := time.Now()
	if p.callback != nil && now.Sub(p.lastTick) >= 500*time.Millisecond {
		elapsed := now.Sub(p.started).Seconds()
		var bps float64
		if elapsed > 0 {
			bps = float64(p.read) / elapsed
		}
		p.callback(p.name, p.read, p.total, bps)
		p.lastTick = now
	}
	return n, err
}
