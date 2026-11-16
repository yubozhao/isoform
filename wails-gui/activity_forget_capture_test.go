package main

import (
	"os"
	"path/filepath"
	"testing"
)

// captureFixture gives a seeded moment's session a screenshot, a screen
// video segment and call audio, on disk and in its rows.
func captureFixture(t *testing.T, store *ActivityStore, sessionID, name string) (snapshot, segment, audio string) {
	t.Helper()
	snapRel := filepath.Join("2026-11-15", name+".jpg")
	segRel := filepath.Join("2026-11-15", name+".mp4")
	audioRel := filepath.Join("2026-11-15", name+".m4a")
	for dir, rel := range map[string]string{store.SnapshotsDir(): snapRel, store.SegmentsDir(): segRel, store.AudioDir(): audioRel} {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("capture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshots(tx, []ActivitySnapshotRow{{SessionID: sessionID, Timestamp: 1, FilePath: snapRel, StorageTier: "hot", AXText: "private screen words"}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO activity_segments (session_id, start_time, end_time, file_path, width, height, fps, codec, size_bytes, storage_tier)
		VALUES (?, 1, 2, ?, 1, 1, 1, 'hevc', 7, 'hot')`, sessionID, segRel); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachMeetingRecording(sessionID, audioRel, 7); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(store.SnapshotsDir(), snapRel), filepath.Join(store.SegmentsDir(), segRel), filepath.Join(store.AudioDir(), audioRel)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestForgetAMomentErasesItsCaptureAtOnce: Forget on a moment removes its
// screenshots, screen video and call audio files and its session's rows
// (events, OCR text) right away, not at retention, and names that capture
// before it goes. Another moment's capture stays.
func TestForgetAMomentErasesItsCaptureAtOnce(t *testing.T) {
	app := newThreadTestApp(t)
	store := app.activity.store
	day, _ := threadTestDay(t)
	call := seedThreadMoment(t, app, threadFixtureMoment{id: "call", title: "Call with Lena", app: "Zoom", start: at(day, 9, 0), end: at(day, 9, 30)})
	other := seedThreadMoment(t, app, threadFixtureMoment{id: "other", title: "Pricing page copy", app: "Safari", start: at(day, 10, 0), end: at(day, 10, 30)})
	snap, seg, audio := captureFixture(t, store, "session-call", "call")
	keptSnap, keptSeg, keptAudio := captureFixture(t, store, "session-other", "other")

	preview, err := app.GetActivityForgetPreview("screen", call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Goes) == 0 || preview.Goes[0].Kind != "screen" || preview.Goes[0].StartMs != at(day, 9, 0).UnixMilli() {
		t.Fatalf("preview does not name the capture: %+v", preview.Goes)
	}
	if !exists(snap) || !exists(seg) || !exists(audio) {
		t.Fatal("the preview removed something")
	}

	if err := app.ForgetActivityFeedRow("screen", call.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{snap, seg, audio} {
		if exists(path) {
			t.Fatalf("%s outlived Forget", path)
		}
	}
	var rows int
	if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM activity_sessions WHERE id='session-call')
		+ (SELECT COUNT(*) FROM activity_events WHERE session_id='session-call')
		+ (SELECT COUNT(*) FROM activity_snapshots WHERE session_id='session-call')
		+ (SELECT COUNT(*) FROM activity_segments WHERE session_id='session-call')`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("capture rows left = %d, %v", rows, err)
	}
	if got, _ := store.GetActivityByID(call.ID); got != nil {
		t.Fatal("the moment outlived Forget")
	}
	for _, path := range []string{keptSnap, keptSeg, keptAudio} {
		if !exists(path) {
			t.Fatalf("forgetting one moment removed another's %s", path)
		}
	}
	if got, _ := store.GetActivityByID(other.ID); got == nil {
		t.Fatal("forgetting one moment removed another")
	}
}

// TestForgetNamesMomentsThatShareTheCapture: when another moment's run holds
// the same capture session, Forget says that moment loses its screen too.
func TestForgetNamesMomentsThatShareTheCapture(t *testing.T) {
	app := newThreadTestApp(t)
	store := app.activity.store
	day, _ := threadTestDay(t)
	a := seedThreadMoment(t, app, threadFixtureMoment{id: "a", title: "Draft the plan", app: "Notes", start: at(day, 9, 0), end: at(day, 9, 30)})
	seedThreadMoment(t, app, threadFixtureMoment{id: "b", title: "Review the plan", app: "Notes", start: at(day, 9, 30), end: at(day, 10, 0)})
	if _, err := store.db.Exec(`UPDATE activity_timeline_groups SET session_ids='["session-a"]' WHERE id='run-b'`); err != nil {
		t.Fatal(err)
	}
	preview, err := app.GetActivityForgetPreview("screen", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range preview.Goes {
		found = found || item.Kind == "moment" && item.Title == "Review the plan"
	}
	if !found {
		t.Fatalf("the shared moment is not named: %+v", preview.Goes)
	}
}

func TestCaptureFileUnderRefusesPathsOutsideItsFolder(t *testing.T) {
	for _, rel := range []string{"", "/etc/passwd", "../outside.jpg", "a/../../outside.jpg"} {
		if _, ok := captureFileUnder("/tmp/snapshots", rel); ok {
			t.Fatalf("%q accepted", rel)
		}
	}
	if path, ok := captureFileUnder("/tmp/snapshots", "2026-11-15/a.jpg"); !ok || path != "/tmp/snapshots/2026-11-15/a.jpg" {
		t.Fatalf("a plain path was refused: %q", path)
	}
}
