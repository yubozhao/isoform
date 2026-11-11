package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// ErrStreamItemIDTaken reports an append whose id already names a row of
// another session.
var ErrStreamItemIDTaken = errors.New("stream item id belongs to another session")

// AppendStreamItem inserts a single stream item with an atomically determined sort_order.
// It also bumps the parent session's updated_at so that sidebar sorting by
// most-recently-active remains correct across app restarts.
// It returns the assigned sort_order so the caller can use it (e.g. for checkpoint association).
//
// Appending an id the session already has stores nothing and returns that
// row's sort_order: every renderer showing a turn (the app window and a
// browser preview) writes the rows it builds from the same agent events, with
// the same ids, and only the first write of each counts.
func (s *Store) AppendStreamItem(ctx context.Context, sessionLocalID string, item StreamItemRow) (int, error) {
	return appendStreamItem(ctx, s.db, sessionLocalID, item)
}

type streamItemWriter interface {
	execer
	queryer
}

func appendStreamItem(ctx context.Context, db streamItemWriter, sessionLocalID string, item StreamItemRow) (int, error) {
	now := time.Now().UnixMilli()
	if item.CreatedAt == 0 {
		item.CreatedAt = now
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO stream_items (id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(sort_order), -1) + 1 FROM stream_items WHERE session_local_id = ?), ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, item.ID, sessionLocalID, item.Kind, item.Content, item.ToolName, item.ToolInput, item.ToolUseID, item.RunID, item.SessionID, sessionLocalID, item.CreatedAt, item.Images, item.AutomationContext, item.Files)
	if err != nil {
		return 0, err
	}

	// Bump session updated_at so sidebar sorts correctly after restart.
	_, _ = db.ExecContext(ctx,
		"UPDATE sessions SET updated_at = ? WHERE local_id = ?",
		now, sessionLocalID,
	)

	var sortOrder int
	var owner string
	err = db.QueryRowContext(ctx, "SELECT sort_order, session_local_id FROM stream_items WHERE id = ?", item.ID).Scan(&sortOrder, &owner)
	if err != nil {
		return 0, err
	}
	if owner != sessionLocalID {
		return 0, ErrStreamItemIDTaken
	}
	return sortOrder, nil
}

// AppendStreamItemChecked keeps the row invisible to readers until validate
// succeeds. Validation must read through the supplied read-only Store. Both
// connections are private to this operation: the normal Store has a single
// connection, which may be occupied by a concurrent writer waiting for this
// transaction. Borrowing it for validation would create a circular wait.
// A rejection rolls back an uncommitted insert; it never deletes a published row.
func (s *Store) AppendStreamItemChecked(ctx context.Context, sessionLocalID string, item StreamItemRow, validate func(readOnly *Store) error) (int, error) {
	s.closeMu.Lock()
	if s.closing {
		s.closeMu.Unlock()
		return 0, sql.ErrConnDone
	}
	s.operations.Add(1)
	s.closeMu.Unlock()
	defer s.operations.Done()
	if validate == nil {
		return 0, errors.New("stream item admission check is required")
	}
	var path string
	if err := s.db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		return 0, err
	}
	if path == "" {
		return 0, errors.New("checked stream items require a file-backed store")
	}
	readDB, err := openReadOnlyDatabase(path)
	if err != nil {
		return 0, err
	}
	defer readDB.Close()
	readOnly := &Store{db: readDB}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// The existing store already establishes WAL. Never change journal mode
	// or run migrations just to admit a message.
	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return 0, err
	}
	if journal != "wal" {
		return 0, errors.New("checked stream items require WAL isolation")
	}
	for _, query := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			return 0, err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	order, err := appendStreamItem(ctx, tx, sessionLocalID, item)
	if err != nil {
		return 0, err
	}
	if err := validate(readOnly); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return order, nil
}

