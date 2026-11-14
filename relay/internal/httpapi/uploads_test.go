package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"isoform-relay/internal/store"
)

// putUpload uploads a body as a device.
func (r *relay) putUpload(token, id string, body []byte) response {
	r.t.Helper()
	header := bearer(token)
	header.Set("Content-Type", octetStream)
	return r.call(http.MethodPut, "/v1/uploads/"+id, header, body)
}

// hubUpload fetches or deletes an upload as the hub.
func (r *relay) hubUpload(method, hubID, credential, id string) (int, []byte) {
	r.t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+"/v1/hub/uploads/"+id, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("X-Yansu-Hub", hubID)
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func TestUploadRoundTripFromDeviceToItsHub(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"brief:read", "chat:write"})
	id := uuid.NewString()
	body := randomBytes(t, 70*1024)

	r.expectStatus(r.putUpload(dev.bearer, id, body), http.StatusCreated, "put upload")
	stored := filepath.Join(r.dir, "uploads", hubID, id)
	if info, err := os.Stat(stored); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("upload body on disk: %v %v", info, err)
	}
	status, raw := r.hubUpload(http.MethodGet, hubID, credential, id)
	if status != http.StatusOK || !bytes.Equal(raw, body) {
		t.Fatalf("hub get: status %d, %d bytes", status, len(raw))
	}
	if status, _ := r.hubUpload(http.MethodDelete, hubID, credential, id); status != http.StatusOK {
		t.Fatalf("hub delete status %d", status)
	}
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Fatalf("the body must be gone after delete: %v", err)
	}
	if status, _ := r.hubUpload(http.MethodGet, hubID, credential, id); status != http.StatusNotFound {
		t.Fatalf("get after delete status %d", status)
	}
	// A repeated delete after a lost answer still succeeds.
	if status, _ := r.hubUpload(http.MethodDelete, hubID, credential, id); status != http.StatusOK {
		t.Fatalf("repeat delete status %d", status)
	}
}

func TestUploadNeedsChatWriteAndStaysWithItsHub(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	writer := r.pairDevice(hc, []string{"chat:write"})
	reader := r.pairDevice(hc, []string{"brief:read"})
	otherID, otherCredential := uuid.NewString(), randomToken(t)
	r.connectHub(otherID, otherCredential)
	id := uuid.NewString()

	r.expectStatus(r.putUpload(reader.bearer, id, randomBytes(t, 32)), http.StatusForbidden, "device without chat:write")
	r.expectStatus(r.putUpload(writer.bearer, "not-a-uuid", randomBytes(t, 32)), http.StatusNotFound, "malformed id")
	r.expectStatus(r.putUpload(randomToken(t), id, randomBytes(t, 32)), http.StatusUnauthorized, "unknown bearer")
	header := bearer(writer.bearer)
	header.Set("Content-Type", "application/json")
	r.expectStatus(r.call(http.MethodPut, "/v1/uploads/"+id, header, []byte("{}")), http.StatusUnsupportedMediaType, "wrong content type")
	r.expectStatus(r.putUpload(writer.bearer, id, nil), http.StatusRequestEntityTooLarge, "empty body")
	r.expectStatus(r.putUpload(writer.bearer, id, randomBytes(t, 32)), http.StatusCreated, "put")
	r.expectStatus(r.putUpload(reader.bearer, id, randomBytes(t, 32)), http.StatusForbidden, "other device cannot replace")

	if status, _ := r.hubUpload(http.MethodGet, otherID, otherCredential, id); status != http.StatusNotFound {
		t.Fatalf("another hub read the upload: %d", status)
	}
	if status, _ := r.hubUpload(http.MethodDelete, otherID, otherCredential, id); status != http.StatusOK {
		t.Fatalf("another hub delete: %d", status)
	}
	if status, _ := r.hubUpload(http.MethodGet, hubID, credential, id); status != http.StatusOK {
		t.Fatal("another hub's delete removed this hub's upload")
	}
	if status, _ := r.hubUpload(http.MethodGet, hubID, randomToken(t), id); status != http.StatusUnauthorized {
		t.Fatalf("wrong hub credential: %d", status)
	}
}

func TestUploadQuotaRefusesWithoutEvicting(t *testing.T) {
	old := store.MaxUploadBytesPerDevice
	store.MaxUploadBytesPerDevice = 100
	t.Cleanup(func() { store.MaxUploadBytesPerDevice = old })
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	first := uuid.NewString()
	r.expectStatus(r.putUpload(dev.bearer, first, randomBytes(t, 60)), http.StatusCreated, "first")
	resp := r.putUpload(dev.bearer, uuid.NewString(), randomBytes(t, 60))
	r.expectStatus(resp, http.StatusTooManyRequests, "over quota")
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("a full quota must say when to retry")
	}
	if status, _ := r.hubUpload(http.MethodGet, hubID, credential, first); status != http.StatusOK {
		t.Fatal("a waiting recording was evicted")
	}
	// Taking the first one makes room.
	r.hubUpload(http.MethodDelete, hubID, credential, first)
	r.expectStatus(r.putUpload(dev.bearer, uuid.NewString(), randomBytes(t, 60)), http.StatusCreated, "after delete")
}

func TestUploadExpiresAndIsSwept(t *testing.T) {
	r := newRelay(t, "")
	hubID, credential := uuid.NewString(), randomToken(t)
	hc := r.connectHub(hubID, credential)
	dev := r.pairDevice(hc, []string{"chat:write"})
	id := uuid.NewString()
	r.expectStatus(r.putUpload(dev.bearer, id, randomBytes(t, 64)), http.StatusCreated, "put")
	r.advance(store.UploadTTL + time.Minute)
	if status, _ := r.hubUpload(http.MethodGet, hubID, credential, id); status != http.StatusNotFound {
		t.Fatalf("expired upload status %d", status)
	}
	swept, err := r.store.Sweep(context.Background(), r.now())
	if err != nil || swept.Uploads != 1 {
		t.Fatalf("sweep %+v %v", swept, err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "uploads", hubID, id)); !os.IsNotExist(err) {
		t.Fatal("the expired body is still on disk")
	}
}
