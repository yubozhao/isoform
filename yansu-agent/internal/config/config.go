package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	ConfigDirName        = ".yansu-agent"
	ConfigFileName       = "config.json"
	TokenFileName        = "token"
	ProjectFileName      = "project.json"
	DaemonConfigFileName = "daemon.json"

	// Environment variable names
	EnvBaseURL        = "YANSU_URL"
	EnvSlackBotToken  = "SLACK_BOT_TOKEN"
	EnvSlackAppToken  = "SLACK_APP_TOKEN"
	EnvDaemonExecutor = "YANSU_DAEMON_EXECUTOR"

	DaemonExecutorClaude = "claude"
	DaemonExecutorCodex  = "codex"
)

// SlackConfig holds Slack-specific daemon credentials.
type SlackConfig struct {
	BotToken         string `json:"bot_token"`          // xoxb-...
	AppToken         string `json:"app_token"`          // xapp-... (Socket Mode)
	AuthorizedUserID string `json:"authorized_user_id"` // e.g. U0123ABC
}

// RelayConfig holds backend relay mode credentials (team mode, shared Slack App).
type RelayConfig struct {
	TeamID      string `json:"team_id"`       // Slack workspace ID (T0xxxxx)
	SlackUserID string `json:"slack_user_id"` // Slack user ID (U0xxxxx)
}

// DaemonConfig is the top-level daemon configuration stored at ~/.yansu-agent/daemon.json.
// A nil sub-config means that platform is not enabled.
type DaemonConfig struct {
	Slack    *SlackConfig `json:"slack,omitempty"`
	Relay    *RelayConfig `json:"relay,omitempty"`
	Executor string       `json:"executor,omitempty"` // claude (default) | codex
}

// legacyDaemonConfig holds the pre-v2 flat format used for automatic migration.
type legacyDaemonConfig struct {
	SlackBotToken         string `json:"slack_bot_token"`
	SlackAppToken         string `json:"slack_app_token"`
	AuthorizedSlackUserID string `json:"authorized_slack_user_id"`
}

// Config holds the global agent configuration
type Config struct {
	BaseURL         string `json:"base_url"` // legacy: treated as API base URL
	WebBaseURL      string `json:"web_base_url,omitempty"`
	APIBaseURL      string `json:"api_base_url,omitempty"`
	BuildWebBaseURL string `json:"build_web_base_url,omitempty"` // tracks which build wrote the URLs (set by wails-gui)
	SyncInterval    int    `json:"sync_interval_minutes"`        // minutes

	// Token is loaded from a separate file for security, not stored in config.json
	Token string `json:"-"`
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
	return normalizeBaseURL(webBaseURL)
}

func normalizeAndHydrateBaseURLs(cfg *Config) {
	cfg.WebBaseURL = normalizeBaseURL(cfg.WebBaseURL)
	cfg.APIBaseURL = normalizeBaseURL(cfg.APIBaseURL)
	cfg.BaseURL = normalizeBaseURL(cfg.BaseURL)

	if cfg.WebBaseURL == "" {
		cfg.WebBaseURL = cfg.BaseURL

	}
	if cfg.APIBaseURL == "" {
		if local := deriveLocalAPIBaseURLFromWeb(cfg.WebBaseURL); local != "" {
			cfg.APIBaseURL = local
		} else if cfg.BaseURL != "" {
			cfg.APIBaseURL = cfg.BaseURL
		} else {
			cfg.APIBaseURL = defaultAPIBaseURLForWeb(cfg.WebBaseURL)
		}
	}

	// Keep legacy field aligned with API base URL for old call sites.
	cfg.BaseURL = cfg.APIBaseURL
}

// ProjectConfig holds project-specific configuration
type ProjectConfig struct {
	UUID           string    `json:"uuid"`
	Name           string    `json:"name"`
	Organization   string    `json:"organization"`
	Product        string    `json:"product"`
	ProductUUID    string    `json:"product_uuid"`
	YansuURL       string    `json:"yansu_url"`
	CreatedAt      time.Time `json:"created_at"`
	KnowledgeCount int       `json:"knowledge_count"`
	LastSyncedAt   time.Time `json:"last_synced_at"`
	// WorktreeSetupCommand is preserved for the GUI; the CLI does not use it.
	WorktreeSetupCommand string `json:"worktree_setup_command,omitempty"`
}

// AgentHomeEnv overrides the data folder. The desktop sets it per build
// flavor, so a development build never shares ~/.yansu-agent with an installed
// Yansu; the bundled CLI and the embedded tools inherit it.
const AgentHomeEnv = "YANSU_AGENT_HOME"

