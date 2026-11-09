package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const VerifiedShadowLearningSourceVersion = "verified-shadow-learning-source-v1"

// VerifiedShadowLearningSource is the sole admission path from contained
// Shadow evaluation into a later production prediction. Generic Shadow output
// and evaluator prose remain denied by taskContextForbiddenSourceReason.
type VerifiedShadowLearningSource struct {
	Version          string                            `json:"version"`
	Candidate        ShadowTaskLearningCandidate       `json:"candidate"`
	Receipt          ShadowTaskLearningMaterialization `json:"receipt"`
	Learning         TaskContextLearningProvenance     `json:"learning"`
	ContextCandidate *TaskContextCandidate             `json:"contextCandidate,omitempty"`
	CapabilitySignal *WorkPatternCapabilitySignal      `json:"capabilitySignal,omitempty"`
}

type VerifiedShadowLearningSourceResult struct {
	Version    string                         `json:"version"`
	CutoffAt   int64                          `json:"cutoffAt"`
	Sources    []VerifiedShadowLearningSource `json:"sources"`
	Exclusions []TaskContextSourceExclusion   `json:"exclusions"`
}

type verifiedShadowLearningRevisionRecord struct {
	RunID             string
	RunPolicyVersion  string
	RunAuthority      string
	RunCreatedAt      int64
	RevisionCreatedAt int64
	Candidate         ShadowTaskLearningCandidate
	Receipts          []ShadowTaskLearningMaterialization
}

func normalizeTaskContextLearningProvenance(
	value TaskContextLearningProvenance,
) (TaskContextLearningProvenance, error) {
	value.Version = strings.TrimSpace(value.Version)
	value.RunID = strings.TrimSpace(value.RunID)
	value.RevisionID = strings.TrimSpace(value.RevisionID)
	value.MaterializationReceiptID = strings.TrimSpace(
		value.MaterializationReceiptID,
	)
	value.TargetKind = strings.TrimSpace(value.TargetKind)
	value.TargetID = strings.TrimSpace(value.TargetID)
	value.ProjectionKey = strings.TrimSpace(value.ProjectionKey)
	value.CapabilityKey = strings.TrimSpace(value.CapabilityKey)
	value.LearningKind = strings.TrimSpace(value.LearningKind)
	value.EvidenceHash = strings.TrimSpace(value.EvidenceHash)
	value.AuthorityEffect = strings.TrimSpace(value.AuthorityEffect)
	if value.Version != VerifiedShadowLearningSourceVersion ||
		value.RunID == "" || value.RevisionID == "" ||
		value.MaterializationReceiptID == "" || value.TargetKind == "" ||
		value.TargetID == "" || value.ProjectionKey == "" ||
		value.CapabilityKey == "" || value.LearningKind == "" ||
		value.EvidenceHash == "" || value.AvailableAt <= 0 ||
		value.AuthorityEffect != ShadowTaskLearningAuthorityNone {
		return TaskContextLearningProvenance{}, errors.New(
			"verified Shadow learning provenance is incomplete",
		)
	}
	return value, nil
}

func verifiedShadowLearningCandidateEvidenceHash(
	candidate ShadowTaskLearningCandidate,
) (string, error) {
	payload, err := json.Marshal(struct {
		PolicyVersion string                                `json:"policyVersion"`
		Evidence      []ShadowTaskLearningProvenance        `json:"evidence"`
		Dimensions    []ShadowTaskLearningDimensionEvidence `json:"dimensions"`
	}{
		PolicyVersion: candidate.PolicyVersion,
		Evidence:      candidate.Evidence,
		Dimensions:    candidate.Dimensions,
	})
	if err != nil {
		return "", err
	}
	return hashShadowPayload(payload), nil
}

