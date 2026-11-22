package main

import (
	"context"
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/platform/ocr"
)

const (
	ocrCheckInterval            = 2 * time.Minute
	ocrFrameIntervalSec         = 2.5 // extract a JPEG frame every 2.5 seconds
	ocrSnapshotFrameIntervalSec = 15.0
	ocrMaxSnapshotFramesPerRun  = 12
	ocrBackgroundFramePause     = 350 * time.Millisecond
	ocrMinTextLen               = 5   // skip frames with less than 5 chars
	ocrDedupThreshold           = 0.9 // skip frames with >90% Jaccard similarity to previous
)

// ActivityOCRWorker runs OCR on recorded activity video segments in the background.
// Follows the same Run/Stop pattern as ActivityTriager.
type ActivityOCRWorker struct {
	app    *App
	engine ocr.Engine
	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewActivityOCRWorker creates a new OCR worker instance.
func NewActivityOCRWorker(app *App) *ActivityOCRWorker {
	return &ActivityOCRWorker{
		app:    app,
		engine: ocr.New(),
	}
}

// Run starts the OCR processing loop. Blocks until ctx is cancelled.
func (w *ActivityOCRWorker) Run(ctx context.Context) {
	if !w.engine.IsSupported() {
		log.Printf("[ocr] OCR not supported on this platform, worker disabled")
		return
	}

	// One-time migration: re-process all sessions with the updated multi-language
	// OCR engine. The marker file ensures this only runs once per data directory.
	w.migrateOCRIfNeeded()

	ctx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()

	// A run cut short (the app quit mid-session) left its session marked
	// processing, which nothing picks up again; hand those back.
	if w.app.activity != nil && w.app.activity.store != nil {
		if n, err := w.app.activity.store.ResetInterruptedOCR(); err != nil {
			log.Printf("[ocr] failed to reset interrupted sessions: %v", err)
		} else if n > 0 {
			log.Printf("[ocr] %d session(s) interrupted mid-OCR are pending again", n)
		}
	}

	// Run once at startup
	w.processAll(ctx)

	ticker := time.NewTicker(ocrCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processAll(ctx)
		}
	}
}

// Stop cancels the OCR worker loop.
func (w *ActivityOCRWorker) Stop() {
	w.mu.Lock()
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.mu.Unlock()
}

// processAll finds all pending OCR sessions and processes them.
func (w *ActivityOCRWorker) processAll(ctx context.Context) {
	if w.app.activity == nil || w.app.activity.store == nil {
		return
	}

	sessions, err := w.app.activity.store.GetSessionsByOCRStatus("pending")
	if err != nil {
		log.Printf("[ocr] failed to get pending sessions: %v", err)
		return
	}

	for _, session := range sessions {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Protected sessions (touched a protected app) must not be OCR'd
		// or embedded: their pre-blur snapshots from non-protected apps
		// would otherwise become searchable via semantic search, leaking
		// correlated context around the protected-app usage. Note:
		// "dismissed" is NOT checked — that is the normal triage-complete
		// state and those sessions should still be OCR'd for search.
		if session.TriageStatus == "protected" {
			if err := w.app.activity.store.UpdateOCRStatus(session.ID, "skipped"); err != nil {
				log.Printf("[ocr] failed to mark protected session %s skipped: %v", session.ID, err)
			}
			continue
		}

		// A screenshot of this session is still in the PII mask. Leave the
		// session pending until its file lands (or its row is removed) so
		// the late frame is indexed too.
		if w.app.activity.pendingSnapshotSessions.has(session.ID) {
			continue
		}

		w.processSession(ctx, session)
	}
}

