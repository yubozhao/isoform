package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func joinShadowTaskEvaluationLines(values []string) string {
	seen := make(map[string]struct{}, len(values))
	lines := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		lines = append(lines, value)
	}
	return strings.Join(lines, "\n")
}

func taskEpisodeEvidenceEvaluationKind(item TaskEpisodeEvidence) string {
	switch item.Role {
	case TaskEpisodeEvidenceOnset, TaskEpisodeEvidenceContext:
		return ShadowTaskEvidenceBoundary
	case TaskEpisodeEvidenceTask, TaskEpisodeEvidenceDesiredResult:
		return ShadowTaskEvidenceTask
	case TaskEpisodeEvidenceAction:
		return ShadowTaskEvidenceAction
	case TaskEpisodeEvidenceResult:
		return ShadowTaskEvidenceResult
	case TaskEpisodeEvidenceReasoning:
		payload, err := validateTaskLifecycleReasoningEvidence(item)
		if err != nil {
			return ShadowTaskEvidenceAction
		}
		switch payload.Kind {
		case TaskLifecycleReasoningStatedRationale:
			return ShadowTaskEvidenceExplicitReasoning
		case TaskLifecycleReasoningCorrection:
			return ShadowTaskEvidenceExplicitCorrection
		default:
			return ShadowTaskEvidenceAction
		}
	case TaskEpisodeEvidenceContradiction:
		// A contradiction means the lifecycle observer found incompatible
		// evidence. It is not an explicit user correction unless the source
		// itself is represented with that stronger role.
		return ShadowTaskEvidenceAction
	default:
		return ShadowTaskEvidenceAction
	}
}

func taskEpisodeEvidenceMaterializedResult(
	item TaskEpisodeEvidence,
	expectedWorkflow string,
) (*TaskLifecycleMaterializedResult, error) {
	expectedWorkflow = strings.TrimSpace(expectedWorkflow)
	if !taskWorkflowTextResult(expectedWorkflow) {
		return nil, nil
	}
	if strings.TrimSpace(item.PayloadJSON) == "" {
		return nil, nil
	}
	var envelope struct {
		MaterializedResult *TaskLifecycleMaterializedResult `json:"materializedResult"`
	}
	if err := json.Unmarshal([]byte(item.PayloadJSON), &envelope); err != nil {
		// Older evidence payloads are not necessarily lifecycle observations.
		return nil, nil
	}
	result := envelope.MaterializedResult
	if result == nil {
		return nil, nil
	}
	result.Workflow = strings.TrimSpace(result.Workflow)
	result.Kind = strings.TrimSpace(result.Kind)
	result.Content = strings.TrimSpace(result.Content)
	result.SHA256 = strings.ToLower(strings.TrimSpace(result.SHA256))
	result.DestinationSHA256 = strings.ToLower(
		strings.TrimSpace(result.DestinationSHA256),
	)
	result.EvidenceRefs = normalizeShadowTaskEvaluationList(result.EvidenceRefs)
	if result.Workflow != expectedWorkflow {
		return nil, fmt.Errorf(
			"task lifecycle materialized result workflow %q does not match expected workflow %q",
			result.Workflow,
			expectedWorkflow,
		)
	}
	if result.Kind != TaskLifecycleContentCommittedText ||
		result.Content == "" ||
		len(result.EvidenceRefs) == 0 {
		return nil, errors.New("task lifecycle materialized result is incomplete")
	}
	if sha256Hex([]byte(result.Content)) != result.SHA256 {
		return nil, errors.New("task lifecycle materialized result digest does not match")
	}
	if result.Destination == nil && result.DestinationSHA256 == "" {
		// Pre-Task-192 results remain immutable history, but they cannot enter a
		// current destination-bound comparison.
		return nil, nil
	}
	if result.Destination == nil || result.DestinationSHA256 == "" {
		return nil, errors.New("task lifecycle materialized result destination is incomplete")
	}
	destination, ok := taskLifecycleCurrentUserResultDestination(
		result.Destination,
	)
	if !ok {
		return nil, errors.New("task lifecycle materialized result destination is not an admissible user result")
	}
	destinationSHA256, err := taskLifecycleResultDestinationSHA256(destination)
	if err != nil {
		return nil, fmt.Errorf(
			"validate task lifecycle materialized result destination: %w",
			err,
		)
	}
	if destinationSHA256 != result.DestinationSHA256 {
		return nil, errors.New("task lifecycle materialized result destination digest does not match")
	}
	result.Destination = destination
	return result, nil
}

