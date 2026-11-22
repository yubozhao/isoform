package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/mod/semver"

	"wails-gui/internal/platform/activitymon"
)

// Version is the current application version.
// Override at build time: -ldflags "-X main.Version=0.2.0"
var Version = "0.1.0"

// GitCommit, GitDirty, BuildTime are populated by build-wails.sh via ldflags
// so every released binary carries its source provenance. Empty values mean
// this is a raw `go build` or `wails dev` run; perf_analysis.go then falls
// back to `runtime/debug.ReadBuildInfo()` which surfaces the same data from
// Go's built-in VCS metadata.
var GitCommit = ""
var GitDirty = ""
var BuildTime = ""

// defaultDesktopReleasesRepo is the single default for feed and API discovery.
const defaultDesktopReleasesRepo = "yubozhao/isoform-releases"

// Override at build time when DESKTOP_RELEASES_REPO selects another destination.
var desktopReleasesRepo = defaultDesktopReleasesRepo

// R2ManifestURL retains its linker name for compatibility. The primary update
// channel is now version.json attached to the latest public GitHub release.
// Override at build time together with desktopReleasesRepo for another channel.
var R2ManifestURL string

func updateFeedURL() string {
	if updateChannel() == "beta" {
		return "https://github.com/" + desktopReleasesRepo + "/releases/download/beta-feed/version.json"
	}
	if R2ManifestURL != "" {
		return R2ManifestURL
	}
	return "https://github.com/" + desktopReleasesRepo + "/releases/latest/download/version.json"
}

func updateChannel() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("ISOFORM_UPDATE_CHANNEL")), "beta") {
		return "beta"
	}
	return "stable"
}

// r2Manifest is the update JSON structure, also supported by older R2 channels.
type r2Manifest struct {
	Version          string            `json:"version"`
	ReleaseNotes     string            `json:"release_notes"`
	Assets           map[string]string `json:"assets"`                      // legacy single-file updater payloads
	Checksums        map[string]string `json:"checksums,omitempty"`         // platform -> SHA-256 hex digest
	UpdaterAssets    map[string]string `json:"updater_assets,omitempty"`    // current multi-file updater archives
	UpdaterChecksums map[string]string `json:"updater_checksums,omitempty"` // platform -> SHA-256 hex digest
	PublishedAt      string            `json:"published_at,omitempty"`      // RFC 3339 release time, when the feed has it
}

// githubAsset is one file attached to a GitHub release.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// githubRelease is the relevant subset of the GitHub releases/latest API response.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Body    string        `json:"body"`
	Assets  []githubAsset `json:"assets"`
}

// UpdateInfo is returned to the frontend.
type UpdateInfo struct {
	Available    bool   `json:"available"`
	Current      string `json:"current"`
	Latest       string `json:"latest"`
	DownloadURL  string `json:"download_url,omitempty"`
	ReleaseNotes string `json:"release_notes,omitempty"`
	Error        string `json:"error,omitempty"`
	// ReleasedAt is unix ms from the feed's published_at; internal, never sent to the frontend.
	ReleasedAt int64 `json:"-"`
}

// UpdateStatus is the payload broadcast to the frontend via the
// "update:status" Wails event — mirrors Alma's AutoUpdateStatusPayload.
type UpdateStatus struct {
	Status      string  `json:"status"`                // idle|disabled|checking|up-to-date|update-available|downloading|update-downloaded|error
	Version     string  `json:"version,omitempty"`     // new version string
	Detail      string  `json:"detail,omitempty"`      // error message
	Percent     float64 `json:"percent,omitempty"`     // download progress 0-100
	Transferred int64   `json:"transferred,omitempty"` // bytes downloaded
	Total       int64   `json:"total,omitempty"`       // total bytes
	// RestartNeeded is set when the staged update failed the quit probe: it
	// installs only through Restart now, which runs the full checks.
	RestartNeeded bool `json:"restart_needed,omitempty"`
}

// updater holds the runtime state for the self-update lifecycle.
type updater struct {
	opMu               sync.Mutex // serializes check, download and install
	mu                 sync.Mutex
	status             UpdateStatus
	downloadURL        string  // set after a successful check finds an update
	stagingPath        string  // path to the downloaded update asset
	peakPercent        float64 // highest percent ever broadcast — prevents progress bar regression
	expectedChecksum   string  // SHA-256 hex digest paired with the selected release asset
	targetVersion      string
	downloadedChecksum string
	downloadedVersion  string
	info               UpdateInfo
	recoveryError      bool
	lastChecked        int64
	later              bool
	// rollbackNotice is the one-time message after a rollback, cleared once About shows it.
	rollbackNotice      string
	backgroundQueued    bool
	backgroundRequested bool
	backgroundDone      chan struct{}
	prepared            *preparedUpdate // extracted and verified app for a fast restart
	prepareFailed       string          // version/checksum whose preparation failed
	targetNotes         string          // release notes for targetVersion
	targetReleasedAt    int64           // unix ms the feed published targetVersion, 0 if unknown
	stagedAt            int64           // unix ms the staged archive was verified
	releasedAt          int64           // unix ms the staged version was published, 0 if unknown
	blockedVersion      string          // version whose install failed; background downloads and quit installs skip it
	recoveryNextCheck   time.Time       // earliest scheduled check after a failed install
	recoveryBackoff     time.Duration   // gap used for the last recovery check
	installStarted      bool            // a click or quit install has swapped the bundle
	restartNeeded       bool            // the staged version failed the quit probe
	quietDownload       bool            // a newer release is downloading behind a ready update: no progress
}

