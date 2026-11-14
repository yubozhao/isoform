package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ──────────────────────────────────────────────
// Data types
// ──────────────────────────────────────────────

// DailySummaryData contains all data the frontend needs to render the summary view.
type DailySummaryData struct {
	modelSources      *activitySummarySourceSet
	modelSessions     []ActivitySessionRow
	modelSourceIDs    map[string]bool
	Date              string         `json:"date"`              // "2006-01-02"
	AISummary         string         `json:"aiSummary"`         // AI-generated natural language summary
	AISummaryStatus   string         `json:"aiSummaryStatus"`   // "none"|"generating"|"done"|"error"
	AISummaryLanguage string         `json:"aiSummaryLanguage"` // normalized language of the cached row ("", "en", "zh-CN", ...); used to show a hint when it differs from the current UI language
	IsPartial         bool           `json:"isPartial"`         // true if generated while day was still in progress
	SummaryCreatedAt  int64          `json:"summaryCreatedAt"`  // unix ms when the cached AI summary was generated
	AutoStatus        string         `json:"autoStatus"`        // "ready"|"refreshing"|"unavailable"; set only by EnsureDailySummary
	TotalActiveMs     int64          `json:"totalActiveMs"`
	ActiveAvgMs       int64          `json:"activeAvgMs"` // 7-day daily average
	SessionCount      int            `json:"sessionCount"`
	SessionAvg        float64        `json:"sessionAvg"` // 7-day daily average
	UniqueApps        int            `json:"uniqueApps"`
	UniqueAppsAvg     float64        `json:"uniqueAppsAvg"` // 7-day daily average
	SnapshotCount     int            `json:"snapshotCount"`
	OCRPageCount      int            `json:"ocrPageCount"`
	HourlyActive      [24]int        `json:"hourlyActive"` // minutes active per hour
	AppBreakdown      []AppUsageStat `json:"appBreakdown"`
	Moments           []MomentData   `json:"moments"` // session groups as narrative cards (Rhythm / Moments tab)
	TopOCRKeywords    []string       `json:"topOcrKeywords"`
	TopWindowTitles   []string       `json:"topWindowTitles"` // most frequent window titles (for AI context)
}

// MomentData is a narrative-friendly session group rendered in the daily
// Summary "Moments" tab. Each moment is a merged cluster of adjacent activity
// sessions (gap ≤ triagerMergeGap + shared app). Shares boundaries with
// the triager pipeline so the Summary view and the Memory view stay aligned.
type MomentData struct {
	StartMs     int64    `json:"startMs"`
	EndMs       int64    `json:"endMs"`
	SessionIDs  []string `json:"sessionIds,omitempty"` // source activity sessions for drill-down
	Apps        []string `json:"apps"`                 // in first-seen order
	Title       string   `json:"title"`                // descriptive title (triage-derived, otherwise fallback main app)
	Subtitle    string   `json:"subtitle,omitempty"`
	Description string   `json:"description,omitempty"` // concise markdown narrative for the moment
}

// WeeklySummaryData contains data for the weekly summary view.
type WeeklySummaryData struct {
	WeekStart         string         `json:"weekStart"` // Monday date
	WeekEnd           string         `json:"weekEnd"`   // Sunday date
	AISummary         string         `json:"aiSummary"`
	AISummaryStatus   string         `json:"aiSummaryStatus"`
	AISummaryLanguage string         `json:"aiSummaryLanguage"` // see DailySummaryData.AISummaryLanguage
	AutoStatus        string         `json:"autoStatus"`        // "ready"|"refreshing"|"unavailable"; set only by EnsureWeeklySummary
	DailyStats        []DayStatBrief `json:"dailyStats"`
	TotalActiveMs     int64          `json:"totalActiveMs"`
	DailyAvgMs        int64          `json:"dailyAvgMs"`
	PrevWeekMs        int64          `json:"prevWeekMs"`
	TopApps           []AppUsageStat `json:"topApps"`
	dailySummaries    []string       // cached daily AI summaries (not exported, only for prompt building)
}

type DayStatBrief struct {
	Date         string `json:"date"`
	Weekday      string `json:"weekday"`
	ActiveMs     int64  `json:"activeMs"`
	SessionCount int    `json:"sessionCount"`
}

type AppUsageStat struct {
	AppName    string  `json:"appName"`
	BundleID   string  `json:"bundleId,omitempty"`
	ActiveMs   int64   `json:"activeMs"`
	Percentage float64 `json:"percentage"`
}

// GetCachedSummary retrieves a cached AI summary for a given date and type.
func (s *ActivityStore) GetCachedSummary(date, summaryType string) (summary string, isPartial bool, createdAt int64, found bool) {
	var partial int
	err := s.reader().QueryRow(
		`SELECT summary, is_partial, created_at FROM activity_summaries WHERE date = ? AND type = ? AND language = ''`,
		date, summaryType,
	).Scan(&summary, &partial, &createdAt)
	if err != nil {
		return "", false, 0, false
	}
	return summary, partial == 1, createdAt, true
}

// GetCachedSummaryForLanguage retrieves the best cached AI summary for a date,
// preferring the requested UI language but falling back to any existing row in
// any other language (including legacy rows written before the language column
// existed). The fallback is intentional: switching UI language must not
// silently regenerate historical summaries — that would burn LLM quota without
// consent and discard the original wording. Users who want a localized rewrite
// can hit Regenerate manually, which writes a new (date, type, language) row
// without touching the existing ones.
//
// Priority order: exact language match → legacy language=” → newest other
// language by created_at. cachedLanguage reflects the row that actually
// matched so callers can show "Generated in <lang>" hints when the row is a
// cross-language fallback.
func (s *ActivityStore) GetCachedSummaryForLanguage(date, summaryType, language string) (summary string, isPartial bool, createdAt int64, cachedLanguage string, found bool) {
	var partial int
	language = normalizeSummaryLanguage(language)
	err := s.reader().QueryRow(
		`SELECT summary, is_partial, created_at, language FROM activity_summaries
		 WHERE date = ? AND type = ?
		 ORDER BY language = ? DESC, language = '' DESC, created_at DESC
		 LIMIT 1`,
		date, summaryType, language,
	).Scan(&summary, &partial, &createdAt, &cachedLanguage)
	if err != nil {
		return "", false, 0, "", false
	}
	return summary, partial == 1, createdAt, cachedLanguage, true
}

// SaveSummary upserts an AI summary. Always resets moments_partial to 1 — the
// Moments pipeline runs in parallel and only flips moments_partial back to 0
// once persistMoments writes a non-empty payload via SaveMoments. This pairing
// ensures that if the Moments agent silently fails (returns empty), the row
// stays flagged for retry on the next checkPendingSummaries pass.
//
// Each upsert also bumps moments_generation by 1 and returns the new value,
// so a caller about to start a Moments goroutine can pin the gen and reject
// late writes from an earlier in-flight attempt — see SaveMoments and
// waitForMomentsPersisted for the matching halves.
func (s *ActivityStore) SaveSummary(date, summaryType, summary, model string, isPartial bool) (int, error) {
	return s.SaveSummaryWithLanguage(date, summaryType, summary, model, isPartial, "")
}

// SaveSummaryWithLanguage upserts an AI summary and records the UI language it
// was generated for. See SaveSummary for the moments lifecycle contract.
func (s *ActivityStore) SaveSummaryWithLanguage(date, summaryType, summary, model string, isPartial bool, language string) (int, error) {
	start, end, err := activitySummaryBounds(date, summaryType)
	if err != nil {
		return 0, err
	}
	sources, err := s.summarySources(start, end)
	if err != nil {
		return 0, err
	}
	return s.saveSummaryWithSources(date, summaryType, summary, model, isPartial, language, sources)
}

func (s *ActivityStore) saveSummaryWithSources(date, summaryType, summary, model string, isPartial bool, language string, sources []activitySourceProvenance) (int, error) {
	provenance := ""
	if len(sources) > 0 {
		raw, err := json.Marshal(sources)
		if err != nil {
			return 0, err
		}
		provenance = string(raw)
	}
	partial := 0
	if isPartial {
		partial = 1
	}
	language = normalizeSummaryLanguage(language)
	var gen int
	err := s.db.QueryRow(`
		INSERT INTO activity_summaries (date, type, summary, model, is_partial, language, moments_partial, moments_generation, created_at, source_provenance)
		VALUES (?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
		ON CONFLICT(date, type, language) DO UPDATE SET
			summary = excluded.summary,
			model = excluded.model,
			is_partial = excluded.is_partial,
			language = excluded.language,
			moments_partial = 1,
			moments_generation = activity_summaries.moments_generation + 1,
			created_at = excluded.created_at,
            source_provenance = excluded.source_provenance
		RETURNING moments_generation`,
		date, summaryType, summary, model, partial, language, time.Now().UnixMilli(), provenance,
	).Scan(&gen)
	if err != nil {
		return 0, err
	}
	return gen, nil
}

func normalizeSummaryLanguage(language string) string {
	lower := strings.ToLower(strings.TrimSpace(language))
	switch {
	case lower == "":
		return ""
	case strings.HasPrefix(lower, "zh") || strings.Contains(lower, "chinese"):
		return "zh-CN"
	case strings.HasPrefix(lower, "en") || strings.Contains(lower, "english"):
		return "en"
	default:
		return lower
	}
}

// GetCachedMoments returns the persisted Moments JSON for a daily summary, if
// any. Empty string means "never generated". Only daily summaries carry
// moments.
func (s *ActivityStore) GetCachedMoments(date string) string {
	var momentsJSON string
	err := s.reader().QueryRow(
		`SELECT moments_json FROM activity_summaries WHERE date = ? AND type = 'daily' AND language = ''`,
		date,
	).Scan(&momentsJSON)
	if err != nil {
		return ""
	}
	return momentsJSON
}

// GetCachedMomentsForLanguage returns the best cached Moments payload for a
// date, preferring the row whose language matches the requested UI language
// and falling back to any other language (legacy ” included). Same rationale
// as GetCachedSummaryForLanguage: switching UI language must not silently
// regenerate historical Moments.
func (s *ActivityStore) GetCachedMomentsForLanguage(date, language string) string {
	var momentsJSON string
	language = normalizeSummaryLanguage(language)
	err := s.reader().QueryRow(
		`SELECT moments_json FROM activity_summaries
		 WHERE date = ? AND type = 'daily'
		 ORDER BY language = ? DESC, language = '' DESC, created_at DESC
		 LIMIT 1`,
		date, language,
	).Scan(&momentsJSON)
	if err != nil {
		return ""
	}
	return momentsJSON
}

