package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
)

type InitiativeStatus string

const (
	InitiativeStatusObserved  InitiativeStatus = "observed"
	InitiativeStatusShadowed  InitiativeStatus = "shadowed"
	InitiativeStatusProposed  InitiativeStatus = "proposed"
	InitiativeStatusApproved  InitiativeStatus = "approved"
	InitiativeStatusExecuting InitiativeStatus = "executing"
	InitiativeStatusSucceeded InitiativeStatus = "succeeded"
	InitiativeStatusFailed    InitiativeStatus = "failed"
	InitiativeStatusReviewed  InitiativeStatus = "reviewed"
	InitiativeStatusDeferred  InitiativeStatus = "deferred"
	InitiativeStatusDismissed InitiativeStatus = "dismissed"
)

type InitiativeAutonomyMode string

const (
	InitiativeModeShadow   InitiativeAutonomyMode = "shadow"
	InitiativeModeApproval InitiativeAutonomyMode = "approval"
	InitiativeModeAuto     InitiativeAutonomyMode = "auto"
)

type InitiativeMemoryState string

const (
	// Unverified is the default for an AI inference. It must never be injected
	// as durable personal memory or treated as authority.
	InitiativeMemoryUnverified InitiativeMemoryState = "unverified"
	// Eligible means the user confirmed or corrected the prediction. A future
	// self-model compiler may promote it after additional evidence.
	InitiativeMemoryEligible InitiativeMemoryState = "eligible"
	// Blocked means the user rejected the prediction. The underlying observed
	// facts remain intact; only this inferred judgment is disallowed.
	InitiativeMemoryBlocked  InitiativeMemoryState = "blocked"
	InitiativeMemoryPromoted InitiativeMemoryState = "promoted"
	InitiativeMemoryLegacy   InitiativeMemoryState = "legacy"
)

type InitiativeEvidence struct {
	Kind       string `json:"kind"`
	SourceKind string `json:"sourceKind,omitempty"`
	SourceID   string `json:"sourceId,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Quote      string `json:"quote,omitempty"`
	OccurredAt int64  `json:"occurredAt,omitempty"`
}

type InitiativeObservation struct {
	InitiativeID         string                          `json:"initiativeId"`
	SourceKind           string                          `json:"sourceKind"`
	SourceID             string                          `json:"sourceId"`
	PredictedAction      string                          `json:"predictedAction,omitempty"`
	Rationale            string                          `json:"rationale,omitempty"`
	Evidence             []InitiativeEvidence            `json:"evidence"`
	Confidence           float64                         `json:"confidence"`
	RawModelConfidence   *float64                        `json:"rawModelConfidence,omitempty"`
	AdjustedConfidence   *float64                        `json:"adjustedConfidence,omitempty"`
	ConfidenceAdjustment *InitiativeConfidenceAdjustment `json:"confidenceAdjustment,omitempty"`
	ObservedAt           int64                           `json:"observedAt"`
}

type InitiativeConfidenceAdjustment struct {
	PolicyVersion          string   `json:"policyVersion"`
	Applied                bool     `json:"applied"`
	Reason                 string   `json:"reason"`
	RawModelConfidence     float64  `json:"rawModelConfidence"`
	AdjustedConfidence     float64  `json:"adjustedConfidence"`
	CalibrationKind        string   `json:"calibrationKind"`
	CalibrationSourceKind  string   `json:"calibrationSourceKind"`
	CalibrationBand        string   `json:"calibrationBand,omitempty"`
	CalibrationReliability *float64 `json:"calibrationReliability,omitempty"`
	EvaluatedSamples       int      `json:"evaluatedSamples"`
	BlendWeight            float64  `json:"blendWeight"`
	MaxMovement            float64  `json:"maxMovement"`
	Delta                  float64  `json:"delta"`
}

type AutomationExecutionPlan struct {
	Name             string          `json:"name"`
	Description      string          `json:"description,omitempty"`
	Prompt           string          `json:"prompt"`
	ProjectDir       string          `json:"projectDir,omitempty"`
	ScheduleKind     string          `json:"scheduleKind"`
	ScheduleConfig   json.RawMessage `json:"scheduleConfig"`
	ScheduleEvidence string          `json:"scheduleEvidence,omitempty"`
}

type InitiativeExecutionPlan struct {
	Kind       string                   `json:"kind"`
	Automation *AutomationExecutionPlan `json:"automation,omitempty"`
}

type Initiative struct {
	ID                   string                          `json:"id"`
	Kind                 string                          `json:"kind"`
	Status               InitiativeStatus                `json:"status"`
	AutonomyMode         InitiativeAutonomyMode          `json:"autonomyMode"`
	SourceKind           string                          `json:"sourceKind"`
	SourceID             string                          `json:"sourceId"`
	Title                string                          `json:"title"`
	Description          string                          `json:"description"`
	Goal                 string                          `json:"goal"`
	UserIntent           string                          `json:"userIntent"`
	PredictedAction      string                          `json:"predictedAction"`
	Rationale            string                          `json:"rationale"`
	Confidence           float64                         `json:"confidence"`
	RawModelConfidence   *float64                        `json:"rawModelConfidence,omitempty"`
	AdjustedConfidence   *float64                        `json:"adjustedConfidence,omitempty"`
	ConfidenceAdjustment *InitiativeConfidenceAdjustment `json:"confidenceAdjustment,omitempty"`
	RiskLevel            string                          `json:"riskLevel"`
	Reversibility        string                          `json:"reversibility"`
	Evidence             []InitiativeEvidence            `json:"evidence"`
	ExecutionPlan        InitiativeExecutionPlan         `json:"executionPlan"`
	Fingerprint          string                          `json:"fingerprint"`
	ExecutionRef         string                          `json:"executionRef"`
	ChatSessionID        string                          `json:"chatSessionId"`
	MemoryState          InitiativeMemoryState           `json:"memoryState"`
	CreatedAt            int64                           `json:"createdAt"`
	UpdatedAt            int64                           `json:"updatedAt"`
	ResolvedAt           int64                           `json:"resolvedAt,omitempty"`
	ObservationCount     int                             `json:"observationCount"`
	LatestFeedback       *InitiativeFeedback             `json:"latestFeedback,omitempty"`
}

type InitiativeFeedback struct {
	ID              string `json:"id"`
	InitiativeID    string `json:"initiativeId"`
	Decision        string `json:"decision"`
	CorrectedAction string `json:"correctedAction,omitempty"`
	Reason          string `json:"reason,omitempty"`
	CreatedAt       int64  `json:"createdAt"`
}

type ReviewInitiativeInput struct {
	InitiativeID    string `json:"initiativeId"`
	Decision        string `json:"decision"`
	CorrectedAction string `json:"correctedAction,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

const (
	immediateShadowConfidence = 0.90
	twoObservationConfidence  = 0.75
)

func clampConfidence(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func requiredInitiativeObservations(confidence float64) int {
	if clampConfidence(confidence) >= twoObservationConfidence {
		return 2
	}
	return 3
}

func encodeInitiativeJSON(v any, fallback string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return string(b)
}

func decodeInitiativeJSON(raw string, dst any) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		log.Printf("[initiative] decode persisted JSON failed: %v", err)
	}
}

