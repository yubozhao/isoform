package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/asr"
	"wails-gui/models"
)

// Complete a real manager job using a tiny local file installed after planning.
// The download worker takes its already-present path without opening a socket.
func finishArrivalFixture(t *testing.T, job string, handler func(context.Context, models.Entry)) {
	t.Helper()
	dir := t.TempDir()
	data := []byte("arrival fixture")
	n, sum := int64(len(data)), sha256Hex(data)
	e := models.Entry{ID: "fixture", Job: job, Recommended: true, DisplayName: "Fixture", Licence: models.Licence{Name: "MIT"}, SizeBytes: &n,
		Files:   []models.File{{Path: "fixture.bin", Bytes: &n, SHA256: &sum, URLs: []string{"https://model-assets.yansu.app/fixture.bin"}}},
		Sources: []models.Source{{Kind: "mirror", URL: "https://model-assets.yansu.app/"}}}
	manager, err := models.New(dir, dir, []models.Entry{e}, func(string) (int64, error) { return 1000, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	done := make(chan struct{})
	manager.SetDownloadComplete(func(ctx context.Context, entry models.Entry) {
		handler(ctx, entry)
		close(done)
	})
	plan, err := manager.BuildPlan(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "fixture.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = manager.Start(plan); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("missing completion: %+v", manager.Downloads())
	}
}

func receiveWaitingWatch(t *testing.T, watch *mobileRecordingStore) string {
	t.Helper()
	id := uuid.NewString()
	uploads := &mapUploads{bodies: map[string][]byte{}}
	req := sealedRecordingPart(t, uploads, "dev", id, 0, true, []byte("audio"), time.Now())
	if _, _, err := receiveMobileRecordingPart(context.Background(), watch, uploads, recordingTestHub, "dev", req); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSpeechModelArrivalDrainsWaitingAudio(t *testing.T) {
	t.Run("on completion", func(t *testing.T) { checkSpeechModelArrival(t, false) })
	t.Run("after Closed Eyes", func(t *testing.T) { checkSpeechModelArrival(t, true) })
}

func checkSpeechModelArrival(t *testing.T, paused bool) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	app.ctx = t.Context()
	if err := saveConfig(yansuConfig{BackgroundListeningConfigured: true, BackgroundListeningDisabled: true}); err != nil {
		t.Fatal(err)
	}
	store := newLifecycleStore(t)
	app.activity = &activityMonitorState{store: store}
	app.mobileBridge = &mobileBridge{stateDir: t.TempDir(), ctx: context.Background(), app: app}
	watch := app.watchRecordingStore()
	w, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	w.SetMeeting("Zoom", "us.zoom.xos", "waiting-call")
	w.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 2000)))
	call, _, ok := app.saveCallRecordingLocked(w, 0, "meeting_ended")
	if !ok {
		t.Fatal("could not save waiting call")
	}
	oldFollowup, oldDecode := meetingFollowupModel, mobileRecordingTranscriber
	defer func() { meetingFollowupModel, mobileRecordingTranscriber = oldFollowup, oldDecode }()
	meetingFollowupModel = func(*App, string) (string, error) { return `{"decisions":[],"followups":[]}`, nil }
	id, forgotten := receiveWaitingWatch(t, watch), receiveWaitingWatch(t, watch)
	watchReads := 0
	mobileRecordingTranscriber = func(a *App, path string) ([]mobileRecordingSegment, error) {
		if a.speechTranscriber() == nil {
			return nil, errors.New(mobileSpeechNotReady)
		}
		watchReads++
		if filepath.Base(filepath.Dir(path)) != id {
			t.Errorf("read withdrawn audio %s", path)
		}
		return []mobileRecordingSegment{{Segment: asr.Segment{Text: "watch words"}}}, nil
	}
	app.recoverUntranscribedMeetings()
	app.transcribeMobileRecordings(context.Background(), watch)
	watch.onPrivacyWithdrawal(privacyWithdrawal{Scope: privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: "rec-" + forgotten}})
	waiting := func() {
		t.Helper()
		row, err := store.GetSessionByID(call.ID)
		if err != nil || row == nil || row.TranscriptPath != "" {
			t.Fatalf("waiting call: %+v, %v", row, err)
		}
		rec, ok := watch.get(id, "")
		if !ok || rec.State != mobileRecordingFailed || rec.Error != mobileSpeechNotReady {
			t.Fatalf("waiting watch: %+v", rec)
		}
		for _, path := range []string{filepath.Join(store.AudioDir(), call.AudioPath), watch.partPaths(rec)[0]} {
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
		}
	}
	waiting()
	loads := 0
	engine := &recordingFixtureEngine{fakeLongForm: fakeLongForm{texts: map[int16]string{2000: "call words"}}}
	handler := func(ctx context.Context, e models.Entry) {
		done := make(chan struct{})
		app.modelDownloadCompleted(ctx, e, func() error {
			return app.initVoiceInput(true, func() (asr.Transcriber, error) {
				loads++
				if loads == 1 {
					return nil, errors.New("fixture load failure")
				}
				return engine, nil
			})
		}, func(ctx context.Context) {
			app.retryWaitingSpeech(ctx)
			close(done)
		})
		if app.speechTranscriber() != nil {
			<-done
		}
	}
	finishArrivalFixture(t, "privacy", handler)
	if loads != 0 {
		t.Fatal("privacy model loaded speech")
	}
	var logs bytes.Buffer
	output := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(output)
	finishArrivalFixture(t, "live", handler)
	if loads != 1 || !strings.Contains(logs.String(), "[speech-model-arrival] fixture: fixture load failure") {
		t.Fatalf("loads=%d log=%s", loads, &logs)
	}
	waiting()
	if paused {
		app.closedEyes.enabled.Publish(true, time.Time{})
		finishArrivalFixture(t, "speakerPack", handler)
		waiting()
		if engine.calls != 0 || watchReads != 0 {
			t.Fatal("read audio during Closed Eyes")
		}
		app.closedEyes.enabled.Publish(false, time.Time{})
		app.retryWaitingSpeech(context.Background())
	} else {
		finishArrivalFixture(t, "live", handler)
	}
	row, err := store.GetSessionByID(call.ID)
	if err != nil || row == nil || row.TranscriptPath == "" {
		t.Fatalf("call transcript: %+v %v", row, err)
	}
	body, err := os.ReadFile(row.TranscriptPath)
	if err != nil || !strings.Contains(string(body), "call words") {
		t.Fatalf("call transcript %q: %v", body, err)
	}
	rec, _ := watch.get(id, "")
	events, err := store.GetSessionEvents(rec.SessionID)
	if rec.State != mobileRecordingTranscribed || rec.Words != 2 || err != nil || len(events) != 1 || events[0].SpeechText != "watch words" {
		t.Fatalf("watch: %+v events=%v err=%v", rec, events, err)
	}
	finishArrivalFixture(t, "afterCall", handler)
	if loads != 2 || watchReads != 1 || app.GetBackgroundListening() || app.isBackgroundListeningRunning() {
		t.Fatalf("loads=%d watch reads=%d listening=%v", loads, watchReads, app.GetBackgroundListening())
	}
}

