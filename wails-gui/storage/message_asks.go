package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// MessageAskRetention is the longest an ask or a commitment read from
// Messages is kept; a shorter activity retention shortens it.
const MessageAskRetention = 14 * 24 * time.Hour

// Kinds of a MessageAskRow.
const (
	// MessageAskAsked is someone asking the user for something.
	MessageAskAsked = "asked"
	// MessageAskCommitted is the user promising someone something.
	MessageAskCommitted = "committed"
)

// MessageAskRow is one ask or commitment that involves the user, read from
// a Messages conversation and written in Isoform's own words. Never a
// message's text: Summary and NextStep are paraphrases checked against the
// messages they came from, which are not kept. ChatKey is a hash of the
// conversation's id. At is the time of the latest message it rests on, Unix
// milliseconds.
type MessageAskRow struct {
	ID          string
	ChatKey     string
	PrincipalID string
	PersonName  string
	Kind        string
	Summary     string
	NextStep    string
	Action      string
	At          int64
	CreatedAt   int64
}

const messageAskColumns = `id, chat_key, principal_id, person_name, kind, summary, next_step, action, at, created_at`

func migrateMessageAsks(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS message_asks (
			id           TEXT PRIMARY KEY,
			chat_key     TEXT    NOT NULL,
			principal_id TEXT    NOT NULL DEFAULT '',
			person_name  TEXT    NOT NULL DEFAULT '',
			kind         TEXT    NOT NULL CHECK (kind IN ('asked', 'committed')),
			summary      TEXT    NOT NULL,
			next_step    TEXT    NOT NULL DEFAULT '',
			action       TEXT    NOT NULL DEFAULT '',
			at           INTEGER NOT NULL,
			created_at   INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_message_asks_at ON message_asks(at);
		CREATE INDEX IF NOT EXISTS idx_message_asks_principal ON message_asks(principal_id);
	`)
	return err
}

// UpsertMessageAsks writes asks; one already kept keeps its created_at.
func (s *Store) UpsertMessageAsks(ctx context.Context, rows []MessageAskRow, now int64) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	for _, row := range rows {
		if strings.TrimSpace(row.ID) == "" || strings.TrimSpace(row.Summary) == "" || row.At <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO message_asks (`+messageAskColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				principal_id = excluded.principal_id,
				person_name  = excluded.person_name,
				next_step    = excluded.next_step,
				action       = excluded.action,
				at           = MAX(message_asks.at, excluded.at)`,
			row.ID, row.ChatKey, row.PrincipalID, row.PersonName, row.Kind, row.Summary, row.NextStep, row.Action, row.At, now); err != nil {
			return fmt.Errorf("upsert message ask: %w", err)
		}
	}
	return tx.Commit()
}

// ListMessageAsks returns the asks at or after since, newest first.
func (s *Store) ListMessageAsks(ctx context.Context, since int64) ([]MessageAskRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageAskColumns+` FROM message_asks WHERE at >= ? ORDER BY at DESC, id ASC`, since)
	if err != nil {
		return nil, fmt.Errorf("list message asks: %w", err)
	}
	defer rows.Close()
	var out []MessageAskRow
	for rows.Next() {
		var row MessageAskRow
		if err := rows.Scan(&row.ID, &row.ChatKey, &row.PrincipalID, &row.PersonName, &row.Kind, &row.Summary,
			&row.NextStep, &row.Action, &row.At, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetMessageAsk returns one ask, or found false.
func (s *Store) GetMessageAsk(ctx context.Context, id string) (MessageAskRow, bool, error) {
	var row MessageAskRow
	err := s.db.QueryRowContext(ctx, `SELECT `+messageAskColumns+` FROM message_asks WHERE id = ?`, id).Scan(
		&row.ID, &row.ChatKey, &row.PrincipalID, &row.PersonName, &row.Kind, &row.Summary,
		&row.NextStep, &row.Action, &row.At, &row.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return row, false, nil
		}
		return row, false, err
	}
	return row, true, nil
}

// DeleteMessageAsksOf deletes what was kept from the given people.
func (s *Store) DeleteMessageAsksOf(ctx context.Context, principalIDs []string) (int64, error) {
	var removed int64
	for _, id := range principalIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		res, err := s.db.ExecContext(ctx, `DELETE FROM message_asks WHERE principal_id = ?`, id)
		if err != nil {
			return removed, fmt.Errorf("delete message asks: %w", err)
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	return removed, nil
}

// DeleteMessageAsksInChats deletes what was kept from the given
// conversations.
func (s *Store) DeleteMessageAsksInChats(ctx context.Context, chatKeys []string) (int64, error) {
	var removed int64
	for _, key := range chatKeys {
		res, err := s.db.ExecContext(ctx, `DELETE FROM message_asks WHERE chat_key = ?`, key)
		if err != nil {
			return removed, fmt.Errorf("delete message asks: %w", err)
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	return removed, nil
}

// PruneMessageAsks deletes the asks before the cutoff.
func (s *Store) PruneMessageAsks(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM message_asks WHERE at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("prune message asks: %w", err)
	}
	return res.RowsAffected()
}

// DeleteAllMessageAsks empties the table: Messages protected, or the user
// asked Isoform to forget what it read there.
func (s *Store) DeleteAllMessageAsks(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM message_asks`)
	if err != nil {
		return 0, fmt.Errorf("clear message asks: %w", err)
	}
	return res.RowsAffected()
}

// MessageAsksSize is how many notes of who asked what are kept, and the
// bytes their words take (Settings › Your data). It reads lengths only.
func (s *Store) MessageAsksSize(ctx context.Context) (count int, bytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(LENGTH(summary) + LENGTH(next_step) + LENGTH(action) + LENGTH(person_name)), 0) FROM message_asks`).Scan(&count, &bytes)
	return count, bytes, err
}
