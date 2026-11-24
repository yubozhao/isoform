package storage

import (
	"context"
	"errors"
	"os"
	"time"

	"wails-gui/internal/destinations"
)

const LastSearchServiceKey = "what_leaves_last_search_service"

const destinationWeek = 7 * 24 * time.Hour

// StartDestinationRecorder arms crash recovery as part of opening desktop
// storage, before the store is published to senders.
func (s *Store) StartDestinationRecorder() error {
	r, err := destinations.StartRecorder(s)
	s.destinationRecorder = r
	return err
}

const destinationCallsSchema = `CREATE TABLE IF NOT EXISTS destination_calls (
		destination TEXT NOT NULL, second INTEGER NOT NULL, detail TEXT NOT NULL,
		calls INTEGER NOT NULL, latest INTEGER NOT NULL,
		CHECK (
			(destination='assistant_search' AND detail IN ('','claude_code','grok','gemini','doubao','chat:claude_code')) OR
			(destination='agents_mcp_read' AND detail IN ('claude-code','codex')) OR
			(destination NOT IN ('assistant_search','agents_mcp_read') AND detail='')
		),
		PRIMARY KEY(second,destination,detail)
	)`

func (s *Store) migrateDestinationCalls() error {
	_, err := s.db.Exec(destinationCallsSchema)
	return err
}

// CCA14 C1 adds one fixed Chat detail to the search allowlist, preserving the weekly buckets.
func (s *Store) migrateChatSearchDetail() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("ALTER TABLE destination_calls RENAME TO destination_calls_v79"); err != nil {
		return err
	}
	if _, err := tx.Exec(destinationCallsSchema); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO destination_calls(destination,second,detail,calls,latest)
		SELECT destination,second,detail,calls,latest FROM destination_calls_v79;
		DROP TABLE destination_calls_v79`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateDestinationLosses(previous int) error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS destination_count_losses(destination TEXT PRIMARY KEY, at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS destination_count_run(singleton INTEGER PRIMARY KEY CHECK(singleton=1));`)
	if err == nil && previous == 78 {
		// The preview schema had counts but could not establish completeness.
		_, err = s.BeginDestinationRun(context.Background())
	}
	return err
}

const markDestinationLoss = `INSERT INTO destination_count_losses(destination,at) VALUES(?,?)
	ON CONFLICT(destination) DO UPDATE SET at=MAX(at,excluded.at)`

func (s *Store) MarkDestinationIncomplete(ctx context.Context, id destinations.ID, at time.Time) error {
	_, err := s.db.ExecContext(ctx, markDestinationLoss, id, at.UnixNano())
	return err
}

// The writable desktop store arms this marker before publication. It is
// independent of the count tables so a broken count or loss write cannot
// disarm crash recovery or prevent the queue from accepting sends.
func (s *Store) BeginDestinationRun(ctx context.Context) (bool, error) {
	_, fileErr := os.Stat(s.destinationRunFile)
	fallbackArmed := fileErr == nil
	result, err := s.db.ExecContext(ctx, "INSERT OR IGNORE INTO destination_count_run VALUES(1)")
	if err == nil {
		added, err := result.RowsAffected()
		// An unreadable fallback marker also makes prior coverage uncertain.
		return added == 0 || !errors.Is(fileErr, os.ErrNotExist), err
	}
	// A SQLite write failure must not disable senders. An empty file beside
	// the database provides independent crash evidence until a clean flush.
	if fallbackArmed {
		return true, nil
	}
	f, createErr := os.OpenFile(s.destinationRunFile, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if errors.Is(createErr, os.ErrExist) {
		return true, nil
	}
	if createErr != nil {
		return false, errors.Join(err, createErr)
	}
	syncErr := f.Sync()
	return true, errors.Join(syncErr, f.Close())
}

func (s *Store) EndDestinationRun(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM destination_count_run WHERE singleton=1"); err != nil {
		return err
	}
	err := os.Remove(s.destinationRunFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) PruneDestinationCalls(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM destination_count_losses WHERE at <= ?", time.Now().Add(-destinationWeek).UnixNano()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM destination_calls WHERE second < ?", time.Now().Add(-destinationWeek).Unix())
	return err
}

func (s *Store) RecordDestination(ctx context.Context, call destinations.Call) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := time.Now().Add(-destinationWeek).Unix()
	if _, err = tx.ExecContext(ctx, `DELETE FROM destination_calls WHERE second < ?`, cutoff); err != nil {
		return err
	}
	if call.At.Unix() >= cutoff {
		_, err = tx.ExecContext(ctx, `INSERT INTO destination_calls(destination,second,detail,calls,latest)
			VALUES(?,?,?,1,?) ON CONFLICT(second,destination,detail) DO UPDATE
			SET calls=calls+1, latest=MAX(latest,excluded.latest)`, call.ID, call.At.Unix(), call.Detail, call.At.UnixNano())
		if err != nil {
			return err
		}
	}
	// Keep only the provider ID once its last weekly bucket ages out.
	if call.ID == destinations.AssistantSearch && call.At.Unix() >= cutoff {
		_, err = tx.ExecContext(ctx, `INSERT INTO ui_state(key,value)
			SELECT ?,CASE detail WHEN ? THEN 'claude_code' ELSE detail END FROM destination_calls WHERE destination=? ORDER BY latest DESC LIMIT 1
			ON CONFLICT(key) DO UPDATE SET value=excluded.value`, LastSearchServiceKey, destinations.ChatSearchDetail, call.ID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

type DestinationCount struct {
	Incomplete bool
	ID         destinations.ID
	Detail     string
	Calls      int
}

func (s *Store) WeeklyDestinationCounts(ctx context.Context, now time.Time) ([]DestinationCount, error) {
	var counts []DestinationCount
	var err error
	r := s.destinationRecorder
	if r != nil {
		err = r.Wait(ctx)
	}
	if err == nil {
		counts, err = s.readWeeklyDestinationCounts(ctx, now)
	}
	// Sample volatile marks after the SQL snapshot. This includes a write
	// failure concurrent with the query, even when the query itself failed.
	if r != nil {
		for id, at := range r.Losses() {
			if at.After(now.Add(-destinationWeek)) {
				counts = append(counts, DestinationCount{ID: id, Incomplete: true})
			}
		}
	}
	return counts, err
}

func (s *Store) readWeeklyDestinationCounts(ctx context.Context, now time.Time) ([]DestinationCount, error) {
	rows, err := s.committedReader().QueryContext(ctx, `SELECT destination,detail,calls,incomplete FROM (
			SELECT destination,detail,SUM(calls) AS calls,0 AS incomplete,MAX(latest) AS latest FROM destination_calls
			WHERE second >= ? GROUP BY destination,detail
			UNION ALL SELECT destination,'',0,1,at FROM destination_count_losses WHERE at > ?
		) ORDER BY latest,destination,detail`, now.Add(-destinationWeek).Unix(), now.Add(-destinationWeek).UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var counts []DestinationCount
	for rows.Next() {
		var c DestinationCount
		if err := rows.Scan(&c.ID, &c.Detail, &c.Calls, &c.Incomplete); err != nil {
			return nil, err
		}
		counts = append(counts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}
