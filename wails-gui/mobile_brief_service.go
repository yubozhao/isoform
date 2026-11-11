package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
)

const (
	mobileBriefTickInterval = time.Minute
	// mobileBriefRetryDelay is the first wait after a failed scheduled build;
	// later ones back off (mobileBriefRetryBackoff).
	mobileBriefRetryDelay = 2 * time.Minute
	// mobileBriefSourceRetries is how many times one build collects again
	// when its sources changed during collection, before it counts as failed.
	mobileBriefSourceRetries = 3
	// mobileBriefLateAfter is how long after its slot a scheduled brief is
	// labelled late.
	mobileBriefLateAfter = 30 * time.Minute
	// mobileBriefBuildLead is how long before its slot a scheduled brief starts
	// building, so it is in the phone's copy by the slot time.
	mobileBriefBuildLead      = 10 * time.Minute
	mobileBriefCooldown       = 10 * time.Minute
	mobileBriefDefaultWindow  = 24 * time.Hour
	mobileBriefMaxWindow      = 7 * 24 * time.Hour
	mobileBriefReasonPhone    = "phone"
	mobileBriefReasonSettings = "settings"
	mobileBriefReasonSchedule = "scheduled"
	mobileBriefReasonWake     = "wake"
	mobileBriefReasonAdaptive = "adaptive"
)

func init() {
	mobileBriefServiceFactory = func(app *App) mobileBriefSource {
		return newMobileBriefService(app)
	}
}

var _ mobileBriefSource = (*mobileBriefService)(nil)

// mobileBriefService produces the morning and evening briefs, on-demand briefs
// and the schedule view. At most one generation runs at a time, whatever
// triggered it.
type mobileBriefService struct {
	// loggedFailureKeys remembers which failure kinds were logged, once each.
	loggedFailureKeys map[string]bool
	// amend coalesces the amendment of sealed briefs after a privacy change: at
	// most one pass runs, and a change during it sets dirty so one more runs.
	amend    briefAmendState
	app      *App
	store    *mobileBriefStore
	config   *mobileBriefConfigStore
	runs     *mobileBriefRunStore
	now      func() time.Time
	location *time.Location
	tick     time.Duration
	gather   func(ctx context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error)
	power    func(reason string) (func(), error)
	// compose is the narrative boundary, like gather and publish.
	compose         func(context.Context, *App, MobileBriefV1, []MobileBriefCard) ([]MobileBriefPara, string, []mobileBriefCardWords)
	composeWithLead func(context.Context, *App, MobileBriefV1, []MobileBriefCard) ([]MobileBriefPara, string, []mobileBriefCardWords, string, string)
	// modelCall is a test seam; production uses the existing assistant route.
	modelCall func(context.Context, string) (string, error)
	// assistantState reads the same cached sign-in probe used by routing.
	assistantState func(context.Context, string) string
	authResumed    map[string]bool
	// publish is the transport boundary, independent of brief persistence.
	publish func(MobileBriefV1, bool) error
	// speaker reads the brief aloud at build time; a zero speaker stores
	// briefs without audio.
	speaker mobileBriefSpeaker
	// shadowCtx and shadowCancel bound the background shadow runs (see
	// mobile_brief_shadow.go); shadowBudget counts their model calls per day.
	shadowCtx    context.Context
	shadowCancel context.CancelFunc
	shadowBudget mobileBriefModelBudget
	// feed serves the spoken briefs as a private podcast on this Mac while
	// the schedule's feed switch is on.
	feed *mobileBriefFeedServer

	powerWarn sync.Once
	// packageMu orders package writes (mobile_brief_package.go).
	packageMu sync.Mutex

	mu      sync.Mutex
	runCtx  context.Context
	cancel  context.CancelFunc
	started bool
	// startedAt is when this process started the service: a slot scheduled
	// before it is one Isoform wasn't open for.
	startedAt     time.Time
	stopped       bool
	stopDone      chan struct{}
	running       bool
	runningSince  time.Time
	runFinished   chan struct{}
	todayRequests []string
	phoneRequest  *mobileBriefPendingRequest
	queueCancel   context.CancelFunc
	lastOnDemand  map[string]time.Time
	retryAfter    time.Time
	// retryFor is the brief id retryAfter holds back: a slot that keeps
	// failing never delays the next slot.
	retryFor string
	// sourceRetriedFor is the brief id whose last failure, sources changed
	// during collection, was already retried at once.
	sourceRetriedFor string
	missedLogged     map[string]bool
	// awakeSlots are the slots whose window was open while the computer was
	// awake. One that closes without a brief was not slept through, so no
	// missed notice goes out for it (a failure has its own notice).
	awakeSlots map[string]bool
	// windowLogged are the slots already logged as having no delivery window.
	windowLogged map[string]bool
	wake         chan struct{}
	// ahead carries early-brief triggers (lock, display sleep, the scheduled moment).
	ahead       chan string
	aheadArmed  string
	// noTextBuilds counts, per slot ID, the builds that wrote no words (see noteNoTextBuild).
	noTextBuilds map[string]int
	aheadTimer  *time.Timer
	sleeping    bool
	sleepAt     time.Time
	wakeWindows []mobileBriefWakeWindow
	sleepState  func() activitymon.SystemStateChange
	// timing is a fixture seam; production projects only existing timestamps.
	timing func(time.Time) mobileBriefTimingInput
	wg     sync.WaitGroup
	// audioMu keeps same-ID renders and attachment publication in order.
	audioMu sync.Mutex
	// audioRemakeQueued holds one pending remake after a memory change.
	audioRemakeQueued bool
	// Guarded by mu; one failed preparation is surfaced before a user retry.
	audioUploadVersion string
	audioUploadErr     error
	audioUploadCancel  context.CancelFunc
	// patchMu serializes the read-change-save steps on a stored brief (the
	// spoken version, new finds), so neither overwrites the other.
	patchMu sync.Mutex
	todayMu sync.Mutex
	// todayRefreshing is set while refreshTodaySoon runs; todayRefreshAgain
	// asks it for one more pass. Both guarded by todayRefreshMu.
	todayRefreshMu    sync.Mutex
	todayRefreshing   bool
	todayRefreshAgain bool
	// todayRefreshStore is set by a request that may store a refresh's result for
	// a sealed brief (not a mail sync). Guarded by todayRefreshMu.
	todayRefreshStore bool
	todayRefreshWG    sync.WaitGroup
	// todayRefreshAt is when the last live refresh started; todayRefreshQueued
	// means one deferred refresh waits for the gap to pass. Guarded by todayRefreshMu.
	todayRefreshAt     time.Time
	todayRefreshQueued bool
	// withheldLogged is the last withheld text latestWithToday logged, so a
	// condition that lasts is logged once. Guarded by todayMu.
	withheldLogged string
	// Serialize delivery separately; relay latency must never block local reads.
	deliveryMu sync.Mutex
}

type mobileBriefPendingRequest struct {
	state   string
	startAt time.Time
	readyAt time.Time
}

func newMobileBriefService(app *App) *mobileBriefService {
	s := buildMobileBriefService(app)
	if s != nil {
		// The live service is the one stored briefs and source changes re-seal.
		mobileBriefLive.Store(s)
	}
	return s
}

func buildMobileBriefService(app *App) *mobileBriefService {
	stateDir, err := mobileBriefStateDir()
	if err != nil {
		log.Printf("[mobile-brief] state directory is unavailable")
		stateDir = ""
	}
	svc := &mobileBriefService{
		app:          app,
		store:        newMobileBriefStore(stateDir),
		config:       newMobileBriefConfigStore(stateDir),
		runs:         newMobileBriefRunStore(stateDir),
		now:          time.Now,
		location:     time.Local,
		tick:         mobileBriefTickInterval,
		power:        holdMobileBriefPowerAssertion,
		lastOnDemand: map[string]time.Time{},
		missedLogged: map[string]bool{},
		wake:         make(chan struct{}, 1),
		ahead:        make(chan string, 4),
		sleepState:   activitymon.LatestSystemSleepState,
		authResumed:  map[string]bool{},
	}
	svc.publish = func(brief MobileBriefV1, notify bool) error {
		return svc.app.publishMobileBrief(brief, notify)
	}
	svc.gather = func(ctx context.Context, slot string, windowStart, asOf time.Time) (mobileBriefInput, error) {
		return collectMobileBriefInput(ctx, svc.app, slot, windowStart, asOf)
	}
	if app != nil {
		svc.assistantState = app.mobileBriefAssistantState
		svc.speaker = appMobileBriefSpeaker(app)
	}
	svc.feed = newMobileBriefFeedServer(stateDir, svc.store.dir, mobileHubName, svc.feedBriefs)
	if app != nil {
		svc.feed.audioAllowed = func(id string) bool {
			brief, ok := svc.store.load(id)
			return ok && app.mobileBriefForDelivery(brief).Audio != nil
		}
	}
	return svc
}

// Start runs one due check at once, which catches up after launch or wake,
// then checks every tick until ctx is done or Stop is called.
func (s *mobileBriefService) Start(parent context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.started = true
	s.startedAt = s.now()
	s.cancel = cancel
	s.runCtx = ctx
	s.wg.Add(4)
	s.mu.Unlock()
	// The one-time migration of copies the old rule hid runs once per start, off the read path.
	go func() {
		defer s.wg.Done()
		s.migrateOldSealedCopies()
	}()
	defer s.wg.Done()
	if s.config.load().Feed {
		s.applyFeed(true)
	}
	go s.loop(ctx)
	go func() {
		defer s.wg.Done()
		s.resumeSealRetries()
		s.remakeLegacyBriefOnce(ctx)
		s.backfillTodayAudio(ctx)
		s.resumeBriefAudioRetry()
	}()
}

// applyFeed starts or stops the podcast feed server. A feed that cannot start
// is logged once and the switch stays on, so the next start tries again.
func (s *mobileBriefService) applyFeed(on bool) {
	if s.feed == nil {
		return
	}
	if !on {
		s.feed.Stop()
		return
	}
	if _, err := s.feed.Start(); err != nil {
		log.Printf("[mobile-brief] the podcast feed could not start on this Mac")
	}
}

// Stop cancels service work and waits for it to finish before returning.
func (s *mobileBriefService) Stop() {
	s.mu.Lock()
	if s.stopped {
		done := s.stopDone
		s.mu.Unlock()
		<-done
		return
	}
	s.stopped = true
	s.stopDone = make(chan struct{})
	done := s.stopDone
	cancel := s.cancel
	queueCancel := s.queueCancel
	audioCancel := s.audioUploadCancel
	shadowCancel := s.shadowCancel
	s.mu.Unlock()
	if shadowCancel != nil {
		shadowCancel()
	}
	s.stopSealRetries()
	if cancel != nil {
		cancel()
	}
	if queueCancel != nil {
		queueCancel()
	}
	if audioCancel != nil {
		audioCancel()
	}
	s.wg.Wait()
	if s.feed != nil {
		s.feed.Stop()
	}
	close(done)
}

