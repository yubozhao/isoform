package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const purposeTaskContextRefVersion = "purpose-task-context-ref-v1"

type purposeTaskContextRef struct {
	OpenLoopID     string
	CurrentEventID string
	ProjectionHash string
}

type purposeTaskContextProjection struct {
	Version             string   `json:"version"`
	OpenLoopPayloadHash string   `json:"openLoopPayloadHash"`
	EventPayloadHashes  []string `json:"eventPayloadHashes"`
	EvidenceHashes      []string `json:"evidenceHashes"`
	RelationHashes      []string `json:"relationHashes"`
	RelationEventHashes []string `json:"relationEventHashes"`
	TitleRepairHash     string   `json:"titleRepairHash,omitempty"`
}

func purposeTaskContextProjectionHash(
	detail PurposeOpenLoopDetail,
) (string, error) {
	projection := purposeTaskContextProjection{
		Version:             purposeTaskContextRefVersion,
		OpenLoopPayloadHash: strings.TrimSpace(detail.OpenLoop.PayloadHash),
		EventPayloadHashes:  make([]string, 0, len(detail.Events)),
		EvidenceHashes:      make([]string, 0, len(detail.Evidence)),
		RelationHashes:      make([]string, 0, len(detail.Relations)),
		RelationEventHashes: make([]string, 0),
	}
	if projection.OpenLoopPayloadHash == "" || strings.TrimSpace(detail.Current.ID) == "" {
		return "", errors.New("Purpose task context projection is incomplete")
	}
	for _, event := range detail.Events {
		if strings.TrimSpace(event.PayloadHash) == "" {
			return "", errors.New("Purpose task context event hash is missing")
		}
		projection.EventPayloadHashes = append(projection.EventPayloadHashes, event.PayloadHash)
		if event.ID == detail.Current.ID && event.Title != detail.Current.Title {
			projection.TitleRepairHash = agencyPayloadHash([]byte(event.ID + "\x00" + detail.Current.Title))
		}
	}
	for _, evidence := range detail.Evidence {
		if strings.TrimSpace(evidence.PayloadHash) == "" {
			return "", errors.New("Purpose task context evidence hash is missing")
		}
		projection.EvidenceHashes = append(projection.EvidenceHashes, evidence.PayloadHash)
	}
	for _, relation := range detail.Relations {
		if strings.TrimSpace(relation.Relation.PayloadHash) == "" {
			return "", errors.New("Purpose task context relation hash is missing")
		}
		projection.RelationHashes = append(
			projection.RelationHashes,
			relation.Relation.PayloadHash,
		)
		for _, event := range relation.Events {
			if strings.TrimSpace(event.PayloadHash) == "" {
				return "", errors.New("Purpose task context relation event hash is missing")
			}
			projection.RelationEventHashes = append(
				projection.RelationEventHashes,
				event.PayloadHash,
			)
		}
	}
	sort.Strings(projection.EventPayloadHashes)
	sort.Strings(projection.EvidenceHashes)
	sort.Strings(projection.RelationHashes)
	sort.Strings(projection.RelationEventHashes)
	raw, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("encode Purpose task context projection: %w", err)
	}
	return agencyPayloadHash(raw), nil
}

func purposeTaskContextReference(
	detail PurposeOpenLoopDetail,
) (string, error) {
	projectionHash, err := purposeTaskContextProjectionHash(detail)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		taskContextSourcePurposeOpenLoopKind,
		detail.OpenLoop.ID,
		detail.Current.ID,
		projectionHash,
	}, ":"), nil
}

func parsePurposeTaskContextReference(value string) (purposeTaskContextRef, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 4 || parts[0] != taskContextSourcePurposeOpenLoopKind ||
		strings.TrimSpace(parts[1]) == "" || strings.TrimSpace(parts[2]) == "" ||
		len(parts[3]) != 64 {
		return purposeTaskContextRef{}, false
	}
	return purposeTaskContextRef{
		OpenLoopID:     parts[1],
		CurrentEventID: parts[2],
		ProjectionHash: parts[3],
	}, true
}

