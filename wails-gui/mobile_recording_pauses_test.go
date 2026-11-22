package main

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/asr"
	"wails-gui/internal/platform/activitymon"
)

func TestPauseCoverOfAStretch(t *testing.T) {
	at := func(minute int) int64 { return time.Date(2026, 11, 14, 10, minute, 0, 0, time.UTC).UnixMilli() }
	cases := []struct {
		name       string
		start, end int
		pauses     []observationPause
		want       mobileRecordingPauseOverlap
	}{
		{"no pauses", 0, 15, nil, mobileRecordingPauseNone},
		{"pause before", 10, 20, []observationPause{{Start: at(0), End: at(5)}}, mobileRecordingPauseNone},
		{"pause ends as it begins", 10, 20, []observationPause{{Start: at(0), End: at(10)}}, mobileRecordingPauseNone},
		{"pause begins as it ends", 10, 20, []observationPause{{Start: at(20), End: at(30)}}, mobileRecordingPauseNone},
		{"pause after", 0, 5, []observationPause{{Start: at(10), End: at(20)}}, mobileRecordingPauseNone},
		{"pause inside", 0, 15, []observationPause{{Start: at(4), End: at(6)}}, mobileRecordingPausePartial},
		{"pause over the start", 5, 20, []observationPause{{Start: at(0), End: at(6)}}, mobileRecordingPausePartial},
		{"pause over the end", 5, 20, []observationPause{{Start: at(19), End: at(40)}}, mobileRecordingPausePartial},
		{"pause covers it", 5, 20, []observationPause{{Start: at(0), End: at(40)}}, mobileRecordingPauseFull},
		{"pause is exactly it", 5, 20, []observationPause{{Start: at(5), End: at(20)}}, mobileRecordingPauseFull},
		{"open pause covers it", 5, 20, []observationPause{{Start: at(1)}}, mobileRecordingPauseFull},
		{"open pause from inside", 5, 20, []observationPause{{Start: at(9)}}, mobileRecordingPausePartial},
		{"two pauses that join cover it", 5, 20, []observationPause{{Start: at(12), End: at(30)}, {Start: at(0), End: at(12)}}, mobileRecordingPauseFull},
		{"two pauses with a gap", 5, 20, []observationPause{{Start: at(0), End: at(8)}, {Start: at(9), End: at(30)}}, mobileRecordingPausePartial},
	}
	for _, c := range cases {
		if got := mobileRecordingPauseCover(at(c.start), at(c.end), c.pauses); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// A person pauses observing from 10:20 to 10:40 while the watch records
// 10:00 to 11:00 in four parts. The part recorded wholly inside the pause is
// never read; the two parts the pause touches give only their words outside
// it; and the audio of all three is deleted.
func TestWatchAudioFromAPauseIsNeverTranscribedOrKept(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	hour := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	// Parts: 10:00-10:15, 10:15-10:25, 10:25-10:35 (inside), 10:35-11:00.
	starts := []time.Duration{0, 15 * time.Minute, 25 * time.Minute, 35 * time.Minute}
	lengths := []float64{900, 600, 600, 1500}
	for i := range starts {
		req := sealedRecordingPart(t, uploads, "dev-a", recordingID, i, i == len(starts)-1, []byte("audio"), hour.Add(starts[i]))
		req.DurationSeconds = lengths[i]
		if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
			t.Fatal(err)
		}
	}
	pauses := []observationPause{{Start: hour.Add(20 * time.Minute).UnixMilli(), End: hour.Add(40 * time.Minute).UnixMilli()}}
	oldP, oldT, oldS := mobileRecordingPauses, mobileRecordingTranscriber, mobileRecordingSaver
	t.Cleanup(func() { mobileRecordingPauses, mobileRecordingTranscriber, mobileRecordingSaver = oldP, oldT, oldS })
	mobileRecordingPauses = func(*App) ([]observationPause, error) { return pauses, nil }
	// No ffmpeg here: paused minutes that cannot be cut out take the whole file with them.
	oldF := mobileRecordingFFmpegPath
	t.Cleanup(func() { mobileRecordingFFmpegPath = oldF })
	mobileRecordingFFmpegPath = func() string { return "" }

	app := &App{}
	// On receipt the part inside the pause goes at once; the others are still needed.
	app.dropPausedMobileRecordingOnReceipt(store, recordingID)
	exists := func(part string) bool {
		_, err := os.Stat(filepath.Join(store.dir, recordingID, part))
		return err == nil
	}
	if exists("part-02.m4a") || !exists("part-00.m4a") || !exists("part-01.m4a") || !exists("part-03.m4a") {
		t.Fatalf("after receipt: 00=%t 01=%t 02=%t 03=%t", exists("part-00.m4a"), exists("part-01.m4a"), exists("part-02.m4a"), exists("part-03.m4a"))
	}

	var read []string
	mobileRecordingTranscriber = func(_ *App, path string) ([]mobileRecordingSegment, error) {
		read = append(read, filepath.Base(path))
		// Each part says something two minutes in and seven minutes in.
		return mobileRecordingSpeakerSegments([]asr.Segment{
			{Start: 2 * time.Minute, End: 2*time.Minute + 3*time.Second, Text: "early in " + filepath.Base(path)},
			{Start: 7 * time.Minute, End: 7*time.Minute + 3*time.Second, Text: "late in " + filepath.Base(path)},
		}, func(asr.Segment) (*bool, []float32) { return nil, nil }), nil
	}
	var saved []activitymon.ActivityEvent
	mobileRecordingSaver = func(_ *App, _ ActivitySessionRow, events []activitymon.ActivityEvent) error {
		saved = events
		return nil
	}
	app.transcribeMobileRecordings(context.Background(), store)

	if strings.Join(read, ",") != "part-00.m4a,part-01.m4a,part-03.m4a" {
		t.Fatalf("transcribed %v; the part inside the pause must never be read", read)
	}
	var words []string
	for _, evt := range saved {
		if pausedAt(pauses, time.UnixMilli(evt.Timestamp)) {
			t.Fatalf("speech from the pause was saved: %q at %s", evt.SpeechText, time.UnixMilli(evt.Timestamp).UTC())
		}
		words = append(words, evt.SpeechText)
	}
	// 10:02, 10:07 (part 0); 10:17 (part 1; its 10:22 is paused); 10:42 (part 3; its 10:37 is paused).
	want := "early in part-00.m4a|late in part-00.m4a|early in part-01.m4a|late in part-03.m4a"
	if strings.Join(words, "|") != want {
		t.Fatalf("saved %q, want %q", strings.Join(words, "|"), want)
	}
	if !exists("part-00.m4a") || exists("part-01.m4a") || exists("part-02.m4a") || exists("part-03.m4a") {
		t.Fatalf("after transcription: 00=%t 01=%t 02=%t 03=%t; audio a pause touched must be gone",
			exists("part-00.m4a"), exists("part-01.m4a"), exists("part-02.m4a"), exists("part-03.m4a"))
	}
	views, _ := store.list("dev-a", []string{recordingID}, 50, false)
	if len(views) != 1 || views[0].State != mobileRecordingTranscribed {
		t.Fatalf("views %+v", views)
	}
}

func TestAnHourRecordedWhollyInAPauseLeavesNothing(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	started := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)
	req := sealedRecordingPart(t, uploads, "dev-a", recordingID, 0, true, []byte("audio"), started)
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}
	oldP, oldT, oldS := mobileRecordingPauses, mobileRecordingTranscriber, mobileRecordingSaver
	t.Cleanup(func() { mobileRecordingPauses, mobileRecordingTranscriber, mobileRecordingSaver = oldP, oldT, oldS })
	mobileRecordingPauses = func(*App) ([]observationPause, error) {
		return []observationPause{{Start: started.Add(-time.Hour).UnixMilli(), End: started.Add(2 * time.Hour).UnixMilli()}}, nil
	}
	mobileRecordingTranscriber = func(*App, string) ([]mobileRecordingSegment, error) {
		t.Fatal("audio from a pause was read")
		return nil, nil
	}
	mobileRecordingSaver = func(*App, ActivitySessionRow, []activitymon.ActivityEvent) error {
		t.Fatal("a session was saved from a pause")
		return nil
	}
	app := &App{}
	app.transcribeMobileRecordings(context.Background(), store)
	if _, err := os.Stat(filepath.Join(store.dir, recordingID, "part-00.m4a")); err == nil {
		t.Fatal("audio from a pause is still on disk")
	}
	views, _ := store.list("dev-a", []string{recordingID}, 50, false)
	if len(views) != 1 || views[0].State != mobileRecordingTranscribed || views[0].Words != 0 {
		t.Fatalf("views %+v", views)
	}
	if len(store.pending()) != 0 {
		t.Fatal("it is still pending")
	}
}

