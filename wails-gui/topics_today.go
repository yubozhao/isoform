package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

const topicTodayReadBudget = 100 * time.Millisecond

// Display-only derivative in topics.json. File identities deliberately fail
// closed after external edits or a WAL checkpoint; no persisted SQL counters
// or connection-local revisions are trusted across launches.
type topicTodaySnapshot struct {
	Views         []TopicView              `json:"views"`
	State         string                   `json:"state"`
	DatabaseFiles []string                 `json:"databaseFiles"`
	DatabaseStats topicFileStats           `json:"databaseStats"`
	Sources       topicFileStats           `json:"sources"`
	Sessions      topicSessionFileRevision `json:"sessions"`
	Interests     string                   `json:"interests"`
	Policy        [32]byte                 `json:"policy"`
	At            time.Time                `json:"at"`
	Until         time.Time                `json:"until"`
}

func topicTodayStateHash(state topicsStateFile) string {
	state.Today = nil
	return proactiveVoiceHash(state)
}

func (s *topicsService) registryDatabaseFiles(ctx context.Context) ([]string, error) {
	var paths []string
	if s.app.store != nil {
		files, err := s.app.store.DatabaseFiles(ctx)
		if err != nil {
			return nil, err
		}
		paths = append(paths, files...)
	}
	if s.app.activity != nil && s.app.activity.store != nil {
		rows, err := s.app.activity.store.reader().QueryContext(ctx, "PRAGMA database_list")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var seq int
			var name, path string
			if err := rows.Scan(&seq, &name, &path); err != nil {
				return nil, err
			}
			if name == "main" && path != "" {
				paths = append(paths, path, path+"-wal")
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func (s *topicsService) makeTodaySnapshot(ctx context.Context, c *topicRegistryCache) (*topicTodaySnapshot, error) {
	if len(c.result.views) == 0 {
		return nil, nil
	}
	stats, err := topicSessionFileStats(ctx, c.databaseFiles)
	if err != nil {
		return nil, err
	}
	views := c.result.views
	return &topicTodaySnapshot{Views: views, DatabaseFiles: c.databaseFiles, DatabaseStats: stats,
		Sources: c.revision.fileStats, Sessions: c.revision.sessionFiles, Interests: proactiveVoiceHash(c.revision.interests),
		Policy: c.revision.policy, At: c.at, Until: c.until}, nil
}

func (s *topicsService) todayViews() ([]TopicView, bool, bool) {
	views, refreshing, firstRun := s.snapshotRead()
	return views, refreshing, !firstRun && len(views) == 0
}

func (s *topicsService) todayRegistryViews(parent context.Context) (topicRegistryResult, bool, bool) {
	if s.app.IsClosedEyesModeEnabled() {
		return s.closedEyesRegistry(s.store.load(), s.now()), false, true
	}
	views, refreshing, firstRun := s.snapshotRead()
	return s.snapshotRegistryResult(views), refreshing, !firstRun && len(views) == 0
}

func (s *topicsService) snapshotRead() (views []TopicView, refreshing, firstRun bool) {
	if s.app.IsClosedEyesModeEnabled() {
		return s.closedEyesRegistry(s.store.load(), s.now()).views, false, false
	}
	snapshot := s.snapshots.Current()
	firstRun = snapshot == nil
	state := s.store.load()
	views = filterTopicsSnapshot(snapshot, liveTopicPrivacyPolicy())
	cache := s.loadRegistryCache()
	inheritedPins := map[string]bool{}
	if cache != nil {
		for alias, canonical := range cache.result.canonical {
			if state.SidebarPins[alias] {
				inheritedPins[canonical] = true
			}
		}
	}
	filtered := make([]TopicView, 0, len(views))
	for _, view := range views {
		if state.Overrides[view.ID].Removed || !topicDisplayNameSafe(view.Name) {
			continue
		}
		view.PinnedToSidebar = state.SidebarPins[view.ID] || inheritedPins[view.ID]
		for _, project := range state.Projects {
			if project.ID == view.ID {
				view.PinnedToSidebar = view.PinnedToSidebar || topicProjectPinned(project, state.SidebarPins)
				break
			}
		}
		filtered = append(filtered, view)
	}
	s.registryWorkerMu.Lock()
	refreshing = s.registryFullBuilding
	s.registryWorkerMu.Unlock()
	return filtered, refreshing, firstRun
}

func (s *topicsService) snapshotRegistryResult(views []TopicView) topicRegistryResult {
	result := topicRegistryResult{
		views:     views,
		keys:      map[string]TopicKeySet{},
		canonical: map[string]string{},
	}
	cache := s.loadRegistryCache()
	if cache == nil {
		return result
	}
	visible := make(map[string]bool, len(views))
	for _, view := range views {
		visible[view.ID] = true
		if keys, ok := cache.result.keys[view.ID]; ok {
			keys.ThreadKeys = append([]string(nil), keys.ThreadKeys...)
			keys.GoalIDs = append([]string(nil), keys.GoalIDs...)
			result.keys[view.ID] = keys
		}
		if canonical, ok := cache.result.canonical[view.ID]; ok {
			result.canonical[view.ID] = canonical
		}
	}
	for alias, canonical := range cache.result.canonical {
		if !visible[canonical] {
			continue
		}
		result.canonical[alias] = canonical
		if keys, ok := cache.result.keys[alias]; ok {
			keys.ThreadKeys = append([]string(nil), keys.ThreadKeys...)
			keys.GoalIDs = append([]string(nil), keys.GoalIDs...)
			result.keys[alias] = keys
		}
	}
	return result
}

func (s *topicsService) topicsStale() bool {
	s.registryWorkerMu.Lock()
	defer s.registryWorkerMu.Unlock()
	return s.registryStale
}

func (s *topicsService) readableTodaySnapshot(ctx context.Context, state topicsStateFile) bool {
	readable, _, _ := s.readableTodaySnapshotWithRetry(ctx, state)
	return readable
}

func (s *topicsService) readableTodaySnapshotWithRetry(ctx context.Context, state topicsStateFile) (bool, bool, bool) {
	saved := state.Today
	now := s.now()
	if saved == nil || now.Before(saved.At) || !now.Before(saved.Until) || saved.State != topicTodayStateHash(state) {
		return false, false, true
	}
	paths, err := s.registryDatabaseFiles(ctx)
	if err != nil {
		return false, false, ctx.Err() == nil
	}
	if proactiveVoiceHash(paths) != proactiveVoiceHash(saved.DatabaseFiles) {
		return false, false, true
	}
	var releases []func()
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	if s.app.activity != nil && s.app.activity.store != nil {
		revision, err := storage.DatabaseRevision(ctx, s.app.activity.store.db)
		if err != nil {
			return false, false, ctx.Err() == nil
		}
		release, err := storage.LockDatabaseRevision(ctx, s.app.activity.store.db, revision)
		if err != nil {
			return false, false, ctx.Err() == nil
		}
		releases = append(releases, release)
	}
	if s.app.store != nil {
		revision, err := s.app.store.ReadRevision(ctx)
		if err != nil {
			return false, false, ctx.Err() == nil
		}
		release, err := s.app.store.LockReadRevision(ctx, revision)
		if err != nil {
			return false, false, ctx.Err() == nil
		}
		releases = append(releases, release)
	}
	// This revision needs no row walk or observer installation on launch.
	policy, err := json.Marshal([]any{false, pii.GetUserSensitiveApps(), GetUserVoiceInputApps()})
	if err != nil {
		return false, ctx.Err() != nil, ctx.Err() == nil
	}
	if sha256.Sum256(policy) != saved.Policy {
		return false, false, true
	}
	if proactiveVoiceHash(s.app.interestsSvc().store.load()) != saved.Interests {
		return false, false, true
	}
	files, err := s.registrySourceFileStats(ctx)
	if err != nil {
		return false, ctx.Err() != nil, ctx.Err() == nil
	}
	if files != saved.Sources {
		return false, false, true
	}
	stats, err := topicSessionFileStats(ctx, saved.Sessions.Paths)
	if err != nil {
		return false, ctx.Err() != nil, ctx.Err() == nil
	}
	if stats != saved.Sessions.Stats {
		return false, false, true
	}
	stats, err = topicSessionFileStats(ctx, saved.DatabaseFiles)
	if err != nil {
		return false, ctx.Err() != nil, ctx.Err() == nil
	}
	if stats != saved.DatabaseStats {
		return false, false, true
	}
	return ctx.Err() == nil, ctx.Err() != nil, false
}

func (s *topicsService) registryWorkerRunning() bool {
	s.registryWorkerMu.Lock()
	defer s.registryWorkerMu.Unlock()
	return s.registryRefresh != nil
}
func (s *topicsService) registryPending() bool {
	s.registryWorkerMu.Lock()
	defer s.registryWorkerMu.Unlock()
	return s.registryBuildPending
}
func (s *topicsService) registryFullBuildRunning() bool {
	s.registryWorkerMu.Lock()
	defer s.registryWorkerMu.Unlock()
	return s.registryFullBuilding
}
func (s *topicsService) requestRegistryRefresh() {
	s.resetPartialTodayAttempts()
	s.scheduleRegistryRefresh(false)
}

func (s *topicsService) ensureRegistryRefresh() {
	s.scheduleRegistryRefresh(false)
}

// topicRegistryMinRescan is the shortest gap between background registry
// scans (Bo 2026-11-12: periodic, not real-time).
const topicRegistryMinRescan = 30 * time.Minute

func (s *topicsService) markTopicsStale() {
	s.registryWorkerMu.Lock()
	s.registryStale = true
	s.registryWorkerMu.Unlock()
}

func (s *topicsService) scheduleRegistryRefresh(manual bool) {
	s.registryWorkerMu.Lock()
	defer s.registryWorkerMu.Unlock()
	if s.registryRefresh == nil {
		return
	}
	if manual {
		s.registryNextRefresh = s.now()
		s.registryManualBuildPending = true
	} else if s.registryNextRefresh.IsZero() || s.registryNextRefresh.After(s.now()) {
		// A background request (capture, a chat backfill, a recovery retry)
		// rescans at most every topicRegistryMinRescan; only the person's
		// Refresh and privacy withdrawals go sooner.
		next := s.now()
		if earliest := s.registryLastBuildStart.Add(topicRegistryMinRescan); !s.registryLastBuildStart.IsZero() && earliest.After(next) {
			next = earliest
		}
		if s.registryNextRefresh.IsZero() || next.Before(s.registryNextRefresh) {
			s.registryNextRefresh = next
		}
	}
	s.registryBuildPending = true
	select {
	case s.registryRefresh <- struct{}{}:
	default:
	}
}

// Explicit user intent bypasses the background cooldown, still single-flight.
func (a *App) RefreshTodayTopics() {
	a.topicsSvc().scheduleRegistryRefresh(true)
}

func (s *topicsService) registryTimerAfter(delay time.Duration) <-chan time.Time {
	if s.registryAfter != nil {
		return s.registryAfter(delay)
	}
	return time.After(delay)
}

func (s *topicsService) applyPrivacyWithdrawal(scope privacyWithdrawalScope) {
	s.registrySnapshotMu.Lock()
	removed, err := s.snapshots.RemoveMatching(func(entry topicSnapshotEntry) bool {
		return entry.Unproven || topicRefsIntersectScope(entry.Refs, scope)
	})
	s.registryWorkerMu.Lock()
	if s.registryFullBuilding {
		s.registryWithdrawalScopes = append(s.registryWithdrawalScopes, scope)
	}
	deadline := s.now().Add(topicWithdrawalRebuildDelay)
	if s.registryWithdrawalRebuildAt.IsZero() || deadline.Before(s.registryWithdrawalRebuildAt) {
		s.registryWithdrawalRebuildAt = deadline
	}
	if s.registryNextRefresh.IsZero() || s.registryWithdrawalRebuildAt.Before(s.registryNextRefresh) {
		s.registryNextRefresh = s.registryWithdrawalRebuildAt
	}
	requests := s.registryRefresh
	changed := s.registryScheduleChanged
	if requests != nil {
		s.registryBuildPending = true
		select {
		case requests <- struct{}{}:
		default:
		}
	}
	s.registryWorkerMu.Unlock()
	s.registrySnapshotMu.Unlock()
	if err != nil {
		log.Printf("[topics] snapshot withdrawal failed: %v", err)
	}
	if removed > 0 {
		s.app.emitBrowserEvent("topics:updated")
	}
	if changed != nil {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
}

func (s *topicsService) startRegistryWorker(ctx context.Context) {
	s.registryWorkerMu.Lock()
	if s.registryRefresh != nil {
		s.registryWorkerMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	requests := make(chan struct{}, 1)
	done := make(chan struct{})
	scheduleChanged := make(chan struct{}, 1)
	s.registryRefresh = requests
	s.registryWorkerContext = ctx
	s.registryWorkerCancel = cancel
	s.registryWorkerDone = done
	s.registryScheduleChanged = scheduleChanged
	s.registryFullBuilding = false
	s.registryBuildPending = false
	s.registryWorkerMu.Unlock()
	interval := s.store.load().backgroundRefreshInterval()
	snapshot := s.snapshots.Current()
	s.registryWorkerMu.Lock()
	if snapshot == nil {
		s.registryNextRefresh = s.now()
	} else {
		s.registryNextRefresh = snapshot.At.Add(interval)
	}
	if !s.registryWithdrawalRebuildAt.IsZero() && s.registryWithdrawalRebuildAt.Before(s.registryNextRefresh) {
		s.registryNextRefresh = s.registryWithdrawalRebuildAt
	}
	immediate := !s.registryNextRefresh.After(s.now())
	s.registryWorkerMu.Unlock()
	if immediate {
		s.scheduleRegistryRefresh(false)
	}
	go func() {
		defer close(done)
		defer func() {
			s.registryWorkerMu.Lock()
			s.registryRefresh = nil
			s.registryScheduleChanged = nil
			s.registryWorkerMu.Unlock()
		}()
		chatTimer := time.NewTimer(time.Hour)
		chatTimer.Stop()
		defer chatTimer.Stop()
		var refreshDue <-chan time.Time
		var chatRetry <-chan time.Time
		for {
			if refreshDue == nil {
				s.registryWorkerMu.Lock()
				delay := s.registryNextRefresh.Sub(s.now())
				s.registryWorkerMu.Unlock()
				if delay < 0 {
					delay = 0
				}
				refreshDue = s.registryTimerAfter(delay)
			}
			chatOnly := false
			select {
			case <-ctx.Done():
				return
			case <-requests:
				s.registryWorkerMu.Lock()
				delay := s.registryNextRefresh.Sub(s.now())
				s.registryWorkerMu.Unlock()
				if delay > 0 {
					refreshDue = s.registryTimerAfter(delay)
					continue
				}
			case <-scheduleChanged:
				refreshDue = nil
				continue
			case <-refreshDue:
				refreshDue = nil
			case <-chatRetry:
				chatOnly = true
			}
			chatTimer.Stop()
			chatRetry = nil
			// A single builder owns identity and publication. Capture batches coalesce
			// into one pending pass rather than starting overlapping collectors.
			if err := lockTodayChatPublication(ctx, s.registryMu.TryLock); err != nil {
				return
			}
			if !chatOnly {
				s.resetPartialTodayAttempts()
				s.registryWorkerMu.Lock()
				started := s.now()
				interval := s.store.load().backgroundRefreshInterval()
				s.registryLastBuildStart = started
				s.registryWithdrawalRebuildAt = time.Time{}
				s.registryNextRefresh = started.Add(interval)
				s.registryBuildManual = s.registryManualBuildPending
				s.registryManualBuildPending = false
				s.registryStale = false
				s.registryLastSnapshotReplaced = false
				s.registryFullBuilding = true
				s.registryBuildPending = false
				s.registryWorkerMu.Unlock()
			}
			var err error
			published := false
			if chatOnly {
				published = s.retryRegistryChatLocked(ctx)
			} else {
				_, err = s.collectRegistryViewsLocked(ctx, true)
			}
			if work := s.registryDeferredChat; work != nil {
				chatTimer.Reset(time.Second << work.attempts)
				chatRetry = chatTimer.C
			}
			s.registryMu.Unlock()
			s.registryWorkerMu.Lock()
			s.registryBuildPending = len(requests) > 0
			replaced := s.registryLastSnapshotReplaced
			if !chatOnly {
				s.registryFullBuilding = false
			}
			s.registryWorkerMu.Unlock()
			refreshDue = nil
			if ctx.Err() != nil {
				return
			}
			if err != nil && !chatOnly {
				s.resetPartialTodayAttempts()
			}
			if err == nil {
				if (!chatOnly && replaced) || (chatOnly && published) {
					s.app.emitBrowserEvent("topics:updated")
				}
				continue
			}
		}
	}()
}

func (s *topicsService) registryContext() context.Context {
	s.registryWorkerMu.Lock()
	defer s.registryWorkerMu.Unlock()
	return s.registryWorkerContext
}

// Cancel early in shutdown so chat and capture flushes never wait for a
// collector. Join after those flushes, before closing the source databases.
func (s *topicsService) cancelRegistryWorker() {
	s.registryWorkerMu.Lock()
	cancel := s.registryWorkerCancel
	s.registryWorkerMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *topicsService) stopRegistryWorker() {
	s.cancelRegistryWorker()
	s.registryWorkerMu.Lock()
	done := s.registryWorkerDone
	s.registryWorkerMu.Unlock()
	if done != nil {
		<-done
	}
}
