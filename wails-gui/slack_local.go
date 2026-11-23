package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"yansu-agent/agent"

	"wails-gui/internal/destinations"
)

// Slack local integration: a user-built Slack app in Socket Mode (bot token
// xoxb- + app-level token xapp-), talking directly to Slack — no Yansu backend
// relay. Mirrors telegram_local.go / feishu_local.go.

type slackLocalCredentials struct {
	BotToken    string `json:"bot_token"`
	AppToken    string `json:"app_token"`
	TeamName    string `json:"team_name,omitempty"`
	BotUserID   string `json:"bot_user_id,omitempty"`
	BotName     string `json:"bot_name,omitempty"`
	ConnectedAt string `json:"connected_at,omitempty"`
	// The owner is the Slack user who sent the pairing code in a direct
	// message. Until then the bot answers nobody but that pairing message.
	connectorOwnerState
}

// archivedSlackLocalCredentials returns the archived (reusable) Slack app, or nil.
func archivedSlackLocalCredentials() *slackLocalCredentials {
	dir, err := slackLocalStateDir()
	if err != nil {
		log.Printf("[slack-local] state directory unavailable: %v", err)
		return nil
	}
	data := readArchivedLocalCreds(dir)
	if data == nil {
		return nil
	}
	var creds slackLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil || strings.TrimSpace(creds.BotToken) == "" {
		return nil
	}
	return &creds
}

func slackLocalStateDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "slack"), nil
}

func saveSlackLocalCredentials(creds *slackLocalCredentials) error {
	if creds == nil || strings.TrimSpace(creds.BotToken) == "" || strings.TrimSpace(creds.AppToken) == "" {
		return fmt.Errorf("bot token and app token are required")
	}
	dir, err := slackLocalStateDir()
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

func loadSlackLocalCredentials() *slackLocalCredentials {
	creds, _ := readSlackLocalCredentials()
	return creds
}

func readSlackLocalCredentials() (*slackLocalCredentials, error) {
	creds, err := readLocalBotCredentials[slackLocalCredentials](slackLocalStateDir())
	if err != nil || creds == nil {
		return nil, err
	}
	if creds.BotToken == "" || creds.AppToken == "" {
		return nil, nil
	}
	return creds, nil
}

func removeSlackLocalCredentials() {
	dir, err := slackLocalStateDir()
	if err != nil {
		log.Printf("[slack-local] state directory unavailable: %v", err)
		return
	}
	_ = os.Remove(filepath.Join(dir, "credentials.json"))
}

// slackAuthTest validates a bot+app token pair and returns workspace/bot info.
//
//isoform:destination chat_bridges
func slackAuthTest(botToken, appToken string) (*slack.AuthTestResponse, error) {
	api := slack.New(botToken, slack.OptionAppLevelToken(appToken))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return api.AuthTestContext(ctx)
}

// ── Receiver ──

type slackLocalReceiver struct {
	app    *App
	creds  slackLocalCredentials
	api    *slack.Client
	sm     *socketmode.Client
	cancel context.CancelFunc
	done   chan struct{}

	threadMu    sync.Mutex
	threadLocks map[string]*threadLockEntry

	// ownerMu guards creds.OwnerID and creds.PairingCode, which pairing changes.
	ownerMu sync.Mutex
	// connectedAt is when the app was connected; older messages are ignored.
	connectedAt time.Time
	// hinted holds the senders already told how to pair.
	hinted map[string]bool
}

//isoform:destination chat_bridges
func newSlackLocalReceiver(app *App, creds slackLocalCredentials) *slackLocalReceiver {
	api := slack.New(creds.BotToken, slack.OptionAppLevelToken(creds.AppToken))
	return &slackLocalReceiver{
		app:         app,
		creds:       creds,
		api:         api,
		sm:          socketmode.New(api),
		done:        make(chan struct{}),
		threadLocks: make(map[string]*threadLockEntry),
		connectedAt: connectorConnectedAt(creds.ConnectedAt, time.Now()),
		hinted:      map[string]bool{},
	}
}

// credsSnapshot copies the credentials under ownerMu, since pairing updates them.
func (r *slackLocalReceiver) credsSnapshot() slackLocalCredentials {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	return r.creds
}

// ownerPaired reports whether an owner has paired with this app.
func (r *slackLocalReceiver) ownerPaired() bool {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	return r.creds.paired()
}

func (r *slackLocalReceiver) acquireThreadLock(threadKey string) func() {
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

func (r *slackLocalReceiver) run() {
	defer close(r.done)
	// Clear the App slot when the receiver exits for any reason (Socket Mode
	// failure, ctx cancel) so GetSlackStatus doesn't report "online" for a dead
	// receiver. Guarded by identity so we never clear a newer receiver.
	defer func() {
		r.app.mu.Lock()
		if r.app.slackLocal == r {
			r.app.slackLocal = nil
		}
		r.app.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-r.sm.Events:
				if !ok {
					return
				}
				r.handleSocketEvent(ctx, evt)
			}
		}
	}()

	log.Printf("[slack-local] receiver starting for team %q", r.creds.TeamName)
	// RunContext blocks, handling reconnects internally, until ctx is cancelled.
	if err := r.sm.RunContext(ctx); err != nil && ctx.Err() == nil {
		log.Printf("[slack-local] receiver exited: %v", err)
	}
}

