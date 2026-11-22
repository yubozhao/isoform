package storage

import (
	"context"
	"database/sql"
)

// GetMaxSortOrder returns the highest sort_order for a session's stream items.
func (s *Store) GetMaxSortOrder(ctx context.Context, sessionLocalID string) (int, error) {
	var maxOrder int
	err := s.committedReader().QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sort_order), -1) FROM stream_items WHERE session_local_id = ?",
		sessionLocalID).Scan(&maxOrder)
	return maxOrder, err
}

// SaveCheckpointFile inserts a checkpoint file record.
func (s *Store) SaveCheckpointFile(ctx context.Context, row CheckpointFileRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO checkpoint_files (session_local_id, sort_order, file_path, backup_path, existed, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, row.SessionLocalID, row.SortOrder, row.FilePath, row.BackupPath, row.Existed, row.CreatedAt)
	return err
}

// LoadCheckpointFilesFrom returns all checkpoint files with sort_order >= fromSortOrder.
// Results are ordered by sort_order ASC so the earliest backups come first.
func (s *Store) LoadCheckpointFilesFrom(ctx context.Context, sessionLocalID string, fromSortOrder int) ([]CheckpointFileRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, session_local_id, sort_order, file_path, backup_path, existed, created_at
		FROM checkpoint_files
		WHERE session_local_id = ? AND sort_order >= ?
		ORDER BY sort_order ASC
	`, sessionLocalID, fromSortOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CheckpointFileRow
	for rows.Next() {
		var row CheckpointFileRow
		if err := rows.Scan(&row.ID, &row.SessionLocalID, &row.SortOrder, &row.FilePath, &row.BackupPath, &row.Existed, &row.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// DeleteCheckpointFilesFrom removes checkpoint records with sort_order >= fromSortOrder.
func (s *Store) DeleteCheckpointFilesFrom(ctx context.Context, sessionLocalID string, fromSortOrder int) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM checkpoint_files WHERE session_local_id = ? AND sort_order >= ?",
		sessionLocalID, fromSortOrder)
	return err
}

// DeleteCheckpointFilesForSession deletes all checkpoint records for a session
// and returns the deleted rows so the caller can clean up disk files.
func (s *Store) DeleteCheckpointFilesForSession(ctx context.Context, sessionLocalID string) ([]CheckpointFileRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_local_id, sort_order, file_path, backup_path, existed, created_at
		FROM checkpoint_files WHERE session_local_id = ?
	`, sessionLocalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CheckpointFileRow
	for rows.Next() {
		var row CheckpointFileRow
		if err := rows.Scan(&row.ID, &row.SessionLocalID, &row.SortOrder, &row.FilePath, &row.BackupPath, &row.Existed, &row.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	_, err = s.db.ExecContext(ctx, "DELETE FROM checkpoint_files WHERE session_local_id = ?", sessionLocalID)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// LoadCheckpointSessionIDs returns distinct session_local_ids that have checkpoint_files.
func (s *Store) LoadCheckpointSessionIDs(ctx context.Context) ([]string, error) {
	rows, err := s.committedReader().QueryContext(ctx, "SELECT DISTINCT session_local_id FROM checkpoint_files")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ── Git snapshot storage ────────────────────────────────────────────────────

const gitSnapshotCols = `id, session_local_id, sort_order, stash_sha, head_sha, untracked_files, work_dir, created_at`

func scanGitSnapshot(scanner interface{ Scan(...any) error }) (*GitSnapshotRow, error) {
	var row GitSnapshotRow
	err := scanner.Scan(
		&row.ID, &row.SessionLocalID, &row.SortOrder, &row.StashSHA,
		&row.HeadSHA, &row.UntrackedFiles, &row.WorkDir, &row.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SaveGitSnapshot inserts or replaces a git snapshot record.
// Uses INSERT OR REPLACE to handle the case where a rewind re-creates a
// snapshot at the same (session_local_id, sort_order).
func (s *Store) SaveGitSnapshot(ctx context.Context, row GitSnapshotRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO git_snapshots
			(session_local_id, sort_order, stash_sha, head_sha, untracked_files, work_dir, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, row.SessionLocalID, row.SortOrder, row.StashSHA, row.HeadSHA,
		row.UntrackedFiles, row.WorkDir, row.CreatedAt)
	return err
}

// LoadGitSnapshotAt returns the git snapshot for a specific session and sort_order.
func (s *Store) LoadGitSnapshotAt(ctx context.Context, sessionLocalID string, sortOrder int) (*GitSnapshotRow, error) {
	row, err := scanGitSnapshot(s.committedReader().QueryRowContext(ctx,
		`SELECT `+gitSnapshotCols+` FROM git_snapshots WHERE session_local_id = ? AND sort_order = ?`,
		sessionLocalID, sortOrder))
	return row, err
}

// LoadGitSnapshotAtOrBefore returns the closest git snapshot at or before the
// given sort_order.
func (s *Store) LoadGitSnapshotAtOrBefore(ctx context.Context, sessionLocalID string, sortOrder int) (*GitSnapshotRow, error) {
	row, err := scanGitSnapshot(s.committedReader().QueryRowContext(ctx,
		`SELECT `+gitSnapshotCols+` FROM git_snapshots
		 WHERE session_local_id = ? AND sort_order <= ?
		 ORDER BY sort_order DESC LIMIT 1`,
		sessionLocalID, sortOrder))
	return row, err
}

// ListGitSnapshots returns all git snapshots for a session, ordered by sort_order.
func (s *Store) ListGitSnapshots(ctx context.Context, sessionLocalID string) ([]GitSnapshotRow, error) {
	rows, err := s.committedReader().QueryContext(ctx,
		`SELECT `+gitSnapshotCols+` FROM git_snapshots WHERE session_local_id = ? ORDER BY sort_order ASC`,
		sessionLocalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []GitSnapshotRow
	for rows.Next() {
		row, err := scanGitSnapshot(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *row)
	}
	return result, rows.Err()
}

// CountCheckpointFiles returns the number of checkpoint file records for a session.
func (s *Store) CountCheckpointFiles(ctx context.Context, sessionLocalID string) (int, error) {
	var count int
	err := s.committedReader().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM checkpoint_files WHERE session_local_id = ?",
		sessionLocalID).Scan(&count)
	return count, err
}

// CountStreamItems returns the count of stream items at or after a sort_order.
func (s *Store) CountStreamItems(ctx context.Context, sessionLocalID string, fromSortOrder int) (int, error) {
	var count int
	err := s.committedReader().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM stream_items WHERE session_local_id = ? AND sort_order >= ?",
		sessionLocalID, fromSortOrder).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

// DeleteGitSnapshotsFrom removes git snapshot records with sort_order >= fromSortOrder.
func (s *Store) DeleteGitSnapshotsFrom(ctx context.Context, sessionLocalID string, fromSortOrder int) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM git_snapshots WHERE session_local_id = ? AND sort_order >= ?",
		sessionLocalID, fromSortOrder)
	return err
}

// LoadGitSnapshotSHAs returns all stash_sha values for a session (for ref cleanup).
func (s *Store) LoadGitSnapshotSHAs(ctx context.Context, sessionLocalID string, fromSortOrder int) ([]string, error) {
	rows, err := s.committedReader().QueryContext(ctx,
		`SELECT stash_sha FROM git_snapshots WHERE session_local_id = ? AND sort_order >= ? AND stash_sha != ''`,
		sessionLocalID, fromSortOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shas []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		shas = append(shas, sha)
	}
	return shas, rows.Err()
}
