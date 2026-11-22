package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wails-gui/internal/platform/activitymon"
)

// RotatorConfig controls the storage rotation policy.
//
// ColdRetention <= 0 means "never purge by age" (the user picked "Unlimited").
// MaxTotalSize still applies as a hard cap regardless of ColdRetention.
type RotatorConfig struct {
	HotRetention  time.Duration // how long segments stay in "hot" tier
	WarmRetention time.Duration // how long segments stay in "warm" tier; cold screenshots are compacted
	ColdRetention time.Duration // how long segments stay in "cold" tier before deletion (<=0 disables age purge)
	// ScreenshotRetention is how long screenshot images are kept; older ones
	// lose the image and keep their row and text. <=0 keeps them until the
	// storage limit needs the space.
	ScreenshotRetention time.Duration
	MaxTotalSize        int64         // limit on the real on-disk total (bytes), see activity_storage_budget.go
	TargetFreeSize      int64         // over the limit, images and recordings go until the total is at or below this
	CheckInterval       time.Duration // how often to run rotation
}

// DefaultRotatorConfig returns the default rotation configuration.
func DefaultRotatorConfig() RotatorConfig {
	return RotatorConfig{
		HotRetention:   24 * time.Hour,
		WarmRetention:  7 * 24 * time.Hour,
		ColdRetention:  30 * 24 * time.Hour,
		MaxTotalSize:   2 * 1024 * 1024 * 1024, // 2GB
		TargetFreeSize: 1843 * 1024 * 1024,     // 90%
		CheckInterval:  30 * time.Minute,
	}
}

// rotatorMediaBudget is how long one rotation pass may spend compacting
// screenshots and compressing call audio; a backlog continues on the next
// pass. rotatorMediaPause is slept between screenshots so the backlog does
// not hold a core. Passes in the first rotatorMediaMinUptime after launch
// skip this work.
const (
	rotatorMediaBudget    = 5 * time.Minute
	rotatorMediaPause     = 20 * time.Millisecond
	rotatorMediaMinUptime = 10 * time.Minute
)

// StorageRotator manages tiered storage rotation for activity data.
type StorageRotator struct {
	store *ActivityStore
	// onExpire hears each retention purge's threshold, so what was made
	// from the expired activity ages out with it (See them's entries).
	onExpire func(thresholdMs int64)

	mu              sync.RWMutex
	config          RotatorConfig
	usage           ActivityStorageUsage // last measurement, see Usage
	vacuumRequested bool
	cleanup         StorageCleanupStatus // the cleanup the user started, see StartCleanup
	budget          StorageBudgetStatus  // outcome of the last budget check, see enforceBudget

	// usersWaiting counts calls the user is waiting on (the cleanup report,
	// the cleanup) queued for runMu; a background pass stops its media work
	// early for them.
	usersWaiting atomic.Int32

	// runMu serializes rotation passes, the cleanup the user starts and
	// VACUUM. The fields below are only used while it is held.
	runMu             sync.Mutex
	compactSkip       map[int64]bool  // screenshots that could not be compacted in this process
	audioSkip         map[string]bool // recordings that could not be compressed in this process
	lastSnapshotSweep time.Time
	lastVacuum        time.Time

	// ffmpegPath finds the ffmpeg used for call audio; vacuumMoment says
	// whether a VACUUM may block the database now; screenshotRetention reads
	// the saved screenshot time limit when the config sets none. Tests
	// replace them.
	ffmpegPath          func() string
	vacuumMoment        func() bool
	screenshotRetention func() time.Duration
	// limitDecision reads whether the user confirmed removals under the
	// real-disk limit (and whether that was decided at all); confirmLimit
	// records the yes. See activity_storage_budget.go.
	limitDecision func() (confirmed, decided bool)
	confirmLimit  func()
	// recordingFinal reports whether a call recording's transcript is final,
	// so the recording may be compressed or removed by the limit.
	recordingFinal func(audioMedia) bool
	// framesInUse returns the screenshot files the capture still holds in
	// memory and whether a call is open; the App sets it. Nil means none.
	framesInUse func() (held map[string]bool, open bool)
	// mediaDeferReason says why media work should wait this pass (battery,
	// Low Power Mode); "" means go.
	mediaDeferReason func() string
	// mediaPriority applies the platform's background scheduling to media work.
	mediaPriority func(func())
}

