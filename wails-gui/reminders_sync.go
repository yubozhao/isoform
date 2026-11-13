package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"path/filepath"
	"sync"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/storage"
)

// The reminders reader keeps the reminders table current: every open
// reminder plus the ones completed in the last thirty days, so a reminder
// ticked off while Isoform was not running still resolves its commitment. It
// runs on the calendar reader's cadence: once access is granted, every
// fifteen minutes, five seconds after the shared event store reports a
// change, and on wake. It never asks for the Reminders permission itself:
// only the Grant button and onboarding may, through RequestPermission.
const (
	remindersSyncInterval    = cadenceReminders
	remindersChangeDebounce  = 5 * time.Second
	remindersCompletedWindow = 30 * 24 * time.Hour

	// remindersConsentVersion is recorded before the first read, like the
	// calendars. Bump it when the permission copy changes.
	remindersConsentVersion  = 1
	remindersConsentFileName = "reminders-consent.json"
	remindersLastSyncKey     = "reminders_last_sync_at"
	remindersPermissionKey   = "reminders"
	// remindersPendingCloseKey holds, as a JSON list of reminder ids (never
	// titles), the reminders deleted when reading stopped whose commitments
	// could not be closed then.
	remindersPendingCloseKey = "reminders_pending_close"
)

var (
	errRemindersNotAuthorized = errors.New("reminders access is not granted")
	errRemindersReadFailed    = errors.New("could not read the system reminders")
	errRemindersUnsupported   = errors.New("system reminders are not available on this platform")
	errRemindersStopped       = errors.New("reminders reading stopped during the read")

	// remindersStoreChanged receives one token per EKEventStoreChanged burst
	// (calendar_store_changed_export_darwin.go).
	remindersStoreChanged = make(chan struct{}, 1)
)

// reminderSource yields what one read returns. Production reads EventKit;
// tests supply a fake.
type reminderSource interface {
	reminders(completedSince time.Time) ([]calendar.RawReminder, error)
}

type eventKitReminderSource struct{}

func (eventKitReminderSource) reminders(completedSince time.Time) ([]calendar.RawReminder, error) {
	return enumerateSystemReminders(completedSince)
}

// remindersSyncResult is what one read produced.
type remindersSyncResult struct {
	Reason   string
	Upserted int
	Vanished int64
	At       time.Time
}

// remindersSyncService owns the reader loop.
type remindersSyncService struct {
	store   *storage.Store
	source  reminderSource
	now     func() time.Time
	loc     *time.Location
	granted func() bool
	enabled func() bool
	changes <-chan struct{}
	// onSynced runs after every successful read, outside the service lock.
	onSynced func(remindersSyncResult)
	// onCleared runs with the stored reminders just before they are
	// deleted after OS access is revoked, so their commitments can be closed.
	// When it fails the reminders are deleted anyway and their ids kept
	// (remindersPendingCloseKey) until a later pass closes them.
	onCleared func([]storage.ReminderRow) error
	// resetStore refreshes the event store shared with the calendars after
	// access is newly granted; nil in tests.
	resetStore func()
	// consentPath is where the consent record is written; empty skips it.
	consentPath string

	// writeMu orders the writes of a read (its rows and the commitments
	// reconciled from them) against clearing them after OS access is revoked.
	// A Refresh checks the reading switch again before writing.
	writeMu sync.Mutex

	mu       sync.Mutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	kicks    chan string
	syncing  bool
	lastSync time.Time
	lastErr  string
	// wasGranted remembers the last grant state, so a grant made in System
	// Settings (not through the Grant button) starts a read at the next
	// tick. Start sets it, so a launch with access already granted does not
	// reset the store the calendar reader is using.
	wasGranted bool
	// emptyReads counts reads in a row that returned nothing at all.
	emptyReads int
}

// remindersStopWait bounds how long shutdown waits for a read in flight;
// EventKit cannot be interrupted, and the process is exiting anyway.
const remindersStopWait = 3 * time.Second

