package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// What the user told shadow learning before the first-run journey and the
// Desk were removed on 2026-10-13.
//
// The first-run journey excluded its guided tutorial task from learning, and
// the Desk let the user correct a learned pattern. Nothing records either any
// more, but the rows stay: shadow learning keeps skipping excluded sources,
// memory recall keeps hiding corrected memories, and a corrected statement
// replaces the learned one wherever learned patterns are shown.

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

type AgencyLearningCorrectionRecord struct {
	ID                 string `json:"id"`
	IdempotencyKey     string `json:"idempotencyKey"`
	JourneyID          string `json:"journeyId,omitempty"`
	CapabilityKey      string `json:"capabilityKey"`
	LearningRevisionID string `json:"learningRevisionId"`
	OriginalStatement  string `json:"originalStatement"`
	Correction         string `json:"correction"`
	PayloadJSON        string `json:"payloadJson"`
	PayloadHash        string `json:"payloadHash"`
	CreatedAt          int64  `json:"createdAt"`
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

func scanAgencyLearningCorrection(
	scanner interface{ Scan(...any) error },
) (AgencyLearningCorrectionRecord, error) {
	var item AgencyLearningCorrectionRecord
	err := scanner.Scan(
		&item.ID,
		&item.IdempotencyKey,
		&item.JourneyID,
		&item.CapabilityKey,
		&item.LearningRevisionID,
		&item.OriginalStatement,
		&item.Correction,
		&item.PayloadJSON,
		&item.PayloadHash,
		&item.CreatedAt,
	)
	return item, err
}

func (s *ActivityStore) ListAgencyLearningCorrections(
	capabilityKey string,
	limit int,
) ([]AgencyLearningCorrectionRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.Query(`
		SELECT id, idempotency_key, journey_id, capability_key,
			learning_revision_id, original_statement, correction, payload_json,
			payload_hash, created_at
		FROM agency_learning_corrections_v1
		WHERE (? = '' OR capability_key = ?)
		ORDER BY created_at DESC, id DESC
		LIMIT ?`,
		strings.TrimSpace(capabilityKey),
		strings.TrimSpace(capabilityKey),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AgencyLearningCorrectionRecord, 0)
	for rows.Next() {
		item, err := scanAgencyLearningCorrection(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ActivityStore) HasAgencyLearningCorrection(
	revisionID, capabilityKey, statement string,
) (bool, error) {
	return s.HasAgencyLearningCorrectionAt(
		revisionID,
		capabilityKey,
		statement,
		time.Now().UnixMilli(),
	)
}

func (s *ActivityStore) HasAgencyLearningCorrectionAt(
	revisionID, capabilityKey, statement string,
	cutoffAt int64,
) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("activity store is unavailable")
	}
	if cutoffAt <= 0 {
		return false, errors.New("learning correction lookup requires a cutoff")
	}
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(1)
		FROM agency_learning_corrections_v1
		WHERE (learning_revision_id = ? OR capability_key = ?)
			AND original_statement = ?
			AND created_at <= ?`,
		strings.TrimSpace(revisionID),
		normalizeShadowCapabilityKey(capabilityKey, ""),
		strings.TrimSpace(statement),
		cutoffAt,
	).Scan(&count)
	return count > 0, err
}

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
		ShadowTaskLearningTargetMemory,
		ShadowTaskLearningTargetKnowledge,
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

// learnedPattern is one pattern shadow learning supports for a kind of work,
// or the user's correction of it.
type learnedPattern struct {
	ID            string
	CapabilityKey string
	Statement     string
	Uncertainty   string
	LastAt        int64
}

// learnedPatterns lists, per kind of work, the best supported pattern shadow
// learning holds, with the user's latest correction in its place when there
// is one. Kinds with nothing supported yet are left out.
func (s *ActivityStore) learnedPatterns() ([]learnedPattern, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	evaluations, err := s.ListShadowTaskLearningEvaluationRecords("")
	if err != nil {
		return nil, err
	}
	revisions, err := s.ListShadowTaskLearningRevisions("", "", 500)
	if err != nil {
		return nil, err
	}
	corrections, err := s.ListAgencyLearningCorrections("", 500)
	if err != nil {
		return nil, err
	}
	type accumulator struct {
		comparisons int
		lastAt      int64
		candidate   *ShadowTaskLearningRevision
		correction  *AgencyLearningCorrectionRecord
	}
	byCapability := make(map[string]*accumulator)
	ensure := func(key string) *accumulator {
		key = normalizeShadowCapabilityKey(key, "")
		if key == "" {
			key = "observed_work"
		}
		if item := byCapability[key]; item != nil {
			return item
		}
		item := &accumulator{}
		byCapability[key] = item
		return item
	}
	for _, record := range evaluations {
		if record.ExclusionReason != "" {
			continue
		}
		item := ensure(record.CapabilityKey)
		item.comparisons++
		if record.PersistedAt > item.lastAt {
			item.lastAt = record.PersistedAt
		}
	}
	for _, revision := range revisions {
		item := ensure(revision.Candidate.CapabilityKey)
		if revision.CreatedAt > item.lastAt {
			item.lastAt = revision.CreatedAt
		}
		if learnedPatternCandidateUseful(revision.Candidate) &&
			learnedPatternRevisionPreferred(item.candidate, revision) {
			copy := revision
			item.candidate = &copy
		}
	}
	for i := range corrections {
		correction := corrections[i]
		item := ensure(correction.CapabilityKey)
		if item.correction == nil ||
			correction.CreatedAt > item.correction.CreatedAt {
			copy := correction
			item.correction = &copy
		}
		if correction.CreatedAt > item.lastAt {
			item.lastAt = correction.CreatedAt
		}
	}
	patterns := make([]learnedPattern, 0, len(byCapability))
	for key, item := range byCapability {
		pattern := learnedPattern{CapabilityKey: key, LastAt: item.lastAt}
		switch {
		case item.correction != nil:
			pattern.ID = item.correction.LearningRevisionID
			pattern.Statement = strings.TrimSpace(item.correction.Correction)
			pattern.Uncertainty = "The corrected statement is explicit, but future behavior still needs independent evaluation."
		case item.candidate != nil:
			pattern.ID = item.candidate.Candidate.RevisionID
			pattern.Statement = strings.TrimSpace(item.candidate.Candidate.Statement)
			pattern.Uncertainty = learnedPatternUncertainty(item.comparisons, item.candidate)
		default:
			continue
		}
		if pattern.Statement == "" {
			continue
		}
		if pattern.ID == "" {
			pattern.ID = "learning:" + sha256Hex([]byte(key))[:16]
		}
		pattern.Statement = agencyBoundedText(pattern.Statement, 900)
		patterns = append(patterns, pattern)
	}
	sort.Slice(patterns, func(i, j int) bool {
		if patterns[i].LastAt != patterns[j].LastAt {
			return patterns[i].LastAt > patterns[j].LastAt
		}
		return patterns[i].CapabilityKey < patterns[j].CapabilityKey
	})
	return patterns, nil
}

func learnedPatternCandidateUseful(candidate ShadowTaskLearningCandidate) bool {
	if candidate.AuthorityEffect != ShadowTaskLearningAuthorityNone ||
		candidate.Qualification.Decision != ShadowTaskLearningDecisionQualified ||
		strings.TrimSpace(candidate.Statement) == "" {
		return false
	}
	switch candidate.TargetKind {
	case ShadowTaskLearningTargetMemory,
		ShadowTaskLearningTargetKnowledge,
		ShadowTaskLearningTargetTypedSelf:
		return true
	default:
		return false
	}
}

func learnedPatternRevisionPreferred(
	current *ShadowTaskLearningRevision,
	next ShadowTaskLearningRevision,
) bool {
	if current == nil {
		return true
	}
	left := len(current.Candidate.Evidence)
	right := len(next.Candidate.Evidence)
	if right != left {
		return right > left
	}
	if next.CreatedAt != current.CreatedAt {
		return next.CreatedAt > current.CreatedAt
	}
	return next.Candidate.RevisionID > current.Candidate.RevisionID
}

func learnedPatternUncertainty(
	comparisons int,
	candidate *ShadowTaskLearningRevision,
) string {
	switch {
	case comparisons == 0:
		return "Isoform has not completed an independent comparison for this work yet."
	case candidate.Candidate.LearningKind == ShadowTaskLearningKindReasoning:
		return "The reasoning pattern is supported only by source-backed reasoning evidence."
	default:
		return "Behavior can show what happened; the user's reasoning remains unknown unless it is directly supported."
	}
}
