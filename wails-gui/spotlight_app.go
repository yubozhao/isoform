package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// spotlightTagState tracks per-session state while a spotlight chat
// is running. The agent:event bridge accumulates streamed deltas into
// `currentAssistant` / `currentThinking` and emits stream_items rows
// for tool_use / tool_result events as they arrive; on the turn-final
// boundary all pending items get persisted in arrival order.
//
// We mirror what useAgentStreaming.ts does on the React side so the
// main UI's chat history shows the same set of rows (assistant text,
// thinking blocks, tool calls, tool results) regardless of whether
// the conversation was driven by the spotlight or the main composer.
type spotlightTagState struct {
	operationMu      sync.Mutex
	streamMu         sync.Mutex
	mu               sync.Mutex
	persistErr       error
	permission       *AgentEvent
	streaming        bool
	runID            string
	agentSessionID   string
	sortOrder        int // monotonic; assigned at persist time
	currentAssistant *spotlightLiveItem
	currentThinking  *spotlightLiveItem
	pending          []StreamItemData
	// main: this state mirrors a Quick chat exchange in the main chat. The
	// turn is a normal main-chat turn whose rows the window or the desktop
	// turn keeper store; the spotlight only forwards its events.
	main bool
	// fromSortOrder is the main chat's sort order of the exchange's first
	// user row: the panel's snapshot starts there, not at the thread's top.
	fromSortOrder int
	// done is closed when the in-flight executePrompt goroutine for
	// this tag has fully returned (which means flushSpotlightItems has
	// already run for the close event, so all pending rows are in the
	// DB). The edit/reset path waits on this so it can safely truncate
	// without racing the in-flight persist. Initialised to a closed
	// channel for tags with no in-flight turn.
	done chan struct{}
}

// spotlightLiveItem buffers content for an assistant or thinking
// block that is still accumulating deltas. Once a non-{assistant,
// thinking} event arrives — or runId changes — the buffered content
// is finalized into a StreamItemData and queued in `pending`.
type spotlightLiveItem struct {
	id        string
	sortOrder int
	runID     string
	sessionID string
	buf       strings.Builder
}

