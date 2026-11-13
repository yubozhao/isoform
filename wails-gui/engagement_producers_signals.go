package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"runtime"
	"strings"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/identity"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/storage"
)

const (
	// engagementCaptureInterval is how often capture health is sampled.
	engagementCaptureInterval = cadenceEngagementCapture
	// engagementCaptureOutageAfter is how long a capture source must stay off
	// before capture_paused is submitted. Closed-eyes time never counts.
	engagementCaptureOutageAfter = 15 * time.Minute
	// engagementPurposeInterval is how often the Purpose Map is checked.
	engagementPurposeInterval = cadenceCheck
	// engagementCommitmentWindow is how close to its due time a commitment must
	// be before it counts as slipping, and how long the moment stays useful.
	engagementCommitmentWindow = 24 * time.Hour
	engagementWeek             = 7 * 24 * time.Hour
	// engagementReappearAfter is the sighting gap after which a known contact
	// counts as reappeared.
	engagementReappearAfter = 30 * 24 * time.Hour
	// engagementCaptureScreen is the capture kind for screen capture.
	engagementCaptureScreen = "screen"
)

// Prepare against the prior last-seen time, then invoke the returned callback
// only after the triggering sighting is stored. Receipt minting sees that write.
var engagementPersonSightingHook = func(context.Context, string, string, int64) func() { return nil }

// engagementCaptureWatch maps a permission item to the capture source the
// watcher reports. An item missing from a platform's list is not watched there.
type engagementCaptureWatch struct {
	PermissionKey string
	Kind          string
	Label         string
}

var engagementCaptureWatches = []engagementCaptureWatch{
	{PermissionKey: "screen_recording", Kind: engagementCaptureScreen, Label: "Screen capture"},
	{PermissionKey: "screen_capture", Kind: engagementCaptureScreen, Label: "Screen capture"},
	{PermissionKey: "microphone", Kind: "microphone", Label: "Microphone"},
	{PermissionKey: "system_audio", Kind: "system_audio", Label: "System audio"},
}

// captureSnapshot is one watched capture source at one instant.
type captureSnapshot struct {
	Kind    string
	Label   string
	Granted bool // the OS permission is present
	Enabled bool // the user has not switched the source off
	Running bool // the capture loop is alive
}

func (s captureSnapshot) off() bool {
	return !s.Granted || !s.Enabled || !s.Running
}

// stopped is a stop nobody chose: the source is still switched on but its
// permission is gone or its loop died (settings-review ST66). A source the
// user switched off is not an outage.
func (s captureSnapshot) stopped() bool {
	return s.Enabled && (!s.Granted || !s.Running)
}

// cause says why a source is off: "permission" when its access was turned
// off, "chosen" when the user switched it off, "stopped" when its capture
// loop stopped on its own. Empty while it is on.
func (s captureSnapshot) cause() string {
	if !s.Granted {
		return captureCausePermission
	}
	if !s.Enabled {
		return captureCauseChosen
	}
	if !s.Running {
		return captureCauseStopped
	}
	return ""
}

const (
	captureCausePermission = "permission"
	captureCauseChosen     = "chosen"
	captureCauseStopped    = "stopped"
)

// engagementCaptureSnapshots builds the watched sources from a platform's
// permission list. Linux has no capture path the activity monitor reports on,
// so it watches nothing there.
func engagementCaptureSnapshots(
	goos string,
	items []PermissionItem,
	enabled func(permissionKey string) bool,
	running func(kind string) bool,
) []captureSnapshot {
	if goos == "linux" {
		return nil
	}
	var out []captureSnapshot
	for _, watch := range engagementCaptureWatches {
		item, ok := engagementPermissionItem(items, watch.PermissionKey)
		if !ok {
			continue
		}
		out = append(out, captureSnapshot{
			Kind:    watch.Kind,
			Label:   watch.Label,
			Granted: item.Granted,
			Enabled: enabled(watch.PermissionKey),
			Running: running(watch.Kind),
		})
	}
	return out
}

func engagementPermissionItem(items []PermissionItem, key string) (PermissionItem, bool) {
	for _, item := range items {
		if item.Key == key {
			return item, true
		}
	}
	return PermissionItem{}, false
}

