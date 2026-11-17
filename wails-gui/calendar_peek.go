package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/activitymon/appparser"
	"wails-gui/storage"
)

// A calendar peek reads a web calendar that is not on this Mac: it opens the
// calendar's week view in the default browser, reads the visible events from
// the accessibility tree with the same parser the activity monitor uses,
// stores them with source "web", closes the tab it opened and brings back the
// app that was in front. The rules (idle five minutes, or a minute idle in
// the ten minutes before a brief; never in closed-eyes mode, locked, in a
// meeting, with the microphone or camera open or something fullscreen; at
// most every two hours) live in internal/calendar/peek.go; this file
// schedules and drives it, and stops the moment the user touches the Mac.
const (
	calendarPeekTick = time.Minute
	// calendarPeekPageWait is how long a peek waits for the browser to show
	// the calendar before giving up on that provider.
	calendarPeekPageWait = 12 * time.Second
	calendarPeekPoll     = 500 * time.Millisecond
	// calendarPeekSettle is how long the page gets after its URL is right,
	// so the week view has rendered its event chips.
	calendarPeekSettle = 3 * time.Second
	// calendarPeekWindow bounds the vanish pass to the days the view shows.
	calendarPeekWindow = 7 * 24 * time.Hour
	// calendarPeekRetryAfterInput is how long a run interrupted by the user
	// waits before the rules are tried again.
	calendarPeekRetryAfterInput = 10 * time.Minute
	// calendarPeekFailureLimit consecutive failures put a provider on hold
	// for calendarPeekFailureHold.
	calendarPeekFailureLimit = 3
	calendarPeekFailureHold  = 24 * time.Hour
	// calendarPeekNoticeKey records that the one-time notice was shown.
	calendarPeekNoticeKey = "calendar_peek_notice_shown"
	calendarPeekLeaseName = "calendar-peek"
)

var errCalendarPeekInterrupted = errors.New("the user is active; stopped before touching the browser")

// calendarPeekDriver is the browser and screen the peek acts on. The macOS
// driver wraps activitymon; tests supply a fake.
type calendarPeekDriver interface {
	openURL(url string) bool
	frontmostBundle() string
	isBrowser(bundle string) bool
	browserURL(bundle string) string
	// activeTabRef identifies the front window's active tab so exactly that
	// tab can be closed later; "" when the browser cannot be asked.
	activeTabRef(bundle string) string
	closeTabRef(bundle, ref, fragment string) bool
	readTree(bundle string) *activitymon.AXNode
	activate(bundle string) bool
	idle() time.Duration
	defaultBrowser() string
	sleep(d time.Duration)
}

// calendarPeekResult is what one run produced.
type calendarPeekResult struct {
	Providers []string
	Events    int
	Reason    string
	At        time.Time
}

type calendarPeekService struct {
	store  *storage.Store
	driver calendarPeekDriver
	now    func() time.Time
	loc    *time.Location
	// state gathers the rule inputs once per tick; guard is re-read before
	// every step of a run and names the reason to stop ("" to go on).
	state func(now time.Time) calendar.PeekState
	guard func() string
	// onRead records the activity event; notice shows the one-time notice;
	// suppress pauses the activity monitor's capture for the run.
	onRead   func(provider string, events int, at time.Time)
	notice   func() bool
	suppress func(on bool)
	lease    *computerUseLease

	mu        sync.Mutex
	cancel    context.CancelFunc
	runCancel context.CancelFunc
	wg        sync.WaitGroup
	running   bool
	lastPeek  time.Time
	notBefore time.Time
	failures  map[string]int
	holdUntil map[string]time.Time
}