func TestWithoutThePauseLogNothingIsTranscribed(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	started := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)
	req := sealedRecordingPart(t, uploads, "dev-a", recordingID, 0, true, []byte("audio"), started)
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}
	oldP, oldT := mobileRecordingPauses, mobileRecordingTranscriber
	t.Cleanup(func() { mobileRecordingPauses, mobileRecordingTranscriber = oldP, oldT })
	mobileRecordingPauses = func(*App) ([]observationPause, error) { return nil, os.ErrPermission }
	mobileRecordingTranscriber = func(*App, string) ([]mobileRecordingSegment, error) {
		t.Fatal("audio was read without knowing when observing was paused")
		return nil, nil
	}
	app := &App{}
	app.dropPausedMobileRecordingOnReceipt(store, recordingID)
	app.transcribeMobileRecordings(context.Background(), store)
	views, _ := store.list("dev-a", []string{recordingID}, 50, false)
	if len(views) != 1 || views[0].State != mobileRecordingFailed {
		t.Fatalf("views %+v", views)
	}
	// The audio waits for a later try.
	if _, err := os.Stat(filepath.Join(store.dir, recordingID, "part-00.m4a")); err != nil {
		t.Fatal("the audio was removed although nothing said it was paused")
	}
}

func TestKeptSpansLeaveASecondBeforeAndAfterAPause(t *testing.T) {
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	ms := func(seconds int) int64 { return start.Add(time.Duration(seconds) * time.Second).UnixMilli() }
	spans := mobileRecordingKeptSpans(ms(0), ms(60), []observationPause{{Start: ms(20), End: ms(40)}}, time.Second)
	if len(spans) != 2 || spans[0] != (mobileRecordingAudioSpan{0, 19}) || spans[1] != (mobileRecordingAudioSpan{41, 60}) {
		t.Fatalf("spans %+v", spans)
	}
	// An open pause keeps nothing after it; a sliver between two pauses is not kept.
	spans = mobileRecordingKeptSpans(ms(0), ms(60), []observationPause{{Start: ms(10), End: ms(20)}, {Start: ms(22)}}, time.Second)
	if len(spans) != 1 || spans[0] != (mobileRecordingAudioSpan{0, 9}) {
		t.Fatalf("spans %+v", spans)
	}
	if spans := mobileRecordingKeptSpans(ms(0), ms(60), []observationPause{{Start: ms(-5), End: ms(90)}}, time.Second); len(spans) != 0 {
		t.Fatalf("spans %+v", spans)
	}
}