// NewStorageRotator creates a new rotator for the given activity store.
func NewStorageRotator(store *ActivityStore, config RotatorConfig) *StorageRotator {
	return &StorageRotator{
		store:               store,
		config:              config,
		compactSkip:         make(map[int64]bool),
		audioSkip:           make(map[string]bool),
		ffmpegPath:          installedFFmpegPath,
		vacuumMoment:        activitymon.IsScreenLocked,
		screenshotRetention: savedScreenshotRetention,
		limitDecision:       savedStorageLimitDecision,
		confirmLimit:        saveStorageLimitConfirmed,
		recordingFinal:      recordingTranscriptFinal,
		mediaDeferReason:    storageMediaDeferReason,
		mediaPriority:       withBackgroundPriority,
		// The startup reconcile (reconcileSnapshotFiles) covers this run's
		// first day.
		lastSnapshotSweep: time.Now(),
	}
}

// lockForUser takes runMu for work the user is waiting on, asking a running
// background pass to stop its media work first.
func (r *StorageRotator) lockForUser() {
	r.usersWaiting.Add(1)
	r.runMu.Lock()
	r.usersWaiting.Add(-1)
}

// userWaiting reports whether the user is waiting for runMu.
func (r *StorageRotator) userWaiting() bool {
	return r.usersWaiting.Load() > 0
}

// Usage returns the last on-disk measurement, measuring again when it is
// older than maxAge.
func (r *StorageRotator) Usage(maxAge time.Duration) ActivityStorageUsage {
	r.mu.RLock()
	u := r.usage
	r.mu.RUnlock()
	if u.MeasuredAtMs > 0 && time.Since(time.UnixMilli(u.MeasuredAtMs)) <= maxAge {
		return u
	}
	return r.measure()
}

// measure walks the activity folders and caches the result.
func (r *StorageRotator) measure() ActivityStorageUsage {
	u := measureActivityStorage(r.store)
	r.mu.Lock()
	r.usage = u
	r.mu.Unlock()
	return u
}

// snapshotConfig returns a copy of the current config under read lock, with
// the saved screenshot time limit filled in when the config sets none.
func (r *StorageRotator) snapshotConfig() RotatorConfig {
	r.mu.RLock()
	cfg := r.config
	r.mu.RUnlock()
	if cfg.ScreenshotRetention <= 0 && r.screenshotRetention != nil {
		cfg.ScreenshotRetention = r.screenshotRetention()
	}
	return cfg
}

// UpdateConfig swaps the active rotation config. Subsequent rotate() calls
// will use the new values. The check ticker interval is not retroactively
// changed for an already-running Run loop; the new interval applies after
// the next restart.
func (r *StorageRotator) UpdateConfig(cfg RotatorConfig) {
	r.mu.Lock()
	r.config = cfg
	r.mu.Unlock()
}

// TriggerOnce runs a single rotation pass synchronously. Safe to call from
// any goroutine; concurrent calls serialize on runMu.
func (r *StorageRotator) TriggerOnce() {
	r.rotate()
}

// Run starts the rotation loop. Blocks until ctx is cancelled.
func (r *StorageRotator) Run(ctx context.Context) {
	// Run once at startup
	r.rotate()

	ticker := time.NewTicker(r.snapshotConfig().CheckInterval)
	defer ticker.Stop()
	vacuumTicker := time.NewTicker(vacuumCheckInterval)
	defer vacuumTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.rotate()
		case now := <-vacuumTicker.C:
			if err := r.store.db.Ping(); err == nil {
				r.maybeVacuum(now)
			}
		}
	}
}

func (r *StorageRotator) rotate() {
	// Skip if the database has been closed (e.g. during shutdown).
	if err := r.store.db.Ping(); err != nil {
		return
	}
	r.runMu.Lock()
	defer r.runMu.Unlock()
	cfg := r.snapshotConfig()
	now := time.Now()
	r.rotateHotToWarm(cfg)
	r.rotateWarmToCold(cfg)
	r.rotateSnapshotTiers(cfg)
	r.purgeExpiredSessions(cfg)
	r.expireScreenshots(cfg)
	r.sweepOrphanAudio(now)
	r.sweepOrphanSnapshots(now, false)
	// Tidy the database before the limit looks at it: dropped tables and
	// cleared Agency runs become free pages, which count as nothing. Until
	// the Agency runs are cleared the limit waits, so it never removes
	// screenshots to cover space this gives back.
	r.tidyRetiredDatabase()
	if ids, err := r.store.agencyRunsToSlim(); err == nil && len(ids) > 0 {
		r.slimRetiredAgencyRuns(time.Now().Add(rotatorMediaBudget), 50*time.Millisecond, r.userWaiting)
		if left, err := r.store.agencyRunsToSlim(); err != nil || len(left) > 0 {
			log.Printf("[activity-rotator] storage limit waits: retired Agency runs are still being cleared")
			r.checkpointWAL()
			return
		}
	}
	usage := r.measure()
	if cfg.MaxTotalSize > 0 && usage.TotalBytes > cfg.MaxTotalSize && now.Sub(r.lastSnapshotSweep) >= time.Hour {
		// Over the limit: sweep unreferenced frames first, as the cleanup
		// report does, so the limit takes no more than the report shows.
		r.sweepOrphanSnapshots(now, true)
		usage = r.measure()
	}
	r.enforceBudget(cfg, usage, screenshotCutoffMs(cfg, now), false)

	// Shrink what is kept: cold screenshots and transcribed call audio. Not in
	// the first minutes after launch,
	// when the app is busy starting up, and not on battery or in Low Power
	// Mode.
	if time.Since(processStartedAt) >= rotatorMediaMinUptime && (r.mediaDeferReason == nil || r.mediaDeferReason() == "") {
		deadline := time.Now().Add(rotatorMediaBudget)
		r.mediaPriority(func() {
			r.compactColdSnapshots(cfg, deadline, rotatorMediaPause, r.userWaiting, nil)
			r.compressCallAudio(deadline, r.userWaiting, nil)
		})
		r.measure()
	}

	// VACUUM never runs here: it owns the store's single connection for the
	// whole rewrite and once blocked startup recovery for minutes. The
	// rotation loop runs it while the screen is locked (maybeVacuum).
	r.checkpointWAL()
}