// SaveMoments updates the Moments payload for an existing daily summary row.
// Saving a non-empty payload also flips moments_partial=0 to signal the row
// no longer needs retrying. Saving an empty string is treated as a stale-clear
// (used right after SaveSummary to drop the prior run's moments) and keeps
// moments_partial=1 so checkPendingSummaries can still pick the row up if the
// follow-up persistMoments never lands a non-empty payload.
//
// generation gates the write: only rows whose moments_generation matches will
// be updated, so a late-arriving persistMoments goroutine from a previous
// attempt cannot poison the current row. Pass generation=0 to bypass the
// gate (used by the post-SaveSummary stale-clear which intentionally always
// applies, and by callers that don't run a moments pipeline).
//
// No-ops if the row doesn't exist yet or the generation doesn't match; the
// caller is expected to SaveSummary first so the lifecycle stays simple
// (one row per (date, type='daily')).
func (s *ActivityStore) SaveMoments(date, momentsJSON string, generation int) error {
	return s.SaveMomentsWithLanguage(date, momentsJSON, generation, "")
}

// SaveMomentsWithLanguage updates the Moments payload for the summary row in
// the same language cache slot as its owning daily summary.
func (s *ActivityStore) SaveMomentsWithLanguage(date, momentsJSON string, generation int, language string) error {
	language = normalizeSummaryLanguage(language)
	if momentsJSON == "" {
		// Empty-clear is generation-agnostic — it's only ever called by the
		// runGenerateDailySummary path right after its own SaveSummary, and it
		// must always run regardless of which gen owns the row.
		_, err := s.db.Exec(
			`UPDATE activity_summaries SET moments_json = '' WHERE date = ? AND type = 'daily' AND language = ?`,
			date, language,
		)
		return err
	}
	if generation == 0 {
		_, err := s.db.Exec(
			`UPDATE activity_summaries SET moments_json = ?, moments_partial = 0 WHERE date = ? AND type = 'daily' AND language = ?`,
			momentsJSON, date, language,
		)
		return err
	}
	_, err := s.db.Exec(
		`UPDATE activity_summaries SET moments_json = ?, moments_partial = 0 WHERE date = ? AND type = 'daily' AND language = ? AND moments_generation = ?`,
		momentsJSON, date, language, generation,
	)
	return err
}

// GetMomentsPartial reads the moments_partial flag and moments_generation for
// the daily row. Returns ok=false if the row is missing or the query fails —
// callers polling for "moments landed" should treat that as "still pending"
// so a missing row can't be misread as success.
func (s *ActivityStore) GetMomentsPartial(date string) (partial, generation int, ok bool) {
	return s.GetMomentsPartialForLanguage(date, "")
}

func (s *ActivityStore) GetMomentsPartialForLanguage(date, language string) (partial, generation int, ok bool) {
	language = normalizeSummaryLanguage(language)
	err := s.reader().QueryRow(
		`SELECT moments_partial, moments_generation FROM activity_summaries WHERE date = ? AND type = 'daily' AND language = ?`,
		date, language,
	).Scan(&partial, &generation)
	if err != nil {
		return 1, 0, false
	}
	return partial, generation, true
}

// ListPendingSummaryDates returns dates within the last `daysBack` days that
// need (re)generation: either no daily summary at all, or is_partial=1, or
// moments_partial=1. Today is always excluded — its summary is expected to be
// partial during the day. Dates without any activity sessions are skipped.
//
// Used by checkPendingSummaries on app startup so we catch every stale row,
// not just yesterday's. This is what makes the system self-heal after the
// Moments agent silently fails on a previous run.
func (s *ActivityStore) ListPendingSummaryDates(daysBack int) []string {
	return s.ListPendingSummaryDatesForLanguage(daysBack, "")
}

// ListPendingSummaryDatesForLanguage returns daily summary dates that need
// generation. A date is pending only when no row exists in ANY language, the
// best-available row is still is_partial=1, or its Moments payload is partial.
// UI-language mismatch is intentionally NOT a pending trigger: a user
// switching language must not silently re-spend LLM quota on historical days
// — any existing row (legacy ” or another locale) already carries the day's
// record, and GetCachedSummaryForLanguage will fall back to it. Users can
// still hit Regenerate manually for a localized rewrite.
//
// The language argument is kept so we prefer the requested-language row when
// multiple coexist (so its is_partial / moments_partial flags drive the
// decision), but absence of a requested-language row is no longer pending if
// another language's row is final.
func (s *ActivityStore) ListPendingSummaryDatesForLanguage(daysBack int, language string) []string {
	if daysBack <= 0 {
		daysBack = 7
	}
	now := time.Now()
	today := now.Format("2006-01-02")
	language = normalizeSummaryLanguage(language)
	var pending []string
	for i := 1; i <= daysBack; i++ {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		if d == today {
			continue
		}
		if !s.HasActivityData(d) {
			continue
		}
		var isPartial, momentsPartial int
		err := s.reader().QueryRow(
			`SELECT is_partial, moments_partial FROM activity_summaries
			 WHERE date = ? AND type = 'daily'
			 ORDER BY language = ? DESC, language = '' DESC, created_at DESC
			 LIMIT 1`,
			d, language,
		).Scan(&isPartial, &momentsPartial)
		if err != nil {
			// No row in any language → never generated. Pending.
			pending = append(pending, d)
			continue
		}
		if isPartial == 1 || momentsPartial == 1 {
			pending = append(pending, d)
		}
	}
	return pending
}

// HasActivityData checks if any sessions exist for a given date.
func (s *ActivityStore) HasActivityData(date string) bool {
	t, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return false
	}
	startMs := t.UnixMilli()
	endMs := t.AddDate(0, 0, 1).UnixMilli()

	var count int
	err = s.reader().QueryRow(
		`SELECT COUNT(*) FROM activity_sessions WHERE start_time >= ? AND start_time < ?`,
		startMs, endMs,
	).Scan(&count)
	return err == nil && count > 0
}

// ──────────────────────────────────────────────
// Data aggregation queries
// ──────────────────────────────────────────────

// GetDailySummary computes all statistics for a given date and returns cached AI summary if available.
func (a *App) GetDailySummary(dateStr string) (perfValue *DailySummaryData, perfErr error) {
	defer logPageBinding("GetDailySummary", time.Now(), &perfValue)
	data, err := a.getDailySummary(dateStr, nil)
	a.secondPersonDaily(data)
	return data, err
}

func (a *App) getDailySummary(dateStr string, sources *activitySummarySourceSet) (*DailySummaryData, error) {
	am := a.activity
	if am == nil || am.store == nil {
		return nil, fmt.Errorf("activity monitor not initialized")
	}
	store := am.store

	t, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	if err != nil {
		return nil, fmt.Errorf("invalid date: %w", err)
	}

	startMs := t.UnixMilli()
	endMs := t.AddDate(0, 0, 1).UnixMilli()

	data := &DailySummaryData{Date: dateStr, modelSources: sources, modelSourceIDs: make(map[string]bool)}

	// 1. Session stats
	sessions, err := store.modelSessionsOverlapping(startMs, endMs)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}

	if sources != nil {
		captured := make(map[string]bool)
		for _, source := range sources.Sources {
			captured[source.SessionID] = true
		}
		filtered := sessions[:0]
		for _, session := range sessions {
			if captured[session.ID] {
				filtered = append(filtered, session)
			}
		}
		sessions = filtered
	}
	sessions = a.filterActivityModelSessions(store, sessions)
	data.modelSessions = sessions
	for _, session := range sessions {
		data.modelSourceIDs[session.ID] = true
	}
	data.SessionCount = len(sessions)
	appSet := make(map[string]bool)
	for _, s := range sessions {
		sessionStart, sessionEnd := max(s.StartTime, startMs), min(s.EndTime, endMs)
		dur := sessionEnd - sessionStart
		data.SnapshotCount += s.SnapshotCount

		// Parse app names
		var names []string
		if err := json.Unmarshal([]byte(s.AppNames), &names); err == nil {
			for _, n := range names {
				appSet[n] = true
			}
		}

		// Hourly distribution (use local time so chart matches user's timezone)
		startHour := time.UnixMilli(sessionStart).Local().Hour()
		endHour := time.UnixMilli(sessionEnd - 1).Local().Hour()
		// Cap to current day when session crosses midnight (e.g. 23:50-00:10)
		if endHour < startHour {
			endHour = 23
		}
		durMin := int(dur / 60000)
		if durMin < 1 {
			durMin = 1
		}
		if startHour == endHour {
			data.HourlyActive[startHour] += durMin
		} else {
			// Distribute across hours proportionally
			hours := endHour - startHour
			perHour := durMin / (hours + 1)
			if perHour < 1 {
				perHour = 1
			}
			for h := startHour; h <= endHour; h++ {
				data.HourlyActive[h] += perHour
			}
		}
	}
	data.UniqueApps = len(appSet)

	// The day line is present time: the session spans above plus the short
	// between-session stretches the person read or listened through
	// (activity_present_time.go). The hourly chart and app breakdown stay
	// session-based; only this number changes.
	presentMs, _, err := a.activityPresentTimeMs(context.Background(), store, sessions, startMs, endMs)
	if err != nil {
		return nil, fmt.Errorf("present time: %w", err)
	}
	data.TotalActiveMs = presentMs

	// Cap hourly values at 60
	for i := range data.HourlyActive {
		if data.HourlyActive[i] > 60 {
			data.HourlyActive[i] = 60
		}
	}

	// 2. App breakdown from events
	data.AppBreakdown = a.computeAppBreakdown(store, startMs, endMs, data.TotalActiveMs, data.modelSourceIDs)

	// 2b. Moments — only cached agent-produced groupings are shown in the
	// digest UI. If missing, the frontend shows an explicit generate state.
	cached, cachedFound := a.readableActivitySummarySnapshot(dateStr, "daily", a.currentUILanguage())
	if cachedFound {
		data.Moments = decodeCachedActivityMoments(cached.Moments)
	}

	// 3. OCR page count
	var ocrCount int
	store.reader().QueryRow(
		`SELECT COUNT(*) FROM activity_ocr_frames f
		 JOIN activity_sessions s ON f.session_id = s.id
		 WHERE s.end_time >= ? AND s.start_time < ? AND f.text != ''`,
		startMs, endMs,
	).Scan(&ocrCount)
	data.OCRPageCount = ocrCount

	// 5. Top OCR keywords
	data.TopOCRKeywords = a.extractTopOCRKeywords(store, startMs, endMs, data.modelSourceIDs)

	// 5b. Top window titles (for AI prompt context)
	data.TopWindowTitles = a.extractTopWindowTitles(store, startMs, endMs, data.modelSourceIDs)

	// 6. 7-day averages
	avgStart := t.AddDate(0, 0, -7).UnixMilli()
	if err := a.compute7DayAverages(store, avgStart, startMs, data); err != nil {
		return nil, fmt.Errorf("7-day averages: %w", err)
	}

	// 7. Cached AI summary
	if cachedFound {
		data.AISummary = cached.Summary
		data.AISummaryStatus = "done"
		data.IsPartial = cached.IsPartial
		data.SummaryCreatedAt = cached.CreatedAt
		data.AISummaryLanguage = cached.Language
	} else {
		data.AISummaryStatus = "none"
	}

	return data, nil
}

