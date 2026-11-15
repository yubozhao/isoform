package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"wails-gui/internal/pii"
)

const (
	TaskContextAssemblerVersion = "task-context-v6"
	taskContextDefaultBudget    = 64_000
	taskContextMinimumFragment  = 128

	TaskContextSectionWorkProfile = "work_profile"
	TaskContextSectionBoundaries  = "hard_boundaries"
	TaskContextSectionCommitments = "commitments"
	TaskContextSectionSelf        = "self"
	TaskContextSectionKnowledge   = "knowledge"
	TaskContextSectionOutcomes    = "related_outcomes"
	TaskContextSectionInitiatives = "active_initiatives"
	TaskContextSectionAuthority   = "authority"
	TaskContextSectionActivity    = "activity"

	TaskContextEvidenceObservedUser           = "observed_user"
	TaskContextEvidenceExplicitUser           = "explicit_user"
	TaskContextEvidenceEvaluatedProjection    = "evaluated_projection"
	TaskContextEvidenceVerifiedExternalResult = "verified_external_result"
	TaskContextEvidenceProfileProjection      = "profile_projection"
	TaskContextEvidenceObservedContext        = "observed_context"
	TaskContextEvidenceAgentGenerated         = "agent_generated"
	TaskContextEvidenceHiddenShadow           = "hidden_shadow"

	TaskContextPrivacyInclude = "include"
	TaskContextPrivacyRedact  = "redact"
	TaskContextPrivacyExclude = "exclude"
)

// TaskContextLearningProvenance binds a distilled Shadow learning to the
// immutable revision and materialization receipt that made it available. The
// receipt timestamp, not a backdated artifact, is its earliest usable time.
type TaskContextLearningProvenance struct {
	Version                  string `json:"version"`
	RunID                    string `json:"runId"`
	RevisionID               string `json:"revisionId"`
	MaterializationReceiptID string `json:"materializationReceiptId"`
	TargetKind               string `json:"targetKind"`
	TargetID                 string `json:"targetId"`
	ProjectionKey            string `json:"projectionKey"`
	CapabilityKey            string `json:"capabilityKey"`
	LearningKind             string `json:"learningKind"`
	EvidenceHash             string `json:"evidenceHash"`
	AvailableAt              int64  `json:"availableAt"`
	AuthorityEffect          string `json:"authorityEffect"`
}

var taskContextSectionOrder = []string{
	TaskContextSectionWorkProfile,
	TaskContextSectionBoundaries,
	TaskContextSectionCommitments,
	TaskContextSectionSelf,
	TaskContextSectionKnowledge,
	TaskContextSectionOutcomes,
	TaskContextSectionInitiatives,
	TaskContextSectionAuthority,
	TaskContextSectionActivity,
}

var taskContextDefaultSectionBudgets = map[string]int{
	TaskContextSectionWorkProfile: 16_000,
	TaskContextSectionBoundaries:  8_000,
	TaskContextSectionCommitments: 8_000,
	TaskContextSectionSelf:        8_000,
	TaskContextSectionKnowledge:   12_000,
	TaskContextSectionOutcomes:    8_000,
	TaskContextSectionInitiatives: 8_000,
	TaskContextSectionAuthority:   6_000,
	TaskContextSectionActivity:    16_000,
}

// TaskContextCandidate is a source-backed fact that was available no later
// than the task-start cutoff. Evidence class is explicit so hidden shadow
// hypotheses and unsupported agent output cannot leak into production context.
type TaskContextCandidate struct {
	Ref             string                         `json:"ref"`
	Section         string                         `json:"section"`
	SourceKind      string                         `json:"sourceKind"`
	SourceID        string                         `json:"sourceId"`
	ObservedAt      int64                          `json:"observedAt"`
	Content         string                         `json:"content"`
	EvidenceClass   string                         `json:"evidenceClass"`
	PrivacyDecision string                         `json:"privacyDecision"`
	Relevance       float64                        `json:"relevance"`
	Required        bool                           `json:"required,omitempty"`
	Learning        *TaskContextLearningProvenance `json:"learning,omitempty"`
}

type TaskContextAssemblyInput struct {
	EpisodeID     string                     `json:"episodeId"`
	CutoffAt      int64                      `json:"cutoffAt"`
	CreatedAt     int64                      `json:"createdAt"`
	TotalBudget   int                        `json:"totalBudget"`
	SectionBudget map[string]int             `json:"sectionBudget,omitempty"`
	Candidates    []TaskContextCandidate     `json:"candidates"`
	PreExcluded   []TaskContextManifestEntry `json:"preExcluded,omitempty"`
}

