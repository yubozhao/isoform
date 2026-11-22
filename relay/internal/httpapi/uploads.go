package httpapi

import (
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
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
	// uploadRequestsPerMinute is the upload routes' own budget for one device,
	// apart from deviceRequestsPerMinute. A 15-minute part is about eleven
	// 256 KiB pieces and a phone sends several parts at once; what the relay
	// keeps is bounded by the byte quotas in the store, not by this count.
	uploadRequestsPerMinute = 1200
	// uploadsPerDevice bounds one device's uploads per uploadWindow. A phone
	// that was away for days has a hundred parts or more waiting; at 60 an
	// hour they took two hours however fast the path was.
	uploadsPerDevice = 600
	uploadWindow     = time.Hour
	// uploadPiecesPerDevice bounds one device's pieces per uploadWindow. The
	// first piece of an upload also counts against uploadsPerDevice.
	uploadPiecesPerDevice = 16000
	// bodyAsBytes is an upload body as its bytes; bodyAsText is the same bytes
	// as base64 text (PROTOCOL.md 24.1b).
	bodyAsBytes = "application/octet-stream"
	bodyAsText  = "text/plain"
)

// On 2026-11-21 the tunnel in front of the relay refused about one piece in
// forty with a 403 of its own: its firewall reads the first 8 KB of a body, and
// sealed audio is random bytes that now and then look like an attack to it. The
// same piece was refused every time, so its part never arrived. A body sent as
// base64 text holds letters, digits, "+", "/" and "=" only, which no such rule
// matches. The relay stores the decoded bytes; nothing after this differs.

// uploadBodyAsText says whether the body is base64 text, and false for ok when
// the content type is neither of the two an upload may have.
func uploadBodyAsText(r *http.Request) (asText, ok bool) {
	switch r.Header.Get("Content-Type") {
	case bodyAsBytes:
		return false, true
	case bodyAsText:
		return true, true
	default:
		return false, false
	}
}

// onTheWire is the most bytes a body of at most limit bytes takes as sent.
func onTheWire(limit int64, asText bool) int64 {
	if asText {
		return int64(base64.StdEncoding.EncodedLen(int(limit)))
	}
	return limit
}