// GetWeeklySummary computes weekly statistics for the week containing dateStr.
func (a *App) GetWeeklySummary(dateStr string) (perfValue *WeeklySummaryData, perfErr error) {
	defer logPageBinding("GetWeeklySummary", time.Now(), &perfValue)
	data, err := a.getWeeklySummary(dateStr, nil)
	if data != nil {
		data.AISummary = secondPersonLine(data.AISummary, a.activitySelfNames())
	}
	return data, err
}

func (a *App) getWeeklySummary(dateStr string, sources *activitySummarySourceSet) (*WeeklySummaryData, error) {
	am := a.activity
	if am == nil || am.store == nil {
		return nil, fmt.Errorf("activity monitor not initialized")
	}
	store := am.store

	t, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	if err != nil {
		return nil, fmt.Errorf("invalid date: %w", err)
	}

	// Find Monday of this week
	weekday := t.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	monday := t.AddDate(0, 0, -int(weekday-time.Monday))
	sunday := monday.AddDate(0, 0, 6)

	data := &WeeklySummaryData{
		WeekStart: monday.Format("2006-01-02"),
		WeekEnd:   sunday.Format("2006-01-02"),
	}

	// Daily stats for each day of the week
	var sourceIDs []map[string]bool
	if sources != nil {
		ids := make(map[string]bool)
		for _, source := range sources.Sources {
			ids[source.SessionID] = true
		}
		sourceIDs = append(sourceIDs, ids)
	}
	weekdays := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	for i := 0; i < 7; i++ {
		day := monday.AddDate(0, 0, i)
		stats, _, err := a.activitySummaryDayStats(store, day, sourceIDs...)
		if err != nil {
			return nil, fmt.Errorf("stats for %s: %w", day.Format("2006-01-02"), err)
		}
		stats.Weekday = weekdays[i]
		data.DailyStats = append(data.DailyStats, stats)
		data.TotalActiveMs += stats.ActiveMs
	}

	activeDays := 0
	for _, d := range data.DailyStats {
		if d.ActiveMs > 0 {
			activeDays++
		}
	}
	if activeDays > 0 {
		data.DailyAvgMs = data.TotalActiveMs / int64(activeDays)
	}

	// Previous week total
	prevMonday := monday.AddDate(0, 0, -7)
	for i := 0; i < 7; i++ {
		stats, _, err := a.activitySummaryDayStats(store, prevMonday.AddDate(0, 0, i))
		if err != nil {
			return nil, fmt.Errorf("previous week: %w", err)
		}
		data.PrevWeekMs += stats.ActiveMs
	}

	// Top apps for the week
	weekStart := monday.UnixMilli()
	// AddDate keeps the week boundary at local midnight across DST changes.
	weekEnd := sunday.AddDate(0, 0, 1).UnixMilli()
	// Captured generation IDs are a cohort, not a current privacy decision.
	// Recheck once before reusing this operation's allow set in aggregation.
	allowed := a.activityModelSessionIDs(store, weekStart, weekEnd)
	if len(sourceIDs) > 0 {
		for id := range allowed {
			if !sourceIDs[0][id] {
				delete(allowed, id)
			}
		}
	}
	data.TopApps = a.computeAppBreakdown(store, weekStart, weekEnd, data.TotalActiveMs, allowed)

	if sources != nil {
		data.dailySummaries = sources.DailySummaries
	} else {
		// Collect cached daily summaries for richer weekly prompt
		for i := 0; i < 7; i++ {
			day := monday.AddDate(0, 0, i)
			dayStr := day.Format("2006-01-02")
			if ds, _, _, _, found := a.cachedActivitySummaryForRead(dayStr, "daily", a.currentUILanguage()); found {
				data.dailySummaries = append(data.dailySummaries, fmt.Sprintf("%s: %s", dayStr, ds))
			}
		}
	}

	// Cached AI summary
	weekKey := data.WeekStart + "/week"
	if summary, _, _, cachedLang, found := a.cachedActivitySummaryForRead(weekKey, "weekly", a.currentUILanguage()); found {
		data.AISummary = summary
		data.AISummaryStatus = "done"
		data.AISummaryLanguage = cachedLang
	} else {
		data.AISummaryStatus = "none"
	}

	return data, nil
}

// ──────────────────────────────────────────────
// App breakdown computation
// ──────────────────────────────────────────────

func (a *App) computeAppBreakdown(store *ActivityStore, startMs, endMs, totalActiveMs int64, sourceIDs ...map[string]bool) []AppUsageStat {
	allowed := a.activityModelSessionIDs(store, startMs, endMs, sourceIDs...)
	rows, err := store.reader().Query(`
		SELECT e.session_id, e.data FROM activity_events e
		JOIN activity_sessions s ON e.session_id = s.id
		WHERE e.timestamp >= ? AND e.timestamp < ?
		AND e.kind = 'app_switch'
		AND COALESCE(s.triage_status, '') != 'protected'
		ORDER BY e.timestamp`,
		startMs, endMs,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type appSpan struct {
		name  string
		start int64
	}

	appTime := make(map[string]int64)
	appBundle := make(map[string]string)
	var lastSwitch *appSpan

	for rows.Next() {
		var sessionID, data string
		if err := rows.Scan(&sessionID, &data); err != nil || !allowed[sessionID] {
			continue
		}
		var evt activitymon.ActivityEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}
		if evt.AppName != "" && evt.AppBundleID != "" {
			appBundle[evt.AppName] = evt.AppBundleID
		}
		if lastSwitch != nil && lastSwitch.name != "" {
			dur := evt.Timestamp - lastSwitch.start
			// Cap individual span at 5 minutes to avoid idle inflation
			if dur > 5*60*1000 {
				dur = 5 * 60 * 1000
			}
			if dur > 0 {
				appTime[lastSwitch.name] += dur
			}
		}
		name := evt.AppName
		if activityLockScreenApp(evt.AppName, evt.AppBundleID) {
			// The lock screen is never an app, and its time is no one's.
			name = ""
		}
		lastSwitch = &appSpan{name: name, start: evt.Timestamp}
	}

	// If no app_switch events, fall back to session app names
	if len(appTime) == 0 {
		sessionRows, err := store.modelSessionsOverlapping(startMs, endMs)
		if err == nil {
			for _, s := range sessionRows {
				if s.TriageStatus == "dismissed" || !allowed[s.ID] {
					continue
				}
				var names []string
				if err := json.Unmarshal([]byte(s.AppNames), &names); err == nil {
					dur := s.EndTime - s.StartTime
					if dur > 0 && len(names) > 0 {
						perApp := dur / int64(len(names))
						for _, n := range names {
							if pii.IsSensitiveAppName(n) || activityLockScreenApp(n, "") {
								continue
							}
							appTime[n] += perApp
						}
					}
				}
			}
		}
	}

	// Drop protected/sensitive apps: neither their names nor their usage
	// durations may appear in summary prompts or the summary UI. Check by
	// display name too — appBundle can miss a mapping when app_switch
	// events lacked a bundle ID.
	for name := range appTime {
		if pii.IsSensitiveAppName(name) {
			delete(appTime, name)
			continue
		}
		if _, isSensitive := pii.IsSensitiveApp(appBundle[name]); isSensitive {
			delete(appTime, name)
		}
	}

	// Top applications never add up to more than the day's active time
	// (activity review v1.0.0 #5): the per-app spans above are capped, not
	// clipped to presence, so scale them into it.
	var appTotal int64
	for _, ms := range appTime {
		appTotal += ms
	}
	if totalActiveMs > 0 && appTotal > totalActiveMs {
		for name, ms := range appTime {
			appTime[name] = ms * totalActiveMs / appTotal
		}
	}

	// Sort by time descending
	type kv struct {
		name string
		ms   int64
	}
	sorted := make([]kv, 0, len(appTime))
	for name, ms := range appTime {
		sorted = append(sorted, kv{name, ms})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ms > sorted[j].ms })

	// Take top 7
	if len(sorted) > 7 {
		sorted = sorted[:7]
	}

	total := totalActiveMs
	if total <= 0 {
		for _, kv := range sorted {
			total += kv.ms
		}
	}

	result := make([]AppUsageStat, len(sorted))
	for i, kv := range sorted {
		pct := 0.0
		if total > 0 {
			pct = float64(kv.ms) / float64(total) * 100
		}
		result[i] = AppUsageStat{
			AppName:    kv.name,
			BundleID:   appBundle[kv.name],
			ActiveMs:   kv.ms,
			Percentage: pct,
		}
	}
	return result
}

// ──────────────────────────────────────────────
// OCR keyword extraction
// ──────────────────────────────────────────────

// loadMoments returns only the cached agent-produced Moments payload. A
// mechanical grouping is intentionally not shown in the daily digest UI
// because it reads like an AI summary while being only app/session based.
func (a *App) loadMoments(store *ActivityStore, dateStr string) []MomentData {
	snapshot, found := store.cachedActivitySummarySnapshot(dateStr, "daily", a.currentUILanguage())
	if !found || !a.activitySummarySnapshotAllowsRead(dateStr, "daily", snapshot) {
		return nil
	}
	return decodeCachedActivityMoments(snapshot.Moments)
}

func decodeCachedActivityMoments(raw string) []MomentData {
	var moments []MomentData
	if raw != "" && json.Unmarshal([]byte(raw), &moments) == nil && len(moments) > 0 {
		return moments
	}
	return nil
}

