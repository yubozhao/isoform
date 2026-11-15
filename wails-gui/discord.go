package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// DiscordStatus is the Discord connection state returned to the frontend.
type DiscordStatus struct {
	Bound         bool           `json:"bound"`
	DiscordUser   string         `json:"discord_user,omitempty"`
	DiscordUserID string         `json:"discord_user_id,omitempty"`
	Guilds        []DiscordGuild `json:"guilds"`
	DaemonOnline  bool           `json:"daemon_online"`
	// Paired and PairingCode describe a local bot: until its owner sends the
	// pairing code in a direct message, the bot answers nobody.
	Paired      bool   `json:"paired,omitempty"`
	PairingCode string `json:"pairing_code,omitempty"`
}

// DiscordGuild represents an authorized Discord server.
type DiscordGuild struct {
	GuildID   string `json:"guild_id"`
	GuildName string `json:"guild_name"`
	CreatedAt string `json:"created_at,omitempty"`
}

// discordBindingResponse mirrors the backend GET /api/discord/bindings JSON.
type discordBindingResponse struct {
	Bound           bool   `json:"bound"`
	DiscordUserID   string `json:"discord_user_id"`
	DiscordUsername string `json:"discord_username"`
	CreatedAt       string `json:"created_at"`
}

// GetDiscordStatus checks whether the current user has a Discord binding and
// whether the local daemon is connected.
func (a *App) GetDiscordStatus() DiscordStatus {
	if noYansuAccount() {
		return a.discordLocalStatus()
	}
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return DiscordStatus{Guilds: []DiscordGuild{}}
	}

	var binding discordBindingResponse
	if err := apiGet(loadAPIBaseURL(), token, "/api/discord/bindings", &binding); err != nil || !binding.Bound {
		return DiscordStatus{Guilds: []DiscordGuild{}}
	}

	// Fetch authorized guilds.
	var guilds []DiscordGuild
	_ = apiGet(loadAPIBaseURL(), token, "/api/discord/guilds", &guilds)
	if guilds == nil {
		guilds = []DiscordGuild{}
	}

	a.mu.Lock()
	daemonOnline := a.discordDaemon != nil && a.discordDaemon.isConnected()
	a.mu.Unlock()

	return DiscordStatus{
		Bound:         true,
		DiscordUser:   binding.DiscordUsername,
		DiscordUserID: binding.DiscordUserID,
		Guilds:        guilds,
		DaemonOnline:  daemonOnline,
	}
}

// ConnectDiscord opens the unified Discord OAuth flow (identify + guilds).
// The backend detects the user's situation and either auto-binds or prompts for bot installation.
func (a *App) ConnectDiscord() error {
	baseURL := loadAPIBaseURL()
	wailsRuntime.BrowserOpenURL(a.ctx, baseURL+"/discord/connect")
	return nil
}

// InstallDiscordBot opens the Discord OAuth bot install flow (admin only).
// Called after ConnectDiscord detects the user is an admin and bot needs installation.
func (a *App) InstallDiscordBot() error {
	baseURL := loadAPIBaseURL()
	wailsRuntime.BrowserOpenURL(a.ctx, baseURL+"/discord/install")
	return nil
}

// UnbindDiscordAccount removes the Discord binding and stops the daemon.
func (a *App) UnbindDiscordAccount() error {
	if noYansuAccount() {
		err := a.disconnectDiscordLocal()
		if err == nil {
			publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalAccount, ID: "discord"})
		}
		return err
	}
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated")
	}

	if err := apiDelete(loadAPIBaseURL(), token, "/api/discord/bindings"); err != nil {
		return fmt.Errorf("failed to delete Discord binding: %w", err)
	}
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalAccount, ID: "discord"})

	a.StopDiscordDaemon()

	// Clear persisted Discord config.
	if err := updateConfig(func(c *yansuConfig) {
		c.Discord = nil
	}); err != nil {
		return err
	}

	// Clear incremental scan state so fresh scan runs on next connect.
	go a.cleanupDiscordScanState()

	return nil
}

// DisconnectDiscordGuild removes a single guild authorization.
func (a *App) DisconnectDiscordGuild(guildID string) error {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated")
	}

	if err := apiDelete(loadAPIBaseURL(), token, "/api/discord/guilds/"+guildID); err != nil {
		return fmt.Errorf("failed to disconnect guild: %w", err)
	}
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalAccount, ID: "discord:guild:" + guildID})
	return nil
}

