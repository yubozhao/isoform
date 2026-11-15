package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"wails-gui/storage"
	agent "yansu-agent/agent"
)

// lockTimeout is how long CRUD operations wait for s.mu before giving up.
const lockTimeout = 5 * time.Second

// dbTimeout is how long CRUD operations wait for a SQLite DB call.
const dbTimeout = 10 * time.Second

var ErrDuplicateAutomation = errors.New("duplicate automation")

// ---------- public DTOs (Wails-serialized) ----------

// CronJobInput is the payload for creating a new cron job.
type CronJobInput struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	ScheduleKind     string          `json:"scheduleKind"`               // "at", "every", "cron", "manual"
	ScheduleConfig   json.RawMessage `json:"scheduleConfig"`             // kind-specific JSON
	ScheduleEvidence string          `json:"scheduleEvidence,omitempty"` // verbatim quote justifying LLM-inferred cron schedule
	TargetKind       string          `json:"targetKind"`                 // "agent"
	TargetConfig     json.RawMessage `json:"targetConfig"`               // target-specific JSON
	DeleteAfterRun   bool            `json:"deleteAfterRun"`
	RetryPolicy      json.RawMessage `json:"retryPolicy"`     // optional
	SourceSessionID  string          `json:"sourceSessionId"` // chat session that created this job
	SourcePlatform   string          `json:"sourcePlatform"`  // "slack", "feishu", "" for GUI
	NotifyDM         *bool           `json:"notifyDm,omitempty"`
	NotifyPlatform   *string         `json:"notifyPlatform,omitempty"`
	SurfaceKind      string          `json:"surfaceKind,omitempty"` // "" or "scheduled" (legacy default), "handoff"
}

// CronJobPatch is the payload for updating an existing cron job.
type CronJobPatch struct {
	Name           *string          `json:"name,omitempty"`
	Description    *string          `json:"description,omitempty"`
	Enabled        *bool            `json:"enabled,omitempty"`
	ScheduleKind   *string          `json:"scheduleKind,omitempty"`
	ScheduleConfig *json.RawMessage `json:"scheduleConfig,omitempty"`
	TargetKind     *string          `json:"targetKind,omitempty"`
	TargetConfig   *json.RawMessage `json:"targetConfig,omitempty"`
	DeleteAfterRun *bool            `json:"deleteAfterRun,omitempty"`
	RetryPolicy    *json.RawMessage `json:"retryPolicy,omitempty"`
	NotifyDM       *bool            `json:"notifyDm,omitempty"`
	NotifyPlatform *string          `json:"notifyPlatform,omitempty"`
}

// CronJobState is the Go struct auto-serialized to TypeScript by Wails.
type CronJobState struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	Enabled             bool   `json:"enabled"`
	ScheduleKind        string `json:"scheduleKind"`
	ScheduleConfig      any    `json:"scheduleConfig"`
	ScheduleEvidence    string `json:"scheduleEvidence"`
	TargetKind          string `json:"targetKind"`
	TargetConfig        any    `json:"targetConfig"`
	DeleteAfterRun      bool   `json:"deleteAfterRun"`
	RetryPolicy         any    `json:"retryPolicy"`
	NextRunAt           int64  `json:"nextRunAt"`
	LastRunAt           int64  `json:"lastRunAt"`
	LastRunStatus       string `json:"lastRunStatus"`
	LastError           string `json:"lastError"`
	ConsecutiveErrors   int    `json:"consecutiveErrors"`
	RetryAttempt        int    `json:"retryAttempt"`
	RetryRunAt          int64  `json:"retryRunAt"`
	SourceSessionID     string `json:"sourceSessionId"`
	SourcePlatform      string `json:"sourcePlatform"`
	NotifyDM            bool   `json:"notifyDm"`
	NotifyPlatform      string `json:"notifyPlatform"`
	SurfaceKind         string `json:"surfaceKind"`
	SortOrder           int    `json:"sortOrder"`
	CreatedAt           int64  `json:"createdAt"`
	UpdatedAt           int64  `json:"updatedAt"`
	ScheduleDescription string `json:"scheduleDescription"`
}

// CronRunState is the Go struct for run history (Wails-serialized).
type CronRunState struct {
	ID                string   `json:"id"`
	JobID             string   `json:"jobId"`
	StartedAt         int64    `json:"startedAt"`
	FinishedAt        int64    `json:"finishedAt"`
	Status            string   `json:"status"`
	TriggerKind       string   `json:"triggerKind"`
	Error             string   `json:"error"`
	DurationMs        int64    `json:"durationMs"`
	ResultSummary     string   `json:"resultSummary"`
	HasLogs           bool     `json:"hasLogs"`
	NotifyResult      string   `json:"notifyResult"`
	CandidateBackends []string `json:"candidateBackends,omitempty"`
	Backend           string   `json:"backend,omitempty"`
	FallbackFrom      []string `json:"fallbackFrom,omitempty"`
	BackendReason     string   `json:"backendReason,omitempty"`
	Model             string   `json:"model,omitempty"`
	ActionsStarted    int      `json:"actionsStarted,omitempty"`
}

// CronRunWithJobName enriches a run with its parent job's name for timeline display.
type CronRunWithJobName struct {
	CronRunState
	JobName string `json:"jobName"`
}

// cronExecResult holds the output of an agent job execution.
type cronExecResult struct {
	Summary        string
	SessionTag     string
	ItemSortStart  int
	FilePaths      []string        // deliverable file paths declared by agent
	DeliveryTarget *DeliveryTarget // user-specified channel delivery target (nil = DM only)
	WorkDir        string          // job's working directory (for path validation)
	SessionTempDir string          // job-specific temp directory (for path validation)
	// WrittenFiles are the canonical paths this run created through its Write,
	// Edit or NotebookEdit tools. Only these files (plus anything inside WorkDir
	// or SessionTempDir) may be uploaded to the backend, so a run that merely
	// reads a sensitive file such as ~/.ssh/id_ed25519 cannot name it as a
	// deliverable and exfiltrate it.
	WrittenFiles []string
}

// ---------- schedule config shapes ----------

type scheduleAt struct {
	AtMs int64 `json:"at_ms"`
}

type scheduleEvery struct {
	EveryMs int64 `json:"every_ms"`
}

type scheduleCron struct {
	Expr string `json:"expr"`
	TZ   string `json:"tz,omitempty"`
}

// ---------- retry policy shape ----------

type retryPolicy struct {
	MaxAttempts int   `json:"max_attempts"` // 0 = no retry
	BaseMs      int64 `json:"base_ms"`      // e.g. 60000 (1 min)
	MaxBackoff  int64 `json:"max_backoff"`  // cap in ms
}

// ---------- scheduler ----------

const (
	maxConcurrentJobs = 5
	maxCatchupJobs    = 5
	// Stagger overdue-job dispatch enough for the first job's NewSession
	// (~3-5s cold start) and Prompt streaming to begin before firing the
	// next one.
	catchupStaggerMs     = 10000
	maxTimerDuration     = 60 * time.Second
	executeTimeout       = 60 * time.Minute
	agentReadyTimeout    = 60 * time.Second
	autoDisableThreshold = 10
	pruneKeep            = 200
)

// CronScheduler owns the timer loop for cron job execution.
// It follows the SlackIncrementalScanner pattern: a struct with a
// context-based Run loop, a manual trigger channel, and Stop().
type CronScheduler struct {
	store *storage.Store

	// Serialize edits and dispatch with a conditional Today Undo.
	changeMu sync.Mutex
	mu       sync.Mutex
	muHolder string                         // diagnostic: who currently holds the lock
	jobs     map[string]*storage.CronJobRow // in-memory cache
	running  map[string]bool                // job IDs currently in-flight
	// missedThrough is the newest scheduled time each job recorded as
	// missed in this process (see recordMissed).
	missedThrough map[string]int64

	triggerCh chan struct{}
	semaphore chan struct{}
	loadedCh  chan struct{} // closed when initial loadJobs() completes

	wg     sync.WaitGroup
	cancel context.CancelFunc
	once   sync.Once

	emit             func(event string, data any)
	notify           func(title, body string)
	execAgent        func(ctx context.Context, job *storage.CronJobRow) (*cronExecResult, error)
	checkDuplicate   func(ctx context.Context, input CronJobInput, existing []storage.CronJobRow) (bool, string, error)
	generateName     func(description string) string                                                                                                                                                                                           // optional; may be nil
	notifyResult     func(automationName, runResult, runStatus string, durationMs int64, sendDM bool, notifyPlatform string, filePaths []string, deliveryTarget *DeliveryTarget, workDir, sessionTempDir string, writtenFiles []string) string // optional; returns JSON notify result
	onRunLifecycle   func(CronRunLifecycleEvent)
	telemetryCapture func(event string, properties map[string]any)
	// onMissed is told the job id after missed times were recorded for it.
	onMissed func(jobID string)
}

