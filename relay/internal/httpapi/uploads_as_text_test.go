package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"isoform-relay/internal/store"
)

// putTextPiece sends one piece as base64 text (PROTOCOL.md 24.1b).
func (r *relay) putTextPiece(token, id string, offset, total, size int, text []byte) (response, anyOrderProgress) {
	r.t.Helper()
	header := bearer(token)
	header.Set("Content-Type", "text/plain")
	resp := r.call(http.MethodPut, fmt.Sprintf("/v1/uploads/%s/pieces?offset=%d&total=%d&size=%d", id, offset, total, size), header, text)
	var progress anyOrderProgress
	_ = json.Unmarshal(resp.Data, &progress)
	return resp, progress
}

func asText(raw []byte) []byte { return []byte(base64.StdEncoding.EncodeToString(raw)) }

// On 2026-11-21 the tunnel in front of the relay refused the same pieces every
// time, by what their first bytes happened to be. A piece sent as base64 text
// is stored as the bytes it stands for: the hub fetches the same upload, byte
// for byte and with the same SHA-256, as from pieces sent as bytes.
func TestPiecesSentAsTextAreStoredAsTheirBytes(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	const size = 1000
	body := randomBytes(t, 3500)
	want := sha256.Sum256(body)
	pieceAt := func(index int) []byte {
		end := min((index+1)*size, len(body))
		return body[index*size : end]
	}

	// All as text, in an order of the system's liking.
	id := uuid.NewString()
	for _, index := range []int{3, 0, 2} {
		resp, _ := r.putTextPiece(dev.bearer, id, index*size, len(body), size, asText(pieceAt(index)))
		r.expectStatus(resp, http.StatusOK, fmt.Sprintf("text piece %d", index))
	}
	resp, progress := r.putTextPiece(dev.bearer, id, size, len(body), size, asText(pieceAt(1)))
	r.expectStatus(resp, http.StatusCreated, "the last text piece")
	if !progress.Complete || progress.Held != int64(len(body)) {
		t.Fatalf("after the last text piece: %+v", progress)
	}
	status, got := r.hubUpload(http.MethodGet, hubID, credential, id)
	if status != http.StatusOK || !bytes.Equal(got, body) || sha256.Sum256(got) != want {
		t.Fatalf("pieces sent as text: status %d, %d bytes, same bytes %v", status, len(got), bytes.Equal(got, body))
	}

	// One upload may mix the two: a piece refused as bytes goes again as text.
	mixed := uuid.NewString()
	for index := 0; index < 4; index++ {
		if index%2 == 0 {
			resp, _ = r.putSizedPiece(dev.bearer, mixed, index*size, len(body), size, pieceAt(index))
		} else {
			resp, _ = r.putTextPiece(dev.bearer, mixed, index*size, len(body), size, asText(pieceAt(index)))
		}
		if resp.Status != http.StatusOK && resp.Status != http.StatusCreated {
			t.Fatalf("mixed piece %d: status %d (%s)", index, resp.Status, resp.Error)
		}
	}
	status, got = r.hubUpload(http.MethodGet, hubID, credential, mixed)
	if status != http.StatusOK || sha256.Sum256(got) != want {
		t.Fatalf("pieces sent both ways: status %d, %d bytes", status, len(got))
	}

	// The whole-body upload takes text the same way.
	whole := uuid.NewString()
	header := bearer(dev.bearer)
	header.Set("Content-Type", "text/plain")
	resp = r.call(http.MethodPut, "/v1/uploads/"+whole, header, asText(body))
	r.expectStatus(resp, http.StatusCreated, "a whole upload as text")
	status, got = r.hubUpload(http.MethodGet, hubID, credential, whole)
	if status != http.StatusOK || sha256.Sum256(got) != want {
		t.Fatalf("a whole upload as text: status %d, %d bytes", status, len(got))
	}
}

