package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
	"yansu-agent/agent"
)

// ── Constants ────────────────────────────────────────────────────────

const (
	memoryCheckInterval  = cadenceSlackMemory
	memoryBaseDebounce   = 20 * time.Second
	memoryCooldown       = 5 * time.Minute
	memoryMinRounds      = 2
	memoryRoundsEscalate = 3

	// maxProcessedConvIDs caps the per-source processed-conversation maps.
	// When a map exceeds this size, it is reset (all IDs re-derived from DB
	// on the next evaluate cycle). Prevents unbounded growth over long
	// sessions — each string key is ~60 bytes, so 10k entries ≈ 600 KB.
	maxProcessedConvIDs = 10_000
)

// ── Response types ───────────────────────────────────────────────────

// memoryTriageResponse is the AI triage result for memory extraction.
type memoryTriageResponse struct {
	Worth          bool   `json:"worth"`
	WorthKnowledge bool   `json:"worthKnowledge"`
	Reason         string `json:"reason"`
}

// mcMemoryGenerationResponse is the AI-generated memory from the MemoryGenerator pipeline.
type mcMemoryGenerationResponse struct {
	// Assigned by code from exact model inputs; never accepted from JSON.
	inputProvenance *memoryReadProvenance
	inputPromptSafe bool
	Action          string   `json:"action"`             // "create", "update", or "skip"
	UpdateID        string   `json:"updateId,omitempty"` // filename (without .md) of existing entry to update
	Title           string   `json:"title"`
	MemoryPoints    []string `json:"memoryPoints"`
	Dimension       string   `json:"dimension"` // user_insight | topic_highlight | deep_user_model | agentic_memory
}

type memoryPointsRewriteResponse struct {
	MemoryPoints []string `json:"memoryPoints"`
}

// ── Memory source enum ───────────────────────────────────────────────

type memorySource string

const (
	memorySourceConversation memorySource = "conversation"
	memorySourceSlack        memorySource = "slack"
	memorySourceDiscord      memorySource = "discord"
	memorySourceFeishu       memorySource = "feishu"
	memorySourceTelegram     memorySource = "telegram"
	memorySourceWeChat       memorySource = "wechat"
	memorySourceTeams        memorySource = "teams"
	memorySourceRingCentral  memorySource = "ringcentral"
	memorySourceGmail        memorySource = "gmail"
)

// memPipelineResult is the outcome of a single runMemoryPipeline call.
type memPipelineResult int

const (
	memPipelineSuccess  memPipelineResult = iota // extracted or updated at least one memory point
	memPipelineSkipped                           // triage said not worth, dedup skip, or too few messages
	memPipelineFailed                            // model or I/O error; do not mark processed
	memPipelineCanceled                          // ctx canceled; do not mark processed
)

type memoryPipelineMaterialization struct {
	ArtifactKind string
	ArtifactID   string
	Action       string
	Title        string
	Dimension    string
}

type memoryPipelineOutcome struct {
	Result           memPipelineResult
	Materializations []memoryPipelineMaterialization
}

// ── Conversation debounce session ────────────────────────────────────

type memoryConvSession struct {
	source          memorySource
	sourceID        string
	mu              sync.Mutex
	lastSummary     string
	debounceTimer   *time.Timer
	lastTriageTime  time.Time
	roundAtLastTrig int
}

// ── MemoryGenerator ──────────────────────────────────────────────

// MemoryGenerator evaluates data sources and generates memory entries
// via a two-phase pipeline: Haiku triage → strong model generation.
// Generated memory files are saved to ~/.yansu-agent/memory/ and the
// existing MemoryStore (FTS5+vector) picks them up via its sync loop.
type MemoryGenerator struct {
	app    *App
	mu     sync.Mutex
	cancel context.CancelFunc

	// evaluationMu serializes evaluateSlack (ticker), evaluateSlackNow, and evaluateFeishuNow
	// so the same conversation cannot be picked up by two concurrent callers.
	evaluationMu sync.Mutex

	// Conversation sessions (debounced)
	convSessions map[string]*memoryConvSession

	// Track processed conversations per source
	processedSlackConvs    map[string]bool
	processedFeishuConvs   map[string]bool
	processedDiscordConvs  map[string]bool
	processedTelegramConvs map[string]bool
}

// NewMemoryGenerator creates a new memory generator.
func NewMemoryGenerator(app *App) *MemoryGenerator {
	return &MemoryGenerator{
		app:                    app,
		convSessions:           make(map[string]*memoryConvSession),
		processedSlackConvs:    make(map[string]bool),
		processedFeishuConvs:   make(map[string]bool),
		processedDiscordConvs:  make(map[string]bool),
		processedTelegramConvs: make(map[string]bool),
	}
}

// Run starts the periodic evaluation loop. Blocks until ctx is cancelled.
func (mc *MemoryGenerator) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	mc.mu.Lock()
	mc.cancel = cancel
	mc.mu.Unlock()

	// Load previously processed IDs
	mc.loadProcessedState()

	// NOTE: Activity memory is handled by ActivityTriager (same triage → generate flow).
	// MemoryGenerator handles Conversation (debounced) and Slack (periodic).
	mc.evaluateSlack(ctx)

	ticker := time.NewTicker(memoryCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mc.evictProcessedMapsIfNeeded()
			mc.evaluateSlack(ctx)
		}
	}
}

// evictProcessedMapsIfNeeded resets processed-ID maps that have grown
// past maxProcessedConvIDs. The IDs will be re-derived from the database
// on the next loadProcessedState call, so no work is lost.
func (mc *MemoryGenerator) evictProcessedMapsIfNeeded() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	evicted := false
	if len(mc.processedSlackConvs) > maxProcessedConvIDs {
		mc.processedSlackConvs = make(map[string]bool)
		evicted = true
	}
	if len(mc.processedFeishuConvs) > maxProcessedConvIDs {
		mc.processedFeishuConvs = make(map[string]bool)
		evicted = true
	}
	if len(mc.processedDiscordConvs) > maxProcessedConvIDs {
		mc.processedDiscordConvs = make(map[string]bool)
		evicted = true
	}
	if len(mc.processedTelegramConvs) > maxProcessedConvIDs {
		mc.processedTelegramConvs = make(map[string]bool)
		evicted = true
	}
	if len(mc.convSessions) > maxProcessedConvIDs {
		mc.convSessions = make(map[string]*memoryConvSession)
		evicted = true
	}
	if evicted {
		log.Printf("[memory] evicted oversized processed maps, reloading from DB")
		go mc.loadProcessedState()
	}
}

// Stop cancels the memory generator loop.
func (mc *MemoryGenerator) Stop() {
	mc.mu.Lock()
	if mc.cancel != nil {
		mc.cancel()
		mc.cancel = nil
	}
	mc.mu.Unlock()
}

// CleanupSession removes the conversation session entry for the given session,
// freeing debounce timers and accumulated state.
func (mc *MemoryGenerator) CleanupSession(sessionLocalId string) {
	mc.mu.Lock()
	cs, ok := mc.convSessions[sessionLocalId]
	if ok {
		delete(mc.convSessions, sessionLocalId)
	}
	mc.mu.Unlock()

	if ok && cs != nil {
		cs.mu.Lock()
		if cs.debounceTimer != nil {
			cs.debounceTimer.Stop()
		}
		cs.mu.Unlock()
	}
}

// ── Conversation trigger ─────────────────────────────

// TriggerMemory is called by the frontend after each AI response.
// It debounces and then runs the memory extraction pipeline on the conversation.
func (a *App) TriggerMemory(sessionLocalId, summary string, roundCount int) {
	a.triggerMemory(memorySourceConversation, sessionLocalId, summary, roundCount)
}

// Connector callers supply their transport source; owner pairing does not
// change messaging data into a desktop-authored conversation.
func (a *App) triggerMemory(source memorySource, sessionLocalId, summary string, roundCount int) {
	mc := a.getMemoryGenerator()
	if mc == nil {
		return
	}

	mc.mu.Lock()
	cs, ok := mc.convSessions[sessionLocalId]
	if !ok {
		cs = &memoryConvSession{source: source, sourceID: sessionLocalId}
		mc.convSessions[sessionLocalId] = cs
	}
	mc.mu.Unlock()

	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.source != source {
		cs.source = "mixed-connector"
	}
	cs.lastSummary = summary

	if roundCount < memoryMinRounds {
		return
	}

	// Cooldown check
	if !cs.lastTriageTime.IsZero() {
		elapsed := time.Since(cs.lastTriageTime)
		newRounds := roundCount - cs.roundAtLastTrig
		if elapsed < memoryCooldown && newRounds < memoryRoundsEscalate {
			return
		}
	}

	// Capture the originating chat trace_id SYNCHRONOUSLY at TriggerMemory
	// entry, not inside the debounce closure. The closure fires
	// memoryBaseDebounce (20s) later — by which time streaming.go's
	// goroutine has long since exited. The trace_id lookup has to happen
	// while the chat run is still in flight (or freshly completed), which
	// is exactly when TriggerMemory is called by the frontend on the
	// streaming→idle transition. Reading inside the AfterFunc raced
	// against the (now-removed) defer cleanup and always lost; capturing
	// here pins the value via closure so the 20s debounce is harmless.
	a.mu.Lock()
	traceID := a.pendingTraceIDs[sessionLocalId]
	a.mu.Unlock()

	if cs.debounceTimer != nil {
		cs.debounceTimer.Stop()
	}
	cs.debounceTimer = time.AfterFunc(memoryBaseDebounce, func() {
		cs.mu.Lock()
		cs.lastTriageTime = time.Now()
		cs.roundAtLastTrig = roundCount
		curSummary := cs.lastSummary
		curSource, curSourceID := cs.source, cs.sourceID
		cs.mu.Unlock()

		mc.evaluationMu.Lock()
		mc.runMemoryPipeline(a.ctx, curSource, curSourceID, curSummary, traceID)
		mc.evaluationMu.Unlock()
	})
}

