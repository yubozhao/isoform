package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// cliStatusTimeout bounds each version or sign-in check, so a CLI that waits
// on the network or a prompt can never hold onboarding's Tools step.
const cliStatusTimeout = 10 * time.Second

// CLIToolStatus describes the local installation and login state of a CLI tool.
type CLIToolStatus struct {
	Tool      string `json:"tool"`      // "claude" or "codex"
	Installed bool   `json:"installed"` // whether the CLI binary exists
	Version   string `json:"version"`   // version string if installed
	LoggedIn  bool   `json:"logged_in"` // whether the user is authenticated
	Message   string `json:"message"`   // user-friendly status description
}

// CheckLocalCLIStatus checks whether a CLI tool is installed and the user is
// logged in on the local machine. This is used both for local "own plan" mode
// and to surface actionable errors in the UI.
func (a *App) CheckLocalCLIStatus(tool string) CLIToolStatus {
	switch tool {
	case "claude":
		return checkLocalCLI("claude", "claude", "claude auth status")
	case "codex":
		return checkLocalCLI("codex", "codex", "codex login status")
	default:
		return CLIToolStatus{
			Tool:    tool,
			Message: fmt.Sprintf("Unknown CLI tool: %s. Supported: claude, codex.", tool),
		}
	}
}

func checkLocalCLI(tool, command, loginCheckCmd string) CLIToolStatus {
	status := CLIToolStatus{Tool: tool}

	// Check if installed.
	versionOut, err := runCLIStatusCommand(command, "--version")
	if err != nil {
		status.Message = fmt.Sprintf("%s is not installed. Please run 'npm install -g %s' to install it.", displayName(tool), installPkg(tool))
		return status
	}
	status.Installed = true
	status.Version = strings.TrimSpace(versionOut)

	// Check login status with the CLI's own status command. It never starts a
	// session or a model call ("claude account" did: Claude Code read it as a
	// prompt).
	parts := strings.Fields(loginCheckCmd)
	loginOut, err := runCLIStatusCommand(parts[0], parts[1:]...)
	if !cliLoggedIn(loginOut, err) {
		status.Message = fmt.Sprintf("%s is installed but not logged in. Please run '%s login' to authenticate.", displayName(tool), command)
		return status
	}

	status.LoggedIn = true
	status.Message = fmt.Sprintf("%s %s, logged in", displayName(tool), status.Version)
	return status
}

// runCLIStatusCommand runs a short, non-interactive CLI check with a timeout
// and no stdin.
func runCLIStatusCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cliStatusTimeout)
	defer cancel()
	out, err := cliProbeCommand(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}

// cliLoggedIn reads the output of `claude auth status` (JSON with loggedIn)
// or `codex login status` (a line such as "Logged in using ChatGPT").
func cliLoggedIn(output string, err error) bool {
	if err != nil {
		return false
	}
	trimmed := strings.TrimSpace(output)
	if strings.HasPrefix(trimmed, "{") {
		var parsed struct {
			LoggedIn *bool `json:"loggedIn"`
		}
		if json.Unmarshal([]byte(trimmed), &parsed) == nil && parsed.LoggedIn != nil {
			return *parsed.LoggedIn
		}
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "not logged in") ||
		strings.Contains(lower, "not authenticated") ||
		strings.Contains(lower, "no account") ||
		strings.Contains(lower, "unauthenticated") {
		return false
	}
	return strings.Contains(lower, "logged in")
}

func displayName(tool string) string {
	switch tool {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	default:
		return tool
	}
}

func installPkg(tool string) string {
	switch tool {
	case "claude":
		return "@anthropic-ai/claude-code"
	case "codex":
		return "@openai/codex"
	default:
		return tool
	}
}
