package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"wails-gui/internal/platform/activitymon"
)

// Agent runs in the derived Activity layers.
//
// A timeline run is one stretch in one app. When every event in it shows a
// coding agent at work (the Claude or Codex app, or a terminal whose window
// names Claude Code or Codex, and no speech), the run is agent work: it gets
// no model summary, so the composer never turns it into one of the person's
// moments and threads never group it. It is recorded in activity_agent_runs
// instead, and the day shows it only as one labelled "your agents" line.
// Moments composed before this rule, whose every run is an agent run, are
// left out of the day's threads too.

// ActivityAgentDay is the labelled agent line for one day.
type ActivityAgentDay struct {
	Runs     int   `json:"runs"`
	ActiveMs int64 `json:"activeMs"`
}

func migrateActivityAgentRuns(tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS activity_agent_runs (
			run_id       TEXT PRIMARY KEY,
			day_start_ms INTEGER NOT NULL,
			start_ms     INTEGER NOT NULL,
			end_ms       INTEGER NOT NULL,
			active_ms    INTEGER NOT NULL DEFAULT 0,
			marked_at    INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_agent_runs_day ON activity_agent_runs(day_start_ms)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("activity_agent_runs: %w", err)
		}
	}
	return nil
}

// timelineEventsAreAgent reports a run whose every app event is a coding
// agent at work. Speech is the person talking, so any speech keeps the run
// the person's.
func timelineEventsAreAgent(events []ActivityTimelineEvent) bool {
	seen := false
	for _, evt := range events {
		if evt.Kind == activitymon.EventSpeech {
			return false
		}
		if evt.AppBundleID == "" && evt.AppName == "" {
			continue
		}
		seen = true
		if !activityEventIsAgent(evt.ActivityEvent) {
			return false
		}
	}
	return seen
}

// activityEventIsAgent is the per-event rule shared with the triage split.
func activityEventIsAgent(evt activitymon.ActivityEvent) bool {
	return activityAgentApp(evt.AppBundleID, evt.AppName) ||
		(isTerminalAppFallback(evt.AppBundleID, evt.AppName, "") && activityAgentWindowTitle(evt.WindowTitle))
}

// MarkTimelineRunAgent records an agent run and parks its summary as
// skipped, so no model reads it. A later event from the person re-queues the
// run (event_count grows past the skipped watermark) and it is checked again.
func (s *ActivityStore) MarkTimelineRunAgent(run ActivityTimelineRunRow) error {
	if _, err := s.db.Exec(`
		INSERT INTO activity_agent_runs (run_id, day_start_ms, start_ms, end_ms, active_ms, marked_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			day_start_ms = excluded.day_start_ms, start_ms = excluded.start_ms,
			end_ms = excluded.end_ms, active_ms = excluded.active_ms, marked_at = excluded.marked_at`,
		run.ID, startOfDayLocal(run.StartMs), run.StartMs, run.EndMs, run.ActiveMs, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("mark timeline run %s agent: %w", run.ID, err)
	}
	return s.MarkTimelineRunSummarySkipped(run.ID, run.EventCount)
}

// ClearTimelineRunAgent forgets the agent mark once the person shows up in
// the run.
func (s *ActivityStore) ClearTimelineRunAgent(runID string) error {
	_, err := s.db.Exec(`DELETE FROM activity_agent_runs WHERE run_id = ?`, runID)
	return err
}

// agentRunDay counts the day's agent runs for the labelled line.
func (s *ActivityStore) agentRunDay(ctx context.Context, day int64) (ActivityAgentDay, error) {
	var out ActivityAgentDay
	err := s.reader().QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(active_ms), 0)
		FROM activity_agent_runs WHERE day_start_ms = ?`, day).Scan(&out.Runs, &out.ActiveMs)
	return out, err
}

// agentRunIDs returns the agent runs around one day (a moment may start the
// day before).
func (s *ActivityStore) agentRunIDs(ctx context.Context, day int64) (map[string]bool, error) {
	return s.agentRunIDsBetween(ctx, day-int64(24*time.Hour/time.Millisecond), activityThreadDayEnd(day))
}

// agentRunIDsBetween returns the agent runs that overlap [startMs, endMs].
func (s *ActivityStore) agentRunIDsBetween(ctx context.Context, startMs, endMs int64) (map[string]bool, error) {
	rows, err := s.reader().QueryContext(ctx, `SELECT run_id FROM activity_agent_runs WHERE end_ms >= ? AND start_ms <= ?`, startMs, endMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// withoutAgentMoments drops moments whose every member run is an agent run.
// A moment with no run list, or any run of the person's, stays.
func withoutAgentMoments(rows []ActivityRow, agentRuns map[string]bool) []ActivityRow {
	if len(agentRuns) == 0 {
		return rows
	}
	out := rows[:0]
	for _, row := range rows {
		if activityRowIsAgent(row, agentRuns) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func activityRowIsAgent(row ActivityRow, agentRuns map[string]bool) bool {
	var runIDs []string
	if json.Unmarshal([]byte(row.RunIDs), &runIDs) != nil || len(runIDs) == 0 {
		return false
	}
	for _, id := range runIDs {
		if !agentRuns[strings.TrimSpace(id)] {
			return false
		}
	}
	return true
}

// agentRunBackfillDays is how far back the one-time backfill checks runs
// that were summarized before agent runs were recognised.
const agentRunBackfillDays = 14

// backfillAgentTimelineRuns marks already-summarized runs from the last two
// weeks that were agent work, so their moments leave the person's threads.
// Summaries are kept; only the mark is added.
func (a *App) backfillAgentTimelineRuns(ctx context.Context) (int, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return 0, nil
	}
	store := a.activity.store
	since := time.Now().Add(-agentRunBackfillDays * 24 * time.Hour).UnixMilli()
	runs, err := store.timelineRunsSince(ctx, since)
	if err != nil {
		return 0, err
	}
	marked := 0
	for _, run := range runs {
		if ctx.Err() != nil {
			return marked, ctx.Err()
		}
		events, err := store.LoadTimelineRunEvents(run)
		if err != nil || !timelineEventsAreAgent(events) {
			continue
		}
		if _, err := store.db.ExecContext(ctx, `
			INSERT OR IGNORE INTO activity_agent_runs (run_id, day_start_ms, start_ms, end_ms, active_ms, marked_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			run.ID, startOfDayLocal(run.StartMs), run.StartMs, run.EndMs, run.ActiveMs, time.Now().UnixMilli()); err != nil {
			log.Printf("[agent-activity] backfill run not marked: %v", err)
			continue
		}
		marked++
	}
	if marked > 0 {
		for day := startOfDayLocal(since); day <= startOfDayLocal(time.Now().UnixMilli()); day += int64(24 * time.Hour / time.Millisecond) {
			activityThreadCacheForget(day)
		}
	}
	return marked, nil
}

// timelineRunsSince lists the runs from since to now.
func (s *ActivityStore) timelineRunsSince(ctx context.Context, since int64) ([]ActivityTimelineRunRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.GetTimelineRuns(100000, 0, since, time.Now().UnixMilli())
}
