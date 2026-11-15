package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	shadowDebugMaxEntries          = 500
	shadowDebugMaxAge              = 7 * 24 * time.Hour
	shadowCorrelationPromptVersion = "shadow-comparator-v1"
)

type ShadowCorrelationCandidateAudit struct {
	ShadowRunID   string `json:"shadowRunId"`
	CapabilityKey string `json:"capabilityKey"`
	Relevance     int    `json:"relevance"`
	PayloadBytes  int    `json:"payloadBytes"`
	Selected      bool   `json:"selected"`
	Reason        string `json:"reason,omitempty"`
}

type ShadowCorrelationAttempt struct {
	ID                string                            `json:"id"`
	SourceKind        string                            `json:"sourceKind"`
	SourceID          string                            `json:"sourceId"`
	ObservedAt        int64                             `json:"observedAt"`
	ObservedSummary   string                            `json:"observedSummary,omitempty"`
	Status            string                            `json:"status"`
	Reason            string                            `json:"reason,omitempty"`
	CandidateCount    int                               `json:"candidateCount"`
	SelectedCount     int                               `json:"selectedCount"`
	ReportedMatches   int                               `json:"reportedMatches"`
	PersistedRunIDs   []string                          `json:"persistedRunIds,omitempty"`
	Candidates        []ShadowCorrelationCandidateAudit `json:"candidates,omitempty"`
	ComparatorPrompt  string                            `json:"comparatorPrompt,omitempty"`
	ComparatorHash    string                            `json:"comparatorHash,omitempty"`
	ComparatorVersion string                            `json:"comparatorVersion"`
	EvaluatorModel    string                            `json:"evaluatorModel,omitempty"`
	EvaluatorRaw      string                            `json:"evaluatorRaw,omitempty"`
	Error             string                            `json:"error,omitempty"`
	CreatedAt         int64                             `json:"createdAt"`
}

type ShadowRunDebugSummary struct {
	ID                    string  `json:"id"`
	CapabilityKey         string  `json:"capabilityKey"`
	Status                string  `json:"status"`
	SourceKind            string  `json:"sourceKind"`
	SourceID              string  `json:"sourceId"`
	PredictedAction       string  `json:"predictedAction"`
	ExpectedResult        string  `json:"expectedResult"`
	ActionConfidence      float64 `json:"actionConfidence"`
	ObservationCount      int     `json:"observationCount"`
	EvaluationCount       int     `json:"evaluationCount"`
	LastObservationAt     int64   `json:"lastObservationAt,omitempty"`
	ObservationHorizonEnd int64   `json:"observationHorizonEnd"`
	CreatedAt             int64   `json:"createdAt"`
}

