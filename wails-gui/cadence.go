package main

import (
	"context"
	"errors"
	"log"
	"time"
)

// Cadences for derived background work (Bo, 2026-11-12): capture stays
// real-time; the work built from it runs daily by default and hourly where
// the day moves. Every interval lives here so it is easy to tune.
//
// What the person asks for never waits for these: sending a chat, "Make my
// brief now", "Refresh now", "Run now". An explicit privacy action applies at
// once. A scheduled brief may run the activity batch early, once, before it
// gathers (prerunActivityForBrief).
//
// Unchanged on purpose: capture (screen, OCR, meetings, audio, signals),
// calendar sync, the brief scheduler tick, relay and presence heartbeats,
// watchdog and health checks, the nightly auto-summary.
const (
	cadenceDaily  = 24 * time.Hour
	cadenceHourly = time.Hour
	// cadenceCheck is how often a daily job looks at its clock. The check
	// reads the stored last run; the work itself runs once a day.
	cadenceCheck = time.Hour

	// Activity: triage, memory, run summaries, the composer, semantic
	// linking and today's threads run in one hourly batch.
	cadenceActivityBatch  = cadenceHourly
	cadenceThreadOutcomes = cadenceDaily

	cadenceMemoryDreamCheck     = time.Minute // a clock read for the nightly slot; the run is daily
	cadenceSelfDream            = cadenceDaily
	cadencePurposeUnderstanding = cadenceDaily
	cadenceEngagementPurpose    = cadenceDaily
	cadenceEngagementCapture    = cadenceHourly
	cadenceBackgroundLearning   = cadenceDaily
	cadenceSuggestions          = cadenceDaily
	cadenceInterests            = 20 * time.Hour // once a day, a little early so a day is never skipped
	cadenceActivityEmbedding    = cadenceHourly
	cadenceAssistantHistory     = cadenceHourly
	cadenceMessageScan          = cadenceHourly
	cadenceSlackMemory          = cadenceHourly
	cadenceReminders            = cadenceHourly
	cadenceContacts             = cadenceDaily
	// cadenceKnowHowBackfill is how often the one-time know-how kind backfill
	// tries again after a failure, across launches.
	cadenceKnowHowBackfill      = cadenceDaily
	cadencePhoneToday           = cadenceHourly
	cadenceBriefPrerunPerSlot   = 20 * time.Hour
	cadenceBriefPrerunMinBudget = 5 * time.Minute
	// cadenceBriefPrerunMaxWait bounds how long a scheduled brief waits for
	// its activity pre-run.
	cadenceBriefPrerunMaxWait = 3 * time.Minute
)

// prerunActivityForBrief runs the hourly activity batch once before a
// scheduled brief gathers, so the brief reads the last hour too. A brief the
// person asked for does not wait, and each scheduled slot pre-runs once a day.
func (a *App) prerunActivityForBrief(ctx context.Context, slot string, asOf time.Time) {
	if a == nil || slot == "" || slot == mobileBriefSlotOnRequest || ctx.Err() != nil {
		return
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < cadenceBriefPrerunMinBudget {
		return
	}
	triager := a.currentActivityTriager()
	if triager == nil {
		return
	}
	if !a.claimDerivedRun("brief_prerun."+slot, cadenceBriefPrerunPerSlot, asOf) {
		return
	}
	// The pre-run never holds the brief up for long: past its budget the
	// brief goes ahead with what the hour before already summarized.
	prerunCtx, cancel := context.WithTimeout(ctx, cadenceBriefPrerunMaxWait)
	defer cancel()
	started := time.Now()
	err := triager.runBatch(prerunCtx, "brief")
	log.Printf("[brief-prerun] slot=%s ms=%d timed_out=%t failed=%t", slot, time.Since(started).Milliseconds(), errors.Is(prerunCtx.Err(), context.DeadlineExceeded), err != nil)
}

func (a *App) currentActivityTriager() *ActivityTriager {
	if a == nil || a.activity == nil {
		return nil
	}
	a.activity.mu.Lock()
	defer a.activity.mu.Unlock()
	return a.activity.triager
}
