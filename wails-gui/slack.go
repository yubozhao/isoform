package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// SlackStatus is the Slack connection state returned to the frontend.
type SlackStatus struct {
	Connected     bool   `json:"connected"`
	TeamName      string `json:"team_name,omitempty"`
	SlackUserID   string `json:"slack_user_id,omitempty"`
	SlackUserName string `json:"slack_user_name,omitempty"`
	DaemonOnline  bool   `json:"daemon_online"`
	// Paired and PairingCode describe a local app: until its owner sends the
	// pairing code, the app answers nobody.
	Paired      bool   `json:"paired,omitempty"`
	PairingCode string `json:"pairing_code,omitempty"`
}

// slackBindingResponse mirrors the backend GET /api/daemon/me/binding JSON.
type slackBindingResponse struct {
	BindingID     int    `json:"binding_id"`
	TeamID        string `json:"team_id"`
	SlackUserID   string `json:"slack_user_id"`
	SlackUserName string `json:"slack_user_name"`
	TeamName      string `json:"team_name"`
}

// GetSlackStatus checks whether the current user has a Slack binding on the backend
// and whether the local daemon WebSocket is connected.
func (a *App) GetSlackStatus() SlackStatus {
	if noYansuAccount() {
		return a.slackLocalStatus()
	}
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return SlackStatus{}
	}

	var binding slackBindingResponse
	if err := apiGet(loadAPIBaseURL(), token, "/api/daemon/me/binding", &binding); err != nil {
		// No binding or auth error — not connected.
		return SlackStatus{}
	}

	a.mu.Lock()
	daemonOnline := a.slackDaemon != nil && a.slackDaemon.isConnected()
	a.mu.Unlock()

	return SlackStatus{
		Connected:    true,
		TeamName:     binding.TeamName,
		SlackUserID:  binding.SlackUserID,
		DaemonOnline: daemonOnline,
	}
}

// ConnectSlack re-uses an existing backend binding when possible, falling back
// to the full OAuth install flow only when no binding exists.
func (a *App) ConnectSlack() {
	token, err := loadYansuToken()
	if err == nil && token != "" {
		var binding slackBindingResponse
		if err := apiGet(loadAPIBaseURL(), token, "/api/daemon/me/binding", &binding); err == nil {
			log.Printf("[slack] ConnectSlack: existing binding found (%s:%s), starting daemon directly", binding.TeamID, binding.SlackUserID)
			go a.StartDaemon()
			return
		}
	}

	baseURL := loadWebBaseURL()
	runtime.BrowserOpenURL(a.ctx, baseURL+"/slack/install")
}

// DisconnectSlack deletes the backend binding, stops the local daemon, and
// clears the persisted Slack config. The backend binding is deleted FIRST to
// prevent UI inconsistency (GetSlackStatus checks backend, not local config).
func (a *App) DisconnectSlack() error {
	if noYansuAccount() {
		err := a.disconnectSlackLocal()
		if err == nil {
			publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalAccount, ID: "slack"})
		}
		return err
	}
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated")
	}
	baseURL := loadAPIBaseURL()

	// Resolve current binding from backend.
	var binding slackBindingResponse
	if err := apiGet(baseURL, token, "/api/daemon/me/binding", &binding); err != nil {
		if isAPINotFound(err) {
			// Already unbound on backend — proceed with local cleanup.
			log.Printf("[slack] DisconnectSlack: no backend binding (404), proceeding with local cleanup")
		} else {
			// Cannot confirm binding state — abort to prevent inconsistency.
			return fmt.Errorf("failed to check Slack binding: %w", err)
		}
	} else {
		// Binding exists — delete it on backend first.
		if err := apiDelete(baseURL, token, fmt.Sprintf("/api/daemon/bindings/%d", binding.BindingID)); err != nil {
			return fmt.Errorf("failed to delete Slack binding: %w", err)
		}
		log.Printf("[slack] DisconnectSlack: deleted backend binding %d", binding.BindingID)
	}

	// Backend binding cleared (or was already absent). Safe to clean up locally.
	publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalAccount, ID: "slack"})
	a.StopDaemon()

	// Reset persisted config synchronously so auto-start on next launch is skipped.
	if err := updateConfig(func(c *yansuConfig) {
		c.Slack = nil
		c.SlackScanCompleted = false
		c.SlackScanTime = ""
		c.SlackScanChannelFilter = nil
		c.SlackKnownMemberChannels = nil
	}); err != nil {
		return err
	}

	// Wipe scan history and dedup state in the background so the user
	// doesn't wait on the pollRW lock or filesystem I/O.
	go a.cleanupSlackScanState()

	return nil
}

