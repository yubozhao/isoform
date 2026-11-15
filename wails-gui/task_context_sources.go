package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	TaskContextSourceReasonFuture                    = "after_task_cutoff"
	TaskContextSourceReasonUnverifiable              = "historical_validity_unverifiable"
	TaskContextSourceReasonHiddenShadow              = "hidden_shadow_excluded"
	TaskContextSourceReasonEvaluator                 = "evaluator_prose_excluded"
	TaskContextSourceReasonUnsupportedAgent          = "unsupported_agent_claim_excluded"
	TaskContextSourceReasonPrivacy                   = "privacy_excluded"
	TaskContextSourceReasonMalformed                 = "malformed_source"
	TaskContextSourceReasonRetracted                 = "retracted_source"
	TaskContextSourceReasonInactive                  = "inactive_source"
	TaskContextSourceReasonSuperseded                = "superseded_before_cutoff"
	TaskContextSourceReasonDuplicateConflict         = "duplicate_ref_conflict"
	TaskContextSourceReasonGuidedFirstRun            = "guided_first_run_excluded"
	TaskContextSourceReasonOutcomeNotVerified        = "outcome_not_verified"
	TaskContextSourceReasonOutcomeEvaluatorMismatch  = "outcome_evaluator_policy_mismatch"
	TaskContextSourceReasonOutcomeGenerationMismatch = "outcome_generation_mismatch"
	TaskContextSourceReasonOutcomeExternalContract   = "outcome_external_result_contract_mismatch"
	TaskContextSourceReasonOutcomeFeedbackInvalid    = "outcome_feedback_contract_mismatch"
	TaskContextSourceReasonPurposeUnsupported        = "purpose_open_loop_not_supported"
	TaskContextSourceReasonPurposeTerminal           = "purpose_open_loop_terminal"
	TaskContextSourceReasonPurposeDuplicate          = "purpose_open_loop_duplicate"
	TaskContextSourceReasonLearningAfterCutoff       = "learning_materialized_after_task_cutoff"
	TaskContextSourceReasonLearningMissingReceipt    = "learning_materialization_receipt_missing"
	TaskContextSourceReasonLearningInvalidReceipt    = "learning_materialization_receipt_invalid"
	TaskContextSourceReasonLearningNotQualified      = "learning_not_qualified"
	TaskContextSourceReasonLearningSuperseded        = "learning_revision_superseded"
	TaskContextSourceReasonLearningCorrected         = "learning_superseded_by_user_correction"
	TaskContextSourceReasonLearningTargetMissing     = "learning_materialization_target_missing"
	TaskContextSourceReasonLearningTargetMismatch    = "learning_materialization_target_mismatch"
	TaskContextSourceReasonLearningFirstRun          = "learning_guided_first_run_excluded"
	TaskContextSourceReasonLearningPolicyMismatch    = "learning_policy_mismatch"
	TaskContextSourceReasonLearningRepeatedOnset     = "learning_repeated_task_start"
	TaskContextSourceReasonRepeatedTaskStart         = "repeated_task_start"
	taskContextSourceMemoryKind                      = "memory_markdown"
	taskContextSourceKnowledgeKind                   = "knowledge_markdown"
	taskContextSourceSelfKind                        = "self_model_entry"
	taskContextSourcePurposeOpenLoopKind             = "purpose_open_loop"
	taskContextSourceLegacyInitiativeKind            = "legacy_initiative"
	taskContextSourceAuthorityKind                   = "authority_grant"
	taskContextSourceInitiativeOutcomeKind           = "initiative_outcome"
	taskContextSourceTaskEpisodeOutcomeKind          = "task_episode_outcome"
	taskContextSourceVerifiedShadowLearningKind      = "verified_shadow_learning"
	taskContextSourceDefaultMemoryRelevance          = 0.55
	taskContextSourceDefaultKnowledgeRelevance       = 0.60
	taskContextSourceDefaultSelfRelevance            = 0.75
	taskContextSourceDefaultPurposeRelevance         = 0.92
	taskContextSourceDefaultInitiativeRelevance      = 0.80
	taskContextSourceDefaultOutcomeRelevance         = 0.70
	taskContextSourceDefaultFeedbackRelevance        = 0.90
	taskContextSourceDefaultRecentActivityRelevance  = 0.90
)

// TaskContextSourceRequest defines the complete source scan for a task-start
// cutoff. RecentActivity is supplied by the caller because it can include
// in-memory observations that have not yet reached a durable store.
type TaskContextSourceRequest struct {
	Store          *ActivityStore         `json:"-"`
	CutoffAt       int64                  `json:"cutoffAt"`
	Query          string                 `json:"query,omitempty"`
	RecentActivity []TaskContextCandidate `json:"recentActivity,omitempty"`
}

// TaskContextSourceExclusion preserves why a discovered source did not become
// an assembler candidate. It intentionally contains no source content.
type TaskContextSourceExclusion struct {
	Ref        string                         `json:"ref"`
	SourceKind string                         `json:"sourceKind"`
	SourceID   string                         `json:"sourceId"`
	ObservedAt int64                          `json:"observedAt,omitempty"`
	Reason     string                         `json:"reason"`
	Learning   *TaskContextLearningProvenance `json:"learning,omitempty"`
}

type TaskContextSourceResult struct {
	Candidates []TaskContextCandidate       `json:"candidates"`
	Exclusions []TaskContextSourceExclusion `json:"exclusions"`
}

type taskContextSourceCollector struct {
	cutoffAt int64
	query    string
	result   TaskContextSourceResult
	seen     map[string]TaskContextCandidate
	excluded map[string]struct{}
}

type taskContextMarkdownVersion struct {
	sourceID     string
	versionID    string
	content      string
	source       string
	dimension    string
	observedAt   int64
	persistedAt  int64
	explicitTime bool
	live         bool
}