func (r *slackLocalReceiver) stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

func (r *slackLocalReceiver) handleSocketEvent(ctx context.Context, evt socketmode.Event) {
	if evt.Type != socketmode.EventTypeEventsAPI {
		return
	}
	eventsAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return
	}
	if evt.Request != nil {
		r.sm.Ack(*evt.Request)
	}
	if eventsAPI.Type != slackevents.CallbackEvent {
		return
	}
	switch ev := eventsAPI.InnerEvent.Data.(type) {
	case *slackevents.MessageEvent:
		// Only handle DMs here; channel messages are handled via app_mention to
		// avoid responding to every channel message.
		if ev.ChannelType != "im" {
			return
		}
		go r.handleMessage(ctx, ev.User, ev.Channel, ev.Text, ev.ThreadTimeStamp, ev.TimeStamp, ev.BotID, ev.SubType, "im")
	case *slackevents.AppMentionEvent:
		go r.handleMessage(ctx, ev.User, ev.Channel, ev.Text, ev.ThreadTimeStamp, ev.TimeStamp, ev.BotID, "", "channel")
	}
}

func (r *slackLocalReceiver) handleMessage(ctx context.Context, user, channel, text, threadTS, ts, botID, subType, chatType string) {
	creds := r.credsSnapshot()
	// Skip bot messages, edits, joins, and our own messages.
	if botID != "" || subType != "" || user == "" || user == creds.BotUserID {
		return
	}
	text = stripSlackMention(strings.TrimSpace(text))
	if text == "" {
		return
	}
	if connectorMessageIsStale(slackMessageTime(ts), r.connectedAt) {
		log.Printf("[slack-local] ignored a message from before the app was connected")
		return
	}
	if !r.senderAllowed(ctx, user, channel, text, chatType) {
		return
	}
	_ = r.app.observeCommunicationMessage(communicationMessage{Source: "slack", App: "Slack", MessageID: channel + ":" + ts, AuthorID: user, OwnerID: creds.OwnerID, Text: text, SentAt: slackMessageTime(ts), BodyAvailable: true})
	// DMs have no thread; keying on the per-message ts would make every DM a new
	// session (breaking history + memory continuity), so use a stable per-user
	// key. Channels/threads keep the thread root ts so each thread is its own
	// session.
	threadKey := "slack:" + creds.BotUserID + ":" + channel + ":" + user
	rootTS := threadTS
	if rootTS == "" {
		rootTS = ts
	}
	if chatType != "im" {
		threadKey += ":" + rootTS
	}
	unlock := r.acquireThreadLock(threadKey)
	defer unlock()

	reply := r.runAgent(ctx, threadKey, text, chatType)
	if strings.TrimSpace(reply) == "" {
		return
	}
	if err := r.app.authorizeInboundConnectorReply(ctx, "slack", threadKey, threadKey); err != nil {
		log.Printf("[slack-local] reply authority denied: %v", err)
		return
	}
	ctx = destinations.CountHTTP(ctx, destinations.ChatBridges)
	if _, _, err := r.api.PostMessageContext(ctx, channel,
		slack.MsgOptionText(reply, false),
		slack.MsgOptionTS(rootTS),
	); err != nil {
		log.Printf("[slack-local] send reply failed: %v", err)
	}
}

