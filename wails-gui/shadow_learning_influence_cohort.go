package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	ShadowLearningInfluenceCohortVersion       = "shadow-learning-influence-cohort-v1"
	ShadowLearningInfluenceCohortPolicyVersion = "shadow-learning-influence-reliability-v1"

	ShadowLearningInfluenceCohortAwaitingMeasurement = "awaiting_measurement"
	ShadowLearningInfluenceCohortEarly               = "early"
	ShadowLearningInfluenceCohortReliable            = "reliable"
	ShadowLearningInfluenceCohortInsufficientQuality = "insufficient_quality"
	ShadowLearningInfluenceCohortIntegrityError      = "integrity_error"

	shadowLearningInfluenceCohortMinimumScoredTasks = 5
	shadowLearningInfluenceCohortMinimumSourceTasks = 5
	shadowLearningInfluenceCohortMinimumProfileWins = 0.60
	shadowLearningInfluenceCohortMinimumMeanLift    = 0.03
	shadowLearningInfluenceCohortMaximumUnknownRate = 0.20
	shadowLearningInfluenceCohortMaximumCorrection  = 0.10
)

type ShadowLearningInfluenceCohortPolicy struct {
	Version               string  `json:"version"`
	MinimumScoredTasks    int     `json:"minimumScoredTasks"`
	MinimumSourceTasks    int     `json:"minimumSourceTasks"`
	MinimumProfileWinRate float64 `json:"minimumProfileWinRate"`
	MinimumMeanLift       float64 `json:"minimumMeanLift"`
	MaximumUnknownRate    float64 `json:"maximumUnknownRate"`
	MaximumCorrectionRate float64 `json:"maximumCorrectionRate"`
}

type ShadowLearningInfluenceChain struct {
	ID                       string   `json:"id"`
	LearningRevisionID       string   `json:"learningRevisionId"`
	LaterTaskEpisodeID       string   `json:"laterTaskEpisodeId"`
	LaterTaskStatus          string   `json:"laterTaskStatus"`
	LaterTaskStartedAt       int64    `json:"laterTaskStartedAt"`
	State                    string   `json:"state"`
	ProfileSnapshotID        string   `json:"profileSnapshotId,omitempty"`
	OnsetIncluded            bool     `json:"onsetIncluded"`
	ShadowIncluded           bool     `json:"shadowIncluded"`
	ShadowRunID              string   `json:"shadowRunId,omitempty"`
	ShadowEvaluationID       string   `json:"shadowEvaluationId,omitempty"`
	AblationRunID            string   `json:"ablationRunId,omitempty"`
	AblationEvaluationID     string   `json:"ablationEvaluationId,omitempty"`
	NeutralScore             *float64 `json:"neutralScore,omitempty"`
	ProfileScore             *float64 `json:"profileScore,omitempty"`
	Lift                     *float64 `json:"lift,omitempty"`
	Winner                   string   `json:"winner,omitempty"`
	HistoricalPolicyExcluded bool     `json:"historicalPolicyExcluded"`
	Corrected                bool     `json:"corrected"`
	CorrectionIDs            []string `json:"correctionIds"`
	IntegrityFailure         bool     `json:"integrityFailure"`
	Reasons                  []string `json:"reasons"`
	AuthorityEffect          string   `json:"authorityEffect"`
}

