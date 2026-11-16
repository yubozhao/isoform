package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"wails-gui/internal/privacy"
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
	if views, _ := store.list("dev-b", nil, 50, false); len(views) != 0 {
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
	mobileRecordingTranscriber = func(_ *App, path string) ([]mobileRecordingSegment, error) {
		if !strings.HasSuffix(path, "part-00.m4a") {
			t.Fatalf("transcribed %s", path)
		}
		return mobileRecordingSpeakerSegments([]asr.Segment{{Start: 2 * time.Second, Text: " Call Sam about the launch "}, {Start: 9 * time.Second, Text: "  "}},
			func(asr.Segment) (*bool, []float32) { return nil, nil }), nil
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
	views, _ := store.list("dev-a", []string{recordingID}, 50, false)
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
	mobileRecordingTranscriber = func(*App, string) ([]mobileRecordingSegment, error) {
		calls++
		return nil, errors.New("no engine")
	}
	(&App{}).transcribeMobileRecordings(context.Background(), store)
	views, _ := store.list("dev-a", nil, 50, false)
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
	if views, _ := store.list("dev-a", []string{done}, 50, false); len(views) != 1 || views[0].State != mobileRecordingTranscribed {
		t.Fatalf("the row must outlive its audio: %+v", views)
	}
}

// A transcript the user deletes, or that retention removes, takes the
// recording's audio with it; the row stays so a resent part is not imported
// again.
func TestDeletedTranscriptTakesItsAudio(t *testing.T) {
	bridge := &mobileBridge{stateDir: t.TempDir()}
	bridge.mu.Lock()
	store := bridge.recordingStoreLocked()
	bridge.mu.Unlock()
	uploads := &mapUploads{bodies: map[string][]byte{}}
	withdrawn, retained, kept := uuid.NewString(), uuid.NewString(), uuid.NewString()
	requests := map[string]MobileRecordingRequest{}
	for _, id := range []string{withdrawn, retained, kept} {
		req := sealedRecordingPart(t, uploads, "dev-a", id, 0, true, []byte("audio"), time.Now())
		requests[id] = req
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
			t.Fatal(err)
		}
		store.setState(id, mobileRecordingTranscribed, "rec-"+id, 3, "")
	}

	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Session, ID: "rec-" + withdrawn})
	publishRetentionRemoval(privacy.Scope{Kind: privacy.Session, ID: "rec-" + retained})

	for _, id := range []string{withdrawn, retained} {
		if _, err := os.Stat(filepath.Join(store.dir, id)); !os.IsNotExist(err) {
			t.Fatalf("audio of a removed transcript is still on disk: %s %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(store.dir, kept, "part-00.m4a")); err != nil {
		t.Fatalf("audio of a kept transcript must stay: %v", err)
	}
	// The phone resends the part (a lost answer): it is answered from the
	// index, not fetched and stored again.
	resent := sealedRecordingPart(t, uploads, "dev-a", withdrawn, 0, true, []byte("audio"), time.Now())
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", resent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.dir, withdrawn)); !os.IsNotExist(err) {
		t.Fatalf("a resent part brought withdrawn audio back: %v", err)
	}
	if pending := store.pending(); len(pending) != 0 {
		t.Fatalf("a removed transcript must not be transcribed again: %+v", pending)
	}
}

// voiceOf is a made-up fingerprint: one axis per voice, so two segments by the
// same voice are identical and different voices are orthogonal.
func voiceOf(axis int) []float32 {
	v := make([]float32, 8)
	v[axis] = 1
	return v
}

