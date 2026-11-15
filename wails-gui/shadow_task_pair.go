package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	ShadowTaskPairStateShadowPending           = "shadow_pending"
	ShadowTaskPairStateShadowFailed            = "shadow_failed"
	ShadowTaskPairStateAwaitingUserResult      = "awaiting_user_result"
	ShadowTaskPairStateUserResultUnavailable   = "user_result_unavailable"
	ShadowTaskPairStateShadowResultUnavailable = "shadow_result_unavailable"
	ShadowTaskPairStateObservationExpired      = "observation_expired"
	ShadowTaskPairStateHistoricalGeneration    = "historical_generation"
	ShadowTaskPairStateRepeatedOnset           = "repeated_task_start"
	ShadowTaskPairStateReady                   = "pair_ready"
	ShadowTaskPairStateEvaluated               = "evaluated"
)

var (
	ErrShadowTaskObservedResultUnavailable = errors.New(
		"shadow task observed result is unavailable",
	)
	ErrShadowTaskShadowResultUnavailable = errors.New(
		"shadow task isolated result is unavailable",
	)
	ErrShadowTaskHistoricalGeneration = errors.New(
		"shadow task uses a historical worker generation",
	)
)

type ShadowTaskPairReadiness struct {
	Workflow                     string   `json:"workflow"`
	State                        string   `json:"state"`
	Ready                        bool     `json:"ready"`
	ShadowPromptVersion          string   `json:"shadowPromptVersion,omitempty"`
	CurrentGeneration            bool     `json:"currentGeneration"`
	TaskEpisodeStatus            string   `json:"taskEpisodeStatus"`
	ShadowAttemptStatus          string   `json:"shadowAttemptStatus,omitempty"`
	UserResultMaterialized       bool     `json:"userResultMaterialized"`
	UserResultSHA256             string   `json:"userResultSha256,omitempty"`
	UserResultDestinationSHA256  string   `json:"userResultDestinationSha256,omitempty"`
	UserResultTruncated          bool     `json:"userResultTruncated"`
	UserResultEvidenceIDs        []string `json:"userResultEvidenceIds"`
	InvalidUserResultEvidenceIDs []string `json:"invalidUserResultEvidenceIds"`
	ShadowResultMaterialized     bool     `json:"shadowResultMaterialized"`
	ShadowResultSHA256           string   `json:"shadowResultSha256,omitempty"`
	ShadowArtifactDigests        []string `json:"shadowArtifactDigests"`
	EvaluationIDs                []string `json:"evaluationIds"`
	CurrentEvaluationIDs         []string `json:"currentEvaluationIds"`
	HistoricalEvaluationIDs      []string `json:"historicalEvaluationIds"`
	Reasons                      []string `json:"reasons"`
}

func shadowTaskRunUsesCurrentGeneration(run ShadowRun) bool {
	return strings.TrimSpace(run.PromptVersion) == shadowTaskPromptVersion
}

func requireCurrentShadowTaskRun(run ShadowRun) error {
	if run.Mode != "task_mirror" || strings.TrimSpace(run.TaskEpisodeID) == "" {
		return errors.New("shadow run is not a task mirror")
	}
	if shadowTaskRunUsesCurrentGeneration(run) {
		return nil
	}
	return fmt.Errorf(
		"%w: prompt %q cannot satisfy current prompt %q",
		ErrShadowTaskHistoricalGeneration,
		strings.TrimSpace(run.PromptVersion),
		shadowTaskPromptVersion,
	)
}

func taskShadowPairObservedEpisode(
	store *ActivityStore,
	taskEpisodeID string,
) (string, []TaskEpisodeEvidence, error) {
	taskEpisodeID = strings.TrimSpace(taskEpisodeID)
	if taskEpisodeID == "" {
		return "", nil, errors.New("task episode ID is required")
	}
	var status string
	err := store.db.QueryRow(`
		SELECT transition.to_status
		FROM task_episode_transitions transition
		WHERE transition.task_episode_id = ?
		ORDER BY transition.sequence DESC
		LIMIT 1`, taskEpisodeID).Scan(&status)
	if err != nil {
		return "", nil, err
	}
	evidence, err := store.ListTaskEpisodeEvidence(
		taskEpisodeID,
		TaskEpisodeEvidenceResult,
		taskEpisodeMaxListLimit,
	)
	if err != nil {
		return "", nil, err
	}
	return status, evidence, nil
}