// generateMomentsViaAgent asks Haiku to thematically group the day's sessions
// into narrative moments. Returns nil if the agent call fails or its output
// cannot be parsed — callers are expected to fall back to buildMoments.
//
// The agent receives sessions by index (S1..SN) and returns moments as
// [startIdx, endIdx] ranges with a title/subtitle. We convert index ranges to
// timestamp ranges locally so the agent cannot hallucinate timestamps.
//
// Takes no summary text — per-session window titles are sufficient thematic
// signal. Keeping this input-free lets the Moments call run in parallel with
// the daily summary LLM call rather than serialized after it.
func (a *App) generateMomentsViaAgent(sessions []ActivitySessionRow) []MomentData {
	if a.activity == nil || a.activity.store == nil {
		return nil
	}
	sessions = a.filterActivityModelSessions(a.activity.store, sessions)
	if len(sessions) == 0 {
		return nil
	}
	// ASC by start for predictable indexing.
	asc := make([]ActivitySessionRow, len(sessions))
	copy(asc, sessions)
	sort.Slice(asc, func(i, j int) bool { return asc[i].StartTime < asc[j].StartTime })

	titlesBySession := a.topWindowTitlesPerSession(asc)

	prompt := redactActivityModelText(buildMomentsPrompt(asc, titlesBySession, responseLanguageInstruction(a.currentUILanguageName())))
	raw, err := a.runEphemeralHaiku(prompt, 60*time.Second)
	if err != nil {
		log.Printf("[activity-summary] moments agent call failed: %v", err)
		return nil
	}
	ranges, err := parseMomentsResponse(raw, len(asc))
	if err != nil {
		log.Printf("[activity-summary] moments parse failed: %v (response_bytes=%d)", err, len(raw))
		return nil
	}
	moments := make([]MomentData, 0, len(ranges))
	for _, r := range ranges {
		m := a.momentFromRange(asc, r)
		moments = append(moments, m)
	}
	return moments
}

func (a *App) normalizeActivityMarkdownToCurrentUILanguage(kind, markdown string) string {
	if a == nil {
		return markdown
	}
	translated, err := a.translateMarkdownToCurrentUILanguage("activity "+kind, markdown)
	if err != nil {
		log.Printf("[activity-summary] failed to normalize generated %s to current UI language: %v", kind, err)
		return markdown
	}
	return translated
}

func (a *App) normalizeMomentRangesToCurrentUILanguage(ranges []momentRange) []momentRange {
	if a == nil || len(ranges) == 0 {
		return ranges
	}
	translated, err := a.translateMomentRangesToLanguage(ranges, a.currentUILanguageName())
	if err != nil {
		log.Printf("[activity-summary] failed to normalize moments to current UI language: %v", err)
		return ranges
	}
	return translated
}

func (a *App) translateMomentRangesToLanguage(ranges []momentRange, languageName string) ([]momentRange, error) {
	languageName = strings.TrimSpace(languageName)
	if languageName == "" {
		languageName = "English"
	}
	type momentText struct {
		Index       int    `json:"index"`
		Title       string `json:"title"`
		Subtitle    string `json:"subtitle"`
		Description string `json:"description"`
	}
	input := make([]momentText, 0, len(ranges))
	for i, r := range ranges {
		input = append(input, momentText{
			Index:       i,
			Title:       r.Title,
			Subtitle:    r.Subtitle,
			Description: r.Description,
		})
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("marshal moment text fields: %w", err)
	}
	prompt := fmt.Sprintf(`Translate these activity Moment text fields into %s.

Rules:
- Return ONLY strict JSON: {"moments":[{"index":0,"title":"...","subtitle":"...","description":"..."}]}
- Preserve every index exactly.
- Translate only title, subtitle, and description.
- Preserve app names, file paths, commands, channel names, URLs, code identifiers, and inline code exactly when translating them would change their meaning.
- Preserve Markdown emphasis and inline code in description.
- Do not add, remove, reorder, or merge moments.

Moment text fields:
%s`, languageName, string(inputJSON))

	raw, err := a.runEphemeralClaudeModel(prompt, a.lightTaskModel(), 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("translate moments to %s: %w", languageName, err)
	}
	raw = strings.TrimSpace(stripCodeFences(raw))
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("translation returned no JSON object")
	}
	var envelope struct {
		Moments []momentText `json:"moments"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal translated moments: %w", err)
	}
	translated := append([]momentRange(nil), ranges...)
	seen := make(map[int]bool, len(envelope.Moments))
	for _, item := range envelope.Moments {
		if item.Index < 0 || item.Index >= len(translated) || seen[item.Index] {
			continue
		}
		seen[item.Index] = true
		translated[item.Index].Title = strings.TrimSpace(item.Title)
		translated[item.Index].Subtitle = strings.TrimSpace(item.Subtitle)
		translated[item.Index].Description = strings.TrimSpace(item.Description)
	}
	if len(seen) != len(ranges) {
		return nil, fmt.Errorf("translation returned %d/%d moments", len(seen), len(ranges))
	}
	return translated, nil
}

// momentRange is the agent's output per moment before we derive apps.
type momentRange struct {
	StartIdx    int    `json:"startIdx"`
	EndIdx      int    `json:"endIdx"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
	Description string `json:"description"`
}

func (a *App) momentFromRange(asc []ActivitySessionRow, r momentRange) MomentData {
	// r.StartIdx / r.EndIdx are 1-based, validated to be in range.
	slice := asc[r.StartIdx-1 : r.EndIdx]
	apps := appsFromSessions(slice)
	m := MomentData{
		StartMs:     slice[0].StartTime,
		EndMs:       slice[len(slice)-1].EndTime,
		SessionIDs:  sessionIDsFromSessions(slice),
		Title:       strings.TrimSpace(r.Title),
		Subtitle:    strings.TrimSpace(r.Subtitle),
		Description: strings.TrimSpace(r.Description),
		Apps:        apps,
	}
	if m.Title == "" && len(m.Apps) > 0 {
		m.Title = m.Apps[0]
	}
	if m.Description == "" {
		m.Description = fallbackMomentDescription(slice, apps)
	}
	return m
}

func sessionIDsFromSessions(sessions []ActivitySessionRow) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if s.ID != "" {
			out = append(out, s.ID)
		}
	}
	return out
}

// appsFromSessions returns the union of apps seen across sessions, preserving
// first-seen order so the most-recently-active app isn't always listed first.
func appsFromSessions(sessions []ActivitySessionRow) []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, s := range sessions {
		var names []string
		if err := json.Unmarshal([]byte(s.AppNames), &names); err != nil {
			continue
		}
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func fallbackMomentDescription(sessions []ActivitySessionRow, apps []string) string {
	if len(sessions) == 0 {
		return ""
	}
	appList := compactAppList(apps, 4)
	totalEvents := 0
	totalSnapshots := 0
	for _, s := range sessions {
		totalEvents += s.EventCount
		totalSnapshots += s.SnapshotCount
	}
	detail := ""
	switch {
	case len(appList) > 0 && len(sessions) > 1:
		detail = fmt.Sprintf("A %s stretch across **%s**, grouped from %d nearby activity sessions.", formatDurationMs(sessions[len(sessions)-1].EndTime-sessions[0].StartTime), appList, len(sessions))
	case len(appList) > 0:
		detail = fmt.Sprintf("A %s stretch centered on **%s**.", formatDurationMs(sessions[0].EndTime-sessions[0].StartTime), appList)
	default:
		detail = fmt.Sprintf("A %s activity stretch captured in this part of the day.", formatDurationMs(sessions[len(sessions)-1].EndTime-sessions[0].StartTime))
	}
	if totalEvents > 0 || totalSnapshots > 0 {
		parts := make([]string, 0, 2)
		if totalEvents > 0 {
			parts = append(parts, fmt.Sprintf("%d events", totalEvents))
		}
		if totalSnapshots > 0 {
			parts = append(parts, fmt.Sprintf("%d snapshots", totalSnapshots))
		}
		detail += " Source trail: " + strings.Join(parts, ", ") + "."
	}
	return detail
}

func compactAppList(apps []string, max int) string {
	if len(apps) == 0 || max <= 0 {
		return ""
	}
	if len(apps) <= max {
		return strings.Join(apps, " · ")
	}
	return strings.Join(apps[:max], " · ") + fmt.Sprintf(" +%d", len(apps)-max)
}

// topWindowTitlesPerSession returns up to 3 representative window titles per
// session. Used as hint content for the moments agent.
func (a *App) topWindowTitlesPerSession(sessions []ActivitySessionRow) map[string][]string {
	out := make(map[string][]string, len(sessions))
	if a.activity == nil || a.activity.store == nil || len(sessions) == 0 {
		return out
	}
	sessions = a.filterActivityModelSessions(a.activity.store, sessions)
	if len(sessions) == 0 {
		return out
	}
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := fmt.Sprintf(`
		SELECT e.session_id, e.data FROM activity_events e
		WHERE e.session_id IN (%s)
		AND e.kind IN ('app_switch', 'focus')
		ORDER BY e.timestamp`, placeholders)
	rows, err := a.activity.store.reader().Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()

	counts := make(map[string]map[string]int)
	for rows.Next() {
		var sid, data string
		if err := rows.Scan(&sid, &data); err != nil {
			continue
		}
		var evt activitymon.ActivityEvent
		if json.Unmarshal([]byte(data), &evt) != nil || evt.WindowTitle == "" {
			continue
		}
		// Protected-app titles carry content (document/diary names) — skip
		// them here too: this covers historical rows persisted before the
		// event-level scrub existed.
		if _, isSensitive := pii.IsSensitiveApp(evt.AppBundleID); isSensitive {
			continue
		}
		if pii.IsSensitiveAppName(evt.AppName) {
			continue
		}
		title := redactActivityModelText(evt.WindowTitle)
		// Slice by runes, not bytes — window titles commonly contain CJK and
		// byte-slicing mid-codepoint produces invalid UTF-8 that later corrupts
		// the JSON payload sent to the Moments agent.
		if runes := []rune(title); len(runes) > 80 {
			title = string(runes[:80])
		}
		lower := strings.ToLower(strings.TrimSpace(title))
		if lower == "" || lower == "untitled" || lower == "desktop" {
			continue
		}
		if counts[sid] == nil {
			counts[sid] = make(map[string]int)
		}
		counts[sid][title]++
	}

	for sid, cs := range counts {
		type tc struct {
			t string
			c int
		}
		list := make([]tc, 0, len(cs))
		for t, c := range cs {
			list = append(list, tc{t, c})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].c > list[j].c })
		top := make([]string, 0, 3)
		for i := 0; i < len(list) && i < 3; i++ {
			top = append(top, list[i].t)
		}
		out[sid] = top
	}
	return out
}