// SaveStreamItems batch-inserts stream items (used for initial hydration or bulk save).
func (s *Store) SaveStreamItems(ctx context.Context, sessionLocalID string, items []StreamItemRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO stream_items (id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            session_local_id=excluded.session_local_id, kind=excluded.kind, content=excluded.content,
            tool_name=excluded.tool_name, tool_input=excluded.tool_input, tool_use_id=excluded.tool_use_id,
            run_id=CASE WHEN stream_items.run_id != '' AND ((stream_items.kind='user' AND EXISTS (SELECT 1 FROM sessions WHERE local_id=stream_items.session_local_id AND kind='main')) OR EXISTS (SELECT 1 FROM stream_item_topics WHERE stream_item_id=excluded.id)) THEN stream_items.run_id ELSE excluded.run_id END,
            session_id=excluded.session_id, sort_order=excluded.sort_order, created_at=excluded.created_at,
            images=excluded.images, automation_context=CASE WHEN json_type(CASE WHEN json_valid(stream_items.automation_context) THEN stream_items.automation_context ELSE '{}' END, '$.topicContext') IS NOT NULL THEN stream_items.automation_context ELSE excluded.automation_context END, files=excluded.files
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	// Query current max sort_order so new items don't collide with existing ones.
	var maxOrder int
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(sort_order), -1) FROM stream_items WHERE session_local_id = ?",
		sessionLocalID,
	).Scan(&maxOrder); err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	for i, item := range items {
		createdAt := item.CreatedAt
		if createdAt == 0 {
			createdAt = now
		}
		if _, err := stmt.ExecContext(ctx, item.ID, sessionLocalID, item.Kind, item.Content, item.ToolName, item.ToolInput, item.ToolUseID, item.RunID, item.SessionID, maxOrder+1+i, createdAt, item.Images, item.AutomationContext, item.Files); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// LoadStreamItems returns all items for a session ordered by sort_order.
func (s *Store) LoadStreamItems(ctx context.Context, sessionLocalID string) ([]StreamItemRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM stream_items WHERE session_local_id = ? ORDER BY sort_order ASC
	`, sessionLocalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []StreamItemRow
	for rows.Next() {
		var item StreamItemRow
		if err := rows.Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

const findProactiveRootSQL = `
	SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
	FROM stream_items INDEXED BY idx_stream_items_proactive_moment
	WHERE session_local_id = ? AND tool_name = 'main_chat_proactive'
		AND json_extract(CASE WHEN json_valid(tool_input) THEN tool_input ELSE '{}' END, '$.momentId') = ?
	ORDER BY sort_order ASC LIMIT 1`

const findProactiveChildSQL = `
	SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
	FROM stream_items WHERE id = (
		SELECT stream_item_id FROM stream_item_proactive_children INDEXED BY idx_proactive_children_identity
		WHERE session_local_id = ? AND moment_id = ?
		ORDER BY sort_order ASC LIMIT 1
	)`

// FindProactiveStreamItem finds a root moment or digest child without loading
// the transcript. Root and child identities both use exact indexed searches,
// including misses for new moments. Missing and malformed payloads do not
// match. At most one row enters Go memory.
func (s *Store) FindProactiveStreamItem(ctx context.Context, sessionLocalID, momentID string) (*StreamItemRow, error) {
	if momentID == "" {
		return nil, nil
	}
	item, err := scanStreamItemRow(s.db.QueryRowContext(ctx, findProactiveRootSQL, sessionLocalID, momentID))
	if err != nil || item != nil {
		return item, err
	}
	return scanStreamItemRow(s.db.QueryRowContext(ctx, findProactiveChildSQL, sessionLocalID, momentID))
}

const findProactiveBriefCardSQL = `
	SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
	FROM stream_items
	WHERE session_local_id = ? AND tool_name = 'main_chat_proactive'
		AND json_extract(CASE WHEN json_valid(tool_input) THEN tool_input ELSE '{}' END, '$.kind') = 'brief_ready'
		AND json_extract(CASE WHEN json_valid(tool_input) THEN tool_input ELSE '{}' END, '$.briefId') = ?
	ORDER BY sort_order ASC LIMIT 1`

// FindProactiveBriefCard returns the earliest Chat card of one brief, whatever
// moment posted it (R40). Malformed payloads never match.
func (s *Store) FindProactiveBriefCard(ctx context.Context, sessionLocalID, briefID string) (*StreamItemRow, error) {
	if briefID == "" {
		return nil, nil
	}
	return scanStreamItemRow(s.db.QueryRowContext(ctx, findProactiveBriefCardSQL, sessionLocalID, briefID))
}

// ListProactiveBriefCards returns every brief Chat card of a session in
// sort order, the earliest first (R40 repair).
func (s *Store) ListProactiveBriefCards(ctx context.Context, sessionLocalID string) ([]StreamItemRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM stream_items
		WHERE session_local_id = ? AND tool_name = 'main_chat_proactive'
			AND json_extract(CASE WHEN json_valid(tool_input) THEN tool_input ELSE '{}' END, '$.kind') = 'brief_ready'
		ORDER BY sort_order ASC
	`, sessionLocalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StreamItemRow
	for rows.Next() {
		item, err := scanStreamItemRow(rows)
		if err != nil {
			return nil, err
		}
		if item != nil {
			result = append(result, *item)
		}
	}
	return result, rows.Err()
}

const findStatusStreamItemSQL = `
	SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
	FROM stream_items WHERE id = (
		SELECT stream_item_id FROM stream_item_status_entries INDEXED BY idx_status_entries_identity
		WHERE session_local_id = ? AND entry_id = ?
		ORDER BY sort_order ASC LIMIT 1
	)`

// FindStatusStreamItem finds the row already holding an entry, across periods.
// Exact indexed identity lookup keeps retry dedupe independent of chat length.
func (s *Store) FindStatusStreamItem(ctx context.Context, sessionLocalID, entryID string) (*StreamItemRow, error) {
	if entryID == "" {
		return nil, nil
	}
	return scanStreamItemRow(s.db.QueryRowContext(ctx, findStatusStreamItemSQL, sessionLocalID, entryID))
}

func scanStreamItemRow(row interface{ Scan(...any) error }) (*StreamItemRow, error) {
	var item StreamItemRow
	err := row.Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// LoadAllStreamItems returns all stream items across all sessions, keyed by session local ID.
// This avoids N*M queries when loading all projects at startup.
func (s *Store) LoadAllStreamItems(ctx context.Context) (map[string][]StreamItemRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM stream_items ORDER BY sort_order ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]StreamItemRow)
	for rows.Next() {
		var item StreamItemRow
		if err := rows.Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files); err != nil {
			return nil, err
		}
		result[item.SessionLocalID] = append(result[item.SessionLocalID], item)
	}
	return result, rows.Err()
}

