package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	ShadowTaskLearningPolicyVersion = "shadow-task-learning-v2"

	ShadowTaskLearningTargetCapabilityProfile = "capability_profile"
	ShadowTaskLearningTargetMemory            = "memory"
	ShadowTaskLearningTargetKnowledge         = "knowledge"
	ShadowTaskLearningTargetTypedSelf         = "typed_self"

	ShadowTaskLearningDecisionQualified  = "qualified"
	ShadowTaskLearningDecisionCollecting = "collecting"
	ShadowTaskLearningDecisionRejected   = "rejected"

	ShadowTaskLearningAuthorityNone      = "none"
	ShadowTaskLearningRevisionAppendOnly = "append_only"

	ShadowTaskLearningKindCapabilityEvidence = "capability_evidence"
	ShadowTaskLearningKindProcessPattern     = "process_pattern"
	ShadowTaskLearningKindResultPattern      = "result_pattern"
	ShadowTaskLearningKindReasoning          = "reasoning"
	ShadowTaskLearningKindCorrection         = "correction"

	shadowTaskLearningMinDimensionConfidence = 0.75
	shadowTaskLearningMinSelfConfidence      = 0.80
	shadowTaskLearningMinPatternEvaluations  = 3
	shadowTaskLearningMinPatternContexts     = 2
	shadowTaskLearningMinCapabilityRuns      = 5
	shadowTaskLearningMinCapabilityContexts  = 3
	shadowTaskLearningMinCapabilitySafety    = 3
	shadowTaskLearningMinTaskReliability     = 0.75
	shadowTaskLearningMinResultQuality       = 0.75
)

// ShadowTaskLearningEvaluationRecord is the read-side input to learning
// projection. Callers construct it only after reading an immutable evaluation
// back from persistence. PersistedAt and EvaluationID are therefore mandatory;
// this pure layer has no database handle and performs no writes.
type ShadowTaskLearningEvaluationRecord struct {
	EvaluationID        string                      `json:"evaluationId"`
	PersistedAt         int64                       `json:"persistedAt"`
	ExclusionReason     string                      `json:"exclusionReason,omitempty"`
	TaskEpisodeStatus   string                      `json:"taskEpisodeStatus"`
	ShadowPromptVersion string                      `json:"shadowPromptVersion,omitempty"`
	ComparisonVersion   string                      `json:"comparisonVersion,omitempty"`
	EvaluatorVersion    string                      `json:"evaluatorVersion,omitempty"`
	CapabilityKey       string                      `json:"capabilityKey"`
	TaskEpisodeID       string                      `json:"taskEpisodeId"`
	ShadowRunID         string                      `json:"shadowRunId"`
	FrozenContextHash   string                      `json:"frozenContextHash"`
	Input               ShadowTaskEvaluationInput   `json:"input"`
	Evaluation          *ShadowTaskEvaluationOutput `json:"evaluation,omitempty"`
}

// ShadowTaskLearningProvenance contains only supplied observed or criterion
// evidence. Shadow output, evaluator prose, and raw model output are
// deliberately not representable here.
type ShadowTaskLearningProvenance struct {
	EvaluationID  string `json:"evaluationId"`
	TaskEpisodeID string `json:"taskEpisodeId"`
	ShadowRunID   string `json:"shadowRunId"`
	ContextHash   string `json:"contextHash"`
	EvidenceID    string `json:"evidenceId"`
	Origin        string `json:"origin"`
	Kind          string `json:"kind"`
	Summary       string `json:"summary"`
	OccurredAt    int64  `json:"occurredAt,omitempty"`
	PersistedAt   int64  `json:"persistedAt"`
}

// ShadowTaskLearningDimensionEvidence preserves each evaluator dimension
// independently. Unknown dimensions always retain a nil Score.
type ShadowTaskLearningDimensionEvidence struct {
	EvaluationID string   `json:"evaluationId"`
	Name         string   `json:"name"`
	Assessment   string   `json:"assessment"`
	Score        *float64 `json:"score"`
	Confidence   float64  `json:"confidence"`
	EvidenceRefs []string `json:"evidenceRefs"`
}

type ShadowTaskLearningQualification struct {
	Decision                  string   `json:"decision"`
	Reasons                   []string `json:"reasons"`
	ObservedEvaluationCount   int      `json:"observedEvaluationCount"`
	QualifiedEvaluationCount  int      `json:"qualifiedEvaluationCount"`
	ObservedTaskEpisodeCount  int      `json:"observedTaskEpisodeCount"`
	ObservedContextCount      int      `json:"observedContextCount"`
	RequiredEvaluationCount   int      `json:"requiredEvaluationCount"`
	RequiredTaskEpisodeCount  int      `json:"requiredTaskEpisodeCount"`
	RequiredContextCount      int      `json:"requiredContextCount"`
	RequiredSafetyCount       int      `json:"requiredSafetyCount,omitempty"`
	AverageTaskReliability    *float64 `json:"averageTaskReliability,omitempty"`
	AverageResultQuality      *float64 `json:"averageResultQuality,omitempty"`
	AverageEvidenceConfidence *float64 `json:"averageEvidenceConfidence,omitempty"`
}

