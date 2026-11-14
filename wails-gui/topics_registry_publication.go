package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log"
	"reflect"

	"wails-gui/internal/pii"
	"wails-gui/storage"
)

type topicRegistryRevision struct {
	main, activity  storage.ReadRevision
	activitySources topicActivityRevision
	sessionFiles    topicSessionFileRevision
	files           todayChatAdmissionFileRevision
	fileStats       topicFileStats
	interests       interestsStateFile
	policy          [sha256.Size]byte
	privacy         uint64
	// briefPrivacy and forgets are the app-wide privacy epoch (withdrawals,
	// corrections, sensitive apps) and the forget markers generation.
	briefPrivacy  uint64
	forgets       uint64
	closedEyes    bool
	mainStore     *storage.Store
	activityStore *ActivityStore
}

// All-or-release acquisition never holds Closed Eyes while waiting for a
// memory writer. The caller supplies the short snapshot/publication deadline.
func (s *topicsService) lockRegistrySnapshot(ctx context.Context, publishing bool) (func(), error) {
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		releases = nil
	}
	locks := []struct {
		try     func() bool
		release func()
	}{
		{s.app.closedEyes.mu.TryLock, s.app.closedEyes.mu.Unlock},
		{memoryMutationMu.TryLock, memoryMutationMu.Unlock},
		{memoryFileLocks.publication.TryLock, memoryFileLocks.publication.Unlock},
		{pii.TryLockSensitiveAppsForPublication, pii.UnlockSensitiveAppsForPublication},
		{userVoiceInputAppsPublicationMu.TryRLock, userVoiceInputAppsPublicationMu.RUnlock},
		{s.app.interestsSvc().store.publication.TryRLock, s.app.interestsSvc().store.publication.RUnlock},
	}
	if s.app.activity != nil && s.app.activity.store != nil {
		store := s.app.activity.store
		locks = append(locks, struct {
			try     func() bool
			release func()
		}{store.readPrivacyMu.TryLock, store.readPrivacyMu.Unlock})
	}
	if publishing {
		locks = append(locks, struct {
			try     func() bool
			release func()
		}{s.store.publication.TryLock, s.store.publication.Unlock})
	}
	if err := lockTodayChatPublication(ctx, func() bool {
		for _, lock := range locks {
			if !lock.try() {
				release()
				return false
			}
			releases = append(releases, lock.release)
		}
		return true
	}); err != nil {
		release()
		return nil, errors.Join(errTopicRegistryChanged, err)
	}
	return release, nil
}

// The source observer detects edits to existing sessions and external commits.
// For a published result only its known file paths need stat checks; new capture
// paths cannot withdraw its sources. Avoid rescanning the entire sessions table.
func (s *topicsService) registryRevisionWithSessionFiles(ctx context.Context, known *topicSessionFileRevision) (topicRegistryRevision, error) {
	stamp := topicRegistryRevision{mainStore: s.app.store, closedEyes: s.app.IsClosedEyesModeEnabled(),
		briefPrivacy: mobileBriefPrivacyEpoch.Load(), forgets: memoryTombstoneGeneration.Load()}
	var err error
	raw, err := json.Marshal([]any{stamp.closedEyes, pii.GetUserSensitiveApps(), GetUserVoiceInputApps()})
	if err != nil {
		return stamp, err
	}
	stamp.policy = sha256.Sum256(raw)
	stamp.interests = s.app.interestsSvc().store.load()
	if !stamp.closedEyes {
		stamp.fileStats, err = s.registrySourceFileStats(ctx)
		if err != nil {
			return stamp, err
		}
	}
	if s.app.store != nil {
		stamp.main, err = s.app.store.ReadRevision(ctx)
		if err != nil {
			return stamp, err
		}
	}
	if s.app.activity != nil && s.app.activity.store != nil {
		stamp.activityStore = s.app.activity.store
		stamp.activity, err = storage.DatabaseRevision(ctx, stamp.activityStore.db)
		stamp.privacy = stamp.activityStore.readPrivacyRevision.Load()
		if err != nil {
			return stamp, err
		}
		if stamp.privacy%2 != 0 {
			return stamp, errTopicRegistryChanged
		}
		if !stamp.closedEyes {
			if known == nil {
				stamp.sessionFiles, err = s.registrySessionFiles(ctx)
			} else {
				stamp.sessionFiles.Paths = known.Paths
				stamp.sessionFiles.Stats, err = topicSessionFileStats(ctx, known.Paths)
			}
			if err != nil {
				return stamp, err
			}
		}
		stamp.activitySources, err = readTopicActivityRevision(ctx, stamp.activityStore.db)
		if err != nil {
			return stamp, err
		}
	}
	return stamp, ctx.Err()
}