func TestSpeechModelArrivalWaitsForInFlightWatchFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}
	store := newMobileRecordingStore(t.TempDir())
	id := receiveWaitingWatch(t, store)
	old := mobileRecordingTranscriber
	defer func() { mobileRecordingTranscriber = old }()
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	mobileRecordingTranscriber = func(*App, string) ([]mobileRecordingSegment, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release
			return nil, errors.New(mobileSpeechNotReady)
		}
		return nil, nil
	}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		app.transcribeMobileRecordings(context.Background(), store)
	}()
	<-started
	retryDone := make(chan struct{})
	go func() {
		defer close(retryDone)
		app.retryMobileRecordings(context.Background(), store, false)
	}()
	close(release)
	<-workerDone
	<-retryDone
	rec, ok := store.get(id, "")
	if !ok || calls != 2 || rec.State != mobileRecordingTranscribed || rec.Words != 0 || rec.Error != "" || rec.SessionID != "" || rec.AudioRemoved {
		t.Fatalf("calls=%d row=%+v", calls, rec)
	}
	if _, err := os.Stat(store.partPaths(rec)[0]); err != nil {
		t.Fatal(err)
	}
	// Reopen the on-disk index and run the launch retry on the quiet recording.
	reopened := newMobileRecordingStore(filepath.Dir(store.dir))
	reopened.retryFailedAndPrune()
	app.transcribeMobileRecordings(context.Background(), reopened)
	if calls != 2 || len(reopened.pending()) != 0 {
		t.Fatalf("launch retried quiet recording: %d calls", calls)
	}
}

func TestSpeechModelArrivalBeforeAppContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		app := &App{}
		initialized, retried := false, false
		app.modelDownloadCompleted(t.Context(), models.Entry{Job: "live"}, func() error {
			initialized = true
			return nil
		}, func(context.Context) { retried = true })
		synctest.Wait()
		if !initialized || retried {
			t.Fatalf("before app startup: initialized=%v retried=%v", initialized, retried)
		}
	})
}

func TestSpeechModelArrivalCloseDuringBacklog(t *testing.T) {
	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &App{ctx: appCtx}
	started, finished := make(chan struct{}), make(chan struct{})
	finishArrivalFixture(t, "live", func(ctx context.Context, e models.Entry) {
		app.modelDownloadCompleted(ctx, e, func() error { return nil }, func(workerCtx context.Context) {
			close(started)
			defer close(finished)
			select {
			case <-workerCtx.Done():
			case <-time.After(3 * time.Second):
				t.Error("manager close held up by backlog")
			}
		})
		<-started
	}) // The real manager is now closed, with the backlog still running.
	select {
	case <-finished:
		t.Fatal("backlog ended before app cancellation")
	default:
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("backlog did not observe app cancellation")
	}
}

func TestSpeechModelArrivalMissingVADDoesNotDownload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths := defaultSherpaPaths()
	if paths.senseVoice == "" {
		t.Skip("speech assets unavailable on this platform")
	}
	for _, path := range []string{paths.senseVoice, paths.tokens} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	saved := http.DefaultTransport
	defer func() { http.DefaultTransport = saved }()
	http.DefaultTransport = updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Errorf("arrival made an HTTP request to %s", req.URL.Host)
		return nil, errors.New("unexpected network request")
	})
	app := &App{ctx: t.Context()}
	finishArrivalFixture(t, "live", func(ctx context.Context, entry models.Entry) {
		app.modelDownloadCompleted(ctx, entry, app.initInstalledVoiceInput, func(context.Context) { t.Error("started backlog without VAD") })
	})
	if app.getWhisperState() != nil {
		t.Fatal("started engine initialization without VAD")
	}
	if _, err := os.Stat(paths.silero); !os.IsNotExist(err) {
		t.Fatalf("VAD after arrival: %v", err)
	}
}

func TestVoiceInputDownloadingPhaseTracksTransfer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}
	for _, transfer := range []bool{false, true} {
		err := app.initVoiceInput(false, func() (asr.Transcriber, error) {
			if app.GetVoiceInputStatus().Downloading {
				t.Fatal("plain engine load reported downloading")
			}
			if transfer {
				app.voiceDownloadProgress("fixture", 1, 2, 1)
				if !app.GetVoiceInputStatus().Downloading {
					t.Fatal("transfer did not report downloading")
				}
			}
			return nil, errors.New("fixture failure")
		})
		if err == nil || app.GetVoiceInputStatus().Downloading {
			t.Fatal("downloading after initialization finished")
		}
	}
}