// initUpdater starts the background update checker — called once from
// App.startup after the Wails context is available.
func (a *App) initUpdater() {
	if a.updater == nil {
		a.updater = &updater{status: UpdateStatus{Status: "idle"}}
	}

	a.reportUpdateRecovery()
	a.reportInstalledUpdate()
	a.reportRolledBackUpdate()
	if startUpdateQAHook(a) {
		return
	}

	// Sweep any .yansu-old / .yansu-update-* remnants left over from a
	// prior auto-update. installArchive renames the old exe and DLLs aside
	// before swapping new ones into place, but it can't delete them while
	// the previous process holds them open on Windows — so the cleanup
	// runs on the next startup, when the loader has finally released them.
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			cleanLeakedUpdateRemnants(filepath.Dir(exe))
		}
	}

	if isReleaseChannel() {
		activitymon.StartSystemStateObservers()
		wake, unsubscribe := activitymon.SubscribeWake()
		go func() {
			defer unsubscribe()
			runUpdateScheduleWithWake(a.ctx, updateLaunchDelay, updateCheckInterval, wake, a.autoCheckForUpdates)
		}()
	}
}

// broadcastUpdateStatus emits the "update:status" event to the frontend.
// For "downloading" events the percent is clamped to the highest value seen
// so the progress bar never visually regresses during download retries.
func (a *App) broadcastUpdateStatus(s UpdateStatus) {
	a.updater.mu.Lock()
	if s.Status == "downloading" && a.updater.quietDownload {
		// A newer release downloads behind the ready one: the cards keep showing the ready one.
		a.updater.mu.Unlock()
		return
	}
	if s.Status == "update-downloaded" {
		s.RestartNeeded = a.updater.restartNeeded
	}
	if s.Status == "downloading" {
		if s.Percent < a.updater.peakPercent {
			s.Percent = a.updater.peakPercent
		} else {
			a.updater.peakPercent = s.Percent
		}
	} else {
		// Reset peak when leaving the downloading state (e.g. error, downloaded, idle).
		a.updater.peakPercent = 0
	}
	wasReady := a.updater.status.Status == "update-downloaded"
	a.updater.status = s
	a.updater.mu.Unlock()

	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "update:status", s)
		// The app menu's Restart to Update follows a downloaded update (design S3).
		if wasReady != (s.Status == "update-downloaded") {
			a.refreshAppMenu()
		}
	}
}

// ── Public API (exposed to frontend via Wails bindings) ─────────────

// GetVersion returns the current application version.
func (a *App) GetVersion() string {
	return Version
}

// GetUpdateStatus returns the last known update status so the frontend
// can initialise its state on load.
func (a *App) GetUpdateStatus() UpdateStatus {
	if a.updater == nil {
		return UpdateStatus{Status: "idle"}
	}
	a.updater.mu.Lock()
	defer a.updater.mu.Unlock()
	return a.updater.status
}

// CheckForUpdates fetches the latest release, compares versions,
// and returns the result. This is the user-triggered manual check.
func (a *App) CheckForUpdates() UpdateInfo {
	a.updater.opMu.Lock()
	defer func() {
		a.updater.opMu.Unlock()
		a.queueBackgroundDownload()
	}()
	a.updater.mu.Lock()
	// Repeated manual checks may hydrate release notes,
	// including after a download. Keep that verified selection installable.
	if a.updater.stagingPath != "" && a.updater.info.Available {
		info := a.updater.info
		a.updater.mu.Unlock()
		return info
	}
	a.updater.recoveryError = false
	a.updater.recoveryNextCheck = time.Time{}
	a.updater.recoveryBackoff = 0
	a.updater.mu.Unlock()
	return a.checkForUpdates()
}

func (a *App) checkForUpdates() UpdateInfo {
	if !isReleaseChannel() {
		a.broadcastUpdateStatus(UpdateStatus{Status: "disabled"})
		return UpdateInfo{Current: Version}
	}
	a.updater.mu.Lock()
	stale := a.updater.stagingPath
	a.updater.stagingPath = ""
	a.updater.downloadURL = ""
	a.updater.expectedChecksum = ""
	a.updater.downloadedChecksum = ""
	a.updater.mu.Unlock()
	if stale != "" {
		_ = os.Remove(stale)
	}
	a.broadcastUpdateStatus(UpdateStatus{Status: "checking"})

	info := a.fetchLatestRelease()
	a.recordUpdateCheck(info)
	a.updater.mu.Lock()
	a.updater.info = info
	a.updater.lastChecked = time.Now().UnixMilli()
	a.updater.mu.Unlock()

	if info.Error != "" {
		a.broadcastUpdateStatus(UpdateStatus{Status: "error", Detail: info.Error})
		return info
	}
	if !info.Available {
		a.broadcastUpdateStatus(UpdateStatus{Status: "up-to-date"})
		return info
	}

	a.updater.mu.Lock()
	a.setTargetLocked(info)
	a.updater.mu.Unlock()

	a.broadcastUpdateStatus(UpdateStatus{
		Status:  "update-available",
		Version: info.Latest,
	})
	return info
}