// beginWork admits a write-capable operation before shutdown starts.
func (s *mobileBriefService) beginWork() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return false
	}
	s.wg.Add(1)
	return true
}

func (s *mobileBriefService) loop(ctx context.Context) {
	defer s.wg.Done()
	s.checkDue(ctx)
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkDue(ctx)
		case <-s.wake:
			s.checkDue(ctx)
		case trigger := <-s.ahead:
			s.aheadCheck(ctx, trigger)
		}
	}
}

// checkDue generates the scheduled brief that is due now, if any. Observed
// sleep recovers missed slots even after their normal catch-up window closes.
func (s *mobileBriefService) checkDue(ctx context.Context) {
	s.armAheadMoment(s.now(), s.location)
	if s.app.firstWeekBriefWait(s.now()) {
		return
	}
	var latestSleepState activitymon.SystemStateChange
	if s.sleepState != nil {
		latestSleepState = s.sleepState()
	}
	s.mu.Lock()
	recoveringWake := s.sleeping && latestSleepState.Kind == activitymon.SystemStateWake && !latestSleepState.At.Before(s.sleepAt)
	s.mu.Unlock()
	// The notification may have missed the relay's wake hook as well. Close its
	// old socket before clearing the sleep latch or waking another due check.
	if recoveringWake && s.app != nil {
		s.app.mobileRelayRecovered()
	}
	s.mu.Lock()
	// A full native event queue may drop Wake. The observation survives outside
	// that queue, so reconcile it before allowing the sleep latch to skip a tick.
	if recoveringWake && s.sleeping && !latestSleepState.At.Before(s.sleepAt) {
		// Pair the cached wake with its native sleep, not an older delivered
		// sleep from before an awake interval whose notifications were lost.
		s.sleepAt = latestSleepState.SleepAt
		s.systemStateChangedLocked(latestSleepState)
	}
	sleeping := s.sleeping
	wakeWindows := append([]mobileBriefWakeWindow(nil), s.wakeWindows...)
	s.mu.Unlock()
	if sleeping {
		return
	}
	settings := s.config.load()
	if !settings.Enabled {
		return
	}
	now := s.now()
	s.noteAwake(now, settings)
	if settings.Adaptive {
		s.checkAdaptiveDue(ctx, now, settings)
		return
	}
	slot, due, _ := mobileBriefPendingSlot(now, s.location, settings, s.slotFilled)
	recovery, recovering := mobileBriefWakeSlot(now, s.location, settings, s.slotFilled, wakeWindows)
	if recovering && !s.slotHasRequestedBrief(recovery, settings) {
		slot, due = recovery.Slot, true
	} else {
		recovering = false
	}
	// Slots before the schedule was first watched, or before it was last
	// turned on, are never missed.
	since := s.runs.firstSeen(now)
	if enabledAt, err := time.Parse(time.RFC3339Nano, settings.EnabledAt); err == nil && enabledAt.After(since) {
		since = enabledAt
	}
	if !recovering {
		s.noteMissed(now, mobileBriefMissedSlots(now, s.location, settings, s.store.exists, since))
		// A slot that passed before the schedule was turned on or first
		// watched is not made late, nor one a brief made on request after its
		// time stands in for.
		if occurrence, found := s.slotOccurrence(now, settings, slot); due && found && (!s.catchUpAllowed(occurrence.ScheduledAt, now, settings) || s.briefSince(occurrence.ScheduledAt) || s.briefJustBefore(occurrence.ScheduledAt)) {
			due = false
		}
	}
	if !due {
		return
	}

	occurrence, found := s.slotOccurrence(now, settings, slot)
	if found && s.briefWaitingForSignIn(ctx, occurrence.BriefID) {
		return
	}
	assistant := s.briefSignInAssistant(occurrence.BriefID)
	s.mu.Lock()
	if s.running || s.stopped || s.sleeping || s.retryHeldLocked(now, occurrence.BriefID) || (assistant != "" && s.authResumed[assistant]) {
		s.mu.Unlock()
		return
	}
	if assistant != "" {
		if s.authResumed == nil {
			s.authResumed = map[string]bool{}
		}
		s.authResumed[assistant] = true
	}
	s.running = true
	s.runningSince = now
	s.runFinished = make(chan struct{})
	s.coalesceQueuedPhoneRequestLocked(now)
	s.mu.Unlock()
	s.notifyPhoneToday()
	s.announceBuilding(true)

	reason := mobileBriefReasonSchedule
	var generation *MobileBriefGeneration
	if recovering {
		reason = mobileBriefReasonWake
		generation = &MobileBriefGeneration{Reason: "mac_asleep", ScheduledAt: formatMobileBriefTime(recovery.ScheduledAt)}
	} else if found && now.Sub(occurrence.ScheduledAt) > mobileBriefLateAfter {
		// Isoform was not running at the slot's time, or earlier tries failed.
		generation = &MobileBriefGeneration{Reason: "late", ScheduledAt: formatMobileBriefTime(occurrence.ScheduledAt)}
	}
	recorded := false
	recordCompleted := func(brief MobileBriefV1) {
		if !found {
			return
		}
		run := mobileBriefRun{BriefID: brief.BriefID, Slot: slot, ScheduledAt: formatMobileBriefTime(occurrence.ScheduledAt), ScheduledReady: true,
			Status: mobileBriefRunOK, At: brief.AsOf}
		if generation != nil {
			if recovering {
				run.Reason = mobileBriefMissedAsleep
			} else {
				run.Reason = s.missedReason(occurrence.ScheduledAt)
			}
		}
		s.recordRun(run)
		recorded = true
	}
	brief, err := s.generateRefencedOnStored(ctx, slot, reason, generation, recordCompleted)

	s.mu.Lock()
	s.running = false
	s.runningSince = time.Time{}
	todayRequests := s.todayRequests
	s.todayRequests = nil
	if s.runFinished != nil {
		close(s.runFinished)
		s.runFinished = nil
	}
	if s.phoneRequest != nil && s.phoneRequest.state == "making" {
		s.lastOnDemand[mobileBriefReasonPhone] = s.now()
		s.phoneRequest = nil
	}
	s.mu.Unlock()
	s.notifyTodayBriefRequests(todayRequests, err == nil && ctx.Err() == nil)
	s.notifyPhoneToday()
	s.announceBuilding(false)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if errors.Is(err, errRunnerNotInitialized) {
			// The model runner has not started: wait for it, as for sign-in.
			// Nothing is recorded and the next pass tries again.
			log.Printf("[mobile-brief] waiting for the model runner before building the %s brief", slot)
			return
		}
		if found {
			s.recordFailure(occurrence, err)
		} else {
			s.retryLater(occurrence.BriefID, slot, 1, err)
		}
		return
	}
	// A build with no written words, or one that kept a made-ahead copy, does not
	// fill the slot; retry it on the normal schedule (not every tick).
	if found && (!mobileBriefHasWrittenText(brief) || mobileBriefIsAhead(brief)) {
		s.noteNoTextBuild(s.now(), occurrence.BriefID)
	}
	if found {
		// A rebuild can retain an existing stored brief instead of saving one.
		if !recorded {
			recordCompleted(brief)
		}
		if s.app != nil {
			s.app.emitBrowserEvent("brief:stored", brief.BriefID)
		}
	}
}

// notifyTodayBriefRequests reports only the outcome of the generation that
// owned each request; stored-brief events also fire for later audio updates.
func (s *mobileBriefService) notifyTodayBriefRequests(tokens []string, success bool) {
	if s.app == nil {
		return
	}
	for _, token := range tokens {
		s.app.emitBrowserEvent("today:brief-request-finished", token, success)
	}
}

// mobileBriefBuildingEvent tells Today a brief started (true) or stopped
// (false) being built, after Building already says so. Today follows it
// instead of polling while it waits.
const mobileBriefBuildingEvent = "brief:building"

func (s *mobileBriefService) announceBuilding(building bool) {
	if s.app != nil {
		s.app.emitBrowserEvent(mobileBriefBuildingEvent, building)
	}
}

// generateRefenced builds a brief, collecting again at once when its sources
// changed during collection (errMobileBriefSourcesChanged). Background writes
// to the databases and files the brief reads are common while the user works,
// and the fence must still reject every one of them, so a build that saw one
// starts over instead of waiting for the next retry.
func (s *mobileBriefService) generateRefenced(ctx context.Context, slot, reason string, generation *MobileBriefGeneration) (MobileBriefV1, error) {
	return s.generateRefencedOnStored(ctx, slot, reason, generation, nil)
}

func (s *mobileBriefService) generateRefencedOnStored(ctx context.Context, slot, reason string, generation *MobileBriefGeneration, onStored func(MobileBriefV1)) (MobileBriefV1, error) {
	var brief MobileBriefV1
	var err error
	for try := 0; try < mobileBriefSourceRetries; try++ {
		brief, err = s.generateWithMetadataOnStored(ctx, slot, reason, generation, onStored)
		if !errors.Is(err, errMobileBriefSourcesChanged) || ctx.Err() != nil {
			return brief, err
		}
	}
	return brief, err
}

// mobileBriefFailureKind names why a build failed, for the log: never content.
func mobileBriefFailureKind(err error) string {
	switch {
	case errors.Is(err, errMobileBriefSourcesChanged):
		return "sources_changed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, errMobileBriefStateUnavailable):
		return "state_unavailable"
	default:
		return "build_error"
	}
}

// retryLater waits before the next try of a failed slot, longer after each
// failure in a row (mobileBriefRetryBackoff), and logs why it failed.
// Sources that changed during collection are a background write, not a
// broken brief: the first such failure in a row is retried at once.
func (s *mobileBriefService) retryLater(briefID, slot string, attempts int, err error) {
	delay := mobileBriefRetryBackoff(attempts)
	s.mu.Lock()
	now := false
	if !errors.Is(err, errMobileBriefSourcesChanged) {
		s.sourceRetriedFor = ""
	} else if s.sourceRetriedFor != briefID {
		s.sourceRetriedFor, delay, now = briefID, 0, true
	}
	s.retryAfter, s.retryFor = s.now().Add(delay), briefID
	s.mu.Unlock()
	if now {
		log.Printf("[mobile-brief] scheduled %s brief failed (%s, attempt %d): %s; retrying now", slot, mobileBriefFailureKind(err), attempts, safeLogError(err))
		select {
		case s.wake <- struct{}{}:
		default:
		}
		return
	}
	log.Printf("[mobile-brief] scheduled %s brief failed (%s, attempt %d): %s; retrying in %s", slot, mobileBriefFailureKind(err), attempts, safeLogError(err), delay)
}

