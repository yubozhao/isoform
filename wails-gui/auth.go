package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"wails-gui/internal/platform/shellenv"
)

// There is no Yansu sign-in (see noYansuAccount in account_mode.go): the
// profile below is the local People "self" identity, and the token helpers
// that remain only clear leftovers from earlier installs.

// UserProfile represents the current user's profile
type UserProfile struct {
	Name         string  `json:"name"`
	DisplayName  string  `json:"display_name"`
	Email        string  `json:"email"`
	AvatarURL    *string `json:"avatar_url,omitempty"`
	IsSuperAdmin bool    `json:"is_super_admin"`
}

// slackConfig holds persisted Slack daemon identity for auto-reconnect on restart.
type slackConfig struct {
	TeamID        string `json:"team_id"`
	SlackUserID   string `json:"slack_user_id"`
	SlackUserName string `json:"slack_user_name,omitempty"`
	TeamName      string `json:"team_name"`
	ConnectedAt   string `json:"connected_at"`
}

// discordConfig holds persisted Discord daemon identity for auto-reconnect on restart.
type discordConfig struct {
	DiscordUserID   string `json:"discord_user_id"`
	DiscordUsername string `json:"discord_username"`
	ConnectedAt     string `json:"connected_at"`
}

// feishuConfig holds persisted Feishu daemon identity for auto-reconnect on restart.
type feishuConfig struct {
	AppID          string `json:"app_id"`
	OrgName        string `json:"org_name,omitempty"` // org whose credential was used for this binding
	FeishuOpenID   string `json:"feishu_open_id"`
	FeishuUserName string `json:"feishu_user_name"`
	ConnectedAt    string `json:"connected_at"`
}

// telegramConfig holds persisted Telegram daemon identity for auto-reconnect on restart.
type telegramConfig struct {
	TelegramUserID   string `json:"telegram_user_id"`
	TelegramUserName string `json:"telegram_user_name"`
	ConnectedAt      string `json:"connected_at"`
}

// wechatConfig holds persisted WeChat bot identity for auto-reconnect on restart.
type wechatConfig struct {
	BotID       string `json:"bot_id"`
	ConnectedAt string `json:"connected_at"`
}