// getMemoryGenerator returns the memory generator, initializing if needed.
func (a *App) getMemoryGenerator() *MemoryGenerator {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.memoryGenerator
}

// ── Slack evaluation ────────────────────────────────────────────────

func (mc *MemoryGenerator) evaluateSlack(ctx context.Context) {
	mc.evaluationMu.Lock()
	defer mc.evaluationMu.Unlock()

	dataRoot, err := flavorDataDir()
	if err != nil {
		return
	}

	convDir := filepath.Join(dataRoot, "slack-scan", "conversations")
	entries, err := os.ReadDir(convDir)
	if err != nil {
		return // no slack scan data
	}

	for _, e := range entries {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		convID := strings.TrimSuffix(e.Name(), ".json")

		mc.mu.Lock()
		processed := mc.processedSlackConvs[convID]
		mc.mu.Unlock()
		if processed {
			continue
		}

		data, err := os.ReadFile(filepath.Join(convDir, e.Name()))
		if err != nil {
			continue
		}

		var conv SlimConversation
		if err := json.Unmarshal(data, &conv); err != nil {
			continue
		}

		if conv.MessageCount < 3 {
			mc.markSlackProcessed(convID)
			continue
		}

		summary := mc.buildSlackSummary(memorySourceSlack, conv)
		// Background slack-scan ticker, not part of an agentloop chain —
		// pass "" so runMemoryPipeline suppresses agentloop.* telemetry.
		result := mc.runMemoryPipeline(ctx, memorySourceSlack, convID, summary, "")
		// Only mark processed on terminal outcomes; failed/canceled stay retryable.
		if result == memPipelineSuccess || result == memPipelineSkipped {
			mc.markSlackProcessed(convID)
		}
	}
}

func (mc *MemoryGenerator) buildSlackSummary(source memorySource, conv SlimConversation) string {
	var b strings.Builder
	selfUserID, selfLabel := connectorSelfIdentity(source)

	b.WriteString(fmt.Sprintf("[%s Conversation]\n", connectorSourceLabel(source)))
	b.WriteString(fmt.Sprintf("Channel: %s\n", conv.ChannelID))
	b.WriteString(fmt.Sprintf("Type: %s\n", conv.Type))
	b.WriteString(fmt.Sprintf("Participants: %s\n", strings.Join(conv.Participants, ", ")))
	b.WriteString(formatConnectorBoundUserLine(selfLabel, selfUserID))
	b.WriteString(fmt.Sprintf("Messages: %d\n\n", conv.MessageCount))

	for _, msg := range conv.Messages {
		name := msg.UserName
		if name == "" {
			name = msg.UserID
		}
		if name == "" {
			name = "unknown"
		}
		text := msg.Text
		if len(text) > 300 {
			text = text[:300] + "..."
		}
		b.WriteString(formatConnectorMessageLine(msg.UserID, name, text, selfUserID))
	}

	return b.String()
}

func connectorSourceLabel(source memorySource) string {
	switch source {
	case memorySourceSlack:
		return "Slack"
	case memorySourceDiscord:
		return "Discord"
	case memorySourceFeishu:
		return "Feishu"
	case memorySourceTelegram:
		return "Telegram"
	case memorySourceWeChat:
		return "WeChat"
	case memorySourceTeams:
		return "Teams"
	case memorySourceRingCentral:
		return "RingCentral"
	case memorySourceGmail:
		return "Gmail"
	default:
		return "Connector"
	}
}

func isConnectorMemorySource(source memorySource) bool {
	switch source {
	case memorySourceSlack,
		memorySourceDiscord,
		memorySourceFeishu,
		memorySourceTelegram,
		memorySourceWeChat,
		memorySourceTeams,
		memorySourceRingCentral,
		memorySourceGmail:
		return true
	default:
		return false
	}
}

func connectorSelfIdentity(source memorySource) (userID, userLabel string) {
	cfg := loadConfig()

	switch source {
	case memorySourceSlack:
		if cfg.Slack != nil {
			return cfg.Slack.SlackUserID, cfg.Slack.SlackUserID
		}
	case memorySourceDiscord:
		if cfg.Discord != nil {
			label := cfg.Discord.DiscordUsername
			if label == "" {
				label = cfg.Discord.DiscordUserID
			}
			return cfg.Discord.DiscordUserID, label
		}
	case memorySourceFeishu:
		if cfg.Feishu != nil {
			label := cfg.Feishu.FeishuUserName
			if label == "" {
				label = cfg.Feishu.FeishuOpenID
			}
			return cfg.Feishu.FeishuOpenID, label
		}
	case memorySourceTelegram:
		if cfg.Telegram != nil {
			label := cfg.Telegram.TelegramUserName
			if label == "" {
				label = cfg.Telegram.TelegramUserID
			}
			return cfg.Telegram.TelegramUserID, label
		}
	}

	return "", ""
}

func formatConnectorBoundUserLine(selfLabel, selfUserID string) string {
	switch {
	case selfLabel != "" && selfUserID != "" && selfLabel != selfUserID:
		return fmt.Sprintf("Bound User: %s (%s)\n", selfLabel, selfUserID)
	case selfLabel != "":
		return fmt.Sprintf("Bound User: %s\n", selfLabel)
	case selfUserID != "":
		return fmt.Sprintf("Bound User: %s\n", selfUserID)
	default:
		return "Bound User: [unknown]\n"
	}
}

func formatConnectorMessageLine(messageUserID, speakerName, text, selfUserID string) string {
	tag := "OTHER"
	if selfUserID != "" && messageUserID != "" && messageUserID == selfUserID {
		tag = "SELF"
	}
	return fmt.Sprintf("[%s] %s: %s\n", tag, speakerName, text)
}

func (mc *MemoryGenerator) markSlackProcessed(id string) {
	mc.mu.Lock()
	mc.processedSlackConvs[id] = true
	mc.mu.Unlock()
	mc.saveProcessedStateForSource("slack")
}

// ── Dedup helpers ────────────────────────────────────────────────────

// firstPersonPerspectiveInstruction returns the shared guidance block telling
// the LLM to write memory points from the user's own perspective (first person)
// and to distinguish the user from other people present in the source data.
//
// Memory entries describe the user themselves, so they read most naturally in
// first person. The "self vs others" guidance prevents activity data sourced
// from chats/emails/social posts from contaminating memory with third-party
// observations about the user (compliments, opinions, descriptions) that are
// not the user's own preferences or behaviors.
func firstPersonPerspectiveInstruction(userName string) string {
	return fmt.Sprintf(`The user's name is **%s**. Memory entries describe the user themselves, so every memory point MUST be written in FIRST PERSON from %s's own perspective — as if %s is describing themselves (e.g., "I prefer bun over npm", "I use Vim keybindings", "我习惯在终端里用语音输入"). NEVER write memory points in the third person; NEVER use the user's name inside a memory point.

CRITICAL — distinguish the user (%s) from OTHER PEOPLE:
The source data may include chats, emails, social posts, meetings, or shared screens where other people talk to %s, talk about %s, or appear alongside %s. Only %s's OWN behaviors, choices, stated preferences, and self-descriptions are memory material.

- DO turn into memory: things %s themselves said, did, configured, preferred, or chose.
- DO NOT turn into memory: things OTHER people said ABOUT %s (compliments, opinions, nicknames, third-party descriptions like "X is the goat", "everyone loves your demo"), or things other people did/preferred.
- If you cannot clearly attribute a statement to %s themselves, skip it.`, userName, userName, userName, userName, userName, userName, userName, userName, userName, userName, userName)
}

// ── Core pipeline: triage → generate → save ─────────────────────────

// traceID carries the upstream agentloop correlation id when the pipeline is
// triggered by a user-initiated chat session (frontend TriggerMemory after an
// AI response completes). Pass "" for background paths — connector scans
// (Slack/Discord/Feishu/Telegram on the periodic ticker) are not part of an
// agentloop chain and their downstream ExecuteTask calls suppress telemetry
// instead of emitting trace-less agentloop.* events.
func (mc *MemoryGenerator) runMemoryPipeline(
	ctx context.Context,
	source memorySource,
	sourceID, summary, traceID string,
) memPipelineResult {
	return mc.runMemoryPipelineDetailed(
		ctx,
		source,
		sourceID,
		summary,
		traceID,
	).Result
}

