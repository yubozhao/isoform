package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"yansu-agent/agent"

	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const (
	feishuRegistrationURL = "https://accounts.feishu.cn/oauth/v1/app/registration"
	larkRegistrationURL   = "https://accounts.larksuite.com/oauth/v1/app/registration"
	feishuAPIBaseURL      = "https://open.feishu.cn"
	larkAPIBaseURL        = "https://open.larksuite.com"
)

type feishuLocalCredentials struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
	OpenID    string `json:"open_id"`
	UserName  string `json:"user_name,omitempty"`
	// BotName is the connected bot application's display name (e.g.
	// "<registrant>的智能助手"). Lazily fetched and persisted so the connection
	// card can identify the bot instead of the human registrant (OpenID/UserName).
	BotName        string `json:"bot_name,omitempty"`
	Brand          string `json:"brand,omitempty"`
	ConnectedAt    string `json:"connected_at"`
	VerificationID string `json:"verification_id,omitempty"`
	// PermissionsReady records that the full Feishu permission set was confirmed
	// granted at least once, so the desktop can keep the "enable full access"
	// panel hidden across page reloads instead of re-deriving it every mount.
	PermissionsReady bool `json:"permissions_ready,omitempty"`
	// The owner is the Feishu user who sent the pairing code in a direct
	// message. Until then the app answers nobody but that pairing message.
	connectorOwnerState
}

type feishuRegistrationSession struct {
	SessionID  string
	DeviceCode string
	// Brand is the brand the user picked in the UI (informational).
	Brand string
	// PollBrand is the domain currently used for polling. Registration always
	// begins on the Feishu domain (its launcher works for both Feishu and Lark);
	// when a poll reports tenant_brand=lark we switch this to "lark" and continue
	// polling on the Lark domain, mirroring the openclaw reference flow.
	PollBrand string
}

var feishuRegistrationSessions sync.Map // session_id -> *feishuRegistrationSession

// normalizeFeishuBrand maps any brand string to the two supported values so
// feishu and lark each get their own isolated state directory and config slot.
func normalizeFeishuBrand(brand string) string {
	if strings.EqualFold(strings.TrimSpace(brand), "lark") {
		return "lark"
	}
	return "feishu"
}

func feishuStateDir(brand string) (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, normalizeFeishuBrand(brand)), nil
}

func saveFeishuLocalCredentials(creds *feishuLocalCredentials) error {
	if creds == nil {
		return fmt.Errorf("nil Feishu credentials")
	}
	dir, err := feishuStateDir(creds.Brand)
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

func loadFeishuLocalCredentials(brand string) *feishuLocalCredentials {
	dir, err := feishuStateDir(brand)
	if err != nil {
		log.Printf("[feishu-local] state directory unavailable: %v", err)
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		return nil
	}
	var creds feishuLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil
	}
	if creds.AppID == "" || creds.AppSecret == "" {
		return nil
	}
	return &creds
}

func removeFeishuLocalCredentials(brand string) {
	dir, err := feishuStateDir(brand)
	if err != nil {
		log.Printf("[feishu-local] state directory unavailable: %v", err)
		return
	}
	_ = os.Remove(filepath.Join(dir, "credentials.json"))
}

const feishuArchivedCredsFile = "credentials.archived.json"

func feishuArchivedCredsPath(brand string) (string, error) {
	dir, err := feishuStateDir(brand)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, feishuArchivedCredsFile), nil
}

