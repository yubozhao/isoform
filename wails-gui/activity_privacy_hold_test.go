package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/ocr"
	"wails-gui/models"
	"yansu-agent/agent"
)

type holdOCREngine struct{ fakeOCREngine }

func (holdOCREngine) RecognizeJPEGWithBoxes(_ []byte, _ []string, priority ocr.Priority) ([]ocr.TextObservation, error) {
	if priority == ocr.PriorityLow {
		return []ocr.TextObservation{{Text: "Remaining visible text"}}, nil
	}
	return []ocr.TextObservation{{Text: "me@example.com", X: .1, Y: .1, Width: .2, Height: .08}, {Text: "hidden passage", X: .5, Y: .5, Width: .3, Height: .08}}, nil
}

type holdDetector struct {
	calls  atomic.Int32
	failed bool
}

func (d *holdDetector) Detect([]ocr.TextObservation) []pii.DetectionResult {
	d.calls.Add(1)
	if d.failed {
		return []pii.DetectionResult{{Width: 1, Height: 1}}
	}
	return []pii.DetectionResult{{X: .5, Y: .5, Width: .3, Height: .08}}
}
func (*holdDetector) IsReady() bool { return true }
func (*holdDetector) Destroy()      {}

func waitForPrivacyHold(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("privacy worker timed out")
		}
		time.Sleep(time.Millisecond * 5)
	}
}