// LoadStreamItemCounts returns the persisted stream item count for every
// session that has at least one item. Used by metadata-only project loads.
func (s *Store) LoadStreamItemCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_local_id, COUNT(*)
		FROM stream_items
		GROUP BY session_local_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var sessionLocalID string
		var count int
		if err := rows.Scan(&sessionLocalID, &count); err != nil {
			return nil, err
		}
		result[sessionLocalID] = count
	}
	return result, rows.Err()
}

// LoadStreamItemsLatest loads the most recent `limit` items for a session.
// Items are returned in ascending sort_order (oldest first).
func (s *Store) LoadStreamItemsLatest(ctx context.Context, sessionLocalID string, limit int) (StreamItemPage, error) {
	// Fetch limit+1 to detect whether more items exist.
	rows, err := s.streamReader().QueryContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM stream_items WHERE session_local_id = ?
		ORDER BY sort_order DESC LIMIT ?
	`, sessionLocalID, limit+1)
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

	// Reverse so items are in ascending sort_order.
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}

	return StreamItemPage{Items: items, HasMore: hasMore}, nil
}

// LoadConversationHistory loads the most recent user/assistant items for a
// session, skipping tool_use, result, system, thinking, etc. This ensures
// that history injection after a session restart captures enough actual
// conversation turns regardless of how many tool calls were made.
// Items are returned in ascending sort_order (oldest first).
func (s *Store) LoadConversationHistory(ctx context.Context, sessionLocalID string, limit int) ([]StreamItemRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM stream_items
		WHERE session_local_id = ? AND kind IN ('user', 'assistant') AND content != ''
		ORDER BY sort_order DESC LIMIT ?
	`, sessionLocalID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []StreamItemRow
	for rows.Next() {
		var item StreamItemRow
		if err := rows.Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse so items are in ascending sort_order.
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}

	return items, nil
}

