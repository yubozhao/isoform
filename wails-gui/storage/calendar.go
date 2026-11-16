package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CalendarEventRetention is how long a past event without a linked meeting
// session is kept. Events linked to a session stay as long as the session.
const CalendarEventRetention = 90 * 24 * time.Hour

// CalendarEventRow is one calendar occurrence as stored. Times are Unix
// milliseconds. DeletedAt is set when a sync no longer sees the event.
type CalendarEventRow struct {
	ID             string
	UID            string
	LocalID        string
	CalendarID     string
	CalendarTitle  string
	Source         string
	Provider       string
	Title          string
	StartAt        int64
	EndAt          int64
	AllDay         bool
	TimeZone       string
	Location       string
	Lat            float64
	Lon            float64
	HasCoordinate  bool
	ConferenceURL  string
	ConferenceHost string
	URL            string
	Availability   string
	Status         string
	MyStatus       string
	OrganizerEmail string
	OrganizerName  string
	Recurring      bool
	AttendeeCount  int
	SessionID      string
	FirstSeenAt    int64
	UpdatedAt      int64
	DeletedAt      int64
	Attendees      []CalendarAttendeeRow
}

// CalendarAttendeeRow is one person on an event. PrincipalID is set when the
// address resolved to a known person.
type CalendarAttendeeRow struct {
	EventID     string
	Email       string
	Name        string
	Role        string
	Status      string
	IsMe        bool
	PrincipalID string
}

// CalendarSourceRow is one calendar account or web calendar the user uses.
type CalendarSourceRow struct {
	ID          string
	Kind        string
	Provider    string
	Title       string
	DetectedBy  string
	Calendars   int
	FirstSeenAt int64
	LastSeenAt  int64
	LastReadAt  int64
}

// MeetingFollowupRow is what a model pass found in one meeting transcript.
type MeetingFollowupRow struct {
	SessionID     string
	EventID       string
	Title         string
	DecisionsJSON string
	FollowupsJSON string
	CreatedAt     int64
}

const calendarEventColumns = `id, uid, local_id, calendar_id, calendar_title, source, provider, title,
	start_at, end_at, all_day, time_zone, location, lat, lon, has_coordinate,
	conference_url, conference_host, url, availability, status, my_status,
	organizer_email, organizer_name, recurring, attendee_count, session_id,
	first_seen_at, updated_at, deleted_at`

func scanCalendarEvent(scanner interface{ Scan(...any) error }) (CalendarEventRow, error) {
	var row CalendarEventRow
	var allDay, hasCoordinate, recurring int
	err := scanner.Scan(
		&row.ID, &row.UID, &row.LocalID, &row.CalendarID, &row.CalendarTitle, &row.Source, &row.Provider, &row.Title,
		&row.StartAt, &row.EndAt, &allDay, &row.TimeZone, &row.Location, &row.Lat, &row.Lon, &hasCoordinate,
		&row.ConferenceURL, &row.ConferenceHost, &row.URL, &row.Availability, &row.Status, &row.MyStatus,
		&row.OrganizerEmail, &row.OrganizerName, &recurring, &row.AttendeeCount, &row.SessionID,
		&row.FirstSeenAt, &row.UpdatedAt, &row.DeletedAt,
	)
	row.AllDay = allDay != 0
	row.HasCoordinate = hasCoordinate != 0
	row.Recurring = recurring != 0
	return row, err
}

