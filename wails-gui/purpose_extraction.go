package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/pii"
	"yansu-agent/agent"
)

const (
	PurposeExtractionInputVersionV1    = "purpose-extraction-input-v1"
	PurposeExtractionManifestVersionV1 = "purpose-extraction-manifest-v1"
	PurposeExtractionPromptVersionV1   = "purpose-extraction-prompt-v1"

	PurposeExtractionInputVersionV2    = "purpose-extraction-input-v2"
	PurposeExtractionManifestVersionV2 = "purpose-extraction-manifest-v2"
	PurposeExtractionPromptVersionV2   = "purpose-extraction-prompt-v2"

	PurposeExtractionInputVersion    = "purpose-extraction-input-v3"
	PurposeExtractionManifestVersion = "purpose-extraction-manifest-v3"
	PurposeExtractionPromptVersion   = "purpose-extraction-prompt-v3"
	PurposeExtractionOutputVersion   = "purpose-extraction-output-v1"

	PurposeExtractionStatusSucceeded = "succeeded"
	PurposeExtractionStatusFailed    = "failed"
	PurposeExtractionStatusNoSources = "no_sources"

	PurposeOperationSupport    = "support"
	PurposeOperationUpdate     = "update"
	PurposeOperationWait       = "wait"
	PurposeOperationResolve    = "resolve"
	PurposeOperationContradict = "contradict"
	PurposeOperationDismiss    = "dismiss"

	PurposeManifestIncluded = "included"
	PurposeManifestExcluded = "excluded"

	purposeExtractionMaxPromptCharacters  = 120_000
	purposeExtractionMaxPurposeCharacters = 96_000
	purposeExtractionMinFragment          = 256
	purposeExtractionMaxHypotheses        = 64
)

type PurposeExtractionRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	CutoffAt       int64  `json:"cutoffAt"`
	Query          string `json:"query,omitempty"`
}

type PurposeExtractionSource struct {
	Ref               string `json:"ref"`
	SourceKind        string `json:"sourceKind"`
	SourceID          string `json:"sourceId"`
	SourceVersion     string `json:"sourceVersion"`
	SourceObservedAt  int64  `json:"sourceObservedAt"`
	SourceHash        string `json:"sourceHash"`
	PromptContentHash string `json:"promptContentHash"`
	EvidenceClass     string `json:"evidenceClass"`
	PrivacyDecision   string `json:"privacyDecision"`
	IndependenceKey   string `json:"independenceKey"`
	Content           string `json:"content"`
	Truncated         bool   `json:"truncated,omitempty"`
}

type PurposeExtractionManifestEntry struct {
	Ref               string `json:"ref"`
	SourceKind        string `json:"sourceKind"`
	SourceID          string `json:"sourceId"`
	SourceObservedAt  int64  `json:"sourceObservedAt,omitempty"`
	EvidenceClass     string `json:"evidenceClass,omitempty"`
	PrivacyDecision   string `json:"privacyDecision,omitempty"`
	Decision          string `json:"decision"`
	Reason            string `json:"reason,omitempty"`
	SourceHash        string `json:"sourceHash,omitempty"`
	PromptContentHash string `json:"promptContentHash,omitempty"`
	IncludedChars     int    `json:"includedChars,omitempty"`
	Truncated         bool   `json:"truncated,omitempty"`
}

type PurposeExtractionManifest struct {
	Version                   string                           `json:"version"`
	CutoffAt                  int64                            `json:"cutoffAt"`
	BudgetChars               int                              `json:"budgetChars"`
	UsedChars                 int                              `json:"usedChars"`
	ScannedCount              int                              `json:"scannedCount"`
	IncludedCount             int                              `json:"includedCount"`
	ExcludedCount             int                              `json:"excludedCount"`
	Entries                   []PurposeExtractionManifestEntry `json:"entries"`
	CurrentPurposeBudgetChars int                              `json:"currentPurposeBudgetChars,omitempty"`
	CurrentPurposeUsedChars   int                              `json:"currentPurposeUsedChars,omitempty"`
	CurrentPurposeCount       int                              `json:"currentPurposeCount,omitempty"`
	CurrentPurposeHash        string                           `json:"currentPurposeHash,omitempty"`
}

