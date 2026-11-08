package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"wails-gui/internal/platform/shellenv"
)

// MCPReadConnection never includes a credential. Authorized means a read grant
// exists; Connected additionally checks that the assistant still has our command.
type MCPReadConnection struct {
	Client             string `json:"client"`
	Name               string `json:"name"`
	Command            string `json:"command"`
	Connected          bool   `json:"connected"`
	Authorized         bool   `json:"authorized"`
	Available          bool   `json:"available"`
	AvailabilityReason string `json:"availabilityReason,omitempty"`
	Issue              string `json:"issue,omitempty"`
	ConflictCommand    string `json:"conflictCommand,omitempty"`
}

// Keep subprocess execution injectable so revocation tests never invoke an
// installed assistant. Callers must not expose output or errors to the UI.
var mcpReadRunCLI = func(ctx context.Context, cli string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.WaitDelay = time.Second
	cmd.Dir, _ = os.UserHomeDir()
	return cmd.Output()
}

func mcpReadCLIPath() string {
	// Share the trusted bundle lookup with installation and install.json so a
	// working-directory binary cannot reach registration through either path.
	if path := findBundledYansuCLI(); path != "" {
		return path
	}
	if path := installedYansuCLIPath(); path != "" {
		return path
	}
	return mcpReadPATHExecutable("yansu")
}

func mcpReadPATHExecutable(command string) string {
	path, err := exec.LookPath(command)
	// LookPath can return a relative candidate together with exec.ErrDot.
	// Reject relative results even when GODEBUG disables that error: neither
	// registration nor assistant probes may depend on a working directory.
	if err != nil || !filepath.IsAbs(path) {
		return ""
	}
	return path
}

func mcpReadClaudeEntry() (rawMcpServerEntry, bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return rawMcpServerEntry{}, false, err
	}
	path := filepath.Join(home, ".claude.json")
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		path = filepath.Join(dir, ".claude.json")
	}
	entries, err, _ := readMcpDiscoveryFile(path)
	entry, exists := entries["isoform"]
	return entry, exists, err
}

// Only registrations with the CLI shape Isoform writes may be replaced. A
// different server using the same name remains owned by the user.
func mcpReadIsIsoformClaudeEntry(entry rawMcpServerEntry) bool {
	if entry.URL != "" || (entry.Type != "" && entry.Type != "stdio") {
		return false
	}
	name := filepath.Base(entry.Command)
	if name != "yansu" && name != "yansu.exe" {
		return false
	}
	if len(entry.Args) != 4 && len(entry.Args) != 6 {
		return false
	}
	if !slices.Equal(entry.Args[:4], []string{"mcp", "serve", "--client", "claude-code"}) {
		return false
	}
	return len(entry.Args) == 4 || (entry.Args[4] == "--data-dir" && entry.Args[5] != "")
}

func mcpReadConflictCommand(entry rawMcpServerEntry) string {
	// Args, environment and remote URLs can contain credentials. Show only a
	// simple executable filename; malformed command strings get translated
	// generic copy in the UI instead of exposing their contents.
	name := filepath.Base(entry.Command)
	if name == "." || len(name) > 80 {
		return ""
	}
	for _, char := range name {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && !strings.ContainsRune("._-", char) {
			return ""
		}
	}
	return name
}

func mcpReadServeArgs(client string) []string {
	return []string{"mcp", "serve", "--client", client, "--data-dir", configDirOrFallback()}
}

func mcpReadRegistrationArgs(client, yansu string) []string {
	args := []string{"mcp", "add", "isoform", "--"}
	if client == "claude-code" {
		args = []string{"mcp", "add", "--transport", "stdio", "--scope", "user", "isoform", "--"}
	}
	args = append(args, yansu)
	return append(args, mcpReadServeArgs(client)...)
}

func mcpReadCommandString(command string, args []string) string {
	parts := append([]string{command}, args...)
	for i := range parts {
		parts[i] = shellenv.ShellQuote(parts[i])
	}
	return strings.Join(parts, " ")
}

