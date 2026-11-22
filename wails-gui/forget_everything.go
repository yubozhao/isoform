package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/privacy"
	"wails-gui/storage"
)

// forget_everything.go is Settings › Your data › Forget everything
// (computer-data v1.0.0 rule 6; docs/forget-everything-2026-11-16.md): one
// job that removes everything Isoform holds about the person, each store
// through its own forget path. Settings, the phone pairing and the app stay.
//
// The job is saved after every step, so a quit or a crash resumes where it
// stopped, and a step that failed leaves the job pending and is tried again.
// A step counts as done only when what it removes is gone.

const (
	forgetEverythingFile  = "forget-everything.json"
	forgetEverythingEvent = "your-data:forget"
	// forgetEverythingRetry is how long a pending job waits before trying its
	// failed steps again.
	forgetEverythingRetry = 5 * time.Minute
	// forgetEverythingLimit bounds a per-item step's loop.
	forgetEverythingLimit = 100000
)

// Job states.
const (
	forgetIdle    = "idle"
	forgetRunning = "running"
	forgetPending = "pending"
	forgetDone    = "done"
)

// ForgetStep is one store's step as the page shows it.
type ForgetStep struct {
	ID    string `json:"id"`
	State string `json:"state"` // "waiting", "done" or "failed"
	Error string `json:"error,omitempty"`
}

// ForgetEverythingStatus is the job's saved state.
type ForgetEverythingStatus struct {
	// JobID names this run of the job, the same across retries and restarts.
	JobID      string       `json:"jobId,omitempty"`
	State      string       `json:"state"`
	StartedAt  string       `json:"startedAt,omitempty"`
	FinishedAt string       `json:"finishedAt,omitempty"`
	Steps      []ForgetStep `json:"steps"`
}

// forgetEverythingStep removes one store. It returns nil only when the store
// holds nothing more of the person's.
type forgetEverythingStep struct {
	id  string
	run func(a *App, ctx context.Context) error
}

// forgetEverythingSteps is the order: sources first, then what was learned
// from them, then answers and copies, so no reader shows what a later step
// still holds.
var forgetEverythingSteps = []forgetEverythingStep{
	{"activity", (*App).forgetAllActivitySessions},
	{"moments", (*App).forgetAllMoments},
	{"watch", (*App).forgetAllWatchRecordings},
	{"meetings", (*App).forgetAllMeetingNotes},
	{"files", (*App).forgetAllFileActivity},
	{"mail", (*App).forgetAllMail},
	{"calendar", (*App).forgetAllCalendar},
	{"messages", func(a *App, _ context.Context) error { return a.ForgetMessages() }},
	{"voices", func(a *App, _ context.Context) error { return a.ForgetVoices() }},
	{"chats", (*App).forgetAllChatThreads},
	{"mainChat", (*App).forgetMainChat},
	{"handoffs", func(a *App, _ context.Context) error { return a.ForgetWorkOnItHandoff("") }},
	{"assistantHistory", func(a *App, _ context.Context) error { return a.forgetAssistantHistory() }},
	{"memory", (*App).forgetAllMemory},
	{"knowledge", (*App).forgetAllKnowledge},
	{"playbooks", (*App).forgetAllPlaybooks},
	{"knowHowDrafts", (*App).forgetAllKnowHowDrafts},
	{"people", (*App).forgetAllPeople},
	{"topics", (*App).forgetAllTopics},
	{"answers", func(a *App, _ context.Context) error { return a.DeleteAnswers() }},
	// The briefs and their sealed copies, packages and recordings (the brief
	// store's own forget, mobile_brief_forget.go).
	{"briefs", (*App).forgetAllBriefs},
	{"workProfile", (*App).forgetWorkProfile},
	{"logs", (*App).forgetDumpsAndLogs},
	{"agentOfYou", (*App).forgetAgentOfYouProfile},
	{"phones", (*App).forgetOnPhones},
	// What the assistant was given (See them): last, so no step above
	// records an entry after it.
	{"assistantCalls", (*App).forgetAllAssistantCalls},
}

type forgetEverythingJob struct {
	mu      sync.Mutex
	running bool
	timer   *time.Timer
}

var forgetEverythingRun forgetEverythingJob

// forgetEverythingApp is the app the job runs for, for callbacks such as a
// phone's confirmation.
var forgetEverythingApp atomic.Pointer[App]

