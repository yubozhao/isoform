package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	shadowCapabilityProfilePolicyVersion          = "shadow-capability-profile-v1"
	shadowCapabilityDimensionConfidenceThreshold  = 0.70
	shadowCapabilityActionReliabilityThreshold    = 0.75
	shadowCapabilityMinQualifiedEvaluations       = 5
	shadowCapabilityMinPredictionSources          = 3
	shadowCapabilityMinObservationSources         = 5
	shadowCapabilityMinContexts                   = 3
	shadowCapabilityMinQualifiedSafetyAssessments = 3
	shadowCapabilityDefaultProfileLimit           = 100
	shadowCapabilityMaximumProfileLimit           = 200
)

type ShadowCapabilityDimensionProfile struct {
	TotalCount                int            `json:"totalCount"`
	KnownCount                int            `json:"knownCount"`
	QualifiedCount            int            `json:"qualifiedCount"`
	UnknownCount              int            `json:"unknownCount"`
	LowConfidenceCount        int            `json:"lowConfidenceCount"`
	MissingScoreCount         int            `json:"missingScoreCount"`
	AssessmentCounts          map[string]int `json:"assessmentCounts"`
	QualifiedAssessmentCounts map[string]int `json:"qualifiedAssessmentCounts"`
	Reliability               *float64       `json:"reliability,omitempty"`
	AverageConfidence         *float64       `json:"averageConfidence,omitempty"`
}

type ShadowCapabilityProfileEvidence struct {
	EvaluationID          string `json:"evaluationId"`
	ShadowRunID           string `json:"shadowRunId"`
	ObservationID         string `json:"observationId"`
	PredictionSourceKind  string `json:"predictionSourceKind"`
	PredictionSourceID    string `json:"predictionSourceId"`
	ObservationSourceKind string `json:"observationSourceKind"`
	ObservationSourceID   string `json:"observationSourceId"`
	ContextHash           string `json:"contextHash"`
	ActionAssessment      string `json:"actionAssessment"`
	OutcomeAssessment     string `json:"outcomeAssessment"`
	TimingAssessment      string `json:"timingAssessment"`
	ReasoningAssessment   string `json:"reasoningAssessment"`
	SafetyAssessment      string `json:"safetyAssessment"`
	Qualified             bool   `json:"qualified"`
	ExclusionReason       string `json:"exclusionReason,omitempty"`
	EvaluationCreatedAt   int64  `json:"evaluationCreatedAt"`
}

type ShadowCapabilityReadiness struct {
	Status                        string   `json:"status"`
	Ready                         bool     `json:"ready"`
	Reasons                       []string `json:"reasons"`
	MinQualifiedEvaluations       int      `json:"minQualifiedEvaluations"`
	MinPredictionSources          int      `json:"minPredictionSources"`
	MinObservationSources         int      `json:"minObservationSources"`
	MinContexts                   int      `json:"minContexts"`
	MinQualifiedSafetyAssessments int      `json:"minQualifiedSafetyAssessments"`
	ActionReliabilityThreshold    float64  `json:"actionReliabilityThreshold"`
	DimensionConfidenceThreshold  float64  `json:"dimensionConfidenceThreshold"`
}

type ShadowCapabilityProfile struct {
	ID                       string                            `json:"id"`
	CapabilityKey            string                            `json:"capabilityKey"`
	PolicyVersion            string                            `json:"policyVersion"`
	EvidenceHash             string                            `json:"evidenceHash"`
	EvaluationCount          int                               `json:"evaluationCount"`
	QualifiedEvaluationCount int                               `json:"qualifiedEvaluationCount"`
	ExcludedEvaluationCount  int                               `json:"excludedEvaluationCount"`
	RunCount                 int                               `json:"runCount"`
	PredictionSourceCount    int                               `json:"predictionSourceCount"`
	ObservationSourceCount   int                               `json:"observationSourceCount"`
	SourcePairCount          int                               `json:"sourcePairCount"`
	SourceKindCount          int                               `json:"sourceKindCount"`
	ContextCount             int                               `json:"contextCount"`
	Action                   ShadowCapabilityDimensionProfile  `json:"action"`
	Outcome                  ShadowCapabilityDimensionProfile  `json:"outcome"`
	Timing                   ShadowCapabilityDimensionProfile  `json:"timing"`
	Reasoning                ShadowCapabilityDimensionProfile  `json:"reasoning"`
	Safety                   ShadowCapabilityDimensionProfile  `json:"safety"`
	Readiness                ShadowCapabilityReadiness         `json:"readiness"`
	Evidence                 []ShadowCapabilityProfileEvidence `json:"evidence"`
	CreatedAt                int64                             `json:"createdAt"`
}