func buildMomentsPrompt(sessions []ActivitySessionRow, titlesBySession map[string][]string, languageInstruction string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `%s

You are grouping a person's day into 5-15 narrative "moments" for a summary view.

Each moment is a consecutive run of sessions that belong together thematically (e.g. "PR review deep dive", "Morning standup + Slack catch-up", "Debugging session"). Adjacent short sessions on related work should merge into one moment. Unrelated context switches should become separate moments.

Rules:
- Respond with STRICT JSON: {"moments": [{"startIdx": 1, "endIdx": 3, "title": "...", "subtitle": "...", "description": "..."}]}
- startIdx and endIdx are 1-based session indexes (inclusive). Must be 1 <= startIdx <= endIdx <= N.
- Moments must not overlap and should be in ascending order.
- Every session does NOT need to be covered — it's OK to skip 1-2 fragmentary sessions between coherent moments.
- Aim for 5-15 moments total. Err toward fewer, larger moments for a narrative feel.
- Title: short, concrete, 2-8 words. Name real projects/files/topics when visible in window titles. No emoji. Follow the UI language instruction above.
- Subtitle: optional, 4-12 words of supporting detail. Omit if nothing to add.
- Description: required, 1-2 concise sentences in markdown. Say what you did and mention concrete projects, files, people, channels, or artifacts visible in the titles. Use only simple markdown emphasis or inline code; no headings, tables, or bullet lists.
- Do NOT include any prose outside the JSON object.

%s

`, languageInstruction, activitySecondPersonRule)

	sb.WriteString("Sessions (index. HH:MM-HH:MM | duration | apps | top window titles):\n")

	for i, s := range sessions {
		idx := i + 1
		start := time.UnixMilli(s.StartTime).Format("15:04")
		end := time.UnixMilli(s.EndTime).Format("15:04")
		dur := formatDurationMs(s.EndTime - s.StartTime)
		var apps []string
		_ = json.Unmarshal([]byte(s.AppNames), &apps)
		appStr := strings.Join(apps, ",")
		titles := titlesBySession[s.ID]
		titleStr := ""
		if len(titles) > 0 {
			quoted := make([]string, len(titles))
			for j, t := range titles {
				quoted[j] = fmt.Sprintf("%q", t)
			}
			titleStr = strings.Join(quoted, ", ")
		}
		fmt.Fprintf(&sb, "%d. %s-%s | %s | %s | %s\n", idx, start, end, dur, appStr, titleStr)
	}

	sb.WriteString("\nRespond with the JSON object now:")
	return sb.String()
}

// parseMomentsResponse extracts the JSON object from the agent's raw output and
// validates ranges against the session count.
func parseMomentsResponse(raw string, sessionCount int) ([]momentRange, error) {
	// Trim common wrapping (```json ... ``` or leading prose).
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if strings.HasPrefix(strings.ToLower(s), "json") {
			s = s[4:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	start := strings.Index(s, "{")
	if start < 0 {
		return nil, fmt.Errorf("no JSON object in response")
	}

	// Decode only the first JSON value: a reply can add a note or a second
	// fenced block after it, and the old first-to-last-brace cut then failed.
	var envelope struct {
		Moments []momentRange `json:"moments"`
	}
	if err := json.NewDecoder(strings.NewReader(s[start:])).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	out := make([]momentRange, 0, len(envelope.Moments))
	for _, r := range envelope.Moments {
		if r.StartIdx < 1 || r.EndIdx > sessionCount || r.StartIdx > r.EndIdx {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartIdx < out[j].StartIdx })
	// Drop overlaps: if a later range's StartIdx <= prior's EndIdx, skip it.
	deduped := out[:0]
	lastEnd := 0
	for _, r := range out {
		if r.StartIdx <= lastEnd {
			continue
		}
		deduped = append(deduped, r)
		lastEnd = r.EndIdx
	}
	if len(deduped) == 0 {
		return nil, fmt.Errorf("no valid moments in response")
	}
	return deduped, nil
}

func truncateMomentsLog(s string, n int) string {
	if runes := []rune(s); len(runes) > n {
		return string(runes[:n]) + "..."
	}
	return s
}

func (a *App) extractTopOCRKeywords(store *ActivityStore, startMs, endMs int64, sourceIDs ...map[string]bool) []string {
	allowed := a.activityModelSessionIDs(store, startMs, endMs, sourceIDs...)
	// Protected sessions are excluded: their frames (possibly persisted
	// before the OCR gate existed) must not feed keyword extraction.
	rows, err := store.reader().Query(`
		SELECT f.session_id, f.text FROM activity_ocr_frames f
		JOIN activity_sessions s ON f.session_id = s.id
		WHERE s.end_time >= ? AND s.start_time < ? AND f.text != ''
		AND COALESCE(s.triage_status, '') != 'protected'`,
		startMs, endMs,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	wordCount := make(map[string]int)
	stopWords := map[string]bool{
		// English stop words
		"the": true, "a": true, "an": true, "is": true, "are": true, "was": true,
		"were": true, "be": true, "been": true, "to": true, "of": true, "and": true,
		"in": true, "that": true, "it": true, "for": true, "on": true, "with": true,
		"as": true, "at": true, "by": true, "from": true, "or": true, "this": true,
		"not": true, "but": true, "all": true, "can": true, "had": true, "has": true,
		"have": true, "will": true, "would": true, "there": true, "their": true,
		"you": true, "your": true, "we": true, "our": true, "they": true,
		// Code noise words
		"import": true, "export": true, "function": true, "const": true, "let": true,
		"var": true, "return": true, "if": true, "else": true, "new": true,
		"true": true, "false": true, "null": true, "undefined": true, "void": true,
		"class": true, "interface": true, "type": true, "string": true, "number": true,
		"err": true, "nil": true, "func": true, "package": true, "struct": true,
		"int": true, "bool": true, "error": true, "fmt": true, "log": true,
		"div": true, "span": true, "src": true, "href": true, "className": true,
	}

	for rows.Next() {
		var sessionID, text string
		if err := rows.Scan(&sessionID, &text); err != nil || !allowed[sessionID] {
			continue
		}
		words := strings.Fields(strings.ToLower(redactActivityModelText(text)))
		for _, w := range words {
			w = strings.Trim(w, ".,;:!?\"'()[]{}/<>")
			if len(w) > 2 && !stopWords[w] {
				wordCount[w]++
			}
		}
	}

	type wc struct {
		word  string
		count int
	}
	sorted := make([]wc, 0, len(wordCount))
	for w, c := range wordCount {
		sorted = append(sorted, wc{w, c})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })

	result := make([]string, 0, 10)
	for i := 0; i < len(sorted) && i < 10; i++ {
		result = append(result, sorted[i].word)
	}
	return result
}

// ──────────────────────────────────────────────
// Window title extraction
// ──────────────────────────────────────────────

func (a *App) extractTopWindowTitles(store *ActivityStore, startMs, endMs int64, sourceIDs ...map[string]bool) []string {
	allowed := a.activityModelSessionIDs(store, startMs, endMs, sourceIDs...)
	rows, err := store.reader().Query(`
		SELECT e.session_id, e.data FROM activity_events e
		JOIN activity_sessions s ON e.session_id = s.id
		WHERE e.timestamp >= ? AND e.timestamp < ?
		AND e.kind IN ('app_switch', 'focus')
		AND COALESCE(s.triage_status, '') != 'protected'
		ORDER BY e.timestamp`,
		startMs, endMs,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	titleCount := make(map[string]int)
	for rows.Next() {
		var sessionID, data string
		if err := rows.Scan(&sessionID, &data); err != nil || !allowed[sessionID] {
			continue
		}
		var evt activitymon.ActivityEvent
		if json.Unmarshal([]byte(data), &evt) != nil || evt.WindowTitle == "" {
			continue
		}
		// Skip protected apps — their window titles carry content (document
		// names, diary entry titles) and must not reach summary prompts.
		// Also covers historical rows persisted before the event-level scrub.
		if _, isSensitive := pii.IsSensitiveApp(evt.AppBundleID); isSensitive {
			continue
		}
		// Clean up very long or generic titles
		title := redactActivityModelText(evt.WindowTitle)
		// Slice by runes, not bytes — window titles commonly contain CJK and
		// byte-slicing mid-codepoint produces invalid UTF-8 that later corrupts
		// the JSON payload sent to the Moments agent.
		if runes := []rune(title); len(runes) > 80 {
			title = string(runes[:80])
		}
		// Skip generic/empty titles
		lower := strings.ToLower(title)
		if lower == "" || lower == "untitled" || lower == "desktop" {
			continue
		}
		titleCount[title]++
	}

	type tc struct {
		title string
		count int
	}
	sorted := make([]tc, 0, len(titleCount))
	for t, c := range titleCount {
		sorted = append(sorted, tc{t, c})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })

	// Return top 8 unique titles
	result := make([]string, 0, 8)
	for i := 0; i < len(sorted) && i < 8; i++ {
		result = append(result, sorted[i].title)
	}
	return result
}

// ──────────────────────────────────────────────
// 7-day averages
// ──────────────────────────────────────────────

// activitySummaryDayStats uses the Day page's session gates and present-time
// rule. Aggregate one local day at a time so midnight and DST boundaries match
// the Day page, without counting a gap between different days.
func (a *App) activitySummaryDayStats(store *ActivityStore, day time.Time, sourceIDs ...map[string]bool) (DayStatBrief, int, error) {
	stats := DayStatBrief{Date: day.Format("2006-01-02")}
	startMs, endMs := day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli()
	sessions, err := store.modelSessionsOverlapping(startMs, endMs)
	if err != nil {
		return stats, 0, err
	}
	if len(sourceIDs) > 0 {
		filtered := sessions[:0]
		for _, session := range sessions {
			if sourceIDs[0][session.ID] {
				filtered = append(filtered, session)
			}
		}
		sessions = filtered
	}
	sessions = a.filterActivityModelSessions(store, sessions)
	stats.ActiveMs, _, err = a.activityPresentTimeMs(context.Background(), store, sessions, startMs, endMs)
	if err != nil {
		return stats, 0, err
	}
	stats.SessionCount = len(sessions)
	apps := make(map[string]bool)
	for _, session := range sessions {
		var names []string
		if json.Unmarshal([]byte(session.AppNames), &names) == nil {
			for _, name := range names {
				apps[name] = true
			}
		}
	}
	return stats, len(apps), nil
}

func (a *App) compute7DayAverages(store *ActivityStore, avgStartMs, avgEndMs int64, data *DailySummaryData) error {
	var days int
	var totalActive int64
	var totalSessions int
	var totalApps int
	for day := time.UnixMilli(avgStartMs).In(time.Local); day.UnixMilli() < avgEndMs; day = day.AddDate(0, 0, 1) {
		stats, apps, err := a.activitySummaryDayStats(store, day)
		if err != nil {
			return err
		}
		if stats.ActiveMs <= 0 {
			continue
		}
		days++
		totalActive += stats.ActiveMs
		totalSessions += stats.SessionCount
		totalApps += apps
	}
	if days == 0 {
		return nil
	}
	data.ActiveAvgMs = totalActive / int64(days)
	data.SessionAvg = float64(totalSessions) / float64(days)
	data.UniqueAppsAvg = float64(totalApps) / float64(days)
	return nil
}

// ──────────────────────────────────────────────
// AI summary generation
// ──────────────────────────────────────────────

// GenerateDailySummary triggers AI summary generation for a specific date.
// Concurrent calls for the same dateStr/language coalesce via singleflight: every
// caller awaits the same in-flight LLM run instead of racing duplicate
// generations. Frontend Generate buttons, the cron rollover handler, and the
// startup pending-summaries pass can all hit this concurrently for "today".
func (a *App) GenerateDailySummary(dateStr string) error {
	sfKey := dateStr + "|" + normalizeSummaryLanguage(a.currentUILanguage())
	_, err, _ := a.generateDailySummarySF.Do(sfKey, func() (any, error) {
		return nil, a.runGenerateDailySummary(dateStr)
	})
	return err
}

// runGenerateDailySummary contains the actual generation logic. Always invoked
// through singleflight via GenerateDailySummary so it never runs more than
// once concurrently for the same date.
func (a *App) runGenerateDailySummary(dateStr string) error {
	am := a.activity
	if am == nil || am.store == nil {
		return fmt.Errorf("activity monitor not initialized")
	}
	sources, err := a.captureSummarySources(dateStr, "daily")
	if err != nil {
		return err
	}
	language := a.currentUILanguage()

	// Get structured data
	data, err := a.getDailySummary(dateStr, &sources)
	if err != nil {
		return err
	}
	if data.SessionCount == 0 {
		return fmt.Errorf("no activity data for %s", dateStr)
	}

	// Mark as generating
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "activity:summary-generating", dateStr)
	}

	// Kick off the Moments agent call in parallel. The Haiku call no longer
	// needs the summary text, so its wall-clock runs concurrently with the
	// Sonnet summary call below. Persisting the result still has to happen
	// AFTER SaveSummary because SaveMoments is UPDATE-only — the summary row
	// must exist first. We enforce that ordering via the channel: the
	// persistMoments goroutine is only started after SaveSummary succeeds.
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "activity:moments-generating", dateStr)
	}
	momentsCh := make(chan []MomentData, 1)
	go a.generateMomentsAsync(dateStr, momentsCh, data.modelSessions)

	// Build prompt with rich per-source context (transcripts, memories,
	// knowledge, messenger, OCR, clicks, speech).
	prompt := a.buildRichSummaryPrompt(dateStr, data)
	if prompt == "" {
		return fmt.Errorf("activity source changed before model generation")
	}
	// "Isoform noticed" rides in the same call (activity_noticed.go).
	prompt = a.withNoticedRequest(prompt, dateStr)

	// Call stronger model for daily summary — runs once per day, quality matters.
	summary, err := a.callRichModelForSummary(prompt)
	if err != nil {
		log.Printf("[activity-summary] AI generation failed for %s: %v", dateStr, err)
		if a.ctx != nil {
			wailsRuntime.EventsEmit(a.ctx, "activity:summary-error", dateStr)
		}
		// Drain the moments channel so generateMomentsAsync doesn't leak on
		// send if it finishes after we've returned. Also signal moments-error
		// so the frontend can clear its loading state — moments are never
		// persisted when the summary row is missing (SaveMoments is UPDATE-only).
		go func() {
			<-momentsCh
			if a.ctx != nil {
				wailsRuntime.EventsEmit(a.ctx, "activity:moments-error", dateStr)
			}
		}()
		return err
	}
	// Both generation prompts already request the UI language. Do not spend
	// another model pass translating the summary or its moments.
	summary, noticed := splitNoticedBlock(summary)
	summary = secondPersonLine(summary, a.activitySelfNames())

	// Determine if partial (today)
	today := time.Now().Format("2006-01-02")
	isPartial := dateStr == today

	// Save to cache; gen pins this attempt's identity for the moments pipeline
	// so a stalled goroutine from a prior attempt can't pretend its result
	// satisfies our wait.
	var gen int
	err = am.store.withSummarySources(sources, func() error {
		if !a.summarySourcesAllowRead(sources.Sources) {
			return fmt.Errorf("activity source changed during model generation")
		}
		var saveErr error
		gen, saveErr = am.store.saveSummaryWithSources(dateStr, "daily", summary, a.lightTaskModel(), isPartial, language, sources.Sources)
		if saveErr != nil {
			return saveErr
		}
		// Clear the prior generation's moments in the same deletion fence.
		if err := am.store.SaveMomentsWithLanguage(dateStr, "", 0, language); err != nil {
			log.Printf("[activity-summary] failed to clear moments for %s: %v", dateStr, err)
		}
		return nil
	})
	if err != nil {
		log.Printf("[activity-summary] failed to save summary for %s: %v", dateStr, err)
		go func() {
			<-momentsCh
			if a.ctx != nil {
				wailsRuntime.EventsEmit(a.ctx, "activity:moments-error", dateStr)
			}
		}()
		return err
	}

	log.Printf("[activity-summary] generated summary for %s (partial=%v, gen=%d)", dateStr, isPartial, gen)
	a.saveNoticed(dateStr, language, gen, noticed)

	if a.telemetry != nil {
		a.telemetry.Capture("activity.summary.generated", map[string]any{
			"date":       dateStr,
			"is_partial": isPartial,
			"generation": gen,
		})
	}

	// Notify frontend the summary text is ready — moments persistence happens
	// asynchronously below and must not affect this latency or success rate.
	if a.ctx != nil {
		// A new summary can change the day's noticed items.
		a.touchDerivedViews(derivedDayNoticed)
		wailsRuntime.EventsEmit(a.ctx, "activity:summary-ready", dateStr)
	}

	// Persist moments when the parallel agent call returns. Failures are
	// logged and swallowed; the UI shows an explicit generate/regenerate state
	// instead of presenting mechanical grouping as a digest. gen gates the
	// write so a stalled prior-attempt goroutine that lands here after a new
	// SaveSummary cannot fool the wait loop.
	go a.persistMomentsWithSources(dateStr, gen, language, momentsCh, sources)

	return nil
}