func newCalendarPeekService(a *App) *calendarPeekService {
	svc := &calendarPeekService{
		store:     a.store,
		driver:    newCalendarPeekDriver(),
		now:       time.Now,
		loc:       time.Local,
		lease:     &sharedComputerUseLease,
		suppress:  setActivityCaptureSuppressed,
		failures:  map[string]int{},
		holdUntil: map[string]time.Time{},
	}
	svc.state = a.calendarPeekState
	svc.guard = a.calendarPeekGuard
	svc.onRead = a.calendarPeekRead
	svc.notice = a.calendarPeekNotice
	if stored, err := a.store.GetUIState(context.Background(), calendarPeekLastKey); err == nil {
		if t, err := time.Parse(time.RFC3339, stored); err == nil {
			svc.lastPeek = t
		}
	}
	return svc
}

// Start runs the scheduler until ctx ends. Without a driver (no browser
// automation on this platform) it does nothing.
func (s *calendarPeekService) Start(parent context.Context) {
	if s == nil || s.store == nil || s.driver == nil {
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
	s.wg.Add(1)
	go s.loop(ctx)
}

func (s *calendarPeekService) Stop() {
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

// cancelRun stops a run in flight: the switch was turned off or observing
// was paused. The run closes its tab on the way out.
func (s *calendarPeekService) cancelRun() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.runCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *calendarPeekService) loop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(calendarPeekTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.check(ctx)
		}
	}
}

// check applies the rules once and runs a peek when they allow it.
func (s *calendarPeekService) check(ctx context.Context) (calendar.PeekDecision, error) {
	now := s.now()
	state := s.state(now)
	s.mu.Lock()
	state.LastPeek = s.lastPeek
	retryAt := s.notBefore
	s.mu.Unlock()
	if now.Before(retryAt) {
		return calendar.PeekDecision{Reason: "user was active"}, nil
	}
	state.Providers = s.providersNotOnHold(state.Providers, now)
	decision := calendar.DecidePeek(state, now)
	if !decision.Run {
		return decision, nil
	}
	_, err := s.run(ctx, decision.Providers, decision.Reason)
	return decision, err
}

// providersNotOnHold drops providers whose recent reads kept failing.
func (s *calendarPeekService) providersNotOnHold(providers []string, now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, p := range providers {
		if until, held := s.holdUntil[p]; held && now.Before(until) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// run reads each provider in turn and restores the screen afterwards. It
// takes the shared computer-use lease, pauses the activity monitor's capture
// and never runs twice at once. A run the user interrupts does not count as
// a peek; one that merely failed does, so the browser is not reopened for two
// hours.
func (s *calendarPeekService) run(parent context.Context, providers []string, reason string) (calendarPeekResult, error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return calendarPeekResult{}, errors.New("a calendar peek is already running")
	}
	s.running = true
	ctx, cancel := context.WithCancel(parent)
	s.runCancel = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.running = false
		s.runCancel = nil
		s.mu.Unlock()
	}()

	now := s.now()
	res := calendarPeekResult{Reason: reason, At: now}
	if s.lease != nil {
		ok, holder := s.lease.TryAcquire(calendarPeekLeaseName)
		if !ok {
			return res, fmt.Errorf("the screen is in use by %s", holder)
		}
		defer s.lease.Release(calendarPeekLeaseName)
	}
	if s.suppress != nil {
		s.suppress(true)
		defer s.suppress(false)
	}
	if !s.showNoticeOnce(ctx) {
		return res, fmt.Errorf("calendar peek notice not delivered")
	}

	startedAt := now
	// stop names why the run must end now: the user touched the Mac (idle
	// time is re-read before every step and must stay at least a minute,
	// which also catches input between the decision and the run), a guard
	// (switch, pause, lock, device, meeting), or a cancel.
	stop := func() string {
		if ctx.Err() != nil {
			return "cancelled"
		}
		if s.guard != nil {
			if why := s.guard(); why != "" {
				return why
			}
		}
		if idle := s.driver.idle(); idle < calendar.PeekBriefIdle || idle < s.now().Sub(startedAt) {
			return "user input"
		}
		return ""
	}

	previous := s.driver.frontmostBundle()
	var browser string
	var errs []string
	interrupted := false
	for _, provider := range providers {
		if why := stop(); why != "" {
			interrupted = true
			errs = append(errs, why)
			break
		}
		n, used, err := s.readProvider(ctx, provider, now, stop)
		if used != "" {
			browser = used
		}
		if err != nil {
			errs = append(errs, provider+": "+err.Error())
			if errors.Is(err, errCalendarPeekInterrupted) {
				interrupted = true
				break
			}
			s.noteFailure(provider, now)
			continue
		}
		s.noteSuccess(provider)
		res.Providers = append(res.Providers, provider)
		res.Events += n
	}
	// Bring back whatever the user had in front, but only if the browser we
	// drove is still in front: a window the user switched to meanwhile stays.
	if previous != "" && browser != "" && previous != browser && s.driver.frontmostBundle() == browser {
		s.driver.activate(previous)
	}
	s.mu.Lock()
	if interrupted {
		s.notBefore = now.Add(calendarPeekRetryAfterInput)
	} else {
		s.lastPeek = now
	}
	s.mu.Unlock()
	if !interrupted {
		if err := s.store.SetUIState(ctx, calendarPeekLastKey, now.UTC().Format(time.RFC3339)); err != nil {
			log.Printf("[calendar-peek] record last read: %v", err)
		}
	}
	if err := recordCalendarPeekWindow(context.Background(), s.store, startedAt, s.now()); err != nil {
		log.Printf("[calendar-peek] record peek window: %v", err)
	}
	log.Printf("[calendar-peek] %s: read %d events from %d calendar(s)", reason, res.Events, len(res.Providers))
	if len(errs) > 0 {
		return res, errors.New(strings.Join(errs, "; "))
	}
	return res, nil
}

