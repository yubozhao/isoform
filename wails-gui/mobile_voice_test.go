package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"wails-gui/storage"
)

// sealedVoiceSample seals audio as the phone does and returns the request.
func sealedVoiceSample(t *testing.T, uploads *mapUploads, deviceID string, audio []byte) MobileVoiceSampleRequest {
	t.Helper()
	key, nonce := make([]byte, 32), make([]byte, 12)
	_, _ = rand.Read(key)
	_, _ = rand.Read(nonce)
	uploadID := uuid.NewString()
	sealed, err := sealMobileUpload(key, nonce, recordingTestHub, deviceID, uploadID, audio)
	if err != nil {
		t.Fatal(err)
	}
	uploads.bodies[uploadID] = sealed
	sum := sha256.Sum256(audio)
	return MobileVoiceSampleRequest{
		IdempotencyKey: uuid.NewString(),
		Upload:         MobileRecordingUpload{ID: uploadID, Key: base64.RawURLEncoding.EncodeToString(key), Nonce: base64.RawURLEncoding.EncodeToString(nonce)},
		SHA256:         hex.EncodeToString(sum[:]), Bytes: int64(len(audio)), Mime: "audio/mp4", DurationSeconds: 8,
	}
}

func voiceTestApp(t *testing.T) (*App, *storage.Store) {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &App{store: store}, store
}

func TestPhoneVoiceSampleIsEnrolledForTheOwnerAndNoAudioIsKept(t *testing.T) {
	app, store := voiceTestApp(t)
	uploads := &mapUploads{bodies: map[string][]byte{}}
	req := sealedVoiceSample(t, uploads, "dev-a", []byte("eight seconds of the owner"))

	old := mobileVoiceEmbedder
	t.Cleanup(func() { mobileVoiceEmbedder = old })
	var samplePath string
	mobileVoiceEmbedder = func(_ *App, path string) ([]float32, float64, error) {
		samplePath = path
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != "eight seconds of the owner" {
			t.Fatalf("the embedder did not get the opened sample: %q %v", raw, err)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0600 {
			t.Fatalf("sample mode %v", info.Mode())
		}
		return []float32{0.1, 0.2, 0.3}, 8, nil
	}

	sample, err := app.enrollMobileVoiceSample(context.Background(), store, uploads, recordingTestHub, "dev-a", req)
	if err != nil {
		t.Fatal(err)
	}
	if !sample.FromPhone || sample.AudioSeconds != 8 || sample.ID == 0 {
		t.Fatalf("sample %+v", sample)
	}
	if _, err := os.Stat(samplePath); !os.IsNotExist(err) {
		t.Fatalf("the opened audio must be gone after enrollment: %v", err)
	}
	if len(uploads.bodies) != 0 {
		t.Fatal("the relay copy must be deleted")
	}
	rows, err := store.ListVoiceEmbeddingsForKind(context.Background(), storage.PrincipalKindSelf)
	if err != nil || len(rows) != 1 || len(rows[0].Embedding) != 3 {
		t.Fatalf("the owner's samples: %+v %v", rows, err)
	}
	listed, _ := mobileVoiceSamples(context.Background(), store)
	if len(listed) != 1 || listed[0].ID != sample.ID {
		t.Fatalf("listed %+v", listed)
	}
}

func TestAnUnusablePhoneSampleStillLeavesTheRelayAndEnrollsNothing(t *testing.T) {
	app, store := voiceTestApp(t)
	old := mobileVoiceEmbedder
	t.Cleanup(func() { mobileVoiceEmbedder = old })
	mobileVoiceEmbedder = func(*App, string) ([]float32, float64, error) { return nil, 1, errMobileVoiceTooShort }

	uploads := &mapUploads{bodies: map[string][]byte{}}
	req := sealedVoiceSample(t, uploads, "dev-a", []byte("hm"))
	if _, err := app.enrollMobileVoiceSample(context.Background(), store, uploads, recordingTestHub, "dev-a", req); !errors.Is(err, errMobileVoiceTooShort) {
		t.Fatalf("too short: %v", err)
	}
	// A body sealed for another device does not open.
	other := sealedVoiceSample(t, uploads, "dev-b", []byte("someone else"))
	if _, err := app.enrollMobileVoiceSample(context.Background(), store, uploads, recordingTestHub, "dev-a", other); !errors.Is(err, errMobileAttachmentRejected) {
		t.Fatalf("wrong device: %v", err)
	}
	// A changed body fails its checksum.
	changed := sealedVoiceSample(t, uploads, "dev-a", []byte("audio"))
	changed.SHA256 = strings.Repeat("0", 64)
	if _, err := app.enrollMobileVoiceSample(context.Background(), store, uploads, recordingTestHub, "dev-a", changed); !errors.Is(err, errMobileRecordingSHA) {
		t.Fatalf("checksum: %v", err)
	}
	if len(uploads.bodies) != 0 {
		t.Fatalf("every sample the computer read must leave the relay, %d left", len(uploads.bodies))
	}
	if rows, _ := store.ListVoiceEmbeddingsForKind(context.Background(), storage.PrincipalKindSelf); len(rows) != 0 {
		t.Fatalf("nothing may be enrolled: %+v", rows)
	}
}

func TestForgettingTheOwnersVoiceFromThePhone(t *testing.T) {
	app, store := voiceTestApp(t)
	ctx := context.Background()
	self, err := store.EnsureSelf(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreatePrincipal(ctx, storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: "Sam", CreatedFrom: "test", Confidence: 1})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, label := range []string{"enrolled 2026-11-01 09:00", "phone 2026-11-14 10:00"} {
		row, err := store.CreateVoiceEmbedding(ctx, storage.VoiceEmbeddingRow{PrincipalID: self.ID, Embedding: []float32{1, 0}, Label: label})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, row.ID)
	}
	theirs, err := store.CreateVoiceEmbedding(ctx, storage.VoiceEmbeddingRow{PrincipalID: other.ID, Embedding: []float32{0, 1}})
	if err != nil {
		t.Fatal(err)
	}

	// Another person's sample cannot be named from the phone.
	if removed, err := app.forgetMobileVoice(ctx, store, theirs.ID); err != nil || removed != 0 {
		t.Fatalf("removed someone else's sample: %d %v", removed, err)
	}
	if removed, err := app.forgetMobileVoice(ctx, store, ids[1]); err != nil || removed != 1 {
		t.Fatalf("one sample: %d %v", removed, err)
	}
	if left, _ := mobileVoiceSamples(ctx, store); len(left) != 1 || left[0].FromPhone {
		t.Fatalf("left %+v", left)
	}
	if removed, err := app.forgetMobileVoice(ctx, store, 0); err != nil || removed != 1 {
		t.Fatalf("all: %d %v", removed, err)
	}
	if left, _ := mobileVoiceSamples(ctx, store); len(left) != 0 {
		t.Fatalf("left %+v", left)
	}
	if rows, _ := store.ListVoiceEmbeddingsByPrincipal(ctx, other.ID); len(rows) != 1 {
		t.Fatal("forgetting the owner's voice removed another person's")
	}
}