// Building reports whether a brief is being built now.
func (s *mobileBriefService) Building() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// PatchLatestMobileBrief changes the newest stored brief through fn and
// stores it again when fn says so.
func (s *mobileBriefService) PatchLatestMobileBrief(fn func(MobileBriefV1) (MobileBriefV1, bool), after func(MobileBriefV1)) (MobileBriefV1, bool) {
	if !s.beginWork() {
		return MobileBriefV1{}, false
	}
	defer s.wg.Done()
	s.patchMu.Lock()
	latest, ok := s.store.latest()
	if !ok {
		s.patchMu.Unlock()
		return MobileBriefV1{}, false
	}
	patched, changed := fn(latest)
	if !changed {
		s.patchMu.Unlock()
		return latest, false
	}
	asOf, err := time.Parse(time.RFC3339Nano, patched.AsOf)
	if err != nil {
		asOf = s.now()
	}
	if err := s.store.save(patched, asOf); err != nil {
		s.patchMu.Unlock()
		log.Printf("[mobile-brief] the %s brief could not be updated", patched.Slot)
		return latest, false
	}
	s.patchMu.Unlock()
	s.saveFinishedRead(patched)
	s.deliverStoredBrief(patched, after)
	return patched, true
}

// deliverStoredBrief preserves delivery order without holding patchMu across
// a network send. Re-read after waiting, so queued publications include any
// newer audio/finds/Today patch and never replace a newly generated document.
func (s *mobileBriefService) deliverStoredBrief(brief MobileBriefV1, send func(MobileBriefV1)) {
	if send == nil {
		return
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	stored, ok := s.store.load(brief.BriefID)
	if ok && stored.AsOf == brief.AsOf {
		if s.app != nil {
			stored = s.app.mobileBriefForDelivery(stored)
		}
		send(stored)
	}
}

// slotOccurrence finds today's scheduled time for a slot.
func (s *mobileBriefService) slotOccurrence(now time.Time, settings mobileBriefSettings, slot string) (mobileBriefSlotOccurrence, bool) {
	for _, occurrence := range mobileBriefTodaySlots(now, s.location, settings) {
		if occurrence.Slot == slot {
			return occurrence, true
		}
	}
	return mobileBriefSlotOccurrence{}, false
}

// recordRun stores a slot's outcome. Storage trouble is logged once per run
// and never stops the brief.
func (s *mobileBriefService) recordRun(run mobileBriefRun) mobileBriefRun {
	stored, err := s.runs.record(run)
	if err != nil {
		log.Printf("[mobile-brief] could not record the %s run", run.Slot)
		return run
	}
	return stored
}

// recordFailure counts one failed build of a slot and schedules the next try.
// Failures never announce the slot missed: that waits until its window closes
// (noteMissed), so a brief that fails at 07:00 can still arrive at 09:00.
func (s *mobileBriefService) recordFailure(occurrence mobileBriefSlotOccurrence, err error) {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(err.Error())))
	if kind := mobileBriefFailureKind(err); kind != "build_error" {
		key = kind
	}
	cause := ""
	assistant := ""
	switch true {
	case errors.Is(err, context.DeadlineExceeded):
		cause = "timeout"
	case handoffRunAuthFailed(err):
		var routed *mobileBriefAssistantError
		if errors.As(err, &routed) {
			assistant = routed.assistant
		}
		if assistant != "" {
			cause = "signed_out"
		}
	}
	text := briefFailureText(err)
	s.mu.Lock()
	first := !s.loggedFailureKeys[key]
	if s.loggedFailureKeys == nil {
		s.loggedFailureKeys = map[string]bool{}
	}
	s.loggedFailureKeys[key] = true
	s.mu.Unlock()
	if first {
		log.Printf("[mobile-brief] %s brief failed (key %s): %s", occurrence.Slot, key[:min(len(key), 12)], text)
	}
	stored := s.recordRun(mobileBriefRun{BriefID: occurrence.BriefID, Slot: occurrence.Slot,
		ScheduledAt: formatMobileBriefTime(occurrence.ScheduledAt), Status: mobileBriefRunFailed, At: formatMobileBriefTime(s.now()), FailureKey: key, FailureText: text, FailureCause: cause, FailureAssistant: assistant})
	if cause == "signed_out" {
		// The route may have used a cached signed-in probe immediately before
		// authentication failed. Do not use that stale result to resume.
		if s.app != nil {
			s.app.mu.Lock()
			probes := s.app.assistantProbes
			s.app.mu.Unlock()
			if probes != nil {
				probes.invalidate()
			}
		}
		s.mu.Lock()
		s.retryAfter, s.retryFor = time.Time{}, ""
		s.mu.Unlock()
	} else {
		s.retryLater(occurrence.BriefID, occurrence.Slot, stored.Attempts, err)
	}
	if s.app != nil {
		s.app.emitBrowserEvent("mobile:updated")
		s.app.emitBrowserEvent(mobileBriefBuildingEvent, false)
	}
}

// briefSignInAssistant reads the persisted blocker without probing.
func (s *mobileBriefService) briefSignInAssistant(briefID string) string {
	for _, run := range s.runs.list() {
		if run.BriefID == briefID && run.Status == mobileBriefRunFailed && run.FailureCause == "signed_out" {
			return run.FailureAssistant
		}
	}
	return ""
}

// briefWaitingForSignIn holds the failed slot across restarts. A successful
// probe admits one retry; another authentication failure waits for a fresh
// signed-out observation before it can be admitted again.
func (s *mobileBriefService) briefWaitingForSignIn(ctx context.Context, briefID string) bool {
	assistant := s.briefSignInAssistant(briefID)
	if assistant == "" {
		return false
	}
	state := ""
	if s.assistantState != nil {
		state = s.assistantState(ctx, assistant)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authResumed == nil {
		s.authResumed = map[string]bool{}
	}
	if state == "signed_out" {
		delete(s.authResumed, assistant)
	}
	if state != "signed_in" || s.authResumed[assistant] {
		return true
	}
	return false
}

// retryHeldLocked reports that a failed try of briefID is still waiting out
// its backoff. Guarded by mu.
func (s *mobileBriefService) retryHeldLocked(now time.Time, briefID string) bool {
	return now.Before(s.retryAfter) && s.retryFor == briefID
}

// noteMissed records each slot whose catch-up window closed without a brief,
// logs it once per process, and tells the user once per slot. A slot already
// recorded as missed is left alone, so the run file is not rewritten on every
// tick.
func (s *mobileBriefService) noteMissed(now time.Time, missed []mobileBriefSlotOccurrence) {
	if len(missed) == 0 {
		return
	}
	recorded := map[string]mobileBriefRun{}
	for _, run := range s.runs.list() {
		recorded[run.BriefID] = run
	}
	for _, occurrence := range missed {
		s.mu.Lock()
		logged := s.missedLogged[occurrence.BriefID]
		s.missedLogged[occurrence.BriefID] = true
		s.mu.Unlock()
		if !logged {
			log.Printf("[mobile-brief] a scheduled brief missed its delivery window")
		}
		s.mu.Lock()
		awake := s.awakeSlots[occurrence.BriefID]
		s.mu.Unlock()
		if existing, ok := recorded[occurrence.BriefID]; ok && existing.Status == mobileBriefRunMissed {
			if !awake || existing.Attempts > 0 {
				s.notifyMissed(existing)
			}
			continue
		}
		reason := ""
		if !awake {
			reason = s.missedReason(occurrence.ScheduledAt)
		}
		stored := s.recordRun(mobileBriefRun{BriefID: occurrence.BriefID, Slot: occurrence.Slot,
			ScheduledAt: formatMobileBriefTime(occurrence.ScheduledAt), Status: mobileBriefRunMissed, At: formatMobileBriefTime(now), Reason: reason})
		// The notice is for a brief the computer slept through, or one whose
		// builds kept failing until the next slot: one that was awake with no
		// failed try (a brief made on request stood in) is neither.
		if !awake || stored.Attempts > 0 {
			s.notifyMissed(stored)
		}
	}
}

// missedReason says why a slot scheduled at at didn't run, only from what
// this process observed: a sleep window around the slot's time (asleep), or
// the service starting after it (closed). Otherwise it is empty: Isoform
// doesn't guess.
func (s *mobileBriefService) missedReason(at time.Time) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, window := range s.wakeWindows {
		if !at.Before(window.SleepAt) && at.Before(window.WakeAt) {
			return mobileBriefMissedAsleep
		}
	}
	if s.sleeping && !s.sleepAt.IsZero() && !at.Before(s.sleepAt) {
		return mobileBriefMissedAsleep
	}
	if !s.startedAt.IsZero() && s.startedAt.After(at) {
		return mobileBriefMissedClosed
	}
	return ""
}

// catchUpAllowed reports whether a slot scheduled at at may still be made now:
// never one from before the schedule was turned on, and not late one from
// before Isoform first watched the schedule (a fresh install is not greeted
// with a late brief).
func (s *mobileBriefService) catchUpAllowed(at, now time.Time, settings mobileBriefSettings) bool {
	if enabledAt, err := time.Parse(time.RFC3339Nano, settings.EnabledAt); err == nil && at.Before(enabledAt) {
		return false
	}
	seen, ok := s.runs.seenAt()
	return !ok || !at.Before(seen) || now.Sub(at) <= mobileBriefLateAfter
}

