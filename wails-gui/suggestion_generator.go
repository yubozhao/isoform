package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"

	"yansu-agent/agent"

	"github.com/google/uuid"
)

// ── Constants ────────────────────────────────────────────────────────

const (
	// suggestionCheckInterval is the ticker cadence. The check is local; a
	// model call happens only when suggestionMinInterval has passed.
	suggestionCheckInterval = cadenceCheck

	// suggestionMinInterval is the floor between two successful generations:
	// suggestions are derived work and refresh once a day (derived_cadence.go).
	suggestionMinInterval = cadenceSuggestions

	// suggestionCount is the target chip count surfaced to the UI.
	suggestionCount = 5

	// suggestionMinAccepted is the minimum parsed-array length we will accept
	// from the model. Below this we abort the batch rather than ship a thin
	// result — the UI can fall back to the prior cached batch (or hide).
	suggestionMinAccepted = 3

	// suggestionMaxChipLen caps chip text length at render time.
	suggestionMaxChipLen = 80

	// suggestionGenerateTimeout bounds a single AI call so that a stuck peer
	// cannot hold runMu across multiple ticker periods.
	suggestionGenerateTimeout = 2 * time.Minute

	// suggestionRecentRuns is the number of recent activity pipeline runs
	// sampled for prompt context.
	suggestionRecentRuns = 12

	// suggestionRecentConvTitles caps how many recent conversation labels
	// feed the de-duplication hint in the prompt.
	suggestionRecentConvTitles = 10
)

// ── Types ────────────────────────────────────────────────────────────

// suggestionGenerationResponse is the expected JSON payload from the model.
type suggestionGenerationResponse struct {
	Suggestions []string `json:"suggestions"`
}

// SuggestionGenerator owns the background loop that refreshes the cached
// composer suggestion batch used by the empty-state composer chips.
//
// Lifecycle mirrors ActivityTriager: a single Run(ctx) goroutine, stopped
// via Stop() or ctx cancellation. Generation calls are serialized through
// runMu (TryLock), and a dedicated per-instance semaphore (cap=1) prevents
// piling work onto the shared activity/memory semaphore which is already
// heavily used.
type SuggestionGenerator struct {
	app    *App
	mu     sync.Mutex
	cancel context.CancelFunc

	// aiSem bounds concurrent AI calls issued by THIS generator. We keep
	// it local (not shared with activity/memory generators) because
	// suggestion generation is low-priority and we never want it to starve
	// those pipelines by occupying a shared slot.
	aiSem chan struct{}

	// runMu prevents a manual trigger from colliding with the ticker. Held
	// for the full duration of evaluate(), acquired via TryLock.
	runMu sync.Mutex

	// lastGeneratedAt is the wall-clock time of the last SUCCESSFUL
	// generation. Used to enforce suggestionMinInterval between batches.
	lastGeneratedAtMu sync.Mutex
	lastGeneratedAt   time.Time
}

// NewSuggestionGenerator constructs a generator bound to the given App.
// The returned instance has not yet started — call Run(ctx) to spin up the
// background loop.
func NewSuggestionGenerator(app *App) *SuggestionGenerator {
	return &SuggestionGenerator{
		app:   app,
		aiSem: make(chan struct{}, 1),
	}
}

// Run drives the generator loop. Blocks until ctx is cancelled.
//
// On startup we run one evaluate immediately — this is the "cold start"
// path: if we have no cached batch (or the cached batch predates the
// 60-minute floor), we generate one so the empty-state composer shows
// chips instead of skeletons on the very first session.
func (c *SuggestionGenerator) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()

	c.hydrateLastGeneratedFromDB(ctx)

	_ = c.evaluate(ctx)

	ticker := time.NewTicker(suggestionCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.evaluate(ctx)
		}
	}
}

// Stop cancels the generator loop. Safe to call multiple times.
func (c *SuggestionGenerator) Stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.mu.Unlock()
}

// errSuggestionBusy is returned when evaluate is called while another
// invocation is still holding runMu. The ticker path swallows it; manual
// triggers (future) can surface it as "already running" feedback.
var errSuggestionBusy = fmt.Errorf("suggestion generator already running")

// ForceGenerate is a DEBUG entry point that bypasses the 60-minute floor,
// the "new activity since last batch" check, and the SourceHash dedup, but
// keeps runMu TryLock and the aiSem so concurrent requests cannot pile up.
//
// TODO(debug-only): remove this method and its caller once the feature ships.
func (c *SuggestionGenerator) ForceGenerate(ctx context.Context) error {
	if c.app == nil || c.app.store == nil {
		return fmt.Errorf("generator not ready")
	}
	if !c.runMu.TryLock() {
		return errSuggestionBusy
	}
	defer c.runMu.Unlock()

	select {
	case c.aiSem <- struct{}{}:
	default:
		return fmt.Errorf("AI semaphore busy")
	}
	defer func() { <-c.aiSem }()

	genCtx, cancel := context.WithTimeout(ctx, suggestionGenerateTimeout)
	defer cancel()

	log.Printf("[suggestions] ForceGenerate: bypassing time/hash gates")
	return c.generate(genCtx, nil)
}

