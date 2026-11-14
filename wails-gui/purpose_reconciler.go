package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	purposeReconcileReceiptPrefix  = "purpose-reconcile:"
	purposeReconcileEventPrefix    = "purpose-reconcile-event:"
	purposeReconcileRelationPrefix = "purpose-reconcile-relation:"
)

func purposeReconciliationEventKey(hypothesisID string) string {
	return purposeReconcileEventPrefix + hypothesisID
}

func purposeReconciliationRelationKey(hypothesisID string) string {
	return purposeReconcileRelationPrefix + hypothesisID
}

func purposeExtractionSourcesEqual(
	left PurposeExtractionSource,
	right PurposeExtractionSource,
) bool {
	leftJSON, _, leftErr := agencyStructPayload(left)
	rightJSON, _, rightErr := agencyStructPayload(right)
	return leftErr == nil && rightErr == nil && leftJSON == rightJSON
}

// purposeFrozenSourceDriftError reports that a run's evidence changed or was
// removed after the model read it. The run can never reconcile again: its
// frozen input no longer matches what the user has.
type purposeFrozenSourceDriftError struct{ ref string }

func (err purposeFrozenSourceDriftError) Error() string {
	return fmt.Sprintf("Purpose frozen source %q drifted or is unavailable", err.ref)
}

func (runtime *PurposeExtractionRuntime) validateFrozenSources(
	detail PurposeExtractionRunDetail,
) error {
	current, _, err := gatherPurposeExtractionInput(
		runtime.activity,
		PurposeExtractionRequest{
			IdempotencyKey: detail.Run.IdempotencyKey,
			CutoffAt:       detail.Run.CutoffAt,
			Query:          detail.Input.Query,
		},
	)
	if err != nil {
		return err
	}
	currentByRef := purposeSourcesByRef(current)
	for _, frozen := range detail.Input.Sources {
		candidate, ok := currentByRef[frozen.Ref]
		if !ok || !purposeExtractionSourcesEqual(frozen, candidate) {
			return purposeFrozenSourceDriftError{ref: frozen.Ref}
		}
	}
	return nil
}

func purposeActiveDuplicateTarget(detail PurposeOpenLoopDetail) (string, error) {
	target := ""
	for _, relation := range detail.Relations {
		if relation.Relation.SubjectID != detail.OpenLoop.ID ||
			relation.Relation.RelationType != PurposeRelationDuplicateOf ||
			relation.Current.EventType != PurposeRelationEventAsserted {
			continue
		}
		if target != "" && target != relation.Relation.ObjectID {
			return "", fmt.Errorf("Purpose root %q has ambiguous duplicate targets", detail.OpenLoop.ID)
		}
		target = relation.Relation.ObjectID
	}
	return target, nil
}

func purposeCanonicalDetail(
	details []PurposeOpenLoopDetail,
	canonicalKey string,
) (PurposeOpenLoopDetail, bool, error) {
	byID := make(map[string]PurposeOpenLoopDetail, len(details))
	for _, detail := range details {
		byID[detail.OpenLoop.ID] = detail
	}
	resolved := make(map[string]PurposeOpenLoopDetail)
	for _, detail := range details {
		if detail.OpenLoop.CanonicalKey != canonicalKey {
			continue
		}
		current := detail
		seen := make(map[string]struct{})
		for {
			if _, cycle := seen[current.OpenLoop.ID]; cycle {
				return PurposeOpenLoopDetail{}, false, errors.New("Purpose duplicate relation cycle")
			}
			seen[current.OpenLoop.ID] = struct{}{}
			targetID, err := purposeActiveDuplicateTarget(current)
			if err != nil {
				return PurposeOpenLoopDetail{}, false, err
			}
			if targetID == "" {
				break
			}
			target, ok := byID[targetID]
			if !ok {
				return PurposeOpenLoopDetail{}, false,
					fmt.Errorf("Purpose duplicate target %q is missing", targetID)
			}
			current = target
		}
		resolved[current.OpenLoop.ID] = current
	}
	if len(resolved) == 0 {
		return PurposeOpenLoopDetail{}, false, nil
	}
	if len(resolved) != 1 {
		return PurposeOpenLoopDetail{}, false,
			fmt.Errorf("Purpose canonical key %q is ambiguous", canonicalKey)
	}
	for _, detail := range resolved {
		return detail, true, nil
	}
	return PurposeOpenLoopDetail{}, false, nil
}