type ShadowLearningInfluenceCohortSummary struct {
	ID                        string                         `json:"id"`
	ManifestSHA256            string                         `json:"manifestSha256"`
	State                     string                         `json:"state"`
	Summary                   string                         `json:"summary"`
	LearningRevisionID        string                         `json:"learningRevisionId"`
	LearningReceiptID         string                         `json:"learningReceiptId"`
	LearningTargetKind        string                         `json:"learningTargetKind"`
	LearningKind              string                         `json:"learningKind"`
	LearningCapability        string                         `json:"learningCapability"`
	LearningAvailableAt       int64                          `json:"learningAvailableAt"`
	SourceTaskEpisodeCount    int                            `json:"sourceTaskEpisodeCount"`
	SourceShadowRunCount      int                            `json:"sourceShadowRunCount"`
	SourceEvaluationCount     int                            `json:"sourceEvaluationCount"`
	SourceTaskEpisodeIDs      []string                       `json:"sourceTaskEpisodeIds"`
	SourceShadowRunIDs        []string                       `json:"sourceShadowRunIds"`
	SourceEvaluationIDs       []string                       `json:"sourceEvaluationIds"`
	RelevantLaterTaskCount    int                            `json:"relevantLaterTaskCount"`
	OnsetOmittedCount         int                            `json:"onsetOmittedCount"`
	ShadowOmittedCount        int                            `json:"shadowOmittedCount"`
	AwaitingResultCount       int                            `json:"awaitingResultCount"`
	AwaitingAblationCount     int                            `json:"awaitingAblationCount"`
	HistoricalAblationCount   int                            `json:"historicalAblationCount"`
	ScoredChainCount          int                            `json:"scoredChainCount"`
	UnknownChainCount         int                            `json:"unknownChainCount"`
	ProfileWins               int                            `json:"profileWins"`
	NeutralWins               int                            `json:"neutralWins"`
	Ties                      int                            `json:"ties"`
	PositiveLiftTotal         float64                        `json:"positiveLiftTotal"`
	NegativeLiftTotal         float64                        `json:"negativeLiftTotal"`
	MeanLift                  float64                        `json:"meanLift"`
	ProfileWinRate            float64                        `json:"profileWinRate"`
	UnknownRate               float64                        `json:"unknownRate"`
	CorrectionCount           int                            `json:"correctionCount"`
	CorrectedChainCount       int                            `json:"correctedChainCount"`
	CorrectionRate            float64                        `json:"correctionRate"`
	IntegrityExclusionCount   int                            `json:"integrityExclusionCount"`
	Blockers                  []string                       `json:"blockers"`
	ChainCount                int                            `json:"chainCount"`
	AuthorityEffect           string                         `json:"authorityEffect"`
	ProductionInfluenceEffect string                         `json:"productionInfluenceEffect"`
	Chains                    []ShadowLearningInfluenceChain `json:"chains"`
}

type ShadowLearningInfluenceCohortDetail struct {
	Summary ShadowLearningInfluenceCohortSummary `json:"summary"`
	Chains  []ShadowLearningInfluenceChain       `json:"chains"`
}

type ShadowLearningInfluenceCohortSnapshot struct {
	ID                        string                                 `json:"id"`
	Version                   string                                 `json:"version"`
	PolicyVersion             string                                 `json:"policyVersion"`
	GeneratedAt               int64                                  `json:"generatedAt"`
	ReadCutoffAt              int64                                  `json:"readCutoffAt"`
	ManifestSHA256            string                                 `json:"manifestSha256"`
	AuthorityEffect           string                                 `json:"authorityEffect"`
	ProductionInfluenceEffect string                                 `json:"productionInfluenceEffect"`
	Policy                    ShadowLearningInfluenceCohortPolicy    `json:"policy"`
	CohortCount               int                                    `json:"cohortCount"`
	AwaitingMeasurementCount  int                                    `json:"awaitingMeasurementCount"`
	EarlyCount                int                                    `json:"earlyCount"`
	ReliableCount             int                                    `json:"reliableCount"`
	InsufficientQualityCount  int                                    `json:"insufficientQualityCount"`
	IntegrityErrorCount       int                                    `json:"integrityErrorCount"`
	Cohorts                   []ShadowLearningInfluenceCohortSummary `json:"cohorts"`
}

func shadowLearningInfluenceCohortPolicy() ShadowLearningInfluenceCohortPolicy {
	return ShadowLearningInfluenceCohortPolicy{
		Version:               ShadowLearningInfluenceCohortPolicyVersion,
		MinimumScoredTasks:    shadowLearningInfluenceCohortMinimumScoredTasks,
		MinimumSourceTasks:    shadowLearningInfluenceCohortMinimumSourceTasks,
		MinimumProfileWinRate: shadowLearningInfluenceCohortMinimumProfileWins,
		MinimumMeanLift:       shadowLearningInfluenceCohortMinimumMeanLift,
		MaximumUnknownRate:    shadowLearningInfluenceCohortMaximumUnknownRate,
		MaximumCorrectionRate: shadowLearningInfluenceCohortMaximumCorrection,
	}
}

