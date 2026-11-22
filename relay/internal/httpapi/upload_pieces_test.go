package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/store"
)

type pieceProgress struct {
	Held     int64 `json:"held"`
	Total    int64 `json:"total"`
	Complete bool  `json:"complete"`
}

// putPiece sends one piece of an upload as a device.
func (r *relay) putPiece(token, id string, offset, total int, piece []byte) (response, pieceProgress) {
	r.t.Helper()
	header := bearer(token)
	header.Set("Content-Type", octetStream)
	resp := r.call(http.MethodPut, fmt.Sprintf("/v1/uploads/%s/pieces?offset=%d&total=%d", id, offset, total), header, piece)
	var progress pieceProgress
	_ = json.Unmarshal(resp.Data, &progress)
	return resp, progress
}

func (r *relay) pieceProgress(token, id string) (response, pieceProgress) {
	r.t.Helper()
	resp := r.call(http.MethodGet, "/v1/uploads/"+id+"/pieces", bearer(token), nil)
	var progress pieceProgress
	_ = json.Unmarshal(resp.Data, &progress)
	return resp, progress
}

// On 2026-11-18 a tunnel cut every 2.8 MB upload after some seconds and two
// days of recordings stayed on a phone. An upload sent in pieces loses only the
// piece in flight: the phone asks what the relay holds and goes on from there,
// and the hub sees the upload only when it is whole.
func TestUploadInPiecesResumesAndReachesTheHubWhole(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	id := uuid.NewString()
	body := randomBytes(t, 700*1024)
	const piece = 256 * 1024

	if resp, _ := r.pieceProgress(dev.bearer, id); resp.Status != http.StatusNotFound {
		t.Fatalf("progress of an unknown upload: status %d", resp.Status)
	}
	resp, progress := r.putPiece(dev.bearer, id, 0, len(body), body[:piece])
	r.expectStatus(resp, http.StatusOK, "first piece")
	if progress.Held != piece || progress.Complete {
		t.Fatalf("after the first piece: %+v", progress)
	}
	// The hub does not see an upload that is still arriving.
	if status, _ := r.hubUpload(http.MethodGet, hubID, credential, id); status != http.StatusNotFound {
		t.Fatalf("an unfinished upload must be hidden from the hub, status %d", status)
	}
	// The answer to the first piece was lost and the phone sends it again: the
	// relay says what it holds, and nothing is written twice.
	resp, progress = r.putPiece(dev.bearer, id, 0, len(body), body[:piece])
	r.expectStatus(resp, http.StatusConflict, "repeated piece")
	if progress.Held != piece {
		t.Fatalf("a repeat must report the held size: %+v", progress)
	}
	// A piece from further on is refused the same way.
	resp, _ = r.putPiece(dev.bearer, id, 2*piece, len(body), body[2*piece:])
	r.expectStatus(resp, http.StatusConflict, "piece with a gap")
	// The phone asks and continues.
	if resp, progress = r.pieceProgress(dev.bearer, id); resp.Status != http.StatusOK || progress.Held != piece || progress.Total != int64(len(body)) {
		t.Fatalf("progress: %d %+v", resp.Status, progress)
	}
	resp, _ = r.putPiece(dev.bearer, id, piece, len(body), body[piece:2*piece])
	r.expectStatus(resp, http.StatusOK, "second piece")
	resp, progress = r.putPiece(dev.bearer, id, 2*piece, len(body), body[2*piece:])
	r.expectStatus(resp, http.StatusCreated, "last piece")
	if !progress.Complete || progress.Held != int64(len(body)) {
		t.Fatalf("after the last piece: %+v", progress)
	}
	// The last answer was lost too: the repeat says it is complete.
	resp, progress = r.putPiece(dev.bearer, id, 2*piece, len(body), body[2*piece:])
	r.expectStatus(resp, http.StatusCreated, "repeat of the last piece")
	if !progress.Complete {
		t.Fatalf("repeat of the last piece: %+v", progress)
	}
	if resp, progress = r.pieceProgress(dev.bearer, id); resp.Status != http.StatusOK || !progress.Complete {
		t.Fatalf("progress of a finished upload: %d %+v", resp.Status, progress)
	}
	stored := filepath.Join(r.dir, "uploads", hubID, id)
	if info, err := os.Stat(stored); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("upload body on disk: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "upload-pieces", hubID, id)); !os.IsNotExist(err) {
		t.Fatalf("the arriving copy must be gone: %v", err)
	}
	status, raw := r.hubUpload(http.MethodGet, hubID, credential, id)
	if status != http.StatusOK || !bytes.Equal(raw, body) {
		t.Fatalf("hub get: status %d, %d bytes, equal %v", status, len(raw), bytes.Equal(raw, body))
	}
	if status, _ := r.hubUpload(http.MethodDelete, hubID, credential, id); status != http.StatusOK {
		t.Fatalf("hub delete status %d", status)
	}
}

