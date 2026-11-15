package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/platform/activitymon"
)

// watchDeskFixture is an app with a real activity store, a bridge with a
// recording store, and one transcribed recording of two voices.
func watchDeskFixture(t *testing.T) (*App, *mobileBridge, *mobileRecordingStore, string) {
	t.Helper()
	activity, err := NewActivityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { activity.Close() })
	app := &App{activity: &activityMonitorState{store: activity}}
	bridge := &mobileBridge{stateDir: t.TempDir(), app: app}
	app.mobileBridge = bridge
	bridge.mu.Lock()
	store := bridge.recordingStoreLocked()
	bridge.mu.Unlock()

	uploads := &mapUploads{bodies: map[string][]byte{}}
	id := uuid.NewString()
	started := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	req := sealedRecordingPart(t, uploads, mobileTestDeviceID, id, 0, true, []byte("audio"), started)
	req.Context = &MobileRecordingPartContext{
		HeartRate: &MobileRecordingHeartRate{Avg: 104, Min: 88, Max: 131, Samples: 12},
		Location:  &MobileRecordingLocation{Latitude: 37.7599, Longitude: -122.4148, Place: "Mission District"},
	}
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, mobileTestDeviceID, req); err != nil {
		t.Fatal(err)
	}
	events := []activitymon.ActivityEvent{
		{Timestamp: started.Add(2 * time.Second).UnixMilli(), Kind: activitymon.EventSpeech, AppName: mobileRecordingAppName,
			SpeechText: "Call Sam about the launch", SpeechSpeakerLabel: mobileRecordingSpeakerOwner},
		{Timestamp: started.Add(9 * time.Second).UnixMilli(), Kind: activitymon.EventSpeech, AppName: mobileRecordingAppName,
			SpeechText: "Friday works for me", SpeechSpeakerLabel: mobileRecordingSpeakerPrefix + "1"},
	}
	session := ActivitySessionRow{ID: "rec-" + id, StartTime: started.UnixMilli(), EndTime: started.Add(time.Minute).UnixMilli(),
		TriggerKind: mobileRecordingTrigger, AppNames: `["` + mobileRecordingAppName + `"]`, EventCount: len(events),
		TriageStatus: "pending", CreatedAt: time.Now().UnixMilli()}
	if err := activity.SaveSession(session, events, nil); err != nil {
		t.Fatal(err)
	}
	store.setState(id, mobileRecordingTranscribed, session.ID, 8, "")
	store.setOwnerVoice(id, mobileOwnerVoiceLabelled)
	return app, bridge, store, id
}

func TestWatchRecordingsListShowsPartsContextAndWhoSpoke(t *testing.T) {
	app, _, _, id := watchDeskFixture(t)
	list, err := app.GetWatchRecordings()
	if err != nil || len(list) != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
	rec := list[0]
	if rec.ID != id || rec.State != mobileRecordingTranscribed || !rec.HasAudio || rec.SessionID != "rec-"+id || rec.Words != 8 || len(rec.Parts) != 1 {
		t.Fatalf("recording %+v", rec)
	}
	part := rec.Parts[0]
	if part.Place != "Mission District" || part.HeartRate != "raised" || part.HeartRateAvg != 104 || part.HeartRateMax != 131 || !part.HasAudio {
		t.Fatalf("part %+v", part)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "37.7") || strings.Contains(string(raw), "122.4") {
		t.Fatalf("the list carries coordinates: %s", raw)
	}
	lines, err := app.GetWatchRecordingTranscript(id)
	if err != nil || len(lines) != 2 || lines[0].Speaker != "you" || lines[1].Speaker != "speaker_1" || lines[1].Text != "Friday works for me" {
		t.Fatalf("lines %+v %v", lines, err)
	}
}

func TestWatchRecordingAudioIsServedByPartAndNeverByPath(t *testing.T) {
	app, _, _, id := watchDeskFixture(t)
	app.bridge = &hostBridge{}
	get := func(query string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		app.handleActivityRecordingAudio(rec, httptest.NewRequest(http.MethodGet, "/api/activity/recording?"+query, nil))
		return rec
	}
	if got := get("id=" + id + "&part=0"); got.Code != http.StatusOK || got.Body.String() != "audio" || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("part 0: %d %q", got.Code, got.Body.String())
	}
	for _, query := range []string{"id=" + id + "&part=1", "id=" + uuid.NewString() + "&part=0", "id=../x&part=0", "id=" + id} {
		if got := get(query); got.Code == http.StatusOK {
			t.Fatalf("%s was served", query)
		}
	}
}

