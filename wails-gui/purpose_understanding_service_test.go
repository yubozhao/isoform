package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"wails-gui/storage"
	"yansu-agent/agent"
)

type purposeUnderstandingTestEvents struct {
	mu    sync.Mutex
	names []string
}

func (events *purposeUnderstandingTestEvents) emit(
	name string,
	_ ...interface{},
) {
	events.mu.Lock()
	events.names = append(events.names, name)
	events.mu.Unlock()
}

func (events *purposeUnderstandingTestEvents) count(name string) int {
	events.mu.Lock()
	defer events.mu.Unlock()
	count := 0
	for _, candidate := range events.names {
		if candidate == name {
			count++
		}
	}
	return count
}

func purposeUnderstandingTestService(
	t *testing.T,
	activity *ActivityStore,
) (*PurposeUnderstandingService, *App) {
	t.Helper()
	mailStore, err := storage.New(filepath.Join(t.TempDir(), "purpose-mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mailStore.Close() })
	app := &App{activity: &activityMonitorState{store: activity}, store: mailStore}
	service, err := NewPurposeUnderstandingService(app)
	if err != nil {
		t.Fatal(err)
	}
	return service, app
}

func purposeUnderstandingWriteSource(
	t *testing.T,
	home string,
	at time.Time,
	id string,
) {
	t.Helper()
	purposeWriteMemory(
		t,
		home,
		id,
		at,
		memorySource("manual"),
		"I will prepare the launch decision with a verified result.",
	)
}

func TestPurposeUnderstandingCachesUnchangedEvidenceAcrossCutoffs(
	t *testing.T,
) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	activity := newTestStore(t)
	now := time.Now().Add(-30 * time.Second)
	purposeUnderstandingWriteSource(
		t,
		home,
		now.Add(-time.Minute),
		"purpose-understanding-cache",
	)
	runner := &purposeExtractionFakeRunner{responses: []string{
		purposeExtractionOutputJSON(t),
	}}
	service, _ := purposeUnderstandingTestService(t, activity)
	service.now = func() time.Time { return now }
	service.runner = func() (agent.AgentRunner, string, error) {
		return runner, "gpt-5.6-sol", nil
	}

	service.runAndReport(
		context.Background(),
		[]string{PurposeUnderstandingTriggerStartup},
	)
	first := service.Snapshot().Last
	if first == nil || first.State != PurposeUnderstandingStateNoChange ||
		first.Cached || first.Fingerprint == "" || first.RunID == "" ||
		first.IncludedCount != 1 {
		t.Fatalf("unexpected first Purpose understanding cycle: %+v", first)
	}
	now = now.Add(time.Second)
	service.runAndReport(
		context.Background(),
		[]string{PurposeUnderstandingTriggerPeriodic},
	)
	second := service.Snapshot().Last
	if second == nil || second.State != PurposeUnderstandingStateNoChange ||
		!second.Cached || second.Fingerprint != first.Fingerprint ||
		second.RunID != first.RunID {
		t.Fatalf("unchanged evidence did not use the stable cache: first=%+v second=%+v", first, second)
	}
	if len(runner.inputs) != 1 || !runner.inputs[0].NoTools ||
		!runner.inputs[0].Ephemeral || !runner.inputs[0].SuppressTelemetry {
		t.Fatalf("Purpose understanding model boundary changed: %+v", runner.inputs)
	}
	if got := purposeCountRows(t, activity, "purpose_extraction_runs_v1"); got != 1 {
		t.Fatalf("unchanged evidence persisted %d extraction runs", got)
	}
}

func TestPurposeUnderstandingEmitsOnlyReconciledPurposeChanges(
	t *testing.T,
) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	activity := newTestStore(t)
	now := time.Now().Add(-30 * time.Second)
	purposeUnderstandingWriteSource(
		t,
		home,
		now.Add(-time.Minute),
		"purpose-understanding-change",
	)
	preflight, _, err := gatherPurposeExtractionInput(
		activity,
		PurposeExtractionRequest{
			IdempotencyKey: "purpose-understanding-change-preflight",
			CutoffAt:       now.UnixMilli(),
		},
	)
	if err != nil || len(preflight.Sources) != 1 {
		t.Fatalf("gather Purpose change source: input=%+v err=%v", preflight, err)
	}
	runner := &purposeExtractionFakeRunner{responses: []string{
		purposeExtractionOutputJSON(t, purposeExtractionHypothesis(
			"launch-decision",
			"launch decision",
			PurposeKindGoal,
			PurposeOperationSupport,
			"Prepare launch decision",
			"The launch decision has a verified result.",
			preflight.Sources[0].Ref,
		)),
	}}
	service, _ := purposeUnderstandingTestService(t, activity)
	events := &purposeUnderstandingTestEvents{}
	service.emit = events.emit
	service.now = func() time.Time { return now }
	service.runner = func() (agent.AgentRunner, string, error) {
		return runner, "gpt-5.6-sol", nil
	}
	before := purposeLedgerSideEffectCounts(t, activity)

	service.runAndReport(
		context.Background(),
		[]string{PurposeUnderstandingTriggerMemory},
	)
	last := service.Snapshot().Last
	if last == nil || last.State != PurposeUnderstandingStateChanged ||
		len(last.ChangedRootIDs) != 1 || len(last.ChangedRelationIDs) != 0 {
		t.Fatalf("unexpected changed Purpose cycle: %+v", last)
	}
	if events.count("purpose:updated") != 1 ||
		events.count("agency:updated") != 1 ||
		events.count("memory:updated") != 0 ||
		events.count("self_model:updated") != 0 {
		t.Fatalf("unexpected Purpose understanding event set: %+v", events.names)
	}
	if after := purposeLedgerSideEffectCounts(t, activity); after != before {
		t.Fatalf("Purpose understanding escaped containment: before=%s after=%s", before, after)
	}
	if got := purposeCountRows(t, activity, "purpose_open_loops_v1"); got != 1 {
		t.Fatalf("Purpose understanding reconciled %d roots", got)
	}
}