func TestRecordingSpeechIsSplitByWhoSpoke(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	started := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	for part := 0; part < 2; part++ {
		req := sealedRecordingPart(t, uploads, "dev-a", recordingID, part, part == 1, []byte("audio"), started.Add(time.Duration(part)*30*time.Second))
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
			t.Fatal(err)
		}
	}
	yes, no := true, false
	oldT, oldS := mobileRecordingTranscriber, mobileRecordingSaver
	t.Cleanup(func() { mobileRecordingTranscriber, mobileRecordingSaver = oldT, oldS })
	mobileRecordingTranscriber = func(_ *App, path string) ([]mobileRecordingSegment, error) {
		if strings.HasSuffix(path, "part-00.m4a") {
			return []mobileRecordingSegment{
				{Segment: asr.Segment{Start: 0, End: 4 * time.Second, Text: "I will send the deck"}, OwnerVerified: &yes, Embedding: voiceOf(0)},
				{Segment: asr.Segment{Start: 5 * time.Second, End: 9 * time.Second, Text: "Thursday works for me"}, OwnerVerified: &no, Embedding: voiceOf(1)},
				{Segment: asr.Segment{Start: 10 * time.Second, End: 10500 * time.Millisecond, Text: "ok"}},
			}, nil
		}
		return []mobileRecordingSegment{
			{Segment: asr.Segment{Start: 0, End: 3 * time.Second, Text: "Can legal see it first"}, OwnerVerified: &no, Embedding: voiceOf(2)},
			{Segment: asr.Segment{Start: 4 * time.Second, End: 8 * time.Second, Text: "and Thursday is fine"}, OwnerVerified: &no, Embedding: voiceOf(1)},
		}, nil
	}
	var saved []activitymon.ActivityEvent
	mobileRecordingSaver = func(_ *App, _ ActivitySessionRow, events []activitymon.ActivityEvent) error {
		saved = events
		return nil
	}
	(&App{}).transcribeMobileRecordings(context.Background(), store)

	var labels []string
	for _, evt := range saved {
		labels = append(labels, evt.SpeechSpeakerLabel)
	}
	// The owner is "you"; the second voice keeps its number in the next part;
	// a stretch too short to judge carries no label.
	want := []string{"you", "speaker_1", "", "speaker_2", "speaker_1"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Fatalf("labels %v want %v", labels, want)
	}
	if saved[0].SpeechSpeakerVerified == nil || !*saved[0].SpeechSpeakerVerified || saved[1].SpeechSpeakerVerified == nil || *saved[1].SpeechSpeakerVerified {
		t.Fatal("the verdict the brief and memory read must be stored per segment")
	}
	views, _ := store.list("dev-a", []string{recordingID}, 50, false)
	if len(views) != 1 || views[0].OwnerVoice != mobileOwnerVoiceLabelled {
		t.Fatalf("views %+v", views)
	}
}

func TestWithoutAnEnrolledVoiceNobodyIsYou(t *testing.T) {
	events := []activitymon.ActivityEvent{
		{Kind: activitymon.EventSpeech, SpeechText: "a", SpeechSpeakerEmbedding: voiceOf(0)},
		{Kind: activitymon.EventSpeech, SpeechText: "b", SpeechSpeakerEmbedding: voiceOf(1)},
		{Kind: activitymon.EventSpeech, SpeechText: "c", SpeechSpeakerEmbedding: voiceOf(0)},
	}
	if got := labelMobileRecordingSpeakers(events); got != mobileOwnerVoiceNotEnrolled {
		t.Fatalf("status %q", got)
	}
	if events[0].SpeechSpeakerLabel != "speaker_1" || events[1].SpeechSpeakerLabel != "speaker_2" || events[2].SpeechSpeakerLabel != "speaker_1" {
		t.Fatalf("labels %q %q %q", events[0].SpeechSpeakerLabel, events[1].SpeechSpeakerLabel, events[2].SpeechSpeakerLabel)
	}
	bare := []activitymon.ActivityEvent{{Kind: activitymon.EventSpeech, SpeechText: "a"}}
	if got := labelMobileRecordingSpeakers(bare); got != mobileOwnerVoiceUnavailable || bare[0].SpeechSpeakerLabel != "" {
		t.Fatalf("no voice model: status %q label %q", got, bare[0].SpeechSpeakerLabel)
	}
}