// processSession runs OCR on a single session.
// For snapshot sessions (SnapshotCount > 0): reads JPEG files directly.
// For legacy HEVC sessions: extracts frames via ffmpeg.
func (w *ActivityOCRWorker) processSession(ctx context.Context, session ActivitySessionRow) {
	store := w.app.activity.store

	// Mark as processing
	if err := store.UpdateOCRStatus(session.ID, "processing"); err != nil {
		log.Printf("[ocr] failed to update status for session %s: %v", session.ID, err)
		return
	}

	langs := getOCRLanguagePrefs(w.engine)
	frameCount := 0

	if session.SnapshotCount > 0 {
		// New snapshot-based session: read JPEG files directly
		snapshots, err := store.GetSessionSnapshots(session.ID)
		if err != nil {
			log.Printf("[ocr] failed to get snapshots for session %s: %v", session.ID, err)
			return
		}
		snapshots = snapshotsWithImages(snapshots)
		if len(snapshots) == 0 {
			store.UpdateOCRStatus(session.ID, "done")
			return
		}

		selected := selectSnapshotsForOCR(snapshots)
		if len(selected) < len(snapshots) {
			log.Printf("[ocr] session %s sampling %d/%d snapshots for background OCR", session.ID, len(selected), len(snapshots))
		}

		for i, snap := range selected {
			select {
			case <-ctx.Done():
				// Hand the session back so the next run finishes it.
				_ = store.UpdateOCRStatus(session.ID, "pending")
				return
			default:
			}

			if !w.indexSnapshot(session, snap, langs) {
				continue
			}
			frameCount++

			if i+1 < len(selected) && !sleepWithContext(ctx, ocrBackgroundFramePause) {
				return
			}
		}
	} else {
		// Legacy HEVC session: extract frames via ffmpeg
		segments, err := store.GetSessionSegments(session.ID)
		if err != nil {
			log.Printf("[ocr] failed to get segments for session %s: %v", session.ID, err)
			return
		}

		if len(segments) == 0 {
			store.UpdateOCRStatus(session.ID, "done")
			return
		}

		ffmpegPath := installedFFmpegPath()
		if ffmpegPath == "" {
			log.Printf("[ocr] ffmpeg not available, skipping session %s", session.ID)
			store.UpdateOCRStatus(session.ID, "pending") // retry later
			return
		}

		for _, seg := range segments {
			select {
			case <-ctx.Done():
				return
			default:
			}

			segmentsDir := store.SegmentsDir()
			hevcPath := fmt.Sprintf("%s/%s", segmentsDir, seg.FilePath)

			segDurationSec := float64(seg.EndTime-seg.StartTime) / 1000.0
			if segDurationSec <= 0 {
				segDurationSec = 30.0
			}

			for t := 0.0; t < segDurationSec; t += ocrFrameIntervalSec {
				select {
				case <-ctx.Done():
					return
				default:
				}

				tStr := fmt.Sprintf("%.1f", t)
				jpegData := extractFrame(ffmpegPath, hevcPath, tStr)
				if len(jpegData) == 0 {
					continue
				}

				prio := ocr.PriorityLow
				callID := nextOCRBreadcrumbID()
				ocrStart := time.Now()
				log.Printf("[ocr] breadcrumb start call_id=%d source=background-segment priority=%s session=%s segment=%d t=%s bytes=%d",
					callID, ocrPriorityLabel(prio), session.ID, seg.ID, tStr, len(jpegData))

				// Legacy video-segment path: boxes are not persisted — these
				// rows key on activity_segments.id, which the path-based box
				// cache can never resolve back to a snapshot.
				result, _, err := recognizeBackgroundOCRText(w.engine, jpegData, langs, prio)
				if err != nil {
					log.Printf("[ocr] breadcrumb failed call_id=%d source=background-segment duration_ms=%d err=%v",
						callID, time.Since(ocrStart).Milliseconds(), err)
					log.Printf("[ocr] recognize failed for segment %d at t=%s: %v", seg.ID, tStr, err)
					continue
				}
				log.Printf("[ocr] breadcrumb done call_id=%d source=background-segment duration_ms=%d text_len=%d confidence=%.3f",
					callID, time.Since(ocrStart).Milliseconds(), len(result.Text), result.Confidence)

				text := ocr.NormalizeFullwidth(removeCJKSpaces(result.Text))
				if len(text) < ocrMinTextLen {
					continue
				}

				// Legacy segment-based OCR path: we sampled a frame from a
				// video segment. SegmentID is activity_segments.id; FrameTime
				// is a seconds offset INTO the segment (not session-relative).
				// See the OCRFrameRow doc comment for the full contract.
				frame := OCRFrameRow{
					SessionID:  session.ID,
					SegmentID:  seg.ID,
					FrameTime:  t,
					Text:       text,
					Confidence: result.Confidence,
				}
				if err := store.SaveOCRFrame(frame); err != nil {
					log.Printf("[ocr] failed to save frame for session %s: %v", session.ID, err)
					continue
				}
				frameCount++
			}
		}
	}

	// Mark done
	if err := store.UpdateOCRStatus(session.ID, "done"); err != nil {
		log.Printf("[ocr] failed to mark session %s as done: %v", session.ID, err)
		return
	}

	log.Printf("[ocr] session %s processed: %d frames indexed", session.ID, frameCount)

	if w.app.telemetry != nil {
		w.app.telemetry.Capture("activity.ocr.completed", map[string]any{
			"session_id":  session.ID,
			"frame_count": frameCount,
		})
	}

	// Emit event to frontend
	if w.app.ctx != nil {
		w.app.emitBrowserEvent("activity:ocr-done", map[string]any{
			"sessionId":  session.ID,
			"frameCount": frameCount,
		})
	}

	// Compute embeddings for the OCR frames (non-blocking, best-effort).
	if frameCount > 0 && store.Embedder() != nil {
		go func() {
			if err := store.EmbedOCRFrames(ctx, session.ID, w.app.activityEmbeddingGate(store)); err != nil {
				log.Printf("[ocr] embedding failed for session %s: %v", session.ID, err)
			}
		}()
	}
}

