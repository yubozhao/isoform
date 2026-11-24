package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"yansu-agent/agent"

	"github.com/google/uuid"
)

// wechatMaxImageBytes caps the post-decryption size of an inbound image
// attachment. Images larger than this are skipped with a note, to keep the
// agent prompt (and base64 overhead) within reasonable bounds.
const wechatMaxImageBytes = 20 * 1024 * 1024 // 20 MB

// wechatMonitor long-polls the iLink Bot API for incoming messages and routes
// them to the local AgentRunner. This is the WeChat equivalent of daemonClient
// but communicates directly with WeChat servers instead of through DaemonHub.
type wechatMonitor struct {
	app   *App
	api   *wechatAPIClient
	creds wechatCredentials

	// contextTokens caches per-user context tokens for reply threading.
	contextTokens sync.Map // map[string]string (userID -> contextToken)

	// Per-sender lock map with refcount + lazy eviction — mirrors daemonClient
	// so long-running monitors don't leak one entry per unique WeChat contact.
	// Reuses the package-level threadLockEntry / threadLockEvictAfter defined
	// in daemon.go.
	threadMu    sync.Mutex
	threadLocks map[string]*threadLockEntry

	cancel context.CancelFunc
	done   chan struct{}

	// ownerMu guards creds.OwnerID and creds.PairingCode, which pairing changes.
	ownerMu sync.Mutex
	// connectedAt is when the bot was connected; older messages are ignored.
	connectedAt time.Time
	// hinted holds the senders already told how to pair.
	hinted map[string]bool
}

// acquireThreadLock locks the per-sender mutex for threadKey and returns an
// unlock function that the caller MUST defer. Implementation mirrors
// daemonClient.acquireThreadLock so concurrent messages from the same WeChat
// user serialise around the shared session/history state, and the map doesn't
// grow unbounded over a long-running monitor.
func (m *wechatMonitor) acquireThreadLock(threadKey string) func() {
	m.threadMu.Lock()

	now := time.Now()

	// Lazy eviction: only purge entries with no active users.
	for k, entry := range m.threadLocks {
		if entry.refCount == 0 && now.Sub(entry.lastUsed) > threadLockEvictAfter {
			delete(m.threadLocks, k)
		}
	}

	entry, ok := m.threadLocks[threadKey]
	if !ok {
		entry = &threadLockEntry{}
		m.threadLocks[threadKey] = entry
	}
	entry.refCount++
	entry.lastUsed = now

	m.threadMu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		m.threadMu.Lock()
		entry.refCount--
		entry.lastUsed = time.Now()
		m.threadMu.Unlock()
	}
}

func newWeChatMonitor(app *App, creds wechatCredentials) *wechatMonitor {
	return &wechatMonitor{
		app:         app,
		api:         newWeChatAPIClient(creds.BaseURL, creds.BotToken),
		creds:       creds,
		threadLocks: make(map[string]*threadLockEntry),
		done:        make(chan struct{}),
		connectedAt: connectorConnectedAt(creds.ConnectedAt, time.Now()),
		hinted:      map[string]bool{},
	}
}

// ownerPaired reports whether an owner has paired with this bot.
func (m *wechatMonitor) ownerPaired() bool {
	m.ownerMu.Lock()
	defer m.ownerMu.Unlock()
	return m.creds.paired()
}

// wechatPlainText joins the text items of a message, the text a pairing code
// is read from.
func wechatPlainText(msg wechatMessage) string {
	var text string
	for _, item := range msg.ItemList {
		if item.Type == wechatItemTypeText && item.TextItem != nil {
			text += item.TextItem.Text
		}
	}
	return text
}