// downloadMaxRetries is the number of automatic retry attempts for the update download.
const downloadMaxRetries = 3

// downloadIdleTimeout is how long the download can stall (no data received)
// before it is considered failed.
const downloadIdleTimeout = 30 * time.Second

// DownloadUpdate downloads the update asset to a staging directory and
// emits progress events. Call CheckForUpdates first so the download URL
// is known. The download automatically retries up to 3 times and resumes
// from partial data using HTTP Range requests.
func (a *App) DownloadUpdate() error { return a.downloadUpdate(false) }

func (a *App) downloadUpdate(background bool) error {
	a.updater.opMu.Lock()
	defer a.updater.opMu.Unlock()
	// Recheck policy after acquiring operation ownership: a queued download
	// must not start after the person has turned background updates off.
	if background && (!isReleaseChannel() || !backgroundUpdatesEnabled() || (a.ctx != nil && a.ctx.Err() != nil)) {
		return nil
	}
	a.updater.mu.Lock()
	staged := a.updater.stagingPath
	url := a.updater.downloadURL
	target := a.updater.targetVersion
	if staged != "" {
		// A waiting update is replaced only by a newer release, verified in the other slot.
		newer := background && url != "" && target != "" && isNewer(target, a.updater.downloadedVersion)
		a.updater.mu.Unlock()
		if !newer {
			return nil
		}
		return a.downloadNewerStaged(url, target)
	}
	if background && a.updater.blocksBackground(target) {
		a.updater.mu.Unlock()
		return nil
	}
	a.updater.mu.Unlock()

	if url == "" {
		if background {
			return nil
		}
		return fmt.Errorf("no update available to download")
	}

	// Staging directory: ~/.yansu-agent/updates/
	dataRoot, err := flavorDataDir()
	if err != nil {
		return fmt.Errorf("cannot locate home dir: %w", err)
	}
	stagingDir := filepath.Join(dataRoot, "updates")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}

	stagingPath := filepath.Join(stagingDir, platformAssetName())
	lastErr := a.downloadWithRetries(newUpdateDownloadClient(), url, stagingPath)
	if lastErr == nil {
		a.recordStagedUpdate()
		a.queuePrepareDownloadedUpdate()
		return nil
	}

	// All retries exhausted.
	os.Remove(stagingPath)
	a.broadcastUpdateStatus(UpdateStatus{Status: "error", Detail: lastErr.Error()})
	return lastErr
}

