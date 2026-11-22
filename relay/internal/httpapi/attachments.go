package httpapi

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"isoform-relay/internal/store"
	"isoform-relay/internal/wire"
)

// Attachments (PROTOCOL.md section 17): the hub uploads a sealed file for its
// phones with its hub credential, and a phone downloads it with its device
// bearer. The relay stores and serves the bytes as given. An upload is accepted
// only from a hub that already exists and has a live device that reads briefs,
// within a per-hub and a per-address rate limit, streamed to disk under the
// size limit, so an unknown party cannot fill the relay's disk.

const (
	// attachmentUploadsPerHub bounds one hub's uploads per attachmentUploadWindow.
	attachmentUploadsPerHub = 12
	// attachmentUploadsPerAddress bounds uploads from one client address.
	attachmentUploadsPerAddress = 30
	attachmentUploadWindow      = time.Hour
)

// hubAttachmentRoute serves PUT /v1/hub/attachments/{id}.
func (h *Handler) hubAttachmentRoute(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/hub/attachments/")
	if !wire.ValidUUID(id) {
		reply(w, http.StatusNotFound, nil, "Route not found")
		return
	}
	if r.Method != http.MethodPut {
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
	now := h.Now()
	ctx, cancel := storeContext(r.Context())
	defer cancel()
	// The hub must already exist: this route never creates one.
	switch err := h.Store.AuthorizeExistingHub(ctx, hubID, wire.Digest(token)); {
	case err == nil:
	case errors.Is(err, store.ErrUnauthorized):
		h.authFailed(w, r, "Hub authentication required")
		return
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to authenticate hub")
		return
	}
	if !h.allow(w, r, "attachment_hub", hubID, attachmentUploadsPerHub, attachmentUploadWindow, now) {
		reply(w, http.StatusTooManyRequests, nil, "Too many uploads")
		return
	}
	if !h.allow(w, r, "attachment_address", clientIP(r), attachmentUploadsPerAddress, attachmentUploadWindow, now) {
		reply(w, http.StatusTooManyRequests, nil, "Too many uploads")
		return
	}
	readable, err := h.Store.HubCanReceiveAttachments(ctx, hubID, now)
	if err != nil {
		reply(w, http.StatusInternalServerError, nil, "Unable to check the hub's devices")
		return
	}
	if !readable {
		reply(w, http.StatusForbidden, nil, "No paired device can read briefs")
		return
	}
	// As bytes, or as base64 text: a tunnel in front of the relay may refuse sealed
	// bytes by what they happen to be (PROTOCOL.md 24.1b, 17.2).
	asText, ok := uploadBodyAsText(r)
	if !ok {
		reply(w, http.StatusUnsupportedMediaType, nil, "Content-Type must be application/octet-stream or text/plain")
		return
	}
	if r.ContentLength > onTheWire(store.MaxAttachmentBytes, asText) {
		reply(w, http.StatusRequestEntityTooLarge, nil, "Attachment exceeds 16 MiB")
		return
	}
	// The body streams to disk; one byte over the limit ends the read.
	var body io.Reader = http.MaxBytesReader(w, r.Body, onTheWire(store.MaxAttachmentBytes+1, asText))
	if asText {
		body = base64.NewDecoder(base64.StdEncoding, body)
	}
	var notText base64.CorruptInputError
	switch err = h.Store.PutAttachment(ctx, hubID, id, body, now); {
	case err == nil:
	case errors.As(err, &notText):
		reply(w, http.StatusBadRequest, nil, "The attachment is not base64 text")
		return
	case errors.Is(err, store.ErrTooLarge):
		reply(w, http.StatusRequestEntityTooLarge, nil, "Attachment is empty or exceeds 16 MiB")
		return
	case errors.Is(err, store.ErrStorageFull):
		reply(w, http.StatusInsufficientStorage, nil, "The relay cannot store more attachments right now")
		return
	case errors.Is(err, store.ErrConflict):
		reply(w, http.StatusConflict, nil, "Attachment id is in use")
		return
	case errors.Is(err, store.ErrNoReader):
		reply(w, http.StatusForbidden, nil, "No paired device can read briefs")
		return
	case errors.Is(err, store.ErrNotFound):
		reply(w, http.StatusUnauthorized, nil, "Hub authentication required")
		return
	default:
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reply(w, http.StatusRequestEntityTooLarge, nil, "Attachment exceeds 16 MiB")
			return
		}
		reply(w, http.StatusInternalServerError, nil, "Unable to store the attachment")
		return
	}
	reply(w, http.StatusCreated, map[string]any{"id": id}, "")
}

// attachmentRoute serves GET /v1/attachments/{id} for a device of the owning hub.
func (h *Handler) attachmentRoute(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/attachments/")
	if !wire.ValidUUID(id) {
		reply(w, http.StatusNotFound, nil, "Route not found")
		return
	}
	if r.Method != http.MethodGet {
		reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
		return
	}
	device, ok := h.authDevice(w, r, "brief:read")
	if !ok {
		return
	}
	ctx, cancel := storeContext(r.Context())
	defer cancel()
	path, _, err := h.Store.GetAttachment(ctx, device.HubID, id, h.Now())
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		reply(w, http.StatusNotFound, nil, "Attachment not found")
		return
	default:
		reply(w, http.StatusInternalServerError, nil, "Unable to read the attachment")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		reply(w, http.StatusNotFound, nil, "Attachment not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		reply(w, http.StatusInternalServerError, nil, "Unable to read the attachment")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// ServeContent streams the file and honours range requests.
	http.ServeContent(w, r, "", info.ModTime(), file)
}
