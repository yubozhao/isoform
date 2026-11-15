package main

import (
	"errors"
	"strings"
)

// The first-run journey (removed 2026-10-13) excluded its guided tutorial
// task from learning. The rows in first_run_exclusions_v1 stay: work hours
// and task context keep leaving that tutorial out.

// firstRunContaminationReason explains why a guided first-run source was left
// out of learning.
const firstRunContaminationReason = "guided first-outcome source is excluded from Shadow mode learning"

// FirstRunExclusionGuided marks the first-run guided tutorial task.
const FirstRunExclusionGuided = "guided_tutorial"

type FirstRunExclusionRecord struct {
	ID         string `json:"id"`
	JourneyID  string `json:"journeyId"`
	RunID      string `json:"runId"`
	SourceKind string `json:"sourceKind"`
	SourceID   string `json:"sourceId"`
	Reason     string `json:"reason"`
	CreatedAt  int64  `json:"createdAt"`
}

func (s *ActivityStore) IsFirstRunLearningExcluded(
	sourceKind, sourceID string,
) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("activity store is unavailable")
	}
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(1)
		FROM first_run_exclusions_v1
		WHERE source_kind = ? AND source_id = ?`,
		strings.TrimSpace(sourceKind),
		strings.TrimSpace(sourceID),
	).Scan(&count)
	return count > 0, err
}

func (s *ActivityStore) ListFirstRunExclusions(
	runID string,
) ([]FirstRunExclusionRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	rows, err := s.db.Query(`
		SELECT id, journey_id, run_id, source_kind, source_id, reason, created_at
		FROM first_run_exclusions_v1
		WHERE (? = '' OR run_id = ?)
		ORDER BY created_at ASC, id ASC`,
		strings.TrimSpace(runID),
		strings.TrimSpace(runID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]FirstRunExclusionRecord, 0)
	for rows.Next() {
		var item FirstRunExclusionRecord
		if err := rows.Scan(
			&item.ID,
			&item.JourneyID,
			&item.RunID,
			&item.SourceKind,
			&item.SourceID,
			&item.Reason,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