func TestSpeakerVerdictSkipsShortAndEmptySegmentsAndCapsLongOnes(t *testing.T) {
	var spans []time.Duration
	out := mobileRecordingSpeakerSegments([]asr.Segment{
		{Start: 0, End: 500 * time.Millisecond, Text: "hm"},
		{Start: time.Second, End: 5 * time.Second, Text: "  "},
		{Start: 6 * time.Second, End: 96 * time.Second, Text: "a long story"},
	}, func(seg asr.Segment) (*bool, []float32) {
		spans = append(spans, seg.End-seg.Start)
		return nil, voiceOf(0)
	})
	if len(out) != 3 || len(spans) != 1 || spans[0] != mobileRecordingSpeakerMaxSpan {
		t.Fatalf("spans %v out %d", spans, len(out))
	}
	if out[0].Embedding != nil || out[1].Embedding != nil || out[2].Embedding == nil {
		t.Fatal("only the long spoken segment gets a fingerprint")
	}
}

func TestReadWAVSpanReadsOnlyTheAskedStretch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.wav")
	// 3 seconds of 16 kHz mono: second 0 is silence, second 1 is +0.5, second 2 is -0.5.
	data := make([]byte, 0, 3*16000*2)
	for sec, v := range []int16{0, 16384, -16384} {
		_ = sec
		for i := 0; i < 16000; i++ {
			data = append(data, byte(uint16(v)), byte(uint16(v)>>8))
		}
	}
	header := []byte("RIFF\x00\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80\x3e\x00\x00\x00\x7d\x00\x00\x02\x00\x10\x00data\x00\x00\x00\x00")
	if err := os.WriteFile(path, append(header, data...), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := readMeetingWAVInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	samples, err := readMeetingWAVSpan(path, info, time.Second, 2*time.Second)
	if err != nil || len(samples) != 16000 || samples[0] != 0.5 || samples[15999] != 0.5 {
		t.Fatalf("span: %d samples, first %v, err %v", len(samples), samples[:1], err)
	}
	if tail, err := readMeetingWAVSpan(path, info, 2500*time.Millisecond, 10*time.Second); err != nil || len(tail) != 8000 || tail[0] != -0.5 {
		t.Fatalf("a span past the end is cut at the end: %d %v", len(tail), err)
	}
	if _, err := readMeetingWAVSpan(path, info, 5*time.Second, 6*time.Second); err == nil {
		t.Fatal("a span wholly past the end must fail")
	}
}

func TestRecordingContextIsCoarsenedAndBadValuesAreDropped(t *testing.T) {
	got := sanitizeMobileRecordingContext(&MobileRecordingPartContext{
		HeartRate: &MobileRecordingHeartRate{Avg: 103.6, Min: 71.2, Max: 131.4, Samples: 40},
		Location:  &MobileRecordingLocation{Latitude: 37.759812, Longitude: -122.414923, AccuracyMeters: 1234.5, Place: " Mission District,\n San Francisco "},
	})
	if got == nil || got.HeartRate == nil || got.Location == nil {
		t.Fatalf("context %+v", got)
	}
	if *got.HeartRate != (MobileRecordingHeartRate{Avg: 104, Min: 71, Max: 131, Samples: 40}) {
		t.Fatalf("heart rate %+v", got.HeartRate)
	}
	if got.Location.Latitude != 37.76 || got.Location.Longitude != -122.41 || got.Location.AccuracyMeters != 1235 ||
		got.Location.Place != "Mission District, San Francisco" {
		t.Fatalf("location %+v", got.Location)
	}

	for name, bad := range map[string]*MobileRecordingPartContext{
		"nil":                  nil,
		"empty":                {},
		"no samples":           {HeartRate: &MobileRecordingHeartRate{Avg: 80, Min: 70, Max: 90}},
		"impossible rate":      {HeartRate: &MobileRecordingHeartRate{Avg: 400, Min: 70, Max: 500, Samples: 3}},
		"min above avg":        {HeartRate: &MobileRecordingHeartRate{Avg: 80, Min: 95, Max: 99, Samples: 3}},
		"latitude off the map": {Location: &MobileRecordingLocation{Latitude: 91, Longitude: 10}},
		"null island":          {Location: &MobileRecordingLocation{}},
	} {
		if got := sanitizeMobileRecordingContext(bad); got != nil {
			t.Fatalf("%s kept: %+v", name, got)
		}
	}
	// A place that is really coordinates never becomes a name.
	for _, place := range []string{"37.7598, -122.4149", "N 37 45 35", "   ", "\x00\x01"} {
		if got := cleanMobileRecordingPlace(place); got != "" {
			t.Fatalf("place %q kept as %q", place, got)
		}
	}
	if got := cleanMobileRecordingPlace(strings.Repeat("Llanfair ", 30)); len([]rune(got)) > mobileRecordingMaxPlaceRunes {
		t.Fatalf("place not shortened: %d", len([]rune(got)))
	}
}

