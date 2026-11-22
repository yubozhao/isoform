package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/store"
)

type anyOrderProgress struct {
	Held     int64  `json:"held"`
	Total    int64  `json:"total"`
	Complete bool   `json:"complete"`
	Size     int64  `json:"size"`
	Have     string `json:"have"`
}

// putSizedPiece sends one piece of an upload cut into pieces of size bytes.
func (r *relay) putSizedPiece(token, id string, offset, total, size int, piece []byte) (response, anyOrderProgress) {
	r.t.Helper()
	header := bearer(token)
	header.Set("Content-Type", octetStream)
	resp := r.call(http.MethodPut, fmt.Sprintf("/v1/uploads/%s/pieces?offset=%d&total=%d&size=%d", id, offset, total, size), header, piece)
	var progress anyOrderProgress
	_ = json.Unmarshal(resp.Data, &progress)
	return resp, progress
}

// A phone whose app is closed hands all pieces to the system, which sends them
// in the order it likes and retries each by itself. The relay takes them at
// any place and finishes the upload when it holds them all.
func TestUploadPiecesArriveInAnyOrderAndRepeat(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	other := r.pairDevice(hc, []string{"chat:write"})
	id := uuid.NewString()
	const size = 1000
	body := randomBytes(t, 3500)
	pieceAt := func(index int) []byte {
		end := (index + 1) * size
		if end > len(body) {
			end = len(body)
		}
		return body[index*size : end]
	}

	// The last piece arrives first and starts the upload.
	resp, progress := r.putSizedPiece(dev.bearer, id, 3000, len(body), size, pieceAt(3))
	r.expectStatus(resp, http.StatusOK, "last piece first")
	if progress.Have != "0001" || progress.Held != 500 || progress.Size != size || progress.Complete {
		t.Fatalf("after the last piece: %+v", progress)
	}
	if status, _ := r.hubUpload(http.MethodGet, hubID, credential, id); status != http.StatusNotFound {
		t.Fatalf("an unfinished upload must be hidden from the hub, status %d", status)
	}
	resp, progress = r.putSizedPiece(dev.bearer, id, 1000, len(body), size, pieceAt(1))
	r.expectStatus(resp, http.StatusOK, "second piece")
	// The system sends a piece twice: it is answered as stored and held does not grow.
	resp, progress = r.putSizedPiece(dev.bearer, id, 1000, len(body), size, pieceAt(1))
	r.expectStatus(resp, http.StatusOK, "repeat")
	if progress.Have != "0101" || progress.Held != 1500 {
		t.Fatalf("after a repeat: %+v", progress)
	}
	// Progress says which pieces are missing.
	get := r.call(http.MethodGet, "/v1/uploads/"+id+"/pieces", bearer(dev.bearer), nil)
	var asked anyOrderProgress
	_ = json.Unmarshal(get.Data, &asked)
	if get.Status != http.StatusOK || asked.Have != "0101" || asked.Size != size {
		t.Fatalf("progress: %d %+v", get.Status, asked)
	}
	// Pieces that do not fit the cut are refused and change nothing.
	resp, _ = r.putSizedPiece(dev.bearer, id, 500, len(body), size, body[500:1500])
	r.expectStatus(resp, http.StatusRequestEntityTooLarge, "not on a piece boundary")
	resp, _ = r.putSizedPiece(dev.bearer, id, 0, len(body), size, body[:400])
	r.expectStatus(resp, http.StatusRequestEntityTooLarge, "a short piece that is not the last")
	resp, _ = r.putSizedPiece(dev.bearer, id, 3000, len(body), size, randomBytes(t, 600))
	r.expectStatus(resp, http.StatusRequestEntityTooLarge, "past the end")
	// Another piece size is answered with what the relay has.
	resp, progress = r.putSizedPiece(dev.bearer, id, 0, len(body), 700, body[:700])
	r.expectStatus(resp, http.StatusConflict, "another piece size")
	if progress.Size != size || progress.Have != "0101" {
		t.Fatalf("another piece size must report the relay's: %+v", progress)
	}
	// Another phone cannot add to it.
	resp, _ = r.putSizedPiece(other.bearer, id, 0, len(body), size, pieceAt(0))
	r.expectStatus(resp, http.StatusConflict, "another device")
	// A piece sent the in-order way lands in its place when it fits the cut.
	resp, _ = r.putPiece(dev.bearer, id, 2000, len(body), pieceAt(2))
	r.expectStatus(resp, http.StatusOK, "a piece without size on an any-order upload")

	resp, progress = r.putSizedPiece(dev.bearer, id, 0, len(body), size, pieceAt(0))
	r.expectStatus(resp, http.StatusCreated, "the missing piece")
	if !progress.Complete || progress.Held != int64(len(body)) {
		t.Fatalf("after the missing piece: %+v", progress)
	}
	// Late repeats after the end say it is complete.
	resp, progress = r.putSizedPiece(dev.bearer, id, 1000, len(body), size, pieceAt(1))
	r.expectStatus(resp, http.StatusCreated, "repeat after the end")
	if !progress.Complete {
		t.Fatalf("repeat after the end: %+v", progress)
	}
	status, raw := r.hubUpload(http.MethodGet, hubID, credential, id)
	if status != http.StatusOK || !bytes.Equal(raw, body) {
		t.Fatalf("hub get: status %d, %d bytes, equal %v", status, len(raw), bytes.Equal(raw, body))
	}
	if _, err := os.Stat(filepath.Join(r.dir, "upload-pieces", hubID, id)); !os.IsNotExist(err) {
		t.Fatalf("the arriving copy must be gone: %v", err)
	}
}

