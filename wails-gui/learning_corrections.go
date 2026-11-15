package main

import (
	"context"
	"errors"
	"strings"
)

// Corrections the user made to a learned memory or know-how entry. Learning
// wrote the entry (shadow_task_learning_materializations) and the user
// corrected it (agency_learning_corrections_v1); memory recall, search, Today
// chat and About you keep hiding those entries. Both tables are kept when the
// learning code goes, so the corrections stay honored.

const (
	correctedLearningTargetMemory    = "memory"
	correctedLearningTargetKnowledge = "knowledge"
)

// ListCorrectedAgencyLearningTargetIDs returns the IDs of the memory and
// know-how entries the user corrected.
func (s *ActivityStore) ListCorrectedAgencyLearningTargetIDs() (
	map[string]struct{},
	error,
) {
	return s.ListCorrectedAgencyLearningTargetIDsContext(context.Background())
}

func (s *ActivityStore) ListCorrectedAgencyLearningTargetIDsContext(ctx context.Context) (map[string]struct{}, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT materialization.target_id
		FROM shadow_task_learning_materializations materialization
		INNER JOIN agency_learning_corrections_v1 correction
			ON correction.learning_revision_id = materialization.revision_id
		WHERE materialization.status = 'materialized'
			AND materialization.target_id <> ''
			AND materialization.target_kind IN (?, ?)`,
		correctedLearningTargetMemory,
		correctedLearningTargetKnowledge,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id = strings.TrimSpace(id); id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids, rows.Err()
}
