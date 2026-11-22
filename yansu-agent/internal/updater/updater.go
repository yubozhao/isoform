package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// GitHub API URL for latest release
	releasesURL = "https://api.github.com/repos/Isoform/yansu-releases/releases/latest"
	// Download URL template
	downloadURL = "https://github.com/Isoform/yansu-releases/releases/download/%s/%s"
)

// Release represents a GitHub release
type Release struct {
	TagName string  `json:"tag_name"`
	Name    string  `json:"name"`
	Assets  []Asset `json:"assets"`
}

// Asset represents a release asset
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// UpdateResult contains the result of an update operation
type UpdateResult struct {
	CurrentVersion string
	LatestVersion  string
	Updated        bool
}

// CheckLatestVersion checks the latest available version
func CheckLatestVersion(ctx context.Context) (string, error) {
	release, err := getLatestRelease(ctx)
	if err != nil {
		return "", err
	}
	return release.TagName, nil
}

// Update checks for updates and applies them if available
func Update(ctx context.Context, currentVersion string, force bool) (*UpdateResult, error) {
	result := &UpdateResult{
		CurrentVersion: currentVersion,
	}

	// Get latest release info
	release, err := getLatestRelease(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check for updates: %w", err)
	}

	result.LatestVersion = release.TagName

	// Compare versions
	if !force && !isNewerVersion(currentVersion, release.TagName) {
		return result, nil
	}

	// Find the appropriate asset for this platform
	assetName := getBinaryName()
	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}

	if downloadURL == "" {
		return nil, fmt.Errorf("no binary available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// Download the new binary
	newBinary, err := downloadBinary(ctx, downloadURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download update: %w", err)
	}
	defer os.Remove(newBinary)

	// Replace the current binary
	if err := replaceBinary(newBinary); err != nil {
		return nil, fmt.Errorf("failed to apply update: %w", err)
	}

	result.Updated = true
	return result, nil
}

//isoform:destination metadata
func getLatestRelease(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", releasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "yansu-agent")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

func getBinaryName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("yansu-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)
}

//isoform:destination metadata
func downloadBinary(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "yansu-agent")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Create temp file
	tmpFile, err := os.CreateTemp("", "yansu-update-*")
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()

	// Copy with progress (could add progress bar here)
	_, err = io.Copy(tmpFile, resp.Body)
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", err
	}

	// Make executable
	if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
		os.Remove(tmpFile.Name())
		return "", err
	}

	return tmpFile.Name(), nil
}

func replaceBinary(newBinaryPath string) error {
	// Get current executable path
	execPath, err := os.Executable()
	if err != nil {
		return err
	}

	// Resolve symlinks
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return err
	}

	// On Windows, we need to rename the old binary first
	if runtime.GOOS == "windows" {
		oldPath := execPath + ".old"
		os.Remove(oldPath) // Remove any existing .old file
		if err := os.Rename(execPath, oldPath); err != nil {
			return err
		}
	}

	// Copy new binary to exec path
	src, err := os.Open(newBinaryPath)
	if err != nil {
		return err
	}
	defer src.Close()

	// Create new file (or replace existing on Unix)
	dst, err := os.OpenFile(execPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}

	return nil
}

// isNewerVersion compares two version strings (e.g., "v1.0.0" vs "v1.1.0")
func isNewerVersion(current, latest string) bool {
	// Strip 'v' prefix if present
	current = strings.TrimPrefix(current, "v")
	latest = strings.TrimPrefix(latest, "v")

	// Simple comparison - could be improved with semver library
	currentParts := strings.Split(current, ".")
	latestParts := strings.Split(latest, ".")

	for i := 0; i < len(latestParts) && i < len(currentParts); i++ {
		var cp, lp int
		fmt.Sscanf(currentParts[i], "%d", &cp)
		fmt.Sscanf(latestParts[i], "%d", &lp)

		if lp > cp {
			return true
		}
		if lp < cp {
			return false
		}
	}

	return len(latestParts) > len(currentParts)
}
