package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"yansu-agent/agent"

	"wails-gui/internal/destinations"
)

// externalRootConversationKey is the synthetic thread discriminator for
// non-threaded external conversations. It keeps top-level DM/channel exchanges
// in one rolling conversation instead of fragmenting by message ID.
const externalRootConversationKey = "__root__"

// Per-thread conversation history is now maintained by the package-level
// helpers in conversation_history.go so the WeChat monitor (and any future
// non-daemon integration) can share the same rolling-window storage.

// externalConversationKey returns the conversation key used for
// external messages. Real threads stay isolated. For DMs, each top-level
// message uses its own eventTS as the conversation key so that
// completion notifications can thread back to the original message.
func (d *daemonClient) externalConversationKey(channelID, threadTS, eventTS, chatType string) string {
	isDirectChat := chatType == "p2p" || chatType == "dm" || strings.HasPrefix(channelID, "D")

	convID := externalRootConversationKey
	switch {
	case isDirectChat:
		if threadTS != "" && threadTS != eventTS {
			// DM thread reply — continue the existing conversation for that thread.
			convID = threadTS
		} else if eventTS != "" {
			// DM top-level message — use the message TS so notifications can
			// reply in-thread (matches how stream responses thread via EventTS).
			convID = eventTS
		}
	case threadTS != "" && threadTS != eventTS:
		convID = threadTS
	case threadTS == "" && eventTS != "":
		convID = eventTS
	case threadTS != "":
		// Feishu group roots currently arrive with threadTS == eventTS due to
		// upstream fallback. Keep the root message ID so later thread replies
		// reuse the same conversation key.
		convID = threadTS
	}
	return d.platform + ":" + d.teamID + ":" + d.userID + ":" + channelID + ":" + convID
}

// daemonClient is a WebSocket client that connects to the backend DaemonHub
// and routes incoming platform events from chat connectors to the local AgentRunner.
type daemonClient struct {
	app        *App
	platform   string // connector platform
	backendURL string
	token      string // initial token; use getToken() for HTTP calls to pick up refreshes
	teamID     string // platform team or app ID
	userID     string // platform user ID

	conn   *websocket.Conn
	sendMu sync.Mutex

	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	connected bool

	// Per-thread mutex: ensures messages within the same thread are processed
	// serially to prevent concurrent session ID read/write races.
	threadMu    sync.Mutex
	threadLocks map[string]*threadLockEntry

	// interactiveSem caps how many interactive events (long Claude sessions)
	// run in parallel. Without this a burst of 20 messages spawns 20
	// concurrent 10-minute LLM sessions.
	interactiveSem chan struct{}
}

func readAllWithHardLimit(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("content exceeds %d bytes", limit)
	}
	return data, nil
}

type threadLockEntry struct {
	mu       sync.Mutex
	refCount int       // number of goroutines holding or waiting for this lock
	lastUsed time.Time // updated on acquire and release
}

// maxInteractiveConcurrency caps concurrent Claude sessions per daemon
// connection. Messages beyond this count queue (bounded by the reader
// loop's natural rate) instead of fanning out into unbounded goroutines.
const maxInteractiveConcurrency = 8

func newDaemonClient(app *App, platform, backendURL, token, teamID, userID string) *daemonClient {
	return &daemonClient{
		app:            app,
		platform:       platform,
		backendURL:     backendURL,
		token:          token,
		teamID:         teamID,
		userID:         userID,
		threadLocks:    make(map[string]*threadLockEntry),
		done:           make(chan struct{}),
		interactiveSem: make(chan struct{}, maxInteractiveConcurrency),
	}
}

// senderAllowed checks the relay's sender against the platform identity bound
// when this daemon connected. Backend fanout alone is not authority to access
// the owner's context. Missing identities fail closed, including old payloads.
func (d *daemonClient) senderAllowed(senderID string) bool {
	owner := strings.TrimSpace(d.userID)
	if owner != "" && strings.TrimSpace(senderID) == owner {
		return true
	}
	log.Printf("[daemon] refused interactive %s message: sender is not the bound owner", d.platform)
	return false
}

