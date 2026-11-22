package main

// Persistent OCR highlight-box cache.
//
// The background OCR worker always ran RecognizeJPEGWithBoxes and then threw
// the boxes away, so the click-time tag highlight (GetOCRHighlightBoxes) had
// to re-run a full Accurate Vision OCR per frame just to recover coordinates.
// This file closes that loop:
//
//   - worker boxes are mapped from the focus-crop space back to full-frame
//     normalized coordinates and persisted on the OCR frame row (boxes_json);
//   - the highlight path reads stored boxes first and only falls back to an
//     on-demand OCR for frames the worker never sampled;
//   - on-demand results are written back (text-less box-cache rows), so any
//     frame pays the OCR cost at most once across app restarts;
//   - concurrent highlight calls for the same frame (e.g. several keyword
//     chips active at once) share a single OCR via singleflight — the OCR
//     output is keyword-independent, only the final filter differs.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	// Register the JPEG decoder for image.DecodeConfig in fullImageDimensions;
	// this file must not depend on a sibling file's jpeg import surviving
	// future refactors.
	_ "image/jpeg"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wails-gui/internal/platform/ocr"
)

// ocrHighlightMaxConcurrent caps concurrent on-demand highlight OCR engine
// runs (per-frame Accurate Vision passes, each a helper process). 2 keeps a
// single frame near its ~1s standalone latency while still overlapping I/O;
// higher values let parallel runs thrash each other (see ocrBoxesSem).
const ocrHighlightMaxConcurrent = 2

// observationsToFullFrameBoxes converts engine observations into highlight
// boxes in full-frame normalized coordinates. cropRect is the pixel rectangle
// of the OCR input within the full image (zero when OCR saw the full frame);
// imgW/imgH are the full image's pixel dimensions.
func observationsToFullFrameBoxes(observations []ocr.TextObservation, cropRect image.Rectangle, imgW, imgH int) []OCRHighlightBox {
	boxes := make([]OCRHighlightBox, 0, len(observations))
	cropped := !cropRect.Empty() && imgW > 0 && imgH > 0
	for _, obs := range observations {
		b := OCRHighlightBox{
			X:      obs.X,
			Y:      obs.Y,
			Width:  obs.Width,
			Height: obs.Height,
			Text:   obs.Text,
		}
		if cropped {
			cw := float64(cropRect.Dx())
			ch := float64(cropRect.Dy())
			b.X = (float64(cropRect.Min.X) + obs.X*cw) / float64(imgW)
			b.Y = (float64(cropRect.Min.Y) + obs.Y*ch) / float64(imgH)
			b.Width = obs.Width * cw / float64(imgW)
			b.Height = obs.Height * ch / float64(imgH)
		}
		boxes = append(boxes, b)
	}
	return boxes
}