// liveEngagementCaptureSnapshots reads the current capture state. Microphone
// and system audio share the background-listening switch and capture loop.
func (a *App) liveEngagementCaptureSnapshots() []captureSnapshot {
	enabled := func(permissionKey string) bool {
		switch permissionKey {
		case "microphone":
			if microphoneSwitchIsSource() && !backgroundMicrophoneEnabled() {
				return false
			}
			return a.GetBackgroundListening()
		case "system_audio":
			return a.GetBackgroundListening()
		default:
			return a.isPermissionEnabled(permissionKey)
		}
	}
	running := func(kind string) bool {
		switch kind {
		case engagementCaptureScreen:
			return a.isActivityMonitorRunning()
		default:
			return a.isBackgroundListeningRunning()
		}
	}
	snapshots := engagementCaptureSnapshots(runtime.GOOS, getPlatformPermissions().Items, enabled, running)
	// A microphone the user switched off on purpose is not an outage.
	if microphoneSwitchIsSource() && !backgroundMicrophoneEnabled() {
		kept := snapshots[:0]
		for _, snap := range snapshots {
			if snap.Kind != "microphone" {
				kept = append(kept, snap)
			}
		}
		snapshots = kept
	}
	return snapshots
}

// engagementCaptureOutage is one stretch of a capture source being off.
type engagementCaptureOutage struct {
	Kind  string
	Label string
	Start time.Time
	// Cause is why it went off (captureCausePermission or captureCauseStopped).
	Cause string
}

// engagementCaptureTracker remembers when each watched source went off. The
// watcher goroutine owns it, so a restart begins outages afresh. A source
// only counts as paused after it has been seen on in this process: a
// permission that was never granted is onboarding, not an outage.
type engagementCaptureTracker struct {
	open       map[string]engagementCaptureOutage
	wasOn      map[string]bool
	last       time.Time
	lastAt     time.Time
	sleepState func() activitymon.SystemStateChange
	// sleptAt is when the computer last went to sleep, zero if unknown.
	sleptAt func() time.Time
	locked  func() bool
}

func newEngagementCaptureTracker() *engagementCaptureTracker {
	return &engagementCaptureTracker{open: map[string]engagementCaptureOutage{}, wasOn: map[string]bool{}, sleptAt: latestSystemSleepAt, locked: activitymon.IsScreenLocked, sleepState: activitymon.LatestSystemSleepState}
}

func latestSystemSleepAt() time.Time {
	state := activitymon.LatestSystemSleepState()
	switch state.Kind {
	case activitymon.SystemStateSleep:
		return state.At
	case activitymon.SystemStateWake:
		return state.SleepAt
	}
	return time.Time{}
}

// observe records one sample and returns the outages that have lasted longer
// than engagementCaptureOutageAfter. A source that is back on ends its outage,
// and a source missing from the sample is no longer watched, so its outage ends.
func (t *engagementCaptureTracker) observe(now time.Time, snapshots []captureSnapshot) []engagementCaptureOutage {
	seen := make(map[string]bool, len(snapshots))
	var due []engagementCaptureOutage
	// The computer slept since the last sample: what was off before it is
	// not an outage nobody chose.
	if !t.last.IsZero() && t.sleptAt != nil {
		if slept := t.sleptAt(); !slept.IsZero() && slept.After(t.last) && !slept.After(now) {
			clear(t.open)
		}
	}
	t.last = now
	for _, snap := range snapshots {
		seen[snap.Kind] = true
		if !snap.stopped() {
			if !snap.off() {
				t.wasOn[snap.Kind] = true
			}
			delete(t.open, snap.Kind)
			continue
		}
		if !t.wasOn[snap.Kind] {
			continue
		}
		outage, ok := t.open[snap.Kind]
		if !ok {
			outage = engagementCaptureOutage{Kind: snap.Kind, Label: snap.Label, Start: now, Cause: snap.cause()}
			t.open[snap.Kind] = outage
		}
		if now.Sub(outage.Start) > engagementCaptureOutageAfter {
			due = append(due, outage)
		}
	}
	for kind := range t.open {
		if !seen[kind] {
			delete(t.open, kind)
		}
	}
	return due
}

// reset drops every open outage. Closed-eyes mode calls it so that paused time
// never counts toward an outage.
func (t *engagementCaptureTracker) reset() {
	clear(t.open)
}

