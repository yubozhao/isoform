package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	OutboundSendSending   = "sending"
	OutboundSendAccepted  = "accepted"
	OutboundSendFinalized = "finalized"
	OutboundSendFailed    = "failed"
	OutboundSendUnknown   = "unknown"
)

var ErrOutboundSendSnapshotChanged = errors.New("outbound send draft snapshot changed")

type OutboundSendRow struct {
	MomentID    string
	Revision    string
	Channel     string
	PrincipalID string
	Recipient   string
	DraftTo     string
	Subject     string
	Text        string
	SourceKey   string
	State       string
	SentAt      int64
	FollowupAt  int64
	CreatedAt   int64
	UpdatedAt   int64
}

func (s *Store) migrateOutboundSends() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS outbound_sends_v1 (
			moment_id    TEXT NOT NULL,
			revision     TEXT NOT NULL,
			channel      TEXT NOT NULL,
			principal_id TEXT NOT NULL DEFAULT '',
			recipient    TEXT NOT NULL DEFAULT '',
			draft_to     TEXT NOT NULL,
			subject      TEXT NOT NULL DEFAULT '',
			text         TEXT NOT NULL,
			source_key   TEXT NOT NULL DEFAULT '',
			state        TEXT NOT NULL,
			sent_at      INTEGER NOT NULL DEFAULT 0,
			followup_at  INTEGER NOT NULL DEFAULT 0,
			created_at   INTEGER NOT NULL,
			updated_at   INTEGER NOT NULL,
			PRIMARY KEY (moment_id, revision)
		);
		CREATE INDEX IF NOT EXISTS idx_outbound_sends_waiting
			ON outbound_sends_v1(principal_id, followup_at)
			WHERE state IN ('accepted', 'finalized') AND followup_at > 0;
	`)
	return err
}

const outboundSendColumns = `moment_id, revision, channel, principal_id, recipient, draft_to, subject, text, source_key, state, sent_at, followup_at, created_at, updated_at`

func scanOutboundSend(scanner interface{ Scan(...any) error }) (OutboundSendRow, error) {
	var row OutboundSendRow
	err := scanner.Scan(&row.MomentID, &row.Revision, &row.Channel, &row.PrincipalID, &row.Recipient,
		&row.DraftTo, &row.Subject, &row.Text, &row.SourceKey, &row.State, &row.SentAt, &row.FollowupAt,
		&row.CreatedAt, &row.UpdatedAt)
	return row, err
}

func sameOutboundSendSnapshot(left, right OutboundSendRow) bool {
	return left.MomentID == right.MomentID && left.Revision == right.Revision && left.Channel == right.Channel &&
		left.PrincipalID == right.PrincipalID && left.Recipient == right.Recipient && left.DraftTo == right.DraftTo &&
		left.Subject == right.Subject && left.Text == right.Text && left.SourceKey == right.SourceKey
}

func (s *Store) LoadOutboundSend(ctx context.Context, momentID, revision string) (*OutboundSendRow, error) {
	row, err := scanOutboundSend(s.committedReader().QueryRowContext(ctx, `SELECT `+outboundSendColumns+` FROM outbound_sends_v1 WHERE moment_id = ? AND revision = ?`, momentID, revision))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ClaimOutboundSend durably reserves a first attempt or a retry after a
// definitive failure. Accepted, finalized and uncertain attempts are returned
// without a new claim so callers never invoke their transport again.
func (s *Store) ClaimOutboundSend(ctx context.Context, row OutboundSendRow, now int64) (OutboundSendRow, bool, error) {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	row.MomentID = strings.TrimSpace(row.MomentID)
	row.Revision = strings.TrimSpace(row.Revision)
	if row.MomentID == "" || row.Revision == "" {
		return row, false, errors.New("outbound send needs a moment and revision")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return row, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO outbound_sends_v1 (`+outboundSendColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)
		ON CONFLICT(moment_id, revision) DO NOTHING
	`, row.MomentID, row.Revision, row.Channel, row.PrincipalID, row.Recipient, row.DraftTo,
		row.Subject, row.Text, row.SourceKey, OutboundSendSending, now, now)
	if err != nil {
		return row, false, err
	}
	if n, _ := result.RowsAffected(); n == 1 {
		row.State = OutboundSendSending
		row.CreatedAt, row.UpdatedAt = now, now
		return row, true, tx.Commit()
	}
	stored, err := scanOutboundSend(tx.QueryRowContext(ctx, `SELECT `+outboundSendColumns+` FROM outbound_sends_v1 WHERE moment_id = ? AND revision = ?`, row.MomentID, row.Revision))
	if err != nil {
		return row, false, err
	}
	if !sameOutboundSendSnapshot(stored, row) {
		return stored, false, ErrOutboundSendSnapshotChanged
	}
	if stored.State != OutboundSendFailed {
		return stored, false, tx.Commit()
	}
	result, err = tx.ExecContext(ctx, `UPDATE outbound_sends_v1 SET state = ?, updated_at = ? WHERE moment_id = ? AND revision = ? AND state = ?`,
		OutboundSendSending, now, row.MomentID, row.Revision, OutboundSendFailed)
	if err != nil {
		return stored, false, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return stored, false, errors.New("outbound send state changed while retrying")
	}
	stored.State = OutboundSendSending
	stored.UpdatedAt = now
	return stored, true, tx.Commit()
}

func (s *Store) MarkOutboundSendAccepted(ctx context.Context, momentID, revision string, sentAt, followupAt int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE outbound_sends_v1 SET state = ?, sent_at = ?, followup_at = ?, updated_at = ? WHERE moment_id = ? AND revision = ? AND state = ?`,
		OutboundSendAccepted, sentAt, followupAt, time.Now().UnixMilli(), momentID, revision, OutboundSendSending)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("outbound send was not marked accepted")
	}
	return nil
}

