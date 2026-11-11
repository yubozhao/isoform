package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// MainChatViewSnapshot reads the main identity, preference and candidate rows in
// one transaction. The caller owns classification; storage has no second rule.
type MainChatViewSnapshot struct {
	SessionID   string
	HideRoutine bool
	Items       []StreamItemRow
}

// Legacy cards predate the expiry payload field. Read their original moment's
// deadline in the same snapshot; never rewrite history or infer a new deadline.
const mainChatViewInput = `CASE WHEN i.kind='system' AND i.tool_name='main_chat_proactive' AND json_valid(i.tool_input)
    THEN CASE WHEN json_type(i.tool_input,'$.expiresAt') IS NULL
        THEN json_set(i.tool_input,'$.expiresAt',COALESCE((SELECT expires_at FROM engagement_moments WHERE id=json_extract(i.tool_input,'$.momentId')),0))
        ELSE i.tool_input END ELSE i.tool_input END`

const mainChatViewColumns = `i.id,i.session_local_id,i.kind,i.content,i.tool_name,` + mainChatViewInput + `,i.tool_use_id,i.run_id,i.session_id,i.sort_order,i.created_at,i.images,i.automation_context,i.files`

// This is an indexed candidate superset, not a second row-class rule: Go
// still decides waiting, expiry and digest lifecycle. Closed rows and routine
// kinds cannot require an answer; their old payloads need not enter Go at all.
const mainChatOpenCandidatePredicate = `kind='system' AND tool_name='main_chat_proactive'
    AND json_extract(CASE WHEN json_valid(tool_input) THEN tool_input ELSE '{}' END,'$.state') IN ('pending','opened')
    AND COALESCE(json_extract(CASE WHEN json_valid(tool_input) THEN tool_input ELSE '{}' END,'$.kind'),'') NOT IN ('brief_ready','capture_paused','work_finished')`

// Legacy digest envelopes can still say pending after every child is handled.
// The indexed root-state scan filters those envelopes before loading payloads.
// Descendant leaf states are only a superset: Go checks nested expiry/kinds.
const mainChatOpenDigestPredicate = `(COALESCE(json_extract(i.tool_input,'$.kind'),'')!='digest' OR EXISTS (
    SELECT 1 FROM json_tree(i.tool_input,'$.items') child
    WHERE child.type='object'
    AND json_extract(CASE WHEN child.type='object' THEN child.value ELSE '{}' END,'$.state') IN ('pending','opened')
    AND COALESCE(json_extract(CASE WHEN child.type='object' THEN child.value ELSE '{}' END,'$.kind'),'') NOT IN ('brief_ready','capture_paused','work_finished','digest')))`

