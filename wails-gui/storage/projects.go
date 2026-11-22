package storage

import (
	"context"
	"database/sql"
	"time"
)

// execer abstracts *sql.DB and *sql.Tx so upsert helpers work in both contexts.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// upsertProject inserts or updates a project using the given execer (db or tx).
// If a different project already occupies the same path, it is removed first
// so the new project can take ownership (e.g. re-cloning after manual deletion).
func upsertProject(ctx context.Context, e execer, p ProjectRow) error {
	// Remove any stale project that occupies the same path under a different ID.
	if _, err := e.ExecContext(ctx,
		`DELETE FROM projects WHERE path = ? AND id != ?`, p.Path, p.ID,
	); err != nil {
		return err
	}

	_, err := e.ExecContext(ctx, `
		INSERT INTO projects (id, name, path, organization, product, is_yansu_project, active_session_id, active_workspace_id, sort_order, setup_command, hidden, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			path = excluded.path,
			organization = excluded.organization,
			product = excluded.product,
			is_yansu_project = excluded.is_yansu_project,
			active_session_id = excluded.active_session_id,
			active_workspace_id = excluded.active_workspace_id,
			sort_order = excluded.sort_order,
			setup_command = excluded.setup_command,
			hidden = excluded.hidden,
			updated_at = excluded.updated_at
	`, p.ID, p.Name, p.Path, p.Organization, p.Product, p.IsYansuProject, p.ActiveSessionID, p.ActiveWorkspaceID, p.SortOrder, p.SetupCommand, p.Hidden, p.CreatedAt, p.UpdatedAt)
	return err
}

// upsertSession inserts or updates a session using the given execer (db or tx).
func upsertSession(ctx context.Context, e execer, sess SessionRow) error {
	_, err := e.ExecContext(ctx, `
		INSERT INTO sessions (local_id, project_id, workspace_id, session_id, label, model, prd_enabled, thinking_level, plan_mode, thinking_enabled, pinned, pinned_at, automation_result, hidden, meeting_id, kind, parent_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(local_id) DO UPDATE SET
			session_id = excluded.session_id,
			label = excluded.label,
			model = excluded.model,
			prd_enabled = excluded.prd_enabled,
			thinking_level = excluded.thinking_level,
			plan_mode = excluded.plan_mode,
			thinking_enabled = excluded.thinking_enabled,
			pinned = excluded.pinned,
			pinned_at = excluded.pinned_at,
			automation_result = excluded.automation_result,
			workspace_id = excluded.workspace_id,
			hidden = excluded.hidden,
			meeting_id = CASE WHEN excluded.meeting_id = '' THEN sessions.meeting_id ELSE excluded.meeting_id END,
			kind = CASE WHEN excluded.kind = '' THEN sessions.kind ELSE excluded.kind END,
			parent_id = CASE WHEN excluded.parent_id = '' THEN sessions.parent_id ELSE excluded.parent_id END,
			updated_at = excluded.updated_at
	`, sess.LocalID, sess.ProjectID, sess.WorkspaceID, sess.SessionID, sess.Label, sess.Model, sess.PrdEnabled, sess.ThinkingLevel, sess.PlanMode, sess.ThinkingEnabled, sess.Pinned, sess.PinnedAt, sess.AutomationResult, sess.Hidden, sess.MeetingID, sess.Kind, sess.ParentID, sess.CreatedAt, sess.UpdatedAt)
	return err
}

// SaveProject upserts a project row.
func (s *Store) SaveProject(ctx context.Context, p ProjectRow) error {
	now := time.Now().UnixMilli()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	return upsertProject(ctx, s.db, p)
}

// LoadProjects returns all saved projects ordered by sort_order (then creation time as tiebreaker).
func (s *Store) LoadProjects(ctx context.Context) ([]ProjectRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, name, path, organization, product, is_yansu_project, active_session_id, active_workspace_id, sort_order, setup_command, created_at, updated_at
		FROM projects WHERE hidden = 0 ORDER BY sort_order ASC, created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ProjectRow
	for rows.Next() {
		var p ProjectRow
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &p.Organization, &p.Product, &p.IsYansuProject, &p.ActiveSessionID, &p.ActiveWorkspaceID, &p.SortOrder, &p.SetupCommand, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// SaveProjectWithSessions atomically upserts a project and all its sessions in one transaction.
func (s *Store) SaveProjectWithSessions(ctx context.Context, p ProjectRow, sessions []SessionRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	if err := upsertProject(ctx, tx, p); err != nil {
		return err
	}

	for _, sess := range sessions {
		if sess.CreatedAt == 0 {
			sess.CreatedAt = now
		}
		sess.UpdatedAt = now
		if err := upsertSession(ctx, tx, sess); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// DeleteProject removes a project and its sessions/items via cascade.
func (s *Store) DeleteProject(ctx context.Context, id string) error {
	if main, err := s.holdsMainChat(ctx, s.db, "project_id", id); err != nil {
		return err
	} else if main {
		return ErrMainChatProtected
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", id)
	return err
}

// UpdateProjectActiveSession updates only the active_session_id field.
func (s *Store) UpdateProjectActiveSession(ctx context.Context, projectID, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE projects SET active_session_id = ?, updated_at = ? WHERE id = ?",
		sessionID, time.Now().UnixMilli(), projectID,
	)
	return err
}

// UpdateProjectSetupCommand updates only the setup_command field for a project.
func (s *Store) UpdateProjectSetupCommand(ctx context.Context, projectID, command string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE projects SET setup_command = ?, updated_at = ? WHERE id = ?",
		command, time.Now().UnixMilli(), projectID,
	)
	return err
}

// GetProjectSetupCommand returns the setup_command for a project.
func (s *Store) GetProjectSetupCommand(ctx context.Context, projectID string) (string, error) {
	var cmd string
	err := s.committedReader().QueryRowContext(ctx,
		"SELECT setup_command FROM projects WHERE id = ?", projectID,
	).Scan(&cmd)
	return cmd, err
}

// UpdateProjectSortOrders sets the sort_order of each project according to its
// position in the given ids slice. This is called after a drag-and-drop reorder.
func (s *Store) UpdateProjectSortOrders(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE projects SET sort_order = ?, updated_at = ? WHERE id = ?",
			i, now, id,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// LoadProjectByPath returns a single project matching the given path,
// including hidden projects. Returns sql.ErrNoRows if not found.
func (s *Store) LoadProjectByPath(ctx context.Context, path string) (ProjectRow, error) {
	var p ProjectRow
	err := s.committedReader().QueryRowContext(ctx, `
		SELECT id, name, path, organization, product, is_yansu_project, active_session_id, active_workspace_id, sort_order, setup_command, hidden, created_at, updated_at
		FROM projects WHERE path = ?
	`, path).Scan(&p.ID, &p.Name, &p.Path, &p.Organization, &p.Product, &p.IsYansuProject, &p.ActiveSessionID, &p.ActiveWorkspaceID, &p.SortOrder, &p.SetupCommand, &p.Hidden, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// NextProjectSortOrder returns the next available sort_order value
// (one greater than the current maximum).
func (s *Store) NextProjectSortOrder(ctx context.Context) (int, error) {
	var maxOrder int
	err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sort_order), -1) FROM projects",
	).Scan(&maxOrder)
	if err != nil {
		return 0, err
	}
	return maxOrder + 1, nil
}