// ShadowTaskLearningCandidate is a pure recommendation for a later writer.
// ProjectionKey identifies the semantic assertion. RevisionID includes the
// immutable evidence set, so added evidence creates a new append-only revision
// without changing or overwriting an earlier candidate.
type ShadowTaskLearningCandidate struct {
	ProjectionKey       string                                `json:"projectionKey"`
	RevisionID          string                                `json:"revisionId"`
	RevisionMode        string                                `json:"revisionMode"`
	PolicyVersion       string                                `json:"policyVersion"`
	TargetKind          string                                `json:"targetKind"`
	LearningKind        string                                `json:"learningKind"`
	CapabilityKey       string                                `json:"capabilityKey"`
	Statement           string                                `json:"statement,omitempty"`
	SuggestedSelfKind   string                                `json:"suggestedSelfKind,omitempty"`
	SuggestedSelfStatus string                                `json:"suggestedSelfStatus,omitempty"`
	EvidenceHash        string                                `json:"evidenceHash"`
	Evidence            []ShadowTaskLearningProvenance        `json:"evidence"`
	Dimensions          []ShadowTaskLearningDimensionEvidence `json:"dimensions"`
	Qualification       ShadowTaskLearningQualification       `json:"qualification"`
	AuthorityEffect     string                                `json:"authorityEffect"`
}

type ShadowTaskLearningExclusion struct {
	RecordKey    string `json:"recordKey"`
	EvaluationID string `json:"evaluationId,omitempty"`
	Reason       string `json:"reason"`
}

type ShadowTaskLearningProjection struct {
	PolicyVersion       string                        `json:"policyVersion"`
	AuthorityEffect     string                        `json:"authorityEffect"`
	EvaluationCount     int                           `json:"evaluationCount"`
	ExcludedEvaluations []ShadowTaskLearningExclusion `json:"excludedEvaluations"`
	Candidates          []ShadowTaskLearningCandidate `json:"candidates"`
}

type normalizedShadowTaskLearningRecord struct {
	EvaluationID  string
	PersistedAt   int64
	CapabilityKey string
	TaskEpisodeID string
	ShadowRunID   string
	ContextHash   string
	Input         ShadowTaskEvaluationInput
	Evaluation    ShadowTaskEvaluationOutput
	Evidence      map[string]ShadowTaskEvaluationEvidence
	PayloadHash   string
}

type shadowTaskLearningCandidateGroup struct {
	targetKind    string
	learningKind  string
	capabilityKey string
	statement     string
	selfKind      string
	correction    bool
	records       map[string]normalizedShadowTaskLearningRecord
	evidence      map[string]ShadowTaskLearningProvenance
	dimensions    map[string]ShadowTaskLearningDimensionEvidence
}

func shadowTaskLearningRecordKey(record ShadowTaskLearningEvaluationRecord) string {
	payload, _ := json.Marshal(record)
	return "shadow-task-learning-record:" + sha256Hex(payload)
}

func shadowTaskLearningSafeEvidence(item ShadowTaskEvaluationEvidence) bool {
	switch item.Origin {
	case ShadowTaskEvidenceOriginObserved, ShadowTaskEvidenceOriginCriterion:
		return true
	default:
		return false
	}
}

func shadowTaskLearningObservedEvidence(item ShadowTaskEvaluationEvidence) bool {
	return item.Origin == ShadowTaskEvidenceOriginObserved
}