// observeCaptureHealth samples capture once. Closed-eyes mode is a deliberate
// pause and a locked screen stops capture on purpose, so both clear the
// tracker and submit nothing.
func (a *App) observeCaptureHealth(
	ctx context.Context,
	tracker *engagementCaptureTracker,
	now time.Time,
	snapshots func() []captureSnapshot,
) {
	var due, ended []engagementCaptureOutage
	slept := false
	if tracker.sleepState != nil {
		state := tracker.sleepState()
		if !state.At.After(now) {
			slept = engagementCaptureSleptSince(state, tracker.lastAt)
		}
	}
	tracker.lastAt = now
	if a.IsClosedEyesModeEnabled() || slept || a.isBackgroundListeningScreenLocked() || (tracker.locked != nil && tracker.locked()) {
		tracker.reset()
	} else {
		samples := snapshots()
		// Only a present, running source proves an observed recovery. A
		// reset or a source no longer watched leaves its endpoint unknown.
		for _, sample := range samples {
			if outage, open := tracker.open[sample.Kind]; open && !sample.off() {
				ended = append(ended, outage)
			}
		}
		due = tracker.observe(now, samples)
	}
	if err := a.recordCaptureOutageIntervals(ctx, due, ended, now); err != nil {
		log.Printf("[engagement] recording capture intervals: %v", err)
	}
	for _, outage := range engagementCaptureEvents(due) {
		if err := a.submitCapturePausedSignal(ctx, outage, now); err != nil {
			log.Printf("[engagement] capture paused for %s: %v", outage.Kind, err)
		}
	}
}

// engagementCaptureEvents keeps one outage per stop: the microphone and
// system audio share one capture loop, so its stop is one event, not two.
func engagementCaptureEvents(due []engagementCaptureOutage) []engagementCaptureOutage {
	listening := map[int64]bool{}
	for _, outage := range due {
		if outage.Kind == "microphone" && outage.Cause == captureCauseStopped {
			listening[outage.Start.UnixMilli()] = true
		}
	}
	out := due[:0:0]
	for _, outage := range due {
		if outage.Kind == "system_audio" && outage.Cause == captureCauseStopped && listening[outage.Start.UnixMilli()] {
			continue
		}
		out = append(out, outage)
	}
	return out
}

// submitCapturePausedSignal submits the moment for one outage. The dedupe key
// carries the outage start, so each outage is announced once.
func (a *App) submitCapturePausedSignal(ctx context.Context, outage engagementCaptureOutage, now time.Time) error {
	return a.submitEngagementSignal(ctx, engagement.Moment{
		Kind:      engagement.KindCapturePaused,
		SubjectID: outage.Kind,
		DedupeKey: engagement.DedupeKey(engagement.KindCapturePaused, outage.Kind, outage.Start.Format(time.RFC3339)),
		CreatedAt: outage.Start,
		Stakes:    0.7,
		Params: map[string]string{
			"what":     outage.Label,
			"cause":    outage.Cause,
			"duration": engagementDurationText(now.Sub(outage.Start)),
		},
	})
}

// engagementDurationText renders an outage length for the copy, such as
// "20 minutes" or "2 hours".
func engagementDurationText(d time.Duration) string {
	minutes := int(d.Round(time.Minute).Minutes())
	switch hours := minutes / 60; hours {
	case 0:
		return fmt.Sprintf("%d minutes", minutes)
	case 1:
		return "1 hour"
	default:
		return fmt.Sprintf("%d hours", hours)
	}
}

// engagementPurposeSignals returns the moment a current Purpose statement earns
// now, if any. Commitments can slip; goals ask for a check-in.
func engagementPurposeSignals(detail PurposeOpenLoopDetail, now time.Time) []engagement.Moment {
	if !detail.Eligible {
		return nil
	}
	var (
		moment engagement.Moment
		ok     bool
	)
	switch detail.OpenLoop.Kind {
	case PurposeKindCommitment:
		moment, ok = engagementCommitmentSlippingMoment(detail, now)
	case PurposeKindGoal:
		moment, ok = engagementGoalCheckInMoment(detail, now)
	}
	if !ok {
		return nil
	}
	return []engagement.Moment{moment}
}

