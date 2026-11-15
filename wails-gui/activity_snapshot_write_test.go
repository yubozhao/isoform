package main

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/ocr"
)

// failingOCREngine fails every recognition, like the OCR helper timing out.
type failingOCREngine struct{ fakeOCREngine }

func (failingOCREngine) RecognizeJPEGWithBoxes(_ []byte, _ []string, _ ocr.Priority) ([]ocr.TextObservation, error) {
	return nil, errors.New("ocr helper timed out after 20s")
}

// giantEmailOCREngine sees an email address in a text line covering half the
// frame, so its mask would be over the safety cap.
type giantEmailOCREngine struct{ fakeOCREngine }

func (giantEmailOCREngine) RecognizeJPEGWithBoxes(_ []byte, _ []string, _ ocr.Priority) ([]ocr.TextObservation, error) {
	return []ocr.TextObservation{{Text: "jane.doe@example.com", X: 0, Y: 0.25, Width: 1, Height: 0.5, Confidence: 1}}, nil
}

func solidTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A frame whose PII mask would be over the size cap is dropped, not written
// with that PII unmasked.
func TestRunAsyncPIIMasking_OversizedMaskDropsFrame(t *testing.T) {
	am := &activityMonitorState{piiSem: make(chan struct{}, 1), ocrEngine: giantEmailOCREngine{fakeOCREngine{supported: true}}}
	path := filepath.Join(t.TempDir(), "2026-10-15", "frame.jpg")
	write := newSnapshotWrite("2026-10-15/frame.jpg")

	if got := am.runAsyncPIIMasking(path, solidTestJPEG(t), 80, time.Now(), 0, 0, nil, write); got != piiMaskLaunched {
		t.Fatalf("runAsyncPIIMasking() = %v, want piiMaskLaunched", got)
	}
	<-write.done
	if finished, ok, _ := write.result(); !finished || ok {
		t.Fatalf("write finished=%v ok=%v, want a dropped frame", finished, ok)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("frame with an oversized mask must not be on disk, stat err = %v", err)
	}
}

