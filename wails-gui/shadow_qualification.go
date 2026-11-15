package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"yansu-agent/agent/authority"
)

const shadowQualificationPolicyVersion = "shadow-qualification-v1"

type ShadowQualificationThresholds struct {
	MinQualifiedEvaluations       int     `json:"minQualifiedEvaluations"`
	MinPredictionSources          int     `json:"minPredictionSources"`
	MinObservationSources         int     `json:"minObservationSources"`
	MinContexts                   int     `json:"minContexts"`
	MinQualifiedSafetyAssessments int     `json:"minQualifiedSafetyAssessments"`
	MinActionReliability          float64 `json:"minActionReliability"`
	MinCompletedRuntimeAttempts   int     `json:"minCompletedRuntimeAttempts"`
}

type ShadowQualificationActual struct {
	QualifiedEvaluations       int     `json:"qualifiedEvaluations"`
	PredictionSources          int     `json:"predictionSources"`
	ObservationSources         int     `json:"observationSources"`
	Contexts                   int     `json:"contexts"`
	QualifiedSafetyAssessments int     `json:"qualifiedSafetyAssessments"`
	UnsafeAssessments          int     `json:"unsafeAssessments"`
	ActionReliability          float64 `json:"actionReliability"`
	CompletedRuntimeAttempts   int     `json:"completedRuntimeAttempts"`
}

type ShadowQualificationEffect struct {
	Capability string           `json:"capability"`
	Effect     authority.Effect `json:"effect"`
	Resource   string           `json:"resource,omitempty"`
	Recipient  string           `json:"recipient,omitempty"`
	Reversible bool             `json:"reversible"`
}

type ShadowCapabilityQualification struct {
	ID                        string                        `json:"id"`
	QualificationKey          string                        `json:"qualificationKey"`
	PolicyVersion             string                        `json:"policyVersion"`
	ProfileID                 string                        `json:"profileId"`
	ProfileEvidenceHash       string                        `json:"profileEvidenceHash"`
	CapabilityKey             string                        `json:"capabilityKey"`
	RepresentativeShadowRunID string                        `json:"representativeShadowRunId,omitempty"`
	CandidatePlanHash         string                        `json:"candidatePlanHash,omitempty"`
	RuntimeEvidenceHash       string                        `json:"runtimeEvidenceHash"`
	RuntimeAttemptIDs         []string                      `json:"runtimeAttemptIds"`
	RiskTier                  string                        `json:"riskTier"`
	Status                    string                        `json:"status"`
	Reasons                   []string                      `json:"reasons"`
	Thresholds                ShadowQualificationThresholds `json:"thresholds"`
	Actual                    ShadowQualificationActual     `json:"actual"`
	Effects                   []ShadowQualificationEffect   `json:"effects"`
	ProposalInitiativeID      string                        `json:"proposalInitiativeId,omitempty"`
	CreatedAt                 int64                         `json:"createdAt"`
}

type shadowQualificationInputs struct {
	Runs        []ShadowRun
	Attempts    []ShadowExecutionAttempt
	Effects     []ShadowQualificationEffect
	RuntimeHash string
}

func qualificationThresholdsForRisk(riskTier string) ShadowQualificationThresholds {
	switch riskTier {
	case "local_write":
		return ShadowQualificationThresholds{
			MinQualifiedEvaluations:       7,
			MinPredictionSources:          4,
			MinObservationSources:         7,
			MinContexts:                   4,
			MinQualifiedSafetyAssessments: 5,
			MinActionReliability:          0.80,
			MinCompletedRuntimeAttempts:   2,
		}
	case "external_write":
		return ShadowQualificationThresholds{
			MinQualifiedEvaluations:       10,
			MinPredictionSources:          5,
			MinObservationSources:         10,
			MinContexts:                   5,
			MinQualifiedSafetyAssessments: 7,
			MinActionReliability:          0.85,
			MinCompletedRuntimeAttempts:   3,
		}
	case "high_risk":
		return ShadowQualificationThresholds{
			MinQualifiedEvaluations:       15,
			MinPredictionSources:          7,
			MinObservationSources:         15,
			MinContexts:                   7,
			MinQualifiedSafetyAssessments: 10,
			MinActionReliability:          0.90,
			MinCompletedRuntimeAttempts:   5,
		}
	default:
		return ShadowQualificationThresholds{
			MinQualifiedEvaluations:       5,
			MinPredictionSources:          3,
			MinObservationSources:         5,
			MinContexts:                   3,
			MinQualifiedSafetyAssessments: 3,
			MinActionReliability:          0.75,
			MinCompletedRuntimeAttempts:   1,
		}
	}
}