func TestRecordingContextReachesTheSessionAsWordsNeverCoordinates(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	started := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	first := sealedRecordingPart(t, uploads, "dev-a", recordingID, 0, false, []byte("audio one"), started)
	first.Context = &MobileRecordingPartContext{
		HeartRate: &MobileRecordingHeartRate{Avg: 128, Min: 96, Max: 151, Samples: 60},
		Location:  &MobileRecordingLocation{Latitude: 37.759812, Longitude: -122.414923, Place: "Mission District"},
	}
	second := sealedRecordingPart(t, uploads, "dev-a", recordingID, 1, true, []byte("audio two"), started.Add(15*time.Minute))
	// The second part has a location without a name: nothing is said about where.
	second.Context = &MobileRecordingPartContext{Location: &MobileRecordingLocation{Latitude: 37.80, Longitude: -122.27}}
	for _, req := range []MobileRecordingRequest{first, second} {
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
			t.Fatal(err)
		}
	}
	index, err := os.ReadFile(filepath.Join(store.dir, "index.json"))
	if err != nil || strings.Contains(string(index), "37.759812") || !strings.Contains(string(index), "37.76") {
		t.Fatalf("the index must hold the coarse position only: %v %s", err, index)
	}

	oldT, oldS := mobileRecordingTranscriber, mobileRecordingSaver
	t.Cleanup(func() { mobileRecordingTranscriber, mobileRecordingSaver = oldT, oldS })
	mobileRecordingTranscriber = func(_ *App, path string) ([]mobileRecordingSegment, error) {
		return []mobileRecordingSegment{{Segment: asr.Segment{Start: 3 * time.Second, Text: "keep going"}}}, nil
	}
	var savedEvents []activitymon.ActivityEvent
	mobileRecordingSaver = func(_ *App, _ ActivitySessionRow, events []activitymon.ActivityEvent) error {
		savedEvents = events
		return nil
	}
	(&App{}).transcribeMobileRecordings(context.Background(), store)
	if len(savedEvents) != 2 {
		t.Fatalf("events %+v", savedEvents)
	}
	want := mobileRecordingAppName + " · at Mission District · heart rate high (avg 128, peak 151)"
	if savedEvents[0].WindowTitle != want {
		t.Fatalf("first part title %q, want %q", savedEvents[0].WindowTitle, want)
	}
	if savedEvents[1].WindowTitle != mobileRecordingAppName {
		t.Fatalf("a part with no name and no heart rate must keep the plain title: %q", savedEvents[1].WindowTitle)
	}
	for _, evt := range savedEvents {
		if strings.Contains(evt.WindowTitle, "37.") || strings.Contains(evt.WindowTitle, "122.") {
			t.Fatalf("coordinates reached text: %q", evt.WindowTitle)
		}
	}
}