type taskEpisodeMaterializedResultCandidate struct {
	evidence TaskEpisodeEvidence
	result   *TaskLifecycleMaterializedResult
}

type taskEpisodeMaterializedResultSelection struct {
	result             *TaskLifecycleMaterializedResult
	evidenceIDs        []string
	invalidEvidenceIDs []string
	invalidReason      string
}

func selectTaskEpisodeMaterializedResult(
	evidence []TaskEpisodeEvidence,
	expectedWorkflow string,
) taskEpisodeMaterializedResultSelection {
	selection := taskEpisodeMaterializedResultSelection{
		evidenceIDs:        []string{},
		invalidEvidenceIDs: []string{},
	}
	candidates := make([]taskEpisodeMaterializedResultCandidate, 0)
	for _, item := range evidence {
		if item.Role != TaskEpisodeEvidenceResult {
			continue
		}
		materialized, err := taskEpisodeEvidenceMaterializedResult(
			item,
			expectedWorkflow,
		)
		if err != nil {
			selection.invalidEvidenceIDs = append(
				selection.invalidEvidenceIDs,
				item.ID,
			)
			continue
		}
		if materialized == nil {
			continue
		}
		candidates = append(candidates, taskEpisodeMaterializedResultCandidate{
			evidence: item,
			result:   materialized,
		})
	}
	selection.invalidEvidenceIDs = uniqueSortedStrings(
		selection.invalidEvidenceIDs,
	)
	if len(selection.invalidEvidenceIDs) > 0 {
		selection.invalidReason = "invalid_user_result_provenance"
		return selection
	}
	if len(candidates) == 0 {
		return selection
	}

	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].evidence.ObservedAt !=
			candidates[right].evidence.ObservedAt {
			return candidates[left].evidence.ObservedAt >
				candidates[right].evidence.ObservedAt
		}
		if candidates[left].evidence.CreatedAt !=
			candidates[right].evidence.CreatedAt {
			return candidates[left].evidence.CreatedAt >
				candidates[right].evidence.CreatedAt
		}
		return candidates[left].evidence.ID > candidates[right].evidence.ID
	})
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.result.DestinationSHA256 ==
			selected.result.DestinationSHA256 {
			continue
		}
		selection.invalidReason = "conflicting_user_result_destinations"
		for _, item := range candidates {
			selection.invalidEvidenceIDs = append(
				selection.invalidEvidenceIDs,
				item.evidence.ID,
			)
		}
		selection.invalidEvidenceIDs = uniqueSortedStrings(
			selection.invalidEvidenceIDs,
		)
		return selection
	}

	selection.result = selected.result
	selection.evidenceIDs = []string{selected.evidence.ID}
	return selection
}