// senderAllowed accepts messages only from the paired owner. Until the bot is
// paired, the only message accepted is the pairing code: it saves the sender as
// the owner and is answered with a confirmation, but starts no run.
func (m *wechatMonitor) senderAllowed(ctx context.Context, msg wechatMessage, text string) bool {
	m.ownerMu.Lock()
	if m.creds.paired() {
		owner := m.creds.OwnerID
		m.ownerMu.Unlock()
		if msg.FromUserID != owner {
			log.Printf("[wechat] refused a message from %s: only the paired owner, %s, may use this bot", msg.FromUserID, owner)
			return false
		}
		return true
	}
	if !connectorPairingMatches(m.creds.connectorOwnerState, text) {
		hint := !m.hinted[msg.FromUserID]
		m.hinted[msg.FromUserID] = true
		m.ownerMu.Unlock()
		log.Printf("[wechat] ignored a message from %s: the bot is not paired and the message is not the pairing code", msg.FromUserID)
		if hint {
			m.notice(ctx, msg, "This bot is not paired yet. Send the pairing code shown in Isoform's WeChat settings.")
		}
		return false
	}
	paired := m.creds
	paired.OwnerID = msg.FromUserID
	paired.OwnerPaired = true
	paired.PairingCode = ""
	if saved := loadWeChatCredentials(); saved == nil || saved.BotToken != m.creds.BotToken {
		m.ownerMu.Unlock()
		log.Printf("[wechat] pairing refused: the saved credentials are for another bot")
		return false
	}
	if err := saveWeChatCredentials(&paired); err != nil {
		m.ownerMu.Unlock()
		log.Printf("[wechat] could not save the paired owner: %s", activityPrivateLogField("error", err.Error()))
		m.notice(ctx, msg, "Isoform could not save the pairing. Try again.")
		return false
	}
	m.creds = paired
	m.ownerMu.Unlock()
	log.Printf("[wechat] paired with %s", msg.FromUserID)
	m.notice(ctx, msg, "Paired with Isoform. Only you can use this bot now.")
	return false
}

// notice replies to the user a notice is about, in the thread of the message.
func (m *wechatMonitor) notice(_ context.Context, msg wechatMessage, text string) {
	if err := sendWeChatText(m.api, msg.FromUserID, msg.ContextToken, text); err != nil {
		log.Printf("[wechat] send notice failed: %s", activityPrivateLogField("error", err.Error()))
	}
}

// run starts the long-polling loop. Blocks until stopped.
func (m *wechatMonitor) run() {
	defer close(m.done)

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	defer cancel()

	cursor := m.loadCursor()
	consecutiveErrors := 0

	log.Printf("[wechat] monitor started: %s %s", activityPrivateLogField("endpoint", m.api.baseURL), activityPrivateLogField("cursor", cursor))

	for {
		select {
		case <-ctx.Done():
			log.Printf("[wechat] monitor context cancelled, exiting poll loop")
			return
		default:
		}

		resp, err := m.api.GetUpdates(cursor)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= 3 {
				log.Printf("[wechat] %d consecutive errors, pausing 30s: %s", consecutiveErrors, activityPrivateLogField("error", err.Error()))
				select {
				case <-time.After(30 * time.Second):
				case <-ctx.Done():
					return
				}
				consecutiveErrors = 0
				continue
			}
			log.Printf("[wechat] getUpdates error (attempt %d): %s", consecutiveErrors, activityPrivateLogField("error", err.Error()))
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}
		consecutiveErrors = 0

		// errcode -14 is a normal long-poll timeout (no new messages). Just retry.
		if resp.Errcode == -14 {
			continue
		}

		if resp.Errcode != 0 {
			log.Printf("[wechat] getUpdates errcode %d: %s", resp.Errcode, activityPrivateLogField("error", resp.Errmsg))
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}

		// Update cursor.
		if resp.GetUpdatesBuf != "" {
			cursor = resp.GetUpdatesBuf
			m.saveCursor(cursor)
		}

		// Process messages.
		for i, msg := range resp.Msgs {
			log.Printf("[wechat] message[%d]: type=%d from=%s items=%d context_token=%d bytes",
				i, msg.MessageType, msg.FromUserID, len(msg.ItemList), len(msg.ContextToken))
			for j, item := range msg.ItemList {
				textContent := ""
				if item.TextItem != nil {
					textContent = item.TextItem.Text
				}
				log.Printf("[wechat]   item[%d]: type=%d %s %s",
					j, item.Type, activityPrivateLogField("text", textContent), activityPrivateLogField("file", item.FileName))
			}
			if msg.MessageType != wechatMessageTypeUser {
				log.Printf("[wechat] skipping non-user message (type=%d)", msg.MessageType)
				continue
			}
			go m.handleMessage(ctx, msg)
		}
	}
}

