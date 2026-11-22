package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image/jpeg"
	"log"
	"os"
	"sort"

	"wails-gui/internal/pii"
	"wails-gui/models"
)

func migrateSnapshotPrivacyHold(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE activity_snapshots ADD COLUMN privacy_held INTEGER NOT NULL DEFAULT 0;
        ALTER TABLE activity_ocr_frames ADD COLUMN privacy_held_text TEXT;
        CREATE INDEX idx_snapshot_privacy_hold ON activity_snapshots(session_id,id) WHERE privacy_held=1;
        CREATE TRIGGER snapshot_hold_source_revision AFTER UPDATE OF privacy_held ON activity_snapshots
        WHEN OLD.privacy_held != NEW.privacy_held AND NEW.session_id IS NOT NULL BEGIN
            INSERT INTO activity_evidence_source_revisions(session_id,revision) VALUES(NEW.session_id,1)
            ON CONFLICT(session_id) DO UPDATE SET revision=revision+1;
        END`)
	return err
}

// captureWhilePrivacyUnavailable returns true when the startup queue or the
// local regex-only writer took ownership of the capture.
func (a *App) captureWhilePrivacyUnavailable(am *activityMonitorState, frame pendingPIISnapshot) bool {
	ready, starting := am.piiDetectorState()
	if ready {
		return false
	}
	if !starting {
		a.startPIIDetectorAsync(am)
	}
	// The external helper can still be starting when its manifest is absent.
	// Only installed assets qualify for the short, volatile startup queue.
	_, starting = am.piiDetectorState()
	installed := starting && models.Present(am.configDir, "gliner-pii")
	am.piiDetectorMu.RLock()
	ready = am.piiDetector != nil && am.piiDetector.IsReady()
	queued := !ready && installed && am.piiInitInFlight && am.queuePendingPIISnapshot(frame)
	am.piiDetectorMu.RUnlock()
	if !ready && !queued {
		a.keepPrivacyHeldFrame(am, frame)
	}
	return !ready
}

const sqlSessionHasNoPrivacyHold = ` NOT EXISTS (SELECT 1 FROM activity_snapshots h WHERE h.session_id=activity_sessions.id AND h.privacy_held=1)`

// Once startup has settled, apply backpressure to capture instead of keeping
// a second backlog of unmasked JPEGs or skipping frames at the PII cooldown.
func (a *App) keepPrivacyHeldFrame(am *activityMonitorState, frame pendingPIISnapshot) {
	am.piiSem <- struct{}{}
	defer func() { <-am.piiSem }()
	a.maskAndKeepPrivacyHold(am, frame)
}

func (a *App) maskAndKeepPrivacyHold(am *activityMonitorState, frame pendingPIISnapshot) error {
	_, ok, err := am.processPIISnapshot(frame.path, frame.jpegData, frame.quality, false, true, func(data []byte) (int64, bool) {
		return int64(len(data)), a.keepHeldPixels(am, frame, data)
	})
	if !ok {
		a.keepFullyBlurredHold(am, frame)
	}
	return err
}

// When OCR cannot establish which regions are safe, retain the whole-frame
// blur already permitted by unmaskedWriteAllowed. Never fall back to raw pixels.
func (a *App) keepFullyBlurredHold(am *activityMonitorState, frame pendingPIISnapshot) {
	if a.shouldDiscardPIIOutput(am) {
		return
	}
	data, err := pii.BlurEntireImage(frame.jpegData, frame.quality)
	if err != nil {
		log.Printf("[privacy-hold] failed to blur unreadable capture: %v", err)
		return
	}
	a.keepHeldPixels(am, frame, data)
}

// Commit the pending row before publishing the JPEG so a crash cannot leave
// a completed capture looking like an orphan to startup reconciliation.
func (a *App) keepHeldPixels(am *activityMonitorState, frame pendingPIISnapshot, data []byte) bool {
	if a.IsClosedEyesModeEnabled() || am.store == nil {
		return false
	}
	snap := frame.snapshot
	snap.privacyHeld = true
	snap.sizeBytes = int64(len(data))
	if err := am.store.stagePrivacyHold(snap.activityRow("")); err != nil {
		log.Printf("[privacy-hold] stage failed frame=%s: %v", snap.relPath, err)
		return false
	}
	if _, ok := writePIIProcessedSnapshot(frame.path, data, a.IsClosedEyesModeEnabled); !ok {
		am.store.discardStagedPrivacyHold(snap.relPath)
		return false
	}
	am.mu.Lock()
	paused := a.IsClosedEyesModeEnabled()
	if am.enabled && !paused {
		am.appendCapturedSnapshotLocked(snap, frame.bundleID, frame.parseHash)
	}
	am.mu.Unlock()
	if paused {
		am.store.discardStagedPrivacyHold(snap.relPath)
		_ = os.Remove(frame.path)
		return false
	}
	activityPipelineMetrics.captureCompleted.Add(1)
	return true
}

type heldSnapshot struct {
	id            int64
	session, path string
	size          int64
}

func (s *ActivityStore) heldSnapshotBatch(ctx context.Context, after int64) ([]heldSnapshot, error) {
	rows, err := s.reader().QueryContext(ctx, `SELECT id,session_id,file_path,COALESCE(size_bytes,0) FROM activity_snapshots WHERE privacy_held=1 AND session_id IS NOT NULL AND id>? ORDER BY id LIMIT 32`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var frames []heldSnapshot
	for rows.Next() {
		var f heldSnapshot
		if err := rows.Scan(&f.id, &f.session, &f.path, &f.size); err != nil {
			return nil, err
		}
		frames = append(frames, f)
	}
	return frames, rows.Err()
}

func (a *App) releasePrivacyHolds(am *activityMonitorState) {
	ctx := a.ctx
	if ctx == nil || am.store == nil {
		return
	}
	am.privacyReleaseMu.Lock()
	defer am.privacyReleaseMu.Unlock()
	var discarded int
	defer func() {
		if discarded > 0 {
			log.Printf("[privacy-hold] discarded %d unavailable held frame(s)", discarded)
		}
	}()
	var after int64
	for ctx.Err() == nil && !a.shouldDiscardPIIOutput(am) {
		batch, err := am.store.heldSnapshotBatch(ctx, after)
		if err != nil {
			log.Printf("[privacy-hold] scan failed: %v", err)
			return
		}
		if len(batch) == 0 {
			return
		}
		for _, f := range batch {
			if ctx.Err() != nil || a.shouldDiscardPIIOutput(am) {
				return
			}
			after = f.id
			err := a.releaseHeldSnapshot(ctx, am, f)
			if errors.Is(err, errHeldImageDiscarded) {
				discarded++
				continue
			}
			if err != nil {
				log.Printf("[privacy-hold] re-mask failed frame=%d: %v", f.id, err)
				continue
			}
			log.Printf("[privacy-hold] released frame=%d", f.id)
			if !a.deferExtractSessionUntilPIIReady(am, f.session) && ctx.Err() == nil && !a.shouldDiscardPIIOutput(am) {
				if session, err := am.store.GetSessionByID(f.session); err == nil && session != nil && session.TriageStatus != "protected" {
					a.onSessionDateRollover(*session)
				}
			}
		}
	}
}

// Local flushes persist even through a long download. Model extraction and
// the day-rollover task wait for the last held member of the saved session.
func (a *App) deferExtractSessionUntilPIIReady(am *activityMonitorState, sessionID string) bool {
	var held bool
	err := am.store.reader().QueryRow(`SELECT EXISTS(SELECT 1 FROM activity_snapshots WHERE session_id=? AND privacy_held=1)`, sessionID).Scan(&held)
	return err != nil || held
}

func (a *App) releaseHeldSnapshot(ctx context.Context, am *activityMonitorState, f heldSnapshot) error {
	path, ok := captureFileUnder(am.store.SnapshotsDir(), f.path)
	if !ok {
		return errors.New("invalid held frame path")
	}
	data, err := readHeldJPEG(path)
	if err != nil {
		return am.store.discardUnavailableHeldImage(ctx, f, path)
	}
	var masked []byte
	_, ok, err = am.processPIISnapshot(path, data, snapshotQuality, true, false, func(b []byte) (int64, bool) { masked = b; return int64(len(b)), true })
	if err != nil {
		return err
	}
	if !ok || len(masked) == 0 {
		return errors.New("masking failed")
	}
	// Background OCR may have read the regex-only image. Refresh its text with
	// the same focus crop and OCR normalization before the session becomes eligible.
	am.privacyImageMu.Lock()
	defer am.privacyImageMu.Unlock()
	var snap ActivitySnapshotRow
	snap.SessionID, snap.ID, snap.FilePath = f.session, f.id, f.path
	var indexed bool
	err = am.store.reader().QueryRowContext(ctx, `SELECT COALESCE(focus_rect_x,0),COALESCE(focus_rect_y,0),COALESCE(focus_rect_w,0),COALESCE(focus_rect_h,0),
        EXISTS(SELECT 1 FROM activity_ocr_frames o WHERE o.session_id=h.session_id AND o.segment_id=h.id AND (o.text!='' OR o.privacy_held_text IS NOT NULL))
        FROM activity_snapshots h WHERE h.id=? AND privacy_held=1`, f.id).Scan(&snap.FocusRectX, &snap.FocusRectY, &snap.FocusRectW, &snap.FocusRectH, &indexed)
	if err != nil {
		return err
	}
	var fresh OCRFrameRow
	if indexed {
		fresh, _, err = recognizeSnapshotOCR(am.ocrEngine, snap, masked, getOCRLanguagePrefs(am.ocrEngine), 0)
		if err != nil {
			return err
		}
	}
	if ctx.Err() != nil || a.shouldDiscardPIIOutput(am) {
		return errors.New("release stopped")
	}
	// Take the SQLite writer lock before replacing the image. Forget either
	// wins first (the update matches nothing) or removes the rewritten file.
	tx, err := am.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE activity_snapshots SET privacy_held=0,size_bytes=? WHERE id=? AND session_id=? AND file_path=? AND privacy_held=1`, len(masked), f.id, f.session, f.path)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return fmt.Errorf("held frame no longer available (%d rows)", n)
	}
	// Text-less highlight rows must remain text-less. FTS follows the update.
	if _, err = tx.ExecContext(ctx, `UPDATE activity_ocr_frames SET text=CASE WHEN text='' AND privacy_held_text IS NULL THEN '' ELSE ? END,privacy_held_text=NULL,boxes_json='',confidence=? WHERE session_id=? AND segment_id=?`, fresh.Text, fresh.Confidence, f.session, f.id); err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, `UPDATE activity_sessions SET total_size_bytes=MAX(total_size_bytes+?,0) WHERE id=?`, int64(len(masked))-f.size, f.session); err != nil {
		return err
	}
	if err = writeSnapshotFile(path, masked); err != nil {
		return err
	}
	return tx.Commit()
}

