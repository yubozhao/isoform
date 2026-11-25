package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/browserhistory"
)

// writeChromeHistory writes a Chromium-shaped History for Chrome's Default
// profile under home and returns its path.
func writeChromeHistory(t *testing.T, home string, visits []browserhistory.Visit) string {
	t.Helper()
	dir := filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "History")
	os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE urls (id INTEGER PRIMARY KEY, url LONGVARCHAR, title LONGVARCHAR, last_visit_time INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE visits (id INTEGER PRIMARY KEY, url INTEGER NOT NULL, visit_time INTEGER NOT NULL, transition INTEGER DEFAULT 0)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for i, v := range visits {
		res, err := db.Exec(`INSERT INTO urls (url, title) VALUES (?, ?)`, v.URL, v.Title)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := db.Exec(`INSERT INTO visits (id, url, visit_time) VALUES (?, ?, ?)`, i+1, id, browserhistory.ChromiumTime(v.At)); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// newTestHistoryReader reads as if reading had been allowed since two days
// ago; set since to nil for a first run.
func newTestHistoryReader(t *testing.T, home string, now time.Time) (*browserHistoryReader, *ActivityStore, *int64) {
	t.Helper()
	store := newTestStore(t)
	marker := now.Add(-48 * time.Hour).UnixMilli()
	r := &browserHistoryReader{
		home:      home,
		scratch:   t.TempDir(),
		now:       func() time.Time { return now },
		store:     func() *ActivityStore { return store },
		allowed:   func() (bool, bool) { return true, true },
		pauses:    func() ([]observationPause, error) { return nil, nil },
		sensitive: func(string, string) bool { return false },
		mtimes:    map[string]time.Time{},
	}
	r.since = func() (int64, bool) { return marker, marker != 0 }
	r.setSince = func(ms int64) { marker = ms }
	return r, store, &marker
}

func TestBrowserHistoryReaderStoresScrubbedVisitsOnce(t *testing.T) {
	home := t.TempDir()
	now := time.Now().Truncate(time.Second)
	writeChromeHistory(t, home, []browserhistory.Visit{
		{URL: "https://accounts.example.com/reset?token=abc123", Title: "Reset for ann@example.com", At: now.Add(-2 * time.Hour)},
		{URL: "https://github.com/example/isoform/pull/12", Title: "PR 12", At: now.Add(-time.Hour)},
		{URL: "chrome://settings", Title: "Settings", At: now.Add(-30 * time.Minute)},
		{URL: "https://old.example.com/", Title: "Too old", At: now.Add(-72 * time.Hour)},
	})
	r, store, _ := newTestHistoryReader(t, home, now)

	added, err := r.readOnce(context.Background())
	if err != nil || added != 2 {
		t.Fatalf("first read: %d %v", added, err)
	}
	rows, _ := store.ListBrowserVisits(0, now.Add(time.Hour).UnixMilli())
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].URL != "https://accounts.example.com/reset" || strings.Contains(rows[0].Title, "ann@example.com") {
		t.Fatalf("not scrubbed: %+v", rows[0])
	}
	if rows[1].Browser != "Chrome" || rows[1].Profile != "Default" || rows[1].URL != "https://github.com/example/isoform/pull/12" {
		t.Fatalf("row %+v", rows[1])
	}

	// Unchanged file: not read again.
	if added, _ := r.readOnce(context.Background()); added != 0 {
		t.Fatalf("unchanged read added %d", added)
	}

	// A changed file is read again, and nothing is stored twice.
	path := writeChromeHistory(t, home, []browserhistory.Visit{
		{URL: "https://github.com/example/isoform/pull/12", Title: "PR 12", At: now.Add(-time.Hour)},
		{URL: "https://news.ycombinator.com/", Title: "Hacker News", At: now.Add(-time.Minute)},
	})
	later := time.Now().Add(time.Minute)
	os.Chtimes(path, later, later)
	added, err = r.readOnce(context.Background())
	if err != nil || added != 1 {
		t.Fatalf("second read: %d %v", added, err)
	}
}

func TestBrowserHistoryReaderRespectsPausesAndProtectedBrowsers(t *testing.T) {
	home := t.TempDir()
	now := time.Now().Truncate(time.Second)
	writeChromeHistory(t, home, []browserhistory.Visit{
		{URL: "https://before.example.com/", Title: "Before", At: now.Add(-3 * time.Hour)},
		{URL: "https://during.example.com/", Title: "During the pause", At: now.Add(-2 * time.Hour)},
		{URL: "https://after.example.com/", Title: "After", At: now.Add(-time.Hour)},
	})
	r, store, marker := newTestHistoryReader(t, home, now)

	// A pause log that cannot be read stores nothing.
	r.pauses = func() ([]observationPause, error) { return nil, os.ErrPermission }
	if added, err := r.readOnce(context.Background()); added != 0 || err == nil {
		t.Fatalf("read with an unreadable pause log: %d %v", added, err)
	}
	// A visit made while observing was paused is never stored.
	r.pauses = func() ([]observationPause, error) {
		return []observationPause{{Start: now.Add(-150 * time.Minute).UnixMilli(), End: now.Add(-90 * time.Minute).UnixMilli()}}, nil
	}
	if added, err := r.readOnce(context.Background()); err != nil || added != 2 {
		t.Fatalf("read with a pause: %d %v", added, err)
	}
	rows, _ := store.ListBrowserVisits(0, now.UnixMilli())
	for _, row := range rows {
		if strings.Contains(row.URL, "during") {
			t.Fatalf("a visit during the pause was stored: %+v", row)
		}
	}
	// A browser the user protects is not read, and its visits are deleted.
	r.sensitive = func(bundleID, _ string) bool { return bundleID == "com.google.Chrome" }
	r.readOnce(context.Background())
	if rows, _ := store.ListBrowserVisits(0, now.UnixMilli()); len(rows) != 0 {
		t.Fatalf("a protected browser's visits were kept: %+v", rows)
	}
	// Watching off (or paused): nothing read, and reading later starts from
	// now, never reaching back into the time it was off.
	r.sensitive = func(string, string) bool { return false }
	r.allowed = func() (bool, bool) { return false, true }
	if added, _ := r.readOnce(context.Background()); added != 0 || *marker != now.UnixMilli() {
		t.Fatalf("read while not allowed, or the start did not move: %d %d", added, *marker)
	}
	r.allowed = func() (bool, bool) { return true, true }
	r.mtimes = map[string]time.Time{}
	if added, _ := r.readOnce(context.Background()); added != 0 {
		t.Fatalf("visits from before reading was allowed again were read: %d", added)
	}
}

func TestBrowserHistoryFirstRunNeverReachesBack(t *testing.T) {
	home := t.TempDir()
	now := time.Now().Truncate(time.Second)
	writeChromeHistory(t, home, []browserhistory.Visit{
		{URL: "https://before.example.com/", Title: "Before Isoform read anything", At: now.Add(-time.Hour)},
	})
	r, _, marker := newTestHistoryReader(t, home, now)
	*marker = 0
	if added, err := r.readOnce(context.Background()); err != nil || added != 0 {
		t.Fatalf("first run read back: %d %v", added, err)
	}
	if *marker != now.UnixMilli() {
		t.Fatalf("first run did not record its start: %d", *marker)
	}
}

func TestBrowserHistoryNeverReadsTheTimeIsoformWasNotRunning(t *testing.T) {
	home := t.TempDir()
	now := time.Now().Truncate(time.Second)
	// Quit on Friday with the newest stored visit at 17:55, relaunched on
	// Sunday: the weekend is not read, however close it is to that visit.
	writeChromeHistory(t, home, []browserhistory.Visit{
		{URL: "https://friday.example.com/", Title: "Friday", At: now.Add(-48 * time.Hour)},
		{URL: "https://weekend.example.com/", Title: "Weekend", At: now.Add(-46 * time.Hour)},
		{URL: "https://sunday.example.com/", Title: "Sunday, before launch", At: now.Add(-time.Hour)},
		{URL: "https://after.example.com/", Title: "After launch", At: now.Add(-10 * time.Minute)},
	})
	r, store, marker := newTestHistoryReader(t, home, now)
	*marker = now.Add(-72 * time.Hour).UnixMilli()
	r.started = now.Add(-30 * time.Minute)
	if _, err := store.InsertBrowserVisits([]BrowserVisitRow{{
		URL: "https://friday.example.com/", VisitedAt: now.Add(-48 * time.Hour).UnixMilli(),
		Browser: "Chrome", Profile: "Default", SourceVisitID: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if added, err := r.readOnce(context.Background()); err != nil || added != 1 {
		t.Fatalf("read after launch: %d %v", added, err)
	}
	rows, _ := store.ListBrowserVisits(now.Add(-47*time.Hour).UnixMilli(), now.UnixMilli())
	if len(rows) != 1 || !strings.Contains(rows[0].URL, "after.example.com") {
		t.Fatalf("visits from before the launch were read: %+v", rows)
	}
}

func TestBrowserHistoryNeverReadsTheTimeABrowserWasProtected(t *testing.T) {
	home := t.TempDir()
	now := time.Now().Truncate(time.Second)
	path := writeChromeHistory(t, home, []browserhistory.Visit{
		{URL: "https://protected.example.com/", Title: "While protected", At: now.Add(-time.Hour)},
	})
	r, store, _ := newTestHistoryReader(t, home, now)

	// Protected at a read: skipped, and its reading moves to that read.
	protected := true
	r.sensitive = func(bundleID, _ string) bool { return protected && bundleID == "com.google.Chrome" }
	if added, err := r.readOnce(context.Background()); err != nil || added != 0 {
		t.Fatalf("read while protected: %d %v", added, err)
	}
	protected = false
	if added, err := r.readOnce(context.Background()); err != nil || added != 0 {
		t.Fatalf("un-protecting read the protected time back: %d %v", added, err)
	}

	// Protected and un-protected between two reads: the Privacy page change
	// moves the reading start too.
	later := now.Add(time.Hour)
	r.now = func() time.Time { return later }
	writeChromeHistory(t, home, []browserhistory.Visit{
		{URL: "https://protected.example.com/", Title: "While protected", At: now.Add(-time.Hour)},
		{URL: "https://between.example.com/", Title: "Protected between reads", At: now.Add(30 * time.Minute)},
		{URL: "https://after.example.com/", Title: "After", At: now.Add(50 * time.Minute)},
	})
	touched := time.Now().Add(time.Minute)
	os.Chtimes(path, touched, touched)
	protected = true
	r.noteProtected(now.Add(40 * time.Minute)) // RemoveSensitiveApp, just before the removal
	protected = false
	if added, err := r.readOnce(context.Background()); err != nil || added != 1 {
		t.Fatalf("read after un-protecting: %d %v", added, err)
	}
	rows, _ := store.ListBrowserVisits(0, later.UnixMilli())
	if len(rows) != 1 || !strings.Contains(rows[0].URL, "after.example.com") {
		t.Fatalf("visits made while protected were read: %+v", rows)
	}
}

func TestObservationPauseLog(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	var pauses []observationPause
	pauses = nextObservationPauses(pauses, true, t0)
	pauses = nextObservationPauses(pauses, true, t0.Add(time.Minute)) // already paused
	if len(pauses) != 1 || pauses[0].End != 0 {
		t.Fatalf("open pause %+v", pauses)
	}
	if !pausedAt(pauses, t0.Add(time.Hour)) {
		t.Fatal("an open pause covers now")
	}
	pauses = nextObservationPauses(pauses, false, t0.Add(10*time.Minute))
	if pausedAt(pauses, t0.Add(10*time.Minute)) || !pausedAt(pauses, t0.Add(5*time.Minute)) || pausedAt(pauses, t0.Add(-time.Second)) {
		t.Fatalf("closed pause %+v", pauses)
	}
	// Closed pauses expire only beyond the full reader coverage window.
	later := t0.Add(observationPausesKept + 24*time.Hour)
	pauses = nextObservationPauses(pauses, true, later)
	if len(pauses) != 1 || pauses[0].Start != later.UnixMilli() {
		t.Fatalf("old pauses kept %+v", pauses)
	}
}

func TestMobileBriefWatchingCandidates(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	rows := []BrowserVisitRow{
		{URL: "https://www.google.com/search", Title: "eventkit reminders - Google Search", VisitedAt: at.UnixMilli()},
		{URL: "https://developer.apple.com/documentation/eventkit", Title: "EventKit", VisitedAt: at.Add(time.Minute).UnixMilli()},
		{URL: "https://github.com/a", Title: "A", VisitedAt: at.Add(time.Hour).UnixMilli()},
		{URL: "https://github.com/b", Title: "B", VisitedAt: at.Add(2 * time.Hour).UnixMilli()},
	}
	got := mobileBriefWatchingCandidates(rows)
	var site, research *mobileBriefCandidate
	for i := range got {
		switch got[i].Kind {
		case "site":
			if got[i].Title == "github.com" {
				site = &got[i]
			}
		case "research":
			research = &got[i]
		}
	}
	if site == nil || site.Detail != "2 visits" || site.SourceKind != "web" {
		t.Fatalf("site %+v in %+v", site, got)
	}
	if research == nil || research.Title != "eventkit reminders" || research.Detail != "Searched, then read 1 page" {
		t.Fatalf("research %+v", research)
	}
	brief := buildMobileBriefV1(mobileBriefInput{
		Slot: mobileBriefSlotEvening, WindowStart: at.Add(-time.Hour), AsOf: at.Add(3 * time.Hour),
		Location: time.UTC, Watching: got,
	})
	if len(brief.Sections.Watching) != len(got) {
		t.Fatalf("watching section %+v", brief.Sections.Watching)
	}
	if mobileBriefWatchingCandidates(nil) != nil {
		t.Fatal("no visits, no candidates")
	}
}
