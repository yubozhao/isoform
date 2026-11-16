package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"

	"wails-gui/internal/platform/shellenv"
	agenttools "yansu-agent/agent/tools"
)

const (
	yansuCLIBundleDirName = "yansu-cli-bundle"
	yansuCLIInstallRoot   = "bin"
)

func ensureBundledYansuAssets() {
	installedPath, err := ensureBundledYansuCLI()
	if err != nil {
		log.Printf("[yansu-cli] bundled install skipped: %v", err)
	}
	if installedPath != "" {
		ensurePathDirInEnv(installedPath)
	}

	// Drop install.json so external tools (notably the Yansu skill installed
	// into coding agents) can locate this app and its bundled CLI without
	// asking the user — Windows ships a portable .zip with no fixed install
	// path, so discovery via a file in a stable per-user location is the only
	// reliable cross-platform option.
	writeInstallDiscovery()
}

// yansuCLIInstallMu keeps the launch task and the assistant router from
// copying the CLI at the same time (both write destPath+".tmp").
var yansuCLIInstallMu sync.Mutex

func ensureBundledYansuCLI() (string, error) {
	yansuCLIInstallMu.Lock()
	defer yansuCLIInstallMu.Unlock()
	srcPath := findBundledYansuCLI()
	if srcPath == "" {
		return "", fmt.Errorf("bundled yansu binary not found")
	}

	dataDir, err := flavorDataDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	destDir := filepath.Join(dataDir, yansuCLIInstallRoot)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create install directory: %w", err)
	}

	destPath := filepath.Join(destDir, bundledYansuCLIExecutableName())
	if shouldCopyBundledCLI(srcPath, destPath) {
		if err := copyFileExecutable(srcPath, destPath); err != nil {
			return "", fmt.Errorf("install bundled yansu binary: %w", err)
		}
		log.Printf("[yansu-cli] installed bundled binary to %s", destPath)
	}

	if goruntime.GOOS != "windows" {
		_ = os.Chmod(destPath, 0o755)
	}
	if err := agenttools.RegisterTrustedYansuCLI(destPath, srcPath); err != nil {
		// Installation remains usable outside the sandbox. An unverified CLI
		// receives no exception to the agent's credential hard blocks.
		log.Printf("[yansu-cli] sandbox credential access unavailable: %v", err)
	}
	return destPath, nil
}

// expectedYansuCLIPath is where ensureBundledYansuCLI installs the CLI, whether
// or not it is there yet; "" when the data folder cannot be resolved.
func expectedYansuCLIPath() string {
	dataDir, err := flavorDataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dataDir, yansuCLIInstallRoot, bundledYansuCLIExecutableName())
}

// assistantToolShimPath is the CLI path the assistant router hands Claude Code
// and Codex. The CLI is installed by a launch task that can finish after the
// router is built (a new data folder, the first launch). An empty path
// reported both assistants unavailable until the next restart, and a path the
// CLI was not at yet failed the first run's tool-server check, which paused
// Claude Code for ten minutes (daily trial, 2026-11-15). So the router
// installs the CLI itself when it is not there yet, and keeps the path it
// will be at only when there is no bundle to install from.
func assistantToolShimPath() string {
	if path := installedYansuCLIPath(); path != "" {
		return path
	}
	if path, err := ensureBundledYansuCLI(); err == nil && path != "" {
		return path
	}
	return expectedYansuCLIPath()
}

func installedYansuCLIPath() string {
	dataDir, err := flavorDataDir()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(dataDir, yansuCLIInstallRoot, bundledYansuCLIExecutableName())
	if shellenv.IsExecutableFile(candidate) {
		return candidate
	}
	return ""
}

func bundledYansuCLIExecutableName() string {
	if goruntime.GOOS == "windows" {
		return "yansu.exe"
	}
	return "yansu"
}

func findBundledYansuCLI() string {
	for _, candidate := range bundledYansuCLIPathCandidates() {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if shellenv.IsExecutableFile(candidate) {
			return candidate
		}
	}
	return ""
}

func bundledYansuCLIPathCandidates() []string {
	executable := bundledYansuCLIExecutableName()
	candidates := make([]string, 0, 2)

	// Startup installs this binary into the user's PATH, and install.json and
	// MCP registration persist its location. Only trust the app's own bundle;
	// the working directory can belong to an unrelated project.
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, yansuCLIBundleDirName, "bin", executable),
			filepath.Join(exeDir, "..", "Resources", yansuCLIBundleDirName, "bin", executable),
		)
	}

	return candidates
}

func shouldCopyBundledCLI(srcPath, destPath string) bool {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return false
	}
	destInfo, err := os.Stat(destPath)
	if err != nil {
		return true
	}
	if srcInfo.Size() != destInfo.Size() {
		return true
	}
	return srcInfo.ModTime().After(destInfo.ModTime())
}

func copyFileExecutable(srcPath, destPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}

	tmpPath := destPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func ensurePathDirInEnv(binaryPath string) {
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		return
	}
	dir := filepath.Dir(binaryPath)
	if dir == "" || dir == "." {
		return
	}

	pathValue := os.Getenv("PATH")
	if pathValue == "" {
		pathValue = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	sep := string(os.PathListSeparator)
	cleanDir := filepath.Clean(dir)

	// Prepend the bundled install dir (and strip any existing entries
	// pointing to the same location) so the bundled CLI wins over a
	// system-installed yansu (e.g. an older /usr/local/bin/yansu).
	var filtered []string
	for _, existing := range strings.Split(pathValue, sep) {
		if filepath.Clean(strings.TrimSpace(existing)) == cleanDir {
			continue
		}
		filtered = append(filtered, existing)
	}
	_ = os.Setenv("PATH", dir+sep+strings.Join(filtered, sep))
}