func (s *calendarPeekService) noteFailure(provider string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[provider]++
	if s.failures[provider] >= calendarPeekFailureLimit {
		s.holdUntil[provider] = now.Add(calendarPeekFailureHold)
		s.failures[provider] = 0
		log.Printf("[calendar-peek] %s failed %d times; not read again for %s", provider, calendarPeekFailureLimit, calendarPeekFailureHold)
	}
}

func (s *calendarPeekService) noteSuccess(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[provider] = 0
	delete(s.holdUntil, provider)
}

// showNoticeOnce tells the user, the first time a peek ever runs, that
// Isoform reads web calendars while the Mac is idle and where to turn it off.
func (s *calendarPeekService) showNoticeOnce(ctx context.Context) bool {
	if s.notice == nil || s.store == nil {
		return false
	}
	shown, err := s.store.GetUIState(ctx, calendarPeekNoticeKey)
	if err != nil {
		return false
	}
	if shown != "" {
		return true
	}
	if !s.notice() {
		return false
	}
	if err := s.store.SetUIState(ctx, calendarPeekNoticeKey, s.now().UTC().Format(time.RFC3339)); err != nil {
		log.Printf("[calendar-peek] record notice: %v", err)
		return false
	}
	return true
}

// readProvider opens one web calendar, reads its week view and stores the
// events. It returns the browser it drove. The tab it opened is closed before
// it returns, whatever happened, and the read stops at the first sign of the
// user (stop returns a reason).
func (s *calendarPeekService) readProvider(ctx context.Context, provider string, now time.Time, stop func() string) (int, string, error) {
	url := calendar.WebCalendarURL(provider)
	fragment := calendar.WebSourceHost(provider)
	if url == "" || fragment == "" {
		return 0, "", fmt.Errorf("no week view for %s", provider)
	}
	if why := stop(); why != "" {
		return 0, "", fmt.Errorf("%w (%s)", errCalendarPeekInterrupted, why)
	}
	if !s.driver.openURL(url) {
		return 0, "", errors.New("the browser did not open")
	}
	// Identify the tab as soon as a browser is in front, so it can be closed
	// by identity even when the page redirects (sign-in, another Outlook
	// host) or never finishes loading.
	browser, tabRef := "", ""
	deadline := s.now().Add(calendarPeekPageWait)
	interrupted := ""
	for s.now().Before(deadline) {
		if why := stop(); why != "" {
			interrupted = why
			break
		}
		front := s.driver.frontmostBundle()
		if s.driver.isBrowser(front) {
			if browser == "" {
				browser = front
				tabRef = s.driver.activeTabRef(front)
			}
			if strings.Contains(s.driver.browserURL(front), fragment) {
				break
			}
		}
		s.driver.sleep(calendarPeekPoll)
	}
	closeTab := func() {
		if browser != "" && tabRef != "" {
			s.driver.closeTabRef(browser, tabRef, fragment)
		}
	}
	if interrupted != "" {
		closeTab()
		return 0, browser, fmt.Errorf("%w (%s)", errCalendarPeekInterrupted, interrupted)
	}
	if browser == "" || !strings.Contains(s.driver.browserURL(browser), fragment) {
		closeTab()
		return 0, browser, errors.New("the calendar did not show in a browser Isoform can read")
	}
	defer closeTab()
	s.driver.sleep(calendarPeekSettle)
	if why := stop(); why != "" {
		return 0, browser, fmt.Errorf("%w (%s)", errCalendarPeekInterrupted, why)
	}

	tree := s.driver.readTree(browser)
	if tree == nil {
		return 0, browser, errors.New("the calendar page could not be read")
	}
	items := appparser.CollectCalendarEvents(tree)
	webItems := make([]calendar.WebEventItem, 0, len(items))
	for _, item := range items {
		webItems = append(webItems, calendar.WebEventItem{Title: item.Title, Time: item.Time, Location: item.Location, Date: item.Date})
	}
	events := calendar.ParseWebEvents(webItems, provider, now, s.loc)
	rows := make([]storage.CalendarEventRow, 0, len(events))
	seen := map[string]bool{}
	for _, ev := range events {
		rows = append(rows, calendarEventRow(ev))
		seen[ev.ID] = true
	}
	if err := s.store.UpsertCalendarEvents(ctx, rows, now.UnixMilli()); err != nil {
		return 0, browser, err
	}
	// The view shows the days around today; an event it no longer shows is
	// gone. An empty read (a page that had not rendered) must not delete them.
	if len(events) > 0 {
		from := now.Add(-calendarPeekWindow)
		to := now.Add(calendarPeekWindow)
		if _, err := s.store.MarkCalendarEventsVanished(ctx, calendar.SourceWeb, provider, from.UnixMilli(), to.UnixMilli(), seen, now.UnixMilli()); err != nil {
			log.Printf("[calendar-peek] mark vanished: %v", err)
		}
	}
	// A peek is a read, not a sighting: last_seen_at keeps saying when the
	// user last opened the calendar, so a calendar they stop using ages out.
	if err := s.store.TouchCalendarSourceRead(ctx, calendar.WebSourceID(provider), now.UnixMilli()); err != nil {
		log.Printf("[calendar-peek] record read: %v", err)
	}
	if s.onRead != nil {
		s.onRead(provider, len(events), now)
	}
	return len(events), browser, nil
}

