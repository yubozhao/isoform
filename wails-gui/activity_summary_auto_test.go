package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"wails-gui/storage"
)

// autoSummaryGenRecorder is an injected generator fake: it records every call
// and never touches a real model. When store is set, a successful call also
// writes the cached summary row the real Generate methods would have written,
// so follow-up evaluations see the summary as in place.
type autoSummaryGenRecorder struct {
	mu       sync.Mutex
	daily    []string
	weekly   []string
	failNext bool
	block    chan struct{}  // when non-nil, calls block until the channel is closed
	store    *ActivityStore // when non-nil, successful calls persist a complete cache row
}

func (r *autoSummaryGenRecorder) generate(kind, date string) error {
	if r.block != nil {
		<-r.block
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case "daily":
		r.daily = append(r.daily, date)
	default:
		r.weekly = append(r.weekly, date)
	}
	if r.failNext {
		r.failNext = false
		return fmt.Errorf("model unavailable")
	}
	if r.store != nil {
		rowDate, rowType := date, kind
		if kind == "weekly" {
			rowDate = date + "/week"
		}
		if _, err := r.store.saveSummaryWithSources(rowDate, rowType, "auto summary", "test-model", false, "en", nil); err != nil {
			return fmt.Errorf("persist fake summary: %w", err)
		}
	}
	return nil
}

func (r *autoSummaryGenRecorder) dailyCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.daily...)
}

func (r *autoSummaryGenRecorder) weeklyCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.weekly...)
}

func (r *autoSummaryGenRecorder) totalCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.daily) + len(r.weekly)
}

// newAutoSummaryTestApp builds an App with temporary stores and an injected
// generator recorder. The fake clock starts at the given time.
func newAutoSummaryTestApp(t *testing.T, now time.Time) (*App, *autoSummaryGenRecorder, func(time.Time)) {
	t.Helper()
	stateStore, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = stateStore.Close() })

	recorder := &autoSummaryGenRecorder{}
	var clockMu sync.Mutex
	fakeNow := now
	app := &App{
		store:    stateStore,
		activity: &activityMonitorState{store: newTestStore(t)},
	}
	app.autoSummaryGenDaily = func(date string) error { return recorder.generate("daily", date) }
	app.autoSummaryGenWeekly = func(date string) error { return recorder.generate("weekly", date) }
	app.autoSummaryNow = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return fakeNow
	}
	setNow := func(t2 time.Time) {
		clockMu.Lock()
		fakeNow = t2
		clockMu.Unlock()
	}
	return app, recorder, setNow
}

// seedAutoSummaryCache writes a cached summary row directly, controlling the
// partial flag and creation time.
func seedAutoSummaryCache(t *testing.T, s *ActivityStore, date, summaryType string, isPartial bool, createdAt time.Time) {
	t.Helper()
	if _, err := s.saveSummaryWithSources(date, summaryType, "cached summary", "test-model", isPartial, "en", nil); err != nil {
		t.Fatalf("saveSummaryWithSources(%s, %s): %v", date, summaryType, err)
	}
	if _, err := s.db.Exec(
		`UPDATE activity_summaries SET created_at = ? WHERE date = ? AND type = ?`,
		createdAt.UnixMilli(), date, summaryType,
	); err != nil {
		t.Fatalf("backdate created_at for %s/%s: %v", date, summaryType, err)
	}
}

// autoSummaryIdle reports whether every background generation has drained.
func autoSummaryIdle(app *App) bool {
	app.autoSummaryMu.Lock()
	defer app.autoSummaryMu.Unlock()
	return len(app.autoSummaryInFlight) == 0
}

func TestEnsureDailySummary_TodayFreshIsReady(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-fresh-1", today, 9, []string{"Code"})
	// Today's rows are saved partial; a fresh partial row must not regenerate.
	seedAutoSummaryCache(t, app.activity.store, today, "daily", true, now.Add(-10*time.Minute))

	data, err := app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusReady {
		t.Fatalf("AutoStatus = %q, want %q", data.AutoStatus, autoStatusReady)
	}
	if recorder.totalCalls() != 0 {
		t.Fatalf("generator calls = %d, want 0 for a fresh today summary", recorder.totalCalls())
	}
}

