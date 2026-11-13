package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// SelectedFrame records a single frame (parsed snapshot or OCR sample) that
// the AI triage pipeline considered for a group run. Operators use the list
// in the Settings > Activity Pipeline view to verify the sampling strategy:
// which frames were chosen, which were skipped, and whether the 12-OCR /
// 30 KB parsed budgets are picking up the right signal.
//
// Kind is one of:
//   - "parsed"          — parsed snapshot whose text reached the prompt
//   - "parsed_sibling"  — parsed snapshot folded into a winner via dedup
//     (winner-only would hide N-1 frames); SkipReason
//     is "parsed_dedup_to_winner"
//   - "parsed_skipped"  — parsed snapshot whose entire dedup group never
//     rendered (size cap dropped it); SkipReason is
//     "parsed_oversize"
//   - "ocr"             — OCR frame that reached the prompt
//   - "ocr_skipped"     — OCR frame the pipeline considered but dropped;
//     SkipReason explains why
//
// SnapshotID is set for parsed entries (and for OCR entries when the
// underlying OCR frame is associated with a snapshot; 0 otherwise).
// FrameTime is the OCR frame's offset from session start in seconds and is
// only meaningful for OCR kinds.
type SelectedFrame struct {
	Kind       string  `json:"kind"`
	SessionID  string  `json:"sessionId"`
	SnapshotID int64   `json:"snapshotId,omitempty"`
	FrameTime  float64 `json:"frameTime,omitempty"`
	App        string  `json:"app,omitempty"`
	Label      string  `json:"label,omitempty"`
	// PromptText is the actual text this frame contributed to the triage
	// prompt — already dedup'd, rune-aware truncated, and formatted the
	// same way the LLM saw it. Populated for "parsed" and "ocr" entries;
	// empty for "parsed_sibling" / "parsed_skipped" / "ocr_skipped"
	// because those frames contributed nothing of their own to the prompt.
	//
	// The audit UI uses this to answer "what did the LLM actually see for
	// this frame", which can differ substantially from the snapshot's raw
	// parsed_json / OCR text (dedup, truncation, merge-with-OCR fallback).
	PromptText string `json:"promptText,omitempty"`
	// SkipReason explains why a non-winning frame was not used. Populated
	// for "parsed_sibling", "parsed_skipped" and "ocr_skipped"; empty for
	// "parsed" and "ocr" winners. Accepted values:
	//
	//   - "parsed_dedup_to_winner"  parsed frame in same (app, key) group
	//                               as the winner; folded into it
	//   - "parsed_oversize"         parsed group fell past the section
	//                               size cap and never rendered
	//   - "ocr_covered_by_parsed"   OCR frame's snapshot already produced
	//                               high-fidelity parsed content
	//   - "ocr_prefix_dedup"        OCR text's normalized prefix matches
	//                               a frame already in the prompt
	//   - "ocr_over_cap"            12-frame per-group OCR budget already
	//                               filled before this frame was reached
	//
	// The list is intentionally small and stable so the frontend can map
	// reason → translated label without a server round-trip. New codes
	// must be added to wails-gui/frontend/src/i18n/locales/*/activity.json
	// under session.skipReason.* in the same commit.
	SkipReason string `json:"skipReason,omitempty"`

	// ── Quality metadata (P1) ────────────────────────────────────────
	//
	// Observability-only fields surfaced by the audit UI so operators
	// can judge "did the pipeline pick a representative frame" without
	// re-querying the DB. None of these influence prompt assembly or
	// downstream pipelines — they're sourced from data already produced
	// by the OCR/parser/snapshot path.

	// OCR detector confidence (0.0–1.0). Set for kind="ocr" and
	// kind="ocr_skipped". Reading this lets operators spot low-quality
	// OCR winners (e.g. 0.3 garbled text from a video) that the prompt
	// would have been better off without.
	//
	// json:omitempty intentionally elides exact zero: the OCR engine's
	// zero-confidence frames are usually noise, and a missing badge
	// in the UI reads the same as "no signal worth showing".
	OCRConfidence float64 `json:"ocrConfidence,omitempty"`

	// Number of OTHER snapshots folded into this parsed winner via
	// dedup (i.e. group size minus the winner itself). Set for
	// kind="parsed"; zero means a singleton group, which signals "this
	// winner represents only itself, not a long stretch of similar
	// frames" — useful to spot under-representative winners.
	//
	// NOTE: no omitempty. Zero is a load-bearing value (singleton
	// signal) and must round-trip through JSON; otherwise the UI can't
	// distinguish "winner with 0 siblings (recorded)" from "legacy row
	// without the field (data missing)".
	ParsedSiblingCount int `json:"parsedSiblingCount"`

	// Source URL when the snapshot is a browser frame. Set for
	// kind="parsed" / "ocr" / their skipped counterparts when the
	// snapshot has browserUrl. The hover card shows just the hostname
	// so operators can instantly tell "twitter.com" vs "docs.x.com"
	// without opening the screenshot.
	BrowserURL string `json:"browserUrl,omitempty"`

	// Number of prior OCR frames in this group whose normalized prefix
	// matched this frame's, at the moment this frame was dropped. Set
	// only for kind="ocr_skipped" with SkipReason="ocr_prefix_dedup".
	// Answers "how saturated was this text already" — a chain length of
	// 1 is a single near-duplicate, a chain length of 20 is a screen the
	// user stared at for minutes.
	DedupChainLen int `json:"dedupChainLen,omitempty"`
}

