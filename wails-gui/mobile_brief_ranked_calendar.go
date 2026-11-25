package main

import (
	"context"
	"time"

	"wails-gui/internal/calendar"
)

// Only the digest is retained locally; the model never receives the calendar
// event's raw fields, attendee addresses or conference URL.
func morningBriefCalendarSourceDigest(event calendar.Event) string {
	return mobileBriefModelDigest(event)
}

func (a *App) morningBriefCalendarReadAllowed(ctx context.Context) bool {
	return a.morningBriefCalendarReadAllowedWithGrant(ctx, calendarAccessGranted())
}

// The injected OS grant is only a test seam; production always reads the
// current system permission before rechecking an exact stored occurrence.
func (a *App) morningBriefCalendarReadAllowedWithGrant(ctx context.Context, granted bool) bool {
	return a != nil && a.store != nil && (!a.IsClosedEyesModeEnabled() || pausedBriefReadAllowed(ctx)) && ctx.Err() == nil &&
		a.captureAllowed(ctx, "mobile_brief_ranked_calendar", calendarPermissionKey) && granted && proactiveVoiceAppAllowed("com.apple.iCal", "Calendar")
}

// morningBriefCalendarSourceReadable reopens the exact occurrence and checks
// the current calendar permissions and its originating source before trusting
// a previously captured digest. Web rows without app provenance fail closed.
func (a *App) morningBriefCalendarSourceReadable(ctx context.Context, id, expected string) bool {
	if id == "" || expected == "" || !a.morningBriefCalendarReadAllowed(ctx) {
		return false
	}
	row, found, err := a.store.GetCalendarEvent(ctx, id)
	if err != nil || !found || row.DeletedAt != 0 || row.Source != calendar.SourceEventKit ||
		row.SessionID != "" && !a.proactiveVoiceSessionsAllowedContext(ctx, []string{row.SessionID}, nil) {
		return false
	}
	event := calendarEventFromRow(row)
	if event.Status == "canceled" || event.MyStatus == calendar.StatusDeclined {
		return false
	}
	return ctx.Err() == nil && morningBriefCalendarSourceDigest(event) == expected
}

// morningBriefCalendarOriginDigest identifies an occurrence and the policy
// context of its originating session, without freezing mutable event content.
func morningBriefCalendarOriginDigest(event calendar.Event, sessionID string) string {
	return mobileBriefModelDigest(struct {
		ID, UID, LocalID, Source, CalendarID, Provider, SessionID string
	}{event.ID, event.UID, event.LocalID, event.Source, event.CalendarID, event.Provider, sessionID})
}

type morningBriefCalendarOrigin struct {
	SourceDigest string
	OriginDigest string
}

// Capture origin and content from the same row. The caller only attaches an
// origin if the separately gathered event has the same content digest.
func (a *App) morningBriefCalendarOrigins(ctx context.Context, from, to time.Time) map[string]morningBriefCalendarOrigin {
	if !a.morningBriefCalendarReadAllowed(ctx) {
		return nil
	}
	rows, err := a.store.ListCalendarEvents(ctx, from.UnixMilli(), to.UnixMilli(), false)
	if err != nil {
		return nil
	}
	out := make(map[string]morningBriefCalendarOrigin, len(rows))
	for _, row := range rows {
		if ctx.Err() != nil {
			return nil
		}
		if row.DeletedAt != 0 || row.Source != calendar.SourceEventKit || row.SessionID != "" && !a.proactiveVoiceSessionsAllowedContext(ctx, []string{row.SessionID}, nil) {
			continue
		}
		event := calendarEventFromRow(row)
		out[event.ID] = morningBriefCalendarOrigin{SourceDigest: morningBriefCalendarSourceDigest(event), OriginDigest: morningBriefCalendarOriginDigest(event, row.SessionID)}
	}
	return out
}

// A readable origin authorizes only a code-side historical comparison. It
// does not authorize sending an old event title or other old text to a model.
func (a *App) morningBriefCalendarOriginReadable(ctx context.Context, id, expected string) bool {
	if id == "" || expected == "" || !a.morningBriefCalendarReadAllowed(ctx) {
		return false
	}
	row, found, err := a.store.GetCalendarEvent(ctx, id)
	if err != nil || !found || row.DeletedAt != 0 || row.Source != calendar.SourceEventKit ||
		row.SessionID != "" && !a.proactiveVoiceSessionsAllowedContext(ctx, []string{row.SessionID}, nil) {
		return false
	}
	return ctx.Err() == nil && morningBriefCalendarOriginDigest(calendarEventFromRow(row), row.SessionID) == expected
}
