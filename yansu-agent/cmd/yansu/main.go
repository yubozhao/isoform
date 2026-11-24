package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"yansu-agent/cronsecret"
	"yansu-agent/internal/auth"
	"yansu-agent/internal/client"
	"yansu-agent/internal/config"
	daemonpkg "yansu-agent/internal/daemon"
	"yansu-agent/internal/knowledge"
	"yansu-agent/internal/updater"
	"yansu-agent/runtoken"
)

var (
	version = "0.3.1"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--isoform-sandbox-capability" {
		fmt.Println(sandboxCapability)
		return
	}
	if err := checkSandboxCommand(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:     "yansu",
	Short:   "Yansu Agent - Local development companion",
	Long:    "Local development companion for Isoform.",
	Version: version,
}

// ============================================================================
// Login/Logout Commands
// ============================================================================

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "The Yansu service is retired",
	RunE:  retiredNotice,
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out from Yansu",
	RunE: func(cmd *cobra.Command, args []string) error {
		return auth.Logout()
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication and sync status",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Auth status
		if err := auth.Status(); err != nil {
			return err
		}

		fmt.Println()

		// Project status
		projectRoot, err := config.FindProjectRoot()
		if err != nil {
			fmt.Println("Project: Not in a Yansu project")
			return nil
		}

		somethingDir := config.GetSomethingDir(projectRoot)
		projectCfg, err := config.LoadProject(somethingDir)
		if err != nil {
			fmt.Println("Project: Configuration error")
			return nil
		}

		fmt.Printf("Project: %s\n", projectCfg.Name)
		fmt.Printf("  Organization: %s\n", projectCfg.Organization)
		fmt.Printf("  Product: %s\n", projectCfg.Product)
		if !projectCfg.LastSyncedAt.IsZero() {
			fmt.Printf("  Last Synced: %s\n", projectCfg.LastSyncedAt.Format(time.RFC3339))
		}

		// Knowledge stats
		knowledgeList, _ := knowledge.LoadAll(filepath.Join(somethingDir, "knowledge"))
		fmt.Printf("  Knowledge Items: %d\n", len(knowledgeList))

		// Pending changes
		newKnowledgeDir := filepath.Join(somethingDir, "new-knowledge")
		entries, _ := os.ReadDir(newKnowledgeDir)
		pending := 0
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
				pending++
			}
		}
		if pending > 0 {
			fmt.Printf("  Pending Changes: %d\n", pending)
		}

		return nil
	},
}

// ============================================================================
// Clone Command
// ============================================================================

var cloneCmd = &cobra.Command{
	Use:   "clone <org/product/project>",
	Short: "The Yansu service is retired",
	Args:  cobra.ExactArgs(1),
	RunE:  retiredError,
}

func splitProjectRef(ref string) []string {
	var parts []string
	var current string
	for _, c := range ref {
		if c == '/' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ============================================================================
// Sync Command
// ============================================================================

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "The Yansu service is retired",
	RunE:  retiredError,
}

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "The Yansu service is retired",
	RunE:  retiredError,
}

var pushCmd = &cobra.Command{
	Use:   "push",
	Short: "Report that the Yansu service is retired",
	RunE:  retiredNotice,
}

func init() {
	for _, name := range []string{"code", "knowledge", "knowledge-only"} {
		pushCmd.Flags().Bool(name, false, "Accepted for compatibility with installed hooks")
	}
}

func retiredNotice(cmd *cobra.Command, args []string) error {
	fmt.Fprintln(cmd.OutOrStdout(), "The Yansu service is retired; nothing was sent.")
	return nil
}

func retiredError(cmd *cobra.Command, args []string) error { return client.ErrRetired }

var analyzeAutoFlag bool
var analyzeCommitsFlag int

var analyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "Analyze recent changes and suggest knowledge to capture",
	Long: `Analyze recent code changes and generate knowledge capture suggestions.

This command examines your recent git commits and staged changes to identify
patterns, decisions, and learnings that should be captured as knowledge.

Examples:
  yansu analyze              # Interactive mode: review and confirm each suggestion
  yansu analyze --auto       # Auto mode: generate drafts without confirmation
  yansu analyze --commits 5  # Analyze the last 5 commits`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAnalyze()
	},
}

func init() {
	analyzeCmd.Flags().BoolVar(&analyzeAutoFlag, "auto", false, "Automatically generate knowledge drafts without confirmation")
	analyzeCmd.Flags().IntVar(&analyzeCommitsFlag, "commits", 1, "Number of recent commits to analyze")
}

func runAnalyze() error {
	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a Yansu project: %w", err)
	}

	// Collect git information
	fmt.Println("Analyzing recent changes...")
	fmt.Println()

	// Get git diff for staged changes
	stagedDiff, err := getGitDiff(projectRoot, true)
	if err != nil {
		stagedDiff = ""
	}

	// Get git diff for unstaged changes
	unstagedDiff, err := getGitDiff(projectRoot, false)
	if err != nil {
		unstagedDiff = ""
	}

	// Get recent commit messages and diffs
	commitInfo, err := getRecentCommits(projectRoot, analyzeCommitsFlag)
	if err != nil {
		commitInfo = ""
	}

	// Get list of changed files
	changedFiles, err := getChangedFiles(projectRoot)
	if err != nil {
		changedFiles = []string{}
	}

	if stagedDiff == "" && unstagedDiff == "" && commitInfo == "" {
		fmt.Println("No changes detected.")
		fmt.Println()
		fmt.Println("Tips:")
		fmt.Println("  - Make some code changes first")
		fmt.Println("  - Use --commits N to analyze more historical commits")
		return nil
	}

	// Display summary
	fmt.Println("=== Changes Summary ===")
	fmt.Println()

	if len(changedFiles) > 0 {
		fmt.Printf("Changed files (%d):\n", len(changedFiles))
		for _, f := range changedFiles {
			fmt.Printf("  • %s\n", f)
		}
		fmt.Println()
	}

	if stagedDiff != "" {
		lines := countLines(stagedDiff)
		fmt.Printf("Staged changes: %d lines\n", lines)
	}

	if unstagedDiff != "" {
		lines := countLines(unstagedDiff)
		fmt.Printf("Unstaged changes: %d lines\n", lines)
	}

	if commitInfo != "" {
		fmt.Printf("Recent commits analyzed: %d\n", analyzeCommitsFlag)
	}

	fmt.Println()

	// Generate knowledge draft
	somethingDir := config.GetSomethingDir(projectRoot)
	draftDir := filepath.Join(somethingDir, "new-knowledge")
	os.MkdirAll(draftDir, 0755)

	// Create a draft file with the analysis
	timestamp := time.Now().Format("20060102-150405")
	draftFile := filepath.Join(draftDir, fmt.Sprintf("draft-%s.md", timestamp))

	draftContent := generateKnowledgeDraft(stagedDiff, unstagedDiff, commitInfo, changedFiles)

	if analyzeAutoFlag {
		// Auto mode: write draft directly
		if err := os.WriteFile(draftFile, []byte(draftContent), 0644); err != nil {
			return fmt.Errorf("failed to write draft: %w", err)
		}
		fmt.Printf("✓ Knowledge draft created: %s\n", draftFile)
		fmt.Println()
		fmt.Println("Next steps:")
		fmt.Println("  1. Edit the draft file to refine the trigger and content")
		fmt.Println("  2. Run 'yansu push' to upload the knowledge")
	} else {
		// Interactive mode: show draft and ask for confirmation
		fmt.Println("=== Suggested Knowledge Draft ===")
		fmt.Println()
		fmt.Println(draftContent)
		fmt.Println()

		fmt.Print("Save this draft? [y/N]: ")
		var response string
		fmt.Scanln(&response)

		if response == "y" || response == "Y" || response == "yes" {
			if err := os.WriteFile(draftFile, []byte(draftContent), 0644); err != nil {
				return fmt.Errorf("failed to write draft: %w", err)
			}
			fmt.Printf("✓ Draft saved to: %s\n", draftFile)
			fmt.Println()
			fmt.Println("Next steps:")
			fmt.Println("  1. Edit the draft file to refine the trigger and content")
			fmt.Println("  2. Run 'yansu push' to upload the knowledge")
		} else {
			fmt.Println("Draft discarded.")
		}
	}

	return nil
}

//isoform:destination local
func getGitDiff(projectRoot string, staged bool) (string, error) {
	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--no-color")

	cmd := exec.Command("git", args...)
	cmd.Dir = projectRoot
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

//isoform:destination local
func getRecentCommits(projectRoot string, count int) (string, error) {
	// Get commit messages and stats
	cmd := exec.Command("git", "log", fmt.Sprintf("-%d", count), "--pretty=format:=== Commit: %h ===\n%s\n%b", "--stat", "--no-color")
	cmd.Dir = projectRoot
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

//isoform:destination local
func getChangedFiles(projectRoot string) ([]string, error) {
	// Get both staged and unstaged changed files
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = projectRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var files []string
	lines := splitLines(string(output))
	for _, line := range lines {
		if len(line) > 3 {
			files = append(files, line[3:])
		}
	}
	return files, nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return len(splitLines(s))
}

func splitLines(s string) []string {
	var lines []string
	var current string
	for _, c := range s {
		if c == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func generateKnowledgeDraft(stagedDiff, unstagedDiff, commitInfo string, changedFiles []string) string {
	// Build a summary of what changed
	var filesSummary string
	if len(changedFiles) > 0 {
		filesSummary = "Files changed:\n"
		for _, f := range changedFiles {
			filesSummary += fmt.Sprintf("- %s\n", f)
		}
	}

	// Truncate diffs if too long
	maxDiffLen := 2000
	if len(stagedDiff) > maxDiffLen {
		stagedDiff = stagedDiff[:maxDiffLen] + "\n... (truncated)"
	}
	if len(unstagedDiff) > maxDiffLen {
		unstagedDiff = unstagedDiff[:maxDiffLen] + "\n... (truncated)"
	}
	if len(commitInfo) > maxDiffLen {
		commitInfo = commitInfo[:maxDiffLen] + "\n... (truncated)"
	}

	draft := `# Action
create

# Trigger
TODO: Describe when this knowledge should be triggered
Example: "How to handle X in this project" or "Pattern for Y"

# Content

## Summary

TODO: Describe the pattern, decision, or learning from these changes.

## Context

` + filesSummary

	if commitInfo != "" {
		draft += `
## Recent Commits

` + "```" + `
` + commitInfo + `
` + "```" + `
`
	}

	if stagedDiff != "" || unstagedDiff != "" {
		draft += `
## Code Changes

`
		if stagedDiff != "" {
			draft += `### Staged Changes
` + "```diff" + `
` + stagedDiff + `
` + "```" + `
`
		}
		if unstagedDiff != "" {
			draft += `### Unstaged Changes
` + "```diff" + `
` + unstagedDiff + `
` + "```" + `
`
		}
	}

	draft += `
## Key Learnings

TODO: What should future developers (or AI agents) know about this?

1.
2.
3.
`

	return draft
}

// ============================================================================
// Hook Commands
// ============================================================================

var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "Manage legacy Yansu Git hooks",
}

var hookInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "The Yansu service is retired",
	RunE:  retiredError,
}

var hookUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove git hooks",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookUninstall()
	},
}

var hookStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show git hook status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookStatus()
	},
}

var hookRunCmd = &cobra.Command{
	Use:    "run",
	Short:  "The Yansu service is retired",
	Hidden: true,
	RunE:   retiredNotice,
}

func runHookUninstall() error {
	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a Yansu project: %w", err)
	}

	hookPath := filepath.Join(projectRoot, ".git", "hooks", "post-commit")

	data, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No hook installed.")
			return nil
		}
		return fmt.Errorf("failed to read hook: %w", err)
	}

	content := string(data)

	// Remove yansu hook section
	if !contains(content, "yansu hook run") {
		fmt.Println("Yansu hook is not installed.")
		return nil
	}

	// Remove yansu hook lines
	lines := splitLines(content)
	var newLines []string
	inYansuSection := false

	for _, line := range lines {
		if contains(line, "Yansu Agent") {
			inYansuSection = true
			continue
		}
		if inYansuSection && contains(line, "yansu hook run") {
			inYansuSection = false
			continue
		}
		if inYansuSection && (line == "" || line[0] == '#') {
			continue
		}
		inYansuSection = false
		newLines = append(newLines, line)
	}

	newContent := ""
	for i, line := range newLines {
		if i > 0 {
			newContent += "\n"
		}
		newContent += line
	}

	// Clean up empty content
	trimmed := newContent
	for len(trimmed) > 0 && (trimmed[0] == '\n' || trimmed[0] == ' ' || trimmed[0] == '\t') {
		trimmed = trimmed[1:]
	}
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == '\n' || trimmed[len(trimmed)-1] == ' ' || trimmed[len(trimmed)-1] == '\t') {
		trimmed = trimmed[:len(trimmed)-1]
	}

	if trimmed == "" || trimmed == "#!/bin/sh" {
		// Remove empty hook file
		os.Remove(hookPath)
	} else {
		if err := os.WriteFile(hookPath, []byte(newContent), 0755); err != nil {
			return fmt.Errorf("failed to write hook: %w", err)
		}
	}

	fmt.Println("✓ Yansu hook uninstalled")
	return nil
}