// A normal session flush adopts this row in its transaction. Until then no
// session can use its contents, and orphan-file reconciliation sees its JPEG.
func (s *ActivityStore) stagePrivacyHold(row ActivitySnapshotRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.SaveSnapshots(tx, []ActivitySnapshotRow{row}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[privacy-hold] marked frame=%s", row.FilePath)
	return nil
}

// A crash before session flush loses the event buffer. Give each surviving
// frame a local timeline owner. Without that buffer we cannot reconstruct
// the session's private-window and protected-app history, so its recovered
// session remains protected even after the image has been re-masked.
func (s *ActivityStore) recoverPrivacyHolds(beforeMs int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO activity_sessions(id,start_time,end_time,trigger_kind,app_names,event_count,segment_count,snapshot_count,total_size_bytes,triage_status,created_at)
       SELECT 'recovered-screen-'||id,timestamp,timestamp,'recovered_snapshot',json_array(COALESCE(app,'')),0,0,1,COALESCE(size_bytes,0),'protected',timestamp
       FROM activity_snapshots WHERE privacy_held=1 AND session_id IS NULL AND timestamp<?`, beforeMs); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE activity_snapshots SET session_id='recovered-screen-'||id WHERE privacy_held=1 AND session_id IS NULL AND timestamp<?`, beforeMs); err != nil {
		return err
	}
	return tx.Commit()
}

