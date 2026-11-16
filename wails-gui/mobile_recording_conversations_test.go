package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/storage"
)

func phoneConversations(t *testing.T, bridge *mobileBridge) MobileConversationsView {
	t.Helper()
	got := watchDeskCall(bridge, http.MethodGet, "/recordings/conversations", "", mobileScopeChatWrite, mobileScopeBriefRead)
	if got.Code != http.StatusOK {
		t.Fatalf("conversations: %d %s", got.Code, got.Body.String())
	}
	var envelope struct {
		Data MobileConversationsView `json:"data"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

// The phone's Watch tab reads what the watch heard as conversations: who
// spoke, the marks, where it was, and today's hours.
func TestPhoneReadsItsConversationsWithMarksPlaceAndHours(t *testing.T) {
	// The fixture's own hour, an hour ago, carries a place, a heart rate, and two lines (you, then speaker_1).
	app, bridge, store, _ := watchDeskFixture(t)

	// A second hour, three hours ago, with a mark on the owner's words.
	uploads := &mapUploads{bodies: map[string][]byte{}}
	marked := uuid.NewString()
	earlier := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Second)
	req := sealedRecordingPart(t, uploads, mobileTestDeviceID, marked, 0, true, []byte("audio"), earlier)
	req.DurationSeconds = 600
	req.Marks = []float64{35}
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, mobileTestDeviceID, req); err != nil {
		t.Fatal(err)
	}
	saveWatchSpeech(t, app, store, marked, earlier, []activitymonSpeech{{20, "speaker_2", "What is the number"}, {30, "you", "Fifteen percent, not ten"}})

	if got := watchDeskCall(bridge, http.MethodGet, "/recordings/conversations", "", mobileScopeChatWrite); got.Code != http.StatusForbidden {
		t.Fatalf("conversations without brief:read: %d", got.Code)
	}
	view := phoneConversations(t, bridge)
	if len(view.Conversations) != 2 {
		t.Fatalf("conversations %+v", view.Conversations)
	}
	newest, older := view.Conversations[0], view.Conversations[1]
	if newest.Place != "Mission District" || newest.HeartRate != "raised" || len(newest.Marks) != 0 {
		t.Fatalf("the newest conversation: %+v", newest)
	}
	if len(newest.Speakers) != 2 || newest.Speakers[0] != "you" {
		t.Fatalf("speakers, you first: %v", newest.Speakers)
	}
	if len(older.Marks) != 1 || len(older.RecordingIDs) != 1 || older.RecordingIDs[0] != marked || older.Place != "" {
		t.Fatalf("the marked conversation: %+v", older)
	}
	if older.Decisions == nil || older.Followups == nil {
		t.Fatal("empty notes must be empty lists, not null")
	}
	if view.DayStart == "" {
		t.Fatal("no day start")
	}

	// Its words, with the mark on the line it points at.
	detail := watchDeskCall(bridge, http.MethodGet, "/recordings/conversation?id="+url.QueryEscape(older.ID), "", mobileScopeChatWrite, mobileScopeBriefRead)
	var envelope struct {
		Data MobileConversationDetail `json:"data"`
	}
	if detail.Code != http.StatusOK || json.Unmarshal(detail.Body.Bytes(), &envelope) != nil {
		t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
	}
	lines := envelope.Data.Lines
	if len(lines) != 2 || lines[0].Marked || !lines[1].Marked || lines[1].Speaker != "you" {
		t.Fatalf("lines %+v", lines)
	}
	if envelope.Data.Conversation.ID != older.ID {
		t.Fatalf("detail entry %+v", envelope.Data.Conversation)
	}
	if got := watchDeskCall(bridge, http.MethodGet, "/recordings/conversation?id="+url.QueryEscape("someone-else:1"), "", mobileScopeChatWrite, mobileScopeBriefRead); got.Code != http.StatusNotFound {
		t.Fatalf("a conversation that is not this phone's: %d", got.Code)
	}

	status, _ := json.Marshal(bridge.statusWithExtras(mobileStatusView{}))
	if !strings.Contains(string(status), `"recordingConversations":true`) {
		t.Fatalf("the status flag is not written: %s", status)
	}

	// Deleted means deleted: the hour takes its conversation with it.
	if _, err := app.deleteWatchRecording(store, marked, ""); err != nil {
		t.Fatal(err)
	}
	if after := phoneConversations(t, bridge); len(after.Conversations) != 1 || after.Conversations[0].ID != newest.ID {
		t.Fatalf("after the delete: %+v", after.Conversations)
	}

	// Closed Eyes: nothing that was heard is read out.
	app.closedEyes.enabled.Store(true)
	defer app.closedEyes.enabled.Store(false)
	if paused := phoneConversations(t, bridge); len(paused.Conversations) != 0 || len(paused.Hours) != 0 {
		t.Fatalf("read out while paused: %+v", paused)
	}
	if got := watchDeskCall(bridge, http.MethodGet, "/recordings/conversation?id="+url.QueryEscape(newest.ID), "", mobileScopeChatWrite, mobileScopeBriefRead); got.Code != http.StatusNotFound {
		t.Fatalf("words read out while paused: %d", got.Code)
	}
}

// Another phone paired with the same computer reads none of this phone's talk.
func TestAnotherPhoneReadsNoneOfTheseConversations(t *testing.T) {
	app, _, store, _ := watchDeskFixture(t)
	mine, _, _, err := app.mobileConversationsFor(store, mobileTestDeviceID, time.Now())
	if err != nil || len(mine.Conversations) != 1 {
		t.Fatalf("own conversations %+v %v", mine.Conversations, err)
	}
	other, others, _, err := app.mobileConversationsFor(store, "another-device", time.Now())
	if err != nil || len(other.Conversations) != 0 || len(others) != 0 || len(other.Hours) != 0 {
		t.Fatalf("another device read %+v %+v", other.Conversations, other.Hours)
	}
}

// "Notes for long talks" is the person's own setting once they choose; until
// then the build switch decides, as before.
func TestNotesForLongTalksIsThePersonsSetting(t *testing.T) {
	app, bridge, _, _ := watchDeskFixture(t)
	state, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	app.store = state

	t.Setenv(featureSwitchesEnv, "")
	if got := app.watchNotesSetting(); got.On || got.Chosen || app.watchNotesOn() {
		t.Fatalf("with no choice and no switch: %+v", got)
	}
	t.Setenv(featureSwitchesEnv, string(featureWatchNotes))
	if got := app.watchNotesSetting(); !got.On || got.Chosen {
		t.Fatalf("with no choice the build switch decides: %+v", got)
	}

	if got := watchDeskCall(bridge, http.MethodPost, "/settings/watch-notes", `{"on":false}`, mobileScopeBriefRead); got.Code != http.StatusForbidden {
		t.Fatalf("set without chat:write: %d", got.Code)
	}
	if got := watchDeskCall(bridge, http.MethodPost, "/settings/watch-notes", `{}`, mobileScopeChatWrite); got.Code != http.StatusBadRequest {
		t.Fatalf("set without a value: %d", got.Code)
	}
	if got := watchDeskCall(bridge, http.MethodPost, "/settings/watch-notes", `{"on":false}`, mobileScopeChatWrite); got.Code != http.StatusOK {
		t.Fatalf("set: %d %s", got.Code, got.Body.String())
	}
	if got := app.watchNotesSetting(); got.On || !got.Chosen {
		t.Fatalf("the person's off must win over the build switch: %+v", got)
	}
	t.Setenv(featureSwitchesEnv, "")
	if got := watchDeskCall(bridge, http.MethodPost, "/settings/watch-notes", `{"on":true}`, mobileScopeChatWrite); got.Code != http.StatusOK {
		t.Fatalf("set on: %d", got.Code)
	}
	if got := app.watchNotesSetting(); !got.On || !got.Chosen {
		t.Fatalf("the person's on must win with the build switch off: %+v", got)
	}
	read := watchDeskCall(bridge, http.MethodGet, "/settings/watch-notes", "", mobileScopeChatWrite)
	var envelope struct {
		Data MobileWatchNotesSetting `json:"data"`
	}
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &envelope) != nil || !envelope.Data.On || !envelope.Data.Chosen {
		t.Fatalf("read: %d %s", read.Code, read.Body.String())
	}
}
