package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
)

type watchForgetFixture struct {
	app              *App
	store            *mobileRecordingStore
	target, neighbor WatchConversation
	recording, path  string
}

func newWatchForgetFixture(t *testing.T, audio bool) watchForgetFixture {
	t.Helper()
	t.Setenv(agentHomeEnv, t.TempDir())
	app, _, store, id := watchDeskFixture(t)
	rec := store.recordings[id]
	start, err := time.Parse(time.RFC3339, rec.Received[0].StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	rec.Received[0].DurationSeconds = 160
	rec.Mime = "audio/wav"
	path := filepath.Join(store.dir, id, rec.Received[0].File)
	if audio {
		writeTestWAV(t, path, 1, 160)
	} else {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		rec.AudioRemoved = true
	}
	db := app.activity.store.db
	if _, err := db.Exec("UPDATE activity_events SET data=json_set(data,'$.speechEndAt',timestamp+1000) WHERE session_id=?", rec.SessionID); err != nil {
		t.Fatal(err)
	}
	at := start.Add(150 * time.Second).UnixMilli()
	evt := activitymon.ActivityEvent{Kind: activitymon.EventSpeech, Timestamp: at, SpeechEndAt: at + 2000, SpeechText: "neighbor's private words", AppName: mobileRecordingAppName}
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO activity_events(session_id,timestamp,kind,data,sort_order) VALUES(?,?,?,?,?)", rec.SessionID, at, evt.Kind, data, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE activity_sessions SET event_count=3,end_time=? WHERE id=?", at+10000, rec.SessionID); err != nil {
		t.Fatal(err)
	}
	rec.Words = 12
	if err := store.saveLocked(); err != nil {
		t.Fatal(err)
	}
	convs, err := app.GetWatchConversations()
	if err != nil || len(convs) != 2 {
		t.Fatalf("fixture conversations %v %v", convs, err)
	}
	names := map[string]watchConversationNameEntry{}
	notes := map[string]watchConversationNotesEntry{}
	for i, c := range convs {
		title := []string{"Neighbor", "Pricing"}[i]
		names[c.ID] = watchConversationNameEntry{Hash: watchConversationHash(c), Name: title, RecordingIDs: c.RecordingIDs}
		loops := app.writeWatchConversationCommitments(c, title, title, meetingFollowupExtraction{Followups: []meetingFollowup{{Text: "Review " + title, Owner: meetingFollowupOwnerMe}}})
		if len(loops) != 1 {
			t.Fatal("fixture commitment missing")
		}
		notes[c.ID] = watchConversationNotesEntry{Hash: watchConversationHash(c), Summary: title + " note", LoopIDs: loops, RecordingIDs: c.RecordingIDs}
	}
	if err := writeWatchConversationNames(store.dir, names); err != nil {
		t.Fatal(err)
	}
	if err := writeWatchConversationNotes(store.dir, notes); err != nil {
		t.Fatal(err)
	}
	convs, err = app.GetWatchConversations()
	if err != nil {
		t.Fatal(err)
	}
	return watchForgetFixture{app, store, convs[1], convs[0], id, path}
}

func TestWatchForgetSharedHour(t *testing.T) {
	for _, audio := range []bool{false, true} {
		t.Run(map[bool]string{false: "transcript and notes", true: "decoded audio"}[audio], func(t *testing.T) {
			if audio {
				bin, err := exec.LookPath("ffmpeg")
				if err != nil {
					t.Skip("ffmpeg is unavailable for the audio assertion")
				}
				orig := mobileRecordingFFmpegPath
				mobileRecordingFFmpegPath = func() string { return bin }
				t.Cleanup(func() { mobileRecordingFFmpegPath = orig })
			}
			f := newWatchForgetFixture(t, audio)
			p, err := f.app.GetWatchConversationForgetPreview(f.target.ID)
			if err != nil {
				t.Fatal(err)
			}
			if p.Count != 3 || p.LineCount != 2 || !p.HasNote || p.Name != "Pricing" || len(p.Followups) != 1 || p.Followups[0].ID != f.target.ItemIDs[0] || p.Followups[0].Title != "Review Pricing" {
				t.Fatalf("preview %+v", p)
			}
			var before []byte
			if audio {
				if !reflect.DeepEqual(p.Audio, []WatchConversationAudioCut{{f.recording, 2000, 10000}}) {
					t.Fatal(p.Audio)
				}
				before, err = os.ReadFile(f.path)
				if err != nil {
					t.Fatal(err)
				}
			} else if len(p.Audio) != 0 {
				t.Fatal(p.Audio)
			}
			if err := f.app.ForgetWatchConversation(f.target.ID); err != nil {
				t.Fatal(err)
			}
			cs, err := f.app.GetWatchConversations()
			if err != nil || len(cs) != 1 || !reflect.DeepEqual(cs[0], f.neighbor) {
				t.Fatalf("neighbor: %+v, %v", cs, err)
			}
			loops, err := NewPurposeMapStore(f.app.activity.store.db).ListDetailsAll()
			if err != nil || len(loops) != 1 || watchFollowupItemPrefix+loops[0].OpenLoop.ID != f.neighbor.ItemIDs[0] {
				t.Fatalf("remaining commitments: %v %v", loops, err)
			}
			if audio {
				length, err := decodedAudioSeconds(context.Background(), mobileRecordingFFmpegPath(), f.path)
				if err != nil || length != 152 {
					t.Fatalf("remaining seconds %f, %v", length, err)
				}
				after, err := os.ReadFile(f.path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after[44:], append(before[44:44+64000:44+64000], before[44+320000:]...)) {
					t.Fatal("retained PCM samples changed")
				}
			}
		})
	}
}

