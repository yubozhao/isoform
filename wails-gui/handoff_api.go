package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
	"yansu-agent/agent/native"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	// handoffStaleTTL is how long an active handoff survives with no action
	// before the lifecycle GC ages it to `stale`.
	handoffStaleTTL = 24 * time.Hour
	// handoffLifecycleInterval is the GC cadence.
	handoffLifecycleInterval = time.Minute
)

// handoffInProgressIdleTimeout is how long an in_progress Handoff may go
// without store activity before the lifecycle blocks it as timed out.
const handoffInProgressIdleTimeout = 2 * time.Hour

// HandoffAcceptResult carries the brief into chat. composeHandoffPrompt adds
// the current result contract once, including on restarts and continuations.
type HandoffAcceptResult struct {
	HandoffID string `json:"handoffId"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`
	Workdir   string `json:"workdir"`
	Model     string `json:"model"`
}

type AgencyHandoffContinuationInput struct {
	HandoffID string `json:"handoffId"`
	SessionID string `json:"sessionId"`
}

// HandoffEntryView is the product read model for a persisted Handoff. Agency
// execution Markdown stays server-side; its user-facing detail is reconstructed
// from the integrity-valid origination source instead.
type HandoffEntryView struct {
	ID            string         `json:"id"`
	Title         string         `json:"title"`
	Status        handoff.Status `json:"status"`
	Source        handoff.Source `json:"source"`
	SourceID      string         `json:"sourceId"`
	SourceLink    string         `json:"sourceLink"`
	AppName       string         `json:"appName"`
	Workdir       string         `json:"workdir"`
	KeyFiles      []string       `json:"keyFiles"`
	ChatSessionID string         `json:"chatSessionId"`
	CronJobID     string         `json:"cronJobId"`
	BlockedReason string         `json:"blockedReason,omitempty"`
	// StopCause says why a Needs you task stopped without finishing; StoppedBy
	// is "person" on a task the person stopped. Group is its board group.
	StopCause     string `json:"stopCause,omitempty"`
	StoppedBy     string `json:"stoppedBy,omitempty"`
	StopAssistant string `json:"stopAssistant,omitempty"`
	StoppedAt     int64  `json:"stoppedAt,omitempty"`
	Group         string `json:"group,omitempty"`
	// Criteria are the brief's success criteria; Verification is the check
	// result once a run has ended (nil before that).
	Criteria     []string              `json:"criteria"`
	Verification *handoff.Verification `json:"verification,omitempty"`
	CreatedAt    int64                 `json:"createdAt"`
	UpdatedAt    int64                 `json:"updatedAt"`
	Body         string                `json:"body"`
	Plan         *HandoffPlanView      `json:"plan,omitempty"`
	Progress     *TaskProgressView     `json:"progress,omitempty"`
	Runner       string                `json:"runner,omitempty"`
	// StepsToCheck and RepliesToChoose make a stopped row say Review.
	StepsToCheck    int `json:"stepsToCheck,omitempty"`
	RepliesToChoose int `json:"repliesToChoose,omitempty"`
}

func handoffEntryView(entry handoff.Entry) HandoffEntryView {
	group, cause := handoff.Placement(entry)
	view := HandoffEntryView{
		Group:         group,
		StopCause:     cause,
		ID:            entry.ID,
		Title:         entry.Title,
		Status:        entry.Status,
		Source:        entry.Source,
		SourceID:      entry.SourceID,
		SourceLink:    entry.SourceLink,
		AppName:       entry.AppName,
		Workdir:       entry.Workdir,
		KeyFiles:      entry.KeyFiles,
		ChatSessionID: entry.ChatSessionID,
		CronJobID:     entry.CronJobID,
		BlockedReason: entry.BlockedReason,
		StoppedBy:     entry.StoppedBy,
		StopAssistant: entry.StopAssistant,
		StoppedAt:     entry.StoppedAt,
		Criteria:      entry.Criteria,
		Verification:  entry.Verification,
		CreatedAt:     entry.CreatedAt,
		UpdatedAt:     entry.UpdatedAt,
	}
	view.StepsToCheck, view.RepliesToChoose = taskStoppedAwaitingAnswers(entry)
	if entry.Source != handoff.SourceAgency {
		view.Body = entry.Body
	}
	return view
}

