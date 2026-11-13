package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ──────────────────────────────────────────────
// Auto summaries: the day and week digests make themselves
// ──────────────────────────────────────────────
//
// EnsureDailySummary / EnsureWeeklySummary return the cached summary data
// immediately (never waiting for the model) and start at most one background
// generation when the rules below say one is due. The screen reads AutoStatus
// to know whether a refresh is running; when the generation finishes the same
// activity:summary-ready event the manual Generate methods emit is fired, so
// a listening screen refreshes on its own.
//
// Quota discipline: a past day with a complete summary is never regenerated,
// today's is written again only when activity ended after it was written and
// at most once per autoSummaryTodayMinAge (snappy-and-first-run-1.0.md: open
// Activity paints the saved summary; a visit with nothing new costs no model
// call), the current week at most once per autoSummaryWeekFreshness, and a
// failed (type, date) is not retried for autoSummaryFailureBackoff.

const (
	// autoSummaryTodayMinAge is how old today's summary must be before new
	// activity can have it written again.
	autoSummaryTodayMinAge = 5 * time.Minute
	// autoSummaryWeekFreshness is the freshness window for the current week.
	autoSummaryWeekFreshness = 6 * time.Hour
	// autoSummaryFailureBackoff is how long a (type, date) is left alone
	// after a background generation failure.
	autoSummaryFailureBackoff = 30 * time.Minute
	// autoSummaryNightlyInterval is how often the after-midnight pass checks
	// for unfinished yesterday / last-week summaries.
	autoSummaryNightlyInterval = 15 * time.Minute
	// autoSummaryNightlyMaxTries caps the generations the after-midnight pass
	// starts for one (kind, date).
	autoSummaryNightlyMaxTries = 3
)

// AutoStatus values reported on DailySummaryData / WeeklySummaryData.
const (
	autoStatusReady       = "ready"       // cached summary is the best available; nothing running
	autoStatusRefreshing  = "refreshing"  // a background generation is running; listen for activity:summary-ready
	autoStatusUnavailable = "unavailable" // no summary to show and none being made right now
)

// autoKickOutcome tells the caller why kickAutoSummary did or did not start a
// background generation.
type autoKickOutcome int

const (
	autoKickStarted  autoKickOutcome = iota // a new generation was started
	autoKickInFlight                        // one was already running for (kind, date, language)
	autoKickBackoff                         // a recent failure blocks retrying this (kind, date)
	autoKickPaused                          // observation is paused (Closed Eyes)
)

// EnsureDailySummary returns the daily summary data immediately plus an
// AutoStatus describing the auto-generation state, and starts at most one
// background generation when the cached AI summary is missing or stale.
// Wails binding.
func (a *App) EnsureDailySummary(dateStr string) (*DailySummaryData, error) {
	data, err := a.GetDailySummary(dateStr)
	if err != nil {
		return nil, err
	}
	now := a.autoSummaryClock()
	hasCache := data.AISummaryStatus == "done"
	shouldGenerate := autoDailyShouldGenerate(dateStr, now, hasCache, data.IsPartial, data.SummaryCreatedAt, data.SessionCount > 0,
		activityEndedAfter(data.modelSessions, data.SummaryCreatedAt))
	data.AutoStatus = a.autoSummaryStatus("daily", dateStr, hasCache, shouldGenerate)
	return data, nil
}

// activityEndedAfter reports a readable session of the day that ended after
// the summary was written: something the summary does not yet cover.
func activityEndedAfter(sessions []ActivitySessionRow, writtenAtMs int64) bool {
	for _, session := range sessions {
		if session.EndTime > writtenAtMs {
			return true
		}
	}
	return false
}

