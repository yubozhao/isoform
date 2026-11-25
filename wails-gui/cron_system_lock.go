package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
)

// The app's own hidden schedules (surface "system": Daily News and the
// search on the user's topics) run with research grants. The cron REST API,
// which agent CLIs call from chats and scheduled runs, must not be able to
// rewrite their prompt, schedule or switch, delete them, or run them: an
// injected prompt could otherwise turn one into a run with those grants.
// A research run also never trusts the stored prompt: it rebuilds it from
// the app's own state (appResearchPrompt).

var errSystemScheduleLocked = errors.New("this schedule belongs to Isoform and cannot be changed or run from outside the app")

// errSystemScheduleUnknown refuses an API action when the app cannot tell
// whether the schedule is its own.
var errSystemScheduleUnknown = errors.New("cannot tell whether this schedule belongs to Isoform right now; try again")

// refuseSystemScheduleFromAPI refuses an API action on a system schedule.
// It fails closed: when neither the scheduler nor the stored row can say
// what the schedule is, the action is refused too. Only a schedule that is
// not stored at all passes, and the action then finds nothing to act on.
func (a *App) refuseSystemScheduleFromAPI(id, action string) error {
	name, surface, err := a.cronJobSurface(id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		log.Printf("[cron-api] refused %s of %s: %v", action, id, err)
		return fmt.Errorf("cannot %s this schedule: %w", action, errSystemScheduleUnknown)
	case strings.EqualFold(strings.TrimSpace(surface), todayNewsSurfaceKind),
		strings.EqualFold(strings.TrimSpace(surface), openAnswerSurfaceKind):
		return fmt.Errorf("cannot %s %q: %w", action, name, errSystemScheduleLocked)
	default:
		return nil
	}
}

// cronJobSurface returns a schedule's name and surface from the scheduler,
// or from the stored row when the scheduler is not running or does not hold
// it. sql.ErrNoRows means the schedule is not stored.
func (a *App) cronJobSurface(id string) (string, string, error) {
	if a.cronScheduler != nil {
		if job, err := a.cronScheduler.GetJob(id); err == nil {
			return job.Name, job.SurfaceKind, nil
		}
	}
	if a.store == nil {
		return "", "", errors.New("cron storage is unavailable")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	row, err := a.store.LoadCronJob(dbCtx, id)
	if err != nil {
		return "", "", err
	}
	return row.Name, row.SurfaceKind, nil
}

// appResearchPrompt rebuilds the prompt of a research schedule from the
// app's own state, so the run never uses a prompt stored where something
// else could have changed it. ok is false when there is nothing to search.
func (a *App) appResearchPrompt(job *CronJobState, via string) (string, bool) {
	if job == nil {
		return "", false
	}
	switch job.Name {
	case todayNewsJobName:
		return a.buildTodayNewsPrompt(a.currentUILanguageName(), a.collectUserInterests(), via), true
	case topicFindsJobName:
		if !featureOn(featureBriefExtras) {
			return "", false
		}
		// A daily search checks its switches again when it runs: one that
		// starts after a pause, or after Watch my screen went off, sends
		// nothing. A search the user started with Find new goes ahead.
		finds := a.topicsSvc().store.load().Finds
		if !finds.Manual && !a.topicLabelsMayLeave() {
			return "", false
		}
		var labels []string
		for _, sent := range finds.Sent {
			labels = append(labels, sent.Label)
		}
		if len(labels) == 0 {
			return "", false
		}
		return buildTopicFindsPromptWithSearches(a.currentUILanguageName(), labels, topicFindsSearches(finds.Sent), via), true
	default:
		if strings.HasPrefix(job.Name, openAnswerJobPrefix) {
			id := openAnswerIDFromJobName(job.Name)
			answer, err := a.store.GetOpenAnswer(context.Background(), id)
			if err != nil || answer.State != "open" || answer.WindowStart <= 0 || a.IsClosedEyesModeEnabled() || !a.dailyTopicSearchAllowed() {
				return "", false
			}
			return openAnswerCheckPrompt(answer, a.currentUILanguageName()), true
		}
		return "", false
	}
}
