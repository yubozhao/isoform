package storage

import (
	"context"
	"fmt"
)

// CountCalendarEventsRead counts the calendar events of one source that a
// sync read at or after weekStart, and of those the ones read at or after
// todayStart. Every sync rewrites updated_at for each event it reads (the
// whole window it reads, upcoming events included), so updated_at is the
// last read and a row read in the window is never missed. A removed event is
// left out: MarkCalendarEventsVanished sets its updated_at to when it was
// found missing, which is not a read.
//
// It reads calendar_events without an index on updated_at on purpose: the
// table holds at most CalendarEventRetention of events (plus the few linked
// to a meeting), and such an index would be rewritten for every row on every
// fifteen-minute sync.
func (s *Store) CountCalendarEventsRead(ctx context.Context, source string, todayStart, weekStart int64) (today, week int64, err error) {
	err = s.committedReader().QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(updated_at >= ?), 0)
		FROM calendar_events
		WHERE source = ? AND updated_at >= ? AND deleted_at = 0`,
		todayStart, source, weekStart,
	).Scan(&week, &today)
	if err != nil {
		return 0, 0, fmt.Errorf("count calendar events read: %w", err)
	}
	return today, week, nil
}
