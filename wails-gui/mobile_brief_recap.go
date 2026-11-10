package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/platform/activitymon"
)

// The morning brief recaps the last day the Mac was in use, so the 07:00
// brief is never empty for someone who worked yesterday: the activity
// summaries of that day, the one-time work that moved since, the work still
// open, and the news the last Daily News run found. Only the recap and the
// work reach back to that day (RecapStart); meetings, browsing and the rest
// keep the brief's own window, so yesterday never reads as today.
const (
	mobileBriefRecapSource = "summary"
	mobileBriefNewsSource  = "news"
	// Keep more candidates for a full workday; the wire section and shared
	// evidence budgets still bound how many are displayed.
	mobileBriefRecapLimit = 12
	mobileBriefNewsLimit  = 3
	mobileBriefNewsFresh  = 24 * time.Hour
)

// mobileBriefMorningStart is where the morning recap starts: the beginning
// of the last day the Mac was in use when nothing happened since local
// midnight (yesterday, or Friday on a Monday), never more than seven days
// back, and never later than the brief's own window start.
func mobileBriefMorningStart(asOf, windowStart time.Time, loc *time.Location, last time.Time, ok bool) time.Time {
	if !ok {
		return windowStart
	}
	if loc == nil {
		loc = time.Local
	}
	local := asOf.In(loc)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	if !last.Before(midnight) {
		return windowStart
	}
	lastLocal := last.In(loc)
	start := time.Date(lastLocal.Year(), lastLocal.Month(), lastLocal.Day(), 0, 0, 0, 0, loc)
	if floor := asOf.Add(-mobileBriefMaxWindow); start.Before(floor) {
		start = floor
	}
	if start.Before(windowStart) {
		return start
	}
	return windowStart
}

// recapStart is where the recap and the moved work start: RecapStart in the
// morning, otherwise the brief's window.
func (in mobileBriefInput) recapStart() time.Time {
	if !in.RecapStart.IsZero() && in.RecapStart.Before(in.WindowStart) {
		return in.RecapStart
	}
	return in.WindowStart
}

// inRecapWindow reports a time inside the recap's window.
func (in mobileBriefInput) inRecapWindow(at time.Time) bool {
	return !at.IsZero() && !at.Before(in.recapStart()) && at.Before(in.AsOf)
}

// mobileBriefLastActive is when the Mac was last in use before asOf, within
// the brief's longest window: the end of the newest activity summary.
func mobileBriefLastActive(a *App, asOf time.Time) (time.Time, bool) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return time.Time{}, false
	}
	rows, err := a.activity.store.GetActivitiesInRange(asOf.Add(-mobileBriefMaxWindow).UnixMilli(), asOf.UnixMilli())
	if err != nil {
		return time.Time{}, false
	}
	var last int64
	for _, row := range rows {
		end := row.EndMs
		if end <= 0 {
			end = row.StartMs
		}
		if end > last && end <= asOf.UnixMilli() {
			last = end
		}
	}
	if last <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(last), true
}