// Serialize a local OCR read and its write-back with replacement of held pixels.
func (w *ActivityOCRWorker) indexSnapshot(session ActivitySessionRow, snap ActivitySnapshotRow, langs []string) bool {
	am := w.app.activity
	am.privacyImageMu.RLock()
	defer am.privacyImageMu.RUnlock()
	store := am.store
	jpegData, err := os.ReadFile(filepath.Join(store.SnapshotsDir(), snap.FilePath))
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[ocr] read snapshot: %v", err)
		}
		return false
	}
	frame, usedFocusRect, err := recognizeSnapshotOCR(w.engine, snap, jpegData, langs, session.StartTime)
	if err != nil || frame.Text == "" {
		return false
	}
	if err := store.saveOCRFrame(frame, true); err != nil {
		log.Printf("[ocr] failed to save frame for session %s: %v", session.ID, err)
		return false
	}

	// Record that this snapshot's persisted focus rect WAS the
	// actual OCR scope. The audit UI keys the dashed rect overlay
	// on this flag so it never claims "OCR scoped to focused
	// window" for snapshots where the cropper rejected the rect.
	if usedFocusRect {
		if err := store.MarkSnapshotOCRUsedFocusRect(snap.ID); err != nil {
			log.Printf("[ocr] failed to mark ocr_used_focus_rect for snapshot %d: %v", snap.ID, err)
		}
	}

	return true
}