func TestPurposeUnderstandingDoesNotRetryFailedUnchangedEvidence(
	t *testing.T,
) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	activity := newTestStore(t)
	now := time.Now().Add(-30 * time.Second)
	purposeUnderstandingWriteSource(
		t,
		home,
		now.Add(-time.Minute),
		"purpose-understanding-failure",
	)
	runner := &purposeExtractionFakeRunner{responses: []string{"not-json"}}
	service, _ := purposeUnderstandingTestService(t, activity)
	service.now = func() time.Time { return now }
	service.runner = func() (agent.AgentRunner, string, error) {
		return runner, "gpt-5.6-sol", nil
	}

	service.runAndReport(
		context.Background(),
		[]string{PurposeUnderstandingTriggerMemory},
	)
	first := service.Snapshot().Last
	if first == nil || first.State != PurposeUnderstandingStateFailed ||
		first.Cached || first.RunID == "" {
		t.Fatalf("unexpected initial failed cycle: %+v", first)
	}
	now = now.Add(time.Second)
	service.runAndReport(
		context.Background(),
		[]string{PurposeUnderstandingTriggerPeriodic},
	)
	second := service.Snapshot().Last
	if second == nil || second.State != PurposeUnderstandingStateFailed ||
		!second.Cached || second.RunID != first.RunID || len(runner.inputs) != 1 {
		t.Fatalf("failed evidence was retried: first=%+v second=%+v calls=%d", first, second, len(runner.inputs))
	}
}

