package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/handoff"
	"wails-gui/storage"
	agent "yansu-agent/agent"
	"yansu-agent/agent/authority"
	agenttools "yansu-agent/agent/tools"
	"yansu-agent/projectcontext"
)

// DeliveryTarget represents a user-specified destination for automation results
// parsed from the agent's <delivery_target> block.
type DeliveryTarget struct {
	Platform string // "slack", "discord", "telegram", "feishu", "wechat"
	Server   string // Discord guild/server name (optional, Discord only)
	Channel  string // channel name (without #) or channel/chat ID; ignored for WeChat (DM-only)
}

// maxDeliverablePaths caps the number of deliverable file paths parsed
// from the agent's <deliverables> block.
const maxDeliverablePaths = 10

// maxDeliverableDescriptionRunes caps the per-file description parsed from a
// <deliverables> line — it renders as a one-line card subtitle, so anything
// longer is agent rambling, not a description.
const maxDeliverableDescriptionRunes = 200

// deliverableBlockRe matches the <deliverables>...</deliverables> block
// in the agent's summary output.
var deliverableBlockRe = regexp.MustCompile(`(?s)<deliverables>\s*(.*?)\s*</deliverables>`)

// deliverableRef is one parsed line of a <deliverables> block: an absolute
// path plus an optional agent-written one-sentence description (the text
// after the first "|" separator; legacy path-only lines yield an empty one).
type deliverableRef struct {
	Path        string
	Description string
}

// parseDeliverableRefs extracts deliverable files from a <deliverables> block
// in the summary and returns the cleaned summary (block removed) plus the
// parsed refs. Each line is "path" or "path | description". If no block is
// found, the original summary is returned unchanged with a nil slice.
func parseDeliverableRefs(summary string) (string, []deliverableRef) {
	match := deliverableBlockRe.FindStringSubmatchIndex(summary)
	if match == nil {
		return summary, nil
	}

	// Extract the inner content (capture group 1).
	inner := summary[match[2]:match[3]]

	// Remove the entire <deliverables>...</deliverables> block from summary.
	cleanSummary := strings.TrimSpace(summary[:match[0]] + summary[match[1]:])

	var refs []deliverableRef
	for _, line := range strings.Split(inner, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ref := deliverableRef{Path: line}
		// Split on the first "|" only, so a description containing "|" stays
		// intact. Paths virtually never contain "|"; descriptions might.
		if i := strings.Index(line, "|"); i >= 0 {
			ref.Path = strings.TrimSpace(line[:i])
			ref.Description = sanitizeDeliverableDescription(line[i+1:])
		}
		if ref.Path == "" {
			continue
		}
		refs = append(refs, ref)
		if len(refs) >= maxDeliverablePaths {
			break
		}
	}

	return cleanSummary, refs
}

// sanitizeDeliverableDescription normalizes an agent-written file description
// for storage and display: inline markdown is stripped (the description
// renders in a plain-text card subtitle) and the result is capped at
// maxDeliverableDescriptionRunes. Every agent-authored ingestion path must
// funnel through this so chat, automation, and extractor descriptions carry
// the same guarantees.
func sanitizeDeliverableDescription(s string) string {
	// Trim first: stripInlineMarkdown's line-prefix patterns are anchored at
	// the start of the string and would miss e.g. " - item".
	s = stripInlineMarkdown(strings.TrimSpace(s))
	if r := []rune(s); len(r) > maxDeliverableDescriptionRunes {
		s = string(r[:maxDeliverableDescriptionRunes]) + "…"
	}
	return s
}

// parseDeliverables is the path-only view of parseDeliverableRefs, for callers
// (notification text, file delivery) that don't need per-file descriptions.
func parseDeliverables(summary string) (string, []string) {
	cleanSummary, refs := parseDeliverableRefs(summary)
	if len(refs) == 0 {
		return cleanSummary, nil
	}
	paths := make([]string, 0, len(refs))
	for _, ref := range refs {
		paths = append(paths, ref.Path)
	}
	return cleanSummary, paths
}

// deliveryTargetBlockRe matches the <delivery_target>...</delivery_target> block
// in the agent's summary output.
var deliveryTargetBlockRe = regexp.MustCompile(`(?s)<delivery_target>\s*(.*?)\s*</delivery_target>`)

// Regex-based field extractors — work for both multiline and single-line formats.
// Platform is always a single token; server/channel use non-greedy match that stops
// at the next field keyword or end-of-line to support names with spaces
// (e.g. Discord server "Marketing Team", Feishu group names).
var (
	deliveryPlatformRe = regexp.MustCompile(`(?im)platform:\s*(\S+)`)
	deliveryServerRe   = regexp.MustCompile(`(?im)(?:server|guild):\s*(.+?)(?:\s+(?:platform|channel|target):|$)`)
	deliveryChannelRe  = regexp.MustCompile(`(?im)(?:channel|target):\s*(.+?)(?:\s+(?:platform|server|guild):|$)`)
)

