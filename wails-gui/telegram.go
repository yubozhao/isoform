package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	runtime "github.com/wailsapp/wails/v2/pkg/runtime"

	"wails-gui/internal/privacy"
)

// TelegramStatus is the Telegram connection state returned to the frontend.
type TelegramStatus struct {
	Connected        bool   `json:"connected"`
	TelegramUserID   int64  `json:"telegram_user_id,omitempty"`
	TelegramUserName string `json:"telegram_user_name,omitempty"`
	DaemonOnline     bool   `json:"daemon_online"`
	// Paired and PairingCode describe a user-built bot: until its owner sends
	// the pairing code, the bot answers nobody.
	Paired      bool   `json:"paired,omitempty"`
	PairingCode string `json:"pairing_code,omitempty"`
}

// telegramBindingResponse mirrors the backend GET /api/telegram/binding JSON.
type telegramBindingResponse struct {
	BindingID        int    `json:"binding_id"`
	TelegramUserID   int64  `json:"telegram_user_id"`
	TelegramUserName string `json:"telegram_user_name"`
}

// TelegramGroupEntry represents an authorized Telegram group.
type TelegramGroupEntry struct {
	ID             int    `json:"id"`
	TelegramChatID int64  `json:"telegram_chat_id"`
	ChatTitle      string `json:"chat_title"`
}

func getTelegramBinding(baseURL, token string) (*telegramBindingResponse, error) {
	var resp telegramBindingResponse
	if err := apiGet(baseURL, token, "/api/telegram/binding", &resp); err != nil {
		return nil, err
	}
	if resp.TelegramUserID == 0 {
		return nil, nil
	}
	return &resp, nil
}

// GetTelegramStatus checks whether the current user has a Telegram binding on the backend
// and whether the local Telegram daemon WebSocket is connected.
func (a *App) GetTelegramStatus() TelegramStatus {
	if noYansuAccount() {
		return a.telegramLocalStatus()
	}
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return TelegramStatus{}
	}

	binding, err := getTelegramBinding(loadAPIBaseURL(), token)
	if err != nil || binding == nil {
		return TelegramStatus{}
	}

	a.mu.Lock()
	daemonOnline := a.telegramDaemon != nil && a.telegramDaemon.isConnected()
	a.mu.Unlock()

	return TelegramStatus{
		Connected:        true,
		TelegramUserID:   binding.TelegramUserID,
		TelegramUserName: binding.TelegramUserName,
		DaemonOnline:     daemonOnline,
	}
}

// ConnectTelegram re-uses an existing backend binding when possible, starting the daemon directly.
// When no binding exists, it generates a startgroup deep link and opens it in the browser,
// so the user can select a Telegram group — this binds their account and registers the group in one step.
func (a *App) ConnectTelegram() {
	token, err := loadYansuToken()
	if err == nil && token != "" {
		if binding, err := getTelegramBinding(loadAPIBaseURL(), token); err == nil && binding != nil {
			log.Printf("[telegram] ConnectTelegram: existing binding found (%d), starting daemon directly", binding.TelegramUserID)
			go a.StartTelegramDaemon()
			return
		}
	}

	// No binding — generate a one-time token and open the bot DM link so the user
	// can send /start <token> in private chat. The DM channel is registered as an
	// authorized chat on successful binding. Groups can be added separately via
	// AddTelegramGroup once the initial binding is established.
	result := a.GenerateTelegramBindToken()
	if result.Error != "" {
		log.Printf("[telegram] ConnectTelegram: failed to generate bind token: %s", result.Error)
		return
	}
	log.Printf("[telegram] ConnectTelegram: opening bot DM link for initial binding")
	runtime.BrowserOpenURL(a.ctx, result.BotLink)
}

// DisconnectTelegram stops the local Telegram daemon, deletes the backend binding
// (which also removes authorized groups server-side), and clears local config
// and scan state.
func (a *App) DisconnectTelegram() error {
	if noYansuAccount() {
		err := a.disconnectTelegramLocal()
		if err == nil {
			publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Account, ID: "telegram"})
		}
		return err
	}
	a.StopTelegramDaemon()
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Account, ID: "telegram"})

	token, _ := loadYansuToken()
	if token != "" {
		baseURL := loadAPIBaseURL()
		if delErr := apiDelete(baseURL, token, "/api/telegram/binding"); delErr != nil {
			log.Printf("[telegram] DisconnectTelegram: delete binding failed: %v", delErr)
		}
	}

	if err := updateConfig(func(c *yansuConfig) {
		c.Telegram = nil
	}); err != nil {
		return err
	}

	// Wipe local scan history and state so a future connect starts clean.
	go a.cleanupTelegramScanState()

	return nil
}

