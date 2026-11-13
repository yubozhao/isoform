package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
)

// timelineSummaryBatchSize is how many app runs one background summary call
// covers. The hourly batch used to make one call per run (22 to 46 an hour in
// Bo's log); the prompt for eight runs stays well under the model's input.
const timelineSummaryBatchSize = 8

// timelineBatchSummaryResponse is the batched reply: one summary per run,
// keyed by its groupId.
type timelineBatchSummaryResponse struct {
	Runs []struct {
		GroupID string `json:"groupId"`
		timelineGroupSummaryResponse
	} `json:"runs"`
}

// buildTimelineBatchSummaryPrompt asks for every run's summary in one reply.
// Each run keeps the exact section the one-run prompt sends.
func buildTimelineBatchSummaryPrompt(jobs []timelineSummaryJob, languageInstruction string) string {
	var b strings.Builder
	b.WriteString(timelineSummaryInstructions(languageInstruction))
	b.WriteString(fmt.Sprintf("(%d app runs follow. Summarize each one on its own with the rules above.)\n", len(jobs)))
	for i, job := range jobs {
		if i > 0 {
			b.WriteString("\nApp run:\n")
		}
		b.WriteString(job.section)
	}
	b.WriteString(`
Return one JSON object with every run, each with its groupId and the JSON shape above:
{"runs": [{"groupId": "<groupId>", "title": "...", "description": "...", "activityType": "...", "entities": [], "timeline": []}]}
Respond immediately with the JSON object.`)
	return redactActivityModelText(b.String())
}

// generateTimelineRunSummaries summarizes claimed runs in calls of
// timelineSummaryBatchSize. A run the reply leaves out takes the usual
// failure path and is retried by a later batch.
func (a *App) generateTimelineRunSummaries(ctx context.Context, runs []ActivityTimelineRunRow, after func()) {
	var jobs []timelineSummaryJob
	for _, run := range runs {
		job, err := a.prepareTimelineRunSummary(run)
		if job == nil {
			if err != nil && !errors.Is(err, errTimelineRunProtected) {
				log.Printf("[timeline-summary] prepare run %s: %v", run.ID, err)
			}
			continue
		}
		if strings.TrimSpace(job.section) == "" {
			_ = a.activity.store.MarkTimelineRunSummarySkipped(run.ID, run.EventCount)
			continue
		}
		jobs = append(jobs, *job)
	}
	for start := 0; start < len(jobs); start += timelineSummaryBatchSize {
		if ctx.Err() != nil {
			return
		}
		end := min(start+timelineSummaryBatchSize, len(jobs))
		a.summarizeTimelineJobs(ctx, jobs[start:end])
		if after != nil {
			after()
		}
	}
}

func (a *App) summarizeTimelineJobs(ctx context.Context, jobs []timelineSummaryJob) {
	if len(jobs) == 1 {
		if _, err := a.generateTimelineRunSummary(ctx, jobs[0].run); err != nil && !errors.Is(err, errTimelineRunProtected) {
			log.Printf("[timeline-summary] generate for run %s (app %s): %v", jobs[0].run.ID, jobs[0].run.AppName, err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timelineGroupSummaryTimeout)
	defer cancel()
	model := a.lightTaskModel()
	prompt := buildTimelineBatchSummaryPrompt(jobs, responseLanguageInstruction(a.currentUILanguageName()))
	raw, err := a.runTimelineGroupSummaryPrompt(ctx, prompt, nil, model)
	var resp timelineBatchSummaryResponse
	if err == nil {
		if perr := json.Unmarshal([]byte(stripCodeFences(strings.TrimSpace(raw))), &resp); perr != nil {
			if perr := unmarshalJSONBestEffort(stripCodeFences(strings.TrimSpace(raw)), &resp); perr != nil {
				err = fmt.Errorf("parse timeline batch summary response: %w", perr)
			}
		}
	}
	if err != nil {
		log.Printf("[timeline-summary] batch of %d runs: %v", len(jobs), err)
		for _, job := range jobs {
			a.markTimelineRunSummaryError(job.run, err)
		}
		return
	}
	byID := make(map[string]timelineGroupSummaryResponse, len(resp.Runs))
	for _, run := range resp.Runs {
		byID[strings.TrimSpace(run.GroupID)] = run.timelineGroupSummaryResponse
	}
	for _, job := range jobs {
		summary, ok := byID[job.group.ID]
		if !ok {
			a.markTimelineRunSummaryError(job.run, fmt.Errorf("timeline batch summary left out run %s", job.run.ID))
			continue
		}
		// Each run keeps only its own reply, so forgetting one run's source
		// never leaves its text in another run's stored response.
		own, _ := json.Marshal(summary)
		if _, err := a.finishTimelineRunSummary(job, summary, model, string(own)); err != nil {
			log.Printf("[timeline-summary] store run %s: %v", job.run.ID, err)
		}
	}
}