// evaluate decides whether to generate a new batch on this tick.
//
// Triggering conditions (OR'd together), subject to runMu TryLock and the
// 60-minute floor:
//
//  1. No cached batch exists at all → generate (cold start).
//  2. Cached batch is older than 60 minutes AND at least one new activity
//     pipeline run produced a "worth" outcome since the last batch → generate.
//
// If the user is idle (no new activity) we do NOT burn tokens, even
// once the 60-minute wall clock has elapsed.
func (c *SuggestionGenerator) evaluate(ctx context.Context) error {
	if c.app == nil || c.app.store == nil {
		return nil
	}
	if !c.runMu.TryLock() {
		log.Printf("[suggestions] evaluate already in progress; skipping")
		return errSuggestionBusy
	}
	defer c.runMu.Unlock()

	latest, err := c.app.store.LatestComposerSuggestions(ctx)
	if err != nil {
		log.Printf("[suggestions] failed to load latest batch: %s", activityPrivateLogField("error", err.Error()))
		return err
	}

	now := time.Now()
	if latest != nil {
		age := now.Sub(time.UnixMilli(latest.GeneratedAt))
		if age < suggestionMinInterval {
			return nil
		}
		if !c.hasNewActivitySince(latest.GeneratedAt) {
			return nil
		}
	}

	select {
	case c.aiSem <- struct{}{}:
	default:
		log.Printf("[suggestions] AI semaphore full; deferring generation")
		return nil
	}
	defer func() { <-c.aiSem }()

	genCtx, cancel := context.WithTimeout(ctx, suggestionGenerateTimeout)
	defer cancel()

	return c.generate(genCtx, latest)
}

// hasNewActivitySince reports whether any activity pipeline run has completed
// since the given timestamp. Per MVP, we treat any fresh triage run as a
// "something new happened" signal regardless of worth flags, so suggestions
// track the user's most recent activity even when it isn't triage-worthy.
func (c *SuggestionGenerator) hasNewActivitySince(unixMs int64) bool {
	if c.app == nil || c.app.activity == nil || c.app.activity.store == nil {
		return false
	}
	runs, err := c.app.activity.store.ListActivityPipelineRuns(suggestionRecentRuns)
	if err != nil {
		log.Printf("[suggestions] failed to list recent pipeline runs: %s", activityPrivateLogField("error", err.Error()))
		return false
	}
	for _, r := range runs {
		if r.CompletedAt > unixMs && r.AISummary != "" {
			return true
		}
	}
	return false
}