func verifiedShadowLearningQualificationValid(
	candidate ShadowTaskLearningCandidate,
) bool {
	qualification := candidate.Qualification
	if qualification.Decision != ShadowTaskLearningDecisionQualified ||
		len(qualification.Reasons) != 0 ||
		qualification.RequiredEvaluationCount <= 0 ||
		qualification.RequiredTaskEpisodeCount <= 0 ||
		qualification.RequiredContextCount <= 0 ||
		qualification.QualifiedEvaluationCount <
			qualification.RequiredEvaluationCount ||
		qualification.ObservedTaskEpisodeCount <
			qualification.RequiredTaskEpisodeCount ||
		qualification.ObservedContextCount < qualification.RequiredContextCount {
		return false
	}
	if candidate.TargetKind == ShadowTaskLearningTargetTypedSelf &&
		(qualification.AverageEvidenceConfidence == nil ||
			*qualification.AverageEvidenceConfidence < shadowTaskLearningMinSelfConfidence) {
		return false
	}
	if candidate.TargetKind == ShadowTaskLearningTargetKnowledge &&
		(qualification.AverageResultQuality == nil ||
			*qualification.AverageResultQuality < shadowTaskLearningMinResultQuality) {
		return false
	}
	if candidate.TargetKind != ShadowTaskLearningTargetCapabilityProfile {
		return true
	}
	if qualification.RequiredEvaluationCount != shadowTaskLearningMinCapabilityRuns ||
		qualification.RequiredTaskEpisodeCount != shadowTaskLearningMinCapabilityRuns ||
		qualification.RequiredContextCount != shadowTaskLearningMinCapabilityContexts ||
		qualification.RequiredSafetyCount != shadowTaskLearningMinCapabilitySafety ||
		qualification.AverageTaskReliability == nil ||
		*qualification.AverageTaskReliability < shadowTaskLearningMinTaskReliability ||
		qualification.AverageResultQuality == nil ||
		*qualification.AverageResultQuality < shadowTaskLearningMinResultQuality {
		return false
	}
	safeEvaluations := make(map[string]struct{})
	for _, dimension := range candidate.Dimensions {
		if dimension.Name != "safety" || dimension.Score == nil ||
			dimension.Confidence < shadowTaskLearningMinDimensionConfidence ||
			len(dimension.EvidenceRefs) == 0 {
			continue
		}
		if dimension.Assessment == ShadowTaskSafetyUnsafe ||
			dimension.Assessment == ShadowTaskSafetyConcern {
			return false
		}
		if dimension.Assessment == ShadowTaskSafetySafe {
			safeEvaluations[dimension.EvaluationID] = struct{}{}
		}
	}
	return len(safeEvaluations) >= qualification.RequiredSafetyCount
}

func validateVerifiedShadowLearningCandidate(
	record verifiedShadowLearningRevisionRecord,
) error {
	candidate := record.Candidate
	if record.RunPolicyVersion != ShadowTaskLearningPolicyVersion ||
		record.RunAuthority != ShadowTaskLearningAuthorityNone ||
		candidate.PolicyVersion != ShadowTaskLearningPolicyVersion ||
		candidate.AuthorityEffect != ShadowTaskLearningAuthorityNone ||
		candidate.RevisionMode != ShadowTaskLearningRevisionAppendOnly ||
		candidate.RevisionID == "" || candidate.ProjectionKey == "" ||
		candidate.CapabilityKey == "" || record.RunCreatedAt <= 0 ||
		record.RevisionCreatedAt <= 0 || record.RevisionCreatedAt != record.RunCreatedAt {
		return errors.New("learning policy or immutable identity is invalid")
	}
	switch candidate.TargetKind {
	case ShadowTaskLearningTargetCapabilityProfile,
		ShadowTaskLearningTargetMemory,
		ShadowTaskLearningTargetKnowledge,
		ShadowTaskLearningTargetTypedSelf:
	default:
		return errors.New("learning target kind is unsupported")
	}
	for _, evidence := range candidate.Evidence {
		if evidence.EvaluationID == "" || evidence.TaskEpisodeID == "" ||
			evidence.ShadowRunID == "" || evidence.ContextHash == "" ||
			evidence.EvidenceID == "" || evidence.Summary == "" ||
			evidence.PersistedAt <= 0 || evidence.PersistedAt > record.RevisionCreatedAt ||
			!shadowTaskLearningSafeEvidence(ShadowTaskEvaluationEvidence{
				Origin: evidence.Origin,
			}) {
			return errors.New("learning contains unsupported evidence")
		}
	}
	evidenceHash, err := verifiedShadowLearningCandidateEvidenceHash(candidate)
	if err != nil || evidenceHash != candidate.EvidenceHash {
		return errors.New("learning evidence hash does not match")
	}
	wantRevisionID := "shadow-task-learning-revision:" + hashShadowPayload([]byte(
		candidate.ProjectionKey+"\x00"+candidate.EvidenceHash,
	))
	wantProjectionKey := "shadow-task-learning:" + shadowTaskLearningGroupKey(
		candidate.TargetKind,
		candidate.LearningKind,
		candidate.CapabilityKey,
		candidate.Statement,
	)
	if candidate.ProjectionKey != wantProjectionKey ||
		candidate.RevisionID != wantRevisionID {
		return errors.New("learning revision identity does not match")
	}
	if !verifiedShadowLearningQualificationValid(candidate) {
		return errors.New("learning qualification is not current or sufficient")
	}
	return nil
}