// EnsureWeeklySummary returns the weekly summary data immediately plus an
// AutoStatus, starting at most one background generation when the cached AI
// summary for the week is missing or stale. Wails binding.
func (a *App) EnsureWeeklySummary(dateStr string) (*WeeklySummaryData, error) {
	data, err := a.GetWeeklySummary(dateStr)
	if err != nil {
		return nil, err
	}
	now := a.autoSummaryClock()
	hasCache := data.AISummaryStatus == "done"
	var isPartial bool
	var createdAt int64
	if hasCache {
		// GetWeeklySummary read the same row for AISummary; fetch its
		// freshness/partial flags for the rules.
		_, isPartial, createdAt, _, _ = a.cachedActivitySummaryForRead(data.WeekStart+"/week", "weekly", a.currentUILanguage())
	}
	isCurrentWeek := data.WeekStart == mondayOf(now).Format("2006-01-02")
	shouldGenerate := autoWeeklyShouldGenerate(isCurrentWeek, now, hasCache, isPartial, createdAt, data.TotalActiveMs > 0)
	data.AutoStatus = a.autoSummaryStatus("weekly", data.WeekStart, hasCache, shouldGenerate)
	return data, nil
}

// autoSummaryStatus resolves the AutoStatus for a binding call, kicking off a
// background generation when the rules want one.
func (a *App) autoSummaryStatus(kind, date string, hasCache, shouldGenerate bool) string {
	if !shouldGenerate {
		switch hasCache {
		case true:
			return autoStatusReady
		default:
			return autoStatusUnavailable
		}
	}
	switch a.kickAutoSummary(kind, date, a.autoSummaryGenerator(kind)) {
	case autoKickStarted, autoKickInFlight:
		return autoStatusRefreshing
	default:
		// autoKickPaused / autoKickBackoff: no generation is running, so the
		// cached summary — even a stale one waiting out its failure backoff —
		// stays the best available and must keep showing.
		switch hasCache {
		case true:
			return autoStatusReady
		default:
			return autoStatusUnavailable
		}
	}
}

// autoDailyShouldGenerate applies the daily rules: today generates when the
// summary is missing, or when activity ended after it was written and it is at
// least autoSummaryTodayMinAge old; a past day generates once when missing or
// partial and is never regenerated once complete. Days without recorded
// activity never generate.
func autoDailyShouldGenerate(dateStr string, now time.Time, hasCache, isPartial bool, createdAtMs int64, hasActivity, newActivity bool) bool {
	if !hasActivity {
		return false
	}
	switch strings.Compare(dateStr, now.Format("2006-01-02")) {
	case 0:
		return !hasCache || newActivity && now.Sub(time.UnixMilli(createdAtMs)) >= autoSummaryTodayMinAge
	case 1:
		// Future day.
		return false
	default:
		return !hasCache || isPartial
	}
}

// autoWeeklyShouldGenerate applies the weekly rules: the current week
// regenerates when missing or older than the freshness window; a past week
// generates once when missing or partial. Weeks without activity never
// generate.
func autoWeeklyShouldGenerate(isCurrentWeek bool, now time.Time, hasCache, isPartial bool, createdAtMs int64, hasActivity bool) bool {
	if !hasActivity {
		return false
	}
	switch isCurrentWeek {
	case true:
		return !hasCache || now.Sub(time.UnixMilli(createdAtMs)) > autoSummaryWeekFreshness
	default:
		return !hasCache || isPartial
	}
}

// kickAutoSummary starts at most one background generation per
// (kind, date, language). A second call while one runs starts nothing and
// reports autoKickInFlight. Generation never starts while observation is
// paused, nor within autoSummaryFailureBackoff of a failure for (kind, date).
func (a *App) kickAutoSummary(kind, date string, gen func(string) error) autoKickOutcome {
	if a.IsClosedEyesModeEnabled() {
		return autoKickPaused
	}
	backoffKey := kind + "|" + date
	flightKey := backoffKey + "|" + normalizeSummaryLanguage(a.currentUILanguage())
	now := a.autoSummaryClock()

	a.autoSummaryMu.Lock()
	if a.autoSummaryInFlight == nil {
		a.autoSummaryInFlight = make(map[string]bool)
	}
	if a.autoSummaryRetryAfter == nil {
		a.autoSummaryRetryAfter = make(map[string]time.Time)
	}
	if until, blocked := a.autoSummaryRetryAfter[backoffKey]; blocked && now.Before(until) {
		a.autoSummaryMu.Unlock()
		return autoKickBackoff
	}
	if a.autoSummaryInFlight[flightKey] {
		a.autoSummaryMu.Unlock()
		return autoKickInFlight
	}
	a.autoSummaryInFlight[flightKey] = true
	a.autoSummaryMu.Unlock()

	go a.runAutoSummaryGeneration(date, backoffKey, flightKey, gen)
	return autoKickStarted
}

