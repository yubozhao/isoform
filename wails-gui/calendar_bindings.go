package main

import (
	"context"
	"errors"
	"log"
	"net/url"
	"strings"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/storage"
)

// calendarPeekEnabledKey is the ui_state key behind "Read web calendars when
// the Mac is idle". Absent means on.
const calendarPeekEnabledKey = "calendar_peek_enabled"

// calendarPeekLastKey is the ui_state key holding when a web calendar was
// last read, as RFC 3339.
const calendarPeekLastKey = "calendar_peek_last_at"

// CalendarSourceView is one row of "Calendars Isoform can see".
type CalendarSourceView struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`     // eventkit | web
	Provider      string `json:"provider"` // icloud | google | exchange | outlook | notion | fantastical | local | subscribed | other
	ProviderLabel string `json:"providerLabel"`
	Title         string `json:"title"`
	DetectedBy    string `json:"detectedBy"` // eventkit | history | screen | peek
	Calendars     int    `json:"calendars"`
	LastSeenAt    string `json:"lastSeenAt"`
	LastReadAt    string `json:"lastReadAt"`
}

// CalendarOverview is what Settings > Observation shows about calendars.
type CalendarOverview struct {
	Supported     bool                 `json:"supported"`
	Authorization string               `json:"authorization"` // granted | denied | restricted | write_only | not_determined | not_applicable
	Granted       bool                 `json:"granted"`
	Enabled       bool                 `json:"enabled"`
	LastSyncAt    string               `json:"lastSyncAt"`
	LastError     string               `json:"lastError"`
	UpcomingCount int                  `json:"upcomingCount"`
	Sources       []CalendarSourceView `json:"sources"`
	PeekEnabled   bool                 `json:"peekEnabled"`
	PeekLastAt    string               `json:"peekLastAt"`
	// PeekProviders are the web calendars a peek would read right now: used
	// in the browser lately and not among the macOS accounts.
	PeekProviders []string `json:"peekProviders"`
}

// GetCalendarOverview reports access, the known calendar sources and the
// state of web calendar reading.
func (a *App) GetCalendarOverview() (CalendarOverview, error) {
	ctx := context.Background()
	view := CalendarOverview{
		Supported:     systemCalendarsSupported(),
		Authorization: calendarAuthorization(),
		Granted:       calendarAccessGranted(),
		Sources:       []CalendarSourceView{},
		PeekProviders: []string{},
	}
	if a == nil || a.store == nil {
		return view, errors.New("the store is not ready")
	}
	// A switch that can't be read fails the read; the page shows it with Try
	// again, never as on.
	enabled, err := a.permissionSwitchRead(context.Background(), calendarPermissionKey)
	if err != nil {
		return view, err
	}
	view.Enabled = enabled
	view.PeekEnabled = a.calendarPeekEnabled()
	if last, lastErr := a.calendarSync.status(); !last.IsZero() {
		view.LastSyncAt = last.UTC().Format(time.RFC3339)
		view.LastError = lastErr
	} else if lastErr != "" {
		view.LastError = lastErr
	}
	if view.LastSyncAt == "" {
		if stored, err := a.store.GetUIState(ctx, calendarLastSyncKey); err == nil {
			view.LastSyncAt = stored
		}
	}
	rows, err := a.store.ListCalendarSources(ctx)
	if err != nil {
		return view, err
	}
	now := time.Now()
	var sources []calendar.Source
	for _, row := range rows {
		view.Sources = append(view.Sources, calendarSourceView(row))
		sources = append(sources, calendarSourceFromRow(row))
	}
	view.PeekProviders = calendar.WebCalendarsToPeek(sources, now, calendar.PeekRecentUse)
	if view.PeekProviders == nil {
		view.PeekProviders = []string{}
	}
	if stored, err := a.store.GetUIState(ctx, calendarPeekLastKey); err == nil {
		view.PeekLastAt = stored
	}
	events, err := a.listCalendarEvents(ctx, now, now.Add(calendarWindowAhead))
	if err == nil {
		view.UpcomingCount = len(events)
	}
	return view, nil
}

// SetCalendarPeekEnabled turns web calendar reading on or off.
func (a *App) SetCalendarPeekEnabled(enabled bool) error {
	if a == nil || a.store == nil {
		return errors.New("the store is not ready")
	}
	value := "true"
	if !enabled {
		value = "false"
	}
	if err := a.store.SetUIState(context.Background(), calendarPeekEnabledKey, value); err != nil {
		return err
	}
	if !enabled {
		a.calendarPeek.cancelRun()
	}
	log.Printf("[calendar] web calendar reading switched %t by user", enabled)
	a.emitBrowserEvent("calendar:synced")
	return nil
}

// calendarPeekEnabled reads the switch; absent means on.
func (a *App) calendarPeekEnabled() bool {
	if a == nil || a.store == nil {
		return false
	}
	value, err := a.store.GetUIState(context.Background(), calendarPeekEnabledKey)
	if err != nil || value == "" {
		return true
	}
	return value != "false"
}

// SyncCalendarsNow reads the calendars right away. It is the Refresh button.
func (a *App) SyncCalendarsNow() error {
	if a == nil || a.calendarSync == nil {
		return errCalendarUnsupported
	}
	if !calendarAccessGranted() {
		return errCalendarNotAuthorized
	}
	if !a.captureAllowed(context.Background(), "calendar_sync", calendarPermissionKey) {
		return errors.New("calendar reading is switched off")
	}
	_, err := a.calendarSync.Sync(context.Background(), "refresh")
	return err
}

func calendarSourceView(row storage.CalendarSourceRow) CalendarSourceView {
	view := CalendarSourceView{
		ID: row.ID, Kind: row.Kind, Provider: row.Provider, ProviderLabel: calendar.ProviderLabel(row.Provider),
		Title: row.Title, DetectedBy: row.DetectedBy, Calendars: row.Calendars,
	}
	if row.LastSeenAt > 0 {
		view.LastSeenAt = time.UnixMilli(row.LastSeenAt).UTC().Format(time.RFC3339)
	}
	if row.LastReadAt > 0 {
		view.LastReadAt = time.UnixMilli(row.LastReadAt).UTC().Format(time.RFC3339)
	}
	return view
}

func calendarSourceFromRow(row storage.CalendarSourceRow) calendar.Source {
	src := calendar.Source{
		ID: row.ID, Kind: row.Kind, Provider: row.Provider, Title: row.Title, DetectedBy: row.DetectedBy, Calendars: row.Calendars,
	}
	if row.FirstSeenAt > 0 {
		src.FirstSeen = time.UnixMilli(row.FirstSeenAt)
	}
	if row.LastSeenAt > 0 {
		src.LastSeen = time.UnixMilli(row.LastSeenAt)
	}
	if row.LastReadAt > 0 {
		src.LastRead = time.UnixMilli(row.LastReadAt)
	}
	return src
}

// UpcomingCalendarEvent is one event the main chat's hub lists: the next
// meetings, read from the synced calendar_events table.
type UpcomingCalendarEvent struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Start         int64  `json:"start"` // unix ms
	End           int64  `json:"end"`   // unix ms
	Location      string `json:"location"`
	CalendarTitle string `json:"calendarTitle"`
	AllDay        bool   `json:"allDay"`
	ConferenceURL string `json:"conferenceUrl"`
}

const upcomingCalendarMaxLimit = 50

// GetUpcomingCalendarEvents returns up to limit timed events that have not
// ended, soonest first: those starting within the next 24 hours, or, when
// there are none, those through the end of tomorrow. All-day, deleted,
// canceled and declined events are left out, and a conference URL is kept
// only when it is http(s). Read-only; it never asks for calendar access.
func (a *App) GetUpcomingCalendarEvents(limit int) (perfValue []UpcomingCalendarEvent, perfErr error) {
	defer logPageBinding("GetUpcomingCalendarEvents", time.Now(), &perfValue)
	if a == nil || a.store == nil {
		return []UpcomingCalendarEvent{}, errors.New("the store is not ready")
	}
	return a.upcomingCalendarEventsAt(time.Now(), limit)
}

func (a *App) upcomingCalendarEventsAt(now time.Time, limit int) ([]UpcomingCalendarEvent, error) {
	if limit <= 0 {
		limit = 3
	}
	if limit > upcomingCalendarMaxLimit {
		limit = upcomingCalendarMaxLimit
	}
	ctx := context.Background()
	y, m, d := now.Date()
	endOfTomorrow := time.Date(y, m, d+2, 0, 0, 0, 0, now.Location())
	for _, until := range []time.Time{now.Add(24 * time.Hour), endOfTomorrow} {
		rows, err := a.store.ListCalendarEvents(ctx, now.UnixMilli(), until.UnixMilli(), false)
		if err != nil {
			return []UpcomingCalendarEvent{}, err
		}
		if out := upcomingCalendarEvents(rows, limit); len(out) > 0 {
			return out, nil
		}
	}
	return []UpcomingCalendarEvent{}, nil
}

// upcomingCalendarEvents filters and shapes stored rows for the hub.
func upcomingCalendarEvents(rows []storage.CalendarEventRow, limit int) []UpcomingCalendarEvent {
	out := []UpcomingCalendarEvent{}
	for _, row := range rows {
		if row.AllDay || row.DeletedAt != 0 || row.Status == "canceled" || row.MyStatus == calendar.StatusDeclined {
			continue
		}
		out = append(out, UpcomingCalendarEvent{
			ID:            row.ID,
			Title:         row.Title,
			Start:         row.StartAt,
			End:           row.EndAt,
			Location:      row.Location,
			CalendarTitle: row.CalendarTitle,
			AllDay:        row.AllDay,
			ConferenceURL: safeConferenceURL(row.ConferenceURL),
		})
		if len(out) == limit {
			break
		}
	}
	return out
}

// safeConferenceURL keeps a calendar-supplied link only when it is an http or
// https URL with a host. Anyone who sends an invite controls that string, so
// file:, javascript: and app-scheme links never reach the OS opener.
func safeConferenceURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.String()
	default:
		return ""
	}
}