func mcpReadAssistantMatches(client, cli, yansu string) bool {
	if cli == "" || yansu == "" {
		return false
	}
	wantArgs := mcpReadServeArgs(client)
	switch client {
	case "claude-code":
		entry, exists, err := mcpReadClaudeEntry()
		return err == nil && exists && mcpReadIsIsoformClaudeEntry(entry) && !entry.Disabled && entry.Command == yansu && slices.Equal(entry.Args, wantArgs)
	case "codex":
		// Ask the installed CLI to parse its TOML. `get` does not start servers
		// or perform an auth/health check; no assistant/model session is started.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, cli, "mcp", "get", "isoform", "--json")
		cmd.WaitDelay = time.Second
		cmd.Dir, _ = os.UserHomeDir()
		output, err := cmd.Output()
		if err != nil || len(output) > 1<<20 {
			return false
		}
		var entry struct {
			Enabled   bool `json:"enabled"`
			Transport struct {
				Type    string   `json:"type"`
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"transport"`
		}
		return json.Unmarshal(output, &entry) == nil && entry.Enabled && entry.Transport.Type == "stdio" && entry.Transport.Command == yansu && slices.Equal(entry.Transport.Args, wantArgs)
	default:
		return false
	}
}

func mcpReadConnection(client string) (MCPReadConnection, error) {
	command, err := mcpReadClientCommand(client)
	if err != nil {
		return MCPReadConnection{}, err
	}
	cli := mcpReadPATHExecutable(command)
	yansu := mcpReadCLIPath()
	row := MCPReadConnection{Client: client, Name: "Codex", Available: cli != "" && yansu != ""}
	if cli == "" {
		row.AvailabilityReason = "assistant_missing"
	} else if yansu == "" {
		row.AvailabilityReason = "isoform_missing"
	}
	if client == "claude-code" {
		row.Name = "Claude Code"
		if entry, exists, err := mcpReadClaudeEntry(); err == nil && exists && !mcpReadIsIsoformClaudeEntry(entry) {
			row.Issue = "name_conflict"
			row.ConflictCommand = mcpReadConflictCommand(entry)
		}
	}
	if cli == "" {
		cli = command
	}
	if yansu == "" {
		yansu = "yansu"
	}
	row.Command = mcpReadCommandString(cli, mcpReadRegistrationArgs(client, yansu))
	_, tokenErr := readMCPReadToken(client)
	row.Authorized = tokenErr == nil
	row.Connected = row.Authorized && row.Available && mcpReadAssistantMatches(client, cli, yansu)
	return row, nil
}

func (a *App) GetMCPReadConnections() ([]MCPReadConnection, error) {
	rows := make([]MCPReadConnection, 0, 2)
	for _, client := range []string{"claude-code", "codex"} {
		row, err := mcpReadConnection(client)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// ConnectMCPReadAssistant is exposed to the native Settings button only, never
// browser RPC or MCP. Merely viewing Settings does not issue a token or register.
func (a *App) ConnectMCPReadAssistant(client string) (MCPReadConnection, error) {
	command, err := mcpReadClientCommand(client)
	if err != nil {
		return MCPReadConnection{}, err
	}
	cli := mcpReadPATHExecutable(command)
	if cli == "" {
		return MCPReadConnection{}, errors.New("assistant CLI unavailable")
	}
	yansu := mcpReadCLIPath()
	if yansu == "" {
		return MCPReadConnection{}, errors.New("Isoform CLI unavailable")
	}
	if a == nil || a.bridge == nil {
		return MCPReadConnection{}, errors.New("Isoform is not running")
	}
	mcpReadCredentialMu.Lock()
	defer mcpReadCredentialMu.Unlock()
	replaceClaude := false
	if client == "claude-code" {
		entry, exists, err := mcpReadClaudeEntry()
		if err != nil {
			return MCPReadConnection{}, errors.New("assistant MCP configuration could not be read")
		}
		if exists {
			if !mcpReadIsIsoformClaudeEntry(entry) {
				return MCPReadConnection{}, errors.New("assistant MCP server name conflict")
			}
			replaceClaude = !mcpReadAssistantMatches(client, cli, yansu)
		}
	}
	if err := a.bridge.publishMCPReadEndpoint(); err != nil {
		return MCPReadConnection{}, err
	}
	_, priorErr := readMCPReadToken(client)
	if _, err := issueMCPReadToken(client); err != nil {
		return MCPReadConnection{}, err
	}
	succeeded := false
	defer func() {
		if !succeeded && priorErr != nil {
			path, _ := mcpReadTokenPath(client)
			_ = os.Remove(path)
		}
	}()
	if !mcpReadAssistantMatches(client, cli, yansu) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if replaceClaude {
			remove := exec.CommandContext(ctx, cli, "mcp", "remove", "isoform", "--scope", "user")
			remove.WaitDelay = time.Second
			remove.Dir, _ = os.UserHomeDir()
			if err := remove.Run(); err != nil {
				return MCPReadConnection{}, errors.New("assistant MCP registration could not be replaced")
			}
		}
		cmd := exec.CommandContext(ctx, cli, mcpReadRegistrationArgs(client, yansu)...)
		cmd.WaitDelay = time.Second
		cmd.Dir, _ = os.UserHomeDir()
		// Output can contain configuration values, so never forward it to UI/logs.
		if err := cmd.Run(); err != nil {
			return MCPReadConnection{}, errors.New("assistant MCP registration failed")
		}
	}
	row, err := mcpReadConnection(client)
	if err != nil || !row.Connected {
		return MCPReadConnection{}, errors.New("assistant MCP registration could not be verified")
	}
	succeeded = true
	return row, nil
}

func (a *App) RevokeMCPReadAssistant(client string) (MCPReadConnection, error) {
	path, err := mcpReadTokenPath(client)
	if err != nil {
		return MCPReadConnection{}, err
	}
	mcpReadCredentialMu.Lock()
	defer mcpReadCredentialMu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return MCPReadConnection{}, err
	}
	// Delete authority first, even when inspection or CLI cleanup fails. Keep
	// Connect serialized until cleanup finishes so it cannot restore a grant
	// whose registration this revoke would then remove.
	cleanupErr := mcpReadRemoveRegistration(client)
	row, err := mcpReadConnection(client)
	return row, errors.Join(cleanupErr, err)
}

// A name alone is not ownership. Require the stdio command and exact arguments
// registered for this data root, with no user-added environment or transport.
// A moved Isoform executable is still recognizable; another data root is not.
func mcpReadIsRemovableEntry(client string, entry rawMcpServerEntry) bool {
	name := filepath.Base(entry.Command)
	return filepath.IsAbs(entry.Command) && (name == "yansu" || name == "yansu.exe") &&
		(entry.Type == "" || entry.Type == "stdio") && entry.URL == "" &&
		entry.Transport == nil && len(entry.Env) == 0 && slices.Equal(entry.Args, mcpReadServeArgs(client))
}

func mcpReadRemoveRegistration(client string) error {
	command, err := mcpReadClientCommand(client)
	if err != nil {
		return err
	}
	cli := mcpReadPATHExecutable(command)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	removeArgs := []string{"mcp", "remove", "isoform"}
	switch client {
	case "claude-code":
		// Inspect only the user entry: project/local registrations are never
		// candidates for the user-scoped remove command below.
		entry, exists, err := mcpReadClaudeEntry()
		if err != nil {
			return errors.New("access revoked; assistant MCP configuration could not be read")
		}
		if !exists || !mcpReadIsRemovableEntry(client, entry) {
			return nil
		}
		removeArgs = append(removeArgs, "--scope", "user")
	case "codex":
		if cli == "" {
			return errors.New("access revoked; assistant CLI unavailable for MCP cleanup")
		}
		// Let Codex parse its own config. Listing distinguishes an absent
		// entry from a failed inspection without parsing CLI error text.
		output, err := mcpReadRunCLI(ctx, cli, "mcp", "list", "--json")
		if err != nil || len(output) > 1<<20 {
			return errors.New("access revoked; assistant MCP configuration could not be read")
		}
		var entries []struct {
			Name      string `json:"name"`
			Transport struct {
				rawMcpServerEntry
				EnvVars []string `json:"env_vars"`
				Cwd     string   `json:"cwd"`
			} `json:"transport"`
		}
		if err := json.Unmarshal(output, &entries); err != nil || entries == nil {
			return errors.New("access revoked; assistant MCP configuration could not be read")
		}
		matches := 0
		for _, entry := range entries {
			if entry.Name == "isoform" {
				if entry.Transport.Type != "stdio" || len(entry.Transport.EnvVars) != 0 || entry.Transport.Cwd != "" ||
					!mcpReadIsRemovableEntry(client, entry.Transport.rawMcpServerEntry) {
					return nil
				}
				matches++
			}
		}
		if matches != 1 {
			return nil
		}
	}
	if cli == "" {
		return errors.New("access revoked; assistant CLI unavailable for MCP cleanup")
	}
	if _, err := mcpReadRunCLI(ctx, cli, removeArgs...); err != nil {
		return errors.New("access revoked; assistant MCP registration could not be removed")
	}
	return nil
}
