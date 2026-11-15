package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	SelfDreamPolicyVersion     = "self-dream-v1"
	SelfDreamPromptVersion     = "self-dream-synthesis-v1"
	SelfDreamAssemblerVersion  = "self-dream-context-v3"
	SelfDreamEvaluationVersion = "self-dream-ablation-v1"
	SelfDreamSafetyMode        = "local_no_tools"

	SelfDreamOperationMerge         = "merge"
	SelfDreamOperationSplit         = "split"
	SelfDreamOperationContextualize = "contextualize"
	SelfDreamOperationContradict    = "contradict"
	SelfDreamOperationSupersede     = "supersede"
	SelfDreamOperationNoChange      = "no_change"

	SelfDreamValidationQualified      = "qualified"
	SelfDreamValidationReviewRequired = "review_required"
	SelfDreamValidationRejected       = "rejected"
	SelfDreamValidationNoChange       = "no_change"

	SelfDreamDecisionAccept            = "accept"
	SelfDreamDecisionKeepSeparate      = "keep_separate"
	SelfDreamDecisionNotEnoughEvidence = "not_enough_evidence"
	SelfDreamDecisionCorrect           = "correct"
	SelfDreamDecisionAutoPromote       = "auto_promote"

	selfDreamContextBudgetChars = 168_000
	selfDreamStatementMaxChars  = 2_000
	selfDreamRawOutputMaxChars  = 128_000
)

var selfDreamScopeKinds = map[string]bool{
	"global":         true,
	"app":            true,
	"app_transition": true,
	"work_mode":      true,
	"capability":     true,
	"project":        true,
	"relationship":   true,
}

type SelfDreamScope struct {
	Kind string `json:"kind"`
	Key  string `json:"key,omitempty"`
}

type SelfDreamRunRecord struct {
	ID               string                     `json:"id"`
	IdempotencyKey   string                     `json:"idempotencyKey"`
	TriggerKind      string                     `json:"triggerKind"`
	PolicyVersion    string                     `json:"policyVersion"`
	AssemblerVersion string                     `json:"assemblerVersion"`
	SourceCutoffAt   int64                      `json:"sourceCutoffAt"`
	BaseSelf         []SelfModelEntry           `json:"baseSelf"`
	BaseSelfHash     string                     `json:"baseSelfHash"`
	Context          AssembledTaskContext       `json:"context"`
	ContextHash      string                     `json:"contextHash"`
	Manifest         []TaskContextManifestEntry `json:"manifest"`
	InputHash        string                     `json:"inputHash"`
	PromptVersion    string                     `json:"promptVersion"`
	Prompt           string                     `json:"prompt"`
	PromptHash       string                     `json:"promptHash"`
	RequestedModel   string                     `json:"requestedModel"`
	CreatedAt        int64                      `json:"createdAt"`
}

type SelfDreamEventRecord struct {
	ID         string `json:"id"`
	RunID      string `json:"runId"`
	EventKind  string `json:"eventKind"`
	DetailJSON string `json:"detailJson"`
	CreatedAt  int64  `json:"createdAt"`
}

type SelfDreamAttemptRecord struct {
	ID               string `json:"id"`
	RunID            string `json:"runId"`
	AttemptKind      string `json:"attemptKind"`
	Status           string `json:"status"`
	SafetyMode       string `json:"safetyMode"`
	LearningEligible bool   `json:"learningEligible"`
	ProductMutation  bool   `json:"productMutation"`
	Model            string `json:"model"`
	PromptHash       string `json:"promptHash"`
	RawOutput        string `json:"rawOutput,omitempty"`
	RawOutputHash    string `json:"rawOutputHash"`
	Error            string `json:"error,omitempty"`
	DurationMs       int64  `json:"durationMs"`
	CreatedAt        int64  `json:"createdAt"`
}

type SelfDreamOperationDraft struct {
	Operation       string         `json:"operation"`
	TargetEntryIDs  []string       `json:"targetEntryIds"`
	SelfKind        string         `json:"selfKind"`
	Scope           SelfDreamScope `json:"scope"`
	Statement       string         `json:"statement,omitempty"`
	SplitStatements []string       `json:"splitStatements,omitempty"`
	ReasoningStatus string         `json:"reasoningStatus"`
	Rationale       string         `json:"rationale,omitempty"`
	Uncertainty     string         `json:"uncertainty,omitempty"`
	Confidence      float64        `json:"confidence"`
	EvidenceRefs    []string       `json:"evidenceRefs"`
}