// NewCronScheduler creates a scheduler bound to the given store.
func NewCronScheduler(
	store *storage.Store,
	emit func(string, any),
	notify func(string, string),
	execAgent func(ctx context.Context, job *storage.CronJobRow) (*cronExecResult, error),
) *CronScheduler {
	return &CronScheduler{
		store:         store,
		jobs:          make(map[string]*storage.CronJobRow),
		running:       make(map[string]bool),
		missedThrough: make(map[string]int64),
		triggerCh:     make(chan struct{}, 1),
		semaphore:     make(chan struct{}, maxConcurrentJobs),
		loadedCh:      make(chan struct{}),
		emit:          emit,
		notify:        notify,
		execAgent:     execAgent,
	}
}

// SetNotifyResult registers the backend notification callback.
// Called after construction because it depends on App methods (API base URL, auth token).
// The callback returns a JSON string describing the DM notification outcome.
func (s *CronScheduler) SetNotifyResult(fn func(automationName, runResult, runStatus string, durationMs int64, sendDM bool, notifyPlatform string, filePaths []string, deliveryTarget *DeliveryTarget, workDir, sessionTempDir string, writtenFiles []string) string) {
	s.notifyResult = fn
}

// SetTelemetryCapture registers the telemetry callback for feature-usage tracking.
func (s *CronScheduler) SetTelemetryCapture(fn func(event string, properties map[string]any)) {
	s.telemetryCapture = fn
}

// SetGenerateName registers the AI name generation callback.
// Called after construction because it depends on App methods.
func (s *CronScheduler) SetGenerateName(fn func(string) string) {
	s.generateName = fn
}

// SetOnMissed registers the callback told about a job after missed times
// were recorded for it (the missed-schedule notice checks it).
func (s *CronScheduler) SetOnMissed(fn func(jobID string)) {
	s.onMissed = fn
}

// SetRunLifecycle registers a normalized run callback used by initiative receipts.
func (s *CronScheduler) SetRunLifecycle(fn func(CronRunLifecycleEvent)) {
	s.onRunLifecycle = fn
}

func (s *CronScheduler) emitRunLifecycle(event CronRunLifecycleEvent) {
	if s.onRunLifecycle == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("[cron] run lifecycle callback panicked: %v", recovered)
		}
	}()
	s.onRunLifecycle(event)
}

func (s *CronScheduler) SetCheckDuplicate(fn func(context.Context, CronJobInput, []storage.CronJobRow) (bool, string, error)) {
	s.checkDuplicate = fn
}

// Run starts the timer loop. Blocks until ctx is cancelled or Stop() is called.
func (s *CronScheduler) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.trackedLock()
	s.cancel = cancel
	s.trackedUnlock()
	defer cancel()

	// Load all jobs into memory.
	if err := s.loadJobs(ctx); err != nil {
		log.Printf("[cron] failed to load jobs: %v", err)
		close(s.loadedCh) // unblock any ListJobs callers
		return
	}
	close(s.loadedCh)

	// Startup catchup: fire overdue jobs (max 5).
	s.startupCatchup(ctx)

	log.Printf("[cron] scheduler started (%d jobs loaded)", len(s.jobs))

	s.timerLoop(ctx)

	log.Printf("[cron] scheduler stopped")
}

// Stop cancels the scheduler context and waits for all in-flight jobs.
func (s *CronScheduler) Stop() {
	s.once.Do(func() {
		s.trackedLock()
		if s.cancel != nil {
			s.cancel()
		}
		s.trackedUnlock()
	})
	s.wg.Wait()
}

// signal requests an immediate timer recompute. Non-blocking.
func (s *CronScheduler) signal() {
	select {
	case s.triggerCh <- struct{}{}:
	default:
	}
}

// trackedLock acquires s.mu and records who holds it for diagnostics.
func (s *CronScheduler) trackedLock() {
	s.mu.Lock()
	if _, file, line, ok := runtime.Caller(1); ok {
		s.muHolder = fmt.Sprintf("%s:%d", filepath.Base(file), line)
	} else {
		s.muHolder = "unknown"
	}
}

// trackedUnlock releases s.mu and clears the holder.
func (s *CronScheduler) trackedUnlock() {
	s.muHolder = ""
	s.mu.Unlock()
}

// lockWithTimeout tries to acquire s.mu within the given duration.
// Returns true if the lock was acquired, false on timeout.
//
// Uses sync.Mutex.TryLock + polling so no background acquire goroutine is
// spawned per call (hot path: tick() every 100 ms). time.NewTimer is used
// instead of time.After so the timer allocation is released promptly
// whether the lock is acquired or we time out.
func (s *CronScheduler) lockWithTimeout(caller string, d time.Duration) bool {
	if s.mu.TryLock() {
		s.muHolder = caller
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-poll.C:
			if s.mu.TryLock() {
				s.muHolder = caller
				return true
			}
		case <-timer.C:
			log.Printf("[cron] TIMEOUT: %s failed to acquire s.mu after %v", caller, d)
			return false
		}
	}
}

// ---------- public API ----------

// AddJob creates a new cron job, persists it, and signals the timer loop.
func (s *CronScheduler) AddJob(ctx context.Context, input CronJobInput) (CronJobState, error) {
	if input.SurfaceKind == todayNewsSurfaceKind && input.Name == todayNewsJobName {
		return CronJobState{}, fmt.Errorf("daily news has been retired")
	}
	if !cronSurfaceOn(input.SurfaceKind) {
		if input.SurfaceKind == playbookSurfaceKind {
			return CronJobState{}, errFeatureOff(featurePlaybooks)
		}
		return CronJobState{}, errFeatureOff(featureAutomations)
	}
	if err := validateTargetProjectDir(input.TargetConfig); err != nil {
		return CronJobState{}, err
	}
	id := uuid.New().String()
	now := time.Now()

	nextRun, err := computeFirstRun(input.ScheduleKind, input.ScheduleConfig, now)
	if err != nil {
		log.Printf("[cron] AddJob: computeFirstRun failed: %v", err)
		return CronJobState{}, fmt.Errorf("compute first run: %w", err)
	}

	notifyDM := true
	if input.NotifyDM != nil {
		notifyDM = *input.NotifyDM
	}
	notifyPlatform := ""
	if input.NotifyPlatform != nil {
		notifyPlatform = *input.NotifyPlatform
	}

	row := storage.CronJobRow{
		ID:               id,
		Name:             input.Name,
		Description:      input.Description,
		Enabled:          true,
		ScheduleKind:     input.ScheduleKind,
		ScheduleConfig:   string(input.ScheduleConfig),
		ScheduleEvidence: input.ScheduleEvidence,
		TargetKind:       input.TargetKind,
		TargetConfig:     string(input.TargetConfig),
		DeleteAfterRun:   input.DeleteAfterRun,
		RetryPolicy:      string(input.RetryPolicy),
		SourceSessionID:  input.SourceSessionID,
		SourcePlatform:   input.SourcePlatform,
		NotifyDM:         notifyDM,
		NotifyPlatform:   notifyPlatform,
		SurfaceKind:      input.SurfaceKind,
		NextRunAt:        nextRun,
	}

	// Handoffs are one-shot, user-accepted tasks — fingerprint dedup is meaningless
	// (each is unique to a specific moment) and would block legitimate sequential
	// handoffs. Skip the entire dedup block for them.
	if input.SurfaceKind == "handoff" {
		saveCtx, saveCancel := context.WithTimeout(context.Background(), dbTimeout)
		defer saveCancel()
		if err := s.store.SaveCronJob(saveCtx, row); err != nil {
			log.Printf("[cron] AddJob: DB save failed for handoff: %v", err)
			return CronJobState{}, fmt.Errorf("save cron job: %w", err)
		}
		if !s.lockWithTimeout("AddJob-handoff", lockTimeout) {
			log.Printf("[cron] AddJob: WARN timeout acquiring lock for handoff %s", id)
			state := jobRowToState(&row)
			s.emit("cron:state", state)
			return state, nil
		}
		func() {
			defer s.trackedUnlock()
			s.jobs[id] = &row
		}()
		s.signal()
		state := jobRowToState(&row)
		s.emit("cron:state", state)
		return state, nil
	}

	listCtx, listCancel := context.WithTimeout(context.Background(), dbTimeout)
	existing, err := s.store.ListCronJobs(listCtx)
	listCancel()
	if err != nil {
		log.Printf("[cron] AddJob: dedup list failed (fail closed): %v", err)
		return CronJobState{}, fmt.Errorf("dedup check: %w", err)
	}

	fp := computeAutomationFingerprint(
		input.Description,
		input.TargetKind,
		string(input.TargetConfig),
	)
	isAutoCreated := input.SourcePlatform != ""
	if fp != "" {
		for _, job := range existing {
			if job.Fingerprint != "" && job.Fingerprint == fp {
				// User-created: allow same task with a different schedule.
				if !isAutoCreated && (job.ScheduleKind != input.ScheduleKind || job.ScheduleConfig != string(input.ScheduleConfig)) {
					continue
				}
				log.Printf("[cron] AddJob: exact fingerprint match with job %q (%s)", job.Name, job.ID)
				return CronJobState{}, fmt.Errorf("%w: exact match with %q (%s)", ErrDuplicateAutomation, job.Name, job.ID)
			}
		}
	}

	// Semantic dedup only for auto-created automations — respect user intent.
	if isAutoCreated && s.checkDuplicate != nil && len(existing) > 0 {
		dedupCtx, dedupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		isDuplicate, matchedName, dedupErr := s.checkDuplicate(dedupCtx, input, existing)
		dedupCancel()
		if dedupErr != nil {
			log.Printf("[cron] AddJob: semantic dedup failed (proceeding): %v", dedupErr)
		} else if isDuplicate {
			log.Printf("[cron] AddJob: semantic duplicate of %q", matchedName)
			return CronJobState{}, fmt.Errorf("%w: semantically similar to %q", ErrDuplicateAutomation, matchedName)
		}
	}

	// Only persist fingerprint for auto-created jobs. Manual jobs do not get a
	// DB fingerprint so the UNIQUE constraint cannot block same-content jobs
	// that were allowed through by the schedule-difference exception above.
	if isAutoCreated {
		row.Fingerprint = fp
	}

	saveCtx, saveCancel := context.WithTimeout(context.Background(), dbTimeout)
	defer saveCancel()
	if err := s.store.SaveCronJob(saveCtx, row); err != nil {
		if isFingerprintUniqueConflict(err) {
			log.Printf("[cron] AddJob: fingerprint UNIQUE conflict for %q", input.Name)
			return CronJobState{}, fmt.Errorf("%w: fingerprint conflict", ErrDuplicateAutomation)
		}
		log.Printf("[cron] AddJob: DB save failed: %v", err)
		return CronJobState{}, fmt.Errorf("save cron job: %w", err)
	}

	if !s.lockWithTimeout("AddJob", lockTimeout) {
		log.Printf("[cron] AddJob: WARN timeout acquiring lock, DB saved but cache not updated for %s", id)
		state := jobRowToState(&row)
		s.emit("cron:state", state)
		return state, nil
	}
	func() {
		defer s.trackedUnlock()
		s.jobs[id] = &row
	}()

	s.signal()

	state := jobRowToState(&row)
	s.emit("cron:state", state)

	if s.telemetryCapture != nil {
		s.telemetryCapture("automation.created", map[string]any{
			"job_id":           state.ID,
			"schedule_kind":    state.ScheduleKind,
			"delete_after_run": state.DeleteAfterRun,
			"source_platform":  input.SourcePlatform,
		})
	}

	// If the name looks auto-truncated (matches the prompt prefix), try
	// generating a better one via AI in the background so the UI doesn't
	// block waiting for the API call.
	if s.generateName != nil {
		prompt := extractPromptFromTargetConfig(input.TargetConfig)
		if prompt != "" && looksAutoTruncated(input.Name, prompt) {
			go func() {
				aiName := s.generateName(prompt)
				if aiName == "" || aiName == input.Name {
					return
				}
				fields := map[string]any{"name": aiName}
				if err := s.store.UpdateCronJob(context.Background(), id, fields); err != nil {
					log.Printf("[cron] async name update for %s failed: %v", id, err)
					return
				}
				func() {
					s.trackedLock()
					defer s.trackedUnlock()
					if cached, ok := s.jobs[id]; ok {
						cached.Name = aiName
					}
				}()
				updated := state
				updated.Name = aiName
				s.emit("cron:state", updated)
			}()
		}
	}

	return state, nil
}