func TestWatchForgetAcrossHours(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	app, _, store, nine, ten := samTalkFixture(t)
	cs, err := app.GetWatchConversations()
	if err != nil || len(cs) != 2 {
		t.Fatalf("%v %v", cs, err)
	}
	target := cs[1]
	p, err := app.GetWatchConversationForgetPreview(target.ID)
	if err != nil || len(p.Audio) != 2 || p.Count != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if err := app.ForgetWatchConversation(target.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{nine, ten} {
		rec, ok := store.get(id, "")
		if !ok || rec.State != mobileRecordingDeleted {
			t.Fatal(id)
		}
	}
	cs2, err := app.GetWatchConversations()
	if err != nil || len(cs2) != 1 || !reflect.DeepEqual(cs2[0], cs[0]) {
		t.Fatalf("%+v %v", cs2, err)
	}
}

func TestWatchForgetStaleAndFailedRead(t *testing.T) {
	f := newWatchForgetFixture(t, false)
	if err := f.app.ForgetWatchConversation("stale"); !errors.Is(err, errWatchConversationGone) {
		t.Fatal(err)
	}
	if _, err := f.app.activity.store.db.Exec("UPDATE activity_events SET data='{' WHERE session_id=?", "rec-"+f.recording); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.GetWatchConversationForgetPreview(f.target.ID); err == nil || errors.Is(err, errWatchConversationGone) {
		t.Fatal(err)
	}
	if err := f.app.ForgetWatchConversation(f.target.ID); err == nil || errors.Is(err, errWatchConversationGone) {
		t.Fatal(err)
	}
	if len(readWatchConversationNotes(f.store.dir)) != 2 {
		t.Fatal("failed read pruned notes")
	}
}

func TestWatchForgetCrashRecovery(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	app, _, store, id := watchDeskFixture(t)
	cs, err := app.GetWatchConversations()
	if err != nil || len(cs) != 1 {
		t.Fatal(err)
	}
	p, err := app.watchForgetPlan(cs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	path := store.partPaths(p.hours[0].recording)[0]
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.commitWatchCut(store, p, func() bool { return true }); !errors.Is(err, errWatchCutInterrupted) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.dir, watchForgetJournal, "journal.json")); err != nil {
		t.Fatal(err)
	}
	reopened := newMobileRecordingStore(filepath.Dir(store.dir))
	reopened.cutDB = app.activity.store.db
	entries, err := reopened.all()
	if err != nil || len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("%+v %v", entries, err)
	}
	restored, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, restored) {
		t.Fatal("original audio was not restored", err)
	}
	if err := app.ForgetWatchConversation(cs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestWatchForgetAmbiguousClaim(t *testing.T) {
	f := newWatchForgetFixture(t, false)
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	session := "rec-" + f.recording
	claim := renderOKFDocument(map[string]any{"type": "memory", "title": "Pricing owner", "evidence_sessions": []string{session}}, "A claim")
	if err := os.WriteFile(filepath.Join(dir, "pricing.md"), []byte(claim), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.GetWatchConversationForgetPreview(f.target.ID); err == nil {
		t.Fatal("shared session attributed to one conversation")
	}
	if err := f.app.ForgetWatchConversation(f.target.ID); err == nil {
		t.Fatal("forgot ambiguous source")
	}
	cs, err := f.app.GetWatchConversations()
	if err != nil || len(cs) != 2 {
		t.Fatal(cs, err)
	}
}

func TestWatchForgetChangedConfirmation(t *testing.T) {
	f := newWatchForgetFixture(t, false)
	if _, err := f.app.GetWatchConversationForgetPreview(f.target.ID); err != nil {
		t.Fatal(err)
	}
	notes := readWatchConversationNotes(f.store.dir)
	n := notes[f.target.ID]
	n.Summary = "A new note"
	notes[f.target.ID] = n
	if err := writeWatchConversationNotes(f.store.dir, notes); err != nil {
		t.Fatal(err)
	}
	if err := f.app.ForgetWatchConversation(f.target.ID); err == nil {
		t.Fatal("accepted stale confirmation")
	}
	cs, err := f.app.GetWatchConversations()
	if err != nil || len(cs) != 2 {
		t.Fatal(cs, err)
	}
}

func TestWatchForgetWholeHour(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	app, _, store, id := watchDeskFixture(t)
	cs, err := app.GetWatchConversations()
	if err != nil || len(cs) != 1 {
		t.Fatal(cs, err)
	}
	rec, ok := store.get(id, "")
	if !ok {
		t.Fatal("recording missing")
	}
	path := store.partPaths(rec)[0]
	if err := app.ForgetWatchConversation(cs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("audio survived", err)
	}
	tombstone, ok := store.get(id, "")
	if !ok || tombstone.State != mobileRecordingDeleted || tombstone.SessionID != "" || !tombstone.AudioRemoved {
		t.Fatal(tombstone)
	}
	var sessions int
	if err := app.activity.store.db.QueryRow("SELECT COUNT(*) FROM activity_sessions WHERE id=?", rec.SessionID).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal(sessions, err)
	}
}

func TestWatchForgetUnavailableActivityStore(t *testing.T) {
	f := newWatchForgetFixture(t, false)
	if err := f.app.activity.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.GetWatchConversations(); err == nil {
		t.Fatal("desktop list hid closed database")
	}
	if _, err := f.app.GetWatchLane(); err == nil {
		t.Fatal("watch lane hid closed database")
	}
	if _, _, _, err := f.app.mobileConversationsFor(f.store, mobileTestDeviceID, time.Now()); err == nil {
		t.Fatal("phone list hid closed database")
	}
	if _, err := f.app.GetWatchConversationForgetPreview(f.target.ID); err == nil || errors.Is(err, errWatchConversationGone) {
		t.Fatal("unavailable database reported as a successful read or missing conversation", err)
	}
	if len(readWatchConversationNotes(f.store.dir)) != 2 {
		t.Fatal("notes pruned after database closed")
	}
}

func TestWatchSpeechDisplaySkipsDamagedRows(t *testing.T) {
	for _, tc := range []struct{ name, blob string }{
		{"missing session", ""},
		{"malformed JSON", "{"},
		{"invalid timestamp", `{"kind":"speech","timestamp":0,"speechText":"unusable"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(agentHomeEnv, t.TempDir())
			app, _, store, id := watchDeskFixture(t)
			rec := store.recordings[id]
			cs, err := app.GetWatchConversations()
			if err != nil || len(cs) != 1 {
				t.Fatalf("fixture: %+v, %v", cs, err)
			}
			original := cs[0]
			if tc.blob == "" {
				// Simulate an index entry surviving cleanup of its session.
				orphan := *rec
				orphan.ID = "fc29935d-1962-493d-a9bb-f36ffc286db9"
				orphan.SessionID = "rec-" + orphan.ID
				store.recordings[orphan.ID] = &orphan
				if err := store.saveLocked(); err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := app.activity.store.db.Exec("INSERT INTO activity_events(session_id,timestamp,kind,data,sort_order) VALUES(?,?,?,?,?)", rec.SessionID, original.StartAt, "speech", tc.blob, 2)
				if err != nil {
					t.Fatal(err)
				}
			}
			cs, err = app.GetWatchConversations()
			if err != nil || len(cs) != 1 || !reflect.DeepEqual(cs[0], original) {
				t.Fatalf("desktop list: %+v, %v", cs, err)
			}
			if _, err := app.GetWatchLane(); err != nil {
				t.Fatal("watch lane:", err)
			}
			view, _, _, err := app.mobileConversationsFor(store, rec.DeviceID, time.Now())
			if err != nil || len(view.Conversations) != 1 || view.Conversations[0].ID != original.ID {
				t.Fatalf("phone list: %+v, %v", view, err)
			}
			if _, err := app.GetWatchConversationForgetPreview(original.ID); err == nil || errors.Is(err, errWatchConversationGone) {
				t.Fatal("strict preview:", err)
			}
			if err := app.ForgetWatchConversation(original.ID); err == nil || errors.Is(err, errWatchConversationGone) {
				t.Fatal("strict cut:", err)
			}
			if _, err := os.Stat(store.partPaths(*rec)[0]); err != nil {
				t.Fatal("recording should survive refused cut:", err)
			}
		})
	}
}

func TestWatchCutLoadUnpublishedDirectory(t *testing.T) {
	t.Setenv(agentHomeEnv, t.TempDir())
	_, _, store, id := watchDeskFixture(t)
	for _, name := range []string{watchForgetJournal, ".staging-conversation-cut", ".retired-conversation-cut"} {
		dir := filepath.Join(store.dir, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "old0"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reopened := newMobileRecordingStore(filepath.Dir(store.dir))
	recs, err := reopened.all()
	if err != nil || len(recs) != 1 || recs[0].ID != id {
		t.Fatalf("reopened recordings: %+v, %v", recs, err)
	}
	for _, name := range []string{watchForgetJournal, ".staging-conversation-cut", ".retired-conversation-cut"} {
		if _, err := os.Stat(filepath.Join(store.dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived: %v", name, err)
		}
	}
	if _, err := os.Stat(reopened.partPaths(recs[0])[0]); err != nil {
		t.Fatal(err)
	}
}

func TestWatchCutLoadWithoutCutState(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	// A first load works with no activity DB and creates no recording or
	// recovery directory. A cached load needs no disk access at all.
	if recs, err := store.all(); err != nil || len(recs) != 0 {
		t.Fatalf("empty store: %+v, %v", recs, err)
	}
	if _, err := os.Stat(store.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.dir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.all(); err != nil {
		t.Fatal("cached load touched disk:", err)
	}
}
