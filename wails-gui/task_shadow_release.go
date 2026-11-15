package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	taskShadowCapabilityReleaseVersion = "task-shadow-capability-release-v1"
	taskShadowReleaseDecisionQualified = "qualified"
	taskShadowReleaseDecisionSuspended = "suspended"
	taskShadowReleaseAuthorityNone     = "none"
)

type TaskShadowCapabilityReleaseEvidence struct {
	EvaluationIDs  []string `json:"evaluationIds"`
	TaskEpisodeIDs []string `json:"taskEpisodeIds"`
	ShadowRunIDs   []string `json:"shadowRunIds"`
	ContextHashes  []string `json:"contextHashes"`
}

type TaskShadowCapabilityReleaseEvent struct {
	ID                    string                              `json:"id"`
	EventKey              string                              `json:"eventKey"`
	Version               string                              `json:"version"`
	CapabilityKey         string                              `json:"capabilityKey"`
	PromptVersion         string                              `json:"promptVersion"`
	CampaignVersion       string                              `json:"campaignVersion"`
	LearningPolicyVersion string                              `json:"learningPolicyVersion"`
	Decision              string                              `json:"decision"`
	EvidenceHash          string                              `json:"evidenceHash"`
	PreviousEventID       string                              `json:"previousEventId,omitempty"`
	Qualification         ShadowTaskLearningQualification     `json:"qualification"`
	Evidence              TaskShadowCapabilityReleaseEvidence `json:"evidence"`
	AuthorityEffect       string                              `json:"authorityEffect"`
	CreatedAt             int64                               `json:"createdAt"`
}

type TaskShadowCapabilityReleaseSnapshot struct {
	Version                  string                             `json:"version"`
	CapabilityKey            string                             `json:"capabilityKey"`
	PromptVersion            string                             `json:"promptVersion"`
	State                    string                             `json:"state"`
	Active                   bool                               `json:"active"`
	PendingDecision          string                             `json:"pendingDecision,omitempty"`
	AuthorityEffect          string                             `json:"authorityEffect"`
	CurrentPromptEventCount  int                                `json:"currentPromptEventCount"`
	HistoricalEventCount     int                                `json:"historicalEventCount"`
	LatestCurrentPromptEvent *TaskShadowCapabilityReleaseEvent  `json:"latestCurrentPromptEvent,omitempty"`
	Events                   []TaskShadowCapabilityReleaseEvent `json:"events"`
}

func normalizeTaskShadowReleaseEvidence(
	input TaskShadowCapabilityReleaseEvidence,
) TaskShadowCapabilityReleaseEvidence {
	input.EvaluationIDs = uniqueSortedStrings(input.EvaluationIDs)
	input.TaskEpisodeIDs = uniqueSortedStrings(input.TaskEpisodeIDs)
	input.ShadowRunIDs = uniqueSortedStrings(input.ShadowRunIDs)
	input.ContextHashes = uniqueSortedStrings(input.ContextHashes)
	return input
}

func taskShadowReleaseEvidenceFromCandidate(
	candidate ShadowTaskLearningCandidate,
) TaskShadowCapabilityReleaseEvidence {
	result := TaskShadowCapabilityReleaseEvidence{
		EvaluationIDs:  []string{},
		TaskEpisodeIDs: []string{},
		ShadowRunIDs:   []string{},
		ContextHashes:  []string{},
	}
	for _, item := range candidate.Evidence {
		result.EvaluationIDs = append(result.EvaluationIDs, item.EvaluationID)
		result.TaskEpisodeIDs = append(result.TaskEpisodeIDs, item.TaskEpisodeID)
		result.ShadowRunIDs = append(result.ShadowRunIDs, item.ShadowRunID)
		result.ContextHashes = append(result.ContextHashes, item.ContextHash)
	}
	for _, item := range candidate.Dimensions {
		result.EvaluationIDs = append(result.EvaluationIDs, item.EvaluationID)
	}
	return normalizeTaskShadowReleaseEvidence(result)
}

func taskShadowQualityCapabilityCandidate(
	projection ShadowTaskLearningProjection,
) (ShadowTaskLearningCandidate, bool) {
	for _, candidate := range projection.Candidates {
		if candidate.TargetKind == ShadowTaskLearningTargetCapabilityProfile &&
			candidate.CapabilityKey == TaskWorkflowComposeText {
			return candidate, true
		}
	}
	return ShadowTaskLearningCandidate{}, false
}