func TestRecordingContextIsDeletedWithTheAudio(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	withdrawn, old := uuid.NewString(), uuid.NewString()
	for _, id := range []string{withdrawn, old} {
		req := sealedRecordingPart(t, uploads, "dev-a", id, 0, true, []byte("audio"), time.Now())
		req.Context = &MobileRecordingPartContext{
			HeartRate: &MobileRecordingHeartRate{Avg: 88, Min: 70, Max: 99, Samples: 12},
			Location:  &MobileRecordingLocation{Latitude: 48.86, Longitude: 2.35, Place: "Le Marais"},
		}
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
			t.Fatal(err)
		}
		store.setState(id, mobileRecordingTranscribed, "rec-"+id, 3, "")
	}
	if index, _ := os.ReadFile(filepath.Join(store.dir, "index.json")); !strings.Contains(string(index), "Le Marais") {
		t.Fatalf("context not stored with the part: %s", index)
	}

	// The transcript is deleted: audio and context go.
	store.forgetAudioOfSession("rec-" + withdrawn)
	// The keep time of the other one passes: audio and context go.
	store.now = func() time.Time { return time.Now().Add(mobileRecordingAudioKeep + time.Hour) }
	store.retryFailedAndPrune()

	index, err := os.ReadFile(filepath.Join(store.dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"Le Marais", "48.86", "heartRate", "location"} {
		if strings.Contains(string(index), gone) {
			t.Fatalf("%q outlived the audio: %s", gone, index)
		}
	}
}

func TestStatusAdvertisesRecordingContextAndTheRequestTakesIt(t *testing.T) {
	var req MobileRecordingRequest
	body := `{"idempotencyKey":"k","recordingId":"r","part":0,"last":true,"upload":{"id":"u","key":"k","nonce":"n"},"sha256":"s","bytes":1,"mime":"audio/mp4","startedAt":"t","durationSeconds":1,"source":"watch","context":{"heartRate":{"avg":90,"min":80,"max":99,"samples":5},"location":{"latitude":1.5,"longitude":2.5,"accuracyMeters":900,"place":"Somewhere"}}}`
	if err := mobileDecodeStrict([]byte(body), &req); err != nil {
		t.Fatalf("the strict decoder refuses the context: %v", err)
	}
	if req.Context == nil || req.Context.HeartRate.Samples != 5 || req.Context.Location.Place != "Somewhere" {
		t.Fatalf("context %+v", req.Context)
	}
	raw, _ := json.Marshal(mobileStatusView{RecordingContext: true})
	if !strings.Contains(string(raw), `"recordingContext":true`) {
		t.Fatalf("status %s", raw)
	}
}

func TestStatusSaysWhenObservingIsPausedSoTheWatchCanStop(t *testing.T) {
	if mobileObservingPauseView(false, 1, time.Now()) != nil {
		t.Fatal("not paused must say nothing")
	}
	raw, _ := json.Marshal((&mobileBridge{}).statusWithExtras(mobileStatusView{}))
	// The key itself: a later piece adds "observingPauseControl", which is not a pause.
	if strings.Contains(string(raw), `"observingPause":`) {
		t.Fatalf("status of a computer that is observing: %s", raw)
	}
	since := time.Date(2026, 11, 14, 11, 4, 0, 0, time.UTC)
	until := since.Add(time.Hour)
	raw, _ = json.Marshal(map[string]any{"observingPause": mobileObservingPauseView(true, since.UnixMilli(), until)})
	if !strings.Contains(string(raw), `"observingPause":{"since":"2026-11-14T11:04:00Z","until":"2026-11-14T12:04:00Z"}`) {
		t.Fatalf("status %s", raw)
	}
	// A pause with no end and a start the computer cannot vouch for is still a pause.
	raw, _ = json.Marshal(map[string]any{"observingPause": mobileObservingPauseView(true, 0, time.Time{})})
	if !strings.Contains(string(raw), `"observingPause":{}`) {
		t.Fatalf("status %s", raw)
	}
}