// UpsertCalendarEvents writes the occurrences one sync read. An existing row
// keeps its first_seen_at and its session link; everything else follows the
// source, and a row seen again is undeleted. Attendees are replaced.
func (s *Store) UpsertCalendarEvents(ctx context.Context, rows []CalendarEventRow, now int64) error {
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
		id := strings.TrimSpace(row.ID)
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO calendar_events (`+calendarEventColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
			ON CONFLICT(id) DO UPDATE SET
				uid = excluded.uid, local_id = excluded.local_id,
				calendar_id = excluded.calendar_id, calendar_title = excluded.calendar_title,
				source = excluded.source, provider = excluded.provider, title = excluded.title,
				start_at = excluded.start_at, end_at = excluded.end_at, all_day = excluded.all_day,
				time_zone = excluded.time_zone, location = excluded.location,
				lat = excluded.lat, lon = excluded.lon, has_coordinate = excluded.has_coordinate,
				conference_url = excluded.conference_url, conference_host = excluded.conference_host,
				url = excluded.url, availability = excluded.availability, status = excluded.status,
				my_status = excluded.my_status, organizer_email = excluded.organizer_email,
				organizer_name = excluded.organizer_name, recurring = excluded.recurring,
				attendee_count = excluded.attendee_count,
				session_id = CASE WHEN excluded.session_id != '' THEN excluded.session_id ELSE calendar_events.session_id END,
				updated_at = excluded.updated_at, deleted_at = 0
		`,
			id, row.UID, row.LocalID, row.CalendarID, row.CalendarTitle, row.Source, row.Provider, row.Title,
			row.StartAt, row.EndAt, boolInt(row.AllDay), row.TimeZone, row.Location, row.Lat, row.Lon, boolInt(row.HasCoordinate),
			row.ConferenceURL, row.ConferenceHost, row.URL, row.Availability, row.Status, row.MyStatus,
			row.OrganizerEmail, row.OrganizerName, boolInt(row.Recurring), row.AttendeeCount, row.SessionID,
			now, now,
		); err != nil {
			return fmt.Errorf("upsert calendar event %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_event_attendees WHERE event_id = ?`, id); err != nil {
			return fmt.Errorf("clear attendees %s: %w", id, err)
		}
		for _, att := range row.Attendees {
			if att.Email == "" && att.Name == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT OR REPLACE INTO calendar_event_attendees (event_id, email, name, role, status, is_me, principal_id)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, id, att.Email, att.Name, att.Role, att.Status, boolInt(att.IsMe), att.PrincipalID); err != nil {
				return fmt.Errorf("insert attendee %s: %w", id, err)
			}
		}
	}
	return tx.Commit()
}