func newSnapshotWriteTestStore(t *testing.T) *ActivityStore {
	t.Helper()
	store, err := NewActivityStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewActivityStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func saveSnapshotWriteTestSession(t *testing.T, store *ActivityStore, sessionID string, paths ...string) {
	t.Helper()
	rows := make([]ActivitySnapshotRow, 0, len(paths))
	for i, p := range paths {
		rows = append(rows, ActivitySnapshotRow{
			SessionID:   sessionID,
			Timestamp:   int64(1000 + i),
			FilePath:    p,
			SizeBytes:   100,
			Trigger:     "click",
			StorageTier: "hot",
		})
	}
	err := store.SaveSessionWithSnapshots(ActivitySessionRow{
		ID:             sessionID,
		StartTime:      1000,
		EndTime:        2000,
		TriggerKind:    "idle",
		AppNames:       "[]",
		SnapshotCount:  len(rows),
		TotalSizeBytes: int64(100 * len(rows)),
		TriageStatus:   "pending",
		CreatedAt:      2000,
	}, rows)
	if err != nil {
		t.Fatalf("SaveSessionWithSnapshots() error = %v", err)
	}
}

func snapshotWriteTestCounts(t *testing.T, store *ActivityStore, sessionID string) (rows, count int, size int64) {
	t.Helper()
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM activity_snapshots WHERE session_id = ?`, sessionID).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if err := store.db.QueryRow(`SELECT snapshot_count, total_size_bytes FROM activity_sessions WHERE id = ?`, sessionID).Scan(&count, &size); err != nil {
		t.Fatalf("read session counts: %v", err)
	}
	return rows, count, size
}

// The bug behind the empty Activity screen: the OCR helper timed out on every
// screenshot, the PII mask dropped each frame without writing it, but the
// entry had already joined the session and became a row pointing at nothing.
func TestRunAsyncPIIMasking_OCRFailureRecordsDroppedWrite(t *testing.T) {
	am := &activityMonitorState{piiSem: make(chan struct{}, 1), ocrEngine: failingOCREngine{fakeOCREngine{supported: true}}}
	path := filepath.Join(t.TempDir(), "2026-10-15", "frame.jpg")
	write := newSnapshotWrite("2026-10-15/frame.jpg")

	if got := am.runAsyncPIIMasking(path, []byte{1, 2, 3}, 80, time.Now(), 0, 0, nil, write); got != piiMaskLaunched {
		t.Fatalf("runAsyncPIIMasking() = %v, want piiMaskLaunched", got)
	}
	kept := awaitSnapshotWrites([]snapshotEntry{{relPath: write.relPath, write: write}}, 2*time.Second)

	if len(kept) != 0 {
		t.Fatalf("awaitSnapshotWrites kept %d entries, want the dropped frame left out", len(kept))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dropped frame must not be on disk, stat err = %v", err)
	}
}

func TestRunAsyncPIIMasking_CleanFrameKeepsEntryWithFile(t *testing.T) {
	am := &activityMonitorState{piiSem: make(chan struct{}, 1), ocrEngine: fakeOCREngine{supported: true}}
	path := filepath.Join(t.TempDir(), "2026-10-15", "frame.jpg")
	write := newSnapshotWrite("2026-10-15/frame.jpg")

	if got := am.runAsyncPIIMasking(path, []byte{1, 2, 3}, 80, time.Now(), 0, 0, nil, write); got != piiMaskLaunched {
		t.Fatalf("runAsyncPIIMasking() = %v, want piiMaskLaunched", got)
	}
	kept := awaitSnapshotWrites([]snapshotEntry{{relPath: write.relPath, sizeBytes: 999, write: write}}, 2*time.Second)

	if len(kept) != 1 {
		t.Fatalf("awaitSnapshotWrites kept %d entries, want 1", len(kept))
	}
	if kept[0].sizeBytes != 3 {
		t.Fatalf("sizeBytes = %d, want the written size 3", kept[0].sizeBytes)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("clean frame must be on disk: %v", err)
	}
}

func TestAwaitSnapshotWrites_KeepsSyncAndInFlightEntries(t *testing.T) {
	inFlight := newSnapshotWrite("b.jpg")
	failed := newSnapshotWrite("c.jpg")
	failed.finish(0, false)

	start := time.Now()
	kept := awaitSnapshotWrites([]snapshotEntry{
		{relPath: "a.jpg"},
		{relPath: "b.jpg", write: inFlight},
		{relPath: "c.jpg", write: failed},
	}, 50*time.Millisecond)

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("awaitSnapshotWrites took %s, want it bounded by the budget", elapsed)
	}
	if len(kept) != 2 || kept[0].relPath != "a.jpg" || kept[1].relPath != "b.jpg" {
		t.Fatalf("kept = %+v, want a.jpg and the in-flight b.jpg", kept)
	}
}

// snapshotWriteTestTier returns the storage_tier and AX text of the row for relPath.
func snapshotWriteTestTier(t *testing.T, store *ActivityStore, relPath string) (tier, axText string, size int64) {
	t.Helper()
	if err := store.db.QueryRow(`SELECT storage_tier, ax_text, size_bytes FROM activity_snapshots WHERE file_path = ?`, relPath).
		Scan(&tier, &axText, &size); err != nil {
		t.Fatalf("read row %s: %v", relPath, err)
	}
	return tier, axText, size
}

// commitSnapshotWritesForTest does what extractSession does around the
// session commit: count unfinished writes as pending, then mark them saved.
func commitSnapshotWritesForTest(am *activityMonitorState, sessionID string, entries []snapshotEntry) {
	am.trackSnapshotWrites(sessionID, entries)
	am.markSnapshotWritesPersisted(entries)
}

// A write still in flight at the session commit becomes a row; if it fails
// afterwards, the row is marked image-missing (its text stays) and the
// session counts cover only rows with a file.
func TestSnapshotWrite_FailureAfterCommitMarksRowMissing(t *testing.T) {
	store := newSnapshotWriteTestStore(t)
	am := &activityMonitorState{store: store}
	saveSnapshotWriteTestSession(t, store, "s1", "2026-10-15/a.jpg", "2026-10-15/b.jpg")
	if _, err := store.db.Exec(`UPDATE activity_snapshots SET ax_text = 'remembered text'`); err != nil {
		t.Fatal(err)
	}
	write := newSnapshotWrite("2026-10-15/b.jpg")

	commitSnapshotWritesForTest(am, "s1", []snapshotEntry{
		{relPath: "2026-10-15/a.jpg", sizeBytes: 100},
		{relPath: write.relPath, sizeBytes: 100, write: write},
	})
	if rows, _, _ := snapshotWriteTestCounts(t, store, "s1"); rows != 2 {
		t.Fatalf("rows before the write finished = %d, want 2", rows)
	}
	if !am.pendingSnapshotSessions.has("s1") {
		t.Fatal("a committed row whose file is still being written must keep the session pending for OCR")
	}

	am.finishSnapshotWrite(write, 0, false)

	rows, count, size := snapshotWriteTestCounts(t, store, "s1")
	if rows != 2 || count != 1 || size != 100 {
		t.Fatalf("after failed write: rows=%d snapshot_count=%d size=%d, want 2/1/100", rows, count, size)
	}
	if tier, axText, rowSize := snapshotWriteTestTier(t, store, write.relPath); tier != snapshotTierMissing || axText != "remembered text" || rowSize != 0 {
		t.Fatalf("failed row: tier=%q ax_text=%q size=%d, want missing, text kept, 0", tier, axText, rowSize)
	}
	if am.pendingSnapshotSessions.has("s1") {
		t.Fatal("the session must stop being pending once the write finished")
	}
}

// A write that fails between awaitSnapshotWrites and the commit has its row
// marked image-missing when the commit marks it persisted.
func TestSnapshotWrite_FailureBeforeCommitMarksRowOnPersist(t *testing.T) {
	store := newSnapshotWriteTestStore(t)
	am := &activityMonitorState{store: store}
	saveSnapshotWriteTestSession(t, store, "s1", "2026-10-15/a.jpg")
	write := newSnapshotWrite("2026-10-15/a.jpg")

	am.finishSnapshotWrite(write, 0, false)
	commitSnapshotWritesForTest(am, "s1", []snapshotEntry{{relPath: write.relPath, sizeBytes: 100, write: write}})

	if rows, count, size := snapshotWriteTestCounts(t, store, "s1"); rows != 1 || count != 0 || size != 0 {
		t.Fatalf("rows=%d snapshot_count=%d size=%d, want the row kept and counted as image-missing", rows, count, size)
	}
	if tier, _, _ := snapshotWriteTestTier(t, store, write.relPath); tier != snapshotTierMissing {
		t.Fatalf("tier = %q, want missing", tier)
	}
	if am.pendingSnapshotSessions.has("s1") {
		t.Fatal("a finished write must not leave the session pending")
	}
}

// The masked JPEG is re-encoded, so a write that lands after the commit
// corrects the row's size and the session total.
func TestSnapshotWrite_SuccessAfterCommitFixesSize(t *testing.T) {
	store := newSnapshotWriteTestStore(t)
	am := &activityMonitorState{store: store}
	saveSnapshotWriteTestSession(t, store, "s1", "2026-10-15/a.jpg", "2026-10-15/b.jpg")
	write := newSnapshotWrite("2026-10-15/b.jpg")

	commitSnapshotWritesForTest(am, "s1", []snapshotEntry{{relPath: write.relPath, sizeBytes: 100, write: write}})
	am.finishSnapshotWrite(write, 140, true)

	rows, count, size := snapshotWriteTestCounts(t, store, "s1")
	if rows != 2 || count != 2 || size != 240 {
		t.Fatalf("rows=%d snapshot_count=%d size=%d, want 2/2/240", rows, count, size)
	}
	var rowSize int64
	if err := store.db.QueryRow(`SELECT size_bytes FROM activity_snapshots WHERE file_path = ?`, write.relPath).Scan(&rowSize); err != nil {
		t.Fatal(err)
	}
	if rowSize != 140 {
		t.Fatalf("row size = %d, want the written size 140", rowSize)
	}
}

func TestSnapshotWrite_SuccessKeepsRow(t *testing.T) {
	store := newSnapshotWriteTestStore(t)
	am := &activityMonitorState{store: store}
	saveSnapshotWriteTestSession(t, store, "s1", "2026-10-15/a.jpg")
	write := newSnapshotWrite("2026-10-15/a.jpg")

	commitSnapshotWritesForTest(am, "s1", []snapshotEntry{{relPath: write.relPath, sizeBytes: 100, write: write}})
	am.finishSnapshotWrite(write, 100, true)

	if rows, count, size := snapshotWriteTestCounts(t, store, "s1"); rows != 1 || count != 1 || size != 100 {
		t.Fatalf("rows=%d snapshot_count=%d size=%d, want the row kept unchanged", rows, count, size)
	}
}

// Shutdown flushes must commit inside the shutdown watchdog, and the
// pre-meeting flush runs on the event drain, so neither waits for writes nor
// for the capture or PII slot of its final frame.
func TestSnapshotWriteWait_NoWaitOnShutdownAndPreMeeting(t *testing.T) {
	for _, trigger := range []string{"shutdown_in_meeting", "shutdown_meeting_candidate", "pre_meeting_flush"} {
		if got := snapshotWriteWait(trigger); got != 0 {
			t.Errorf("snapshotWriteWait(%q) = %s, want 0", trigger, got)
		}
		if got := finalCaptureWait(trigger); got != 0 {
			t.Errorf("finalCaptureWait(%q) = %s, want 0", trigger, got)
		}
	}
	for _, trigger := range []string{"idle_transition", "periodic_flush", "meeting_ended"} {
		if got := snapshotWriteWait(trigger); got != pendingSnapshotWriteWait {
			t.Errorf("snapshotWriteWait(%q) = %s, want %s", trigger, got, pendingSnapshotWriteWait)
		}
		if got := finalCaptureWait(trigger); got != sessionEndCaptureWait {
			t.Errorf("finalCaptureWait(%q) = %s, want %s", trigger, got, sessionEndCaptureWait)
		}
	}

	inFlight := newSnapshotWrite("a.jpg")
	start := time.Now()
	kept := awaitSnapshotWrites([]snapshotEntry{{relPath: "a.jpg", write: inFlight}}, 0)
	if elapsed := time.Since(start); elapsed > pendingSnapshotWriteWait/10 {
		t.Fatalf("zero budget waited %s", elapsed)
	}
	if len(kept) != 1 {
		t.Fatalf("zero budget kept %d entries, want the in-flight one", len(kept))
	}
}

// A frame whose referent is still masking, or whose referent the mask
// dropped, must keep its full parsed payload: the referent's row may never
// exist, and a dedup stub would resolve to nothing.
func TestCanElideParsedPayload_RequiresReferentWritten(t *testing.T) {
	const bundle, hash = "com.example.App", "hash-1"

	am := &activityMonitorState{enabled: true}
	failed := newSnapshotWrite("a.jpg")
	am.appendCapturedSnapshot(snapshotEntry{relPath: "a.jpg", timestamp: 1, write: failed}, bundle, hash)
	if am.canElideParsedPayload(bundle, hash) {
		t.Fatal("referent still masking: identical frame must not be elided")
	}
	failed.finish(0, false)
	if am.canElideParsedPayload(bundle, hash) {
		t.Fatal("referent dropped by the mask: identical frame must not be elided")
	}

	written := newSnapshotWrite("b.jpg")
	am.appendCapturedSnapshot(snapshotEntry{relPath: "b.jpg", timestamp: 2, write: written}, bundle, hash)
	written.finish(10, true)
	if !am.canElideParsedPayload(bundle, hash) {
		t.Fatal("referent written: identical frame should be elided")
	}
	if am.canElideParsedPayload(bundle, "other-hash") {
		t.Fatal("different content must never be elided")
	}

	sync := &activityMonitorState{enabled: true}
	sync.appendCapturedSnapshot(snapshotEntry{relPath: "c.jpg", timestamp: 3}, bundle, hash)
	if !sync.canElideParsedPayload(bundle, hash) {
		t.Fatal("referent written synchronously: identical frame should be elided")
	}
}

// The startup reconcile keeps every row and its text: a row whose screenshot
// is gone is marked image-missing, a marked row whose file is back is
// restored, and files no row refers to are removed.
func TestReconcileSnapshotFiles(t *testing.T) {
	store := newSnapshotWriteTestStore(t)
	saveSnapshotWriteTestSession(t, store, "s1", "2026-10-15/kept.jpg", "2026-10-15/gone.jpg", "2026-10-16/gone.jpg", "2026-10-15/back.jpg")
	exec := func(q string) {
		t.Helper()
		if _, err := store.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE activity_snapshots SET ax_text = 'remembered text', parsed_json = '{"body":"x"}'`)
	// back.jpg was marked earlier; its file has since reappeared.
	exec(`UPDATE activity_snapshots SET storage_tier = 'missing', size_bytes = 0 WHERE file_path = '2026-10-15/back.jpg'`)
	exec(`UPDATE activity_sessions SET snapshot_count = 3, total_size_bytes = 300 WHERE id = 's1'`)

	dir := filepath.Join(store.SnapshotsDir(), "2026-10-15")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, size int) {
		t.Helper()
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "kept.jpg"), 100)
	write(filepath.Join(dir, "back.jpg"), 70)
	write(filepath.Join(dir, "gone.jpg.tmp"), 1) // half-written by a quit
	write(filepath.Join(dir, "unsaved.jpg"), 1)  // written, never committed
	write(filepath.Join(dir, "notes.txt"), 1)    // not a snapshot file
	write(filepath.Join(store.SnapshotsDir(), ".ocr_marker"), 1)
	inFlight := filepath.Join(dir, "in-flight.jpg") // this run's own write
	write(inFlight, 1)

	cutoff := time.Now().Add(time.Minute).UnixMilli()
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(inFlight, future, future); err != nil {
		t.Fatal(err)
	}

	got, err := store.ReconcileSnapshotFiles(cutoff)
	if err != nil {
		t.Fatalf("ReconcileSnapshotFiles() error = %v", err)
	}
	if got.Marked != 2 || got.Restored != 1 || got.FilesRemoved != 2 {
		t.Fatalf("reconcile = %+v, want 2 marked, 1 restored, 2 files removed", got)
	}

	rows, count, size := snapshotWriteTestCounts(t, store, "s1")
	if rows != 4 || count != 2 || size != 170 {
		t.Fatalf("s1 rows=%d snapshot_count=%d size=%d, want 4 rows kept, 2 with files, 100+70 bytes", rows, count, size)
	}
	if tier, axText, rowSize := snapshotWriteTestTier(t, store, "2026-10-16/gone.jpg"); tier != snapshotTierMissing || axText != "remembered text" || rowSize != 0 {
		t.Fatalf("marked row: tier=%q ax_text=%q size=%d, want missing, text kept, 0", tier, axText, rowSize)
	}
	if tier, _, rowSize := snapshotWriteTestTier(t, store, "2026-10-15/back.jpg"); tier != "hot" || rowSize != 70 {
		t.Fatalf("restored row: tier=%q size=%d, want hot, 70", tier, rowSize)
	}
	for _, name := range []string{"gone.jpg.tmp", "unsaved.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s not removed, stat err = %v", name, err)
		}
	}
	for _, path := range []string{filepath.Join(dir, "kept.jpg"), filepath.Join(dir, "notes.txt"), inFlight, filepath.Join(store.SnapshotsDir(), ".ocr_marker")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s must stay: %v", path, err)
		}
	}

	if again, err := store.ReconcileSnapshotFiles(cutoff); err != nil || again != (SnapshotFileReconcile{}) {
		t.Fatalf("second reconcile = %+v, %v; want nothing to do", again, err)
	}
	snap, err := store.FindClosestSnapshot("s1", 1002)
	if err != nil || !snap.HasImage() {
		t.Fatalf("FindClosestSnapshot = %+v, %v; want a row with an image", snap, err)
	}
}