// generate builds the prompt, calls the model, validates the response, and
// on success persists the batch and broadcasts a `composer-suggestions:updated`
// event to the frontend.
//
// On any parse / validation failure we keep `previous` alive (do not write a
// new row, do not broadcast) so the UI continues to show the last good batch.
func (c *SuggestionGenerator) generate(ctx context.Context, previous *storage.ComposerSuggestionsRow) error {
	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()

	if runner == nil {
		return fmt.Errorf("runner not initialized")
	}

	log.Printf("[suggestions] generate: entry (hasPrevious=%t)", previous != nil)

	inputs := c.collectInputs(ctx)
	hash := hashInputs(inputs)

	// Record input sizes before the hash check so skipped generations remain
	// diagnosable. Content previews require the explicit transcript debug flag.
	logSuggestionInputs(inputs, hash)

	if previous != nil && previous.SourceHash == hash {
		log.Printf("[suggestions] source hash unchanged (%s); skipping regeneration", hash[:12])
		return nil
	}

	prompt := buildSuggestionPrompt(inputs)
	log.Printf("[suggestions][prompt] %s", activityPrivateLogField("prompt", prompt))

	var result strings.Builder
	log.Printf("[suggestions] generate ExecuteTask starting (model=%q)", c.app.lightTaskModel())
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		AutoApproveTools: true,
		Prompt:           prompt,
		Priority:         agent.PriorityBackground,
		Model:            c.app.lightTaskModel(),
		// Pure JSON-generation task: the prompt already contains the
		// full role/format spec, so suppress the runner's default
		// system prompt and tool list — otherwise a small/cheap model
		// drifts into generic "coding assistant" chit-chat instead of
		// emitting the strict JSON the parser expects.
		NoSystem:          true,
		NoTools:           true,
		Ephemeral:         true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if event.Type == "assistant" {
			result.WriteString(event.Content)
		}
		return nil
	})
	if err != nil {
		log.Printf("[suggestions] ExecuteTask failed: %s", activityPrivateLogField("error", err.Error()))
		return fmt.Errorf("execute suggestion generation: %w", err)
	}

	raw := stripCodeFences(strings.TrimSpace(result.String()))
	log.Printf("[suggestions][response] prompt: %s", activityPrivateLogField("prompt", prompt))
	log.Printf("[suggestions][response] raw: %s", activityPrivateLogField("response", raw))
	var resp suggestionGenerationResponse
	if err := unmarshalJSONBestEffort(raw, &resp); err != nil {
		log.Printf("[suggestions] parse failed (%s); keeping previous batch", activityPrivateLogField("error", err.Error()))
		return fmt.Errorf("parse suggestion response: %w", err)
	}

	cleaned := cleanSuggestions(resp.Suggestions)
	if len(cleaned) < suggestionMinAccepted {
		log.Printf("[suggestions] too few valid chips (got %d, need %d); keeping previous batch",
			len(cleaned), suggestionMinAccepted)
		return fmt.Errorf("suggestion response below minimum count")
	}
	if len(cleaned) > suggestionCount {
		cleaned = cleaned[:suggestionCount]
	}

	payload, err := json.Marshal(cleaned)
	if err != nil {
		return fmt.Errorf("marshal suggestions: %w", err)
	}

	row := storage.ComposerSuggestionsRow{
		ID:          uuid.NewString(),
		GeneratedAt: time.Now().UnixMilli(),
		Suggestions: string(payload),
		SourceHash:  hash,
	}
	if err := c.app.store.SaveComposerSuggestions(ctx, row); err != nil {
		return fmt.Errorf("save suggestions: %w", err)
	}

	// Keep the table small — 20 rows is plenty for debugging.
	if err := c.app.store.CleanupComposerSuggestions(ctx, 20); err != nil {
		log.Printf("[suggestions] cleanup warning: %s", activityPrivateLogField("error", err.Error()))
	}

	c.lastGeneratedAtMu.Lock()
	c.lastGeneratedAt = time.UnixMilli(row.GeneratedAt)
	c.lastGeneratedAtMu.Unlock()

	c.app.emitBrowserEvent("composer-suggestions:updated", map[string]any{
		"id":          row.ID,
		"generatedAt": row.GeneratedAt,
		"suggestions": cleaned,
	})

	log.Printf("[suggestions] published batch id=%s count=%d %s", row.ID, len(cleaned), activityPrivateLogField("chips", string(payload)))
	return nil
}

// logSuggestionInputs records source counts and sizes. Source-derived content
// is available only as bounded previews under the transcript debug flag.
func logSuggestionInputs(in suggestionPromptInputs, hash string) {
	log.Printf(
		"[suggestions][inputs] hash=%s %s activities=%d apps=%d convTitles=%d "+
			"todayAISummary=%dB yesterdayAISummary=%dB topApps=%d keyMoments=%d "+
			"topWindowTitles=%d topOCRKeywords=%d",
		hash[:12], activityPrivateLogField("user", in.UserName),
		len(in.RecentActivity), len(in.RecentAppNames), len(in.RecentConvTitles),
		len(in.TodayAISummary), len(in.YesterdayAISummary),
		len(in.TodayTopApps), len(in.TodayKeyMoments),
		len(in.TodayTopWindowTitles), len(in.TodayTopOCRKeywords),
	)
	for i, s := range in.RecentActivity {
		log.Printf("[suggestions][inputs]   activity[%d]: %s", i, activityPrivateLogField("activity", s))
	}
	if len(in.RecentAppNames) > 0 {
		log.Printf("[suggestions][inputs]   apps: %s", activityPrivateLogField("apps", strings.Join(in.RecentAppNames, ", ")))
	}
	for i, t := range in.RecentConvTitles {
		log.Printf("[suggestions][inputs]   convTitle[%d]: %s", i, activityPrivateLogField("title", t))
	}
	if in.TodayAISummary != "" {
		log.Printf("[suggestions][inputs]   todayAISummary: %s", activityPrivateLogField("summary", in.TodayAISummary))
	}
	if in.YesterdayAISummary != "" {
		log.Printf("[suggestions][inputs]   yesterdayAISummary: %s", activityPrivateLogField("summary", in.YesterdayAISummary))
	}
	if len(in.TodayTopApps) > 0 {
		log.Printf("[suggestions][inputs]   topApps: %s", activityPrivateLogField("apps", strings.Join(in.TodayTopApps, ", ")))
	}
	for i, m := range in.TodayKeyMoments {
		log.Printf("[suggestions][inputs]   keyMoment[%d]: %s", i, activityPrivateLogField("moment", m))
	}
	if len(in.TodayTopWindowTitles) > 0 {
		log.Printf("[suggestions][inputs]   topWindowTitles: %s", activityPrivateLogField("titles", strings.Join(in.TodayTopWindowTitles, " | ")))
	}
	if len(in.TodayTopOCRKeywords) > 0 {
		log.Printf("[suggestions][inputs]   topOCRKeywords: %s", activityPrivateLogField("keywords", strings.Join(in.TodayTopOCRKeywords, ", ")))
	}
	if len(in.RecentActivity) == 0 && in.TodayAISummary == "" && in.YesterdayAISummary == "" {
		log.Printf("[suggestions][inputs] WARNING: no activity summaries found — chips will be generic")
	}
}