// archiveFeishuLocalCredentials keeps a copy of the active credentials so a user
// who disconnects can later reuse the same scan-created app instead of creating
// a brand-new one (each new registration provisions a fresh PersonalAgent app
// with a new app_id/open_id and reset permissions). No-op when nothing is saved.
func archiveFeishuLocalCredentials(brand string) {
	path, err := feishuArchivedCredsPath(brand)
	if err != nil {
		log.Printf("[feishu-local] archived credentials path unavailable: %v", err)
		return
	}
	creds := loadFeishuLocalCredentials(brand)
	if creds == nil {
		return
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		log.Printf("[feishu-local] failed to encode archived credentials: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("[feishu-local] failed to archive credentials: %v", err)
	}
}

func loadArchivedFeishuLocalCredentials(brand string) *feishuLocalCredentials {
	path, err := feishuArchivedCredsPath(brand)
	if err != nil {
		log.Printf("[feishu-local] archived credentials path unavailable: %v", err)
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var creds feishuLocalCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil
	}
	if creds.AppID == "" || creds.AppSecret == "" {
		return nil
	}
	return &creds
}

func removeArchivedFeishuLocalCredentials(brand string) {
	path, err := feishuArchivedCredsPath(brand)
	if err != nil {
		log.Printf("[feishu-local] archived credentials path unavailable: %v", err)
		return
	}
	_ = os.Remove(path)
}

func resolveFeishuRegistrationURL(brand string) string {
	if strings.EqualFold(brand, "lark") {
		return larkRegistrationURL
	}
	return feishuRegistrationURL
}

func resolveFeishuAPIBaseURL(brand string) string {
	if strings.EqualFold(brand, "lark") {
		return larkAPIBaseURL
	}
	return feishuAPIBaseURL
}

//isoform:destination chat_bridges
func postFeishuRegistration(ctx context.Context, brand string, form url.Values) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveFeishuRegistrationURL(brand), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("registration request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("registration response HTTP %d: %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("registration HTTP %d: %s", resp.StatusCode, regErrMsg(out))
	}
	return out, nil
}

func regString(v any) string {
	s, _ := v.(string)
	return s
}

func regInt(v any, fallback int) int {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	}
	return fallback
}

func regErrMsg(resp map[string]any) string {
	if msg := regString(resp["error_description"]); msg != "" {
		return msg
	}
	if msg := regString(resp["error_msg"]); msg != "" {
		return msg
	}
	if msg := regString(resp["error"]); msg != "" {
		return msg
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// appendFromOnboard adds the from=onboard query parameter the Feishu/Lark
// launcher expects on first load. Without it the larksuite launcher renders
// "链接已失效" on the initial scan (refreshing the same URL then works);
// openclaw's reference onboarding flow sets the same param. Applied to both
// brands to match that proven flow.
func appendFromOnboard(raw string) string {
	if raw == "" || strings.Contains(raw, "from=onboard") {
		return raw
	}
	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	return raw + sep + "from=onboard"
}

func beginFeishuLocalRegistration(ctx context.Context, brand string) (*FeishuRegistration, *feishuRegistrationSession, error) {
	// Always begin on the Feishu domain — its launcher (open.feishu.cn) works for
	// both Feishu and Lark accounts, whereas open.larksuite.com renders "链接已失效"
	// on first load. The real brand is detected from tenant_brand during polling
	// and the poll domain is switched to Lark then. This mirrors openclaw.
	const beginBrand = "feishu"
	if _, err := postFeishuRegistration(ctx, beginBrand, url.Values{"action": {"init"}}); err != nil {
		return nil, nil, err
	}
	resp, err := postFeishuRegistration(ctx, beginBrand, url.Values{
		"action":            {"begin"},
		"archetype":         {"PersonalAgent"},
		"auth_method":       {"client_secret"},
		"request_user_info": {"open_id"},
	})
	if err != nil {
		return nil, nil, err
	}
	deviceCode := regString(resp["device_code"])
	if deviceCode == "" {
		return nil, nil, fmt.Errorf("registration begin missing device_code: %s", regErrMsg(resp))
	}
	verificationURI := regString(resp["verification_uri"])
	completeURI := regString(resp["verification_uri_complete"])
	if completeURI == "" {
		completeURI = verificationURI
	}
	completeURI = appendFromOnboard(completeURI)
	var b [16]byte
	_, _ = rand.Read(b[:])
	sessionID := fmt.Sprintf("%x", b[:])
	reg := &FeishuRegistration{
		SessionID:               sessionID,
		VerificationURIComplete: completeURI,
		VerificationURI:         verificationURI,
		UserCode:                regString(resp["user_code"]),
		ExpiresIn:               regInt(resp["expire_in"], 600),
		Interval:                regInt(resp["interval"], 5),
	}
	return reg, &feishuRegistrationSession{
		SessionID:  sessionID,
		DeviceCode: deviceCode,
		Brand:      brand,
		PollBrand:  beginBrand,
	}, nil
}

func pollFeishuLocalRegistration(ctx context.Context, sess *feishuRegistrationSession) (*feishuLocalCredentials, string, error) {
	if sess == nil || sess.DeviceCode == "" {
		return nil, "expired", nil
	}
	pollBrand := sess.PollBrand
	if pollBrand == "" {
		pollBrand = "feishu"
	}
	resp, err := postFeishuRegistration(ctx, pollBrand, url.Values{
		"action":      {"poll"},
		"device_code": {sess.DeviceCode},
	})
	if err != nil {
		return nil, "pending", err
	}
	// Switch the poll domain to Lark as soon as the scanning user is identified as
	// a Lark tenant — even while still pending — so subsequent polls (and the
	// final credential fetch) hit the correct domain. Mirrors openclaw's setDomain.
	if ui, ok := resp["user_info"].(map[string]any); ok {
		if tb := regString(ui["tenant_brand"]); normalizeFeishuBrand(tb) == "lark" && sess.PollBrand != "lark" {
			sess.PollBrand = "lark"
			return nil, "pending", nil
		}
	}
	if e := regString(resp["error"]); e != "" {
		switch e {
		case "authorization_pending", "slow_down":
			return nil, "pending", nil
		case "access_denied":
			return nil, "error", nil
		case "expired_token", "invalid_grant":
			return nil, "expired", nil
		default:
			return nil, "error", fmt.Errorf("registration poll error: %s", regErrMsg(resp))
		}
	}
	clientID := regString(resp["client_id"])
	clientSecret := regString(resp["client_secret"])
	if clientID == "" || clientSecret == "" {
		return nil, "pending", nil
	}
	creds := &feishuLocalCredentials{
		AppID:       clientID,
		AppSecret:   clientSecret,
		Brand:       normalizeFeishuBrand(pollBrand),
		ConnectedAt: time.Now().Format(time.RFC3339),
	}
	if ui, ok := resp["user_info"].(map[string]any); ok {
		creds.OpenID = regString(ui["open_id"])
		creds.UserName = firstRegString(ui, "name", "display_name", "nickname", "en_name")
		if tb := regString(ui["tenant_brand"]); tb != "" {
			creds.Brand = tb
		}
	}
	return creds, "connected", nil
}

func firstRegString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(regString(values[key])); value != "" {
			return value
		}
	}
	return ""
}