// engagementCommitmentSlippingMoment fires when a dated commitment is due within
// a day, or was due within the last day, and no update or resolve evidence has
// arrived since it was created.
func engagementCommitmentSlippingMoment(detail PurposeOpenLoopDetail, now time.Time) (engagement.Moment, bool) {
	current := detail.Current
	title := commitmentShortName(current.Title)
	if current.DueKind != PurposeDueDate || title == "" {
		return engagement.Moment{}, false
	}
	due, ok := parseEngagementDue(current.DueValue, now.Location())
	if !ok {
		return engagement.Moment{}, false
	}
	expires := due.Add(engagementCommitmentWindow)
	if due.After(now.Add(engagementCommitmentWindow)) || !now.Before(expires) {
		return engagement.Moment{}, false
	}
	if engagementHasProgress(detail) {
		return engagement.Moment{}, false
	}
	return engagement.Moment{
		Kind:      engagement.KindCommitmentSlipping,
		SubjectID: detail.OpenLoop.ID,
		DedupeKey: engagement.DedupeKey(engagement.KindCommitmentSlipping,
			detail.OpenLoop.ID, due.In(now.Location()).Format("2006-01-02")),
		CreatedAt: now,
		DueAt:     due,
		ExpiresAt: expires,
		Stakes:    0.7,
		Params: map[string]string{
			"title": title,
			"due":   engagementDueText(due, now),
		},
	}, true
}

// parseEngagementDue reads a Purpose due value. A bare day means the end of
// that local day, and an RFC 3339 value is used as written. Any other text is
// not a due time the producer can rely on, so it reports false.
func parseEngagementDue(value string, loc *time.Location) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at, true
	}
	day, err := time.ParseInLocation("2006-01-02", value, loc)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc).Add(-time.Second), true
}

// engagementDueText says when something is due relative to now: "today",
// "tomorrow", or "on Oct 14".
func engagementDueText(due, now time.Time) string {
	loc := now.Location()
	dueLocal := due.In(loc)
	dueDay := time.Date(dueLocal.Year(), dueLocal.Month(), dueLocal.Day(), 0, 0, 0, 0, loc)
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	days := int(math.Round(dueDay.Sub(nowDay).Hours() / 24))
	switch days {
	case 0:
		return "today"
	case 1:
		return "tomorrow"
	default:
		return "on " + dueLocal.Format("Jan 2")
	}
}

// engagementHasProgress reports whether update or resolve evidence was observed
// after the statement was created. A reminder that was renamed or moved to
// another day is not progress: the commitment follows its new due date, and
// the moment's dedupe key carries that date.
func engagementHasProgress(detail PurposeOpenLoopDetail) bool {
	created := detail.OpenLoop.CreatedAt
	for _, evidence := range detail.Evidence {
		if evidence.SourceObservedAt < created {
			continue
		}
		if evidence.SourceKind == reminderPurposeSourceKind && evidence.Stance == PurposeStanceUpdate {
			continue
		}
		switch evidence.Stance {
		case PurposeStanceUpdate, PurposeStanceResolve:
			return true
		}
	}
	return false
}

// engagementGoalCheckInMoment fires when a goal has gone a week without any
// recorded activity.
func engagementGoalCheckInMoment(detail PurposeOpenLoopDetail, now time.Time) (engagement.Moment, bool) {
	title := strings.TrimSpace(detail.Current.Title)
	if title == "" || now.Sub(engagementLastTouch(detail)) < engagementWeek {
		return engagement.Moment{}, false
	}
	return engagement.Moment{
		Kind:      engagement.KindGoalCheckIn,
		SubjectID: detail.OpenLoop.ID,
		DedupeKey: engagement.DedupeKey(engagement.KindGoalCheckIn, detail.OpenLoop.ID, engagement.WeekKey(now)),
		CreatedAt: now,
		ExpiresAt: now.Add(engagementWeek),
		Stakes:    0.4,
		Params:    map[string]string{"title": title},
	}, true
}

// engagementLastTouch is the latest recorded activity on a statement: its
// creation, a later ledger event, or evidence observed since.
func engagementLastTouch(detail PurposeOpenLoopDetail) time.Time {
	last := detail.OpenLoop.CreatedAt
	for _, event := range detail.Events {
		if event.OccurredAt > last {
			last = event.OccurredAt
		}
	}
	for _, evidence := range detail.Evidence {
		if evidence.SourceObservedAt > last {
			last = evidence.SourceObservedAt
		}
	}
	return time.UnixMilli(last)
}

// checkPurposeEngagement reads the Purpose Map and submits the commitment and
// goal moments it earns.
func (a *App) checkPurposeEngagement(ctx context.Context, now time.Time) {
	if a == nil || a.engagement == nil {
		return
	}
	store, err := a.purposeProductStore()
	if err != nil {
		return // Purpose storage is not ready yet; the next pass tries again.
	}
	details, err := store.ListDetailsAll()
	if err != nil {
		log.Printf("[engagement] reading the Purpose Map: %v", err)
		return
	}
	for _, detail := range details {
		for _, m := range engagementPurposeSignals(detail, now) {
			if err := a.submitEngagementSignal(ctx, m); err != nil {
				log.Printf("[engagement] %s for %s: %v", m.Kind, m.SubjectID, err)
			}
		}
	}
}