// Activity that ended after today's summary was written makes it due again.
func TestEnsureDailySummary_TodayStaleRefreshes(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-stale-1", today, 9, []string{"Code"})
	seedAutoSummaryCache(t, app.activity.store, today, "daily", true, now.Add(-2*time.Hour))
	// 11:00 to 11:45: after the 10:00 summary.
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-stale-2", today, 11, []string{"Code"})

	data, err := app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("AutoStatus = %q, want %q", data.AutoStatus, autoStatusRefreshing)
	}
	waitFor(t, 2*time.Second, "stale-today generation to drain", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 || calls[0] != today {
		t.Fatalf("daily generator calls = %v, want exactly [%s]", calls, today)
	}
}

// Snappy 1.0: opening Activity paints the saved summary. A visit with nothing
// new since it was written makes no model call, however old the summary is.
func TestEnsureDailySummary_TwoVisitsWithNoNewActivityGenerateOnce(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, recorder, setNow := newAutoSummaryTestApp(t, now)
	recorder.store = app.activity.store
	seedActivitySummaryI18nSession(t, app.activity.store, "visits-1", today, 9, []string{"Code"})
	// First visit: no summary yet, so one is written.
	if data, err := app.EnsureDailySummary(today); err != nil || data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("first visit: %+v, %v", data, err)
	}
	waitFor(t, 2*time.Second, "first generation to drain", func() bool { return autoSummaryIdle(app) })
	// The summary was written at the first visit's time.
	if _, err := app.activity.store.db.Exec(`UPDATE activity_summaries SET created_at = ? WHERE date = ? AND type = 'daily'`, now.UnixMilli(), today); err != nil {
		t.Fatal(err)
	}
	// Second visit, two hours later, with no activity since: the saved
	// summary is ready and nothing is written.
	setNow(now.Add(2 * time.Hour))
	data, err := app.EnsureDailySummary(today)
	if err != nil || data.AutoStatus != autoStatusReady {
		t.Fatalf("second visit: %+v, %v", data, err)
	}
	waitFor(t, 2*time.Second, "second visit to stay idle", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 {
		t.Fatalf("two visits with no new activity made %d generations, want 1", len(calls))
	}
}

// New activity writes today's summary again at most every 5 minutes.
func TestEnsureDailySummary_NewActivityWaitsFiveMinutes(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, recorder, setNow := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "five-1", today, 11, []string{"Code"})
	// Written at 11:43, two minutes before the 11:00 to 11:45 session ended.
	seedAutoSummaryCache(t, app.activity.store, today, "daily", true, now.Add(-17*time.Minute))
	setNow(time.Date(2026, 10, 20, 11, 46, 0, 0, time.Local))
	if data, err := app.EnsureDailySummary(today); err != nil || data.AutoStatus != autoStatusReady {
		t.Fatalf("3 minutes after writing: %+v, %v", data, err)
	}
	if recorder.totalCalls() != 0 {
		t.Fatalf("regenerated before 5 minutes: %d calls", recorder.totalCalls())
	}
	setNow(time.Date(2026, 10, 20, 11, 48, 0, 0, time.Local))
	if data, err := app.EnsureDailySummary(today); err != nil || data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("5 minutes after writing, with new activity: %+v, %v", data, err)
	}
	waitFor(t, 2*time.Second, "generation to drain", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 {
		t.Fatalf("generations = %d, want 1", len(calls))
	}
}

func TestEnsureDailySummary_TodayMissingGenerates(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-missing-1", today, 10, []string{"Code"})

	data, err := app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("AutoStatus = %q, want %q", data.AutoStatus, autoStatusRefreshing)
	}
	waitFor(t, 2*time.Second, "missing-today generation to drain", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 || calls[0] != today {
		t.Fatalf("daily generator calls = %v, want exactly [%s]", calls, today)
	}
}

