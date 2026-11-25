package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/identity"
	"wails-gui/storage"
)

// The calendar reader keeps calendar_events current for the window one day
// back to fourteen days ahead. It runs once access is granted, every fifteen
// minutes, five seconds after the event store reports a change, and on wake.
// It never asks for the Calendars permission itself: only the Grant button
// and onboarding may, through RequestPermission.
const (
	calendarSyncInterval   = 15 * time.Minute
	calendarChangeDebounce = 5 * time.Second
	calendarWindowBack     = 24 * time.Hour
	calendarWindowAhead    = 14 * 24 * time.Hour

	// calendarConsentVersion is recorded before the first read, like the
	// contacts import does. Bump it when the permission copy changes.
	calendarConsentVersion  = 1
	calendarConsentFileName = "calendar-consent.json"
	calendarLastSyncKey     = "calendar_last_sync_at"
	calendarPermissionKey   = "calendars"
	// calendarSightingApp is the app recorded on a principal sighting made
	// from an invite.
	calendarSightingApp = "calendar"
)

var (
	errCalendarNotAuthorized = errors.New("calendar access is not granted")
	errCalendarReadFailed    = errors.New("could not read the system calendars")
	errCalendarUnsupported   = errors.New("system calendars are not available on this platform")

	// calendarStoreChanged receives one token per EKEventStoreChanged burst.
	calendarStoreChanged = make(chan struct{}, 1)
	holdsStoreChanged    = make(chan struct{}, 1)
)

// calendarEventSource yields what one sync reads. Production reads EventKit;
// tests supply a fake.
type calendarEventSource interface {
	calendars() ([]calendar.RawCalendar, error)
	events(start, end time.Time) ([]calendar.RawEvent, error)
}

type eventKitSource struct{}

func (eventKitSource) calendars() ([]calendar.RawCalendar, error) { return enumerateSystemCalendars() }
func (eventKitSource) events(start, end time.Time) ([]calendar.RawEvent, error) {
	return enumerateSystemCalendarEvents(start, end)
}

// calendarSyncResult is what one sync produced, for the log and for the
// producers that run after a sync.
type calendarSyncResult struct {
	Reason   string
	Events   []calendar.Event
	Upserted int
	Vanished int64
	Sources  int
	At       time.Time
}

// calendarSyncService owns the reader loop.
type calendarSyncService struct {
	store   *storage.Store
	source  calendarEventSource
	now     func() time.Time
	granted func() bool
	enabled func() bool
	changes <-chan struct{}
	// onSynced runs after every successful sync, outside the service lock.
	onSynced func(calendarSyncResult)
	// webDetect records the web calendars seen in the browser. It needs no
	// permission, so it runs on every tick whether or not access is granted,
	// but not while reading is switched off or observing is paused.
	webDetect func(context.Context)
	paused    func() bool
	// peekEnabled is the web calendar switch: without it the browser history
	// is never read, since nothing would use the result.
	peekEnabled func() bool
	// consentPath is where the consent record is written; empty skips it.
	consentPath string

	mu       sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	kicks    chan string
	syncing  bool
	lastSync time.Time
	lastErr  string
	// wasGranted remembers the last grant state, so a grant made in System
	// Settings (not through the Grant button) starts a sync at the next tick.
	wasGranted bool
}

func newCalendarSyncService(a *App) *calendarSyncService {
	svc := &calendarSyncService{
		store:   a.store,
		source:  eventKitSource{},
		now:     time.Now,
		granted: calendarAccessGranted,
		enabled: func() bool { return a.captureAllowed(context.Background(), "calendar_sync", calendarPermissionKey) },
		changes: calendarStoreChanged,
		kicks:   make(chan string, 4),
	}
	if path, err := calendarConsentPath(); err == nil {
		svc.consentPath = path
	}
	svc.onSynced = func(res calendarSyncResult) { a.afterCalendarSync(res) }
	svc.webDetect = a.detectWebCalendarSources
	svc.paused = a.IsClosedEyesModeEnabled
	svc.peekEnabled = a.calendarPeekCaptureAllowed
	return svc
}