// Text that is not base64, and text that stands for the wrong number of bytes,
// is refused and nothing of it is kept.
func TestTextThatIsNotAPieceIsRefused(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	const size = 1000
	body := randomBytes(t, 2000)
	id := uuid.NewString()

	resp, _ := r.putTextPiece(dev.bearer, id, 0, len(body), size, []byte("this is not a piece!"))
	r.expectStatus(resp, http.StatusBadRequest, "not base64")
	resp, _ = r.putTextPiece(dev.bearer, id, 0, len(body), size, asText(body[:400]))
	r.expectStatus(resp, http.StatusRequestEntityTooLarge, "text for a short piece that is not the last")
	if get := r.call(http.MethodGet, "/v1/uploads/"+id+"/pieces", bearer(dev.bearer), nil); get.Status != http.StatusNotFound {
		t.Fatalf("a refused piece must start nothing, status %d", get.Status)
	}
	header := bearer(dev.bearer)
	header.Set("Content-Type", "text/html")
	resp = r.call(http.MethodPut, fmt.Sprintf("/v1/uploads/%s/pieces?offset=0&total=%d&size=%d", id, len(body), size), header, asText(body[:size]))
	r.expectStatus(resp, http.StatusUnsupportedMediaType, "another content type")

	// A piece of the largest size fits as text; text for more than that does not.
	big := randomBytes(t, store.MaxUploadPieceBytes+1)
	large := uuid.NewString()
	resp, _ = r.putTextPiece(dev.bearer, large, 0, 2*store.MaxUploadPieceBytes, store.MaxUploadPieceBytes, asText(big[:store.MaxUploadPieceBytes]))
	r.expectStatus(resp, http.StatusOK, "the largest piece as text")
	resp, _ = r.putTextPiece(dev.bearer, uuid.NewString(), 0, 2*store.MaxUploadPieceBytes+8, store.MaxUploadPieceBytes+4, asText(append(big, 1, 2, 3)))
	r.expectStatus(resp, http.StatusRequestEntityTooLarge, "text for more than the largest piece")

	header.Set("Content-Type", "text/plain")
	resp = r.call(http.MethodPut, "/v1/uploads/"+uuid.NewString(), header, []byte("this is not an upload!"))
	r.expectStatus(resp, http.StatusBadRequest, "a whole upload that is not base64")
}

// The relay says it takes uploads as text, so a phone knows before it sends.
func TestHealthSaysUploadsAsText(t *testing.T) {
	r := newRelay(t, "")
	resp := r.call(http.MethodGet, "/v1/health", nil, nil)
	var health struct {
		UploadsAsText bool `json:"uploadsAsText"`
	}
	if err := json.Unmarshal(resp.Data, &health); err != nil || !health.UploadsAsText {
		t.Fatalf("health must say uploadsAsText: %s (%v)", resp.Data, err)
	}
}

// The computer's attachment for the phone goes through the same tunnel: as
// base64 text it is stored as its bytes, and the phone downloads those.
func TestAnAttachmentSentAsTextIsStoredAsItsBytes(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"brief:read"})
	body := randomBytes(t, 5000)
	id := uuid.NewString()

	resp := r.putAttachment(hubID, credential, id, asText(body), "text/plain")
	r.expectStatus(resp, http.StatusCreated, "an attachment as text")
	status, _, got := r.getRaw("/v1/attachments/"+id, dev.bearer)
	if status != http.StatusOK || !bytes.Equal(got, body) || sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatalf("an attachment sent as text: status %d, %d bytes", status, len(got))
	}
	resp = r.putAttachment(hubID, credential, uuid.NewString(), []byte("this is not an attachment!"), "text/plain")
	r.expectStatus(resp, http.StatusBadRequest, "not base64")
	resp = r.putAttachment(hubID, credential, uuid.NewString(), asText(body), "text/html")
	r.expectStatus(resp, http.StatusUnsupportedMediaType, "another content type")

	health := r.call(http.MethodGet, "/v1/health", nil, nil)
	var says struct {
		AttachmentsAsText bool `json:"attachmentsAsText"`
	}
	if err := json.Unmarshal(health.Data, &says); err != nil || !says.AttachmentsAsText {
		t.Fatalf("health must say attachmentsAsText: %s", health.Data)
	}
}