// parseDeliveryTarget extracts a delivery target from the agent's summary.
// Returns the cleaned summary (block removed) and the parsed target (nil if not found).
func parseDeliveryTarget(summary string) (string, *DeliveryTarget) {
	match := deliveryTargetBlockRe.FindStringSubmatchIndex(summary)
	if match == nil {
		return summary, nil
	}

	inner := summary[match[2]:match[3]]
	cleanSummary := strings.TrimSpace(summary[:match[0]] + summary[match[1]:])

	target := &DeliveryTarget{}

	if m := deliveryPlatformRe.FindStringSubmatch(inner); m != nil {
		target.Platform = strings.ToLower(strings.TrimSpace(m[1]))
	}
	if m := deliveryServerRe.FindStringSubmatch(inner); m != nil {
		target.Server = strings.TrimSpace(m[1])
	}
	if m := deliveryChannelRe.FindStringSubmatch(inner); m != nil {
		target.Channel = strings.TrimPrefix(strings.TrimSpace(m[1]), "#")
	}

	if target.Platform == "" {
		return cleanSummary, nil
	}
	return cleanSummary, target
}

// configuredDeliveryPlatform returns the platform a job sends its result to,
// or "" when the job has no destination configured (notifications off, or
// the platform is "none").
func configuredDeliveryPlatform(job *storage.CronJobRow) string {
	if job == nil || !job.NotifyDM {
		return ""
	}
	platform := strings.ToLower(strings.TrimSpace(job.NotifyPlatform))
	if platform == "none" {
		return ""
	}
	return platform
}

// unattendedDeliveryTarget keeps a model-written <delivery_target> only when it
// names the destination the job was configured with: its notify platform, with
// no channel or server. The job stores no channel or server, so any such name
// was chosen by the model and is dropped. Dropped results go to the job's own
// destination, or nowhere when the job has none.
func unattendedDeliveryTarget(target *DeliveryTarget, job *storage.CronJobRow) *DeliveryTarget {
	if target == nil {
		return nil
	}
	configured := configuredDeliveryPlatform(job)
	if configured == "" || target.Platform != configured || target.Channel != "" || target.Server != "" {
		log.Printf("[cron-exec] ignored a model-written delivery target (platform=%q channel=%q server=%q): not configured on the job",
			target.Platform, target.Channel, target.Server)
		return nil
	}
	return target
}

// errAgentNotReady is a sentinel error indicating the agent runner is not
// available. The scheduler records the run as "skipped" (not "error")
// and does NOT increment consecutive_errors / trigger auto-disable.
var errAgentNotReady = fmt.Errorf("agent not ready")

// errTopicSearchPaused holds a topic or Daily News search under Closed Eyes.
// It wraps errAgentNotReady so the scheduler records the run as skipped.
var errTopicSearchPaused = fmt.Errorf("%w: observing is paused", errAgentNotReady)

// cronTargetConfig is the parsed target_config for agent jobs.
type cronTargetConfig struct {
	SessionTag string `json:"session_tag"`
	ProjectDir string `json:"project_dir"`
	Prompt     string `json:"prompt"`
	Model      string `json:"model"`
	// HandoffID, when set, links this at-job back to a file-first handoff so the
	// executor can flip its lifecycle status to completed/aborted on finish.
	HandoffID string `json:"handoff_id"`
	// SOPID, when set, makes this the schedule of a playbook: each run
	// rebuilds its prompt from the playbook and gets the sites the user
	// approved for it (playbook_run.go).
	SOPID        string                      `json:"sop_id,omitempty"`
	TodayNoticed *todayNoticedScheduleSource `json:"today_noticed,omitempty"`
}

