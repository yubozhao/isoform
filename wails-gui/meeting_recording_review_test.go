package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"yansu-agent/agent"
)

func TestMeetingAudioBufferedDistinctChunksKeepEverySample(t *testing.T) {
	for _, source := range []backgroundListeningSource{backgroundListeningSourceMicrophone, backgroundListeningSourceSystem} {
		for _, identified := range []bool{false, true} {
			t.Run(string(source)+"/"+map[bool]string{false: "legacy", true: "identified"}[identified], func(t *testing.T) {
				w, err := newMeetingAudioWriter(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer w.Abort()
				first := timedChunk(source, time.Now(), constSamples(5*meetingAudioSampleRate, 1000))
				second := timedChunk(source, first.CapturedAt.Add(100*time.Millisecond), constSamples(5*meetingAudioSampleRate, 2000))
				if !identified {
					first.CaptureID, second.CaptureID = 0, 0
				}
				w.WriteCapturedChunk(first)
				w.WriteCapturedChunk(second)
				if identified {
					replay := first
					replay.CapturedAt = second.CapturedAt.Add(time.Second)
					w.WriteCapturedChunk(replay)
				}
				if got := w.trackBytesLocked(source); got != 320000 {
					t.Fatalf("distinct buffered reads lost samples: %d", got)
				}
				path := w.micFile.Name()
				if source == backgroundListeningSourceSystem {
					path = w.sysFile.Name()
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < len(raw); i += 2 {
					want := int16(1000)
					if i >= 160000 {
						want = 2000
					}
					if got := int16(binary.LittleEndian.Uint16(raw[i:])); got != want {
						t.Fatalf("sample %d: %d want %d", i/2, got, want)
					}
				}
			})
		}
	}
}

func reviewTwoRotationCall(t *testing.T, app *App, firstDate string) ActivitySessionRow {
	t.Helper()
	store := app.activity.store
	seedActivitySummaryI18nSession(t, store, "opening-rotation", firstDate, 23, []string{"Zoom"})
	seedActivitySummaryI18nSession(t, store, "final-rotation", "2026-04-20", 1, []string{"Zoom"})
	row, err := store.GetSessionByID("final-rotation")
	if err != nil {
		t.Fatal(err)
	}
	row.IsMeeting, row.MeetingApp = true, "Zoom"
	row.TranscriptPath = filepath.Join(t.TempDir(), "call.md")
	row.AudioPath = "2026-04-20/call.wav"
	path := meetingWAVMetaPath(store.AudioDir(), row.AudioPath)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeMeetingAudioMeta(path, meetingAudioMeta{ActivitySessions: []string{"opening-rotation", row.ID}, App: "Zoom"}); err != nil {
		t.Fatal(err)
	}
	// Exercise legacy sidecars first; the new header is checked separately.
	if err := os.WriteFile(row.TranscriptPath, []byte("# Call\n- **Session**: "+row.ID+"\n\n## Transcript\nOpening rotation sentence.\nFinal rotation sentence.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE activity_sessions SET is_meeting=1,meeting_app=?,transcript_path=?,audio_path=? WHERE id=?`, row.MeetingApp, row.TranscriptPath, row.AudioPath, row.ID); err != nil {
		t.Fatal(err)
	}
	return *row
}

func TestWholeCallReadersRecheckEveryRotation(t *testing.T) {
	for _, date := range []string{"2026-04-19", "2026-04-20"} {
		for _, mode := range []string{"sidecar", "retained-header", "retained-legacy", "missing-legacy-proof"} {
			t.Run(date+"/"+mode, func(t *testing.T) {
				isolateActivityPrivacyTest(t)
				app := newActivitySummaryI18nTestApp(t, &activitySummaryI18nFakeRunner{})
				row := reviewTwoRotationCall(t, app, date)
				if mode != "sidecar" {
					raw, err := os.ReadFile(row.TranscriptPath)
					if err != nil {
						t.Fatal(err)
					}
					text := string(raw)
					if mode == "retained-header" {
						text = withTranscriptActivitySources(text, []string{"opening-rotation", row.ID})
					} else {
						text = strings.Replace(text, "- **Session**: ", "- **Audio**: activity/audio/"+filepath.ToSlash(row.AudioPath)+"\n- **Session**: ", 1)
					}
					if err := os.WriteFile(row.TranscriptPath, []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
					if mode == "missing-legacy-proof" {
						if err := os.Remove(meetingWAVMetaPath(app.activity.store.AudioDir(), row.AudioPath)); err != nil {
							t.Fatal(err)
						}
					}
					row.AudioPath = ""
					if _, err := app.activity.store.db.Exec(`UPDATE activity_sessions SET audio_path='' WHERE id=?`, row.ID); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "missing-legacy-proof" {
					if _, err := app.GetMeetingTranscript(row.ID); err == nil {
						t.Fatal("legacy recording without dependency proof was readable")
					}
					if got := app.loadDailyMeetings([]ActivitySessionRow{row}); strings.Contains(got, "rotation sentence") {
						t.Fatal("legacy recording without proof entered daily context")
					}
					sources, err := app.captureSummarySources("2026-04-20", "daily")
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(sources.MeetingContext, "rotation sentence") {
						t.Fatal("legacy recording without proof entered captured context")
					}
					triager := &ActivityTriager{app: app}
					for _, o := range triager.buildGroupObservationSequence(sessionGroup{Sessions: []ActivitySessionRow{row}}).Observations {
						if strings.Contains(o.Body, "rotation sentence") {
							t.Fatal("legacy recording without proof entered semantic context")
						}
					}
					return
				}
				sources, err := app.captureSummarySources("2026-04-20", "daily")
				if err != nil {
					t.Fatal(err)
				}
				ids := []string{}
				for _, s := range sources.Sources {
					ids = append(ids, s.SessionID)
				}
				if !stringSliceContains(ids, "opening-rotation") || !strings.Contains(sources.MeetingContext, "Opening rotation sentence.") {
					t.Fatalf("whole call not captured with provenance: %+v", sources)
				}
				data, err := app.getDailySummary("2026-04-20", &sources, true)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(app.buildRichSummaryPrompt("2026-04-20", data), "Opening rotation sentence.") {
					t.Fatal("control prompt has no transcript")
				}
				triager := &ActivityTriager{app: app}
				semanticRow := row
				semanticRow.AudioPath = "" // semantic query returns only the transcript path
				seq := triager.buildGroupObservationSequence(sessionGroup{Sessions: []ActivitySessionRow{semanticRow}})
				indices := []int{}
				for i, o := range seq.Observations {
					if o.Source == "meeting_transcript" {
						indices = append(indices, i)
					}
				}
				refs := collectSourceRefs(indices, seq)
				if len(indices) == 0 || !stringSliceContains(refs.SessionIDs, "opening-rotation") {
					t.Fatalf("semantic sources incomplete: %+v", refs)
				}
				finalSource, err := app.activity.store.activitySessionProvenance(row)
				if err != nil {
					t.Fatal(err)
				}
				if err := app.activity.store.UpdateTriageStatus("opening-rotation", "protected"); err != nil {
					t.Fatal(err)
				}
				if app.summarySourcesAllowRead(sources.Sources) || app.summarySourcesAllowRead([]activitySourceProvenance{finalSource}) {
					t.Fatal("protected opening accepted by full or incomplete provenance")
				}
				if got := app.loadDailyMeetings([]ActivitySessionRow{row}); strings.Contains(got, "rotation sentence") {
					t.Fatalf("daily transcript leaked: %s", got)
				}
				if got := app.buildRichSummaryPrompt("2026-04-20", data); strings.Contains(got, "rotation sentence") {
					t.Fatalf("captured prompt leaked: %s", got)
				}
				live, err := app.GetDailySummary("2026-04-20")
				if err != nil {
					t.Fatal(err)
				}
				if got := app.buildRichSummaryPrompt("2026-04-20", live); strings.Contains(got, "rotation sentence") {
					t.Fatalf("live prompt leaked: %s", got)
				}
				if _, err := app.GetMeetingTranscript(row.ID); err == nil {
					t.Fatal("direct transcript reader accepted protected opening")
				}
				seq = triager.buildGroupObservationSequence(sessionGroup{Sessions: []ActivitySessionRow{semanticRow}})
				for _, o := range seq.Observations {
					if strings.Contains(o.Body, "rotation sentence") {
						t.Fatal("semantic stitching read protected opening")
					}
				}
			})
		}
	}
}

type recordingProtectionSummaryRunner struct {
	activitySummaryI18nFakeRunner
	store       *ActivityStore
	momentsDone chan struct{}
}

func (r *recordingProtectionSummaryRunner) ExecuteTask(ctx context.Context, input *agent.TaskInput, cb agent.StreamCallback) (string, error) {
	if strings.Contains(input.Prompt, "STRICT JSON") && strings.Contains(input.Prompt, `"moments"`) {
		id, err := r.activitySummaryI18nFakeRunner.ExecuteTask(ctx, input, cb)
		close(r.momentsDone)
		return id, err
	}
	if strings.Contains(input.Prompt, "daily activity digest") {
		select {
		case <-r.momentsDone:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if err := r.store.UpdateTriageStatus("opening-rotation", "protected"); err != nil {
			return "", err
		}
	}
	return r.activitySummaryI18nFakeRunner.ExecuteTask(ctx, input, cb)
}
func TestWholeCallSummaryPublicationRechecksOpeningOutsideDigestDay(t *testing.T) {
	for _, date := range []string{"2026-04-19", "2026-04-20"} {
		t.Run(date, func(t *testing.T) {
			isolateActivityPrivacyTest(t)
			runner := &recordingProtectionSummaryRunner{momentsDone: make(chan struct{})}
			app := newActivitySummaryI18nTestApp(t, &runner.activitySummaryI18nFakeRunner)
			app.runner = runner
			runner.store = app.activity.store
			reviewTwoRotationCall(t, app, date)
			err := app.GenerateDailySummary("2026-04-20")
			if err == nil || !strings.Contains(err.Error(), "activity source changed") {
				t.Fatalf("published changed whole-call source: %v", err)
			}
			if !runner.hasPromptContaining("Opening rotation sentence.") {
				t.Fatal("control did not generate from opening")
			}
			if _, _, _, _, found := runner.store.GetCachedSummaryForLanguage("2026-04-20", "daily", "zh-CN"); found {
				t.Fatal("protected call summary published")
			}
			if got := runner.store.GetCachedMomentsForLanguage("2026-04-20", "zh-CN"); got != "" {
				t.Fatalf("protected call moments published: %s", got)
			}
		})
	}
}

func TestRejoinedCallAnnotationsSurviveTheMoveToActivity(t *testing.T) {
	// A notes entry in the format the Knowledge writer used last: each
	// slice inside its markers, the user's annotations around and inside
	// them, the transcripts gone. The move keeps every annotation, in order,
	// and none of the generated text.
	app := newMeetingNotesTestApp(t)
	stubMeetingNotesLearn(t)
	start := time.Now().Add(-time.Hour).Truncate(time.Second)
	part := func(id, text string, offset time.Duration) string {
		at := start.Add(offset)
		return fmt.Sprintf("<!-- meeting-session %s %d -->\n## Meeting notes (continued)\n\n%s<!-- /meeting-session -->\n", id, at.UnixMilli(),
			formatTranscriptMessage("Zoom", "## Transcript\n\n"+text+"\n", 3, at, "1m 0s"))
	}
	annotations := []string{"My prefix: keep verbatim.", "My between-section annotation.", "My fix inside the second part.", "## My notes\nKeep this suffix."}
	second := strings.Replace(part("two", "[10:01:00] Other: second generated sentence", time.Minute), "<!-- /meeting-session -->", annotations[2]+"\n<!-- /meeting-session -->", 1)
	body := annotations[0] + "\n\n" + part("one", "[10:00:00] Me: first generated sentence", 0) + "\n" + annotations[1] + "\n\n" +
		second + "\n" + annotations[3] + "\n"
	entryID := meetingKnowledgeEntryID("one-call")
	fm := map[string]any{"type": "Isoform Knowledge", "title": "Meeting notes: Zoom, " + start.Format("Jan 2 15:04"), "source": meetingKnowledgeSource,
		"resource": "yansu://knowledge/" + entryID, "apps": []string{"Zoom"}, "activity_sessions": []string{"one", "two"}, "timestamp": okfTimestamp(start)}
	dir, _ := knowledgeDir()
	if err := os.WriteFile(filepath.Join(dir, entryID+".md"), []byte(renderOKFDocument(fm, body)), 0o600); err != nil {
		t.Fatal(err)
	}
	app.moveMeetingNotesToActivity(context.Background())
	r, ok, err := app.activity.store.meetingNotesByKey("one")
	if err != nil || !ok {
		t.Fatalf("moved notes: ok=%v err=%v", ok, err)
	}
	previous := -1
	for _, text := range annotations {
		i := strings.Index(r.UserNotes, text)
		if i <= previous || strings.Count(r.UserNotes, text) != 1 {
			t.Fatalf("user text lost, repeated or moved: %q\n%s", text, r.UserNotes)
		}
		previous = i
	}
	for _, generated := range []string{"generated sentence", "Zoom call", "Meeting notes", "meeting-session"} {
		if strings.Contains(r.UserNotes, generated) {
			t.Fatalf("generated text kept as the user's: %q\n%s", generated, r.UserNotes)
		}
	}
	if len(r.Slices) != 2 {
		t.Fatalf("slices = %+v", r.Slices)
	}
}

type reviewPrivacyTailRecorder struct {
	finalChunkRecorder
	front activitymon.AppInfo
}

func (r *reviewPrivacyTailRecorder) ReadChunk(time.Duration) (capturedAudioChunk, error) {
	<-r.stop
	chunk := timedChunk(r.source, time.Now(), constSamples(9600, 1000))
	chunk.Duration = 600 * time.Millisecond
	chunk.CapturedApp = r.front
	return chunk, nil
}
func TestShutdownPartialTailEnrichesExcludedMicrophoneHolders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		userVoiceInputAppsMu.Lock()
		old := userVoiceInputApps
		userVoiceInputApps = map[string]string{"com.example.excluded": "Excluded"}
		userVoiceInputAppsMu.Unlock()
		defer func() { userVoiceInputAppsMu.Lock(); userVoiceInputApps = old; userVoiceInputAppsMu.Unlock() }()
		app := &App{activity: &activityMonitorState{buf: activitybuf.NewRingBuffer(16, time.Hour)}}
		holder := micInputSnapshot{Active: true, BundleIDs: []string{"com.example.excluded"}}
		r := &combinedAudioRecorder{newRecorders: func() []audioRecorder {
			return []audioRecorder{&reviewPrivacyTailRecorder{finalChunkRecorder: finalChunkRecorder{source: backgroundListeningSourceMicrophone}, front: activitymon.AppInfo{BundleID: "com.apple.Notes", AppName: "Notes"}}}
		}}
		captured := 0
		samples := 0
		b := &backgroundAudioCapture{recorder: r, micSnapshot: func() micInputSnapshot { return holder }, sample: func(now time.Time) { samples++; app.sampleCaptureState(now) }, annotateChunk: app.annotateBackgroundAudioChunk, shouldTranscribe: app.shouldTranscribeBackgroundAudioChunk, onRecordingChunk: func(capturedAudioChunk) { captured++ }}
		if err := b.start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(600 * time.Millisecond)
		b.stop()
		if samples != 0 {
			t.Fatal("fixture reached sampler; must exercise unsampled tail")
		}
		if captured != 0 {
			t.Fatal("excluded microphone holder's partial shutdown tail passed admission")
		}
	})
}
func TestUnnamedMicrophoneHolderAvailableBeforeRecordingAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		app := &App{activity: &activityMonitorState{buf: activitybuf.NewRingBuffer(16, time.Hour)}}
		r := &stoppableFakeRecorder{chunks: make(chan capturedAudioChunk, 2)}
		captured := []capturedAudioChunk{}
		b := &backgroundAudioCapture{recorder: r, micSnapshot: func() micInputSnapshot { return micInputSnapshot{Active: true} }, annotateChunk: app.annotateBackgroundAudioChunk, shouldTranscribe: app.shouldTranscribeBackgroundAudioChunk, onRecordingChunk: func(c capturedAudioChunk) { captured = append(captured, c) }, running: true, stopCh: make(chan struct{}), captureLoopDone: make(chan struct{})}
		go b.captureLoop()
		for i := 0; i < 2; i++ {
			chunk := timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 1000))
			chunk.CapturedApp = activitymon.AppInfo{BundleID: "us.zoom.xos", AppName: "Zoom", WindowTitle: "Quarterly review"}
			r.chunks <- chunk
		}
		synctest.Wait()
		b.stop()
		if len(captured) != 2 || !captured[1].OtherMicUser || !captured[1].CapturedMeetingCandidate {
			t.Fatalf("unnamed holder lost before candidate admission: %+v", captured)
		}
	})
}