type feishuLocalReceiver struct {
	app    *App
	creds  feishuLocalCredentials
	cancel context.CancelFunc
	done   chan struct{}

	threadMu    sync.Mutex
	threadLocks map[string]*threadLockEntry
	poster      *feishuLocalPoster
	botOpenID   string

	// ownerMu guards creds.OwnerID and creds.PairingCode, which pairing changes.
	ownerMu sync.Mutex
	// connectedAt is when the app was connected; older messages are ignored.
	connectedAt time.Time
	// hinted holds the senders already told how to pair.
	hinted map[string]bool
}

func newFeishuLocalReceiver(app *App, creds feishuLocalCredentials) *feishuLocalReceiver {
	return &feishuLocalReceiver{
		app:         app,
		creds:       creds,
		done:        make(chan struct{}),
		threadLocks: make(map[string]*threadLockEntry),
		poster:      newFeishuLocalPoster(creds),
		connectedAt: connectorConnectedAt(creds.ConnectedAt, time.Now()),
		hinted:      map[string]bool{},
	}
}

// ownerPaired reports whether an owner has paired with this app.
func (r *feishuLocalReceiver) ownerPaired() bool {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	return r.creds.paired()
}

func (r *feishuLocalReceiver) acquireThreadLock(threadKey string) func() {
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

//isoform:destination chat_bridges
func (r *feishuLocalReceiver) run() {
	defer close(r.done)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	defer cancel()

	r.botOpenID = r.poster.getBotOpenID(ctx)
	d := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
			r.handleSDKMessage(ctx, event)
			return nil
		})
	wsOpts := []larkws.ClientOption{
		larkws.WithEventHandler(d),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	}
	// Lark apps live on the larksuite domain; without this the WS client connects
	// to open.feishu.cn (the SDK default) and never receives the app's messages.
	if normalizeFeishuBrand(r.creds.Brand) == "lark" {
		wsOpts = append(wsOpts, larkws.WithDomain(lark.LarkBaseUrl))
	}
	cli := larkws.NewClient(r.creds.AppID, r.creds.AppSecret, wsOpts...)
	log.Printf("[feishu-local] receiver starting for app %s (brand=%s)", r.creds.AppID, normalizeFeishuBrand(r.creds.Brand))
	if err := cli.Start(ctx); err != nil && ctx.Err() == nil {
		log.Printf("[feishu-local] receiver exited: %v", err)
	}
}

