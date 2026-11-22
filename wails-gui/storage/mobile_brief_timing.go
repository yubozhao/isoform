package storage

import "context"

// CalendarTimingRow contains only the clock range and the provider identifier
// needed to recheck source exclusions. No calendar content is selected.
type CalendarTimingRow struct {
	StartAt  int64
	EndAt    int64
	Provider string
}

// ListNativeCalendarTiming excludes web observations and linked meeting
// sessions: establishing their privacy would require reading activity content.
func (s *Store) ListNativeCalendarTiming(ctx context.Context, from, to int64) ([]CalendarTimingRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT start_at, end_at, provider FROM calendar_events
		WHERE end_at > ? AND start_at < ? AND end_at > start_at
		  AND source = 'eventkit' AND session_id = '' AND deleted_at = 0
		  AND all_day = 0 AND status != 'canceled' AND my_status != 'declined'
		  AND availability != 'free'
		ORDER BY start_at, end_at`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarTimingRow
	for rows.Next() {
		var row CalendarTimingRow
		if err := rows.Scan(&row.StartAt, &row.EndAt, &row.Provider); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ListMobileBriefOpenTimes reads explicit notification opens from the Mac and
// phone. Delivery, polling, other notices and background presence are not opens.
func (s *Store) ListMobileBriefOpenTimes(ctx context.Context, from, to int64) ([]int64, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT DISTINCT r.at FROM engagement_receipts r
		JOIN engagement_moments m ON m.id = r.moment_id
		WHERE m.kind = 'brief_ready' AND r.state = 'opened'
		  AND r.channel IN ('desktop', 'push') AND r.at >= ? AND r.at < ?
		ORDER BY r.at`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}