// looksAutoTruncated checks whether the name is just a prefix of the prompt
// (i.e. the frontend used truncation instead of a real title).
func looksAutoTruncated(name, prompt string) bool {
	clean := strings.TrimSuffix(name, "...")
	return strings.HasPrefix(prompt, clean)
}

func isFingerprintUniqueConflict(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") && strings.Contains(msg, "cron_jobs.fingerprint")
}

// validateTargetProjectDir checks the project_dir of an agent target config
// (see validateCronProjectDir). A config without one passes.
func validateTargetProjectDir(tc json.RawMessage) error {
	if len(tc) == 0 {
		return nil
	}
	var cfg struct {
		ProjectDir string `json:"project_dir"`
	}
	if err := json.Unmarshal(tc, &cfg); err != nil {
		return nil
	}
	return validateCronProjectDir(cfg.ProjectDir)
}

func extractPromptFromTargetConfig(tc json.RawMessage) string {
	var cfg map[string]any
	if err := json.Unmarshal(tc, &cfg); err != nil {
		return ""
	}
	prompt, _ := cfg["prompt"].(string)
	return strings.TrimSpace(prompt)
}

// UpdateJob patches an existing cron job and signals the timer loop.
func (s *CronScheduler) UpdateJob(ctx context.Context, id string, patch CronJobPatch) (CronJobState, error) {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	if patch.TargetConfig != nil {
		if err := validateTargetProjectDir(*patch.TargetConfig); err != nil {
			return CronJobState{}, err
		}
	}
	if !s.lockWithTimeout("UpdateJob-read", lockTimeout) {
		return CronJobState{}, fmt.Errorf("timeout acquiring lock for job %s", id)
	}
	var jobCopy storage.CronJobRow
	var found bool
	func() {
		defer s.trackedUnlock()
		job, ok := s.jobs[id]
		if !ok {
			return
		}
		found = true
		jobCopy = *job
	}()
	if !found {
		log.Printf("[cron] UpdateJob: job %s not found in cache", id)
		return CronJobState{}, fmt.Errorf("job %s not found", id)
	}
	if retiredTodayNewsJob(&jobCopy) {
		return CronJobState{}, fmt.Errorf("daily news has been retired")
	}

	fields := make(map[string]any)

	if patch.Name != nil {
		jobCopy.Name = *patch.Name
		fields["name"] = *patch.Name
	}
	if patch.Description != nil {
		jobCopy.Description = *patch.Description
		fields["description"] = *patch.Description
	}
	if patch.Enabled != nil {
		wasEnabled := jobCopy.Enabled
		jobCopy.Enabled = *patch.Enabled
		fields["enabled"] = *patch.Enabled
		if *patch.Enabled {
			jobCopy.ConsecutiveErrors = 0
			jobCopy.RetryAttempt = 0
			jobCopy.RetryRunAt = 0
			fields["consecutive_errors"] = 0
			fields["retry_attempt"] = 0
			fields["retry_run_at"] = 0
			// A recurring schedule resumed after its next time passed
			// waits for its next time instead of running at once, and the
			// times it passed while paused are not missed runs.
			if !wasEnabled && jobCopy.NextRunAt > 0 && jobCopy.NextRunAt <= time.Now().UnixMilli() {
				switch jobCopy.ScheduleKind {
				case "every", "cron":
					jobCopy.NextRunAt = computeNextRun(&jobCopy)
					fields["next_run_at"] = jobCopy.NextRunAt
				default:
				}
			}
		}
	}
	if patch.ScheduleKind != nil {
		jobCopy.ScheduleKind = *patch.ScheduleKind
		fields["schedule_kind"] = *patch.ScheduleKind
	}
	if patch.ScheduleConfig != nil {
		jobCopy.ScheduleConfig = string(*patch.ScheduleConfig)
		fields["schedule_config"] = string(*patch.ScheduleConfig)
	}
	if patch.TargetKind != nil {
		jobCopy.TargetKind = *patch.TargetKind
		fields["target_kind"] = *patch.TargetKind
	}
	if patch.TargetConfig != nil {
		jobCopy.TargetConfig = string(*patch.TargetConfig)
		fields["target_config"] = string(*patch.TargetConfig)
	}
	if patch.DeleteAfterRun != nil {
		jobCopy.DeleteAfterRun = *patch.DeleteAfterRun
		fields["delete_after_run"] = *patch.DeleteAfterRun
	}
	if patch.RetryPolicy != nil {
		jobCopy.RetryPolicy = string(*patch.RetryPolicy)
		fields["retry_policy"] = string(*patch.RetryPolicy)
	}
	if patch.NotifyDM != nil {
		jobCopy.NotifyDM = *patch.NotifyDM
		fields["notify_dm"] = *patch.NotifyDM
	}
	if patch.NotifyPlatform != nil {
		jobCopy.NotifyPlatform = *patch.NotifyPlatform
		fields["notify_platform"] = *patch.NotifyPlatform
	}

	// Recompute next_run_at if schedule changed.
	if patch.ScheduleKind != nil || patch.ScheduleConfig != nil {
		nextRun, err := computeFirstRun(jobCopy.ScheduleKind, json.RawMessage(jobCopy.ScheduleConfig), time.Now())
		if err != nil {
			log.Printf("[cron] UpdateJob: computeFirstRun failed: %v", err)
			return CronJobState{}, fmt.Errorf("compute next run: %w", err)
		}
		jobCopy.NextRunAt = nextRun
		fields["next_run_at"] = nextRun

		// User edited the schedule — the LLM's original justification no longer
		// applies. Clear the evidence so the UI stops attributing it to activity
		// triage and so future debug traces correctly show "user decision".
		jobCopy.ScheduleEvidence = ""
		fields["schedule_evidence"] = ""
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(), dbTimeout)
	defer dbCancel()
	if err := s.store.UpdateCronJob(dbCtx, id, fields); err != nil {
		log.Printf("[cron] UpdateJob: DB write failed: %v", err)
		return CronJobState{}, fmt.Errorf("update cron job: %w", err)
	}

	if !s.lockWithTimeout("UpdateJob-write", lockTimeout) {
		// DB is updated but cache is stale — log but don't fail.
		log.Printf("[cron] UpdateJob: WARN timeout acquiring lock for cache update, DB updated but cache stale for %s", id)
		state := jobRowToState(&jobCopy)
		s.emit("cron:state", state)
		return state, nil
	}
	func() {
		defer s.trackedUnlock()
		s.jobs[id] = &jobCopy
	}()

	s.signal()

	state := jobRowToState(&jobCopy)
	s.emit("cron:state", state)
	return state, nil
}