func purposeTaskContextEvidenceClass(detail PurposeOpenLoopDetail) string {
	for _, evidence := range purposeEventEvidence(detail, detail.Current.ID) {
		if evidence.EvidenceClass == PurposeEvidenceExplicitUser ||
			evidence.EvidenceClass == PurposeEvidenceCorrection {
			return TaskContextEvidenceExplicitUser
		}
	}
	return TaskContextEvidenceEvaluatedProjection
}

func purposeTaskContextRelations(detail PurposeOpenLoopDetail) string {
	items := make([]string, 0, len(detail.Relations))
	for _, relation := range detail.Relations {
		if relation.Current.EventType != PurposeRelationEventAsserted {
			continue
		}
		items = append(items, strings.Join([]string{
			relation.Relation.RelationType,
			relation.Relation.SubjectID,
			relation.Relation.ObjectID,
			relation.Relation.ID,
			relation.Current.ID,
		}, " | "))
	}
	sort.Strings(items)
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, "; ")
}

func purposeTaskContextContent(
	detail PurposeOpenLoopDetail,
	projectionHash string,
) string {
	rationale := "unknown"
	if detail.Current.ReasoningStatus == PurposeReasoningExplicit &&
		strings.TrimSpace(detail.Current.Rationale) != "" {
		rationale = strings.TrimSpace(detail.Current.Rationale)
	}
	due := detail.Current.DueKind
	if strings.TrimSpace(detail.Current.DueValue) != "" {
		due += ": " + strings.TrimSpace(detail.Current.DueValue)
	}
	waiting := strings.TrimSpace(detail.Current.WaitingCondition)
	if waiting == "" {
		waiting = "none"
	}
	return strings.Join([]string{
		"Purpose open loop: " + detail.OpenLoop.ID,
		"Kind: " + detail.OpenLoop.Kind,
		"Title: " + detail.Current.Title,
		"Desired outcome: " + detail.Current.DesiredOutcome,
		"Owner: " + detail.Current.Owner,
		"Counterparty: " + emptyPurposeTaskContextValue(detail.Current.Counterparty),
		"Scope: " + emptyPurposeTaskContextValue(detail.Current.Scope),
		"Due: " + due,
		"Lifecycle state: " + detail.Current.LifecycleState,
		"Support state: " + detail.Current.SupportState,
		"Waiting condition: " + waiting,
		"Reasoning status: " + detail.Current.ReasoningStatus,
		"Supported rationale: " + rationale,
		"Current event: " + detail.Current.ID,
		"Current event payload hash: " + detail.Current.PayloadHash,
		"Evidence set hash: " + detail.Current.EvidenceSetHash,
		"Projection hash: " + projectionHash,
		"Active relations: " + purposeTaskContextRelations(detail),
	}, "\n")
}

func emptyPurposeTaskContextValue(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "unknown"
}

func purposeTaskContextIneligibleReason(detail PurposeOpenLoopDetail) string {
	switch detail.Current.LifecycleState {
	case PurposeStateCandidate:
		return TaskContextSourceReasonPurposeUnsupported
	case PurposeStateResolved, PurposeStateContradicted, PurposeStateDismissed,
		PurposeStateSuperseded:
		return TaskContextSourceReasonPurposeTerminal
	}
	for _, relation := range detail.Relations {
		if relation.Relation.SubjectID == detail.OpenLoop.ID &&
			relation.Relation.RelationType == PurposeRelationDuplicateOf &&
			relation.Current.EventType == PurposeRelationEventAsserted {
			return TaskContextSourceReasonPurposeDuplicate
		}
	}
	return TaskContextSourceReasonInactive
}

