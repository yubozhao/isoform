package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MailRetention is how long a message's headers are kept: the window the
// Mail reader reads. Older headers are no longer kept current.
const MailRetention = 7 * 24 * time.Hour

// Mailboxes a message is read from.
const (
	MailboxInbox = "inbox"
	MailboxSent  = "sent"
)

// MailMessageRow is the headers of one message as stored. Never a body.
// ID is a hash of the mailbox and the message-id; ThreadKey a hash of the
// normalized subject. Addresses are lowercased; To and CC hold one address
// per line. ToMe is set for an inbox message that names the user in To.
// At is when it was received (inbox) or sent (sent), Unix milliseconds.
type MailMessageRow struct {
	ID          string
	Mailbox     string
	SenderEmail string
	SenderName  string
	To          string
	CC          string
	ToMe        bool
	Subject     string
	ThreadKey   string
	At          int64
	Read        bool
	Flagged     bool
	Replied     bool
	PrincipalID string
	FirstSeenAt int64
	UpdatedAt   int64
}

const mailColumns = `id, mailbox, sender_email, sender_name, to_addresses, cc_addresses, to_me, subject,
	thread_key, at, read, flagged, replied, principal_id, first_seen_at, updated_at`

func scanMailMessage(scanner interface{ Scan(...any) error }) (MailMessageRow, error) {
	var row MailMessageRow
	var toMe, read, flagged, replied int
	err := scanner.Scan(&row.ID, &row.Mailbox, &row.SenderEmail, &row.SenderName, &row.To, &row.CC, &toMe,
		&row.Subject, &row.ThreadKey, &row.At, &read, &flagged, &replied, &row.PrincipalID,
		&row.FirstSeenAt, &row.UpdatedAt)
	row.ToMe, row.Read, row.Flagged, row.Replied = toMe != 0, read != 0, flagged != 0, replied != 0
	return row, err
}

// UpsertMailMessages writes the messages one read returned. A stored message
// keeps its first_seen_at and, when the new row names none, its person;
// everything else follows Mail. It returns the ids that were new.
func (s *Store) UpsertMailMessages(ctx context.Context, rows []MailMessageRow, now int64) ([]string, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	var added []string
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" || row.At <= 0 {
			continue
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_messages WHERE id = ?`, id).Scan(&exists); err != nil {
			return nil, fmt.Errorf("look up mail message: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mail_messages (`+mailColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				sender_email = excluded.sender_email,
				sender_name  = excluded.sender_name,
				to_addresses = excluded.to_addresses,
				cc_addresses = excluded.cc_addresses,
				to_me        = excluded.to_me,
				subject      = excluded.subject,
				thread_key   = excluded.thread_key,
				at           = excluded.at,
				read         = excluded.read,
				flagged      = excluded.flagged,
				replied      = excluded.replied,
				principal_id = CASE WHEN excluded.principal_id != '' THEN excluded.principal_id ELSE mail_messages.principal_id END,
				updated_at   = excluded.updated_at`,
			id, row.Mailbox, row.SenderEmail, row.SenderName, row.To, row.CC, boolInt(row.ToMe), row.Subject,
			row.ThreadKey, row.At, boolInt(row.Read), boolInt(row.Flagged), boolInt(row.Replied), row.PrincipalID,
			now, now); err != nil {
			return nil, fmt.Errorf("upsert mail message: %w", err)
		}
		if exists == 0 {
			added = append(added, id)
		}
	}
	return added, tx.Commit()
}

// ListMailMessages returns the messages at or after since, newest first.
func (s *Store) ListMailMessages(ctx context.Context, since int64) ([]MailMessageRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `SELECT `+mailColumns+` FROM mail_messages WHERE at >= ? ORDER BY at DESC, id ASC`, since)
	if err != nil {
		return nil, fmt.Errorf("list mail messages: %w", err)
	}
	defer rows.Close()
	var out []MailMessageRow
	for rows.Next() {
		row, err := scanMailMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteMailMessagesGone deletes the messages of one mailbox at or after
// since that a read no longer saw: moved out of the inbox, archived or
// deleted in Mail.
func (s *Store) DeleteMailMessagesGone(ctx context.Context, mailbox string, since int64, seen map[string]bool) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM mail_messages WHERE mailbox = ? AND at >= ?`, mailbox, since)
	if err != nil {
		return 0, fmt.Errorf("list mail messages: %w", err)
	}
	var gone []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if !seen[id] {
			gone = append(gone, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	var removed int64
	for _, id := range gone {
		res, err := s.db.ExecContext(ctx, `DELETE FROM mail_messages WHERE id = ?`, id)
		if err != nil {
			return removed, fmt.Errorf("delete mail message: %w", err)
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	return removed, nil
}

// PruneMailMessages deletes the messages before the cutoff.
func (s *Store) PruneMailMessages(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mail_messages WHERE at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("prune mail messages: %w", err)
	}
	return res.RowsAffected()
}

// DeleteAllMailMessages empties the table when Mail is explicitly protected.
func (s *Store) DeleteAllMailMessages(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM mail_messages`)
	if err != nil {
		return 0, fmt.Errorf("clear mail messages: %w", err)
	}
	return res.RowsAffected()
}
