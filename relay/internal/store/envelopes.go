package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"time"

	"isoform-relay/internal/push"
	"isoform-relay/internal/wire"
)

// Entry is a stored request envelope and its sequence number.
type Entry struct {
	Seq  uint64
	Body []byte
}

// AddRequest queues a device request for its hub. An identical retry succeeds
// without a second copy. A different envelope under a used sequence is a
// conflict. A full queue reports ErrQueueFull, since a request is never dropped.
func (s *Store) AddRequest(ctx context.Context, dev Device, env wire.Envelope, body []byte, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM envelopes WHERE device_id=? AND dir='d2h' AND expires_at<=?", dev.ID, millis(now)); err != nil {
		return err
	}
	stored, err := storedBody(ctx, tx, dev.ID, "d2h", env.Seq)
	if err != nil {
		return err
	}
	if stored != nil {
		if string(stored) == string(body) {
			return nil
		}
		return ErrConflict
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM envelopes WHERE device_id=? AND dir='d2h'", dev.ID).Scan(&count); err != nil {
		return err
	}
	if count >= MaxQueued {
		return ErrQueueFull
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO envelopes(device_id,dir,seq,kind,body,expires_at) VALUES(?,'d2h',?,'req',?,?)", dev.ID, int64(env.Seq), string(body), millis(now.Add(RequestTTL))); err != nil {
		return err
	}
	return tx.Commit()
}

// AddResponse stores a hub envelope of kind res or event for a device of the
// hub and queues no push. The newest MaxQueued responses and the newest
// MaxEvents events are kept; older ones of each kind are dropped.
func (s *Store) AddResponse(ctx context.Context, hubID string, env wire.Envelope, body []byte) error {
	return s.storeResponse(ctx, hubID, env, body, "", push.Notice{}, time.Time{})
}

// AddResponseWithPush stores a hub event like AddResponse. When the envelope is
// new and pushKind is set, it also queues one push of that kind for the device,
// in the same transaction, if the device has a registration. A repeated
// envelope queues nothing.
func (s *Store) AddResponseWithPush(ctx context.Context, hubID string, env wire.Envelope, body []byte, pushKind string, now time.Time) error {
	return s.storeResponse(ctx, hubID, env, body, pushKind, push.Notice{}, now)
}

// AddNoticeResponse stores a hub event like AddResponseWithPush and queues one
// moment push that carries the sealed notice. The push is queued only for a new
// envelope, in the same transaction, as with AddResponseWithPush.
func (s *Store) AddNoticeResponse(ctx context.Context, hubID string, env wire.Envelope, body []byte, notice push.Notice, now time.Time) error {
	return s.storeResponse(ctx, hubID, env, body, push.KindMoment, notice, now)
}

func (s *Store) storeResponse(ctx context.Context, hubID string, env wire.Envelope, body []byte, pushKind string, notice push.Notice, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = deviceInHub(ctx, tx, hubID, env.Dev); err != nil {
		return err
	}
	stored, err := storedBody(ctx, tx, env.Dev, "h2d", env.Seq)
	if err != nil {
		return err
	}
	if stored != nil {
		if string(stored) == string(body) {
			return nil
		}
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO envelopes(device_id,dir,seq,kind,body,expires_at) VALUES(?,'h2d',?,?,?,0)", env.Dev, int64(env.Seq), env.Kind, string(body)); err != nil {
		return err
	}
	if err = trimHubEnvelopes(ctx, tx, env.Dev, env.Kind); err != nil {
		return err
	}
	eventID := "event:" + strconv.FormatUint(env.Seq, 10)
	switch pushKind {
	case "":
	case push.KindMoment:
		if err = push.EnqueueNotice(ctx, tx, env.Dev, eventID, notice, now); err != nil {
			return err
		}
	default:
		if err = push.Enqueue(ctx, tx, env.Dev, eventID, pushKind, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UnsentRequests lists a device's unexpired requests after seq, oldest first.
// trimHubEnvelopes drops the oldest hub envelopes of the kind just stored. Events
// and responses are trimmed separately, so an event burst cannot evict a
// response the phone has not read yet.
func trimHubEnvelopes(ctx context.Context, tx *sql.Tx, deviceID, kind string) error {
	match, limit := "kind<>'event'", MaxQueued
	if kind == "event" {
		match, limit = "kind='event'", MaxEvents
	}
	query := "DELETE FROM envelopes WHERE device_id=? AND dir='h2d' AND " + match +
		" AND seq<=(SELECT seq FROM envelopes WHERE device_id=? AND dir='h2d' AND " + match +
		" ORDER BY seq DESC LIMIT 1 OFFSET ?)"
	_, err := tx.ExecContext(ctx, query, deviceID, deviceID, limit)
	return err
}

func (s *Store) UnsentRequests(ctx context.Context, deviceID string, after uint64, now time.Time, limit int) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT seq,body FROM envelopes WHERE device_id=? AND dir='d2h' AND seq>? AND expires_at>? ORDER BY seq LIMIT ?", deviceID, int64(after), millis(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		var seq int64
		var body string
		if err = rows.Scan(&seq, &body); err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Seq: uint64(seq), Body: []byte(body)})
	}
	return entries, rows.Err()
}

