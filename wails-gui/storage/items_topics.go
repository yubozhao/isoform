package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// The same source proof follows every persisted run row. Keeping it on replies
// prevents deleting the user bubble from erasing a response's read dependency.
const topicRunProofSource = `SELECT json_extract(source.automation_context, '$.topicContext')
    FROM stream_items source JOIN stream_item_topics tags ON tags.stream_item_id=source.id
        AND tags.session_id=source.session_local_id
    WHERE source.session_local_id=NEW.session_local_id AND source.run_id=NEW.run_id
        AND json_type(CASE WHEN json_valid(source.automation_context) THEN source.automation_context ELSE '{}' END, '$.topicContext')='object'
    LIMIT 1`

const inheritTopicRunProof = `
    UPDATE stream_items SET automation_context=json_set(
        CASE WHEN json_valid(automation_context) THEN CASE WHEN json_type(automation_context)='object' THEN automation_context ELSE '{}' END ELSE '{}' END,
        '$.topicContext', json((` + topicRunProofSource + `)))
    WHERE session_local_id=NEW.session_local_id AND run_id=NEW.run_id AND kind!='user'
        AND EXISTS (` + topicRunProofSource + `);`

func (s *Store) migrateStreamItemTopics() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
        CREATE TABLE IF NOT EXISTS stream_item_topics (
            stream_item_id TEXT NOT NULL REFERENCES stream_items(id) ON DELETE CASCADE,
            session_id TEXT NOT NULL REFERENCES sessions(local_id) ON DELETE CASCADE,
            topic_id TEXT NOT NULL,
            created_at INTEGER NOT NULL,
            PRIMARY KEY (stream_item_id, topic_id)
        );
        CREATE INDEX IF NOT EXISTS idx_stream_item_topics_topic ON stream_item_topics(session_id, topic_id, created_at, stream_item_id);
        CREATE INDEX IF NOT EXISTS idx_stream_items_topic_run ON stream_items(session_local_id, run_id) WHERE run_id != '';
    `); err != nil {
		return err
	}
	triggers := []struct{ name, statement string }{
		{"stream_items_topic_move", `CREATE TRIGGER stream_items_topic_move AFTER UPDATE OF session_local_id ON stream_items
            WHEN OLD.session_local_id != NEW.session_local_id
            BEGIN DELETE FROM stream_item_topics WHERE stream_item_id=NEW.id; END`},
		{"stream_items_topic_insert", `CREATE TRIGGER stream_items_topic_insert AFTER INSERT ON stream_items
            WHEN NEW.run_id!='' AND NEW.kind!='user'
            BEGIN
                INSERT INTO stream_item_topics(stream_item_id,session_id,topic_id,created_at)
                SELECT NEW.id,NEW.session_local_id,tags.topic_id,NEW.created_at
                FROM stream_items source JOIN stream_item_topics tags ON tags.stream_item_id=source.id AND tags.session_id=source.session_local_id
                WHERE source.session_local_id=NEW.session_local_id AND source.run_id=NEW.run_id
                ON CONFLICT(stream_item_id,topic_id) DO NOTHING;
                ` + inheritTopicRunProof + ` END`},
		{"stream_items_topic_run_update", `CREATE TRIGGER stream_items_topic_run_update AFTER UPDATE OF run_id ON stream_items
            WHEN NEW.run_id!=''
            BEGIN
                INSERT INTO stream_item_topics(stream_item_id,session_id,topic_id,created_at)
                SELECT dest.id,dest.session_local_id,tags.topic_id,dest.created_at
                FROM stream_items source JOIN stream_item_topics tags ON tags.stream_item_id=source.id AND tags.session_id=source.session_local_id
                JOIN stream_items dest ON dest.session_local_id=source.session_local_id AND dest.run_id=source.run_id
                WHERE source.session_local_id=NEW.session_local_id AND source.run_id=NEW.run_id AND dest.kind!='user'
                ON CONFLICT(stream_item_id,topic_id) DO NOTHING;
                ` + inheritTopicRunProof + ` END`},
	}
	repaired := false
	normalize := func(value string) string {
		return strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(value), ";")), " ")
	}
	for _, trigger := range triggers {
		var existing string
		err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, trigger.name).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if normalize(existing) == normalize(trigger.statement) {
			continue
		}
		// Names are static implementation constants, never caller input.
		if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ` + trigger.name); err != nil {
			return err
		}
		if _, err := tx.Exec(trigger.statement); err != nil {
			return err
		}
		repaired = true
	}
	if repaired {
		// Recover only an existing proof. Never stamp today's source state onto
		// an earlier synthesis whose original inputs were not recorded.
		if _, err := tx.Exec(`UPDATE stream_items SET run_id=run_id WHERE run_id!='' AND EXISTS
            (SELECT 1 FROM stream_item_topics WHERE stream_item_id=stream_items.id)
            AND json_type(CASE WHEN json_valid(automation_context) THEN automation_context ELSE '{}' END,'$.topicContext')='object';
            UPDATE stream_items SET automation_context=json_set(
                CASE WHEN json_valid(automation_context) THEN CASE WHEN json_type(automation_context)='object' THEN automation_context ELSE '{}' END ELSE '{}' END,
                '$.topicContext',json('{"version":0}'))
            WHERE EXISTS (SELECT 1 FROM stream_item_topics WHERE stream_item_id=stream_items.id)
                AND json_type(CASE WHEN json_valid(automation_context) THEN automation_context ELSE '{}' END,'$.topicContext') IS NULL;`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BindStreamItemTopicRun attaches the runner's identity before publishing its
// first event. A routed fallback may replace the user row's current run;
// earlier rows keep their own tags and run identities. Triggers tag each
// subsequent renderer/phone write in its insert
// transaction, and cover rows that arrived before the binding or gained RunID.
func (s *Store) BindStreamItemTopicRun(ctx context.Context, sessionID, userItemID, runID string) error {
	if runID == "" {
		return errors.New("TOPIC_RUN_REQUIRED")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE stream_items SET run_id = ?
        WHERE id = ? AND session_local_id = ? AND kind = 'user'
        AND EXISTS (SELECT 1 FROM stream_item_topics WHERE stream_item_id = stream_items.id)`, runID, userItemID, sessionID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("TOPIC_TURN_UNAVAILABLE")
	}
	return err
}

// LoadTopicStreamItems returns a backwards page, newest first. The caller
// merges events before reversing for the renderer. At/id form an exclusive,
// stable cursor even when several rows share a millisecond.
func (s *Store) LoadTopicStreamItems(ctx context.Context, sessionID, topicID string, at int64, id string, limit int) ([]StreamItemRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `SELECT i.id, i.session_local_id, i.kind, i.content, i.tool_name, i.tool_input, i.tool_use_id, i.run_id, i.session_id, i.sort_order, t.created_at, i.images, i.automation_context, i.files
        FROM stream_item_topics AS t JOIN stream_items AS i ON i.id = t.stream_item_id AND i.session_local_id = t.session_id
        WHERE t.session_id = ? AND t.topic_id = ? AND (t.created_at < ? OR (t.created_at = ? AND 'message:' || i.id < ?))
        ORDER BY t.created_at DESC, i.id DESC LIMIT ?`, sessionID, topicID, at, at, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []StreamItemRow{}
	for rows.Next() {
		var item StreamItemRow
		if err := rows.Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// InsertStreamItemTopics inserts idempotent tags only on the named main row.
func (s *Store) InsertStreamItemTopics(ctx context.Context, itemID, sessionID string, topicIDs []string) error {
	return s.insertStreamItemTopics(ctx, itemID, sessionID, topicIDs, nil, nil)
}

// InsertStreamItemTopicsIfCurrent reserves the main database writer and checks
// the same revision as A3 admission before publishing inferred membership.
// validate must inspect only files/policy, never query this Store.
func (s *Store) InsertStreamItemTopicsIfCurrent(ctx context.Context, itemID, sessionID string, topicIDs []string, revision ReadRevision, validate func() error) error {
	return s.insertStreamItemTopics(ctx, itemID, sessionID, topicIDs, &revision, validate)
}
func (s *Store) insertStreamItemTopics(ctx context.Context, itemID, sessionID string, topicIDs []string, revision *ReadRevision, validate func() error) error {
	return s.insertStreamItemTopicBatch(ctx, []StreamItemTopicInsert{{ItemID: itemID, SessionID: sessionID, TopicIDs: topicIDs}}, revision, "", validate)
}

// StreamItemTopicInsert is an inferred-membership write in a checked batch.
type StreamItemTopicInsert struct {
	ItemID, SessionID string
	TopicIDs          []string
}

// InsertStreamItemTopicBatchIfCurrent checks one source revision for the entire
// backfill, so its own inserts cannot invalidate or refresh the source snapshot.
func (s *Store) InsertStreamItemTopicBatchIfCurrent(ctx context.Context, batch []StreamItemTopicInsert, revision ReadRevision, validate func() error) error {
	return s.insertStreamItemTopicBatch(ctx, batch, &revision, "", validate)
}

// CompleteStreamItemTopicBackfillIfCurrent commits membership and its completion
// marker in one checked transaction. A failed source check or marker write rolls
// both back; validate must never query this Store.
func (s *Store) CompleteStreamItemTopicBackfillIfCurrent(ctx context.Context, batch []StreamItemTopicInsert, revision ReadRevision, completionKey string, validate func() error) error {
	return s.insertStreamItemTopicBatch(ctx, batch, &revision, completionKey, validate)
}

func (s *Store) insertStreamItemTopicBatch(ctx context.Context, batch []StreamItemTopicInsert, revision *ReadRevision, completionKey string, validate func() error) error {
	release, err := s.lockContextUseRevision(ctx)
	if err != nil {
		return err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET updated_at=updated_at WHERE 0`); err != nil {
		return err
	}
	if revision != nil {
		expected := *revision
		expected.changes += s.contextUses.changes
		current, err := DatabaseRevision(ctx, tx)
		if err != nil {
			return err
		}
		if current != expected {
			return ErrReadRevisionChanged
		}
	}
	for _, item := range batch {
		for _, id := range item.TopicIDs {
			if _, err = tx.ExecContext(ctx, `INSERT INTO stream_item_topics(stream_item_id,session_id,topic_id,created_at) SELECT i.id,i.session_local_id,?,i.created_at FROM stream_items i JOIN sessions s ON s.local_id=i.session_local_id AND s.kind='main' WHERE i.id=? AND i.session_local_id=? AND NOT(i.kind='system' AND i.tool_name='main_chat_status') ON CONFLICT DO NOTHING`, id, item.ItemID, item.SessionID); err != nil {
				return err
			}
		}
	}
	if completionKey != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO ui_state(key,value) VALUES(?,'true') ON CONFLICT(key) DO UPDATE SET value=excluded.value`, completionKey); err != nil {
			return err
		}
	}
	if validate != nil {
		if err = validate(); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpgradeTopicProofs rewrites each stored topic proof to its current form. A
// pair applies only to rows still holding the exact old proof, so a row changed
// meanwhile is left alone. It returns how many rows changed.
func (s *Store) UpgradeTopicProofs(ctx context.Context, pairs map[string]string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var changed int64
	for oldProof, newProof := range pairs {
		res, err := tx.ExecContext(ctx, `UPDATE stream_items SET automation_context=json_set(
            CASE WHEN json_valid(automation_context) THEN CASE WHEN json_type(automation_context)='object' THEN automation_context ELSE '{}' END ELSE '{}' END,
            '$.topicContext', json(?))
            WHERE json_valid(automation_context) AND json_type(automation_context,'$.topicContext')='object'
              AND json(json_extract(automation_context,'$.topicContext'))=json(?)`, newProof, oldProof)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		changed += n
	}
	return changed, tx.Commit()
}