func BuildShadowTaskEvaluationInput(
	store *ActivityStore,
	shadowRunID string,
) (ShadowTaskEvaluationInput, error) {
	if store == nil || store.db == nil {
		return ShadowTaskEvaluationInput{}, errors.New("activity store is unavailable")
	}
	run, err := store.GetShadowRun(strings.TrimSpace(shadowRunID))
	if err != nil {
		return ShadowTaskEvaluationInput{}, err
	}
	if err := requireCurrentShadowTaskRun(run); err != nil {
		return ShadowTaskEvaluationInput{}, err
	}
	detail, err := store.GetTaskEpisodeDetail(run.TaskEpisodeID)
	if err != nil {
		return ShadowTaskEvaluationInput{}, err
	}
	if detail.Episode.FrozenContext.SHA256 != run.TaskContextHash {
		return ShadowTaskEvaluationInput{}, errors.New("shadow run and task episode context hashes differ")
	}
	attempts, err := store.ListShadowExecutionAttempts(run.ID)
	if err != nil {
		return ShadowTaskEvaluationInput{}, err
	}
	if len(attempts) == 0 {
		return ShadowTaskEvaluationInput{}, errors.New("shadow task has no execution attempt")
	}
	attempt := attempts[len(attempts)-1]
	return buildShadowTaskEvaluationInput(run, detail, attempt)
}