func runHookStatus() error {
	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a Yansu project: %w", err)
	}

	hookPath := filepath.Join(projectRoot, ".git", "hooks", "post-commit")

	data, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Hook status: Not installed")
			fmt.Println()
			fmt.Println("Run 'yansu hook install' to enable automatic knowledge capture")
			return nil
		}
		return fmt.Errorf("failed to read hook: %w", err)
	}

	if contains(string(data), "yansu hook run") {
		fmt.Println("Hook status: Installed ✓")
		fmt.Println()
		fmt.Println("Automatic knowledge capture is enabled.")
		fmt.Println("Each commit will be analyzed by AI to extract knowledge.")
	} else {
		fmt.Println("Hook status: Not installed")
		fmt.Println()
		fmt.Println("Run 'yansu hook install' to enable automatic knowledge capture")
	}

	return nil
}

var updateForceFlag bool

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update yansu to the latest version",
	Long: `Check for updates and download the latest version of yansu.

Examples:
  yansu update          # Update if a newer version is available
  yansu update --force  # Force update even if already on latest version`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUpdate()
	},
}

func init() {
	updateCmd.Flags().BoolVarP(&updateForceFlag, "force", "f", false, "Force update even if already on latest version")
}

func runUpdate() error {
	ctx := context.Background()

	fmt.Printf("Current version: %s\n", version)
	fmt.Print("Checking for updates... ")

	result, err := updater.Update(ctx, version, updateForceFlag)
	if err != nil {
		fmt.Println("failed")
		return err
	}

	fmt.Printf("latest version: %s\n", result.LatestVersion)

	if !result.Updated {
		if result.CurrentVersion == result.LatestVersion || !updateForceFlag {
			fmt.Println("Already up to date.")
		}
		return nil
	}

	fmt.Printf("\n✓ Updated to %s\n", result.LatestVersion)
	fmt.Println("Please restart yansu to use the new version.")
	return nil
}

// ============================================================================
// Daemon Commands
// ============================================================================

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the retired Yansu daemon",
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon (runs until Ctrl+C or SIGTERM)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemonpkg.Run(cmd.Context())
	},
}

var daemonSetExecutorCmd = &cobra.Command{
	Use:       "set-executor [claude|codex]",
	Short:     "Set daemon AI executor for free-text tasks",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{config.DaemonExecutorClaude, config.DaemonExecutorCodex},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDaemonSetExecutor(args[0])
	},
}

var daemonRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register the current project directory (prefer: yansu local register)",
	Long: `Register the current directory as a known yansu project so the daemon
can route Slack commands to it.

Prefer using 'yansu local register' which pairs naturally with 'yansu local unregister'.
This alias is kept for backwards compatibility.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDaemonRegister()
	},
}

func validateDaemonExecutor(executor string) error {
	if config.IsSupportedDaemonExecutor(executor) {
		return nil
	}
	return fmt.Errorf("daemon executor %q is not supported (supported: %s, %s)",
		strings.TrimSpace(executor), config.DaemonExecutorClaude, config.DaemonExecutorCodex)
}

func runDaemonSetExecutor(rawExecutor string) error {
	daemonCfg, err := config.LoadDaemonConfig()
	if err != nil {
		return fmt.Errorf("failed to load daemon config: %w", err)
	}

	executor := config.NormalizeDaemonExecutor(rawExecutor)
	if err := validateDaemonExecutor(executor); err != nil {
		return err
	}

	daemonCfg.Executor = executor
	if err := config.SaveDaemonConfig(daemonCfg); err != nil {
		return fmt.Errorf("failed to save daemon config: %w", err)
	}

	fmt.Printf("✓ Daemon executor set to %q\n", executor)
	fmt.Println("Restart daemon to apply:")
	fmt.Println("  yansu daemon stop")
	fmt.Println("  yansu daemon start")
	return nil
}

func runDaemonRegister() error {
	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a yansu project (no .something/project.json found): %w", err)
	}

	somethingDir := config.GetSomethingDir(projectRoot)
	projectCfg, err := config.LoadProject(somethingDir)
	if err != nil {
		return fmt.Errorf("failed to load project config: %w", err)
	}

	ref := fmt.Sprintf("%s/%s/%s", projectCfg.Organization, projectCfg.Product, projectCfg.Name)

	registry := daemonpkg.NewProjectRegistry()
	if err := registry.Load(); err != nil {
		// registry.Load returns nil for os.IsNotExist; any other error means the
		// file is corrupt or unreadable — warn but still proceed so the user can
		// overwrite the broken entry.
		fmt.Fprintf(os.Stderr, "warning: failed to load project registry: %v\n", err)
	}
	if err := registry.Register(ref, projectRoot); err != nil {
		return fmt.Errorf("failed to register project: %w", err)
	}

	fmt.Printf("✓ Registered: %s → %s\n", ref, projectRoot)
	return nil
}

// ============================================================================
// Service Commands
// ============================================================================

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage the yansu daemon system service",
}

var serviceForce bool

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Register yansu daemon as a launchd/systemd user service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceInstall(serviceForce)
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove yansu daemon from system services",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceUninstall()
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show system service status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServiceStatus()
	},
}

var serviceStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running daemon (without uninstalling)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemonpkg.StopService()
	},
}

var serviceRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemonpkg.RestartService()
	},
}

func runServiceInstall(force bool) error {
	// Pre-flight: check login (matches pattern from runWhoami)
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Token == "" {
		return fmt.Errorf("not authenticated — run 'yansu login' first")
	}

	// Load daemon config. LoadDaemonConfig() merges file + env vars and returns nil error
	// with empty struct when file is missing — which is fine if env vars fill it in.
	// Real errors (parse errors, permission issues) are returned as non-nil.
	daemonCfg, err := config.LoadDaemonConfig()
	if err != nil {
		return fmt.Errorf("failed to load daemon config: %w", err)
	}

	// Daemon now always uses relay mode (auto-discovered at startup); no manual config needed.

	// Pre-flight: executor validation (prevents install of service that will fail on start)
	if err := validateDaemonExecutor(daemonCfg.Executor); err != nil {
		return err
	}

	// Warn (non-fatal) if no projects registered
	registry := daemonpkg.NewProjectRegistry()
	_ = registry.Load()
	if len(registry.List()) == 0 {
		fmt.Println("Warning: no projects registered. Run 'yansu local register' in a project directory after install.")
	}

	yansuPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to determine yansu binary path: %w", err)
	}
	return daemonpkg.InstallService(yansuPath, "", force)
}

func runServiceUninstall() error {
	return daemonpkg.UninstallService()
}

func runServiceStatus() error {
	return daemonpkg.ServiceStatus()
}

// ============================================================================
// whoami
// ============================================================================

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show the currently authenticated user",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runWhoami()
	},
}

func runWhoami() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Token == "" {
		return fmt.Errorf("not authenticated (run 'yansu login' first)")
	}
	ctx := context.Background()
	apiClient := client.New(cfg.BaseURL, cfg.Token)
	profile, err := apiClient.GetProfile(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch profile: %w", err)
	}
	fmt.Printf("User:  %s\n", profile.Name)
	if profile.DisplayName != "" && profile.DisplayName != profile.Name {
		fmt.Printf("       (%s)\n", profile.DisplayName)
	}
	fmt.Printf("Email: %s\n", profile.Email)
	fmt.Printf("URL:   %s\n", cfg.BaseURL)
	return nil
}

// ============================================================================
// setup-token
// ============================================================================

var setupTokenName string
var setupTokenExpiresIn int

var setupTokenCmd = &cobra.Command{
	Use:   "setup-token",
	Short: "The Yansu service is retired",
	RunE:  retiredError,
}

var listCmd = &cobra.Command{
	Use:     "list [org | org/product | org/product/project]",
	Aliases: []string{"ls"},
	Short:   "List cloud organizations, products, or projects",
	Long: `Browse the Yansu cloud hierarchy:

  yansu list                         — list your organizations
  yansu list <org>                   — list products in an org
  yansu list <org>/<product>         — list projects in a product
  yansu list <org>/<product>/<proj>  — show project details

Alias: yansu ls`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := ""
		if len(args) > 0 {
			path = args[0]
		}
		return runLs(path)
	},
}

func runLs(path string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Token == "" {
		return fmt.Errorf("not authenticated (run 'yansu login' first)")
	}
	ctx := context.Background()
	apiClient := client.New(cfg.BaseURL, cfg.Token)

	parts := splitProjectRef(path)
	switch len(parts) {
	case 0: // list orgs
		orgs, err := apiClient.ListOrganizations(ctx)
		if err != nil {
			return fmt.Errorf("failed to list organizations: %w", err)
		}
		if len(orgs) == 0 {
			fmt.Println("No organizations found.")
			return nil
		}
		fmt.Printf("Organizations (%d):\n", len(orgs))
		for _, o := range orgs {
			display := o.Name
			if o.DisplayName != "" && o.DisplayName != o.Name {
				display = fmt.Sprintf("%s  (%s)", o.Name, o.DisplayName)
			}
			fmt.Printf("  • %s\n", display)
		}
		fmt.Printf("\nRun `yansu ls <org>` to list products.\n")

	case 1: // list products in org
		org := parts[0]
		products, err := apiClient.ListProducts(ctx, org)
		if err != nil {
			return fmt.Errorf("failed to list products: %w", err)
		}
		if len(products) == 0 {
			fmt.Printf("No products found in org %q.\n", org)
			return nil
		}
		fmt.Printf("Products in %s (%d):\n", org, len(products))
		for _, p := range products {
			display := p.Name
			if p.DisplayName != "" && p.DisplayName != p.Name {
				display = fmt.Sprintf("%s  (%s)", p.Name, p.DisplayName)
			}
			fmt.Printf("  • %s\n", display)
		}
		fmt.Printf("\nRun `yansu ls %s/<product>` to list projects.\n", org)

	case 2: // list projects in product
		org, product := parts[0], parts[1]
		projects, err := apiClient.ListProjectsInProduct(ctx, org, product)
		if err != nil {
			return fmt.Errorf("failed to list projects: %w", err)
		}
		if len(projects) == 0 {
			fmt.Printf("No projects found in %s/%s.\n", org, product)
			return nil
		}
		fmt.Printf("Projects in %s/%s (%d):\n", org, product, len(projects))
		for _, p := range projects {
			branch := ""
			if p.CodingBranch != "" {
				branch = fmt.Sprintf("  [branch: %s]", p.CodingBranch)
			}
			fmt.Printf("  • %s/%s/%s%s\n", org, product, p.Name, branch)
		}
		fmt.Printf("\nRun `yansu clone %s/<project>` to clone a project.\n", org+"/"+product)

	case 3: // show single project
		org, product, proj := parts[0], parts[1], parts[2]
		p, err := apiClient.GetProject(ctx, org, product, proj)
		if err != nil {
			return fmt.Errorf("failed to get project: %w", err)
		}
		fmt.Printf("Project:  %s/%s/%s\n", org, product, p.Name)
		if p.CodingBranch != "" {
			fmt.Printf("Branch:   %s\n", p.CodingBranch)
		}
		fmt.Printf("UUID:     %s\n", p.UUID)
		fmt.Printf("Created:  %s\n", p.CreatedAt.Format("2006-01-02"))

	default:
		return fmt.Errorf("too many path components; expected org, org/product, or org/product/project")
	}
	return nil
}

// ============================================================================
// local — manage locally registered projects
// ============================================================================

var localCmd = &cobra.Command{
	Use:   "local",
	Short: "Manage locally registered projects",
}

var localListCmd = &cobra.Command{
	Use:   "list",
	Short: "List locally registered projects",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLocalList()
	},
}

var localRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register the current project directory with the local daemon",
	Long: `Register the current directory as a known yansu project so the daemon
