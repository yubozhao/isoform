package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// newMeetingNotesTestApp is an app with both stores and a temporary home.
func newMeetingNotesTestApp(t *testing.T) *App {
	t.Helper()
	app := newPrivacyReadTestApp(t)
	prior := pii.GetUserSensitiveApps()
	t.Cleanup(func() { pii.SetUserSensitiveApps(prior) })
	pii.SetUserSensitiveApps(nil)
	return app
}

// saveMeetingSlice stores a captured meeting session with a transcript file
// and returns its row.
func saveMeetingSlice(t *testing.T, app *App, id string, start time.Time, minutes int, transcript string) ActivitySessionRow {
	t.Helper()
	store := app.activity.store
	path := ""
	if transcript != "" {
		dir := filepath.Join(os.Getenv("HOME"), "transcripts")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(dir, id+".md")
		if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	row := ActivitySessionRow{
		ID: id, StartTime: start.UnixMilli(), EndTime: start.Add(time.Duration(minutes) * time.Minute).UnixMilli(),
		TriggerKind: "meeting_ended", AppNames: `["Halcyon Meet"]`, TriageStatus: "dismissed", IsMeeting: true,
		MeetingApp: "Halcyon Meet", TranscriptPath: path, CreatedAt: start.UnixMilli(),
	}
	if err := store.SaveSession(row, nil, nil); err != nil {
		t.Fatal(err)
	}
	if path != "" {
		if err := store.UpdateMeetingStatus(id, "Halcyon Meet", path); err != nil {
			t.Fatal(err)
		}
	}
	return row
}

func meetingTranscriptFixture(session ActivitySessionRow, meetingID string, lines ...string) string {
	start := time.UnixMilli(session.StartTime)
	md := "# Meeting Transcript: Halcyon Meet\n\n- **Date**: " + start.Format("2006-01-02 15:04") + "\n- **Duration**: 10m 0s\n- **App**: Halcyon Meet\n"
	if meetingID != "" {
		md += "- **Meeting**: " + meetingID + "\n"
	}
	md += "- **Session**: " + session.ID + "\n\n## Transcript\n\n"
	for _, line := range lines {
		md += line + "\n\n"
	}
	return md
}

var studioOutline = meetingFollowupExtraction{
	Summary:   "We reviewed the studio floor plan and kept the welcome tour.",
	Decisions: []string{"Keep the welcome tour"},
	Followups: []meetingFollowup{
		{Text: "Send the revised floor plan", Owner: meetingFollowupOwnerMe, Due: "Tuesday"},
		{Text: "Check the welcome copy", Owner: meetingFollowupOwnerOther, Person: "Maya Chen"},
	},
}

var pricingOutline = studioOutline

// learnStub records learning passes instead of calling models.
type learnStub struct {
	calls  []sessionGroup
	failed bool // the next passes report a model error
}

func stubMeetingNotesLearn(t *testing.T) *learnStub {
	t.Helper()
	prior := meetingNotesLearn
	stub := &learnStub{}
	meetingNotesLearn = func(_ *ActivityTriager, _ context.Context, group sessionGroup, text string, _ SessionContentProfile, _ triageFeatureFlags) bool {
		group.entrySeed += "|" + text
		stub.calls = append(stub.calls, group)
		return !stub.failed
	}
	t.Cleanup(func() { meetingNotesLearn = prior })
	return stub
}

func TestMeetingNotesAreStoredWithTheMeetingsActivity(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	store := app.activity.store
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Minute)
	first := saveMeetingSlice(t, app, "slice-one-1111", start, 10, "")
	second := saveMeetingSlice(t, app, "slice-two-2222", start.Add(15*time.Minute), 20, "")

	key, err := app.recordMeetingNotes(first, "Halcyon Meet", 400, "meeting-A", "Halcyon studio review", &studioOutline)
	if err != nil || key != "meeting-A" {
		t.Fatalf("record first slice: key=%q err=%v", key, err)
	}
	// A rejoined call joins the same meeting; its own outline stays its own.
	if _, err := app.recordMeetingNotes(second, "Halcyon Meet", 800, "meeting-A", "", &meetingFollowupExtraction{Summary: "We agreed on the opening date.", Decisions: []string{"Keep the welcome tour", "Open on the 24th"}}); err != nil {
		t.Fatal(err)
	}

	notes, err := app.GetMeetingNotesForSessions([]string{second.ID})
	if err != nil || len(notes) != 1 {
		t.Fatalf("notes for the second slice = %+v, %v", notes, err)
	}
	got := notes[0]
	if got.MeetingKey != "meeting-A" || got.Title != "Halcyon studio review" || got.MeetingApp != "Halcyon Meet" {
		t.Fatalf("meeting header = %+v", got)
	}
	if got.StartTime != first.StartTime || got.EndTime != second.EndTime {
		t.Fatalf("span = %d..%d, want %d..%d", got.StartTime, got.EndTime, first.StartTime, second.EndTime)
	}
	if got.Summary != "We reviewed the studio floor plan and kept the welcome tour.\n\nWe agreed on the opening date." {
		t.Fatalf("summary = %q", got.Summary)
	}
	if strings.Join(got.Decisions, "|") != "Keep the welcome tour|Open on the 24th" {
		t.Fatalf("decisions = %v", got.Decisions)
	}
	if len(got.Followups) != 2 || got.Followups[0].Owner != "me" || got.Followups[1].Person != "Maya Chen" {
		t.Fatalf("follow-ups = %+v", got.Followups)
	}
	if len(got.Parts) != 2 || got.Parts[0].SessionID != first.ID || got.Parts[1].WordCount != 800 {
		t.Fatalf("parts = %+v", got.Parts)
	}

	// The same slice again rewrites it, never adds one.
	if _, err := app.recordMeetingNotes(first, "Halcyon Meet", 410, "meeting-A", "", &studioOutline); err != nil {
		t.Fatal(err)
	}
	r, _, _ := store.meetingNotesByKey("meeting-A")
	if len(r.Slices) != 2 || r.Slices[0].WordCount != 410 {
		t.Fatalf("slices after a retry = %+v", r.Slices)
	}

	// Nothing is written to Knowledge.
	dir, _ := knowledgeDir()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			t.Fatalf("meeting notes became a Knowledge entry: %s", e.Name())
		}
	}

	// A call without a meeting id is keyed by its session.
	solo := saveMeetingSlice(t, app, "solo-3333", start.Add(time.Hour), 5, "")
	if key, _ := app.recordMeetingNotes(solo, "Halcyon Meet", 10, "", "", nil); key != solo.ID {
		t.Fatalf("solo key = %q", key)
	}
}