// scheduleFacts is what checkDue reads to decide whether a slot runs.
func (s *mobileBriefService) scheduleFacts(now time.Time, settings mobileBriefSettings) mobileBriefScheduleFacts {
	return mobileBriefScheduleFacts{
		Exists:         s.store.exists,
		StandsIn:       s.briefSince,
		MadeJustBefore: s.briefJustBefore,
		CatchUpAllowed: func(at time.Time) bool { return s.catchUpAllowed(at, now, settings) },
		RetryAt: func(briefID string) time.Time {
			if s.briefSignInAssistant(briefID) != "" {
				return now.Add(48 * time.Hour)
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.retryFor != briefID {
				return time.Time{}
			}
			return s.retryAfter
		},
	}
}

// slotNextRun is when the scheduler next makes slot (mobileBriefSlotNextRun,
// or the adaptive prediction for that slot). catchUp says it is today's late
// slot. Tasks, the brief's detail and the phone's Scheduled read it.
func (s *mobileBriefService) slotNextRun(now time.Time, settings mobileBriefSettings, slot string) (at time.Time, catchUp bool) {
	if !settings.Enabled {
		return time.Time{}, false
	}
	if settings.Adaptive {
		next, found := s.nextAdaptivePredictionFor(now, settings, slot)
		if !found {
			return time.Time{}, false
		}
		return mobileBriefReadyAt(next, now, s.location, settings), !next.ScheduledAt.After(now)
	}
	return mobileBriefSlotNextRun(now, s.location, settings, slot, s.scheduleFacts(now, settings))
}

// onRequestBriefBetween finds the first stored brief made on request (Make it
// now, or asked from the phone) at or after from and before to.
func (s *mobileBriefService) onRequestBriefBetween(from, to time.Time) (string, time.Time, bool) {
	briefID, first := "", time.Time{}
	for _, brief := range s.store.briefs() {
		if brief.Slot != mobileBriefSlotOnRequest {
			continue
		}
		at := mobileBriefAsOfTime(brief)
		if at.IsZero() || at.Before(from) || !at.Before(to) || (!first.IsZero() && !at.Before(first)) {
			continue
		}
		briefID, first = brief.BriefID, at
	}
	return briefID, first, briefID != ""
}

// slotHasRequestedBrief reports an on-request brief made in this slot's
// delivery window. A prior scheduled brief does not suppress wake recovery.
func (s *mobileBriefService) slotHasRequestedBrief(occurrence mobileBriefSlotOccurrence, settings mobileBriefSettings) bool {
	_, _, found := s.onRequestBriefBetween(occurrence.ScheduledAt, mobileBriefSlotCloses(occurrence, s.location, settings))
	return found
}

// briefSince reports a stored brief made at or after at.
func (s *mobileBriefService) briefSince(at time.Time) bool {
	latest, found := s.store.latest()
	if !found || !mobileBriefStandsIn(latest) {
		return false
	}
	asOf, err := time.Parse(time.RFC3339Nano, latest.AsOf)
	return err == nil && !asOf.Before(at)
}

// briefJustBefore reports the newest stored brief was made in the two hours
// before at (mobileBriefMadeJustBefore).
func (s *mobileBriefService) briefJustBefore(at time.Time) bool {
	latest, found := s.store.latest()
	return found && mobileBriefStandsIn(latest) && mobileBriefMadeJustBefore(mobileBriefAsOfTime(latest), at)
}

// noteAwake records the slots whose window is open now, while the computer
// is awake (mobileBriefSlotCloses).
func (s *mobileBriefService) noteAwake(now time.Time, settings mobileBriefSettings) {
	loc := s.location
	if loc == nil {
		loc = time.Local
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.awakeSlots == nil {
		s.awakeSlots = map[string]bool{}
	}
	for _, day := range []int{-1, 0} {
		for _, occurrence := range mobileBriefTodaySlots(now.In(loc).AddDate(0, 0, day), loc, settings) {
			if !now.Before(occurrence.ScheduledAt) && now.Before(mobileBriefSlotCloses(occurrence, loc, settings)) {
				s.awakeSlots[occurrence.BriefID] = true
			}
		}
	}
}

// notifyMissed submits the brief_missed moment once per slot.
func (s *mobileBriefService) notifyMissed(run mobileBriefRun) {
	if run.Notified || s.app == nil {
		return
	}
	if !s.app.produceBriefMissed(run.BriefID, run.Slot, s.briefMissedDetails(run)) {
		return
	}
	if err := s.runs.markNotified(run.BriefID); err != nil {
		log.Printf("[mobile-brief] could not record the missed-brief notice for the %s slot", run.Slot)
	}
}

// briefMissedDetails names a reason only the record proves (the computer
// was asleep, Isoform was closed, or its builds failed) and the time the
// slot was due.
func (s *mobileBriefService) briefMissedDetails(run mobileBriefRun) map[string]string {
	details := map[string]string{}
	switch {
	case run.Reason != "":
		details["reason"] = run.Reason
	case run.Attempts > 0:
		details["reason"] = "failed"
	}
	if at, err := time.Parse(time.RFC3339, run.ScheduledAt); err == nil {
		loc := s.location
		if loc == nil {
			loc = time.Local
		}
		details["time"] = at.In(loc).Format("15:04")
	}
	return details
}

// Health returns last run, next run, missed runs and the failing slot.
func (s *mobileBriefService) Health() MobileBriefHealth {
	settings := s.config.load()
	latest, ok := s.store.latest()
	now := s.now()
	view := s.withPending(mobileBriefHealthView(now, s.location, settings, latest, ok, s.runs.list()), now, settings, latest, ok)
	return s.withNextRun(view, now, settings)
}

// withNextRun names the next brief by the time it will be ready: the adaptive
// prediction's ready time, or the scheduled slot. Every reader of the health view
// (Today's launch read, Phone, Tasks) goes through here, so they agree (R64).
func (s *mobileBriefService) withNextRun(view MobileBriefHealth, now time.Time, settings mobileBriefSettings) MobileBriefHealth {
	// A switched-off schedule names no next brief, so nothing stale survives.
	view.NextRunAt, view.NextSlot = "", ""
	if settings.Enabled && !settings.Adaptive {
		if next, found := mobileBriefScheduledNextRun(now, s.location, settings, s.scheduleFacts(now, settings)); found {
			view.NextRunAt, view.NextSlot = formatMobileBriefTime(next.ScheduledAt), next.Slot
		}
	}
	if settings.Enabled && settings.Adaptive {
		if next, found := s.nextAdaptivePrediction(now, settings); found {
			view.NextRunAt, view.NextSlot = formatMobileBriefTime(mobileBriefReadyAt(next, now, s.location, settings)), next.Slot
		}
	}
	return view
}

// withPending adds today's slot that is on its way (mobileBriefPendingOccurrence)
// and, after a failed try, when the next try runs. latest is the newest
// stored brief, never the Today snapshot.
func (s *mobileBriefService) withPending(view MobileBriefHealth, now time.Time, settings mobileBriefSettings, latest MobileBriefV1, ok bool) MobileBriefHealth {
	pending, found := mobileBriefPendingOccurrence(now, s.location, settings, latest, ok, func(at time.Time) bool {
		return s.catchUpAllowed(at, now, settings)
	})
	if !found {
		return view
	}
	view.PendingSlot = pending.Slot
	view.PendingAt = formatMobileBriefTime(pending.ScheduledAt)
	for _, run := range s.runs.list() {
		if run.BriefID == pending.BriefID && run.Status == mobileBriefRunFailed {
			view.PendingAttempts = run.Attempts
			view.PendingCause = run.FailureCause
			view.PendingAssistant = run.FailureAssistant
		}
	}
	s.mu.Lock()
	retryAfter, retryFor := s.retryAfter, s.retryFor
	s.mu.Unlock()
	// A try after the window closes never runs: the next slot takes over
	// (today-review v1.1.21 R49).
	if retryAfter.After(now) && retryFor == pending.BriefID && retryAfter.Before(mobileBriefSlotCloses(pending, s.location, settings)) {
		view.RetryAt = formatMobileBriefTime(retryAfter)
	}
	return view
}

// windowStart is the end of the previous brief, or 24 hours before asOf when
// there is none. The window never exceeds seven days.
func (s *mobileBriefService) windowStart(asOf time.Time) time.Time {
	fallback := asOf.Add(-mobileBriefDefaultWindow)
	var latest MobileBriefV1
	ok := false
	for _, candidate := range s.store.briefs() {
		// A made-ahead copy is provisional, and a build with no words failed to
		// cover its window: the next build reads from the last brief with words
		// (a "skipped" brief covered its window by design).
		if candidate.NarrativeStatus != "quiet" && !mobileBriefIsAhead(candidate) && (mobileBriefHasWrittenText(candidate) || candidate.NarrativeStatus == "skipped") {
			latest, ok = candidate, true
			break
		}
	}
	if !ok {
		return fallback
	}
	previous, err := time.Parse(time.RFC3339Nano, latest.AsOf)
	if err != nil || !previous.Before(asOf) {
		return fallback
	}
	floor := asOf.Add(-mobileBriefMaxWindow)
	if previous.Before(floor) {
		return floor
	}
	return previous
}

// generate builds, stores and delivers one brief. It holds a power assertion
// for the whole run and logs only the slot and the narrative status.
func (s *mobileBriefService) generate(ctx context.Context, slot, reason string) (MobileBriefV1, error) {
	return s.generateWithMetadata(ctx, slot, reason, nil)
}

func (s *mobileBriefService) generateWithMetadata(ctx context.Context, slot, reason string, generation *MobileBriefGeneration) (MobileBriefV1, error) {
	return s.generateWithMetadataOnStored(ctx, slot, reason, generation, nil)
}

func (s *mobileBriefService) generateWithMetadataOnStored(ctx context.Context, slot, reason string, generation *MobileBriefGeneration, onStored func(MobileBriefV1)) (MobileBriefV1, error) {
	useEpoch := s.app.contextUseEpoch()
	asOf := s.now()
	if s.app != nil {
		s.app.releaseOpenAnswerHolds(ctx, asOf, true)
	}
	windowStart := s.windowStart(asOf)
	release := s.holdPower(reason)
	defer release()

	if slot == mobileBriefSlotEvening && s.app != nil {
		s.app.fillThreadOutcomes(ctx, asOf, s.location)
	}
	if s.app != nil && s.app.engagement != nil {
		// Weekly projections can include these receipts, but their readers
		// must not write after the collection fence starts.
		s.app.engagement.inferCompletedReceipts(ctx, s.app.engagement.now())
	}
	// Fence the callback itself: it can read a record that is corrected before
	// it returns. Capturing only after building would bless that stale text.
	var gathered *mobileBriefGatherRevision
	if s.app != nil && s.app.store != nil {
		var err error
		gathered, err = s.app.readMobileBriefGatherRevision(ctx)
		if err != nil {
			return MobileBriefV1{}, withLogStage("source fence", err)
		}
		defer gathered.close()
	}
	gatherCtx := context.WithValue(ctx, mobileBriefGatherContextKey{}, true)
	if gathered != nil && gathered.memory != nil {
		gatherCtx = context.WithValue(gatherCtx, mobileBriefPinnedMemoryKey{}, gathered.memory)
	}
	input, err := s.gather(gatherCtx, slot, windowStart, asOf)
	if err != nil {
		return MobileBriefV1{}, withLogStage("gather", err)
	}
	// An early brief is about its slot's day; every other brief uses its as-of day.
	if day, ok := ctx.Value(mobileBriefAheadDayKey{}).(time.Time); ok {
		input.BriefDay = day
	}
	// The brief's local date is stamped in the service's zone, the one its
	// schedule and Today read, whatever zone the sources came with.
	if s.location != nil && input.Location != s.location {
		input.Location = s.location
		input.Timezone = mobileBriefTimezoneName(s.location)
	}
	if slot == mobileBriefSlotMorning {
		input.YesterdayActivity = s.app.mobileBriefYesterdayActivityFor(ctx, input.AsOf, input.briefDayRef(input.Location), input.Location)
	}
	input.PreviousBriefs = s.store.briefs()
	input = mobileBriefWithoutExcluded(input, s.app.mobileBriefExclusions())
	brief := buildMobileBriefV1(input)
	// Cited as collected, before the read gates drop a source that left.
	gathered.cite(brief)
	brief.DisplaySlot = mobileBriefDisplaySlot(brief, s.config.load())
	if generation != nil {
		generation.GeneratedAt = brief.AsOf
		generation.Message = mobileBriefLateMessage(slot, s.app.currentUILanguage())
		brief.Generation = generation
	}
	if s.app != nil {
		// The freshest finds on the user's topics lead the watching section.
		brief = s.app.topicFindsForBrief(brief, asOf)
	}
	if err := ctx.Err(); err != nil {
		return MobileBriefV1{}, err
	}

	if s.app != nil {
		brief = s.app.mobileBriefForGeneration(brief)
	}
	// Existing composer overrides are test seams; production always uses the
	// ranked route for scheduled mornings.
	calendarOnlyFallback := s.compose == nil && s.composeWithLead == nil && len(mobileBriefPicks(brief)) == 0 && len(mobileBriefDayItems(brief)) > 0 && len(mobileBriefNarrativeTemplateOnly(brief, s.app.currentUILanguageName())) == 0
	// The ranked writer serves the scheduled morning and evening (the evening
	// reflects back; rankedPartsBalance sets its larger part).
	rankedMorning := (slot == mobileBriefSlotMorning || slot == mobileBriefSlotEvening) && s.compose == nil && s.composeWithLead == nil && !calendarOnlyFallback
	var modelBinding *mobileBriefModelBinding
	if !rankedMorning && !calendarOnlyFallback {
		modelBinding = s.app.captureMobileBriefModelSources(ctx, brief, gathered)
	}
	if gathered != nil {
		if changed := s.app.mobileBriefGatherChange(ctx, gathered); changed != "" {
			log.Printf("[mobile-brief] %s brief sources changed during collection (%s): %s", slot, time.Since(time.UnixMilli(gathered.startedMs)).Round(time.Second), changed)
			return MobileBriefV1{}, errMobileBriefSourcesChanged
		}
	}
	if input.attendanceCache != nil {
		s.app.saveMobileBriefAttendanceCache(ctx, *input.attendanceCache)
	}
	// The cards are written in the same pass as the brief, so their words
	// share its goal; Isoform's own sentences stand in for any the model
	// does not write well (mobile_brief_cards.go).
	language := s.app.currentUILanguageName()
	if !rankedMorning {
		_, brief.Offers = mobileBriefClosing(brief, language)
	}
	cards := mobileBriefPatternCards(brief, language)
	var paragraphs []MobileBriefPara
	var status string
	var words []mobileBriefCardWords
	var rankedCards []MobileBriefCard
	var weekLead, weekLeadFacts string
	if rankedMorning {
		var draft morningBriefDraft
		original := brief
		brief, modelBinding, draft, err = s.composeRankedMorning(ctx, input, brief, gathered)
		if err != nil {
			if !mobileBriefWriteJudgeFailed(err) || ctx.Err() != nil || errors.Is(err, errMobileBriefSourcesChanged) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return MobileBriefV1{}, withLogStage("ranked morning", err)
			}
			log.Printf("[mobile-brief] ranked morning writing failed (%s); using on-request composer", safeLogError(err))
			brief, rankedMorning = original, false
			modelBinding = s.app.captureMobileBriefModelSources(ctx, brief, gathered)
			_, brief.Offers = mobileBriefClosing(brief, language)
			cards = mobileBriefPatternCards(brief, language)
			paragraphs, status, words, weekLead, weekLeadFacts = composeMobileBriefNarrativeWithLeadAt(ctx, s.app, brief, cards, s.now)
		} else {
			paragraphs, status = draft.Paragraphs, "ok"
			rankedCards = draft.Cards
		}
	} else if s.composeWithLead != nil {
		paragraphs, status, words, weekLead, weekLeadFacts = s.composeWithLead(ctx, s.app, brief, cards)
	} else if s.compose != nil {
		paragraphs, status, words = s.compose(ctx, s.app, brief, cards)
	} else {
		paragraphs, status, words, weekLead, weekLeadFacts = composeMobileBriefNarrativeWithLeadAt(ctx, s.app, brief, cards, s.now)
	}
	brief.Narrative = paragraphs
	brief.NarrativeStatus = status
	brief.WeekLead, brief.WeekLeadFacts = weekLead, weekLeadFacts
	if !rankedMorning {
		_, brief.Offers = mobileBriefClosing(brief, language)
	}
	// Composition can overlap an edit. Retry before later navigation reads
	// refresh their own topic state.
	if gathered != nil {
		if changed := s.app.mobileBriefGatherChange(ctx, gathered); changed != "" {
			log.Printf("[mobile-brief] %s brief sources changed during composition: %s", slot, changed)
			return MobileBriefV1{}, errMobileBriefSourcesChanged
		}
	}
	if s.app != nil && s.app.topicSource != nil {
		_, _ = s.app.topicsSvc().registryViews(ctx)
	}
	// Reuse this post-model source read for navigation and the final binding.
	// Collection must finish before patchMu: slow stores cannot block patches.
	var sources todayChatSources
	if s.app != nil && len(brief.Narrative) > 0 && (modelBinding != nil || s.app.topicSource != nil && len(s.app.topicsSvc().store.load().ChatTopics) > 0) {
		sources = s.app.collectTodayChatSources(ctx, time.Now())
	}
	brief = s.app.mobileBriefWithParagraphTopicsFrom(ctx, brief, sources)
	if rankedMorning {
		brief.Cards, brief.Offers = rankedCards, nil
		// The delivery gate rejects unbound ranked prose, so bind it before
		// its first delivery projection as well as before publication.
		if !mobileBriefStaticEmpty(brief) {
			s.app.finishMobileBriefModelBindingFrom(ctx, &brief, modelBinding, sources)
			if brief.modelBinding == nil {
				return MobileBriefV1{}, errMobileBriefSourcesChanged
			}
		}
	} else {
		brief.Cards = mobileBriefApplyCardWords(brief, mobileBriefPatternCards(brief, language), words, language)
	}
	if s.app != nil {
		// Filter the newly written card words too, then restore navigation from
		// this request's already validated links. No second collection is needed.
		brief = s.app.mobileBriefForGeneration(brief)
		brief = s.app.mobileBriefWithParagraphTopicsFrom(ctx, brief, sources)
	}
	if err := ctx.Err(); err != nil {
		return MobileBriefV1{}, err
	}
	s.patchMu.Lock()
	if reason == mobileBriefReasonAdaptive && !s.adaptiveDeliveryAllowed(asOf, slot) {
		s.patchMu.Unlock()
		return MobileBriefV1{}, errMobileBriefTimingDeferred
	}
	if s.app != nil {
		// A search may have finished during the narrative: take its finds.
		brief = s.app.topicFindsForBrief(brief, asOf)
	}
	brief = mobileBriefWithDisplayTimes(brief, language)
	// A greeting never stands as a paragraph of its own (PROTOCOL.md 8.1).
	// Join it before the synthesis is bound and the voice reads it.
	brief = mobileBriefWithJoinedGreeting(brief)
	brief = mobileBriefWithOwnVoice(brief)
	s.app.finishMobileBriefModelBindingFrom(ctx, &brief, modelBinding, sources)
	brief = mobileBriefForPublication(brief)
	// Over-long model text is trimmed to whole sentences after the word floor has judged the
	// whole reply, so it is never silenced for its length; what is stored is what is shown and
	// spoken (design R74 v1.0.1).
	brief.Narrative = mobileBriefTrimParagraphs(brief.Narrative)
	if s.app != nil && !mobileBriefStaticEmpty(brief) {
		s.app.appendOpenAnswerBriefParagraph(ctx, &brief, windowStart, asOf, language)
	}
	// The part counts follow the final narrative, so they are set last.
	brief = mobileBriefWithNarrativeParts(brief)
	// A stored copy with written words is kept over a build with no words, and a
	// made-ahead build never replaces a final copy with words. A made-ahead copy
	// with words is replaced by a real build (below), and a remake replaces a
	// made-ahead copy with words only when it is complete.
	if stored, found := s.store.load(brief.BriefID); found && mobileBriefHasWrittenText(stored) && (!mobileBriefHasWrittenText(brief) || mobileBriefIsAhead(brief) && !mobileBriefIsAhead(stored)) {
		s.patchMu.Unlock()
		log.Printf("[mobile-brief] kept the stored %s brief: the rebuild has no written text or is made ahead", brief.Slot)
		return stored, nil
	}
	// Delivery projection reads completed run records, but this run is not
	// recorded until after generation returns. Keep the observed timing on the
	// stored brief; every delivery still rechecks the matching run record.
	if generation != nil {
		captured := *generation
		brief.Generation = &captured
	}
	if brief.generationDebug != nil {
		counts := *brief.generationDebug
		counts.EvidenceByKind = mobileBriefEvidenceCounts(brief.Evidence)
		brief.generationDebug = &counts
	}
	firstReadBriefMu.Lock()
	// The copy is sealed after patchMu is released (below), so the save does not
	// announce or seal under the lock.
	// An awake slot's brief replaces the early brief stored under its ID (R40).
	prior, hadPrior := s.store.load(brief.BriefID)
	replacedAhead := hadPrior && mobileBriefIsAhead(prior) && !mobileBriefIsAhead(brief)
	// A remake of a held early brief sends the phone no second notice either: its push is already due at the slot.
	remadeAhead := hadPrior && mobileBriefIsAhead(prior) && mobileBriefIsAhead(brief)
	err = s.store.saveDocument(brief, asOf)
	s.patchMu.Unlock()
	if err == nil && replacedAhead {
		logMobileBriefAhead("slot", brief.BriefID, true)
		if s.runs != nil {
			if markErr := s.runs.markEarlyReplaced(brief.BriefID); markErr != nil {
				log.Printf("[mobile-brief] the early run could not record its replacement")
			}
		}
	}
	if err != nil {
		firstReadBriefMu.Unlock()
		return MobileBriefV1{}, err
	}
	// Delivery projects timing from the run ledger, so record a scheduled
	// completion only after the brief is saved and before its first projection.
	if onStored != nil {
		onStored(brief)
	}
	if reason == mobileBriefReasonSchedule {
		s.startBriefShadow(brief)
	}
	if brief.NarrativeStatus != "quiet" {
		s.saveFinishedRead(brief)
	}
	if brief.NarrativeStatus == "quiet" {
		firstReadBriefMu.Unlock()
		if s.app != nil {
			s.app.emitBrowserEvent("brief:stored", brief.BriefID)
			s.app.emitBrowserEvent("mobile:updated")
		}
		return brief, nil
	}
	// An early brief makes no desktop notice now; the phone's notice is due at
	// its slot. A brief replacing an early one sends the phone no second notice.
	ahead := mobileBriefIsAhead(brief)
	notify := false
	if s.app != nil {
		s.app.recordBriefContextUses(brief, useEpoch)
		if !ahead {
			notify = s.app.produceBriefReady(brief, reason != mobileBriefReasonPhone && reason != mobileBriefReasonWake, reason == mobileBriefReasonSettings)
		}
	}
	phoneNotify := mobileBriefPhoneNotify(ahead, replacedAhead || remadeAhead, notify)
	firstReadBriefMu.Unlock()
	if s.app != nil {
		s.deliverStoredBrief(brief, func(current MobileBriefV1) {
			if err := s.publish(current, phoneNotify); err != nil {
				log.Printf("[mobile-brief] paired-device delivery did not complete")
			}
		})
		s.app.emitBrowserEvent("brief:stored", brief.BriefID)
		s.app.emitBrowserEvent("mobile:updated")
		// The day's search for new finds follows the morning brief.
		if s.beginWork() {
			go func() {
				defer s.wg.Done()
				s.app.maybeStartDailyTopicFinds("brief")
			}()
		}
	}
	log.Print(mobileBriefLogSummary(brief))

	// The spoken version follows in a second step, so the text brief and its
	// notice never wait for synthesis, the encode or the upload. Stop cancels
	// a step still running; the text brief stays as stored.
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return brief, nil
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		s.addAudio(ctx, brief, asOf)
	}()
	return brief, nil
}