// slackMessageTime reads a Slack message timestamp such as "1700000000.000100".
// An unreadable one gives the zero time, which connectorMessageIsStale refuses.
func slackMessageTime(ts string) time.Time {
	secs, err := strconv.ParseFloat(strings.TrimSpace(ts), 64)
	if err != nil || secs <= 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(secs*float64(time.Second)))
}

// senderAllowed accepts messages only from the paired owner. Until the app is
// paired, the only message accepted is a direct message carrying the pairing
// code: it saves the sender as the owner and is answered with a confirmation,
// but starts no run.
func (r *slackLocalReceiver) senderAllowed(ctx context.Context, user, channel, text, chatType string) bool {
	r.ownerMu.Lock()
	if r.creds.paired() {
		owner := r.creds.OwnerID
		r.ownerMu.Unlock()
		if user != owner {
			log.Printf("[slack-local] refused a message from user %s: only the paired owner, user %s, may use this app", user, owner)
			return false
		}
		return true
	}
	if chatType != "im" {
		r.ownerMu.Unlock()
		log.Printf("[slack-local] ignored a channel message from user %s: the app is not paired yet", user)
		return false
	}
	if !connectorPairingMatches(r.creds.connectorOwnerState, text) {
		hint := !r.hinted[user]
		r.hinted[user] = true
		r.ownerMu.Unlock()
		log.Printf("[slack-local] ignored a message from user %s: the app is not paired and the message is not the pairing code", user)
		if hint {
			r.notice(ctx, channel, "This app is not paired yet. Send the pairing code shown in Isoform's Slack settings.")
		}
		return false
	}
	paired := r.creds
	paired.OwnerID = user
	paired.OwnerPaired = true
	paired.PairingCode = ""
	if saved := loadSlackLocalCredentials(); saved == nil || saved.BotToken != r.creds.BotToken {
		r.ownerMu.Unlock()
		log.Printf("[slack-local] pairing refused: the saved credentials are for another app")
		return false
	}
	if err := saveSlackLocalCredentials(&paired); err != nil {
		r.ownerMu.Unlock()
		log.Printf("[slack-local] could not save the paired owner: %v", err)
		r.notice(ctx, channel, "Isoform could not save the pairing. Try again.")
		return false
	}
	r.creds = paired
	r.ownerMu.Unlock()
	log.Printf("[slack-local] paired with user %s", user)
	r.notice(ctx, channel, "Paired with Isoform. Only you can use this app now.")
	return false
}

// notice posts a short notice to the conversation a message came from.
func (r *slackLocalReceiver) notice(ctx context.Context, channel, text string) {
	ctx = destinations.CountHTTP(ctx, destinations.ChatBridges)
	if _, _, err := r.api.PostMessageContext(ctx, channel, slack.MsgOptionText(text, false)); err != nil {
		log.Printf("[slack-local] send notice failed: %v", err)
	}
}

// stripSlackMention removes the leading <@BOTID> mention Slack injects into
// app_mention text.
func stripSlackMention(text string) string {
	for {
		start := strings.Index(text, "<@")
		if start != 0 {
			break
		}
		end := strings.Index(text, ">")
		if end < 0 {
			break
		}
		text = strings.TrimSpace(text[end+1:])
	}
	return text
}

