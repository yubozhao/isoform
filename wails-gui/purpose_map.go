package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	PurposeOpenLoopContractVersion = "purpose-open-loop-v1"
	PurposeRelationContractVersion = "purpose-open-loop-relation-v1"

	PurposeKindCommitment     = "commitment"
	PurposeKindResponsibility = "responsibility"
	PurposeKindGoal           = "goal"
	PurposeKindConcern        = "concern"
	PurposeKindOpportunity    = "opportunity"

	PurposeEventDiscovered   = "discovered"
	PurposeEventReinforced   = "reinforced"
	PurposeEventRevised      = "revised"
	PurposeEventWaiting      = "waiting"
	PurposeEventResolved     = "resolved"
	PurposeEventReopened     = "reopened"
	PurposeEventContradicted = "contradicted"
	PurposeEventDismissed    = "dismissed"
	PurposeEventSuperseded   = "superseded"

	PurposeStateCandidate    = "candidate"
	PurposeStateActive       = "active"
	PurposeStateWaiting      = "waiting"
	PurposeStateResolved     = "resolved"
	PurposeStateContradicted = "contradicted"
	PurposeStateDismissed    = "dismissed"
	PurposeStateSuperseded   = "superseded"

	PurposeSupportHypothesis   = "hypothesis"
	PurposeSupportSupported    = "supported"
	PurposeSupportContradicted = "contradicted"
	PurposeSupportUnknown      = "unknown"

	PurposeEvidenceExplicitUser        = "explicit_user"
	PurposeEvidenceObservedUser        = "observed_user"
	PurposeEvidenceEvaluatedProjection = "evaluated_projection"
	PurposeEvidenceVerifiedExternal    = "verified_external"
	PurposeEvidenceVerifiedOutcome     = "verified_outcome"
	PurposeEvidenceCorrection          = "correction"

	PurposeStanceSupport    = "support"
	PurposeStanceUpdate     = "update"
	PurposeStanceWait       = "wait"
	PurposeStanceResolve    = "resolve"
	PurposeStanceContradict = "contradict"
	PurposeStanceDismiss    = "dismiss"

	PurposeOwnerUser    = "user"
	PurposeOwnerOther   = "other"
	PurposeOwnerShared  = "shared"
	PurposeOwnerUnknown = "unknown"

	PurposeDueNone      = "none"
	PurposeDueDate      = "date"
	PurposeDueWindow    = "window"
	PurposeDueCondition = "condition"
	PurposeDueUnknown   = "unknown"

	PurposeReasoningExplicit = "explicit"
	PurposeReasoningUnknown  = "unknown"

	PurposeRelationDuplicateOf    = "duplicate_of"
	PurposeRelationSupersedes     = "supersedes"
	PurposeRelationDependsOn      = "depends_on"
	PurposeRelationBlocks         = "blocks"
	PurposeRelationRelatedTo      = "related_to"
	PurposeRelationEventAsserted  = "asserted"
	PurposeRelationEventRetracted = "retracted"

	purposeMaxIdentityBytes = 512
	purposeMaxKeyBytes      = 1_024
	purposeMaxTitleBytes    = 4_000
	purposeMaxOutcomeBytes  = 12_000
	purposeMaxScopeBytes    = 8_000
	purposeMaxFieldBytes    = 4_000
	purposeMaxReasonBytes   = 8_000
)

type PurposeEvidenceInput struct {
	SourceKind       string `json:"sourceKind"`
	SourceID         string `json:"sourceId"`
	SourceVersion    string `json:"sourceVersion"`
	SourceObservedAt int64  `json:"sourceObservedAt"`
	SourceHash       string `json:"sourceHash"`
	EvidenceClass    string `json:"evidenceClass"`
	Stance           string `json:"stance"`
	IndependenceKey  string `json:"independenceKey"`
	ExcerptHash      string `json:"excerptHash,omitempty"`
	PrivacyDecision  string `json:"privacyDecision"`
}

type CreatePurposeOpenLoopInput struct {
	IdempotencyKey  string                 `json:"idempotencyKey"`
	CanonicalKey    string                 `json:"canonicalKey"`
	Kind            string                 `json:"kind"`
	Title           string                 `json:"title"`
	DesiredOutcome  string                 `json:"desiredOutcome"`
	Owner           string                 `json:"owner"`
	Counterparty    string                 `json:"counterparty,omitempty"`
	Scope           string                 `json:"scope,omitempty"`
	DueKind         string                 `json:"dueKind"`
	DueValue        string                 `json:"dueValue,omitempty"`
	Rationale       string                 `json:"rationale,omitempty"`
	ReasoningStatus string                 `json:"reasoningStatus"`
	Confidence      float64                `json:"confidence"`
	OccurredAt      int64                  `json:"occurredAt"`
	Evidence        []PurposeEvidenceInput `json:"evidence"`
}

type AppendPurposeOpenLoopEventInput struct {
	OpenLoopID       string                 `json:"openLoopId"`
	ExpectedEventID  string                 `json:"expectedEventId"`
	IdempotencyKey   string                 `json:"idempotencyKey"`
	EventType        string                 `json:"eventType"`
	Title            string                 `json:"title"`
	DesiredOutcome   string                 `json:"desiredOutcome"`
	Owner            string                 `json:"owner"`
	Counterparty     string                 `json:"counterparty,omitempty"`
	Scope            string                 `json:"scope,omitempty"`
	DueKind          string                 `json:"dueKind"`
	DueValue         string                 `json:"dueValue,omitempty"`
	WaitingCondition string                 `json:"waitingCondition,omitempty"`
	Rationale        string                 `json:"rationale,omitempty"`
	ReasoningStatus  string                 `json:"reasoningStatus"`
	Confidence       float64                `json:"confidence"`
	OccurredAt       int64                  `json:"occurredAt"`
	Evidence         []PurposeEvidenceInput `json:"evidence"`
}

type CreatePurposeRelationInput struct {
	IdempotencyKey  string `json:"idempotencyKey"`
	SubjectID       string `json:"subjectId"`
	ObjectID        string `json:"objectId"`
	RelationType    string `json:"relationType"`
	EvidenceEventID string `json:"evidenceEventId"`
	OccurredAt      int64  `json:"occurredAt"`
}

type RetractPurposeRelationInput struct {
	RelationID      string `json:"relationId"`
	ExpectedEventID string `json:"expectedEventId"`
	IdempotencyKey  string `json:"idempotencyKey"`
	EvidenceEventID string `json:"evidenceEventId"`
	Reason          string `json:"reason"`
	OccurredAt      int64  `json:"occurredAt"`
}

type PurposeOpenLoopRecord struct {
	ID              string `json:"id"`
	IdempotencyKey  string `json:"idempotencyKey"`
	CanonicalKey    string `json:"canonicalKey"`
	Kind            string `json:"kind"`
	ContractVersion string `json:"contractVersion"`
	CreatedAt       int64  `json:"createdAt"`
	PayloadJSON     string `json:"payloadJson"`
	PayloadHash     string `json:"payloadHash"`
}

type PurposeOpenLoopEventRecord struct {
	ID                 string   `json:"id"`
	OpenLoopID         string   `json:"openLoopId"`
	IdempotencyKey     string   `json:"idempotencyKey"`
	Sequence           int      `json:"sequence"`
	EventType          string   `json:"eventType"`
	ExpectedEventID    string   `json:"expectedEventId,omitempty"`
	PredecessorEventID string   `json:"predecessorEventId,omitempty"`
	Title              string   `json:"title"`
	DesiredOutcome     string   `json:"desiredOutcome"`
	Owner              string   `json:"owner"`
	Counterparty       string   `json:"counterparty,omitempty"`
	Scope              string   `json:"scope,omitempty"`
	DueKind            string   `json:"dueKind"`
	DueValue           string   `json:"dueValue,omitempty"`
	WaitingCondition   string   `json:"waitingCondition,omitempty"`
	LifecycleState     string   `json:"lifecycleState"`
	SupportState       string   `json:"supportState"`
	Confidence         float64  `json:"confidence"`
	Rationale          string   `json:"rationale,omitempty"`
	ReasoningStatus    string   `json:"reasoningStatus"`
	EvidenceSetHash    string   `json:"evidenceSetHash"`
	EvidenceIDs        []string `json:"evidenceIds"`
	OccurredAt         int64    `json:"occurredAt"`
	CreatedAt          int64    `json:"createdAt"`
	PayloadJSON        string   `json:"payloadJson"`
	PayloadHash        string   `json:"payloadHash"`
}

type PurposeEvidenceRecord struct {
	ID               string `json:"id"`
	EventID          string `json:"eventId"`
	SourceKind       string `json:"sourceKind"`
	SourceID         string `json:"sourceId"`
	SourceVersion    string `json:"sourceVersion"`
	SourceObservedAt int64  `json:"sourceObservedAt"`
	SourceHash       string `json:"sourceHash"`
	EvidenceClass    string `json:"evidenceClass"`
	Stance           string `json:"stance"`
	IndependenceKey  string `json:"independenceKey"`
	ExcerptHash      string `json:"excerptHash,omitempty"`
	PrivacyDecision  string `json:"privacyDecision"`
	CreatedAt        int64  `json:"createdAt"`
	PayloadJSON      string `json:"payloadJson"`
	PayloadHash      string `json:"payloadHash"`
}

type PurposeRelationRecord struct {
	ID              string `json:"id"`
	IdempotencyKey  string `json:"idempotencyKey"`
	SubjectID       string `json:"subjectId"`
	ObjectID        string `json:"objectId"`
	RelationType    string `json:"relationType"`
	ContractVersion string `json:"contractVersion"`
	CreatedAt       int64  `json:"createdAt"`
	PayloadJSON     string `json:"payloadJson"`
	PayloadHash     string `json:"payloadHash"`
}

type PurposeRelationEventRecord struct {
	ID              string `json:"id"`
	RelationID      string `json:"relationId"`
	IdempotencyKey  string `json:"idempotencyKey"`
	Sequence        int    `json:"sequence"`
	EventType       string `json:"eventType"`
	ExpectedEventID string `json:"expectedEventId,omitempty"`
	EvidenceEventID string `json:"evidenceEventId"`
	Reason          string `json:"reason,omitempty"`
	OccurredAt      int64  `json:"occurredAt"`
	CreatedAt       int64  `json:"createdAt"`
	PayloadJSON     string `json:"payloadJson"`
	PayloadHash     string `json:"payloadHash"`
}

type PurposeRelationDetail struct {
	Relation PurposeRelationRecord        `json:"relation"`
	Events   []PurposeRelationEventRecord `json:"events"`
	Current  PurposeRelationEventRecord   `json:"current"`
}

type PurposeOpenLoopDetail struct {
	OpenLoop  PurposeOpenLoopRecord        `json:"openLoop"`
	Events    []PurposeOpenLoopEventRecord `json:"events"`
	Evidence  []PurposeEvidenceRecord      `json:"evidence"`
	Relations []PurposeRelationDetail      `json:"relations"`
	Current   PurposeOpenLoopEventRecord   `json:"current"`
	Eligible  bool                         `json:"eligible"`
}

type PurposeOpenLoopMutationResult struct {
	OpenLoop PurposeOpenLoopRecord      `json:"openLoop"`
	Event    PurposeOpenLoopEventRecord `json:"event"`
	Evidence []PurposeEvidenceRecord    `json:"evidence"`
}

type PurposeRelationMutationResult struct {
	Relation PurposeRelationRecord      `json:"relation"`
	Event    PurposeRelationEventRecord `json:"event"`
}

type PurposeMapStore struct {
	db *sql.DB
}

func NewPurposeMapStore(db *sql.DB) *PurposeMapStore {
	return &PurposeMapStore{db: db}
}

func (store *PurposeMapStore) require() error {
	if store == nil || store.db == nil {
		return errors.New("Purpose Map storage is unavailable")
	}
	return nil
}

func normalizePurposeText(value, field string, maxBytes int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("Purpose %s is required", field)
	}
	if len(value) > maxBytes {
		return "", fmt.Errorf("Purpose %s is too long", field)
	}
	return value, nil
}

func normalizePurposeID(value, field string) (string, error) {
	return normalizePurposeText(value, field, purposeMaxIdentityBytes, true)
}

func normalizePurposeKind(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case PurposeKindCommitment, PurposeKindResponsibility, PurposeKindGoal,
		PurposeKindConcern, PurposeKindOpportunity:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported Purpose kind %q", value)
	}
}