// downloadAttempt performs a single download attempt, resuming from any
// partial file already on disk via HTTP Range.
//
//isoform:destination metadata
func (a *App) downloadAttempt(client *http.Client, url, stagingPath string, attempt int) error {
	a.updater.mu.Lock()
	expected := a.updater.expectedChecksum
	a.updater.mu.Unlock()
	if decoded, err := hex.DecodeString(expected); err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("update checksum is unavailable or invalid")
	}

	// Check for existing partial file to resume.
	var existingSize int64
	if info, err := os.Stat(stagingPath); err == nil {
		existingSize = info.Size()
	}

	// Use a cancellable context so we can abort on idle timeout.
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	// 416 Range Not Satisfiable: the local file is already complete (or
	// larger than the remote). Delete it and retry from scratch.
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && existingSize > 0 {
		os.Remove(stagingPath)
		log.Printf("[update] got 416, local file complete or stale — restarting download")
		existingSize = 0
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		resp, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("download failed: %w", err)
		}
		defer resp.Body.Close()
	}

	var total int64
	var transferred int64

	switch resp.StatusCode {
	case http.StatusOK:
		// Server does not support Range or sent the full file -- start fresh.
		total = resp.ContentLength
		transferred = 0
		existingSize = 0
	case http.StatusPartialContent:
		// Server honored Range -- resume from existingSize.
		total = existingSize + resp.ContentLength
		transferred = existingSize
	default:
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	// Open file for writing (append if resuming, create otherwise).
	var flags int
	if existingSize > 0 && resp.StatusCode == http.StatusPartialContent {
		flags = os.O_WRONLY | os.O_APPEND
	} else {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(stagingPath, flags, 0o644)
	if err != nil {
		return fmt.Errorf("open staging file: %w", err)
	}

	a.broadcastUpdateStatus(UpdateStatus{
		Status:      "downloading",
		Percent:     safePercent(transferred, total),
		Transferred: transferred,
		Total:       total,
	})

	// Idle timer: cancels the context (and thus the read) if no data arrives
	// within downloadIdleTimeout.
	idleTimer := time.AfterFunc(downloadIdleTimeout, cancel)
	defer idleTimer.Stop()

	// Copy with progress reporting.
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			// Reset idle timer on every successful read.
			idleTimer.Reset(downloadIdleTimeout)

			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				f.Close()
				os.Remove(stagingPath)
				return fmt.Errorf("write staging file: %w", writeErr)
			}
			transferred += int64(n)

			a.broadcastUpdateStatus(UpdateStatus{
				Status:      "downloading",
				Percent:     safePercent(transferred, total),
				Transferred: transferred,
				Total:       total,
			})
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			// Keep partial file for resume on next attempt.
			if ctx.Err() != nil {
				return fmt.Errorf("download stalled (no data for %v)", downloadIdleTimeout)
			}
			return fmt.Errorf("download read: %w", readErr)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close staging file: %w", err)
	}

	// Verify the digest paired with this selected archive, including resumed bytes.

	if err := verifySHA256(stagingPath, expected); err != nil {
		_ = os.Remove(stagingPath)
		return fmt.Errorf("checksum verification failed: %w", err)
	}
	a.updater.mu.Lock()
	version, notes, released := a.updater.targetVersion, a.updater.targetNotes, a.updater.targetReleasedAt
	a.updater.mu.Unlock()
	// This pending write is the commit point: the archive is staged only once it names this file.
	stagedAt := time.Now().UnixMilli()
	if err := savePendingUpdateRecord(pendingUpdate{Path: stagingPath, Checksum: expected, Version: version,
		ReleaseNotes: notes, StagedAt: stagedAt, ReleasedAt: released}); err != nil {
		return err
	}
	a.updater.mu.Lock()
	a.updater.stagingPath = stagingPath
	a.updater.downloadedChecksum = expected
	a.updater.downloadedVersion = a.updater.targetVersion
	a.updater.stagedAt = stagedAt
	a.updater.releasedAt = released
	// A freshly verified archive has not been probed yet.
	a.updater.restartNeeded = false
	ver := a.updater.targetVersion
	a.updater.mu.Unlock()

	a.broadcastUpdateStatus(UpdateStatus{
		Status:  "update-downloaded",
		Version: ver,
	})

	log.Printf("[update] downloaded update to %s (%d bytes)", stagingPath, transferred)
	return nil
}

// safePercent computes a download percentage, returning 0 when total is unknown.
func safePercent(transferred, total int64) float64 {
	if total > 0 {
		return float64(transferred) / float64(total) * 100
	}
	return 0
}

// verifySHA256 computes the SHA-256 digest of the file at path and compares
// it against the expected hex string. Returns nil on match.
func verifySHA256(path, expected string) error {
	if !validUpdateChecksum(expected) {
		return fmt.Errorf("update checksum is unavailable or invalid")
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash file: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("expected %s, got %s", expected, got)
	}
	return nil
}

// QuitAndInstall applies the downloaded update and restarts the app.
// Mirrors Alma's quitAndInstall().
func (a *App) QuitAndInstall() error {
	a.updater.opMu.Lock()
	defer a.updater.opMu.Unlock()
	a.updater.mu.Lock()
	staging := a.updater.stagingPath
	checksum := a.updater.downloadedChecksum
	a.updater.mu.Unlock()

	if staging == "" {
		return fmt.Errorf("no downloaded update to install")
	}

	var err error
	switch runtime.GOOS {
	case "darwin":
		// A prepared update needs no rehash: the archive is not read again.
		err = a.installDarwin(staging, checksum)
	default:
		if err = verifySHA256(staging, checksum); err == nil {
			err = a.installArchive(staging)
		}
	}
	if err != nil {
		// Remove the corrupted/broken staging file so the next attempt
		// starts a fresh download instead of resuming from bad data.
		os.Remove(staging)
		a.updater.mu.Lock()
		a.updater.stagingPath = ""
		a.updater.mu.Unlock()
		a.broadcastUpdateStatus(UpdateStatus{Status: "error", Detail: err.Error()})
		return err
	}
	return nil
}

// ── Internal helpers ────────────────────────────────────────────────

// autoCheckForUpdates runs a background check and broadcasts the result.
func (a *App) autoCheckForUpdates() {
	if !a.updater.opMu.TryLock() {
		return
	}
	defer func() {
		a.updater.opMu.Unlock()
		a.queueBackgroundDownload()
	}()
	a.updater.mu.Lock()
	staged := a.updater.stagingPath != ""
	recovery := a.updater.recoveryError
	due := !time.Now().Before(a.updater.recoveryNextCheck)
	a.updater.mu.Unlock()
	if staged && runtime.GOOS == "darwin" {
		// Covers a download restored at launch or a preparation cut short.
		a.prepareDownloadedUpdateLocked(findAppBundle(), darwinInstallSteps.verify)
	}
	switch {
	case staged:
		// Keep checking while an update waits: a newer verified release replaces it.
		a.checkNewerThanStaged()
	case recovery:
		// After a failed install, retry with backoff. The failed version is never downloaded again.
		if due {
			a.checkAfterRecovery()
		}
	default:
		// Background failures stay in the event stream, so Settings can show them.
		a.checkForUpdates()
	}
}

