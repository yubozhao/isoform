package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"yansu-agent/agent"
)

const (
	PurposeUnderstandingTriggerStartup     = "startup"
	PurposeUnderstandingTriggerMemory      = "memory:updated"
	PurposeUnderstandingTriggerSelf        = "self_model:updated"
	PurposeUnderstandingTriggerPeriodic    = "periodic"
	PurposeUnderstandingStateIdle          = "idle"
	PurposeUnderstandingStateCollecting    = "collecting"
	PurposeUnderstandingStateChecking      = "checking"
	PurposeUnderstandingStateNoSources     = "no_sources"
	PurposeUnderstandingStateNoChange      = "no_change"
	PurposeUnderstandingStateChanged       = "changed"
	PurposeUnderstandingStateRecovered     = "recovered"
	PurposeUnderstandingStateUnavailable   = "unavailable"
	PurposeUnderstandingStateFailed        = "failed"
	PurposeUnderstandingStateCancelled     = "cancelled"
	purposeUnderstandingInitialDelay       = 20 * time.Second
	purposeUnderstandingMeaningfulDebounce = 10 * time.Second
	purposeUnderstandingPeriodicInterval   = cadencePurposeUnderstanding
	purposeUnderstandingRunTimeout         = 5 * time.Minute
)

type PurposeUnderstandingCycleStatus struct {
	State                string   `json:"state"`
	TriggerKinds         []string `json:"triggerKinds"`
	Model                string   `json:"model,omitempty"`
	Fingerprint          string   `json:"fingerprint,omitempty"`
	RunID                string   `json:"runId,omitempty"`
	Cached               bool     `json:"cached"`
	ScannedCount         int      `json:"scannedCount"`
	IncludedCount        int      `json:"includedCount"`
	ExcludedCount        int      `json:"excludedCount"`
	CurrentPurposeCount  int      `json:"currentPurposeCount"`
	ChangedRootIDs       []string `json:"changedRootIds"`
	ChangedRelationIDs   []string `json:"changedRelationIds"`
	RecoveredRunIDs      []string `json:"recoveredRunIds"`
	StartedAt            int64    `json:"startedAt,omitempty"`
	CompletedAt          int64    `json:"completedAt,omitempty"`
	DurationMilliseconds int64    `json:"durationMilliseconds,omitempty"`
	Error                string   `json:"error,omitempty"`
}

type PurposeUnderstandingRuntimeSnapshot struct {
	Started         bool                             `json:"started"`
	Running         bool                             `json:"running"`
	PendingTriggers []string                         `json:"pendingTriggers"`
	Current         *PurposeUnderstandingCycleStatus `json:"current,omitempty"`
	Last            *PurposeUnderstandingCycleStatus `json:"last,omitempty"`
}

type purposeUnderstandingChanges struct {
	RootIDs     []string
	RelationIDs []string
}

func (changes purposeUnderstandingChanges) empty() bool {
	return len(changes.RootIDs) == 0 && len(changes.RelationIDs) == 0
}

func mergePurposeUnderstandingChanges(
	left purposeUnderstandingChanges,
	right purposeUnderstandingChanges,
) purposeUnderstandingChanges {
	return purposeUnderstandingChanges{
		RootIDs: uniqueSortedStrings(append(left.RootIDs, right.RootIDs...)),
		RelationIDs: uniqueSortedStrings(
			append(left.RelationIDs, right.RelationIDs...),
		),
	}
}

type PurposeUnderstandingService struct {
	app      *App
	activity *ActivityStore
	now      func() time.Time
	runner   func() (agent.AgentRunner, string, error)
	emit     func(string, ...interface{})

	initialDelay time.Duration
	debounce     time.Duration
	periodic     time.Duration
	runTimeout   time.Duration

	cycleMu  sync.Mutex
	mu       sync.Mutex
	stop     context.CancelFunc
	active   context.CancelFunc
	started  bool
	running  bool
	stopping bool
	pending  map[string]struct{}
	trigger  chan struct{}
	current  *PurposeUnderstandingCycleStatus
	last     *PurposeUnderstandingCycleStatus
	wg       sync.WaitGroup
}