// startSpotlightSubmission is the Go-side entry the host-bridge
// /api/spotlight/submit handler delegates to. It routes the prompt
// through the same `executePrompt` pipeline the main UI uses — same
// model, same composePrompt context injection, same tools, same
// conversation history loading — so the user sees identical reply
// behavior whether they prompted from the spotlight pill or from the
// main chat box.
//
// Stream events flow back via two channels: the standard agent:event
// emit (which the main UI listens to) AND the spotlightStreamHub
// (which the spotlight panel's SSE consumer subscribes to). The
// translation happens in bridgeAgentEventToSpotlight.
func (a *App) startSpotlightSubmission(prompt, bundleID, sessionLocalID, requestID string) (spotlightSubmitResponse, error) {
	if a.ctx == nil {
		return spotlightSubmitResponse{}, fmt.Errorf("app not started")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return spotlightSubmitResponse{}, fmt.Errorf("prompt is empty")
	}
	if a.store == nil {
		return spotlightSubmitResponse{}, fmt.Errorf("chat storage unavailable")
	}
	if a.spotlightStream == nil {
		return spotlightSubmitResponse{}, fmt.Errorf("spotlight stream unavailable")
	}

	tag := strings.TrimSpace(sessionLocalID)
	isFollowup := tag != ""
	// Quick chat answers in the one Chat thread (computer-chat v1.0.0 rules
	// 10 and 11). Without a main chat (setup not done) it keeps its own.
	if main := a.spotlightMainChatTag(tag); main != "" {
		return a.startSpotlightMainTurn(prompt, bundleID, main, requestID, isFollowup)
	}
	if !isFollowup {
		if requestID != "" {
			tag = "spotlight_" + requestID
		} else {
			tag = fmt.Sprintf("spotlight_%d", time.Now().UnixNano())
		}
	}
	a.spotlightTagsMu.Lock()
	if a.spotlightTags == nil {
		a.spotlightTags = make(map[string]*spotlightTagState)
	}
	state, exists := a.spotlightTags[tag]
	if !exists && !isFollowup {
		state = &spotlightTagState{done: closedChan()}
		a.spotlightTags[tag] = state
	}
	a.spotlightTagsMu.Unlock()
	if state == nil {
		return spotlightSubmitResponse{}, fmt.Errorf("unknown spotlight session")
	}
	// A persisted request from a prior host lifetime has an unknown run
	// outcome. Do not execute it again when its in-memory receipt is gone.
	if !isFollowup && !exists && requestID != "" {
		prior, err := a.store.LoadStreamItem(context.Background(), tag+"-u-"+requestID)
		if err != nil || prior != nil {
			a.spotlightTagsMu.Lock()
			if a.spotlightTags[tag] == state {
				delete(a.spotlightTags, tag)
			}
			a.spotlightTagsMu.Unlock()
			if err != nil {
				return spotlightSubmitResponse{}, err
			}
			return spotlightSubmitResponse{}, fmt.Errorf("prior spotlight request outcome is unavailable")
		}
	}
	state.operationMu.Lock()
	defer state.operationMu.Unlock()
	if isFollowup {
		state.mu.Lock()
		done := state.done
		state.mu.Unlock()
		select {
		case <-done:
		default:
			return spotlightSubmitResponse{}, fmt.Errorf("spotlight turn is still running")
		}
	}
	if err := a.ensureSpotlightSession(tag, prompt); err != nil {
		return spotlightSubmitResponse{}, err
	}
	userItemID, order, err := a.persistSpotlightUserMessage(tag, prompt, requestID, state)
	if err != nil {
		return spotlightSubmitResponse{}, err
	}
	// What's on screen goes with the first question only while its chip was
	// there: the pill sends no bundle ID once the person removed it
	// (computer-chat v1.0.0 rule 8). Closed Eyes and sensitive apps are
	// checked again when the prompt reads it.
	if !isFollowup && strings.TrimSpace(bundleID) != "" {
		_ = a.AttachQuickChatContext(tag, bundleID)
	}
	a.publishSpotlightUserPromptEcho(tag, prompt)
	a.notifySpotlightSessionChanged(tag)
	a.runSpotlightTurn(tag, prompt, state, isFollowup, bundleID)
	return spotlightSubmitResponse{SessionLocalID: tag, UserItemID: userItemID, SortOrder: order}, nil
}

