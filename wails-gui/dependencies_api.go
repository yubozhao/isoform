package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"wails-gui/internal/platform/cmdutil"
)

// Onboarding's dependency check: which assistants and gateways this Mac has,
// and installing the ones it lacks. magpie never needs installing (the user's
// own copy is used when present, Isoform's bundled copy otherwise); the
// Claude Code and Codex CLIs come from npm.

// GitStatus reports whether Git is available locally, without fetching or
// installing anything. Recoverable is true only when no local Git candidate
// exists and the shipped MinGit manifest can be planned for this platform; it
// never authorizes a download by itself.
type GitStatus struct {
	Available   bool   `json:"available"`
	Path        string `json:"path"`
	Recoverable bool   `json:"recoverable"`
}

// gitReadiness resolves the same local candidates the rest of the app uses
// (system Git, then the bundled/installed copy) and, when none exist, whether
// the Windows-only confirmed download manifest is plannable. No network I/O.
func gitReadiness() GitStatus {
	if path := resolveGitPathNow(); path != "" {
		return GitStatus{Available: true, Path: path}
	}
	_, err := gitDownloadEntry(runtime.GOOS + "/" + runtime.GOARCH)
	return GitStatus{Available: false, Recoverable: err == nil}
}

// DependencyStatus is the dependency check the onboarding step shows.
type DependencyStatus struct {
	Magpie       MagpieInstallView `json:"magpie"`
	GatewaySetup SetupMagpieProbe  `json:"gatewaySetup"`
	Claude       CLIToolStatus     `json:"claude"`
	Codex        CLIToolStatus     `json:"codex"`
	// NPM is true when npm is on the login shell's PATH, so Install can work.
	NPM bool `json:"npm"`
	// Ollama is true when a local Ollama answers.
	Ollama bool `json:"ollama"`
	// Git reports the local Git readiness used by the setup and Settings
	// surfaces; it only signals a recoverable download, never performs one.
	Git GitStatus `json:"git"`
}

// CLIInstallResult is what InstallCLITool returns.
type CLIInstallResult struct {
	Tool   string        `json:"tool"`
	OK     bool          `json:"ok"`
	Output string        `json:"output"`
	Status CLIToolStatus `json:"status"`
}

const cliInstallTimeout = 10 * time.Minute

// CheckDependencies reports the gateway and assistant CLIs this Mac has.
func (a *App) CheckDependencies() DependencyStatus {
	status := DependencyStatus{
		Magpie:       a.magpieInstallView(),
		GatewaySetup: a.gatewaySetupAvailability(),
		Claude:       a.CheckLocalCLIStatus("claude"),
		Codex:        a.CheckLocalCLIStatus("codex"),
		NPM:          loginShellHas("npm"),
		Git:          gitReadiness(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status.Ollama = len(detectOllamaModels(ctx, "")) > 0
	return status
}

// InstallCLITool installs Claude Code or Codex with npm through the login
// shell, then re-checks the tool. The output tail comes back for the UI.
func (a *App) InstallCLITool(tool string) (CLIInstallResult, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if tool != "claude" && tool != "codex" {
		return CLIInstallResult{Tool: tool}, fmt.Errorf("unknown tool %q (expected claude or codex)", tool)
	}
	if !loginShellHas("npm") {
		return CLIInstallResult{Tool: tool, Status: a.CheckLocalCLIStatus(tool)},
			errors.New("npm is not installed; install Node.js first, then try again")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliInstallTimeout)
	defer cancel()
	out, err := loginShellOutput(ctx, "npm install -g "+installPkg(tool))
	result := CLIInstallResult{Tool: tool, Output: tailText(out, 4000)}
	if err != nil {
		log.Printf("[dependencies] npm install %s failed: %v", installPkg(tool), err)
		result.Status = a.CheckLocalCLIStatus(tool)
		return result, fmt.Errorf("npm install -g %s: %w", installPkg(tool), err)
	}
	result.Status = a.CheckLocalCLIStatus(tool)
	result.OK = result.Status.Installed
	log.Printf("[dependencies] installed %s (%s)", displayName(tool), result.Status.Version)
	return result, nil
}

// loginShellHas reports whether the user's login shell can find name on its
// PATH (the app's own PATH is narrower than a terminal's).
func loginShellHas(name string) bool {
	if runtime.GOOS == "windows" {
		_, err := exec.LookPath(name)
		return err == nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := loginShellOutput(ctx, "command -v "+name)
	return err == nil && strings.TrimSpace(out) != ""
}

// loginShellOutput runs a command line through the user's login shell.
func loginShellOutput(ctx context.Context, command string) (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = cmdutil.CommandContext(ctx, "cmd", "/D", "/C", command)
	default:
		shell := userLoginShell()
		cmd = cmdutil.CommandContext(ctx, shell, "-lc", command)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func userLoginShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	return "/bin/sh"
}

func tailText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[len(text)-limit:]
}
