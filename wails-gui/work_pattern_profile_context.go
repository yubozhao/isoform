package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	WorkPatternInfluenceNeutral = "neutral"
	WorkPatternInfluenceProfile = "profile"
)

type WorkPatternSemanticSignal struct {
	Ref           string                         `json:"ref"`
	Section       string                         `json:"section"`
	SourceKind    string                         `json:"sourceKind"`
	SourceID      string                         `json:"sourceId"`
	ObservedAt    int64                          `json:"observedAt"`
	Content       string                         `json:"content"`
	EvidenceClass string                         `json:"evidenceClass"`
	Relevance     float64                        `json:"relevance"`
	Truncated     bool                           `json:"truncated,omitempty"`
	Learning      *TaskContextLearningProvenance `json:"learning,omitempty"`
}

type WorkPatternCapabilitySignal struct {
	ProfileID          string                         `json:"profileId"`
	CapabilityKey      string                         `json:"capabilityKey"`
	Ready              bool                           `json:"ready"`
	Status             string                         `json:"status"`
	ActionReliability  *float64                       `json:"actionReliability,omitempty"`
	EvaluationCount    int                            `json:"evaluationCount"`
	QualifiedCount     int                            `json:"qualifiedCount"`
	UnknownReasoning   int                            `json:"unknownReasoning"`
	SourceContextCount int                            `json:"sourceContextCount"`
	CreatedAt          int64                          `json:"createdAt"`
	Learning           *TaskContextLearningProvenance `json:"learning,omitempty"`
}

type WorkPatternProfileCorrection struct {
	ID                string `json:"id"`
	IdempotencyKey    string `json:"idempotencyKey"`
	ProfileSnapshotID string `json:"profileSnapshotId"`
	ScopeKind         string `json:"scopeKind"`
	ScopeKey          string `json:"scopeKey"`
	Revision          int    `json:"revision"`
	OriginalStatement string `json:"originalStatement"`
	Correction        string `json:"correction"`
	CreatedAt         int64  `json:"createdAt"`
}

type RecordWorkPatternProfileCorrectionInput struct {
	IdempotencyKey    string `json:"idempotencyKey"`
	ProfileSnapshotID string `json:"profileSnapshotId"`
	ScopeKind         string `json:"scopeKind"`
	ScopeKey          string `json:"scopeKey"`
	OriginalStatement string `json:"originalStatement"`
	Correction        string `json:"correction"`
	CreatedAt         int64  `json:"createdAt,omitempty"`
}

type WorkPatternProfileSelectionEntry struct {
	Ref                   string                         `json:"ref"`
	Kind                  string                         `json:"kind"`
	EvidenceClass         string                         `json:"evidenceClass"`
	Included              bool                           `json:"included"`
	Reason                string                         `json:"reason"`
	Truncated             bool                           `json:"truncated,omitempty"`
	Learning              *TaskContextLearningProvenance `json:"learning,omitempty"`
	OnsetPromptIncluded   *bool                          `json:"onsetPromptIncluded,omitempty"`
	OnsetPromptReason     string                         `json:"onsetPromptReason,omitempty"`
	ShadowContextIncluded *bool                          `json:"shadowContextIncluded,omitempty"`
	ShadowContextReason   string                         `json:"shadowContextReason,omitempty"`
}

type WorkPatternProfilePromptSelectionSummary struct {
	ManifestEntryCount   int `json:"manifestEntryCount"`
	SelectedEntryCount   int `json:"selectedEntryCount"`
	OmittedEntryCount    int `json:"omittedEntryCount"`
	ContradictionCount   int `json:"contradictionCount"`
	TruncatedEntryCount  int `json:"truncatedEntryCount"`
	PromptCandidateCount int `json:"promptCandidateCount"`
	PromptIncludedCount  int `json:"promptIncludedCount"`
	PromptOmittedCount   int `json:"promptOmittedCount"`
}