// executeAgentJob is the execution adapter for agent target jobs.
// It parses target_config, ensures a hidden session exists, calls
// executePromptSync, and returns the result summary with log metadata.
func (a *App) executeAgentJob(ctx context.Context, job *storage.CronJobRow) (result *cronExecResult, retErr error) {
	var cfg cronTargetConfig
	if err := json.Unmarshal([]byte(job.TargetConfig), &cfg); err != nil {
		return nil, fmt.Errorf("parse target config: %w", err)
	}
	if cfg.TodayNoticed != nil && !a.todayNoticedScheduleReadable(cfg.TodayNoticed) {
		return nil, errTodayNoticedUnavailable
	}
	var handoffRun *handoff.Entry
	if cfg.HandoffID != "" {
		if a.handoffStore == nil {
			return nil, errors.New("one-time work store is unavailable")
		}
		entry, err := a.handoffStore.Get(cfg.HandoffID)
		if err != nil {
			return nil, err
		}
		if entry.Status != handoff.StatusInProgress || entry.CronJobID != job.ID {
			return nil, errors.New("one-time work no longer belongs to this run")
		}
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ctx = runCtx
		handoffRun = &entry
		if err := a.registerHandoffRun(handoffRun, entry.Workdir, cancel); err != nil {
			return nil, err
		}
		defer func() {
			summary := ""
			if result != nil {
				summary = result.Summary
			}
			go a.markHandoffAttemptEnded(*handoffRun, retErr == nil, summary, retErr)
		}()
	}
	researchJob := isAppResearchJob(job)
	if isTopicSearchJob(job) && !a.topicSearchMayLeave() {
		// A topic or Daily News search queued before Closed Eyes doesn't go
		// after it.
		log.Printf("[cron] %s held: observing is paused", job.Name)
		return &cronExecResult{}, errTopicSearchPaused
	}
	if job.SurfaceKind == openAnswerSurfaceKind {
		if !researchJob {
			log.Printf("[open-answer] refusing unrecognized open-answer job: job=%s name=%q", job.ID, job.Name)
			return &cronExecResult{}, nil
		}
		state := jobRowToState(job)
		if _, ok := a.appResearchPrompt(&state, ""); !ok {
			a.deferOpenAnswerCheck(job, "privacy or research is unavailable")
			return &cronExecResult{}, nil
		}
	}
	if researchJob {
		// The app's research runs search through the user's signed-in Claude
		// Code when there is one, else through the native loop with opencli
		// (research_cli.go), and use the prompt the app builds now, never the
		// stored one (cron_system_lock.go).
		via := researchVia()
		if summary, handled, err := a.runResearchJob(ctx, job, via); handled {
			if job.SurfaceKind == openAnswerSurfaceKind {
				if err != nil && err.Error() == "the search has no topics to look for" {
					a.deferOpenAnswerCheck(job, "privacy or research is unavailable")
					return &cronExecResult{Summary: ""}, nil
				}
				if err == nil {
					a.completeOpenAnswerCheck(
						openAnswerIDFromJobName(job.Name),
						strings.TrimSpace(stripResearchMeta(summary)),
						time.Now(),
						job.ID,
					)
				}
				return &cronExecResult{Summary: ""}, err
			}
			return &cronExecResult{Summary: summary}, err
		}
		state := jobRowToState(job)
		prompt, ok := a.appResearchPrompt(&state, via)
		if !ok {
			if job.SurfaceKind == openAnswerSurfaceKind {
				a.deferOpenAnswerCheck(job, "privacy or research is unavailable")
				return &cronExecResult{Summary: ""}, nil
			}
			return nil, fmt.Errorf("the search has no topics to look for")
		}
		cfg.Prompt = prompt
	}
	if cfg.SOPID != "" {
		prompt, grants, err := a.preparePlaybookRun(job.ID, cfg.SOPID)
		if err != nil {
			return nil, err
		}
		cfg.Prompt = prompt
		ctx = withPlaybookGrants(ctx, grants, a.playbookRunAuthority(job.ID, cfg.SOPID, grants))
	}
	if strings.TrimSpace(cfg.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required in target config")
	}
	// A project folder of /, the home folder or an ancestor of it would make
	// every file under it the run's work folder, and so uploadable.
	if err := validateCronProjectDir(cfg.ProjectDir); err != nil {
		return nil, err
	}

	// Ensure a hidden log session exists for this job.
	// This session tag is for UI/log persistence only; automation runs are
	// intentionally stateless at the model layer. A stored tag that does not
	// name this job's own hidden session (a target config rewritten to point
	// at a chat) is dropped, so a run never writes into another chat.
	tag := a.ownCronSessionTag(ctx, job.ID, cfg.SessionTag)

	if tag == "" {
		// Create a new hidden session for this job.
		var err error
		tag, err = a.ensureCronSession(ctx, job.ID, cfg.ProjectDir)
		if err != nil {
			return nil, fmt.Errorf("ensure cron session: %w", err)
		}

		// Persist the session_tag back to the job so subsequent runs append logs
		// to the same hidden automation thread.
		cfg.SessionTag = tag
		updatedConfig, _ := json.Marshal(cfg)
		_ = a.store.UpdateCronJob(ctx, job.ID, map[string]any{
			"target_config": string(updatedConfig),
		})
		// Also update the in-memory job row so the scheduler cache stays in sync.
		job.TargetConfig = string(updatedConfig)
	}

	// Resolve work directory. When the job does not pin a projectDir, fall
	// back to a stable per-job directory under ~/.isoform-workspaces so that
	// deliverables survive app restart and are addressable from the UI. The
	// system temp dir was a poor fit here: macOS cleans it asynchronously and
	// the in-memory sessionTempDirs cache is lost on restart, stranding the
	// user from artifacts produced by earlier runs.
	//
	// enforcedWorkDir is only set in the fallback case. When the user has
	// pinned a projectDir we stay out of the agent's way so that business
	// prompts like "save the report to ~/Documents/reports/" are honored.
	workDir := cfg.ProjectDir
	var enforcedWorkDir string
	if workDir == "" {
		dir, derr := a.defaultAutomationWorkDir(job.ID)
		if derr != nil {
			return nil, fmt.Errorf("resolve default work dir: %w", derr)
		}
		workDir = dir
		enforcedWorkDir = dir
	}

	if handoffRun != nil {
		a.handoffActionMu.Lock()
		saved, err := a.handoffStore.MutateIfAttempt(handoffRun.ID, handoffRun.AttemptID, []handoff.Status{handoff.StatusInProgress}, func(e *handoff.Entry) { e.Workdir = workDir })
		a.handoffActionMu.Unlock()
		if err != nil {
			return nil, err
		}
		*handoffRun = saved
	}

	model := cfg.Model
	if model == "" {
		model = a.chatTaskModel()
	}

	// Resolve the run's scratch folder for path validation. It is derived
	// from the job ID, never from the session tag, so a tag that names a
	// chat cannot make that chat's folder an upload source.
	sessionTempDir := a.getOrCreateSessionTempDir(cronScratchTag(job.ID))

	// Only forward notify_platform as a prompt-level delivery hint when the
	// user has explicitly opted in to integration DMs. If NotifyDM is off or
	// the platform is "none", the post-run notification path will short-
	// circuit only as long as the model does NOT emit <delivery_target> —
	// passing a non-empty hint here would force the model to emit one and
	// silently re-enable the suppressed notification.
	promptNotifyPlatform := ""
	if job.NotifyDM && job.NotifyPlatform != "none" {
		promptNotifyPlatform = job.NotifyPlatform
	}
	authorityInitiativeID := ""
	if a.activity != nil && a.activity.store != nil {
		if initiative, initiativeErr := a.activity.store.GetInitiativeByExecutionRef(job.ID); initiativeErr == nil {
			authorityInitiativeID = initiative.ID
		}
	}

	runCtx := withAppResearchGrants(ctx, appResearchAuthorityGrants(job))
	takeCronToolFailures(tag) // nothing left over from an earlier run
	if isTopicSearchJob(job) && !a.topicSearchMayLeave() {
		return &cronExecResult{}, errTopicSearchPaused
	}
	summary, sortStart, writtenFiles, err := a.executePromptSync(
		runCtx,
		tag,
		"",
		workDir,
		enforcedWorkDir,
		cfg.Prompt,
		model,
		promptNotifyPlatform,
		func(firstSortOrder int) {
			if a.store == nil {
				return
			}
			if err := a.store.AttachLogsToLatestRunningCronRun(ctx, job.ID, tag, firstSortOrder); err != nil {
				log.Printf("[cron-exec] attach logs to running cron run failed: job=%s tag=%s sortStart=%d err=%v",
					job.ID, tag, firstSortOrder, err)
			}
		},
		cfg.HandoffID,
		authorityInitiativeID,
	)
	if job.SurfaceKind == openAnswerSurfaceKind {
		if err == nil {
			a.completeOpenAnswerCheck(
				openAnswerIDFromJobName(job.Name),
				strings.TrimSpace(stripResearchMeta(summary)),
				time.Now(),
				job.ID,
			)
		}
		return &cronExecResult{Summary: ""}, err
	}

	failedTools := takeCronToolFailures(tag)
	if researchJob {
		summary = a.recordNativeResearch(ctx, tag, sortStart, summary, failedTools, job.Name, cfg.Prompt)
		if searchErr := researchRunError(summary); searchErr != nil {
			err = errors.Join(err, searchErr)
		}
	}

	// Parse deliverable file paths and delivery target from agent summary.
	cleanSummary, filePaths := parseDeliverables(summary)
	cleanSummary, deliveryTarget := parseDeliveryTarget(cleanSummary)
	// The run is unattended: a model-written target counts only when it is the
	// destination the job itself was configured with.
	deliveryTarget = unattendedDeliveryTarget(deliveryTarget, job)

	// Surface "agent finished but produced nothing" as an error rather than a
	// silent ok. The user's mental model of an automation is "it produces
	// something" — a run with empty summary, no declared deliverable files,
	// and no delivery target violates that contract and must be visible in
	// the UI as a failure. Leaving this as ok caused the "nothing really
	// happened" user report that motivated this check.
	if err == nil && isNoDeliverableResult(cleanSummary, filePaths, deliveryTarget) {
		err = fmt.Errorf("run produced no deliverable: agent returned empty summary with no files or delivery target")
	}

	// Spoken completion announcement. Only on success, and only when the user
	// opted into automation auto-speak — the chat read-aloud setting is
	// independent and must not make automations talk. The frontend composes the
	// localized "<job> finished" sentence and speaks it; we just hand over the
	// job name so the wording stays in the i18n layer.
	if err == nil && a.GetAutoSpeakAutomationEnabled() {
		a.emitBrowserEvent("tts:announce", map[string]any{
			"category": "automation",
			"name":     job.Name,
		})
	}

	return &cronExecResult{
		Summary:        cleanSummary,
		SessionTag:     tag,
		ItemSortStart:  sortStart,
		FilePaths:      filePaths,
		DeliveryTarget: deliveryTarget,
		WorkDir:        workDir,
		SessionTempDir: sessionTempDir,
		WrittenFiles:   writtenFiles,
	}, err
}