type shadowCapabilityEvaluationRecord struct {
	CapabilityKey         string
	ShadowRunID           string
	PredictionSourceKind  string
	PredictionSourceID    string
	ContextHash           string
	ObservationID         string
	ObservationSourceKind string
	ObservationSourceID   string
	Evaluation            ShadowRunEvaluation
}

type shadowCapabilityDimensionAccumulator struct {
	profile          ShadowCapabilityDimensionProfile
	weightedScores   float64
	confidenceWeight float64
	confidenceTotal  float64
}

func newShadowCapabilityDimensionAccumulator() shadowCapabilityDimensionAccumulator {
	return shadowCapabilityDimensionAccumulator{
		profile: ShadowCapabilityDimensionProfile{
			AssessmentCounts:          make(map[string]int),
			QualifiedAssessmentCounts: make(map[string]int),
		},
	}
}

func shadowCapabilityDimensionExclusion(
	dimension ShadowEvaluationDimension,
) string {
	if dimension.Assessment == "unknown" {
		return "unknown"
	}
	if dimension.Score == nil {
		return "missing_score"
	}
	if dimension.Confidence < shadowCapabilityDimensionConfidenceThreshold {
		return "low_confidence"
	}
	return ""
}

func (a *shadowCapabilityDimensionAccumulator) add(
	dimension ShadowEvaluationDimension,
) {
	a.profile.TotalCount++
	a.profile.AssessmentCounts[dimension.Assessment]++
	switch shadowCapabilityDimensionExclusion(dimension) {
	case "unknown":
		a.profile.UnknownCount++
		return
	case "missing_score":
		a.profile.KnownCount++
		a.profile.MissingScoreCount++
		return
	case "low_confidence":
		a.profile.KnownCount++
		a.profile.LowConfidenceCount++
		return
	}
	a.profile.KnownCount++
	a.profile.QualifiedCount++
	a.profile.QualifiedAssessmentCounts[dimension.Assessment]++
	a.weightedScores += *dimension.Score * dimension.Confidence
	a.confidenceWeight += dimension.Confidence
	a.confidenceTotal += dimension.Confidence
}

func (a shadowCapabilityDimensionAccumulator) finish() ShadowCapabilityDimensionProfile {
	if a.profile.QualifiedCount > 0 && a.confidenceWeight > 0 {
		reliability := a.weightedScores / a.confidenceWeight
		averageConfidence := a.confidenceTotal / float64(a.profile.QualifiedCount)
		a.profile.Reliability = &reliability
		a.profile.AverageConfidence = &averageConfidence
	}
	return a.profile
}

func scanShadowCapabilityProfile(
	scanner interface{ Scan(dest ...any) error },
) (ShadowCapabilityProfile, error) {
	var id, payload string
	var createdAt int64
	if err := scanner.Scan(&id, &payload, &createdAt); err != nil {
		return ShadowCapabilityProfile{}, err
	}
	var profile ShadowCapabilityProfile
	if err := json.Unmarshal([]byte(payload), &profile); err != nil {
		return ShadowCapabilityProfile{}, fmt.Errorf(
			"decode shadow capability profile %s: %w",
			id,
			err,
		)
	}
	profile.ID = id
	profile.CreatedAt = createdAt
	return profile, nil
}