func normalizePurposeEventType(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case PurposeEventDiscovered, PurposeEventReinforced, PurposeEventRevised,
		PurposeEventWaiting, PurposeEventResolved, PurposeEventReopened,
		PurposeEventContradicted, PurposeEventDismissed, PurposeEventSuperseded:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported Purpose event type %q", value)
	}
}

func normalizePurposeOwner(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = PurposeOwnerUnknown
	}
	switch value {
	case PurposeOwnerUser, PurposeOwnerOther, PurposeOwnerShared, PurposeOwnerUnknown:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported Purpose owner %q", value)
	}
}

func normalizePurposeDue(kind, value string) (string, string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	value = strings.TrimSpace(value)
	if kind == "" {
		kind = PurposeDueUnknown
	}
	switch kind {
	case PurposeDueNone, PurposeDueUnknown:
		if value != "" {
			return "", "", errors.New("Purpose due value requires a typed due signal")
		}
	case PurposeDueDate, PurposeDueWindow, PurposeDueCondition:
		if value == "" {
			return "", "", errors.New("Purpose typed due signal requires a value")
		}
	default:
		return "", "", fmt.Errorf("unsupported Purpose due kind %q", kind)
	}
	if len(value) > purposeMaxFieldBytes {
		return "", "", errors.New("Purpose due value is too long")
	}
	return kind, value, nil
}

func normalizePurposeReasoning(status, rationale string) (string, string, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	rationale = strings.TrimSpace(rationale)
	if status == "" {
		status = PurposeReasoningUnknown
	}
	if len(rationale) > purposeMaxReasonBytes {
		return "", "", errors.New("Purpose rationale is too long")
	}
	switch status {
	case PurposeReasoningExplicit:
		if rationale == "" {
			return "", "", errors.New("explicit Purpose reasoning requires a rationale")
		}
	case PurposeReasoningUnknown:
		if rationale != "" {
			return "", "", errors.New("unknown Purpose reasoning cannot retain a rationale")
		}
	default:
		return "", "", fmt.Errorf("unsupported Purpose reasoning status %q", status)
	}
	return status, rationale, nil
}

func normalizePurposeHash(value, field string, required bool) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" && !required {
		return "", nil
	}
	if len(value) != 64 {
		return "", fmt.Errorf("Purpose %s must be a SHA-256 hash", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("Purpose %s must be a SHA-256 hash", field)
	}
	return value, nil
}

func normalizePurposeEvidenceClass(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case PurposeEvidenceExplicitUser, PurposeEvidenceObservedUser,
		PurposeEvidenceEvaluatedProjection, PurposeEvidenceVerifiedExternal,
		PurposeEvidenceVerifiedOutcome, PurposeEvidenceCorrection:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported Purpose evidence class %q", value)
	}
}

func normalizePurposeStance(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case PurposeStanceSupport, PurposeStanceUpdate, PurposeStanceWait,
		PurposeStanceResolve, PurposeStanceContradict, PurposeStanceDismiss:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported Purpose evidence stance %q", value)
	}
}

func normalizePurposeEvidenceInput(input PurposeEvidenceInput) (PurposeEvidenceInput, error) {
	var err error
	if input.SourceKind, err = normalizePurposeID(input.SourceKind, "source kind"); err != nil {
		return PurposeEvidenceInput{}, err
	}
	if input.SourceID, err = normalizePurposeID(input.SourceID, "source ID"); err != nil {
		return PurposeEvidenceInput{}, err
	}
	if input.SourceVersion, err = normalizePurposeID(input.SourceVersion, "source version"); err != nil {
		return PurposeEvidenceInput{}, err
	}
	if input.SourceObservedAt <= 0 {
		return PurposeEvidenceInput{}, errors.New("Purpose source occurrence time is required")
	}
	if input.SourceHash, err = normalizePurposeHash(input.SourceHash, "source hash", true); err != nil {
		return PurposeEvidenceInput{}, err
	}
	if input.EvidenceClass, err = normalizePurposeEvidenceClass(input.EvidenceClass); err != nil {
		return PurposeEvidenceInput{}, err
	}
	if input.Stance, err = normalizePurposeStance(input.Stance); err != nil {
		return PurposeEvidenceInput{}, err
	}
	if input.IndependenceKey, err = normalizePurposeID(input.IndependenceKey, "source independence key"); err != nil {
		return PurposeEvidenceInput{}, err
	}
	if input.ExcerptHash, err = normalizePurposeHash(input.ExcerptHash, "excerpt hash", false); err != nil {
		return PurposeEvidenceInput{}, err
	}
	input.PrivacyDecision = strings.ToLower(strings.TrimSpace(input.PrivacyDecision))
	if input.PrivacyDecision == "" {
		input.PrivacyDecision = TaskContextPrivacyRedact
	}
	if input.PrivacyDecision != TaskContextPrivacyInclude &&
		input.PrivacyDecision != TaskContextPrivacyRedact {
		return PurposeEvidenceInput{}, errors.New("Purpose evidence must be included or redacted before storage")
	}
	return input, nil
}

func purposeEvidenceIdentity(input PurposeEvidenceInput) string {
	return strings.Join([]string{
		input.SourceKind,
		input.SourceID,
		input.SourceVersion,
		input.Stance,
	}, "\x00")
}