// prepareEngagementPersonSighting detects the gap before the write, and returns
// the submission to run after a successful write, including its source receipt.
func (a *App) prepareEngagementPersonSighting(ctx context.Context, principalID, app string, seenAt int64) func() {
	if a == nil || a.engagement == nil || a.store == nil || principalID == "" || seenAt <= 0 {
		return nil
	}
	last, err := a.store.PrincipalLastSeenAt(ctx, principalID)
	if err != nil {
		log.Printf("[engagement] reading last sighting: %v", err)
		return nil
	}
	seen := time.UnixMilli(seenAt)
	if last == 0 || seen.Sub(time.UnixMilli(last)) < engagementReappearAfter {
		return nil
	}
	principal, err := a.store.GetPrincipal(ctx, principalID)
	if err != nil {
		log.Printf("[engagement] reading principal: %v", err)
		return nil
	}
	name := strings.TrimSpace(principal.DisplayName)
	where := identity.AppDisplayName(app)
	if principal.Kind != storage.PrincipalKindContact || name == "" || where == "" {
		return nil
	}
	moment := engagement.Moment{
		Kind:      engagement.KindPersonReappeared,
		SubjectID: principalID,
		DedupeKey: engagement.DedupeKey(engagement.KindPersonReappeared, principalID, engagement.WeekKey(seen)),
		CreatedAt: seen,
		ExpiresAt: seen.Add(3 * 24 * time.Hour),
		Stakes:    0.3,
		Params:    map[string]string{"name": name, "where": where},
	}
	return func() {
		if err := a.submitEngagementSignal(ctx, moment); err != nil {
			log.Printf("[engagement] person reappeared for %s: %v", principalID, err)
		}
	}
}

// engagementSelfDreamOperations submits learned_to_confirm for each persisted
// operation that will wait for the user in Settings > Personal context, under
// the same rules the Settings card lists by: an answered proposal or one whose
// targets changed is skipped. A proposal that a later run repeats keeps its
// dedupe key, so it is announced once.
func (a *App) engagementSelfDreamOperations(ctx context.Context, operations []SelfDreamOperationRecord) {
	if a == nil || a.engagement == nil {
		return
	}
	var queue *selfDreamReviewQueue
	if a.activity != nil && a.activity.store != nil {
		loaded, err := a.activity.store.loadSelfDreamReviewQueue()
		if err != nil {
			log.Printf("[engagement] reading Self Dream reviews: %v", err)
			return
		}
		queue = &loaded
	}
	for _, op := range operations {
		if !selfDreamAwaitsReview(op) {
			continue
		}
		if queue != nil {
			if _, open := queue.open(op); !open {
				continue
			}
		}
		created := time.Now()
		if op.CreatedAt > 0 {
			created = time.UnixMilli(op.CreatedAt)
		}
		if err := a.submitEngagementSignal(ctx, engagement.Moment{
			Kind:      engagement.KindLearnedToConfirm,
			SubjectID: op.ID,
			DedupeKey: engagementSelfDreamDedupeKey(op),
			CreatedAt: created,
			ExpiresAt: created.Add(engagementWeek),
			Stakes:    0.3,
			Params:    map[string]string{"statement": engagementSelfDreamSummary(op)},
		}); err != nil {
			log.Printf("[engagement] learned to confirm for %s: %v", op.ID, err)
		}
	}
}

// engagementSelfDreamDedupeKey is the learned_to_confirm key of a Self Dream
// proposal, shared by every run's copy of it.
func engagementSelfDreamDedupeKey(op SelfDreamOperationRecord) string {
	return engagement.DedupeKey(engagement.KindLearnedToConfirm, selfDreamProposalKey(op))
}

// engagementRetireSelfDreamProposal withdraws the proposal's learned_to_confirm
// moment while it is still waiting to be delivered, once the user answered it
// in Settings or it went stale. A moment already sent stays as it is.
func (a *App) engagementRetireSelfDreamProposal(ctx context.Context, op SelfDreamOperationRecord) {
	if a == nil || a.engagement == nil {
		return
	}
	if err := a.engagement.Retire(ctx, engagementSelfDreamDedupeKey(op), "answered_in_settings"); err != nil {
		log.Printf("[engagement] retiring learned to confirm for %s: %v", op.ID, err)
	}
}

