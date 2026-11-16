package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// migrateBrowserVisits adds browser_visits: the page visits read from the
// Chromium History databases (browser_history_sync.go). A visit is keyed by
// its browser, profile, time and stored link, so a read that overlaps the
// last one adds nothing twice, even after History renumbered its visits.
func migrateBrowserVisits(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS browser_visits (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			url             TEXT    NOT NULL,
			title           TEXT    NOT NULL DEFAULT '',
			visited_at      INTEGER NOT NULL,
			browser         TEXT    NOT NULL,
			profile         TEXT    NOT NULL,
			source_visit_id INTEGER NOT NULL,
			UNIQUE (browser, profile, visited_at, url)
		);
		CREATE INDEX IF NOT EXISTS idx_browser_visits_visited ON browser_visits(visited_at);
	`)
	return err
}

// BrowserVisitRow is one stored page visit. VisitedAt is Unix milliseconds;
// URL and Title are already scrubbed.
type BrowserVisitRow struct {
	URL           string
	Title         string
	VisitedAt     int64
	Browser       string
	Profile       string
	SourceVisitID int64
}

// InsertBrowserVisits stores visits, skipping the ones already stored. It
// returns how many were new.
func (s *ActivityStore) InsertBrowserVisits(rows []BrowserVisitRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin browser visits: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO browser_visits (url, title, visited_at, browser, profile, source_visit_id)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare browser visits: %w", err)
	}
	defer stmt.Close()
	added := 0
	for _, row := range rows {
		if strings.TrimSpace(row.URL) == "" {
			continue
		}
		res, err := stmt.Exec(row.URL, row.Title, row.VisitedAt, row.Browser, row.Profile, row.SourceVisitID)
		if err != nil {
			return 0, fmt.Errorf("insert browser visit: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	return added, tx.Commit()
}

// LatestBrowserVisit is the time of the newest stored visit of one browser
// profile, or 0.
func (s *ActivityStore) LatestBrowserVisit(browser, profile string) (int64, error) {
	var latest sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(visited_at) FROM browser_visits WHERE browser = ? AND profile = ?`, browser, profile).Scan(&latest)
	if err != nil {
		return 0, fmt.Errorf("latest browser visit: %w", err)
	}
	return latest.Int64, nil
}

// ListBrowserVisits returns the visits in [from, to), oldest first.
func (s *ActivityStore) ListBrowserVisits(from, to int64) ([]BrowserVisitRow, error) {
	return s.ListBrowserVisitsContext(context.Background(), from, to)
}

func (s *ActivityStore) ListBrowserVisitsContext(ctx context.Context, from, to int64) ([]BrowserVisitRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT url, title, visited_at, browser, profile, source_visit_id
		FROM browser_visits WHERE visited_at >= ? AND visited_at < ?
		ORDER BY visited_at ASC, id ASC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list browser visits: %w", err)
	}
	defer rows.Close()
	var out []BrowserVisitRow
	for rows.Next() {
		var row BrowserVisitRow
		if err := rows.Scan(&row.URL, &row.Title, &row.VisitedAt, &row.Browser, &row.Profile, &row.SourceVisitID); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// PruneBrowserVisits deletes the visits before the cutoff.
func (s *ActivityStore) PruneBrowserVisits(before int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM browser_visits WHERE visited_at < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("prune browser visits: %w", err)
	}
	return res.RowsAffected()
}

// DeleteBrowserVisitsOf deletes every stored visit of one browser, for a
// browser the user protects.
func (s *ActivityStore) DeleteBrowserVisitsOf(browser string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM browser_visits WHERE browser = ?`, browser)
	if err != nil {
		return 0, fmt.Errorf("delete browser visits: %w", err)
	}
	return res.RowsAffected()
}

// DeleteAllBrowserActivity removes every stored visit and site day of every
// browser (Your data's Forget everything).
func (s *ActivityStore) DeleteAllBrowserActivity() error {
	for _, query := range []string{`DELETE FROM browser_visits`, `DELETE FROM browser_site_days`} {
		if _, err := s.db.Exec(query); err != nil {
			return fmt.Errorf("delete browser activity: %w", err)
		}
	}
	return nil
}