func (mc *MemoryGenerator) runMemoryPipelineDetailed(
	ctx context.Context,
	source memorySource,
	sourceID, summary, traceID string,
) memoryPipelineOutcome {
	outcome := memoryPipelineOutcome{
		Result:           memPipelineSkipped,
		Materializations: []memoryPipelineMaterialization{},
	}
	if summary == "" {
		return outcome
	}
	pipelineStart := time.Now()

	log.Printf("[memory] starting pipeline for %s:%s", source, sourceID)

	// Emit status to frontend
	mc.emitStatus("triaging", string(source), sourceID)

	// Phase 1: Triage (Haiku — fast, cheap)
	triageResult, err := mc.runTriage(ctx, source, summary, traceID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			outcome.Result = memPipelineCanceled
			return outcome
		}
		log.Printf("[memory] triage failed for %s:%s: %s", source, sourceID, activityPrivateLogField("error", err.Error()))
		logTriage("memory", sourceID, "error", "triage failed: "+activityPrivateLogField("error", err.Error()))
		mc.emitStatus("idle", string(source), sourceID)
		outcome.Result = memPipelineFailed
		return outcome
	}

	// Log triage result with worthKnowledge field
	worthKnowledge := triageResult.WorthKnowledge
	if !triageResult.Worth && !triageResult.WorthKnowledge {
		log.Printf("[memory] triage: not worth for %s:%s: %s", source, sourceID, activityPrivateLogField("reason", triageResult.Reason))
		logTriageEntry(TriageLogEntry{
			Source:         "memory",
			ID:             sourceID,
			Decision:       "not_worth",
			Reason:         triageResult.Reason,
			WorthMemory:    &triageResult.Worth,
			WorthKnowledge: &worthKnowledge,
		})
		mc.emitStatus("idle", string(source), sourceID)
		return outcome
	}

	log.Printf("[memory] triage: worth=%v worthKnowledge=%v for %s:%s: %s", triageResult.Worth, triageResult.WorthKnowledge, source, sourceID, activityPrivateLogField("reason", triageResult.Reason))
	logTriageEntry(TriageLogEntry{
		Source:         "memory",
		ID:             sourceID,
		Decision:       "worth",
		Reason:         triageResult.Reason,
		WorthMemory:    &triageResult.Worth,
		WorthKnowledge: &worthKnowledge,
	})
	mc.emitStatus("generating", string(source), sourceID)

	// Run knowledge generation synchronously so it stays within evaluationMu.
	if triageResult.WorthKnowledge {
		outcome.Materializations = append(
			outcome.Materializations,
			mc.generateAndSaveKnowledgeDetailed(
				ctx,
				source,
				sourceID,
				summary,
				traceID,
			)...,
		)
	}

	if !triageResult.Worth {
		// Only knowledge was worth generating, memory pipeline stops here
		mc.emitStatus("idle", string(source), sourceID)
		if len(outcome.Materializations) > 0 {
			outcome.Result = memPipelineSuccess
		}
		return outcome
	}

	// Phase 2: Generate (strongest model — memory quality matters)
	memResult, err := mc.runGeneration(ctx, source, sourceID, summary, traceID)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			outcome.Result = memPipelineCanceled
			return outcome
		}
		log.Printf("[memory] generation failed for %s:%s: %s", source, sourceID, activityPrivateLogField("error", err.Error()))
		mc.emitStatus("idle", string(source), sourceID)
		outcome.Result = memPipelineFailed
		return outcome
	}

	// Panel titles resolve from the actual saved artifact, not model text.
	var panelEntryID string
	logDecision := func(decision, reason string) {
		logTriageEntry(TriageLogEntry{
			Source: "memory", ID: sourceID, EntryID: panelEntryID, Decision: decision, Reason: reason,
			Title: memResult.Title,
		})
	}

	memResult.Title = memoryTitleInSecondPerson(memResult.Title)
	if len(memResult.MemoryPoints) == 0 {
		log.Printf("[memory] generation returned no points for %s:%s, skipping", source, sourceID)
		logDecision("skip", "AI returned no memory points")
		mc.emitStatus("idle", string(source), sourceID)
		return outcome
	}

	// Correction-match gate (parallel to activity_triage.go) — refuse
	// to save chat-source memories that closely match anything the user has
	// already rejected or reassigned. This makes the "栗子" class of bugs
	// not re-emerge from the chat pipelines either.
	if dir, err := memoryDir(); err == nil {
		if corrections, err := LoadRecentCorrections(dir, 50); err != nil {
			log.Printf("[memory] load corrections for chat-source gate failed: %s", activityPrivateLogField("error", err.Error()))
		} else if match := FindMatchingCorrection(corrections, memResult.Title, memResult.MemoryPoints); match != nil {
			reason := fmt.Sprintf(
				"matches previously %s memory %q (score=%.2f, reason=%q)",
				match.Correction.Kind, match.Correction.Title, match.Score, match.Correction.Reason,
			)
			log.Printf("[memory] correction-match skip for %s:%s: %s", source, sourceID, activityPrivateLogField("reason", reason))
			logDecision("correction_match_skip", reason)
			mc.emitStatus("idle", string(source), sourceID)
			return outcome
		}
	}

	// Attribution hard gate (parallel to ValidateMemoryDimension on the
	// activity path). Count [SELF] vs [OTHER] lines in the summary that
	// went to the LLM; if the conversation was overwhelmingly someone
	// else talking, refuse to let user_insight / deep_user_model /
	// agentic_memory reach disk — the model may have ignored the
	// "[OTHER] doesn't justify personal memory" directive in the prompt.
	connectorProfile := ParseConnectorAttributionProfile(summary)
	if action, reason := ValidateConnectorMemoryDimension(connectorProfile, memResult.Dimension); action != MemoryGateAllow {
		logReason := activityLogFieldMetadata("reason", reason)
		if triageSystemReason(reason) {
			logReason = reason
		}
		switch action {
		case MemoryGateSkip:
			log.Printf("[memory] attribution-gate skip for %s:%s — %s", source, sourceID, logReason)
			logDecision("attribution_skip", reason)
			mc.emitStatus("idle", string(source), sourceID)
			return outcome
		case MemoryGateCoerceToHighlight:
			log.Printf("[memory] attribution-gate coerce for %s:%s — %s", source, sourceID, logReason)
			logDecision("attribution_coerce", reason)
			memResult.Dimension = "topic_highlight"
		}
	}

	// Handle dedup action
	action := memResult.Action
	if action == "" {
		action = "create" // backwards compatibility
	}

	if action != "skip" && memResult.UpdateID == "" {
		if id := nearDuplicateMemoryID(memResult.Title); id != "" {
			action = "update"
			memResult.UpdateID = id
			logDecision("dedup_near_duplicate", activityPrivateLogField("update_id", id))
		}
	}

	switch action {
	case "skip":
		log.Printf("[memory] generation returned skip for %s:%s — duplicate of existing memory", source, sourceID)
		logDecision("dedup_skip", activityPrivateLogField("title", memResult.Title))
		mc.emitStatus("idle", string(source), sourceID)
		return outcome
	case "update":
		if memResult.UpdateID != "" {
			entryID, detailedAction, err := mc.updateMemoryFileDetailed(
				ctx,
				memResult.UpdateID,
				source,
				sourceID,
				memResult,
				traceID,
			)
			if err != nil {
				mc.emitStatus("idle", string(source), sourceID)
				outcome.Result = memPipelineFailed
				return outcome
			}
			mc.app.linkInitiativeArtifact(string(source), sourceID, "memory", entryID)
			panelEntryID = entryID
			logDecision("dedup_update", fmt.Sprintf("%s points=%d %s", activityPrivateLogField("update_id", entryID), len(memResult.MemoryPoints), activityPrivateLogField("title", memResult.Title)))
			outcome.Materializations = append(
				outcome.Materializations,
				memoryPipelineMaterialization{
					ArtifactKind: BackgroundArtifactMemory,
					ArtifactID:   entryID,
					Action:       detailedAction,
					Title:        memResult.Title,
					Dimension:    memResult.Dimension,
				},
			)
		} else {
			// No updateId provided, fall through to create
			entryID, err := mc.saveMemoryFileContext(ctx, source, sourceID, memResult)
			if err != nil {
				mc.emitStatus("idle", string(source), sourceID)
				outcome.Result = memPipelineFailed
				return outcome
			}
			mc.app.linkInitiativeArtifact(string(source), sourceID, "memory", entryID)
			panelEntryID = entryID
			logDecision("saved", fmt.Sprintf("points=%d %s (update without id)", len(memResult.MemoryPoints), activityPrivateLogField("title", memResult.Title)))
			outcome.Materializations = append(
				outcome.Materializations,
				memoryPipelineMaterialization{
					ArtifactKind: BackgroundArtifactMemory,
					ArtifactID:   entryID,
					Action:       BackgroundMaterializationCreated,
					Title:        memResult.Title,
					Dimension:    memResult.Dimension,
				},
			)
		}
	default: // "create"
		entryID, err := mc.saveMemoryFileContext(ctx, source, sourceID, memResult)
		if err != nil {
			mc.emitStatus("idle", string(source), sourceID)
			outcome.Result = memPipelineFailed
			return outcome
		}
		panelEntryID = entryID
		logDecision("saved", fmt.Sprintf("points=%d %s", len(memResult.MemoryPoints), activityPrivateLogField("title", memResult.Title)))
		mc.app.linkInitiativeArtifact(string(source), sourceID, "memory", entryID)
		if mc.app.ctx != nil && entryID != "" {
			mc.app.emitBrowserEvent("insight:saved", map[string]string{
				"kind":   "memory",
				"id":     entryID,
				"title":  memResult.Title,
				"source": string(source),
			})
		}
		outcome.Materializations = append(
			outcome.Materializations,
			memoryPipelineMaterialization{
				ArtifactKind: BackgroundArtifactMemory,
				ArtifactID:   entryID,
				Action:       BackgroundMaterializationCreated,
				Title:        memResult.Title,
				Dimension:    memResult.Dimension,
			},
		)
	}

	mc.emitStatus("idle", string(source), sourceID)

	if mc.app.telemetry != nil {
		mc.app.telemetry.Capture("memory.generated", map[string]any{
			"action":        action,
			"source":        string(source),
			"points_count":  len(memResult.MemoryPoints),
			"dimension":     memResult.Dimension,
			"generation_ms": time.Since(pipelineStart).Milliseconds(),
		})
	}

	logAction := action
	switch action {
	case "create", "update", "skip":
	default:
		logAction = activityPrivateLogField("action", action)
	}
	log.Printf("[memory] %s %d memory points for %s:%s", logAction, len(memResult.MemoryPoints), source, sourceID)
	outcome.Result = memPipelineSuccess
	return outcome
}

// ── Phase 1: Triage ─────────────────────────────────────────────────

func connectorMemoryTriageAddendum(source memorySource) string {
	if !isConnectorMemorySource(source) {
		return ""
	}

	return fmt.Sprintf(`
## %s-specific memory guardrails

This data came from a messaging connector and may include multiple speakers.
- Treat lines tagged [SELF] as the primary evidence about the bound user.
- Treat lines tagged [OTHER] as context only. They may support project/team context or reusable knowledge, but they do NOT justify personal memory points about the user unless the bound user explicitly confirms the fact.
- Do NOT create or approve memory about the user's persona, vibe, tone, humor, communication style, values, identity, or "what kind of person they are".
- Do NOT infer long-term traits from chat style alone.
- Prefer durable operational facts: explicit tool choices, recurring workflows, stable responsibilities, project/team context, and recurring constraints.
- For connector data, default to worth=false unless there is a clear, durable, user-attributable fact that will improve future collaboration.`, connectorSourceLabel(source))
}

