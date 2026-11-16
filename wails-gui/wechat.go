package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"wails-gui/internal/privacy"
)

// WeChatStatus is the WeChat connection state returned to the frontend.
type WeChatStatus struct {
	Connected bool   `json:"connected"`
	BotID     string `json:"bot_id,omitempty"`
	Online    bool   `json:"online"` // true when monitor is actively polling
	// Paired and PairingCode describe the bot: until its owner sends the
	// pairing code, the bot answers nobody.
	Paired      bool   `json:"paired,omitempty"`
	PairingCode string `json:"pairing_code,omitempty"`
}

// WeChatQRCode is the QR code data returned to the frontend for display.
type WeChatQRCode struct {
	QRData string `json:"qr_data"` // URL to encode as QR code
	QRCode string `json:"qr_code"` // QR code identifier for polling
	Error  string `json:"error,omitempty"`
}

// WeChatLoginPollResult is the result of polling the QR code status.
type WeChatLoginPollResult struct {
	Status string `json:"status"` // "wait", "scaned", "confirmed", "expired", "error"
	Error  string `json:"error,omitempty"`
}

// GetWeChatStatus returns the current WeChat connection state.
func (a *App) GetWeChatStatus() WeChatStatus {
	creds := loadWeChatCredentials()
	if creds == nil {
		return WeChatStatus{}
	}

	a.mu.Lock()
	online := a.wechatMon != nil
	a.mu.Unlock()

	return WeChatStatus{
		Connected:   true,
		BotID:       creds.BotID,
		Online:      online,
		Paired:      creds.paired(),
		PairingCode: creds.PairingCode,
	}
}

// StartWeChatLogin initiates the QR code login flow. Returns QR data for
// the frontend to render as a QR code image.
func (a *App) StartWeChatLogin() WeChatQRCode {
	log.Printf("[wechat] StartWeChatLogin: fetching QR code from %s", wechatDefaultBaseURL)
	client := newWeChatLoginClient()
	qr, err := client.GetQRCode()
	if err != nil {
		log.Printf("[wechat] StartWeChatLogin error: %v", err)
		return WeChatQRCode{Error: err.Error()}
	}
	log.Printf("[wechat] StartWeChatLogin: got QR code (url=%d bytes, qrcode=%s)", len(qr.QRCodeImgContent), qr.QRCode)
	return WeChatQRCode{
		QRData: qr.QRCodeImgContent,
		QRCode: qr.QRCode,
	}
}

// PollWeChatLogin polls the QR code scan status. The frontend should call
// this repeatedly until status is "confirmed" or "expired".
func (a *App) PollWeChatLogin(qrcode string) WeChatLoginPollResult {
	client := newWeChatLoginClient()
	status, err := client.GetQRCodeStatus(qrcode)
	if err != nil {
		return WeChatLoginPollResult{Status: "error", Error: err.Error()}
	}

	if status.Status == "confirmed" {
		// Save credentials.
		creds := &wechatCredentials{
			BotToken:    status.BotToken,
			BaseURL:     status.BaseURL,
			BotID:       status.IlinkBotID,
			UserID:      status.IlinkUID,
			ConnectedAt: time.Now().Format(time.RFC3339),
		}
		// Logging in to the same bot again keeps an owner who paired with it.
		if previous := loadWeChatCredentials(); previous != nil && previous.BotID == creds.BotID && previous.paired() {
			creds.connectorOwnerState = connectorOwnerState{OwnerID: previous.OwnerID, OwnerName: previous.OwnerName, OwnerPaired: true}
		}
		if _, err := ensureConnectorPairing(&creds.connectorOwnerState); err != nil {
			log.Printf("[wechat] failed to create a pairing code: %v", err)
			return WeChatLoginPollResult{Status: "error", Error: "failed to create a pairing code"}
		}
		if err := saveWeChatCredentials(creds); err != nil {
			log.Printf("[wechat] failed to save credentials: %v", err)
			return WeChatLoginPollResult{Status: "error", Error: "failed to save credentials"}
		}

		// Persist config for auto-start.
		if err := updateConfig(func(c *yansuConfig) {
			c.WeChat = &wechatConfig{
				BotID:       creds.BotID,
				ConnectedAt: time.Now().Format(time.RFC3339),
			}
		}); err != nil {
			log.Printf("[wechat] failed to persist config: %v", err)
		}
		if err := a.bindIntegrationSelfIdentity(context.Background(), "wechat", creds.UserID, ""); err != nil {
			log.Printf("[wechat] failed to bind current user to People self: %v", err)
		}

		// Start monitor.
		go a.StartWeChatMonitor()
	}

	return WeChatLoginPollResult{Status: status.Status}
}