func GatherTaskContextSources(
	request TaskContextSourceRequest,
) (TaskContextSourceResult, error) {
	if request.CutoffAt <= 0 {
		return TaskContextSourceResult{}, errors.New(
			"task context source gathering requires a task inferred-start cutoff",
		)
	}
	collector := &taskContextSourceCollector{
		cutoffAt: request.CutoffAt,
		query:    strings.TrimSpace(request.Query),
		result: TaskContextSourceResult{
			Candidates: []TaskContextCandidate{},
			Exclusions: []TaskContextSourceExclusion{},
		},
		seen:     make(map[string]TaskContextCandidate),
		excluded: make(map[string]struct{}),
	}

	baseDir, err := flavorDataDir()
	if err != nil {
		return TaskContextSourceResult{}, fmt.Errorf(
			"resolve home for task context sources: %w",
			err,
		)
	}
	if err := collector.gatherMemoryMarkdown(
		filepath.Join(baseDir, "memory"),
	); err != nil {
		return TaskContextSourceResult{}, err
	}
	if err := collector.gatherKnowledgeMarkdown(
		filepath.Join(baseDir, "knowledge"),
	); err != nil {
		return TaskContextSourceResult{}, err
	}

	if request.Store != nil && request.Store.db != nil {
		if err := collector.gatherTypedSelf(request.Store.db); err != nil {
			return TaskContextSourceResult{}, err
		}
		if err := collector.gatherPurposeOpenLoops(request.Store.db); err != nil {
			return TaskContextSourceResult{}, err
		}
		if err := collector.gatherLegacyInitiatives(request.Store.db); err != nil {
			return TaskContextSourceResult{}, err
		}
		if err := collector.gatherAuthorityConstraints(request.Store.db); err != nil {
			return TaskContextSourceResult{}, err
		}
		if err := collector.gatherPriorOutcomes(request.Store.db); err != nil {
			return TaskContextSourceResult{}, err
		}
	}
	collector.gatherRecentActivity(request.RecentActivity)
	collector.sort()
	return collector.result, nil
}

func (collector *taskContextSourceCollector) sort() {
	sort.SliceStable(collector.result.Candidates, func(i, j int) bool {
		left := collector.result.Candidates[i]
		right := collector.result.Candidates[j]
		if left.Section != right.Section {
			return taskContextSectionIndex(left.Section) <
				taskContextSectionIndex(right.Section)
		}
		if left.SourceKind != right.SourceKind {
			return left.SourceKind < right.SourceKind
		}
		if left.SourceID != right.SourceID {
			return left.SourceID < right.SourceID
		}
		if left.ObservedAt != right.ObservedAt {
			return left.ObservedAt < right.ObservedAt
		}
		return left.Ref < right.Ref
	})
	sort.SliceStable(collector.result.Exclusions, func(i, j int) bool {
		left := collector.result.Exclusions[i]
		right := collector.result.Exclusions[j]
		if left.SourceKind != right.SourceKind {
			return left.SourceKind < right.SourceKind
		}
		if left.SourceID != right.SourceID {
			return left.SourceID < right.SourceID
		}
		if left.ObservedAt != right.ObservedAt {
			return left.ObservedAt < right.ObservedAt
		}
		if left.Reason != right.Reason {
			return left.Reason < right.Reason
		}
		return left.Ref < right.Ref
	})
}

func (collector *taskContextSourceCollector) exclude(
	ref, sourceKind, sourceID string,
	observedAt int64,
	reason string,
) {
	sourceKind = strings.TrimSpace(sourceKind)
	sourceID = strings.TrimSpace(sourceID)
	reason = strings.TrimSpace(reason)
	if ref = strings.TrimSpace(ref); ref == "" {
		ref = taskContextSourceRef(
			"excluded:"+sourceKind,
			sourceID,
			observedAt,
			reason,
		)
	}
	key := strings.Join([]string{
		ref,
		sourceKind,
		sourceID,
		strconv.FormatInt(observedAt, 10),
		reason,
	}, "\x00")
	if _, ok := collector.excluded[key]; ok {
		return
	}
	collector.excluded[key] = struct{}{}
	collector.result.Exclusions = append(
		collector.result.Exclusions,
		TaskContextSourceExclusion{
			Ref:        ref,
			SourceKind: sourceKind,
			SourceID:   sourceID,
			ObservedAt: observedAt,
			Reason:     reason,
		},
	)
}

func (collector *taskContextSourceCollector) addExclusion(
	exclusion TaskContextSourceExclusion,
) {
	before := len(collector.result.Exclusions)
	collector.exclude(
		exclusion.Ref,
		exclusion.SourceKind,
		exclusion.SourceID,
		exclusion.ObservedAt,
		exclusion.Reason,
	)
	if exclusion.Learning != nil && len(collector.result.Exclusions) > before {
		collector.result.Exclusions[len(collector.result.Exclusions)-1].Learning =
			exclusion.Learning
	}
}

