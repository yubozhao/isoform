package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// The Today finds section paints from a saved view: the last filtered result,
// written beside topics.json with the same private permissions. It is served
// only while the privacy stamp it was made under still holds. A changed stamp
// (a forget, a private app, Closed Eyes) deletes it at once, so withdrawn text
// is never served. Writing happens only after the filter has run, and only when
// the stamp held across the whole computation.
//
// The view is recomputed rarely (snappy-first): when a source or topic change
// event arrives (coalesced for a minute), when the saved view is older than
// topicFindsRefreshAge, when there is no saved view, and at once when the
// privacy stamp changed. The first refresh after launch waits
// topicFindsLaunchDelay and runs on a low-priority thread.
const (
	topicFindsSnapshotFileName = "finds-view.json"
	topicFindsSnapshotVersion  = 1
	topicFindsLoadingStatus    = "loading"
	topicFindsRefreshAge       = 15 * time.Minute
	topicFindsLaunchDelay      = 30 * time.Second
	topicFindsEventDelay       = time.Minute
)

type topicFindsSnapshot struct {
	Version int `json:"version"`
	// ReadStamp is the cheap privacy stamp the view was made under (what a read
	// compares). Deep is the full check, taken before the view was computed; a
	// view is served only once Deep is set.
	ReadStamp string         `json:"readStamp"`
	Deep      string         `json:"deep"`
	SavedAt   time.Time      `json:"savedAt"`
	View      TopicFindsView `json:"view"`
}

// topicFindsSnapshotState is the refresh bookkeeping kept on the topics service.
type topicFindsSnapshotState struct {
	mu          sync.Mutex
	launchAt    time.Time
	launchDelay time.Duration
	eventDelay  time.Duration
	// dueAt is the earliest time the next refresh may run (zero: none queued).
	dueAt   time.Time
	running bool
	done    chan struct{}
	last    string
	// refreshes counts computations, so tests can prove reads do not recompute.
	refreshes atomic.Int64
	// computeHook runs between computing a view and checking the stamp again;
	// tests use it to move the stamp mid-computation.
	computeHook func()
	// lastRefresh is when the last refresh ran; zero means none since launch.
	lastRefresh time.Time
}

func (s *topicsService) findsSnapshotPath() string {
	if s == nil || s.store == nil || s.store.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.store.path), topicFindsSnapshotFileName)
}

// topicFindsRead says what a saved view means for the read that found it.
type topicFindsRead int

const (
	topicFindsMissing topicFindsRead = iota
	topicFindsStale
	topicFindsUnverified
	topicFindsFound
)

// readFindsSnapshot returns the saved view when it was made under stamp. Any
// other saved view, or an unreadable one, is deleted rather than served.
func (s *topicsService) readFindsSnapshot(stamp string) (TopicFindsView, time.Time, topicFindsRead) {
	path := s.findsSnapshotPath()
	if path == "" {
		return TopicFindsView{}, time.Time{}, topicFindsMissing
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return TopicFindsView{}, time.Time{}, topicFindsMissing
	}
	var snap topicFindsSnapshot
	if json.Unmarshal(raw, &snap) != nil || snap.Version != topicFindsSnapshotVersion || stamp == "" || snap.ReadStamp != stamp {
		_ = os.Remove(path)
		return TopicFindsView{}, time.Time{}, topicFindsStale
	}
	if snap.Deep == "" {
		return TopicFindsView{}, time.Time{}, topicFindsUnverified
	}
	return snap.View, snap.SavedAt, topicFindsFound
}

// writeFindsSnapshot saves view under stamp with the same permissions as the
// topics state: a private folder and owner-only files, written atomically.
func (s *topicsService) writeFindsSnapshot(stamp, deep string, view TopicFindsView) error {
	return s.writeFindsSnapshotAt(stamp, deep, view, time.Now())
}