const threadLockEvictAfter = 30 * time.Minute

// acquireThreadLock locks the per-thread mutex for the given key and returns
// an unlock function. The caller MUST defer the returned function.
//
// Internally it tracks a reference count so that lazy eviction never removes
// an entry that is still held or has goroutines waiting on it.
func (d *daemonClient) acquireThreadLock(key string) func() {
	d.threadMu.Lock()

	now := time.Now()

	// Lazy eviction: only purge entries with no active users.
	for k, entry := range d.threadLocks {
		if entry.refCount == 0 && now.Sub(entry.lastUsed) > threadLockEvictAfter {
			delete(d.threadLocks, k)
		}
	}

	entry, ok := d.threadLocks[key]
	if !ok {
		entry = &threadLockEntry{}
		d.threadLocks[key] = entry
	}
	entry.refCount++
	entry.lastUsed = now

	d.threadMu.Unlock()

	// Block until we own the per-thread mutex.
	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		d.threadMu.Lock()
		entry.refCount--
		entry.lastUsed = time.Now()
		d.threadMu.Unlock()
	}
}

// getToken returns the freshest available Yansu token, falling back to the
// initially captured token if the reload fails. This prevents silent 401s
// after an OAuth token refresh (typically every ~1 hour).
func (d *daemonClient) getToken() string {
	if fresh, err := loadYansuToken(); err == nil && fresh != "" {
		return fresh
	}
	return d.token
}

func (d *daemonClient) isConnected() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connected
}

func (d *daemonClient) setConnected(v bool) {
	d.mu.Lock()
	d.connected = v
	d.mu.Unlock()
}

// run starts the daemon listener with reconnection logic. Blocks until stopped.
func (d *daemonClient) run() {
	close(d.done)
}

func (d *daemonClient) stop() {
	d.mu.Lock()
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-d.done
}

func buildDaemonWSURL(baseURL string) (string, bool) {
	return "", false
}

// daemonRelayGuardrail is prepended to every daemon-relayed IM message. These
// sessions run unattended with auto-approved tools on the user's machine, so
// externally-visible write operations must be gated on explicit confirmation
// from the user inside the conversation (thread sessions resume, so a
// confirm-then-act flow works across messages).
const daemonRelayGuardrail = `[Safety rules — external side effects. MUST follow]
This request arrived via a chat relay and runs unattended. Before ANY
externally-visible write operation — pushing commits (git push), creating,
approving or merging pull requests or issues, publishing packages, sending
messages or emails to third parties, or calling any API that changes state
outside this machine — you MUST have explicit confirmation from the user in
this conversation:
- If the user's current message explicitly and unambiguously requests that
  exact write operation, treat it as confirmed.
- Otherwise, reply describing what you propose to do and ask the user to
  confirm, then stop. Never perform an unrequested external write.
- NEVER approve code reviews or spend the user's personal sign-off
  (approvals, reviews, votes) — even if asked; the user must do that
  themselves.
- Replying in this chat thread is the expected output channel and needs no
  confirmation. Local, reversible work in the working directory (reading
  code, editing files, running tests) also needs no confirmation.

`