func (collector *taskContextSourceCollector) add(
	candidate TaskContextCandidate,
) {
	candidate.SourceKind = strings.TrimSpace(candidate.SourceKind)
	candidate.SourceID = strings.TrimSpace(candidate.SourceID)
	candidate.Content = strings.TrimSpace(candidate.Content)
	if candidate.Ref = strings.TrimSpace(candidate.Ref); candidate.Ref == "" {
		candidate.Ref = taskContextSourceRef(
			candidate.SourceKind,
			candidate.SourceID,
			candidate.ObservedAt,
			candidate.Content,
		)
	}
	if candidate.SourceKind == "" || candidate.SourceID == "" ||
		candidate.Content == "" || candidate.ObservedAt <= 0 {
		collector.exclude(
			candidate.Ref,
			candidate.SourceKind,
			candidate.SourceID,
			candidate.ObservedAt,
			TaskContextSourceReasonUnverifiable,
		)
		return
	}
	if candidate.ObservedAt > collector.cutoffAt {
		collector.exclude(
			candidate.Ref,
			candidate.SourceKind,
			candidate.SourceID,
			candidate.ObservedAt,
			TaskContextSourceReasonFuture,
		)
		return
	}
	if reason := taskContextForbiddenSourceReason(
		candidate.SourceKind,
	); reason != "" &&
		candidate.SourceKind != taskContextSourceVerifiedShadowLearningKind {
		collector.exclude(
			candidate.Ref,
			candidate.SourceKind,
			candidate.SourceID,
			candidate.ObservedAt,
			reason,
		)
		return
	}
	switch candidate.EvidenceClass {
	case TaskContextEvidenceHiddenShadow:
		collector.exclude(
			candidate.Ref,
			candidate.SourceKind,
			candidate.SourceID,
			candidate.ObservedAt,
			TaskContextSourceReasonHiddenShadow,
		)
		return
	case TaskContextEvidenceAgentGenerated:
		collector.exclude(
			candidate.Ref,
			candidate.SourceKind,
			candidate.SourceID,
			candidate.ObservedAt,
			TaskContextSourceReasonUnsupportedAgent,
		)
		return
	}
	if candidate.PrivacyDecision == TaskContextPrivacyExclude {
		collector.exclude(
			candidate.Ref,
			candidate.SourceKind,
			candidate.SourceID,
			candidate.ObservedAt,
			TaskContextSourceReasonPrivacy,
		)
		return
	}
	normalized, err := normalizeTaskContextCandidate(
		candidate,
		collector.cutoffAt,
	)
	if err != nil {
		collector.exclude(
			candidate.Ref,
			candidate.SourceKind,
			candidate.SourceID,
			candidate.ObservedAt,
			TaskContextSourceReasonMalformed,
		)
		return
	}
	if previous, ok := collector.seen[normalized.Ref]; ok {
		previousJSON, _ := json.Marshal(previous)
		currentJSON, _ := json.Marshal(normalized)
		if string(previousJSON) != string(currentJSON) {
			collector.exclude(
				normalized.Ref,
				normalized.SourceKind,
				normalized.SourceID,
				normalized.ObservedAt,
				TaskContextSourceReasonDuplicateConflict,
			)
		}
		return
	}
	collector.seen[normalized.Ref] = normalized
	collector.result.Candidates = append(
		collector.result.Candidates,
		normalized,
	)
}

func taskContextSourceRef(
	sourceKind, sourceID string,
	observedAt int64,
	content string,
) string {
	return strings.Join([]string{
		strings.TrimSpace(sourceKind),
		strings.TrimSpace(sourceID),
		strconv.FormatInt(observedAt, 10),
		taskContextHash(strings.TrimSpace(content))[:16],
	}, ":")
}

func taskContextForbiddenSourceReason(source string) string {
	normalized := strings.ToLower(strings.TrimSpace(source))
	replacer := strings.NewReplacer("-", "_", ".", "_", ":", "_", "/", "_")
	normalized = replacer.Replace(normalized)
	switch {
	case strings.Contains(normalized, "shadow"):
		return TaskContextSourceReasonHiddenShadow
	case strings.Contains(normalized, "evaluator"),
		strings.Contains(normalized, "evaluation"):
		return TaskContextSourceReasonEvaluator
	case normalized == "agent",
		normalized == "assistant",
		normalized == "llm",
		strings.Contains(normalized, "agent_generated"),
		strings.Contains(normalized, "model_output"),
		strings.Contains(normalized, "assistant_claim"):
		return TaskContextSourceReasonUnsupportedAgent
	default:
		return ""
	}
}

func taskContextSourceRelevance(query, content string, base float64) float64 {
	if base < 0 {
		base = 0
	}
	if base > 1 {
		base = 1
	}
	queryWords := selfModelWords(query)
	if len(queryWords) == 0 {
		return base
	}
	contentWords := selfModelWords(content)
	matches := 0
	for word := range queryWords {
		if _, ok := contentWords[word]; ok {
			matches++
		}
	}
	score := base + 0.35*(float64(matches)/float64(len(queryWords)))
	if score > 1 {
		return 1
	}
	return score
}

func taskContextMarkdownTimestamp(
	content string,
	fallback int64,
) (int64, bool) {
	if doc, ok := parseOKFDocument(content); ok {
		if parsed := parseOKFTimestampMs(
			okfString(doc.Frontmatter, "timestamp"),
		); parsed > 0 {
			return parsed, true
		}
	}
	if parsed := parseMemoryFrontmatter(content).UpdatedAt; parsed > 0 {
		return parsed, true
	}
	entry := parseKnowledgeFile("source.md", content)
	if parsed := taskContextParseSourceTime(entry.Date); parsed > 0 {
		return parsed, true
	}
	return fallback, false
}

func taskContextParseSourceTime(value string) int64 {
	if parsed := parseOKFTimestampMs(value); parsed > 0 {
		return parsed
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02",
		"January 2, 2006",
		"Jan 2, 2006",
	} {
		if parsed, err := time.ParseInLocation(
			layout,
			strings.TrimSpace(value),
			time.Local,
		); err == nil {
			return parsed.UnixMilli()
		}
	}
	return 0
}

