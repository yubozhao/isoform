package storage

import (
	"context"
	"time"
)

// upsertWorkspace inserts or updates a workspace using the given execer (db or tx).
func upsertWorkspace(ctx context.Context, e execer, ws WorkspaceRow) error {
	_, err := e.ExecContext(ctx, `
		INSERT INTO workspaces (id, project_id, label, is_default, worktree_path, worktree_branch, base_branch, active_session_id, sort_order, hidden, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			label = excluded.label,
			is_default = excluded.is_default,
			worktree_path = excluded.worktree_path,
			worktree_branch = excluded.worktree_branch,
			base_branch = excluded.base_branch,
			active_session_id = excluded.active_session_id,
			sort_order = excluded.sort_order,
			hidden = excluded.hidden,
			updated_at = excluded.updated_at
	`, ws.ID, ws.ProjectID, ws.Label, ws.IsDefault, ws.WorktreePath, ws.WorktreeBranch, ws.BaseBranch, ws.ActiveSessionID, ws.SortOrder, ws.Hidden, ws.CreatedAt, ws.UpdatedAt)
	return err
}

// SaveWorkspace upserts a workspace row.
func (s *Store) SaveWorkspace(ctx context.Context, ws WorkspaceRow) error {
	now := time.Now().UnixMilli()
	if ws.CreatedAt == 0 {
		ws.CreatedAt = now
	}
	ws.UpdatedAt = now

	return upsertWorkspace(ctx, s.db, ws)
}

// LoadWorkspaces returns all workspaces for a given project, ordered by sort_order then created_at.
func (s *Store) LoadWorkspaces(ctx context.Context, projectID string) ([]WorkspaceRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, project_id, label, is_default, worktree_path, worktree_branch, base_branch, active_session_id, sort_order, created_at, updated_at
		FROM workspaces WHERE project_id = ? AND hidden = 0 ORDER BY is_default DESC, sort_order ASC, created_at ASC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []WorkspaceRow
	for rows.Next() {
		var ws WorkspaceRow
		if err := rows.Scan(&ws.ID, &ws.ProjectID, &ws.Label, &ws.IsDefault, &ws.WorktreePath, &ws.WorktreeBranch, &ws.BaseBranch, &ws.ActiveSessionID, &ws.SortOrder, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, ws)
	}
	return result, rows.Err()
}

// LoadWorkspacesByProject returns all workspaces for a project, including hidden ones.
func (s *Store) LoadWorkspacesByProject(ctx context.Context, projectID string) ([]WorkspaceRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, project_id, label, is_default, hidden, worktree_path, worktree_branch, base_branch, active_session_id, sort_order, created_at, updated_at
		FROM workspaces WHERE project_id = ? ORDER BY is_default DESC, sort_order ASC, created_at ASC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []WorkspaceRow
	for rows.Next() {
		var ws WorkspaceRow
		if err := rows.Scan(&ws.ID, &ws.ProjectID, &ws.Label, &ws.IsDefault, &ws.Hidden, &ws.WorktreePath, &ws.WorktreeBranch, &ws.BaseBranch, &ws.ActiveSessionID, &ws.SortOrder, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, ws)
	}
	return result, rows.Err()
}

// LoadAllWorkspaces returns all workspaces across all projects, keyed by project ID.
func (s *Store) LoadAllWorkspaces(ctx context.Context) (map[string][]WorkspaceRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, project_id, label, is_default, worktree_path, worktree_branch, base_branch, active_session_id, sort_order, created_at, updated_at
		FROM workspaces WHERE hidden = 0 ORDER BY is_default DESC, sort_order ASC, created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]WorkspaceRow)
	for rows.Next() {
		var ws WorkspaceRow
		if err := rows.Scan(&ws.ID, &ws.ProjectID, &ws.Label, &ws.IsDefault, &ws.WorktreePath, &ws.WorktreeBranch, &ws.BaseBranch, &ws.ActiveSessionID, &ws.SortOrder, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return nil, err
		}
		result[ws.ProjectID] = append(result[ws.ProjectID], ws)
	}
	return result, rows.Err()
}