const initiativeSelectColumns = `
	id, kind, status, autonomy_mode, source_kind, source_id,
	title, description, goal, user_intent, predicted_action, rationale,
	confidence, raw_model_confidence, adjusted_confidence, confidence_adjustment_json,
	risk_level, reversibility, evidence_json, execution_plan_json,
	fingerprint, execution_ref, chat_session_id, memory_state,
	created_at, updated_at, resolved_at,
	(SELECT COUNT(*) FROM initiative_observations io WHERE io.initiative_id = initiatives.id)`

func scanInitiative(scanner interface{ Scan(dest ...any) error }) (Initiative, error) {
	var item Initiative
	var status, mode, memoryState string
	var evidenceJSON, planJSON, confidenceAdjustmentJSON string
	var resolvedAt sql.NullInt64
	var rawModelConfidence, adjustedConfidence sql.NullFloat64
	err := scanner.Scan(
		&item.ID, &item.Kind, &status, &mode, &item.SourceKind, &item.SourceID,
		&item.Title, &item.Description, &item.Goal, &item.UserIntent,
		&item.PredictedAction, &item.Rationale, &item.Confidence,
		&rawModelConfidence, &adjustedConfidence, &confidenceAdjustmentJSON,
		&item.RiskLevel, &item.Reversibility, &evidenceJSON, &planJSON,
		&item.Fingerprint, &item.ExecutionRef, &item.ChatSessionID, &memoryState,
		&item.CreatedAt, &item.UpdatedAt, &resolvedAt, &item.ObservationCount,
	)
	if err != nil {
		return Initiative{}, err
	}
	item.Status = InitiativeStatus(status)
	item.AutonomyMode = InitiativeAutonomyMode(mode)
	item.MemoryState = InitiativeMemoryState(memoryState)
	item.Confidence = clampConfidence(item.Confidence)
	if rawModelConfidence.Valid {
		value := clampConfidence(rawModelConfidence.Float64)
		item.RawModelConfidence = &value
	}
	if adjustedConfidence.Valid {
		value := clampConfidence(adjustedConfidence.Float64)
		item.AdjustedConfidence = &value
	}
	if item.RawModelConfidence != nil || item.AdjustedConfidence != nil {
		var adjustment InitiativeConfidenceAdjustment
		decodeInitiativeJSON(confidenceAdjustmentJSON, &adjustment)
		item.ConfidenceAdjustment = &adjustment
	}
	if resolvedAt.Valid {
		item.ResolvedAt = resolvedAt.Int64
	}
	decodeInitiativeJSON(evidenceJSON, &item.Evidence)
	decodeInitiativeJSON(planJSON, &item.ExecutionPlan)
	return item, nil
}