// newGroupPipelineRun builds the common fields of a "group" pipeline run record
// from a sessionGroup. Caller fills in prefilter / AI / outcome fields after.
func newGroupPipelineRun(group sessionGroup, startedAt time.Time) ActivityPipelineRunRow {
	sessionIDs := make([]string, 0, len(group.Sessions))
	for _, s := range group.Sessions {
		sessionIDs = append(sessionIDs, s.ID)
	}
	sessionIDsJSON, _ := json.Marshal(sessionIDs)
	appNamesJSON, _ := json.Marshal(group.allAppNames())

	// All groups are screen-observed "activity" now that IM extractor injection
	// is gone.
	source := "activity"

	return ActivityPipelineRunRow{
		Kind:                         "group",
		StartedAt:                    startedAt.UnixMilli(),
		GroupID:                      group.primaryID(),
		SessionIDs:                   string(sessionIDsJSON),
		AppNames:                     string(appNamesJSON),
		EventCount:                   group.totalEventCount(),
		DurationMs:                   group.endTime() - group.startTime(),
		Source:                       source,
		ShadowObserverExpected:       true,
		ShadowObserverDispatchStatus: TaskShadowObserverDispatchPending,
	}
}

// boolPtr returns a pointer to b. Convenience for populating optional bool
// fields on ActivityPipelineRunRow.
func boolPtr(b bool) *bool { return &b }

// boolPtrIf returns &v when cond is true, otherwise nil. Used on the
// Worth* run-row fields where nil carries the load-bearing semantic
// "this axis was not evaluated" — distinct from "evaluated and false".
func boolPtrIf(cond bool, v bool) *bool {
	if !cond {
		return nil
	}
	return &v
}

// activityPipelineMaxEntries caps the number of pipeline run rows kept on disk.
// Combined with activityPipelineMaxAge, this gives a small, bounded observability
// window for diagnosing why activities did or did not produce automations.
const (
	activityPipelineMaxEntries = 200
	activityPipelineMaxAge     = 7 * 24 * time.Hour
)

