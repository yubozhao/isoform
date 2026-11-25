package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/browserhistory"
)

// The browser history reader copies each Chromium profile's History database
// every fifteen minutes, only when the file or its WAL changed, and stores
// the new page visits made on this Mac in browser_visits. Safari's history
// needs Full Disk Access and is not read; incognito windows never reach
// History; visits synced from other devices are left out.
//
// It reads only while "Watch my screen" is on and observing is not paused,
// and never reaches back before the moment reading was last allowed: the
// first run starts at that moment (no backfill), and so does every return
// after the switch was off and every launch, so the time Isoform was not
// running is never read. A visit inside a pause (the pause log) is never
// stored either, and a browser the user protects on the Privacy page is not
// read, its stored visits are deleted, and once it is no longer protected
// its reading starts from that moment.
//
// What is kept of a visit: the link's scheme, host and plain path (no query,
// no fragment, nothing after a token-like segment) and the page title with
// personal details and codes redacted, for three days.
const (
	browserHistoryInterval = 15 * time.Minute
	browserHistoryFirstRun = time.Minute
	// browserHistoryOverlap is how far before the newest stored visit a read
	// starts again, so a visit History wrote late is not skipped; the unique
	// key keeps it from being stored twice.
	browserHistoryOverlap = 6 * time.Hour
	// browserHistoryRetention is how long a visit is kept: enough for the
	// brief and a few days of research threads.
	browserHistoryRetention = 3 * 24 * time.Hour
	// browserHistoryReadLimit bounds one read of one profile; a full batch
	// is continued at the next tick.
	browserHistoryReadLimit = 5000
	browserHistoryTitleMax  = 300
	// browserHistorySinceKey holds, as Unix milliseconds, the moment reading
	// was last allowed again; no read reaches back before it.
	browserHistorySinceKey = "browser_history_since"
	browserHistoryScratch  = "browser-history"
)

// browserHistoryReader owns the read loop.
type browserHistoryReader struct {
	home    string
	scratch string
	now     func() time.Time
	store   func() *ActivityStore
	// allowed is "Watch my screen" on and observing not paused.
	allowed   captureSwitch
	pauses    func() ([]observationPause, error)
	sensitive func(bundleID, name string) bool
	// since and setSince keep the browserHistorySinceKey marker.
	since    func() (int64, bool)
	setSince func(int64)
	// started is when this run began; no read reaches back before it.
	unknownThrough int64 // last tick with an unreadable switch
	started        time.Time

	// protectedAt holds, per browser name, the last moment the browser was
	// seen protected; its reading never reaches back before it.
	protectedMu sync.Mutex
	protectedAt map[string]int64

	// mtimes remembers each History file's change time at its last full
	// read.
	mtimes map[string]time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// browserVisitSensitive reports whether the user protects this browser, by
// bundle id or by name.
func browserVisitSensitive(bundleID, name string) bool {
	if _, sensitive := pii.IsSensitiveApp(bundleID); sensitive {
		return true
	}
	return pii.IsSensitiveAppName(name)
}

func newBrowserHistoryReader(a *App) *browserHistoryReader {
	home, _ := os.UserHomeDir()
	r := &browserHistoryReader{
		home: home,
		now:  time.Now,
		store: func() *ActivityStore {
			if a.activity == nil {
				return nil
			}
			return a.activity.store
		},
		allowed: func() (bool, bool) {
			if a.IsClosedEyesModeEnabled() {
				return false, true
			}
			return a.screenCaptureState("browser_history")
		},
		pauses:    a.observationPauses,
		sensitive: browserVisitSensitive,
		since: func() (int64, bool) {
			if a.store == nil {
				return 0, false
			}
			raw, err := a.store.GetUIState(context.Background(), browserHistorySinceKey)
			if err != nil || raw == "" {
				return 0, false
			}
			ms, err := strconv.ParseInt(raw, 10, 64)
			return ms, err == nil
		},
		setSince: func(ms int64) {
			if a.store == nil {
				return
			}
			if err := a.store.SetUIState(context.Background(), browserHistorySinceKey, strconv.FormatInt(ms, 10)); err != nil {
				log.Printf("[browser-history] save reading start: %v", err)
			}
		},
		mtimes:      map[string]time.Time{},
		started:     time.Now(),
		protectedAt: map[string]int64{},
	}
	if dataRoot, err := flavorDataDir(); err == nil {
		r.scratch = filepath.Join(dataRoot, "tmp", browserHistoryScratch)
	}
	return r
}

// screenWatchingEnabled is the "Watch my screen" switch: both screen keys on.
func (a *App) screenWatchingEnabled() bool {
	on, _ := a.screenCaptureState("screen_watching")
	return on
}

// startBrowserHistoryReader starts the loop once from startup.
func (a *App) startBrowserHistoryReader(ctx context.Context) {
	if a == nil {
		return
	}
	if !a.IsClosedEyesModeEnabled() {
		a.closeOpenObservationPause(time.Now())
	}
	r := newBrowserHistoryReader(a)
	if r.home == "" {
		return
	}
	r.prepareScratch()
	runCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	a.browserHistory = r
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.loop(runCtx)
	}()
}