func (s *topicsService) captureRegistryRevision(ctx context.Context) (topicRegistryRevision, error) {
	if err := s.prepareRegistryActivityRevision(ctx); err != nil {
		return topicRegistryRevision{}, err
	}
	var files todayChatAdmissionFileRevision
	var stats topicFileStats
	var err error
	if !s.app.IsClosedEyesModeEnabled() {
		files, stats, err = s.cachedRegistrySourceFiles(ctx)
		if err != nil {
			return topicRegistryRevision{}, err
		}
	}
	// Enumerate source permission files outside the short publication barrier.
	// Bind the enumeration to the scoped database revision so a newly changed
	// session cannot introduce an unchecked path before collection begins.
	var sessionFiles topicSessionFileRevision
	var activitySources topicActivityRevision
	if s.app.activity != nil && s.app.activity.store != nil && !s.app.IsClosedEyesModeEnabled() {
		activitySources, err = readTopicActivityRevision(ctx, s.app.activity.store.db)
		if err != nil {
			return topicRegistryRevision{}, err
		}
		sessionFiles, err = s.registrySessionFilesForCapture(ctx, activitySources)
		if err != nil {
			return topicRegistryRevision{}, err
		}
		if s.registrySessionFilesCaptured != nil {
			s.registrySessionFilesCaptured()
		}
	}
	short, cancel := context.WithTimeout(ctx, topicRegistrySnapshotBudget)
	defer cancel()
	release, err := s.lockRegistrySnapshot(short, false)
	if err != nil {
		return topicRegistryRevision{}, err
	}
	defer release()
	stamp, err := s.registryRevisionWithSessionFiles(short, &sessionFiles)
	if err != nil {
		return stamp, err
	}
	if !stamp.closedEyes {
		// The paths describe the header versions seen during enumeration.
		// Fresh stats alone cannot bless a replacement header with old paths.
		if stamp.sessionFiles.Stats != sessionFiles.Stats {
			return stamp, errTopicRegistryChanged
		}
		if stamp.activitySources != activitySources {
			return stamp, errTopicRegistryChanged
		}
		if stamp.fileStats != stats {
			return stamp, errTopicRegistryChanged
		}
		stamp.files = files
	}
	return stamp, nil
}