// LoadStreamItemsBefore loads `limit` items with sort_order < cursor.
// Items are returned in ascending sort_order (oldest first).
func (s *Store) LoadStreamItemsBefore(ctx context.Context, sessionLocalID string, cursor, limit int) (StreamItemPage, error) {
	rows, err := s.streamReader().QueryContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM stream_items WHERE session_local_id = ? AND sort_order < ?
		ORDER BY sort_order DESC LIMIT ?
	`, sessionLocalID, cursor, limit+1)
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

	// Reverse so items are in ascending sort_order.
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}

	return StreamItemPage{Items: items, HasMore: hasMore}, nil
}

// LoadRecentStreamItems loads the latest `limit` items per session across all sessions.
// Used during startup to avoid loading the entire stream_items table.
func (s *Store) LoadRecentStreamItems(ctx context.Context, limit int) (map[string][]StreamItemRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY session_local_id ORDER BY sort_order DESC) AS rn
			FROM stream_items
		) sub
		WHERE rn <= ?
		ORDER BY session_local_id, sort_order ASC
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]StreamItemRow)
	for rows.Next() {
		var item StreamItemRow
		if err := rows.Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files); err != nil {
			return nil, err
		}
		result[item.SessionLocalID] = append(result[item.SessionLocalID], item)
	}
	return result, rows.Err()
}

// LoadRecentStreamItemsByTurns loads the last `turns` conversation turns (user messages)
// worth of stream items per session across all sessions. This ensures that a consistent
// number of user-visible conversation exchanges are loaded regardless of how many
// tool calls or intermediate items each turn produced.
// Sessions that have no user messages fall back to loading the most recent 50 items.
//
// When a session has fewer user messages than the `turns` limit the cutoff
// resolves to sort_order 0 so pre-user content (e.g. the transcript body of
// an auto-created meeting chat, agent-produced preamble) stays visible when
// the user opens the session. Without this, the first assistant-only stretch
// of a short session disappears on reload as soon as a single user message
// anchors the cutoff mid-conversation.
func (s *Store) LoadRecentStreamItemsByTurns(ctx context.Context, turns int) (map[string][]StreamItemRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH turn_cutoffs AS (
			SELECT session_local_id,
				CASE WHEN COUNT(*) < ? THEN 0 ELSE MIN(sort_order) END AS cutoff
			FROM (
				SELECT session_local_id, sort_order,
					ROW_NUMBER() OVER (PARTITION BY session_local_id ORDER BY sort_order DESC) AS rn
				FROM stream_items
				WHERE kind = 'user'
			)
			WHERE rn <= ?
			GROUP BY session_local_id
		),
		fallback AS (
			SELECT session_local_id, MIN(sort_order) AS cutoff
			FROM (
				SELECT session_local_id, sort_order,
					ROW_NUMBER() OVER (PARTITION BY session_local_id ORDER BY sort_order DESC) AS rn
				FROM stream_items
				WHERE session_local_id NOT IN (SELECT session_local_id FROM turn_cutoffs)
			)
			WHERE rn <= 50
			GROUP BY session_local_id
		),
		all_cutoffs AS (
			SELECT * FROM turn_cutoffs
			UNION ALL
			SELECT * FROM fallback
		)
		SELECT si.id, si.session_local_id, si.kind, si.content, si.tool_name,
			si.tool_input, si.tool_use_id, si.run_id, si.session_id, si.sort_order, si.created_at, si.images, si.automation_context, si.files
		FROM stream_items si
		INNER JOIN all_cutoffs ac ON si.session_local_id = ac.session_local_id
		WHERE si.sort_order >= ac.cutoff
		ORDER BY si.session_local_id, si.sort_order ASC
	`, turns, turns)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]StreamItemRow)
	for rows.Next() {
		var item StreamItemRow
		if err := rows.Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files); err != nil {
			return nil, err
		}
		result[item.SessionLocalID] = append(result[item.SessionLocalID], item)
	}
	return result, rows.Err()
}