func (s *Store) MarkOutboundSendFailed(ctx context.Context, momentID, revision string, unknown bool) error {
	state := OutboundSendFailed
	if unknown {
		state = OutboundSendUnknown
	}
	result, err := s.db.ExecContext(ctx, `UPDATE outbound_sends_v1 SET state = ?, updated_at = ? WHERE moment_id = ? AND revision = ? AND state = ?`,
		state, time.Now().UnixMilli(), momentID, revision, OutboundSendSending)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("outbound send was not marked finished")
	}
	return nil
}

func (s *Store) MarkOutboundSendFinalized(ctx context.Context, momentID, revision string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbound_sends_v1 SET state = ?, updated_at = ? WHERE moment_id = ? AND revision = ? AND state IN (?, ?)`,
		OutboundSendFinalized, time.Now().UnixMilli(), momentID, revision, OutboundSendAccepted, OutboundSendFinalized)
	return err
}

func (s *Store) ListOutboundSendsWaiting(ctx context.Context) ([]OutboundSendRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `SELECT `+outboundSendColumns+` FROM outbound_sends_v1 WHERE state IN (?, ?) AND followup_at > 0 ORDER BY followup_at, moment_id, revision`, OutboundSendAccepted, OutboundSendFinalized)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutboundSendRow{}
	for rows.Next() {
		row, err := scanOutboundSend(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ListOutboundSendsSentSince lists the sends Mail or Slack accepted at or after
// since (unix ms), newest first: the phone's Done rows for sent replies.
func (s *Store) ListOutboundSendsSentSince(ctx context.Context, since int64) ([]OutboundSendRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `SELECT `+outboundSendColumns+` FROM outbound_sends_v1 WHERE state IN (?, ?) AND sent_at >= ? ORDER BY sent_at DESC, moment_id, revision`, OutboundSendAccepted, OutboundSendFinalized, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutboundSendRow{}
	for rows.Next() {
		row, err := scanOutboundSend(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) RetireOutboundSendFollowup(ctx context.Context, momentID, revision string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbound_sends_v1 SET followup_at = 0, updated_at = ? WHERE moment_id = ? AND revision = ?`, time.Now().UnixMilli(), momentID, revision)
	return err
}

type outboundSendExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func deleteOutboundSendsByChannel(ctx context.Context, execer outboundSendExecer, channel string) (int64, error) {
	result, err := execer.ExecContext(ctx, `DELETE FROM outbound_sends_v1 WHERE channel = ?`, channel)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) DeleteOutboundSendsByChannel(ctx context.Context, channel string) (int64, error) {
	return deleteOutboundSendsByChannel(ctx, s.db, channel)
}

// DeleteAllMailData clears Mail source rows and their outbound draft snapshots
// together. Other channels' outbound history is not Mail-derived and remains.
func (s *Store) DeleteAllMailData(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM mail_messages`)
	if err != nil {
		return 0, err
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := deleteOutboundSendsByChannel(ctx, tx, "mail"); err != nil {
		return 0, err
	}
	return removed, tx.Commit()
}

// ClaimSingleOutboundSend permits at most one uncertain or accepted send for
// a draft, even when its text is edited between attempts. Definitive failures
// can be retried. The read and reservation share a transaction so competing
// callers cannot both obtain authority to send.
func (s *Store) ClaimSingleOutboundSend(ctx context.Context, row OutboundSendRow, now int64) (OutboundSendRow, bool, error) {
	if strings.TrimSpace(row.MomentID) == "" || strings.TrimSpace(row.Revision) == "" {
		return row, false, errors.New("outbound send needs a moment and revision")
	}
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return row, false, err
	}
	defer tx.Rollback()
	var blocked int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM outbound_sends_v1 WHERE moment_id = ? AND state <> ?`, row.MomentID, OutboundSendFailed).Scan(&blocked); err != nil {
		return row, false, err
	}
	if blocked != 0 {
		return row, false, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO outbound_sends_v1 (`+outboundSendColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)
		ON CONFLICT(moment_id, revision) DO NOTHING`, row.MomentID, row.Revision, row.Channel, row.PrincipalID, row.Recipient,
		row.DraftTo, row.Subject, row.Text, row.SourceKey, OutboundSendSending, now, now)
	if err != nil {
		return row, false, err
	}
	if n, _ := result.RowsAffected(); n == 1 {
		row.State, row.CreatedAt, row.UpdatedAt = OutboundSendSending, now, now
		return row, true, tx.Commit()
	}
	stored, err := scanOutboundSend(tx.QueryRowContext(ctx, `SELECT `+outboundSendColumns+` FROM outbound_sends_v1 WHERE moment_id = ? AND revision = ?`, row.MomentID, row.Revision))
	if err != nil {
		return row, false, err
	}
	if !sameOutboundSendSnapshot(stored, row) {
		return stored, false, ErrOutboundSendSnapshotChanged
	}
	result, err = tx.ExecContext(ctx, `UPDATE outbound_sends_v1 SET state = ?, updated_at = ? WHERE moment_id = ? AND revision = ? AND state = ?`, OutboundSendSending, now, row.MomentID, row.Revision, OutboundSendFailed)
	if err != nil {
		return stored, false, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return stored, false, errors.New("outbound send state changed while retrying")
	}
	stored.State, stored.UpdatedAt = OutboundSendSending, now
	return stored, true, tx.Commit()
}

// LoadSingleOutboundSend finds an attempt that prevents another send of the
// same draft. Failed attempts are excluded because they may be retried.
func (s *Store) LoadSingleOutboundSend(ctx context.Context, momentID string) (*OutboundSendRow, error) {
	row, err := scanOutboundSend(s.committedReader().QueryRowContext(ctx, `SELECT `+outboundSendColumns+` FROM outbound_sends_v1 WHERE moment_id = ? AND state <> ? ORDER BY created_at DESC LIMIT 1`, momentID, OutboundSendFailed))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