// fetchLatestRelease checks the JSON feed first, falling back to the GitHub API.
func (a *App) fetchLatestRelease() UpdateInfo {
	// The Isoform development build is built from the working tree and
	// shares the release's identity, so an update would replace the dev app
	// with a release. Only release builds check the feed.
	if !isReleaseChannel() {
		return UpdateInfo{Available: false, Current: Version}
	}

	info := a.fetchFromR2()
	if info.Error == "" {
		return info
	}
	if updateChannel() == "beta" {
		return info
	}
	log.Printf("[update] manifest check failed (%s), falling back to GitHub", info.Error)
	return a.fetchFromGitHub()
}

// fetchFromR2 queries version.json (the name is retained for channel compatibility).
//
//isoform:destination metadata
func (a *App) fetchFromR2() UpdateInfo {
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(updateFeedURL())
	if err != nil {
		return UpdateInfo{Current: Version, Error: fmt.Sprintf("R2 request failed: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return UpdateInfo{Current: Version, Error: fmt.Sprintf("R2 returned %d", resp.StatusCode)}
	}

	var manifest r2Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return UpdateInfo{Current: Version, Error: fmt.Sprintf("R2 invalid response: %v", err)}
	}

	latest := strings.TrimPrefix(manifest.Version, "v")
	if updateChannel() != "beta" && semver.Prerelease("v"+latest) != "" {
		return UpdateInfo{Current: Version, Error: "Stable feed contains a prerelease"}
	}
	if !isNewer(latest, Version) {
		return UpdateInfo{Available: false, Current: Version, Latest: latest, ReleaseNotes: manifest.ReleaseNotes}
	}

	platformKey := runtime.GOOS + "-" + displayArch()
	downloadURL, checksum, ok := manifest.updateAssetForPlatform(platformKey)
	if !ok {
		return UpdateInfo{
			Current: Version,
			Error:   fmt.Sprintf("No R2 update asset for platform %s", platformKey),
		}
	}

	if !validUpdateChecksum(checksum) {
		return UpdateInfo{Current: Version, Error: "Update feed has no valid SHA-256 checksum"}
	}
	// Store the checksum paired with the chosen asset set.
	a.setExpectedUpdateChecksum(checksum)

	return UpdateInfo{
		Available:    true,
		Current:      Version,
		Latest:       latest,
		DownloadURL:  downloadURL,
		ReleaseNotes: manifest.ReleaseNotes,
		ReleasedAt:   parseReleaseTimeMillis(manifest.PublishedAt),
	}
}

// updateAssetForPlatform returns the updater artifact URL and checksum for
// the current manifest generation. New manifests should put multi-file
// updater archives in updater_assets and reserve assets for legacy clients
// that only understand single-file .gz payloads. Manifests generated before
// updater_assets existed continue to work by falling back to assets.
func (m r2Manifest) updateAssetForPlatform(platformKey string) (string, string, bool) {
	if len(m.UpdaterAssets) > 0 {
		downloadURL := m.UpdaterAssets[platformKey]
		if downloadURL == "" {
			return "", "", false
		}
		return downloadURL, m.UpdaterChecksums[platformKey], true
	}

	downloadURL := m.Assets[platformKey]
	if downloadURL == "" {
		return "", "", false
	}
	return downloadURL, m.Checksums[platformKey], true
}

func (a *App) setExpectedUpdateChecksum(checksum string) {
	if a.updater == nil {
		return
	}
	a.updater.mu.Lock()
	a.updater.expectedChecksum = checksum
	a.updater.mu.Unlock()
}

// fetchFromGitHub queries the GitHub API for the latest release.
//
//isoform:destination metadata
func (a *App) fetchFromGitHub() UpdateInfo {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", desktopReleasesRepo)
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return UpdateInfo{Current: Version, Error: fmt.Sprintf("Failed to build request: %v", err)}
	}
	req.Header.Set("User-Agent", "Yansu-Desktop/"+Version)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return UpdateInfo{Current: Version, Error: fmt.Sprintf("Failed to check: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return UpdateInfo{Current: Version, Error: fmt.Sprintf("GitHub API returned %d", resp.StatusCode)}
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return UpdateInfo{Current: Version, Error: fmt.Sprintf("Invalid response: %v", err)}
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	if !isNewer(latest, Version) {
		return UpdateInfo{Available: false, Current: Version, Latest: latest, ReleaseNotes: release.Body}
	}

	// Match by suffix so versioned ("Isoform-0.2.0-darwin-arm64.app.tar.gz")
	// and unversioned ("Isoform-darwin-arm64.app.tar.gz") release assets both
	// resolve. The release tag is the source of truth for the version, so
	// the asset filename only needs to disambiguate platform/arch.
	assetSuffix := platformAssetSuffix()
	downloadURL, assetName := "", ""
	for _, asset := range release.Assets {
		if strings.HasSuffix(asset.Name, assetSuffix) {
			downloadURL, assetName = asset.BrowserDownloadURL, asset.Name
			break
		}
	}
	if downloadURL == "" {
		return UpdateInfo{
			Current: Version,
			Error:   fmt.Sprintf("No asset matching *%s in release %s", assetSuffix, release.TagName),
		}
	}

	checksum, err := fetchGitHubAssetChecksum(client, release.Assets, assetName)
	if err != nil {
		a.setExpectedUpdateChecksum("")
		return UpdateInfo{Current: Version, Error: fmt.Sprintf("Release checksum unavailable: %v", err)}
	}
	a.setExpectedUpdateChecksum(checksum)

	return UpdateInfo{
		Available:    true,
		Current:      Version,
		Latest:       latest,
		DownloadURL:  downloadURL,
		ReleaseNotes: release.Body,
	}
}

// displayArch maps Go's runtime.GOARCH to the display name used in
// release filenames (x64/arm64), matching the industry convention used
// by Cursor, VS Code, Node.js, etc.
func displayArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	default:
		return runtime.GOARCH
	}
}