func connectorMemoryGenerationAddendum(source memorySource) string {
	if !isConnectorMemorySource(source) {
		return ""
	}

	return fmt.Sprintf(`
## %s-specific extraction guardrails

- Extract ONLY explicit, durable, operational context about the bound user.
- Only [SELF] lines count as primary evidence for personal memory.
- [OTHER] lines may provide context, but do NOT turn them into personal memories about the user unless the bound user later confirms the fact.
- Never write memory points about personality, vibe, tone, communication style, humor, values, identity, or other soul/persona-like traits.
- If the data only supports stylistic impressions or third-party opinions, return action="skip".`, connectorSourceLabel(source))
}

func (mc *MemoryGenerator) runTriage(ctx context.Context, source memorySource, summary, traceID string) (*memoryTriageResponse, error) {
	mc.app.mu.Lock()
	runner := mc.app.runner
	mc.app.mu.Unlock()

	if runner == nil {
		return nil, fmt.Errorf("runner not initialized")
	}

	// Load existing memories and knowledge for early dedup at triage stage
	existingMemories, _ := loadMemoryGenerationContextForQuery(ctx, mc.app, summary, 100_000)
	existingKnowledge := mc.app.loadExistingKnowledgeSummaryWithBudget(100_000)
	var dedupSection string
	if existingMemories != "" || existingKnowledge != "" {
		var dedupBuf strings.Builder
		dedupBuf.WriteString(`
## Deduplication check (IMPORTANT)

Before deciding worth/worthKnowledge, check the existing entries below.
- Set worth=false if the data ONLY contains information already fully covered by existing memories (nothing new to add or update).
- Set worthKnowledge=false if the data ONLY contains insights already fully covered by existing knowledge (nothing new to add or update).
- Still return true if the data has NEW details, UPDATED information, or DIFFERENT aspects even if the general topic is already covered.
`)
		if existingMemories != "" {
			dedupBuf.WriteString(fmt.Sprintf("\nExisting memories:\n%s", existingMemories))
		}
		if existingKnowledge != "" {
			dedupBuf.WriteString(fmt.Sprintf("\nExisting knowledge:\n%s", existingKnowledge))
		}
		dedupSection = dedupBuf.String()
	}

	userName := mc.app.getUserDisplayName()
	perspectiveInstruction := firstPersonPerspectiveInstruction(userName)
	prompt := fmt.Sprintf(`Analyze this data and determine whether it contains personal context, user preferences, habits, or patterns worth remembering for future interactions.

%s

Data:
---
%s
---

Respond with ONLY a JSON object (no markdown, no code fences):
{"worth": true/false, "worthKnowledge": true/false, "reason": "brief explanation"}

## What is memory?

Memory captures PERSONAL CONTEXT about %s — their preferences, habits, workflows, tools, team dynamics, project context, and recurring patterns. Unlike knowledge (which is reusable technical insights), memory is about understanding %s specifically. Remember: only %s's OWN preferences and behaviors count — what OTHERS say about %s does not.

## Return worth=true if the data reveals:
- User preferences (tools, languages, frameworks, editors, settings)
- Work patterns (schedule, workflow habits, explicit collaboration preferences)
- Project context (team members, architecture decisions, tech stack)
- Recurring behaviors (debugging approaches, review patterns, coding conventions)
- Personal context (role, team structure, goals, priorities)
- Tool configurations or environment setup
- Frequently discussed topics or domains

## Return worth=false for:
- Generic technical discussions with no personal context
- Very brief or trivial interactions
- System notifications or automated messages
- Content that only makes sense in the moment (ephemeral context)
- Data that is ENTIRELY redundant with existing memories (no new information at all)

Default to worth=true. Most meaningful interactions (>60%%%%) contain some personal context worth remembering.

## Knowledge (worthKnowledge)

Return worthKnowledge=true if the conversation contains a SPECIFIC, reusable insight in ANY of these categories:

- **Technical insights** — concrete error+workaround, API behavior, tool configuration, specific technique (version, command, config key, error message)
- **People & org knowledge** — who owns what system, who is the expert on a topic, team responsibilities, reporting lines
- **Process knowledge** — how deploys work, what approvals are needed, release procedures, review workflows — tribal knowledge that lives in people's heads
- **Domain/business knowledge** — business rules, licensing constraints, metric definitions, customer segment behaviors — context that shapes decisions
- **Failure patterns** — what went wrong, root cause, and the fix — distilled post-mortems someone can learn from
- **Environment/infra quirks** — CI limits, shared resource gotchas, platform-specific behaviors that cause surprises
- **Vendor/API gotchas** — undocumented limits, misleading error messages, behaviors that differ from docs
- **Decision rationale** — WHY a specific technology, architecture, or approach was chosen over alternatives

Return worthKnowledge=false if:
- The conversation is casual chat, status updates, or scheduling coordination
- The only possible "insights" are generic (e.g., "retry patterns help", "flexible config is good")
- You cannot point to a SPECIFIC detail (name, number, command, rule, constraint) in the data
- The insight is ENTIRELY redundant with existing knowledge entries

Default to worthKnowledge=false. Most conversations (>70%%%%) should be false. Only flag conversations with concrete, reusable insights.
%s
%s
CRITICAL: Do NOT ask the user any questions. Respond immediately with the JSON object.`, perspectiveInstruction, summary, userName, userName, userName, userName, dedupSection, connectorMemoryTriageAddendum(source))

	var result strings.Builder

	prompt, _ = pii.RedactText(prompt)
	log.Printf("[memory] triage ExecuteTask starting")
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            prompt,
		Model:             mc.app.lightTaskModel(),
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		TraceID:           traceID,
		SuppressTelemetry: traceID == "",
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
		case "error":
			log.Printf("[memory] triage stream error: %s", activityPrivateLogField("error", event.Error))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("execute triage: %w", err)
	}

	raw := strings.TrimSpace(result.String())
	raw = stripCodeFences(raw)

	var resp memoryTriageResponse
	if err := unmarshalJSONBestEffort(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse triage response: %w", err)
	}

	return &resp, nil
}

// ── Phase 2: Generation ─────────────────────────────────────────────

func (mc *MemoryGenerator) runGeneration(ctx context.Context, source memorySource, sourceID, summary, traceID string) (*mcMemoryGenerationResponse, error) {
	mc.app.mu.Lock()
	runner := mc.app.runner
	mc.app.mu.Unlock()

	if runner == nil {
		return nil, fmt.Errorf("runner not initialized")
	}

	userName := mc.app.getUserDisplayName()

	// Load existing memories for dedup context
	promptContext := loadMemoryGenerationPromptContextForQuery(ctx, mc.app, summary)
	existingMemories := promptContext.existing
	var existingSection string
	if existingMemories != "" {
		existingSection = fmt.Sprintf(`
## Existing memories (IMPORTANT — check for duplicates!)

These memories are comparison hints for choosing create/update/skip, not sources of new facts.
For create, extract only from the current data. For update, merge only the selected updateId with the current data.

The following memories already exist. Before creating a new entry:
1. If the new data is essentially the SAME as an existing entry (same topic, same insights), return action="skip".
2. If the new data UPDATES or EXPANDS an existing entry (same topic but new/changed details), return action="update" with the updateId set to the existing entry's id.
3. Only return action="create" if this is genuinely NEW information not covered by any existing entry.

%s`, existingMemories)
	}

	languageInstruction := responseLanguageInstruction(mc.app.currentUILanguageName())
	perspectiveInstruction := firstPersonPerspectiveInstruction(userName)

	correctionsSection := promptContext.corrections
	initiativeBoundary := memoryGenerationSourceBoundary

	prompt := fmt.Sprintf(`%s

Extract personal memory points from this data. If nothing is worth remembering, return an empty array.

%s
%s
%s
Data:
---
%s
---
%s
Respond with ONLY a JSON object (no markdown, no code fences):
{
  "action": "create|update|skip",
  "updateId": "existing-entry-id-if-updating",
  "title": "the belief in one short sentence starting with You (or 你 in Chinese), with all verbs agreeing with the subject, e.g. You use design reviews to find launch blockers before release",
  "memoryPoints": ["memory point 1", "memory point 2"],
  "dimension": "user_insight|topic_highlight|deep_user_model|agentic_memory"
}

## action field rules:

- "skip": The data duplicates existing memories with no new information. Return empty memoryPoints.
- "update": The data relates to an existing memory but has new or updated details. Set updateId to the id of the entry to update (from the [id=xxx] tag in existing memories). Return the COMPLETE updated memoryPoints (merged old + new).
- "create": The data contains genuinely new personal context not covered by any existing memory.

## What is memory?

Memory captures personal context about %s to help future AI interactions be more personalized and helpful. Think of it as building a profile of %s over time — written by %s, about %s.

## Three mandatory properties:

1. **Personal** — The insight is about %s specifically: their own preferences, habits, patterns, context. Generic facts that apply to everyone are NOT memory. Things OTHER people said about %s are NOT memory.

2. **Durable** — The insight is likely to remain true over time. Fleeting states ("I am frustrated right now") are NOT memory. Stable patterns ("I prefer verbose error messages") ARE memory.

3. **Actionable** — Knowing this changes how you interact with %s. "I exist" is useless. "I prefer bun over npm" directly influences future recommendations.

## Good memory examples (note the first-person voice):

- "I prefer using bun over npm for all Node.js projects"
- "My main project uses Go backend + React frontend + PostgreSQL"
- "I use Vim keybindings in VS Code"
- "My team follows conventional commits format"
- "I frequently work on frontend performance optimization"

## Bad memory examples:

- "Today I had a conversation about X" (diary entry, not memory)
- "Someone called me 'the goat'" / "Others praised my work on Y" (third-party opinion ABOUT me, not my own preference or behavior)
- "%s prefers bun over npm" (third-person voice — write in first person instead: "I prefer bun over npm")
- "React is a popular framework" (generic fact, not personal)
- "The code was deployed at 3pm" (ephemeral, not durable)
- "Programming requires attention to detail" (truism, not actionable)

## Output rules:

- Default: return action="skip" with empty memoryPoints []. This is correct when no personal context is present or when existing memories already cover it.
- Each point must be a clear, self-contained FIRST-PERSON statement about myself (from the user's perspective).
- Prefer specific details over vague observations.
- One genuine memory is better than five mediocre ones.
- Do NOT duplicate information that would already be in existing memories.
- Title should describe the CONTEXT, not what happened.

## Dimension classification (required)

Every memory you create or update MUST be tagged with exactly ONE dimension.
Choose the BEST fit from these four keys (use the exact lowercase key, no other value is accepted):

- "user_insight" — Concrete working habits, preferences, and tool choices.
  Examples: prefers Rust for CLI tools, uses dark mode, always commits from a feature branch,
  prefers bun over npm, uses Vim keybindings in VS Code.

- "topic_highlight" — Recent topics of focus, active investigations, short-term themes.
  Examples: researching vector databases this week, evaluating Kubernetes operators,
  currently debugging a Postgres replication lag, reading about rate limiting strategies.

- "deep_user_model" — Long-term persona, role, identity, or durable responsibilities.
  Examples: senior backend engineer focused on distributed systems, tech lead of the
  payments team at Acme, PhD student in ML research, solo founder building a
  developer-tool startup.

- "agentic_memory" — AI operational preferences and instructions for how the assistant
  should behave with this user.
  Examples: always run lint before commit, use conventional commits, don't suggest
  emoji in code, prefer minimal diffs, ask before running destructive commands.

Do NOT invent other values. Do NOT return "custom" — that is reserved for manual
user-authored memories and must never come from generation. If none of the four
fit, pick the closest match (usually "user_insight").
%s`, languageInstruction, perspectiveInstruction, correctionsSection, initiativeBoundary, summary, existingSection,
		userName, userName, userName, userName,
		userName, userName,
		userName,
		userName,
		connectorMemoryGenerationAddendum(source))

	var result strings.Builder

	// Chat-tier model: structured extraction with a detailed rubric — the
	// chat tier is sufficient and the code tier's cost is not justified for
	// an automatic per-conversation background job.
	prompt, _ = pii.RedactText(prompt)
	log.Printf("[memory] generation ExecuteTask starting")
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            prompt,
		Model:             claudeCodeModelConfig().ChatModel,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		TraceID:           traceID,
		SuppressTelemetry: traceID == "",
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
		case "error":
			log.Printf("[memory] generation stream error: %s", activityPrivateLogField("error", event.Error))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("execute generation: %w", err)
	}

	raw := strings.TrimSpace(result.String())
	raw = stripCodeFences(raw)

	var resp mcMemoryGenerationResponse
	if err := unmarshalJSONBestEffort(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse generation response: %w", err)
	}

	promptContext.apply(&resp)
	dropPromptMemoryPoints(&resp)
	return &resp, nil
}