func TestDeletingAWatchRecordingRemovesAudioContextAndTranscript(t *testing.T) {
	app, _, store, id := watchDeskFixture(t)
	if err := app.DeleteWatchRecording(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.dir, id)); !os.IsNotExist(err) {
		t.Fatalf("audio is still on disk: %v", err)
	}
	if events, _ := app.activity.store.GetSessionEventsIncludingProtected("rec-" + id); len(events) != 0 {
		t.Fatalf("the transcript is still in Activity: %+v", events)
	}
	if list, _ := app.GetWatchRecordings(); len(list) != 0 {
		t.Fatalf("a deleted recording is still listed: %+v", list)
	}
	if _, err := app.GetWatchRecordingTranscript(id); err == nil {
		t.Fatal("a deleted recording still has a transcript")
	}
	raw, _ := os.ReadFile(filepath.Join(store.dir, "index.json"))
	if strings.Contains(string(raw), "Mission") || strings.Contains(string(raw), "heartRate") {
		t.Fatalf("the index kept context of a deleted recording: %s", raw)
	}
	// An older phone does not hear about it; one that asks does.
	if views, _ := store.list(mobileTestDeviceID, []string{id}, 50, false); len(views) != 0 {
		t.Fatalf("views %+v", views)
	}
	if views, _ := store.list(mobileTestDeviceID, []string{id}, 50, true); len(views) != 1 || views[0].State != mobileRecordingDeleted {
		t.Fatalf("views %+v", views)
	}
	// A part sent again is answered and never stored.
	uploads := &mapUploads{bodies: map[string][]byte{}}
	again := sealedRecordingPart(t, uploads, mobileTestDeviceID, id, 0, true, []byte("audio"), time.Now())
	view, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, mobileTestDeviceID, again)
	if err != nil || view.State != mobileRecordingDeleted {
		t.Fatalf("resent part: %+v %v", view, err)
	}
	if _, err := os.Stat(filepath.Join(store.dir, id)); !os.IsNotExist(err) {
		t.Fatalf("a resent part brought the audio back: %v", err)
	}
	// A transcription that finishes late cannot bring it back.
	if store.setState(id, mobileRecordingTranscribed, "rec-"+id, 8, "") {
		t.Fatal("a deleted recording was moved to transcribed")
	}
	if len(store.pending()) != 0 {
		t.Fatal("a deleted recording is pending")
	}
	if err := app.DeleteWatchRecording(id); err != nil {
		t.Fatalf("a second delete: %v", err)
	}
}

func watchDeskCall(bridge *mobileBridge, method, path, body string, scopes ...string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, mobileAPIPrefix+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	bridge.handleAuthorized(recorder, req, mobileTestDevice(scopes...))
	return recorder
}

func TestPhoneReadsATranscriptAndDeletesEverywhere(t *testing.T) {
	_, bridge, store, id := watchDeskFixture(t)
	if got := watchDeskCall(bridge, http.MethodGet, "/recordings/transcript?id="+id, "", mobileScopeChatWrite); got.Code != http.StatusForbidden {
		t.Fatalf("transcript without brief:read: %d", got.Code)
	}
	if got := watchDeskCall(bridge, http.MethodPost, "/recordings/delete", `{"recordingId":"`+id+`"}`, mobileScopeBriefRead); got.Code != http.StatusForbidden {
		t.Fatalf("delete without chat:write: %d", got.Code)
	}
	got := watchDeskCall(bridge, http.MethodGet, "/recordings/transcript?id="+id, "", mobileScopeChatWrite, mobileScopeBriefRead)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"speaker":"you"`) || !strings.Contains(got.Body.String(), "Friday works for me") {
		t.Fatalf("transcript: %d %s", got.Code, got.Body.String())
	}
	if got := watchDeskCall(bridge, http.MethodGet, "/recordings/transcript?id="+uuid.NewString(), "", mobileScopeChatWrite, mobileScopeBriefRead); got.Code != http.StatusNotFound {
		t.Fatalf("unknown transcript: %d", got.Code)
	}
	// Another device's recording is not found.
	if _, ok := store.get(id, "another-device"); ok {
		t.Fatal("another device read the recording")
	}
	got = watchDeskCall(bridge, http.MethodPost, "/recordings/delete", `{"recordingId":"`+id+`"}`, mobileScopeChatWrite)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"state":"deleted"`) {
		t.Fatalf("delete: %d %s", got.Code, got.Body.String())
	}
	if got := watchDeskCall(bridge, http.MethodGet, "/recordings/transcript?id="+id, "", mobileScopeChatWrite, mobileScopeBriefRead); got.Code != http.StatusNotFound {
		t.Fatalf("transcript after delete: %d", got.Code)
	}
	if got := watchDeskCall(bridge, http.MethodGet, "/recordings?deleted=1&ids="+id, "", mobileScopeChatWrite); !strings.Contains(got.Body.String(), `"state":"deleted"`) {
		t.Fatalf("list with deleted: %s", got.Body.String())
	}
	if got := watchDeskCall(bridge, http.MethodGet, "/recordings?ids="+id, "", mobileScopeChatWrite); strings.Contains(got.Body.String(), id) {
		t.Fatalf("list without deleted: %s", got.Body.String())
	}
	raw, _ := json.Marshal(mobileStatusView{RecordingDetail: true})
	if !strings.Contains(string(raw), `"recordingDetail":true`) {
		t.Fatalf("status %s", raw)
	}
}
