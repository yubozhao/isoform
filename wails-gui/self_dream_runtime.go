package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"yansu-agent/agent"
)

const (
	selfDreamModelTimeout          = 3 * time.Minute
	selfDreamSchedulerInitialDelay = 2 * time.Minute
	selfDreamSchedulerInterval     = cadenceCheck
	selfDreamSchedulerMinInterval  = cadenceSelfDream
)

type selfDreamFrozenInput struct {
	Run               SelfDreamRunRecord
	IncludedSources   int
	EvidenceWatermark int64
}

type SelfDreamService struct {
	app *App

	mu        sync.Mutex
	running   bool
	cancel    context.CancelFunc
	stop      context.CancelFunc
	lastStart time.Time
}

func NewSelfDreamService(app *App) *SelfDreamService {
	return &SelfDreamService{app: app}
}

func (s *SelfDreamService) Start(parent context.Context) {
	s.mu.Lock()
	if s.stop != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.stop = cancel
	s.mu.Unlock()
	go s.runScheduler(ctx)
}

func (s *SelfDreamService) Stop() {
	s.mu.Lock()
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

func (s *SelfDreamService) runScheduler(ctx context.Context) {
	timer := time.NewTimer(selfDreamSchedulerInitialDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		s.schedule(ctx)
	}
	ticker := time.NewTicker(selfDreamSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.schedule(ctx)
		}
	}
}

func (s *SelfDreamService) schedule(parent context.Context) {
	s.mu.Lock()
	if s.running || (!s.lastStart.IsZero() &&
		time.Since(s.lastStart) < selfDreamSchedulerMinInterval) ||
		!s.app.claimDerivedRun("self_dream", selfDreamSchedulerMinInterval, time.Now()) {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.running = true
	s.cancel = cancel
	s.lastStart = time.Now()
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.cancel = nil
			s.mu.Unlock()
		}()
		if _, err := s.app.runSelfDreamShadow(ctx, "scheduled"); err != nil &&
			!errors.Is(err, context.Canceled) {
			log.Printf("[self-dream] scheduled run failed: %v", err)
		}
	}()
}

func selfDreamSourceAllowed(candidate TaskContextCandidate) bool {
	switch candidate.SourceKind {
	case taskContextSourceMemoryKind,
		taskContextSourceKnowledgeKind,
		taskContextSourceSelfKind,
		taskContextSourceInitiativeOutcomeKind,
		taskContextSourceTaskEpisodeOutcomeKind,
		"work_pattern_profile",
		"work_pattern_profile_correction":
		return true
	default:
		return false
	}
}

func selfDreamExcludedManifest(
	candidate TaskContextCandidate,
	reason string,
) TaskContextManifestEntry {
	return TaskContextManifestEntry{
		Ref:             candidate.Ref,
		Section:         candidate.Section,
		SourceKind:      candidate.SourceKind,
		SourceID:        candidate.SourceID,
		ObservedAt:      candidate.ObservedAt,
		EvidenceClass:   candidate.EvidenceClass,
		PrivacyDecision: candidate.PrivacyDecision,
		Included:        false,
		Reason:          reason,
	}
}

func filterSelfDreamCandidates(
	candidates []TaskContextCandidate,
	cutoffAt int64,
) ([]TaskContextCandidate, []TaskContextManifestEntry) {
	allowed := make([]TaskContextCandidate, 0, len(candidates))
	excluded := make([]TaskContextManifestEntry, 0)
	for _, candidate := range candidates {
		switch {
		case candidate.ObservedAt > cutoffAt:
			excluded = append(
				excluded,
				selfDreamExcludedManifest(
					candidate,
					TaskContextSourceReasonFuture,
				),
			)
		case !selfDreamSourceAllowed(candidate):
			excluded = append(
				excluded,
				selfDreamExcludedManifest(
					candidate,
					"self_dream_source_class_excluded",
				),
			)
		case candidate.EvidenceClass == TaskContextEvidenceObservedContext ||
			candidate.EvidenceClass == TaskContextEvidenceHiddenShadow ||
			candidate.EvidenceClass == TaskContextEvidenceAgentGenerated:
			excluded = append(
				excluded,
				selfDreamExcludedManifest(
					candidate,
					"self_dream_contamination_excluded",
				),
			)
		case candidate.EvidenceClass == TaskContextEvidenceVerifiedExternalResult:
			excluded = append(
				excluded,
				selfDreamExcludedManifest(
					candidate,
					"self_dream_external_result_excluded",
				),
			)
		case candidate.PrivacyDecision == TaskContextPrivacyExclude:
			excluded = append(
				excluded,
				selfDreamExcludedManifest(
					candidate,
					TaskContextSourceReasonPrivacy,
				),
			)
		default:
			allowed = append(allowed, candidate)
		}
	}
	return allowed, excluded
}