// decodeTestPCM decodes an audio file to 16 kHz mono samples with ffmpeg.
func decodeTestPCM(t *testing.T, ffmpeg, path string) []int16 {
	t.Helper()
	raw, err := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-i", path, "-ac", "1", "-ar", "16000", "-f", "s16le", "-").Output()
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	samples := make([]int16, len(raw)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	return samples
}

func peakOf(samples []int16) float64 {
	peak := 0.0
	for _, v := range samples {
		peak = math.Max(peak, math.Abs(float64(v)))
	}
	return peak
}

// The real cut, with the real ffmpeg: one minute of audio that is silent
// except for a loud tone from 0:20 to 0:40, and a pause over exactly those
// seconds. After the cut the file is the 38 seconds outside the pause (less a
// second on each side of it) and not one sample of the tone is left.
func TestNoPausedSecondSurvivesInATouchedPartsAudio(t *testing.T) {
	ffmpeg := testFFmpeg(t)
	source := filepath.Join(t.TempDir(), "source.m4a")
	if out, err := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-y", "-f", "lavfi",
		"-i", "sine=frequency=1000:duration=60:sample_rate=16000",
		"-af", "volume='if(between(t,20,40),1,0)':eval=frame",
		"-ac", "1", "-c:a", "aac", "-b:a", "24k", source).CombinedOutput(); err != nil {
		t.Fatalf("make source: %v: %s", err, out)
	}
	audio, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	// The source really holds the tone, or the check below would prove nothing.
	if peak := peakOf(decodeTestPCM(t, ffmpeg, source)); peak < 3000 {
		t.Fatalf("the source has no tone to cut (peak %.0f)", peak)
	}

	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	started := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	req := sealedRecordingPart(t, uploads, "dev-a", recordingID, 0, true, audio, started)
	req.DurationSeconds = 60
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}
	oldF := mobileRecordingFFmpegPath
	t.Cleanup(func() { mobileRecordingFFmpegPath = oldF })
	mobileRecordingFFmpegPath = func() string { return ffmpeg }
	pauses := []observationPause{{Start: started.Add(20 * time.Second).UnixMilli(), End: started.Add(40 * time.Second).UnixMilli()}}

	// On receipt a touched part is left whole: its other words are still to be read.
	if n := store.dropPausedAudio(recordingID, pauses, true); n != 0 {
		t.Fatalf("changed %d parts on receipt", n)
	}
	oldPauses, oldTranscriber := mobileRecordingPauses, mobileRecordingTranscriber
	t.Cleanup(func() { mobileRecordingPauses, mobileRecordingTranscriber = oldPauses, oldTranscriber })
	mobileRecordingPauses = func(*App) ([]observationPause, error) { return pauses, nil }
	mobileRecordingTranscriber = func(*App, string) ([]mobileRecordingSegment, error) { return nil, nil }
	(&App{}).transcribeMobileRecordings(context.Background(), store)
	path := filepath.Join(store.dir, recordingID, "part-00.m4a")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the part's audio is gone; only the paused seconds should be: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	samples := decodeTestPCM(t, ffmpeg, path)
	seconds := float64(len(samples)) / 16000
	if seconds < 37 || seconds > 39 {
		t.Fatalf("the cut audio is %.2f s, want about 38", seconds)
	}
	if peak := peakOf(samples); peak > 200 {
		t.Fatalf("audio from the pause survived the cut (peak %.0f)", peak)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(store.dir, recordingID, "*.cut.*")); len(leftovers) != 0 {
		t.Fatalf("left behind %v", leftovers)
	}
	rec, _ := store.get(recordingID, "dev-a")
	if len(rec.Received) != 1 || !rec.Received[0].PausedCut || rec.Received[0].Paused {
		t.Fatalf("part %+v", rec.Received)
	}
	// A second pass leaves the cut file alone.
	if n := store.dropPausedAudio(recordingID, pauses, false); n != 0 {
		t.Fatalf("changed %d parts again", n)
	}
}

