package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	{"watch", (*App).forgetAllWatchRecordings},
	{"meetings", (*App).forgetAllMeetingNotes},
	{"files", (*App).forgetAllFileActivity},
	{"mail", (*App).forgetAllMail},
	{"messages", func(a *App, _ context.Context) error { return a.ForgetMessages() }},
	{"voices", func(a *App, _ context.Context) error { return a.ForgetVoices() }},
	{"chats", (*App).forgetAllChatThreads},
	{"handoffs", func(a *App, _ context.Context) error { return a.ForgetWorkOnItHandoff("") }},
	{"assistantHistory", func(a *App, _ context.Context) error { return a.forgetAssistantHistory() }},
	{"memory", (*App).forgetAllMemory},
	{"knowledge", (*App).forgetAllKnowledge},
	{"people", (*App).forgetAllPeople},
	{"answers", func(a *App, _ context.Context) error { return a.DeleteAnswers() }},
}

type forgetEverythingJob struct {
	mu      sync.Mutex
	running bool
	timer   *time.Timer
}

var forgetEverythingRun forgetEverythingJob

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
	status := loadForgetEverything()
	if status.State != forgetRunning && status.State != forgetPending {
		status = ForgetEverythingStatus{State: forgetRunning, StartedAt: mobileTimestamp(time.Now())}
		for _, step := range forgetEverythingSteps {
			status.Steps = append(status.Steps, ForgetStep{ID: step.id, State: "waiting"})
		}
		if err := saveForgetEverything(status); err != nil {
			return status, err
		}
		log.Printf("[forget-everything] started steps=%d", len(status.Steps))
	}
	a.runForgetEverythingSoon(0)
	return status, nil
}

// resumeForgetEverything runs at launch: a job that was running or pending
// when the app stopped carries on.
func (a *App) resumeForgetEverything() {
	status := loadForgetEverything()
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

// forgetAllPeople keeps the person's own row, so a call still knows which
// side is theirs.
func (a *App) forgetAllPeople(_ context.Context) error {
	return forgetEach(func() ([]string, error) {
		people, err := a.ListPrincipals()
		ids := make([]string, 0, len(people))
		for _, p := range people {
			if p.Kind != storage.PrincipalKindSelf {
				ids = append(ids, p.ID)
			}
		}
		return ids, err
	}, a.DeletePrincipal)
}