// editSpotlightSubmission rewrites a previously-submitted user message
// and re-runs the conversation from that point. Mirrors what main UI's
// handleEditMessage does for chat: update the existing user row,
// truncate everything after it (so DB and the spotlight UI agree on a
// single user bubble for that slot), then start a new agent turn.
//
// Without this, edit/reset in spotlight only updated the in-memory
// reducer; the persisted history kept accumulating one extra user
// row per edit, and the main UI — which loads from the DB — would
// show three bubbles for what the spotlight UI rendered as one.
//
// editFromSortOrder is the sort_order of the user message being edited.
// Items with sort_order > editFromSortOrder (the previous turn's
// assistant/tool/result rows) get deleted; the user row itself stays
// but with content replaced. state.sortOrder is reset so the next
// turn's items pick up immediately after the edited message.
func (a *App) editSpotlightSubmission(prompt, bundleID, sessionLocalID, editItemID string, editFromSortOrder int) error {
	if a.ctx == nil {
		return fmt.Errorf("app not started")
	}
	if a.spotlightIsMainTag(strings.TrimSpace(sessionLocalID)) {
		// The exchange is in the Chat thread: edit it there.
		return errSpotlightMainEdit
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("prompt is empty")
	}
	tag := strings.TrimSpace(sessionLocalID)
	if tag == "" {
		return fmt.Errorf("sessionLocalId is required for edit")
	}
	if strings.TrimSpace(editItemID) == "" {
		return fmt.Errorf("editItemId is required for edit")
	}

	a.spotlightTagsMu.Lock()
	state, ok := a.spotlightTags[tag]
	a.spotlightTagsMu.Unlock()
	if !ok || state == nil {
		return fmt.Errorf("unknown spotlight tag: %s", tag)
	}
	state.operationMu.Lock()
	defer state.operationMu.Unlock()

	// If a turn is in flight, cancel it and wait for its goroutine to
	// fully unwind. The cancel triggers a synthetic close event whose
	// listener (bridgeAgentEventToSpotlight) flushes any pending rows
	// to the DB; only after the goroutine returns is it safe to
	// truncate, otherwise the flush would race with our delete and
	// leave orphan rows.
	state.mu.Lock()
	doneCh := state.done
	state.mu.Unlock()
	a.CancelByTag(tag)
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
			return fmt.Errorf("timed out waiting for prior turn")
		}
	}

	if a.store == nil {
		return fmt.Errorf("chat storage unavailable")
	}
	if err := a.store.ReplaceUserTurn(context.Background(), tag, editItemID, editFromSortOrder, prompt); err != nil {
		return fmt.Errorf("replace user turn: %w", err)
	}

	// Reset per-turn buffers and rewind sortOrder to just after the
	// edited message. The `pending` slice should already be empty (the
	// prior turn's close flushed it), but clear defensively.
	state.mu.Lock()
	state.currentAssistant = nil
	state.currentThinking = nil
	state.pending = nil
	state.sortOrder = editFromSortOrder + 1
	state.mu.Unlock()

	a.publishSpotlightUserPromptEcho(tag, prompt)
	a.notifySpotlightSessionChanged(tag)
	log.Printf("[spotlight] edit: rewrote msg id=%s newSortOrderBase=%d tag=%s",
		editItemID, editFromSortOrder+1, tag)

	a.runSpotlightTurn(tag, prompt, state, true /* isFollowup */, bundleID)
	return nil
}

// publishSpotlightUserPromptEcho fires the user_message + assistant_started
// SSE events so the spotlight UI can render the user bubble immediately,
// before the model produces its first token. Shared by the new-submission
// and edit paths.
func (a *App) publishSpotlightUserPromptEcho(tag, prompt string) {
	if a.spotlightStream == nil {
		return
	}
	a.spotlightStream.Publish(tag, spotlightStreamEvent{
		Kind: "user_message",
		Data: jsonEncodeOrEmpty(map[string]string{"prompt": prompt}),
	})
	a.spotlightStream.Publish(tag, spotlightStreamEvent{
		Kind: "assistant_started",
		Data: "{}",
	})
}