func (m *wechatMonitor) stop() {
	if m.cancel != nil {
		m.cancel()
	}
	<-m.done
}

// handleMessage processes a single incoming WeChat user message.
func (m *wechatMonitor) handleMessage(ctx context.Context, msg wechatMessage) {
	// Pairing and the owner check come first, so a stranger's message changes
	// no state and starts no run.
	var sentAt time.Time
	if msg.CreateTimeMS > 0 {
		sentAt = time.UnixMilli(msg.CreateTimeMS)
	}
	if connectorMessageIsStale(sentAt, m.connectedAt) {
		log.Printf("[wechat] ignored a message from before the bot was connected")
		return
	}
	if !m.senderAllowed(ctx, msg, wechatPlainText(msg)) {
		return
	}

	// Serialise messages from the same user so two rapid messages don't race
	// on the shared session ID / history state for this thread.
	threadKey := "wechat:" + msg.FromUserID
	unlock := m.acquireThreadLock(threadKey)
	defer unlock()

	// Record bot activity for smart notification routing.
	m.app.mu.Lock()
	m.app.wechatLastActivity = time.Now()
	m.app.mu.Unlock()

	// Cache context token for replies.
	if msg.ContextToken != "" {
		m.contextTokens.Store(msg.FromUserID, msg.ContextToken)
	}

	// Extract text + image items. Voice/video/file are not yet supported by the
	// agent; we surface a short note in the prompt so the user knows why they
	// weren't processed.
	var userText string // original text the user typed (without notes/defaults)
	var images []agent.ImageAttachment
	var unsupportedNotes []string
	// newImageAttempted tracks whether the user sent any image item this turn,
	// regardless of whether it made it into `images` (download failures, missing
	// CDN info, and oversize attachments all land in unsupportedNotes instead).
	// Used below to suppress the visual-anchor policy when the user is trying
	// to send a new image — otherwise a failed new-image turn would be answered
	// against the previous image and ignore the failure notes.
	var newImageAttempted bool

	for _, item := range msg.ItemList {
		switch item.Type {
		case wechatItemTypeText:
			if item.TextItem != nil {
				userText += item.TextItem.Text
			}
		case wechatItemTypeImage:
			newImageAttempted = true
			if item.ImageItem == nil || item.ImageItem.Media == nil {
				log.Printf("[wechat] image item from %s has no image_item.media, skipping", msg.FromUserID)
				unsupportedNotes = append(unsupportedNotes, "[图片缺少 CDN 信息，无法读取]")
				continue
			}
			data, mime, err := m.api.DownloadAndDecryptMedia(ctx, item.ImageItem.Media)
			if err != nil {
				log.Printf("[wechat] failed to download image from %s: %s", msg.FromUserID, activityPrivateLogField("error", err.Error()))
				unsupportedNotes = append(unsupportedNotes, fmt.Sprintf("[图片下载失败: %v]", err))
				continue
			}
			if len(data) > wechatMaxImageBytes {
				log.Printf("[wechat] image too large (%d bytes), skipping", len(data))
				unsupportedNotes = append(unsupportedNotes, fmt.Sprintf("[图片过大 %d 字节，已跳过]", len(data)))
				continue
			}
			images = append(images, agent.ImageAttachment{
				MediaType:  mime,
				DataBase64: base64.StdEncoding.EncodeToString(data),
			})
		case wechatItemTypeVoice:
			unsupportedNotes = append(unsupportedNotes, "[语音消息，暂不支持解析]")
		case wechatItemTypeVideo:
			unsupportedNotes = append(unsupportedNotes, "[视频消息，暂不支持解析]")
		case wechatItemTypeFile:
			name := item.FileName
			if name == "" {
				name = "文件"
			}
			unsupportedNotes = append(unsupportedNotes, fmt.Sprintf("[文件附件「%s」，暂不支持解析]", name))
		default:
			log.Printf("[wechat] unknown item type %d from %s, skipping", item.Type, msg.FromUserID)
		}
	}

	if len(userText) == 0 && len(images) == 0 && len(unsupportedNotes) == 0 {
		log.Printf("[wechat] skipping empty message from %s (no content extracted)", msg.FromUserID)
		return
	}
	if userText != "" {
		_ = m.app.observeCommunicationMessage(communicationMessage{Source: "wechat", App: "WeChat", MessageID: fmt.Sprintf("%d", msg.MessageID), AuthorID: msg.FromUserID, OwnerID: msg.FromUserID, Text: userText, SentAt: sentAt, BodyAvailable: true})
	}

	// promptText is what we send to the agent this turn. It combines the user's
	// actual text, any unsupported-type notes, and a sensible default when the
	// user sent only images (so the agent doesn't respond with nothing).
	promptText := userText
	if len(unsupportedNotes) > 0 {
		if promptText != "" {
			promptText += "\n\n"
		}
		promptText += strings.Join(unsupportedNotes, "\n")
	}
	if promptText == "" {
		promptText = "用户发来一张图片，请查看并回复。"
	}

	// historyUserMsg is what we persist as the "user turn" for future context.
	// Unlike promptText, this describes what the user ACTUALLY sent (no default
	// stand-in) so a follow-up turn asking about a previously-sent image can
	// tell there was one, even after an agent session resume drops the image bytes.
	historyUserMsg := buildWeChatHistoryUserMsg(userText, len(images), unsupportedNotes)

	log.Printf("[wechat] received from %s: %s images=%d", msg.FromUserID, activityPrivateLogField("text", promptText), len(images))

	// Send typing indicator: fetch ticket on-demand via getconfig (requires user + context token).
	contextToken := ""
	if v, ok := m.contextTokens.Load(msg.FromUserID); ok {
		contextToken = v.(string)
	}
	if contextToken != "" {
		cfg, err := m.api.GetConfig(msg.FromUserID, contextToken)
		if err != nil {
			log.Printf("[wechat] getconfig error for typing: %s", activityPrivateLogField("error", err.Error()))
		} else if cfg.TypingTicket != "" {
			if err := m.api.SendTyping(msg.FromUserID, cfg.TypingTicket); err != nil {
				log.Printf("[wechat] sendTyping error: %s", activityPrivateLogField("error", err.Error()))
			} else {
				log.Printf("[wechat] typing indicator sent to %s", msg.FromUserID)
			}
		}
	}

	// Route to AgentRunner.
	m.app.mu.Lock()
	runner := m.app.runner
	m.app.mu.Unlock()

	if runner == nil {
		log.Printf("[wechat] AgentRunner is nil, cannot process message")
		m.sendTextReply(msg.FromUserID, "Agent is not ready. Please try again later.")
		return
	}

	log.Printf("[wechat] routing message to AgentRunner from %s", msg.FromUserID)

	taskCtx, taskCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer taskCancel()

	// WeChat-relayed message → agent reply is a first-class agentloop entry
	// point. Mint trace_id and emit kickoff event.
	traceID := uuid.NewString()
	if m.app.telemetry != nil {
		m.app.telemetry.Capture("agentloop.connector.message_received", map[string]any{
			"trace_id":    traceID,
			"connector":   "wechat",
			"transport":   "local",
			"thread_key":  threadKey,
			"has_image":   len(images) > 0,
			"image_count": len(images),
		})
	}

	// Stash the trace_id under threadKey so the post-run TriggerMemory
	// call below inherits it — without this the connector memory
	// pipeline reads "" and suppresses agentloop.* events.
	m.app.mu.Lock()
	m.app.pendingTraceIDs[threadKey] = traceID
	m.app.mu.Unlock()

	// Bound the entry's lifetime via defer so panics don't leak it. See
	// the matching block in daemon.go's handleSlackEvent for the full
	// rationale (compare-and-delete with captured traceID).
	defer schedulePendingTraceIDCleanup(m.app, threadKey, traceID)

	input := &agent.TaskInput{
		Model:            m.app.chatTaskModel(),
		WorkDir:          m.app.getOrCreateSessionTempDir(threadKey),
		Images:           images,
		TraceID:          traceID,
		AutoApproveTools: true,
		// Inbound connector messages come from people other than the user
		// and nobody reviews the run: never publish anything on their behalf.
		DenyExternalWrites: true,
	}
	// Read-only reach: its own folder, web search and the research opencli
	// calls (connector_authority.go).
	input.AuthorityGrants = connectorRunGrants(input.WorkDir)
	var historyItems []conversationItem
	if m.app.store != nil {
		input.SessionID, _ = m.app.store.GetUIState(taskCtx, "daemon:session:"+threadKey)
		// Load rolling per-thread history so the agent retains context across
		// turns even when agent session resume fails (e.g. after app restart)
		// and a fresh session is created. The runner injects input.History
		// automatically when it falls back to a fresh session.
		historyItems = loadConversationHistory(m.app.store, threadKey)
		input.History = renderConversationItems(historyItems)
	}

	// Visual anchor: on a text follow-up directly after an image the user
	// previously sent, prepend a policy reminding the agent to trust its
	// earlier identification instead of re-evaluating the image from scratch.
	// Covers both runner paths (resume success — policy rides as a regular user
	// message; fresh-session fallback — policy survives as part of input.Prompt
	// which the runner appends after input.History).
	//
	// Injected BEFORE composePrompt so the standard system contexts
	// (yansu/memory/activity/...) still come first and the anchor lands
	// directly above the user prompt — preserving normal instruction
	// hierarchy. See shouldAnchorVisualContext for the trigger conditions.
	finalPromptText := promptText
	if shouldAnchorVisualContext(historyItems, newImageAttempted, userText) {
		finalPromptText = visualAnchorPolicy + "\n\n" + promptText
	}
	input.Prompt = m.app.composePromptFor(finalPromptText, m.ownerPaired())
	log.Printf("[wechat] agent input: %s sessionID=%q prompt_len=%d images=%d history_len=%d",
		activityPrivateLogField("work_dir", input.WorkDir), input.SessionID, len(input.Prompt), len(input.Images), len(input.History))

	refusals := &connectorRefusals{}
	var newSessionID, lastContent string
	eventCount := 0
	_, err := runner.ExecuteTask(taskCtx, input, func(event *agent.StreamEvent) error {
		eventCount++
		refusals.observe(event)
		if event.SessionID != "" {
			newSessionID = event.SessionID
		}
		switch event.Type {
		case "text_delta", "assistant":
			if event.Content != "" {
				lastContent += event.Content
			}
		}
		if eventCount <= 5 || eventCount%20 == 0 {
			log.Printf("[wechat] agent event #%d: type=%s content_len=%d session=%s",
				eventCount, event.Type, len(event.Content), event.SessionID)
		}
		return nil
	})

	errorDiagnostic := ""
	if err != nil {
		errorDiagnostic = activityPrivateLogField("error", err.Error())
	}
	log.Printf("[wechat] agent finished: events=%d reply_len=%d err_present=%t %s newSession=%s",
		eventCount, len(lastContent), err != nil, errorDiagnostic, newSessionID)

	// Persist session ID for thread continuity.
	if newSessionID != "" && m.app.store != nil {
		_ = m.app.store.SetUIState(context.Background(), "daemon:session:"+threadKey, newSessionID)
	}

	// Persist this exchange so future turns get rolling text context via
	// input.History even when the agent session can't be resumed, and trigger
	// the memory pipeline so the soul/memory stays up to date.
	if lastContent != "" {
		summary, roundCount := appendAndSummarizeConversation(m.app.store, threadKey, historyUserMsg, lastContent)
		m.app.triggerMemory(memorySourceWeChat, threadKey, summary, roundCount)
	}

	// Send reply, saying in plain words what the run was refused.
	if reply := withRefusalNote(lastContent, refusals, "WeChat"); reply != "" {
		log.Printf("[wechat] sending reply to %s (%d chars)", msg.FromUserID, len(reply))
		m.sendTextReply(msg.FromUserID, reply)
	} else if err != nil {
		log.Printf("[wechat] sending error reply to %s: %s", msg.FromUserID, activityPrivateLogField("error", err.Error()))
		m.sendTextReply(msg.FromUserID, "Sorry, I encountered an error processing your message.")
	} else {
		log.Printf("[wechat] no content to reply to %s (agent returned empty)", msg.FromUserID)
	}
}