// RemoveJob deletes a cron job.
func (s *CronScheduler) RemoveJob(ctx context.Context, id string) error {
	dbCtx, dbCancel := context.WithTimeout(context.Background(), dbTimeout)
	defer dbCancel()
	if err := s.store.DeleteCronJob(dbCtx, id); err != nil {
		log.Printf("[cron] RemoveJob: DB delete failed: %v", err)
		return fmt.Errorf("delete cron job: %w", err)
	}

	if !s.lockWithTimeout("RemoveJob", lockTimeout) {
		log.Printf("[cron] RemoveJob: WARN timeout acquiring lock, DB deleted but cache not updated for %s", id)
	} else {
		delete(s.jobs, id)
		s.trackedUnlock()
	}

	s.signal()
	s.emit("cron:state", map[string]any{"id": id, "deleted": true})

	if s.telemetryCapture != nil {
		s.telemetryCapture("automation.deleted", map[string]any{
			"job_id": id,
		})
	}

	return nil
}

// ReorderJobs updates the sort order of cron jobs based on the given ID order.
func (s *CronScheduler) ReorderJobs(ids []string) error {
	if !s.lockWithTimeout("ReorderJobs", lockTimeout) {
		return fmt.Errorf("timeout acquiring lock for reorder")
	}
	defer s.trackedUnlock()

	for i, id := range ids {
		if job, ok := s.jobs[id]; ok {
			job.SortOrder = i
		}
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(), dbTimeout)
	defer dbCancel()
	var firstErr error
	for i, id := range ids {
		if err := s.store.UpdateCronJob(dbCtx, id, map[string]any{"sort_order": i}); err != nil {
			log.Printf("[cron] ReorderJobs: failed to update sort_order for %s: %v", id, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("update sort_order for %s: %w", id, err)
			}
		}
	}

	return firstErr
}

// RunNow triggers an immediate execution of the given job.
func (s *CronScheduler) RunNow(ctx context.Context, id string) error {
	var jobCopy storage.CronJobRow
	var found bool
	func() {
		s.trackedLock()
		defer s.trackedUnlock()
		job, ok := s.jobs[id]
		if !ok {
			return
		}
		found = true
		jobCopy = *job
	}()
	if !found {
		return fmt.Errorf("job %s not found", id)
	}
	if retiredTodayNewsJob(&jobCopy) {
		return fmt.Errorf("daily news has been retired")
	}

	if !s.dispatchJob(ctx, &jobCopy, "manual") {
		return fmt.Errorf("job %s is already running", id)
	}
	return nil
}