func normalizeTaskShadowReleaseEvent(
	input TaskShadowCapabilityReleaseEvent,
) (TaskShadowCapabilityReleaseEvent, error) {
	input.Version = strings.TrimSpace(input.Version)
	input.CapabilityKey = strings.TrimSpace(input.CapabilityKey)
	input.PromptVersion = strings.TrimSpace(input.PromptVersion)
	input.CampaignVersion = strings.TrimSpace(input.CampaignVersion)
	input.LearningPolicyVersion = strings.TrimSpace(
		input.LearningPolicyVersion,
	)
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.EvidenceHash = strings.ToLower(strings.TrimSpace(input.EvidenceHash))
	input.PreviousEventID = strings.TrimSpace(input.PreviousEventID)
	input.AuthorityEffect = strings.TrimSpace(input.AuthorityEffect)
	input.Evidence = normalizeTaskShadowReleaseEvidence(input.Evidence)
	if input.Version != taskShadowCapabilityReleaseVersion ||
		input.CapabilityKey == "" || input.PromptVersion == "" ||
		input.CampaignVersion == "" || input.LearningPolicyVersion == "" ||
		input.EvidenceHash == "" || input.CreatedAt <= 0 {
		return TaskShadowCapabilityReleaseEvent{}, errors.New(
			"task shadow capability release event is incomplete",
		)
	}
	if len(input.EvidenceHash) != 64 {
		return TaskShadowCapabilityReleaseEvent{}, errors.New(
			"task shadow capability release evidence hash must be SHA-256",
		)
	}
	if _, err := hex.DecodeString(input.EvidenceHash); err != nil {
		return TaskShadowCapabilityReleaseEvent{}, errors.New(
			"task shadow capability release evidence hash must be SHA-256",
		)
	}
	if input.Decision != taskShadowReleaseDecisionQualified &&
		input.Decision != taskShadowReleaseDecisionSuspended {
		return TaskShadowCapabilityReleaseEvent{}, fmt.Errorf(
			"unsupported task shadow capability release decision %q",
			input.Decision,
		)
	}
	if input.Decision == taskShadowReleaseDecisionQualified &&
		input.Qualification.Decision != ShadowTaskLearningDecisionQualified {
		return TaskShadowCapabilityReleaseEvent{}, errors.New(
			"qualified release requires a qualified evidence decision",
		)
	}
	if input.Decision == taskShadowReleaseDecisionQualified &&
		(len(input.Evidence.EvaluationIDs) <
			input.Qualification.RequiredEvaluationCount ||
			len(input.Evidence.TaskEpisodeIDs) <
				input.Qualification.RequiredTaskEpisodeCount ||
			len(input.Evidence.ContextHashes) <
				input.Qualification.RequiredContextCount) {
		return TaskShadowCapabilityReleaseEvent{}, errors.New(
			"qualified release evidence manifest does not satisfy its frozen gate",
		)
	}
	if input.AuthorityEffect != taskShadowReleaseAuthorityNone {
		return TaskShadowCapabilityReleaseEvent{}, errors.New(
			"task shadow capability release cannot affect authority",
		)
	}
	eventKeyPayload, err := json.Marshal(struct {
		Version         string `json:"version"`
		CapabilityKey   string `json:"capabilityKey"`
		PromptVersion   string `json:"promptVersion"`
		CampaignVersion string `json:"campaignVersion"`
		Decision        string `json:"decision"`
		EvidenceHash    string `json:"evidenceHash"`
		PreviousEventID string `json:"previousEventId"`
	}{
		Version:         input.Version,
		CapabilityKey:   input.CapabilityKey,
		PromptVersion:   input.PromptVersion,
		CampaignVersion: input.CampaignVersion,
		Decision:        input.Decision,
		EvidenceHash:    input.EvidenceHash,
		PreviousEventID: input.PreviousEventID,
	})
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, err
	}
	input.EventKey = "task-shadow-release:" + sha256Hex(eventKeyPayload)
	input.ID = "task-shadow-release-event:" + sha256Hex([]byte(
		input.EventKey,
	))

	return input, nil
}

func scanTaskShadowCapabilityReleaseEvent(
	scanner interface{ Scan(...any) error },
) (TaskShadowCapabilityReleaseEvent, error) {
	var result TaskShadowCapabilityReleaseEvent
	var qualificationJSON string
	var evidenceJSON string
	err := scanner.Scan(
		&result.ID,
		&result.EventKey,
		&result.Version,
		&result.CapabilityKey,
		&result.PromptVersion,
		&result.CampaignVersion,
		&result.LearningPolicyVersion,
		&result.Decision,
		&result.EvidenceHash,
		&result.PreviousEventID,
		&qualificationJSON,
		&evidenceJSON,
		&result.AuthorityEffect,
		&result.CreatedAt,
	)
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, err
	}
	if err := json.Unmarshal(
		[]byte(qualificationJSON),
		&result.Qualification,
	); err != nil {
		return TaskShadowCapabilityReleaseEvent{}, err
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &result.Evidence); err != nil {
		return TaskShadowCapabilityReleaseEvent{}, err
	}
	result.Evidence = normalizeTaskShadowReleaseEvidence(result.Evidence)
	return result, nil
}

