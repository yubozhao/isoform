package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/image/draw"
)

// Cold screenshots are kept smaller. A screenshot that reached the cold tier
// is re-encoded as a JPEG of at most snapshotCompactMaxPixels (about a laptop
// screen at standard resolution) at the capture quality. The file keeps its
// name and format, so the Activity viewer, the lightbox, OCR boxes (stored in
// normalized coordinates) and every reader that decodes JPEG work unchanged;
// only the row's size, width and height change.
//
// On the user's screenshots (Retina windows, multi-display captures) this
// made files 2.9x smaller with text still readable: a 3442x2168 Retina
// capture went from 726 KB to 168 KB at 1781x1122.
const (
	snapshotCompactMaxPixels = 2_000_000
	snapshotCompactQuality   = snapshotQuality
	// snapshotCompactSizeFactor estimates a compacted file's bytes as
	// size * (maxPixels / pixels) * factor. Measured 0.86 on real
	// screenshots; 0.9 keeps the estimate on the safe side.
	snapshotCompactSizeFactor = 0.9
)

// snapshotCompactedSize is the expected size of m once compacted (its size
// when it is not a compaction candidate).
func snapshotCompactedSize(m snapshotMedia) int64 {
	px := int64(m.Width) * int64(m.Height)
	if px <= snapshotCompactMaxPixels || m.Width <= 0 || m.Height <= 0 {
		return m.Size
	}
	return int64(float64(m.Size) * float64(snapshotCompactMaxPixels) / float64(px) * snapshotCompactSizeFactor)
}

// snapshotIsCompactable mirrors compactableSnapshotWhere for a row already
// in hand.
func snapshotIsCompactable(m snapshotMedia, coldBeforeMs int64) bool {
	if m.PrivacyHeld || m.Tier == snapshotTierMissing || m.RelPath == "" {
		return false
	}
	if m.Tier != "cold" && m.Timestamp >= coldBeforeMs {
		return false
	}
	if m.OCRStatus == "pending" || m.OCRStatus == "processing" {
		return false
	}
	if (m.TriageStatus == "pending" || m.TriageStatus == "triaging") &&
		m.SessionSavedAt >= time.Now().Add(-removableTriageWait).UnixMilli() {
		return false
	}
	return m.Width <= 0 || m.Height <= 0 || int64(m.Width)*int64(m.Height) > snapshotCompactMaxPixels
}

// snapshotProjectedSize is the size a screenshot will take once the
// compactor has been through it.
func snapshotProjectedSize(m snapshotMedia, coldBeforeMs int64) int64 {
	if snapshotIsCompactable(m, coldBeforeMs) {
		return snapshotCompactedSize(m)
	}
	return m.Size
}

// compactSnapshotJPEG scales a JPEG down to at most snapshotCompactMaxPixels
// and re-encodes it. It returns the new bytes and pixel size, and ok=false
// (with the image's real size) when the image is already small enough.
func compactSnapshotJPEG(data []byte) (out []byte, width, height int, ok bool, err error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("read jpeg header: %w", err)
	}
	px := int64(cfg.Width) * int64(cfg.Height)
	if px <= snapshotCompactMaxPixels {
		return nil, cfg.Width, cfg.Height, false, nil
	}
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, cfg.Width, cfg.Height, false, fmt.Errorf("decode jpeg: %w", err)
	}
	scale := math.Sqrt(float64(snapshotCompactMaxPixels) / float64(px))
	width = max(1, int(float64(cfg.Width)*scale))
	height = max(1, int(float64(cfg.Height)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: snapshotCompactQuality}); err != nil {
		return nil, cfg.Width, cfg.Height, false, fmt.Errorf("encode jpeg: %w", err)
	}
	return withICCProfile(buf.Bytes(), jpegICCSegments(data)), width, height, true, nil
}

// jpegICCSegments returns the source's ICC profile segments (APP2
// "ICC_PROFILE"), whole with their markers, so the shrunk copy keeps the
// colors its display space defines (Go's encoder writes none).
func jpegICCSegments(data []byte) [][]byte {
	var segs [][]byte
	for i := 2; i+4 <= len(data) && data[0] == 0xFF && data[1] == 0xD8; {
		if data[i] != 0xFF {
			break
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan, end of image
			break
		}
		n := int(data[i+2])<<8 | int(data[i+3])
		if n < 2 || i+2+n > len(data) {
			break
		}
		if seg := data[i : i+2+n]; marker == 0xE2 && bytes.HasPrefix(seg[4:], []byte("ICC_PROFILE\x00")) {
			segs = append(segs, seg)
		}
		i += 2 + n
	}
	return segs
}

