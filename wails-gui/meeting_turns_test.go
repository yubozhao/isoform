package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/asr"
	"wails-gui/internal/calendar"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

// Deterministic model boundary for the portable PCM unit fixture. Native
// segmentation/embedding is exercised separately by TestCallNativeModels.
type syntheticCallDiarizer struct{}

func (syntheticCallDiarizer) Close() {}
func (syntheticCallDiarizer) Process(samples []float32) ([]callSpeakerSpan, error) {
	return []callSpeakerSpan{
		{Start: time.Second, End: 4 * time.Second, Speaker: 0, Embedding: []float32{1, 0, 0}},
		{Start: 3 * time.Second, End: 6 * time.Second, Speaker: 1, Embedding: []float32{0.8, 0.6, 0}},
		{Start: 8 * time.Second, End: 11 * time.Second, Speaker: 0, Embedding: []float32{1, 0, 0}},
	}, nil
}

type syntheticCallASR struct{}

func (syntheticCallASR) TranscribeLongForm(r asr.SampleReader, source string) ([]asr.Segment, error) {
	var samples []float32
	for {
		block, err := r.ReadSamples()
		samples = append(samples, block...)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if source == "microphone" {
		return []asr.Segment{{Start: 2 * time.Second, End: 5 * time.Second, Text: "I can send the report."}}, nil
	}
	if len(samples) < 40000 {
		return nil, nil
	}
	return []asr.Segment{{Start: 0, End: time.Duration(len(samples)) * time.Second / 16000, Text: "Please send the report."}}, nil
}

func syntheticTwoVoiceCall(t *testing.T) ([]meetingUtterance, string) {
	t.Helper()
	writer, err := newMeetingAudioWriter(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local)
	for _, source := range []backgroundListeningSource{backgroundListeningSourceMicrophone, backgroundListeningSourceSystem} {
		writer.WriteCapturedChunk(timedChunk(source, start.Add(12*time.Second), constSamples(12*16000, 1000)))
	}
	rel, _, err := writer.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(writer.audioDir, rel)
	out, err := transcribeDiarizedMeetingRecording(syntheticCallASR{}, path, syntheticCallDiarizer{})
	if err != nil {
		t.Fatal(err)
	}
	return out, path
}

func TestCallTurnsTwoTrackTwoVoicesAndOverlap(t *testing.T) {
	u, _ := syntheticTwoVoiceCall(t)
	turns := buildCallTurns(u, "first")
	if len(turns) != 4 {
		t.Fatalf("lost turns: %+v", turns)
	}
	if turns[0].SpeakerID != turns[3].SpeakerID || turns[0].SpeakerID == turns[2].SpeakerID {
		t.Fatalf("two remote voices not stable: %+v", turns)
	}
	if turns[1].SpeakerID != "you" || turns[1].Source != "microphone" {
		t.Fatalf("microphone attribution: %+v", turns)
	}
	if turns[1].StartTime >= turns[0].EndTime || turns[2].StartTime >= turns[1].EndTime {
		t.Fatal("overlap was removed")
	}
	for i := 1; i < len(turns); i++ {
		if turns[i].StartTime < turns[i-1].StartTime {
			t.Fatal("not chronological")
		}
	}
	md, _ := buildMeetingTranscriptMarkdown("Zoom", ActivitySessionRow{StartTime: turns[0].StartTime, EndTime: turns[3].EndTime}, u, nil)
	if !strings.Contains(md, "You:") || !strings.Contains(md, "Speaker 2:") || !strings.Contains(md, "Speaker 3:") {
		t.Fatalf("missing file turn labels: %s", md)
	}
}

type countedCallDiarizer struct{ calls int }

func (d *countedCallDiarizer) Close() {}
func (d *countedCallDiarizer) Process(samples []float32) ([]callSpeakerSpan, error) {
	d.calls++
	return (syntheticCallDiarizer{}).Process(samples)
}

type countedCallASR struct{ calls int }

func (a *countedCallASR) TranscribeLongForm(r asr.SampleReader, source string) ([]asr.Segment, error) {
	a.calls++
	return (syntheticCallASR{}).TranscribeLongForm(r, source)
}

func TestCallTurnsSecondLoadDoesNotReprocessUnassignedAudio(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	u, path := syntheticTwoVoiceCall(t)
	row := saveSyntheticCall(t, app, "unassigned-call", "unassigned", u)
	// Begin with a legacy-text view, as when models become available after
	// the user first opened an old call.
	legacy := legacyCallUtterances("## Transcript\n[10:00:01] Other: Hello.\n", row)
	if err := app.saveMeetingCallTurns("unassigned", row, legacy, nil, nil); err != nil {
		t.Fatal(err)
	}
	diarizer, transcriber := &countedCallDiarizer{}, &countedCallASR{}
	rebuilds := 0
	rebuild := func(ActivitySessionRow) []meetingUtterance {
		rebuilds++
		out, err := transcribeDiarizedMeetingRecording(transcriber, path, diarizer)
		if err != nil {
			t.Fatal(err)
		}
		// An audio pass may leave a remote utterance unassigned. That is
		// still processed audio, including the older :unknown identity.
		out[0].SpeakerID = ""
		return out
	}
	first, err := app.meetingCallTurns(row.ID, rebuild)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "unavailable" || first.Turns[0].SpeakerID != row.ID+":unknown" {
		t.Fatalf("unassigned turn/limited note lost: %+v", first)
	}
	if rebuilds != 1 || diarizer.calls == 0 || transcriber.calls == 0 {
		t.Fatalf("first load did not process audio: %d / %d / %d", rebuilds, diarizer.calls, transcriber.calls)
	}
	firstDiarizations, firstTranscriptions := diarizer.calls, transcriber.calls
	second, err := app.meetingCallTurns(row.ID, rebuild)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilds != 1 || diarizer.calls != firstDiarizations || transcriber.calls != firstTranscriptions {
		t.Fatalf("second load processed audio again: %d / %d / %d", rebuilds, diarizer.calls, transcriber.calls)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("persisted turns changed on second load: %+v / %+v", first, second)
	}
}

// A legacy call whose recording keeps failing to decode must not be decoded
// again on every load: the failure is recorded on the recording's sidecar
// with the file's size and modification time, and the same unchanged file is
// left alone until a day passes. The legacy text keeps showing either way
// (meetingCallTurns keeps it when a rebuild yields nothing).
func TestCallTurnsFailedDecodeIsNotRetriedOnAnUnchangedRecording(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	dir := app.activity.store.AudioDir()
	rel := filepath.Join("2026-10-19", "1000-legacy.wav")
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not really audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	row := ActivitySessionRow{ID: "legacy-failing", AudioPath: rel}
	attempts := 0
	failing := func(ActivitySessionRow) ([]meetingUtterance, bool) { attempts++; return nil, false }
	for load := 0; load < 2; load++ {
		if got := app.decodeCallAudioOncePerFile(row, failing); len(got) != 0 {
			t.Fatalf("load %d: %+v", load, got)
		}
	}
	if attempts != 1 {
		t.Fatalf("an unchanged recording was decoded on every load: %d attempts", attempts)
	}
	// The failure is recorded with the file's size and modification time.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	failed := recordedMeetingAudioDecodeFailure(dir, rel)
	if failed == nil || failed.Size != info.Size() || failed.ModMs != info.ModTime().UnixMilli() || failed.FailedMs <= 0 {
		t.Fatalf("the failure was not recorded with the file: %+v", failed)
	}
	// A recording without a sidecar gets none: the read gate keeps it closed.
	if _, err := os.Stat(meetingWAVMetaPath(dir, rel)); !os.IsNotExist(err) {
		t.Fatalf("noting the failure created a sidecar: %v", err)
	}
	// A changed file is decoded again...
	if err := os.Chtimes(path, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	app.decodeCallAudioOncePerFile(row, failing)
	if attempts != 2 {
		t.Fatalf("a changed recording was not retried: %d attempts", attempts)
	}
	// ...and so is an unchanged one once a day has passed.
	if meetingAudioDecodeFailedRecently(dir, rel, time.Now().Add(25*time.Hour)) {
		t.Fatal("a day-old failure still suppresses the retry")
	}
	// A later retry that succeeds clears the record.
	if err := os.Chtimes(path, time.Now(), time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := app.decodeCallAudioOncePerFile(row, func(ActivitySessionRow) ([]meetingUtterance, bool) {
		return []meetingUtterance{{Text: "words"}}, true
	}); len(got) != 1 {
		t.Fatalf("a successful rebuild yields its utterances: %+v", got)
	}
	if recordedMeetingAudioDecodeFailure(dir, rel) != nil {
		t.Fatal("a successful rebuild did not clear the recorded failure")
	}
}

func TestCallLegacyTranscriptStaysCompleteAndLimitedAcrossLoads(t *testing.T) {
	for _, lines := range []string{
		"[10:00:01] Me: My first words.\n[10:00:03] Other: Their reply.\n[10:00:05] Me: My last words.\n",
		"[10:00:01] Me: Only my words.\n",
	} {
		t.Run(lines, func(t *testing.T) {
			app := newMeetingNotesTestApp(t)
			stubMeetingNotesLearn(t)
			row := saveMeetingSlice(t, app, "legacy-call", time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local), 1, "## Transcript\n"+lines)
			if _, err := app.recordMeetingNotes(row, "Zoom", 20, "legacy", "", nil); err != nil {
				t.Fatal(err)
			}
			// Missing installed models/audio must retain every legacy line,
			// including the limitation note for a microphone-only transcript.
			unavailable := func(ActivitySessionRow) []meetingUtterance { return nil }
			var first MeetingCallTurnsView
			for load := 0; load < 2; load++ {
				view, err := app.meetingCallTurns(row.ID, unavailable)
				if err != nil || view.Status != "unavailable" || len(view.Turns) != strings.Count(lines, "\n") {
					t.Fatalf("load %d lost legacy text/limitation: %+v %v", load, view, err)
				}
				for _, turn := range view.Turns {
					if !turn.LegacyText || !strings.Contains(lines, turn.Text) {
						t.Fatalf("legacy provenance/text lost: %+v", turn)
					}
				}
				if load == 0 {
					first = view
				} else if !reflect.DeepEqual(first, view) {
					t.Fatalf("legacy turns changed on reload: %+v / %+v", first, view)
				}
			}
		})
	}
}

func TestCallTurnProvenanceMigrationPreservesExistingUnknownTurns(t *testing.T) {
	db := newTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := migrateMeetingNotes(tx); err != nil {
		t.Fatal(err)
	}
	if err := migrateMeetingTurns(tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO meeting_notes(meeting_key,created_at,updated_at) VALUES('old',1,1);
	INSERT INTO meeting_call_turns VALUES('old','old-session',0,'old-session:unknown','system_audio',1,2,'Keep these words.');`); err != nil {
		t.Fatal(err)
	}
	for replay := 0; replay < 2; replay++ {
		if err := migrateMeetingTurnProvenance(tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &ActivityStore{db: db}
	turns, err := store.callTurns("old-session")
	if err != nil || len(turns) != 1 || turns[0].Text != "Keep these words." || turns[0].LegacyText {
		t.Fatalf("migration lost text or invented legacy provenance: %+v %v", turns, err)
	}
}

func TestCallInviteNamesUseContactsAndNeverInviteOrder(t *testing.T) {
	event := &calendar.Event{Attendees: []calendar.Attendee{{Name: "Me", IsMe: true}, {Email: "lin@example.com", Status: calendar.StatusAccepted}}}
	contacts := []systemContact{{FullName: "Lin Chen", Emails: []string{"LIN@example.com"}}}
	if got := callInviteName(event, contacts, 1); got != "Lin Chen" {
		t.Fatalf("exact contact lookup = %q", got)
	}
	if got := callInviteName(event, contacts, 2); got != "" {
		t.Fatal("assigned an invite to one of two voices")
	}
	event.Attendees = append(event.Attendees, calendar.Attendee{Name: "Sam"})
	if got := callInviteName(event, contacts, 1); got != "" {
		t.Fatal("guessed from invite order")
	}
	event.Attendees[2].Status = calendar.StatusDeclined
	if got := callInviteName(event, contacts, 1); got != "Lin Chen" {
		t.Fatalf("declined invitee counted: %q", got)
	}
}

func saveSyntheticCall(t *testing.T, app *App, id, key string, u []meetingUtterance) ActivitySessionRow {
	t.Helper()
	start := u[0].At
	row := saveMeetingSlice(t, app, id, start, 1, "## Transcript\n[10:00:01] Other: Hello.\n")
	if _, err := app.recordMeetingNotes(row, "Zoom", 20, key, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := app.saveMeetingCallTurns(key, row, u, nil, nil); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestCallRenameWholeCallAndMatchSecondSyntheticCall(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	u, _ := syntheticTwoVoiceCall(t)
	first := saveSyntheticCall(t, app, "call-first", "call-one", u)
	view, err := app.GetMeetingCallTurns(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := view.Turns[0].SpeakerID
	gen, _ := app.activity.store.callVoiceGeneration()
	// The real store commits a synthetic model reference, then the public
	// Rename path changes it without requiring a device or installed model.
	if err := app.activity.store.renameCallSpeaker("call-one", id, "Lin", []float32{1, 0, 0}, gen); err != nil {
		t.Fatal(err)
	}
	if err := app.RenameMeetingSpeaker("call-one", id, "Lin Chen"); err != nil {
		t.Fatal(err)
	}
	view, err = app.GetMeetingCallTurns(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Turns[0].Name != "Lin Chen" || view.Turns[3].Name != "Lin Chen" || view.Turns[2].Name != "" {
		t.Fatalf("rename propagation: %+v", view)
	}
	second := saveSyntheticCall(t, app, "call-second", "call-two", u)
	next, err := app.GetMeetingCallTurns(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Turns[0].Name != "Lin Chen" || next.Turns[2].Name != "" {
		t.Fatalf("second call match: %+v", next)
	}
	refs, err := app.activity.store.callSignatures()
	if err != nil || len(refs) != 1 || refs[0].Key != "call-one" {
		t.Fatalf("auto-enrolled or lost provenance: %+v %v", refs, err)
	}
	encoded, _ := json.Marshal(next)
	if strings.Contains(strings.ToLower(string(encoded)), "embedding") || strings.Contains(string(encoded), "voice:") {
		t.Fatalf("private voice data in DTO: %s", encoded)
	}
	if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	next, err = app.GetMeetingCallTurns(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Turns[0].Name != "" {
		t.Fatal("a protected teaching call still supplies names")
	}
	if len(app.readableCallSignatures()) != 0 {
		t.Fatal("protected voice used for later calls")
	}
}

func TestCallVoiceMatchRejectsWeakAmbiguousAndWrongDimensions(t *testing.T) {
	refs := []callVoiceSignature{{Name: "Lin", Embedding: []float32{1, 0}}, {Name: "Sam", Embedding: []float32{0.99, 0.01}}}
	if _, ok := matchCallVoice([]float32{1, 0}, refs); ok {
		t.Fatal("ambiguous voices matched")
	}
	for _, probe := range [][]float32{{0, 1}, {1}, {0, 0}} {
		if _, ok := matchCallVoice(probe, refs[:1]); ok {
			t.Fatalf("bad probe matched: %v", probe)
		}
	}
}

func TestCallForgetAndDeleteProvenanceCascades(t *testing.T) {
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	u, _ := syntheticTwoVoiceCall(t)
	first := saveSyntheticCall(t, app, "first-source", "first-call", u)
	second := saveSyntheticCall(t, app, "second-source", "second-call", u)
	store := app.activity.store
	gen, _ := store.callVoiceGeneration()
	for _, s := range []struct{ key, id string }{{"first-call", first.ID}, {"second-call", second.ID}} {
		if err := store.renameCallSpeaker(s.key, s.id+":speaker-2", "Lin", []float32{1, 0}, gen); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.DeleteMeetingNotes("first-call"); err != nil {
		t.Fatal(err)
	}
	refs, err := store.callSignatures()
	if err != nil || len(refs) != 1 || refs[0].Key != "second-call" {
		t.Fatalf("independent sample deleted: %+v %v", refs, err)
	}
	turns, _ := store.callTurns(first.ID)
	if len(turns) != 0 {
		t.Fatal("deleted call turns retained")
	}
	if _, err := store.db.Exec(`INSERT INTO activity_events(session_id,timestamp,kind,data,sort_order) VALUES(?,1,'speech','{"speechText":"kept","speechSpeakerEmbedding":[1,0]}',0)`, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.ForgetVoices(); err != nil {
		t.Fatal(err)
	}
	var legacyData string
	if err := store.db.QueryRow(`SELECT data FROM activity_events WHERE session_id=?`, second.ID).Scan(&legacyData); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacyData, "speechSpeakerEmbedding") || !strings.Contains(legacyData, "kept") {
		t.Fatalf("legacy embedding erasure damaged speech: %s", legacyData)
	}
	refs, _ = store.callSignatures()
	if len(refs) != 0 {
		t.Fatal("Forget left signatures")
	}
	// A slow extraction from before Forget must not put its result back.
	if err := store.renameCallSpeaker("second-call", second.ID+":speaker-2", "Lin", []float32{1, 0}, gen); err != nil {
		t.Fatal(err)
	}
	refs, _ = store.callSignatures()
	if len(refs) != 0 {
		t.Fatal("late extraction resurrected a forgotten voice")
	}
	view, err := app.GetMeetingCallTurns(second.ID)
	if err != nil || view.Turns[0].Name != "Lin" {
		t.Fatalf("Forget removed the user's note label: %+v %v", view, err)
	}
	gen, _ = store.callVoiceGeneration()
	if err := store.renameCallSpeaker("second-call", second.ID+":speaker-2", "Lin", []float32{1, 0}, gen); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSession(second.ID); err != nil {
		t.Fatal(err)
	}
	refs, _ = store.callSignatures()
	if len(refs) != 0 {
		t.Fatal("session deletion left a teaching signature")
	}
}

func TestCallTurnsNamesAndLearningObeyPrivacyGates(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*App, ActivitySessionRow)
	}{
		{"Closed Eyes", func(a *App, _ ActivitySessionRow) { a.closedEyes.enabled.Store(true) }},
		{"protected", func(a *App, r ActivitySessionRow) {
			_, _ = a.activity.store.db.Exec(`UPDATE activity_sessions SET triage_status='protected' WHERE id=?`, r.ID)
		}},
		{"excluded or sensitive app", func(_ *App, _ ActivitySessionRow) {
			pii.SetUserSensitiveApps(map[string]string{"us.halcyon.xos": "Halcyon Meet"})
		}},
		{"pause", func(a *App, r ActivitySessionRow) {
			a.saveObservationPauses([]observationPause{{Start: r.StartTime + 1, End: r.StartTime + 2000}})
		}},
		{"deleted", func(a *App, r ActivitySessionRow) { _ = a.DeleteMeetingNotes("private-call") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newMeetingNotesTestApp(t)
			stubMeetingNotesLearn(t)
			u, _ := syntheticTwoVoiceCall(t)
			row := saveSyntheticCall(t, app, "gate-source", "private-call", u)
			gen, _ := app.activity.store.callVoiceGeneration()
			if err := app.activity.store.renameCallSpeaker("private-call", row.ID+":speaker-2", "Lin", []float32{1, 0}, gen); err != nil {
				t.Fatal(err)
			}
			tc.apply(app, row)
			if v, err := app.GetMeetingCallTurns(row.ID); err == nil || len(v.Turns) > 0 {
				t.Fatalf("turns escaped gate: %+v %v", v, err)
			}
			if err := app.RenameMeetingSpeaker("private-call", row.ID+":speaker-2", "Sam"); err == nil {
				t.Fatal("rename escaped gate")
			}
			if err := app.saveMeetingCallTurns("private-call", row, u, nil, nil); err == nil {
				t.Fatal("generation escaped gate")
			}
			if refs := app.readableCallSignatures(); len(refs) > 0 {
				t.Fatal("private signature matched")
			}
		})
	}
}

func TestCallSignatureNeverUsesMixedOrShortAudio(t *testing.T) {
	u, path := syntheticTwoVoiceCall(t)
	turns := buildCallTurns(u, "one")
	calls := 0
	extract := func(samples []float32, rate int) ([]float32, error) {
		calls++
		if rate != 16000 || len(samples) < 24000 {
			t.Fatal("short sample")
		}
		return []float32{1, 0}, nil
	}
	if got := extractCallSpeakerSignature(path, turns, "one:speaker-3", extract); got != nil || calls != 0 {
		t.Fatal("mixed speech taught a signature")
	}
	if got := extractCallSpeakerSignature(path, turns, "one:speaker-2", extract); len(got) != 2 || calls != 1 {
		t.Fatal("clean later turn did not teach a signature")
	}
}

func TestCallModelsPinnedAndMissingModelsNeverDownload(t *testing.T) {
	if len(sha256SpeakerEmbedding) != 64 || len(sha256SpeakerSegmentation) != 64 {
		t.Fatal("model hash missing")
	}
	dir := t.TempDir()
	p := sherpaPaths{speakerEmbd: filepath.Join(dir, "embedding"), segmentation: filepath.Join(dir, "segmentation")}
	if d, err := newCallDiarizer(p); err == nil || d != nil {
		t.Fatal("missing models were loaded")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("call-time setup wrote files")
	}
	if err := os.WriteFile(p.segmentation, []byte("corrupt model"), 0600); err != nil {
		t.Fatal(err)
	}
	if d, err := newCallDiarizer(p); err == nil || d != nil {
		t.Fatal("unverified models were loaded")
	}
}

func TestCallRejoinedShortRepliesKeepTheirSpeaker(t *testing.T) {
	u := []meetingUtterance{
		{Source: "system_audio", SpeakerID: "speaker-2", Embedding: []float32{1, 0}},
		{Source: "system_audio", SpeakerID: "speaker-2", Text: "Yes."},
		{Source: "system_audio", SpeakerID: "speaker-3", Embedding: []float32{0, 1}},
	}
	alignCallSpeakerIdentities(u, []callVoiceSignature{{Speaker: "earlier:speaker-4", Name: "earlier:speaker-4", Embedding: []float32{1, 0}}})
	if u[0].CallSpeakerID != "earlier:speaker-4" || u[1].CallSpeakerID != u[0].CallSpeakerID || u[2].CallSpeakerID != "" {
		t.Fatalf("short replies lost the call identity: %+v", u)
	}
}

func TestCallSegmentationNeverDiscardsUnassignedAudio(t *testing.T) {
	spans := coverCallSpeakerSpans([]callSpeakerSpan{{Start: time.Second, End: 3 * time.Second, Speaker: 0}, {Start: 2 * time.Second, End: 4 * time.Second, Speaker: 1}}, 6*time.Second)
	covered := time.Duration(0)
	known := 0
	unknown := 0
	for _, span := range spans {
		if span.Start > covered {
			t.Fatalf("audio gap: %s to %s", covered, span.Start)
		}
		covered = max(covered, span.End)
		if span.Speaker < 0 {
			unknown++
		} else {
			known++
		}
	}
	if covered != 6*time.Second || known != 2 || unknown != 2 {
		t.Fatalf("lost coverage or overlap: %+v", spans)
	}
	allUnknown := coverCallSpeakerSpans(nil, 6*time.Second)
	if len(allUnknown) != 1 || allUnknown[0].End != 6*time.Second || allUnknown[0].Speaker != -1 {
		t.Fatal("no labels discarded the recording")
	}
}

func TestCallLiveFallbackKeepsMatchingWordsOnBothTracks(t *testing.T) {
	app := &App{}
	ms := time.Now().UnixMilli()
	events := []activitymon.ActivityEvent{speechEvent(ms, "microphone", "Yes, I can send it today."), speechEvent(ms, "system_audio", "Yes, I can send it today.")}
	u, ok := app.meetingTranscriptUtterances(ActivitySessionRow{}, events)
	if !ok || len(u) != 2 {
		t.Fatalf("lost simultaneous speech: %+v", u)
	}
	turns := buildCallTurns(u, "live")
	if len(turns) != 2 || turns[0].SpeakerID != "you" || turns[1].Source != "system_audio" {
		t.Fatalf("live turn labels: %+v", turns)
	}
	raw, _ := json.Marshal(activitymon.ActivityEvent{SpeechText: "kept", SpeechSpeakerEmbedding: []float32{1, 0}})
	if strings.Contains(string(raw), "speechSpeakerEmbedding") || !strings.Contains(string(raw), "kept") {
		t.Fatalf("biometric data in Activity DTO: %s", raw)
	}
}

func TestCallMonoTracksKeepTheirSource(t *testing.T) {
	for _, source := range []backgroundListeningSource{backgroundListeningSourceMicrophone, backgroundListeningSourceSystem} {
		t.Run(string(source), func(t *testing.T) {
			writer, err := newMeetingAudioWriter(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now().Add(-time.Minute)
			writer.WriteCapturedChunk(timedChunk(source, start.Add(12*time.Second), constSamples(12*16000, 1000)))
			rel, _, err := writer.Finalize()
			if err != nil {
				t.Fatal(err)
			}
			meta := readMeetingWAVMeta(writer.audioDir, rel)
			if meta.MonoSource != string(source) {
				t.Fatalf("mono track lost its role: %q", meta.MonoSource)
			}
			u, err := transcribeDiarizedMeetingRecording(syntheticCallASR{}, filepath.Join(writer.audioDir, rel), syntheticCallDiarizer{}, meta.MonoSource)
			if err != nil {
				t.Fatal(err)
			}
			turns := buildCallTurns(u, "mono")
			if source == backgroundListeningSourceMicrophone {
				if len(turns) != 1 || turns[0].SpeakerID != "you" {
					t.Fatalf("mic-only call must be You: %+v", turns)
				}
			} else {
				if len(turns) != 3 {
					t.Fatalf("system-only voices not separated: %+v", turns)
				}
				for _, turn := range turns {
					if turn.SpeakerID == "you" {
						t.Fatal("system-only audio became You")
					}
				}
			}
			if _, err := transcribeDiarizedMeetingRecording(syntheticCallASR{}, filepath.Join(writer.audioDir, rel), syntheticCallDiarizer{}); err == nil {
				t.Fatal("legacy mono source guessed")
			}
		})
	}
}