// Start runs the loop until the context ends or Stop is called. It does
// nothing when the platform has no calendar store.
func (s *calendarSyncService) Start(parent context.Context) {
	if s == nil || s.store == nil || !systemCalendarsSupported() {
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.mu.Unlock()
	watchSystemCalendarChanges()
	s.wg.Add(1)
	go s.loop(ctx)
}

// Stop ends the loop and waits for a sync in flight.
func (s *calendarSyncService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// RequestSync asks for a sync soon, with a reason for the log. It never
// blocks; a request that finds the queue full is folded into the pending one.
func (s *calendarSyncService) RequestSync(reason string) {
	if s == nil {
		return
	}
	select {
	case s.kicks <- reason:
	default:
	}
}

func (s *calendarSyncService) loop(ctx context.Context) {
	defer s.wg.Done()
	s.detectWeb(ctx)
	s.syncIfAllowed(ctx, "startup")
	ticker := time.NewTicker(calendarSyncInterval)
	defer ticker.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if debounce != nil {
				debounce.Stop()
			}
			return
		case <-ticker.C:
			s.detectWeb(ctx)
			s.syncIfAllowed(ctx, "interval")
		case <-s.changes:
			if debounce == nil {
				debounce = time.NewTimer(calendarChangeDebounce)
			} else {
				debounce.Reset(calendarChangeDebounce)
			}
			debounceC = debounce.C
		case <-debounceC:
			debounceC = nil
			s.syncIfAllowed(ctx, "store changed")
		case reason := <-s.kicks:
			s.syncIfAllowed(ctx, reason)
		}
	}
}

func (s *calendarSyncService) detectWeb(ctx context.Context) {
	if s.webDetect == nil || ctx.Err() != nil {
		return
	}
	if s.enabled != nil && !s.enabled() {
		return
	}
	if s.paused != nil && s.paused() {
		return
	}
	if s.peekEnabled != nil && !s.peekEnabled() {
		return
	}
	s.webDetect(ctx)
}

// syncIfAllowed runs one sync when access is granted and the user has not
// switched reading off. It logs each outcome once.
func (s *calendarSyncService) syncIfAllowed(ctx context.Context, reason string) {
	granted := s.granted()
	s.mu.Lock()
	was := s.wasGranted
	s.wasGranted = granted
	s.mu.Unlock()
	if !granted {
		if was {
			log.Printf("[calendar] access is no longer granted; reading stops")
		}
		return
	}
	if !s.enabled() {
		return
	}
	if !was {
		// EventKit caches a denial made before the grant; a fresh store sees
		// the calendars a grant in System Settings just opened.
		resetSystemCalendarStore()
		if reason == "interval" {
			reason = "access granted"
		}
	}
	if _, err := s.Sync(ctx, reason); err != nil && ctx.Err() == nil {
		log.Printf("[calendar] sync (%s) failed: %v", reason, err)
	}
}