func (a *App) productHandoffEntryView(entry handoff.Entry) HandoffEntryView {
	view := handoffEntryView(entry)
	a.populateTaskProgress(entry, &view)
	if entry.Source != handoff.SourceAgency || a == nil || a.activity == nil ||
		a.activity.store == nil {
		return view
	}
	view.Plan = agencyHandoffPlan(a.activity.store.db, entry)
	return view
}

// runHandoffLifecycle ages active handoffs to `stale` (TTL or superseded) on a
// ticker. Runs once promptly at startup, then every handoffLifecycleInterval.
func (a *App) runHandoffLifecycle(ctx context.Context) {
	ticker := time.NewTicker(handoffLifecycleInterval)
	defer ticker.Stop()
	for {
		if a.handoffStore != nil {
			if expired, err := a.handoffStore.MarkStale(handoffStaleTTL); err != nil {
				log.Printf("[handoff] stale GC failed: %v", err)
			} else if len(expired) > 0 {
				log.Printf("[handoff] aged %d handoff(s) to stale", len(expired))
				a.emitBrowserEvent("handoff:updated", map[string]any{"reason": "gc"})
				if a.telemetry != nil {
					for _, e := range expired {
						a.telemetry.Capture("handoff.expired", map[string]any{
							"handoff_id": e.ID,
							"source":     string(e.Source),
						})
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Idle checks run on ticks only: startup recovery covers the
			// first pass, after the Engagement service is up.
			a.blockIdleHandoffs(time.Now())
		}
	}
}

// ListHandoffs returns every product-safe Handoff, newest first. Wails binding.
func (a *App) ListHandoffs() (perfValue []HandoffEntryView, perfErr error) {
	defer logPageBinding("ListHandoffs", time.Now(), &perfValue)
	if a.handoffStore == nil {
		return nil, nil
	}
	entries, err := a.handoffStore.List()
	if err != nil {
		return nil, err
	}
	views := make([]HandoffEntryView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, a.productHandoffEntryView(entry))
	}
	return views, nil
}

// ResolveAgencyHandoffExecution hydrates an exact passive continuation
// reference after the user chooses Continue. It is deliberately read-only and
// revalidates current Handoff state instead of trusting an old permission
// receipt or product snapshot.
func (a *App) ResolveAgencyHandoffExecution(
	input AgencyHandoffContinuationInput,
) (HandoffAcceptResult, error) {
	if a == nil || a.handoffStore == nil {
		return HandoffAcceptResult{}, fmt.Errorf("handoff store not ready")
	}
	input.HandoffID = strings.TrimSpace(input.HandoffID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	if input.HandoffID == "" || input.SessionID == "" {
		return HandoffAcceptResult{}, fmt.Errorf(
			"handoff and saved conversation are required",
		)
	}
	entry, err := a.handoffStore.Get(input.HandoffID)
	if err != nil {
		return HandoffAcceptResult{}, fmt.Errorf(
			"This one-time work is no longer available to continue: %w",
			err,
		)
	}
	if entry.Status != handoff.StatusInProgress &&
		entry.Status != handoff.StatusNeedsReview &&
		entry.Status != handoff.StatusCompleted {
		return HandoffAcceptResult{}, errors.New(
			"This one-time work is no longer available to continue",
		)
	}
	if strings.TrimSpace(entry.ChatSessionID) == "" ||
		entry.ChatSessionID != input.SessionID {
		return HandoffAcceptResult{}, errors.New(
			"The saved conversation no longer matches this one-time work",
		)
	}
	if strings.TrimSpace(entry.Body) == "" {
		return HandoffAcceptResult{}, errors.New(
			"This one-time work has no retained conversation context",
		)
	}
	workdir := ""
	if strings.TrimSpace(entry.Workdir) != "" {
		if stat, statErr := os.Stat(entry.Workdir); statErr == nil && stat.IsDir() {
			workdir = entry.Workdir
		}
	}
	return HandoffAcceptResult{
		HandoffID: entry.ID,
		SessionID: entry.ChatSessionID,
		Title:     entry.Title,
		Prompt:    entry.Body,
		Workdir:   workdir,
		Model:     a.chatTaskModel(),
	}, nil
}

// GetActiveHandoffChips returns the newest handoffs to surface as composer chips
// (waiting, blocked or needs review). Wails binding.
func (a *App) GetActiveHandoffChips(limit int) (perfValue []HandoffEntryView, perfErr error) {
	defer logPageBinding("GetActiveHandoffChips", time.Now(), &perfValue)
	if a.handoffStore == nil {
		return nil, nil
	}
	entries, err := a.handoffStore.ActiveForChips(limit)
	if err != nil {
		return nil, err
	}
	views := make([]HandoffEntryView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, a.productHandoffEntryView(entry))
	}
	return views, nil
}

// handoffAcceptableStatuses are the states a Handoff can be started from:
// waiting, aged, and Start again on a run that was blocked, needs review or
// was stopped.
var handoffAcceptableStatuses = []handoff.Status{
	handoff.StatusActive,
	handoff.StatusStale,
	handoff.StatusBlocked,
	handoff.StatusNeedsReview,
	handoff.StatusAborted,
}

// checkAgencyHandoffStartable refuses Agency-written work that may no longer
// run. A stale Agency entry was retired, and an entry whose stored plan is
// missing or no longer matches it has nothing the user could have approved
// (see handoff_legacy_plan.go). Other sources start as they always have.
func (a *App) checkAgencyHandoffStartable(entry handoff.Entry) error {
	if entry.Source != handoff.SourceAgency {
		return nil
	}
	if entry.Status == handoff.StatusStale {
		return errors.New("This one-time work was retired and can no longer start")
	}
	if a.productHandoffEntryView(entry).Plan == nil {
		return errors.New("This one-time work has no verified plan and cannot start")
	}
	return nil
}

// AcceptHandoff claims a handoff and creates the chat session that will execute
// it. The frontend then sends the returned prompt through the normal chat send
// path so handoff execution has the same streaming, cancel, and follow-up
// behavior as any other chat turn. Handoffs the Agency wrote (Source agency)
// start here like any other now that the Desk is gone, as long as they are not
// retired and their plan still verifies. Wails binding.
func (a *App) AcceptHandoff(id string) (HandoffAcceptResult, error) {
	return a.acceptHandoff(id, nil)
}

func (a *App) acceptHandoff(id string, send func(HandoffAcceptResult) error) (HandoffAcceptResult, error) {
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	if a.handoffStore == nil {
		return HandoffAcceptResult{}, fmt.Errorf("handoff store not ready")
	}
	original, err := a.handoffStore.Get(id)
	if err != nil {
		return HandoffAcceptResult{}, fmt.Errorf("load handoff %s: %w", id, err)
	}
	if err := a.validateListHandoffStart(&original); err != nil {
		return HandoffAcceptResult{}, err
	}
	if !slices.Contains(handoffAcceptableStatuses, original.Status) {
		return HandoffAcceptResult{}, fmt.Errorf("handoff %s is not actionable (status=%q)", id, original.Status)
	}
	if err := a.checkAgencyHandoffStartable(original); err != nil {
		return HandoffAcceptResult{}, err
	}
	if err := a.handoffRestartGate(original); err != nil {
		return HandoffAcceptResult{}, err
	}

	// Start again on a run that was blocked or needs review continues in its own
	// chat, so the earlier conversation stays one thread. When that chat is gone,
	// the run gets a new one like any other accept.
	reusedSessionID := ""
	switch original.Status {
	case handoff.StatusBlocked, handoff.StatusNeedsReview, handoff.StatusAborted:
		if a.chatSessionExists(original.ChatSessionID) {
			reusedSessionID = original.ChatSessionID
		}
	}

	// The claim holds only while the status the checks above saw still holds, so
	// work retired in between is not started.
	attemptStartedAt := time.Now().UnixMilli()
	noAIAtStart := !handoffStartAIReady(a)
	e, err := a.handoffStore.MutateIfStatus(id, []handoff.Status{original.Status}, func(entry *handoff.Entry) {
		entry.Status = handoff.StatusInProgress
		entry.BlockedReason = ""
		entry.Verification = nil
		entry.Result = nil
		entry.AttemptID = uuid.NewString()
		entry.AttemptStartedAt = attemptStartedAt
		entry.LastActivityAt = attemptStartedAt
		entry.ClearStop()
		entry.NoAIAtStart = noAIAtStart
		switch original.Status {
		case handoff.StatusBlocked, handoff.StatusNeedsReview, handoff.StatusAborted:
			params := prepareHandoffRestart(entry, send != nil)
			ensureThread(entry).AppendEvent(handoff.Event{ID: uuid.NewString(), Kind: handoff.EventLine, At: attemptStartedAt, Code: handoff.EventCodeStartedAgain, Params: params})
		}
	})
	if err != nil {
		return HandoffAcceptResult{}, fmt.Errorf("claim handoff %s: %w", id, err)
	}
	a.emitBrowserEvent("handoff:updated", map[string]any{"id": e.ID, "status": string(e.Status)})

	rollback := func(reason string) {
		restored, rerr := a.handoffStore.MutateIfStatus(id, []handoff.Status{handoff.StatusInProgress}, func(entry *handoff.Entry) {
			entry.Status = original.Status
			entry.BlockedReason = original.BlockedReason
			entry.ChatSessionID = original.ChatSessionID
			entry.CronJobID = original.CronJobID
			entry.AttemptID = original.AttemptID
			entry.AttemptStartedAt = original.AttemptStartedAt
			entry.LastActivityAt = original.LastActivityAt
			entry.Result = original.Result
			entry.Verification = original.Verification
			entry.StoppedBy, entry.StopCause = original.StoppedBy, original.StopCause
			entry.StopAssistant, entry.StoppedAt = original.StopAssistant, original.StoppedAt
			entry.NoAIAtStart = original.NoAIAtStart
			entry.Thread = original.Thread
		})
		if rerr != nil {
			log.Printf("[handoff] accept: rollback after %s failed: %v", reason, rerr)
			return
		}
		a.emitBrowserEvent("handoff:updated", map[string]any{"id": restored.ID, "status": string(restored.Status)})
	}

	sessionID := reusedSessionID
	if sessionID == "" {
		sessionID, err = a.createHandoffExecutionSession(e)
		if err != nil {
			rollback("session creation")
			return HandoffAcceptResult{}, fmt.Errorf("create handoff session: %w", err)
		}
	}
	// discardSession removes a session this accept created. A reused chat is the
	// user's conversation and is never removed here.
	discardSession := func() {
		if reusedSessionID == "" {
			a.cleanupHandoffExecutionSession(sessionID)
		}
	}

	// Pin WorkDir to the handoff's resolved workdir, but only if it still
	// exists on disk — a stale or unresolved path falls back to the cron
	// default work dir rather than failing the run.
	workdir := ""
	if e.Workdir != "" {
		if st, serr := os.Stat(e.Workdir); serr == nil && st.IsDir() {
			workdir = e.Workdir
		} else {
			log.Printf("[handoff] accept: workdir %q not usable, using cron default", e.Workdir)
		}
	}

	current, err := a.handoffStore.Get(id)
	if err != nil || current.Status != handoff.StatusInProgress {
		discardSession()
		if err != nil {
			return HandoffAcceptResult{}, fmt.Errorf("handoff %s disappeared while accepting: %w", id, err)
		}
		return HandoffAcceptResult{}, fmt.Errorf("handoff %s is no longer accepting (status=%q)", id, current.Status)
	}

	saved, serr := a.handoffStore.MutateIfStatus(id, []handoff.Status{handoff.StatusInProgress}, func(entry *handoff.Entry) {
		entry.ChatSessionID = sessionID
		entry.CronJobID = ""
	})
	if serr != nil {
		discardSession()
		return HandoffAcceptResult{}, fmt.Errorf("persist in_progress state: %w", serr)
	}

	a.emitBrowserEvent("handoff:updated", map[string]any{"id": saved.ID, "status": string(saved.Status)})

	result := HandoffAcceptResult{
		HandoffID: e.ID, SessionID: sessionID, Title: e.Title,
		Prompt: e.Body, Workdir: workdir, Model: a.chatTaskModel(),
	}
	if send != nil && e.Thread != nil {
		result.Prompt = handoffRestartPrompt(e.Body, handoffTakenReplies(e), e.Thread.Skipped)
	}
	if send != nil {
		if err := send(result); err != nil {
			rollback("chat send")
			discardSession()
			return HandoffAcceptResult{}, fmt.Errorf("send handoff prompt: %w", err)
		}
	}
	if a.telemetry != nil {
		a.telemetry.Capture("handoff.accepted", map[string]any{
			"handoff_id":  e.ID,
			"has_workdir": workdir != "",
		})
	}

	return result, nil
}

func (a *App) cleanupHandoffExecutionSession(sessionID string) {
	if sessionID == "" || a.store == nil {
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if rerr := a.store.RemoveGroupItemsByRef(ctx, "chat", sessionID); rerr != nil {
		log.Printf("[handoff] cleanup: sidebar group removal failed: %v", rerr)
	}
	if rerr := a.store.DeleteSession(ctx, sessionID); rerr != nil {
		log.Printf("[handoff] cleanup: session deletion failed: %v", rerr)
	}
	a.mainChatRowsChanged(sessionID)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "sidebar:groups-changed", nil)
		runtime.EventsEmit(a.ctx, "session:deleted", sessionID)
	}
}

// AbortHandoff cancels a handoff: removes its cron job if one was created and
// marks it aborted. Handoffs the Agency wrote are stopped here like any other.
// Wails binding.
func (a *App) AbortHandoff(id string) error {
	return a.abortHandoff(id, "")
}

// A main-chat Stop belongs to one persisted transition. Check its identity
// under the same lock as AcceptHandoff so starting another attempt cannot
// slip between the check and cancellation.
func (a *App) abortHandoff(id, expectedMomentID string) error {
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	if a.handoffStore == nil {
		return fmt.Errorf("handoff store not ready")
	}
	e, err := a.handoffStore.Get(id)
	if err != nil {
		return fmt.Errorf("load handoff %s: %w", id, err)
	}
	if e.Status == handoff.StatusAborted {
		return nil
	}
	if expectedMomentID != "" {
		if e.Status == handoff.StatusCompleted {
			return errors.New("work is already done; use its latest update")
		}
		if mainChatHandoffMomentID(e) != expectedMomentID {
			return errors.New("work has changed since this message; use its latest update")
		}
	}
	if e.CronJobID != "" && a.cronScheduler != nil {
		if rerr := a.cronScheduler.RemoveJob(a.ctx, e.CronJobID); rerr != nil {
			log.Printf("[handoff] abort: cron removal failed (continuing): %v", rerr)
		}
	}
	progress := HandoffEntryView{}
	a.handoffActionMu.Unlock()
	a.populateTaskProgress(e, &progress)
	steps := a.handoffEffectSteps(e)
	a.handoffActionMu.Lock()
	_, err = a.handoffStore.MutateIfAttempt(id, e.AttemptID, []handoff.Status{handoff.StatusActive, handoff.StatusStale, handoff.StatusInProgress, handoff.StatusBlocked, handoff.StatusNeedsReview}, func(entry *handoff.Entry) {
		entry.Status = handoff.StatusAborted
		entry.BlockedReason = ""
		recordPersonStop(entry, progress.Progress, time.Now().UnixMilli())
		if e.Status == handoff.StatusInProgress {
			ensureThread(entry).Steps = steps
		}
	})
	if err != nil {
		return fmt.Errorf("mark aborted: %w", err)
	}
	a.cancelHandoffRunLocked(e)
	// Stopping a task yourself never notifies: no moment, no main-chat report.
	a.emitBrowserEvent("handoff:updated", map[string]any{"id": id, "status": string(handoff.StatusAborted)})

	if a.telemetry != nil {
		a.telemetry.Capture("handoff.aborted", map[string]any{
			"handoff_id":   id,
			"had_cron_job": e.CronJobID != "",
		})
	}

	return nil
}

// DeleteHandoff removes a handoff entirely: cancels its cron job, deletes its
// streaming chat session, and removes the handoff file. Handoffs the Agency
// wrote are deleted here like any other. Wails binding.
func (a *App) DeleteHandoff(id string) error {
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	if a.handoffStore == nil {
		return fmt.Errorf("handoff store not ready")
	}
	if e, gerr := a.handoffStore.Get(id); gerr == nil { // best-effort cleanup
		if e.CronJobID != "" && a.cronScheduler != nil {
			if rerr := a.cronScheduler.RemoveJob(a.ctx, e.CronJobID); rerr != nil {
				log.Printf("[handoff] delete: cron removal failed (continuing): %v", rerr)
			}
		}
		a.cancelHandoffRunLocked(e)
		a.cleanupHandoffExecutionSession(e.ChatSessionID)
	}
	if err := a.handoffStore.Delete(id); err != nil {
		return fmt.Errorf("delete handoff %s: %w", id, err)
	}
	a.emitBrowserEvent("handoff:updated", map[string]any{"id": id, "reason": "deleted"})
	return nil
}

// markHandoffDone settles an in_progress Handoff when its run ends. A run that
// errored is aborted. A run that ended cleanly is verified against its success
// criteria and lands in completed, or in needs_review when the evidence does not
// hold. transcript is the run's final assistant text. Both the chat stream and
// the cron executor call this from a goroutine, since the verifier is a model call.
func (a *App) markHandoffDone(id string, ok bool, transcript string) {
	if a == nil || a.handoffStore == nil {
		return
	}
	entry, err := a.handoffStore.Get(id)
	if err != nil {
		return
	}
	a.markHandoffAttemptDone(entry, ok, transcript)
}

func (a *App) markHandoffAttemptDone(current handoff.Entry, ok bool, transcript string) {
	a.markHandoffAttemptEnded(current, ok, transcript, nil)
}

// markHandoffAttemptEnded is markHandoffAttemptDone with the run's error, so a
// structured auth failure can name the stop.
func (a *App) markHandoffAttemptEnded(current handoff.Entry, ok bool, transcript string, runErr error) {
	if !ok && a.handoffShuttingDown.Load() {
		// Quitting ends the run; startup recovery marks it stopped by the restart.
		return
	}
	pickupTried := false
	if ok && !a.handoffShuttingDown.Load() {
		started, tried := a.pickUpTaskReplies(current)
		if started {
			// Replies were waiting: the run goes on with them (tasks-review v1.0.5 §2).
			return
		}
		pickupTried = tried
	}
	a.handoffActionMu.Lock()
	entry, err := a.handoffStore.Get(current.ID)
	if err != nil || entry.Status != handoff.StatusInProgress || entry.AttemptID != current.AttemptID {
		a.handoffActionMu.Unlock()
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	verifyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if a.handoffRuns == nil {
		a.handoffRuns = map[string]*handoffRunState{}
	}
	a.handoffRuns[current.ID] = &handoffRunState{sessionID: current.ChatSessionID, attemptID: current.AttemptID, cancel: cancel, lastActivity: time.Now()}
	a.handoffActionMu.Unlock()
	outcome := handoffOutcome{Status: handoff.StatusAborted}
	var proof handoffStopProof
	if ok {
		outcome = verifyHandoffOutcome(entry, transcript, func(prompt string) (string, error) { return a.askHandoffVerifierContext(verifyCtx, prompt) })
	} else {
		proof = handoffStopProbe(a)
		proof.NoAIAtStart = entry.NoAIAtStart
		proof.LoopGuard = errors.Is(runErr, native.ErrLoopGuardStuck)
		proof.AuthFailed = handoffRunAuthFailed(runErr)
	}
	var steps []handoff.EffectStep
	if !ok {
		steps = a.handoffEffectSteps(entry)
	}
	requeue := false
	if handoffSettleHook != nil {
		handoffSettleHook(current.ID)
	}
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	saved, err := a.handoffStore.MutateIfAttempt(current.ID, current.AttemptID, []handoff.Status{handoff.StatusInProgress}, func(e *handoff.Entry) {
		if ok && !pickupTried && e.Thread != nil && len(e.Thread.WaitingReplies()) > 0 {
			// A reply came while the run was being checked: nothing waits on a
			// finished task, so the run takes it instead of settling.
			requeue = true
			return
		}
		e.Status = outcome.Status
		e.Result = outcome.Result
		if ok {
			verification := outcome.Verification
			e.Verification = &verification
			recordHandoffStepsDone(e, time.Now().UnixMilli())
			if !pii.IsSensitiveAppName(e.AppName) {
				recordHandoffThreadResult(e, transcript, time.Now().UnixMilli())
			}
		} else {
			recordSystemStop(e, handoffStopCause(proof), proof.Assistant, time.Now().UnixMilli())
			ensureThread(e).Steps = steps
		}
	})
	if run := a.handoffRuns[current.ID]; run != nil && run.attemptID == current.AttemptID {
		delete(a.handoffRuns, current.ID)
	}
	if err != nil {
		return
	}
	if requeue {
		go a.markHandoffAttemptEnded(saved, true, transcript, nil)
		return
	}
	a.emitBrowserEvent("handoff:updated", map[string]any{"id": current.ID, "status": string(saved.Status)})
	if ok {
		a.produceWorkFinished(saved)
	}
	a.postHandoffResult(saved, transcript)
	if ok {
		go a.afterRunBgShells(saved.ChatSessionID)
	}
}

// chatSessionExists reports whether a chat session is still saved. A reused
// Handoff chat must exist; a deleted one is replaced by a new session.
func (a *App) chatSessionExists(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || a == nil || a.store == nil {
		return false
	}
	exists, err := a.store.SessionExists(context.Background(), sessionID)
	return err == nil && exists
}

// markHandoffBlocked moves an in_progress Handoff to blocked with a reason code
// and submits the work_blocked moment. Entries in any other status are left alone.
func (a *App) markHandoffBlocked(id, reason string) {
	if id == "" || a.handoffStore == nil {
		return
	}
	var steps []handoff.EffectStep
	if current, err := a.handoffStore.Get(id); err == nil && current.Status == handoff.StatusInProgress {
		steps = a.handoffEffectSteps(current)
	}
	saved, err := a.handoffStore.MutateIfStatus(id, []handoff.Status{handoff.StatusInProgress}, func(entry *handoff.Entry) {
		ensureThread(entry).Steps = steps
		entry.Status = handoff.StatusBlocked
		entry.BlockedReason = reason
		cause := handoff.CauseRestarted
		if reason == handoff.BlockedTimeout {
			cause = handoff.CauseTimeout
		}
		recordSystemStop(entry, cause, "", time.Now().UnixMilli())
	})
	if err != nil {
		log.Printf("[handoff] blocked status update skipped: %v", err)
		return
	}
	a.emitBrowserEvent("handoff:updated", map[string]any{"id": id, "status": string(handoff.StatusBlocked), "blockedReason": reason})
	a.produceWorkBlocked(saved)
	a.postHandoffReport(saved)
}

// handoffTurnLive reports whether a chat turn for the session is running in this
// process. Nothing is live right after a restart.
func (a *App) handoffTurnLive(sessionTag string) bool {
	if sessionTag == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activeTags[sessionTag]
}

// recoverInterruptedHandoffs runs once at startup. A restart ends every turn, so
// each in_progress Handoff without a live turn was interrupted.
func (a *App) recoverInterruptedHandoffs() {
	if a == nil || a.handoffStore == nil {
		return
	}
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	entries, err := a.handoffStore.List()
	if err != nil {
		log.Printf("[handoff] startup recovery skipped: %v", err)
		return
	}
	for _, entry := range entries {
		if entry.Status != handoff.StatusInProgress || a.handoffTurnLive(entry.ChatSessionID) || a.handoffRuns[entry.ID] != nil {
			continue
		}
		a.markHandoffBlocked(entry.ID, handoff.BlockedInterrupted)
	}
}

// blockIdleHandoffs blocks and cancels work with no stream activity for two
// hours, including a live context whose model has stalled.
func (a *App) blockIdleHandoffs(now time.Time) {
	if a == nil || a.handoffStore == nil {
		return
	}
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	entries, err := a.handoffStore.List()
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Status != handoff.StatusInProgress {
			continue
		}
		last := entry.LastActivityAt
		if last == 0 {
			last = entry.UpdatedAt
		}
		if run := a.handoffRuns[entry.ID]; run != nil && run.attemptID == entry.AttemptID {
			last = run.lastActivity.UnixMilli()
		}
		if now.Sub(time.UnixMilli(last)) < handoffInProgressIdleTimeout {
			continue
		}
		a.markHandoffBlocked(entry.ID, handoff.BlockedTimeout)
		a.cancelHandoffRunLocked(entry)
	}
}

// cancelHandoffTurn stops the chat turn running for a Handoff session.
func (a *App) cancelHandoffTurn(sessionTag string) {
	a.cancelActiveChatTurn(sessionTag)
}