func newShadowLearningInfluenceCohortSnapshot(
	now int64,
) ShadowLearningInfluenceCohortSnapshot {
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	return ShadowLearningInfluenceCohortSnapshot{
		Version:                   ShadowLearningInfluenceCohortVersion,
		PolicyVersion:             ShadowLearningInfluenceCohortPolicyVersion,
		GeneratedAt:               now,
		ReadCutoffAt:              now,
		AuthorityEffect:           ShadowTaskLearningAuthorityNone,
		ProductionInfluenceEffect: ShadowTaskLearningAuthorityNone,
		Policy:                    shadowLearningInfluenceCohortPolicy(),
		Cohorts:                   []ShadowLearningInfluenceCohortSummary{},
	}
}

func shadowLearningInfluenceCohortID(revisionID string) string {
	return "shadow-learning-influence-cohort:" + sha256Hex([]byte(
		ShadowLearningInfluenceCohortPolicyVersion+"\x00"+revisionID,
	))

}

func shadowLearningInfluenceChainID(revisionID, episodeID string) string {
	return "shadow-learning-influence-chain:" + sha256Hex([]byte(
		revisionID+"\x00"+episodeID,
	))

}

func shadowLearningInfluenceContainsReason(
	reasons []string,
	want string,
) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func shadowLearningInfluenceRelevantEpisode(
	source VerifiedShadowLearningSource,
	detail TaskEpisodeDetail,
) bool {
	if detail.Episode.InferredStartAt <= source.Receipt.CreatedAt {
		return false
	}
	for _, evidence := range source.Candidate.Evidence {
		if evidence.TaskEpisodeID == detail.Episode.ID {
			return false
		}
	}
	selection := workPatternProfileSelectionFromEpisodeDetail(detail)
	if selection.Version == "" {
		return shadowLearningInfluenceHasSelectionEvidence(detail)
	}
	onset, onsetErr := shadowLearningInfluenceEntry(
		selection.OnsetManifest,
		source.Learning,
	)
	shadow, shadowErr := shadowLearningInfluenceEntry(
		selection.ShadowManifest,
		source.Learning,
	)
	return onsetErr != nil || shadowErr != nil || onset != nil || shadow != nil
}

func shadowLearningInfluenceChainCorrections(
	proof ShadowLearningInfluenceProof,
	source VerifiedShadowLearningSource,
	corrections []WorkPatternProfileCorrection,
) []string {
	result := make([]string, 0)
	for _, correction := range corrections {
		if correction.ProfileSnapshotID == proof.ProfileSnapshotID &&
			correction.ScopeKind == WorkPatternScopeCapability &&
			correction.ScopeKey == source.Learning.CapabilityKey {
			result = append(result, correction.ID)
		}
	}
	return uniqueSortedStrings(result)
}

func shadowLearningInfluenceChainFromCandidate(
	source VerifiedShadowLearningSource,
	candidate shadowLearningInfluenceCandidate,
	corrections []WorkPatternProfileCorrection,
) ShadowLearningInfluenceChain {
	proof := candidate.proof
	correctionIDs := shadowLearningInfluenceChainCorrections(
		proof,
		source,
		corrections,
	)
	return ShadowLearningInfluenceChain{
		ID: shadowLearningInfluenceChainID(
			source.Learning.RevisionID,
			proof.LaterTaskEpisodeID,
		),
		LearningRevisionID:       source.Learning.RevisionID,
		LaterTaskEpisodeID:       proof.LaterTaskEpisodeID,
		LaterTaskStatus:          proof.LaterTaskStatus,
		LaterTaskStartedAt:       proof.LaterTaskStartedAt,
		State:                    proof.State,
		ProfileSnapshotID:        proof.ProfileSnapshotID,
		OnsetIncluded:            proof.OnsetProfilePresent && proof.OnsetPromptIncluded != nil && *proof.OnsetPromptIncluded,
		ShadowIncluded:           proof.ShadowContextIncluded != nil && *proof.ShadowContextIncluded,
		ShadowRunID:              proof.ShadowRunID,
		ShadowEvaluationID:       proof.ShadowEvaluationID,
		AblationRunID:            proof.AblationRunID,
		AblationEvaluationID:     proof.AblationEvaluationID,
		NeutralScore:             proof.NeutralScore,
		ProfileScore:             proof.ProfileScore,
		Lift:                     proof.Lift,
		Winner:                   proof.Winner,
		HistoricalPolicyExcluded: shadowLearningInfluenceContainsReason(proof.Reasons, "independent_ablation_not_recorded"),
		Corrected:                len(correctionIDs) > 0,
		CorrectionIDs:            correctionIDs,
		IntegrityFailure:         proof.State == ShadowLearningInfluenceIntegrityError,
		Reasons:                  uniqueSortedStrings(proof.Reasons),
		AuthorityEffect:          ShadowTaskLearningAuthorityNone,
	}
}