// Only the Activity detail bindings may read the pre-release OCR column.
// All model, embedding, FTS and derivative queries see an empty text field.
func (s *ActivityStore) localOCRTextBySnapshot(session string, id int64) (string, error) {
	var text string
	err := s.reader().QueryRow(`SELECT COALESCE(privacy_held_text,text) FROM activity_ocr_frames WHERE session_id=? AND COALESCE(privacy_held_text,text)!='' ORDER BY ABS(segment_id-?),id DESC LIMIT 1`, session, id).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return text, err
}

func (s *ActivityStore) localOCRMatchFrames(session, keyword string) ([]OCRFrameRow, error) {
	frames, err := s.GetOCRMatchFrames(session, keyword)
	if err != nil {
		return nil, err
	}
	rows, err := s.reader().Query(`SELECT id,segment_id,frame_time,privacy_held_text,confidence FROM activity_ocr_frames WHERE session_id=? AND instr(lower(privacy_held_text),lower(?))>0 ORDER BY frame_time`, session, keyword)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f := OCRFrameRow{SessionID: session}
		if err := rows.Scan(&f.ID, &f.SegmentID, &f.FrameTime, &f.Text, &f.Confidence); err != nil {
			return nil, err
		}
		frames = append(frames, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].FrameTime < frames[j].FrameTime })
	return frames, nil
}