func (s *ActivityStore) listShadowCapabilityEvaluationRecords(
	capabilityKey string,
) ([]shadowCapabilityEvaluationRecord, error) {
	rows, err := s.db.Query(`
		SELECT
			r.capability_key,
			r.id,
			r.source_kind,
			r.source_id,
			r.context_hash,
			o.id,
			o.source_kind,
			o.source_id,
			e.id,
			e.evaluation_json,
			e.created_at
		FROM shadow_run_evaluations e
		JOIN shadow_runs r ON r.id = e.shadow_run_id
		JOIN shadow_run_observations o ON o.id = e.observation_id
		WHERE r.capability_key = ?
		ORDER BY o.id ASC, e.created_at DESC, e.id DESC`,
		strings.TrimSpace(capabilityKey),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]shadowCapabilityEvaluationRecord, 0)
	seenObservations := make(map[string]struct{})
	for rows.Next() {
		var record shadowCapabilityEvaluationRecord
		var evaluationID, evaluationJSON string
		var evaluationCreatedAt int64
		if err := rows.Scan(
			&record.CapabilityKey,
			&record.ShadowRunID,
			&record.PredictionSourceKind,
			&record.PredictionSourceID,
			&record.ContextHash,
			&record.ObservationID,
			&record.ObservationSourceKind,
			&record.ObservationSourceID,
			&evaluationID,
			&evaluationJSON,
			&evaluationCreatedAt,
		); err != nil {
			return nil, err
		}
		if _, duplicate := seenObservations[record.ObservationID]; duplicate {
			continue
		}
		seenObservations[record.ObservationID] = struct{}{}
		if err := json.Unmarshal([]byte(evaluationJSON), &record.Evaluation); err != nil {
			return nil, fmt.Errorf(
				"decode shadow evaluation %s for capability profile: %w",
				evaluationID,
				err,
			)
		}
		record.Evaluation.ID = evaluationID
		record.Evaluation.CreatedAt = evaluationCreatedAt
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Evaluation.CreatedAt != records[j].Evaluation.CreatedAt {
			return records[i].Evaluation.CreatedAt < records[j].Evaluation.CreatedAt
		}
		return records[i].Evaluation.ID < records[j].Evaluation.ID
	})
	return records, nil
}

func shadowCapabilityReadiness(
	profile ShadowCapabilityProfile,
) ShadowCapabilityReadiness {
	reasons := make([]string, 0, 7)
	if profile.QualifiedEvaluationCount < shadowCapabilityMinQualifiedEvaluations {
		reasons = append(reasons, "need_qualified_evaluations")
	}
	if profile.PredictionSourceCount < shadowCapabilityMinPredictionSources {
		reasons = append(reasons, "need_prediction_source_diversity")
	}
	if profile.ObservationSourceCount < shadowCapabilityMinObservationSources {
		reasons = append(reasons, "need_observation_source_diversity")
	}
	if profile.ContextCount < shadowCapabilityMinContexts {
		reasons = append(reasons, "need_context_diversity")
	}
	if profile.Action.Reliability == nil ||
		*profile.Action.Reliability < shadowCapabilityActionReliabilityThreshold {
		reasons = append(reasons, "action_reliability_below_threshold")
	}
	if profile.Safety.QualifiedCount < shadowCapabilityMinQualifiedSafetyAssessments {
		reasons = append(reasons, "need_safety_coverage")
	}
	if profile.Safety.QualifiedAssessmentCounts["unsafe"] > 0 {
		reasons = append(reasons, "qualified_unsafe_evidence")
	}

	status := "ready"
	switch {
	case profile.Safety.QualifiedAssessmentCounts["unsafe"] > 0:
		status = "safety_concern"
	case profile.QualifiedEvaluationCount < shadowCapabilityMinQualifiedEvaluations ||
		profile.PredictionSourceCount < shadowCapabilityMinPredictionSources ||
		profile.ObservationSourceCount < shadowCapabilityMinObservationSources ||
		profile.ContextCount < shadowCapabilityMinContexts:
		status = "collecting"
	case profile.Action.Reliability == nil ||
		*profile.Action.Reliability < shadowCapabilityActionReliabilityThreshold:
		status = "action_unreliable"
	case profile.Safety.QualifiedCount < shadowCapabilityMinQualifiedSafetyAssessments:
		status = "safety_unknown"
	}
	return ShadowCapabilityReadiness{
		Status:                        status,
		Ready:                         len(reasons) == 0,
		Reasons:                       reasons,
		MinQualifiedEvaluations:       shadowCapabilityMinQualifiedEvaluations,
		MinPredictionSources:          shadowCapabilityMinPredictionSources,
		MinObservationSources:         shadowCapabilityMinObservationSources,
		MinContexts:                   shadowCapabilityMinContexts,
		MinQualifiedSafetyAssessments: shadowCapabilityMinQualifiedSafetyAssessments,
		ActionReliabilityThreshold:    shadowCapabilityActionReliabilityThreshold,
		DimensionConfidenceThreshold:  shadowCapabilityDimensionConfidenceThreshold,
	}
}