// platformAssetName returns the local staging filename for a downloaded
// update. The unversioned form is used so successive updates overwrite the
// same path; SHA-256 verification catches any partial-write or stale-bytes
// corruption that the resumable download could leave behind.
//
// Each platform uses an archive format that can carry the full set of
// sibling files the running app needs — the previous bare-binary .gz
// format on Windows/Linux could not carry sherpa-onnx DLLs or the
// quick-chat-spotlight helper, which is what caused the
// "sherpa-onnx-c-api.dll was not found" QA bug on Windows.
func platformAssetName() string {
	arch := displayArch()
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("Isoform-%s-%s.app.tar.gz", runtime.GOOS, arch)
	case "windows":
		return fmt.Sprintf("Isoform-%s-%s.zip", runtime.GOOS, arch)
	default:
		return fmt.Sprintf("Isoform-%s-%s.tar.gz", runtime.GOOS, arch)
	}
}

// platformAssetSuffix is the trailing portion of a release asset filename
// for the current platform — used by the GitHub fallback to locate the
// right asset whether or not the release embeds the version in the name.
func platformAssetSuffix() string {
	arch := displayArch()
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("-%s-%s.app.tar.gz", runtime.GOOS, arch)
	case "windows":
		return fmt.Sprintf("-%s-%s.zip", runtime.GOOS, arch)
	default:
		return fmt.Sprintf("-%s-%s.tar.gz", runtime.GOOS, arch)
	}
}

// ── macOS: full .app bundle replacement ─────────────────────────────

// installDarwin atomically replaces the running .app bundle with the
// prepared update (or, without one, the extracted and verified archive), then
// quits; the helper opens the new app once this process has exited.
func (a *App) installDarwin(stagingPath, checksum string) error {
	a.updater.mu.Lock()
	version, stagedAt, released := a.updater.downloadedVersion, a.updater.stagedAt, a.updater.releasedAt
	a.updater.mu.Unlock()
	if err := a.installBundleUpdate(findAppBundle(), stagingPath, checksum, darwinInstallSteps); err != nil {
		return err
	}
	_ = writeInstalledRecord(installedRecord{Version: version, From: Version, How: updateHowRestartNow,
		ReleasedAt: released, InstalledAt: time.Now().UnixMilli()})
	a.finishInstallRecords(stagingPath, version, stagedAt, updateHowRestartNow)
	a.quitAfterUpdateSwap()
	return nil
}

// cleanLeakedStagingDirs removes empty/orphaned .yansu-update-* directories
// in the .app bundle's parent dir. Earlier versions of installDarwin lost
// their cleanup to os.Exit; this catches up any leaked dirs and is safe to
// call from app startup or before a new update extracts.
func cleanLeakedStagingDirs(bundleDir string) {
	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), ".yansu-update-") {
			_ = os.RemoveAll(filepath.Join(bundleDir, e.Name()))
		}
	}
}

