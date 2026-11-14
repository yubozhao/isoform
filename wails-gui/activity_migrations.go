package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// ──────────────────────────────────────────────
// Schema inspection helpers
// ──────────────────────────────────────────────

// hasTable reports whether a table exists in the database.
func (s *ActivityStore) hasTable(name string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name,
	).Scan(&count)
	return count > 0, err
}

// hasColumn reports whether a table contains the given column.
// Pattern from storage/storage.go:23-52.
func (s *ActivityStore) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			pk           int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// hasIndex reports whether an index exists in the database.
func (s *ActivityStore) hasIndex(name string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?", name,
	).Scan(&count)
	return count > 0, err
}

// hasTrigger reports whether a trigger exists in the database.
func (s *ActivityStore) hasTrigger(name string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?", name,
	).Scan(&count)
	return count > 0, err
}

// tableSchemaContains reports whether the CREATE TABLE SQL for the given table
// contains the specified substring.
func (s *ActivityStore) tableSchemaContains(table, substr string) (bool, error) {
	var sqlText sql.NullString
	err := s.db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table,
	).Scan(&sqlText)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if !sqlText.Valid {
		return false, nil
	}
	return strings.Contains(sqlText.String, substr), nil
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// ──────────────────────────────────────────────
// Migration registry
// ──────────────────────────────────────────────

// activityMigration represents a named, idempotent schema migration.
type activityMigration struct {
	Name string
	Fn   func(tx *sql.Tx) error
}

// allActivityMigrations is the ordered list of all migrations for the activity
// database. Names are stable and must never be renamed once merged to main.
//
// Fresh DB note: ocr_frames_fts already creates FTS with trigram, and
// fts_trigram drops/recreates it. This is intentional historical replay —
// merged migrations cannot be removed or folded.
var allActivityMigrations = []activityMigration{
	{Name: "core_tables", Fn: migrateCoreTables},
	{Name: "start_time_index", Fn: migrateStartTimeIndex},
	{Name: "ocr_frames_fts", Fn: migrateOCRFramesFTS},
	{Name: "snapshots", Fn: migrateSnapshots},
	{Name: "ocr_constraint_fix", Fn: migrateOCRConstraintFix},
	{Name: "automation_status", Fn: migrateAutomationStatus},
	{Name: "activity_summaries", Fn: migrateActivitySummariesV7},
	{Name: "fts_trigram", Fn: migrateFTSTrigram},
	{Name: "prediction_tables", Fn: migratePredictionTables},
	{Name: "prediction_cron_columns", Fn: migratePredictionCronColumns},
	{Name: "embedding_columns", Fn: migrateEmbeddingColumns},
	{Name: "meeting_columns", Fn: migrateMeetingColumns},
	{Name: "activity_pipeline_runs", Fn: migrateActivityPipelineRuns},
	{Name: "audio_columns", Fn: migrateAudioColumns},
	{Name: "ax_text_column", Fn: migrateAXTextColumn},
	{Name: "browser_url_column", Fn: migrateBrowserURLColumn},
	{Name: "activity_summaries_moments", Fn: migrateActivitySummariesMoments},
	{Name: "snapshot_parsed_columns", Fn: migrateSnapshotParsedColumns},
	{Name: "activity_summaries_moments_partial", Fn: migrateActivitySummariesMomentsPartial},
	{Name: "activity_summaries_moments_partial_reset", Fn: migrateActivitySummariesMomentsPartialReset},
	{Name: "activity_summaries_moments_generation", Fn: migrateActivitySummariesMomentsGeneration},
	{Name: "activity_summaries_language", Fn: migrateActivitySummariesLanguage},
	{Name: "activity_summaries_language_key", Fn: migrateActivitySummariesLanguageKey},
	{Name: "handoff_status", Fn: migrateHandoffStatus},
	{Name: "handoff_prompt_columns", Fn: migrateHandoffPromptColumns},
	{Name: "handoff_pipeline_columns", Fn: migrateHandoffPipelineColumns},
	{Name: "pipeline_selected_frames", Fn: migratePipelineSelectedFrames},
	{Name: "focus_rect_columns", Fn: migrateFocusRectColumns},
	{Name: "ocr_used_focus_rect_column", Fn: migrateOCRUsedFocusRectColumn},
	{Name: "triage_prompt_columns", Fn: migrateTriagePromptColumns},
	{Name: "rename_crystal_to_triage", Fn: migrateRenameCrystalToTriage},
	{Name: "meeting_group_id_column", Fn: migrateMeetingGroupIDColumn},
	{Name: "semantic_events", Fn: migrateSemanticEventsTable},
	{Name: "semantic_status_column", Fn: migrateSemanticStatusColumn},
	{Name: "semantic_display_keywords_column", Fn: migrateSemanticDisplayKeywordsColumn},
	{Name: "activity_timeline_group_summaries", Fn: migrateActivityTimelineGroupSummaries},
	{Name: "activity_timeline_groups", Fn: migrateActivityTimelineGroups},
	{Name: "activities_tables", Fn: migrateActivitiesTables},
	{Name: "ocr_boxes_json_column", Fn: migrateOCRBoxesJSONColumn},
	{Name: "snapshot_file_path_index", Fn: migrateSnapshotFilePathIndex},
	{Name: "pipeline_run_source", Fn: migratePipelineRunSource},
	{Name: "timeline_summary_image_failure_requeue", Fn: migrateTimelineSummaryImageFailureRequeue},
	{Name: "sop_status", Fn: migrateSOPStatus},
	{Name: "initiatives", Fn: migrateInitiatives},
	{Name: "initiative_observations", Fn: migrateInitiativeObservations},
	{Name: "initiative_learning", Fn: migrateInitiativeLearning},
	{Name: "initiative_detail_history", Fn: migrateInitiativeDetailHistory},
	{Name: "initiative_calibration", Fn: migrateInitiativeCalibration},
	{Name: "initiative_confidence_adjustment", Fn: migrateInitiativeConfidenceAdjustment},
	{Name: "authority_decisions", Fn: migrateAuthorityDecisions},
	{Name: "shadow_runs", Fn: migrateShadowRuns},
	{Name: "pipeline_shadow_run_id", Fn: migratePipelineShadowRunID},
	{Name: "shadow_correlation_attempts", Fn: migrateShadowCorrelationAttempts},
	{Name: "shadow_capability_profiles", Fn: migrateShadowCapabilityProfiles},
	{Name: "shadow_execution_runtime", Fn: migrateShadowExecutionRuntime},
	{Name: "shadow_capability_qualification", Fn: migrateShadowCapabilityQualification},
	{Name: "capability_authority", Fn: migrateCapabilityAuthority},
	{Name: "typed_self_model", Fn: migrateTypedSelfModel},
	{Name: "task_episodes", Fn: migrateTaskEpisodes},
	{Name: "task_episode_boundary_confidence", Fn: migrateTaskEpisodeBoundaryConfidence},
	{Name: "task_episode_profile_influence", Fn: migrateTaskEpisodeProfileInfluence},
	{Name: "task_episode_reasoning_hypotheses", Fn: migrateTaskEpisodeReasoningHypotheses},
	{Name: "task_context_manifest", Fn: migrateTaskContextManifest},
	{Name: "shadow_task_linkage", Fn: migrateShadowTaskLinkage},
	{Name: "shadow_task_learning", Fn: migrateShadowTaskLearning},
	{
		Name: "task_shadow_capability_release",
		Fn:   migrateTaskShadowCapabilityRelease,
	},
	{Name: "agency_initiatives_v2", Fn: migrateAgencyInitiativesV2},
	{
		Name: "agency_initiative_outcome_evaluations",
		Fn:   migrateAgencyInitiativeOutcomeEvaluations,
	},
	{
		Name: "agency_initiative_feedback_applications",
		Fn:   migrateAgencyInitiativeFeedbackApplications,
	},
	{
		Name: "agency_initiative_feedback_adherence",
		Fn:   migrateAgencyInitiativeFeedbackAdherence,
	},
	{
		Name: "agency_initiative_feedback_calibration",
		Fn:   migrateAgencyInitiativeFeedbackCalibration,
	},
	{
		Name: "agency_initiative_planner_canary",
		Fn:   migrateAgencyInitiativePlannerCanary,
	},
	{
		Name: "agency_initiative_planner_release_decisions",
		Fn:   migrateAgencyInitiativePlannerReleaseDecisions,
	},
	{
		Name: "agency_initiative_planner_rollout",
		Fn:   migrateAgencyInitiativePlannerRollout,
	},
	{
		Name: "agency_initiative_planner_run_provenance_columns",
		Fn:   migrateAgencyInitiativePlannerRunProvenanceColumns,
	},
	{
		Name: "agency_initiative_planner_rollout_monitoring",
		Fn:   migrateAgencyInitiativePlannerRolloutMonitoring,
	},
	{
		Name: "agency_initiative_planner_monitoring_reviews",
		Fn:   migrateAgencyInitiativePlannerMonitoringReviews,
	},
	{
		Name: "agency_initiative_planner_rollback_review_provenance",
		Fn:   migrateAgencyInitiativePlannerRollbackReviewProvenance,
	},
	{
		Name: "agency_initiative_continuations",
		Fn:   migrateAgencyInitiativeContinuations,
	},
	{Name: "agency_origination_runtime", Fn: migrateAgencyOriginationRuntime},
	{Name: "agency_operating_envelopes", Fn: migrateAgencyOperatingEnvelopes},
	{Name: "agency_activation_gate", Fn: migrateAgencyActivationGate},
	{
		Name: "agency_operating_envelope_policy_guard",
		Fn:   migrateAgencyOperatingEnvelopePolicyGuard,
	},
	{
		Name: "agency_operating_envelope_gmail_draft_boundary",
		Fn:   migrateAgencyOperatingEnvelopeGmailDraftBoundary,
	},
	{
		Name: "agency_operating_envelope_purpose_scope",
		Fn:   migrateAgencyOperatingEnvelopePurposeScope,
	},
	{
		Name: "agency_operating_envelope_parallel_initiatives",
		Fn:   migrateAgencyOperatingEnvelopeParallelInitiatives,
	},
	{
		Name: "agency_activation_gate_policy_v2",
		Fn:   migrateAgencyActivationGatePolicyV2,
	},
	{
		Name: "agency_activation_gate_policy_v3",
		Fn:   migrateAgencyActivationGatePolicyV3,
	},
	{
		Name: "agency_activation_gate_policy_v4",
		Fn:   migrateAgencyActivationGatePolicyV4,
	},
	{
		Name: "agency_initiative_re_admissions",
		Fn:   migrateAgencyInitiativeReAdmissions,
	},
	{Name: "agency_permission_decisions", Fn: migrateAgencyPermissionDecisions},
	{Name: "agency_schedule_commands", Fn: migrateAgencyScheduleCommands},
	{
		Name: "agency_permission_overlap_resolutions",
		Fn:   migrateAgencyPermissionOverlapResolutions,
	},
	{
		Name: "agency_permission_completed_by_user",
		Fn:   migrateAgencyOriginationPermissionCompletedByUser,
	},
	{
		Name: "agency_handoff_user_completions",
		Fn:   migrateAgencyHandoffUserCompletions,
	},
	{
		Name: "agency_permission_source_task_ended",
		Fn:   migrateAgencyOriginationPermissionSourceTaskEnded,
	},
	{
		Name: "agency_handoff_source_end_retirements",
		Fn:   migrateAgencyHandoffSourceEndRetirements,
	},
	{
		Name: "agency_handoff_evidence_invalidations",
		Fn:   migrateAgencyHandoffEvidenceInvalidations,
	},
	{
		Name: "agency_schedule_evidence_invalidations",
		Fn:   migrateAgencyScheduleEvidenceInvalidations,
	},
	{Name: "agency_chat", Fn: migrateAgencyChat},
	{Name: "task_agent_attempts", Fn: migrateTaskAgentAttempts},
	{Name: "first_run", Fn: migrateFirstRun},
	{Name: "first_outcome_debug_replays", Fn: migrateFirstOutcomeDebugReplays},
	{Name: "work_pattern_profiles", Fn: migrateWorkPatternProfiles},
	{
		Name: "work_pattern_profile_correction_revisions",
		Fn:   migrateWorkPatternProfileCorrectionRevisions,
	},
	{
		Name: "work_pattern_profile_session_cache",
		Fn:   migrateWorkPatternProfileSessionCache,
	},
	{Name: "self_dream", Fn: migrateSelfDream},
	{Name: "background_understanding", Fn: migrateBackgroundUnderstanding},
	{Name: "self_user_directives", Fn: migrateSelfUserDirectives},
	{Name: "purpose_map", Fn: migratePurposeMap},
	{Name: "purpose_extraction", Fn: migratePurposeExtraction},
	{
		Name: "purpose_agency_outcome_receipts",
		Fn:   migratePurposeAgencyOutcomeReceipts,
	},
	{
		Name: "purpose_agency_outcome_receipt_provenance_status",
		Fn:   migratePurposeAgencyOutcomeReceiptProvenanceStatus,
	},
	{
		Name: "task_shadow_observer_traces",
		Fn:   migrateTaskShadowObserverTraces,
	},
	{
		Name: "task_shadow_observer_coverage",
		Fn:   migrateTaskShadowObserverCoverage,
	},
	{
		Name: "task_shadow_observer_exact_input",
		Fn:   migrateTaskShadowObserverExactInput,
	},
	{
		Name: "task_context_manifest_stats",
		Fn:   migrateTaskContextManifestStats,
	},
	{
		Name: "drop_removed_agency_tables",
		Fn:   migrateDropRemovedAgencyTables,
	},
	{
		Name: "activity_events_kind_time_index",
		Fn:   migrateActivityEventsKindTimeIndex,
	},
	{Name: "browser_visits", Fn: migrateBrowserVisits},
	{Name: "file_activity", Fn: migrateFileActivity},
	{Name: "browser_site_days", Fn: migrateBrowserSiteDays},
	{Name: "activity_retention_sources", Fn: migrateActivityRetentionSources},
	{Name: "activity_run_evidence_tokens", Fn: migrateActivityRunEvidenceTokens},
	{Name: "activity_quiet_settling", Fn: migrateActivityQuietSettling},
	{Name: "activity_outcomes_and_corrections", Fn: migrateActivityOutcomesAndCorrections},
	{Name: "activity_evidence_identifier_policy", Fn: migrateActivityEvidenceIdentifierPolicy},
	{Name: "activity_evidence_source_revisions", Fn: migrateActivityEvidenceSourceRevisions},
	{Name: "activity_evidence_receipt_policy", Fn: migrateActivityEvidenceReceiptPolicy},
	{Name: "activity_outcome_measurement_cache", Fn: migrateActivityOutcomeMeasurementCache},
	{Name: "activity_event_context_index", Fn: migrateActivityEventContextIndex},
	{Name: "activity_retention_sources_minimal", Fn: migrateMinimalActivityRetentionSources},
	{Name: "activity_retention_unavailable", Fn: migrateActivityRetentionUnavailable},
	{Name: "activity_summary_source_provenance", Fn: migrateActivitySummarySourceProvenance},
	{Name: "activity_retained_source_privacy", Fn: migrateActivityRetainedSourcePrivacy},
	{Name: "purpose_mail_title_repairs", Fn: migratePurposeMailTitleRepairs},
	{Name: "activity_thread_outcome", Fn: migrateActivityThreadOutcome},
	{Name: "assistant_text_edits", Fn: migrateAssistantTextEdits},
	{Name: "meeting_notes", Fn: migrateMeetingNotes},
	{Name: "meeting_notes_learned_kinds", Fn: migrateMeetingNotesLearnedKinds},
	{Name: "meeting_call_turns", Fn: migrateMeetingTurns},
	{Name: "meeting_call_turn_provenance", Fn: migrateMeetingTurnProvenance},
	{Name: "activity_user_corrections", Fn: migrateActivityUserCorrections},
	{Name: "activity_threads", Fn: migrateActivityThreads},
	{Name: "activity_noticed", Fn: migrateActivityNoticed},
	{Name: "activity_thread_link_state", Fn: migrateActivityThreadLinkState},
	{Name: "activity_end_id_index", Fn: migrateActivityEndIDIndex},
	{Name: "calendar_holds", Fn: migrateCalendarHolds},
	{Name: "calendar_hold_dismissals", Fn: migrateCalendarHoldDismissals},
	{Name: "calendar_hold_active_source", Fn: migrateCalendarHoldActiveSource},
	{Name: "calendar_hold_recovery", Fn: migrateCalendarHoldRecovery},
	{Name: "task_episode_read_revision", Fn: migrateTaskEpisodeReadRevision},
	{Name: "noticed_plain_invoice_followup", Fn: migrateNoticedPlainInvoiceFollowup},
	{Name: "activity_agent_runs", Fn: migrateActivityAgentRuns},
	{Name: "activity_agent_runs_follow_runs", Fn: migrateActivityAgentRunsFollowRuns},
}

// ──────────────────────────────────────────────
// Migration infrastructure
// ──────────────────────────────────────────────

// ensureMigrationLog creates the migration_log table if it doesn't exist.
func (s *ActivityStore) ensureMigrationLog() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS migration_log (
		name       TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create migration_log: %w", err)
	}
	return nil
}

// loadAppliedMigrations reads all names from migration_log into a set.
func (s *ActivityStore) loadAppliedMigrations() map[string]bool {
	result := make(map[string]bool)
	rows, err := s.db.Query("SELECT name FROM migration_log")
	if err != nil {
		log.Printf("[activity] load applied migrations: %v", err)
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			log.Printf("[activity] scan applied migration name: %v", err)
			continue
		}
		result[name] = true
	}
	if err := rows.Err(); err != nil {
		log.Printf("[activity] load applied migrations iteration error: %v", err)
	}
	return result
}