// withICCProfile puts the segments right after the start-of-image marker.
func withICCProfile(jpegData []byte, segs [][]byte) []byte {
	if len(segs) == 0 || len(jpegData) < 2 {
		return jpegData
	}
	out := make([]byte, 0, len(jpegData)+len(segs)*600)
	out = append(out, jpegData[:2]...)
	for _, seg := range segs {
		out = append(out, seg...)
	}
	return append(out, jpegData[2:]...)
}

// snapshotCompactBatch is how many rows the compactor reads per query.
const snapshotCompactBatch = 50

// compactColdSnapshots shrinks compactable screenshots, newest first, until
// none are left, deadline passes or stop (when set) says so. pause is slept
// between images so a long backlog does not hold a core. Returns how many
// files were rewritten and the bytes saved.
func (r *StorageRotator) compactColdSnapshots(cfg RotatorConfig, deadline time.Time, pause time.Duration, stop func() bool, progress func(done int)) (compacted int, saved int64) {
	coldBefore := time.Now().Add(-cfg.WarmRetention).UnixMilli()
	done := 0
	more := func() bool { return time.Now().Before(deadline) && (stop == nil || !stop()) }
	for more() {
		batch, err := r.store.snapshotsToCompact(coldBefore, snapshotCompactBatch+len(r.compactSkip))
		if err != nil {
			log.Printf("[activity-rotator] list snapshots to compact: %v", err)
			return
		}
		worked := false
		for _, m := range batch {
			if r.compactSkip[m.ID] {
				continue
			}
			if !more() {
				return
			}
			worked = true
			n, ok := r.compactOneSnapshot(m)
			if ok {
				compacted++
				saved += n
			}
			done++
			if progress != nil {
				progress(done)
			}
			if pause > 0 {
				time.Sleep(pause)
			}
		}
		if !worked {
			return
		}
	}
	return
}

// compactOneSnapshot rewrites one screenshot. A row whose file is gone is
// marked image-missing; one that cannot be read is skipped for the rest of
// this process. Returns the bytes saved and whether the file was rewritten.
func (r *StorageRotator) compactOneSnapshot(m snapshotMedia) (int64, bool) {
	path := filepath.Join(r.store.SnapshotsDir(), m.RelPath)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := r.store.MarkSnapshotMissingByFilePath(m.RelPath); err != nil {
			log.Printf("[activity-rotator] mark missing screenshot %s: %v", m.RelPath, err)
			r.compactSkip[m.ID] = true
		}
		return 0, false
	}
	if err != nil {
		r.compactSkip[m.ID] = true
		return 0, false
	}
	out, w, h, ok, err := compactSnapshotJPEG(data)
	if err != nil {
		log.Printf("[activity-rotator] compact screenshot %s: %v", m.RelPath, err)
		r.compactSkip[m.ID] = true
		return 0, false
	}
	if !ok || len(out) >= len(data) {
		// Already small (or not smaller re-encoded): record the file's real
		// size and pixels so the row is not picked again. A still-large image
		// stays skipped.
		if ok {
			if cfg, err := jpeg.DecodeConfig(bytes.NewReader(data)); err == nil {
				w, h = cfg.Width, cfg.Height
			}
		}
		if err := r.store.updateSnapshotAfterCompaction(m, int64(len(data)), w, h); err != nil {
			log.Printf("[activity-rotator] record screenshot size %s: %v", m.RelPath, err)
		}
		if int64(w)*int64(h) > snapshotCompactMaxPixels {
			r.compactSkip[m.ID] = true
		}
		return 0, false
	}
	if err := writeSnapshotFile(path, out); err != nil {
		log.Printf("[activity-rotator] write compacted screenshot %s: %v", m.RelPath, err)
		r.compactSkip[m.ID] = true
		return 0, false
	}
	if err := r.store.updateSnapshotAfterCompaction(m, int64(len(out)), w, h); err != nil {
		// The file is smaller now; the row keeps its old numbers until the
		// next pass reads the file again and records them.
		log.Printf("[activity-rotator] record compacted screenshot %s: %v", m.RelPath, err)
	}
	return int64(len(data) - len(out)), true
}
