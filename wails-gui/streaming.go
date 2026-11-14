package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/platform/shellenv"
	"wails-gui/storage"
	"yansu-agent/agent"
	"yansu-agent/projectcontext"
)

// ImageData is a Wails-bound struct for passing image attachments from the frontend.
type ImageData struct {
	MediaType  string `json:"media_type"`
	DataBase64 string `json:"data_base64"`
}

// FileData is a Wails-bound struct for passing non-image file attachments from the frontend.
type FileData struct {
	FileName   string `json:"file_name"`
	MediaType  string `json:"media_type"`
	DataBase64 string `json:"data_base64"`
	FileSize   int64  `json:"file_size"`
}

// pendingTraceIDCleanupDelay is how long after a run completes we keep
// the run's entry in a.pendingTraceIDs so post-run consumers
// (TriggerMemory's 20s debounce, daemon/wechat handler TriggerMemory
// chains) can still read it. The
// delay must comfortably exceed memoryBaseDebounce (20s) so the
// debounce closure sees a populated map even when the chat goroutine
// has already exited. 60s gives 3× margin and is short enough that the
// map never accumulates more than a few seconds of completed-run keys
// at steady state, eliminating the unbounded growth that the original
// "do not delete here" fix introduced for long-running daemons.
const pendingTraceIDCleanupDelay = 60 * time.Second

// schedulePendingTraceIDCleanup arranges for the per-key entry in
// a.pendingTraceIDs to be removed after pendingTraceIDCleanupDelay,
// but ONLY if the value at the key is still the traceID this caller
// just wrote. The compare-and-delete prevents an older run's pending
// timer from wiping a newer run's value when both runs happen on the
// same key inside the 60s window:
//
//	t=0   Run A writes map[K]=tA, schedules cleanup(tA) for t=60
//	t=10  Run B writes map[K]=tB (overwriting tA), schedules cleanup(tB) for t=70
//	t=60  cleanup(tA) fires; observes map[K]==tB != tA, no-ops
//	t=70  cleanup(tB) fires; observes map[K]==tB == tB, deletes
//
// Without the compare, the t=60 cleanup would unconditionally delete
// the key and wipe Run B's still-live trace, sending the next reader
// into the SuppressTelemetry branch. Safe to call from any goroutine;
// timer reference is intentionally discarded — concurrent runs each
// own their own timer and the compare resolves any race between them.
func schedulePendingTraceIDCleanup(a *App, key, traceID string) {
	if key == "" || traceID == "" {
		return
	}
	time.AfterFunc(pendingTraceIDCleanupDelay, func() {
		a.mu.Lock()
		if a.pendingTraceIDs[key] == traceID {
			delete(a.pendingTraceIDs, key)
		}
		a.mu.Unlock()
	})
}

func (a *App) responseLanguageNameFromInput(uiLanguage string) string {
	languageName := strings.TrimSpace(uiLanguage)
	switch languageName {
	case "English", "Simplified Chinese":
		return languageName
	case "":
		return a.currentUILanguageName()
	default:
		return uiLanguageDisplayName(languageName)
	}
}

// PrewarmSession is retained for frontend compatibility but is a no-op for
// the native in-process agent loop — there is no subprocess to warm.
func (a *App) PrewarmSession(workDir string, model string, planMode bool) {
	a.mu.Lock()
	runner := a.runner
	readyCh := a.readyCh
	a.mu.Unlock()

	go func() {
		if runner == nil {
			select {
			case <-readyCh:
				a.mu.Lock()
				runner = a.runner
				a.mu.Unlock()
				if runner == nil {
					return
				}
			case <-time.After(10 * time.Second):
				return
			}
		}

		if strings.TrimSpace(workDir) == "" {
			// Use a sentinel key for standalone-chat (Generate) sessions.
			// executePrompt creates a per-tag temp dir later, so we can't
			// predict the exact path. The sentinel lets consumePrewarmed
			// match any Generate session.
			workDir = ""
		} else if absDir, err := filepath.Abs(workDir); err == nil {
			workDir = absDir
		}

		// The native runner initializes on demand with nothing to warm.
		_ = workDir
		_ = model
		_ = planMode
		_ = runner
	}()
}

func (a *App) SendPrompt(prompt string, workDir string, model string, sessionID string, tag string, planMode bool, thinkingEnabled bool, reasoningEffort string, sessionMode string, remoteEnvJSON string, uiLanguage string, traceID string) error {
	_ = reasoningEffort
	_ = sessionMode
	_ = remoteEnvJSON
	return onceChatSend(&a.chatSendReceipts, tag, traceID, func() error {
		return a.executePrompt(prompt, workDir, model, sessionID, tag, nil, planMode, thinkingEnabled, uiLanguage, nil, nil, traceID, "agentloop.user.send_clicked", promptOriginMac)
	})
}

func (a *App) SendPromptWithImages(prompt string, workDir string, model string, sessionID string, tag string, images []ImageData, planMode bool, thinkingEnabled bool, reasoningEffort string, sessionMode string, remoteEnvJSON string, uiLanguage string, traceID string) error {
	_ = reasoningEffort
	_ = sessionMode
	_ = remoteEnvJSON
	return onceChatSend(&a.chatSendReceipts, tag, traceID, func() error {
		return a.executePrompt(prompt, workDir, model, sessionID, tag, images, planMode, thinkingEnabled, uiLanguage, nil, nil, traceID, "agentloop.user.send_clicked", promptOriginMac)
	})
}

func (a *App) SendPromptWithAttachments(prompt string, workDir string, model string, sessionID string, tag string, images []ImageData, files []FileData, planMode bool, thinkingEnabled bool, reasoningEffort string, sessionMode string, remoteEnvJSON string, uiLanguage string, traceID string) error {
	_ = reasoningEffort
	_ = sessionMode
	_ = remoteEnvJSON
	return onceChatSend(&a.chatSendReceipts, tag, traceID, func() error {
		return a.sendPromptWithAttachments(prompt, workDir, model, sessionID, tag, images, files, planMode, thinkingEnabled, uiLanguage, traceID, nil)
	})
}

func (a *App) sendPromptWithAttachments(prompt string, workDir string, model string, sessionID string, tag string, images []ImageData, files []FileData, planMode bool, thinkingEnabled bool, uiLanguage string, traceID string, promptComposer func(string, string) string, preparations ...func(context.Context) error) error {

	resolvedWorkDir := strings.TrimSpace(workDir)
	if resolvedWorkDir != "" {
		if absDir, err := filepath.Abs(resolvedWorkDir); err == nil {
			resolvedWorkDir = absDir
		}
	} else {
		resolvedWorkDir = a.getOrCreateSessionTempDir(tag)
	}

	augmented := attachmentPrompt(prompt, images, files, resolvedWorkDir)
	return a.executePromptOwned(augmented, prompt, workDir, model, sessionID, tag, images, planMode, thinkingEnabled, uiLanguage, nil, promptComposer, traceID, "agentloop.user.send_clicked", promptOriginMac, nil, preparations...)
}

// attachmentPrompt is the prompt the model reads for attached files and
// images: each file's extracted text in <attached-file> tags, then the words.
func attachmentPrompt(prompt string, images []ImageData, files []FileData, resolvedWorkDir string) string {
	augmented := prompt
	if len(files) > 0 {
		var blocks []string
		for _, f := range files {
			result, err := processFileContent(f.DataBase64, f.FileName, f.MediaType, resolvedWorkDir)
			if err != nil {
				log.Printf("[file-processor] error processing %s: %v", f.FileName, err)
				continue
			}
			block := buildFileContextBlock(f.FileName, f.MediaType, f.FileSize, result.text)
			if block != "" {
				blocks = append(blocks, block)
			}
		}
		if len(blocks) > 0 {
			preamble := "The user has attached file(s). Their text content has been extracted and is included in the <attached-file> tags below. " +
				"Use this content directly — do NOT search for these files on disk.\n\n"
			augmented = preamble + strings.Join(blocks, "\n\n") + "\n\n"
			if len(images) > 0 {
				augmented += fmt.Sprintf("[The user also attached %d image(s) included as visual input above. Analyze them as well.]\n\n", len(images))
			}
			augmented += prompt
		}
	} else if len(images) > 0 {
		augmented = fmt.Sprintf("[The user attached %d image(s) as visual input above.]\n\n", len(images)) + prompt
	}
	return augmented
}

// activeSessionFile is the well-known path where the currently-active
// chat session tag is written so the yansu CLI can read it.
func activeSessionFile() string {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dataRoot, "active-session")
}

// writeActiveSessionFile persists the session tag to a well-known file
// so the yansu CLI can auto-associate cron jobs with the chat that
// created them, without relying on the AI to pass --source-session.
func writeActiveSessionFile(tag string) {
	path := activeSessionFile()
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(tag), 0o644)
}

// removeActiveSessionFile removes the active-session file if it still
// contains the given tag (avoids clobbering a newer session's write).
func removeActiveSessionFile(tag string) {
	path := activeSessionFile()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(data)) == tag {
		_ = os.Remove(path)
	}
}

// executePrompt is the shared implementation for all prompt functions.
// allowedTools restricts available tools when non-nil.
// executePrompt drives a single chat send through the agent runner.
//
// kickoffEvent, when non-empty, is the PostHog event name emitted AFTER
// every pre-flight gate passes (subscription, activeTags collision) and
// BEFORE the agent goroutine launches. This is where the agentloop chain
// starts; emitting earlier (e.g. from the frontend before SendPrompt is
// called) creates phantom kickoff rows when a pre-flight blocks the run.
// Pass "" for callers that emit their own dedicated kickoff (spotlight
// already emits agentloop.spotlight.submitted after its own pre-flight).
// errPromptAlreadyRunning reports that the chat session already has a turn in
// flight. The phone bridge maps it to a busy answer (PROTOCOL.md section 15.3).
var errPromptAlreadyRunning = errors.New("prompt already running for this session")

// errChatDeleted refuses a turn in a chat deleted while something (a finished
// background script) was about to resume it.
var errChatDeleted = errors.New("the chat was deleted")

var errChatSourceAccessChanged = errors.New("source access changed; this chat cannot continue with its previous context")

// promptOrigin says where a chat message came from. The main chat's
// Upcoming tools are for messages typed on the Mac: a message from the
// phone gets none of them, and the reply says the change can be made on
// the Mac. A turn Isoform starts by itself (a finished background script
// resuming the chat) gets no app tools at all.
type promptOrigin string

const (
	promptOriginMac    promptOrigin = ""
	promptOriginPhone  promptOrigin = "phone"
	promptOriginResume promptOrigin = "resume"
	// An Ask initiated by a panel chip can answer, but cannot perform actions.
	promptOriginPanelAsk promptOrigin = "panel_ask"
	promptOriginPhoneAsk promptOrigin = "phone_ask"
)

func isPanelAskOrigin(origin promptOrigin) bool {
	return origin == promptOriginPanelAsk || origin == promptOriginPhoneAsk
}

func newChatRunContext(parent context.Context) (context.Context, context.CancelFunc) {
	const nativeBudget = 30 * time.Minute
	// Two bounded assistant attempts plus a minute for account/control probes
	// and teardown. Native gets its own full budget when the router reaches it;
	// native-only chats are still capped at 30 minutes, not the outer envelope.
	const assistantAllowance = 2*agent.DefaultExternalAttemptTimeout + time.Minute
	ctx := agent.WithNativeFallbackBudget(parent, nativeBudget)
	return context.WithTimeout(ctx, nativeBudget+assistantAllowance)
}

// chatDeleteGuardedLocked reports whether the chat refuses a new turn: a
// delete is running for it, or one already removed it. Call with a.mu held.
func (a *App) chatDeleteGuardedLocked(tag string) bool {
	return a.deletedChats[tag] || a.deletingChats[tag] > 0
}

// beginChatTurn registers Stop before any read-time preparation can block.
// The reservation stays held until preparation fails or the runner finishes.
func (a *App) beginChatTurn(tag string) (context.Context, context.CancelFunc, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.chatDeleteGuardedLocked(tag) {
		return nil, nil, errChatDeleted
	}
	if a.activeTags[tag] {
		return nil, nil, errPromptAlreadyRunning
	}
	if a.activeTags == nil {
		a.activeTags = make(map[string]bool)
	}
	if a.tagCancels == nil {
		a.tagCancels = make(map[string]context.CancelFunc)
	}
	a.activeTags[tag] = true
	binding := &topicTurnBinding{}
	if a.topicHistoryTurns == nil {
		a.topicHistoryTurns = map[string]*topicTurnBinding{}
	}
	a.topicHistoryTurns[tag] = binding
	ctx, cancel := newChatRunContext(context.WithValue(context.Background(), topicTurnContextKey{}, binding))
	a.tagCancels[tag] = cancel
	return ctx, cancel, nil
}

func (a *App) finishChatPreparation(tag string, cancel context.CancelFunc) {
	cancel()
	a.mu.Lock()
	delete(a.activeTags, tag)
	delete(a.tagCancels, tag)
	delete(a.topicHistoryTurns, tag)
	a.mu.Unlock()
}

// sourceBoundChatHistory preserves ordinary history while projecting recent,
// readable topic exchanges. The mobile-offer gate retains its stricter contract.
func (a *App) sourceBoundChatHistory(ctx context.Context, tag string) ([]storage.StreamItemRow, bool, error) {
	if a == nil || a.store == nil || tag == "" {
		return nil, false, nil
	}
	ctx = a.topicHistoryContext(ctx, tag)
	return a.sourceBoundChatHistoryDepth(ctx, tag, map[string]bool{})
}
func (a *App) modelChatHistoryRows(ctx context.Context, tag string) ([]storage.StreamItemRow, error) {
	ctx = a.topicHistoryContext(ctx, tag)
	rows, _, err := a.sourceBoundChatHistory(ctx, tag)
	if err == nil {
		freezeTopicChatHistory(ctx, rows)
	}
	return rows, err
}

func (a *App) sourceBoundChatHistoryDepth(ctx context.Context, tag string, seen map[string]bool) ([]storage.StreamItemRow, bool, error) {
	if a == nil || a.store == nil || tag == "" {
		return nil, false, nil
	}
	if seen[tag] || len(seen) >= 8 {
		return nil, true, errChatSourceAccessChanged
	}
	seen[tag] = true
	rows, err := a.store.LoadStreamItems(ctx, tag)
	if err != nil {
		return nil, false, err
	}
	rows, bound := a.filterTopicChatHistory(ctx, rows)
	session, err := a.store.LoadSession(ctx, tag)
	if err != nil {
		return nil, true, err
	}
	if session != nil && session.Kind == storage.SessionKindSide && session.ParentID != "" {
		_, inherited, parentErr := a.sourceBoundChatHistoryDepth(ctx, session.ParentID, seen)
		if parentErr != nil {
			return nil, true, parentErr
		}
		bound = bound || inherited
	}
	for _, row := range rows {
		if mobileOfferMetadataPresent(row.AutomationContext) {
			bound = true
			break
		}
	}
	if !bound {
		return rows, false, nil
	}
	filtered := a.mobileOfferHistoryRowsDirect(ctx, append([]storage.StreamItemRow(nil), rows...))
	if len(filtered) != len(rows) {
		return nil, true, errChatSourceAccessChanged
	}
	for i := range rows {
		if filtered[i] != rows[i] {
			return nil, true, errChatSourceAccessChanged
		}
	}
	return filtered, true, nil
}