// yansuConfig mirrors ~/.yansu-agent/config.json (shared with CLI).
type yansuConfig struct {
	BaseURL         string `json:"base_url"` // legacy: treated as API base URL
	WebBaseURL      string `json:"web_base_url,omitempty"`
	APIBaseURL      string `json:"api_base_url,omitempty"`
	BuildWebBaseURL string `json:"build_web_base_url,omitempty"` // tracks which build wrote the URLs
	// EnvironmentOverride allows runtime switching via magic key.
	// "staging" forces staging URL, "production" forces production URL.
	// Empty means no override — local dev builds default to staging,
	// release builds (with ldflags) default to production.
	EnvironmentOverride string `json:"environment_override,omitempty"`
	SyncInterval        int    `json:"sync_interval_minutes"`
	WorktreeRoot        string `json:"worktree_root,omitempty"`
	RepoRoot            string `json:"repo_root,omitempty"`
	BranchPrefix        string `json:"branch_prefix,omitempty"`
	// GitCloneTimeoutMS overrides GUI clone timeout when > 0.
	// Env YANSU_GIT_CLONE_TIMEOUT_MS still has higher priority.
	GitCloneTimeoutMS int `json:"git_clone_timeout_ms,omitempty"`
	// WorktreeSetupCommand is a shell command to run after creating a worktree.
	WorktreeSetupCommand string `json:"worktree_setup_command,omitempty"`
	// WorktreeSetupTimeoutMS is the timeout for the setup command in milliseconds.
	WorktreeSetupTimeoutMS int `json:"worktree_setup_timeout_ms,omitempty"`
	// Slack holds persisted Slack daemon identity for auto-reconnect.
	Slack *slackConfig `json:"slack,omitempty"`
	// Feishu holds persisted Feishu daemon identity for auto-reconnect.
	Feishu *feishuConfig `json:"feishu,omitempty"`
	// Lark holds persisted Lark (international) daemon identity for auto-reconnect.
	Lark *feishuConfig `json:"lark,omitempty"`
	// Discord holds persisted Discord daemon identity for auto-reconnect.
	Discord *discordConfig `json:"discord,omitempty"`
	// Telegram holds persisted Telegram daemon identity for auto-reconnect.
	Telegram *telegramConfig `json:"telegram,omitempty"`
	// WeChat holds persisted WeChat bot identity for auto-reconnect.
	WeChat *wechatConfig `json:"wechat,omitempty"`
	// SlackScanCompleted is set to true only after full scan success (manifest "completed").
	SlackScanCompleted bool   `json:"slack_scan_completed,omitempty"`
	SlackScanTime      string `json:"slack_scan_time,omitempty"`
	// SlackScanDaysBack configures scan range; 0 = default 7 days.
	SlackScanDaysBack int `json:"slack_scan_days_back,omitempty"`
	// SlackScanChannelFilter restricts scan to these channel IDs; nil/empty = scan all.
	SlackScanChannelFilter []string `json:"slack_scan_channel_filter,omitempty"`
	// SlackKnownMemberChannels tracks which member channels were known at last
	// filter save, so newly-joined channels can be auto-included in scans.
	SlackKnownMemberChannels []string `json:"slack_known_member_channels,omitempty"`
	// UseYansuGateway forces auth through the Yansu AI gateway (sub2api)
	// even when local credentials (BYOK/native) are available.
	UseYansuGateway bool `json:"use_yansu_gateway,omitempty"`
	// FeishuScanChatFilter restricts Feishu scan to these chat IDs; nil/empty = scan all.
	FeishuScanChatFilter []string `json:"feishu_scan_chat_filter,omitempty"`
	// DiscordScanChannelFilter restricts Discord scan to these channels per guild.
	// nil/empty map = scan all (no filter applied).
	// Guild present with non-empty list = scan only those channels.
	// Guild present with empty list = skip that guild entirely.
	DiscordScanChannelFilter map[string][]string `json:"discord_scan_channel_filter,omitempty"`
	// StartAtLoginDisabled registers the app as a macOS login item via
	// SMAppService. Stored as inverse so zero-value (false) = enabled.
	StartAtLoginDisabled bool `json:"start_at_login_disabled,omitempty"`
	// BackgroundListeningConfigured tracks whether the user has explicitly
	// confirmed the background listening setting. Fresh installs persist the
	// default enabled state on first launch; upgrades stay disabled until set.
	BackgroundListeningConfigured bool `json:"background_listening_configured,omitempty"`
	// BackgroundListeningDisabled controls the background speech capture
	// feature. Stored as inverse so zero-value (false) = enabled once the
	// setting has been explicitly configured or persisted for a fresh install.
	BackgroundListeningDisabled bool `json:"background_listening_disabled,omitempty"`
	// BackgroundListeningSource preserves the legacy source preference. The
	// current implementation captures both microphone and system audio when
	// supported, ignoring this value except for backward compatibility.
	BackgroundListeningSource string `json:"background_listening_source,omitempty"`
	// BackgroundMicrophonePaused switches off the microphone source of
	// background listening while system audio keeps going: the Microphone
	// switch in Settings > Observation on macOS. Voice typing the user
	// starts is not affected.
	BackgroundMicrophonePaused bool `json:"background_microphone_paused,omitempty"`
	// BackgroundSystemAudioOutsideCalls turns the computer's own sound into
	// text outside calls too (videos, podcasts). Off by default; during a
	// call both sides are always heard.
	BackgroundSystemAudioOutsideCalls bool `json:"background_system_audio_outside_calls,omitempty"`
	// AfterCallSpeechModelOff keeps calls on the live speech engine and
	// stops the after-call model's download (after_call_speech_model.go).
	AfterCallSpeechModelOff bool `json:"after_call_speech_model_off,omitempty"`
	// AutomaticMeetingRecordingDisabled controls whether Yansu automatically
	// detects supported calls and saves them as meeting records. Stored as an
	// inverse so missing config keeps the existing default-enabled behaviour.
	AutomaticMeetingRecordingDisabled bool `json:"automatic_meeting_recording_disabled,omitempty"`
	// SystemAudioPermissionGranted caches whether macOS system-audio capture
	// permission has been granted for this app. Core Audio taps do not expose
	// a non-interactive preflight API, so we update this after explicit
	// permission requests and successful recorder starts.
	SystemAudioPermissionGranted bool `json:"system_audio_permission_granted,omitempty"`
	// BrowserBridgeEnabled controls whether the localhost HTTP/WebSocket
	// browser bridge (port 34116) starts on app launch. Pointer so we can
	// distinguish "unset" (nil) from an explicit false: unset falls back to
	// the default policy in shouldEnableBrowserBridge().
	BrowserBridgeEnabled *bool `json:"browser_bridge_enabled,omitempty"`
	// AutoSpeakEnabled controls whether assistant chat replies are automatically
	// read aloud (sherpa MeloTTS) once a message completes. Pointer so unset
	// (nil) is distinguishable from explicit false; unset defaults to off.
	AutoSpeakEnabled *bool `json:"auto_speak_enabled,omitempty"`
	// AutoSpeakAutomationEnabled controls whether a short spoken announcement
	// ("<job> finished") plays when a background automation run completes.
	// Independent of AutoSpeakEnabled (chat). Pointer so unset == off.
	AutoSpeakAutomationEnabled *bool `json:"auto_speak_automation_enabled,omitempty"`
	// AutoSpeakNotificationsEnabled controls whether notification titles (new
	// knowledge/memory, handoff proposals) are read aloud when they fire.
	// Independent of the other two. Pointer so unset == off.
	AutoSpeakNotificationsEnabled *bool `json:"auto_speak_notifications_enabled,omitempty"`
	// CuaCursorOverlayEnabled controls whether the bundled cua-driver renders
	// its virtual-cursor lens overlay during computer-use actions. Pointer so
	// we can distinguish "unset" (nil) from an explicit false: unset falls
	// back to the default policy in shouldEnableCuaCursorOverlay().
	CuaCursorOverlayEnabled *bool `json:"cua_cursor_overlay_enabled,omitempty"`
	// AskBeforeRiskyCommands makes attended chats ask before risky Bash
	// commands (Settings > Privacy). Off when unset, so the default is off.
	AskBeforeRiskyCommands bool `json:"ask_before_risky_commands,omitempty"`
	// AssistantHistoryOff is the Settings > Privacy opt-out for reading this
	// computer's Claude Code and Codex history. Absent means on.
	AssistantHistoryOff bool `json:"assistant_history_off,omitempty"`
}