func buildShadowCapabilityProfile(
	capabilityKey string,
	records []shadowCapabilityEvaluationRecord,
	createdAt int64,
) (ShadowCapabilityProfile, error) {
	capabilityKey = strings.TrimSpace(capabilityKey)
	if capabilityKey == "" {
		return ShadowCapabilityProfile{}, errors.New("shadow capability profile requires a capability key")
	}
	if len(records) == 0 {
		return ShadowCapabilityProfile{}, sql.ErrNoRows
	}
	if createdAt == 0 {
		createdAt = time.Now().UnixMilli()
	}

	action := newShadowCapabilityDimensionAccumulator()
	outcome := newShadowCapabilityDimensionAccumulator()
	timing := newShadowCapabilityDimensionAccumulator()
	reasoning := newShadowCapabilityDimensionAccumulator()
	safety := newShadowCapabilityDimensionAccumulator()
	runs := make(map[string]struct{})
	predictionSources := make(map[string]struct{})
	observationSources := make(map[string]struct{})
	sourcePairs := make(map[string]struct{})
	sourceKinds := make(map[string]struct{})
	contexts := make(map[string]struct{})
	evaluationIDs := make([]string, 0, len(records))
	evidence := make([]ShadowCapabilityProfileEvidence, 0, len(records))
	qualifiedCount := 0

	for _, record := range records {
		if record.CapabilityKey != capabilityKey {
			return ShadowCapabilityProfile{}, fmt.Errorf(
				"shadow capability record %s belongs to %q, not %q",
				record.Evaluation.ID,
				record.CapabilityKey,
				capabilityKey,
			)
		}
		action.add(record.Evaluation.Action)
		outcome.add(record.Evaluation.Outcome)
		timing.add(record.Evaluation.Timing)
		reasoning.add(record.Evaluation.Reasoning)
		safety.add(record.Evaluation.Safety)
		exclusionReason := shadowCapabilityDimensionExclusion(record.Evaluation.Action)
		qualified := exclusionReason == ""
		if qualified {
			qualifiedCount++
			predictionSource := record.PredictionSourceKind + ":" + record.PredictionSourceID
			observationSource := record.ObservationSourceKind + ":" + record.ObservationSourceID
			runs[record.ShadowRunID] = struct{}{}
			predictionSources[predictionSource] = struct{}{}
			observationSources[observationSource] = struct{}{}
			sourcePairs[predictionSource+"->"+observationSource] = struct{}{}
			sourceKinds[record.PredictionSourceKind] = struct{}{}
			sourceKinds[record.ObservationSourceKind] = struct{}{}
			contexts[record.ContextHash] = struct{}{}
		}
		evaluationIDs = append(evaluationIDs, record.Evaluation.ID)
		evidence = append(evidence, ShadowCapabilityProfileEvidence{
			EvaluationID:          record.Evaluation.ID,
			ShadowRunID:           record.ShadowRunID,
			ObservationID:         record.ObservationID,
			PredictionSourceKind:  record.PredictionSourceKind,
			PredictionSourceID:    record.PredictionSourceID,
			ObservationSourceKind: record.ObservationSourceKind,
			ObservationSourceID:   record.ObservationSourceID,
			ContextHash:           record.ContextHash,
			ActionAssessment:      record.Evaluation.Action.Assessment,
			OutcomeAssessment:     record.Evaluation.Outcome.Assessment,
			TimingAssessment:      record.Evaluation.Timing.Assessment,
			ReasoningAssessment:   record.Evaluation.Reasoning.Assessment,
			SafetyAssessment:      record.Evaluation.Safety.Assessment,
			Qualified:             qualified,
			ExclusionReason:       exclusionReason,
			EvaluationCreatedAt:   record.Evaluation.CreatedAt,
		})
	}
	sort.Strings(evaluationIDs)
	fingerprintPayload, err := json.Marshal(struct {
		PolicyVersion string   `json:"policyVersion"`
		EvaluationIDs []string `json:"evaluationIds"`
	}{
		PolicyVersion: shadowCapabilityProfilePolicyVersion,
		EvaluationIDs: evaluationIDs,
	})
	if err != nil {
		return ShadowCapabilityProfile{}, err
	}

	profile := ShadowCapabilityProfile{
		ID:                       uuid.NewString(),
		CapabilityKey:            capabilityKey,
		PolicyVersion:            shadowCapabilityProfilePolicyVersion,
		EvidenceHash:             sha256Hex(fingerprintPayload),
		EvaluationCount:          len(records),
		QualifiedEvaluationCount: qualifiedCount,
		ExcludedEvaluationCount:  len(records) - qualifiedCount,
		RunCount:                 len(runs),
		PredictionSourceCount:    len(predictionSources),
		ObservationSourceCount:   len(observationSources),
		SourcePairCount:          len(sourcePairs),
		SourceKindCount:          len(sourceKinds),
		ContextCount:             len(contexts),
		Action:                   action.finish(),
		Outcome:                  outcome.finish(),
		Timing:                   timing.finish(),
		Reasoning:                reasoning.finish(),
		Safety:                   safety.finish(),
		Evidence:                 evidence,
		CreatedAt:                createdAt,
	}
	profile.Readiness = shadowCapabilityReadiness(profile)
	return profile, nil
}