func TestPurposeUnderstandingRecoversMissingReceiptsWithoutRunner(
	t *testing.T,
) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	activity := newTestStore(t)
	now := time.Now().Add(-30 * time.Second)
	purposeUnderstandingWriteSource(
		t,
		home,
		now.Add(-time.Minute),
		"purpose-understanding-recovery",
	)
	request := PurposeExtractionRequest{
		IdempotencyKey: "purpose-understanding-unreconciled",
		CutoffAt:       now.UnixMilli(),
	}
	input, manifest, err := gatherPurposeExtractionInput(activity, request)
	if err != nil || len(input.Sources) != 1 {
		t.Fatalf("gather recovery input: input=%+v err=%v", input, err)
	}
	runner := &purposeExtractionFakeRunner{responses: []string{
		purposeExtractionOutputJSON(t, purposeExtractionHypothesis(
			"recover-purpose",
			"recover purpose",
			PurposeKindResponsibility,
			PurposeOperationSupport,
			"Recover Purpose understanding",
			"The interrupted Purpose hypothesis is reconciled.",
			input.Sources[0].Ref,
		)),
	}}
	execution, executionErr := runPurposeExtractionModel(
		context.Background(),
		runner,
		"gpt-5.6-sol",
		input,
		manifest,
	)
	if executionErr != nil {
		t.Fatal(executionErr)
	}
	run, hypotheses, err := purposeExtractionRecords(
		request,
		"gpt-5.6-sol",
		execution,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	detail, added, err := persistPurposeExtractionRun(
		activity.db,
		run,
		hypotheses,
	)
	if err != nil || !added || len(detail.Receipts) != 0 {
		t.Fatalf("persist unreconciled run: added=%v detail=%+v err=%v", added, detail, err)
	}

	service, _ := purposeUnderstandingTestService(t, activity)
	events := &purposeUnderstandingTestEvents{}
	service.emit = events.emit
	service.now = func() time.Time { return now.Add(time.Second) }
	service.runner = func() (agent.AgentRunner, string, error) {
		return nil, "", errors.New("runner intentionally unavailable")
	}
	service.runAndReport(
		context.Background(),
		[]string{PurposeUnderstandingTriggerStartup},
	)
	last := service.Snapshot().Last
	if last == nil || last.State != PurposeUnderstandingStateRecovered ||
		len(last.RecoveredRunIDs) != 1 || last.RecoveredRunIDs[0] != run.ID ||
		len(last.ChangedRootIDs) != 1 {
		t.Fatalf("unreconciled run was not recovered: %+v", last)
	}
	if events.count("purpose:updated") != 1 || events.count("agency:updated") != 1 {
		t.Fatalf("recovery did not emit exact product updates: %+v", events.names)
	}
	loaded := purposeNewRuntime(t, activity, nil)
	recovered, err := loaded.GetRun(run.ID)
	if err != nil || len(recovered.Receipts) != 1 {
		t.Fatalf("recovery receipt missing: detail=%+v err=%v", recovered, err)
	}
}

type purposeUnderstandingBlockingRunner struct {
	started  chan struct{}
	release  chan struct{}
	response string
	once     sync.Once
	mu       sync.Mutex
	calls    int
	active   int
	maximum  int
}

func newPurposeUnderstandingBlockingRunner(
	response string,
) *purposeUnderstandingBlockingRunner {
	return &purposeUnderstandingBlockingRunner{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		response: response,
	}
}

func (runner *purposeUnderstandingBlockingRunner) Init(context.Context) error { return nil }
func (runner *purposeUnderstandingBlockingRunner) IsReady() bool              { return true }
func (runner *purposeUnderstandingBlockingRunner) ExecuteTask(
	ctx context.Context,
	_ *agent.TaskInput,
	callback agent.StreamCallback,
) (string, error) {
	runner.mu.Lock()
	runner.calls++
	runner.active++
	if runner.active > runner.maximum {
		runner.maximum = runner.active
	}
	runner.mu.Unlock()
	runner.once.Do(func() { close(runner.started) })
	defer func() {
		runner.mu.Lock()
		runner.active--
		runner.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-runner.release:
	}
	if err := callback(&agent.StreamEvent{
		Type: "assistant", Content: runner.response,
	}); err != nil {
		return "", err
	}
	return "purpose-understanding-blocking", nil
}
func (runner *purposeUnderstandingBlockingRunner) CancelRun(string) error { return nil }
func (runner *purposeUnderstandingBlockingRunner) RespondToPermission(string, string) error {
	return nil
}
func (runner *purposeUnderstandingBlockingRunner) CancelPermission(string) {}
func (runner *purposeUnderstandingBlockingRunner) Shutdown() error         { return nil }

