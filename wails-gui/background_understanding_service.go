package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/platform/activitymon"
)

const (
	backgroundSchedulerInitialDelay  = 2 * time.Minute
	backgroundSchedulerPollInterval  = cadenceCheck
	backgroundRunTimeout             = 8 * time.Minute
	backgroundQuietWindow            = 2 * time.Minute
	backgroundFallbackHintTTL        = 30 * time.Minute
	backgroundShadowFallbackLookback = 2 * time.Hour

	backgroundSchedulingPolicyVersion = "background-understanding-scheduling-v2-source-fair"
)

type BackgroundUnderstandingSourceView struct {
	Source            string `json:"source"`
	Label             string `json:"label"`
	Available         bool   `json:"available"`
	Connected         bool   `json:"connected"`
	Allowed           bool   `json:"allowed"`
	GrantID           string `json:"grantId,omitempty"`
	ScopeLabel        string `json:"scopeLabel,omitempty"`
	SourceQuery       string `json:"sourceQuery,omitempty"`
	LookbackDays      int    `json:"lookbackDays,omitempty"`
	MaxItemsPerRun    int    `json:"maxItemsPerRun,omitempty"`
	LastRunAt         string `json:"lastRunAt,omitempty"`
	LastRunState      string `json:"lastRunState,omitempty"`
	LastRunSummary    string `json:"lastRunSummary,omitempty"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

type BackgroundUnderstandingRunView struct {
	ID                   string `json:"id"`
	MissionKind          string `json:"missionKind"`
	Source               string `json:"source"`
	State                string `json:"state"`
	TriggerKind          string `json:"triggerKind"`
	TriggerReason        string `json:"triggerReason"`
	StartedAt            string `json:"startedAt"`
	UpdatedAt            string `json:"updatedAt"`
	Summary              string `json:"summary"`
	SourceItemCount      int    `json:"sourceItemCount"`
	MaterializationCount int    `json:"materializationCount"`
}

type BackgroundUnderstandingSnapshotView struct {
	Enabled                 bool                                `json:"enabled"`
	Status                  string                              `json:"status"`
	StatusReason            string                              `json:"statusReason"`
	CurrentRunID            string                              `json:"currentRunId,omitempty"`
	SchedulingPolicyVersion string                              `json:"schedulingPolicyVersion"`
	DailyRunLimit           int                                 `json:"dailyRunLimit"`
	TodayRunCount           int                                 `json:"todayRunCount"`
	TodayAttemptCount       int                                 `json:"todayAttemptCount"`
	ReservedRunCount        int                                 `json:"reservedRunCount"`
	ReservedSource          string                              `json:"reservedSource,omitempty"`
	MinIntervalMinutes      int                                 `json:"minIntervalMinutes"`
	Sources                 []BackgroundUnderstandingSourceView `json:"sources"`
	Runs                    []BackgroundUnderstandingRunView    `json:"runs"`
}

type backgroundUnderstandingDailyUsage struct {
	Attempts          int
	Consumed          int
	Reserved          int
	LocalRunLimit     int
	RunsByMissionKind map[string]int
}

type BackgroundUnderstandingService struct {
	app              *App
	store            *backgroundUnderstandingStore
	gmail            *backgroundGmailReader
	now              func() time.Time
	screenLocked     func() bool
	computerUseReady func() bool

	mu             sync.Mutex
	stop           context.CancelFunc
	runCancel      context.CancelFunc
	running        bool
	stopping       bool
	currentRunID   string
	currentGrantID string
	fallbackReason string
	fallbackAt     time.Time
	fallbackSignal chan struct{}
	wg             sync.WaitGroup
}

func NewBackgroundUnderstandingService(
	app *App,
) (*BackgroundUnderstandingService, error) {
	if app == nil || app.activity == nil || app.activity.store == nil {
		return nil, errors.New(
			"background understanding requires the activity store",
		)
	}
	store, err := newBackgroundUnderstandingStore(app.activity.store.db)
	if err != nil {
		return nil, err
	}
	return &BackgroundUnderstandingService{
		app:          app,
		store:        store,
		now:          time.Now,
		screenLocked: activitymon.IsScreenLocked,
		computerUseReady: func() bool {
			// Another Isoform reader (the web calendar peek) may hold the screen.
			return app.cuaDriver != nil && activitymon.ComputerUsePermitted() && sharedComputerUseLease.Holder() == ""
		},
		gmail: newBackgroundGmailReader(
			func() (backgroundComputerUseCaller, error) {
				if app.cuaDriver == nil {
					return nil, errors.New("Computer Use is unavailable")
				}
				return app.cuaDriver, nil
			},
			app.authorizeBackgroundGmailComputerUseAction,
		),
		fallbackSignal: make(chan struct{}, 1),
	}, nil
}

func (service *BackgroundUnderstandingService) Start(
	parent context.Context,
) {
	if service == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	service.mu.Lock()
	if service.stop != nil {
		service.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	service.stop = cancel
	service.stopping = false
	service.wg.Add(1)
	service.mu.Unlock()
	service.restoreRecentShadowFallback()
	go func() {
		defer service.wg.Done()
		service.runScheduler(ctx)
	}()
}

func (service *BackgroundUnderstandingService) Stop() {
	if service == nil {
		return
	}
	service.mu.Lock()
	stop := service.stop
	runCancel := service.runCancel
	service.stop = nil
	service.stopping = true
	service.mu.Unlock()
	if runCancel != nil {
		runCancel()
	}
	if stop != nil {
		stop()
	}
	service.wg.Wait()
}

func (service *BackgroundUnderstandingService) CancelSource(
	grantID string,
) {
	service.mu.Lock()
	cancel := service.runCancel
	shouldCancel := grantID != "" &&
		grantID == service.currentGrantID
	service.mu.Unlock()
	if shouldCancel && cancel != nil {
		cancel()
	}
}

func (service *BackgroundUnderstandingService) CancelCurrent() {
	if service == nil {
		return
	}
	service.mu.Lock()
	cancel := service.runCancel
	service.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (service *BackgroundUnderstandingService) runScheduler(
	ctx context.Context,
) {
	timer := time.NewTimer(backgroundSchedulerInitialDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-service.fallbackSignal:
		service.runAutomatic(ctx)
	case <-timer.C:
		service.runAutomatic(ctx)
	}
	ticker := time.NewTicker(backgroundSchedulerPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-service.fallbackSignal:
			service.runAutomatic(ctx)
		case <-ticker.C:
			service.runAutomatic(ctx)
		}
	}
}

func backgroundShadowFallbackReasonForEpisodes(
	episodes []TaskEpisode,
	now time.Time,
) string {
	var fallbackReason string
	for _, episode := range episodes {
		if episode.InferredStartAt <= 0 ||
			episode.InferredStartAt > now.UnixMilli() ||
			now.UnixMilli()-episode.InferredStartAt >
				backgroundShadowFallbackLookback.Milliseconds() {
			continue
		}
		switch episode.CurrentStatus {
		case TaskEpisodeStatusCandidate,
			TaskEpisodeStatusActive,
			TaskEpisodeStatusPaused:
		default:
			continue
		}
		ready, readinessErr := taskEpisodeProductionShadowReady(episode)
		if ready {
			return ""
		}
		reason := taskShadowBackgroundFallbackReason(readinessErr)
		if errors.Is(
			readinessErr,
			ErrTaskWorkflowInsufficientFrozenContext,
		) {
			fallbackReason = reason
			continue
		}
		if fallbackReason == "" {
			fallbackReason = reason
		}
	}
	return fallbackReason
}

func (service *BackgroundUnderstandingService) restoreRecentShadowFallback() {
	if service == nil || service.app == nil || service.app.activity == nil ||
		service.app.activity.store == nil {
		return
	}
	episodes, err := service.app.activity.store.ListTaskEpisodes("", 20)
	if err != nil {
		return
	}
	if reason := backgroundShadowFallbackReasonForEpisodes(
		episodes,
		service.now(),
	); reason != "" {
		service.OfferShadowFallback(reason)
	}
}

func (service *BackgroundUnderstandingService) OfferShadowFallback(
	reason string,
) {
	service.OfferCapacityFallback(reason)
}

// OfferCapacityFallback supplies a low-priority reason to the existing
// eligibility gate. It never forces a run or widens source permissions.
func (service *BackgroundUnderstandingService) OfferCapacityFallback(
	reason string,
) {
	if service == nil {
		return
	}
	reason = agencyBoundedText(strings.TrimSpace(reason), 240)
	if reason == "" {
		return
	}
	now := service.now()
	service.mu.Lock()
	if service.fallbackReason != "" && !service.fallbackAt.IsZero() &&
		now.Sub(service.fallbackAt) <= backgroundFallbackHintTTL &&
		backgroundShadowFallbackReasonPriority(service.fallbackReason) >
			backgroundShadowFallbackReasonPriority(reason) {
		signal := service.fallbackSignal
		service.mu.Unlock()
		if signal != nil {
			select {
			case signal <- struct{}{}:
			default:
			}
		}
		return
	}
	service.fallbackReason = reason
	service.fallbackAt = now
	signal := service.fallbackSignal
	service.mu.Unlock()
	if signal != nil {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

func backgroundShadowFallbackReasonPriority(reason string) int {
	switch {
	case strings.Contains(reason, "frozen onset context"):
		return 2
	case strings.Contains(reason, "outside the current Shadow rollout"):
		return 1
	default:
		return 0
	}
}

func (service *BackgroundUnderstandingService) ClearShadowFallback() {
	if service == nil {
		return
	}
	service.mu.Lock()
	service.fallbackReason = ""
	service.fallbackAt = time.Time{}
	service.mu.Unlock()
}

func (service *BackgroundUnderstandingService) shadowFallbackReason(
	now time.Time,
) string {
	if service == nil {
		return ""
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.fallbackReason == "" || service.fallbackAt.IsZero() ||
		now.Sub(service.fallbackAt) > backgroundFallbackHintTTL {
		service.fallbackReason = ""
		service.fallbackAt = time.Time{}
		return ""
	}
	return service.fallbackReason
}

func (service *BackgroundUnderstandingService) runAutomatic(
	parent context.Context,
) {
	now := service.now()
	eligible, reason := service.automaticEligibility(now)
	if !eligible {
		return
	}
	// Background learning is derived work: one automatic run a day
	// (cadence.go). A run the person starts does not pass through here.
	if !service.app.claimDerivedRun("background_learning", cadenceBackgroundLearning, now) {
		return
	}
	key := fmt.Sprintf(
		"automatic:%d",
		now.UnixMilli()/max(int64(1), cadenceBackgroundLearning.Milliseconds()),
	)
	ctx, cancel := context.WithTimeout(parent, backgroundRunTimeout)
	defer cancel()
	if _, err := service.RunOnce(
		ctx,
		BackgroundTriggerAutomatic,
		reason,
		key,
		"",
		now,
	); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[background-understanding] automatic run failed: %v", err)
	}
}

func backgroundDayStart(now time.Time) int64 {
	year, month, day := now.Date()
	return time.Date(
		year,
		month,
		day,
		0,
		0,
		0,
		0,
		now.Location(),
	).UnixMilli()
}

func (service *BackgroundUnderstandingService) gmailReservationActive(
	now time.Time,
) bool {
	if service == nil || service.store == nil {
		return false
	}
	grant, err := service.store.ActiveSourceGrant(
		BackgroundSourceGmail,
		now.UnixMilli(),
	)
	return err == nil && backgroundGrantAllowsOnlyGmailComputerUseRead(grant)
}

func (service *BackgroundUnderstandingService) dailyUsage(
	now time.Time,
	settings BackgroundUnderstandingSettings,
) (backgroundUnderstandingDailyUsage, error) {
	return service.store.DailyUsageSince(
		backgroundDayStart(now),
		settings.DailyRunLimit,
		service.gmailReservationActive(now),
	)
}

func (service *BackgroundUnderstandingService) reservedGmailReason() string {
	if service.screenLocked != nil && service.screenLocked() {
		return "One daily learning run is reserved for Gmail. Unlock this computer before Isoform can use it."
	}
	if service.computerUseReady == nil || !service.computerUseReady() {
		return "One daily learning run is reserved for Gmail. Turn on Computer Use before Isoform can use the signed-in session."
	}
	return "One daily learning run is reserved for the allowed Gmail source."
}

func (service *BackgroundUnderstandingService) automaticEligibility(
	now time.Time,
) (bool, string) {
	if service == nil || service.store == nil {
		return false, "Background learning storage is unavailable."
	}
	if service.app != nil && service.app.IsClosedEyesModeEnabled() {
		return false, "Privacy pause stops background learning."
	}
	settings, err := service.store.Settings()
	if err != nil || !settings.Enabled {
		return false, "Background learning is paused."
	}
	service.mu.Lock()
	running := service.running
	service.mu.Unlock()
	if running {
		return false, "A background learning mission is already running."
	}
	observerPaused, quiet := service.primaryObservationAvailability(now)
	locked := service.screenLocked != nil && service.screenLocked()
	if !observerPaused && !locked && !quiet {
		return false, "Primary observation is available."
	}
	usage, err := service.dailyUsage(now, settings)
	if err != nil || usage.Consumed >= settings.DailyRunLimit {
		return false, "The daily background learning limit is reached."
	}
	mission, _, _, _, err := service.selectMission("", now)
	if err != nil {
		return false, "No bounded background source is ready."
	}
	if mission == BackgroundMissionRetainedActivity &&
		usage.RunsByMissionKind[BackgroundMissionRetainedActivity] >=
			usage.LocalRunLimit {
		if usage.Reserved > 0 {
			return false, service.reservedGmailReason()
		}
		return false, "The daily background learning limit is reached."
	}
	latest, err := service.store.LatestRunForMission(mission)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, "Background learning history is unavailable."
	}
	if err == nil && now.UnixMilli()-latest.UpdatedAt <
		int64(settings.MinIntervalMinutes)*time.Minute.Milliseconds() {
		return false, "The bounded background interval has not elapsed."
	}
	if locked {
		return true, "The desktop is locked; primary observation cannot progress."
	}
	if quiet {
		if fallbackReason := service.shadowFallbackReason(now); fallbackReason != "" {
			return true, fallbackReason + " Using the quiet window for bounded learning."
		}
		return true, "The user is quiet; use the bounded window for background learning."
	}
	return true, "Observation is paused; use retained time for bounded learning."
}

func (service *BackgroundUnderstandingService) primaryObservationAvailability(
	now time.Time,
) (bool, bool) {
	if service == nil || service.app == nil || service.app.activity == nil {
		return true, false
	}
	activity := service.app.activity
	if activity.monitor == nil || !activity.monitor.IsRunning() {
		return true, false
	}
	activity.mu.Lock()
	lastActivityAt := activity.lastUserActivityTime
	inMeeting := activity.inMeeting
	activity.mu.Unlock()
	if inMeeting {
		return false, false
	}
	if lastActivityAt <= 0 && activity.store != nil &&
		activity.store.db != nil {
		_ = activity.store.db.QueryRow(`
			SELECT COALESCE(MAX(timestamp), 0)
			FROM activity_events
			WHERE kind != ?`,
			string(activitymon.EventSpeech),
		).Scan(&lastActivityAt)
	}
	if lastActivityAt <= 0 {
		return false, false
	}
	quietFor := now.UnixMilli() - lastActivityAt
	return false, quietFor >= backgroundQuietWindow.Milliseconds()
}

func (service *BackgroundUnderstandingService) beginRun(
	parent context.Context,
) (context.Context, context.CancelFunc, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.stopping {
		return nil, nil, errors.New(
			"background understanding service is stopping",
		)
	}
	if service.running {
		return nil, nil, errors.New(
			"a background understanding mission is already running",
		)
	}
	ctx, cancel := context.WithCancel(parent)
	service.running = true
	service.runCancel = cancel
	service.wg.Add(1)
	return ctx, cancel, nil
}

func (service *BackgroundUnderstandingService) endRun() {
	service.mu.Lock()
	service.running = false
	service.runCancel = nil
	service.currentRunID = ""
	service.currentGrantID = ""
	service.mu.Unlock()
	service.wg.Done()
}

func (service *BackgroundUnderstandingService) selectMission(
	preferredSource string,
	now time.Time,
) (string, string, *BackgroundSourceGrant, map[string]any, error) {
	preferredSource = strings.TrimSpace(preferredSource)
	if preferredSource != "" &&
		preferredSource != BackgroundSourceLocalActivity &&
		preferredSource != BackgroundSourceGmail {
		return "", "", nil, nil, fmt.Errorf(
			"unsupported preferred background source %q",
			preferredSource,
		)
	}
	gmailGrant, grantErr := service.store.ActiveSourceGrant(
		BackgroundSourceGmail,
		now.UnixMilli(),
	)
	grantReady := grantErr == nil &&
		backgroundGrantAllowsOnlyGmailComputerUseRead(gmailGrant)
	computerUseReady := service.computerUseReady != nil &&
		service.computerUseReady()
	locked := service.screenLocked != nil && service.screenLocked()
	gmailReady := grantReady && computerUseReady && !locked
	if preferredSource == BackgroundSourceGmail && !gmailReady {
		if errors.Is(grantErr, sql.ErrNoRows) || !grantReady {
			return "", "", nil, nil, errors.New(
				"Gmail learning requires an active Computer Use source grant",
			)
		}
		if locked {
			return "", "", nil, nil, errors.New(
				"unlock this computer before Gmail learning can use the browser",
			)
		}
		return "", "", nil, nil, errors.New(
			"enable Computer Use before Gmail learning can inspect the signed-in browser session",
		)
	}
	if preferredSource == BackgroundSourceGmail ||
		preferredSource == "" && gmailReady &&
			service.missionIsOldest(BackgroundMissionGmail) {
		scope := map[string]any{
			"grantId":           gmailGrant.ID,
			"source":            gmailGrant.Source,
			"scopeLabel":        gmailGrant.ScopeLabel,
			"sourceQuery":       gmailGrant.SourceQuery,
			"lookbackDays":      gmailGrant.LookbackDays,
			"maxItemsPerRun":    gmailGrant.MaxItemsPerRun,
			"allowedOperations": gmailGrant.AllowedOperations,
			"accessMode":        ComputerUseAppAccessMode,
			"transport":         backgroundGmailTransport,
			"oauthFallback":     false,
			"appAccessPolicy":   ComputerUseAppAccessPolicyVersion,
		}
		return BackgroundMissionGmail,
			BackgroundSourceGmail,
			&gmailGrant,
			scope,
			nil
	}
	return BackgroundMissionRetainedActivity,
		BackgroundSourceLocalActivity,
		nil,
		map[string]any{
			"source":        BackgroundSourceLocalActivity,
			"retainedOnly":  true,
			"policyVersion": WorkPatternProfilePolicyVersion,
		},
		nil
}

func (service *BackgroundUnderstandingService) missionIsOldest(
	mission string,
) bool {
	runs, err := service.store.ListRuns(backgroundMaximumRunList)
	if err != nil {
		return true
	}
	last := map[string]int64{
		BackgroundMissionGmail:            0,
		BackgroundMissionRetainedActivity: 0,
	}
	for _, run := range runs {
		if _, ok := last[run.MissionKind]; ok &&
			run.UpdatedAt > last[run.MissionKind] {
			last[run.MissionKind] = run.UpdatedAt
		}
	}
	return last[mission] <= last[BackgroundMissionRetainedActivity]
}

func (service *BackgroundUnderstandingService) RunOnce(
	parent context.Context,
	triggerKind, triggerReason, idempotencyKey, preferredSource string,
	now time.Time,
) (BackgroundUnderstandingRunDetail, error) {
	if service == nil || service.store == nil {
		return BackgroundUnderstandingRunDetail{}, errors.New(
			"background understanding service is unavailable",
		)
	}
	settings, err := service.store.Settings()
	if err != nil {
		return BackgroundUnderstandingRunDetail{}, err
	}
	if !settings.Enabled {
		return BackgroundUnderstandingRunDetail{}, errors.New(
			"background understanding is paused",
		)
	}
	if service.app != nil && service.app.IsClosedEyesModeEnabled() {
		return BackgroundUnderstandingRunDetail{}, errors.New(
			"background understanding is unavailable during privacy pause",
		)
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return BackgroundUnderstandingRunDetail{}, errors.New(
			"background understanding run requires an idempotency key",
		)
	}
	runCtx, cancel, err := service.beginRun(parent)
	if err != nil {
		return BackgroundUnderstandingRunDetail{}, err
	}
	defer cancel()
	defer service.endRun()

	mission, source, grant, scope, err := service.selectMission(
		preferredSource,
		now,
	)
	if err != nil {
		return BackgroundUnderstandingRunDetail{}, err
	}
	scopeJSON := backgroundJSON(scope, "{}")
	run, added, err := service.store.AddRun(
		BackgroundUnderstandingRun{
			IdempotencyKey: idempotencyKey,
			TriggerKind:    triggerKind,
			TriggerReason:  triggerReason,
			MissionKind:    mission,
			Source:         source,
			GrantID: func() string {
				if grant == nil {
					return ""
				}
				return grant.ID
			}(),
			SourceScopeJSON: scopeJSON,
			StartedAt:       now.UnixMilli(),
		},
		map[string]any{
			"missionKind":             mission,
			"source":                  source,
			"scopeHash":               agencyPayloadHash([]byte(scopeJSON)),
			"schedulingPolicyVersion": backgroundSchedulingPolicyVersion,
		},
	)
	if err != nil {
		return BackgroundUnderstandingRunDetail{}, err
	}
	if !added {
		return service.store.RunDetail(run.ID)
	}
	service.mu.Lock()
	service.currentRunID = run.ID
	if grant != nil {
		service.currentGrantID = grant.ID
	}
	service.mu.Unlock()
	service.app.emitBrowserEvent("agency:updated")
	if _, err := service.store.AppendRunEvent(
		run.ID,
		BackgroundRunRunning,
		map[string]any{
			"missionKind": mission,
			"source":      source,
		},
		service.now().UnixMilli(),
	); err != nil {
		return BackgroundUnderstandingRunDetail{}, err
	}

	switch mission {
	case BackgroundMissionGmail:
		err = service.runGmailMission(runCtx, run, *grant, now)
	default:
		err = service.runRetainedActivityMission(runCtx, run, now)
	}
	if err != nil {
		state := BackgroundRunFailed
		if errors.Is(err, context.Canceled) {
			state = BackgroundRunCancelled
		}
		failureDetails := map[string]any{
			"error": agencyBoundedText(err.Error(), 900),
		}
		var detailed interface {
			BackgroundRunFailureDetails() map[string]any
		}
		if errors.As(err, &detailed) {
			for key, value := range detailed.BackgroundRunFailureDetails() {
				failureDetails[key] = value
			}
		}
		_, appendErr := service.store.AppendRunEvent(
			run.ID,
			state,
			failureDetails,
			service.now().UnixMilli(),
		)
		service.app.emitBrowserEvent("agency:updated")
		if appendErr != nil {
			return BackgroundUnderstandingRunDetail{}, fmt.Errorf(
				"background mission failed: %v; append failure receipt: %w",
				err,
				appendErr,
			)
		}
		return service.store.RunDetail(run.ID)
	}
	service.app.emitBrowserEvent("agency:updated")
	return service.store.RunDetail(run.ID)
}

func (service *BackgroundUnderstandingService) runRetainedActivityMission(
	ctx context.Context,
	run BackgroundUnderstandingRun,
	now time.Time,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, added, err := service.app.activity.store.
		EnsureWorkPatternProfileSnapshot(
			now.UnixMilli(),
			service.now().UnixMilli(),
		)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	receiptHash := agencyPayloadHash([]byte(
		snapshot.SourceHash + "\x00" + snapshot.SHA256,
	))
	_, _, err = service.store.AddSourceItem(
		BackgroundUnderstandingSourceItem{
			RunID:           run.ID,
			Source:          BackgroundSourceLocalActivity,
			SourceItemKind:  "work_pattern_aggregate",
			ExternalIDHash:  snapshot.SourceHash,
			ContentHash:     snapshot.SHA256,
			AttributionKind: "aggregate",
			ObservedAt:      snapshot.SourceCutoffAt,
			MetadataJSON: backgroundJSON(
				map[string]any{
					"sampleCount":  snapshot.SampleCount,
					"sessionCount": snapshot.SessionCount,
					"status":       snapshot.Status,
				},
				"{}",
			),
			CreatedAt: service.now().UnixMilli(),
		},
	)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	action := BackgroundMaterializationExisting
	if added {
		action = BackgroundMaterializationCreated
	}
	if _, _, err := service.store.AddMaterialization(
		BackgroundUnderstandingMaterialization{
			RunID:             run.ID,
			ArtifactKind:      BackgroundArtifactWorkPatternProfile,
			ArtifactID:        snapshot.ID,
			Action:            action,
			Summary:           "Refreshed evidence-backed work patterns.",
			SourceReceiptHash: receiptHash,
			CreatedAt:         service.now().UnixMilli(),
		},
	); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = service.store.AppendRunEvent(
		run.ID,
		BackgroundRunCompleted,
		map[string]any{
			"summary":       "Refreshed evidence-backed work patterns from retained activity.",
			"sourceItems":   1,
			"materialized":  1,
			"profileStatus": snapshot.Status,
			"sampleCount":   snapshot.SampleCount,
			"sessionCount":  snapshot.SessionCount,
			"receiptHash":   receiptHash,
		},
		service.now().UnixMilli(),
	)
	return err
}

func (service *BackgroundUnderstandingService) runGmailMission(
	ctx context.Context,
	run BackgroundUnderstandingRun,
	grant BackgroundSourceGrant,
	now time.Time,
) error {
	if service.app.memoryGenerator == nil {
		return errors.New("Memory and Knowledge pipeline is unavailable")
	}
	batch, err := service.gmail.Read(ctx, run.ID, grant, "", now)
	if err != nil {
		return &backgroundGmailMissionError{err: err, batch: batch}
	}
	newItems := make([]backgroundGmailObservation, 0, len(batch.Items))
	for _, item := range batch.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen, err := service.store.SourceItemSeen(
			BackgroundSourceGmail,
			item.ExternalIDHash,
			item.ContentHash,
		)
		if err != nil {
			return err
		}
		if seen {
			continue
		}
		newItems = append(newItems, item)
		if _, _, err := service.store.AddSourceItem(
			BackgroundUnderstandingSourceItem{
				RunID:           run.ID,
				Source:          BackgroundSourceGmail,
				SourceItemKind:  "message_metadata",
				ExternalIDHash:  item.ExternalIDHash,
				ThreadIDHash:    item.ThreadIDHash,
				ContentHash:     item.ContentHash,
				AttributionKind: item.AttributionKind,
				ObservedAt:      item.ObservedAt,
				MetadataJSON:    item.MetadataJSON,
				CreatedAt:       service.now().UnixMilli(),
			},
		); err != nil {
			return err
		}
	}
	if len(newItems) == 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := service.store.AppendRunEvent(
			run.ID,
			BackgroundRunSkipped,
			map[string]any{
				"summary":       "No new Gmail metadata matched the granted scope.",
				"fetchedItems":  len(batch.Items),
				"newItems":      0,
				"receiptHash":   batch.ReceiptHash,
				"transport":     batch.Transport,
				"oauthFallback": false,
				"browser":       batch.BrowserName,
				"browserBundle": batch.BrowserBundle,
				"browserPid":    batch.BrowserPID,
				"windowId":      batch.WindowID,
				"snapshotId":    batch.SnapshotID,
				"snapshotHash":  batch.SnapshotHash,
				"axStatus":      batch.AXStatus,
				"cleanup":       batch.CleanupOutcome,
				"actions":       batch.Actions,
			},
			service.now().UnixMilli(),
		)
		return err
	}
	summary := backgroundGmailSummary(batch, newItems)
	sourceID := "background-gmail-" + batch.ReceiptHash[:24]
	outcome := service.app.memoryGenerator.runMemoryPipelineDetailed(
		ctx,
		memorySourceGmail,
		sourceID,
		summary,
		"",
	)
	if outcome.Result == memPipelineCanceled {
		return context.Canceled
	}
	if outcome.Result == memPipelineFailed {
		return errors.New("Memory and Knowledge pipeline failed")
	}
	for _, artifact := range outcome.Materializations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, _, err := service.store.AddMaterialization(
			BackgroundUnderstandingMaterialization{
				RunID:             run.ID,
				ArtifactKind:      artifact.ArtifactKind,
				ArtifactID:        artifact.ArtifactID,
				Action:            artifact.Action,
				Summary:           artifact.Title,
				SourceReceiptHash: batch.ReceiptHash,
				CreatedAt:         service.now().UnixMilli(),
			},
		); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = service.store.AppendRunEvent(
		run.ID,
		BackgroundRunCompleted,
		map[string]any{
			"summary": "Reviewed bounded Gmail metadata and passed supported " +
				"understanding through existing Memory and Knowledge gates.",
			"fetchedItems":     len(batch.Items),
			"newItems":         len(newItems),
			"materialized":     len(outcome.Materializations),
			"pipelineResult":   int(outcome.Result),
			"receiptHash":      batch.ReceiptHash,
			"payloadInspected": false,
			"attachmentsRead":  false,
			"transport":        batch.Transport,
			"oauthFallback":    false,
			"browser":          batch.BrowserName,
			"browserBundle":    batch.BrowserBundle,
			"browserPid":       batch.BrowserPID,
			"windowId":         batch.WindowID,
			"snapshotId":       batch.SnapshotID,
			"snapshotHash":     batch.SnapshotHash,
			"axStatus":         batch.AXStatus,
			"cleanup":          batch.CleanupOutcome,
			"actions":          batch.Actions,
		},
		service.now().UnixMilli(),
	)
	return err
}

func backgroundRunSummary(run BackgroundUnderstandingRun) string {
	var detail map[string]any
	if json.Unmarshal([]byte(run.DetailJSON), &detail) == nil {
		if value, ok := detail["summary"].(string); ok {
			return agencyBoundedText(value, 500)
		}
		if value, ok := detail["error"].(string); ok {
			return agencyBoundedText(value, 500)
		}
	}
	switch run.CurrentState {
	case BackgroundRunRunning:
		return "Inspecting the bounded source."
	case BackgroundRunSelected:
		return "Selected and waiting to start."
	default:
		return strings.ReplaceAll(run.CurrentState, "_", " ")
	}
}

func (service *BackgroundUnderstandingService) Snapshot() (BackgroundUnderstandingSnapshotView, error) {
	settings, err := service.store.Settings()
	if err != nil {
		return BackgroundUnderstandingSnapshotView{}, err
	}
	now := service.now()
	runs, err := service.store.ListRuns(12)
	if err != nil {
		return BackgroundUnderstandingSnapshotView{}, err
	}
	usage, err := service.dailyUsage(now, settings)
	if err != nil {
		return BackgroundUnderstandingSnapshotView{}, err
	}
	result := BackgroundUnderstandingSnapshotView{
		Enabled:                 settings.Enabled,
		Status:                  "waiting",
		StatusReason:            "Waiting for a quiet window behind primary work.",
		SchedulingPolicyVersion: backgroundSchedulingPolicyVersion,
		DailyRunLimit:           settings.DailyRunLimit,
		TodayRunCount:           usage.Consumed,
		TodayAttemptCount:       usage.Attempts,
		ReservedRunCount:        usage.Reserved,
		MinIntervalMinutes:      settings.MinIntervalMinutes,
		Sources:                 []BackgroundUnderstandingSourceView{},
		Runs:                    []BackgroundUnderstandingRunView{},
	}
	if usage.Reserved > 0 {
		result.ReservedSource = BackgroundSourceGmail
	}
	if fallbackReason := service.shadowFallbackReason(now); fallbackReason != "" {
		result.StatusReason = fallbackReason +
			" Background learning will use the next quiet window."
	}
	service.mu.Lock()
	running := service.running
	result.CurrentRunID = service.currentRunID
	service.mu.Unlock()
	switch {
	case !settings.Enabled:
		result.Status = "paused"
		result.StatusReason = "Background learning is paused."
	case service.app != nil && service.app.IsClosedEyesModeEnabled():
		result.Status = "paused"
		result.StatusReason = "Privacy pause stops background learning."
	case running:
		result.Status = "working"
		result.StatusReason = "A bounded learning mission is running."
	case usage.Consumed >= settings.DailyRunLimit:
		result.Status = "limit_reached"
		result.StatusReason = "Today's bounded learning limit is reached."
	case usage.Reserved > 0 &&
		usage.RunsByMissionKind[BackgroundMissionRetainedActivity] >=
			usage.LocalRunLimit &&
		(service.screenLocked != nil && service.screenLocked() ||
			service.computerUseReady == nil || !service.computerUseReady()):
		result.Status = "waiting_for_source"
		result.StatusReason = service.reservedGmailReason()
	case service.screenLocked != nil && service.screenLocked():
		result.Status = "ready"
		result.StatusReason = "Primary observation is unavailable; background learning can run."
	default:
		observerPaused, quiet := service.primaryObservationAvailability(now)
		if observerPaused {
			result.Status = "ready"
			result.StatusReason = "Observation is paused; retained time is available for learning."
		} else if quiet {
			result.Status = "ready"
			if fallbackReason := service.shadowFallbackReason(now); fallbackReason != "" {
				result.StatusReason = fallbackReason +
					" A quiet window is available for bounded learning."
			} else {
				result.StatusReason = "A quiet window is available for bounded learning."
			}
		}
	}
	for _, run := range runs {
		detail, detailErr := service.store.RunDetail(run.ID)
		sourceItems := 0
		materializations := 0
		if detailErr == nil {
			sourceItems = len(detail.SourceItems)
			materializations = len(detail.Materializations)
		}
		result.Runs = append(result.Runs, BackgroundUnderstandingRunView{
			ID:                   run.ID,
			MissionKind:          run.MissionKind,
			Source:               run.Source,
			State:                run.CurrentState,
			TriggerKind:          run.TriggerKind,
			TriggerReason:        run.TriggerReason,
			StartedAt:            agencyTimestamp(run.StartedAt),
			UpdatedAt:            agencyTimestamp(run.UpdatedAt),
			Summary:              backgroundRunSummary(run),
			SourceItemCount:      sourceItems,
			MaterializationCount: materializations,
		})
	}
	local := BackgroundUnderstandingSourceView{
		Source: BackgroundSourceLocalActivity,
		Label:  "Retained activity",
		Available: service.app != nil &&
			service.app.activity != nil &&
			service.app.activity.store != nil,
		Connected: true,
		Allowed:   true,
	}
	computerUseReady := service.computerUseReady != nil &&
		service.computerUseReady()
	locked := service.screenLocked != nil && service.screenLocked()
	gmail := BackgroundUnderstandingSourceView{
		Source:    BackgroundSourceGmail,
		Label:     "Gmail",
		Available: service.app != nil && service.app.cuaDriver != nil,
		Connected: computerUseReady && !locked,
	}
	grant, grantErr := service.store.ActiveSourceGrant(
		BackgroundSourceGmail,
		now.UnixMilli(),
	)
	if grantErr == nil && backgroundGrantAllowsOnlyGmailComputerUseRead(grant) {
		gmail.Allowed = true
		gmail.GrantID = grant.ID
		gmail.ScopeLabel = grant.ScopeLabel
		gmail.SourceQuery = grant.SourceQuery
		gmail.LookbackDays = grant.LookbackDays
		gmail.MaxItemsPerRun = grant.MaxItemsPerRun
	}
	if grantErr == nil && !backgroundGrantAllowsOnlyGmailComputerUseRead(grant) {
		gmail.UnavailableReason = "Allow Gmail again under the Computer Use policy."
	} else if locked {
		gmail.UnavailableReason = "Unlock this computer before Gmail can be read."
	} else if !gmail.Connected {
		gmail.UnavailableReason = "Enable Computer Use. Gmail uses your existing signed-in browser session; no Google connection is needed."
	} else if !gmail.Allowed {
		gmail.UnavailableReason = "Uses your signed-in Gmail session only after you allow a bounded read scope."
	}
	for _, run := range runs {
		var target *BackgroundUnderstandingSourceView
		switch run.Source {
		case BackgroundSourceLocalActivity:
			target = &local
		case BackgroundSourceGmail:
			target = &gmail
		}
		if target == nil || target.LastRunAt != "" {
			continue
		}
		target.LastRunAt = agencyTimestamp(run.UpdatedAt)
		target.LastRunState = run.CurrentState
		target.LastRunSummary = backgroundRunSummary(run)
	}
	result.Sources = append(result.Sources, local, gmail)
	sort.SliceStable(result.Sources, func(i, j int) bool {
		return result.Sources[i].Source < result.Sources[j].Source
	})
	return result, nil
}

func (a *App) backgroundUnderstandingService() (
	*BackgroundUnderstandingService,
	error,
) {
	if a == nil {
		return nil, errors.New("background understanding app is unavailable")
	}
	a.backgroundUnderstandingMu.RLock()
	service := a.backgroundUnderstanding
	a.backgroundUnderstandingMu.RUnlock()
	if service == nil {
		return nil, errors.New(
			"background understanding service is unavailable",
		)
	}
	return service, nil
}

func (a *App) cancelBackgroundUnderstandingForPrimaryActivity() {
	if a == nil {
		return
	}
	a.backgroundUnderstandingMu.RLock()
	service := a.backgroundUnderstanding
	a.backgroundUnderstandingMu.RUnlock()
	if service != nil {
		service.CancelCurrent()
	}
}

func (a *App) offerBackgroundUnderstandingForShadowFallback(reason string) {
	if a == nil {
		return
	}
	a.backgroundUnderstandingMu.RLock()
	service := a.backgroundUnderstanding
	a.backgroundUnderstandingMu.RUnlock()
	if service != nil {
		service.OfferShadowFallback(reason)
	}
}

func (a *App) clearBackgroundUnderstandingShadowFallback() {
	if a == nil {
		return
	}
	a.backgroundUnderstandingMu.RLock()
	service := a.backgroundUnderstanding
	a.backgroundUnderstandingMu.RUnlock()
	if service != nil {
		service.ClearShadowFallback()
		service.CancelCurrent()
	}
}

// backgroundUnderstandingNotRunningSnapshot is the read model while no
// coordinator runs. The app no longer starts one since the Desk was removed.
func backgroundUnderstandingNotRunningSnapshot() BackgroundUnderstandingSnapshotView {
	return BackgroundUnderstandingSnapshotView{
		Enabled:                 false,
		Status:                  "unavailable",
		StatusReason:            "Background learning is not running.",
		SchedulingPolicyVersion: backgroundSchedulingPolicyVersion,
		DailyRunLimit:           backgroundDefaultDailyRunLimit,
		MinIntervalMinutes:      backgroundDefaultMinIntervalMinutes,
		Sources:                 []BackgroundUnderstandingSourceView{},
		Runs:                    []BackgroundUnderstandingRunView{},
	}
}

func (a *App) GetBackgroundUnderstandingSnapshot() (
	BackgroundUnderstandingSnapshotView,
	error,
) {
	service, err := a.backgroundUnderstandingService()
	if err != nil {
		return backgroundUnderstandingNotRunningSnapshot(), nil
	}
	return service.Snapshot()
}

func (a *App) GetBackgroundUnderstandingRunDetail(
	runID string,
) (BackgroundUnderstandingRunDetail, error) {
	service, err := a.backgroundUnderstandingService()
	if err != nil {
		return BackgroundUnderstandingRunDetail{}, err
	}
	return service.store.RunDetail(runID)
}
