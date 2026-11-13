package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// activityComposerBatchSize is how many runs one composer call places. The
// hourly batch made one call per run (18 of 29 calls in an hour of Bo's app,
// 2026-11-13); ten runs a call keeps a busy hour at about three calls.
const (
	activityComposerBatchSize = 10
	// activityComposerBatchTimeout allows for ten routing decisions in one
	// reply; one decision keeps activityComposerTimeout.
	activityComposerBatchTimeout = 5 * time.Minute
)

// composerBatchResponse is the batched reply: one routing decision per run,
// in run order.
type composerBatchResponse struct {
	Runs []struct {
		RunID string `json:"runId"`
		composerResponse
	} `json:"runs"`
}

// buildComposerBatchPrompt places several runs, in time order, in one call.
// Candidates are listed once; each run keeps the exact section the one-run
// prompt sends. A later run may join an activity an earlier run in the list
// started: its id is "act_" + that run's id, the id composeApply gives it.
func buildComposerBatchPrompt(items []composerItem, languageInstruction string) string {
	var b strings.Builder
	if s := strings.TrimSpace(languageInstruction); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	b.WriteString(composerInstructions())
	b.WriteString(fmt.Sprintf(`This time there are %d new app runs, in time order, instead of one. Place them one after another, as if each earlier run were already placed:
- A run may join one of the candidate activities below.
- A run may also join the activity an earlier run in this list started with route "new". Its id is "act_" followed by that run's runId; use it as joinBucketId and give it an admission entry like any candidate.
- Judge every run on its own evidence with the criteria above. Being next to each other in this list is not evidence.

`, len(items)))

	seen := map[string]bool{}
	var buckets []ActivityRow
	for _, item := range items {
		for _, bucket := range item.candidates {
			if !seen[bucket.ID] {
				seen[bucket.ID] = true
				buckets = append(buckets, bucket)
			}
		}
	}
	b.WriteString(composerCandidatesSection(buckets))
	for i, item := range items {
		b.WriteString(fmt.Sprintf("\nNew app run %d of %d to place (runId=%s):\n", i+1, len(items), item.run.ID))
		b.WriteString(composerRunSection(item.run, item.summary, item.transition))
	}
	b.WriteString(`
Respond with ONLY this JSON object, one entry per run in the same order:
{"runs": [{"runId": "<runId>", "route": "skip" | "join" | "new", "joinBucketId": "...", "merge": [], "confidence": "...", "reason": "...", "admission": [...], "bucket": {...}}]}
Each entry has the fields and values of the one-run object below.`)
	b.WriteString(composerResponseShape())
	return redactActivityModelText(b.String())
}

// composeRuns places ready runs in order: runs settled without a model first
// keep their place, the rest go to the model activityComposerBatchSize at a
// time. It reports whether anything changed and whether the pass must stop
// (a transient model failure leaves the runs ready for the next batch).
func (c *ActivityTriager) composeRuns(ctx context.Context, store *ActivityStore, runs []ActivityTimelineRunRow, now func() int64) (progressed, stop bool) {
	var pending []composerItem
	flush := func() bool {
		if len(pending) == 0 {
			return true
		}
		changed, ok := c.composeBatch(ctx, store, pending, now)
		pending = nil
		progressed = progressed || changed
		return ok
	}
	for _, run := range runs {
		if ctx.Err() != nil {
			return progressed, true
		}
		if activitySourceAppIsSensitive(run.AppKey, run.AppName) {
			// The one-run path owns this rare case unchanged.
			if !flush() {
				return progressed, true
			}
			changed, err := c.composeRun(ctx, run, now())
			if !c.composeOutcome(store, run, changed, err, now()) {
				return progressed, true
			}
			progressed = progressed || changed
			continue
		}
		item, changed, err := c.prepareComposeRun(run, now())
		if item == nil {
			if !c.composeOutcome(store, run, changed, err, now()) {
				return progressed, true
			}
			progressed = progressed || changed
			continue
		}
		pending = append(pending, *item)
		if len(pending) == activityComposerBatchSize && !flush() {
			return progressed, true
		}
	}
	if !flush() {
		return progressed, true
	}
	return progressed, false
}

// composeOutcome keeps the one-run error handling: a permanent failure skips
// the run; a transient one stops the pass.
func (c *ActivityTriager) composeOutcome(store *ActivityStore, run ActivityTimelineRunRow, _ bool, err error, now int64) bool {
	if err == nil {
		return true
	}
	if isPermanentExtractionFailure(err) {
		// Permanent (content moderation etc.) recurs identically — skip
		// this run by linking it to no bucket at its current count.
		if lerr := store.UpsertActivityRunLink(run.ID, "", run.SummarizedEventCount, now); lerr != nil {
			log.Printf("[activity-composer] skip-mark run %s: %v", run.ID, lerr)
		}
		return true
	}
	log.Printf("[activity-composer] compose run %s (will retry): %v", run.ID, err)
	return false
}

// composeBatch makes one call for a batch and applies the decisions in run
// order. A batch of one, a permanently rejected batch prompt and runs the
// reply leaves out go through the one-run path, so no run is lost.
func (c *ActivityTriager) composeBatch(ctx context.Context, store *ActivityStore, items []composerItem, now func() int64) (progressed, ok bool) {
	single := func(list []composerItem) bool {
		for _, item := range list {
			changed, err := c.composeRun(ctx, item.run, now())
			if !c.composeOutcome(store, item.run, changed, err, now()) {
				return false
			}
			progressed = progressed || changed
		}
		return true
	}
	if len(items) == 1 {
		return progressed, single(items)
	}
	prompt := buildComposerBatchPrompt(items, c.composerLanguageInstruction()) + c.composerNamingCorrections()
	var reply composerBatchResponse
	err := c.runComposerJSON(ctx, prompt, activityComposerBatchTimeout, &reply)
	if err != nil {
		if isPermanentExtractionFailure(err) {
			return progressed, single(items)
		}
		log.Printf("[activity-composer] batch of %d runs (will retry): %v", len(items), err)
		return progressed, false
	}
	byRun := make(map[string]composerResponse, len(reply.Runs))
	for _, entry := range reply.Runs {
		byRun[strings.TrimSpace(entry.RunID)] = entry.composerResponse
	}
	var started []ActivityRow
	var missing []composerItem
	for _, item := range items {
		resp, found := byRun[item.run.ID]
		if !found {
			missing = append(missing, item)
			continue
		}
		changed, err := c.applyComposerResponse(item, resp, started, now())
		if !c.composeOutcome(store, item.run, changed, err, now()) {
			return progressed, false
		}
		progressed = progressed || changed
		// An activity this run started is a candidate for the runs after it.
		if row, err := store.GetActivityByID("act_" + item.run.ID); err == nil && row != nil {
			started = append(started, *row)
		}
	}
	if len(missing) > 0 {
		log.Printf("[activity-composer] batch reply left out %d of %d runs; placing them one at a time", len(missing), len(items))
		return progressed, single(missing)
	}
	return progressed, true
}
