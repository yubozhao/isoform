package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"reflect"
	"syscall"
	"time"
)

type topicRegistryCache struct {
	result        topicRegistryResult
	revision      topicRegistryRevision
	state         topicsStateFile
	databaseFiles []string
	chatComplete  bool
	at, until     time.Time
}

// Codes, never underlying error messages, cross the binding or enter logs.
// SQLite and filesystem messages can contain private SQL, titles or paths.
type topicRegistryReadError struct {
	step, kind string
	cause      error
}

func (e *topicRegistryReadError) Error() string {
	return "topic_registry_refresh_failed: step=" + e.step + " kind=" + e.kind
}
func (e *topicRegistryReadError) Unwrap() []error { return []error{errTopicRegistryChanged, e.cause} }

func topicRegistryErrorKind(err error) string {
	var coded interface{ Code() int }
	var osCode syscall.Errno
	switch true {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &coded):
		return fmt.Sprintf("sqlite_%d", coded.Code())
	case errors.As(err, &osCode):
		return fmt.Sprintf("os_%d", osCode)
	case errors.Is(err, fs.ErrNotExist):
		return "not_found"
	case errors.Is(err, fs.ErrPermission):
		return "permission"
	case errors.Is(err, errTopicRegistryChanged):
		return "revision_changed"
	default:
		return fmt.Sprintf("%T", err)
	}
}

func (s *topicsService) registryReadFailure(step string, err error, started time.Time, activities, goals int) error {
	kind := topicRegistryErrorKind(err)
	s.registryFailuresMu.Lock()
	defer s.registryFailuresMu.Unlock()
	key := step + ":" + kind
	if s.registryFailures == nil {
		s.registryFailures = map[string]bool{}
	}
	if !s.registryFailures[key] {
		s.registryFailures[key] = true
		log.Printf("[topics] registry_read_failed step=%s kind=%s elapsed_ms=%d activities=%d goals=%d", step, kind, time.Since(started).Milliseconds(), activities, goals)
	}
	return &topicRegistryReadError{step: step, kind: kind, cause: err}
}

func (s *topicsService) cachedRegistryViews(revision topicRegistryRevision, state topicsStateFile, now time.Time) (topicRegistryResult, bool) {
	c := s.loadRegistryCache()
	if !topicRegistryCacheMatches(c, revision, state, now) {
		return topicRegistryResult{}, false
	}
	// Refresh once in the background as the cache ages. The caller's snapshot
	// has just revalidated all source and privacy revisions. No stale cache
	// can authorize chat: chat still uses its original admission proofs.
	if !s.registryWorkerRunning() && now.Sub(c.at) >= 5*time.Second {
		s.retryRegistryInBackground()
	}
	return c.result, true
}

func (s *topicsService) retryRegistryInBackground() {
	if s.registryWorkerRunning() {
		s.ensureRegistryRefresh()
		return
	}
	// Tests without an app lifecycle use synchronous explicit refreshes only.
	if s.app.ctx == nil || s.registryRetrying || time.Now().Before(s.registryRetryAt) {
		return
	}
	s.registryRetrying = true
	s.registryRetryAt = time.Now().Add(5 * time.Second)
	go func() {
		ctx, cancel := context.WithTimeout(s.app.ctx, 5*time.Second)
		defer cancel()
		s.registryMu.Lock()
		defer s.registryMu.Unlock()
		defer func() { s.registryRetrying = false }()
		if ctx.Err() != nil {
			return
		}
		_, err := s.collectRegistryViewsLocked(ctx, true)
		s.registryWorkerMu.Lock()
		replaced := s.registryLastSnapshotReplaced
		s.registryWorkerMu.Unlock()
		if err == nil && replaced {
			s.app.emitBrowserEvent("topics:updated")
		}
	}()
}

func (s *topicsService) loadRegistryCache() *topicRegistryCache {
	s.registryCacheMu.RLock()
	defer s.registryCacheMu.RUnlock()
	return s.registryCache
}

// topicRegistryCachePrivacyMatches is topicRegistryCacheMatches without the
// source revisions: only the privacy part (activity and app-wide privacy
// epochs, forget markers, policy, Closed Eyes), the
// interests the person kept or removed, and the topics state must be unchanged. Used only while a rebuild holds the lock.
func topicRegistryCachePrivacyMatches(c *topicRegistryCache, revision topicRegistryRevision, state topicsStateFile, now time.Time) bool {
	return c != nil && !revision.closedEyes && !now.Before(c.at) && now.Before(c.until) &&
		c.revision.privacy == revision.privacy && c.revision.policy == revision.policy &&
		c.revision.briefPrivacy == revision.briefPrivacy && c.revision.forgets == revision.forgets &&
		c.revision.closedEyes == revision.closedEyes && reflect.DeepEqual(c.revision.interests, revision.interests) &&
		reflect.DeepEqual(c.state, state)
}

func topicRegistryCacheMatches(c *topicRegistryCache, revision topicRegistryRevision, state topicsStateFile, now time.Time) bool {
	return c != nil && !now.Before(c.at) && now.Before(c.until) && sameTopicRegistrySources(c.revision, revision) && reflect.DeepEqual(c.state, state)
}