// backfillTodayAudio gives today's stored brief one fresh recording when none
// is bound to its readable text. Existing audio files are never adopted without
// synthesis: addAudio binds only the new reading and rechecks its sources.
func (s *mobileBriefService) backfillTodayAudio(ctx context.Context) {
	if s.app == nil || s.speaker.synthesize == nil || s.speaker.voiceReady == nil || s.app.IsClosedEyesModeEnabled() {
		return
	}
	brief, found := s.store.latest()
	if !found || brief.NarrativeStatus != "ok" {
		return
	}
	loc := s.location
	if loc == nil {
		loc = time.Local
	}
	if brief.LocalDate != s.now().In(loc).Format("2006-01-02") {
		return
	}
	// Refresh the sealed view after an exclusion before deciding what to read.
	if _, _, ok := s.seal(ctx, brief); !ok {
		return
	}
	delivered := s.app.mobileBriefForDelivery(brief)
	if delivered.NarrativeStatus != "ok" || len(delivered.Narrative) == 0 {
		return
	}
	lang := s.speaker.voiceLang()
	if audio := brief.Audio; audio != nil && audio.Status == mobileBriefAudioStatusOK && audio.Bytes > 0 && audio.binding == mobileBriefAudioBinding(delivered, lang) {
		return
	}
	asOf := mobileBriefAsOfTime(brief)
	if asOf.IsZero() {
		asOf = s.now()
	}
	// Read the brief as it reads now: once a memory change removes parts of
	// it, the recording is made again without them (today-review v1.1.10 B).
	delivered.Audio = brief.Audio
	s.addAudioWithLatestGuard(ctx, delivered, asOf, true)
}