type SelfDreamModelResponse struct {
	Operations    []SelfDreamOperationDraft `json:"operations"`
	Uncertainties []string                  `json:"uncertainties"`
}

type SelfDreamOperationRecord struct {
	ID               string         `json:"id"`
	RunID            string         `json:"runId"`
	AttemptID        string         `json:"attemptId"`
	Ordinal          int            `json:"ordinal"`
	Operation        string         `json:"operation"`
	TargetEntryIDs   []string       `json:"targetEntryIds"`
	SelfKind         string         `json:"selfKind"`
	Scope            SelfDreamScope `json:"scope"`
	Statement        string         `json:"statement,omitempty"`
	SplitStatements  []string       `json:"splitStatements,omitempty"`
	ReasoningStatus  string         `json:"reasoningStatus"`
	Rationale        string         `json:"rationale,omitempty"`
	Uncertainty      string         `json:"uncertainty,omitempty"`
	Confidence       float64        `json:"confidence"`
	EvidenceRefs     []string       `json:"evidenceRefs"`
	Source           string         `json:"source"`
	ValidationStatus string         `json:"validationStatus"`
	ValidationReason string         `json:"validationReason,omitempty"`
	AutoEligible     bool           `json:"autoEligible"`
	OperationHash    string         `json:"operationHash"`
	CreatedAt        int64          `json:"createdAt"`
}

type SelfDreamProjectionMetrics struct {
	EntryCount           int     `json:"entryCount"`
	DuplicateEntryCount  int     `json:"duplicateEntryCount"`
	DuplicateRate        float64 `json:"duplicateRate"`
	EvidenceCoverage     float64 `json:"evidenceCoverage"`
	InformationLoss      float64 `json:"informationLoss"`
	ContradictionRisk    float64 `json:"contradictionRisk"`
	UnsupportedReasoning int     `json:"unsupportedReasoning"`
	Quality              float64 `json:"quality"`
}

type SelfDreamEvaluationRecord struct {
	ID                   string                     `json:"id"`
	RunID                string                     `json:"runId"`
	EvaluationVersion    string                     `json:"evaluationVersion"`
	Neutral              SelfDreamProjectionMetrics `json:"neutral"`
	Dream                SelfDreamProjectionMetrics `json:"dream"`
	Improvement          float64                    `json:"improvement"`
	CoverageLoss         float64                    `json:"coverageLoss"`
	ContradictionRisk    float64                    `json:"contradictionRisk"`
	UnsupportedReasoning int                        `json:"unsupportedReasoning"`
	RealEvidence         bool                       `json:"realEvidence"`
	CreatedAt            int64                      `json:"createdAt"`
}

