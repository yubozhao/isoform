package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Store queries behind the storage budget: which screenshots and call
// recordings exist, which can be made smaller, and how to take an image or a
// recording away while the row and its text stay.

// snapshotMedia is one screenshot file the storage budget can shrink or remove.
type snapshotMedia struct {
	ID          int64
	SessionID   string
	Timestamp   int64
	RelPath     string
	Size        int64
	Width       int
	Height      int
	PrivacyHeld bool
	Tier        string
	OCRStatus   string
	// TriageStatus and SessionSavedAt are the session's triage state and
	// save time (created_at).
	TriageStatus   string
	SessionSavedAt int64
}

// audioMedia is one saved call recording.
type audioMedia struct {
	SessionID string
	StartTime int64
	// SavedAt is when the session row was written (created_at), which for a
	// long call is much later than StartTime.
	SavedAt     int64
	RelPath     string
	Size        int64
	Transcribed bool
}

// snapshotMediaColumns selects a snapshotMedia from activity_snapshots sn
// joined (LEFT) to activity_sessions s.
const snapshotMediaColumns = `sn.id, COALESCE(sn.session_id, ''), sn.timestamp, sn.file_path,
	COALESCE(sn.size_bytes, 0), COALESCE(sn.width, 0), COALESCE(sn.height, 0),
	sn.storage_tier, sn.privacy_held, COALESCE(s.ocr_status, ''), COALESCE(s.triage_status, ''), COALESCE(s.created_at, 0)`

