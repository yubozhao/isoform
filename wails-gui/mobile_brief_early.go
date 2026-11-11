package main

import (
	"context"
	"log"
	"time"
)

// An early brief is made before its slot, for the slot's day (P157, handoff
// early-brief v1.0.0). It is stored under the slot's own ID, carries
// generation.reason "made_ahead" and generation.scheduledAt (the slot time), and
// shows nowhere before that time: the stored briefs, the latest record and the
// phone's outbox all hold it until its slot. At its slot an awake computer
// builds the slot's brief as usual, which replaces it (R40). A computer that
// slept through the slot keeps the early brief.

const (
	// mobileBriefGenerationMadeAhead is generation.reason for an early brief.
	mobileBriefGenerationMadeAhead = "made_ahead"
	// mobileBriefReasonAhead is the build reason for an early brief.
	mobileBriefReasonAhead = "ahead"
	// mobileBriefAheadMorningWindow is how far ahead of a morning slot an early brief
	// may be made. Other slots may be made up to mobileBriefAheadOtherWindow ahead.
	mobileBriefAheadMorningWindow = 12 * time.Hour
	mobileBriefAheadOtherWindow   = time.Hour
	// mobileBriefRemakeAge is how old a held early brief must be before a later
	// trigger makes it again (same ID; it replaces the held one only once complete).
	mobileBriefRemakeAge = time.Hour
	// mobileBriefAheadSleepLookback is how far back a slept-through slot allows a
	// scheduled early brief on a computer that slept.
	mobileBriefAheadSleepLookback = 14 * 24 * time.Hour
	// mobileBriefNoTextRetry is how long a slot waits before the next build after one
	// that produced no written text (or kept a made-ahead copy). It bounds a morning to
	// a handful of builds, the same cap as the failure backoff.
	mobileBriefNoTextRetry = 30 * time.Minute
	// mobileBriefNoTextBuilds is how many builds of one slot may write no words: the
	// first, and two retries. Then the slot waits for the next slot (no model calls
	// for a quiet day).
	mobileBriefNoTextBuilds = 3
	// mobileBriefAheadMomentHour is the scheduled moment when the person has no quiet
	// hours set earlier (9 PM).
	mobileBriefAheadMomentHour = 21
	// mobileBriefAheadLatestMoment is the latest the scheduled moment may be before a
	// morning slot: no more than this many hours before it.
)

// mobileBriefClock is the time the held-brief rules read. Tests replace it.
var mobileBriefClock = time.Now

// mobileBriefIsAhead reports whether a stored brief was made before its slot.
func mobileBriefIsAhead(brief MobileBriefV1) bool {
	return brief.Generation != nil && brief.Generation.Reason == mobileBriefGenerationMadeAhead
}

