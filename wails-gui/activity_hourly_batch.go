package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/platform/activitymon"
)

// Capture stays real-time; the model work derived from it runs once an hour
// (Bo, 2026-11-12). One batch covers every activity session captured since
// the previous batch: one triage call, at most one memory and one knowledge
// call, then the activity-driven proactive voice checks. A batch with nothing
// new makes no model call. Settings › Activity pipeline "Run now" starts a
// batch at once.
const (
	activityBatchInterval = cadenceActivityBatch
	// activityBatchStartupDelay keeps an overdue batch off the first paint.
	activityBatchStartupDelay = 2 * time.Minute
	activityBatchLastRunKey   = "activity_batch.last_run"
	// activityAgentReason labels sessions that only show coding agents at work.
	activityAgentReason = "agent activity: coding agent sessions are not about the user"
)

// activityBatchClock is the app-wide schedule of the hourly batch. The
// engagement service reads it to hold activity-driven chat cards until the
// batch, and the proactive voice gate reads it to decide whether a check may
// call the model now.
type activityBatchClock struct {
	mu   sync.Mutex
	due  time.Time
	open int
}

func (c *activityBatchClock) setDue(due time.Time) {
	c.mu.Lock()
	c.due = due
	c.mu.Unlock()
}

func (c *activityBatchClock) dueAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.due
}

// openWindow marks a running batch voice pass; the returned func closes it.
func (c *activityBatchClock) openWindow() func() {
	c.mu.Lock()
	c.open++
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.open--
		c.mu.Unlock()
	}
}

// allows reports whether activity-driven voice may run at now: during a batch
// voice pass, once the batch is due, or when no batch is scheduled at all
// (activity capture off, tests), which keeps the old immediate behavior.
func (c *activityBatchClock) allows(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open > 0 || c.due.IsZero() || !now.Before(c.due)
}

// engagementActivityVoiceKind names the proactive voice kinds whose source is
// activity capture: a capture outage seen by the capture-health sampler (the
// microphone stops after each audio batch) and a commitment read from the
// Purpose Map that activity feeds. Mail, calendar and brief cards are not
// activity-driven and keep their immediate voice.
func engagementActivityVoiceKind(kind engagement.Kind) bool {
	switch kind {
	case engagement.KindCapturePaused, engagement.KindCommitmentSlipping:
		return true
	default:
		return false
	}
}

func (a *App) activityBatchAllowsVoice(kind engagement.Kind, now time.Time) bool {
	if a == nil || !engagementActivityVoiceKind(kind) {
		return true
	}
	return a.activityBatch.allows(now)
}

// engagementChatReportRetryAt holds a deferred activity-driven chat card until
// the next batch instead of retrying it every five minutes.
func (a *App) engagementChatReportRetryAt(kind engagement.Kind, now time.Time) time.Time {
	if a != nil && engagementActivityVoiceKind(kind) {
		if due := a.activityBatch.dueAt(); due.After(now) {
			return due
		}
	}
	return now.Add(engagementRetryDelay)
}

// runActivityVoiceBatch delivers the activity-driven chat cards held for this
// batch, with the voice window open.
func (a *App) runActivityVoiceBatch(ctx context.Context) {
	if a == nil || a.engagement == nil || ctx.Err() != nil {
		return
	}
	defer a.activityBatch.openWindow()()
	a.engagement.Pass(ctx)
}

// nextBatchAt returns when the next hourly batch runs, from the last batch
// time this computer recorded.
func (c *ActivityTriager) nextBatchAt(now time.Time) time.Time {
	last := c.lastBatchRun()
	if last.IsZero() {
		// The first batch after an install or upgrade covers its first hour.
		return now.Add(activityBatchInterval)
	}
	due := last.Add(activityBatchInterval)
	if earliest := now.Add(activityBatchStartupDelay); due.Before(earliest) && c.firstSchedule() {
		return earliest
	}
	if due.Before(now) {
		return now
	}
	return due
}

func (c *ActivityTriager) firstSchedule() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	first := !c.batchScheduled
	c.batchScheduled = true
	return first
}