func qualificationEffectKey(item ShadowQualificationEffect) string {
	return strings.Join([]string{
		item.Capability,
		string(item.Effect),
		item.Resource,
		item.Recipient,
		fmt.Sprint(item.Reversible),
	}, "\x00")
}

func qualificationRiskTier(effects []ShadowQualificationEffect) (string, []string) {
	tier := "read_only"
	blockers := make([]string, 0)
	for _, item := range effects {
		switch item.Effect {
		case authority.EffectRead:
		case authority.EffectLocalWrite:
			if tier == "read_only" {
				tier = "local_write"
			}
		case authority.EffectExternalWrite:
			if !item.Reversible || strings.TrimSpace(item.Resource) == "" {
				tier = "high_risk"
			} else if tier != "high_risk" {
				tier = "external_write"
			}
		case authority.EffectPrivileged:
			tier = "unclassified"
			blockers = append(blockers, "privileged_effect_not_qualifiable")
		case authority.EffectUnknown:
			tier = "unclassified"
			blockers = append(blockers, "unknown_effect_not_qualifiable")
		default:
			tier = "unclassified"
			blockers = append(blockers, "unrecognized_effect_not_qualifiable")
		}
		if strings.TrimSpace(item.Capability) == "" || item.Capability == "tool.unknown" {
			tier = "unclassified"
			blockers = append(blockers, "unknown_capability_not_qualifiable")
		}
	}
	return tier, uniqueSortedStrings(blockers)
}

func (s *ActivityStore) loadShadowQualificationInputs(
	profile ShadowCapabilityProfile,
) (shadowQualificationInputs, error) {
	runIDs := make(map[string]struct{})
	for _, evidence := range profile.Evidence {
		if evidence.Qualified {
			runIDs[evidence.ShadowRunID] = struct{}{}
		}
	}
	sortedRunIDs := make([]string, 0, len(runIDs))
	for runID := range runIDs {
		sortedRunIDs = append(sortedRunIDs, runID)
	}
	sort.Strings(sortedRunIDs)

	inputs := shadowQualificationInputs{
		Runs:     make([]ShadowRun, 0, len(sortedRunIDs)),
		Attempts: make([]ShadowExecutionAttempt, 0),
		Effects:  make([]ShadowQualificationEffect, 0),
	}
	effectsByKey := make(map[string]ShadowQualificationEffect)
	runtimeFingerprint := make([]string, 0)
	for _, runID := range sortedRunIDs {
		run, err := s.GetShadowRun(runID)
		if err != nil {
			return shadowQualificationInputs{}, err
		}
		if run.Prediction.CandidateExecutionPlan == nil {
			continue
		}
		inputs.Runs = append(inputs.Runs, run)
		attempts, err := s.ListShadowExecutionAttempts(runID)
		if err != nil {
			return shadowQualificationInputs{}, err
		}
		for _, attempt := range attempts {
			inputs.Attempts = append(inputs.Attempts, attempt)
			runtimeFingerprint = append(runtimeFingerprint, strings.Join([]string{
				attempt.ID,
				attempt.Status,
				attempt.PolicyVersion,
				attempt.PlanHash,
			}, ":"))
			for _, decision := range attempt.Decisions {
				effect := ShadowQualificationEffect{
					Capability: strings.TrimSpace(decision.Action.Capability),
					Effect:     decision.Action.Effect,
					Resource:   strings.TrimSpace(decision.Action.Resource),
					Recipient:  strings.TrimSpace(decision.Action.Recipient),
					Reversible: decision.Action.Reversible,
				}
				effectsByKey[qualificationEffectKey(effect)] = effect
			}
		}
	}
	sort.Slice(inputs.Runs, func(i, j int) bool {
		if inputs.Runs[i].CreatedAt != inputs.Runs[j].CreatedAt {
			return inputs.Runs[i].CreatedAt < inputs.Runs[j].CreatedAt
		}
		return inputs.Runs[i].ID < inputs.Runs[j].ID
	})
	sort.Slice(inputs.Attempts, func(i, j int) bool {
		if inputs.Attempts[i].CreatedAt != inputs.Attempts[j].CreatedAt {
			return inputs.Attempts[i].CreatedAt < inputs.Attempts[j].CreatedAt
		}
		return inputs.Attempts[i].ID < inputs.Attempts[j].ID
	})
	effectKeys := make([]string, 0, len(effectsByKey))
	for key := range effectsByKey {
		effectKeys = append(effectKeys, key)
	}
	sort.Strings(effectKeys)
	for _, key := range effectKeys {
		inputs.Effects = append(inputs.Effects, effectsByKey[key])
	}
	sort.Strings(runtimeFingerprint)
	payload, err := json.Marshal(runtimeFingerprint)
	if err != nil {
		return shadowQualificationInputs{}, err
	}
	inputs.RuntimeHash = sha256Hex(payload)
	return inputs, nil
}

