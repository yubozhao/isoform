package main

import (
	"context"
	"errors"
	"sort"
	"time"

	"wails-gui/internal/calendar"
)

const (
	calendarOfferHoldsKey     = "calendar.offer_holds"
	calendarHoldCalendarIDKey = "calendar.hold_calendar_id"
)

// HoldSettings is the persisted offer preference and the calendars EventKit
// reports writable. The stored calendar is never accepted without rechecking.
type HoldSettings struct {
	Granted     bool           `json:"granted"`
	OfferHolds  bool           `json:"offerHolds"`
	CalendarID  string         `json:"calendarId"`
	Calendars   []holdCalendar `json:"calendars"`
	BrowserOnly []string       `json:"browserOnly"`
}

// holdMeetingsDefault picks the busiest meetings account and then its most
// used writable calendar. Stable input order resolves ties.
func holdMeetingsDefault(calendars []holdCalendar, events []calendar.RawEvent, now time.Time) string {
	meetings := map[string]int{}
	counts := map[string]int{}
	for _, event := range events {
		start, err := time.Parse(time.RFC3339, event.Start)
		if err != nil || start.Before(now.AddDate(0, 0, -30)) || !start.Before(now) || event.SourceID == "" {
			continue
		}
		counts[event.CalendarID]++
		for _, attendee := range event.Attendees {
			if !attendee.IsMe {
				meetings[event.SourceID]++
				break
			}
		}
	}
	var account string
	bestMeetings := 0
	accounts := make([]string, 0, len(meetings))
	for id := range meetings {
		accounts = append(accounts, id)
	}
	sort.Strings(accounts)
	for _, id := range accounts {
		if meetings[id] > bestMeetings {
			account, bestMeetings = id, meetings[id]
		}
	}
	var selected string
	bestCount := -1
	for i := range calendars {
		cal := &calendars[i]
		cal.MeetingsAccount = account != "" && cal.SourceID == account
		if cal.MeetingsAccount && counts[cal.ID] > bestCount {
			selected, bestCount = cal.ID, counts[cal.ID]
		}
	}
	return selected
}

func (a *App) holdSettings(store holdEventStore, granted bool) (HoldSettings, error) {
	out := HoldSettings{Granted: granted, OfferHolds: true, Calendars: []holdCalendar{}, BrowserOnly: []string{}}
	if a == nil || a.store == nil {
		return out, errors.New("the store is not ready")
	}
	ctx := context.Background()
	value, err := a.store.GetUIState(ctx, calendarOfferHoldsKey)
	if err != nil {
		return out, err
	}
	out.OfferHolds = value != "false"
	if !granted {
		return out, nil
	}
	rows, err := a.store.ListCalendarSources(ctx)
	if err != nil {
		return out, err
	}
	sources := make([]calendar.Source, 0, len(rows))
	for _, row := range rows {
		sources = append(sources, calendarSourceFromRow(row))
	}
	out.BrowserOnly = calendar.WebCalendarsToPeek(sources, time.Now(), calendar.PeekRecentUse)
	out.Calendars, err = store.WritableCalendars()
	if err != nil {
		return out, err
	}
	if out.Calendars == nil {
		out.Calendars = []holdCalendar{}
	}
	now := time.Now()
	events, readErr := store.ReadWindow(now.AddDate(0, 0, -30), now)
	meetingsDefault := ""
	if readErr == nil {
		meetingsDefault = holdMeetingsDefault(out.Calendars, events, now)
	}
	selected, err := a.store.GetUIState(ctx, calendarHoldCalendarIDKey)
	if err != nil {
		return out, err
	}
	for _, cal := range out.Calendars {
		if cal.ID == selected {
			out.CalendarID = selected
			break
		}
	}
	if out.CalendarID == "" {
		out.CalendarID = meetingsDefault
	}
	if out.CalendarID == "" {
		for _, cal := range out.Calendars {
			if cal.IsDefault {
				out.CalendarID = cal.ID
				break
			}
		}
	}
	return out, nil
}

// GetHoldSettings never requests calendar permission.
func (a *App) GetHoldSettings() (HoldSettings, error) {
	return a.holdSettings(eventKitHoldStore{}, calendarAccessGranted())
}

func (a *App) SetOfferHolds(enabled bool) error {
	// The Today strip shows the person's own hold change at once.
	defer a.touchDerivedViews(derivedTodayDayShape)
	if a == nil || a.store == nil {
		return errors.New("the store is not ready")
	}
	value := "false"
	if enabled {
		value = "true"
	}
	return a.store.SetUIState(context.Background(), calendarOfferHoldsKey, value)
}

func (a *App) setHoldCalendar(store holdEventStore, granted bool, id string) error {
	if a == nil || a.store == nil || !granted {
		return errCalendarNotAuthorized
	}
	calendars, err := store.WritableCalendars()
	if err != nil {
		return err
	}
	for _, cal := range calendars {
		if cal.ID == id && id != "" {
			return a.store.SetUIState(context.Background(), calendarHoldCalendarIDKey, id)
		}
	}
	return errHoldUnavailable
}

// SetHoldCalendar remembers an explicitly selected writable calendar.
func (a *App) SetHoldCalendar(id string) error {
	// The Today strip shows the person's own hold change at once.
	defer a.touchDerivedViews(derivedTodayDayShape)
	return a.setHoldCalendar(eventKitHoldStore{}, calendarAccessGranted(), id)
}