func (c *ActivityTriager) lastBatchRun() time.Time {
	c.mu.Lock()
	last := c.lastBatch
	c.mu.Unlock()
	if !last.IsZero() || c.app == nil || c.app.store == nil {
		return last
	}
	raw, err := c.app.store.GetUIState(context.Background(), activityBatchLastRunKey)
	if err != nil || raw == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func (c *ActivityTriager) recordBatchRun(at time.Time) {
	c.mu.Lock()
	c.lastBatch = at
	c.mu.Unlock()
	if c.app == nil || c.app.store == nil {
		return
	}
	if err := c.app.store.SetUIState(context.Background(), activityBatchLastRunKey, strconv.FormatInt(at.UnixMilli(), 10)); err != nil {
		log.Printf("[triager] recording batch time failed: %s", activityPrivateLogField("error", err.Error()))
	}
}

// runHourlyBatches runs one batch per hour until ctx ends.
func (c *ActivityTriager) runHourlyBatches(ctx context.Context) {
	for {
		due := c.nextBatchAt(time.Now())
		if c.app != nil {
			c.app.activityBatch.setDue(due)
		}
		timer := time.NewTimer(time.Until(due))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		_ = c.runBatch(ctx, "hourly")
	}
}

// runBatch is one batch: triage and learning over everything captured since
// the last batch, then the activity-driven voice checks held for it.
func (c *ActivityTriager) runBatch(ctx context.Context, trigger string) error {
	started := time.Now()
	err := c.evaluate(ctx)
	if errors.Is(err, errGeneratorBusy) {
		return err
	}
	c.runDerivedActivityLayers(ctx, trigger == "manual")
	if c.app != nil {
		c.app.runActivityVoiceBatch(ctx)
	}
	c.recordBatchRun(started)
	if c.app != nil && trigger == "hourly" {
		c.app.activityBatch.setDue(started.Add(activityBatchInterval))
	}
	log.Printf("[triager] batch done: trigger=%s elapsed=%s", trigger, time.Since(started).Round(time.Second))
	return err
}

// splitAgentActivity removes sessions that only show coding agents at work.
// They are recorded as agent activity, never sent to a model and never turned
// into memories about the user.
func (c *ActivityTriager) splitAgentActivity(sessions []ActivitySessionRow) (user, agentSessions []ActivitySessionRow) {
	for _, session := range sessions {
		var events []activitymon.ActivityEvent
		if c.app != nil && c.app.activity != nil && c.app.activity.store != nil {
			events, _ = c.app.activity.store.GetSessionEvents(session.ID)
		}
		if activitySessionIsAgent(session, events) {
			agentSessions = append(agentSessions, session)
			continue
		}
		user = append(user, session)
	}
	return user, agentSessions
}

func (c *ActivityTriager) recordAgentActivity(sessions []ActivitySessionRow) {
	if len(sessions) == 0 {
		return
	}
	group := sessionGroup{Sessions: sessions}
	run := newGroupPipelineRun(group, time.Now())
	run.PrefilterPass = boolPtr(false)
	run.PrefilterReason = activityAgentReason
	run.TriageStatus = "dismissed"
	run.TriageReason = activityAgentReason
	for _, s := range sessions {
		if err := c.app.activity.store.UpdateTriageStatus(s.ID, "dismissed"); err != nil {
			log.Printf("[triager] failed to label session %s as agent activity: %s", s.ID, activityPrivateLogField("error", err.Error()))
		}
	}
	logTriage("agent", group.primaryID(), "dismiss", activityAgentReason)
	run.CompletedAt = time.Now().UnixMilli()
	c.recordPipelineRun(&run)
}

// activitySessionIsAgent reports a session in which every app shown is a
// coding agent: the Claude or Codex app, or a terminal whose window names
// Claude Code or Codex. Speech is the person talking, so any speech keeps the
// session the person's own.
func activitySessionIsAgent(session ActivitySessionRow, events []activitymon.ActivityEvent) bool {
	seen := false
	for _, evt := range events {
		if evt.Kind == activitymon.EventSpeech {
			return false
		}
		if evt.AppBundleID == "" && evt.AppName == "" {
			continue
		}
		seen = true
		if !activityEventIsAgent(evt) {
			return false
		}
	}
	if seen {
		return true
	}
	if session.IsMeeting {
		return false
	}
	var names []string
	if json.Unmarshal([]byte(session.AppNames), &names) != nil || len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !activityAgentApp("", name) {
			return false
		}
	}
	return true
}

func activityAgentApp(bundleID, name string) bool {
	switch strings.ToLower(strings.TrimSpace(bundleID)) {
	case "com.anthropic.claudefordesktop", "com.anthropic.claude", "com.openai.codex":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "codex":
		return true
	}
	return false
}

func activityAgentWindowTitle(title string) bool {
	lower := strings.ToLower(title)
	return strings.Contains(lower, "claude code") || strings.Contains(lower, "codex")
}

// batchCandidateGroups joins the groups that passed the pre-filter into one
// batch group, in capture order.
func batchCandidateGroups(groups []sessionGroup) []sessionGroup {
	if len(groups) <= 1 {
		return groups
	}
	var batch sessionGroup
	for _, g := range groups {
		batch.Sessions = append(batch.Sessions, g.Sessions...)
	}
	sort.SliceStable(batch.Sessions, func(i, j int) bool {
		return batch.Sessions[i].StartTime < batch.Sessions[j].StartTime
	})
	return []sessionGroup{batch}
}

// runDerivedActivityLayers runs the activity layers built from capture once,
// in dependency order: semantic events, run summaries, the composer that
// groups summarized runs, today's threads, and (daily) their outcomes. Each
// used to run on its own 2 to 15 minute ticker.
// A manual run ("Run now") also runs the daily layers at once.
func (c *ActivityTriager) runDerivedActivityLayers(ctx context.Context, force bool) {
	if c == nil || c.app == nil || c.app.activity == nil || c.app.activity.store == nil {
		return
	}
	steps := []func(){
		func() { c.runSemanticPassGuarded(ctx) },
		func() { c.runTimelineSummaryPassGuarded(ctx) },
		func() { c.runActivityComposerPassGuarded(ctx) },
		func() { c.app.runTodayThreadPass(ctx, time.Now()) },
		func() {
			// Outcomes are read once a day; the evening brief also fills
			// them before it gathers.
			if c.app.claimDerivedRun("thread_outcomes", cadenceThreadOutcomes, time.Now()) || force {
				c.app.fillThreadOutcomes(ctx, time.Now(), time.Local)
			}
		},
	}
	for _, step := range steps {
		if ctx.Err() != nil {
			return
		}
		step()
	}
}