// calendarPeekState gathers the rule inputs from the app.
func (a *App) calendarPeekState(now time.Time) calendar.PeekState {
	state := calendar.PeekState{
		Enabled:          a.calendarPeekEnabled() && a.isPermissionEnabled(calendarPermissionKey),
		Granted:          calendarAccessGranted(),
		ComputerUseReady: activitymon.ComputerUseSupported() && activitymon.ComputerUsePermitted(),
		ComputerUseBusy:  sharedComputerUseLease.Holder() != "",
		ClosedEyes:       a.IsClosedEyesModeEnabled(),
		ScreenLocked:     activitymon.IsScreenLocked(),
		Idle:             activitymon.IdleTime(),
		Fullscreen:       frontWindowFillsScreen(activitymon.FrontmostWindowBounds()),
	}
	if a.calendarPeek != nil && a.calendarPeek.driver != nil {
		state.BrowserScriptable = activitymon.BrowserScriptable(a.calendarPeek.driver.defaultBrowser())
	}
	if a.contextSignals != nil {
		snap := a.contextSignals.snapshot()
		state.DeviceInUse = snap.MicInUse || snap.CameraInUse
	}
	if a.store != nil {
		if rows, err := a.store.ListCalendarSources(context.Background()); err == nil {
			var sources []calendar.Source
			for _, row := range rows {
				sources = append(sources, calendarSourceFromRow(row))
			}
			state.Providers = calendar.WebCalendarsToPeek(sources, now, calendar.PeekRecentUse)
		}
	}
	if schedule, err := a.GetMobileBriefSchedule(); err == nil && schedule.Enabled {
		state.BriefTimes = calendar.BriefTimesFor(now, time.Local, schedule.Morning, schedule.Evening)
	}
	if a.activity != nil {
		a.activity.mu.Lock()
		state.InMeeting = a.activity.inMeeting
		a.activity.mu.Unlock()
	}
	return state
}

