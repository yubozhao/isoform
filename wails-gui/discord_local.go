package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/google/uuid"
	"yansu-agent/agent"

	"wails-gui/internal/destinations"
)

// Discord local integration: a user-built Discord bot (token from the Discord
// developer portal) connected directly to the Discord gateway — no Yansu
// backend relay. Mirrors telegram_local.go / slack_local.go.

// discordInvitePermissions is the permission bitfield the invite URL requests:
// View Channels, Send Messages, Read Message History, Embed Links, Attach
// Files, Add Reactions.
const discordInvitePermissions = 1024 + 2048 + 65536 + 16384 + 32768 + 64

type discordLocalCredentials struct {
	BotToken    string `json:"bot_token"`
	AppID       string `json:"app_id,omitempty"`
	BotUserID   string `json:"bot_user_id,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
	ConnectedAt string `json:"connected_at,omitempty"`
	// The owner is the Discord user who sent the pairing code in a direct
	// message. Until then the bot answers nobody but that pairing message.
	connectorOwnerState
}

func discordLocalStateDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "discord"), nil
}

func saveDiscordLocalCredentials(creds *discordLocalCredentials) error {
	if creds == nil || strings.TrimSpace(creds.BotToken) == "" {
		return fmt.Errorf("bot token is required")
	}
	dir, err := discordLocalStateDir()
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

func loadDiscordLocalCredentials() *discordLocalCredentials {
	creds, _ := readDiscordLocalCredentials()
	return creds
}

func readDiscordLocalCredentials() (*discordLocalCredentials, error) {
	creds, err := readLocalBotCredentials[discordLocalCredentials](discordLocalStateDir())
	if err != nil || creds == nil {
		return nil, err
	}
	if creds.BotToken == "" {
		return nil, nil
	}
	return creds, nil
}

func removeDiscordLocalCredentials() {
	dir, err := discordLocalStateDir()
	if err != nil {
		log.Printf("[discord-local] state directory unavailable: %v", err)
		return
	}
	_ = os.Remove(filepath.Join(dir, "credentials.json"))
}

func discordInviteURL(appID string) string {
	if appID == "" {
		return ""
	}
	return fmt.Sprintf(
		"https://discord.com/oauth2/authorize?client_id=%s&scope=bot%%20applications.commands&permissions=%d",
		appID, discordInvitePermissions,
	)
}

// discordValidate opens a short-lived session to confirm the token and read the
// bot's identity + application id (used for the invite URL).
//
//isoform:destination chat_bridges
func discordValidate(botToken string) (*discordgo.Session, *discordgo.User, string, error) {
	dg, err := discordgo.New("Bot " + strings.TrimSpace(botToken))
	if err != nil {
		return nil, nil, "", err
	}
	me, err := dg.User("@me")
	if err != nil {
		return nil, nil, "", err
	}
	appID := ""
	if app, err := dg.Application("@me"); err == nil && app != nil {
		appID = app.ID
	}
	return dg, me, appID, nil
}

// ── Receiver ──

type discordLocalReceiver struct {
	app     *App
	creds   discordLocalCredentials
	session *discordgo.Session
	done    chan struct{}
	cancel  context.CancelFunc

	threadMu    sync.Mutex
	threadLocks map[string]*threadLockEntry

	// ownerMu guards creds.OwnerID and creds.PairingCode, which pairing changes.
	ownerMu sync.Mutex
	// connectedAt is when the bot was connected; older messages are ignored.
	connectedAt time.Time
	// hinted holds the senders already told how to pair.
	hinted map[string]bool
}

//isoform:destination chat_bridges
func newDiscordLocalReceiver(app *App, creds discordLocalCredentials) (*discordLocalReceiver, error) {
	dg, err := discordgo.New("Bot " + creds.BotToken)
	if err != nil {
		return nil, err
	}
	dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentMessageContent
	r := &discordLocalReceiver{
		app:         app,
		creds:       creds,
		session:     dg,
		done:        make(chan struct{}),
		threadLocks: make(map[string]*threadLockEntry),
		connectedAt: connectorConnectedAt(creds.ConnectedAt, time.Now()),
		hinted:      map[string]bool{},
	}
	dg.AddHandler(func(_ *discordgo.Session, m *discordgo.MessageCreate) {
		r.onMessage(m)
	})
	return r, nil
}

func (r *discordLocalReceiver) acquireThreadLock(threadKey string) func() {
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

func (r *discordLocalReceiver) run() {
	defer close(r.done)
	// Clear the App slot on exit (gateway open failure, ctx cancel) so
	// GetDiscordStatus doesn't report "online" for a dead receiver.
	defer func() {
		r.app.mu.Lock()
		if r.app.discordLocal == r {
			r.app.discordLocal = nil
		}
		r.app.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	defer cancel()

	log.Printf("[discord-local] receiver starting for bot %s", r.creds.BotUsername)
	// Open connects the gateway in background goroutines (non-blocking) and
	// handles heartbeats + reconnects internally.
	if err := r.session.Open(); err != nil {
		log.Printf("[discord-local] gateway open failed: %v", err)
		return
	}
	<-ctx.Done()
	_ = r.session.Close()
}

func (r *discordLocalReceiver) stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

// credsSnapshot copies the credentials under ownerMu, since pairing updates them.
func (r *discordLocalReceiver) credsSnapshot() discordLocalCredentials {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	return r.creds
}

// ownerPaired reports whether an owner has paired with this bot.
func (r *discordLocalReceiver) ownerPaired() bool {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	return r.creds.paired()
}

func (r *discordLocalReceiver) onMessage(m *discordgo.MessageCreate) {
	creds := r.credsSnapshot()
	if m.Author == nil || m.Author.Bot || m.Author.ID == creds.BotUserID {
		return
	}
	text := strings.TrimSpace(m.Content)
	if text == "" {
		return
	}
	isDM := m.GuildID == ""
	if !isDM && !r.isBotMentioned(m) {
		return
	}
	text = r.stripMention(text)
	if text == "" {
		return
	}
	if connectorMessageIsStale(m.Timestamp, r.connectedAt) {
		log.Printf("[discord-local] ignored a message from before the bot was connected")
		return
	}
	if !r.senderAllowed(m, text, isDM) {
		return
	}
	chatType := "channel"
	if isDM {
		chatType = "im"
	}
	threadKey := "discord:" + creds.BotUserID + ":" + m.ChannelID + ":" + m.Author.ID

	ctx, cancel := context.WithTimeout(context.Background(), 11*time.Minute)
	defer cancel()
	unlock := r.acquireThreadLock(threadKey)
	defer unlock()

	reply := r.runAgent(ctx, threadKey, text, chatType)
	if strings.TrimSpace(reply) == "" {
		return
	}
	if err := r.app.authorizeInboundConnectorReply(ctx, "discord", threadKey, threadKey); err != nil {
		log.Printf("[discord-local] reply authority denied: %v", err)
		return
	}
	ctx = destinations.CountHTTP(ctx, destinations.ChatBridges)
	if _, err := r.session.ChannelMessageSendReply(m.ChannelID, truncateDiscordText(reply), m.Reference(), discordgo.WithContext(ctx)); err != nil {
		log.Printf("[discord-local] send reply failed: %v", err)
	}
}

// senderAllowed accepts messages only from the paired owner. Until the bot is
// paired, the only message accepted is a direct message carrying the pairing
// code: it saves the sender as the owner and is answered with a confirmation,
// but starts no run.
func (r *discordLocalReceiver) senderAllowed(m *discordgo.MessageCreate, text string, isDM bool) bool {
	r.ownerMu.Lock()
	if r.creds.paired() {
		owner := r.creds.OwnerID
		r.ownerMu.Unlock()
		if m.Author.ID != owner {
			log.Printf("[discord-local] refused a message from user %s: only the paired owner, user %s, may use this bot", m.Author.ID, owner)
			return false
		}
		return true
	}
	if !isDM {
		r.ownerMu.Unlock()
		log.Printf("[discord-local] ignored a server message from user %s: the bot is not paired yet", m.Author.ID)
		return false
	}
	if !connectorPairingMatches(r.creds.connectorOwnerState, text) {
		hint := !r.hinted[m.Author.ID]
		r.hinted[m.Author.ID] = true
		r.ownerMu.Unlock()
		log.Printf("[discord-local] ignored a message from user %s: the bot is not paired and the message is not the pairing code", m.Author.ID)
		if hint {
			r.notice(m.ChannelID, "This bot is not paired yet. Send the pairing code shown in Isoform's Discord settings.")
		}
		return false
	}
	paired := r.creds
	paired.OwnerID = m.Author.ID
	paired.OwnerPaired = true
	paired.PairingCode = ""
	if saved := loadDiscordLocalCredentials(); saved == nil || saved.BotToken != r.creds.BotToken {
		r.ownerMu.Unlock()
		log.Printf("[discord-local] pairing refused: the saved credentials are for another bot")
		return false
	}
	if err := saveDiscordLocalCredentials(&paired); err != nil {
		r.ownerMu.Unlock()
		log.Printf("[discord-local] could not save the paired owner: %v", err)
		r.notice(m.ChannelID, "Isoform could not save the pairing. Try again.")
		return false
	}
	r.creds = paired
	r.ownerMu.Unlock()
	log.Printf("[discord-local] paired with user %s", m.Author.ID)
	r.notice(m.ChannelID, "Paired with Isoform. Only you can use this bot now.")
	return false
}

// notice posts a short notice to the channel a message came from.
func (r *discordLocalReceiver) notice(channelID, text string) {
	ctx := destinations.CountHTTP(context.Background(), destinations.ChatBridges)
	if _, err := r.session.ChannelMessageSend(channelID, text, discordgo.WithContext(ctx)); err != nil {
		log.Printf("[discord-local] send notice failed: %v", err)
	}
}

func (r *discordLocalReceiver) isBotMentioned(m *discordgo.MessageCreate) bool {
	creds := r.credsSnapshot()
	for _, u := range m.Mentions {
		if u.ID == creds.BotUserID {
			return true
		}
	}
	return false
}

func (r *discordLocalReceiver) stripMention(text string) string {
	botUserID := r.credsSnapshot().BotUserID
	if botUserID == "" {
		return text
	}
	text = strings.ReplaceAll(text, "<@"+botUserID+">", "")
	text = strings.ReplaceAll(text, "<@!"+botUserID+">", "")
	return strings.TrimSpace(text)
}

func truncateDiscordText(s string) string {
	const limit = 2000
	if len(s) <= limit {
		return s
	}
	return s[:limit-1] + "…"
}

func (r *discordLocalReceiver) runAgent(ctx context.Context, threadKey, text, chatType string) string {
	personal := connectorPersonalContext(r.ownerPaired(), chatType)
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
			"connector": "discord",
			"transport": "local",
			"chat_type": chatType,
		})
	}
	r.app.mu.Lock()
	r.app.pendingTraceIDs[threadKey] = traceID
	r.app.mu.Unlock()
	defer schedulePendingTraceIDCleanup(r.app, threadKey, traceID)

	input := &agent.TaskInput{
		Prompt:           r.app.composePromptFor(text, personal, "", "discord"),
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
		r.app.triggerMemory(memorySourceDiscord, threadKey, summary, roundCount)
		return withRefusalNote(lastContent, refusals, "Discord")
	}
	if note := refusals.note("Discord"); note != "" {
		return note
	}
	if err != nil {
		log.Printf("[discord-local] agent error: %v", err)
		return "Sorry, I encountered an error processing your message."
	}
	return ""
}

// ── App-level local control ──

func (a *App) startDiscordLocal() error {
	creds := loadDiscordLocalCredentials()
	if creds == nil {
		return fmt.Errorf("no local Discord credentials")
	}
	if changed, err := ensureConnectorPairing(&creds.connectorOwnerState); err != nil {
		return fmt.Errorf("prepare Discord pairing: %w", err)
	} else if changed {
		if err := saveDiscordLocalCredentials(creds); err != nil {
			return fmt.Errorf("save Discord pairing: %w", err)
		}
	}
	r, err := newDiscordLocalReceiver(a, *creds)
	if err != nil {
		return err
	}
	a.mu.Lock()
	existing := a.discordLocal
	a.discordLocal = r
	a.mu.Unlock()
	if existing != nil {
		existing.stop()
	}
	go r.run()
	log.Printf("[discord-local] started for bot %s", creds.BotUsername)
	return nil
}

func (a *App) stopDiscordLocal() {
	a.mu.Lock()
	r := a.discordLocal
	a.discordLocal = nil
	a.mu.Unlock()
	if r != nil {
		r.stop()
	}
}

func (a *App) tryAutoStartDiscordLocal() {
	if loadDiscordLocalCredentials() == nil {
		return
	}
	if err := a.startDiscordLocal(); err != nil {
		log.Printf("[discord-local] auto-start failed: %v", err)
	}
}

// DiscordTestResult is returned by the connection-test binding. InviteURL lets
// the wizard offer a one-click "add bot to your server" step.
type DiscordTestResult struct {
	Success     bool   `json:"success"`
	BotUserID   string `json:"bot_user_id,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
	InviteURL   string `json:"invite_url,omitempty"`
	Error       string `json:"error,omitempty"`
}