// generateMomentsAsync runs the agent-driven Moments pipeline and sends the
// result on out. Exactly one value is always sent (nil on any error/panic),
// so the receiver can drain safely. Intended to run in a goroutine in
// parallel with the daily summary LLM call.
func (a *App) generateMomentsAsync(dateStr string, out chan<- []MomentData, captured ...[]ActivitySessionRow) {
	var moments []MomentData
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[activity-summary] moments panic for %s: %v", dateStr, r)
		}
		out <- moments
	}()
	if len(captured) > 0 {
		moments = a.generateMomentsViaAgent(captured[0])
	} else {
		moments = a.computeMoments(dateStr)
	}
}

func (a *App) computeMoments(dateStr string) []MomentData {
	am := a.activity
	if am == nil || am.store == nil {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	if err != nil {
		return nil
	}
	// AddDate(0,0,1) keeps the same wall-clock time on the next day across DST
	// transitions; 24*time.Hour would shift by an hour on spring-forward or
	// fall-back days and drop or double-count sessions.
	startMs := t.UnixMilli()
	endMs := t.AddDate(0, 0, 1).UnixMilli()
	sessions, err := am.store.modelSessionsOverlapping(startMs, endMs)
	if err != nil {
		log.Printf("[activity-summary] moments: failed to load sessions for %s: %v", dateStr, err)
		return nil
	}
	return a.generateMomentsViaAgent(sessions)
}

// persistMomentsForLanguage waits for the parallel Moments agent to finish, then writes
// the payload — but only against the generation that owned this attempt at
// the time SaveSummary ran. Must only be started AFTER SaveSummary succeeds —
// SaveMoments is UPDATE-only and no-ops if the summary row is missing or the
// generation has been bumped by a newer SaveSummary. Emits
// activity:moments-ready on success and activity:moments-error on real
// failures (empty agent output, marshal/save error). Generation drift
// produces NEITHER event — the newer attempt that bumped the generation has
// its own persistMomentsForLanguage goroutine that will emit the authoritative event,
// and the frontend listens by date so a drift error here would prematurely
// clear the new attempt's loading state.
func (a *App) persistMomentsForLanguage(dateStr string, generation int, language string, in <-chan []MomentData) {
	sources, err := a.captureSummarySources(dateStr, "daily")
	if err != nil {
		<-in
		return
	}
	a.persistMomentsWithSources(dateStr, generation, language, in, sources)
}

func (a *App) persistMomentsWithSources(dateStr string, generation int, language string, in <-chan []MomentData, sources activitySummarySourceSet) (outcome string) {
	outcome = "error"
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[activity-summary] moments persist panic for %s: %v", dateStr, r)
		}
		if a.ctx == nil {
			return
		}
		switch outcome {
		case "ready":
			wailsRuntime.EventsEmit(a.ctx, "activity:moments-ready", dateStr)
		case "superseded":
			// Stay silent — newer attempt owns the date, its goroutine
			// will emit ready/error.
		default:
			wailsRuntime.EventsEmit(a.ctx, "activity:moments-error", dateStr)
		}
	}()
	am := a.activity
	if am == nil || am.store == nil {
		return
	}
	moments := <-in
	if len(moments) == 0 {
		return
	}
	payload, err := json.Marshal(moments)
	if err != nil {
		log.Printf("[activity-summary] moments marshal failed for %s: %v", dateStr, err)
		return
	}
	if err := am.store.withSummarySources(sources, func() error {
		if !a.summarySourcesAllowRead(sources.Sources) {
			return fmt.Errorf("activity source changed during model generation")
		}
		return am.store.SaveMomentsWithLanguage(dateStr, string(payload), generation, language)
	}); err != nil {
		log.Printf("[activity-summary] failed to save moments for %s (gen=%d): %v", dateStr, generation, err)
		return
	}
	// Verify the gated write actually landed — a stalled goroutine from a
	// previous attempt would silently no-op here when the row's generation
	// has moved on. Treat as discarded so the deferred event emit stays
	// silent (the newer attempt is on the hook for ready/error).
	if _, gen, ok := am.store.GetMomentsPartialForLanguage(dateStr, language); !ok {
		return
	} else if gen != generation {
		log.Printf("[activity-summary] moments for %s discarded (gen=%d, current=%d)", dateStr, generation, gen)
		outcome = "superseded"
		return
	}
	log.Printf("[activity-summary] saved %d moments for %s (gen=%d)", len(moments), dateStr, generation)
	return "ready"
}

