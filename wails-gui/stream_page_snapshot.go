package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// The first page of a chat stream (LoadStreamItemsPage at the latest cursor)
// paints from a saved copy: the last projected page, saved beside the other
// private data. A copy is served only while two things hold:
//   - the privacy stamp it was made under (savedCopyStamp, which names the
//     durable brief privacy epoch, so a restart keeps the copy and any
//     withdrawal drops it at once), and
//   - the stream's item version it was read at (storage.StreamItemVersion, bumped
//     by triggers on every insert, edit, withdrawal and removal). A version that
//     differs is recomputed under a short deadline; on timeout the copy is served
//     and the event that changed the stream adds what is missing.
//
// Reads of one page share the next run (trailingCall), so a burst of reads costs
// two runs, not one per caller, and no caller gets a page computed before it
// asked. Older pages always read live.
const (
	streamPageDirName         = "stream-pages"
	streamPageVersionDeadline = 200 * time.Millisecond
	streamPageRecomputeWait   = 400 * time.Millisecond
	streamPageRefreshAge      = 15 * time.Minute
	streamPageLaunchDelay     = 30 * time.Second
	streamPageEventDelay      = time.Second
	streamPageDefaultLimit    = 50
	streamPageSnapshotFormat  = 2
)

// streamPageWakeEvents are the events that say a stream changed. They wake a
// refresh early; correctness never depends on them, because the version does.
var streamPageWakeEvents = map[string]bool{
	"session:items-appended": true, "main-chat:waiting-changed": true,
}

type streamPageKey struct {
	session string
	limit   int
}

type streamPageSnapshot struct {
	Format  int   `json:"format"`
	Version int64 `json:"version"`
	// ReadStamp is the cheap privacy stamp the page was made under; it is what a
	// read compares. Deep is the full check, recorded by a background pass; a copy
	// is served only once Deep is set.
	ReadStamp string             `json:"readStamp"`
	Deep      string             `json:"deep"`
	SavedAt   time.Time          `json:"savedAt"`
	Page      StreamItemPageData `json:"page"`
	// Cards are the page's proactive cards as stored, before projection. A read
	// projects them again through the live gates (stream_page_cards.go).
	Cards []StreamItemData `json:"cards,omitempty"`
}

type streamPageReadResult struct {
	page StreamItemPageData
	err  error
}

// streamPageState is the saved-page bookkeeping kept on the app.
type streamPageState struct {
	mu                         sync.Mutex
	launchAt                   time.Time
	keys                       map[streamPageKey]bool
	reads                      map[streamPageKey]*trailingCall[streamPageReadResult]
	lastRun                    time.Time
	dueAt                      time.Time
	verifyAt                   time.Time
	running                    bool
	versionWait, recomputeWait time.Duration
	launchDelay, eventDelay    time.Duration
	// computeGate, when set, runs inside each computation; tests use it to hold a
	// run open while more reads arrive.
	computeGate func()
	// cardBudget bounds the card check on one read; tests set it.
	cardBudget time.Duration
	// verifyQueued counts copies queued for their full check; verifyDone counts
	// the ones a pass has finished. Equal means no check is pending or running.
	verifyQueued, verifyDone atomic.Int64
	// verifyKeys are copies saved since the last background pass, awaiting their
	// full check; dueRecompute asks the next pass to recompute every page.
	verifyKeys   map[streamPageKey]bool
	dueRecompute bool
	// computes and retries count background work, so tests can prove reads
	// within the window do no work.
	computes atomic.Int64
	retries  atomic.Int64
}

// settings fills in the defaults a zero-value state lacks. Callers hold mu.
func (st *streamPageState) settings() {
	if st.keys == nil {
		st.keys = map[streamPageKey]bool{}
		st.launchAt = time.Now()
		st.versionWait = streamPageVersionDeadline
		st.recomputeWait = streamPageRecomputeWait
		st.launchDelay = streamPageLaunchDelay
		st.eventDelay = streamPageEventDelay
		st.cardBudget = streamPageCardBudget
	}
}