func normalizePurposeEvidenceInputs(inputs []PurposeEvidenceInput, occurredAt int64) ([]PurposeEvidenceInput, error) {
	if len(inputs) == 0 {
		return nil, errors.New("Purpose event requires source evidence")
	}
	result := make([]PurposeEvidenceInput, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	stance := ""
	for _, input := range inputs {
		normalized, err := normalizePurposeEvidenceInput(input)
		if err != nil {
			return nil, err
		}
		if normalized.SourceObservedAt > occurredAt {
			return nil, errors.New("Purpose evidence cannot occur after its lifecycle event")
		}
		if stance == "" {
			stance = normalized.Stance
		} else if stance != normalized.Stance {
			return nil, errors.New("one Purpose event cannot mix evidence stances")
		}
		key := purposeEvidenceIdentity(normalized)
		if _, ok := seen[key]; ok {
			return nil, errors.New("Purpose event contains duplicate evidence")
		}
		seen[key] = struct{}{}
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool {
		return purposeEvidenceIdentity(result[i]) < purposeEvidenceIdentity(result[j])
	})
	return result, nil
}

func purposeHasEvidenceClass(items []PurposeEvidenceRecord, classes ...string) bool {
	allowed := make(map[string]struct{}, len(classes))
	for _, class := range classes {
		allowed[class] = struct{}{}
	}
	for _, item := range items {
		if _, ok := allowed[item.EvidenceClass]; ok {
			return true
		}
	}
	return false
}

func purposeSupportFromEvidence(items []PurposeEvidenceRecord) string {
	independent := make(map[string]struct{})
	for _, item := range items {
		if item.Stance != PurposeStanceSupport && item.Stance != PurposeStanceUpdate {
			continue
		}
		switch item.EvidenceClass {
		case PurposeEvidenceExplicitUser, PurposeEvidenceCorrection:
			return PurposeSupportSupported
		case PurposeEvidenceObservedUser, PurposeEvidenceEvaluatedProjection:
			independent[item.IndependenceKey] = struct{}{}
		}
	}
	if len(independent) >= 2 {
		return PurposeSupportSupported
	}
	return PurposeSupportHypothesis
}

func purposeEventState(eventType, previousState, support string, currentEvidence []PurposeEvidenceRecord) (string, string, error) {
	stance := ""
	if len(currentEvidence) > 0 {
		stance = currentEvidence[0].Stance
	}
	allStance := func(expected string) bool {
		if stance != expected {
			return false
		}
		for _, item := range currentEvidence {
			if item.Stance != expected {
				return false
			}
		}
		return len(currentEvidence) > 0
	}
	switch eventType {
	case PurposeEventDiscovered:
		if previousState != "" || !allStance(PurposeStanceSupport) {
			return "", "", errors.New("invalid Purpose discovery transition")
		}
		if support == PurposeSupportSupported {
			return PurposeStateActive, support, nil
		}
		return PurposeStateCandidate, support, nil
	case PurposeEventReinforced, PurposeEventRevised:
		if previousState == PurposeStateResolved || previousState == PurposeStateContradicted ||
			previousState == PurposeStateDismissed || previousState == PurposeStateSuperseded ||
			(previousState != PurposeStateCandidate && previousState != PurposeStateActive && previousState != PurposeStateWaiting) {
			return "", "", errors.New("invalid Purpose reinforcement transition")
		}
		expectedStance := PurposeStanceSupport
		if eventType == PurposeEventRevised {
			expectedStance = PurposeStanceUpdate
		}
		if !allStance(expectedStance) {
			return "", "", errors.New("Purpose reinforcement stance is inconsistent")
		}
		if previousState == PurposeStateCandidate && support == PurposeSupportSupported {
			return PurposeStateActive, support, nil
		}
		return previousState, support, nil
	case PurposeEventWaiting:
		if previousState != PurposeStateActive || support != PurposeSupportSupported || !allStance(PurposeStanceWait) {
			return "", "", errors.New("invalid Purpose waiting transition")
		}
		return PurposeStateWaiting, support, nil
	case PurposeEventResolved:
		if (previousState != PurposeStateActive && previousState != PurposeStateWaiting) || !allStance(PurposeStanceResolve) {
			return "", "", errors.New("invalid Purpose resolution transition")
		}
		if !purposeHasEvidenceClass(currentEvidence, PurposeEvidenceExplicitUser,
			PurposeEvidenceCorrection, PurposeEvidenceVerifiedExternal,
			PurposeEvidenceVerifiedOutcome) {
			return "", "", errors.New("Purpose resolution requires verified or explicit evidence")
		}
		return PurposeStateResolved, support, nil
	case PurposeEventReopened:
		if previousState != PurposeStateResolved && previousState != PurposeStateContradicted &&
			previousState != PurposeStateDismissed {
			return "", "", errors.New("invalid Purpose reopening transition")
		}
		if !allStance(PurposeStanceSupport) && !allStance(PurposeStanceUpdate) {
			return "", "", errors.New("Purpose reopening stance is inconsistent")
		}
		if !purposeHasEvidenceClass(currentEvidence, PurposeEvidenceExplicitUser,
			PurposeEvidenceCorrection, PurposeEvidenceVerifiedExternal) {
			return "", "", errors.New("Purpose reopening requires explicit or verified evidence")
		}
		if support == PurposeSupportSupported {
			return PurposeStateActive, support, nil
		}
		return PurposeStateCandidate, support, nil
	case PurposeEventContradicted:
		if previousState != PurposeStateCandidate && previousState != PurposeStateActive && previousState != PurposeStateWaiting {
			return "", "", errors.New("invalid Purpose contradiction transition")
		}
		if !allStance(PurposeStanceContradict) {
			return "", "", errors.New("Purpose contradiction stance is inconsistent")
		}
		if !purposeHasEvidenceClass(currentEvidence, PurposeEvidenceExplicitUser,
			PurposeEvidenceCorrection, PurposeEvidenceVerifiedExternal) {
			independent := make(map[string]struct{})
			for _, item := range currentEvidence {
				if item.EvidenceClass == PurposeEvidenceObservedUser ||
					item.EvidenceClass == PurposeEvidenceEvaluatedProjection {
					independent[item.IndependenceKey] = struct{}{}
				}
			}
			if len(independent) < 2 {
				return "", "", errors.New("Purpose contradiction requires independent evidence")
			}
		}
		return PurposeStateContradicted, PurposeSupportContradicted, nil
	case PurposeEventDismissed:
		if previousState != PurposeStateCandidate && previousState != PurposeStateActive && previousState != PurposeStateWaiting {
			return "", "", errors.New("invalid Purpose dismissal transition")
		}
		if !allStance(PurposeStanceDismiss) || !purposeHasEvidenceClass(
			currentEvidence, PurposeEvidenceExplicitUser, PurposeEvidenceCorrection,
		) {
			return "", "", errors.New("Purpose dismissal requires explicit evidence")
		}
		return PurposeStateDismissed, support, nil
	case PurposeEventSuperseded:
		if previousState != PurposeStateCandidate && previousState != PurposeStateActive && previousState != PurposeStateWaiting {
			return "", "", errors.New("invalid Purpose supersession transition")
		}
		if !allStance(PurposeStanceUpdate) {
			return "", "", errors.New("Purpose supersession stance is inconsistent")
		}
		return PurposeStateSuperseded, support, nil
	default:
		return "", "", fmt.Errorf("unsupported Purpose event type %q", eventType)
	}
}

func purposeEventEligible(event PurposeOpenLoopEventRecord) bool {
	return event.SupportState == PurposeSupportSupported &&
		(event.LifecycleState == PurposeStateActive || event.LifecycleState == PurposeStateWaiting)
}

type purposeRootPayload struct {
	ID              string `json:"id"`
	IdempotencyKey  string `json:"idempotencyKey"`
	CanonicalKey    string `json:"canonicalKey"`
	Kind            string `json:"kind"`
	ContractVersion string `json:"contractVersion"`
	CreatedAt       int64  `json:"createdAt"`
}

type purposeEventPayload struct {
	ID                 string   `json:"id"`
	OpenLoopID         string   `json:"openLoopId"`
	IdempotencyKey     string   `json:"idempotencyKey"`
	Sequence           int      `json:"sequence"`
	EventType          string   `json:"eventType"`
	ExpectedEventID    string   `json:"expectedEventId,omitempty"`
	PredecessorEventID string   `json:"predecessorEventId,omitempty"`
	Title              string   `json:"title"`
	DesiredOutcome     string   `json:"desiredOutcome"`
	Owner              string   `json:"owner"`
	Counterparty       string   `json:"counterparty,omitempty"`
	Scope              string   `json:"scope,omitempty"`
	DueKind            string   `json:"dueKind"`
	DueValue           string   `json:"dueValue,omitempty"`
	WaitingCondition   string   `json:"waitingCondition,omitempty"`
	LifecycleState     string   `json:"lifecycleState"`
	SupportState       string   `json:"supportState"`
	Confidence         float64  `json:"confidence"`
	Rationale          string   `json:"rationale,omitempty"`
	ReasoningStatus    string   `json:"reasoningStatus"`
	EvidenceSetHash    string   `json:"evidenceSetHash"`
	EvidenceIDs        []string `json:"evidenceIds"`
	OccurredAt         int64    `json:"occurredAt"`
	CreatedAt          int64    `json:"createdAt"`
}

type purposeEvidencePayload struct {
	ID               string `json:"id"`
	EventID          string `json:"eventId"`
	SourceKind       string `json:"sourceKind"`
	SourceID         string `json:"sourceId"`
	SourceVersion    string `json:"sourceVersion"`
	SourceObservedAt int64  `json:"sourceObservedAt"`
	SourceHash       string `json:"sourceHash"`
	EvidenceClass    string `json:"evidenceClass"`
	Stance           string `json:"stance"`
	IndependenceKey  string `json:"independenceKey"`
	ExcerptHash      string `json:"excerptHash,omitempty"`
	PrivacyDecision  string `json:"privacyDecision"`
	CreatedAt        int64  `json:"createdAt"`
}

type purposeRelationPayload struct {
	ID              string `json:"id"`
	IdempotencyKey  string `json:"idempotencyKey"`
	SubjectID       string `json:"subjectId"`
	ObjectID        string `json:"objectId"`
	RelationType    string `json:"relationType"`
	ContractVersion string `json:"contractVersion"`
	CreatedAt       int64  `json:"createdAt"`
}

type purposeRelationEventPayload struct {
	ID              string `json:"id"`
	RelationID      string `json:"relationId"`
	IdempotencyKey  string `json:"idempotencyKey"`
	Sequence        int    `json:"sequence"`
	EventType       string `json:"eventType"`
	ExpectedEventID string `json:"expectedEventId,omitempty"`
	EvidenceEventID string `json:"evidenceEventId"`
	Reason          string `json:"reason,omitempty"`
	OccurredAt      int64  `json:"occurredAt"`
	CreatedAt       int64  `json:"createdAt"`
}

func purposeRootPayloadJSON(record PurposeOpenLoopRecord) (string, string, error) {
	return agencyStructPayload(purposeRootPayload{
		ID: record.ID, IdempotencyKey: record.IdempotencyKey,
		CanonicalKey: record.CanonicalKey, Kind: record.Kind,
		ContractVersion: record.ContractVersion, CreatedAt: record.CreatedAt,
	})
}

func purposeEvidencePayloadJSON(record PurposeEvidenceRecord) (string, string, error) {
	return agencyStructPayload(purposeEvidencePayload{
		ID: record.ID, EventID: record.EventID, SourceKind: record.SourceKind,
		SourceID: record.SourceID, SourceVersion: record.SourceVersion,
		SourceObservedAt: record.SourceObservedAt, SourceHash: record.SourceHash,
		EvidenceClass: record.EvidenceClass, Stance: record.Stance,
		IndependenceKey: record.IndependenceKey, ExcerptHash: record.ExcerptHash,
		PrivacyDecision: record.PrivacyDecision, CreatedAt: record.CreatedAt,
	})
}

func purposeEventPayloadJSON(record PurposeOpenLoopEventRecord) (string, string, error) {
	return agencyStructPayload(purposeEventPayload{
		ID: record.ID, OpenLoopID: record.OpenLoopID,
		IdempotencyKey: record.IdempotencyKey, Sequence: record.Sequence,
		EventType: record.EventType, ExpectedEventID: record.ExpectedEventID,
		PredecessorEventID: record.PredecessorEventID, Title: record.Title,
		DesiredOutcome: record.DesiredOutcome, Owner: record.Owner,
		Counterparty: record.Counterparty, Scope: record.Scope, DueKind: record.DueKind,
		DueValue: record.DueValue, WaitingCondition: record.WaitingCondition,
		LifecycleState: record.LifecycleState, SupportState: record.SupportState,
		Confidence: record.Confidence, Rationale: record.Rationale,
		ReasoningStatus: record.ReasoningStatus, EvidenceSetHash: record.EvidenceSetHash,
		EvidenceIDs: append([]string(nil), record.EvidenceIDs...),
		OccurredAt:  record.OccurredAt, CreatedAt: record.CreatedAt,
	})
}

func purposeRelationPayloadJSON(record PurposeRelationRecord) (string, string, error) {
	return agencyStructPayload(purposeRelationPayload{
		ID: record.ID, IdempotencyKey: record.IdempotencyKey,
		SubjectID: record.SubjectID, ObjectID: record.ObjectID,
		RelationType: record.RelationType, ContractVersion: record.ContractVersion,
		CreatedAt: record.CreatedAt,
	})
}

func purposeRelationEventPayloadJSON(record PurposeRelationEventRecord) (string, string, error) {
	return agencyStructPayload(purposeRelationEventPayload{
		ID: record.ID, RelationID: record.RelationID,
		IdempotencyKey: record.IdempotencyKey, Sequence: record.Sequence,
		EventType: record.EventType, ExpectedEventID: record.ExpectedEventID,
		EvidenceEventID: record.EvidenceEventID, Reason: record.Reason,
		OccurredAt: record.OccurredAt, CreatedAt: record.CreatedAt,
	})
}

func purposeEvidenceSetHash(items []PurposeEvidenceRecord) (string, []string, error) {
	items = append([]PurposeEvidenceRecord(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	ids := make([]string, 0, len(items))
	hashes := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
		hashes = append(hashes, item.PayloadHash)
	}
	raw, _, err := agencyStructPayload(hashes)
	if err != nil {
		return "", nil, err
	}
	return agencyPayloadHash([]byte(raw)), ids, nil
}

const purposeRootSelectColumns = `
	id, idempotency_key, canonical_key, kind, contract_version, created_at,
	payload_json, payload_hash`

const purposeEventSelectColumns = `
	id, open_loop_id, idempotency_key, sequence, event_type,
	expected_event_id, predecessor_event_id, title, desired_outcome, owner,
	counterparty, scope, due_kind, due_value, waiting_condition,
	lifecycle_state, support_state, confidence, rationale, reasoning_status,
	evidence_set_hash, occurred_at, created_at, payload_json, payload_hash`

const purposeEvidenceSelectColumns = `
	id, event_id, source_kind, source_id, source_version, source_observed_at,
	source_hash, evidence_class, stance, independence_key, excerpt_hash,
	privacy_decision, created_at, payload_json, payload_hash`

const purposeRelationSelectColumns = `
	id, idempotency_key, subject_id, object_id, relation_type,
	contract_version, created_at, payload_json, payload_hash`

const purposeRelationEventSelectColumns = `
	id, relation_id, idempotency_key, sequence, event_type,
	expected_event_id, evidence_event_id, reason, occurred_at, created_at,
	payload_json, payload_hash`

type purposeQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

type purposeContextQueryer struct {
	ctx context.Context
	db  *sql.DB
}

func (q purposeContextQueryer) Query(query string, args ...any) (*sql.Rows, error) {
	return q.db.QueryContext(q.ctx, query, args...)
}

func (q purposeContextQueryer) QueryRow(query string, args ...any) *sql.Row {
	return q.db.QueryRowContext(q.ctx, query, args...)
}

func scanPurposeRoot(scanner interface{ Scan(dest ...any) error }) (PurposeOpenLoopRecord, error) {
	var item PurposeOpenLoopRecord
	err := scanner.Scan(
		&item.ID, &item.IdempotencyKey, &item.CanonicalKey, &item.Kind,
		&item.ContractVersion, &item.CreatedAt, &item.PayloadJSON, &item.PayloadHash,
	)
	return item, err
}

func scanPurposeEvent(scanner interface{ Scan(dest ...any) error }) (PurposeOpenLoopEventRecord, error) {
	var item PurposeOpenLoopEventRecord
	err := scanner.Scan(
		&item.ID, &item.OpenLoopID, &item.IdempotencyKey, &item.Sequence,
		&item.EventType, &item.ExpectedEventID, &item.PredecessorEventID,
		&item.Title, &item.DesiredOutcome, &item.Owner, &item.Counterparty,
		&item.Scope, &item.DueKind, &item.DueValue, &item.WaitingCondition,
		&item.LifecycleState, &item.SupportState, &item.Confidence,
		&item.Rationale, &item.ReasoningStatus, &item.EvidenceSetHash,
		&item.OccurredAt, &item.CreatedAt, &item.PayloadJSON, &item.PayloadHash,
	)
	return item, err
}

func scanPurposeEvidence(scanner interface{ Scan(dest ...any) error }) (PurposeEvidenceRecord, error) {
	var item PurposeEvidenceRecord
	err := scanner.Scan(
		&item.ID, &item.EventID, &item.SourceKind, &item.SourceID,
		&item.SourceVersion, &item.SourceObservedAt, &item.SourceHash,
		&item.EvidenceClass, &item.Stance, &item.IndependenceKey,
		&item.ExcerptHash, &item.PrivacyDecision, &item.CreatedAt,
		&item.PayloadJSON, &item.PayloadHash,
	)
	return item, err
}

func scanPurposeRelation(scanner interface{ Scan(dest ...any) error }) (PurposeRelationRecord, error) {
	var item PurposeRelationRecord
	err := scanner.Scan(
		&item.ID, &item.IdempotencyKey, &item.SubjectID, &item.ObjectID,
		&item.RelationType, &item.ContractVersion, &item.CreatedAt,
		&item.PayloadJSON, &item.PayloadHash,
	)
	return item, err
}

func scanPurposeRelationEvent(scanner interface{ Scan(dest ...any) error }) (PurposeRelationEventRecord, error) {
	var item PurposeRelationEventRecord
	err := scanner.Scan(
		&item.ID, &item.RelationID, &item.IdempotencyKey, &item.Sequence,
		&item.EventType, &item.ExpectedEventID, &item.EvidenceEventID,
		&item.Reason, &item.OccurredAt, &item.CreatedAt,
		&item.PayloadJSON, &item.PayloadHash,
	)
	return item, err
}

func validatePurposeRoot(item PurposeOpenLoopRecord) error {
	if _, err := normalizePurposeID(item.ID, "root ID"); err != nil {
		return err
	}
	if _, err := normalizePurposeID(item.IdempotencyKey, "root idempotency key"); err != nil {
		return err
	}
	key, err := normalizePurposeText(item.CanonicalKey, "canonical key", purposeMaxKeyBytes, true)
	if err != nil || key != item.CanonicalKey {
		return errors.New("Purpose root canonical key is not normalized")
	}
	kind, err := normalizePurposeKind(item.Kind)
	if err != nil || kind != item.Kind {
		return errors.New("Purpose root kind is invalid")
	}
	if item.ContractVersion != PurposeOpenLoopContractVersion || item.CreatedAt <= 0 {
		return errors.New("Purpose root contract is invalid")
	}
	if item.ID != deterministicAgencyID("purpose-open-loop", item.IdempotencyKey) {
		return fmt.Errorf("Purpose root %q deterministic identity is invalid", item.ID)
	}
	payloadJSON, payloadHash, err := purposeRootPayloadJSON(item)
	if err != nil {
		return err
	}
	if payloadJSON != item.PayloadJSON || payloadHash != item.PayloadHash {
		return fmt.Errorf("Purpose root %q payload is inconsistent", item.ID)
	}
	return nil
}

func validatePurposeEvidence(item PurposeEvidenceRecord) error {
	input, err := normalizePurposeEvidenceInput(PurposeEvidenceInput{
		SourceKind: item.SourceKind, SourceID: item.SourceID,
		SourceVersion: item.SourceVersion, SourceObservedAt: item.SourceObservedAt,
		SourceHash: item.SourceHash, EvidenceClass: item.EvidenceClass,
		Stance: item.Stance, IndependenceKey: item.IndependenceKey,
		ExcerptHash: item.ExcerptHash, PrivacyDecision: item.PrivacyDecision,
	})
	if err != nil {
		return err
	}
	if input.SourceKind != item.SourceKind || input.SourceID != item.SourceID ||
		input.SourceVersion != item.SourceVersion || input.SourceHash != item.SourceHash ||
		input.EvidenceClass != item.EvidenceClass || input.Stance != item.Stance ||
		input.IndependenceKey != item.IndependenceKey ||
		input.ExcerptHash != item.ExcerptHash || input.PrivacyDecision != item.PrivacyDecision {
		return fmt.Errorf("Purpose evidence %q is not normalized", item.ID)
	}
	if item.ID == "" || item.EventID == "" || item.CreatedAt <= 0 ||
		item.SourceObservedAt > item.CreatedAt {
		return fmt.Errorf("Purpose evidence %q timing or identity is invalid", item.ID)
	}
	inputIdentity := PurposeEvidenceInput{
		SourceKind: item.SourceKind, SourceID: item.SourceID,
		SourceVersion: item.SourceVersion, Stance: item.Stance,
	}
	if item.ID != deterministicAgencyID(
		"purpose-open-loop-evidence",
		item.EventID+"\x00"+purposeEvidenceIdentity(inputIdentity),
	) {
		return fmt.Errorf("Purpose evidence %q deterministic identity is invalid", item.ID)
	}
	payloadJSON, payloadHash, err := purposeEvidencePayloadJSON(item)
	if err != nil {
		return err
	}
	if payloadJSON != item.PayloadJSON || payloadHash != item.PayloadHash {
		return fmt.Errorf("Purpose evidence %q payload is inconsistent", item.ID)
	}
	return nil
}

func validatePurposeEventFields(item PurposeOpenLoopEventRecord) error {
	if _, err := normalizePurposeEventType(item.EventType); err != nil {
		return err
	}
	if _, err := normalizePurposeText(item.Title, "title", purposeMaxTitleBytes, true); err != nil {
		return err
	}
	if _, err := normalizePurposeText(item.DesiredOutcome, "desired outcome", purposeMaxOutcomeBytes, true); err != nil {
		return err
	}
	if _, err := normalizePurposeOwner(item.Owner); err != nil {
		return err
	}
	if _, err := normalizePurposeText(item.Counterparty, "counterparty", purposeMaxFieldBytes, false); err != nil {
		return err
	}
	if _, err := normalizePurposeText(item.Scope, "scope", purposeMaxScopeBytes, false); err != nil {
		return err
	}
	if _, _, err := normalizePurposeDue(item.DueKind, item.DueValue); err != nil {
		return err
	}
	if _, err := normalizePurposeText(item.WaitingCondition, "waiting condition", purposeMaxFieldBytes, false); err != nil {
		return err
	}
	if _, _, err := normalizePurposeReasoning(item.ReasoningStatus, item.Rationale); err != nil {
		return err
	}
	if item.Confidence < 0 || item.Confidence > 1 || item.OccurredAt <= 0 ||
		item.CreatedAt < item.OccurredAt || item.Sequence <= 0 {
		return fmt.Errorf("Purpose event %q numeric or timing fields are invalid", item.ID)
	}
	if item.ID != deterministicAgencyID(
		"purpose-open-loop-event",
		item.OpenLoopID+"\x00"+item.IdempotencyKey,
	) {
		return fmt.Errorf("Purpose event %q deterministic identity is invalid", item.ID)
	}
	return nil
}

func validatePurposeRelationRoot(item PurposeRelationRecord, roots map[string]PurposeOpenLoopRecord) error {
	if item.ID == "" || item.IdempotencyKey == "" || item.SubjectID == item.ObjectID ||
		item.CreatedAt <= 0 || item.ContractVersion != PurposeRelationContractVersion {
		return fmt.Errorf("Purpose relation %q identity is invalid", item.ID)
	}
	if _, ok := roots[item.SubjectID]; !ok {
		return fmt.Errorf("Purpose relation %q has no subject root", item.ID)
	}
	if _, ok := roots[item.ObjectID]; !ok {
		return fmt.Errorf("Purpose relation %q has no object root", item.ID)
	}
	if item.CreatedAt < roots[item.SubjectID].CreatedAt ||
		item.CreatedAt < roots[item.ObjectID].CreatedAt ||
		item.ID != deterministicAgencyID("purpose-open-loop-relation", item.IdempotencyKey) {
		return fmt.Errorf("Purpose relation %q creation identity is invalid", item.ID)
	}
	switch item.RelationType {
	case PurposeRelationDuplicateOf, PurposeRelationSupersedes,
		PurposeRelationDependsOn, PurposeRelationBlocks, PurposeRelationRelatedTo:
	default:
		return fmt.Errorf("Purpose relation %q type is invalid", item.ID)
	}
	payloadJSON, payloadHash, err := purposeRelationPayloadJSON(item)
	if err != nil {
		return err
	}
	if payloadJSON != item.PayloadJSON || payloadHash != item.PayloadHash {
		return fmt.Errorf("Purpose relation %q payload is inconsistent", item.ID)
	}
	return nil
}

func validatePurposeRelationEvent(
	item PurposeRelationEventRecord,
	relation PurposeRelationRecord,
	previous *PurposeRelationEventRecord,
	events map[string]PurposeOpenLoopEventRecord,
	evidenceByEvent map[string][]PurposeEvidenceRecord,
) error {
	if item.ID == "" || item.RelationID != relation.ID || item.IdempotencyKey == "" ||
		item.Sequence <= 0 || item.OccurredAt <= 0 || item.CreatedAt < item.OccurredAt {
		return fmt.Errorf("Purpose relation event %q identity is invalid", item.ID)
	}
	if item.ID != deterministicAgencyID(
		"purpose-open-loop-relation-event",
		item.RelationID+"\x00"+item.IdempotencyKey,
	) {
		return fmt.Errorf("Purpose relation event %q deterministic identity is invalid", item.ID)
	}
	evidence, ok := events[item.EvidenceEventID]
	if !ok || (evidence.OpenLoopID != relation.SubjectID && evidence.OpenLoopID != relation.ObjectID) ||
		evidence.OccurredAt > item.OccurredAt || evidence.CreatedAt > item.CreatedAt {
		return fmt.Errorf("Purpose relation event %q evidence is invalid", item.ID)
	}
	if previous == nil {
		if item.Sequence != 1 || item.EventType != PurposeRelationEventAsserted || item.ExpectedEventID != "" {
			return fmt.Errorf("Purpose relation event %q initial transition is invalid", item.ID)
		}
		if item.CreatedAt != relation.CreatedAt {
			return fmt.Errorf("Purpose relation event %q creation time is inconsistent", item.ID)
		}
	} else {
		if item.Sequence != previous.Sequence+1 || item.ExpectedEventID != previous.ID ||
			item.OccurredAt < previous.OccurredAt || item.CreatedAt < previous.CreatedAt {
			return fmt.Errorf("Purpose relation event %q predecessor is invalid", item.ID)
		}
		if item.EventType == previous.EventType {
			return fmt.Errorf("Purpose relation event %q repeats current state", item.ID)
		}
		if item.EventType != PurposeRelationEventAsserted && item.EventType != PurposeRelationEventRetracted {
			return fmt.Errorf("Purpose relation event %q type is invalid", item.ID)
		}
	}
	if item.EventType == PurposeRelationEventRetracted && strings.TrimSpace(item.Reason) == "" {
		return fmt.Errorf("Purpose relation event %q retraction reason is required", item.ID)
	}
	if item.EventType == PurposeRelationEventRetracted && !purposeHasEvidenceClass(
		evidenceByEvent[item.EvidenceEventID],
		PurposeEvidenceExplicitUser,
		PurposeEvidenceCorrection,
	) {
		return fmt.Errorf("Purpose relation event %q retraction requires explicit evidence", item.ID)
	}
	if item.EventType == PurposeRelationEventAsserted && item.Reason != "" {
		return fmt.Errorf("Purpose relation event %q assertion cannot contain a retraction reason", item.ID)
	}
	payloadJSON, payloadHash, err := purposeRelationEventPayloadJSON(item)
	if err != nil {
		return err
	}
	if payloadJSON != item.PayloadJSON || payloadHash != item.PayloadHash {
		return fmt.Errorf("Purpose relation event %q payload is inconsistent", item.ID)
	}
	return nil
}

func purposeActiveRelationCycles(relations []PurposeRelationDetail) error {
	byType := make(map[string]map[string][]string)
	duplicateTargets := make(map[string]string)
	for _, detail := range relations {
		if detail.Current.EventType != PurposeRelationEventAsserted {
			continue
		}
		relation := detail.Relation
		if relation.RelationType == PurposeRelationRelatedTo {
			continue
		}
		if relation.RelationType == PurposeRelationDuplicateOf {
			if existing, ok := duplicateTargets[relation.SubjectID]; ok && existing != relation.ObjectID {
				return fmt.Errorf("Purpose root %q has ambiguous duplicate canonical roots", relation.SubjectID)
			}
			duplicateTargets[relation.SubjectID] = relation.ObjectID
		}
		graph := byType[relation.RelationType]
		if graph == nil {
			graph = make(map[string][]string)
			byType[relation.RelationType] = graph
		}
		graph[relation.SubjectID] = append(graph[relation.SubjectID], relation.ObjectID)
	}
	for relationType, graph := range byType {
		visiting := make(map[string]bool)
		visited := make(map[string]bool)
		var visit func(string) bool
		visit = func(node string) bool {
			if visiting[node] {
				return true
			}
			if visited[node] {
				return false
			}
			visiting[node] = true
			for _, next := range graph[node] {
				if visit(next) {
					return true
				}
			}
			visiting[node] = false
			visited[node] = true
			return false
		}
		for node := range graph {
			if visit(node) {
				return fmt.Errorf("Purpose %s relation graph contains a cycle", relationType)
			}
		}
	}
	return nil
}

func validatePurposeRelationHistory(relations []PurposeRelationDetail) error {
	type historicalEvent struct {
		relation PurposeRelationRecord
		event    PurposeRelationEventRecord
	}
	events := make([]historicalEvent, 0)
	for _, detail := range relations {
		for _, event := range detail.Events {
			events = append(events, historicalEvent{relation: detail.Relation, event: event})
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].event.CreatedAt != events[j].event.CreatedAt {
			return events[i].event.CreatedAt < events[j].event.CreatedAt
		}
		if events[i].event.OccurredAt != events[j].event.OccurredAt {
			return events[i].event.OccurredAt < events[j].event.OccurredAt
		}
		return events[i].event.ID < events[j].event.ID
	})
	current := make(map[string]PurposeRelationDetail)
	for _, item := range events {
		detail := current[item.relation.ID]
		detail.Relation = item.relation
		detail.Events = append(detail.Events, item.event)
		detail.Current = item.event
		current[item.relation.ID] = detail
		projection := make([]PurposeRelationDetail, 0, len(current))
		for _, relation := range current {
			projection = append(projection, relation)
		}
		if err := purposeActiveRelationCycles(projection); err != nil {
			return fmt.Errorf("invalid historical Purpose relation state: %w", err)
		}
	}
	return nil
}

func loadPurposeLedger(queryer purposeQueryer) ([]PurposeOpenLoopDetail, error) {
	rootRows, err := queryer.Query(`SELECT ` + purposeRootSelectColumns + ` FROM purpose_open_loops_v1 ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	roots := make([]PurposeOpenLoopRecord, 0)
	rootByID := make(map[string]PurposeOpenLoopRecord)
	for rootRows.Next() {
		item, scanErr := scanPurposeRoot(rootRows)
		if scanErr != nil {
			rootRows.Close()
			return nil, scanErr
		}
		if err := validatePurposeRoot(item); err != nil {
			rootRows.Close()
			return nil, err
		}
		if _, exists := rootByID[item.ID]; exists {
			rootRows.Close()
			return nil, fmt.Errorf("duplicate Purpose root %q", item.ID)
		}
		roots = append(roots, item)
		rootByID[item.ID] = item
	}
	if err := rootRows.Err(); err != nil {
		rootRows.Close()
		return nil, err
	}
	if err := rootRows.Close(); err != nil {
		return nil, err
	}

	eventRows, err := queryer.Query(`SELECT ` + purposeEventSelectColumns + ` FROM purpose_open_loop_events_v1 ORDER BY open_loop_id, sequence`)
	if err != nil {
		return nil, err
	}
	eventsByRoot := make(map[string][]PurposeOpenLoopEventRecord)
	eventByID := make(map[string]PurposeOpenLoopEventRecord)
	for eventRows.Next() {
		item, scanErr := scanPurposeEvent(eventRows)
		if scanErr != nil {
			eventRows.Close()
			return nil, scanErr
		}
		if _, ok := rootByID[item.OpenLoopID]; !ok {
			eventRows.Close()
			return nil, fmt.Errorf("Purpose event %q has no root", item.ID)
		}
		if _, exists := eventByID[item.ID]; exists {
			eventRows.Close()
			return nil, fmt.Errorf("duplicate Purpose event %q", item.ID)
		}
		eventsByRoot[item.OpenLoopID] = append(eventsByRoot[item.OpenLoopID], item)
		eventByID[item.ID] = item
	}
	if err := eventRows.Err(); err != nil {
		eventRows.Close()
		return nil, err
	}
	if err := eventRows.Close(); err != nil {
		return nil, err
	}

	evidenceRows, err := queryer.Query(`SELECT ` + purposeEvidenceSelectColumns + ` FROM purpose_open_loop_evidence_v1 ORDER BY event_id, id`)
	if err != nil {
		return nil, err
	}
	evidenceByEvent := make(map[string][]PurposeEvidenceRecord)
	for evidenceRows.Next() {
		item, scanErr := scanPurposeEvidence(evidenceRows)
		if scanErr != nil {
			evidenceRows.Close()
			return nil, scanErr
		}
		if _, ok := eventByID[item.EventID]; !ok {
			evidenceRows.Close()
			return nil, fmt.Errorf("Purpose evidence %q has no event", item.ID)
		}
		if err := validatePurposeEvidence(item); err != nil {
			evidenceRows.Close()
			return nil, err
		}
		evidenceByEvent[item.EventID] = append(evidenceByEvent[item.EventID], item)
	}
	if err := evidenceRows.Err(); err != nil {
		evidenceRows.Close()
		return nil, err
	}
	if err := evidenceRows.Close(); err != nil {
		return nil, err
	}

	for rootID, events := range eventsByRoot {
		allSupportEvidence := make([]PurposeEvidenceRecord, 0)
		var previous *PurposeOpenLoopEventRecord
		for index := range events {
			item := &events[index]
			if err := validatePurposeEventFields(*item); err != nil {
				return nil, err
			}
			if previous == nil {
				if item.Sequence != 1 || item.EventType != PurposeEventDiscovered ||
					item.ExpectedEventID != "" || item.PredecessorEventID != "" {
					return nil, fmt.Errorf("Purpose event %q initial transition is invalid", item.ID)
				}
				if item.CreatedAt != rootByID[rootID].CreatedAt {
					return nil, fmt.Errorf("Purpose event %q creation time is inconsistent", item.ID)
				}
			} else if item.Sequence != previous.Sequence+1 ||
				item.ExpectedEventID != previous.ID || item.PredecessorEventID != previous.ID ||
				item.OccurredAt < previous.OccurredAt || item.CreatedAt < previous.CreatedAt {
				return nil, fmt.Errorf("Purpose event %q predecessor is invalid", item.ID)
			}
			currentEvidence := evidenceByEvent[item.ID]
			if len(currentEvidence) == 0 {
				return nil, fmt.Errorf("Purpose event %q has no evidence", item.ID)
			}
			for _, evidence := range currentEvidence {
				if evidence.SourceObservedAt > item.OccurredAt || evidence.CreatedAt != item.CreatedAt {
					return nil, fmt.Errorf("Purpose event %q evidence timing is invalid", item.ID)
				}
			}
			if item.EventType == PurposeEventDiscovered || item.EventType == PurposeEventReinforced ||
				item.EventType == PurposeEventRevised || item.EventType == PurposeEventReopened {
				allSupportEvidence = append(allSupportEvidence, currentEvidence...)
			}
			support := purposeSupportFromEvidence(allSupportEvidence)
			previousState := ""
			if previous != nil {
				previousState = previous.LifecycleState
			}
			expectedState, expectedSupport, err := purposeEventState(
				item.EventType, previousState, support, currentEvidence,
			)
			if err != nil {
				return nil, fmt.Errorf("validate Purpose event %q: %w", item.ID, err)
			}
			if item.LifecycleState != expectedState || item.SupportState != expectedSupport {
				return nil, fmt.Errorf("Purpose event %q state projection is inconsistent", item.ID)
			}
			if item.LifecycleState == PurposeStateWaiting && strings.TrimSpace(item.WaitingCondition) == "" {
				return nil, fmt.Errorf("Purpose event %q waiting condition is required", item.ID)
			}
			if item.LifecycleState != PurposeStateWaiting && item.WaitingCondition != "" {
				return nil, fmt.Errorf("Purpose event %q has a stale waiting condition", item.ID)
			}
			if item.ReasoningStatus == PurposeReasoningExplicit {
				carried := previous != nil && previous.ReasoningStatus == PurposeReasoningExplicit &&
					previous.Rationale == item.Rationale
				if !carried && !purposeHasEvidenceClass(currentEvidence,
					PurposeEvidenceExplicitUser, PurposeEvidenceCorrection) {
					return nil, fmt.Errorf("Purpose event %q explicit reasoning has no explicit source", item.ID)
				}
			}
			evidenceSetHash, evidenceIDs, err := purposeEvidenceSetHash(currentEvidence)
			if err != nil {
				return nil, err
			}
			item.EvidenceIDs = evidenceIDs
			if item.EvidenceSetHash != evidenceSetHash {
				return nil, fmt.Errorf("Purpose event %q evidence set is inconsistent", item.ID)
			}
			payloadJSON, payloadHash, err := purposeEventPayloadJSON(*item)
			if err != nil {
				return nil, err
			}
			if item.PayloadJSON != payloadJSON || item.PayloadHash != payloadHash {
				return nil, fmt.Errorf("Purpose event %q payload is inconsistent", item.ID)
			}
			eventByID[item.ID] = *item
			previous = item
		}
		eventsByRoot[rootID] = events
	}
	for _, root := range roots {
		if len(eventsByRoot[root.ID]) == 0 {
			return nil, fmt.Errorf("Purpose root %q has no lifecycle event", root.ID)
		}
	}

	relationRows, err := queryer.Query(`SELECT ` + purposeRelationSelectColumns + ` FROM purpose_open_loop_relations_v1 ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	relations := make([]PurposeRelationRecord, 0)
	relationByID := make(map[string]PurposeRelationRecord)
	for relationRows.Next() {
		item, scanErr := scanPurposeRelation(relationRows)
		if scanErr != nil {
			relationRows.Close()
			return nil, scanErr
		}
		if err := validatePurposeRelationRoot(item, rootByID); err != nil {
			relationRows.Close()
			return nil, err
		}
		relations = append(relations, item)
		relationByID[item.ID] = item
	}
	if err := relationRows.Err(); err != nil {
		relationRows.Close()
		return nil, err
	}
	if err := relationRows.Close(); err != nil {
		return nil, err
	}

	relationEventRows, err := queryer.Query(`SELECT ` + purposeRelationEventSelectColumns + ` FROM purpose_open_loop_relation_events_v1 ORDER BY relation_id, sequence`)
	if err != nil {
		return nil, err
	}
	relationEvents := make(map[string][]PurposeRelationEventRecord)
	for relationEventRows.Next() {
		item, scanErr := scanPurposeRelationEvent(relationEventRows)
		if scanErr != nil {
			relationEventRows.Close()
			return nil, scanErr
		}
		if _, ok := relationByID[item.RelationID]; !ok {
			relationEventRows.Close()
			return nil, fmt.Errorf("Purpose relation event %q has no relation", item.ID)
		}
		relationEvents[item.RelationID] = append(relationEvents[item.RelationID], item)
	}
	if err := relationEventRows.Err(); err != nil {
		relationEventRows.Close()
		return nil, err
	}
	if err := relationEventRows.Close(); err != nil {
		return nil, err
	}

	relationDetails := make([]PurposeRelationDetail, 0, len(relations))
	for _, relation := range relations {
		events := relationEvents[relation.ID]
		if len(events) == 0 {
			return nil, fmt.Errorf("Purpose relation %q has no lifecycle event", relation.ID)
		}
		var previous *PurposeRelationEventRecord
		for index := range events {
			item := &events[index]
			if err := validatePurposeRelationEvent(
				*item,
				relation,
				previous,
				eventByID,
				evidenceByEvent,
			); err != nil {
				return nil, err
			}
			previous = item
		}
		relationDetails = append(relationDetails, PurposeRelationDetail{
			Relation: relation, Events: events, Current: events[len(events)-1],
		})
	}
	if err := purposeActiveRelationCycles(relationDetails); err != nil {
		return nil, err
	}
	if err := validatePurposeRelationHistory(relationDetails); err != nil {
		return nil, err
	}

	details := make([]PurposeOpenLoopDetail, 0, len(roots))
	for _, root := range roots {
		events := eventsByRoot[root.ID]
		evidence := make([]PurposeEvidenceRecord, 0)
		for _, event := range events {
			evidence = append(evidence, evidenceByEvent[event.ID]...)
		}
		rootRelations := make([]PurposeRelationDetail, 0)
		for _, relation := range relationDetails {
			if relation.Relation.SubjectID == root.ID || relation.Relation.ObjectID == root.ID {
				rootRelations = append(rootRelations, relation)
			}
		}
		current := events[len(events)-1]
		eligible := purposeEventEligible(current)
		for _, relation := range rootRelations {
			if relation.Relation.SubjectID == root.ID &&
				relation.Relation.RelationType == PurposeRelationDuplicateOf &&
				relation.Current.EventType == PurposeRelationEventAsserted {
				eligible = false
			}
		}
		details = append(details, PurposeOpenLoopDetail{
			OpenLoop: root, Events: events, Evidence: evidence,
			Relations: rootRelations, Current: current, Eligible: eligible,
		})
	}
	return details, nil
}

// errPurposeOpenLoopNotFound is wrapped by lookups of an id the ledger does
// not hold, so callers can tell a missing goal from a failed read.
var errPurposeOpenLoopNotFound = errors.New("Purpose open loop does not exist")

func findPurposeDetail(details []PurposeOpenLoopDetail, openLoopID string) (PurposeOpenLoopDetail, error) {
	for _, detail := range details {
		if detail.OpenLoop.ID == openLoopID {
			return detail, nil
		}
	}
	return PurposeOpenLoopDetail{}, fmt.Errorf("%w: %q", errPurposeOpenLoopNotFound, openLoopID)
}

// purposeCurrentLedgerEvent returns the authoritative event for lifecycle
// writers. Current may carry a display-only title repair; that label must never
// become authored text or new evidence when an unrelated field changes.
func purposeCurrentLedgerEvent(detail PurposeOpenLoopDetail) PurposeOpenLoopEventRecord {
	for _, event := range detail.Events {
		if event.ID == detail.Current.ID {
			return event
		}
	}
	return detail.Current
}

func (store *PurposeMapStore) ListDetailsAll() ([]PurposeOpenLoopDetail, error) {
	return store.ListDetailsAllContext(context.Background())
}

func (store *PurposeMapStore) ListDetailsAllContext(ctx context.Context) ([]PurposeOpenLoopDetail, error) {
	if err := store.require(); err != nil {
		return nil, err
	}
	queryer := purposeContextQueryer{ctx: ctx, db: store.db}
	details, err := loadPurposeLedger(queryer)
	if err != nil {
		return nil, err
	}
	return projectPurposeMailTitleRepairs(queryer, details, time.Now().UnixMilli())
}

func (store *PurposeMapStore) GetDetail(openLoopID string) (PurposeOpenLoopDetail, error) {
	if err := store.require(); err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	openLoopID = strings.TrimSpace(openLoopID)
	if openLoopID == "" {
		return PurposeOpenLoopDetail{}, errors.New("Purpose open-loop ID is required")
	}
	details, err := store.ListDetailsAll()
	if err != nil {
		return PurposeOpenLoopDetail{}, err
	}
	return findPurposeDetail(details, openLoopID)
}

func projectPurposeDetailsAt(
	details []PurposeOpenLoopDetail,
	cutoffAt int64,
) ([]PurposeOpenLoopDetail, error) {
	if cutoffAt <= 0 {
		return nil, errors.New("Purpose projection cutoff is required")
	}
	result := make([]PurposeOpenLoopDetail, 0, len(details))
	for _, detail := range details {
		if detail.OpenLoop.CreatedAt > cutoffAt {
			continue
		}
		events := make([]PurposeOpenLoopEventRecord, 0, len(detail.Events))
		includedEventIDs := make(map[string]struct{})
		for _, event := range detail.Events {
			if event.OccurredAt <= cutoffAt && event.CreatedAt <= cutoffAt {
				events = append(events, event)
				includedEventIDs[event.ID] = struct{}{}
			}
		}
		if len(events) == 0 {
			continue
		}
		evidence := make([]PurposeEvidenceRecord, 0, len(detail.Evidence))
		for _, item := range detail.Evidence {
			if _, ok := includedEventIDs[item.EventID]; ok {
				evidence = append(evidence, item)
			}
		}
		relations := make([]PurposeRelationDetail, 0, len(detail.Relations))
		for _, relation := range detail.Relations {
			if relation.Relation.CreatedAt > cutoffAt {
				continue
			}
			eventsAtCutoff := make([]PurposeRelationEventRecord, 0, len(relation.Events))
			for _, event := range relation.Events {
				if event.OccurredAt <= cutoffAt && event.CreatedAt <= cutoffAt {
					eventsAtCutoff = append(eventsAtCutoff, event)
				}
			}
			if len(eventsAtCutoff) == 0 {
				continue
			}
			relations = append(relations, PurposeRelationDetail{
				Relation: relation.Relation,
				Events:   eventsAtCutoff,
				Current:  eventsAtCutoff[len(eventsAtCutoff)-1],
			})
		}
		current := events[len(events)-1]
		eligible := purposeEventEligible(current)
		for _, relation := range relations {
			if relation.Relation.SubjectID == detail.OpenLoop.ID &&
				relation.Relation.RelationType == PurposeRelationDuplicateOf &&
				relation.Current.EventType == PurposeRelationEventAsserted {
				eligible = false
			}
		}
		result = append(result, PurposeOpenLoopDetail{
			OpenLoop:  detail.OpenLoop,
			Events:    events,
			Evidence:  evidence,
			Relations: relations,
			Current:   current,
			Eligible:  eligible,
		})
	}
	return result, nil
}

// ListDetailsAt validates the complete append-only ledger before returning the
// state that was both observed and persisted by the cutoff. A backdated event
// created later cannot leak into a historical task or planning context.
func (store *PurposeMapStore) ListDetailsAt(cutoffAt int64) ([]PurposeOpenLoopDetail, error) {
	if err := store.require(); err != nil {
		return nil, err
	}
	details, err := loadPurposeLedger(store.db)
	if err != nil {
		return nil, err
	}
	details, err = projectPurposeDetailsAt(details, cutoffAt)
	if err != nil {
		return nil, err
	}
	return projectPurposeMailTitleRepairs(store.db, details, cutoffAt)
}

func normalizePurposeCanonicalKey(value string) (string, error) {
	value = strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
	return normalizePurposeText(value, "canonical key", purposeMaxKeyBytes, true)
}

func normalizeCreatePurposeInput(input CreatePurposeOpenLoopInput) (CreatePurposeOpenLoopInput, error) {
	var err error
	if input.IdempotencyKey, err = normalizePurposeID(input.IdempotencyKey, "idempotency key"); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.CanonicalKey, err = normalizePurposeCanonicalKey(input.CanonicalKey); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.Kind, err = normalizePurposeKind(input.Kind); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.Title, err = normalizePurposeText(input.Title, "title", purposeMaxTitleBytes, true); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.DesiredOutcome, err = normalizePurposeText(input.DesiredOutcome, "desired outcome", purposeMaxOutcomeBytes, true); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.Owner, err = normalizePurposeOwner(input.Owner); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.Counterparty, err = normalizePurposeText(input.Counterparty, "counterparty", purposeMaxFieldBytes, false); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.Scope, err = normalizePurposeText(input.Scope, "scope", purposeMaxScopeBytes, false); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.DueKind, input.DueValue, err = normalizePurposeDue(input.DueKind, input.DueValue); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.ReasoningStatus, input.Rationale, err = normalizePurposeReasoning(input.ReasoningStatus, input.Rationale); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	if input.Confidence < 0 || input.Confidence > 1 {
		return CreatePurposeOpenLoopInput{}, errors.New("Purpose confidence must be between zero and one")
	}
	if input.OccurredAt <= 0 {
		return CreatePurposeOpenLoopInput{}, errors.New("Purpose occurrence time is required")
	}
	if input.Evidence, err = normalizePurposeEvidenceInputs(input.Evidence, input.OccurredAt); err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	for _, evidence := range input.Evidence {
		if evidence.Stance != PurposeStanceSupport {
			return CreatePurposeOpenLoopInput{}, errors.New("Purpose discovery requires supporting evidence")
		}
	}
	return input, nil
}

func normalizeAppendPurposeInput(input AppendPurposeOpenLoopEventInput) (AppendPurposeOpenLoopEventInput, error) {
	var err error
	if input.OpenLoopID, err = normalizePurposeID(input.OpenLoopID, "open-loop ID"); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.ExpectedEventID, err = normalizePurposeID(input.ExpectedEventID, "expected event ID"); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.IdempotencyKey, err = normalizePurposeID(input.IdempotencyKey, "idempotency key"); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.EventType, err = normalizePurposeEventType(input.EventType); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.EventType == PurposeEventDiscovered {
		return AppendPurposeOpenLoopEventInput{}, errors.New("Purpose discovery must create a new root")
	}
	if input.EventType == PurposeEventSuperseded {
		return AppendPurposeOpenLoopEventInput{}, errors.New(
			"Purpose supersession requires an atomic canonical relation",
		)
	}
	if input.Title, err = normalizePurposeText(input.Title, "title", purposeMaxTitleBytes, true); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.DesiredOutcome, err = normalizePurposeText(input.DesiredOutcome, "desired outcome", purposeMaxOutcomeBytes, true); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.Owner, err = normalizePurposeOwner(input.Owner); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.Counterparty, err = normalizePurposeText(input.Counterparty, "counterparty", purposeMaxFieldBytes, false); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.Scope, err = normalizePurposeText(input.Scope, "scope", purposeMaxScopeBytes, false); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.DueKind, input.DueValue, err = normalizePurposeDue(input.DueKind, input.DueValue); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.WaitingCondition, err = normalizePurposeText(input.WaitingCondition, "waiting condition", purposeMaxFieldBytes, false); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.ReasoningStatus, input.Rationale, err = normalizePurposeReasoning(input.ReasoningStatus, input.Rationale); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	if input.Confidence < 0 || input.Confidence > 1 {
		return AppendPurposeOpenLoopEventInput{}, errors.New("Purpose confidence must be between zero and one")
	}
	if input.OccurredAt <= 0 {
		return AppendPurposeOpenLoopEventInput{}, errors.New("Purpose occurrence time is required")
	}
	if input.Evidence, err = normalizePurposeEvidenceInputs(input.Evidence, input.OccurredAt); err != nil {
		return AppendPurposeOpenLoopEventInput{}, err
	}
	return input, nil
}

func buildPurposeEvidenceRecords(
	eventID string,
	createdAt int64,
	inputs []PurposeEvidenceInput,
) ([]PurposeEvidenceRecord, error) {
	items := make([]PurposeEvidenceRecord, 0, len(inputs))
	for _, input := range inputs {
		item := PurposeEvidenceRecord{
			ID: deterministicAgencyID(
				"purpose-open-loop-evidence",
				eventID+"\x00"+purposeEvidenceIdentity(input),
			),
			EventID: eventID, SourceKind: input.SourceKind,
			SourceID: input.SourceID, SourceVersion: input.SourceVersion,
			SourceObservedAt: input.SourceObservedAt, SourceHash: input.SourceHash,
			EvidenceClass: input.EvidenceClass, Stance: input.Stance,
			IndependenceKey: input.IndependenceKey, ExcerptHash: input.ExcerptHash,
			PrivacyDecision: input.PrivacyDecision, CreatedAt: createdAt,
		}
		var err error
		item.PayloadJSON, item.PayloadHash, err = purposeEvidencePayloadJSON(item)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func purposeEvidenceInputsMatch(records []PurposeEvidenceRecord, inputs []PurposeEvidenceInput) bool {
	if len(records) != len(inputs) {
		return false
	}
	byIdentity := make(map[string]PurposeEvidenceRecord, len(records))
	for _, record := range records {
		byIdentity[strings.Join([]string{
			record.SourceKind, record.SourceID, record.SourceVersion, record.Stance,
		}, "\x00")] = record
	}
	for _, input := range inputs {
		record, ok := byIdentity[purposeEvidenceIdentity(input)]
		if !ok || record.SourceObservedAt != input.SourceObservedAt ||
			record.SourceHash != input.SourceHash || record.EvidenceClass != input.EvidenceClass ||
			record.IndependenceKey != input.IndependenceKey ||
			record.ExcerptHash != input.ExcerptHash ||
			record.PrivacyDecision != input.PrivacyDecision {
			return false
		}
	}
	return true
}

func purposeEventEvidence(detail PurposeOpenLoopDetail, eventID string) []PurposeEvidenceRecord {
	result := make([]PurposeEvidenceRecord, 0)
	for _, item := range detail.Evidence {
		if item.EventID == eventID {
			result = append(result, item)
		}
	}
	return result
}

func purposeCreateMatches(
	detail PurposeOpenLoopDetail,
	event PurposeOpenLoopEventRecord,
	input CreatePurposeOpenLoopInput,
) bool {
	return detail.OpenLoop.Kind == input.Kind &&
		detail.OpenLoop.CanonicalKey == input.CanonicalKey &&
		event.EventType == PurposeEventDiscovered && event.Sequence == 1 &&
		event.Title == input.Title && event.DesiredOutcome == input.DesiredOutcome &&
		event.Owner == input.Owner && event.Counterparty == input.Counterparty &&
		event.Scope == input.Scope && event.DueKind == input.DueKind &&
		event.DueValue == input.DueValue && event.Rationale == input.Rationale &&
		event.ReasoningStatus == input.ReasoningStatus &&
		event.Confidence == input.Confidence && event.OccurredAt == input.OccurredAt &&
		purposeEvidenceInputsMatch(purposeEventEvidence(detail, event.ID), input.Evidence)
}

func purposeAppendMatches(
	detail PurposeOpenLoopDetail,
	event PurposeOpenLoopEventRecord,
	input AppendPurposeOpenLoopEventInput,
) bool {
	return event.OpenLoopID == input.OpenLoopID &&
		event.ExpectedEventID == input.ExpectedEventID &&
		event.EventType == input.EventType && event.Title == input.Title &&
		event.DesiredOutcome == input.DesiredOutcome && event.Owner == input.Owner &&
		event.Counterparty == input.Counterparty && event.Scope == input.Scope &&
		event.DueKind == input.DueKind && event.DueValue == input.DueValue &&
		event.WaitingCondition == input.WaitingCondition &&
		event.Rationale == input.Rationale && event.ReasoningStatus == input.ReasoningStatus &&
		event.Confidence == input.Confidence && event.OccurredAt == input.OccurredAt &&
		purposeEvidenceInputsMatch(purposeEventEvidence(detail, event.ID), input.Evidence)
}

func purposeEventByIdempotencyTx(tx *sql.Tx, key string) (PurposeOpenLoopEventRecord, error) {
	return scanPurposeEvent(tx.QueryRow(`SELECT `+purposeEventSelectColumns+`
		FROM purpose_open_loop_events_v1 WHERE idempotency_key = ?`, key))
}

func purposeRelationEventByIdempotencyTx(tx *sql.Tx, key string) (PurposeRelationEventRecord, error) {
	return scanPurposeRelationEvent(tx.QueryRow(`SELECT `+purposeRelationEventSelectColumns+`
		FROM purpose_open_loop_relation_events_v1 WHERE idempotency_key = ?`, key))
}

func insertPurposeRootTx(tx *sql.Tx, item PurposeOpenLoopRecord) error {
	_, err := tx.Exec(`INSERT INTO purpose_open_loops_v1 (
		id, idempotency_key, canonical_key, kind, contract_version,
		created_at, payload_json, payload_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.IdempotencyKey, item.CanonicalKey, item.Kind,
		item.ContractVersion, item.CreatedAt, item.PayloadJSON, item.PayloadHash,
	)
	return err
}

func insertPurposeEventTx(tx *sql.Tx, item PurposeOpenLoopEventRecord) error {
	_, err := tx.Exec(`INSERT INTO purpose_open_loop_events_v1 (
		id, open_loop_id, idempotency_key, sequence, event_type,
		expected_event_id, predecessor_event_id, title, desired_outcome, owner,
		counterparty, scope, due_kind, due_value, waiting_condition,
		lifecycle_state, support_state, confidence, rationale, reasoning_status,
		evidence_set_hash, occurred_at, created_at, payload_json, payload_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.OpenLoopID, item.IdempotencyKey, item.Sequence,
		item.EventType, item.ExpectedEventID, item.PredecessorEventID,
		item.Title, item.DesiredOutcome, item.Owner, item.Counterparty, item.Scope,
		item.DueKind, item.DueValue, item.WaitingCondition, item.LifecycleState,
		item.SupportState, item.Confidence, item.Rationale, item.ReasoningStatus,
		item.EvidenceSetHash, item.OccurredAt, item.CreatedAt,
		item.PayloadJSON, item.PayloadHash,
	)
	return err
}

func insertPurposeEvidenceTx(tx *sql.Tx, items []PurposeEvidenceRecord) error {
	for _, item := range items {
		if _, err := tx.Exec(`INSERT INTO purpose_open_loop_evidence_v1 (
			id, event_id, source_kind, source_id, source_version,
			source_observed_at, source_hash, evidence_class, stance,
			independence_key, excerpt_hash, privacy_decision, created_at,
			payload_json, payload_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ID, item.EventID, item.SourceKind, item.SourceID,
			item.SourceVersion, item.SourceObservedAt, item.SourceHash,
			item.EvidenceClass, item.Stance, item.IndependenceKey,
			item.ExcerptHash, item.PrivacyDecision, item.CreatedAt,
			item.PayloadJSON, item.PayloadHash,
		); err != nil {
			return err
		}
	}
	return nil
}

func purposeResultForEvent(detail PurposeOpenLoopDetail, eventID string) (PurposeOpenLoopMutationResult, error) {
	for _, event := range detail.Events {
		if event.ID == eventID {
			return PurposeOpenLoopMutationResult{
				OpenLoop: detail.OpenLoop, Event: event,
				Evidence: purposeEventEvidence(detail, event.ID),
			}, nil
		}
	}
	return PurposeOpenLoopMutationResult{}, fmt.Errorf("Purpose event %q is missing from validated detail", eventID)
}

func (store *PurposeMapStore) Create(
	input CreatePurposeOpenLoopInput,
) (PurposeOpenLoopMutationResult, bool, error) {
	if err := store.require(); err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	input, err := normalizeCreatePurposeInput(input)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	createdAt := time.Now().UnixMilli()
	if input.OccurredAt > createdAt {
		return PurposeOpenLoopMutationResult{}, false, errors.New("Purpose occurrence time cannot be in the future")
	}
	root := PurposeOpenLoopRecord{
		ID:             deterministicAgencyID("purpose-open-loop", input.IdempotencyKey),
		IdempotencyKey: input.IdempotencyKey, CanonicalKey: input.CanonicalKey,
		Kind: input.Kind, ContractVersion: PurposeOpenLoopContractVersion,
		CreatedAt: createdAt,
	}
	root.PayloadJSON, root.PayloadHash, err = purposeRootPayloadJSON(root)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	event := PurposeOpenLoopEventRecord{
		ID:         deterministicAgencyID("purpose-open-loop-event", root.ID+"\x00"+input.IdempotencyKey),
		OpenLoopID: root.ID, IdempotencyKey: input.IdempotencyKey,
		Sequence: 1, EventType: PurposeEventDiscovered,
		Title: input.Title, DesiredOutcome: input.DesiredOutcome, Owner: input.Owner,
		Counterparty: input.Counterparty, Scope: input.Scope,
		DueKind: input.DueKind, DueValue: input.DueValue,
		Confidence: input.Confidence, Rationale: input.Rationale,
		ReasoningStatus: input.ReasoningStatus, OccurredAt: input.OccurredAt,
		CreatedAt: createdAt,
	}
	evidence, err := buildPurposeEvidenceRecords(event.ID, createdAt, input.Evidence)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	if event.ReasoningStatus == PurposeReasoningExplicit &&
		!purposeHasEvidenceClass(evidence, PurposeEvidenceExplicitUser, PurposeEvidenceCorrection) {
		return PurposeOpenLoopMutationResult{}, false, errors.New("explicit Purpose reasoning requires explicit evidence")
	}
	support := purposeSupportFromEvidence(evidence)
	event.LifecycleState, event.SupportState, err = purposeEventState(
		event.EventType, "", support, evidence,
	)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	event.EvidenceSetHash, event.EvidenceIDs, err = purposeEvidenceSetHash(evidence)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	event.PayloadJSON, event.PayloadHash, err = purposeEventPayloadJSON(event)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}

	tx, err := store.db.Begin()
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	defer tx.Rollback()
	existingEvent, eventErr := purposeEventByIdempotencyTx(tx, input.IdempotencyKey)
	if eventErr == nil {
		details, loadErr := loadPurposeLedger(tx)
		if loadErr != nil {
			return PurposeOpenLoopMutationResult{}, false, loadErr
		}
		detail, findErr := findPurposeDetail(details, existingEvent.OpenLoopID)
		if findErr != nil {
			return PurposeOpenLoopMutationResult{}, false, findErr
		}
		if !purposeCreateMatches(detail, existingEvent, input) {
			return PurposeOpenLoopMutationResult{}, false, errors.New("Purpose creation idempotency conflict")
		}
		result, resultErr := purposeResultForEvent(detail, existingEvent.ID)
		return result, false, resultErr
	}
	if !errors.Is(eventErr, sql.ErrNoRows) {
		return PurposeOpenLoopMutationResult{}, false, eventErr
	}
	var collision string
	if scanErr := tx.QueryRow(`SELECT id FROM purpose_open_loops_v1 WHERE id = ? OR idempotency_key = ? LIMIT 1`, root.ID, root.IdempotencyKey).Scan(&collision); scanErr == nil {
		return PurposeOpenLoopMutationResult{}, false, errors.New("Purpose creation identity conflict")
	} else if !errors.Is(scanErr, sql.ErrNoRows) {
		return PurposeOpenLoopMutationResult{}, false, scanErr
	}
	if err := insertPurposeRootTx(tx, root); err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	if err := insertPurposeEventTx(tx, event); err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	if err := insertPurposeEvidenceTx(tx, evidence); err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	details, err := loadPurposeLedger(tx)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	detail, err := findPurposeDetail(details, root.ID)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	result, err := purposeResultForEvent(detail, event.ID)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	return result, true, nil
}

func (store *PurposeMapStore) AppendEvent(
	input AppendPurposeOpenLoopEventInput,
) (PurposeOpenLoopMutationResult, bool, error) {
	if err := store.require(); err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	input, err := normalizeAppendPurposeInput(input)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	createdAt := time.Now().UnixMilli()
	if input.OccurredAt > createdAt {
		return PurposeOpenLoopMutationResult{}, false, errors.New("Purpose occurrence time cannot be in the future")
	}
	tx, err := store.db.Begin()
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	defer tx.Rollback()
	existingEvent, eventErr := purposeEventByIdempotencyTx(tx, input.IdempotencyKey)
	if eventErr == nil {
		details, loadErr := loadPurposeLedger(tx)
		if loadErr != nil {
			return PurposeOpenLoopMutationResult{}, false, loadErr
		}
		detail, findErr := findPurposeDetail(details, existingEvent.OpenLoopID)
		if findErr != nil {
			return PurposeOpenLoopMutationResult{}, false, findErr
		}
		if !purposeAppendMatches(detail, existingEvent, input) {
			return PurposeOpenLoopMutationResult{}, false, errors.New("Purpose event idempotency conflict")
		}
		result, resultErr := purposeResultForEvent(detail, existingEvent.ID)
		return result, false, resultErr
	}
	if !errors.Is(eventErr, sql.ErrNoRows) {
		return PurposeOpenLoopMutationResult{}, false, eventErr
	}
	details, err := loadPurposeLedger(tx)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	detail, err := findPurposeDetail(details, input.OpenLoopID)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	previous := detail.Current
	if previous.ID != input.ExpectedEventID {
		return PurposeOpenLoopMutationResult{}, false, errors.New("Purpose event predecessor is stale")
	}
	if input.OccurredAt < previous.OccurredAt {
		return PurposeOpenLoopMutationResult{}, false, errors.New("Purpose event cannot predate its predecessor")
	}
	event := PurposeOpenLoopEventRecord{
		ID:         deterministicAgencyID("purpose-open-loop-event", input.OpenLoopID+"\x00"+input.IdempotencyKey),
		OpenLoopID: input.OpenLoopID, IdempotencyKey: input.IdempotencyKey,
		Sequence: previous.Sequence + 1, EventType: input.EventType,
		ExpectedEventID: previous.ID, PredecessorEventID: previous.ID,
		Title: input.Title, DesiredOutcome: input.DesiredOutcome, Owner: input.Owner,
		Counterparty: input.Counterparty, Scope: input.Scope,
		DueKind: input.DueKind, DueValue: input.DueValue,
		WaitingCondition: input.WaitingCondition, Confidence: input.Confidence,
		Rationale: input.Rationale, ReasoningStatus: input.ReasoningStatus,
		OccurredAt: input.OccurredAt, CreatedAt: createdAt,
	}
	currentEvidence, err := buildPurposeEvidenceRecords(event.ID, createdAt, input.Evidence)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	if event.ReasoningStatus == PurposeReasoningExplicit {
		carried := previous.ReasoningStatus == PurposeReasoningExplicit && previous.Rationale == event.Rationale
		if !carried && !purposeHasEvidenceClass(currentEvidence,
			PurposeEvidenceExplicitUser, PurposeEvidenceCorrection) {
			return PurposeOpenLoopMutationResult{}, false, errors.New("explicit Purpose reasoning requires explicit evidence")
		}
	}
	allSupportEvidence := append([]PurposeEvidenceRecord(nil), detail.Evidence...)
	allSupportEvidence = append(allSupportEvidence, currentEvidence...)
	support := purposeSupportFromEvidence(allSupportEvidence)
	event.LifecycleState, event.SupportState, err = purposeEventState(
		event.EventType, previous.LifecycleState, support, currentEvidence,
	)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	if event.LifecycleState == PurposeStateWaiting {
		if event.WaitingCondition == "" {
			return PurposeOpenLoopMutationResult{}, false, errors.New("Purpose waiting event requires a condition")
		}
	} else if event.WaitingCondition != "" {
		return PurposeOpenLoopMutationResult{}, false, errors.New("non-waiting Purpose event cannot retain a waiting condition")
	}
	event.EvidenceSetHash, event.EvidenceIDs, err = purposeEvidenceSetHash(currentEvidence)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	event.PayloadJSON, event.PayloadHash, err = purposeEventPayloadJSON(event)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	if err := insertPurposeEventTx(tx, event); err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	if err := insertPurposeEvidenceTx(tx, currentEvidence); err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	details, err = loadPurposeLedger(tx)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	detail, err = findPurposeDetail(details, input.OpenLoopID)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	result, err := purposeResultForEvent(detail, event.ID)
	if err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return PurposeOpenLoopMutationResult{}, false, err
	}
	return result, true, nil
}

func normalizePurposeRelationType(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case PurposeRelationDuplicateOf, PurposeRelationSupersedes,
		PurposeRelationDependsOn, PurposeRelationBlocks, PurposeRelationRelatedTo:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported Purpose relation type %q", value)
	}
}

func normalizeCreatePurposeRelation(input CreatePurposeRelationInput) (CreatePurposeRelationInput, error) {
	var err error
	if input.IdempotencyKey, err = normalizePurposeID(input.IdempotencyKey, "relation idempotency key"); err != nil {
		return CreatePurposeRelationInput{}, err
	}
	if input.SubjectID, err = normalizePurposeID(input.SubjectID, "relation subject ID"); err != nil {
		return CreatePurposeRelationInput{}, err
	}
	if input.ObjectID, err = normalizePurposeID(input.ObjectID, "relation object ID"); err != nil {
		return CreatePurposeRelationInput{}, err
	}
	if input.SubjectID == input.ObjectID {
		return CreatePurposeRelationInput{}, errors.New("Purpose relation cannot reference the same root twice")
	}
	if input.RelationType, err = normalizePurposeRelationType(input.RelationType); err != nil {
		return CreatePurposeRelationInput{}, err
	}
	if input.EvidenceEventID, err = normalizePurposeID(input.EvidenceEventID, "relation evidence event ID"); err != nil {
		return CreatePurposeRelationInput{}, err
	}
	if input.OccurredAt <= 0 {
		return CreatePurposeRelationInput{}, errors.New("Purpose relation occurrence time is required")
	}
	return input, nil
}

func normalizeRetractPurposeRelation(input RetractPurposeRelationInput) (RetractPurposeRelationInput, error) {
	var err error
	if input.RelationID, err = normalizePurposeID(input.RelationID, "relation ID"); err != nil {
		return RetractPurposeRelationInput{}, err
	}
	if input.ExpectedEventID, err = normalizePurposeID(input.ExpectedEventID, "expected relation event ID"); err != nil {
		return RetractPurposeRelationInput{}, err
	}
	if input.IdempotencyKey, err = normalizePurposeID(input.IdempotencyKey, "relation idempotency key"); err != nil {
		return RetractPurposeRelationInput{}, err
	}
	if input.EvidenceEventID, err = normalizePurposeID(input.EvidenceEventID, "relation evidence event ID"); err != nil {
		return RetractPurposeRelationInput{}, err
	}
	if input.Reason, err = normalizePurposeText(input.Reason, "relation retraction reason", purposeMaxReasonBytes, true); err != nil {
		return RetractPurposeRelationInput{}, err
	}
	if input.OccurredAt <= 0 {
		return RetractPurposeRelationInput{}, errors.New("Purpose relation occurrence time is required")
	}
	return input, nil
}

func purposeFindRelation(details []PurposeOpenLoopDetail, relationID string) (PurposeRelationDetail, error) {
	seen := make(map[string]struct{})
	for _, detail := range details {
		for _, relation := range detail.Relations {
			if _, ok := seen[relation.Relation.ID]; ok {
				continue
			}
			seen[relation.Relation.ID] = struct{}{}
			if relation.Relation.ID == relationID {
				return relation, nil
			}
		}
	}
	return PurposeRelationDetail{}, fmt.Errorf("Purpose relation %q does not exist", relationID)
}

func purposeFindEvent(details []PurposeOpenLoopDetail, eventID string) (PurposeOpenLoopEventRecord, error) {
	for _, detail := range details {
		for _, event := range detail.Events {
			if event.ID == eventID {
				return event, nil
			}
		}
	}
	return PurposeOpenLoopEventRecord{}, fmt.Errorf("Purpose evidence event %q does not exist", eventID)
}

func insertPurposeRelationTx(tx *sql.Tx, item PurposeRelationRecord) error {
	_, err := tx.Exec(`INSERT INTO purpose_open_loop_relations_v1 (
		id, idempotency_key, subject_id, object_id, relation_type,
		contract_version, created_at, payload_json, payload_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.IdempotencyKey, item.SubjectID, item.ObjectID,
		item.RelationType, item.ContractVersion, item.CreatedAt,
		item.PayloadJSON, item.PayloadHash,
	)
	return err
}

func insertPurposeRelationEventTx(tx *sql.Tx, item PurposeRelationEventRecord) error {
	_, err := tx.Exec(`INSERT INTO purpose_open_loop_relation_events_v1 (
		id, relation_id, idempotency_key, sequence, event_type,
		expected_event_id, evidence_event_id, reason, occurred_at, created_at,
		payload_json, payload_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.RelationID, item.IdempotencyKey, item.Sequence,
		item.EventType, item.ExpectedEventID, item.EvidenceEventID,
		item.Reason, item.OccurredAt, item.CreatedAt,
		item.PayloadJSON, item.PayloadHash,
	)
	return err
}

func purposeRelationResult(
	details []PurposeOpenLoopDetail,
	relationID, eventID string,
) (PurposeRelationMutationResult, error) {
	detail, err := purposeFindRelation(details, relationID)
	if err != nil {
		return PurposeRelationMutationResult{}, err
	}
	for _, event := range detail.Events {
		if event.ID == eventID {
			return PurposeRelationMutationResult{Relation: detail.Relation, Event: event}, nil
		}
	}
	return PurposeRelationMutationResult{}, fmt.Errorf("Purpose relation event %q is missing", eventID)
}

func (store *PurposeMapStore) CreateRelation(
	input CreatePurposeRelationInput,
) (PurposeRelationMutationResult, bool, error) {
	if err := store.require(); err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	input, err := normalizeCreatePurposeRelation(input)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	createdAt := time.Now().UnixMilli()
	if input.OccurredAt > createdAt {
		return PurposeRelationMutationResult{}, false, errors.New("Purpose relation cannot occur in the future")
	}
	tx, err := store.db.Begin()
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	defer tx.Rollback()
	existingEvent, eventErr := purposeRelationEventByIdempotencyTx(tx, input.IdempotencyKey)
	if eventErr == nil {
		details, loadErr := loadPurposeLedger(tx)
		if loadErr != nil {
			return PurposeRelationMutationResult{}, false, loadErr
		}
		relation, findErr := purposeFindRelation(details, existingEvent.RelationID)
		if findErr != nil {
			return PurposeRelationMutationResult{}, false, findErr
		}
		if relation.Relation.SubjectID != input.SubjectID ||
			relation.Relation.ObjectID != input.ObjectID ||
			relation.Relation.RelationType != input.RelationType ||
			existingEvent.EventType != PurposeRelationEventAsserted ||
			existingEvent.EvidenceEventID != input.EvidenceEventID ||
			existingEvent.OccurredAt != input.OccurredAt {
			return PurposeRelationMutationResult{}, false, errors.New("Purpose relation idempotency conflict")
		}
		result, resultErr := purposeRelationResult(details, relation.Relation.ID, existingEvent.ID)
		return result, false, resultErr
	}
	if !errors.Is(eventErr, sql.ErrNoRows) {
		return PurposeRelationMutationResult{}, false, eventErr
	}
	details, err := loadPurposeLedger(tx)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if _, err := findPurposeDetail(details, input.SubjectID); err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if _, err := findPurposeDetail(details, input.ObjectID); err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	evidence, err := purposeFindEvent(details, input.EvidenceEventID)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if evidence.OpenLoopID != input.SubjectID && evidence.OpenLoopID != input.ObjectID {
		return PurposeRelationMutationResult{}, false, errors.New("Purpose relation evidence belongs to an unrelated root")
	}
	if evidence.OccurredAt > input.OccurredAt {
		return PurposeRelationMutationResult{}, false, errors.New("Purpose relation cannot predate its evidence")
	}
	relation := PurposeRelationRecord{
		ID:             deterministicAgencyID("purpose-open-loop-relation", input.IdempotencyKey),
		IdempotencyKey: input.IdempotencyKey, SubjectID: input.SubjectID,
		ObjectID: input.ObjectID, RelationType: input.RelationType,
		ContractVersion: PurposeRelationContractVersion, CreatedAt: createdAt,
	}
	relation.PayloadJSON, relation.PayloadHash, err = purposeRelationPayloadJSON(relation)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	event := PurposeRelationEventRecord{
		ID:         deterministicAgencyID("purpose-open-loop-relation-event", relation.ID+"\x00"+input.IdempotencyKey),
		RelationID: relation.ID, IdempotencyKey: input.IdempotencyKey,
		Sequence: 1, EventType: PurposeRelationEventAsserted,
		EvidenceEventID: input.EvidenceEventID, OccurredAt: input.OccurredAt,
		CreatedAt: createdAt,
	}
	event.PayloadJSON, event.PayloadHash, err = purposeRelationEventPayloadJSON(event)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if err := insertPurposeRelationTx(tx, relation); err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if err := insertPurposeRelationEventTx(tx, event); err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	details, err = loadPurposeLedger(tx)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	result, err := purposeRelationResult(details, relation.ID, event.ID)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	return result, true, nil
}

func (store *PurposeMapStore) RetractRelation(
	input RetractPurposeRelationInput,
) (PurposeRelationMutationResult, bool, error) {
	if err := store.require(); err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	input, err := normalizeRetractPurposeRelation(input)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	createdAt := time.Now().UnixMilli()
	if input.OccurredAt > createdAt {
		return PurposeRelationMutationResult{}, false, errors.New("Purpose relation cannot occur in the future")
	}
	tx, err := store.db.Begin()
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	defer tx.Rollback()
	existingEvent, eventErr := purposeRelationEventByIdempotencyTx(tx, input.IdempotencyKey)
	if eventErr == nil {
		details, loadErr := loadPurposeLedger(tx)
		if loadErr != nil {
			return PurposeRelationMutationResult{}, false, loadErr
		}
		if existingEvent.RelationID != input.RelationID ||
			existingEvent.ExpectedEventID != input.ExpectedEventID ||
			existingEvent.EventType != PurposeRelationEventRetracted ||
			existingEvent.EvidenceEventID != input.EvidenceEventID ||
			existingEvent.Reason != input.Reason || existingEvent.OccurredAt != input.OccurredAt {
			return PurposeRelationMutationResult{}, false, errors.New("Purpose relation retraction idempotency conflict")
		}
		result, resultErr := purposeRelationResult(details, input.RelationID, existingEvent.ID)
		return result, false, resultErr
	}
	if !errors.Is(eventErr, sql.ErrNoRows) {
		return PurposeRelationMutationResult{}, false, eventErr
	}
	details, err := loadPurposeLedger(tx)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	relation, err := purposeFindRelation(details, input.RelationID)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if relation.Current.ID != input.ExpectedEventID ||
		relation.Current.EventType != PurposeRelationEventAsserted {
		return PurposeRelationMutationResult{}, false, errors.New("Purpose relation predecessor is stale")
	}
	evidence, err := purposeFindEvent(details, input.EvidenceEventID)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if evidence.OpenLoopID != relation.Relation.SubjectID &&
		evidence.OpenLoopID != relation.Relation.ObjectID {
		return PurposeRelationMutationResult{}, false, errors.New("Purpose relation evidence belongs to an unrelated root")
	}
	if input.OccurredAt < relation.Current.OccurredAt || input.OccurredAt < evidence.OccurredAt {
		return PurposeRelationMutationResult{}, false, errors.New("Purpose relation retraction predates current evidence")
	}
	event := PurposeRelationEventRecord{
		ID:         deterministicAgencyID("purpose-open-loop-relation-event", input.RelationID+"\x00"+input.IdempotencyKey),
		RelationID: input.RelationID, IdempotencyKey: input.IdempotencyKey,
		Sequence:        relation.Current.Sequence + 1,
		EventType:       PurposeRelationEventRetracted,
		ExpectedEventID: relation.Current.ID, EvidenceEventID: input.EvidenceEventID,
		Reason: input.Reason, OccurredAt: input.OccurredAt, CreatedAt: createdAt,
	}
	event.PayloadJSON, event.PayloadHash, err = purposeRelationEventPayloadJSON(event)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if err := insertPurposeRelationEventTx(tx, event); err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	details, err = loadPurposeLedger(tx)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	result, err := purposeRelationResult(details, input.RelationID, event.ID)
	if err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return PurposeRelationMutationResult{}, false, err
	}
	return result, true, nil
}