func newRemindersSyncService(a *App) *remindersSyncService {
	svc := &remindersSyncService{
		store:      a.store,
		source:     eventKitReminderSource{},
		now:        time.Now,
		loc:        time.Local,
		granted:    remindersAccessGranted,
		enabled:    func() bool { return a.isPermissionEnabled(remindersPermissionKey) },
		changes:    remindersStoreChanged,
		kicks:      make(chan string, 4),
		resetStore: resetSystemCalendarStore,
	}
	if dataRoot, err := flavorDataDir(); err == nil {
		svc.consentPath = filepath.Join(dataRoot, remindersConsentFileName)
	}
	svc.onSynced = func(res remindersSyncResult) { a.afterRemindersSync(res) }
	svc.onCleared = a.closeReminderCommitments
	return svc
}

// Start runs the loop until the context ends or Stop is called. It does
// nothing when the platform has no reminders store.
func (s *remindersSyncService) Start(parent context.Context) {
	if s == nil || s.store == nil || !systemRemindersSupported() {
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
	s.noteGrantAtStart()
	// One observer on the shared event store wakes both readers.
	watchSystemCalendarChanges()
	s.wg.Add(1)
	go s.loop(ctx)
}

// noteGrantAtStart records the grant state at launch: the event store is new
// then, so it needs no reset for a grant made before the launch.
func (s *remindersSyncService) noteGrantAtStart() {
	granted := s.granted()
	s.mu.Lock()
	s.wasGranted = granted
	s.mu.Unlock()
}

// Stop ends the loop and waits a little for a read in flight.
func (s *remindersSyncService) Stop() {
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
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(remindersStopWait):
		log.Printf("[reminders] a read is still in flight at shutdown; not waiting for it")
	}
}

// RequestSync asks for a read soon, with a reason for the log. It never
// blocks; a request that finds the queue full is folded into the pending one.
func (s *remindersSyncService) RequestSync(reason string) {
	if s == nil {
		return
	}
	select {
	case s.kicks <- reason:
	default:
	}
}

func (s *remindersSyncService) loop(ctx context.Context) {
	defer s.wg.Done()
	s.syncIfAllowed(ctx, "startup")
	ticker := time.NewTicker(remindersSyncInterval)
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
			s.syncIfAllowed(ctx, "interval")
		case <-s.changes:
			if debounce == nil {
				debounce = time.NewTimer(remindersChangeDebounce)
			} else {
				debounce.Reset(remindersChangeDebounce)
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

// syncIfAllowed runs one read when access is granted and the user has not
// switched reading off. Switching off preserves saved reminders and their
// commitments. Revoking OS access still clears them, including a revocation
// made while Isoform was not running.
func (s *remindersSyncService) syncIfAllowed(ctx context.Context, reason string) {
	granted := s.granted()
	s.mu.Lock()
	was := s.wasGranted
	s.wasGranted = granted
	s.mu.Unlock()
	if !granted {
		if was {
			log.Printf("[reminders] access is no longer granted; reading stops")
		}
		s.clearStored(ctx)
		return
	}
	if !s.enabled() {
		return
	}
	// Commitments left open by an earlier stop are closed first; the read
	// below reopens the ones still open in Reminders.
	s.closePending(ctx)
	if !was {
		// EventKit caches a denial made before the grant; a fresh store sees
		// the reminders a grant in System Settings just opened.
		if s.resetStore != nil {
			s.resetStore()
		}
		if reason == "interval" {
			reason = "access granted"
		}
	}
	if _, err := s.Sync(ctx, reason); err != nil && ctx.Err() == nil {
		log.Printf("[reminders] sync (%s) failed: %v", reason, err)
	}
}

// Sync reads the reminders, upserts each one, marks the ones that vanished
// as deleted and prunes old closed ones. It runs at most once at a time.
func (s *remindersSyncService) Sync(ctx context.Context, reason string) (remindersSyncResult, error) {
	s.mu.Lock()
	if s.syncing {
		s.mu.Unlock()
		return remindersSyncResult{}, errors.New("a reminders sync is already running")
	}
	s.syncing = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.syncing = false
		s.mu.Unlock()
	}()

	now := s.now()
	res := remindersSyncResult{Reason: reason, At: now}
	if err := recordObservationConsent(s.consentPath, remindersConsentVersion, now); err != nil {
		log.Printf("[reminders] record consent: %v", err)
	}
	completedSince := now.Add(-remindersCompletedWindow)
	raws, err := s.source.reminders(completedSince)
	if err != nil {
		s.noteError(err)
		return res, err
	}
	var rows []storage.ReminderRow
	seen := map[string]bool{}
	for _, raw := range raws {
		r, ok := calendar.NormalizeReminder(raw, s.loc)
		if !ok || seen[r.UID] {
			continue
		}
		seen[r.UID] = true
		rows = append(rows, reminderRow(r, now))
	}
	// Reading may have stopped while EventKit answered (a Refresh racing a
	// switch-off); then nothing is written. Revoked access after this point
	// waits for the writes and clears them; switching off retains them.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.readingAllowed() {
		return res, errRemindersStopped
	}
	if err := s.store.UpsertReminders(ctx, rows, now.UnixMilli()); err != nil {
		s.noteError(err)
		return res, err
	}
	res.Upserted = len(rows)
	// One read that returns nothing at all may be a transient EventKit
	// answer; two in a row mean the reminders are gone.
	if len(rows) == 0 {
		s.emptyReads++
	} else {
		s.emptyReads = 0
	}
	if len(rows) > 0 || s.emptyReads >= 2 {
		vanished, err := s.store.MarkRemindersVanished(ctx, seen, completedSince.UnixMilli(), now.UnixMilli())
		if err != nil {
			log.Printf("[reminders] mark vanished: %v", err)
		}
		res.Vanished = vanished
	} else if live, err := s.store.ListReminders(ctx, false); err == nil && len(live) > 0 {
		log.Printf("[reminders] empty read with %d open reminders; keeping them until the next read", len(live))
	}
	if _, err := s.store.PruneReminders(ctx, now.Add(-storage.ReminderRetention).UnixMilli()); err != nil {
		log.Printf("[reminders] prune: %v", err)
	}
	if err := s.store.SetUIState(ctx, remindersLastSyncKey, now.UTC().Format(time.RFC3339)); err != nil {
		log.Printf("[reminders] record last sync: %v", err)
	}
	s.mu.Lock()
	s.lastSync = now
	s.lastErr = ""
	s.mu.Unlock()
	log.Printf("[reminders] sync (%s): %d reminders, %d vanished", reason, res.Upserted, res.Vanished)
	if s.onSynced != nil {
		s.onSynced(res)
	}
	return res, nil
}