// Access indexes are additive repairs, with no schema stamp or table change.
func (s *Store) ensureMainChatViewIndexes() error {
	_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_stream_items_session_created ON stream_items(session_local_id,created_at DESC,id DESC);
        CREATE INDEX IF NOT EXISTS idx_stream_items_main_chat_open ON stream_items(session_local_id,created_at DESC,id DESC) WHERE ` + mainChatOpenCandidatePredicate + `;`)
	return err
}

func scanMainChatViewRow(rows *sql.Rows) (StreamItemRow, error) {
	var r StreamItemRow
	err := rows.Scan(&r.ID, &r.SessionLocalID, &r.Kind, &r.Content, &r.ToolName, &r.ToolInput, &r.ToolUseID, &r.RunID, &r.SessionID, &r.SortOrder, &r.CreatedAt, &r.Images, &r.AutomationContext, &r.Files)
	return r, err
}

// ReadMainChatView includes today's rows and indexed open candidates of any age.
func (s *Store) ReadMainChatView(ctx context.Context, since int64) (MainChatViewSnapshot, error) {
	return s.readMainChatView(ctx, since, 0, "", 0)
}

// ReadMainChatWaitingCandidates bounds a backwards open-card scan; the caller
// classifies candidates and advances through expiry/legacy digest filtering.
func (s *Store) ReadMainChatWaitingCandidates(ctx context.Context, at int64, id string, limit int) (MainChatViewSnapshot, error) {
	if limit <= 0 || limit > 201 {
		limit = 201
	}
	return s.readMainChatView(ctx, 0, at, id, limit)
}
func (s *Store) readMainChatView(ctx context.Context, since, at int64, id string, limit int) (MainChatViewSnapshot, error) {
	result := MainChatViewSnapshot{Items: []StreamItemRow{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var hide string
	err = tx.QueryRowContext(ctx, `SELECT value FROM ui_state WHERE key='main_chat_hide_routine'`).Scan(&hide)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	result.HideRoutine = hide == "true"
	err = tx.QueryRowContext(ctx, `SELECT local_id FROM sessions WHERE kind='main' ORDER BY created_at,local_id LIMIT 1`).Scan(&result.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, tx.Commit()
	}
	if err != nil {
		return result, err
	}
	var query string
	var args []any
	if limit > 0 {
		query = `SELECT ` + mainChatViewColumns + ` FROM stream_items i INDEXED BY idx_stream_items_main_chat_open WHERE session_local_id=? AND ` + mainChatOpenCandidatePredicate + ` AND ` + mainChatOpenDigestPredicate + ` AND (created_at<? OR (created_at=? AND id<?)) ORDER BY created_at DESC,id DESC LIMIT ?`
		args = []any{result.SessionID, at, at, id, limit}
	} else {
		// Disjoint indexed ranges avoid a lifetime OR scan and duplicates.
		query = `SELECT ` + mainChatViewColumns + ` FROM stream_items i INDEXED BY idx_stream_items_session_created WHERE session_local_id=? AND created_at>=?
            UNION ALL SELECT ` + mainChatViewColumns + ` FROM stream_items i INDEXED BY idx_stream_items_main_chat_open WHERE session_local_id=? AND created_at<? AND ` + mainChatOpenCandidatePredicate + ` AND ` + mainChatOpenDigestPredicate + ` ORDER BY created_at DESC,id DESC`
		args = []any{result.SessionID, since, result.SessionID, since}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		r, e := scanMainChatViewRow(rows)
		if e != nil {
			rows.Close()
			return result, e
		}
		result.Items = append(result.Items, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

type MainChatRowMetadata struct {
	Row      StreamItemRow
	TopicIDs []string
}

// LoadMainChatRowMetadata loads all page tags and classification inputs in one
// query. Only main-session rows participate; another chat has no view metadata.
func (s *Store) LoadMainChatRowMetadata(ctx context.Context, sessionID string, ids []string) (map[string]MainChatRowMetadata, error) {
	out := map[string]MainChatRowMetadata{}
	if len(ids) == 0 {
		return out, nil
	}
	args := []any{sessionID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.streamReader().QueryContext(ctx, `SELECT i.id,i.kind,i.content,i.tool_name,`+mainChatViewInput+`,i.created_at,COALESCE(t.topic_id,'') FROM stream_items i JOIN sessions s ON s.local_id=i.session_local_id AND s.kind='main' LEFT JOIN stream_item_topics t ON t.stream_item_id=i.id AND t.session_id=i.session_local_id WHERE i.session_local_id=? AND i.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY t.topic_id`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var r StreamItemRow
		var tag string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Content, &r.ToolName, &r.ToolInput, &r.CreatedAt, &tag); err != nil {
			return out, err
		}
		m := out[r.ID]
		m.Row = r
		if tag != "" {
			m.TopicIDs = append(m.TopicIDs, tag)
		}
		out[r.ID] = m
	}
	return out, rows.Err()
}

// LoadMainChatTopicBackfillRows bounds the one-time scan without loading chats
// outside the main conversation or parsing arbitrary historical payloads.
func (s *Store) LoadMainChatTopicBackfillRows(ctx context.Context, since int64) ([]StreamItemRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mainChatViewColumns+` FROM stream_items i JOIN sessions s ON s.local_id=i.session_local_id AND s.kind='main' WHERE i.kind='system' AND i.tool_name IN ('main_chat_proactive','main_chat_report') AND i.created_at>=? ORDER BY i.created_at DESC,i.id DESC LIMIT 500`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StreamItemRow{}
	for rows.Next() {
		r, err := scanMainChatViewRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
