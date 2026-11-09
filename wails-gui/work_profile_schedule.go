package main

import (
	"context"
	"time"

	"wails-gui/internal/platform/activitymon"
)

var workProfileSchedulerKick = make(chan struct{}, 1)

func workProfileKick() {
	select {
	case workProfileSchedulerKick <- struct{}{}:
	default:
	}
}

// workProfileWeeklySlot is the most recent Monday at 09:00 in local time.
func workProfileWeeklySlot(now time.Time) time.Time {
	year, month, day := now.Date()
	slot := time.Date(year, month, day, 9, 0, 0, 0, now.Location())
	days := (int(now.Weekday()) + 6) % 7
	slot = slot.AddDate(0, 0, -days)
	if slot.After(now) {
		slot = slot.AddDate(0, 0, -7)
	}
	return slot
}

func (a *App) startWorkProfileScheduler(ctx context.Context) {
	activitymon.StartSystemStateObservers()
	wake, unsubscribe := activitymon.SubscribeWake()
	go func() {
		defer unsubscribe()
		runWorkProfileSchedule(ctx, wake, workProfileSchedulerKick, time.Now, a.checkWorkProfileSchedule)
	}()
}

func runWorkProfileSchedule(ctx context.Context, wake, kick <-chan struct{}, now func() time.Time, check func()) {
	for {
		check()
		current := now()
		next := workProfileWeeklySlot(current).AddDate(0, 0, 7)
		timer := time.NewTimer(next.Sub(current))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-wake:
		case <-kick:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

func (a *App) checkWorkProfileSchedule() {
	path, err := workProfilePath()
	if err != nil {
		return
	}
	a.mu.Lock()
	ready := a.runner != nil
	a.mu.Unlock()
	if !ready {
		return
	}
	now := time.Now()
	slot := workProfileWeeklySlot(now).Format(time.RFC3339)
	workProfileStateMu.Lock()
	state, err := workProfileReadState(path)
	if err != nil || state.Approved == nil || state.ApprovedVersion == 0 {
		workProfileStateMu.Unlock()
		return
	}
	reconcile := state.Pending == nil && state.Ready != nil
	due := state.LastWeeklySlot != slot
	if !reconcile && !due {
		workProfileStateMu.Unlock()
		return
	}
	workProfileStateMu.Unlock()
	if !due {
		slot = ""
	}
	_, _ = a.startWorkProfileDraft(false, slot)
}
