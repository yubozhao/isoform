package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	briefcore "wails-gui/internal/brief"
	"wails-gui/storage"
)

// watchMarkFixture is the watch desk fixture with a state store, so the pause
// log can be read (the gather fails closed without it). The fixture's own
// recording has no marks.
func watchMarkFixture(t *testing.T) (*App, *mobileRecordingStore) {
	t.Helper()
	app, _, store, _ := watchDeskFixture(t)
	state, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	app.store = state
	return app, store
}

// markedWatchHour stores one transcribed watch recording that began at
// started, with marks (seconds from its start) and its speech.
func markedWatchHour(t *testing.T, app *App, store *mobileRecordingStore, started time.Time, marks []float64, speech []activitymonSpeech) string {
	t.Helper()
	uploads := &mapUploads{bodies: map[string][]byte{}}
	id := uuid.NewString()
	req := sealedRecordingPart(t, uploads, mobileTestDeviceID, id, 0, true, []byte("audio"), started)
	req.DurationSeconds = 600
	req.Marks = marks
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, mobileTestDeviceID, req); err != nil {
		t.Fatal(err)
	}
	saveWatchSpeech(t, app, store, id, started, speech)
	return id
}

func watchMarkInput(now time.Time) mobileBriefInput {
	return mobileBriefInput{Slot: "evening", WindowStart: now.Add(-12 * time.Hour), AsOf: now, Location: time.UTC}
}

// The moments the wearer marked reach the brief, newest first, with the
// owner's own words for the writer and never another person's.
func TestMarkedWatchMomentsReachTheBrief(t *testing.T) {
	app, store := watchMarkFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	own := markedWatchHour(t, app, store, now.Add(-3*time.Hour), []float64{12},
		[]activitymonSpeech{{10, "you", "The pricing number is the one that matters"}, {200, "speaker_1", "Noted"}})
	theirs := markedWatchHour(t, app, store, now.Add(-2*time.Hour), []float64{205},
		[]activitymonSpeech{{10, "you", "Go on"}, {200, "speaker_1", "My salary is a secret"}})
	markedWatchHour(t, app, store, now.Add(-90*time.Minute), nil, []activitymonSpeech{{10, "you", "Nothing marked here"}})

	in := watchMarkInput(now)
	collectMobileBriefWatchMarks(app, &in)
	if len(in.Marked) != 2 {
		t.Fatalf("marked candidates %+v, want one for each recording with a mark", in.Marked)
	}
	first, second := in.Marked[0], in.Marked[1]
	if first.SourceID != "rec-"+theirs || second.SourceID != "rec-"+own || first.SourceKind != "activity_session" {
		t.Fatalf("order or source: %+v", in.Marked)
	}
	if first.Title == second.Title || first.Title == "" {
		t.Fatalf("titles must differ: %q %q", first.Title, second.Title)
	}
	if first.writerDetail != "" {
		t.Fatalf("another person's words reached the writer: %q", first.writerDetail)
	}
	if !strings.Contains(second.writerDetail, "The pricing number is the one that matters") || !strings.Contains(second.writerDetail, "never quote") {
		t.Fatalf("the owner's marked words: %q", second.writerDetail)
	}
	for _, c := range in.Marked {
		if strings.Contains(c.Title+c.Detail+c.writerDetail, "salary") {
			t.Fatalf("another person's words are in the candidate: %+v", c)
		}
	}

	// The writer's material carries them first, flagged, dated by a day word only.
	brief := buildMobileBriefV1(in)
	m, sources := newBriefMaterial(in, brief, briefPartDone, "")
	if len(m.Items) < 2 || !m.Items[0].Marked || !m.Items[1].Marked {
		t.Fatalf("marked moments must come first in the material: %+v", m.Items)
	}
	for _, item := range m.Items[:2] {
		// A few hours back is today, or yesterday just after midnight: a day word, never a clock.
		if item.Group != briefcore.GroupDone || (item.When != "today" && item.When != "yesterday") {
			t.Fatalf("marked item %+v", item)
		}
		if src := sources[item.ID]; src.kind != "activity_session" || !strings.HasPrefix(src.id, "rec-") {
			t.Fatalf("source of %s: %+v", item.ID, src)
		}
	}
	for _, item := range m.Items[2:] {
		if item.Marked {
			t.Fatalf("an item that was not marked carries the flag: %+v", item)
		}
	}
}

// Closed Eyes wins, and deleted means deleted: nothing from a pause, a paused
// app, or a deleted recording, and nothing older than the brief's window.
func TestMarkedWatchMomentsRespectPausesDeletionAndTheWindow(t *testing.T) {
	app, store := watchMarkFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	speech := []activitymonSpeech{{10, "you", "Remember this"}}
	deleted := markedWatchHour(t, app, store, now.Add(-3*time.Hour), []float64{12}, speech)
	markedWatchHour(t, app, store, now.Add(-30*time.Hour), []float64{12}, speech)
	kept := markedWatchHour(t, app, store, now.Add(-2*time.Hour), []float64{12}, speech)

	if _, err := app.deleteWatchRecording(store, deleted, ""); err != nil {
		t.Fatal(err)
	}
	in := watchMarkInput(now)
	collectMobileBriefWatchMarks(app, &in)
	if len(in.Marked) != 1 || in.Marked[0].SourceID != "rec-"+kept {
		t.Fatalf("only the recording in the window that still exists may be there: %+v", in.Marked)
	}

	// Without a readable pause log nothing is gathered.
	state := app.store
	app.store = nil
	blind := watchMarkInput(now)
	collectMobileBriefWatchMarks(app, &blind)
	app.store = state
	if len(blind.Marked) != 0 {
		t.Fatalf("marks were gathered without the pause log: %+v", blind.Marked)
	}

	// While observing is paused nothing is gathered.
	app.closedEyes.enabled.Store(true)
	paused := watchMarkInput(now)
	collectMobileBriefWatchMarks(app, &paused)
	app.closedEyes.enabled.Store(false)
	if len(paused.Marked) != 0 {
		t.Fatalf("marks were gathered while observing was paused: %+v", paused.Marked)
	}
}

func TestMarkedWatchMomentsAreCappedAndOwnWordsAreChecked(t *testing.T) {
	app, store := watchMarkFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < mobileBriefWatchMarkLimit+2; i++ {
		markedWatchHour(t, app, store, now.Add(-time.Duration(i+2)*time.Hour), []float64{12}, []activitymonSpeech{{10, "you", "Remember this"}})
	}
	in := watchMarkInput(now)
	in.WindowStart = now.Add(-24 * time.Hour)
	collectMobileBriefWatchMarks(app, &in)
	if len(in.Marked) != mobileBriefWatchMarkLimit {
		t.Fatalf("%d marked candidates, want %d", len(in.Marked), mobileBriefWatchMarkLimit)
	}
	titles := map[string]bool{}
	for _, c := range in.Marked {
		titles[c.Title] = true
	}
	if len(titles) != mobileBriefWatchMarkLimit {
		t.Fatalf("titles repeat: %v", titles)
	}
	if got := mobileBriefWatchMarkOwnWords("   "); got != "" {
		t.Fatalf("empty words: %q", got)
	}
	if got := mobileBriefWatchMarkOwnWords(strings.Repeat("word ", 200)); len(got) > mobileBriefWatchMarkWords {
		t.Fatalf("words not bounded: %d", len(got))
	}
}