// ConfigDir returns the selected data folder. A bundled CLI retains its flavor
// when invoked outside the desktop's environment; standalone installs keep the
// published folder unless an explicit home or flavor is selected.
func ConfigDir() (string, error) {
	bundledRoot := ""
	if executable, err := os.Executable(); err == nil {
		bundledRoot = bundledAgentHome(executable)
	}
	isoform := filepath.Base(bundledRoot) == ".isoform-agent" || strings.EqualFold(strings.TrimSpace(os.Getenv("YANSU_APP_FLAVOR")), "isoform")
	root := strings.TrimSpace(os.Getenv(AgentHomeEnv))
	if root != "" && !filepath.IsAbs(root) {
		return "", fmt.Errorf("%s must be an absolute path: %q", AgentHomeEnv, root)
	}
	if root == "" && bundledRoot != "" && !(isoform && filepath.Base(bundledRoot) == ConfigDirName) {
		root = bundledRoot
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("home directory must be an absolute path: %q", home)
		}
		dirName := ConfigDirName
		if isoform {
			dirName = ".isoform-agent"
		}
		root = filepath.Join(home, dirName)
	}
	root = filepath.Clean(root)
	if isoform {
		if err := rejectPublishedAgentHome(root); err != nil {
			return "", err
		}
	}
	return root, nil
}

func rejectPublishedAgentHome(root string) error {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return nil
	}
	legacy, err := resolveExistingHomeParents(filepath.Join(home, ConfigDirName))
	if err != nil {
		return fmt.Errorf("resolve published app data folder: %w", err)
	}
	resolved, err := resolveExistingHomeParents(root)
	if err != nil {
		return fmt.Errorf("resolve Isoform data folder: %w", err)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		legacy, resolved = strings.ToLower(legacy), strings.ToLower(resolved)
	}
	relative, err := filepath.Rel(legacy, resolved)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
		return fmt.Errorf("Isoform data folder %q resolves inside the published app's data folder; set %s to a separate absolute path", root, AgentHomeEnv)
	}
	return nil
}

func resolveExistingHomeParents(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = resolveExistingHomeParents(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

func bundledAgentHome(executable string) string {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	bin := filepath.Dir(executable)
	root := filepath.Dir(bin)
	if filepath.Base(bin) != "bin" || !filepath.IsAbs(root) {
		return ""
	}
	switch filepath.Base(root) {
	case ".isoform-agent", ConfigDirName:
		return root
	default:
		return ""
	}
}

// ConfigPath returns the path to the config file
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ConfigFileName), nil
}

// Load loads the global configuration
// Priority: environment variables > config file > defaults
func Load() (*Config, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		SyncInterval: 5,
		// WebBaseURL and APIBaseURL are intentionally left empty so that
		// normalizeAndHydrateBaseURLs can derive them from the legacy BaseURL
		// field when loading older config files that only contain base_url.
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
		// Config file doesn't exist, use defaults
	} else {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config: %w", err)
		}
		if cfg.SyncInterval == 0 {
			cfg.SyncInterval = 5
		}
	}

	// Environment variable override (applied before hydration so it takes precedence).
	if envURL := os.Getenv(EnvBaseURL); envURL != "" {
		cfg.WebBaseURL = normalizeBaseURL(envURL)
		cfg.APIBaseURL = defaultAPIBaseURLForWeb(cfg.WebBaseURL)
	}
	// Single-pass hydration: derive any empty fields from legacy BaseURL or defaults.
	normalizeAndHydrateBaseURLs(cfg)

	// Load token from separate file
	cfg.Token, _ = LoadToken()

	return cfg, nil
}

// Save saves the global configuration
func Save(cfg *Config) error {
	configDir, err := ConfigDir()
	if err != nil {
		return err
	}
	normalizeAndHydrateBaseURLs(cfg)

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// TokenPath returns the path to the token file
func TokenPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, TokenFileName), nil
}

// LoadToken loads the token from the token file
func LoadToken() (string, error) {
	tokenPath, err := TokenPath()
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read token: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

// SaveToken saves the token to a separate file with restricted permissions
func SaveToken(token string) error {
	configDir, err := ConfigDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	tokenPath := filepath.Join(configDir, TokenFileName)

	// Write token file with restricted permissions (owner read/write only)
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		return fmt.Errorf("failed to write token: %w", err)
	}

	return nil
}

// ClearToken removes the token file
func ClearToken() error {
	tokenPath, err := TokenPath()
	if err != nil {
		return err
	}

	if err := os.Remove(tokenPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove token: %w", err)
	}

	return nil
}

// LoadProject loads project-specific configuration from .something/project.json
func LoadProject(somethingDir string) (*ProjectConfig, error) {
	projectPath := filepath.Join(somethingDir, ProjectFileName)
	data, err := os.ReadFile(projectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read project config: %w", err)
	}

	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse project config: %w", err)
	}

	return &cfg, nil
}

