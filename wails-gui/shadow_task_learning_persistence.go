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

const shadowTaskLearningListMax = 500

const firstRunContaminationReason = "guided first-outcome source is excluded from Shadow mode learning"

type firstRunLearningSource struct {
	Kind string
	ID   string
}

func normalizeFirstRunLearningSources(
	sources []firstRunLearningSource,
) []firstRunLearningSource {
	seen := make(map[string]struct{}, len(sources))
	normalized := make([]firstRunLearningSource, 0, len(sources))
	for _, source := range sources {
		source.Kind = strings.TrimSpace(source.Kind)
		source.ID = strings.TrimSpace(source.ID)
		if source.Kind == "" || source.ID == "" {
			continue
		}
		key := source.Kind + "\x00" + source.ID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, source)
	}
	return normalized
}

// hasFirstRunLearningExclusion returns true when any supplied provenance was
// captured by the visible guided first outcome. Callers must treat lookup
// errors as a block: a missing containment decision must never become learning.
func (s *ActivityStore) hasFirstRunLearningExclusion(
	sources ...firstRunLearningSource,
) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("activity store is unavailable")
	}
	sources = normalizeFirstRunLearningSources(sources)
	for _, source := range sources {
		excluded, err := s.IsFirstRunLearningExcluded(source.Kind, source.ID)
		if err != nil {
			return false, err
		}
		if excluded {
			return true, nil
		}
	}
	return false, nil
}

func (s *ActivityStore) isFirstRunSessionGroupLearningExcluded(
	group sessionGroup,
) (bool, error) {
	sources := make([]firstRunLearningSource, 0, len(group.Sessions)*2)
	for _, session := range group.Sessions {
		// The task onset path identifies normal groups as activity, while
		// first-run capture records the concrete activity session. Match both
		// identities so a mixed stitched group fails closed.
		sources = append(sources,
			firstRunLearningSource{Kind: "activity", ID: session.ID},
			firstRunLearningSource{Kind: "activity_session", ID: session.ID},
		)
	}
	return s.hasFirstRunLearningExclusion(sources...)
}