func buildShadowTaskEvaluationInput(
	run ShadowRun,
	detail TaskEpisodeDetail,
	attempt ShadowExecutionAttempt,
) (ShadowTaskEvaluationInput, error) {
	if run.ID == "" || detail.Episode.ID == "" || attempt.ID == "" ||
		run.TaskEpisodeID != detail.Episode.ID ||
		attempt.ShadowRunID != run.ID ||
		detail.Episode.FrozenContext.SHA256 != run.TaskContextHash {
		return ShadowTaskEvaluationInput{}, errors.New(
			"shadow evaluation input sources do not share one immutable task",
		)
	}
	shadowActions := make([]string, 0, len(attempt.Actions))
	evaluationEvidence := make([]ShadowTaskEvaluationEvidence, 0)
	for index, action := range attempt.Actions {
		summary := strings.TrimSpace(strings.Join([]string{
			action.Type,
			action.ToolName,
			action.InputJSON,
			action.Output,
			action.Error,
		}, " "))
		if summary == "" {
			continue
		}
		shadowActions = append(shadowActions, summary)
		evaluationEvidence = append(evaluationEvidence, ShadowTaskEvaluationEvidence{
			ID:      fmt.Sprintf("shadow-action:%d", index+1),
			Origin:  ShadowTaskEvidenceOriginShadow,
			Kind:    ShadowTaskEvidenceAction,
			Summary: summary,
		})
	}
	shadowArtifacts := make([]ShadowTaskEvaluationArtifact, 0, len(attempt.Artifacts))
	for _, artifact := range attempt.Artifacts {
		summary := strings.TrimSpace(artifact.Content)
		if summary == "" {
			summary = fmt.Sprintf("%s artifact with %d bytes", artifact.Path, artifact.SizeBytes)
		}
		id := "shadow-artifact:" + artifact.Path
		shadowArtifacts = append(shadowArtifacts, ShadowTaskEvaluationArtifact{
			ID:      id,
			Kind:    "isolated_file",
			Name:    artifact.Path,
			Summary: summary,
			Digest:  artifact.SHA256,
		})
		evaluationEvidence = append(evaluationEvidence, ShadowTaskEvaluationEvidence{
			ID:      id,
			Origin:  ShadowTaskEvidenceOriginShadow,
			Kind:    ShadowTaskEvidenceArtifact,
			Summary: summary,
		})
	}
	shadowResult := strings.TrimSpace(attempt.AssistantOutput)
	if shadowResult != "" {
		evaluationEvidence = append(
			evaluationEvidence,
			ShadowTaskEvaluationEvidence{
				ID:      "shadow-result",
				Origin:  ShadowTaskEvidenceOriginShadow,
				Kind:    ShadowTaskEvidenceResult,
				Summary: shadowResult,
			},
		)
	}
	evaluationEvidence = append(evaluationEvidence, ShadowTaskEvaluationEvidence{
		ID:      "shadow-plan",
		Origin:  ShadowTaskEvidenceOriginShadow,
		Kind:    ShadowTaskEvidenceTask,
		Summary: attempt.Plan.Description,
	})
	for index, decision := range attempt.Decisions {
		summary := fmt.Sprintf(
			"%s %s for %s: %s",
			decision.Decision,
			decision.Action.Effect,
			decision.Action.Capability,
			decision.Reason,
		)
		evaluationEvidence = append(evaluationEvidence, ShadowTaskEvaluationEvidence{
			ID:      fmt.Sprintf("shadow-safety:%d", index+1),
			Origin:  ShadowTaskEvidenceOriginShadow,
			Kind:    ShadowTaskEvidenceSafety,
			Summary: summary,
		})
	}

	resultSelection := selectTaskEpisodeMaterializedResult(
		detail.Evidence,
		run.CapabilityKey,
	)
	if resultSelection.invalidReason != "" {
		return ShadowTaskEvaluationInput{}, fmt.Errorf(
			"%w for %s: %s (%s)",
			ErrShadowTaskObservedResultUnavailable,
			run.CapabilityKey,
			resultSelection.invalidReason,
			strings.Join(resultSelection.invalidEvidenceIDs, ","),
		)
	}
	selectedResultEvidence := make(
		map[string]struct{},
		len(resultSelection.evidenceIDs),
	)
	for _, id := range resultSelection.evidenceIDs {
		selectedResultEvidence[id] = struct{}{}
	}

	observedActions := make([]string, 0)
	observedResults := make([]string, 0)
	observedArtifacts := make([]ShadowTaskEvaluationArtifact, 0)
	seenObservedResults := make(map[string]struct{})
	for _, item := range detail.Evidence {
		id := "observed:" + item.ID
		kind := taskEpisodeEvidenceEvaluationKind(item)
		summary := item.Summary
		_, selectedResult := selectedResultEvidence[item.ID]
		if item.Role == TaskEpisodeEvidenceResult {
			if selectedResult && resultSelection.result != nil {
				summary = resultSelection.result.Content
			} else {
				summary = "Result evidence excluded from the current user-result selection."
			}
		}
		evaluationEvidence = append(evaluationEvidence, ShadowTaskEvaluationEvidence{
			ID:         id,
			Origin:     ShadowTaskEvidenceOriginObserved,
			Kind:       kind,
			OccurredAt: item.ObservedAt,
			Summary:    summary,
		})
		switch item.Role {
		case TaskEpisodeEvidenceResult:
			if !selectedResult || resultSelection.result == nil {
				continue
			}
			materialized := resultSelection.result
			if _, duplicate := seenObservedResults[materialized.SHA256]; duplicate {
				continue
			}
			seenObservedResults[materialized.SHA256] = struct{}{}
			observedResults = append(observedResults, materialized.Content)
			observedArtifacts = append(
				observedArtifacts,
				ShadowTaskEvaluationArtifact{
					ID:      "observed-text:" + materialized.SHA256,
					Kind:    materialized.Kind,
					Name:    "Observed user text",
					Summary: materialized.Content,
					Digest:  materialized.SHA256,
				},
			)
		case TaskEpisodeEvidenceAction, TaskEpisodeEvidenceOnset:
			observedActions = append(observedActions, item.Summary)
		}
	}
	evaluationEvidence = append(evaluationEvidence,
		ShadowTaskEvaluationEvidence{
			ID:         "observed-task",
			Origin:     ShadowTaskEvidenceOriginObserved,
			Kind:       ShadowTaskEvidenceTask,
			OccurredAt: detail.Episode.InferredStartAt,
			Summary:    detail.Episode.LatestHypothesis.TaskHypothesis,
		},
		ShadowTaskEvaluationEvidence{
			ID:      "quality:desired-result",
			Origin:  ShadowTaskEvidenceOriginCriterion,
			Kind:    ShadowTaskEvidenceQualityCriterion,
			Summary: detail.Episode.LatestHypothesis.DesiredResultHypothesis,
		},
	)

	completedAt := int64(0)
	for _, transition := range detail.Transitions {
		evaluationEvidence = append(evaluationEvidence, ShadowTaskEvaluationEvidence{
			ID:         "observed-transition:" + transition.ID,
			Origin:     ShadowTaskEvidenceOriginObserved,
			Kind:       ShadowTaskEvidenceBoundary,
			OccurredAt: transition.OccurredAt,
			Summary:    transition.FromStatus + " -> " + transition.ToStatus + ": " + transition.Reason,
		})
		if transition.ToStatus == TaskEpisodeStatusCompleted {
			completedAt = transition.OccurredAt
		}
	}
	policyBoundaries := make([]string, 0, len(attempt.Decisions))
	for _, decision := range attempt.Decisions {
		policyBoundaries = append(policyBoundaries, fmt.Sprintf(
			"%s %s: %s",
			decision.Decision,
			decision.Action.Capability,
			decision.Reason,
		))
	}

	qualityCriteria := []string{
		detail.Episode.LatestHypothesis.DesiredResultHypothesis,
	}
	if run.CapabilityKey == TaskWorkflowComposeText {
		qualityCriteria = append(
			qualityCriteria,
			"Cover the intended outcome and audience.",
			"Keep factual claims supported by the frozen context.",
			"Use an appropriate tone for the intended destination.",
			"Avoid unsupported claims or invented details.",
			"Produce complete text that is ready for its intended destination.",
		)
	} else if run.CapabilityKey == TaskWorkflowAnalyzeText {
		qualityCriteria = append(
			qualityCriteria,
			"Answer the desired analytical outcome.",
			"Distinguish evidence-backed findings from uncertainty.",
			"Cover material risks, gaps, or tradeoffs supported by the frozen context.",
			"Avoid invented facts and unsupported causal claims.",
			"Produce a complete, usable textual result.",
		)
	}

	input := ShadowTaskEvaluationInput{
		Version:            ShadowTaskEvaluationInputVersion,
		TaskEpisodeID:      detail.Episode.ID,
		ShadowRunID:        run.ID,
		FrozenContextHash:  run.TaskContextHash,
		PredictedReasoning: append([]ShadowReasoningHypothesis(nil), run.Prediction.ReasoningHypotheses...),
		Shadow: ShadowTaskEvaluationWork{
			Task:          run.Prediction.Goal,
			DesiredResult: run.Prediction.ExpectedResult,
			Plan: normalizeShadowTaskEvaluationList([]string{
				attempt.Plan.Description,
				attempt.Plan.Rationale,
			}),
			Actions:     shadowActions,
			Artifacts:   shadowArtifacts,
			Result:      attempt.AssistantOutput,
			StartedAt:   attempt.StartedAt,
			CompletedAt: attempt.CompletedAt,
		},
		Observed: ShadowTaskEvaluationWork{
			Task:          detail.Episode.LatestHypothesis.TaskHypothesis,
			DesiredResult: detail.Episode.LatestHypothesis.DesiredResultHypothesis,
			Actions:       observedActions,
			Artifacts:     observedArtifacts,
			Result:        joinShadowTaskEvaluationLines(observedResults),
			StartedAt:     detail.Episode.InferredStartAt,
			CompletedAt:   completedAt,
		},
		QualityCriteria:  qualityCriteria,
		PolicyBoundaries: normalizeShadowTaskEvaluationList(policyBoundaries),
		Evidence:         evaluationEvidence,
	}
	if taskWorkflowTextResult(run.CapabilityKey) {
		if len(input.Shadow.Actions) > 0 || len(input.Shadow.Artifacts) > 0 {
			return ShadowTaskEvaluationInput{}, fmt.Errorf(
				"%w for %s: isolated text workflow produced tool activity",
				ErrShadowTaskShadowResultUnavailable,
				run.CapabilityKey,
			)
		}
		if len(input.Observed.Artifacts) == 0 ||
			strings.TrimSpace(input.Observed.Result) == "" {
			return ShadowTaskEvaluationInput{}, fmt.Errorf(
				"%w for %s",
				ErrShadowTaskObservedResultUnavailable,
				run.CapabilityKey,
			)
		}
		if strings.TrimSpace(input.Shadow.Result) == "" {
			return ShadowTaskEvaluationInput{}, fmt.Errorf(
				"%w for %s",
				ErrShadowTaskShadowResultUnavailable,
				run.CapabilityKey,
			)
		}
	}
	return normalizeShadowTaskEvaluationInput(input)
}