// runSpotlightTurn launches the executePrompt goroutine and tracks its
// completion via state.done so the edit path can wait for the prior
// turn to finish flushing before it truncates. Resets the live-item
// buffers on the way in (sortOrder + pending are intentionally NOT
// reset — they carry across turns).
func (a *App) runSpotlightTurn(tag, prompt string, state *spotlightTagState, isFollowup bool, bundleID string) {
	state.mu.Lock()
	state.currentAssistant = nil
	state.currentThinking = nil
	state.permission = nil
	state.streaming = true
	state.runID = ""
	state.agentSessionID = ""
	state.done = make(chan struct{})
	doneCh := state.done
	state.mu.Unlock()

	go func() {
		defer close(doneCh)
		log.Printf("[spotlight] submission tag=%s prompt_len=%d followup=%v bundleID=%s",
			tag, len(prompt), isFollowup, bundleID)
		// Spotlight is a first-class agentloop entry point (analogous to
		// the chat composer's send-click): mint a trace_id here so every
		// downstream agentloop.* event from this run can be stitched
		// together, and emit a marker event so the PostHog dashboard can
		// tell spotlight-initiated runs apart from chat / cron / connector
		// runs at a glance.
		traceID := uuid.NewString()
		if a.telemetry != nil {
			a.telemetry.Capture("agentloop.spotlight.submitted", map[string]any{
				"trace_id":  traceID,
				"tag":       tag,
				"bundle_id": bundleID,
				"followup":  isFollowup,
			})
		}
		// kickoffEvent="" — spotlight emits its own agentloop.spotlight.submitted
		// above; we don't want a duplicate agentloop.user.send_clicked for the
		// same run.
		if err := a.executePrompt(prompt, "" /* workDir */, a.chatTaskModel(), "" /* sessionID */, tag, nil, false, false, "" /* uiLanguage */, nil, nil, traceID, "", promptOriginMac); err != nil {
			log.Printf("[spotlight] executePrompt failed tag=%s err=%v", tag, err)
			if a.spotlightStream != nil {
				a.bridgeAgentEventToSpotlight(AgentEvent{Type: "error", Tag: tag}, state)
				a.bridgeAgentEventToSpotlight(AgentEvent{Type: "done", Tag: tag}, state)
			}
			return
		}
		// executePrompt starts the runner asynchronously. Keep done open until
		// its cleanup has flushed the turn, so an edit cannot truncate early.
		for {
			a.mu.Lock()
			active := a.activeTags[tag]
			a.mu.Unlock()
			if !active {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		state.streamMu.Lock()
		state.mu.Lock()
		missingTerminal := state.streaming
		state.mu.Unlock()
		state.streamMu.Unlock()
		if missingTerminal && a.spotlightStream != nil {
			a.bridgeAgentEventToSpotlight(AgentEvent{Type: "error", Tag: tag}, state)
		}
	}()
}

// closedChan returns a chan that's already closed, so non-streaming
// tags can be waited on without blocking.
func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// registerSpotlightAgentBridge wires emitAgentEvent → spotlightStreamHub
// for tags that belong to active spotlight sessions. Idempotent; safe
// to call multiple times in startup. Returns an unsubscriber but in
// practice we keep the listener for the app's lifetime.
func (a *App) registerSpotlightAgentBridge() func() {
	return a.onAgentEvent(func(evt AgentEvent) {
		a.spotlightTagsMu.Lock()
		state, ok := a.spotlightTags[evt.Tag]
		a.spotlightTagsMu.Unlock()
		if !ok || state == nil {
			return
		}
		a.bridgeAgentEventToSpotlight(evt, state)
	})
}

// bridgeAgentEventToSpotlight forwards the full AgentEvent over SSE
// so the spotlight panel can run the same event-processing logic as
// the main UI. Side-effect: accumulates per-event StreamItemData rows
// into state.pending so we can persist them on the close boundary.
// The main UI's persistence path runs in React (useAgentStreaming.ts)
// but skips spotlight tags to avoid duplicate writes — that means
// this Go-side path is the *only* persistence sink for spotlight
// chats, so it has to cover every event kind the main UI loader
// expects (assistant, thinking, tool, result), not just assistant.
// Without this, the main UI's processing-block (step buttons,
// thinking, tool input/output) would have no rows to render when
// the user reopens the conversation from the sidebar.
func (a *App) bridgeAgentEventToSpotlight(evt AgentEvent, state *spotlightTagState) {
	hub := a.spotlightStream
	if hub == nil {
		return
	}

	state.streamMu.Lock()
	defer state.streamMu.Unlock()
	state.mu.Lock()
	if evt.RunID != "" {
		state.runID = evt.RunID
	}
	if evt.SessionID != "" {
		state.agentSessionID = evt.SessionID
	}
	switch evt.Type {
	case "run_created":
		state.streaming = true
	case "tool_result":
		if state.permission != nil && evt.ToolUseID != "" && evt.ToolUseID == state.permission.ToolUseID {
			state.permission = nil
		}
	case "permission_request":
		permission := evt
		permission.ToolInput = append(permission.ToolInput[:0:0], permission.ToolInput...)
		permission.PermissionOptions = append(permission.PermissionOptions[:0:0], permission.PermissionOptions...)
		if permission.SessionID == "" {
			permission.SessionID = state.agentSessionID
		}
		state.permission = &permission
	case "done", "close", "error", "auth_error":
		state.streaming = false
		state.permission = nil
	}
	state.mu.Unlock()

	// Track the event for persistence before publishing so the stored
	// stream reflects what the panel renders. A main-chat exchange is
	// stored by the window or the turn keeper, never here.
	if !state.main {
		a.accumulateSpotlightItem(evt, state)
	}

	// Forward the entire AgentEvent. Receiver decides what to render
	// per Type (assistant / tool_use / tool_result / thinking /
	// permission_request / compact_* / available_commands / error /
	// run_created / done / close). Keeping the wire shape identical
	// to the main UI's `agent:event` payload means we can share the
	// React processor between projects.
	hub.Publish(evt.Tag, spotlightStreamEvent{
		Kind: "agent_event",
		Data: jsonEncodeOrEmpty(evt),
	})

	// Persist on the runner-final boundary. `close` always fires
	// once per submission — even on cancellation (executePrompt
	// synthesises one). We do NOT CloseSession the hub here —
	// follow-up turns reuse the same SSE connection so the panel
	// stays mounted.
	if !state.main && (evt.Type == "close" || evt.Type == "error") {
		a.flushSpotlightItems(evt.Tag, state)
	}
}

// accumulateSpotlightItem turns one agent event into either a delta
// appended to the current live assistant/thinking buffer, or a new
// StreamItemData queued for persistence. Mirrors the case-by-case
// logic in useAgentStreaming.ts so the persisted shape matches.
func (a *App) accumulateSpotlightItem(evt AgentEvent, state *spotlightTagState) {
	state.mu.Lock()
	defer state.mu.Unlock()

	tag := evt.Tag
	runID := evt.RunID
	sessionID := evt.SessionID

	switch evt.Type {
	case "assistant":
		if evt.Content == "" {
			return
		}
		// Tool calls in between assistant deltas break continuity —
		// when we see one, currentAssistant is finalized + cleared, so
		// the next assistant delta starts a new row.
		if state.currentAssistant != nil && state.currentAssistant.runID == runID {
			state.currentAssistant.buf.WriteString(evt.Content)
			return
		}
		// Different runID (or no live assistant yet) — finalize any
		// stale ongoing assistant from a previous run, then start fresh.
		state.finalizeLiveItemLocked(&state.currentAssistant, "assistant")
		state.currentAssistant = &spotlightLiveItem{
			id:        fmt.Sprintf("%s-asst-%d", tag, state.sortOrder),
			sortOrder: state.sortOrder,
			runID:     runID,
			sessionID: sessionID,
		}
		state.currentAssistant.buf.WriteString(evt.Content)
		state.sortOrder++

	case "thinking":
		if evt.Content == "" {
			return
		}
		if state.currentThinking != nil && state.currentThinking.runID == runID {
			state.currentThinking.buf.WriteString(evt.Content)
			return
		}
		state.finalizeLiveItemLocked(&state.currentThinking, "thinking")
		state.currentThinking = &spotlightLiveItem{
			id:        fmt.Sprintf("%s-think-%d", tag, state.sortOrder),
			sortOrder: state.sortOrder,
			runID:     runID,
			sessionID: sessionID,
		}
		state.currentThinking.buf.WriteString(evt.Content)
		state.sortOrder++

	case "tool_use":
		// Tool calls break both assistant and thinking continuity.
		state.finalizeLiveItemLocked(&state.currentAssistant, "assistant")
		state.finalizeLiveItemLocked(&state.currentThinking, "thinking")
		toolInput := ""
		if len(evt.ToolInput) > 0 {
			toolInput = string(evt.ToolInput)
		}
		state.pending = append(state.pending, StreamItemData{
			ID:        fmt.Sprintf("%s-tool-%d", tag, state.sortOrder),
			Kind:      "tool",
			ToolName:  evt.ToolName,
			ToolInput: toolInput,
			ToolUseID: evt.ToolUseID,
			RunID:     runID,
			SessionID: sessionID,
			SortOrder: state.sortOrder,
		})
		state.sortOrder++

	case "tool_result":
		state.finalizeLiveItemLocked(&state.currentAssistant, "assistant")
		state.finalizeLiveItemLocked(&state.currentThinking, "thinking")
		content := evt.Content
		if content == "" {
			content = "Result received."
		}
		state.pending = append(state.pending, StreamItemData{
			ID:        fmt.Sprintf("%s-result-%d", tag, state.sortOrder),
			Kind:      "result",
			Content:   content,
			ToolName:  evt.ToolName,
			ToolUseID: evt.ToolUseID,
			RunID:     runID,
			SessionID: sessionID,
			SortOrder: state.sortOrder,
		})
		state.sortOrder++

	case "error":
		message := evt.Error
		if message == "" {
			message = evt.Content
		}
		state.finalizeLiveItemLocked(&state.currentAssistant, "assistant")
		state.finalizeLiveItemLocked(&state.currentThinking, "thinking")
		state.pending = append(state.pending, StreamItemData{
			ID:        fmt.Sprintf("%s-err-%d", tag, state.sortOrder),
			Kind:      "error",
			Content:   message,
			RunID:     runID,
			SessionID: sessionID,
			SortOrder: state.sortOrder,
		})
		state.sortOrder++
	}
}

// finalizeLiveItemLocked moves a live (assistant|thinking) buffer into
// the pending queue with its accumulated content. Caller must hold
// state.mu. After this, *liveRef is nil and the buffer is gone.
func (s *spotlightTagState) finalizeLiveItemLocked(liveRef **spotlightLiveItem, kind string) {
	live := *liveRef
	if live == nil {
		return
	}
	content := live.buf.String()
	if content == "" {
		*liveRef = nil
		return
	}

	s.pending = append(s.pending, StreamItemData{
		ID:        live.id,
		Kind:      kind,
		Content:   content,
		RunID:     live.runID,
		SessionID: live.sessionID,
		SortOrder: live.sortOrder,
	})
	*liveRef = nil
}

// flushSpotlightItems finalizes any live buffers and writes every
// queued StreamItemData to the store. Each row gets a fresh sort
// order (re-numbered from the current persist offset, so follow-up
// turns line up after the previous turn's items).
func (a *App) flushSpotlightItems(tag string, state *spotlightTagState) {
	if a.store == nil {
		return
	}

	state.mu.Lock()
	state.finalizeLiveItemLocked(&state.currentAssistant, "assistant")
	state.finalizeLiveItemLocked(&state.currentThinking, "thinking")
	pending := state.pending
	state.pending = nil
	state.mu.Unlock()

	if len(pending) == 0 {
		return
	}

	// Re-number sortOrder so it's monotonic across turns. Each turn
	// reserves [persistOffset, persistOffset+len) and bumps the
	// per-state offset for follow-ups.
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].SortOrder < pending[j].SortOrder })
	state.mu.Lock()
	base := max(0, state.sortOrder-len(pending))
	for i := range pending {
		pending[i].SortOrder = base + i
	}
	state.mu.Unlock()
	persisted := 0
	for _, item := range pending {
		if _, err := a.persistenceService().PersistStreamItem(context.Background(), tag, item); err != nil {
			state.mu.Lock()
			if state.persistErr == nil {
				state.persistErr = err
			}
			state.mu.Unlock()
			log.Printf("[spotlight] persist item failed tag=%s id=%s kind=%s err=%v",
				tag, item.ID, item.Kind, err)
			continue
		}
		persisted++
	}
	log.Printf("[spotlight] persisted %d items tag=%s", persisted, tag)
	a.notifySpotlightSessionChanged(tag)
}