func taskContextMarkdownSource(content string) string {
	if doc, ok := parseOKFDocument(content); ok {
		return strings.TrimSpace(okfString(doc.Frontmatter, "source"))
	}
	if source := strings.TrimSpace(
		parseMemoryFrontmatter(content).Source,
	); source != "" {
		return source
	}
	for _, line := range strings.SplitN(content, "\n", 40) {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{
			"- **Source**:",
			"**Source**:",
		} {
			if value, ok := strings.CutPrefix(line, prefix); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func taskContextMarkdownVersions(
	dir, historyDirName, sourceKind, sourceID string,
	live os.DirEntry,
	cutoffAt int64,
) ([]taskContextMarkdownVersion, []TaskContextSourceExclusion) {
	versions := make([]taskContextMarkdownVersion, 0)
	exclusions := make([]TaskContextSourceExclusion, 0)
	appendVersion := func(
		path, versionID string,
		fallback int64,
		liveVersion bool,
	) {
		data, err := os.ReadFile(path)
		if err != nil {
			exclusions = append(exclusions, TaskContextSourceExclusion{
				Ref: taskContextSourceRef(
					"excluded:"+sourceKind,
					sourceID+":"+versionID,
					fallback,
					TaskContextSourceReasonMalformed,
				),
				SourceKind: sourceKind,
				SourceID:   sourceID + ":" + versionID,
				ObservedAt: fallback,
				Reason:     TaskContextSourceReasonMalformed,
			})
			return
		}
		content := string(data)
		observedAt, explicit := taskContextMarkdownTimestamp(content, fallback)
		versions = append(versions, taskContextMarkdownVersion{
			sourceID:     sourceID,
			versionID:    versionID,
			content:      content,
			source:       taskContextMarkdownSource(content),
			dimension:    extractMemoryDimension(content),
			observedAt:   observedAt,
			persistedAt:  fallback,
			explicitTime: explicit,
			live:         liveVersion,
		})
	}

	info, infoErr := live.Info()
	if infoErr != nil {
		exclusions = append(exclusions, TaskContextSourceExclusion{
			Ref: taskContextSourceRef(
				"excluded:"+sourceKind,
				sourceID+":live",
				0,
				TaskContextSourceReasonUnverifiable,
			),
			SourceKind: sourceKind,
			SourceID:   sourceID + ":live",
			Reason:     TaskContextSourceReasonUnverifiable,
		})
	} else {
		appendVersion(
			filepath.Join(dir, live.Name()),
			"live",
			info.ModTime().UnixMilli(),
			true,
		)
	}

	historyDir := filepath.Join(dir, historyDirName, sourceID)
	history, err := os.ReadDir(historyDir)
	if err != nil && !os.IsNotExist(err) {
		exclusions = append(exclusions, TaskContextSourceExclusion{
			Ref: taskContextSourceRef(
				"excluded:"+sourceKind,
				sourceID+":history",
				0,
				TaskContextSourceReasonMalformed,
			),
			SourceKind: sourceKind,
			SourceID:   sourceID + ":history",
			Reason:     TaskContextSourceReasonMalformed,
		})
	}
	for _, entry := range history {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(entry.Name(), ".md")
		persistedAt, parseErr := strconv.ParseInt(stem, 10, 64)
		if parseErr != nil || persistedAt <= 0 {
			persistedAt = 0
		}
		appendVersion(
			filepath.Join(historyDir, entry.Name()),
			"history:"+entry.Name(),
			persistedAt,
			false,
		)
	}

	for index := range versions {
		version := &versions[index]
		if version.observedAt <= 0 {
			continue
		}
		if version.live || version.explicitTime {
			continue
		}
		if version.observedAt > cutoffAt {
			// A history snapshot taken after the cutoff does not establish
			// when undated content first became valid.
			version.observedAt = 0
		}
	}
	return versions, exclusions
}

func (collector *taskContextSourceCollector) gatherMarkdownDir(
	dir, historyDirName, sourceKind string,
	build func(taskContextMarkdownVersion) TaskContextCandidate,
) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s task context sources: %w", sourceKind, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") ||
			isOKFReservedMarkdownFile(entry.Name()) {
			continue
		}
		sourceID := strings.TrimSuffix(entry.Name(), ".md")
		versions, readExclusions := taskContextMarkdownVersions(
			dir,
			historyDirName,
			sourceKind,
			sourceID,
			entry,
			collector.cutoffAt,
		)
		for _, exclusion := range readExclusions {
			collector.exclude(
				exclusion.Ref,
				exclusion.SourceKind,
				exclusion.SourceID,
				exclusion.ObservedAt,
				exclusion.Reason,
			)
		}
		valid := make([]taskContextMarkdownVersion, 0, len(versions))
		for _, version := range versions {
			versionSourceID := sourceID + ":" + version.versionID
			ref := taskContextSourceRef(
				sourceKind,
				versionSourceID,
				version.observedAt,
				version.content,
			)
			switch {
			case version.observedAt <= 0:
				collector.exclude(
					ref,
					sourceKind,
					versionSourceID,
					version.observedAt,
					TaskContextSourceReasonUnverifiable,
				)
			case version.observedAt > collector.cutoffAt:
				collector.exclude(
					ref,
					sourceKind,
					versionSourceID,
					version.observedAt,
					TaskContextSourceReasonFuture,
				)
			case version.live && version.persistedAt > collector.cutoffAt:
				// A current file touched after task start may contain later
				// manual edits even when its embedded timestamp is stale.
				// Only a version-history snapshot can safely recover the
				// earlier contents in that case.
				collector.exclude(
					ref,
					sourceKind,
					versionSourceID,
					version.persistedAt,
					TaskContextSourceReasonUnverifiable,
				)
			default:
				valid = append(valid, version)
			}
		}
		if len(valid) == 0 {
			continue
		}
		sort.SliceStable(valid, func(i, j int) bool {
			if valid[i].observedAt != valid[j].observedAt {
				return valid[i].observedAt > valid[j].observedAt
			}
			if valid[i].live != valid[j].live {
				return valid[i].live
			}
			return valid[i].versionID > valid[j].versionID
		})
		selected := valid[0]
		if sourceKind == taskContextSourceMemoryKind {
			// Forgetting applies now, including when a historical cutoff would
			// otherwise recover an older version for Self Dream or Purpose.
			forgotten, err := isMemoryForgotten(dir, sourceID, selected.content, time.Now())
			if err != nil {
				return err
			}
			if forgotten {
				collector.exclude(
					taskContextSourceRef(sourceKind, sourceID+":"+selected.versionID, selected.observedAt, selected.content),
					sourceKind, sourceID, selected.observedAt, TaskContextSourceReasonRetracted,
				)
				continue
			}
		}
		for _, superseded := range valid[1:] {
			collector.exclude(
				taskContextSourceRef(
					sourceKind,
					sourceID+":"+superseded.versionID,
					superseded.observedAt,
					superseded.content,
				),
				sourceKind,
				sourceID+":"+superseded.versionID,
				superseded.observedAt,
				TaskContextSourceReasonSuperseded,
			)
		}
		if reason := taskContextForbiddenSourceReason(
			selected.source,
		); reason != "" {
			collector.exclude(
				taskContextSourceRef(
					sourceKind,
					sourceID+":"+selected.versionID,
					selected.observedAt,
					selected.content,
				),
				sourceKind,
				sourceID,
				selected.observedAt,
				reason,
			)
			continue
		}
		collector.add(build(selected))
	}
	return nil
}