// When the cut fails, the file that still holds the paused minutes is deleted.
func TestAFailedCutDeletesTheAudio(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	started := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	req := sealedRecordingPart(t, uploads, "dev-a", recordingID, 0, true, []byte("not audio"), started)
	req.DurationSeconds = 60
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}
	oldF, oldTrim := mobileRecordingFFmpegPath, mobileRecordingTrim
	t.Cleanup(func() { mobileRecordingFFmpegPath, mobileRecordingTrim = oldF, oldTrim })
	mobileRecordingFFmpegPath = func() string { return "ffmpeg" }
	mobileRecordingTrim = func(string, string, string, []mobileRecordingAudioSpan) ([]byte, error) {
		return []byte("invalid data"), os.ErrInvalid
	}
	pauses := []observationPause{{Start: started.Add(20 * time.Second).UnixMilli(), End: started.Add(40 * time.Second).UnixMilli()}}
	if n := store.dropPausedAudio(recordingID, pauses, false); n != 1 {
		t.Fatalf("changed %d parts, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(store.dir, recordingID, "part-00.m4a")); err == nil {
		t.Fatal("audio that still holds the pause was kept")
	}
	rec, _ := store.get(recordingID, "dev-a")
	if len(rec.Received) != 1 || !rec.Received[0].Paused || rec.Received[0].PausedCut {
		t.Fatalf("part %+v", rec.Received)
	}
}

// A tap on the watch during a pause is something from the pause: it goes with the paused audio.
// Marks made outside the pause stay.
func TestMarksMadeDuringAPauseGoWithTheAudio(t *testing.T) {
	store := newMobileRecordingStore(t.TempDir())
	uploads := &mapUploads{bodies: map[string][]byte{}}
	recordingID := uuid.NewString()
	started := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	req := sealedRecordingPart(t, uploads, "dev-a", recordingID, 0, true, []byte("not audio"), started)
	req.DurationSeconds = 60
	req.Marks = []float64{5, 25, 39.5, 50}
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}
	pauses := []observationPause{{Start: started.Add(20 * time.Second).UnixMilli(), End: started.Add(40 * time.Second).UnixMilli()}}
	if n := store.dropPausedAudio(recordingID, pauses, false); n != 1 {
		t.Fatalf("changed %d parts, want 1", n)
	}
	rec, _ := store.get(recordingID, "dev-a")
	if got := rec.Received[0].Marks; len(got) != 2 || got[0] != 5 || got[1] != 50 {
		t.Fatalf("marks %v, want the two made outside the pause", got)
	}

	// An hour recorded wholly inside a pause keeps none.
	whole := uuid.NewString()
	req = sealedRecordingPart(t, uploads, "dev-a", whole, 0, true, []byte("not audio"), started)
	req.DurationSeconds = 60
	req.Marks = []float64{5, 50}
	if _, _, err := receiveMobileRecordingPart(context.Background(), store, uploads, recordingTestHub, "dev-a", req); err != nil {
		t.Fatal(err)
	}
	all := []observationPause{{Start: started.Add(-time.Minute).UnixMilli(), End: started.Add(2 * time.Minute).UnixMilli()}}
	if n := store.dropPausedAudio(whole, all, true); n != 1 {
		t.Fatalf("changed %d parts, want 1", n)
	}
	rec, _ = store.get(whole, "dev-a")
	if got := rec.Received[0].Marks; len(got) != 0 {
		t.Fatalf("marks %v survived an hour that was all pause", got)
	}
}