// An upload begun in order (the app was open) goes on in any order (the app
// was closed): whole pieces already held count, a partial one is sent again.
func TestAnUploadBegunInOrderContinuesInAnyOrder(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	id := uuid.NewString()
	const size = 1000
	body := randomBytes(t, 3200)

	resp, _ := r.putPiece(dev.bearer, id, 0, len(body), body[:1400])
	r.expectStatus(resp, http.StatusOK, "in order, 1400 bytes")
	resp, progress := r.putSizedPiece(dev.bearer, id, 3000, len(body), size, body[3000:])
	r.expectStatus(resp, http.StatusOK, "any order, last piece")
	if progress.Have != "1001" || progress.Held != 1200 {
		t.Fatalf("after the change: %+v", progress)
	}
	resp, _ = r.putSizedPiece(dev.bearer, id, 2000, len(body), size, body[2000:3000])
	r.expectStatus(resp, http.StatusOK, "third piece")
	resp, progress = r.putSizedPiece(dev.bearer, id, 1000, len(body), size, body[1000:2000])
	r.expectStatus(resp, http.StatusCreated, "second piece ends it")
	if status, raw := r.hubUpload(http.MethodGet, hubID, credential, id); status != http.StatusOK || !bytes.Equal(raw, body) {
		t.Fatalf("hub get: %d equal %v", status, bytes.Equal(raw, body))
	}
}

func TestAnyOrderUploadsReserveExpireAndStartAgain(t *testing.T) {
	old := store.MaxUploadBytesPerDevice
	store.MaxUploadBytesPerDevice = 100
	t.Cleanup(func() { store.MaxUploadBytesPerDevice = old })
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	id := uuid.NewString()
	body := randomBytes(t, 60)

	resp, _ := r.putSizedPiece(dev.bearer, id, 40, 60, 20, body[40:])
	r.expectStatus(resp, http.StatusOK, "a piece starts the upload")
	resp, _ = r.putSizedPiece(dev.bearer, uuid.NewString(), 20, 60, 20, body[20:40])
	r.expectStatus(resp, http.StatusTooManyRequests, "a second upload over the quota")
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("a full quota must say when to retry")
	}
	resp, _ = r.putSizedPiece(dev.bearer, uuid.NewString(), 0, 5000, 1, body[:1])
	r.expectStatus(resp, http.StatusRequestEntityTooLarge, "too many pieces")

	r.advance(store.UploadTTL + time.Minute)
	if _, err := r.store.Sweep(context.Background(), r.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "upload-pieces", hubID, id)); !os.IsNotExist(err) {
		t.Fatal("the expired arriving body is still on disk")
	}
	// The same id starts again; what was held before does not count.
	resp, progress := r.putSizedPiece(dev.bearer, id, 0, 60, 20, body[:20])
	r.expectStatus(resp, http.StatusOK, "start again")
	if progress.Have != "100" {
		t.Fatalf("start again: %+v", progress)
	}
	resp, _ = r.putSizedPiece(dev.bearer, id, 20, 60, 20, body[20:40])
	r.expectStatus(resp, http.StatusOK, "second")
	resp, _ = r.putSizedPiece(dev.bearer, id, 40, 60, 20, body[40:])
	r.expectStatus(resp, http.StatusCreated, "third")
	if status, raw := r.hubUpload(http.MethodGet, hubID, credential, id); status != http.StatusOK || !bytes.Equal(raw, body) {
		t.Fatalf("hub get: %d", status)
	}
}