func TestValidateVoiceSample(t *testing.T) {
	uploads := &mapUploads{bodies: map[string][]byte{}}
	good := sealedVoiceSample(t, uploads, "dev", []byte("a"))
	if err := validateMobileVoiceSample(good); err != nil {
		t.Fatal(err)
	}
	bad := []func(*MobileVoiceSampleRequest){
		func(r *MobileVoiceSampleRequest) { r.Upload.ID = "x" },
		func(r *MobileVoiceSampleRequest) { r.Bytes = mobileVoiceMaxBytes + 1 },
		func(r *MobileVoiceSampleRequest) { r.DurationSeconds = 2 },
		func(r *MobileVoiceSampleRequest) { r.DurationSeconds = 31 },
		func(r *MobileVoiceSampleRequest) { r.Mime = "video/mp4" },
		func(r *MobileVoiceSampleRequest) { r.SHA256 = "zz" },
	}
	for i, change := range bad {
		req := good
		change(&req)
		if err := validateMobileVoiceSample(req); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestVoiceRoutesNeedMemoryCorrect(t *testing.T) {
	bridge := &mobileBridge{}
	for _, route := range [][2]string{{http.MethodGet, "/voice"}, {http.MethodPost, "/voice/samples"}, {http.MethodPost, "/voice/forget"}} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(route[0], mobileAPIPrefix+route[1], strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		bridge.handleAuthorized(recorder, req, mobileTestDevice(mobileScopeBriefRead, mobileScopeChatWrite))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s without memory:correct: %d", route[0], route[1], recorder.Code)
		}
	}
}