func TestEnsureDailySummary_TodayNoActivityDoesNothing(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)

	data, err := app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusUnavailable {
		t.Fatalf("AutoStatus = %q, want %q", data.AutoStatus, autoStatusUnavailable)
	}
	if recorder.totalCalls() != 0 {
		t.Fatalf("generator calls = %d, want 0 for a day without activity", recorder.totalCalls())
	}
}

func TestEnsureDailySummary_PastDayRules(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	cases := []struct {
		name       string
		seedCache  bool
		isPartial  bool
		wantStatus string
		wantCalls  int
	}{
		{name: "complete is never regenerated", seedCache: true, isPartial: false, wantStatus: autoStatusReady, wantCalls: 0},
		{name: "partial regenerates once", seedCache: true, isPartial: true, wantStatus: autoStatusRefreshing, wantCalls: 1},
		{name: "missing generates once", seedCache: false, wantStatus: autoStatusRefreshing, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, recorder, _ := newAutoSummaryTestApp(t, now)
			seedActivitySummaryI18nSession(t, app.activity.store, "auto-past-"+tc.name, yesterday, 15, []string{"Code"})
			if tc.seedCache {
				seedAutoSummaryCache(t, app.activity.store, yesterday, "daily", tc.isPartial, now.AddDate(0, 0, -1))
			}
			data, err := app.EnsureDailySummary(yesterday)
			if err != nil {
				t.Fatalf("EnsureDailySummary: %v", err)
			}
			if data.AutoStatus != tc.wantStatus {
				t.Fatalf("AutoStatus = %q, want %q", data.AutoStatus, tc.wantStatus)
			}
			waitFor(t, 2*time.Second, "generation to drain", func() bool { return autoSummaryIdle(app) })
			if got := recorder.totalCalls(); got != tc.wantCalls {
				t.Fatalf("generator calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

func TestEnsureWeeklySummary_Rules(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local) // Tuesday
	thisMonday := mondayOf(now)
	lastMonday := thisMonday.AddDate(0, 0, -7)

	cases := []struct {
		name        string
		weekMonday  time.Time
		seedCache   bool
		isPartial   bool
		createdAt   time.Time
		seedSession bool
		wantStatus  string
		wantCalls   int
	}{
		{name: "current week missing generates", weekMonday: thisMonday, seedSession: true, wantStatus: autoStatusRefreshing, wantCalls: 1},
		{name: "current week fresh is ready", weekMonday: thisMonday, seedCache: true, createdAt: now.Add(-1 * time.Hour), seedSession: true, wantStatus: autoStatusReady, wantCalls: 0},
		{name: "current week stale refreshes", weekMonday: thisMonday, seedCache: true, createdAt: now.Add(-7 * time.Hour), seedSession: true, wantStatus: autoStatusRefreshing, wantCalls: 1},
		{name: "past week complete is ready", weekMonday: lastMonday, seedCache: true, createdAt: lastMonday.Add(5 * 24 * time.Hour), seedSession: true, wantStatus: autoStatusReady, wantCalls: 0},
		{name: "past week partial regenerates", weekMonday: lastMonday, seedCache: true, isPartial: true, createdAt: lastMonday.Add(5 * 24 * time.Hour), seedSession: true, wantStatus: autoStatusRefreshing, wantCalls: 1},
		{name: "past week missing generates", weekMonday: lastMonday, seedSession: true, wantStatus: autoStatusRefreshing, wantCalls: 1},
		{name: "week without activity does nothing", weekMonday: lastMonday, wantStatus: autoStatusUnavailable, wantCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, recorder, _ := newAutoSummaryTestApp(t, now)
			if tc.seedSession {
				seedActivitySummaryI18nSession(t, app.activity.store, "auto-week-"+tc.name, tc.weekMonday.Format("2006-01-02"), 11, []string{"Code"})
			}
			if tc.seedCache {
				seedAutoSummaryCache(t, app.activity.store, tc.weekMonday.Format("2006-01-02")+"/week", "weekly", tc.isPartial, tc.createdAt)
			}
			data, err := app.EnsureWeeklySummary(tc.weekMonday.Format("2006-01-02"))
			if err != nil {
				t.Fatalf("EnsureWeeklySummary: %v", err)
			}
			if data.AutoStatus != tc.wantStatus {
				t.Fatalf("AutoStatus = %q, want %q", data.AutoStatus, tc.wantStatus)
			}
			waitFor(t, 2*time.Second, "generation to drain", func() bool { return autoSummaryIdle(app) })
			if got := recorder.totalCalls(); got != tc.wantCalls {
				t.Fatalf("generator calls = %d, want %d", got, tc.wantCalls)
			}
			if tc.wantCalls > 0 {
				if calls := recorder.weeklyCalls(); len(calls) != 1 || calls[0] != tc.weekMonday.Format("2006-01-02") {
					t.Fatalf("weekly generator calls = %v, want exactly [%s]", calls, tc.weekMonday.Format("2006-01-02"))
				}
			}
		})
	}
}

func TestEnsureDailySummary_ConcurrentCallsStartOneGeneration(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-flight-1", today, 10, []string{"Code"})
	recorder.block = make(chan struct{})

	statuses := make([]string, 2)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data, err := app.EnsureDailySummary(today)
			if err != nil {
				t.Errorf("EnsureDailySummary: %v", err)
				return
			}
			statuses[i] = data.AutoStatus
		}(i)
	}
	wg.Wait()
	close(recorder.block)

	for i, status := range statuses {
		if status != autoStatusRefreshing {
			t.Fatalf("call %d AutoStatus = %q, want %q", i, status, autoStatusRefreshing)
		}
	}
	waitFor(t, 2*time.Second, "single-flight generation to drain", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 {
		t.Fatalf("daily generator calls = %v, want exactly one start for concurrent calls", calls)
	}
}

func TestAutoSummaryNightly_MakesYesterdayOnceOnly(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 30, 0, 0, time.Local) // Tuesday, after midnight
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)
	recorder.store = app.activity.store // successful fakes persist the summary row
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-nightly-1", yesterday, 15, []string{"Code"})

	app.autoSummaryNightlyPass(now)
	waitFor(t, 2*time.Second, "nightly generation to drain", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 || calls[0] != yesterday {
		t.Fatalf("daily generator calls = %v, want exactly [%s]", calls, yesterday)
	}

	// A second pass the same night does nothing: yesterday's summary is in
	// place, so the pass marks the date done.
	app.autoSummaryNightlyPass(now.Add(30 * time.Minute))
	waitFor(t, 2*time.Second, "second nightly pass to settle", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 {
		t.Fatalf("daily generator calls after second pass = %v, want still exactly one", calls)
	}
}

func TestAutoSummaryNightly_FailedGenerationRetriesAfterBackoff(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 30, 0, 0, time.Local) // Tuesday, after midnight
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	app, recorder, setNow := newAutoSummaryTestApp(t, now)
	recorder.store = app.activity.store // the retry persists the summary row
	recorder.failNext = true            // the first nightly generation fails
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-nightly-retry-1", yesterday, 15, []string{"Code"})

	// Pass 1: the generation starts and fails. The date must stay open.
	app.autoSummaryNightlyPass(now)
	waitFor(t, 2*time.Second, "failed nightly generation to drain", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 {
		t.Fatalf("daily generator calls = %v, want exactly one failed attempt", calls)
	}

	// Pass 2, inside the backoff window: no new attempt, date still not done.
	setNow(now.Add(15 * time.Minute))
	app.autoSummaryNightlyPass(now.Add(15 * time.Minute))
	waitFor(t, 2*time.Second, "backoff pass to settle", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 {
		t.Fatalf("daily generator calls during backoff = %v, want still one", calls)
	}

	// Pass 3, after the backoff expires: the tick retries and the summary lands.
	setNow(now.Add(31 * time.Minute))
	app.autoSummaryNightlyPass(now.Add(31 * time.Minute))
	waitFor(t, 2*time.Second, "retry generation to drain", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 2 {
		t.Fatalf("daily generator calls after backoff = %v, want a retry", calls)
	}

	// Pass 4: the summary is now in place, so the pass completes the date;
	// pass 5 then early-returns. No further attempts either way.
	setNow(now.Add(46 * time.Minute))
	app.autoSummaryNightlyPass(now.Add(46 * time.Minute))
	setNow(now.Add(61 * time.Minute))
	app.autoSummaryNightlyPass(now.Add(61 * time.Minute))
	waitFor(t, 2*time.Second, "final passes to settle", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 2 {
		t.Fatalf("daily generator calls after success = %v, want still two", calls)
	}
}

func TestAutoSummaryNightly_MondayMakesLastWeekSummary(t *testing.T) {
	now := time.Date(2026, 10, 19, 1, 0, 0, 0, time.Local) // Monday, after midnight
	if now.Weekday() != time.Monday {
		t.Fatalf("test premise broken: %s is not a Monday", now.Format("2006-01-02"))
	}
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	lastMonday := mondayOf(now).AddDate(0, 0, -7).Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-nightly-mon-1", yesterday, 15, []string{"Code"})
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-nightly-mon-2", lastMonday, 12, []string{"Code"})

	app.autoSummaryNightlyPass(now)
	waitFor(t, 2*time.Second, "monday nightly generation to drain", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != 1 || calls[0] != yesterday {
		t.Fatalf("daily generator calls = %v, want exactly [%s]", calls, yesterday)
	}
	if calls := recorder.weeklyCalls(); len(calls) != 1 || calls[0] != lastMonday {
		t.Fatalf("weekly generator calls = %v, want exactly [%s]", calls, lastMonday)
	}
}

func TestAutoSummaryNightly_SkipsWhenNothingToDo(t *testing.T) {
	now := time.Date(2026, 10, 20, 3, 0, 0, 0, time.Local)
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-nightly-done-1", yesterday, 15, []string{"Code"})
	// Yesterday already has a complete summary.
	seedAutoSummaryCache(t, app.activity.store, yesterday, "daily", false, now.Add(-3*time.Hour))

	app.autoSummaryNightlyPass(now)
	waitFor(t, 2*time.Second, "nightly pass to settle", func() bool { return autoSummaryIdle(app) })
	if recorder.totalCalls() != 0 {
		t.Fatalf("generator calls = %d, want 0 when yesterday is already complete", recorder.totalCalls())
	}
}

func TestEnsureDailySummary_FailureBacksOffThirtyMinutes(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, recorder, setNow := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-backoff-1", today, 10, []string{"Code"})
	recorder.failNext = true

	data, err := app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("AutoStatus = %q, want %q", data.AutoStatus, autoStatusRefreshing)
	}
	waitFor(t, 2*time.Second, "failed generation to drain", func() bool { return autoSummaryIdle(app) })
	if recorder.totalCalls() != 1 {
		t.Fatalf("generator calls = %d, want 1", recorder.totalCalls())
	}

	// Within the backoff window the binding reports unavailable and starts nothing.
	data, err = app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusUnavailable {
		t.Fatalf("AutoStatus during backoff = %q, want %q", data.AutoStatus, autoStatusUnavailable)
	}
	if recorder.totalCalls() != 1 {
		t.Fatalf("generator calls during backoff = %d, want still 1", recorder.totalCalls())
	}

	setNow(now.Add(29 * time.Minute))
	data, err = app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusUnavailable || recorder.totalCalls() != 1 {
		t.Fatalf("at +29m AutoStatus = %q calls = %d, want %q and 1", data.AutoStatus, recorder.totalCalls(), autoStatusUnavailable)
	}

	// After the backoff expires the next call retries.
	setNow(now.Add(31 * time.Minute))
	data, err = app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("AutoStatus after backoff = %q, want %q", data.AutoStatus, autoStatusRefreshing)
	}
	waitFor(t, 2*time.Second, "retry generation to drain", func() bool { return autoSummaryIdle(app) })
	if recorder.totalCalls() != 2 {
		t.Fatalf("generator calls after backoff = %d, want 2", recorder.totalCalls())
	}
}

func TestEnsureDailySummary_BackoffKeepsCachedSummaryReady(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-backoff-cache-1", today, 10, []string{"Code"})
	// A stale cached summary for today: due for refresh, but present.
	seedAutoSummaryCache(t, app.activity.store, today, "daily", true, now.Add(-2*time.Hour))
	recorder.failNext = true

	data, err := app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("AutoStatus = %q, want %q for a stale today summary", data.AutoStatus, autoStatusRefreshing)
	}
	waitFor(t, 2*time.Second, "failed generation to drain", func() bool { return autoSummaryIdle(app) })
	if recorder.totalCalls() != 1 {
		t.Fatalf("generator calls = %d, want 1", recorder.totalCalls())
	}

	// During the backoff the cached summary keeps showing: ready, not
	// unavailable, and no new generation starts.
	data, err = app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusReady {
		t.Fatalf("AutoStatus during backoff with cache = %q, want %q", data.AutoStatus, autoStatusReady)
	}
	if data.AISummaryStatus != "done" || data.AISummary == "" {
		t.Fatalf("cached summary must still be served during backoff: status=%q", data.AISummaryStatus)
	}
	if recorder.totalCalls() != 1 {
		t.Fatalf("generator calls during backoff = %d, want still 1", recorder.totalCalls())
	}
}

func TestAutoSummary_ClosedEyesGeneratesNothing(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	app, recorder, _ := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-paused-1", today, 10, []string{"Code"})
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-paused-2", yesterday, 15, []string{"Code"})
	app.closedEyes.enabled.Store(true)
	t.Cleanup(func() { app.closedEyes.enabled.Store(false) })

	data, err := app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusUnavailable {
		t.Fatalf("AutoStatus with closed eyes = %q, want %q", data.AutoStatus, autoStatusUnavailable)
	}
	app.autoSummaryNightlyPass(now)
	waitFor(t, 2*time.Second, "closed-eyes settle", func() bool { return autoSummaryIdle(app) })
	if recorder.totalCalls() != 0 {
		t.Fatalf("generator calls with closed eyes = %d, want 0", recorder.totalCalls())
	}
}

func TestAutoSummary_FinishEventEmitted(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, _, _ := newAutoSummaryTestApp(t, now)
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-event-1", today, 10, []string{"Code"})
	app.eventBroadcaster = NewEventBroadcaster()
	client := app.eventBroadcaster.AddClient("auto-summary-event-test")
	defer app.eventBroadcaster.RemoveClient("auto-summary-event-test")

	if _, err := app.EnsureDailySummary(today); err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case payload, ok := <-client:
			if !ok {
				t.Fatal("event channel closed before activity:summary-ready")
			}
			var evt BrowserEvent
			if err := json.Unmarshal(payload, &evt); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}
			if evt.Event != "activity:summary-ready" {
				continue
			}
			if len(evt.Args) != 1 || evt.Args[0] != today {
				t.Fatalf("summary-ready args = %v, want [%s]", evt.Args, today)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for activity:summary-ready")
		}
	}
}

func TestAutoSummaryNightly_StopsAfterThreeFailedAttemptsButPageOpenStillTries(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 30, 0, 0, time.Local) // Tuesday, after midnight
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	app, recorder, setNow := newAutoSummaryTestApp(t, now)
	recorder.store = app.activity.store
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-nightly-cap-1", yesterday, 15, []string{"Code"})

	// Every nightly generation fails, after the model answered. Passes run
	// past each back-off; the pass starts at most three attempts.
	for i := 0; i < 6; i++ {
		at := now.Add(time.Duration(i) * (autoSummaryFailureBackoff + time.Minute))
		setNow(at)
		recorder.mu.Lock()
		recorder.failNext = true
		recorder.mu.Unlock()
		app.autoSummaryNightlyPass(at)
		waitFor(t, 2*time.Second, "nightly attempt to drain", func() bool { return autoSummaryIdle(app) })
	}
	if calls := recorder.dailyCalls(); len(calls) != autoSummaryNightlyMaxTries {
		t.Fatalf("nightly attempts = %v, want %d", calls, autoSummaryNightlyMaxTries)
	}
	app.autoSummaryMu.Lock()
	given := app.autoSummaryNightlyFor
	app.autoSummaryMu.Unlock()
	if given != now.Format("2006-01-02") {
		t.Fatalf("the pass must give the date up at the cap, done for %q", given)
	}

	// Opening Activity is not capped: it still tries, and this time succeeds.
	recorder.mu.Lock()
	recorder.failNext = false
	recorder.mu.Unlock()
	later := now.Add(6*(autoSummaryFailureBackoff+time.Minute) + autoSummaryFailureBackoff)
	setNow(later)
	data, err := app.EnsureDailySummary(yesterday)
	if err != nil {
		t.Fatal(err)
	}
	if data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("page open after the nightly cap: AutoStatus = %q, want refreshing", data.AutoStatus)
	}
	waitFor(t, 2*time.Second, "page-open generation to drain", func() bool { return autoSummaryIdle(app) })
	if calls := recorder.dailyCalls(); len(calls) != autoSummaryNightlyMaxTries+1 {
		t.Fatalf("page open did not try after the cap: %v", calls)
	}
}