func TestUploadPiecesStayWithTheirDeviceAndWithinBounds(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	other := r.pairDevice(hc, []string{"chat:write"})
	reader := r.pairDevice(hc, []string{"brief:read"})
	id := uuid.NewString()
	body := randomBytes(t, 3000)

	resp, _ := r.putPiece(reader.bearer, id, 0, len(body), body[:1000])
	r.expectStatus(resp, http.StatusForbidden, "without chat:write")
	resp, _ = r.putPiece(dev.bearer, id, 1000, len(body), body[1000:2000])
	r.expectStatus(resp, http.StatusConflict, "an upload cannot start in the middle")
	resp, _ = r.putPiece(dev.bearer, id, 0, len(body), body[:1000])
	r.expectStatus(resp, http.StatusOK, "first piece")
	// Another phone can neither add to it nor learn that it exists.
	resp, _ = r.putPiece(other.bearer, id, 1000, len(body), body[1000:2000])
	r.expectStatus(resp, http.StatusConflict, "another device's piece")
	if resp, _ = r.pieceProgress(other.bearer, id); resp.Status != http.StatusNotFound {
		t.Fatalf("another device's progress: status %d", resp.Status)
	}
	// A piece may not run past the declared size, be empty, or exceed 1 MiB.
	resp, _ = r.putPiece(dev.bearer, id, 1000, len(body), randomBytes(t, 2001))
	r.expectStatus(resp, http.StatusRequestEntityTooLarge, "past the end")
	resp, _ = r.putPiece(dev.bearer, uuid.NewString(), 0, 4<<20, randomBytes(t, store.MaxUploadPieceBytes+1))
	r.expectStatus(resp, http.StatusRequestEntityTooLarge, "piece over 1 MiB")
	resp, _ = r.putPiece(dev.bearer, uuid.NewString(), 0, store.MaxUploadBytes+1, body[:10])
	r.expectStatus(resp, http.StatusRequestEntityTooLarge, "upload over 16 MiB")
	// The refused pieces changed nothing.
	if _, progress := r.pieceProgress(dev.bearer, id); progress.Held != 1000 {
		t.Fatalf("held after refused pieces: %+v", progress)
	}
	// A whole-body upload under the same id replaces the arriving one.
	r.expectStatus(r.putUpload(dev.bearer, id, body), http.StatusCreated, "whole body over pieces")
	if status, raw := r.hubUpload(http.MethodGet, hubID, credential, id); status != http.StatusOK || !bytes.Equal(raw, body) {
		t.Fatalf("hub get after whole body: %d", status)
	}
}

