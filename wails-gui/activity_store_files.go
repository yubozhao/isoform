package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/privacy"
)

// migrateFileActivity adds file_activity: the documents the user opened or
// edited lately, read from Spotlight's metadata (files_activity_sync.go). A
// file is keyed by a hash of its path; no path and no content is stored.
// dir_hashes holds a hash of every folder above the file, so a folder the
// user keeps private later finds its files without a stored path.
func migrateFileActivity(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS file_activity (
			id            TEXT    PRIMARY KEY,
			name          TEXT    NOT NULL,
			folder        TEXT    NOT NULL,
			dir_hashes    TEXT    NOT NULL DEFAULT '',
			kind          TEXT    NOT NULL DEFAULT '',
			last_used_at  INTEGER NOT NULL DEFAULT 0,
			modified_at   INTEGER NOT NULL DEFAULT 0,
			active_at     INTEGER NOT NULL,
			action        TEXT    NOT NULL,
			app_bundle_id TEXT    NOT NULL DEFAULT '',
			app_name      TEXT    NOT NULL DEFAULT '',
			first_seen_at INTEGER NOT NULL,
			updated_at    INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_file_activity_active ON file_activity(active_at);
	`)
	return err
}

// FileActivityRow is one stored document. Times are Unix milliseconds;
// ActiveAt is the later of the open and the edit that counted, and Action
// says which one it was. Name is already scrubbed.
type FileActivityRow struct {
	ID          string
	Name        string
	Folder      string
	DirHashes   string
	Kind        string
	LastUsedAt  int64
	ModifiedAt  int64
	ActiveAt    int64
	Action      string
	AppBundleID string
	AppName     string
	FirstSeenAt int64
	UpdatedAt   int64
}

// UpsertFileActivity stores documents. A stored file keeps the later of its
// stored and new times, and its app unless the new row names one. It returns
// how many rows were new or moved forward.
func (s *ActivityStore) UpsertFileActivity(rows []FileActivityRow, now int64) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin file activity: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO file_activity (id, name, folder, dir_hashes, kind, last_used_at, modified_at,
			active_at, action, app_bundle_id, app_name, first_seen_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			folder = excluded.folder,
			dir_hashes = excluded.dir_hashes,
			kind = excluded.kind,
			last_used_at = MAX(file_activity.last_used_at, excluded.last_used_at),
			modified_at = MAX(file_activity.modified_at, excluded.modified_at),
			action = CASE WHEN excluded.active_at >= file_activity.active_at
				THEN excluded.action ELSE file_activity.action END,
			app_bundle_id = CASE WHEN excluded.active_at >= file_activity.active_at AND excluded.app_bundle_id != ''
				THEN excluded.app_bundle_id ELSE file_activity.app_bundle_id END,
			app_name = CASE WHEN excluded.active_at >= file_activity.active_at AND excluded.app_bundle_id != ''
				THEN excluded.app_name ELSE file_activity.app_name END,
			active_at = MAX(file_activity.active_at, excluded.active_at),
			updated_at = excluded.updated_at
		WHERE excluded.active_at > file_activity.active_at
			OR excluded.name != file_activity.name
			OR excluded.dir_hashes != file_activity.dir_hashes`)
	if err != nil {
		return 0, fmt.Errorf("prepare file activity: %w", err)
	}
	defer stmt.Close()
	changed := 0
	for _, row := range rows {
		if strings.TrimSpace(row.ID) == "" || row.ActiveAt <= 0 {
			continue
		}
		res, err := stmt.Exec(row.ID, row.Name, row.Folder, row.DirHashes, row.Kind, row.LastUsedAt, row.ModifiedAt,
			row.ActiveAt, row.Action, row.AppBundleID, row.AppName, now, now)
		if err != nil {
			return 0, fmt.Errorf("upsert file activity: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed++
		}
	}
	return changed, tx.Commit()
}

// FileActiveAt returns the stored activity time of each of the given ids
// that is stored, so a read looks up the app only for files that moved.
func (s *ActivityStore) FileActiveAt(ids []string) (map[string]int64, error) {
	out := make(map[string]int64, len(ids))
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		chunk := ids[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT id, active_at FROM file_activity WHERE id IN (?`+
			strings.Repeat(",?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("file activity times: %w", err)
		}
		for rows.Next() {
			var id string
			var at int64
			if err := rows.Scan(&id, &at); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = at
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListFileActivity returns the documents active in [from, to), newest first.
func (s *ActivityStore) ListFileActivity(from, to int64) ([]FileActivityRow, error) {
	return s.ListFileActivityContext(context.Background(), from, to)
}

func (s *ActivityStore) ListFileActivityContext(ctx context.Context, from, to int64) ([]FileActivityRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, folder, dir_hashes, kind, last_used_at, modified_at, active_at, action,
			app_bundle_id, app_name, first_seen_at, updated_at
		FROM file_activity WHERE active_at >= ? AND active_at < ?
		ORDER BY active_at DESC, id ASC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list file activity: %w", err)
	}
	defer rows.Close()
	var out []FileActivityRow
	for rows.Next() {
		var row FileActivityRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Folder, &row.DirHashes, &row.Kind, &row.LastUsedAt,
			&row.ModifiedAt, &row.ActiveAt, &row.Action, &row.AppBundleID, &row.AppName,
			&row.FirstSeenAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// fileActivityApp is one app stored with a document.
type fileActivityApp struct {
	BundleID string
	Name     string
}

// FileActivityApps lists the distinct apps stored with a document.
func (s *ActivityStore) FileActivityApps() ([]fileActivityApp, error) {
	rows, err := s.db.Query(`SELECT DISTINCT app_bundle_id, app_name FROM file_activity WHERE app_bundle_id != '' OR app_name != ''`)
	if err != nil {
		return nil, fmt.Errorf("file activity apps: %w", err)
	}
	defer rows.Close()
	var out []fileActivityApp
	for rows.Next() {
		var app fileActivityApp
		if err := rows.Scan(&app.BundleID, &app.Name); err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

// DeleteFileActivityOfApp deletes every document stored with one app, for
// an app the user protects.
func (s *ActivityStore) DeleteFileActivityOfApp(bundleID, name string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM file_activity WHERE app_bundle_id = ? AND app_name = ?`, bundleID, name)
	if err != nil {
		return 0, fmt.Errorf("delete file activity of an app: %w", err)
	}
	return res.RowsAffected()
}

// ProtectedSessionAt reports whether a session that touched a protected app
// was running at the given time.
func (s *ActivityStore) ProtectedSessionAt(at int64) (bool, error) {
	var found int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM activity_sessions
		WHERE triage_status = 'protected' AND start_time <= ? AND end_time >= ?`, at, at).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("protected session: %w", err)
	}
	return found > 0, nil
}

// DeleteFileActivityInProtectedSessions deletes the documents worked on
// while a session that touched a protected app was running.
func (s *ActivityStore) DeleteFileActivityInProtectedSessions() (int64, error) {
	res, err := s.db.Exec(`
		DELETE FROM file_activity WHERE EXISTS (
			SELECT 1 FROM activity_sessions s
			WHERE s.triage_status = 'protected'
			  AND s.start_time <= file_activity.active_at AND s.end_time >= file_activity.active_at)`)
	if err != nil {
		return 0, fmt.Errorf("delete file activity in protected sessions: %w", err)
	}
	return res.RowsAffected()
}

// DeleteFileActivityUnder deletes every document inside the folder whose
// FolderHash is given, for a folder the user keeps private.
func (s *ActivityStore) DeleteFileActivityUnder(folderHash string) (int64, error) {
	if strings.TrimSpace(folderHash) == "" {
		return 0, nil
	}
	rows, err := s.db.Query(`SELECT id FROM file_activity WHERE instr(' ' || dir_hashes || ' ', ' ' || ? || ' ') > 0`, folderHash)
	if err != nil {
		return 0, fmt.Errorf("find file activity under a folder: %w", err)
	}
	var deletedIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("find file activity under a folder: %w", err)
		}
		deletedIDs = append(deletedIDs, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, fmt.Errorf("find file activity under a folder: %w", err)
	}
	res, err := s.db.Exec(`DELETE FROM file_activity WHERE instr(' ' || dir_hashes || ' ', ' ' || ? || ' ') > 0`, folderHash)
	if err != nil {
		return 0, fmt.Errorf("delete file activity under a folder: %w", err)
	}
	deleted, err := res.RowsAffected()
	if err == nil && deleted > 0 {
		for _, id := range deletedIDs {
			publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: "file", ID: "file:" + id})
		}
	}
	return deleted, err
}

// DeleteFileActivityIDs deletes the given documents.
func (s *ActivityStore) DeleteFileActivityIDs(ids []string) (int64, error) {
	var total int64
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		chunk := ids[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT id FROM file_activity WHERE id IN (?`+strings.Repeat(",?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return total, fmt.Errorf("find file activity for deletion: %w", err)
		}
		var deletedIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return total, fmt.Errorf("find file activity for deletion: %w", err)
			}
			deletedIDs = append(deletedIDs, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return total, fmt.Errorf("find file activity for deletion: %w", err)
		}
		res, err := s.db.Exec(`DELETE FROM file_activity WHERE id IN (?`+strings.Repeat(",?", len(chunk)-1)+`)`, args...)
		if err != nil {
			return total, fmt.Errorf("delete file activity: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n > 0 {
			for _, id := range deletedIDs {
				publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Source, SourceKind: "file", ID: "file:" + id})
			}
		}
	}
	return total, nil
}