// Rows from the running app are left to their own write.
func TestReconcileSnapshotFiles_LeavesThisRunAlone(t *testing.T) {
	store := newSnapshotWriteTestStore(t)
	saveSnapshotWriteTestSession(t, store, "s1", "2026-10-15/now.jpg")
	if _, err := store.db.Exec(`UPDATE activity_snapshots SET timestamp = 9000`); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReconcileSnapshotFiles(5000)
	if err != nil || got.Marked != 0 {
		t.Fatalf("reconcile = %+v, %v; want the row from after the cutoff untouched", got, err)
	}
}

// The rotator moves hot rows to warm and cold; it must not overwrite a mark.
func TestUpdateSnapshotTierKeepsMissingMark(t *testing.T) {
	store := newSnapshotWriteTestStore(t)
	saveSnapshotWriteTestSession(t, store, "s1", "2026-10-15/a.jpg")
	if _, err := store.MarkSnapshotMissingByFilePath("2026-10-15/a.jpg"); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := store.db.QueryRow(`SELECT id FROM activity_snapshots`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSnapshotTier(id, "warm"); err != nil {
		t.Fatal(err)
	}
	if tier, _, _ := snapshotWriteTestTier(t, store, "2026-10-15/a.jpg"); tier != snapshotTierMissing {
		t.Fatalf("tier = %q, want missing kept", tier)
	}
}

func TestSnapshotsWithImagesSkipsMissing(t *testing.T) {
	got := snapshotsWithImages([]ActivitySnapshotRow{
		{ID: 1, FilePath: "a.jpg", StorageTier: "hot"},
		{ID: 2, FilePath: "b.jpg", StorageTier: snapshotTierMissing},
		{ID: 3, FilePath: "c.jpg", StorageTier: "warm"},
		{ID: 4, FilePath: "", StorageTier: "hot"},
	})
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("snapshotsWithImages = %+v, want rows 1 and 3", got)
	}
}