type ShadowMemoryProjectionDebug struct {
	Path    string `json:"path,omitempty"`
	Exists  bool   `json:"exists"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

type ShadowRunDebugDetail struct {
	Detail            ShadowRunDetail             `json:"detail"`
	ExecutionAttempts []ShadowExecutionAttempt    `json:"executionAttempts"`
	MemoryProjection  ShadowMemoryProjectionDebug `json:"memoryProjection"`
}

type ShadowAgentDebugSnapshot struct {
	GeneratedAt    int64                           `json:"generatedAt"`
	Runs           []ShadowRunDebugSummary         `json:"runs"`
	Profiles       []ShadowCapabilityProfile       `json:"profiles"`
	Qualifications []ShadowCapabilityQualification `json:"qualifications"`
	SelfModel      []SelfModelEntry                `json:"selfModel"`
	Attempts       []ShadowCorrelationAttempt      `json:"attempts"`
}

func normalizeShadowCorrelationAttempt(input ShadowCorrelationAttempt) (ShadowCorrelationAttempt, error) {
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.ComparatorVersion = strings.TrimSpace(input.ComparatorVersion)
	if input.SourceKind == "" || input.SourceID == "" {
		return ShadowCorrelationAttempt{}, errors.New("shadow correlation attempt requires a source")
	}
	if input.ObservedAt == 0 {
		return ShadowCorrelationAttempt{}, errors.New("shadow correlation attempt requires an observed timestamp")
	}
	switch input.Status {
	case "no_candidates", "completed", "error":
	default:
		return ShadowCorrelationAttempt{}, fmt.Errorf("unsupported shadow correlation attempt status %q", input.Status)
	}
	if input.ComparatorVersion == "" {
		input.ComparatorVersion = shadowCorrelationPromptVersion
	}
	if input.ComparatorPrompt != "" {
		expected := sha256Hex([]byte(input.ComparatorPrompt))
		if input.ComparatorHash != "" && input.ComparatorHash != expected {
			return ShadowCorrelationAttempt{}, errors.New("shadow correlation attempt prompt hash does not match")
		}
		input.ComparatorHash = expected
	}
	input.CandidateCount = len(input.Candidates)
	input.SelectedCount = 0
	for _, candidate := range input.Candidates {
		if candidate.Selected {
			input.SelectedCount++
		}
	}
	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.CreatedAt == 0 {
		input.CreatedAt = time.Now().UnixMilli()
	}
	if input.PersistedRunIDs == nil {
		input.PersistedRunIDs = []string{}
	}
	if input.Candidates == nil {
		input.Candidates = []ShadowCorrelationCandidateAudit{}
	}
	return input, nil
}

func (s *ActivityStore) AddShadowCorrelationAttempt(input ShadowCorrelationAttempt) (ShadowCorrelationAttempt, error) {
	input, err := normalizeShadowCorrelationAttempt(input)
	if err != nil {
		return ShadowCorrelationAttempt{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ShadowCorrelationAttempt{}, fmt.Errorf("encode shadow correlation attempt: %w", err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO shadow_correlation_attempts (
			id, source_kind, source_id, observed_at, status,
			candidate_count, selected_count, match_count, attempt_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.ID,
		input.SourceKind,
		input.SourceID,
		input.ObservedAt,
		input.Status,
		input.CandidateCount,
		input.SelectedCount,
		input.ReportedMatches,
		string(payload),
		input.CreatedAt,
	); err != nil {
		return ShadowCorrelationAttempt{}, fmt.Errorf("save shadow correlation attempt: %w", err)
	}
	if err := s.PruneShadowCorrelationAttempts(); err != nil {
		return input, fmt.Errorf("prune shadow correlation attempts: %w", err)
	}
	return input, nil
}

func scanShadowCorrelationAttempt(scanner interface{ Scan(dest ...any) error }) (ShadowCorrelationAttempt, error) {
	var id, raw string
	var createdAt int64
	if err := scanner.Scan(&id, &raw, &createdAt); err != nil {
		return ShadowCorrelationAttempt{}, err
	}
	var item ShadowCorrelationAttempt
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return ShadowCorrelationAttempt{}, fmt.Errorf("decode shadow correlation attempt %s: %w", id, err)
	}
	item.ID = id
	item.CreatedAt = createdAt
	return item, nil
}

func (s *ActivityStore) ListShadowCorrelationAttempts(limit int) ([]ShadowCorrelationAttempt, error) {
	if limit <= 0 || limit > shadowDebugMaxEntries {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT id, attempt_json, created_at
		FROM shadow_correlation_attempts
		ORDER BY created_at DESC, rowid DESC
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShadowCorrelationAttempt, 0, limit)
	for rows.Next() {
		item, err := scanShadowCorrelationAttempt(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *ActivityStore) PruneShadowCorrelationAttempts() error {
	cutoff := time.Now().Add(-shadowDebugMaxAge).UnixMilli()
	if _, err := s.db.Exec(`DELETE FROM shadow_correlation_attempts WHERE created_at < ?`, cutoff); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		DELETE FROM shadow_correlation_attempts
		WHERE id NOT IN (
			SELECT id FROM shadow_correlation_attempts
			ORDER BY created_at DESC, rowid DESC
			LIMIT ?
		)`,
		shadowDebugMaxEntries,
	)
	return err
}