func recentChatConversationRows(rows []storage.StreamItemRow, limit int) []storage.StreamItemRow {
	items := make([]storage.StreamItemRow, 0, limit)
	for _, row := range rows {
		if (row.Kind == "user" || row.Kind == "assistant") && row.Content != "" {
			items = append(items, row)
		}
	}
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	return items
}

func (a *App) executePrompt(prompt string, workDir string, model string, sessionID string, tag string, images []ImageData, planMode bool, thinkingEnabled bool, uiLanguage string, allowedTools []string, promptComposer func(string, string) string, traceID string, kickoffEvent string, origin promptOrigin, preparations ...func(context.Context) error) error {
	return a.executePromptOwned(prompt, prompt, workDir, model, sessionID, tag, images, planMode, thinkingEnabled, uiLanguage, allowedTools, promptComposer, traceID, kickoffEvent, origin, nil, preparations...)
}

func (a *App) executePromptOwned(prompt string, userPrompt string, workDir string, model string, sessionID string, tag string, images []ImageData, planMode bool, thinkingEnabled bool, uiLanguage string, allowedTools []string, promptComposer func(string, string) string, traceID string, kickoffEvent string, origin promptOrigin, owner *bgShellSessionInfo, preparations ...func(context.Context) error) error {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return fmt.Errorf("prompt is required")
	}
	// Nothing leaves the computer without an AI: no runner is called.
	if !chatSendAIReady(a) {
		return a.keepChatSend(tag)
	}
	ctx, cancel, err := a.beginChatTurn(tag)
	if err != nil {
		return err
	}
	turnSource := &todayTurnSource{}
	ctx = context.WithValue(ctx, todayTurnSourceKey{}, turnSource)
	for _, prepare := range preparations {
		if err := prepare(ctx); err != nil {
			a.finishChatPreparation(tag, cancel)
			return err
		}
	}
	initialRows, initiallySourceBound, err := a.sourceBoundChatHistory(ctx, tag)
	if err != nil {
		a.finishChatPreparation(tag, cancel)
		return err
	}
	var promptSource string
	if len(initialRows) > 0 {
		last := initialRows[len(initialRows)-1]
		if last.Kind == "user" && mobileOfferMetadataPresent(last.AutomationContext) {
			promptSource = last.AutomationContext
		}
	}
	checkSourcePrompt := func(checkCtx context.Context) error {
		if err := a.validateTurnTopicContext(checkCtx); err != nil {
			return err
		}
		if promptSource == "" {
			return nil
		}
		current, allowed := a.mobileOfferStoredText(checkCtx, trimmed, promptSource)
		if !allowed || current != trimmed {
			return errChatSourceAccessChanged
		}
		return nil
	}
	if err := checkSourcePrompt(ctx); err != nil {
		a.finishChatPreparation(tag, cancel)
		return err
	}
	if err := ctx.Err(); err != nil {
		a.finishChatPreparation(tag, cancel)
		return err
	}
	a.mu.Lock()
	runner := a.runner
	readyCh := a.readyCh
	// Make the traceID available to TriggerMemory calls that fire during
	// this run so the memory pipeline can be correlated back to the
	// originating agentloop.user.send_clicked event in PostHog.
	if traceID != "" {
		a.pendingTraceIDs[tag] = traceID
	}
	a.mu.Unlock()
	// Read after the checks above, so a deleted or busy chat is refused
	// without waiting on the store.
	languageName := a.responseLanguageNameFromInput(uiLanguage)

	// When the user has opted out of telemetry (tier == "off"), blank the
	// traceID so downstream consumers — the kickoff event, the agent loop
	// (which stamps x-yansu-trace-id on outbound HTTP calls), and the
	// backend's maybeCaptureConversation (which gates on traceID != "") —
	// all skip. This mirrors what SuppressTelemetry does for background
	// runs, but for user-initiated opt-out. pendingTraceIDs was already
	// written above (memory pipeline is device-local, not backend
	// telemetry) and is deliberately left intact.
	if currentTelemetryTier() == TelemetryTierOff {
		traceID = ""
	}

	// Count admitted human turns only. Background script continuations have
	// no kickoff, trace or phone origin; no prompt or session data is sent.
	if a.telemetry != nil && (kickoffEvent != "" || traceID != "" || origin == promptOriginPhone || origin == promptOriginPhoneAsk) {
		a.telemetry.Capture("chat.turn_sent", todayTurnProperties(turnSource))
	}

	// Emit the kickoff event now — every pre-flight gate has passed and
	// the agent run is about to start. Emitting earlier (e.g. from
	// frontend useComposerInput before SendPrompt is called) used to
	// produce orphan kickoff rows in PostHog when the subscription
	// pre-flight or the activeTags collision short-circuited the run.
	// kickoffEvent is empty for callers that emit their own dedicated
	// kickoff (spotlight emits agentloop.spotlight.submitted instead).
	if kickoffEvent != "" && a.telemetry != nil && traceID != "" {
		a.telemetry.Capture(kickoffEvent, map[string]any{
			"trace_id":         traceID,
			"session_id":       sessionID,
			"session_local_id": tag,
			"model":            model,
			"plan_mode":        planMode,
			"thinking_enabled": thinkingEnabled,
			"has_images":       len(images) > 0,
			"prompt_length":    len(trimmed),
		})
	}

	// Write active session tag to a well-known file so the yansu CLI
	// can auto-associate cron jobs with the originating chat session
	// without relying on the AI to pass --source-session.
	writeActiveSessionFile(tag)

	// Resolve absolute path for work dir.
	// For standalone chat sessions (Generate), workDir is empty — create a
	// per-session temp directory so the agent has a dedicated sandbox.
	// Capture whether the *caller* supplied a real project workDir before we
	// rewrite it, so the skill-hub domain classification below stays
	// faithful: chat sessions get the chat-domain skills, project sessions
	// get the project-domain skills.
	hasProjectDir := strings.TrimSpace(workDir) != ""
	if hasProjectDir {
		if absDir, err := filepath.Abs(workDir); err == nil {
			workDir = absDir
		}
	} else {
		workDir = a.getOrCreateSessionTempDir(tag)
	}

	work := a.handoffEntryForSession(tag)
	if owner != nil && owner.HandoffID != "" && (work == nil || work.ID != owner.HandoffID || work.AttemptID != owner.AttemptID) {
		a.finishChatPreparation(tag, cancel)
		return errors.New("background script attempt is no longer current")
	}
	if work != nil && work.Status != handoff.StatusInProgress {
		if origin == promptOriginResume && (work.Status == handoff.StatusAborted || work.Status == handoff.StatusBlocked) {
			a.finishChatPreparation(tag, cancel)
			return errors.New("one-time work is stopped or blocked")
		}
		work = nil
	}
	if err := a.registerHandoffRun(work, workDir, cancel); err != nil {
		a.finishChatPreparation(tag, cancel)
		return err
	}
	chipUserID := ""
	admissionUserID := ""
	filterOpenAnswers := work == nil && owner == nil && a.isMainChatSession(ctx, tag)
	if work == nil && owner == nil {
		admissionUserID = a.attendedMainChatUserID(ctx, tag, initialRows, origin, kickoffEvent, trimmed)
		chipUserID = a.answerChipUser(ctx, tag, initialRows, origin, kickoffEvent, trimmed)
		if filterOpenAnswers && admissionUserID != "" && promptComposer == nil && len(preparations) == 0 {
			a.scheduleOpenAnswerAskPromotion(tag, admissionUserID, userPrompt)
		}
	}
	candidateQuestion := openAnswerTurnQuestion(initialRows, admissionUserID, userPrompt)
	candidatePrompt := filterOpenAnswers && candidateQuestion != "" && promptComposer == nil
	// Short role names ("opus", "sonnet", "haiku") pass through as-is;
	// the NativeAgentRunner resolves them via its ModelAliases table.
	//
	// HistoryKey is the frontend's sessionLocalID (tag) — stream_items
	// are keyed by it, so the runner's HistoryLoader needs it to find
	// prior turns in this conversation.
	currentTopicTurn := false
	if binding := topicTurnFromContext(ctx); binding != nil {
		currentTopicTurn = binding.userItemID != ""
	}
	input := &agent.TaskInput{
		WorkDir:                 workDir,
		Model:                   model,
		SessionID:               sessionID,
		HistoryKey:              tag,
		BackendHint:             a.chatBackendHint(tag),
		PlanMode:                planMode,
		ThinkingEnabled:         thinkingEnabled,
		AllowedTools:            allowedTools,
		NoTools:                 isPanelAskOrigin(origin),
		ProgrammaticToolCalling: !isPanelAskOrigin(origin),
		RequireNativeDispatch:   initiallySourceBound || currentTopicTurn,
		// Attended chat: a person can answer a prompt, so the Settings
		// toggle decides whether risky Bash commands ask first.
		AskBeforeRiskyCommands: askBeforeRiskyCommands(),
		// TraceID is the cross-process correlation id generated by the
		// frontend at the moment the user hit Send. The agent loop
		// stamps it onto every emitted telemetry event and on the
		// x-yansu-trace-id header of every outbound HTTP request so
		// backend logs (Loki) and downstream events (PostHog) can be
		// stitched together. Empty (legacy callers / tests) is fine —
		// the loop just doesn't emit a trace_id property.
		TraceID: traceID,
	}
	listBackend := listHandoffBackend(work)
	if listBackend != "" {
		input.ForceBackend = listBackend
		input.DenyExternalWrites = true
	}
	refreshTopicSession := func() error {
		if a.store == nil {
			return nil
		}
		current, err := a.topicChatSession(ctx, tag, sessionID)
		if err == nil {
			input.SessionID = current
		}
		return err
	}
	if err := refreshTopicSession(); err != nil {
		a.finishChatPreparation(tag, cancel)
		return err
	}
	input.ExtraTools = append(a.mainChatExtraTools(tag, origin), a.mainChatDraftTools(tag, origin)...)
	input.BeforeDispatch = func(dispatchCtx context.Context) error {
		if err := dispatchCtx.Err(); err != nil {
			return err
		}
		// Retries can outlive the turn. Keep its proofs/cache, but honor the
		// dispatch lifetime rather than the completed turn's cancellation.
		if binding := topicTurnFromContext(ctx); binding != nil {
			dispatchCtx = context.WithValue(dispatchCtx, topicTurnContextKey{}, binding)
		}
		if binding := topicTurnFromContext(dispatchCtx); binding != nil && binding.userItemID != "" {
			row, err := a.store.LoadStreamItem(dispatchCtx, binding.userItemID)
			if err != nil || row == nil || row.SessionLocalID != tag || row.Kind != "user" || row.AutomationContext != binding.metadata || binding.metadata == "" {
				return errChatSourceAccessChanged
			}
		}
		if _, _, err := a.sourceBoundChatHistory(dispatchCtx, tag); err != nil {
			return err
		}
		// The runner has already selected its todo/plan state. Persist any
		// newly detected withdrawal before refusing this request, so a retry or
		// restart uses the replacement even if the old rows subsequently age out.
		current, err := a.topicChatSession(dispatchCtx, tag, sessionID)
		if err != nil {
			return err
		}
		if current != input.SessionID {
			return errChatSourceAccessChanged
		}
		if turnHasFrozenTopicHistory(dispatchCtx) && !input.RequireNativeDispatch {
			return errChatSourceAccessChanged
		}
		if err := checkSourcePrompt(dispatchCtx); err != nil {
			return err
		}
		return nil
	}

	// NativeAgentRunner has no session-init bootstrap, so everything here
	// runs synchronously before the first HTTP POST. Until composePrompt's
	// injects are themselves fast or parallelized, total PromptFunc time is
	// added to first-token latency. The [prompt-func] log line below makes
	// this visible.
	capturedSessionID := sessionID
	capturedTag := tag
	capturedSkillDomain := skillHubDomainChat
	capturedLanguageName := languageName
	if hasProjectDir {
		capturedSkillDomain = skillHubDomainProject
	}
	composeTurn := func(external bool) (string, string) {
		historyCtx := context.WithoutCancel(ctx)
		pfStart := time.Now()
		// Deferred prompt composition can read slow stores or await enrichment.
		// Recheck on both sides so it cannot freeze newly withdrawn source text
		// into the router's reusable prompt/history for a provider fallback.
		if _, _, err := a.sourceBoundChatHistory(historyCtx, capturedTag); err != nil {
			cancel()
			return "", ""
		}
		if err := checkSourcePrompt(historyCtx); err != nil {
			cancel()
			return "", ""
		}
		var promptWithMemory string
		if work != nil {
			// One-time work sends only what the task needs: the brief, its
			// key files, the working directory and the result contract. The
			// main chat's personal context bundle (memory, people, activity,
			// soul, focused app) never reaches a handoff run.
			promptWithMemory = composeHandoffPrompt(trimmed, *work, workDir)
		} else if promptComposer != nil {
			composerStart := time.Now()
			promptWithMemory = promptComposer(trimmed, capturedLanguageName)
			if d := time.Since(composerStart); d > 50*time.Millisecond {
				log.Printf("[prompt-func][SLOW] promptComposer took %v", d)
			}
		} else {
			opts := []string{capturedTag, "", capturedSkillDomain, capturedLanguageName, string(origin)}
			if external {
				opts = append(opts, "external-chat")
			}
			promptWithMemory = a.composePrompt(trimmed, opts...)
		}
		if candidatePrompt {
			promptWithMemory = openAnswerCandidateDirective + promptWithMemory
		}
		if filterOpenAnswers && admissionUserID != "" && promptComposer == nil {
			promptWithMemory = a.openAnswerChangePrompt(historyCtx, capturedTag, capturedLanguageName) + promptWithMemory
		}
		if hasProjectDir {
			promptWithMemory = projectcontext.WithPrompt(promptWithMemory, workDir)
		}
		rows, _, err := a.sourceBoundChatHistory(historyCtx, capturedTag)
		if err != nil {
			cancel()
			return "", ""
		}
		if err := checkSourcePrompt(historyCtx); err != nil {
			cancel()
			return "", ""
		}
		var history string
		if capturedSessionID != "" || input.SessionID != sessionID {
			textRows := recentChatConversationRows(rows, 30)
			freezeTopicChatHistory(ctx, textRows)
			history = formatConversationHistory(textRows)
		}
		log.Printf("[prompt-func] total=%v prompt-bytes=%d history-bytes=%d", time.Since(pfStart), len(promptWithMemory), len(history))
		return promptWithMemory, history
	}

	input.PromptFunc = func() (string, string) { return composeTurn(false) }
	// Today/source-bound turns, handoffs, quick chat and connector composers
	// retain their existing task-specific composition and routing contracts.
	if work == nil && promptComposer == nil &&
		a.isMainChatSession(ctx, tag) && origin != promptOriginResume &&
		!hasFocusedAppContext(tag) && mainChatContextToolsAllowed(input) {
		input.ExternalPromptFunc = func() (string, string) { return composeTurn(true) }
		input.ExternalExtraTools = a.chatContextTools(tag, capturedLanguageName)
	}

	for _, img := range images {
		if img.MediaType != "" && img.DataBase64 != "" {
			input.Images = append(input.Images, agent.ImageAttachment{
				MediaType:  img.MediaType,
				DataBase64: img.DataBase64,
			})
		}
	}

	// Phase 1.5: wire the Context Engine snapshot collector and the
	// PII text redactor so step.llm.* events carry L1+L2 fields
	// (gated by the user's telemetry tier setting). The template
	// name is a coarse identifier — plan vs. default — which is the
	// one bit of compositional state the loop can't reconstruct on
	// its own. The pii.RedactText-backed redactor only fires for
	// detailed-tier upstream error bodies on step.llm.error events.
	template := "default"
	if planMode {
		template = "plan_mode"
	}
	input.ContextEngineCollector = a.buildCEContextCollector(template)
	input.Redactor = buildCERedactor()

	go func() {
		a.beginDesktopTurn(tag, origin)
		panelPendingUserID := admissionUserID
		var trackedRunID string
		var chipAnswer *finishedSegment
		pendingBashCmds := make(map[string]string)
		sawErrorEvent := false
		var runErr error
		scheduledPanelAnswer := false
		var handoffTranscript strings.Builder
		turnLog := newChatTurnLog(work != nil, origin, time.Now())

		defer func() {
			log.Print(turnLog.line(ctx.Err(), sawErrorEvent, runErr, tag, time.Now()))
			if isPanelAskOrigin(origin) && panelPendingUserID != "" && !scheduledPanelAnswer {
				a.cancelPanelAskAnswer(panelPendingUserID)
			}
			// Stop, quitting and an agent restart cancel the run; a run that
			// ended or ran out of time leaves ctx uncancelled or past its
			// deadline. Read before cancel() below.
			stopped := errors.Is(ctx.Err(), context.Canceled)
			a.finishDesktopTurn(tag, stopped)
			// Settling a Handoff may call the verifier model, so it runs beside
			// the cleanup below rather than delaying it.
			if work != nil {
				snapshot := *work
				go a.markHandoffAttemptEnded(snapshot, ctx.Err() == nil && !sawErrorEvent, handoffTranscript.String(), runErr)
			}
			cancel()
			a.mu.Lock()
			delete(a.activeTags, tag)
			delete(a.tagCancels, tag)
			delete(a.topicHistoryTurns, tag)
			// A stopped run drops the background scripts that finished
			// during it, here under the lock, so a finish arriving once the
			// chat is free cannot resume them.
			var droppedBgShells []bgShellFinish
			if stopped && a.bgShells != nil {
				droppedBgShells = a.bgShells.TakePending(tag)
			}
			if trackedRunID != "" {
				delete(a.streams, trackedRunID)
				if a.runIDByTag[tag] == trackedRunID {
					delete(a.runIDByTag, tag)
				}
			}
			a.mu.Unlock()
			removeActiveSessionFile(tag)
			// Background scripts that finished during the run resume the
			// chat now, unless the run was stopped.
			if stopped {
				if len(droppedBgShells) > 0 {
					log.Printf("[bg-shell-done] tag=%s run stopped, not resuming %d finished script(s)", tag, len(droppedBgShells))
				}
			} else {
				go a.afterRunBgShells(tag)
			}
			// Defer cleanup of pendingTraceIDs[tag] for
			// pendingTraceIDCleanupDelay (60s). Doing it inline
			// here races TriggerMemory, which the
			// frontend fires on the streaming→idle transition AND
			// the memory pipeline's 20s debounce reads later still
			// — eager delete made every read lose and dropped the
			// trace into the SuppressTelemetry branch. The delay
			// must comfortably exceed memoryBaseDebounce (20s) so
			// the debounce closure can capture the trace at
			// schedule-time AND the closure can fire. Pass traceID
			// so the cleanup compares-and-deletes — a follow-up
			// send on the same tag inside 60s will have overwritten
			// the value, and this run's stale timer must not wipe
			// the newer run's trace.
			schedulePendingTraceIDCleanup(a, tag, traceID)
		}()

		// Wait for agent to become ready if currently starting/restarting.
		if runner == nil {
			select {
			case <-readyCh:
				a.mu.Lock()
				runner = a.runner
				a.mu.Unlock()
				if runner == nil {
					a.emitAgentEvent(AgentEvent{Type: "error", Tag: tag, Error: "agent failed to initialize"})
					return
				}
			case <-time.After(15 * time.Second):
				a.emitAgentEvent(AgentEvent{Type: "error", Tag: tag, Error: "agent is not ready, please try again"})
				return
			case <-ctx.Done():
				return
			}
		}

		// Determine current turn's sort_order for file checkpointing.
		// tag == sessionLocalID (set by frontend: useComposerInput.ts:51).
		checkpointSortOrder := -1
		if a.store != nil {
			if order, err := a.store.GetMaxSortOrder(context.Background(), tag); err == nil {
				checkpointSortOrder = order
			} else {
				log.Printf("[checkpoint] GetMaxSortOrder failed for tag=%s: %v", tag, err)
			}
		} else {
			log.Printf("[checkpoint] store is nil, skipping checkpoint setup")
		}
		log.Printf("[checkpoint] turn starting: tag=%s checkpointSortOrder=%d model=%s workDir=%s", tag, checkpointSortOrder, model, workDir)
		backedUp := make(map[string]bool)

		// Create a git snapshot before the turn starts to capture the entire
		// worktree state. This is done for ALL sessions (not just Codex) because
		// any model can modify files via Bash commands, npm scripts, etc. that
		// per-file backup cannot intercept. Git snapshot is the only reliable way
		// to capture and restore the complete file state (same approach as Cursor).
		// Per-file backup is kept as a fallback for non-git directories only.
		hasGitSnapshot := false
		if a.store != nil && checkpointSortOrder >= 0 && workDir != "" && isGitRepo(workDir) {
			commitSHA, headSHA, untracked, err := createGitSnapshot(workDir)
			if err != nil {
				log.Printf("[git-snapshot] create failed (non-fatal): %v", err)
			} else {
				if err := a.store.SaveGitSnapshot(context.Background(), storage.GitSnapshotRow{
					SessionLocalID: tag,
					SortOrder:      checkpointSortOrder,
					StashSHA:       commitSHA,
					HeadSHA:        headSHA,
					UntrackedFiles: untracked,
					WorkDir:        workDir,
					CreatedAt:      time.Now().UnixMilli(),
				}); err != nil {
					log.Printf("[git-snapshot] DB save failed (non-fatal): %v", err)
				} else {
					hasGitSnapshot = true
					log.Printf("[git-snapshot] saved to DB: tag=%s sortOrder=%d commitSHA=%q headSHA=%s",
						tag, checkpointSortOrder, commitSHA, headSHA)
				}
			}
		} else if workDir != "" {
			log.Printf("[checkpoint] git snapshot skipped: store=%v sortOrder=%d isGitRepo=%v",
				a.store != nil, checkpointSortOrder, isGitRepo(workDir))
		}

		if ctx.Err() != nil {
			a.emitAgentEvent(AgentEvent{Type: "done", Tag: tag})
			a.emitAgentEvent(AgentEvent{Type: "close", Tag: tag})
			return
		}
		// Source-bound turns resolve their deferred context before entering any
		// runner, including one that would otherwise resume a private CLI cache.
		// The last check also covers time spent waiting for runner initialization
		// and creating the file checkpoint above.
		_, sourceBound, sourceErr := a.sourceBoundChatHistory(ctx, tag)
		input.RequireNativeDispatch = sourceBound || currentTopicTurn || promptSource != ""
		if sourceErr == nil {
			sourceErr = checkSourcePrompt(ctx)
		}
		if sourceErr == nil && sourceBound && input.PromptFunc != nil {
			input.Prompt, input.History = input.PromptFunc()
			input.PromptFunc = nil
			var remainingBound bool
			_, remainingBound, sourceErr = a.sourceBoundChatHistory(ctx, tag)
			input.RequireNativeDispatch = remainingBound || currentTopicTurn || promptSource != "" || turnHasFrozenTopicHistory(ctx)
		}
		if sourceErr == nil {
			sourceErr = refreshTopicSession()
		}
		if sourceErr != nil || ctx.Err() != nil {
			sawErrorEvent = true
			cancel()
			a.emitAgentEvent(AgentEvent{Type: "error", Tag: tag, Error: errChatSourceAccessChanged.Error()})
			a.emitAgentEvent(AgentEvent{Type: "close", Tag: tag})
			return
		}
		var answerStream openAnswerStream
		if candidatePrompt {
			answerStream.question = candidateQuestion
		}
		if listBackend != "" {
			if _, ok := runner.(*agent.RoutingAgentRunner); !ok {
				sawErrorEvent = true
				a.emitAgentEvent(a.chatFailureEvent(tag, "", errListRunnerUnavailable, listBackend))
				a.emitAgentEvent(AgentEvent{Type: "close", Tag: tag})
				return
			}
		}
		runID, err := runner.ExecuteTask(ctx, input, func(event *agent.StreamEvent) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			turnLog.note(event)
			if err := checkListHandoffRoute(listBackend, event); err != nil {
				return err
			}

			a.touchHandoffRun(work, time.Now())
			a.noteHandoffRunRoute(work, event)
			if work != nil {
				switch event.Type {
				case "assistant":
					handoffTranscript.WriteString(event.Content)
				case "tool_use":
					handoffTranscript.Reset()
				}
			}

			// The gateway reports which account served the call. Record it
			// for the model accounts view; the chat itself never sees it.
			if event.Type == "model_route" {
				a.recordModelRoute("chat", model, input, event)
				return nil
			}

			if event.Type == "auth_error" {
				// The Yansu gateway is retired, so gateway-based recovery is
				// gone. Two cases remain:
				//
				//  1. External agent (Claude Code / Codex): the CLI carries its
				//     own login, and an auth error usually means that login has
				//     expired. "Run 'claude login'" is a genuinely actionable
				//     recovery here, so let the error reach the frontend and
				//     render the auth error card.
				//
				//  2. Native runner (gateway or user BYOK): with the gateway
				//     gone, an auth error means the user has not configured
				//     their own endpoint (or their key is wrong). The
				//     retirement banner already nudges them to Settings → AI
				//     Provider, so suppress the raw error and stay silent
				//     rather than showing a misleading "reconnect / claude
				//     login" card for a gateway that no longer exists.
				if _, ext := externalAgentActive(); ext {
					log.Printf("[agent] auth error from external agent tag=%s error=%q — surfacing for 'claude login' recovery", tag, event.Error)
					event.Type = "error"
					sawErrorEvent = true
					// Fall through to emit the error to the frontend.
				} else {
					log.Printf("[agent] auth error on native runner (gateway retired) tag=%s error=%q — staying silent, retirement banner guides the user", tag, event.Error)
					go a.authManager.RecoverAuth() // no-op when gateway is off
					sawErrorEvent = true
					return nil
				}
			}

			if event.Type == "error" {
				sawErrorEvent = true
				log.Printf("[agent] error event tag=%s session=%s error=%q", tag, event.SessionID, event.Error)

				// Safety net: catch any auth-related errors that arrive through
				// paths other than the runner's isAuthError classification.
				// Skip if sub2api is already active — re-triggering recovery
				// would be a no-op and the error should reach the user.
				if isFallbackError(event.Error) && !a.authManager.IsActive() {
					go a.authManager.RecoverAuth()
				}
			}

			// Per-file backup as fallback for non-git directories.
			// When a git snapshot was taken, skip per-file backup since the
			// snapshot already captures the complete worktree state.
			if event.Type == "tool_use" {
				log.Printf("[checkpoint] tool_use event: tool=%s checkpointSortOrder=%d tag=%s", event.ToolName, checkpointSortOrder, tag)
				if !hasGitSnapshot && checkpointSortOrder >= 0 {
					a.maybeBackupFile(tag, checkpointSortOrder+1, event.ToolName, event.ToolInput, backedUp)
				}
			}

			// Persist a compaction boundary on BOTH success (compact_end)
			// and failure (compact_error) so the next session resume can
			// skip the pre-cut items rather than reloading the full,
			// uncompacted history. Without persisting on compact_error,
			// the in-memory dropOldestHalf takes effect for the active
			// run, but a restart immediately re-balloons context and
			// re-triggers compaction (which re-fails the same way) — a
			// silent cascade that bricks the session.
			//
			// On compact_error, event.Content is empty;
			// persistCompactionBoundary substitutes a fallback notice so
			// the synthesised user message at resume time explains the
			// truncation rather than appearing as an empty turn.
			if event.Type == "compact_end" || event.Type == "compact_error" {
				a.persistCompactionBoundary(tag, event.Content)
			}

			evt := AgentEvent{
				Type:              event.Type,
				RunID:             event.RunID,
				SessionID:         event.SessionID,
				Tag:               tag,
				Content:           event.Content,
				ToolName:          event.ToolName,
				ToolUseID:         event.ToolUseID,
				AvailableCommands: event.AvailableCommands,
				Error:             event.Error,
				PermissionOptions: event.PermissionOptions,
			}
			evt = a.withChatCause(evt, event.Status)

			if event.ToolInput != nil {
				if encoded, err := json.Marshal(event.ToolInput); err == nil {
					evt.ToolInput = encoded
				}
			}
			if event.Type == "permission_request" {
				evt.UpcomingApproval = a.upcomingApprovalFor(event.ToolName, evt.ToolInput)
			}

			if event.Type == "available_commands" {
				log.Printf(
					"[slash-debug] emit agent:event available_commands tag=%s session=%s commands=%d",
					tag,
					event.SessionID,
					len(event.AvailableCommands),
				)
			}

			// Track background shells: capture Bash command from tool_use,
			// register when tool_result confirms a background shell started.
			// Only a run_in_background call starts a shell; a foreground
			// command's output can contain "shell_id=" too.
			if event.Type == "tool_use" && event.ToolName == "Bash" && event.ToolInput != nil {
				if m, ok := event.ToolInput.(map[string]any); ok {
					if background, _ := m["run_in_background"].(bool); background && event.ToolUseID != "" {
						cmd, _ := m["command"].(string)
						pendingBashCmds[event.ToolUseID] = cmd
					}
				}
			}
			if event.Type == "tool_result" && event.ToolName == "Bash" {
				if cmd, background := pendingBashCmds[event.ToolUseID]; background {
					delete(pendingBashCmds, event.ToolUseID)
					a.registerBgShellFromToolResult(tag, sessionID, workDir, model, event.ToolName, event.Content, cmd, work)
				}
			}

			// Register cancel in streams map when we learn the run ID
			if event.Type == "run_created" && event.RunID != "" {
				trackedRunID = event.RunID
				binding := topicTurnFromContext(ctx)
				if binding != nil && binding.userItemID != "" {
					if err := a.store.BindStreamItemTopicRun(ctx, tag, binding.userItemID, event.RunID); err != nil {
						// Native runners may ignore a run_created callback error.
						// Cancel independently so dispatch cannot outlive its seed.
						cancel()
						return err
					}
				}
				runBindingUserID := attendedMainChatRunBindingUserID(admissionUserID, chipUserID)
				if runBindingUserID != "" && (binding == nil || binding.userItemID == "") {
					if err := a.store.BindAnswerChipUserRun(ctx, tag, runBindingUserID, event.RunID); err != nil {
						chipUserID = ""
						admissionUserID = ""
						answerStream.question = ""
					}
				}
				a.mu.Lock()
				a.streams[event.RunID] = cancel
				a.runIDByTag[tag] = event.RunID
				a.mu.Unlock()
			}

			if filterOpenAnswers {
				answerStream.emit(evt, a.emitAgentEvent)
			} else {
				a.emitAgentEvent(evt)
			}
			switch event.Type {
			case "assistant":
				if admissionUserID != "" || candidatePrompt {
					chipAnswer = a.answerChipSegment(tag)
				}
			case "run_created", "tool_use_start", "tool_use":
				chipAnswer = nil
			}
			return nil
		})
		runErr = err
		if filterOpenAnswers {
			answerStream.finish(a.emitAgentEvent)
		}
		if err == nil && ctx.Err() == nil && !sawErrorEvent {
			a.scheduleAnswerChips(chipUserID, trackedRunID, chipAnswer)
			if isPanelAskOrigin(origin) && admissionUserID != "" && trackedRunID != "" && chipAnswer != nil {
				userID, runID, answerID, answerText := admissionUserID, trackedRunID, chipAnswer.id, chipAnswer.content
				a.schedulePanelAskAnswer(tag, userID, runID, answerID, answerText)
				scheduledPanelAnswer = true
			}
			if admission, ok := resolveCompletedOpenAnswerAdmission(
				admissionUserID,
				trackedRunID,
				chipAnswer,
				answerStream.completedCandidate(),
			); ok {
				time.AfterFunc(agentSegmentWriteDelay, func() {
					a.admitOpenAnswer(
						admission.tag,
						admission.userID,
						admission.runID,
						admission.assistantSegmentID,
						admission.candidate,
					)
				})
			}
			if change, ok := resolveCompletedOpenAnswerChange(
				tag,
				admissionUserID,
				trackedRunID,
				answerStream.completedChange(),
			); ok {
				time.AfterFunc(agentSegmentWriteDelay, func() {
					a.applyOpenAnswerChange(change)
				})
			}
		}

		if err != nil && ctx.Err() == nil && !sawErrorEvent {
			sawErrorEvent = true
			log.Printf("[agent] ExecuteTask failed tag=%s runID=%s error=%q", tag, trackedRunID, err.Error())
			if trackedRunID == "" {
				trackedRunID = runID
			}
			a.emitAgentEvent(a.chatFailureEvent(tag, trackedRunID, err, listBackend))
		}

		// When cancelled, the callback's ctx.Done() guard blocks all terminal
		// events (error/done/close) from reaching the frontend. Emit both
		// done and close so the UI properly exits the streaming state and
		// triggers plan-mode auto-show logic that depends on the done event.
		if ctx.Err() != nil {
			doneEvt := AgentEvent{Type: "done", Tag: tag, RunID: trackedRunID}
			if ctx.Err() == context.DeadlineExceeded && a.bgShells != nil {
				bgActive := a.bgShells.ActiveForSession(tag)
				if len(bgActive) > 0 {
					doneEvt.Content = fmt.Sprintf("run_paused:wall_time:bg_shells=%d", len(bgActive))
				} else {
					doneEvt.Content = "run_paused:wall_time"
				}
			}
			a.emitAgentEvent(doneEvt)
			a.emitAgentEvent(AgentEvent{Type: "close", Tag: tag})
		}
	}()

	return nil
}