type TaskContextEvidence struct {
	Ref           string                         `json:"ref"`
	SourceKind    string                         `json:"sourceKind"`
	SourceID      string                         `json:"sourceId"`
	ObservedAt    int64                          `json:"observedAt"`
	EvidenceClass string                         `json:"evidenceClass"`
	Content       string                         `json:"content"`
	ContentHash   string                         `json:"contentHash"`
	Truncated     bool                           `json:"truncated,omitempty"`
	Learning      *TaskContextLearningProvenance `json:"learning,omitempty"`
}

type TaskContextSection struct {
	Name          string                `json:"name"`
	BudgetChars   int                   `json:"budgetChars"`
	IncludedChars int                   `json:"includedChars"`
	Evidence      []TaskContextEvidence `json:"evidence"`
}

type TaskContextManifestEntry struct {
	Ref             string                         `json:"ref"`
	Section         string                         `json:"section"`
	SourceKind      string                         `json:"sourceKind"`
	SourceID        string                         `json:"sourceId"`
	ObservedAt      int64                          `json:"observedAt"`
	EvidenceClass   string                         `json:"evidenceClass"`
	PrivacyDecision string                         `json:"privacyDecision"`
	Included        bool                           `json:"included"`
	Reason          string                         `json:"reason,omitempty"`
	ContentHash     string                         `json:"contentHash,omitempty"`
	IncludedChars   int                            `json:"includedChars,omitempty"`
	Learning        *TaskContextLearningProvenance `json:"learning,omitempty"`
}

type TaskContextPayload struct {
	AssemblerVersion string               `json:"assemblerVersion"`
	CutoffAt         int64                `json:"cutoffAt"`
	TotalBudgetChars int                  `json:"totalBudgetChars"`
	IncludedChars    int                  `json:"includedChars"`
	Sections         []TaskContextSection `json:"sections"`
}

type AssembledTaskContext struct {
	EpisodeID     string                     `json:"episodeId"`
	CutoffAt      int64                      `json:"cutoffAt"`
	CreatedAt     int64                      `json:"createdAt"`
	CanonicalJSON string                     `json:"canonicalJson"`
	SHA256        string                     `json:"sha256"`
	Payload       TaskContextPayload         `json:"payload"`
	Manifest      []TaskContextManifestEntry `json:"manifest"`
}

func normalizeTaskContextSection(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, section := range taskContextSectionOrder {
		if value == section {
			return value, nil
		}
	}
	return "", fmt.Errorf("unsupported task context section %q", value)
}

func normalizeTaskContextEvidenceClass(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case TaskContextEvidenceObservedUser,
		TaskContextEvidenceExplicitUser,
		TaskContextEvidenceEvaluatedProjection,
		TaskContextEvidenceVerifiedExternalResult,
		TaskContextEvidenceProfileProjection,
		TaskContextEvidenceObservedContext,
		TaskContextEvidenceAgentGenerated,
		TaskContextEvidenceHiddenShadow:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported task context evidence class %q", value)
	}
}

func normalizeTaskContextPrivacy(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return TaskContextPrivacyRedact, nil
	}
	switch value {
	case TaskContextPrivacyInclude, TaskContextPrivacyRedact, TaskContextPrivacyExclude:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported task context privacy decision %q", value)
	}
}

func taskContextHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func truncateTaskContext(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func taskContextBudget(input TaskContextAssemblyInput, section string) int {
	if value, ok := input.SectionBudget[section]; ok {
		return value
	}
	return taskContextDefaultSectionBudgets[section]
}

func normalizeTaskContextCandidate(
	input TaskContextCandidate,
	cutoffAt int64,
) (TaskContextCandidate, error) {
	input.Ref = strings.TrimSpace(input.Ref)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.Content = strings.TrimSpace(input.Content)
	if input.Ref == "" || input.SourceKind == "" || input.SourceID == "" {
		return TaskContextCandidate{}, errors.New("task context evidence requires ref, source kind, and source id")
	}
	if input.ObservedAt <= 0 {
		return TaskContextCandidate{}, fmt.Errorf("task context evidence %q requires a timestamp", input.Ref)
	}
	if input.ObservedAt > cutoffAt {
		return TaskContextCandidate{}, fmt.Errorf(
			"task context evidence %q at %d exceeds cutoff %d",
			input.Ref,
			input.ObservedAt,
			cutoffAt,
		)
	}
	var err error
	if input.Section, err = normalizeTaskContextSection(input.Section); err != nil {
		return TaskContextCandidate{}, err
	}
	if input.EvidenceClass, err = normalizeTaskContextEvidenceClass(input.EvidenceClass); err != nil {
		return TaskContextCandidate{}, err
	}
	if input.PrivacyDecision, err = normalizeTaskContextPrivacy(input.PrivacyDecision); err != nil {
		return TaskContextCandidate{}, err
	}
	if input.Relevance < 0 {
		input.Relevance = 0
	}
	if input.Relevance > 1 {
		input.Relevance = 1
	}
	// Verified shadow learning was the only source with learning provenance;
	// the shadow agent is removed, so nothing may claim it.
	if input.SourceKind == taskContextSourceVerifiedShadowLearningKind ||
		input.Learning != nil {
		return TaskContextCandidate{}, fmt.Errorf(
			"task context evidence %q claims removed shadow learning provenance",
			input.Ref,
		)
	}
	return input, nil
}

func AssembleTaskContext(input TaskContextAssemblyInput) (AssembledTaskContext, error) {
	input.EpisodeID = strings.TrimSpace(input.EpisodeID)
	if input.EpisodeID == "" {
		return AssembledTaskContext{}, errors.New("task context requires an episode id")
	}
	if input.CutoffAt <= 0 || input.CreatedAt <= 0 {
		return AssembledTaskContext{}, errors.New("task context requires stable cutoff and creation timestamps")
	}
	if input.TotalBudget <= 0 {
		input.TotalBudget = taskContextDefaultBudget
	}

	candidates := make([]TaskContextCandidate, 0, len(input.Candidates))
	seen := make(map[string]TaskContextCandidate, len(input.Candidates))
	for _, raw := range input.Candidates {
		candidate, err := normalizeTaskContextCandidate(raw, input.CutoffAt)
		if err != nil {
			return AssembledTaskContext{}, err
		}
		if previous, ok := seen[candidate.Ref]; ok {
			previousJSON, _ := json.Marshal(previous)
			candidateJSON, _ := json.Marshal(candidate)
			if string(previousJSON) != string(candidateJSON) {
				return AssembledTaskContext{}, fmt.Errorf("task context ref %q has conflicting evidence", candidate.Ref)
			}
			continue
		}
		seen[candidate.Ref] = candidate
		candidates = append(candidates, candidate)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Section != right.Section {
			return taskContextSectionIndex(left.Section) < taskContextSectionIndex(right.Section)
		}
		if left.Required != right.Required {
			return left.Required
		}
		if left.Relevance != right.Relevance {
			return left.Relevance > right.Relevance
		}
		if left.ObservedAt != right.ObservedAt {
			return left.ObservedAt > right.ObservedAt
		}
		return left.Ref < right.Ref
	})

	sections := make([]TaskContextSection, 0, len(taskContextSectionOrder))
	sectionByName := make(map[string]*TaskContextSection, len(taskContextSectionOrder))
	for _, name := range taskContextSectionOrder {
		budget := taskContextBudget(input, name)
		if budget < 0 {
			return AssembledTaskContext{}, fmt.Errorf("task context section %q has a negative budget", name)
		}
		sections = append(sections, TaskContextSection{
			Name:        name,
			BudgetChars: budget,
			Evidence:    []TaskContextEvidence{},
		})
		sectionByName[name] = &sections[len(sections)-1]
	}

	manifest := make(
		[]TaskContextManifestEntry,
		0,
		len(input.PreExcluded)+len(candidates),
	)
	for _, entry := range input.PreExcluded {
		entry.Ref = strings.TrimSpace(entry.Ref)
		entry.SourceKind = strings.TrimSpace(entry.SourceKind)
		entry.SourceID = strings.TrimSpace(entry.SourceID)
		entry.Reason = strings.TrimSpace(entry.Reason)
		entry.Included = false
		entry.ContentHash = ""
		entry.IncludedChars = 0
		if entry.Ref == "" || entry.SourceKind == "" ||
			entry.SourceID == "" || entry.Reason == "" {
			return AssembledTaskContext{}, errors.New(
				"pre-excluded task context manifest entry is incomplete",
			)
		}
		manifest = append(manifest, entry)
	}
	sort.SliceStable(manifest, func(i, j int) bool {
		if manifest[i].SourceKind != manifest[j].SourceKind {
			return manifest[i].SourceKind < manifest[j].SourceKind
		}
		if manifest[i].SourceID != manifest[j].SourceID {
			return manifest[i].SourceID < manifest[j].SourceID
		}
		if manifest[i].ObservedAt != manifest[j].ObservedAt {
			return manifest[i].ObservedAt < manifest[j].ObservedAt
		}
		return manifest[i].Ref < manifest[j].Ref
	})
	totalUsed := 0
	for _, candidate := range candidates {
		entry := TaskContextManifestEntry{
			Ref:             candidate.Ref,
			Section:         candidate.Section,
			SourceKind:      candidate.SourceKind,
			SourceID:        candidate.SourceID,
			ObservedAt:      candidate.ObservedAt,
			EvidenceClass:   candidate.EvidenceClass,
			PrivacyDecision: candidate.PrivacyDecision,
			Learning:        candidate.Learning,
		}
		switch candidate.EvidenceClass {
		case TaskContextEvidenceHiddenShadow:
			entry.Reason = "hidden_shadow_excluded"
			manifest = append(manifest, entry)
			continue
		case TaskContextEvidenceAgentGenerated:
			entry.Reason = "unsupported_agent_claim_excluded"
			manifest = append(manifest, entry)
			continue
		}
		if candidate.PrivacyDecision == TaskContextPrivacyExclude {
			entry.Reason = "privacy_excluded"
			manifest = append(manifest, entry)
			continue
		}

		content := candidate.Content
		if candidate.PrivacyDecision == TaskContextPrivacyRedact {
			content, _ = pii.RedactText(content)
			content = strings.TrimSpace(content)
		}
		if content == "" {
			entry.Reason = "empty_after_privacy"
			manifest = append(manifest, entry)
			continue
		}

		section := sectionByName[candidate.Section]
		remainingSection := section.BudgetChars - section.IncludedChars
		remainingTotal := input.TotalBudget - totalUsed
		remaining := min(remainingSection, remainingTotal)
		if remaining <= 0 {
			if candidate.Required {
				return AssembledTaskContext{}, fmt.Errorf(
					"required task context %q does not fit its budget",
					candidate.Ref,
				)
			}
			entry.Reason = "budget_exhausted"
			manifest = append(manifest, entry)
			continue
		}

		included := content
		truncated := false
		if len(included) > remaining {
			if candidate.Required {
				return AssembledTaskContext{}, fmt.Errorf(
					"required task context %q does not fit its budget",
					candidate.Ref,
				)
			}
			if remaining < taskContextMinimumFragment {
				entry.Reason = "insufficient_fragment_budget"
				manifest = append(manifest, entry)
				continue
			}
			included = truncateTaskContext(included, remaining)
			truncated = true
		}
		if included == "" {
			entry.Reason = "empty_after_budget"
			manifest = append(manifest, entry)
			continue
		}

		hash := taskContextHash(included)
		section.Evidence = append(section.Evidence, TaskContextEvidence{
			Ref:           candidate.Ref,
			SourceKind:    candidate.SourceKind,
			SourceID:      candidate.SourceID,
			ObservedAt:    candidate.ObservedAt,
			EvidenceClass: candidate.EvidenceClass,
			Content:       included,
			ContentHash:   hash,
			Truncated:     truncated,
			Learning:      candidate.Learning,
		})
		section.IncludedChars += len(included)
		totalUsed += len(included)
		entry.Included = true
		entry.ContentHash = hash
		entry.IncludedChars = len(included)
		if truncated {
			entry.Reason = "included_truncated"
		} else {
			entry.Reason = "included"
		}
		manifest = append(manifest, entry)
	}

	payload := TaskContextPayload{
		AssemblerVersion: TaskContextAssemblerVersion,
		CutoffAt:         input.CutoffAt,
		TotalBudgetChars: input.TotalBudget,
		IncludedChars:    totalUsed,
		Sections:         sections,
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return AssembledTaskContext{}, fmt.Errorf("encode task context: %w", err)
	}
	canonicalJSON, err := canonicalTaskEpisodeJSON(string(canonical), true)
	if err != nil {
		return AssembledTaskContext{}, fmt.Errorf("canonicalize task context: %w", err)
	}
	return AssembledTaskContext{
		EpisodeID:     input.EpisodeID,
		CutoffAt:      input.CutoffAt,
		CreatedAt:     input.CreatedAt,
		CanonicalJSON: canonicalJSON,
		SHA256:        taskContextHash(canonicalJSON),
		Payload:       payload,
		Manifest:      manifest,
	}, nil
}

func taskContextSectionIndex(section string) int {
	for index, candidate := range taskContextSectionOrder {
		if candidate == section {
			return index
		}
	}
	return len(taskContextSectionOrder)
}

func (context AssembledTaskContext) FrozenContext() TaskEpisodeFrozenContext {
	return TaskEpisodeFrozenContext{
		TaskEpisodeID: context.EpisodeID,
		CutoffAt:      context.CutoffAt,
		CanonicalJSON: context.CanonicalJSON,
		SHA256:        context.SHA256,
		Manifest:      append([]TaskContextManifestEntry(nil), context.Manifest...),
		CreatedAt:     context.CreatedAt,
	}
}