// bootstrapMigrationLog back-fills migration_log for databases upgrading from
// PRAGMA user_version tracking. Probes actual schema to determine which
// migrations have already been applied.
//
// Two-step process to avoid SQLite single-connection self-deadlock:
//
//	Step 1: schema checks outside transaction (using s.db directly)
//	Step 2: batch INSERT inside transaction (atomic)
func (s *ActivityStore) bootstrapMigrationLog() error {
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM migration_log").Scan(&count); err != nil {
		return fmt.Errorf("check migration_log count: %w", err)
	}
	if count > 0 {
		return nil
	}

	type reconcileRule struct {
		Name  string
		Check func() (bool, error)
	}
	rules := []reconcileRule{
		{"core_tables", func() (bool, error) {
			a, e1 := s.hasTable("activity_sessions")
			b, e2 := s.hasTable("activity_events")
			c, e3 := s.hasTable("activity_segments")
			d, e4 := s.hasIndex("idx_activity_events_session")
			e, e5 := s.hasIndex("idx_activity_segments_session")
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"start_time_index", func() (bool, error) {
			return s.hasIndex("idx_activity_sessions_start_time")
		}},
		{"ocr_frames_fts", func() (bool, error) {
			a, e1 := s.hasTable("activity_ocr_frames")
			b, e2 := s.hasTable("activity_ocr_fts")
			c, e3 := s.hasColumn("activity_sessions", "ocr_status")
			d, e4 := s.hasIndex("idx_ocr_frames_session")
			e, e5 := s.hasTrigger("ocr_fts_insert")
			f, e6 := s.hasTrigger("ocr_fts_delete")
			g, e7 := s.hasTrigger("ocr_fts_update")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g, nil
		}},
		{"snapshots", func() (bool, error) {
			a, e1 := s.hasTable("activity_snapshots")
			b, e2 := s.hasColumn("activity_sessions", "snapshot_count")
			c, e3 := s.hasIndex("idx_activity_snapshots_session")
			d, e4 := s.hasIndex("idx_activity_snapshots_timestamp")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"ocr_constraint_fix", func() (bool, error) {
			exists, err := s.hasTable("activity_ocr_frames")
			if err != nil {
				return false, err
			}
			if !exists {
				return false, nil
			}
			hasFK, err := s.tableSchemaContains("activity_ocr_frames", "REFERENCES activity_segments")
			if err != nil {
				return false, err
			}
			return !hasFK, nil
		}},
		{"automation_status", func() (bool, error) {
			return s.hasColumn("activity_sessions", "automation_status")
		}},
		{"activity_summaries", func() (bool, error) {
			return s.hasTable("activity_summaries")
		}},
		{"fts_trigram", func() (bool, error) {
			return s.tableSchemaContains("activity_ocr_fts", "trigram")
		}},
		{"prediction_tables", func() (bool, error) {
			a, e1 := s.hasTable("prediction_proposals")
			b, e2 := s.hasTable("prediction_metrics")
			c, e3 := s.hasIndex("idx_prediction_proposals_status")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"prediction_cron_columns", func() (bool, error) {
			a, e1 := s.hasColumn("prediction_proposals", "kind")
			b, e2 := s.hasColumn("prediction_proposals", "cron_job_id")
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"embedding_columns", func() (bool, error) {
			a, e1 := s.hasColumn("activity_ocr_frames", "embedding")
			b, e2 := s.hasColumn("activity_ocr_frames", "embed_hash")
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"meeting_columns", func() (bool, error) {
			a, e1 := s.hasColumn("activity_sessions", "is_meeting")
			b, e2 := s.hasColumn("activity_sessions", "meeting_app")
			c, e3 := s.hasColumn("activity_sessions", "transcript_path")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"activity_pipeline_runs", func() (bool, error) {
			a, e1 := s.hasTable("activity_pipeline_runs")
			b, e2 := s.hasIndex("idx_activity_pipeline_runs_started")
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"audio_columns", func() (bool, error) {
			a, e1 := s.hasColumn("activity_sessions", "audio_path")
			b, e2 := s.hasColumn("activity_sessions", "audio_size_bytes")
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"ax_text_column", func() (bool, error) {
			return s.hasColumn("activity_snapshots", "ax_text")
		}},
		{"browser_url_column", func() (bool, error) {
			return s.hasColumn("activity_snapshots", "browser_url")
		}},
		{"activity_summaries_moments", func() (bool, error) {
			return s.hasColumn("activity_summaries", "moments_json")
		}},
		{"snapshot_parsed_columns", func() (bool, error) {
			a, e1 := s.hasColumn("activity_snapshots", "content_type")
			b, e2 := s.hasColumn("activity_snapshots", "app")
			c, e3 := s.hasColumn("activity_snapshots", "parsed_json")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"activity_summaries_moments_partial", func() (bool, error) {
			return s.hasColumn("activity_summaries", "moments_partial")
		}},
		{"activity_summaries_moments_partial_reset", func() (bool, error) {
			// Pure data backfill — schema state can't tell us whether the
			// reset already ran. Always return false so runPendingMigrations
			// gets a chance to apply it; the migration body itself is a
			// safe idempotent UPDATE.
			return false, nil
		}},
		{"activity_summaries_moments_generation", func() (bool, error) {
			return s.hasColumn("activity_summaries", "moments_generation")
		}},
		{"activity_summaries_language", func() (bool, error) {
			return s.hasColumn("activity_summaries", "language")
		}},
		{"activity_summaries_language_key", func() (bool, error) {
			return s.tableSchemaContains("activity_summaries", "PRIMARY KEY (date, type, language)")
		}},
		{"handoff_status", func() (bool, error) {
			return s.hasColumn("activity_sessions", "handoff_status")
		}},
		{"handoff_prompt_columns", func() (bool, error) {
			a, e1 := s.hasColumn("prediction_proposals", "handoff_prompt")
			b, e2 := s.hasColumn("prediction_proposals", "handoff_proposal_id")
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"handoff_pipeline_columns", func() (bool, error) {
			a, e1 := s.hasColumn("activity_pipeline_runs", "worth_handoff")
			b, e2 := s.hasColumn("activity_pipeline_runs", "handoff_status")
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"pipeline_selected_frames", func() (bool, error) {
			return s.hasColumn("activity_pipeline_runs", "selected_frames")
		}},
		{"focus_rect_columns", func() (bool, error) {
			a, e1 := s.hasColumn("activity_snapshots", "focus_rect_x")
			b, e2 := s.hasColumn("activity_snapshots", "focus_rect_y")
			c, e3 := s.hasColumn("activity_snapshots", "focus_rect_w")
			d, e4 := s.hasColumn("activity_snapshots", "focus_rect_h")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"ocr_used_focus_rect_column", func() (bool, error) {
			return s.hasColumn("activity_snapshots", "ocr_used_focus_rect")
		}},
		{"triage_prompt_columns", func() (bool, error) {
			a, e1 := s.hasColumn("activity_pipeline_runs", "triage_flags")
			b, e2 := s.hasColumn("activity_pipeline_runs", "triage_prompt_bytes")
			c, e3 := s.hasColumn("activity_pipeline_runs", "triage_prompt")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"rename_crystal_to_triage", func() (bool, error) {
			// Considered applied only once both tables expose the renamed
			// triage_status column AND the dead crystal_session_id column is
			// gone from activity_sessions. A legacy DB (crystal_status present)
			// fails this and re-runs the idempotent migration.
			a, e1 := s.hasColumn("activity_sessions", "triage_status")
			b, e2 := s.hasColumn("activity_pipeline_runs", "triage_status")
			c, e3 := s.hasColumn("activity_sessions", "crystal_session_id")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && !c, nil
		}},
		{"meeting_group_id_column", func() (bool, error) {
			return s.hasColumn("activity_sessions", "meeting_group_id")
		}},
		{"semantic_events", func() (bool, error) {
			return s.hasTable("activity_semantic_events")
		}},
		{"semantic_status_column", func() (bool, error) {
			return s.hasColumn("activity_sessions", "semantic_status")
		}},
		{"semantic_display_keywords_column", func() (bool, error) {
			return s.hasColumn("activity_semantic_events", "display_keywords")
		}},
		{"activity_timeline_group_summaries", func() (bool, error) {
			return s.hasTable("activity_timeline_group_summaries")
		}},
		{"activity_timeline_groups", func() (bool, error) {
			return s.hasTable("activity_timeline_groups")
		}},
		{"activities_tables", func() (bool, error) {
			return s.hasTable("activities")
		}},
		{"ocr_boxes_json_column", func() (bool, error) {
			return s.hasColumn("activity_ocr_frames", "boxes_json")
		}},
		{"snapshot_file_path_index", func() (bool, error) {
			return s.hasIndex("idx_activity_snapshots_file_path")
		}},
		{"pipeline_run_source", func() (bool, error) {
			return s.hasColumn("activity_pipeline_runs", "source")
		}},
		{"sop_status", func() (bool, error) {
			return s.hasColumn("activity_sessions", "sop_status")
		}},
		{"initiatives", func() (bool, error) {
			a, e1 := s.hasTable("initiatives")
			b, e2 := s.hasTable("initiative_feedback")
			c, e3 := s.hasTable("initiative_artifact_links")
			d, e4 := s.hasIndex("idx_initiatives_status")
			e, e5 := s.hasIndex("idx_initiatives_source")
			f, e6 := s.hasColumn("activity_pipeline_runs", "initiative_id")
			if err := firstErr(e1, e2, e3, e4, e5, e6); err != nil {
				return false, err
			}
			return a && b && c && d && e && f, nil
		}},
		{"initiative_observations", func() (bool, error) {
			a, e1 := s.hasTable("initiative_observations")
			b, e2 := s.hasIndex("idx_initiative_observations_source")
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"initiative_learning", func() (bool, error) {
			a, e1 := s.hasTable("initiative_outcomes")
			b, e2 := s.hasTable("initiative_action_receipts")
			c, e3 := s.hasIndex("idx_initiative_outcomes_initiative")
			d, e4 := s.hasIndex("idx_initiative_receipts_initiative")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"initiative_detail_history", func() (bool, error) {
			a, e1 := s.hasColumn("initiative_observations", "predicted_action")
			b, e2 := s.hasColumn("initiative_observations", "rationale")
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"initiative_calibration", func() (bool, error) {
			return s.hasColumn("initiative_outcomes", "prediction_confidence")
		}},
		{"initiative_confidence_adjustment", func() (bool, error) {
			a, e1 := s.hasColumn("initiatives", "raw_model_confidence")
			b, e2 := s.hasColumn("initiatives", "adjusted_confidence")
			c, e3 := s.hasColumn("initiatives", "confidence_adjustment_json")
			d, e4 := s.hasColumn("initiative_observations", "raw_model_confidence")
			e, e5 := s.hasColumn("initiative_observations", "adjusted_confidence")
			f, e6 := s.hasColumn("initiative_observations", "confidence_adjustment_json")
			if err := firstErr(e1, e2, e3, e4, e5, e6); err != nil {
				return false, err
			}
			return a && b && c && d && e && f, nil
		}},
		{"authority_decisions", func() (bool, error) {
			a, e1 := s.hasTable("authority_decisions")
			b, e2 := s.hasIndex("idx_authority_decisions_run")
			c, e3 := s.hasIndex("idx_authority_decisions_initiative")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"shadow_runs", func() (bool, error) {
			a, e1 := s.hasTable("shadow_runs")
			b, e2 := s.hasTable("shadow_run_evidence")
			c, e3 := s.hasTable("shadow_run_observations")
			d, e4 := s.hasTable("shadow_run_evaluations")
			e, e5 := s.hasIndex("idx_shadow_runs_pending")
			f, e6 := s.hasIndex("idx_shadow_observations_run")
			g, e7 := s.hasIndex("idx_shadow_evaluations_run")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g, nil
		}},
		{"pipeline_shadow_run_id", func() (bool, error) {
			return s.hasColumn("activity_pipeline_runs", "shadow_run_id")
		}},
		{"shadow_correlation_attempts", func() (bool, error) {
			a, e1 := s.hasTable("shadow_correlation_attempts")
			b, e2 := s.hasIndex("idx_shadow_correlation_attempts_created")
			c, e3 := s.hasIndex("idx_shadow_correlation_attempts_source")
			d, e4 := s.hasIndex("idx_shadow_correlation_attempts_status")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"shadow_capability_profiles", func() (bool, error) {
			a, e1 := s.hasTable("shadow_capability_profile_snapshots")
			b, e2 := s.hasIndex("idx_shadow_capability_profiles_latest")
			c, e3 := s.hasIndex("idx_shadow_capability_profiles_ready")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"shadow_execution_runtime", func() (bool, error) {
			a, e1 := s.hasTable("shadow_execution_attempts")
			b, e2 := s.hasIndex("idx_shadow_execution_attempts_run")
			c, e3 := s.hasIndex("idx_shadow_execution_attempts_status")
			d, e4 := s.hasColumn("authority_decisions", "shadow_run_id")
			e, e5 := s.hasColumn("authority_decisions", "execution_mode")
			f, e6 := s.hasIndex("idx_authority_decisions_shadow_run")
			if err := firstErr(e1, e2, e3, e4, e5, e6); err != nil {
				return false, err
			}
			return a && b && c && d && e && f, nil
		}},
		{"shadow_capability_qualification", func() (bool, error) {
			a, e1 := s.hasTable("shadow_capability_qualifications")
			b, e2 := s.hasIndex("idx_shadow_qualifications_capability")
			c, e3 := s.hasIndex("idx_shadow_qualifications_status")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"capability_authority", func() (bool, error) {
			a, e1 := s.hasTable("capability_manifests")
			b, e2 := s.hasTable("authority_grants")
			c, e3 := s.hasTable("authority_grant_events")
			d, e4 := s.hasIndex("idx_capability_manifests_initiative")
			e, e5 := s.hasIndex("idx_authority_grants_initiative")
			f, e6 := s.hasIndex("idx_authority_grant_events_grant")
			if err := firstErr(e1, e2, e3, e4, e5, e6); err != nil {
				return false, err
			}
			return a && b && c && d && e && f, nil
		}},
		{"typed_self_model", func() (bool, error) {
			a, e1 := s.hasTable("self_model_entries")
			b, e2 := s.hasIndex("idx_self_model_entries_latest")
			c, e3 := s.hasIndex("idx_self_model_entries_kind")
			d, e4 := s.hasTrigger("self_model_entries_no_update")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"task_episodes", func() (bool, error) {
			a, e1 := s.hasTable("task_episodes")
			b, e2 := s.hasTable("task_episode_context_snapshots")
			c, e3 := s.hasTable("task_episode_hypotheses")
			d, e4 := s.hasTable("task_episode_evidence")
			e, e5 := s.hasTable("task_episode_transitions")
			f, e6 := s.hasTable("task_episode_links")
			g, e7 := s.hasIndex("idx_task_episodes_timeline")
			h, e8 := s.hasIndex("idx_task_episode_transitions_episode")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"task_episode_boundary_confidence", func() (bool, error) {
			return s.hasColumn("task_episode_hypotheses", "boundary_confidence")
		}},
		{"task_episode_profile_influence", func() (bool, error) {
			return s.hasColumn("task_episode_hypotheses", "profile_influence_json")
		}},
		{"task_episode_reasoning_hypotheses", func() (bool, error) {
			return s.hasColumn("task_episode_hypotheses", "reasoning_hypotheses_json")
		}},
		{"task_context_manifest", func() (bool, error) {
			return s.hasColumn("task_episode_context_snapshots", "manifest_json")
		}},
		{"shadow_task_linkage", func() (bool, error) {
			a, e1 := s.hasColumn("shadow_runs", "task_episode_id")
			b, e2 := s.hasColumn("shadow_runs", "mode")
			c, e3 := s.hasColumn("shadow_runs", "task_context_hash")
			d, e4 := s.hasIndex("idx_shadow_runs_task_episode")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"shadow_task_learning", func() (bool, error) {
			a, e1 := s.hasTable("shadow_task_learning_runs")
			b, e2 := s.hasTable("shadow_task_learning_revisions")
			c, e3 := s.hasTable("shadow_task_learning_materializations")
			d, e4 := s.hasIndex("idx_shadow_task_learning_target")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"task_shadow_capability_release", func() (bool, error) {
			a, e1 := s.hasTable(
				"task_shadow_capability_release_events_v1",
			)
			b, e2 := s.hasIndex("idx_task_shadow_release_capability_v1")
			c, e3 := s.hasTrigger(
				"task_shadow_release_events_v1_no_update",
			)
			d, e4 := s.hasTrigger(
				"task_shadow_release_events_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"work_pattern_profiles", func() (bool, error) {
			a, e1 := s.hasTable("work_pattern_profile_snapshots_v1")
			b, e2 := s.hasTable("work_pattern_profile_evidence_v1")
			c, e3 := s.hasTable("work_pattern_profile_corrections_v1")
			d, e4 := s.hasTable("work_pattern_profile_ablation_runs_v1")
			e, e5 := s.hasTable("work_pattern_profile_ablation_evaluations_v1")
			f, e6 := s.hasColumn(
				"task_episode_context_snapshots",
				"work_profile_snapshot_id",
			)
			g, e7 := s.hasColumn("shadow_runs", "work_profile_snapshot_id")
			h, e8 := s.hasIndex(
				"idx_work_pattern_profile_snapshots_cutoff_v1",
			)
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"work_pattern_profile_correction_revisions", func() (bool, error) {
			a, e1 := s.hasColumn(
				"work_pattern_profile_corrections_v1",
				"revision",
			)
			b, e2 := s.hasIndex(
				"idx_work_pattern_profile_corrections_revision_v1",
			)
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"work_pattern_profile_session_cache", func() (bool, error) {
			a, e1 := s.hasTable("work_pattern_profile_session_cache_v1")
			b, e2 := s.hasIndex(
				"idx_work_pattern_profile_session_cache_retention_v1",
			)
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"self_dream", func() (bool, error) {
			a, e1 := s.hasTable("self_dream_runs_v1")
			b, e2 := s.hasTable("self_dream_operations_v1")
			c, e3 := s.hasTable("self_dream_replays_v1")
			d, e4 := s.hasTrigger("self_dream_runs_v1_no_update")
			e, e5 := s.hasTrigger("self_dream_replays_v1_no_delete")
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"background_understanding", func() (bool, error) {
			a, e1 := s.hasTable(
				"background_understanding_setting_events_v1",
			)
			b, e2 := s.hasTable("background_source_grants_v1")
			c, e3 := s.hasTable("background_understanding_runs_v1")
			d, e4 := s.hasTable(
				"background_understanding_source_items_v1",
			)
			e, e5 := s.hasTable(
				"background_understanding_materializations_v1",
			)
			f, e6 := s.hasIndex("idx_background_runs_created_v1")
			g, e7 := s.hasTrigger(
				"background_understanding_setting_events_v1_no_update",
			)
			h, e8 := s.hasTrigger(
				"background_understanding_materializations_v1_no_delete",
			)
			if err := firstErr(
				e1, e2, e3, e4, e5, e6, e7, e8,
			); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"agency_initiatives_v2", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiatives_v2")
			b, e2 := s.hasTable("agency_planning_runs_v2")
			c, e3 := s.hasTable("agency_initiative_runs_v2")
			d, e4 := s.hasTable("agency_initiative_actions_v2")
			e, e5 := s.hasIndex("idx_agency_initiative_runs_v2_initiative")
			f, e6 := s.hasIndex("idx_agency_initiative_run_events_v2_pending")
			if err := firstErr(e1, e2, e3, e4, e5, e6); err != nil {
				return false, err
			}
			return a && b && c && d && e && f, nil
		}},
		{"agency_initiative_outcome_evaluations", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_outcome_evaluations_v1")
			b, e2 := s.hasIndex("idx_agency_outcome_evaluations_outcome_v1")
			c, e3 := s.hasTrigger(
				"agency_initiative_outcome_evaluations_v1_no_update",
			)
			d, e4 := s.hasTrigger(
				"agency_initiative_outcome_evaluations_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"agency_initiative_feedback_applications", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_feedback_applications_v1")
			b, e2 := s.hasIndex("idx_agency_feedback_applications_worker_v1")
			c, e3 := s.hasIndex("idx_agency_feedback_applications_feedback_v1")
			d, e4 := s.hasTrigger(
				"agency_initiative_feedback_applications_v1_no_update",
			)
			e, e5 := s.hasTrigger(
				"agency_initiative_feedback_applications_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"agency_initiative_feedback_adherence", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_feedback_adherence_attempts_v1")
			b, e2 := s.hasTable("agency_initiative_feedback_adherence_evaluations_v1")
			c, e3 := s.hasIndex("idx_agency_feedback_adherence_attempts_application_v1")
			d, e4 := s.hasIndex("idx_agency_feedback_adherence_evaluations_application_v1")
			e, e5 := s.hasTrigger(
				"agency_initiative_feedback_adherence_attempts_v1_no_update",
			)
			f, e6 := s.hasTrigger(
				"agency_initiative_feedback_adherence_attempts_v1_no_delete",
			)
			g, e7 := s.hasTrigger(
				"agency_initiative_feedback_adherence_evaluations_v1_no_update",
			)
			h, e8 := s.hasTrigger(
				"agency_initiative_feedback_adherence_evaluations_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"agency_initiative_feedback_calibration", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_feedback_calibration_samples_v1")
			b, e2 := s.hasIndex("idx_agency_feedback_calibration_cohort_v1")
			c, e3 := s.hasIndex("idx_agency_feedback_calibration_pair_v1")
			d, e4 := s.hasTrigger(
				"agency_initiative_feedback_calibration_samples_v1_no_update",
			)
			e, e5 := s.hasTrigger(
				"agency_initiative_feedback_calibration_samples_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"agency_initiative_planner_canary", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_planner_canary_experiments_v1")
			b, e2 := s.hasTable("agency_initiative_planner_canary_cases_v1")
			c, e3 := s.hasTable("agency_initiative_planner_canary_attempts_v1")
			d, e4 := s.hasTable("agency_initiative_planner_canary_comparisons_v1")
			e, e5 := s.hasIndex("idx_agency_planner_canary_cases_experiment_v1")
			f, e6 := s.hasIndex("idx_agency_planner_canary_attempts_experiment_v1")
			g, e7 := s.hasTrigger(
				"agency_initiative_planner_canary_experiments_v1_no_update",
			)
			h, e8 := s.hasTrigger(
				"agency_initiative_planner_canary_cases_v1_no_update",
			)
			i, e9 := s.hasTrigger(
				"agency_initiative_planner_canary_attempts_v1_no_update",
			)
			j, e10 := s.hasTrigger(
				"agency_initiative_planner_canary_comparisons_v1_no_update",
			)
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8, e9, e10); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h && i && j, nil
		}},
		{"agency_initiative_planner_release_decisions", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_planner_release_decisions_v1")
			b, e2 := s.hasIndex("idx_agency_planner_release_decisions_experiment_v1")
			c, e3 := s.hasTrigger(
				"agency_initiative_planner_release_decisions_v1_no_update",
			)
			d, e4 := s.hasTrigger(
				"agency_initiative_planner_release_decisions_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"agency_initiative_planner_rollout", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_planner_rollout_events_v1")
			b, e2 := s.hasIndex("idx_agency_planner_rollout_events_created_v1")
			c, e3 := s.hasTrigger(
				"agency_initiative_planner_rollout_events_v1_no_update",
			)
			d, e4 := s.hasTrigger(
				"agency_initiative_planner_rollout_events_v1_no_delete",
			)
			e, e5 := s.hasColumn("agency_planning_runs_v2", "planner_rollout_event_id")
			f, e6 := s.hasColumn(
				"agency_planning_runs_v2",
				"planner_rollout_event_payload_hash",
			)
			g, e7 := s.hasColumn("agency_planning_runs_v2", "planner_release_decision_id")
			h, e8 := s.hasColumn(
				"agency_planning_runs_v2",
				"planner_release_decision_payload_hash",
			)
			i, e9 := s.hasColumn("agency_planning_runs_v2", "rendered_prompt_hash")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8, e9); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h && i, nil
		}},
		{"agency_initiative_planner_run_provenance_columns", func() (bool, error) {
			a, e1 := s.hasColumn("agency_planning_runs_v2", "planner_rollout_event_id")
			b, e2 := s.hasColumn(
				"agency_planning_runs_v2",
				"planner_rollout_event_payload_hash",
			)
			c, e3 := s.hasColumn("agency_planning_runs_v2", "planner_release_decision_id")
			d, e4 := s.hasColumn(
				"agency_planning_runs_v2",
				"planner_release_decision_payload_hash",
			)
			e, e5 := s.hasColumn("agency_planning_runs_v2", "rendered_prompt_hash")
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"agency_initiative_planner_rollout_monitoring", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_planner_rollout_observations_v1")
			b, e2 := s.hasIndex("idx_agency_planner_rollout_observations_epoch_v1")
			c, e3 := s.hasIndex("idx_agency_planner_rollout_observations_subject_v1")
			d, e4 := s.hasTrigger(
				"agency_initiative_planner_rollout_observations_v1_no_update",
			)
			e, e5 := s.hasTrigger(
				"agency_initiative_planner_rollout_observations_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"agency_initiative_planner_monitoring_reviews", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_planner_monitoring_reviews_v1")
			b, e2 := s.hasIndex("idx_agency_planner_monitoring_reviews_time_v1")
			c, e3 := s.hasTrigger(
				"agency_initiative_planner_monitoring_reviews_v1_no_update",
			)
			d, e4 := s.hasTrigger(
				"agency_initiative_planner_monitoring_reviews_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"agency_initiative_planner_rollback_review_provenance", func() (bool, error) {
			a, e1 := s.hasTable(
				"agency_initiative_planner_rollback_review_provenance_v1",
			)
			b, e2 := s.hasIndex(
				"idx_agency_planner_rollback_review_provenance_time_v1",
			)
			c, e3 := s.hasTrigger(
				"agency_initiative_planner_rollback_review_provenance_v1_no_update",
			)
			d, e4 := s.hasTrigger(
				"agency_initiative_planner_rollback_review_provenance_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"agency_origination_runtime", func() (bool, error) {
			a, e1 := s.hasTable("agency_ambient_authority_grants_v1")
			b, e2 := s.hasTable("agency_ambient_authority_events_v1")
			c, e3 := s.hasTable("agency_origination_runs_v1")
			d, e4 := s.hasTable("agency_origination_candidates_v1")
			e, e5 := s.hasTable("agency_origination_permission_requests_v1")
			f, e6 := s.hasTable("agency_origination_permission_events_v1")
			g, e7 := s.hasIndex("idx_agency_origination_runs_created_v1")
			h, e8 := s.hasIndex("idx_agency_permission_events_request_v1")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"agency_operating_envelopes", func() (bool, error) {
			a, e1 := s.hasTable("agency_operating_envelopes_v1")
			b, e2 := s.hasTable("agency_operating_envelope_events_v1")
			c, e3 := s.hasIndex("idx_agency_operating_envelope_events_v1")
			d, e4 := s.hasTrigger("agency_operating_envelopes_v1_no_update")
			e, e5 := s.hasTrigger("agency_operating_envelopes_v1_no_delete")
			f, e6 := s.hasTrigger("agency_operating_envelope_events_v1_no_update")
			g, e7 := s.hasTrigger("agency_operating_envelope_events_v1_no_delete")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g, nil
		}},
		{"agency_activation_gate", func() (bool, error) {
			a, e1 := s.hasTable("agency_origination_gate_receipts_v1")
			b, e2 := s.hasIndex(
				"idx_agency_origination_gate_receipts_created_v1",
			)
			c, e3 := s.hasTrigger(
				"agency_origination_gate_receipts_v1_no_update",
			)
			d, e4 := s.hasTrigger(
				"agency_origination_gate_receipts_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"agency_operating_envelope_policy_guard", func() (bool, error) {
			return s.hasTrigger("agency_operating_envelopes_v1_policy_guard")
		}},
		{"agency_operating_envelope_gmail_draft_boundary", func() (bool, error) {
			return s.tableSchemaContains(
				"agency_operating_envelopes_v1",
				"local_and_gmail_draft",
			)
		}},
		{"agency_operating_envelope_purpose_scope", func() (bool, error) {
			a, e1 := s.hasColumn(
				"agency_operating_envelopes_v1",
				"purpose_scope_refs_json",
			)
			b, e2 := s.hasColumn(
				"agency_operating_envelopes_v1",
				"purpose_scope_hash",
			)
			c, e3 := s.hasTrigger("agency_operating_envelopes_v1_policy_guard")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"agency_operating_envelope_parallel_initiatives", func() (bool, error) {
			a, e1 := s.hasColumn(
				"agency_operating_envelopes_v1",
				"max_concurrent_initiatives",
			)
			b, e2 := s.tableSchemaContains(
				"agency_operating_envelopes_v1",
				"agency-operating-envelope-v4",
			)
			c, e3 := s.hasTrigger("agency_operating_envelopes_v1_policy_guard")
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"agency_activation_gate_policy_v2", func() (bool, error) {
			a, e1 := s.tableSchemaContains(
				"agency_origination_gate_receipts_v1",
				"agency-activation-gate-v2",
			)
			b, e2 := s.hasTrigger(
				"agency_origination_gate_receipts_v1_no_update",
			)
			c, e3 := s.hasTrigger(
				"agency_origination_gate_receipts_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"agency_activation_gate_policy_v3", func() (bool, error) {
			a, e1 := s.tableSchemaContains(
				"agency_origination_gate_receipts_v1",
				"agency-activation-gate-v3",
			)
			b, e2 := s.hasTrigger(
				"agency_origination_gate_receipts_v1_no_update",
			)
			c, e3 := s.hasTrigger(
				"agency_origination_gate_receipts_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"agency_activation_gate_policy_v4", func() (bool, error) {
			a, e1 := s.tableSchemaContains(
				"agency_origination_gate_receipts_v1",
				"agency-activation-gate-v4",
			)
			b, e2 := s.hasTrigger(
				"agency_origination_gate_receipts_v1_no_update",
			)
			c, e3 := s.hasTrigger(
				"agency_origination_gate_receipts_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3); err != nil {
				return false, err
			}
			return a && b && c, nil
		}},
		{"agency_initiative_re_admissions", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_re_admissions_v1")
			b, e2 := s.hasTable("agency_initiative_re_admission_events_v1")
			c, e3 := s.hasIndex("idx_agency_re_admission_events_current_v1")
			d, e4 := s.hasIndex("idx_agency_re_admission_successor_once_v1")
			e, e5 := s.hasTrigger("agency_initiative_re_admissions_v1_no_update")
			f, e6 := s.hasTrigger("agency_initiative_re_admissions_v1_no_delete")
			g, e7 := s.hasTrigger(
				"agency_initiative_re_admission_events_v1_no_update",
			)
			h, e8 := s.hasTrigger(
				"agency_initiative_re_admission_events_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"agency_initiative_continuations", func() (bool, error) {
			a, e1 := s.hasTable("agency_initiative_continuations_v1")
			b, e2 := s.hasTable("agency_initiative_continuation_events_v1")
			c, e3 := s.hasIndex("idx_agency_continuation_events_current_v1")
			d, e4 := s.hasIndex("idx_agency_continuation_successor_once_v1")
			e, e5 := s.hasTrigger("agency_initiative_continuations_v1_no_update")
			f, e6 := s.hasTrigger("agency_initiative_continuations_v1_no_delete")
			g, e7 := s.hasTrigger(
				"agency_initiative_continuation_events_v1_no_update",
			)
			h, e8 := s.hasTrigger(
				"agency_initiative_continuation_events_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"agency_permission_decisions", func() (bool, error) {
			a, e1 := s.hasTable("agency_permission_decision_receipts_v1")
			b, e2 := s.hasIndex("idx_agency_permission_receipts_request_v1")
			c, e3 := s.hasIndex("idx_agency_permission_receipts_recent_v1")
			d, e4 := s.hasTrigger(
				"agency_permission_decision_receipts_v1_no_update",
			)
			e, e5 := s.hasTrigger(
				"agency_permission_decision_receipts_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"agency_schedule_commands", func() (bool, error) {
			a, e1 := s.hasTable("agency_schedule_command_receipts_v1")
			b, e2 := s.hasIndex("idx_agency_schedule_commands_schedule_v1")
			c, e3 := s.hasIndex("idx_agency_schedule_commands_recent_v1")
			d, e4 := s.hasTrigger(
				"agency_schedule_command_receipts_v1_no_update",
			)
			e, e5 := s.hasTrigger(
				"agency_schedule_command_receipts_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"agency_permission_overlap_resolutions", func() (bool, error) {
			a, e1 := s.hasTable("agency_permission_overlap_resolutions_v1")
			b, e2 := s.hasIndex(
				"idx_agency_permission_overlap_resolutions_recent_v1",
			)
			c, e3 := s.hasTrigger(
				"agency_permission_overlap_resolutions_v1_no_update",
			)
			d, e4 := s.hasTrigger(
				"agency_permission_overlap_resolutions_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"agency_permission_completed_by_user", func() (bool, error) {
			a, e1 := s.tableSchemaContains(
				"agency_origination_permission_events_v1",
				"'completed_by_user'",
			)
			b, e2 := s.hasIndex("idx_agency_permission_events_request_v1")
			c, e3 := s.hasTrigger("agency_origination_permission_events_v1_no_update")
			d, e4 := s.hasTrigger("agency_origination_permission_events_v1_no_delete")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"agency_handoff_user_completions", func() (bool, error) {
			a, e1 := s.hasTable("agency_handoff_user_completion_receipts_v1")
			b, e2 := s.hasIndex("idx_agency_handoff_user_completion_request_v1")
			c, e3 := s.hasIndex("idx_agency_handoff_user_completion_episode_v1")
			d, e4 := s.hasIndex("idx_agency_handoff_user_completion_recent_v1")
			e, e5 := s.hasTrigger(
				"agency_handoff_user_completion_receipts_v1_no_update",
			)
			f, e6 := s.hasTrigger(
				"agency_handoff_user_completion_receipts_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5, e6); err != nil {
				return false, err
			}
			return a && b && c && d && e && f, nil
		}},
		{"agency_permission_source_task_ended", func() (bool, error) {
			a, e1 := s.tableSchemaContains(
				"agency_origination_permission_events_v1",
				"'source_task_ended'",
			)
			b, e2 := s.hasIndex("idx_agency_permission_events_request_v1")
			c, e3 := s.hasTrigger("agency_origination_permission_events_v1_no_update")
			d, e4 := s.hasTrigger("agency_origination_permission_events_v1_no_delete")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"agency_handoff_source_end_retirements", func() (bool, error) {
			a, e1 := s.hasTable("agency_handoff_source_end_receipts_v1")
			b, e2 := s.hasIndex("idx_agency_handoff_source_end_request_v1")
			c, e3 := s.hasIndex("idx_agency_handoff_source_end_episode_v1")
			d, e4 := s.hasIndex("idx_agency_handoff_source_end_recent_v1")
			e, e5 := s.hasTrigger(
				"agency_handoff_source_end_receipts_v1_no_update",
			)
			f, e6 := s.hasTrigger(
				"agency_handoff_source_end_receipts_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5, e6); err != nil {
				return false, err
			}
			return a && b && c && d && e && f, nil
		}},
		{"agency_handoff_evidence_invalidations", func() (bool, error) {
			a, e1 := s.tableSchemaContains(
				"agency_origination_permission_events_v1",
				"'evidence_invalidated'",
			)
			b, e2 := s.hasTable("agency_handoff_evidence_invalidation_receipts_v1")
			c, e3 := s.hasIndex("idx_agency_handoff_evidence_invalidation_request_v1")
			d, e4 := s.hasIndex("idx_agency_handoff_evidence_invalidation_recent_v1")
			e, e5 := s.hasTrigger(
				"agency_handoff_evidence_invalidation_receipts_v1_no_update",
			)
			f, e6 := s.hasTrigger(
				"agency_handoff_evidence_invalidation_receipts_v1_no_delete",
			)
			g, e7 := s.hasIndex("idx_agency_permission_events_request_v1")
			h, e8 := s.hasTrigger("agency_origination_permission_events_v1_no_update")
			i, e9 := s.hasTrigger("agency_origination_permission_events_v1_no_delete")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8, e9); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h && i, nil
		}},
		{"agency_schedule_evidence_invalidations", func() (bool, error) {
			a, e1 := s.hasTable("agency_schedule_evidence_invalidation_receipts_v1")
			b, e2 := s.hasIndex("idx_agency_schedule_evidence_invalidation_request_v1")
			c, e3 := s.hasIndex("idx_agency_schedule_evidence_invalidation_recent_v1")
			d, e4 := s.hasTrigger(
				"agency_schedule_evidence_invalidation_receipts_v1_no_update",
			)
			e, e5 := s.hasTrigger(
				"agency_schedule_evidence_invalidation_receipts_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"agency_chat", func() (bool, error) {
			a, e1 := s.hasTable("agency_chat_messages_v1")
			b, e2 := s.hasTable("agency_chat_runs_v1")
			c, e3 := s.hasIndex("idx_agency_chat_messages_context_v1")
			d, e4 := s.hasIndex("idx_agency_chat_runs_context_v1")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"task_agent_attempts", func() (bool, error) {
			a, e1 := s.hasTable("task_agent_attempts")
			b, e2 := s.hasTable("task_agent_attempt_events")
			c, e3 := s.hasIndex("idx_task_agent_attempts_phase")
			d, e4 := s.hasIndex("idx_task_agent_attempt_events_latest")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"first_run", func() (bool, error) {
			a, e1 := s.hasTable("first_run_journeys_v1")
			b, e2 := s.hasTable("first_run_journey_events_v1")
			c, e3 := s.hasTable("first_outcome_runs_v1")
			d, e4 := s.hasTable("first_outcome_run_events_v1")
			e, e5 := s.hasTable("first_outcome_artifacts_v1")
			f, e6 := s.hasTable("first_run_receipts_v1")
			g, e7 := s.hasTable("first_run_exclusions_v1")
			h, e8 := s.hasTable("agency_learning_corrections_v1")
			i, e9 := s.hasTrigger("first_run_journeys_v1_no_update")
			j, e10 := s.hasTrigger(
				"agency_learning_corrections_v1_no_delete",
			)
			if err := firstErr(
				e1, e2, e3, e4, e5, e6, e7, e8, e9, e10,
			); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h && i && j, nil
		}},
		{"first_outcome_debug_replays", func() (bool, error) {
			a, e1 := s.hasTable("first_outcome_debug_replays_v1")
			b, e2 := s.hasIndex("idx_first_outcome_debug_replays_v1_run")
			c, e3 := s.hasTrigger(
				"first_outcome_debug_replays_v1_no_update",
			)
			d, e4 := s.hasTrigger(
				"first_outcome_debug_replays_v1_no_delete",
			)
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			return a && b && c && d, nil
		}},
		{"self_user_directives", func() (bool, error) {
			a, e1 := s.hasTable("self_user_directives_v1")
			b, e2 := s.hasTable("self_user_directive_events_v1")
			c, e3 := s.hasIndex("idx_self_user_directive_events_current_v1")
			d, e4 := s.hasTrigger("self_user_directives_v1_no_update")
			e, e5 := s.hasTrigger("self_user_directives_v1_no_delete")
			f, e6 := s.hasTrigger("self_user_directive_events_v1_no_update")
			g, e7 := s.hasTrigger("self_user_directive_events_v1_no_delete")
			h, e8 := s.hasTrigger("self_model_entries_no_delete")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"purpose_map", func() (bool, error) {
			a, e1 := s.hasTable("purpose_open_loops_v1")
			b, e2 := s.hasTable("purpose_open_loop_events_v1")
			c, e3 := s.hasTable("purpose_open_loop_evidence_v1")
			d, e4 := s.hasTable("purpose_open_loop_relations_v1")
			e, e5 := s.hasTable("purpose_open_loop_relation_events_v1")
			f, e6 := s.hasIndex("idx_purpose_open_loop_events_current_v1")
			g, e7 := s.hasTrigger("purpose_open_loops_v1_no_update")
			h, e8 := s.hasTrigger("purpose_open_loop_relation_events_v1_no_delete")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"assistant_text_edits", func() (bool, error) {
			a, e1 := s.hasTable("assistant_text_edits_v1")
			b, e2 := s.hasIndex("assistant_text_edits_field_v1")
			c, e3 := s.hasTrigger("assistant_text_edits_v1_no_update")
			d, e4 := s.hasTrigger("assistant_text_edits_v1_no_delete")
			return a && b && c && d, firstErr(e1, e2, e3, e4)
		}},
		{"purpose_mail_title_repairs", func() (bool, error) {
			a, e1 := s.hasTable("purpose_mail_title_repairs_v1")
			b, e2 := s.hasTrigger("purpose_mail_title_repairs_v1_no_update")
			c, e3 := s.hasTrigger("purpose_mail_title_repairs_v1_no_delete")
			return a && b && c, firstErr(e1, e2, e3)
		}},
		{"activity_thread_outcome", func() (bool, error) {
			return s.hasColumn("activities", "thread_outcome")
		}},
		{"activity_user_corrections", s.activityUserCorrectionsPresent},
		{"activity_noticed", s.activityNoticedPresent},
		{"meeting_call_turns", func() (bool, error) {
			a, e1 := s.hasTable("meeting_call_turns")
			b, e2 := s.hasTable("meeting_call_speakers")
			c, e3 := s.hasTable("meeting_voice_signatures")
			d, e4 := s.hasTable("meeting_voice_state")
			e, e5 := s.hasTrigger("meeting_call_delete")
			f, e6 := s.hasTrigger("meeting_call_slice_delete")
			g, e7 := s.hasIndex("idx_meeting_call_turns_call")
			return a && b && c && d && e && f && g, firstErr(e1, e2, e3, e4, e5, e6, e7)
		}},
		{"meeting_call_turn_provenance", func() (bool, error) {
			return s.hasColumn("meeting_call_turns", "legacy_text")
		}},
		{"activity_threads", s.activityThreadsPresent},
		{"activity_thread_link_state", func() (bool, error) {
			a, e1 := s.hasColumn("activity_threads", "user_confirmed")
			b, e2 := s.hasColumn("activity_threads", "reason_basis")
			c, e3 := s.hasTrigger("activity_thread_confirmation_insert")
			d, e4 := s.hasTrigger("activity_thread_confirmation_delete")
			e, e5 := s.hasTrigger("activity_thread_confirmation_move")
			return a && b && c && d && e, firstErr(e1, e2, e3, e4, e5)
		}},
		{"meeting_notes", func() (bool, error) {
			a, e1 := s.hasTable("meeting_notes")
			b, e2 := s.hasTable("meeting_note_slices")
			c, e3 := s.hasIndex("idx_meeting_note_slices_meeting")
			return a && b && c, firstErr(e1, e2, e3)
		}},
		{"meeting_notes_learned_kinds", func() (bool, error) {
			return s.hasColumn("meeting_notes", "learned_kinds")
		}},
		{"purpose_extraction", func() (bool, error) {
			a, e1 := s.hasTable("purpose_extraction_runs_v1")
			b, e2 := s.hasTable("purpose_extraction_hypotheses_v1")
			c, e3 := s.hasTable("purpose_reconciliation_receipts_v1")
			d, e4 := s.hasIndex("idx_purpose_extraction_runs_created_v1")
			e, e5 := s.hasIndex("idx_purpose_receipts_run_v1")
			f, e6 := s.hasTrigger("purpose_extraction_runs_v1_no_update")
			g, e7 := s.hasTrigger("purpose_extraction_hypotheses_v1_no_delete")
			h, e8 := s.hasTrigger("purpose_reconciliation_receipts_v1_no_delete")
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"purpose_agency_outcome_receipts", func() (bool, error) {
			a, e1 := s.hasTable("purpose_agency_outcome_receipts_v1")
			b, e2 := s.hasIndex("idx_purpose_agency_outcome_receipts_created_v1")
			c, e3 := s.hasIndex("idx_purpose_agency_outcome_receipts_purpose_v1")
			d, e4 := s.hasTrigger("purpose_agency_outcome_receipts_v1_no_update")
			e, e5 := s.hasTrigger("purpose_agency_outcome_receipts_v1_no_delete")
			if err := firstErr(e1, e2, e3, e4, e5); err != nil {
				return false, err
			}
			return a && b && c && d && e, nil
		}},
		{"purpose_agency_outcome_receipt_provenance_status", func() (bool, error) {
			var schema string
			err := s.db.QueryRow(`
				SELECT sql FROM sqlite_master
				WHERE type = 'table' AND name = 'purpose_agency_outcome_receipts_v1'
			`).Scan(&schema)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			return strings.Contains(strings.ToLower(schema), "'provenance_failed'"), nil
		}},
		{"task_shadow_observer_traces", func() (bool, error) {
			a, e1 := s.hasTable("task_shadow_observer_traces_v1")
			b, e2 := s.hasTable("task_shadow_observer_trace_events_v1")
			c, e3 := s.hasIndex("idx_task_shadow_observer_traces_v1_recent")
			d, e4 := s.hasIndex("idx_task_shadow_observer_trace_events_v1_latest")
			e, e5 := s.hasTrigger("task_shadow_observer_traces_v1_no_update")
			f, e6 := s.hasTrigger("task_shadow_observer_traces_v1_no_delete")
			g, e7 := s.hasTrigger("task_shadow_observer_trace_events_v1_no_update")
			h, e8 := s.hasTrigger("task_shadow_observer_trace_events_v1_no_delete")
			i, e9 := s.hasTrigger(
				"task_shadow_observer_trace_events_v1_validate_insert",
			)
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8, e9); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h && i, nil
		}},
		{"task_shadow_observer_coverage", func() (bool, error) {
			a, e1 := s.hasColumn(
				"activity_pipeline_runs",
				"shadow_observer_expected",
			)
			b, e2 := s.hasColumn(
				"activity_pipeline_runs",
				"shadow_observer_admission",
			)
			c, e3 := s.hasColumn(
				"activity_pipeline_runs",
				"shadow_observer_dispatch_status",
			)
			d, e4 := s.hasColumn(
				"activity_pipeline_runs",
				"shadow_observer_dispatch_reason",
			)
			e, e5 := s.hasColumn(
				"activity_pipeline_runs",
				"shadow_observer_trace_id",
			)
			f, e6 := s.hasColumn(
				"task_shadow_observer_traces_v1",
				"source_pipeline_run_id",
			)
			g, e7 := s.hasIndex(
				"idx_activity_pipeline_runs_shadow_observer_trace",
			)
			h, e8 := s.hasIndex(
				"idx_task_shadow_observer_traces_v1_source_pipeline",
			)
			if err := firstErr(e1, e2, e3, e4, e5, e6, e7, e8); err != nil {
				return false, err
			}
			return a && b && c && d && e && f && g && h, nil
		}},
		{"task_shadow_observer_exact_input", func() (bool, error) {
			a, e1 := s.hasColumn(
				"task_shadow_observer_traces_v1",
				"input_identity_hash",
			)
			b, e2 := s.hasIndex(
				"idx_task_shadow_observer_traces_v1_exact_input",
			)
			if err := firstErr(e1, e2); err != nil {
				return false, err
			}
			return a && b, nil
		}},
		{"task_context_manifest_stats", func() (bool, error) {
			a, e1 := s.hasTable("task_context_manifest_stats_v1")
			b, e2 := s.hasIndex("idx_task_context_manifest_stats_created_v1")
			c, e3 := s.hasTrigger("task_context_manifest_stats_v1_no_update")
			d, e4 := s.hasTrigger("task_context_manifest_stats_v1_no_delete")
			if err := firstErr(e1, e2, e3, e4); err != nil {
				return false, err
			}
			if !a || !b || !c || !d {
				return false, nil
			}
			var missing int
			if err := s.db.QueryRow(`
				SELECT COUNT(*)
				FROM task_episode_context_snapshots context
				LEFT JOIN task_context_manifest_stats_v1 stats
					ON stats.task_episode_id = context.task_episode_id
				WHERE stats.task_episode_id IS NULL
			`).Scan(&missing); err != nil {
				return false, err
			}
			return missing == 0, nil
		}},
		{"activity_retention_unavailable", func() (bool, error) {
			return s.hasTable("activity_retention_unavailable_v1")
		}},
		{"activity_retention_sources_minimal", func() (bool, error) {
			exists, err := s.hasTable("activity_retention_sources_v1")
			if err != nil || !exists {
				return false, err
			}
			var extra int
			err = s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('activity_retention_sources_v1')
				WHERE name NOT IN ('source_kind', 'source_id', 'excluded')`).Scan(&extra)
			return extra == 0, err
		}},
		{"activity_retention_sources", func() (bool, error) {
			return s.hasTable("activity_retention_sources_v1")
		}},
		{"drop_removed_agency_tables", func() (bool, error) {
			// Absent tables cannot tell a finished drop from a database the
			// Agency migrations have not reached yet. Always return false so
			// the drop runs after them; it only uses DROP TABLE IF EXISTS.
			return false, nil
		}},
		{"activity_events_kind_time_index", func() (bool, error) {
			return s.hasIndex("idx_activity_events_kind_time")
		}},
		{"browser_visits", func() (bool, error) {
			a, e1 := s.hasTable("browser_visits")
			b, e2 := s.hasIndex("idx_browser_visits_visited")
			return a && b, firstErr(e1, e2)
		}},
		{"file_activity", func() (bool, error) {
			a, e1 := s.hasTable("file_activity")
			b, e2 := s.hasIndex("idx_file_activity_active")
			return a && b, firstErr(e1, e2)
		}},
		{"browser_site_days", func() (bool, error) {
			a, e1 := s.hasTable("browser_site_days")
			b, e2 := s.hasIndex("idx_browser_site_days_day")
			return a && b, firstErr(e1, e2)
		}},
		{"activity_run_evidence_tokens", func() (bool, error) {
			a, e1 := s.hasColumn("activity_timeline_groups", "evidence_tokens")
			b, e2 := s.hasColumn("activity_timeline_groups", "evidence_excluded")
			return a && b, firstErr(e1, e2)
		}},
		{"activity_quiet_settling", func() (bool, error) {
			return s.hasColumn("activities", "paused_at_ms")
		}},
		{"activity_outcomes_and_corrections", func() (bool, error) {
			a, e1 := s.hasTable("activity_outcomes")
			b, e2 := s.hasTable("activity_run_corrections")
			c, e3 := s.hasColumn("activity_run_links", "link_reason")
			d, e4 := s.hasColumn("activity_run_links", "link_token_kind")
			e, e5 := s.hasColumn("activity_run_links", "link_token_value")
			f, e6 := s.hasIndex("idx_activity_outcomes_activity")
			g, e7 := s.hasIndex("idx_activity_run_corrections_run")
			return a && b && c && d && e && f && g, firstErr(e1, e2, e3, e4, e5, e6, e7)
		}},
		{"activity_evidence_identifier_policy", func() (bool, error) {
			// A schema probe cannot prove a data cleanup ran. Reapply this
			// idempotent privacy cleanup if the migration log was lost.
			return false, nil
		}},
		{"activity_evidence_receipt_policy", func() (bool, error) {
			return false, nil
		}},
		{"calendar_holds", func() (bool, error) { return s.hasTable("holds") }},
		{"calendar_hold_dismissals", func() (bool, error) { return s.hasTable("hold_offer_dismissals") }},
		{"calendar_hold_active_source", func() (bool, error) {
			a, e1 := s.hasColumn("holds", "source_active")
			b, e2 := s.hasIndex("holds_active_source")
			return a && b, firstErr(e1, e2)
		}},
		{"calendar_hold_recovery", func() (bool, error) { return s.hasColumn("holds", "failure_reason") }},
		{"task_episode_read_revision", func() (bool, error) {
			// The table and all nine change triggers: a partial set is not derived.
			a, e1 := s.hasTable("task_episode_read_revision")
			var triggers int
			e2 := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'task_episode_read_revision_%'`).Scan(&triggers)
			return a && triggers == 9, firstErr(e1, e2)
		}},
		{"activity_event_context_index", func() (bool, error) {
			return s.hasIndex("idx_activity_events_context")
		}},
		{"activity_end_id_index", func() (bool, error) {
			return s.hasIndex("idx_activities_end_id")
		}},
		{"activity_summary_source_provenance", func() (bool, error) { return s.hasColumn("activity_summaries", "source_provenance") }},
		{"activity_retained_source_privacy", func() (bool, error) { return s.hasColumn("activity_retained_source_privacy", "provenance") }},
		{"activity_outcome_measurement_cache", func() (bool, error) {
			a, e1 := s.hasColumn("activity_run_evidence_state", "measurement_evidence")
			b, e2 := s.hasColumn("activity_outcome_observations", "event_rewrite_fingerprint")
			c, e3 := s.hasColumn("activity_outcome_observations", "event_row_id")
			d, e4 := s.hasColumn("activity_outcome_observations", "message_context_fingerprint")
			e, e5 := s.hasTable("activity_event_rewrite_revisions")
			var triggers int
			e6 := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'activity_event_rewrite_%'`).Scan(&triggers)
			f, e7 := s.hasColumn("activity_event_rewrite_revisions", "snapshot_revision")
			return a && b && c && d && e && f && triggers == 5, firstErr(e1, e2, e3, e4, e5, e6, e7)
		}},
		{"activity_evidence_source_revisions", func() (bool, error) {
			a, e1 := s.hasTable("activity_evidence_source_revisions")
			b, e2 := s.hasTable("activity_run_evidence_state")
			c, e3 := s.hasTable("activity_outcome_observations")
			var triggers int
			e4 := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'evidence_revision_%'`).Scan(&triggers)
			return a && b && c && triggers == 9, firstErr(e1, e2, e3, e4)
		}},
	}

	// Step 1: collect applied migration names outside transaction.
	var applied []string
	for _, rule := range rules {
		ok, err := rule.Check()
		if err != nil {
			return fmt.Errorf("bootstrap: probe %q failed: %w", rule.Name, err)
		}
		if ok {
			applied = append(applied, rule.Name)
		}
	}

	if len(applied) == 0 {
		return nil
	}

	// Step 2: batch write inside transaction (atomic).
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin bootstrap tx: %w", err)
	}
	defer tx.Rollback()

	for _, name := range applied {
		if _, err := tx.Exec("INSERT INTO migration_log (name, applied_at) VALUES (?, ?)",
			name, time.Now().Unix()); err != nil {
			return fmt.Errorf("record migration %q during bootstrap: %w", name, err)
		}
	}
	return tx.Commit()
}

// runPendingMigrations executes migrations not yet recorded in migration_log.
// Individual migration failures are logged but do not block store initialization
// (fail-and-continue). In SQLite local context, the design goal is no
// half-migration state; Commit failure is treated as unconfirmed and retried on
// next startup (migration functions must be idempotent).
func (s *ActivityStore) runPendingMigrations() {
	s.appliedMigrations = s.loadAppliedMigrations()

	for _, m := range allActivityMigrations {
		if s.appliedMigrations[m.Name] {
			continue
		}

		tx, err := s.db.Begin()
		if err != nil {
			log.Printf("[activity] migration %q: begin tx failed: %v", m.Name, err)
			continue
		}

		if err := m.Fn(tx); err != nil {
			tx.Rollback()
			log.Printf("[activity] migration %q failed (feature degraded): %v", m.Name, err)
			continue
		}

		if _, err := tx.Exec("INSERT INTO migration_log (name, applied_at) VALUES (?, ?)",
			m.Name, time.Now().Unix()); err != nil {
			tx.Rollback()
			log.Printf("[activity] migration %q: record failed: %v", m.Name, err)
			continue
		}

		if err := tx.Commit(); err != nil {
			log.Printf("[activity] migration %q: commit failed: %v", m.Name, err)
			continue
		}

		s.appliedMigrations[m.Name] = true
		log.Printf("[activity] migration %q applied", m.Name)
	}
}

// ──────────────────────────────────────────────
// Migration functions
//
// Each function receives a *sql.Tx and executes DDL idempotently.
// The registry records success in migration_log within the same tx.
// ──────────────────────────────────────────────

func migrateCoreTables(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_sessions (
			id               TEXT PRIMARY KEY,
			start_time       INTEGER NOT NULL,
			end_time         INTEGER NOT NULL,
			trigger_kind     TEXT NOT NULL,
			app_names        TEXT NOT NULL,
			event_count      INTEGER NOT NULL,
			segment_count    INTEGER NOT NULL,
			total_size_bytes INTEGER NOT NULL DEFAULT 0,
			triage_status    TEXT NOT NULL DEFAULT 'pending',
			created_at       INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS activity_events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL REFERENCES activity_sessions(id) ON DELETE CASCADE,
			timestamp  INTEGER NOT NULL,
			kind       TEXT NOT NULL,
			data       TEXT NOT NULL,
			sort_order INTEGER NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_activity_events_session ON activity_events(session_id);

		CREATE TABLE IF NOT EXISTS activity_segments (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id   TEXT NOT NULL REFERENCES activity_sessions(id) ON DELETE CASCADE,
			start_time   INTEGER NOT NULL,
			end_time     INTEGER NOT NULL,
			file_path    TEXT NOT NULL,
			width        INTEGER NOT NULL,
			height       INTEGER NOT NULL,
			fps          INTEGER NOT NULL,
			codec        TEXT NOT NULL,
			size_bytes   INTEGER NOT NULL,
			storage_tier TEXT NOT NULL DEFAULT 'hot'
		);

		CREATE INDEX IF NOT EXISTS idx_activity_segments_session ON activity_segments(session_id)
	`)
	if err != nil {
		return fmt.Errorf("create core tables: %w", err)
	}
	return nil
}

// migrateActivityEventsKindTimeIndex lets the capture counts (capture_counts.go)
// count one kind of event in a time window from the index alone. Without it
// every event row of the window is read to check its kind, and speech rows
// carry a voice embedding each.
//
// It runs once, while the activity store opens; on a large activity.db it
// takes a while, so its start and duration are logged.
func migrateActivityEventsKindTimeIndex(tx *sql.Tx) error {
	started := time.Now()
	log.Printf("[activity] building idx_activity_events_kind_time")
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_events_kind_time ON activity_events(kind, timestamp)`); err != nil {
		return fmt.Errorf("create activity_events kind/time index: %w", err)
	}
	log.Printf("[activity] built idx_activity_events_kind_time in %s", time.Since(started).Round(time.Millisecond))
	return nil
}

func migrateActivityEndIDIndex(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activities_end_id ON activities(end_ms DESC, id DESC)`); err != nil {
		return fmt.Errorf("create activities end/id index: %w", err)
	}
	return nil
}

func migrateStartTimeIndex(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_sessions_start_time ON activity_sessions(start_time)`); err != nil {
		return fmt.Errorf("create start_time index: %w", err)
	}
	return nil
}

func migrateOCRFramesFTS(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_ocr_frames (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL REFERENCES activity_sessions(id) ON DELETE CASCADE,
			segment_id INTEGER NOT NULL REFERENCES activity_segments(id) ON DELETE CASCADE,
			frame_time REAL NOT NULL,
			text       TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 0.0
		)
	`); err != nil {
		return fmt.Errorf("create ocr_frames: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_ocr_frames_session ON activity_ocr_frames(session_id)`); err != nil {
		return fmt.Errorf("create ocr_frames index: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS activity_ocr_fts USING fts5(
			text, content='activity_ocr_frames', content_rowid='id',
			tokenize='trigram'
		)
	`); err != nil {
		return fmt.Errorf("create ocr_fts: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TRIGGER IF NOT EXISTS ocr_fts_insert AFTER INSERT ON activity_ocr_frames BEGIN
			INSERT INTO activity_ocr_fts(rowid, text) VALUES (new.id, new.text);
		END
	`); err != nil {
		return fmt.Errorf("create fts insert trigger: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TRIGGER IF NOT EXISTS ocr_fts_delete AFTER DELETE ON activity_ocr_frames BEGIN
			INSERT INTO activity_ocr_fts(activity_ocr_fts, rowid, text) VALUES ('delete', old.id, old.text);
		END
	`); err != nil {
		return fmt.Errorf("create fts delete trigger: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TRIGGER IF NOT EXISTS ocr_fts_update AFTER UPDATE ON activity_ocr_frames BEGIN
			INSERT INTO activity_ocr_fts(activity_ocr_fts, rowid, text) VALUES ('delete', old.id, old.text);
			INSERT INTO activity_ocr_fts(rowid, text) VALUES (new.id, new.text);
		END
	`); err != nil {
		return fmt.Errorf("create fts update trigger: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE activity_sessions ADD COLUMN ocr_status TEXT NOT NULL DEFAULT 'pending'`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add ocr_status: %w", err)
		}
	}
	return nil
}

func migrateSnapshots(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_snapshots (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id   TEXT REFERENCES activity_sessions(id) ON DELETE CASCADE,
			timestamp    INTEGER NOT NULL,
			file_path    TEXT NOT NULL,
			width        INTEGER,
			height       INTEGER,
			size_bytes   INTEGER,
			trigger_kind TEXT,
			storage_tier TEXT NOT NULL DEFAULT 'hot'
		)
	`); err != nil {
		return fmt.Errorf("create snapshots: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_snapshots_session ON activity_snapshots(session_id)`); err != nil {
		return fmt.Errorf("create snapshots session index: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_snapshots_timestamp ON activity_snapshots(timestamp)`); err != nil {
		return fmt.Errorf("create snapshots timestamp index: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE activity_sessions ADD COLUMN snapshot_count INTEGER DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add snapshot_count: %w", err)
		}
	}
	return nil
}

func migrateOCRConstraintFix(tx *sql.Tx) error {
	// Rebuild activity_ocr_frames to drop FOREIGN KEY on segment_id.
	// Snapshot sessions store snapshot IDs in segment_id, which don't exist
	// in activity_segments. SQLite can't ALTER CONSTRAINT, so we rebuild.
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_ocr_frames_new (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL REFERENCES activity_sessions(id) ON DELETE CASCADE,
			segment_id INTEGER NOT NULL,
			frame_time REAL NOT NULL,
			text       TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 0.0
		)
	`); err != nil {
		return fmt.Errorf("create new ocr table: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO activity_ocr_frames_new (id, session_id, segment_id, frame_time, text, confidence)
		SELECT id, session_id, segment_id, frame_time, text, confidence FROM activity_ocr_frames
	`); err != nil {
		return fmt.Errorf("copy ocr data: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS activity_ocr_frames`); err != nil {
		return fmt.Errorf("drop old ocr table: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE activity_ocr_frames_new RENAME TO activity_ocr_frames`); err != nil {
		return fmt.Errorf("rename ocr table: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_ocr_frames_session ON activity_ocr_frames(session_id)`); err != nil {
		return fmt.Errorf("recreate ocr index: %w", err)
	}
	// Recreate FTS triggers (they reference the table by name).
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ocr_fts_insert`); err != nil {
		return fmt.Errorf("recreate fts triggers: drop insert: %w", err)
	}
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ocr_fts_delete`); err != nil {
		return fmt.Errorf("recreate fts triggers: drop delete: %w", err)
	}
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ocr_fts_update`); err != nil {
		return fmt.Errorf("recreate fts triggers: drop update: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO activity_ocr_fts(activity_ocr_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("recreate fts triggers: rebuild fts: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TRIGGER IF NOT EXISTS ocr_fts_insert AFTER INSERT ON activity_ocr_frames BEGIN
			INSERT INTO activity_ocr_fts(rowid, text) VALUES (new.id, new.text);
		END
	`); err != nil {
		return fmt.Errorf("recreate fts insert trigger: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TRIGGER IF NOT EXISTS ocr_fts_delete AFTER DELETE ON activity_ocr_frames BEGIN
			INSERT INTO activity_ocr_fts(activity_ocr_fts, rowid, text) VALUES ('delete', old.id, old.text);
		END
	`); err != nil {
		return fmt.Errorf("recreate fts delete trigger: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TRIGGER IF NOT EXISTS ocr_fts_update AFTER UPDATE ON activity_ocr_frames BEGIN
			INSERT INTO activity_ocr_fts(activity_ocr_fts, rowid, text) VALUES ('delete', old.id, old.text);
			INSERT INTO activity_ocr_fts(rowid, text) VALUES (new.id, new.text);
		END
	`); err != nil {
		return fmt.Errorf("recreate fts update trigger: %w", err)
	}
	return nil
}

func migrateAutomationStatus(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_sessions ADD COLUMN automation_status TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add automation_status: %w", err)
		}
	}
	return nil
}

func migrateActivitySummariesV7(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_summaries (
			date       TEXT NOT NULL,
			type       TEXT NOT NULL,
			summary    TEXT NOT NULL,
			stats_json TEXT NOT NULL DEFAULT '{}',
			model      TEXT,
			is_partial INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (date, type)
		)
	`); err != nil {
		return fmt.Errorf("create activity_summaries: %w", err)
	}
	return nil
}

func migrateFTSTrigram(tx *sql.Tx) error {
	// Switch FTS5 tokenizer to trigram for CJK substring search.
	// Existing users may have the FTS table created with unicode61 in V3.
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ocr_fts_insert`); err != nil {
		return fmt.Errorf("fts trigram: drop insert trigger: %w", err)
	}
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ocr_fts_delete`); err != nil {
		return fmt.Errorf("fts trigram: drop delete trigger: %w", err)
	}
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ocr_fts_update`); err != nil {
		return fmt.Errorf("fts trigram: drop update trigger: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS activity_ocr_fts`); err != nil {
		return fmt.Errorf("fts trigram: drop fts table: %w", err)
	}

	if _, err := tx.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS activity_ocr_fts USING fts5(
			text, content='activity_ocr_frames', content_rowid='id',
			tokenize='trigram'
		)
	`); err != nil {
		return fmt.Errorf("create fts with trigram: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO activity_ocr_fts(rowid, text) SELECT id, text FROM activity_ocr_frames`); err != nil {
		return fmt.Errorf("repopulate fts: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TRIGGER IF NOT EXISTS ocr_fts_insert AFTER INSERT ON activity_ocr_frames BEGIN
			INSERT INTO activity_ocr_fts(rowid, text) VALUES (new.id, new.text);
		END
	`); err != nil {
		return fmt.Errorf("create fts insert trigger: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TRIGGER IF NOT EXISTS ocr_fts_delete AFTER DELETE ON activity_ocr_frames BEGIN
			INSERT INTO activity_ocr_fts(activity_ocr_fts, rowid, text) VALUES ('delete', old.id, old.text);
		END
	`); err != nil {
		return fmt.Errorf("create fts delete trigger: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TRIGGER IF NOT EXISTS ocr_fts_update AFTER UPDATE ON activity_ocr_frames BEGIN
			INSERT INTO activity_ocr_fts(activity_ocr_fts, rowid, text) VALUES ('delete', old.id, old.text);
			INSERT INTO activity_ocr_fts(rowid, text) VALUES (new.id, new.text);
		END
	`); err != nil {
		return fmt.Errorf("create fts update trigger: %w", err)
	}
	return nil
}

// migrateSemanticStatusColumn adds an independent status track for the semantic
// event layer so it never couples to triage_status / cooldown. Existing sessions
// are historical (no backfill — design open Q5) and are marked done so the new
// pass skips them; future sessions default to 'pending' and get extracted.
func migrateSemanticStatusColumn(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_sessions ADD COLUMN semantic_status TEXT NOT NULL DEFAULT 'pending'`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add semantic_status column: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE activity_sessions SET semantic_status = 'done'`); err != nil {
		return fmt.Errorf("backfill semantic_status=done: %w", err)
	}
	return nil
}

// migrateSemanticDisplayKeywordsColumn adds the curated display-tag column to
// semantic events. Existing rows default to '[]'; GetSemanticEventsInRange
// derives keywords from the row's entities + title at read time when the column
// is empty (so historical/settled events still get clean tags without a backfill),
// and re-extracted/merged events populate it directly. Entities (the internal
// stitching signal) are left untouched.
func migrateSemanticDisplayKeywordsColumn(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_semantic_events ADD COLUMN display_keywords TEXT NOT NULL DEFAULT '[]'`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add display_keywords column: %w", err)
		}
	}
	return nil
}

// migrateOCRBoxesJSONColumn adds per-line bounding boxes to OCR frame rows.
// The background OCR worker has always computed boxes (RecognizeJPEGWithBoxes)
// and then discarded them, forcing the tag-highlight path to re-run a full
// Accurate OCR per frame at click time just to recover coordinates. Stored as
// a JSON array of OCRHighlightBox in FULL-FRAME normalized [0,1] coordinates
// (worker observations are mapped out of the focus crop before persisting).
// Empty string means "not computed" (legacy rows / text-only producers);
// '[]' means "computed, no text found". Existing rows stay empty — they
// backfill lazily via the on-demand highlight OCR's write-through.
func migrateOCRBoxesJSONColumn(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_ocr_frames ADD COLUMN boxes_json TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add boxes_json column: %w", err)
		}
	}
	return nil
}

// migrateSnapshotFilePathIndex indexes activity_snapshots.file_path, the
// lookup key of the OCR highlight-box cache (GetSnapshotOCRIdentityByFilePath
// runs on every first box fetch for a frame). Without it that click-path
// query is a full table scan over an unboundedly growing table.
func migrateSnapshotFilePathIndex(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_snapshots_file_path ON activity_snapshots(file_path)`); err != nil {
		return fmt.Errorf("create snapshots file_path index: %w", err)
	}
	return nil
}

// migrateSemanticEventsTable creates the activity_semantic_events table that
// backs the event-model layer (cross-app / cross-time semantic events). It is
// fully independent and does not touch any existing table. See
// docs/activity-event-model-p1-design.md (local) for the design.
func migrateSemanticEventsTable(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_semantic_events (
			id                 TEXT PRIMARY KEY,
			title              TEXT NOT NULL,
			description        TEXT NOT NULL DEFAULT '',
			category           TEXT NOT NULL,
			entities           TEXT NOT NULL DEFAULT '[]',
			apps               TEXT NOT NULL DEFAULT '[]',
			start_ms           INTEGER NOT NULL,
			end_ms             INTEGER NOT NULL,
			source_refs        TEXT NOT NULL DEFAULT '{}',
			thumbnail_refs     TEXT NOT NULL DEFAULT '[]',
			task_worthy        INTEGER NOT NULL DEFAULT 0,
			category_dot_color TEXT NOT NULL DEFAULT '',
			status             TEXT NOT NULL DEFAULT 'open',
			created_at         INTEGER NOT NULL,
			updated_at         INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create activity_semantic_events: %w", err)
	}
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_semantic_events_time ON activity_semantic_events(start_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_events_status ON activity_semantic_events(status)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_events_category ON activity_semantic_events(category)`,
		`CREATE INDEX IF NOT EXISTS idx_semantic_events_open_end ON activity_semantic_events(status, end_ms)`,
	} {
		if _, err := tx.Exec(idx); err != nil {
			return fmt.Errorf("create semantic_events index: %w", err)
		}
	}
	return nil
}

// migrateActivityTimelineGroupSummaries stores the per-app-run semantic layer
// shown in the Timeline view. Raw sessions/events remain the source of truth;
// this table only caches model summaries keyed by the deterministic app-group id.
func migrateActivityTimelineGroupSummaries(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_timeline_group_summaries (
			group_id      TEXT PRIMARY KEY,
			app_key       TEXT NOT NULL,
			app_name      TEXT NOT NULL,
			start_ms      INTEGER NOT NULL,
			end_ms        INTEGER NOT NULL,
			title         TEXT NOT NULL DEFAULT '',
			description   TEXT NOT NULL DEFAULT '',
			activity_type TEXT NOT NULL DEFAULT 'unknown',
			entities      TEXT NOT NULL DEFAULT '[]',
			timeline_json TEXT NOT NULL DEFAULT '[]',
			model         TEXT NOT NULL DEFAULT '',
			prompt        TEXT NOT NULL DEFAULT '',
			response_raw  TEXT NOT NULL DEFAULT '',
			created_at    INTEGER NOT NULL,
			updated_at    INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create activity_timeline_group_summaries: %w", err)
	}
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_timeline_group_summaries_time ON activity_timeline_group_summaries(start_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_timeline_group_summaries_app ON activity_timeline_group_summaries(app_key)`,
	} {
		if _, err := tx.Exec(idx); err != nil {
			return fmt.Errorf("create timeline group summary index: %w", err)
		}
	}
	return nil
}

// migrateActivityTimelineGroups creates the persisted per-app timeline run
// table and backfills it from the full event history, so runs exist with
// stable ids from the first launch after the upgrade. Existing summaries
// (keyed by the old derived "<ts>:<appKey>:<sessionID>" ids) are re-keyed to
// the new run ids by exact (app_key, start_ms) match — both id schemes anchor
// on the run's first event, so a mismatch means the summary belonged to a
// window-cut artifact and is dropped.
//
// Idempotent for the commit-failure retry case: the run table is rebuilt from
// scratch (derivation is deterministic), and the summary re-key only touches
// rows still carrying old-format ids.
func migrateActivityTimelineGroups(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_timeline_groups (
			id                     TEXT PRIMARY KEY,
			first_event_row_id     INTEGER NOT NULL UNIQUE,
			last_event_row_id      INTEGER NOT NULL,
			app_key                TEXT NOT NULL,
			app_name               TEXT NOT NULL,
			start_ms               INTEGER NOT NULL,
			end_ms                 INTEGER NOT NULL,
			active_ms              INTEGER NOT NULL DEFAULT 0,
			event_count            INTEGER NOT NULL DEFAULT 0,
			session_ids            TEXT NOT NULL DEFAULT '[]',
			trigger_kinds          TEXT NOT NULL DEFAULT '[]',
			status                 TEXT NOT NULL DEFAULT 'open',
			summary_status         TEXT NOT NULL DEFAULT 'none',
			summary_attempts       INTEGER NOT NULL DEFAULT 0,
			next_retry_at          INTEGER NOT NULL DEFAULT 0,
			summarized_event_count INTEGER NOT NULL DEFAULT 0,
			created_at             INTEGER NOT NULL,
			updated_at             INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create activity_timeline_groups: %w", err)
	}
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_timeline_groups_start ON activity_timeline_groups(start_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_timeline_groups_end ON activity_timeline_groups(end_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_timeline_groups_summary ON activity_timeline_groups(summary_status, next_retry_at, end_ms)`,
	} {
		if _, err := tx.Exec(idx); err != nil {
			return fmt.Errorf("create timeline groups index: %w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM activity_timeline_groups`); err != nil {
		return fmt.Errorf("reset activity_timeline_groups for rebuild: %w", err)
	}

	items, err := scanTimelineRunEventsFromRowID(tx, 0)
	if err != nil {
		return fmt.Errorf("backfill scan events: %w", err)
	}
	if len(items) > 0 {
		sortTimelineRunEvents(items)
		runs := deriveTimelineRuns(items)
		markTimelineRunStatuses(runs)
		nowMs := time.Now().UnixMilli()
		for _, run := range runs {
			if err := insertTimelineRun(tx, run, nowMs); err != nil {
				return fmt.Errorf("backfill insert: %w", err)
			}
		}
		if err := rekeyTimelineGroupSummaries(tx, runs); err != nil {
			return err
		}
	}

	// Any run that now owns a summary row (re-keyed above, or pre-existing
	// run-keyed rows from a retried migration) is done; remember the covered
	// event count so growth re-triggers regeneration.
	if _, err := tx.Exec(`
		UPDATE activity_timeline_groups
		SET summary_status = 'done', summarized_event_count = event_count
		WHERE id IN (SELECT group_id FROM activity_timeline_group_summaries)`); err != nil {
		return fmt.Errorf("mark summarized runs done: %w", err)
	}
	return nil
}

// migrateActivitiesTables creates the activity-composer layer (layer 3): one
// `activities` bucket per coherent piece of work, composed from per-app
// timeline runs, plus an `activity_run_links` table tracking which run has been
// routed into which bucket (and at what summarized-event count, so a run that
// later grows re-triggers a bucket refresh). Runs + run summaries remain the
// source of truth; these tables only hold the composed grouping.
func migrateActivitiesTables(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activities (
			id                    TEXT PRIMARY KEY,
			day_start_ms          INTEGER NOT NULL,
			status                TEXT NOT NULL DEFAULT 'active',
			title                 TEXT NOT NULL DEFAULT '',
			description           TEXT NOT NULL DEFAULT '',
			latest_change         TEXT NOT NULL DEFAULT '',
			category              TEXT NOT NULL DEFAULT '',
			category_color        TEXT NOT NULL DEFAULT '',
			entities              TEXT NOT NULL DEFAULT '[]',
			run_ids               TEXT NOT NULL DEFAULT '[]',
			active_ms_by_app      TEXT NOT NULL DEFAULT '{}',
			start_ms              INTEGER NOT NULL DEFAULT 0,
			end_ms                INTEGER NOT NULL DEFAULT 0,
			continues_activity_id TEXT NOT NULL DEFAULT '',
			created_at            INTEGER NOT NULL,
			updated_at            INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create activities: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_run_links (
			run_id               TEXT PRIMARY KEY,
			activity_id          TEXT NOT NULL,
			composed_event_count INTEGER NOT NULL DEFAULT 0,
			created_at           INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create activity_run_links: %w", err)
	}
	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_activities_day ON activities(day_start_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_activities_status_day ON activities(status, day_start_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_activities_end ON activities(end_ms)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_run_links_activity ON activity_run_links(activity_id)`,
	} {
		if _, err := tx.Exec(idx); err != nil {
			return fmt.Errorf("create activities index: %w", err)
		}
	}
	return nil
}

// rekeyTimelineGroupSummaries rewrites old-format summary keys
// ("<ts>:<appKey>:<sessionID>") to persisted run ids by (app_key, start_ms)
// match. Unmatched or duplicate-target rows are deleted — they are exactly
// the orphaned window-cut summaries the persisted-run design eliminates.
func rekeyTimelineGroupSummaries(tx *sql.Tx, runs []ActivityTimelineRunRow) error {
	type runRef struct {
		id string
	}
	byAppStart := make(map[string]runRef, len(runs))
	for _, run := range runs {
		byAppStart[fmt.Sprintf("%s|%d", run.AppKey, run.StartMs)] = runRef{id: run.ID}
	}

	rows, err := tx.Query(`
		SELECT group_id, app_key, start_ms
		FROM activity_timeline_group_summaries
		WHERE group_id NOT LIKE 'tg\_%' ESCAPE '\'`)
	if err != nil {
		return fmt.Errorf("query legacy timeline summaries: %w", err)
	}
	type legacySummary struct {
		groupID string
		appKey  string
		startMs int64
	}
	var legacy []legacySummary
	for rows.Next() {
		var row legacySummary
		if err := rows.Scan(&row.groupID, &row.appKey, &row.startMs); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy timeline summary: %w", err)
		}
		legacy = append(legacy, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	used := make(map[string]bool, len(legacy))
	for _, old := range legacy {
		ref, ok := byAppStart[fmt.Sprintf("%s|%d", old.appKey, old.startMs)]
		if !ok || used[ref.id] {
			if _, err := tx.Exec(`DELETE FROM activity_timeline_group_summaries WHERE group_id = ?`, old.groupID); err != nil {
				return fmt.Errorf("delete orphaned timeline summary %s: %w", old.groupID, err)
			}
			continue
		}
		used[ref.id] = true
		if _, err := tx.Exec(`
			UPDATE activity_timeline_group_summaries SET group_id = ? WHERE group_id = ?`,
			ref.id, old.groupID); err != nil {
			return fmt.Errorf("rekey timeline summary %s: %w", old.groupID, err)
		}
	}
	return nil
}

func migratePredictionTables(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS prediction_proposals (
			id                TEXT PRIMARY KEY,
			source_kind       TEXT NOT NULL,
			source_id         TEXT NOT NULL,
			status            TEXT NOT NULL DEFAULT 'pending',
			user_intent       TEXT NOT NULL,
			predicted_next    TEXT NOT NULL,
			tool_proposal     TEXT NOT NULL,
			app_name          TEXT NOT NULL,
			description       TEXT NOT NULL,
			features          TEXT NOT NULL DEFAULT '[]',
			confidence        REAL NOT NULL DEFAULT 0.0,
			chat_session_id   TEXT,
			dismiss_reason    TEXT,
			created_at        INTEGER NOT NULL,
			resolved_at       INTEGER
		)
	`); err != nil {
		return fmt.Errorf("create prediction_proposals: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_prediction_proposals_status ON prediction_proposals(status)`); err != nil {
		return fmt.Errorf("create prediction_proposals status index: %w", err)
	}
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS prediction_metrics (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			proposal_id TEXT NOT NULL,
			outcome     TEXT NOT NULL,
			source_kind TEXT NOT NULL,
			time_to_decision INTEGER,
			created_at  INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create prediction_metrics: %w", err)
	}
	return nil
}

func migratePredictionCronColumns(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE prediction_proposals ADD COLUMN kind TEXT NOT NULL DEFAULT 'prediction'`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add kind column: %w", err)
		}
	}
	if _, err := tx.Exec(`ALTER TABLE prediction_proposals ADD COLUMN cron_job_id TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add cron_job_id column: %w", err)
		}
	}
	return nil
}

func migrateEmbeddingColumns(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_ocr_frames ADD COLUMN embedding TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add embedding column: %w", err)
		}
	}
	if _, err := tx.Exec(`ALTER TABLE activity_ocr_frames ADD COLUMN embed_hash TEXT`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add embed_hash column: %w", err)
		}
	}
	return nil
}

func migrateActivityPipelineRuns(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_pipeline_runs (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			kind              TEXT NOT NULL,
			started_at        INTEGER NOT NULL,
			completed_at      INTEGER NOT NULL,
			pending_count     INTEGER,
			group_count       INTEGER,
			filtered_count    INTEGER,
			candidate_count   INTEGER,
			group_id          TEXT,
			session_ids       TEXT,
			app_names         TEXT,
			event_count       INTEGER,
			duration_ms       INTEGER,
			prefilter_pass    INTEGER,
			prefilter_reason  TEXT,
			ai_summary        TEXT,
			ai_response_raw   TEXT,
			worth_memory      INTEGER,
			worth_knowledge   INTEGER,
			worth_automation  INTEGER,
			triage_status     TEXT,
			triage_reason     TEXT,
			automation_status TEXT,
			automation_reason TEXT,
			automation_job_id TEXT,
			memory_status     TEXT,
			memory_reason     TEXT,
			knowledge_status  TEXT,
			knowledge_reason  TEXT,
			error             TEXT
		)
	`); err != nil {
		return fmt.Errorf("create activity_pipeline_runs: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_pipeline_runs_started ON activity_pipeline_runs(started_at DESC)`); err != nil {
		return fmt.Errorf("create activity_pipeline_runs index: %w", err)
	}
	return nil
}

func migrateMeetingColumns(tx *sql.Tx) error {
	for _, col := range []struct {
		name string
		def  string
	}{
		{"is_meeting", "INTEGER NOT NULL DEFAULT 0"},
		{"meeting_app", "TEXT NOT NULL DEFAULT ''"},
		{"transcript_path", "TEXT NOT NULL DEFAULT ''"},
	} {
		if _, err := tx.Exec(`ALTER TABLE activity_sessions ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("add %s column: %w", col.name, err)
			}
		}
	}
	return nil
}

func migrateAudioColumns(tx *sql.Tx) error {
	for _, col := range []struct {
		name string
		def  string
	}{
		{"audio_path", "TEXT NOT NULL DEFAULT ''"},
		{"audio_size_bytes", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if _, err := tx.Exec(`ALTER TABLE activity_sessions ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("add %s column: %w", col.name, err)
			}
		}
	}
	return nil
}

func migrateAXTextColumn(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_snapshots ADD COLUMN ax_text TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add ax_text column: %w", err)
		}
	}
	return nil
}

func migrateBrowserURLColumn(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_snapshots ADD COLUMN browser_url TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add browser_url column: %w", err)
		}
	}
	return nil
}

// migrateSnapshotParsedColumns adds the structured parser output columns
// to activity_snapshots so app-aware filters and full-fidelity recall
// don't have to LIKE-search ax_text.
//
//   - content_type: classification from ParsedContent.ContentType
//     ("conversation", "document", "task_list", "events", "terminal",
//     "web_page", or empty when no parser matched). Indexed for fast
//     filtering ("show me all conversations from yesterday").
//   - app: short app key from ParsedContent.Metadata["app"] ("slack",
//     "notion", "gmail", …). Indexed for per-tool retrieval that bypasses
//     the noisier bundle_id matching on activity_sessions.app_names.
//   - parsed_json: full ParsedContent serialized as JSON. Lossless storage
//     for Title / Channel / Messages / URL / Metadata so future features
//     (per-message vector search, structured snapshot detail UI) can read
//     them without re-parsing the AX tree.
func migrateSnapshotParsedColumns(tx *sql.Tx) error {
	for _, col := range []struct{ name, def string }{
		{"content_type", "TEXT NOT NULL DEFAULT ''"},
		{"app", "TEXT NOT NULL DEFAULT ''"},
		{"parsed_json", "TEXT NOT NULL DEFAULT ''"},
	} {
		if _, err := tx.Exec(`ALTER TABLE activity_snapshots ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("add %s column: %w", col.name, err)
			}
		}
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_snapshots_content_type ON activity_snapshots(content_type)`); err != nil {
		return fmt.Errorf("create content_type index: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_snapshots_app ON activity_snapshots(app)`); err != nil {
		return fmt.Errorf("create app index: %w", err)
	}
	return nil
}

// migrateFocusRectColumns adds the normalized focused-window bounds captured
// at the same instant as the JPEG. Used by the OCR worker to crop pixels to
// the focused window before recognition, so unrelated background apps and
// in-window distractions don't leak into the triage prompt. Stored as virtual-
// desktop-normalized REAL in [0,1] so the rect survives the
// capture-resolution-vs-OCR-resolution mismatch. Four-zero means "unknown" —
// callers must fall back to the full frame (legacy snapshots, multi-display
// edge cases, non-darwin platforms).
func migrateFocusRectColumns(tx *sql.Tx) error {
	for _, col := range []struct{ name, def string }{
		{"focus_rect_x", "REAL NOT NULL DEFAULT 0"},
		{"focus_rect_y", "REAL NOT NULL DEFAULT 0"},
		{"focus_rect_w", "REAL NOT NULL DEFAULT 0"},
		{"focus_rect_h", "REAL NOT NULL DEFAULT 0"},
	} {
		if _, err := tx.Exec(`ALTER TABLE activity_snapshots ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("add %s column: %w", col.name, err)
			}
		}
	}
	return nil
}

// migrateOCRUsedFocusRectColumn flips on once the OCR worker successfully
// cropped to the focused-window rect. Without this column the audit UI can
// only tell that a focus rect was known at capture time; not whether the
// OCR engine actually scoped its input to that rect (the cropper can fall
// back to the full frame when the rect is degenerate or the JPEG fails to
// decode). Distinguishing the two is the whole point of this surface, so it
// gets its own column rather than overloading focus_rect_*.
func migrateOCRUsedFocusRectColumn(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_snapshots ADD COLUMN ocr_used_focus_rect INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add ocr_used_focus_rect column: %w", err)
		}
	}
	return nil
}

// migrateActivitySummariesMoments adds a column to persist the AI-grouped
// Moments payload alongside the narrative daily summary. Stored as JSON since
// the list is only read on the Summary view and has no query surface.
func migrateActivitySummariesMoments(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_summaries ADD COLUMN moments_json TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add moments_json column: %w", err)
		}
	}
	return nil
}

// migrateActivitySummariesMomentsPartial gives Moments an independent partial
// flag so checkPendingSummaries can detect rows where the topic summary
// finished but the Moments agent silently failed. Without this, persistMoments
// returning empty leaves the prior partial moments_json in place while
// is_partial flips to 0 — and the row never gets retried again.
//
// This first pass leaves backfill to migrateActivitySummariesMomentsPartialReset
// below, which forces every daily row to moments_partial=1. The schema can't
// tell whether existing moments_json belongs to the same generation as the
// stored topic, so the only correct legacy stance is "assume stale" and let
// the next checkPendingSummaries pass regenerate within the 7-day window.
func migrateActivitySummariesMomentsPartial(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_summaries ADD COLUMN moments_partial INTEGER NOT NULL DEFAULT 1`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add moments_partial column: %w", err)
		}
	}
	return nil
}

// migrateActivitySummariesMomentsPartialReset force-marks every daily row's
// moments_partial=1 so the first checkPendingSummaries pass after upgrade
// re-evaluates each within-window day, including legacy "is_partial=0 +
// stale non-empty moments_json" rows produced by older clients that never
// cleared moments after a Moments-only failure. Costs at most 7 LLM calls
// on first launch (the 7-day pending scan window).
func migrateActivitySummariesMomentsPartialReset(tx *sql.Tx) error {
	if _, err := tx.Exec(`UPDATE activity_summaries SET moments_partial = 1 WHERE type = 'daily'`); err != nil {
		return fmt.Errorf("force-reset moments_partial: %w", err)
	}
	return nil
}

// migrateActivitySummariesMomentsGeneration adds a monotonic counter that
// SaveSummary bumps on every upsert. Late-arriving persistMoments goroutines
// from a previous attempt can then be detected and discarded: SaveMoments
// only writes when the row's current generation matches the captured value,
// so a stalled attempt 1 cannot poison attempt 2's wait-for-moments check.
//
// Default 0 is fine for legacy rows: the next SaveSummary on a row bumps it
// to 1, and any in-flight pre-upgrade goroutine has already lost its window
// (process restart kills it).
func migrateActivitySummariesMomentsGeneration(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_summaries ADD COLUMN moments_generation INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add moments_generation column: %w", err)
		}
	}
	return nil
}

// migrateActivitySummariesLanguage records which UI language a cached summary
// was generated for. Legacy rows keep the empty default: English UI may reuse
// them, while non-English UI treats them as stale and regenerates.
func migrateActivitySummariesLanguage(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_summaries ADD COLUMN language TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add language column: %w", err)
		}
	}
	return nil
}

// migrateActivitySummariesLanguageKey makes language part of the cache key.
// A separate language column is not enough: the old PRIMARY KEY(date, type)
// would make a Chinese regeneration overwrite the English cache for the same
// day/week. Rebuilding the table lets both languages coexist while preserving
// legacy rows with empty language.
func migrateActivitySummariesLanguageKey(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS activity_summaries_new (
			date               TEXT NOT NULL,
			type               TEXT NOT NULL,
			language           TEXT NOT NULL DEFAULT '',
			summary            TEXT NOT NULL,
			stats_json         TEXT NOT NULL DEFAULT '{}',
			model              TEXT,
			is_partial         INTEGER NOT NULL DEFAULT 0,
			created_at         INTEGER NOT NULL,
			moments_json       TEXT NOT NULL DEFAULT '',
			moments_partial    INTEGER NOT NULL DEFAULT 1,
			moments_generation INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (date, type, language)
		)
	`); err != nil {
		return fmt.Errorf("create activity_summaries_new: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO activity_summaries_new (
			date, type, language, summary, stats_json, model, is_partial,
			created_at, moments_json, moments_partial, moments_generation
		)
		SELECT
			date,
			type,
			COALESCE(language, ''),
			summary,
			stats_json,
			model,
			is_partial,
			created_at,
			moments_json,
			moments_partial,
			moments_generation
		FROM activity_summaries
	`); err != nil {
		return fmt.Errorf("copy activity_summaries into language-key table: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE activity_summaries`); err != nil {
		return fmt.Errorf("drop old activity_summaries: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE activity_summaries_new RENAME TO activity_summaries`); err != nil {
		return fmt.Errorf("rename activity_summaries_new: %w", err)
	}
	return nil
}

// migrateHandoffStatus adds the handoff_status column to activity_sessions so
// the triager can guard against re-proposing a handoff for the same
// activity group on retry. Mirrors the automation_status pattern.
func migrateHandoffStatus(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_sessions ADD COLUMN handoff_status TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add handoff_status: %w", err)
		}
	}
	return nil
}

// migrateSOPStatus adds the sop_status column to activity_sessions so the
// triager can guard against re-dispatching SOP authoring for the same
// activity group on retry. Mirrors the handoff_status pattern.
func migrateSOPStatus(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_sessions ADD COLUMN sop_status TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add sop_status: %w", err)
		}
	}
	return nil
}

// migrateHandoffPromptColumns adds the handoff_prompt and handoff_proposal_id
// columns to prediction_proposals. handoff_prompt persists the agent prompt
// between proposal time and user accept (which may happen hours later).
// handoff_proposal_id mirrors cron_job_id but is reserved for future use when
// we may want to chain handoff proposals (none today; kept for symmetry).
func migrateHandoffPromptColumns(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE prediction_proposals ADD COLUMN handoff_prompt TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add handoff_prompt: %w", err)
		}
	}
	if _, err := tx.Exec(`ALTER TABLE prediction_proposals ADD COLUMN handoff_proposal_id TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add handoff_proposal_id: %w", err)
		}
	}
	return nil
}

// migrateHandoffPipelineColumns adds handoff observability columns to
// activity_pipeline_runs so the Settings UI can show handoff decisions and
// outcomes alongside triage/automation/memory/knowledge.
func migrateHandoffPipelineColumns(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_pipeline_runs ADD COLUMN worth_handoff INTEGER`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add worth_handoff: %w", err)
		}
	}
	if _, err := tx.Exec(`ALTER TABLE activity_pipeline_runs ADD COLUMN handoff_status TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add handoff_status: %w", err)
		}
	}
	if _, err := tx.Exec(`ALTER TABLE activity_pipeline_runs ADD COLUMN handoff_reason TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add handoff_reason: %w", err)
		}
	}
	if _, err := tx.Exec(`ALTER TABLE activity_pipeline_runs ADD COLUMN handoff_proposal_id TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add handoff_proposal_id: %w", err)
		}
	}
	return nil
}

// migratePipelineRunSource adds a `source` column to activity_pipeline_runs so
// the dev view can distinguish/filter inbox-origin runs ("slack"/"discord"/
// "telegram") from screen-observed "activity" runs.
func migratePipelineRunSource(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_pipeline_runs ADD COLUMN source TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add source: %w", err)
		}
	}
	return nil
}

// migrateTimelineSummaryImageFailureRequeue re-queues summary generation for
// runs parked as failed_permanent by the 2026-07 image-rejection incident: a
// gateway routing change sent screenshot-bearing summary requests to a
// text-only upstream, so every such run burned its attempts on the same HTTP
// 400 and was parked. The worker now degrades those calls to text-only, so
// one retry round recovers them. One-shot and bounded — future genuine
// permanent failures park exactly as before.
func migrateTimelineSummaryImageFailureRequeue(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		UPDATE activity_timeline_groups
		SET summary_status = 'failed_transient', summary_attempts = 0, next_retry_at = 0
		WHERE summary_status = 'failed_permanent'`); err != nil {
		return fmt.Errorf("requeue failed_permanent timeline summaries: %w", err)
	}
	return nil
}

// migratePipelineSelectedFrames adds a JSON column to activity_pipeline_runs
// that records the exact frames (parsed snapshots + OCR samples) the AI triage
// prompt was built from, so the Settings UI can show operators which frames
// drove each decision instead of only the rendered text.
func migratePipelineSelectedFrames(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_pipeline_runs ADD COLUMN selected_frames TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add selected_frames: %w", err)
		}
	}
	return nil
}

// migrateTriagePromptColumns records the exact triage prompt sent to the LLM
// alongside the per-feature flag snapshot, so the Settings UI can diagnose
// why a group landed where it did and whether the user's feature toggles
// actually trimmed the prompt.
func migrateTriagePromptColumns(tx *sql.Tx) error {
	for _, col := range []struct {
		name string
		def  string
	}{
		{"triage_flags", "TEXT NOT NULL DEFAULT ''"},
		{"triage_prompt_bytes", "INTEGER NOT NULL DEFAULT 0"},
		{"triage_prompt", "TEXT NOT NULL DEFAULT ''"},
	} {
		if _, err := tx.Exec(`ALTER TABLE activity_pipeline_runs ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("add %s column: %w", col.name, err)
			}
		}
	}
	return nil
}

// migrateRenameCrystalToTriage renames the leftover "crystal" columns from the
// removed crystal-apps feature to semantic "triage" names, drops the columns
// that are now always empty, and normalizes the dead "crystallized" status
// value to "dismissed".
//
//   - activity_sessions: crystal_status → triage_status; drop crystal_session_id
//   - activity_pipeline_runs: crystal_status → triage_status,
//     crystal_reason → triage_reason; drop worth_crystal
//   - prediction_proposals: drop crystal_session_id
//
// All operations are guarded with the existing "no such column" / "duplicate
// column" idempotency so a re-run (or a fresh DB that already created the new
// schema in migrateCoreTables) is safe. The bundled modernc.org/sqlite driver
// supports ALTER TABLE … RENAME COLUMN / DROP COLUMN (SQLite ≥ 3.35).
func migrateRenameCrystalToTriage(tx *sql.Tx) error {
	renames := []struct {
		table, from, to string
	}{
		{"activity_sessions", "crystal_status", "triage_status"},
		{"activity_pipeline_runs", "crystal_status", "triage_status"},
		{"activity_pipeline_runs", "crystal_reason", "triage_reason"},
	}
	for _, r := range renames {
		stmt := fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", r.table, r.from, r.to)
		if _, err := tx.Exec(stmt); err != nil {
			msg := err.Error()
			// "no such column" — the old column is already gone (re-run, or a
			// fresh DB created the new schema directly).
			// "duplicate column" — the new column already exists.
			if !strings.Contains(msg, "no such column") && !strings.Contains(msg, "duplicate column") {
				return fmt.Errorf("rename %s.%s -> %s: %w", r.table, r.from, r.to, err)
			}
		}
	}

	drops := []struct {
		table, column string
	}{
		{"activity_sessions", "crystal_session_id"},
		{"prediction_proposals", "crystal_session_id"},
		{"activity_pipeline_runs", "worth_crystal"},
	}
	for _, d := range drops {
		stmt := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", d.table, d.column)
		if _, err := tx.Exec(stmt); err != nil {
			if !strings.Contains(err.Error(), "no such column") {
				return fmt.Errorf("drop %s.%s: %w", d.table, d.column, err)
			}
		}
	}

	// Normalize the dead "crystallized" status value to "dismissed". Nothing
	// produces "crystallized" anymore, but legacy rows may still carry it.
	if _, err := tx.Exec(`UPDATE activity_sessions SET triage_status = 'dismissed' WHERE triage_status = 'crystallized'`); err != nil {
		return fmt.Errorf("backfill activity_sessions triage_status: %w", err)
	}
	if _, err := tx.Exec(`UPDATE activity_pipeline_runs SET triage_status = 'dismissed' WHERE triage_status = 'crystallized'`); err != nil {
		return fmt.Errorf("backfill activity_pipeline_runs triage_status: %w", err)
	}

	return nil
}

// migrateMeetingGroupIDColumn adds the column that lets us group every
// activity_sessions row from a single ongoing call under one identifier.
// The meeting lifecycle watcher already maintains an in-memory inMeeting
// state machine (false→true on MEETING_STARTED, true→false on MEETING_ENDED);
// we generate a UUID on the false→true edge and stamp every periodic_flush
// slice with it until MEETING_ENDED resets it. Recent meeting fusion can
// then group by meeting_group_id instead of using fragile time gaps.
//
// Existing rows backfill to ” (empty); LoadRecentMeetingEvents treats empty
// values as singleton groups so historical sessions render as one event each
// — no false fusion of pre-migration data.
func migrateMeetingGroupIDColumn(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_sessions ADD COLUMN meeting_group_id TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add meeting_group_id column: %w", err)
		}
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_sessions_meeting_group_id ON activity_sessions(meeting_group_id) WHERE meeting_group_id != ''`); err != nil {
		return fmt.Errorf("create meeting_group_id index: %w", err)
	}
	return nil
}

// migrateInitiatives introduces the proactive-agent source of truth. The
// historical prediction tables are intentionally retained as read-only
// migration input so existing local installations do not lose provenance.
func migrateInitiatives(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS initiatives (
			id                  TEXT PRIMARY KEY,
			kind                TEXT NOT NULL,
			status              TEXT NOT NULL,
			autonomy_mode       TEXT NOT NULL DEFAULT 'shadow',
			source_kind         TEXT NOT NULL,
			source_id           TEXT NOT NULL,
			title               TEXT NOT NULL,
			description         TEXT NOT NULL DEFAULT '',
			goal                TEXT NOT NULL DEFAULT '',
			user_intent         TEXT NOT NULL DEFAULT '',
			predicted_action    TEXT NOT NULL DEFAULT '',
			rationale           TEXT NOT NULL DEFAULT '',
			confidence          REAL NOT NULL DEFAULT 0.0,
			risk_level          TEXT NOT NULL DEFAULT 'unknown',
			reversibility       TEXT NOT NULL DEFAULT 'unknown',
			evidence_json       TEXT NOT NULL DEFAULT '[]',
			execution_plan_json TEXT NOT NULL DEFAULT '{}',
			fingerprint         TEXT NOT NULL DEFAULT '',
			execution_ref       TEXT NOT NULL DEFAULT '',
			chat_session_id     TEXT NOT NULL DEFAULT '',
			memory_state        TEXT NOT NULL DEFAULT 'unverified',
			created_at          INTEGER NOT NULL,
			updated_at          INTEGER NOT NULL,
			resolved_at         INTEGER
		);

		CREATE INDEX IF NOT EXISTS idx_initiatives_status
			ON initiatives(status, updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_initiatives_source
			ON initiatives(source_kind, source_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_initiatives_fingerprint
			ON initiatives(fingerprint) WHERE fingerprint != '';
		CREATE INDEX IF NOT EXISTS idx_initiatives_execution_ref
			ON initiatives(execution_ref) WHERE execution_ref != '';

		CREATE TABLE IF NOT EXISTS initiative_feedback (
			id               TEXT PRIMARY KEY,
			initiative_id    TEXT NOT NULL REFERENCES initiatives(id) ON DELETE CASCADE,
			decision         TEXT NOT NULL,
			corrected_action TEXT NOT NULL DEFAULT '',
			reason           TEXT NOT NULL DEFAULT '',
			created_at       INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_initiative_feedback_initiative
			ON initiative_feedback(initiative_id, created_at DESC);

		CREATE TABLE IF NOT EXISTS initiative_artifact_links (
			initiative_id TEXT NOT NULL REFERENCES initiatives(id) ON DELETE CASCADE,
			artifact_kind TEXT NOT NULL,
			artifact_id   TEXT NOT NULL,
			relation      TEXT NOT NULL,
			created_at    INTEGER NOT NULL,
			PRIMARY KEY (initiative_id, artifact_kind, artifact_id, relation)
		);
		CREATE INDEX IF NOT EXISTS idx_initiative_artifacts_artifact
			ON initiative_artifact_links(artifact_kind, artifact_id);

		CREATE TABLE IF NOT EXISTS initiative_observations (
			initiative_id TEXT NOT NULL REFERENCES initiatives(id) ON DELETE CASCADE,
			source_kind   TEXT NOT NULL,
			source_id     TEXT NOT NULL,
			evidence_json TEXT NOT NULL DEFAULT '[]',
			confidence    REAL NOT NULL DEFAULT 0.0,
			observed_at   INTEGER NOT NULL,
			PRIMARY KEY (initiative_id, source_kind, source_id)
		);
		CREATE INDEX IF NOT EXISTS idx_initiative_observations_source
			ON initiative_observations(source_kind, source_id, observed_at DESC);
	`); err != nil {
		return fmt.Errorf("create initiative tables: %w", err)
	}

	if _, err := tx.Exec(`ALTER TABLE activity_pipeline_runs ADD COLUMN initiative_id TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add activity pipeline initiative_id: %w", err)
		}
	}
	if err := backfillInitiativeObservations(tx); err != nil {
		return err
	}

	var hasLegacy int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='prediction_proposals'`).Scan(&hasLegacy); err != nil {
		return fmt.Errorf("check legacy prediction table: %w", err)
	}
	if hasLegacy == 0 {
		return nil
	}

	// Legacy accepted rows already own a cron job, so they migrate as approved
	// rather than shadowed. Pending predictions become proposed. New inference
	// never writes the legacy table.
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO initiatives (
			id, kind, status, autonomy_mode, source_kind, source_id,
			title, description, goal, user_intent, predicted_action, rationale,
			confidence, risk_level, reversibility, evidence_json, execution_plan_json,
			fingerprint, execution_ref, chat_session_id, memory_state,
			created_at, updated_at, resolved_at
		)
		SELECT
			id,
			CASE WHEN COALESCE(kind, '') IN ('automation', 'handoff') THEN kind ELSE 'automation' END,
			CASE status
				WHEN 'accepted' THEN 'approved'
				WHEN 'accepting' THEN 'proposed'
				WHEN 'dismissed' THEN 'dismissed'
				ELSE 'proposed'
			END,
			CASE WHEN status = 'accepted' THEN 'approval' ELSE 'shadow' END,
			source_kind, source_id,
			CASE WHEN app_name != '' THEN app_name ELSE description END,
			description, user_intent, user_intent,
			CASE WHEN predicted_next != '' THEN predicted_next ELSE tool_proposal END,
			tool_proposal, confidence, 'unknown', 'unknown', '[]', '{}', '',
			COALESCE(cron_job_id, ''), COALESCE(chat_session_id, ''), 'legacy',
			created_at, COALESCE(resolved_at, created_at), resolved_at
		FROM prediction_proposals
	`); err != nil {
		return fmt.Errorf("migrate legacy predictions to initiatives: %w", err)
	}
	return backfillInitiativeObservations(tx)
}

func migrateInitiativeObservations(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS initiative_observations (
			initiative_id TEXT NOT NULL REFERENCES initiatives(id) ON DELETE CASCADE,
			source_kind   TEXT NOT NULL,
			source_id     TEXT NOT NULL,
			evidence_json TEXT NOT NULL DEFAULT '[]',
			confidence    REAL NOT NULL DEFAULT 0.0,
			observed_at   INTEGER NOT NULL,
			PRIMARY KEY (initiative_id, source_kind, source_id)
		);
		CREATE INDEX IF NOT EXISTS idx_initiative_observations_source
			ON initiative_observations(source_kind, source_id, observed_at DESC);
	`); err != nil {
		return fmt.Errorf("create initiative observations: %w", err)
	}
	return backfillInitiativeObservations(tx)
}

func backfillInitiativeObservations(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO initiative_observations (
			initiative_id, source_kind, source_id, evidence_json, confidence, observed_at
		)
		SELECT id, source_kind, source_id, evidence_json, confidence, created_at
		FROM initiatives
		WHERE source_kind != '' AND source_id != ''
	`); err != nil {
		return fmt.Errorf("backfill initiative observations: %w", err)
	}
	return nil
}

func migrateInitiativeLearning(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS initiative_outcomes (
			id                TEXT PRIMARY KEY,
			initiative_id     TEXT NOT NULL REFERENCES initiatives(id) ON DELETE CASCADE,
			source_kind       TEXT NOT NULL,
			source_id         TEXT NOT NULL,
			observed_action   TEXT NOT NULL,
			observed_result   TEXT NOT NULL DEFAULT '',
			assessment        TEXT NOT NULL,
			assessment_reason TEXT NOT NULL DEFAULT '',
			confidence        REAL NOT NULL DEFAULT 0.0,
			evidence_json     TEXT NOT NULL DEFAULT '[]',
			created_at        INTEGER NOT NULL,
			UNIQUE (initiative_id, source_kind, source_id)
		);
		CREATE INDEX IF NOT EXISTS idx_initiative_outcomes_initiative
			ON initiative_outcomes(initiative_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_initiative_outcomes_source
			ON initiative_outcomes(source_kind, source_id, created_at DESC);

		CREATE TABLE IF NOT EXISTS initiative_action_receipts (
			id                TEXT PRIMARY KEY,
			initiative_id     TEXT NOT NULL REFERENCES initiatives(id) ON DELETE CASCADE,
			execution_ref     TEXT NOT NULL,
			run_id            TEXT NOT NULL UNIQUE,
			status            TEXT NOT NULL,
			trigger_kind      TEXT NOT NULL DEFAULT '',
			started_at        INTEGER NOT NULL,
			finished_at       INTEGER,
			duration_ms       INTEGER NOT NULL DEFAULT 0,
			result_summary    TEXT NOT NULL DEFAULT '',
			error             TEXT NOT NULL DEFAULT '',
			session_tag       TEXT NOT NULL DEFAULT '',
			item_sort_start   INTEGER NOT NULL DEFAULT -1,
			artifacts_json    TEXT NOT NULL DEFAULT '[]',
			side_effects_json TEXT NOT NULL DEFAULT '[]',
			created_at        INTEGER NOT NULL,
			updated_at        INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_initiative_receipts_initiative
			ON initiative_action_receipts(initiative_id, started_at DESC);
		CREATE INDEX IF NOT EXISTS idx_initiative_receipts_execution
			ON initiative_action_receipts(execution_ref, started_at DESC);
	`); err != nil {
		return fmt.Errorf("create initiative learning tables: %w", err)
	}
	return nil
}

func migrateInitiativeDetailHistory(tx *sql.Tx) error {
	for _, statement := range []string{
		`ALTER TABLE initiative_observations ADD COLUMN predicted_action TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE initiative_observations ADD COLUMN rationale TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.Exec(statement); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add initiative observation history column: %w", err)
		}
	}
	return nil
}

func migrateInitiativeCalibration(tx *sql.Tx) error {
	if _, err := tx.Exec(
		`ALTER TABLE initiative_outcomes ADD COLUMN prediction_confidence REAL`,
	); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return fmt.Errorf("add initiative calibration confidence snapshot: %w", err)
	}
	return nil
}

func migrateInitiativeConfidenceAdjustment(tx *sql.Tx) error {
	for _, statement := range []string{
		`ALTER TABLE initiatives ADD COLUMN raw_model_confidence REAL`,
		`ALTER TABLE initiatives ADD COLUMN adjusted_confidence REAL`,
		`ALTER TABLE initiatives ADD COLUMN confidence_adjustment_json TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE initiative_observations ADD COLUMN raw_model_confidence REAL`,
		`ALTER TABLE initiative_observations ADD COLUMN adjusted_confidence REAL`,
		`ALTER TABLE initiative_observations ADD COLUMN confidence_adjustment_json TEXT NOT NULL DEFAULT '{}'`,
	} {
		if _, err := tx.Exec(statement); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("add initiative confidence adjustment column: %w", err)
		}
	}
	return nil
}

func migrateAuthorityDecisions(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS authority_decisions (
			id              TEXT PRIMARY KEY,
			policy_version  TEXT NOT NULL,
			run_id          TEXT NOT NULL DEFAULT '',
			session_id      TEXT NOT NULL DEFAULT '',
			initiative_id   TEXT NOT NULL DEFAULT '',
			tool_use_id     TEXT NOT NULL DEFAULT '',
			tool_name       TEXT NOT NULL DEFAULT '',
			operation       TEXT NOT NULL DEFAULT '',
			capability      TEXT NOT NULL,
			effect          TEXT NOT NULL,
			resource        TEXT NOT NULL DEFAULT '',
			recipient       TEXT NOT NULL DEFAULT '',
			reversible      INTEGER NOT NULL DEFAULT 0,
			unattended      INTEGER NOT NULL DEFAULT 0,
			decision        TEXT NOT NULL,
			reason          TEXT NOT NULL,
			grant_id        TEXT NOT NULL DEFAULT '',
			evaluation_json TEXT NOT NULL,
			created_at      INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_authority_decisions_run
			ON authority_decisions(run_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_authority_decisions_initiative
			ON authority_decisions(initiative_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_authority_decisions_capability
			ON authority_decisions(capability, created_at DESC);
	`); err != nil {
		return fmt.Errorf("create authority decision ledger: %w", err)
	}
	return nil
}

// migrateShadowRuns adds the immutable prediction-time ledger used by true
// shadow deployment. Historical Initiative rows are intentionally not
// backfilled because their context and prediction changed over time.
func migrateShadowRuns(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS shadow_runs (
			id                        TEXT PRIMARY KEY,
			decision_key              TEXT NOT NULL UNIQUE,
			capability_key            TEXT NOT NULL,
			source_kind               TEXT NOT NULL,
			source_id                 TEXT NOT NULL,
			context_hash              TEXT NOT NULL,
			context_snapshot_json     TEXT NOT NULL,
			prediction_hash           TEXT NOT NULL,
			prediction_json           TEXT NOT NULL,
			model                     TEXT NOT NULL DEFAULT '',
			prompt_version            TEXT NOT NULL,
			runner_version            TEXT NOT NULL DEFAULT '',
			observation_horizon_start INTEGER NOT NULL,
			observation_horizon_end   INTEGER NOT NULL,
			created_at                INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_runs_capability
			ON shadow_runs(capability_key, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_shadow_runs_source
			ON shadow_runs(source_kind, source_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_shadow_runs_pending
			ON shadow_runs(observation_horizon_end, created_at DESC);

		CREATE TABLE IF NOT EXISTS shadow_run_evidence (
			id           TEXT PRIMARY KEY,
			shadow_run_id TEXT NOT NULL REFERENCES shadow_runs(id) ON DELETE CASCADE,
			role         TEXT NOT NULL,
			source_kind  TEXT NOT NULL,
			source_id    TEXT NOT NULL,
			content_hash TEXT NOT NULL DEFAULT '',
			summary      TEXT NOT NULL DEFAULT '',
			observed_at  INTEGER NOT NULL DEFAULT 0,
			created_at   INTEGER NOT NULL,
			UNIQUE (shadow_run_id, role, source_kind, source_id, content_hash)
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_evidence_run
			ON shadow_run_evidence(shadow_run_id, created_at ASC);
		CREATE INDEX IF NOT EXISTS idx_shadow_evidence_source
			ON shadow_run_evidence(source_kind, source_id, observed_at DESC);

		CREATE TABLE IF NOT EXISTS shadow_run_observations (
			id                      TEXT PRIMARY KEY,
			shadow_run_id           TEXT NOT NULL REFERENCES shadow_runs(id) ON DELETE CASCADE,
			source_kind             TEXT NOT NULL,
			source_id               TEXT NOT NULL,
			observed_action         TEXT NOT NULL,
			observed_result         TEXT NOT NULL DEFAULT '',
			reasoning_evidence_json TEXT NOT NULL DEFAULT '[]',
			evidence_json           TEXT NOT NULL DEFAULT '[]',
			observed_at             INTEGER NOT NULL,
			created_at              INTEGER NOT NULL,
			UNIQUE (shadow_run_id, source_kind, source_id)
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_observations_run
			ON shadow_run_observations(shadow_run_id, observed_at ASC);
		CREATE INDEX IF NOT EXISTS idx_shadow_observations_source
			ON shadow_run_observations(source_kind, source_id, observed_at DESC);

		CREATE TABLE IF NOT EXISTS shadow_run_evaluations (
			id                   TEXT PRIMARY KEY,
			shadow_run_id        TEXT NOT NULL REFERENCES shadow_runs(id) ON DELETE CASCADE,
			observation_id       TEXT NOT NULL REFERENCES shadow_run_observations(id) ON DELETE CASCADE,
			idempotency_key      TEXT NOT NULL UNIQUE,
			comparison_version   TEXT NOT NULL,
			evaluator_model      TEXT NOT NULL DEFAULT '',
			action_assessment    TEXT NOT NULL,
			outcome_assessment   TEXT NOT NULL,
			timing_assessment    TEXT NOT NULL,
			reasoning_assessment TEXT NOT NULL,
			safety_assessment    TEXT NOT NULL,
			evaluation_json      TEXT NOT NULL,
			created_at           INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_evaluations_run
			ON shadow_run_evaluations(shadow_run_id, created_at ASC);
		CREATE INDEX IF NOT EXISTS idx_shadow_evaluations_observation
			ON shadow_run_evaluations(observation_id, created_at ASC);

		CREATE TRIGGER IF NOT EXISTS shadow_runs_no_update
		BEFORE UPDATE ON shadow_runs
		BEGIN
			SELECT RAISE(ABORT, 'shadow runs are immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS shadow_run_evidence_no_update
		BEFORE UPDATE ON shadow_run_evidence
		BEGIN
			SELECT RAISE(ABORT, 'shadow run evidence is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS shadow_run_observations_no_update
		BEFORE UPDATE ON shadow_run_observations
		BEGIN
			SELECT RAISE(ABORT, 'shadow run observations are immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS shadow_run_evaluations_no_update
		BEFORE UPDATE ON shadow_run_evaluations
		BEGIN
			SELECT RAISE(ABORT, 'shadow run evaluations are immutable');
		END;
	`); err != nil {
		return fmt.Errorf("create shadow run ledger: %w", err)
	}
	return nil
}

func migratePipelineShadowRunID(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE activity_pipeline_runs ADD COLUMN shadow_run_id TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("add activity pipeline shadow_run_id: %w", err)
		}
	}
	return nil
}

// migrateShadowCorrelationAttempts adds a bounded, append-only observability
// ledger. Unlike shadow_run_evaluations, attempts also retain zero-match and
// error paths so developers can diagnose candidate selection and comparator
// behavior without changing production evidence.
func migrateShadowCorrelationAttempts(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS shadow_correlation_attempts (
			id               TEXT PRIMARY KEY,
			source_kind      TEXT NOT NULL,
			source_id        TEXT NOT NULL,
			observed_at      INTEGER NOT NULL,
			status           TEXT NOT NULL,
			candidate_count  INTEGER NOT NULL DEFAULT 0,
			selected_count   INTEGER NOT NULL DEFAULT 0,
			match_count      INTEGER NOT NULL DEFAULT 0,
			attempt_json     TEXT NOT NULL,
			created_at       INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_correlation_attempts_created
			ON shadow_correlation_attempts(created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_shadow_correlation_attempts_source
			ON shadow_correlation_attempts(source_kind, source_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_shadow_correlation_attempts_status
			ON shadow_correlation_attempts(status, created_at DESC);

		CREATE TRIGGER IF NOT EXISTS shadow_correlation_attempts_no_update
		BEFORE UPDATE ON shadow_correlation_attempts
		BEGIN
			SELECT RAISE(ABORT, 'shadow correlation attempts are immutable');
		END;
	`); err != nil {
		return fmt.Errorf("create shadow correlation attempt ledger: %w", err)
	}
	return nil
}

// migrateShadowCapabilityProfiles adds an append-only materialized projection
// over canonical ShadowRun evaluations. Snapshots are rebuildable and never
// participate in authority decisions.
func migrateShadowCapabilityProfiles(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS shadow_capability_profile_snapshots (
			id                         TEXT PRIMARY KEY,
			capability_key             TEXT NOT NULL,
			policy_version             TEXT NOT NULL,
			evidence_hash              TEXT NOT NULL,
			profile_json               TEXT NOT NULL,
			qualified_evaluation_count INTEGER NOT NULL,
			ready                      INTEGER NOT NULL DEFAULT 0,
			created_at                 INTEGER NOT NULL,
			UNIQUE (capability_key, policy_version, evidence_hash)
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_capability_profiles_latest
			ON shadow_capability_profile_snapshots(capability_key, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_shadow_capability_profiles_ready
			ON shadow_capability_profile_snapshots(ready, created_at DESC);
		CREATE TRIGGER IF NOT EXISTS shadow_capability_profiles_no_update
		BEFORE UPDATE ON shadow_capability_profile_snapshots
		BEGIN
			SELECT RAISE(ABORT, 'shadow capability profile snapshots are immutable');
		END;
	`); err != nil {
		return fmt.Errorf("create shadow capability profile snapshots: %w", err)
	}
	return nil
}

// migrateShadowExecutionRuntime adds immutable execution-attempt evidence and
// links normalized authority decisions back to the ShadowRun whose frozen
// candidate plan produced them.
func migrateShadowExecutionRuntime(tx *sql.Tx) error {
	for _, statement := range []string{
		`ALTER TABLE authority_decisions ADD COLUMN shadow_run_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE authority_decisions ADD COLUMN execution_mode TEXT NOT NULL DEFAULT 'normal'`,
	} {
		if _, err := tx.Exec(statement); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("add shadow authority column: %w", err)
		}
	}
	if _, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_authority_decisions_shadow_run
			ON authority_decisions(shadow_run_id, created_at ASC);

		CREATE TABLE IF NOT EXISTS shadow_execution_attempts (
			id                TEXT PRIMARY KEY,
			shadow_run_id     TEXT NOT NULL REFERENCES shadow_runs(id) ON DELETE CASCADE,
			idempotency_key   TEXT NOT NULL UNIQUE,
			runtime_version   TEXT NOT NULL,
			status            TEXT NOT NULL,
			policy_version    TEXT NOT NULL,
			agent_run_id      TEXT NOT NULL DEFAULT '',
			plan_hash         TEXT NOT NULL,
			attempt_json      TEXT NOT NULL,
			created_at        INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_execution_attempts_run
			ON shadow_execution_attempts(shadow_run_id, created_at ASC);
		CREATE INDEX IF NOT EXISTS idx_shadow_execution_attempts_status
			ON shadow_execution_attempts(status, created_at DESC);
		CREATE TRIGGER IF NOT EXISTS shadow_execution_attempts_no_update
		BEFORE UPDATE ON shadow_execution_attempts
		BEGIN
			SELECT RAISE(ABORT, 'shadow execution attempts are immutable');
		END;
	`); err != nil {
		return fmt.Errorf("create shadow execution runtime ledger: %w", err)
	}
	return nil
}

func migrateShadowCapabilityQualification(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS shadow_capability_qualifications (
			id                           TEXT PRIMARY KEY,
			qualification_key            TEXT NOT NULL UNIQUE,
			policy_version               TEXT NOT NULL,
			profile_id                   TEXT NOT NULL,
			profile_evidence_hash        TEXT NOT NULL,
			capability_key               TEXT NOT NULL,
			representative_shadow_run_id TEXT NOT NULL DEFAULT '',
			candidate_plan_hash          TEXT NOT NULL DEFAULT '',
			runtime_evidence_hash        TEXT NOT NULL,
			risk_tier                    TEXT NOT NULL,
			status                       TEXT NOT NULL,
			proposal_initiative_id       TEXT NOT NULL DEFAULT '',
			qualification_json           TEXT NOT NULL,
			created_at                   INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_shadow_qualifications_capability
			ON shadow_capability_qualifications(capability_key, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_shadow_qualifications_status
			ON shadow_capability_qualifications(status, created_at DESC);
		CREATE TRIGGER IF NOT EXISTS shadow_capability_qualifications_no_update
		BEFORE UPDATE ON shadow_capability_qualifications
		BEGIN
			SELECT RAISE(ABORT, 'shadow capability qualifications are immutable');
		END;
	`); err != nil {
		return fmt.Errorf("create shadow capability qualification ledger: %w", err)
	}
	return nil
}

// migrateCapabilityAuthority separates evidence-backed readiness from explicit
// user authority. Manifests and grants are immutable; revocation is represented
// by an append-only event so the complete decision history remains inspectable.
func migrateCapabilityAuthority(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS capability_manifests (
			id               TEXT PRIMARY KEY,
			initiative_id    TEXT NOT NULL REFERENCES initiatives(id) ON DELETE CASCADE,
			qualification_id TEXT NOT NULL REFERENCES shadow_capability_qualifications(id) ON DELETE RESTRICT,
			policy_version   TEXT NOT NULL,
			plan_hash        TEXT NOT NULL,
			manifest_json    TEXT NOT NULL,
			created_at       INTEGER NOT NULL,
			UNIQUE (initiative_id, qualification_id, policy_version, plan_hash)
		);
		CREATE INDEX IF NOT EXISTS idx_capability_manifests_initiative
			ON capability_manifests(initiative_id, created_at DESC);
		CREATE TRIGGER IF NOT EXISTS capability_manifests_no_update
		BEFORE UPDATE ON capability_manifests
		BEGIN
			SELECT RAISE(ABORT, 'capability manifests are immutable');
		END;

		CREATE TABLE IF NOT EXISTS authority_grants (
			id              TEXT PRIMARY KEY,
			initiative_id   TEXT NOT NULL REFERENCES initiatives(id) ON DELETE CASCADE,
			manifest_id     TEXT NOT NULL REFERENCES capability_manifests(id) ON DELETE RESTRICT,
			capability      TEXT NOT NULL,
			resource_prefix TEXT NOT NULL DEFAULT '',
			effect          TEXT NOT NULL,
			expires_at      INTEGER NOT NULL,
			created_at      INTEGER NOT NULL,
			UNIQUE (manifest_id, capability, resource_prefix)
		);
		CREATE INDEX IF NOT EXISTS idx_authority_grants_initiative
			ON authority_grants(initiative_id, expires_at DESC);
		CREATE TRIGGER IF NOT EXISTS authority_grants_no_update
		BEFORE UPDATE ON authority_grants
		BEGIN
			SELECT RAISE(ABORT, 'authority grants are immutable');
		END;

		CREATE TABLE IF NOT EXISTS authority_grant_events (
			id         TEXT PRIMARY KEY,
			grant_id   TEXT NOT NULL REFERENCES authority_grants(id) ON DELETE CASCADE,
			event_type TEXT NOT NULL,
			reason     TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			UNIQUE (grant_id, event_type)
		);
		CREATE INDEX IF NOT EXISTS idx_authority_grant_events_grant
			ON authority_grant_events(grant_id, created_at ASC);
		CREATE TRIGGER IF NOT EXISTS authority_grant_events_no_update
		BEFORE UPDATE ON authority_grant_events
		BEGIN
			SELECT RAISE(ABORT, 'authority grant events are immutable');
		END;
	`); err != nil {
		return fmt.Errorf("create capability authority state: %w", err)
	}
	return nil
}

func migrateTypedSelfModel(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS self_model_entries (
			id                  TEXT PRIMARY KEY,
			entry_key           TEXT NOT NULL,
			revision            INTEGER NOT NULL,
			kind                TEXT NOT NULL,
			scope               TEXT NOT NULL DEFAULT '',
			statement           TEXT NOT NULL,
			status              TEXT NOT NULL,
			confidence          REAL NOT NULL,
			reasoning_status    TEXT NOT NULL,
			evidence_json       TEXT NOT NULL,
			contradictions_json TEXT NOT NULL,
			source_hash         TEXT NOT NULL UNIQUE,
			created_at          INTEGER NOT NULL,
			UNIQUE (entry_key, revision)
		);
		CREATE INDEX IF NOT EXISTS idx_self_model_entries_latest
			ON self_model_entries(entry_key, revision DESC);
		CREATE INDEX IF NOT EXISTS idx_self_model_entries_kind
			ON self_model_entries(kind, status, created_at DESC);
		CREATE TRIGGER IF NOT EXISTS self_model_entries_no_update
		BEFORE UPDATE ON self_model_entries
		BEGIN
			SELECT RAISE(ABORT, 'self model entries are immutable');
		END;
	`); err != nil {
		return fmt.Errorf("create typed self model: %w", err)
	}
	return nil
}

// migrateTaskEpisodes adds the observed user-work boundary and context ledger.
// Task episodes are deliberately isolated from Initiatives, memory, and Self:
// later systems may consume evaluated episode evidence, but this migration has
// no projection or execution side effects.
func migrateTaskEpisodes(tx *sql.Tx) error {
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS task_episodes (
			id                  TEXT PRIMARY KEY,
			inferred_start_at   INTEGER NOT NULL,
			detected_at         INTEGER NOT NULL,
			create_payload_hash TEXT NOT NULL,
			create_payload_json TEXT NOT NULL,
			created_at          INTEGER NOT NULL,
			CHECK (inferred_start_at > 0),
			CHECK (detected_at >= inferred_start_at)
		);
		CREATE INDEX IF NOT EXISTS idx_task_episodes_timeline
			ON task_episodes(inferred_start_at DESC, detected_at DESC, id ASC);

		CREATE TABLE IF NOT EXISTS task_episode_context_snapshots (
			id             TEXT PRIMARY KEY,
			task_episode_id TEXT NOT NULL UNIQUE REFERENCES task_episodes(id) ON DELETE RESTRICT,
			cutoff_at      INTEGER NOT NULL,
			canonical_json TEXT NOT NULL,
			sha256         TEXT NOT NULL,
			created_at     INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_task_episode_context_cutoff
			ON task_episode_context_snapshots(cutoff_at DESC, task_episode_id);

		CREATE TABLE IF NOT EXISTS task_episode_hypotheses (
			id                        TEXT PRIMARY KEY,
			task_episode_id           TEXT NOT NULL REFERENCES task_episodes(id) ON DELETE RESTRICT,
			revision                  INTEGER NOT NULL,
			task_hypothesis           TEXT NOT NULL,
			desired_result_hypothesis TEXT NOT NULL,
			task_confidence           REAL NOT NULL,
			boundary_confidence       REAL NOT NULL,
			desired_result_confidence REAL NOT NULL,
			alternatives_json         TEXT NOT NULL,
			evidence_ids_json         TEXT NOT NULL,
			profile_influence_json     TEXT NOT NULL DEFAULT '{}',
			reasoning_hypotheses_json  TEXT NOT NULL DEFAULT '[]',
			created_at                INTEGER NOT NULL,
			UNIQUE (task_episode_id, revision),
			CHECK (revision > 0),
			CHECK (task_confidence >= 0 AND task_confidence <= 1),
			CHECK (boundary_confidence >= 0 AND boundary_confidence <= 1),
			CHECK (desired_result_confidence >= 0 AND desired_result_confidence <= 1)
		);
		CREATE INDEX IF NOT EXISTS idx_task_episode_hypotheses_episode
			ON task_episode_hypotheses(task_episode_id, revision ASC);

		CREATE TABLE IF NOT EXISTS task_episode_evidence (
			id              TEXT PRIMARY KEY,
			task_episode_id TEXT NOT NULL REFERENCES task_episodes(id) ON DELETE RESTRICT,
			role            TEXT NOT NULL CHECK (
				role IN ('onset', 'context', 'task', 'desired_result', 'action',
					'result', 'reasoning', 'contradiction')
			),
			source_kind     TEXT NOT NULL,
			source_id       TEXT NOT NULL,
			summary         TEXT NOT NULL DEFAULT '',
			payload_json    TEXT NOT NULL DEFAULT '{}',
			content_hash    TEXT NOT NULL,
			observed_at     INTEGER NOT NULL,
			created_at      INTEGER NOT NULL,
			UNIQUE (
				task_episode_id, role, source_kind, source_id,
				content_hash, observed_at
			)
		);
		CREATE INDEX IF NOT EXISTS idx_task_episode_evidence_episode
			ON task_episode_evidence(task_episode_id, observed_at ASC, created_at ASC);
		CREATE INDEX IF NOT EXISTS idx_task_episode_evidence_source
			ON task_episode_evidence(source_kind, source_id, observed_at DESC);

		CREATE TABLE IF NOT EXISTS task_episode_transitions (
			id              TEXT PRIMARY KEY,
			task_episode_id TEXT NOT NULL REFERENCES task_episodes(id) ON DELETE RESTRICT,
			sequence        INTEGER NOT NULL,
			from_status     TEXT NOT NULL,
			to_status       TEXT NOT NULL CHECK (
				to_status IN ('candidate', 'active', 'paused', 'completed',
					'abandoned', 'merged', 'split', 'expired')
			),
			reason          TEXT NOT NULL DEFAULT '',
			occurred_at     INTEGER NOT NULL,
			created_at      INTEGER NOT NULL,
			UNIQUE (task_episode_id, sequence),
			CHECK (sequence > 0)
		);
		CREATE INDEX IF NOT EXISTS idx_task_episode_transitions_episode
			ON task_episode_transitions(task_episode_id, sequence ASC);
		CREATE INDEX IF NOT EXISTS idx_task_episode_transitions_status
			ON task_episode_transitions(to_status, occurred_at DESC);

		CREATE TABLE IF NOT EXISTS task_episode_links (
			id                TEXT PRIMARY KEY,
			source_episode_id TEXT NOT NULL REFERENCES task_episodes(id) ON DELETE RESTRICT,
			target_episode_id TEXT NOT NULL REFERENCES task_episodes(id) ON DELETE RESTRICT,
			relation          TEXT NOT NULL CHECK (
				relation IN ('parent', 'merged_into', 'split_into')
			),
			created_at        INTEGER NOT NULL,
			UNIQUE (source_episode_id, target_episode_id, relation),
			CHECK (source_episode_id != target_episode_id)
		);
		CREATE INDEX IF NOT EXISTS idx_task_episode_links_source
			ON task_episode_links(source_episode_id, relation, created_at ASC);
		CREATE INDEX IF NOT EXISTS idx_task_episode_links_target
			ON task_episode_links(target_episode_id, relation, created_at ASC);

		CREATE TRIGGER IF NOT EXISTS task_episodes_no_update
		BEFORE UPDATE ON task_episodes
		BEGIN
			SELECT RAISE(ABORT, 'task episodes are immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episodes_no_delete
		BEFORE DELETE ON task_episodes
		BEGIN
			SELECT RAISE(ABORT, 'task episodes are immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_context_no_update
		BEFORE UPDATE ON task_episode_context_snapshots
		BEGIN
			SELECT RAISE(ABORT, 'task episode frozen context is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_context_no_delete
		BEFORE DELETE ON task_episode_context_snapshots
		BEGIN
			SELECT RAISE(ABORT, 'task episode frozen context is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_hypotheses_no_update
		BEFORE UPDATE ON task_episode_hypotheses
		BEGIN
			SELECT RAISE(ABORT, 'task episode hypotheses are immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_hypotheses_no_delete
		BEFORE DELETE ON task_episode_hypotheses
		BEGIN
			SELECT RAISE(ABORT, 'task episode hypotheses are immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_evidence_no_update
		BEFORE UPDATE ON task_episode_evidence
		BEGIN
			SELECT RAISE(ABORT, 'task episode evidence is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_evidence_no_delete
		BEFORE DELETE ON task_episode_evidence
		BEGIN
			SELECT RAISE(ABORT, 'task episode evidence is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_transitions_no_update
		BEFORE UPDATE ON task_episode_transitions
		BEGIN
			SELECT RAISE(ABORT, 'task episode transitions are immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_transitions_no_delete
		BEFORE DELETE ON task_episode_transitions
		BEGIN
			SELECT RAISE(ABORT, 'task episode transitions are immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_links_no_update
		BEFORE UPDATE ON task_episode_links
		BEGIN
			SELECT RAISE(ABORT, 'task episode links are immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS task_episode_links_no_delete
		BEFORE DELETE ON task_episode_links
		BEGIN
			SELECT RAISE(ABORT, 'task episode links are immutable');
		END;
	`); err != nil {
		return fmt.Errorf("create task episode ledger: %w", err)
	}
	return nil
}

func migrateTaskEpisodeBoundaryConfidence(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(task_episode_hypotheses)`)
	if err != nil {
		return fmt.Errorf("inspect task episode hypotheses: %w", err)
	}
	hasColumn := false
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			pk           int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan task episode hypotheses: %w", err)
		}
		if name == "boundary_confidence" {
			hasColumn = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasColumn {
		return nil
	}
	if _, err := tx.Exec(`
		ALTER TABLE task_episode_hypotheses
		ADD COLUMN boundary_confidence REAL NOT NULL DEFAULT 0
			CHECK (boundary_confidence >= 0 AND boundary_confidence <= 1)
	`); err != nil {
		return fmt.Errorf("add task episode boundary confidence: %w", err)
	}
	return nil
}

func migrateTaskEpisodeProfileInfluence(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(task_episode_hypotheses)`)
	if err != nil {
		return fmt.Errorf("inspect task episode hypotheses: %w", err)
	}
	hasColumn := false
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			pk           int
		)
		if err := rows.Scan(
			&cid,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&pk,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan task episode hypotheses: %w", err)
		}
		if name == "profile_influence_json" {
			hasColumn = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasColumn {
		return nil
	}
	if _, err := tx.Exec(`
		ALTER TABLE task_episode_hypotheses
		ADD COLUMN profile_influence_json TEXT NOT NULL DEFAULT '{}'
	`); err != nil {
		return fmt.Errorf("add task episode profile influence: %w", err)
	}
	return nil
}

func migrateTaskEpisodeReasoningHypotheses(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA table_info(task_episode_hypotheses)`)
	if err != nil {
		return fmt.Errorf("inspect task episode hypotheses: %w", err)
	}
	hasColumn := false
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			pk           int
		)
		if err := rows.Scan(
			&cid,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&pk,
		); err != nil {
			rows.Close()
			return fmt.Errorf("scan task episode hypotheses: %w", err)
		}
		if name == "reasoning_hypotheses_json" {
			hasColumn = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasColumn {
		return nil
	}
	if _, err := tx.Exec(`
		ALTER TABLE task_episode_hypotheses
		ADD COLUMN reasoning_hypotheses_json TEXT NOT NULL DEFAULT '[]'
	`); err != nil {
		return fmt.Errorf("add task episode reasoning hypotheses: %w", err)
	}
	return nil
}

func migrateShadowTaskLinkage(tx *sql.Tx) error {
	columns := []struct {
		name       string
		definition string
	}{
		{"task_episode_id", "TEXT NOT NULL DEFAULT ''"},
		{"mode", "TEXT NOT NULL DEFAULT 'legacy_prediction'"},
		{"task_context_hash", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		rows, err := tx.Query(`PRAGMA table_info(shadow_runs)`)
		if err != nil {
			return fmt.Errorf("inspect shadow runs: %w", err)
		}
		found := false
		for rows.Next() {
			var (
				cid          int
				name         string
				columnType   string
				notNull      int
				defaultValue any
				pk           int
			)
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				return fmt.Errorf("scan shadow runs: %w", err)
			}
			if name == column.name {
				found = true
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if found {
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf(
			`ALTER TABLE shadow_runs ADD COLUMN %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return fmt.Errorf("add shadow run %s: %w", column.name, err)
		}
	}
	if _, err := tx.Exec(`
		CREATE INDEX IF NOT EXISTS idx_shadow_runs_task_episode
			ON shadow_runs(task_episode_id, created_at ASC)
	`); err != nil {
		return fmt.Errorf("index shadow task episodes: %w", err)
	}
	return nil
}

// Evidence is deliberately not backfilled in a migration: extraction needs the
// current privacy policy. The run worker refreshes only eligible evidence.
func migrateActivityRunEvidenceTokens(tx *sql.Tx) error {
	for _, column := range []struct{ name, definition string }{
		{"evidence_tokens", "TEXT NOT NULL DEFAULT '[]'"},
		{"evidence_excluded", "INTEGER NOT NULL DEFAULT 0"},
	} {
		var found int
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('activity_timeline_groups') WHERE name = ?`, column.name).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			if _, err := tx.Exec(`ALTER TABLE activity_timeline_groups ADD COLUMN ` + column.name + ` ` + column.definition); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrateActivityQuietSettling(tx *sql.Tx) error {
	var found int
	if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('activities') WHERE name = 'paused_at_ms'`).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		if _, err := tx.Exec(`ALTER TABLE activities ADD COLUMN paused_at_ms INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

func migrateActivityOutcomesAndCorrections(tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS activity_outcomes (
			id TEXT PRIMARY KEY, activity_id TEXT NOT NULL, run_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('reported', 'fixed', 'done', 'abandoned')),
			reason TEXT NOT NULL, occurred_at INTEGER NOT NULL, target TEXT NOT NULL DEFAULT '',
			evidence TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_outcomes_activity ON activity_outcomes(activity_id, occurred_at, id)`,
		`CREATE TABLE IF NOT EXISTS activity_run_corrections (
			id TEXT PRIMARY KEY, activity_id TEXT NOT NULL, run_id TEXT NOT NULL,
			reason TEXT NOT NULL, created_at INTEGER NOT NULL, UNIQUE(activity_id, run_id))`,
		`CREATE INDEX IF NOT EXISTS idx_activity_run_corrections_run ON activity_run_corrections(run_id)`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, name := range []string{"link_reason", "link_token_kind", "link_token_value"} {
		var found int
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('activity_run_links') WHERE name = ?`, name).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			if _, err := tx.Exec(`ALTER TABLE activity_run_links ADD COLUMN ` + name + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	return nil
}