// remakeAudioSoon makes today's recording again after a change that can
// remove parts of the brief (a stated exclusion). Until it is ready the brief
// has no player; one remake waits while another runs.
func (s *mobileBriefService) remakeAudioSoon() {
	s.mu.Lock()
	if s.stopped || s.runCtx == nil || s.audioRemakeQueued {
		s.mu.Unlock()
		return
	}
	s.audioRemakeQueued = true
	ctx := s.runCtx
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		s.mu.Lock()
		s.audioRemakeQueued = false
		s.mu.Unlock()
		s.backfillTodayAudio(ctx)
	}()
}

// addAudio reads the stored brief aloud, seals the file for paired phones,
// and saves the brief again with its audio. A missing voice starts downloading
// and the brief carries that status until the next brief can use the voice.
func (s *mobileBriefService) addAudio(ctx context.Context, brief MobileBriefV1, asOf time.Time) {
	s.addAudioWithLatestGuard(ctx, brief, asOf, false)
}

func (s *mobileBriefService) addAudioWithLatestGuard(ctx context.Context, brief MobileBriefV1, asOf time.Time, requireLatest bool) {
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	lang := s.speaker.voiceLang()
	// Start with the latest stored and privacy-projected text, including any
	// amendment made after this generation was sealed.
	initial, found := s.store.load(brief.BriefID)
	if !found || initial.AsOf != brief.AsOf {
		return
	}
	brief = initial
	if s.app != nil {
		shown, _, err := s.finishedReadWithholding(initial)
		if err != nil || mobileBriefAudioBinding(shown, lang) != mobileBriefAudioBinding(s.app.mobileBriefForDelivery(initial), lang) {
			return
		}
		brief = shown
	}
	input := mobileBriefAudioBinding(brief, lang)
	// Until the recording is saved, the stored brief says it is being made. Any path that does not save the
	// final audio clears that mark again (P138).
	saved := false
	marked := s.markAudioRendering(brief.BriefID, input, lang, asOf)
	defer func() {
		if marked && !saved {
			s.clearAudioRendering(brief.BriefID, asOf)
		}
	}()
	var audio mobileBriefAudioFile
	var ok bool
	for attempt := 0; ; attempt++ {
		var reason string
		audio, ok, reason = renderMobileBriefAudio(ctx, s.speaker, s.store.dir, brief)
		if !ok {
			if reason != "nothing to say" && reason != "no audio store" {
				log.Printf("[mobile-brief] %s brief has no spoken version: %s", brief.Slot, reason)
			}
			if audio.Audio.Status == "" {
				return
			}
		}
		// Rendering can overlap a Today refresh or source withdrawal. Recheck
		// before sealing/uploading bytes, then again before attaching them.
		stored, found := s.store.load(brief.BriefID)
		if !found {
			return
		}
		if s.app != nil {
			shown, _, err := s.finishedReadWithholding(stored)
			if err != nil || mobileBriefAudioBinding(shown, lang) != mobileBriefAudioBinding(s.app.mobileBriefForDelivery(stored), lang) {
				return
			}
			stored = shown
		}
		current := mobileBriefAudioBinding(stored, lang)
		if input == current {
			break
		}
		// What the brief says changed while the voice spoke (a row its
		// template reads, a withdrawn source): read it once more as it
		// reads now. A newer generation records its own audio.
		if attempt > 0 || ctx.Err() != nil || stored.AsOf != brief.AsOf {
			return
		}
		brief, input = stored, current
	}
	shaped := audio.Audio
	shaped.binding = input
	if ok && s.app != nil {
		if attachment, err := s.app.sealMobileBriefAudio(ctx, brief.BriefID, audio.Path); err == nil {
			shaped.Attachment = attachment
		} else if !errors.Is(err, errMobileAttachmentOffline) {
			log.Printf("[mobile-brief] audio stays on this Mac: %v", err)
		}
	}
	if ctx.Err() != nil {
		return
	}
	// The projection reads sources, so it runs before patchMu; the write under
	// the lock is valid only if the privacy state and the stored brief are the
	// ones it saw. A stored document replaced meanwhile (a fresh on-request
	// brief) leaves the audio with the older brief, which is left alone.
	pre, preFound := s.store.load(brief.BriefID)
	preEpoch := ""
	preCurrent := pre
	if s.app != nil {
		preEpoch = s.app.briefPrivacyPrint()
		if preFound {
			shown, _, err := s.finishedReadWithholding(pre)
			if err != nil || mobileBriefAudioBinding(shown, lang) != mobileBriefAudioBinding(s.app.mobileBriefForDelivery(pre), lang) {
				return
			}
			preCurrent = shown
		}
	}
	s.patchMu.Lock()
	stored, found := s.store.load(brief.BriefID)
	if s.app != nil && s.app.briefPrivacyPrint() != preEpoch {
		s.patchMu.Unlock()
		return
	}
	if !found || !preFound || stored.AsOf != pre.AsOf || mobileBriefAudioBinding(stored, lang) != mobileBriefAudioBinding(pre, lang) || input != mobileBriefAudioBinding(preCurrent, lang) {
		s.patchMu.Unlock()
		return
	}
	if requireLatest {
		latest, ok := s.store.latest()
		if !ok || latest.BriefID != brief.BriefID || latest.AsOf != brief.AsOf {
			s.patchMu.Unlock()
			return
		}
	}
	stored.Audio = &shaped
	// Saved without sealing under the lock: the copy is sealed after the unlock.
	if err := s.store.saveDocument(stored, asOf); err != nil {
		s.patchMu.Unlock()
		log.Printf("[mobile-brief] the spoken %s brief could not be recorded", brief.Slot)
		return
	}
	saved = true
	s.patchMu.Unlock()
	s.saveFinishedRead(stored)
	if s.app != nil {
		// Delivery may wait on the relay; local readers never wait on it.
		// No second notice: the phone re-reads on its next poll.
		s.deliverStoredBrief(stored, func(current MobileBriefV1) {
			if requireLatest {
				latest, ok := s.store.latest()
				if !ok || latest.BriefID != current.BriefID || latest.AsOf != current.AsOf {
					return
				}
			}
			if err := s.publish(current, false); err != nil {
				log.Printf("[mobile-brief] paired-device delivery of the spoken brief did not complete")
			}
		})
	}
	if s.app != nil {
		s.app.emitBrowserEvent("brief:stored", stored.BriefID)
		s.app.emitBrowserEvent("mobile:updated")
	}
	if ok && shaped.Attachment == nil {
		s.scheduleBriefAudioRetry(stored)
	}
	log.Printf("[mobile-brief] stored %s brief audio status: %s", brief.Slot, shaped.Status)
}