func (s *ActivityStore) SaveInitiative(item Initiative) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("initiative id is required")
	}
	if item.Status == "" {
		item.Status = InitiativeStatusShadowed
	}
	if item.AutonomyMode == "" {
		item.AutonomyMode = InitiativeModeShadow
	}
	if item.MemoryState == "" {
		item.MemoryState = InitiativeMemoryUnverified
	}
	now := time.Now().UnixMilli()
	if item.CreatedAt == 0 {
		item.CreatedAt = now
	}
	if item.UpdatedAt == 0 {
		item.UpdatedAt = item.CreatedAt
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin save initiative: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO initiatives (
			id, kind, status, autonomy_mode, source_kind, source_id,
			title, description, goal, user_intent, predicted_action, rationale,
			confidence, raw_model_confidence, adjusted_confidence, confidence_adjustment_json,
			risk_level, reversibility, evidence_json, execution_plan_json,
			fingerprint, execution_ref, chat_session_id, memory_state,
			created_at, updated_at, resolved_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Kind, item.Status, item.AutonomyMode, item.SourceKind, item.SourceID,
		item.Title, item.Description, item.Goal, item.UserIntent, item.PredictedAction, item.Rationale,
		clampConfidence(item.Confidence), nullableConfidence(item.RawModelConfidence),
		nullableConfidence(item.AdjustedConfidence),
		encodeInitiativeJSON(item.ConfidenceAdjustment, "{}"),
		item.RiskLevel, item.Reversibility,
		encodeInitiativeJSON(item.Evidence, "[]"), encodeInitiativeJSON(item.ExecutionPlan, "{}"),
		item.Fingerprint, item.ExecutionRef, item.ChatSessionID, item.MemoryState,
		item.CreatedAt, item.UpdatedAt, nullableUnixMillis(item.ResolvedAt),
	); err != nil {
		return fmt.Errorf("save initiative: %w", err)
	}
	if strings.TrimSpace(item.SourceKind) != "" && strings.TrimSpace(item.SourceID) != "" {
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO initiative_observations (
				initiative_id, source_kind, source_id, evidence_json, confidence,
				raw_model_confidence, adjusted_confidence, confidence_adjustment_json,
				observed_at, predicted_action, rationale
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ID, item.SourceKind, item.SourceID,
			encodeInitiativeJSON(item.Evidence, "[]"), clampConfidence(item.Confidence),
			nullableConfidence(item.RawModelConfidence), nullableConfidence(item.AdjustedConfidence),
			encodeInitiativeJSON(item.ConfidenceAdjustment, "{}"), item.CreatedAt,
			item.PredictedAction, item.Rationale,
		); err != nil {
			return fmt.Errorf("save initiative observation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit initiative: %w", err)
	}
	return nil
}

func (s *ActivityStore) AddInitiativeObservation(initiativeID string, candidate Initiative, immediate bool) (Initiative, bool, error) {
	if strings.TrimSpace(initiativeID) == "" {
		return Initiative{}, false, errors.New("initiative id is required")
	}
	if strings.TrimSpace(candidate.SourceKind) == "" || strings.TrimSpace(candidate.SourceID) == "" {
		return Initiative{}, false, errors.New("initiative observation source is required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Initiative{}, false, fmt.Errorf("begin initiative observation: %w", err)
	}
	defer tx.Rollback()

	current, err := scanInitiative(tx.QueryRow(`SELECT `+initiativeSelectColumns+` FROM initiatives WHERE id = ?`, initiativeID))
	if err != nil {
		return Initiative{}, false, err
	}
	if current.Status != InitiativeStatusObserved {
		return current, false, nil
	}

	observedAt := candidate.CreatedAt
	if observedAt == 0 {
		observedAt = time.Now().UnixMilli()
	}
	result, err := tx.Exec(`
		INSERT OR IGNORE INTO initiative_observations (
			initiative_id, source_kind, source_id, evidence_json, confidence,
			raw_model_confidence, adjusted_confidence, confidence_adjustment_json,
			observed_at, predicted_action, rationale
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		initiativeID, candidate.SourceKind, candidate.SourceID,
		encodeInitiativeJSON(candidate.Evidence, "[]"), clampConfidence(candidate.Confidence),
		nullableConfidence(candidate.RawModelConfidence), nullableConfidence(candidate.AdjustedConfidence),
		encodeInitiativeJSON(candidate.ConfidenceAdjustment, "{}"), observedAt,
		candidate.PredictedAction, candidate.Rationale,
	)
	if err != nil {
		return Initiative{}, false, fmt.Errorf("add initiative observation: %w", err)
	}
	added, err := result.RowsAffected()
	if err != nil {
		return Initiative{}, false, fmt.Errorf("initiative observation rows affected: %w", err)
	}
	if added == 0 {
		if err := tx.Rollback(); err != nil {
			return Initiative{}, false, fmt.Errorf("rollback duplicate initiative observation: %w", err)
		}
		item, getErr := s.GetInitiative(initiativeID)
		return item, false, getErr
	}

	var observationCount int
	var aggregateRawConfidence, aggregateAdjustedConfidence float64
	if err := tx.QueryRow(`
		SELECT COUNT(*),
			COALESCE(AVG(COALESCE(raw_model_confidence, confidence)), 0),
			COALESCE(AVG(COALESCE(adjusted_confidence, confidence)), 0)
		FROM initiative_observations
		WHERE initiative_id = ?`, initiativeID).Scan(
		&observationCount, &aggregateRawConfidence, &aggregateAdjustedConfidence,
	); err != nil {
		return Initiative{}, false, fmt.Errorf("summarize initiative observations: %w", err)
	}

	status := InitiativeStatusObserved
	if immediate || observationCount >= requiredInitiativeObservations(aggregateAdjustedConfidence) {
		status = InitiativeStatusShadowed
	}
	aggregateRawConfidence = clampConfidence(aggregateRawConfidence)
	aggregateAdjustedConfidence = clampConfidence(aggregateAdjustedConfidence)
	aggregateAdjustment := aggregateInitiativeConfidenceAdjustment(
		candidate.ConfidenceAdjustment,
		aggregateRawConfidence,
		aggregateAdjustedConfidence,
	)
	now := time.Now().UnixMilli()
	evidence := mergeInitiativeEvidence(current.Evidence, candidate.Evidence)
	if _, err := tx.Exec(`
		UPDATE initiatives SET
			status = ?, title = ?, description = ?, goal = ?, user_intent = ?,
			predicted_action = ?, rationale = ?, confidence = ?,
			raw_model_confidence = ?, adjusted_confidence = ?, confidence_adjustment_json = ?,
			risk_level = ?,
			reversibility = ?, evidence_json = ?, execution_plan_json = ?,
			updated_at = ?
		WHERE id = ? AND status = 'observed'`,
		status, candidate.Title, candidate.Description, candidate.Goal, candidate.UserIntent,
		candidate.PredictedAction, candidate.Rationale, aggregateAdjustedConfidence,
		aggregateRawConfidence, aggregateAdjustedConfidence,
		encodeInitiativeJSON(aggregateAdjustment, "{}"),
		candidate.RiskLevel, candidate.Reversibility,
		encodeInitiativeJSON(evidence, "[]"), encodeInitiativeJSON(candidate.ExecutionPlan, "{}"),
		now, initiativeID,
	); err != nil {
		return Initiative{}, false, fmt.Errorf("update observed initiative: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Initiative{}, false, fmt.Errorf("commit initiative observation: %w", err)
	}
	item, err := s.GetInitiative(initiativeID)
	return item, true, err
}

func mergeInitiativeEvidence(existing, added []InitiativeEvidence) []InitiativeEvidence {
	merged := make([]InitiativeEvidence, 0, len(existing)+len(added))
	seen := make(map[string]struct{}, len(existing)+len(added))
	for _, evidence := range append(existing, added...) {
		key := strings.Join([]string{
			evidence.Kind,
			evidence.SourceKind,
			evidence.SourceID,
			evidence.Summary,
			evidence.Quote,
			fmt.Sprint(evidence.OccurredAt),
		}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, evidence)
	}
	return merged
}

func nullableUnixMillis(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullableConfidence(v *float64) any {
	if v == nil {
		return nil
	}
	return clampConfidence(*v)
}

func aggregateInitiativeConfidenceAdjustment(
	adjustment *InitiativeConfidenceAdjustment,
	rawConfidence, adjustedConfidence float64,
) *InitiativeConfidenceAdjustment {
	if adjustment == nil {
		return nil
	}
	result := *adjustment
	result.RawModelConfidence = clampConfidence(rawConfidence)
	result.AdjustedConfidence = clampConfidence(adjustedConfidence)
	result.Delta = result.AdjustedConfidence - result.RawModelConfidence
	result.Applied = result.Delta != 0
	if !result.Applied && result.Reason == "ready_band" {
		result.Reason = "no_change"
	}
	return &result
}

func (s *ActivityStore) GetInitiative(id string) (Initiative, error) {
	item, err := scanInitiative(s.db.QueryRow(`SELECT `+initiativeSelectColumns+` FROM initiatives WHERE id = ?`, id))
	if err != nil {
		return Initiative{}, err
	}
	feedback, feedbackErr := s.GetLatestInitiativeFeedback(id)
	if feedbackErr == nil {
		item.LatestFeedback = &feedback
	} else if !errors.Is(feedbackErr, sql.ErrNoRows) {
		return Initiative{}, feedbackErr
	}
	return item, nil
}

func scanInitiativeObservation(scanner interface{ Scan(dest ...any) error }) (InitiativeObservation, error) {
	var observation InitiativeObservation
	var evidenceJSON, confidenceAdjustmentJSON string
	var rawModelConfidence, adjustedConfidence sql.NullFloat64
	err := scanner.Scan(
		&observation.InitiativeID,
		&observation.SourceKind,
		&observation.SourceID,
		&observation.PredictedAction,
		&observation.Rationale,
		&evidenceJSON,
		&observation.Confidence,
		&rawModelConfidence,
		&adjustedConfidence,
		&confidenceAdjustmentJSON,
		&observation.ObservedAt,
	)
	if err != nil {
		return InitiativeObservation{}, err
	}
	observation.Confidence = clampConfidence(observation.Confidence)
	if rawModelConfidence.Valid {
		value := clampConfidence(rawModelConfidence.Float64)
		observation.RawModelConfidence = &value
	}
	if adjustedConfidence.Valid {
		value := clampConfidence(adjustedConfidence.Float64)
		observation.AdjustedConfidence = &value
	}
	if observation.RawModelConfidence != nil || observation.AdjustedConfidence != nil {
		var adjustment InitiativeConfidenceAdjustment
		decodeInitiativeJSON(confidenceAdjustmentJSON, &adjustment)
		observation.ConfidenceAdjustment = &adjustment
	}
	decodeInitiativeJSON(evidenceJSON, &observation.Evidence)
	return observation, nil
}

func (s *ActivityStore) ListInitiativeObservations(initiativeID string) ([]InitiativeObservation, error) {
	rows, err := s.db.Query(`
		SELECT initiative_id, source_kind, source_id, predicted_action, rationale,
			evidence_json, confidence, raw_model_confidence, adjusted_confidence,
			confidence_adjustment_json, observed_at
		FROM initiative_observations
		WHERE initiative_id = ?
		ORDER BY observed_at ASC, rowid ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	observations := make([]InitiativeObservation, 0)
	for rows.Next() {
		observation, err := scanInitiativeObservation(rows)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

func (s *ActivityStore) ListInitiativesByStatus(status string, limit int) ([]Initiative, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + initiativeSelectColumns + ` FROM initiatives`
	args := []any{}
	if status == "actionable" {
		query += ` WHERE status = 'proposed'
			AND (
				memory_state = 'eligible'
				OR EXISTS (
					SELECT 1
					FROM capability_manifests
					WHERE capability_manifests.initiative_id = initiatives.id
				)
			)`
	} else if strings.TrimSpace(status) != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list initiatives: %w", err)
	}
	defer rows.Close()
	items := make([]Initiative, 0)
	for rows.Next() {
		item, err := scanInitiative(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ActivityStore) ListRecentlyReviewedInitiatives(limit int) ([]Initiative, error) {
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	rows, err := s.db.Query(`SELECT `+initiativeSelectColumns+`
		FROM initiatives
		WHERE status IN ('reviewed', 'proposed', 'approved', 'succeeded', 'failed')
			AND memory_state = 'eligible'
		ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list reviewed initiatives: %w", err)
	}
	defer rows.Close()
	items := make([]Initiative, 0)
	for rows.Next() {
		item, err := scanInitiative(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range items {
		if feedback, feedbackErr := s.GetLatestInitiativeFeedback(items[i].ID); feedbackErr == nil {
			items[i].LatestFeedback = &feedback
		}
	}
	return items, nil
}

func (s *ActivityStore) ListActiveInitiatives(limit int) ([]Initiative, error) {
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	rows, err := s.db.Query(`SELECT `+initiativeSelectColumns+`
		FROM initiatives
		WHERE status IN ('shadowed', 'proposed', 'approved', 'executing', 'succeeded', 'failed', 'deferred', 'reviewed')
		ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list active initiatives: %w", err)
	}
	defer rows.Close()
	items := make([]Initiative, 0)
	for rows.Next() {
		item, err := scanInitiative(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ActivityStore) ListRecentlyBlockedInitiatives(limit int) ([]Initiative, error) {
	if limit <= 0 || limit > 50 {
		limit = 8
	}
	rows, err := s.db.Query(`SELECT `+initiativeSelectColumns+`
		FROM initiatives
		WHERE memory_state = 'blocked'
		ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list blocked initiatives: %w", err)
	}
	defer rows.Close()
	items := make([]Initiative, 0)
	for rows.Next() {
		item, err := scanInitiative(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range items {
		if feedback, feedbackErr := s.GetLatestInitiativeFeedback(items[i].ID); feedbackErr == nil {
			items[i].LatestFeedback = &feedback
		}
	}
	return items, nil
}

func (s *ActivityStore) FindOpenInitiativeByFingerprint(fingerprint string) (Initiative, error) {
	return scanInitiative(s.db.QueryRow(`SELECT `+initiativeSelectColumns+`
		FROM initiatives
		WHERE fingerprint = ? AND status IN ('observed', 'shadowed', 'proposed', 'approved', 'executing', 'succeeded', 'failed', 'deferred', 'reviewed')
		ORDER BY created_at DESC LIMIT 1`, fingerprint))
}

func (s *ActivityStore) GetInitiativeByExecutionRef(executionRef string) (Initiative, error) {
	return scanInitiative(s.db.QueryRow(`SELECT `+initiativeSelectColumns+`
		FROM initiatives WHERE execution_ref = ? ORDER BY created_at DESC LIMIT 1`, executionRef))
}

func (s *ActivityStore) GetInitiativeBySource(sourceKind, sourceID string) (Initiative, error) {
	return scanInitiative(s.db.QueryRow(`SELECT `+initiativeSelectColumns+`
		FROM initiatives
		WHERE id = (
			SELECT initiative_id
			FROM initiative_observations
			WHERE source_kind = ? AND source_id = ?
			ORDER BY observed_at DESC
			LIMIT 1
		)`, sourceKind, sourceID))
}

func (s *ActivityStore) GetLatestInitiativeFeedback(initiativeID string) (InitiativeFeedback, error) {
	var f InitiativeFeedback
	err := s.db.QueryRow(`SELECT id, initiative_id, decision, corrected_action, reason, created_at
		FROM initiative_feedback
		WHERE initiative_id = ?
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1`, initiativeID).
		Scan(&f.ID, &f.InitiativeID, &f.Decision, &f.CorrectedAction, &f.Reason, &f.CreatedAt)
	return f, err
}

func (s *ActivityStore) ListInitiativeFeedback(initiativeID string) ([]InitiativeFeedback, error) {
	rows, err := s.db.Query(`
		SELECT id, initiative_id, decision, corrected_action, reason, created_at
		FROM initiative_feedback
		WHERE initiative_id = ?
		ORDER BY created_at ASC, rowid ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	feedback := make([]InitiativeFeedback, 0)
	for rows.Next() {
		var item InitiativeFeedback
		if err := rows.Scan(
			&item.ID,
			&item.InitiativeID,
			&item.Decision,
			&item.CorrectedAction,
			&item.Reason,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		feedback = append(feedback, item)
	}
	return feedback, rows.Err()
}

func (s *ActivityStore) ReviewInitiative(input ReviewInitiativeInput) (Initiative, error) {
	decision := strings.ToLower(strings.TrimSpace(input.Decision))
	var status InitiativeStatus
	var memoryState InitiativeMemoryState
	switch decision {
	case "correct", "edit":
		status = InitiativeStatusProposed
		memoryState = InitiativeMemoryEligible
	case "wrong":
		status = InitiativeStatusDismissed
		memoryState = InitiativeMemoryBlocked
	case "not_now":
		status = InitiativeStatusDeferred
		memoryState = InitiativeMemoryUnverified
	default:
		return Initiative{}, fmt.Errorf("unsupported initiative decision %q", input.Decision)
	}
	if decision == "edit" && strings.TrimSpace(input.CorrectedAction) == "" {
		return Initiative{}, errors.New("corrected action is required for edit feedback")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Initiative{}, fmt.Errorf("begin initiative review: %w", err)
	}
	defer tx.Rollback()
	var currentStatus string
	var planJSON string
	if err := tx.QueryRow(`
		SELECT status, execution_plan_json
		FROM initiatives
		WHERE id = ?`, input.InitiativeID).Scan(&currentStatus, &planJSON); err != nil {
		return Initiative{}, err
	}
	var plan InitiativeExecutionPlan
	decodeInitiativeJSON(planJSON, &plan)
	if (decision == "correct" || decision == "edit") && plan.Automation == nil {
		status = InitiativeStatusReviewed
	}
	switch decision {
	case "correct", "edit":
		if currentStatus != string(InitiativeStatusShadowed) && currentStatus != string(InitiativeStatusDeferred) {
			return Initiative{}, errors.New("initiative is not awaiting shadow review")
		}
	case "wrong", "not_now":
		if currentStatus != string(InitiativeStatusShadowed) &&
			currentStatus != string(InitiativeStatusDeferred) &&
			currentStatus != string(InitiativeStatusProposed) {
			return Initiative{}, errors.New("initiative is not awaiting review")
		}
	}

	predictedAction := ""
	if decision == "edit" {
		predictedAction = strings.TrimSpace(input.CorrectedAction)
		if plan.Automation != nil {
			plan.Automation.Prompt = predictedAction
			planJSON = encodeInitiativeJSON(plan, planJSON)
		}
	}
	now := time.Now().UnixMilli()
	feedbackID := uuid.New().String()
	if _, err := tx.Exec(`INSERT INTO initiative_feedback
		(id, initiative_id, decision, corrected_action, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, feedbackID, input.InitiativeID, decision,
		strings.TrimSpace(input.CorrectedAction), strings.TrimSpace(input.Reason), now); err != nil {
		return Initiative{}, fmt.Errorf("save initiative feedback: %w", err)
	}
	resolvedAt := any(nil)
	if status == InitiativeStatusDismissed || status == InitiativeStatusReviewed {
		resolvedAt = now
	}
	result, err := tx.Exec(`UPDATE initiatives
		SET status = ?, memory_state = ?, predicted_action = CASE WHEN ? != '' THEN ? ELSE predicted_action END,
			execution_plan_json = ?, updated_at = ?, resolved_at = ?
		WHERE id = ? AND status = ?`,
		status, memoryState, predictedAction, predictedAction, planJSON, now, resolvedAt, input.InitiativeID, currentStatus)
	if err != nil {
		return Initiative{}, fmt.Errorf("update initiative review state: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Initiative{}, fmt.Errorf("initiative review rows affected: %w", err)
	}
	if affected == 0 {
		return Initiative{}, errors.New("initiative is not awaiting shadow review")
	}
	if err := tx.Commit(); err != nil {
		return Initiative{}, fmt.Errorf("commit initiative review: %w", err)
	}
	return s.GetInitiative(input.InitiativeID)
}

func (s *ActivityStore) RecordInitiativeApproval(initiativeID, executionRef string) (Initiative, error) {
	if strings.TrimSpace(executionRef) == "" {
		return Initiative{}, errors.New("initiative execution reference is required")
	}
	result, err := s.db.Exec(`
		UPDATE initiatives
		SET status = 'approved', autonomy_mode = 'approval',
			memory_state = 'eligible',
			execution_ref = CASE WHEN execution_ref = '' THEN ? ELSE execution_ref END,
			updated_at = ?
		WHERE id = ? AND status IN ('proposed', 'approved')
			AND (
				memory_state = 'eligible'
				OR EXISTS (
					SELECT 1 FROM capability_manifests cm
					WHERE cm.initiative_id = initiatives.id
				)
			)`,
		executionRef, time.Now().UnixMilli(), initiativeID)
	if err != nil {
		return Initiative{}, fmt.Errorf("record initiative approval: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Initiative{}, fmt.Errorf("initiative approval rows affected: %w", err)
	}
	if affected == 0 {
		return Initiative{}, errors.New("initiative is not awaiting execution approval")
	}
	return s.GetInitiative(initiativeID)
}

func (s *ActivityStore) LinkInitiativeArtifactBySource(sourceKind, sourceID, artifactKind, artifactID, relation string) error {
	if sourceKind == "" || sourceID == "" || artifactKind == "" || artifactID == "" {
		return nil
	}
	initiative, err := s.GetInitiativeBySource(sourceKind, sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO initiative_artifact_links
		(initiative_id, artifact_kind, artifact_id, relation, created_at)
		VALUES (?, ?, ?, ?, ?)`, initiative.ID, artifactKind, artifactID, relation, time.Now().UnixMilli())
	return err
}

func (s *ActivityStore) RecoverInterruptedInitiatives(maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge).UnixMilli()
	_, err := s.db.Exec(`UPDATE initiatives
		SET status = 'approved', updated_at = ?
		WHERE status = 'executing' AND updated_at < ?`, time.Now().UnixMilli(), cutoff)
	return err
}

func (a *App) ListInitiatives(status string, limit int) []Initiative {
	if a.activity == nil || a.activity.store == nil {
		return []Initiative{}
	}
	items, err := a.activity.store.ListInitiativesByStatus(status, limit)
	if err != nil {
		log.Printf("[initiative] list failed: %v", err)
		return []Initiative{}
	}
	return items
}

func (a *App) ReviewInitiative(input ReviewInitiativeInput) (Initiative, error) {
	if a.activity == nil || a.activity.store == nil {
		return Initiative{}, errors.New("activity store is unavailable")
	}
	current, err := a.activity.store.GetInitiative(input.InitiativeID)
	if err != nil {
		return Initiative{}, err
	}
	if current.Status != InitiativeStatusProposed {
		return Initiative{}, errors.New("internal shadow predictions are not user-reviewable")
	}
	decision := strings.ToLower(strings.TrimSpace(input.Decision))
	if decision != "wrong" && decision != "not_now" {
		return Initiative{}, errors.New("initiative proposal only accepts dismiss or defer feedback")
	}
	item, err := a.activity.store.ReviewInitiative(input)
	if err != nil {
		return Initiative{}, err
	}
	a.emitBrowserEvent("initiative:updated", item)
	a.syncInitiativeLearningMemory(item.ID)
	if a.telemetry != nil {
		a.telemetry.Capture("initiative.reviewed", map[string]any{
			"initiative_id": item.ID,
			"decision":      strings.ToLower(strings.TrimSpace(input.Decision)),
			"source_kind":   item.SourceKind,
			"memory_state":  item.MemoryState,
		})
	}
	return item, nil
}

func cronInputForInitiative(item Initiative) (CronJobInput, error) {
	if item.Kind != "automation" || item.ExecutionPlan.Automation == nil {
		return CronJobInput{}, errors.New("initiative does not contain an automation plan")
	}
	plan := item.ExecutionPlan.Automation
	if strings.TrimSpace(plan.Name) == "" || strings.TrimSpace(plan.Prompt) == "" {
		return CronJobInput{}, errors.New("initiative automation plan is incomplete")
	}
	scheduleConfig := plan.ScheduleConfig
	if len(scheduleConfig) == 0 {
		scheduleConfig = json.RawMessage(`{}`)
	}
	if err := validateAutomationFrequency(plan.ScheduleKind, scheduleConfig); err != nil {
		return CronJobInput{}, fmt.Errorf("initiative schedule is invalid: %w", err)
	}
	targetConfig, err := json.Marshal(map[string]string{
		"prompt":      plan.Prompt,
		"project_dir": plan.ProjectDir,
	})
	if err != nil {
		return CronJobInput{}, fmt.Errorf("encode initiative target: %w", err)
	}
	description := strings.TrimSpace(plan.Description)
	if description == "" {
		description = item.Description
	}
	return CronJobInput{
		Name:             plan.Name,
		Description:      description,
		ScheduleKind:     plan.ScheduleKind,
		ScheduleConfig:   scheduleConfig,
		ScheduleEvidence: plan.ScheduleEvidence,
		TargetKind:       "agent",
		TargetConfig:     targetConfig,
		RetryPolicy:      json.RawMessage(`{}`),
		SourceSessionID:  item.SourceID,
		SourcePlatform:   item.SourceKind,
		SurfaceKind:      "scheduled",
	}, nil
}

func (a *App) findCronJobForInitiative(input CronJobInput) (string, error) {
	if a.store == nil {
		return "", errors.New("automation store is unavailable")
	}
	fingerprint := computeAutomationFingerprint(input.Description, input.TargetKind, string(input.TargetConfig))
	if fingerprint == "" {
		return "", nil
	}
	jobs, err := a.store.ListCronJobs(context.Background())
	if err != nil {
		return "", fmt.Errorf("list automations for initiative approval: %w", err)
	}
	for _, job := range jobs {
		if job.Fingerprint == fingerprint {
			return job.ID, nil
		}
	}
	return "", nil
}

func (a *App) ApproveInitiative(initiativeID string) (Initiative, error) {
	if a.activity == nil || a.activity.store == nil {
		return Initiative{}, errors.New("activity store is unavailable")
	}
	item, err := a.activity.store.GetInitiative(initiativeID)
	if err != nil {
		return Initiative{}, err
	}
	if item.Status == InitiativeStatusApproved && item.ExecutionRef != "" {
		return item, nil
	}
	if item.Status != InitiativeStatusProposed && item.Status != InitiativeStatusApproved {
		return Initiative{}, errors.New("initiative is not awaiting execution approval")
	}

	manifest, manifestErr := a.activity.store.GetCapabilityManifest(item.ID)
	switch {
	case manifestErr == nil:
		if !manifest.Grantable {
			return Initiative{}, fmt.Errorf(
				"initiative requires per-run approval and cannot run unattended: %s",
				strings.Join(manifest.BlockReasons, ", "),
			)
		}
		if _, err := a.activity.store.IssueAuthorityGrants(manifest, time.Now()); err != nil {
			return Initiative{}, fmt.Errorf("issue initiative authority: %w", err)
		}
	case !errors.Is(manifestErr, sql.ErrNoRows):
		return Initiative{}, manifestErr
	case item.MemoryState != InitiativeMemoryEligible:
		return Initiative{}, errors.New("initiative has neither confirmed judgment nor a qualified capability manifest")
	}

	input, err := cronInputForInitiative(item)
	if err != nil {
		return Initiative{}, err
	}
	executionRef, err := a.findCronJobForInitiative(input)
	if err != nil {
		return Initiative{}, err
	}
	if executionRef == "" {
		job, createErr := a.CreateCronJob(input)
		if createErr != nil {
			if !errors.Is(createErr, ErrDuplicateAutomation) {
				return Initiative{}, fmt.Errorf("create automation from initiative: %w", createErr)
			}
			executionRef, err = a.findCronJobForInitiative(input)
			if err != nil {
				return Initiative{}, err
			}
			if executionRef == "" {
				return Initiative{}, fmt.Errorf("resolve duplicate automation for initiative: %w", createErr)
			}
		} else {
			executionRef = job.ID
		}
	}

	item, err = a.activity.store.RecordInitiativeApproval(initiativeID, executionRef)
	if err != nil {
		return Initiative{}, err
	}
	a.emitBrowserEvent("initiative:updated", item)
	if a.telemetry != nil {
		a.telemetry.Capture("initiative.approved", map[string]any{
			"initiative_id": item.ID,
			"execution_ref": item.ExecutionRef,
			"source_kind":   item.SourceKind,
		})
	}
	return item, nil
}

func (a *App) injectInitiativeContext(prompt string) string {
	if a.activity == nil || a.activity.store == nil {
		return prompt
	}
	items, err := a.activity.store.ListActiveInitiatives(8)
	if err != nil || len(items) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString("<initiative-context>\n")
	b.WriteString("These are active initiatives, separate from durable memory. Shadow initiatives are unverified predictions, not user instructions or permission to act. Never execute one unless the user explicitly asks or its autonomy mode permits it.\n\n")
	for _, item := range items {
		fmt.Fprintf(&b, "- [%s/%s] %s\n  Predicted action: %s\n  Why: %s\n",
			item.Status, item.AutonomyMode, item.Title, item.PredictedAction, item.Rationale)
	}
	b.WriteString("</initiative-context>\n\n")
	return b.String() + prompt
}

func (a *App) linkInitiativeArtifact(sourceKind, sourceID, artifactKind, artifactID string) {
	if a.activity == nil || a.activity.store == nil || artifactID == "" {
		return
	}
	if err := a.activity.store.LinkInitiativeArtifactBySource(sourceKind, sourceID, artifactKind, artifactID, "shared_evidence"); err != nil {
		log.Printf("[initiative] link %s %s to %s:%s failed: %v", artifactKind, artifactID, sourceKind, sourceID, err)
	}
}