func forgetEverythingPath() (string, error) {
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, forgetEverythingFile), nil
}

func loadForgetEverything() ForgetEverythingStatus {
	status := ForgetEverythingStatus{State: forgetIdle, Steps: []ForgetStep{}}
	path, err := forgetEverythingPath()
	if err != nil {
		return status
	}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &status)
	}
	return status
}

func saveForgetEverything(status ForgetEverythingStatus) error {
	path, err := forgetEverythingPath()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// GetForgetEverything is the job's state for the page.
func (a *App) GetForgetEverything() ForgetEverythingStatus {
	return loadForgetEverything()
}

// StartForgetEverything starts the job, or resumes one that is pending. It
// returns at once; progress arrives as your-data:forget events.
func (a *App) StartForgetEverything() (ForgetEverythingStatus, error) {
	forgetEverythingApp.Store(a)
	status := loadForgetEverything()
	if status.State != forgetRunning && status.State != forgetPending {
		now := time.Now()
		// Builds already running save nothing from before now.
		if err := forgetCutoff().Set(now); err != nil {
			return status, err
		}
		status = ForgetEverythingStatus{JobID: uuid.NewString(), State: forgetRunning, StartedAt: mobileTimestamp(now)}
		for _, step := range forgetEverythingSteps {
			status.Steps = append(status.Steps, ForgetStep{ID: step.id, State: "waiting"})
		}
		if err := saveForgetEverything(status); err != nil {
			return status, err
		}
		noteForgetQuiet(status)
		log.Printf("[forget-everything] started steps=%d", len(status.Steps))
	}
	a.runForgetEverythingSoon(0)
	return status, nil
}

// resumeForgetEverything runs at launch: a job that was running or pending
// when the app stopped carries on.
func (a *App) resumeForgetEverything() {
	forgetEverythingApp.Store(a)
	status := loadForgetEverything()
	noteForgetQuiet(status)
	if status.State == forgetRunning || status.State == forgetPending {
		log.Printf("[forget-everything] resuming state=%s", status.State)
		a.runForgetEverythingSoon(0)
	}
}

func (a *App) runForgetEverythingSoon(after time.Duration) {
	forgetEverythingRun.mu.Lock()
	defer forgetEverythingRun.mu.Unlock()
	if forgetEverythingRun.timer != nil {
		forgetEverythingRun.timer.Stop()
	}
	forgetEverythingRun.timer = time.AfterFunc(after, func() { a.runForgetEverything(context.Background()) })
}

// runForgetEverything runs every step not done yet, saving after each.
func (a *App) runForgetEverything(ctx context.Context) ForgetEverythingStatus {
	forgetEverythingRun.mu.Lock()
	if forgetEverythingRun.running {
		forgetEverythingRun.mu.Unlock()
		return loadForgetEverything()
	}
	forgetEverythingRun.running = true
	forgetEverythingRun.mu.Unlock()
	defer func() {
		forgetEverythingRun.mu.Lock()
		forgetEverythingRun.running = false
		forgetEverythingRun.mu.Unlock()
	}()

	status := loadForgetEverything()
	if status.State != forgetRunning && status.State != forgetPending {
		return status
	}
	status.State = forgetRunning
	failed := false
	for _, step := range forgetEverythingSteps {
		i := forgetStepIndex(&status, step.id)
		if status.Steps[i].State == "done" {
			continue
		}
		err := runForgetStep(ctx, a, step)
		if err != nil {
			failed = true
			status.Steps[i] = ForgetStep{ID: step.id, State: "failed", Error: err.Error()}
			log.Printf("[forget-everything] step=%s failed: %v", step.id, err)
		} else {
			status.Steps[i] = ForgetStep{ID: step.id, State: "done"}
		}
		if err := saveForgetEverything(status); err != nil {
			log.Printf("[forget-everything] not saved: %v", err)
		}
		a.emitBrowserEvent(forgetEverythingEvent, status)
	}
	if failed {
		status.State = forgetPending
		a.runForgetEverythingSoon(forgetEverythingRetry)
	} else {
		status.State, status.FinishedAt = forgetDone, mobileTimestamp(time.Now())
		log.Printf("[forget-everything] done")
	}
	if err := saveForgetEverything(status); err != nil {
		log.Printf("[forget-everything] not saved: %v", err)
	}
	noteForgetQuiet(status)
	a.emitBrowserEvent(forgetEverythingEvent, status)
	return status
}

// forgetStepIndex finds the step's saved row, adding one for a step a newer
// build added since the job started.
func forgetStepIndex(status *ForgetEverythingStatus, id string) int {
	for i, s := range status.Steps {
		if s.ID == id {
			return i
		}
	}
	status.Steps = append(status.Steps, ForgetStep{ID: id, State: "waiting"})
	return len(status.Steps) - 1
}

// runForgetStep keeps a panicking store from stopping the job.
func runForgetStep(ctx context.Context, a *App, step forgetEverythingStep) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("step %s stopped: %v", step.id, r)
		}
	}()
	return step.run(a, ctx)
}

