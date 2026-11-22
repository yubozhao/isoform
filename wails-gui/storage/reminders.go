package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReminderRetention is how long a completed or deleted reminder is kept.
const ReminderRetention = 90 * 24 * time.Hour

// ReminderRow is one reminder as stored. Times are Unix milliseconds; DueAt
// is 0 without a due date, and local midnight of the due day when DueAllDay
// is set. CompletedAt is set once the reminder is ticked off, DeletedAt once
// a read no longer sees it.
type ReminderRow struct {
	UID         string
	Title       string
	DueAt       int64
	DueAllDay   bool
	List        string
	Priority    int
	CompletedAt int64
	FirstSeenAt int64
	UpdatedAt   int64
	DeletedAt   int64
}

const reminderColumns = `uid, title, due_at, due_all_day, list, priority, completed_at, first_seen_at, updated_at, deleted_at`

func scanReminder(scanner interface{ Scan(...any) error }) (ReminderRow, error) {
	var row ReminderRow
	var allDay int
	err := scanner.Scan(&row.UID, &row.Title, &row.DueAt, &allDay, &row.List, &row.Priority,
		&row.CompletedAt, &row.FirstSeenAt, &row.UpdatedAt, &row.DeletedAt)
	row.DueAllDay = allDay != 0
	return row, err
}

// UpsertReminders writes the reminders one read returned. An existing row
// keeps its first_seen_at; everything else follows the source, and a row
// seen again is undeleted.
func (s *Store) UpsertReminders(ctx context.Context, rows []ReminderRow, now int64) error {
	if len(rows) == 0 {
		return nil
	}
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	for _, row := range rows {
		uid := strings.TrimSpace(row.UID)
		if uid == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO reminders (`+reminderColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
			ON CONFLICT(uid) DO UPDATE SET
				title = excluded.title, due_at = excluded.due_at, due_all_day = excluded.due_all_day,
				list = excluded.list, priority = excluded.priority, completed_at = excluded.completed_at,
				updated_at = excluded.updated_at, deleted_at = 0
		`, uid, row.Title, row.DueAt, boolInt(row.DueAllDay), row.List, row.Priority, row.CompletedAt, now, now); err != nil {
			return fmt.Errorf("upsert reminder %s: %w", uid, err)
		}
	}
	return tx.Commit()
}

// MarkRemindersVanished marks the live reminders a read did not return as
// deleted at now. A read covers every open reminder and the ones completed
// since completedSince, so an older completed reminder that simply fell out
// of that window is left alone. It returns how many rows changed.
func (s *Store) MarkRemindersVanished(ctx context.Context, seen map[string]bool, completedSince, now int64) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT uid FROM reminders
		WHERE deleted_at = 0 AND (completed_at = 0 OR completed_at >= ?)
	`, completedSince)
	if err != nil {
		return 0, fmt.Errorf("list live reminders: %w", err)
	}
	var vanished []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			rows.Close()
			return 0, err
		}
		if !seen[uid] {
			vanished = append(vanished, uid)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(vanished) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var changed int64
	for _, uid := range vanished {
		res, err := tx.ExecContext(ctx, `UPDATE reminders SET deleted_at = ?, updated_at = ? WHERE uid = ? AND deleted_at = 0`, now, now, uid)
		if err != nil {
			return 0, fmt.Errorf("mark reminder vanished %s: %w", uid, err)
		}
		n, _ := res.RowsAffected()
		changed += n
	}
	return changed, tx.Commit()
}

// ListReminders returns the reminders, open ones first by due time (undated
// last), then the rest by last update. Completed and deleted reminders are
// included only when asked.
func (s *Store) ListReminders(ctx context.Context, includeClosed bool) ([]ReminderRow, error) {
	query := `SELECT ` + reminderColumns + ` FROM reminders`
	if !includeClosed {
		query += ` WHERE deleted_at = 0 AND completed_at = 0`
	}
	query += ` ORDER BY (deleted_at = 0 AND completed_at = 0) DESC, due_at = 0, due_at ASC, updated_at DESC, uid ASC`
	rows, err := s.committedReader().QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list reminders: %w", err)
	}
	defer rows.Close()
	var out []ReminderRow
	for rows.Next() {
		row, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// PruneReminders deletes the reminders completed or deleted before cutoff.
func (s *Store) PruneReminders(ctx context.Context, cutoff int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM reminders
		WHERE (deleted_at > 0 AND deleted_at < ?) OR (deleted_at = 0 AND completed_at > 0 AND completed_at < ?)
	`, cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune reminders: %w", err)
	}
	return res.RowsAffected()
}

// DeleteAllReminders removes every stored reminder. It runs when reading is
// switched off or access is gone, so nothing read earlier is kept.
func (s *Store) DeleteAllReminders(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM reminders`)
	if err != nil {
		return 0, fmt.Errorf("delete reminders: %w", err)
	}
	return res.RowsAffected()
}