func purposeNormalizedOutcome(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func purposeHypothesisHasSourceOverlap(
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	target PurposeOpenLoopDetail,
) bool {
	existing := make(map[string]struct{}, len(target.Evidence))
	for _, item := range target.Evidence {
		existing[item.SourceKind+"\x00"+item.SourceID] = struct{}{}
	}
	for _, ref := range hypothesis.EvidenceRefs {
		source, ok := sources[ref]
		if !ok {
			continue
		}
		if _, ok := existing[source.SourceKind+"\x00"+source.SourceID]; ok {
			return true
		}
	}
	return false
}

func purposeHypothesisSourceTime(
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
) (int64, error) {
	var occurredAt int64
	for _, ref := range hypothesis.EvidenceRefs {
		source, ok := sources[ref]
		if !ok {
			return 0, fmt.Errorf("Purpose source %q is unavailable", ref)
		}
		if source.SourceObservedAt > occurredAt {
			occurredAt = source.SourceObservedAt
		}
	}
	if occurredAt <= 0 {
		return 0, errors.New("Purpose reconciliation has no source occurrence time")
	}
	return occurredAt, nil
}

func purposeEvidenceStanceForOperation(operation string) (string, error) {
	switch operation {
	case PurposeOperationSupport:
		return PurposeStanceSupport, nil
	case PurposeOperationUpdate:
		return PurposeStanceUpdate, nil
	case PurposeOperationWait:
		return PurposeStanceWait, nil
	case PurposeOperationResolve:
		return PurposeStanceResolve, nil
	case PurposeOperationContradict:
		return PurposeStanceContradict, nil
	case PurposeOperationDismiss:
		return PurposeStanceDismiss, nil
	default:
		return "", fmt.Errorf("unsupported Purpose operation %q", operation)
	}
}

func purposeEvidenceInputsForHypothesis(
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	stance string,
) ([]PurposeEvidenceInput, error) {
	items := make([]PurposeEvidenceInput, 0, len(hypothesis.EvidenceRefs))
	for _, ref := range hypothesis.EvidenceRefs {
		source, ok := sources[ref]
		if !ok {
			return nil, fmt.Errorf("Purpose source %q is unavailable", ref)
		}
		items = append(items, PurposeEvidenceInput{
			SourceKind:       source.SourceKind,
			SourceID:         source.SourceID,
			SourceVersion:    source.SourceVersion,
			SourceObservedAt: source.SourceObservedAt,
			SourceHash:       source.SourceHash,
			EvidenceClass:    source.EvidenceClass,
			Stance:           stance,
			IndependenceKey:  source.IndependenceKey,
			ExcerptHash:      source.PromptContentHash,
			PrivacyDecision:  source.PrivacyDecision,
		})
	}
	return items, nil
}

func purposeHypothesisHasNovelEvidence(
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	target PurposeOpenLoopDetail,
) bool {
	existing := make(map[string]struct{}, len(target.Evidence))
	for _, item := range target.Evidence {
		existing[strings.Join([]string{
			item.SourceKind,
			item.SourceID,
			item.SourceVersion,
			item.SourceHash,
		}, "\x00")] = struct{}{}
	}
	for _, ref := range hypothesis.EvidenceRefs {
		source, ok := sources[ref]
		if !ok {
			continue
		}
		key := strings.Join([]string{
			source.SourceKind,
			source.SourceID,
			source.SourceVersion,
			source.SourceHash,
		}, "\x00")
		if _, ok := existing[key]; !ok {
			return true
		}
	}
	return false
}

// purposeClosedMatch finds a Purpose the user forgot or marked done that a
// new hypothesis, under another canonical key, would bring back as a new
// goal. The dismissed or resolved event stays in the ledger as the note.
// A forgotten Purpose comes back neither from the sources it already has nor
// in the same words (one aim containing the other, whole words). A done one
// is not made again from its own sources in its own words; seen in new
// sources it may be, because the same words can be next month's payroll. A
// clearly new mention, with new sources and other words, may always become a
// Purpose of its own.
func purposeClosedMatch(
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	details []PurposeOpenLoopDetail,
) (PurposeOpenLoopDetail, bool) {
	for _, detail := range details {
		state := detail.Current.LifecycleState
		if state != PurposeStateDismissed && state != PurposeStateResolved {
			continue
		}
		known := purposeHypothesisCitesOnlyKnownSources(hypothesis, sources, detail)
		same := purposeHypothesisNamesSameAim(hypothesis, detail)
		if state == PurposeStateDismissed && (known || same) || known && same {
			return detail, true
		}
	}
	return PurposeOpenLoopDetail{}, false
}

// purposeSameAimMinRunes keeps a short word ("Email", "Call") from standing
// for every aim that mentions it.
const purposeSameAimMinRunes = 6

// purposeHypothesisNamesSameAim reports a hypothesis whose title or outcome
// equals, contains or is contained in a title or outcome the Purpose ever had.
func purposeHypothesisNamesSameAim(hypothesis PurposeExtractionHypothesisRecord, detail PurposeOpenLoopDetail) bool {
	for _, text := range []string{hypothesis.Title, hypothesis.DesiredOutcome} {
		folded := purposeMailTextFold(text)
		if folded == "" {
			continue
		}
		for _, event := range detail.Events {
			for _, known := range []string{event.Title, event.DesiredOutcome} {
				known = purposeMailTextFold(known)
				if known == "" {
					continue
				}
				if known == folded ||
					utf8.RuneCountInString(known) >= purposeSameAimMinRunes && purposeMailContains(folded, known) ||
					utf8.RuneCountInString(folded) >= purposeSameAimMinRunes && purposeMailContains(known, folded) {
					return true
				}
			}
		}
	}
	return false
}

// purposeHypothesisCitesOnlyKnownSources reports a hypothesis that cites at
// least one source and only source versions already linked to target.
func purposeHypothesisCitesOnlyKnownSources(
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	target PurposeOpenLoopDetail,
) bool {
	cited := false
	for _, ref := range hypothesis.EvidenceRefs {
		if _, ok := sources[ref]; ok {
			cited = true
			break
		}
	}
	return cited && !purposeHypothesisHasNovelEvidence(hypothesis, sources, target)
}

func purposeHypothesisEvidenceClasses(
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
) (bool, int) {
	hasExplicit := false
	independent := make(map[string]struct{})
	for _, ref := range hypothesis.EvidenceRefs {
		source, ok := sources[ref]
		if !ok {
			continue
		}
		if source.EvidenceClass == PurposeEvidenceExplicitUser ||
			source.EvidenceClass == PurposeEvidenceCorrection {
			hasExplicit = true
		}
		if source.EvidenceClass == PurposeEvidenceObservedUser ||
			source.EvidenceClass == PurposeEvidenceEvaluatedProjection {
			independent[source.IndependenceKey] = struct{}{}
		}
	}
	return hasExplicit, len(independent)
}

func purposeOperationEventType(operation string) (string, string, error) {
	switch operation {
	case PurposeOperationSupport:
		return PurposeEventReinforced, PurposeDispositionReinforced, nil
	case PurposeOperationUpdate:
		return PurposeEventRevised, PurposeDispositionRevised, nil
	case PurposeOperationWait:
		return PurposeEventWaiting, PurposeDispositionWaiting, nil
	case PurposeOperationResolve:
		return PurposeEventResolved, PurposeDispositionResolved, nil
	case PurposeOperationContradict:
		return PurposeEventContradicted, PurposeDispositionContradicted, nil
	case PurposeOperationDismiss:
		return PurposeEventDismissed, PurposeDispositionDismissed, nil
	default:
		return "", "", fmt.Errorf("unsupported Purpose operation %q", operation)
	}
}

func purposeOperationAllowed(
	hypothesis PurposeExtractionHypothesisRecord,
	target PurposeOpenLoopDetail,
	sources map[string]PurposeExtractionSource,
) error {
	state := target.Current.LifecycleState
	if state == PurposeStateResolved || state == PurposeStateContradicted ||
		state == PurposeStateDismissed || state == PurposeStateSuperseded {
		return errors.New("terminal Purpose roots are not reopened by automatic extraction")
	}
	hasExplicit, independent := purposeHypothesisEvidenceClasses(hypothesis, sources)
	switch hypothesis.Operation {
	case PurposeOperationSupport, PurposeOperationUpdate:
		if state != PurposeStateCandidate && state != PurposeStateActive &&
			state != PurposeStateWaiting {
			return errors.New("Purpose support target is not open")
		}
	case PurposeOperationWait:
		if state != PurposeStateActive || target.Current.SupportState != PurposeSupportSupported {
			return errors.New("only a supported active Purpose root may become waiting")
		}
	case PurposeOperationResolve:
		if state != PurposeStateActive && state != PurposeStateWaiting {
			return errors.New("Purpose resolution target is not active or waiting")
		}
		if !hasExplicit {
			return errors.New("automatic Purpose resolution requires explicit completion evidence")
		}
	case PurposeOperationContradict:
		if state != PurposeStateCandidate && state != PurposeStateActive &&
			state != PurposeStateWaiting {
			return errors.New("Purpose contradiction target is not open")
		}
		if !hasExplicit && independent < 2 {
			return errors.New("Purpose contradiction requires explicit or independent evidence")
		}
	case PurposeOperationDismiss:
		if state != PurposeStateCandidate && state != PurposeStateActive &&
			state != PurposeStateWaiting {
			return errors.New("Purpose dismissal target is not open")
		}
		if !hasExplicit {
			return errors.New("Purpose dismissal requires explicit evidence")
		}
	default:
		return fmt.Errorf("unsupported Purpose operation %q", hypothesis.Operation)
	}
	return nil
}

func purposeAppendInputForHypothesis(
	hypothesis PurposeExtractionHypothesisRecord,
	target PurposeOpenLoopDetail,
	sources map[string]PurposeExtractionSource,
) (AppendPurposeOpenLoopEventInput, string, error) {
	if err := purposeOperationAllowed(hypothesis, target, sources); err != nil {
		return AppendPurposeOpenLoopEventInput{}, "", err
	}
	eventType, disposition, err := purposeOperationEventType(hypothesis.Operation)
	if err != nil {
		return AppendPurposeOpenLoopEventInput{}, "", err
	}
	stance, err := purposeEvidenceStanceForOperation(hypothesis.Operation)
	if err != nil {
		return AppendPurposeOpenLoopEventInput{}, "", err
	}
	evidence, err := purposeEvidenceInputsForHypothesis(hypothesis, sources, stance)
	if err != nil {
		return AppendPurposeOpenLoopEventInput{}, "", err
	}
	occurredAt, err := purposeHypothesisSourceTime(hypothesis, sources)
	if err != nil {
		return AppendPurposeOpenLoopEventInput{}, "", err
	}
	if occurredAt < target.Current.OccurredAt {
		occurredAt = target.Current.OccurredAt
	}
	current := purposeCurrentLedgerEvent(target)
	input := AppendPurposeOpenLoopEventInput{
		OpenLoopID:       target.OpenLoop.ID,
		ExpectedEventID:  current.ID,
		IdempotencyKey:   purposeReconciliationEventKey(hypothesis.ID),
		EventType:        eventType,
		Title:            current.Title,
		DesiredOutcome:   current.DesiredOutcome,
		Owner:            current.Owner,
		Counterparty:     current.Counterparty,
		Scope:            current.Scope,
		DueKind:          current.DueKind,
		DueValue:         current.DueValue,
		WaitingCondition: current.WaitingCondition,
		Rationale:        current.Rationale,
		ReasoningStatus:  current.ReasoningStatus,
		Confidence:       current.Confidence,
		OccurredAt:       occurredAt,
		Evidence:         evidence,
	}
	if hypothesis.Confidence > input.Confidence {
		input.Confidence = hypothesis.Confidence
	}
	if hypothesis.Operation == PurposeOperationUpdate {
		input.Title = hypothesis.Title
		input.DesiredOutcome = hypothesis.DesiredOutcome
		input.Owner = hypothesis.Owner
		input.Counterparty = hypothesis.Counterparty
		input.Scope = hypothesis.Scope
		input.DueKind = hypothesis.DueKind
		input.DueValue = hypothesis.DueValue
		if hypothesis.ReasoningStatus == PurposeReasoningExplicit ||
			current.ReasoningStatus != PurposeReasoningExplicit {
			input.ReasoningStatus = hypothesis.ReasoningStatus
			input.Rationale = hypothesis.Rationale
		}
	}
	switch hypothesis.Operation {
	case PurposeOperationWait:
		input.WaitingCondition = hypothesis.WaitingCondition
	case PurposeOperationResolve, PurposeOperationContradict, PurposeOperationDismiss:
		input.WaitingCondition = ""
	}
	return input, disposition, nil
}

func purposeCreateInputForHypothesis(
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
) (CreatePurposeOpenLoopInput, error) {
	evidence, err := purposeEvidenceInputsForHypothesis(
		hypothesis,
		sources,
		PurposeStanceSupport,
	)
	if err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	occurredAt, err := purposeHypothesisSourceTime(hypothesis, sources)
	if err != nil {
		return CreatePurposeOpenLoopInput{}, err
	}
	return CreatePurposeOpenLoopInput{
		IdempotencyKey:  purposeReconciliationEventKey(hypothesis.ID),
		CanonicalKey:    hypothesis.CanonicalKey,
		Kind:            hypothesis.Kind,
		Title:           hypothesis.Title,
		DesiredOutcome:  hypothesis.DesiredOutcome,
		Owner:           hypothesis.Owner,
		Counterparty:    hypothesis.Counterparty,
		Scope:           hypothesis.Scope,
		DueKind:         hypothesis.DueKind,
		DueValue:        hypothesis.DueValue,
		Rationale:       hypothesis.Rationale,
		ReasoningStatus: hypothesis.ReasoningStatus,
		Confidence:      hypothesis.Confidence,
		OccurredAt:      occurredAt,
		Evidence:        evidence,
	}, nil
}

func purposeFindEventByIdempotency(
	details []PurposeOpenLoopDetail,
	key string,
) (PurposeOpenLoopDetail, PurposeOpenLoopEventRecord, bool, error) {
	var foundDetail PurposeOpenLoopDetail
	var foundEvent PurposeOpenLoopEventRecord
	found := false
	for _, detail := range details {
		for _, event := range detail.Events {
			if event.IdempotencyKey != key {
				continue
			}
			if found {
				return PurposeOpenLoopDetail{}, PurposeOpenLoopEventRecord{}, false,
					errors.New("duplicate Purpose reconciliation event identity")
			}
			foundDetail = detail
			foundEvent = event
			found = true
		}
	}
	return foundDetail, foundEvent, found, nil
}

func purposeFindActiveRelation(
	details []PurposeOpenLoopDetail,
	subjectID, objectID, relationType string,
) (PurposeRelationDetail, bool, error) {
	var found PurposeRelationDetail
	seen := false
	for _, detail := range details {
		if detail.OpenLoop.ID != subjectID {
			continue
		}
		for _, relation := range detail.Relations {
			if relation.Relation.SubjectID != subjectID ||
				relation.Relation.ObjectID != objectID ||
				relation.Relation.RelationType != relationType ||
				relation.Current.EventType != PurposeRelationEventAsserted {
				continue
			}
			if seen && found.Relation.ID != relation.Relation.ID {
				return PurposeRelationDetail{}, false, errors.New("ambiguous Purpose relation")
			}
			found = relation
			seen = true
		}
	}
	return found, seen, nil
}

func purposeFindRelationByIdempotency(
	details []PurposeOpenLoopDetail,
	key string,
) (PurposeRelationDetail, bool, error) {
	var found PurposeRelationDetail
	seen := false
	seenRelations := make(map[string]struct{})
	for _, detail := range details {
		for _, relation := range detail.Relations {
			if _, ok := seenRelations[relation.Relation.ID]; ok {
				continue
			}
			seenRelations[relation.Relation.ID] = struct{}{}
			for _, event := range relation.Events {
				if event.IdempotencyKey != key {
					continue
				}
				if seen {
					return PurposeRelationDetail{}, false, errors.New("duplicate Purpose relation identity")
				}
				found = relation
				seen = true
			}
		}
	}
	return found, seen, nil
}

func purposeEventContainsSourceOverlap(
	detail PurposeOpenLoopDetail,
	eventID string,
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
) bool {
	for _, evidence := range purposeEventEvidence(detail, eventID) {
		for _, ref := range hypothesis.EvidenceRefs {
			source, ok := sources[ref]
			if ok && evidence.SourceKind == source.SourceKind &&
				evidence.SourceID == source.SourceID {
				return true
			}
		}
	}
	return false
}

func purposeLatestOverlappingEvent(
	detail PurposeOpenLoopDetail,
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
) (PurposeOpenLoopEventRecord, bool) {
	for index := len(detail.Events) - 1; index >= 0; index-- {
		event := detail.Events[index]
		if purposeEventContainsSourceOverlap(detail, event.ID, hypothesis, sources) {
			return event, true
		}
	}
	return PurposeOpenLoopEventRecord{}, false
}

func purposeNewReceipt(
	run PurposeExtractionRunRecord,
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	disposition, openLoopID, eventID, relationID, reason string,
	occurredAt int64,
) (PurposeReconciliationReceiptRecord, error) {
	if occurredAt <= 0 {
		var err error
		occurredAt, err = purposeHypothesisSourceTime(hypothesis, sources)
		if err != nil {
			return PurposeReconciliationReceiptRecord{}, err
		}
	}
	createdAt := time.Now().UnixMilli()
	if createdAt < occurredAt {
		return PurposeReconciliationReceiptRecord{}, errors.New("Purpose receipt occurrence time is in the future")
	}
	idempotencyKey := purposeReconcileReceiptPrefix + hypothesis.ID
	sourceSetHash, err := purposeSourceSetHash(hypothesis, sources)
	if err != nil {
		return PurposeReconciliationReceiptRecord{}, err
	}
	item := PurposeReconciliationReceiptRecord{
		ID:             deterministicAgencyID("purpose-reconciliation-receipt", idempotencyKey),
		IdempotencyKey: idempotencyKey,
		RunID:          run.ID,
		HypothesisID:   hypothesis.ID,
		Disposition:    disposition,
		OpenLoopID:     openLoopID,
		EventID:        eventID,
		RelationID:     relationID,
		Reason:         strings.TrimSpace(reason),
		SourceSetHash:  sourceSetHash,
		OccurredAt:     occurredAt,
		CreatedAt:      createdAt,
	}
	item.PayloadJSON, item.PayloadHash, err = purposeReceiptPayloadJSON(item)
	if err != nil {
		return PurposeReconciliationReceiptRecord{}, err
	}
	if err := validatePurposeReceipt(item, run, hypothesis, PurposeExtractionInput{
		Version:  PurposeExtractionInputVersion,
		CutoffAt: run.CutoffAt,
		Sources:  purposeSourcesForHypothesis(hypothesis, sources),
	}); err != nil {
		return PurposeReconciliationReceiptRecord{}, err
	}
	return item, nil
}

func purposeSourcesForHypothesis(
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
) []PurposeExtractionSource {
	items := make([]PurposeExtractionSource, 0, len(hypothesis.EvidenceRefs))
	for _, ref := range hypothesis.EvidenceRefs {
		if source, ok := sources[ref]; ok {
			items = append(items, source)
		}
	}
	return items
}

func insertPurposeReceipt(
	db *sql.DB,
	item PurposeReconciliationReceiptRecord,
) (PurposeReconciliationReceiptRecord, bool, error) {
	existing, err := scanPurposeReceipt(db.QueryRow(`SELECT `+
		purposeReceiptSelectColumns+` FROM purpose_reconciliation_receipts_v1 WHERE idempotency_key = ?`,
		item.IdempotencyKey))
	if err == nil {
		if existing.PayloadHash != item.PayloadHash || existing.PayloadJSON != item.PayloadJSON {
			return PurposeReconciliationReceiptRecord{}, false,
				errors.New("Purpose reconciliation receipt idempotency conflict")
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PurposeReconciliationReceiptRecord{}, false, err
	}
	_, err = db.Exec(`INSERT INTO purpose_reconciliation_receipts_v1 (
		id, idempotency_key, run_id, hypothesis_id, disposition, open_loop_id,
		event_id, relation_id, reason, source_set_hash, occurred_at, created_at,
		payload_json, payload_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.IdempotencyKey, item.RunID, item.HypothesisID,
		item.Disposition, item.OpenLoopID, item.EventID, item.RelationID,
		item.Reason, item.SourceSetHash, item.OccurredAt, item.CreatedAt,
		item.PayloadJSON, item.PayloadHash,
	)
	if err != nil {
		return PurposeReconciliationReceiptRecord{}, false, err
	}
	return item, true, nil
}

// SettleDrifted closes a run whose frozen sources drifted: every hypothesis
// without a receipt gets a rejected receipt and no Purpose event, so the run
// stays fail-closed and stops being pending. Reconcile has already recovered
// receipts for events committed before a crash, so nothing applied is lost.
func (runtime *PurposeExtractionRuntime) SettleDrifted(
	runID string,
	cause error,
) (PurposeExtractionRunDetail, error) {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	if runtime == nil || runtime.activity == nil || runtime.activity.db == nil {
		return PurposeExtractionRunDetail{}, errors.New("Purpose extraction runtime is unavailable")
	}
	var drift purposeFrozenSourceDriftError
	if !errors.As(cause, &drift) {
		return PurposeExtractionRunDetail{}, errors.New("only drifted Purpose runs can be settled")
	}
	detail, err := loadPurposeExtractionRun(runtime.activity.db, strings.TrimSpace(runID))
	if err != nil {
		return PurposeExtractionRunDetail{}, err
	}
	if detail.Run.Status != PurposeExtractionStatusSucceeded {
		return detail, errors.New("only successful Purpose extraction runs can be settled")
	}
	sources := purposeSourcesByRef(detail.Input)
	settled := make(map[string]struct{}, len(detail.Receipts))
	for _, receipt := range detail.Receipts {
		settled[receipt.HypothesisID] = struct{}{}
	}
	for _, hypothesis := range detail.Hypotheses {
		if _, exists := settled[hypothesis.ID]; exists {
			continue
		}
		receipt, err := purposeReceiptForRejection(detail.Run, hypothesis, sources, "", drift)
		if err != nil {
			return detail, err
		}
		if _, _, err := insertPurposeReceipt(runtime.activity.db, receipt); err != nil {
			return detail, err
		}
	}
	return loadPurposeExtractionRun(runtime.activity.db, detail.Run.ID)
}

func purposeReceiptForRejection(
	run PurposeExtractionRunRecord,
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	openLoopID string,
	reason error,
) (PurposeReconciliationReceiptRecord, error) {
	message := "Purpose hypothesis rejected"
	if reason != nil {
		message = truncateAtRuneBoundary(strings.TrimSpace(reason.Error()), purposeExtractionMaxErrorBytes)
	}
	return purposeNewReceipt(
		run,
		hypothesis,
		sources,
		PurposeDispositionRejected,
		openLoopID,
		"",
		"",
		message,
		0,
	)
}

func purposeExpectedEventTypes(hypothesis PurposeExtractionHypothesisRecord) map[string]string {
	result := map[string]string{}
	if hypothesis.Operation == PurposeOperationSupport || hypothesis.Operation == PurposeOperationUpdate {
		result[PurposeEventDiscovered] = PurposeDispositionCreated
	}
	if eventType, disposition, err := purposeOperationEventType(hypothesis.Operation); err == nil {
		result[eventType] = disposition
	}
	return result
}

func purposeRecoveredEventReceipt(
	run PurposeExtractionRunRecord,
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	detail PurposeOpenLoopDetail,
	event PurposeOpenLoopEventRecord,
	relationID string,
) (PurposeReconciliationReceiptRecord, error) {
	disposition, ok := purposeExpectedEventTypes(hypothesis)[event.EventType]
	if !ok {
		return PurposeReconciliationReceiptRecord{},
			errors.New("recovered Purpose event type does not match its hypothesis")
	}
	if hypothesis.DuplicateOfCanonicalKey == "" &&
		detail.OpenLoop.CanonicalKey != hypothesis.CanonicalKey {
		return PurposeReconciliationReceiptRecord{},
			errors.New("recovered Purpose event belongs to a different canonical root")
	}
	stance := PurposeStanceSupport
	if event.EventType != PurposeEventDiscovered {
		var err error
		stance, err = purposeEvidenceStanceForOperation(hypothesis.Operation)
		if err != nil {
			return PurposeReconciliationReceiptRecord{}, err
		}
	}
	expectedEvidence, err := purposeEvidenceInputsForHypothesis(hypothesis, sources, stance)
	if err != nil {
		return PurposeReconciliationReceiptRecord{}, err
	}
	if !purposeEvidenceInputsMatch(
		purposeEventEvidence(detail, event.ID),
		expectedEvidence,
	) {
		return PurposeReconciliationReceiptRecord{},
			errors.New("recovered Purpose event evidence does not match its hypothesis")
	}
	if hypothesis.DuplicateOfCanonicalKey != "" {
		disposition = PurposeDispositionDeduplicated
	}
	return purposeNewReceipt(
		run,
		hypothesis,
		sources,
		disposition,
		detail.OpenLoop.ID,
		event.ID,
		relationID,
		"",
		event.OccurredAt,
	)
}

func (runtime *PurposeExtractionRuntime) recoverHypothesisEvent(
	run PurposeExtractionRunRecord,
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	details []PurposeOpenLoopDetail,
) (PurposeReconciliationReceiptRecord, bool, error) {
	recoveredDetail, recoveredEvent, found, err := purposeFindEventByIdempotency(
		details,
		purposeReconciliationEventKey(hypothesis.ID),
	)
	if err != nil {
		return PurposeReconciliationReceiptRecord{}, false, err
	}
	if !found && hypothesis.DuplicateOfCanonicalKey != "" {
		relation, relationFound, err := purposeFindRelationByIdempotency(
			details,
			purposeReconciliationRelationKey(hypothesis.ID),
		)
		if err != nil {
			return PurposeReconciliationReceiptRecord{}, false, err
		}
		if relationFound {
			if relation.Relation.RelationType != PurposeRelationDuplicateOf ||
				len(relation.Events) == 0 ||
				relation.Events[0].EventType != PurposeRelationEventAsserted {
				return PurposeReconciliationReceiptRecord{}, true,
					errors.New("recovered Purpose duplicate relation is invalid")
			}
			eventDetail, event, eventFound := purposeDetailEvent(
				details,
				relation.Events[0].EvidenceEventID,
			)
			if !eventFound || eventDetail.OpenLoop.ID != relation.Relation.ObjectID ||
				eventDetail.OpenLoop.Kind != hypothesis.Kind ||
				purposeNormalizedOutcome(event.DesiredOutcome) !=
					purposeNormalizedOutcome(hypothesis.DesiredOutcome) ||
				!purposeEventContainsSourceOverlap(
					eventDetail,
					event.ID,
					hypothesis,
					sources,
				) {
				return PurposeReconciliationReceiptRecord{}, true,
					errors.New("recovered Purpose duplicate relation evidence is inconsistent")
			}
			receipt, err := purposeNewReceipt(
				run,
				hypothesis,
				sources,
				PurposeDispositionDeduplicated,
				eventDetail.OpenLoop.ID,
				event.ID,
				relation.Relation.ID,
				"source-backed duplicate routed to canonical root",
				event.OccurredAt,
			)
			return receipt, true, err
		}
	}
	if !found {
		return PurposeReconciliationReceiptRecord{}, false, nil
	}
	relationID := ""
	if hypothesis.DuplicateOfCanonicalKey != "" {
		if hypothesis.Operation != PurposeOperationSupport &&
			hypothesis.Operation != PurposeOperationUpdate {
			return PurposeReconciliationReceiptRecord{}, true,
				errors.New("recovered duplicate Purpose event has an invalid operation")
		}
		target, targetFound, err := purposeCanonicalDetail(
			details,
			hypothesis.DuplicateOfCanonicalKey,
		)
		if err != nil {
			return PurposeReconciliationReceiptRecord{}, true, err
		}
		if !targetFound || target.OpenLoop.ID != recoveredDetail.OpenLoop.ID ||
			recoveredDetail.OpenLoop.Kind != hypothesis.Kind ||
			purposeNormalizedOutcome(recoveredEvent.DesiredOutcome) !=
				purposeNormalizedOutcome(hypothesis.DesiredOutcome) {
			return PurposeReconciliationReceiptRecord{}, true,
				errors.New("recovered duplicate Purpose event has a different canonical target")
		}
		own, hasOwn, err := purposeCanonicalDetail(details, hypothesis.CanonicalKey)
		if err != nil {
			return PurposeReconciliationReceiptRecord{}, true, err
		}
		if hasOwn && own.OpenLoop.ID != target.OpenLoop.ID {
			if activeTarget, err := purposeActiveDuplicateTarget(own); err != nil {
				return PurposeReconciliationReceiptRecord{}, true, err
			} else if activeTarget != "" && activeTarget != target.OpenLoop.ID {
				return PurposeReconciliationReceiptRecord{}, true,
					errors.New("recovered duplicate Purpose root points to another target")
			}
			if existing, ok, err := purposeFindActiveRelation(
				details,
				own.OpenLoop.ID,
				target.OpenLoop.ID,
				PurposeRelationDuplicateOf,
			); err != nil {
				return PurposeReconciliationReceiptRecord{}, true, err
			} else if ok {
				relationID = existing.Relation.ID
			} else {
				relation, _, err := runtime.purpose.CreateRelation(CreatePurposeRelationInput{
					IdempotencyKey:  purposeReconciliationRelationKey(hypothesis.ID),
					SubjectID:       own.OpenLoop.ID,
					ObjectID:        target.OpenLoop.ID,
					RelationType:    PurposeRelationDuplicateOf,
					EvidenceEventID: recoveredEvent.ID,
					OccurredAt:      recoveredEvent.OccurredAt,
				})
				if err != nil {
					return PurposeReconciliationReceiptRecord{}, true, err
				}
				relationID = relation.Relation.ID
			}
		}
	}
	receipt, err := purposeRecoveredEventReceipt(
		run,
		hypothesis,
		sources,
		recoveredDetail,
		recoveredEvent,
		relationID,
	)
	return receipt, true, err
}

func (runtime *PurposeExtractionRuntime) reconcileDuplicate(
	run PurposeExtractionRunRecord,
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
	details []PurposeOpenLoopDetail,
	own PurposeOpenLoopDetail,
	hasOwn bool,
	target PurposeOpenLoopDetail,
) (PurposeReconciliationReceiptRecord, error) {
	if hypothesis.Operation != PurposeOperationSupport &&
		hypothesis.Operation != PurposeOperationUpdate {
		return purposeReceiptForRejection(
			run, hypothesis, sources, target.OpenLoop.ID,
			errors.New("duplicate reconciliation only accepts support or update evidence"),
		)
	}
	if target.OpenLoop.Kind != hypothesis.Kind ||
		purposeNormalizedOutcome(target.Current.DesiredOutcome) !=
			purposeNormalizedOutcome(hypothesis.DesiredOutcome) {
		return purposeReceiptForRejection(
			run, hypothesis, sources, target.OpenLoop.ID,
			errors.New("duplicate hint does not have exact kind and desired-outcome equivalence"),
		)
	}
	if !purposeHypothesisHasSourceOverlap(hypothesis, sources, target) {
		return purposeReceiptForRejection(
			run, hypothesis, sources, target.OpenLoop.ID,
			errors.New("duplicate hint has no source overlap with the canonical root"),
		)
	}
	if err := purposeOperationAllowed(hypothesis, target, sources); err != nil {
		return purposeReceiptForRejection(run, hypothesis, sources, target.OpenLoop.ID, err)
	}

	var evidenceEvent PurposeOpenLoopEventRecord
	if purposeHypothesisHasNovelEvidence(hypothesis, sources, target) {
		appendInput, _, err := purposeAppendInputForHypothesis(hypothesis, target, sources)
		if err != nil {
			return PurposeReconciliationReceiptRecord{}, err
		}
		result, _, err := runtime.purpose.AppendEvent(appendInput)
		if err != nil {
			return PurposeReconciliationReceiptRecord{}, err
		}
		evidenceEvent = result.Event
	} else {
		var ok bool
		evidenceEvent, ok = purposeLatestOverlappingEvent(target, hypothesis, sources)
		if !ok {
			return PurposeReconciliationReceiptRecord{},
				errors.New("Purpose duplicate overlap has no validated event")
		}
	}

	relationID := ""
	if hasOwn && own.OpenLoop.ID != target.OpenLoop.ID {
		if activeTarget, err := purposeActiveDuplicateTarget(own); err != nil {
			return PurposeReconciliationReceiptRecord{}, err
		} else if activeTarget != "" && activeTarget != target.OpenLoop.ID {
			return purposeReceiptForRejection(
				run, hypothesis, sources, target.OpenLoop.ID,
				errors.New("Purpose duplicate root already points to another canonical root"),
			)
		}
		if existing, ok, err := purposeFindActiveRelation(
			details,
			own.OpenLoop.ID,
			target.OpenLoop.ID,
			PurposeRelationDuplicateOf,
		); err != nil {
			return PurposeReconciliationReceiptRecord{}, err
		} else if ok {
			relationID = existing.Relation.ID
		} else {
			relation, _, err := runtime.purpose.CreateRelation(CreatePurposeRelationInput{
				IdempotencyKey:  purposeReconciliationRelationKey(hypothesis.ID),
				SubjectID:       own.OpenLoop.ID,
				ObjectID:        target.OpenLoop.ID,
				RelationType:    PurposeRelationDuplicateOf,
				EvidenceEventID: evidenceEvent.ID,
				OccurredAt:      evidenceEvent.OccurredAt,
			})
			if err != nil {
				return PurposeReconciliationReceiptRecord{}, err
			}
			relationID = relation.Relation.ID
		}
	}
	return purposeNewReceipt(
		run,
		hypothesis,
		sources,
		PurposeDispositionDeduplicated,
		target.OpenLoop.ID,
		evidenceEvent.ID,
		relationID,
		"source-backed duplicate routed to canonical root",
		evidenceEvent.OccurredAt,
	)
}

func (runtime *PurposeExtractionRuntime) reconcileHypothesis(
	run PurposeExtractionRunRecord,
	hypothesis PurposeExtractionHypothesisRecord,
	sources map[string]PurposeExtractionSource,
) (PurposeReconciliationReceiptRecord, error) {
	details, err := runtime.purpose.ListDetailsAll()
	if err != nil {
		return PurposeReconciliationReceiptRecord{}, err
	}
	if receipt, found, err := runtime.recoverHypothesisEvent(
		run,
		hypothesis,
		sources,
		details,
	); err != nil {
		return PurposeReconciliationReceiptRecord{}, err
	} else if found {
		return receipt, nil
	}

	own, hasOwn, ownErr := purposeCanonicalDetail(details, hypothesis.CanonicalKey)
	if ownErr != nil {
		return purposeReceiptForRejection(run, hypothesis, sources, "", ownErr)
	}
	if hypothesis.DuplicateOfCanonicalKey != "" {
		target, found, targetErr := purposeCanonicalDetail(
			details,
			hypothesis.DuplicateOfCanonicalKey,
		)
		if targetErr != nil {
			return purposeReceiptForRejection(run, hypothesis, sources, "", targetErr)
		}
		if !found {
			return purposeReceiptForRejection(
				run,
				hypothesis,
				sources,
				"",
				errors.New("duplicate canonical target does not exist"),
			)
		}
		return runtime.reconcileDuplicate(
			run,
			hypothesis,
			sources,
			details,
			own,
			hasOwn,
			target,
		)
	}

	if !hasOwn {
		if hypothesis.Operation != PurposeOperationSupport &&
			hypothesis.Operation != PurposeOperationUpdate {
			return purposeReceiptForRejection(
				run,
				hypothesis,
				sources,
				"",
				errors.New("Purpose lifecycle operation has no existing canonical root"),
			)
		}
		if closed, found := purposeClosedMatch(hypothesis, sources, details); found {
			return purposeReceiptForRejection(
				run,
				hypothesis,
				sources,
				closed.OpenLoop.ID,
				errors.New("the user forgot or finished this Purpose; the same sources or words do not bring it back"),
			)
		}
		createInput, err := purposeCreateInputForHypothesis(hypothesis, sources)
		if err != nil {
			return PurposeReconciliationReceiptRecord{}, err
		}
		result, _, err := runtime.purpose.Create(createInput)
		if err != nil {
			return PurposeReconciliationReceiptRecord{}, err
		}
		return purposeNewReceipt(
			run,
			hypothesis,
			sources,
			PurposeDispositionCreated,
			result.OpenLoop.ID,
			result.Event.ID,
			"",
			"",
			result.Event.OccurredAt,
		)
	}

	if !purposeHypothesisHasNovelEvidence(hypothesis, sources, own) {
		return purposeNewReceipt(
			run,
			hypothesis,
			sources,
			PurposeDispositionNoChange,
			own.OpenLoop.ID,
			"",
			"",
			"all referenced source versions are already linked",
			0,
		)
	}
	appendInput, disposition, err := purposeAppendInputForHypothesis(
		hypothesis,
		own,
		sources,
	)
	if err != nil {
		return purposeReceiptForRejection(run, hypothesis, sources, own.OpenLoop.ID, err)
	}
	result, _, err := runtime.purpose.AppendEvent(appendInput)
	if err != nil {
		return PurposeReconciliationReceiptRecord{}, err
	}
	return purposeNewReceipt(
		run,
		hypothesis,
		sources,
		disposition,
		result.OpenLoop.ID,
		result.Event.ID,
		"",
		"",
		result.Event.OccurredAt,
	)
}

// Reconcile performs no model call. It revalidates every frozen source and
// applies each stored hypothesis through the immutable Purpose ledger. Every
// hypothesis gets exactly one immutable receipt, including no-op and rejected
// hypotheses.
func (runtime *PurposeExtractionRuntime) Reconcile(
	runID string,
) (PurposeExtractionRunDetail, error) {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	if runtime == nil || runtime.activity == nil || runtime.activity.db == nil ||
		runtime.purpose == nil {
		return PurposeExtractionRunDetail{}, errors.New("Purpose extraction runtime is unavailable")
	}
	detail, err := loadPurposeExtractionRun(runtime.activity.db, strings.TrimSpace(runID))
	if err != nil {
		return PurposeExtractionRunDetail{}, err
	}
	if detail.Run.Status != PurposeExtractionStatusSucceeded {
		return detail, errors.New("only successful Purpose extraction runs can reconcile")
	}
	sources := purposeSourcesByRef(detail.Input)
	receiptByHypothesis := make(map[string]struct{}, len(detail.Receipts))
	for _, receipt := range detail.Receipts {
		receiptByHypothesis[receipt.HypothesisID] = struct{}{}
	}
	hypotheses := append([]PurposeExtractionHypothesisRecord(nil), detail.Hypotheses...)
	sort.Slice(hypotheses, func(i, j int) bool {
		return hypotheses[i].LocalKey < hypotheses[j].LocalKey
	})
	if len(receiptByHypothesis) == len(hypotheses) {
		return detail, nil
	}

	// An event may have committed immediately before a crash prevented its
	// receipt insert. Recover those receipts from the immutable event and frozen
	// input before consulting mutable source files. No new Purpose event is
	// permitted in this recovery pass.
	purposeDetails, err := runtime.purpose.ListDetailsAll()
	if err != nil {
		return detail, err
	}
	for _, hypothesis := range hypotheses {
		if _, exists := receiptByHypothesis[hypothesis.ID]; exists {
			continue
		}
		receipt, found, err := runtime.recoverHypothesisEvent(
			detail.Run,
			hypothesis,
			sources,
			purposeDetails,
		)
		if err != nil {
			return detail, err
		}
		if !found {
			continue
		}
		if _, _, err := insertPurposeReceipt(runtime.activity.db, receipt); err != nil {
			return detail, err
		}
		receiptByHypothesis[hypothesis.ID] = struct{}{}
		purposeDetails, err = runtime.purpose.ListDetailsAll()
		if err != nil {
			return detail, err
		}
	}
	if len(receiptByHypothesis) == len(hypotheses) {
		return loadPurposeExtractionRun(runtime.activity.db, detail.Run.ID)
	}
	if err := runtime.validateFrozenSources(detail); err != nil {
		return detail, err
	}
	for _, hypothesis := range hypotheses {
		if _, exists := receiptByHypothesis[hypothesis.ID]; exists {
			continue
		}
		if reason := purposeExtractionMailTextReason(hypothesisRecordAsOutput(hypothesis), detail.Input, runtime.mailSubjects); reason != "" {
			receipt, err := purposeNewReceipt(detail.Run, hypothesis, sources,
				PurposeDispositionRejected, "", "", "", reason, 0)
			if err != nil {
				return detail, err
			}
			if _, _, err := insertPurposeReceipt(runtime.activity.db, receipt); err != nil {
				return detail, err
			}
			receiptByHypothesis[hypothesis.ID] = struct{}{}
			continue
		}
		receipt, err := runtime.reconcileHypothesis(detail.Run, hypothesis, sources)
		if err != nil {
			return detail, err
		}
		if _, _, err := insertPurposeReceipt(runtime.activity.db, receipt); err != nil {
			return detail, err
		}
		receiptByHypothesis[hypothesis.ID] = struct{}{}
	}
	return loadPurposeExtractionRun(runtime.activity.db, detail.Run.ID)
}