can route Slack commands to it.

Run this command inside a yansu project directory (one containing .something/project.json).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDaemonRegister()
	},
}

var localUnregisterCmd = &cobra.Command{
	Use:   "unregister [ref]",
	Short: "Remove a project from the local daemon registry",
	Long: `Remove a project from the local daemon registry.
If no ref is given, uses the project in the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := ""
		if len(args) > 0 {
			ref = args[0]
		}
		return runLocalUnregister(ref)
	},
}

var localPathCmd = &cobra.Command{
	Use:   "path <ref>",
	Short: "Print the local filesystem path for a registered project",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLocalPath(args[0])
	},
}

func runLocalList() error {
	registry := daemonpkg.NewProjectRegistry()
	if err := registry.Load(); err != nil {
		return fmt.Errorf("failed to load project registry: %w", err)
	}
	entries := registry.List()
	if len(entries) == 0 {
		fmt.Println("No projects registered locally.")
		fmt.Println("Run `yansu clone <ref>` or `yansu daemon register` in a project directory.")
		return nil
	}
	fmt.Printf("Locally registered projects (%d):\n", len(entries))
	for _, e := range entries {
		fmt.Printf("  • %-40s  %s\n", e.Ref, e.Path)
	}
	return nil
}

func runLocalUnregister(ref string) error {
	if ref == "" {
		projectRoot, err := config.FindProjectRoot()
		if err != nil {
			return fmt.Errorf("not in a yansu project and no ref given: %w", err)
		}
		somethingDir := config.GetSomethingDir(projectRoot)
		projectCfg, err := config.LoadProject(somethingDir)
		if err != nil {
			return fmt.Errorf("failed to load project config: %w", err)
		}
		ref = fmt.Sprintf("%s/%s/%s", projectCfg.Organization, projectCfg.Product, projectCfg.Name)
	}

	registry := daemonpkg.NewProjectRegistry()
	if err := registry.Load(); err != nil {
		return fmt.Errorf("failed to load project registry: %w", err)
	}
	if err := registry.Unregister(ref); err != nil {
		return fmt.Errorf("failed to unregister project: %w", err)
	}
	fmt.Printf("✓ Unregistered: %s\n", ref)
	return nil
}

func runLocalPath(ref string) error {
	registry := daemonpkg.NewProjectRegistry()
	if err := registry.Load(); err != nil {
		return fmt.Errorf("failed to load project registry: %w", err)
	}
	path, err := registry.Resolve(ref)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// ============================================================================
// init — initialise an existing directory as a yansu project (no git clone)
// ============================================================================

var projectInitCmd = &cobra.Command{
	Use:   "init <org/product/project>",
	Short: "The Yansu service is retired",
	Args:  cobra.ExactArgs(1),
	RunE:  retiredError,
}

var daemonProcessStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the yansu daemon process is running",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDaemonProcessStatus()
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running yansu daemon process",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDaemonStop()
	},
}

var daemonRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the yansu daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDaemonRestart(cmd.Context())
	},
}

var daemonLogsFollowFlag bool
var daemonLogsLinesFlag int

var daemonLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show daemon log output",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDaemonLogs(daemonLogsFollowFlag, daemonLogsLinesFlag)
	},
}

func runDaemonProcessStatus() error {
	pid, err := config.ReadPIDFile()
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}
	if pid == 0 {
		fmt.Println("Daemon: not running (no PID file)")
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("Daemon: not running (PID %d not found)\n", pid)
		config.RemovePIDFile()
		return nil
	}
	// On Unix, FindProcess always succeeds; send signal 0 to test liveness.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		fmt.Printf("Daemon: not running (PID %d is dead)\n", pid)
		config.RemovePIDFile()
		return nil
	}
	logPath, _ := config.LogFilePath()
	fmt.Printf("Daemon: running (PID %d)\n", pid)
	if logPath != "" {
		fmt.Printf("Logs:   %s\n", logPath)
	}
	return nil
}

func runDaemonStop() error {
	pid, err := config.ReadPIDFile()
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}
	if pid == 0 {
		fmt.Println("Daemon is not running.")
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("No process with PID %d found.\n", pid)
		config.RemovePIDFile()
		return nil
	}
	// Verify the process is still alive before signaling.
	// Note: this guards against already-dead processes but does NOT verify process identity;
	// if the daemon crashed without cleaning up the PID file and the PID was reused by another
	// process, SIGTERM would be sent to that unrelated process (a known limitation of PID files).
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		fmt.Printf("Daemon (PID %d) is already dead; cleaning up stale PID file.\n", pid)
		config.RemovePIDFile()
		return nil
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM to PID %d: %w", pid, err)
	}
	// Wait for the process to actually exit before returning.
	fmt.Printf("Stopping daemon (PID %d)...\n", pid)
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			config.RemovePIDFile()
			fmt.Println("✓ Daemon stopped.")
			return nil
		}
	}
	fmt.Printf("Warning: daemon (PID %d) has not exited after 5 seconds.\n", pid)
	return nil
}

func runDaemonRestart(ctx context.Context) error {
	pid, err := config.ReadPIDFile()
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}
	if pid != 0 {
		proc, findErr := os.FindProcess(pid)
		if findErr == nil {
			if sigErr := proc.Signal(syscall.SIGTERM); sigErr == nil {
				fmt.Printf("Stopping daemon (PID %d)...\n", pid)
				// Wait up to 5 seconds for the process to exit.
				exited := false
				for i := 0; i < 10; i++ {
					time.Sleep(500 * time.Millisecond)
					if err2 := proc.Signal(syscall.Signal(0)); err2 != nil {
						exited = true
						break
					}
				}
				if !exited {
					return fmt.Errorf("daemon (PID %d) did not exit within 5 seconds; use 'kill -9 %d' to force-stop it", pid, pid)
				}
			}
		}
		config.RemovePIDFile()
	}
	fmt.Println("Starting daemon...")
	return daemonpkg.Run(ctx)
}

func runDaemonLogs(follow bool, lines int) error {
	logPath, err := config.LogFilePath()
	if err != nil {
		return fmt.Errorf("failed to determine log file path: %w", err)
	}
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return fmt.Errorf("log file not found: %s\n(Hint: install as a service with `yansu service install` for persistent logs)", logPath)
	}
	args := []string{fmt.Sprintf("-%d", lines)}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, logPath)
	cmd := exec.Command("tail", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ============================================================================
// knowledge — per-project knowledge CRUD
// ============================================================================

var knowledgeCmd = &cobra.Command{
	Use:   "knowledge",
	Short: "Manage local knowledge for the current project",
}

var knowledgeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all knowledge items in the current project",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runKnowledgeList()
	},
}

var knowledgeSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search knowledge items by trigger or content",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runKnowledgeSearch(args[0])
	},
}

var knowledgeShowCmd = &cobra.Command{
	Use:   "show <uuid-or-filename>",
	Short: "Show the full content of a knowledge item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runKnowledgeShow(args[0])
	},
}

var knowledgeAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new knowledge item to the current project",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runKnowledgeAdd()
	},
}

var knowledgeDeleteCmd = &cobra.Command{
	Use:   "delete <uuid-or-filename>",
	Short: "Delete a knowledge item from the current project",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runKnowledgeDelete(args[0])
	},
}

func getKnowledgeDir() (string, error) {
	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		return "", fmt.Errorf("not in a yansu project: %w", err)
	}
	return filepath.Join(config.GetSomethingDir(projectRoot), "knowledge"), nil
}

func runKnowledgeList() error {
	knowledgeDir, err := getKnowledgeDir()
	if err != nil {
		return err
	}
	items, err := knowledge.LoadAll(knowledgeDir)
	if err != nil {
		return fmt.Errorf("failed to load knowledge: %w", err)
	}
	if len(items) == 0 {
		fmt.Println("No knowledge items found.")
		return nil
	}
	fmt.Printf("Knowledge items (%d):\n\n", len(items))
	for _, k := range items {
		uuid := k.UUID
		if uuid == "" {
			uuid = "(no uuid)"
		}
		tags := ""
		if len(k.Tags) > 0 {
			tags = "  [" + strings.Join(k.Tags, ", ") + "]"
		}
		fmt.Printf("  %s%s\n    %s\n", k.Trigger, tags, uuid)
	}
	return nil
}

func runKnowledgeSearch(query string) error {
	knowledgeDir, err := getKnowledgeDir()
	if err != nil {
		return err
	}
	items, err := knowledge.LoadAll(knowledgeDir)
	if err != nil {
		return fmt.Errorf("failed to load knowledge: %w", err)
	}
	q := strings.ToLower(query)
	var matches []*knowledge.Knowledge
	for _, k := range items {
		if strings.Contains(strings.ToLower(k.Trigger), q) ||
			strings.Contains(strings.ToLower(k.Content), q) {
			matches = append(matches, k)
		}
	}
	if len(matches) == 0 {
		fmt.Printf("No knowledge items match %q.\n", query)
		return nil
	}
	fmt.Printf("Matches for %q (%d):\n\n", query, len(matches))
	for _, k := range matches {
		fmt.Printf("  Trigger: %s\n", k.Trigger)
		fmt.Printf("  File:    %s\n", filepath.Base(k.FilePath))
		if k.UUID != "" {
			fmt.Printf("  UUID:    %s\n", k.UUID)
		}
		// Show first 120 chars of content as preview
		preview := k.Content
		if len(preview) > 120 {
			preview = preview[:120] + "..."
		}
		fmt.Printf("  Preview: %s\n\n", strings.ReplaceAll(preview, "\n", " "))
	}
	return nil
}

func runKnowledgeShow(identifier string) error {
	knowledgeDir, err := getKnowledgeDir()
	if err != nil {
		return err
	}
	items, err := knowledge.LoadAll(knowledgeDir)
	if err != nil {
		return fmt.Errorf("failed to load knowledge: %w", err)
	}
	id := strings.ToLower(identifier)
	for _, k := range items {
		if strings.ToLower(k.UUID) == id ||
			strings.EqualFold(filepath.Base(k.FilePath), identifier) ||
			strings.EqualFold(filepath.Base(k.FilePath), identifier+".md") {
			fmt.Printf("Trigger: %s\n", k.Trigger)
			if k.UUID != "" {
				fmt.Printf("UUID:    %s\n", k.UUID)
			}
			if len(k.Tags) > 0 {
				fmt.Printf("Tags:    %s\n", strings.Join(k.Tags, ", "))
			}
			fmt.Printf("File:    %s\n\n", k.FilePath)
			fmt.Println(k.Content)
			return nil
		}
	}
	return fmt.Errorf("knowledge item %q not found", identifier)
}

func runKnowledgeAdd() error {
	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a yansu project: %w", err)
	}
	somethingDir := config.GetSomethingDir(projectRoot)

	stdinScanner := bufio.NewScanner(os.Stdin)

	fmt.Print("Trigger (the question or scenario that should surface this knowledge):\n> ")
	var trigger string
	if stdinScanner.Scan() {
		trigger = strings.TrimSpace(stdinScanner.Text())
	}
	if trigger == "" {
		return fmt.Errorf("trigger cannot be empty")
	}

	fmt.Println("Content (what an AI should know; end with a line containing only '---'):")
	var contentLines []string
	for stdinScanner.Scan() {
		line := stdinScanner.Text()
		if line == "---" {
			break
		}
		contentLines = append(contentLines, line)
	}
	content := strings.TrimSpace(strings.Join(contentLines, "\n"))
	if content == "" {
		return fmt.Errorf("content cannot be empty")
	}

	newKnowledgeDir := filepath.Join(somethingDir, "new-knowledge")
	if err := os.MkdirAll(newKnowledgeDir, 0755); err != nil {
		return fmt.Errorf("failed to create new-knowledge directory: %w", err)
	}

	fileName := knowledge.TriggerToFileName(trigger) + ".md"
	filePath := filepath.Join(newKnowledgeDir, fileName)
	fileContent := fmt.Sprintf("# Action\ncreate\n\n# Trigger\n%s\n\n# Content\n%s\n", trigger, content)
	if err := os.WriteFile(filePath, []byte(fileContent), 0644); err != nil {
		return fmt.Errorf("failed to write knowledge file: %w", err)
	}

	fmt.Printf("\n✓ Knowledge saved to %s\n", filePath)
	fmt.Println("Run `yansu push` to upload it to the cloud.")
	return nil
}

func runKnowledgeDelete(identifier string) error {
	knowledgeDir, err := getKnowledgeDir()
	if err != nil {
		return err
	}
	items, err := knowledge.LoadAll(knowledgeDir)
	if err != nil {
		return fmt.Errorf("failed to load knowledge: %w", err)
	}
	id := strings.ToLower(identifier)
	for _, k := range items {
		if strings.ToLower(k.UUID) == id ||
			strings.EqualFold(filepath.Base(k.FilePath), identifier) ||
			strings.EqualFold(filepath.Base(k.FilePath), identifier+".md") {
			if err := os.Remove(k.FilePath); err != nil {
				return fmt.Errorf("failed to delete file: %w", err)
			}
			fmt.Printf("✓ Deleted: %s\n", filepath.Base(k.FilePath))
			fmt.Println("Run `yansu push` to sync the deletion to the cloud.")
			return nil
		}
	}
	return fmt.Errorf("knowledge item %q not found", identifier)
}

// ============================================================================
// Stage Commands
// ============================================================================

// newProjectClient loads config + project context and returns a ready API client.
// Prints an error and returns nil on failure.
func newProjectClient() (*client.Client, error) {
	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		return nil, fmt.Errorf("not in a Yansu project: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("not authenticated (run 'yansu login' first)")
	}
	somethingDir := config.GetSomethingDir(projectRoot)
	projectCfg, err := config.LoadProject(somethingDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load project config: %w", err)
	}
	apiClient := client.New(cfg.BaseURL, cfg.Token)
	apiClient.SetProject(projectCfg.Organization, projectCfg.Product, projectCfg.Name)
	apiClient.SetProductUUID(projectCfg.ProductUUID)
	return apiClient, nil
}

// newBaseClient loads config and returns an API client without project context.
// Used for flat-UUID endpoints (feedback reply/retry/resolve).
func newBaseClient() (*client.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("not authenticated (run 'yansu login' first)")
	}
	return client.New(cfg.BaseURL, cfg.Token), nil
}

func stageStatusEmoji(status string) string {
	switch status {
	case "completed":
		return "✅"
	case "running":
		return "🔄"
	case "failed":
		return "❌"
	default:
		return "⏳"
	}
}

var stageCmd = &cobra.Command{
	Use:   "stage",
	Short: "Manage project stages",
}

var stageListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all stages with their status",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		stages, err := apiClient.ListStages(context.Background())
		if err != nil {
			return fmt.Errorf("failed to list stages: %w", err)
		}
		if len(stages) == 0 {
			fmt.Println("No stages found.")
			return nil
		}
		fmt.Printf("Stages (%d):\n", len(stages))
		for _, s := range stages {
			fmt.Printf("  %s %-30s %s\n", stageStatusEmoji(s.Status), s.Name, s.DisplayName)
		}
		return nil
	},
}

var stageShowCmd = &cobra.Command{
	Use:   "show <stage>",
	Short: "View stage details and content",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		stage, err := apiClient.GetStage(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to get stage: %w", err)
		}
		fmt.Printf("Stage: %s (%s)\n", stage.DisplayName, stage.Name)
		fmt.Printf("  Type:   %s\n", stage.Type)
		fmt.Printf("  Status: %s %s\n", stageStatusEmoji(stage.Status), stage.Status)
		if stage.Content != "" {
			fmt.Printf("\n%s\n", stage.Content)
		}
		return nil
	},
}

var stageTriggerCmd = &cobra.Command{
	Use:   "trigger <stage>",
	Short: "Run/trigger a stage",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		stageName := args[0]
		fmt.Printf("Triggering stage %q...\n", stageName)
		_, err = apiClient.RunStage(context.Background(), stageName)
		if err != nil {
			return fmt.Errorf("failed to trigger stage: %w", err)
		}
		fmt.Printf("✅ Stage %q triggered successfully.\n", stageName)
		return nil
	},
}

var stageTasksCmd = &cobra.Command{
	Use:   "tasks <stage>",
	Short: "View execution task summary for a stage",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		summary, err := apiClient.GetStageTasksSummary(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to get tasks: %w", err)
		}
		fmt.Printf("Tasks for %q (total: %d):\n", args[0], summary.Total)
		fmt.Printf("  🔄 Running:   %d\n", summary.Running)
		fmt.Printf("  ⏳ Pending:   %d\n", summary.Pending)
		fmt.Printf("  ✅ Completed: %d\n", summary.Completed)
		fmt.Printf("  ❌ Failed:    %d\n", summary.Failed)
		if summary.Cancelled > 0 {
			fmt.Printf("  🚫 Cancelled: %d\n", summary.Cancelled)
		}
		return nil
	},
}

var stageVersionsCmd = &cobra.Command{
	Use:   "versions <stage>",
	Short: "Show version history for a stage",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		versions, total, err := apiClient.ListStageVersions(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to list versions: %w", err)
		}
		if total == 0 {
			fmt.Printf("No versions for stage %q.\n", args[0])
			return nil
		}
		fmt.Printf("Versions for %q (%d):\n", args[0], total)
		for _, v := range versions {
			fmt.Printf("  v%-4d  %-10s  %s\n", v.VersionNumber, v.Source, v.CreatedAt.Format("2006-01-02 15:04"))
		}
		return nil
	},
}

var stageRestoreCmd = &cobra.Command{
	Use:   "restore <stage> <version>",
	Short: "Restore a stage to a previous version",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		stageName := args[0]
		version, err := strconv.Atoi(args[1])
		if err != nil || version < 1 {
			return fmt.Errorf("version must be a positive integer")
		}
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		fmt.Printf("Restoring stage %q to version %d...\n", stageName, version)
		if err := apiClient.RestoreStageVersion(context.Background(), stageName, version); err != nil {
			return fmt.Errorf("failed to restore: %w", err)
		}
		fmt.Printf("✅ Stage %q restored to version %d.\n", stageName, version)
		return nil
	},
}

// ============================================================================
// Scenario Commands
// ============================================================================

func scenarioStatusEmoji(status string) string {
	switch status {
	case "passed":
		return "✅"
	case "building":
		return "🔄"
	case "failed":
		return "❌"
	case "cancelled":
		return "🚫"
	case "queued":
		return "⏳"
	default:
		return "📝"
	}
}

var scenarioCmd = &cobra.Command{
	Use:   "scenario",
	Short: "Manage project scenarios",
}

var scenarioListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all scenarios with their status",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		scenarios, err := apiClient.ListScenarios(context.Background())
		if err != nil {
			return fmt.Errorf("failed to list scenarios: %w", err)
		}
		if len(scenarios) == 0 {
			fmt.Println("No scenarios found.")
			return nil
		}
		fmt.Printf("Scenarios (%d):\n", len(scenarios))
		for _, s := range scenarios {
			seq := fmt.Sprintf("#%-3d", s.ScenarioSequence)
			name := s.Name
			if len(name) > 50 {
				name = name[:50] + "…"
			}
			fmt.Printf("  %s %s %-52s %s\n", scenarioStatusEmoji(s.Status), seq, name, s.UUID[:8])
		}
		return nil
	},
}

var scenarioShowCmd = &cobra.Command{
	Use:   "show <uuid>",
	Short: "Show full details of a scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		s, err := apiClient.GetScenario(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to get scenario: %w", err)
		}
		fmt.Printf("%s Scenario #%d: %s\n", scenarioStatusEmoji(s.Status), s.ScenarioSequence, s.Name)
		fmt.Printf("  UUID:   %s\n", s.UUID)
		fmt.Printf("  Status: %s\n", s.Status)
		if s.Description != "" {
			fmt.Printf("\n%s\n", s.Description)
		}
		if s.Error != nil {
			fmt.Printf("\nError: %s\n", *s.Error)
		}
		if s.Metadata != nil && s.Metadata.Checklist != nil {
			cl := s.Metadata.Checklist
			fmt.Printf("\nChecklist: %s (%d items)\n", cl.Title, len(cl.Items))
			for _, item := range cl.Items {
				mark := "☐"
				if item.Checked {
					mark = "☑"
				}
				fmt.Printf("  %s [%s] %s\n", mark, item.ID, item.Title)
			}
		}
		return nil
	},
}

var scenarioRequeueCmd = &cobra.Command{
	Use:   "requeue <uuid>",
	Short: "Re-queue a failed or passed scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		s, err := apiClient.RequeueScenario(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to requeue scenario: %w", err)
		}
		fmt.Printf("✅ Scenario %s re-queued (status: %s).\n", s.UUID[:8], s.Status)
		return nil
	},
}

var scenarioCancelCmd = &cobra.Command{
	Use:   "cancel <uuid>",
	Short: "Cancel a queued or building scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		s, err := apiClient.CancelScenario(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to cancel scenario: %w", err)
		}
		fmt.Printf("🚫 Scenario %s cancelled.\n", s.UUID[:8])
		return nil
	},
}

var scenarioChecklistCmd = &cobra.Command{
	Use:   "checklist",
	Short: "Manage scenario acceptance checklist",
}

var scenarioChecklistGenerateCmd = &cobra.Command{
	Use:   "generate <uuid>",
	Short: "Generate acceptance checklist for a passed scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		result, err := apiClient.GenerateScenarioChecklist(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to generate checklist: %w", err)
		}
		fmt.Printf("🔄 Checklist generation started (task: %s).\n", result.ExecutorTaskUUID[:8])
		fmt.Printf("   Run `yansu scenario show %s` to see the checklist when ready.\n", args[0])
		return nil
	},
}

var scenarioChecklistCheckCmd = &cobra.Command{
	Use:   "check <uuid> <item-id>",
	Short: "Mark a checklist item as checked",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		if _, err := apiClient.UpdateScenarioChecklistItem(context.Background(), args[0], args[1], true); err != nil {
			return fmt.Errorf("failed to update checklist item: %w", err)
		}
		fmt.Printf("☑ Item %q marked as checked.\n", args[1])
		return nil
	},
}

var scenarioChecklistUncheckCmd = &cobra.Command{
	Use:   "uncheck <uuid> <item-id>",
	Short: "Mark a checklist item as unchecked",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		if _, err := apiClient.UpdateScenarioChecklistItem(context.Background(), args[0], args[1], false); err != nil {
			return fmt.Errorf("failed to update checklist item: %w", err)
		}
		fmt.Printf("☐ Item %q marked as unchecked.\n", args[1])
		return nil
	},
}

var stageEditNoteFlag string

var stageEditCmd = &cobra.Command{
	Use:   "edit <stage>",
	Short: "Update stage content (reads from stdin)",
	Long: `Update the content of a stage by piping new content via stdin.

Examples:
  # Pipe new content directly
  echo "# Updated PRD\n..." | yansu stage edit prd

  # With a change description
  cat new_prd.md | yansu stage edit prd --note "add SQLite decision"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		stageName := args[0]

		content, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read stdin: %w", err)
		}
		if len(strings.TrimSpace(string(content))) == 0 {
			return fmt.Errorf("content is empty: pipe new content via stdin")
		}

		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}

		note := stageEditNoteFlag
		if note == "" {
			note = "Updated via yansu CLI"
		}

		_, err = apiClient.UpdateStage(context.Background(), stageName, client.UpdateStageRequest{
			Content:           string(content),
			ChangeDescription: note,
		})
		if err != nil {
			return fmt.Errorf("failed to update stage: %w", err)
		}
		fmt.Printf("✅ Stage %q updated.\n", stageName)
		return nil
	},
}