// Screenshot readers for the vision model skip rows without an image.
func TestRepresentativeSnapshotsSkipMissingImages(t *testing.T) {
	rows := []ActivitySnapshotRow{
		{ID: 1, FilePath: "a.jpg", StorageTier: "hot", FocusRectW: 1, FocusRectH: 1, Trigger: "click"},
		{ID: 2, FilePath: "b.jpg", StorageTier: snapshotTierMissing, FocusRectW: 1, FocusRectH: 1, Trigger: wechatInputTrigger},
	}
	o := &Observation{SnapshotIDs: []int64{1, 2}}
	byID := map[int64]ActivitySnapshotRow{1: rows[0], 2: rows[1]}
	if got, ok := representativeVisionSnapshot(o, byID); !ok || got.ID != 1 {
		t.Fatalf("representativeVisionSnapshot = %d, %v; want row 1", got.ID, ok)
	}
	if got, ok := representativeWeChatSnapshot(o, rows); !ok || got.ID != 1 {
		t.Fatalf("representativeWeChatSnapshot = %d, %v; want row 1, not the marked keyboard frame", got.ID, ok)
	}
}

// The count of writes pending for a session goes up before the commit and
// down when each write finishes, in whatever order the two happen.
func TestSnapshotWrite_PendingCountOrdering(t *testing.T) {
	am := &activityMonitorState{}

	// Finishes between the count and the commit's mark.
	w1 := newSnapshotWrite("a.jpg")
	am.trackSnapshotWrites("s1", []snapshotEntry{{relPath: "a.jpg", write: w1}})
	if !am.pendingSnapshotSessions.has("s1") {
		t.Fatal("an unfinished write must be pending from before the commit")
	}
	am.finishSnapshotWrite(w1, 1, true)
	am.markSnapshotWritesPersisted([]snapshotEntry{{relPath: "a.jpg", sizeBytes: 1, write: w1}})
	if am.pendingSnapshotSessions.has("s1") {
		t.Fatal("pending count stuck after a write finished before the mark")
	}

	// Finished before the count: never pending.
	w2 := newSnapshotWrite("b.jpg")
	am.finishSnapshotWrite(w2, 1, true)
	am.trackSnapshotWrites("s2", []snapshotEntry{{relPath: "b.jpg", write: w2}})
	if am.pendingSnapshotSessions.has("s2") {
		t.Fatal("a finished write must not be counted")
	}

	// Commit failed: the count is undone and a late finish does not go negative.
	w3 := newSnapshotWrite("c.jpg")
	entries := []snapshotEntry{{relPath: "c.jpg", write: w3}}
	am.trackSnapshotWrites("s3", entries)
	am.untrackSnapshotWrites(entries)
	am.finishSnapshotWrite(w3, 1, true)
	if am.pendingSnapshotSessions.has("s3") {
		t.Fatal("pending count left after a failed commit")
	}
	w4 := newSnapshotWrite("d.jpg")
	am.trackSnapshotWrites("s3", []snapshotEntry{{relPath: "d.jpg", write: w4}})
	if !am.pendingSnapshotSessions.has("s3") {
		t.Fatal("a new write for the session must count again")
	}
}

func TestWriteSnapshotFileLeavesNoTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.jpg")
	if err := writeSnapshotFile(path, []byte{1, 2}); err != nil {
		t.Fatalf("writeSnapshotFile() error = %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) != 2 {
		t.Fatalf("ReadFile = %v, %v", data, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind, stat err = %v", err)
	}
}

// coldTimeoutOCREngine reports a helper that overran its cold-start budget.
type coldTimeoutOCREngine struct {
	fakeOCREngine
	calls *int
}

func (e coldTimeoutOCREngine) RecognizeJPEGWithBoxes(_ []byte, _ []string, _ ocr.Priority) ([]ocr.TextObservation, error) {
	*e.calls++
	return nil, &ocr.HelperTimeoutError{Timeout: 3 * time.Minute, Cold: true}
}

// A wedged helper must not hold every queued unmasked frame in memory for a
// full cold budget each: after one cold timeout the rest of the batch goes.
func TestDrainPendingPIISnapshots_DropsBatchAfterColdTimeout(t *testing.T) {
	calls := 0
	am := &activityMonitorState{
		enabled:   true,
		piiSem:    make(chan struct{}, 1),
		ocrEngine: coldTimeoutOCREngine{fakeOCREngine: fakeOCREngine{supported: true}, calls: &calls},
	}
	app := &App{activity: am}
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		am.queuePendingPIISnapshot(pendingPIISnapshot{
			path:     filepath.Join(dir, "f.jpg"),
			jpegData: []byte{byte(i + 1)},
			snapshot: snapshotEntry{relPath: "f.jpg", timestamp: int64(i + 1)},
		})
	}
	am.pendingPIIDraining = true

	app.drainPendingPIISnapshots(am)

	if calls != 1 {
		t.Fatalf("OCR calls = %d, want 1 (the rest of the batch dropped)", calls)
	}
	if len(am.allSnapshots) != 0 || len(am.pendingPIISnapshots) != 0 || am.pendingPIIDraining {
		t.Fatalf("after drain: snapshots=%d queued=%d draining=%v, want 0/0/false",
			len(am.allSnapshots), len(am.pendingPIISnapshots), am.pendingPIIDraining)
	}
}