const taskShadowReleaseSelect = `
	SELECT
		id,
		event_key,
		version,
		capability_key,
		prompt_version,
		campaign_version,
		learning_policy_version,
		decision,
		evidence_hash,
		previous_event_id,
		qualification_json,
		evidence_manifest_json,
		authority_effect,
		created_at
	FROM task_shadow_capability_release_events_v1
`

func (s *ActivityStore) AppendTaskShadowCapabilityReleaseEvent(
	input TaskShadowCapabilityReleaseEvent,
) (TaskShadowCapabilityReleaseEvent, bool, error) {
	if s == nil || s.db == nil {
		return TaskShadowCapabilityReleaseEvent{}, false, errors.New(
			"activity store is unavailable",
		)
	}
	normalized, err := normalizeTaskShadowReleaseEvent(input)
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	qualificationJSON, err := json.Marshal(normalized.Qualification)
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	evidenceJSON, err := json.Marshal(normalized.Evidence)
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO task_shadow_capability_release_events_v1 (
			id,
			event_key,
			version,
			capability_key,
			prompt_version,
			campaign_version,
			learning_policy_version,
			decision,
			evidence_hash,
			previous_event_id,
			qualification_json,
			evidence_manifest_json,
			authority_effect,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		normalized.ID,
		normalized.EventKey,
		normalized.Version,
		normalized.CapabilityKey,
		normalized.PromptVersion,
		normalized.CampaignVersion,
		normalized.LearningPolicyVersion,
		normalized.Decision,
		normalized.EvidenceHash,
		normalized.PreviousEventID,
		string(qualificationJSON),
		string(evidenceJSON),
		normalized.AuthorityEffect,
		normalized.CreatedAt,
	)
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	persisted, err := scanTaskShadowCapabilityReleaseEvent(s.db.QueryRow(
		taskShadowReleaseSelect+` WHERE event_key = ?`,
		normalized.EventKey,
	))
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	if persisted.ID != normalized.ID ||
		persisted.EvidenceHash != normalized.EvidenceHash ||
		persisted.Decision != normalized.Decision {
		return TaskShadowCapabilityReleaseEvent{}, false, errors.New(
			"task shadow capability release idempotency conflict",
		)
	}
	return persisted, rowsAffected > 0, nil
}