// screenshotCutoffMs is the time before which the screenshot time limit
// removes images (0 when there is none).
func screenshotCutoffMs(cfg RotatorConfig, now time.Time) int64 {
	if cfg.ScreenshotRetention <= 0 {
		return 0
	}
	return now.Add(-cfg.ScreenshotRetention).UnixMilli()
}

// checkpointWAL forces SQLite to fold the WAL file back into the main DB and
// truncate it. SQLite's auto-checkpoint at 1000 pages (~4 MB) was being
// starved on busy days, letting activity.db-wal grow past 100 MB and double
// the on-disk footprint. Running TRUNCATE once per rotation cycle keeps it
// bounded.
func (r *StorageRotator) checkpointWAL() {
	busy, logFrames, ckptFrames, err := r.store.CheckpointTruncate()
	if err != nil {
		log.Printf("[activity-rotator] wal checkpoint failed: %v", err)
		return
	}
	if busy != 0 {
		log.Printf(
			"[activity-rotator] wal checkpoint partial: readers held back full truncate (log_frames=%d ckpt_frames=%d)",
			logFrames, ckptFrames,
		)
	}
}

// rotateHotToWarm transitions segments older than HotRetention from "hot" to "warm".
func (r *StorageRotator) rotateHotToWarm(cfg RotatorConfig) {
	threshold := time.Now().Add(-cfg.HotRetention)
	segments, err := r.store.GetSegmentsByTier("hot")
	if err != nil {
		log.Printf("[activity-rotator] failed to get hot segments: %v", err)
		return
	}

	for _, seg := range segments {
		if time.UnixMilli(seg.StartTime).Before(threshold) {
			if err := r.store.UpdateSegmentTier(seg.ID, "warm"); err != nil {
				log.Printf("[activity-rotator] failed to update segment %d to warm: %v", seg.ID, err)
			}
		}
	}
}

// rotateWarmToCold transitions segments older than WarmRetention from "warm" to "cold".
func (r *StorageRotator) rotateWarmToCold(cfg RotatorConfig) {
	threshold := time.Now().Add(-cfg.WarmRetention)
	segments, err := r.store.GetSegmentsByTier("warm")
	if err != nil {
		log.Printf("[activity-rotator] failed to get warm segments: %v", err)
		return
	}

	for _, seg := range segments {
		if time.UnixMilli(seg.StartTime).Before(threshold) {
			if err := r.store.UpdateSegmentTier(seg.ID, "cold"); err != nil {
				log.Printf("[activity-rotator] failed to update segment %d to cold: %v", seg.ID, err)
			}
		}
	}
}

// rotateSnapshotTiers transitions snapshots between tiers (same thresholds as segments).
func (r *StorageRotator) rotateSnapshotTiers(cfg RotatorConfig) {
	hotThreshold := time.Now().Add(-cfg.HotRetention)
	warmThreshold := time.Now().Add(-cfg.WarmRetention)

	for _, tier := range []struct {
		from      string
		to        string
		threshold time.Time
	}{
		{"hot", "warm", hotThreshold},
		{"warm", "cold", warmThreshold},
	} {
		snapshots, err := r.store.GetSnapshotsByTier(tier.from)
		if err != nil {
			log.Printf("[activity-rotator] failed to get %s snapshots: %v", tier.from, err)
			continue
		}
		for _, snap := range snapshots {
			if time.UnixMilli(snap.Timestamp).Before(tier.threshold) {
				if err := r.store.UpdateSnapshotTier(snap.ID, tier.to); err != nil {
					log.Printf("[activity-rotator] failed to update snapshot %d to %s: %v", snap.ID, tier.to, err)
				}
			}
		}
	}
}