func (s *topicsService) publishRegistrySnapshot(ctx context.Context, expected topicRegistryRevision, state topicsStateFile, result topicRegistryResult, cache *topicRegistryCache, candidate *topicSnapshot) error {
	short, cancel := context.WithTimeout(ctx, topicRegistrySnapshotBudget)
	defer cancel()
	release, err := s.lockRegistrySnapshot(short, true)
	if err != nil {
		return err
	}
	defer release()
	current, err := s.registryRevisionWithSessionFiles(short, &expected.sessionFiles)
	current.files = expected.files // Already hashed outside barriers; metadata must still match.
	if err != nil {
		return err
	}
	if !sameTopicRegistrySources(current, expected) {
		return errTopicRegistryChanged
	}
	if expected.activityStore != nil {
		release, err := lockTopicActivityRevision(short, expected.activityStore.db, expected.activitySources)
		if err != nil {
			return errors.Join(errTopicRegistryChanged, err)
		}
		defer release()
	}
	if expected.mainStore != nil {
		release, err := expected.mainStore.LockReadRevision(short, expected.main)
		if err != nil {
			return errors.Join(errTopicRegistryChanged, err)
		}
		defer release()
	}
	// Source files may also be edited outside app writer barriers while the
	// database fence waits. Recheck files only; no database reads under fences.
	if err := s.validateRegistryFiles(short, expected); err != nil {
		return err
	}
	// Keep positions in the shared desktop/phone publication. Priority is used
	// for the first display; later new topics join after retained cards.
	if s.registryWorkerRunning() && state.Today != nil {
		result.views = stableTopicViewOrder(state.Today.Views, result.views)
		reorderTopicSnapshotEntries(candidate, result.views)
		cache.result = result
	}
	today, err := s.makeTodaySnapshot(short, cache)
	if err != nil {
		return err
	}
	if err := s.store.publishRegistryLocked(state, result.projects, result.chat, today); err != nil {
		return err
	}
	// Bind the result to the exact state while choices/withdrawals are still
	// excluded. A concurrent later choice must invalidate, never bless, this cache.
	cache.state = s.store.load()
	s.registryCacheMu.Lock()
	s.registryCache = cache
	s.registryPartial = nil
	s.registryCacheMu.Unlock()
	log.Printf("[topics] today level=info stage=publish publish_count=%d", len(result.views))
	return nil
}

func reorderTopicSnapshotEntries(snapshot *topicSnapshot, views []TopicView) {
	if snapshot == nil || len(snapshot.Topics) < 2 {
		return
	}
	byID := make(map[string]topicSnapshotEntry, len(snapshot.Topics))
	for _, entry := range snapshot.Topics {
		byID[entry.Topic.ID] = entry
	}
	ordered := make([]topicSnapshotEntry, 0, len(snapshot.Topics))
	for _, view := range views {
		if entry, ok := byID[view.ID]; ok {
			ordered = append(ordered, entry)
			delete(byID, view.ID)
		}
	}
	for _, entry := range snapshot.Topics {
		if remaining, ok := byID[entry.Topic.ID]; ok {
			ordered = append(ordered, remaining)
			delete(byID, entry.Topic.ID)
		}
	}
	snapshot.Topics = ordered
}

func (s *topicsService) validateRegistryFiles(ctx context.Context, expected topicRegistryRevision) error {
	if !expected.closedEyes && expected.activityStore != nil {
		sessionFiles, err := topicSessionFileStats(ctx, expected.sessionFiles.Paths)
		if err != nil {
			return err
		}
		if sessionFiles != expected.sessionFiles.Stats {
			return errTopicRegistryChanged
		}
	}
	if !expected.closedEyes {
		files, err := s.registrySourceFileStats(ctx)
		if err != nil {
			return err
		}
		if files != expected.fileStats {
			return errTopicRegistryChanged
		}
	}
	if !reflect.DeepEqual(s.app.interestsSvc().store.load(), expected.interests) {
		return errTopicRegistryChanged
	}
	return nil
}

// Ignore only the writer's unrelated append-only capture inserts. Both the
// scoped source revision and other-connection data_version still have to match.
func sameTopicRegistrySources(left, right topicRegistryRevision) bool {
	left.activity = right.activity
	return reflect.DeepEqual(left, right)
}

func stableTopicViewOrder(previous, incoming []TopicView) []TopicView {
	byID := make(map[string]TopicView, len(incoming))
	for _, view := range incoming {
		byID[view.ID] = view
	}
	ordered := make([]TopicView, 0, len(incoming))
	for _, old := range previous {
		if view, ok := byID[old.ID]; ok {
			ordered = append(ordered, view)
			delete(byID, old.ID)
		}
	}
	for _, view := range incoming {
		if _, ok := byID[view.ID]; ok {
			ordered = append(ordered, view)
			delete(byID, view.ID)
		}
	}
	return ordered
}