func (r *feishuLocalReceiver) stop() {
	if r.cancel != nil {
		r.cancel()
	}
	<-r.done
}

type feishuLocalMessage struct {
	SenderOpenID string
	SenderType   string
	MessageID    string
	RootID       string
	ParentID     string
	ChatID       string
	ChatType     string
	MessageType  string
	Content      string
	Mentions     json.RawMessage
	// CreateTime is the send time in milliseconds, as Feishu reports it.
	CreateTime string
}

func (r *feishuLocalReceiver) handleSDKMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) {
	if event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil {
		return
	}
	msg := feishuLocalMessage{}
	if event.Event.Sender.SenderId != nil {
		msg.SenderOpenID = larkString(event.Event.Sender.SenderId.OpenId)
	}
	msg.SenderType = larkString(event.Event.Sender.SenderType)
	m := event.Event.Message
	msg.MessageID = larkString(m.MessageId)
	msg.RootID = larkString(m.RootId)
	msg.ParentID = larkString(m.ParentId)
	msg.ChatID = larkString(m.ChatId)
	msg.ChatType = larkString(m.ChatType)
	msg.MessageType = larkString(m.MessageType)
	msg.Content = larkString(m.Content)
	msg.CreateTime = larkString(m.CreateTime)
	if m.Mentions != nil {
		if b, err := json.Marshal(m.Mentions); err == nil {
			msg.Mentions = b
		}
	}
	go r.handleMessage(ctx, msg)
}

func larkString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (r *feishuLocalReceiver) handleMessage(ctx context.Context, msg feishuLocalMessage) {
	if msg.SenderType == "app" || msg.SenderOpenID == "" {
		return
	}
	if msg.ChatType == "group" && !r.isBotMentioned(msg) {
		return
	}
	text := r.extractText(msg)
	if strings.TrimSpace(text) == "" {
		return
	}
	text = r.stripBotMention(text, msg)
	if connectorMessageIsStale(feishuMessageTime(msg.CreateTime), r.connectedAt) {
		log.Printf("[feishu-local] ignored a message from before the app was connected")
		return
	}
	if !r.senderAllowed(ctx, msg, text) {
		return
	}
	threadTS := msg.RootID
	if threadTS == "" {
		threadTS = msg.MessageID
	}
	threadKey := "feishu:" + r.creds.AppID + ":" + msg.SenderOpenID + ":" + msg.ChatID + ":" + threadTS
	unlock := r.acquireThreadLock(threadKey)
	defer unlock()

	reply := r.runAgent(ctx, threadKey, text, msg.ChatType)
	if strings.TrimSpace(reply) == "" {
		return
	}
	if err := r.app.authorizeInboundConnectorReply(ctx, "feishu", threadKey, threadKey); err != nil {
		log.Printf("[feishu-local] reply authority denied: %v", err)
		return
	}
	if err := r.poster.sendMessage(ctx, msg.ChatID, msg.MessageID, reply); err != nil {
		log.Printf("[feishu-local] send reply failed: %v", err)
	}
}

// feishuMessageTime reads a Feishu send time in milliseconds. An unreadable
// one gives the zero time, which connectorMessageIsStale refuses.
func feishuMessageTime(ms string) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(ms), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(n)
}

