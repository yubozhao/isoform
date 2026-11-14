package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"
)

// The generation key is independent of envelope delivery sequence numbers:
// adding an attachment or refreshing live rows does not create a new recording.
func mobileBriefAudioVersion(brief MobileBriefV1) string {
	return brief.BriefID + "|" + brief.AsOf
}

// Retry a failed relay upload without making the phone wait for the file.
// The service context ends this worker at shutdown; a newer brief ends it too.
func (s *mobileBriefService) scheduleBriefAudioRetry(brief MobileBriefV1) {
	s.mu.Lock()
	if s.stopped || s.runCtx == nil || s.app == nil {
		s.mu.Unlock()
		return
	}
	ctx := s.runCtx
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		s.retryBriefAudio(ctx, brief, mobileBriefRetryBackoff, func(ctx context.Context, _ []byte) (*MobileBriefAudioAttachment, error) {
			path, _, found := mobileBriefAudioPath(s.store.dir, brief.BriefID)
			if !found {
				return nil, errMobileBriefNoAudio
			}
			return s.app.sealMobileBriefAudio(ctx, brief.BriefID, path)
		})
	}()
}

// resumeBriefAudioRetry picks the retry up again after a relaunch: today's
// latest brief whose recording is on this computer but never reached the
// relay is sent again (today-review v1.1.9 #1).
func (s *mobileBriefService) resumeBriefAudioRetry() {
	if latest, ok := s.briefAudioToResend(); ok {
		s.scheduleBriefAudioRetry(latest)
	}
}

func (s *mobileBriefService) briefAudioToResend() (MobileBriefV1, bool) {
	latest, ok := s.store.latest()
	if !ok || latest.Audio == nil || latest.Audio.Status != mobileBriefAudioStatusOK || latest.Audio.Bytes == 0 || latest.Audio.Attachment != nil {
		return MobileBriefV1{}, false
	}
	loc := s.location
	if loc == nil {
		loc = time.Local
	}
	return latest, latest.LocalDate == s.now().In(loc).Format("2006-01-02")
}

type mobileBriefAudioRetryContextKey struct{}