// ── Prompt inputs ────────────────────────────────────────────────────

// suggestionPromptInputs is the bundle of distilled signals fed to the model.
// MVP: activity-only. Memory/knowledge wiring is intentionally left commented
// out below so we can re-enable it later without re-deriving the shape.
type suggestionPromptInputs struct {
	UserName         string
	RecentActivity   []string // AISummary strings from recent pipeline runs (most-recent-first)
	RecentAppNames   []string // deduped app names from recent activity
	RecentConvTitles []string // labels of the most recent chat sessions

	// Layer-3 daily-level signals, derived from GetDailySummary/DailySummaryData.
	// These are the most distilled view of the user's day: a cached natural-
	// language summary plus structured aggregates (top apps with time on task,
	// detected key moments, frequent window titles, OCR keywords). All fields
	// are best-effort — a missing daily summary is expected on a fresh day
	// before the async summary job has run, and the prompt degrades gracefully.
	TodayAISummary       string
	YesterdayAISummary   string   // fallback when today's cached summary isn't generated yet
	TodayTopApps         []string // "AppName (Xm Ys)" — capped
	TodayKeyMoments      []string // "Title — Description" — capped
	TodayTopWindowTitles []string // capped
	TodayTopOCRKeywords  []string // capped

	// MemorySummary string // MVP: disabled — re-enable when memory/knowledge signals return
}

func (c *SuggestionGenerator) collectInputs(ctx context.Context) suggestionPromptInputs {
	// TODO(debug-only): entry trace showing which subsystems are wired up.
	// Verifies c.app.activity isn't nil from an init race before we bail
	// silently on the nil check below.
	log.Printf("[suggestions][collect] entry app=%t activity=%t activityStore=%t store=%t",
		c.app != nil,
		c.app != nil && c.app.activity != nil,
		c.app != nil && c.app.activity != nil && c.app.activity.store != nil,
		c.app != nil && c.app.store != nil,
	)

	inputs := suggestionPromptInputs{
		UserName: c.app.getUserDisplayName(),
		// MemorySummary: loadExistingMemorySummary(), // MVP: disabled
	}

	// ── Layer 1: per-triage timeline summaries + app list (activity_pipeline_runs) ──
	if c.app.activity != nil && c.app.activity.store != nil {
		runs, err := c.app.activity.store.ListActivityPipelineRuns(suggestionRecentRuns)
		if err != nil {
			log.Printf("[suggestions][collect] ListActivityPipelineRuns failed: %s", activityPrivateLogField("error", err.Error()))
		} else {
			log.Printf("[suggestions][collect] layer1: pipelineRuns=%d (requested %d)",
				len(runs), suggestionRecentRuns)
			seenApps := map[string]bool{}
			for _, r := range runs {
				if !c.pipelineRunAllowsModelRead(r) {
					continue
				}
				if r.AISummary != "" {
					inputs.RecentActivity = append(inputs.RecentActivity, r.AISummary)
				}
				if r.AppNames != "" {
					var apps []string
					if err := json.Unmarshal([]byte(r.AppNames), &apps); err == nil {
						for _, a := range pii.FilterSensitiveAppNames(apps) {
							if a == "" || seenApps[a] {
								continue
							}
							seenApps[a] = true
							inputs.RecentAppNames = append(inputs.RecentAppNames, a)
						}
					}
				}
			}
		}
	} else {
		log.Printf("[suggestions][collect] layer1 SKIPPED: activity or activity.store is nil")
	}

	// ── Layer 3: daily structured aggregates + cached natural-language summary ──
	// Pulled via GetDailySummary (which reads from activity_sessions / events /
	// ocr_frames and any cached AI summary). Best-effort: misses are logged but
	// never fatal, and the prompt still works with just layer-1 data.
	c.collectDailySummaryInputs(&inputs)

	// ── Fallback: if layers 1 + 3 yielded zero activity signal (e.g. first-run,
	//    or today has no sessions yet, or triage/summary jobs haven't run), pull
	//    the most recent N sessions directly from activity_sessions regardless
	//    of date. This keeps chips grounded in something concrete even before
	//    any structured pipeline output exists.
	if len(inputs.RecentActivity) == 0 && len(inputs.RecentAppNames) == 0 &&
		inputs.TodayAISummary == "" && inputs.YesterdayAISummary == "" &&
		len(inputs.TodayTopApps) == 0 {
		c.collectRecentSessionsFallback(&inputs)
	}

	if grouped, err := c.app.store.LoadAllSessions(ctx); err == nil {
		var flat []storage.SessionRow
		for _, rows := range grouped {
			flat = append(flat, rows...)
		}
		// Sort by updated_at desc, take top N labels.
		for i := 0; i < len(flat); i++ {
			for j := i + 1; j < len(flat); j++ {
				if flat[j].UpdatedAt > flat[i].UpdatedAt {
					flat[i], flat[j] = flat[j], flat[i]
				}
			}
		}
		for i := 0; i < len(flat) && len(inputs.RecentConvTitles) < suggestionRecentConvTitles; i++ {
			label := strings.TrimSpace(flat[i].Label)
			if label == "" {
				continue
			}
			inputs.RecentConvTitles = append(inputs.RecentConvTitles, label)
		}
	}

	return inputs
}

