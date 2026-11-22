package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// SaveCronJob inserts or updates a cron job.
func (s *Store) SaveCronJob(ctx context.Context, job CronJobRow) error {
	now := time.Now().UnixMilli()
	if job.CreatedAt == 0 {
		job.CreatedAt = now
	}
	job.UpdatedAt = now

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cron_jobs (id, name, description, enabled, schedule_kind, schedule_config, schedule_evidence,
			target_kind, target_config, delete_after_run, retry_policy,
			next_run_at, last_run_at, last_run_status, last_error,
			consecutive_errors, retry_attempt, retry_run_at, source_session_id, source_platform, fingerprint, notify_dm, notify_platform, surface_kind, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			enabled = excluded.enabled,
			schedule_kind = excluded.schedule_kind,
			schedule_config = excluded.schedule_config,
			schedule_evidence = excluded.schedule_evidence,
			target_kind = excluded.target_kind,
			target_config = excluded.target_config,
			delete_after_run = excluded.delete_after_run,
			retry_policy = excluded.retry_policy,
			next_run_at = excluded.next_run_at,
			last_run_at = excluded.last_run_at,
			last_run_status = excluded.last_run_status,
			last_error = excluded.last_error,
			consecutive_errors = excluded.consecutive_errors,
			retry_attempt = excluded.retry_attempt,
			retry_run_at = excluded.retry_run_at,
			source_session_id = excluded.source_session_id,
			source_platform = excluded.source_platform,
			fingerprint = excluded.fingerprint,
			notify_dm = excluded.notify_dm,
			notify_platform = excluded.notify_platform,
			surface_kind = excluded.surface_kind,
			sort_order = excluded.sort_order,
			updated_at = excluded.updated_at
	`, job.ID, job.Name, job.Description, job.Enabled, job.ScheduleKind, job.ScheduleConfig, job.ScheduleEvidence,
		job.TargetKind, job.TargetConfig, job.DeleteAfterRun, job.RetryPolicy,
		job.NextRunAt, job.LastRunAt, job.LastRunStatus, job.LastError,
		job.ConsecutiveErrors, job.RetryAttempt, job.RetryRunAt, job.SourceSessionID, job.SourcePlatform, job.Fingerprint, job.NotifyDM, job.NotifyPlatform, job.SurfaceKind, job.SortOrder, job.CreatedAt, job.UpdatedAt)
	return err
}

// LoadCronJob returns a single cron job by ID.
func (s *Store) LoadCronJob(ctx context.Context, id string) (*CronJobRow, error) {
	var job CronJobRow
	err := s.committedReader().QueryRowContext(ctx, `
		SELECT id, name, description, enabled, schedule_kind, schedule_config, schedule_evidence,
			target_kind, target_config, delete_after_run, retry_policy,
			next_run_at, last_run_at, last_run_status, last_error,
			consecutive_errors, retry_attempt, retry_run_at, source_session_id, source_platform, fingerprint, notify_dm, notify_platform, surface_kind, sort_order, created_at, updated_at
		FROM cron_jobs WHERE id = ?
	`, id).Scan(&job.ID, &job.Name, &job.Description, &job.Enabled, &job.ScheduleKind, &job.ScheduleConfig, &job.ScheduleEvidence,
		&job.TargetKind, &job.TargetConfig, &job.DeleteAfterRun, &job.RetryPolicy,
		&job.NextRunAt, &job.LastRunAt, &job.LastRunStatus, &job.LastError,
		&job.ConsecutiveErrors, &job.RetryAttempt, &job.RetryRunAt, &job.SourceSessionID, &job.SourcePlatform, &job.Fingerprint, &job.NotifyDM, &job.NotifyPlatform, &job.SurfaceKind, &job.SortOrder, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// ListCronJobs returns all cron jobs ordered by sort_order then creation time.
func (s *Store) ListCronJobs(ctx context.Context) ([]CronJobRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, name, description, enabled, schedule_kind, schedule_config, schedule_evidence,
			target_kind, target_config, delete_after_run, retry_policy,
			next_run_at, last_run_at, last_run_status, last_error,
			consecutive_errors, retry_attempt, retry_run_at, source_session_id, source_platform, fingerprint, notify_dm, notify_platform, surface_kind, sort_order, created_at, updated_at
		FROM cron_jobs ORDER BY sort_order ASC, created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CronJobRow
	for rows.Next() {
		var job CronJobRow
		if err := rows.Scan(&job.ID, &job.Name, &job.Description, &job.Enabled, &job.ScheduleKind, &job.ScheduleConfig, &job.ScheduleEvidence,
			&job.TargetKind, &job.TargetConfig, &job.DeleteAfterRun, &job.RetryPolicy,
			&job.NextRunAt, &job.LastRunAt, &job.LastRunStatus, &job.LastError,
			&job.ConsecutiveErrors, &job.RetryAttempt, &job.RetryRunAt, &job.SourceSessionID, &job.SourcePlatform, &job.Fingerprint, &job.NotifyDM, &job.NotifyPlatform, &job.SurfaceKind, &job.SortOrder, &job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

// UpdateCronJob updates specific fields on a cron job by ID.
// The fields map keys must be valid column names.
func (s *Store) UpdateCronJob(ctx context.Context, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	fields["updated_at"] = time.Now().UnixMilli()

	setClauses := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+1)
	for col, val := range fields {
		setClauses = append(setClauses, col+" = ?")
		args = append(args, val)
	}
	args = append(args, id)

	query := fmt.Sprintf("UPDATE cron_jobs SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// DeleteCronJob removes a cron job and its runs (via ON DELETE CASCADE).
// Also cleans up sidebar group references.
func (s *Store) DeleteCronJob(ctx context.Context, id string) error {
	if err := s.RemoveGroupItemsByRef(ctx, "automation", id); err != nil {
		return fmt.Errorf("clean group items: %w", err)
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM cron_jobs WHERE id = ?", id)
	return err
}

// InsertCronRun inserts a new cron run record.
func (s *Store) InsertCronRun(ctx context.Context, run CronRunRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cron_runs (id, job_id, started_at, finished_at, status, trigger_kind, error, duration_ms, result_summary, session_tag, item_sort_start, notify_result, candidate_backends, backend, fallback_from, backend_reason, model, actions_started)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, run.ID, run.JobID, run.StartedAt, run.FinishedAt, run.Status, run.TriggerKind, run.Error, run.DurationMs, run.ResultSummary, run.SessionTag, run.ItemSortStart, run.NotifyResult, run.CandidateBackends, run.Backend, run.FallbackFrom, run.BackendReason, run.Model, run.ActionsStarted)
	return err
}

// UpdateCronRun updates specific fields on a cron run by ID.
func (s *Store) UpdateCronRun(ctx context.Context, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}

	setClauses := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+1)
	for col, val := range fields {
		setClauses = append(setClauses, col+" = ?")
		args = append(args, val)
	}
	args = append(args, id)

	query := fmt.Sprintf("UPDATE cron_runs SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// FinalizeCronRun commits completion and the schedule's retry state together.
// If either update fails, the run stays running so crash recovery can prevent
// replay of an assistant action with an uncertain outcome.
func (s *Store) FinalizeCronRun(ctx context.Context, runID, jobID string, runFields, jobFields map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, update := range []struct {
		table  string
		id     string
		fields map[string]any
	}{{"cron_runs", runID, runFields}, {"cron_jobs", jobID, jobFields}} {
		if len(update.fields) == 0 {
			continue
		}
		clauses := make([]string, 0, len(update.fields))
		args := make([]any, 0, len(update.fields)+1)
		for column, value := range update.fields {
			clauses = append(clauses, column+" = ?")
			args = append(args, value)
		}
		args = append(args, update.id)
		if _, err := tx.ExecContext(ctx, "UPDATE "+update.table+" SET "+strings.Join(clauses, ", ")+" WHERE id = ?", args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListCronRuns returns the most recent runs for a job, ordered newest first.
func (s *Store) AttachLogsToLatestRunningCronRun(ctx context.Context, jobID, sessionTag string, itemSortStart int) error {
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(sessionTag) == "" || itemSortStart < 0 {
		return nil
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE cron_runs
		SET session_tag = ?, item_sort_start = ?
		WHERE id = (
			SELECT id FROM cron_runs
			WHERE job_id = ? AND status = 'running'
			ORDER BY started_at DESC LIMIT 1
		)
	`, sessionTag, itemSortStart, jobID)
	return err
}