// Sync reads the window, upserts every occurrence, marks the ones that
// vanished as deleted, resolves attendees to people and records which
// calendar accounts exist. It runs at most once at a time.
func (s *calendarSyncService) Sync(ctx context.Context, reason string) (calendarSyncResult, error) {
	s.mu.Lock()
	if s.syncing {
		s.mu.Unlock()
		return calendarSyncResult{}, errors.New("a calendar sync is already running")
	}
	s.syncing = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.syncing = false
		s.mu.Unlock()
	}()

	now := s.now()
	res := calendarSyncResult{Reason: reason, At: now}
	if err := s.recordConsent(now); err != nil {
		log.Printf("[calendar] record consent: %v", err)
	}

	rawCalendars, err := s.source.calendars()
	if err != nil {
		s.noteError(err)
		return res, err
	}
	sources := calendar.SourcesFromEventKit(rawCalendars, now)
	keep := map[string]bool{}
	for _, src := range sources {
		keep[src.ID] = true
		if err := s.store.UpsertCalendarSource(ctx, calendarSourceRow(src, now)); err != nil {
			log.Printf("[calendar] record source %s: %v", src.ID, err)
		}
	}
	if err := s.store.DeleteCalendarSourcesOfKind(ctx, calendar.SourceKindEventKit, keep); err != nil {
		log.Printf("[calendar] drop removed sources: %v", err)
	}
	res.Sources = len(sources)

	windowStart := now.Add(-calendarWindowBack)
	windowEnd := now.Add(calendarWindowAhead)
	rawEvents, err := s.source.events(windowStart, windowEnd)
	if err != nil {
		s.noteError(err)
		return res, err
	}
	var rows []storage.CalendarEventRow
	seen := map[string]bool{}
	resolver := identity.NewResolver(s.store, false)
	own, err := s.store.CalendarOwnEmails(ctx)
	if err != nil {
		log.Printf("[calendar] own addresses: %v", err)
		own = map[string]bool{}
	}
	events := make([]calendar.Event, 0, len(rawEvents))
	for _, raw := range rawEvents {
		ev, ok := calendar.NormalizeEventKit(raw)
		if !ok || seen[ev.ID] {
			continue
		}
		seen[ev.ID] = true
		events = append(events, ev)
		for _, att := range ev.Attendees {
			if att.IsMe && att.Email != "" {
				own[strings.ToLower(strings.TrimSpace(att.Email))] = true
			}
		}
	}
	for _, ev := range events {
		markOwnCalendarAddresses(&ev, own)
		_, existed, err := s.store.GetCalendarEvent(ctx, ev.ID)
		if err != nil {
			log.Printf("[calendar] look up %s: %v", ev.ID, err)
		}
		row := calendarEventRow(ev)
		row.Attendees = s.resolveAttendees(ctx, resolver, ev, !existed, now)
		rows = append(rows, row)
		res.Events = append(res.Events, ev)
	}
	if err := s.store.UpsertCalendarEvents(ctx, rows, now.UnixMilli()); err != nil {
		s.noteError(err)
		return res, err
	}
	res.Upserted = len(rows)
	// A read that returned nothing while rows are live is more likely a
	// transient EventKit answer than everything being cancelled at once.
	if len(rows) > 0 {
		vanished, err := s.store.MarkCalendarEventsVanished(ctx, calendar.SourceEventKit, "", windowStart.UnixMilli(), windowEnd.UnixMilli(), seen, now.UnixMilli())
		if err != nil {
			log.Printf("[calendar] mark vanished: %v", err)
		}
		res.Vanished = vanished
	} else if live, err := s.store.ListCalendarEvents(ctx, windowStart.UnixMilli(), windowEnd.UnixMilli(), false); err == nil && len(live) > 0 {
		log.Printf("[calendar] empty read with %d live events; keeping them", len(live))
	}
	if _, err := s.store.PruneCalendarEvents(ctx, now.Add(-storage.CalendarEventRetention).UnixMilli()); err != nil {
		log.Printf("[calendar] prune: %v", err)
	}
	for _, src := range sources {
		if err := s.store.TouchCalendarSourceRead(ctx, src.ID, now.UnixMilli()); err != nil {
			log.Printf("[calendar] touch source %s: %v", src.ID, err)
		}
	}
	if err := s.store.SetUIState(ctx, calendarLastSyncKey, now.UTC().Format(time.RFC3339)); err != nil {
		log.Printf("[calendar] record last sync: %v", err)
	}
	s.mu.Lock()
	s.lastSync = now
	s.lastErr = ""
	s.mu.Unlock()
	log.Printf("[calendar] sync (%s): %d sources, %d events, %d vanished", reason, res.Sources, res.Upserted, res.Vanished)
	if s.onSynced != nil {
		s.onSynced(res)
	}
	return res, nil
}