// ConnectWeChat starts the monitor if credentials exist, otherwise does nothing.
func (a *App) ConnectWeChat() {
	creds := loadWeChatCredentials()
	if creds == nil {
		log.Printf("[wechat] ConnectWeChat: no credentials")
		return
	}
	go a.StartWeChatMonitor()
}

// DisconnectWeChat stops the monitor and clears credentials.
func (a *App) DisconnectWeChat() error {
	dir, err := wechatStateDir()
	if err != nil {
		return err
	}
	a.StopWeChatMonitor()

	// Clear config.
	if err := updateConfig(func(c *yansuConfig) {
		c.WeChat = nil
	}); err != nil {
		return err
	}

	// Remove credential file.
	_ = os.Remove(filepath.Join(dir, "credentials.json"))

	// Remove cursor.
	_ = os.Remove(filepath.Join(dir, "cursor.json"))

	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Account, ID: "wechat"})
	return nil
}

// StartWeChatMonitor starts the WeChat long-polling monitor.
func (a *App) StartWeChatMonitor() error {
	creds := loadWeChatCredentials()
	if creds == nil {
		return fmt.Errorf("no WeChat credentials")
	}
	if err := a.bindIntegrationSelfIdentity(context.Background(), "wechat", creds.UserID, ""); err != nil {
		log.Printf("[wechat] failed to restore People self binding: %v", err)
	}
	if changed, err := ensureConnectorPairing(&creds.connectorOwnerState); err != nil {
		return fmt.Errorf("prepare WeChat pairing: %w", err)
	} else if changed {
		if err := saveWeChatCredentials(creds); err != nil {
			return fmt.Errorf("save WeChat pairing: %w", err)
		}
	}

	a.mu.Lock()
	existing := a.wechatMon
	if existing != nil {
		a.mu.Unlock()
		return nil // already running
	}
	mon := newWeChatMonitor(a, *creds)
	a.wechatMon = mon
	a.mu.Unlock()

	go mon.run()
	log.Printf("[wechat] monitor started for bot %s", creds.BotID)
	return nil
}

// StopWeChatMonitor stops the WeChat long-polling monitor.
func (a *App) StopWeChatMonitor() {
	a.mu.Lock()
	mon := a.wechatMon
	a.wechatMon = nil
	a.mu.Unlock()

	if mon != nil {
		mon.stop()
		log.Printf("[wechat] monitor stopped")
	}
}

// tryAutoStartWeChatMonitor attempts to start from persisted config on app launch.
func (a *App) tryAutoStartWeChatMonitor() {
	cfg := loadConfig()
	if cfg.WeChat == nil {
		return
	}

	creds := loadWeChatCredentials()
	if creds == nil {
		log.Printf("[wechat] auto-start skipped: no credentials")
		return
	}

	a.mu.Lock()
	if a.wechatMon != nil {
		a.mu.Unlock()
		return
	}
	mon := newWeChatMonitor(a, *creds)
	a.wechatMon = mon
	a.mu.Unlock()

	go mon.run()
	log.Printf("[wechat] auto-start launched for bot %s", creds.BotID)
	if err := a.bindIntegrationSelfIdentity(context.Background(), "wechat", creds.UserID, ""); err != nil {
		log.Printf("[wechat] failed to restore People self binding: %v", err)
	}
}

// --- Credential persistence ---

func saveWeChatCredentials(creds *wechatCredentials) error {
	dir, err := wechatStateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0600)
}

func loadWeChatCredentials() *wechatCredentials {
	dir, err := wechatStateDir()
	if err != nil {
		log.Printf("[wechat] state directory unavailable: %v", err)
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		return nil
	}
	var creds wechatCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil
	}
	if creds.BotToken == "" {
		return nil
	}
	return &creds
}