// runAutoSummaryGeneration runs the generator, records a failure backoff on
// error, and on success emits the same event the manual Generate methods emit
// so a listening screen refreshes.
func (a *App) runAutoSummaryGeneration(date, backoffKey, flightKey string, gen func(string) error) {
	var err error
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
		a.autoSummaryMu.Lock()
		delete(a.autoSummaryInFlight, flightKey)
		if err != nil {
			a.autoSummaryRetryAfter[backoffKey] = a.autoSummaryClock().Add(autoSummaryFailureBackoff)
		}
		a.autoSummaryMu.Unlock()
		if err != nil {
			log.Printf("[activity-summary] auto summary failed for %s: %s", date, truncateMomentsLog(err.Error(), 120))
			return
		}
		a.emitAutoSummaryReady(date)
	}()
	err = gen(date)
}

// emitAutoSummaryReady emits the same event the manual Generate methods emit
// when a summary finishes (activity:summary-ready with the date, or the week
// start for weekly), so a screen that listens refreshes. Guards like
// emitBrowserEvent: plain unit/browser contexts have no Wails event bus and
// EventsEmit treats that as fatal.
func (a *App) emitAutoSummaryReady(date string) {
	if a.ctx != nil && a.ctx.Value("events") != nil {
		// A new summary can change the day's noticed items.
		a.touchDerivedViews(derivedDayNoticed)
		wailsRuntime.EventsEmit(a.ctx, "activity:summary-ready", date)
	}
	if a.eventBroadcaster != nil {
		a.eventBroadcaster.Broadcast("activity:summary-ready", date)
	}
}

// autoSummaryGenerator returns the generation function for a summary kind.
// The seams default to the existing manual Generate methods so the same model
// routing (lightTaskModel), language handling, and finish events apply; tests
// inject fakes through App.autoSummaryGenDaily / App.autoSummaryGenWeekly.
func (a *App) autoSummaryGenerator(kind string) func(string) error {
	switch kind {
	case "daily":
		return a.autoGenerateDaily
	default:
		return a.autoGenerateWeekly
	}
}

func (a *App) autoGenerateDaily(dateStr string) error {
	if a.autoSummaryGenDaily != nil {
		return a.autoSummaryGenDaily(dateStr)
	}
	return a.GenerateDailySummary(dateStr)
}

func (a *App) autoGenerateWeekly(dateStr string) error {
	if a.autoSummaryGenWeekly != nil {
		return a.autoSummaryGenWeekly(dateStr)
	}
	return a.GenerateWeeklySummary(dateStr)
}

// autoSummaryClock returns the current time; tests pin it via
// App.autoSummaryNow to exercise staleness and backoff windows.
func (a *App) autoSummaryClock() time.Time {
	if a.autoSummaryNow != nil {
		return a.autoSummaryNow()
	}
	return time.Now()
}

// ──────────────────────────────────────────────
// After-midnight pass
// ──────────────────────────────────────────────

// runAutoSummaryNightly is the lifecycle goroutine for the after-midnight
// auto-summary pass, shaped like runHandoffLifecycle: tick on an interval and
// return when the app context is cancelled at shutdown. The first tick fires
// one interval after startup — app-start recovery belongs to
// checkPendingSummaries. A tick delayed by sleep fires on wake, so the pass
// still runs once the computer is awake.
func (a *App) runAutoSummaryNightly(ctx context.Context) {
	ticker := time.NewTicker(autoSummaryNightlyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.autoSummaryNightlyPass(now)
		}
	}
}