var localClaudeEnvVarNames = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	// Tracked here so a developer's pre-existing shell value is captured
	// before sub2api injection overwrites it, and restored when the gateway
	// is toggled off. Without this, toggling sub2api off would leak the
	// 262144 cap into direct-Anthropic sessions that actually support 1M.
	"YANSU_CONTEXT_WINDOW_OVERRIDE",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"ALL_PROXY",
	"NO_PROXY",
}

// ── Token helpers ──

func yansuTokenPath() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, tokenFileName), nil
}

func loadYansuToken() (string, error) {
	tokenPath, err := yansuTokenPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func clearYansuToken() error {
	tokenPath, err := yansuTokenPath()
	if err != nil {
		return err
	}
	if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ── Config helpers ──

func yansuConfigPath() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, configFileName), nil
}

// loadConfig reads ~/.yansu-agent/config.json. Returns zero-value on missing file.
func loadConfig() yansuConfig {
	path, err := yansuConfigPath()
	if err != nil {
		return yansuConfig{UseYansuGateway: true}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return yansuConfig{UseYansuGateway: true}
	}
	var cfg yansuConfig
	_ = json.Unmarshal(data, &cfg)
	// BYOK has been removed — the Yansu AI gateway is the only supported mode.
	// Force the field on regardless of what's persisted, so existing installs
	// with UseYansuGateway=false automatically flip to gateway mode.
	cfg.UseYansuGateway = true
	return cfg
}

// loadConfigChecked preserves the first-install defaults but reports an
// unreadable or malformed existing file to settings callers.
func loadConfigChecked() (yansuConfig, error) {
	path, err := yansuConfigPath()
	if err != nil {
		return yansuConfig{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return yansuConfig{UseYansuGateway: true}, nil
	}
	if err != nil {
		return yansuConfig{}, err
	}
	if strings.TrimSpace(string(data)) == "null" {
		return yansuConfig{}, fmt.Errorf("config must be an object")
	}
	var cfg yansuConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return yansuConfig{}, err
	}
	cfg.UseYansuGateway = true
	return cfg, nil
}

func normalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func deriveLocalAPIBaseURLFromWeb(webBaseURL string) string {
	u, err := url.Parse(webBaseURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if u.Port() != "3000" {
		return ""
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return ""
	}

	alt := *u
	if host == "::1" {
		alt.Host = "[::1]:8080"
	} else {
		alt.Host = host + ":8080"
	}
	alt.Path = ""
	alt.RawQuery = ""
	alt.Fragment = ""
	return normalizeBaseURL(alt.String())
}

func defaultAPIBaseURLForWeb(webBaseURL string) string {
	if local := deriveLocalAPIBaseURLFromWeb(webBaseURL); local != "" {
		return local
	}
	return webBaseURL
}

const defaultStagingWebBaseURL = "https://staging-dashboard.example.com"
const defaultProductionWebBaseURL = "https://dashboard.yansu.ai"

// isReleaseBuild reports whether DefaultWebBaseURL was overridden at build time
// via -ldflags (i.e. this is a CI-produced binary, not a local dev build).
func isReleaseBuild() bool {
	return DefaultWebBaseURL != ""
}

func resolveWebBaseURL(cfg yansuConfig) string {
	// 1. Environment variable always wins (local dev override).
	if envURL := normalizeBaseURL(os.Getenv(envBaseURL)); envURL != "" {
		return envURL
	}
	// 2. Explicit runtime environment override (magic key toggle).
	//    Both directions must be handled: "staging" forces staging,
	//    "production" forces production. Empty = no override (use build default).
	switch cfg.EnvironmentOverride {
	case "staging":
		return defaultStagingWebBaseURL
	case "production":
		return defaultProductionWebBaseURL
	}
	// 3. Release builds: use the build-time injected value.
	if isReleaseBuild() {
		return DefaultWebBaseURL
	}
	if isReleaseChannel() {
		return defaultProductionWebBaseURL
	}
	// 4. Local dev builds (DefaultWebBaseURL is empty): fall back to staging.
	return defaultStagingWebBaseURL
}

func resolveAPIBaseURL(cfg yansuConfig) string {
	// Derive from resolved web base URL.
	// When YANSU_URL points to localhost:3000, automatically use :8080 for API.
	webBaseURL := resolveWebBaseURL(cfg)
	if local := deriveLocalAPIBaseURLFromWeb(webBaseURL); local != "" {
		return local
	}
	return webBaseURL
}

// saveBaseURLToConfig writes web/api base URLs into config.json, preserving other fields.
func saveBaseURLToConfig(webBaseURL string) error {
	normalized := normalizeBaseURL(webBaseURL)
	apiBase := defaultAPIBaseURLForWeb(normalized)

	configMu.Lock()
	defer configMu.Unlock()

	cfg := loadConfig()
	cfg.WebBaseURL = normalized
	cfg.APIBaseURL = apiBase
	cfg.BaseURL = apiBase // keep legacy field aligned for older clients
	cfg.BuildWebBaseURL = DefaultWebBaseURL
	if cfg.SyncInterval == 0 {
		cfg.SyncInterval = 5
	}

	dir, err := flavorDataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, configFileName), data, 0600)
}

// saveConfig writes the full config back to config.json.
func saveConfig(cfg yansuConfig) error {
	dir, err := flavorDataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, configFileName), data, 0600)
}

// configMu serializes config read-modify-write cycles so concurrent
// goroutines (e.g. auto-start daemons on startup) don't overwrite each
// other's changes.
var configMu sync.Mutex

// updateConfig atomically loads config.json, applies fn, and writes it back.
// Use this instead of a raw loadConfig/saveConfig pair when the caller runs
// concurrently with other config writers.
func updateConfig(fn func(*yansuConfig)) error {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := loadConfigChecked()
	if err != nil {
		return err
	}
	fn(&cfg)
	return saveConfig(cfg)
}

// loadWebBaseURL returns the web frontend URL used for browser redirects.
func loadWebBaseURL() string {
	return resolveWebBaseURL(loadConfig())
}