func shadowTaskLegacyDimension(
	input ShadowTaskEvaluationDimension,
) ShadowEvaluationDimension {
	return ShadowEvaluationDimension{
		Assessment:   input.Assessment,
		Score:        input.Score,
		Confidence:   input.Confidence,
		Reason:       input.Reason,
		EvidenceRefs: append([]string(nil), input.EvidenceRefs...),
	}
}

func shadowTaskLegacySafety(
	input ShadowTaskEvaluationDimension,
) ShadowEvaluationDimension {
	assessment := input.Assessment
	if assessment == ShadowTaskSafetyConcern {
		assessment = "unsafe"
	}
	return ShadowEvaluationDimension{
		Assessment:   assessment,
		Score:        input.Score,
		Confidence:   input.Confidence,
		Reason:       input.Reason,
		EvidenceRefs: append([]string(nil), input.EvidenceRefs...),
	}
}

func PersistShadowTaskEvaluation(
	store *ActivityStore,
	input ShadowTaskEvaluationInput,
	result ShadowTaskEvaluationResult,
	createdAt int64,
) (ShadowRunEvaluation, bool, error) {
	if store == nil || store.db == nil {
		return ShadowRunEvaluation{}, false, errors.New("activity store is unavailable")
	}
	normalizedInput, err := normalizeShadowTaskEvaluationInput(input)
	if err != nil {
		return ShadowRunEvaluation{}, false, err
	}
	run, err := store.GetShadowRun(normalizedInput.ShadowRunID)
	if err != nil {
		return ShadowRunEvaluation{}, false, err
	}
	if err := requireCurrentShadowTaskRun(run); err != nil {
		return ShadowRunEvaluation{}, false, err
	}
	if run.TaskEpisodeID != normalizedInput.TaskEpisodeID ||
		run.TaskContextHash != normalizedInput.FrozenContextHash {
		return ShadowRunEvaluation{}, false, errors.New(
			"shadow task evaluation input does not match its current run",
		)
	}
	if result.Evaluation == nil {
		return ShadowRunEvaluation{}, false, errors.New("shadow task evaluation has no validated output")
	}
	if result.ComparatorVersion != ShadowTaskComparatorVersion ||
		result.EvaluatorVersion != ShadowTaskEvaluatorVersion ||
		result.InputVersion != normalizedInput.Version {
		return ShadowRunEvaluation{}, false, errors.New("shadow task evaluation provenance is incompatible")
	}
	if shadowTaskComparatorHash(result.ComparatorPrompt) != result.ComparatorHash {
		return ShadowRunEvaluation{}, false, errors.New("shadow task comparator hash does not match prompt")
	}
	output, _, err := normalizeShadowTaskEvaluationOutput(normalizedInput, *result.Evaluation)
	if err != nil {
		return ShadowRunEvaluation{}, false, err
	}

	inputPayload, err := json.Marshal(normalizedInput)
	if err != nil {
		return ShadowRunEvaluation{}, false, err
	}
	observationSourceID := sha256Hex(inputPayload)
	observedAt := normalizedInput.Observed.CompletedAt
	if observedAt == 0 {
		observedAt = normalizedInput.Observed.StartedAt
	}
	reasoning := make([]ShadowReasoningEvidence, 0)
	observationEvidence := make([]ShadowEvidenceReference, 0)
	for _, item := range normalizedInput.Evidence {
		if item.Origin != ShadowTaskEvidenceOriginObserved {
			continue
		}
		observationEvidence = append(observationEvidence, ShadowEvidenceReference{
			Role:       item.Kind,
			SourceKind: "task_episode_evidence",
			SourceID:   item.ID,
			Content:    item.Summary,
			Summary:    item.Summary,
			ObservedAt: item.OccurredAt,
		})
		switch item.Kind {
		case ShadowTaskEvidenceExplicitReasoning:
			reasoning = append(reasoning, ShadowReasoningEvidence{
				Kind:       "stated_rationale",
				Statement:  item.Summary,
				Confidence: 1,
				SourceRefs: []string{item.ID},
			})
		case ShadowTaskEvidenceExplicitCorrection:
			reasoning = append(reasoning, ShadowReasoningEvidence{
				Kind:       "correction",
				Statement:  item.Summary,
				Confidence: 1,
				SourceRefs: []string{item.ID},
			})
		case ShadowTaskEvidenceRepeatedDecision:
			reasoning = append(reasoning, ShadowReasoningEvidence{
				Kind:       "repeated_choice",
				Statement:  item.Summary,
				Confidence: 1,
				SourceRefs: []string{item.ID},
			})
		}
	}
	observation, _, err := store.AddShadowRunObservation(ShadowRunObservation{
		ID:                deterministicTaskEpisodeID("shadow-task-observation", normalizedInput.ShadowRunID+"\x00"+observationSourceID),
		ShadowRunID:       normalizedInput.ShadowRunID,
		SourceKind:        "task_episode_evaluation",
		SourceID:          observationSourceID,
		ObservedAction:    joinShadowTaskEvaluationLines(append([]string{normalizedInput.Observed.Task}, normalizedInput.Observed.Actions...)),
		ObservedResult:    normalizedInput.Observed.Result,
		ReasoningEvidence: reasoning,
		Evidence:          observationEvidence,
		ObservedAt:        observedAt,
		CreatedAt:         createdAt,
	})
	if err != nil {
		return ShadowRunEvaluation{}, false, err
	}

	evaluation := ShadowRunEvaluation{
		ID:                  deterministicTaskEpisodeID("shadow-task-evaluation", normalizedInput.ShadowRunID+"\x00"+result.ComparatorHash),
		ShadowRunID:         normalizedInput.ShadowRunID,
		ObservationID:       observation.ID,
		IdempotencyKey:      normalizedInput.ShadowRunID + ":" + observation.ID + ":" + result.ComparatorHash,
		ComparisonVersion:   result.ComparatorVersion,
		EvaluatorVersion:    result.EvaluatorVersion,
		EvaluatorModel:      result.EvaluatorModel,
		ComparatorPrompt:    result.ComparatorPrompt,
		ComparatorHash:      result.ComparatorHash,
		EvaluatorRaw:        result.EvaluatorRaw,
		Action:              shadowTaskLegacyDimension(output.Process),
		Outcome:             shadowTaskLegacyDimension(output.ResultSimilarity),
		Timing:              shadowTaskLegacyDimension(output.Timing),
		Reasoning:           shadowTaskLegacyDimension(output.Reasoning),
		Safety:              shadowTaskLegacySafety(output.Safety),
		TaskEvaluationInput: &normalizedInput,
		TaskEvaluation:      &output,
		Summary:             output.Summary,
		CreatedAt:           createdAt,
	}
	return store.AddShadowRunEvaluation(evaluation)
}

func sortedShadowTaskEvidenceIDs(input ShadowTaskEvaluationInput) []string {
	result := make([]string, 0, len(input.Evidence))
	for _, item := range input.Evidence {
		result = append(result, item.ID)
	}
	sort.Strings(result)
	return result
}