func init() {
	stageEditCmd.Flags().StringVar(&stageEditNoteFlag, "note", "", "description of the change")
}

// ============================================================================
// Feedback Commands
// ============================================================================

var feedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "Manage stage feedbacks",
}

var feedbackListCmd = &cobra.Command{
	Use:   "list <stage>",
	Short: "List feedbacks for a stage",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		items, total, err := apiClient.ListFeedbacksByStage(context.Background(), args[0])
		if err != nil {
			return fmt.Errorf("failed to list feedbacks: %w", err)
		}
		if total == 0 {
			fmt.Printf("No feedbacks for stage %q.\n", args[0])
			return nil
		}
		fmt.Printf("Feedbacks for %q (%d):\n", args[0], total)
		for _, fb := range items {
			uuid8 := shortUUID(fb.UUID)
			content := fb.Content
			if len(content) > 70 {
				content = content[:70] + "…"
			}
			fmt.Printf("  %s  %s  [%s]  %s\n", feedbackStatusSymbol(fb.Status), uuid8, fb.Type, content)
		}
		return nil
	},
}

func feedbackStatusSymbol(status string) string {
	switch status {
	case "resolved":
		return "✅"
	case "processing":
		return "🔄"
	case "ignored":
		return "🚫"
	default:
		return "💬"
	}
}