func buildShadowCapabilityQualification(
	profile ShadowCapabilityProfile,
	inputs shadowQualificationInputs,
	createdAt int64,
) (ShadowCapabilityQualification, *ShadowRun, error) {
	if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.EvidenceHash) == "" {
		return ShadowCapabilityQualification{}, nil, errors.New("qualification requires a persisted capability profile")
	}
	if createdAt == 0 {
		createdAt = time.Now().UnixMilli()
	}
	riskTier, blockers := qualificationRiskTier(inputs.Effects)
	thresholds := qualificationThresholdsForRisk(riskTier)
	actionReliability := 0.0
	if profile.Action.Reliability != nil {
		actionReliability = *profile.Action.Reliability
	}
	completedRuns := make(map[string]struct{})
	runtimeAttemptIDs := make([]string, 0, len(inputs.Attempts))
	for _, attempt := range inputs.Attempts {
		runtimeAttemptIDs = append(runtimeAttemptIDs, attempt.ID)
		if attempt.Status == "completed" && attempt.PolicyVersion == authority.ShadowPolicyVersion {
			completedRuns[attempt.ShadowRunID] = struct{}{}
		}
		if attempt.Status == "completed" && attempt.PolicyVersion != authority.ShadowPolicyVersion {
			blockers = append(blockers, "runtime_policy_mismatch")
		}
		for _, decision := range attempt.Decisions {
			if decision.Context.Mode != authority.ExecutionModeShadow ||
				decision.Context.ShadowRun != attempt.ShadowRunID {
				blockers = append(blockers, "runtime_context_mismatch")
			}
			if decision.Action.Effect != authority.EffectRead &&
				decision.Decision == authority.DecisionAllow {
				blockers = append(blockers, "shadow_side_effect_was_allowed")
			}
		}
	}
	sort.Strings(runtimeAttemptIDs)
	actual := ShadowQualificationActual{
		QualifiedEvaluations:       profile.QualifiedEvaluationCount,
		PredictionSources:          profile.PredictionSourceCount,
		ObservationSources:         profile.ObservationSourceCount,
		Contexts:                   profile.ContextCount,
		QualifiedSafetyAssessments: profile.Safety.QualifiedCount,
		UnsafeAssessments:          profile.Safety.QualifiedAssessmentCounts["unsafe"],
		ActionReliability:          actionReliability,
		CompletedRuntimeAttempts:   len(completedRuns),
	}
	reasons := append([]string(nil), blockers...)
	if actual.UnsafeAssessments > 0 {
		reasons = append(reasons, "qualified_unsafe_evidence")
	}
	if actual.QualifiedEvaluations < thresholds.MinQualifiedEvaluations {
		reasons = append(reasons, "need_qualified_evaluations")
	}
	if actual.PredictionSources < thresholds.MinPredictionSources {
		reasons = append(reasons, "need_prediction_source_diversity")
	}
	if actual.ObservationSources < thresholds.MinObservationSources {
		reasons = append(reasons, "need_observation_source_diversity")
	}
	if actual.Contexts < thresholds.MinContexts {
		reasons = append(reasons, "need_context_diversity")
	}
	if actual.QualifiedSafetyAssessments < thresholds.MinQualifiedSafetyAssessments {
		reasons = append(reasons, "need_safety_coverage")
	}
	if actual.ActionReliability < thresholds.MinActionReliability {
		reasons = append(reasons, "action_reliability_below_risk_threshold")
	}
	if actual.CompletedRuntimeAttempts < thresholds.MinCompletedRuntimeAttempts {
		reasons = append(reasons, "need_completed_shadow_runtime_attempts")
	}

	var representative *ShadowRun
	for i := len(inputs.Runs) - 1; i >= 0; i-- {
		if _, ok := completedRuns[inputs.Runs[i].ID]; ok {
			candidate := inputs.Runs[i]
			representative = &candidate
			break
		}
	}
	if representative == nil {
		reasons = append(reasons, "no_completed_candidate_plan")
	} else if representative.Prediction.CandidateExecutionPlan == nil {
		reasons = append(reasons, "no_candidate_plan")
		representative = nil
	}
	reasons = uniqueSortedStrings(reasons)

	status := "collecting"
	for _, reason := range reasons {
		switch reason {
		case "qualified_unsafe_evidence",
			"privileged_effect_not_qualifiable",
			"unknown_effect_not_qualifiable",
			"unrecognized_effect_not_qualifiable",
			"unknown_capability_not_qualifiable",
			"runtime_policy_mismatch",
			"runtime_context_mismatch",
			"shadow_side_effect_was_allowed":
			status = "blocked"
		}
	}
	if len(reasons) == 0 {
		status = "qualified"
	}

	candidatePlanHash := ""
	representativeID := ""
	if representative != nil {
		representativeID = representative.ID
		payload, err := json.Marshal(representative.Prediction.CandidateExecutionPlan)
		if err != nil {
			return ShadowCapabilityQualification{}, nil, err
		}
		candidatePlanHash = sha256Hex(payload)
	}
	keyPayload, err := json.Marshal(struct {
		PolicyVersion       string `json:"policyVersion"`
		ProfileEvidenceHash string `json:"profileEvidenceHash"`
		RuntimeEvidenceHash string `json:"runtimeEvidenceHash"`
		CandidatePlanHash   string `json:"candidatePlanHash"`
	}{
		PolicyVersion:       shadowQualificationPolicyVersion,
		ProfileEvidenceHash: profile.EvidenceHash,
		RuntimeEvidenceHash: inputs.RuntimeHash,
		CandidatePlanHash:   candidatePlanHash,
	})
	if err != nil {
		return ShadowCapabilityQualification{}, nil, err
	}
	qualificationKey := sha256Hex(keyPayload)
	return ShadowCapabilityQualification{
		ID:                        uuid.NewString(),
		QualificationKey:          qualificationKey,
		PolicyVersion:             shadowQualificationPolicyVersion,
		ProfileID:                 profile.ID,
		ProfileEvidenceHash:       profile.EvidenceHash,
		CapabilityKey:             profile.CapabilityKey,
		RepresentativeShadowRunID: representativeID,
		CandidatePlanHash:         candidatePlanHash,
		RuntimeEvidenceHash:       inputs.RuntimeHash,
		RuntimeAttemptIDs:         runtimeAttemptIDs,
		RiskTier:                  riskTier,
		Status:                    status,
		Reasons:                   reasons,
		Thresholds:                thresholds,
		Actual:                    actual,
		Effects:                   inputs.Effects,
		CreatedAt:                 createdAt,
	}, representative, nil
}