// sendTextReply sends a plain text reply to a WeChat user.
func (m *wechatMonitor) sendTextReply(toUserID, text string) {
	if err := m.app.authorizeInboundConnectorReply(context.Background(), "wechat", toUserID, toUserID); err != nil {
		log.Printf("[wechat] reply authority denied: %s", activityPrivateLogField("error", err.Error()))
		return
	}
	contextToken, _ := m.contextTokens.Load(toUserID)
	ct, _ := contextToken.(string)
	log.Printf("[wechat] sendTextReply to %s: context_token=%d bytes, text=%d chars",
		toUserID, len(ct), len(text))
	if err := sendWeChatText(m.api, toUserID, ct, text); err != nil {
		log.Printf("[wechat] sendTextReply error: %s", activityPrivateLogField("error", err.Error()))
	} else {
		log.Printf("[wechat] sendTextReply success to %s", toUserID)
	}
}

// --- Cursor persistence ---

func wechatStateDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "wechat"), nil
}

func (m *wechatMonitor) loadCursor() string {
	dir, err := wechatStateDir()
	if err != nil {
		log.Printf("[wechat] state directory unavailable: %s", activityPrivateLogField("error", err.Error()))
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "cursor.json"))
	if err != nil {
		return ""
	}
	var state struct {
		Cursor string `json:"cursor"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	return state.Cursor
}

func (m *wechatMonitor) saveCursor(cursor string) {
	dir, err := wechatStateDir()
	if err != nil {
		log.Printf("[wechat] state directory unavailable: %s", activityPrivateLogField("error", err.Error()))
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	data, _ := json.Marshal(map[string]string{"cursor": cursor})
	_ = os.WriteFile(filepath.Join(dir, "cursor.json"), data, 0600)
}

// truncate shortens a string to maxLen characters for logging.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("...(%d more)", len(s)-maxLen)
}

// buildWeChatHistoryUserMsg renders the user's turn as it will be persisted
// into the rolling history. Images are represented as a count-placeholder
// because we only store text (the image bytes can be huge and agent session
// resume, when it succeeds, already carries the multimodal content). Keeping
// this marker lets the agent reason about questions like "what was in the
// picture I sent" even after a process restart drops the session.
func buildWeChatHistoryUserMsg(userText string, imageCount int, unsupportedNotes []string) string {
	parts := make([]string, 0, 3)
	if userText != "" {
		parts = append(parts, userText)
	}
	if imageCount > 0 {
		parts = append(parts, fmt.Sprintf("[user sent %d image(s)]", imageCount))
	}
	if len(unsupportedNotes) > 0 {
		parts = append(parts, strings.Join(unsupportedNotes, " "))
	}
	return strings.Join(parts, " ")
}
