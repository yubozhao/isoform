package main

import (
	"context"
	"errors"
	"log"
	"time"
)

var errMobileBriefTimingDeferred = errors.New("adaptive brief is outside its delivery window")

// mobileBriefDeferredRetry is how long an adaptive slot waits after a build
// that could not be delivered.
const mobileBriefDeferredRetry = 10 * time.Minute

func (s *mobileBriefService) timingData(now time.Time) mobileBriefTimingInput {
	if s.timing != nil {
		return s.timing(now)
	}
	return s.timingInput(now)
}

// Schedule and Health share the scheduler's predictor and durable slot IDs.
// Estimates for tomorrow are refreshed as today's observations become history.
func (s *mobileBriefService) nextAdaptivePrediction(now time.Time, settings mobileBriefSettings) (mobileBriefTimingPrediction, bool) {
	return s.nextAdaptivePredictionFor(now, settings, "")
}

// nextAdaptivePredictionFor is nextAdaptivePrediction for one slot; an empty
// slot means any.
func (s *mobileBriefService) nextAdaptivePredictionFor(now time.Time, settings mobileBriefSettings, slot string) (mobileBriefTimingPrediction, bool) {
	input := s.timingData(now)
	loc := s.location
	if loc == nil {
		loc = time.Local
	}
	s.mu.Lock()
	retryAfter, retryFor := s.retryAfter, s.retryFor
	s.mu.Unlock()
	for day := 0; day < 2; day++ {
		for _, p := range mobileBriefPredictDay(now.In(loc).AddDate(0, 0, day), loc, settings, input) {
			if s.briefSignInAssistant(p.BriefID) != "" || (slot != "" && p.Slot != slot) || p.ScheduledAt.IsZero() || !now.Before(p.Deadline) || s.slotBlocked(p.BriefID, p.ScheduledAt) {
				continue
			}
			if fixed, found := s.slotOccurrence(p.ScheduledAt, settings, p.Slot); found && fixed.BriefID == p.BriefID && s.slotHasRequestedBrief(fixed, settings) {
				continue
			}
			nextEligible := now.In(loc)
			if retryFor == p.BriefID && retryAfter.After(nextEligible) {
				nextEligible = retryAfter.In(loc)
			}
			if p.ScheduledAt.Before(nextEligible) {
				p.ScheduledAt = nextEligible
			}
			if !p.ScheduledAt.Before(p.Deadline) {
				continue
			}
			return p, true
		}
	}
	return mobileBriefTimingPrediction{}, false
}

func (s *mobileBriefService) checkAdaptiveDue(ctx context.Context, now time.Time, settings mobileBriefSettings) {
	input := s.timingData(now)
	if input.Sleeping {
		return
	}
	loc := s.location
	if loc == nil {
		loc = time.Local
	}
	local := now.In(loc)
	since := s.runs.firstSeen(now)
	if enabledAt, err := time.Parse(time.RFC3339Nano, settings.EnabledAt); err == nil && enabledAt.After(since) {
		since = enabledAt
	}
	var missed []mobileBriefSlotOccurrence
	// Yesterday is included so a closed application cannot conceal a missed
	// slot. A slot is missed once its window closes (mobileBriefSlotCloses).
	for _, day := range []int{-1, 0} {
		for _, fixed := range mobileBriefTodaySlots(local.AddDate(0, 0, day), loc, settings) {
			if !fixed.ScheduledAt.Before(since) && !now.Before(mobileBriefSlotCloses(fixed, loc, settings)) && !s.store.exists(fixed.BriefID) {
				missed = append(missed, fixed)
			}
		}
	}
	s.noteMissed(now, missed)
	predictions := mobileBriefPredictDay(now, loc, settings, input)
	slotAt := map[string]time.Time{}
	for _, fixed := range mobileBriefTodaySlots(now, loc, settings) {
		slotAt[fixed.BriefID] = fixed.ScheduledAt
	}
	for _, p := range predictions {
		if p.ScheduledAt.IsZero() && !now.Before(slotAt[p.BriefID]) && !s.store.exists(p.BriefID) {
			s.logNoWindow(p)
		}
	}
	// A slot the computer slept through is made on waking, labelled late,
	// without waiting for the day's work to settle.
	s.mu.Lock()
	windows := append([]mobileBriefWakeWindow(nil), s.wakeWindows...)
	s.mu.Unlock()
	if recovery, ok := mobileBriefWakeSlot(now, loc, settings, s.store.exists, windows); ok && !s.slotHasRequestedBrief(recovery, settings) {
		s.runAdaptiveSlot(ctx, recovery, mobileBriefReasonWake, &MobileBriefGeneration{Reason: "mac_asleep", ScheduledAt: formatMobileBriefTime(recovery.ScheduledAt)})
		return
	}
	for _, p := range predictions {
		if p.ScheduledAt.IsZero() || now.Before(p.ScheduledAt) || !now.Before(p.Deadline) || s.slotBlocked(p.BriefID, slotAt[p.BriefID]) || !s.catchUpAllowed(slotAt[p.BriefID], now, settings) || s.slotHasRequestedBrief(mobileBriefSlotOccurrence{BriefID: p.BriefID, Slot: p.Slot, ScheduledAt: slotAt[p.BriefID]}, settings) {
			continue
		}
		s.runAdaptiveSlot(ctx, p.mobileBriefSlotOccurrence, mobileBriefReasonAdaptive, nil)
		return
	}
}

