package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// SaveSession upserts a session row.
func (s *Store) SaveSession(ctx context.Context, sess SessionRow) error {
	now := time.Now().UnixMilli()
	if sess.CreatedAt == 0 {
		sess.CreatedAt = now
	}
	sess.UpdatedAt = now

	return upsertSession(ctx, s.db, sess)
}

// LoadSessions returns all sessions for a given project.
func (s *Store) LoadSessions(ctx context.Context, projectID string) ([]SessionRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT local_id, project_id, workspace_id, session_id, label, model, prd_enabled, thinking_level, plan_mode, thinking_enabled, pinned, pinned_at, automation_result, COALESCE(meeting_id, ''), COALESCE(kind, ''), COALESCE(parent_id, ''), created_at, updated_at
		FROM sessions WHERE project_id = ? AND hidden = 0 ORDER BY pinned DESC, pinned_at ASC, updated_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SessionRow
	for rows.Next() {
		var sess SessionRow
		if err := rows.Scan(&sess.LocalID, &sess.ProjectID, &sess.WorkspaceID, &sess.SessionID, &sess.Label, &sess.Model, &sess.PrdEnabled, &sess.ThinkingLevel, &sess.PlanMode, &sess.ThinkingEnabled, &sess.Pinned, &sess.PinnedAt, &sess.AutomationResult, &sess.MeetingID, &sess.Kind, &sess.ParentID, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, sess)
	}
	return result, rows.Err()
}