func TestRecordingJournalRecoversOriginalIdentitiesAndPublishesOnce(t *testing.T) {
	for _, failure := range []string{"finalize", "owner-commit", "rotation-commit"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			app := mainChatTestApp(t)
			root := t.TempDir()
			store, err := NewActivityStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			app.activity = &activityMonitorState{store: store, buf: activitybuf.NewRingBuffer(64, time.Hour), capture: failingScreenCapture{}}
			old := meetingFollowupModel
			meetingFollowupModel = func(*App, string) (string, error) { return `{"decisions":[],"followups":[]}`, nil }
			defer func() { meetingFollowupModel = old }()
			engine := &recordingFixtureEngine{fakeLongForm: fakeLongForm{texts: map[int16]string{2000: "The recovered call keeps its original opening."}}}
			app.whisper = &whisperState{server: engine}
			w, err := newMeetingAudioWriter(store.AudioDir())
			if err != nil {
				t.Fatal(err)
			}
			w.SetMeeting("Zoom", "us.zoom.xos", "recovered-one-call")
			w.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(meetingAudioSampleRate, 2000)))
			epoch, _ := app.automaticMeetingRecordingEpoch()
			if failure == "rotation-commit" {
				app.activity.inMeeting, app.activity.meetingAppName, app.activity.meetingBundleID = true, "Zoom", "us.zoom.xos"
				app.activity.meetingStartMs, app.activity.meetingRecordingEpoch, app.activity.meetingAudioWriter = time.Now().Add(-time.Second).UnixMilli(), epoch, w
				app.activity.buf.Push(activitymon.ActivityEvent{Kind: activitymon.EventClick, Timestamp: time.Now().UnixMilli(), AppName: "Zoom", AppBundleID: "us.zoom.xos"})
			}
			if failure != "finalize" {
				if _, err := store.db.Exec(`CREATE TRIGGER fail_call_commit BEFORE INSERT ON activity_sessions BEGIN SELECT RAISE(ABORT,'injected call commit failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			blocked := ""
			if failure == "finalize" {
				blocked = filepath.Join(w.audioDir, w.relDir, fmt.Sprintf("%d-%s.wav", w.metaSnapshot().StartMs, w.id))
				if err := os.Mkdir(blocked, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "rotation-commit" {
				app.extractSession(meetingSliceTrigger)
			} else if _, _, ok := app.saveCallRecordingLocked(w, epoch, "meeting_ended"); ok {
				t.Fatal("injected failure did not fail")
			}
			pending := w.metaSnapshot()
			if len(pending.PendingSources) != 1 {
				t.Fatalf("pending identity not journaled: %+v", pending)
			}
			original := pending.PendingSources[0].SessionID
			if _, err := store.GetSessionByID(original); err == nil {
				t.Fatal("injected failed row exists")
			}
			if failure == "finalize" {
				if err := os.Remove(blocked); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			} else {
				if _, err := store.db.Exec(`DROP TRIGGER fail_call_commit`); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "rotation-commit" {
				if _, _, ok := app.saveCallRecordingLocked(w, epoch, "meeting_ended"); !ok {
					t.Fatal("could not save owner after failed rotation")
				}
			}
			// Reopen both the database and journal reader, as a new process would.
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = NewActivityStore(root)
			if err != nil {
				t.Fatal(err)
			}
			app.activity.store = store
			for i := 0; i < 2; i++ {
				store.recoverMeetingAudioTracks(time.Now().Add(time.Hour), false)
			}
			row, err := store.GetSessionByID(original)
			if err != nil || row == nil {
				t.Fatalf("original identity not recovered: %+v %v", row, err)
			}
			calls := meetingSessions(t, store)
			audioCount := 0
			var call ActivitySessionRow
			for _, s := range calls {
				if s.AudioPath != "" {
					audioCount++
					call = s
				}
			}
			if audioCount != 1 {
				t.Fatalf("recovery duplicated or lost call: %+v", calls)
			}
			if failure == "rotation-commit" && row.EndTime > call.StartTime {
				t.Fatalf("recovered activity rotations overlap: %+v %+v", row, call)
			}
			if failure != "rotation-commit" && call.ID != original {
				t.Fatalf("recovery replaced original owner: %s != %s", call.ID, original)
			}
			if !app.meetingTranscriptAllowed(call, nil) {
				t.Fatalf("recovered provenance blocks call: %+v", readMeetingWAVMeta(store.AudioDir(), call.AudioPath))
			}
			app.recoverUntranscribedMeetings()
			count := engine.calls
			app.recoverUntranscribedMeetings()
			if count == 0 || engine.calls != count {
				t.Fatalf("call not transcribed once: %d then %d", count, engine.calls)
			}
			callPtr, err := store.GetSessionByID(call.ID)
			if err != nil || callPtr.TranscriptPath == "" {
				t.Fatalf("recovered transcript missing: %+v %v", callPtr, err)
			}
			raw, err := os.ReadFile(callPtr.TranscriptPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(raw), "The recovered call keeps its original opening.") != 1 {
				t.Fatalf("recovered text missing or repeated: %s", raw)
			}
			keys, err := store.recentMeetingNotes(0)
			if err != nil || len(keys) != 1 {
				t.Fatalf("recovered meeting count: %v %v", keys, err)
			}
			notes, err := app.GetMeetingNotesForSessions([]string{original})
			if err != nil || len(notes) != 1 {
				t.Fatalf("the recovered call is not on Activity under its original session: %+v %v", notes, err)
			}
			if transcript, err := app.GetMeetingNotesTranscript(call.ID); err != nil || strings.Count(transcript, "The recovered call keeps its original opening.") != 1 {
				t.Fatalf("the notes' transcript is incomplete: %v", err)
			}
		})
	}
}

func TestRecordingRecoveryNeverInventsMissingCommittedSources(t *testing.T) {
	for _, mode := range []string{"unknown-committed", "removed-pending", "protected-committed"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			store := newLifecycleStore(t)
			app := &App{activity: &activityMonitorState{store: store}}
			w, err := newMeetingAudioWriter(store.AudioDir())
			if err != nil {
				t.Fatal(err)
			}
			w.SetMeeting("Zoom", "us.zoom.xos", "blocked-call")
			w.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 2000)))
			owner := "journal-owner"
			other := "committed-dependency"
			if mode == "unknown-committed" {
				w.NoteActivitySession(other)
			} else {
				if err := store.SaveSession(ActivitySessionRow{ID: other, AppNames: `["Zoom"]`, TriageStatus: "protected", StartTime: 1, EndTime: 2}, nil, nil); err != nil {
					t.Fatal(err)
				}
				if err := w.completeActivitySession(activitySourceProvenance{SessionID: other, StartTime: 1, EndTime: 2}, false); err != nil {
					t.Fatal(err)
				}
				if mode == "removed-pending" {
					if err := store.DeleteSession(other); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := w.completeActivitySession(activitySourceProvenance{SessionID: owner, StartTime: 1, EndTime: 2}, true); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.Finalize(); err != nil {
				t.Fatal(err)
			}
			store.recoverMeetingAudioTracks(time.Now().Add(time.Hour), false)
			if mode == "protected-committed" {
				current, err := store.GetSessionByID(other)
				if err != nil || current.TriageStatus != "protected" {
					t.Fatal("pending journal overwrote protected committed row")
				}
			}
			for _, row := range meetingSessions(t, store) {
				if row.AudioPath != "" && app.meetingTranscriptAllowed(row, nil) {
					t.Fatal("missing or protected dependency became readable")
				}
			}
			if mode != "protected-committed" {
				if _, err := store.GetSessionByID(other); err == nil {
					t.Fatal("missing committed/deleted identity recreated")
				}
			}
		})
	}
}

func TestWholeCallSummaryRetentionKeepsOutsideDayDependency(t *testing.T) {
	isolateActivityPrivacyTest(t)
	app := newActivitySummaryI18nTestApp(t, &activitySummaryI18nFakeRunner{})
	row := reviewTwoRotationCall(t, app, "2026-04-19")
	store := app.activity.store
	if _, err := store.db.Exec(`UPDATE activity_sessions SET app_names='["com.example.opening"]' WHERE id='opening-rotation'`); err != nil {
		t.Fatal(err)
	}
	sources, err := app.captureSummarySources("2026-04-20", "daily")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.saveSummaryWithSources("2026-04-20", "daily", "Opening rotation sentence.", "test", false, "zh-CN", sources.Sources); err != nil {
		t.Fatal(err)
	}
	if err := store.deleteRetainedSession("opening-rotation"); err != nil {
		t.Fatal(err)
	}
	snapshot, found := store.cachedActivitySummarySnapshot("2026-04-20", "daily", "zh-CN")
	if !found || !app.activitySummarySnapshotAllowsRead("2026-04-20", "daily", snapshot) {
		t.Fatal("safe retained outside-day source blocked summary")
	}
	if got, err := app.GetMeetingTranscript(row.ID); err != nil || !strings.Contains(got, "Opening rotation sentence.") {
		t.Fatalf("safe retained transcript blocked: %v", err)
	}
	pii.AddUserSensitiveApp("com.example.opening", "Opening")
	if app.activitySummarySnapshotAllowsRead("2026-04-20", "daily", snapshot) {
		t.Fatal("retained outside-day dependency did not restrict summary")
	}
	if _, err := app.GetMeetingTranscript(row.ID); err == nil {
		t.Fatal("retained protected opening authorized transcript")
	}
}

func TestRecordingPendingPrivateProofSurvivesRecovery(t *testing.T) {
	root := t.TempDir()
	store, err := NewActivityStore(root)
	if err != nil {
		t.Fatal(err)
	}
	w, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	w.SetMeeting("Zoom", "us.zoom.xos", "private-pending-call")
	w.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 2000)))
	if err := w.completeActivitySession(activitySourceProvenance{SessionID: "private-rotation", Private: true, StartTime: 1, EndTime: 2}, false); err != nil {
		t.Fatal(err)
	}
	if err := w.completeActivitySession(activitySourceProvenance{SessionID: "private-owner", StartTime: 2, EndTime: 3}, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewActivityStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.recoverMeetingAudioTracks(time.Now().Add(time.Hour), false)
	first, err := store.GetSessionByID("private-rotation")
	if err != nil || first.TriageStatus != "protected" {
		t.Fatalf("lost pending privacy: %+v %v", first, err)
	}
	owner, err := store.GetSessionByID("private-owner")
	if err != nil || owner.AudioPath == "" {
		t.Fatalf("lost recording owner: %+v %v", owner, err)
	}
	app := &App{activity: &activityMonitorState{store: store}}
	if app.meetingTranscriptAllowed(*owner, nil) {
		t.Fatal("recovered private rotation authorized whole call")
	}
}

func TestRecordingRecoveryDoesNotReattachRetainedAudio(t *testing.T) {
	store := newLifecycleStore(t)
	w, err := newMeetingAudioWriter(store.AudioDir())
	if err != nil {
		t.Fatal(err)
	}
	w.SetMeeting("Zoom", "us.zoom.xos", "finished-call")
	w.WriteCapturedChunk(timedChunk(backgroundListeningSourceMicrophone, time.Now(), constSamples(16000, 2000)))
	const owner = "finished-owner"
	if err := w.completeActivitySession(activitySourceProvenance{SessionID: owner, StartTime: 1, EndTime: 2}, true); err != nil {
		t.Fatal(err)
	}
	path, size, err := w.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSession(ActivitySessionRow{ID: owner, StartTime: 1, EndTime: 2, AppNames: `["Zoom"]`, IsMeeting: true, MeetingApp: "Zoom", AudioPath: path, AudioSizeBytes: size}, nil, nil); err != nil {
		t.Fatal(err)
	}
	w.commitActivitySession(owner)
	// Retention/cleanup may clear the DB reference before removing the file.
	if _, err := store.db.Exec(`UPDATE activity_sessions SET audio_path='',audio_size_bytes=0 WHERE id=?`, owner); err != nil {
		t.Fatal(err)
	}
	store.recoverMeetingAudioTracks(time.Now().Add(time.Hour), false)
	row, err := store.GetSessionByID(owner)
	if err != nil || row.AudioPath != "" {
		t.Fatalf("recovery revived retired media: %+v %v", row, err)
	}
}