// ResponsesSince lists a device's hub envelopes after seq, oldest first. A
// device that was removed or has expired reports ErrUnauthorized.
func (s *Store) ResponsesSince(ctx context.Context, deviceID string, after uint64, now time.Time, limit int) ([]json.RawMessage, error) {
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT expires_at FROM devices WHERE id=?", deviceID).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && expires <= millis(now)) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM envelopes WHERE device_id=? AND dir='h2d' AND seq>? ORDER BY seq LIMIT ?", deviceID, int64(after), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	envelopes := []json.RawMessage{}
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		envelopes = append(envelopes, json.RawMessage(body))
	}
	return envelopes, rows.Err()
}

// AckResponses deletes hub envelopes the device has received through upTo.
// Briefs are kept in their own table and are unaffected.
func (s *Store) AckResponses(ctx context.Context, deviceID string, upTo uint64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM envelopes WHERE device_id=? AND dir='h2d' AND seq<=?", deviceID, int64(upTo))
	return err
}

// AckRequests deletes a device's requests the hub has consumed through upTo.
// The device must belong to the hub; an unknown device is ErrNotFound.
func (s *Store) AckRequests(ctx context.Context, hubID, deviceID string, upTo uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = deviceInHub(ctx, tx, hubID, deviceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM envelopes WHERE device_id=? AND dir='d2h' AND seq<=?", deviceID, int64(upTo)); err != nil {
		return err
	}
	return tx.Commit()
}

// storedBody returns the canonical bytes already stored at a sequence number,
// or nil when the sequence is free.
func storedBody(ctx context.Context, q querier, deviceID, dir string, seq uint64) ([]byte, error) {
	var body string
	err := q.QueryRowContext(ctx, "SELECT body FROM envelopes WHERE device_id=? AND dir=? AND seq=?", deviceID, dir, int64(seq)).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(body), nil
}

// PurgeDevice drops every ciphertext queued for one of the hub's devices: its
// envelopes both ways, its stored briefs, its uploads and its pushes not yet
// sent (PROTOCOL.md 28). The pairing stays.
func (s *Store) PurgeDevice(ctx context.Context, hubID, deviceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = deviceInHub(ctx, tx, hubID, deviceID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM uploads WHERE device_id=?", deviceID)
	if err != nil {
		return err
	}
	var uploads []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		uploads = append(uploads, id)
	}
	rows.Close()
	for _, statement := range []string{
		"DELETE FROM envelopes WHERE device_id=?",
		"DELETE FROM briefs WHERE device_id=?",
		"DELETE FROM uploads WHERE device_id=?",
		"UPDATE push_outbox SET state='cancelled' WHERE device_id=? AND state IN ('queued','inflight')",
	} {
		if _, err = tx.ExecContext(ctx, statement, deviceID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, id := range uploads {
		_ = os.Remove(s.uploadPath(hubID, id))
	}
	return nil
}