// ── Phase 3: Save ───────────────────────────────────────────────────

// normalizeMemoryDimension validates a dimension from the LLM and falls back
// to "user_insight" when the value is missing or unrecognized. "custom" is
// intentionally rejected for generator-produced memories — that value is
// reserved for user-authored manual entries.
func normalizeMemoryDimension(d string) string {
	v := strings.ToLower(strings.TrimSpace(d))
	switch v {
	case "user_insight", "topic_highlight", "deep_user_model", "agentic_memory":
		return v
	default:
		return "user_insight"
	}
}

func cleanMemoryPoints(points []string) []string {
	cleaned := make([]string, 0, len(points))
	for _, point := range points {
		point = strings.TrimSpace(point)
		if point == "" {
			continue
		}
		cleaned = append(cleaned, point)
	}
	return cleaned
}

// A selected update target can become protected after the dedup prompt was
// built. Preserve the local merge and its lineage without redisclosing it to a
// rewrite or translation model.
func (a *App) memoryUpdateAllowsModelRead(id, content string) bool {
	allowRead, err := a.memoryReadFilter()
	return err == nil && allowRead(id, content)
}

func (a *App) rewriteMemoryUpdatePoints(ctx context.Context, id, content string, existingPoints, incomingPoints []string, userName, traceID string) ([]string, error) {
	if !a.memoryUpdateAllowsModelRead(id, content) {
		return mergeMemoryPoints(existingPoints, incomingPoints), nil
	}
	return a.rewriteMemoryPointsFirstPerson(ctx, existingPoints, incomingPoints, userName, traceID)
}

// rewriteMemoryPointsFirstPerson asks the model to produce the canonical full
// memory-point list for an update. This avoids preserving legacy third-person
// bullets via code-level merging while still protecting older points from being
// silently dropped when the generation prompt only saw a truncated memory
// summary.
//
// traceID carries the upstream agentloop correlation id when the rewrite is
// part of a user-initiated chain (memory generator driven by chat post-
// processing). Pass "" for background paths (connector scans, activity
// generator updates) — the helper then flips SuppressTelemetry on so the
// agentloop.* PostHog dashboard stays limited to user-initiated runs.
func (a *App) rewriteMemoryPointsFirstPerson(ctx context.Context, existingPoints, incomingPoints []string, userName, traceID string) ([]string, error) {
	existingPoints = cleanMemoryPoints(existingPoints)
	incomingPoints = cleanMemoryPoints(incomingPoints)
	if len(existingPoints) == 0 {
		return incomingPoints, nil
	}
	if len(incomingPoints) == 0 {
		return existingPoints, nil
	}

	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	if runner == nil {
		return mergeMemoryPoints(existingPoints, incomingPoints), nil
	}

	existingJSON, err := json.MarshalIndent(existingPoints, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal existing memory points: %w", err)
	}
	incomingJSON, err := json.MarshalIndent(incomingPoints, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal incoming memory points: %w", err)
	}

	languageInstruction := responseLanguageInstruction(a.currentUILanguageName())
	perspectiveInstruction := firstPersonPerspectiveInstruction(userName)
	prompt := fmt.Sprintf(`%s

Canonicalize an existing memory update. Return the COMPLETE final memoryPoints array for the memory file.

%s

Existing memory points:
%s

Incoming memory points from the latest extraction:
%s

Respond with ONLY a JSON object (no markdown, no code fences):
{
  "memoryPoints": ["final memory point 1", "final memory point 2"]
}

Rules:
- Preserve every still-valid durable fact from the existing points.
- Add genuinely new facts from the incoming points.
- Merge duplicate or overlapping facts into one point.
- Rewrite every point as a clear FIRST-PERSON statement from the user's perspective.
- Remove third-party opinions, compliments, nicknames, or personality/vibe judgments from other people.
- Do not keep legacy third-person wording such as "%s prefers..." in the final output.
- Do not invent facts that are not present in either list.`, languageInstruction, perspectiveInstruction, existingJSON, incomingJSON, userName)

	var result strings.Builder
	// Light model: merging two JSON lists of short factual points needs no
	// deep reasoning.
	prompt, _ = pii.RedactText(prompt)
	_, err = runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		Prompt:            prompt,
		Model:             a.lightTaskModel(),
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		TraceID:           traceID,
		SuppressTelemetry: traceID == "",
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
		case "error":
			log.Printf("[memory] memory point rewrite stream error: %s", activityPrivateLogField("error", event.Error))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("execute memory point rewrite: %w", err)
	}

	raw := strings.TrimSpace(result.String())
	raw = stripCodeFences(raw)

	var resp memoryPointsRewriteResponse
	if err := unmarshalJSONBestEffort(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse memory point rewrite response: %w", err)
	}
	finalPoints := cleanMemoryPoints(resp.MemoryPoints)
	if len(finalPoints) == 0 {
		return nil, fmt.Errorf("memory point rewrite returned no points")
	}
	return finalPoints, nil
}

// saveMemoryFile writes a memory entry to disk and returns its ID
// (filename without the .md extension) on success.
func (mc *MemoryGenerator) saveMemoryFile(source memorySource, sourceID string, mem *mcMemoryGenerationResponse) (string, error) {
	return mc.saveMemoryFileContext(mc.app.memorySemanticContext(), source, sourceID, mem)
}

func (mc *MemoryGenerator) saveMemoryFileContext(ctx context.Context, source memorySource, sourceID string, mem *mcMemoryGenerationResponse) (string, error) {
	dir, err := memoryDir()
	if err != nil {
		log.Printf("[memory] failed to get memory dir: %s", activityPrivateLogField("error", err.Error()))
		return "", err
	}

	// Deterministic filename from source + sourceID
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", source, sourceID)))
	entryID := fmt.Sprintf("%s-%x", source, hash[:8])
	filename := entryID + ".md"

	filePath := filepath.Join(dir, filename)
	content := buildOKFMemoryMarkdown(entryID, mem.Title, time.Now(), source, mem.Dimension, mem.MemoryPoints)
	content = withMemoryObservationSourceID(content, "", entryID)
	content = withMemoryReadProvenance(content, memoryGenerationProvenance(mem, memoryGenerationCurrentSource(mem, memoryReadSource(source))))
	if err := mc.app.prepareMemoryObservedWriteContext(ctx, dir, entryID, content); err != nil {
		return "", err
	}
	content = mc.app.normalizeGeneratedMarkdownToCurrentUILanguage("memory", content)
	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock := memoryFileLocks.Acquire(entryID)
	defer unlock()
	if err := checkMemoryWriteAllowed(dir, entryID, content, true); err != nil {
		return "", err
	}
	if previous, err := os.ReadFile(filePath); err == nil {
		content, err = preserveMemoryConfirmation(content, string(previous))
		if err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := snapshotMemoryFile(dir, entryID); err != nil {
		return "", err
	}
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		log.Printf("[memory] failed to write memory file: %s %s", activityPrivateLogField("path", filePath), activityPrivateLogField("error", err.Error()))
		return "", err
	}

	log.Printf("[memory] saved memory to %s", activityPrivateLogField("path", filePath))

	// Emit event to frontend to refresh memory list
	if mc.app.ctx != nil {
		mc.app.emitBrowserEvent("memory:updated")
	}

	// Mark memory store dirty so the FTS5+vector index picks up the new file.
	mc.app.markMemoryDirty("memory-generator")
	return entryID, nil
}

