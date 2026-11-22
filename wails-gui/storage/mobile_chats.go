package storage

import (
	"context"
	"database/sql"
	"errors"
)

// MobileChatRow is one visible chat session as the phone reads it. Hidden
// sessions (cron-owned and other background runs) and sessions under hidden
// projects or workspaces are never returned.
type MobileChatRow struct {
	ID              string
	Label           string
	UpdatedAt       int64
	SessionID       string
	Model           string
	PlanMode        bool
	ThinkingEnabled bool
	ProjectPath     string
	WorktreePath    string
	// Kind is the session's chat-model kind ("main", "side" or "").
	Kind string
	// ParentID is the main chat a side chat was split off from.
	ParentID string
}

const mobileVisibleChatFrom = ` FROM sessions s
	JOIN projects p ON p.id = s.project_id
	JOIN workspaces w ON w.id = s.workspace_id
	WHERE s.hidden = 0 AND p.hidden = 0 AND w.hidden = 0 `

// MobileChatList returns the newest visible chat sessions first.
func (s *Store) MobileChatList(ctx context.Context, limit int) ([]MobileChatRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.local_id, s.label, s.updated_at, COALESCE(s.kind, ''), COALESCE(s.parent_id, '')`+mobileVisibleChatFrom+
			`ORDER BY s.updated_at DESC, s.local_id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []MobileChatRow
	for rows.Next() {
		var row MobileChatRow
		if err := rows.Scan(&row.ID, &row.Label, &row.UpdatedAt, &row.Kind, &row.ParentID); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// MobileChat returns one visible chat session with what a phone turn needs to
// run it. The bool is false when no visible session has the id.
func (s *Store) MobileChat(ctx context.Context, id string) (MobileChatRow, bool, error) {
	var row MobileChatRow
	err := s.db.QueryRowContext(ctx,
		`SELECT s.local_id, s.label, s.updated_at, s.session_id, s.model, s.plan_mode, s.thinking_enabled, p.path, w.worktree_path, COALESCE(s.kind, ''), COALESCE(s.parent_id, '')`+
			mobileVisibleChatFrom+`AND s.local_id = ?`, id,
	).Scan(&row.ID, &row.Label, &row.UpdatedAt, &row.SessionID, &row.Model, &row.PlanMode, &row.ThinkingEnabled, &row.ProjectPath, &row.WorktreePath, &row.Kind, &row.ParentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MobileChatRow{}, false, nil
		}
		return MobileChatRow{}, false, err
	}
	return row, true, nil
}

// LoadStreamItemsAfter returns up to limit items whose sort order is greater
// than afterSortOrder, oldest first. HasMore reports whether more rows follow.
func (s *Store) LoadStreamItemsAfter(ctx context.Context, sessionLocalID string, afterSortOrder, limit int) (StreamItemPage, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM stream_items WHERE session_local_id = ? AND sort_order > ?
		ORDER BY sort_order ASC LIMIT ?
	`, sessionLocalID, afterSortOrder, limit+1)
	if err != nil {
		return StreamItemPage{}, err
	}
	defer rows.Close()
	var items []StreamItemRow
	for rows.Next() {
		var item StreamItemRow
		if err := rows.Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files); err != nil {
			return StreamItemPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return StreamItemPage{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return StreamItemPage{Items: items, HasMore: hasMore}, nil
}

// DeleteStreamItemByID removes one stream item. The phone bridge uses it to
// roll back its own user row, so rows the desktop stored meanwhile survive.
func (s *Store) DeleteStreamItemByID(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM stream_items WHERE id = ?`, id)
	return err
}