// cleanupSlackScanState wipes scan history, artifacts, and dedup state
// left over from a previous Slack workspace. Intended to run in a goroutine
// so DisconnectSlack returns immediately.
func (a *App) cleanupSlackScanState() {
	// Acquire exclusive scanner lock so we don't race with an in-flight poll().
	if s := a.slackScanner; s != nil {
		s.pollRW.Lock()
		defer s.pollRW.Unlock()
	}

	// Wipe dynamic scan state (cursors, history, work files).
	if err := os.RemoveAll(slackDynamicStateDir()); err != nil {
		log.Printf("[slack] cleanupSlackScanState: failed to remove dynamic state dir: %v", err)
	}

	// Wipe shared scan artifacts so a future connect starts fully clean.
	scanDir := a.scanDir()
	if err := os.Remove(filepath.Join(scanDir, "known_processed.json")); err != nil && !os.IsNotExist(err) {
		log.Printf("[slack] cleanupSlackScanState: failed to remove known_processed.json: %v", err)
	}
	if err := os.Remove(filepath.Join(scanDir, "known_automations.json")); err != nil && !os.IsNotExist(err) {
		log.Printf("[slack] cleanupSlackScanState: failed to remove known_automations.json: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(scanDir, "conversations")); err != nil {
		log.Printf("[slack] cleanupSlackScanState: failed to remove conversations dir: %v", err)
	}

	// Reset the slackConvs dedup map so the next scan doesn't skip previously
	// seen conversation IDs that belong to the old workspace.
	// clearSlackConvs holds mc.mu and calls saveProcessedState, which avoids
	// a race with concurrent saveProcessedState calls and handles the case
	// where memory_processed.json does not exist yet.
	if mc := a.getMemoryGenerator(); mc != nil {
		mc.clearSlackConvs()
	}

	log.Printf("[slack] scan state cleanup completed")
}

// SlackChannelsResult wraps the channel list with an error field
// so the frontend can distinguish "no channels" from "request failed".
type SlackChannelsResult struct {
	Channels []SlackScanChannel `json:"channels"`
	Error    string             `json:"error,omitempty"`
}

// GetSlackChannels returns the list of Slack channels the bot has joined.
func (a *App) GetSlackChannels() SlackChannelsResult {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return SlackChannelsResult{Channels: []SlackScanChannel{}, Error: "Not authenticated"}
	}
	var list SlackScanChannelList
	if err := apiGet(loadAPIBaseURL(), token, "/api/slack/channels", &list); err != nil {
		return SlackChannelsResult{Channels: []SlackScanChannel{}, Error: "Failed to load channels"}
	}
	channels := list.Channels
	if channels == nil {
		channels = []SlackScanChannel{}
	}
	return SlackChannelsResult{Channels: channels}
}

// JoinSlackChannel makes the bot join a public Slack channel via the backend.
// Used by the settings UI when the user selects a channel the bot hasn't joined yet.
func (a *App) JoinSlackChannel(channelID string) error {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated")
	}
	return apiPost(loadAPIBaseURL(), token, fmt.Sprintf("/api/slack/channels/%s/join", channelID), nil, nil)
}

// StartDaemon discovers the Slack binding from the backend and starts the daemon WebSocket.
// If no binding exists, it returns an error without starting.
func (a *App) StartDaemon() error {
	if noYansuAccount() {
		return a.startSlackLocal()
	}
	token, err := loadYansuToken()
	if err != nil || token == "" {
		log.Printf("[slack] StartDaemon skipped: not authenticated")
		return fmt.Errorf("not authenticated")
	}
	baseURL := loadAPIBaseURL()

	// Check for existing binding on the backend.
	var binding slackBindingResponse
	if err := apiGet(baseURL, token, "/api/daemon/me/binding", &binding); err != nil {
		log.Printf("[slack] StartDaemon failed: no Slack binding from backend (%v)", err)
		return fmt.Errorf("no Slack binding: %w", err)
	}

	// Persist the binding locally for auto-reconnect on restart.
	if err := updateConfig(func(c *yansuConfig) {
		c.Slack = &slackConfig{
			TeamID:        binding.TeamID,
			SlackUserID:   binding.SlackUserID,
			SlackUserName: binding.SlackUserName,
			TeamName:      binding.TeamName,
			ConnectedAt:   time.Now().Format(time.RFC3339),
		}
	}); err != nil {
		log.Printf("[slack] failed to persist Slack binding: %v", err)
	}
	if err := a.bindIntegrationSelfIdentity(context.Background(), "slack", binding.SlackUserID, binding.SlackUserName); err != nil {
		log.Printf("[slack] failed to bind current user to People self: %v", err)
	}

	// Ensure we always run the daemon with the latest binding identity.
	// If a stale/disconnected daemon instance exists, replace it.
	a.mu.Lock()
	existing := a.slackDaemon
	if existing != nil {
		sameBinding := existing.teamID == binding.TeamID && existing.userID == binding.SlackUserID
		if sameBinding && existing.isConnected() {
			a.mu.Unlock()
			return nil // already running for current binding
		}
	}
	d := newDaemonClient(a, "slack", baseURL, token, binding.TeamID, binding.SlackUserID)
	a.slackDaemon = d
	a.mu.Unlock()

	if existing != nil {
		existing.stop()
	}

	go d.run()
	log.Printf("[slack] StartDaemon launched for %s:%s via %s", binding.TeamID, binding.SlackUserID, baseURL)

	return nil
}