func taskContextMemorySection(dimension string) string {
	switch strings.ToLower(strings.TrimSpace(dimension)) {
	case "deep_user_model", "agentic_memory", "user_insight":
		return TaskContextSectionSelf
	default:
		return TaskContextSectionKnowledge
	}
}

func taskContextMarkdownEvidenceClass(
	source, dimension string,
) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "manual" || source == "user" || source == "user_edit" ||
		source == "edit" || (source == "" && dimension == "custom") {
		return TaskContextEvidenceExplicitUser
	}
	return TaskContextEvidenceEvaluatedProjection
}

func (collector *taskContextSourceCollector) gatherMemoryMarkdown(
	dir string,
) error {
	return collector.gatherMarkdownDir(
		dir,
		memoryHistoryDirName,
		taskContextSourceMemoryKind,
		func(version taskContextMarkdownVersion) TaskContextCandidate {
			section := taskContextMemorySection(version.dimension)
			return TaskContextCandidate{
				Ref: taskContextSourceRef(
					taskContextSourceMemoryKind,
					version.sourceID,
					version.observedAt,
					version.content,
				),
				Section:    section,
				SourceKind: taskContextSourceMemoryKind,
				SourceID:   version.sourceID,
				ObservedAt: version.observedAt,
				Content:    version.content,
				EvidenceClass: taskContextMarkdownEvidenceClass(
					version.source,
					version.dimension,
				),
				PrivacyDecision: TaskContextPrivacyRedact,
				Relevance: taskContextSourceRelevance(
					collector.query,
					version.content,
					taskContextSourceDefaultMemoryRelevance,
				),
			}
		},
	)
}

func (collector *taskContextSourceCollector) gatherKnowledgeMarkdown(
	dir string,
) error {
	return collector.gatherMarkdownDir(
		dir,
		knowledgeHistoryDirName,
		taskContextSourceKnowledgeKind,
		func(version taskContextMarkdownVersion) TaskContextCandidate {
			return TaskContextCandidate{
				Ref: taskContextSourceRef(
					taskContextSourceKnowledgeKind,
					version.sourceID,
					version.observedAt,
					version.content,
				),
				Section:    TaskContextSectionKnowledge,
				SourceKind: taskContextSourceKnowledgeKind,
				SourceID:   version.sourceID,
				ObservedAt: version.observedAt,
				Content:    version.content,
				EvidenceClass: taskContextMarkdownEvidenceClass(
					version.source,
					"",
				),
				PrivacyDecision: TaskContextPrivacyRedact,
				Relevance: taskContextSourceRelevance(
					collector.query,
					version.content,
					taskContextSourceDefaultKnowledgeRelevance,
				),
			}
		},
	)
}