func listVerifiedShadowLearningRevisionRecords(
	store *ActivityStore,
) ([]verifiedShadowLearningRevisionRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	exists, err := taskContextTableExists(store.db, "shadow_task_learning_revisions")
	if err != nil || !exists {
		return []verifiedShadowLearningRevisionRecord{}, err
	}
	rows, err := store.db.Query(`
		SELECT
			revision.revision_id, revision.run_id, revision.projection_key,
			revision.policy_version, revision.target_kind, revision.learning_kind,
			revision.capability_key, revision.decision, revision.evidence_hash,
			revision.authority_effect, revision.candidate_json, revision.created_at,
			run.policy_version, run.authority_effect, run.created_at,
			materialization.id, materialization.target_kind,
			materialization.target_id, materialization.status,
			materialization.error, materialization.created_at
		FROM shadow_task_learning_revisions revision
		JOIN shadow_task_learning_runs run ON run.id = revision.run_id
		LEFT JOIN shadow_task_learning_materializations materialization
			ON materialization.revision_id = revision.revision_id
		ORDER BY revision.created_at ASC, revision.revision_id ASC,
			materialization.created_at ASC, materialization.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byRevision := make(map[string]*verifiedShadowLearningRevisionRecord)
	order := make([]string, 0)
	for rows.Next() {
		var (
			revisionID, runID, projectionKey, policyVersion  string
			targetKind, learningKind, capabilityKey          string
			decision, evidenceHash, authority, candidateJSON string
			revisionCreatedAt, runCreatedAt                  int64
			runPolicyVersion, runAuthority                   string
			receiptID, receiptTargetKind, receiptTargetID    sql.NullString
			receiptStatus, receiptError                      sql.NullString
			receiptCreatedAt                                 sql.NullInt64
		)
		if err := rows.Scan(
			&revisionID, &runID, &projectionKey, &policyVersion,
			&targetKind, &learningKind, &capabilityKey, &decision,
			&evidenceHash, &authority, &candidateJSON, &revisionCreatedAt,
			&runPolicyVersion, &runAuthority, &runCreatedAt,
			&receiptID, &receiptTargetKind, &receiptTargetID,
			&receiptStatus, &receiptError, &receiptCreatedAt,
		); err != nil {
			return nil, err
		}
		record := byRevision[revisionID]
		if record == nil {
			var candidate ShadowTaskLearningCandidate
			if err := json.Unmarshal([]byte(candidateJSON), &candidate); err != nil {
				return nil, fmt.Errorf("decode Shadow learning revision %s: %w", revisionID, err)
			}
			if candidate.RevisionID != revisionID || candidate.ProjectionKey != projectionKey ||
				candidate.PolicyVersion != policyVersion || candidate.TargetKind != targetKind ||
				candidate.LearningKind != learningKind || candidate.CapabilityKey != capabilityKey ||
				candidate.Qualification.Decision != decision || candidate.EvidenceHash != evidenceHash ||
				candidate.AuthorityEffect != authority {
				return nil, fmt.Errorf("Shadow learning revision %s columns do not match payload", revisionID)
			}
			record = &verifiedShadowLearningRevisionRecord{
				RunID:             runID,
				RunPolicyVersion:  runPolicyVersion,
				RunAuthority:      runAuthority,
				RunCreatedAt:      runCreatedAt,
				RevisionCreatedAt: revisionCreatedAt,
				Candidate:         candidate,
				Receipts:          []ShadowTaskLearningMaterialization{},
			}
			byRevision[revisionID] = record
			order = append(order, revisionID)
		}
		if receiptID.Valid {
			record.Receipts = append(record.Receipts, ShadowTaskLearningMaterialization{
				ID:         receiptID.String,
				RevisionID: revisionID,
				TargetKind: receiptTargetKind.String,
				TargetID:   receiptTargetID.String,
				Status:     receiptStatus.String,
				Error:      receiptError.String,
				CreatedAt:  receiptCreatedAt.Int64,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]verifiedShadowLearningRevisionRecord, 0, len(order))
	for _, revisionID := range order {
		result = append(result, *byRevision[revisionID])
	}
	return result, nil
}

func verifiedShadowLearningReceiptAt(
	record verifiedShadowLearningRevisionRecord,
	cutoffAt int64,
) (ShadowTaskLearningMaterialization, string, int64) {
	identities := make(map[string]struct{})
	eligible := make([]ShadowTaskLearningMaterialization, 0)
	futureAt := int64(0)
	invalid := false
	for _, receipt := range record.Receipts {
		if receipt.CreatedAt > cutoffAt {
			if futureAt == 0 || receipt.CreatedAt < futureAt {
				futureAt = receipt.CreatedAt
			}
			continue
		}
		if receipt.CreatedAt < record.RevisionCreatedAt ||
			receipt.TargetKind != record.Candidate.TargetKind {
			invalid = true
			continue
		}
		switch receipt.Status {
		case ShadowTaskLearningMaterializationMaterialized:
			if strings.TrimSpace(receipt.TargetID) == "" {
				invalid = true
				continue
			}
			identities[receipt.Status+"\x00"+receipt.TargetID] = struct{}{}
			eligible = append(eligible, receipt)
		case ShadowTaskLearningMaterializationSkipped,
			ShadowTaskLearningMaterializationDenied:
			invalid = true
		}
	}
	if invalid || len(identities) > 1 {
		return ShadowTaskLearningMaterialization{},
			TaskContextSourceReasonLearningInvalidReceipt, 0
	}
	if len(eligible) == 0 {
		if futureAt > 0 {
			return ShadowTaskLearningMaterialization{},
				TaskContextSourceReasonLearningAfterCutoff, futureAt
		}
		return ShadowTaskLearningMaterialization{},
			TaskContextSourceReasonLearningMissingReceipt, 0
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].CreatedAt != eligible[j].CreatedAt {
			return eligible[i].CreatedAt < eligible[j].CreatedAt
		}
		return eligible[i].ID < eligible[j].ID
	})
	return eligible[0], "", eligible[0].CreatedAt
}

func verifiedShadowLearningProvenance(
	record verifiedShadowLearningRevisionRecord,
	receipt ShadowTaskLearningMaterialization,
) TaskContextLearningProvenance {
	candidate := record.Candidate
	return TaskContextLearningProvenance{
		Version:                  VerifiedShadowLearningSourceVersion,
		RunID:                    record.RunID,
		RevisionID:               candidate.RevisionID,
		MaterializationReceiptID: receipt.ID,
		TargetKind:               candidate.TargetKind,
		TargetID:                 receipt.TargetID,
		ProjectionKey:            candidate.ProjectionKey,
		CapabilityKey:            candidate.CapabilityKey,
		LearningKind:             candidate.LearningKind,
		EvidenceHash:             candidate.EvidenceHash,
		AvailableAt:              receipt.CreatedAt,
		AuthorityEffect:          candidate.AuthorityEffect,
	}
}

func verifiedShadowLearningRef(
	learning TaskContextLearningProvenance,
	content string,
) string {
	return strings.Join([]string{
		"verified-shadow-learning",
		learning.RevisionID,
		learning.MaterializationReceiptID,
		taskContextHash(content)[:16],
	}, ":")
}

func verifiedShadowLearningSelfContent(entry SelfModelEntry) string {
	return strings.TrimSpace(strings.Join([]string{
		"Kind: " + entry.Kind,
		"Scope: " + entry.Scope,
		"Statement: " + entry.Statement,
		fmt.Sprintf("Confidence: %.4f", clampConfidence(entry.Confidence)),
		"Reasoning status: " + entry.ReasoningStatus,
	}, "\n"))
}

func verifiedShadowLearningTargetContent(
	store *ActivityStore,
	record verifiedShadowLearningRevisionRecord,
	receipt ShadowTaskLearningMaterialization,
) (string, string) {
	candidate := record.Candidate
	wantTargetID := shadowTaskLearningTargetID(candidate)
	switch candidate.TargetKind {
	case ShadowTaskLearningTargetCapabilityProfile:
		if receipt.TargetID != candidate.RevisionID {
			return "", TaskContextSourceReasonLearningTargetMismatch
		}
		return "", ""
	case ShadowTaskLearningTargetMemory:
		if receipt.TargetID != wantTargetID {
			return "", TaskContextSourceReasonLearningTargetMismatch
		}
		dataRoot, err := flavorDataDir()
		if err != nil {
			return "", TaskContextSourceReasonLearningTargetMissing
		}
		content, err := os.ReadFile(filepath.Join(
			dataRoot, "memory", receipt.TargetID+".md",
		))
		if errors.Is(err, os.ErrNotExist) {
			return "", TaskContextSourceReasonLearningTargetMissing
		}
		if err != nil {
			return "", TaskContextSourceReasonLearningTargetMismatch
		}
		expected := buildShadowLearningMemoryMarkdown(store, receipt.TargetID, candidate, time.UnixMilli(record.RunCreatedAt))
		if !bytes.Equal(content, []byte(expected)) {
			// Older immutable materializations predate the read-provenance
			// header. Preserve exact-byte verification of that historical form;
			// the MCP reader still withholds its unknown source dependencies.
			legacy, ok := parseOKFDocument(buildOKFMemoryMarkdown(receipt.TargetID, shadowTaskLearningTitle(candidate.Statement), time.UnixMilli(record.RunCreatedAt), "shadow_task_evaluation", "agentic_memory", shadowTaskLearningEvidencePoints(candidate)))
			if !ok {
				return "", TaskContextSourceReasonLearningTargetMismatch
			}
			delete(legacy.Frontmatter, "read_provenance")
			legacyExpected := renderOKFDocument(legacy.Frontmatter, legacy.Body)
			if !bytes.Equal(content, []byte(legacyExpected)) {
				return "", TaskContextSourceReasonLearningTargetMismatch
			}
			return legacyExpected, ""
		}
		return expected, ""
	case ShadowTaskLearningTargetKnowledge:
		if receipt.TargetID != wantTargetID {
			return "", TaskContextSourceReasonLearningTargetMismatch
		}
		dataRoot, err := flavorDataDir()
		if err != nil {
			return "", TaskContextSourceReasonLearningTargetMissing
		}
		content, err := os.ReadFile(filepath.Join(
			dataRoot, "knowledge", receipt.TargetID+".md",
		))
		if errors.Is(err, os.ErrNotExist) {
			return "", TaskContextSourceReasonLearningTargetMissing
		}
		if err != nil {
			return "", TaskContextSourceReasonLearningTargetMismatch
		}
		expected := buildOKFKnowledgeMarkdown(
			receipt.TargetID,
			shadowTaskLearningTitle(candidate.Statement),
			"Repeated, source-backed work pattern learned from evaluated user outcomes.",
			time.UnixMilli(record.RunCreatedAt),
			"shadow_task_evaluation",
			nil,
			nil,
			shadowTaskLearningEvidencePoints(candidate),
			"how_to",
		)
		legacyExpected := expected
		if doc, ok := parseOKFDocument(expected); ok {
			if existing, parsed := parseOKFDocument(string(content)); parsed {
				if kind := okfString(existing.Frontmatter, "kind"); kind != "" {
					doc.Frontmatter["kind"] = kind
				}
			}
			expected = knowledgeProvenance(renderOKFDocument(doc.Frontmatter, doc.Body), string(content), []string{"shadow_task_evaluation"}, nil, true)
		}
		if knowHowDocumentsEqualExceptKind(string(content), expected) {
			return expected, ""
		}
		if knowHowDocumentsEqualExceptKind(string(content), legacyExpected) {
			return legacyExpected, ""
		}
		return "", TaskContextSourceReasonLearningTargetMismatch
	case ShadowTaskLearningTargetTypedSelf:
		entry, err := store.GetSelfModelEntryByID(receipt.TargetID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", TaskContextSourceReasonLearningTargetMissing
		}
		if err != nil || candidate.SuggestedSelfStatus != selfModelStatusActive ||
			entry.EntryKey != candidate.ProjectionKey ||
			entry.Kind != candidate.SuggestedSelfKind ||
			entry.Scope != candidate.CapabilityKey ||
			entry.Statement != candidate.Statement ||
			entry.Status != selfModelStatusActive ||
			entry.ReasoningStatus != "source_backed_shadow_task_evaluation" {
			return "", TaskContextSourceReasonLearningTargetMismatch
		}
		latest, err := store.GetLatestSelfModelEntry(entry.EntryKey)
		if err != nil || latest.ID != entry.ID {
			return "", TaskContextSourceReasonLearningTargetMismatch
		}
		for _, evidence := range candidate.Evidence {
			matched := false
			for _, stored := range entry.Evidence {
				if stored.SourceKind == "shadow_task_evaluation" &&
					stored.SourceID == evidence.EvaluationID &&
					stored.Statement == evidence.Summary &&
					stored.OccurredAt == evidence.OccurredAt {
					matched = true
					break
				}
			}
			if !matched {
				return "", TaskContextSourceReasonLearningTargetMismatch
			}
		}
		return verifiedShadowLearningSelfContent(entry), ""
	default:
		return "", TaskContextSourceReasonLearningPolicyMismatch
	}
}

func verifiedShadowLearningUnknownReasoningCount(
	candidate ShadowTaskLearningCandidate,
) int {
	count := 0
	for _, dimension := range candidate.Dimensions {
		if dimension.Name == "reasoning" &&
			dimension.Assessment == ShadowTaskAssessmentUnknown {
			count++
		}
	}
	return count
}

func verifiedShadowLearningExclusion(
	record verifiedShadowLearningRevisionRecord,
	reason string,
	observedAt int64,
	learning *TaskContextLearningProvenance,
) TaskContextSourceExclusion {
	return TaskContextSourceExclusion{
		Ref: strings.Join([]string{
			"verified-shadow-learning-exclusion",
			record.Candidate.RevisionID,
			reason,
		}, ":"),
		SourceKind: taskContextSourceVerifiedShadowLearningKind,
		SourceID:   record.Candidate.RevisionID,
		ObservedAt: observedAt,
		Reason:     reason,
		Learning:   learning,
	}
}

func VerifiedShadowLearningSourcesAt(
	store *ActivityStore,
	cutoffAt int64,
	query string,
) (VerifiedShadowLearningSourceResult, error) {
	result := VerifiedShadowLearningSourceResult{
		Version:    VerifiedShadowLearningSourceVersion,
		CutoffAt:   cutoffAt,
		Sources:    []VerifiedShadowLearningSource{},
		Exclusions: []TaskContextSourceExclusion{},
	}
	if store == nil || store.db == nil {
		return result, errors.New("activity store is unavailable")
	}
	if cutoffAt <= 0 {
		return result, errors.New("verified Shadow learning requires a cutoff")
	}
	records, err := listVerifiedShadowLearningRevisionRecords(store)
	if err != nil {
		return result, err
	}
	if len(records) == 0 {
		return result, nil
	}
	repeatedOnsets, err := store.taskEpisodeRepeatedOnsetSetAt(cutoffAt)
	if err != nil {
		return result, err
	}
	latest := make(map[string]int)
	for index, record := range records {
		if record.RevisionCreatedAt > cutoffAt {
			result.Exclusions = append(result.Exclusions,
				verifiedShadowLearningExclusion(
					record,
					TaskContextSourceReasonFuture,
					record.RevisionCreatedAt,
					nil,
				),
			)
			continue
		}
		key := record.Candidate.TargetKind + "\x00" + record.Candidate.ProjectionKey
		if previous, ok := latest[key]; !ok ||
			record.RevisionCreatedAt > records[previous].RevisionCreatedAt ||
			(record.RevisionCreatedAt == records[previous].RevisionCreatedAt &&
				record.Candidate.RevisionID > records[previous].Candidate.RevisionID) {
			latest[key] = index
		}
	}
	selected := make(map[int]struct{}, len(latest))
	for _, index := range latest {
		selected[index] = struct{}{}
	}
	for index, record := range records {
		if record.RevisionCreatedAt > cutoffAt {
			continue
		}
		if _, ok := selected[index]; !ok {
			result.Exclusions = append(result.Exclusions,
				verifiedShadowLearningExclusion(
					record,
					TaskContextSourceReasonLearningSuperseded,
					record.RevisionCreatedAt,
					nil,
				),
			)
			continue
		}
		if err := validateVerifiedShadowLearningCandidate(record); err != nil {
			reason := TaskContextSourceReasonLearningPolicyMismatch
			if record.Candidate.Qualification.Decision !=
				ShadowTaskLearningDecisionQualified {
				reason = TaskContextSourceReasonLearningNotQualified
			}
			result.Exclusions = append(result.Exclusions,
				verifiedShadowLearningExclusion(
					record, reason, record.RevisionCreatedAt, nil,
				),
			)
			continue
		}
		repeatedSource := false
		for _, evidence := range record.Candidate.Evidence {
			if _, repeated := repeatedOnsets[evidence.TaskEpisodeID]; repeated {
				repeatedSource = true
				break
			}
		}
		if repeatedSource {
			result.Exclusions = append(result.Exclusions,
				verifiedShadowLearningExclusion(
					record,
					TaskContextSourceReasonLearningRepeatedOnset,
					record.RevisionCreatedAt,
					nil,
				),
			)
			continue
		}
		excluded, err := store.isShadowTaskLearningCandidateFirstRunExcluded(
			record.Candidate,
		)
		if err != nil {
			return result, err
		}
		if excluded {
			result.Exclusions = append(result.Exclusions,
				verifiedShadowLearningExclusion(
					record,
					TaskContextSourceReasonLearningFirstRun,
					record.RevisionCreatedAt,
					nil,
				),
			)
			continue
		}
		corrected, err := store.HasAgencyLearningCorrectionAt(
			record.Candidate.RevisionID,
			record.Candidate.CapabilityKey,
			record.Candidate.Statement,
			cutoffAt,
		)
		if err != nil {
			return result, err
		}
		if corrected {
			result.Exclusions = append(result.Exclusions,
				verifiedShadowLearningExclusion(
					record,
					TaskContextSourceReasonLearningCorrected,
					record.RevisionCreatedAt,
					nil,
				),
			)
			continue
		}
		receipt, reason, observedAt := verifiedShadowLearningReceiptAt(record, cutoffAt)
		if reason != "" {
			result.Exclusions = append(result.Exclusions,
				verifiedShadowLearningExclusion(record, reason, observedAt, nil),
			)
			continue
		}
		learning := verifiedShadowLearningProvenance(record, receipt)
		if _, err := normalizeTaskContextLearningProvenance(learning); err != nil {
			return result, err
		}
		content, reason := verifiedShadowLearningTargetContent(store, record, receipt)
		if reason != "" {
			result.Exclusions = append(result.Exclusions,
				verifiedShadowLearningExclusion(
					record, reason, receipt.CreatedAt, &learning,
				),
			)
			continue
		}
		source := VerifiedShadowLearningSource{
			Version:   VerifiedShadowLearningSourceVersion,
			Candidate: record.Candidate,
			Receipt:   receipt,
			Learning:  learning,
		}
		if record.Candidate.TargetKind == ShadowTaskLearningTargetCapabilityProfile {
			source.CapabilitySignal = &WorkPatternCapabilitySignal{
				ProfileID:          record.Candidate.RevisionID,
				CapabilityKey:      record.Candidate.CapabilityKey,
				Ready:              true,
				Status:             ShadowTaskLearningDecisionQualified,
				ActionReliability:  record.Candidate.Qualification.AverageTaskReliability,
				EvaluationCount:    record.Candidate.Qualification.ObservedEvaluationCount,
				QualifiedCount:     record.Candidate.Qualification.QualifiedEvaluationCount,
				UnknownReasoning:   verifiedShadowLearningUnknownReasoningCount(record.Candidate),
				SourceContextCount: record.Candidate.Qualification.ObservedContextCount,
				CreatedAt:          receipt.CreatedAt,
				Learning:           &learning,
			}
		} else {
			section := TaskContextSectionSelf
			baseRelevance := taskContextSourceDefaultMemoryRelevance
			if record.Candidate.TargetKind == ShadowTaskLearningTargetKnowledge {
				section = TaskContextSectionKnowledge
				baseRelevance = taskContextSourceDefaultKnowledgeRelevance
			} else if record.Candidate.TargetKind == ShadowTaskLearningTargetTypedSelf {
				baseRelevance = taskContextSourceDefaultSelfRelevance
			}
			source.ContextCandidate = &TaskContextCandidate{
				Ref:             verifiedShadowLearningRef(learning, content),
				Section:         section,
				SourceKind:      taskContextSourceVerifiedShadowLearningKind,
				SourceID:        receipt.ID,
				ObservedAt:      receipt.CreatedAt,
				Content:         content,
				EvidenceClass:   TaskContextEvidenceEvaluatedProjection,
				PrivacyDecision: TaskContextPrivacyRedact,
				Relevance: taskContextSourceRelevance(
					query,
					content,
					baseRelevance,
				),
				Learning: &learning,
			}
		}
		result.Sources = append(result.Sources, source)
	}
	sort.SliceStable(result.Sources, func(i, j int) bool {
		if result.Sources[i].Learning.AvailableAt != result.Sources[j].Learning.AvailableAt {
			return result.Sources[i].Learning.AvailableAt < result.Sources[j].Learning.AvailableAt
		}
		return result.Sources[i].Learning.RevisionID < result.Sources[j].Learning.RevisionID
	})
	sort.SliceStable(result.Exclusions, func(i, j int) bool {
		if result.Exclusions[i].SourceID != result.Exclusions[j].SourceID {
			return result.Exclusions[i].SourceID < result.Exclusions[j].SourceID
		}
		return result.Exclusions[i].Reason < result.Exclusions[j].Reason
	})
	return result, nil
}
