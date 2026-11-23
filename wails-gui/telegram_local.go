package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"yansu-agent/agent"

	"wails-gui/internal/destinations"
)

// Telegram local integration: a user-built bot (token from @BotFather) talking
// directly to api.telegram.org via long-polling without a backend relay.
// Mirrors the feishu_local.go receiver shape. Pure net/http,
// no third-party dependency (the Telegram Bot API is plain HTTPS GET/POST).

const telegramAPIBase = "https://api.telegram.org"

type telegramLocalCredentials struct {
	BotToken    string `json:"bot_token"`
	BotID       int64  `json:"bot_id,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
	ConnectedAt string `json:"connected_at,omitempty"`
	// OwnerUserID is the Telegram account allowed to talk to the bot: the
	// one that sent PairingCode in a private chat. OwnerPaired marks an owner
	// set that way; an owner saved by an older build, which took the first
	// sender, is dropped and has to pair again.
	OwnerUserID   int64  `json:"owner_user_id,omitempty"`
	OwnerUserName string `json:"owner_user_name,omitempty"`
	OwnerPaired   bool   `json:"owner_paired,omitempty"`
	// PairingCode is shown in the app until the owner sends it to the bot.
	PairingCode string `json:"pairing_code,omitempty"`
}

// telegramPairingAlphabet leaves out letters and digits that read alike.
const telegramPairingAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// newTelegramPairingCode returns a random code such as "K7QF-M2XP" (40 bits).
func newTelegramPairingCode() (string, error) {
	return newConnectorPairingCode()
}

// normalizePairingText reads a pairing message: the code itself, or the
// "/start CODE" a t.me link with a start parameter sends.
func normalizePairingText(text string) string {
	text = strings.TrimSpace(text)
	if fields := strings.Fields(text); len(fields) == 2 && strings.HasPrefix(fields[0], "/start") {
		text = fields[1]
	}
	text = strings.ToUpper(text)
	return strings.NewReplacer("-", "", " ", "").Replace(text)
}

// ensureTelegramPairing drops an owner that was not set by pairing and gives
// an unpaired bot a pairing code, saving the change. It reports whether creds
// changed.
func ensureTelegramPairing(creds *telegramLocalCredentials) (bool, error) {
	changed := false
	if creds.OwnerUserID != 0 && !creds.OwnerPaired {
		log.Printf("[telegram-local] dropping bot owner %d, set before pairing codes; the owner pairs again", creds.OwnerUserID)
		creds.OwnerUserID, creds.OwnerUserName = 0, ""
		changed = true
	}
	if creds.OwnerUserID == 0 && creds.PairingCode == "" {
		code, err := newTelegramPairingCode()
		if err != nil {
			return false, err
		}
		creds.PairingCode = code
		changed = true
	}
	if creds.OwnerUserID != 0 && creds.PairingCode != "" {
		creds.PairingCode = ""
		changed = true
	}
	if changed {
		return true, saveTelegramLocalCredentials(creds)
	}
	return false, nil
}

func telegramLocalStateDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "telegram"), nil
}

func saveTelegramLocalCredentials(creds *telegramLocalCredentials) error {
	if creds == nil || strings.TrimSpace(creds.BotToken) == "" {
		return fmt.Errorf("nil or empty Telegram credentials")
	}
	dir, err := telegramLocalStateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0600)
}

func loadTelegramLocalCredentials() *telegramLocalCredentials {
	creds, _ := readTelegramLocalCredentials()
	return creds
}

func readTelegramLocalCredentials() (*telegramLocalCredentials, error) {
	creds, err := readLocalBotCredentials[telegramLocalCredentials](telegramLocalStateDir())
	if err != nil || creds == nil {
		return nil, err
	}
	if strings.TrimSpace(creds.BotToken) == "" {
		return nil, nil
	}
	return creds, nil
}

func removeTelegramLocalCredentials() {
	dir, err := telegramLocalStateDir()
	if err != nil {
		log.Printf("[telegram-local] state directory unavailable: %v", err)
		return
	}
	_ = os.Remove(filepath.Join(dir, "credentials.json"))
	_ = os.Remove(filepath.Join(dir, "offset"))
}

func loadTelegramOffset() int64 {
	dir, err := telegramLocalStateDir()
	if err != nil {
		log.Printf("[telegram-local] state directory unavailable: %v", err)
		return 0
	}
	data, err := os.ReadFile(filepath.Join(dir, "offset"))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n
}

func saveTelegramOffset(offset int64) {
	dir, err := telegramLocalStateDir()
	if err != nil {
		log.Printf("[telegram-local] state directory unavailable: %v", err)
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "offset"), []byte(strconv.FormatInt(offset, 10)), 0600)
}

// ── Telegram Bot API client (raw net/http) ──

type telegramBotClient struct {
	token  string
	http   *http.Client
	apiURL string
}

//isoform:destination chat_bridges
func newTelegramBotClient(token string, pollTimeoutSec int) *telegramBotClient {
	// HTTP timeout must exceed the long-poll timeout or getUpdates is cut short.
	return &telegramBotClient{
		token:  token,
		apiURL: telegramAPIBase + "/bot" + token,
		http:   &http.Client{Timeout: time.Duration(pollTimeoutSec+15) * time.Second},
	}
}

type telegramAPIResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

//isoform:destination chat_bridges
func (c *telegramBotClient) call(ctx context.Context, method string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/"+method, reqBody)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == "sendMessage" {
		req = destinations.CountRequest(req, destinations.ChatBridges)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var api telegramAPIResponse
	if err := json.Unmarshal(data, &api); err != nil {
		return fmt.Errorf("telegram %s: bad response: %w", method, err)
	}
	if !api.OK {
		return fmt.Errorf("telegram %s: %s", method, api.Description)
	}
	if out != nil && len(api.Result) > 0 {
		return json.Unmarshal(api.Result, out)
	}
	return nil
}

type telegramUser struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
	First    string `json:"first_name"`
}

func (c *telegramBotClient) getMe(ctx context.Context) (*telegramUser, error) {
	var u telegramUser
	if err := c.call(ctx, "getMe", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // "private", "group", "supergroup", "channel"
}

type telegramMessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type telegramMessage struct {
	MessageID int64                   `json:"message_id"`
	Date      int64                   `json:"date"`
	From      *telegramUser           `json:"from"`
	Chat      telegramChat            `json:"chat"`
	Text      string                  `json:"text"`
	Caption   string                  `json:"caption"`
	Entities  []telegramMessageEntity `json:"entities"`
	ReplyTo   *telegramMessage        `json:"reply_to_message"`
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

func (c *telegramBotClient) getUpdates(ctx context.Context, offset int64, timeoutSec int) ([]telegramUpdate, error) {
	var updates []telegramUpdate
	body := map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message"},
	}
	if err := c.call(ctx, "getUpdates", body, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *telegramBotClient) sendMessage(ctx context.Context, chatID, replyToMessageID int64, text string) error {
	body := map[string]any{
		"chat_id": chatID,
		"text":    truncateTelegramText(text),
	}
	if replyToMessageID != 0 {
		body["reply_parameters"] = map[string]any{"message_id": replyToMessageID, "allow_sending_without_reply": true}
	}
	return c.call(ctx, "sendMessage", body, nil)
}

// truncateTelegramText keeps messages under Telegram's 4096-char limit.
func truncateTelegramText(s string) string {
	const limit = 4096
	if len(s) <= limit {
		return s
	}
	return s[:limit-1] + "…"
}

// ── Receiver ──

type telegramLocalReceiver struct {
	app    *App
	creds  telegramLocalCredentials
	client *telegramBotClient
	cancel context.CancelFunc
	done   chan struct{}

	threadMu    sync.Mutex
	threadLocks map[string]*threadLockEntry

	// ownerMu guards creds.OwnerUserID and creds.PairingCode, which the
	// pairing message changes.
	ownerMu sync.Mutex
	// connectedAt is when the bot was connected; older messages (queued
	// before it, replayed from a saved offset) are ignored.
	connectedAt time.Time
	// hinted holds the senders already told how to pair.
	hinted map[int64]bool
}

func newTelegramLocalReceiver(app *App, creds telegramLocalCredentials) *telegramLocalReceiver {
	connectedAt, _ := time.Parse(time.RFC3339, creds.ConnectedAt)
	return &telegramLocalReceiver{
		app:         app,
		creds:       creds,
		client:      newTelegramBotClient(creds.BotToken, 30),
		done:        make(chan struct{}),
		threadLocks: make(map[string]*threadLockEntry),
		connectedAt: connectedAt,
		hinted:      map[int64]bool{},
	}
}

func (r *telegramLocalReceiver) acquireThreadLock(threadKey string) func() {
	r.threadMu.Lock()
	now := time.Now()
	for k, entry := range r.threadLocks {
		if entry.refCount == 0 && now.Sub(entry.lastUsed) > threadLockEvictAfter {
			delete(r.threadLocks, k)
		}
	}
	entry, ok := r.threadLocks[threadKey]
	if !ok {
		entry = &threadLockEntry{}
		r.threadLocks[threadKey] = entry
	}
	entry.refCount++
	entry.lastUsed = now
	r.threadMu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		r.threadMu.Lock()
		entry.refCount--
		entry.lastUsed = time.Now()
		r.threadMu.Unlock()
	}
}

func (r *telegramLocalReceiver) run() {
	defer close(r.done)
	// Clear the App slot on exit, mirroring the Slack/Discord receivers. The
	// long-poll loop only returns on ctx cancel today (callers already nil the
	// field), but this keeps the invariant if a future change lets run() return
	// on its own, so GetTelegramStatus never reports "online" for a dead
	// receiver. Guarded by identity so a newer receiver is never cleared.
	defer func() {
		r.app.mu.Lock()
		if r.app.telegramLocal == r {
			r.app.telegramLocal = nil
		}
		r.app.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	defer cancel()

	const pollTimeout = 30
	offset := loadTelegramOffset()
	backoff := 2 * time.Second
	const maxBackoff = 30 * time.Second
	log.Printf("[telegram-local] receiver starting for bot @%s", r.creds.BotUsername)

	for ctx.Err() == nil {
		updates, err := r.client.getUpdates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[telegram-local] getUpdates error: %v (retry in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff = time.Duration(float64(backoff) * 1.8)
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		backoff = 2 * time.Second
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
				saveTelegramOffset(offset)
			}
			if u.Message != nil {
				go r.handleMessage(ctx, *u.Message)
			}
		}
	}
}

func (r *telegramLocalReceiver) stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

func (r *telegramLocalReceiver) handleMessage(ctx context.Context, msg telegramMessage) {
	if msg.From == nil || msg.From.IsBot {
		return
	}
	if !r.connectedAt.IsZero() && msg.Date != 0 && time.Unix(msg.Date, 0).Before(r.connectedAt.Truncate(time.Second)) {
		log.Printf("[telegram-local] ignored message %d from before the bot was connected", msg.MessageID)
		return
	}
	if !r.senderAllowed(ctx, msg) {
		return
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}
	if text == "" {
		return
	}
	isGroup := msg.Chat.Type == "group" || msg.Chat.Type == "supergroup"
	if isGroup && !r.isBotAddressed(msg) {
		return
	}
	text = r.stripBotMention(text)
	if strings.TrimSpace(text) == "" {
		return
	}

	threadKey := "telegram:" + strconv.FormatInt(r.creds.BotID, 10) + ":" + strconv.FormatInt(msg.Chat.ID, 10) + ":" + strconv.FormatInt(msg.From.ID, 10)
	unlock := r.acquireThreadLock(threadKey)
	defer unlock()

	reply := r.runAgent(ctx, threadKey, text, msg.Chat.Type)
	if strings.TrimSpace(reply) == "" {
		return
	}
	if err := r.app.authorizeInboundConnectorReply(ctx, "telegram", threadKey, threadKey); err != nil {
		log.Printf("[telegram-local] reply authority denied: %v", err)
		return
	}
	if err := r.client.sendMessage(ctx, msg.Chat.ID, msg.MessageID, reply); err != nil {
		log.Printf("[telegram-local] send reply failed: %v", err)
	}
}

// senderAllowed accepts messages only from the bot's owner. A user-built bot
// is reachable by anyone who finds its name, and every message starts an
// unattended agent run, so strangers are refused. The owner is the account
// that sends the pairing code shown in the app, in a private chat; the owner
// is saved before any message of theirs is answered, and the pairing message
// itself only confirms the pairing.
func (r *telegramLocalReceiver) senderAllowed(ctx context.Context, msg telegramMessage) bool {
	r.ownerMu.Lock()
	if r.creds.OwnerUserID != 0 {
		owner := r.creds.OwnerUserID
		r.ownerMu.Unlock()
		if msg.From.ID != owner {
			log.Printf("[telegram-local] refused a message from user %d (@%s) in chat %d: only the paired owner, user %d, may use this bot",
				msg.From.ID, msg.From.Username, msg.Chat.ID, owner)
			return false
		}
		return true
	}
	if msg.Chat.Type != "private" {
		r.ownerMu.Unlock()
		log.Printf("[telegram-local] ignored a %s message from user %d: the bot is not paired yet", msg.Chat.Type, msg.From.ID)
		return false
	}
	code := normalizePairingText(r.creds.PairingCode)
	if code == "" || normalizePairingText(msg.Text) != code {
		hint := !r.hinted[msg.From.ID]
		r.hinted[msg.From.ID] = true
		r.ownerMu.Unlock()
		log.Printf("[telegram-local] ignored a message from user %d: the bot is not paired and the message is not the pairing code", msg.From.ID)
		if hint {
			r.reply(ctx, msg, "This bot is not paired yet. Send the pairing code shown in Isoform's Telegram settings.")
		}
		return false
	}
	paired := r.creds
	paired.OwnerUserID = msg.From.ID
	paired.OwnerUserName = msg.From.Username
	paired.OwnerPaired = true
	paired.PairingCode = ""
	if saved := loadTelegramLocalCredentials(); saved == nil || saved.BotToken != r.creds.BotToken {
		r.ownerMu.Unlock()
		log.Printf("[telegram-local] pairing refused: the saved credentials are for another bot")
		return false
	}
	if err := saveTelegramLocalCredentials(&paired); err != nil {
		r.ownerMu.Unlock()
		log.Printf("[telegram-local] could not save the paired owner: %v", err)
		r.reply(ctx, msg, "Isoform could not save the pairing. Try again.")
		return false
	}
	r.creds = paired
	r.ownerMu.Unlock()
	log.Printf("[telegram-local] paired with user %d (@%s)", msg.From.ID, msg.From.Username)
	r.reply(ctx, msg, "Paired with Isoform. Only you can use this bot now.")
	return false
}

// reply sends a short notice to the chat a message came from.
func (r *telegramLocalReceiver) reply(ctx context.Context, msg telegramMessage, text string) {
	if err := r.client.sendMessage(ctx, msg.Chat.ID, msg.MessageID, text); err != nil {
		log.Printf("[telegram-local] send notice failed: %v", err)
	}
}

// isBotAddressed reports whether a group message targets this bot — either an
// @username mention or a reply to one of the bot's own messages.
func (r *telegramLocalReceiver) isBotAddressed(msg telegramMessage) bool {
	if msg.ReplyTo != nil && msg.ReplyTo.From != nil && msg.ReplyTo.From.ID == r.creds.BotID {
		return true
	}
	if r.creds.BotUsername == "" {
		return false
	}
	needle := "@" + strings.ToLower(r.creds.BotUsername)
	return strings.Contains(strings.ToLower(msg.Text+" "+msg.Caption), needle)
}

func (r *telegramLocalReceiver) stripBotMention(text string) string {
	if r.creds.BotUsername == "" {
		return text
	}
	return strings.TrimSpace(strings.ReplaceAll(text, "@"+r.creds.BotUsername, ""))
}

func (r *telegramLocalReceiver) runAgent(ctx context.Context, threadKey, text, chatType string) string {
	r.ownerMu.Lock()
	paired := r.creds.OwnerPaired && r.creds.OwnerUserID != 0
	r.ownerMu.Unlock()
	personal := connectorPersonalContext(paired, chatType)
	threadKey = connectorContextThreadKey(threadKey, personal)
	r.app.mu.Lock()
	runner := r.app.runner
	r.app.mu.Unlock()
	if runner == nil {
		return "Agent is not ready. Please try again later."
	}
	taskCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	traceID := uuid.NewString()
	if r.app.telemetry != nil {
		r.app.telemetry.Capture("agentloop.connector.message_received", map[string]any{
			"trace_id":  traceID,
			"connector": "telegram",
			"transport": "local",
			"chat_type": chatType,
		})
	}
	r.app.mu.Lock()
	r.app.pendingTraceIDs[threadKey] = traceID
	r.app.mu.Unlock()
	defer schedulePendingTraceIDCleanup(r.app, threadKey, traceID)

	input := &agent.TaskInput{
		Prompt:           r.app.composePromptFor(text, personal, "", "telegram"),
		Model:            r.app.chatTaskModel(),
		WorkDir:          r.app.getOrCreateSessionTempDir(threadKey),
		TraceID:          traceID,
		AutoApproveTools: true,
		// Inbound connector messages come from people other than the user
		// and nobody reviews the run: never publish anything on their behalf.
		DenyExternalWrites: true,
	}
	// Read-only reach: its own folder, web search and the research opencli
	// calls (connector_authority.go).
	input.AuthorityGrants = connectorRunGrants(input.WorkDir)
	if r.app.store != nil {
		input.SessionID, _ = r.app.store.GetUIState(taskCtx, "daemon:session:"+threadKey)
		historyItems := loadConversationHistory(r.app.store, threadKey)
		input.History = renderConversationItems(historyItems)
	}
	refusals := &connectorRefusals{}
	var newSessionID, lastContent string
	_, err := runner.ExecuteTask(taskCtx, input, func(event *agent.StreamEvent) error {
		if event.SessionID != "" {
			newSessionID = event.SessionID
		}
		refusals.observe(event)
		switch event.Type {
		case "text_delta", "assistant":
			lastContent += event.Content
		}
		return nil
	})
	if newSessionID != "" && r.app.store != nil {
		_ = r.app.store.SetUIState(context.Background(), "daemon:session:"+threadKey, newSessionID)
	}
	if lastContent != "" {
		summary, roundCount := appendAndSummarizeConversation(r.app.store, threadKey, text, lastContent)
		r.app.triggerMemory(memorySourceTelegram, threadKey, summary, roundCount)
		return withRefusalNote(lastContent, refusals, "Telegram")
	}
	if note := refusals.note("Telegram"); note != "" {
		return note
	}
	if err != nil {
		log.Printf("[telegram-local] agent error: %v", err)
		return "Sorry, I encountered an error processing your message."
	}
	return ""
}

// ── App-level local control (called by telegram.go branches) ──

// startTelegramLocal validates nothing (creds already validated on save) and
// launches the local receiver, replacing any existing one.
func (a *App) startTelegramLocal() error {
	creds := loadTelegramLocalCredentials()
	if creds == nil {
		return fmt.Errorf("no local Telegram credentials")
	}
	if _, err := ensureTelegramPairing(creds); err != nil {
		return fmt.Errorf("prepare Telegram pairing: %w", err)
	}
	a.mu.Lock()
	existing := a.telegramLocal
	r := newTelegramLocalReceiver(a, *creds)
	a.telegramLocal = r
	a.mu.Unlock()
	if existing != nil {
		existing.stop()
	}
	go r.run()
	log.Printf("[telegram-local] started for bot @%s (%d)", creds.BotUsername, creds.BotID)
	return nil
}

func (a *App) stopTelegramLocal() {
	a.mu.Lock()
	r := a.telegramLocal
	a.telegramLocal = nil
	a.mu.Unlock()
	if r != nil {
		r.stop()
	}
}

func (a *App) tryAutoStartTelegramLocal() {
	if loadTelegramLocalCredentials() == nil {
		return
	}
	if err := a.startTelegramLocal(); err != nil {
		log.Printf("[telegram-local] auto-start failed: %v", err)
	}
}

// TelegramTestResult is returned by the connection-test binding.
type TelegramTestResult struct {
	Success     bool   `json:"success"`
	BotID       int64  `json:"bot_id,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
	Error       string `json:"error,omitempty"`
}