// GenerateWeeklySummary triggers AI weekly summary generation.
func (a *App) GenerateWeeklySummary(dateStr string) error {
	am := a.activity
	if am == nil || am.store == nil {
		return fmt.Errorf("activity monitor not initialized")
	}

	day, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	if err != nil {
		return err
	}
	monday := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
	sources, err := a.captureSummarySources(monday.Format("2006-01-02"), "weekly")
	if err != nil {
		return err
	}
	data, err := a.getWeeklySummary(dateStr, &sources)
	if err != nil {
		return err
	}
	if data.TotalActiveMs == 0 {
		return fmt.Errorf("no activity data for week of %s", data.WeekStart)
	}

	prompt := buildWeeklySummaryPrompt(data, responseLanguageInstruction(a.currentUILanguageName()))
	summary, err := a.callHaikuForSummary(prompt)
	if err != nil {
		return err
	}
	summary = a.normalizeActivityMarkdownToCurrentUILanguage("weekly summary", summary)
	summary = secondPersonLine(summary, a.activitySelfNames())

	weekKey := data.WeekStart + "/week"
	// Weekly summaries don't run a moments pipeline, so we discard gen.
	if err := am.store.withSummarySources(sources, func() error {
		if !a.summarySourcesAllowRead(sources.Sources) {
			return fmt.Errorf("activity source changed during model generation")
		}
		_, err := am.store.saveSummaryWithSources(weekKey, "weekly", summary, a.lightTaskModel(), false, a.currentUILanguage(), sources.Sources)
		return err
	}); err != nil {
		return err
	}

	if a.ctx != nil {
		// A new summary can change the day's noticed items.
		a.touchDerivedViews(derivedDayNoticed)
		wailsRuntime.EventsEmit(a.ctx, "activity:summary-ready", data.WeekStart)
	}
	return nil
}

// ──────────────────────────────────────────────
// Timing triggers
// ──────────────────────────────────────────────

// pendingSummaryDaysBack is how far back checkPendingSummaries scans for
// stale / missing daily summaries. 7 days covers a typical weekend-off /
// short-trip absence without re-billing tokens for older days the user is
// unlikely to look at.
const pendingSummaryDaysBack = 7

// pendingSummaryMaxRetries is the number of additional attempts after the
// first failure (so total attempts = 1 + retries). Sleep between attempts
// gives transient agent / network errors time to clear.
const pendingSummaryMaxRetries = 2

// pendingSummaryRetrySleep is the wait between retry attempts.
const pendingSummaryRetrySleep = 30 * time.Second

// pendingMomentsWaitTimeout caps how long a startup retry waits for the
// asynchronous Moments goroutine to land before treating the attempt as a
// Moments-only failure. The Moments agent typically returns within 5–30s;
// 90s leaves headroom for the slow tail without making the retry loop
// stretch the whole startup recovery beyond a reasonable bound.
const pendingMomentsWaitTimeout = 90 * time.Second

// pendingMomentsPollInterval is how often waitForMomentsPersisted checks the
// flag — 1s keeps polling cost negligible (one indexed point lookup per tick)
// while ensuring we notice a fast Moments completion within a second.
const pendingMomentsPollInterval = 1 * time.Second

// momentsWaitResult disambiguates the four ways waitForMomentsPersisted can
// stop polling. Callers must distinguish drift (someone else owns this date
// now) from timeout (we're still on the hook), because retrying on drift
// would clobber the new owner's freshly written summary.
type momentsWaitResult int

const (
	momentsWaitPersisted   momentsWaitResult = iota // moments_partial flipped to 0 at our generation
	momentsWaitTimeout                              // deadline elapsed before any state change
	momentsWaitDrift                                // another SaveSummary bumped moments_generation past us
	momentsWaitShutdown                             // a.ctx cancelled (app exiting)
	momentsWaitUnavailable                          // store missing — caller's invariant broken
)

// checkPendingSummaries scans the last pendingSummaryDaysBack days for daily
// summaries that are missing, partial, or have stale Moments, and regenerates
// each one with retries. Runs as a launch task queued right after the
// credential probe (deferAgentProbe), so it only checks the probe result
// (probeAndGreet sets a.probeSucceeded = true) instead of waiting for it:
// launch tasks run one at a time, so waiting here would hold the queue.
func (a *App) checkPendingSummaries() {
	am := a.activity
	if am == nil || am.store == nil {
		return
	}
	if a.launch.isStopped() {
		return
	}
	a.mu.Lock()
	ready := a.probeSucceeded
	a.mu.Unlock()
	if !ready {
		log.Printf("[activity-summary] credentials not verified, skipping pending summaries")
		return
	}

	pending := am.store.ListPendingSummaryDatesForLanguage(pendingSummaryDaysBack, a.currentUILanguage())
	if len(pending) > 0 {
		log.Printf("[activity-summary] pending daily summaries: %v", pending)
	}
	for _, dateStr := range pending {
		if a.launch.isStopped() {
			return
		}
		if err := a.generateDailySummaryWithRetry(dateStr); err != nil {
			log.Printf("[activity-summary] pending generation gave up for %s: %v", dateStr, err)
		}
	}

	// Weekly summary on Monday
	if time.Now().Weekday() == time.Monday {
		lastMonday := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
		weekKey := lastMonday + "/week"
		if _, _, _, _, found := am.store.GetCachedSummaryForLanguage(weekKey, "weekly", a.currentUILanguage()); !found {
			log.Printf("[activity-summary] generating pending weekly summary for week of %s", lastMonday)
			if err := a.GenerateWeeklySummary(lastMonday); err != nil {
				log.Printf("[activity-summary] pending weekly generation failed: %v", err)
			}
		}
	}
}

// generateDailySummaryWithRetry calls GenerateDailySummary up to
// 1+pendingSummaryMaxRetries times, sleeping pendingSummaryRetrySleep between
// attempts. Used during startup recovery so a single transient agent error
// doesn't leave a day's summary stale until the next app restart.
//
// GenerateDailySummary returns once the topic summary is saved; the Moments
// pipeline runs in a fire-and-forget goroutine and may silently no-op (empty
// agent output, marshal/save error). To make this loop also catch
// Moments-only failures, each successful topic-attempt then waits for
// moments_partial to flip to 0 within pendingMomentsWaitTimeout and treats a
// timeout as a retryable failure.
//
// Uses a.ctx-aware sleeps so app shutdown interrupts the recovery loop
// promptly instead of holding the goroutine for the full retry window.
func (a *App) generateDailySummaryWithRetry(dateStr string) error {
	am := a.activity
	if am == nil || am.store == nil {
		return fmt.Errorf("activity monitor not initialized")
	}
	var lastErr error
	for attempt := 0; attempt <= pendingSummaryMaxRetries; attempt++ {
		language := a.currentUILanguage()
		if attempt > 0 {
			if !a.sleepCtx(pendingSummaryRetrySleep) {
				return fmt.Errorf("aborted retry for %s: app shutting down", dateStr)
			}
			log.Printf("[activity-summary] retry %d/%d for %s", attempt, pendingSummaryMaxRetries, dateStr)
		}
		if err := a.GenerateDailySummary(dateStr); err != nil {
			lastErr = err
			log.Printf("[activity-summary] generate %s attempt %d failed: %v", dateStr, attempt+1, err)
			continue
		}
		// Capture this attempt's generation right after GenerateDailySummary
		// returns. The SaveSummary inside it bumped moments_generation; any
		// subsequent SaveSummary (later retry, regenerate) will bump again
		// and the wait below will report drift.
		_, expectedGen, ok := am.store.GetMomentsPartialForLanguage(dateStr, language)
		if !ok {
			lastErr = fmt.Errorf("moments row missing for %s after generate", dateStr)
			log.Printf("[activity-summary] %s attempt %d: row missing post-save", dateStr, attempt+1)
			continue
		}
		switch a.waitForMomentsPersistedForLanguage(dateStr, expectedGen, language, pendingMomentsWaitTimeout) {
		case momentsWaitPersisted:
			return nil
		case momentsWaitDrift:
			// Another path (user manual regenerate, concurrent recovery,
			// rollover handler) has taken over this date with a newer
			// generation. Retrying here would call SaveSummary again and
			// clobber the new owner's freshly written summary + clear its
			// in-flight moments_json. Yield instead — the new owner has
			// its own loop tracking persistence.
			log.Printf("[activity-summary] %s attempt %d: yielding to newer attempt (drift)", dateStr, attempt+1)
			return nil
		case momentsWaitShutdown:
			return fmt.Errorf("aborted wait for %s: app shutting down", dateStr)
		case momentsWaitUnavailable:
			return fmt.Errorf("activity store unavailable for %s", dateStr)
		case momentsWaitTimeout:
			lastErr = fmt.Errorf("moments not persisted for %s within %s", dateStr, pendingMomentsWaitTimeout)
			log.Printf("[activity-summary] %s attempt %d: topic done but moments still partial after %s", dateStr, attempt+1, pendingMomentsWaitTimeout)
		}
	}
	return lastErr
}