func (s *ActivityStore) ListTaskShadowCapabilityReleaseEvents(
	capabilityKey string,
	limit int,
) ([]TaskShadowCapabilityReleaseEvent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	capabilityKey = strings.TrimSpace(capabilityKey)
	if capabilityKey == "" {
		return nil, errors.New("task shadow release capability key is required")
	}
	limit = normalizeTaskShadowDebugLimit(limit)
	rows, err := s.db.Query(
		taskShadowReleaseSelect+`
		WHERE capability_key = ?
		ORDER BY created_at DESC, rowid DESC
		LIMIT ?`,
		capabilityKey,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TaskShadowCapabilityReleaseEvent, 0)
	for rows.Next() {
		item, scanErr := scanTaskShadowCapabilityReleaseEvent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func buildTaskShadowCapabilityReleaseSnapshot(
	store *ActivityStore,
	campaign TaskShadowQualityCampaign,
	limit int,
) (TaskShadowCapabilityReleaseSnapshot, error) {
	result := TaskShadowCapabilityReleaseSnapshot{
		Version:         taskShadowCapabilityReleaseVersion,
		CapabilityKey:   campaign.CapabilityKey,
		PromptVersion:   campaign.PromptVersion,
		State:           "unqualified",
		AuthorityEffect: taskShadowReleaseAuthorityNone,
		Events:          []TaskShadowCapabilityReleaseEvent{},
	}
	events, err := store.ListTaskShadowCapabilityReleaseEvents(
		campaign.CapabilityKey,
		limit,
	)
	if err != nil {
		return result, err
	}
	result.Events = events
	for index := range events {
		if events[index].PromptVersion != campaign.PromptVersion {
			result.HistoricalEventCount++
			continue
		}
		result.CurrentPromptEventCount++
		if result.LatestCurrentPromptEvent == nil {
			current := events[index]
			result.LatestCurrentPromptEvent = &current
		}
	}
	latest := result.LatestCurrentPromptEvent
	switch {
	case campaign.Complete && latest != nil &&
		latest.Decision == taskShadowReleaseDecisionQualified &&
		latest.EvidenceHash == campaign.EvidenceHash:
		result.State = taskShadowReleaseDecisionQualified
		result.Active = true
	case campaign.Complete:
		result.State = "qualification_pending_persistence"
		result.PendingDecision = taskShadowReleaseDecisionQualified
	case latest != nil && latest.Decision == taskShadowReleaseDecisionQualified:
		result.State = "suspension_pending_persistence"
		result.PendingDecision = taskShadowReleaseDecisionSuspended
	case latest != nil && latest.Decision == taskShadowReleaseDecisionSuspended:
		result.State = taskShadowReleaseDecisionSuspended
	}
	return result, nil
}

func reconcileTaskShadowCapabilityRelease(
	store *ActivityStore,
	campaign TaskShadowQualityCampaign,
	candidate ShadowTaskLearningCandidate,
	createdAt int64,
) (TaskShadowCapabilityReleaseEvent, bool, error) {
	if store == nil || store.db == nil {
		return TaskShadowCapabilityReleaseEvent{}, false, errors.New(
			"activity store is unavailable",
		)
	}
	store.taskShadowReleaseMu.Lock()
	defer store.taskShadowReleaseMu.Unlock()
	return reconcileTaskShadowCapabilityReleaseLocked(
		store,
		campaign,
		candidate,
		createdAt,
	)
}

func reconcileTaskShadowCapabilityReleaseLocked(
	store *ActivityStore,
	campaign TaskShadowQualityCampaign,
	candidate ShadowTaskLearningCandidate,
	createdAt int64,
) (TaskShadowCapabilityReleaseEvent, bool, error) {
	if candidate.TargetKind != ShadowTaskLearningTargetCapabilityProfile ||
		candidate.CapabilityKey != campaign.CapabilityKey ||
		candidate.PolicyVersion != ShadowTaskLearningPolicyVersion ||
		campaign.Version != taskShadowQualityCampaignVersion ||
		campaign.PromptVersion != shadowTaskPromptVersion ||
		candidate.EvidenceHash == "" ||
		campaign.EvidenceHash != candidate.EvidenceHash {
		return TaskShadowCapabilityReleaseEvent{}, false, errors.New(
			"task shadow release candidate does not match the campaign",
		)
	}
	events, err := store.ListTaskShadowCapabilityReleaseEvents(
		campaign.CapabilityKey,
		taskShadowDebugMaxLimit,
	)
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	var latest *TaskShadowCapabilityReleaseEvent
	for index := range events {
		if events[index].PromptVersion == campaign.PromptVersion {
			item := events[index]
			latest = &item
			break
		}
	}
	decision := ""
	switch {
	case campaign.Complete &&
		(latest == nil ||
			latest.Decision != taskShadowReleaseDecisionQualified ||
			latest.EvidenceHash != campaign.EvidenceHash):
		decision = taskShadowReleaseDecisionQualified
	case !campaign.Complete && latest != nil &&
		latest.Decision == taskShadowReleaseDecisionQualified:
		decision = taskShadowReleaseDecisionSuspended
	default:
		return TaskShadowCapabilityReleaseEvent{}, false, nil
	}
	previousEventID := ""
	if latest != nil {
		previousEventID = latest.ID
	}
	return store.AppendTaskShadowCapabilityReleaseEvent(
		TaskShadowCapabilityReleaseEvent{
			Version:               taskShadowCapabilityReleaseVersion,
			CapabilityKey:         campaign.CapabilityKey,
			PromptVersion:         campaign.PromptVersion,
			CampaignVersion:       campaign.Version,
			LearningPolicyVersion: candidate.PolicyVersion,
			Decision:              decision,
			EvidenceHash:          campaign.EvidenceHash,
			PreviousEventID:       previousEventID,
			Qualification:         campaign.Qualification,
			Evidence: taskShadowReleaseEvidenceFromCandidate(
				candidate,
			),
			AuthorityEffect: taskShadowReleaseAuthorityNone,
			CreatedAt:       createdAt,
		},
	)
}

func reconcileTaskShadowCapabilityReleaseFromStore(
	store *ActivityStore,
	createdAt int64,
) (TaskShadowCapabilityReleaseEvent, bool, error) {
	if store == nil || store.db == nil {
		return TaskShadowCapabilityReleaseEvent{}, false, errors.New(
			"activity store is unavailable",
		)
	}
	store.taskShadowReleaseMu.Lock()
	defer store.taskShadowReleaseMu.Unlock()
	campaign, err := buildTaskShadowQualityCampaign(
		store,
		taskShadowDebugMaxLimit,
		createdAt,
	)
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	records, err := store.ListShadowTaskLearningEvaluationRecords(
		campaign.CapabilityKey,
	)
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	projection, err := BuildShadowTaskLearningProjection(records)
	if err != nil {
		return TaskShadowCapabilityReleaseEvent{}, false, err
	}
	candidate, ok := taskShadowQualityCapabilityCandidate(projection)
	if !ok {
		return TaskShadowCapabilityReleaseEvent{}, false, nil
	}
	return reconcileTaskShadowCapabilityReleaseLocked(
		store,
		campaign,
		candidate,
		createdAt,
	)
}
