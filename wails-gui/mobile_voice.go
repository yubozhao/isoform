package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wails-gui/storage"
)

// The owner's voice, added from the phone (PROTOCOL.md section 25). The phone
// records a short sample, seals it as an upload (section 24.1) and sends the
// key here. The computer opens it, takes the voice fingerprint with its local
// speaker model, stores the fingerprint beside the samples enrolled on the
// computer, and keeps no audio: the opened sample lives in a temporary file
// for the length of the request and the relay copy is deleted. The phone can
// list the owner's samples and remove one or all of them.

const (
	mobileVoiceMinSeconds = 3
	mobileVoiceMaxSeconds = 30
	mobileVoiceMaxBytes   = 4 << 20
	mobileVoiceFetchWait  = time.Minute
	// mobileVoicePhoneLabel starts the label of a sample that came from a phone.
	mobileVoicePhoneLabel = "phone "
)

var (
	errMobileVoiceTooShort = errors.New("the sample is too short; record at least 3 seconds of speech")
	errMobileVoiceInvalid  = errors.New("the voice sample is not valid")
)

// MobileVoiceSampleRequest is the body of POST /voice/samples.
type MobileVoiceSampleRequest struct {
	IdempotencyKey  string                `json:"idempotencyKey"`
	Upload          MobileRecordingUpload `json:"upload"`
	SHA256          string                `json:"sha256"`
	Bytes           int64                 `json:"bytes"`
	Mime            string                `json:"mime"`
	DurationSeconds float64               `json:"durationSeconds"`
}

// MobileVoiceSample is one enrolled sample as the phone sees it. No
// fingerprint and no audio is ever sent.
type MobileVoiceSample struct {
	ID           int64   `json:"id"`
	AudioSeconds float64 `json:"audioSeconds"`
	CapturedAt   string  `json:"capturedAt"`
	FromPhone    bool    `json:"fromPhone"`
}

// MobileVoiceView answers GET /voice.
type MobileVoiceView struct {
	// ModelReady is false while this computer has no speaker model yet; a
	// sample cannot be added until it has.
	ModelReady bool                `json:"modelReady"`
	Samples    []MobileVoiceSample `json:"samples"`
}

// mobileVoiceEmbedder turns an audio file into a voice fingerprint and its
// length in seconds. The default decodes with the bundled ffmpeg and uses the
// enrollment extractor; tests replace it.
var mobileVoiceEmbedder = func(a *App, path string) ([]float32, float64, error) {
	state, err := a.ensureVoiceEnrollment()
	if err != nil {
		return nil, 0, err
	}
	wavPath, cleanup, err := decodeMobileRecordingToWAV(installedFFmpegPath(), path)
	if err != nil {
		return nil, 0, err
	}
	defer cleanup()
	info, err := readMeetingWAVInfo(wavPath)
	if err != nil {
		return nil, 0, err
	}
	reader, err := openMeetingWAVChannel(wavPath, info, 0)
	if err != nil {
		return nil, 0, err
	}
	defer reader.Close()
	var samples []float32
	for len(samples) < (mobileVoiceMaxSeconds+1)*meetingAudioSampleRate {
		block, err := reader.ReadSamples()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		samples = append(samples, block...)
	}
	seconds := float64(len(samples)) / meetingAudioSampleRate
	if seconds < mobileVoiceMinSeconds {
		return nil, seconds, errMobileVoiceTooShort
	}
	emb, err := state.extractor.ExtractFromPCM(samples, meetingAudioSampleRate)
	if err != nil || len(emb) == 0 {
		return nil, seconds, fmt.Errorf("voice fingerprint: %w", err)
	}
	return emb, seconds, nil
}

func validateMobileVoiceSample(req MobileVoiceSampleRequest) error {
	switch {
	case !mobileValidUUID(req.Upload.ID):
		return errMobileVoiceInvalid
	case req.Bytes < 1 || req.Bytes > mobileVoiceMaxBytes:
		return errMobileVoiceInvalid
	case req.DurationSeconds < mobileVoiceMinSeconds || req.DurationSeconds > mobileVoiceMaxSeconds:
		return errMobileVoiceTooShort
	}
	if sum, err := hex.DecodeString(req.SHA256); err != nil || len(sum) != sha256.Size || strings.ToLower(req.SHA256) != req.SHA256 {
		return errMobileVoiceInvalid
	}
	switch req.Mime {
	case "audio/mp4", "audio/wav":
		return nil
	default:
		return errMobileVoiceInvalid
	}
}