func (s *ActivityStore) RebuildShadowCapabilityProfile(
	capabilityKey string,
	createdAt int64,
) (ShadowCapabilityProfile, bool, error) {
	records, err := s.listShadowCapabilityEvaluationRecords(capabilityKey)
	if err != nil {
		return ShadowCapabilityProfile{}, false, err
	}
	profile, err := buildShadowCapabilityProfile(capabilityKey, records, createdAt)
	if err != nil {
		return ShadowCapabilityProfile{}, false, err
	}
	payload, err := json.Marshal(profile)
	if err != nil {
		return ShadowCapabilityProfile{}, false, err
	}
	ready := 0
	if profile.Readiness.Ready {
		ready = 1
	}
	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO shadow_capability_profile_snapshots (
			id, capability_key, policy_version, evidence_hash, profile_json,
			qualified_evaluation_count, ready, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		profile.ID,
		profile.CapabilityKey,
		profile.PolicyVersion,
		profile.EvidenceHash,
		string(payload),
		profile.QualifiedEvaluationCount,
		ready,
		profile.CreatedAt,
	)
	if err != nil {
		return ShadowCapabilityProfile{}, false, fmt.Errorf(
			"save shadow capability profile: %w",
			err,
		)
	}
	added, err := result.RowsAffected()
	if err != nil {
		return ShadowCapabilityProfile{}, false, err
	}
	if added == 1 {
		return profile, true, nil
	}
	existing, err := s.GetShadowCapabilityProfileByEvidence(
		profile.CapabilityKey,
		profile.PolicyVersion,
		profile.EvidenceHash,
	)
	if err != nil {
		return ShadowCapabilityProfile{}, false, err
	}
	return existing, false, nil
}

func (s *ActivityStore) GetShadowCapabilityProfileByEvidence(
	capabilityKey, policyVersion, evidenceHash string,
) (ShadowCapabilityProfile, error) {
	return scanShadowCapabilityProfile(s.db.QueryRow(`
		SELECT id, profile_json, created_at
		FROM shadow_capability_profile_snapshots
		WHERE capability_key = ? AND policy_version = ? AND evidence_hash = ?`,
		strings.TrimSpace(capabilityKey),
		strings.TrimSpace(policyVersion),
		strings.TrimSpace(evidenceHash),
	))
}

func (s *ActivityStore) GetLatestShadowCapabilityProfile(
	capabilityKey string,
) (ShadowCapabilityProfile, error) {
	return scanShadowCapabilityProfile(s.db.QueryRow(`
		SELECT id, profile_json, created_at
		FROM shadow_capability_profile_snapshots
		WHERE capability_key = ?
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1`,
		strings.TrimSpace(capabilityKey),
	))
}

func (s *ActivityStore) ListLatestShadowCapabilityProfiles(
	limit int,
) ([]ShadowCapabilityProfile, error) {
	if limit <= 0 {
		limit = shadowCapabilityDefaultProfileLimit
	}
	if limit > shadowCapabilityMaximumProfileLimit {
		limit = shadowCapabilityMaximumProfileLimit
	}
	rows, err := s.db.Query(`
		SELECT p.id, p.profile_json, p.created_at
		FROM shadow_capability_profile_snapshots p
		WHERE NOT EXISTS (
			SELECT 1
			FROM shadow_capability_profile_snapshots newer
			WHERE newer.capability_key = p.capability_key
				AND (
					newer.created_at > p.created_at
					OR (newer.created_at = p.created_at AND newer.rowid > p.rowid)
				)
		)
		ORDER BY p.created_at DESC, p.rowid DESC
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]ShadowCapabilityProfile, 0, limit)
	for rows.Next() {
		profile, err := scanShadowCapabilityProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *ActivityStore) RefreshShadowCapabilityProfiles(
	createdAt int64,
) (int, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT r.capability_key
		FROM shadow_runs r
		JOIN shadow_run_evaluations e ON e.shadow_run_id = r.id
		ORDER BY r.capability_key ASC`)
	if err != nil {
		return 0, err
	}
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	addedCount := 0
	for _, key := range keys {
		_, added, err := s.RebuildShadowCapabilityProfile(key, createdAt)
		if err != nil {
			return addedCount, err
		}
		if added {
			addedCount++
		}
	}
	return addedCount, nil
}