// TestDiscordConnection validates a bot token (no persist) and returns the
// auto-generated invite URL.
func (a *App) TestDiscordConnection(botToken string) DiscordTestResult {
	botToken = strings.TrimSpace(botToken)
	if botToken == "" {
		return DiscordTestResult{Error: "bot token is required"}
	}
	_, me, appID, err := discordValidate(botToken)
	if err != nil {
		return DiscordTestResult{Error: err.Error()}
	}
	return DiscordTestResult{
		Success:     true,
		BotUserID:   me.ID,
		BotUsername: me.Username,
		InviteURL:   discordInviteURL(appID),
	}
}

// SaveDiscordLocalCredentials validates, persists, and starts the receiver.
func (a *App) SaveDiscordLocalCredentials(botToken string) error {
	dir, err := discordLocalStateDir()
	if err != nil {
		return err
	}
	botToken = strings.TrimSpace(botToken)
	if botToken == "" {
		return fmt.Errorf("bot token is required")
	}
	_, me, appID, err := discordValidate(botToken)
	if err != nil {
		return fmt.Errorf("connection test failed — not saved: %w", err)
	}
	creds := &discordLocalCredentials{
		BotToken:    botToken,
		AppID:       appID,
		BotUserID:   me.ID,
		BotUsername: me.Username,
		ConnectedAt: time.Now().Format(time.RFC3339),
	}
	// Reconnecting the same bot keeps an owner who paired with it; anything
	// else pairs again with a new code.
	for _, previous := range []*discordLocalCredentials{loadDiscordLocalCredentials(), archivedDiscordLocalCredentials()} {
		if previous != nil && previous.BotUserID == me.ID && previous.paired() {
			creds.connectorOwnerState = connectorOwnerState{OwnerID: previous.OwnerID, OwnerName: previous.OwnerName, OwnerPaired: true}
			break
		}
	}
	if _, err := ensureConnectorPairing(&creds.connectorOwnerState); err != nil {
		return fmt.Errorf("create a pairing code: %w", err)
	}
	if err := saveDiscordLocalCredentials(creds); err != nil {
		return err
	}
	if err := a.bindIntegrationSelfIdentity(context.Background(), "discord", me.ID, me.Username); err != nil {
		log.Printf("[discord-local] bind self identity failed: %v", err)
	}
	// A fresh connect supersedes any archived (reusable) credentials.
	removeArchivedLocalCreds(dir)
	return a.startDiscordLocal()
}