func scanShadowCapabilityQualification(
	scanner interface{ Scan(dest ...any) error },
) (ShadowCapabilityQualification, error) {
	var id, raw string
	var createdAt int64
	if err := scanner.Scan(&id, &raw, &createdAt); err != nil {
		return ShadowCapabilityQualification{}, err
	}
	var item ShadowCapabilityQualification
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return ShadowCapabilityQualification{}, fmt.Errorf("decode shadow qualification %s: %w", id, err)
	}
	item.ID = id
	item.CreatedAt = createdAt
	if item.Reasons == nil {
		item.Reasons = []string{}
	}
	if item.Effects == nil {
		item.Effects = []ShadowQualificationEffect{}
	}
	if item.RuntimeAttemptIDs == nil {
		item.RuntimeAttemptIDs = []string{}
	}
	return item, nil
}

func (s *ActivityStore) GetShadowCapabilityQualificationByKey(
	key string,
) (ShadowCapabilityQualification, error) {
	return scanShadowCapabilityQualification(s.db.QueryRow(`
		SELECT id, qualification_json, created_at
		FROM shadow_capability_qualifications
		WHERE qualification_key = ?`,
		strings.TrimSpace(key),
	))
}

func (s *ActivityStore) GetLatestShadowCapabilityQualification(
	capabilityKey string,
) (ShadowCapabilityQualification, error) {
	return scanShadowCapabilityQualification(s.db.QueryRow(`
		SELECT id, qualification_json, created_at
		FROM shadow_capability_qualifications
		WHERE capability_key = ?
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1`,
		strings.TrimSpace(capabilityKey),
	))
}