// handleSlackEvent processes a single platform message by running it through the local AgentRunner.
// A bound owner's private conversation gets personal context; shared replies
// use isolated sessions without it.
func (d *daemonClient) handleSlackEvent(relayID, senderID, channelID, threadTS, eventTS, chatType, text string, images []ImageData) {
	if !d.senderAllowed(senderID) {
		return
	}
	personal := connectorPersonalContext(true, chatType)
	if err := d.app.authorizeInboundConnectorReply(context.Background(), d.platform, relayID, relayID); err != nil {
		log.Printf("[daemon] reply authority denied platform=%s relay=%s: %v", d.platform, relayID, err)
		return
	}
	d.sendJSON(map[string]string{
		"type":     "stream_start",
		"relay_id": relayID,
	})

	d.app.mu.Lock()
	runner := d.app.runner
	d.app.mu.Unlock()

	if runner == nil {
		d.sendJSON(map[string]string{
			"type":     "reply",
			"relay_id": relayID,
			"text":     "Agent is not ready. Please try again later.",
		})
		d.sendJSON(map[string]string{
			"type":     "stream_end",
			"relay_id": relayID,
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Daemon-relayed external chat messages are first-class agentloop entry
	// points (the user is talking to Yansu via an external IM platform).
	// Mint the trace_id and emit the kickoff event so the PostHog dashboard
	// can attribute the run to the source platform. `connector` names the
	// platform only (slack / feishu / discord / telegram); the
	// orthogonal `transport` property captures HOW the message reached
	// the desktop ("daemon" via the backend WS relay vs "local" for the
	// pipelines that poll the platform directly). PostHog can group by
	// connector to count platform usage AND segment by transport without
	// needing two different connector strings per platform.
	traceID := uuid.NewString()
	if d.app.telemetry != nil {
		d.app.telemetry.Capture("agentloop.connector.message_received", map[string]any{
			"trace_id":  traceID,
			"connector": d.platform,
			"transport": "daemon",
			"chat_type": chatType,
			"has_image": len(images) > 0,
		})
	}

	// DenyExternalWrites: relay sessions run unattended with auto-approved
	// tools; pushing commits / PR actions must happen from the desktop app
	// or terminal where the user acts personally (matches the CLI daemon).
	// Explicit chat-tier model: connector replies are conversational, the
	// same class of work as GUI chat — never the code/opus tier.
	input := &agent.TaskInput{Prompt: text, Model: d.app.chatTaskModel(), TraceID: traceID, AutoApproveTools: true, DenyExternalWrites: true}
	// Phase 1.5: connector-driven chats (Slack / Feishu / Discord /
	// Telegram DMs etc.) are user-initiated even though there
	// is no GUI in front — wire the CE collector + redactor so these
	// runs show up in the dashboard with the same L1+L2 fields as
	// streaming.go's user chat. Tier comes from the same global
	// telemetry_tier_pref so the user's single Privacy setting
	// covers both surfaces.
	input.ContextEngineCollector = d.app.buildCEContextCollector("default")
	input.Redactor = buildCERedactor()
	for _, img := range images {
		if img.MediaType != "" && img.DataBase64 != "" {
			input.Images = append(input.Images, agent.ImageAttachment{
				MediaType:  img.MediaType,
				DataBase64: img.DataBase64,
			})
		}
	}

	// Only private conversations receive personal context. Shared sessions use
	// a fresh namespace so old prompts and scratch files cannot bring it back.
	threadKey := d.externalConversationKey(channelID, threadTS, eventTS, chatType)
	threadKey = connectorContextThreadKey(threadKey, personal)

	input.Prompt = d.app.composePromptFor(text, personal, "", d.platform)
	input.WorkDir = d.app.getOrCreateSessionTempDir(threadKey)
	if d.app.store != nil {
		input.SessionID, _ = d.app.store.GetUIState(ctx, "daemon:session:"+threadKey)
	}

	// Stash the freshly-minted trace_id under the per-thread key so the
	// post-run TriggerMemory(threadKey, ...) call below can pick it up.
	// Without this, only chat sessions initiated via streaming.go's
	// executePrompt ever populate the map, and the connector-driven
	// post-processing always reads "" and silently flips into
	// SuppressTelemetry.
	d.app.mu.Lock()
	d.app.pendingTraceIDs[threadKey] = traceID
	d.app.mu.Unlock()

	// Bound the entry's lifetime via a defer so a panic in ExecuteTask
	// or any of the post-run helpers still releases the map slot —
	// straight-line placement at end-of-function would have leaked
	// the entry forever on panic in a long-running daemon. Compare-
	// and-delete: schedulePendingTraceIDCleanup only removes the
	// value if it still equals the traceID this defer captured, so a
	// follow-up message on the same thread that overwrites the entry
	// is unaffected.
	defer schedulePendingTraceIDCleanup(d.app, threadKey, traceID)

	input.Prompt = daemonRelayGuardrail + input.Prompt
	// Read-only reach: its own folder, web search and the research opencli
	// calls (connector_authority.go).
	input.AuthorityGrants = connectorRunGrants(input.WorkDir)
	refusals := &connectorRefusals{}

	var newSessionID, lastContent string
	_, err := runner.ExecuteTask(ctx, input, func(event *agent.StreamEvent) error {
		refusals.observe(event)
		if event.SessionID != "" {
			newSessionID = event.SessionID
		}
		switch event.Type {
		case "text_delta", "assistant":
			if event.Content != "" {
				lastContent += event.Content
				d.sendJSON(map[string]string{
					"type":     "stream_chunk",
					"relay_id": relayID,
					"text":     event.Content,
				})
			}
		case "error":
			d.sendJSON(map[string]string{
				"type":     "stream_chunk",
				"relay_id": relayID,
				"text":     "Error: " + event.Error,
			})
		}
		return nil
	})

	// Persist the new Claude session ID before the error check so that even if ExecuteTask
	// returned an error after emitting a session ID, the next message in this thread can
	// resume. Use context.Background() so a timed-out ctx does not block the write.
	if threadKey != "" && newSessionID != "" && d.app.store != nil {
		_ = d.app.store.SetUIState(context.Background(), "daemon:session:"+threadKey, newSessionID)
	}

	// Always persist the conversation history for this thread so the memory
	// summary stays current. The memory pipeline applies its own debounce and
	// cooldown, so TriggerMemory is safe to call on every turn.
	if threadKey != "" && lastContent != "" {
		summary, roundCount := appendAndSummarizeConversation(d.app.store, threadKey, text, lastContent)
		d.app.triggerMemory(memorySource(d.platform), threadKey, summary, roundCount)
	}

	if err != nil && lastContent == "" {
		d.sendJSON(map[string]string{
			"type":     "reply",
			"relay_id": relayID,
			"text":     "Sorry, I encountered an error processing your message.",
		})
	}

	if note := refusals.note(connectorDisplayName(d.platform)); note != "" {
		d.sendJSON(map[string]string{
			"type":     "stream_chunk",
			"relay_id": relayID,
			"text":     "\n\n" + note,
		})
	}

	d.sendJSON(map[string]string{
		"type":     "stream_end",
		"relay_id": relayID,
	})
}

func (d *daemonClient) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.sendJSON(map[string]string{"type": "heartbeat"})
		case <-ctx.Done():
			return
		}
	}
}