func shortUUID(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}

var feedbackPendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List feedbacks awaiting your reply",
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newProjectClient()
		if err != nil {
			return err
		}
		items, total, err := apiClient.ListPendingReplyFeedbacks(context.Background())
		if err != nil {
			return fmt.Errorf("failed to list pending feedbacks: %w", err)
		}
		if total == 0 {
			fmt.Println("No feedbacks awaiting your reply. 🎉")
			return nil
		}
		fmt.Printf("Feedbacks awaiting your reply (%d):\n", total)
		for _, fb := range items {
			uuid8 := shortUUID(fb.UUID)
			content := fb.Content
			if len(content) > 60 {
				content = content[:60] + "…"
			}
			line := fmt.Sprintf("  💬  %s  [%s]  %s", uuid8, fb.StageDisplayName, content)
			if fb.CommentCount > 0 {
				line += fmt.Sprintf(" (%d comments)", fb.CommentCount)
			}
			fmt.Println(line)
		}
		fmt.Printf("\nReply with: yansu feedback reply <uuid> <your reply>\n")
		return nil
	},
}

var feedbackReplyCmd = &cobra.Command{
	Use:   "reply <uuid> <text...>",
	Short: "Reply to a feedback",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		uuid := args[0]
		text := strings.Join(args[1:], " ")
		apiClient, err := newBaseClient()
		if err != nil {
			return err
		}
		_, err = apiClient.CreateFeedbackComment(context.Background(), uuid, client.CreateFeedbackCommentRequest{
			Content: text,
		})
		if err != nil {
			return fmt.Errorf("failed to reply: %w", err)
		}
		fmt.Printf("✅ Reply sent to feedback %s.\n", shortUUID(uuid))
		return nil
	},
}

var feedbackRetryCmd = &cobra.Command{
	Use:   "retry <uuid>",
	Short: "Retry AI response for a feedback",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newBaseClient()
		if err != nil {
			return err
		}
		if err := apiClient.RetryFeedback(context.Background(), args[0]); err != nil {
			return fmt.Errorf("failed to retry: %w", err)
		}
		fmt.Printf("✅ Retry triggered for feedback %s.\n", shortUUID(args[0]))
		return nil
	},
}

var feedbackShowCmd = &cobra.Command{
	Use:   "show <uuid>",
	Short: "Show full details of a feedback",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		uuid := args[0]
		apiClient, err := newBaseClient()
		if err != nil {
			return err
		}
		fb, err := apiClient.GetFeedback(context.Background(), uuid)
		if err != nil {
			return fmt.Errorf("failed to get feedback: %w", err)
		}
		fmt.Printf("%s  UUID: %s\n", feedbackStatusSymbol(fb.Status), fb.UUID)
		fmt.Printf("  Type:   %s\n", fb.Type)
		fmt.Printf("  Status: %s\n", fb.Status)
		fmt.Printf("  Stage:  %s\n", fb.StageVersionUUID)
		fmt.Printf("  Created: %s\n", fb.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("\nContent:\n%s\n", fb.Content)

		comments, total, err := apiClient.ListFeedbackComments(context.Background(), uuid)
		if err != nil {
			fmt.Printf("\n⚠️  Could not load comments: %v\n", err)
			return nil
		}
		if total == 0 {
			fmt.Printf("\nNo comments yet.\n")
			return nil
		}
		fmt.Printf("\nComments (%d):\n", total)
		for _, c := range comments {
			userName := c.User.DisplayName
			if userName == "" {
				userName = c.User.Name
			}
			if userName == "" {
				userName = c.UserUUID
			}
			fmt.Printf("  [%s] %s: %s\n", c.CreatedAt.Format("2006-01-02 15:04"), userName, c.Content)
		}
		return nil
	},
}

var feedbackResolveCmd = &cobra.Command{
	Use:   "resolve <uuid>",
	Short: "Mark a feedback as resolved",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiClient, err := newBaseClient()
		if err != nil {
			return err
		}
		if err := apiClient.UpdateFeedback(context.Background(), args[0], "resolved"); err != nil {
			return fmt.Errorf("failed to resolve: %w", err)
		}
		fmt.Printf("✅ Feedback %s marked as resolved.\n", shortUUID(args[0]))
		return nil
	},
}

// ============================================================================
// Cron Commands (desktop automation scheduler)
// ============================================================================

var cronCmd = &cobra.Command{
	Use:   "cron",
	Short: "Manage cron jobs in the Isoform desktop app",
	Long: `Manage scheduled automation jobs running in the Isoform desktop app.

The cron API is available when the desktop app is running. The port always
comes from the desktop's cron-port file; YANSU_CRON_PORT is ignored:
  ~/.yansu-agent/cron-port
  YANSU_CRON_SECRET / macOS Keychain (cron-secret file on other platforms)

Desktop agent runs automatically use an expiring YANSU_RUN_TOKEN and its
paired YANSU_RUN_PORT, limited to the run's session, workspace and authority.`,
}

var cronListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all cron jobs",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/cron/jobs", nil, cmd)
	},
}

var cronShowCmd = &cobra.Command{
	Use:   "show <job-id>",
	Short: "Show details of a cron job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/cron/jobs/"+args[0], nil, cmd)
	},
}

var (
	cronAddName           string
	cronAddSchedule       string
	cronAddPrompt         string
	cronAddProject        string
	cronAddModel          string
	cronAddSourceSession  string
	cronAddSourcePlatform string
	cronAddNotifyPlatform string
)

var cronAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Create a new cron job",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Build targetConfig from CLI flags.
		targetCfg := map[string]any{
			"prompt":      cronAddPrompt,
			"project_dir": cronAddProject,
		}
		if cronAddModel != "" {
			targetCfg["model"] = cronAddModel
		}
		targetCfgJSON, _ := json.Marshal(targetCfg)

		body := map[string]any{
			"name":         cronAddName,
			"targetKind":   "agent",
			"targetConfig": json.RawMessage(targetCfgJSON),
		}
		if cronAddSourceSession != "" {
			body["sourceSessionId"] = cronAddSourceSession
		}
		if cronAddSourcePlatform != "" {
			body["sourcePlatform"] = cronAddSourcePlatform
		}
		if cronAddNotifyPlatform != "" {
			if cronAddNotifyPlatform == "auto" {
				body["notifyPlatform"] = ""
			} else {
				body["notifyPlatform"] = cronAddNotifyPlatform
			}
		}

		// Parse --schedule into scheduleKind + scheduleConfig.
		// Supported formats:
		//   "every 5m"          → scheduleKind=every, every_ms=300000
		//   "cron 0 9 * * *"    → scheduleKind=cron, expr="0 9 * * *"
		//   "5m" / "1h"         → scheduleKind=every (shorthand)
		//   "*/5 * * * *"       → scheduleKind=cron (auto-detect: 5+ space-separated fields)
		schedule := cronAddSchedule
		kind, cfg, err := parseCLISchedule(schedule)
		if err != nil {
			return err
		}
		body["scheduleKind"] = kind
		body["scheduleConfig"] = cfg

		return cronRequest("POST", "/cron/jobs", body, cmd)
	},
}

var (
	cronUpdateName           string
	cronUpdateSchedule       string
	cronUpdatePrompt         string
	cronUpdateEnabled        string
	cronUpdateNotifyPlatform string
)

var cronUpdateCmd = &cobra.Command{
	Use:   "update <job-id>",
	Short: "Update a cron job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]any{}
		if cronUpdateName != "" {
			body["name"] = cronUpdateName
		}
		if cronUpdateSchedule != "" {
			kind, cfg, err := parseCLISchedule(cronUpdateSchedule)
			if err != nil {
				return err
			}
			body["scheduleKind"] = kind
			body["scheduleConfig"] = cfg
		}
		if cronUpdatePrompt != "" {
			// Fetch the current job to merge prompt into existing targetConfig,
			// so we don't lose project_dir, model, session_tag, etc.
			resp, err := doCronRequest("GET", "/cron/jobs/"+args[0], nil, "")
			if err != nil {
				return fmt.Errorf("failed to fetch current job for merge: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("failed to fetch current job (status %d): cannot safely update prompt without existing config", resp.StatusCode)
			}
			var currentJob map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&currentJob); err != nil {
				return fmt.Errorf("failed to decode current job: %w", err)
			}
			// Merge prompt into existing targetConfig.
			tcMap := map[string]any{"prompt": cronUpdatePrompt}
			if tc, ok := currentJob["targetConfig"]; ok {
				if existing, ok := tc.(map[string]any); ok {
					for k, v := range existing {
						if k != "prompt" {
							tcMap[k] = v
						}
					}
				}
			}
			cfgJSON, _ := json.Marshal(tcMap)
			body["targetConfig"] = json.RawMessage(cfgJSON)
		}
		if cronUpdateEnabled != "" {
			switch cronUpdateEnabled {
			case "true", "1":
				body["enabled"] = true
			case "false", "0":
				body["enabled"] = false
			default:
				return fmt.Errorf("invalid --enabled value %q (use true/false or 1/0)", cronUpdateEnabled)
			}
		}
		if cronUpdateNotifyPlatform != "" {
			if cronUpdateNotifyPlatform == "auto" {
				body["notifyPlatform"] = ""
			} else {
				body["notifyPlatform"] = cronUpdateNotifyPlatform
			}
		}
		if len(body) == 0 {
			return fmt.Errorf("no fields to update (use --name, --schedule, --prompt, --enabled, or --notify-platform)")
		}
		return cronRequest("PUT", "/cron/jobs/"+args[0], body, cmd)
	},
}

var cronDeleteCmd = &cobra.Command{
	Use:   "delete <job-id>",
	Short: "Delete a cron job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("DELETE", "/cron/jobs/"+args[0], nil, cmd)
	},
}

var cronRunCmd = &cobra.Command{
	Use:   "run <job-id>",
	Short: "Trigger immediate execution of a cron job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("POST", "/cron/jobs/"+args[0]+"/run", nil, cmd)
	},
}

// ============================================================================
// Skill commands (desktop Skill Hub)
// ============================================================================

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Manage agent skills in the Yansu desktop app",
	Long: `Manage agent skills through the Yansu desktop app's Skill Hub.

Search requires an explicit source so chat agents do not silently choose a
registry for the user. Run "yansu skill sources" to see available sources.`,
}

var skillSourcesCmd = &cobra.Command{
	Use:   "sources",
	Short: "List available skill sources",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/skills/sources", nil, cmd)
	},
}

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed skills",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/skills", nil, cmd)
	},
}

var skillDiscoverQuery string

var skillDiscoverCmd = &cobra.Command{
	Use:   "discover [query]",
	Short: "Discover skills installed by other local agent apps",
	Long: `Discover candidate skills from common local agent app directories.

This only lists candidates. It does not install or enable them for Yansu.
Use "yansu skill install --source agent-dirs <id> --yes" after user confirmation.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := strings.TrimSpace(skillDiscoverQuery)
		if len(args) == 1 {
			query = strings.TrimSpace(args[0])
		}
		path := "/skills/search?source=agent-dirs"
		if query != "" {
			path += "&q=" + url.QueryEscape(query)
		}
		return cronRequest("GET", path, nil, cmd)
	},
}

var skillSearchSource string

var skillSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search skills from an explicit source",
	Long: `Search skills from an explicit source.

Examples:
  yansu skill search --source clawhub "calendar"
  yansu skill search --source installed "calendar"
  yansu skill search --source agent-dirs "calendar"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		source := strings.TrimSpace(skillSearchSource)
		if source == "" {
			return fmt.Errorf("--source is required. Run 'yansu skill sources' to see available sources")
		}
		path := "/skills/search?source=" + url.QueryEscape(source) + "&q=" + url.QueryEscape(args[0])
		return cronRequest("GET", path, nil, cmd)
	},
}

var (
	skillInstallSource string
	skillInstallYes    bool
)