func (runner *purposeUnderstandingBlockingRunner) counts() (int, int) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.calls, runner.maximum
}

func waitForPurposeUnderstanding(
	t *testing.T,
	timeout time.Duration,
	condition func() bool,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for Purpose understanding state")
}

func TestPurposeUnderstandingSchedulerCoalescesAndRemainsSingleFlight(
	t *testing.T,
) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	activity := newTestStore(t)
	now := time.Now().Add(-30 * time.Second)
	purposeUnderstandingWriteSource(
		t,
		home,
		now.Add(-time.Minute),
		"purpose-understanding-single-flight",
	)
	runner := newPurposeUnderstandingBlockingRunner(
		purposeExtractionOutputJSON(t),
	)
	service, _ := purposeUnderstandingTestService(t, activity)
	service.now = func() time.Time { return now }
	service.runner = func() (agent.AgentRunner, string, error) {
		return runner, "gpt-5.6-sol", nil
	}
	service.initialDelay = time.Hour
	service.periodic = time.Hour
	service.debounce = 5 * time.Millisecond
	service.runTimeout = time.Second
	service.Start(context.Background())
	defer service.Stop()
	service.Trigger(PurposeUnderstandingTriggerMemory)
	service.Trigger(PurposeUnderstandingTriggerSelf)
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Purpose understanding model call did not start")
	}
	service.Trigger(PurposeUnderstandingTriggerMemory)
	close(runner.release)
	waitForPurposeUnderstanding(t, 2*time.Second, func() bool {
		last := service.Snapshot().Last
		return last != nil && last.Cached && !service.Snapshot().Running
	})
	calls, maximum := runner.counts()
	if calls != 1 || maximum != 1 {
		t.Fatalf("Purpose understanding was not single-flight: calls=%d max=%d", calls, maximum)
	}
}

func TestPurposeUnderstandingStartupFallbackDoesNotRepeatCompletedCycle(
	t *testing.T,
) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	activity := newTestStore(t)
	now := time.Now().Add(-30 * time.Second)
	purposeUnderstandingWriteSource(
		t,
		home,
		now.Add(-time.Minute),
		"purpose-understanding-single-startup",
	)
	runner := &purposeExtractionFakeRunner{responses: []string{
		purposeExtractionOutputJSON(t),
	}}
	service, _ := purposeUnderstandingTestService(t, activity)
	service.now = func() time.Time { return now }
	service.runner = func() (agent.AgentRunner, string, error) {
		return runner, "gpt-5.6-sol", nil
	}
	service.initialDelay = 20 * time.Millisecond
	service.periodic = time.Hour
	service.debounce = time.Millisecond
	service.runTimeout = time.Second
	service.Start(context.Background())
	defer service.Stop()
	service.Trigger(PurposeUnderstandingTriggerStartup)
	waitForPurposeUnderstanding(t, 2*time.Second, func() bool {
		last := service.Snapshot().Last
		return last != nil && !service.Snapshot().Running
	})
	first := service.Snapshot().Last
	if first == nil || first.Cached {
		t.Fatalf("first startup cycle was not a new evidence check: %+v", first)
	}
	time.Sleep(40 * time.Millisecond)
	last := service.Snapshot().Last
	if last == nil || last.StartedAt != first.StartedAt || last.Cached {
		t.Fatalf("startup fallback repeated a completed cycle: first=%+v last=%+v", first, last)
	}
}