// feishuFileInfo carries the fields needed to download a Feishu message attachment.
type feishuFileInfo struct {
	AppID     string
	TenantKey string
	MessageID string
	ImageKey  string
}

// processFeishuFiles downloads each Feishu image attachment and returns them as ImageData.
func (d *daemonClient) processFeishuFiles(ctx context.Context, files []feishuFileInfo) (images []ImageData, extraText string) {
	for _, f := range files {
		if f.ImageKey == "" || f.MessageID == "" {
			continue
		}
		content, mimeType, err := d.downloadFeishuImageContent(ctx, f.AppID, f.TenantKey, f.MessageID, f.ImageKey)
		if err != nil {
			log.Printf("[gui-daemon] failed to download feishu image %s: %v", f.ImageKey, err)
			extraText += fmt.Sprintf("\n[Image download failed: %s]", f.ImageKey)
			continue
		}
		images = append(images, ImageData{
			MediaType:  mimeType,
			DataBase64: base64.StdEncoding.EncodeToString(content),
		})
	}
	return
}

// downloadFeishuImageContent downloads a Feishu message image via the backend proxy
// (which holds the tenant access token).
func (d *daemonClient) downloadFeishuImageContent(ctx context.Context, appID, tenantKey, messageID, imageKey string) ([]byte, string, error) {
	return nil, "", errNoYansuAccount
}

// slackFileInfo carries the fields needed to process a Slack file attachment.
type slackFileInfo struct {
	ID       string
	Name     string
	MimeType string
	URL      string
	Size     int
}

