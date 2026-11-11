package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// stream_item_versions holds one counter per session. Triggers on stream_items
// bump it on every insert, update and delete, so any write to a session's
// items (appends, streaming flushes, edits, withdrawals and removals) changes
// the version. A saved page is only served while the version it was read at is
// still current. The triggers run on every start, like the other additive
// structures, so a sibling build's migrations cannot leave them out.
func (s *Store) ensureStreamItemVersions() error {
	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS stream_item_versions (
			session_local_id TEXT PRIMARY KEY,
			version          INTEGER NOT NULL DEFAULT 0
		);
		CREATE TRIGGER IF NOT EXISTS stream_items_version_insert AFTER INSERT ON stream_items
		BEGIN
			INSERT INTO stream_item_versions(session_local_id, version) VALUES (NEW.session_local_id, 1)
			ON CONFLICT(session_local_id) DO UPDATE SET version = version + 1;
		END;
		CREATE TRIGGER IF NOT EXISTS stream_items_version_update AFTER UPDATE ON stream_items
		BEGIN
			INSERT INTO stream_item_versions(session_local_id, version) VALUES (NEW.session_local_id, 1)
			ON CONFLICT(session_local_id) DO UPDATE SET version = version + 1;
		END;
		CREATE TRIGGER IF NOT EXISTS stream_items_version_delete AFTER DELETE ON stream_items
		BEGIN
			INSERT INTO stream_item_versions(session_local_id, version) VALUES (OLD.session_local_id, 1)
			ON CONFLICT(session_local_id) DO UPDATE SET version = version + 1;
		END;
	`); err != nil {
		return fmt.Errorf("stream item versions: %w", err)
	}
	return nil
}

// StreamItemVersion returns the session's current item version: one primary-key
// lookup. A session with no items yet is version 0.
func (s *Store) StreamItemVersion(ctx context.Context, sessionLocalID string) (int64, error) {
	var version int64
	err := s.streamReader().QueryRowContext(ctx, `SELECT version FROM stream_item_versions WHERE session_local_id = ?`, sessionLocalID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return version, err
}