// senderAllowed accepts messages only from the paired owner. Until the app is
// paired, the only message accepted is a direct (p2p) message carrying the
// pairing code: it saves the sender as the owner and is answered with a
// confirmation, but starts no run.
func (r *feishuLocalReceiver) senderAllowed(ctx context.Context, msg feishuLocalMessage, text string) bool {
	r.ownerMu.Lock()
	if r.creds.paired() {
		owner := r.creds.OwnerID
		r.ownerMu.Unlock()
		if msg.SenderOpenID != owner {
			log.Printf("[feishu-local] refused a message from %s: only the paired owner, %s, may use this app", msg.SenderOpenID, owner)
			return false
		}
		return true
	}
	if msg.ChatType != "p2p" {
		r.ownerMu.Unlock()
		log.Printf("[feishu-local] ignored a %s message from %s: the app is not paired yet", msg.ChatType, msg.SenderOpenID)
		return false
	}
	if !connectorPairingMatches(r.creds.connectorOwnerState, text) {
		hint := !r.hinted[msg.SenderOpenID]
		r.hinted[msg.SenderOpenID] = true
		r.ownerMu.Unlock()
		log.Printf("[feishu-local] ignored a message from %s: the app is not paired and the message is not the pairing code", msg.SenderOpenID)
		if hint {
			r.notice(ctx, msg, "This app is not paired yet. Send the pairing code shown in Isoform's Feishu settings.")
		}
		return false
	}
	paired := r.creds
	paired.OwnerID = msg.SenderOpenID
	paired.PairingCode = ""
	paired.OwnerPaired = true
	if saved := loadFeishuLocalCredentials(normalizeFeishuBrand(r.creds.Brand)); saved == nil || saved.AppID != r.creds.AppID {
		r.ownerMu.Unlock()
		log.Printf("[feishu-local] pairing refused: the saved credentials are for another app")
		return false
	}
	if err := saveFeishuLocalCredentials(&paired); err != nil {
		r.ownerMu.Unlock()
		log.Printf("[feishu-local] could not save the paired owner: %v", err)
		r.notice(ctx, msg, "Isoform could not save the pairing. Try again.")
		return false
	}
	r.creds = paired
	r.ownerMu.Unlock()
	log.Printf("[feishu-local] paired with %s", msg.SenderOpenID)
	r.notice(ctx, msg, "Paired with Isoform. Only you can use this app now.")
	return false
}

// notice replies to the message a notice is about.
func (r *feishuLocalReceiver) notice(ctx context.Context, msg feishuLocalMessage, text string) {
	if err := r.poster.sendMessage(ctx, msg.ChatID, msg.MessageID, text); err != nil {
		log.Printf("[feishu-local] send notice failed: %v", err)
	}
}

func (r *feishuLocalReceiver) runAgent(ctx context.Context, threadKey, text, chatType string) string {
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
			"connector": "feishu",
			"transport": "local",
			"chat_type": chatType,
		})
	}
	r.app.mu.Lock()
	r.app.pendingTraceIDs[threadKey] = traceID
	r.app.mu.Unlock()
	defer schedulePendingTraceIDCleanup(r.app, threadKey, traceID)

	input := &agent.TaskInput{
		Prompt:           r.app.composePromptFor(text, personal, "", "feishu"),
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
		r.app.triggerMemory(memorySourceFeishu, threadKey, summary, roundCount)
		return withRefusalNote(lastContent, refusals, "Feishu")
	}
	if note := refusals.note("Feishu"); note != "" {
		return note
	}
	if err != nil {
		log.Printf("[feishu-local] agent error: %v", err)
		return "Sorry, I encountered an error processing your message."
	}
	return ""
}

func (r *feishuLocalReceiver) extractText(msg feishuLocalMessage) string {
	switch msg.MessageType {
	case "text":
		var content struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(msg.Content), &content); err != nil {
			return ""
		}
		return content.Text
	case "post":
		return extractFeishuPostText(msg.Content)
	case "file":
		var content struct {
			FileName string `json:"file_name"`
		}
		_ = json.Unmarshal([]byte(msg.Content), &content)
		if content.FileName != "" {
			return "[File: " + content.FileName + "]"
		}
		return "[File]"
	case "image":
		return "[Image]"
	default:
		return ""
	}
}