// engagementSelfDreamSummary is the one-line summary of an operation, the same
// line the Updates to review card in Settings shows. It reuses the product
// copy, so no raw memory text is copied.
func engagementSelfDreamSummary(op SelfDreamOperationRecord) string {
	after := selfDreamProposedStatements(op)
	// selfDreamUpdateSummary reads only the count of targets, so placeholders
	// keep the summary identical to the one the Settings card shows.
	before := make([]string, len(op.TargetEntryIDs))
	return engagementOneLine(selfDreamUpdateSummary(op, before, after), 200)
}

// engagementOneLine keeps the first line of text and caps its length in runes.
func engagementOneLine(text string, limit int) string {
	line := strings.TrimSpace(text)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if runes := []rune(line); len(runes) > limit {
		line = strings.TrimSpace(string(runes[:limit])) + "..."
	}
	return line
}

// engagementMemoryDreamPreview submits learned_to_confirm for a finished dry-run
// Memory Dream cycle that proposes changes. Only counts leave the cycle, never
// memory text.
func (a *App) engagementMemoryDreamPreview(ctx context.Context, dryRun bool, run *DreamRun, preview *DreamPreview) {
	if a == nil || a.engagement == nil || !dryRun || run == nil || preview == nil || run.Status != "completed" {
		return
	}
	count := len(preview.ArchiveProposed) + len(preview.SynthesisProposed)
	if count == 0 {
		return
	}
	noun := "memory cleanups"
	if count == 1 {
		noun = "memory cleanup"
	}
	created := time.Now()
	if err := a.submitEngagementSignal(ctx, engagement.Moment{
		Kind:      engagement.KindLearnedToConfirm,
		SubjectID: run.ID,
		DedupeKey: engagement.DedupeKey(engagement.KindLearnedToConfirm, run.ID),
		CreatedAt: created,
		ExpiresAt: created.Add(engagementWeek),
		Stakes:    0.3,
		Params:    map[string]string{"statement": fmt.Sprintf("Isoform found %d %s to review.", count, noun)},
	}); err != nil {
		log.Printf("[engagement] learned to confirm for %s: %v", run.ID, err)
	}
}

// submitEngagementSignal submits a moment unless its dedupe key was used before.
// The engagement service only dedupes open moments, so a signal whose moment was
// already sent would otherwise be announced again on the next pass.
func (a *App) submitEngagementSignal(ctx context.Context, m engagement.Moment) error {
	if a == nil || a.engagement == nil || a.store == nil {
		return nil
	}
	seen, err := a.store.EngagementMomentKeyExists(ctx, m.DedupeKey)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}
	_, err = a.engagement.Submit(ctx, m)
	return err
}

// startEngagementSignals installs the sighting hook and starts the watcher. It
// runs once from startEngagement, after the engagement service has started.
func (a *App) startEngagementSignals(ctx context.Context) {
	if a == nil {
		return
	}
	engagementPersonSightingHook = a.prepareEngagementPersonSighting
	go a.runEngagementSignals(ctx)
}

// runEngagementSignals samples capture health every five minutes and checks the
// Purpose Map every thirty, until ctx ends.
func (a *App) runEngagementSignals(ctx context.Context) {
	capture := time.NewTicker(engagementCaptureInterval)
	defer capture.Stop()
	purpose := time.NewTicker(engagementPurposeInterval)
	defer purpose.Stop()
	tracker := newEngagementCaptureTracker()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-capture.C:
			a.observeCaptureHealth(ctx, tracker, now, a.liveEngagementCaptureSnapshots)
		case now := <-purpose.C:
			if a.claimDerivedRun("engagement_purpose", cadenceEngagementPurpose, now) {
				a.checkPurposeEngagement(ctx, now)
			}
		}
	}
}

// engagementCaptureSleptSince reports a computer asleep now, or one that woke
// after the previous sample, so an outage never spans a sleep.
func engagementCaptureSleptSince(state activitymon.SystemStateChange, last time.Time) bool {
	switch state.Kind {
	case activitymon.SystemStateSleep:
		return true
	case activitymon.SystemStateWake:
		return !last.IsZero() && state.At.After(last)
	}
	return false
}