// SaveProject saves project-specific configuration
func SaveProject(somethingDir string, cfg *ProjectConfig) error {
	if err := os.MkdirAll(somethingDir, 0755); err != nil {
		return fmt.Errorf("failed to create .something directory: %w", err)
	}

	projectPath := filepath.Join(somethingDir, ProjectFileName)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal project config: %w", err)
	}

	if err := os.WriteFile(projectPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write project config: %w", err)
	}

	return nil
}

// FindProjectRoot finds the project root by looking for .something directory
func FindProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	dir := cwd
	for {
		somethingDir := filepath.Join(dir, ".something")
		if info, err := os.Stat(somethingDir); err == nil && info.IsDir() {
			projectFile := filepath.Join(somethingDir, ProjectFileName)
			if _, err := os.Stat(projectFile); err == nil {
				return dir, nil
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("not in a Yansu project (no .something/project.json found)")
}

// GetSomethingDir returns the .something directory path for a project
func GetSomethingDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".something")
}

// PIDFilePath returns the path to the daemon PID file.
func PIDFilePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.pid"), nil
}

// LogFilePath returns the path to the daemon log file.
func LogFilePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "daemon.log"), nil
}

// WritePIDFile writes the current process PID to the PID file.
func WritePIDFile() error {
	pidPath, err := PIDFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pidPath), 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	return os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600)
}

// ReadPIDFile reads the daemon PID. Returns 0 if the PID file does not exist.
func ReadPIDFile() (int, error) {
	pidPath, err := PIDFilePath()
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to read PID file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID file content: %w", err)
	}
	return pid, nil
}

// RemovePIDFile removes the daemon PID file (best-effort).
func RemovePIDFile() {
	if pidPath, err := PIDFilePath(); err == nil {
		os.Remove(pidPath)
	}
}

// DaemonConfigPath returns the path to the daemon config file
func DaemonConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DaemonConfigFileName), nil
}

// LoadDaemonConfig loads the daemon configuration from ~/.yansu-agent/daemon.json.
// If the file uses the legacy flat format (pre-v2), it is automatically migrated to
// the current nested format: the original file is backed up as daemon.json.bak and
// the new format is written in its place. Environment variables override file values.
func LoadDaemonConfig() (*DaemonConfig, error) {
	cfg := &DaemonConfig{}

	cfgPath, err := DaemonConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read daemon config: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse daemon config: %w", err)
		}

		// If the new format has no platforms configured, try reading legacy format.
		if cfg.Slack == nil {
			var legacy legacyDaemonConfig
			if jsonErr := json.Unmarshal(data, &legacy); jsonErr == nil &&
				(legacy.SlackBotToken != "" || legacy.SlackAppToken != "" || legacy.AuthorizedSlackUserID != "") {
				// Migrate: backup the old file, write the new format.
				bakPath := cfgPath + ".bak"
				_ = os.WriteFile(bakPath, data, 0600) // best-effort backup
				cfg.Slack = &SlackConfig{
					BotToken:         legacy.SlackBotToken,
					AppToken:         legacy.SlackAppToken,
					AuthorizedUserID: legacy.AuthorizedSlackUserID,
				}
				if saveErr := SaveDaemonConfig(cfg); saveErr != nil {
					return nil, fmt.Errorf("daemon config migration failed: %w", saveErr)
				}
				fmt.Printf("✓ Migrated daemon config to new format (backup: %s)\n", bakPath)
			}
		}
	}

	// Environment variable overrides (highest priority)
	if v := os.Getenv(EnvSlackBotToken); v != "" {
		if cfg.Slack == nil {
			cfg.Slack = &SlackConfig{}
		}
		cfg.Slack.BotToken = v
	}
	if v := os.Getenv(EnvSlackAppToken); v != "" {
		if cfg.Slack == nil {
			cfg.Slack = &SlackConfig{}
		}
		cfg.Slack.AppToken = v
	}
	if v := os.Getenv(EnvDaemonExecutor); v != "" {
		cfg.Executor = v
	}

	cfg.Executor = NormalizeDaemonExecutor(cfg.Executor)

	return cfg, nil
}

// SaveDaemonConfig saves the daemon configuration to ~/.yansu-agent/daemon.json with 0600 permissions.
func SaveDaemonConfig(cfg *DaemonConfig) error {
	configDir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	cfgPath := filepath.Join(configDir, DaemonConfigFileName)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal daemon config: %w", err)
	}
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write daemon config: %w", err)
	}
	return nil
}

// NormalizeDaemonExecutor normalizes executor names and applies default.
func NormalizeDaemonExecutor(executor string) string {
	normalized := strings.ToLower(strings.TrimSpace(executor))
	if normalized == "" {
		return DaemonExecutorClaude
	}
	return normalized
}

// IsSupportedDaemonExecutor reports whether an executor is supported.
func IsSupportedDaemonExecutor(executor string) bool {
	normalized := NormalizeDaemonExecutor(executor)
	switch normalized {
	case DaemonExecutorClaude, DaemonExecutorCodex:
		return true
	}
	return false
}