func (s *topicsService) writeFindsSnapshotAt(stamp, deep string, view TopicFindsView, savedAt time.Time) error {
	path := s.findsSnapshotPath()
	if path == "" || stamp == "" || deep == "" {
		return nil
	}
	raw, err := json.Marshal(topicFindsSnapshot{Version: topicFindsSnapshotVersion, ReadStamp: stamp, Deep: deep, SavedAt: savedAt, View: view})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// topicFindsSaved is the Today section's read. A saved view made under the
// current stamp is served, and a refresh is scheduled only when it is older
// than topicFindsRefreshAge. A stale stamp refreshes at once; no saved view
// refreshes after the launch delay. Until a view exists the section shows its
// loading placeholder.
func (a *App) topicFindsSaved() TopicFindsView {
	svc := a.topicsSvc()
	now := time.Now()
	stampStart := time.Now()
	stamp := a.savedCopyStamp(context.Background())
	stampMs := time.Since(stampStart).Milliseconds()
	if stamp == "" {
		logFindsRead("placeholder", "no-stamp", stampMs)
		return a.topicFindsPlaceholder(svc)
	}
	view, savedAt, read := svc.readFindsSnapshot(stamp)
	switch read {
	case topicFindsFound:
		svc.findsSnap.mu.Lock()
		neverRefreshed := svc.findsSnap.lastRefresh.IsZero()
		svc.findsSnap.mu.Unlock()
		// The first refresh after launch runs the full check on what was saved
		// before the launch; an old view refreshes too.
		if neverRefreshed || now.Sub(savedAt) > topicFindsRefreshAge {
			svc.scheduleFindsRefresh(a, svc.afterLaunch(now))
		}
		logFindsRead("copy", "stamp-held", stampMs)
		return view
	case topicFindsStale:
		svc.scheduleFindsRefresh(a, now)
		logFindsRead("placeholder", "stamp-changed", stampMs)
	case topicFindsUnverified:
		svc.scheduleFindsRefresh(a, now)
		logFindsRead("placeholder", "unverified", stampMs)
	default:
		svc.scheduleFindsRefresh(a, svc.afterLaunch(now))
		logFindsRead("placeholder", "no-copy", stampMs)
	}
	return a.topicFindsPlaceholder(svc)
}

// logFindsRead is the one line per finds read that says whether the saved view
// was served, and why not.
func logFindsRead(served, reason string, stampMs int64) {
	log.Printf("[finds] served=%s reason=%s stamp_ms=%d", served, reason, stampMs)
}

func (a *App) topicFindsPlaceholder(svc *topicsService) TopicFindsView {
	if !svc.store.load().enabled() {
		return a.topicFindsView()
	}
	return TopicFindsView{Enabled: true, Status: topicFindsLoadingStatus, Items: []TopicFind{}, Topics: []string{}}
}

// afterLaunch is the earliest time a refresh may run: now, or once the launch
// delay has passed.
func (s *topicsService) afterLaunch(now time.Time) time.Time {
	s.findsSnap.mu.Lock()
	defer s.findsSnap.mu.Unlock()
	if ready := s.findsSnap.launchAt.Add(s.findsSnap.launchDelay); ready.After(now) {
		return ready
	}
	return now
}

// noteTopicFindsSourceChanged schedules a refresh after a source or topic
// change event. Events in a burst coalesce into one refresh.
func (a *App) noteTopicFindsSourceChanged(eventName string) {
	if !topicFindsSourceEvents[eventName] {
		return
	}
	svc := a.topicsSvc()
	svc.scheduleFindsRefresh(a, time.Now().Add(svc.findsEventDelay()))
}

// topicFindsSourceEvents are the events that say topics or their sources changed.
var topicFindsSourceEvents = map[string]bool{
	"topics:updated": true, "topics:withdrawn": true, "memory:updated": true,
	"activity:summary-ready": true, "people:updated": true, "calendar:synced": true,
	"mail:synced": true, "sensitive-apps:updated": true, closedEyesModeChangedEvent: true,
}

func (s *topicsService) findsEventDelay() time.Duration {
	s.findsSnap.mu.Lock()
	defer s.findsSnap.mu.Unlock()
	return s.findsSnap.eventDelay
}

// scheduleFindsRefresh queues one refresh no earlier than at. Requests that
// arrive while one is queued keep the earlier time; one goroutine runs them.
func (s *topicsService) scheduleFindsRefresh(a *App, at time.Time) {
	if a == nil || a.ctx == nil || a.ctx.Err() != nil {
		return
	}
	s.findsSnap.mu.Lock()
	defer s.findsSnap.mu.Unlock()
	if s.findsSnap.dueAt.IsZero() || at.Before(s.findsSnap.dueAt) {
		s.findsSnap.dueAt = at
	}
	if s.findsSnap.running {
		return
	}
	s.findsSnap.running = true
	s.findsSnap.done = make(chan struct{})
	go s.runFindsRefreshes(a, a.ctx, s.findsSnap.done)
}

// runFindsRefreshes runs queued refreshes one at a time, on a thread that has
// been lowered in priority. Work queued during a refresh runs after it.
func (s *topicsService) runFindsRefreshes(a *App, ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		s.findsSnap.mu.Lock()
		if ctx.Err() != nil {
			s.findsSnap.dueAt = time.Time{}
			s.findsSnap.running = false
			s.findsSnap.mu.Unlock()
			return
		}
		due := s.findsSnap.dueAt
		if due.IsZero() {
			s.findsSnap.running = false
			s.findsSnap.mu.Unlock()
			return
		}
		s.findsSnap.mu.Unlock()
		if wait := time.Until(due); wait > 0 {
			timer := time.NewTimer(min(wait, time.Second))
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		s.findsSnap.mu.Lock()
		s.findsSnap.dueAt = time.Time{}
		s.findsSnap.mu.Unlock()
		a.refreshTopicFinds()
	}
}

// refreshTopicFinds computes the view and saves it only if the privacy stamp
// held across the computation. It emits topics:finds when the view changed.
func (a *App) refreshTopicFinds() {
	svc := a.topicsSvc()
	svc.findsSnap.refreshes.Add(1)
	ctx := context.Background()
	before := a.savedCopyStamp(ctx)
	// The full check is taken before the view, so a change during the view's
	// computation leaves a mismatch for the next check to find.
	deep := a.savedCopyDeepStamp(ctx)
	view := a.topicFindsView()
	svc.findsSnap.mu.Lock()
	hook := svc.findsSnap.computeHook
	svc.findsSnap.mu.Unlock()
	if hook != nil {
		hook()
	}
	after := a.savedCopyStamp(ctx)
	svc.findsSnap.mu.Lock()
	svc.findsSnap.lastRefresh = time.Now()
	svc.findsSnap.mu.Unlock()
	if before == "" || before != after || deep == "" {
		return
	}
	raw, err := json.Marshal(view)
	if err != nil {
		return
	}
	svc.findsSnap.mu.Lock()
	changed := string(raw) != svc.findsSnap.last
	svc.findsSnap.last = string(raw)
	svc.findsSnap.mu.Unlock()
	if err := svc.writeFindsSnapshot(before, deep, view); err != nil {
		return
	}
	if changed {
		a.emitBrowserEvent("topics:finds")
	}
}