// processSlackFiles downloads each file and categorises it:
//   - image/*        → appended to returned images (multimodal input)
//   - text-based     → content included inline in returned extraText
//   - everything else → brief mention appended to extraText
func (d *daemonClient) processSlackFiles(ctx context.Context, files []slackFileInfo) (images []ImageData, extraText string) {
	const maxTextBytes = 50 * 1024

	for _, f := range files {
		if f.Size > 10*1024*1024 {
			log.Printf("[gui-daemon] skipping %s: file too large (%d bytes)", f.Name, f.Size)
			extraText += fmt.Sprintf("\n[Attachment: %s (%s, %d bytes) — skipped: file too large]", f.Name, f.MimeType, f.Size)
			continue
		}

		content, resolvedMIME, err := d.downloadSlackFileContent(ctx, f.ID, f.URL, f.MimeType)
		if err != nil {
			log.Printf("[gui-daemon] failed to download %s: %v", f.Name, err)
			extraText += fmt.Sprintf("\n[Attachment: %s (%s, %d bytes) — download failed]", f.Name, f.MimeType, f.Size)
			continue
		}

		switch {
		case strings.HasPrefix(resolvedMIME, "image/"):
			images = append(images, ImageData{
				MediaType:  resolvedMIME,
				DataBase64: base64.StdEncoding.EncodeToString(content),
			})
		case isTextMIME(resolvedMIME):
			truncated := ""
			text := content
			if len(content) > maxTextBytes {
				text = content[:maxTextBytes]
				truncated = " (truncated)"
			}
			// Use a fence that cannot collide with the file content.
			fence := "```"
			for bytes.Contains(text, []byte(fence)) {
				fence += "`"
			}
			extraText += fmt.Sprintf("\n[File: %s (%s)%s]\n%s\n%s\n%s", f.Name, resolvedMIME, truncated, fence, string(text), fence)
		default:
			extraText += fmt.Sprintf("\n[Attachment: %s (%s, %d bytes)]", f.Name, f.MimeType, f.Size)
		}
	}
	return
}

// isTextMIME reports whether the MIME type represents human-readable text.
func isTextMIME(mimeType string) bool {
	textPrefixes := []string{
		"text/",
		"application/json",
		"application/xml",
		"application/javascript",
		"application/x-yaml",
		"application/yaml",
		"application/toml",
		"application/x-sh",
	}
	for _, prefix := range textPrefixes {
		if strings.HasPrefix(mimeType, prefix) {
			return true
		}
	}
	return false
}

// downloadSlackFileContent downloads a file via the backend API (which holds the bot token)
// and returns the raw content bytes and resolved MIME type.
func (d *daemonClient) downloadSlackFileContent(ctx context.Context, fileID, fileURL, fallbackMIME string) ([]byte, string, error) {
	return nil, "", errNoYansuAccount
}