var skillInstallCmd = &cobra.Command{
	Use:   "install <ref>",
	Short: "Install a skill from ClawHub, GitHub, or a local folder",
	Long: `Install a skill from a known source.

Examples:
  yansu skill install --source clawhub calendar
  yansu skill install --source agent-dirs claude:calendar:abc12345 --yes
  yansu skill install --source github owner/repo#skill-dir --yes
  yansu skill install --source local /path/to/skill --yes

If --source is omitted, Yansu can infer it from clawhub.ai URLs, github.com URLs,
owner/repo GitHub shorthands, or existing local directories.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ref := strings.TrimSpace(args[0])
		source := strings.TrimSpace(skillInstallSource)
		if source == "" {
			source = inferSkillInstallSource(ref)
		}
		if source == "" {
			return fmt.Errorf("--source is required unless ref is a clawhub.ai URL, github.com URL, owner/repo shorthand, or local directory")
		}
		if (source == "github" || source == "local" || source == "agent-dirs") && !skillInstallYes {
			return fmt.Errorf("%s skills are not moderated; pass --yes only after the user confirms this source", source)
		}
		body := map[string]any{
			"source": source,
			"ref":    ref,
		}
		if skillInstallYes {
			body["confirm_unmoderated"] = true
		}
		return cronRequest("POST", "/skills/install", body, cmd)
	},
}

var skillEnableCmd = &cobra.Command{
	Use:   "enable <skill-id>",
	Short: "Enable an installed skill",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("PATCH", "/skills/by-id/"+url.PathEscape(args[0]), map[string]any{"enabled": true}, cmd)
	},
}

var skillDisableCmd = &cobra.Command{
	Use:   "disable <skill-id>",
	Short: "Disable an installed skill",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("PATCH", "/skills/by-id/"+url.PathEscape(args[0]), map[string]any{"enabled": false}, cmd)
	},
}

var (
	skillDomainsChat       string
	skillDomainsProject    string
	skillDomainsAutomation string
)

var skillDomainsCmd = &cobra.Command{
	Use:   "domains <skill-id>",
	Short: "Update where an installed skill is enabled",
	Long: `Update where an installed skill is enabled. Only provided flags are changed.

Examples:
  yansu skill domains calendar --chat true --project false
  yansu skill domains calendar --automation true`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		domains := map[string]bool{}
		if err := addSkillDomainFlag(domains, "chat", skillDomainsChat); err != nil {
			return err
		}
		if err := addSkillDomainFlag(domains, "project", skillDomainsProject); err != nil {
			return err
		}
		if err := addSkillDomainFlag(domains, "automation", skillDomainsAutomation); err != nil {
			return err
		}
		if len(domains) == 0 {
			return fmt.Errorf("no domains to update (use --chat, --project, or --automation with true/false)")
		}
		return cronRequest("PATCH", "/skills/by-id/"+url.PathEscape(args[0]), map[string]any{"domains": domains}, cmd)
	},
}

var skillUninstallCmd = &cobra.Command{
	Use:   "uninstall <skill-id>",
	Short: "Uninstall a skill",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("DELETE", "/skills/by-id/"+url.PathEscape(args[0]), nil, cmd)
	},
}

func inferSkillInstallSource(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if parsed, err := url.Parse(ref); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		switch strings.ToLower(parsed.Hostname()) {
		case "clawhub.ai", "www.clawhub.ai":
			return "clawhub"
		case "github.com", "www.github.com":
			return "github"
		}
	}
	if info, err := os.Stat(ref); err == nil && info.IsDir() {
		return "local"
	}
	if strings.Count(ref, ":") == 2 && !strings.Contains(ref, "://") {
		return "agent-dirs"
	}
	// Refuse the owner/repo github fallback when ref looks like a filesystem
	// path (absolute, "./foo", "../foo", or "~/foo"). os.Stat above already
	// caught existing directories, so reaching here with a path-shaped ref
	// means the user typed a non-existent local path — surface that as
	// "unknown source" rather than firing off a remote github install.
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../") || strings.HasPrefix(ref, "~/") || ref == "." || ref == ".." || ref == "~" {
		return ""
	}
	parts := strings.Split(strings.Trim(ref, "/"), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(ref, "://") {
		return "github"
	}
	return ""
}

func addSkillDomainFlag(domains map[string]bool, name, value string) error {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return nil
	}
	switch value {
	case "true", "1", "yes", "on":
		domains[name] = true
	case "false", "0", "no", "off":
		domains[name] = false
	default:
		return fmt.Errorf("invalid --%s value %q (use true or false)", name, value)
	}
	return nil
}

// ============================================================================
// MCP commands (desktop MCP Server Hub)
// ============================================================================

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP servers in the Isoform desktop app",
	Long: `Manage MCP (Model Context Protocol) servers through the Isoform desktop app.

Use "yansu mcp discover" to find servers already configured in Claude Desktop,
Cursor, Codex, Windsurf, Claude Code CLI, or VS Code, then "yansu mcp install
--source local <source>:<name>" to import them. Use "yansu mcp install
--source manual --transport sse|http ..." to register a remote server. Add
manual stdio servers in Isoform Settings.`,
}

var mcpListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed MCP servers (with health and tool counts)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/mcp", nil, cmd)
	},
}

var mcpSourcesCmd = &cobra.Command{
	Use:   "sources",
	Short: "Show which local MCP config files Yansu scans",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/mcp/sources", nil, cmd)
	},
}

var mcpDiscoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Discover MCP servers configured by other tools",
	Long: `Discover MCP server entries from Claude Desktop, Claude Code CLI,
Cursor, Codex, Windsurf, and VS Code. Output is a JSON document with
sources[] (where Yansu looked) and servers[] (what it found).

This only lists candidates. Install with:
    yansu mcp install --source local --ref <source-id>:<name>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/mcp/discover", nil, cmd)
	},
}

var (
	mcpInstallSource    string
	mcpInstallRef       string
	mcpInstallID        string
	mcpInstallName      string
	mcpInstallTransport string
	mcpInstallCommand   string
	mcpInstallArgs      []string
	mcpInstallURL       string
	mcpInstallEnv       []string
	mcpInstallWorkDir   string
	mcpInstallNoStart   bool
)

var mcpInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install an MCP server (manual definition or imported from another tool)",
	Long: `Install an MCP server.

Local import (one-shot copy from another MCP-aware tool):
    yansu mcp install --source local --ref claude-desktop:filesystem
    yansu mcp install --source local --ref cursor:github --no-auto-start

Manual stdio servers must be added in Isoform Settings.

Manual SSE/HTTP install:
    yansu mcp install --source manual --id remote --transport sse \
        --url https://example.com/sse`,
	RunE: func(cmd *cobra.Command, args []string) error {
		source := strings.ToLower(strings.TrimSpace(mcpInstallSource))
		body := map[string]any{
			"auto_start": !mcpInstallNoStart,
		}
		switch source {
		case "", "manual":
			body["source"] = "manual"
			id := strings.TrimSpace(mcpInstallID)
			if id == "" {
				return fmt.Errorf("--id is required for manual installs")
			}
			body["id"] = id
			if name := strings.TrimSpace(mcpInstallName); name != "" {
				body["name"] = name
			}
			transport := strings.ToLower(strings.TrimSpace(mcpInstallTransport))
			if transport == "" {
				transport = "stdio"
			}
			body["transport"] = transport
			switch transport {
			case "stdio":
				cmdName := strings.TrimSpace(mcpInstallCommand)
				if cmdName == "" {
					return fmt.Errorf("--command is required for stdio servers")
				}
				body["command"] = cmdName
				if len(mcpInstallArgs) > 0 {
					body["args"] = expandCommaSeparated(mcpInstallArgs)
				}
				if strings.TrimSpace(mcpInstallWorkDir) != "" {
					body["work_dir"] = strings.TrimSpace(mcpInstallWorkDir)
				}
			case "sse", "http":
				rawURL := strings.TrimSpace(mcpInstallURL)
				if rawURL == "" {
					return fmt.Errorf("--url is required for sse/http servers")
				}
				body["url"] = rawURL
			default:
				return fmt.Errorf("unsupported --transport %q (expected stdio, sse, or http)", transport)
			}
			if env, err := parseMcpEnvFlags(mcpInstallEnv); err != nil {
				return err
			} else if len(env) > 0 {
				body["env"] = env
			}
		case "local":
			ref := strings.TrimSpace(mcpInstallRef)
			if ref == "" {
				return fmt.Errorf("--ref is required for local installs (format: <source-id>:<name>)")
			}
			parts := strings.SplitN(ref, ":", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return fmt.Errorf("invalid --ref %q (expected <source-id>:<name>, e.g. cursor:filesystem)", ref)
			}
			body["source"] = "local"
			body["source_id"] = parts[0]
			body["discovered_name"] = parts[1]
		case "registry":
			slug := strings.TrimSpace(mcpInstallRef)
			if slug == "" {
				return fmt.Errorf("--ref <registry-slug> is required for registry installs (run 'yansu mcp registry list' to see slugs)")
			}
			body["source"] = "registry"
			body["registry_slug"] = slug
		default:
			return fmt.Errorf("unsupported --source %q (expected manual, local, or registry)", source)
		}
		return cronRequest("POST", "/mcp/install", body, cmd)
	},
}

var mcpEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Enable an installed MCP server (spawns the subprocess)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("POST", "/mcp/"+url.PathEscape(args[0])+"/enable", nil, cmd)
	},
}

var mcpDisableCmd = &cobra.Command{
	Use:   "disable <id>",
	Short: "Disable an installed MCP server (stops the subprocess, removes its tools)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("POST", "/mcp/"+url.PathEscape(args[0])+"/disable", nil, cmd)
	},
}

var mcpRestartCmd = &cobra.Command{
	Use:   "restart <id>",
	Short: "Restart an MCP server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("POST", "/mcp/"+url.PathEscape(args[0])+"/restart", nil, cmd)
	},
}

var mcpStopCmd = &cobra.Command{
	Use:   "stop <id>",
	Short: "Stop an MCP server without disabling its persisted config",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("POST", "/mcp/"+url.PathEscape(args[0])+"/stop", nil, cmd)
	},
}

var mcpToolsCmd = &cobra.Command{
	Use:   "tools <id>",
	Short: "List the tools currently exposed by an MCP server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/mcp/"+url.PathEscape(args[0])+"/tools", nil, cmd)
	},
}

var mcpTailLines int

var mcpTailCmd = &cobra.Command{
	Use:   "tail <id>",
	Short: "Tail the most recent stderr lines from an MCP server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "/mcp/" + url.PathEscape(args[0]) + "/logs"
		if mcpTailLines > 0 {
			path += fmt.Sprintf("?n=%d", mcpTailLines)
		}
		return cronRequest("GET", path, nil, cmd)
	},
}

var mcpUninstallCmd = &cobra.Command{
	Use:   "uninstall <id>",
	Short: "Uninstall an MCP server (stops it and removes its config + scratch dir)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("DELETE", "/mcp/"+url.PathEscape(args[0]), nil, cmd)
	},
}

var mcpRegistryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Browse the curated catalog of MCP servers (modelcontextprotocol/servers README)",
}

var mcpRegistryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List catalog entries (cached for 24h)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/mcp/registry", nil, cmd)
	},
}

var mcpRegistrySearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Filter catalog entries by name or description",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/mcp/registry?q="+url.QueryEscape(args[0]), nil, cmd)
	},
}

var mcpRegistryRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Force a re-fetch of the catalog now",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("POST", "/mcp/registry", map[string]any{"action": "refresh"}, cmd)
	},
}

// expandCommaSeparated lets the user pass either repeated --args flags or a
// single comma-separated value, whichever is more ergonomic for their shell.
// We split each input on commas so `--args "-y,@x/y,/tmp"` and
// `--args -y --args @x/y --args /tmp` both yield the same arg vector.
func expandCommaSeparated(in []string) []string {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		for _, piece := range strings.Split(raw, ",") {
			piece = strings.TrimSpace(piece)
			if piece != "" {
				out = append(out, piece)
			}
		}
	}
	return out
}

// parseMcpEnvFlags accepts repeated --env KEY=VALUE flags (possibly with
// commas separating multiple pairs in one flag) and returns the merged
// map. Empty keys are rejected so a malformed flag doesn't silently get
// accepted as a no-op.
func parseMcpEnvFlags(in []string) (map[string]string, error) {
	out := map[string]string{}
	for _, raw := range in {
		for _, pair := range strings.Split(raw, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			eq := strings.Index(pair, "=")
			if eq <= 0 {
				return nil, fmt.Errorf("invalid --env value %q (expected KEY=VALUE)", pair)
			}
			out[strings.TrimSpace(pair[:eq])] = pair[eq+1:]
		}
	}
	return out, nil
}