// recordNativeResearch keeps only the final answer of a native research run
// and appends the app's record of what its searches returned
// (research_cli.go). A research_meta block the model wrote is always
// removed; when the run's tool results cannot be read there is no record,
// and the run counts as one that could not search.
func (a *App) recordNativeResearch(ctx context.Context, tag string, sortStart int, summary string, failedTools map[string]bool, jobName, prompt string) string {
	if a.store == nil || sortStart < 0 {
		log.Printf("[research] no record for tag=%s: the run's tool results were not stored", tag)
		return stripResearchMeta(summary)
	}
	items, err := a.store.LoadStreamItemsForCronRun(ctx, tag, sortStart, 0)
	if err != nil {
		log.Printf("[research] no record for tag=%s: %v", tag, err)
		return stripResearchMeta(summary)
	}
	meta, final := researchMetaFromItems(items, failedTools)
	// The scheduled composer no longer adds standing personal context. Like
	// the dedicated research route, personalization follows the task's topics.
	meta.Personal = researchPersonal(jobName, prompt)
	if strings.TrimSpace(final) != "" {
		summary = final
	}
	return withResearchMeta(summary, meta)
}

// isNoDeliverableResult reports whether a completed agent run produced nothing
// the user can act on — no summary text, no declared deliverable files, and
// no delivery target. Extracted as a standalone helper so the rule is
// unit-testable without needing to stand up a full runner.
func isNoDeliverableResult(summary string, filePaths []string, deliveryTarget *DeliveryTarget) bool {
	return strings.TrimSpace(summary) == "" && len(filePaths) == 0 && deliveryTarget == nil
}