type PurposeExtractionCurrentPurpose struct {
	Ref              string   `json:"ref"`
	OpenLoopID       string   `json:"openLoopId"`
	CurrentEventID   string   `json:"currentEventId"`
	ProjectionHash   string   `json:"projectionHash"`
	CanonicalKey     string   `json:"canonicalKey"`
	Kind             string   `json:"kind"`
	Title            string   `json:"title"`
	DesiredOutcome   string   `json:"desiredOutcome"`
	Owner            string   `json:"owner"`
	Counterparty     string   `json:"counterparty,omitempty"`
	Scope            string   `json:"scope,omitempty"`
	DueKind          string   `json:"dueKind"`
	DueValue         string   `json:"dueValue,omitempty"`
	WaitingCondition string   `json:"waitingCondition,omitempty"`
	ReasoningStatus  string   `json:"reasoningStatus"`
	LifecycleState   string   `json:"lifecycleState"`
	SupportState     string   `json:"supportState"`
	Eligible         bool     `json:"eligible"`
	DuplicateOfIDs   []string `json:"duplicateOfIds"`
}

type PurposeExtractionInput struct {
	Version        string                            `json:"version"`
	CutoffAt       int64                             `json:"cutoffAt"`
	Query          string                            `json:"query,omitempty"`
	Sources        []PurposeExtractionSource         `json:"sources"`
	CurrentPurpose []PurposeExtractionCurrentPurpose `json:"currentPurpose"`
	MailPrivacy    *PurposeExtractionMailPrivacy     `json:"mailPrivacy,omitempty"`
}

type PurposeExtractionHypothesis struct {
	LocalKey                string   `json:"localKey"`
	CanonicalKey            string   `json:"canonicalKey"`
	DuplicateOfCanonicalKey string   `json:"duplicateOfCanonicalKey,omitempty"`
	Kind                    string   `json:"kind"`
	Operation               string   `json:"operation"`
	Title                   string   `json:"title"`
	DesiredOutcome          string   `json:"desiredOutcome"`
	Owner                   string   `json:"owner"`
	Counterparty            string   `json:"counterparty,omitempty"`
	Scope                   string   `json:"scope,omitempty"`
	DueKind                 string   `json:"dueKind"`
	DueValue                string   `json:"dueValue,omitempty"`
	WaitingCondition        string   `json:"waitingCondition,omitempty"`
	ReasoningStatus         string   `json:"reasoningStatus"`
	Rationale               string   `json:"rationale,omitempty"`
	Confidence              float64  `json:"confidence"`
	EvidenceRefs            []string `json:"evidenceRefs"`
}

type PurposeExtractionOutput struct {
	Version    string                        `json:"version"`
	Hypotheses []PurposeExtractionHypothesis `json:"hypotheses"`
}

type PurposeExtractionExecution struct {
	Input       PurposeExtractionInput    `json:"input"`
	Manifest    PurposeExtractionManifest `json:"manifest"`
	Prompt      string                    `json:"prompt"`
	PromptHash  string                    `json:"promptHash"`
	RawOutput   string                    `json:"rawOutput"`
	Output      *PurposeExtractionOutput  `json:"output,omitempty"`
	StartedAt   int64                     `json:"startedAt"`
	CompletedAt int64                     `json:"completedAt"`
}

func normalizePurposeExtractionRequest(
	request PurposeExtractionRequest,
) (PurposeExtractionRequest, error) {
	var err error
	if request.IdempotencyKey, err = normalizePurposeID(
		request.IdempotencyKey,
		"extraction idempotency key",
	); err != nil {
		return PurposeExtractionRequest{}, err
	}
	if request.CutoffAt <= 0 {
		return PurposeExtractionRequest{}, errors.New("Purpose extraction cutoff is required")
	}
	if request.CutoffAt > time.Now().UnixMilli() {
		return PurposeExtractionRequest{}, errors.New("Purpose extraction cutoff cannot be in the future")
	}
	if request.Query, err = normalizePurposeText(
		request.Query,
		"extraction query",
		purposeMaxFieldBytes,
		false,
	); err != nil {
		return PurposeExtractionRequest{}, err
	}
	return request, nil
}

func purposeEvidenceClassForTaskCandidate(value string) (string, bool) {
	switch value {
	case TaskContextEvidenceExplicitUser:
		return PurposeEvidenceExplicitUser, true
	case TaskContextEvidenceObservedUser:
		return PurposeEvidenceObservedUser, true
	case TaskContextEvidenceEvaluatedProjection:
		return PurposeEvidenceEvaluatedProjection, true
	default:
		return "", false
	}
}

func purposeExtractionSourceKindAllowed(value string) bool {
	switch value {
	case taskContextSourceMemoryKind,
		taskContextSourceKnowledgeKind,
		taskContextSourceSelfKind:
		return true
	default:
		return false
	}
}

