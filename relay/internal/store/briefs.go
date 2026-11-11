package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"isoform-relay/internal/push"
	"isoform-relay/internal/wire"
)

// Brief is a stored encrypted brief. Version is the envelope sequence number.
type Brief struct {
	Version uint64
	AsOf    string
	Body    []byte
}

// briefState is derived from deliver_at at the time of an operation; it is not
// persisted, so a held brief becomes due without a write or a background job.
type briefState string

const (
	briefHeld briefState = "held"
	briefDue  briefState = "due"
)

// briefStateAt is the single transition from held to due. Its placeholder is
// the operation's current time in milliseconds. All reads and retention use
// the same boundary, including the immediate-delivery sentinel (0).
func briefStateAt() string {
	return "CASE WHEN deliver_at<=? THEN '" + string(briefDue) + "' ELSE '" + string(briefHeld) + "' END"
}

// retainBriefs applies independent limits to due and held versions, either for
// one device on insertion or for all devices during maintenance.
func retainBriefs(ctx context.Context, tx *sql.Tx, now time.Time, deviceID string) (int64, error) {
	filter := ""
	args := []any{millis(now)}
	if deviceID != "" {
		filter = " WHERE device_id=?"
		args = append(args, deviceID)
	}
	query := `DELETE FROM briefs WHERE rowid IN (
		SELECT rowid FROM (
			SELECT rowid, state, ROW_NUMBER() OVER (PARTITION BY device_id, state ORDER BY version DESC) AS position
			FROM (SELECT rowid, device_id, version, ` + briefStateAt() + ` AS state FROM briefs` + filter + `)
		) WHERE position > CASE state WHEN ? THEN ? ELSE ? END
	)`
	args = append(args, briefHeld, KeptHeldBriefs, KeptBriefs)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// PutBrief stores a brief for a device of the hub and keeps the newest
// KeptBriefs due and KeptHeldBriefs held versions. With notify it queues one
// push for the brief, once.
func (s *Store) PutBrief(ctx context.Context, hubID string, env wire.Envelope, body []byte, asOf string, notify bool, now time.Time) error {
	return s.PutBriefDueAt(ctx, hubID, env, body, asOf, notify, now, time.Time{})
}

// PutBriefDueAt is PutBrief for a brief that is due later: a deliverAt after now
// keeps the brief out of every read until that time, and its one push is due
// then. A zero or past deliverAt is the same as PutBrief.
func (s *Store) PutBriefDueAt(ctx context.Context, hubID string, env wire.Envelope, body []byte, asOf string, notify bool, now, deliverAt time.Time) error {
	// A brief that is not held stores 0, so it is visible whatever clock a read uses.
	due := now
	heldUntil := int64(0)
	if deliverAt.After(now) {
		due = deliverAt
		heldUntil = millis(deliverAt)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = deviceInHub(ctx, tx, hubID, env.Dev); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO briefs(device_id,version,as_of,body,stored_at,deliver_at) VALUES(?,?,?,?,?,?)", env.Dev, int64(env.Seq), asOf, string(body), millis(now), heldUntil)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 && notify {
		if err = push.EnqueueAt(ctx, tx, env.Dev, strconv.FormatUint(env.Seq, 10), push.KindBrief, now, due); err != nil {
			return err
		}
	}
	if _, err = retainBriefs(ctx, tx, now, env.Dev); err != nil {
		return err
	}
	return tx.Commit()
}

// LatestBrief returns the device's newest brief that is due at now, or ErrNotFound.
func (s *Store) LatestBrief(ctx context.Context, deviceID string, now time.Time) (Brief, error) {
	briefs, err := s.ListBriefs(ctx, deviceID, 1, now)
	if err != nil {
		return Brief{}, err
	}
	if len(briefs) == 0 {
		return Brief{}, ErrNotFound
	}
	return briefs[0], nil
}

// ListBriefs returns up to limit briefs due at now, newest first. A brief held
// for a later time is not listed.
func (s *Store) ListBriefs(ctx context.Context, deviceID string, limit int, now time.Time) ([]Brief, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT version,as_of,body FROM briefs WHERE device_id=? AND "+briefStateAt()+"=? ORDER BY version DESC LIMIT ?", deviceID, millis(now), briefDue, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	briefs := []Brief{}
	for rows.Next() {
		var version int64
		var b Brief
		var body string
		if err = rows.Scan(&version, &b.AsOf, &body); err != nil {
			return nil, err
		}
		b.Version = uint64(version)
		b.Body = []byte(body)
		briefs = append(briefs, b)
	}
	return briefs, rows.Err()
}

// deviceHub returns the hub that owns a device.
func deviceHub(ctx context.Context, q querier, deviceID string) (string, error) {
	var hubID string
	err := q.QueryRowContext(ctx, "SELECT hub_id FROM devices WHERE id=?", deviceID).Scan(&hubID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hubID, err
}