// findAppBundle walks up from the current executable to find the enclosing
// .app bundle. Returns "" if not running inside one.
func findAppBundle() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for {
		if strings.HasSuffix(dir, ".app") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ── Windows/Linux: multi-file archive replacement ───────────────────

// installArchive extracts the staged archive (.zip on Windows, .tar.gz
// on Linux) and atomically replaces each top-level sibling next to the
// running executable, then restarts the process. Old files are renamed
// aside with a .yansu-old suffix and cleaned up on the next startup
// via cleanLeakedUpdateRemnants — Windows allows renaming a running
// .exe or a loaded DLL but not deleting them, so deletion has to wait
// until the loader releases them.
//
// This replaces the previous installBinary() path which used
// github.com/minio/selfupdate for single-file atomic replacement. That
// path could only carry the bare exe, so any auto-update that needed to
// also refresh sherpa-onnx DLLs, the quick-chat-spotlight helper, or any
// sibling bundle silently lost them — see the
// "sherpa-onnx-c-api.dll was not found" QA report on Windows.
func (a *App) installArchive(stagingPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	installDir := filepath.Dir(exe)

	// Opportunistic cleanup of leaked .yansu-old files / .yansu-update-*
	// staging dirs from prior failed updates. Safe to do because the
	// update flow is single-threaded within a process and concurrent
	// updater instances are blocked by the single-instance lock.
	cleanLeakedUpdateRemnants(installDir)

	tmpDir, err := os.MkdirTemp(installDir, ".yansu-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractUpdateArchive(stagingPath, tmpDir); err != nil {
		return fmt.Errorf("extract failed: %w", err)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("read extracted dir: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("update archive contained no files")
	}

	// All current files are moved aside into a single per-update quarantine
	// dir (one timestamp-suffixed top-level entry) rather than scattering
	// <name>.yansu-old siblings. A fresh dir per attempt also sidesteps the
	// "rename target already exists" failure that a leftover .yansu-old from
	// an earlier crashed update would otherwise cause. Locked files (a loaded
	// DLL, a running git.exe) can't be deleted yet, so the quarantine is swept
	// on the next startup by cleanLeakedUpdateRemnants once the loader lets go.
	quarantine, err := os.MkdirTemp(installDir, ".yansu-old-*")
	if err != nil {
		return fmt.Errorf("create quarantine dir: %w", err)
	}

	// undo holds a rollback closure for every move performed so far, in order.
	// On any single failure (e.g. AV holding one DLL open longer than the
	// rest) we replay them in reverse to restore the prior install exactly.
	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}

	for _, e := range entries {
		name := e.Name()
		target := filepath.Join(installDir, name)
		src := filepath.Join(tmpDir, name)
		aside := filepath.Join(quarantine, name)

		// Lstat, not Stat: a (possibly broken) symlink entry must be detected
		// and moved aside as-is rather than followed to a missing target.
		if _, statErr := os.Lstat(target); statErr == nil {
			if err := os.Rename(target, aside); err != nil {
				// Whole-entry rename is rejected on Windows when a directory
				// still holds an open file handle — e.g. a running git.exe or
				// loaded DLL under git-bundle. Renaming an individual open or
				// executing file IS permitted, so fall back to replacing the
				// directory's contents file by file.
				if e.IsDir() {
					log.Printf("[update] %s locked (%v), replacing file by file", name, err)
					if ferr := replaceDirFileWise(src, target, aside, &undo); ferr != nil {
						rollback()
						return fmt.Errorf("move old %s aside: %w", name, ferr)
					}
					log.Printf("[update] %s replaced file by file", name)
					continue // replaceDirFileWise already moved the new files into place
				}
				rollback()
				return fmt.Errorf("move old %s aside: %w", name, err)
			}
			undo = append(undo, func() { _ = os.Rename(aside, target) })
		}

		if err := os.Rename(src, target); err != nil {
			rollback()
			return fmt.Errorf("move new %s into place: %w", name, err)
		}
		undo = append(undo, func() { _ = os.RemoveAll(target) })
	}

	log.Printf("[update] replaced %d top-level entries in %s", len(entries), installDir)

	// Restart from the new exe (its path is unchanged across the swap).
	cmd := exec.Command(exe)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		rollback()
		return fmt.Errorf("restart failed: %w", err)
	}
	_ = os.Remove(stagingPath)
	_ = os.RemoveAll(quarantine)

	// Explicit cleanup: os.Exit skips the deferred RemoveAll on tmpDir.
	_ = os.RemoveAll(tmpDir)
	// os.Exit skips shutdown, so end any research search first.
	stopResearchProcesses()
	os.Exit(0)
	return nil // unreachable
}

// replaceDirFileWise replaces the contents of dstDir with the freshly
// extracted srcDir, one regular file at a time. It is the fallback for when
// os.Rename can't move dstDir aside wholesale — on Windows a directory rename
// fails while any descendant file is open, but renaming an individual open or
// executing file (a running git.exe, a loaded DLL) is allowed. Each moved file
// goes into asideDir (preserving its relative path) and records an undo closure
// so a later failure can roll the whole install back.
//
// The result is an exact mirror of the new bundle, matching the clean-replace
// semantics of the whole-dir-rename fast path: a first pass quarantines files
// that exist in dstDir but not in srcDir (a removed DLL or renamed binary)
// before the second pass swaps each new file into place. os.Lstat is used so a
// symlink entry is detected and moved aside as-is rather than followed.
func replaceDirFileWise(srcDir, dstDir, asideDir string, undo *[]func()) error {
	moveAside := func(path, rel string) error {
		aside := filepath.Join(asideDir, rel)
		if err := os.MkdirAll(filepath.Dir(aside), 0o755); err != nil {
			return fmt.Errorf("mkdir aside for %s: %w", rel, err)
		}
		if err := os.Rename(path, aside); err != nil {
			return fmt.Errorf("move %s aside: %w", rel, err)
		}
		*undo = append(*undo, func() { _ = os.Rename(aside, path) })
		return nil
	}

	// Pass 1: quarantine stale files no longer present in the new bundle.
	if _, err := os.Lstat(dstDir); err == nil {
		if err := filepath.WalkDir(dstDir, func(dstPath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dstDir, dstPath)
			if err != nil {
				return err
			}
			if _, statErr := os.Lstat(filepath.Join(srcDir, rel)); os.IsNotExist(statErr) {
				return moveAside(dstPath, rel)
			}
			return nil
		}); err != nil {
			return err
		}
	}

	// Pass 2: move every new file into place, quarantining what it replaces.
	return filepath.WalkDir(srcDir, func(srcPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, srcPath)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, rel)

		if d.IsDir() {
			// Mirror the directory; never move directories wholesale.
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", rel, err)
			}
			return nil
		}

		// Move any existing old file aside before dropping the new one in.
		if _, statErr := os.Lstat(dst); statErr == nil {
			if err := moveAside(dst, rel); err != nil {
				return err
			}
		}

		if err := os.Rename(srcPath, dst); err != nil {
			return fmt.Errorf("move new %s into place: %w", rel, err)
		}
		*undo = append(*undo, func() { _ = os.Remove(dst) })
		return nil
	})
}