// ListJobs returns all cron jobs as state objects.
// It waits for the initial load to complete (up to 5s) to avoid returning
// an empty list due to a race with the goroutine that calls Run().
func (s *CronScheduler) ListJobs() []CronJobState {
	select {
	case <-s.loadedCh:
	case <-time.After(5 * time.Second):
		log.Printf("[cron] ListJobs: timed out waiting for initial load")
	}

	s.trackedLock()
	defer s.trackedUnlock()

	result := make([]CronJobState, 0, len(s.jobs))
	for _, job := range s.jobs {
		result = append(result, jobRowToState(job))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SortOrder != result[j].SortOrder {
			return result[i].SortOrder < result[j].SortOrder
		}
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result
}

// GetJob returns a single cron job state.
func (s *CronScheduler) GetJob(id string) (CronJobState, error) {
	select {
	case <-s.loadedCh:
	case <-time.After(5 * time.Second):
	}

	s.trackedLock()
	defer s.trackedUnlock()

	job, ok := s.jobs[id]
	if !ok {
		return CronJobState{}, fmt.Errorf("job %s not found", id)
	}
	return jobRowToState(job), nil
}

// GetRuns returns the recent run history for a job.
func (s *CronScheduler) GetRuns(ctx context.Context, jobID string) ([]CronRunState, error) {
	rows, err := s.store.ListCronRuns(ctx, jobID, pruneKeep)
	if err != nil {
		return nil, err
	}
	result := make([]CronRunState, len(rows))
	for i, r := range rows {
		result[i] = runRowToState(r)
	}
	return result, nil
}

// ---------- internal: timer loop ----------

func (s *CronScheduler) timerLoop(ctx context.Context) {
	for {
		nextAt := s.tick(ctx)

		delta := time.Until(time.UnixMilli(nextAt))
		if delta <= 0 {
			delta = 100 * time.Millisecond
		}
		if delta > maxTimerDuration {
			delta = maxTimerDuration
		}

		timer := time.NewTimer(delta)
		select {
		case <-timer.C:
			// Timer fired; loop back to tick.
		case <-s.triggerCh:
			timer.Stop()
			// Signal received; loop back to tick immediately.
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// tick dispatches all due jobs and returns the next-earliest fire time (ms).
func (s *CronScheduler) tick(ctx context.Context) int64 {
	now := time.Now().UnixMilli()
	var earliest int64

	var dueJobs []*storage.CronJobRow
	func() {
		s.trackedLock()
		defer s.trackedUnlock()
		for _, job := range s.jobs {
			if !job.Enabled {
				continue
			}
			// Check retry first.
			if job.RetryAttempt > 0 && job.RetryRunAt > 0 && job.RetryRunAt <= now {
				cp := *job
				dueJobs = append(dueJobs, &cp)
				continue
			}
			// Check normal schedule.
			if job.NextRunAt > 0 && job.NextRunAt <= now {
				cp := *job
				dueJobs = append(dueJobs, &cp)
				continue
			}
			// Track the next-earliest fire time.
			nextFire := job.NextRunAt
			if job.RetryAttempt > 0 && job.RetryRunAt > 0 {
				if nextFire == 0 || job.RetryRunAt < nextFire {
					nextFire = job.RetryRunAt
				}
			}
			if nextFire > 0 && (earliest == 0 || nextFire < earliest) {
				earliest = nextFire
			}
		}
	}()

	for _, job := range dueJobs {
		triggerKind := "scheduled"
		isRetry := job.RetryAttempt > 0 && job.RetryRunAt > 0 && job.RetryRunAt <= now
		if isRetry {
			// A retry runs like a scheduled run, but its record says it
			// stands in for the scheduled time that failed.
			triggerKind = cronTriggerRetry
		}
		// A schedule that comes due after the Mac slept through several of
		// its times runs once, for the newest time; the earlier ones are
		// recorded as missed. Computed before dispatch, which moves nothing
		// but last_run_at.
		missed := dueMissedTimes(job, time.UnixMilli(now), isRetry)
		if s.dispatchJob(ctx, job, triggerKind) {
			// The run just dispatched decides whether the schedule is
			// failing, so the missed times alone tell no one.
			s.recordMissed(ctx, job, missed, false)
		}
	}

	if earliest == 0 {
		// No pending jobs; wake up in maxTimerDuration.
		earliest = time.Now().Add(maxTimerDuration).UnixMilli()
	}
	return earliest
}

// ---------- internal: dispatch & execute ----------

// cronTriggerRetry is the trigger kind of a run that retries a failed
// scheduled run: it stands in for the same scheduled time.
const cronTriggerRetry = "retry"

// cronTriggerIsScheduled reports whether a run comes from the schedule (on
// time, caught up, or retried) rather than a press of Run now.
func cronTriggerIsScheduled(triggerKind string) bool {
	switch triggerKind {
	case "scheduled", "catchup", cronTriggerRetry:
		return true
	default:
		return false
	}
}

// dispatchJob is the ONLY entry point for launching a job execution goroutine.
func (s *CronScheduler) dispatchJob(ctx context.Context, job *storage.CronJobRow, triggerKind string) bool {
	if retiredTodayNewsJob(job) {
		return false
	}
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	var alreadyRunning bool
	func() {
		s.trackedLock()
		defer s.trackedUnlock()
		if _, exists := s.jobs[job.ID]; !exists || s.running[job.ID] {
			alreadyRunning = true
			return
		}
		s.running[job.ID] = true
		// For scheduled/catchup triggers, update last_run_at at dispatch time
		// (not completion time) to prevent drift from execution duration.
		if cronTriggerIsScheduled(triggerKind) {
			job.LastRunAt = time.Now().UnixMilli()
		}
	}()
	if alreadyRunning {
		return false
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.trackedLock()
			defer s.trackedUnlock()
			delete(s.running, job.ID)
		}()

		// Semaphore: cancelable acquisition.
		select {
		case s.semaphore <- struct{}{}:
			defer func() { <-s.semaphore }()
		case <-ctx.Done():
			return
		}

		s.executeJob(ctx, job, triggerKind)
	}()
	return true
}

// executeJob runs a single job execution cycle.
func (s *CronScheduler) executeJob(ctx context.Context, job *storage.CronJobRow, triggerKind string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[cron] PANIC in executeJob for job %s: %v", job.ID, r)
		}
	}()

	runID := uuid.New().String()
	startedAt := time.Now().UnixMilli()

	// Insert running record.
	run := storage.CronRunRow{
		ID:            runID,
		JobID:         job.ID,
		StartedAt:     startedAt,
		Status:        "running",
		TriggerKind:   triggerKind,
		ItemSortStart: -1, // sentinel: not yet set
	}
	if err := s.store.InsertCronRun(ctx, run); err != nil {
		log.Printf("[cron] failed to insert run record for job %s: %v", job.ID, err)
		return
	}
	s.emit("cron:run", runRowToState(run))
	s.emitRunLifecycle(CronRunLifecycleEvent{
		JobID:       job.ID,
		JobName:     job.Name,
		SurfaceKind: job.SurfaceKind,
		RunID:       runID,
		Status:      "running",
		TriggerKind: triggerKind,
		StartedAt:   startedAt,
	})

	// Mark job as running so the sidebar card shows an animation.
	_ = s.store.UpdateCronJob(ctx, job.ID, map[string]any{"last_run_status": "running"})
	func() {
		s.trackedLock()
		defer s.trackedUnlock()
		if cached, ok := s.jobs[job.ID]; ok {
			cached.LastRunStatus = "running"
			s.emit("cron:state", jobRowToState(cached))
		}
	}()

	// Execute with timeout.
	routeRecorder := &cronRunRouteRecorder{row: run, persist: func(row storage.CronRunRow) error {
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
		defer saveCancel()
		if err := s.store.UpdateCronRun(saveCtx, runID, cronRunRouteFields(row)); err != nil {
			return fmt.Errorf("persist run route %s: %w", runID, err)
		}
		s.emit("cron:run", runRowToState(row))
		return nil
	}}
	execCtx, execCancel := context.WithTimeout(withCronRunRoute(ctx, routeRecorder), executeTimeout)
	defer execCancel()

	var execResult *cronExecResult
	var execErr error

	switch job.TargetKind {
	// "acp_agent" is the retired name for the in-process agent runner; migration
	// v37 renames it to "agent", but it only runs on DBs below version 37, so
	// legacy jobs created before the rename can still carry it. Treat it as
	// "agent" here so those jobs execute instead of erroring out.
	case "agent", "acp_agent":
		if s.execAgent != nil {
			execResult, execErr = s.execAgent(execCtx, job)
		} else {
			execErr = fmt.Errorf("agent executor not configured")
		}
	default:
		execErr = fmt.Errorf("unknown target kind: %s", job.TargetKind)
	}

	var resultSummary string
	var filePaths []string
	var deliveryTarget *DeliveryTarget
	var workDir, sessionTempDir string
	var writtenFiles []string
	if execResult != nil {
		resultSummary = execResult.Summary
		run.SessionTag = execResult.SessionTag
		run.ItemSortStart = execResult.ItemSortStart
		filePaths = execResult.FilePaths
		deliveryTarget = execResult.DeliveryTarget
		workDir = execResult.WorkDir
		sessionTempDir = execResult.SessionTempDir
		writtenFiles = execResult.WrittenFiles
		log.Printf("[cron] run %s completed: sessionTag=%q itemSortStart=%d hasLogs=%v deliverables=%d deliveryTarget=%v",
			runID, run.SessionTag, run.ItemSortStart, run.SessionTag != "" && run.ItemSortStart >= 0, len(filePaths), deliveryTarget != nil)
	}

	route, blocked := routeRecorder.snapshot()
	var blockedErr *agent.BlockedRunError
	isBlockedError := errors.As(execErr, &blockedErr)
	blocked = blocked || isBlockedError || (execErr != nil && route.ActionsStarted > 0)
	if blockedErr != nil && blockedErr.Actions > route.ActionsStarted {
		route.ActionsStarted = blockedErr.Actions
	}
	if blocked && execErr == nil {
		execErr = &agent.BlockedRunError{
			Backend: agent.Backend(route.Backend),
			Actions: route.ActionsStarted,
			Cause:   errors.New("automatic retry is blocked"),
		}
	}
	applyCronRunRoute(&run, route)
	finishedAt := time.Now().UnixMilli()
	durationMs := finishedAt - startedAt

	// Update run record.
	runFields := map[string]any{
		"finished_at":     finishedAt,
		"duration_ms":     durationMs,
		"result_summary":  resultSummary,
		"session_tag":     run.SessionTag,
		"item_sort_start": run.ItemSortStart,
	}

	for key, value := range cronRunRouteFields(run) {
		runFields[key] = value
	}

	// Update parent job state.
	jobFields := map[string]any{}

	if cronTriggerIsScheduled(triggerKind) {
		jobFields["last_run_at"] = job.LastRunAt
	}

	if execErr != nil && !blocked && errors.Is(execErr, errAgentNotReady) {
		// Agent not ready: record as "skipped" without incrementing
		// consecutive_errors or triggering auto-disable.
		runFields["status"] = "skipped"
		runFields["error"] = execErr.Error()
		run.Status = "skipped"
		run.Error = execErr.Error()

		jobFields["last_run_status"] = "skipped"
		// Do NOT touch consecutive_errors / retry fields.
	} else if execErr != nil {
		runFields["status"] = "error"
		runFields["error"] = execErr.Error()
		run.Status = "error"
		run.Error = execErr.Error()

		jobFields["last_run_status"] = "error"
		jobFields["last_error"] = execErr.Error()
		jobFields["consecutive_errors"] = job.ConsecutiveErrors + 1

		if blocked {
			runFields["status"] = "blocked"
			run.Status = "blocked"
			jobFields["last_run_status"] = "blocked"
			// Never repeat an action whose assistant failed after starting it,
			// even when this schedule has an automatic retry policy.
			failedJob := *job
			failedJob.RetryPolicy = ""
			jobFields["retry_attempt"] = 0
			jobFields["retry_run_at"] = int64(0)
			s.handleFailure(ctx, &failedJob, jobFields)
		} else {
			s.handleFailure(ctx, job, jobFields)
		}
	} else {
		runFields["status"] = "ok"
		run.Status = "ok"

		jobFields["last_run_status"] = "ok"
		jobFields["last_error"] = ""
		jobFields["consecutive_errors"] = 0
		jobFields["retry_attempt"] = 0
		jobFields["retry_run_at"] = 0

		s.handleSuccess(ctx, job, jobFields)
	}

	run.FinishedAt = finishedAt
	run.DurationMs = durationMs
	run.ResultSummary = resultSummary

	// Completion and retry policy must commit together. A persisted blocked
	// run with an old retry clock would otherwise replay after a restart.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
	if err := s.store.FinalizeCronRun(finishCtx, runID, job.ID, runFields, jobFields); err != nil {
		log.Printf("[cron] failed to finalize run %s: %v", runID, err)
	}
	finishCancel()

	// Sync in-memory cache (scoped so defer guarantees unlock).
	func() {
		s.trackedLock()
		defer s.trackedUnlock()
		cached, ok := s.jobs[job.ID]
		if !ok {
			return
		}
		if v, ok := jobFields["last_run_status"]; ok {
			cached.LastRunStatus, _ = v.(string)
		}
		if v, ok := jobFields["last_error"]; ok {
			cached.LastError, _ = v.(string)
		}
		if v, ok := jobFields["consecutive_errors"]; ok {
			cached.ConsecutiveErrors, _ = v.(int)
		}
		if v, ok := jobFields["enabled"]; ok {
			cached.Enabled, _ = v.(bool)
		}
		if v, ok := jobFields["next_run_at"]; ok {
			cached.NextRunAt, _ = v.(int64)
		}
		if v, ok := jobFields["retry_attempt"]; ok {
			cached.RetryAttempt, _ = v.(int)
		}
		if v, ok := jobFields["retry_run_at"]; ok {
			cached.RetryRunAt, _ = v.(int64)
		}
		if v, ok := jobFields["last_run_at"]; ok {
			cached.LastRunAt, _ = v.(int64)
		}
		if v, ok := jobFields["delete_after_run"]; ok {
			cached.DeleteAfterRun, _ = v.(bool)
		}
		if v, ok := jobFields["notify_dm"]; ok {
			cached.NotifyDM, _ = v.(bool)
		}
		// Sync target_config if it was updated during execution
		// (e.g. session_tag persisted by executeAgentJob).
		if job.TargetConfig != cached.TargetConfig {
			cached.TargetConfig = job.TargetConfig
		}
	}()

	// Emit updated state.
	s.emitJobState(job.ID)
	runState := runRowToState(run)
	log.Printf("[cron] emitting cron:run event: id=%s status=%s hasLogs=%v", runState.ID, runState.Status, runState.HasLogs)
	s.emit("cron:run", runState)
	s.emitRunLifecycle(CronRunLifecycleEvent{
		JobID:          job.ID,
		JobName:        job.Name,
		SurfaceKind:    job.SurfaceKind,
		RunID:          runID,
		Status:         run.Status,
		TriggerKind:    triggerKind,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		DurationMs:     durationMs,
		ResultSummary:  resultSummary,
		Error:          run.Error,
		SessionTag:     run.SessionTag,
		ItemSortStart:  run.ItemSortStart,
		FilePaths:      filePaths,
		DeliveryTarget: deliveryTarget,
	})

	if s.telemetryCapture != nil {
		s.telemetryCapture("automation.completed", map[string]any{
			"job_id":      job.ID,
			"run_id":      runID,
			"status":      run.Status,
			"duration_ms": durationMs,
			"has_error":   execErr != nil,
		})
	}

	// Notify on completion. The app's own hidden runs (Daily News, the
	// search on the user's topics) say nothing here: their results land on
	// Today, and their one notice a day goes through Engagement. A playbook
	// sends its own result where the user chose (playbook_run.go).
	quiet := job.SurfaceKind == "system" || job.SurfaceKind == playbookSurfaceKind
	if execErr != nil {
		if !quiet {
			s.notify("Automation failed", fmt.Sprintf("%s: %s", job.Name, execErr.Error()))
		}
	} else {
		if !quiet {
			s.notify("Automation completed", job.Name)
		}

		// Create local chat session + optionally send integration DM. A
		// playbook's result goes only where the user chose for it.
		if s.notifyResult != nil && job.SurfaceKind != playbookSurfaceKind && strings.TrimSpace(resultSummary) != "" {
			go func(jobName, summary string, dur int64, rid string, sendDM bool, platform string, fps []string, dt *DeliveryTarget, wd, std string, written []string) {
				notifyJSON := s.notifyResult(jobName, summary, "ok", dur, sendDM, platform, fps, dt, wd, std, written)
				if notifyJSON != "" {
					if err := s.store.UpdateCronRun(ctx, rid, map[string]any{"notify_result": notifyJSON}); err != nil {
						log.Printf("[cron] failed to save notify_result for run %s: %v", rid, err)
					}
				}
			}(job.Name, resultSummary, durationMs, runID, job.NotifyDM, job.NotifyPlatform, filePaths, deliveryTarget, workDir, sessionTempDir, writtenFiles)
		}
	}

	// Prune old runs.
	if err := s.store.PruneCronRuns(ctx, job.ID, pruneKeep); err != nil {
		log.Printf("[cron] failed to prune runs for job %s: %v", job.ID, err)
	}

	s.signal()
}

// handleSuccess processes a successful job execution.
func (s *CronScheduler) handleSuccess(ctx context.Context, job *storage.CronJobRow, fields map[string]any) {
	switch job.ScheduleKind {
	case "at":
		// Terminal state: disable the job.
		fields["enabled"] = false
		fields["next_run_at"] = int64(0)
		fields["retry_attempt"] = 0
		fields["retry_run_at"] = int64(0)

		if job.DeleteAfterRun {
			// Schedule deletion after we finish updating.
			go func() {
				time.Sleep(500 * time.Millisecond)
				if err := s.RemoveJob(ctx, job.ID); err != nil {
					log.Printf("[cron] failed to delete job %s after run: %v", job.ID, err)
				}
			}()
		}
	case "every", "cron":
		nextRun := computeNextRun(job)
		fields["next_run_at"] = nextRun
	}
}

// handleFailure processes a failed job execution.
func (s *CronScheduler) handleFailure(ctx context.Context, job *storage.CronJobRow, fields map[string]any) {
	newConsecutive := job.ConsecutiveErrors + 1

	// Auto-disable after threshold. The app's own hidden schedules (Daily
	// News, the search on the user's topics) are never turned off behind the
	// user's back, and never say so in a pop-up: they keep their schedule,
	// and Today and Upcoming show that they fail. Only the user pauses them.
	if newConsecutive >= autoDisableThreshold && job.SurfaceKind != "system" {
		fields["enabled"] = false
		fields["retry_attempt"] = 0
		fields["retry_run_at"] = int64(0)
		s.notify("Automation auto-disabled",
			fmt.Sprintf("%s: %d consecutive errors", job.Name, newConsecutive))
		log.Printf("[cron] auto-disabled job %s after %d consecutive errors", job.ID, newConsecutive)

		if job.ScheduleKind == "at" {
			fields["next_run_at"] = int64(0)
		}
		return
	}

	// Check retry policy.
	rp := parseRetryPolicy(job.RetryPolicy)
	if rp.MaxAttempts > 0 && job.RetryAttempt < rp.MaxAttempts {
		attempt := job.RetryAttempt + 1
		backoff := computeBackoff(rp, attempt)
		retryAt := time.Now().UnixMilli() + backoff

		fields["retry_attempt"] = attempt
		fields["retry_run_at"] = retryAt
		log.Printf("[cron] job %s: scheduling retry %d/%d in %dms", job.ID, attempt, rp.MaxAttempts, backoff)
	} else if rp.MaxAttempts > 0 && job.RetryAttempt >= rp.MaxAttempts {
		// Retry exhausted.
		fields["retry_attempt"] = 0
		fields["retry_run_at"] = int64(0)

		if job.ScheduleKind == "at" {
			// Terminal state for at-jobs on retry exhaustion.
			fields["enabled"] = false
			fields["next_run_at"] = int64(0)
		}
	}

	// Advance next_run_at for recurring jobs regardless of retry state.
	// at-kind is one-shot: a failed run with no retry pending must terminate
	// the job, otherwise the scheduler re-dispatches the same stale at_ms
	// timestamp on every tick (next_run_at stays in the past forever).
	// Mirrors handleSuccess's at-kind disable path. Without this guard,
	// rate-limited handoffs (or any failing at-kind job without a retry
	// policy) loop until consecutive_errors hits autoDisableThreshold,
	// firing N toasts and burning N-1 wasted API calls.
	switch job.ScheduleKind {
	case "every", "cron":
		nextRun := computeNextRun(job)
		fields["next_run_at"] = nextRun
	case "at":
		retryPending := rp.MaxAttempts > 0 && job.RetryAttempt < rp.MaxAttempts
		if !retryPending {
			fields["enabled"] = false
			fields["next_run_at"] = int64(0)
		}
	}
}

// ---------- internal: startup ----------

func (s *CronScheduler) loadJobs(ctx context.Context) error {
	rows, err := s.store.ListCronJobs(ctx)
	if err != nil {
		return err
	}

	// Remove only the retired system job. Deleting it cascades to its runs;
	// associated chat sessions are independent and remain untouched.
	kept := rows[:0]
	for i := range rows {
		if retiredTodayNewsJob(&rows[i]) {
			if err := s.store.DeleteCronJob(ctx, rows[i].ID); err != nil {
				return fmt.Errorf("remove retired news job: %w", err)
			}
			continue
		}
		kept = append(kept, rows[i])
	}
	rows = kept

	s.trackedLock()
	defer s.trackedUnlock()

	for i := range rows {
		r := rows[i]
		// A job whose feature is off stays saved but is never loaded: no
		// timer, no catch-up run, not listed, not runnable.
		if !cronSurfaceOn(r.SurfaceKind) {
			continue
		}
		s.jobs[r.ID] = &r
	}
	return nil
}

// cronCoreSurfaceKinds are the scheduler's internal one-shot jobs that chat
// and Tasks rely on: open answer checks, handoffs and topic finds. They run
// whatever the feature switches say.
var cronCoreSurfaceKinds = map[string]bool{openAnswerSurfaceKind: true, "handoff": true, todayNewsSurfaceKind: true}

// cronSurfaceOn says whether jobs of a surface kind may load, be added or run:
// core jobs always, playbook schedules with the playbooks switch, and every
// other job (user automations, Today's suggested schedules, scheduled
// initiatives) with the automations switch.
func cronSurfaceOn(surfaceKind string) bool {
	if cronCoreSurfaceKinds[surfaceKind] {
		return true
	}
	switch surfaceKind {
	case playbookSurfaceKind:
		return featureOn(featurePlaybooks)
	default:
		return featureOn(featureAutomations)
	}
}

func retiredTodayNewsJob(job *storage.CronJobRow) bool {
	return job != nil && job.SurfaceKind == todayNewsSurfaceKind && job.Name == todayNewsJobName
}

func (s *CronScheduler) startupCatchup(ctx context.Context) {
	now := time.Now().UnixMilli()

	var overdue []*storage.CronJobRow
	func() {
		s.trackedLock()
		defer s.trackedUnlock()
		for _, job := range s.jobs {
			if job.Enabled && job.NextRunAt > 0 && job.NextRunAt < now {
				cp := *job
				overdue = append(overdue, &cp)
			}
		}
	}()

	if len(overdue) == 0 {
		return
	}

	// Sort by next_run_at ascending (oldest first).
	sort.Slice(overdue, func(i, j int) bool {
		return overdue[i].NextRunAt < overdue[j].NextRunAt
	})

	// Dispatch the first maxCatchupJobs with stagger. Each catch-up run
	// covers the newest scheduled time that passed; the earlier ones are
	// recorded as missed.
	dispatched := 0
	for i, job := range overdue {
		if i >= maxCatchupJobs {
			break
		}
		if i > 0 {
			time.Sleep(time.Duration(catchupStaggerMs) * time.Millisecond)
		}
		missed := missedOccurrences(job, time.Now(), true)
		if s.dispatchJob(ctx, job, "catchup") {
			dispatched++
			s.recordMissed(ctx, job, missed, false)
		}
	}

	// Advance remaining overdue jobs to next future fire time. None of their
	// passed times ran, so every one of them is recorded as missed.
	for i := maxCatchupJobs; i < len(overdue); i++ {
		job := overdue[i]
		s.recordMissed(ctx, job, missedOccurrences(job, time.Now(), false), true)
		nextRun := computeNextRun(job)

		if nextRun > 0 {
			fields := map[string]any{"next_run_at": nextRun}
			if err := s.store.UpdateCronJob(ctx, job.ID, fields); err != nil {
				log.Printf("[cron] failed to advance overdue job %s: %v", job.ID, err)
			}
			func() {
				s.trackedLock()
				defer s.trackedUnlock()
				if cached, ok := s.jobs[job.ID]; ok {
					cached.NextRunAt = nextRun
				}
			}()
		} else {
			// Terminal job (e.g. "at" schedule) with no future fire time:
			// disable it so the timer loop doesn't re-trigger it.
			fields := map[string]any{"enabled": false, "next_run_at": int64(0)}
			if err := s.store.UpdateCronJob(ctx, job.ID, fields); err != nil {
				log.Printf("[cron] failed to disable terminal overdue job %s: %v", job.ID, err)
			}
			func() {
				s.trackedLock()
				defer s.trackedUnlock()
				if cached, ok := s.jobs[job.ID]; ok {
					cached.Enabled = false
					cached.NextRunAt = 0
				}
			}()
			log.Printf("[cron] disabled overdue terminal job %s (no future fire time)", job.ID)
		}
	}

	if dispatched > 0 {
		log.Printf("[cron] startup catchup: dispatched %d overdue jobs, advanced %d", dispatched, max(0, len(overdue)-maxCatchupJobs))
	}
}

// ---------- internal: missed runs ----------

const (
	// cronRunMissed is the run status of a scheduled time that passed while
	// the Mac was asleep or Isoform was closed, and that no run covered.
	cronRunMissed = "missed"
	// cronMissedWindow bounds how far back missed times are recorded; the
	// Upcoming page counts the last seven days.
	cronMissedWindow = 7 * 24 * time.Hour
	// cronMissedPerGap caps the missed rows one gap records, so a frequent
	// schedule that slept overnight does not push its real runs out of the
	// pruned history.
	cronMissedPerGap = 20
	// cronMissedMaxSteps bounds the walk over a cron schedule's times; a
	// week of a schedule that runs every minute is 10,080 steps.
	cronMissedMaxSteps = 20_000
)

// missedOccurrences lists the scheduled times of job, oldest first, from its
// due time (next_run_at) up to now, within the last cronMissedWindow and at
// most cronMissedPerGap of them. When covered is true the newest time is left
// out: the run being dispatched now stands for it. Manual jobs and jobs that
// are not due have no missed times.
func missedOccurrences(job *storage.CronJobRow, now time.Time, covered bool) []int64 {
	if job == nil || job.NextRunAt <= 0 || job.NextRunAt > now.UnixMilli() {
		return nil
	}
	nowMs := now.UnixMilli()
	floor := now.Add(-cronMissedWindow).UnixMilli()
	var times []int64
	switch job.ScheduleKind {
	case "at":
		times = []int64{job.NextRunAt}
	case "every":
		var cfg scheduleEvery
		if err := json.Unmarshal([]byte(job.ScheduleConfig), &cfg); err != nil || cfg.EveryMs <= 0 {
			return nil
		}
		// The times are NextRunAt + i*every for i up to last; only the
		// newest cronMissedPerGap+1 can survive the cap below, so they are
		// computed directly instead of walked.
		last := (nowMs - job.NextRunAt) / cfg.EveryMs
		first := last - cronMissedPerGap
		if first < 0 {
			first = 0
		}
		for i := first; i <= last; i++ {
			times = append(times, job.NextRunAt+i*cfg.EveryMs)
		}
	case "cron":
		var cfg scheduleCron
		if err := json.Unmarshal([]byte(job.ScheduleConfig), &cfg); err != nil {
			return nil
		}
		sched, err := parseCronExpr(cfg.Expr, cfg.TZ)
		if err != nil {
			return nil
		}
		t := time.UnixMilli(job.NextRunAt)
		if job.NextRunAt < floor {
			// The first scheduled time at or after the window start.
			t = sched.Next(time.UnixMilli(floor).Add(-time.Millisecond))
		}
		for steps := 0; !t.IsZero() && t.UnixMilli() <= nowMs && steps < cronMissedMaxSteps; steps++ {
			times = append(times, t.UnixMilli())
			t = sched.Next(t)
		}
	default:
		return nil
	}
	if covered && len(times) > 0 {
		times = times[:len(times)-1]
	}
	var out []int64
	for _, t := range times {
		if t >= floor {
			out = append(out, t)
		}
	}
	if len(out) > cronMissedPerGap {
		out = out[len(out)-cronMissedPerGap:]
	}
	return out
}

// dueMissedTimes is what a dispatch from the timer leaves missed. A normal
// run covers the newest passed time. A retry covers it too when that time
// came due after the retry was set, since the retry runs in its place the
// way a catch-up run does; earlier passed times are missed either way.
func dueMissedTimes(job *storage.CronJobRow, now time.Time, isRetry bool) []int64 {
	if !isRetry {
		return missedOccurrences(job, now, true)
	}
	missed := missedOccurrences(job, now, false)
	if len(missed) > 0 && missed[len(missed)-1] >= job.RetryRunAt {
		missed = missed[:len(missed)-1]
	}
	return missed
}

// recordMissed stores one missed run per scheduled time that passed without
// a run. A time at or before the newest one this job already recorded, or
// already stored as missed, is skipped: a job whose skipped run is retried
// every minute until the agent is ready comes due again and again with the
// same passed times, and those must not be recorded twice, even after the
// pruned history dropped the older rows.
//
// tell passes the job to the onMissed callback once rows were recorded. It
// is set only when no run covers the passed times (catch-up at launch that
// skips the job); a dispatched run decides on its own outcome instead, so a
// Mac that slept through two mornings and catches up fine is not reported.
func (s *CronScheduler) recordMissed(ctx context.Context, job *storage.CronJobRow, times []int64, tell bool) {
	if len(times) == 0 || s.store == nil {
		return
	}
	s.trackedLock()
	through := s.missedThrough[job.ID]
	s.trackedUnlock()
	var fresh []int64
	for _, at := range times {
		if at > through {
			fresh = append(fresh, at)
		}
	}
	if len(fresh) == 0 {
		return
	}
	existing, err := s.store.ListCronRuns(ctx, job.ID, pruneKeep)
	if err != nil {
		log.Printf("[cron] missed runs for job %s not recorded: %v", job.ID, err)
		return
	}
	seen := make(map[int64]bool, len(existing))
	for _, run := range existing {
		if run.Status == cronRunMissed {
			seen[run.StartedAt] = true
		}
	}
	// A time that passed while the job's runs were being skipped (the agent
	// was not ready) says so, instead of blaming a sleeping Mac.
	reason := ""
	if job.LastRunStatus == "skipped" {
		reason = errAgentNotReady.Error()
	}
	recorded := 0
	for _, at := range fresh {
		if seen[at] {
			continue
		}
		seen[at] = true
		row := storage.CronRunRow{
			ID:            uuid.New().String(),
			JobID:         job.ID,
			StartedAt:     at,
			FinishedAt:    at,
			Status:        cronRunMissed,
			TriggerKind:   "scheduled",
			Error:         reason,
			ItemSortStart: -1,
		}
		if err := s.store.InsertCronRun(ctx, row); err != nil {
			log.Printf("[cron] missed run for job %s not recorded: %v", job.ID, err)
			continue
		}
		recorded++
		s.emit("cron:run", runRowToState(row))
	}
	s.trackedLock()
	if newest := fresh[len(fresh)-1]; newest > s.missedThrough[job.ID] {
		s.missedThrough[job.ID] = newest
	}
	s.trackedUnlock()
	if recorded > 0 {
		log.Printf("[cron] job %s: %d scheduled time(s) passed without a run", job.ID, recorded)
		if tell && s.onMissed != nil {
			s.onMissed(job.ID)
		}
	}
}

// ---------- internal: schedule computation ----------

// computeFirstRun calculates the initial next_run_at for a new job.
func computeFirstRun(kind string, config json.RawMessage, now time.Time) (int64, error) {
	switch kind {
	case "at":
		var cfg scheduleAt
		if err := json.Unmarshal(config, &cfg); err != nil {
			return 0, fmt.Errorf("parse at config: %w", err)
		}
		return cfg.AtMs, nil

	case "every":
		var cfg scheduleEvery
		if err := json.Unmarshal(config, &cfg); err != nil {
			return 0, fmt.Errorf("parse every config: %w", err)
		}
		if cfg.EveryMs <= 0 {
			return 0, fmt.Errorf("every_ms must be positive")
		}
		return now.UnixMilli() + cfg.EveryMs, nil

	case "cron":
		var cfg scheduleCron
		if err := json.Unmarshal(config, &cfg); err != nil {
			return 0, fmt.Errorf("parse cron config: %w", err)
		}
		sched, err := parseCronExpr(cfg.Expr, cfg.TZ)
		if err != nil {
			return 0, err
		}
		next := sched.Next(now)
		return next.UnixMilli(), nil

	case "manual":
		// Manual jobs never auto-fire; next_run_at stays 0 so the scheduler skips them.
		return 0, nil

	default:
		return 0, fmt.Errorf("unknown schedule kind: %s", kind)
	}
}

// computeNextRun calculates the next fire time for an existing job.
// For "every" jobs, the cadence anchor is last_run_at.
// For "cron" jobs, uses robfig/cron/v3 parser.
// For "at" jobs, returns 0 (terminal).
func computeNextRun(job *storage.CronJobRow) int64 {
	now := time.Now()

	switch job.ScheduleKind {
	case "at":
		return 0

	case "every":
		var cfg scheduleEvery
		if err := json.Unmarshal([]byte(job.ScheduleConfig), &cfg); err != nil || cfg.EveryMs <= 0 {
			return 0
		}

		anchor := job.LastRunAt
		if anchor == 0 {
			return now.UnixMilli() + cfg.EveryMs
		}

		// Forward-roll to next future time point.
		nowMs := now.UnixMilli()
		if anchor+cfg.EveryMs > nowMs {
			return anchor + cfg.EveryMs
		}
		// next = anchor + every_ms * ceil((nowMs - anchor) / every_ms)
		elapsed := nowMs - anchor
		periods := int64(math.Ceil(float64(elapsed) / float64(cfg.EveryMs)))
		return anchor + cfg.EveryMs*periods

	case "cron":
		var cfg scheduleCron
		if err := json.Unmarshal([]byte(job.ScheduleConfig), &cfg); err != nil {
			return 0
		}
		sched, err := parseCronExpr(cfg.Expr, cfg.TZ)
		if err != nil {
			log.Printf("[cron] failed to parse cron expr for job %s: %v", job.ID, err)
			return 0
		}
		next := sched.Next(now)
		return next.UnixMilli()

	case "manual":
		return 0

	default:
		return 0
	}
}

// parseCronExpr parses a cron expression with optional timezone.
func parseCronExpr(expr, tz string) (cron.Schedule, error) {
	return storage.ParseCronExpr(expr, tz)
}

// ---------- internal: retry ----------

func parseRetryPolicy(raw string) retryPolicy {
	if raw == "" {
		return retryPolicy{}
	}
	var rp retryPolicy
	if err := json.Unmarshal([]byte(raw), &rp); err != nil {
		return retryPolicy{}
	}
	return rp
}

// computeBackoff returns the backoff delay in milliseconds for the given attempt.
func computeBackoff(rp retryPolicy, attempt int) int64 {
	if rp.BaseMs <= 0 {
		rp.BaseMs = 60_000 // default 1 minute
	}
	if rp.MaxBackoff <= 0 {
		rp.MaxBackoff = 3_600_000 // default 1 hour
	}

	delay := float64(rp.BaseMs) * math.Pow(2, float64(attempt-1))
	if delay > float64(rp.MaxBackoff) {
		delay = float64(rp.MaxBackoff)
	}
	return int64(delay)
}

// ---------- internal: conversions ----------

// parseJSONField parses a JSON string into a Go value for safe Wails serialization.
// Empty strings return nil (serializes to JSON null).
func parseJSONField(s string) any {
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil
	}
	return v
}