func shadowTaskLearningNormalizeRecord(
	record ShadowTaskLearningEvaluationRecord,
) (
	normalizedShadowTaskLearningRecord,
	*ShadowTaskLearningExclusion,
	error,
) {
	recordKey := shadowTaskLearningRecordKey(record)
	record.EvaluationID = strings.TrimSpace(record.EvaluationID)
	record.ExclusionReason = strings.TrimSpace(record.ExclusionReason)
	record.TaskEpisodeStatus = strings.ToLower(strings.TrimSpace(record.TaskEpisodeStatus))
	record.ShadowPromptVersion = strings.TrimSpace(record.ShadowPromptVersion)
	record.ComparisonVersion = strings.TrimSpace(record.ComparisonVersion)
	record.EvaluatorVersion = strings.TrimSpace(record.EvaluatorVersion)
	record.CapabilityKey = strings.TrimSpace(record.CapabilityKey)
	record.TaskEpisodeID = strings.TrimSpace(record.TaskEpisodeID)
	record.ShadowRunID = strings.TrimSpace(record.ShadowRunID)
	record.FrozenContextHash = strings.ToLower(strings.TrimSpace(record.FrozenContextHash))

	exclude := func(reason string) (
		normalizedShadowTaskLearningRecord,
		*ShadowTaskLearningExclusion,
		error,
	) {
		return normalizedShadowTaskLearningRecord{}, &ShadowTaskLearningExclusion{
			RecordKey:    recordKey,
			EvaluationID: record.EvaluationID,
			Reason:       reason,
		}, nil
	}
	if record.ExclusionReason != "" {
		if record.ExclusionReason != taskEpisodeRepeatedOnsetReason {
			return normalizedShadowTaskLearningRecord{}, nil, fmt.Errorf(
				"unsupported Shadow learning exclusion %q",
				record.ExclusionReason,
			)
		}
		return exclude(record.ExclusionReason)
	}

	if record.EvaluationID == "" || record.PersistedAt <= 0 {
		return exclude("evaluation_not_persisted")
	}
	if record.TaskEpisodeStatus != TaskEpisodeStatusCompleted {
		return exclude("task_episode_not_completed")
	}
	if record.Evaluation == nil {
		return exclude("persisted_evaluation_missing_payload")
	}
	if record.ShadowPromptVersion != shadowTaskPromptVersion {
		return exclude("incompatible_shadow_prompt_version")
	}
	if record.Input.Version != ShadowTaskEvaluationInputVersion {
		return exclude("incompatible_evaluation_input_version")
	}
	if record.Evaluation.Version != ShadowTaskEvaluationOutputVersion {
		return exclude("incompatible_evaluation_output_version")
	}
	if record.ComparisonVersion != ShadowTaskComparatorVersion {
		return exclude("incompatible_comparator_version")
	}
	if record.EvaluatorVersion != ShadowTaskEvaluatorVersion {
		return exclude("incompatible_evaluator_version")
	}
	if record.CapabilityKey == "" ||
		record.TaskEpisodeID == "" ||
		record.ShadowRunID == "" ||
		record.FrozenContextHash == "" {
		return normalizedShadowTaskLearningRecord{}, nil, errors.New(
			"persisted shadow task learning record is missing identity provenance",
		)
	}

	input, err := normalizeShadowTaskEvaluationInput(record.Input)
	if err != nil {
		return normalizedShadowTaskLearningRecord{}, nil, fmt.Errorf(
			"normalize persisted evaluation %s input: %w",
			record.EvaluationID,
			err,
		)
	}
	if record.TaskEpisodeID != input.TaskEpisodeID ||
		record.ShadowRunID != input.ShadowRunID ||
		record.FrozenContextHash != strings.ToLower(input.FrozenContextHash) {
		return normalizedShadowTaskLearningRecord{}, nil, fmt.Errorf(
			"persisted evaluation %s identity does not match its immutable input",
			record.EvaluationID,
		)
	}
	evaluation, _, err := normalizeShadowTaskEvaluationOutput(input, *record.Evaluation)
	if err != nil {
		return normalizedShadowTaskLearningRecord{}, nil, fmt.Errorf(
			"normalize persisted evaluation %s output: %w",
			record.EvaluationID,
			err,
		)
	}

	evidence := make(map[string]ShadowTaskEvaluationEvidence, len(input.Evidence))
	hasObserved := false
	for _, item := range input.Evidence {
		evidence[item.ID] = item
		hasObserved = hasObserved || shadowTaskLearningObservedEvidence(item)
	}
	if !hasObserved {
		return exclude("no_source_backed_observed_evidence")
	}

	normalized := normalizedShadowTaskLearningRecord{
		EvaluationID:  record.EvaluationID,
		PersistedAt:   record.PersistedAt,
		CapabilityKey: normalizeShadowCapabilityKey(record.CapabilityKey, record.TaskEpisodeID),
		TaskEpisodeID: record.TaskEpisodeID,
		ShadowRunID:   record.ShadowRunID,
		ContextHash:   record.FrozenContextHash,
		Input:         input,
		Evaluation:    evaluation,
		Evidence:      evidence,
	}
	payload, err := json.Marshal(struct {
		EvaluationID  string                     `json:"evaluationId"`
		PersistedAt   int64                      `json:"persistedAt"`
		CapabilityKey string                     `json:"capabilityKey"`
		Input         ShadowTaskEvaluationInput  `json:"input"`
		Evaluation    ShadowTaskEvaluationOutput `json:"evaluation"`
	}{
		EvaluationID:  normalized.EvaluationID,
		PersistedAt:   normalized.PersistedAt,
		CapabilityKey: normalized.CapabilityKey,
		Input:         normalized.Input,
		Evaluation:    normalized.Evaluation,
	})
	if err != nil {
		return normalizedShadowTaskLearningRecord{}, nil, err
	}
	normalized.PayloadHash = sha256Hex(payload)
	return normalized, nil, nil
}

func shadowTaskLearningDimensions(
	output ShadowTaskEvaluationOutput,
) []struct {
	name      string
	dimension ShadowTaskEvaluationDimension
} {
	return []struct {
		name      string
		dimension ShadowTaskEvaluationDimension
	}{
		{name: "task", dimension: output.Task},
		{name: "boundary", dimension: output.Boundary},
		{name: "process", dimension: output.Process},
		{name: "result_similarity", dimension: output.ResultSimilarity},
		{name: "result_quality", dimension: output.ResultQuality},
		{name: "timing", dimension: output.Timing},
		{name: "reasoning", dimension: output.Reasoning},
		{name: "safety", dimension: output.Safety},
	}
}

func shadowTaskLearningDimensionByName(
	output ShadowTaskEvaluationOutput,
	name string,
) ShadowTaskEvaluationDimension {
	switch name {
	case "task":
		return output.Task
	case "boundary":
		return output.Boundary
	case "process":
		return output.Process
	case "result_similarity":
		return output.ResultSimilarity
	case "result_quality":
		return output.ResultQuality
	case "timing":
		return output.Timing
	case "reasoning":
		return output.Reasoning
	case "safety":
		return output.Safety
	default:
		return ShadowTaskEvaluationDimension{
			Assessment: ShadowTaskAssessmentUnknown,
		}
	}
}

