package main

import (
	"context"
	"strconv"
	"time"
)

// Derived model work runs on a clock, not on events (Bo, 2026-11-12): daily by
// default, hourly where the day moves. What the person asks for (a chat, a
// brief on request) stays immediate. The last run of each background job is
// kept in UI state, so restarting the app does not run it again. The
// intervals are in cadence.go.

// derivedCadencePrefix keys the last run time of each job in UI state.
const derivedCadencePrefix = "derived_cadence."

// derivedRunDue reports whether job last ran at least every ago, or never.
func (a *App) derivedRunDue(job string, every time.Duration, now time.Time) bool {
	if a == nil || a.store == nil {
		return true
	}
	raw, err := a.store.GetUIState(context.Background(), derivedCadencePrefix+job)
	if err != nil || raw == "" {
		return true
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return true
	}
	return now.Sub(time.UnixMilli(ms)) >= every
}

// recordDerivedRun stores now as job's last run.
func (a *App) recordDerivedRun(job string, now time.Time) {
	if a == nil || a.store == nil {
		return
	}
	_ = a.store.SetUIState(context.Background(), derivedCadencePrefix+job, strconv.FormatInt(now.UnixMilli(), 10))
}

// claimDerivedRun records a run and reports true when job is due.
func (a *App) claimDerivedRun(job string, every time.Duration, now time.Time) bool {
	if !a.derivedRunDue(job, every, now) {
		return false
	}
	a.recordDerivedRun(job, now)
	return true
}