// updateMemoryFile updates an existing memory file identified by updateID.
//
// Update semantics: the generation prompt may only see a truncated summary of
// the target memory, so the update path reads the full existing file and asks
// the model to rewrite the complete canonical point list in first person. This
// preserves durable old facts without carrying legacy third-person wording
// forward through a blind code-level merge.
func (mc *MemoryGenerator) updateMemoryFile(
	ctx context.Context,
	updateID string,
	source memorySource,
	sourceID string,
	mem *mcMemoryGenerationResponse,
	traceID string,
) error {
	_, _, err := mc.updateMemoryFileDetailed(
		ctx,
		updateID,
		source,
		sourceID,
		mem,
		traceID,
	)
	return err
}

func (mc *MemoryGenerator) updateMemoryFileDetailed(
	ctx context.Context,
	updateID string,
	source memorySource,
	sourceID string,
	mem *mcMemoryGenerationResponse,
	traceID string,
) (string, string, error) {
	dir, err := memoryDir()
	if err != nil {
		log.Printf("[memory] failed to get memory dir: %s", activityPrivateLogField("error", err.Error()))
		return "", "", err
	}

	// Sanitize updateID to prevent path traversal
	if strings.Contains(updateID, "..") || strings.ContainsAny(updateID, "/\\") {
		log.Printf("[memory] invalid updateID %s, falling back to create", activityPrivateLogField("id", updateID))
		entryID, err := mc.saveMemoryFileContext(ctx, source, sourceID, memoryGenerationUnverifiedUpdate(mem))
		return entryID, BackgroundMaterializationCreated, err
	}

	filePath := filepath.Join(dir, updateID+".md")

	// Serialize against concurrent writers (SaveMemoryEntry, the activity
	// generator's own update path, another memory-generator update)
	// across the read-modify-write + snapshot sequence below. Required by
	// the contract in memory.go's snapshotMemoryFile comment: the caller
	// must hold the keyed lock across snapshot + write.
	unlock := memoryFileLocks.Acquire(updateID)
	defer unlock()

	// Read existing file. If it's missing, release the lock before the
	// fallback create path so the fresh entryID (derived from source +
	// sourceID inside saveMemoryFile) can acquire its own lock without
	// self-deadlocking on this stale updateID.
	existingRaw, readErr := os.ReadFile(filePath)
	if os.IsNotExist(readErr) {
		log.Printf("[memory] updateID %s not found, falling back to create", activityPrivateLogField("id", updateID))
		unlock()
		entryID, err := mc.saveMemoryFileContext(ctx, source, sourceID, memoryGenerationUnverifiedUpdate(mem))
		return entryID, BackgroundMaterializationCreated, err
	}
	if readErr != nil {
		log.Printf("[memory] failed to read existing memory %s: %s — falling back to create", activityPrivateLogField("path", filePath), activityPrivateLogField("error", readErr.Error()))
		unlock()
		entryID, err := mc.saveMemoryFileContext(ctx, source, sourceID, memoryGenerationUnverifiedUpdate(mem))
		return entryID, BackgroundMaterializationCreated, err
	}
	candidate := buildOKFMemoryMarkdown(updateID, mem.Title, time.Now(), source, mem.Dimension, mem.MemoryPoints)
	sourceHash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", source, sourceID)))
	sourceEntryID := fmt.Sprintf("%s-%x", source, sourceHash[:8])
	candidate = withMemoryObservationSourceID(candidate, string(existingRaw), sourceEntryID)
	unlock()

	if err := checkMemoryWriteAllowed(dir, updateID, candidate, true); err != nil {
		return "", "", err
	}
	existingPoints := parseMemoryPoints(string(existingRaw))
	userName := mc.app.getUserDisplayName()
	finalPoints, err := mc.app.rewriteMemoryUpdatePoints(ctx, updateID, string(existingRaw), existingPoints, mem.MemoryPoints, userName, traceID)
	if err != nil {
		return "", "", fmt.Errorf("rewrite memory points: %w", err)
	}

	content := buildOKFMemoryMarkdown(updateID, mem.Title, time.Now(), source, mem.Dimension, finalPoints)
	content = preserveMemoryTopics(content, string(existingRaw))
	content = withMemoryObservationSourceID(content, string(existingRaw), sourceEntryID)
	content = withMemoryReadProvenance(content, memoryGenerationProvenance(mem, memoryReadProvenanceForFile(dir, updateID, string(existingRaw)), memoryGenerationCurrentSource(mem, memoryReadSource(source))))
	if err := mc.app.prepareMemoryObservedWriteContext(ctx, dir, updateID, content); err != nil {
		return "", "", err
	}
	if mc.app.memoryUpdateAllowsModelRead(updateID, string(existingRaw)) {
		content = mc.app.normalizeGeneratedMarkdownToCurrentUILanguage("memory", content)
	}
	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock = memoryFileLocks.Acquire(updateID)
	defer unlock()
	if err := validateMemoryObservationUnchanged(dir, updateID, existingRaw, content); err != nil {
		return "", "", err
	}
	// Translation can change the words after source bookkeeping. Reconcile
	// against the verified previous revision at the final write boundary.
	content, err = preserveMemoryConfirmation(content, string(existingRaw))
	if err != nil {
		return "", "", err
	}
	// Snapshot the pre-update content to .history/ so the Version History
	// UI can show this as a restorable previous version. Must run while
	// the lock is held and before the overwrite write below.
	if err := snapshotMemoryFile(dir, updateID); err != nil {
		log.Printf("[memory] failed to snapshot before update: %s %s", activityPrivateLogField("update_id", updateID), activityPrivateLogField("error", err.Error()))
		return "", "", fmt.Errorf("snapshot %s: %w", updateID, err)
	}

	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		log.Printf("[memory] failed to update memory file: %s %s", activityPrivateLogField("path", filePath), activityPrivateLogField("error", err.Error()))
		return "", "", err
	}

	log.Printf("[memory] updated existing memory at %s (existing=%d llm=%d final=%d)",
		activityPrivateLogField("path", filePath), len(existingPoints), len(mem.MemoryPoints), len(finalPoints))

	if mc.app.ctx != nil {
		mc.app.emitBrowserEvent("memory:updated")
	}
	mc.app.markMemoryDirty("memory-generator-update")
	return updateID, BackgroundMaterializationUpdated, nil
}

// parseMemoryPoints extracts bullet lines from a memory entry's markdown.
//
// Anchors on the `## Memory Points` section marker and only reads bullets
// below it, so header rows (`- **Updated**:` / `- **Source**: ` /
// `- **Dimension**:` / etc.) can't bleed in. The previous approach of
// filtering on `- **` prefix was wrong because legit memory points can
// begin with bold (e.g. `- **Note**: prefers verbose errors`) and would
// be silently dropped.
//
// If the section marker is absent (entries we didn't author ourselves,
// hand-edited files, etc.), returns nil rather than guessing — the
// caller falls back to the LLM-provided list alone, which is strictly
// better than returning garbage metadata rows as if they were points.
func parseMemoryPoints(markdown string) []string {
	const marker = "## Memory Points"
	idx := strings.Index(markdown, marker)
	if idx < 0 {
		return nil
	}
	body := markdown[idx+len(marker):]
	var points []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			// Next H2 section — stop. Memory entries shouldn't have one
			// today, but this keeps the parser honest if the format grows.
			break
		}
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		content := strings.TrimSpace(trimmed[2:])
		if content == "" {
			continue
		}
		points = append(points, content)
	}
	return points
}

// mergeMemoryPoints returns the union of the existing and incoming bullet
// lists in order: existing bullets first (preserves historical ordering
// and keeps the user's familiar prefix visible), then new bullets the
// LLM introduced. Duplicates are collapsed by trimmed exact match so a
// bullet the LLM echoes from the existing list doesn't appear twice.
// Whitespace is the only normalization applied — two bullets with
// meaningfully different wording but the same meaning are both kept,
// and users can prune via Version History.
func mergeMemoryPoints(existing, incoming []string) []string {
	seen := make(map[string]bool, len(existing)+len(incoming))
	merged := make([]string, 0, len(existing)+len(incoming))
	for _, p := range existing {
		key := strings.TrimSpace(p)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, key)
	}
	for _, p := range incoming {
		key := strings.TrimSpace(p)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, key)
	}
	return merged
}

// ── Status events ───────────────────────────────────────────────────

// MemoryStatus is emitted to the frontend to show pipeline progress.
type MemoryStatus struct {
	Status   string `json:"status"` // idle|triaging|generating
	Source   string `json:"source"` // conversation|activity|slack
	SourceID string `json:"sourceId"`
}

func (mc *MemoryGenerator) emitStatus(status, source, sourceID string) {
	if mc.app.ctx != nil {
		mc.app.emitBrowserEvent("memory:status", MemoryStatus{
			Status:   status,
			Source:   source,
			SourceID: sourceID,
		})
	}
}

// ── Processed state persistence ─────────────────────────────────────
//
// Per-source files: ~/.yansu-agent/memory_processed_{source}.json
// Each file is a simple JSON object: {"convID": true, ...}
// Legacy: ~/.yansu-agent/memory_processed.json (migrated on first load)

type legacyProcessedState struct {
	SlackConvs    map[string]bool `json:"slackConvs"`
	FeishuConvs   map[string]bool `json:"feishuConvs"`
	DiscordConvs  map[string]bool `json:"discordConvs,omitempty"`
	TelegramConvs map[string]bool `json:"telegramConvs,omitempty"`
}

func processedStateDir() string {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return ""
	}
	return dataRoot
}

func processedStatePath(source string) string {
	dir := processedStateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "memory_processed_"+source+".json")
}