func (s *Store) ListCronRuns(ctx context.Context, jobID string, limit int) ([]CronRunRow, error) {
	rows, err := s.committedReader().QueryContext(ctx, `
		SELECT id, job_id, started_at, finished_at, status, trigger_kind, error, duration_ms, result_summary, session_tag, item_sort_start, notify_result, candidate_backends, backend, fallback_from, backend_reason, model, actions_started
		FROM cron_runs WHERE job_id = ? ORDER BY started_at DESC LIMIT ?
	`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CronRunRow
	for rows.Next() {
		var run CronRunRow
		if err := rows.Scan(&run.ID, &run.JobID, &run.StartedAt, &run.FinishedAt, &run.Status, &run.TriggerKind, &run.Error, &run.DurationMs, &run.ResultSummary, &run.SessionTag, &run.ItemSortStart, &run.NotifyResult, &run.CandidateBackends, &run.Backend, &run.FallbackFrom, &run.BackendReason, &run.Model, &run.ActionsStarted); err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

// LoadStreamItemsForCronRun loads stream items that belong to a specific cron run.
// It uses the session_tag and item_sort_start from the run record, and finds the
// upper bound from the next run's item_sort_start (or all remaining items).
func (s *Store) LoadStreamItemsForCronRun(ctx context.Context, sessionTag string, sortStart, sortEnd int) ([]StreamItemRow, error) {
	var query string
	var args []any

	if sortEnd > 0 {
		query = `
			SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
			FROM stream_items WHERE session_local_id = ? AND sort_order >= ? AND sort_order < ?
			ORDER BY sort_order ASC
		`
		args = []any{sessionTag, sortStart, sortEnd}
	} else {
		query = `
			SELECT id, session_local_id, kind, content, tool_name, tool_input, tool_use_id, run_id, session_id, sort_order, created_at, images, automation_context, files
			FROM stream_items WHERE session_local_id = ? AND sort_order >= ?
			ORDER BY sort_order ASC
		`
		args = []any{sessionTag, sortStart}
	}

	rows, err := s.committedReader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []StreamItemRow
	for rows.Next() {
		var item StreamItemRow
		if err := rows.Scan(&item.ID, &item.SessionLocalID, &item.Kind, &item.Content, &item.ToolName, &item.ToolInput, &item.ToolUseID, &item.RunID, &item.SessionID, &item.SortOrder, &item.CreatedAt, &item.Images, &item.AutomationContext, &item.Files); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// GetNextCronRunSortStart returns the item_sort_start of the run that started
// immediately after the given run (same job), or -1 if no such run exists.
func (s *Store) GetNextCronRunSortStart(ctx context.Context, jobID, runID string, startedAt int64) (int, error) {
	var sortStart int
	err := s.committedReader().QueryRowContext(ctx, `
		SELECT item_sort_start FROM cron_runs
		WHERE job_id = ? AND started_at > ? AND item_sort_start >= 0
		ORDER BY started_at ASC LIMIT 1
	`, jobID, startedAt).Scan(&sortStart)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil // no next run found, use -1 as sentinel
	}
	if err != nil {
		return -1, fmt.Errorf("query next cron run sort start: %w", err)
	}
	return sortStart, nil
}

// ListCronRunsByDateRange returns all runs across all jobs within a time range.
// Missed times (status "missed") are scheduled times that passed without a
// run, not runs, so the calendar, timeline and deliverables never see them;
// a job's own history (ListCronRuns) keeps them.
func (s *Store) ListCronRunsByDateRange(ctx context.Context, startMs, endMs int64, limit int) ([]CronRunRow, error) {
	query := `
		SELECT id, job_id, started_at, finished_at, status, trigger_kind, error, duration_ms, result_summary, session_tag, item_sort_start, notify_result, candidate_backends, backend, fallback_from, backend_reason, model, actions_started
		FROM cron_runs
		WHERE started_at >= ? AND started_at < ? AND status != 'missed'
		ORDER BY started_at DESC`
	var args []interface{}
	args = append(args, startMs, endMs)
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.committedReader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CronRunRow
	for rows.Next() {
		var run CronRunRow
		if err := rows.Scan(&run.ID, &run.JobID, &run.StartedAt, &run.FinishedAt, &run.Status, &run.TriggerKind, &run.Error, &run.DurationMs, &run.ResultSummary, &run.SessionTag, &run.ItemSortStart, &run.NotifyResult, &run.CandidateBackends, &run.Backend, &run.FallbackFrom, &run.BackendReason, &run.Model, &run.ActionsStarted); err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

// PruneCronRuns deletes all but the most recent `keep` runs for a job.
func (s *Store) PruneCronRuns(ctx context.Context, jobID string, keep int) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM cron_runs
		WHERE job_id = ? AND id NOT IN (
			SELECT id FROM cron_runs WHERE job_id = ? ORDER BY started_at DESC LIMIT ?
		)
	`, jobID, jobID, keep)
	return err
}

// MarkStaleRunsAsError closes runs interrupted by a crash. Action counts are
// persisted before dispatch, so zero-action attempts are safe to retry. Runs
// with actions block that occurrence without turning off recurring schedules.
func (s *Store) MarkStaleRunsAsError(ctx context.Context) error {
	now := time.Now().UnixMilli()
	const staleError = "interrupted by app restart"
	const blockedError = "interrupted by app restart; run outcome unknown; automatic retry is blocked"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
        SELECT j.id, j.enabled, j.surface_kind, j.schedule_kind, j.schedule_config,
            j.next_run_at, j.last_run_at,
            MAX(r.started_at)
        FROM cron_jobs j JOIN cron_runs r ON r.job_id = j.id
        WHERE r.status = 'running' AND r.actions_started > 0
        GROUP BY j.id
    `)
	if err != nil {
		return err
	}
	type interruptedJob struct {
		job       CronJobRow
		startedAt int64
	}
	var interrupted []interruptedJob
	for rows.Next() {
		var item interruptedJob
		if err := rows.Scan(&item.job.ID, &item.job.Enabled, &item.job.SurfaceKind,
			&item.job.ScheduleKind, &item.job.ScheduleConfig, &item.job.NextRunAt,
			&item.job.LastRunAt, &item.startedAt); err != nil {
			rows.Close()
			return err
		}
		interrupted = append(interrupted, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
        UPDATE cron_jobs SET last_run_status = 'error', last_error = ?,
            consecutive_errors = consecutive_errors + 1
        WHERE id IN (SELECT DISTINCT job_id FROM cron_runs WHERE status = 'running')
            AND id NOT IN (
                SELECT DISTINCT job_id FROM cron_runs WHERE status = 'running' AND actions_started > 0
            )
    `, staleError); err != nil {
		return err
	}

	// Keep the run and the schedule change atomic. Advance only the occurrence
	// that was already executing; a newer occurrence can still catch up later.
	for _, item := range interrupted {
		job := item.job
		next := job.NextRunAt
		reason := blockedError
		switch job.ScheduleKind {
		case "at":
			next = 0
			if job.SurfaceKind != "system" {
				job.Enabled = false
			}
		case "manual":
			next = 0
		default:
			through := item.startedAt
			if through <= 0 {
				through = now
			}
			if next <= through {
				var err error
				next, err = nextCronOccurrenceAfter(job, time.UnixMilli(through))
				if err != nil {
					// Invalid configuration cannot safely identify the next
					// occurrence. Retain the enabled choice but do not replay.
					next = 0
					reason += "; next occurrence unavailable: " + err.Error()
				}
			}
			if through > job.LastRunAt {
				job.LastRunAt = through
			}
		}
		if _, err := tx.ExecContext(ctx, `
            UPDATE cron_jobs SET last_run_status = 'blocked', last_error = ?,
                consecutive_errors = consecutive_errors + 1,
                enabled = ?, next_run_at = ?, last_run_at = ?, retry_attempt = 0, retry_run_at = 0
            WHERE id = ?
        `, reason, job.Enabled, next, job.LastRunAt, job.ID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
        UPDATE cron_runs SET status = 'blocked', error = ?, finished_at = ?
        WHERE status = 'running' AND actions_started > 0
    `, blockedError, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
        UPDATE cron_runs SET status = 'error', error = ?, finished_at = ?
        WHERE status = 'running'
    `, staleError, now); err != nil {
		return err
	}
	return tx.Commit()
}

// nextCronOccurrenceAfter advances a recurring schedule beyond the interrupted
// occurrence. It intentionally uses the run's start, not startup time: later
// missed occurrences remain eligible for ordinary scheduler catch-up.
func nextCronOccurrenceAfter(job CronJobRow, after time.Time) (int64, error) {
	switch job.ScheduleKind {
	case "every":
		var config struct {
			EveryMs int64 `json:"every_ms"`
		}
		if err := json.Unmarshal([]byte(job.ScheduleConfig), &config); err != nil {
			return 0, err
		}
		if config.EveryMs <= 0 {
			return 0, fmt.Errorf("every_ms must be positive")
		}
		anchor := job.NextRunAt
		if anchor <= 0 {
			anchor = after.UnixMilli()
		}
		periods := (after.UnixMilli()-anchor)/config.EveryMs + 1
		next := anchor + periods*config.EveryMs
		if next <= after.UnixMilli() {
			return 0, fmt.Errorf("schedule interval overflows")
		}
		return next, nil
	case "cron":
		var config struct {
			Expr string `json:"expr"`
			TZ   string `json:"tz"`
		}
		if err := json.Unmarshal([]byte(job.ScheduleConfig), &config); err != nil {
			return 0, err
		}
		schedule, err := ParseCronExpr(config.Expr, config.TZ)
		if err != nil {
			return 0, err
		}
		next := schedule.Next(after)
		if next.IsZero() {
			return 0, fmt.Errorf("schedule has no future occurrence")
		}
		return next.UnixMilli(), nil
	default:
		return 0, fmt.Errorf("unsupported schedule kind %q", job.ScheduleKind)
	}
}

// ParseCronExpr is the shared parser used by the live scheduler and startup
// recovery, including the same timezone and descriptor handling.
func ParseCronExpr(expr, tz string) (cron.Schedule, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if tz != "" {
		expr = fmt.Sprintf("CRON_TZ=%s %s", tz, expr)
		parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	}
	schedule, err := parser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("parse cron expression %q: %w", expr, err)
	}
	return schedule, nil
}