func purposeExtractionSourceVersion(candidate TaskContextCandidate) string {
	return agencyPayloadHash([]byte(strings.TrimSpace(candidate.Ref)))
}

func purposeExtractionIndependenceKey(candidate TaskContextCandidate) string {
	return agencyPayloadHash([]byte(strings.Join([]string{
		strings.TrimSpace(candidate.SourceKind),
		strings.TrimSpace(candidate.SourceID),
	}, "\x00")))
}

func purposeManifestEntryForExclusion(
	exclusion TaskContextSourceExclusion,
) PurposeExtractionManifestEntry {
	return PurposeExtractionManifestEntry{
		Ref: exclusion.Ref, SourceKind: exclusion.SourceKind,
		SourceID: exclusion.SourceID, SourceObservedAt: exclusion.ObservedAt,
		Decision: PurposeManifestExcluded, Reason: exclusion.Reason,
	}
}

func assemblePurposeExtractionInput(
	request PurposeExtractionRequest,
	sourceResult TaskContextSourceResult,
) (PurposeExtractionInput, PurposeExtractionManifest, error) {
	request, err := normalizePurposeExtractionRequest(request)
	if err != nil {
		return PurposeExtractionInput{}, PurposeExtractionManifest{}, err
	}
	input := PurposeExtractionInput{
		Version:        PurposeExtractionInputVersion,
		CutoffAt:       request.CutoffAt,
		Query:          request.Query,
		Sources:        []PurposeExtractionSource{},
		CurrentPurpose: []PurposeExtractionCurrentPurpose{},
		MailPrivacy:    newPurposeExtractionMailPrivacy(nil),
	}
	currentPurposeJSON, currentPurposeHash, err := agencyStructPayload(
		input.CurrentPurpose,
	)
	if err != nil {
		return PurposeExtractionInput{}, PurposeExtractionManifest{}, err
	}
	manifest := PurposeExtractionManifest{
		Version:                   PurposeExtractionManifestVersion,
		CutoffAt:                  request.CutoffAt,
		BudgetChars:               purposeExtractionMaxPromptCharacters,
		CurrentPurposeBudgetChars: purposeExtractionMaxPurposeCharacters,
		CurrentPurposeUsedChars:   len(currentPurposeJSON),
		CurrentPurposeHash:        currentPurposeHash,
		Entries: make([]PurposeExtractionManifestEntry, 0,
			len(sourceResult.Candidates)+len(sourceResult.Exclusions)),
	}
	for _, exclusion := range sourceResult.Exclusions {
		manifest.Entries = append(
			manifest.Entries,
			purposeManifestEntryForExclusion(exclusion),
		)
	}
	for _, candidate := range sourceResult.Candidates {
		manifest.ScannedCount++
		entry := PurposeExtractionManifestEntry{
			Ref: candidate.Ref, SourceKind: candidate.SourceKind,
			SourceID: candidate.SourceID, SourceObservedAt: candidate.ObservedAt,
			EvidenceClass:   candidate.EvidenceClass,
			PrivacyDecision: candidate.PrivacyDecision,
			Decision:        PurposeManifestExcluded,
		}
		if !purposeExtractionSourceKindAllowed(candidate.SourceKind) {
			entry.Reason = "source_not_eligible_for_purpose"
			manifest.Entries = append(manifest.Entries, entry)
			continue
		}
		evidenceClass, allowed := purposeEvidenceClassForTaskCandidate(
			candidate.EvidenceClass,
		)
		if !allowed {
			entry.Reason = "evidence_class_not_eligible_for_purpose"
			manifest.Entries = append(manifest.Entries, entry)
			continue
		}
		if candidate.PrivacyDecision == TaskContextPrivacyExclude {
			entry.Reason = TaskContextSourceReasonPrivacy
			manifest.Entries = append(manifest.Entries, entry)
			continue
		}
		content := strings.TrimSpace(candidate.Content)
		sourceHash := agencyPayloadHash([]byte(content))
		if candidate.PrivacyDecision == TaskContextPrivacyRedact {
			content, _ = pii.RedactText(content)
			content = strings.TrimSpace(content)
		}
		if content == "" {
			entry.Reason = "empty_after_privacy"
			manifest.Entries = append(manifest.Entries, entry)
			continue
		}
		remaining := purposeExtractionMaxPromptCharacters - manifest.UsedChars
		if remaining < purposeExtractionMinFragment {
			entry.Reason = "purpose_prompt_budget_exhausted"
			manifest.Entries = append(manifest.Entries, entry)
			continue
		}
		included := content
		truncated := false
		if len(included) > remaining {
			included = truncateAtRuneBoundary(included, remaining)
			included = strings.TrimSpace(included)
			truncated = true
		}
		if len(included) < purposeExtractionMinFragment && len(content) >= purposeExtractionMinFragment {
			entry.Reason = "insufficient_purpose_fragment_budget"
			manifest.Entries = append(manifest.Entries, entry)
			continue
		}
		source := PurposeExtractionSource{
			Ref: candidate.Ref, SourceKind: candidate.SourceKind,
			SourceID:          candidate.SourceID,
			SourceVersion:     purposeExtractionSourceVersion(candidate),
			SourceObservedAt:  candidate.ObservedAt,
			SourceHash:        sourceHash,
			PromptContentHash: agencyPayloadHash([]byte(included)),
			EvidenceClass:     evidenceClass,
			PrivacyDecision:   candidate.PrivacyDecision,
			IndependenceKey:   purposeExtractionIndependenceKey(candidate),
			Content:           included, Truncated: truncated,
		}
		input.Sources = append(input.Sources, source)
		entry.Decision = PurposeManifestIncluded
		entry.SourceHash = source.SourceHash
		entry.PromptContentHash = source.PromptContentHash
		entry.IncludedChars = len(included)
		entry.Truncated = truncated
		manifest.UsedChars += len(included)
		manifest.IncludedCount++
		manifest.Entries = append(manifest.Entries, entry)
	}
	manifest.ScannedCount += len(sourceResult.Exclusions)
	manifest.ExcludedCount = len(manifest.Entries) - manifest.IncludedCount
	sort.SliceStable(manifest.Entries, func(i, j int) bool {
		left := manifest.Entries[i]
		right := manifest.Entries[j]
		if left.SourceKind != right.SourceKind {
			return left.SourceKind < right.SourceKind
		}
		if left.SourceID != right.SourceID {
			return left.SourceID < right.SourceID
		}
		if left.SourceObservedAt != right.SourceObservedAt {
			return left.SourceObservedAt < right.SourceObservedAt
		}
		return left.Ref < right.Ref
	})
	return input, manifest, nil
}