// mobileBriefAudioBinding binds a recording to what it says, in the voice's
// language: the brief's text as shown (its paragraphs, its weekly recap and
// the question that closes it), the sources each paragraph cites by identity,
// and the complete spoken script. Ref numbers, labels and the live rows a Today
// refresh adds are not part of it: they change on every refresh, and binding
// them kept every recording made from 2026-10-19 to 2026-10-21 from being
// attached although its file was written.
// markAudioRendering saves the stored brief with a rendering status before the voice speaks it, so a phone that
// reads the brief now shows "Recording on its way" instead of nothing. Audio that is already playable stays until
// the new recording replaces it. The mark is skipped when the brief changed since the audio was asked for.
func (s *mobileBriefService) markAudioRendering(briefID, input, lang string, asOf time.Time) bool {
	// Only a brief the voice can speak now is being made; a missing voice waits as "downloading".
	if s.speaker.voiceReady == nil || !s.speaker.voiceReady(lang) {
		return false
	}
	s.patchMu.Lock()
	defer s.patchMu.Unlock()
	stored, found := s.store.load(briefID)
	if !found || (stored.Audio != nil && stored.Audio.Status == mobileBriefAudioStatusOK) {
		return false
	}
	current := stored
	if s.app != nil {
		current = s.app.mobileBriefForDelivery(stored)
	}
	if input != mobileBriefAudioBinding(current, lang) {
		return false
	}
	stored.Audio = &MobileBriefAudio{Status: mobileBriefAudioStatusRendering}
	if err := s.store.saveDocument(stored, asOf); err != nil {
		log.Printf("[mobile-brief] the %s brief could not record that its audio is being made", stored.Slot)
		return false
	}
	return true
}

// clearAudioRendering removes a rendering mark that no finished recording replaced, so the brief never says a
// recording is being made after the attempt has ended.
func (s *mobileBriefService) clearAudioRendering(briefID string, asOf time.Time) {
	s.patchMu.Lock()
	defer s.patchMu.Unlock()
	stored, found := s.store.load(briefID)
	if !found || stored.Audio == nil || stored.Audio.Status != mobileBriefAudioStatusRendering {
		return
	}
	stored.Audio = nil
	if err := s.store.saveDocument(stored, asOf); err != nil {
		log.Printf("[mobile-brief] the %s brief could not clear its audio mark", stored.Slot)
	}
}

func mobileBriefAudioBinding(brief MobileBriefV1, lang string) string {
	evidence := mobileBriefEvidenceByRef(brief)
	said := func(paragraphs []MobileBriefPara) [][]string {
		out := make([][]string, 0, len(paragraphs))
		for _, paragraph := range paragraphs {
			line := []string{pii.DisplayModelText(paragraph.Text, lang)}
			for _, id := range paragraph.Refs {
				ref, ok := evidence[id]
				if !ok {
					line = append(line, "")
					continue
				}
				line = append(line, mobileBriefSourceKey(ref.SourceKind, ref.SourceID))
			}
			out = append(out, line)
		}
		return out
	}
	spoken := mobileBriefSpokenText(brief, lang)
	var closing string
	if len(brief.WeeklyRecap) > 0 && brief.YourWeek == nil {
		closing = pii.DisplayModelText(mobileBriefDeliveredClosing(brief, lang).Text, lang)
	}
	base := struct {
		ID, AsOf, Status  string
		Narrative, Weekly [][]string
		Spoken, Closing   string
	}{brief.BriefID, brief.AsOf, brief.NarrativeStatus, said(brief.Narrative), said(brief.WeeklyRecap), spoken, closing}
	if question := morningBriefWeekQuestionSpeech(brief.WeekQuestion, lang); question != "" {
		return lang + ":v2:" + mobileBriefModelDigest(struct {
			Base     any
			Question string
		}{base, question})
	}
	return lang + ":v2:" + mobileBriefModelDigest(base)
}

// mobileBriefAudioForRead is the recording a read may play: one bound to its
// text (since 2026-10-21) plays while the brief as read now still says
// exactly that, so a recording hidden for a while plays again once its text
// is back, and never once the text it speaks has changed or left.
func mobileBriefAudioForRead(brief MobileBriefV1) *MobileBriefAudio {
	if brief.Audio == nil {
		return nil
	}
	if brief.Audio.binding == "" {
		// An older recording cannot prove it matches the text as shown now.
		// Download/rendering statuses carry no playable recording.
		if brief.Audio.Bytes == 0 || brief.Audio.Status == mobileBriefAudioStatusDownloading || brief.Audio.Status == mobileBriefAudioStatusRendering {
			for _, paragraphs := range [][]MobileBriefPara{brief.Narrative, brief.WeeklyRecap} {
				for _, paragraph := range paragraphs {
					if pii.DisplayModelText(paragraph.Text, "en") != paragraph.Text {
						return nil
					}
				}
			}
			return brief.Audio
		}
		return nil
	}
	lang, _, _ := strings.Cut(brief.Audio.binding, ":")
	if brief.Audio.binding != mobileBriefAudioBinding(brief, lang) {
		return nil
	}
	return brief.Audio
}

// Audio returns the stored spoken brief or its download status for an id.
func (s *mobileBriefService) Audio(briefID string) (MobileBriefAudioData, bool) {
	brief, found := s.store.load(briefID)
	if !found || brief.Audio == nil {
		return MobileBriefAudioData{}, false
	}
	if s.app != nil {
		shown, _, err := s.finishedReadWithholding(brief)
		if err != nil || shown.Audio == nil || mobileBriefAudioForRead(shown) == nil || shown.Audio.binding != brief.Audio.binding || shown.Audio.SHA256 != brief.Audio.SHA256 {
			return MobileBriefAudioData{}, false
		}
	}
	if found && brief.Audio != nil && brief.Audio.Status == mobileBriefAudioStatusDownloading {
		return MobileBriefAudioData{
			BriefID:       briefID,
			Status:        brief.Audio.Status,
			StatusMessage: brief.Audio.StatusMessage,
		}, true
	}
	if !mobileBriefAudioReady(brief.Audio) {
		return MobileBriefAudioData{}, false
	}
	path, mime, ok := mobileBriefAudioPath(s.store.dir, briefID)
	if !ok || mime != brief.Audio.Mime {
		return MobileBriefAudioData{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return MobileBriefAudioData{}, false
	}
	if brief.Audio != nil && brief.Audio.SHA256 != "" && (len(data) != brief.Audio.Bytes || fmt.Sprintf("%x", sha256.Sum256(data)) != brief.Audio.SHA256) {
		return MobileBriefAudioData{}, false
	}
	out := MobileBriefAudioData{BriefID: briefID, Status: mobileBriefAudioStatusOK, Mime: mime, Base64: base64.StdEncoding.EncodeToString(data)}
	if found && brief.Audio != nil {
		out.DurationSeconds = brief.Audio.DurationSeconds
	} else if mime == mobileBriefAudioMimeWAV {
		if seconds, err := wavDurationSeconds(data); err == nil {
			out.DurationSeconds = seconds
		}
	}
	return out, true
}

// holdPower keeps the Mac awake while a brief is generated. The assertion is
// optional: when it cannot be taken, generation goes ahead and one line is
// logged.
func (s *mobileBriefService) holdPower(reason string) func() {
	if s.power == nil {
		return func() {}
	}
	release, err := s.power(reason)
	if err != nil {
		s.powerWarn.Do(func() {
			log.Printf("[mobile-brief] the Mac could not be held awake during generation")
		})
		return func() {}
	}
	if release == nil {
		return func() {}
	}
	return release
}

// LatestMobileBrief refreshes local signals in the newest stored brief.
func (s *mobileBriefService) LatestMobileBrief() (MobileBriefV1, bool) {
	// The live signals are brought up to date first, as before (latestWithToday);
	// the read after it is the sealed read every surface shares.
	s.latestWithToday()
	brief, ok, err := s.finishedLatest()
	return brief, ok && err == nil
}

// ListMobileBriefs returns summaries of stored briefs, newest first.
func (s *mobileBriefService) ListMobileBriefs(limit int) []MobileBriefSummary {
	return s.store.list(limit)
}

// GetMobileBrief returns one stored brief. The ID is validated before any file
// is opened.
func (s *mobileBriefService) GetMobileBrief(briefID string) (MobileBriefV1, bool) {
	brief, ok, err := s.finishedBrief(briefID)
	return brief, ok && err == nil
}

// RequestMobileBrief starts an on-demand brief. The result is "started" when a
// generation begins, "running" when one is already in flight, and "cooldown"
// with the next allowed time when this source generated one less than ten
// minutes ago.
func (s *mobileBriefService) RequestMobileBrief(reason string) (MobileBriefRequestResult, error) {
	return s.requestMobileBrief(reason, "")
}

// RequestTodayMobileBrief attaches an opaque request token to the generation
// that will satisfy it, including a generation already in flight.
func (s *mobileBriefService) RequestTodayMobileBrief(token string) (MobileBriefRequestResult, error) {
	if len(token) < 1 || len(token) > 80 || strings.Trim(token, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return MobileBriefRequestResult{}, errors.New("invalid today brief request token")
	}
	return s.requestMobileBrief(mobileBriefReasonSettings, token)
}

func (s *mobileBriefService) requestMobileBrief(reason, todayToken string) (MobileBriefRequestResult, error) {
	switch reason {
	case mobileBriefReasonPhone, mobileBriefReasonSettings:
	default:
		return MobileBriefRequestResult{}, errors.New("brief request reason must be phone or settings")
	}
	now := s.now()

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return MobileBriefRequestResult{}, errMobileBriefUnavailable
	}
	if reason == mobileBriefReasonPhone && s.phoneRequest != nil {
		result := s.phoneRequestResultLocked(now)
		s.mu.Unlock()
		return result, nil
	}
	if s.running {
		if todayToken != "" {
			s.todayRequests = append(s.todayRequests, todayToken)
		}
		if reason == mobileBriefReasonPhone {
			start := s.runningSince
			if start.IsZero() {
				start = now
			}
			s.phoneRequest = &mobileBriefPendingRequest{state: "making", startAt: start, readyAt: start.Add(90 * time.Second)}
			result := s.phoneRequestResultLocked(now)
			s.mu.Unlock()
			s.notifyPhoneToday()
			return result, nil
		}
		s.mu.Unlock()
		return MobileBriefRequestResult{Status: "running"}, nil
	}
	if last, ok := s.lastOnDemand[reason]; ok {
		if next := last.Add(mobileBriefCooldown); now.Before(next) {
			if reason == mobileBriefReasonPhone {
				ctx := s.runCtx
				if ctx == nil {
					ctx = context.Background()
				}
				ctx, s.queueCancel = context.WithCancel(ctx)
				s.phoneRequest = &mobileBriefPendingRequest{state: "queued", startAt: next, readyAt: next.Add(90 * time.Second)}
				result := s.phoneRequestResultLocked(now)
				s.wg.Add(1)
				go s.runQueuedPhoneBrief(ctx, next.Sub(now))
				s.mu.Unlock()
				s.notifyPhoneToday()
				return result, nil
			}
			s.mu.Unlock()
			return MobileBriefRequestResult{
				Status:        "cooldown",
				NextAllowedAt: formatMobileBriefTime(next),
			}, nil
		}
	}
	ctx := s.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	s.running = true
	s.runningSince = now
	s.runFinished = make(chan struct{})
	if todayToken != "" {
		s.todayRequests = append(s.todayRequests, todayToken)
	}
	result := MobileBriefRequestResult{Status: "started"}
	if reason == mobileBriefReasonPhone {
		s.phoneRequest = &mobileBriefPendingRequest{state: "making", startAt: now, readyAt: now.Add(90 * time.Second)}
		result = s.phoneRequestResultLocked(now)
	} else {
		s.coalesceQueuedPhoneRequestLocked(now)
	}
	s.wg.Add(1)
	go s.runOnDemand(ctx, reason)
	s.mu.Unlock()
	s.notifyPhoneToday()
	return result, nil
}