func legacyProcessedStatePath() string {
	dir := processedStateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "memory_processed.json")
}

// loadProcessedState loads per-source files for all sources, migrating from
// the legacy single file if needed. Migration is idempotent and crash-safe:
// each source is migrated independently, and corrupt/unreadable per-source
// files are regenerated from the legacy file.
func (mc *MemoryGenerator) loadProcessedState() {
	mc.loadProcessedStateForSource("slack")
	mc.loadProcessedStateForSource("feishu")
	mc.loadProcessedStateForSource("discord")
	mc.loadProcessedStateForSource("telegram")

	// Delete legacy file only after all 4 per-source files exist and are readable.
	legacyPath := legacyProcessedStatePath()
	if legacyPath == "" {
		return
	}
	if _, err := os.Stat(legacyPath); err != nil {
		return // no legacy file
	}
	allMigrated := true
	for _, src := range []string{"slack", "feishu", "discord", "telegram"} {
		p := processedStatePath(src)
		if p == "" {
			allMigrated = false
			break
		}
		// Verify file is readable AND valid JSON, not just present on disk.
		data, err := os.ReadFile(p)
		if err != nil {
			allMigrated = false
			break
		}
		var m map[string]bool
		if json.Unmarshal(data, &m) != nil {
			allMigrated = false
			break
		}
	}
	if allMigrated {
		_ = os.Remove(legacyPath)
	}
}

// loadProcessedStateForSource loads one source's processed map from its per-source
// file. If the file is missing or corrupt and a legacy file exists, extracts that
// source's data from the legacy file and writes the per-source file atomically.
func (mc *MemoryGenerator) loadProcessedStateForSource(source string) {
	path := processedStatePath(source)
	if path == "" {
		return
	}

	// Try reading the per-source file.
	if data, err := os.ReadFile(path); err == nil {
		var m map[string]bool
		if json.Unmarshal(data, &m) == nil {
			mc.setProcessedMap(source, m)
			return
		}
		// Corrupt per-source file — fall through to migration.
	}

	// Per-source file missing or corrupt — try migrating from legacy.
	legacyPath := legacyProcessedStatePath()
	if legacyPath == "" {
		return
	}
	legacyData, err := os.ReadFile(legacyPath)
	if err != nil {
		return
	}
	var legacy legacyProcessedState
	if json.Unmarshal(legacyData, &legacy) != nil {
		return
	}

	var m map[string]bool
	switch source {
	case "slack":
		m = legacy.SlackConvs
	case "feishu":
		m = legacy.FeishuConvs
	case "discord":
		m = legacy.DiscordConvs
	case "telegram":
		m = legacy.TelegramConvs
	}
	if m == nil {
		m = make(map[string]bool)
	}
	mc.setProcessedMap(source, m)

	// Write the per-source file atomically.
	if out, err := json.MarshalIndent(m, "", "  "); err == nil {
		writeFileAtomic(path, out, "[memory] migrate-"+source)
	}
}

// setProcessedMap sets the in-memory map for a source under mc.mu.
func (mc *MemoryGenerator) setProcessedMap(source string, m map[string]bool) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	switch source {
	case "slack":
		mc.processedSlackConvs = m
	case "feishu":
		mc.processedFeishuConvs = m
	case "discord":
		mc.processedDiscordConvs = m
	case "telegram":
		mc.processedTelegramConvs = m
	}
}

// saveProcessedStateForSource deep-copies one source's map and writes it atomically.
// mc.mu must NOT be held by the caller.
func (mc *MemoryGenerator) saveProcessedStateForSource(source string) {
	path := processedStatePath(source)
	if path == "" {
		return
	}

	mc.mu.Lock()
	var src map[string]bool
	switch source {
	case "slack":
		src = mc.processedSlackConvs
	case "feishu":
		src = mc.processedFeishuConvs
	case "discord":
		src = mc.processedDiscordConvs
	case "telegram":
		src = mc.processedTelegramConvs
	}
	cp := make(map[string]bool, len(src))
	for k, v := range src {
		cp[k] = v
	}
	mc.mu.Unlock()

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return
	}
	writeFileAtomic(path, data, "[memory] save-"+source)
}

// markFeishuProcessed marks a Feishu conversation as processed and persists state.
func (mc *MemoryGenerator) markFeishuProcessed(id string) {
	mc.mu.Lock()
	mc.processedFeishuConvs[id] = true
	mc.mu.Unlock()
	mc.saveProcessedStateForSource("feishu")
}

// markDiscordProcessed marks a Discord conversation as processed and persists state.
func (mc *MemoryGenerator) markDiscordProcessed(id string) {
	mc.mu.Lock()
	mc.processedDiscordConvs[id] = true
	mc.mu.Unlock()
	mc.saveProcessedStateForSource("discord")
}

// clearSlackConvs resets the in-memory Slack dedup map and persists the change.
func (mc *MemoryGenerator) clearSlackConvs() {
	mc.mu.Lock()
	mc.processedSlackConvs = make(map[string]bool)
	mc.mu.Unlock()
	mc.saveProcessedStateForSource("slack")
}

// clearFeishuConvs resets the in-memory Feishu dedup map and persists the change.
func (mc *MemoryGenerator) clearFeishuConvs() {
	mc.mu.Lock()
	mc.processedFeishuConvs = make(map[string]bool)
	mc.mu.Unlock()
	mc.saveProcessedStateForSource("feishu")
}

// clearDiscordConvs resets the in-memory Discord dedup map and persists the change.
func (mc *MemoryGenerator) clearDiscordConvs() {
	mc.mu.Lock()
	mc.processedDiscordConvs = make(map[string]bool)
	mc.mu.Unlock()
	mc.saveProcessedStateForSource("discord")
}

// markTelegramProcessed marks a Telegram conversation as processed and persists state.
func (mc *MemoryGenerator) markTelegramProcessed(id string) {
	mc.mu.Lock()
	mc.processedTelegramConvs[id] = true
	mc.mu.Unlock()
	mc.saveProcessedStateForSource("telegram")
}

// clearTelegramConvs resets the in-memory Telegram dedup map and persists the change.
func (mc *MemoryGenerator) clearTelegramConvs() {
	mc.mu.Lock()
	mc.processedTelegramConvs = make(map[string]bool)
	mc.mu.Unlock()
	mc.saveProcessedStateForSource("telegram")
}

// ── Memory retry queue ───────────────────────────────────────────────

const memoryRetryFile = "memory_retry.json"
const memoryMaxRetries = 3

type memoryRetryEntry struct {
	ConvID    string `json:"conv_id"`
	Failures  int    `json:"failures"`
	LastTried string `json:"last_tried"`
}

// loadMemoryRetryIDs reads the retry queue for a platform's state directory
// and returns conversation IDs that are eligible for retry (< maxRetries).
func loadMemoryRetryIDs(stateDir string) []string {
	path := filepath.Join(stateDir, memoryRetryFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []memoryRetryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.Failures < memoryMaxRetries {
			ids = append(ids, e.ConvID)
		}
	}
	return ids
}

// updateMemoryRetryQueue persists the failed conversation IDs with their retry counts.
// Callers MUST only invoke this when the evaluate loop ran to completion (no ctx
// cancellation) so that failedIDs accurately represents the full set of failures.
// IDs not in failedIDs are removed (they succeeded or were skipped).
// IDs exceeding memoryMaxRetries are dropped.
func updateMemoryRetryQueue(stateDir string, failedIDs []string) {
	path := filepath.Join(stateDir, memoryRetryFile)

	if len(failedIDs) == 0 {
		_ = os.Remove(path)
		return
	}

	// Load existing entries to preserve failure counts.
	var existing []memoryRetryEntry
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing)
	}
	lookup := make(map[string]int, len(existing))
	for _, e := range existing {
		lookup[e.ConvID] = e.Failures
	}

	now := time.Now().Format(time.RFC3339)
	seen := make(map[string]struct{}, len(failedIDs))
	var updated []memoryRetryEntry
	for _, id := range failedIDs {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		failures := lookup[id] + 1
		if failures < memoryMaxRetries {
			updated = append(updated, memoryRetryEntry{
				ConvID:    id,
				Failures:  failures,
				LastTried: now,
			})
		}
	}

	if len(updated) == 0 {
		_ = os.Remove(path)
		return
	}
	data, err := json.MarshalIndent(updated, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(stateDir, 0700)
	_ = os.WriteFile(path, data, 0600)
}

// evaluateTelegramNow synchronously processes the specified Telegram conversations
// from telegramScanDir/conversations/ and returns the count submitted to the memory pipeline
// along with IDs of conversations that failed (for retry).
// evaluationMu prevents concurrent runs with evaluateSlack()/evaluateSlackNow()/evaluateFeishuNow().
func (mc *MemoryGenerator) evaluateTelegramNow(ctx context.Context, telegramScanDir string, convIDs []string) (int, []string, error) {
	mc.evaluationMu.Lock()
	defer mc.evaluationMu.Unlock()

	convDir := filepath.Join(telegramScanDir, "conversations")
	processed := 0
	var failedIDs []string

	for _, convID := range convIDs {
		if ctx.Err() != nil {
			return processed, failedIDs, ctx.Err()
		}

		mc.mu.Lock()
		alreadyDone := mc.processedTelegramConvs[convID]
		mc.mu.Unlock()
		if alreadyDone {
			continue
		}

		convPath := filepath.Join(convDir, convID+".json")
		data, err := os.ReadFile(convPath)
		if err != nil {
			log.Printf("[memory] evaluateTelegramNow: read %s: %s", convID, activityPrivateLogField("error", err.Error()))
			failedIDs = append(failedIDs, convID)
			continue
		}
		var conv TelegramSlimConversation
		if err := json.Unmarshal(data, &conv); err != nil {
			log.Printf("[memory] evaluateTelegramNow: parse %s: %s", convID, activityPrivateLogField("error", err.Error()))
			failedIDs = append(failedIDs, convID)
			continue
		}
		if len(conv.Messages) < 3 {
			mc.markTelegramProcessed(convID)
			continue
		}

		selfUserID, selfLabel := connectorSelfIdentity(memorySourceTelegram)
		summary := buildTelegramSummary(conv, selfUserID, selfLabel)
		// Background telegram scan, not an agentloop chain — suppress.
		result := mc.runMemoryPipeline(ctx, memorySourceTelegram, convID, summary, "")
		if result == memPipelineSuccess || result == memPipelineSkipped {
			mc.markTelegramProcessed(convID)
			processed++
		} else if result == memPipelineFailed {
			failedIDs = append(failedIDs, convID)
		}
	}
	return processed, failedIDs, nil
}