func (s *ActivityStore) ListShadowRunDebugSummaries(limit int, now int64) ([]ShadowRunDebugSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	rows, err := s.db.Query(`
		SELECT
			r.id, r.capability_key, r.source_kind, r.source_id,
			r.prediction_json, r.observation_horizon_end, r.created_at,
			(
				SELECT COUNT(*)
				FROM shadow_run_observations observation
				WHERE observation.shadow_run_id = r.id
			),
			(
				SELECT COUNT(*)
				FROM shadow_run_evaluations evaluation
				WHERE evaluation.shadow_run_id = r.id
			),
			(
				SELECT COUNT(DISTINCT evaluation.observation_id)
				FROM shadow_run_evaluations evaluation
				WHERE evaluation.shadow_run_id = r.id
			),
			COALESCE((
				SELECT MAX(observation.observed_at)
				FROM shadow_run_observations observation
				WHERE observation.shadow_run_id = r.id
			), 0)
		FROM shadow_runs r
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShadowRunDebugSummary, 0, limit)
	for rows.Next() {
		var item ShadowRunDebugSummary
		var predictionJSON string
		var evaluatedObservationCount int
		if err := rows.Scan(
			&item.ID,
			&item.CapabilityKey,
			&item.SourceKind,
			&item.SourceID,
			&predictionJSON,
			&item.ObservationHorizonEnd,
			&item.CreatedAt,
			&item.ObservationCount,
			&item.EvaluationCount,
			&evaluatedObservationCount,
			&item.LastObservationAt,
		); err != nil {
			return nil, err
		}
		var prediction struct {
			PredictedAction  string  `json:"predictedAction"`
			ExpectedResult   string  `json:"expectedResult"`
			ActionConfidence float64 `json:"actionConfidence"`
		}
		if err := json.Unmarshal([]byte(predictionJSON), &prediction); err != nil {
			return nil, fmt.Errorf(
				"decode shadow run %s prediction summary: %w",
				item.ID,
				err,
			)
		}
		item.PredictedAction = prediction.PredictedAction
		item.ExpectedResult = prediction.ExpectedResult
		item.ActionConfidence = prediction.ActionConfidence
		switch {
		case item.ObservationCount > 0 &&
			evaluatedObservationCount == item.ObservationCount:
			item.Status = "evaluated"
		case item.ObservationCount > 0:
			item.Status = "observed"
		case now > item.ObservationHorizonEnd:
			item.Status = "expired"
		default:
			item.Status = "awaiting_observation"
		}
		items = append(items, ShadowRunDebugSummary{
			ID:                    item.ID,
			CapabilityKey:         item.CapabilityKey,
			Status:                item.Status,
			SourceKind:            item.SourceKind,
			SourceID:              item.SourceID,
			PredictedAction:       item.PredictedAction,
			ExpectedResult:        item.ExpectedResult,
			ActionConfidence:      item.ActionConfidence,
			ObservationCount:      item.ObservationCount,
			EvaluationCount:       item.EvaluationCount,
			LastObservationAt:     item.LastObservationAt,
			ObservationHorizonEnd: item.ObservationHorizonEnd,
			CreatedAt:             item.CreatedAt,
		})
	}
	return items, rows.Err()
}

func loadShadowMemoryProjection(runID string) ShadowMemoryProjectionDebug {
	dir, err := memoryDir()
	if err != nil {
		return ShadowMemoryProjectionDebug{Error: err.Error()}
	}
	path := filepath.Join(dir, "shadow-run-learning-"+runID+".md")
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ShadowMemoryProjectionDebug{Path: path}
	}
	if err != nil {
		return ShadowMemoryProjectionDebug{Path: path, Error: err.Error()}
	}
	return ShadowMemoryProjectionDebug{
		Path:    path,
		Exists:  true,
		Content: string(content),
	}
}

func (a *App) GetShadowAgentDebugSnapshot(limit int) (ShadowAgentDebugSnapshot, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return ShadowAgentDebugSnapshot{}, errors.New("activity store not initialized")
	}
	now := time.Now().UnixMilli()
	runs, err := a.activity.store.ListShadowRunDebugSummaries(limit, now)
	if err != nil {
		return ShadowAgentDebugSnapshot{}, err
	}
	attempts, err := a.activity.store.ListShadowCorrelationAttempts(limit)
	if err != nil {
		return ShadowAgentDebugSnapshot{}, err
	}
	profiles, err := a.activity.store.ListLatestShadowCapabilityProfiles(limit)
	if err != nil {
		return ShadowAgentDebugSnapshot{}, err
	}
	qualifications, err := a.activity.store.ListLatestShadowCapabilityQualifications(limit)
	if err != nil {
		return ShadowAgentDebugSnapshot{}, err
	}
	selfModel, err := a.activity.store.ListCurrentSelfModelEntries()
	if err != nil {
		return ShadowAgentDebugSnapshot{}, err
	}
	return ShadowAgentDebugSnapshot{
		GeneratedAt:    now,
		Runs:           runs,
		Profiles:       profiles,
		Qualifications: qualifications,
		SelfModel:      selfModel,
		Attempts:       attempts,
	}, nil
}

func (a *App) GetShadowAgentDebugRun(runID string) (ShadowRunDebugDetail, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return ShadowRunDebugDetail{}, errors.New("activity store not initialized")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ShadowRunDebugDetail{}, errors.New("shadow run id is required")
	}
	detail, err := a.activity.store.GetShadowRunDetail(runID, time.Now().UnixMilli())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ShadowRunDebugDetail{}, fmt.Errorf("shadow run %q not found", runID)
		}
		return ShadowRunDebugDetail{}, err
	}
	executionAttempts, err := a.activity.store.ListShadowExecutionAttempts(runID)
	if err != nil {
		return ShadowRunDebugDetail{}, err
	}
	return ShadowRunDebugDetail{
		Detail:            detail,
		ExecutionAttempts: executionAttempts,
		MemoryProjection:  loadShadowMemoryProjection(detail.Run.ID),
	}, nil
}