// cronAPIRequestTimeout must exceed the server-side worst case: semantic dedup
// (30s) + DB write (10s) + overhead. Previous 15s value caused false timeouts
// when the dedup AI call was slow.
const cronAPIRequestTimeout = 60 * time.Second

// parseCLISchedule parses a --schedule flag value into scheduleKind and
// scheduleConfig JSON. Supported formats:
//
//	"every 5m"       → ("every", {"every_ms":300000})
//	"cron 0 9 * * *" → ("cron",  {"expr":"0 9 * * *"})
//	"5m"             → ("every", {"every_ms":300000})      (shorthand)
//	"*/5 * * * *"    → ("cron",  {"expr":"*/5 * * * *"})   (auto-detect)
func parseCLISchedule(schedule string) (string, json.RawMessage, error) {
	schedule = strings.TrimSpace(schedule)
	if schedule == "" {
		return "", nil, fmt.Errorf("--schedule is required")
	}

	// Prefix-based parsing: "every <duration>" or "cron <expr>".
	if strings.HasPrefix(schedule, "every ") {
		dur := strings.TrimSpace(strings.TrimPrefix(schedule, "every "))
		d, err := time.ParseDuration(dur)
		if err != nil {
			return "", nil, fmt.Errorf("invalid duration in 'every %s': %w", dur, err)
		}
		cfg, _ := json.Marshal(map[string]any{"every_ms": d.Milliseconds()})
		return "every", cfg, nil
	}
	if strings.HasPrefix(schedule, "cron ") {
		expr := strings.TrimSpace(strings.TrimPrefix(schedule, "cron "))
		cfg, _ := json.Marshal(map[string]any{"expr": expr})
		return "cron", cfg, nil
	}

	// Auto-detect: if it contains spaces, treat as raw cron expression.
	if strings.Contains(schedule, " ") {
		cfg, _ := json.Marshal(map[string]any{"expr": schedule})
		return "cron", cfg, nil
	}

	// Otherwise try as Go duration shorthand.
	d, err := time.ParseDuration(schedule)
	if err != nil {
		return "", nil, fmt.Errorf("invalid schedule: %q (use 'every 5m', 'cron 0 9 * * *', or a raw cron expression)", schedule)
	}
	cfg, _ := json.Marshal(map[string]any{"every_ms": d.Milliseconds()})
	return "every", cfg, nil
}

// cronRequest sends an authenticated HTTP request to the cron API.
func cronRequest(method, path string, body map[string]any, cmd *cobra.Command) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		reqBody = strings.NewReader(string(data))
	}

	contentType := ""
	if body != nil {
		contentType = "application/json"
	}

	resp, err := doCronRequest(method, path, reqBody, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if method == http.MethodGet && path == "/mcp" {
		sandboxed, err := cliSandboxed()
		if err != nil {
			return err
		}
		if sandboxed {
			respBody, err = sandboxMCPListResponse(respBody, resp.StatusCode)
			if err != nil {
				return err
			}
		}
	}

	if resp.StatusCode == http.StatusConflict {
		// 409 means the job already exists (duplicate). Treat as idempotent
		// success so retries after a timeout don't confuse the caller.
		fmt.Fprintln(cmd.OutOrStdout(), "Automation already exists: "+strings.TrimSpace(string(respBody)))
		return nil
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("cron API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	// Pretty-print JSON output.
	var pretty json.RawMessage
	if err := json.Unmarshal(respBody, &pretty); err == nil {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		enc.Encode(pretty)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), string(respBody))
	}
	return nil
}

//isoform:destination local
func doCronRequest(method, path string, body io.Reader, contentType string, opts ...cronsecret.Option) (*http.Response, error) {
	port, secret, err := loadCronCredentials(opts...)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	client := &http.Client{
		Timeout: cronAPIRequestTimeout,
		// Even a loopback redirect can forward Authorization to another port.
		// Authenticated API requests must stay at the published endpoint.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("cron API redirects are not allowed")
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cron API unreachable (is the desktop app running?): %w", err)
	}
	return resp, nil
}

// loadCronCredentials reads only the desktop-published port. Environment
// variables cannot redirect the platform credential to another listener.
// The secret may come from env or the platform store (Keychain on macOS).
func loadCronCredentials(opts ...cronsecret.Option) (int, string, error) {
	// A run credential pins its listener in memory. Never fall back to a
	// platform credential if it is missing, expired, or explicitly cleared.
	if token, present := os.LookupEnv(runtoken.TokenEnv); present || os.Getenv(runtoken.ScopedEnv) != "" {
		port, err := strconv.Atoi(os.Getenv(runtoken.PortEnv))
		if err != nil || port < 1 || port > 65535 || strings.TrimSpace(token) == "" {
			return 0, "", errors.New("scoped run credential unavailable")
		}
		return port, token, nil
	}
	// Kernel sandbox status cannot be removed with `env -u`. A prompt-injected
	// shell must not turn the bundled CLI into a master-credential proxy.
	if sandboxed, err := cliSandboxed(); err != nil || sandboxed {
		return 0, "", errors.New("sandboxed CLI requires a scoped run token")
	}
	secret := os.Getenv("YANSU_CRON_SECRET")

	configDir, err := config.ConfigDir()
	if err != nil {
		return 0, "", fmt.Errorf("resolve cron credential directory: %w", err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "cron-port"))
	if err != nil {
		return 0, "", fmt.Errorf("cron API port not found (start the desktop app): %w", err)
	}
	portStr := strings.TrimSpace(string(data))
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return 0, "", fmt.Errorf("invalid published cron API port %q", portStr)
	}
	if secret == "" {
		stored, err := cronsecret.Load(configDir, opts...)
		if err != nil {
			return 0, "", fmt.Errorf("cron API secret unavailable (set YANSU_CRON_SECRET or start the desktop app): %w", err)
		}
		secret = stored
	}

	return port, secret, nil
}

// ============================================================================
// Activity commands (desktop activity monitor)
// ============================================================================

var activityCmd = &cobra.Command{
	Use:   "activity",
	Short: "Query desktop activity data from the Isoform app",
	Long: `Query desktop activity sessions recorded by the Isoform desktop app.

The activity API shares the same connection as cron. The port always comes
from the desktop's cron-port file; YANSU_CRON_PORT is ignored:
  ~/.yansu-agent/cron-port
  YANSU_CRON_SECRET / macOS Keychain (cron-secret file on other platforms)

Desktop agent runs automatically use an expiring YANSU_RUN_TOKEN and its
paired YANSU_RUN_PORT, with the desktop's existing read-time privacy gates.`,
}

var (
	activityListDate    string
	activityListLimit   int
	activityListKeyword string
	activitySummaryDate string
)

var activityListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent activity sessions",
	Long: `List activity sessions with optional filters.

Examples:
  yansu activity list                      # last 20 sessions
  yansu activity list --date 2026-04-07    # all sessions on that date
  yansu activity list --limit 50           # last 50 sessions
  yansu activity list --keyword Chrome     # sessions involving Chrome`,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := []string{}
		if activityListDate != "" {
			params = append(params, "date="+activityListDate)
		}
		if activityListLimit > 0 {
			params = append(params, fmt.Sprintf("limit=%d", activityListLimit))
		}
		if activityListKeyword != "" {
			params = append(params, "keyword="+activityListKeyword)
		}
		path := "/activity/sessions"
		if len(params) > 0 {
			path += "?" + strings.Join(params, "&")
		}
		return cronRequest("GET", path, nil, cmd)
	},
}

var activityShowCmd = &cobra.Command{
	Use:   "show <session-id>",
	Short: "Show detailed events for an activity session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/activity/sessions/"+args[0], nil, cmd)
	},
}

var activitySummaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Show daily activity summary",
	Long: `Show a summary of desktop activity for a given date.

Examples:
  yansu activity summary                   # today's summary
  yansu activity summary --date 2026-04-07 # specific date`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "/activity/summary"
		if activitySummaryDate != "" {
			path += "?date=" + activitySummaryDate
		}
		return pollActivitySummary(path, cmd)
	},
}

// pollActivitySummary fetches /activity/summary and, if the server is still
// generating the AI narrative, polls until it lands or the budget is spent.
// The server returns status="generating" immediately while a background
// goroutine runs the LLM call; we poll instead of holding one long HTTP
// request so a slow generation can't exceed the 60s client timeout.
func pollActivitySummary(path string, cmd *cobra.Command) error {
	const (
		maxAttempts  = 30 // 30 * 3s = 90s total budget
		pollInterval = 3 * time.Second
	)

	var lastBody []byte
	notified := false
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := doCronRequest("GET", path, nil, "")
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("cron API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		lastBody = body
		var parsed struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil || parsed.Status != "generating" {
			break
		}
		if !notified {
			fmt.Fprintln(cmd.ErrOrStderr(), "Generating activity summary…")
			notified = true
		}
		time.Sleep(pollInterval)
	}

	var pretty json.RawMessage
	if err := json.Unmarshal(lastBody, &pretty); err == nil {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(pretty)
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(lastBody))
	return nil
}

var activitySearchLimit int

var activitySearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Semantic search across activity sessions",
	Long: `Search activity sessions using semantic similarity (RAG).
Matches against OCR text extracted from screenshots.

Examples:
  yansu activity search "writing code in VS Code"
  yansu activity search "reading email" --limit 5`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := fmt.Sprintf("/activity/search?q=%s&limit=%d",
			args[0], activitySearchLimit)
		return cronRequest("GET", path, nil, cmd)
	},
}

// ============================================================================
// Memory commands (AI-captured personal memories)
// ============================================================================

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Query AI-captured memories from the Yansu app",
	Long: `Query personal memories captured by the Yansu desktop app.

Memories are short, durable personal notes (preferences, working habits, topics
of focus, AI operational instructions) generated automatically from chats,
conversations, and desktop activity. They live in ~/.yansu-agent/memory/ and
are indexed via FTS5 + vector search for retrieval.

Dimensions:
  user_insight     Concrete working habits, tool choices
  topic_highlight  Recent topics of focus, active investigations
  deep_user_model  Long-term persona, role, identity
  agentic_memory   AI operational preferences / behavior instructions
  custom           User-authored memories`,
}

var (
	memoryListDimension   string
	memoryListLimit       int
	memorySearchLimit     int
	memorySearchDimension string
)

var memoryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List memory entries, newest first",
	Long: `List memory entries, newest first. Returns summaries only (no body).
Use 'yansu memory show <id>' to fetch full content.

Examples:
  yansu memory list                              # all memories, newest first
  yansu memory list --limit 20                   # latest 20
  yansu memory list --dimension user_insight     # only working habits`,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := []string{}
		if memoryListDimension != "" {
			params = append(params, "dimension="+url.QueryEscape(memoryListDimension))
		}
		if memoryListLimit > 0 {
			params = append(params, fmt.Sprintf("limit=%d", memoryListLimit))
		}
		path := "/memory/list"
		if len(params) > 0 {
			path += "?" + strings.Join(params, "&")
		}
		return cronRequest("GET", path, nil, cmd)
	},
}

var memorySearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Hybrid (vector + FTS) search across memories and knowledge",
	Long: `Search memories using hybrid vector + FTS retrieval — the same index
the app uses to auto-inject context into the agent.

Examples:
  yansu memory search "bun vs npm preference"
  yansu memory search "payments team" --limit 5
  yansu memory search "deploy workflow" --dimension agentic_memory`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := []string{"q=" + url.QueryEscape(args[0])}
		if memorySearchLimit > 0 {
			params = append(params, fmt.Sprintf("limit=%d", memorySearchLimit))
		}
		if memorySearchDimension != "" {
			params = append(params, "dimension="+url.QueryEscape(memorySearchDimension))
		}
		return cronRequest("GET", "/memory/search?"+strings.Join(params, "&"), nil, cmd)
	},
}

var memoryShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show full content of a memory entry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cronRequest("GET", "/memory/show/"+url.PathEscape(args[0]), nil, cmd)
	},
}

// ============================================================================
// Init
// ============================================================================