// GetDiscordInviteURL returns the invite URL for the saved bot (for the wizard's
// "add to server" button after save).
func (a *App) GetDiscordInviteURL() string {
	creds := loadDiscordLocalCredentials()
	if creds == nil {
		return ""
	}
	return discordInviteURL(creds.AppID)
}

func (a *App) discordLocalStatus() DiscordStatus {
	creds := loadDiscordLocalCredentials()
	if creds == nil {
		return DiscordStatus{}
	}
	a.mu.Lock()
	online := a.discordLocal != nil
	a.mu.Unlock()
	return DiscordStatus{
		Bound:         true,
		DiscordUser:   creds.BotUsername,
		DiscordUserID: creds.BotUserID,
		Guilds:        []DiscordGuild{},
		DaemonOnline:  online,
		Paired:        creds.paired(),
		PairingCode:   creds.PairingCode,
	}
}

// disconnectDiscordLocal stops the receiver and clears local credentials. A copy
// is archived first so the user can reuse the same bot on a future reconnect
// instead of re-entering the token (mirrors Feishu).
func (a *App) disconnectDiscordLocal() error {
	dir, err := discordLocalStateDir()
	if err != nil {
		return err
	}
	a.stopDiscordLocal()
	archiveLocalCreds(dir)
	removeDiscordLocalCredentials()
	return nil
}