// StopDaemon stops the daemon WebSocket if running.
func (a *App) StopDaemon() {
	a.mu.Lock()
	d := a.slackDaemon
	a.slackDaemon = nil
	a.mu.Unlock()

	if d != nil {
		d.stop()
	}
}

// SendSlackMessage posts a proactive message to a Slack channel on behalf of the
// authenticated user. channelID is required; threadTS may be empty to post a
// top-level message rather than a threaded reply.
func (a *App) SendSlackMessage(channelID, threadTS, text string) error {
	token, err := loadYansuToken()
	if err != nil || token == "" {
		return fmt.Errorf("not authenticated")
	}
	return apiPost(loadAPIBaseURL(), token, "/api/slack/send", map[string]string{
		"channel_id": channelID,
		"thread_ts":  threadTS,
		"text":       text,
	}, nil)
}

// tryAutoStartDaemon attempts to start the daemon from persisted config.
// Called from startup() — errors are logged, not propagated.
func (a *App) tryAutoStartDaemon() {
	if noYansuAccount() {
		a.tryAutoStartSlackLocal() // no account: local Socket Mode bot, not backend relay
		return
	}
	token, err := loadYansuToken()
	if err != nil || token == "" {
		log.Printf("[slack] auto-start skipped: not authenticated")
		return
	}

	baseURL := loadAPIBaseURL()

	cfg := loadConfig()
	teamID := ""
	slackUserID := ""
	slackUserName := ""
	teamName := ""

	if cfg.Slack != nil && cfg.Slack.TeamID != "" && cfg.Slack.SlackUserID != "" {
		teamID = cfg.Slack.TeamID
		slackUserID = cfg.Slack.SlackUserID
		slackUserName = cfg.Slack.SlackUserName
		teamName = cfg.Slack.TeamName
		// Backfill the display name for configs persisted before slack_user_name
		// was tracked, so the People self handle shows a name instead of the ID.
		// Only adopt the backend name when it belongs to the same Slack user the
		// daemon is bound to — GetMyBinding returns the most recent binding, which
		// may be a different workspace/user.
		if slackUserName == "" {
			var binding slackBindingResponse
			if err := apiGet(baseURL, token, "/api/daemon/me/binding", &binding); err == nil &&
				binding.SlackUserID == slackUserID && binding.SlackUserName != "" {
				slackUserName = binding.SlackUserName
				if err := updateConfig(func(c *yansuConfig) {
					if c.Slack != nil {
						c.Slack.SlackUserName = slackUserName
					}
				}); err != nil {
					log.Printf("[slack] auto-start: failed to persist backfilled user name: %v", err)
				}
			}
		}
	} else {
		// No persisted binding — recover from backend with retries
		// (the backend may still be starting up).
		var binding slackBindingResponse
		var lastErr error
		for attempt := 1; attempt <= 5; attempt++ {
			if attempt > 1 {
				time.Sleep(time.Duration(attempt) * time.Second)
			}
			if err := apiGet(baseURL, token, "/api/daemon/me/binding", &binding); err != nil {
				lastErr = err
				continue
			}
			lastErr = nil
			break
		}
		if lastErr != nil {
			log.Printf("[slack] auto-start skipped: no persisted binding and backend lookup failed after retries (%v)", lastErr)
			return
		}
		teamID = binding.TeamID
		slackUserID = binding.SlackUserID
		slackUserName = binding.SlackUserName
		teamName = binding.TeamName

		if err := updateConfig(func(c *yansuConfig) {
			c.Slack = &slackConfig{
				TeamID:        teamID,
				SlackUserID:   slackUserID,
				SlackUserName: slackUserName,
				TeamName:      teamName,
				ConnectedAt:   time.Now().Format(time.RFC3339),
			}
		}); err != nil {
			log.Printf("[slack] auto-start: failed to persist recovered Slack binding: %v", err)
		}
		log.Printf("[slack] auto-start recovered binding %s:%s from backend", teamID, slackUserID)
	}

	d := newDaemonClient(a, "slack", baseURL, token, teamID, slackUserID)

	a.mu.Lock()
	if a.slackDaemon != nil {
		a.mu.Unlock()
		log.Printf("[slack] auto-start skipped: daemon already exists")
		return // already running
	}
	a.slackDaemon = d
	a.mu.Unlock()

	go d.run()
	log.Printf("[slack] auto-start launched for %s:%s via %s", teamID, slackUserID, baseURL)
	if err := a.bindIntegrationSelfIdentity(context.Background(), "slack", slackUserID, slackUserName); err != nil {
		log.Printf("[slack] failed to restore People self binding: %v", err)
	}
}