// Opening Activity in the first seconds after launch reaches the generator
// before the model runner is up. That attempt must wait for the runner and
// retry once it is ready, not fail into the 30-minute backoff (which left
// today's summary unwritten for hours after a launch).
func TestAutoSummary_WaitsForRunnerInsteadOfBackingOff(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, _, _ := newAutoSummaryTestApp(t, now)
	app.autoSummaryRunnerWait = 10 * time.Millisecond
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-runner-1", today, 10, []string{"Code"})

	var mu sync.Mutex
	calls := 0
	app.autoSummaryGenDaily = func(string) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		app.mu.Lock()
		ready := app.runner != nil
		app.mu.Unlock()
		if !ready {
			return fmt.Errorf("generate: %w", errRunnerNotInitialized)
		}
		return nil
	}

	data, err := app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("AutoStatus = %q, want %q", data.AutoStatus, autoStatusRefreshing)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	early := calls
	mu.Unlock()
	if early != 1 {
		t.Fatalf("generator calls before the runner is up = %d, want 1 (no retry storm)", early)
	}
	app.mu.Lock()
	app.runner = &purposeExtractionFakeRunner{}
	app.mu.Unlock()
	waitFor(t, 2*time.Second, "generation to finish once the runner is up", func() bool { return autoSummaryIdle(app) })
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 2 {
		t.Fatalf("generator calls = %d, want 2 (first before the runner, one retry after)", got)
	}
	app.autoSummaryMu.Lock()
	_, blocked := app.autoSummaryRetryAfter["daily|"+today]
	app.autoSummaryMu.Unlock()
	if blocked {
		t.Fatal("a runner that was not up yet set the failure backoff")
	}
}

// A runner that never comes up within the wait leaves no backoff: the next
// visit may try again at once.
func TestAutoSummary_RunnerNeverReadyLeavesNoBackoff(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	app, _, _ := newAutoSummaryTestApp(t, now)
	app.autoSummaryRunnerWait = time.Millisecond
	seedActivitySummaryI18nSession(t, app.activity.store, "auto-runner-2", today, 10, []string{"Code"})
	app.autoSummaryGenDaily = func(string) error { return errRunnerNotInitialized }

	if _, err := app.EnsureDailySummary(today); err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	waitFor(t, 2*time.Second, "generation to give up", func() bool { return autoSummaryIdle(app) })
	data, err := app.EnsureDailySummary(today)
	if err != nil {
		t.Fatalf("EnsureDailySummary: %v", err)
	}
	if data.AutoStatus != autoStatusRefreshing {
		t.Fatalf("second visit AutoStatus = %q, want %q (no backoff)", data.AutoStatus, autoStatusRefreshing)
	}
	waitFor(t, 2*time.Second, "second generation to give up", func() bool { return autoSummaryIdle(app) })
}