// extractUpdateArchive dispatches to the right extractor based on the
// staging path extension. Top-level entries land flat under destDir
// (matching the layout the release.yml packaging step produces).
// Reuses the zip/tar.gz extractors in bundled_git.go which already
// guard against zip-slip / tar-slip escapes.
func extractUpdateArchive(stagingPath, destDir string) error {
	f, err := os.Open(stagingPath)
	if err != nil {
		return fmt.Errorf("open staging file: %w", err)
	}
	defer f.Close()

	if strings.HasSuffix(stagingPath, ".zip") {
		st, err := f.Stat()
		if err != nil {
			return fmt.Errorf("stat staging file: %w", err)
		}
		return extractZipFromReader(f, st.Size(), destDir)
	}
	// .tar.gz (Linux). macOS goes through installDarwin which has its
	// own extractor specialised for the .app bundle layout.
	return extractTarGzFromReader(f, destDir)
}

// cleanLeakedUpdateRemnants removes stale .yansu-old files and
// .yansu-update-* staging dirs left behind by prior failed/restarted
// updates. Safe to call from app startup or before a new update extracts.
func cleanLeakedUpdateRemnants(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		// Matches both the legacy <name>.yansu-old siblings and the current
		// per-update .yansu-old-* quarantine dirs, plus .yansu-update-* extract
		// staging dirs.
		if strings.HasSuffix(name, ".yansu-old") || strings.HasPrefix(name, ".yansu-old") || strings.HasPrefix(name, ".yansu-update-") {
			_ = os.RemoveAll(filepath.Join(dir, name))
		}
	}
}

// ── Version comparison ──────────────────────────────────────────────

// isNewer reports whether remote is a higher semver than local.
func isNewer(remote, local string) bool {
	if _, ok := parseUpdateVersion(remote); !ok {
		return false
	}
	if updateChannel() != "beta" && semver.Prerelease("v"+strings.TrimPrefix(remote, "v")) != "" {
		return false
	}
	if _, ok := parseUpdateVersion(local); !ok {
		return false
	}
	return semver.Compare("v"+strings.TrimPrefix(remote, "v"), "v"+strings.TrimPrefix(local, "v")) > 0
}

// Require three numeric components and a valid semver prerelease.
func parseUpdateVersion(v string) ([3]uint64, bool) {
	var result [3]uint64
	v = strings.TrimPrefix(v, "v")
	if !semver.IsValid("v"+v) || semver.Build("v"+v) != "" {
		return result, false
	}
	parts := strings.Split(strings.SplitN(v, "-", 2)[0], ".")
	if len(parts) != 3 {
		return result, false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return result, false
			}
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, false
		}
		result[i] = n
	}
	return result, true
}

func validUpdateChecksum(checksum string) bool {
	digest, err := hex.DecodeString(checksum)
	return err == nil && len(digest) == sha256.Size
}

// GitHub fallback releases must publish a checksum file with the exact asset
// name. Missing, malformed or conflicting entries never authorize a download.
func fetchGitHubAssetChecksum(client *http.Client, assets []githubAsset, assetName string) (string, error) {
	checksumURL := ""
	for _, asset := range assets {
		if asset.Name == "SHA256SUMS" || asset.Name == "SHA256SUMS.txt" || asset.Name == "checksums.txt" {
			checksumURL = asset.BrowserDownloadURL
			break
		}
	}
	if checksumURL == "" {
		return "", fmt.Errorf("release has no checksum file")
	}
	resp, err := client.Get(checksumURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 1<<20 {
		return "", fmt.Errorf("checksum file is too large")
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	found := ""
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != assetName {
			continue
		}
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != sha256.Size {
			return "", fmt.Errorf("invalid asset checksum")
		}
		checksum := strings.ToLower(fields[0])
		if found != "" && found != checksum {
			return "", fmt.Errorf("conflicting asset checksums")
		}
		found = checksum
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("asset missing from checksum file")
	}
	return found, nil
}