func (s *ActivityStore) discardStagedPrivacyHold(path string) {
	if _, err := s.db.Exec(`DELETE FROM activity_snapshots WHERE session_id IS NULL AND privacy_held=1 AND file_path=?`, path); err != nil {
		log.Printf("[privacy-hold] discard pending row: %v", err)
	}
}

var errHeldImageDiscarded = errors.New("discarded unavailable held image")

func readHeldJPEG(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular JPEG file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	_, err = jpeg.Decode(bytes.NewReader(data))
	return data, err
}

// Recheck under the same writer lock used for release and retention. A file
// restored in the meantime can be re-masked on a later pass.
func (s *ActivityStore) discardUnavailableHeldImage(ctx context.Context, f heldSnapshot, path string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	removed, err := deleteHeldSnapshot(tx, f.id, f.session, f.path)
	if err != nil {
		return err
	}
	if !removed {
		return sql.ErrNoRows
	}
	_, readErr := readHeldJPEG(path)
	if readErr == nil {
		return errors.New("held image became readable during cleanup")
	}
	var pathErr *os.PathError
	if errors.As(readErr, &pathErr) && !errors.Is(readErr, os.ErrNotExist) && !errors.Is(readErr, os.ErrPermission) {
		return readErr
	}
	info, err := os.Lstat(path)
	if err == nil && !info.IsDir() {
		// An unreadable file may still exist. Erase it before committing the
		// deletion, unless another row owns it.
		var shared bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM activity_snapshots WHERE file_path=?)`, f.path).Scan(&shared); err != nil {
			return err
		}
		if !shared {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return errHeldImageDiscarded
}

// Remove the snapshot and its derived text together. Snapshot OCR uses
// segment_id for the snapshot ID and has no foreign key to that row.
func deleteHeldSnapshot(tx *sql.Tx, id int64, session, path string) (bool, error) {
	var size int64
	err := tx.QueryRow(`DELETE FROM activity_snapshots WHERE id=? AND session_id IS NULLIF(?,'') AND file_path=? AND privacy_held=1 RETURNING COALESCE(size_bytes,0)`, id, session, path).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if session != "" {
		if _, err := tx.Exec(`DELETE FROM activity_ocr_frames WHERE session_id=? AND segment_id=?`, session, id); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`UPDATE activity_sessions SET snapshot_count=(SELECT COUNT(*) FROM activity_snapshots WHERE session_id=? AND storage_tier!=?),total_size_bytes=MAX(total_size_bytes-?,0) WHERE id=?`, session, snapshotTierMissing, size, session); err != nil {
			return false, err
		}
	}
	return true, nil
}
