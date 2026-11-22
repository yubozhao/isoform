package storage

import (
	"context"
	"errors"
)

// ListMCPRunMetadata intentionally excludes result summaries, prompts, stream
// logs and errors. Large automation outputs never enter a history read.
func (s *Store) ListMCPRunMetadata(ctx context.Context, startMs, endMs int64, limit int) ([]CronRunRow, error) {
	if limit <= 0 {
		return nil, errors.New("run metadata limit must be positive")
	}
	rows, err := s.committedReader().QueryContext(ctx, `SELECT id,job_id,started_at,finished_at,status
		FROM cron_runs WHERE started_at>=? AND started_at<? AND status!='missed'
		ORDER BY started_at DESC,id LIMIT ?`, startMs, endMs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronRunRow
	for rows.Next() {
		var row CronRunRow
		if err := rows.Scan(&row.ID, &row.JobID, &row.StartedAt, &row.FinishedAt, &row.Status); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ReadMCPRun resolves one evidence reference without fetching stream logs or
// starting a scheduler. Callers decide which of its fields may cross MCP.
func (s *Store) ReadMCPRun(ctx context.Context, id string) (CronRunRow, error) {
	var row CronRunRow
	err := s.committedReader().QueryRowContext(ctx, `SELECT id, job_id, started_at, finished_at, status, result_summary
		FROM cron_runs WHERE id = ? AND length(result_summary) <= 1048576`, id).
		Scan(&row.ID, &row.JobID, &row.StartedAt, &row.FinishedAt, &row.Status, &row.ResultSummary)
	return row, err
}