func (s *calendarSyncService) noteError(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

// status reports the last sync time and error for the settings UI.
func (s *calendarSyncService) status() (time.Time, string) {
	if s == nil {
		return time.Time{}, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSync, s.lastErr
}

// markOwnCalendarAddresses marks as the person every attendee whose address
// the calendar reported as the current user on some event. Each account
// flags only itself, so with two calendar accounts the second address sat on
// shared meetings as a guest: the brief and meeting prep named the person
// among their own attendees.
func markOwnCalendarAddresses(ev *calendar.Event, own map[string]bool) {
	for i, att := range ev.Attendees {
		if !att.IsMe && att.Email != "" && own[strings.ToLower(strings.TrimSpace(att.Email))] {
			ev.Attendees[i].IsMe = true
		}
	}
}

// resolveAttendees links each attendee address to the person it already
// belongs to. Nothing is created: an unknown address stays unlinked. A
// sighting is recorded only for a new event and only for known people, so a
// repeated sync never inflates anyone's count.
func (s *calendarSyncService) resolveAttendees(ctx context.Context, resolver *identity.Resolver, ev calendar.Event, newEvent bool, now time.Time) []storage.CalendarAttendeeRow {
	rows := make([]storage.CalendarAttendeeRow, 0, len(ev.Attendees))
	for _, att := range ev.Attendees {
		row := storage.CalendarAttendeeRow{EventID: ev.ID, Email: att.Email, Name: att.Name, Role: att.Role, Status: att.Status, IsMe: att.IsMe}
		if att.Email != "" && !att.IsMe && resolver != nil {
			subject, err := resolver.ResolveSender(ctx, "email", att.Email, "")
			if err != nil {
				log.Printf("[calendar] resolve attendee: %v", err)
			} else if subject.PrincipalID != "" && subject.Kind != identity.SubjectAnonymous && subject.Kind != identity.SubjectSelf {
				row.PrincipalID = subject.PrincipalID
				if newEvent {
					seenAt := ev.Start
					if seenAt.After(now) {
						seenAt = now
					}
					if err := s.store.BumpPrincipalSighting(ctx, subject.PrincipalID, calendarSightingApp, "", seenAt.UnixMilli(), ""); err != nil {
						log.Printf("[calendar] record sighting: %v", err)
					}
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// calendarConsent is the stored acceptance of the calendar permission copy,
// kept beside the contacts consent with 0600 permissions.
type calendarConsent struct {
	Version    int    `json:"version"`
	AcceptedAt string `json:"acceptedAt"`
}

func calendarConsentPath() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, calendarConsentFileName), nil
}

func (s *calendarSyncService) recordConsent(now time.Time) error {
	return recordObservationConsent(s.consentPath, calendarConsentVersion, now)
}

// recordObservationConsent writes the consent record at path once per
// version, before the first read of a data source the user granted. An empty
// path skips it. The reminders reader uses it too.
func recordObservationConsent(path string, version int, now time.Time) error {
	if path == "" {
		return nil
	}
	if raw, err := os.ReadFile(path); err == nil {
		var consent calendarConsent
		if json.Unmarshal(raw, &consent) == nil && consent.Version == version {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeMobilePrivateJSON(path, calendarConsent{
		Version:    version,
		AcceptedAt: now.UTC().Format(time.RFC3339),
	})
}

// calendarEventRow converts a normalised event to its stored shape. Attendees
// are filled by the caller once they are resolved.
func calendarEventRow(ev calendar.Event) storage.CalendarEventRow {
	return storage.CalendarEventRow{
		ID: ev.ID, UID: ev.UID, LocalID: ev.LocalID,
		CalendarID: ev.CalendarID, CalendarTitle: ev.CalendarTitle,
		Source: ev.Source, Provider: ev.Provider, Title: ev.Title,
		StartAt: ev.Start.UnixMilli(), EndAt: ev.End.UnixMilli(), AllDay: ev.AllDay, TimeZone: ev.TimeZone,
		Location: ev.Location, Lat: ev.Latitude, Lon: ev.Longitude, HasCoordinate: ev.HasCoordinate,
		ConferenceURL: ev.ConferenceURL, ConferenceHost: ev.ConferenceHost, URL: ev.URL,
		Availability: ev.Availability, Status: ev.Status, MyStatus: ev.MyStatus,
		OrganizerEmail: ev.OrganizerEmail, OrganizerName: ev.OrganizerName,
		Recurring: ev.Recurring, AttendeeCount: ev.AttendeeCount(),
	}
}

// calendarEventFromRow is the inverse, for the brief, the producers and
// meeting matching.
func calendarEventFromRow(row storage.CalendarEventRow) calendar.Event {
	ev := calendar.Event{
		ID: row.ID, UID: row.UID, LocalID: row.LocalID,
		Source: row.Source, CalendarID: row.CalendarID, CalendarTitle: row.CalendarTitle, Provider: row.Provider,
		Title: row.Title, Start: time.UnixMilli(row.StartAt), End: time.UnixMilli(row.EndAt), AllDay: row.AllDay, TimeZone: row.TimeZone,
		Location: row.Location, Latitude: row.Lat, Longitude: row.Lon, HasCoordinate: row.HasCoordinate,
		URL: row.URL, ConferenceURL: row.ConferenceURL, ConferenceHost: row.ConferenceHost,
		Availability: row.Availability, Status: row.Status, MyStatus: row.MyStatus,
		OrganizerEmail: row.OrganizerEmail, OrganizerName: row.OrganizerName, Recurring: row.Recurring,
	}
	for _, att := range row.Attendees {
		ev.Attendees = append(ev.Attendees, calendar.Attendee{Name: att.Name, Email: att.Email, Role: att.Role, Status: att.Status, IsMe: att.IsMe})
	}
	return ev
}

func calendarSourceRow(src calendar.Source, now time.Time) storage.CalendarSourceRow {
	row := storage.CalendarSourceRow{
		ID: src.ID, Kind: src.Kind, Provider: src.Provider, Title: src.Title, DetectedBy: src.DetectedBy, Calendars: src.Calendars,
		FirstSeenAt: src.FirstSeen.UnixMilli(), LastSeenAt: src.LastSeen.UnixMilli(),
	}
	if src.FirstSeen.IsZero() {
		row.FirstSeenAt = now.UnixMilli()
	}
	if src.LastSeen.IsZero() {
		row.LastSeenAt = now.UnixMilli()
	}
	if !src.LastRead.IsZero() {
		row.LastReadAt = src.LastRead.UnixMilli()
	}
	return row
}

// listCalendarEvents loads the live events overlapping [from, to) as
// normalised events, oldest first.
func (a *App) listCalendarEvents(ctx context.Context, from, to time.Time) ([]calendar.Event, error) {
	if a == nil || a.store == nil {
		return nil, nil
	}
	rows, err := a.store.ListCalendarEvents(ctx, from.UnixMilli(), to.UnixMilli(), false)
	if err != nil {
		return nil, err
	}
	out := make([]calendar.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, calendarEventFromRow(row))
	}
	return out, nil
}

// startCalendarSync builds and starts the reader. It runs once from startup.
func (a *App) startCalendarSync(ctx context.Context) {
	if a == nil || a.store == nil {
		return
	}
	svc := newCalendarSyncService(a)
	a.calendarSync = svc
	svc.Start(ctx)
	// meeting_prep checks the next fifteen minutes once a minute.
	go a.runCalendarMoments(ctx)
}

// stopCalendarSync ends the reader at shutdown.
func (a *App) stopCalendarSync() {
	if a == nil || a.calendarSync == nil {
		return
	}
	a.calendarSync.Stop()
}

// calendarPermissionChanged reacts to the Grant button and to the Calendars
// switch: a grant or a switch-on starts a sync, a switch-off only logs.
func (a *App) calendarPermissionChanged(granted, enabled bool) {
	if a == nil || a.calendarSync == nil {
		return
	}
	switch {
	case granted && enabled:
		a.calendarSync.RequestSync("enabled")
	case !enabled:
		log.Printf("[calendar] reading switched off")
		a.calendarPeek.cancelRun()
	}
}

// afterCalendarSync runs the producers that depend on fresh events: clashes
// and waiting invites become passive calendar_conflict moments, and a meeting
// starting within the lead time gets its meeting_prep without waiting for the
// minute loop.
func (a *App) afterCalendarSync(res calendarSyncResult) {
	if a == nil {
		return
	}
	ctx := context.Background()
	a.produceCalendarConflicts(ctx, res.At)
	a.produceMeetingPrep(ctx, res.At)
	a.emitBrowserEvent("calendar:synced")
}