type SelfDreamDecisionRecord struct {
	ID             string `json:"id"`
	OperationID    string `json:"operationId"`
	IdempotencyKey string `json:"idempotencyKey"`
	ActorKind      string `json:"actorKind"`
	Decision       string `json:"decision"`
	Note           string `json:"note,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
}

type SelfDreamCompilationRecord struct {
	ID              string   `json:"id"`
	OperationID     string   `json:"operationId"`
	DecisionID      string   `json:"decisionId"`
	CompilationKind string   `json:"compilationKind"`
	SourceEntryIDs  []string `json:"sourceEntryIds"`
	OutputEntryIDs  []string `json:"outputEntryIds"`
	CreatedAt       int64    `json:"createdAt"`
}

type SelfDreamReplayRecord struct {
	ID                   string                     `json:"id"`
	RunID                string                     `json:"runId"`
	Status               string                     `json:"status"`
	SafetyMode           string                     `json:"safetyMode"`
	LearningEligible     bool                       `json:"learningEligible"`
	ProductMutation      bool                       `json:"productMutation"`
	ContextHash          string                     `json:"contextHash"`
	ContextMatchesSource bool                       `json:"contextMatchesSource"`
	PromptVersion        string                     `json:"promptVersion"`
	Model                string                     `json:"model"`
	PromptHash           string                     `json:"promptHash"`
	RawOutput            string                     `json:"rawOutput,omitempty"`
	RawOutputHash        string                     `json:"rawOutputHash"`
	Operations           []SelfDreamOperationRecord `json:"operations"`
	Error                string                     `json:"error,omitempty"`
	DurationMs           int64                      `json:"durationMs"`
	CreatedAt            int64                      `json:"createdAt"`
}

type SelfDreamRunDetail struct {
	Run          SelfDreamRunRecord           `json:"run"`
	Status       string                       `json:"status"`
	Events       []SelfDreamEventRecord       `json:"events"`
	Attempt      *SelfDreamAttemptRecord      `json:"attempt,omitempty"`
	Operations   []SelfDreamOperationRecord   `json:"operations"`
	Evaluation   *SelfDreamEvaluationRecord   `json:"evaluation,omitempty"`
	Decisions    []SelfDreamDecisionRecord    `json:"decisions"`
	Compilations []SelfDreamCompilationRecord `json:"compilations"`
	Replays      []SelfDreamReplayRecord      `json:"replays"`
}

type SelfDreamRunSummary struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	TriggerKind    string `json:"triggerKind"`
	SourceCutoffAt int64  `json:"sourceCutoffAt"`
	InputHash      string `json:"inputHash"`
	Model          string `json:"model,omitempty"`
	OperationCount int    `json:"operationCount"`
	QualifiedCount int    `json:"qualifiedCount"`
	ReviewCount    int    `json:"reviewCount"`
	RejectedCount  int    `json:"rejectedCount"`
	CreatedAt      int64  `json:"createdAt"`
}

type SelfDreamRolloutStatus struct {
	Enabled                   bool     `json:"enabled"`
	Ready                     bool     `json:"ready"`
	CompletedRealRuns         int      `json:"completedRealRuns"`
	ScoredCandidates          int      `json:"scoredCandidates"`
	MeanImprovement           float64  `json:"meanImprovement"`
	CoverageLossCount         int      `json:"coverageLossCount"`
	UnsupportedReasoningCount int      `json:"unsupportedReasoningCount"`
	CorrectionOrRejectRate    float64  `json:"correctionOrRejectRate"`
	StaleCompilationErrors    int      `json:"staleCompilationErrors"`
	Reasons                   []string `json:"reasons"`
	UpdatedAt                 int64    `json:"updatedAt,omitempty"`
}

type ReviewSelfDreamOperationInput struct {
	OperationID    string `json:"operationId"`
	IdempotencyKey string `json:"idempotencyKey"`
	Decision       string `json:"decision"`
	Note           string `json:"note,omitempty"`
}

type ReplaySelfDreamDebugInput struct {
	RunID  string `json:"runId"`
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
}

type ReplaySelfDreamDebugResult struct {
	Replay SelfDreamReplayRecord `json:"replay"`
	Prompt string                `json:"prompt"`
}

func normalizeSelfDreamScope(scope SelfDreamScope) (SelfDreamScope, error) {
	scope.Kind = strings.ToLower(strings.TrimSpace(scope.Kind))
	scope.Key = strings.TrimSpace(scope.Key)
	if scope.Kind == "" {
		scope.Kind = "global"
	}
	if !selfDreamScopeKinds[scope.Kind] {
		return SelfDreamScope{}, fmt.Errorf(
			"unsupported Self Dream scope %q",
			scope.Kind,
		)
	}
	if scope.Kind == "global" {
		if scope.Key != "" {
			return SelfDreamScope{}, errors.New(
				"global Self Dream scope cannot have a key",
			)
		}
		return scope, nil
	}
	if scope.Key == "" {
		return SelfDreamScope{}, fmt.Errorf(
			"Self Dream scope %s requires a key",
			scope.Kind,
		)
	}
	return scope, nil
}

func selfDreamScopeFromStored(value string) SelfDreamScope {
	value = strings.TrimSpace(value)
	if value == "" {
		return SelfDreamScope{Kind: "global"}
	}
	if kind, key, ok := strings.Cut(value, ":"); ok &&
		selfDreamScopeKinds[strings.ToLower(strings.TrimSpace(kind))] &&
		strings.TrimSpace(key) != "" {
		return SelfDreamScope{
			Kind: strings.ToLower(strings.TrimSpace(kind)),
			Key:  strings.TrimSpace(key),
		}
	}
	return SelfDreamScope{Kind: "capability", Key: value}
}

func selfDreamStoredScope(scope SelfDreamScope) string {
	scope, err := normalizeSelfDreamScope(scope)
	if err != nil || scope.Kind == "global" {
		return ""
	}
	return scope.Kind + ":" + scope.Key
}

func selfDreamCanonicalStatement(value string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if b.Len() > 0 && !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func selfDreamStatementWords(value string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, word := range strings.Fields(selfDreamCanonicalStatement(value)) {
		if len([]rune(word)) >= 2 {
			out[word] = struct{}{}
		}
	}
	return out
}

func selfDreamWordCoverage(source, output string) float64 {
	sourceWords := selfDreamStatementWords(source)
	if len(sourceWords) == 0 {
		return 1
	}
	outputWords := selfDreamStatementWords(output)
	matched := 0
	for word := range sourceWords {
		if _, ok := outputWords[word]; ok {
			matched++
		}
	}
	return float64(matched) / float64(len(sourceWords))
}

func selfDreamOperationHash(draft SelfDreamOperationDraft) string {
	draft.TargetEntryIDs = uniqueSortedStrings(draft.TargetEntryIDs)
	draft.EvidenceRefs = uniqueSortedStrings(draft.EvidenceRefs)
	draft.SplitStatements = uniqueSortedStrings(draft.SplitStatements)
	payload, _ := json.Marshal(draft)
	return sha256Hex(payload)
}

func selfDreamCurrentEntryByID(
	entries []SelfModelEntry,
) map[string]SelfModelEntry {
	out := make(map[string]SelfModelEntry, len(entries))
	for _, entry := range entries {
		out[entry.ID] = entry
	}
	return out
}

func selfDreamIncludedManifest(
	manifest []TaskContextManifestEntry,
) map[string]TaskContextManifestEntry {
	out := make(map[string]TaskContextManifestEntry)
	for _, item := range manifest {
		if item.Included {
			out[item.Ref] = item
		}
	}
	return out
}

func selfDreamEntryManifestRefs(
	manifest []TaskContextManifestEntry,
) map[string]string {
	out := make(map[string]string)
	for _, item := range manifest {
		if item.Included && item.SourceKind == taskContextSourceSelfKind {
			out[item.SourceID] = item.Ref
		}
	}
	return out
}

func detectDeterministicSelfDreamOperations(
	entries []SelfModelEntry,
	manifest []TaskContextManifestEntry,
) []SelfDreamOperationDraft {
	manifestRefs := selfDreamEntryManifestRefs(manifest)
	groups := make(map[string][]SelfModelEntry)
	for _, entry := range entries {
		if entry.Status != selfModelStatusActive ||
			len(entry.Contradictions) > 0 {
			continue
		}
		canonical := selfDreamCanonicalStatement(entry.Statement)
		if canonical == "" {
			continue
		}
		scope := strings.TrimSpace(entry.Scope)
		key := strings.Join([]string{entry.Kind, scope, canonical}, "\x00")
		groups[key] = append(groups[key], entry)
	}
	keys := make([]string, 0, len(groups))
	for key, group := range groups {
		if len(group) >= 2 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]SelfDreamOperationDraft, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].CreatedAt != group[j].CreatedAt {
				return group[i].CreatedAt < group[j].CreatedAt
			}
			return group[i].ID < group[j].ID
		})
		targets := make([]string, 0, len(group))
		refs := make([]string, 0, len(group))
		missingRef := false
		for _, entry := range group {
			targets = append(targets, entry.ID)
			ref := manifestRefs[entry.ID]
			if ref == "" {
				missingRef = true
				break
			}
			refs = append(refs, ref)
		}
		if missingRef {
			continue
		}
		out = append(out, SelfDreamOperationDraft{
			Operation:       SelfDreamOperationMerge,
			TargetEntryIDs:  targets,
			SelfKind:        group[0].Kind,
			Scope:           selfDreamScopeFromStored(group[0].Scope),
			Statement:       group[0].Statement,
			ReasoningStatus: "unknown",
			Uncertainty:     "No new reasoning is inferred; only exact duplicate statements are combined.",
			Confidence:      1,
			EvidenceRefs:    refs,
		})
	}
	return out
}

func selfDreamOperationAllowed(value string) bool {
	switch value {
	case SelfDreamOperationMerge,
		SelfDreamOperationSplit,
		SelfDreamOperationContextualize,
		SelfDreamOperationContradict,
		SelfDreamOperationSupersede,
		SelfDreamOperationNoChange:
		return true
	default:
		return false
	}
}

func selfDreamMentionsAuthorityMutation(value string) bool {
	value = selfDreamCanonicalStatement(value)
	for _, phrase := range []string{
		"grant authority",
		"grant permission",
		"authorize execution",
		"auto approve",
		"execute without approval",
		"bypass approval",
	} {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

func validateSelfDreamOperation(
	draft SelfDreamOperationDraft,
	source string,
	base []SelfModelEntry,
	manifest []TaskContextManifestEntry,
	now int64,
) SelfDreamOperationRecord {
	record := SelfDreamOperationRecord{
		Operation:      strings.ToLower(strings.TrimSpace(draft.Operation)),
		TargetEntryIDs: uniqueSortedStrings(draft.TargetEntryIDs),
		SelfKind:       strings.ToLower(strings.TrimSpace(draft.SelfKind)),
		Statement:      agencyBoundedText(draft.Statement, selfDreamStatementMaxChars),
		SplitStatements: selfDreamBoundedList(
			draft.SplitStatements,
			8,
			selfDreamStatementMaxChars,
		),
		ReasoningStatus: strings.ToLower(strings.TrimSpace(draft.ReasoningStatus)),
		Rationale:       agencyBoundedText(draft.Rationale, 2_000),
		Uncertainty:     agencyBoundedText(draft.Uncertainty, 2_000),
		Confidence:      clampConfidence(draft.Confidence),
		EvidenceRefs:    uniqueSortedStrings(draft.EvidenceRefs),
		Source:          source,
		CreatedAt:       now,
	}
	if record.ReasoningStatus == "" {
		record.ReasoningStatus = "unknown"
	}
	scope, scopeErr := normalizeSelfDreamScope(draft.Scope)
	record.Scope = scope
	reject := func(reason string) SelfDreamOperationRecord {
		record.ValidationStatus = SelfDreamValidationRejected
		record.ValidationReason = reason
		record.AutoEligible = false
		record.OperationHash = selfDreamOperationHash(SelfDreamOperationDraft{
			Operation:       record.Operation,
			TargetEntryIDs:  record.TargetEntryIDs,
			SelfKind:        record.SelfKind,
			Scope:           record.Scope,
			Statement:       record.Statement,
			SplitStatements: record.SplitStatements,
			ReasoningStatus: record.ReasoningStatus,
			Rationale:       record.Rationale,
			Uncertainty:     record.Uncertainty,
			Confidence:      record.Confidence,
			EvidenceRefs:    record.EvidenceRefs,
		})
		return record
	}
	if !selfDreamOperationAllowed(record.Operation) {
		return reject("unsupported_operation")
	}
	if scopeErr != nil {
		return reject("invalid_scope")
	}
	if record.Operation == SelfDreamOperationNoChange {
		record.ValidationStatus = SelfDreamValidationNoChange
		record.ValidationReason = "model_found_no_supported_change"
		record.OperationHash = selfDreamOperationHash(draft)
		return record
	}
	if _, err := normalizeSelfModelKind(record.SelfKind); err != nil {
		return reject("invalid_self_kind")
	}
	baseByID := selfDreamCurrentEntryByID(base)
	targets := make([]SelfModelEntry, 0, len(record.TargetEntryIDs))
	for _, targetID := range record.TargetEntryIDs {
		target, ok := baseByID[targetID]
		if !ok {
			return reject("target_not_in_frozen_self")
		}
		if target.Status != selfModelStatusActive {
			return reject("target_not_active")
		}
		targets = append(targets, target)
	}
	switch record.Operation {
	case SelfDreamOperationMerge:
		if len(targets) < 2 || record.Statement == "" {
			return reject("merge_requires_multiple_targets_and_statement")
		}
	case SelfDreamOperationSplit:
		if len(targets) != 1 || len(record.SplitStatements) < 2 {
			return reject("split_requires_one_target_and_multiple_statements")
		}
	case SelfDreamOperationContextualize:
		if len(targets) != 1 || record.Statement == "" ||
			record.Scope.Kind == "global" {
			return reject("contextualize_requires_one_target_and_contextual_scope")
		}
	case SelfDreamOperationContradict, SelfDreamOperationSupersede:
		if len(targets) == 0 || record.Statement == "" {
			return reject("operation_requires_target_and_statement")
		}
	}
	for _, target := range targets {
		if selfModelIsUserDirective(target) {
			return reject("user_directive_requires_direct_revision")
		}
		if target.Kind != record.SelfKind &&
			record.Operation != SelfDreamOperationContradict {
			return reject("incompatible_self_kinds")
		}
		if len(target.Contradictions) > 0 &&
			record.Operation != SelfDreamOperationContradict {
			return reject("target_has_unresolved_correction")
		}
	}
	included := selfDreamIncludedManifest(manifest)
	explicitRefs := 0
	repeatedRefs := make(map[string]struct{})
	for _, ref := range record.EvidenceRefs {
		item, ok := included[ref]
		if !ok {
			return reject("evidence_ref_not_in_frozen_manifest")
		}
		if item.EvidenceClass == TaskContextEvidenceExplicitUser {
			explicitRefs++
		}
		if item.EvidenceClass == TaskContextEvidenceExplicitUser ||
			item.EvidenceClass == TaskContextEvidenceEvaluatedProjection {
			repeatedRefs[item.SourceKind+"\x00"+item.SourceID] = struct{}{}
		}
	}
	if len(record.EvidenceRefs) == 0 {
		return reject("operation_requires_frozen_evidence")
	}
	switch record.ReasoningStatus {
	case "unknown":
	case "explicit":
		if explicitRefs == 0 {
			return reject("explicit_reasoning_requires_explicit_evidence")
		}
	case "repeated_source_backed":
		if len(repeatedRefs) < 2 {
			return reject("repeated_reasoning_requires_multiple_sources")
		}
	default:
		return reject("unsupported_reasoning_status")
	}
	if selfDreamMentionsAuthorityMutation(
		strings.Join(
			append([]string{record.Statement, record.Rationale}, record.SplitStatements...),
			" ",
		),
	) {
		return reject("authority_mutation_is_out_of_scope")
	}
	if record.Operation == SelfDreamOperationMerge ||
		record.Operation == SelfDreamOperationSupersede {
		sourceText := ""
		for _, target := range targets {
			sourceText += " " + target.Statement
		}
		if selfDreamWordCoverage(sourceText, record.Statement) < 0.65 {
			return reject("candidate_drops_material_source_terms")
		}
	}
	record.ValidationStatus = SelfDreamValidationReviewRequired
	record.ValidationReason = "qualified_consequential_change_requires_review"
	exact := source == "deterministic" &&
		record.Operation == SelfDreamOperationMerge
	if exact {
		canonical := selfDreamCanonicalStatement(record.Statement)
		for _, target := range targets {
			if selfModelHasUserEdits(target) || target.Kind != record.SelfKind ||
				strings.TrimSpace(target.Scope) != strings.TrimSpace(targets[0].Scope) ||
				selfDreamCanonicalStatement(target.Statement) != canonical ||
				len(target.Contradictions) > 0 {
				exact = false
				break
			}
		}
	}
	if exact {
		record.ValidationStatus = SelfDreamValidationQualified
		record.ValidationReason = "deterministic_exact_duplicate_with_complete_evidence"
		record.AutoEligible = true
	}
	record.OperationHash = selfDreamOperationHash(SelfDreamOperationDraft{
		Operation:       record.Operation,
		TargetEntryIDs:  record.TargetEntryIDs,
		SelfKind:        record.SelfKind,
		Scope:           record.Scope,
		Statement:       record.Statement,
		SplitStatements: record.SplitStatements,
		ReasoningStatus: record.ReasoningStatus,
		Rationale:       record.Rationale,
		Uncertainty:     record.Uncertainty,
		Confidence:      record.Confidence,
		EvidenceRefs:    record.EvidenceRefs,
	})
	return record
}

func selfDreamDuplicateCount(entries []SelfModelEntry) int {
	counts := make(map[string]int)
	for _, entry := range entries {
		if entry.Status != selfModelStatusActive {
			continue
		}
		key := strings.Join([]string{
			entry.Kind,
			strings.TrimSpace(entry.Scope),
			selfDreamCanonicalStatement(entry.Statement),
		}, "\x00")
		counts[key]++
	}
	duplicates := 0
	for _, count := range counts {
		if count > 1 {
			duplicates += count - 1
		}
	}
	return duplicates
}

func evaluateSelfDreamProjection(
	runID string,
	base []SelfModelEntry,
	operations []SelfDreamOperationRecord,
	realEvidence bool,
	now int64,
) SelfDreamEvaluationRecord {
	activeCount := 0
	for _, entry := range base {
		if entry.Status == selfModelStatusActive {
			activeCount++
		}
	}
	neutralDuplicates := selfDreamDuplicateCount(base)
	neutralRate := 0.0
	if activeCount > 0 {
		neutralRate = float64(neutralDuplicates) / float64(activeCount)
	}
	neutral := SelfDreamProjectionMetrics{
		EntryCount:          activeCount,
		DuplicateEntryCount: neutralDuplicates,
		DuplicateRate:       neutralRate,
		EvidenceCoverage:    1,
		Quality:             clampConfidence(1 - neutralRate*0.30),
	}
	dreamCount := activeCount
	dreamDuplicates := neutralDuplicates
	coverageLoss := 0.0
	contradictionRisk := 0.0
	unsupported := 0
	for _, operation := range operations {
		if operation.ValidationStatus == SelfDreamValidationRejected ||
			operation.ValidationStatus == SelfDreamValidationNoChange {
			if strings.Contains(
				operation.ValidationReason,
				"reasoning",
			) {
				unsupported++
			}
			continue
		}
		switch operation.Operation {
		case SelfDreamOperationMerge:
			dreamCount -= max(len(operation.TargetEntryIDs)-1, 0)
			if operation.AutoEligible {
				dreamDuplicates -= max(len(operation.TargetEntryIDs)-1, 0)
			}
		case SelfDreamOperationSplit:
			dreamCount += max(len(operation.SplitStatements)-1, 0)
		case SelfDreamOperationContextualize:
		case SelfDreamOperationContradict:
			contradictionRisk = maxFloat(contradictionRisk, 0.15)
		case SelfDreamOperationSupersede:
			dreamCount -= max(len(operation.TargetEntryIDs)-1, 0)
		}
		if strings.Contains(operation.ValidationReason, "drops_material") {
			coverageLoss = maxFloat(coverageLoss, 0.35)
		}
	}
	if dreamCount < 0 {
		dreamCount = 0
	}
	if dreamDuplicates < 0 {
		dreamDuplicates = 0
	}
	dreamRate := 0.0
	if dreamCount > 0 {
		dreamRate = float64(dreamDuplicates) / float64(dreamCount)
	}
	dream := SelfDreamProjectionMetrics{
		EntryCount:           dreamCount,
		DuplicateEntryCount:  dreamDuplicates,
		DuplicateRate:        dreamRate,
		EvidenceCoverage:     clampConfidence(1 - coverageLoss),
		InformationLoss:      coverageLoss,
		ContradictionRisk:    contradictionRisk,
		UnsupportedReasoning: unsupported,
	}
	dream.Quality = clampConfidence(
		1 -
			dream.DuplicateRate*0.30 -
			coverageLoss*0.50 -
			contradictionRisk*0.20 -
			float64(unsupported)*0.10,
	)
	return SelfDreamEvaluationRecord{
		ID:                   deterministicAuthorityID("self-dream-evaluation", runID),
		RunID:                runID,
		EvaluationVersion:    SelfDreamEvaluationVersion,
		Neutral:              neutral,
		Dream:                dream,
		Improvement:          dream.Quality - neutral.Quality,
		CoverageLoss:         coverageLoss,
		ContradictionRisk:    contradictionRisk,
		UnsupportedReasoning: unsupported,
		RealEvidence:         realEvidence,
		CreatedAt:            now,
	}
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func selfDreamNow(value int64) int64 {
	if value > 0 {
		return value
	}
	return time.Now().UnixMilli()
}

// selfDreamBoundedList trims, bounds and de-duplicates values, keeping at most
// limit of them.
func selfDreamBoundedList(values []string, limit, maxChars int) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		value = agencyBoundedText(value, maxChars)
		key := strings.ToLower(value)
		if value == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
		if len(out) == limit {
			break
		}
	}
	return out
}

// selfDreamModelID resolves the model a Self Dream run uses from the chat task
// model and the configured provider.
func selfDreamModelID(
	taskModel string,
	provider AIProviderConfig,
) string {
	taskModel = strings.TrimSpace(taskModel)
	switch strings.ToLower(taskModel) {
	case "sonnet", "opus":
		if (provider.isBYOA() || provider.isExternalAgent()) &&
			strings.TrimSpace(provider.Model) != "" {
			return strings.TrimSpace(provider.Model)
		}
	case "haiku":
		if provider.isBYOA() || provider.isExternalAgent() {
			if model := provider.lightModelOrChat(); model != "" {
				return model
			}
		}
	}
	return claudeModelForAnthropicAPI(taskModel)
}