// archivedDiscordLocalCredentials returns the archived (reusable) bot, or nil.
func archivedDiscordLocalCredentials() *discordLocalCredentials {
	dir, err := discordLocalStateDir()
	if err != nil {
		log.Printf("[discord-local] state directory unavailable: %v", err)
		return nil
	}
	data := readArchivedLocalCreds(dir)
	if data == nil {
		return nil
	}
	var creds discordLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil || strings.TrimSpace(creds.BotToken) == "" {
		return nil
	}
	return &creds
}

// GetDiscordArchived reports whether a reusable (archived) Discord bot exists.
// Wails binding consumed by the chat app setup reuse card.
func (a *App) GetDiscordArchived() ArchivedConnectorInfo {
	dir, err := discordLocalStateDir()
	if err != nil {
		log.Printf("[discord-local] state directory unavailable: %v", err)
		return ArchivedConnectorInfo{}
	}
	data := readArchivedLocalCreds(dir)
	if data == nil {
		return ArchivedConnectorInfo{}
	}
	var creds discordLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil || strings.TrimSpace(creds.BotToken) == "" {
		return ArchivedConnectorInfo{}
	}
	return ArchivedConnectorInfo{Has: true, Label: strings.TrimSpace(creds.BotUsername)}
}

// ReuseDiscordLocal restores the archived bot token, re-validates it, and
// reconnects — no need to paste the token again. Wails binding.
func (a *App) ReuseDiscordLocal() error {
	dir, err := discordLocalStateDir()
	if err != nil {
		return err
	}
	data := readArchivedLocalCreds(dir)
	if data == nil {
		return fmt.Errorf("no archived Discord credentials to reuse")
	}
	var creds discordLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return fmt.Errorf("archived credentials corrupt: %w", err)
	}
	if strings.TrimSpace(creds.BotToken) == "" {
		return fmt.Errorf("archived credentials missing bot token")
	}
	// SaveDiscordLocalCredentials re-validates, persists, starts, and clears the
	// archived copy on success.
	return a.SaveDiscordLocalCredentials(creds.BotToken)
}

// DiscardDiscordArchived drops the archived bot so the user starts fresh
// ("Create new" in the reuse card). Wails binding.
func (a *App) DiscardDiscordArchived() {
	dir, err := discordLocalStateDir()
	if err != nil {
		log.Printf("[discord-local] state directory unavailable: %v", err)
		return
	}
	removeArchivedLocalCreds(dir)
}
