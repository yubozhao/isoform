package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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

var errReplaced = errors.New("replaced by another daemon")

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
	defer close(d.done)

	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.cancel = cancel
	d.mu.Unlock()
	defer cancel()

	backoff := 2 * time.Second

	for {
		connectStart := time.Now()
		err := d.connectAndListen(ctx)
		d.setConnected(false)

		if ctx.Err() != nil {
			return // clean shutdown
		}

		if errors.Is(err, errReplaced) {
			log.Printf("[gui-daemon] replaced by another daemon, dormant 30s")
			select {
			case <-time.After(30 * time.Second):
			case <-ctx.Done():
				return
			}
			backoff = 2 * time.Second
			continue
		}

		// Reset backoff if the connection was stable for a while — this means
		// the backend restarted (not that it was never reachable), so we should
		// reconnect quickly rather than waiting out a grown backoff.
		if time.Since(connectStart) > 30*time.Second {
			backoff = 2 * time.Second
		}

		log.Printf("[gui-daemon] disconnected: %v. Reconnecting in %v...", err, backoff)
		select {
		case <-time.After(backoff):
			if backoff*2 < 5*time.Minute {
				backoff *= 2
			} else {
				backoff = 5 * time.Minute
			}
		case <-ctx.Done():
			return
		}
	}
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