func (s *mobileBriefService) retryBriefAudio(ctx context.Context, original MobileBriefV1, delay func(int) time.Duration, upload func(context.Context, []byte) (*MobileBriefAudioAttachment, error)) {
	version := mobileBriefAudioVersion(original)
	for attempt := 1; ; attempt++ {
		timer := time.NewTimer(delay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		latest, ok := s.store.latest()
		if !ok || mobileBriefAudioVersion(latest) != version {
			return
		}
		attemptCtx, cancel := context.WithTimeout(context.WithValue(ctx, mobileBriefAudioRetryContextKey{}, true), mobileAttachmentUploadWait)
		err := s.prepareMobileBriefAudio(attemptCtx, original.BriefID, version, upload)
		cancel()
		if errors.Is(err, errMobileAttachmentOffline) || errors.Is(err, errMobileBriefNoAudio) {
			// No paired phone reads briefs, or the file is gone: nothing to
			// send. A phone paired later asks for the audio itself.
			return
		}
		if err != nil {
			continue
		}
		latest, ok = s.GetMobileBrief(original.BriefID)
		current, currentOK := s.store.latest()
		if !ok || !currentOK || mobileBriefAudioVersion(current) != version {
			return
		}
		s.deliverStoredBrief(latest, func(brief MobileBriefV1) { _ = s.publish(brief, false) })
		if s.app != nil {
			s.app.emitBrowserEvent("brief:stored", original.BriefID)
		}
		return
	}
}

var errMobileBriefAudioBusy = errors.New("brief audio is still preparing")

type mobileBriefAudioResource struct {
	BriefID string            `json:"briefId"`
	Version string            `json:"version"`
	Audio   *MobileBriefAudio `json:"audio"`
}

func mobileBriefAudioFailure(w http.ResponseWriter, status int, reason string) {
	mobileJSON(w, status, map[string]string{"reason": reason}, reason)
}

// serveBriefAudio returns only sealed attachment coordinates through the normal
// authorized bridge. The file is downloaded once from the bearer-protected
// attachment route; neither the relay nor a plaintext HTTP route gets its key.
func (bridge *mobileBridge) serveBriefAudio(w http.ResponseWriter, r *http.Request, briefID string) {
	if !mobileValidBriefID(briefID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the brief id is not valid")
		return
	}
	versions := r.URL.Query()["version"]
	if len(versions) != 1 || versions[0] == "" || len(versions[0]) > 256 {
		mobileJSON(w, http.StatusBadRequest, nil, "one brief version is required")
		return
	}
	version := versions[0]
	svc := bridge.briefSource()
	if svc == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileBriefUnavailable.Error())
		return
	}
	brief, found := svc.GetMobileBrief(briefID)
	if !found {
		mobileBriefAudioFailure(w, http.StatusNotFound, "audio_unavailable")
		return
	}
	if mobileBriefAudioVersion(brief) != version {
		mobileBriefAudioFailure(w, http.StatusConflict, "version_changed")
		return
	}
	if brief.Audio == nil || brief.Audio.Status == mobileBriefAudioStatusDownloading || brief.Audio.Status == mobileBriefAudioStatusRendering {
		reason := "audio_rendering"
		if brief.Audio != nil && brief.Audio.Status == mobileBriefAudioStatusDownloading {
			reason = "voice_not_installed"
		} else if service, ok := svc.(*mobileBriefService); ok {
			// A policy-withdrawn recording is unavailable, not still rendering.
			if stored, found := service.store.load(briefID); found && stored.Audio != nil {
				reason = "audio_unavailable"
			} else if !service.mobileBriefVoiceInstalled() {
				reason = "voice_not_installed"
			}
		}
		mobileBriefAudioFailure(w, http.StatusNotFound, reason)
		return
	}
	if !mobileBriefAudioReady(brief.Audio) {
		mobileBriefAudioFailure(w, http.StatusNotFound, "audio_unavailable")
		return
	}
	if brief.Audio.Attachment == nil {
		service, ok := svc.(*mobileBriefService)
		if !ok {
			mobileBriefAudioFailure(w, http.StatusNotFound, "audio_unavailable")
			return
		}
		if len(brief.Audio.SHA256) != 64 {
			mobileBriefAudioFailure(w, http.StatusNotFound, "audio_unavailable")
			return
		}
		err := service.queueMobileBriefAudio(bridge.ctx, briefID, version, bridge.uploadBriefAudioSnapshot)
		reason := "audio_rendering"
		if err != nil && !errors.Is(err, errMobileBriefAudioBusy) {
			reason = "audio_unavailable"
		}
		mobileBriefAudioFailure(w, http.StatusNotFound, reason)
		return
	}
	// Recheck source policy before returning an existing key. Background upload
	// completion also rechecks the version and source policy before storing it.
	current, found := svc.GetMobileBrief(briefID)
	if !found || current.Audio == nil {
		mobileBriefAudioFailure(w, http.StatusNotFound, "audio_unavailable")
		return
	}
	if mobileBriefAudioVersion(current) != version {
		mobileBriefAudioFailure(w, http.StatusConflict, "version_changed")
		return
	}
	if !mobileBriefAudioReady(current.Audio) || current.Audio.Attachment == nil {
		mobileBriefAudioFailure(w, http.StatusNotFound, "audio_unavailable")
		return
	}
	mobileJSON(w, http.StatusOK, mobileBriefAudioResource{BriefID: briefID, Version: version, Audio: current.Audio}, "")
}

func mobileBriefAudioReady(audio *MobileBriefAudio) bool {
	if audio == nil || (audio.Status != "" && audio.Status != mobileBriefAudioStatusOK) {
		return false
	}
	return (audio.Mime == mobileBriefAudioMimeM4A || audio.Mime == mobileBriefAudioMimeWAV) &&
		audio.Bytes > 0 && audio.Bytes <= mobileAttachmentMaxBytes-16 && audio.DurationSeconds > 0 &&
		!math.IsNaN(audio.DurationSeconds) && !math.IsInf(audio.DurationSeconds, 0)
}

func (s *mobileBriefService) mobileBriefVoiceInstalled() bool {
	if s.speaker.voiceReady == nil {
		return false
	}
	lang := "en"
	if s.speaker.language != nil {
		lang = s.speaker.language()
	}
	return s.speaker.voiceReady(mobileBriefVoiceLang(lang))
}

func (bridge *mobileBridge) uploadBriefAudioSnapshot(ctx context.Context, data []byte) (*MobileBriefAudioAttachment, error) {
	bridge.mu.Lock()
	client, identity := bridge.client, bridge.identity
	bridge.mu.Unlock()
	if client == nil || identity == nil {
		return nil, errMobileAttachmentOffline
	}
	return sealAndUploadAttachmentBytes(ctx, bridge.attachmentUploader(client), identity.HubID, data)
}