// The session keeps the events up to its last captured frame even when the
// PII mask drops that frame: the span comes from every captured frame, not
// only the ones that became rows.
func TestExtractSession_FailedLastFrameKeepsTrailingEvents(t *testing.T) {
	store := newSnapshotWriteTestStore(t)
	am := &activityMonitorState{
		enabled: true,
		store:   store,
		buf:     activitybuf.NewRingBuffer(16, time.Hour),
	}
	app := &App{activity: am}

	base := time.Now().Add(-time.Minute).UnixMilli()
	for i := int64(0); i < 3; i++ {
		am.buf.Push(activitymon.ActivityEvent{
			Timestamp: base + i*1000,
			Kind:      activitymon.EventClick,
			AppName:   "Cursor",
		})
	}
	dropped := newSnapshotWrite("2026-10-15/last.jpg")
	dropped.finish(0, false)
	am.allSnapshots = []snapshotEntry{
		{relPath: "2026-10-15/first.jpg", timestamp: base, sizeBytes: 100},
		{relPath: dropped.relPath, timestamp: base + 2000, sizeBytes: 100, write: dropped},
	}

	app.extractSession("periodic_flush")

	var sessionID string
	var eventCount, snapshotCount int
	if err := store.db.QueryRow(`SELECT id, event_count, snapshot_count FROM activity_sessions`).Scan(&sessionID, &eventCount, &snapshotCount); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if snapshotCount != 1 {
		t.Fatalf("snapshot_count = %d, want 1 (the dropped frame left out)", snapshotCount)
	}
	if eventCount != 3 {
		t.Fatalf("event_count = %d, want 3 (events up to the dropped last frame kept)", eventCount)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM activity_snapshots WHERE session_id = ?`, sessionID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("snapshot rows = %d, want 1", rows)
	}
}
