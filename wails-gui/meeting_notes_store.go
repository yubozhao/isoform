package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A meeting's notes live with its activity, not in Knowledge. One
// meeting_notes row holds a call (a call the user rejoined keeps one row,
// keyed by the activity monitor's meeting id, or by its first activity
// session when there is none) and one meeting_note_slices row per activity
// session of that call. A slice carries what the after-call pass found in
// its transcript: a short summary, the decisions and the follow-ups. The
// pass runs before the notes are published, so its slice waits with an
// empty meeting key until recordMeetingNotes assigns it to its meeting. The
// transcript and the recording stay on the session row; the notes link to
// them. Knowledge and memory are learned from the notes (see
// meeting_notes_learning.go); the notes themselves never become a Knowledge
// entry.

func migrateMeetingNotes(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS meeting_notes (
			meeting_key TEXT PRIMARY KEY,
			meeting_id TEXT NOT NULL DEFAULT '',
			meeting_app TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			start_time INTEGER NOT NULL DEFAULT 0,
			end_time INTEGER NOT NULL DEFAULT 0,
			user_notes TEXT NOT NULL DEFAULT '',
			legacy_knowledge_id TEXT NOT NULL DEFAULT '',
			learned_hash TEXT NOT NULL DEFAULT '',
			learned_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_meeting_notes_start ON meeting_notes(start_time);
		CREATE INDEX IF NOT EXISTS idx_meeting_notes_legacy ON meeting_notes(legacy_knowledge_id) WHERE legacy_knowledge_id <> '';
		CREATE TABLE IF NOT EXISTS meeting_note_slices (
			session_id TEXT PRIMARY KEY,
			meeting_key TEXT NOT NULL,
			start_time INTEGER NOT NULL DEFAULT 0,
			end_time INTEGER NOT NULL DEFAULT 0,
			word_count INTEGER NOT NULL DEFAULT 0,
			summary TEXT NOT NULL DEFAULT '',
			decisions_json TEXT NOT NULL DEFAULT '[]',
			followups_json TEXT NOT NULL DEFAULT '[]',
			updated_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_meeting_note_slices_meeting ON meeting_note_slices(meeting_key, start_time);
	`)
	return err
}

// migrateMeetingNotesLearnedKinds adds the column that records which kinds a
// notes version was learned with. Rows from before it keep the empty value,
// which counts as both kinds learned, so nothing is relearned on upgrade.
func migrateMeetingNotesLearnedKinds(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE meeting_notes ADD COLUMN learned_kinds TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add learned_kinds: %w", err)
		}
	}
	return nil
}

// meetingNotesRecord is one meeting's notes as stored.
type meetingNotesRecord struct {
	Key               string
	MeetingID         string
	MeetingApp        string
	Title             string
	StartTime         int64
	EndTime           int64
	UserNotes         string
	LegacyKnowledgeID string
	LearnedHash       string
	LearnedKinds      string
	Slices            []meetingNoteSlice
}

// meetingNoteSlice is one activity session of a meeting.
type meetingNoteSlice struct {
	SessionID string
	StartTime int64
	EndTime   int64
	WordCount int
	Summary   string
	Decisions []string
	Followups []meetingFollowup
}

// SessionIDs lists the meeting's activity sessions, earliest first.
func (r meetingNotesRecord) SessionIDs() []string {
	ids := make([]string, 0, len(r.Slices))
	for _, s := range r.Slices {
		ids = append(ids, s.SessionID)
	}
	return ids
}

// meetingNotesHeader is what a publish knows about the meeting itself.
type meetingNotesHeader struct {
	Key        string
	MeetingID  string
	MeetingApp string
	Title      string
	StartTime  int64
	EndTime    int64
}

// saveMeetingNotesSlice records one activity session of a meeting: the
// meeting row is created or widened to cover the slice, and the slice is
// written. A slice that already has an outline keeps it; the outline is
// written by saveMeetingNotesOutline. A slice that moves to another meeting
// takes the user's notes of a meeting it leaves empty along.
func (s *ActivityStore) saveMeetingNotesSlice(h meetingNotesHeader, sessionID string, startMs, endMs int64, wordCount int) error {
	if s == nil || s.db == nil {
		return errors.New("activity store unavailable")
	}
	h.Key, sessionID = strings.TrimSpace(h.Key), strings.TrimSpace(sessionID)
	if h.Key == "" || sessionID == "" {
		return errors.New("meeting notes need a meeting key and a session")
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertMeetingNotesHeader(tx, h, startMs, endMs, now); err != nil {
		return err
	}
	var previous string
	if err := tx.QueryRow(`SELECT meeting_key FROM meeting_note_slices WHERE session_id = ?`, sessionID).Scan(&previous); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO meeting_note_slices (session_id, meeting_key, start_time, end_time, word_count, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			meeting_key = excluded.meeting_key, start_time = excluded.start_time,
			end_time = excluded.end_time, word_count = excluded.word_count,
			updated_at = excluded.updated_at`,
		sessionID, h.Key, startMs, endMs, wordCount, now); err != nil {
		return fmt.Errorf("save meeting notes slice: %w", err)
	}
	if previous != "" && previous != h.Key {
		if err := retireEmptyMeetingNotes(tx, previous, h.Key, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// retireEmptyMeetingNotes removes a meeting left without slices, moving the
// user's notes it held to the meeting its slice went to.
func retireEmptyMeetingNotes(tx *sql.Tx, key, into string, now int64) error {
	var left int
	if err := tx.QueryRow(`SELECT count(*) FROM meeting_note_slices WHERE meeting_key = ?`, key).Scan(&left); err != nil || left > 0 {
		return err
	}
	var userNotes string
	if err := tx.QueryRow(`SELECT user_notes FROM meeting_notes WHERE meeting_key = ?`, key).Scan(&userNotes); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := appendMeetingUserNotes(tx, into, userNotes, now); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM meeting_notes WHERE meeting_key = ?`, key)
	return err
}

// appendMeetingUserNotes adds text the user wrote to a meeting's notes once.
func appendMeetingUserNotes(tx *sql.Tx, key, text string, now int64) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var userNotes string
	if err := tx.QueryRow(`SELECT user_notes FROM meeting_notes WHERE meeting_key = ?`, key).Scan(&userNotes); err != nil {
		return err
	}
	if strings.Contains(userNotes, text) {
		return nil
	}
	if userNotes = strings.TrimSpace(userNotes); userNotes != "" {
		userNotes += "\n\n"
	}
	_, err := tx.Exec(`UPDATE meeting_notes SET user_notes = ?, updated_at = ? WHERE meeting_key = ?`, userNotes+text, now, key)
	return err
}

// upsertMeetingNotesHeader creates the meeting row or widens its span. The
// title, app and meeting id are filled when known and never cleared; the
// user's notes are never touched here.
func upsertMeetingNotesHeader(tx *sql.Tx, h meetingNotesHeader, startMs, endMs, now int64) error {
	if h.StartTime <= 0 || (startMs > 0 && startMs < h.StartTime) {
		h.StartTime = startMs
	}
	if endMs > h.EndTime {
		h.EndTime = endMs
	}
	_, err := tx.Exec(`
		INSERT INTO meeting_notes (meeting_key, meeting_id, meeting_app, title, start_time, end_time, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(meeting_key) DO UPDATE SET
			meeting_id = CASE WHEN excluded.meeting_id <> '' THEN excluded.meeting_id ELSE meeting_notes.meeting_id END,
			meeting_app = CASE WHEN excluded.meeting_app <> '' THEN excluded.meeting_app ELSE meeting_notes.meeting_app END,
			title = CASE WHEN meeting_notes.title = '' THEN excluded.title ELSE meeting_notes.title END,
			start_time = CASE WHEN meeting_notes.start_time <= 0 OR (excluded.start_time > 0 AND excluded.start_time < meeting_notes.start_time) THEN excluded.start_time ELSE meeting_notes.start_time END,
			end_time = MAX(meeting_notes.end_time, excluded.end_time),
			updated_at = excluded.updated_at`,
		h.Key, strings.TrimSpace(h.MeetingID), strings.TrimSpace(h.MeetingApp), strings.TrimSpace(h.Title),
		h.StartTime, h.EndTime, now, now)
	if err != nil {
		return fmt.Errorf("save meeting notes: %w", err)
	}
	return nil
}

// saveMeetingNotesOutline stores what the after-call pass found in one
// session's transcript. A session not yet published as a slice of a meeting
// gets a pending slice that recordMeetingNotes later assigns.
func (s *ActivityStore) saveMeetingNotesOutline(sessionID, title string, extraction meetingFollowupExtraction) error {
	if s == nil || s.db == nil {
		return errors.New("activity store unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("meeting notes outline needs a session")
	}
	decisions, err := json.Marshal(nonNilStrings(extraction.Decisions))
	if err != nil {
		return err
	}
	followups := extraction.Followups
	if followups == nil {
		followups = []meetingFollowup{}
	}
	followupsJSON, err := json.Marshal(followups)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO meeting_note_slices (session_id, meeting_key, summary, decisions_json, followups_json, updated_at)
		VALUES (?, '', ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			summary = excluded.summary, decisions_json = excluded.decisions_json,
			followups_json = excluded.followups_json, updated_at = excluded.updated_at`,
		sessionID, strings.TrimSpace(extraction.Summary), string(decisions), string(followupsJSON), now); err != nil {
		return err
	}
	if title = strings.TrimSpace(title); title != "" {
		if _, err := tx.Exec(`UPDATE meeting_notes SET title = ?, updated_at = ?
			WHERE title = '' AND meeting_key = (SELECT meeting_key FROM meeting_note_slices WHERE session_id = ? AND meeting_key <> '')`, title, now, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// meetingNoteSliceHasOutline reports whether the after-call pass already
// stored an outline for a session.
func (s *ActivityStore) meetingNoteSliceHasOutline(sessionID string) bool {
	if s == nil || s.db == nil {
		return false
	}
	var has int
	err := s.reader().QueryRow(`SELECT summary <> '' OR decisions_json NOT IN ('', '[]') OR followups_json NOT IN ('', '[]')
		FROM meeting_note_slices WHERE session_id = ?`, strings.TrimSpace(sessionID)).Scan(&has)
	return err == nil && has != 0
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

const meetingNotesColumns = `meeting_key, meeting_id, meeting_app, title, start_time, end_time,
	user_notes, legacy_knowledge_id, learned_hash, learned_kinds`

func scanMeetingNotesHeader(row interface{ Scan(...any) error }) (meetingNotesRecord, error) {
	var r meetingNotesRecord
	err := row.Scan(&r.Key, &r.MeetingID, &r.MeetingApp, &r.Title, &r.StartTime, &r.EndTime,
		&r.UserNotes, &r.LegacyKnowledgeID, &r.LearnedHash, &r.LearnedKinds)
	return r, err
}

// meetingNotesByKey loads one meeting's notes with every slice.
func (s *ActivityStore) meetingNotesByKey(key string) (meetingNotesRecord, bool, error) {
	if s == nil || s.db == nil {
		return meetingNotesRecord{}, false, errors.New("activity store unavailable")
	}
	r, err := scanMeetingNotesHeader(s.reader().QueryRow(`SELECT `+meetingNotesColumns+` FROM meeting_notes WHERE meeting_key = ?`, strings.TrimSpace(key)))
	if errors.Is(err, sql.ErrNoRows) {
		return meetingNotesRecord{}, false, nil
	}
	if err != nil {
		return meetingNotesRecord{}, false, err
	}
	if r.Slices, err = s.meetingNoteSlices(r.Key); err != nil {
		return meetingNotesRecord{}, false, err
	}
	return r, true, nil
}

// meetingNotesKeyForSession returns the meeting a session is a slice of.
func (s *ActivityStore) meetingNotesKeyForSession(sessionID string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, errors.New("activity store unavailable")
	}
	var key string
	err := s.reader().QueryRow(`SELECT meeting_key FROM meeting_note_slices WHERE session_id = ? AND meeting_key <> ''`, strings.TrimSpace(sessionID)).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return key, err == nil, err
}

// meetingNotesForSessions loads every meeting with a slice among the given
// sessions, earliest meeting first.
func (s *ActivityStore) meetingNotesForSessions(sessionIDs []string) ([]meetingNotesRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store unavailable")
	}
	seen := map[string]bool{}
	var keys []string
	for _, id := range sessionIDs {
		key, ok, err := s.meetingNotesKeyForSession(id)
		if err != nil {
			return nil, err
		}
		if ok && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	var out []meetingNotesRecord
	for _, key := range keys {
		r, ok, err := s.meetingNotesByKey(key)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, r)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartTime < out[j-1].StartTime; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func (s *ActivityStore) meetingNoteSlices(key string) ([]meetingNoteSlice, error) {
	rows, err := s.reader().Query(`
		SELECT session_id, start_time, end_time, word_count, summary, decisions_json, followups_json
		FROM meeting_note_slices WHERE meeting_key = ? ORDER BY start_time ASC, session_id ASC`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []meetingNoteSlice
	for rows.Next() {
		var sl meetingNoteSlice
		var decisions, followups string
		if err := rows.Scan(&sl.SessionID, &sl.StartTime, &sl.EndTime, &sl.WordCount, &sl.Summary, &decisions, &followups); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(decisions), &sl.Decisions)
		_ = json.Unmarshal([]byte(followups), &sl.Followups)
		out = append(out, sl)
	}
	return out, rows.Err()
}

// markMeetingNotesLearned records the notes version the learning pass ran
// over and the kinds it learned, so the same notes are never learned twice
// and a kind a switch kept off is learned when the switch turns on.
func (s *ActivityStore) markMeetingNotesLearned(key, hash, kinds string) error {
	if s == nil || s.db == nil {
		return errors.New("activity store unavailable")
	}
	_, err := s.db.Exec(`UPDATE meeting_notes SET learned_hash = ?, learned_kinds = ?, learned_at = ? WHERE meeting_key = ?`, hash, kinds, time.Now().UnixMilli(), key)
	return err
}

// deleteMeetingNotes removes a meeting's notes and every slice.
func (s *ActivityStore) deleteMeetingNotes(key string) error {
	if s == nil || s.db == nil {
		return errors.New("activity store unavailable")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM meeting_note_slices WHERE meeting_key = ?`, key); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM meeting_notes WHERE meeting_key = ?`, key); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteMeetingNoteSlicesOfSession runs inside an explicit session delete:
// the session's slice goes, and a meeting left without slices goes with it.
// Retention does not call it; notes outlive raw capture like summaries do.
func deleteMeetingNoteSlicesOfSession(tx *sql.Tx, sessionID string) error {
	var key string
	err := tx.QueryRow(`SELECT meeting_key FROM meeting_note_slices WHERE session_id = ?`, sessionID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM meeting_note_slices WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM meeting_notes WHERE meeting_key = ? AND NOT EXISTS (SELECT 1 FROM meeting_note_slices WHERE meeting_key = ?)`, key, key)
	return err
}

// recentMeetingNotes lists the meetings that ended since sinceMs.
func (s *ActivityStore) recentMeetingNotes(sinceMs int64) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store unavailable")
	}
	rows, err := s.reader().Query(`SELECT meeting_key FROM meeting_notes WHERE end_time >= ? ORDER BY start_time ASC`, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