func jobRowToState(row *storage.CronJobRow) CronJobState {
	return CronJobState{
		ID:                  row.ID,
		Name:                row.Name,
		Description:         row.Description,
		Enabled:             row.Enabled,
		ScheduleKind:        row.ScheduleKind,
		ScheduleConfig:      parseJSONField(row.ScheduleConfig),
		ScheduleEvidence:    row.ScheduleEvidence,
		TargetKind:          row.TargetKind,
		TargetConfig:        parseJSONField(row.TargetConfig),
		DeleteAfterRun:      row.DeleteAfterRun,
		RetryPolicy:         parseJSONField(row.RetryPolicy),
		NextRunAt:           row.NextRunAt,
		LastRunAt:           row.LastRunAt,
		LastRunStatus:       row.LastRunStatus,
		LastError:           row.LastError,
		ConsecutiveErrors:   row.ConsecutiveErrors,
		RetryAttempt:        row.RetryAttempt,
		RetryRunAt:          row.RetryRunAt,
		SourceSessionID:     row.SourceSessionID,
		SourcePlatform:      row.SourcePlatform,
		NotifyDM:            row.NotifyDM,
		NotifyPlatform:      row.NotifyPlatform,
		SurfaceKind:         row.SurfaceKind,
		SortOrder:           row.SortOrder,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
		ScheduleDescription: formatScheduleDescription(row.ScheduleKind, row.ScheduleConfig),
	}
}