// collectMobileBriefRecap adds the window's activity summaries to the moved
// section, longest first. The morning can reach back to the last active day.
// It fails closed, as topics do: a summary with no readable apps, one that involved a
// sensitive, messaging or mail app, one from a private window, or one from
// a stretch when observing was paused (or the pause log cannot be read)
// stays out.
func collectMobileBriefRecap(a *App, in *mobileBriefInput) {
	if a == nil || a.activity == nil || a.activity.store == nil || a.IsClosedEyesModeEnabled() {
		return
	}
	pauses, err := a.observationPauses()
	if err != nil {
		log.Printf("[mobile-brief] activity recap skipped: the pause log cannot be read")
		return
	}
	if pausedAt(pauses, in.AsOf) {
		return
	}
	rows, err := a.activity.store.GetActivitiesInRange(in.recapStart().UnixMilli(), in.AsOf.UnixMilli())
	if err != nil {
		log.Printf("[mobile-brief] activity recap unavailable: %v", err)
		return
	}
	// Summary labels can outlive their capture. Recheck every backing run and
	// session against the current policy before selecting or storing a recap.
	rows = a.filterComposerModelCandidates(a.activity.store, rows)
	type recap struct {
		row   ActivityRow
		apps  []string
		dwell time.Duration
	}
	var kept []recap
	for _, row := range rows {
		// A summary that narrates typing would put captured input in a title.
		if strings.TrimSpace(row.Title) == "" || threadOutcomeTypingNarration.MatchString(row.Title) {
			continue
		}
		apps, dwell := topicAppTime(row.ActiveMsByApp)
		if len(apps) == 0 || topicAnySensitive(apps) || topicAnyPeopleApp(apps) {
			continue
		}
		if activitymon.IsPrivateBrowsing(row.Title) || pausedAt(pauses, mobileBriefMillis(row.StartMs)) || pausedAt(pauses, mobileBriefMillis(row.EndMs)) {
			continue
		}
		if dwell <= 0 && row.EndMs > row.StartMs {
			dwell = time.Duration(row.EndMs-row.StartMs) * time.Millisecond
		}
		kept = append(kept, recap{row: row, apps: apps, dwell: dwell})
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].dwell > kept[j].dwell })
	if len(kept) > mobileBriefRecapLimit {
		kept = kept[:mobileBriefRecapLimit]
	}
	for _, r := range kept {
		if outcome := a.readableThreadOutcome(context.Background(), r.row); outcome != nil {
			status := "done"
			if outcome.NextStep != "" || threadOutcomeUnfinished(outcome) {
				status = "needs_review"
			}
			in.Moved = append(in.Moved, mobileBriefCandidate{
				SourceKind: mobileBriefRecapSource, SourceID: threadOutcomeSourceID(r.row.ID, outcome),
				Title: outcome.Summary, Kind: mobileBriefThreadOutcomeSource, Status: status,
				At: mobileBriefMillis(r.row.EndMs), OpenedAt: mobileBriefMillis(r.row.StartMs), Apps: r.apps, NextStep: outcome.NextStep, Outcome: outcome,
			})
			continue
		}
		detail := ""
		if minutes := int(r.dwell / time.Minute); minutes > 0 {
			detail = fmt.Sprintf("%d min", minutes)
		}
		in.Moved = append(in.Moved, mobileBriefCandidate{
			SourceKind: mobileBriefRecapSource,
			SourceID:   r.row.ID,
			Title:      r.row.Title,
			Detail:     detail,
			Status:     "done",
			Kind:       "recap",
			At:         mobileBriefMillis(r.row.StartMs),
			Apps:       r.apps,
		})
	}
}

// collectMobileBriefNews adds the stories Today shows (todayNewsShownRun) to
// the watching section of the morning brief, when their run finished in the
// last day. A run that searched for the user's topics, a native-route run,
// or one that does not say, is used only while those topics may leave the
// Mac: observing is not paused and Watch my screen is on. A paused Daily
// News adds nothing.
func collectMobileBriefNews(a *App, in *mobileBriefInput) {
	if in.Slot != mobileBriefSlotMorning || a == nil {
		return
	}
	job := a.findTodayNewsJob()
	if job == nil || !job.Enabled {
		return
	}
	runs, err := a.GetCronRuns(job.ID)
	if err != nil {
		return
	}
	run, items, ok := todayNewsShownRun(runs)
	if !ok {
		return
	}
	finished := mobileBriefMillis(run.FinishedAt)
	if finished.IsZero() || finished.After(in.AsOf) || in.AsOf.Sub(finished) > mobileBriefNewsFresh {
		return
	}
	_, meta, hasMeta := splitResearchMeta(run.ResultSummary)
	if !hasMeta || (meta.Personal != researchPersonalNo && !a.topicLabelsMayLeave()) {
		return
	}
	for i, item := range items {
		if i >= mobileBriefNewsLimit {
			break
		}
		in.Watching = append(in.Watching, mobileBriefCandidate{
			SourceKind: mobileBriefNewsSource,
			SourceID:   fmt.Sprintf("%s:%d", run.ID, i),
			Title:      item.Title,
			Detail:     item.Source,
			Status:     "news",
			Kind:       "news",
			At:         finished,
			URL:        mobileBriefLink(item.URL),
		})
	}
}

// mobileBriefEndColdStart ends the cold start when the brief has something
// to say: a recap, a meeting, an open loop or news. Cold start is for a Mac
// Isoform knows nothing about yet.
func mobileBriefEndColdStart(in *mobileBriefInput) {
	if in.ColdStart && (len(in.Moved) > 0 || len(in.Meetings) > 0 || len(in.OpenLoops) > 0 || len(in.Watching) > 0 || len(in.WorkingToward) > 0 || len(in.Commitments) > 0 || len(in.WaitingOnYou) > 0) {
		in.ColdStart = false
	}
}