// standaloneChatProjectID / standaloneChatWorkspaceID match the
// constants the main-UI React side uses (lib/stream-helpers.ts:
// STANDALONE_CHAT_PROJECT_ID) for chats that have no real filesystem
// project — "General Chat" in the sidebar. Spotlight conversations
// are functionally identical to standalone chats from the main UI's
// composer (no workdir, agent runs in a temp dir, full tool access),
// so they slot into the same virtual project. This way they show up
// alongside main-UI standalone chats and the user gets a single,
// unified history surface.
const (
	standaloneChatProjectID   = "__standalone_chat__"
	standaloneChatWorkspaceID = "__standalone_workspace__"
)

// ensureSpotlightSession lazily creates the standalone-chat
// project + workspace (no-op when they already exist — main UI may
// have created them already), and inserts a fresh session row for
// `tag` if one doesn't exist. The label is derived from the first
// prompt so the sidebar entry is meaningful rather than a raw
// "spotlight_42" id. Idempotent: subsequent calls for the same tag
// hit ON CONFLICT DO NOTHING.
func (a *App) ensureSpotlightSession(tag, firstPrompt string) error {
	if a.store == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	label := strings.TrimSpace(firstPrompt)
	if len(label) > 60 {
		label = label[:60] + "…"
	}
	if label == "" {
		label = "Quick Chat"
	}
	data := ProjectData{
		ID:                standaloneChatProjectID,
		Name:              "General Chat",
		Path:              "",
		IsYansuProject:    false,
		ActiveWorkspaceID: standaloneChatWorkspaceID,
		Workspaces: []WorkspaceData{{
			ID:        standaloneChatWorkspaceID,
			Label:     "General Chat",
			IsDefault: true,
			CreatedAt: now,
			UpdatedAt: now,
			Sessions: []SessionData{{
				LocalID:     tag,
				WorkspaceID: standaloneChatWorkspaceID,
				Label:       label,
				Model:       a.chatTaskModel(),
				CreatedAt:   now,
				UpdatedAt:   now,
			}},
		}},
	}
	return a.persistenceService().EnsureProjectStructure(context.Background(), data)
}