func NewPurposeUnderstandingService(
	app *App,
) (*PurposeUnderstandingService, error) {
	if app == nil || app.activity == nil || app.activity.store == nil ||
		app.activity.store.db == nil {
		return nil, errors.New(
			"Purpose understanding requires the activity store",
		)
	}
	return &PurposeUnderstandingService{
		app: app, activity: app.activity.store, now: time.Now,
		runner: func() (agent.AgentRunner, string, error) {
			return purposeUnderstandingRunner(app)
		},
		emit:         app.emitBrowserEvent,
		initialDelay: purposeUnderstandingInitialDelay,
		debounce:     purposeUnderstandingMeaningfulDebounce,
		periodic:     purposeUnderstandingPeriodicInterval,
		runTimeout:   purposeUnderstandingRunTimeout,
		pending:      make(map[string]struct{}),
		trigger:      make(chan struct{}, 1),
	}, nil
}

func purposeUnderstandingRunner(
	app *App,
) (agent.AgentRunner, string, error) {
	if app == nil {
		return nil, "", errors.New("Purpose understanding app is unavailable")
	}
	app.mu.Lock()
	runner := app.runner
	app.mu.Unlock()
	if runner == nil || !runner.IsReady() {
		return nil, "", errors.New("Purpose understanding runner is not ready")
	}
	model := purposeUnderstandingPrimaryModel(app)
	if model == "" {
		return nil, "", errors.New("Purpose understanding model is unavailable")
	}
	return runner, model, nil
}

func purposeUnderstandingPrimaryModel(app *App) string {
	return effectivePrimaryTaskModel(app)
}

func purposeUnderstandingTriggerAllowed(trigger string) bool {
	switch strings.TrimSpace(trigger) {
	case PurposeUnderstandingTriggerStartup,
		PurposeUnderstandingTriggerMemory,
		PurposeUnderstandingTriggerSelf,
		PurposeUnderstandingTriggerPeriodic:
		return true
	default:
		return false
	}
}

func (service *PurposeUnderstandingService) Start(parent context.Context) bool {
	if service == nil {
		return false
	}
	if parent == nil {
		parent = context.Background()
	}
	service.mu.Lock()
	if service.started {
		service.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(parent)
	service.stop = cancel
	service.started = true
	service.stopping = false
	service.wg.Add(1)
	service.mu.Unlock()
	go func() {
		defer service.wg.Done()
		service.runScheduler(ctx)
	}()
	return true
}

func (service *PurposeUnderstandingService) Stop() {
	if service == nil {
		return
	}
	service.mu.Lock()
	stop := service.stop
	active := service.active
	service.stop = nil
	service.started = false
	service.stopping = true
	service.mu.Unlock()
	if active != nil {
		active()
	}
	if stop != nil {
		stop()
	}
	service.wg.Wait()
}

func (service *PurposeUnderstandingService) Trigger(trigger string) {
	if service == nil {
		return
	}
	trigger = strings.TrimSpace(trigger)
	if !purposeUnderstandingTriggerAllowed(trigger) {
		return
	}
	service.mu.Lock()
	if service.stopping {
		service.mu.Unlock()
		return
	}
	service.pending[trigger] = struct{}{}
	service.mu.Unlock()
	select {
	case service.trigger <- struct{}{}:
	default:
	}
}

func (service *PurposeUnderstandingService) consumePending() []string {
	service.mu.Lock()
	result := make([]string, 0, len(service.pending))
	for trigger := range service.pending {
		result = append(result, trigger)
	}
	clear(service.pending)
	service.mu.Unlock()
	sort.Strings(result)
	return result
}

func (service *PurposeUnderstandingService) needsStartupFallback() bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	return !service.running && service.current == nil && service.last == nil &&
		len(service.pending) == 0
}

func resetPurposeUnderstandingTimer(
	timer **time.Timer,
	timerC *<-chan time.Time,
	delay time.Duration,
) {
	if delay <= 0 {
		delay = time.Millisecond
	}
	if *timer == nil {
		*timer = time.NewTimer(delay)
	} else {
		if !(*timer).Stop() {
			select {
			case <-(*timer).C:
			default:
			}
		}
		(*timer).Reset(delay)
	}
	*timerC = (*timer).C
}