// LoadSession returns a single session by local_id.
func (s *Store) LoadSession(ctx context.Context, localID string) (*SessionRow, error) {
	var sess SessionRow
	err := s.db.QueryRowContext(ctx, `
		SELECT local_id, project_id, workspace_id, session_id, label, model, prd_enabled, thinking_level, plan_mode, thinking_enabled, pinned, pinned_at, automation_result, COALESCE(meeting_id, ''), COALESCE(kind, ''), COALESCE(parent_id, ''), created_at, updated_at
		FROM sessions WHERE local_id = ?
	`, localID).Scan(&sess.LocalID, &sess.ProjectID, &sess.WorkspaceID, &sess.SessionID, &sess.Label, &sess.Model, &sess.PrdEnabled, &sess.ThinkingLevel, &sess.PlanMode, &sess.ThinkingEnabled, &sess.Pinned, &sess.PinnedAt, &sess.AutomationResult, &sess.MeetingID, &sess.Kind, &sess.ParentID, &sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// SessionHidden reports whether the session with localID is hidden.
// sql.ErrNoRows when there is no such session.
func (s *Store) SessionHidden(ctx context.Context, localID string) (bool, error) {
	var hidden bool
	err := s.db.QueryRowContext(ctx, `SELECT hidden FROM sessions WHERE local_id = ?`, localID).Scan(&hidden)
	return hidden, err
}

// LoadMainSession returns the main chat session (kind = "main"), hidden or
// not, oldest first when more than one exists. sql.ErrNoRows when none.
func (s *Store) LoadMainSession(ctx context.Context) (*SessionRow, error) {
	var sess SessionRow
	var hidden bool
	err := s.db.QueryRowContext(ctx, `
		SELECT local_id, project_id, workspace_id, session_id, label, model, prd_enabled, thinking_level, plan_mode, thinking_enabled, pinned, pinned_at, automation_result, hidden, COALESCE(meeting_id, ''), COALESCE(kind, ''), COALESCE(parent_id, ''), created_at, updated_at
		FROM sessions WHERE kind = ? ORDER BY created_at ASC, local_id ASC LIMIT 1
	`, SessionKindMain).Scan(&sess.LocalID, &sess.ProjectID, &sess.WorkspaceID, &sess.SessionID, &sess.Label, &sess.Model, &sess.PrdEnabled, &sess.ThinkingLevel, &sess.PlanMode, &sess.ThinkingEnabled, &sess.Pinned, &sess.PinnedAt, &sess.AutomationResult, &hidden, &sess.MeetingID, &sess.Kind, &sess.ParentID, &sess.CreatedAt, &sess.UpdatedAt)
	if err != nil {
		return nil, err
	}
	sess.Hidden = hidden
	return &sess, nil
}

// LoadSessionsByParent returns the visible side chats split off from parentID,
// newest first.
func (s *Store) LoadSessionsByParent(ctx context.Context, parentID string) ([]SessionRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT local_id, project_id, workspace_id, session_id, label, model, prd_enabled, thinking_level, plan_mode, thinking_enabled, pinned, pinned_at, automation_result, COALESCE(meeting_id, ''), COALESCE(kind, ''), COALESCE(parent_id, ''), created_at, updated_at
		FROM sessions WHERE parent_id = ? AND hidden = 0 ORDER BY updated_at DESC, local_id ASC
	`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []SessionRow
	for rows.Next() {
		var sess SessionRow
		if err := rows.Scan(&sess.LocalID, &sess.ProjectID, &sess.WorkspaceID, &sess.SessionID, &sess.Label, &sess.Model, &sess.PrdEnabled, &sess.ThinkingLevel, &sess.PlanMode, &sess.ThinkingEnabled, &sess.Pinned, &sess.PinnedAt, &sess.AutomationResult, &sess.MeetingID, &sess.Kind, &sess.ParentID, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, sess)
	}
	return result, rows.Err()
}

// SessionExists reports whether a chat session row is saved under localID. It
// counts rows rather than scanning one, so a row with unset columns still counts.
func (s *Store) SessionExists(ctx context.Context, localID string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE local_id = ?`, localID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// LoadAllSessions returns all sessions across all projects, keyed by project ID.
// This avoids N+1 queries when loading multiple projects.
func (s *Store) LoadAllSessions(ctx context.Context) (map[string][]SessionRow, error) {
	rows, err := s.db.QueryContext(ctx, `
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
		result[sess.ProjectID] = append(result[sess.ProjectID], sess)
	}
	return result, rows.Err()
}

// queryer is the subset of *sql.DB and *sql.Tx the guards need.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ErrMainChatProtected is returned by every delete that would remove the main
// chat (kind "main"), directly or by cascading over its workspace or project.
var ErrMainChatProtected = errors.New("the chat cannot be deleted")

// holdsMainChat reports whether a session row matching column = id is the
// main chat; column is one of local_id, workspace_id or project_id.
func (s *Store) holdsMainChat(ctx context.Context, q queryer, column, id string) (bool, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE `+column+` = ? AND kind = ?`, id, SessionKindMain).Scan(&count)
	return count > 0, err
}

// DeleteSession removes a session and its items via cascade. The main chat
// is refused with ErrMainChatProtected.
func (s *Store) DeleteSession(ctx context.Context, localID string) error {
	if main, err := s.holdsMainChat(ctx, s.db, "local_id", localID); err != nil {
		return err
	} else if main {
		return ErrMainChatProtected
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE local_id = ?", localID)
	return err
}

// SoftDeleteSession marks a session as hidden so list queries skip it but
// the row (and its stream_items) remain on disk. Used for chat sessions
// that back a pending approval: hiding lets the user "delete" the
// chat from the sidebar without losing the approval payload, which they
// can later recover by reopening the approval chat.
func (s *Store) SoftDeleteSession(ctx context.Context, localID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET hidden = 1, updated_at = ? WHERE local_id = ?",
		time.Now().UnixMilli(), localID,
	)
	return err
}

// RestoreSession clears the hidden flag on a previously soft-deleted
// session, bringing it back into list queries (and therefore the sidebar).
func (s *Store) RestoreSession(ctx context.Context, localID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET hidden = 0, updated_at = ? WHERE local_id = ?",
		time.Now().UnixMilli(), localID,
	)
	return err
}

// UpdateSessionSessionID updates the Claude session_id for a session.
func (s *Store) UpdateSessionSessionID(ctx context.Context, localID, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET session_id = ?, updated_at = ? WHERE local_id = ?",
		sessionID, time.Now().UnixMilli(), localID,
	)
	return err
}

// ForkSession creates an independent session containing a snapshot of every
// persisted stream item in sourceLocalID. Runtime identity and sidebar-only
// metadata are deliberately reset so future turns cannot resume or group with
// the source session.
func (s *Store) ForkSession(ctx context.Context, sourceLocalID, newLocalID, newLabel string) (SessionRow, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionRow{}, 0, errors.Wrap(err, "begin fork session transaction")
	}
	defer tx.Rollback() //nolint:errcheck

	var source SessionRow
	err = tx.QueryRowContext(ctx, `
		SELECT local_id, project_id, workspace_id, session_id, label, model, prd_enabled, thinking_level, plan_mode, thinking_enabled, pinned, pinned_at, automation_result, hidden, COALESCE(meeting_id, ''), COALESCE(kind, ''), COALESCE(parent_id, ''), created_at, updated_at
		FROM sessions WHERE local_id = ? AND hidden = 0
	`, sourceLocalID).Scan(
		&source.LocalID,
		&source.ProjectID,
		&source.WorkspaceID,
		&source.SessionID,
		&source.Label,
		&source.Model,
		&source.PrdEnabled,
		&source.ThinkingLevel,
		&source.PlanMode,
		&source.ThinkingEnabled,
		&source.Pinned,
		&source.PinnedAt,
		&source.AutomationResult,
		&source.Hidden,
		&source.MeetingID,
		&source.Kind,
		&source.ParentID,
		&source.CreatedAt,
		&source.UpdatedAt,
	)
	if err != nil {
		return SessionRow{}, 0, errors.Wrap(err, "load source session")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM stream_items WHERE session_local_id = ? ORDER BY sort_order ASC
	`, sourceLocalID)
	if err != nil {
		return SessionRow{}, 0, errors.Wrap(err, "load source stream items")
	}
	defer rows.Close() //nolint:errcheck

	var sourceItems []StreamItemRow
	for rows.Next() {
		var item StreamItemRow
		if err := rows.Scan(
			&item.ID,
			&item.SessionLocalID,
			&item.Kind,
			&item.Content,
			&item.ToolName,
			&item.ToolInput,
			&item.ToolUseID,
			&item.RunID,
			&item.SessionID,
			&item.SortOrder,
			&item.CreatedAt,
			&item.Images,
			&item.AutomationContext,
			&item.Files,
		); err != nil {
			return SessionRow{}, 0, errors.Wrap(err, "scan source stream item")
		}
		sourceItems = append(sourceItems, item)
	}
	if err := rows.Err(); err != nil {
		return SessionRow{}, 0, errors.Wrap(err, "iterate source stream items")
	}
	if err := rows.Close(); err != nil {
		return SessionRow{}, 0, errors.Wrap(err, "close source stream items")
	}

	now := time.Now().UnixMilli()
	label := strings.TrimSpace(newLabel)
	if label == "" {
		label = source.Label + " (branch)"
	}
	forked := SessionRow{
		LocalID:         newLocalID,
		ProjectID:       source.ProjectID,
		WorkspaceID:     source.WorkspaceID,
		Label:           label,
		Model:           source.Model,
		PrdEnabled:      source.PrdEnabled,
		ThinkingLevel:   source.ThinkingLevel,
		PlanMode:        source.PlanMode,
		ThinkingEnabled: source.ThinkingEnabled,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := upsertSession(ctx, tx, forked); err != nil {
		return SessionRow{}, 0, errors.Wrap(err, "insert forked session")
	}

	itemIDMap := make(map[string]string, len(sourceItems))
	for _, item := range sourceItems {
		itemIDMap[item.ID] = uuid.NewString()
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO stream_items (id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return SessionRow{}, 0, errors.Wrap(err, "prepare forked stream item insert")
	}
	defer stmt.Close()

	for _, item := range sourceItems {
		content := item.Content
		if item.Kind == "compact_boundary" {
			content = remapCompactBoundarySummaryID(content, itemIDMap)
		}
		if _, err := stmt.ExecContext(
			ctx,
			itemIDMap[item.ID],
			newLocalID,
			item.Kind,
			content,
			item.ToolName,
			item.ToolInput,
			item.ToolUseID,
			item.RunID,
			item.SessionID,
			item.SortOrder,
			item.CreatedAt,
			item.Images,
			item.AutomationContext,
			item.Files,
		); err != nil {
			return SessionRow{}, 0, errors.Wrap(err, "insert forked stream item")
		}
	}

	if err := tx.Commit(); err != nil {
		return SessionRow{}, 0, errors.Wrap(err, "commit forked session")
	}
	return forked, len(sourceItems), nil
}

func remapCompactBoundarySummaryID(content string, itemIDMap map[string]string) string {
	var metadata map[string]any
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		return content
	}
	summaryID, ok := metadata["summary_id"].(string)
	if !ok {
		return content
	}
	newSummaryID, ok := itemIDMap[summaryID]
	if !ok {
		return content
	}
	metadata["summary_id"] = newSummaryID
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return content
	}
	return string(encoded)
}

// ClearMainChat removes every message of the main chat and what hangs off
// them (chips, topics, open answers and their posts), and keeps the session
// itself: one empty main chat with the same id (Your data's Forget
// everything). It is the main chat's own clear, not a delete: the main chat
// still can't be deleted. It returns the main chat's id, "" when there is none.
func (s *Store) ClearMainChat(ctx context.Context) (string, error) {
	var localID string
	err := s.db.QueryRowContext(ctx, `SELECT local_id FROM sessions WHERE kind = ? LIMIT 1`, SessionKindMain).Scan(&localID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM open_answers WHERE chat_session_id = ?`,
		`DELETE FROM stream_items WHERE session_local_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, localID); err != nil {
			return "", fmt.Errorf("clear main chat: %w", err)
		}
	}
	return localID, tx.Commit()
}