// Persist with a stable request identity so retry after an uncertain response
// cannot insert a second user message.
func (a *App) persistSpotlightUserMessage(tag, prompt, requestID string, state *spotlightTagState) (string, int, error) {
	state.mu.Lock()
	provisionalOrder := state.sortOrder
	state.mu.Unlock()
	id := fmt.Sprintf("%s-u-%d", tag, provisionalOrder)
	if requestID != "" {
		id = tag + "-u-" + requestID
	}
	item := StreamItemData{ID: id, Kind: "user", Content: prompt}
	order, err := a.persistenceService().PersistStreamItem(context.Background(), tag, item)
	if err != nil {
		return "", 0, err
	}
	saved, err := a.store.LoadStreamItem(context.Background(), id)
	if err != nil {
		return "", 0, err
	}
	if saved == nil || saved.SessionLocalID != tag || saved.Kind != "user" || saved.Content != prompt {
		return "", 0, fmt.Errorf("spotlight request identity does not match the saved message")
	}
	state.mu.Lock()
	if state.sortOrder <= order {
		state.sortOrder = order + 1
	}
	state.mu.Unlock()
	return id, order, nil
}

// notifySpotlightSessionChanged tells the main UI that a spotlight
// conversation's persisted state changed (new session, new turn).
// The renderer hook in App.tsx listens for "spotlight:session-changed"
// and re-runs LoadAllProjects so the sidebar picks up the new
// session row + stream items. No-op when ctx is nil (app not started).
func (a *App) notifySpotlightSessionChanged(tag string) {
	if a.ctx == nil {
		return
	}
	a.emitBrowserEvent("spotlight:session-changed", map[string]string{"tag": tag})
}