func shadowLearningInfluenceCohortBlockers(
	result ShadowLearningInfluenceCohortSummary,
) []string {
	policy := shadowLearningInfluenceCohortPolicy()
	blockers := make([]string, 0)
	if result.IntegrityExclusionCount > 0 {
		blockers = append(blockers, fmt.Sprintf(
			"%d relevant chains failed immutable provenance checks",
			result.IntegrityExclusionCount,
		))
	}
	if result.RelevantLaterTaskCount == 0 {
		blockers = append(blockers, "no later task has frozen this exact learning")
	}
	if result.ScoredChainCount < policy.MinimumScoredTasks {
		blockers = append(blockers, fmt.Sprintf(
			"%d of %d independently scored later tasks",
			result.ScoredChainCount,
			policy.MinimumScoredTasks,
		))
	}
	if result.SourceTaskEpisodeCount < policy.MinimumSourceTasks {
		blockers = append(blockers, fmt.Sprintf(
			"%d of %d independent source tasks",
			result.SourceTaskEpisodeCount,
			policy.MinimumSourceTasks,
		))
	}
	if result.ProfileWinRate < policy.MinimumProfileWinRate {
		blockers = append(blockers, fmt.Sprintf(
			"profile win rate %.0f%% is below %.0f%%",
			result.ProfileWinRate*100,
			policy.MinimumProfileWinRate*100,
		))
	}
	if result.MeanLift < policy.MinimumMeanLift {
		blockers = append(blockers, fmt.Sprintf(
			"mean lift %.1f%% is below %.1f%%",
			result.MeanLift*100,
			policy.MinimumMeanLift*100,
		))
	}
	if result.UnknownRate > policy.MaximumUnknownRate {
		blockers = append(blockers, fmt.Sprintf(
			"unknown rate %.0f%% exceeds %.0f%%",
			result.UnknownRate*100,
			policy.MaximumUnknownRate*100,
		))
	}
	if result.CorrectionRate > policy.MaximumCorrectionRate {
		blockers = append(blockers, fmt.Sprintf(
			"corrected-chain rate %.0f%% exceeds %.0f%%",
			result.CorrectionRate*100,
			policy.MaximumCorrectionRate*100,
		))
	}
	return uniqueSortedStrings(blockers)
}