// TestTelegramConnection validates a bot token via getMe without persisting it.
// Used by the setup wizard's "Test connection" step.
func (a *App) TestTelegramConnection(botToken string) TelegramTestResult {
	botToken = strings.TrimSpace(botToken)
	if botToken == "" {
		return TelegramTestResult{Error: "bot token is required"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	me, err := newTelegramBotClient(botToken, 0).getMe(ctx)
	if err != nil {
		return TelegramTestResult{Error: err.Error()}
	}
	return TelegramTestResult{Success: true, BotID: me.ID, BotUsername: me.Username}
}

// SaveTelegramLocalCredentials validates the token (getMe), persists it, and
// starts the local receiver. The chat app setup calls this.
func (a *App) SaveTelegramLocalCredentials(botToken string) error {
	dir, err := telegramLocalStateDir()
	if err != nil {
		return err
	}
	botToken = strings.TrimSpace(botToken)
	if botToken == "" {
		return fmt.Errorf("bot token is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	me, err := newTelegramBotClient(botToken, 0).getMe(ctx)
	if err != nil {
		return fmt.Errorf("connection test failed — not saved: %w", err)
	}
	creds := &telegramLocalCredentials{
		BotToken:    botToken,
		BotID:       me.ID,
		BotUsername: me.Username,
		ConnectedAt: time.Now().Format(time.RFC3339),
	}
	// Reconnecting the same bot keeps an owner who paired with it; anything
	// else pairs again with a new code.
	for _, previous := range []*telegramLocalCredentials{loadTelegramLocalCredentials(), archivedTelegramLocalCredentials()} {
		if previous != nil && previous.BotID == me.ID && previous.OwnerPaired && previous.OwnerUserID != 0 {
			creds.OwnerUserID, creds.OwnerUserName, creds.OwnerPaired = previous.OwnerUserID, previous.OwnerUserName, true
			break
		}
	}
	if creds.OwnerUserID == 0 {
		code, err := newTelegramPairingCode()
		if err != nil {
			return fmt.Errorf("create a pairing code: %w", err)
		}
		creds.PairingCode = code
	}
	if err := saveTelegramLocalCredentials(creds); err != nil {
		return err
	}
	if err := a.bindIntegrationSelfIdentity(context.Background(), "telegram", strconv.FormatInt(me.ID, 10), me.Username); err != nil {
		log.Printf("[telegram-local] bind self identity failed: %v", err)
	}
	// A fresh connect supersedes any archived (reusable) credentials.
	removeArchivedLocalCreds(dir)
	return a.startTelegramLocal()
}

// telegramLocalStatus builds the status struct from local creds + receiver state.
func (a *App) telegramLocalStatus() TelegramStatus {
	creds := loadTelegramLocalCredentials()
	if creds == nil {
		return TelegramStatus{}
	}
	a.mu.Lock()
	online := a.telegramLocal != nil
	a.mu.Unlock()
	return TelegramStatus{
		Connected:        true,
		TelegramUserID:   creds.BotID,
		TelegramUserName: creds.BotUsername,
		DaemonOnline:     online,
		Paired:           creds.OwnerPaired && creds.OwnerUserID != 0,
		PairingCode:      creds.PairingCode,
	}
}

// archivedTelegramLocalCredentials returns the archived (reusable) bot, or nil.
func archivedTelegramLocalCredentials() *telegramLocalCredentials {
	dir, err := telegramLocalStateDir()
	if err != nil {
		log.Printf("[telegram-local] state directory unavailable: %v", err)
		return nil
	}
	data := readArchivedLocalCreds(dir)
	if data == nil {
		return nil
	}
	var creds telegramLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil || strings.TrimSpace(creds.BotToken) == "" {
		return nil
	}
	return &creds
}

// disconnectTelegramLocal stops the receiver and clears local credentials. A
// copy is archived first so the user can reuse the same bot on a future
// reconnect instead of re-entering the token (mirrors Feishu).
func (a *App) disconnectTelegramLocal() error {
	dir, err := telegramLocalStateDir()
	if err != nil {
		return err
	}
	a.stopTelegramLocal()
	archiveLocalCreds(dir)
	removeTelegramLocalCredentials()
	return nil
}

// GetTelegramArchived reports whether a reusable (archived) Telegram bot exists.
// Wails binding consumed by the chat app setup reuse card.
func (a *App) GetTelegramArchived() ArchivedConnectorInfo {
	dir, err := telegramLocalStateDir()
	if err != nil {
		log.Printf("[telegram-local] state directory unavailable: %v", err)
		return ArchivedConnectorInfo{}
	}
	data := readArchivedLocalCreds(dir)
	if data == nil {
		return ArchivedConnectorInfo{}
	}
	var creds telegramLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil || strings.TrimSpace(creds.BotToken) == "" {
		return ArchivedConnectorInfo{}
	}
	label := strings.TrimSpace(creds.BotUsername)
	if label != "" {
		label = "@" + label
	}
	return ArchivedConnectorInfo{Has: true, Label: label}
}

// ReuseTelegramLocal restores the archived bot token, re-validates it, and
// reconnects — no need to paste the token again. Wails binding.
func (a *App) ReuseTelegramLocal() error {
	dir, err := telegramLocalStateDir()
	if err != nil {
		return err
	}
	data := readArchivedLocalCreds(dir)
	if data == nil {
		return fmt.Errorf("no archived Telegram credentials to reuse")
	}
	var creds telegramLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return fmt.Errorf("archived credentials corrupt: %w", err)
	}
	if strings.TrimSpace(creds.BotToken) == "" {
		return fmt.Errorf("archived credentials missing bot token")
	}
	// SaveTelegramLocalCredentials re-validates via getMe, persists, starts, and
	// clears the archived copy on success.
	return a.SaveTelegramLocalCredentials(creds.BotToken)
}

// DiscardTelegramArchived drops the archived bot so the user starts fresh
// ("Create new" in the reuse card). Wails binding.
func (a *App) DiscardTelegramArchived() {
	dir, err := telegramLocalStateDir()
	if err != nil {
		log.Printf("[telegram-local] state directory unavailable: %v", err)
		return
	}
	removeArchivedLocalCreds(dir)
}