func recognizeSnapshotOCR(engine ocr.Engine, snap ActivitySnapshotRow, jpegData []byte, langs []string, startTime int64) (OCRFrameRow, bool, error) {
	// Crop to the focused-window rect captured alongside the JPEG so
	// OCR sees only the window the user was working in. Other apps,
	// background notifications, and adjacent on-screen content stay
	// out of the triage prompt. If no rect was recorded (legacy
	// snapshot, multi-display fallback, non-darwin) the helper
	// returns the original bytes and we OCR the full frame.
	ocrInput := jpegData
	usedFocusRect := false
	var ocrCropRect image.Rectangle
	if cropped, cropRect, cropErr := cropForOCR(
		snap, jpegData, snapshotQuality,
	); cropErr != nil {
		log.Printf("[ocr] focus-rect crop failed for snapshot %s: %v (falling back to full frame)", snap.FilePath, cropErr)
	} else if !cropRect.Empty() {
		ocrInput = cropped
		ocrCropRect = cropRect
		usedFocusRect = true
	}

	prio := ocr.PriorityLow
	callID := nextOCRBreadcrumbID()
	ocrStart := time.Now()
	log.Printf("[ocr] breadcrumb start call_id=%d source=background-snapshot priority=%s session=%s snapshot=%d file=%s bytes=%d input_bytes=%d",
		callID, ocrPriorityLabel(prio), snap.SessionID, snap.ID, filepath.Base(snap.FilePath), len(jpegData), len(ocrInput))

	result, observations, err := recognizeBackgroundOCRText(engine, ocrInput, langs, prio)
	if err != nil {
		log.Printf("[ocr] breadcrumb failed call_id=%d source=background-snapshot duration_ms=%d err=%v",
			callID, time.Since(ocrStart).Milliseconds(), err)
		log.Printf("[ocr] recognize failed for snapshot %s: %v", snap.FilePath, err)
		return OCRFrameRow{}, false, err
	}
	log.Printf("[ocr] breadcrumb done call_id=%d source=background-snapshot duration_ms=%d text_len=%d confidence=%.3f",
		callID, time.Since(ocrStart).Milliseconds(), len(result.Text), result.Confidence)

	text := ocr.NormalizeFullwidth(removeCJKSpaces(result.Text))
	if len(text) < ocrMinTextLen {
		text = ""
	}

	// Persist the boxes the engine already computed, mapped from the
	// focus-crop space back to full-frame normalized coordinates, so
	// the tag-highlight path never has to re-OCR this frame. A header
	// dims failure only degrades this frame to the on-demand path.
	boxesJSON := ""
	if usedFocusRect {
		if imgW, imgH, dimErr := fullImageDimensions(jpegData); dimErr == nil {
			boxesJSON = marshalOCRBoxes(observationsToFullFrameBoxes(observations, ocrCropRect, imgW, imgH))
		} else {
			log.Printf("[ocr] skip box persist for snapshot %s: read dims: %v", snap.FilePath, dimErr)
		}
	} else {
		boxesJSON = marshalOCRBoxes(observationsToFullFrameBoxes(observations, image.Rectangle{}, 0, 0))
	}

	// Snapshot-based OCR path: we OCR'd a JPEG snapshot directly,
	// so the row's SegmentID column actually stores activity_snapshots.id
	// (NOT activity_segments.id). FrameTime is a session-relative offset
	// in seconds. See the OCRFrameRow doc comment for the full contract
	// — consumers must handle both interpretations.
	frame := OCRFrameRow{
		SessionID:  snap.SessionID,
		SegmentID:  snap.ID,
		FrameTime:  float64(snap.Timestamp-startTime) / 1000.0,
		Text:       text,
		Confidence: result.Confidence,
		BoxesJSON:  boxesJSON,
	}
	return frame, usedFocusRect, nil
}

// recognizeBackgroundOCRText runs box-mode OCR and joins the per-line text.
// The raw observations are returned alongside so the snapshot path can
// persist their bounding boxes (boxes_json) instead of discarding them and
// forcing the tag-highlight path to re-OCR the frame at click time.
func recognizeBackgroundOCRText(engine ocr.Engine, jpegData []byte, langs []string, prio ocr.Priority) (*ocr.Result, []ocr.TextObservation, error) {
	// Keep background OCR on the same Vision path as PII/highlight OCR. The
	// text-only fast path has crashed inside macOS Vision on some 26.x systems.
	observations, err := engine.RecognizeJPEGWithBoxes(jpegData, langs, prio)
	if err != nil {
		return nil, nil, err
	}
	if len(observations) == 0 {
		return &ocr.Result{}, nil, nil
	}

	var text strings.Builder
	var totalConfidence float64
	var textCount int
	for _, obs := range observations {
		if obs.Text == "" {
			continue
		}
		if text.Len() > 0 {
			text.WriteByte('\n')
		}
		text.WriteString(obs.Text)
		totalConfidence += obs.Confidence
		textCount++
	}
	if textCount == 0 {
		return &ocr.Result{}, nil, nil
	}

	return &ocr.Result{
		Text:       text.String(),
		Confidence: totalConfidence / float64(textCount),
	}, observations, nil
}