func gatherPurposeExtractionInput(
	store *ActivityStore,
	request PurposeExtractionRequest,
) (PurposeExtractionInput, PurposeExtractionManifest, error) {
	if store == nil || store.db == nil {
		return PurposeExtractionInput{}, PurposeExtractionManifest{}, errors.New(
			"Purpose extraction requires activity storage",
		)
	}
	sources, err := GatherTaskContextSources(TaskContextSourceRequest{
		Store: store, CutoffAt: request.CutoffAt, Query: request.Query,
	})
	if err != nil {
		return PurposeExtractionInput{}, PurposeExtractionManifest{}, err
	}
	input, manifest, err := assemblePurposeExtractionInput(request, sources)
	if err != nil {
		return PurposeExtractionInput{}, PurposeExtractionManifest{}, err
	}
	return attachPurposeExtractionCurrentPurpose(
		NewPurposeMapStore(store.db),
		input,
		manifest,
	)
}

func validatePurposeExtractionInputManifest(
	input PurposeExtractionInput,
	manifest PurposeExtractionManifest,
) error {
	if input.CutoffAt <= 0 || manifest.CutoffAt != input.CutoffAt ||
		manifest.BudgetChars != purposeExtractionMaxPromptCharacters ||
		manifest.ScannedCount != len(manifest.Entries) ||
		manifest.IncludedCount != len(input.Sources) ||
		manifest.ExcludedCount != len(manifest.Entries)-manifest.IncludedCount {
		return errors.New("Purpose extraction input manifest summary is inconsistent")
	}
	switch input.Version {
	case PurposeExtractionInputVersionV1:
		if manifest.Version != PurposeExtractionManifestVersionV1 ||
			len(input.CurrentPurpose) != 0 ||
			manifest.CurrentPurposeBudgetChars != 0 ||
			manifest.CurrentPurposeUsedChars != 0 ||
			manifest.CurrentPurposeCount != 0 ||
			manifest.CurrentPurposeHash != "" || input.MailPrivacy != nil {
			return errors.New("Purpose extraction v1 current-Purpose projection is invalid")
		}
	case PurposeExtractionInputVersionV2, PurposeExtractionInputVersion:
		expectedManifest := PurposeExtractionManifestVersionV2
		if input.Version == PurposeExtractionInputVersion {
			expectedManifest = PurposeExtractionManifestVersion
			if err := validatePurposeExtractionMailPrivacy(input.MailPrivacy); err != nil {
				return err
			}
		} else if input.MailPrivacy != nil {
			return errors.New("historical Purpose extraction cannot contain mail privacy context")
		}
		if manifest.Version != expectedManifest {
			return errors.New("Purpose extraction manifest version is invalid")
		}
		if err := validatePurposeExtractionCurrentPurposeProjection(
			input.CurrentPurpose,
			manifest,
		); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported Purpose extraction input version %q", input.Version)
	}
	if _, err := normalizePurposeText(
		input.Query,
		"extraction query",
		purposeMaxFieldBytes,
		false,
	); err != nil {
		return err
	}

	entriesByRef := make(map[string]PurposeExtractionManifestEntry, len(manifest.Entries))
	included := 0
	usedChars := 0
	for _, entry := range manifest.Entries {
		if strings.TrimSpace(entry.Ref) == "" || strings.TrimSpace(entry.SourceKind) == "" ||
			strings.TrimSpace(entry.SourceID) == "" {
			return errors.New("Purpose extraction manifest contains an incomplete source identity")
		}
		if _, exists := entriesByRef[entry.Ref]; exists {
			return fmt.Errorf("Purpose extraction manifest contains duplicate ref %q", entry.Ref)
		}
		entriesByRef[entry.Ref] = entry
		switch entry.Decision {
		case PurposeManifestIncluded:
			if entry.Reason != "" || entry.SourceObservedAt <= 0 ||
				entry.SourceObservedAt > input.CutoffAt || entry.IncludedChars <= 0 {
				return fmt.Errorf("Purpose extraction manifest inclusion %q is invalid", entry.Ref)
			}
			included++
			usedChars += entry.IncludedChars
		case PurposeManifestExcluded:
			if strings.TrimSpace(entry.Reason) == "" {
				return fmt.Errorf("Purpose extraction manifest exclusion %q has no reason", entry.Ref)
			}
		default:
			return fmt.Errorf("Purpose extraction manifest decision %q is invalid", entry.Decision)
		}
	}
	if included != manifest.IncludedCount || usedChars != manifest.UsedChars ||
		manifest.UsedChars < 0 || manifest.UsedChars > manifest.BudgetChars {
		return errors.New("Purpose extraction manifest byte accounting is inconsistent")
	}

	seenSources := make(map[string]struct{}, len(input.Sources))
	for _, source := range input.Sources {
		if _, exists := seenSources[source.Ref]; exists {
			return fmt.Errorf("Purpose extraction input contains duplicate source ref %q", source.Ref)
		}
		seenSources[source.Ref] = struct{}{}
		if !purposeExtractionSourceKindAllowed(source.SourceKind) ||
			source.SourceObservedAt <= 0 || source.SourceObservedAt > input.CutoffAt ||
			strings.TrimSpace(source.SourceID) == "" || strings.TrimSpace(source.Content) == "" {
			return fmt.Errorf("Purpose extraction source %q is invalid", source.Ref)
		}
		switch source.EvidenceClass {
		case PurposeEvidenceExplicitUser, PurposeEvidenceObservedUser,
			PurposeEvidenceEvaluatedProjection:
		default:
			return fmt.Errorf("Purpose extraction source %q evidence class is invalid", source.Ref)
		}
		if source.PrivacyDecision != TaskContextPrivacyInclude &&
			source.PrivacyDecision != TaskContextPrivacyRedact {
			return fmt.Errorf("Purpose extraction source %q privacy decision is invalid", source.Ref)
		}
		if _, err := normalizePurposeHash(source.SourceHash, "source hash", true); err != nil {
			return err
		}
		if source.SourceVersion != agencyPayloadHash([]byte(strings.TrimSpace(source.Ref))) ||
			source.PromptContentHash != agencyPayloadHash([]byte(source.Content)) ||
			source.IndependenceKey != agencyPayloadHash([]byte(strings.Join([]string{
				strings.TrimSpace(source.SourceKind),
				strings.TrimSpace(source.SourceID),
			}, "\x00"))) {
			return fmt.Errorf("Purpose extraction source %q hashes are inconsistent", source.Ref)
		}
		entry, ok := entriesByRef[source.Ref]
		if !ok || entry.Decision != PurposeManifestIncluded ||
			entry.SourceKind != source.SourceKind || entry.SourceID != source.SourceID ||
			entry.SourceObservedAt != source.SourceObservedAt ||
			entry.EvidenceClass != source.EvidenceClass ||
			entry.PrivacyDecision != source.PrivacyDecision ||
			entry.SourceHash != source.SourceHash ||
			entry.PromptContentHash != source.PromptContentHash ||
			entry.IncludedChars != len(source.Content) ||
			entry.Truncated != source.Truncated {
			return fmt.Errorf("Purpose extraction source %q does not match its manifest", source.Ref)
		}
	}
	return nil
}