// ensureCronSession creates a hidden session for a cron job.
// If a visible project exists at the given path, the session is created
// under that project's default workspace. Otherwise a synthetic hidden
// project is created.
func (a *App) ensureCronSession(ctx context.Context, jobID, projectDir string) (string, error) {
	sessionLocalID := uuid.New().String()

	var projectID, workspaceID string

	// Try to find an existing project by path.
	if projectDir != "" {
		projects, err := a.store.LoadProjects(ctx)
		if err == nil {
			for _, p := range projects {
				if p.Path == projectDir {
					projectID = p.ID
					break
				}
			}
		}
	}

	if projectID != "" {
		// Found existing project — find its default workspace.
		workspaces, err := a.store.LoadWorkspaces(ctx, projectID)
		if err == nil && len(workspaces) > 0 {
			workspaceID = workspaces[0].ID
		}
	}

	if projectID == "" {
		// Check if a synthetic project already exists for this job
		// (includes hidden projects, which LoadProjects above filters out).
		syntheticPath := fmt.Sprintf("__cron__/%s", jobID)
		if existing, err := a.store.LoadProjectByPath(ctx, syntheticPath); err == nil {
			projectID = existing.ID
			if ws, err := a.store.LoadWorkspacesByProject(ctx, projectID); err == nil && len(ws) > 0 {
				workspaceID = ws[0].ID
			}
			// If the synthetic project exists but its workspace is missing,
			// recreate the default hidden workspace so the session is valid.
			if workspaceID == "" {
				workspaceID = uuid.New().String()
				if err := a.store.SaveWorkspace(ctx, storage.WorkspaceRow{
					ID:        workspaceID,
					ProjectID: projectID,
					Label:     "default",
					IsDefault: true,
					Hidden:    true,
				}); err != nil {
					return "", fmt.Errorf("create synthetic workspace: %w", err)
				}
			}
		}

		if projectID == "" {
			// Create a hidden synthetic project.
			projectID = uuid.New().String()
			workspaceID = uuid.New().String()

			if err := a.store.EnsureProjectStructure(ctx,
				storage.ProjectRow{
					ID:     projectID,
					Name:   fmt.Sprintf("Cron: %s", jobID[:8]),
					Path:   syntheticPath,
					Hidden: true,
				},
				[]storage.WorkspaceRow{{
					ID:        workspaceID,
					ProjectID: projectID,
					Label:     "default",
					IsDefault: true,
					Hidden:    true,
				}},
				nil,
			); err != nil {
				return "", fmt.Errorf("create synthetic project: %w", err)
			}
		}
	}

	if workspaceID == "" {
		workspaceID = uuid.New().String()
	}

	// Create the hidden session.
	sess := storage.SessionRow{
		LocalID:     sessionLocalID,
		ProjectID:   projectID,
		WorkspaceID: workspaceID,
		Label:       cronSessionLabel(jobID),
		Hidden:      true,
	}
	if err := a.store.SaveSession(ctx, sess); err != nil {
		return "", fmt.Errorf("save cron session: %w", err)
	}

	return sessionLocalID, nil
}

// cronSessionLabel is the label of a job's hidden log session.
func cronSessionLabel(jobID string) string {
	short := jobID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("Cron session %s", short)
}

// cronScratchTag names a job's scratch folder (see getOrCreateSessionTempDir).
func cronScratchTag(jobID string) string {
	return "cron-" + jobID
}

// ownCronSessionTag returns tag when it names this job's own hidden log
// session, or "" so a new one is created.
func (a *App) ownCronSessionTag(ctx context.Context, jobID, tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" || a.store == nil {
		return ""
	}
	sess, err := a.store.LoadSession(ctx, tag)
	hidden, hiddenErr := a.store.SessionHidden(ctx, tag)
	if err != nil || hiddenErr != nil || sess == nil || !hidden || sess.Kind != "" ||
		sess.Label != cronSessionLabel(jobID) {
		log.Printf("[cron-exec] job %s: ignoring session_tag %q, which is not this job's own log session", jobID, tag)
		return ""
	}
	return tag
}