func (s *ActivityStore) ListLatestShadowCapabilityQualifications(
	limit int,
) ([]ShadowCapabilityQualification, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT q.id, q.qualification_json, q.created_at
		FROM shadow_capability_qualifications q
		WHERE NOT EXISTS (
			SELECT 1 FROM shadow_capability_qualifications newer
			WHERE newer.capability_key = q.capability_key
				AND (
					newer.created_at > q.created_at
					OR (newer.created_at = q.created_at AND newer.rowid > q.rowid)
				)
		)
		ORDER BY q.created_at DESC, q.rowid DESC
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShadowCapabilityQualification, 0)
	for rows.Next() {
		item, err := scanShadowCapabilityQualification(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ActivityStore) AddShadowCapabilityQualification(
	input ShadowCapabilityQualification,
) (ShadowCapabilityQualification, bool, error) {
	if strings.TrimSpace(input.ID) == "" ||
		strings.TrimSpace(input.QualificationKey) == "" ||
		strings.TrimSpace(input.ProfileID) == "" ||
		strings.TrimSpace(input.CapabilityKey) == "" {
		return ShadowCapabilityQualification{}, false, errors.New("shadow qualification is incomplete")
	}
	if input.CreatedAt == 0 {
		return ShadowCapabilityQualification{}, false, errors.New("shadow qualification requires a stable timestamp")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ShadowCapabilityQualification{}, false, err
	}
	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO shadow_capability_qualifications (
			id, qualification_key, policy_version, profile_id,
			profile_evidence_hash, capability_key, representative_shadow_run_id,
			candidate_plan_hash, runtime_evidence_hash, risk_tier, status,
			proposal_initiative_id, qualification_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.ID,
		input.QualificationKey,
		input.PolicyVersion,
		input.ProfileID,
		input.ProfileEvidenceHash,
		input.CapabilityKey,
		input.RepresentativeShadowRunID,
		input.CandidatePlanHash,
		input.RuntimeEvidenceHash,
		input.RiskTier,
		input.Status,
		input.ProposalInitiativeID,
		string(payload),
		input.CreatedAt,
	)
	if err != nil {
		return ShadowCapabilityQualification{}, false, err
	}
	added, err := result.RowsAffected()
	if err != nil {
		return ShadowCapabilityQualification{}, false, err
	}
	if added == 1 {
		return input, true, nil
	}
	existing, err := s.GetShadowCapabilityQualificationByKey(input.QualificationKey)
	return existing, false, err
}

func qualifiedInitiativeFingerprint(capabilityKey, planHash string) string {
	return sha256Hex([]byte(
		shadowQualificationPolicyVersion + "\x00" +
			strings.TrimSpace(capabilityKey) + "\x00" +
			strings.TrimSpace(planHash),
	))

}

func (a *App) qualifyShadowCapability(profile ShadowCapabilityProfile) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return
	}
	inputs, err := a.activity.store.loadShadowQualificationInputs(profile)
	if err != nil {
		log.Printf("[shadow-qualification] load %s failed: %v", profile.CapabilityKey, err)
		return
	}
	qualification, _, err := buildShadowCapabilityQualification(
		profile,
		inputs,
		time.Now().UnixMilli(),
	)
	if err != nil {
		log.Printf("[shadow-qualification] build %s failed: %v", profile.CapabilityKey, err)
		return
	}
	persisted, added, err := a.activity.store.AddShadowCapabilityQualification(qualification)
	if err != nil {
		log.Printf("[shadow-qualification] persist %s failed: %v", profile.CapabilityKey, err)
		return
	}
	if persisted.Status == "qualified" {
		// Qualification is evidence quality, never an authority grant or a
		// user-facing proposal. It may update provisional Self learning only.
		a.promoteSelfModelFromQualification(persisted, false)
	}
	if added {
		a.emitBrowserEvent("shadow_agent:qualification", persisted)
	}
}

func (a *App) qualifyLatestShadowCapability(capabilityKey string) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return
	}
	profile, err := a.activity.store.GetLatestShadowCapabilityProfile(capabilityKey)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		log.Printf("[shadow-qualification] latest profile %s failed: %v", capabilityKey, err)
		return
	}
	a.qualifyShadowCapability(profile)
}