func scanSnapshotMedia(rows *sql.Rows) ([]snapshotMedia, error) {
	defer rows.Close()
	var out []snapshotMedia
	for rows.Next() {
		var m snapshotMedia
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Timestamp, &m.RelPath,
			&m.Size, &m.Width, &m.Height, &m.Tier, &m.PrivacyHeld, &m.OCRStatus, &m.TriageStatus, &m.SessionSavedAt); err != nil {
			return nil, fmt.Errorf("scan snapshot media: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// snapshotsWithImageBefore returns screenshot rows that still have an image
// and were taken before beforeMs, oldest first, starting after the
// (afterTs, afterID) cursor.
func (s *ActivityStore) snapshotsWithImageBefore(beforeMs, afterTs, afterID int64, limit int) ([]snapshotMedia, error) {
	rows, err := s.db.Query(`
		SELECT `+snapshotMediaColumns+`
		FROM activity_snapshots sn
		LEFT JOIN activity_sessions s ON s.id = sn.session_id
		WHERE sn.storage_tier != ? AND sn.file_path != '' AND sn.timestamp < ?
			AND (sn.timestamp > ? OR (sn.timestamp = ? AND sn.id > ?))
		ORDER BY sn.timestamp, sn.id
		LIMIT ?`, snapshotTierMissing, beforeMs, afterTs, afterTs, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("query snapshots with image: %w", err)
	}
	return scanSnapshotMedia(rows)
}

// removableTriageWait bounds how long a session waiting for triage (which
// reads its images, and has vision describe weak ones) keeps them from the
// limits, so a stuck session cannot pin its images forever.
const removableTriageWait = 7 * 24 * time.Hour

// snapshotsRemovableBefore returns, like snapshotsWithImageBefore, the
// screenshots the storage limit and the screenshot time limit may take the
// image of: taken before beforeMs, in a session saved before beforeMs too (a
// long call saved just now keeps its frames for another day), not waiting
// for OCR, and not waiting for triage (for up to removableTriageWait), both
// of which still have to read the image.
func (s *ActivityStore) snapshotsRemovableBefore(beforeMs, afterTs, afterID int64, limit int) ([]snapshotMedia, error) {
	triageSince := time.Now().Add(-removableTriageWait).UnixMilli()
	rows, err := s.db.Query(`
		SELECT `+snapshotMediaColumns+`
		FROM activity_snapshots sn
		LEFT JOIN activity_sessions s ON s.id = sn.session_id
		WHERE sn.storage_tier != ? AND sn.file_path != '' AND sn.timestamp < ?
			AND COALESCE(s.created_at, 0) < ?
			AND COALESCE(s.ocr_status, '') NOT IN ('pending', 'processing')
			AND NOT (COALESCE(s.triage_status, '') IN ('pending', 'triaging') AND COALESCE(s.created_at, 0) >= ?)
			AND (sn.timestamp > ? OR (sn.timestamp = ? AND sn.id > ?))
		ORDER BY sn.timestamp, sn.id
		LIMIT ?`, snapshotTierMissing, beforeMs, beforeMs, triageSince, afterTs, afterTs, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("query removable snapshots: %w", err)
	}
	return scanSnapshotMedia(rows)
}

// snapshotMediaByIDs returns the screenshot rows with the given ids.
func (s *ActivityStore) snapshotMediaByIDs(ids []int64) ([]snapshotMedia, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.Query(`
		SELECT `+snapshotMediaColumns+`
		FROM activity_snapshots sn
		LEFT JOIN activity_sessions s ON s.id = sn.session_id
		WHERE sn.id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query snapshots by id: %w", err)
	}
	return scanSnapshotMedia(rows)
}

// compactableSnapshotWhere picks screenshots the compactor may shrink: the
// image is there, the row is cold (or old enough to become cold on the next
// tier pass), it is larger than snapshotCompactMaxPixels or its size is not
// recorded, and its session is not waiting for OCR or (for up to
// removableTriageWait, 7 days) for triage, both of which read the
// full-resolution image. Arguments: coldBeforeMs, snapshotCompactMaxPixels.
const compactableSnapshotWhere = `sn.privacy_held=0 AND sn.storage_tier != 'missing' AND sn.file_path != ''
	AND (sn.storage_tier = 'cold' OR sn.timestamp < ?)
	AND (COALESCE(sn.width, 0) <= 0 OR COALESCE(sn.height, 0) <= 0 OR sn.width * sn.height > ?)
	AND COALESCE(s.ocr_status, '') NOT IN ('pending', 'processing')
	AND NOT (COALESCE(s.triage_status, '') IN ('pending', 'triaging')
		AND COALESCE(s.created_at, 0) >= (CAST(strftime('%s', 'now') AS INTEGER) - 604800) * 1000)`

// snapshotsToCompact returns up to limit compactable screenshots, newest
// first: the oldest are the first the budget removes, so shrinking them first
// would be wasted work.
func (s *ActivityStore) snapshotsToCompact(coldBeforeMs int64, limit int) ([]snapshotMedia, error) {
	rows, err := s.db.Query(`
		SELECT `+snapshotMediaColumns+`
		FROM activity_snapshots sn
		LEFT JOIN activity_sessions s ON s.id = sn.session_id
		WHERE `+compactableSnapshotWhere+`
		ORDER BY sn.timestamp DESC, sn.id DESC
		LIMIT ?`, coldBeforeMs, snapshotCompactMaxPixels, limit)
	if err != nil {
		return nil, fmt.Errorf("query snapshots to compact: %w", err)
	}
	return scanSnapshotMedia(rows)
}

// compactableSnapshotTotals returns how many screenshots are waiting to be
// shrunk, their bytes now, and their expected bytes afterwards
// (snapshotCompactedSize per row).
func (s *ActivityStore) compactableSnapshotTotals(coldBeforeMs int64) (count int, before, after int64, err error) {
	var b, a sql.NullFloat64
	err = s.db.QueryRow(`
		SELECT COUNT(*), SUM(COALESCE(sn.size_bytes, 0)),
			SUM(CASE WHEN COALESCE(sn.width, 0) > 0 AND COALESCE(sn.height, 0) > 0
				THEN COALESCE(sn.size_bytes, 0) * MIN(1.0, ? * 1.0 / (sn.width * sn.height)) * ?
				ELSE COALESCE(sn.size_bytes, 0) END)
		FROM activity_snapshots sn
		LEFT JOIN activity_sessions s ON s.id = sn.session_id
		WHERE `+compactableSnapshotWhere,
		snapshotCompactMaxPixels, snapshotCompactSizeFactor, coldBeforeMs, snapshotCompactMaxPixels).
		Scan(&count, &b, &a)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("sum compactable snapshots: %w", err)
	}
	return count, int64(b.Float64), int64(a.Float64), nil
}

// updateSnapshotAfterCompaction records a screenshot's new size and pixel
// dimensions and moves its session's total by the size difference.
func (s *ActivityStore) updateSnapshotAfterCompaction(m snapshotMedia, newSize int64, width, height int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE activity_snapshots SET size_bytes = ?, width = ?, height = ?
		WHERE id = ? AND storage_tier != ?`, newSize, width, height, m.ID, snapshotTierMissing)
	if err != nil {
		return fmt.Errorf("update snapshot %d: %w", m.ID, err)
	}
	if n, _ := res.RowsAffected(); n > 0 && m.SessionID != "" && newSize != m.Size {
		if _, err := tx.Exec(`UPDATE activity_sessions SET total_size_bytes = MAX(total_size_bytes + ?, 0) WHERE id = ?`,
			newSize-m.Size, m.SessionID); err != nil {
			return fmt.Errorf("update session %s size: %w", m.SessionID, err)
		}
	}
	return tx.Commit()
}

// stripSnapshotImages marks screenshot rows image-missing (tier "missing",
// size 0) and recounts their sessions over rows with a file, exactly as the
// startup reconcile does for a lost file. Ordinary rows keep their AX text, parsed
// content and OCR. Held rows and their OCR must be erased together. The caller removes the files after this commits, so a
// failure here leaves both row and file as they were.
func (s *ActivityStore) stripSnapshotImages(rows []snapshotMedia) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	freed := make(map[string]int64)
	for _, m := range rows {
		// A staged row may have acquired an owner, or a release may have
		// rewritten the image since the budget selected it.
		err := tx.QueryRow(`SELECT COALESCE(session_id,''),COALESCE(size_bytes,0),privacy_held FROM activity_snapshots WHERE id=? AND file_path=? AND storage_tier!=?`, m.ID, m.RelPath, snapshotTierMissing).Scan(&m.SessionID, &m.Size, &m.PrivacyHeld)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		if m.PrivacyHeld {
			removed, err := deleteHeldSnapshot(tx, m.ID, m.SessionID, m.RelPath)
			if err != nil {
				return err
			}
			if removed {
				continue
			}
		}
		res, err := tx.Exec(`UPDATE activity_snapshots SET storage_tier = ?, size_bytes = 0
			WHERE id = ? AND storage_tier != ?`, snapshotTierMissing, m.ID, snapshotTierMissing)
		if err != nil {
			return fmt.Errorf("strip snapshot %d: %w", m.ID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 && m.SessionID != "" {
			freed[m.SessionID] += m.Size
		}
	}
	for sessionID, size := range freed {
		if _, err := tx.Exec(`
			UPDATE activity_sessions
			SET snapshot_count = (SELECT COUNT(*) FROM activity_snapshots WHERE session_id = ? AND storage_tier != ?),
				total_size_bytes = MAX(total_size_bytes - ?, 0)
			WHERE id = ?`, sessionID, snapshotTierMissing, size, sessionID); err != nil {
			return fmt.Errorf("recount session %s: %w", sessionID, err)
		}
	}
	return tx.Commit()
}

// sessionsWithAudio returns every session with a saved call recording that
// started before beforeMs, oldest first.
func (s *ActivityStore) sessionsWithAudio(beforeMs int64) ([]audioMedia, error) {
	rows, err := s.db.Query(`
		SELECT id, start_time, COALESCE(created_at, 0), audio_path, COALESCE(audio_size_bytes, 0),
			COALESCE(transcript_path, '') != ''
		FROM activity_sessions
		WHERE COALESCE(audio_path, '') != '' AND start_time < ?
		ORDER BY start_time, id`, beforeMs)
	if err != nil {
		return nil, fmt.Errorf("query sessions with audio: %w", err)
	}
	defer rows.Close()
	var out []audioMedia
	for rows.Next() {
		var m audioMedia
		if err := rows.Scan(&m.SessionID, &m.StartTime, &m.SavedAt, &m.RelPath, &m.Size, &m.Transcribed); err != nil {
			return nil, fmt.Errorf("scan session audio: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// referencedAudioPaths returns the cleaned audio paths that session rows
// point at.
func (s *ActivityStore) referencedAudioPaths() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT audio_path FROM activity_sessions WHERE COALESCE(audio_path, '') != ''`)
	if err != nil {
		return nil, fmt.Errorf("query audio paths: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan audio path: %w", err)
		}
		out[filepath.Clean(p)] = true
	}
	return out, rows.Err()
}

// referencedSnapshotPaths returns the cleaned screenshot paths rows point at.
func (s *ActivityStore) referencedSnapshotPaths() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT file_path FROM activity_snapshots WHERE file_path != ''`)
	if err != nil {
		return nil, fmt.Errorf("query snapshot paths: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan snapshot path: %w", err)
		}
		out[filepath.Clean(p)] = true
	}
	return out, rows.Err()
}

// replaceSessionAudio points a session at its compressed recording. It only
// changes a row that still names oldRel, so a recording the call pipeline
// replaced or removed in the meantime is left alone. Reports whether the row
// changed.
func (s *ActivityStore) replaceSessionAudio(sessionID, oldRel, newRel string, newSize int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE activity_sessions SET audio_path = ?, audio_size_bytes = ?
		WHERE id = ? AND audio_path = ?`, newRel, newSize, sessionID, oldRel)
	if err != nil {
		return false, fmt.Errorf("replace session audio: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// stripSessionAudio clears a session's recording (the transcript and the
// rest of the row stay). Like replaceSessionAudio it only changes a row that
// still names relPath.
func (s *ActivityStore) stripSessionAudio(sessionID, relPath string) (bool, error) {
	return s.replaceSessionAudio(sessionID, relPath, "", 0)
}

// databaseFiles returns the paths of the activity database and its WAL and
// shared-memory files.
func (s *ActivityStore) databaseFiles() []string {
	db := filepath.Join(s.baseDir(), "activity.db")
	return []string{db, db + "-wal", db + "-shm"}
}

// baseDir is the data folder the store was opened in (~/.yansu-agent or the
// flavor's folder).
func (s *ActivityStore) baseDir() string {
	return filepath.Dir(filepath.Dir(s.snapshotsDir))
}

// databaseFreeBytes returns the bytes of unused pages inside the database
// file (what VACUUM gives back) and the file's page bytes.
func (s *ActivityStore) databaseFreeBytes() (free, total int64, err error) {
	var pageSize, pageCount, freeCount int64
	if err := s.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		return 0, 0, fmt.Errorf("page_size: %w", err)
	}
	if err := s.db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		return 0, 0, fmt.Errorf("page_count: %w", err)
	}
	if err := s.db.QueryRow(`PRAGMA freelist_count`).Scan(&freeCount); err != nil {
		return 0, 0, fmt.Errorf("freelist_count: %w", err)
	}
	return freeCount * pageSize, pageCount * pageSize, nil
}

// retiredTablesPresent returns the tables of removed features that are still
// in the database (the drop migration did not run or failed) and the bytes
// their pages take. Bytes are 0 when the page statistics are unavailable.
func (s *ActivityStore) retiredTablesPresent() ([]string, int64, error) {
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return nil, 0, fmt.Errorf("list tables: %w", err)
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("scan table: %w", err)
		}
		existing[name] = true
	}
	rows.Close()
	var present []string
	for _, t := range removedAgencyTables {
		if existing[t] {
			present = append(present, t)
		}
	}
	if len(present) == 0 {
		return nil, 0, nil
	}
	var total int64
	for _, t := range present {
		// One table (and its indexes) at a time: dbstat filters on a single
		// name without scanning the whole file.
		var bytes sql.NullInt64
		if err := s.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE name = ?`, t).Scan(&bytes); err != nil {
			return present, 0, nil
		}
		total += bytes.Int64
		idx, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ?`, t)
		if err != nil {
			continue
		}
		var names []string
		for idx.Next() {
			var n string
			if idx.Scan(&n) == nil {
				names = append(names, n)
			}
		}
		idx.Close()
		for _, n := range names {
			var ib sql.NullInt64
			if s.db.QueryRow(`SELECT SUM(pgsize) FROM dbstat WHERE name = ?`, n).Scan(&ib) == nil {
				total += ib.Int64
			}
		}
	}
	return present, total, nil
}

// dropRetiredTables drops the tables of removed features that are still in
// the database. Their pages go to the free list until the next VACUUM.
func (s *ActivityStore) dropRetiredTables() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := migrateDropRemovedAgencyTables(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetInterruptedOCR puts sessions an interrupted OCR run left at
// "processing" back to "pending". Returns how many.
func (s *ActivityStore) ResetInterruptedOCR() (int64, error) {
	res, err := s.db.Exec(`UPDATE activity_sessions SET ocr_status = 'pending' WHERE ocr_status = 'processing'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
