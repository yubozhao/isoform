package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// voice_source_version is one counter that triggers bump on every write to the
// tables a proactive card's read gate reloads: people and their app handles,
// mail, calendar events, reminders, and the source permission switches
// (ui_state keys "permission_enabled:..."), and edits or removals of the cards'
// receipts. A cached read verdict is kept only
// while the counter it was reached under is current, so any change to those
// sources, from any writer (a sync, a permission switch, an origin rewritten),
// is seen on the next read. The triggers run on every start, like the other
// additive structures.
var voiceSourceTables = []string{"principals", "app_handles", "mail_messages", "calendar_events", "reminders"}

func (s *Store) ensureVoiceSourceVersion() error {
	var sb strings.Builder
	sb.WriteString(`
		CREATE TABLE IF NOT EXISTS voice_source_version (
			id      INTEGER PRIMARY KEY CHECK (id = 1),
			version INTEGER NOT NULL DEFAULT 0
		);
		INSERT OR IGNORE INTO voice_source_version(id, version) VALUES (1, 0);
	`)
	bump := `UPDATE voice_source_version SET version = version + 1 WHERE id = 1;`
	for _, table := range voiceSourceTables {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			fmt.Fprintf(&sb, "CREATE TRIGGER IF NOT EXISTS %s_voice_source_%s AFTER %s ON %s BEGIN %s END;\n", table, strings.ToLower(op), op, table, bump)
		}
	}
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		row := "NEW"
		if op == "DELETE" {
			row = "OLD"
		}
		fmt.Fprintf(&sb, "CREATE TRIGGER IF NOT EXISTS ui_state_voice_source_%s AFTER %s ON ui_state WHEN %s.key LIKE 'permission_enabled:%%' BEGIN %s END;\n", strings.ToLower(op), op, row, bump)
	}
	// A card's receipt (its local provenance) edited or removed changes what
	// its gate allows. A new receipt comes with a new card, which the stream
	// version already names, so inserts are left out.
	for _, op := range []string{"UPDATE", "DELETE"} {
		fmt.Fprintf(&sb, "CREATE TRIGGER IF NOT EXISTS ui_state_voice_receipt_%s AFTER %s ON ui_state WHEN OLD.key LIKE 'proactive_voice_provenance:%%' OR OLD.key LIKE 'engagement_journey_provenance:%%' BEGIN %s END;\n", strings.ToLower(op), op, bump)
	}
	// voice_source_revocation counts only the writes that can take a shown
	// card away: a source row removed, a person edited, a permission switch
	// changed, a receipt edited or removed. A sync adding or updating mail,
	// calendar or reminder rows does not count. The saved first chat page
	// follows this counter: it paints at once and its cards are confirmed by
	// the live gates right after, so a sync must not throw the copy away
	// (2026-11-15: every launch recomputed it, 1.1 to 4.1 s).
	sb.WriteString(`
		CREATE TABLE IF NOT EXISTS voice_source_revocation (
			id      INTEGER PRIMARY KEY CHECK (id = 1),
			version INTEGER NOT NULL DEFAULT 0
		);
		INSERT OR IGNORE INTO voice_source_revocation(id, version) VALUES (1, 0);
	`)
	revoke := `UPDATE voice_source_revocation SET version = version + 1 WHERE id = 1;`
	for _, table := range voiceSourceTables {
		fmt.Fprintf(&sb, "CREATE TRIGGER IF NOT EXISTS %s_voice_revocation_delete AFTER DELETE ON %s BEGIN %s END;\n", table, table, revoke)
	}
	fmt.Fprintf(&sb, "CREATE TRIGGER IF NOT EXISTS principals_voice_revocation_update AFTER UPDATE ON principals BEGIN %s END;\n", revoke)
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		row := "NEW"
		if op == "DELETE" {
			row = "OLD"
		}
		fmt.Fprintf(&sb, "CREATE TRIGGER IF NOT EXISTS ui_state_voice_revocation_%s AFTER %s ON ui_state WHEN %s.key LIKE 'permission_enabled:%%' BEGIN %s END;\n", strings.ToLower(op), op, row, revoke)
	}
	for _, op := range []string{"UPDATE", "DELETE"} {
		fmt.Fprintf(&sb, "CREATE TRIGGER IF NOT EXISTS ui_state_voice_receipt_revocation_%s AFTER %s ON ui_state WHEN OLD.key LIKE 'proactive_voice_provenance:%%' OR OLD.key LIKE 'engagement_journey_provenance:%%' BEGIN %s END;\n", strings.ToLower(op), op, revoke)
	}
	if _, err := s.db.Exec(sb.String()); err != nil {
		return fmt.Errorf("voice source version: %w", err)
	}
	return nil
}

// VoiceSourceRevocationVersion returns the revocation counter: one
// primary-key lookup.
func (s *Store) VoiceSourceRevocationVersion(ctx context.Context) (int64, error) {
	var version int64
	err := s.streamReader().QueryRowContext(ctx, `SELECT version FROM voice_source_revocation WHERE id = 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return version, err
}

// VoiceSourceVersion returns the counter: one primary-key lookup.
func (s *Store) VoiceSourceVersion(ctx context.Context) (int64, error) {
	var version int64
	err := s.streamReader().QueryRowContext(ctx, `SELECT version FROM voice_source_version WHERE id = 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return version, err
}