// stopBrowserHistoryReader ends the loop at shutdown.
func (a *App) stopBrowserHistoryReader() {
	if a == nil || a.browserHistory == nil {
		return
	}
	r := a.browserHistory
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.wg.Wait()
}

// browserHistoryWatchingChanged runs when "Watch my screen" is switched: a
// switch back on starts reading from that moment, so nothing visited while it
// was off is ever read.
func (a *App) browserHistoryWatchingChanged(enabled bool) {
	if a == nil || a.browserHistory == nil || !enabled {
		return
	}
	a.browserHistory.setSince(time.Now().UnixMilli())
}

// browserHistoryProtectionChanged runs just before the user's protected
// apps change on the Privacy page: every browser protected until now starts
// reading from this moment, so nothing browsed while it was protected is read
// once it no longer is.
func (a *App) browserHistoryProtectionChanged() {
	if a == nil || a.browserHistory == nil {
		return
	}
	a.browserHistory.noteProtected(time.Now())
}

// noteProtected records now as the reading start of every browser the user
// protects at this moment.
func (r *browserHistoryReader) noteProtected(now time.Time) {
	if r.sensitive == nil {
		return
	}
	for _, browser := range browserhistory.Browsers {
		if r.sensitive(browser.BundleID, browser.Name) {
			r.markProtected(browser.Name, now)
		}
	}
}

func (r *browserHistoryReader) markProtected(browser string, now time.Time) {
	r.protectedMu.Lock()
	defer r.protectedMu.Unlock()
	if r.protectedAt == nil {
		r.protectedAt = map[string]int64{}
	}
	if ms := now.UnixMilli(); ms > r.protectedAt[browser] {
		r.protectedAt[browser] = ms
	}
}

// browserFloor is the earliest moment a read of this browser may reach: the
// global floor, or later when the browser was protected since.
func (r *browserHistoryReader) browserFloor(browser string, floor int64) int64 {
	r.protectedMu.Lock()
	defer r.protectedMu.Unlock()
	if at := r.protectedAt[browser]; at > floor {
		return at
	}
	return floor
}

// prepareScratch makes the private directory History is copied into and
// removes copies a crash left behind.
func (r *browserHistoryReader) prepareScratch() {
	if r.scratch == "" {
		return
	}
	if err := os.MkdirAll(r.scratch, 0o700); err != nil {
		r.scratch = ""
		return
	}
	entries, err := os.ReadDir(r.scratch)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "history-") {
			os.RemoveAll(filepath.Join(r.scratch, entry.Name()))
		}
	}
}

func (r *browserHistoryReader) loop(ctx context.Context) {
	timer := time.NewTimer(browserHistoryFirstRun)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if added, err := r.readOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[browser-history] read: %v", err)
		} else if added > 0 {
			log.Printf("[browser-history] %d new visits", added)
		}
		timer.Reset(browserHistoryInterval)
	}
}