func buildPurposeExtractionPromptV1(input PurposeExtractionInput) (string, error) {
	legacyInput := struct {
		Version  string                    `json:"version"`
		CutoffAt int64                     `json:"cutoffAt"`
		Query    string                    `json:"query,omitempty"`
		Sources  []PurposeExtractionSource `json:"sources"`
	}{
		Version: input.Version, CutoffAt: input.CutoffAt,
		Query: input.Query, Sources: input.Sources,
	}
	inputJSON, err := json.Marshal(legacyInput)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"You are Isoform's Purpose Map hypothesis extractor.",
		"Identify current commitments, responsibilities, goals, concerns, or opportunities supported by the supplied sources.",
		"Your output is a hypothesis only. It grants no authority, requests no work, and does not prove completion or hidden reasoning.",
		"Use only evidenceRefs from the supplied sources. Never cite model output, infer a source, or invent an owner, recipient, due signal, rationale, or result.",
		"If reasoning is not explicit in a cited explicit-user source, set reasoningStatus to unknown and rationale to an empty string.",
		"Use operation support for new/repeated evidence, update for a changed description, wait for a source-backed waiting condition, resolve only for explicit completion evidence, contradict for conflicting evidence, and dismiss only for explicit user dismissal.",
		"Use duplicateOfCanonicalKey only when the sources clearly describe the same desired outcome as that canonical key; the backend will independently verify it.",
		"Return strict JSON with exactly this shape:",
		`{"version":"purpose-extraction-output-v1","hypotheses":[{"localKey":"stable-key","canonicalKey":"stable semantic identity","duplicateOfCanonicalKey":"","kind":"commitment|responsibility|goal|concern|opportunity","operation":"support|update|wait|resolve|contradict|dismiss","title":"short title","desiredOutcome":"observable desired outcome","owner":"user|other|shared|unknown","counterparty":"","scope":"","dueKind":"none|date|window|condition|unknown","dueValue":"","waitingCondition":"","reasoningStatus":"explicit|unknown","rationale":"","confidence":0.0,"evidenceRefs":["exact-ref"]}]}`,
		"Return no Markdown and no text outside the JSON object.",
		"Frozen source input:",
		string(inputJSON),
	}, "\n\n"), nil
}