// uploadRoute serves PUT /v1/uploads/{id} for a device.
func (h *Handler) uploadRoute(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/uploads/")
	if rest, ok := strings.CutSuffix(id, "/pieces"); ok {
		h.uploadPiecesRoute(w, r, rest)
		return
	}
	if !wire.ValidUUID(id) {
		reply(w, http.StatusNotFound, nil, "Route not found")
		return
	}
	if r.Method != http.MethodPut {
		reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
		return
	}
	device, ok := h.authDeviceWithin(w, r, uploadScope, "upload_rate", uploadRequestsPerMinute)
	if !ok {
		return
	}
	if !h.allow(w, r, "uploads", device.ID, uploadsPerDevice, uploadWindow, h.Now()) {
		reply(w, http.StatusTooManyRequests, nil, "Too many uploads")
		return
	}
	asText, ok := uploadBodyAsText(r)
	if !ok {
		reply(w, http.StatusUnsupportedMediaType, nil, "Content-Type must be application/octet-stream or text/plain")
		return
	}
	if r.ContentLength > onTheWire(store.MaxUploadBytes, asText) {
		reply(w, http.StatusRequestEntityTooLarge, nil, "Upload exceeds 16 MiB")
		return
	}
	ctx, cancel := storeContext(r.Context())
	defer cancel()
	// One byte, or one group of text, past the limit is let in so the store sees
	// an upload that is too long and refuses it.
	var body io.Reader = http.MaxBytesReader(w, r.Body, onTheWire(store.MaxUploadBytes+1, asText))
	if asText {
		body = base64.NewDecoder(base64.StdEncoding, body)
	}
	var notText base64.CorruptInputError
	switch err := h.Store.PutUpload(ctx, device, id, body, h.Now()); {
	case err == nil:
	case errors.As(err, &notText):
		reply(w, http.StatusBadRequest, nil, "The upload is not base64 text")
		return
	case errors.Is(err, store.ErrTooLarge):
		reply(w, http.StatusRequestEntityTooLarge, nil, "Upload is empty or exceeds 16 MiB")
		return
	case errors.Is(err, store.ErrQuotaFull):
		setRetryAfter(w, 10*time.Minute)
		h.logLimitError(w, r, err)
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

// uploadPiecesRoute serves an upload sent in pieces (PROTOCOL.md 24.1a):
// PUT /v1/uploads/{id}/pieces?offset=N&total=T appends a piece, and
// GET /v1/uploads/{id}/pieces says how much the relay holds.
func (h *Handler) uploadPiecesRoute(w http.ResponseWriter, r *http.Request, id string) {
	if !wire.ValidUUID(id) {
		reply(w, http.StatusNotFound, nil, "Route not found")
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodGet {
		reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
		return
	}
	device, ok := h.authDeviceWithin(w, r, uploadScope, "upload_rate", uploadRequestsPerMinute)
	if !ok {
		return
	}
	ctx, cancel := storeContext(r.Context())
	defer cancel()
	if r.Method == http.MethodGet {
		progress, err := h.Store.UploadProgress(ctx, device, id, h.Now())
		switch {
		case err == nil:
			reply(w, http.StatusOK, uploadProgressData(id, progress), "")
		case errors.Is(err, store.ErrNotFound):
			reply(w, http.StatusNotFound, nil, "Upload not found")
		default:
			reply(w, http.StatusInternalServerError, nil, "Unable to read the upload")
		}
		return
	}
	query := r.URL.Query()
	offset, offsetErr := strconv.ParseInt(query.Get("offset"), 10, 64)
	total, totalErr := strconv.ParseInt(query.Get("total"), 10, 64)
	if offsetErr != nil || totalErr != nil || offset < 0 || total <= 0 {
		log.Print(refusedPieceLine("no usable offset and total", offset, total, 0, 0, r.ContentLength))
		reply(w, http.StatusBadRequest, nil, "offset and total are required")
		return
	}
	// With size the pieces may arrive in any order (PROTOCOL.md 24.1a).
	var size int64
	if text := query.Get("size"); text != "" {
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil || parsed <= 0 {
			log.Print(refusedPieceLine("no usable size", offset, total, parsed, 0, r.ContentLength))
			reply(w, http.StatusBadRequest, nil, "size must be a positive number")
			return
		}
		size = parsed
	}
	asText, ok := uploadBodyAsText(r)
	if !ok {
		reply(w, http.StatusUnsupportedMediaType, nil, "Content-Type must be application/octet-stream or text/plain")
		return
	}
	if total > store.MaxUploadBytes {
		reply(w, http.StatusRequestEntityTooLarge, nil, "Upload exceeds 16 MiB")
		return
	}
	if r.ContentLength > onTheWire(store.MaxUploadPieceBytes, asText) {
		reply(w, http.StatusRequestEntityTooLarge, nil, "Piece exceeds 1 MiB")
		return
	}
	if !h.allow(w, r, "upload_pieces", device.ID, uploadPiecesPerDevice, uploadWindow, h.Now()) {
		reply(w, http.StatusTooManyRequests, nil, "Too many uploads")
		return
	}
	// An upload counts once: at its first piece in order, or at whichever piece
	// arrives first when no upload under this id is known.
	first := offset == 0
	if size > 0 {
		_, err := h.Store.UploadProgress(ctx, device, id, h.Now())
		first = errors.Is(err, store.ErrNotFound)
	}
	if first {
		if !h.allow(w, r, "uploads", device.ID, uploadsPerDevice, uploadWindow, h.Now()) {
			reply(w, http.StatusTooManyRequests, nil, "Too many uploads")
			return
		}
	}
	piece, err := io.ReadAll(http.MaxBytesReader(w, r.Body, onTheWire(store.MaxUploadPieceBytes+1, asText)))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reply(w, http.StatusRequestEntityTooLarge, nil, "Piece exceeds 1 MiB")
			return
		}
		// The connection was cut mid-piece: nothing of it is kept.
		log.Print(refusedPieceLine("the piece did not arrive whole", offset, total, size, int64(len(piece)), r.ContentLength))
		reply(w, http.StatusBadRequest, nil, "The piece did not arrive whole")
		return
	}
	if asText {
		// The numbers in the line are of the text as it arrived, never the piece.
		arrived := int64(len(piece))
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(piece)))
		n, err := base64.StdEncoding.Decode(decoded, piece)
		if err != nil {
			log.Print(refusedPieceLine("the piece is not base64 text", offset, total, size, arrived, r.ContentLength))
			reply(w, http.StatusBadRequest, nil, "The piece is not base64 text")
			return
		}
		piece = decoded[:n]
	}
	// The store's time starts now. Until 2026-11-20 it started before the piece
	// was read: a piece that took more than five seconds to arrive, on a slow
	// link or beside other pieces, was refused with 500 although it had arrived
	// whole, and the phone then stopped the whole part.
	ctx, cancel = storeContext(r.Context())
	defer cancel()
	var progress store.UploadProgress
	if size > 0 {
		progress, err = h.Store.PutUploadPieceAnyOrder(ctx, device, id, offset, total, size, piece, h.Now())
	} else {
		progress, err = h.Store.PutUploadPiece(ctx, device, id, offset, total, piece, h.Now())
	}
	switch {
	case err == nil:
		status := http.StatusOK
		if progress.Complete {
			status = http.StatusCreated
		}
		reply(w, status, uploadProgressData(id, progress), "")
	case errors.Is(err, store.ErrUploadOffset):
		reply(w, http.StatusConflict, uploadProgressData(id, progress), "The piece does not start where the upload ends")
	case errors.Is(err, store.ErrTooLarge):
		reply(w, http.StatusRequestEntityTooLarge, nil, "The piece is empty, too large or past the end of the upload")
	case errors.Is(err, store.ErrQuotaFull):
		setRetryAfter(w, 10*time.Minute)
		h.logLimitError(w, r, err)
		reply(w, http.StatusTooManyRequests, nil, "Too many recordings are waiting for the computer")
	case errors.Is(err, store.ErrStorageFull):
		reply(w, http.StatusInsufficientStorage, nil, "The relay cannot store more uploads right now")
	case errors.Is(err, store.ErrConflict):
		reply(w, http.StatusConflict, nil, "Upload id is in use")
	default:
		// The cause, never the piece.
		log.Print("piece not stored: " + failureCause(err))
		reply(w, http.StatusInternalServerError, nil, "Unable to store the piece")
	}
}

