package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (s *Store) migrateStreamItemChips() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS stream_item_chips (
        stream_item_id TEXT PRIMARY KEY REFERENCES stream_items(id) ON DELETE CASCADE,
        session_id TEXT NOT NULL REFERENCES sessions(local_id) ON DELETE CASCADE,
        run_id TEXT NOT NULL DEFAULT '',
        chips_json TEXT NOT NULL,
        language TEXT NOT NULL,
        created_at INTEGER NOT NULL
    );
    CREATE INDEX IF NOT EXISTS idx_stream_item_chips_created ON stream_item_chips(created_at);
    CREATE TRIGGER IF NOT EXISTS stream_items_chips_move AFTER UPDATE OF session_local_id ON stream_items
        WHEN OLD.session_local_id != NEW.session_local_id
        BEGIN DELETE FROM stream_item_chips WHERE stream_item_id=NEW.id; END;
    CREATE TRIGGER IF NOT EXISTS stream_items_chips_change AFTER UPDATE OF content,run_id,kind,tool_name ON stream_items
        WHEN OLD.content != NEW.content OR OLD.run_id != NEW.run_id OR OLD.kind != NEW.kind OR OLD.tool_name != NEW.tool_name
        BEGIN DELETE FROM stream_item_chips WHERE stream_item_id=NEW.id; END;`)
	return err
}

// BindAnswerChipUserRun binds an ordinary admitted send before run_created.
// Topic sends use their existing binding, including routed fallback updates.
func (s *Store) BindAnswerChipUserRun(ctx context.Context, sessionID, itemID, runID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE stream_items SET run_id=?
		WHERE id=? AND session_local_id=? AND kind='user' AND tool_name=''
        AND EXISTS (SELECT 1 FROM sessions WHERE local_id=? AND kind='main')`, runID, itemID, sessionID, sessionID)
	return err
}

// ReserveAnswerChips records an empty attempt before dispatch. The transaction
// enforces single flight and the rolling hourly cap, including after restart.
// A failed call leaves the empty result; reads never trigger generation.
func (s *Store) ReserveAnswerChips(ctx context.Context, answer StreamItemRow, language string, now int64) (bool, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	// Identifier-free timestamps survive row deletion, so deleting/rewinding
	// answers cannot reset spending. Keep only the rolling hour, at most 60.
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM ui_state WHERE key='main_chat_chip_attempts'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, 0, err
	}
	attempts := []int64{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &attempts); err != nil {
			return false, 0, err
		}
	}
	kept := []int64{}
	for _, at := range attempts {
		if at > now-time.Hour.Milliseconds() {
			kept = append(kept, at)
		}
	}
	count := len(kept)
	if count >= 60 {
		return false, count, nil
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO stream_item_chips(stream_item_id,session_id,run_id,chips_json,language,created_at)
        SELECT id,session_local_id,run_id,'[]',?,? FROM stream_items
        WHERE id=? AND session_local_id=? AND run_id=? AND content=? AND kind='assistant' AND tool_name=''
        AND EXISTS (SELECT 1 FROM sessions WHERE local_id=? AND kind='main')
        ON CONFLICT(stream_item_id) DO NOTHING`, language, now, answer.ID, answer.SessionLocalID, answer.RunID, answer.Content, answer.SessionLocalID)
	if err != nil {
		return false, count, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, count, err
	}
	if n == 1 {
		budget, err := json.Marshal(append(kept, now))
		if err != nil {
			return false, count, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ui_state(key,value) VALUES('main_chat_chip_attempts',?)
            ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(budget)); err != nil {
			return false, count, err
		}
	}
	return n == 1, count, tx.Commit()
}

// CompleteAnswerChips never recreates a deleted/moved/edited answer or attempt.
func (s *Store) CompleteAnswerChips(ctx context.Context, answer StreamItemRow, chips []string) (bool, error) {
	if chips == nil {
		chips = []string{}
	}
	raw, err := json.Marshal(chips)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE stream_item_chips SET chips_json=?
        WHERE stream_item_id=? AND session_id=? AND run_id=?
        AND EXISTS (SELECT 1 FROM stream_items i JOIN sessions s ON s.local_id=i.session_local_id
            WHERE i.id=stream_item_chips.stream_item_id AND i.session_local_id=stream_item_chips.session_id AND i.run_id=stream_item_chips.run_id
                AND i.kind='assistant' AND i.tool_name='' AND i.content=? AND s.kind='main')`, string(raw), answer.ID, answer.SessionLocalID, answer.RunID, answer.Content)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ListMainChatAnswerChips returns only existing assistant rows in the main chat.
func (s *Store) ListMainChatAnswerChips(ctx context.Context, ids []string) (map[string][]string, error) {
	result := map[string][]string{}
	if len(ids) > 100 {
		ids = ids[:100]
	}
	if len(ids) == 0 {
		return result, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := s.committedReader().QueryContext(ctx, `SELECT c.stream_item_id,c.chips_json FROM stream_item_chips c
        JOIN stream_items i ON i.id=c.stream_item_id AND i.session_local_id=c.session_id AND i.run_id=c.run_id
        JOIN sessions s ON s.local_id=c.session_id
        WHERE s.kind='main' AND i.kind='assistant' AND i.tool_name='' AND c.stream_item_id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		chips := []string{}
		if err := json.Unmarshal([]byte(raw), &chips); err != nil {
			return nil, err
		}
		result[id] = chips
	}
	return result, rows.Err()
}