// cleanupTelegramScanState removes local scan history, cursors, and work files
// left over from a previous Telegram connection.
func (a *App) cleanupTelegramScanState() {
	if s := a.telegramScanner; s != nil {
		s.pollRW.Lock()
		defer s.pollRW.Unlock()
	}

	if err := os.RemoveAll(telegramDynamicStateDir()); err != nil {
		log.Printf("[telegram] cleanupTelegramScanState: failed to remove state dir: %v", err)
	}

	// Clear in-memory dedup map so the next scan doesn't skip conversations
	// that were processed under the old binding.
	if mc := a.getMemoryGenerator(); mc != nil {
		mc.clearTelegramConvs()
	}

	log.Printf("[telegram] scan state cleanup completed")
}

// TelegramBindTokenResult is the result of generating a Telegram bind token.
type TelegramBindTokenResult struct {
	BotLink   string `json:"bot_link"`   // ?start= link for DM binding
	GroupLink string `json:"group_link"` // ?startgroup= link for adding a group
	Error     string `json:"error,omitempty"`
}

// GenerateTelegramBindToken generates a one-time bind token and returns the startgroup deep link.
func (a *App) GenerateTelegramBindToken() TelegramBindTokenResult {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return TelegramBindTokenResult{Error: "not authenticated"}
	}

	var resp struct {
		BotLink   string `json:"bot_link"`
		GroupLink string `json:"group_link"`
	}
	if err := apiPost(loadAPIBaseURL(), token, "/api/telegram/bind-token", nil, &resp); err != nil {
		log.Printf("[telegram] GenerateTelegramBindToken: %v", err)
		return TelegramBindTokenResult{Error: err.Error()}
	}
	return TelegramBindTokenResult{BotLink: resp.BotLink, GroupLink: resp.GroupLink}
}

// GetTelegramGroups returns the authorized Telegram groups for the current user.
func (a *App) GetTelegramGroups() []TelegramGroupEntry {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return []TelegramGroupEntry{}
	}

	groups, err := apiGetList[TelegramGroupEntry](loadAPIBaseURL(), token, "/api/telegram/groups")
	if err != nil {
		log.Printf("[telegram] GetTelegramGroups: %v", err)
		return []TelegramGroupEntry{}
	}
	if groups == nil {
		return []TelegramGroupEntry{}
	}
	return groups
}

// AddTelegramGroup generates a new startgroup link and opens the browser so the user
// can add another Telegram group without going through the full connect flow.
func (a *App) AddTelegramGroup() {
	result := a.GenerateTelegramBindToken()
	if result.Error != "" {
		log.Printf("[telegram] AddTelegramGroup: %v", result.Error)
		return
	}
	runtime.BrowserOpenURL(a.ctx, result.GroupLink)
}

// DeleteTelegramGroup removes an authorized group for the current user.
func (a *App) DeleteTelegramGroup(chatID int64) error {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated")
	}
	if err := apiDelete(loadAPIBaseURL(), token, fmt.Sprintf("/api/telegram/groups/%d", chatID)); err != nil {
		return err
	}
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Account, ID: fmt.Sprintf("telegram:group:%d", chatID)})
	return nil
}

// StartTelegramDaemon discovers the Telegram binding from the backend and starts the daemon WebSocket.
func (a *App) StartTelegramDaemon() error {
	if noYansuAccount() {
		return a.startTelegramLocal()
	}
	token, err := loadYansuToken()
	if err != nil || token == "" {
		log.Printf("[telegram] StartTelegramDaemon skipped: not authenticated")
		return fmt.Errorf("not authenticated")
	}
	baseURL := loadAPIBaseURL()

	binding, err := getTelegramBinding(baseURL, token)
	if err != nil {
		log.Printf("[telegram] StartTelegramDaemon failed: binding lookup error (%v)", err)
		return fmt.Errorf("load Telegram binding: %w", err)
	}
	if binding == nil {
		log.Printf("[telegram] StartTelegramDaemon failed: no Telegram binding from backend")
		return fmt.Errorf("no Telegram binding")
	}

	telegramUserIDStr := strconv.FormatInt(binding.TelegramUserID, 10)

	// Persist the binding locally for auto-reconnect on restart.
	if err := updateConfig(func(c *yansuConfig) {
		c.Telegram = &telegramConfig{
			TelegramUserID:   telegramUserIDStr,
			TelegramUserName: binding.TelegramUserName,
			ConnectedAt:      time.Now().Format(time.RFC3339),
		}
	}); err != nil {
		log.Printf("[telegram] failed to persist Telegram binding: %v", err)
	}
	if err := a.bindIntegrationSelfIdentity(context.Background(), "telegram", telegramUserIDStr, binding.TelegramUserName); err != nil {
		log.Printf("[telegram] failed to bind current user to People self: %v", err)
	}

	a.mu.Lock()
	existing := a.telegramDaemon
	if existing != nil {
		sameBinding := existing.userID == telegramUserIDStr
		if sameBinding && existing.isConnected() {
			a.mu.Unlock()
			return nil // already running for current binding
		}
	}
	// Telegram has no workspace/team concept — teamID is empty, userID is the telegram_user_id.
	d := newDaemonClient(a, "telegram", baseURL, token, "", telegramUserIDStr)
	a.telegramDaemon = d
	a.mu.Unlock()

	if existing != nil {
		existing.stop()
	}

	go d.run()
	log.Printf("[telegram] StartTelegramDaemon launched for user %s via %s", telegramUserIDStr, baseURL)

	return nil
}