// validateCronProjectDir refuses a project folder that is the file system
// root, the home folder, a folder that contains the home folder, or one that
// holds private data. Named work and tool folders inside the app data roots
// remain usable; their individual files still pass the upload privacy guard.
func validateCronProjectDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("project folder %q must be an absolute path", dir)
	}
	clean := resolvedFolder(dir)
	if clean == filepath.Dir(clean) {
		return fmt.Errorf("project folder %q is the file system root", dir)
	}
	// Fail closed: without the home folder the home and private checks cannot
	// run, so the folder is refused rather than trusted.
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return fmt.Errorf("project folder %q cannot be checked: the home folder is unknown", dir)
	}
	home = resolvedFolder(home)
	// Home and private checks run before the temp exemption, so a temp folder
	// that contains the home folder or a private folder is still refused.
	if pathWithinFolded(home, clean) {
		return fmt.Errorf("project folder %q is the home folder or contains it; choose a project folder", dir)
	}
	for _, data := range appDataFolders() {
		if pathWithinFolded(data, clean) {
			return fmt.Errorf("project folder %q is or holds private data (%s); choose a project folder", dir, filepath.Base(data))
		}
		if !pathWithinFolded(clean, data) {
			continue
		}
		// Containment is already established with the platform's case folding.
		// Keep that comparison when extracting a differently cased child path.
		relative := strings.TrimLeft(clean[len(data):], string(filepath.Separator))
		if !cronDataWorkFolder(relative) {
			return fmt.Errorf("project folder %q is inside private app data; choose a work or tool folder", dir)
		}
		if err := agenttools.CheckProtectedPaths("Write", map[string]any{"path": clean}, ""); err != nil {
			return fmt.Errorf("project folder %q is private: %w", dir, err)
		}
	}
	for _, private := range privateHomeFolders() {
		if pathWithinFolded(clean, private) || pathWithinFolded(private, clean) {
			return fmt.Errorf("project folder %q is or holds private data (%s); choose a project folder", dir, filepath.Base(private))
		}
	}
	// The system temp folder, where the app's own research schedules run, is
	// accepted here. It is no longer exempt from the home and private checks
	// above, so a temp folder that holds the home folder is still refused.
	return nil
}

// Keep broad private roots out of cron work folders while preserving existing
// agent-visible workspaces and bundled tools. File guards still apply within
// these folders, including to a secret placed next to an ordinary report.
func cronDataWorkFolder(relative string) bool {
	first, _, _ := strings.Cut(filepath.ToSlash(relative), "/")
	switch strings.ToLower(first) {
	case "scratch", "research", "workspaces", "skills", "skill-hub", "bin", "opencli", "bun",
		"chat", "chats", "automations", "handoff", "handoffs", "deliverables", "run":
		return true
	default:
		return false
	}
}

// appDataFolders are Isoform's and Yansu's data folders, resolved.
func appDataFolders() []string {
	var dirs []string
	if custom := strings.TrimSpace(os.Getenv("YANSU_AGENT_HOME")); custom != "" && filepath.IsAbs(custom) {
		dirs = append(dirs, resolvedFolder(custom))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, name := range []string{configDirName, ".isoform-agent", ".yansu-agent"} {
			dirs = append(dirs, resolvedFolder(filepath.Join(home, name)))
		}
	}
	return dirs
}

// privateHomeFolders hold the user's keys, credentials and app data
// (~/.ssh, ~/.gnupg, ~/.aws, ~/.config, ~/Library with its keychains).
func privateHomeFolders() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	var dirs []string
	for _, name := range []string{".ssh", ".gnupg", ".aws", ".config", "Library"} {
		dirs = append(dirs, resolvedFolder(filepath.Join(home, name)))
	}
	return dirs
}

// resolvedFolder resolves symlinks in the nearest existing ancestor of dir,
// so a folder that does not exist yet compares like one that does. It also
// maps the macOS Data-volume spelling of a folder onto its plain spelling
// (stripDataVolumeFirmlink), so every folder compares in one form.
func resolvedFolder(dir string) string {
	clean := filepath.Clean(dir)
	existing := clean
	var missing []string
	for {
		if resolved, err := filepath.EvalSymlinks(existing); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return stripDataVolumeFirmlink(resolved)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return stripDataVolumeFirmlink(clean)
		}
		missing = append(missing, filepath.Base(existing))
		existing = parent
	}
}

