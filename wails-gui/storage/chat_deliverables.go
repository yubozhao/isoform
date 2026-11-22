package storage

import (
	"context"
	"database/sql"
	"time"
)

// SaveChatDeliverable upserts a chat deliverable record keyed on its content
// hash ID (session + sorted paths), so re-recording the same file set for a
// session is idempotent rather than inflating the table.
func (s *Store) SaveChatDeliverable(ctx context.Context, row ChatDeliverableRow) error {
	now := time.Now().UnixMilli()
	if row.CreatedAt == 0 {
		row.CreatedAt = now
	}
	row.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO chat_deliverables (id, session_id, title, summary, files,
			work_dir, session_temp_dir, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id       = excluded.session_id,
			title            = excluded.title,
			summary          = excluded.summary,
			files            = excluded.files,
			work_dir         = excluded.work_dir,
			session_temp_dir = excluded.session_temp_dir,
			updated_at       = excluded.updated_at
	`, row.ID, row.SessionID, row.Title, row.Summary, row.Files,
		row.WorkDir, row.SessionTempDir, row.CreatedAt, row.UpdatedAt)
	return err
}

// ListChatDeliverables returns chat deliverables, newest first.
func (s *Store) ListChatDeliverables(ctx context.Context, limit int) ([]ChatDeliverableRow, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, session_id, title, summary, files, work_dir, session_temp_dir, created_at, updated_at
		FROM chat_deliverables
		ORDER BY updated_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ChatDeliverableRow
	for rows.Next() {
		var r ChatDeliverableRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Title, &r.Summary, &r.Files,
			&r.WorkDir, &r.SessionTempDir, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// GetChatDeliverable returns a single chat deliverable by ID. Returns
// (nil, nil) when no row matches so callers can treat "not found" distinctly
// from a real query error.
func (s *Store) GetChatDeliverable(ctx context.Context, id string) (*ChatDeliverableRow, error) {
	var r ChatDeliverableRow
	err := s.committedReader().QueryRowContext(ctx, `
		SELECT id, session_id, title, summary, files, work_dir, session_temp_dir, created_at, updated_at
		FROM chat_deliverables WHERE id = ?
	`, id).Scan(&r.ID, &r.SessionID, &r.Title, &r.Summary, &r.Files,
		&r.WorkDir, &r.SessionTempDir, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