func (s *ActivityStore) isTaskEpisodeFirstRunLearningExcluded(
	episodeID string,
) (bool, error) {
	episodeID = strings.TrimSpace(episodeID)
	if episodeID == "" {
		return false, errors.New("task episode ID is required")
	}
	episode, err := s.GetTaskEpisode(episodeID)
	if err != nil {
		return false, err
	}
	sources := []firstRunLearningSource{
		{Kind: "task_episode", ID: episodeID},
		{Kind: "task_episode_context", ID: episode.FrozenContext.ID},
	}
	for _, entry := range episode.FrozenContext.Manifest {
		sources = append(sources, firstRunLearningSource{
			Kind: entry.SourceKind,
			ID:   entry.SourceID,
		})
	}
	rows, err := s.db.Query(`
		SELECT source_kind, source_id
		FROM task_episode_evidence
		WHERE task_episode_id = ?`, episodeID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var source firstRunLearningSource
		if err := rows.Scan(&source.Kind, &source.ID); err != nil {
			return false, err
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return s.hasFirstRunLearningExclusion(sources...)
}

func (s *ActivityStore) isShadowTaskRunFirstRunLearningExcluded(
	runID string,
) (bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false, errors.New("shadow run ID is required")
	}
	var taskEpisodeID, sourceKind, sourceID string
	if err := s.db.QueryRow(`
		SELECT task_episode_id, source_kind, source_id
		FROM shadow_runs WHERE id = ?`, runID).Scan(
		&taskEpisodeID,
		&sourceKind,
		&sourceID,
	); err != nil {
		return false, err
	}
	sources := []firstRunLearningSource{
		{Kind: "shadow_run", ID: runID},
		{Kind: sourceKind, ID: sourceID},
	}
	rows, err := s.db.Query(`
		SELECT source_kind, source_id
		FROM shadow_run_evidence
		WHERE shadow_run_id = ?`, runID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var source firstRunLearningSource
		if err := rows.Scan(&source.Kind, &source.ID); err != nil {
			return false, err
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	excluded, err := s.hasFirstRunLearningExclusion(sources...)
	if err != nil || excluded {
		return excluded, err
	}
	return s.isTaskEpisodeFirstRunLearningExcluded(taskEpisodeID)
}

func (s *ActivityStore) isShadowTaskLearningEvaluationFirstRunExcluded(
	evaluationID, runID string,
) (bool, error) {
	excluded, err := s.hasFirstRunLearningExclusion(
		firstRunLearningSource{Kind: "shadow_run_evaluation", ID: evaluationID},
		firstRunLearningSource{Kind: "shadow_evaluation", ID: evaluationID},
	)
	if err != nil || excluded {
		return excluded, err
	}
	return s.isShadowTaskRunFirstRunLearningExcluded(runID)
}

func (s *ActivityStore) isShadowTaskLearningCandidateFirstRunExcluded(
	candidate ShadowTaskLearningCandidate,
) (bool, error) {
	sources := make([]firstRunLearningSource, 0, len(candidate.Evidence)*3)
	for _, evidence := range candidate.Evidence {
		sources = append(sources,
			firstRunLearningSource{Kind: "shadow_run_evaluation", ID: evidence.EvaluationID},
			firstRunLearningSource{Kind: "shadow_evaluation", ID: evidence.EvaluationID},
			firstRunLearningSource{Kind: "task_episode", ID: evidence.TaskEpisodeID},
			firstRunLearningSource{Kind: "shadow_run", ID: evidence.ShadowRunID},
		)
	}
	return s.hasFirstRunLearningExclusion(sources...)
}

type ShadowTaskLearningRun struct {
	ID              string                       `json:"id"`
	InputHash       string                       `json:"inputHash"`
	PolicyVersion   string                       `json:"policyVersion"`
	AuthorityEffect string                       `json:"authorityEffect"`
	Projection      ShadowTaskLearningProjection `json:"projection"`
	CreatedAt       int64                        `json:"createdAt"`
}

type ShadowTaskLearningRevision struct {
	RunID     string                      `json:"runId"`
	Candidate ShadowTaskLearningCandidate `json:"candidate"`
	CreatedAt int64                       `json:"createdAt"`
}

type ShadowTaskLearningMaterialization struct {
	ID         string `json:"id"`
	RevisionID string `json:"revisionId"`
	TargetKind string `json:"targetKind"`
	TargetID   string `json:"targetId,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
}

func normalizeShadowTaskLearningLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > shadowTaskLearningListMax {
		return shadowTaskLearningListMax
	}
	return limit
}

func shadowTaskLearningProjectionInputHash(
	projection ShadowTaskLearningProjection,
) (string, []byte, error) {
	projection.PolicyVersion = strings.TrimSpace(projection.PolicyVersion)
	projection.AuthorityEffect = strings.TrimSpace(projection.AuthorityEffect)
	if projection.PolicyVersion != ShadowTaskLearningPolicyVersion {
		return "", nil, fmt.Errorf(
			"unsupported shadow task learning policy %q",
			projection.PolicyVersion,
		)
	}
	if projection.AuthorityEffect != ShadowTaskLearningAuthorityNone {
		return "", nil, errors.New("shadow task learning may not change authority")
	}
	payload, err := json.Marshal(projection)
	if err != nil {
		return "", nil, err
	}
	return hashShadowPayload(payload), payload, nil
}

func PersistShadowTaskLearningProjection(
	store *ActivityStore,
	projection ShadowTaskLearningProjection,
	createdAt int64,
) (ShadowTaskLearningRun, bool, error) {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	if store == nil || store.db == nil {
		return ShadowTaskLearningRun{}, false, errors.New("activity store is unavailable")
	}
	if createdAt <= 0 {
		return ShadowTaskLearningRun{}, false, errors.New(
			"shadow task learning persistence requires a stable timestamp",
		)
	}
	for _, candidate := range projection.Candidates {
		if candidate.TargetKind != ShadowTaskLearningTargetMemory {
			continue
		}
		if err := validateMemoryForgetSource(store.db, "memory", shadowTaskLearningTargetID(candidate), shadowTaskLearningMemoryContent(candidate, createdAt)); err != nil {
			return ShadowTaskLearningRun{}, false, err
		}
	}
	inputHash, payload, err := shadowTaskLearningProjectionInputHash(projection)
	if err != nil {
		return ShadowTaskLearningRun{}, false, err
	}
	run := ShadowTaskLearningRun{
		ID:              deterministicTaskEpisodeID("shadow-task-learning-run", inputHash),
		InputHash:       inputHash,
		PolicyVersion:   projection.PolicyVersion,
		AuthorityEffect: projection.AuthorityEffect,
		Projection:      projection,
		CreatedAt:       createdAt,
	}
	tx, err := store.db.Begin()
	if err != nil {
		return ShadowTaskLearningRun{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`
		INSERT OR IGNORE INTO shadow_task_learning_runs (
			id, input_hash, policy_version, authority_effect, projection_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?)`,
		run.ID,
		run.InputHash,
		run.PolicyVersion,
		run.AuthorityEffect,
		string(payload),
		run.CreatedAt,
	)
	if err != nil {
		return ShadowTaskLearningRun{}, false, err
	}
	addedRows, err := result.RowsAffected()
	if err != nil {
		return ShadowTaskLearningRun{}, false, err
	}
	if addedRows == 0 {
		existing, err := getShadowTaskLearningRunTx(tx, run.ID)
		if err != nil {
			return ShadowTaskLearningRun{}, false, err
		}
		existingPayload, _ := json.Marshal(existing.Projection)
		if existing.InputHash != run.InputHash || string(existingPayload) != string(payload) {
			return ShadowTaskLearningRun{}, false, errors.New(
				"shadow task learning run key has a different immutable payload",
			)
		}
		return existing, false, nil
	}
	for _, candidate := range projection.Candidates {
		if candidate.AuthorityEffect != ShadowTaskLearningAuthorityNone {
			return ShadowTaskLearningRun{}, false, errors.New(
				"shadow task learning candidate may not change authority",
			)
		}
		candidatePayload, err := json.Marshal(candidate)
		if err != nil {
			return ShadowTaskLearningRun{}, false, err
		}
		if _, err := tx.Exec(`
			INSERT INTO shadow_task_learning_revisions (
				revision_id, run_id, projection_key, policy_version, target_kind,
				learning_kind, capability_key, decision, evidence_hash,
				authority_effect, candidate_json, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			candidate.RevisionID,
			run.ID,
			candidate.ProjectionKey,
			candidate.PolicyVersion,
			candidate.TargetKind,
			candidate.LearningKind,
			candidate.CapabilityKey,
			candidate.Qualification.Decision,
			candidate.EvidenceHash,
			candidate.AuthorityEffect,
			string(candidatePayload),
			createdAt,
		); err != nil {
			return ShadowTaskLearningRun{}, false, fmt.Errorf(
				"persist shadow task learning revision %s: %w",
				candidate.RevisionID,
				err,
			)
		}
	}
	if err := tx.Commit(); err != nil {
		return ShadowTaskLearningRun{}, false, err
	}
	return run, true, nil
}

func getShadowTaskLearningRunTx(
	queryer interface {
		QueryRow(string, ...any) *sql.Row
	},
	id string,
) (ShadowTaskLearningRun, error) {
	var run ShadowTaskLearningRun
	var raw string
	if err := queryer.QueryRow(`
		SELECT id, input_hash, policy_version, authority_effect, projection_json, created_at
		FROM shadow_task_learning_runs WHERE id = ?`,
		strings.TrimSpace(id),
	).Scan(
		&run.ID,
		&run.InputHash,
		&run.PolicyVersion,
		&run.AuthorityEffect,
		&raw,
		&run.CreatedAt,
	); err != nil {
		return ShadowTaskLearningRun{}, err
	}
	if err := json.Unmarshal([]byte(raw), &run.Projection); err != nil {
		return ShadowTaskLearningRun{}, err
	}
	return run, nil
}

func (s *ActivityStore) GetShadowTaskLearningRun(id string) (ShadowTaskLearningRun, error) {
	if s == nil || s.db == nil {
		return ShadowTaskLearningRun{}, errors.New("activity store is unavailable")
	}
	return getShadowTaskLearningRunTx(s.db, id)
}

func (s *ActivityStore) ListShadowTaskLearningRuns(limit int) ([]ShadowTaskLearningRun, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	rows, err := s.db.Query(`
		SELECT id, input_hash, policy_version, authority_effect, projection_json, created_at
		FROM shadow_task_learning_runs
		ORDER BY created_at DESC, id ASC
		LIMIT ?`,
		normalizeShadowTaskLearningLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ShadowTaskLearningRun, 0)
	for rows.Next() {
		var item ShadowTaskLearningRun
		var raw string
		if err := rows.Scan(
			&item.ID,
			&item.InputHash,
			&item.PolicyVersion,
			&item.AuthorityEffect,
			&raw,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item.Projection); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *ActivityStore) ListShadowTaskLearningRevisions(
	targetKind, decision string,
	limit int,
) ([]ShadowTaskLearningRevision, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	rows, err := s.db.Query(`
		SELECT run_id, candidate_json, created_at
		FROM shadow_task_learning_revisions
		WHERE (? = '' OR target_kind = ?) AND (? = '' OR decision = ?)
		ORDER BY created_at DESC, revision_id ASC
		LIMIT ?`,
		strings.TrimSpace(targetKind),
		strings.TrimSpace(targetKind),
		strings.TrimSpace(decision),
		strings.TrimSpace(decision),
		normalizeShadowTaskLearningLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ShadowTaskLearningRevision, 0)
	for rows.Next() {
		var item ShadowTaskLearningRevision
		var raw string
		if err := rows.Scan(&item.RunID, &raw, &item.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item.Candidate); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *ActivityStore) ListShadowTaskLearningEvaluationRecords(
	capabilityKey string,
) ([]ShadowTaskLearningEvaluationRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	rows, err := s.db.Query(`
		SELECT e.id, e.evaluation_json, e.created_at, r.capability_key,
			r.task_episode_id, r.id, r.task_context_hash, r.prompt_version,
			(
				SELECT transition.to_status
				FROM task_episode_transitions transition
				WHERE transition.task_episode_id = r.task_episode_id
				ORDER BY transition.sequence DESC LIMIT 1
			)
		FROM shadow_run_evaluations e
		JOIN shadow_runs r ON r.id = e.shadow_run_id
		WHERE r.mode = 'task_mirror'
			AND (? = '' OR r.capability_key = ?)
		ORDER BY e.created_at ASC, e.id ASC`,
		strings.TrimSpace(capabilityKey),
		strings.TrimSpace(capabilityKey),
	)
	if err != nil {
		return nil, err
	}
	type persistedEvaluation struct {
		evaluationID        string
		raw                 string
		persistedAt         int64
		candidateCapability string
		episodeID           string
		runID               string
		contextHash         string
		promptVersion       string
		status              string
	}
	persisted := make([]persistedEvaluation, 0)
	for rows.Next() {
		var item persistedEvaluation
		if err := rows.Scan(
			&item.evaluationID,
			&item.raw,
			&item.persistedAt,
			&item.candidateCapability,
			&item.episodeID,
			&item.runID,
			&item.contextHash,
			&item.promptVersion,
			&item.status,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
		persisted = append(persisted, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	// Exclusion checks traverse additional provenance tables. Finish and close
	// the evaluation cursor first so single-connection stores cannot deadlock.
	repeatedOnsets, err := s.taskEpisodeRepeatedOnsetSet()
	if err != nil {
		return nil, err
	}
	records := make([]ShadowTaskLearningEvaluationRecord, 0, len(persisted))
	for _, item := range persisted {
		var evaluation ShadowRunEvaluation
		if err := json.Unmarshal([]byte(item.raw), &evaluation); err != nil {
			return nil, err
		}
		if evaluation.TaskEvaluationInput == nil || evaluation.TaskEvaluation == nil {
			continue
		}
		excluded, err := s.isShadowTaskLearningEvaluationFirstRunExcluded(
			item.evaluationID,
			item.runID,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"check first-run exclusion for shadow run %s: %w",
				item.runID,
				err,
			)
		}
		if excluded {
			continue
		}
		episode, err := s.GetTaskEpisode(item.episodeID)
		if err != nil {
			return nil, fmt.Errorf(
				"load task episode %s for learning containment: %w",
				item.episodeID,
				err,
			)
		}
		if errors.Is(
			taskEpisodeFrozenContextSafetyError(episode),
			ErrTaskWorkflowInsufficientFrozenContext,
		) {
			continue
		}
		record := ShadowTaskLearningEvaluationRecord{
			EvaluationID:        item.evaluationID,
			PersistedAt:         item.persistedAt,
			TaskEpisodeStatus:   item.status,
			ShadowPromptVersion: item.promptVersion,
			ComparisonVersion:   evaluation.ComparisonVersion,
			EvaluatorVersion:    evaluation.EvaluatorVersion,
			CapabilityKey:       item.candidateCapability,
			TaskEpisodeID:       item.episodeID,
			ShadowRunID:         item.runID,
			FrozenContextHash:   item.contextHash,
			Input:               *evaluation.TaskEvaluationInput,
			Evaluation:          evaluation.TaskEvaluation,
		}
		if _, repeated := repeatedOnsets[item.episodeID]; repeated {
			record.ExclusionReason = taskEpisodeRepeatedOnsetReason
		}
		records = append(records, record)
	}
	return records, nil
}

func (a *App) BuildAndPersistShadowTaskLearning(
	capabilityKey string,
	createdAt int64,
) (ShadowTaskLearningRun, bool, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return ShadowTaskLearningRun{}, false, errors.New("activity store is unavailable")
	}
	records, err := a.activity.store.ListShadowTaskLearningEvaluationRecords(capabilityKey)
	if err != nil {
		return ShadowTaskLearningRun{}, false, err
	}
	projection, err := BuildShadowTaskLearningProjection(records)
	if err != nil {
		return ShadowTaskLearningRun{}, false, err
	}
	// Resolve semantic suppression before entering the short persistence lock.
	// Persist still rechecks the prepared receipt under the lock, closing a
	// concurrent forget without holding a mutation lock during model inference.
	for _, candidate := range projection.Candidates {
		if candidate.TargetKind != ShadowTaskLearningTargetMemory {
			continue
		}
		if err := a.prepareShadowTaskLearningMemory(candidate, createdAt); err != nil {
			return ShadowTaskLearningRun{}, false, err
		}
	}
	run, added, err := PersistShadowTaskLearningProjection(
		a.activity.store,
		projection,
		createdAt,
	)
	if err != nil {
		return run, added, err
	}
	if _, reconcileErr := a.reconcileShadowTaskLearningMaterializations(
		run.ID,
		createdAt,
	); reconcileErr != nil {
		return run, added, reconcileErr
	}
	if strings.TrimSpace(capabilityKey) == TaskWorkflowComposeText {
		if _, _, releaseErr := reconcileTaskShadowCapabilityReleaseFromStore(
			a.activity.store,
			createdAt,
		); releaseErr != nil {
			return run, added, releaseErr
		}
	}
	return run, added, nil
}

func shadowTaskLearningTitle(statement string) string {
	statement = strings.Join(strings.Fields(strings.TrimSpace(statement)), " ")
	if statement == "" {
		return "Evaluated work pattern"
	}
	const max = 80
	if len(statement) > max {
		return truncateAtRuneBoundary(statement, max) + "..."
	}
	return statement
}

func shadowTaskLearningTargetID(candidate ShadowTaskLearningCandidate) string {
	return "shadow-learning-" + hashShadowPayload([]byte(candidate.ProjectionKey))[:16]
}

func shadowTaskLearningMemoryContent(candidate ShadowTaskLearningCandidate, createdAt int64) string {
	return buildOKFMemoryMarkdown(
		shadowTaskLearningTargetID(candidate), shadowTaskLearningTitle(candidate.Statement),
		time.UnixMilli(createdAt), memorySource("shadow_task_evaluation"), "agentic_memory",
		shadowTaskLearningEvidencePoints(candidate),
	)
}

// shadowTaskLearningMemoryDocument is the memory the learning pass would write,
// with the read provenance of the sessions behind it already attached. The
// preparation pass and the final write both start from it: preparing the bare
// content would let the optional semantic probe send statements and evidence
// summaries of an app that is now sensitive to the model, because the missing
// dependencies would make the content look unrestricted.
func (a *App) shadowTaskLearningMemoryDocument(candidate ShadowTaskLearningCandidate, createdAt int64) string {
	var activityStore *ActivityStore
	if a != nil && a.activity != nil {
		activityStore = a.activity.store
	}
	return withMemoryReadProvenance(shadowTaskLearningMemoryContent(candidate, createdAt), shadowMemoryReadProvenance(activityStore, candidate))
}

// prepareShadowTaskLearningMemory resolves semantic suppression for one memory
// candidate before the short persistence lock.
func (a *App) prepareShadowTaskLearningMemory(candidate ShadowTaskLearningCandidate, createdAt int64) error {
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	return a.prepareMemoryObservedWrite(dir, shadowTaskLearningTargetID(candidate), a.shadowTaskLearningMemoryDocument(candidate, createdAt))
}

func shadowTaskLearningEvidencePoints(candidate ShadowTaskLearningCandidate) []string {
	points := []string{candidate.Statement}
	seen := map[string]struct{}{candidate.Statement: {}}
	for _, evidence := range candidate.Evidence {
		statement := strings.TrimSpace(evidence.Summary)
		if statement == "" {
			continue
		}
		if _, ok := seen[statement]; ok {
			continue
		}
		seen[statement] = struct{}{}
		points = append(points, statement)
	}
	return points
}

func (a *App) materializeShadowTaskLearningCandidate(
	candidate ShadowTaskLearningCandidate,
	createdAt int64,
) (ShadowTaskLearningMaterialization, error) {
	return a.materializeShadowTaskLearningCandidateAt(
		candidate,
		createdAt,
		createdAt,
	)
}

func (a *App) materializeShadowTaskLearningCandidateAt(
	candidate ShadowTaskLearningCandidate,
	targetCreatedAt, receiptCreatedAt int64,
) (ShadowTaskLearningMaterialization, error) {
	status := ShadowTaskLearningMaterializationSkipped
	targetID := ""
	errorText := ""
	excluded, exclusionErr := a.activity.store.
		isShadowTaskLearningCandidateFirstRunExcluded(candidate)
	if exclusionErr != nil {
		status = ShadowTaskLearningMaterializationDenied
		errorText = "first-run exclusion lookup failed: " + exclusionErr.Error()
	} else if excluded {
		status = ShadowTaskLearningMaterializationDenied
		errorText = firstRunContaminationReason
	} else if repeatedOnsets, repeatedErr := a.activity.store.
		taskEpisodeRepeatedOnsetSet(); repeatedErr != nil {
		status = ShadowTaskLearningMaterializationDenied
		errorText = "repeated task start lookup failed: " + repeatedErr.Error()
	} else if shadowTaskLearningCandidateUsesRepeatedOnset(
		candidate,
		repeatedOnsets,
	) {
		status = ShadowTaskLearningMaterializationDenied
		errorText = taskEpisodeRepeatedOnsetReason
	} else if corrected, correctionErr := a.activity.store.HasAgencyLearningCorrection(
		candidate.RevisionID,
		candidate.CapabilityKey,
		candidate.Statement,
	); correctionErr != nil {
		status = ShadowTaskLearningMaterializationDenied
		errorText = "learning correction lookup failed: " + correctionErr.Error()
	} else if corrected {
		status = ShadowTaskLearningMaterializationDenied
		errorText = "superseded by explicit user correction"
	} else if candidate.AuthorityEffect != ShadowTaskLearningAuthorityNone {
		status = ShadowTaskLearningMaterializationDenied
		errorText = "candidate attempted to change authority"
	} else if candidate.Qualification.Decision == ShadowTaskLearningDecisionQualified {
		targetID, errorText = a.applyQualifiedShadowTaskLearningCandidate(
			candidate,
			targetCreatedAt,
		)
		if errorText == "" {
			status = ShadowTaskLearningMaterializationMaterialized
		} else {
			status = ShadowTaskLearningMaterializationError
		}
	}
	receipt, _, err := a.activity.store.AddShadowTaskLearningMaterialization(
		ShadowTaskLearningMaterialization{
			RevisionID: candidate.RevisionID,
			TargetKind: candidate.TargetKind,
			TargetID:   targetID,
			Status:     status,
			Error:      errorText,
			CreatedAt:  receiptCreatedAt,
		},
	)
	if err != nil {
		return receipt, err
	}
	if status == ShadowTaskLearningMaterializationError {
		return receipt, errors.New(errorText)
	}
	return receipt, nil
}

func shadowTaskLearningCandidateUsesRepeatedOnset(
	candidate ShadowTaskLearningCandidate,
	repeatedOnsets map[string]struct{},
) bool {
	for _, evidence := range candidate.Evidence {
		if _, repeated := repeatedOnsets[evidence.TaskEpisodeID]; repeated {
			return true
		}
	}
	return false
}

func (a *App) applyQualifiedShadowTaskLearningCandidate(
	candidate ShadowTaskLearningCandidate,
	createdAt int64,
) (string, string) {
	targetID := shadowTaskLearningTargetID(candidate)
	when := time.UnixMilli(createdAt)
	switch candidate.TargetKind {
	case ShadowTaskLearningTargetCapabilityProfile:
		// The immutable revision is the capability profile read model. It is
		// intentionally not an authority grant and needs no second write.
		return candidate.RevisionID, ""
	case ShadowTaskLearningTargetMemory:
		content := a.shadowTaskLearningMemoryDocument(candidate, createdAt)
		dir, err := memoryDir()
		if err != nil {
			return "", err.Error()
		}
		path := filepath.Join(dir, targetID+".md")
		if existing, readErr := os.ReadFile(path); readErr == nil &&
			bytes.Equal(existing, []byte(content)) {
			return targetID, ""
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return "", readErr.Error()
		}
		if _, err := a.saveMemoryEntry(targetID, content, false); err != nil {
			return "", err.Error()
		}
		return targetID, ""
	case ShadowTaskLearningTargetKnowledge:
		dir, err := knowledgeDir()
		if err != nil {
			return "", err.Error()
		}
		unlock := memoryFileLocks.Acquire("knowledge:" + targetID)
		defer unlock()
		path := filepath.Join(dir, targetID+".md")
		existing, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return "", readErr.Error()
		}
		kind := "how_to"
		if doc, ok := parseOKFDocument(string(existing)); ok && validKnowHowKind(okfString(doc.Frontmatter, "kind")) {
			kind = okfString(doc.Frontmatter, "kind")
		}
		content := buildOKFKnowledgeMarkdown(
			targetID,
			shadowTaskLearningTitle(candidate.Statement),
			"Repeated, source-backed work pattern learned from evaluated user outcomes.",
			when,
			"shadow_task_evaluation",
			nil,
			nil,
			shadowTaskLearningEvidencePoints(candidate),
			kind,
		)
		if knowHowDocumentsEqualExceptKind(string(existing), content) {
			return targetID, ""
		}
		content = knowledgeProvenance(content, string(existing), []string{"shadow_task_evaluation"}, nil, true)
		if knowHowDocumentsEqualExceptKind(string(existing), content) {
			return targetID, ""
		}
		if err := snapshotKnowledgeFile(dir, targetID); err != nil {
			return "", err.Error()
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			return "", err.Error()
		}
		a.markMemoryDirty("shadow-task-learning")
		return targetID, ""
	case ShadowTaskLearningTargetTypedSelf:
		evidence := make([]SelfModelEvidence, 0, len(candidate.Evidence))
		for _, item := range candidate.Evidence {
			evidence = append(evidence, SelfModelEvidence{
				SourceKind: "shadow_task_evaluation",
				SourceID:   item.EvaluationID,
				Statement:  item.Summary,
				Confidence: candidate.QualificationConfidence(),
				SourceRefs: []string{item.EvidenceID},
				OccurredAt: item.OccurredAt,
			})
		}
		entry, _, err := a.activity.store.AddSelfModelEntry(SelfModelEntry{
			EntryKey:        candidate.ProjectionKey,
			Kind:            candidate.SuggestedSelfKind,
			Scope:           candidate.CapabilityKey,
			Statement:       candidate.Statement,
			Status:          candidate.SuggestedSelfStatus,
			Confidence:      candidate.QualificationConfidence(),
			ReasoningStatus: "source_backed_shadow_task_evaluation",
			Evidence:        evidence,
			CreatedAt:       createdAt,
		})
		if err != nil {
			return "", err.Error()
		}
		return entry.ID, ""
	default:
		return "", "unsupported shadow task learning target"
	}
}

func (candidate ShadowTaskLearningCandidate) QualificationConfidence() float64 {
	if candidate.Qualification.AverageEvidenceConfidence == nil {
		return 0
	}
	return clampConfidence(*candidate.Qualification.AverageEvidenceConfidence)
}

func (s *ActivityStore) AddShadowTaskLearningMaterialization(
	input ShadowTaskLearningMaterialization,
) (ShadowTaskLearningMaterialization, bool, error) {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	if s == nil || s.db == nil {
		return ShadowTaskLearningMaterialization{}, false, errors.New(
			"activity store is unavailable",
		)
	}
	input.RevisionID = strings.TrimSpace(input.RevisionID)
	input.TargetKind = strings.TrimSpace(input.TargetKind)
	input.TargetID = strings.TrimSpace(input.TargetID)
	if input.TargetKind == ShadowTaskLearningTargetMemory && input.TargetID != "" {
		if err := validateMemoryForgetSource(s.db, "memory", input.TargetID, ""); err != nil {
			return ShadowTaskLearningMaterialization{}, false, err
		}
	}
	status, err := shadowTaskLearningMaterializationStatus(input.Status)
	if err != nil {
		return ShadowTaskLearningMaterialization{}, false, err
	}
	input.Status = status
	input.Error = truncateShadowExecutionText(input.Error)
	if input.RevisionID == "" || input.TargetKind == "" || input.Status == "" || input.CreatedAt <= 0 {
		return ShadowTaskLearningMaterialization{}, false, errors.New(
			"shadow task learning materialization is incomplete",
		)
	}
	idPayload, _ := json.Marshal(struct {
		RevisionID string `json:"revisionId"`
		TargetKind string `json:"targetKind"`
		TargetID   string `json:"targetId"`
		Status     string `json:"status"`
		Error      string `json:"error"`
	}{
		RevisionID: input.RevisionID,
		TargetKind: input.TargetKind,
		TargetID:   input.TargetID,
		Status:     input.Status,
		Error:      input.Error,
	})
	input.ID = deterministicTaskEpisodeID(
		"shadow-task-learning-materialization",
		string(idPayload),
	)
	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO shadow_task_learning_materializations (
			id, revision_id, target_kind, target_id, status, error, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		input.ID,
		input.RevisionID,
		input.TargetKind,
		input.TargetID,
		input.Status,
		input.Error,
		input.CreatedAt,
	)
	if err != nil {
		return ShadowTaskLearningMaterialization{}, false, err
	}
	added, err := result.RowsAffected()
	if err != nil {
		return ShadowTaskLearningMaterialization{}, false, err
	}
	if added == 1 {
		return input, true, nil
	}
	var existing ShadowTaskLearningMaterialization
	if err := s.db.QueryRow(`
		SELECT id, revision_id, target_kind, target_id, status, error, created_at
		FROM shadow_task_learning_materializations
		WHERE id = ?`,
		input.ID,
	).Scan(
		&existing.ID,
		&existing.RevisionID,
		&existing.TargetKind,
		&existing.TargetID,
		&existing.Status,
		&existing.Error,
		&existing.CreatedAt,
	); err != nil {
		return ShadowTaskLearningMaterialization{}, false, err
	}
	if existing.RevisionID != input.RevisionID ||
		existing.TargetKind != input.TargetKind ||
		existing.TargetID != input.TargetID ||
		existing.Status != input.Status ||
		existing.Error != input.Error {
		return ShadowTaskLearningMaterialization{}, false, errors.New(
			"shadow task learning materialization key has a different immutable payload",
		)
	}
	return existing, false, nil
}

func (s *ActivityStore) HasMaterializedShadowTaskLearningRevision(
	revisionID, targetKind string,
) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("activity store is unavailable")
	}
	var count int
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM shadow_task_learning_materializations
		WHERE revision_id = ? AND target_kind = ? AND status = 'materialized'`,
		strings.TrimSpace(revisionID),
		strings.TrimSpace(targetKind),
	).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *ActivityStore) ListShadowTaskLearningMaterializations(
	limit int,
) ([]ShadowTaskLearningMaterialization, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity store is unavailable")
	}
	rows, err := s.db.Query(`
		SELECT id, revision_id, target_kind, target_id, status, error, created_at
		FROM shadow_task_learning_materializations
		ORDER BY created_at DESC, id ASC LIMIT ?`,
		normalizeShadowTaskLearningLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ShadowTaskLearningMaterialization, 0)
	for rows.Next() {
		var item ShadowTaskLearningMaterialization
		if err := rows.Scan(
			&item.ID,
			&item.RevisionID,
			&item.TargetKind,
			&item.TargetID,
			&item.Status,
			&item.Error,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func sortedShadowTaskLearningCandidateIDs(
	projection ShadowTaskLearningProjection,
) []string {
	result := make([]string, 0, len(projection.Candidates))
	for _, candidate := range projection.Candidates {
		result = append(result, candidate.RevisionID)
	}
	sort.Strings(result)
	return result
}