// StartDiscordDaemon starts the Discord daemon WebSocket connection.
func (a *App) StartDiscordDaemon() error {
	if noYansuAccount() {
		return a.startDiscordLocal()
	}
	return a.startDiscordDaemon()
}

// startDiscordDaemon is the internal daemon launcher.
func (a *App) startDiscordDaemon() error {
	status := a.GetDiscordStatus()
	if !status.Bound {
		return fmt.Errorf("no Discord binding")
	}
	if err := a.bindIntegrationSelfIdentity(context.Background(), "discord", status.DiscordUserID, status.DiscordUser); err != nil {
		log.Printf("[discord] failed to bind current user to People self: %v", err)
	}

	token, err := loadYansuToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated")
	}
	baseURL := loadAPIBaseURL()

	a.mu.Lock()
	existing := a.discordDaemon
	if existing != nil && existing.isConnected() {
		a.mu.Unlock()
		return nil // already running
	}

	// Discord routing uses discord_user_id only (no team_id equivalent).
	d := newDaemonClient(a, "discord", baseURL, token, "", status.DiscordUserID)
	a.discordDaemon = d
	a.mu.Unlock()

	if existing != nil {
		existing.stop()
	}

	go d.run()
	log.Printf("[discord] startDiscordDaemon launched for %s via %s", status.DiscordUserID, baseURL)
	return nil
}

// StopDiscordDaemon stops the Discord daemon WebSocket connection.
func (a *App) StopDiscordDaemon() {
	a.mu.Lock()
	d := a.discordDaemon
	a.discordDaemon = nil
	a.mu.Unlock()

	if d != nil {
		d.stop()
	}
}

// cleanupDiscordScanState wipes scan state left from a previous Discord binding.
func (a *App) cleanupDiscordScanState() {
	if s := a.discordScanner; s != nil {
		s.pollRW.Lock()
		defer s.pollRW.Unlock()
	}

	stateDir, err := discordDynamicStateDir()
	if err != nil {
		log.Printf("[discord] resolve scan state: %v", err)
		return
	}
	scanDir, err := a.discordScanDir()
	if err != nil {
		log.Printf("[discord] resolve scan data: %v", err)
		return
	}
	if err := os.RemoveAll(stateDir); err != nil {
		log.Printf("[discord] cleanupDiscordScanState: failed to remove dynamic state dir: %v", err)
	}

	if err := os.Remove(filepath.Join(scanDir, "known_processed.json")); err != nil && !os.IsNotExist(err) {
		log.Printf("[discord] cleanupDiscordScanState: failed to remove known_processed.json: %v", err)
	}
	if err := os.Remove(filepath.Join(scanDir, "known_automations.json")); err != nil && !os.IsNotExist(err) {
		log.Printf("[discord] cleanupDiscordScanState: failed to remove known_automations.json: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(scanDir, "conversations")); err != nil {
		log.Printf("[discord] cleanupDiscordScanState: failed to remove conversations dir: %v", err)
	}

	// Clear in-memory dedup map so the next scan doesn't skip conversations
	// that were processed under the old binding.
	if mc := a.getMemoryGenerator(); mc != nil {
		mc.clearDiscordConvs()
	}

	log.Printf("[discord] scan state cleanup completed")
}

// SendDiscordMessage posts a message to a Discord channel via the backend.
func (a *App) SendDiscordMessage(channelID, text string) error {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated")
	}
	return apiPost(loadAPIBaseURL(), token, "/api/discord/send", map[string]string{
		"channel_id": channelID,
		"text":       text,
	}, nil)
}

// SendDiscordDM opens a DM channel with the given Discord user and sends a text message.
func (a *App) SendDiscordDM(userID, text string) error {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated")
	}
	return apiPost(loadAPIBaseURL(), token, "/api/discord/dm", map[string]string{
		"user_id": userID,
		"text":    text,
	}, nil)
}

// tryAutoStartDiscordDaemon starts the Discord daemon if a binding exists.
// Retries the status check in case the backend is still starting up.
func (a *App) tryAutoStartDiscordDaemon() {
	if noYansuAccount() {
		a.tryAutoStartDiscordLocal() // no account: local gateway bot, not backend relay
		return
	}
	var status DiscordStatus
	for attempt := 1; attempt <= 5; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		status = a.GetDiscordStatus()
		if status.Bound {
			break
		}
	}
	if status.Bound && !status.DaemonOnline {
		if err := a.startDiscordDaemon(); err != nil {
			log.Printf("[discord] auto-start failed: %v", err)
		}
	}
}