// readCall returns the coalescer for key. Callers hold mu.
func (st *streamPageState) readCall(key streamPageKey) *trailingCall[streamPageReadResult] {
	if st.reads == nil {
		st.reads = map[streamPageKey]*trailingCall[streamPageReadResult]{}
	}
	call := st.reads[key]
	if call == nil {
		call = &trailingCall[streamPageReadResult]{}
		st.reads[key] = call
	}
	return call
}

func streamPageFile(key streamPageKey) (string, error) {
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key.session + "\x00" + string(rune(key.limit))))
	return filepath.Join(root, streamPageDirName, hex.EncodeToString(sum[:16])+".json"), nil
}

// readStreamPageSnapshot returns the saved page when it was made under stamp
// and its full check has been recorded, and "" as the reason. Otherwise it
// returns why not: "no-copy", "stamp-changed", "unverified" or "unreadable". A
// copy whose stamp does not match is deleted, never served.
func readStreamPageSnapshot(key streamPageKey, stamp string) (streamPageSnapshot, string) {
	snap, reason := readRawStreamPageSnapshot(key)
	if reason != "" {
		return streamPageSnapshot{}, reason
	}
	if stamp == "" || snap.ReadStamp != stamp {
		if path, err := streamPageFile(key); err == nil {
			_ = os.Remove(path)
		}
		return streamPageSnapshot{}, "stamp-changed"
	}
	if snap.Deep == "" {
		return streamPageSnapshot{}, "unverified"
	}
	return snap, ""
}

// readRawStreamPageSnapshot reads the saved page without judging it.
func readRawStreamPageSnapshot(key streamPageKey) (streamPageSnapshot, string) {
	path, err := streamPageFile(key)
	if err != nil {
		return streamPageSnapshot{}, "unreadable"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return streamPageSnapshot{}, "no-copy"
	}
	var snap streamPageSnapshot
	if json.Unmarshal(raw, &snap) != nil || snap.Format != streamPageSnapshotFormat {
		_ = os.Remove(path)
		return streamPageSnapshot{}, "unreadable"
	}
	return snap, ""
}

