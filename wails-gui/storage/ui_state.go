package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"
)

// UIStateOnWriter puts GetUIState back on the single writer connection. It
// is the rollback switch for moving UI-state reads to the read pool
// (2026-11-21): ISOFORM_UISTATE_ON_WRITER=1 at launch, no rebuild.
var UIStateOnWriter = os.Getenv("ISOFORM_UISTATE_ON_WRITER") == "1"

// PostUIStateAnswer records an explicit answer and its main-chat row together.
// The previous value is compared in the same transaction to reject stale edits.
func (s *Store) PostUIStateAnswer(ctx context.Context, key, previous, answer, sessionID, itemID string) (int, error) {
	return s.postUIStateMessage(ctx, key, previous, answer, answer, sessionID, itemID, "", nil, nil)
}

// PostUIStateAnswerWithContext keeps the origin of an explicit answer in the
// same transaction as the answer and chat row.
func (s *Store) PostUIStateAnswerWithContext(ctx context.Context, key, previous, answer, sessionID, itemID, contextJSON string) (int, error) {
	return s.postUIStateMessage(ctx, key, previous, answer, answer, sessionID, itemID, contextJSON, nil, nil)
}

// PostUIStateMessageIfCurrent publishes a state choice and its message as
// one commit, only while the source read remains current.
func (s *Store) PostUIStateMessageIfCurrent(ctx context.Context, key, previous, value, message, sessionID, itemID string, revision ReadRevision, validate func() error) (int, error) {
	return s.postUIStateMessage(ctx, key, previous, value, message, sessionID, itemID, "", &revision, validate)
}

func (s *Store) postUIStateMessage(ctx context.Context, key, previous, value, message, sessionID, itemID, contextJSON string, revision *ReadRevision, validate func() error) (int, error) {
	if key == "" || value == "" || message == "" || sessionID == "" || itemID == "" {
		return -1, errors.New("invalid answer")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return -1, err
	}
	defer tx.Rollback()
	// Reserve the SQLite writer before comparing, including when the key is new.
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET updated_at=updated_at WHERE 0"); err != nil {
		return -1, err
	}
	if revision != nil {
		current, err := DatabaseRevision(ctx, tx)
		if err != nil {
			return -1, err
		}
		if current != *revision {
			return -1, ErrReadRevisionChanged
		}
	}
	var current string
	err = tx.QueryRowContext(ctx, "SELECT value FROM ui_state WHERE key=?", key).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return -1, err
	}
	if current != previous {
		return -1, errors.New("answer changed")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ui_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
		return -1, err
	}
	now := time.Now().UnixMilli()
	var order int
	err = tx.QueryRowContext(ctx, `INSERT INTO stream_items(id,session_local_id,kind,content,sort_order,created_at,automation_context)
		SELECT ?,local_id,'user',?,(SELECT COALESCE(MAX(sort_order),-1)+1 FROM stream_items WHERE session_local_id=?),?,?
		FROM sessions WHERE local_id=? AND kind='main' RETURNING sort_order`, itemID, message, sessionID, now, contextJSON, sessionID).Scan(&order)
	if err != nil {
		return -1, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET updated_at=? WHERE local_id=?", now, sessionID); err != nil {
		return -1, err
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

// DeleteUIState removes a retired setting, including its stored value.
func (s *Store) DeleteUIState(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM ui_state WHERE key = ?", key)
	return err
}

// SetUIState sets a key-value pair in the ui_state table.
func (s *Store) SetUIState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ui_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// ReserveUIStateValue stores value only when key is absent and returns the
// existing or newly stored value. The insert is atomic across app processes.
func (s *Store) ReserveUIStateValue(ctx context.Context, key, value string) (string, error) {
	if err := s.SetUIStateIfAbsent(ctx, key, value); err != nil {
		return "", err
	}
	return s.GetUIState(ctx, key)
}

// SetUIStateIfAbsent preserves a choice saved concurrently with a migration.
func (s *Store) SetUIStateIfAbsent(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ui_state(key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, key, value)
	return err
}

// GetUIState retrieves a value from the ui_state table. Returns empty string if not found.
//
// It reads committed state on the read pool, never queuing behind the single
// writer connection that background jobs hold for long writes: a settings
// page that read through it stayed empty while a job ran (2026-11-21, 61
// bindings). SetUIState commits at once, so a read after it sees the new
// value. No caller can read inside its own open write on one connection.
func (s *Store) GetUIState(ctx context.Context, key string) (string, error) {
	reader := s.streamReader()
	if UIStateOnWriter {
		reader = s.db
	}
	var value string
	err := reader.QueryRowContext(ctx, "SELECT value FROM ui_state WHERE key = ?", key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}

// GetStreamPageUIState reads committed display state without borrowing the
// writer connection used by background jobs.
func (s *Store) GetStreamPageUIState(ctx context.Context, key string) (string, error) {
	value, _, err := s.LookupUIState(ctx, key)
	return value, err
}

// LookupUIState distinguishes a saved empty string from an absent preference.
func (s *Store) LookupUIState(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.streamReader().QueryRowContext(ctx, "SELECT value FROM ui_state WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// PruneWatchPairingReports removes reports belonging to phones no longer paired.
func (s *Store) PruneWatchPairingReports(ctx context.Context, pairedIDs []string) error {
	ids, err := json.Marshal(pairedIDs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM ui_state WHERE key GLOB 'watch_paired_*'
        AND key NOT IN (SELECT 'watch_paired_' || value FROM json_each(?) WHERE type='text')`, string(ids))
	return err
}