func taskContextTableExists(db *sql.DB, name string) (bool, error) {
	var exists int
	err := db.QueryRow(`
		SELECT 1
		FROM sqlite_master
		WHERE type = 'table' AND name = ?
		LIMIT 1`,
		name,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func taskContextSelfEvidenceClass(reasoningStatus string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(reasoningStatus)) {
	case "explicit_user_control",
		"behavior_and_explicit_approval",
		"explicit_user",
		"explicit_user_correction",
		"self_dream_user_correction",
		"user_confirmed":
		return TaskContextEvidenceExplicitUser, true
	case "source_backed",
		"evaluated_projection",
		"repeated_source_backed":
		return TaskContextEvidenceEvaluatedProjection, true
	default:
		return "", false
	}
}

func (collector *taskContextSourceCollector) gatherTypedSelf(
	db *sql.DB,
) error {
	exists, err := taskContextTableExists(db, "self_model_entries")
	if err != nil {
		return fmt.Errorf("inspect typed Self sources: %w", err)
	}
	if !exists {
		return nil
	}
	rows, err := db.Query(`
		SELECT
			current.id,
			current.entry_key,
			current.revision,
			current.kind,
			current.scope,
			current.statement,
			current.status,
			current.confidence,
			current.reasoning_status,
			current.evidence_json,
			current.contradictions_json,
			current.created_at
		FROM self_model_entries current
		WHERE current.created_at <= ?
			AND current.revision = (
				SELECT MAX(candidate.revision)
				FROM self_model_entries candidate
				WHERE candidate.entry_key = current.entry_key
					AND candidate.created_at <= ?
			)
		ORDER BY current.entry_key ASC, current.revision ASC`,
		collector.cutoffAt,
		collector.cutoffAt,
	)
	if err != nil {
		return fmt.Errorf("query typed Self sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, entryKey, kind, scope, statement string
			status, reasoningStatus              string
			evidenceJSON, contradictionsJSON     string
			revision                             int
			confidence                           float64
			createdAt                            int64
		)
		if err := rows.Scan(
			&id,
			&entryKey,
			&revision,
			&kind,
			&scope,
			&statement,
			&status,
			&confidence,
			&reasoningStatus,
			&evidenceJSON,
			&contradictionsJSON,
			&createdAt,
		); err != nil {
			return fmt.Errorf("scan typed Self source: %w", err)
		}
		ref := fmt.Sprintf(
			"self:%s:r%d:%s",
			entryKey,
			revision,
			id,
		)
		if status == selfModelStatusRetracted {
			collector.exclude(
				ref,
				taskContextSourceSelfKind,
				id,
				createdAt,
				TaskContextSourceReasonRetracted,
			)
			continue
		}
		if status != selfModelStatusActive {
			collector.exclude(
				ref,
				taskContextSourceSelfKind,
				id,
				createdAt,
				TaskContextSourceReasonUnsupportedAgent,
			)
			continue
		}
		evidenceClass, supported := taskContextSelfEvidenceClass(
			reasoningStatus,
		)
		if !supported {
			collector.exclude(
				ref,
				taskContextSourceSelfKind,
				id,
				createdAt,
				TaskContextSourceReasonUnsupportedAgent,
			)
			continue
		}
		var evidence, contradictions []SelfModelEvidence
		if json.Unmarshal([]byte(evidenceJSON), &evidence) != nil ||
			json.Unmarshal([]byte(contradictionsJSON), &contradictions) != nil {
			collector.exclude(
				ref,
				taskContextSourceSelfKind,
				id,
				createdAt,
				TaskContextSourceReasonMalformed,
			)
			continue
		}
		hasFutureEvidence := false
		for _, item := range append(evidence, contradictions...) {
			if item.OccurredAt > collector.cutoffAt {
				hasFutureEvidence = true
				break
			}
		}
		if hasFutureEvidence {
			collector.exclude(
				ref,
				taskContextSourceSelfKind,
				id,
				createdAt,
				TaskContextSourceReasonFuture,
			)
			continue
		}
		section := TaskContextSectionSelf
		required := false
		if kind == selfModelKindBoundary {
			section = TaskContextSectionBoundaries
			required = true
		}
		content := strings.TrimSpace(strings.Join([]string{
			"Kind: " + kind,
			"Scope: " + scope,
			"Statement: " + statement,
			fmt.Sprintf("Confidence: %.4f", clampConfidence(confidence)),
			"Reasoning status: " + reasoningStatus,
		}, "\n"))
		relevance := taskContextSourceRelevance(
			collector.query,
			content,
			taskContextSourceDefaultSelfRelevance,
		)
		if required {
			relevance = 1
		}
		collector.add(TaskContextCandidate{
			Ref:             ref,
			Section:         section,
			SourceKind:      taskContextSourceSelfKind,
			SourceID:        id,
			ObservedAt:      createdAt,
			Content:         content,
			EvidenceClass:   evidenceClass,
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance:       relevance,
			Required:        required,
		})
	}
	return rows.Err()
}

func (collector *taskContextSourceCollector) gatherLegacyInitiatives(
	db *sql.DB,
) error {
	exists, err := taskContextTableExists(db, "initiatives")
	if err != nil {
		return fmt.Errorf("inspect legacy initiative sources: %w", err)
	}
	if !exists {
		return nil
	}
	rows, err := db.Query(`
		SELECT
			id,
			kind,
			status,
			autonomy_mode,
			title,
			description,
			goal,
			user_intent,
			created_at,
			updated_at
		FROM initiatives
		WHERE created_at <= ?
			AND updated_at <= ?
			AND status IN ('approved', 'executing')
			AND autonomy_mode != 'shadow'
		ORDER BY id ASC`,
		collector.cutoffAt,
		collector.cutoffAt,
	)
	if err != nil {
		return fmt.Errorf("query legacy initiative sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, kind, status, mode   string
			title, description, goal string
			userIntent               string
			createdAt, updatedAt     int64
		)
		if err := rows.Scan(
			&id,
			&kind,
			&status,
			&mode,
			&title,
			&description,
			&goal,
			&userIntent,
			&createdAt,
			&updatedAt,
		); err != nil {
			return fmt.Errorf("scan legacy initiative source: %w", err)
		}
		content := strings.TrimSpace(strings.Join([]string{
			"Kind: " + kind,
			"Title: " + title,
			"Description: " + description,
			"Goal: " + goal,
			"User intent: " + userIntent,
			"State at task start: " + status,
			"Authority mode: " + mode,
		}, "\n"))
		collector.add(TaskContextCandidate{
			Ref: taskContextSourceRef(
				taskContextSourceLegacyInitiativeKind,
				id,
				updatedAt,
				content,
			),
			Section:         TaskContextSectionInitiatives,
			SourceKind:      taskContextSourceLegacyInitiativeKind,
			SourceID:        id,
			ObservedAt:      updatedAt,
			Content:         content,
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance: taskContextSourceRelevance(
				collector.query,
				content,
				taskContextSourceDefaultInitiativeRelevance,
			),
		})
	}
	return rows.Err()
}

func (collector *taskContextSourceCollector) gatherAuthorityConstraints(
	db *sql.DB,
) error {
	hasGrants, err := taskContextTableExists(db, "authority_grants")
	if err != nil {
		return fmt.Errorf("inspect authority grant sources: %w", err)
	}
	hasEvents, err := taskContextTableExists(db, "authority_grant_events")
	if err != nil {
		return fmt.Errorf("inspect authority event sources: %w", err)
	}
	if !hasGrants || !hasEvents {
		return nil
	}
	rows, err := db.Query(`
		SELECT
			grant_row.id,
			grant_row.initiative_id,
			grant_row.capability,
			grant_row.resource_prefix,
			grant_row.effect,
			grant_row.expires_at,
			grant_row.created_at,
			(
				SELECT MIN(issued.created_at)
				FROM authority_grant_events issued
				WHERE issued.grant_id = grant_row.id
					AND issued.event_type = 'issued'
			),
			(
				SELECT MIN(revoked.created_at)
				FROM authority_grant_events revoked
				WHERE revoked.grant_id = grant_row.id
					AND revoked.event_type = 'revoked'
			),
			(
				SELECT revoked.reason
				FROM authority_grant_events revoked
				WHERE revoked.grant_id = grant_row.id
					AND revoked.event_type = 'revoked'
				ORDER BY revoked.created_at ASC
				LIMIT 1
			)
		FROM authority_grants grant_row
		WHERE grant_row.created_at <= ?
		ORDER BY grant_row.id ASC`,
		collector.cutoffAt,
	)
	if err != nil {
		return fmt.Errorf("query authority constraint sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, initiativeID, capability, resource string
			effect, revocationReason               string
			expiresAt, createdAt                   int64
			issuedAt, revokedAt                    sql.NullInt64
			revocation                             sql.NullString
		)
		if err := rows.Scan(
			&id,
			&initiativeID,
			&capability,
			&resource,
			&effect,
			&expiresAt,
			&createdAt,
			&issuedAt,
			&revokedAt,
			&revocation,
		); err != nil {
			return fmt.Errorf("scan authority constraint source: %w", err)
		}
		if revocation.Valid {
			revocationReason = revocation.String
		}
		ref := "authority-grant:" + id
		if !issuedAt.Valid || issuedAt.Int64 <= 0 ||
			issuedAt.Int64 > collector.cutoffAt {
			reason := TaskContextSourceReasonUnverifiable
			observedAt := createdAt
			if issuedAt.Valid && issuedAt.Int64 > collector.cutoffAt {
				reason = TaskContextSourceReasonFuture
				observedAt = issuedAt.Int64
			}
			collector.exclude(
				ref,
				taskContextSourceAuthorityKind,
				id,
				observedAt,
				reason,
			)
			continue
		}
		status := "active"
		observedAt := issuedAt.Int64
		if revokedAt.Valid && revokedAt.Int64 <= collector.cutoffAt {
			status = "revoked"
			observedAt = revokedAt.Int64
		} else if expiresAt <= collector.cutoffAt {
			status = "expired"
		}
		content := strings.TrimSpace(strings.Join([]string{
			"Authority status at task start: " + status,
			"Initiative: " + initiativeID,
			"Capability: " + capability,
			"Effect: " + effect,
			"Resource scope: " + resource,
			fmt.Sprintf("Expires at: %d", expiresAt),
			"Revocation reason: " + revocationReason,
		}, "\n"))
		collector.add(TaskContextCandidate{
			Ref:             ref + ":" + status,
			Section:         TaskContextSectionAuthority,
			SourceKind:      taskContextSourceAuthorityKind,
			SourceID:        id,
			ObservedAt:      observedAt,
			Content:         content,
			EvidenceClass:   TaskContextEvidenceExplicitUser,
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance:       1,
			Required:        true,
		})
	}
	return rows.Err()
}

func (collector *taskContextSourceCollector) gatherPriorOutcomes(
	db *sql.DB,
) error {
	if err := collector.gatherLegacyInitiativeOutcomes(db); err != nil {
		return err
	}
	return collector.gatherTaskEpisodeOutcomes(db)
}

func (collector *taskContextSourceCollector) gatherLegacyInitiativeOutcomes(
	db *sql.DB,
) error {
	exists, err := taskContextTableExists(db, "initiative_outcomes")
	if err != nil {
		return fmt.Errorf("inspect initiative outcome sources: %w", err)
	}
	if !exists {
		return nil
	}
	rows, err := db.Query(`
		SELECT
			id,
			initiative_id,
			source_kind,
			source_id,
			observed_action,
			observed_result,
			created_at
		FROM initiative_outcomes
		WHERE created_at <= ?
		ORDER BY id ASC`,
		collector.cutoffAt,
	)
	if err != nil {
		return fmt.Errorf("query initiative outcome sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, initiativeID, sourceKind, sourceID string
			action, result                         string
			createdAt                              int64
		)
		if err := rows.Scan(
			&id,
			&initiativeID,
			&sourceKind,
			&sourceID,
			&action,
			&result,
			&createdAt,
		); err != nil {
			return fmt.Errorf("scan initiative outcome source: %w", err)
		}
		if reason := taskContextForbiddenSourceReason(sourceKind); reason != "" {
			collector.exclude(
				"initiative-outcome:"+id,
				taskContextSourceInitiativeOutcomeKind,
				id,
				createdAt,
				reason,
			)
			continue
		}
		content := strings.TrimSpace(strings.Join([]string{
			"Initiative: " + initiativeID,
			"Observed action: " + action,
			"Observed result: " + result,
			"Source: " + sourceKind + ":" + sourceID,
		}, "\n"))
		collector.add(TaskContextCandidate{
			Ref: taskContextSourceRef(
				taskContextSourceInitiativeOutcomeKind,
				id,
				createdAt,
				content,
			),
			Section:         TaskContextSectionOutcomes,
			SourceKind:      taskContextSourceInitiativeOutcomeKind,
			SourceID:        id,
			ObservedAt:      createdAt,
			Content:         content,
			EvidenceClass:   TaskContextEvidenceObservedUser,
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance: taskContextSourceRelevance(
				collector.query,
				content,
				taskContextSourceDefaultOutcomeRelevance,
			),
		})
	}
	return rows.Err()
}

func (collector *taskContextSourceCollector) gatherTaskEpisodeOutcomes(
	db *sql.DB,
) error {
	exists, err := taskContextTableExists(db, "task_episode_evidence")
	if err != nil {
		return fmt.Errorf("inspect task episode outcome sources: %w", err)
	}
	if !exists {
		return nil
	}
	repeatedOnsets := make(map[string]struct{})
	hasEpisodes, err := taskContextTableExists(db, "task_episodes")
	if err != nil {
		return fmt.Errorf("inspect task episode sources: %w", err)
	}
	hasHypotheses, err := taskContextTableExists(
		db,
		"task_episode_hypotheses",
	)
	if err != nil {
		return fmt.Errorf("inspect task episode hypothesis sources: %w", err)
	}
	if hasEpisodes && hasHypotheses {
		repeatedOnsets, err = (&ActivityStore{db: db}).
			taskEpisodeRepeatedOnsetSetAt(collector.cutoffAt)
		if err != nil {
			return fmt.Errorf("inspect repeated task starts: %w", err)
		}
	}
	guidedEpisodes := make(map[string]struct{})
	hasFirstRunExclusions, err := taskContextTableExists(
		db,
		"first_run_exclusions_v1",
	)
	if err != nil {
		return fmt.Errorf("inspect First Run task exclusions: %w", err)
	}
	if hasFirstRunExclusions {
		exclusionRows, err := db.Query(`
			SELECT source_id
			FROM first_run_exclusions_v1
			WHERE source_kind = 'task_episode'
				AND reason = ?`,
			FirstRunExclusionGuided,
		)
		if err != nil {
			return fmt.Errorf("query First Run task exclusions: %w", err)
		}
		for exclusionRows.Next() {
			var episodeID string
			if err := exclusionRows.Scan(&episodeID); err != nil {
				exclusionRows.Close()
				return fmt.Errorf("scan First Run task exclusion: %w", err)
			}
			guidedEpisodes[strings.TrimSpace(episodeID)] = struct{}{}
		}
		if err := exclusionRows.Err(); err != nil {
			exclusionRows.Close()
			return fmt.Errorf("iterate First Run task exclusions: %w", err)
		}
		exclusionRows.Close()
	}
	rows, err := db.Query(`
		SELECT
			id,
			task_episode_id,
			source_kind,
			source_id,
			summary,
			observed_at,
			created_at
		FROM task_episode_evidence
		WHERE role = 'result'
			AND observed_at <= ?
			AND created_at <= ?
		ORDER BY id ASC`,
		collector.cutoffAt,
		collector.cutoffAt,
	)
	if err != nil {
		return fmt.Errorf("query task episode outcome sources: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, episodeID, sourceKind, sourceID string
			summary                             string
			observedAt, createdAt               int64
		)
		if err := rows.Scan(
			&id,
			&episodeID,
			&sourceKind,
			&sourceID,
			&summary,
			&observedAt,
			&createdAt,
		); err != nil {
			return fmt.Errorf("scan task episode outcome source: %w", err)
		}
		if _, guided := guidedEpisodes[episodeID]; guided {
			collector.exclude(
				"task-episode-outcome:"+id,
				taskContextSourceTaskEpisodeOutcomeKind,
				id,
				observedAt,
				TaskContextSourceReasonGuidedFirstRun,
			)
			continue
		}
		if _, repeated := repeatedOnsets[episodeID]; repeated {
			collector.exclude(
				"task-episode-outcome:"+id,
				taskContextSourceTaskEpisodeOutcomeKind,
				id,
				observedAt,
				TaskContextSourceReasonRepeatedTaskStart,
			)
			continue
		}
		if reason := taskContextForbiddenSourceReason(sourceKind); reason != "" {
			collector.exclude(
				"task-episode-outcome:"+id,
				taskContextSourceTaskEpisodeOutcomeKind,
				id,
				observedAt,
				reason,
			)
			continue
		}
		content := strings.TrimSpace(strings.Join([]string{
			"Task episode: " + episodeID,
			"Observed result: " + summary,
			"Source: " + sourceKind + ":" + sourceID,
		}, "\n"))
		collector.add(TaskContextCandidate{
			Ref: taskContextSourceRef(
				taskContextSourceTaskEpisodeOutcomeKind,
				id,
				observedAt,
				content,
			),
			Section:         TaskContextSectionOutcomes,
			SourceKind:      taskContextSourceTaskEpisodeOutcomeKind,
			SourceID:        id,
			ObservedAt:      observedAt,
			Content:         content,
			EvidenceClass:   TaskContextEvidenceEvaluatedProjection,
			PrivacyDecision: TaskContextPrivacyRedact,
			Relevance: taskContextSourceRelevance(
				collector.query,
				content,
				taskContextSourceDefaultOutcomeRelevance,
			),
		})
	}
	return rows.Err()
}

func (collector *taskContextSourceCollector) gatherRecentActivity(
	recent []TaskContextCandidate,
) {
	for _, candidate := range recent {
		if strings.TrimSpace(candidate.Section) == "" {
			candidate.Section = TaskContextSectionActivity
		}
		if strings.TrimSpace(candidate.EvidenceClass) == "" {
			candidate.EvidenceClass = TaskContextEvidenceObservedUser
		}
		if strings.TrimSpace(candidate.PrivacyDecision) == "" {
			candidate.PrivacyDecision = TaskContextPrivacyRedact
		}
		if candidate.Relevance <= 0 {
			candidate.Relevance = taskContextSourceRelevance(
				collector.query,
				candidate.Content,
				taskContextSourceDefaultRecentActivityRelevance,
			)
		}
		collector.add(candidate)
	}
}