// LoadWorkspace returns a single workspace by ID.
func (s *Store) LoadWorkspace(ctx context.Context, workspaceID string) (*WorkspaceRow, error) {
	var ws WorkspaceRow
	err := s.committedReader().QueryRowContext(ctx, `
		SELECT id, project_id, label, is_default, worktree_path, worktree_branch, base_branch, active_session_id, sort_order, created_at, updated_at
		FROM workspaces WHERE id = ?
	`, workspaceID).Scan(&ws.ID, &ws.ProjectID, &ws.Label, &ws.IsDefault, &ws.WorktreePath, &ws.WorktreeBranch, &ws.BaseBranch, &ws.ActiveSessionID, &ws.SortOrder, &ws.CreatedAt, &ws.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

// DeleteWorkspace removes a workspace. Sessions are cascade-deleted via
// a cleanup query since SQLite FK constraints on workspace_id aren't enforced
// (workspace_id was added via ALTER TABLE after initial creation).
func (s *Store) DeleteWorkspace(ctx context.Context, workspaceID string) error {
	if main, err := s.holdsMainChat(ctx, s.db, "workspace_id", workspaceID); err != nil {
		return err
	} else if main {
		return ErrMainChatProtected
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// Delete checkpoint files for sessions in this workspace.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM checkpoint_files WHERE session_local_id IN (SELECT local_id FROM sessions WHERE workspace_id = ?)",
		workspaceID,
	); err != nil {
		return err
	}

	// Delete stream items for sessions in this workspace.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM stream_items WHERE session_local_id IN (SELECT local_id FROM sessions WHERE workspace_id = ?)",
		workspaceID,
	); err != nil {
		return err
	}

	// Delete sessions in this workspace.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM sessions WHERE workspace_id = ?",
		workspaceID,
	); err != nil {
		return err
	}

	// Delete the workspace itself.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM workspaces WHERE id = ?",
		workspaceID,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// UpdateWorkspaceActiveSession updates the active session for a workspace.
func (s *Store) UpdateWorkspaceActiveSession(ctx context.Context, workspaceID, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE workspaces SET active_session_id = ?, updated_at = ? WHERE id = ?",
		sessionID, time.Now().UnixMilli(), workspaceID,
	)
	return err
}

// UpdateWorkspaceSortOrders updates the sort_order for workspaces in the given ID order.
// Only updates workspaces belonging to the specified projectID.
func (s *Store) UpdateWorkspaceSortOrders(ctx context.Context, projectID string, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE workspaces SET sort_order = ?, updated_at = ? WHERE id = ? AND project_id = ?",
			i, time.Now().UnixMilli(), id, projectID,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// LoadAllSessionsByWorkspace returns all sessions keyed by workspace ID.
func (s *Store) LoadAllSessionsByWorkspace(ctx context.Context) (map[string][]SessionRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT local_id, project_id, workspace_id, session_id, label, model, prd_enabled, thinking_level, plan_mode, thinking_enabled, pinned, pinned_at, automation_result, COALESCE(meeting_id, ''), COALESCE(kind, ''), COALESCE(parent_id, ''), created_at, updated_at
		FROM sessions WHERE hidden = 0 ORDER BY pinned DESC, pinned_at ASC, updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]SessionRow)
	for rows.Next() {
		var sess SessionRow
		if err := rows.Scan(&sess.LocalID, &sess.ProjectID, &sess.WorkspaceID, &sess.SessionID, &sess.Label, &sess.Model, &sess.PrdEnabled, &sess.ThinkingLevel, &sess.PlanMode, &sess.ThinkingEnabled, &sess.Pinned, &sess.PinnedAt, &sess.AutomationResult, &sess.MeetingID, &sess.Kind, &sess.ParentID, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
			return nil, err
		}
		key := sess.WorkspaceID
		if key == "" {
			key = sess.ProjectID // fallback for unassigned sessions
		}
		result[key] = append(result[key], sess)
	}
	return result, rows.Err()
}