func (service *PurposeUnderstandingService) runScheduler(ctx context.Context) {
	initialDelay := service.initialDelay
	if initialDelay <= 0 {
		initialDelay = time.Millisecond
	}
	periodicInterval := service.periodic
	if periodicInterval <= 0 {
		periodicInterval = purposeUnderstandingPeriodicInterval
	}
	initial := time.NewTimer(initialDelay)
	periodic := time.NewTicker(periodicInterval)
	defer initial.Stop()
	defer periodic.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-initial.C:
			if service.needsStartupFallback() {
				service.Trigger(PurposeUnderstandingTriggerStartup)
			}
		case <-periodic.C:
			service.Trigger(PurposeUnderstandingTriggerPeriodic)
		case <-service.trigger:
			resetPurposeUnderstandingTimer(
				&debounce,
				&debounceC,
				service.debounce,
			)
		case <-debounceC:
			debounceC = nil
			triggers := service.consumePending()
			if len(triggers) > 0 {
				service.runAndReport(ctx, triggers)
			}
		}
	}
}

func copyPurposeUnderstandingCycleStatus(
	input *PurposeUnderstandingCycleStatus,
) *PurposeUnderstandingCycleStatus {
	if input == nil {
		return nil
	}
	result := *input
	result.TriggerKinds = append([]string{}, input.TriggerKinds...)
	result.ChangedRootIDs = append([]string{}, input.ChangedRootIDs...)
	result.ChangedRelationIDs = append([]string{}, input.ChangedRelationIDs...)
	result.RecoveredRunIDs = append([]string{}, input.RecoveredRunIDs...)
	return &result
}

func (service *PurposeUnderstandingService) Snapshot() PurposeUnderstandingRuntimeSnapshot {
	if service == nil {
		return PurposeUnderstandingRuntimeSnapshot{
			PendingTriggers: []string{},
		}
	}
	service.mu.Lock()
	pending := make([]string, 0, len(service.pending))
	for trigger := range service.pending {
		pending = append(pending, trigger)
	}
	sort.Strings(pending)
	result := PurposeUnderstandingRuntimeSnapshot{
		Started: service.started, Running: service.running,
		PendingTriggers: pending,
		Current:         copyPurposeUnderstandingCycleStatus(service.current),
		Last:            copyPurposeUnderstandingCycleStatus(service.last),
	}
	service.mu.Unlock()
	return result
}

func (service *PurposeUnderstandingService) publishCurrent(
	status PurposeUnderstandingCycleStatus,
) {
	service.mu.Lock()
	service.running = status.CompletedAt == 0
	service.current = copyPurposeUnderstandingCycleStatus(&status)
	service.mu.Unlock()
	if service.emit != nil {
		service.emit("purpose-understanding:updated")
	}
}

func (service *PurposeUnderstandingService) finishCurrent(
	status PurposeUnderstandingCycleStatus,
) {
	service.mu.Lock()
	service.running = false
	service.current = nil
	service.last = copyPurposeUnderstandingCycleStatus(&status)
	service.mu.Unlock()
	if service.emit != nil {
		service.emit("purpose-understanding:updated")
	}
}

