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
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/asr"
	"wails-gui/internal/platform/activitymon"
)

// mapUploads is a relay stand-in holding sealed uploads by id.
type mapUploads struct {
	bodies  map[string][]byte
	deleted []string
}

func (m *mapUploads) downloadUpload(_ context.Context, id string) ([]byte, error) {
	body, ok := m.bodies[id]
	if !ok {
		return nil, errMobileRecordingGone
	}
	return body, nil
}

func (m *mapUploads) deleteUpload(_ context.Context, id string) error {
	delete(m.bodies, id)
	m.deleted = append(m.deleted, id)
	return nil
}

const recordingTestHub = "6f1c2c9e-3b1a-4c55-9f4e-2a7b8c9d0e1f"

// sealedRecordingPart seals audio as the phone does and returns the request.
func sealedRecordingPart(t *testing.T, uploads *mapUploads, deviceID, recordingID string, part int, last bool, audio []byte, started time.Time) MobileRecordingRequest {
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
	return MobileRecordingRequest{
		IdempotencyKey: uuid.NewString(), RecordingID: recordingID, Part: part, Last: last,
		Upload: MobileRecordingUpload{ID: uploadID, Key: base64.RawURLEncoding.EncodeToString(key), Nonce: base64.RawURLEncoding.EncodeToString(nonce)},
		SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(audio)), Mime: "audio/mp4",
		StartedAt: started.UTC().Format(time.RFC3339), DurationSeconds: 30, Source: "watch",
	}
}

func TestMobileUploadSealIsBoundToHubDeviceAndID(t *testing.T) {
	key, nonce := make([]byte, 32), make([]byte, 12)
	_, _ = rand.Read(key)
	sealed, err := sealMobileUpload(key, nonce, "hub", "dev", "up", []byte("audio"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openMobileUpload(key, nonce, "hub", "dev", "up", sealed); err != nil || string(got) != "audio" {
		t.Fatalf("open = %q %v", got, err)
	}
	for _, c := range [][3]string{{"other", "dev", "up"}, {"hub", "other", "up"}, {"hub", "dev", "other"}} {
		if _, err := openMobileUpload(key, nonce, c[0], c[1], c[2], sealed); err == nil {
			t.Fatalf("opened under %v", c)
		}
	}
}

func TestReceiveRecordingPartStoresPrivatelyAndClearsTheRelay(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	audio := []byte("not really aac but bytes")
	req := sealedRecordingPart(t, uploads, "dev-a", recordingID, 0, false, audio, time.Now().Add(-time.Minute))

	view, complete, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req)
	if err != nil || complete || view.PartsReceived != 1 || view.State != mobileRecordingReceived {
		t.Fatalf("first part: %+v complete=%v err=%v", view, complete, err)
	}
	if len(uploads.bodies) != 0 || len(uploads.deleted) != 1 {
		t.Fatalf("relay copy not removed: %v", uploads.deleted)
	}
	file := filepath.Join(store.dir, recordingID, "part-00.m4a")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("part file %v %v", info, err)
	}
	if dir, _ := os.Stat(filepath.Join(store.dir, recordingID)); dir.Mode().Perm() != 0700 {
		t.Fatalf("folder mode %v", dir.Mode())
	}
	if raw, _ := os.ReadFile(file); string(raw) != string(audio) {
		t.Fatal("stored bytes differ")
	}

	// A repeat of the same part (a lost answer) is answered from the index.
	if view, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil || view.PartsReceived != 1 {
		t.Fatalf("repeat: %+v %v", view, err)
	}

	second := sealedRecordingPart(t, uploads, "dev-a", recordingID, 1, true, []byte("second"), time.Now())
	if _, complete, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", second); err != nil || !complete {
		t.Fatalf("second part complete=%v err=%v", complete, err)
	}
	if pending := store.pending(); len(pending) != 1 || pending[0].ID != recordingID {
		t.Fatalf("pending %+v", pending)
	}

	// A part past the last one is refused.
	late := sealedRecordingPart(t, uploads, "dev-a", recordingID, 2, false, []byte("late"), time.Now())
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", late); !errors.Is(err, errMobileRecordingInvalid) {
		t.Fatalf("part past the last: %v", err)
	}

	// Another device cannot add to or read this recording.
	foreign := sealedRecordingPart(t, uploads, "dev-b", recordingID, 1, true, []byte("x"), time.Now())
	foreign.Part = 0
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-b", foreign); !errors.Is(err, errMobileRecordingInvalid) {
		t.Fatalf("foreign device: %v", err)
	}
	if views, _ := store.list("dev-b", nil, 50); len(views) != 0 {
		t.Fatalf("dev-b sees %+v", views)
	}
}