// readOnce reads every profile whose History changed since its last read and
// prunes old visits. It returns how many visits were new.
func (r *browserHistoryReader) readOnce(ctx context.Context) (int, error) {
	store := r.store()
	if store == nil {
		return 0, nil
	}
	now := r.now()
	// A browser the user protects loses what was kept of it even while
	// reading is paused: its visits and its site summary.
	for _, browser := range browserhistory.Browsers {
		if r.sensitive != nil && r.sensitive(browser.BundleID, browser.Name) {
			_, _ = store.DeleteBrowserVisitsOf(browser.Name)
			_, _ = store.DeleteBrowserSiteDaysOf(browser.Name)
		}
	}
	on, known := r.allowed()
	if !known {
		r.unknownThrough = now.UnixMilli()
		return 0, nil
	}
	if !on {
		// Reading resumes from the moment it is allowed again.
		r.setSince(now.UnixMilli())
		return 0, nil
	}
	floor, ok := r.since()
	if !ok {
		// First run: start now, never reach back.
		floor = now.UnixMilli()
		r.setSince(floor)
	}
	// Keep the outage boundary in memory while preference writes may also fail.
	if r.unknownThrough > floor {
		floor = r.unknownThrough
	}
	// Nothing from before this launch: Isoform was not running then.
	if started := r.started.UnixMilli(); !r.started.IsZero() && started > floor {
		floor = started
	}
	added := 0
	var firstErr error
	for _, profile := range browserhistory.Profiles(r.home) {
		if ctx.Err() != nil {
			return added, ctx.Err()
		}
		if r.sensitive != nil && r.sensitive(profile.Browser.BundleID, profile.Browser.Name) {
			r.markProtected(profile.Browser.Name, now)
			if _, err := store.DeleteBrowserVisitsOf(profile.Browser.Name); err != nil && firstErr == nil {
				firstErr = err
			}
			if _, err := store.DeleteBrowserSiteDaysOf(profile.Browser.Name); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		changed, err := browserhistory.ModTime(profile.Path)
		if err != nil {
			continue
		}
		if last, ok := r.mtimes[profile.Path]; ok && !changed.After(last) {
			continue
		}
		n, complete, err := r.readProfile(ctx, store, profile, r.browserFloor(profile.Browser.Name, floor))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		added += n
		if complete {
			r.mtimes[profile.Path] = changed
		}
	}
	if _, err := store.PruneBrowserVisits(now.Add(-browserHistoryRetention).UnixMilli()); err != nil && firstErr == nil {
		firstErr = err
	}
	return added, firstErr
}

// readProfile reads one profile's new visits. complete is false when the
// read hit its limit and more remain.
func (r *browserHistoryReader) readProfile(ctx context.Context, store *ActivityStore, profile browserhistory.Profile, floor int64) (int, bool, error) {
	since := time.UnixMilli(floor)
	latest, err := store.LatestBrowserVisit(profile.Browser.Name, profile.Profile)
	if err != nil {
		return 0, false, err
	}
	if overlap := time.UnixMilli(latest).Add(-browserHistoryOverlap); latest > 0 && overlap.After(since) {
		since = overlap
	}
	var visits []browserhistory.Visit
	err = browserhistory.WithCopy(ctx, profile.Path, r.scratch, func(db *sql.DB) error {
		var err error
		visits, err = browserhistory.ReadVisits(ctx, db, since, browserHistoryReadLimit)
		return err
	})
	if err != nil {
		return 0, false, err
	}
	// Read the pause log after the copy, so a pause that began during it
	// still counts; a log that cannot be read stores nothing.
	pauses, err := r.pauses()
	if err != nil {
		return 0, false, err
	}
	n, err := store.InsertBrowserVisits(browserVisitRows(visits, profile, pauses))
	return n, len(visits) < browserHistoryReadLimit, err
}

// browserVisitRows keeps what is stored of each visit: web links only, none
// made while observing was paused, the link and title scrubbed.
func browserVisitRows(visits []browserhistory.Visit, profile browserhistory.Profile, pauses []observationPause) []BrowserVisitRow {
	rows := make([]BrowserVisitRow, 0, len(visits))
	for _, v := range visits {
		if browserhistory.Host(v.URL) == "" || pausedAt(pauses, v.At) {
			continue
		}
		link := pii.RedactURL(v.URL)
		if link == "" {
			continue
		}
		rows = append(rows, BrowserVisitRow{
			URL:           link,
			Title:         browserVisitTitle(v.Title),
			VisitedAt:     v.At.UnixMilli(),
			Browser:       profile.Browser.Name,
			Profile:       profile.Profile,
			SourceVisitID: v.ID,
		})
	}
	return rows
}

// browserVisitTitle redacts personal details and codes from a page title and
// bounds it.
func browserVisitTitle(title string) string {
	title = strings.Join(strings.Fields(strings.ToValidUTF8(title, "")), " ")
	if title == "" {
		return ""
	}
	title, _ = pii.RedactText(title)
	title = pii.RedactCodes(title)
	if utf8.RuneCountInString(title) > browserHistoryTitleMax {
		title = string([]rune(title)[:browserHistoryTitleMax])
	}
	return title
}