func buildPurposeExtractionPromptV2(input PurposeExtractionInput) (string, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"You are Isoform's Purpose Map hypothesis extractor.",
		"Identify current commitments, responsibilities, goals, concerns, or opportunities supported by the supplied evidence sources.",
		"The currentPurpose section is frozen comparison context, not evidence. Use it to preserve canonical identity, distinguish new support from updates, and avoid duplicate roots. Never cite a currentPurpose ref as evidence.",
		"Your output is a hypothesis only. It grants no authority, requests no work, and does not prove completion or hidden reasoning.",
		"Use evidenceRefs only from the sources section. Never cite model output, currentPurpose, infer a source, or invent an owner, recipient, due signal, rationale, or result.",
		"When evidence concerns an existing root, reuse its exact canonicalKey. Use duplicateOfCanonicalKey only when evidence clearly describes the same desired outcome under a different canonical identity; the backend independently verifies overlap.",
		"If reasoning is not explicit in a cited explicit-user source, set reasoningStatus to unknown and rationale to an empty string.",
		"Use operation support for new or repeated evidence, update for a changed description, wait for a source-backed waiting condition, resolve only for explicit completion evidence, contradict for conflicting evidence, and dismiss only for explicit user dismissal.",
		"Return strict JSON with exactly this shape:",
		`{"version":"purpose-extraction-output-v1","hypotheses":[{"localKey":"stable-key","canonicalKey":"stable semantic identity","duplicateOfCanonicalKey":"","kind":"commitment|responsibility|goal|concern|opportunity","operation":"support|update|wait|resolve|contradict|dismiss","title":"short title","desiredOutcome":"observable desired outcome","owner":"user|other|shared|unknown","counterparty":"","scope":"","dueKind":"none|date|window|condition|unknown","dueValue":"","waitingCondition":"","reasoningStatus":"explicit|unknown","rationale":"","confidence":0.0,"evidenceRefs":["exact-source-ref"]}]}`,
		"Return no Markdown and no text outside the JSON object.",
		"Frozen extraction input:",
		string(inputJSON),
	}, "\n\n"), nil
}