var errForgetNotReady = errors.New("this part of Isoform is not ready yet")

// forgetEach removes every item list returns with forget, then reads the list
// again: the step is done only when it comes back empty.
func forgetEach(list func() ([]string, error), forget func(string) error) error {
	ids, err := list()
	if err != nil {
		return err
	}
	var last error
	for i, id := range ids {
		if i == forgetEverythingLimit {
			break
		}
		if err := forget(id); err != nil {
			last = err
		}
	}
	left, err := list()
	if err != nil {
		return err
	}
	if len(left) > 0 {
		if last == nil {
			last = errors.New("some items are still kept")
		}
		return fmt.Errorf("%d left: %w", len(left), last)
	}
	return nil
}

func (a *App) forgetAllActivitySessions(_ context.Context) error {
	if a.activity == nil || a.activity.store == nil {
		return errForgetNotReady
	}
	store := a.activity.store
	return forgetEach(func() ([]string, error) {
		rows, err := store.GetFilteredSessionsIncludingProtected(forgetEverythingLimit, 0, 0, 0, "")
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids, err
	}, store.eraseSessionCapture)
}

func (a *App) forgetAllWatchRecordings(_ context.Context) error {
	return forgetEach(func() ([]string, error) {
		recordings, err := a.GetWatchRecordings()
		ids := make([]string, 0, len(recordings))
		for _, r := range recordings {
			ids = append(ids, r.ID)
		}
		return ids, err
	}, a.DeleteWatchRecording)
}

func (a *App) forgetAllMeetingNotes(_ context.Context) error {
	return forgetEach(func() ([]string, error) {
		notes, err := a.GetRecentMeetingNotes(forgetEverythingLimit)
		ids := make([]string, 0, len(notes))
		for _, n := range notes {
			ids = append(ids, n.MeetingKey)
		}
		return ids, err
	}, a.DeleteMeetingNotes)
}

func (a *App) forgetAllFileActivity(_ context.Context) error {
	if a.activity == nil || a.activity.store == nil {
		return errForgetNotReady
	}
	if _, err := a.activity.store.DeleteAllFileActivity(); err != nil {
		return err
	}
	if err := a.activity.store.DeleteAllBrowserActivity(); err != nil {
		return err
	}
	_, err := a.activity.store.ResetAllOCR()
	return err
}

func (a *App) forgetAllMail(ctx context.Context) error {
	if a.mailSync == nil {
		return nil
	}
	a.mailSync.clearStored(ctx, "forget everything")
	return nil
}

func (a *App) forgetAllChatThreads(ctx context.Context) error {
	if a.store == nil {
		return errForgetNotReady
	}
	return forgetEach(func() ([]string, error) {
		byProject, err := a.store.LoadAllSessions(ctx)
		var ids []string
		for _, rows := range byProject {
			for _, row := range rows {
				if !a.isMainChatSession(ctx, row.LocalID) {
					ids = append(ids, row.LocalID)
				}
			}
		}
		return ids, err
	}, a.DeleteSessionData)
}

func (a *App) forgetAllMemory(_ context.Context) error {
	return forgetEach(func() ([]string, error) {
		entries, err := a.GetMemoryEntries()
		ids := make([]string, 0, len(entries))
		for _, e := range entries {
			ids = append(ids, e.ID)
		}
		return ids, err
	}, func(id string) error {
		preview, err := a.GetMemoryForgetPreview(id)
		if err != nil {
			return err
		}
		return a.ForgetMemoryEntry(id, preview.ConfirmationToken)
	})
}