// subscribeSpotlightStream is the SSE handler's entry. Returns a
// channel that fires for each event published under sessionLocalID.
// The cancel fn must be called when the SSE connection drops.
func (a *App) subscribeSpotlightStream(sessionLocalID string, after uint64) (<-chan spotlightStreamEvent, func(), error) {
	if a.spotlightStream == nil {
		return nil, nil, fmt.Errorf("spotlight stream not initialized")
	}
	return a.spotlightStream.Subscribe(sessionLocalID, after)
}

// buildSpotlightDeliverable kicks off the existing automation pipeline
// from a deliverable proposal embedded in a chat session. v1 does not
// yet wire deliverable metadata through the spotlight stream, so this
// returns a not-implemented error; the spotlight UI can render the
// "Build →" button as disabled until a follow-up PR connects the
// automation_extractor pipeline.
func (a *App) buildSpotlightDeliverable(sessionLocalID, deliverableID string) (string, error) {
	log.Printf("[spotlight] build-deliverable requested: session=%s deliverable=%s",
		sessionLocalID, deliverableID)
	return "", fmt.Errorf("build-deliverable wiring is pending — see DESIGN.md §9")
}

// dismissSpotlight is called from /api/spotlight/dismiss when the user
// closes the panel. Tears down active SSE subscribers and drops the
// per-tag in-memory state so the next /show starts a fresh session
// (the persisted DB rows survive — main UI can still re-open the
// conversation from its session list).
func (a *App) dismissSpotlight() {
	if a.spotlightStream != nil {
		a.spotlightStream.mu.Lock()
		sessions := make([]string, 0, len(a.spotlightStream.subs)+len(a.spotlightStream.history))
		seen := make(map[string]struct{})
		for k := range a.spotlightStream.subs {
			sessions = append(sessions, k)
			seen[k] = struct{}{}
		}
		for k := range a.spotlightStream.history {
			if _, ok := seen[k]; !ok {
				sessions = append(sessions, k)
			}
		}
		a.spotlightStream.mu.Unlock()
		for _, s := range sessions {
			a.spotlightStream.CloseSession(s)
		}
	}

	a.spotlightTagsMu.Lock()
	a.spotlightTags = nil
	a.spotlightTagsMu.Unlock()
}