func (r *slackLocalReceiver) runAgent(ctx context.Context, threadKey, text, chatType string) string {
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
			"connector": "slack",
			"transport": "local",
			"chat_type": chatType,
		})
	}
	r.app.mu.Lock()
	r.app.pendingTraceIDs[threadKey] = traceID
	r.app.mu.Unlock()
	defer schedulePendingTraceIDCleanup(r.app, threadKey, traceID)

	input := &agent.TaskInput{
		Prompt:           r.app.composePromptFor(text, personal, "", "slack"),
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
		r.app.triggerMemory(memorySourceSlack, threadKey, summary, roundCount)
		return withRefusalNote(lastContent, refusals, "Slack")
	}
	if note := refusals.note("Slack"); note != "" {
		return note
	}
	if err != nil {
		log.Printf("[slack-local] agent error: %v", err)
		return "Sorry, I encountered an error processing your message."
	}
	return ""
}

// ── App-level local control ──

func (a *App) startSlackLocal() error {
	creds := loadSlackLocalCredentials()
	if creds == nil {
		return fmt.Errorf("no local Slack credentials")
	}
	if changed, err := ensureConnectorPairing(&creds.connectorOwnerState); err != nil {
		return fmt.Errorf("prepare Slack pairing: %w", err)
	} else if changed {
		if err := saveSlackLocalCredentials(creds); err != nil {
			return fmt.Errorf("save Slack pairing: %w", err)
		}
	}
	a.mu.Lock()
	existing := a.slackLocal
	r := newSlackLocalReceiver(a, *creds)
	a.slackLocal = r
	a.mu.Unlock()
	if existing != nil {
		existing.stop()
	}
	go r.run()
	log.Printf("[slack-local] started for team %q", creds.TeamName)
	return nil
}

func (a *App) stopSlackLocal() {
	a.mu.Lock()
	r := a.slackLocal
	a.slackLocal = nil
	a.mu.Unlock()
	if r != nil {
		r.stop()
	}
}

func (a *App) tryAutoStartSlackLocal() {
	if loadSlackLocalCredentials() == nil {
		return
	}
	if err := a.startSlackLocal(); err != nil {
		log.Printf("[slack-local] auto-start failed: %v", err)
	}
}

