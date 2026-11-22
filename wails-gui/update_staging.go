package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Two archive slots hold the update. The staged one is named by pending.json;
// a newer release downloads into the other slot. pending.json is rewritten
// only after the new archive is verified, so a crash leaves exactly one
// committed archive, and the other slot is an orphan that startup removes.
func updateSlotPaths(stagingDir string) (primary, next string) {
	primary = filepath.Join(stagingDir, platformAssetName())
	return primary, primary + ".next"
}

// otherUpdateSlot returns the slot that is not current.
func otherUpdateSlot(stagingDir, current string) string {
	primary, next := updateSlotPaths(stagingDir)
	if current == primary {
		return next
	}
	return primary
}

// downloadWithRetries runs the retry loop shared by the first download and by a
// newer download. Each attempt verifies the bytes before it returns nil.
func (a *App) downloadWithRetries(client *http.Client, url, path string) error {
	var lastErr error
	for attempt := 1; attempt <= downloadMaxRetries; attempt++ {
		lastErr = a.downloadAttempt(client, url, path, attempt)
		if lastErr == nil {
			return nil
		}
		log.Printf("[update] download attempt %d/%d failed: %v", attempt, downloadMaxRetries, lastErr)
		if attempt < downloadMaxRetries {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}
	return lastErr
}

//isoform:destination metadata
func newUpdateDownloadClient() *http.Client {
	transport := &http.Transport{
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	return &http.Client{Transport: transport}
}

// checkNewerThanStaged looks for a release newer than the staged one. It never
// touches the staged archive or its status. A newer release is only recorded as
// the target; downloadUpdate fetches it into the other slot.
func (a *App) checkNewerThanStaged() {
	if !isReleaseChannel() {
		return
	}
	info := a.fetchLatestRelease()
	a.recordUpdateCheck(info)
	if info.Error != "" || !info.Available {
		return
	}
	a.updater.mu.Lock()
	defer a.updater.mu.Unlock()
	if !isNewer(info.Latest, a.updater.downloadedVersion) || info.Latest == a.updater.blockedVersion {
		return
	}
	a.setTargetLocked(info)
}

// checkAfterRecovery is the scheduled check after a failed install. It stays
// quiet: the rollback message keeps showing until something changes. The
// failed version is never downloaded again in the background.
func (a *App) checkAfterRecovery() {
	a.updater.mu.Lock()
	a.updater.advanceRecoveryBackoff(time.Now())
	a.updater.mu.Unlock()
	if !isReleaseChannel() {
		return
	}
	info := a.fetchLatestRelease()
	a.recordUpdateCheck(info)
	if info.Error != "" || !info.Available {
		return
	}
	a.updater.mu.Lock()
	defer a.updater.mu.Unlock()
	if a.updater.blockedVersion == "" {
		// Records from older helpers name no version. The release offered when the
		// install failed is the one being blocked; later releases are not.
		a.updater.blockedVersion = info.Latest
		if err := saveBlockedVersion(info.Latest); err != nil {
			log.Printf("[update] save blocked version: %v", err)
		}
	}
	if info.Latest == a.updater.blockedVersion {
		return
	}
	a.setTargetLocked(info)
}

// setTargetLocked records the release downloadUpdate should fetch. Caller holds u.mu.
func (a *App) setTargetLocked(info UpdateInfo) {
	a.updater.downloadURL = info.DownloadURL
	a.updater.targetVersion = info.Latest
	a.updater.targetNotes = info.ReleaseNotes
	a.updater.targetReleasedAt = info.ReleasedAt
}

// downloadNewerStaged fetches a newer release while an older verified one stays
// staged. downloadAttempt commits the new archive (pending.json) only after its
// checksum passes, so the staged archive is removed only after that commit.
// Caller holds opMu.
func (a *App) downloadNewerStaged(url, version string) error {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return fmt.Errorf("cannot locate data dir: %w", err)
	}
	stagingDir := filepath.Join(dataRoot, "updates")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	a.updater.mu.Lock()
	current := a.updater.stagingPath
	stagedVersion := a.updater.downloadedVersion
	a.updater.mu.Unlock()

	slot := otherUpdateSlot(stagingDir, current)
	// The free slot is never the staged archive, so an old partial there is safe to drop.
	_ = os.Remove(slot)
	a.updater.mu.Lock()
	a.updater.quietDownload = true
	a.updater.mu.Unlock()
	err = a.downloadWithRetries(newUpdateDownloadClient(), url, slot)
	a.updater.mu.Lock()
	a.updater.quietDownload = false
	a.updater.mu.Unlock()
	if err != nil {
		_ = os.Remove(slot)
		log.Printf("[update] newer release %s not staged; %s stays staged: %v", version, stagedVersion, err)
		// Progress events during the attempt replaced the card; restore it.
		a.updater.mu.Lock()
		a.updater.downloadURL = ""
		a.updater.targetVersion = ""
		a.updater.mu.Unlock()
		a.broadcastUpdateStatus(UpdateStatus{Status: "update-downloaded", Version: stagedVersion})
		return err
	}
	a.retireStagedArchive(current, slot)
	a.recordStagedUpdate()
	a.queuePrepareDownloadedUpdate()
	return nil
}

// retireStagedArchive removes the archive a newer one replaced, and the
// prepared app built from it. Runs only after the new archive is committed.
func (a *App) retireStagedArchive(old, now string) {
	if old == "" || old == now {
		return
	}
	_ = os.Remove(old)
	a.updater.mu.Lock()
	prepared := a.updater.prepared
	a.updater.prepared = nil
	a.updater.mu.Unlock()
	discardPreparedUpdate(prepared)
	log.Printf("[update] replaced staged archive with a newer one")
}

// recordStagedUpdate runs after any verified archive has become the staged one.
func (a *App) recordStagedUpdate() {
	a.updater.mu.Lock()
	version := a.updater.downloadedVersion
	a.updater.recoveryError = false
	a.updater.mu.Unlock()
	a.recordUpdateStaged(version)
}