// calendarPeekGuard is re-read before every step of a run: the switches,
// observing paused, the lock, a device in use, a meeting.
func (a *App) calendarPeekGuard() string {
	switch {
	case !a.calendarPeekEnabled() || !a.isPermissionEnabled(calendarPermissionKey):
		return "switched off"
	case a.IsClosedEyesModeEnabled():
		return "closed eyes"
	case activitymon.IsScreenLocked():
		return "screen locked"
	}
	if a.contextSignals != nil {
		if snap := a.contextSignals.snapshot(); snap.MicInUse || snap.CameraInUse {
			return "microphone or camera in use"
		}
	}
	if a.activity != nil {
		a.activity.mu.Lock()
		inMeeting := a.activity.inMeeting
		a.activity.mu.Unlock()
		if inMeeting {
			return "in a meeting"
		}
	}
	return ""
}

// frontWindowFillsScreen reports a window that covers the screen: a
// presentation, a video, a fullscreen app.
func frontWindowFillsScreen(b activitymon.WindowBounds) bool {
	return b.W >= 0.98 && b.H >= 0.98
}

// calendarPeekRead logs one read in the Activity log and refreshes Settings.
// The log row is rendered by the frontend from the provider and the count.
func (a *App) calendarPeekRead(provider string, events int, at time.Time) {
	a.recordSystemActivityEvent(activitymon.ActivityEvent{
		Timestamp: at.UnixMilli(),
		Kind:      activitymon.EventCalendarPeek,
		Provider:  provider,
		Count:     events,
		Detail:    fmt.Sprintf("calendar peek: %d events from %s", events, provider),
	})
	a.emitBrowserEvent("calendar:synced")
}

// calendarPeekNotice is the one-time desktop notice before the first peek.
func (a *App) calendarPeekNotice() bool {
	return a.notifyAutomaticDesktop(
		"Isoform can read web calendars when the computer is idle",
		"It opens your browser briefly while you are away and closes it again. Turn this off in Settings > What Isoform sees.",
		"settings:activity",
	) == "desktop"
}

// startCalendarPeek starts the scheduler. It runs once from startup, after
// the calendar reader, and needs no permission beyond Accessibility.
func (a *App) startCalendarPeek(ctx context.Context) {
	if a == nil || a.store == nil {
		return
	}
	svc := newCalendarPeekService(a)
	a.calendarPeek = svc
	svc.Start(ctx)
}

func (a *App) stopCalendarPeek() {
	if a == nil || a.calendarPeek == nil {
		return
	}
	a.calendarPeek.Stop()
}
