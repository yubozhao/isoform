package main

import (
	"context"
	"log"
	"time"
)

const (
	// timelineSummaryQueueBatch is the fetch page size while draining the
	// queue — a pass keeps pulling pages until no due runs remain, so this
	// bounds memory per query, not throughput.
	timelineSummaryQueueBatch = 16
	// timelineAutoSummarizeHorizon bounds background quota: only runs that
	// ended within this window are auto-summarized. Older runs (including the
	// migration backfill) generate on-demand when the user selects them.
	timelineAutoSummarizeHorizon = 48 * time.Hour
	// timelineOpenRunQuiet is how long the open run must sit without new
	// events before the worker summarizes it early; growth after that makes
	// it eligible again, so the summary catches up when the run closes.
	timelineOpenRunQuiet = 10 * time.Minute
	// timelineSummaryMaxAttempts caps transient retries before a run is
	// parked as failed_permanent.
	timelineSummaryMaxAttempts = 4
)

// KickTimelineSummaries is kept for the session-save path: run summaries are
// derived work and wait for the hourly batch, so a save needs no wake-up.
func (c *ActivityTriager) KickTimelineSummaries() {}

// runTimelineSummaryPassGuarded runs one worker pass under timelineSummaryMu
// (non-blocking — a still-running pass is already draining the queue and will
// pick up whatever prompted this wake-up on its next page fetch).
func (c *ActivityTriager) runTimelineSummaryPassGuarded(ctx context.Context) {
	if !c.timelineSummaryMu.TryLock() {
		return
	}
	defer c.timelineSummaryMu.Unlock()
	c.runTimelineSummaryPass(ctx)
}

// notifyTimelineGroupsUpdated tells the UI to refetch timeline groups.
func (c *ActivityTriager) notifyTimelineGroupsUpdated() {
	if c.app != nil && c.app.ctx != nil {
		c.app.emitBrowserEvent("activity:timeline-groups-updated")
	}
}

// runTimelineSummaryPass self-heals the run table, then drains the due queue
// newest-first: eligible per-app runs are summarized in batches (one model
// call per timelineSummaryBatchSize runs) until nothing is due. The UI is notified after each run so
// titles stream into the sidebar as the queue drains. Failures are recorded
// on the run (with backoff) and never abort the pass.
func (c *ActivityTriager) runTimelineSummaryPass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[timeline-summary] recovered from panic: %v", r)
		}
	}()
	store := c.activityStore()
	if store == nil || c.app == nil {
		return
	}

	// Self-heal: catch up runs for any events persisted without a reconcile
	// (crash between session commit and reconcile, sessions written outside
	// extractSession). The reconcile's fast path makes this free when the
	// table is already current.
	reconciled, err := store.ReconcileTimelineRuns()
	if err != nil {
		log.Printf("[timeline-summary] self-heal reconcile: %v", err)
	} else if reconciled {
		c.notifyTimelineGroupsUpdated()
	}

	// The runner is assigned asynchronously after app startup; without it no
	// generation can succeed, so skip the pass without consuming attempts.
	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()
	if runner == nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		now := time.Now().UnixMilli()
		due, err := store.GetTimelineRunsDueForSummary(
			now,
			timelineAutoSummarizeHorizon.Milliseconds(),
			timelineOpenRunQuiet.Milliseconds(),
			timelineSummaryQueueBatch,
		)
		if err != nil {
			log.Printf("[timeline-summary] load due runs: %v", err)
			return
		}
		if len(due) == 0 {
			return
		}
		progressed := false
		var claimedRuns []ActivityTimelineRunRow
		for _, run := range due {
			select {
			case <-ctx.Done():
				return
			default:
			}
			claimed, err := store.ClaimTimelineRunForSummary(run.ID, []string{
				timelineSummaryStatusNone,
				timelineSummaryStatusFailedTransient,
				timelineSummaryStatusDone,
				timelineSummaryStatusSkipped,
			})
			if err != nil {
				log.Printf("[timeline-summary] claim run %s: %v", run.ID, err)
				continue
			}
			if !claimed {
				continue
			}
			progressed = true
			claimedRuns = append(claimedRuns, run)
		}
		// One model call covers up to timelineSummaryBatchSize runs.
		// Protected-app runs are skipped inside prepareTimelineRunSummary.
		c.app.generateTimelineRunSummaries(ctx, claimedRuns, c.notifyTimelineGroupsUpdated)
		// Every fetched run was claimed by the on-demand path in the
		// meantime — nothing left for this pass to make progress on.
		if !progressed {
			return
		}
	}
}