func TestPrivacyHoldCaptureRestartAndArrival(t *testing.T) {
	isolateActivityPrivacyTest(t)
	root := t.TempDir()
	store, err := NewActivityStore(root)
	if err != nil {
		t.Fatal(err)
	}
	am := &activityMonitorState{enabled: true, configDir: root, store: store, ocrEngine: holdOCREngine{fakeOCREngine{true}}, piiSem: make(chan struct{}, 1)}
	am.newPrivacyDetector = func(string, pii.ProgressFn) (pii.Detector, error) { return nil, models.ErrNotReady }
	app := &App{ctx: t.Context(), activity: am, store: newVisionSwitchStore(t)}
	withVisionPrivacyModel(t, true)
	img := image.NewGray(image.Rect(0, 0, 128, 128))
	for i := range img.Pix {
		img.SetGray(i%128, i/128, color.Gray{Y: uint8(i * 37)})
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.SnapshotsDir(), "screen.jpg")
	frame := pendingPIISnapshot{path: path, jpegData: buf.Bytes(), quality: snapshotQuality, snapshot: snapshotEntry{relPath: "screen.jpg", timestamp: 1100, axText: "held-only"}}
	if !app.captureWhilePrivacyUnavailable(am, frame) {
		t.Fatal("missing model bypassed hold")
	}
	waitForPrivacyHold(t, func() bool {
		am.mu.Lock()
		defer am.mu.Unlock()
		return len(am.allSnapshots) == 1 && !am.pendingPIIDraining
	})
	snap := am.allSnapshots[0]
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	obs, _ := am.ocrEngine.RecognizeJPEGWithBoxes(nil, nil, ocr.PriorityMid)
	want, err := pii.MaskRegions(buf.Bytes(), pii.DetectWithRegex(obs), snapshotQuality)
	if err != nil || !snap.privacyHeld || !bytes.Equal(want, before) || bytes.Equal(before, buf.Bytes()) {
		t.Fatal("captured frame was not kept with regex masks and a hold")
	}
	start := time.Now().UnixMilli()
	session := ActivitySessionRow{ID: "held", StartTime: start, EndTime: start + 1000, AppNames: `["Notes"]`, TriageStatus: "pending", EventCount: triagerMinEvents, SnapshotCount: 1}
	if err := store.SaveSessionWithSnapshots(session, []ActivitySnapshotRow{{SessionID: session.ID, FilePath: snap.relPath, PrivacyHeld: snap.privacyHeld, StorageTier: "hot", AXText: "held-only", Timestamp: 1100}}); err != nil {
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
	am.store = store
	local, err := store.GetSessionSnapshotsIncludingProtected(session.ID)
	if err != nil || len(local) != 1 || !local[0].PrivacyHeld {
		t.Fatalf("reopened local timeline: %+v %v", local, err)
	}
	if err := store.SaveOCRFrame(OCRFrameRow{SessionID: session.ID, SegmentID: local[0].ID, Text: "hidden passage", BoxesJSON: "[]"}); err != nil {
		t.Fatal(err)
	}
	if text, err := store.localOCRTextBySnapshot(session.ID, local[0].ID); err != nil || text != "hidden passage" {
		t.Fatalf("local held OCR=%q: %v", text, err)
	}
	if text, err := store.GetOCRTextBySnapshot(session.ID, local[0].ID); err != nil || text != "" {
		t.Fatalf("model-visible held OCR=%q: %v", text, err)
	}
	runner := &memoryTranslationFakeRunner{ready: true, response: "{}"}
	app.runner = runner
	triager := NewActivityTriager(app, nil)
	triager.triageGroup(t.Context(), sessionGroup{Sessions: []ActivitySessionRow{session}})
	triager.wg.Wait()
	triager.describeWeChatScreenshot(t.Context(), &agent.ImageAttachment{DataBase64: "held"}, session.ID)
	if runner.calls != 0 || app.activityModelSessionIDs(store, start, start+1000)[session.ID] || app.focusedSnapshotAllowsModelRead(local[0]) {
		t.Fatal("held session reached model, vision, or Chat context")
	}
	detector := &holdDetector{}
	am.newPrivacyDetector = func(string, pii.ProgressFn) (pii.Detector, error) { return detector, nil }
	finishArrivalFixture(t, "privacy", func(ctx context.Context, e models.Entry) { app.modelDownloadCompleted(ctx, e, nil, nil) })
	waitForPrivacyHold(t, func() bool { return app.activitySessionAllowsModelRead(store, session) })
	am.privacyReleaseMu.Lock()
	am.privacyReleaseMu.Unlock()
	if !triager.visionSessionAllowsModelRead(session.ID) || !app.focusedSnapshotAllowsModelRead(local[0]) {
		t.Fatal("released session still withheld from vision or Chat")
	}
	if app.deferExtractSessionUntilPIIReady(am, session.ID) {
		t.Fatal("released session still deferred")
	}
	after, err := os.ReadFile(path)
	if err != nil || bytes.Equal(before, after) || detector.calls.Load() != 1 {
		t.Fatal("arrival did not rewrite frame with GLiNER")
	}
	rows, err := store.GetOCRFramesBySession(session.ID)
	if err != nil || len(rows) != 1 || rows[0].Text != "Remaining visible text" {
		t.Fatalf("stale OCR: %+v %v", rows, err)
	}
	if app.captureWhilePrivacyUnavailable(am, frame) {
		t.Fatal("ready model marked a new capture")
	}
}

func TestPrivacyHoldFailedRemask(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTimelineTestStore(t)
	am := &activityMonitorState{enabled: true, store: store, ocrEngine: holdOCREngine{fakeOCREngine{true}}, piiDetector: &holdDetector{failed: true}}
	app := &App{ctx: t.Context(), activity: am}
	session := ActivitySessionRow{ID: "failed", AppNames: `["Notes"]`, StartTime: 1, EndTime: 2}
	if err := store.SaveSessionWithSnapshots(session, []ActivitySnapshotRow{{SessionID: session.ID, FilePath: "bad.jpg", PrivacyHeld: true, StorageTier: "hot"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.SnapshotsDir(), "bad.jpg")
	data := solidTestJPEG(t)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	app.releasePrivacyHolds(am)
	batch, err := store.heldSnapshotBatch(t.Context(), 0)
	if err != nil || len(batch) != 1 || app.activitySessionAllowsModelRead(store, session) {
		t.Fatal("failed frame released")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("failed release changed held JPEG")
	}
}

func TestPrivacyHoldMigration(t *testing.T) {
	store := newTimelineTestStore(t)
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE activity_ocr_frames DROP COLUMN privacy_held_text; DROP TRIGGER snapshot_hold_source_revision; DROP INDEX idx_snapshot_privacy_hold; ALTER TABLE activity_snapshots DROP COLUMN privacy_held; INSERT INTO activity_snapshots(timestamp,file_path) VALUES(1,'legacy.jpg')`); err != nil {
		t.Fatal(err)
	}
	if err := migrateSnapshotPrivacyHold(tx); err != nil {
		t.Fatal(err)
	}
	var held bool
	if err := tx.QueryRow(`SELECT privacy_held FROM activity_snapshots WHERE file_path='legacy.jpg'`).Scan(&held); err != nil || held {
		t.Fatalf("legacy row: held=%t err=%v", held, err)
	}
}

func TestPrivacyHoldRecoversBeforeSessionFlush(t *testing.T) {
	store := newTimelineTestStore(t)
	frame := snapshotEntry{privacyHeld: true, relPath: "interrupted.jpg", app: "Notes", timestamp: 12}
	if err := store.stagePrivacyHold(frame.activityRow("")); err != nil {
		t.Fatal(err)
	}
	if err := store.recoverPrivacyHolds(13); err != nil {
		t.Fatal(err)
	}
	if err := store.recoverPrivacyHolds(13); err != nil {
		t.Fatal(err)
	}
	frames, err := store.heldSnapshotBatch(t.Context(), 0)
	if err != nil || len(frames) != 1 || frames[0].path != frame.relPath {
		t.Fatalf("recovered=%v err=%v", frames, err)
	}
	session, err := store.GetSessionByID(frames[0].session)
	if err != nil || session == nil || session.TriageStatus != "protected" {
		t.Fatalf("lost source history was not protected: %v %v", session, err)
	}
}

func TestPrivacyHoldCancelledRelease(t *testing.T) {
	store := newTimelineTestStore(t)
	am := &activityMonitorState{enabled: true, store: store, piiDetector: &holdDetector{}, ocrEngine: holdOCREngine{fakeOCREngine{true}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	app := &App{ctx: ctx, activity: am}
	session := ActivitySessionRow{ID: "cancel", AppNames: "[]"}
	if err := store.SaveSessionWithSnapshots(session, []ActivitySnapshotRow{{SessionID: session.ID, FilePath: "cancel.jpg", PrivacyHeld: true}}); err != nil {
		t.Fatal(err)
	}
	app.releasePrivacyHolds(am)
	if !app.deferExtractSessionUntilPIIReady(am, session.ID) {
		t.Fatal("cancelled release cleared hold")
	}
}

func addHeldRetentionFrame(t *testing.T, store *ActivityStore, at time.Time, data []byte) (int64, string) {
	t.Helper()
	insertSession(t, store, "held", at, "dismissed")
	ocrDone(t, store, "held")
	id, rel := addSnapshot(t, store, "held", at, 100, 100, data, "hot")
	if _, err := store.db.Exec(`UPDATE activity_snapshots SET privacy_held=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOCRFrame(OCRFrameRow{SessionID: "held", SegmentID: id, Text: "private capture"}); err != nil {
		t.Fatal(err)
	}
	return id, filepath.Join(store.SnapshotsDir(), rel)
}

func checkHeldFrameErased(t *testing.T, store *ActivityStore, id int64, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still present or inaccessible: %v", err)
	}
	// Simulate the OCR worker finishing after the frame was deleted.
	if err := store.saveOCRFrame(OCRFrameRow{SessionID: "held", SegmentID: id, Text: "private capture"}, true); err != nil {
		t.Fatal(err)
	}
	var snapshots, ocr int
	if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM activity_snapshots WHERE id=?),(SELECT COUNT(*) FROM activity_ocr_frames WHERE segment_id=? AND session_id='held')`, id, id).Scan(&snapshots, &ocr); err != nil || snapshots != 0 || ocr != 0 {
		t.Fatalf("remaining snapshots=%d OCR=%d err=%v", snapshots, ocr, err)
	}
}

func TestPrivacyHoldReviewRetention(t *testing.T) {
	for _, mode := range []string{"keep time", "size cap"} {
		t.Run(mode, func(t *testing.T) {
			store := newTestStore(t)
			const mb = 1 << 20
			id, path := addHeldRetentionFrame(t, store, time.Now().Add(-30*24*time.Hour), make([]byte, mb))
			r := storageTestRotator(store, 1)
			cfg := r.snapshotConfig()
			switch mode {
			case "keep time":
				cfg.ColdRetention = 7 * 24 * time.Hour
				r.purgeExpiredSessions(cfg)
			case "size cap":
				usage := r.measure()
				cfg.MaxTotalSize = usage.TotalBytes - mb/2
				removed, _ := r.enforceBudget(cfg, usage, 0, false)
				if removed != 1 {
					t.Fatalf("budget removed %d images", removed)
				}
			}
			checkHeldFrameErased(t, store, id, path)
		})
	}
}

func TestPrivacyHoldReviewUnavailable(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			store := newTestStore(t)
			id, path := addHeldRetentionFrame(t, store, time.Now(), []byte("invalid JPEG"))
			if kind == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			detector := &holdDetector{}
			am := &activityMonitorState{enabled: true, store: store, piiDetector: detector}
			app := &App{ctx: t.Context(), activity: am}
			var logs bytes.Buffer
			output := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(output)
			app.releasePrivacyHolds(am)
			log.SetOutput(output)
			if !strings.Contains(logs.String(), "[privacy-hold] discarded 1 unavailable held frame(s)") || strings.Contains(logs.String(), "[privacy-hold] released frame=") {
				t.Fatalf("discard log: %s", logs.String())
			}
			if detector.calls.Load() != 0 || app.deferExtractSessionUntilPIIReady(am, "held") {
				t.Fatal("unavailable frame was sent to detector or still blocks session")
			}
			checkHeldFrameErased(t, store, id, path)
			var n, bytes int64
			if err := store.db.QueryRow(`SELECT snapshot_count,total_size_bytes FROM activity_sessions WHERE id='held'`).Scan(&n, &bytes); err != nil || n != 0 || bytes != 0 {
				t.Fatalf("remaining count=%d size=%d err=%v", n, bytes, err)
			}
		})
	}
}

func TestPrivacyHoldReviewStagedRevisions(t *testing.T) {
	store := newTestStore(t)
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO activity_evidence_source_revisions(session_id,revision) VALUES(NULL,1),(NULL,1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateOwnedSnapshotRevisions(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	row := snapshotEntry{privacyHeld: true, relPath: "pending.jpg", timestamp: 1}.activityRow("")
	if err := store.stagePrivacyHold(row); err != nil {
		t.Fatal(err)
	}
	store.discardStagedPrivacyHold(row.FilePath)
	if err := store.stagePrivacyHold(row); err != nil {
		t.Fatal(err)
	}
	row.SessionID = "owned"
	if err := store.SaveSessionWithSnapshots(ActivitySessionRow{ID: "owned", AppNames: "[]"}, []ActivitySnapshotRow{row}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE activity_snapshots SET session_id=NULL WHERE session_id='owned'`); err != nil {
		t.Fatal(err)
	}
	var unowned, rev int64
	if err := store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM activity_evidence_source_revisions WHERE session_id IS NULL),revision FROM activity_evidence_source_revisions WHERE session_id='owned'`).Scan(&unowned, &rev); err != nil || unowned != 0 || rev != 2 {
		t.Fatalf("unowned rows=%d owner revision=%d err=%v", unowned, rev, err)
	}
}