func shadowTaskLearningSafeRefs(
	record normalizedShadowTaskLearningRecord,
	dimension ShadowTaskEvaluationDimension,
) []string {
	refs := make([]string, 0, len(dimension.EvidenceRefs))
	for _, ref := range dimension.EvidenceRefs {
		item, ok := record.Evidence[ref]
		if !ok || !shadowTaskLearningSafeEvidence(item) {
			continue
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func shadowTaskLearningObservedRefs(
	record normalizedShadowTaskLearningRecord,
	dimension ShadowTaskEvaluationDimension,
) []string {
	refs := make([]string, 0, len(dimension.EvidenceRefs))
	for _, ref := range dimension.EvidenceRefs {
		item, ok := record.Evidence[ref]
		if !ok || !shadowTaskLearningObservedEvidence(item) {
			continue
		}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func shadowTaskLearningKnownDimension(
	record normalizedShadowTaskLearningRecord,
	dimension ShadowTaskEvaluationDimension,
) bool {
	return dimension.Assessment != ShadowTaskAssessmentUnknown &&
		dimension.Score != nil &&
		dimension.Confidence >= shadowTaskLearningMinDimensionConfidence &&
		len(shadowTaskLearningObservedRefs(record, dimension)) > 0
}

// Safety is runtime evidence, not a claim about the user. Deterministic
// authority decisions from the isolated attempt may qualify or veto a
// capability even though shadow-origin evidence remains forbidden from
// Memory, Knowledge, and Self materialization.
func shadowTaskLearningKnownSafetyDimension(
	record normalizedShadowTaskLearningRecord,
	dimension ShadowTaskEvaluationDimension,
) bool {
	if dimension.Assessment == ShadowTaskAssessmentUnknown ||
		dimension.Score == nil ||
		dimension.Confidence < shadowTaskLearningMinDimensionConfidence {
		return false
	}
	for _, ref := range dimension.EvidenceRefs {
		item, ok := record.Evidence[ref]
		if !ok || item.Kind != ShadowTaskEvidenceSafety {
			continue
		}
		if item.Origin == ShadowTaskEvidenceOriginObserved ||
			item.Origin == ShadowTaskEvidenceOriginShadow {
			return true
		}
	}
	return false
}

func shadowTaskLearningCanonicalStatement(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func shadowTaskLearningGroupKey(
	targetKind, learningKind, capabilityKey, statement string,
) string {
	payload := strings.Join([]string{
		ShadowTaskLearningPolicyVersion,
		targetKind,
		learningKind,
		capabilityKey,
		shadowTaskLearningCanonicalStatement(statement),
	}, "\x00")
	return sha256Hex([]byte(payload))
}

func shadowTaskLearningGetGroup(
	groups map[string]*shadowTaskLearningCandidateGroup,
	targetKind, learningKind, capabilityKey, statement string,
) *shadowTaskLearningCandidateGroup {
	key := shadowTaskLearningGroupKey(
		targetKind,
		learningKind,
		capabilityKey,
		statement,
	)
	if current := groups[key]; current != nil {
		return current
	}
	group := &shadowTaskLearningCandidateGroup{
		targetKind:    targetKind,
		learningKind:  learningKind,
		capabilityKey: capabilityKey,
		statement:     strings.TrimSpace(statement),
		records:       make(map[string]normalizedShadowTaskLearningRecord),
		evidence:      make(map[string]ShadowTaskLearningProvenance),
		dimensions:    make(map[string]ShadowTaskLearningDimensionEvidence),
	}
	groups[key] = group
	return group
}

func shadowTaskLearningAddDimension(
	group *shadowTaskLearningCandidateGroup,
	record normalizedShadowTaskLearningRecord,
	name string,
	dimension ShadowTaskEvaluationDimension,
) {
	group.records[record.EvaluationID] = record
	refs := shadowTaskLearningSafeRefs(record, dimension)
	score := dimension.Score
	if dimension.Assessment == ShadowTaskAssessmentUnknown ||
		dimension.Score == nil ||
		len(shadowTaskLearningObservedRefs(record, dimension)) == 0 {
		dimension.Assessment = ShadowTaskAssessmentUnknown
		score = nil
	}
	group.dimensions[record.EvaluationID+"\x00"+name] =
		ShadowTaskLearningDimensionEvidence{
			EvaluationID: record.EvaluationID,
			Name:         name,
			Assessment:   dimension.Assessment,
			Score:        score,
			Confidence:   dimension.Confidence,
			EvidenceRefs: refs,
		}
	for _, ref := range refs {
		item := record.Evidence[ref]
		key := record.EvaluationID + "\x00" + item.ID
		group.evidence[key] = ShadowTaskLearningProvenance{
			EvaluationID:  record.EvaluationID,
			TaskEpisodeID: record.TaskEpisodeID,
			ShadowRunID:   record.ShadowRunID,
			ContextHash:   record.ContextHash,
			EvidenceID:    item.ID,
			Origin:        item.Origin,
			Kind:          item.Kind,
			Summary:       item.Summary,
			OccurredAt:    item.OccurredAt,
			PersistedAt:   record.PersistedAt,
		}
	}
}

func shadowTaskLearningAddObservedStatement(
	group *shadowTaskLearningCandidateGroup,
	record normalizedShadowTaskLearningRecord,
	name string,
	dimension ShadowTaskEvaluationDimension,
	evidence ShadowTaskEvaluationEvidence,
) {
	shadowTaskLearningAddDimension(group, record, name, dimension)
	key := record.EvaluationID + "\x00" + evidence.ID
	group.evidence[key] = ShadowTaskLearningProvenance{
		EvaluationID:  record.EvaluationID,
		TaskEpisodeID: record.TaskEpisodeID,
		ShadowRunID:   record.ShadowRunID,
		ContextHash:   record.ContextHash,
		EvidenceID:    evidence.ID,
		Origin:        evidence.Origin,
		Kind:          evidence.Kind,
		Summary:       evidence.Summary,
		OccurredAt:    evidence.OccurredAt,
		PersistedAt:   record.PersistedAt,
	}
}

func shadowTaskLearningDimensionReferences(
	dimension ShadowTaskEvaluationDimension,
	evidenceID string,
) bool {
	for _, ref := range dimension.EvidenceRefs {
		if ref == evidenceID {
			return true
		}
	}
	return false
}

func shadowTaskLearningReasoningKind(kind string) bool {
	switch kind {
	case ShadowTaskEvidenceExplicitReasoning,
		ShadowTaskEvidenceExplicitCorrection,
		ShadowTaskEvidenceRepeatedDecision:
		return true
	default:
		return false
	}
}

func shadowTaskLearningBuildGroups(
	records []normalizedShadowTaskLearningRecord,
) map[string]*shadowTaskLearningCandidateGroup {
	groups := make(map[string]*shadowTaskLearningCandidateGroup)
	for _, record := range records {
		capability := shadowTaskLearningGetGroup(
			groups,
			ShadowTaskLearningTargetCapabilityProfile,
			ShadowTaskLearningKindCapabilityEvidence,
			record.CapabilityKey,
			"",
		)
		for _, named := range shadowTaskLearningDimensions(record.Evaluation) {
			shadowTaskLearningAddDimension(
				capability,
				record,
				named.name,
				named.dimension,
			)
		}

		process := record.Evaluation.Process
		resultSimilarity := record.Evaluation.ResultSimilarity
		resultQuality := record.Evaluation.ResultQuality
		reasoning := record.Evaluation.Reasoning
		for _, item := range record.Input.Evidence {
			if !shadowTaskLearningObservedEvidence(item) {
				continue
			}
			switch {
			case item.Kind == ShadowTaskEvidenceAction &&
				shadowTaskLearningKnownDimension(record, process) &&
				shadowTaskLearningDimensionReferences(process, item.ID):
				memory := shadowTaskLearningGetGroup(
					groups,
					ShadowTaskLearningTargetMemory,
					ShadowTaskLearningKindProcessPattern,
					record.CapabilityKey,
					item.Summary,
				)
				shadowTaskLearningAddObservedStatement(
					memory,
					record,
					"process",
					process,
					item,
				)
				if shadowTaskLearningKnownDimension(record, resultQuality) {
					knowledge := shadowTaskLearningGetGroup(
						groups,
						ShadowTaskLearningTargetKnowledge,
						ShadowTaskLearningKindProcessPattern,
						record.CapabilityKey,
						item.Summary,
					)
					shadowTaskLearningAddObservedStatement(
						knowledge,
						record,
						"process",
						process,
						item,
					)
					shadowTaskLearningAddDimension(
						knowledge,
						record,
						"result_quality",
						resultQuality,
					)
				}

			case (item.Kind == ShadowTaskEvidenceResult ||
				item.Kind == ShadowTaskEvidenceArtifact) &&
				(shadowTaskLearningKnownDimension(record, resultSimilarity) ||
					shadowTaskLearningKnownDimension(record, resultQuality)) &&
				(shadowTaskLearningDimensionReferences(resultSimilarity, item.ID) ||
					shadowTaskLearningDimensionReferences(resultQuality, item.ID)):
				knowledge := shadowTaskLearningGetGroup(
					groups,
					ShadowTaskLearningTargetKnowledge,
					ShadowTaskLearningKindResultPattern,
					record.CapabilityKey,
					item.Summary,
				)
				if shadowTaskLearningKnownDimension(record, resultSimilarity) {
					shadowTaskLearningAddObservedStatement(
						knowledge,
						record,
						"result_similarity",
						resultSimilarity,
						item,
					)
				}
				if shadowTaskLearningKnownDimension(record, resultQuality) {
					shadowTaskLearningAddObservedStatement(
						knowledge,
						record,
						"result_quality",
						resultQuality,
						item,
					)
				}

			case shadowTaskLearningReasoningKind(item.Kind) &&
				shadowTaskLearningKnownDimension(record, reasoning) &&
				shadowTaskLearningDimensionReferences(reasoning, item.ID):
				learningKind := ShadowTaskLearningKindReasoning
				if item.Kind == ShadowTaskEvidenceExplicitCorrection {
					learningKind = ShadowTaskLearningKindCorrection
				}
				memory := shadowTaskLearningGetGroup(
					groups,
					ShadowTaskLearningTargetMemory,
					learningKind,
					record.CapabilityKey,
					item.Summary,
				)
				memory.correction = item.Kind == ShadowTaskEvidenceExplicitCorrection
				shadowTaskLearningAddObservedStatement(
					memory,
					record,
					"reasoning",
					reasoning,
					item,
				)

				self := shadowTaskLearningGetGroup(
					groups,
					ShadowTaskLearningTargetTypedSelf,
					learningKind,
					record.CapabilityKey,
					item.Summary,
				)
				self.correction = item.Kind == ShadowTaskEvidenceExplicitCorrection
				switch item.Kind {
				case ShadowTaskEvidenceRepeatedDecision:
					self.selfKind = selfModelKindPreference
				default:
					self.selfKind = selfModelKindDecisionPolicy
				}
				shadowTaskLearningAddObservedStatement(
					self,
					record,
					"reasoning",
					reasoning,
					item,
				)
			}
		}
	}
	return groups
}

func shadowTaskLearningAverage(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	var total float64
	for _, value := range values {
		total += value
	}
	average := total / float64(len(values))
	return &average
}

func shadowTaskLearningSortedGroupRecords(
	group *shadowTaskLearningCandidateGroup,
) []normalizedShadowTaskLearningRecord {
	ids := make([]string, 0, len(group.records))
	for id := range group.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	records := make([]normalizedShadowTaskLearningRecord, 0, len(ids))
	for _, id := range ids {
		records = append(records, group.records[id])
	}
	return records
}

func shadowTaskLearningSortedGroupDimensions(
	group *shadowTaskLearningCandidateGroup,
) []ShadowTaskLearningDimensionEvidence {
	keys := make([]string, 0, len(group.dimensions))
	for key := range group.dimensions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	dimensions := make([]ShadowTaskLearningDimensionEvidence, 0, len(keys))
	for _, key := range keys {
		dimensions = append(dimensions, group.dimensions[key])
	}
	return dimensions
}

func shadowTaskLearningCapabilityQualification(
	group *shadowTaskLearningCandidateGroup,
) ShadowTaskLearningQualification {
	episodes := make(map[string]struct{})
	contexts := make(map[string]struct{})
	qualified := 0
	safeCount := 0
	taskScores := make([]float64, 0, len(group.records))
	qualityScores := make([]float64, 0, len(group.records))
	confidences := make([]float64, 0, len(group.records)*2)
	safetyConcern := false

	for _, record := range shadowTaskLearningSortedGroupRecords(group) {
		episodes[record.TaskEpisodeID] = struct{}{}
		contexts[record.ContextHash] = struct{}{}
		task := record.Evaluation.Task
		quality := record.Evaluation.ResultQuality
		safety := record.Evaluation.Safety
		taskKnown := shadowTaskLearningKnownDimension(record, task)
		qualityKnown := shadowTaskLearningKnownDimension(record, quality)
		safetyKnown := shadowTaskLearningKnownSafetyDimension(record, safety)
		if safetyKnown {
			safeCount++
			if safety.Assessment == ShadowTaskSafetyUnsafe ||
				safety.Assessment == ShadowTaskSafetyConcern {
				safetyConcern = true
			}
		}
		if taskKnown {
			taskScores = append(taskScores, *task.Score)
			confidences = append(confidences, task.Confidence)
		}
		if qualityKnown {
			qualityScores = append(qualityScores, *quality.Score)
			confidences = append(confidences, quality.Confidence)
		}
		if taskKnown && qualityKnown && !safetyConcern {
			qualified++
		}
	}

	taskAverage := shadowTaskLearningAverage(taskScores)
	qualityAverage := shadowTaskLearningAverage(qualityScores)
	confidenceAverage := shadowTaskLearningAverage(confidences)
	reasons := make([]string, 0, 7)
	if qualified < shadowTaskLearningMinCapabilityRuns {
		reasons = append(reasons, "need_qualified_evaluations")
	}
	if len(episodes) < shadowTaskLearningMinCapabilityRuns {
		reasons = append(reasons, "need_task_episode_diversity")
	}
	if len(contexts) < shadowTaskLearningMinCapabilityContexts {
		reasons = append(reasons, "need_context_diversity")
	}
	if safeCount < shadowTaskLearningMinCapabilitySafety {
		reasons = append(reasons, "need_source_backed_safety_coverage")
	}
	if taskAverage == nil || *taskAverage < shadowTaskLearningMinTaskReliability {
		reasons = append(reasons, "task_reliability_below_threshold")
	}
	if qualityAverage == nil || *qualityAverage < shadowTaskLearningMinResultQuality {
		reasons = append(reasons, "result_quality_below_threshold")
	}
	decision := ShadowTaskLearningDecisionCollecting
	if safetyConcern {
		decision = ShadowTaskLearningDecisionRejected
		reasons = append(reasons, "source_backed_safety_concern")
	} else if len(reasons) == 0 {
		decision = ShadowTaskLearningDecisionQualified
	}
	reasons = uniqueSortedStrings(reasons)
	return ShadowTaskLearningQualification{
		Decision:                  decision,
		Reasons:                   reasons,
		ObservedEvaluationCount:   len(group.records),
		QualifiedEvaluationCount:  qualified,
		ObservedTaskEpisodeCount:  len(episodes),
		ObservedContextCount:      len(contexts),
		RequiredEvaluationCount:   shadowTaskLearningMinCapabilityRuns,
		RequiredTaskEpisodeCount:  shadowTaskLearningMinCapabilityRuns,
		RequiredContextCount:      shadowTaskLearningMinCapabilityContexts,
		RequiredSafetyCount:       shadowTaskLearningMinCapabilitySafety,
		AverageTaskReliability:    taskAverage,
		AverageResultQuality:      qualityAverage,
		AverageEvidenceConfidence: confidenceAverage,
	}
}

func shadowTaskLearningPatternQualification(
	group *shadowTaskLearningCandidateGroup,
) ShadowTaskLearningQualification {
	episodes := make(map[string]struct{})
	contexts := make(map[string]struct{})
	confidences := make([]float64, 0, len(group.dimensions))
	qualityScores := make([]float64, 0, len(group.records))
	qualified := make(map[string]struct{})
	dimensions := shadowTaskLearningSortedGroupDimensions(group)
	for _, record := range shadowTaskLearningSortedGroupRecords(group) {
		episodes[record.TaskEpisodeID] = struct{}{}
		contexts[record.ContextHash] = struct{}{}
		recordQualified := false
		for _, dimension := range dimensions {
			if dimension.EvaluationID != record.EvaluationID ||
				dimension.Assessment == ShadowTaskAssessmentUnknown ||
				dimension.Score == nil ||
				dimension.Confidence < shadowTaskLearningMinDimensionConfidence ||
				len(dimension.EvidenceRefs) == 0 {
				continue
			}
			recordQualified = true
			confidences = append(confidences, dimension.Confidence)
			if dimension.Name == "result_quality" {
				qualityScores = append(qualityScores, *dimension.Score)
			}
		}
		if recordQualified {
			qualified[record.EvaluationID] = struct{}{}
		}
	}

	requiredEvaluations := shadowTaskLearningMinPatternEvaluations
	requiredContexts := shadowTaskLearningMinPatternContexts
	if group.targetKind == ShadowTaskLearningTargetMemory &&
		group.learningKind == ShadowTaskLearningKindReasoning {
		requiredEvaluations = 1
		requiredContexts = 1
	}
	if group.correction {
		requiredEvaluations = 1
		requiredContexts = 1
	}
	if group.targetKind == ShadowTaskLearningTargetTypedSelf {
		requiredEvaluations = 2
		requiredContexts = 2
		if group.correction {
			requiredEvaluations = 1
			requiredContexts = 1
		}
		if group.selfKind == selfModelKindPreference {
			requiredEvaluations = 3
			requiredContexts = 2
		}
	}

	averageConfidence := shadowTaskLearningAverage(confidences)
	averageQuality := shadowTaskLearningAverage(qualityScores)
	reasons := make([]string, 0, 4)
	if len(qualified) < requiredEvaluations {
		reasons = append(reasons, "need_repeated_evaluated_evidence")
	}
	if len(episodes) < requiredEvaluations {
		reasons = append(reasons, "need_task_episode_diversity")
	}
	if len(contexts) < requiredContexts {
		reasons = append(reasons, "need_context_diversity")
	}
	if group.targetKind == ShadowTaskLearningTargetTypedSelf &&
		(averageConfidence == nil ||
			*averageConfidence < shadowTaskLearningMinSelfConfidence) {
		reasons = append(reasons, "self_confidence_below_threshold")
	}
	if group.targetKind == ShadowTaskLearningTargetKnowledge &&
		(averageQuality == nil ||
			*averageQuality < shadowTaskLearningMinResultQuality) {
		reasons = append(reasons, "result_quality_below_threshold")
	}

	decision := ShadowTaskLearningDecisionCollecting
	enoughEvidence := len(qualified) >= requiredEvaluations &&
		len(episodes) >= requiredEvaluations &&
		len(contexts) >= requiredContexts
	if enoughEvidence &&
		group.targetKind == ShadowTaskLearningTargetKnowledge &&
		(averageQuality == nil ||
			*averageQuality < shadowTaskLearningMinResultQuality) {
		decision = ShadowTaskLearningDecisionRejected
	} else if len(reasons) == 0 {
		decision = ShadowTaskLearningDecisionQualified
	}
	reasons = uniqueSortedStrings(reasons)
	return ShadowTaskLearningQualification{
		Decision:                  decision,
		Reasons:                   reasons,
		ObservedEvaluationCount:   len(group.records),
		QualifiedEvaluationCount:  len(qualified),
		ObservedTaskEpisodeCount:  len(episodes),
		ObservedContextCount:      len(contexts),
		RequiredEvaluationCount:   requiredEvaluations,
		RequiredTaskEpisodeCount:  requiredEvaluations,
		RequiredContextCount:      requiredContexts,
		AverageResultQuality:      averageQuality,
		AverageEvidenceConfidence: averageConfidence,
	}
}

func shadowTaskLearningFinalizeGroup(
	group *shadowTaskLearningCandidateGroup,
) (ShadowTaskLearningCandidate, error) {
	evidence := make([]ShadowTaskLearningProvenance, 0, len(group.evidence))
	for _, item := range group.evidence {
		evidence = append(evidence, item)
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].EvaluationID != evidence[j].EvaluationID {
			return evidence[i].EvaluationID < evidence[j].EvaluationID
		}
		return evidence[i].EvidenceID < evidence[j].EvidenceID
	})

	dimensions := make([]ShadowTaskLearningDimensionEvidence, 0, len(group.dimensions))
	for _, item := range group.dimensions {
		item.EvidenceRefs = uniqueSortedStrings(item.EvidenceRefs)
		dimensions = append(dimensions, item)
	}
	sort.Slice(dimensions, func(i, j int) bool {
		if dimensions[i].EvaluationID != dimensions[j].EvaluationID {
			return dimensions[i].EvaluationID < dimensions[j].EvaluationID
		}
		return dimensions[i].Name < dimensions[j].Name
	})

	evidencePayload, err := json.Marshal(struct {
		PolicyVersion string                                `json:"policyVersion"`
		Evidence      []ShadowTaskLearningProvenance        `json:"evidence"`
		Dimensions    []ShadowTaskLearningDimensionEvidence `json:"dimensions"`
	}{
		PolicyVersion: ShadowTaskLearningPolicyVersion,
		Evidence:      evidence,
		Dimensions:    dimensions,
	})
	if err != nil {
		return ShadowTaskLearningCandidate{}, err
	}
	evidenceHash := sha256Hex(evidencePayload)
	projectionKey := "shadow-task-learning:" + shadowTaskLearningGroupKey(
		group.targetKind,
		group.learningKind,
		group.capabilityKey,
		group.statement,
	)
	revisionID := "shadow-task-learning-revision:" + sha256Hex([]byte(
		projectionKey+"\x00"+evidenceHash,
	))

	qualification := shadowTaskLearningPatternQualification(group)
	if group.targetKind == ShadowTaskLearningTargetCapabilityProfile {
		qualification = shadowTaskLearningCapabilityQualification(group)
	}
	selfStatus := ""
	if group.targetKind == ShadowTaskLearningTargetTypedSelf &&
		qualification.Decision == ShadowTaskLearningDecisionQualified {
		selfStatus = selfModelStatusProvisional
		if qualification.QualifiedEvaluationCount >= 3 ||
			(group.correction && qualification.QualifiedEvaluationCount >= 2) {
			selfStatus = selfModelStatusActive
		}
	}
	return ShadowTaskLearningCandidate{
		ProjectionKey:       projectionKey,
		RevisionID:          revisionID,
		RevisionMode:        ShadowTaskLearningRevisionAppendOnly,
		PolicyVersion:       ShadowTaskLearningPolicyVersion,
		TargetKind:          group.targetKind,
		LearningKind:        group.learningKind,
		CapabilityKey:       group.capabilityKey,
		Statement:           group.statement,
		SuggestedSelfKind:   group.selfKind,
		SuggestedSelfStatus: selfStatus,
		EvidenceHash:        evidenceHash,
		Evidence:            evidence,
		Dimensions:          dimensions,
		Qualification:       qualification,
		AuthorityEffect:     ShadowTaskLearningAuthorityNone,
	}, nil
}

// BuildShadowTaskLearningProjection deterministically derives write
// candidates from persisted, completed task-mirror evaluations. It does not
// accept a store, writer, App, authority gateway, or agent runner, making
// persistence and authority side effects impossible at this boundary.
func BuildShadowTaskLearningProjection(
	input []ShadowTaskLearningEvaluationRecord,
) (ShadowTaskLearningProjection, error) {
	projection := ShadowTaskLearningProjection{
		PolicyVersion:   ShadowTaskLearningPolicyVersion,
		AuthorityEffect: ShadowTaskLearningAuthorityNone,
		Candidates:      []ShadowTaskLearningCandidate{},
	}
	records := make([]normalizedShadowTaskLearningRecord, 0, len(input))
	seen := make(map[string]string)
	for _, item := range input {
		record, exclusion, err := shadowTaskLearningNormalizeRecord(item)
		if err != nil {
			return ShadowTaskLearningProjection{}, err
		}
		if exclusion != nil {
			projection.ExcludedEvaluations = append(
				projection.ExcludedEvaluations,
				*exclusion,
			)
			continue
		}
		if existingHash, exists := seen[record.EvaluationID]; exists {
			if existingHash != record.PayloadHash {
				return ShadowTaskLearningProjection{}, fmt.Errorf(
					"persisted evaluation %s has conflicting immutable payloads",
					record.EvaluationID,
				)
			}
			continue
		}
		seen[record.EvaluationID] = record.PayloadHash
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].EvaluationID < records[j].EvaluationID
	})
	sort.Slice(projection.ExcludedEvaluations, func(i, j int) bool {
		left := projection.ExcludedEvaluations[i]
		right := projection.ExcludedEvaluations[j]
		if left.EvaluationID != right.EvaluationID {
			return left.EvaluationID < right.EvaluationID
		}
		if left.Reason != right.Reason {
			return left.Reason < right.Reason
		}
		return left.RecordKey < right.RecordKey
	})
	projection.EvaluationCount = len(records)
	if len(records) == 0 {
		return projection, nil
	}

	groups := shadowTaskLearningBuildGroups(records)
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		candidate, err := shadowTaskLearningFinalizeGroup(groups[key])
		if err != nil {
			return ShadowTaskLearningProjection{}, err
		}
		projection.Candidates = append(projection.Candidates, candidate)
	}
	sort.Slice(projection.Candidates, func(i, j int) bool {
		left := projection.Candidates[i]
		right := projection.Candidates[j]
		if left.TargetKind != right.TargetKind {
			return left.TargetKind < right.TargetKind
		}
		if left.ProjectionKey != right.ProjectionKey {
			return left.ProjectionKey < right.ProjectionKey
		}
		return left.RevisionID < right.RevisionID
	})
	return projection, nil
}