// snapshotsWithImages leaves out rows marked image-missing: their JPEG is
// gone, so there is nothing to OCR.
func snapshotsWithImages(snapshots []ActivitySnapshotRow) []ActivitySnapshotRow {
	out := snapshots[:0:0]
	for _, snap := range snapshots {
		if snap.HasImage() {
			out = append(out, snap)
		}
	}
	return out
}

func selectSnapshotsForOCR(snapshots []ActivitySnapshotRow) []ActivitySnapshotRow {
	if len(snapshots) <= 1 {
		return snapshots
	}

	minIntervalMs := int64(ocrSnapshotFrameIntervalSec * 1000)
	selected := make([]ActivitySnapshotRow, 0, min(len(snapshots), ocrMaxSnapshotFramesPerRun))
	lastSelectedTs := int64(0)

	for i, snap := range snapshots {
		force := len(selected) == 0 || i == len(snapshots)-1 || snap.Trigger == "session_end"
		if !force && snap.Timestamp-lastSelectedTs < minIntervalMs {
			continue
		}

		selected = append(selected, snap)
		lastSelectedTs = snap.Timestamp
		if len(selected) >= ocrMaxSnapshotFramesPerRun {
			break
		}
	}

	last := snapshots[len(snapshots)-1]
	if len(selected) > 0 && selected[len(selected)-1].ID == last.ID {
		return selected
	}
	if len(selected) >= ocrMaxSnapshotFramesPerRun {
		selected[len(selected)-1] = last
		return selected
	}
	return append(selected, last)
}

// removeCJKSpaces removes spaces between consecutive CJK characters.
// Windows.Media.Ocr treats each CJK character as a separate "word" and joins
// them with spaces, producing "广 告 互 动" instead of "广告互动".
func removeCJKSpaces(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return s
	}
	out := make([]rune, 0, len(runes))
	out = append(out, runes[0])
	for i := 1; i < len(runes); i++ {
		// Skip a space that sits between two CJK characters
		if runes[i] == ' ' && i+1 < len(runes) && isCJK(runes[i-1]) && isCJK(runes[i+1]) {
			continue
		}
		out = append(out, runes[i])
	}
	return string(out)
}

// Bump the marker whenever the OCR pipeline behavior changes meaningfully
// so existing sessions get re-OCR'd. v4: removed sampling cap (was OCRing
// every Nth snapshot, now does every snapshot) and removed dedup so the
// daily summary has the full on-screen content timeline.
const ocrMigrationMarker = "ocr_v4_full_capture"

// migrateOCRIfNeeded resets all OCR data once so sessions are re-processed
// with the updated multi-language engine. A marker file prevents re-running.
func (w *ActivityOCRWorker) migrateOCRIfNeeded() {
	if w.app.activity == nil || w.app.activity.store == nil {
		return
	}
	store := w.app.activity.store
	markerPath := filepath.Join(store.SnapshotsDir(), "."+ocrMigrationMarker)
	if _, err := os.Stat(markerPath); err == nil {
		return // already migrated
	}

	count, err := store.ResetAllOCR()
	if err != nil {
		log.Printf("[ocr] migration reset failed: %v", err)
		return
	}

	os.WriteFile(markerPath, []byte("done"), 0600)

	if count > 0 {
		log.Printf("[ocr] migration: reset %d sessions for multi-language re-indexing", count)
	}
}