func (collector *taskContextSourceCollector) gatherPurposeOpenLoops(
	db *sql.DB,
) error {
	exists, err := taskContextTableExists(db, "purpose_open_loops_v1")
	if err != nil {
		return fmt.Errorf("inspect Purpose task context sources: %w", err)
	}
	if !exists {
		return nil
	}
	all, err := loadPurposeLedger(db)
	if err != nil {
		return fmt.Errorf("load Purpose task context sources: %w", err)
	}
	projected, err := projectPurposeDetailsAt(all, collector.cutoffAt)
	if err != nil {
		return fmt.Errorf("project Purpose task context sources: %w", err)
	}
	projected, err = projectPurposeMailTitleRepairs(db, projected, collector.cutoffAt)
	if err != nil {
		return fmt.Errorf("project Purpose title repairs: %w", err)
	}
	projectedByID := make(map[string]PurposeOpenLoopDetail, len(projected))
	for _, detail := range projected {
		projectedByID[detail.OpenLoop.ID] = detail
	}
	for _, detail := range all {
		if detail.OpenLoop.CreatedAt > collector.cutoffAt {
			collector.exclude(
				"",
				taskContextSourcePurposeOpenLoopKind,
				detail.OpenLoop.ID,
				detail.OpenLoop.CreatedAt,
				TaskContextSourceReasonFuture,
			)
			continue
		}
		for _, event := range detail.Events {
			if event.OccurredAt > collector.cutoffAt || event.CreatedAt > collector.cutoffAt {
				collector.exclude(
					"purpose-open-loop-event:"+detail.OpenLoop.ID+":"+event.ID,
					taskContextSourcePurposeOpenLoopKind,
					detail.OpenLoop.ID,
					max(event.OccurredAt, event.CreatedAt),
					TaskContextSourceReasonFuture,
				)
			}
		}
		for _, relation := range detail.Relations {
			if relation.Relation.CreatedAt > collector.cutoffAt {
				collector.exclude(
					"purpose-open-loop-relation:"+detail.OpenLoop.ID+":"+relation.Relation.ID,
					taskContextSourcePurposeOpenLoopKind,
					detail.OpenLoop.ID,
					relation.Relation.CreatedAt,
					TaskContextSourceReasonFuture,
				)
				continue
			}
			for _, event := range relation.Events {
				if event.OccurredAt > collector.cutoffAt || event.CreatedAt > collector.cutoffAt {
					collector.exclude(
						"purpose-open-loop-relation-event:"+detail.OpenLoop.ID+":"+event.ID,
						taskContextSourcePurposeOpenLoopKind,
						detail.OpenLoop.ID,
						max(event.OccurredAt, event.CreatedAt),
						TaskContextSourceReasonFuture,
					)
				}
			}
		}
		atCutoff, ok := projectedByID[detail.OpenLoop.ID]
		if !ok {
			continue
		}
		ref, refErr := purposeTaskContextReference(atCutoff)
		if refErr != nil {
			collector.exclude(
				"",
				taskContextSourcePurposeOpenLoopKind,
				detail.OpenLoop.ID,
				atCutoff.Current.OccurredAt,
				TaskContextSourceReasonMalformed,
			)
			continue
		}
		if !atCutoff.Eligible {
			collector.exclude(
				ref,
				taskContextSourcePurposeOpenLoopKind,
				detail.OpenLoop.ID,
				atCutoff.Current.OccurredAt,
				purposeTaskContextIneligibleReason(atCutoff),
			)
			continue
		}
		projectionHash, _ := purposeTaskContextProjectionHash(atCutoff)
		content := purposeTaskContextContent(atCutoff, projectionHash)
		collector.add(TaskContextCandidate{
			Ref:             ref,
			Section:         TaskContextSectionCommitments,
			SourceKind:      taskContextSourcePurposeOpenLoopKind,
			SourceID:        detail.OpenLoop.ID,
			ObservedAt:      atCutoff.Current.OccurredAt,
			Content:         content,
			EvidenceClass:   purposeTaskContextEvidenceClass(atCutoff),
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance: taskContextSourceRelevance(
				collector.query,
				content,
				taskContextSourceDefaultPurposeRelevance,
			),
		})
	}
	return nil
}