// A pipeline summary can mix multiple sessions. Drop the entire derived
// result if any source is protected, missing or no longer permitted.
func (c *SuggestionGenerator) pipelineRunAllowsModelRead(run ActivityPipelineRunRow) bool {
	if c.app == nil || c.app.activity == nil || c.app.activity.store == nil || c.app.IsClosedEyesModeEnabled() {
		return false
	}
	var apps []string
	if run.AppNames != "" && json.Unmarshal([]byte(run.AppNames), &apps) != nil {
		return false
	}
	for _, app := range apps {
		if activitySourceAppIsSensitive(app, app) {
			return false
		}
	}
	var ids []string
	if json.Unmarshal([]byte(run.SessionIDs), &ids) != nil || len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		session, err := c.app.activity.store.GetSessionByID(id)
		if err != nil || session == nil || !c.app.activitySessionAllowsModelRead(c.app.activity.store, *session) {
			return false
		}
	}
	return true
}

// collectDailySummaryInputs hydrates the Layer-3 fields on inputs from today's
// DailySummaryData (app breakdown, key moments, window titles, OCR keywords,
// cached AI summary). If today's AI summary hasn't been generated yet, falls
// back to yesterday's cached narrative so the prompt still has distilled
// context on a fresh day.
//
// All failures are logged and swallowed — layer-1 activity timelines remain
// the base signal even if this entire step is skipped.
func (c *SuggestionGenerator) collectDailySummaryInputs(inputs *suggestionPromptInputs) {
	if c.app == nil || c.app.activity == nil || c.app.activity.store == nil {
		log.Printf("[suggestions][collect] layer3 SKIPPED: app/activity/store nil (app=%t activity=%t store=%t)",
			c.app != nil,
			c.app != nil && c.app.activity != nil,
			c.app != nil && c.app.activity != nil && c.app.activity.store != nil)
		return
	}

	const (
		maxTopApps         = 6
		maxTopWindowTitles = 8
		maxTopOCRKeywords  = 10
	)

	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	log.Printf("[suggestions][collect] layer3: calling GetDailySummary(%s)", today)
	daily, err := c.app.GetDailySummary(today)
	if err != nil {
		log.Printf("[suggestions][collect] GetDailySummary(%s) failed: %s", today, activityPrivateLogField("error", err.Error()))
	} else if daily == nil {
		log.Printf("[suggestions][collect] GetDailySummary(%s) returned nil", today)
	} else {
		log.Printf("[suggestions][collect] layer3 daily: aiSummary=%dB appBreakdown=%d topWindowTitles=%d topOCRKeywords=%d sessions=%d",
			len(daily.AISummary), len(daily.AppBreakdown),
			len(daily.TopWindowTitles), len(daily.TopOCRKeywords), daily.SessionCount)
		inputs.TodayAISummary = redactActivityModelText(strings.TrimSpace(daily.AISummary))

		for _, app := range daily.AppBreakdown {
			if app.AppName == "" {
				continue
			}
			inputs.TodayTopApps = append(inputs.TodayTopApps,
				fmt.Sprintf("%s (%s)", app.AppName, formatDurationMs(app.ActiveMs)))
			if len(inputs.TodayTopApps) >= maxTopApps {
				break
			}
		}

		inputs.TodayTopWindowTitles = capStrings(daily.TopWindowTitles, maxTopWindowTitles)
		inputs.TodayTopOCRKeywords = capStrings(daily.TopOCRKeywords, maxTopOCRKeywords)
	}

	// Fall back to yesterday's cached AI narrative only when today's is empty.
	// (Today's structured aggregates are still valuable even without today's
	// AI summary — we keep those regardless.)
	if inputs.TodayAISummary == "" {
		summary, _, _, _, found := c.app.cachedActivitySummaryForRead(yesterday, "daily", c.app.currentUILanguage())
		log.Printf("[suggestions][collect] layer3 fallback GetCachedSummary(%s, daily): found=%t len=%dB",
			yesterday, found, len(summary))
		if found {
			inputs.YesterdayAISummary = redactActivityModelText(strings.TrimSpace(summary))
		}
	}
}