// The first piece reserves the whole upload, so a started upload can finish and
// a second one that would not fit is refused before any of it is sent.
func TestUploadPiecesReserveTheirSizeAgainstTheQuota(t *testing.T) {
	old := store.MaxUploadBytesPerDevice
	store.MaxUploadBytesPerDevice = 100
	t.Cleanup(func() { store.MaxUploadBytesPerDevice = old })
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	first := uuid.NewString()
	body := randomBytes(t, 60)

	resp, _ := r.putPiece(dev.bearer, first, 0, 60, body[:20])
	r.expectStatus(resp, http.StatusOK, "first piece")
	expectLimitLog(t, "waiting_upload_bytes", "/v1/uploads/{id}/pieces", 60, 100, func() int {
		resp, _ = r.putPiece(dev.bearer, uuid.NewString(), 0, 60, body[:20])
		return resp.Status
	})
	r.expectStatus(resp, http.StatusTooManyRequests, "second upload over the quota")
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("a full quota must say when to retry")
	}
	r.expectStatus(r.putUpload(dev.bearer, uuid.NewString(), randomBytes(t, 60)), http.StatusTooManyRequests, "whole body over the quota")
	resp, _ = r.putPiece(dev.bearer, first, 20, 60, body[20:])
	r.expectStatus(resp, http.StatusCreated, "the started upload finishes")
	if status, raw := r.hubUpload(http.MethodGet, hubID, credential, first); status != http.StatusOK || !bytes.Equal(raw, body) {
		t.Fatalf("hub get: %d", status)
	}
}

// On 2026-11-22 a phone with 255 parts waiting was refused every new part:
// 48 uploads, most of them half sent the evening before, held every place.
// More than 48 begun uploads are taken, each can still finish, and the bytes
// stay the bound.
func TestMoreThanFortyEightBegunUploadsAreTakenAndTheBytesStillBound(t *testing.T) {
	old := store.MaxUploadBytesPerDevice
	store.MaxUploadBytesPerDevice = 60 * 30
	t.Cleanup(func() { store.MaxUploadBytesPerDevice = old })
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	body := randomBytes(t, 30)
	begun := make([]string, 60)
	for i := range begun {
		begun[i] = uuid.NewString()
		resp, _ := r.putPiece(dev.bearer, begun[i], 0, len(body), body[:10])
		r.expectStatus(resp, http.StatusOK, fmt.Sprintf("begun upload %d", i+1))
	}
	// The bytes are full: one more is refused, and nothing waiting is evicted.
	resp, _ := r.putPiece(dev.bearer, uuid.NewString(), 0, len(body), body[:10])
	r.expectStatus(resp, http.StatusTooManyRequests, "an upload past the bytes")
	// Every begun upload still finishes and reaches the hub whole.
	for i, id := range begun {
		resp, _ := r.putPiece(dev.bearer, id, 10, len(body), body[10:])
		r.expectStatus(resp, http.StatusCreated, fmt.Sprintf("begun upload %d finishes", i+1))
	}
	if status, raw := r.hubUpload(http.MethodGet, hubID, credential, begun[59]); status != http.StatusOK || !bytes.Equal(raw, body) {
		t.Fatalf("hub get of the sixtieth: %d", status)
	}
	if store.KeptUploads <= 48 {
		t.Fatalf("the count must not be the bound again: %d", store.KeptUploads)
	}
}