func extractFeishuPostText(raw string) string {
	var content struct {
		Title   string              `json:"title"`
		Content [][]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return ""
	}
	var sb strings.Builder
	if content.Title != "" {
		sb.WriteString(content.Title)
		sb.WriteString("\n")
	}
	for _, paragraph := range content.Content {
		for _, elemRaw := range paragraph {
			var elem struct {
				Tag      string `json:"tag"`
				Text     string `json:"text"`
				FileName string `json:"file_name"`
			}
			if err := json.Unmarshal(elemRaw, &elem); err != nil {
				continue
			}
			switch elem.Tag {
			case "text", "a", "code", "code_block":
				sb.WriteString(elem.Text)
			case "media":
				if elem.FileName != "" {
					sb.WriteString("[File: " + elem.FileName + "]")
				}
			case "img":
				sb.WriteString("[Image]")
			case "br":
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

func (r *feishuLocalReceiver) stripBotMention(text string, msg feishuLocalMessage) string {
	var mentions []struct {
		Key string `json:"key"`
		ID  struct {
			OpenID string `json:"open_id"`
		} `json:"id"`
	}
	if len(msg.Mentions) == 0 || json.Unmarshal(msg.Mentions, &mentions) != nil {
		return strings.TrimSpace(text)
	}
	for _, m := range mentions {
		if m.ID.OpenID == r.botOpenID && m.Key != "" {
			text = strings.ReplaceAll(text, m.Key, "")
		}
	}
	return strings.TrimSpace(text)
}

func (r *feishuLocalReceiver) isBotMentioned(msg feishuLocalMessage) bool {
	if r.botOpenID == "" || len(msg.Mentions) == 0 {
		return false
	}
	var mentions []struct {
		ID struct {
			OpenID string `json:"open_id"`
		} `json:"id"`
	}
	if err := json.Unmarshal(msg.Mentions, &mentions); err != nil {
		return false
	}
	for _, m := range mentions {
		if m.ID.OpenID == r.botOpenID {
			return true
		}
	}
	return false
}

type feishuLocalPoster struct {
	creds   feishuLocalCredentials
	baseURL string
	mu      sync.Mutex
	token   string
	expire  time.Time
}

func newFeishuLocalPoster(creds feishuLocalCredentials) *feishuLocalPoster {
	return &feishuLocalPoster{creds: creds, baseURL: resolveFeishuAPIBaseURL(creds.Brand)}
}

func (p *feishuLocalPoster) tenantAccessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	if p.token != "" && time.Now().Before(p.expire.Add(-2*time.Minute)) {
		tok := p.token
		p.mu.Unlock()
		return tok, nil
	}
	p.mu.Unlock()

	body := map[string]string{"app_id": p.creds.AppID, "app_secret": p.creds.AppSecret}
	var resp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := p.doJSON(ctx, http.MethodPost, "/open-apis/auth/v3/tenant_access_token/internal", "", body, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 || resp.TenantAccessToken == "" {
		return "", fmt.Errorf("tenant token error %d: %s", resp.Code, resp.Msg)
	}
	p.mu.Lock()
	p.token = resp.TenantAccessToken
	p.expire = time.Now().Add(time.Duration(regInt(resp.Expire, 7200)) * time.Second)
	p.mu.Unlock()
	return resp.TenantAccessToken, nil
}

//isoform:destination chat_bridges
func (p *feishuLocalPoster) doJSON(ctx context.Context, method, path, token string, body any, target any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Feishu API HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	if target != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, target)
	}
	return nil
}

func (p *feishuLocalPoster) sendMessage(ctx context.Context, chatID, replyToMessageID, text string) error {
	token, err := p.tenantAccessToken(ctx)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"text": truncateForFeishuLocal(text)})
	if replyToMessageID != "" {
		body := map[string]any{"msg_type": "text", "content": string(content)}
		return p.doJSON(ctx, http.MethodPost, "/open-apis/im/v1/messages/"+url.PathEscape(replyToMessageID)+"/reply", token, body, nil)
	}
	body := map[string]string{"receive_id": chatID, "msg_type": "text", "content": string(content)}
	return p.doJSON(ctx, http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=chat_id", token, body, nil)
}

func (p *feishuLocalPoster) sendDirectToUser(ctx context.Context, openID, text string) error {
	token, err := p.tenantAccessToken(ctx)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"text": truncateForFeishuLocal(text)})
	body := map[string]string{"receive_id": openID, "msg_type": "text", "content": string(content)}
	return p.doJSON(ctx, http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=open_id", token, body, nil)
}