// evaluateSlackNow synchronously processes the specified Slack conversations
// and returns the count of conversations submitted to the memory pipeline
// along with IDs of conversations that failed (for retry).
//
// evaluationMu serializes this with the background ticker's evaluateSlack() call so that
// the same conversation cannot be picked up by both concurrently.
func (mc *MemoryGenerator) evaluateSlackNow(ctx context.Context, convIDs []string) (int, []string, error) {
	mc.evaluationMu.Lock()
	defer mc.evaluationMu.Unlock()

	dataRoot, err := flavorDataDir()
	if err != nil {
		return 0, nil, err
	}
	convDir := filepath.Join(dataRoot, "slack-scan", "conversations")
	processed := 0
	var failedIDs []string

	for _, convID := range convIDs {
		if ctx.Err() != nil {
			return processed, failedIDs, ctx.Err()
		}

		mc.mu.Lock()
		alreadyDone := mc.processedSlackConvs[convID]
		mc.mu.Unlock()
		if alreadyDone {
			continue
		}

		convPath := filepath.Join(convDir, convID+".json")
		data, err := os.ReadFile(convPath)
		if err != nil {
			log.Printf("[memory] evaluateSlackNow: read %s: %s", convID, activityPrivateLogField("error", err.Error()))
			failedIDs = append(failedIDs, convID)
			continue
		}
		var conv SlimConversation
		if err := json.Unmarshal(data, &conv); err != nil {
			log.Printf("[memory] evaluateSlackNow: parse %s: %s", convID, activityPrivateLogField("error", err.Error()))
			failedIDs = append(failedIDs, convID)
			continue
		}
		if len(conv.Messages) < 3 {
			mc.markSlackProcessed(convID)
			continue
		}

		summary := mc.buildSlackSummary(memorySourceSlack, conv)
		// Background slack-scan (evaluateSlackNow), suppress agentloop.*
		result := mc.runMemoryPipeline(ctx, memorySourceSlack, convID, summary, "")
		if result == memPipelineSuccess || result == memPipelineSkipped {
			mc.markSlackProcessed(convID)
			processed++
		} else if result == memPipelineFailed {
			failedIDs = append(failedIDs, convID)
		}
	}
	return processed, failedIDs, nil
}

// evaluateFeishuNow synchronously processes the specified Feishu conversations
// from feishuScanDir/conversations/ and returns the count submitted to the memory pipeline
// along with IDs of conversations that failed (for retry).
// evaluationMu prevents concurrent runs with evaluateSlack()/evaluateSlackNow().
func (mc *MemoryGenerator) evaluateFeishuNow(ctx context.Context, feishuScanDir string, convIDs []string) (int, []string, error) {
	mc.evaluationMu.Lock()
	defer mc.evaluationMu.Unlock()

	convDir := filepath.Join(feishuScanDir, "conversations")
	processed := 0
	var failedIDs []string

	for _, convID := range convIDs {
		if ctx.Err() != nil {
			return processed, failedIDs, ctx.Err()
		}

		mc.mu.Lock()
		alreadyDone := mc.processedFeishuConvs[convID]
		mc.mu.Unlock()
		if alreadyDone {
			continue
		}

		convPath := filepath.Join(convDir, convID+".json")
		data, err := os.ReadFile(convPath)
		if err != nil {
			log.Printf("[memory] evaluateFeishuNow: read %s: %s", convID, activityPrivateLogField("error", err.Error()))
			failedIDs = append(failedIDs, convID)
			continue
		}
		var conv FeishuSlimConversation // defined in feishu_segmenter.go
		if err := json.Unmarshal(data, &conv); err != nil {
			log.Printf("[memory] evaluateFeishuNow: parse %s: %s", convID, activityPrivateLogField("error", err.Error()))
			failedIDs = append(failedIDs, convID)
			continue
		}
		if len(conv.Messages) < 3 {
			mc.markFeishuProcessed(convID)
			continue
		}

		selfUserID, selfLabel := connectorSelfIdentity(memorySourceFeishu)
		summary := buildFeishuSummary(conv, selfUserID, selfLabel) // defined in feishu_segmenter.go
		// Background feishu scan, suppress agentloop.* telemetry.
		result := mc.runMemoryPipeline(ctx, memorySourceFeishu, convID, summary, "")
		if result == memPipelineSuccess || result == memPipelineSkipped {
			mc.markFeishuProcessed(convID)
			processed++
		} else if result == memPipelineFailed {
			failedIDs = append(failedIDs, convID)
		}
	}
	return processed, failedIDs, nil
}

// evaluateDiscordConversations synchronously processes the specified Discord conversations
// from discord-scan/conversations/ and returns the count of convs processed, memory files
// actually written (memPipelineSuccess only), and IDs of conversations that failed (for retry).
func (mc *MemoryGenerator) evaluateDiscordConversations(ctx context.Context, convIDs []string) (processed int, memoryCount int, failedIDs []string, err error) {
	mc.evaluationMu.Lock()
	defer mc.evaluationMu.Unlock()

	dataRoot, homeErr := flavorDataDir()
	if homeErr != nil {
		return 0, 0, nil, homeErr
	}
	convDir := filepath.Join(dataRoot, "discord-scan", "conversations")

	for _, convID := range convIDs {
		if ctx.Err() != nil {
			return processed, memoryCount, failedIDs, ctx.Err()
		}

		mc.mu.Lock()
		alreadyDone := mc.processedDiscordConvs[convID]
		mc.mu.Unlock()
		if alreadyDone {
			continue
		}

		convPath := filepath.Join(convDir, convID+".json")
		data, readFileErr := os.ReadFile(convPath)
		if readFileErr != nil {
			log.Printf("[memory] evaluateDiscordConversations: read %s: %s", convID, activityPrivateLogField("error", readFileErr.Error()))
			failedIDs = append(failedIDs, convID)
			continue
		}
		// Discord conversations use the same SlimConversation format as Slack.
		var conv SlimConversation
		if jsonErr := json.Unmarshal(data, &conv); jsonErr != nil {
			log.Printf("[memory] evaluateDiscordConversations: parse %s: %s", convID, activityPrivateLogField("error", jsonErr.Error()))
			failedIDs = append(failedIDs, convID)
			continue
		}
		if len(conv.Messages) < 3 {
			mc.markDiscordProcessed(convID)
			continue
		}

		summary := mc.buildSlackSummary(memorySourceDiscord, conv) // reuse connector summary builder (same format)
		// Background discord scan, suppress agentloop.* telemetry.
		result := mc.runMemoryPipeline(ctx, memorySourceDiscord, convID, summary, "")
		if result == memPipelineSuccess || result == memPipelineSkipped {
			mc.markDiscordProcessed(convID)
			processed++
		} else if result == memPipelineFailed {
			failedIDs = append(failedIDs, convID)
		}
		if result == memPipelineSuccess {
			memoryCount++
		}
	}
	return processed, memoryCount, failedIDs, nil
}

// ── Summary generation ────────────────────────────────────────────────

// generateMemorySummary uses the AgentRunner to write a concise DM notification
// summarizing the cold-start memory extraction.
// platform: "slack" or "feishu"
// memCount: conversations submitted to memory pipeline and marked processed
// totalScanned: total conversations found during scan
func (a *App) generateMemorySummary(ctx context.Context, platform string, memCount, totalScanned int) string {
	a.mu.Lock()
	runner := a.runner
	ready := a.ready
	a.mu.Unlock()

	platformTitle := strings.ToUpper(platform[:1]) + platform[1:]

	if !ready || runner == nil {
		return fmt.Sprintf("✅ %s memory extraction complete\n\nAnalyzed %d conversations, submitted %d for memory extraction.",
			platformTitle, totalScanned, memCount)
	}

	recentFiles := countRecentMemoryFiles(time.Hour)
	alreadyProcessed := totalScanned - memCount

	prompt := fmt.Sprintf(`You just completed a cold-start %s memory extraction.
Stats: scanned %d conversations total; %d submitted to memory extraction this run (%d already processed in prior runs); ~%d new memory files created in the last hour.

Write a brief, friendly DM notification (4-6 lines) that:
1. Confirms memory extraction is complete
2. Shows: scanned N conversations, submitted M for memory extraction (skipped K already processed)
3. Mentions ~%d new memory files created
4. Mentions the user can search their memories

Start with "✅ Memory extraction complete" and use bullet points.
Keep it under 150 words. No markdown headers.`, platform, totalScanned, memCount, alreadyProcessed, recentFiles, recentFiles)

	var result strings.Builder
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority: agent.PriorityBackground,
		Prompt:   prompt,
		// Light model: formatting a short stats notification, no reasoning.
		Model:             a.lightTaskModel(),
		Ephemeral:         true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		if event.Type == "assistant" {
			result.WriteString(event.Content)
		}
		return nil
	})
	if err != nil || result.Len() == 0 {
		return fmt.Sprintf("✅ Memory extraction complete\n\nAnalyzed %d %s conversations and extracted %d memory sessions.",
			totalScanned, platform, memCount)
	}
	return result.String()
}

// countRecentMemoryFiles counts .md files in ~/.yansu-agent/memory/ modified within d.
func countRecentMemoryFiles(d time.Duration) int {
	dir, err := memoryDir()
	if err != nil {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-d)
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			n++
		}
	}
	return n
}