// queueMobileBriefAudio starts at most one background preparation. The serial
// device worker must remain available for chat and status while bytes upload.
func (s *mobileBriefService) queueMobileBriefAudio(parent context.Context, briefID, version string, upload func(context.Context, []byte) (*MobileBriefAudioAttachment, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || parent.Err() != nil {
		return errMobileBriefUnavailable
	}
	if s.audioUploadVersion == version && s.audioUploadErr != nil {
		err := s.audioUploadErr
		s.audioUploadErr = nil
		return err
	}
	if !s.audioMu.TryLock() {
		return errMobileBriefAudioBusy
	}
	s.audioUploadVersion, s.audioUploadErr = version, nil
	ctx, cancel := context.WithTimeout(parent, mobileAttachmentUploadWait)
	s.audioUploadCancel = cancel
	stopServiceCancel := func() bool { return false }
	if s.runCtx != nil {
		stopServiceCancel = context.AfterFunc(s.runCtx, cancel)
	}
	// Stop sets stopped under mu before waiting, so this Add cannot race a
	// new task into an already completed service shutdown.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.audioMu.Unlock()
		defer cancel()
		defer stopServiceCancel()
		err := s.prepareMobileBriefAudioLocked(ctx, briefID, version, upload)
		s.mu.Lock()
		if s.audioUploadVersion == version {
			s.audioUploadErr = err
			s.audioUploadCancel = nil
		}
		s.mu.Unlock()
	}()
	return nil
}

// prepareMobileBriefAudio can upload already produced audio after pairing or a
// relay reconnect. It never synthesizes or installs a voice. The digest prevents
// a same-ID regeneration from associating old metadata with new file bytes.
func (s *mobileBriefService) prepareMobileBriefAudio(ctx context.Context, briefID, version string, upload func(context.Context, []byte) (*MobileBriefAudioAttachment, error)) error {
	// A read cannot wait behind synthesis or another phone's upload: that
	// would consume its bridge deadline before this upload even starts.
	if !s.audioMu.TryLock() {
		return errMobileBriefAudioBusy
	}
	defer s.audioMu.Unlock()
	return s.prepareMobileBriefAudioLocked(ctx, briefID, version, upload)
}

func (s *mobileBriefService) prepareMobileBriefAudioLocked(ctx context.Context, briefID, version string, upload func(context.Context, []byte) (*MobileBriefAudioAttachment, error)) error {
	brief, found := s.GetMobileBrief(briefID)
	if !found || mobileBriefAudioVersion(brief) != version || !mobileBriefAudioReady(brief.Audio) {
		return errMobileBriefNoAudio
	}
	if brief.Audio.Attachment != nil {
		return nil
	}
	// Old local-only recordings lack the generation digest. Fail closed until
	// the computer produces another brief instead of guessing their identity.
	if len(brief.Audio.SHA256) != 64 {
		return errMobileBriefNoAudio
	}
	path, mime, found := mobileBriefAudioPath(s.store.dir, briefID)
	if !found || mime != brief.Audio.Mime {
		return errMobileBriefNoAudio
	}
	file, err := os.Open(path)
	if err != nil {
		return errMobileBriefNoAudio
	}
	data, readErr := io.ReadAll(io.LimitReader(file, mobileAttachmentMaxBytes))
	_ = file.Close()
	if readErr != nil || len(data) != brief.Audio.Bytes || fmt.Sprintf("%x", sha256.Sum256(data)) != brief.Audio.SHA256 {
		return errMobileBriefNoAudio
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	attachment, err := upload(ctx, data)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if attachment == nil {
		return errors.New("audio attachment unavailable")
	}
	// The projection reads sources, which may be slow, so it runs before patchMu.
	// The write under the lock is valid only if the privacy state and the stored
	// brief are the ones the projection saw; otherwise the attachment is dropped.
	pre, preFound := s.store.load(briefID)
	preEpoch := s.app.briefPrivacyPrint()
	preProjected := s.app.mobileBriefForDelivery(pre)
	if !preFound || mobileBriefAudioVersion(preProjected) != version || preProjected.Audio == nil || preProjected.Audio.SHA256 != brief.Audio.SHA256 {
		return errMobileBriefNoAudio
	}
	// The source-change hook runs after patchMu is released (deferred calls run
	// last-in first-out, so it is registered before the unlock).
	written := false
	defer func() {
		if written {
			briefSourceChanged()
		}
	}()
	s.patchMu.Lock()
	defer s.patchMu.Unlock()
	if retry, _ := ctx.Value(mobileBriefAudioRetryContextKey{}).(bool); retry {
		latest, found := s.store.latest()
		if !found || mobileBriefAudioVersion(latest) != version {
			return errMobileBriefNoAudio
		}
	}
	stored, found := s.store.load(briefID)
	if !found || stored.AsOf != pre.AsOf || s.app.briefPrivacyPrint() != preEpoch || stored.Audio == nil || stored.Audio.SHA256 != pre.Audio.SHA256 {
		return errMobileBriefNoAudio
	}
	shaped := *stored.Audio
	shaped.Attachment = attachment
	stored.Audio = &shaped
	document, err := marshalStoredMobileBrief(stored)
	if err != nil {
		return err
	}
	// Updating a historical recording must not move the latest brief pointer.
	err = func() error {
		s.store.mu.Lock()
		defer s.store.mu.Unlock()
		return writeMobileBriefFileAtomic(s.store.dir, briefID+".json", document)
	}()
	written = err == nil
	return err
}