func buildShadowLearningInfluenceCohortSummary(
	source VerifiedShadowLearningSource,
	chains []ShadowLearningInfluenceChain,
) ShadowLearningInfluenceCohortSummary {
	episodes, runs, evaluations := shadowLearningInfluenceSourceIDs(source)
	result := ShadowLearningInfluenceCohortSummary{
		ID:                        shadowLearningInfluenceCohortID(source.Learning.RevisionID),
		State:                     ShadowLearningInfluenceCohortAwaitingMeasurement,
		LearningRevisionID:        source.Learning.RevisionID,
		LearningReceiptID:         source.Learning.MaterializationReceiptID,
		LearningTargetKind:        source.Learning.TargetKind,
		LearningKind:              source.Learning.LearningKind,
		LearningCapability:        source.Learning.CapabilityKey,
		LearningAvailableAt:       source.Learning.AvailableAt,
		SourceTaskEpisodeCount:    len(episodes),
		SourceShadowRunCount:      len(runs),
		SourceEvaluationCount:     len(evaluations),
		SourceTaskEpisodeIDs:      episodes,
		SourceShadowRunIDs:        runs,
		SourceEvaluationIDs:       evaluations,
		RelevantLaterTaskCount:    len(chains),
		ChainCount:                len(chains),
		AuthorityEffect:           ShadowTaskLearningAuthorityNone,
		ProductionInfluenceEffect: ShadowTaskLearningAuthorityNone,
		Blockers:                  []string{},
		Chains:                    []ShadowLearningInfluenceChain{},
	}
	correctionIDs := make([]string, 0)
	measuredCount := 0
	totalLift := 0.0
	for _, chain := range chains {
		if !chain.OnsetIncluded {
			result.OnsetOmittedCount++
		}
		if !chain.ShadowIncluded {
			result.ShadowOmittedCount++
		}
		switch chain.State {
		case ShadowLearningInfluenceAwaitingShadowResult:
			result.AwaitingResultCount++
		case ShadowLearningInfluenceAwaitingAblation:
			result.AwaitingAblationCount++
		case ShadowLearningInfluenceMeasuredUnknown:
			result.UnknownChainCount++
		case ShadowLearningInfluenceIntegrityError:
			result.IntegrityExclusionCount++
		}
		if chain.HistoricalPolicyExcluded {
			result.HistoricalAblationCount++
		}
		if chain.Corrected {
			result.CorrectedChainCount++
			correctionIDs = append(correctionIDs, chain.CorrectionIDs...)
		}
		if chain.NeutralScore == nil || chain.ProfileScore == nil {
			continue
		}
		result.ScoredChainCount++
		lift := *chain.ProfileScore - *chain.NeutralScore
		totalLift += lift
		measuredCount++
		if lift > 0 {
			result.PositiveLiftTotal += lift
		} else if lift < 0 {
			result.NegativeLiftTotal += lift
		}
		switch chain.Winner {
		case WorkPatternInfluenceProfile:
			result.ProfileWins++
		case WorkPatternInfluenceNeutral:
			result.NeutralWins++
		case "tie":
			result.Ties++
		default:
			result.UnknownChainCount++
		}
	}
	result.CorrectionCount = len(uniqueSortedStrings(correctionIDs))
	if measuredCount > 0 {
		result.MeanLift = roundWorkPatternRolloutMetric(
			totalLift / float64(measuredCount),
		)
		result.ProfileWinRate = roundWorkPatternRolloutMetric(
			float64(result.ProfileWins) / float64(measuredCount),
		)
	}
	measuredOrUnknown := result.ScoredChainCount + result.UnknownChainCount
	if measuredOrUnknown > 0 {
		result.UnknownRate = roundWorkPatternRolloutMetric(
			float64(result.UnknownChainCount) / float64(measuredOrUnknown),
		)
	}
	if result.RelevantLaterTaskCount > 0 {
		result.CorrectionRate = roundWorkPatternRolloutMetric(
			float64(result.CorrectedChainCount) /
				float64(result.RelevantLaterTaskCount),
		)
	}
	policy := shadowLearningInfluenceCohortPolicy()
	switch {
	case result.IntegrityExclusionCount > 0:
		result.State = ShadowLearningInfluenceCohortIntegrityError
		result.Summary = "Relevant later work contains an immutable-provenance failure."
	case result.RelevantLaterTaskCount == 0:
		result.State = ShadowLearningInfluenceCohortAwaitingMeasurement
		result.Summary = "This learning has not reached a later independent task yet."
	case result.ScoredChainCount < policy.MinimumScoredTasks:
		result.State = ShadowLearningInfluenceCohortEarly
		result.Summary = "The learning is being measured, but the cohort is still early."
	case result.SourceTaskEpisodeCount < policy.MinimumSourceTasks ||
		result.ProfileWinRate < policy.MinimumProfileWinRate ||
		result.MeanLift < policy.MinimumMeanLift ||
		result.UnknownRate > policy.MaximumUnknownRate ||
		result.CorrectionRate > policy.MaximumCorrectionRate:
		result.State = ShadowLearningInfluenceCohortInsufficientQuality
		result.Summary = "Repeated outcomes do not currently satisfy the reliability policy."
	default:
		result.State = ShadowLearningInfluenceCohortReliable
		result.Summary = "Repeated independent outcomes satisfy the read-only reliability policy."
	}
	result.Blockers = shadowLearningInfluenceCohortBlockers(result)
	return result
}