func TestReceiveRecordingPartRefusesAChangedBody(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	req := sealedRecordingPart(t, uploads, "dev-a", uuid.NewString(), 0, true, []byte("audio"), time.Now())
	req.SHA256 = strings.Repeat("0", 64)
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); !errors.Is(err, errMobileRecordingSHA) {
		t.Fatalf("checksum: %v", err)
	}
	// Sealed for another device: the opener refuses it.
	other := sealedRecordingPart(t, uploads, "dev-b", uuid.NewString(), 0, true, []byte("audio"), time.Now())
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", other); !errors.Is(err, errMobileAttachmentRejected) {
		t.Fatalf("wrong device seal: %v", err)
	}
	if entries, _ := os.ReadDir(store.dir); len(entries) != 0 {
		t.Fatalf("refused parts left files: %d", len(entries))
	}
}

func TestValidateRecordingRequest(t *testing.T) {
	now := time.Now()
	uploads := &mapUploads{bodies: map[string][]byte{}}
	good := sealedRecordingPart(t, uploads, "dev", uuid.NewString(), 0, true, []byte("a"), now)
	if err := validateMobileRecordingRequest(good, now); err != nil {
		t.Fatalf("good request: %v", err)
	}
	bad := []func(*MobileRecordingRequest){
		func(r *MobileRecordingRequest) { r.RecordingID = "x" },
		func(r *MobileRecordingRequest) { r.Part = -1 },
		func(r *MobileRecordingRequest) { r.Part = mobileRecordingMaxParts },
		func(r *MobileRecordingRequest) { r.Bytes = 0 },
		func(r *MobileRecordingRequest) { r.DurationSeconds = 0 },
		func(r *MobileRecordingRequest) { r.Mime = "video/mp4" },
		func(r *MobileRecordingRequest) { r.Source = "car" },
		func(r *MobileRecordingRequest) { r.SHA256 = strings.ToUpper(r.SHA256) },
		func(r *MobileRecordingRequest) { r.StartedAt = now.Add(time.Hour).UTC().Format(time.RFC3339) },
	}
	for i, change := range bad {
		req := good
		change(&req)
		if err := validateMobileRecordingRequest(req, now); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestTranscribedRecordingBecomesSpeechActivity(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	started := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	req := sealedRecordingPart(t, uploads, "dev-a", recordingID, 0, true, []byte("audio"), started)
	if err := validateMobileRecordingRequest(req, started.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}

	oldT, oldS := mobileRecordingTranscriber, mobileRecordingSaver
	t.Cleanup(func() { mobileRecordingTranscriber, mobileRecordingSaver = oldT, oldS })
	mobileRecordingTranscriber = func(_ *App, path string) ([]asr.Segment, error) {
		if !strings.HasSuffix(path, "part-00.m4a") {
			t.Fatalf("transcribed %s", path)
		}
		return []asr.Segment{{Start: 2 * time.Second, Text: " Call Sam about the launch "}, {Start: 9 * time.Second, Text: "  "}}, nil
	}
	var saved ActivitySessionRow
	var savedEvents []activitymon.ActivityEvent
	mobileRecordingSaver = func(_ *App, session ActivitySessionRow, events []activitymon.ActivityEvent) error {
		saved, savedEvents = session, events
		return nil
	}

	app := &App{}
	app.transcribeMobileRecordings(context.Background(), store)
	if saved.ID != "rec-"+recordingID || saved.TriggerKind != mobileRecordingTrigger || saved.TriageStatus != "pending" {
		t.Fatalf("session %+v", saved)
	}
	if len(savedEvents) != 1 {
		t.Fatalf("events %+v", savedEvents)
	}
	evt := savedEvents[0]
	if evt.Kind != activitymon.EventSpeech || evt.SpeechText != "Call Sam about the launch" || evt.AppName != mobileRecordingAppName ||
		evt.SpeechSource != "microphone" || evt.Timestamp != started.Add(2*time.Second).UnixMilli() {
		t.Fatalf("event %+v", evt)
	}
	views, _ := store.list("dev-a", []string{recordingID}, 50)
	if len(views) != 1 || views[0].State != mobileRecordingTranscribed || views[0].Words != 5 {
		t.Fatalf("views %+v", views)
	}
	if len(store.pending()) != 0 {
		t.Fatal("a transcribed recording is still pending")
	}
}

func TestFailedTranscriptionIsMarkedAndNotRetriedInALoop(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	req := sealedRecordingPart(t, uploads, "dev-a", uuid.NewString(), 0, true, []byte("audio"), time.Now())
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}
	oldT := mobileRecordingTranscriber
	t.Cleanup(func() { mobileRecordingTranscriber = oldT })
	calls := 0
	mobileRecordingTranscriber = func(*App, string) ([]asr.Segment, error) {
		calls++
		return nil, errors.New("no engine")
	}
	(&App{}).transcribeMobileRecordings(context.Background(), store)
	views, _ := store.list("dev-a", nil, 50)
	if calls != 1 || len(views) != 1 || views[0].State != mobileRecordingFailed {
		t.Fatalf("calls=%d views=%+v", calls, views)
	}
}

func TestRecordingRoutesNeedChatWrite(t *testing.T) {
	bridge := &mobileBridge{}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, mobileAPIPrefix+"/recordings", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		bridge.handleAuthorized(recorder, req, mobileTestDevice(mobileScopeBriefRead))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s without chat:write: %d", method, recorder.Code)
		}
	}
}

func TestRecordingStoreSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store := newMobileRecordingStore(dir)
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	req := sealedRecordingPart(t, uploads, "dev-a", recordingID, 0, true, []byte("audio"), time.Now())
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}
	store.setState(recordingID, mobileRecordingTranscribing, "", 0, "")
	reopened := newMobileRecordingStore(dir)
	if pending := reopened.pending(); len(pending) != 1 {
		t.Fatalf("a recording interrupted mid-transcription must be pending after restart: %+v", pending)
	}
	if info, _ := os.Stat(filepath.Join(dir, "recordings", "index.json")); info.Mode().Perm() != 0600 {
		t.Fatalf("index mode %v", info.Mode())
	}
}

func TestRecordingWithoutALastPartIsClosedAfterSixHours(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	clock := time.Now()
	store.now = func() time.Time { return clock }
	uploads := &mapUploads{bodies: map[string][]byte{}}
	req := sealedRecordingPart(t, uploads, "dev-a", uuid.NewString(), 0, false, []byte("audio"), clock)
	if _, complete, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil || complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
	if len(store.pending()) != 0 {
		t.Fatal("a recording still arriving must not be transcribed")
	}
	clock = clock.Add(mobileRecordingAbandonedAfter + time.Minute)
	if pending := store.pending(); len(pending) != 1 || pending[0].Parts != 1 {
		t.Fatalf("abandoned recording not closed: %+v", pending)
	}
}

func TestOldTranscribedAudioIsRemovedAndFailedOnesRetried(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	clock := time.Now()
	store.now = func() time.Time { return clock }
	uploads := &mapUploads{bodies: map[string][]byte{}}
	done, failed := uuid.NewString(), uuid.NewString()
	for _, id := range []string{done, failed} {
		req := sealedRecordingPart(t, uploads, "dev-a", id, 0, true, []byte("audio"), clock)
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
			t.Fatal(err)
		}
	}
	store.setState(done, mobileRecordingTranscribed, "rec-"+done, 3, "")
	store.setState(failed, mobileRecordingFailed, "", 0, "no engine")

	store.retryFailedAndPrune()
	if _, err := os.Stat(filepath.Join(store.dir, done, "part-00.m4a")); err != nil {
		t.Fatalf("fresh audio must stay: %v", err)
	}
	if pending := store.pending(); len(pending) != 1 || pending[0].ID != failed {
		t.Fatalf("failed recording not retried: %+v", pending)
	}

	clock = clock.Add(mobileRecordingAudioKeep + time.Hour)
	store.retryFailedAndPrune()
	if _, err := os.Stat(filepath.Join(store.dir, done)); !os.IsNotExist(err) {
		t.Fatalf("old audio still on disk: %v", err)
	}
	if views, _ := store.list("dev-a", []string{done}, 50); len(views) != 1 || views[0].State != mobileRecordingTranscribed {
		t.Fatalf("the row must outlive its audio: %+v", views)
	}
}