func (p *feishuLocalPoster) getBotOpenID(ctx context.Context) string {
	token, err := p.tenantAccessToken(ctx)
	if err != nil {
		log.Printf("[feishu-local] get bot token failed: %v", err)
		return ""
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := p.doJSON(ctx, http.MethodGet, "/open-apis/bot/v3/info", token, nil, &resp); err != nil {
		log.Printf("[feishu-local] get bot info failed: %v", err)
		return ""
	}
	if resp.Code != 0 {
		log.Printf("[feishu-local] get bot info error %d: %s", resp.Code, resp.Msg)
		return ""
	}
	return resp.Bot.OpenID
}

func (p *feishuLocalPoster) getUserDisplayName(ctx context.Context, openID string) (string, error) {
	openID = strings.TrimSpace(openID)
	if openID == "" {
		return "", fmt.Errorf("empty Feishu open_id")
	}
	token, err := p.tenantAccessToken(ctx)
	if err != nil {
		return "", err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			User struct {
				Name     string `json:"name"`
				Nickname string `json:"nickname"`
				EnName   string `json:"en_name"`
			} `json:"user"`
		} `json:"data"`
	}
	path := "/open-apis/contact/v3/users/" + url.PathEscape(openID) + "?user_id_type=open_id"
	if err := p.doJSON(ctx, http.MethodGet, path, token, nil, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("get Feishu user error %d: %s", resp.Code, resp.Msg)
	}
	for _, name := range []string{resp.Data.User.Name, resp.Data.User.Nickname, resp.Data.User.EnName} {
		if name = strings.TrimSpace(name); name != "" {
			return name, nil
		}
	}
	return "", nil
}

// getBotAppName returns the bot application's display name. For scan-created
// PersonalAgent apps Feishu auto-names the app after the registrant
// ("<name>的智能助手"), so this embeds the user's display name and needs no
// contact permission — unlike getUserDisplayName, whose contact/v3/users lookup
// is gated by the app's contact data scope and often returns an empty name.
func (p *feishuLocalPoster) getBotAppName(ctx context.Context) (string, error) {
	token, err := p.tenantAccessToken(ctx)
	if err != nil {
		return "", err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			AppName string `json:"app_name"`
		} `json:"bot"`
	}
	if err := p.doJSON(ctx, http.MethodGet, "/open-apis/bot/v3/info", token, nil, &resp); err != nil {
		return "", err
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("get Feishu bot info error %d: %s", resp.Code, resp.Msg)
	}
	return strings.TrimSpace(resp.Bot.AppName), nil
}

func (p *feishuLocalPoster) listChats(ctx context.Context) ([]FeishuChatEntry, error) {
	token, err := p.tenantAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Items []struct {
				ChatID   string `json:"chat_id"`
				Name     string `json:"name"`
				ChatType string `json:"chat_type"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := p.doJSON(ctx, http.MethodGet, "/open-apis/im/v1/chats?page_size=100", token, nil, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("list chats error %d: %s", resp.Code, resp.Msg)
	}
	out := make([]FeishuChatEntry, 0, len(resp.Data.Items))
	for _, item := range resp.Data.Items {
		out = append(out, FeishuChatEntry{
			ChatID:   item.ChatID,
			Name:     item.Name,
			ChatType: item.ChatType,
		})
	}
	return out, nil
}

func truncateForFeishuLocal(text string) string {
	const maxRunes = 14000
	r := []rune(text)
	if len(r) <= maxRunes {
		return text
	}
	return string(r[:maxRunes]) + "\n\n[truncated]"
}