// expandSpotlightToMain handles the spotlight's "maximize" button:
// surface the main Yansu window, emit an event the React side picks up
// to switch to the standalone-chat project + activate the spotlight
// session, and tear down the spotlight panel state. The spotlight
// helper is responsible for hiding its own window (the supervisor
// keeps the helper process alive between invocations).
//
// sessionLocalID is the spotlight tag (e.g. "spotlight_<nanos>"); main
// UI's persistence layer keys sessions by the same id, so the React
// side can call handleSwitchSession(STANDALONE_CHAT_PROJECT_ID, tag)
// and the chat surface loads with the in-progress conversation.
func (a *App) expandSpotlightToMain(sessionLocalID string) {
	if a.ctx == nil {
		return
	}
	// Surface the main window. WindowShow + WindowUnminimise mirrors
	// the singleton-focus path so we don't miss the case where the
	// user had Yansu minimised when they invoked the spotlight.
	switchToRegularApp() // no-op on non-darwin
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)

	// Notify the React side. App.tsx listens for this and (a) refreshes
	// projects so a fresh spotlight session shows up in the sidebar,
	// then (b) switches to the standalone chat project + activates the
	// spotlight session.
	a.emitBrowserEvent("spotlight:expand-to-chat", map[string]string{
		"sessionLocalId": sessionLocalID,
	})

	// Tear down the spotlight panel's in-memory state — the panel is
	// going away and the next /show should start fresh.
	a.dismissSpotlight()
}

// jsonEncodeOrEmpty marshals v; on error returns "{}" so the SSE
// `data:` line is always valid JSON.
func jsonEncodeOrEmpty(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