// readingAllowed reports whether access is granted and the switch is on.
func (s *remindersSyncService) readingAllowed() bool {
	return (s.granted == nil || s.granted()) && (s.enabled == nil || s.enabled())
}

// clearStored closes the commitments made from the stored reminders and
// deletes the reminders. It waits for the writes of a read in flight; with
// nothing stored it costs one query. The reminders are deleted even when
// their commitments cannot be closed now (the Purpose Map is not open yet):
// their ids are kept and a later pass closes them.
func (s *remindersSyncService) clearStored(ctx context.Context) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.closePendingLocked(ctx)
	rows, err := s.store.ListReminders(ctx, true)
	if err != nil || len(rows) == 0 {
		return
	}
	if s.onCleared != nil {
		if err := s.onCleared(rows); err != nil {
			log.Printf("[reminders] close commitments: %v; closing them later", err)
			s.addPendingClose(ctx, rows)
		}
	}
	removed, err := s.store.DeleteAllReminders(ctx)
	if err != nil {
		log.Printf("[reminders] clear: %v", err)
		return
	}
	log.Printf("[reminders] reading stopped; removed %d stored reminders", removed)
}

// closePending closes the commitments an earlier stop could not.
func (s *remindersSyncService) closePending(ctx context.Context) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.closePendingLocked(ctx)
}

func (s *remindersSyncService) closePendingLocked(ctx context.Context) {
	uids := s.pendingClose(ctx)
	if len(uids) == 0 || s.onCleared == nil {
		return
	}
	rows := make([]storage.ReminderRow, 0, len(uids))
	for _, uid := range uids {
		rows = append(rows, storage.ReminderRow{UID: uid})
	}
	if err := s.onCleared(rows); err != nil {
		log.Printf("[reminders] close %d pending commitments: %v", len(rows), err)
		return
	}
	if err := s.store.SetUIState(ctx, remindersPendingCloseKey, ""); err != nil {
		log.Printf("[reminders] clear pending closes: %v", err)
	}
}

func (s *remindersSyncService) pendingClose(ctx context.Context) []string {
	raw, err := s.store.GetUIState(ctx, remindersPendingCloseKey)
	if err != nil || raw == "" {
		return nil
	}
	var uids []string
	if json.Unmarshal([]byte(raw), &uids) != nil {
		return nil
	}
	return uids
}

