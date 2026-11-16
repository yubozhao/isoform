package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Forget means forgotten (hub, 2026-11-15): Forget on an Activity row erases
// the raw capture behind it at once (screenshots, screen video, OCR, events
// and call audio), not at retention. Each capture session goes through the
// user deletion (deleteSession: its rows, OCR and events by cascade, the
// privacy receipt, summaries citing it, its meeting notes, and the privacy
// withdrawal), then its files are removed the way the storage rotator
// removes them.

// activitySessionCapture is the files one capture session holds.
type activitySessionCapture struct {
	snapshots []string
	segments  []string
	audio     string
}

func (s *ActivityStore) sessionCaptureFiles(sessionID string) (activitySessionCapture, error) {
	var out activitySessionCapture
	for _, q := range []struct {
		query string
		into  *[]string
	}{
		{`SELECT file_path FROM activity_snapshots WHERE session_id = ? AND COALESCE(file_path, '') != ''`, &out.snapshots},
		{`SELECT file_path FROM activity_segments WHERE session_id = ? AND COALESCE(file_path, '') != ''`, &out.segments},
	} {
		rows, err := s.db.Query(q.query, sessionID)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return out, err
			}
			*q.into = append(*q.into, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	err := s.db.QueryRow(`SELECT COALESCE(audio_path, '') FROM activity_sessions WHERE id = ?`, sessionID).Scan(&out.audio)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return out, err
}

// captureFileUnder joins a stored relative path to its folder and refuses a
// path that would leave it.
func captureFileUnder(dir, rel string) (string, bool) {
	if dir == "" || rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	path := filepath.Join(dir, rel)
	root := filepath.Clean(dir) + string(filepath.Separator)
	return path, strings.HasPrefix(path, root)
}

// eraseSessionCapture deletes one capture session as a user deletion and
// then removes its files. A file another session's row still names stays.
func (s *ActivityStore) eraseSessionCapture(sessionID string) error {
	files, err := s.sessionCaptureFiles(sessionID)
	if err != nil {
		return fmt.Errorf("read capture of session %s: %w", sessionID, err)
	}
	if err := s.DeleteSession(sessionID); err != nil {
		return fmt.Errorf("delete session %s: %w", sessionID, err)
	}
	var failed []error
	for _, rel := range files.snapshots {
		path, ok := captureFileUnder(s.SnapshotsDir(), rel)
		if !ok || s.snapshotRowExists(rel) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			failed = append(failed, err)
		}
	}
	for _, rel := range files.segments {
		path, ok := captureFileUnder(s.SegmentsDir(), rel)
		if !ok || s.segmentRowExists(rel) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			failed = append(failed, err)
		}
	}
	if _, ok := captureFileUnder(s.AudioDir(), files.audio); ok && !s.audioRowExists(files.audio) {
		if err := removeMeetingRecording(s.AudioDir(), files.audio); err != nil {
			failed = append(failed, err)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("remove capture files of session %s: %w", sessionID, errors.Join(failed...))
	}
	return nil
}

func (s *ActivityStore) segmentRowExists(relPath string) bool {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM activity_segments WHERE file_path = ?`, relPath).Scan(&n); err != nil {
		return true
	}
	return n > 0
}

func (s *ActivityStore) audioRowExists(relPath string) bool {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM activity_sessions WHERE audio_path = ?`, relPath).Scan(&n); err != nil {
		return true
	}
	return n > 0
}

// momentCaptureSessions reads the capture sessions behind a moment's runs.
func (s *ActivityStore) momentCaptureSessions(activityID string) ([]string, error) {
	return s.captureSessionsWhere(`SELECT g.session_ids FROM activity_run_links l
		JOIN activity_timeline_groups g ON g.id = l.run_id WHERE l.activity_id = ?`, activityID)
}

func (s *ActivityStore) captureSessionsWhere(query string, arg string) ([]string, error) {
	rows, err := s.db.Query(query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []string{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, fmt.Errorf("run sessions: %w", err)
		}
		for _, id := range ids {
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out, rows.Err()
}

// momentsSharingCapture names the other moments whose runs hold one of these
// sessions: forgetting the capture takes their screens too, so Forget says so.
func (s *ActivityStore) momentsSharingCapture(activityID string, sessions []string) ([]string, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	want := map[string]bool{}
	for _, id := range sessions {
		want[id] = true
	}
	rows, err := s.db.Query(`SELECT DISTINCT a.id, a.title, g.session_ids FROM activity_run_links l
		JOIN activity_timeline_groups g ON g.id = l.run_id
		JOIN activities a ON a.id = l.activity_id WHERE l.activity_id != ?`, activityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []string{}
	for rows.Next() {
		var id, title, raw string
		if err := rows.Scan(&id, &title, &raw); err != nil {
			return nil, err
		}
		var ids []string
		if json.Unmarshal([]byte(raw), &ids) != nil {
			continue
		}
		for _, sid := range ids {
			if want[sid] && !seen[id] {
				seen[id] = true
				out = append(out, strings.TrimSpace(title))
				break
			}
		}
	}
	return out, rows.Err()
}
