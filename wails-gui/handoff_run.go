package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"wails-gui/internal/handoff"
	"yansu-agent/agent"
)

// An attempt owns its cancellation and last observed stream activity. A live
// context alone is not activity: a stalled model must still time out.
var errHandoffRunBusy = errors.New("one-time work has a run or verification in progress")

// listHandoffBackend pins a checked list task to the assistant named when it
// was added. Ordinary handoffs retain their existing routing behavior.
func listHandoffBackend(entry *handoff.Entry) string {
	if entry == nil || entry.ListItem == nil {
		return ""
	}
	switch entry.ListItem.Who {
	case "claude_code":
		return string(agent.BackendClaudeCode)
	case "codex":
		return string(agent.BackendCodex)
	default:
		return ""
	}
}

// taskForcedBackend is the backend a task must run on: a checked list task's
// assistant, or the coding agent a hand-off pinned (Agents C1). The run fails
// rather than route elsewhere (checkListHandoffRoute).
func taskForcedBackend(entry *handoff.Entry) string {
	if backend := listHandoffBackend(entry); backend != "" {
		return backend
	}
	if entry == nil {
		return ""
	}
	switch entry.Backend {
	case string(agent.BackendClaudeCode), string(agent.BackendCodex):
		return entry.Backend
	default:
		return ""
	}
}

func (a *App) validateListHandoffStart(entry *handoff.Entry) error {
	if entry == nil || entry.ListItem == nil {
		return nil
	}
	if listHandoffBackend(entry) == "" || a.IsClosedEyesModeEnabled() {
		return errors.New("this list task cannot start as code work")
	}
	info, err := os.Stat(entry.Workdir)
	if mobileTaskRunner(loadAIProviderConfig().Provider) != entry.ListItem.Who || !filepath.IsAbs(entry.Workdir) || err != nil || !info.IsDir() {
		return errors.New("the selected runner or project is unavailable")
	}
	projects := a.GetAssistantHistory().Projects
	project, ok := brainDumpProject(entry.ListItem.Project, projects)
	if !ok && entry.Thread != nil {
		for _, event := range entry.Thread.Events {
			if event.ID == brainDumpProjectEvent && event.ChosenID != "" {
				project, ok = brainDumpChoiceProject(event.ChosenID, projects)
				break
			}
		}
	}
	if !ok || filepath.Clean(project.Path) != filepath.Clean(entry.Workdir) {
		return errors.New("the selected project is no longer available")
	}
	return nil
}

func checkListHandoffRoute(expected string, event *agent.StreamEvent) error {
	if expected != "" && event != nil && event.Type == "backend_route" && event.Backend != expected {
		return errListRunnerUnavailable
	}
	return nil
}

type handoffRunState struct {
	sessionID    string
	attemptID    string
	cancel       context.CancelFunc
	lastActivity time.Time
	runID        string
	runner       string
}

func (a *App) noteHandoffRunRoute(entry *handoff.Entry, event *agent.StreamEvent) {
	if entry == nil || event == nil || event.Type != "run_created" {
		return
	}
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	run := a.handoffRuns[entry.ID]
	if run == nil || run.attemptID != entry.AttemptID {
		return
	}
	run.runID = event.RunID
	switch event.Backend {
	case "claude-code":
		run.runner = "claude_code"
	case "codex":
		run.runner = "codex"
	case "native":
		run.runner = "isoform"
	default:
		run.runner = ""
	}
}

func (a *App) handoffEntryForSession(sessionID string) *handoff.Entry {
	if a == nil || a.handoffStore == nil || sessionID == "" {
		return nil
	}
	entries, err := a.handoffStore.List()
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.ChatSessionID == sessionID {
			return &entry
		}
	}
	return nil
}

func (a *App) registerHandoffRun(entry *handoff.Entry, workdir string, cancel context.CancelFunc) error {
	if entry == nil {
		return nil
	}
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	if a.handoffRuns[entry.ID] != nil {
		cancel()
		return errHandoffRunBusy
	}
	now := time.Now()
	saved, err := a.handoffStore.MutateIfAttempt(entry.ID, entry.AttemptID, []handoff.Status{handoff.StatusInProgress}, func(e *handoff.Entry) {
		e.Workdir = workdir
		e.LastActivityAt = now.UnixMilli()
	})
	if err != nil {
		cancel()
		return err
	}
	if a.handoffRuns == nil {
		a.handoffRuns = map[string]*handoffRunState{}
	}
	*entry = saved
	a.handoffRuns[entry.ID] = &handoffRunState{sessionID: entry.ChatSessionID, attemptID: entry.AttemptID, cancel: cancel, lastActivity: now}
	return nil
}

func (a *App) touchHandoffRun(entry *handoff.Entry, now time.Time) {
	if entry == nil {
		return
	}
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	run := a.handoffRuns[entry.ID]
	if run == nil || run.attemptID != entry.AttemptID {
		return
	}
	run.lastActivity = now
	// Bound disk writes during token streaming. Timeout checks use the exact
	// in-process time; recovery always marks a run without a process interrupted.
	if now.UnixMilli()-entry.LastActivityAt < time.Minute.Milliseconds() {
		return
	}
	if _, err := a.handoffStore.MutateIfAttempt(entry.ID, entry.AttemptID, []handoff.Status{handoff.StatusInProgress}, func(e *handoff.Entry) { e.LastActivityAt = now.UnixMilli() }); err == nil {
		entry.LastActivityAt = now.UnixMilli()
	}
}

// Caller holds handoffActionMu. Invalidate before cancelling so a completion
// callback cannot race Stop and produce a completed result.
func (a *App) cancelHandoffRunLocked(entry handoff.Entry) {
	if a.bgShells != nil {
		a.bgShells.RemoveForSession(entry.ChatSessionID)
	}
	if run := a.handoffRuns[entry.ID]; run != nil && run.attemptID == entry.AttemptID {
		delete(a.handoffRuns, entry.ID)
		run.cancel()
	}
	a.cancelHandoffTurn(entry.ChatSessionID)
}

func (a *App) handoffSessionBusy(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	a.handoffActionMu.Lock()
	defer a.handoffActionMu.Unlock()
	for _, run := range a.handoffRuns {
		if run.sessionID == sessionID {
			return true
		}
	}
	return false
}

func (a *App) handoffSessionStopped(sessionID string) bool {
	entry := a.handoffEntryForSession(sessionID)
	return entry != nil && (entry.Status == handoff.StatusAborted || entry.Status == handoff.StatusBlocked)
}
