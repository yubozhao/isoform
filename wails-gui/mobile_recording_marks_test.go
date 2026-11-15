package main

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/platform/activitymon"
)

// activitymonSpeech is one line of a stored transcript: seconds from the
// recording's start, who spoke, and the words.
type activitymonSpeech struct {
	after   int
	speaker string
	text    string
}

// saveWatchSpeech stores a recording's transcript as its activity session and
// marks the recording transcribed.
func saveWatchSpeech(t *testing.T, app *App, store *mobileRecordingStore, id string, started time.Time, speech []activitymonSpeech) {
	t.Helper()
	var events []activitymon.ActivityEvent
	for _, line := range speech {
		events = append(events, activitymon.ActivityEvent{Timestamp: started.Add(time.Duration(line.after) * time.Second).UnixMilli(),
			Kind: activitymon.EventSpeech, AppName: mobileRecordingAppName, SpeechText: line.text, SpeechSpeakerLabel: line.speaker})
	}
	session := ActivitySessionRow{ID: "rec-" + id, StartTime: started.UnixMilli(), EndTime: started.Add(time.Hour).UnixMilli(),
		TriggerKind: mobileRecordingTrigger, AppNames: `["` + mobileRecordingAppName + `"]`, EventCount: len(events),
		TriageStatus: "pending", CreatedAt: time.Now().UnixMilli()}
	if err := app.activity.store.SaveSession(session, events, nil); err != nil {
		t.Fatal(err)
	}
	store.setState(id, mobileRecordingTranscribed, session.ID, 8, "")
}

func TestMarksAreKeptInsideThePartInOrderWithoutRepeats(t *testing.T) {
	got := sanitizeMobileRecordingMarks([]float64{120.04, 5, 5.01, -1, 901, math.NaN(), math.Inf(1), 900}, 900)
	want := []float64{5, 120, 900}
	if len(got) != len(want) {
		t.Fatalf("marks %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("marks %v, want %v", got, want)
		}
	}
	if sanitizeMobileRecordingMarks(nil, 900) != nil || sanitizeMobileRecordingMarks([]float64{-3}, 900) != nil {
		t.Fatal("no usable mark must store nothing")
	}
	many := make([]float64, 200)
	for i := range many {
		many[i] = float64(i)
	}
	if got := sanitizeMobileRecordingMarks(many, 900); len(got) != mobileRecordingMaxMarks {
		t.Fatalf("%d marks kept", len(got))
	}
}

func TestAMarkPointsAtTheWordsBeingSaid(t *testing.T) {
	at := func(seconds int) int64 { return time.Date(2026, 11, 14, 10, 0, seconds, 0, time.UTC).UnixMilli() }
	lines := func() []WatchRecordingLine {
		return []WatchRecordingLine{{At: at(0), Text: "a"}, {At: at(40), Text: "b"}, {At: at(300), Text: "c"}}
	}
	marked := func(marks ...int64) string {
		got := lines()
		markWatchRecordingLines(got, marks)
		var out []string
		for _, line := range got {
			if line.Marked {
				out = append(out, line.Text)
			}
		}
		return strings.Join(out, ",")
	}
	if got := marked(at(45)); got != "b" {
		t.Fatalf("a mark five seconds into b marked %q", got)
	}
	if got := marked(at(40)); got != "b" {
		t.Fatalf("a mark as b begins marked %q", got)
	}
	// Two minutes of quiet after b: too long ago to be what was meant, and c is too far ahead.
	if got := marked(at(160)); got != "" {
		t.Fatalf("a mark in a long quiet marked %q", got)
	}
	// Just before c begins: the wearer marked as it started.
	if got := marked(at(285)); got != "c" {
		t.Fatalf("a mark fifteen seconds before c marked %q", got)
	}
	if got := marked(at(45), at(310)); got != "b,c" {
		t.Fatalf("two marks marked %q", got)
	}
	markWatchRecordingLines(nil, []int64{at(1)})
}

// A part carries the moments the wearer marked. The computer stores them
// with the part, shows the marked words on its own page and to the phone,
// and forgets them with the recording.
func TestMarksTravelWithAPartAndGoWithTheRecording(t *testing.T) {
	app, bridge, store, _ := watchDeskFixture(t)
	uploads := &mapUploads{bodies: map[string][]byte{}}
	id := uuid.NewString()
	started := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	req := sealedRecordingPart(t, uploads, mobileTestDeviceID, id, 0, true, []byte("audio"), started)
	req.DurationSeconds = 600
	req.Marks = []float64{12, 5000}
	raw, _ := json.Marshal(req)
	var decoded MobileRecordingRequest
	if err := mobileDecodeStrict(raw, &decoded); err != nil || len(decoded.Marks) != 2 {
		t.Fatalf("the strict decoder and the marks: %v %+v", err, decoded.Marks)
	}
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, mobileTestDeviceID, req); err != nil {
		t.Fatal(err)
	}
	rec, _ := store.get(id, "")
	if len(rec.Received) != 1 || len(rec.Received[0].Marks) != 1 || rec.Received[0].Marks[0] != 12 {
		t.Fatalf("stored marks %+v; the one past the part's end must be dropped", rec.Received)
	}
	events := []activitymonSpeech{{10, "you", "This is the number that matters"}, {200, "speaker_1", "Noted"}}
	saveWatchSpeech(t, app, store, id, started, events)

	lines, err := app.GetWatchRecordingTranscript(id)
	if err != nil || len(lines) != 2 || !lines[0].Marked || lines[1].Marked {
		t.Fatalf("lines %+v %v", lines, err)
	}
	phone := watchDeskCall(bridge, "GET", "/recordings/transcript?id="+id, "", mobileScopeChatWrite, mobileScopeBriefRead).Body.String()
	if strings.Count(phone, `"marked":true`) != 1 {
		t.Fatalf("the phone's transcript: %s", phone)
	}
	status, _ := json.Marshal(mobileStatusView{RecordingMarks: true})
	if !strings.Contains(string(status), `"recordingMarks":true`) {
		t.Fatalf("status %s", status)
	}

	if _, err := app.deleteWatchRecording(store, id, ""); err != nil {
		t.Fatal(err)
	}
	rec, _ = store.get(id, "")
	for _, part := range rec.Received {
		if len(part.Marks) != 0 {
			t.Fatalf("marks outlived the deleted recording: %+v", part.Marks)
		}
	}
}