// WorkPatternProfileContext is the full cutoff-safe selection result. Before
// model use it is compiled into a bounded prompt projection while its complete
// selection provenance is retained as immutable TaskEpisode evidence.
type WorkPatternProfileContext struct {
	SnapshotID              string                                    `json:"snapshotId"`
	SnapshotSHA256          string                                    `json:"snapshotSha256"`
	PolicyVersion           string                                    `json:"policyVersion"`
	SourceCutoffAt          int64                                     `json:"sourceCutoffAt"`
	Status                  string                                    `json:"status"`
	Confidence              float64                                   `json:"confidence"`
	InfluenceMode           string                                    `json:"influenceMode"`
	PromptProjectionVersion string                                    `json:"promptProjectionVersion,omitempty"`
	SourceContextSHA256     string                                    `json:"sourceContextSha256,omitempty"`
	SelectionManifestSHA256 string                                    `json:"selectionManifestSha256,omitempty"`
	SelectionSummary        *WorkPatternProfilePromptSelectionSummary `json:"selectionSummary,omitempty"`
	SelectedProjections     []WorkPatternProjection                   `json:"selectedProjections"`
	SemanticSignals         []WorkPatternSemanticSignal               `json:"semanticSignals"`
	CapabilitySignals       []WorkPatternCapabilitySignal             `json:"capabilitySignals"`
	Contradictions          []WorkPatternProfileCorrection            `json:"contradictions"`
	SelectionManifest       []WorkPatternProfileSelectionEntry        `json:"selectionManifest"`
	SelectionReason         string                                    `json:"selectionReason"`
	CanonicalJSON           string                                    `json:"canonicalJson,omitempty"`
}

func workPatternProjectionMetric(
	item WorkPatternProjection,
	name string,
) (WorkPatternMetric, bool) {
	for _, metric := range item.Metrics {
		if metric.Name == name {
			return metric, true
		}
	}
	return WorkPatternMetric{}, false
}

func workPatternProjectionStatement(item WorkPatternProjection) string {
	switch item.ScopeKind {
	case WorkPatternScopeTransition:
		if metric, ok := workPatternProjectionMetric(item, "transition_count"); ok {
			return fmt.Sprintf(
				"Isoform observed %d transitions in the %s pattern across %d sessions.",
				int(mathRound(metric.Value)),
				item.Label,
				item.SessionCount,
			)
		}
	case WorkPatternScopeWorkMode:
		return fmt.Sprintf(
			"%s recur together across %d observed work sessions.",
			item.Label,
			item.SessionCount,
		)
	case WorkPatternScopeApp:
		if metric, ok := workPatternProjectionMetric(item, "median_dwell_seconds"); ok {
			return fmt.Sprintf(
				"Observed work intervals in %s have a median duration of %s.",
				item.Label,
				formatWorkPatternDuration(metric.Value),
			)
		}
	case WorkPatternScopeGlobal:
		dwell, hasDwell := workPatternProjectionMetric(
			item,
			"median_dwell_seconds",
		)
		switches, hasSwitches := workPatternProjectionMetric(
			item,
			"switches_per_active_hour",
		)
		if hasDwell && hasSwitches {
			return fmt.Sprintf(
				"Across %d sessions, the median app interval is %s with %.1f app transitions per active hour.",
				item.SessionCount,
				formatWorkPatternDuration(dwell.Value),
				switches.Value,
			)
		}
	}
	return fmt.Sprintf(
		"%s is supported by %d observed intervals across %d sessions.",
		item.Label,
		item.SampleCount,
		item.SessionCount,
	)
}

func mathRound(value float64) float64 {
	if value < 0 {
		return float64(int(value - 0.5))
	}
	return float64(int(value + 0.5))
}

func formatWorkPatternDuration(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%.0f seconds", mathRound(seconds))
	}
	if seconds < 3600 {
		return fmt.Sprintf("%.1f minutes", seconds/60)
	}
	return fmt.Sprintf("%.1f hours", seconds/3600)
}

func listWorkPatternCapabilitySignalsAt(
	store *ActivityStore,
	cutoffAt int64,
) ([]WorkPatternCapabilitySignal, error) {
	// Capability signals came only from verified shadow learning, which is
	// removed.
	result := make([]WorkPatternCapabilitySignal, 0)
	return sortedWorkPatternCapabilitySignals(result), nil
}