// StopTelegramDaemon stops the Telegram daemon WebSocket if running.
func (a *App) StopTelegramDaemon() {
	a.mu.Lock()
	d := a.telegramDaemon
	a.telegramDaemon = nil
	a.mu.Unlock()

	if d != nil {
		d.stop()
	}
}

// tryAutoStartTelegramDaemon attempts to start the Telegram daemon from persisted config.
// Called from startup() — errors are logged, not propagated.
func (a *App) tryAutoStartTelegramDaemon() {
	if noYansuAccount() {
		// Without an account the local bot receiver (direct to api.telegram.org) runs,
		// not the backend relay.
		a.tryAutoStartTelegramLocal()
		return
	}
	token, err := loadYansuToken()
	if err != nil || token == "" {
		log.Printf("[telegram] auto-start skipped: not authenticated")
		return
	}

	baseURL := loadAPIBaseURL()

	cfg := loadConfig()
	telegramUserID := ""
	telegramUserName := ""

	if cfg.Telegram != nil && cfg.Telegram.TelegramUserID != "" {
		telegramUserID = cfg.Telegram.TelegramUserID
		telegramUserName = cfg.Telegram.TelegramUserName
	} else {
		var binding *telegramBindingResponse
		var lastErr error
		for attempt := 1; attempt <= 5; attempt++ {
			if attempt > 1 {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			binding, lastErr = getTelegramBinding(baseURL, token)
			if lastErr != nil {
				continue
			}
			break
		}
		if lastErr != nil {
			log.Printf("[telegram] auto-start skipped: binding lookup failed after retries (%v)", lastErr)
			return
		}
		if binding == nil {
			log.Printf("[telegram] auto-start skipped: no persisted binding and no backend binding")
			return
		}
		telegramUserID = strconv.FormatInt(binding.TelegramUserID, 10)
		telegramUserName = binding.TelegramUserName

		if err := updateConfig(func(c *yansuConfig) {
			c.Telegram = &telegramConfig{
				TelegramUserID:   telegramUserID,
				TelegramUserName: binding.TelegramUserName,
				ConnectedAt:      time.Now().Format(time.RFC3339),
			}
		}); err != nil {
			log.Printf("[telegram] auto-start: failed to persist recovered Telegram binding: %v", err)
		}
		log.Printf("[telegram] auto-start recovered binding %s from backend", telegramUserID)
	}

	d := newDaemonClient(a, "telegram", baseURL, token, "", telegramUserID)

	a.mu.Lock()
	if a.telegramDaemon != nil {
		a.mu.Unlock()
		log.Printf("[telegram] auto-start skipped: daemon already exists")
		return
	}
	a.telegramDaemon = d
	a.mu.Unlock()

	go d.run()
	log.Printf("[telegram] auto-start launched for user %s via %s", telegramUserID, baseURL)
	if err := a.bindIntegrationSelfIdentity(context.Background(), "telegram", telegramUserID, telegramUserName); err != nil {
		log.Printf("[telegram] failed to restore People self binding: %v", err)
	}
}

// handleURLOpen is called by the macOS URL scheme handler when the app is opened
// via a yansu:// deep link (e.g. after Telegram bind completes).
func (a *App) handleURLOpen(url string) {
	log.Printf("[app] URL open: %s", url)
	switch {
	case strings.HasPrefix(url, "yansu://telegram/bound"):
		a.ShowWindow()
		a.emitBrowserEvent("telegram:bound")
	}
}