// purgeExpiredSessions deletes sessions whose start_time is older than
// ColdRetention.
//
// The previous implementation used GetSegmentsByTier("cold") as the entry
// point, which silently skipped the entire snapshot-only / event-only session
// population produced by the current activity-monitor pipeline (see
// activity_monitor.go: the main path inserts activity_sessions + snapshots
// without ever creating an activity_segments row, so that pipeline never had
// any cold segments for the rotator to find). The setting was therefore a
// no-op for the vast majority of real sessions. We now scan
// activity_sessions directly so the user-configured retention applies to
// every session regardless of which pipeline produced it.
//
// When ColdRetention <= 0 (the user picked "Unlimited"), age-based purge is
// skipped entirely — only the quota enforcer can reclaim space.
func (r *StorageRotator) purgeExpiredSessions(cfg RotatorConfig) {
	if cfg.ColdRetention <= 0 {
		return
	}
	thresholdMs := time.Now().Add(-cfg.ColdRetention).UnixMilli()

	// Filter at the SQL layer rather than loading every activity_sessions row
	// into Go memory and discarding the bulk of it. On large activity DBs
	// (months of recordings) the previous approach allocated thousands of
	// session rows just to drop most of them on the floor.
	sessions, err := r.store.GetSessionsOlderThan(thresholdMs)
	if err != nil {
		log.Printf("[activity-rotator] failed to get expired sessions: %v", err)
		return
	}

	for _, s := range sessions {
		r.deleteSessionAndFiles(s.ID)
	}

	// Runs expire independently of a failed session deletion. Their purge
	// atomically protects any live members before discarding private summaries.
	if err := r.store.purgeTimelineRunsOlderThan(thresholdMs); err != nil {
		log.Printf("[activity-rotator] failed to purge expired timeline runs: %v", err)
	}
	if r.onExpire != nil {
		r.onExpire(thresholdMs)
	}
}

// deleteSessionAndFiles removes a session row (cascade clears events /
// segments / snapshots in DB) and best-effort removes the on-disk segment,
// snapshot, audio, and MP4-cache files. Returns true if the DB row was
// deleted; false on DB failure (in which case files are left alone to keep
// DB and disk in sync).
func (r *StorageRotator) deleteSessionAndFiles(sessionID string) bool {
	// Pre-fetch file paths before the cascade wipes the rows.
	segments, _ := r.store.GetSessionSegmentsIncludingProtected(sessionID)
	snapshots, _ := r.store.GetSessionSnapshotsIncludingProtected(sessionID)
	audioPath := r.store.getSessionAudioPath(sessionID)
	transcriptPath := r.store.getSessionTranscriptPath(sessionID)

	// DB first: if this fails we leave files in place so the next rotation
	// can retry, and the OCR worker won't find stale references mid-delete.
	if err := r.store.deleteSessionForRetention(sessionID); err != nil {
		log.Printf("[activity-rotator] failed to delete session %s: %v", sessionID, err)
		return false
	}

	// Files are best-effort; orphans are harmless.
	for _, seg := range segments {
		fullPath := filepath.Join(r.store.SegmentsDir(), seg.FilePath)
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[activity-rotator] failed to delete segment file %s: %v", fullPath, err)
		}
		r.removeMP4Cache(seg.FilePath)
	}
	for _, snap := range snapshots {
		snapPath := filepath.Join(r.store.SnapshotsDir(), snap.FilePath)
		os.Remove(snapPath)
	}
	if audioPath != "" {
		_ = removeMeetingRecording(r.store.AudioDir(), audioPath)
	}
	if transcriptPath != "" {
		os.Remove(transcriptPath)
	}
	return true
}

// removeMP4Cache deletes the cached MP4 file corresponding to an HEVC segment.
// The MP4 cache lives at ~/.yansu-agent/cache/mp4/<relPath with .mp4 extension>.
func (r *StorageRotator) removeMP4Cache(hevcRelPath string) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return
	}
	mp4Name := strings.TrimSuffix(hevcRelPath, ".hevc") + ".mp4"
	mp4Path := filepath.Join(dataRoot, "cache", "mp4", mp4Name)
	if err := os.Remove(mp4Path); err == nil {
		log.Printf("[activity-rotator] removed MP4 cache: %s", mp4Path)
	}
}
