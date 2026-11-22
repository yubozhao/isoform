package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/storage"
)

func listenOffFixture(t *testing.T) (*App, *mobileBridge) {
	t.Helper()
	app, bridge, _, _ := watchDeskFixture(t)
	state, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	app.store = state
	return app, bridge
}

// The phone turns listening off, never on: off holds from that moment until
// the wearer starts listening on the watch.
func TestThePhoneCanOnlyTurnWatchListeningOff(t *testing.T) {
	app, bridge := listenOffFixture(t)
	if got, err := app.watchListening(); err != nil || got.Off {
		t.Fatalf("off before anyone asked: %+v", got)
	}
	if got := watchDeskCall(bridge, http.MethodPost, "/watch/listening", `{"off":true}`, mobileScopeBriefRead); got.Code != http.StatusForbidden {
		t.Fatalf("without chat:write: %d", got.Code)
	}
	before := time.Now().Add(-time.Second)
	got := watchDeskCall(bridge, http.MethodPost, "/watch/listening", `{"off":true}`, mobileScopeChatWrite)
	var envelope struct {
		Data MobileWatchListening `json:"data"`
	}
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &envelope) != nil || !envelope.Data.Off || envelope.Data.Since == "" {
		t.Fatalf("off: %d %s", got.Code, got.Body.String())
	}
	spans, err := app.watchListenOffSpans()
	if err != nil || len(spans) != 1 || spans[0].End != 0 || spans[0].Start < before.UnixMilli() {
		t.Fatalf("spans %+v %v", spans, err)
	}
	// Off twice is one off.
	watchDeskCall(bridge, http.MethodPost, "/watch/listening", `{"off":true}`, mobileScopeChatWrite)
	if spans, _ = app.watchListenOffSpans(); len(spans) != 1 {
		t.Fatalf("a second off opened a second stretch: %+v", spans)
	}
	// The phone cannot turn it on by itself.
	if got := watchDeskCall(bridge, http.MethodPost, "/watch/listening", `{"off":false}`, mobileScopeChatWrite); got.Code != http.StatusBadRequest {
		t.Fatalf("on without the watch's own start: %d", got.Code)
	}
	if got, err := app.watchListening(); err != nil || !got.Off {
		t.Fatal("a refused on ended the off")
	}
	// The wearer started on the watch: the off ends then.
	started := time.Now().UTC().Truncate(time.Second)
	body, _ := json.Marshal(map[string]any{"off": false, "startedAt": started.Format(time.RFC3339)})
	if got := watchDeskCall(bridge, http.MethodPost, "/watch/listening", string(body), mobileScopeChatWrite); got.Code != http.StatusOK {
		t.Fatalf("on from the watch: %d %s", got.Code, got.Body.String())
	}
	if got, err := app.watchListening(); err != nil || got.Off {
		t.Fatal("still off after the wearer started on the watch")
	}
	spans, _ = app.watchListenOffSpans()
	if len(spans) != 1 || spans[0].End <= spans[0].Start {
		t.Fatalf("the closed stretch: %+v", spans)
	}
	status := watchDeskCall(bridge, http.MethodGet, "/watch/listening", "", mobileScopeChatWrite)
	if status.Code != http.StatusOK {
		t.Fatalf("read: %d", status.Code)
	}
}

// A start the watch reports from before the off, or from the future, cannot
// open the stretch: audio from before the wearer's own start stays dropped.
func TestTheOffNeverEndsBeforeItBeganOrAfterNow(t *testing.T) {
	app, _ := listenOffFixture(t)
	now := time.Now()
	if err := app.setWatchListeningOff(now.Add(-10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := app.endWatchListeningOff(now.Add(-time.Hour), now); err != nil {
		t.Fatal(err)
	}
	spans, _ := app.watchListenOffSpans()
	if len(spans) != 1 || spans[0].End <= spans[0].Start {
		t.Fatalf("ended before it began: %+v", spans)
	}
	if err := app.setWatchListeningOff(now.Add(-5 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := app.endWatchListeningOff(now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	spans, _ = app.watchListenOffSpans()
	if len(spans) != 2 || spans[1].End > now.UnixMilli() {
		t.Fatalf("ended in the future: %+v", spans)
	}
}

// What the watch recorded after the off is dropped like audio from a pause; a
// recording made on the phone is not touched.
func TestWatchAudioAfterTheOffIsDropped(t *testing.T) {
	app, _ := listenOffFixture(t)
	oldP := mobileRecordingPauses
	t.Cleanup(func() { mobileRecordingPauses = oldP })
	pause := observationPause{Start: 1000, End: 2000}
	mobileRecordingPauses = func(*App) ([]observationPause, error) { return []observationPause{pause}, nil }
	now := time.Now()
	if err := app.setWatchListeningOff(now); err != nil {
		t.Fatal(err)
	}
	watch, err := app.mobileRecordingDropSpans("watch")
	if err != nil || len(watch) != 2 || watch[0] != pause || watch[1].Start != now.UnixMilli() || watch[1].End != 0 {
		t.Fatalf("a watch recording's spans: %+v %v", watch, err)
	}
	phone, err := app.mobileRecordingDropSpans("phone")
	if err != nil || len(phone) != 1 || phone[0] != pause {
		t.Fatalf("a phone recording's spans: %+v %v", phone, err)
	}
	// An hour recorded wholly after the off is wholly covered.
	later := now.Add(10 * time.Minute)
	if cover := mobileRecordingPauseCover(later.UnixMilli(), later.Add(15*time.Minute).UnixMilli(), watch); cover != mobileRecordingPauseFull {
		t.Fatalf("cover of audio after the off: %v", cover)
	}
	if cover := mobileRecordingPauseCover(now.Add(-time.Hour).UnixMilli(), now.Add(-30*time.Minute).UnixMilli(), watch); cover != mobileRecordingPauseNone {
		t.Fatalf("cover of audio before the off: %v", cover)
	}
}

// Settings' Your watch row reads the phone's setting, turns it off as the
// phone does, and fails rather than guessing when the setting can't be read.
func TestSettingsWatchListeningRow(t *testing.T) {
	app, _ := listenOffFixture(t)
	// The fixture holds a recording from the watch.
	view, err := app.GetWatchListening()
	if err != nil || view.Off || !view.HasWatch {
		t.Fatalf("fresh: %+v %v", view, err)
	}
	view, err = app.TurnOffWatchListening()
	if err != nil || !view.Off || view.Since == "" {
		t.Fatalf("turned off: %+v %v", view, err)
	}
	if phone, err := app.watchListening(); err != nil || !phone.Off || phone.Since != view.Since {
		t.Fatalf("phone listening state after desktop off: %+v %v", phone, err)
	}
	if err := app.store.SetUIState(context.Background(), watchListenOffKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.GetWatchListening(); err == nil {
		t.Fatal("an unreadable setting read as a state")
	}
}
