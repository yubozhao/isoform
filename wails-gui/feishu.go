package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wails-gui/internal/privacy"
)

// FeishuStatus is the Feishu connection state returned to the frontend.
type FeishuStatus struct {
	Connected       bool                   `json:"connected"`
	AppID           string                 `json:"app_id,omitempty"`
	BotName         string                 `json:"bot_name,omitempty"`
	FeishuUserName  string                 `json:"feishu_user_name,omitempty"`
	FeishuAccountID string                 `json:"feishu_account_id,omitempty"`
	DaemonOnline    bool                   `json:"daemon_online"`
	FullPermissions *FeishuPermissionCheck `json:"full_permissions,omitempty"`
	// PermissionReady mirrors the persisted credential flag so the frontend can
	// keep the "enable full access" panel hidden after a reload without
	// re-running the permission probe.
	PermissionReady bool `json:"permission_ready"`
	// Paired and PairingCode describe a local app: until its owner sends the
	// pairing code in a direct message, the app answers nobody.
	Paired      bool   `json:"paired,omitempty"`
	PairingCode string `json:"pairing_code,omitempty"`
}

// FeishuReceiverStatus reports whether the local Feishu persistent receiver is online.
type FeishuReceiverStatus struct {
	Configured bool   `json:"configured"`
	AppID      string `json:"app_id,omitempty"`
	Started    bool   `json:"started"`
	Connected  bool   `json:"connected"`
	LastError  string `json:"last_error,omitempty"`
	Status     string `json:"status"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// feishuBindingResponse mirrors the backend GET /api/daemon/me/feishu-binding JSON.
type feishuBindingResponse struct {
	BindingID      int    `json:"binding_id"`
	AppID          string `json:"app_id"`
	AppName        string `json:"app_name"`
	FeishuOpenID   string `json:"feishu_open_id"`
	FeishuUserName string `json:"feishu_user_name"`
	Status         string `json:"status"`
}

// getActiveFeishuBinding calls GET /api/daemon/me/feishu-binding which filters
// by active app status. Returns (nil, nil) when no active binding exists (HTTP 404).
// Returns (nil, err) on network/server errors — callers must not clear state on error.
func getActiveFeishuBinding(baseURL, token string) (*feishuBindingResponse, error) {
	var resp feishuBindingResponse
	if err := apiGet(baseURL, token, "/api/daemon/me/feishu-binding", &resp); err != nil {
		if isAPINotFound(err) {
			return nil, nil // no active binding
		}
		return nil, err
	}
	return &resp, nil
}

// GetFeishuStatus checks local credentials and receiver state for the given
// brand ("feishu" or "lark").
func (a *App) GetFeishuStatus(brand string) FeishuStatus {
	brand = normalizeFeishuBrand(brand)
	creds := loadFeishuLocalCredentials(brand)
	if creds == nil {
		return FeishuStatus{}
	}

	a.mu.Lock()
	online := a.imLocal[brand] != nil
	a.mu.Unlock()

	// Backfill the bot's display name once so the card can identify the bot
	// rather than the human registrant. Guarded + persisted, so this only fetches
	// while BotName is still empty.
	if strings.TrimSpace(creds.BotName) == "" {
		go a.refreshStoredFeishuBotName(brand)
	}

	return FeishuStatus{
		Connected:       true,
		AppID:           creds.AppID,
		BotName:         creds.BotName,
		FeishuUserName:  creds.UserName,
		FeishuAccountID: maskFeishuAccountID(creds.OpenID),
		DaemonOnline:    online,
		PermissionReady: creds.PermissionsReady,
		Paired:          creds.paired(),
		PairingCode:     creds.PairingCode,
	}
}

// RefreshFeishuPermissions probes newly granted Feishu permissions and returns
// the latest local connection status.
func (a *App) RefreshFeishuPermissions(brand string) (FeishuStatus, error) {
	brand = normalizeFeishuBrand(brand)
	creds := loadFeishuLocalCredentials(brand)
	if creds == nil {
		return FeishuStatus{}, fmt.Errorf("no local Feishu credentials")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	check, err := newFeishuLocalPoster(*creds).checkFullPermissions(ctx)
	if err != nil {
		return FeishuStatus{}, err
	}

	if check.Ready && !creds.PermissionsReady {
		creds.PermissionsReady = true
		if err := saveFeishuLocalCredentials(creds); err != nil {
			log.Printf("[feishu-local] failed to persist permission-ready flag: %v", err)
		}
	}

	go a.refreshStoredFeishuIdentity(*creds)

	status := a.GetFeishuStatus(brand)
	status.FullPermissions = check
	status.PermissionReady = creds.PermissionsReady
	return status, nil
}

func maskFeishuAccountID(openID string) string {
	openID = strings.TrimSpace(openID)
	const visibleSuffix = 6
	if len(openID) <= visibleSuffix {
		return openID
	}
	prefix := ""
	if i := strings.Index(openID, "_"); i >= 0 {
		prefix = openID[:i+1]
	}
	return prefix + "…" + openID[len(openID)-visibleSuffix:]
}

// GetFeishuReceiverStatus returns the local Feishu receiver runtime state.
func (a *App) GetFeishuReceiverStatus(brand string) (*FeishuReceiverStatus, error) {
	brand = normalizeFeishuBrand(brand)
	creds := loadFeishuLocalCredentials(brand)
	a.mu.Lock()
	connected := a.imLocal[brand] != nil
	a.mu.Unlock()
	status := "not_configured"
	appID := ""
	if creds != nil {
		appID = creds.AppID
		if connected {
			status = "connected"
		} else {
			status = "saved"
		}
	}
	return &FeishuReceiverStatus{
		Configured: creds != nil,
		AppID:      appID,
		Started:    connected,
		Connected:  connected,
		Status:     status,
		UpdatedAt:  time.Now().Format(time.RFC3339),
	}, nil
}

// RestartFeishuReceiver restarts the local WS receiver for the given brand.
func (a *App) RestartFeishuReceiver(brand string) error {
	brand = normalizeFeishuBrand(brand)
	a.StopFeishuDaemon(brand)
	return a.StartFeishuDaemon(brand)
}

// FeishuRegistration is the QR payload for the scan-to-create-app flow.
type FeishuRegistration struct {
	SessionID               string `json:"session_id"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	VerificationURI         string `json:"verification_uri"`
	UserCode                string `json:"user_code"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartFeishuRegistration begins the scan-to-create-app flow and returns the QR payload.
// The user scans verification_uri_complete in Feishu/Lark; Feishu creates a PersonalAgent
// app and the desktop app stores its credentials locally on poll success.
func (a *App) StartFeishuRegistration(brand string) (*FeishuRegistration, error) {
	brand = normalizeFeishuBrand(brand)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reg, sess, err := beginFeishuLocalRegistration(ctx, brand)
	if err != nil {
		return nil, err
	}
	feishuRegistrationSessions.Store(sess.SessionID, sess)
	return reg, nil
}

// PollFeishuRegistration polls the registration status once. Returns the status string:
// "pending" | "connected" | "expired" | "error".
func (a *App) PollFeishuRegistration(sessionID string) (string, error) {
	v, ok := feishuRegistrationSessions.Load(sessionID)
	if !ok {
		return "expired", nil
	}
	sess, _ := v.(*feishuRegistrationSession)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	creds, status, err := pollFeishuLocalRegistration(ctx, sess)
	if err != nil {
		return "", err
	}
	switch status {
	case "connected":
		feishuRegistrationSessions.Delete(sessionID)
		if creds != nil {
			enrichCtx, enrichCancel := context.WithTimeout(ctx, 15*time.Second)
			a.enrichFeishuIdentity(enrichCtx, creds)
			enrichCancel()
			if err := a.bindFeishuSelfIdentity(ctx, creds); err != nil {
				log.Printf("[feishu-local] failed to bind current user to People self: %v", err)
			}
			if err := saveFeishuLocalCredentials(creds); err != nil {
				return "error", err
			}
			// A freshly created app supersedes any previously archived one.
			removeArchivedFeishuLocalCredentials(creds.Brand)
			if err := updateConfig(func(c *yansuConfig) {
				setFeishuBrandConfig(c, creds.Brand, &feishuConfig{
					AppID:          creds.AppID,
					FeishuOpenID:   creds.OpenID,
					FeishuUserName: creds.UserName,
					ConnectedAt:    creds.ConnectedAt,
				})
			}); err != nil {
				log.Printf("[feishu-local] failed to persist config: %v", err)
			}
			brand := creds.Brand
			go func() {
				if err := a.StartFeishuDaemon(brand); err != nil {
					log.Printf("[feishu-local] start receiver after registration failed: %v", err)
				}
			}()
		}
	case "expired", "error":
		feishuRegistrationSessions.Delete(sessionID)
	}
	return status, nil
}

// SendFeishuMessage posts a proactive message to a Feishu chat on behalf of the
// authenticated user. chatID is required; replyToMessageID may be empty to post a
// new top-level message rather than a thread reply.
func (a *App) SendFeishuMessage(chatID, replyToMessageID, text string) error {
	creds := loadFeishuLocalCredentials("feishu")
	if creds == nil {
		return fmt.Errorf("no local Feishu credentials")
	}
	return newFeishuLocalPoster(*creds).sendMessage(context.Background(), chatID, replyToMessageID, text)
}

// DisconnectFeishu stops the local receiver for the given brand and clears its
// local config.
func (a *App) DisconnectFeishu(brand string) error {
	brand = normalizeFeishuBrand(brand)
	// Make the disconnect authoritative immediately: archive + delete the local
	// credentials and clear config BEFORE stopping the daemon. StopFeishuDaemon
	// can block while the receiver's WebSocket shuts down, and doing it first
	// left a window where GetFeishuStatus still saw the credentials and reported
	// "connected" (e.g. after navigating away and back mid-disconnect).
	//
	// Keep a copy of the credentials so the user can reuse this same app on a
	// future reconnect instead of provisioning a brand-new one.
	archiveFeishuLocalCredentials(brand)
	removeFeishuLocalCredentials(brand)
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Account, ID: "feishu:" + brand})

	if err := updateConfig(func(c *yansuConfig) {
		setFeishuBrandConfig(c, brand, nil)
		if brand == "feishu" {
			c.FeishuScanChatFilter = nil
		}
	}); err != nil {
		return err
	}

	a.StopFeishuDaemon(brand)

	// Drop the lark-cli profile for this brand so the agent no longer sees it.
	go larkCliRemoveProfile(brand)

	// Wipe local scan history and state so a future connect starts clean.
	// Only Feishu runs the incremental scanner/pipeline today.
	if brand == "feishu" {
		go a.cleanupFeishuScanState()
	}

	return nil
}

// FeishuArchivedApp describes a previously connected Feishu app retained after
// disconnect so the user can reuse it instead of scanning to create a new one.
type FeishuArchivedApp struct {
	AppID           string `json:"app_id"`
	FeishuUserName  string `json:"feishu_user_name,omitempty"`
	FeishuAccountID string `json:"feishu_account_id,omitempty"`
	Brand           string `json:"brand,omitempty"`
	ConnectedAt     string `json:"connected_at,omitempty"`
}

// GetArchivedFeishuApp returns the most recently disconnected Feishu app that can
// be reused, or nil when none is available or an app is already connected.
func (a *App) GetArchivedFeishuApp(brand string) *FeishuArchivedApp {
	brand = normalizeFeishuBrand(brand)
	if loadFeishuLocalCredentials(brand) != nil {
		return nil // already connected — nothing to reuse
	}
	creds := loadArchivedFeishuLocalCredentials(brand)
	if creds == nil {
		return nil
	}
	return &FeishuArchivedApp{
		AppID:           creds.AppID,
		FeishuUserName:  creds.UserName,
		FeishuAccountID: maskFeishuAccountID(creds.OpenID),
		Brand:           creds.Brand,
		ConnectedAt:     creds.ConnectedAt,
	}
}

// ReuseArchivedFeishuApp restores the archived credentials as active, validates
// the app still exists in Feishu, rebinds the People self identity, and starts
// the receiver. Returns an error when there is nothing to reuse or the app is no
// longer valid (e.g. deleted in the Feishu developer console).
func (a *App) ReuseArchivedFeishuApp(brand string) error {
	brand = normalizeFeishuBrand(brand)
	creds := loadArchivedFeishuLocalCredentials(brand)
	if creds == nil {
		return fmt.Errorf("no archived Feishu app to reuse")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := newFeishuLocalPoster(*creds).tenantAccessToken(ctx); err != nil {
		return fmt.Errorf("archived Feishu app is no longer valid: %w", err)
	}

	if err := saveFeishuLocalCredentials(creds); err != nil {
		return err
	}
	removeArchivedFeishuLocalCredentials(brand)
	if err := updateConfig(func(c *yansuConfig) {
		setFeishuBrandConfig(c, creds.Brand, &feishuConfig{
			AppID:          creds.AppID,
			FeishuOpenID:   creds.OpenID,
			FeishuUserName: creds.UserName,
			ConnectedAt:    creds.ConnectedAt,
		})
	}); err != nil {
		log.Printf("[feishu-local] reuse: failed to persist config: %v", err)
	}
	if err := a.bindFeishuSelfIdentity(context.Background(), creds); err != nil {
		log.Printf("[feishu-local] reuse: failed to bind People self: %v", err)
	}
	return a.StartFeishuDaemon(creds.Brand)
}

// cleanupFeishuScanState removes local scan history, cursors, and work files
// left over from a previous Feishu connection.
func (a *App) cleanupFeishuScanState() {
	if s := a.feishuScanner; s != nil {
		s.pollRW.Lock()
		defer s.pollRW.Unlock()
	}

	if err := os.RemoveAll(feishuDynamicStateDir()); err != nil {
		log.Printf("[feishu] cleanupFeishuScanState: failed to remove dynamic state dir: %v", err)
	}

	if dataRoot, _ := flavorDataDir(); dataRoot != "" {
		scanDir := filepath.Join(dataRoot, feishuScanDirName)
		if err := os.Remove(filepath.Join(scanDir, "known_processed.json")); err != nil && !os.IsNotExist(err) {
			log.Printf("[feishu] cleanupFeishuScanState: failed to remove known_processed.json: %v", err)
		}
		if err := os.Remove(filepath.Join(scanDir, "known_automations.json")); err != nil && !os.IsNotExist(err) {
			log.Printf("[feishu] cleanupFeishuScanState: failed to remove known_automations.json: %v", err)
		}
		if err := os.RemoveAll(filepath.Join(scanDir, "conversations")); err != nil {
			log.Printf("[feishu] cleanupFeishuScanState: failed to remove conversations dir: %v", err)
		}
	}

	// Clear in-memory dedup map so evaluateFeishuNow doesn't skip
	// conversations that were processed under the old binding.
	if mc := a.getMemoryGenerator(); mc != nil {
		mc.clearFeishuConvs()
	}

	log.Printf("[feishu] scan state cleanup completed")
}

// setFeishuBrandConfig stores (or clears, when cfg is nil) the per-brand local
// connection config in the persisted config.
func setFeishuBrandConfig(c *yansuConfig, brand string, cfg *feishuConfig) {
	if normalizeFeishuBrand(brand) == "lark" {
		c.Lark = cfg
		return
	}
	c.Feishu = cfg
}

// feishuBrandConfig returns the persisted per-brand local connection config.
func feishuBrandConfig(c *yansuConfig, brand string) *feishuConfig {
	if normalizeFeishuBrand(brand) == "lark" {
		return c.Lark
	}
	return c.Feishu
}

// StartFeishuDaemon starts the local WebSocket receiver for the given brand from
// its local credentials.
func (a *App) StartFeishuDaemon(brand string) error {
	brand = normalizeFeishuBrand(brand)
	creds := loadFeishuLocalCredentials(brand)
	if creds == nil {
		return fmt.Errorf("no local Feishu credentials")
	}
	if changed, err := ensureConnectorPairing(&creds.connectorOwnerState); err != nil {
		return fmt.Errorf("prepare Feishu pairing: %w", err)
	} else if changed {
		if err := saveFeishuLocalCredentials(creds); err != nil {
			return fmt.Errorf("save Feishu pairing: %w", err)
		}
	}
	if err := a.bindFeishuSelfIdentity(context.Background(), creds); err != nil {
		log.Printf("[feishu-local] failed to restore People self binding: %v", err)
	}

	a.mu.Lock()
	existing := a.imLocal[brand]
	if existing != nil && existing.creds.AppID == creds.AppID {
		a.mu.Unlock()
		go a.refreshStoredFeishuIdentity(*creds)
		return nil
	}
	r := newFeishuLocalReceiver(a, *creds)
	a.imLocal[brand] = r
	a.mu.Unlock()

	if existing != nil {
		existing.stop()
	}

	// Register/refresh the lark-cli profile for this brand so the agent can
	// operate the account. Only on an actual (re)start — the idempotent fast
	// path above returns before this. Runs on connect, reuse, and auto-start.
	go larkCliConfigure(creds.Brand, creds.AppID, creds.AppSecret)

	go r.run()
	go a.refreshStoredFeishuIdentity(*creds)
	log.Printf("[feishu-local] %s receiver launched for %s:%s", brand, creds.AppID, creds.OpenID)
	return nil
}

// StopFeishuDaemon stops the local receiver for the given brand. The legacy
// daemon/watchdog only applies to the Feishu brand.
func (a *App) StopFeishuDaemon(brand string) {
	brand = normalizeFeishuBrand(brand)
	a.mu.Lock()
	r := a.imLocal[brand]
	delete(a.imLocal, brand)
	var d *daemonClient
	var watchdogCancel context.CancelFunc
	if brand == "feishu" {
		d = a.feishuDaemon
		a.feishuDaemon = nil
		watchdogCancel = a.feishuWatchdogCancel
		a.feishuWatchdogCancel = nil
	}
	a.mu.Unlock()

	if watchdogCancel != nil {
		watchdogCancel()
	}
	if d != nil {
		d.stop()
	}
	if r != nil {
		r.stop()
	}
}

// tryAutoStartFeishuDaemon attempts to start the local receivers for every brand
// with persisted credentials. Called from startup() — errors are logged.
func (a *App) tryAutoStartFeishuDaemon() {
	for _, brand := range []string{"feishu", "lark"} {
		if loadFeishuLocalCredentials(brand) == nil {
			continue
		}
		if err := a.StartFeishuDaemon(brand); err != nil {
			log.Printf("[feishu-local] %s auto-start failed: %v", brand, err)
		}
	}
}

// ── Feishu chat list and scan filter bindings ────────────────────────

// FeishuChatEntry is a chat descriptor returned to the frontend.
type FeishuChatEntry struct {
	ChatID   string `json:"chat_id"`
	Name     string `json:"name"`
	ChatType string `json:"chat_type"`
}

// FeishuChatsResult wraps the chat list with an optional error/warning for the frontend.
type FeishuChatsResult struct {
	Chats   []FeishuChatEntry `json:"chats"`
	Error   string            `json:"error,omitempty"`
	Warning string            `json:"warning,omitempty"`
}

// GetFeishuChats returns the list of chats the local Feishu bot has access to.
func (a *App) GetFeishuChats() FeishuChatsResult {
	creds := loadFeishuLocalCredentials("feishu")
	if creds == nil {
		return FeishuChatsResult{Chats: []FeishuChatEntry{}, Error: "No local Feishu connection"}
	}
	chats, err := newFeishuLocalPoster(*creds).listChats(context.Background())
	if err != nil {
		log.Printf("[feishu-local] GetFeishuChats failed: %v", err)
		return FeishuChatsResult{Chats: []FeishuChatEntry{}, Error: "Failed to load chats"}
	}
	return FeishuChatsResult{Chats: chats}
}

// GetFeishuScanChatFilter returns the configured chat filter.
// An empty/nil slice means "scan all chats".
func (a *App) GetFeishuScanChatFilter() []string {
	cfg := loadConfig()
	return cfg.FeishuScanChatFilter
}

// SetFeishuScanChatFilter persists a chat filter to config.json.
// Pass nil or empty to clear the filter (scan all).
func (a *App) SetFeishuScanChatFilter(chatIDs []string) {
	_ = updateConfig(func(c *yansuConfig) {
		c.FeishuScanChatFilter = chatIDs
	})
}

// ── Feishu scan pipeline bindings ────────────────────────────────────

// RunFeishuScanPipeline triggers the Feishu incremental scanner immediately.
// Kept for backward Wails binding compatibility; frontend migrated to
// GetFeishuDynamicScanHistory in Phase 6.
func (a *App) RunFeishuScanPipeline() error {
	if !furnaceScanEnabled {
		return nil
	}
	log.Printf("[feishu-local] RunFeishuScanPipeline skipped: local Feishu history sync is not implemented yet")
	return nil
}

// RescanFeishu clears all Feishu scan state and triggers a fresh full scan.
// Mirrors RescanSlack: cancels in-flight extraction, clears cursors + processed
// convs, removes shared conversations, and triggers an immediate poll.
func (a *App) RescanFeishu() error {
	if !furnaceScanEnabled {
		return nil
	}
	log.Printf("[feishu-local] RescanFeishu skipped: local Feishu history sync is not implemented yet")
	return nil
}

// sendFeishuDirectMessage sends a local Feishu DM to openID.
// This is an internal helper (not Wails-bound).
func (a *App) sendFeishuDirectMessage(appID, openID, text string) error {
	creds := loadFeishuLocalCredentials("feishu")
	if creds == nil {
		return fmt.Errorf("no local Feishu credentials")
	}
	if appID != "" && appID != creds.AppID {
		return fmt.Errorf("local Feishu app mismatch")
	}
	return newFeishuLocalPoster(*creds).sendDirectToUser(context.Background(), openID, text)
}