// EnsureProjectStructure atomically inserts a project, its workspaces, and their
// sessions only if they do not already exist (ON CONFLICT DO NOTHING). This is
// used to satisfy foreign-key constraints before inserting stream items, without
// overwriting mutable fields (like session labels) that may have been updated
// concurrently.
func (s *Store) EnsureProjectStructure(ctx context.Context, p ProjectRow, workspaces []WorkspaceRow, sessions []SessionRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UnixMilli()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	// Insert project only if it does not exist. The path column also has its
	// own UNIQUE index, so a chained ON CONFLICT(path) keeps the upsert
	// idempotent when a caller (e.g. an automation proposal fallback) races
	// to materialize a sentinel project whose path slot is already taken.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects (id, name, path, organization, product, is_yansu_project, active_session_id, active_workspace_id, sort_order, setup_command, hidden, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
		ON CONFLICT(path) DO NOTHING
	`, p.ID, p.Name, p.Path, p.Organization, p.Product, p.IsYansuProject, p.ActiveSessionID, p.ActiveWorkspaceID, p.SortOrder, p.SetupCommand, p.Hidden, p.CreatedAt, p.UpdatedAt); err != nil {
		return err
	}

	for _, ws := range workspaces {
		if ws.CreatedAt == 0 {
			ws.CreatedAt = now
		}
		ws.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workspaces (id, project_id, label, is_default, worktree_path, worktree_branch, base_branch, active_session_id, sort_order, hidden, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING
		`, ws.ID, ws.ProjectID, ws.Label, ws.IsDefault, ws.WorktreePath, ws.WorktreeBranch, ws.BaseBranch, ws.ActiveSessionID, ws.SortOrder, ws.Hidden, ws.CreatedAt, ws.UpdatedAt); err != nil {
			return err
		}
	}

	for _, sess := range sessions {
		if sess.CreatedAt == 0 {
			sess.CreatedAt = now
		}
		if sess.UpdatedAt == 0 {
			sess.UpdatedAt = now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (local_id, project_id, workspace_id, session_id, label, model, prd_enabled, thinking_level, plan_mode, thinking_enabled, pinned, pinned_at, hidden, meeting_id, kind, parent_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(local_id) DO NOTHING
		`, sess.LocalID, sess.ProjectID, sess.WorkspaceID, sess.SessionID, sess.Label, sess.Model, sess.PrdEnabled, sess.ThinkingLevel, sess.PlanMode, sess.ThinkingEnabled, sess.Pinned, sess.PinnedAt, sess.Hidden, sess.MeetingID, sess.Kind, sess.ParentID, sess.CreatedAt, sess.UpdatedAt); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SaveProjectFull atomically upserts a project, its workspaces, and their sessions.
func (s *Store) SaveProjectFull(ctx context.Context, p ProjectRow, workspaces []WorkspaceRow, sessions []SessionRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	now := time.Now().UnixMilli()
	if p.CreatedAt == 0 {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	if err := upsertProject(ctx, tx, p); err != nil {
		return err
	}

	for _, ws := range workspaces {
		if ws.CreatedAt == 0 {
			ws.CreatedAt = now
		}
		ws.UpdatedAt = now
		if err := upsertWorkspace(ctx, tx, ws); err != nil {
			return err
		}
	}

	for _, sess := range sessions {
		if sess.CreatedAt == 0 {
			sess.CreatedAt = now
		}
		if sess.UpdatedAt == 0 {
			sess.UpdatedAt = now
		}
		if err := upsertSession(ctx, tx, sess); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// UpdateProjectActiveWorkspace updates only the active_workspace_id field.
func (s *Store) UpdateProjectActiveWorkspace(ctx context.Context, projectID, workspaceID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE projects SET active_workspace_id = ?, updated_at = ? WHERE id = ?",
		workspaceID, time.Now().UnixMilli(), projectID,
	)
	return err
}

// scanWorkspaceRow is a helper to avoid repeating the scan list.
func scanWorkspaceRow(scanner interface{ Scan(...any) error }) (WorkspaceRow, error) {
	var ws WorkspaceRow
	err := scanner.Scan(&ws.ID, &ws.ProjectID, &ws.Label, &ws.IsDefault, &ws.WorktreePath, &ws.WorktreeBranch, &ws.BaseBranch, &ws.ActiveSessionID, &ws.SortOrder, &ws.CreatedAt, &ws.UpdatedAt)
	return ws, err
}

// Ensure scanWorkspaceRow is used to silence the linter.
var _ = scanWorkspaceRow