// ActivityPipelineRunRow captures one execution of the activity triager
// pipeline. There are two kinds:
//
//   - "tick"  — a single triager evaluate() invocation. Records counters
//     (pending sessions, merged groups, prefilter dismissals, AI candidates).
//   - "group" — a single session group going through the AI triage path.
//     Records the AI input summary, the raw AI response, the "worth"
//     decisions, and the per-pipeline outcome (triage / automation /
//     memory / knowledge), plus any error.
//
// Because the retention window is small (200 entries, 7 days), full text
// (AI summary + AI response) is stored without truncation so that operators
// can see exactly what the model decided and why.
type ActivityPipelineRunRow struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	StartedAt   int64  `json:"startedAt"`
	CompletedAt int64  `json:"completedAt"`

	// Tick-only counters.
	PendingCount   int `json:"pendingCount"`
	GroupCount     int `json:"groupCount"`
	FilteredCount  int `json:"filteredCount"`
	CandidateCount int `json:"candidateCount"`

	// Group identification.
	GroupID    string `json:"groupId,omitempty"`
	SessionIDs string `json:"sessionIds,omitempty"` // JSON array
	AppNames   string `json:"appNames,omitempty"`   // JSON array
	EventCount int    `json:"eventCount,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	// Source of the group: "activity" for screen-observed sessions, or an IM
	// platform ("slack"/"discord"/"telegram") when the group came from injected
	// inbox messages. Lets the dev view filter inbox-origin runs.
	Source string `json:"source,omitempty"`

	// Exact production Observer provenance. Legacy group rows have Expected
	// false and are excluded from coverage rather than inferred as failures.
	ShadowObserverExpected       bool   `json:"shadowObserverExpected"`
	ShadowObserverAdmission      string `json:"shadowObserverAdmission,omitempty"`
	ShadowObserverDispatchStatus string `json:"shadowObserverDispatchStatus,omitempty"`
	ShadowObserverDispatchReason string `json:"shadowObserverDispatchReason,omitempty"`
	ShadowObserverTraceID        string `json:"shadowObserverTraceId,omitempty"`

	// Prefilter outcome (only set for groups that hit the heuristic stage).
	PrefilterPass   *bool  `json:"prefilterPass,omitempty"`
	PrefilterReason string `json:"prefilterReason,omitempty"`

	// AI input/output (full text).
	AISummary     string `json:"aiSummary,omitempty"`
	AIResponseRaw string `json:"aiResponseRaw,omitempty"`

	// AI decisions. nil = AI did not run for this run record.
	WorthMemory     *bool `json:"worthMemory,omitempty"`
	WorthKnowledge  *bool `json:"worthKnowledge,omitempty"`
	WorthAutomation *bool `json:"worthAutomation,omitempty"`
	WorthHandoff    *bool `json:"worthHandoff,omitempty"`

	// Per-pipeline outcomes. TriageStatus / TriageReason record the activity
	// pipeline's lifecycle decision for the group ("dismissed" = processed).
	TriageStatus      string `json:"triageStatus,omitempty"`
	TriageReason      string `json:"triageReason,omitempty"`
	AutomationStatus  string `json:"automationStatus,omitempty"`
	AutomationReason  string `json:"automationReason,omitempty"`
	InitiativeID      string `json:"initiativeId,omitempty"`
	ShadowRunID       string `json:"shadowRunId,omitempty"`
	MemoryStatus      string `json:"memoryStatus,omitempty"`
	MemoryReason      string `json:"memoryReason,omitempty"`
	KnowledgeStatus   string `json:"knowledgeStatus,omitempty"`
	KnowledgeReason   string `json:"knowledgeReason,omitempty"`
	HandoffStatus     string `json:"handoffStatus,omitempty"`
	HandoffReason     string `json:"handoffReason,omitempty"`
	HandoffProposalID string `json:"handoffProposalId,omitempty"`

	// Frames the prompt was actually built from (parsed snapshots + OCR
	// samples that survived dedup and the per-group caps). Empty for tick
	// rows and for groups whose prompt was assembled before this column
	// existed.
	SelectedFrames []SelectedFrame `json:"selectedFrames,omitempty"`

	// Triage prompt diagnostics. TriageFlags is a JSON snapshot of the
	// per-feature toggle state at the moment the prompt was assembled (so a
	// later setting change does not retro-rewrite the record). TriagePrompt
	// is the exact text sent to the LLM; TriagePromptBytes is len(prompt)
	// cached for quick filtering / display without re-loading the full body.
	// All three are empty for tick rows and for groups skipped before the
	// builder ran.
	TriageFlags       string `json:"triageFlags,omitempty"`
	TriagePromptBytes int    `json:"triagePromptBytes,omitempty"`
	TriagePrompt      string `json:"triagePrompt,omitempty"`

	// Top-level pipeline error (e.g. AI call failure).
	Error string `json:"error,omitempty"`
}

// encodeSelectedFrames serializes the per-run frame list to JSON for storage.
// Returns an empty string (not "null") when there is nothing to record so the
// column matches the NOT NULL DEFAULT ” migration contract and the Settings
// UI can treat absence symmetrically across legacy and fresh rows.
func encodeSelectedFrames(frames []SelectedFrame) string {
	if len(frames) == 0 {
		return ""
	}
	b, err := json.Marshal(frames)
	if err != nil {
		log.Printf("[pipeline-runs] marshal selected_frames failed: %v", err)
		return ""
	}
	return string(b)
}

// decodeSelectedFrames is the inverse of encodeSelectedFrames. Returns nil on
// empty input or parse failure — the column is observability metadata, never
// load-bearing, so we never want a bad row to break the whole list view.
func decodeSelectedFrames(raw string) []SelectedFrame {
	if raw == "" {
		return nil
	}
	var out []SelectedFrame
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		log.Printf("[pipeline-runs] unmarshal selected_frames failed: %v", err)
		return nil
	}
	return out
}

func boolToInt(b *bool) any {
	if b == nil {
		return nil
	}
	if *b {
		return 1
	}
	return 0
}

func intToBool(v sql.NullInt64) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Int64 != 0
	return &b
}

// SaveActivityPipelineRun persists a pipeline run record and prunes old entries
// to stay within the retention window. Errors are wrapped with context.
func (s *ActivityStore) SaveActivityPipelineRun(run ActivityPipelineRunRow) (int64, error) {
	selectedFramesJSON := encodeSelectedFrames(run.SelectedFrames)
	res, err := s.db.Exec(`
		INSERT INTO activity_pipeline_runs (
			kind, started_at, completed_at,
			pending_count, group_count, filtered_count, candidate_count,
			group_id, session_ids, app_names, event_count, duration_ms, source,
			prefilter_pass, prefilter_reason,
			ai_summary, ai_response_raw,
			worth_memory, worth_knowledge, worth_automation, worth_handoff,
			triage_status, triage_reason,
			automation_status, automation_reason, initiative_id, shadow_run_id,
			memory_status, memory_reason,
			knowledge_status, knowledge_reason,
			handoff_status, handoff_reason, handoff_proposal_id,
			selected_frames,
			triage_flags, triage_prompt_bytes, triage_prompt,
			shadow_observer_expected, shadow_observer_admission,
			shadow_observer_dispatch_status, shadow_observer_dispatch_reason,
			shadow_observer_trace_id,
			error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.Kind, run.StartedAt, run.CompletedAt,
		run.PendingCount, run.GroupCount, run.FilteredCount, run.CandidateCount,
		run.GroupID, run.SessionIDs, run.AppNames, run.EventCount, run.DurationMs, run.Source,
		boolToInt(run.PrefilterPass), run.PrefilterReason,
		run.AISummary, run.AIResponseRaw,
		boolToInt(run.WorthMemory),
		boolToInt(run.WorthKnowledge), boolToInt(run.WorthAutomation), boolToInt(run.WorthHandoff),
		run.TriageStatus, run.TriageReason,
		run.AutomationStatus, run.AutomationReason, run.InitiativeID, run.ShadowRunID,
		run.MemoryStatus, run.MemoryReason,
		run.KnowledgeStatus, run.KnowledgeReason,
		run.HandoffStatus, run.HandoffReason, run.HandoffProposalID,
		selectedFramesJSON,
		run.TriageFlags, run.TriagePromptBytes, run.TriagePrompt,
		run.ShadowObserverExpected,
		run.ShadowObserverAdmission,
		run.ShadowObserverDispatchStatus,
		run.ShadowObserverDispatchReason,
		run.ShadowObserverTraceID,
		run.Error,
	)
	if err != nil {
		return 0, fmt.Errorf("insert activity_pipeline_run: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	// Best-effort prune. Failures here should not block the save.
	if err := s.PruneActivityPipelineRuns(); err != nil {
		log.Printf("[pipeline-runs] prune after save failed: %v", err)
	}

	return id, nil
}

// UpdateActivityPipelineRunCompletion updates the columns that are populated
// after the row is initially persisted by the synchronous portion of
// triageGroup: completion timestamp, top-level error, and the
// triage / automation outcomes (which may continue to mutate after the
// memory+knowledge generation goroutine has been dispatched).
//
// It deliberately does NOT touch memory_status / memory_reason /
// knowledge_status / knowledge_reason — those are owned by the async
// generation workers (see UpdateActivityPipelineRunMemoryOutcome) and writing
// them here would race with worker completion writes.
func (s *ActivityStore) UpdateActivityPipelineRunCompletion(
	id int64, completedAt int64, errStr string,
	triageStatus, triageReason string,
	automationStatus, automationReason, initiativeID, shadowRunID string,
	handoffStatus, handoffReason, handoffProposalID string,
	shadowObserverExpected bool,
	shadowObserverAdmission, shadowObserverDispatchStatus,
	shadowObserverDispatchReason, shadowObserverTraceID string,
) error {
	if id <= 0 {
		return fmt.Errorf("invalid pipeline run id: %d", id)
	}
	_, err := s.db.Exec(`
		UPDATE activity_pipeline_runs
		SET completed_at = ?, error = ?,
		    triage_status = ?, triage_reason = ?,
		    automation_status = ?, automation_reason = ?, initiative_id = ?, shadow_run_id = ?,
		    handoff_status = ?, handoff_reason = ?, handoff_proposal_id = ?,
		    shadow_observer_expected = ?, shadow_observer_admission = ?,
		    shadow_observer_dispatch_status = ?, shadow_observer_dispatch_reason = ?,
		    shadow_observer_trace_id = ?
		WHERE id = ?`,
		completedAt, errStr,
		triageStatus, triageReason,
		automationStatus, automationReason, initiativeID, shadowRunID,
		handoffStatus, handoffReason, handoffProposalID,
		shadowObserverExpected, shadowObserverAdmission,
		shadowObserverDispatchStatus, shadowObserverDispatchReason,
		shadowObserverTraceID,
		id,
	)
	if err != nil {
		return fmt.Errorf("update pipeline run completion: %w", err)
	}
	return nil
}

// UpdateActivityPipelineRunMemoryOutcome updates only the memory generation
// outcome columns of an existing pipeline run row. Called from the async
// memory generation worker so the telemetry reflects what actually happened
// (created / updated / skipped / error) rather than just the initial
// "dispatched" placeholder written when the worker was scheduled.
//
// Disjoint from UpdateActivityPipelineRunCompletion's column set — the two
// can run in any order without racing.
func (s *ActivityStore) UpdateActivityPipelineRunMemoryOutcome(id int64, status, reason string) error {
	if id <= 0 {
		return fmt.Errorf("invalid pipeline run id: %d", id)
	}
	_, err := s.db.Exec(`
		UPDATE activity_pipeline_runs
		SET memory_status = ?, memory_reason = ?
		WHERE id = ?`, status, reason, id)
	if err != nil {
		return fmt.Errorf("update pipeline run memory outcome: %w", err)
	}
	return nil
}

// UpdateActivityPipelineRunKnowledgeOutcome is the knowledge-side counterpart
// of UpdateActivityPipelineRunMemoryOutcome. Called from the async knowledge
// generation worker so the telemetry reflects what actually happened
// (created / updated / skipped / error) rather than just the initial
// "dispatched" placeholder written when the worker was scheduled.
//
// Column set is disjoint from both UpdateActivityPipelineRunCompletion and
// UpdateActivityPipelineRunMemoryOutcome, so the three can run in any order
// without racing.
func (s *ActivityStore) UpdateActivityPipelineRunKnowledgeOutcome(id int64, status, reason string) error {
	if id <= 0 {
		return fmt.Errorf("invalid pipeline run id: %d", id)
	}
	_, err := s.db.Exec(`
		UPDATE activity_pipeline_runs
		SET knowledge_status = ?, knowledge_reason = ?
		WHERE id = ?`, status, reason, id)
	if err != nil {
		return fmt.Errorf("update pipeline run knowledge outcome: %w", err)
	}
	return nil
}

// GetActivityPipelineRunByID returns a single run row by primary key, or
// (zero, sql.ErrNoRows-wrapped) if it doesn't exist. Used by the async memory
// generation worker to re-read its own row after writing back the outcome,
// so it can emit a fresh `activity_pipeline:run` event with all columns
// populated (the front-end replaces by ID).
func (s *ActivityStore) GetActivityPipelineRunByID(id int64) (ActivityPipelineRunRow, error) {
	var r ActivityPipelineRunRow
	if id <= 0 {
		return r, fmt.Errorf("invalid pipeline run id: %d", id)
	}
	var prefilterPass, worthMemory, worthKnowledge, worthAutomation, worthHandoff sql.NullInt64
	var selectedFramesJSON string
	err := s.db.QueryRow(`
		SELECT id, kind, started_at, completed_at,
			COALESCE(pending_count, 0), COALESCE(group_count, 0),
			COALESCE(filtered_count, 0), COALESCE(candidate_count, 0),
			COALESCE(group_id, ''), COALESCE(session_ids, ''), COALESCE(app_names, ''),
			COALESCE(event_count, 0), COALESCE(duration_ms, 0), COALESCE(source, ''),
			prefilter_pass, COALESCE(prefilter_reason, ''),
			COALESCE(ai_summary, ''), COALESCE(ai_response_raw, ''),
			worth_memory, worth_knowledge, worth_automation, worth_handoff,
			COALESCE(triage_status, ''), COALESCE(triage_reason, ''),
			COALESCE(automation_status, ''), COALESCE(automation_reason, ''),
			COALESCE(initiative_id, ''),
			COALESCE(shadow_run_id, ''),
			COALESCE(memory_status, ''), COALESCE(memory_reason, ''),
			COALESCE(knowledge_status, ''), COALESCE(knowledge_reason, ''),
			COALESCE(handoff_status, ''), COALESCE(handoff_reason, ''),
			COALESCE(handoff_proposal_id, ''),
			COALESCE(selected_frames, ''),
			COALESCE(triage_flags, ''), COALESCE(triage_prompt_bytes, 0), COALESCE(triage_prompt, ''),
			COALESCE(shadow_observer_expected, 0),
			COALESCE(shadow_observer_admission, ''),
			COALESCE(shadow_observer_dispatch_status, ''),
			COALESCE(shadow_observer_dispatch_reason, ''),
			COALESCE(shadow_observer_trace_id, ''),
			COALESCE(error, '')
		FROM activity_pipeline_runs
		WHERE id = ?`, id).Scan(
		&r.ID, &r.Kind, &r.StartedAt, &r.CompletedAt,
		&r.PendingCount, &r.GroupCount, &r.FilteredCount, &r.CandidateCount,
		&r.GroupID, &r.SessionIDs, &r.AppNames, &r.EventCount, &r.DurationMs, &r.Source,
		&prefilterPass, &r.PrefilterReason,
		&r.AISummary, &r.AIResponseRaw,
		&worthMemory, &worthKnowledge, &worthAutomation, &worthHandoff,
		&r.TriageStatus, &r.TriageReason,
		&r.AutomationStatus, &r.AutomationReason, &r.InitiativeID, &r.ShadowRunID,
		&r.MemoryStatus, &r.MemoryReason,
		&r.KnowledgeStatus, &r.KnowledgeReason,
		&r.HandoffStatus, &r.HandoffReason, &r.HandoffProposalID,
		&selectedFramesJSON,
		&r.TriageFlags, &r.TriagePromptBytes, &r.TriagePrompt,
		&r.ShadowObserverExpected,
		&r.ShadowObserverAdmission,
		&r.ShadowObserverDispatchStatus,
		&r.ShadowObserverDispatchReason,
		&r.ShadowObserverTraceID,
		&r.Error,
	)
	if err != nil {
		return r, fmt.Errorf("get activity_pipeline_run %d: %w", id, err)
	}
	r.PrefilterPass = intToBool(prefilterPass)
	r.WorthMemory = intToBool(worthMemory)
	r.WorthKnowledge = intToBool(worthKnowledge)
	r.WorthAutomation = intToBool(worthAutomation)
	r.WorthHandoff = intToBool(worthHandoff)
	r.SelectedFrames = decodeSelectedFrames(selectedFramesJSON)
	return r, nil
}

// ListActivityPipelineRuns returns the most recent pipeline runs, newest first.
// Limit is clamped to [1, activityPipelineMaxEntries].
func (s *ActivityStore) ListActivityPipelineRuns(limit int) ([]ActivityPipelineRunRow, error) {
	if limit <= 0 {
		limit = activityPipelineMaxEntries
	}
	if limit > activityPipelineMaxEntries {
		limit = activityPipelineMaxEntries
	}

	rows, err := s.db.Query(`
		SELECT id, kind, started_at, completed_at,
			COALESCE(pending_count, 0), COALESCE(group_count, 0),
			COALESCE(filtered_count, 0), COALESCE(candidate_count, 0),
			COALESCE(group_id, ''), COALESCE(session_ids, ''), COALESCE(app_names, ''),
			COALESCE(event_count, 0), COALESCE(duration_ms, 0), COALESCE(source, ''),
			prefilter_pass, COALESCE(prefilter_reason, ''),
			COALESCE(ai_summary, ''), COALESCE(ai_response_raw, ''),
			worth_memory, worth_knowledge, worth_automation, worth_handoff,
			COALESCE(triage_status, ''), COALESCE(triage_reason, ''),
			COALESCE(automation_status, ''), COALESCE(automation_reason, ''),
			COALESCE(initiative_id, ''),
			COALESCE(shadow_run_id, ''),
			COALESCE(memory_status, ''), COALESCE(memory_reason, ''),
			COALESCE(knowledge_status, ''), COALESCE(knowledge_reason, ''),
			COALESCE(handoff_status, ''), COALESCE(handoff_reason, ''),
			COALESCE(handoff_proposal_id, ''),
			COALESCE(selected_frames, ''),
			COALESCE(triage_flags, ''), COALESCE(triage_prompt_bytes, 0), COALESCE(triage_prompt, ''),
			COALESCE(shadow_observer_expected, 0),
			COALESCE(shadow_observer_admission, ''),
			COALESCE(shadow_observer_dispatch_status, ''),
			COALESCE(shadow_observer_dispatch_reason, ''),
			COALESCE(shadow_observer_trace_id, ''),
			COALESCE(error, '')
		FROM activity_pipeline_runs
		ORDER BY started_at DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query activity_pipeline_runs: %w", err)
	}
	defer rows.Close()

	var out []ActivityPipelineRunRow
	for rows.Next() {
		var r ActivityPipelineRunRow
		var prefilterPass, worthMemory, worthKnowledge, worthAutomation, worthHandoff sql.NullInt64
		var selectedFramesJSON string

		if err := rows.Scan(
			&r.ID, &r.Kind, &r.StartedAt, &r.CompletedAt,
			&r.PendingCount, &r.GroupCount, &r.FilteredCount, &r.CandidateCount,
			&r.GroupID, &r.SessionIDs, &r.AppNames, &r.EventCount, &r.DurationMs, &r.Source,
			&prefilterPass, &r.PrefilterReason,
			&r.AISummary, &r.AIResponseRaw,
			&worthMemory, &worthKnowledge, &worthAutomation, &worthHandoff,
			&r.TriageStatus, &r.TriageReason,
			&r.AutomationStatus, &r.AutomationReason, &r.InitiativeID, &r.ShadowRunID,
			&r.MemoryStatus, &r.MemoryReason,
			&r.KnowledgeStatus, &r.KnowledgeReason,
			&r.HandoffStatus, &r.HandoffReason, &r.HandoffProposalID,
			&selectedFramesJSON,
			&r.TriageFlags, &r.TriagePromptBytes, &r.TriagePrompt,
			&r.ShadowObserverExpected,
			&r.ShadowObserverAdmission,
			&r.ShadowObserverDispatchStatus,
			&r.ShadowObserverDispatchReason,
			&r.ShadowObserverTraceID,
			&r.Error,
		); err != nil {
			return nil, fmt.Errorf("scan activity_pipeline_run: %w", err)
		}

		r.PrefilterPass = intToBool(prefilterPass)
		r.WorthMemory = intToBool(worthMemory)
		r.WorthKnowledge = intToBool(worthKnowledge)
		r.WorthAutomation = intToBool(worthAutomation)
		r.WorthHandoff = intToBool(worthHandoff)
		r.SelectedFrames = decodeSelectedFrames(selectedFramesJSON)

		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate activity_pipeline_runs: %w", err)
	}
	return out, nil
}

// ClearActivityPipelineRuns deletes all pipeline run records.
func (s *ActivityStore) ClearActivityPipelineRuns() error {
	if _, err := s.db.Exec(`DELETE FROM activity_pipeline_runs`); err != nil {
		return fmt.Errorf("delete activity_pipeline_runs: %w", err)
	}
	return nil
}

// GetActivityPipelineRuns returns the most recent activity pipeline run records.
// Wails-bound. Pass limit<=0 to return up to the full retention window.
func (a *App) GetActivityPipelineRuns(limit int) (perfValue []ActivityPipelineRunRow, perfErr error) {
	defer logPageBinding("GetActivityPipelineRuns", time.Now(), &perfValue)
	if a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	return a.activity.store.ListActivityPipelineRuns(limit)
}

// ClearActivityPipelineRuns wipes all stored pipeline run records.
// Wails-bound. Useful for shrinking the observability window before reproducing
// a specific scenario.
func (a *App) ClearActivityPipelineRuns() error {
	if a.activity == nil || a.activity.store == nil {
		return fmt.Errorf("activity store not initialized")
	}
	return a.activity.store.ClearActivityPipelineRuns()
}

// RunActivityPipelineNow runs the hourly activity batch now. Wails-bound.
// Returns once the batch finishes (synchronous, can be slow if there are many
// pending sessions). Useful for triggering a pipeline run from the Settings UI
// without waiting for the next hourly batch.
func (a *App) RunActivityPipelineNow() error {
	if a.activity == nil {
		return fmt.Errorf("activity monitor not initialized")
	}
	a.activity.mu.Lock()
	c := a.activity.triager
	a.activity.mu.Unlock()
	if c == nil {
		return fmt.Errorf("activity triager not running")
	}
	// Fall back to Background when Wails OnStartup has not yet populated a.ctx
	// (edge case: manual trigger during startup handshake).
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return c.runBatch(ctx, "manual")
}

// PruneActivityPipelineRuns enforces the retention window:
//
//	row count <= activityPipelineMaxEntries
//	row age   <= activityPipelineMaxAge
//
// Both rules are applied together; a row is kept only if it satisfies both.
func (s *ActivityStore) PruneActivityPipelineRuns() error {
	cutoff := time.Now().Add(-activityPipelineMaxAge).UnixMilli()

	if _, err := s.db.Exec(`DELETE FROM activity_pipeline_runs WHERE started_at < ?`, cutoff); err != nil {
		return fmt.Errorf("prune by age: %w", err)
	}

	if _, err := s.db.Exec(`
		DELETE FROM activity_pipeline_runs
		WHERE id NOT IN (
			SELECT id FROM activity_pipeline_runs
			ORDER BY started_at DESC, id DESC
			LIMIT ?
		)`, activityPipelineMaxEntries); err != nil {
		return fmt.Errorf("prune by count: %w", err)
	}

	return nil
}