func buildPurposeExtractionPrompt(input PurposeExtractionInput) (string, error) {
	switch input.Version {
	case PurposeExtractionInputVersionV1:
		return buildPurposeExtractionPromptV1(input)
	case PurposeExtractionInputVersionV2:
		return buildPurposeExtractionPromptV2(input)
	case PurposeExtractionInputVersion:
		return buildPurposeExtractionPromptV3(input)
	default:
		return "", fmt.Errorf(
			"unsupported Purpose extraction prompt input version %q",
			input.Version,
		)
	}
}

func normalizePurposeOperation(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case PurposeOperationSupport, PurposeOperationUpdate, PurposeOperationWait,
		PurposeOperationResolve, PurposeOperationContradict, PurposeOperationDismiss:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported Purpose extraction operation %q", value)
	}
}

func normalizePurposeExtractionHypothesis(
	hypothesis PurposeExtractionHypothesis,
	sources map[string]PurposeExtractionSource,
) (PurposeExtractionHypothesis, error) {
	var err error
	if hypothesis.LocalKey, err = normalizePurposeID(hypothesis.LocalKey, "hypothesis local key"); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.CanonicalKey, err = normalizePurposeCanonicalKey(hypothesis.CanonicalKey); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if strings.TrimSpace(hypothesis.DuplicateOfCanonicalKey) != "" {
		if hypothesis.DuplicateOfCanonicalKey, err = normalizePurposeCanonicalKey(
			hypothesis.DuplicateOfCanonicalKey,
		); err != nil {
			return PurposeExtractionHypothesis{}, err
		}
		if hypothesis.DuplicateOfCanonicalKey == hypothesis.CanonicalKey {
			return PurposeExtractionHypothesis{}, errors.New("Purpose hypothesis cannot duplicate its own canonical key")
		}
	}
	if hypothesis.Kind, err = normalizePurposeKind(hypothesis.Kind); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.Operation, err = normalizePurposeOperation(hypothesis.Operation); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.Title, err = normalizePurposeText(hypothesis.Title, "hypothesis title", purposeMaxTitleBytes, true); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.DesiredOutcome, err = normalizePurposeText(hypothesis.DesiredOutcome, "hypothesis desired outcome", purposeMaxOutcomeBytes, true); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.Owner, err = normalizePurposeOwner(hypothesis.Owner); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.Counterparty, err = normalizePurposeText(hypothesis.Counterparty, "hypothesis counterparty", purposeMaxFieldBytes, false); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.Scope, err = normalizePurposeText(hypothesis.Scope, "hypothesis scope", purposeMaxScopeBytes, false); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.DueKind, hypothesis.DueValue, err = normalizePurposeDue(hypothesis.DueKind, hypothesis.DueValue); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.WaitingCondition, err = normalizePurposeText(hypothesis.WaitingCondition, "hypothesis waiting condition", purposeMaxFieldBytes, false); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.Operation == PurposeOperationWait {
		if hypothesis.WaitingCondition == "" {
			return PurposeExtractionHypothesis{}, errors.New("Purpose wait hypothesis requires a condition")
		}
	} else if hypothesis.WaitingCondition != "" {
		return PurposeExtractionHypothesis{}, errors.New("non-wait Purpose hypothesis cannot contain a waiting condition")
	}
	if hypothesis.ReasoningStatus, hypothesis.Rationale, err = normalizePurposeReasoning(
		hypothesis.ReasoningStatus,
		hypothesis.Rationale,
	); err != nil {
		return PurposeExtractionHypothesis{}, err
	}
	if hypothesis.Confidence < 0 || hypothesis.Confidence > 1 {
		return PurposeExtractionHypothesis{}, errors.New("Purpose hypothesis confidence must be between zero and one")
	}
	if len(hypothesis.EvidenceRefs) == 0 {
		return PurposeExtractionHypothesis{}, errors.New("Purpose hypothesis requires evidence refs")
	}
	hypothesis.EvidenceRefs = uniqueSortedStrings(hypothesis.EvidenceRefs)
	if len(hypothesis.EvidenceRefs) == 0 {
		return PurposeExtractionHypothesis{}, errors.New("Purpose hypothesis evidence refs are empty")
	}
	hasExplicit := false
	for _, ref := range hypothesis.EvidenceRefs {
		source, ok := sources[ref]
		if !ok {
			return PurposeExtractionHypothesis{}, fmt.Errorf("Purpose hypothesis references unavailable evidence %q", ref)
		}
		if source.EvidenceClass == PurposeEvidenceExplicitUser ||
			source.EvidenceClass == PurposeEvidenceCorrection {
			hasExplicit = true
		}
	}
	if hypothesis.ReasoningStatus == PurposeReasoningExplicit && !hasExplicit {
		return PurposeExtractionHypothesis{}, errors.New("Purpose hypothesis explicit reasoning has no explicit source")
	}
	return hypothesis, nil
}