func TestUnfinishedUploadExpiresAndIsSwept(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	id := uuid.NewString()
	body := randomBytes(t, 90)
	resp, _ := r.putPiece(dev.bearer, id, 0, len(body), body[:30])
	r.expectStatus(resp, http.StatusOK, "first piece")
	arriving := filepath.Join(r.dir, "upload-pieces", hubID, id)
	if info, err := os.Stat(arriving); err != nil || info.Mode().Perm() != 0600 || info.Size() != 30 {
		t.Fatalf("arriving body on disk: %v %v", info, err)
	}
	r.advance(store.UploadTTL + time.Minute)
	if resp, _ := r.pieceProgress(dev.bearer, id); resp.Status != http.StatusNotFound {
		t.Fatalf("progress of an expired upload: status %d", resp.Status)
	}
	resp, _ = r.putPiece(dev.bearer, id, 30, len(body), body[30:60])
	r.expectStatus(resp, http.StatusConflict, "a piece for an expired upload")
	if _, err := r.store.Sweep(context.Background(), r.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(arriving); !os.IsNotExist(err) {
		t.Fatal("the expired arriving body is still on disk")
	}
	// The same id starts again from the beginning.
	resp, progress := r.putPiece(dev.bearer, id, 0, len(body), body)
	r.expectStatus(resp, http.StatusCreated, "start again")
	if !progress.Complete {
		t.Fatalf("start again: %+v", progress)
	}
}

// On 2026-11-22 a phone could not know which of its parts the relay held: it
// began with other parts and each was refused. It asks once and is told its
// own uploads, whole and arriving, and nothing of another phone's.
func TestAPhoneIsToldWhatTheRelayHoldsOfItsUploads(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	other := r.pairDevice(hc, []string{"chat:write"})
	noScope := r.pairDevice(hc, []string{"brief:read"})
	type listed struct {
		ID       string `json:"id"`
		Held     int64  `json:"held"`
		Total    int64  `json:"total"`
		Complete bool   `json:"complete"`
	}
	list := func(token string) (response, []listed) {
		resp := r.call(http.MethodGet, "/v1/uploads", bearer(token), nil)
		var data struct {
			Uploads []listed `json:"uploads"`
		}
		_ = json.Unmarshal(resp.Data, &data)
		return resp, data.Uploads
	}

	resp, uploads := list(dev.bearer)
	r.expectStatus(resp, http.StatusOK, "nothing yet")
	if uploads == nil || len(uploads) != 0 {
		t.Fatalf("an empty list is a list: %s", resp.Data)
	}
	body := randomBytes(t, 90)
	whole, half, theirs := uuid.NewString(), uuid.NewString(), uuid.NewString()
	r.expectStatus(r.putUpload(dev.bearer, whole, body), http.StatusCreated, "a whole upload")
	put, _ := r.putPiece(dev.bearer, half, 0, len(body), body[:30])
	r.expectStatus(put, http.StatusOK, "a begun upload")
	put, _ = r.putPiece(other.bearer, theirs, 0, len(body), body[:30])
	r.expectStatus(put, http.StatusOK, "another phone's upload")

	_, uploads = list(dev.bearer)
	want := []listed{{ID: whole, Held: 90, Total: 90, Complete: true}, {ID: half, Held: 30, Total: 90}}
	if len(uploads) != 2 || uploads[0] != want[0] || uploads[1] != want[1] {
		t.Fatalf("listed %+v, want %+v", uploads, want)
	}
	if _, uploads = list(other.bearer); len(uploads) != 1 || uploads[0].ID != theirs {
		t.Fatalf("the other phone sees its own only: %+v", uploads)
	}
	resp, _ = list(noScope.bearer)
	r.expectStatus(resp, http.StatusForbidden, "without the upload scope")
	r.expectStatus(r.call(http.MethodGet, "/v1/uploads", nil, nil), http.StatusUnauthorized, "without a credential")

	// Asking changes nothing: the begun upload finishes, and what the hub took is gone from the list.
	put, _ = r.putPiece(dev.bearer, half, 30, len(body), body[30:])
	r.expectStatus(put, http.StatusCreated, "the begun upload finishes")
	if status, _ := r.hubUpload(http.MethodDelete, hubID, credential, whole); status != http.StatusOK {
		t.Fatalf("hub delete: %d", status)
	}
	if _, uploads = list(dev.bearer); len(uploads) != 1 || uploads[0] != (listed{ID: half, Held: 90, Total: 90, Complete: true}) {
		t.Fatalf("after the hub took one: %+v", uploads)
	}
	// An upload past its time is not listed.
	r.advance(store.UploadTTL + time.Minute)
	if _, uploads = list(dev.bearer); len(uploads) != 0 {
		t.Fatalf("expired uploads must not be listed: %+v", uploads)
	}
	health := r.call(http.MethodGet, "/v1/health", nil, nil)
	if !bytes.Contains(health.Data, []byte(`"uploadList":true`)) {
		t.Fatalf("health must say uploadList: %s", health.Data)
	}
}