// GetWebBaseURL exposes the current web base URL to the frontend.
func (a *App) GetWebBaseURL() string {
	return loadWebBaseURL()
}

// loadAPIBaseURL returns the API URL used for HTTP calls and daemon WS.
func loadAPIBaseURL() string {
	return resolveAPIBaseURL(loadConfig())
}

// loadBaseURL is kept for backward compatibility and maps to API base URL.
func loadBaseURL() string {
	return loadAPIBaseURL()
}

// ── Identity methods (exposed to frontend via Wails) ──

// selfProfileAsUserProfile surfaces the local People "self" identity as a
// UserProfile so the app (which has no Yansu login / remote profile) can
// still show the user's own name in the UI. Returns nil when no self profile
// with a name is set yet.
func (a *App) selfProfileAsUserProfile() *UserProfile {
	if a.store == nil {
		return nil
	}
	rows, err := a.store.ListPrincipalsByKind(context.Background(), "self")
	if err != nil || len(rows) == 0 {
		return nil
	}
	name := strings.TrimSpace(rows[0].DisplayName)
	if name == "" {
		return nil
	}
	log.Printf("[profile] user profile resolved from People self: %q", name)
	return &UserProfile{Name: name, DisplayName: name}
}

// GetUserProfile returns the current user's profile.
func (a *App) GetUserProfile() (*UserProfile, error) {
	// There is no remote Yansu profile (the /api/auth/profile path is denied),
	// and a stale ~/.yansu-agent/token from an earlier install must NOT route
	// us into the API path. Always surface the local People "self" identity
	// instead so the sidebar / menu / greeting show the user's own name rather
	// than the generic "User". Returns not-authenticated only when no self
	// profile is set yet (the UI keeps its fallback label).
	if noYansuAccount() {
		if p := a.selfProfileAsUserProfile(); p != nil {
			return p, nil
		}
		return nil, fmt.Errorf("not authenticated")
	}

	token, err := loadYansuToken()
	if err != nil || token == "" {
		return nil, fmt.Errorf("not authenticated")
	}

	var profile UserProfile
	if err := apiGet(loadAPIBaseURL(), token, "/api/auth/profile", &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}

// getUserDisplayName returns the user's display name. It prefers the
// locally set self-profile name (from the principals table) over the
// remote API profile, falling back to email or "the user". The result
// is cached in-memory so subsequent calls avoid repeated lookups. Safe
// to call from background goroutines (e.g. memory generator).
func (a *App) getUserDisplayName() string {
	a.mu.Lock()
	cached := a.cachedDisplayName
	a.mu.Unlock()
	if cached != "" {
		return cached
	}

	// Prefer the locally set self-profile name.
	if a.store != nil {
		rows, err := a.store.ListPrincipalsByKind(context.Background(), "self")
		if err == nil && len(rows) > 0 && rows[0].DisplayName != "" {
			name := rows[0].DisplayName
			a.mu.Lock()
			a.cachedDisplayName = name
			a.mu.Unlock()
			return name
		}
	}

	profile, err := a.GetUserProfile()
	if err != nil || profile == nil {
		return "the user"
	}
	name := profile.DisplayName
	if name == "" {
		name = profile.Name
	}
	if name == "" {
		name = profile.Email
	}
	if name == "" {
		return "the user"
	}

	a.mu.Lock()
	a.cachedDisplayName = name
	a.mu.Unlock()
	return name
}

func syncLocalClaudeCodeEnvFromLoginShell() []string {
	applied := shellenv.ApplyEnvVars(localClaudeEnvVarNames...)
	if len(applied) > 0 {
		log.Printf("[anthropic-env] restored local Claude env vars from login shell: %s", strings.Join(applied, ", "))
	}
	return applied
}

func (a *App) captureLocalClaudeEnvSnapshot() {
	snapshot := make(map[string]string, len(localClaudeEnvVarNames))
	for _, name := range localClaudeEnvVarNames {
		snapshot[name] = os.Getenv(name)
	}

	a.mu.Lock()
	a.localClaudeEnvSnapshot = snapshot
	a.mu.Unlock()
}

func (a *App) restoreLocalClaudeEnvSnapshot() []string {
	a.mu.Lock()
	snapshot := make(map[string]string, len(a.localClaudeEnvSnapshot))
	for key, value := range a.localClaudeEnvSnapshot {
		snapshot[key] = value
	}
	a.mu.Unlock()

	applied := make([]string, 0, len(snapshot))
	for _, name := range localClaudeEnvVarNames {
		value := snapshot[name]
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			continue
		}
		applied = append(applied, name)
	}
	if len(applied) > 0 {
		log.Printf("[anthropic-env] restored local Claude env vars from in-memory snapshot: %s", strings.Join(applied, ", "))
	}
	return applied
}