func uploadProgressData(id string, progress store.UploadProgress) map[string]any {
	data := map[string]any{"id": id, "held": progress.Held, "total": progress.Total, "complete": progress.Complete}
	if progress.Size > 0 {
		data["size"], data["have"] = progress.Size, progress.Have
	}
	return data
}

// listUploads serves GET /v1/uploads (PROTOCOL.md 24.1c): what the relay holds
// of this phone's own uploads, whole or still arriving. A phone with many parts
// waiting asks once and finishes the uploads it has begun before it begins
// more. On 2026-11-22 it could not know which those were: uploads half sent
// the evening before held every place, the phone began with other parts, and
// each was refused. The answer holds ids the phone made itself and byte
// counts; nothing is changed by asking.
func (h *Handler) listUploads(w http.ResponseWriter, r *http.Request) {
	device, ok := h.authDeviceWithin(w, r, uploadScope, "upload_rate", uploadRequestsPerMinute)
	if !ok {
		return
	}
	ctx, cancel := storeContext(r.Context())
	defer cancel()
	held, err := h.Store.DeviceUploads(ctx, device, h.Now())
	if err != nil {
		reply(w, http.StatusInternalServerError, nil, "Unable to read the uploads")
		return
	}
	uploads := make([]map[string]any, 0, len(held))
	for _, upload := range held {
		uploads = append(uploads, map[string]any{"id": upload.ID, "held": upload.Held, "total": upload.Total, "complete": upload.Complete})
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, http.StatusOK, map[string]any{"uploads": uploads}, "")
}