// runAdaptiveSlot makes one slot's brief and records the run.
func (s *mobileBriefService) runAdaptiveSlot(ctx context.Context, occurrence mobileBriefSlotOccurrence, reason string, generation *MobileBriefGeneration) {
	now := s.now()
	// The prediction is when preparation starts. The run record describes the
	// configured slot, which is when the brief is due to be ready.
	recordedOccurrence := occurrence
	fixedSchedule := false
	if fixed, found := s.slotOccurrence(now, s.config.load(), occurrence.Slot); found && fixed.BriefID == occurrence.BriefID {
		recordedOccurrence = fixed
		fixedSchedule = true
	}
	if s.briefWaitingForSignIn(ctx, occurrence.BriefID) {
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
	if generation == nil {
		if fixed, found := s.slotOccurrence(now, s.config.load(), occurrence.Slot); found && now.Sub(fixed.ScheduledAt) > mobileBriefLateAfter {
			// Isoform was not running at the slot's time, or earlier tries failed.
			generation = &MobileBriefGeneration{Reason: "late", ScheduledAt: formatMobileBriefTime(fixed.ScheduledAt)}
		}
	}
	s.announceBuilding(true)
	recorded := false
	recordCompleted := func(brief MobileBriefV1) {
		run := mobileBriefRun{BriefID: brief.BriefID, Slot: occurrence.Slot, ScheduledAt: formatMobileBriefTime(recordedOccurrence.ScheduledAt), ScheduledReady: fixedSchedule, Status: mobileBriefRunOK, At: brief.AsOf}
		if generation != nil {
			if generation.Reason == "mac_asleep" {
				run.Reason = mobileBriefMissedAsleep
			} else if scheduled, err := time.Parse(time.RFC3339Nano, generation.ScheduledAt); err == nil {
				run.Reason = s.missedReason(scheduled)
			}
		}
		s.recordRun(run)
		recorded = true
	}
	brief, err := s.generateRefencedOnStored(ctx, occurrence.Slot, reason, generation, recordCompleted)
	s.mu.Lock()
	s.running = false
	s.runningSince = time.Time{}
	if s.runFinished != nil {
		close(s.runFinished)
		s.runFinished = nil
	}
	if s.phoneRequest != nil && s.phoneRequest.state == "making" {
		s.lastOnDemand[mobileBriefReasonPhone] = s.now()
		s.phoneRequest = nil
	}
	s.mu.Unlock()
	s.notifyPhoneToday()
	s.announceBuilding(false)
	if errors.Is(err, errMobileBriefTimingDeferred) {
		// A deferred build cost a model call; the next try waits, so a slot
		// that cannot deliver never rebuilds every minute.
		s.mu.Lock()
		s.retryFor, s.retryAfter = occurrence.BriefID, s.now().Add(mobileBriefDeferredRetry)
		s.mu.Unlock()
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.recordFailure(recordedOccurrence, err)
		return
	}
	if !recorded {
		recordCompleted(brief)
	}
}

// logNoWindow says once per slot why a slot whose time has come has no
// delivery window left today (the reason that held it, never content).
func (s *mobileBriefService) logNoWindow(p mobileBriefTimingPrediction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.windowLogged == nil {
		s.windowLogged = map[string]bool{}
	}
	if s.windowLogged[p.BriefID] {
		return
	}
	s.windowLogged[p.BriefID] = true
	log.Printf("[mobile-brief] the %s brief has no delivery window left today: held=%s", p.Slot, p.Held)
}

// Generation can take minutes. Recheck the saved controls, sleep, date, quiet
// hours, current work and cap before making the result visible or publishing it.
func (s *mobileBriefService) adaptiveDeliveryAllowed(started time.Time, slot string) bool {
	settings := s.config.load()
	if !settings.Enabled || !settings.Adaptive {
		return false
	}
	now := s.now()
	loc := s.location
	if loc == nil {
		loc = time.Local
	}
	if started.In(loc).Format("2006-01-02") != now.In(loc).Format("2006-01-02") {
		return false
	}
	s.mu.Lock()
	sleeping := s.sleeping || s.stopped
	s.mu.Unlock()
	input := s.timingData(now)
	if sleeping || input.Sleeping {
		return false
	}
	for _, p := range mobileBriefPredictDay(now, loc, settings, input) {
		if p.Slot == slot {
			// A copy made ahead does not count: the real build replaces it.
			return !p.ScheduledAt.IsZero() && !now.Before(p.ScheduledAt) && now.Before(p.Deadline) && !s.finalCopyExists(p.BriefID)
		}
	}
	return false
}

// finalCopyExists reports a stored brief for id that is not a copy made ahead.
func (s *mobileBriefService) finalCopyExists(id string) bool {
	stored, ok := s.store.load(id)
	if !ok {
		return s.store.exists(id)
	}
	return !mobileBriefIsAhead(stored)
}