func runRowToState(row storage.CronRunRow) CronRunState {
	return CronRunState{
		ID:                row.ID,
		JobID:             row.JobID,
		StartedAt:         row.StartedAt,
		FinishedAt:        row.FinishedAt,
		Status:            row.Status,
		TriggerKind:       row.TriggerKind,
		Error:             row.Error,
		DurationMs:        row.DurationMs,
		ResultSummary:     row.ResultSummary,
		HasLogs:           row.SessionTag != "" && row.ItemSortStart >= 0,
		NotifyResult:      row.NotifyResult,
		CandidateBackends: cronRouteBackends(row.CandidateBackends),
		Backend:           row.Backend,
		FallbackFrom:      cronRouteBackends(row.FallbackFrom),
		BackendReason:     row.BackendReason,
		Model:             row.Model,
		ActionsStarted:    row.ActionsStarted,
	}
}

// emitJobState emits the current state for a job from the in-memory cache.
func (s *CronScheduler) emitJobState(id string) {
	var state CronJobState
	var found bool
	func() {
		s.trackedLock()
		defer s.trackedUnlock()
		job, ok := s.jobs[id]
		if !ok {
			return
		}
		found = true
		state = jobRowToState(job)
	}()
	if found {
		s.emit("cron:state", state)
	}
}