func decodePurposeExtractionOutput(
	raw string,
	input PurposeExtractionInput,
) (PurposeExtractionOutput, error) {
	raw = stripCodeFences(strings.TrimSpace(raw))
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var output PurposeExtractionOutput
	if err := decoder.Decode(&output); err != nil {
		return PurposeExtractionOutput{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return PurposeExtractionOutput{}, errors.New("Purpose extraction output contains multiple JSON values")
		}
		return PurposeExtractionOutput{}, err
	}
	if output.Version != PurposeExtractionOutputVersion {
		return PurposeExtractionOutput{}, fmt.Errorf("unsupported Purpose extraction output version %q", output.Version)
	}
	if len(output.Hypotheses) > purposeExtractionMaxHypotheses {
		return PurposeExtractionOutput{}, errors.New("Purpose extraction returned too many hypotheses")
	}
	sources := make(map[string]PurposeExtractionSource, len(input.Sources))
	for _, source := range input.Sources {
		sources[source.Ref] = source
	}
	seen := make(map[string]struct{}, len(output.Hypotheses))
	for index, hypothesis := range output.Hypotheses {
		normalized, err := normalizePurposeExtractionHypothesis(hypothesis, sources)
		if err != nil {
			return PurposeExtractionOutput{}, fmt.Errorf("Purpose hypothesis %d: %w", index, err)
		}
		if _, ok := seen[normalized.LocalKey]; ok {
			return PurposeExtractionOutput{}, fmt.Errorf("duplicate Purpose hypothesis local key %q", normalized.LocalKey)
		}
		seen[normalized.LocalKey] = struct{}{}
		output.Hypotheses[index] = normalized
	}
	sort.Slice(output.Hypotheses, func(i, j int) bool {
		return output.Hypotheses[i].LocalKey < output.Hypotheses[j].LocalKey
	})
	return output, nil
}

func runPurposeExtractionModel(
	ctx context.Context,
	runner agent.AgentRunner,
	model string,
	input PurposeExtractionInput,
	manifest PurposeExtractionManifest,
) (PurposeExtractionExecution, error) {
	result := PurposeExtractionExecution{
		Input: input, Manifest: manifest, StartedAt: time.Now().UnixMilli(),
	}
	prompt, err := buildPurposeExtractionPrompt(input)
	if err != nil {
		result.CompletedAt = time.Now().UnixMilli()
		return result, err
	}
	result.Prompt = prompt
	result.PromptHash = agencyPayloadHash([]byte(prompt))
	model = strings.TrimSpace(model)
	if len(input.Sources) == 0 {
		result.CompletedAt = time.Now().UnixMilli()
		return result, nil
	}
	if runner == nil || model == "" {
		result.CompletedAt = time.Now().UnixMilli()
		return result, errors.New("Purpose extraction model is unavailable")
	}
	var raw strings.Builder
	ctx, given := withPassScope(ctx, passPurpose)
	defer finishPassScope(given)
	_, err = runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		BackendHint:       agent.BackendHintLearning,
		AutoApproveTools:  true,
		Prompt:            prompt,
		Model:             model,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch event.Type {
		case "assistant":
			raw.WriteString(event.Content)
		case "error":
			return errors.New(event.Error)
		}
		return nil
	})
	result.RawOutput = strings.TrimSpace(raw.String())
	result.CompletedAt = time.Now().UnixMilli()
	if err != nil {
		return result, fmt.Errorf("execute Purpose extraction: %w", err)
	}
	output, err := decodePurposeExtractionOutput(result.RawOutput, input)
	if err != nil {
		return result, fmt.Errorf("parse Purpose extraction: %w", err)
	}
	result.Output = &output
	return result, nil
}