// marshalOCRBoxes serializes boxes for the boxes_json column. Always returns
// at least "[]" so a stored value is distinguishable from the empty-string
// "not computed" default.
func marshalOCRBoxes(boxes []OCRHighlightBox) string {
	if len(boxes) == 0 {
		return "[]"
	}
	data, err := json.Marshal(boxes)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// filterOCRBoxesByKeyword keeps boxes whose line text contains the keyword,
// case-insensitive and CJK-space-insensitive — the same matching semantics as
// GetSemanticEventKeywordFrames so "which frames match" and "where on the
// frame" never disagree.
func filterOCRBoxesByKeyword(boxes []OCRHighlightBox, keyword string) []OCRHighlightBox {
	kw := strings.ToLower(removeCJKSpaces(keyword))
	if kw == "" {
		return nil
	}
	var out []OCRHighlightBox
	for _, b := range boxes {
		if strings.Contains(strings.ToLower(removeCJKSpaces(b.Text)), kw) {
			out = append(out, b)
		}
	}
	return out
}

// snapshotOCRIdentity locates a snapshot row by its stored (relative) file
// path and returns the keys the OCR box cache needs. FrameTime mirrors the
// worker's session-relative seconds offset so write-through rows look like
// worker rows.
type snapshotOCRIdentity struct {
	held       bool
	SnapshotID int64
	SessionID  string
	FrameTime  float64
}

// GetSnapshotOCRIdentityByFilePath resolves a snapshot file path to the keys
// used by the OCR box cache. found=false (no error) when no snapshot has this
// path — e.g. a legacy video-segment frame, which the cache simply skips.
func (s *ActivityStore) GetSnapshotOCRIdentityByFilePath(relPath string) (snapshotOCRIdentity, bool, error) {
	var id snapshotOCRIdentity
	var snapTS, sessStart int64
	err := s.db.QueryRow(`
		SELECT snap.id, snap.session_id, snap.timestamp, sess.start_time,snap.privacy_held
		FROM activity_snapshots snap
		JOIN activity_sessions sess ON sess.id = snap.session_id
		WHERE snap.file_path = ?
		ORDER BY snap.id DESC
		LIMIT 1`, relPath).Scan(&id.SnapshotID, &id.SessionID, &snapTS, &sessStart, &id.held)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return snapshotOCRIdentity{}, false, nil
		}
		return snapshotOCRIdentity{}, false, fmt.Errorf("resolve snapshot by path: %w", err)
	}
	id.FrameTime = float64(snapTS-sessStart) / 1000.0
	return id, true, nil
}

// GetStoredOCRBoxes returns the persisted boxes_json for a snapshot, newest
// row first. found=false when no row has computed boxes (empty boxes_json is
// treated as missing so legacy text-only rows fall through to on-demand OCR).
func (s *ActivityStore) GetStoredOCRBoxes(sessionID string, snapshotID int64) (string, bool, error) {
	var boxesJSON string
	err := s.db.QueryRow(`
		SELECT boxes_json FROM activity_ocr_frames
		WHERE session_id = ? AND segment_id = ? AND boxes_json != ''
		ORDER BY id DESC
		LIMIT 1`, sessionID, snapshotID).Scan(&boxesJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("query stored ocr boxes: %w", err)
	}
	return boxesJSON, true, nil
}

