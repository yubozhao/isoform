package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/pii"
)

const (
	WorkPatternInfluenceNeutral = "neutral"
	WorkPatternInfluenceProfile = "profile"

	workPatternSemanticContextTokenBudget   = 4_000
	workPatternCapabilityContextTokenBudget = 2_000
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

type workPatternProfileContextPayload struct {
	SnapshotID          string                             `json:"snapshotId"`
	SnapshotSHA256      string                             `json:"snapshotSha256"`
	PolicyVersion       string                             `json:"policyVersion"`
	SourceCutoffAt      int64                              `json:"sourceCutoffAt"`
	Status              string                             `json:"status"`
	Confidence          float64                            `json:"confidence"`
	InfluenceMode       string                             `json:"influenceMode"`
	SelectedProjections []WorkPatternProjection            `json:"selectedProjections"`
	SemanticSignals     []WorkPatternSemanticSignal        `json:"semanticSignals"`
	CapabilitySignals   []WorkPatternCapabilitySignal      `json:"capabilitySignals"`
	Contradictions      []WorkPatternProfileCorrection     `json:"contradictions"`
	SelectionManifest   []WorkPatternProfileSelectionEntry `json:"selectionManifest"`
	SelectionReason     string                             `json:"selectionReason"`
}

func normalizeWorkPatternInfluenceMode(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == WorkPatternInfluenceProfile {
		return WorkPatternInfluenceProfile
	}
	return WorkPatternInfluenceNeutral
}

func workPatternEstimatedTokens(value string) int {
	length := len([]rune(value))
	if length == 0 {
		return 0
	}
	return (length + 3) / 4
}

func workPatternProjectionKey(item WorkPatternProjection) string {
	return item.ScopeKind + "\x00" + item.ScopeKey
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

func workPatternCurrentKeys(values []string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func workPatternProjectionMatches(
	item WorkPatternProjection,
	appKeys map[string]struct{},
	transitionKeys map[string]struct{},
) (bool, int) {
	if item.Status != WorkPatternProfileStatusQualified {
		return false, 0
	}
	switch item.ScopeKind {
	case WorkPatternScopeGlobal:
		return true, 1
	case WorkPatternScopeApp:
		_, ok := appKeys[item.ScopeKey]
		if ok {
			return true, 3
		}
	case WorkPatternScopeTransition:
		_, ok := transitionKeys[item.ScopeKey]
		if ok {
			return true, 100
		}
	case WorkPatternScopeWorkMode:
		overlap := 0
		for _, app := range item.Apps {
			if _, ok := appKeys[app]; ok {
				overlap++
			}
		}
		if overlap >= 2 {
			extraApps := max(len(item.Apps)-overlap, 0)
			return true, 40 + overlap*10 - extraApps*4
		}
	}
	return false, 0
}

func workPatternProjectionMatchedAppPairs(
	item WorkPatternProjection,
	appKeys map[string]struct{},
) []string {
	if item.ScopeKind != WorkPatternScopeWorkMode {
		return nil
	}
	matched := make([]string, 0, len(item.Apps))
	for _, app := range item.Apps {
		app = strings.ToLower(strings.TrimSpace(app))
		if _, ok := appKeys[app]; ok {
			matched = append(matched, app)
		}
	}
	sort.Strings(matched)
	result := make([]string, 0)
	for left := 0; left < len(matched); left++ {
		for right := left + 1; right < len(matched); right++ {
			result = append(
				result,
				matched[left]+"\x00"+matched[right],
			)
		}
	}
	return result
}

func selectWorkPatternProjections(
	snapshot WorkPatternProfileSnapshot,
	appKeys, transitionKeys []string,
	corrections []WorkPatternProfileCorrection,
) ([]WorkPatternProjection, []WorkPatternProfileCorrection) {
	corrected := make(map[string]WorkPatternProfileCorrection)
	for _, correction := range corrections {
		key := correction.ScopeKind + "\x00" + correction.ScopeKey
		if previous, ok := corrected[key]; !ok ||
			correction.Revision > previous.Revision {
			corrected[key] = correction
		}
	}
	appSet := workPatternCurrentKeys(appKeys)
	transitionSet := workPatternCurrentKeys(transitionKeys)
	type rankedProjection struct {
		item WorkPatternProjection
		rank int
	}
	ranked := make([]rankedProjection, 0)
	contradictions := make([]WorkPatternProfileCorrection, 0)
	for _, projection := range snapshot.Projections {
		matched, rank := workPatternProjectionMatches(
			projection,
			appSet,
			transitionSet,
		)
		if !matched {
			continue
		}
		if correction, ok := corrected[workPatternProjectionKey(projection)]; ok {
			contradictions = append(contradictions, correction)
			continue
		}
		ranked = append(ranked, rankedProjection{
			item: projection,
			rank: rank,
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].rank != ranked[j].rank {
			return ranked[i].rank > ranked[j].rank
		}
		if ranked[i].item.Confidence != ranked[j].item.Confidence {
			return ranked[i].item.Confidence > ranked[j].item.Confidence
		}
		return workPatternProjectionKey(ranked[i].item) <
			workPatternProjectionKey(ranked[j].item)
	})
	result := make([]WorkPatternProjection, 0, len(ranked))
	coveredModePairs := make(map[string]struct{})
	for _, item := range ranked {
		if item.item.ScopeKind == WorkPatternScopeWorkMode {
			pairs := workPatternProjectionMatchedAppPairs(
				item.item,
				appSet,
			)
			addsContext := false
			for _, pair := range pairs {
				if _, covered := coveredModePairs[pair]; !covered {
					addsContext = true
					break
				}
			}
			if !addsContext {
				continue
			}
			for _, pair := range pairs {
				coveredModePairs[pair] = struct{}{}
			}
		}
		result = append(result, item.item)
	}
	sort.SliceStable(contradictions, func(i, j int) bool {
		if contradictions[i].ScopeKind != contradictions[j].ScopeKind {
			return contradictions[i].ScopeKind < contradictions[j].ScopeKind
		}
		if contradictions[i].ScopeKey != contradictions[j].ScopeKey {
			return contradictions[i].ScopeKey < contradictions[j].ScopeKey
		}
		return contradictions[i].Revision < contradictions[j].Revision
	})
	return result, contradictions
}

func workPatternSemanticSectionAllowed(value string) bool {
	switch value {
	case TaskContextSectionBoundaries,
		TaskContextSectionCommitments,
		TaskContextSectionSelf,
		TaskContextSectionKnowledge:
		return true
	default:
		return false
	}
}

func workPatternSemanticCandidateContent(
	candidate TaskContextCandidate,
) string {
	content := strings.TrimSpace(candidate.Content)
	if candidate.PrivacyDecision == TaskContextPrivacyRedact {
		content, _ = pii.RedactText(content)
		content = strings.TrimSpace(content)
	}
	return content
}

func workPatternSemanticContentKey(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func selectWorkPatternSemanticSignals(
	sources TaskContextSourceResult,
	cutoffAt int64,
) []WorkPatternSemanticSignal {
	candidates := append([]TaskContextCandidate(nil), sources.Candidates...)
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Required != candidates[j].Required {
			return candidates[i].Required
		}
		if candidates[i].Relevance != candidates[j].Relevance {
			return candidates[i].Relevance > candidates[j].Relevance
		}
		if candidates[i].ObservedAt != candidates[j].ObservedAt {
			return candidates[i].ObservedAt > candidates[j].ObservedAt
		}
		return candidates[i].Ref < candidates[j].Ref
	})
	result := make([]WorkPatternSemanticSignal, 0)
	selectedContent := make(map[string]struct{})
	used := 0
	for _, candidate := range candidates {
		if !workPatternSemanticSectionAllowed(candidate.Section) ||
			candidate.ObservedAt > cutoffAt ||
			candidate.PrivacyDecision == TaskContextPrivacyExclude ||
			candidate.EvidenceClass == TaskContextEvidenceObservedContext ||
			candidate.EvidenceClass == TaskContextEvidenceHiddenShadow ||
			candidate.EvidenceClass == TaskContextEvidenceAgentGenerated {
			continue
		}
		content := workPatternSemanticCandidateContent(candidate)
		contentKey := workPatternSemanticContentKey(content)
		if content == "" ||
			contentKey == "" ||
			used >= workPatternSemanticContextTokenBudget {
			continue
		}
		if _, duplicate := selectedContent[contentKey]; duplicate {
			continue
		}
		remainingTokens :=
			workPatternSemanticContextTokenBudget - used
		truncated := false
		if workPatternEstimatedTokens(content) > remainingTokens {
			content = truncateTaskContext(content, remainingTokens*4)
			truncated = true
		}
		if content == "" {
			continue
		}
		result = append(result, WorkPatternSemanticSignal{
			Ref:           candidate.Ref,
			Section:       candidate.Section,
			SourceKind:    candidate.SourceKind,
			SourceID:      candidate.SourceID,
			ObservedAt:    candidate.ObservedAt,
			Content:       content,
			EvidenceClass: candidate.EvidenceClass,
			Relevance:     candidate.Relevance,
			Truncated:     truncated,
			Learning:      candidate.Learning,
		})
		selectedContent[contentKey] = struct{}{}
		used += workPatternEstimatedTokens(content)
	}
	return result
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

func selectWorkPatternCapabilitySignalsAt(
	store *ActivityStore,
	cutoffAt int64,
	query string,
) ([]WorkPatternCapabilitySignal, []WorkPatternProfileSelectionEntry, error) {
	signals, err := listWorkPatternCapabilitySignalsAt(store, cutoffAt)
	if err != nil {
		return nil, nil, err
	}
	query = strings.TrimSpace(query)
	type rankedSignal struct {
		signal    WorkPatternCapabilitySignal
		relevance float64
	}
	ranked := make([]rankedSignal, 0, len(signals))
	for _, signal := range signals {
		relevance := float64(1)
		if query != "" {
			relevance = taskContextSourceRelevance(
				query,
				signal.CapabilityKey,
				0,
			)
		}
		ranked = append(ranked, rankedSignal{
			relevance: relevance,
			signal:    signal,
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].relevance != ranked[j].relevance {
			return ranked[i].relevance > ranked[j].relevance
		}
		return ranked[i].signal.CapabilityKey <
			ranked[j].signal.CapabilityKey
	})
	result := make([]WorkPatternCapabilitySignal, 0, len(ranked))
	manifest := make([]WorkPatternProfileSelectionEntry, 0, len(ranked))
	used := 0
	for _, item := range ranked {
		entry := WorkPatternProfileSelectionEntry{
			Ref:           "capability:" + item.signal.ProfileID,
			Kind:          "capability",
			EvidenceClass: WorkPatternEvidenceEvaluated,
			Reason:        "omitted_by_relevance",
			Learning:      item.signal.Learning,
		}
		if item.relevance <= 0 {
			manifest = append(manifest, entry)
			continue
		}
		encoded, _ := json.Marshal(item.signal)
		estimatedTokens := workPatternEstimatedTokens(string(encoded))
		if used+estimatedTokens >
			workPatternCapabilityContextTokenBudget {
			entry.Reason = "omitted_by_token_budget"
			manifest = append(manifest, entry)
			continue
		}
		result = append(result, item.signal)
		used += estimatedTokens
		entry.Included = true
		entry.Reason = "selected_for_current_context"
		manifest = append(manifest, entry)
	}
	sort.SliceStable(manifest, func(i, j int) bool {
		return manifest[i].Ref < manifest[j].Ref
	})
	return result, manifest, nil
}

func workPatternProfileSelectionManifest(
	snapshot WorkPatternProfileSnapshot,
	sources TaskContextSourceResult,
	selected []WorkPatternProjection,
	semantic []WorkPatternSemanticSignal,
	capabilityManifest []WorkPatternProfileSelectionEntry,
	contradictions []WorkPatternProfileCorrection,
) []WorkPatternProfileSelectionEntry {
	selectedProjections := make(map[string]struct{}, len(selected))
	for _, projection := range selected {
		selectedProjections[workPatternProjectionKey(projection)] = struct{}{}
	}
	contradictedScopes := make(map[string]struct{}, len(contradictions))
	for _, correction := range contradictions {
		contradictedScopes[correction.ScopeKind+"\x00"+correction.ScopeKey] =
			struct{}{}
	}
	result := make([]WorkPatternProfileSelectionEntry, 0)
	for _, projection := range snapshot.Projections {
		key := workPatternProjectionKey(projection)
		entry := WorkPatternProfileSelectionEntry{
			Ref: strings.Join([]string{
				"projection",
				projection.ScopeKind,
				projection.ScopeKey,
			}, ":"),
			Kind:          "projection",
			EvidenceClass: projection.EvidenceKind,
			Reason:        "not_selected_for_current_context",
		}
		if _, ok := selectedProjections[key]; ok {
			entry.Included = true
			entry.Reason = "selected_for_current_context"
		} else if _, ok := contradictedScopes[key]; ok {
			entry.Reason = "explicitly_contradicted"
		} else if projection.Status != WorkPatternProfileStatusQualified {
			entry.Reason = "insufficient_evidence"
		}
		result = append(result, entry)
	}

	selectedSemantic := make(map[string]WorkPatternSemanticSignal, len(semantic))
	selectedSemanticContent := make(map[string]struct{}, len(semantic))
	for _, signal := range semantic {
		selectedSemantic[signal.Ref] = signal
		selectedSemanticContent[workPatternSemanticContentKey(signal.Content)] =
			struct{}{}
		result = append(result, WorkPatternProfileSelectionEntry{
			Ref:           "semantic:" + signal.Ref,
			Kind:          "semantic",
			EvidenceClass: signal.EvidenceClass,
			Included:      true,
			Reason: func() string {
				if signal.Truncated {
					return "selected_and_truncated_to_token_budget"
				}
				return "selected_for_current_context"
			}(),
			Truncated: signal.Truncated,
			Learning:  signal.Learning,
		})
	}
	for _, candidate := range sources.Candidates {
		if !workPatternSemanticSectionAllowed(candidate.Section) ||
			candidate.ObservedAt > snapshot.SourceCutoffAt ||
			candidate.PrivacyDecision == TaskContextPrivacyExclude ||
			candidate.EvidenceClass == TaskContextEvidenceObservedContext ||
			candidate.EvidenceClass == TaskContextEvidenceHiddenShadow ||
			candidate.EvidenceClass == TaskContextEvidenceAgentGenerated {
			continue
		}
		if _, ok := selectedSemantic[candidate.Ref]; ok {
			continue
		}
		reason := "omitted_by_token_budget_or_relevance"
		content := workPatternSemanticCandidateContent(candidate)
		contentKey := workPatternSemanticContentKey(content)
		if _, duplicate := selectedSemanticContent[contentKey]; duplicate {
			reason = "duplicate_semantic_content"
		}
		result = append(result, WorkPatternProfileSelectionEntry{
			Ref:           "semantic:" + candidate.Ref,
			Kind:          "semantic",
			EvidenceClass: candidate.EvidenceClass,
			Reason:        reason,
			Learning:      candidate.Learning,
		})
	}
	result = append(result, capabilityManifest...)
	for _, correction := range contradictions {
		result = append(result, WorkPatternProfileSelectionEntry{
			Ref:           "contradiction:" + correction.ID,
			Kind:          "contradiction",
			EvidenceClass: TaskContextEvidenceExplicitUser,
			Reason:        "explicitly_contradicted",
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Ref < result[j].Ref
	})
	return result
}

func buildWorkPatternProfileContext(
	store *ActivityStore,
	snapshot WorkPatternProfileSnapshot,
	sources TaskContextSourceResult,
	query string,
	appKeys, transitionKeys []string,
	influenceMode string,
) (WorkPatternProfileContext, error) {
	if snapshot.ID == "" || len(snapshot.SHA256) != 64 ||
		workPatternHash([]byte(snapshot.CanonicalJSON)) != snapshot.SHA256 {
		return WorkPatternProfileContext{}, errors.New(
			"work pattern context requires a valid immutable snapshot",
		)
	}
	corrections, err := store.ListWorkPatternProfileCorrections(
		snapshot.SourceCutoffAt,
	)
	if err != nil {
		return WorkPatternProfileContext{}, err
	}
	selected, contradictions := selectWorkPatternProjections(
		snapshot,
		appKeys,
		transitionKeys,
		corrections,
	)
	semantic := selectWorkPatternSemanticSignals(
		sources,
		snapshot.SourceCutoffAt,
	)
	capabilities, capabilityManifest, err := selectWorkPatternCapabilitySignalsAt(
		store,
		snapshot.SourceCutoffAt,
		query,
	)
	if err != nil {
		return WorkPatternProfileContext{}, err
	}
	selectionManifest := workPatternProfileSelectionManifest(
		snapshot,
		sources,
		selected,
		semantic,
		capabilityManifest,
		contradictions,
	)
	mode := normalizeWorkPatternInfluenceMode(influenceMode)
	reason := "insufficient_profile_evidence"
	if snapshot.Status == WorkPatternProfileStatusQualified {
		reason = "qualified_profile_available"
	}
	if len(selected) == 0 && snapshot.Status == WorkPatternProfileStatusQualified {
		reason = "no_supported_contextual_projection"
	}
	payload := workPatternProfileContextPayload{
		SnapshotID:          snapshot.ID,
		SnapshotSHA256:      snapshot.SHA256,
		PolicyVersion:       snapshot.PolicyVersion,
		SourceCutoffAt:      snapshot.SourceCutoffAt,
		Status:              snapshot.Status,
		Confidence:          snapshot.Confidence,
		InfluenceMode:       mode,
		SelectedProjections: selected,
		SemanticSignals:     semantic,
		CapabilitySignals:   capabilities,
		Contradictions:      contradictions,
		SelectionManifest:   selectionManifest,
		SelectionReason:     reason,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return WorkPatternProfileContext{}, err
	}
	return WorkPatternProfileContext{
		SnapshotID:          payload.SnapshotID,
		SnapshotSHA256:      payload.SnapshotSHA256,
		PolicyVersion:       payload.PolicyVersion,
		SourceCutoffAt:      payload.SourceCutoffAt,
		Status:              payload.Status,
		Confidence:          payload.Confidence,
		InfluenceMode:       payload.InfluenceMode,
		SelectedProjections: payload.SelectedProjections,
		SemanticSignals:     payload.SemanticSignals,
		CapabilitySignals:   payload.CapabilitySignals,
		Contradictions:      payload.Contradictions,
		SelectionManifest:   payload.SelectionManifest,
		SelectionReason:     payload.SelectionReason,
		CanonicalJSON:       string(canonical),
	}, nil
}

func neutralWorkPatternProfileContext(
	value WorkPatternProfileContext,
) WorkPatternProfileContext {
	value.InfluenceMode = WorkPatternInfluenceNeutral
	value.SelectedProjections = []WorkPatternProjection{}
	value.SemanticSignals = []WorkPatternSemanticSignal{}
	value.CapabilitySignals = []WorkPatternCapabilitySignal{}
	for index := range value.SelectionManifest {
		item := &value.SelectionManifest[index]
		if item.Kind == "contradiction" {
			continue
		}
		item.Included = false
		item.Reason = "neutral_fallback"
		item.Truncated = false
	}
	payload := workPatternProfileContextPayload{
		SnapshotID:          value.SnapshotID,
		SnapshotSHA256:      value.SnapshotSHA256,
		PolicyVersion:       value.PolicyVersion,
		SourceCutoffAt:      value.SourceCutoffAt,
		Status:              value.Status,
		Confidence:          value.Confidence,
		InfluenceMode:       value.InfluenceMode,
		SelectedProjections: value.SelectedProjections,
		SemanticSignals:     value.SemanticSignals,
		CapabilitySignals:   value.CapabilitySignals,
		Contradictions:      value.Contradictions,
		SelectionManifest:   value.SelectionManifest,
		SelectionReason:     "neutral_fallback",
	}
	payloadBytes, _ := json.Marshal(payload)
	value.SelectionReason = payload.SelectionReason
	value.CanonicalJSON = string(payloadBytes)
	return value
}

func profileWorkPatternProfileContext(
	value WorkPatternProfileContext,
) WorkPatternProfileContext {
	value.InfluenceMode = WorkPatternInfluenceProfile
	payload := workPatternProfileContextPayload{
		SnapshotID:          value.SnapshotID,
		SnapshotSHA256:      value.SnapshotSHA256,
		PolicyVersion:       value.PolicyVersion,
		SourceCutoffAt:      value.SourceCutoffAt,
		Status:              value.Status,
		Confidence:          value.Confidence,
		InfluenceMode:       value.InfluenceMode,
		SelectedProjections: value.SelectedProjections,
		SemanticSignals:     value.SemanticSignals,
		CapabilitySignals:   value.CapabilitySignals,
		Contradictions:      value.Contradictions,
		SelectionManifest:   value.SelectionManifest,
		SelectionReason:     value.SelectionReason,
	}
	payloadBytes, _ := json.Marshal(payload)
	value.CanonicalJSON = string(payloadBytes)
	return value
}

func workPatternProfileEvidenceQualified(
	context WorkPatternProfileContext,
) bool {
	return context.Status == WorkPatternProfileStatusQualified &&
		len(context.SelectedProjections) > 0
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