// mobileBriefAheadSlotTime is the slot time an early brief waits for.
func mobileBriefAheadSlotTime(brief MobileBriefV1) (time.Time, bool) {
	if !mobileBriefIsAhead(brief) {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, brief.Generation.ScheduledAt)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// mobileBriefHeld reports whether an early brief must stay hidden at now: it is
// made, but its slot has not come yet.
func mobileBriefHeld(brief MobileBriefV1, now time.Time) bool {
	at, ok := mobileBriefAheadSlotTime(brief)
	return ok && now.Before(at)
}

// mobileBriefAheadTarget picks the slot an early brief should be made for: the
// next slot, when its brief doesn't exist yet, or when the held early brief for it is
// more than an hour old (then it is made again under the same ID). A morning slot
// may be made up to 12 hours ahead; any other slot up to 1 hour ahead. No other
// brief blocks it. held loads a stored brief by ID.
func mobileBriefAheadTarget(now time.Time, loc *time.Location, settings mobileBriefSettings, held func(string) (MobileBriefV1, bool)) (mobileBriefSlotOccurrence, bool) {
	if loc == nil {
		loc = time.Local
	}
	var next mobileBriefSlotOccurrence
	found := false
	for day := -1; day <= 1; day++ {
		for _, occurrence := range mobileBriefTodaySlots(now.In(loc).AddDate(0, 0, day), loc, settings) {
			at := occurrence.ScheduledAt
			if !at.After(now) {
				continue
			}
			if !found || at.Before(next.ScheduledAt) {
				next, found = occurrence, true
			}
		}
	}
	if !found {
		return mobileBriefSlotOccurrence{}, false
	}
	window := mobileBriefAheadOtherWindow
	if next.Slot == mobileBriefSlotMorning {
		window = mobileBriefAheadMorningWindow
	}
	if next.ScheduledAt.Sub(now) > window {
		return mobileBriefSlotOccurrence{}, false
	}
	brief, exists := held(next.BriefID)
	if !exists {
		return next, true
	}
	// A brief that is not an early one is the slot's final brief; it is never remade.
	if !mobileBriefIsAhead(brief) {
		return mobileBriefSlotOccurrence{}, false
	}
	if now.Sub(mobileBriefAsOfTime(brief)) <= mobileBriefRemakeAge {
		return mobileBriefSlotOccurrence{}, false
	}
	return next, true
}

// mobileBriefAheadMoment is today's scheduled moment for an early brief: 9 PM, or
// the start of the person's quiet hours when that is earlier. It is never before
// the day's last slot, and never more than 12 hours before the morning slot.
// quiet is the quiet start as a duration since local midnight.
func mobileBriefAheadMoment(now time.Time, loc *time.Location, settings mobileBriefSettings, quiet time.Duration, hasQuiet bool) time.Time {
	if loc == nil {
		loc = time.Local
	}
	local := now.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	moment := midnight.Add(time.Duration(mobileBriefAheadMomentHour) * time.Hour)
	if hasQuiet {
		if start := midnight.Add(quiet); start.Before(moment) {
			moment = start
		}
	}
	var lastSlot time.Time
	for _, occurrence := range mobileBriefTodaySlots(midnight, loc, settings) {
		if occurrence.ScheduledAt.After(lastSlot) {
			lastSlot = occurrence.ScheduledAt
		}
	}
	if moment.Before(lastSlot) {
		moment = lastSlot
	}
	for _, occurrence := range mobileBriefTodaySlots(midnight.AddDate(0, 0, 1), loc, settings) {
		if occurrence.Slot == mobileBriefSlotMorning {
			if earliest := occurrence.ScheduledAt.Add(-mobileBriefAheadMorningWindow); moment.Before(earliest) {
				moment = earliest
			}
		}
	}
	return moment
}

// quietStartOfDay reads the person's quiet-hours start. Quiet hours count as set
// when their start and end differ (engagement's QuietWindow.Contains).
func (s *mobileBriefService) quietStartOfDay() (time.Duration, bool) {
	svc := s.app.engagementService()
	if svc == nil {
		return 0, false
	}
	svc.mu.Lock()
	quiet := svc.prefs.Quiet
	svc.mu.Unlock()
	if quiet.Start == quiet.End {
		return 0, false
	}
	return quiet.Start, true
}

// mobileBriefPhoneNotify decides the phone's notice for a brief. An early brief
// notifies the phone at its slot (the relay pushes then), not when it is made.
// A brief that replaces an early one sends no second notice.
func mobileBriefPhoneNotify(ahead, replacedAhead, notify bool) bool {
	if replacedAhead {
		return false
	}
	return ahead || notify
}

// mobileBriefAheadDayKey carries an early brief's slot day into the generator.
type mobileBriefAheadDayKey struct{}

// mobileBriefDeliverAtText is the slot time an early brief's frame and outbox row
// carry (RFC 3339, UTC), or empty for any other brief.
func mobileBriefDeliverAtText(brief MobileBriefV1) string {
	at, ok := mobileBriefAheadSlotTime(brief)
	if !ok {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// logMobileBriefAhead records an early-brief decision: the trigger, the brief ID,
// and whether its slot replaced it.
func logMobileBriefAhead(trigger, briefID string, replaced bool) {
	log.Printf("[mobile-brief] early brief trigger=%s id=%s replaced=%t", trigger, briefID, replaced)
}

// queueAhead asks the loop to check for an early brief. It never blocks, so it
// is safe under the state lock.
func (s *mobileBriefService) queueAhead(trigger string) {
	select {
	case s.ahead <- trigger:
	default:
	}
}

// requestAhead is queueAhead for callers that do not hold the state lock.
func (s *mobileBriefService) requestAhead(trigger string) {
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if !stopped {
		s.queueAhead(trigger)
	}
}

// armAheadMoment arms one timer for today's scheduled moment. Once the moment has
// passed the same day, it asks at once, so a computer shut after 11 PM still gets
// its early brief. Armed once per day.
func (s *mobileBriefService) armAheadMoment(now time.Time, loc *time.Location) {
	if loc == nil {
		loc = time.Local
	}
	quiet, hasQuiet := s.quietStartOfDay()
	moment := mobileBriefAheadMoment(now, loc, s.config.load(), quiet, hasQuiet)
	day := moment.Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.aheadArmed == day {
		return
	}
	s.aheadArmed = day
	if s.aheadTimer != nil {
		s.aheadTimer.Stop()
	}
	if !now.Before(moment) {
		s.queueAhead("scheduled")
		return
	}
	s.aheadTimer = time.AfterFunc(moment.Sub(now), func() { s.requestAhead("scheduled") })
}

// sleptThroughSlotRecently reports whether the computer slept through a brief
// slot in the last 14 days (the runs ledger's missed-asleep reason).
func (s *mobileBriefService) sleptThroughSlotRecently(now time.Time) bool {
	for _, run := range s.runs.list() {
		if run.Reason != mobileBriefMissedAsleep {
			continue
		}
		at, err := time.Parse(time.RFC3339, run.ScheduledAt)
		if err != nil {
			continue
		}
		if !at.After(now) && now.Sub(at) <= mobileBriefAheadSleepLookback {
			return true
		}
	}
	return false
}

// slotFilled reports whether a stored brief satisfies its slot for the scheduler.
// Only a final brief with written words does. A made-ahead copy is provisional
// (generation.reason made_ahead, the explicit marker; timing is never inferred),
// and a build with no written text never blocks a later build that can write it.
// An awake slot therefore still runs its real build (rule 1, rule 2).
func (s *mobileBriefService) slotFilled(briefID string) bool {
	brief, ok := s.store.load(briefID)
	return (ok && mobileBriefStandsIn(brief)) || s.noTextSpent(briefID)
}

// mobileBriefStandsIn reports whether a stored brief can stand in for its slot:
// a final brief with written words. The scheduler's "made since the slot" and
// "made just before the slot" checks use it too, so a made-ahead copy or a
// no-words build never blocks the slot's real build.
func mobileBriefStandsIn(brief MobileBriefV1) bool {
	return !mobileBriefIsAhead(brief) && mobileBriefHasWrittenText(brief)
}

// noteNoTextBuild counts a real build of a slot that wrote no words (or kept a
// made-ahead copy) and holds the next try mobileBriefNoTextRetry. The count is in
// memory: a restart starts it again.
func (s *mobileBriefService) noteNoTextBuild(now time.Time, briefID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noTextBuilds == nil {
		s.noTextBuilds = map[string]int{}
	}
	s.noTextBuilds[briefID]++
	s.retryAfter, s.retryFor = now.Add(mobileBriefNoTextRetry), briefID
}

// noTextSpent reports whether a slot used its builds without words.
func (s *mobileBriefService) noTextSpent(briefID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.noTextBuilds[briefID] >= mobileBriefNoTextBuilds
}

// slotBlocked reports whether a stored brief already fills a slot. A final brief
// always does. An early brief does only when the computer slept through the slot
// (its words stand); an awake computer's slot build replaces it (R40).
func (s *mobileBriefService) slotBlocked(briefID string, at time.Time) bool {
	if !s.store.exists(briefID) {
		return false
	}
	brief, ok := s.store.load(briefID)
	if !ok || !mobileBriefIsAhead(brief) {
		return true
	}
	s.mu.Lock()
	windows := append([]mobileBriefWakeWindow(nil), s.wakeWindows...)
	s.mu.Unlock()
	for _, window := range windows {
		if !window.SleepAt.IsZero() && !window.WakeAt.IsZero() && !at.Before(window.SleepAt) && !at.After(window.WakeAt) {
			return true
		}
	}
	return false
}

// aheadCheck makes the early brief for the next slot when a trigger allows it:
// the next slot is within the window, no brief was made since the slot before it,
// and no brief fills it yet. A scheduled moment needs a slept-through slot in the
// last 14 days. The build runs under the same state as a scheduled build.
func (s *mobileBriefService) aheadCheck(ctx context.Context, trigger string) {
	settings := s.config.load()
	if !settings.Enabled || ctx.Err() != nil {
		return
	}
	now := s.now()
	loc := s.location
	if loc == nil {
		loc = time.Local
	}
	slot, ok := mobileBriefAheadTarget(now, loc, settings, s.store.load)
	if !ok {
		return
	}
	if trigger == "scheduled" && !s.sleptThroughSlotRecently(now) {
		return
	}
	s.mu.Lock()
	if s.running || s.sleeping || s.stopped {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.runningSince = now
	s.runFinished = make(chan struct{})
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.runningSince = time.Time{}
		if s.runFinished != nil {
			close(s.runFinished)
			s.runFinished = nil
		}
		s.mu.Unlock()
	}()
	gen := &MobileBriefGeneration{
		Reason:      mobileBriefGenerationMadeAhead,
		ScheduledAt: formatMobileBriefTime(slot.ScheduledAt),
		GeneratedAt: formatMobileBriefTime(now),
	}
	ctx = context.WithValue(ctx, mobileBriefAheadDayKey{}, slot.ScheduledAt)
	brief, err := s.generateRefencedOnStored(ctx, slot.Slot, mobileBriefReasonAhead, gen, nil)
	if err != nil {
		log.Printf("[mobile-brief] the early %s brief could not be made", slot.Slot)
		return
	}
	logMobileBriefAhead(trigger, brief.BriefID, false)
	if s.runs != nil {
		s.recordRun(mobileBriefRun{
			BriefID: brief.BriefID, Slot: slot.Slot, ScheduledAt: formatMobileBriefTime(slot.ScheduledAt),
			ScheduledReady: true, Status: mobileBriefRunOK, At: brief.AsOf,
			Early: &mobileBriefEarlyRun{Trigger: trigger, MadeAt: formatMobileBriefTime(now), Replaced: false},
		})
	}
}