func TestMeetingNotesFallBackToTheStoredOutline(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	start := time.Now().Add(-time.Hour)
	session := saveMeetingSlice(t, app, "recovered-4444", start, 10, "")
	decisions, _ := json.Marshal([]string{"Ship it"})
	followups, _ := json.Marshal([]meetingFollowup{{Text: "Write the post", Owner: "me"}})
	if err := app.store.SaveMeetingFollowup(context.Background(), storage.MeetingFollowupRow{SessionID: session.ID, Title: "Launch sync", DecisionsJSON: string(decisions), FollowupsJSON: string(followups)}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.recordMeetingNotes(session, "Halcyon Meet", 120, "", "", nil); err != nil {
		t.Fatal(err)
	}
	notes, _ := app.GetMeetingNotesForSessions([]string{session.ID})
	if len(notes) != 1 || notes[0].Title != "Launch sync" || len(notes[0].Decisions) != 1 || notes[0].Followups[0].Text != "Write the post" {
		t.Fatalf("recovered notes = %+v", notes)
	}
}

func TestMeetingNotesPassTheTranscriptPrivacyGates(t *testing.T) {
	cases := []struct {
		name  string
		apply func(t *testing.T, app *App, first, second ActivitySessionRow)
	}{
		{"closed eyes", func(t *testing.T, app *App, _, _ ActivitySessionRow) { app.closedEyes.enabled.Store(true) }},
		{"one slice protected", func(t *testing.T, app *App, _, second ActivitySessionRow) {
			if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, second.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"sensitive app", func(t *testing.T, _ *App, _, _ ActivitySessionRow) {
			pii.SetUserSensitiveApps(map[string]string{"us.halcyon.xos": "Halcyon Meet"})
		}},
		{"observation pause", func(t *testing.T, app *App, first, _ ActivitySessionRow) {
			app.saveObservationPauses([]observationPause{{Start: first.StartTime + 1000, End: first.StartTime + 2000}})
		}},
		{"slice with a deletion receipt", func(t *testing.T, app *App, first, _ ActivitySessionRow) {
			// Deleting a session removes its slice; a slice row left behind
			// (a crash between the two) is still denied by the private
			// receipt the deletion wrote.
			if err := app.activity.store.DeleteSession(first.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := app.activity.store.db.Exec(`INSERT INTO meeting_note_slices (session_id, meeting_key, updated_at) VALUES (?, 'meeting-P', 1)`, first.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newMeetingNotesTestApp(t)
			stubMeetingNotesLearn(t)
			start := time.Now().Add(-3 * time.Hour)
			first := saveMeetingSlice(t, app, "gate-first-1", start, 10, "transcript")
			second := saveMeetingSlice(t, app, "gate-second-2", start.Add(20*time.Minute), 10, "transcript")
			for _, s := range []ActivitySessionRow{first, second} {
				if _, err := app.recordMeetingNotes(s, "Halcyon Meet", 50, "meeting-P", "", &studioOutline); err != nil {
					t.Fatal(err)
				}
			}
			if notes, _ := app.GetMeetingNotesForSessions([]string{first.ID, second.ID}); len(notes) != 1 {
				t.Fatalf("readable before the gate: %+v", notes)
			}
			if text, err := app.GetMeetingNotesTranscript(second.ID); err != nil || text != "transcript" {
				t.Fatalf("transcript before the gate: %q %v", text, err)
			}
			tc.apply(t, app, first, second)
			notes, err := app.GetMeetingNotesForSessions([]string{first.ID, second.ID})
			if err != nil || len(notes) != 0 {
				t.Fatalf("notes after %s = %+v, %v", tc.name, notes, err)
			}
			if _, err := app.meetingRecordingPath(second.ID); err == nil {
				t.Fatalf("recording readable after %s", tc.name)
			}
			if text, err := app.GetMeetingNotesTranscript(second.ID); err == nil || text != "" {
				t.Fatalf("transcript readable after %s", tc.name)
			}
		})
	}
}

func TestMeetingNotesWithoutCaptureObservePauses(t *testing.T) {
	// A legacy slice whose row and receipt are both gone carries no time of
	// its own provenance: a pause over the slice still hides the meeting.
	app := newMeetingNotesTestApp(t)
	store := app.activity.store
	start := time.Now().Add(-30 * 24 * time.Hour)
	if err := store.saveMeetingNotesSlice(meetingNotesHeader{Key: "old-call", MeetingApp: "Halcyon Meet"}, "long-gone-1313", start.UnixMilli(), start.Add(10*time.Minute).UnixMilli(), 40); err != nil {
		t.Fatal(err)
	}
	if notes, _ := app.GetMeetingNotesForSessions([]string{"long-gone-1313"}); len(notes) != 1 {
		t.Fatalf("legacy notes without a pause = %+v", notes)
	}
	app.saveObservationPauses([]observationPause{{Start: start.Add(time.Minute).UnixMilli(), End: start.Add(2 * time.Minute).UnixMilli()}})
	if notes, _ := app.GetMeetingNotesForSessions([]string{"long-gone-1313"}); len(notes) != 0 {
		t.Fatalf("a pause over the call did not hide its notes: %+v", notes)
	}
}

func TestMeetingNotesOutliveRetentionButNotDeletion(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	store := app.activity.store
	start := time.Now().Add(-48 * time.Hour)
	kept := saveMeetingSlice(t, app, "retained-5555", start, 10, "")
	gone := saveMeetingSlice(t, app, "deleted-6666", start.Add(time.Hour), 10, "")
	for _, s := range []ActivitySessionRow{kept, gone} {
		if _, err := app.recordMeetingNotes(s, "Halcyon Meet", 50, "", "", &studioOutline); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.deleteRetainedSession(kept.ID); err != nil {
		t.Fatal(err)
	}
	if notes, _ := app.GetMeetingNotesForSessions([]string{kept.ID}); len(notes) != 1 || notes[0].Parts[0].HasTranscript {
		t.Fatalf("notes after retention = %+v", notes)
	}
	if err := store.DeleteSession(gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.meetingNotesByKey(gone.ID); ok {
		t.Fatal("a deleted call's notes stayed")
	}
	// Deleting a call retention already removed still removes its notes.
	if err := store.DeleteSession(kept.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.meetingNotesByKey(kept.ID); ok {
		t.Fatal("a retained call's notes stayed after its deletion")
	}
}

func TestMeetingNotesKeepTheSummaryFromTheAfterCallPass(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	// Isolate stored-note persistence from automatic learning during teardown.
	app.meetingRecordingDisabled.Store(true)
	stubMeetingNotesLearn(t)
	prior := meetingFollowupModel
	t.Cleanup(func() { meetingFollowupModel = prior })
	meetingFollowupModel = func(*App, string) (string, error) {
		return `{"summary":"We picked the opening date.","decisions":["Open on the 24th"],"followups":[{"text":"Tell support","owner":"me"}]}`, nil
	}
	start := time.Now().Add(-time.Hour)
	session := saveMeetingSlice(t, app, "summary-1414", start, 10, "")
	md := meetingTranscriptFixture(session, "", "[10:00:00] Me: Let us open on the 24th.")
	if _, err := app.extractMeetingFollowups(context.Background(), session, md, nil, nil); err != nil {
		t.Fatal(err)
	}
	// The pass ran before the notes were published (or a crash came in
	// between): publishing without an outline keeps what the pass stored.
	if notes, _ := app.GetMeetingNotesForSessions([]string{session.ID}); len(notes) != 0 {
		t.Fatalf("an outline alone is not a meeting yet: %+v", notes)
	}
	if _, err := app.recordMeetingNotes(session, "Halcyon Meet", 8, "", "", nil); err != nil {
		t.Fatal(err)
	}
	notes, _ := app.GetMeetingNotesForSessions([]string{session.ID})
	if len(notes) != 1 || notes[0].Summary != "We picked the opening date." || notes[0].Decisions[0] != "Open on the 24th" || notes[0].Followups[0].Text != "Tell support" {
		t.Fatalf("notes after the pass = %+v", notes)
	}
}

func TestMeetingNotesFollowTheirSliceToItsMeeting(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	// Isolate stored-note persistence from automatic learning during teardown.
	app.meetingRecordingDisabled.Store(true)
	stubMeetingNotesLearn(t)
	store := app.activity.store
	start := time.Now().Add(-time.Hour)
	session := saveMeetingSlice(t, app, "rekey-1515", start, 10, "")
	if _, err := app.recordMeetingNotes(session, "Halcyon Meet", 10, "", "", &studioOutline); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meeting_notes SET user_notes='Bring the draft.' WHERE meeting_key=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.recordMeetingNotes(session, "Halcyon Meet", 10, "meeting-R", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.meetingNotesByKey(session.ID); ok {
		t.Fatal("the meeting the slice left stayed behind")
	}
	r, ok, _ := store.meetingNotesByKey("meeting-R")
	if !ok || r.UserNotes != "Bring the draft." || len(r.Slices) != 1 || r.Slices[0].Summary == "" {
		t.Fatalf("moved slice = %+v", r)
	}
}

func TestDeletingMeetingNotesDeletesTheCallAndWhatItTaught(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	store := app.activity.store
	start := time.Now().Add(-time.Hour)
	session := saveMeetingSlice(t, app, "forget-1616", start, 10, "transcript")
	rel := filepath.Join(start.Format("2006-01-02"), "1792000000000-forget16.wav")
	if err := os.MkdirAll(filepath.Join(store.AudioDir(), filepath.Dir(rel)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.AudioDir(), rel), []byte("RIFF....WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE activity_sessions SET audio_path=? WHERE id=?`, rel, session.ID); err != nil {
		t.Fatal(err)
	}
	key, err := app.recordMeetingNotes(session, "Halcyon Meet", 10, "meeting-F", "", &studioOutline)
	if err != nil {
		t.Fatal(err)
	}
	leftoverDay := filepath.Join(store.AudioDir(), "2026-10-14")
	leftoverTracks(t, leftoverDay, "1792000000000-gggg7777", meetingAudioMeta{App: "Halcyon Meet", MeetingID: "meeting-F"})
	group := sessionGroup{Sessions: []ActivitySessionRow{session}, entrySeed: meetingNotesEntrySeed(key)}
	triager := NewActivityTriager(app, nil)
	knowledgeID, err := triager.saveKnowledge(session, group, &knowledgeGenerationResponse{Title: "Halcyon studio", KnowledgePoints: []string{"The welcome tour stays."}})
	if err != nil {
		t.Fatal(err)
	}
	memoryID, err := triager.saveActivityMemory(session, group, &mcMemoryGenerationResponse{Title: "Halcyon studio call", Dimension: "topic_highlight", MemoryPoints: []string{"I keep the welcome tour."}})
	if err != nil {
		t.Fatal(err)
	}

	// The owner's follow-up from this call became a commitment; one from another call exists too.
	app.writeMeetingCommitments(session, "Halcyon studio", "transcript", meetingFollowupExtraction{
		Followups: []meetingFollowup{{Text: "Send the tour notes to the studio", Owner: meetingFollowupOwnerMe}}})
	other := ActivitySessionRow{ID: "another-call", EndTime: start.UnixMilli()}
	app.writeMeetingCommitments(other, "Another call", "other transcript", meetingFollowupExtraction{
		Followups: []meetingFollowup{{Text: "Book the venue", Owner: meetingFollowupOwnerMe}}})
	purpose, err := app.purposeProductStore()
	if err != nil {
		t.Fatal(err)
	}
	if loops, err := purpose.ListDetailsAll(); err != nil || len(loops) != 2 {
		t.Fatalf("commitments before the delete: %d %v", len(loops), err)
	}

	if err := app.DeleteMeetingNotes(key); err != nil {
		t.Fatal(err)
	}
	// Deleted means deleted: the commitment made from this call is gone from the Purpose Map, the other one stays.
	loops, err := purpose.ListDetailsAll()
	if err != nil || len(loops) != 1 || loops[0].Current.Title != "Book the venue" {
		t.Fatalf("commitments after the delete: %+v %v", loops, err)
	}
	var left int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM purpose_open_loop_events_v1 WHERE title LIKE '%tour notes%'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("the deleted call's commitment text is still stored: %d rows %v", left, err)
	}
	if _, ok, _ := store.meetingNotesByKey(key); ok {
		t.Fatal("notes stayed")
	}
	if _, err := os.Stat(filepath.Join(store.AudioDir(), rel)); !os.IsNotExist(err) {
		t.Fatalf("recording stayed: %v", err)
	}
	if _, err := os.Stat(session.TranscriptPath); !os.IsNotExist(err) {
		t.Fatalf("transcript stayed: %v", err)
	}
	if row, _ := store.GetSessionByID(session.ID); row == nil || row.AudioPath != "" || row.TranscriptPath != "" {
		t.Fatalf("session row still links the call: %+v", row)
	}
	if _, err := app.GetKnowledgeEntry(knowledgeID); err == nil {
		t.Fatal("knowledge learned from the notes stayed")
	}
	dir, _ := memoryDir()
	if _, err := os.Stat(filepath.Join(dir, memoryID+".md")); !os.IsNotExist(err) {
		t.Fatalf("memory learned from the notes stayed: %v", err)
	}
	if !meetingNotesPublished(key) {
		t.Fatal("no marker keeps recovery from writing the notes again")
	}
	if err := app.DeleteMeetingNotes(key); err != nil {
		t.Fatalf("deleting again: %v", err)
	}
	if _, err := os.Stat(filepath.Join(leftoverDay, "1792000000000-gggg7777.mic.pcm")); !os.IsNotExist(err) {
		t.Fatalf("the meeting's leftover audio would come back as a call: %v", err)
	}
	// A transcript a failed removal left behind is not turned back into notes.
	orphanDir, _ := transcriptDir()
	md := meetingTranscriptFixture(session, key, "[10:00:00] Me: Hello again.")
	if err := os.WriteFile(filepath.Join(orphanDir, start.Format("2006-01-02")+"-halcyon-"+session.ID[:8]+".md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	app.recoverOrphanTranscripts(context.Background())
	if _, ok, _ := store.meetingNotesByKey(key); ok {
		t.Fatal("recovery wrote deleted notes again")
	}
}

func TestMeetingRecordingRouteServesOnlyReadableRecordings(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	app.bridge = &hostBridge{app: app}
	store := app.activity.store
	start := time.Now().Add(-time.Hour)
	session := saveMeetingSlice(t, app, "audio-7777", start, 10, "")
	rel := filepath.Join(start.Format("2006-01-02"), "1792000000000-audio777.wav")
	if err := os.MkdirAll(filepath.Join(store.AudioDir(), filepath.Dir(rel)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.AudioDir(), rel), []byte("RIFF....WAVE"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The recording's sidecar carries its capture provenance.
	if err := os.WriteFile(meetingWAVMetaPath(store.AudioDir(), rel), []byte(`{"app":"Halcyon Meet","activitySessions":["audio-7777"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE activity_sessions SET audio_path=? WHERE id=?`, rel, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.recordMeetingNotes(session, "Halcyon Meet", 50, "", "", &studioOutline); err != nil {
		t.Fatal(err)
	}
	serve := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		app.handleActivityMeetingAudio(rec, httptest.NewRequest(http.MethodGet, "/api/activity/audio?session="+session.ID, nil))
		return rec
	}
	if rec := serve(); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "audio/wav" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("readable recording: %d %v", rec.Code, rec.Header())
	}
	notes, _ := app.GetMeetingNotesForSessions([]string{session.ID})
	if len(notes) != 1 || !notes[0].Parts[0].HasRecording {
		t.Fatalf("parts = %+v", notes)
	}
	app.closedEyes.enabled.Store(true)
	if rec := serve(); rec.Code != http.StatusNotFound {
		t.Fatalf("recording served while observation is paused: %d", rec.Code)
	}
	app.closedEyes.enabled.Store(false)
	// A recording whose own capture was protected is not served.
	if err := os.WriteFile(meetingWAVMetaPath(store.AudioDir(), rel), []byte(`{"app":"Halcyon Meet","protected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := serve(); rec.Code != http.StatusNotFound {
		t.Fatalf("a protected recording was served: %d", rec.Code)
	}
	if _, err := store.db.Exec(`UPDATE activity_sessions SET audio_path='../escape.wav' WHERE id=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	if rec := serve(); rec.Code != http.StatusNotFound {
		t.Fatalf("recording outside the audio folder served: %d", rec.Code)
	}
}

func TestKnowledgeIsLearnedFromMeetingNotesWithProvenance(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stub := stubMeetingNotesLearn(t)
	calls := &stub.calls
	start := time.Now().Add(-time.Hour)
	first := saveMeetingSlice(t, app, "learn-first-1", start, 10, "")
	second := saveMeetingSlice(t, app, "learn-second-2", start.Add(15*time.Minute), 10, "")
	store := app.activity.store
	for _, s := range []ActivitySessionRow{first, second} {
		if err := store.saveMeetingNotesSlice(meetingNotesHeader{Key: "meeting-L", MeetingID: "meeting-L", MeetingApp: "Halcyon Meet", Title: "Halcyon studio review"}, s.ID, s.StartTime, s.EndTime, 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.saveMeetingNotesOutline(first.ID, "", studioOutline); err != nil {
		t.Fatal(err)
	}

	// A pass that fails leaves the notes unlearned; the next one runs again.
	stub.failed = true
	if ran, err := app.learnFromMeetingNotes(context.Background(), "meeting-L"); !ran || err == nil {
		t.Fatalf("failed pass: ran=%v err=%v", ran, err)
	}
	stub.failed = false
	stub.calls = nil
	ran, err := app.learnFromMeetingNotes(context.Background(), "meeting-L")
	if err != nil || !ran || len(*calls) != 1 {
		t.Fatalf("learning ran=%v err=%v calls=%d", ran, err, len(*calls))
	}
	group := (*calls)[0]
	if ids := sessionIDsFromGroup(group); strings.Join(ids, ",") != first.ID+","+second.ID {
		t.Fatalf("learning group sessions = %v", ids)
	}
	seed, text, _ := strings.Cut(group.entrySeed, "|")
	if seed != meetingNotesEntrySeed("meeting-L") {
		t.Fatalf("entry seed = %q", seed)
	}
	for _, want := range []string{"Meeting: Halcyon studio review", "We reviewed the studio floor plan and kept the welcome tour.", "- Keep the welcome tour", "- The user: Send the revised floor plan (due Tuesday)", "- Maya Chen: Check the welcome copy"} {
		if !strings.Contains(text, want) {
			t.Fatalf("learning text lacks %q:\n%s", want, text)
		}
	}

	// The same notes are learned once; changed notes are learned again.
	if ran, _ := app.learnFromMeetingNotes(context.Background(), "meeting-L"); ran {
		t.Fatal("the same notes were learned twice")
	}
	if err := store.saveMeetingNotesOutline(second.ID, "", meetingFollowupExtraction{Decisions: []string{"Open on the 24th"}}); err != nil {
		t.Fatal(err)
	}
	app.learnPendingMeetingNotes(context.Background())
	if len(*calls) != 2 {
		t.Fatal("changed notes were not learned at the next start")
	}

	// A protected slice stops the pass before any model sees the notes.
	if err := store.saveMeetingNotesOutline(second.ID, "", meetingFollowupExtraction{Decisions: []string{"Open on the 25th"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	if ran, _ := app.learnFromMeetingNotes(context.Background(), "meeting-L"); ran || len(*calls) != 2 {
		t.Fatal("protected notes were learned")
	}
}

func TestWhatAPassLearnedGoesWhenItsNotesGoDuringThePass(t *testing.T) {
	for _, during := range []string{"notes deleted", "recording turned off"} {
		t.Run(during, func(t *testing.T) {
			app := newMeetingNotesTestApp(t)
			store := app.activity.store
			session := saveMeetingSlice(t, app, "racing-1717", time.Now().Add(-time.Hour), 10, "")
			if err := store.saveMeetingNotesSlice(meetingNotesHeader{Key: "meeting-Q", MeetingApp: "Halcyon Meet"}, session.ID, session.StartTime, session.EndTime, 10); err != nil {
				t.Fatal(err)
			}
			if err := store.saveMeetingNotesOutline(session.ID, "", studioOutline); err != nil {
				t.Fatal(err)
			}
			prior := meetingNotesLearn
			t.Cleanup(func() { meetingNotesLearn = prior })
			var knowledgeID string
			meetingNotesLearn = func(c *ActivityTriager, _ context.Context, group sessionGroup, _ string, _ SessionContentProfile, _ triageFeatureFlags) bool {
				id, err := c.saveKnowledge(group.Sessions[0], group, &knowledgeGenerationResponse{Title: "Halcyon studio", KnowledgePoints: []string{"The welcome tour stays."}})
				if err != nil {
					t.Fatal(err)
				}
				knowledgeID = id
				if during == "notes deleted" {
					if err := store.deleteMeetingNotes("meeting-Q"); err != nil {
						t.Fatal(err)
					}
				} else {
					app.meetingRecordingDisabled.Store(true)
				}
				return true
			}
			if _, err := app.learnFromMeetingNotes(context.Background(), "meeting-Q"); err != nil {
				t.Fatal(err)
			}
			if _, err := app.GetKnowledgeEntry(knowledgeID); err == nil {
				t.Fatalf("what the pass learned stayed after the %s", during)
			}
			if r, ok, _ := store.meetingNotesByKey("meeting-Q"); ok && r.LearnedHash != "" {
				t.Fatal("the notes were marked learned")
			}
			// With recording off nothing is learned at all.
			if during == "recording turned off" {
				if ran, _ := app.learnFromMeetingNotes(context.Background(), "meeting-Q"); ran {
					t.Fatal("notes were learned while recording is off")
				}
			}
		})
	}
}

func TestMeetingNotesLearnOnlyWhatTheSettingsAllow(t *testing.T) {
	cases := []struct {
		name               string
		knowledge, memory  string
		wantKnowledge      bool
		wantMemory         bool
		wantRan, wantMarks bool
	}{
		{"both on", "", "", true, true, true, true},
		{"How you do things off", "false", "", false, true, true, true},
		{"About you off", "", "false", true, false, true, true},
		{"both off", "false", "false", false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newMeetingNotesTestApp(t)
			store := app.activity.store
			ctx := context.Background()
			if err := app.store.SetUIState(ctx, triageEnableKnowledgeKey, tc.knowledge); err != nil {
				t.Fatal(err)
			}
			if err := app.store.SetUIState(ctx, triageEnableMemoryKey, tc.memory); err != nil {
				t.Fatal(err)
			}
			session := saveMeetingSlice(t, app, "switch-1818", time.Now().Add(-time.Hour), 10, "")
			if err := store.saveMeetingNotesSlice(meetingNotesHeader{Key: "meeting-W", MeetingApp: "Halcyon Meet"}, session.ID, session.StartTime, session.EndTime, 10); err != nil {
				t.Fatal(err)
			}
			if err := store.saveMeetingNotesOutline(session.ID, "", studioOutline); err != nil {
				t.Fatal(err)
			}
			// The real gate over stand-in writers: each writes its entry the
			// way the triager's writer does.
			priorK, priorM := meetingNotesLearnKnowledge, meetingNotesLearnMemory
			t.Cleanup(func() { meetingNotesLearnKnowledge, meetingNotesLearnMemory = priorK, priorM })
			wroteKnowledge, wroteMemory := false, false
			meetingNotesLearnKnowledge = func(c *ActivityTriager, _ context.Context, group sessionGroup, _ string, _ SessionContentProfile) {
				wroteKnowledge = true
				if _, err := c.saveKnowledge(group.Sessions[0], group, &knowledgeGenerationResponse{Title: "Halcyon studio", KnowledgePoints: []string{"The welcome tour stays."}}); err != nil {
					t.Fatal(err)
				}
			}
			meetingNotesLearnMemory = func(c *ActivityTriager, _ context.Context, group sessionGroup, _ string, _ SessionContentProfile) {
				wroteMemory = true
				if _, err := c.saveActivityMemory(group.Sessions[0], group, &mcMemoryGenerationResponse{Title: "Halcyon studio call", Dimension: "topic_highlight", MemoryPoints: []string{"I keep the welcome tour."}}); err != nil {
					t.Fatal(err)
				}
			}
			ran, err := app.learnFromMeetingNotes(ctx, "meeting-W")
			if err != nil || ran != tc.wantRan {
				t.Fatalf("ran=%v err=%v, want ran=%v", ran, err, tc.wantRan)
			}
			if wroteKnowledge != tc.wantKnowledge || wroteMemory != tc.wantMemory {
				t.Fatalf("knowledge=%v memory=%v, want %v %v", wroteKnowledge, wroteMemory, tc.wantKnowledge, tc.wantMemory)
			}
			seed := meetingNotesEntrySeed("meeting-W")
			if _, err := app.GetKnowledgeEntry(activityKnowledgeEntryID(seed)); (err == nil) != tc.wantKnowledge {
				t.Fatalf("knowledge entry present=%v, want %v", err == nil, tc.wantKnowledge)
			}
			dir, _ := memoryDir()
			if _, err := os.Stat(filepath.Join(dir, activityMemoryEntryID(seed)+".md")); (err == nil) != tc.wantMemory {
				t.Fatalf("memory entry present=%v, want %v", err == nil, tc.wantMemory)
			}
			if r, _, _ := store.meetingNotesByKey("meeting-W"); (r.LearnedHash != "") != tc.wantMarks {
				t.Fatalf("learned=%q, want marked=%v", r.LearnedHash, tc.wantMarks)
			}
			if !tc.wantMarks {
				// Turning a switch back on learns the notes.
				if err := app.store.SetUIState(ctx, triageEnableKnowledgeKey, ""); err != nil {
					t.Fatal(err)
				}
				if ran, err := app.learnFromMeetingNotes(ctx, "meeting-W"); !ran || err != nil || !wroteKnowledge || wroteMemory {
					t.Fatalf("re-enabled: ran=%v err=%v knowledge=%v memory=%v", ran, err, wroteKnowledge, wroteMemory)
				}
			}
		})
	}
}

func TestMeetingNotesLearningPassReportsAPrivacyStop(t *testing.T) {
	// The real pass: the triager's generation rechecks privacy first, so a
	// protected session stops it before any model, and the pass reports it
	// did not finish.
	app := newMeetingNotesTestApp(t)
	session := saveMeetingSlice(t, app, "stopped-1212", time.Now().Add(-time.Hour), 10, "")
	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	session.TriageStatus = "protected"
	group := sessionGroup{Sessions: []ActivitySessionRow{session}, entrySeed: meetingNotesEntrySeed("meeting-S")}
	if meetingNotesLearn(NewActivityTriager(app, nil), context.Background(), group, "Notes from a call.", SessionContentProfile{}, triageFeatureFlags{Knowledge: true, Memory: true}) {
		t.Fatal("a pass stopped by privacy reported it finished")
	}
}

func TestMeetingNotesLearnIntoEntriesKeyedByTheMeeting(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	start := time.Now().Add(-time.Hour)
	first := saveMeetingSlice(t, app, "entry-first-1", start, 10, "")
	second := saveMeetingSlice(t, app, "entry-second-2", start.Add(15*time.Minute), 10, "")
	group := sessionGroup{Sessions: []ActivitySessionRow{first, second}, entrySeed: meetingNotesEntrySeed("meeting-E")}
	triager := NewActivityTriager(app, nil)

	// What the notes teach arrives silently, like the notes.
	if group.insightSource() != meetingNotesInsightSource || (&sessionGroup{Sessions: group.Sessions}).insightSource() != "activity" {
		t.Fatal("insight source")
	}
	knowledgeID, err := triager.saveKnowledge(first, group, &knowledgeGenerationResponse{Title: "Halcyon studio", Description: "Halcyon studio decisions", KnowledgePoints: []string{"The welcome tour stays."}})
	if err != nil {
		t.Fatal(err)
	}
	if knowledgeID != activityKnowledgeEntryID(meetingNotesEntrySeed("meeting-E")) || knowledgeID == activityKnowledgeEntryID(first.ID) {
		t.Fatalf("knowledge id = %q", knowledgeID)
	}
	entry, err := app.GetKnowledgeEntry(knowledgeID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(entry.SessionIDs, ",") != first.ID+","+second.ID || entry.Apps != "Halcyon Meet" {
		t.Fatalf("knowledge provenance = %+v", entry)
	}
	if strings.Contains(entry.Content, "source: meeting") {
		t.Fatal("learned knowledge is labelled as raw meeting notes")
	}
	// Protecting a slice hides what was learned from the call.
	if !app.memorySourceAllowsRead(entry.Content) {
		t.Fatal("learned knowledge unreadable before protection")
	}

	memoryID, err := triager.saveActivityMemory(first, group, &mcMemoryGenerationResponse{Title: "Halcyon studio call", Dimension: "topic_highlight", MemoryPoints: []string{"I keep the welcome tour."}})
	if err != nil {
		t.Fatal(err)
	}
	if memoryID != activityMemoryEntryID(meetingNotesEntrySeed("meeting-E")) {
		t.Fatalf("memory id = %q", memoryID)
	}
	dir, _ := memoryDir()
	raw, err := os.ReadFile(filepath.Join(dir, memoryID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	p := memoryReadProvenanceFromContent(string(raw))
	if strings.Join(p.Sessions, ",") != first.ID+","+second.ID || len(p.Kinds) != 1 || p.Kinds[0] != "activity" {
		t.Fatalf("memory provenance = %+v", p)
	}

	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, second.ID); err != nil {
		t.Fatal(err)
	}
	if app.memorySourceAllowsRead(entry.Content) || app.memorySourceAllowsRead(string(raw)) {
		t.Fatal("protecting a slice did not hide what was learned from the call")
	}
}

// writeLegacyMeetingEntry writes a meeting-notes Knowledge entry the way
// publishMeetingNotes did before notes moved to Activity.
func writeLegacyMeetingEntry(t *testing.T, app *App, session ActivitySessionRow, key, transcript string) string {
	t.Helper()
	dir, _ := knowledgeDir()
	entryID := meetingKnowledgeEntryID(key)
	start := time.UnixMilli(session.StartTime)
	fm := map[string]any{
		"type":              "Isoform Knowledge",
		"title":             "Meeting notes: Halcyon Meet, " + start.Format("Jan 2 15:04"),
		"resource":          "yansu://knowledge/" + entryID,
		"tags":              []string{"knowledge", meetingKnowledgeSource},
		"timestamp":         okfTimestamp(start),
		"source":            meetingKnowledgeSource,
		"description":       "Halcyon Meet call.",
		"apps":              []string{"Halcyon Meet"},
		"activity_sessions": []string{session.ID},
	}
	body := "## Meeting notes\n\n" + formatTranscriptMessage("Halcyon Meet", transcript, 6, start, "10m 0s")
	if err := os.WriteFile(filepath.Join(dir, entryID+".md"), []byte(renderOKFDocument(fm, body)), 0o600); err != nil {
		t.Fatal(err)
	}
	return entryID
}

func TestLegacyMeetingNotesMoveToActivityOnce(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stub := stubMeetingNotesLearn(t)
	store := app.activity.store
	start := time.Now().Add(-24 * time.Hour).Truncate(time.Minute)
	session := saveMeetingSlice(t, app, "legacy-8888", start, 10, "placeholder")
	transcript := meetingTranscriptFixture(session, "meeting-M", "[10:00:01] Me: Let's start the studio review.", "[10:00:09] Other: The welcome tour stays.")
	if err := os.WriteFile(session.TranscriptPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	decisions, _ := json.Marshal([]string{"Keep the welcome tour"})
	if err := app.store.SaveMeetingFollowup(context.Background(), storage.MeetingFollowupRow{SessionID: session.ID, DecisionsJSON: string(decisions), FollowupsJSON: "[]"}); err != nil {
		t.Fatal(err)
	}
	entryID := writeLegacyMeetingEntry(t, app, session, "meeting-M", transcript)

	// The user retitled the entry, added a note, and fixed a word in the
	// transcript.
	dir, _ := knowledgeDir()
	path := filepath.Join(dir, entryID+".md")
	raw, _ := os.ReadFile(path)
	doc, _ := parseOKFDocument(string(raw))
	doc.Frontmatter["title"] = "Halcyon studio review with Maya Chen"
	body := strings.Replace(doc.Body, "[10:00:09] Other: The welcome tour stays.", "[10:00:09] Other: The welcome tour stays for students.", 1)
	body += "\n\nMy note: ask about opening hours.\n- remind Maya Chen on Tuesday\n"
	if err := os.WriteFile(path, []byte(renderOKFDocument(doc.Frontmatter, body)), 0o600); err != nil {
		t.Fatal(err)
	}
	// An entry with no activity session stays in Knowledge.
	orphanID := "meeting-orphan"
	orphan := renderOKFDocument(map[string]any{"type": "Isoform Knowledge", "title": "Meeting notes: Halcyon Meet, Oct 1 09:00", "source": meetingKnowledgeSource}, "## Meeting notes\n\nOnly text.\n")
	if err := os.WriteFile(filepath.Join(dir, orphanID+".md"), []byte(orphan), 0o600); err != nil {
		t.Fatal(err)
	}
	// Knowledge learned elsewhere stays.
	other := renderOKFDocument(map[string]any{"type": "Isoform Knowledge", "title": "Halcyon studio", "source": "activity", "activity_sessions": []string{session.ID}}, "- The welcome tour stays.\n")
	if err := os.WriteFile(filepath.Join(dir, "activity-other.md"), []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}

	history := filepath.Join(dir, knowledgeHistoryDirName, entryID)
	if err := os.MkdirAll(history, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, "1.md"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	app.moveMeetingNotesToActivity(context.Background())

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the moved entry is still in Knowledge: %v", err)
	}
	if _, err := os.Stat(history); err != nil {
		t.Fatalf("the moved entry's revision history was lost: %v", err)
	}
	for _, kept := range []string{orphanID, "activity-other"} {
		if _, err := os.Stat(filepath.Join(dir, kept+".md")); err != nil {
			t.Fatalf("%s left Knowledge: %v", kept, err)
		}
	}
	r, ok, err := store.meetingNotesByKey("meeting-M")
	if err != nil || !ok {
		t.Fatalf("moved notes: ok=%v err=%v", ok, err)
	}
	if r.MeetingID != "meeting-M" || r.Title != "Halcyon studio review with Maya Chen" || r.LegacyKnowledgeID != entryID {
		t.Fatalf("moved header = %+v", r)
	}
	if !strings.Contains(r.UserNotes, "[10:00:09] Other: The welcome tour stays for students.") ||
		!strings.Contains(r.UserNotes, "My note: ask about opening hours.\n- remind Maya Chen on Tuesday") {
		t.Fatalf("user text lost: %q", r.UserNotes)
	}
	if strings.Contains(r.UserNotes, "Let's start the studio review") || strings.Contains(r.UserNotes, "## Meeting notes") || strings.Contains(r.UserNotes, "words") {
		t.Fatalf("generated text kept as the user's: %q", r.UserNotes)
	}
	if len(r.Slices) != 1 || r.Slices[0].SessionID != session.ID || len(r.Slices[0].Decisions) != 1 || r.Slices[0].WordCount == 0 {
		t.Fatalf("moved slices = %+v", r.Slices)
	}
	notes, _ := app.GetMeetingNotesForSessions([]string{session.ID})
	if len(notes) != 1 || notes[0].UserNotes != r.UserNotes {
		t.Fatalf("moved notes on Activity = %+v", notes)
	}
	// Moved notes are not learned again.
	if ran, _ := app.learnFromMeetingNotes(context.Background(), "meeting-M"); ran || len(stub.calls) != 0 {
		t.Fatal("moved notes were learned again")
	}
	if done, _ := app.store.GetUIState(context.Background(), meetingNotesMovedStateKey); done == "" {
		t.Fatal("the move left no marker")
	}
	// The user rejoins the moved call: its new part is learned.
	rejoin := saveMeetingSlice(t, app, "rejoin-8889", start.Add(20*time.Minute), 5, "")
	if _, err := app.recordMeetingNotes(rejoin, "Halcyon Meet", 30, "meeting-M", "", &meetingFollowupExtraction{Summary: "We came back to opening hours."}); err != nil {
		t.Fatal(err)
	}
	if ran, err := app.learnFromMeetingNotes(context.Background(), "meeting-M"); !ran || err != nil {
		t.Fatalf("the rejoined part was not learned: ran=%v err=%v", ran, err)
	}

	// Once is once: an entry written later is not moved by a second start.
	late := saveMeetingSlice(t, app, "late-9999", start.Add(time.Hour), 10, "")
	lateID := writeLegacyMeetingEntry(t, app, late, late.ID, meetingTranscriptFixture(late, "", "[11:00:00] Me: Hello."))
	app.moveMeetingNotesToActivity(context.Background())
	if _, err := os.Stat(filepath.Join(dir, lateID+".md")); err != nil {
		t.Fatal("the move ran twice")
	}
	// Deleting the moved meeting's notes takes the old history with them.
	if err := app.DeleteMeetingNotes("meeting-M"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(history); !os.IsNotExist(err) {
		t.Fatalf("the moved entry's history outlived its notes: %v", err)
	}
}

func TestLegacyMeetingNotesStayWhenTheMoveFails(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	start := time.Now().Add(-time.Hour)
	session := saveMeetingSlice(t, app, "failing-1010", start, 10, "")
	entryID := writeLegacyMeetingEntry(t, app, session, session.ID, meetingTranscriptFixture(session, "", "[10:00:00] Me: Hello there."))
	if _, err := app.activity.store.db.Exec(`DROP TABLE meeting_note_slices`); err != nil {
		t.Fatal(err)
	}
	app.moveMeetingNotesToActivity(context.Background())
	dir, _ := knowledgeDir()
	if _, err := os.Stat(filepath.Join(dir, entryID+".md")); err != nil {
		t.Fatalf("a failed move removed the entry: %v", err)
	}
	if done, _ := app.store.GetUIState(context.Background(), meetingNotesMovedStateKey); done != "" {
		t.Fatal("a failed move was marked done")
	}
}

func TestLegacyMeetingNotesUserText(t *testing.T) {
	body := "## Meeting notes\n\n**Halcyon Meet call** · Monday, October 13 at 9:30 AM · 10m 0s · 6 words\n\n[09:30:00] Me: Hello.\n\n" +
		"Follow up with Ana.\n\n## Meeting notes (continued, 09:45)\n\n**Halcyon Meet call** · Monday, October 13 at 9:45 AM · 2m 0s · 3 words\n\n[09:45:00] Other: Bye now.\n\n" +
		"<!-- meeting-session s-1 1 -->\n<!-- /meeting-session -->\n"
	if got := legacyMeetingNotesUserText(body, nil); got != "Follow up with Ana." {
		t.Fatalf("without transcripts = %q", got)
	}
	known := map[string]bool{"[09:30:00] Me: Hello.": true}
	if got := legacyMeetingNotesUserText(body, known); got != "Follow up with Ana.\n\n[09:45:00] Other: Bye now." {
		t.Fatalf("with transcripts = %q", got)
	}
	// A paragraph's continuation line is the transcript's, not the user's.
	wrapped := "## Meeting notes\n\n[09:30:00] Me: Hello,\nand welcome.\n\nMy own line.\n"
	if got := legacyMeetingNotesUserText(wrapped, map[string]bool{"[09:30:00] Me: Hello,": true, "and welcome.": true}); got != "My own line." {
		t.Fatalf("continuation line = %q", got)
	}
	if got := legacyMeetingNotesWordCount(body); got != 9 {
		t.Fatalf("word count = %d", got)
	}
}

func TestMeetingFollowupPassReturnsASummary(t *testing.T) {
	got, err := parseMeetingFollowupJSON("```json\n{\"summary\":\"  We   met.  \",\"decisions\":[],\"followups\":[]}\n```")
	if err != nil || got.Summary != "We met." {
		t.Fatalf("summary = %q, %v", got.Summary, err)
	}
	if prompt := buildMeetingFollowupPrompt("", nil, "[10:00:00] Hi."); !strings.Contains(prompt, `"summary"`) {
		t.Fatal("prompt does not ask for a summary")
	}
}

func TestRecentMeetingNotesAreNewestFirstAndReadable(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	now := time.Now()
	old := saveMeetingSlice(t, app, "recent-old-1919", now.Add(-10*24*time.Hour), 10, "")
	earlier := saveMeetingSlice(t, app, "recent-a-2020", now.Add(-5*time.Hour), 10, "")
	later := saveMeetingSlice(t, app, "recent-b-2121", now.Add(-2*time.Hour), 10, "")
	hidden := saveMeetingSlice(t, app, "recent-c-2222", now.Add(-time.Hour), 10, "")
	for _, s := range []ActivitySessionRow{old, earlier, later, hidden} {
		if _, err := app.recordMeetingNotes(s, "Halcyon Meet", 5, "", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, hidden.ID); err != nil {
		t.Fatal(err)
	}
	notes, err := app.GetRecentMeetingNotes(3)
	if err != nil || len(notes) != 2 || notes[0].MeetingKey != later.ID || notes[1].MeetingKey != earlier.ID {
		t.Fatalf("recent notes = %+v %v", notes, err)
	}
}
