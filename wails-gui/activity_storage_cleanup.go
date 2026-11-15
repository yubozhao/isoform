package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Settings > Storage: what the activity data takes on disk, the storage
// limit, how long screenshot images are kept, and a cleanup the user starts.
// The cleanup first reports exactly what it would remove or convert and how
// much space that frees (PlanActivityStorageCleanup, which changes nothing),
// and only runs when the user confirms (StartActivityStorageCleanup). It does
// what the rotator does over time, all at once and to the end:
//   - removes screenshot and recording files no activity refers to;
//   - drops tables of removed features still in the database, clears the
//     retired Agency planning runs (activity_storage_retired_runs.go) and asks for
//     a VACUUM, which runs the next time the screen is locked;
//   - removes images older than the screenshot time limit, then the oldest
//     images and recordings while the total is over the storage limit (rows
//     and text stay);
//   - shrinks cold screenshots and compresses transcribed call recordings.

// ── Saved screenshot time limit ──

// readActivityConfigFile returns the saved storage settings as they are in
// activity-config.json (zero value when missing or unreadable).
func readActivityConfigFile() activityRetentionConfig {
	var cfg activityRetentionConfig
	path := activityConfigPath()
	if path == "" {
		return cfg
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	return cfg
}

// updateActivityConfigFile changes activity-config.json and keeps every
// setting change does not touch.
func updateActivityConfigFile(change func(*activityRetentionConfig)) error {
	path := activityConfigPath()
	if path == "" {
		return fmt.Errorf("cannot determine config path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	cfg := readActivityConfigFile()
	change(&cfg)
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// readSavedScreenshotDays returns the saved screenshot time limit, nil when
// none was saved.
func readSavedScreenshotDays() *int {
	cfg := readActivityConfigFile()
	if cfg.ScreenshotDays == nil || *cfg.ScreenshotDays < 0 {
		return nil
	}
	return cfg.ScreenshotDays
}

// savedStorageLimitDecision reads whether the user said yes to removals
// under the real-disk storage limit, and whether it was decided at all.
func savedStorageLimitDecision() (confirmed, decided bool) {
	cfg := readActivityConfigFile()
	if cfg.StorageLimitConfirmed == nil {
		return false, false
	}
	return *cfg.StorageLimitConfirmed, true
}

// saveStorageLimitConfirmed records the yes.
func saveStorageLimitConfirmed() {
	if err := updateActivityConfigFile(func(cfg *activityRetentionConfig) {
		yes := true
		cfg.StorageLimitConfirmed = &yes
	}); err != nil {
		log.Printf("[activity-storage] record storage limit confirmation: %v", err)
	}
}

// savedScreenshotRetention is the rotator's screenshot time limit. Without a
// saved value images go with their sessions, so it is off.
func savedScreenshotRetention() time.Duration {
	days := readSavedScreenshotDays()
	if days == nil || *days <= 0 {
		return 0
	}
	return time.Duration(*days) * 24 * time.Hour
}

// GetScreenshotRetentionDays returns how many days screenshot images are
// kept, or -1 when they go with their recordings: no screenshot time chosen,
// or one that is not shorter than the recording time limit (the whole
// recording, its text included, is removed at that limit first).
func (a *App) GetScreenshotRetentionDays() int {
	recordingDays, _ := readActivityRetentionConfig()
	return effectiveScreenshotChoice(readSavedScreenshotDays(), recordingDays)
}

// effectiveScreenshotChoice is the screenshot time limit that applies: the
// saved days when they are shorter than the recording time limit, else -1
// (with recordings).
func effectiveScreenshotChoice(saved *int, recordingDays int) int {
	if saved == nil || *saved <= 0 || (recordingDays > 0 && *saved >= recordingDays) {
		return -1
	}
	return *saved
}

// SetScreenshotRetentionDays saves how many days screenshot images are kept
// and starts a rotation pass. Older images are removed; their activity rows
// and text stay. -1, 0, or days not shorter than the recording time limit
// clear the choice: images go with their recordings.
func (a *App) SetScreenshotRetentionDays(days int) error {
	if days < -1 {
		return fmt.Errorf("screenshot days must be -1 (with recordings) or more")
	}
	recordingDays, _ := readActivityRetentionConfig()
	if err := updateActivityConfigFile(func(cfg *activityRetentionConfig) {
		// A time not shorter than the recording limit would never apply:
		// the recording, its text included, goes at that limit first.
		if effectiveScreenshotChoice(&days, recordingDays) < 0 {
			cfg.ScreenshotDays = nil
			return
		}
		cfg.ScreenshotDays = &days
	}); err != nil {
		return err
	}
	if a.activity != nil && a.activity.rotator != nil {
		go a.activity.rotator.TriggerOnce()
	}
	return nil
}

// GetActivityStorageBudget reports where the activity data stands against
// the storage limit: within it, over it and waiting for the user's yes, or
// over it with nothing left the limit may remove.
func (a *App) GetActivityStorageBudget() (perfValue StorageBudgetStatus) {
	defer logPageBinding("GetActivityStorageBudget", time.Now(), &perfValue)
	if a.activity == nil || a.activity.rotator == nil {
		return StorageBudgetStatus{State: budgetStateOK}
	}
	return a.activity.rotator.BudgetStatus()
}

// ConfirmActivityStorageLimit is the user's yes on the Storage page: from
// now on the oldest screenshots and recordings over the limit are removed.
// It starts a rotation pass.
func (a *App) ConfirmActivityStorageLimit() error {
	if a.activity == nil || a.activity.rotator == nil {
		return fmt.Errorf("activity monitor not initialized")
	}
	a.activity.rotator.confirmLimit()
	go a.activity.rotator.TriggerOnce()
	return nil
}

// GetActivityStorageUsage returns what the activity data takes on disk by
// kind, measured from the files (at most a minute old).
func (a *App) GetActivityStorageUsage() (ActivityStorageUsage, error) {
	if a.activity == nil || a.activity.rotator == nil {
		return ActivityStorageUsage{}, nil
	}
	return a.activity.rotator.Usage(time.Minute), nil
}

// ── Cleanup plan ──

// StorageCleanupItem is one line of the cleanup report.
type StorageCleanupItem struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
	// AfterBytes is the expected size after a conversion; 0 for removals.
	AfterBytes int64 `json:"afterBytes"`
	// OldestMs and NewestMs bound the files by capture time (file time for
	// files no activity refers to); 0 when Count is 0.
	OldestMs int64 `json:"oldestMs"`
	NewestMs int64 `json:"newestMs"`
}

func (it *StorageCleanupItem) add(bytes, after, atMs int64) {
	it.Count++
	it.Bytes += bytes
	it.AfterBytes += after
	if atMs > 0 && (it.OldestMs == 0 || atMs < it.OldestMs) {
		it.OldestMs = atMs
	}
	if atMs > it.NewestMs {
		it.NewestMs = atMs
	}
}

// freed is what the item frees: all of it for a removal, the difference for
// a conversion.
func (it StorageCleanupItem) freed(conversion bool) int64 {
	if conversion {
		return it.Bytes - it.AfterBytes
	}
	return it.Bytes
}

// StorageCleanupPlan is the cleanup report: everything the cleanup would
// remove or convert, with counts and bytes. Building it changes nothing.
type StorageCleanupPlan struct {
	CreatedAtMs    int64                `json:"createdAtMs"`
	LimitBytes     int64                `json:"limitBytes"`
	ScreenshotDays int                  `json:"screenshotDays"`
	Usage          ActivityStorageUsage `json:"usage"`
	// Files no activity refers to.
	UnlinkedScreenshots StorageCleanupItem `json:"unlinkedScreenshots"`
	UnlinkedRecordings  StorageCleanupItem `json:"unlinkedRecordings"`
	// Sessions older than the recording time limit ("Keep recordings for"),
	// removed whole with their text: Count is sessions, Bytes their files.
	ExpiredSessions StorageCleanupItem `json:"expiredSessions"`
	RetentionDays   int                `json:"retentionDays"`
	// Images older than the screenshot time limit (text kept).
	ExpiredScreenshots StorageCleanupItem `json:"expiredScreenshots"`
	// The oldest images and recordings over the storage limit (text kept).
	OverLimitScreenshots StorageCleanupItem `json:"overLimitScreenshots"`
	OverLimitRecordings  StorageCleanupItem `json:"overLimitRecordings"`
	// Conversions.
	ShrinkScreenshots  StorageCleanupItem `json:"shrinkScreenshots"`
	CompressRecordings StorageCleanupItem `json:"compressRecordings"`
	// Database: Count is the number of retired tables; Bytes what the next
	// VACUUM gives back (free pages plus those tables), which comes back only
	// when the computer is next locked, with enough free disk, at most every
	// 12 hours; it is not part of FreedBytes.
	DatabaseSpace StorageCleanupItem `json:"databaseSpace"`
	// RecordingsNeedFFmpeg is set when recordings could be compressed but
	// ffmpeg is not installed.
	RecordingsNeedFFmpeg bool `json:"recordingsNeedFFmpeg"`
	// LimitUnreachable is set when the storage limit cannot be reached even
	// with every file it may take removed; KeptBytes is what stays. Nothing
	// is removed for the limit then.
	LimitUnreachable bool  `json:"limitUnreachable"`
	KeptBytes        int64 `json:"keptBytes"`
	// FreedBytes is what the cleanup frees while it runs; AfterBytes the
	// total then.
	FreedBytes int64 `json:"freedBytes"`
	AfterBytes int64 `json:"afterBytes"`
}

// snapshotFileInfo is one file in a screenshot date folder.
type snapshotFileInfo struct {
	size  int64
	mtime time.Time
}

// listSnapshotFiles lists the regular files in the screenshot date folders
// by path relative to the screenshots folder.
func listSnapshotFiles(snapshotsDir string) map[string]snapshotFileInfo {
	files := make(map[string]snapshotFileInfo)
	days, err := os.ReadDir(snapshotsDir)
	if err != nil {
		return files
	}
	for _, d := range days {
		if !d.IsDir() || !isSnapshotDateDir(d.Name()) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(snapshotsDir, d.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			files[filepath.Join(d.Name(), e.Name())] = snapshotFileInfo{size: info.Size(), mtime: info.ModTime()}
		}
	}
	return files
}

// PlanCleanup builds the cleanup report for the current settings.
func (r *StorageRotator) PlanCleanup() (StorageCleanupPlan, error) {
	r.lockForUser()
	defer r.runMu.Unlock()
	cfg := r.snapshotConfig()
	now := time.Now()
	plan := StorageCleanupPlan{
		CreatedAtMs:    now.UnixMilli(),
		LimitBytes:     cfg.MaxTotalSize,
		ScreenshotDays: int(cfg.ScreenshotRetention / (24 * time.Hour)),
		Usage:          r.measure(),
	}
	coldBefore := now.Add(-cfg.WarmRetention).UnixMilli()

	// Screenshot files no row refers to (the reconcile's rule).
	files := listSnapshotFiles(r.store.SnapshotsDir())
	referenced, err := r.store.referencedSnapshotPaths()
	if err != nil {
		return plan, err
	}
	orphanBefore := now.Add(-orphanSnapshotMinAge)
	for rel, f := range files {
		isTemp := strings.HasSuffix(rel, ".jpg.tmp")
		if !isTemp && (!strings.HasSuffix(rel, ".jpg") || referenced[rel]) {
			continue
		}
		if !f.mtime.Before(orphanBefore) {
			continue
		}
		plan.UnlinkedScreenshots.add(f.size, 0, f.mtime.UnixMilli())
	}

	// Recording files no row refers to.
	orphanAudio, _, err := r.orphanAudioFiles(now, orphanAudioMinAge)
	if err != nil {
		return plan, err
	}
	for _, rel := range orphanAudio {
		if info, err := os.Stat(filepath.Join(r.store.AudioDir(), rel)); err == nil {
			plan.UnlinkedRecordings.add(info.Size(), 0, info.ModTime().UnixMilli())
		}
	}

	// Database space the cleanup gives back through VACUUM: free pages,
	// retired tables it drops and the retired Agency runs it clears. Shown
	// on its own line; it counts toward nothing until a VACUUM has run.
	tables, retiredBytes, err := r.store.retiredTablesPresent()
	if err != nil {
		return plan, err
	}
	free, _, err := r.store.databaseFreeBytes()
	if err != nil {
		return plan, err
	}
	plan.DatabaseSpace = StorageCleanupItem{Count: len(tables), Bytes: free + retiredBytes + r.store.agencyRunBytes()}

	// Starting point for the limit: the total once what is planned so far
	// and the pending shrinking are done. Usage already leaves out free
	// pages; the retired tables and Agency runs this clears become free
	// pages too, exactly as the automatic pass clears them before it
	// enforces the limit.
	projected := r.projectedUsage(cfg, plan.Usage, now) -
		plan.UnlinkedScreenshots.Bytes - plan.UnlinkedRecordings.Bytes -
		(plan.DatabaseSpace.Bytes - free)

	// Rows whose image the plan removes, so they are not also shrunk.
	removed := make(map[int64]bool)
	removedAudio := make(map[string]bool)
	planRemove := func(item *StorageCleanupItem, m snapshotMedia) {
		f, ok := files[m.RelPath]
		if !ok || removed[m.ID] {
			return // no file: the reconcile only marks the row
		}
		item.add(f.size, 0, m.Timestamp)
		removed[m.ID] = true
		projected -= snapshotProjectedSize(m, coldBefore)
	}

	// Sessions past the recording time limit go whole, text included.
	var sessionsFrom int64
	if cfg.ColdRetention > 0 {
		sessionsFrom = now.Add(-cfg.ColdRetention).UnixMilli()
		plan.RetentionDays = int(cfg.ColdRetention / (24 * time.Hour))
		expired, err := r.store.GetSessionsOlderThan(sessionsFrom)
		if err != nil {
			return plan, err
		}
		for _, sess := range expired {
			var bytes int64
			snaps, _ := r.store.GetSessionSnapshotsIncludingProtected(sess.ID)
			for _, sn := range snaps {
				if f, ok := files[sn.FilePath]; ok && sn.StorageTier != snapshotTierMissing {
					bytes += f.size
					removed[sn.ID] = true
					projected -= snapshotProjectedSize(snapshotMedia{
						ID: sn.ID, Timestamp: sn.Timestamp, RelPath: sn.FilePath, Size: sn.SizeBytes,
						Width: sn.Width, Height: sn.Height, Tier: sn.StorageTier, OCRStatus: sess.OCRStatus,
					}, coldBefore)
				}
			}
			if sess.AudioPath != "" {
				if info, err := os.Stat(filepath.Join(r.store.AudioDir(), sess.AudioPath)); err == nil {
					bytes += info.Size()
					projected -= r.audioProjectedSize(audioMedia{RelPath: sess.AudioPath, Size: info.Size(), Transcribed: sess.TranscriptPath != ""})
				}
				removedAudio[sess.ID] = true
			}
			plan.ExpiredSessions.add(bytes, 0, sess.StartTime)
		}
	}

	cutoff := screenshotCutoffMs(cfg, now)
	if cutoff > 0 {
		cursorTs, cursorID := int64(-1), int64(0)
		for {
			page, err := r.store.snapshotsRemovableBefore(cutoff, cursorTs, cursorID, 1000)
			if err != nil {
				return plan, err
			}
			if len(page) == 0 {
				break
			}
			for _, m := range page {
				planRemove(&plan.ExpiredScreenshots, m)
				cursorTs, cursorID = m.Timestamp, m.ID
			}
		}
	}

	snaps, audio, reduced, reachable, err := r.budgetVictimsAfter(cfg, projected, cutoff, sessionsFrom, now)
	if err != nil {
		return plan, err
	}
	if !reachable {
		plan.LimitUnreachable = true
		plan.KeptBytes = projected - reduced
	}
	for _, m := range snaps {
		planRemove(&plan.OverLimitScreenshots, m)
	}
	for _, m := range audio {
		if info, err := os.Stat(filepath.Join(r.store.AudioDir(), m.RelPath)); err == nil {
			plan.OverLimitRecordings.add(info.Size(), 0, m.StartTime)
		}
		removedAudio[m.SessionID] = true
	}

	// Screenshots to shrink: every compactable row the plan keeps.
	if err := r.forEachCompactable(coldBefore, func(m snapshotMedia) {
		if removed[m.ID] {
			return
		}
		f, ok := files[m.RelPath]
		if !ok {
			return
		}
		m.Size = f.size
		plan.ShrinkScreenshots.add(f.size, snapshotCompactedSize(m), m.Timestamp)
	}); err != nil {
		return plan, err
	}

	// Recordings to compress.
	recordings, err := r.store.sessionsWithAudio(math.MaxInt64)
	if err != nil {
		return plan, err
	}
	for _, m := range recordings {
		if removedAudio[m.SessionID] || !r.audioCompressible(m) {
			continue
		}
		info, err := os.Stat(filepath.Join(r.store.AudioDir(), m.RelPath))
		if err != nil {
			continue
		}
		m.Size = info.Size()
		plan.CompressRecordings.add(m.Size, r.audioProjectedSize(m), m.StartTime)
	}
	if plan.CompressRecordings.Count > 0 && r.ffmpegPath() == "" {
		plan.RecordingsNeedFFmpeg = true
		plan.CompressRecordings = StorageCleanupItem{}
	}

	plan.FreedBytes = plan.UnlinkedScreenshots.freed(false) + plan.UnlinkedRecordings.freed(false) +
		plan.ExpiredSessions.freed(false) + plan.ExpiredScreenshots.freed(false) +
		plan.OverLimitScreenshots.freed(false) + plan.OverLimitRecordings.freed(false) +
		plan.ShrinkScreenshots.freed(true) + plan.CompressRecordings.freed(true)
	plan.AfterBytes = max(plan.Usage.TotalBytes-plan.FreedBytes, 0)
	return plan, nil
}

// forEachCompactable calls fn for every compactable screenshot, newest
// first.
func (r *StorageRotator) forEachCompactable(coldBeforeMs int64, fn func(snapshotMedia)) error {
	rows, err := r.store.db.Query(`
		SELECT `+snapshotMediaColumns+`
		FROM activity_snapshots sn
		LEFT JOIN activity_sessions s ON s.id = sn.session_id
		WHERE `+compactableSnapshotWhere+`
		ORDER BY sn.timestamp DESC, sn.id DESC`, coldBeforeMs, snapshotCompactMaxPixels)
	if err != nil {
		return fmt.Errorf("query compactable snapshots: %w", err)
	}
	all, err := scanSnapshotMedia(rows)
	if err != nil {
		return err
	}
	for _, m := range all {
		fn(m)
	}
	return nil
}

// ── Running the cleanup ──

// StorageCleanupStatus reports a cleanup the user started.
type StorageCleanupStatus struct {
	Running bool `json:"running"`
	// Phase: "unlinked", "database", "expired", "limit", "shrink",
	// "compress", "done".
	Phase        string `json:"phase"`
	Done         int    `json:"done"`
	Total        int    `json:"total"`
	StartedAtMs  int64  `json:"startedAtMs"`
	FinishedAtMs int64  `json:"finishedAtMs"`
	BeforeBytes  int64  `json:"beforeBytes"`
	AfterBytes   int64  `json:"afterBytes"`
	FreedBytes   int64  `json:"freedBytes"`
	// DatabasePending is set when database space is given back by a VACUUM
	// that waits for the screen to lock.
	DatabasePending bool   `json:"databasePending"`
	Error           string `json:"error,omitempty"`
}

func (r *StorageRotator) cleanupStatus() StorageCleanupStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cleanup
}

func (r *StorageRotator) setCleanupProgress(phase string, done, total int) {
	r.mu.Lock()
	r.cleanup.Phase = phase
	r.cleanup.Done = done
	r.cleanup.Total = total
	r.mu.Unlock()
}

// StartCleanup runs the cleanup in the background. It refuses while one is
// running.
func (r *StorageRotator) StartCleanup() error {
	r.mu.Lock()
	if r.cleanup.Running {
		r.mu.Unlock()
		return fmt.Errorf("a cleanup is already running")
	}
	r.cleanup = StorageCleanupStatus{Running: true, Phase: "unlinked", StartedAtMs: time.Now().UnixMilli()}
	r.mu.Unlock()
	go r.runCleanup()
	return nil
}

// runCleanup does everything PlanCleanup reports, in the same order.
func (r *StorageRotator) runCleanup() {
	r.runCleanupWithMediaPriority(r.mediaPriority)
}

// runCleanupWithMediaPriority lets tests run the real media work at caller
// priority, independent of the OS background scheduler under machine load.
// runMedia must finish the supplied work before returning.
func (r *StorageRotator) runCleanupWithMediaPriority(runMedia func(func())) {
	r.lockForUser()
	defer r.runMu.Unlock()
	cfg := r.snapshotConfig()
	now := time.Now()
	before := r.measure()
	r.mu.Lock()
	r.cleanup.BeforeBytes = before.TotalBytes
	r.mu.Unlock()

	r.setCleanupProgress("unlinked", 0, 0)
	r.sweepOrphanSnapshots(now, true)
	r.sweepOrphanAudio(now)

	r.setCleanupProgress("database", 0, 0)
	r.tidyRetiredDatabase()
	r.slimRetiredAgencyRuns(time.Now().Add(100*365*24*time.Hour), 0, nil)
	if free, _, err := r.store.databaseFreeBytes(); err == nil && free > 0 {
		r.RequestVacuum()
	}

	r.rotateHotToWarm(cfg)
	r.rotateWarmToCold(cfg)
	r.rotateSnapshotTiers(cfg)

	r.setCleanupProgress("expired", 0, 0)
	r.purgeExpiredSessions(cfg)
	r.expireScreenshots(cfg)

	// The user started this after seeing what goes: that is their yes.
	r.setCleanupProgress("limit", 0, 0)
	r.enforceBudget(cfg, r.measure(), screenshotCutoffMs(cfg, now), true)

	coldBefore := time.Now().Add(-cfg.WarmRetention).UnixMilli()
	toShrink, _, _, _ := r.store.compactableSnapshotTotals(coldBefore)
	r.setCleanupProgress("shrink", 0, toShrink)
	noDeadline := time.Now().Add(100 * 365 * 24 * time.Hour)
	runMedia(func() {
		r.compactColdSnapshots(cfg, noDeadline, 0, nil, func(done int) { r.setCleanupProgress("shrink", done, toShrink) })
	})

	toCompress := 0
	if sessions, err := r.store.sessionsWithAudio(math.MaxInt64); err == nil {
		for _, m := range sessions {
			if r.audioCompressible(m) {
				toCompress++
			}
		}
	}
	r.setCleanupProgress("compress", 0, toCompress)
	runMedia(func() {
		r.compressCallAudio(noDeadline, nil, func(done int) { r.setCleanupProgress("compress", done, toCompress) })
	})

	r.checkpointWAL()
	after := r.measure()
	r.mu.Lock()
	r.cleanup.Running = false
	r.cleanup.Phase = "done"
	r.cleanup.FinishedAtMs = time.Now().UnixMilli()
	r.cleanup.AfterBytes = after.TotalBytes
	r.cleanup.FreedBytes = max(before.TotalBytes-after.TotalBytes, 0)
	r.cleanup.DatabasePending = r.vacuumRequested
	r.mu.Unlock()
	log.Printf("[activity-storage] cleanup done: %d -> %d bytes", before.TotalBytes, after.TotalBytes)
}

// ── Bindings ──

// PlanActivityStorageCleanup reports what the storage cleanup would remove
// or convert under the current settings and how much space that frees. It
// changes nothing.
func (a *App) PlanActivityStorageCleanup() (StorageCleanupPlan, error) {
	if a.activity == nil || a.activity.rotator == nil {
		return StorageCleanupPlan{}, fmt.Errorf("activity monitor not initialized")
	}
	return a.activity.rotator.PlanCleanup()
}

// StartActivityStorageCleanup runs the storage cleanup in the background;
// GetActivityStorageCleanupStatus reports its progress.
func (a *App) StartActivityStorageCleanup() error {
	if a.activity == nil || a.activity.rotator == nil {
		return fmt.Errorf("activity monitor not initialized")
	}
	return a.activity.rotator.StartCleanup()
}

// GetActivityStorageCleanupStatus reports the running or last cleanup.
func (a *App) GetActivityStorageCleanupStatus() StorageCleanupStatus {
	if a.activity == nil || a.activity.rotator == nil {
		return StorageCleanupStatus{}
	}
	return a.activity.rotator.cleanupStatus()
}