func writeStreamPageSnapshot(key streamPageKey, snap streamPageSnapshot) error {
	path, err := streamPageFile(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadLatestStreamPage is the latest-cursor read. It is coalesced per page: a
// caller that arrives during a run gets the next run, which starts after it
// arrived.
func (a *App) loadLatestStreamPage(session string, limit int) (StreamItemPageData, error) {
	if limit <= 0 {
		limit = streamPageDefaultLimit
	}
	key := streamPageKey{session: session, limit: limit}
	st := &a.streamPages
	lockStart := time.Now()
	st.mu.Lock()
	lockWait := time.Since(lockStart)
	st.settings()
	st.keys[key] = true
	call := st.readCall(key)
	st.mu.Unlock()
	log.Printf("[perf] binding=LoadStreamItemsPage stage=lock_wait ms=%d", lockWait.Milliseconds())
	waitStart := time.Now()
	res := call.Do(func() streamPageReadResult {
		log.Printf("[perf] binding=LoadStreamItemsPage stage=coalescer_run_offset ms=%d", time.Since(waitStart).Milliseconds())
		page, err := a.loadLatestStreamPageNow(key)
		return streamPageReadResult{page: page, err: err}
	})
	log.Printf("[perf] binding=LoadStreamItemsPage stage=coalescer_total ms=%d", time.Since(waitStart).Milliseconds())
	return res.page, res.err
}

// loadLatestStreamPageNow is one run of the latest-cursor read: the saved first
// page when it holds, otherwise a computed one. It logs whether the copy was
// served and, if not, why.
func (a *App) loadLatestStreamPageNow(key streamPageKey) (StreamItemPageData, error) {
	st := &a.streamPages
	st.mu.Lock()
	versionWait, recomputeWait, cardBudget := st.versionWait, st.recomputeWait, st.cardBudget
	st.mu.Unlock()
	now := time.Now()
	stampStart := time.Now()
	stamp := a.streamPageStamp(context.Background())
	stampMs := time.Since(stampStart).Milliseconds()
	log.Printf("[perf] binding=LoadStreamItemsPage stage=privacy_stamp ms=%d", stampMs)
	if stamp == "" {
		page, _, err := a.computeLatestStreamPage(key, "")
		logStreamPage("recompute", "no-stamp", stampMs, 0, time.Since(now).Milliseconds(), 0, 0)
		return page, err
	}
	versionStart := time.Now()
	verCtx, cancel := context.WithTimeout(context.Background(), versionWait)
	version, verErr := a.persistenceService().StreamItemVersion(verCtx, key.session)
	cancel()
	versionMs := time.Since(versionStart).Milliseconds()
	log.Printf("[perf] binding=LoadStreamItemsPage stage=version ms=%d", versionMs)
	saved, reason := readStreamPageSnapshot(key, stamp)
	cards, cardCheckMs := 0, int64(0)
	if reason == "" {
		cards = streamPageCardCount(saved.Page)
	}
	if reason == "" && cards > 0 {
		// A copy with cards is served only when the version holds and every card
		// is confirmed by the live gates within the card budget (stream_page_cards.go).
		if verErr == nil && saved.Version == version {
			page, ms, why := a.confirmProactiveCards(key, saved, now, cardBudget)
			cardCheckMs = ms
			if why == "" {
				a.scheduleStreamPageRefresh(a.streamPageDueFor(now, saved.SavedAt))
				logStreamPage("copy", "version-held", stampMs, versionMs, 0, cards, cardCheckMs)
				return page, nil
			}
			reason = why
		} else {
			reason = "proactive-card"
		}
		if reason != "" {
			// Not served: drop the copy now, so a withdrawn card's raw row does
			// not stay in the file until the next save.
			removeStreamPageCopy(key)
		}
	}
	switch {
	case reason == "" && verErr == nil && saved.Version == version:
		a.scheduleStreamPageRefresh(a.streamPageDueFor(now, saved.SavedAt))
		logStreamPage("copy", "version-held", stampMs, versionMs, 0, 0, 0)
		return saved.Page, nil
	case reason == "" && verErr != nil:
		// The version could not be read in time: serve the copy and refresh.
		a.scheduleStreamPageRefresh(now)
		logStreamPage("copy", "version-timeout", stampMs, versionMs, 0, 0, 0)
		return saved.Page, nil
	case reason != "":
		// No usable copy: compute before the first paint, then refresh later.
		computeStart := time.Now()
		page, saved, err := a.computeLatestStreamPage(key, stamp)
		if saved {
			a.scheduleStreamVerify(key)
		}
		a.scheduleStreamPageRefresh(a.streamPageDueFor(now, time.Time{}))
		logStreamPage("recompute", reason, stampMs, versionMs, time.Since(computeStart).Milliseconds(), cards, cardCheckMs)
		return page, err
	}
	// The copy is from an older version: recompute under a short deadline. On
	// timeout the copy is served, and the stream event adds what is missing.
	type result struct {
		page StreamItemPageData
		err  error
	}
	done := make(chan result, 1)
	computeStart := time.Now()
	go func() {
		page, saved, err := a.computeLatestStreamPage(key, stamp)
		if saved {
			a.scheduleStreamVerify(key)
		}
		done <- result{page, err}
	}()
	select {
	case r := <-done:
		if r.err == nil {
			logStreamPage("recompute", "version-changed", stampMs, versionMs, time.Since(computeStart).Milliseconds(), 0, 0)
			return r.page, nil
		}
		a.scheduleStreamPageRefresh(now)
		logStreamPage("copy", "recompute-error", stampMs, versionMs, time.Since(computeStart).Milliseconds(), 0, 0)
		return saved.Page, nil
	case <-time.After(recomputeWait):
		a.scheduleStreamPageRefresh(now)
		logStreamPage("copy", "recompute-timeout", stampMs, versionMs, time.Since(computeStart).Milliseconds(), 0, 0)
		return saved.Page, nil
	}
}

// logStreamPage is the one line per read that says whether the saved copy was
// served, and why not. The timings show where a slow read spent its time.
func logStreamPage(served, reason string, stampMs, versionMs, computeMs int64, cards int, cardCheckMs int64) {
	log.Printf("[stream-page] served=%s reason=%s stamp_ms=%d version_ms=%d compute_ms=%d cards=%d card_check_ms=%d", served, reason, stampMs, versionMs, computeMs, cards, cardCheckMs)
}

// streamPageStamp is the privacy stamp a saved first page is made under: the
// durable one (savedCopyStamp), so a copy survives a restart and any privacy
// change, withdrawal included, drops it before the next read.
func (a *App) streamPageStamp(ctx context.Context) string {
	return a.savedCopyStamp(ctx)
}

// streamPageDueFor is when the next background refresh may run for a copy
// saved at savedAt (zero: none). A copy older than the refresh age refreshes
// now; otherwise the first refresh after launch waits the launch delay.
func (a *App) streamPageDueFor(now, savedAt time.Time) time.Time {
	st := &a.streamPages
	st.mu.Lock()
	defer st.mu.Unlock()
	st.settings()
	if !savedAt.IsZero() && now.Sub(savedAt) > streamPageRefreshAge {
		return now
	}
	if st.lastRun.IsZero() {
		if ready := st.launchAt.Add(st.launchDelay); ready.After(now) {
			return ready
		}
	}
	if !st.lastRun.IsZero() && now.Sub(st.lastRun) > streamPageRefreshAge {
		return now
	}
	return time.Time{}
}

// computeLatestStreamPage reads the latest page, projects it, and saves it when
// the privacy stamp it was read under still holds. The item version is read
// before the items, so a write during the read leaves the saved version behind.
// stamp is the stamp the caller read; "" means no copy is saved.
func (a *App) computeLatestStreamPage(key streamPageKey, stamp string) (StreamItemPageData, bool, error) {
	st := &a.streamPages
	st.computes.Add(1)
	st.mu.Lock()
	gate := st.computeGate
	st.mu.Unlock()
	if gate != nil {
		gate()
	}
	ctx := context.Background()
	version, verr := a.persistenceService().StreamItemVersion(ctx, key.session)
	page, err := a.persistenceService().LoadStreamItemsPage(ctx, key.session, -1, key.limit)
	if err != nil {
		return StreamItemPageData{}, false, err
	}
	cardsRaw := rawProactiveCards(page.Items)
	enrichStart := time.Now()
	streamCtx := context.WithValue(ctx, streamProjectionTimingKey{}, true)
	incomplete := false
	streamCtx = context.WithValue(streamCtx, streamProjectionIncompleteKey{}, &incomplete)
	a.projectStreamItemsContext(streamCtx, page.Items)
	release, err := a.projectMainChatView(streamCtx, key.session, page.Items, time.Now())
	enrichDuration := time.Since(enrichStart)
	if err != nil {
		log.Printf("[perf] binding=LoadStreamItemsPage stage=enrich ms=%d", enrichDuration.Milliseconds())
		return StreamItemPageData{}, false, err
	}
	release()
	log.Printf("[perf] binding=LoadStreamItemsPage stage=enrich ms=%d", enrichDuration.Milliseconds())
	if incomplete || verr != nil || stamp == "" || a.streamPageStamp(ctx) != stamp {
		return page, false, nil
	}
	_ = writeStreamPageSnapshot(key, streamPageSnapshot{Format: streamPageSnapshotFormat, Version: version, ReadStamp: stamp, SavedAt: time.Now(), Page: page, Cards: cardsRaw})
	return page, true, nil
}

// scheduleStreamPageRefresh queues background work no earlier than at (zero:
// nothing to do). One goroutine runs the queue; it holds no OS thread.
func (a *App) scheduleStreamPageRefresh(at time.Time) {
	if at.IsZero() {
		return
	}
	a.queueStreamWork(at, true, nil)
}

// scheduleStreamVerify queues the full check of one saved copy. It recomputes
// nothing.
func (a *App) scheduleStreamVerify(key streamPageKey) {
	a.queueStreamWork(time.Now(), false, &key)
}

func (a *App) queueStreamWork(at time.Time, recompute bool, verify *streamPageKey) {
	st := &a.streamPages
	st.mu.Lock()
	defer st.mu.Unlock()
	st.settings()
	if recompute {
		st.dueRecompute = true
		if st.dueAt.IsZero() || at.Before(st.dueAt) {
			st.dueAt = at
		}
	}
	if verify != nil {
		if st.verifyKeys == nil {
			st.verifyKeys = map[streamPageKey]bool{}
		}
		if !st.verifyKeys[*verify] {
			st.verifyQueued.Add(1)
		}
		st.verifyKeys[*verify] = true
		if st.verifyAt.IsZero() || at.Before(st.verifyAt) {
			st.verifyAt = at
		}
	}
	if st.running {
		return
	}
	st.running = true
	go a.runStreamPageRefreshes()
}

func (a *App) runStreamPageRefreshes() {
	st := &a.streamPages
	for {
		st.mu.Lock()
		now := time.Now()
		runRecompute := st.dueRecompute && !st.dueAt.IsZero() && !now.Before(st.dueAt)
		runVerify := len(st.verifyKeys) > 0 && !now.Before(st.verifyAt)
		if !runRecompute && !runVerify {
			// Nothing due: sleep until the earliest pending time, or stop.
			earliest := time.Time{}
			if st.dueRecompute && !st.dueAt.IsZero() {
				earliest = st.dueAt
			}
			if len(st.verifyKeys) > 0 && (earliest.IsZero() || st.verifyAt.Before(earliest)) {
				earliest = st.verifyAt
			}
			if earliest.IsZero() {
				st.running = false
				st.mu.Unlock()
				return
			}
			st.mu.Unlock()
			time.Sleep(min(time.Until(earliest), time.Second))
			continue
		}
		var verify map[streamPageKey]bool
		if runRecompute {
			st.dueAt = time.Time{}
			st.dueRecompute = false
		}
		if runVerify {
			verify = st.verifyKeys
			st.verifyKeys = nil
			st.verifyAt = time.Time{}
		}
		st.mu.Unlock()
		if runRecompute {
			a.refreshStreamPages()
		}
		for key := range verify {
			a.verifyStreamPageCopy(key)
			st.verifyDone.Add(1)
		}
	}
}

// verifyStreamPageCopy runs a copy's full check in the background. A copy with
// no recorded check gets one, if its cheap stamp still holds. A copy whose
// recorded check no longer matches is dropped, and the stream is told to reload.
func (a *App) verifyStreamPageCopy(key streamPageKey) {
	ctx := context.Background()
	snap, reason := readRawStreamPageSnapshot(key)
	if reason != "" {
		return
	}
	deep := a.savedCopyDeepStamp(ctx)
	if deep == "" {
		return
	}
	if snap.Deep == "" {
		if snap.ReadStamp == a.savedCopyStamp(ctx) {
			snap.Deep = deep
			_ = writeStreamPageSnapshot(key, snap)
		}
		return
	}
	if snap.Deep != deep {
		if path, err := streamPageFile(key); err == nil {
			_ = os.Remove(path)
		}
		a.emitBrowserEvent("session:items-appended", map[string]any{"sessionId": key.session, "source": "privacy-check"})
	}
}

// refreshStreamPages runs the work the saved read leaves out: the permission
// card retries, then a recomputed and saved first page for each known session.
// Retries that fail stay dirty, so a delay loses no write.
func (a *App) refreshStreamPages() {
	st := &a.streamPages
	st.retries.Add(1)
	a.retryWorkPermissionWrites()
	st.mu.Lock()
	st.settings()
	keys := make([]streamPageKey, 0, len(st.keys))
	for key := range st.keys {
		keys = append(keys, key)
	}
	st.mu.Unlock()
	for _, key := range keys {
		// A copy saved here is unverified until its full check runs. The read
		// path schedules that check; a refresh must too, or every read that
		// follows recomputes.
		if _, saved, _ := a.computeLatestStreamPage(key, a.streamPageStamp(context.Background())); saved {
			a.scheduleStreamVerify(key)
		}
	}
	st.mu.Lock()
	st.lastRun = time.Now()
	st.mu.Unlock()
}

// noteStreamPageEvent wakes the refresh early after a stream change event.
func (a *App) noteStreamPageEvent(eventName string) {
	if streamPageWakeEvents[eventName] {
		st := &a.streamPages
		st.mu.Lock()
		st.settings()
		delay := st.eventDelay
		st.mu.Unlock()
		a.scheduleStreamPageRefresh(time.Now().Add(delay))
	}
}