func shadowLearningInfluenceCohortManifestSHA256(
	readCutoffAt int64,
	summary ShadowLearningInfluenceCohortSummary,
) (string, error) {
	summary.ManifestSHA256 = ""
	payload, err := json.Marshal(struct {
		Version      string                               `json:"version"`
		Policy       string                               `json:"policy"`
		ReadCutoffAt int64                                `json:"readCutoffAt"`
		Summary      ShadowLearningInfluenceCohortSummary `json:"summary"`
	}{
		Version:      ShadowLearningInfluenceCohortVersion,
		Policy:       ShadowLearningInfluenceCohortPolicyVersion,
		ReadCutoffAt: readCutoffAt,
		Summary:      summary,
	})
	if err != nil {
		return "", err
	}
	return sha256Hex(payload), nil
}

func (s *ActivityStore) buildShadowLearningInfluenceCohortDetails(
	now int64,
	revisionIDs map[string]struct{},
) ([]ShadowLearningInfluenceCohortDetail, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	if now <= 0 {
		now = time.Now().UnixMilli()
	}
	sources, episodes, err := loadShadowLearningInfluenceLedgerForRevisions(
		s,
		now,
		revisionIDs,
	)
	if err != nil {
		return nil, err
	}
	corrections, err := s.ListWorkPatternProfileCorrections(now)
	if err != nil {
		return nil, err
	}
	identities, err := s.taskEpisodeOnsetIdentityIndexAt(0, now)
	if err != nil {
		return nil, err
	}
	repeatedEpisodes := identities.repeatedEpisodeIDs()
	episodeGroups := make(map[string][]TaskEpisodeDetail)
	episodeGroupOrder := make([]string, 0, len(episodes))
	for _, episode := range episodes {
		key := identities.observationKey(episode.Episode.ID)
		if _, found := episodeGroups[key]; !found {
			episodeGroupOrder = append(episodeGroupOrder, key)
		}
		episodeGroups[key] = append(episodeGroups[key], episode)
	}
	result := make([]ShadowLearningInfluenceCohortDetail, 0, len(sources))
	for _, source := range sources {
		base := newShadowLearningInfluenceProof(now)
		chains := make([]ShadowLearningInfluenceChain, 0)
		for _, groupKey := range episodeGroupOrder {
			group := episodeGroups[groupKey]
			if len(group) == 0 {
				continue
			}
			repeated := false
			relevant := false
			for _, episode := range group {
				if _, excluded := repeatedEpisodes[episode.Episode.ID]; excluded {
					repeated = true
				}
				if shadowLearningInfluenceRelevantEpisode(source, episode) {
					relevant = true
				}
			}
			if !relevant {
				continue
			}
			if repeated {
				representative := group[0].Episode
				chains = append(chains, ShadowLearningInfluenceChain{
					ID: shadowLearningInfluenceChainID(
						source.Learning.RevisionID,
						representative.ID,
					),
					LearningRevisionID: source.Learning.RevisionID,
					LaterTaskEpisodeID: representative.ID,
					LaterTaskStatus:    representative.CurrentStatus,
					LaterTaskStartedAt: representative.InferredStartAt,
					State:              ShadowLearningInfluenceIntegrityError,
					IntegrityFailure:   true,
					Reasons: []string{
						taskEpisodeRepeatedOnsetReason,
					},
					AuthorityEffect: ShadowTaskLearningAuthorityNone,
				})
				continue
			}
			episode := group[0]
			candidate := shadowLearningInfluenceCandidateForEpisode(
				s,
				base,
				source,
				episode,
			)
			chains = append(chains, shadowLearningInfluenceChainFromCandidate(
				source,
				candidate,
				corrections,
			))
		}
		sort.SliceStable(chains, func(i, j int) bool {
			if chains[i].LaterTaskStartedAt != chains[j].LaterTaskStartedAt {
				return chains[i].LaterTaskStartedAt < chains[j].LaterTaskStartedAt
			}
			return chains[i].ID < chains[j].ID
		})
		summary := buildShadowLearningInfluenceCohortSummary(source, chains)
		summary.Chains = append(
			[]ShadowLearningInfluenceChain(nil),
			chains...,
		)
		summary.ManifestSHA256, err =
			shadowLearningInfluenceCohortManifestSHA256(now, summary)
		if err != nil {
			return nil, err
		}
		result = append(result, ShadowLearningInfluenceCohortDetail{
			Summary: summary,
			Chains:  summary.Chains,
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Summary.LearningAvailableAt !=
			result[j].Summary.LearningAvailableAt {
			return result[i].Summary.LearningAvailableAt >
				result[j].Summary.LearningAvailableAt
		}
		return result[i].Summary.ID < result[j].Summary.ID
	})
	return result, nil
}

func (s *ActivityStore) BuildShadowLearningInfluenceCohortSnapshot(
	now int64,
) (ShadowLearningInfluenceCohortSnapshot, error) {
	return s.buildShadowLearningInfluenceCohortSnapshot(now, nil)
}

func (s *ActivityStore) BuildShadowLearningInfluenceCohortSnapshotForRevisions(
	now int64,
	revisionIDs []string,
) (ShadowLearningInfluenceCohortSnapshot, error) {
	filter := make(map[string]struct{}, len(revisionIDs))
	for _, revisionID := range uniqueSortedStrings(revisionIDs) {
		filter[revisionID] = struct{}{}
	}
	return s.buildShadowLearningInfluenceCohortSnapshot(now, filter)
}

func (s *ActivityStore) buildShadowLearningInfluenceCohortSnapshot(
	now int64,
	revisionIDs map[string]struct{},
) (ShadowLearningInfluenceCohortSnapshot, error) {
	result := newShadowLearningInfluenceCohortSnapshot(now)
	details, err := s.buildShadowLearningInfluenceCohortDetails(
		result.GeneratedAt,
		revisionIDs,
	)
	if err != nil {
		return result, err
	}
	result.CohortCount = len(details)
	result.Cohorts = make(
		[]ShadowLearningInfluenceCohortSummary,
		0,
		len(details),
	)
	for _, detail := range details {
		result.Cohorts = append(result.Cohorts, detail.Summary)
		switch detail.Summary.State {
		case ShadowLearningInfluenceCohortAwaitingMeasurement:
			result.AwaitingMeasurementCount++
		case ShadowLearningInfluenceCohortEarly:
			result.EarlyCount++
		case ShadowLearningInfluenceCohortReliable:
			result.ReliableCount++
		case ShadowLearningInfluenceCohortInsufficientQuality:
			result.InsufficientQualityCount++
		case ShadowLearningInfluenceCohortIntegrityError:
			result.IntegrityErrorCount++
		}
	}
	manifest := make([]string, 0, len(result.Cohorts))
	for _, cohort := range result.Cohorts {
		manifest = append(manifest, cohort.ID+"\x00"+cohort.ManifestSHA256)
	}
	manifestJSON, err := json.Marshal(struct {
		Version      string   `json:"version"`
		Policy       string   `json:"policy"`
		ReadCutoffAt int64    `json:"readCutoffAt"`
		Cohorts      []string `json:"cohorts"`
	}{
		Version:      result.Version,
		Policy:       result.PolicyVersion,
		ReadCutoffAt: result.ReadCutoffAt,
		Cohorts:      manifest,
	})
	if err != nil {
		return result, err
	}
	result.ManifestSHA256 = sha256Hex(manifestJSON)
	result.ID = "shadow-learning-influence-cohort-snapshot:" +
		result.ManifestSHA256
	return result, nil
}