// Cached day summaries do not retain per-sentence provenance. If any original
// session is now protected, none of that day's derived text may enter a prompt.
func (c *SuggestionGenerator) dayAllowsSuggestionContext(date string) bool {
	if c.app == nil {
		return false
	}
	return c.app.activityDayAllowsModelRead(date)
}

// collectRecentSessionsFallback hydrates RecentActivity + RecentAppNames from
// the most recent N activity_sessions rows, ignoring date bounds. Activated
// only when layers 1 and 3 produce nothing — typically first-run, a freshly
// installed build, or the rare window before any triage/daily-summary job has
// had a chance to execute.
//
// Each emitted activity line is a terse human-readable trace:
//
//	"session 14:02–14:37 (35m) using Cursor, Slack, Chrome · 812 events · dismissed"
//
// This is weaker signal than a real AISummary, but good enough to ground
// suggestions in recent app usage when nothing better is available.
func (c *SuggestionGenerator) collectRecentSessionsFallback(inputs *suggestionPromptInputs) {
	if c.app == nil || c.app.activity == nil || c.app.activity.store == nil {
		return
	}
	const fallbackSessionLimit = 20

	sessions, err := c.app.activity.store.GetFilteredSessions(fallbackSessionLimit, 0, 0, 0, "")
	if err != nil {
		log.Printf("[suggestions][collect] fallback GetFilteredSessions failed: %s", activityPrivateLogField("error", err.Error()))
		return
	}
	log.Printf("[suggestions][collect] fallback: found %d recent sessions (no date filter)", len(sessions))
	if len(sessions) == 0 {
		return
	}

	// Index pipeline runs by session id. Pull a larger pool than Layer 1
	// (fallbackPipelineRunsLimit) so we have enough to cover all 20 fallback
	// sessions even when each session fans out into multiple runs. Runs are
	// returned newest-first; keep the first (most recent) match per session.
	runBySession := map[string]*ActivityPipelineRunRow{}
	const fallbackPipelineRunsLimit = 100
	if runs, err := c.app.activity.store.ListActivityPipelineRuns(fallbackPipelineRunsLimit); err != nil {
		log.Printf("[suggestions][collect] fallback ListActivityPipelineRuns failed: %s", activityPrivateLogField("error", err.Error()))
	} else {
		for i := range runs {
			r := &runs[i]
			if !c.pipelineRunAllowsModelRead(*r) {
				continue
			}
			if r.SessionIDs == "" {
				continue
			}
			var ids []string
			if err := json.Unmarshal([]byte(r.SessionIDs), &ids); err != nil {
				continue
			}
			for _, id := range ids {
				if _, exists := runBySession[id]; !exists && id != "" {
					runBySession[id] = r
				}
			}
		}
		log.Printf("[suggestions][collect] fallback: indexed %d pipeline runs across %d sessions",
			len(runs), len(runBySession))
	}

	seenApps := map[string]bool{}
	for _, s := range sessions {
		if !c.app.activitySessionAllowsModelRead(c.app.activity.store, s) {
			continue
		}
		// Protected sessions never reach here — GetFilteredSessions excludes
		// them at the store layer.
		var apps []string
		if s.AppNames != "" {
			_ = json.Unmarshal([]byte(s.AppNames), &apps)
		}
		// Filter ONCE and use the filtered list everywhere below — the
		// session line formatter previously received the raw list.
		apps = pii.FilterSensitiveAppNames(apps)

		for _, a := range apps {
			a = strings.TrimSpace(a)
			if a == "" || seenApps[a] {
				continue
			}
			seenApps[a] = true
			inputs.RecentAppNames = append(inputs.RecentAppNames, a)
		}

		line := formatFallbackSessionLine(s, apps)
		if run, ok := runBySession[s.ID]; ok {
			if triage := formatTriageSnippet(run); triage != "" {
				line += " · " + triage
			}
		}
		inputs.RecentActivity = append(inputs.RecentActivity, line)
	}
}

// formatTriageSnippet distills an ActivityPipelineRunRow into a short title +
// description trailer suitable for appending to a fallback session line. We
// prefer the structured triage Title/Description (parsed from AIResponseRaw)
// because they are explicit and short; AISummary is the narrative fallback
// when triage didn't emit a title. Returns "" when nothing useful is found.
func formatTriageSnippet(run *ActivityPipelineRunRow) string {
	if run == nil {
		return ""
	}
	if raw := strings.TrimSpace(run.AIResponseRaw); raw != "" {
		var tr activityTriageResponse
		if err := json.Unmarshal([]byte(raw), &tr); err == nil {
			title := strings.TrimSpace(tr.Title)
			desc := strings.TrimSpace(tr.Description)
			switch {
			case title != "" && desc != "":
				return fmt.Sprintf("%q — %s", title, truncateForPrompt(desc, 160))
			case title != "":
				return fmt.Sprintf("%q", title)
			case desc != "":
				return truncateForPrompt(desc, 200)
			}
		}
	}
	if summary := strings.TrimSpace(run.AISummary); summary != "" {
		return truncateForPrompt(summary, 200)
	}
	return ""
}

