package store

import (
	"context"
	"log"
	"time"

	"isoform-relay/internal/push"
)

// Swept counts the rows one maintenance pass removed.
type Swept struct {
	Pairings, Requests, Devices, Briefs, Pushes, Receipts, Attachments, SMS, Email int64
}

// Sweep removes expired pairings, requests and devices, briefs beyond the
// newest KeptBriefs due and KeptHeldBriefs held per device, finished push rows
// older than a day, notice receipts older than NoticeReceiptKeep, expired
// attachments, and the text message rows sweepSMS names.
func (s *Store) Sweep(ctx context.Context, now time.Time) (Swept, error) {
	var swept Swept
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return swept, err
	}
	defer tx.Rollback()
	steps := []struct {
		query string
		count *int64
		args  []any
	}{
		{"DELETE FROM pairings WHERE expires_at<=?", &swept.Pairings, []any{millis(now)}},
		{"DELETE FROM envelopes WHERE dir='d2h' AND expires_at<=?", &swept.Requests, []any{millis(now)}},
		{"DELETE FROM devices WHERE expires_at<=?", &swept.Devices, []any{millis(now)}},
		{"DELETE FROM notice_receipts WHERE received_at<=?", &swept.Receipts, []any{millis(now.Add(-NoticeReceiptKeep))}},
		// An email code is kept a day after it expired, so the hub can still
		// be told so, then it and its sealed address go.
		{"DELETE FROM email_verifications WHERE expires_at<=?", &swept.Email, []any{millis(now.Add(-emailVerificationKeep))}},
	}
	for _, step := range steps {
		result, err := tx.ExecContext(ctx, step.query, step.args...)
		if err != nil {
			return Swept{}, err
		}
		if *step.count, err = result.RowsAffected(); err != nil {
			return Swept{}, err
		}
	}
	if swept.Briefs, err = retainBriefs(ctx, tx, now, ""); err != nil {
		return Swept{}, err
	}
	if swept.Pushes, err = push.Sweep(ctx, tx, now.Add(-push.Expiry)); err != nil {
		return Swept{}, err
	}
	if swept.SMS, err = sweepSMS(ctx, tx, now); err != nil {
		return Swept{}, err
	}
	if err = tx.Commit(); err != nil {
		return Swept{}, err
	}
	// Attachments touch the file system, so they go after the transaction.
	if swept.Attachments, err = s.sweepAttachments(ctx, now); err != nil {
		return Swept{}, err
	}
	return swept, nil
}

// checkpointEvery is how many maintenance passes separate WAL checkpoints.
const checkpointEvery = 60

// Checkpoint folds the write-ahead log into the database file and truncates
// it, so the log cannot outgrow the state volume while readers keep it open.
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Maintain runs Sweep every interval until ctx ends, and a Checkpoint every
// checkpointEvery passes. It logs counts only.
func (s *Store) Maintain(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	passes := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			swept, err := s.Sweep(ctx, time.Now())
			if err != nil {
				log.Print("maintenance pass failed; it will retry")
				continue
			}
			if total := swept.Pairings + swept.Requests + swept.Devices + swept.Briefs + swept.Pushes + swept.Receipts + swept.Attachments + swept.SMS + swept.Email; total > 0 {
				log.Printf("maintenance removed %d expired or superseded rows", total)
			}
			if passes++; passes%checkpointEvery == 0 {
				if err := s.Checkpoint(ctx); err != nil {
					log.Print("write-ahead log checkpoint failed; it will retry")
				}
			}
		}
	}
}