// sleepCtx blocks for d, returning false if a.ctx is cancelled before d
// elapses. Returns true otherwise. a.ctx is nil in unit tests, so we fall
// back to a plain time.Sleep there (a.ctx is only set by Wails OnStartup).
func (a *App) sleepCtx(d time.Duration) bool {
	if a.ctx == nil {
		time.Sleep(d)
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-a.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// waitForMomentsPersistedForLanguage polls the moments_partial flag for the given date
// until it flips to 0 at the expected generation, or the timeout elapses.
// The returned momentsWaitResult tells the caller WHY polling stopped — the
// retry loop must distinguish drift (someone else owns this date, back off)
// from timeout (still ours, retry). A missing row is treated as "still
// pending" (GetMomentsPartial returns ok=false), so a never-saved summary
// doesn't cause this to claim success.
func (a *App) waitForMomentsPersistedForLanguage(dateStr string, expectedGeneration int, language string, timeout time.Duration) momentsWaitResult {
	am := a.activity
	if am == nil || am.store == nil {
		return momentsWaitUnavailable
	}
	deadline := time.Now().Add(timeout)
	for {
		partial, gen, ok := am.store.GetMomentsPartialForLanguage(dateStr, language)
		if ok && gen != expectedGeneration {
			// Another upsert (regenerate, retry) bumped the row past us.
			// The new attempt has its own waitForMomentsPersisted; this
			// one must yield rather than retry, or its retry loop would
			// clobber the new owner's fresh summary text and moments.
			return momentsWaitDrift
		}
		if ok && partial == 0 {
			return momentsWaitPersisted
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return momentsWaitTimeout
		}
		wait := pendingMomentsPollInterval
		if wait > remaining {
			wait = remaining
		}
		if !a.sleepCtx(wait) {
			return momentsWaitShutdown
		}
	}
}

// onSessionDateRollover checks if a saved session belongs to a previous day
// and triggers summary generation. Called from extractSession.
func (a *App) onSessionDateRollover(session ActivitySessionRow) {
	today := time.Now().Format("2006-01-02")
	sessionDate := time.UnixMilli(session.StartTime).Format("2006-01-02")
	if sessionDate != today && sessionDate != "" {
		am := a.activity
		if am == nil || am.store == nil {
			return
		}
		if _, isPartial, _, _, found := am.store.GetCachedSummaryForLanguage(sessionDate, "daily", a.currentUILanguage()); !found || isPartial {
			log.Printf("[activity-summary] date rollover detected, generating summary for %s", sessionDate)
			go func() {
				if err := a.GenerateDailySummary(sessionDate); err != nil {
					log.Printf("[activity-summary] date rollover generation failed for %s: %v", sessionDate, err)
				}
			}()
		}
	}
}

// ──────────────────────────────────────────────
// Haiku API call (via agent runner, same as probeAndGreet)
// ──────────────────────────────────────────────

func (a *App) callHaikuForSummary(prompt string) (string, error) {
	return a.runEphemeralHaiku(redactActivityModelText(prompt), 60*time.Second)
}

// callRichModelForSummary uses the chat-tier model (Sonnet by default) for
// daily/weekly summaries since they run once per day with rich input and
// quality matters more than latency.
func (a *App) callRichModelForSummary(prompt string) (string, error) {
	return a.runEphemeralClaudeModel(redactActivityModelText(prompt), a.chatTaskModel(), 120*time.Second)
}

// ──────────────────────────────────────────────
// Prompt builders
// ──────────────────────────────────────────────

// buildRichSummaryPrompt builds a daily summary prompt that includes ALL
// available context: meetings, memories, knowledge, messenger
// conversations, OCR samples, click highlights, speech. The model can then
// describe what was actually done, not just how much time was spent.
func (a *App) buildRichSummaryPrompt(dateStr string, data *DailySummaryData) string {
	if data.modelSources != nil && !a.summarySourcesAllowRead(data.modelSources.Sources) {
		return ""
	}
	const maxPromptBytes = 200_000

	var sb strings.Builder

	fmt.Fprintf(&sb, `%s

Write a daily activity digest as scannable Markdown. Use the rich context below and name real people, projects, companies, dollar amounts, dates, branches, PRs, channels, files, and artifacts when visible.

Output format:
1. Start with exactly one short opening sentence summarizing the day's shape.
2. Then write 2-4 topic sections using Markdown headings:
   ### Technical
   ### Follow-ups & blockers
   ### Meetings & deals
   ### Admin / finance
   ### Personal
   Use only the sections that fit the data, and rename headings to match the actual day when a more specific label is clearer.
3. Under each heading, write 2-4 bullet points. Each bullet should be one concise line; two lines is acceptable when the item is genuinely dense.

Bullet rules:
- Be SPECIFIC. Include names, products, branches, PR numbers, channels, files, dollar amounts, and dates you see in the data.
- CURATE. Pick the 8-14 most meaningful items for the day, not every trace.
- Include state when inferable, using plain prefixes like "Decision:", "Next:", "Done:", "Open:", "Blocked:", or "Waiting:".
- Prefer verbs of commitment over passive description.
- Keep bullets readable in a calendar card. Do not write long paragraphs.
- No emojis. No tables. No ending invitation or question.
- Do NOT use rigid data-source headings like "OCR", "Messenger", or "Window Titles". Group by meaning.
- If data is sparse, write fewer sections and fewer bullets. Don't pad.

%s

`, responseLanguageInstruction(a.currentUILanguageName()), activitySecondPersonRule)

	// Core stats
	fmt.Fprintf(&sb, "Date: %s\n", dateStr)
	fmt.Fprintf(&sb, "Total active time: %s\n", formatDurationMs(data.TotalActiveMs))
	fmt.Fprintf(&sb, "Sessions: %d\n", data.SessionCount)

	if len(data.AppBreakdown) > 0 {
		sb.WriteString("Apps (by time): ")
		for i, app := range data.AppBreakdown {
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "%s (%s)", app.AppName, formatDurationMs(app.ActiveMs))
		}
		sb.WriteString("\n")
	}

	if len(data.TopWindowTitles) > 0 {
		fmt.Fprintf(&sb, "Frequent window titles: %s\n", strings.Join(data.TopWindowTitles, " | "))
	}

	peakHour, peakMin := 0, 0
	for h, m := range data.HourlyActive {
		if m > peakMin {
			peakMin = m
			peakHour = h
		}
	}
	if peakMin > 0 {
		fmt.Fprintf(&sb, "Most active hour: %d:00-%d:00\n", peakHour, peakHour+1)
	}

	// Pull rich per-source context. Order matters: highest-value first so
	// they get the prompt-byte budget if we hit the cap.
	t, _ := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	startMs := t.UnixMilli()
	endMs := t.AddDate(0, 0, 1).UnixMilli()

	sessions := data.modelSessions
	if data.modelSourceIDs == nil && a.activity != nil && a.activity.store != nil {
		sessions, _ = a.activity.store.modelSessionsOverlapping(startMs, endMs)
	}

	// Append in priority order; stop if we hit the cap.
	appendSection := func(section string) {
		if section == "" {
			return
		}
		if sb.Len()+len(section) > maxPromptBytes {
			return
		}
		sb.WriteString(section)
	}

	if data.modelSources != nil {
		appendSection(data.modelSources.MeetingContext)
	} else {
		appendSection(a.loadDailyMeetings(sessions))
	}
	if data.modelSources != nil {
		appendSection(data.modelSources.KnowledgeContext)
		appendSection(data.modelSources.MemoryContext)
	} else {
		appendSection(a.loadDailyKnowledge(dateStr))
		appendSection(a.loadDailyMemories(startMs, endMs))
	}
	appendSection(a.loadDailyOCRSamples(sessions))
	appendSection(a.loadDailyClickHighlights(sessions))
	appendSection(a.loadDailySpeech(sessions))

	sb.WriteString("\n\nWrite the summary now:")
	return redactActivityModelText(sb.String())
}

func buildWeeklySummaryPrompt(data *WeeklySummaryData, languageInstruction string) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, `%s

Write a detailed summary of your work week, written to you. Use multiple paragraphs, as long as needed to cover the data thoroughly.

Structure:
1. Overview: what they primarily focused on this week. Use daily summaries (if provided) to identify recurring themes, major projects, or evolving tasks across the week. Be specific about project names, features, or areas of work.
2. Day-by-day narrative: briefly walk through how the week unfolded -- which days were most and least active, how effort was distributed, and whether there was a ramp-up or wind-down pattern. Mention specific days by name (e.g. "Tuesday was the most productive day at 4h 30m").
3. Productivity trends: compare against last week if data is available. Note whether overall activity increased or decreased and speculate on possible reasons based on the app usage and daily summaries.
4. Tools and focus areas: describe which applications dominated the week and what that suggests about the nature of the work (coding, writing, research, communication, etc.).
5. Closing: an overall characterization of the week and any notable patterns (e.g. "consistent morning starts", "heavy end-of-week push", "balanced and steady throughout").

Tone: warm and observant, like a thoughtful colleague giving a thorough weekly debrief. No emoji, no markdown, no bullet points -- write in flowing prose paragraphs.

%s

`, languageInstruction, activitySecondPersonRule)

	fmt.Fprintf(&sb, "Week: %s to %s\n", data.WeekStart, data.WeekEnd)
	fmt.Fprintf(&sb, "Total active time: %s\n", formatDurationMs(data.TotalActiveMs))
	fmt.Fprintf(&sb, "Daily average: %s\n", formatDurationMs(data.DailyAvgMs))

	if data.PrevWeekMs > 0 {
		change := float64(data.TotalActiveMs-data.PrevWeekMs) / float64(data.PrevWeekMs) * 100
		fmt.Fprintf(&sb, "vs last week: %+.0f%%\n", change)
	}

	// Find most and least active days
	var bestDay, worstDay string
	var bestMs, worstMs int64
	worstMs = int64(^uint64(0) >> 1) // max int64
	for _, d := range data.DailyStats {
		if d.ActiveMs > bestMs {
			bestMs = d.ActiveMs
			bestDay = d.Weekday
		}
		if d.ActiveMs > 0 && d.ActiveMs < worstMs {
			worstMs = d.ActiveMs
			worstDay = d.Weekday
		}
	}
	if bestDay != "" {
		fmt.Fprintf(&sb, "Most active day: %s (%s)\n", bestDay, formatDurationMs(bestMs))
	}
	if worstDay != "" && worstDay != bestDay {
		fmt.Fprintf(&sb, "Least active day: %s (%s)\n", worstDay, formatDurationMs(worstMs))
	}

	sb.WriteString("Daily breakdown:\n")
	for _, d := range data.DailyStats {
		if d.ActiveMs > 0 {
			fmt.Fprintf(&sb, "  %s: %s (%d sessions)\n", d.Weekday, formatDurationMs(d.ActiveMs), d.SessionCount)
		}
	}

	if len(data.TopApps) > 0 {
		sb.WriteString("Top applications: ")
		for i, app := range data.TopApps {
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "%s (%s)", app.AppName, formatDurationMs(app.ActiveMs))
		}
		sb.WriteString("\n")
	}

	// Include cached daily summaries if available (much richer context)
	if len(data.dailySummaries) > 0 {
		sb.WriteString("\nDaily summaries (already generated):\n")
		for _, ds := range data.dailySummaries {
			fmt.Fprintf(&sb, "  %s\n", ds)
		}
	}

	sb.WriteString("\nWrite the summary now:")
	return redactActivityModelText(sb.String())
}

// ──────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────

func formatDurationMs(ms int64) string {
	if ms <= 0 {
		return "0m"
	}
	totalMin := ms / 60000
	if totalMin < 60 {
		return fmt.Sprintf("%dm", totalMin)
	}
	h := totalMin / 60
	m := totalMin % 60
	if m > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dh", h)
}