// formatFallbackSessionLine renders one session row as a prompt-friendly line.
func formatFallbackSessionLine(s ActivitySessionRow, apps []string) string {
	start := time.UnixMilli(s.StartTime).Local()
	end := time.UnixMilli(s.EndTime).Local()
	dur := time.Duration(s.EndTime-s.StartTime) * time.Millisecond
	if dur < 0 {
		dur = 0
	}

	appList := strings.Join(apps, ", ")
	if appList == "" {
		appList = "unknown app"
	}

	line := fmt.Sprintf("session %s–%s (%s) using %s · %d events",
		start.Format("15:04"), end.Format("15:04"),
		dur.Round(time.Second), appList, s.EventCount)
	if s.TriageStatus != "" && s.TriageStatus != "pending" {
		line += " · " + s.TriageStatus
	}
	if s.IsMeeting {
		line += " · meeting"
	}
	return line
}

// capStrings returns a trimmed copy of s limited to the first n non-empty
// entries, preserving order.
func capStrings(s []string, n int) []string {
	if len(s) == 0 || n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	for _, v := range s {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
		if len(out) >= n {
			break
		}
	}
	return out
}

// hashInputs is a stable fingerprint over the prompt inputs, used to skip
// regeneration when nothing has changed.
func hashInputs(in suggestionPromptInputs) string {
	h := sha256.New()
	for _, s := range in.RecentActivity {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, s := range in.RecentAppNames {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, s := range in.RecentConvTitles {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	h.Write([]byte(in.TodayAISummary))
	h.Write([]byte{0})
	h.Write([]byte(in.YesterdayAISummary))
	h.Write([]byte{0})
	for _, s := range in.TodayTopApps {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, s := range in.TodayKeyMoments {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, s := range in.TodayTopWindowTitles {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, s := range in.TodayTopOCRKeywords {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cleanSuggestions trims whitespace, drops empties, enforces the length cap,
// and de-duplicates case-insensitively while preserving order.
func cleanSuggestions(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len([]rune(s)) > suggestionMaxChipLen {
			continue
		}
		key := strings.ToLower(s)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// hydrateLastGeneratedFromDB seeds lastGeneratedAt from the most recent
// persisted batch so that a restart within the 60-minute floor does not
// immediately regenerate.
func (c *SuggestionGenerator) hydrateLastGeneratedFromDB(ctx context.Context) {
	if c.app == nil || c.app.store == nil {
		return
	}
	latest, err := c.app.store.LatestComposerSuggestions(ctx)
	if err != nil || latest == nil {
		return
	}
	c.lastGeneratedAtMu.Lock()
	c.lastGeneratedAt = time.UnixMilli(latest.GeneratedAt)
	c.lastGeneratedAtMu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────
//
// buildSuggestionPrompt is the one meaningful piece of business logic in this
// file: it decides what the suggestion chips actually say.
//
// TODO(user): fill in the system and user prompt bodies below. The LLM
// response must be STRICT JSON of the form:
//
//	{"suggestions": ["...", "..."]}
//
// Each string should be a short, first-person prompt the user could send
// right now based on the provided context. Hard constraints the parser
// enforces downstream: array length ≥ 3 (see suggestionMinAccepted),
// each chip ≤ 80 runes (see suggestionMaxChipLen), no duplicates
// (case-insensitive). The prompt itself should ask for exactly 5.
//
// ─────────────────────────────────────────────────────────────────────

func redactSuggestionInputs(in suggestionPromptInputs) suggestionPromptInputs {
	for _, field := range []*string{&in.UserName, &in.TodayAISummary, &in.YesterdayAISummary} {
		*field, _ = pii.RedactText(*field)
	}
	for _, field := range []*[]string{&in.RecentActivity, &in.RecentAppNames, &in.RecentConvTitles,
		&in.TodayTopApps, &in.TodayKeyMoments, &in.TodayTopWindowTitles, &in.TodayTopOCRKeywords} {
		out := make([]string, 0, len(*field))
		for _, value := range *field {
			redacted, _ := pii.RedactText(value)
			out = append(out, redacted)
		}
		*field = out
	}
	return in
}

func buildSuggestionPrompt(in suggestionPromptInputs) string {
	// TODO(user): replace the placeholder body below with the real prompt.
	// MVP inputs are activity-only (RecentActivity + RecentAppNames), plus
	// RecentConvTitles for de-duplication. Reasonable structure:
	//   (1) role/persona — "you generate composer suggestion chips for an
	//       intelligent personal assistant";
	//   (2) strict output contract — JSON {"suggestions": [5 strings]},
	//       nothing else;
	//   (3) tone/hard rules — first-person, ≤80 chars, no duplicates with
	//       RecentConvTitles, no preamble, no questions;
	//   (4) context — RecentActivity as bullets, RecentAppNames as comma
	//       list, RecentConvTitles as the "already discussed" list.
	prompt, _ := pii.RedactText(suggestionSystemPromptPlaceholder + "\n\n" + renderSuggestionUserPrompt(in))
	return prompt
}

// suggestionSystemPromptPlaceholder is a minimal draft that keeps the feature
// wired end-to-end until the real prompt lands. Replace when filling in
// buildSuggestionPrompt.
const suggestionSystemPromptPlaceholder = `You generate short, first-person prompt chips for the empty-state composer of a personal AI assistant.

Output STRICT JSON, nothing else:
{"suggestions": ["...", "...", "...", "...", "..."]}

Rules:
- Exactly 5 entries.
- Each entry is what the user might type to their assistant right now, written in the user's own voice (first person, imperative or declarative).
- Each entry is ≤ 80 characters.
- No numbering, no bullets, no quotes inside entries, no trailing punctuation beyond a single period or question mark.
- Avoid re-suggesting topics the user already has open conversations about.
- Ground suggestions in the provided context; if the context is sparse, return generic but useful assistant prompts.
- Do NOT ask the user questions. Do NOT add preamble or commentary around the JSON.`

func renderSuggestionUserPrompt(in suggestionPromptInputs) string {
	in = redactSuggestionInputs(in)
	var b strings.Builder
	if in.UserName != "" {
		fmt.Fprintf(&b, "User: %s\n\n", in.UserName)
	}

	// ── Layer-3 distilled-day context (highest-signal, goes first) ──
	//
	// Order matters: the model pays most attention to the top of the prompt, so
	// we lead with the cached natural-language day summary (most condensed), then
	// the structured aggregates (top apps + key moments + window titles + OCR
	// keywords). Today's narrative takes priority; yesterday's is only used as
	// fallback before today's async summary job has run.
	if in.TodayAISummary != "" {
		b.WriteString("Today so far (narrative summary):\n")
		fmt.Fprintf(&b, "%s\n\n", truncateForPrompt(in.TodayAISummary, 1500))
	} else if in.YesterdayAISummary != "" {
		b.WriteString("Yesterday (narrative summary, today's not generated yet):\n")
		fmt.Fprintf(&b, "%s\n\n", truncateForPrompt(in.YesterdayAISummary, 1500))
	}
	if len(in.TodayTopApps) > 0 {
		fmt.Fprintf(&b, "Today's top apps by time on task: %s\n\n",
			strings.Join(in.TodayTopApps, ", "))
	}
	if len(in.TodayKeyMoments) > 0 {
		b.WriteString("Today's key moments:\n")
		for _, m := range in.TodayKeyMoments {
			fmt.Fprintf(&b, "- %s\n", truncateForPrompt(m, 200))
		}
		b.WriteString("\n")
	}
	if len(in.TodayTopWindowTitles) > 0 {
		fmt.Fprintf(&b, "Frequent window titles today: %s\n\n",
			strings.Join(in.TodayTopWindowTitles, " | "))
	}
	if len(in.TodayTopOCRKeywords) > 0 {
		fmt.Fprintf(&b, "Frequent on-screen keywords today: %s\n\n",
			strings.Join(in.TodayTopOCRKeywords, ", "))
	}

	// ── Layer-1 per-triage timeline backup + recent apps ──
	//
	// Kept as supplementary evidence so the model can ground suggestions in
	// specific recent moments, not just the day-level distillation.
	if len(in.RecentActivity) > 0 {
		b.WriteString("Recent distilled activity (per-triage timelines):\n")
		for _, s := range in.RecentActivity {
			// 500 runes accommodates enriched fallback lines that bundle
			// session time/apps + triage Title/Description + activity metadata
			// in a single string. Pure Layer-1 AISummaries are typically
			// shorter than this so the cap only bites on the fallback path.
			fmt.Fprintf(&b, "- %s\n", truncateForPrompt(s, 500))
		}
		b.WriteString("\n")
	}
	if len(in.RecentAppNames) > 0 {
		fmt.Fprintf(&b, "Recent apps: %s\n\n", strings.Join(in.RecentAppNames, ", "))
	}

	if len(in.RecentConvTitles) > 0 {
		b.WriteString("Existing conversations (avoid duplicating these):\n")
		for _, t := range in.RecentConvTitles {
			fmt.Fprintf(&b, "- %s\n", t)
		}
		b.WriteString("\n")
	}
	// MVP: memory summary section intentionally disabled — re-enable alongside
	// the MemorySummary field once we bring memory/knowledge signals back in.
	b.WriteString(`Return exactly 5 short, first-person prompts as strict JSON: {"suggestions": [...]}.`)
	return b.String()
}

func truncateForPrompt(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