// isFallbackError returns true if the error string indicates the user's
// Anthropic credentials cannot serve the request and sub2api fallback is
// warranted. Covers authentication, billing/quota, and rate-limit errors.
func isFallbackError(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	patterns := []string{
		// Auth (401)
		"authentication_error",
		"invalid_api_key",
		"invalid_x_api_key",
		"invalid api key",
		"unauthorized",
		"authentication required",
		"401",
		"expired",
		"could not authenticate",
		// Billing / quota (403)
		"permission_error",
		"forbidden",
		"403",
		"billing",
		"insufficient",
		"credit",
		"payment",
		// Rate limit (429)
		"rate_limit",
		"rate limit",
		"too many requests",
		"429",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// clearAnthropicEnv removes injected Anthropic env vars.
func clearAnthropicEnv() {
	os.Unsetenv("ANTHROPIC_AUTH_TOKEN")
	os.Unsetenv("ANTHROPIC_BASE_URL")
	// Must clear too — otherwise the 262144 cap the retired gateway injected
	// would persist into direct-Anthropic mode and silently shrink a 1M
	// window down to 256K. restoreLocalClaudeEnvSnapshot runs right after
	// this and will write back any non-empty pre-existing value.
	os.Unsetenv("YANSU_CONTEXT_WINDOW_OVERRIDE")
}

// AuthSourceInfo describes the current Anthropic auth source for debug display.
type AuthSourceInfo struct {
	Source   string `json:"source"`   // "api_key", "auth_token", "native", "sub2api", "pending"
	Verified bool   `json:"verified"` // true once the model runner is up
}

// modelRunnerReady reports whether the model runner is up. It stands in for
// "credentials verified": the credential probe never runs while
// UseYansuGateway is on, and loadConfig forces that on for every install.
func (a *App) modelRunnerReady() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runner != nil && a.ready
}

// GetAuthSource returns the current Anthropic credential source for debug UI.
func (a *App) GetAuthSource() AuthSourceInfo {
	if a.authManager != nil && a.authManager.IsActive() {
		return AuthSourceInfo{Source: "sub2api", Verified: true}
	}
	ready := a.modelRunnerReady()
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return AuthSourceInfo{Source: "api_key", Verified: ready}
	}
	if os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		return AuthSourceInfo{Source: "auth_token", Verified: ready}
	}
	if ready {
		return AuthSourceInfo{Source: "native", Verified: true}
	}
	return AuthSourceInfo{Source: "pending", Verified: false}
}

// HasLocalCredentials returns true when the user has BYOK or native credentials
// available, regardless of whether the Yansu gateway override is active.
func (a *App) HasLocalCredentials() bool {
	return os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" || a.modelRunnerReady()
}

// GetUseYansuGateway returns whether the manual Yansu AI gateway override is enabled.
func (a *App) GetUseYansuGateway() bool {
	return loadConfig().UseYansuGateway
}

// SetUseYansuGateway enables or disables the manual Yansu AI gateway override.
// Persists the setting and sets env vars synchronously, then restarts the
// agent in the background so the frontend is not blocked.
func (a *App) SetUseYansuGateway(enabled bool) ([]ExecutorInfo, error) {
	// The Yansu gateway is retired. Enabling it is refused; disabling only
	// clears a leftover override. Managed model accounts replace it.
	if enabled {
		return a.ListAvailableModels(), fmt.Errorf("the Isoform gateway is retired; use managed model accounts or your own key in Settings")
	}
	if err := updateConfig(func(c *yansuConfig) {
		c.UseYansuGateway = enabled
	}); err != nil {
		return nil, err
	}

	clearAnthropicEnv()
	a.restoreLocalClaudeEnvSnapshot()
	syncLocalClaudeCodeEnvFromLoginShell()
	a.authManager.Reset()

	// Resolve models NOW (env vars are already set) so the frontend can
	// update the model selector in the same callback — no extra round-trip.
	models := a.ListAvailableModels()

	go func() {
		a.StopAgent()
		if _, err := a.StartAgent(); err != nil {
			log.Printf("[gateway] agent restart failed: %v", err)
			return
		}
		if !enabled {
			a.probeAndGreet()
		}
	}()

	return models, nil
}