func selfDreamWorkPatternSources(
	store *ActivityStore,
	cutoffAt int64,
) ([]TaskContextCandidate, []TaskContextManifestEntry, error) {
	snapshot, err := store.GetLatestWorkPatternProfileSnapshotAt(cutoffAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	corrections, err := store.ListWorkPatternProfileCorrections(cutoffAt)
	if err != nil {
		return nil, nil, err
	}
	corrected := make(map[string]WorkPatternProfileCorrection)
	for _, correction := range corrections {
		key := correction.ScopeKind + "\x00" + correction.ScopeKey
		if current, ok := corrected[key]; !ok ||
			correction.Revision > current.Revision {
			corrected[key] = correction
		}
	}
	candidates := make([]TaskContextCandidate, 0)
	excluded := make([]TaskContextManifestEntry, 0)
	for _, projection := range snapshot.Projections {
		ref := strings.Join([]string{
			"work-profile",
			snapshot.ID,
			projection.ScopeKind,
			projection.ScopeKey,
		}, ":")
		candidate := TaskContextCandidate{
			Ref:        ref,
			Section:    TaskContextSectionWorkProfile,
			SourceKind: "work_pattern_profile",
			SourceID:   snapshot.ID,
			ObservedAt: snapshot.SourceCutoffAt,
			Content: strings.Join([]string{
				"Scope: " + projection.ScopeKind + ":" + projection.ScopeKey,
				"Observed pattern: " +
					workPatternProjectionStatement(projection),
				fmt.Sprintf(
					"Evidence: %d intervals across %d sessions",
					projection.SampleCount,
					projection.SessionCount,
				),
			}, "\n"),
			EvidenceClass:   TaskContextEvidenceProfileProjection,
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance:       0.85,
		}
		if snapshot.Status != WorkPatternProfileStatusQualified ||
			projection.Status != WorkPatternProfileStatusQualified ||
			projection.EvidenceKind != WorkPatternEvidenceObserved {
			excluded = append(
				excluded,
				selfDreamExcludedManifest(
					candidate,
					"insufficient_or_non_observed_work_pattern",
				),
			)
			continue
		}
		correctionKey := projection.ScopeKind + "\x00" + projection.ScopeKey
		if _, ok := corrected[correctionKey]; ok {
			excluded = append(
				excluded,
				selfDreamExcludedManifest(
					candidate,
					"explicitly_contradicted",
				),
			)
			continue
		}
		candidates = append(candidates, candidate)
	}
	for _, correction := range corrections {
		candidates = append(candidates, TaskContextCandidate{
			Ref: strings.Join([]string{
				"work-profile-correction",
				correction.ID,
			}, ":"),
			Section:    TaskContextSectionSelf,
			SourceKind: "work_pattern_profile_correction",
			SourceID:   correction.ID,
			ObservedAt: correction.CreatedAt,
			Content: strings.Join([]string{
				"Corrected scope: " +
					correction.ScopeKind + ":" + correction.ScopeKey,
				"Prior statement: " + correction.OriginalStatement,
				"Explicit correction: " + correction.Correction,
			}, "\n"),
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance:       1,
			Required:        true,
		})
	}
	return candidates, excluded, nil
}

func selfDreamSectionBudgets() map[string]int {
	return map[string]int{
		TaskContextSectionWorkProfile: 24_000,
		TaskContextSectionBoundaries:  16_000,
		TaskContextSectionCommitments: 16_000,
		TaskContextSectionSelf:        40_000,
		TaskContextSectionKnowledge:   48_000,
		TaskContextSectionOutcomes:    24_000,
		TaskContextSectionInitiatives: 0,
		TaskContextSectionAuthority:   0,
		TaskContextSectionActivity:    0,
	}
}

func buildSelfDreamStableInputHash(
	baseHash string,
	manifest []TaskContextManifestEntry,
	evidenceWatermark int64,
) (string, error) {
	type stableManifestEntry struct {
		Ref           string `json:"ref"`
		SourceKind    string `json:"sourceKind"`
		SourceID      string `json:"sourceId"`
		EvidenceClass string `json:"evidenceClass,omitempty"`
		Included      bool   `json:"included"`
		Reason        string `json:"reason,omitempty"`
		ContentHash   string `json:"contentHash,omitempty"`
		IncludedChars int    `json:"includedChars,omitempty"`
	}
	stable := make([]stableManifestEntry, 0, len(manifest))
	for _, item := range manifest {
		stable = append(stable, stableManifestEntry{
			Ref:           item.Ref,
			SourceKind:    item.SourceKind,
			SourceID:      item.SourceID,
			EvidenceClass: item.EvidenceClass,
			Included:      item.Included,
			Reason:        item.Reason,
			ContentHash:   item.ContentHash,
			IncludedChars: item.IncludedChars,
		})
	}
	sort.SliceStable(stable, func(i, j int) bool {
		if stable[i].SourceKind != stable[j].SourceKind {
			return stable[i].SourceKind < stable[j].SourceKind
		}
		if stable[i].SourceID != stable[j].SourceID {
			return stable[i].SourceID < stable[j].SourceID
		}
		return stable[i].Ref < stable[j].Ref
	})
	payload, err := json.Marshal(struct {
		PolicyVersion     string                `json:"policyVersion"`
		AssemblerVersion  string                `json:"assemblerVersion"`
		PromptVersion     string                `json:"promptVersion"`
		BaseSelfHash      string                `json:"baseSelfHash"`
		EvidenceWatermark int64                 `json:"evidenceWatermark"`
		Manifest          []stableManifestEntry `json:"manifest"`
	}{
		PolicyVersion:     SelfDreamPolicyVersion,
		AssemblerVersion:  SelfDreamAssemblerVersion,
		PromptVersion:     SelfDreamPromptVersion,
		BaseSelfHash:      baseHash,
		EvidenceWatermark: evidenceWatermark,
		Manifest:          stable,
	})
	if err != nil {
		return "", err
	}
	return hashShadowPayload(payload), nil
}

func buildSelfDreamPrompt(run SelfDreamRunRecord) string {
	var evidence strings.Builder
	for _, section := range run.Context.Payload.Sections {
		if len(section.Evidence) == 0 {
			continue
		}
		fmt.Fprintf(&evidence, "\n## %s\n", section.Name)
		for _, item := range section.Evidence {
			fmt.Fprintf(
				&evidence,
				"\nSOURCE_REF: %s\nSOURCE_KIND: %s\nSOURCE_ID: %s\nEVIDENCE_CLASS: %s\n%s\n",
				item.Ref,
				item.SourceKind,
				item.SourceID,
				item.EvidenceClass,
				item.Content,
			)
		}
	}
	baseJSON, _ := json.MarshalIndent(run.BaseSelf, "", "  ")
	return `You are the shadow-only Self Dream synthesizer for Yansu.

Your task is to propose a smaller, clearer, context-correct canonical Self from
frozen evidence. You do not edit anything. Your output will be validated and
measured against the current Self before any user review.

HARD RULES
----------
1. Return strict JSON only. Do not use markdown fences.
2. Allowed operations: merge, split, contextualize, contradict, supersede,
   no_change.
3. Cite only SOURCE_REF values present below.
4. Target only canonical Self entry IDs present in BASE SELF.
5. Do not grant authority, permission, approval, execution, schedules,
   Handoffs, or Initiatives.
6. Preserve every supported fact and contradiction. Prefer no_change over
   information loss.
7. Observed behavior supports what happened, not why. Set reasoningStatus to
   "unknown" unless explicit user evidence or repeated source-backed decisions
   directly support the rationale.
8. Use contextual scopes when a statement is not globally true. Allowed scope
   kinds: global, app, app_transition, work_mode, capability, project,
   relationship.
9. Corrections override inferred interpretations.
10. Propose the smallest useful operation set. Do not paraphrase merely for
    style.

OUTPUT SCHEMA
-------------
{
  "operations": [{
    "operation": "merge|split|contextualize|contradict|supersede|no_change",
    "targetEntryIds": ["canonical-self-entry-id"],
    "selfKind": "principle|preference|decision_policy|boundary",
    "scope": {"kind": "global", "key": ""},
    "statement": "required except for split/no_change",
    "splitStatements": ["required only for split"],
    "reasoningStatus": "unknown|explicit|repeated_source_backed",
    "rationale": "brief evidence-backed explanation; empty when unknown",
    "uncertainty": "what remains unknown",
    "confidence": 0.0,
    "evidenceRefs": ["exact SOURCE_REF"]
  }],
  "uncertainties": ["cross-operation uncertainty"]
}

For no_change, return one operation with empty targetEntryIds, an empty
statement, global scope, reasoningStatus "unknown", confidence 1, and the most
relevant evidenceRefs when available.

BASE SELF
---------
` + string(baseJSON) + `

FROZEN EVIDENCE
---------------` + evidence.String()
}

func decodeSelfDreamResponse(raw string) (SelfDreamModelResponse, error) {
	raw = strings.TrimSpace(stripCodeFences(raw))
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var response SelfDreamModelResponse
	if err := decoder.Decode(&response); err != nil {
		return SelfDreamModelResponse{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return SelfDreamModelResponse{}, errors.New(
				"Self Dream response contains multiple JSON values",
			)
		}
		return SelfDreamModelResponse{}, err
	}
	if len(response.Operations) == 0 {
		return SelfDreamModelResponse{}, errors.New(
			"Self Dream response contains no operations",
		)
	}
	if len(response.Operations) > 32 {
		return SelfDreamModelResponse{}, errors.New(
			"Self Dream response contains too many operations",
		)
	}
	response.Uncertainties = selfDreamBoundedList(
		response.Uncertainties,
		16,
		1_000,
	)
	return response, nil
}

func runSelfDreamAnalysis(
	ctx context.Context,
	runner agent.AgentRunner,
	model, prompt string,
) (SelfDreamModelResponse, string, error) {
	if runner == nil {
		return SelfDreamModelResponse{}, "", errors.New(
			"Self Dream model runner is not initialized",
		)
	}
	var raw strings.Builder
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:           agent.PriorityBackground,
		BackendHint:        agent.BackendHintLearning,
		AutoApproveTools:   true,
		DenyExternalWrites: true,
		Prompt:             prompt,
		Model:              strings.TrimSpace(model),
		PlanMode:           true,
		Ephemeral:          true,
		NoTools:            true,
		NoSystem:           true,
		SuppressTelemetry:  true,
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
	output := strings.TrimSpace(raw.String())
	if err != nil {
		return SelfDreamModelResponse{}, output, err
	}
	response, err := decodeSelfDreamResponse(output)
	return response, output, err
}

func (a *App) buildSelfDreamFrozenInput(
	cutoffAt int64,
) (selfDreamFrozenInput, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return selfDreamFrozenInput{}, errors.New(
			"activity store is unavailable",
		)
	}
	store := a.activity.store
	cutoffAt = selfDreamNow(cutoffAt)
	base, err := store.ListSelfModelEntriesAt(cutoffAt)
	if err != nil {
		return selfDreamFrozenInput{}, err
	}
	sort.SliceStable(base, func(i, j int) bool {
		if base[i].EntryKey != base[j].EntryKey {
			return base[i].EntryKey < base[j].EntryKey
		}
		return base[i].Revision < base[j].Revision
	})
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return selfDreamFrozenInput{}, err
	}
	baseHash := hashShadowPayload(baseJSON)
	sources, err := GatherTaskContextSources(TaskContextSourceRequest{
		Store:    store,
		CutoffAt: cutoffAt,
		Query: "user principles preferences decisions boundaries work patterns " +
			"goals communication relationships and repeated outcomes",
	})
	if err != nil {
		return selfDreamFrozenInput{}, err
	}
	workCandidates, workExcluded, err := selfDreamWorkPatternSources(
		store,
		cutoffAt,
	)
	if err != nil {
		return selfDreamFrozenInput{}, err
	}
	sources.Candidates = append(sources.Candidates, workCandidates...)
	preExcluded := taskContextSourceExclusionManifest(sources.Exclusions)
	preExcluded = append(preExcluded, workExcluded...)
	allowed, filteredExcluded := filterSelfDreamCandidates(
		sources.Candidates,
		cutoffAt,
	)
	preExcluded = append(preExcluded, filteredExcluded...)
	runID := "self-dream-" + hashShadowPayload(
		[]byte(fmt.Sprintf("%d:%s", cutoffAt, baseHash)),
	)[:24]
	contextValue, err := AssembleTaskContext(TaskContextAssemblyInput{
		EpisodeID:     runID,
		CutoffAt:      cutoffAt,
		CreatedAt:     cutoffAt,
		TotalBudget:   selfDreamContextBudgetChars,
		SectionBudget: selfDreamSectionBudgets(),
		Candidates:    allowed,
		PreExcluded:   preExcluded,
	})
	if err != nil {
		return selfDreamFrozenInput{}, err
	}
	includedCount := 0
	evidenceWatermark := int64(0)
	for _, item := range contextValue.Manifest {
		if item.Included {
			includedCount++
		}
		if item.ObservedAt > evidenceWatermark {
			evidenceWatermark = item.ObservedAt
		}
	}
	inputHash, err := buildSelfDreamStableInputHash(
		baseHash,
		contextValue.Manifest,
		evidenceWatermark,
	)
	if err != nil {
		return selfDreamFrozenInput{}, err
	}
	model := selfDreamModelID(
		a.chatTaskModel(),
		loadAIProviderConfig(),
	)
	run := SelfDreamRunRecord{
		ID:               runID,
		IdempotencyKey:   SelfDreamPolicyVersion + ":" + inputHash,
		TriggerKind:      "scheduled",
		PolicyVersion:    SelfDreamPolicyVersion,
		AssemblerVersion: SelfDreamAssemblerVersion,
		SourceCutoffAt:   cutoffAt,
		BaseSelf:         base,
		BaseSelfHash:     baseHash,
		Context:          contextValue,
		ContextHash:      contextValue.SHA256,
		Manifest:         contextValue.Manifest,
		InputHash:        inputHash,
		PromptVersion:    SelfDreamPromptVersion,
		RequestedModel:   model,
		CreatedAt:        cutoffAt,
	}
	run.Prompt = buildSelfDreamPrompt(run)
	run.PromptHash = hashShadowPayload([]byte(run.Prompt))
	return selfDreamFrozenInput{
		Run:               run,
		IncludedSources:   includedCount,
		EvidenceWatermark: evidenceWatermark,
	}, nil
}