//isoform:destination yansu_backend
func (d *daemonClient) connectAndListen(ctx context.Context) error {
	// No Yansu account: the daemon relays IM messages through the Yansu backend, so it
	// must never connect. Fail closed at the socket layer (defense in depth — IM
	// auto-start and the connect UI are already gated upstream).
	if noYansuAccount() {
		return fmt.Errorf("daemon disabled: no Isoform account")
	}
	wsURL, ok := buildDaemonWSURL(d.backendURL)
	if !ok {
		return fmt.Errorf("invalid backend URL for WebSocket: %s", d.backendURL)
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+d.getToken())

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	d.sendMu.Lock()
	d.conn = conn
	d.sendMu.Unlock()
	defer func() {
		conn.Close()
		d.sendMu.Lock()
		d.conn = nil
		d.sendMu.Unlock()
	}()

	// Register with DaemonHub (platform-aware fields).
	regFields := map[string]string{
		"type":        "register",
		"client_type": "gui",
		"platform":    d.platform,
	}
	switch d.platform {
	case "feishu":
		regFields["feishu_app_id"] = d.teamID
		regFields["feishu_open_id"] = d.userID
	case "discord":
		regFields["discord_user_id"] = d.userID
		// Discord routing uses discord_user_id only (no team_id equivalent)
	case "telegram":
		regFields["telegram_user_id"] = d.userID // telegram user ID (teamID is unused for Telegram)
	default: // "slack"
		regFields["team_id"] = d.teamID
		regFields["slack_user_id"] = d.userID
	}
	regMsg, err := json.Marshal(regFields)
	if err != nil {
		return fmt.Errorf("marshal register: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, regMsg); err != nil {
		return fmt.Errorf("send register: %w", err)
	}

	// Read registration ack.
	_, ackRaw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read register ack: %w", err)
	}
	var ack struct {
		Type   string `json:"type"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(ackRaw, &ack); err != nil {
		return fmt.Errorf("parse register ack: %w", err)
	}
	if ack.Status != "ok" {
		return fmt.Errorf("registration rejected: %s", ack.Error)
	}

	log.Printf("[gui-daemon:%s] connected as %s:%s", d.platform, d.teamID, d.userID)
	d.setConnected(true)

	// Ping handler to reset read deadline.
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		d.sendMu.Lock()
		defer d.sendMu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})

	// Close conn on ctx cancel to unblock ReadMessage.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stopWatch:
		}
	}()

	// Heartbeat goroutine.
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go d.heartbeat(heartbeatCtx)

	// Read loop.
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		var base struct {
			Type    string          `json:"type"`
			RelayID string          `json:"relay_id"`
			Event   json.RawMessage `json:"event"`
		}
		if err := json.Unmarshal(msg, &base); err != nil {
			continue
		}

		switch base.Type {
		case "event":
			var evt struct {
				UserID      string `json:"user_id"`
				ChannelID   string `json:"channel_id"`
				ThreadTS    string `json:"thread_ts"`
				ThreadID    string `json:"thread_id"` // Discord sends thread_id instead of thread_ts
				Text        string `json:"text"`
				EventTS     string `json:"event_ts"`
				ChatType    string `json:"chat_type"`
				Platform    string `json:"platform"`
				EventType   string `json:"event_type"`   // "message", "app_mention" (Slack), "passive_message" (Telegram)
				UserName    string `json:"user_name"`    // Discord passive payload: pre-resolved display name
				ChannelName string `json:"channel_name"` // Discord passive payload: pre-resolved channel name
				// Telegram passive event fields
				MessageThreadID string `json:"message_thread_id"`   // Topics supergroup topic ID
				ReplyToMsgID    string `json:"reply_to_message_id"` // reply parent message ID
				SenderName      string `json:"sender_name"`         // sender display name
				// Feishu-specific fields
				AppID     string `json:"app_id"`
				TenantKey string `json:"tenant_key"`
				// Files carries Slack, Feishu, Discord, and Telegram file metadata (fields are platform-specific).
				Files []struct {
					// Slack fields
					ID                 string `json:"id"`
					Name               string `json:"name"`
					MimeType           string `json:"mimetype"`  // Slack
					DiscordMimeType    string `json:"mime_type"` // Discord
					URLPrivate         string `json:"url_private"`
					URLPrivateDownload string `json:"url_private_download"`
					Size               int    `json:"size"`
					Thumb360           string `json:"thumb_360"`
					// Feishu fields
					ImageKey  string `json:"image_key"`
					MessageID string `json:"message_id"`
					Type      string `json:"type"`
					// Telegram file ID and Discord attachment URL
					FileID string `json:"file_id"`
					URL    string `json:"url"`
				} `json:"files"`
			}
			if err := json.Unmarshal(base.Event, &evt); err != nil {
				continue
			}

			// Discord sends "thread_id" instead of "thread_ts"; normalize so
			// downstream code (externalConversationKey, handleSlackEvent) works uniformly.
			if evt.ThreadTS == "" && evt.ThreadID != "" {
				evt.ThreadTS = evt.ThreadID
			}

			relayID := base.RelayID

			// Slack channel/group messages (event_type "message", non-DM): silently
			// ignore. We intentionally skip event_ack for these so the backend does
			// NOT call SetThreadStatus("is thinking...") — the user should never see a
			// processing indicator for silent channel events. The backend's ack timer
			// is cancelled by stream_end (see CancelAckTimer in daemonHandleStreamEnd).
			if d.platform == "slack" && evt.EventType == "message" && evt.ChatType != "p2p" {
				// Send stream_end to clean up backend relay context and cancel ack timer.
				d.sendJSON(map[string]string{
					"type":     "stream_end",
					"relay_id": relayID,
				})
				continue
			}

			// Discord channel messages (passive): silently ignore. Same pattern as
			// Slack above — skip event_ack, send stream_end.
			if d.platform == "discord" && evt.EventType == "message" && evt.ChatType == "channel" {
				d.sendJSON(map[string]string{
					"type":     "stream_end",
					"relay_id": relayID,
				})
				continue
			}

			// Telegram group messages (passive): record for the Furnace scanner.
			// Unlike Slack/Discord/Feishu (which fetch history via REST API),
			// Telegram Bot API has no history endpoint, so passive messages
			// must be recorded here.
			if d.platform == "telegram" && evt.EventType == "passive_message" {
				// Record for Furnace scan — Telegram has no history API,
				// so real-time events are the only message source.
				d.app.appendTelegramMessage(TelegramRawMessage{
					ChatID:   evt.ChannelID,
					ChatType: evt.ChatType,
					SenderID: evt.UserID,
					Text:     evt.Text,
					EventTS:  evt.EventTS,
				})
				if d.app.telegramScanner != nil {
					d.app.telegramScanner.Trigger()
				}
				d.sendJSON(map[string]string{
					"type":     "stream_end",
					"relay_id": relayID,
				})
				continue
			}

			// Reject interactive non-owner messages before downloading attachments,
			// composing personal context, or announcing that a run has started.
			if !d.senderAllowed(evt.UserID) {
				d.sendJSON(map[string]string{"type": "stream_end", "relay_id": relayID})
				continue
			}

			// Ack immediately for interactive events (@mention, DM, etc.).
			// This cancels the backend ack timer and sets "is thinking..." status.
			d.sendJSON(map[string]string{
				"type":     "event_ack",
				"relay_id": relayID,
			})

			// Process in parallel, capped by interactiveSem so bursts don't
			// fan out into dozens of concurrent LLM sessions.
			go func(evt struct {
				UserID      string `json:"user_id"`
				ChannelID   string `json:"channel_id"`
				ThreadTS    string `json:"thread_ts"`
				ThreadID    string `json:"thread_id"`
				Text        string `json:"text"`
				EventTS     string `json:"event_ts"`
				ChatType    string `json:"chat_type"`
				Platform    string `json:"platform"`
				EventType   string `json:"event_type"`
				UserName    string `json:"user_name"`
				ChannelName string `json:"channel_name"`
				// Telegram passive event fields
				MessageThreadID string `json:"message_thread_id"`
				ReplyToMsgID    string `json:"reply_to_message_id"`
				SenderName      string `json:"sender_name"`
				AppID           string `json:"app_id"`
				TenantKey       string `json:"tenant_key"`
				Files           []struct {
					ID                 string `json:"id"`
					Name               string `json:"name"`
					MimeType           string `json:"mimetype"`  // Slack
					DiscordMimeType    string `json:"mime_type"` // Discord
					URLPrivate         string `json:"url_private"`
					URLPrivateDownload string `json:"url_private_download"`
					Size               int    `json:"size"`
					Thumb360           string `json:"thumb_360"`
					ImageKey           string `json:"image_key"`
					MessageID          string `json:"message_id"`
					Type               string `json:"type"`
					// Telegram file ID and Discord attachment URL
					FileID string `json:"file_id"`
					URL    string `json:"url"`
				} `json:"files"`
			}, relayID string) {
				// Cap concurrent interactive handlers. If the daemon
				// connection is torn down while we're queued, drop the
				// event rather than accumulate goroutines.
				select {
				case d.interactiveSem <- struct{}{}:
					defer func() { <-d.interactiveSem }()
				case <-ctx.Done():
					return
				}

				text := evt.Text
				var images []ImageData
				if len(evt.Files) > 0 {
					dlCtx, dlCancel := context.WithTimeout(context.Background(), 30*time.Second)
					var extraText string
					if evt.Platform == "feishu" {
						feishuFiles := make([]feishuFileInfo, len(evt.Files))
						for i, f := range evt.Files {
							feishuFiles[i] = feishuFileInfo{
								AppID:     evt.AppID,
								TenantKey: evt.TenantKey,
								MessageID: f.MessageID,
								ImageKey:  f.ImageKey,
							}
						}
						images, extraText = d.processFeishuFiles(dlCtx, feishuFiles)
					} else if evt.Platform == "discord" {
						// Discord CDN URLs are publicly accessible (temporary signed URLs).
						// Download directly via HTTP GET — no backend proxy needed.
						images, extraText = d.processDiscordFiles(dlCtx, evt.Files)
					} else if evt.Platform == "telegram" {
						tgFiles := make([]telegramFileInfo, len(evt.Files))
						for i, f := range evt.Files {
							tgFiles[i] = telegramFileInfo{
								FileID:   f.FileID,
								Name:     f.Name,
								MimeType: f.MimeType,
							}
						}
						images, extraText = d.processTelegramFiles(dlCtx, tgFiles)
					} else {
						fileInfos := make([]slackFileInfo, len(evt.Files))
						for i, f := range evt.Files {
							dlURL := f.URLPrivateDownload
							if dlURL == "" {
								dlURL = f.URLPrivate
							}
							fileInfos[i] = slackFileInfo{ID: f.ID, Name: f.Name, MimeType: f.MimeType, URL: dlURL, Size: f.Size}
						}
						images, extraText = d.processSlackFiles(dlCtx, fileInfos)
					}
					dlCancel()
					text += extraText
				}
				// Record incoming Telegram messages for scan history.
				if d.platform == "telegram" {
					d.app.appendTelegramMessage(TelegramRawMessage{
						ChatID:   evt.ChannelID,
						ChatType: evt.ChatType,
						SenderID: evt.UserID,
						Text:     text,
						EventTS:  evt.EventTS,
					})
					if d.app.telegramScanner != nil {
						d.app.telegramScanner.Trigger()
					}
				}
				// Serialize messages within the same thread to prevent concurrent
				// session ID read/write races on the shared per-thread Claude session.
				threadKey := d.externalConversationKey(evt.ChannelID, evt.ThreadTS, evt.EventTS, evt.ChatType)
				unlock := d.acquireThreadLock(threadKey)
				defer unlock()
				d.handleSlackEvent(relayID, evt.UserID, evt.ChannelID, evt.ThreadTS, evt.EventTS, evt.ChatType, text, images)
			}(evt, relayID)

		case "replaced":
			return errReplaced
		}
	}
}

func buildDaemonWSURL(baseURL string) (string, bool) {
	// Nothing dials the Yansu daemon hub without an account — pure-function chokepoint so
	// any caller (current or future) is blocked, not just connectAndListen.
	if noYansuAccount() {
		return "", false
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", false
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/daemon/ws"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), true
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
//
//isoform:destination chat_bridges
func (d *daemonClient) downloadFeishuImageContent(ctx context.Context, appID, tenantKey, messageID, imageKey string) ([]byte, string, error) {
	reqBody, err := json.Marshal(map[string]string{
		"app_id":     appID,
		"tenant_key": tenantKey,
		"message_id": messageID,
		"image_key":  imageKey,
	})
	if err != nil {
		return nil, "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(d.backendURL, "/")+"/api/feishu/files/download",
		bytes.NewReader(reqBody))
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.getToken())

	req = destinations.CountRequest(req, destinations.ChatBridges)
	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("backend returned %d: %s", resp.StatusCode, string(body))
	}

	var fileResp struct {
		Content  []byte `json:"content"`
		MimeType string `json:"mime_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fileResp); err != nil {
		return nil, "", fmt.Errorf("decode response: %w", err)
	}
	if len(fileResp.Content) == 0 {
		return nil, "", fmt.Errorf("empty content for image %s", imageKey)
	}

	mimeType := fileResp.MimeType
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	return fileResp.Content, mimeType, nil
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
//
//isoform:destination chat_bridges
func (d *daemonClient) downloadSlackFileContent(ctx context.Context, fileID, fileURL, fallbackMIME string) ([]byte, string, error) {
	reqBody, err := json.Marshal(map[string]string{
		"team_id": d.teamID,
		"file_id": fileID,
		"url":     fileURL,
	})
	if err != nil {
		return nil, "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(d.backendURL, "/")+"/api/slack/files/download",
		bytes.NewReader(reqBody))
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.getToken())

	req = destinations.CountRequest(req, destinations.ChatBridges)
	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("backend returned %d: %s", resp.StatusCode, string(body))
	}

	var fileResp struct {
		Content  []byte `json:"content"`
		MimeType string `json:"mime_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fileResp); err != nil {
		return nil, "", fmt.Errorf("decode response: %w", err)
	}
	if len(fileResp.Content) == 0 {
		return nil, "", fmt.Errorf("empty content for file %s", fileID)
	}

	resolvedMIME := fileResp.MimeType
	if resolvedMIME == "" {
		resolvedMIME = fallbackMIME
	}
	return fileResp.Content, resolvedMIME, nil
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
//
//isoform:destination chat_bridges
func (d *daemonClient) downloadTelegramFileContent(ctx context.Context, fileID, fallbackMIME string) ([]byte, string, error) {
	reqBody, err := json.Marshal(map[string]string{
		"file_id":   fileID,
		"mime_type": fallbackMIME,
	})
	if err != nil {
		return nil, "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(d.backendURL, "/")+"/api/telegram/files/download",
		bytes.NewReader(reqBody))
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.getToken())

	req = destinations.CountRequest(req, destinations.ChatBridges)
	resp, err := httpClientWithTimeout.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("backend returned %d: %s", resp.StatusCode, string(body))
	}

	var fileResp struct {
		Content  []byte `json:"content"`
		MimeType string `json:"mime_type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fileResp); err != nil {
		return nil, "", fmt.Errorf("decode response: %w", err)
	}
	if len(fileResp.Content) == 0 {
		return nil, "", fmt.Errorf("empty content for file %s", fileID)
	}

	mimeType := fileResp.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return fileResp.Content, mimeType, nil
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