func (service *PurposeUnderstandingService) runAndReport(
	parent context.Context,
	triggers []string,
) {
	service.cycleMu.Lock()
	defer service.cycleMu.Unlock()
	if err := parent.Err(); err != nil {
		return
	}
	started := service.now().UnixMilli()
	status := PurposeUnderstandingCycleStatus{
		State:          PurposeUnderstandingStateCollecting,
		TriggerKinds:   append([]string{}, triggers...),
		ChangedRootIDs: []string{}, ChangedRelationIDs: []string{},
		RecoveredRunIDs: []string{}, StartedAt: started,
	}
	service.publishCurrent(status)
	timeout := service.runTimeout
	if timeout <= 0 {
		timeout = purposeUnderstandingRunTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	service.mu.Lock()
	if service.stopping {
		service.mu.Unlock()
		cancel()
		status.State = PurposeUnderstandingStateCancelled
		status.CompletedAt = service.now().UnixMilli()
		if status.CompletedAt < status.StartedAt {
			status.CompletedAt = status.StartedAt
		}
		status.DurationMilliseconds = status.CompletedAt - status.StartedAt
		service.finishCurrent(status)
		return
	}
	service.active = cancel
	service.mu.Unlock()

	status = service.executeCycle(ctx, status)
	cancel()
	service.mu.Lock()
	service.active = nil
	service.mu.Unlock()
	status.CompletedAt = service.now().UnixMilli()
	if status.CompletedAt < status.StartedAt {
		status.CompletedAt = status.StartedAt
	}
	status.DurationMilliseconds = status.CompletedAt - status.StartedAt
	service.finishCurrent(status)
	if status.State == PurposeUnderstandingStateFailed ||
		status.State == PurposeUnderstandingStateUnavailable {
		log.Printf(
			"[purpose-understanding] %s after %dms: %s",
			status.State,
			status.DurationMilliseconds,
			status.Error,
		)
	}
}

func purposeUnderstandingPendingRunIDs(db *sql.DB) ([]string, error) {
	if db == nil {
		return nil, errors.New("Purpose understanding storage is unavailable")
	}
	rows, err := db.Query(`
		SELECT run.id
		FROM purpose_extraction_runs_v1 run
		WHERE run.status = ?
			AND EXISTS (
				SELECT 1
				FROM purpose_extraction_hypotheses_v1 hypothesis
				WHERE hypothesis.run_id = run.id
					AND NOT EXISTS (
						SELECT 1
						FROM purpose_reconciliation_receipts_v1 receipt
						WHERE receipt.hypothesis_id = hypothesis.id
					)
			)
		ORDER BY run.created_at, run.id`,
		PurposeExtractionStatusSucceeded,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func purposeUnderstandingChangesFromReceipts(
	before map[string]struct{},
	after PurposeExtractionRunDetail,
) purposeUnderstandingChanges {
	changes := purposeUnderstandingChanges{RootIDs: []string{}, RelationIDs: []string{}}
	for _, receipt := range after.Receipts {
		if _, existed := before[receipt.ID]; existed {
			continue
		}
		switch receipt.Disposition {
		case PurposeDispositionCreated, PurposeDispositionReinforced,
			PurposeDispositionRevised, PurposeDispositionWaiting,
			PurposeDispositionResolved, PurposeDispositionContradicted,
			PurposeDispositionDismissed:
			if receipt.OpenLoopID != "" {
				changes.RootIDs = append(changes.RootIDs, receipt.OpenLoopID)
			}
		case PurposeDispositionDeduplicated:
			if receipt.RelationID != "" && receipt.OpenLoopID != "" {
				changes.RootIDs = append(changes.RootIDs, receipt.OpenLoopID)
			}
		}
		if receipt.RelationID != "" {
			changes.RelationIDs = append(changes.RelationIDs, receipt.RelationID)
		}
	}
	changes.RootIDs = uniqueSortedStrings(changes.RootIDs)
	changes.RelationIDs = uniqueSortedStrings(changes.RelationIDs)
	return changes
}

func purposeUnderstandingReceiptSet(
	detail PurposeExtractionRunDetail,
) map[string]struct{} {
	result := make(map[string]struct{}, len(detail.Receipts))
	for _, receipt := range detail.Receipts {
		result[receipt.ID] = struct{}{}
	}
	return result
}

func (service *PurposeUnderstandingService) recoverPending(
	runtime *PurposeExtractionRuntime,
) ([]string, purposeUnderstandingChanges, error) {
	ids, err := purposeUnderstandingPendingRunIDs(service.activity.db)
	if err != nil {
		return nil, purposeUnderstandingChanges{}, err
	}
	recovered := []string{}
	changes := purposeUnderstandingChanges{RootIDs: []string{}, RelationIDs: []string{}}
	for _, id := range ids {
		before, err := runtime.GetRun(id)
		if err != nil {
			return nil, purposeUnderstandingChanges{}, err
		}
		after, err := runtime.Reconcile(id)
		var drift purposeFrozenSourceDriftError
		if errors.As(err, &drift) {
			// Its evidence changed after the model read it. Settle it as
			// rejected so one stale run cannot fail every later cycle.
			log.Printf("[purpose-understanding] settled run %s: %v", id, err)
			after, err = runtime.SettleDrifted(id, err)
		}
		if err != nil {
			return nil, purposeUnderstandingChanges{}, err
		}
		if len(after.Receipts) > len(before.Receipts) {
			recovered = append(recovered, id)
			changes = mergePurposeUnderstandingChanges(
				changes,
				purposeUnderstandingChangesFromReceipts(
					purposeUnderstandingReceiptSet(before),
					after,
				),
			)
		}
	}
	return recovered, changes, nil
}

func (service *PurposeUnderstandingService) emitPurposeChanges(
	changes purposeUnderstandingChanges,
) {
	if service.emit == nil || changes.empty() {
		return
	}
	for _, rootID := range changes.RootIDs {
		service.emit("purpose:updated", rootID)
	}
	service.emit("agency:updated")
}

func (service *PurposeUnderstandingService) executeCycle(
	ctx context.Context,
	status PurposeUnderstandingCycleStatus,
) PurposeUnderstandingCycleStatus {
	runtime, err := NewPurposeExtractionRuntime(service.activity, nil, "")
	if err != nil {
		status.State = PurposeUnderstandingStateFailed
		status.Error = agencyBoundedText(err.Error(), 900)
		return status
	}
	mailSubjects, err := purposeExtractionMailSubjects(service.app)
	if err != nil {
		status.State = PurposeUnderstandingStateFailed
		status.Error = "Purpose mail privacy context is unavailable"
		return status
	}
	runtime.mailSubjects = mailSubjects
	recovered, recoveredChanges, err := service.recoverPending(runtime)
	if err != nil {
		status.State = PurposeUnderstandingStateFailed
		status.Error = agencyBoundedText(err.Error(), 900)
		return status
	}
	status.RecoveredRunIDs = recovered
	status.ChangedRootIDs = append([]string{}, recoveredChanges.RootIDs...)
	status.ChangedRelationIDs = append([]string{}, recoveredChanges.RelationIDs...)
	service.emitPurposeChanges(recoveredChanges)

	runner, model, err := service.runner()
	if err != nil {
		if len(recovered) > 0 {
			status.State = PurposeUnderstandingStateRecovered
			return status
		}
		status.State = PurposeUnderstandingStateUnavailable
		status.Error = agencyBoundedText(err.Error(), 900)
		return status
	}
	status.Model = model
	runtime, err = NewPurposeExtractionRuntime(service.activity, runner, model)
	if err != nil {
		status.State = PurposeUnderstandingStateFailed
		status.Error = agencyBoundedText(err.Error(), 900)
		return status
	}
	runtime.mailSubjects = mailSubjects
	cutoffAt := service.now().UnixMilli()
	preflight := PurposeExtractionRequest{
		IdempotencyKey: "purpose-understanding-preflight",
		CutoffAt:       cutoffAt,
	}
	input, manifest, err := gatherPurposeExtractionInput(
		service.activity,
		preflight,
	)
	if err != nil {
		status.State = PurposeUnderstandingStateFailed
		status.Error = agencyBoundedText(err.Error(), 900)
		return status
	}
	input.MailPrivacy = newPurposeExtractionMailPrivacy(mailSubjects)
	status.ScannedCount = manifest.ScannedCount
	status.IncludedCount = manifest.IncludedCount
	status.ExcludedCount = manifest.ExcludedCount
	status.CurrentPurposeCount = manifest.CurrentPurposeCount
	fingerprint, err := purposeUnderstandingEvidenceFingerprint(
		input,
		manifest,
		model,
	)
	if err != nil {
		status.State = PurposeUnderstandingStateFailed
		status.Error = agencyBoundedText(err.Error(), 900)
		return status
	}
	status.Fingerprint = fingerprint
	status.State = PurposeUnderstandingStateChecking
	service.publishCurrent(status)
	idempotencyKey, err := purposeUnderstandingIdempotencyKey(fingerprint)
	if err != nil {
		status.State = PurposeUnderstandingStateFailed
		status.Error = agencyBoundedText(err.Error(), 900)
		return status
	}
	if existing, loadErr := loadPurposeExtractionRunByIdempotency(
		service.activity.db,
		idempotencyKey,
	); loadErr == nil {
		status.Cached = true
		status.RunID = existing.Run.ID
		switch existing.Run.Status {
		case PurposeExtractionStatusNoSources:
			status.State = PurposeUnderstandingStateNoSources
		case PurposeExtractionStatusFailed:
			status.State = PurposeUnderstandingStateFailed
			status.Error = existing.Run.Error
		default:
			if len(recovered) > 0 {
				status.State = PurposeUnderstandingStateRecovered
			} else {
				status.State = PurposeUnderstandingStateNoChange
			}
		}
		return status
	} else if !errors.Is(loadErr, sql.ErrNoRows) {
		status.State = PurposeUnderstandingStateFailed
		status.Error = agencyBoundedText(loadErr.Error(), 900)
		return status
	}

	request := PurposeExtractionRequest{
		IdempotencyKey: idempotencyKey,
		CutoffAt:       cutoffAt,
	}
	result, runErr := runtime.runFrozen(ctx, request, input, manifest)
	if result.Detail.Run.ID != "" {
		status.RunID = result.Detail.Run.ID
	}
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status.State = PurposeUnderstandingStateCancelled
		} else {
			status.State = PurposeUnderstandingStateFailed
		}
		status.Error = agencyBoundedText(runErr.Error(), 900)
		return status
	}
	if result.Detail.Run.Status == PurposeExtractionStatusNoSources {
		status.State = PurposeUnderstandingStateNoSources
		return status
	}
	changes := purposeUnderstandingChangesFromReceipts(
		map[string]struct{}{},
		result.Detail,
	)
	status.ChangedRootIDs = uniqueSortedStrings(
		append(status.ChangedRootIDs, changes.RootIDs...),
	)
	status.ChangedRelationIDs = uniqueSortedStrings(
		append(status.ChangedRelationIDs, changes.RelationIDs...),
	)
	service.emitPurposeChanges(changes)
	if len(status.ChangedRootIDs) > 0 || len(status.ChangedRelationIDs) > 0 {
		status.State = PurposeUnderstandingStateChanged
	} else if len(recovered) > 0 {
		status.State = PurposeUnderstandingStateRecovered
	} else {
		status.State = PurposeUnderstandingStateNoChange
	}
	return status
}

func (a *App) purposeUnderstandingService() *PurposeUnderstandingService {
	if a == nil {
		return nil
	}
	a.purposeUnderstandingMu.RLock()
	service := a.purposeUnderstanding
	a.purposeUnderstandingMu.RUnlock()
	return service
}

func (a *App) triggerPurposeUnderstanding(trigger string) {
	if service := a.purposeUnderstandingService(); service != nil {
		service.Trigger(trigger)
	}
}

func (a *App) initPurposeUnderstanding() {
	if a == nil {
		return
	}
	if a.purposeUnderstandingService() != nil {
		return
	}
	service, err := NewPurposeUnderstandingService(a)
	if err != nil {
		log.Printf("[purpose-understanding] startup skipped: %v", err)
		return
	}
	a.purposeUnderstandingMu.Lock()
	if a.purposeUnderstanding != nil {
		a.purposeUnderstandingMu.Unlock()
		return
	}
	a.purposeUnderstanding = service
	a.purposeUnderstandingMu.Unlock()
	log.Printf("[purpose-understanding] ambient scheduler initialized")
}

func (a *App) startPurposeUnderstanding() bool {
	if a == nil {
		return false
	}
	a.initPurposeUnderstanding()
	service := a.purposeUnderstandingService()
	if service == nil {
		return false
	}
	started := service.Start(a.ctx)
	if started {
		log.Printf("[purpose-understanding] ambient scheduler started")
	}
	return started
}

func (a *App) stopPurposeUnderstanding() {
	if a == nil {
		return
	}
	a.purposeUnderstandingMu.RLock()
	service := a.purposeUnderstanding
	a.purposeUnderstandingMu.RUnlock()
	if service != nil {
		service.Stop()
	}
}

func purposeUnderstandingStatusSummary(
	status PurposeUnderstandingCycleStatus,
) string {
	return fmt.Sprintf(
		"%s: %d included sources, %d current Purpose roots",
		status.State,
		status.IncludedCount,
		status.CurrentPurposeCount,
	)
}