// LoadStreamItem returns one stream item by id, or nil when there is none.
func (s *Store) LoadStreamItem(ctx context.Context, id string) (*StreamItemRow, error) {
	var item StreamItemRow
	err := s.db.QueryRowContext(ctx, `
		SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
		FROM stream_items WHERE id = ?
	`, id).Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// UpdateStreamItemToolInput replaces the tool_input field of a stream item
// (a card that records its own answer).
func (s *Store) UpdateStreamItemToolInput(ctx context.Context, id, toolInput string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE stream_items SET tool_input = ? WHERE id = ?", toolInput, id)
	return err
}

// UpdateStreamItemPresentation replaces both representations of a folded
// status row atomically, so old and structured clients see the same update.
func (s *Store) UpdateStreamItemPresentation(ctx context.Context, id, content, toolInput string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE stream_items SET content = ?, tool_input = ? WHERE id = ?", content, toolInput, id)
	return err
}

// MoveStreamItemPresentationToEnd updates a folded status row and gives it the
// next sequence atomically. Its stable id stays unchanged; clients reading
// after the old cursor receive the updated row. The parent session timestamp
// advances in the same transaction. A missing row returns nil, nil.
func (s *Store) MoveStreamItemPresentationToEnd(ctx context.Context, id, content, toolInput string, at time.Time) (*StreamItemRow, error) {
	if at.IsZero() {
		at = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := scanStreamItemRow(tx.QueryRowContext(ctx, `
		UPDATE stream_items
		SET content = ?, tool_input = ?, created_at = ?,
			sort_order = (SELECT COALESCE(MAX(sort_order), -1) + 1 FROM stream_items
				WHERE session_local_id = (SELECT session_local_id FROM stream_items WHERE id = ?))
		WHERE id = ?
		RETURNING id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
	`, content, toolInput, at.UnixMilli(), id, id))
	if err != nil || item == nil {
		return item, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET updated_at = MAX(updated_at, ?) WHERE local_id = ?", at.UnixMilli(), item.SessionLocalID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// UpdateStreamItemContent sets the content field of a stream item as given
// (an edited prompt, a repaired transcript line).
func (s *Store) UpdateStreamItemContent(ctx context.Context, id, content string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE stream_items SET content = ? WHERE id = ?", content, id)
	return err
}

// GrowStreamItemContent sets the content of a chat's streamed row, but never
// makes an assistant or thinking row shorter: more than one renderer writes a
// reply's text as it grows, and one that missed chunks (it joined mid-reply,
// or the bridge dropped events for it) must not cut down what another stored.
// Lengths are compared in bytes, which a NUL in the text cannot cut short. It
// reports whether the row was written.
func (s *Store) GrowStreamItemContent(ctx context.Context, sessionLocalID, id, content string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE stream_items SET content = ?
		WHERE id = ? AND session_local_id = ?
		  AND (kind NOT IN ('assistant', 'thinking') OR length(CAST(content AS BLOB)) <= length(CAST(? AS BLOB)))
	`, content, id, sessionLocalID, content)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteStreamItems removes all items for a session.
func (s *Store) DeleteStreamItems(ctx context.Context, sessionLocalID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM stream_items WHERE session_local_id = ?", sessionLocalID)
	return err
}

// DeleteStreamItemsFrom removes items with sort_order >= fromSortOrder for a session.
// Used by the rewind feature to truncate conversation history from a specific point.
func (s *Store) DeleteStreamItemsFrom(ctx context.Context, sessionLocalID string, fromSortOrder int) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM stream_items WHERE session_local_id = ? AND sort_order >= ?", sessionLocalID, fromSortOrder)
	return err
}

// AppendUserStreamItemAtomic publishes an admitted user message and its sort
// order together. Cancellation before commit cannot expose a provisional row
// to forward-only readers such as the phone chat cursor.
func (s *Store) AppendUserStreamItemAtomic(ctx context.Context, sessionLocalID string, item StreamItemRow) (int, error) {
	return s.appendUserStreamItemAtomic(ctx, sessionLocalID, item, nil, nil)
}

// AppendUserStreamItemAtomicIfCurrent rejects changes since the source read and
// validates non-database sources immediately before commit. validate must not
// query this Store: its sole connection belongs to the publication transaction.
func (s *Store) AppendUserStreamItemAtomicIfCurrent(ctx context.Context, sessionLocalID string, item StreamItemRow, revision ReadRevision, validate func() error) (int, error) {
	release, err := s.lockContextUseRevision(ctx)
	if err != nil {
		return -1, err
	}
	defer release()
	revision.changes += s.contextUses.changes
	return s.appendUserStreamItemAtomic(ctx, sessionLocalID, item, &revision, validate)
}

func (s *Store) appendUserStreamItemAtomic(ctx context.Context, sessionLocalID string, item StreamItemRow, revision *ReadRevision, validate func() error) (int, error) {
	if item.ID == "" || item.Kind != "user" {
		return -1, errors.New("user stream item requires an ID and user kind")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return -1, err
	}
	defer tx.Rollback()
	if revision != nil {
		// BeginTx is deferred in SQLite. Reserve the writer before reading its
		// stamp, without changing a row or advancing total_changes(). Even an
		// external writer must finish before this point or wait until commit.
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET updated_at=updated_at WHERE 0"); err != nil {
			return -1, err
		}
		current, err := DatabaseRevision(ctx, tx)
		if err != nil {
			return -1, err
		}
		if current != *revision {
			return -1, ErrReadRevisionChanged
		}
	}
	now := time.Now().UnixMilli()
	var order int
	err = tx.QueryRowContext(ctx, `INSERT INTO stream_items
		(id, session_local_id, kind, content, sort_order, created_at, images, files, automation_context)
        VALUES (?, ?, 'user', ?, (SELECT COALESCE(MAX(sort_order), -1) + 1 FROM stream_items WHERE session_local_id = ?), ?, ?, ?, ?)
		RETURNING sort_order`, item.ID, sessionLocalID, item.Content, sessionLocalID, now, item.Images, item.Files, item.AutomationContext).Scan(&order)
	if err != nil {
		return -1, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET updated_at = ? WHERE local_id = ?", now, sessionLocalID); err != nil {
		return -1, err
	}
	if item.TopicID != "" {
		tagged, err := tx.ExecContext(ctx, `INSERT INTO stream_item_topics (stream_item_id, session_id, topic_id, created_at)
			SELECT id, session_local_id, ?, created_at FROM stream_items WHERE id = ?
			AND session_local_id IN (SELECT local_id FROM sessions WHERE kind = 'main')`, item.TopicID, item.ID)
		if err != nil {
			return -1, err
		}
		n, err := tagged.RowsAffected()
		if err != nil {
			return -1, err
		}
		if n != 1 {
			return -1, errors.New("TOPIC_REQUIRES_MAIN_CHAT")
		}
	}
	if validate != nil {
		if err := validate(); err != nil {
			return -1, err
		}
	}
	if err := tx.Commit(); err != nil {
		return -1, err
	}
	return order, nil
}

// ReplaceUserTurn atomically updates the identified user message and removes
// later rows. A stale or foreign item must never truncate a conversation.
func (s *Store) ReplaceUserTurn(ctx context.Context, sessionID, itemID string, sortOrder int, content string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE stream_items SET content = ? WHERE session_local_id = ? AND id = ? AND sort_order = ? AND kind = 'user'`, content, sessionID, itemID, sortOrder)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("user message is no longer available")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stream_items WHERE session_local_id = ? AND sort_order > ?`, sessionID, sortOrder); err != nil {
		return err
	}
	return tx.Commit()
}
