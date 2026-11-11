package main

import (
	"time"

	"wails-gui/internal/platform/activitymon"
)

// A wake window is observed system sleep, never inferred from a late tick or
// an unlock. Keep it for retries until its missing slots have been recovered.
type mobileBriefWakeWindow struct {
	SleepAt time.Time
	WakeAt  time.Time
}

func (a *App) mobileBriefSystemStateChanged(change activitymon.SystemStateChange) {
	a.mobileBridgeMu.Lock()
	source := a.mobileBriefSvc
	a.mobileBridgeMu.Unlock()
	if svc, ok := source.(*mobileBriefService); ok {
		svc.systemStateChanged(change)
	}
}

func (s *mobileBriefService) systemStateChanged(change activitymon.SystemStateChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.systemStateChangedLocked(change)
}

func (s *mobileBriefService) systemStateChangedLocked(change activitymon.SystemStateChange) {
	if s.stopped {
		return
	}
	switch change.Kind {
	case activitymon.SystemStateLocked:
		s.queueAhead("lock")
	case activitymon.SystemStateScreensSleep:
		s.queueAhead("screens_sleep")
	case activitymon.SystemStateSleep:
		s.sleeping = true
		s.sleepAt = change.At
	case activitymon.SystemStateWake:
		s.sleeping = false
		if !change.SleepAt.IsZero() {
			s.sleepAt = change.SleepAt
		}
		if !s.sleepAt.IsZero() && !change.At.Before(s.sleepAt) {
			// Keep discrete intervals: the Mac may have been awake between
			// naps. Only today's slots are recovered, so older windows expire.
			kept := s.wakeWindows[:0]
			for _, window := range s.wakeWindows {
				if window.WakeAt.After(change.At.Add(-24 * time.Hour)) {
					kept = append(kept, window)
				}
			}
			s.wakeWindows = append(kept, mobileBriefWakeWindow{SleepAt: s.sleepAt, WakeAt: change.At})
		}
		s.sleepAt = time.Time{}
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// Recover today's earliest unfilled slot crossed during sleep while its window
// is open. A made-ahead copy is not filled: an awake slot builds its own brief.
// A schedule enabled after the slot never claims the computer missed that slot.
func mobileBriefWakeSlot(now time.Time, loc *time.Location, settings mobileBriefSettings, filled func(string) bool, windows []mobileBriefWakeWindow) (mobileBriefSlotOccurrence, bool) {
	enabledAt, _ := time.Parse(time.RFC3339Nano, settings.EnabledAt)
	for _, occurrence := range mobileBriefTodaySlots(now, loc, settings) {
		at := occurrence.ScheduledAt
		if at.After(now) || at.Before(enabledAt) || filled(occurrence.BriefID) || !now.Before(mobileBriefSlotCloses(occurrence, loc, settings)) {
			continue
		}
		for _, window := range windows {
			if !window.SleepAt.IsZero() && !window.WakeAt.IsZero() && !at.Before(window.SleepAt) && !at.After(window.WakeAt) {
				return occurrence, true
			}
		}
	}
	return mobileBriefSlotOccurrence{}, false
}

// mobileBriefLateMessage labels a brief made after its time because the
// computer was asleep then, in plain words.
func mobileBriefLateMessage(slot, language string) string {
	lang := "en"
	if normalizeUILanguage(language) == "zh-CN" {
		lang = "zh"
	}
	switch slot {
	case mobileBriefSlotMorning:
		return pick(lang, "Your morning brief, a bit late.", "你的早间简报，稍晚送达。")
	case mobileBriefSlotEvening:
		return pick(lang, "Your evening brief, a bit late.", "你的晚间简报，稍晚送达。")
	default:
		return pick(lang, "Your brief, a bit late.", "你的简报，稍晚送达。")
	}
}