func (s *mobileBriefService) notifyPhoneToday() {
	if s.app != nil {
		if bridge := s.app.mobileBridgeForUse(); bridge != nil {
			bridge.noteTodayChanged()
		}
	}
}

// A scheduled run can satisfy a queued phone request without starting a
// second generation. The caller owns s.mu.
func (s *mobileBriefService) coalesceQueuedPhoneRequestLocked(start time.Time) {
	if s.phoneRequest == nil || s.phoneRequest.state != "queued" {
		return
	}
	if s.queueCancel != nil {
		s.queueCancel()
		s.queueCancel = nil
	}
	s.phoneRequest = &mobileBriefPendingRequest{state: "making", startAt: start, readyAt: start.Add(90 * time.Second)}
}

func (s *mobileBriefService) phoneRequestResultLocked(now time.Time) MobileBriefRequestResult {
	request := s.phoneRequest
	ready := request.readyAt
	if !ready.After(now) {
		ready = now.Add(90 * time.Second)
	}
	return MobileBriefRequestResult{Status: request.state, ReadyAt: formatMobileBriefTime(ready)}
}

// A queued phone request waits once for the cooldown and then for any run
// already in progress. It never starts two generators at once.
func (s *mobileBriefService) runQueuedPhoneBrief(ctx context.Context, delay time.Duration) {
	defer s.wg.Done()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	for {
		s.mu.Lock()
		if s.stopped || ctx.Err() != nil {
			s.mu.Unlock()
			return
		}
		if s.running {
			done := s.runFinished
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-done:
			}
			continue
		}
		now := s.now()
		s.running = true
		s.runningSince = now
		s.runFinished = make(chan struct{})
		s.phoneRequest = &mobileBriefPendingRequest{state: "making", startAt: now, readyAt: now.Add(90 * time.Second)}
		s.wg.Add(1)
		go s.runOnDemand(ctx, mobileBriefReasonPhone)
		s.mu.Unlock()
		s.notifyPhoneToday()
		return
	}
}

func (s *mobileBriefService) runOnDemand(ctx context.Context, reason string) {
	defer s.wg.Done()
	s.announceBuilding(true)
	_, err := s.generateRefenced(ctx, mobileBriefSlotOnRequest, reason, nil)

	s.mu.Lock()
	s.running = false
	s.runningSince = time.Time{}
	todayRequests := s.todayRequests
	s.todayRequests = nil
	if s.runFinished != nil {
		close(s.runFinished)
		s.runFinished = nil
	}
	if s.phoneRequest != nil && s.phoneRequest.state == "making" {
		s.lastOnDemand[mobileBriefReasonPhone] = s.now()
		s.phoneRequest = nil
	}
	s.lastOnDemand[reason] = s.now()
	s.mu.Unlock()
	s.notifyTodayBriefRequests(todayRequests, err == nil && ctx.Err() == nil)
	s.notifyPhoneToday()
	s.announceBuilding(false)
	if err != nil && ctx.Err() == nil {
		log.Printf("[mobile-brief] on-demand brief failed (%s)", mobileBriefFailureKind(err))
	}
}

// Schedule returns the saved schedule with the last generation and whether the
// native runner is available for the narrative.
func (s *mobileBriefService) Schedule() (MobileBriefSchedule, error) {
	settings := s.config.load()
	view := MobileBriefSchedule{
		Enabled:            settings.Enabled,
		Morning:            settings.Morning,
		Evening:            settings.Evening,
		NarrativeAvailable: mobileBriefNarrativeAvailable(s.app),
		FeedEnabled:        settings.Feed,
		AdaptiveTiming:     &settings.Adaptive,
	}
	if settings.Enabled && settings.Adaptive {
		if next, ok := s.nextAdaptivePrediction(s.now(), settings); ok {
			view.NextPredictedAt = formatMobileBriefTime(next.ScheduledAt)
			view.NextPredictedSlot, view.TimingReason = next.Slot, next.Reason
		} else {
			view.TimingReason = "no_window"
		}
	}
	if settings.Feed && s.feed != nil {
		view.FeedURL = s.feed.URL()
	}
	if latest, ok := s.store.latest(); ok {
		view.LastGeneratedAt = latest.AsOf
		view.LastSlot = latest.Slot
	}
	return view, nil
}

// SetSchedule validates and saves the schedule, then returns the new view.
func (s *mobileBriefService) SetSchedule(schedule MobileBriefSchedule) (MobileBriefSchedule, error) {
	if !s.beginWork() {
		return MobileBriefSchedule{}, errMobileBriefUnavailable
	}
	defer s.wg.Done()
	previous := s.config.load()
	settings := mobileBriefSettings{
		Enabled:   schedule.Enabled,
		Morning:   strings.TrimSpace(schedule.Morning),
		Evening:   strings.TrimSpace(schedule.Evening),
		Feed:      schedule.FeedEnabled,
		EnabledAt: previous.EnabledAt,
		Adaptive:  previous.Adaptive,
	}
	if schedule.AdaptiveTiming != nil {
		settings.Adaptive = *schedule.AdaptiveTiming
	}
	if settings.Enabled && (!previous.Enabled || previous.EnabledAt == "") {
		settings.EnabledAt = formatMobileBriefTime(s.now())
	}
	if err := validateMobileBriefSettings(settings); err != nil {
		return MobileBriefSchedule{}, err
	}
	if err := s.config.save(settings); err != nil {
		return MobileBriefSchedule{}, err
	}
	s.mu.Lock()
	running := s.started && !s.stopped
	s.mu.Unlock()
	if running {
		s.applyFeed(settings.Feed)
	}
	s.notifyPhoneToday()
	return s.Schedule()
}

// mobileBriefSlotClockEntry is one scheduled slot at its local clock time.
type mobileBriefSlotClockEntry struct {
	slot   string
	hour   int
	minute int
}

// mobileBriefSlotClock lists the enabled slots in the order they occur in a day.
func mobileBriefSlotClock(settings mobileBriefSettings) []mobileBriefSlotClockEntry {
	var entries []mobileBriefSlotClockEntry
	if hour, minute, ok := parseMobileBriefClock(settings.Morning); ok {
		entries = append(entries, mobileBriefSlotClockEntry{
			slot: mobileBriefSlotMorning, hour: hour, minute: minute,
		})
	}
	if hour, minute, ok := parseMobileBriefClock(settings.Evening); ok {
		entries = append(entries, mobileBriefSlotClockEntry{
			slot: mobileBriefSlotEvening, hour: hour, minute: minute,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].hour*60+entries[i].minute < entries[j].hour*60+entries[j].minute
	})
	return entries
}

// mobileBriefPendingSlot returns the scheduled slot that is due at now. A slot
// is due when its time has passed on today's local date, the slot is not yet
// filled (filled is the scheduler's test: a final brief with written words, so a
// made-ahead copy or a no-words build does not count), and its window is still
// open (mobileBriefSlotCloses). A slot whose window has closed is returned in
// missed and is never generated.
func mobileBriefPendingSlot(
	now time.Time,
	loc *time.Location,
	settings mobileBriefSettings,
	filled func(briefID string) bool,
) (slot string, due bool, missed []string) {
	if loc == nil {
		loc = time.Local
	}
	local := now.In(loc)
	year, month, day := local.Date()
	datePrefix := local.Format("2006-01-02")
	for _, candidate := range mobileBriefSlotClock(settings) {
		at := time.Date(year, month, day, candidate.hour, candidate.minute, 0, 0, loc)
		// The build starts mobileBriefBuildLead before the slot; the brief is
		// still stated for the slot time (slotOccurrence keeps that time).
		if local.Before(at.Add(-mobileBriefBuildLead)) {
			continue
		}
		briefID := datePrefix + "-" + candidate.slot
		if filled(briefID) {
			continue
		}
		occurrence := mobileBriefSlotOccurrence{BriefID: briefID, Slot: candidate.slot, ScheduledAt: at}
		if local.Before(mobileBriefSlotCloses(occurrence, loc, settings)) {
			return candidate.slot, true, missed
		}
		missed = append(missed, briefID)
	}
	return "", false, missed
}

// Only a writing failure can use the ordinary composer; source admission and
// withdrawal failures must retain their existing fail-closed behavior.
func mobileBriefWriteJudgeFailed(err error) bool {
	for err != nil {
		if stage, ok := err.(*logStageError); ok && stage.stage == "write and judge" {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// briefFailureText is the failure's first line for the run record and the log:
// redacted, one line, at most 200 bytes.
func briefFailureText(err error) string {
	text := err.Error()
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimSpace(redactActivityModelText(text))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// scheduledTimeFor is the configured time of a scheduled slot today. It is
// false for on-request briefs and when the schedule has no such slot.
func (s *mobileBriefService) scheduledTimeFor(slot string, now time.Time) (time.Time, bool) {
	if slot != mobileBriefSlotMorning && slot != mobileBriefSlotEvening {
		return time.Time{}, false
	}
	occurrence, found := s.slotOccurrence(now, s.config.load(), slot)
	return occurrence.ScheduledAt, found
}