func (a *App) CancelStream(runID string) {
	a.mu.Lock()
	if cancel, ok := a.streams[runID]; ok {
		cancel()
		delete(a.streams, runID)
	}
	runner := a.runner
	a.mu.Unlock()

	if runner != nil && runID != "" {
		runner.CancelRun(runID)
	}
}

// CancelByTag cancels a running prompt by its session tag (localId).
// This is used when the frontend doesn't yet have a runID (e.g. the user
// clicks Stop before the run_created event has been flushed to React state).
func (a *App) CancelByTag(tag string) {
	a.mu.Lock()
	cancel, ok := a.tagCancels[tag]
	if ok {
		cancel()
		delete(a.tagCancels, tag)
		delete(a.topicHistoryTurns, tag)
	}
	a.mu.Unlock()
}

// cancelActiveChatTurn stops the chat turn running for a session tag the way
// the Stop button does: once run_created has arrived the run id cancels the
// turn and asks the runner to stop the run; before that the session tag
// cancels the turn's context.
func (a *App) cancelActiveChatTurn(sessionTag string) {
	if sessionTag == "" {
		return
	}
	a.mu.Lock()
	runID := a.runIDByTag[sessionTag]
	a.mu.Unlock()
	if runID != "" {
		a.CancelStream(runID)
		return
	}
	a.CancelByTag(sessionTag)
}