// MarkCalendarEventsVanished marks the live events of one source (and, when
// provider is not empty, one provider) that overlap [windowStart, windowEnd)
// and are not among the seen ids as deleted at now. It returns how many rows
// changed.
func (s *Store) MarkCalendarEventsVanished(ctx context.Context, source, provider string, windowStart, windowEnd int64, seen map[string]bool, now int64) (int64, error) {
	query := `SELECT id FROM calendar_events WHERE source = ? AND deleted_at = 0 AND start_at < ? AND end_at > ?`
	args := []any{source, windowEnd, windowStart}
	if provider = strings.TrimSpace(provider); provider != "" {
		query += ` AND provider = ?`
		args = append(args, provider)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("list window events: %w", err)
	}
	var vanished []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		if !seen[id] {
			vanished = append(vanished, id)
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
	for _, id := range vanished {
		res, err := tx.ExecContext(ctx, `UPDATE calendar_events SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at = 0`, now, now, id)
		if err != nil {
			return 0, fmt.Errorf("mark vanished %s: %w", id, err)
		}
		n, _ := res.RowsAffected()
		changed += n
	}
	return changed, tx.Commit()
}

// ListCalendarEvents returns the events overlapping [from, to), oldest first,
// with their attendees. Deleted events are included only when asked.
func (s *Store) ListCalendarEvents(ctx context.Context, from, to int64, includeDeleted bool) ([]CalendarEventRow, error) {
	query := `SELECT ` + calendarEventColumns + ` FROM calendar_events WHERE start_at < ? AND end_at > ?`
	if !includeDeleted {
		query += ` AND deleted_at = 0`
	}
	query += ` ORDER BY start_at ASC, title ASC`
	rows, err := s.db.QueryContext(ctx, query, to, from)
	if err != nil {
		return nil, fmt.Errorf("list calendar events: %w", err)
	}
	defer rows.Close()
	var out []CalendarEventRow
	for rows.Next() {
		row, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.attachCalendarAttendees(ctx, out)
}

// GetCalendarEvent loads one event with its attendees.
func (s *Store) GetCalendarEvent(ctx context.Context, id string) (CalendarEventRow, bool, error) {
	row, err := scanCalendarEvent(s.db.QueryRowContext(ctx, `SELECT `+calendarEventColumns+` FROM calendar_events WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CalendarEventRow{}, false, nil
	}
	if err != nil {
		return CalendarEventRow{}, false, err
	}
	out, err := s.attachCalendarAttendees(ctx, []CalendarEventRow{row})
	if err != nil {
		return CalendarEventRow{}, false, err
	}
	return out[0], true, nil
}

// ListCalendarEventsForSession returns the events linked to a meeting session.
func (s *Store) ListCalendarEventsForSession(ctx context.Context, sessionID string) ([]CalendarEventRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+calendarEventColumns+` FROM calendar_events WHERE session_id = ? ORDER BY start_at ASC`, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarEventRow
	for rows.Next() {
		row, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.attachCalendarAttendees(ctx, out)
}

// SetCalendarEventSession links an event to the activity session of the
// meeting it turned out to be.
func (s *Store) SetCalendarEventSession(ctx context.Context, eventID, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE calendar_events SET session_id = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(sessionID), time.Now().UnixMilli(), strings.TrimSpace(eventID))
	return err
}

const ownCalendarEmailsSQL = `SELECT lower(email) FROM calendar_event_attendees WHERE is_me != 0 AND email != ''`

// CalendarOwnEmails returns, lower-cased, every address a calendar has
// reported as the current user.
func (s *Store) CalendarOwnEmails(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT `+strings.TrimPrefix(ownCalendarEmailsSQL, "SELECT "))
	if err != nil {
		return nil, fmt.Errorf("own calendar emails: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out[email] = true
	}
	return out, rows.Err()
}

func (s *Store) attachCalendarAttendees(ctx context.Context, events []CalendarEventRow) ([]CalendarEventRow, error) {
	if len(events) == 0 {
		return events, nil
	}
	index := make(map[string]int, len(events))
	ids := make([]any, 0, len(events))
	placeholders := make([]string, 0, len(events))
	for i, ev := range events {
		index[ev.ID] = i
		ids = append(ids, ev.ID)
		placeholders = append(placeholders, "?")
	}
	// An address the calendar reported as the current user on any event is
	// the person on every event: rows stored before the sync marked a second
	// account's address read it as a guest.
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, email, name, role, status, me, CASE WHEN me = 1 THEN '' ELSE principal_id END
		FROM (SELECT a.*, CASE WHEN a.is_me != 0 OR (a.email != '' AND lower(a.email) IN (`+ownCalendarEmailsSQL+`)) THEN 1 ELSE 0 END AS me
		      FROM calendar_event_attendees a WHERE a.event_id IN (`+strings.Join(placeholders, ",")+`))
		ORDER BY me DESC, email ASC, name ASC
	`, ids...)
	if err != nil {
		return nil, fmt.Errorf("list attendees: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var att CalendarAttendeeRow
		var isMe int
		if err := rows.Scan(&att.EventID, &att.Email, &att.Name, &att.Role, &att.Status, &isMe, &att.PrincipalID); err != nil {
			return nil, err
		}
		att.IsMe = isMe != 0
		if i, ok := index[att.EventID]; ok {
			events[i].Attendees = append(events[i].Attendees, att)
		}
	}
	return events, rows.Err()
}

// PruneCalendarEvents deletes past events that ended before cutoff and have no
// session link. It returns how many rows were removed.
func (s *Store) PruneCalendarEvents(ctx context.Context, cutoff int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM calendar_events WHERE end_at < ? AND session_id = ''`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpsertCalendarSource records a calendar source. An existing row keeps its
// first sighting and the way it was first detected; the last sighting, the
// title and the calendar count follow the newer record, and last_read_at only
// moves forward.
func (s *Store) UpsertCalendarSource(ctx context.Context, row CalendarSourceRow) error {
	id := strings.TrimSpace(row.ID)
	if id == "" {
		return errors.New("calendar source id is required")
	}
	if row.FirstSeenAt <= 0 {
		row.FirstSeenAt = row.LastSeenAt
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO calendar_sources (id, kind, provider, title, detected_by, calendars, first_seen_at, last_seen_at, last_read_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind, provider = excluded.provider,
			title = CASE WHEN excluded.title != '' THEN excluded.title ELSE calendar_sources.title END,
			calendars = excluded.calendars,
			first_seen_at = MIN(calendar_sources.first_seen_at, excluded.first_seen_at),
			last_seen_at = MAX(calendar_sources.last_seen_at, excluded.last_seen_at),
			last_read_at = MAX(calendar_sources.last_read_at, excluded.last_read_at)
	`, id, row.Kind, row.Provider, row.Title, row.DetectedBy, row.Calendars, row.FirstSeenAt, row.LastSeenAt, row.LastReadAt)
	return err
}

// TouchCalendarSourceRead records that events were read from a source at the
// given time.
func (s *Store) TouchCalendarSourceRead(ctx context.Context, id string, at int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE calendar_sources SET last_read_at = MAX(last_read_at, ?) WHERE id = ?`, at, strings.TrimSpace(id))
	return err
}

// ListCalendarSources returns every known source, EventKit accounts first.
func (s *Store) ListCalendarSources(ctx context.Context) ([]CalendarSourceRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, provider, title, detected_by, calendars, first_seen_at, last_seen_at, last_read_at
		FROM calendar_sources ORDER BY kind ASC, provider ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CalendarSourceRow
	for rows.Next() {
		var row CalendarSourceRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.Provider, &row.Title, &row.DetectedBy, &row.Calendars, &row.FirstSeenAt, &row.LastSeenAt, &row.LastReadAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteCalendarSourcesOfKind removes sources of one kind whose id is not in
// keep, so accounts removed from macOS disappear from the list.
func (s *Store) DeleteCalendarSourcesOfKind(ctx context.Context, kind string, keep map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM calendar_sources WHERE kind = ?`, kind)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM calendar_sources WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// SaveMeetingFollowup stores the follow-up extraction for a meeting session.
func (s *Store) SaveMeetingFollowup(ctx context.Context, row MeetingFollowupRow) error {
	if strings.TrimSpace(row.SessionID) == "" {
		return errors.New("meeting followup needs a session id")
	}
	if row.CreatedAt <= 0 {
		row.CreatedAt = time.Now().UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO meeting_followups (session_id, event_id, title, decisions_json, followups_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			event_id = excluded.event_id, title = excluded.title,
			decisions_json = excluded.decisions_json, followups_json = excluded.followups_json,
			created_at = excluded.created_at
	`, strings.TrimSpace(row.SessionID), row.EventID, row.Title, row.DecisionsJSON, row.FollowupsJSON, row.CreatedAt)
	return err
}

// GetMeetingFollowup loads the extraction for a session.
func (s *Store) GetMeetingFollowup(ctx context.Context, sessionID string) (MeetingFollowupRow, bool, error) {
	var row MeetingFollowupRow
	err := s.db.QueryRowContext(ctx, `
		SELECT session_id, event_id, title, decisions_json, followups_json, created_at
		FROM meeting_followups WHERE session_id = ?
	`, strings.TrimSpace(sessionID)).Scan(&row.SessionID, &row.EventID, &row.Title, &row.DecisionsJSON, &row.FollowupsJSON, &row.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MeetingFollowupRow{}, false, nil
	}
	if err != nil {
		return MeetingFollowupRow{}, false, err
	}
	return row, true, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// DeleteAllCalendarEvents removes every stored copy of a calendar event and
// its attendees (Your data's Forget everything). The calendar sources, the
// accounts the person chose, stay; the next sync reads the calendars again
// only while calendar reading is on.
func (s *Store) DeleteAllCalendarEvents(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{`DELETE FROM calendar_event_attendees`, `DELETE FROM calendar_events`} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}