// pathWithinFolded reports whether path is root or inside it, ignoring case
// on the case-insensitive file systems of macOS and Windows.
func pathWithinFolded(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		path, root = strings.ToLower(path), strings.ToLower(root)
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// executePromptSync runs a prompt synchronously and returns the
// accumulated assistant content plus the first stream item sort_order.
// Unlike executePrompt (which is fire-and-forget for frontend streaming),
// this blocks until completion or context cancellation.
func (a *App) executePromptSync(
	ctx context.Context,
	tag, sessionID, workDir, enforcedWorkDir, prompt, model, notifyPlatform string,
	onLogsReady func(firstSortOrder int),
	handoffID string,
	authorityInitiativeID string,
) (string, int, []string, error) {
	a.mu.Lock()
	runner := a.runner
	readyCh := a.readyCh
	a.mu.Unlock()

	// Wait for agent readiness.
	if runner == nil {
		select {
		case <-readyCh:
			a.mu.Lock()
			runner = a.runner
			a.mu.Unlock()
			if runner == nil {
				return "", -1, nil, errAgentNotReady
			}
		case <-time.After(agentReadyTimeout):
			return "", -1, nil, errAgentNotReady
		case <-ctx.Done():
			return "", -1, nil, ctx.Err()
		}
	}

	// Compose prompt with cron execution context (suppresses clarifying questions).
	composedPrompt := projectcontext.WithPrompt(a.composeCronPrompt(prompt, enforcedWorkDir, notifyPlatform), workDir)
	var work *handoff.Entry
	if handoffID != "" && a.handoffStore != nil {
		if entry, err := a.handoffStore.Get(handoffID); err == nil && entry.Status == handoff.StatusInProgress {
			work = &entry
			composedPrompt += "\n\n" + buildHandoffResultInstructions(entry)
		}
	}
	authorityGrants := []authority.Grant{}
	if strings.TrimSpace(authorityInitiativeID) != "" {
		if a.activity == nil || a.activity.store == nil {
			return "", -1, nil, fmt.Errorf("load initiative authority: activity store is unavailable")
		}
		var err error
		authorityGrants, err = a.activity.store.ListActiveAuthorityGrants(
			authorityInitiativeID,
			time.Now(),
		)
		if err != nil {
			return "", -1, nil, fmt.Errorf("load initiative authority: %w", err)
		}
	}
	researchGrants := appResearchGrantsFrom(ctx)
	authorityGrants = append(authorityGrants, researchGrants...)
	authorityGrants = append(authorityGrants, playbookGrantsFrom(ctx)...)

	// Cron firing is a first-class agentloop entry point. Mint the trace_id
	// up front so the kickoff event and every downstream agentloop.* event
	// from this run share one correlation id, and emit
	// agentloop.cron.fired so the PostHog dashboard can distinguish cron-
	// initiated runs from chat / spotlight / connector runs at a glance.
	traceID := uuid.NewString()
	if a.telemetry != nil {
		a.telemetry.Capture("agentloop.cron.fired", map[string]any{
			"trace_id": traceID,
			"tag":      tag,
		})
	}

	// Ephemeral is intentionally left false: it flags "do not persist to
	// stream_items" (memory probes, activity triage). Cron runs *do*
	// persist the prompt and events to the hidden session for UI/log
	// playback — statelessness at the model layer is expressed by empty
	// SessionID + empty HistoryKey alone.
	//
	// AutoApproveTools is set because cron is fully unattended: nobody
	// is in front of the UI to answer a permission prompt. Authority still
	// runs before execution and fails closed without a matching scoped
	// grant. A require_approval decision is blocked instead of emitting a
	// permission_request; this flag only skips the legacy prompt after
	// authority has allowed the action.
	input := &agent.TaskInput{
		Prompt:                  composedPrompt,
		WorkDir:                 workDir,
		Model:                   model,
		SessionID:               sessionID,
		CredentialSessionID:     tag,
		TraceID:                 traceID,
		AutoApproveTools:        true,
		ProgrammaticToolCalling: true,
		BackendHint:             agent.BackendHintCron,
		Priority:                agent.PriorityBackground,
		// Every unattended run keeps the coarse external-write deny list,
		// with or without scoped grants: a grant names one resource, and the
		// deny list also fails closed on commands it cannot read.
		DenyExternalWrites:    true,
		AuthorityInitiativeID: authorityInitiativeID,
		AuthorityGrants:       authorityGrants,
		AuthorityEvaluator:    playbookEvaluatorFrom(ctx),
	}

	// Persist the user prompt as a stream item so the conversation
	// history includes the trigger for this run.
	// Capture the sort_order of this first item — it marks the start of
	// stream items belonging to this run.
	automationContext := ""
	if strings.TrimSpace(handoffID) != "" {
		if payload, err := json.Marshal(map[string]any{
			"handoff":   true,
			"handoffId": handoffID,
		}); err == nil {
			automationContext = string(payload)
		}
	}
	firstSortOrder, _ := a.PersistStreamItem(tag, StreamItemData{
		ID:                uuid.New().String(),
		Kind:              "user",
		Content:           prompt,
		AutomationContext: automationContext,
	})
	recordCronRunLogPosition(ctx, tag, firstSortOrder)
	if onLogsReady != nil {
		onLogsReady(firstSortOrder)
	}

	var result strings.Builder
	var lastError string
	var currentRunID string
	var runAssistantText strings.Builder
	// writtenFiles records the canonical paths this run read or changed through
	// its Read, Write, Edit and NotebookEdit tools. Only these files may later
	// be uploaded as a deliverable, even inside the work folder, so a run cannot
	// name a file it never touched, such as ~/.ssh/id_ed25519 or a stale file
	// left in the work folder. A path is pending from its tool_use until the
	// tool_result with the same ToolUseID reports that the tool ran and
	// succeeded: a denied or failed call never counts.
	writtenSet := map[string]struct{}{}
	pendingWrites := map[string]string{}

	_, err := runner.ExecuteTask(ctx, input, func(event *agent.StreamEvent) error {
		a.touchHandoffRun(work, time.Now())
		if err := recordCronRunRoute(ctx, event); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Permission is bypassed at the loop layer via
		// input.AutoApproveTools, so permission_request events should
		// never reach this callback for cron runs. Log loudly if one
		// somehow does — it would mean the flag wasn't honoured and
		// the run is about to hang.
		if event.Type == "permission_request" {
			log.Printf("[cron-exec] UNEXPECTED permission_request: tag=%s sessionID=%s tool=%s — AutoApproveTools should have short-circuited the gate", tag, event.SessionID, event.ToolName)
		}

		// Link agent session ID to the hidden session row.
		if event.Type == "run_created" && event.SessionID != "" {
			a.UpdateSessionClaudeID(tag, event.SessionID)
		}

		// Persist stream items for session continuity.
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
			// Each RunID gets its own assistant stream item. When the RunID
			// changes (e.g. after tool use), create a new item so the final
			// response text is not silently lost by updating a non-existent row.
			assistantID := event.RunID + "-assistant"
			if event.RunID != currentRunID {
				currentRunID = event.RunID
				runAssistantText.Reset()
				runAssistantText.WriteString(event.Content)
				a.PersistStreamItem(tag, StreamItemData{
					ID:      assistantID,
					Kind:    "assistant",
					Content: runAssistantText.String(),
				})
			} else {
				runAssistantText.WriteString(event.Content)
				a.UpdateStreamItemContent(assistantID, runAssistantText.String())
			}
		case "tool_use":
			if work != nil {
				result.Reset()
			}
			var toolInputStr string
			if event.ToolInput != nil {
				if b, err := json.Marshal(event.ToolInput); err == nil {
					toolInputStr = string(b)
				}
			}
			if canonical := touchedDeliverablePath(event.ToolName, event.ToolInput, workDir); canonical != "" && event.ToolUseID != "" {
				pendingWrites[event.ToolUseID] = canonical
			}
			a.PersistStreamItem(tag, StreamItemData{
				ID:        uuid.New().String(),
				Kind:      "tool_use",
				Content:   event.ToolName,
				ToolName:  event.ToolName,
				ToolInput: toolInputStr,
				ToolUseID: event.ToolUseID,
			})
		case "tool_result":
			if canonical, ok := pendingWrites[event.ToolUseID]; ok {
				delete(pendingWrites, event.ToolUseID)
				if event.ToolSucceeded {
					writtenSet[canonical] = struct{}{}
				}
			}
			a.PersistStreamItem(tag, StreamItemData{
				ID:        uuid.New().String(),
				Kind:      "tool_result",
				Content:   event.Content,
				ToolName:  event.ToolName,
				ToolUseID: event.ToolUseID,
			})
			if event.IsError {
				noteCronToolFailure(tag, event.ToolUseID)
			}
		case "error":
			lastError = event.Error
			log.Printf("[cron-exec] stream error tag=%s: %s", tag, event.Error)
		}

		// Emit events for live UI observation (if user has the job detail open).
		var toolInputRaw json.RawMessage
		if event.ToolInput != nil {
			if encoded, err := json.Marshal(event.ToolInput); err == nil {
				toolInputRaw = encoded
			}
		}
		a.emitAgentEvent(AgentEvent{
			Type:      event.Type,
			RunID:     event.RunID,
			SessionID: event.SessionID,
			Tag:       tag,
			Content:   event.Content,
			ToolName:  event.ToolName,
			ToolInput: toolInputRaw,
			ToolUseID: event.ToolUseID,
			Persisted: true,
			Error:     event.Error,
		})

		return nil
	})

	written := make([]string, 0, len(writtenSet))
	for p := range writtenSet {
		written = append(written, p)
	}

	if err != nil {
		if lastError != "" {
			return result.String(), firstSortOrder, written, fmt.Errorf("%s: %w", lastError, err)
		}
		return result.String(), firstSortOrder, written, err
	}
	if lastError != "" {
		return result.String(), firstSortOrder, written, fmt.Errorf("%s", lastError)
	}

	return result.String(), firstSortOrder, written, nil
}

// touchedDeliverablePath returns the canonical path a Read, Write, Edit or
// NotebookEdit tool call read or changed, or "" for any other tool or an
// unreadable path. The path is resolved against workDir and symlink-canonicalized so it
// compares equal to the candidate paths validateUploadDeliverablePath derives.
func touchedDeliverablePath(toolName string, toolInput any, workDir string) string {
	switch toolName {
	case "Read", "Write", "Edit", "NotebookEdit":
	default:
		return ""
	}
	input, ok := toolInput.(map[string]any)
	if !ok {
		return ""
	}
	raw, _ := input["path"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !filepath.IsAbs(raw) && workDir != "" {
		raw = filepath.Join(workDir, raw)
	}
	if resolved, err := resolvedExistingPath(raw); err == nil {
		return resolved
	}
	return filepath.Clean(raw)
}