func normalizeSelfDreamTrigger(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "manual":
		return "manual"
	case "correction":
		return "correction"
	case "startup":
		return "startup"
	default:
		return "scheduled"
	}
}

func (a *App) appendSelfDreamEvent(
	runID, kind string,
	detail any,
) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return
	}
	detailJSON, _ := selfDreamMarshal(detail)
	if _, err := a.activity.store.AddSelfDreamEvent(SelfDreamEventRecord{
		RunID:      runID,
		EventKind:  kind,
		DetailJSON: detailJSON,
		CreatedAt:  time.Now().UnixMilli(),
	}); err != nil {
		log.Printf("[self-dream] append %s event: %v", kind, err)
	}
}

func (a *App) runSelfDreamShadow(
	parent context.Context,
	trigger string,
) (SelfDreamRunDetail, error) {
	input, err := a.buildSelfDreamFrozenInput(time.Now().UnixMilli())
	if err != nil {
		return SelfDreamRunDetail{}, err
	}
	run := input.Run
	run.TriggerKind = normalizeSelfDreamTrigger(trigger)
	if input.IncludedSources < 2 && len(run.BaseSelf) < 2 {
		return SelfDreamRunDetail{}, errors.New(
			"Self Dream needs at least two durable evidence items",
		)
	}
	if cached, err := a.activity.store.GetCompletedSelfDreamRunByInput(
		run.PolicyVersion,
		run.InputHash,
	); err == nil {
		return a.activity.store.GetSelfDreamRunDetail(cached.ID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return SelfDreamRunDetail{}, err
	}
	persisted, added, err := a.activity.store.AddSelfDreamRun(run)
	if err != nil {
		return SelfDreamRunDetail{}, err
	}
	if !added {
		return a.activity.store.GetSelfDreamRunDetail(persisted.ID)
	}
	run = persisted
	a.appendSelfDreamEvent(run.ID, "created", map[string]any{
		"inputHash":         run.InputHash,
		"includedSources":   input.IncludedSources,
		"evidenceWatermark": input.EvidenceWatermark,
	})
	a.appendSelfDreamEvent(run.ID, "analyzing", map[string]any{
		"safetyMode": SelfDreamSafetyMode,
		"model":      run.RequestedModel,
	})

	deterministic := detectDeterministicSelfDreamOperations(
		run.BaseSelf,
		run.Manifest,
	)
	modelDrafts := []SelfDreamOperationDraft{}
	rawOutput := ""
	attemptStatus := "deterministic_only"
	attemptError := ""
	started := time.Now()
	if a.runner != nil && strings.TrimSpace(run.RequestedModel) != "" {
		ctx, cancel := context.WithTimeout(parent, selfDreamModelTimeout)
		response, raw, analysisErr := runSelfDreamAnalysis(
			ctx,
			a.runner,
			run.RequestedModel,
			run.Prompt,
		)
		cancel()
		rawOutput = raw
		if analysisErr != nil {
			attemptStatus = "failed"
			attemptError = analysisErr.Error()
		} else {
			attemptStatus = "completed"
			modelDrafts = response.Operations
		}
	}
	attempt, err := a.activity.store.AddSelfDreamAttempt(
		SelfDreamAttemptRecord{
			RunID:         run.ID,
			Status:        attemptStatus,
			Model:         run.RequestedModel,
			PromptHash:    run.PromptHash,
			RawOutput:     rawOutput,
			RawOutputHash: hashShadowPayload([]byte(rawOutput)),
			Error:         attemptError,
			DurationMs:    time.Since(started).Milliseconds(),
			CreatedAt:     time.Now().UnixMilli(),
		},
	)
	if err != nil {
		a.appendSelfDreamEvent(run.ID, "failed", map[string]any{
			"stage": "persist_attempt",
			"error": err.Error(),
		})
		return SelfDreamRunDetail{}, err
	}
	a.appendSelfDreamEvent(run.ID, "attempt_completed", map[string]any{
		"attemptId": attempt.ID,
		"status":    attempt.Status,
		"error":     attempt.Error,
	})

	now := time.Now().UnixMilli()
	operations := make([]SelfDreamOperationRecord, 0)
	seen := make(map[string]struct{})
	appendDrafts := func(drafts []SelfDreamOperationDraft, source string) {
		for _, draft := range drafts {
			operation := validateSelfDreamOperation(
				draft,
				source,
				run.BaseSelf,
				run.Manifest,
				now,
			)
			if _, duplicate := seen[operation.OperationHash]; duplicate {
				continue
			}
			seen[operation.OperationHash] = struct{}{}
			operation.RunID = run.ID
			operation.AttemptID = attempt.ID
			operations = append(operations, operation)
		}
	}
	appendDrafts(deterministic, "deterministic")
	appendDrafts(modelDrafts, "model")
	if len(operations) == 0 {
		appendDrafts([]SelfDreamOperationDraft{{
			Operation:       SelfDreamOperationNoChange,
			SelfKind:        selfModelKindPreference,
			Scope:           SelfDreamScope{Kind: "global"},
			ReasoningStatus: "unknown",
			Confidence:      1,
		}}, "deterministic")
	}
	operations, err = a.activity.store.AddSelfDreamOperations(operations)
	if err != nil {
		a.appendSelfDreamEvent(run.ID, "failed", map[string]any{
			"stage": "persist_operations",
			"error": err.Error(),
		})
		return SelfDreamRunDetail{}, err
	}
	a.engagementSelfDreamOperations(parent, operations)
	realEvidence := run.TriggerKind != "manual" &&
		input.IncludedSources > 0
	evaluation := evaluateSelfDreamProjection(
		run.ID,
		run.BaseSelf,
		operations,
		realEvidence,
		time.Now().UnixMilli(),
	)
	if _, err := a.activity.store.AddSelfDreamEvaluation(evaluation); err != nil {
		a.appendSelfDreamEvent(run.ID, "failed", map[string]any{
			"stage": "persist_evaluation",
			"error": err.Error(),
		})
		return SelfDreamRunDetail{}, err
	}
	a.appendSelfDreamEvent(run.ID, "evaluated", map[string]any{
		"improvement":  evaluation.Improvement,
		"coverageLoss": evaluation.CoverageLoss,
	})
	if attempt.Status == "failed" && len(deterministic) == 0 {
		a.appendSelfDreamEvent(run.ID, "failed", map[string]any{
			"stage": "model_analysis",
			"error": attempt.Error,
		})
		a.emitBrowserEvent("self_dream:updated", run.ID)
		return a.activity.store.GetSelfDreamRunDetail(run.ID)
	}
	a.appendSelfDreamEvent(run.ID, "completed", map[string]any{
		"operationCount":  len(operations),
		"productMutation": false,
	})
	if err := a.autoPromoteSelfDreamOperations(
		run,
		operations,
		evaluation,
	); err != nil {
		log.Printf("[self-dream] automatic promotion skipped: %v", err)
	}
	a.emitBrowserEvent("self_dream:updated", run.ID)
	return a.activity.store.GetSelfDreamRunDetail(run.ID)
}