func scanWorkPatternProfileCorrection(
	scanner interface{ Scan(dest ...any) error },
) (WorkPatternProfileCorrection, error) {
	var result WorkPatternProfileCorrection
	err := scanner.Scan(
		&result.ID,
		&result.IdempotencyKey,
		&result.ProfileSnapshotID,
		&result.ScopeKind,
		&result.ScopeKey,
		&result.Revision,
		&result.OriginalStatement,
		&result.Correction,
		&result.CreatedAt,
	)
	return result, err
}

func (s *ActivityStore) RecordWorkPatternProfileCorrection(
	input RecordWorkPatternProfileCorrectionInput,
) (WorkPatternProfileCorrection, bool, error) {
	if s == nil || s.db == nil {
		return WorkPatternProfileCorrection{}, false, errors.New(
			"activity store is unavailable",
		)
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.ProfileSnapshotID = strings.TrimSpace(input.ProfileSnapshotID)
	input.ScopeKind = strings.TrimSpace(input.ScopeKind)
	input.ScopeKey = strings.TrimSpace(input.ScopeKey)
	input.OriginalStatement = strings.TrimSpace(input.OriginalStatement)
	input.Correction = strings.TrimSpace(input.Correction)
	if input.CreatedAt <= 0 {
		input.CreatedAt = time.Now().UnixMilli()
	}
	if input.IdempotencyKey == "" || input.ProfileSnapshotID == "" ||
		input.ScopeKind == "" || input.ScopeKey == "" ||
		input.OriginalStatement == "" || input.Correction == "" {
		return WorkPatternProfileCorrection{}, false, errors.New(
			"work pattern correction is incomplete",
		)
	}
	if strings.EqualFold(input.OriginalStatement, input.Correction) {
		return WorkPatternProfileCorrection{}, false, errors.New(
			"work pattern correction must change the statement",
		)
	}
	snapshot, err := s.GetWorkPatternProfileSnapshot(
		input.ProfileSnapshotID,
	)
	if err != nil {
		return WorkPatternProfileCorrection{}, false, err
	}
	foundScope := false
	expectedStatement := ""
	for _, projection := range snapshot.Projections {
		if projection.ScopeKind == input.ScopeKind &&
			projection.ScopeKey == input.ScopeKey {
			foundScope = true
			expectedStatement = workPatternProjectionStatement(projection)
			break
		}
	}
	if !foundScope {
		return WorkPatternProfileCorrection{}, false, errors.New(
			"work pattern correction scope is not present in the snapshot",
		)
	}
	if input.OriginalStatement != expectedStatement {
		return WorkPatternProfileCorrection{}, false, errors.New(
			"work pattern correction targets a stale statement",
		)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return WorkPatternProfileCorrection{}, false, err
	}
	defer tx.Rollback()
	existing, err := scanWorkPatternProfileCorrection(tx.QueryRow(`
		SELECT
			id, idempotency_key, profile_snapshot_id, scope_kind, scope_key,
			revision, original_statement, correction, created_at
		FROM work_pattern_profile_corrections_v1
		WHERE idempotency_key = ?`,
		input.IdempotencyKey,
	))
	if err == nil {
		if existing.ProfileSnapshotID != input.ProfileSnapshotID ||
			existing.ScopeKind != input.ScopeKind ||
			existing.ScopeKey != input.ScopeKey ||
			existing.OriginalStatement != input.OriginalStatement ||
			existing.Correction != input.Correction {
			return WorkPatternProfileCorrection{}, false, errors.New(
				"work pattern correction idempotency key has a different payload",
			)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return WorkPatternProfileCorrection{}, false, err
	}
	revision := 1
	if err := tx.QueryRow(`
		SELECT COALESCE(MAX(revision), 0) + 1
		FROM work_pattern_profile_corrections_v1
		WHERE scope_kind = ? AND scope_key = ?`,
		input.ScopeKind,
		input.ScopeKey,
	).Scan(&revision); err != nil {
		return WorkPatternProfileCorrection{}, false, err
	}
	result := WorkPatternProfileCorrection{
		ID: deterministicTaskEpisodeID(
			"work-pattern-profile-correction",
			input.IdempotencyKey,
		),
		IdempotencyKey:    input.IdempotencyKey,
		ProfileSnapshotID: input.ProfileSnapshotID,
		ScopeKind:         input.ScopeKind,
		ScopeKey:          input.ScopeKey,
		Revision:          revision,
		OriginalStatement: input.OriginalStatement,
		Correction:        input.Correction,
		CreatedAt:         input.CreatedAt,
	}
	if _, err := tx.Exec(`
		INSERT INTO work_pattern_profile_corrections_v1 (
			id, idempotency_key, profile_snapshot_id, scope_kind, scope_key,
			revision, original_statement, correction, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		result.ID,
		result.IdempotencyKey,
		result.ProfileSnapshotID,
		result.ScopeKind,
		result.ScopeKey,
		result.Revision,
		result.OriginalStatement,
		result.Correction,
		result.CreatedAt,
	); err != nil {
		return WorkPatternProfileCorrection{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return WorkPatternProfileCorrection{}, false, err
	}
	return result, true, nil
}

func (s *ActivityStore) ListWorkPatternProfileCorrections(
	cutoffAt int64,
) ([]WorkPatternProfileCorrection, error) {
	if cutoffAt <= 0 {
		cutoffAt = time.Now().UnixMilli()
	}
	rows, err := s.db.Query(`
		SELECT
			id, idempotency_key, profile_snapshot_id, scope_kind, scope_key,
			revision, original_statement, correction, created_at
		FROM work_pattern_profile_corrections_v1
		WHERE created_at <= ?
		ORDER BY scope_kind ASC, scope_key ASC, revision ASC`,
		cutoffAt,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]WorkPatternProfileCorrection, 0)
	for rows.Next() {
		item, err := scanWorkPatternProfileCorrection(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// RecordWorkPatternProfileCorrection appends explicit contradiction evidence
// and then derives a new immutable profile revision whose cutoff includes that
// correction. It cannot edit old snapshots, qualify a capability, or affect
// authority.
func (a *App) RecordWorkPatternProfileCorrection(
	input RecordWorkPatternProfileCorrectionInput,
) (WorkPatternProfileCorrection, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return WorkPatternProfileCorrection{}, errors.New(
			"activity store is unavailable",
		)
	}
	result, _, err := a.activity.store.RecordWorkPatternProfileCorrection(input)
	if err != nil {
		return WorkPatternProfileCorrection{}, err
	}
	_, _, err = a.activity.store.AddSelfModelEntry(SelfModelEntry{
		Kind:            selfModelKindPreference,
		Scope:           "how_you_work",
		Statement:       result.Correction,
		Status:          selfModelStatusActive,
		Confidence:      1,
		ReasoningStatus: "explicit_user_correction",
		Evidence: []SelfModelEvidence{{
			SourceKind: "work_pattern_profile_correction",
			SourceID:   result.ID,
			Statement:  result.OriginalStatement,
			Confidence: 1,
			SourceRefs: []string{
				"profile:" + result.ProfileSnapshotID,
				"projection:" + result.ScopeKind + ":" + result.ScopeKey,
			},
			OccurredAt: result.CreatedAt,
		}},
		CreatedAt: result.CreatedAt,
	})
	if err != nil {
		return WorkPatternProfileCorrection{}, fmt.Errorf(
			"recorded work pattern correction but could not revise Self: %w",
			err,
		)
	}
	if _, _, err := a.activity.store.EnsureWorkPatternProfileSnapshot(
		result.CreatedAt,
		time.Now().UnixMilli(),
	); err != nil {
		return WorkPatternProfileCorrection{}, fmt.Errorf(
			"recorded work pattern correction but could not derive its revision: %w",
			err,
		)
	}
	a.emitBrowserEvent("work_pattern_profile:corrected", result)
	return result, nil
}