// HasEnvOverride returns true when the base URL env override is set.
func (a *App) HasEnvOverride() bool {
	return os.Getenv(envBaseURL) != ""
}

// EnvironmentInfo describes the current environment for the frontend.
// IsDev is true for local dev builds (no -ldflags DefaultWebBaseURL), independent
// of which remote environment the config points at. IsStaging and IsDev are
// orthogonal: developer-tool visibility may use IsStaging || IsDev, while
// telemetry stays keyed to IsStaging; release builds suppress the staging badge.
type EnvironmentInfo struct {
	IsRelease   bool   `json:"is_release"`
	Environment string `json:"environment"` // "production" or "staging"
	IsStaging   bool   `json:"is_staging"`
	IsDev       bool   `json:"is_dev"`
}

func environmentInfoFromConfig(cfg yansuConfig) EnvironmentInfo {
	isStaging := resolveWebBaseURL(cfg) == defaultStagingWebBaseURL
	env := "production"
	if isStaging {
		env = "staging"
	}
	return EnvironmentInfo{IsRelease: isReleaseChannel(), Environment: env, IsStaging: isStaging, IsDev: !isReleaseBuild() && !isReleaseChannel()}
}

// clearTokensForEnvSwitch clears auth state when switching environments.
// Logs (but does not fail on) token deletion errors — a stale token from
// the previous environment will simply fail verification on next CheckAuth.
func clearTokensForEnvSwitch(am *AuthManager) {
	if err := clearYansuToken(); err != nil {
		log.Printf("[env] warning: failed to clear auth token: %v", err)
	}
	am.Reset()
}

// GetEnvironment returns the current environment state.
func (a *App) GetEnvironment() EnvironmentInfo {
	return environmentInfoFromConfig(loadConfig())
}

// SwitchEnvironment toggles between production and staging environments.
// Returns the new environment info. The app must be re-authenticated after switching.
func (a *App) SwitchEnvironment() (EnvironmentInfo, error) {
	current := a.GetEnvironment()

	var override string
	if current.IsStaging {
		override = "production"
	} else {
		override = "staging"
	}
	if err := updateConfig(func(c *yansuConfig) {
		c.EnvironmentOverride = override
	}); err != nil {
		return EnvironmentInfo{}, fmt.Errorf("failed to save environment: %w", err)
	}
	clearTokensForEnvSwitch(a.authManager)
	a.stopAllDaemons()
	info := environmentInfoFromConfig(loadConfig())
	log.Printf("[env] switched to %s environment", info.Environment)
	return info, nil
}

// GetStartAtLogin returns whether the app is registered to start at login.
func (a *App) GetStartAtLogin() (bool, error) {
	return readStartAtLogin()
}

func loginStatusFromCode(status int) (bool, error) {
	if status < 0 || status > 3 {
		return false, fmt.Errorf("cannot read login item status: %d", status)
	}
	return status == 1, nil
}

// SetStartAtLogin registers or unregisters the app as a login item.
func (a *App) SetStartAtLogin(enabled bool) error {
	action := "disable"
	var ok bool
	switch {
	case enabled:
		action = "enable"
		ok = registerAsLoginItem()
	default:
		ok = unregisterLoginItem()
	}
	if !ok {
		return fmt.Errorf("failed to %s start at login", action)
	}
	if err := updateConfig(func(c *yansuConfig) {
		c.StartAtLoginDisabled = !enabled
	}); err != nil {
		return err
	}
	updateMenuBarLoginItemChecked(enabled)
	return nil
}

// ShowWindow brings the app window to the foreground and shows it in the Dock.
func (a *App) ShowWindow() {
	switchToRegularApp()
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
}