// formatScheduleDescription returns a human-readable schedule description.
func formatScheduleDescription(kind, config string) string {
	switch kind {
	case "at":
		var cfg scheduleAt
		if err := json.Unmarshal([]byte(config), &cfg); err != nil {
			return "once"
		}
		t := time.UnixMilli(cfg.AtMs)
		return fmt.Sprintf("once at %s", t.Format("2006-01-02 15:04"))

	case "every":
		var cfg scheduleEvery
		if err := json.Unmarshal([]byte(config), &cfg); err != nil {
			return "recurring"
		}
		return fmt.Sprintf("every %s", formatDuration(time.Duration(cfg.EveryMs)*time.Millisecond))

	case "cron":
		var cfg scheduleCron
		if err := json.Unmarshal([]byte(config), &cfg); err != nil {
			return "cron"
		}
		desc := cfg.Expr
		if cfg.TZ != "" {
			desc += " (" + cfg.TZ + ")"
		}
		return desc

	case "manual":
		return "manual only"

	default:
		return kind
	}
}

// formatDuration formats a duration into a human-readable string.
func formatDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case d >= time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	case d >= time.Minute:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", mins)
	default:
		secs := int(d.Seconds())
		if secs == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", secs)
	}
}

// ---------- AI name generation ----------

// GenerateAutomationName calls Claude Haiku via the agent runner to generate a
// short, descriptive name for an automation based on its description/prompt.
// Falls back to truncation if the runner is unavailable or the call fails.
func (a *App) GenerateAutomationName(description string) string {
	// The binding is an automations entry; the scheduler names its own core
	// jobs through generateAutomationName.
	if !featureOn(featureAutomations) {
		return ""
	}
	return a.generateAutomationName(description)
}

func (a *App) generateAutomationName(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return "Untitled Automation"
	}

	languageInstruction := responseLanguageInstruction(a.currentUILanguageName())
	prompt := languageInstruction + "\n\n" +
		"Generate a short, descriptive title (3-8 words) for an automation task. " +
		"Output ONLY the title, nothing else. No quotes, no punctuation at the end, no explanation.\n\n" +
		description

	name, err := a.runEphemeralHaiku(prompt)
	if err != nil {
		log.Printf("[cron] name-gen failed: %v", err)
		return truncateName(description)
	}

	name = strings.TrimSpace(name)
	name = strings.Trim(name, "\"'")
	if name == "" {
		return truncateName(description)
	}
	return name
}

func truncateName(s string) string {
	runes := []rune(s)
	if len(runes) > 40 {
		return string(runes[:40]) + "..."
	}
	return s
}
