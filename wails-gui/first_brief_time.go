package main

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"
)

// The first brief (computer setup v1.0.0 rule 10): it comes at the next
// brief time today if that is at least an hour away; otherwise a first brief
// is made an hour after setup. No one waits until tomorrow. The time is saved
// when setup finishes, so it holds across a restart, and Today's first-day
// band says it (computer-today v1.1.0 rule 1, GetFirstBriefTime).

// firstBriefTimeKey holds the saved time as {"at": RFC3339, "slot": ...}.
// slot is "morning" or "evening" when the scheduled brief is the first one,
// and "" for a first brief made on its own an hour after setup.
const firstBriefTimeKey = "first_run.brief_at.v1"

// firstBriefMadeKey records that the one-off first brief was asked for.
const firstBriefMadeKey = "first_run.brief_made.v1"

// firstBriefLead is how far away the next brief time must be to be the first.
const firstBriefLead = time.Hour

// firstBriefSlotGrace is how long after its slot time the scheduled first
// brief may take before a first brief is made on its own.
const firstBriefSlotGrace = 20 * time.Minute

// FirstBriefTime is when the first brief comes. At is empty once a brief has
// been made since setup (the band then shows the brief itself).
type FirstBriefTime struct {
	At   string `json:"at,omitempty"`
	Slot string `json:"slot,omitempty"`
}

// firstBriefAt picks the first brief's time for setup finishing at now, with
// the brief times "HH:MM" of the schedule (morning, evening).
func firstBriefAt(now time.Time, times map[string]string) FirstBriefTime {
	var best time.Time
	slot := ""
	// The next brief time today; it is the first brief only when it is at
	// least an hour away (7:00 seen from 6:30 is not: the brief is at 7:30).
	for _, name := range []string{mobileBriefSlotMorning, mobileBriefSlotEvening} {
		at, ok := clockToday(now, times[name])
		if !ok || at.Before(now) {
			continue
		}
		if best.IsZero() || at.Before(best) {
			best, slot = at, name
		}
	}
	if best.IsZero() || best.Sub(now) < firstBriefLead {
		best, slot = now.Add(firstBriefLead), ""
	}
	return FirstBriefTime{At: best.Format(time.RFC3339), Slot: slot}
}

// clockToday is "HH:MM" on now's day, in now's zone.
func clockToday(now time.Time, clock string) (time.Time, bool) {
	parts := strings.Split(strings.TrimSpace(clock), ":")
	if len(parts) != 2 {
		return time.Time{}, false
	}
	hour, err1 := strconv.Atoi(parts[0])
	minute, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return time.Time{}, false
	}
	y, m, d := now.Date()
	return time.Date(y, m, d, hour, minute, 0, 0, now.Location()), true
}

// scheduleFirstBrief saves the first brief's time when setup finishes. It is
// saved once; a later call keeps the first answer.
func (a *App) scheduleFirstBrief(ctx context.Context, now time.Time) {
	if a == nil || a.store == nil {
		return
	}
	if raw, _ := a.store.GetUIState(ctx, firstBriefTimeKey); raw != "" {
		return
	}
	times := map[string]string{mobileBriefSlotMorning: mobileBriefDefaultMorning, mobileBriefSlotEvening: mobileBriefDefaultEvening}
	if svc := a.mobileBriefSource(); svc != nil {
		if schedule, err := svc.Schedule(); err == nil && schedule.Enabled {
			times[mobileBriefSlotMorning], times[mobileBriefSlotEvening] = schedule.Morning, schedule.Evening
		} else if err == nil {
			// The schedule is off: no slot comes on its own, so the first brief
			// is made an hour after setup.
			times = nil
		}
	}
	first := firstBriefAt(now, times)
	raw, _ := json.Marshal(first)
	if err := a.store.SetUIState(ctx, firstBriefTimeKey, string(raw)); err != nil {
		log.Printf("[first-run] saving the first brief time: %v", err)
		return
	}
	log.Printf("[first-run] first brief at=%s slot=%q", first.At, first.Slot)
}

// savedFirstBrief reads the saved first brief time.
func (a *App) savedFirstBrief(ctx context.Context) (FirstBriefTime, time.Time, bool) {
	if a == nil || a.store == nil {
		return FirstBriefTime{}, time.Time{}, false
	}
	raw, _ := a.store.GetUIState(ctx, firstBriefTimeKey)
	var first FirstBriefTime
	if raw == "" || json.Unmarshal([]byte(raw), &first) != nil {
		return FirstBriefTime{}, time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, first.At)
	if err != nil {
		return FirstBriefTime{}, time.Time{}, false
	}
	return first, at, true
}

// firstBriefMade reports whether a brief has been made since setup.
func (a *App) firstBriefMade(ctx context.Context) bool {
	if done, _ := a.store.GetUIState(ctx, firstBriefMadeKey); done == "true" {
		return true
	}
	installed := a.firstWeekInstall(ctx)
	svc := a.mobileBriefSource()
	if svc == nil || installed.IsZero() {
		return false
	}
	schedule, err := svc.Schedule()
	if err != nil || schedule.LastGeneratedAt == "" {
		return false
	}
	last, err := time.Parse(time.RFC3339, schedule.LastGeneratedAt)
	return err == nil && !last.Before(installed)
}

// GetFirstBriefTime is when the first brief comes, for Today's first-day
// band; empty once a brief has been made since setup, or before setup ends.
func (a *App) GetFirstBriefTime() (FirstBriefTime, error) {
	ctx := context.Background()
	first, _, ok := a.savedFirstBrief(ctx)
	if !ok || a.firstBriefMade(ctx) {
		return FirstBriefTime{}, nil
	}
	return first, nil
}

// firstBriefDue makes the first brief when its time has come and no brief
// has been made since setup: the one-off at its time, a scheduled slot
// that has not arrived within firstBriefSlotGrace. The engagement pass calls
// it every minute, so it holds across a restart.
func (a *App) firstBriefDue(ctx context.Context, now time.Time) {
	first, at, ok := a.savedFirstBrief(ctx)
	if !ok {
		return
	}
	due := at
	if first.Slot != "" {
		due = at.Add(firstBriefSlotGrace)
	}
	if now.Before(due) || a.firstBriefMade(ctx) || a.mobileBriefSource() == nil {
		return
	}
	if err := a.store.SetUIState(ctx, firstBriefMadeKey, "true"); err != nil {
		return
	}
	go func() {
		result, err := a.GenerateMobileBriefNow()
		log.Printf("[first-run] first brief made: slot=%q status=%s err=%v", first.Slot, result.Status, err)
	}()
}