// enrollMobileVoiceSample fetches and opens the sealed sample, fingerprints it
// and stores the fingerprint for the owner. The audio is written to a
// temporary file only while it is fingerprinted, and the relay copy is deleted
// whether or not the sample was usable.
func (a *App) enrollMobileVoiceSample(ctx context.Context, store *storage.Store, fetcher mobileUploadFetcher, hubID, deviceID string, req MobileVoiceSampleRequest) (MobileVoiceSample, error) {
	key, keyErr := base64.RawURLEncoding.DecodeString(req.Upload.Key)
	nonce, nonceErr := base64.RawURLEncoding.DecodeString(req.Upload.Nonce)
	if keyErr != nil || nonceErr != nil {
		return MobileVoiceSample{}, errMobileAttachmentKey
	}
	sealed, err := fetcher.downloadUpload(ctx, req.Upload.ID)
	if err != nil {
		return MobileVoiceSample{}, err
	}
	// From here the relay holds a copy the computer has read: it goes, even
	// when the sample turns out to be unusable.
	defer func() {
		if err := fetcher.deleteUpload(context.WithoutCancel(ctx), req.Upload.ID); err != nil {
			log.Printf("[voice] relay copy of a phone sample not removed yet: %v", err)
		}
	}()
	plaintext, err := openMobileUpload(key, nonce, hubID, deviceID, req.Upload.ID, sealed)
	if err != nil {
		return MobileVoiceSample{}, err
	}
	sum := sha256.Sum256(plaintext)
	if int64(len(plaintext)) != req.Bytes || hex.EncodeToString(sum[:]) != req.SHA256 {
		return MobileVoiceSample{}, errMobileRecordingSHA
	}
	dir, err := os.MkdirTemp("", "isoform-voice-sample-")
	if err != nil {
		return MobileVoiceSample{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "sample"+mobileRecordingExtension(req.Mime))
	if err := os.WriteFile(path, plaintext, 0600); err != nil {
		return MobileVoiceSample{}, err
	}

	a.voiceMemoryMu.Lock()
	generation := a.voiceMemoryGeneration
	a.voiceMemoryMu.Unlock()
	emb, seconds, err := mobileVoiceEmbedder(a, path)
	if err != nil {
		return MobileVoiceSample{}, err
	}
	self, err := store.EnsureSelf(ctx, "", nil)
	if err != nil {
		return MobileVoiceSample{}, err
	}
	// "Forget voices" on the computer while the sample was being read wins.
	a.voiceMemoryMu.Lock()
	defer a.voiceMemoryMu.Unlock()
	if a.voiceMemoryGeneration != generation {
		return MobileVoiceSample{}, errors.New("voice enrollment was forgotten")
	}
	saved, err := store.CreateVoiceEmbedding(ctx, storage.VoiceEmbeddingRow{
		PrincipalID:  self.ID,
		Embedding:    emb,
		SampleRate:   meetingAudioSampleRate,
		AudioSeconds: seconds,
		Label:        mobileVoicePhoneLabel + time.Now().Format("2006-01-02 15:04"),
	})
	if err != nil {
		return MobileVoiceSample{}, err
	}
	a.mu.Lock()
	_ = a.refreshSelfEmbeddingCacheLocked()
	a.mu.Unlock()
	return mobileVoiceSampleView(saved), nil
}

func mobileVoiceSampleView(row storage.VoiceEmbeddingRow) MobileVoiceSample {
	return MobileVoiceSample{ID: row.ID, AudioSeconds: row.AudioSeconds,
		CapturedAt: mobileTimestamp(time.UnixMilli(row.CapturedAt)), FromPhone: strings.HasPrefix(row.Label, mobileVoicePhoneLabel)}
}

// mobileVoiceSamples lists the owner's enrolled samples, newest first.
func mobileVoiceSamples(ctx context.Context, store *storage.Store) ([]MobileVoiceSample, error) {
	rows, err := store.ListVoiceEmbeddingsForKind(ctx, storage.PrincipalKindSelf)
	if err != nil {
		return nil, err
	}
	out := make([]MobileVoiceSample, 0, len(rows))
	for _, row := range rows {
		out = append(out, mobileVoiceSampleView(row))
	}
	return out, nil
}

// forgetMobileVoice removes one of the owner's samples, or all of them when id
// is 0. It reports how many went. Only the owner's samples can be named.
func (a *App) forgetMobileVoice(ctx context.Context, store *storage.Store, id int64) (int, error) {
	rows, err := store.ListVoiceEmbeddingsForKind(ctx, storage.PrincipalKindSelf)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, row := range rows {
		if id != 0 && row.ID != id {
			continue
		}
		if err := store.DeleteVoiceEmbedding(ctx, row.ID); err != nil {
			return removed, err
		}
		removed++
	}
	a.mu.Lock()
	_ = a.refreshSelfEmbeddingCacheLocked()
	a.mu.Unlock()
	return removed, nil
}

// serveVoice handles GET /voice.
func (bridge *mobileBridge) serveVoice(w http.ResponseWriter, r *http.Request) {
	store := bridge.app.storeForMobile()
	if store == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	samples, err := mobileVoiceSamples(r.Context(), store)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	ready := false
	if paths := defaultSherpaPaths(); paths.speakerEmbd != "" {
		_, statErr := os.Stat(paths.speakerEmbd)
		ready = statErr == nil
	}
	mobileJSON(w, http.StatusOK, MobileVoiceView{ModelReady: ready, Samples: samples}, "")
}

// serveVoiceSample handles POST /voice/samples.
func (bridge *mobileBridge) serveVoiceSample(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var req MobileVoiceSampleRequest
	if !mobileDecode(w, r, &req) {
		return
	}
	key, valid := mobileKey(device.DeviceID, req.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if err := validateMobileVoiceSample(req); err != nil {
		mobileJSON(w, http.StatusBadRequest, nil, err.Error())
		return
	}
	bridge.mu.Lock()
	client, identity := bridge.client, bridge.identity
	bridge.mu.Unlock()
	store := bridge.app.storeForMobile()
	if client == nil || identity == nil || store == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	// Receipted: a repeat after a lost answer replays the stored sample
	// instead of enrolling the same words twice.
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		ctx, cancel := context.WithTimeout(bridge.ctx, mobileVoiceFetchWait)
		defer cancel()
		sample, err := bridge.app.enrollMobileVoiceSample(ctx, store, client, identity.HubID, device.DeviceID, req)
		switch {
		case err == nil:
			mobileJSON(w, http.StatusCreated, sample, "")
		case errors.Is(err, errMobileRecordingGone):
			mobileJSON(w, http.StatusNotFound, map[string]string{"code": "upload_missing"}, err.Error())
		case errors.Is(err, errVoiceModelMissing):
			mobileJSON(w, http.StatusConflict, map[string]string{"code": "voice_model_missing"}, "this computer has no voice model yet")
		case errors.Is(err, errMobileVoiceTooShort), errors.Is(err, errMobileVoiceInvalid), errors.Is(err, errMobileRecordingSHA),
			errors.Is(err, errMobileAttachmentRejected), errors.Is(err, errMobileAttachmentKey):
			mobileJSON(w, http.StatusBadRequest, nil, err.Error())
		default:
			log.Printf("[voice] phone sample not enrolled: %v", err)
			mobileJSON(w, http.StatusServiceUnavailable, nil, "the computer could not read the sample yet")
		}
	})
}

// serveVoiceForget handles POST /voice/forget.
func (bridge *mobileBridge) serveVoiceForget(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		// ID names one sample; 0 or absent removes every sample of the owner.
		ID int64 `json:"id"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	if _, valid := mobileKey(device.DeviceID, input.IdempotencyKey); !valid || input.ID < 0 {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key and a valid sample id are required")
		return
	}
	store := bridge.app.storeForMobile()
	if store == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	// Removing is safe to repeat, so it keeps no receipt.
	removed, err := bridge.app.forgetMobileVoice(r.Context(), store, input.ID)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	samples, err := mobileVoiceSamples(r.Context(), store)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, map[string]any{"removed": removed, "samples": samples}, "")
}
