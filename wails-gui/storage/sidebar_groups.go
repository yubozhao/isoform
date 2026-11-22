package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CreateSidebarGroup inserts a new sidebar group with the next available sort order.
func (s *Store) CreateSidebarGroup(ctx context.Context, name string) (SidebarGroupRow, error) {
	now := time.Now().UnixMilli()
	id := uuid.New().String()

	var maxOrder int
	err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sort_order), -1) FROM sidebar_groups",
	).Scan(&maxOrder)
	if err != nil {
		return SidebarGroupRow{}, fmt.Errorf("next sort order: %w", err)
	}

	row := SidebarGroupRow{
		ID:        id,
		Name:      name,
		SortOrder: maxOrder + 1,
		Collapsed: false,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sidebar_groups (id, name, sort_order, collapsed, created_at, updated_at)
		VALUES (?, ?, ?, 0, ?, ?)`,
		row.ID, row.Name, row.SortOrder, row.CreatedAt, row.UpdatedAt,
	); err != nil {
		return SidebarGroupRow{}, fmt.Errorf("insert sidebar group: %w", err)
	}

	return row, nil
}

// RenameSidebarGroup updates the name of an existing sidebar group.
func (s *Store) RenameSidebarGroup(ctx context.Context, id, name string) error {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx,
		"UPDATE sidebar_groups SET name = ?, updated_at = ? WHERE id = ?",
		name, now, id,
	)
	return err
}

// DeleteSidebarGroup removes a sidebar group and all its items.
func (s *Store) DeleteSidebarGroup(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM sidebar_group_items WHERE group_id = ?", id,
	); err != nil {
		return fmt.Errorf("delete group items: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM sidebar_groups WHERE id = ?", id,
	); err != nil {
		return fmt.Errorf("delete group: %w", err)
	}

	return tx.Commit()
}

// ReorderSidebarGroups updates the sort_order of groups based on the given ID order.
func (s *Store) ReorderSidebarGroups(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE sidebar_groups SET sort_order = ? WHERE id = ?",
			i, id,
		); err != nil {
			return fmt.Errorf("update sort order for %s: %w", id, err)
		}
	}

	return tx.Commit()
}

// SetSidebarGroupCollapsed updates the collapsed state of a sidebar group.
func (s *Store) SetSidebarGroupCollapsed(ctx context.Context, id string, collapsed bool) error {
	now := time.Now().UnixMilli()
	collapsedInt := 0
	if collapsed {
		collapsedInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE sidebar_groups SET collapsed = ?, updated_at = ? WHERE id = ?",
		collapsedInt, now, id,
	)
	return err
}

// AddItemToSidebarGroup adds an item reference to a sidebar group.
// Returns the created item row. If the same ref already exists in the group, it is a no-op
// and the existing row is returned.
func (s *Store) AddItemToSidebarGroup(ctx context.Context, groupID, itemType, refID string) (SidebarGroupItemRow, error) {
	// Check for existing duplicate.
	var existing SidebarGroupItemRow
	err := s.db.QueryRowContext(ctx, `
		SELECT id, group_id, item_type, ref_id, sort_order, added_at
		FROM sidebar_group_items
		WHERE group_id = ? AND item_type = ? AND ref_id = ?`,
		groupID, itemType, refID,
	).Scan(&existing.ID, &existing.GroupID, &existing.ItemType, &existing.RefID, &existing.SortOrder, &existing.AddedAt)
	switch {
	case err == nil:
		return existing, nil
	case err != sql.ErrNoRows:
		return SidebarGroupItemRow{}, fmt.Errorf("check existing: %w", err)
	}

	now := time.Now().UnixMilli()
	id := uuid.New().String()

	var maxOrder int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sort_order), -1) FROM sidebar_group_items WHERE group_id = ?",
		groupID,
	).Scan(&maxOrder); err != nil {
		return SidebarGroupItemRow{}, fmt.Errorf("next item sort order: %w", err)
	}

	row := SidebarGroupItemRow{
		ID:        id,
		GroupID:   groupID,
		ItemType:  itemType,
		RefID:     refID,
		SortOrder: maxOrder + 1,
		AddedAt:   now,
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sidebar_group_items (id, group_id, item_type, ref_id, sort_order, added_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		row.ID, row.GroupID, row.ItemType, row.RefID, row.SortOrder, row.AddedAt,
	); err != nil {
		return SidebarGroupItemRow{}, fmt.Errorf("insert group item: %w", err)
	}

	return row, nil
}

// RemoveItemFromSidebarGroup removes a specific item from a sidebar group by item ID.
func (s *Store) RemoveItemFromSidebarGroup(ctx context.Context, groupID, itemID string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM sidebar_group_items WHERE group_id = ? AND id = ?",
		groupID, itemID,
	)
	return err
}

// RemoveGroupItemsByRef removes all group item references matching the given type and ref ID
// across all groups. Used when a chat session is deleted.
func (s *Store) RemoveGroupItemsByRef(ctx context.Context, itemType, refID string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM sidebar_group_items WHERE item_type = ? AND ref_id = ?",
		itemType, refID,
	)
	return err
}

// RemoveGroupItemsByRefs removes all group item references matching the given type and
// any of the given ref IDs. Used for bulk deletion (e.g. all sessions in a project).
func (s *Store) RemoveGroupItemsByRefs(ctx context.Context, itemType string, refIDs []string) error {
	if len(refIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(refIDs))
	args := make([]any, 0, len(refIDs)+1)
	args = append(args, itemType)
	for i, id := range refIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	query := fmt.Sprintf(
		"DELETE FROM sidebar_group_items WHERE item_type = ? AND ref_id IN (%s)",
		strings.Join(placeholders, ","),
	)
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// ReorderSidebarGroupItems updates the sort_order of items within a group based on the given ID order.
func (s *Store) ReorderSidebarGroupItems(ctx context.Context, groupID string, itemIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	for i, id := range itemIDs {
		if _, err := tx.ExecContext(ctx,
			"UPDATE sidebar_group_items SET sort_order = ? WHERE id = ? AND group_id = ?",
			i, id, groupID,
		); err != nil {
			return fmt.Errorf("update item sort order for %s: %w", id, err)
		}
	}

	return tx.Commit()
}

// LoadAllSidebarGroups loads all sidebar groups with their items, ordered by sort_order.
func (s *Store) LoadAllSidebarGroups(ctx context.Context) ([]SidebarGroupRow, map[string][]SidebarGroupItemRow, error) {
	groupRows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, name, sort_order, collapsed, created_at, updated_at
		FROM sidebar_groups
		ORDER BY sort_order ASC`)
	if err != nil {
		return nil, nil, fmt.Errorf("load sidebar groups: %w", err)
	}
	defer groupRows.Close()

	var groups []SidebarGroupRow
	for groupRows.Next() {
		var g SidebarGroupRow
		var collapsed int
		if err := groupRows.Scan(&g.ID, &g.Name, &g.SortOrder, &collapsed, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, nil, fmt.Errorf("scan sidebar group: %w", err)
		}
		g.Collapsed = collapsed != 0
		groups = append(groups, g)
	}
	if err := groupRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate sidebar groups: %w", err)
	}

	itemRows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, group_id, item_type, ref_id, sort_order, added_at
		FROM sidebar_group_items
		ORDER BY sort_order ASC`)
	if err != nil {
		return nil, nil, fmt.Errorf("load sidebar group items: %w", err)
	}
	defer itemRows.Close()

	itemsByGroup := make(map[string][]SidebarGroupItemRow)
	for itemRows.Next() {
		var item SidebarGroupItemRow
		if err := itemRows.Scan(&item.ID, &item.GroupID, &item.ItemType, &item.RefID, &item.SortOrder, &item.AddedAt); err != nil {
			return nil, nil, fmt.Errorf("scan sidebar group item: %w", err)
		}
		itemsByGroup[item.GroupID] = append(itemsByGroup[item.GroupID], item)
	}
	if err := itemRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate sidebar group items: %w", err)
	}

	return groups, itemsByGroup, nil
}

// CleanupOrphanGroupItems removes sidebar_group_items whose ref_id no longer
// matches a valid chat session or automation. Called from the app layer which
// provides the valid ID lists.
func (s *Store) CleanupOrphanGroupItems(ctx context.Context, validChatIDs, validAutomationIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Clean chat refs that no longer exist.
	if len(validChatIDs) == 0 {
		// No valid chats: remove all chat refs.
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM sidebar_group_items WHERE item_type = 'chat'",
		); err != nil {
			return fmt.Errorf("clean all chat refs: %w", err)
		}
	} else {
		placeholders := make([]string, len(validChatIDs))
		args := make([]any, len(validChatIDs))
		for i, id := range validChatIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		query := fmt.Sprintf(
			"DELETE FROM sidebar_group_items WHERE item_type = 'chat' AND ref_id NOT IN (%s)",
			strings.Join(placeholders, ","),
		)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("clean orphan chat refs: %w", err)
		}
	}

	// Stale "crystal" item refs from the removed crystal-apps feature have no
	// valid-ID list anymore, so always sweep them out.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM sidebar_group_items WHERE item_type = 'crystal'",
	); err != nil {
		return fmt.Errorf("clean all crystal refs: %w", err)
	}

	// Clean automation refs that no longer exist.
	if len(validAutomationIDs) == 0 {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM sidebar_group_items WHERE item_type = 'automation'",
		); err != nil {
			return fmt.Errorf("clean all automation refs: %w", err)
		}
	} else {
		placeholders := make([]string, len(validAutomationIDs))
		args := make([]any, len(validAutomationIDs))
		for i, id := range validAutomationIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		query := fmt.Sprintf(
			"DELETE FROM sidebar_group_items WHERE item_type = 'automation' AND ref_id NOT IN (%s)",
			strings.Join(placeholders, ","),
		)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("clean orphan automation refs: %w", err)
		}
	}

	// Also clean items whose group no longer exists.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM sidebar_group_items WHERE group_id NOT IN (SELECT id FROM sidebar_groups)",
	); err != nil {
		return fmt.Errorf("clean orphan group items: %w", err)
	}

	return tx.Commit()
}

// DeleteProjectWithGroupCleanup removes a project and cleans up sidebar group items
// referencing its sessions, all within a single transaction.
func (s *Store) DeleteProjectWithGroupCleanup(ctx context.Context, projectID string) error {
	if main, err := s.holdsMainChat(ctx, s.db, "project_id", projectID); err != nil {
		return err
	} else if main {
		return ErrMainChatProtected
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Collect session local_ids that belong to this project.
	rows, err := tx.QueryContext(ctx,
		"SELECT local_id FROM sessions WHERE project_id = ?", projectID,
	)
	if err != nil {
		return fmt.Errorf("query sessions: %w", err)
	}
	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan session id: %w", err)
		}
		sessionIDs = append(sessionIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sessions: %w", err)
	}

	// Remove sidebar group items referencing these sessions.
	if len(sessionIDs) > 0 {
		placeholders := make([]string, len(sessionIDs))
		args := make([]any, len(sessionIDs))
		for i, id := range sessionIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		query := fmt.Sprintf(
			"DELETE FROM sidebar_group_items WHERE item_type = 'chat' AND ref_id IN (%s)",
			strings.Join(placeholders, ","),
		)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("clean group items for project sessions: %w", err)
		}
	}

	// Cascade delete: checkpoint_files, git_snapshots, stream_items, sessions, workspaces, project.
	cascadeStmts := []string{
		"DELETE FROM checkpoint_files WHERE session_local_id IN (SELECT local_id FROM sessions WHERE project_id = ?)",
		"DELETE FROM git_snapshots WHERE session_local_id IN (SELECT local_id FROM sessions WHERE project_id = ?)",
		"DELETE FROM stream_items WHERE session_local_id IN (SELECT local_id FROM sessions WHERE project_id = ?)",
		"DELETE FROM sessions WHERE project_id = ?",
		"DELETE FROM workspaces WHERE project_id = ?",
		"DELETE FROM projects WHERE id = ?",
	}
	for _, stmt := range cascadeStmts {
		if _, err := tx.ExecContext(ctx, stmt, projectID); err != nil {
			return fmt.Errorf("cascade delete: %w", err)
		}
	}

	return tx.Commit()
}

// DeleteWorkspaceWithGroupCleanup removes a workspace and cleans up sidebar group items
// referencing its sessions, all within a single transaction.
func (s *Store) DeleteWorkspaceWithGroupCleanup(ctx context.Context, workspaceID string) error {
	if main, err := s.holdsMainChat(ctx, s.db, "workspace_id", workspaceID); err != nil {
		return err
	} else if main {
		return ErrMainChatProtected
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Collect session local_ids that belong to this workspace.
	rows, err := tx.QueryContext(ctx,
		"SELECT local_id FROM sessions WHERE workspace_id = ?", workspaceID,
	)
	if err != nil {
		return fmt.Errorf("query sessions: %w", err)
	}
	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan session id: %w", err)
		}
		sessionIDs = append(sessionIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sessions: %w", err)
	}

	// Remove sidebar group items referencing these sessions.
	if len(sessionIDs) > 0 {
		placeholders := make([]string, len(sessionIDs))
		args := make([]any, len(sessionIDs))
		for i, id := range sessionIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		query := fmt.Sprintf(
			"DELETE FROM sidebar_group_items WHERE item_type = 'chat' AND ref_id IN (%s)",
			strings.Join(placeholders, ","),
		)
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("clean group items for workspace sessions: %w", err)
		}
	}

	// Cascade delete: checkpoint_files, git_snapshots, stream_items, sessions, workspace.
	cascadeStmts := []string{
		"DELETE FROM checkpoint_files WHERE session_local_id IN (SELECT local_id FROM sessions WHERE workspace_id = ?)",
		"DELETE FROM git_snapshots WHERE session_local_id IN (SELECT local_id FROM sessions WHERE workspace_id = ?)",
		"DELETE FROM stream_items WHERE session_local_id IN (SELECT local_id FROM sessions WHERE workspace_id = ?)",
		"DELETE FROM sessions WHERE workspace_id = ?",
		"DELETE FROM workspaces WHERE id = ?",
	}
	for _, stmt := range cascadeStmts {
		if _, err := tx.ExecContext(ctx, stmt, workspaceID); err != nil {
			return fmt.Errorf("cascade delete: %w", err)
		}
	}

	return tx.Commit()
}