// SaveOCRBoxesForSnapshot writes computed boxes back to the OCR frame store.
// If the snapshot already has a worker OCR row (text without boxes — legacy
// rows predating the boxes_json column) the newest row is updated in place;
// otherwise a text-less box-cache row is inserted. The insert deliberately
// leaves text empty: on-demand OCR sees the FULL frame while worker rows are
// focus-cropped, so persisting its text would silently widen the "what was
// the user looking at" signal that FTS search and the keyword-frame Visual
// flag are built on.
func (s *ActivityStore) SaveOCRBoxesForSnapshot(id snapshotOCRIdentity, boxesJSON string) error {
	if boxesJSON == "" {
		boxesJSON = "[]"
	}
	res, err := s.db.Exec(`
		UPDATE activity_ocr_frames SET boxes_json = ?
		WHERE id = (
			SELECT id FROM activity_ocr_frames
			WHERE session_id = ? AND segment_id = ?
			ORDER BY id DESC
			LIMIT 1
		)`, boxesJSON, id.SessionID, id.SnapshotID)
	if err != nil {
		return fmt.Errorf("update ocr boxes: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := s.db.Exec(`
		INSERT INTO activity_ocr_frames (session_id, segment_id, frame_time, text, confidence, boxes_json)
		VALUES (?, ?, ?, '', 0, ?)`,
		id.SessionID, id.SnapshotID, id.FrameTime, boxesJSON); err != nil {
		return fmt.Errorf("insert ocr box-cache row: %w", err)
	}
	return nil
}

// getOrComputeOCRBoxes returns the full observation set for a snapshot in
// full-frame normalized coordinates: stored boxes when the worker (or a prior
// on-demand run) already computed them, otherwise one on-demand OCR shared
// across concurrent callers and written back for every future call.
func (m *activityMonitorState) getOrComputeOCRBoxes(snapshotRelPath string) ([]OCRHighlightBox, error) {
	// Snapshot paths arrive from the renderer and the browser RPC bridge, so
	// treat them as untrusted: anything absolute or escaping SnapshotsDir is
	// rejected before it can reach os.ReadFile (mirrors the validation
	// handleActivitySnapshot performs on the HTTP surface).
	if !filepath.IsLocal(snapshotRelPath) {
		return nil, fmt.Errorf("invalid snapshot path %q", snapshotRelPath)
	}
	store := m.store
	identity, found, err := store.GetSnapshotOCRIdentityByFilePath(snapshotRelPath)
	if err != nil {
		return nil, err
	}
	if found {
		if boxesJSON, ok, err := store.GetStoredOCRBoxes(identity.SessionID, identity.SnapshotID); err != nil {
			return nil, err
		} else if ok {
			var boxes []OCRHighlightBox
			if json.Unmarshal([]byte(boxesJSON), &boxes) == nil {
				return boxes, nil
			}
			// Corrupt JSON: fall through and recompute (the write-back repairs it).
		}
	}

	// One OCR per frame regardless of how many keyword chips race here: the
	// engine output is keyword-independent, callers filter afterwards.
	v, err, _ := m.ocrBoxesFlight.Do(snapshotRelPath, func() (any, error) {
		// Bound concurrent engine runs across DIFFERENT frames (singleflight
		// only dedupes the same frame). Unbounded parallel Accurate OCR
		// helper processes thrash each other: per-frame latency degraded
		// from ~1s standalone to an observed p90 of 50s. The cheap DB-hit
		// path above never touches this gate.
		queueStart := time.Now()
		if m.ocrBoxesSem != nil {
			m.ocrBoxesSem <- struct{}{}
			defer func() { <-m.ocrBoxesSem }()
		}
		queuedMs := time.Since(queueStart).Milliseconds()

		fullPath := filepath.Join(store.SnapshotsDir(), snapshotRelPath)
		jpegData, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("read snapshot: %w", err)
		}

		engine := ocr.New()
		prio := ocr.PriorityHigh
		callID := nextOCRBreadcrumbID()
		ocrStart := time.Now()
		log.Printf("[ocr] breadcrumb start call_id=%d source=highlight priority=%s file=%s bytes=%d queued_ms=%d",
			callID, ocrPriorityLabel(prio), filepath.Base(snapshotRelPath), len(jpegData), queuedMs)

		observations, err := engine.RecognizeJPEGWithBoxes(jpegData, getOCRLanguagePrefs(engine), prio)
		if err != nil {
			log.Printf("[ocr] breadcrumb failed call_id=%d source=highlight duration_ms=%d err=%v",
				callID, time.Since(ocrStart).Milliseconds(), err)
			return nil, fmt.Errorf("ocr: %w", err)
		}
		log.Printf("[ocr] breadcrumb done call_id=%d source=highlight duration_ms=%d observations=%d",
			callID, time.Since(ocrStart).Milliseconds(), len(observations))

		// On-demand OCR sees the full frame, so observations are already
		// full-frame normalized — no crop mapping needed.
		boxes := observationsToFullFrameBoxes(observations, image.Rectangle{}, 0, 0)
		if found && !identity.held {
			if err := store.SaveOCRBoxesForSnapshot(identity, marshalOCRBoxes(boxes)); err != nil {
				log.Printf("[ocr] write-through of highlight boxes failed for %s: %v", snapshotRelPath, err)
			}
		}
		return boxes, nil
	})
	if err != nil {
		return nil, err
	}
	boxes, _ := v.([]OCRHighlightBox)
	return boxes, nil
}

// fullImageDimensions reads the pixel dimensions from a JPEG header without
// decoding the pixel data.
func fullImageDimensions(jpegData []byte) (int, int, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(jpegData))
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}