// deleteSessionRunStopTimeout bounds how long DeleteSessionData waits for the
// chat's cancelled run to unwind before it removes the chat anyway.
const deleteSessionRunStopTimeout = 5 * time.Second

// waitForChatTurnEnd waits until no run holds the tag, bounded by
// deleteSessionRunStopTimeout. CancelStream and CancelByTag only signal the
// run's context; the run's deferred cleanup drops activeTags[tag] once it has
// unwound, which is what this waits for.
func (a *App) waitForChatTurnEnd(tag string) {
	deadline := time.Now().Add(deleteSessionRunStopTimeout)
	for {
		a.mu.Lock()
		active := a.activeTags[tag]
		a.mu.Unlock()
		if !active {
			return
		}
		if time.Now().After(deadline) {
			log.Printf("[delete-session] tag=%s run still active after %s, deleting anyway", tag, deleteSessionRunStopTimeout)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sanitizeSessionTag reduces a frontend-supplied session tag to a single safe
// path segment for use as a directory name: alphanumerics, dash, and
// underscore are kept; everything else (including path separators and dots) is
// replaced with '_', so no value can produce traversal or escape the chats
// root. Returns "" for an empty/whitespace tag.
func sanitizeSessionTag(tag string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(tag) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// chatSessionWorkDir returns the stable per-session work directory for a
// standalone chat (no pinned project). Files live under
// $HOME/.isoform-workspaces/<flavor>/chats/<tag>/ so they survive app restart, stay
// addressable from the UI (deliverables reopen after a relaunch), and are not
// swept by the OS the way $TMPDIR is. Mirrors defaultAutomationWorkDir.
func (a *App) chatSessionWorkDir(tag string) (string, error) {
	safe := sanitizeSessionTag(tag)
	if safe == "" {
		return "", fmt.Errorf("invalid session tag")
	}
	return agentWorkspacePath("chats", safe)
}

type sessionWorkDirLock struct {
	mu       sync.Mutex
	refCount int
}

// lockSessionWorkDir follows the connector per-thread lock pattern. The
// registry lock is released before waiting or doing filesystem work. Tags
// that normalize to the same folder also share exclusion.
func (a *App) lockSessionWorkDir(tag string) func() {
	key := sanitizeSessionTag(tag)
	a.sessionWorkDirMu.Lock()
	if a.sessionWorkDirLocks == nil {
		a.sessionWorkDirLocks = make(map[string]*sessionWorkDirLock)
	}
	entry := a.sessionWorkDirLocks[key]
	if entry == nil {
		entry = &sessionWorkDirLock{}
		a.sessionWorkDirLocks[key] = entry
	}
	entry.refCount++
	a.sessionWorkDirMu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		a.sessionWorkDirMu.Lock()
		entry.refCount--
		if entry.refCount == 0 {
			delete(a.sessionWorkDirLocks, key)
		}
		a.sessionWorkDirMu.Unlock()
	}
}

// getOrCreateSessionTempDir returns (and caches) the work directory for the
// given session tag. Standalone chats normally use a stable directory from
// chatSessionWorkDir so deliverables remain openable across app launches. If
// that directory cannot be resolved or created, fall back to an isolated
// system temp directory rather than letting the agent run with an empty
// WorkDir and no write/edit confinement.
func (a *App) getOrCreateSessionTempDir(tag string) string {
	unlock := a.lockSessionWorkDir(tag)
	defer unlock()

	home, homeErr := os.UserHomeDir()
	homeKnown := homeErr == nil && filepath.IsAbs(home)
	a.mu.Lock()
	dir, cached := a.sessionTempDirs[tag]
	_, fallback := a.sessionFallbackDirs[tag]
	a.mu.Unlock()
	if cached {
		// Ensure the directory still exists (may have been cleaned up externally).
		if info, err := os.Lstat(dir); err == nil && info.IsDir() && ((!homeKnown && fallback) || validateCronProjectDir(dir) == nil) {
			return dir
		}
		a.mu.Lock()
		delete(a.sessionTempDirs, tag)
		delete(a.sessionFallbackDirs, tag)
		a.mu.Unlock()
	}

	safe := sanitizeSessionTag(tag)
	dir, err := prepareAgentWorkspace("chats", safe)
	if err != nil {
		log.Printf("[session-workdir] failed to resolve dir for tag=%s: %v", tag, err)
	}
	if err != nil {
		dir, err = os.MkdirTemp("", "yansu-generate-*")
		if err != nil {
			log.Printf("[session-workdir] failed to create fallback temp dir for tag=%s: %v", tag, err)
			return ""
		}
		log.Printf("[session-workdir] using fallback temp dir for tag=%s: %s", tag, dir)
		fallback = true
	} else {
		log.Printf("[session-workdir] using stable dir for tag=%s: %s", tag, dir)
		fallback = false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if fallback {
		if a.sessionFallbackDirs == nil {
			a.sessionFallbackDirs = make(map[string]struct{})
		}
		a.sessionFallbackDirs[tag] = struct{}{}
	} else {
		delete(a.sessionFallbackDirs, tag)
	}
	if a.sessionTempDirs == nil {
		a.sessionTempDirs = make(map[string]string)
	}
	a.sessionTempDirs[tag] = dir
	return dir
}

// cleanupFallbackSessionDirsOnShutdown removes only temporary fallback
// directories. Stable chat directories remain on disk so their deliverables
// stay openable after an application restart.
func (a *App) cleanupFallbackSessionDirsOnShutdown() {
	// Include creations already in flight. New tags arriving after this
	// snapshot are independent; cleanup must not erase their cache entries.
	a.sessionWorkDirMu.Lock()
	keys := make(map[string]struct{}, len(a.sessionWorkDirLocks))
	for key := range a.sessionWorkDirLocks {
		keys[key] = struct{}{}
	}
	a.mu.Lock()
	for tag := range a.sessionTempDirs {
		keys[sanitizeSessionTag(tag)] = struct{}{}
	}
	for tag := range a.sessionFallbackDirs {
		keys[sanitizeSessionTag(tag)] = struct{}{}
	}
	if len(a.sessionTempDirs) == 0 {
		a.sessionTempDirs = nil
	}
	if len(a.sessionFallbackDirs) == 0 {
		a.sessionFallbackDirs = nil
	}
	a.mu.Unlock()
	a.sessionWorkDirMu.Unlock()

	for key := range keys {
		unlock := a.lockSessionWorkDir(key)
		a.mu.Lock()
		dirs := make(map[string]string)
		for tag, dir := range a.sessionTempDirs {
			if sanitizeSessionTag(tag) != key {
				continue
			}
			if _, fallback := a.sessionFallbackDirs[tag]; fallback && dir != "" {
				dirs[tag] = dir
			}
			delete(a.sessionTempDirs, tag)
		}
		for tag := range a.sessionFallbackDirs {
			if sanitizeSessionTag(tag) == key {
				delete(a.sessionFallbackDirs, tag)
			}
		}
		if len(a.sessionTempDirs) == 0 {
			a.sessionTempDirs = nil
		}
		if len(a.sessionFallbackDirs) == 0 {
			a.sessionFallbackDirs = nil
		}
		a.mu.Unlock()

		for tag, dir := range dirs {
			if err := a.removeSessionWorkDir(dir); err != nil {
				log.Printf("[session-workdir] shutdown cleanup failed for fallback tag=%s: %v", tag, err)
			}
		}
		unlock()
	}
}

// CleanupSessionTempDir removes all work directories associated with a session.
// Unlike application shutdown, explicit session deletion also removes the
// stable directory so user-deleted chats do not leave deliverables behind.
func (a *App) CleanupSessionTempDir(tag string) {
	unlock := a.lockSessionWorkDir(tag)
	defer unlock()

	a.mu.Lock()
	cachedDir := a.sessionTempDirs[tag]
	delete(a.sessionTempDirs, tag)
	delete(a.sessionFallbackDirs, tag)
	a.mu.Unlock()

	dirs := make(map[string]struct{}, 2)
	if cachedDir != "" {
		dirs[cachedDir] = struct{}{}
	}
	if stableDir, err := a.chatSessionWorkDir(tag); err == nil {
		dirs[stableDir] = struct{}{}
	}

	for dir := range dirs {
		if err := a.removeSessionWorkDir(dir); err != nil {
			log.Printf("[session-workdir] cleanup failed for tag=%s dir=%s: %v", tag, dir, err)
		} else {
			log.Printf("[session-workdir] cleaned up dir for tag=%s: %s", tag, dir)
		}
	}
	if err := removeLegacySessionWorkDir(tag); err != nil {
		log.Printf("[session-workdir] legacy cleanup failed for tag=%s: %v", tag, err)
	}
}

// removeLegacySessionWorkDir deletes an unmigrated chat without first creating
// its replacement workspace. Keep deletion rooted in the configured data
// folder, and refuse an aliased chats parent so other workspace types survive.
func removeLegacySessionWorkDir(tag string) error {
	legacy, err := legacyAgentWorkspacePath("chats", sanitizeSessionTag(tag))
	if err != nil {
		return err
	}
	data, err := os.OpenRoot(filepath.Dir(filepath.Dir(legacy)))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer data.Close()
	before, err := data.Lstat("chats")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !before.IsDir() {
		return fmt.Errorf("legacy chats parent is not a directory")
	}
	chats, err := data.OpenRoot("chats")
	if err != nil {
		return err
	}
	defer chats.Close()
	opened, err := chats.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(before, opened) {
		return fmt.Errorf("legacy chats parent changed during cleanup")
	}
	return chats.RemoveAll(filepath.Base(legacy))
}

func (a *App) removeSessionWorkDir(dir string) error {
	if a.sessionWorkDirRemoveAll != nil {
		return a.sessionWorkDirRemoveAll(dir)
	}
	return os.RemoveAll(dir)
}

// composePrompt is the single entry point for building the full prompt.
// All chat paths (executePrompt and chat connectors) should
// use this to ensure consistent context injection.
// opts: optional [sessionTag, sourcePlatform, skillDomain, uiLanguage,
// promptOrigin].
// composePrompt builds the full user-message text by concatenating
// independently-computed context blocks ahead of the raw user
// query. Historically these inject functions were called in a nested
// chain, which meant the slowest one (usually memory, with its ONNX
// embedding) blocked all the others serially. Each inject actually
// just builds a context string to PREPEND — they don't depend on each
// other's output — so we can fetch all seven concurrently and get a
// total latency of max() instead of sum().
//
// Each inject is called with an empty prompt so it returns only its
// context chunk (or "" if it has nothing to contribute). We then
// concatenate them in the same order the nested version produced, so
// the model-visible prompt is byte-identical to the old serial path.
func (a *App) composePrompt(prompt string, opts ...string) string {
	return a.composePromptFor(prompt, true, opts...)
}

// personalContextProducers are the prompt blocks that carry the user's own
// material: memory, initiatives, people, activity and other chats. A run that
// is not personal (an unpaired connector or a shared-channel reply) gets none
// of them. Tools, skills and the app's own instructions still apply.
var personalContextProducers = map[string]bool{
	"memory":            true,
	"initiatives":       true,
	"memory-cli":        true,
	"people":            true,
	"activity-cli":      true,
	"recent-activity":   true,
	"focused-app":       true,
	"side-chat":         true,
	"main-chat-reports": true,
	"main-chat-texts":   true,
	"soul":              true,
	"upcoming-tools":    true,
	"goal-tools":        true,
}

// composePromptFor is composePrompt with the personal-context switch. The
// local connectors enable it only for a paired owner's private conversation.
func (a *App) composePromptFor(prompt string, personal bool, opts ...string) string {
	tag := ""
	platform := ""
	if len(opts) > 0 {
		tag = opts[0]
	}
	if len(opts) > 1 {
		platform = opts[1]
	}
	skillDomain := skillHubDomainChat
	if platform != "" {
		skillDomain = skillHubDomainAutomation
	}
	if len(opts) > 2 && opts[2] != "" {
		skillDomain = normalizeSkillHubDomain(opts[2])
	}
	languageName := a.currentUILanguageName()
	if len(opts) > 3 {
		languageName = a.responseLanguageNameFromInput(opts[3])
	}
	origin := promptOriginMac
	if len(opts) > 4 {
		origin = promptOrigin(opts[4])
	}
	rawQuery := prompt
	composeStart := time.Now()

	type ctxChunk struct {
		name    string
		content string
		dur     time.Duration
	}
	// Order keeps broad user memory before narrower app/runtime context.
	producers := []struct {
		name string
		fn   func() string
	}{
		{"memory", func() string {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return a.injectMemoryContext(context.WithValue(ctx, contextUseRefKey{}, tag), "", rawQuery)
		}},
		{"initiatives", func() string { return a.injectInitiativeContext("") }},
		{"memory-cli", func() string { return a.injectMemoryCLI("") }},
		{"people", func() string { return a.injectPeopleContext(rawQuery) }},
		{"activity-cli", func() string { return a.injectActivityCLI("") }},
		{"cua-tool", func() string { return a.injectCuaDriverCLI("") }},
		{"soul", func() string { return a.injectSoulContext("") }},
		{"skills", func() string { return a.injectSkillContext("", skillDomain) }},
		{"mcp", func() string { return a.injectMcpContext("") }},
		{"recent-activity", func() string { return a.injectRecentActivityContext("", tag) }},
		{"focused-app", func() string { return a.injectFocusedAppContext(tag) }},
		{"side-chat", func() string { return a.injectSideChatContext(tag) }},
		{"main-chat-reports", func() string { return a.injectMainChatReports(tag) }},
		{"main-chat-texts", func() string { return a.injectMainChatTextReplies(tag) }},
		{"yansu", func() string { return a.injectYansuContext("", tag, platform, languageName) }},
		// After the yansu context, whose cron instructions it narrows for
		// the main chat.
		{"draft-tool", func() string { return a.injectChatDraftToolContext(tag, origin) }},
		{"upcoming-tools", func() string { return a.injectUpcomingToolsContext(tag, origin) }},
		{"goal-tools", func() string { return a.injectGoalToolsContext(tag) }},
		{"web-search-tool", func() string { return a.injectWebSearchToolContext(tag, origin) }},
	}

	if !personal {
		for i := range producers {
			if personalContextProducers[producers[i].name] {
				producers[i].fn = func() string { return "" }
			}
		}
	}
	// A Today turn supplies these sources through its own bounded, item-specific
	// read-time privacy checks. Do not append a second, unrelated selection.
	if len(opts) > 5 && opts[5] == "today" {
		for i := range producers {
			switch producers[i].name {
			case "memory", "people", "initiatives", "recent-activity", "focused-app":
				producers[i].fn = func() string { return "" }
			}
		}
	}

	externalChat := len(opts) > 5 && opts[5] == "external-chat"
	if externalChat {
		for i := range producers {
			switch producers[i].name {
			case "memory", "people", "recent-activity", "memory-cli", "activity-cli", "cua-tool", "yansu":
				producers[i].fn = func() string { return "" }
			}
		}
	}

	chunks := make([]ctxChunk, len(producers))
	var wg sync.WaitGroup
	for i, p := range producers {
		wg.Add(1)
		go func(i int, name string, fn func() string) {
			defer wg.Done()
			t0 := time.Now()
			c := fn()
			d := time.Since(t0)
			chunks[i] = ctxChunk{name: name, content: c, dur: d}
			if d > 50*time.Millisecond {
				log.Printf("[compose-prompt][SLOW] %s took %v", name, d)
			}
		}(i, p.name, p.fn)
	}
	wg.Wait()

	var b strings.Builder
	b.Grow(len(prompt) + 1024)
	if externalChat {
		b.WriteString(a.externalChatStandingRules(languageName))
		b.WriteString(chatContextToolHint)
		b.WriteString(responseLanguageInstruction(languageName) + "\n\n")
	}
	for _, c := range chunks {
		b.WriteString(c.content)
	}
	// Interactive UI chat (empty platform) gets the deliverables protocol so
	// any files the agent produces surface as openable chips below the reply.
	// Automation / IM daemon paths (platform != "") rely on composeCronPrompt's
	// own delivery contract instead and must not be double-injected here.
	if platform == "" {
		b.WriteString(chatDeliverablesDirective)
	}
	b.WriteString(prompt)
	out := b.String()
	log.Printf("[compose-prompt] total=%v final-len=%d (parallel, %d chunks, skill-domain=%s)", time.Since(composeStart), len(out), len(producers), skillDomain)
	return out
}

// cronDeliveryDirective returns a high-visibility preface that pins the
// delivery contract to the configured notify platform. It is prepended to the
// cron execution context so the model sees it before the long rules block.
//
// Caller is responsible for passing "" when the user has disabled DM
// notification (NotifyDM=false) or set notifyPlatform="none". Without that
// gating, this directive would force the model to emit <delivery_target>
// which overrides the disabled-DM short-circuit in app.go.
func cronDeliveryDirective(notifyPlatform string) string {
	switch notifyPlatform {
	case "slack", "discord", "telegram", "feishu", "wechat":
	default:
		return ""
	}
	channelHint := "If the task description names a specific channel/group, follow the <delivery_target> rules below to add a channel field."
	if notifyPlatform == "wechat" {
		channelHint = "WeChat is DM-only in this app; do NOT add a channel field. Even if the task description names a WeChat group, the message will be sent to the connected WeChat user as a DM."
	}
	return fmt.Sprintf(`<delivery-context>
This automation is wired to deliver its result via %q. The framework — not you — performs the delivery. Your only job around delivery is:
  1. Write the full message body the user should receive as your final assistant reply.
  2. End that final reply with:
     <delivery_target>
     platform: %s
     </delivery_target>
Do NOT attempt to call %s APIs, do NOT curl any local or remote endpoint to send the message, and do NOT invoke any CLI to push it. Those paths will fail (no such tool, no such endpoint) and will burn your turn budget. %s
</delivery-context>

`, notifyPlatform, notifyPlatform, notifyPlatform, channelHint)
}

// composeCronPrompt builds the prompt for scheduled automation execution.
// Unlike general chat, this explicitly frames the agent as an executor and
// prevents it from asking clarifying questions or discussing the task.
//
// enforcedWorkDir is non-empty only when the job has no user-pinned
// projectDir and the runtime has fallen back to a managed directory under
// ~/.isoform-workspaces/<flavor>/automations/. In that case we tell the agent to write
// deliverables under this directory — otherwise agents tend to default to
// /tmp/ or $TMPDIR which are unreadable from the UI after the run and may
// be swept by macOS. When the user has pinned a projectDir we deliberately
// stay silent so user-authored prompts (e.g. "save the report to
// ~/Documents/reports/") are not overridden.
func (a *App) composeCronPrompt(prompt, enforcedWorkDir, notifyPlatform string, uiLanguage ...string) string {
	const cronExecutionContext = `<automated-task-execution>
You are executing an automated scheduled task. The task description below defines exactly what to do.

RULES:
- Execute the task immediately and directly. Do NOT ask clarifying questions.
- Do NOT discuss the task, explain what you are about to do, or ask for confirmation.
- Use your best judgment for any ambiguous details and proceed.
- If a step fails, try a reasonable alternative and continue.
- TOOL CALL LOOPS — STOP RETRYING FAST. If any single tool call (Bash command, opencli adapter invocation, WebFetch, etc.) returns the same failure or the same clearly-wrong output twice with identical arguments, ABANDON that specific lookup and continue with what you already have. Do NOT issue the same tool call a third time. This applies to parallel dispatch too: if you fanned out N calls and a SUBSET of them failed identically twice, do not re-dispatch that subset — drop those items and proceed with the ones that succeeded. When a tool_result starts with the prefix [loop-guard], treat it as a hard ban on those exact arguments — switch tactics or write your final answer immediately, never re-issue the same call hoping for a different outcome. Repeated failures from the same arguments are evidence the resource is dead, not evidence to try again.
- NUMERIC PARAMETER SWEEPS ARE NOT PROGRESS. Do NOT enumerate pages, ids, offsets, periods, months, or date windows unless the user explicitly supplied that range or official documentation proves it is required. If one minimal API request succeeds, use it. If an optional numeric parameter is unclear, omit it instead of guessing nearby numbers. If multiple related API calls are genuinely required, use code execution/programmatic tool calling with a bounded loop and an explicit break condition instead of issuing one new assistant-turn Bash call per guessed value.
- FINALIZE FROM PARTIAL DATA. The goal is to ship a useful result, not to hit a target volume. As soon as you have enough information to write a substantive final message, stop collecting and write it. Six well-sourced items is far better than chasing twelve and shipping zero because you got stuck on item eleven. If a numeric target in the task description (e.g. "8~12 items") cannot be fully reached, ship what you have and briefly note in the final message what was skipped and why ("skipped 2 sources whose detail pages returned EMPTY_RESULT").
- You MUST end your run with a substantive final message (at least 2-3 sentences). Tool calls alone are NEVER sufficient — if you only run tools without a final message, the user will see "nothing happened" and the run will be recorded as a failure.
- Your final message MUST do ONE of:
  (a) State the deliverable you produced, in natural language, AND declare it via the <deliverables> or <delivery_target> block below if applicable. If the deliverable is a drafted document or email, include the full text in the final message itself so the user can read, copy, or use it directly.
  (b) If you could not produce the expected deliverable, explicitly explain: what you tried, what blocked you, and what the user needs to change for the task to succeed. Do NOT silently finish.
- If the task description references a specific entity that is missing or unresolvable (e.g. a company name, person, file path, URL that is not provided and cannot be inferred from context), do NOT fabricate a plausible-looking value. Stop, follow rule (b), and report which entity is unresolved so the user can fix the automation.
- RESEARCH TASKS — USE opencli, NOT WebSearch, AND DO NOT FABRICATE URLs. WebSearch is DISABLED in this build (no hosted search API key) and will always return an is_error result; do not call it. Use opencli via Bash for all search/look-up/research work. opencli is pre-installed and on PATH. Required workflow on the first opencli call: run "opencli list -f yaml" to see the live registry, then "opencli <site> -h" for the chosen adapter, then run the actual search command. Do NOT memorize subcommand signatures from training data; re-read help output each time. WebFetch remains allowed, but ONLY for URLs that actually appeared in earlier tool output (an opencli result, a page you already fetched, a sitemap, a URL inside a fetched article, a user-provided URL). Do NOT construct URLs from training-data knowledge — site-specific slugs (e.g. espn.com/soccer/story/_/id/NNNN, nytimes.com paths, news article IDs) are unreliable from memory and many sites route unknown IDs to a 200 OK landing page rather than a 404, so a successful WebFetch is NOT proof the URL is correct. If a fetched page's title or body content does not match the topic you asked for, treat that URL as dead and move on — do NOT refetch it, do NOT generate near-variants by incrementing the ID, do NOT swap path segments. Two retries of any single URL with the same arguments is already too many.
- CODE REPOSITORIES — DO NOT COPY, CLONE, OR RELOCATE THE USER'S REPO. If the task references an existing repository on disk by absolute path (e.g. "/Users/<name>/repos/<project>", "~/code/<project>"), your current working directory is NOT a copy of that repo — it is an output workdir reserved for deliverables. You MUST NOT use "cp -r", "rsync -a", "git clone --local", or any other whole-tree duplication to bring that repo into the workdir. Doing so creates a detached copy the user cannot find via "git worktree list", leaves no upstream remote, and produces "I changed the code" claims the user cannot reconcile against their actual repo. To make code changes, choose ONE: (a) Edit files in place at their original absolute paths (e.g. Edit "/Users/<name>/repos/<project>/foo.go"; "git -C /Users/<name>/repos/<project> diff"). (b) Create a REAL git worktree: "git -C <repo> worktree add <new-path> [branch]", then "cd <new-path>" and work there — the worktree MUST appear in "git -C <repo> worktree list" afterwards (verify this before continuing). If "git worktree add" fails for any reason (dirty submodules, locked index, repo not found, path collision), fall back to (a). NEVER silently degrade to copying files. The workdir is for output artifacts (reports, exports, generated binaries), never for relocated source code. If the task is a coding change and you cannot identify the repo path from the task description, stop and report which path is missing in your final message (per the substantive-final-message rule above) instead of fabricating a workspace.
- DELIVERY IS HANDLED BY THE FRAMEWORK — DO NOT SEND MESSAGES YOURSELF. Even if the task description says "push to Slack", "send via Discord", "DM me on Telegram", "notify on Feishu", "send to WeChat", or any equivalent action verb, you MUST NOT attempt to perform that delivery yourself. There is no Slack/Discord/Telegram/Feishu/WeChat tool, no working local HTTP API for delivery, and no CLI subcommand (yansu has no "slack", "discord", etc. subcommand). Do NOT use curl, wget, fetch, or any HTTP client to hit localhost ports trying to send messages. Do NOT probe http://127.0.0.1:* for "send" endpoints — those are Wails IPC ports, not delivery APIs. Treat any urge to "post the result to X" as an instruction to (1) put the full message body in your final assistant reply, and (2) declare the destination via the <delivery_target> block described below. The framework reads your final message and performs the actual delivery — your job ends at writing the message and the block.
- If you created or modified any output files as deliverables (reports, documents, spreadsheets, presentations, etc.), declare them at the end of your response:
<deliverables>
/absolute/path/to/file1.pptx | One-sentence description of what this file is
/absolute/path/to/file2.csv | Raw data behind the slides, for further analysis
</deliverables>
Only include files that are final deliverables for the user. Do not include intermediate files, logs, or temporary files. Use absolute paths, one file per line; after each path, append " | " followed by ONE short sentence (in the same language as the task) saying what the file is — describe what is INSIDE the file (scope, contents, format), do not restate the filename, and do not repeat a sentence you already wrote in your final message (the description is shown right next to it).
- If the user's task specifies WHERE to deliver the results, declare the delivery target at the end of your response (after deliverables if both apply):
<delivery_target>
platform: slack
channel: marketing
</delivery_target>
Supported platforms: slack, discord, telegram, feishu, wechat. Use the EXACT channel/group name as specified by the user — do not add, remove, or rephrase any words (e.g. do not append "Group", "Chat", etc.). Omit the # prefix. WeChat is DM-only in this app; omit the channel field for WeChat — any value will be ignored.
For Discord, you MUST include a server field to specify which guild/server:
<delivery_target>
platform: discord
server: MyServer
channel: general
</delivery_target>
Rules:
  - If the user specifies both a platform AND a channel (e.g. "send to Slack #marketing"): include both fields. The system will post to that channel/group and skip the DM. For Telegram, the channel value can be a public channel username (e.g. "@mychannel") or a numeric chat ID for private groups. For Feishu, use the group chat name.
  - If the user specifies only a platform (e.g. "notify me on Slack", "send to Discord"): include only the platform field, omit the channel field. The system will send a DM on that platform, overriding the default notification setting.
  - If the user does NOT specify a delivery destination: do NOT include this block. The system will send a DM notification using the default platform.
</automated-task-execution>

`
	contextBlock := cronExecutionContext
	if enforcedWorkDir != "" {
		// Default output location, but EXPLICITLY yield to any path the user
		// specified in the task itself. Agents otherwise tend to default to
		// /tmp/ or $TMPDIR, which are not surfaced in the UI and may be
		// swept by the OS.
		workDirRule := fmt.Sprintf(
			"- Default deliverable location: if the task description does NOT specify an explicit output path or directory, save all deliverable files (documents, spreadsheets, presentations, images, etc.) under %q. Do NOT fall back to /tmp/, $TMPDIR, or any other system temp location. If the task DOES specify a path (e.g. \"save to ~/Desktop/out.pptx\" or \"write to ~/Documents/reports/\"), the task-specified path takes precedence — honor it exactly.\n",
			enforcedWorkDir,
		)
		contextBlock = strings.Replace(contextBlock, "RULES:\n", "RULES:\n"+workDirRule, 1)
	}
	if directive := cronDeliveryDirective(notifyPlatform); directive != "" {
		contextBlock = directive + contextBlock
	}
	languageName := a.currentUILanguageName()
	if len(uiLanguage) > 0 {
		languageName = a.responseLanguageNameFromInput(uiLanguage[0])
	}
	// Scheduled work carries its task and execution/delivery contract only.
	// Sources, files and instructions explicitly named in the task remain in
	// prompt. Only named enabled skill metadata is added; there is no personal
	// context lookup or unrelated standing catalog.
	return a.namedAutomationSkillContext(prompt) + contextBlock + responseLanguageInstruction(languageName) + "\n\n" + prompt
}

// composeHandoffPrompt builds the prompt for a one-time work handoff session.
// A handoff run gets only what the task needs — the brief, the key files the
// brief names, the working directory and the result contract — and none of
// the main chat's personal context blocks (memory, people, recent activity,
// soul, focused app). If the brief needs a fact from memory, it was written
// into the brief when the handoff was created, so no lookup happens here.
// Like the cron path, the result contract is appended by the composer, so the
// brief travels with it on every turn of the run, including follow-ups.
func composeHandoffPrompt(prompt string, entry handoff.Entry, workDir string) string {
	var b strings.Builder
	b.Grow(len(prompt) + 512)
	b.WriteString(prompt)
	if len(entry.KeyFiles) > 0 {
		b.WriteString("\n\n<handoff-key-files>\nKey files named by the brief:\n")
		for _, file := range entry.KeyFiles {
			b.WriteString("- " + file + "\n")
		}
		b.WriteString("</handoff-key-files>")
	}
	if dir := strings.TrimSpace(workDir); dir != "" {
		b.WriteString("\n\n<handoff-workdir>\nWorking directory for this run: " + dir + "\n</handoff-workdir>")
	}
	b.WriteString("\n\n" + buildHandoffResultInstructions(entry))
	return b.String()
}

// connectedIntegrationsContext returns a brief summary of connected messaging
// platforms for injection into the AI system prompt. Uses in-memory daemon
// state (no API calls) so it is safe to call on every prompt.
func (a *App) connectedIntegrationsContext() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	var connected []string
	if a.slackDaemon != nil && a.slackDaemon.isConnected() {
		connected = append(connected, "Slack")
	}
	if a.discordDaemon != nil && a.discordDaemon.isConnected() {
		connected = append(connected, "Discord")
	}
	if a.telegramDaemon != nil && a.telegramDaemon.isConnected() {
		connected = append(connected, "Telegram")
	}
	if a.feishuDaemon != nil && a.feishuDaemon.isConnected() {
		connected = append(connected, "Feishu")
	}

	if len(connected) == 0 {
		return ""
	}
	return "\nCONNECTED INTEGRATIONS (the user already has these platforms linked — use them directly, do NOT ask for webhook URLs or manual setup):\n- " + strings.Join(connected, "\n- ") + "\n"
}

// injectYansuContext prepends product-level context to every user prompt.
// sessionTag identifies the originating chat session (used for automation linkage).
// sourcePlatform identifies the integration platform (slack, feishu, discord, telegram)
// that originated the conversation — passed explicitly to avoid race conditions.
// bashEnvShellHint returns a one-liner describing the Bash-tool runtime shell
// quirks for the current OS. It is appended to the OS line in the prompt's
// RUNTIME ENVIRONMENT section so the model knows, before composing a shell
// command, which syntax actually works.
//
// On Windows in particular, the agent's Bash tool often falls back to cmd.exe
// or PowerShell when no POSIX bash is on PATH. POSIX quoting (single quotes,
// $VAR, 2>/dev/null, etc.) silently breaks there, and `curl` in PowerShell is
// an alias for Invoke-WebRequest with totally different flags. This hint pushes
// the model toward WebFetch and, when Bash IS required, toward the right
// syntax for the host shell.
func bashEnvShellHint() string {
	switch runtime.GOOS {
	case "windows":
		return " Bash on Windows commonly resolves to PowerShell or cmd.exe (not POSIX bash): single-quoted strings, `$VAR`, `2>/dev/null`, and POSIX-style `curl -H 'X: Y'` flags often fail. If you MUST use Bash for HTTP, call `curl.exe` explicitly (PowerShell's `curl` is an alias for Invoke-WebRequest), use double quotes around URLs with `&` query strings, and use `2>$null` instead of `2>/dev/null`."
	case "darwin":
		return " Standard POSIX shell (/bin/bash or /bin/zsh). BSD coreutils — some GNU-only flags (e.g. `sed -i ''` needs the empty string arg, `date -d` is not available) differ from Linux."
	default:
		return " POSIX shell with GNU coreutils."
	}
}

func (a *App) injectYansuContext(prompt string, sessionTag string, sourcePlatform string, uiLanguage ...string) string {
	integrations := a.connectedIntegrationsContext()

	var extraFlags string
	if sessionTag != "" {
		extraFlags += ` --source-session "` + sessionTag + `"`
	}
	platform := sourcePlatform
	if platform == "" {
		platform = "chat"
	}
	extraFlags += ` --source-platform "` + platform + `"`

	languageName := a.currentUILanguageName()
	if len(uiLanguage) > 0 {
		languageName = a.responseLanguageNameFromInput(uiLanguage[0])
	}
	languageInstruction := responseLanguageInstruction(languageName)

	// Resolve the installed yansu CLI binary path so the prompt uses the
	// absolute path (shell-quoted) instead of a bare "yansu" command.
	// The agent's bash subprocess sources shell profile files which reset
	// PATH, so ~/.yansu-agent/bin is never in scope. Without the absolute
	// path the agent must self-discover the binary (works in General Chat
	// but fails in integration daemon contexts where it falls back to the
	// session-scoped CronCreate tool instead).
	yansuCmd := "yansu"
	if p := installedYansuCLIPath(); p != "" {
		yansuCmd = shellenv.ShellQuote(p)
	}

	// The main chat changes, runs and removes existing schedules only with
	// its Upcoming tools, which ask the user where needed; the CLI would go
	// around them, so it is not offered there (see <upcoming-tools>).
	cronEditLines := `- To update an existing automation, use: ` + "`" + yansuCmd + ` cron update <id> [--name \"...\"] [--schedule \"...\"] [--prompt \"...\"] [--enabled true/false] [--notify-platform \"...\"]` + "`" + `. Only include the flags you want to change.
- To delete an automation: ` + "`" + yansuCmd + " cron delete <id>`" + `.
`
	if a.isMainChatSession(context.Background(), sessionTag) {
		cronEditLines = "- To change, pause, resume, run or remove an automation that already exists, follow <upcoming-tools> below. Never use the yansu cron CLI (update, delete or run) for it.\n"
	}

	yansuContext := `<yansu-context>
You are Isoform — an AI assistant that understands users' workflows. You have three core capabilities:

1. **Create automations (cron jobs)** — scheduled recurring tasks that run automatically.
2. **Recall the user's context and history** — when ` + "`" + `<activity-cli>` + "`" + ` and ` + "`" + `<memory-cli>` + "`" + ` sections appear below, you have on-demand CLIs for the user's desktop activity + meeting transcripts (speech events with full spoken text) and their AI-captured personal memories (preferences, working habits, how they want you to behave). You also receive automatic ` + "`" + `<memory-context>` + "`" + ` / ` + "`" + `<knowledge-context>` + "`" + ` injection on every turn — use it first, and reach for the CLIs when you need more. Use these PROACTIVELY whenever the user's question could benefit from their past context. Treat activity, transcripts, and memory as first-class data you already have — never apologize for lacking them.
3. **Track initiatives** — proactive hypotheses about outcomes worth moving forward. Items in ` + "`" + `<initiative-context>` + "`" + ` are operational state, not durable memory. A shadow initiative describes what Isoform thinks the user would do; it is never permission to act.

WHEN TO CREATE AN AUTOMATION:
- User mentions a schedule: "every day", "every morning", "weekly", "every hour", "at 9pm", etc.
- User wants something to run automatically without interaction: "send me a summary", "check for updates", "notify me"
- User wants recurring information delivery: "daily digest", "weekly report"
- **BEFORE creating any automation**, run ` + "`" + yansuCmd + " cron list`" + ` to check existing automations. If a similar one already exists (same purpose, overlapping scope), tell the user and ask if they still want to create a new one. Do NOT silently create duplicates.
- To create an automation, use the yansu CLI: run ` + "`" + yansuCmd + ` cron add --name \"...\" --schedule \"...\" --prompt \"...\" --notify-platform \"...\"` + extraFlags + "`" + ` via Bash.
  Examples: --schedule "cron 0 21 * * *" (daily at 9pm), --schedule "every 2h", --schedule "cron 0 9 * * 1-5" (weekdays 9am)
- For API-backed automations, do at most ONE minimal probe only when required to confirm the endpoint/auth works. Once any request succeeds, STOP probing and create the automation. Do NOT enumerate unknown numeric parameters, pages, ids, offsets, periods, or date windows. If official docs are unavailable, omit optional numeric parameters instead of sweeping guesses. If a bounded batch is unavoidable, use code execution/programmatic tool calling and stop inside the loop as soon as the first useful result is found.
- If the yansu cron add command returns a "duplicate automation" error, tell the user a similar automation already exists. Do NOT fall back to other tools (CronCreate, RemoteTrigger) to bypass the duplicate check.
` + cronEditLines + `- DM notification platform preference: use --notify-platform flag to set which platform receives run completion DMs. Values: slack, discord, telegram, feishu, wechat, none (disable DM). If omitted, the system auto-picks.
  - If the user is chatting from an integration platform (Slack, Discord, etc.): default --notify-platform to that platform. Tell the user: "Run notifications will be sent on [platform]. You can change this anytime in the automation settings."
  - If the user is in General Chat: check CONNECTED INTEGRATIONS listed above. If none connected, use --notify-platform "none". If connected, ask the user which platform they prefer for notifications, then pass it via --notify-platform.

WHEN TO MANAGE SKILLS:
- Skills extend what you can do. Users may ask to search, install, enable, disable, configure, or uninstall skills.
- Use the yansu CLI via Bash:
  - ` + "`" + yansuCmd + " skill sources`" + ` to list available sources.
  - ` + "`" + yansuCmd + " skill list`" + ` to list installed skills.
  - ` + "`" + yansuCmd + " skill discover`" + ` to discover candidate skills from other local agent apps. This only lists candidates; it does not install or enable them for Isoform.
  - ` + "`" + yansuCmd + ` skill search --source <source> \"<query>\"` + "`" + ` to search a user-confirmed source.
  - ` + "`" + yansuCmd + ` skill install --source clawhub <slug-or-url>` + "`" + ` to install a moderated ClawHub skill.
  - ` + "`" + yansuCmd + ` skill install --source agent-dirs <discovered-id> --yes` + "`" + ` only after the user confirms installing an unmoderated skill discovered from another local agent app.
  - ` + "`" + yansuCmd + ` skill install --source github <repo-or-url> --yes` + "`" + ` only after the user confirms installing an unmoderated GitHub skill.
  - ` + "`" + yansuCmd + ` skill install --source local <folder> --yes` + "`" + ` only after the user confirms installing an unmoderated local folder skill.
  - ` + "`" + yansuCmd + " skill enable <id>`" + ` / ` + "`" + yansuCmd + " skill disable <id>`" + ` / ` + "`" + yansuCmd + " skill uninstall <id>`" + `.
  - ` + "`" + yansuCmd + ` skill domains <id> --chat true --project true --automation false` + "`" + ` to configure where a skill applies.
- For skill search, NEVER assume the source. If the user did not name a source, ask a concise follow-up before calling the CLI. Available searchable sources include ClawHub, installed skills, and discovered local agent app skills.
- If the user asks to inspect or discover skills already on their computer, use ` + yansuCmd + ` skill discover or ` + yansuCmd + ` skill search --source agent-dirs. Do not install anything until the user explicitly chooses a discovered skill.
- Installing from a ClawHub URL, discovered skill ID, GitHub URL, owner/repo shorthand, or local folder can infer the source from the ref. For agent-dirs/GitHub/local installs, still explain that the source is unmoderated and get user confirmation before using --yes.
- After installing or changing a skill, briefly state what changed and where the skill is enabled.

RUNTIME ENVIRONMENT (the Bash tool runs here):
- OS: ` + runtime.GOOS + ` (` + runtime.GOARCH + `).` + bashEnvShellHint() + `
- For HTTP calls, **prefer the WebFetch tool over ` + "`curl`" + ` in Bash** — WebFetch is shell- and OS-agnostic, bypasses all quoting / encoding / alias issues, and is faster. Reach for Bash ` + "`curl`" + ` only when WebFetch cannot do the job (need to POST a body, send a non-Bearer auth header, follow a specific redirect, etc.).

WHEN THE USER NEEDS WEB SEARCH OR RESEARCH:

There are two distinct activities. Pick the right tool for each — do NOT use opencli as a generic replacement for everything.

(A) **Fetching a KNOWN URL** (you already know the exact endpoint, or the user gave you one):
  → Use **WebFetch directly**. It is enabled and unrestricted in this build. You do NOT need a URL to have appeared in prior tool output — well-known stable public endpoints below are always valid starting points:
    - Weather: ` + "`https://wttr.in/<city>?format=j1`" + ` (JSON) or ` + "`?format=3`" + ` (one-line). URL-encode CJK city names.
    - Time / timezone: ` + "`https://worldtimeapi.org/api/timezone/<Area>/<City>`" + `
    - Exchange rate: ` + "`https://api.exchangerate.host/latest?base=USD&symbols=CNY`" + `
    - IP / geo: ` + "`https://ipapi.co/<ip>/json/`" + `
    - Wikipedia summary: ` + "`https://en.wikipedia.org/api/rest_v1/page/summary/<term>`" + ` (or zh.wikipedia.org)
    - GitHub repo metadata: ` + "`https://api.github.com/repos/<owner>/<repo>`" + `
    - Any URL the user typed, any URL from prior tool output, any documented public REST endpoint you have high confidence about.
  → Do NOT route these through opencli. opencli is for site-search with anti-bot / login, not for hitting a JSON endpoint.
  → SPA FALLBACK: if WebFetch returns a near-empty JS shell (client-rendered SPA — mostly <script> tags, little readable text), the page needs JavaScript rendering. Run ` + "`opencli web read --url <url>`" + ` via Bash (add ` + "`--wait-for \"<css>\" --wait-until networkidle`" + ` for AJAX shells); it drives a real headless render and returns Markdown. This is the ONLY escalation path for JS-rendered pages — do NOT reach for cua_driver or any GUI browser to scrape a URL.

(B) **Searching / discovering content on a vertical site** (you do NOT have the URL — you need to discover threads, posts, listings, or videos on a third-party platform like X/Twitter, 小红书, 微博, 知乎, B 站, Reddit, 淘宝, etc.):
  → The built-in WebSearch tool is DISABLED in this build (no hosted search API key). Use opencli via the Bash tool instead. opencli is pre-installed and on PATH; see the smart-search entry in <user-installed-skills> for source-routing rules.
  → Preflight: ` + "`opencli list -f yaml`" + ` then ` + "`opencli <site> -h`" + `. Do NOT memorize subcommand signatures.
  → AI-SOURCE BUDGET (hard limit): at most ONE ` + "`opencli <ai> ask`" + ` per question (grok / doubao / gemini / kimi / qwen / ...). NEVER fan the same question out to several AI chatbots in sequence — they share training data and add latency, not signal. If the single ask is insufficient, switch to a primary data source (REST endpoint, ` + "`opencli web read`" + `, a vertical-site adapter) instead of asking another chatbot.

URL-FABRICATION RULE (applies to both paths):
- Never fabricate article slugs from training-data knowledge — site-specific paths (mlb.com, espn.com, nytimes.com, news sites' article IDs, etc.) are unpredictable and many sites return 200 OK on unknown IDs, so a successful WebFetch is NOT proof the URL is real.
- Only WebFetch URLs from: the whitelist above, prior tool output, the user, or documented official REST endpoints.

ROUTING SUMMARY:
- "天气 / weather / 汇率 / 时间 / wiki" → WebFetch directly. Never opencli.
- "search X / 微博讨论 / 小红书测评 / 看看 reddit 上" → opencli.
- "open this URL: ..." → WebFetch directly.
- known URL but WebFetch came back as a JS shell → ` + "`opencli web read --url <url>`" + `. Never cua_driver.

RULES:
- ` + languageInstruction + `
- NEVER show users code, config files, or dev setup steps. Users are non-technical.
- NEVER present multiple options or give time estimates. Just describe what you will build/set up.
- For automations: ONLY use the yansu CLI (` + yansuCmd + ` cron add/update/delete) via Bash. NEVER use built-in CronCreate, CronList, RemoteTrigger, or /schedule tools — they are session-scoped and do not persist. The yansu CLI is the ONLY way to create durable automations.
- For automations: do NOT perform open-ended API parameter discovery. Numeric sweeps are not progress; after one successful probe, create the automation with the proven minimal request. If a bounded batch is unavoidable, use code execution/programmatic tool calling with an explicit stop condition instead of assistant-turn-by-assistant-turn probing.
- For skill search: ONLY use ` + yansuCmd + ` skill search after the source is explicit. Do not silently default to ClawHub or any other source.
- Keep responses concise — 2-3 sentences max, then one question.
- Be warm, personable, and conversational. Mirror the user's tone.
` + integrations + `</yansu-context>

`
	return yansuContext + prompt
}

// injectSoulContext always prepends SOUL.md awareness to the user prompt.
// When the file has content, the agent embodies its persona; when empty or
// missing, the agent still knows the file exists and can create/edit it.
func (a *App) injectSoulContext(prompt string) string {
	soulPath, err := soulFilePath()
	if err != nil {
		log.Printf("[soul] resolve path failed: %v", err)
		return prompt
	}

	soulMd, err := a.ReadSoulFile()
	if err != nil {
		log.Printf("[soul] read persona failed: %v", err)
		return prompt
	}
	soulMd = strings.TrimSpace(soulMd)

	var sb strings.Builder
	sb.WriteString("<soul-context>\n")
	if soulMd != "" {
		sb.WriteString("SOUL.md defines who you are — your persona, tone, and behavioral guidelines. Embody its persona and tone. Avoid stiff, generic replies; follow its guidance unless higher-priority instructions override it.\n\n")
		sb.WriteString(soulMd)
		sb.WriteString("\n\n")
		sb.WriteString(fmt.Sprintf("SOUL.md location (for editing only, do NOT re-read — the full content is already above): %s\n", soulPath))
	} else {
		sb.WriteString(fmt.Sprintf("Your SOUL.md at %s is currently empty — you have no persona active yet. The user can ask you to create or edit it.\n", soulPath))
	}
	sb.WriteString("</soul-context>\n\n")
	sb.WriteString(prompt)
	return sb.String()
}

// injectActivityCLI tells the LLM that activity data is available via
// the Yansu CLI, without injecting any raw data into the prompt. The LLM
// should call the CLI on-demand when the user's question requires activity
// context.
func (a *App) injectActivityCLI(prompt string) string {
	if a.activity == nil || a.activity.store == nil {
		return prompt
	}

	// Resolve yansu CLI path.
	yansuCmd := "yansu"
	if p := installedYansuCLIPath(); p != "" {
		yansuCmd = shellenv.ShellQuote(p)
	}

	var sb strings.Builder
	sb.WriteString("<activity-cli>\n")
	sb.WriteString("DATA YOU HAVE — activity-cli is the user's EXPERIENCE ARCHIVE (do NOT claim you lack it):\n")
	sb.WriteString("- A passive recorder of what the user actually saw, did, said, heard, and clicked on this computer — across ALL apps. Its irreplaceable value is data NO SINGLE APP KEEPS for itself: the cross-app daily narrative; mic-routed meeting audio (who said what); OCR'd snapshots of content that has since been deleted/recalled/edited from the source; the user's click and keystroke stream. Searchable by date, keyword, or session id.\n")
	sb.WriteString("- **Activity sessions** — the raw record of each work session: app/window focus, screen OCR text, clicks, keystrokes, messenger threads, captured memories/knowledge.\n")
	sb.WriteString("- **Meeting transcripts** — when a session is a meeting (Zoom, Google Meet, Teams, etc.), the spoken audio has been transcribed and split by source. The transcript is returned as `speech` events inside `activity show <session-id>` — each event has `speechText` and `speechSource` (microphone = user, system_audio = counterparties). This is OFTEN THE ONLY RECORD of who said what — meeting apps usually don't keep transcripts unless the user enabled recording on the app side.\n\n")
	sb.WriteString("COMMANDS (run via Bash):\n")
	sb.WriteString("  " + yansuCmd + " activity summary --date YYYY-MM-DD  # AI-generated narrative journal for a date (START HERE)\n")
	sb.WriteString("  " + yansuCmd + " activity list --date YYYY-MM-DD    # raw session list for a date\n")
	sb.WriteString("  " + yansuCmd + " activity list --limit N             # last N sessions\n")
	sb.WriteString("  " + yansuCmd + " activity search \"<keyword>\"        # semantic search across sessions\n")
	sb.WriteString("  " + yansuCmd + " activity show <session-id>          # full session detail, including meeting transcripts (speech events)\n\n")
	sb.WriteString("WHEN TO USE — activity-cli is the ONLY tool that holds these kinds of data (do NOT wait for the user to remind you):\n")
	sb.WriteString("- (a) Cross-app activity narrative — \"What did I do yesterday/last week/today?\" / \"Summarize my Tuesday\" → START with `activity summary`. The summary is an AI-written narrative across all apps. No single app holds this story; activity-cli is the only path.\n")
	sb.WriteString("- (b) Meeting transcripts with mic/system_audio split — \"What did <person> say in our 2pm call?\" / \"What did I say about Y?\" → use `activity search \"<keyword>\"` to find the session, then `activity show <id>` and scan the `speech` events. Meeting apps typically don't keep transcripts on their own; the mic-routed audio captured here is usually the only record of who said what.\n")
	sb.WriteString("- (c) Content that has been REMOVED FROM ITS SOURCE — a chat message the counterparty recalled, a draft email the user discarded, content scrolled out and re-rendered differently → the app no longer holds this, but activity-cli's OCR snapshot at the moment of capture does. Use `activity search` then `activity show`.\n")
	sb.WriteString("- (d) The user's CLICK AND KEYSTROKE record — \"What did I click before the bug appeared?\" / \"What did I type into that form?\" → activity-cli has the input stream; apps don't retain ephemeral inputs after re-render.\n")
	sb.WriteString("- (e) Drafting follow-ups from actual spoken words — \"Draft a follow-up to the call\" / \"What were the action items?\" → pull the transcript via `activity show` and compose from the literal words spoken (combines (a) and (b)).\n")
	sb.WriteString("- Any question where the user's subjective prior context would sharpen your answer — check before guessing.\n")
	sb.WriteString("\nDO NOT use activity-cli when the answer is data an app itself maintains — use the `cua_driver` tool instead (see <cua-tool> below):\n")
	sb.WriteString("- activity-cli is OCR-based: lossy on rendered text, only covers what was on screen at capture time, doesn't read app DOM directly. cua_driver reads the live AX tree / DOM and can launch the app in the background if it isn't currently running.\n")
	sb.WriteString("- So: if the user is asking about chat history, document content, email content, source code, calendar events, web page content, file listings — anything the app itself maintains — cua_driver is the right tool, period. \"The app isn't open right now\" is NOT a reason to use activity-cli: cua_driver's launch_app starts the app in the background (focus-safe, activates=false) and then reads state.\n")
	sb.WriteString("- This decision does NOT depend on verb tense or temporal deictics. Past-tense phrasing (\"what did I write\", \"who did I message last\") doesn't imply activity-cli — if the answer is data the app keeps, it's a cua_driver question regardless of phrasing.\n")
	sb.WriteString("- activity-cli's PRIMARY domain is what apps DON'T keep: cross-app narrative, mic-routed meeting transcripts, OCR snapshots of removed/recalled content, the user's input stream — see WHEN TO USE above for the irreplaceable categories (a)-(d).\n")
	sb.WriteString("- activity-cli is also a FALLBACK for an app-data question in one narrow case: cua_driver was already attempted AND its response shows ax_status=\"empty_after_wakeup\" with no usable visual_index. cua_driver internally already retries AX wake-up (which is how Electron/CEF apps like Slack, Discord, Notion, Feishu, DingTalk, WeChat desktop populate) and runs Vision OCR as a third layer — only when BOTH the AX tree and OCR pass come back empty does activity-cli become the fallback. Apps that actually hit this: games, video players, DRM windows, canvas/Metal/WebGL custom-renderer GUIs. Do NOT skip cua_driver on a guess; do NOT trigger this fallback on a partially-populated tree if visual_index still carries content.\n\n")
	sb.WriteString("Only fall back to `activity list/show` when the summary is insufficient or you need a specific session.\n\n")
	sb.WriteString("When presenting activity data to the user:\n")
	sb.WriteString("- Stay conversational and warm, like a thoughtful colleague.\n")
	sb.WriteString("- Name specific people, projects, companies, dollar amounts, dates.\n")
	sb.WriteString("- For each item, include its STATE or NEXT STEP when inferable.\n")
	sb.WriteString("</activity-cli>\n\n")
	sb.WriteString(prompt)
	return sb.String()
}

// injectCuaDriverCLI tells the LLM that the native `cua_driver` tool
// reads the live state of any INSTALLED macOS app — not just running
// apps, since launch_app can start a closed app in the background
// (activates=false, focus-safe) before get_window_state reads state.
// Counterbalances injectActivityCLI's eager guidance: the two tools
// cover different KINDS of data, not different time periods.
// cua_driver = any installed app's INTERNAL data (chat history,
// documents, mail, player state, web pages, calendar, files).
// activity-cli = the user's EXPERIENCE archive (cross-app narrative,
// mic-routed meeting transcripts, OCR'd snapshots of content the
// source has since deleted/recalled, the user's click/keystroke
// stream).
//
// The guidance below is principle-based, not example-based: the model
// is taught to decide via "can I open the app and look at this?"
// rather than from a finite app-name list or from phrasing signals
// (tense markers, temporal deictics) which don't track data ownership.
// Otherwise the LLM picks up the wrong heuristic and silently routes
// data-ownership questions through activity-cli.
func (a *App) injectCuaDriverCLI(prompt string) string {
	var sb strings.Builder
	sb.WriteString("<cua-tool>\n")
	sb.WriteString("DATA YOU HAVE — live read of ANY INSTALLED macOS app's internal data, running or not (via the `cua_driver` tool):\n")
	sb.WriteString("- Full AX tree + screenshot of an app, WITHOUT pulling it to the foreground or stealing focus from whatever the user is doing.\n")
	sb.WriteString("- The app does NOT have to be running. cua_driver can launch_app it in the background (activates=false, no focus theft, no Space switch) and then read state. So \"is the app currently open?\" is NOT a precondition.\n")
	sb.WriteString("- Works for any macOS-visible surface: native AppKit apps, Electron / CEF apps, browser tabs, IDEs, terminals, menu-bar apps, anything macOS lists in NSWorkspace.\n")
	sb.WriteString("- Reads on-screen content AND off-screen content still held in the app's DOM / model layer (e.g. messages above the visible viewport in chat apps).\n\n")
	sb.WriteString("TOOL CALLS:\n")
	sb.WriteString("  cua_driver({\"name\":\"list_apps\",\"args\":{}})                          # enumerate running apps + bundle ids\n")
	sb.WriteString("  cua_driver({\"name\":\"get_window_state\",\"args\":{\"bundle\":\"<id>\"}}) # full AX tree dump + screenshot for that app\n")
	sb.WriteString("    NOTE: the args key is \"bundle\" (NOT \"bundle_id\"). Use the \"bundle\" field returned by list_apps. Daemon will reject calls that use bundle_id and report 'No running app matched pid=0'.\n\n")
	sb.WriteString("WHEN TO USE — pick cua_driver whenever the answer is data an app itself maintains:\n")
	sb.WriteString("- cua_driver and activity-cli are NOT substitutes; they cover DIFFERENT KINDS OF DATA, not different time periods.\n")
	sb.WriteString("  - cua_driver scope = any installed app's INTERNAL data. Examples: chat threads in a messenger (Slack/Discord/Feishu/WeChat group history), saved documents or code files in their editor, emails in the user's mailbox, content of browser tabs the user ALREADY has open, player current state, calendar events, files in Finder. The app does NOT have to be currently running — see LAUNCH IF NEEDED below.\n")
	sb.WriteString("  - activity-cli scope = the user's EXPERIENCE archive, things no single app keeps. Examples: cross-app daily/weekly narrative; meeting transcripts with mic/system_audio split (what the USER said vs what counterparties said); OCR'd screen captures including content that has since been deleted/recalled/edited from the source; the user's click/keystroke record.\n")
	sb.WriteString("- Decision rule (the only one that matters): \"Could I get this answer by opening the app and looking?\" If yes → cua_driver. If the answer specifically needs the user's subjective experience, a withdrawn/deleted message, or a story spanning multiple apps → activity-cli.\n")
	sb.WriteString("- NOT FOR WEB SCRAPING: never use cua_driver to launch a browser and navigate it to an external URL just to read that page. Fetching a URL is WebFetch's job (or `opencli web read --url <url>` when the page is a JS-rendered SPA). Driving a GUI browser costs 5-10 tool calls per page load and can hit login popups. cua_driver on a browser is ONLY for reading tabs the user already has open.\n")
	sb.WriteString("- The rule does NOT depend on verb tense or whether the app is running. Even if a messenger is closed, its chat history IS data that messenger maintains — so a question about \"who did I last message in <group>\" is a cua_driver question (launch_app the messenger in the background, then get_window_state). The right discriminator is data ownership, never phrasing.\n")
	sb.WriteString("- Worked examples:\n")
	sb.WriteString("    \"Who did I last chat with in #X on Slack?\"           → Slack maintains chat history → cua_driver (launch_app if not running, then read).\n")
	sb.WriteString("    \"What does foo.go show in my IDE?\"                   → IDE maintains the buffer → cua_driver.\n")
	sb.WriteString("    \"What's currently playing on NetEase Cloud Music?\"   → app maintains player state → cua_driver (L3 OCR will cover it if AX is empty).\n")
	sb.WriteString("    \"What did I do yesterday across all my apps?\"        → cross-app narrative, no single app holds this → activity-cli (`activity summary`).\n")
	sb.WriteString("    \"What did <person> say at the 2pm call?\"             → mic-routed meeting audio, no app keeps this → activity-cli (speech events).\n")
	sb.WriteString("    \"What was the message that just got recalled in the group?\" → already removed from the source → activity-cli (OCR snapshot).\n\n")
	sb.WriteString("LAUNCH IF NEEDED:\n")
	sb.WriteString("- If the app isn't currently running, call cua_driver name=\"launch_app\" args={\"bundle\":\"<id>\"} FIRST. launch_app is focus-safe: activates=false by default; the app starts in the background, does not steal the frontmost window or switch Spaces.\n")
	sb.WriteString("- Immediately after launch_app succeeds, call get_window_state to read state. This is the canonical read-on-demand pattern for an app that isn't open.\n")
	sb.WriteString("- Caveats of launching fresh — these are activity-cli's territory, not cua_driver's:\n")
	sb.WriteString("    * The app will sync on launch (pull new mail, refresh chat lists, load latest page). Content the user saw earlier but has since been deleted/recalled/edited at the source will NOT be in the freshly-launched state — for that, activity-cli's OCR snapshots are the only path.\n")
	sb.WriteString("    * Drafts, clipboard contents, ephemeral notifications, transient overlays, dismissed banners, and prior tab snapshots are also outside what a fresh launch can recover.\n")
	sb.WriteString("    * launch_app raises \"appNotFound\" if the bundle isn't installed — don't loop; either ask the user, or fall back to activity-cli if the question is answerable from past captures.\n\n")
	sb.WriteString("FALLBACK to activity-cli — only when cua_driver's three internal layers all fail:\n")
	sb.WriteString("- cua_driver has three layers, in this order, all inside the daemon (you don't manage them — they just happen on a single get_window_state call):\n")
	sb.WriteString("    L1. Generic AX walker — depth 40, no per-app parsers, works on any bundle.\n")
	sb.WriteString("    L2. AX wake-up retry — 150/250/400 ms back-off if L1 yields only a window shell. Handles Electron/CEF apps whose AX tree streams in lazily after AXEnhancedUserInterface flips. Slack, Discord, VS Code, Notion, Feishu, DingTalk, WeChat desktop all populate via L2. They are NOT activity-cli fallback cases.\n")
	sb.WriteString("    L3. Vision OCR on the screenshot — runs automatically when L1+L2 still yield ≤1 element. Text regions come back in the same response as v* refs under the \"visual_index\" field.\n")
	sb.WriteString("- Activity-cli is the right fallback ONLY when L1+L2+L3 all fail on the same get_window_state response: ax_status is \"empty_after_wakeup\" AND visual_index is empty or missing. Apps that actually hit this: games, video players, DRM-protected windows, and canvas/Metal/WebGL custom-renderer surfaces (e.g. NetEase Cloud Music is called out in the daemon source).\n")
	sb.WriteString("- Order of operations: ALWAYS try cua_driver first. Inspect the response. If ax_status is \"populated\" or \"woke_up\", you have what you need — read from ui_elements/visual_index and answer. If ax_status is \"empty_after_wakeup\" AND visual_index is empty/missing, NOW activity-cli is justified.\n")
	sb.WriteString("- Do NOT preemptively skip cua_driver because you guess the app might be unreadable. Do NOT run your own OCR — L3 already did it. Do NOT switch to activity-cli on a partially-populated AX tree if visual_index still carries content — OCR'd v* refs are the live answer.\n")
	sb.WriteString("- A dial / daemon error is a DIFFERENT case — see the next block.\n\n")
	sb.WriteString("WORKFLOW:\n")
	sb.WriteString("1. If the bundle ID is unknown, call name=\"list_apps\" first to find it.\n")
	sb.WriteString("2. Call name=\"get_window_state\" with that bundle ID.\n")
	sb.WriteString("3. Read the returned AX tree dump — it contains the text, structure, and visible labels the app currently exposes. Answer from that dump directly.\n\n")
	sb.WriteString("WHEN cua_driver RETURNS AN ERROR — do NOT panic-switch to activity-cli:\n")
	sb.WriteString("- A \"daemon not reachable / didn't respond\" error is almost always transient: Isoform spawns the daemon at launch and respawns on crash, so a missed dial is usually a brief warm-up or restart window. Retry the same call once or twice with a short pause before doing anything else.\n")
	sb.WriteString("- If retries still fail, SURFACE the error to the user (\"Isoform's cua-driver daemon isn't responding — please restart Isoform or check Accessibility permissions in System Settings\") rather than silently substituting activity-cli. activity-cli covers the user's experience archive, not the app's internal data, so substituting it for an app-data question returns the wrong kind of data.\n\n")
	sb.WriteString("DO NOT confuse with activity-cli — they cover DIFFERENT KINDS OF DATA, not different time periods:\n")
	sb.WriteString("- cua_driver   = live read of any installed app's INTERNAL data (the app does NOT have to be running — launch_app it in the background first). Source-of-truth for whatever the app itself maintains: chat history, documents, mail, player state, already-open browser tabs, files, calendar.\n")
	sb.WriteString("- activity-cli = the user's EXPERIENCE archive. Things no single app keeps: cross-app activity narrative, meeting transcripts with mic/system_audio split, OCR'd screen captures including content that has been deleted/recalled/edited from the source, the user's click/keystroke record.\n")
	sb.WriteString("- Decision: \"can I open the app and look at this?\" If yes → cua_driver (launch_app if needed). If the answer needs the user's subjective experience, a removed/recalled message, or a multi-app story → activity-cli. The choice is NOT about \"current vs past\" — cua_driver can launch a closed app and read its persisted history.\n")
	sb.WriteString("- One narrow cross-over: cua_driver may legitimately fall back to activity-cli for an app-data question, but ONLY when get_window_state returns ax_status=\"empty_after_wakeup\" with no usable visual_index (see FALLBACK above) — never on the basis of question wording.\n")
	sb.WriteString("</cua-tool>\n\n")
	sb.WriteString(prompt)
	return sb.String()
}

// injectMemoryCLI tells the LLM it has an active `yansu memory` CLI for
// querying the user's AI-captured memories beyond what auto-inject surfaces.
// Complements the passive <memory-context>/<knowledge-context> blocks that
// injectMemoryContext prepends based on per-turn similarity search.
func (a *App) injectMemoryCLI(prompt string) string {
	a.mu.Lock()
	store := a.memoryStore
	a.mu.Unlock()
	if store == nil {
		return prompt
	}

	yansuCmd := "yansu"
	if p := installedYansuCLIPath(); p != "" {
		yansuCmd = shellenv.ShellQuote(p)
	}

	var sb strings.Builder
	sb.WriteString("<memory-cli>\n")
	sb.WriteString("WHAT MEMORY IS:\n")
	sb.WriteString("- Short, durable personal notes about the user — preferences, working habits, current topics of focus, and operational instructions for how you should behave with them.\n")
	sb.WriteString("- Automatically captured from chats, messenger conversations, and desktop activity; also editable by the user directly.\n")
	sb.WriteString("- Every memory is tagged with one dimension:\n")
	sb.WriteString("    user_insight      concrete working habits, tool choices (\"prefers bun over npm\")\n")
	sb.WriteString("    topic_highlight   recent topics of focus, active investigations\n")
	sb.WriteString("    deep_user_model   long-term persona, role, identity\n")
	sb.WriteString("    agentic_memory    how the user wants you to behave (\"always lint before commit\")\n")
	sb.WriteString("    custom            user-authored memories\n\n")
	sb.WriteString("AUTO-INJECT (already happening, no action needed):\n")
	sb.WriteString("- On every turn, the app semantic-searches memory against the user's message and prepends matches as <memory-context> / <knowledge-context> blocks earlier in this message. If you see those blocks, use them directly — they ARE your recall of the user.\n")
	sb.WriteString("- If the blocks are absent, it means nothing scored high enough for THIS message — NOT that memory is empty. Use the CLI below to look directly.\n\n")
	sb.WriteString("COMMANDS (run via Bash when you need more than auto-inject gave you):\n")
	sb.WriteString("  " + yansuCmd + " memory list                            # all memories, newest first (summaries only)\n")
	sb.WriteString("  " + yansuCmd + " memory list --dimension agentic_memory # filter by dimension\n")
	sb.WriteString("  " + yansuCmd + " memory list --limit 20                 # latest N\n")
	sb.WriteString("  " + yansuCmd + " memory search \"<query>\"                # hybrid vector+FTS search, same index auto-inject uses\n")
	sb.WriteString("  " + yansuCmd + " memory search \"<query>\" --dimension user_insight\n")
	sb.WriteString("  " + yansuCmd + " memory show <id>                       # full content of one memory entry\n\n")
	sb.WriteString("WHEN TO USE THE CLI:\n")
	sb.WriteString("- \"What do you remember about me?\" / \"Summarize what you know about me\" → `memory list --limit 20` (auto-inject can't answer meta questions like this).\n")
	sb.WriteString("- \"What are my tool preferences?\" / \"How do I like to work?\" → `memory list --dimension user_insight`.\n")
	sb.WriteString("- \"How should you behave with me?\" → `memory list --dimension agentic_memory`.\n")
	sb.WriteString("- Narrow follow-up on a topic hinted at in <memory-context> — search with a sharper query than the user's raw message.\n")
	sb.WriteString("- Any moment where you want to double-check an assumption about the user before committing to an answer.\n\n")
	sb.WriteString("DO NOT:\n")
	sb.WriteString("- Claim \"I don't have any memory of you\" or \"I don't remember our past\" — you have memory; use it.\n")
	sb.WriteString("- Duplicate what's already in <memory-context> by calling the CLI for the same thing.\n")
	sb.WriteString("- Try to add or edit memories via CLI — this CLI is read-only. Memory capture runs automatically in the background.\n")
	sb.WriteString("</memory-cli>\n\n")
	sb.WriteString(prompt)
	return sb.String()
}

// formatConversationHistory formats a list of stream items into a plain-text
// conversation transcript for context injection after a session restart.
// Only user and assistant turns are included; tool calls and system messages
// are skipped to keep the context concise.
func formatConversationHistory(items []storage.StreamItemRow) string {
	const maxMsgLen = 2000 // truncate individual messages to avoid flooding context
	var sb strings.Builder
	for _, item := range items {
		switch item.Kind {
		case "user":
			if item.Content == "" {
				continue
			}
			sb.WriteString("User: ")
			sb.WriteString(item.Content)
			sb.WriteString("\n\n")
		case "assistant":
			if item.Content == "" {
				continue
			}
			content := item.Content
			if len(content) > maxMsgLen {
				content = content[:maxMsgLen] + "…[truncated]"
			}
			sb.WriteString("Assistant: ")
			sb.WriteString(content)
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}
