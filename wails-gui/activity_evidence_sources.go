package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sort"
)

// Source revisions are metadata only. Updating an OCR row in place or replacing
// an event invalidates collection without loading its text on every pass.
func migrateActivityEvidenceSourceRevisions(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_evidence_source_revisions (
			session_id TEXT PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS activity_run_evidence_state (
			run_id TEXT PRIMARY KEY REFERENCES activity_timeline_groups(id) ON DELETE CASCADE,
			source_fingerprint TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS activity_outcome_observations (
			activity_id TEXT NOT NULL REFERENCES activities(id) ON DELETE CASCADE,
			run_id TEXT NOT NULL REFERENCES activity_timeline_groups(id) ON DELETE CASCADE,
			source_fingerprint TEXT NOT NULL,
			observed_until_ms INTEGER NOT NULL,
			PRIMARY KEY(activity_id,run_id)
		)`); err != nil {
		return err
	}
	for _, source := range []struct{ table, columns string }{
		{"activity_events", "session_id,timestamp,kind,data"},
		{"activity_snapshots", "session_id,timestamp,app,ax_text,browser_url,parsed_json"},
		{"activity_ocr_frames", "session_id,segment_id,text"},
	} {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			row := "NEW"
			clause := operation
			previousSession := ""
			switch operation {
			case "UPDATE":
				clause += " OF " + source.columns
				previousSession = `INSERT INTO activity_evidence_source_revisions(session_id,revision)
					SELECT OLD.session_id,1 WHERE OLD.session_id IS NOT NULL AND OLD.session_id IS NOT NEW.session_id
					ON CONFLICT(session_id) DO UPDATE SET revision=revision+1;`
			case "DELETE":
				row = "OLD"
			}
			guard := row + ".session_id IS NOT NULL"
			if operation == "UPDATE" {
				guard += " OR OLD.session_id IS NOT NULL"
			}
			statement := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS evidence_revision_%s_%s
				AFTER %s ON %s WHEN %s BEGIN
				INSERT INTO activity_evidence_source_revisions(session_id,revision) SELECT %s.session_id,1 WHERE %s.session_id IS NOT NULL
				ON CONFLICT(session_id) DO UPDATE SET revision=revision+1;
				%s END`, source.table, operation, clause, source.table, guard, row, row, previousSession)
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *ActivityStore) timelineRunEvidenceFingerprint(run ActivityTimelineRunRow) (string, error) {
	hash := sha256.New()
	fmt.Fprintf(hash, "codes-only-bounded-measurements-v3:%s:%s:%s:%d:%d:%d:%d:%d:%t", run.ID, run.AppKey, run.AppName,
		run.FirstEventRowID, run.LastEventRowID, run.StartMs, run.EndMs, run.EventCount,
		activityEvidenceAppExcluded(run.AppKey, run.AppName))
	sessions := append([]string(nil), run.SessionIDs...)
	sort.Strings(sessions)
	for _, sessionID := range sessions {
		var revision int64
		if err := s.db.QueryRow(`SELECT COALESCE((SELECT revision FROM activity_evidence_source_revisions WHERE session_id=?),0)`, sessionID).Scan(&revision); err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "\x00%s:%d:%t", sessionID, revision, s.isSessionProtected(sessionID))
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// Upgrade existing activity databases as well as new installs. The original
// triggers wrote a new NULL primary key for each unowned staging operation.
func migrateOwnedSnapshotRevisions(tx *sql.Tx) error {
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS evidence_revision_activity_snapshots_INSERT;
        DROP TRIGGER IF EXISTS evidence_revision_activity_snapshots_UPDATE;
        DROP TRIGGER IF EXISTS evidence_revision_activity_snapshots_DELETE;
        DELETE FROM activity_evidence_source_revisions WHERE session_id IS NULL`); err != nil {
		return err
	}
	return migrateActivityEvidenceSourceRevisions(tx)
}