// autoSummaryNightlyPass finishes yesterday's daily summary (and, on Mondays,
// last week's weekly summary) when it is missing or partial. It completes at
// most once per local date and does nothing when there is nothing to do.
// The date is marked done only when no generation is needed — i.e. after the
// summaries are actually in place. A pass that starts a generation stays
// open, so when that generation fails the next tick finds the date still
// unfinished and retries once the failure backoff expires.
func (a *App) autoSummaryNightlyPass(now time.Time) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return
	}
	if a.IsClosedEyesModeEnabled() {
		return
	}
	today := now.Format("2006-01-02")
	a.autoSummaryMu.Lock()
	done := a.autoSummaryNightlyFor == today
	a.autoSummaryMu.Unlock()
	if done {
		return
	}

	pending := false
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	if a.autoDailyNeedsGeneration(yesterday, now) {
		pending = a.autoSummaryNightlyTry("daily", yesterday, a.autoGenerateDaily) || pending
	}
	if now.Weekday() == time.Monday {
		lastMonday := mondayOf(now).AddDate(0, 0, -7).Format("2006-01-02")
		if a.autoWeeklyNeedsGeneration(lastMonday, now) {
			pending = a.autoSummaryNightlyTry("weekly", lastMonday, a.autoGenerateWeekly) || pending
		}
	}
	if pending {
		// A generation was due: it is now running, already in flight, or
		// sitting out a failure backoff. Either way the date is not done —
		// the next tick re-evaluates and finds a complete summary (success)
		// or an expired backoff (retry).
		return
	}
	a.autoSummaryMu.Lock()
	a.autoSummaryNightlyFor = today
	a.autoSummaryMu.Unlock()
}

// autoSummaryNightlyTry starts a nightly generation for (kind, date) unless
// the pass already started autoSummaryNightlyMaxTries of them. A generation
// that keeps failing after the model answered would otherwise be retried
// after every back-off all day, spending model quota each time. It reports
// whether the date is still pending; at the cap it logs once and gives the
// date up, while opening Activity (EnsureDailySummary / EnsureWeeklySummary)
// can still try.
func (a *App) autoSummaryNightlyTry(kind, date string, gen func(string) error) bool {
	key := kind + "|" + date
	a.autoSummaryMu.Lock()
	tries := a.autoSummaryNightlyTries[key]
	a.autoSummaryMu.Unlock()
	if tries >= autoSummaryNightlyMaxTries {
		return false
	}
	if a.kickAutoSummary(kind, date, gen) == autoKickStarted {
		a.autoSummaryMu.Lock()
		if a.autoSummaryNightlyTries == nil {
			a.autoSummaryNightlyTries = map[string]int{}
		}
		a.autoSummaryNightlyTries[key]++
		tries = a.autoSummaryNightlyTries[key]
		a.autoSummaryMu.Unlock()
		if tries == autoSummaryNightlyMaxTries {
			log.Printf("[activity-summary] nightly %s summary for %s: %d attempts started, the nightly pass stops here; opening Activity can still try", kind, date, tries)
		}
	}
	return true
}

// autoDailyNeedsGeneration applies the daily rules using point queries only
// (cache row + activity presence), for callers like the nightly pass that do
// not need the full display payload the bindings compute.
func (a *App) autoDailyNeedsGeneration(dateStr string, now time.Time) bool {
	snapshot, found := a.readableActivitySummarySnapshot(dateStr, "daily", a.currentUILanguage())
	// The nightly pass only finishes past days, whose rule ignores new activity.
	return autoDailyShouldGenerate(dateStr, now, found, snapshot.IsPartial, snapshot.CreatedAt, a.activity.store.HasActivityData(dateStr), false)
}

// autoWeeklyNeedsGeneration is the weekly counterpart of
// autoDailyNeedsGeneration. weekStart is the week's Monday date.
func (a *App) autoWeeklyNeedsGeneration(weekStart string, now time.Time) bool {
	monday, err := time.ParseInLocation("2006-01-02", weekStart, time.Local)
	if err != nil {
		return false
	}
	hasActivity := false
	for i := 0; i < 7; i++ {
		if a.activity.store.HasActivityData(monday.AddDate(0, 0, i).Format("2006-01-02")) {
			hasActivity = true
			break
		}
	}
	snapshot, found := a.readableActivitySummarySnapshot(weekStart+"/week", "weekly", a.currentUILanguage())
	isCurrentWeek := weekStart == mondayOf(now).Format("2006-01-02")
	return autoWeeklyShouldGenerate(isCurrentWeek, now, found, snapshot.IsPartial, snapshot.CreatedAt, hasActivity)
}

// mondayOf returns the Monday (local midnight) of the week containing t.
// Weeks start on Monday, matching GetWeeklySummary.
func mondayOf(t time.Time) time.Time {
	weekday := t.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	monday := t.AddDate(0, 0, -int(weekday-time.Monday))
	return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, monday.Location())
}