func (s *remindersSyncService) addPendingClose(ctx context.Context, rows []storage.ReminderRow) {
	uids := s.pendingClose(ctx)
	have := make(map[string]bool, len(uids))
	for _, uid := range uids {
		have[uid] = true
	}
	for _, row := range rows {
		if !have[row.UID] {
			have[row.UID] = true
			uids = append(uids, row.UID)
		}
	}
	data, err := json.Marshal(uids)
	if err != nil {
		return
	}
	if err := s.store.SetUIState(ctx, remindersPendingCloseKey, string(data)); err != nil {
		log.Printf("[reminders] keep pending closes: %v", err)
	}
}

func (s *remindersSyncService) noteError(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

// status reports the last read time and error for the settings UI.
func (s *remindersSyncService) status() (time.Time, string) {
	if s == nil {
		return time.Time{}, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSync, s.lastErr
}

// reminderRow converts a normalised reminder to its stored shape. A
// completed reminder without a completion date counts as completed at the
// read, so it is never taken for an open one.
func reminderRow(r calendar.Reminder, readAt time.Time) storage.ReminderRow {
	row := storage.ReminderRow{
		UID: r.UID, Title: r.Title, DueAllDay: r.DueAllDay, List: r.List, Priority: r.Priority,
	}
	if r.HasDue() {
		row.DueAt = r.Due.UnixMilli()
	}
	switch {
	case r.Completed && !r.CompletedAt.IsZero():
		row.CompletedAt = r.CompletedAt.UnixMilli()
	case r.Completed:
		row.CompletedAt = readAt.UnixMilli()
	}
	return row
}

// startRemindersSync builds and starts the reader. It runs once from startup.
func (a *App) startRemindersSync(ctx context.Context) {
	if a == nil || a.store == nil {
		return
	}
	svc := newRemindersSyncService(a)
	a.remindersSync = svc
	svc.Start(ctx)
}

// stopRemindersSync ends the reader at shutdown.
func (a *App) stopRemindersSync() {
	if a == nil || a.remindersSync == nil {
		return
	}
	a.remindersSync.Stop()
}

// remindersPermissionChanged reacts to the Grant button and to the Reminders
// switch. Both go through the loop: a grant or a switch-on reads, a
// switch-off pauses reading and retains saved data.
func (a *App) remindersPermissionChanged(granted, enabled bool) {
	if a == nil || a.remindersSync == nil {
		return
	}
	switch {
	case granted && enabled:
		a.remindersSync.RequestSync("enabled")
	case !enabled:
		log.Printf("[reminders] reading switched off")
		a.remindersSync.RequestSync("switched off")
	}
}

// closeReminderCommitments dismisses the open commitments made from the given
// reminders, because reading stopped. A later read reopens the ones still
// open in Reminders. It fails when the Purpose Map is not open or a
// commitment could not be closed.
func (a *App) closeReminderCommitments(rows []storage.ReminderRow) error {
	if a == nil {
		return nil
	}
	store, err := a.purposeProductStore()
	if err != nil {
		return err
	}
	closed, err := closeReminderCommitments(store, rows, time.Now(), time.Local)
	if closed > 0 {
		log.Printf("[reminders] reading stopped; closed %d commitments", closed)
		a.emitBrowserEvent("reminders:synced")
	}
	return err
}

// afterRemindersSync reconciles the Purpose Map with the stored reminders and
// tells the settings page.
func (a *App) afterRemindersSync(res remindersSyncResult) {
	if a == nil || a.store == nil {
		return
	}
	defer a.emitBrowserEvent("reminders:synced")
	store, err := a.purposeProductStore()
	if err != nil {
		return // Purpose storage is not ready yet; the next read tries again.
	}
	rows, err := a.store.ListReminders(context.Background(), true)
	if err != nil {
		log.Printf("[reminders] list for reconciliation: %v", err)
		return
	}
	result, err := reconcileReminderCommitments(store, rows, res.At, time.Local)
	if err != nil {
		log.Printf("[reminders] reconcile commitments: %v", err)
		return
	}
	if result.changed() || result.Skipped > 0 {
		log.Printf("[reminders] commitments: %d created, %d revised, %d resolved, %d dismissed, %d reopened, %d over the limit",
			result.Created, result.Revised, result.Resolved, result.Dismissed, result.Reopened, result.Skipped)
	}
}