// SlackTestResult is returned by the connection-test binding.
type SlackTestResult struct {
	Success   bool   `json:"success"`
	TeamName  string `json:"team_name,omitempty"`
	BotUserID string `json:"bot_user_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// TestSlackConnection validates a bot+app token pair without persisting.
func (a *App) TestSlackConnection(botToken, appToken string) SlackTestResult {
	botToken = strings.TrimSpace(botToken)
	appToken = strings.TrimSpace(appToken)
	if botToken == "" || appToken == "" {
		return SlackTestResult{Error: "bot token and app token are required"}
	}
	resp, err := slackAuthTest(botToken, appToken)
	if err != nil {
		return SlackTestResult{Error: err.Error()}
	}
	return SlackTestResult{Success: true, TeamName: resp.Team, BotUserID: resp.UserID}
}

// SaveSlackLocalCredentials validates, persists, and starts the local receiver.
func (a *App) SaveSlackLocalCredentials(botToken, appToken string) error {
	dir, err := slackLocalStateDir()
	if err != nil {
		return err
	}
	botToken = strings.TrimSpace(botToken)
	appToken = strings.TrimSpace(appToken)
	if botToken == "" || appToken == "" {
		return fmt.Errorf("bot token and app token are required")
	}
	resp, err := slackAuthTest(botToken, appToken)
	if err != nil {
		return fmt.Errorf("connection test failed — not saved: %w", err)
	}
	creds := &slackLocalCredentials{
		BotToken:    botToken,
		AppToken:    appToken,
		TeamName:    resp.Team,
		BotUserID:   resp.UserID,
		BotName:     resp.User,
		ConnectedAt: time.Now().Format(time.RFC3339),
	}
	// Reconnecting the same app keeps an owner who paired with it; anything
	// else pairs again with a new code.
	for _, previous := range []*slackLocalCredentials{loadSlackLocalCredentials(), archivedSlackLocalCredentials()} {
		if previous != nil && previous.BotUserID == resp.UserID && previous.paired() {
			creds.connectorOwnerState = connectorOwnerState{OwnerID: previous.OwnerID, OwnerName: previous.OwnerName, OwnerPaired: true}
			break
		}
	}
	if _, err := ensureConnectorPairing(&creds.connectorOwnerState); err != nil {
		return fmt.Errorf("create a pairing code: %w", err)
	}
	if err := saveSlackLocalCredentials(creds); err != nil {
		return err
	}
	if err := a.bindIntegrationSelfIdentity(context.Background(), "slack", resp.UserID, resp.User); err != nil {
		log.Printf("[slack-local] bind self identity failed: %v", err)
	}
	// A fresh connect supersedes any archived (reusable) credentials.
	removeArchivedLocalCreds(dir)
	return a.startSlackLocal()
}

func (a *App) slackLocalStatus() SlackStatus {
	creds := loadSlackLocalCredentials()
	if creds == nil {
		return SlackStatus{}
	}
	a.mu.Lock()
	online := a.slackLocal != nil
	a.mu.Unlock()
	return SlackStatus{
		Connected:     true,
		TeamName:      creds.TeamName,
		SlackUserID:   creds.BotUserID,
		SlackUserName: creds.BotName,
		DaemonOnline:  online,
		Paired:        creds.paired(),
		PairingCode:   creds.PairingCode,
	}
}

// disconnectSlackLocal stops the receiver and clears local credentials. A copy
// is archived first so the user can reuse the same app on a future reconnect
// instead of re-entering both tokens (mirrors Feishu).
func (a *App) disconnectSlackLocal() error {
	dir, err := slackLocalStateDir()
	if err != nil {
		return err
	}
	a.stopSlackLocal()
	archiveLocalCreds(dir)
	removeSlackLocalCredentials()
	return nil
}

// GetSlackArchived reports whether a reusable (archived) Slack app exists.
// Wails binding consumed by the chat app setup reuse card.
func (a *App) GetSlackArchived() ArchivedConnectorInfo {
	dir, err := slackLocalStateDir()
	if err != nil {
		log.Printf("[slack-local] state directory unavailable: %v", err)
		return ArchivedConnectorInfo{}
	}
	data := readArchivedLocalCreds(dir)
	if data == nil {
		return ArchivedConnectorInfo{}
	}
	var creds slackLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil || creds.BotToken == "" || creds.AppToken == "" {
		return ArchivedConnectorInfo{}
	}
	return ArchivedConnectorInfo{Has: true, Label: strings.TrimSpace(creds.TeamName)}
}

// ReuseSlackLocal restores the archived token pair, re-validates it, and
// reconnects — no need to paste the tokens again. Wails binding.
func (a *App) ReuseSlackLocal() error {
	dir, err := slackLocalStateDir()
	if err != nil {
		return err
	}
	data := readArchivedLocalCreds(dir)
	if data == nil {
		return fmt.Errorf("no archived Slack credentials to reuse")
	}
	var creds slackLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return fmt.Errorf("archived credentials corrupt: %w", err)
	}
	if creds.BotToken == "" || creds.AppToken == "" {
		return fmt.Errorf("archived credentials missing tokens")
	}
	// SaveSlackLocalCredentials re-validates, persists, starts, and clears the
	// archived copy on success.
	return a.SaveSlackLocalCredentials(creds.BotToken, creds.AppToken)
}

// DiscardSlackArchived drops the archived app so the user starts fresh
// ("Create new" in the reuse card). Wails binding.
func (a *App) DiscardSlackArchived() {
	dir, err := slackLocalStateDir()
	if err != nil {
		log.Printf("[slack-local] state directory unavailable: %v", err)
		return
	}
	removeArchivedLocalCreds(dir)
}