func BuildShadowTaskPairReadiness(
	store *ActivityStore,
	shadowRunID string,
) (ShadowTaskPairReadiness, error) {
	if store == nil || store.db == nil {
		return ShadowTaskPairReadiness{}, errors.New(
			"activity store is unavailable",
		)
	}
	run, err := store.GetShadowRun(strings.TrimSpace(shadowRunID))
	if err != nil {
		return ShadowTaskPairReadiness{}, err
	}
	if run.Mode != "task_mirror" || run.TaskEpisodeID == "" {
		return ShadowTaskPairReadiness{}, errors.New(
			"shadow run is not a task mirror",
		)
	}
	episodeStatus, resultEvidence, err := taskShadowPairObservedEpisode(
		store,
		run.TaskEpisodeID,
	)
	if err != nil {
		return ShadowTaskPairReadiness{}, err
	}
	attempts, err := store.ListShadowExecutionAttempts(run.ID)
	if err != nil {
		return ShadowTaskPairReadiness{}, err
	}
	evaluations, err := store.ListShadowRunEvaluations(run.ID)
	if err != nil {
		return ShadowTaskPairReadiness{}, err
	}

	result := ShadowTaskPairReadiness{
		Workflow:                     run.CapabilityKey,
		ShadowPromptVersion:          strings.TrimSpace(run.PromptVersion),
		CurrentGeneration:            shadowTaskRunUsesCurrentGeneration(run),
		TaskEpisodeStatus:            episodeStatus,
		UserResultEvidenceIDs:        []string{},
		InvalidUserResultEvidenceIDs: []string{},
		ShadowArtifactDigests:        []string{},
		EvaluationIDs:                []string{},
		CurrentEvaluationIDs:         []string{},
		HistoricalEvaluationIDs:      []string{},
		Reasons:                      []string{},
	}
	if repeatedErr := store.taskEpisodeRepeatedOnsetError(
		run.TaskEpisodeID,
	); repeatedErr != nil {
		if !errors.Is(repeatedErr, ErrTaskEpisodeRepeatedOnset) {
			return ShadowTaskPairReadiness{}, repeatedErr
		}
		result.State = ShadowTaskPairStateRepeatedOnset
		result.Reasons = append(
			result.Reasons,
			taskEpisodeRepeatedOnsetReason,
		)
		return result, nil
	}
	for _, evaluation := range evaluations {
		result.EvaluationIDs = append(result.EvaluationIDs, evaluation.ID)
		if result.CurrentGeneration &&
			shadowTaskEvaluationUsesCurrentContract(evaluation) {
			result.CurrentEvaluationIDs = append(
				result.CurrentEvaluationIDs,
				evaluation.ID,
			)
		} else {
			result.HistoricalEvaluationIDs = append(
				result.HistoricalEvaluationIDs,
				evaluation.ID,
			)
		}
	}
	sort.Strings(result.EvaluationIDs)
	sort.Strings(result.CurrentEvaluationIDs)
	sort.Strings(result.HistoricalEvaluationIDs)
	if len(result.HistoricalEvaluationIDs) > 0 &&
		len(result.CurrentEvaluationIDs) == 0 {
		result.Reasons = append(
			result.Reasons,
			"historical_evaluation_generation_only",
		)
	}
	if !result.CurrentGeneration {
		result.Reasons = append(
			result.Reasons,
			"historical_shadow_prompt_generation",
		)
	}

	resultSelection := selectTaskEpisodeMaterializedResult(
		resultEvidence,
		run.CapabilityKey,
	)
	result.InvalidUserResultEvidenceIDs = append(
		result.InvalidUserResultEvidenceIDs,
		resultSelection.invalidEvidenceIDs...,
	)
	if materialized := resultSelection.result; materialized != nil {
		result.UserResultMaterialized = true
		result.UserResultSHA256 = materialized.SHA256
		result.UserResultDestinationSHA256 =
			materialized.DestinationSHA256
		result.UserResultTruncated = materialized.Truncated
		result.UserResultEvidenceIDs = append(
			result.UserResultEvidenceIDs,
			resultSelection.evidenceIDs...,
		)
	}
	sort.Strings(result.UserResultEvidenceIDs)
	sort.Strings(result.InvalidUserResultEvidenceIDs)
	if resultSelection.invalidReason != "" &&
		resultSelection.invalidReason != "invalid_user_result_provenance" {
		result.Reasons = append(result.Reasons, resultSelection.invalidReason)
	}

	if len(attempts) > 0 {
		attempt := attempts[len(attempts)-1]
		result.ShadowAttemptStatus = attempt.Status
		output := strings.TrimSpace(attempt.AssistantOutput)
		if output != "" {
			result.ShadowResultMaterialized = true
			result.ShadowResultSHA256 = sha256Hex([]byte(output))
		}
		for _, artifact := range attempt.Artifacts {
			digest := strings.TrimSpace(artifact.SHA256)
			if digest == "" {
				continue
			}
			result.ShadowArtifactDigests = append(
				result.ShadowArtifactDigests,
				digest,
			)
		}
		result.ShadowArtifactDigests = uniqueSortedStrings(
			result.ShadowArtifactDigests,
		)
		if !result.ShadowResultMaterialized &&
			!taskWorkflowTextResult(run.CapabilityKey) &&
			len(result.ShadowArtifactDigests) > 0 {
			result.ShadowResultMaterialized = true
			result.ShadowResultSHA256 = sha256Hex(
				[]byte(strings.Join(result.ShadowArtifactDigests, "\n")))

		}
	}

	switch {
	case !result.CurrentGeneration:
		result.State = ShadowTaskPairStateHistoricalGeneration
	case len(attempts) == 0:
		result.State = ShadowTaskPairStateShadowPending
		result.Reasons = append(result.Reasons, "no_shadow_execution_attempt")
	case result.ShadowAttemptStatus != "completed":
		result.State = ShadowTaskPairStateShadowFailed
		result.Reasons = append(
			result.Reasons,
			"shadow_attempt_"+result.ShadowAttemptStatus,
		)
	case taskWorkflowTextResult(run.CapabilityKey) && len(attempts[len(attempts)-1].Actions) > 0:
		result.State = ShadowTaskPairStateShadowFailed
		result.Reasons = append(
			result.Reasons,
			"isolated_text_workflow_used_tools",
		)
	case taskWorkflowTextResult(run.CapabilityKey) && len(result.ShadowArtifactDigests) > 0:
		result.State = ShadowTaskPairStateShadowFailed
		result.Reasons = append(
			result.Reasons,
			"isolated_text_workflow_created_artifacts",
		)
	case len(result.InvalidUserResultEvidenceIDs) > 0:
		result.State = ShadowTaskPairStateUserResultUnavailable
		result.Reasons = append(
			result.Reasons,
			"invalid_user_result_provenance",
		)
	case len(result.CurrentEvaluationIDs) > 0:
		result.State = ShadowTaskPairStateEvaluated
		result.Ready = true
	case !result.ShadowResultMaterialized:
		result.State = ShadowTaskPairStateShadowResultUnavailable
		result.Reasons = append(result.Reasons, "isolated_result_not_materialized")
	case episodeStatus == TaskEpisodeStatusExpired &&
		!result.UserResultMaterialized:
		result.State = ShadowTaskPairStateObservationExpired
		result.Reasons = append(
			result.Reasons,
			"task_episode_observation_expired_without_result",
		)
	case episodeStatus != TaskEpisodeStatusCompleted &&
		episodeStatus != TaskEpisodeStatusAbandoned &&
		episodeStatus != TaskEpisodeStatusExpired:
		result.State = ShadowTaskPairStateAwaitingUserResult
		result.Reasons = append(result.Reasons, "task_episode_not_terminal")
	case taskWorkflowTextResult(run.CapabilityKey) &&
		!result.UserResultMaterialized:
		result.State = ShadowTaskPairStateUserResultUnavailable
		result.Reasons = append(
			result.Reasons,
			"committed_text_result_not_materialized",
		)
	default:
		result.State = ShadowTaskPairStateReady
		result.Ready = true
	}
	return result, nil
}
