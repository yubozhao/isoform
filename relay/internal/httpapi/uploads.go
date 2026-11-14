package httpapi

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

// Uploads (PROTOCOL.md section 24): a phone puts a sealed recording here with
// its device bearer, and its hub fetches and then deletes it with the hub
// credential. The relay never opens the bytes; the key travels to the hub
// inside the sealed POST /recordings request.

const (
	// uploadScope is the device scope a recording upload needs. A recording
	// becomes something the user said to the computer, so it rides on the
	// scope that already lets the phone write chat messages.
	uploadScope = "chat:write"
	// uploadsPerDevice bounds one device's uploads per uploadWindow.
	uploadsPerDevice = 60
	uploadWindow     = time.Hour
)

// uploadRoute serves PUT /v1/uploads/{id} for a device.
func (h *Handler) uploadRoute(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/uploads/")
	if !wire.ValidUUID(id) {
		reply(w, http.StatusNotFound, nil, "Route not found")
		return
	}
	if r.Method != http.MethodPut {
		reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
		return
	}
	device, ok := h.authDevice(w, r, uploadScope)
	if !ok {
		return
	}
	if allowed, retry := h.limits.Allow("uploads:device:"+device.ID, uploadsPerDevice, uploadWindow, h.Now()); !allowed {
		setRetryAfter(w, retry)
		reply(w, http.StatusTooManyRequests, nil, "Too many uploads")
		return
	}
	if media := r.Header.Get("Content-Type"); media != "application/octet-stream" {
		reply(w, http.StatusUnsupportedMediaType, nil, "Content-Type must be application/octet-stream")
		return
	}
	if r.ContentLength > store.MaxUploadBytes {
		reply(w, http.StatusRequestEntityTooLarge, nil, "Upload exceeds 16 MiB")
		return
	}
	ctx, cancel := storeContext(r.Context())
	defer cancel()
	body := http.MaxBytesReader(w, r.Body, store.MaxUploadBytes+1)
	switch err := h.Store.PutUpload(ctx, device, id, body, h.Now()); {
	case err == nil:
	case errors.Is(err, store.ErrTooLarge):
		reply(w, http.StatusRequestEntityTooLarge, nil, "Upload is empty or exceeds 16 MiB")
		return
	case errors.Is(err, store.ErrQuotaFull):
		setRetryAfter(w, 10*time.Minute)
		reply(w, http.StatusTooManyRequests, nil, "Too many recordings are waiting for the computer")
		return
	case errors.Is(err, store.ErrStorageFull):
		reply(w, http.StatusInsufficientStorage, nil, "The relay cannot store more uploads right now")
		return
	case errors.Is(err, store.ErrConflict):
		reply(w, http.StatusConflict, nil, "Upload id is in use")
		return
	default:
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reply(w, http.StatusRequestEntityTooLarge, nil, "Upload exceeds 16 MiB")
			return
		}
		reply(w, http.StatusInternalServerError, nil, "Unable to store the upload")
		return
	}
	reply(w, http.StatusCreated, map[string]any{"id": id}, "")
}

// hubUploadRoute serves GET and DELETE /v1/hub/uploads/{id} for the hub the
// upload was sent to.
func (h *Handler) hubUploadRoute(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/hub/uploads/")
	if !wire.ValidUUID(id) {
		reply(w, http.StatusNotFound, nil, "Route not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
		return
	}
	if h.authLocked(w, r) {
		return
	}
	hubID := r.Header.Get("X-Yansu-Hub")
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !wire.ValidUUID(hubID) {
		h.authFailed(w, r, "Hub authentication required")
		return
	}
	ctx, cancel := storeContext(r.Context())
	defer cancel()
	switch err := h.Store.AuthorizeExistingHub(ctx, hubID, wire.Digest(token)); {
	case err == nil:
	case errors.Is(err, store.ErrUnauthorized):
		h.authFailed(w, r, "Hub authentication required")
		return
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to authenticate hub")
		return
	}
	if r.Method == http.MethodDelete {
		if err := h.Store.DeleteUpload(ctx, hubID, id); err != nil {
			reply(w, http.StatusInternalServerError, nil, "Unable to delete the upload")
			return
		}
		reply(w, http.StatusOK, map[string]any{"deleted": true}, "")
		return
	}
	path, _, err := h.Store.GetUpload(ctx, hubID, id, h.Now())
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		reply(w, http.StatusNotFound, nil, "Upload not found")
		return
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to read the upload")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		reply(w, http.StatusNotFound, nil, "Upload not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		reply(w, http.StatusInternalServerError, nil, "Unable to read the upload")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", info.ModTime(), file)
}