// On 2026-11-19 a phone sent four parts at once, 44 pieces, beside its own
// requests. Pieces counted against the 60 requests a minute every route shared,
// so within seconds the relay refused the remaining pieces and the phone's
// briefs with them. Pieces have their own budget, and leave the shared one whole.
func TestUploadPiecesDoNotUseUpTheDeviceRequestBudget(t *testing.T) {
	r := newRelay(t, "")
	dev := r.pairDevice(r.connectHub(uuid.NewString(), randomToken(t)), append([]string{"chat:write"}, allScopes...))
	const size = 1000
	// Ten parts of eleven pieces each: 110 piece requests inside one minute.
	for range 10 {
		id := uuid.NewString()
		body := randomBytes(t, 11*size)
		var last anyOrderProgress
		for index := range 11 {
			resp, progress := r.putSizedPiece(dev.bearer, id, index*size, len(body), size, body[index*size:(index+1)*size])
			want := http.StatusOK
			if index == 10 {
				want = http.StatusCreated
			}
			r.expectStatus(resp, want, "a piece past the request budget of the minute")
			last = progress
		}
		if !last.Complete {
			t.Fatalf("part of eleven pieces did not finish: %+v", last)
		}
	}
	// The device's other routes still have their whole minute.
	for range deviceRequestsPerMinute {
		r.expectStatus(r.getWith("/v1/presence", dev.bearer), http.StatusOK, "the app's own request while pieces go")
	}
	r.expectStatus(r.getWith("/v1/presence", dev.bearer), http.StatusTooManyRequests, "one request past the minute's budget")
}

// On 2026-11-20 at noon a phone on a slow link sent pieces of three parts. Eight
// were answered 500 and the phone stopped those parts, which cut fourteen more
// pieces off. The store's five seconds had started before the piece was read, so
// a piece that took longer to arrive was refused although it arrived whole.
func TestAPieceThatArrivesSlowlyIsStored(t *testing.T) {
	old := storeCallTimeout
	storeCallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { storeCallTimeout = old })
	r := newRelay(t, "")
	dev := r.pairDevice(r.connectHub(uuid.NewString(), randomToken(t)), append([]string{"chat:write"}, allScopes...))
	id := uuid.NewString()
	piece := randomBytes(t, 1000)

	// The piece takes three times the store's limit to arrive.
	reader, writer := io.Pipe()
	go func() {
		_, _ = writer.Write(piece[:500])
		time.Sleep(3 * storeCallTimeout)
		_, _ = writer.Write(piece[500:])
		_ = writer.Close()
	}()
	req, err := http.NewRequest(http.MethodPut, r.srv.URL+fmt.Sprintf("/v1/uploads/%s/pieces?offset=0&total=%d&size=%d", id, len(piece), len(piece)), reader)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = int64(len(piece))
	req.Header = bearer(dev.bearer)
	req.Header.Set("Content-Type", octetStream)
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("a piece that arrived whole, slowly: got %d, want 201", resp.StatusCode)
	}
}