// processDiscordFiles downloads Discord attachments directly from CDN (no backend proxy needed).
// Discord CDN URLs are publicly accessible temporary signed URLs.
//
//isoform:destination chat_bridges
func (d *daemonClient) processDiscordFiles(ctx context.Context, files []struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	MimeType           string `json:"mimetype"`
	DiscordMimeType    string `json:"mime_type"`
	URLPrivate         string `json:"url_private"`
	URLPrivateDownload string `json:"url_private_download"`
	Size               int    `json:"size"`
	Thumb360           string `json:"thumb_360"`
	ImageKey           string `json:"image_key"`
	MessageID          string `json:"message_id"`
	Type               string `json:"type"`
	FileID             string `json:"file_id"`
	URL                string `json:"url"`
}) (images []ImageData, extraText string) {
	const maxTextBytes = 50 * 1024
	const maxDiscordAttachmentBytes int64 = 10 * 1024 * 1024

	for _, f := range files {
		cdnURL := f.URL
		if cdnURL == "" {
			continue
		}
		mimeType := f.DiscordMimeType
		if mimeType == "" {
			mimeType = f.MimeType
		}
		if int64(f.Size) > maxDiscordAttachmentBytes {
			log.Printf("[gui-daemon] skipping Discord file %s: too large (%d bytes)", f.Name, f.Size)
			extraText += fmt.Sprintf("\n[Attachment: %s (%s, %d bytes) - skipped: file too large]", f.Name, mimeType, f.Size)
			continue
		}

		// Download directly from Discord CDN (publicly accessible signed URL).
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdnURL, nil)
		if err != nil {
			log.Printf("[gui-daemon] Discord download request error for %s: %v", f.Name, err)
			continue
		}
		req = destinations.CountRequest(req, destinations.ChatBridges)
		resp, err := httpClientWithTimeout.Do(req)
		if err != nil {
			log.Printf("[gui-daemon] Discord download error for %s: %v", f.Name, err)
			extraText += fmt.Sprintf("\n[Attachment: %s (%s) - download failed]", f.Name, mimeType)
			continue
		}
		content, err := readAllWithHardLimit(resp.Body, maxDiscordAttachmentBytes)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			log.Printf("[gui-daemon] Discord download failed for %s: status=%d err=%v", f.Name, resp.StatusCode, err)
			extraText += fmt.Sprintf("\n[Attachment: %s (%s) - download failed]", f.Name, mimeType)
			continue
		}

		// Resolve MIME from Content-Type header if not set.
		if mimeType == "" {
			mimeType = resp.Header.Get("Content-Type")
		}

		switch {
		case strings.HasPrefix(mimeType, "image/"):
			images = append(images, ImageData{
				MediaType:  mimeType,
				DataBase64: base64.StdEncoding.EncodeToString(content),
			})
		case isTextMIME(mimeType):
			text := content
			if len(text) > maxTextBytes {
				text = text[:maxTextBytes]
			}
			extraText += fmt.Sprintf("\n--- %s ---\n%s", f.Name, string(text))
		default:
			extraText += fmt.Sprintf("\n[Attachment: %s (%s, %d bytes)]", f.Name, mimeType, len(content))
		}
	}
	return
}

// telegramFileInfo carries the fields needed to download a Telegram file attachment.
type telegramFileInfo struct {
	FileID   string
	Name     string
	MimeType string
}

// processTelegramFiles downloads each Telegram file via the backend proxy and categorises it.
// Telegram file_ids can only be resolved by the backend (which holds the bot token).
func (d *daemonClient) processTelegramFiles(ctx context.Context, files []telegramFileInfo) (images []ImageData, extraText string) {
	for _, f := range files {
		if f.FileID == "" {
			continue
		}
		content, mimeType, err := d.downloadTelegramFileContent(ctx, f.FileID, f.MimeType)
		if err != nil {
			log.Printf("[gui-daemon] failed to download Telegram file %s: %v", f.FileID, err)
			name := f.Name
			if name == "" {
				name = f.FileID
			}
			extraText += fmt.Sprintf("\n[Attachment: %s — download failed]", name)
			continue
		}
		switch {
		case strings.HasPrefix(mimeType, "image/"):
			images = append(images, ImageData{
				MediaType:  mimeType,
				DataBase64: base64.StdEncoding.EncodeToString(content),
			})
		case isTextMIME(mimeType):
			const maxTextBytes = 50 * 1024
			truncated := ""
			text := content
			if len(content) > maxTextBytes {
				text = content[:maxTextBytes]
				truncated = " (truncated)"
			}
			fence := "```"
			for bytes.Contains(text, []byte(fence)) {
				fence += "`"
			}
			name := f.Name
			if name == "" {
				name = f.FileID
			}
			extraText += fmt.Sprintf("\n[File: %s (%s)%s]\n%s\n%s\n%s", name, mimeType, truncated, fence, string(text), fence)
		default:
			name := f.Name
			if name == "" {
				name = f.FileID
			}
			extraText += fmt.Sprintf("\n[Attachment: %s (%s, %d bytes)]", name, mimeType, len(content))
		}
	}
	return
}

// downloadTelegramFileContent resolves a Telegram file_id and downloads its content
// via the backend proxy endpoint, which holds the bot token.
func (d *daemonClient) downloadTelegramFileContent(ctx context.Context, fileID, fallbackMIME string) ([]byte, string, error) {
	return nil, "", errNoYansuAccount
}

func (d *daemonClient) sendJSON(v any) {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	if d.conn == nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	if err := d.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("[gui-daemon] send failed: %v", err)
	}
}