func init() {
	// Auth commands
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(whoamiCmd)

	// Setup token command
	setupTokenCmd.Flags().StringVar(&setupTokenName, "name", "default", "Name for the API token")
	setupTokenCmd.Flags().IntVar(&setupTokenExpiresIn, "expires-in", 365, "Token validity in days")
	rootCmd.AddCommand(setupTokenCmd)

	// Discovery commands
	rootCmd.AddCommand(listCmd)

	// Local project management
	rootCmd.AddCommand(localCmd)
	localCmd.AddCommand(localListCmd)
	localCmd.AddCommand(localRegisterCmd)
	localCmd.AddCommand(localUnregisterCmd)
	localCmd.AddCommand(localPathCmd)

	// Project commands
	rootCmd.AddCommand(cloneCmd)
	rootCmd.AddCommand(projectInitCmd)
	rootCmd.AddCommand(pullCmd)
	rootCmd.AddCommand(syncCmd)
	rootCmd.AddCommand(pushCmd)
	rootCmd.AddCommand(analyzeCmd)

	// Knowledge commands
	rootCmd.AddCommand(knowledgeCmd)
	knowledgeCmd.AddCommand(knowledgeListCmd)
	knowledgeCmd.AddCommand(knowledgeSearchCmd)
	knowledgeCmd.AddCommand(knowledgeShowCmd)
	knowledgeCmd.AddCommand(knowledgeAddCmd)
	knowledgeCmd.AddCommand(knowledgeDeleteCmd)

	// Scenario commands
	rootCmd.AddCommand(scenarioCmd)
	scenarioCmd.AddCommand(scenarioListCmd)
	scenarioCmd.AddCommand(scenarioShowCmd)
	scenarioCmd.AddCommand(scenarioRequeueCmd)
	scenarioCmd.AddCommand(scenarioCancelCmd)
	scenarioCmd.AddCommand(scenarioChecklistCmd)
	scenarioChecklistCmd.AddCommand(scenarioChecklistGenerateCmd)
	scenarioChecklistCmd.AddCommand(scenarioChecklistCheckCmd)
	scenarioChecklistCmd.AddCommand(scenarioChecklistUncheckCmd)

	// Stage commands
	rootCmd.AddCommand(stageCmd)
	stageCmd.AddCommand(stageListCmd)
	stageCmd.AddCommand(stageShowCmd)
	stageCmd.AddCommand(stageEditCmd)
	stageCmd.AddCommand(stageTriggerCmd)
	stageCmd.AddCommand(stageTasksCmd)
	stageCmd.AddCommand(stageVersionsCmd)
	stageCmd.AddCommand(stageRestoreCmd)

	// Feedback commands
	rootCmd.AddCommand(feedbackCmd)
	feedbackCmd.AddCommand(feedbackListCmd)
	feedbackCmd.AddCommand(feedbackShowCmd)
	feedbackCmd.AddCommand(feedbackPendingCmd)
	feedbackCmd.AddCommand(feedbackReplyCmd)
	feedbackCmd.AddCommand(feedbackRetryCmd)
	feedbackCmd.AddCommand(feedbackResolveCmd)

	// Hook commands
	rootCmd.AddCommand(hookCmd)
	hookCmd.AddCommand(hookInstallCmd)
	hookCmd.AddCommand(hookUninstallCmd)
	hookCmd.AddCommand(hookStatusCmd)
	hookCmd.AddCommand(hookRunCmd)

	// Update command
	rootCmd.AddCommand(updateCmd)

	// Daemon commands
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonSetExecutorCmd)
	daemonCmd.AddCommand(daemonRegisterCmd)
	daemonCmd.AddCommand(daemonProcessStatusCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonRestartCmd)
	daemonCmd.AddCommand(daemonLogsCmd)
	daemonLogsCmd.Flags().BoolVarP(&daemonLogsFollowFlag, "follow", "f", false, "follow log output (like tail -f)")
	daemonLogsCmd.Flags().IntVarP(&daemonLogsLinesFlag, "lines", "n", 50, "number of lines to show")

	// Service commands
	rootCmd.AddCommand(serviceCmd)
	serviceCmd.AddCommand(serviceInstallCmd)
	serviceCmd.AddCommand(serviceUninstallCmd)
	serviceCmd.AddCommand(serviceStatusCmd)
	serviceCmd.AddCommand(serviceStopCmd)
	serviceCmd.AddCommand(serviceRestartCmd)
	serviceInstallCmd.Flags().BoolVar(&serviceForce, "force", false, "reinstall even if service is already installed")

	// Cron commands (desktop automation scheduler)
	rootCmd.AddCommand(cronCmd)
	cronCmd.AddCommand(cronListCmd)
	cronCmd.AddCommand(cronShowCmd)
	cronCmd.AddCommand(cronAddCmd)
	cronAddCmd.Flags().StringVar(&cronAddName, "name", "", "Job name (required)")
	cronAddCmd.Flags().StringVar(&cronAddSchedule, "schedule", "", "Schedule expression (required, e.g. 'every 5m', 'cron 0 9 * * *')")
	cronAddCmd.Flags().StringVar(&cronAddPrompt, "prompt", "", "Prompt to execute (required)")
	cronAddCmd.Flags().StringVar(&cronAddProject, "project", "", "Project directory for execution context")
	cronAddCmd.Flags().StringVar(&cronAddModel, "model", "", "Model to use (default: sonnet)")
	cronAddCmd.Flags().StringVar(&cronAddSourceSession, "source-session", "", "Chat session ID that created this job")
	cronAddCmd.Flags().StringVar(&cronAddSourcePlatform, "source-platform", "", "Integration platform that created this job (slack, feishu, discord)")
	cronAddCmd.Flags().StringVar(&cronAddNotifyPlatform, "notify-platform", "", "DM notification platform preference (slack, discord, telegram, feishu, wechat, none, auto to reset)")
	_ = cronAddCmd.MarkFlagRequired("name")
	_ = cronAddCmd.MarkFlagRequired("schedule")
	_ = cronAddCmd.MarkFlagRequired("prompt")
	cronCmd.AddCommand(cronUpdateCmd)
	cronUpdateCmd.Flags().StringVar(&cronUpdateName, "name", "", "New job name")
	cronUpdateCmd.Flags().StringVar(&cronUpdateSchedule, "schedule", "", "New schedule expression")
	cronUpdateCmd.Flags().StringVar(&cronUpdatePrompt, "prompt", "", "New prompt")
	cronUpdateCmd.Flags().StringVar(&cronUpdateEnabled, "enabled", "", "Enable or disable (true/false)")
	cronUpdateCmd.Flags().StringVar(&cronUpdateNotifyPlatform, "notify-platform", "", "DM notification platform preference (slack, discord, telegram, feishu, wechat, none, auto to reset)")
	cronCmd.AddCommand(cronDeleteCmd)
	cronCmd.AddCommand(cronRunCmd)

	// Skill commands (desktop Skill Hub)
	rootCmd.AddCommand(skillCmd)
	skillCmd.AddCommand(skillSourcesCmd)
	skillCmd.AddCommand(skillListCmd)
	skillCmd.AddCommand(skillDiscoverCmd)
	skillDiscoverCmd.Flags().StringVar(&skillDiscoverQuery, "query", "", "Filter discovered agent skills")
	skillCmd.AddCommand(skillSearchCmd)
	skillSearchCmd.Flags().StringVar(&skillSearchSource, "source", "", "Skill source to search (required, e.g. clawhub or installed)")
	skillCmd.AddCommand(skillInstallCmd)
	skillInstallCmd.Flags().StringVar(&skillInstallSource, "source", "", "Skill source to install from (clawhub, github, local, or agent-dirs)")
	skillInstallCmd.Flags().BoolVar(&skillInstallYes, "yes", false, "Confirm installing from an unmoderated source such as GitHub or local")
	skillCmd.AddCommand(skillEnableCmd)
	skillCmd.AddCommand(skillDisableCmd)
	skillCmd.AddCommand(skillDomainsCmd)
	skillDomainsCmd.Flags().StringVar(&skillDomainsChat, "chat", "", "Enable skill for general chat (true/false)")
	skillDomainsCmd.Flags().StringVar(&skillDomainsProject, "project", "", "Enable skill for project chats (true/false)")
	skillDomainsCmd.Flags().StringVar(&skillDomainsAutomation, "automation", "", "Enable skill for automations (true/false)")
	skillCmd.AddCommand(skillUninstallCmd)

	// Activity commands (desktop activity monitor)
	rootCmd.AddCommand(activityCmd)
	activityCmd.AddCommand(activityListCmd)
	activityListCmd.Flags().StringVar(&activityListDate, "date", "", "Filter by date (YYYY-MM-DD)")
	activityListCmd.Flags().IntVar(&activityListLimit, "limit", 20, "Maximum number of sessions to return")
	activityListCmd.Flags().StringVar(&activityListKeyword, "keyword", "", "Filter by keyword (app name, window title, OCR text)")
	activityCmd.AddCommand(activityShowCmd)
	activityCmd.AddCommand(activitySummaryCmd)
	activitySummaryCmd.Flags().StringVar(&activitySummaryDate, "date", "", "Date for summary (YYYY-MM-DD, default: today)")
	activityCmd.AddCommand(activitySearchCmd)
	activitySearchCmd.Flags().IntVar(&activitySearchLimit, "limit", 10, "Maximum number of results")

	// Memory commands (AI-captured personal memories)
	rootCmd.AddCommand(memoryCmd)
	memoryCmd.AddCommand(memoryListCmd)
	memoryListCmd.Flags().StringVar(&memoryListDimension, "dimension", "", "Filter by dimension (user_insight|topic_highlight|deep_user_model|agentic_memory|custom)")
	memoryListCmd.Flags().IntVar(&memoryListLimit, "limit", 0, "Maximum number of entries to return (0 = all)")
	memoryCmd.AddCommand(memorySearchCmd)
	memorySearchCmd.Flags().IntVar(&memorySearchLimit, "limit", 10, "Maximum number of results")
	memorySearchCmd.Flags().StringVar(&memorySearchDimension, "dimension", "", "Filter by dimension (only applies to memory-source hits; knowledge hits are dropped)")
	memoryCmd.AddCommand(memoryShowCmd)

	// MCP commands (desktop MCP Server Hub)
	rootCmd.AddCommand(mcpCmd)
	mcpCmd.AddCommand(mcpListCmd)
	mcpCmd.AddCommand(mcpSourcesCmd)
	mcpCmd.AddCommand(mcpDiscoverCmd)
	mcpCmd.AddCommand(mcpInstallCmd)
	mcpInstallCmd.Flags().StringVar(&mcpInstallSource, "source", "", "Install source: manual or local (default: manual)")
	mcpInstallCmd.Flags().StringVar(&mcpInstallRef, "ref", "", "For --source local: <source-id>:<name> from `yansu mcp discover`")
	mcpInstallCmd.Flags().StringVar(&mcpInstallID, "id", "", "Server id (manual installs)")
	mcpInstallCmd.Flags().StringVar(&mcpInstallName, "name", "", "Display name (manual installs; defaults to id)")
	mcpInstallCmd.Flags().StringVar(&mcpInstallTransport, "transport", "stdio", "Transport: stdio, sse, or http")
	mcpInstallCmd.Flags().StringVar(&mcpInstallCommand, "command", "", "Executable to spawn (stdio transport)")
	mcpInstallCmd.Flags().StringSliceVar(&mcpInstallArgs, "args", nil, "Command arguments (repeatable or comma-separated)")
	mcpInstallCmd.Flags().StringVar(&mcpInstallURL, "url", "", "Endpoint URL (sse/http transport)")
	mcpInstallCmd.Flags().StringSliceVar(&mcpInstallEnv, "env", nil, "Environment variables KEY=VALUE (repeatable or comma-separated)")
	mcpInstallCmd.Flags().StringVar(&mcpInstallWorkDir, "work-dir", "", "Working directory (manual stdio installs)")
	mcpInstallCmd.Flags().BoolVar(&mcpInstallNoStart, "no-auto-start", false, "Persist the server but do not spawn it yet")
	mcpCmd.AddCommand(mcpEnableCmd)
	mcpCmd.AddCommand(mcpDisableCmd)
	mcpCmd.AddCommand(mcpRestartCmd)
	mcpCmd.AddCommand(mcpStopCmd)
	mcpCmd.AddCommand(mcpToolsCmd)
	mcpCmd.AddCommand(mcpTailCmd)
	mcpTailCmd.Flags().IntVarP(&mcpTailLines, "lines", "n", 50, "Number of stderr lines to return")
	mcpCmd.AddCommand(mcpUninstallCmd)
	mcpCmd.AddCommand(mcpRegistryCmd)
	mcpRegistryCmd.AddCommand(mcpRegistryListCmd)
	mcpRegistryCmd.AddCommand(mcpRegistrySearchCmd)
	mcpRegistryCmd.AddCommand(mcpRegistryRefreshCmd)
}