func TestPurposeUnderstandingStopCancelsActiveModelCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	activity := newTestStore(t)
	now := time.Now().Add(-30 * time.Second)
	purposeUnderstandingWriteSource(
		t,
		home,
		now.Add(-time.Minute),
		"purpose-understanding-cancel",
	)
	runner := newPurposeUnderstandingBlockingRunner(
		purposeExtractionOutputJSON(t),
	)
	service, _ := purposeUnderstandingTestService(t, activity)
	service.now = func() time.Time { return now }
	service.runner = func() (agent.AgentRunner, string, error) {
		return runner, "gpt-5.6-sol", nil
	}
	service.initialDelay = time.Hour
	service.periodic = time.Hour
	service.debounce = time.Millisecond
	service.runTimeout = time.Minute
	service.Start(context.Background())
	service.Trigger(PurposeUnderstandingTriggerMemory)
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Purpose understanding model call did not start")
	}
	done := make(chan struct{})
	go func() {
		service.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Purpose understanding Stop did not cancel the model call")
	}
	last := service.Snapshot().Last
	if last == nil || last.State != PurposeUnderstandingStateCancelled {
		t.Fatalf("cancelled Purpose understanding state was not retained: %+v", last)
	}
	if got := purposeCountRows(t, activity, "purpose_extraction_runs_v1"); got != 0 {
		t.Fatalf("cancelled Purpose understanding persisted %d poisoned runs", got)
	}
}

// Purpose understanding is derived work: a belief or self-model update waits
// for the daily cycle instead of starting a model run (derived_cadence.go).
func TestMemoryAndSelfEventsWaitForDailyPurposeUnderstanding(t *testing.T) {
	activity := newTestStore(t)
	service, app := purposeUnderstandingTestService(t, activity)
	app.purposeUnderstanding = service
	app.emitBrowserEvent(PurposeUnderstandingTriggerMemory)
	app.emitBrowserEvent(PurposeUnderstandingTriggerSelf)
	if pending := service.Snapshot().PendingTriggers; len(pending) != 0 {
		t.Fatalf("context events started Purpose understanding: %v", pending)
	}
}

func TestPurposeUnderstandingUsesEffectiveConfiguredPrimaryModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := saveAIProviderConfig(AIProviderConfig{
		Provider:   "openai",
		APIKey:     "test-key",
		Model:      "gpt-5.6-sol",
		LightModel: "gpt-5.6-terra",
	}); err != nil {
		t.Fatal(err)
	}
	runner := &purposeExtractionFakeRunner{}
	app := &App{runner: runner}
	_, model, err := purposeUnderstandingRunner(app)
	if err != nil {
		t.Fatal(err)
	}
	if model != "gpt-5.6-sol" {
		t.Fatalf("Purpose understanding used %q, want configured primary model", model)
	}
}

func TestPurposeUnderstandingPinsDefaultModelAlias(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "")
	runner := &purposeExtractionFakeRunner{}
	app := &App{runner: runner}
	_, model, err := purposeUnderstandingRunner(app)
	if err != nil {
		t.Fatal(err)
	}
	if model != apiClaudeSonnetModel {
		t.Fatalf("Purpose understanding used %q, want %q", model, apiClaudeSonnetModel)
	}
}

func TestAlreadyReadyAgentStartsInitializedPurposeUnderstanding(t *testing.T) {
	activity := newTestStore(t)
	runner := &purposeExtractionFakeRunner{}
	app := &App{
		ctx:      context.Background(),
		activity: &activityMonitorState{store: activity},
		runner:   runner,
		ready:    true,
	}
	service, err := NewPurposeUnderstandingService(app)
	if err != nil {
		t.Fatal(err)
	}
	service.initialDelay = time.Hour
	service.periodic = time.Hour
	app.purposeUnderstanding = service
	status, err := app.StartAgent()
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	view := service.Snapshot()
	if !status.Ready || !view.Started ||
		len(view.PendingTriggers) != 1 ||
		view.PendingTriggers[0] != PurposeUnderstandingTriggerStartup {
		t.Fatalf("already-ready agent did not start Purpose understanding: status=%+v runtime=%+v", status, view)
	}
	service.consumePending()
	if _, err := app.StartAgent(); err != nil {
		t.Fatal(err)
	}
	if pending := service.Snapshot().PendingTriggers; len(pending) != 0 {
		t.Fatalf("idempotent StartAgent retriggered Purpose understanding: %v", pending)
	}
}