// PruneFileActivity deletes the documents last active before the cutoff.
func (s *ActivityStore) PruneFileActivity(before int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM file_activity WHERE active_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("prune file activity: %w", err)
	}
	return res.RowsAffected()
}

// DeleteAllFileActivity empties the table, when reading is switched off.
func (s *ActivityStore) DeleteAllFileActivity() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM file_activity`)
	if err != nil {
		return 0, fmt.Errorf("clear file activity: %w", err)
	}
	return res.RowsAffected()
}

// fileWindowEventKinds are the events that carry the window in front.
var fileWindowEventKinds = []string{
	string(activitymon.EventAppSwitch),
	string(activitymon.EventFocus),
	string(activitymon.EventClick),
	string(activitymon.EventKeyboard),
}

// fileWindowMatch is the window event nearest to a document's activity
// whose title names the document.
type fileWindowMatch struct {
	BundleID    string
	AppName     string
	WindowTitle string
	// Protected is true when the event belongs to a session that touched an
	// app the user protects.
	Protected bool
}

// FileWindowNear finds the window event within [at-window, at+window] whose
// title contains stem, nearest to at first. found is false when none does.
func (s *ActivityStore) FileWindowNear(stem string, at, window int64) (fileWindowMatch, bool, error) {
	stem = strings.TrimSpace(stem)
	if stem == "" {
		return fileWindowMatch{}, false, nil
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(stem)
	args := []any{at - window, at + window}
	for _, kind := range fileWindowEventKinds {
		args = append(args, kind)
	}
	args = append(args, "%"+escaped+"%", at)
	rows, err := s.db.Query(`
		SELECT e.data, COALESCE(s.triage_status, '')
		FROM activity_events e JOIN activity_sessions s ON s.id = e.session_id
		WHERE e.timestamp >= ? AND e.timestamp <= ?
		  AND e.kind IN (?`+strings.Repeat(",?", len(fileWindowEventKinds)-1)+`)
		  AND e.data LIKE ? ESCAPE '\'
		ORDER BY ABS(e.timestamp - ?) ASC
		LIMIT 20`, args...)
	if err != nil {
		return fileWindowMatch{}, false, fmt.Errorf("file window: %w", err)
	}
	defer rows.Close()
	lower := strings.ToLower(stem)
	for rows.Next() {
		var data, triage string
		if err := rows.Scan(&data, &triage); err != nil {
			return fileWindowMatch{}, false, err
		}
		var evt activitymon.ActivityEvent
		if json.Unmarshal([]byte(data), &evt) != nil {
			continue
		}
		// The match must be in the window title, not in what was typed.
		if !strings.Contains(strings.ToLower(evt.WindowTitle), lower) {
			continue
		}
		return fileWindowMatch{
			BundleID:    evt.AppBundleID,
			AppName:     evt.AppName,
			WindowTitle: evt.WindowTitle,
			Protected:   triage == "protected",
		}, true, nil
	}
	return fileWindowMatch{}, false, rows.Err()
}