func (a *App) forgetAllKnowledge(_ context.Context) error {
	return forgetEach(func() ([]string, error) {
		entries, err := a.GetKnowledgeEntries()
		ids := make([]string, 0, len(entries))
		for _, e := range entries {
			ids = append(ids, e.ID)
		}
		return ids, err
	}, a.DeleteKnowledgeEntry)
}

// forgetAllPeople forgets everyone else, and keeps the person's own row as
// identity only: the name and the "this is me" link, so a call still knows
// which side is theirs. Everything learned about them from it goes.
func (a *App) forgetAllPeople(ctx context.Context) error {
	if err := forgetEach(func() ([]string, error) {
		people, err := a.ListPrincipals()
		ids := make([]string, 0, len(people))
		for _, p := range people {
			if p.Kind != storage.PrincipalKindSelf {
				ids = append(ids, p.ID)
			}
		}
		return ids, err
	}, a.DeletePrincipal); err != nil {
		return err
	}
	if a.store == nil {
		return errForgetNotReady
	}
	selves, err := a.store.ListPrincipalsByKind(ctx, storage.PrincipalKindSelf)
	if err != nil {
		return err
	}
	for _, self := range selves {
		if err := a.store.ResetPrincipalToIdentity(ctx, self.ID); err != nil {
			return err
		}
	}
	return nil
}

// forgetAllPlaybooks removes every playbook through DeleteSOP, which also
// drops its schedule.
func (a *App) forgetAllPlaybooks(_ context.Context) error {
	if a.sopStore == nil {
		return nil
	}
	return forgetEach(func() ([]string, error) {
		entries, err := a.sopStore.List()
		ids := make([]string, 0, len(entries))
		for _, e := range entries {
			ids = append(ids, e.ID)
		}
		return ids, err
	}, a.DeleteSOP)
}

// forgetAllKnowHowDrafts removes the proposed know-how drafts from their store.
func (a *App) forgetAllKnowHowDrafts(_ context.Context) error {
	if a.knowHowCandidates == nil {
		return nil
	}
	store := a.knowHowCandidates
	err := forgetEach(func() ([]string, error) {
		entries, err := store.List()
		ids := make([]string, 0, len(entries))
		for _, e := range entries {
			ids = append(ids, e.ID)
		}
		return ids, err
	}, store.Delete)
	a.knowHow.candMu.Lock()
	a.invalidateKnowHowCandidateCacheLocked()
	a.knowHow.candMu.Unlock()
	return err
}

// forgetAllCalendar removes the stored calendar copies, and tells every
// reader (Today, the brief) through the calendar withdrawal.
func (a *App) forgetAllCalendar(ctx context.Context) error {
	if a.store == nil {
		return errForgetNotReady
	}
	if err := a.store.DeleteAllCalendarEvents(ctx); err != nil {
		return err
	}
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Account, ID: "calendar"})
	return nil
}

// forgetAllMoments removes the moments made from activity (Activity's own
// Forget, DeleteActivity, with its topic scrub and withdrawal) and the
// extracted events, which a session's erase leaves in place.
func (a *App) forgetAllMoments(_ context.Context) error {
	if a.activity == nil || a.activity.store == nil {
		return errForgetNotReady
	}
	store := a.activity.store
	if err := forgetEach(func() ([]string, error) {
		rows, err := store.GetActivitiesInRange(0, math.MaxInt64)
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids, err
	}, a.DeleteActivity); err != nil {
		return err
	}
	return forgetEach(func() ([]string, error) {
		rows, err := store.GetSemanticEventsInRange(0, math.MaxInt64)
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids, err
	}, store.DeleteSemanticEvent)
}

// forgetMainChat empties the main chat and keeps it: one empty main chat with
// the same id. A turn running in it stops first.
func (a *App) forgetMainChat(ctx context.Context) error {
	if a.store == nil {
		return errForgetNotReady
	}
	if row, err := a.store.LoadMainSession(ctx); err == nil && row != nil {
		a.cancelActiveChatTurn(row.LocalID)
		a.waitForChatTurnEnd(row.LocalID)
	}
	id, err := a.store.ClearMainChat(ctx)
	if err != nil || id == "" {
		return err
	}
	publishPrivacyWithdrawal(privacy.Scope{Kind: privacy.Session, ID: id})
	return nil
}
