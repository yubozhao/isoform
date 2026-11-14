package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof" // gated behind YANSU_PPROF env var; see startup()
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"wails-gui/models"

	"wails-gui/internal/handoff"
	"wails-gui/internal/platform/cmdutil"
	"wails-gui/internal/platform/ocr"
	"wails-gui/internal/sop"
	"wails-gui/internal/sshconn"
	terminalsvc "wails-gui/internal/terminal"
	"wails-gui/storage"
	"yansu-agent/agent"
	agenttools "yansu-agent/agent/tools"
	"yansu-agent/runtoken"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sync/singleflight"
)

// DefaultWebBaseURL is the web dashboard URL injected at build time via
// -ldflags "-X main.DefaultWebBaseURL=https://dashboard.yansu.ai".
// Left empty so local dev builds default to staging (see resolveWebBaseURL).
var DefaultWebBaseURL string

// DefaultDesktopPostHogKey / DefaultDesktopPostHogHost are optional ingest
// credentials injected only by scripts/build-wails.sh. Both default to empty;
// an unconfigured binary sends nothing, regardless of its runtime environment.
var DefaultDesktopPostHogKey string
var DefaultDesktopPostHogHost string

const (
	cliAuthPath    = "/cli-auth"
	configFileName = "config.json"
	tokenFileName  = "token"
	envBaseURL     = "YANSU_URL"
)

// furnaceScanEnabled gates the per-platform incremental "Furnace" scanners
// (Slack/Feishu/Telegram/Discord). It is currently paused: the scanner Run loops
// and Trigger calls no-op while it is false, but all scanner logic is retained
// so the feature can be re-enabled by flipping this to true.
const furnaceScanEnabled = false

//go:embed assets/appicon.png
var embeddedAppIcon []byte

//go:embed assets/appicon.ico
var embeddedAppIconICO []byte

//go:embed assets/menubar-icon.png assets/menubar-icon@2x.png assets/menubar-icon@3x.png
var embeddedMenuBarIcons embed.FS

type App struct {
	// activityBatch is the schedule of the hourly activity batch
	// (activity_hourly_batch.go).
	activityBatch activityBatchClock

	toolDownloads    toolDownloadPlans
	launch           *launchStartup
	setupModelsOnce  sync.Once
	setupModels      *models.Manager
	setupModelsErr   error
	setupAgents      agentProbeCache
	setupPermissions permissionSequence

	// Fixture observers measure source collection and evidence reads; nil in production.
	todayChatCollectionObserver func()
	// todayChatSourcesMemo shares Today source collections across callers.
	todayChatSourcesMemo todayChatSourcesCoalescer
	// streamPages keeps the saved first page of each chat stream.
	streamPages                streamPageState
	peopleStatsCache           peopleStatsSnapshot
	memoryEvidenceReadObserver func()
	topicPageBeforePublish     func()
	topicPageAfterValidate     func()

	// Main-chat topic publication and bounded startup backfill.
	mainChatTopicBackfillRunning       atomic.Bool
	mainChatRowTopicsBeforePublish     func()
	mainChatRowTopicsBeforeReadPublish func()
	mainChatWaiting                    mainChatWaitingStateHolder

	// topicSource is injected by the RC 1 registry adapter; nil fails closed.
	topicSource TopicSource
	// peopleTopics is the shared-topics source for the person detail; nil uses
	// the registry's committed snapshot (people_topics_adapter.go). Tests may
	// inject a failing seam to prove topics read failures stay non-fatal.
	peopleTopics peopleWorkTopics
	// peopleTogetherProbe lets tests delay or fail the evidence read.
	peopleTogetherProbe func() error
	// peopleLineageProbe, when set, fires on each real principal lineage load
	// so tests can count lineage walks per read generation.
	peopleLineageProbe        func()
	ctx                       context.Context
	mu                        sync.Mutex
	pluginMu                  sync.Mutex
	runner                    agent.AgentRunner
	assistantProbes           *assistantProbeCache
	signOut                   signOutTracker
	signOutTimers             map[string]*time.Timer
	signOutTimersMu           sync.Mutex
	agentChoices              agentChoiceCache
	backgroundUsesChoice      atomic.Bool
	httpPort                  int
	ready                     bool
	readyCh                   chan struct{} // closed when ready==true; recreated on reset
	starting                  bool
	streams                   map[string]context.CancelFunc // runID → cancel
	topicHistoryTurns         map[string]*topicTurnBinding
	tagCancels                map[string]context.CancelFunc // tag → cancel (registered at goroutine start)
	runIDByTag                map[string]string             // tag → run id of the running turn (set on run_created)
	spotlightSubmitReceipts   sync.Map
	chatSendReceipts          sync.Map        // tag + trace ID -> admitted ordinary send
	activeTags                map[string]bool // tag → true while a prompt is running
	deletedChats              map[string]bool // tag → true once the chat is deleted; under mu, no turn ever starts there again
	deletingChats             map[string]int  // tag → count of in-progress deletes; under mu, no turn starts while > 0
	holdsMu                   sync.Mutex
	holds                     *holdCoordinator
	holdsCancel               context.CancelFunc
	holdsDone                 chan struct{}
	contactsSyncMu            sync.Mutex
	contactsSync              *contactsSyncScheduler
	contactsSyncStop          context.CancelFunc
	contactsSyncDone          chan struct{}
	store                     *storage.Store
	openAnswerAskSelectionMu  sync.Mutex
	openAnswerAskSelections   map[string]openAnswerAskSelection
	storageStartup            *storageStartupState
	termMgr                   *terminalsvc.Manager
	sshMgr                    *sshconn.Manager
	bridge                    *hostBridge                     // localhost-only bridge for host OS ops (CUA, spotlight)
	cuaDriver                 *CuaDriverDaemon                // bundled cua-driver daemon (nil when not supported / not located)
	activity                  *activityMonitorState           // live activity monitor + triager
	closedEyes                closedEyesModeState             // privacy mode: stop activity and audio capture while overlay is visible
	generateDailySummarySF    singleflight.Group              // dedups concurrent GenerateDailySummary calls per dateStr
	autoSummaryMu             sync.Mutex                      // guards the auto-summary maps below
	autoSummaryInFlight       map[string]bool                 // kind|date|language with a background summary generation running
	autoSummaryRetryAfter     map[string]time.Time            // kind|date blocked from auto generation until after a failure
	autoSummaryNightlyFor     string                          // local date the after-midnight summary pass last completed
	autoSummaryNightlyTries   map[string]int                  // kind|date generations the after-midnight pass started
	autoSummaryNow            func() time.Time                // test seam; nil → time.Now
	autoSummaryGenDaily       func(dateStr string) error      // test seam; nil → GenerateDailySummary
	autoSummaryGenWeekly      func(dateStr string) error      // test seam; nil → GenerateWeeklySummary
	generateTodayNewsSF       singleflight.Group              // dedups concurrent GenerateTodayNews calls per date+language
	shadowTaskEvaluationSF    singleflight.Group              // dedups task-mirror comparison when runtime and lifecycle finish together
	purposeUnderstandingMu    sync.RWMutex                    // guards ambient Purpose-understanding lifecycle and event reads
	purposeUnderstanding      *PurposeUnderstandingService    // source-backed current-Purpose scheduler
	backgroundUnderstandingMu sync.RWMutex                    // guards the low-priority learning coordinator
	backgroundUnderstanding   *BackgroundUnderstandingService // nil in the app: bounded background learning no longer starts since the Desk was removed
	personResearchInFlight    sync.Map                        // principalID → struct{} while a People research run is active (dedup)
	composioActivationSF      singleflight.Group              // dedups managed Composio MCP activation per app
	composioActivationErrs    map[string]string               // app → last activation error string (cleared on success); guarded by mu
	memoryStore               *MemoryStore                    // FTS5-indexed memory search (markdown files)
	memoryExternalReads       memoryExternalReadCache         // redacted model-facing memory, kept until memory changes
	interestsReads            interestsReadCache              // interest evidence read decisions, kept until privacy state changes
	memoryGenerator           *MemoryGenerator                // generative memory pipeline (triage → generate → save)
	memoryDream               *MemoryDreamService             // nightly memory consolidator: dedup, expire, similarity merge, Haiku-mediated synthesis
	aboutYouPortrait          *AboutYouPortraitService        // source-bound weekly owner portrait
	selfDream                 *SelfDreamService               // shadow-only canonical Self synthesis, evaluation, and gated revision compiler
	suggestionGenerator       *SuggestionGenerator            // empty-state composer chips (activity → LLM → cache)
	handoffStore              *handoff.Store                  // first-class file store for handoffs (~/.yansu-agent/handoff)
	handoffActionMu           sync.Mutex                      // serializes handoff start and stop transactions
	handoffRuns               map[string]*handoffRunState     // guarded by handoffActionMu
	knowHow                   knowHowState
	assistantHistory          assistantHistoryState
	workPermissions           mainChatWorkPermissions
	sopStore                  *sop.Store                      // first-class bundle store for SOPs (~/.yansu-agent/sops)
	playbookPlans             *sop.PlanStore                  // per-playbook schedule, channels and approved sites (sops/.plans.json)
	knowHowCandidates         *sop.Store                      // proposed know-how drafts (~/.yansu-agent/know-how-candidates), never listed under Playbooks
	playbookRoutineMu         sync.Mutex                      // serializes routine detector passes (playbook_routine.go)
	sopAuthorMu               sync.Mutex                      // serializes heavy SOP author runs (triage + manual never overlap)
	sopAuthorStatusMu         sync.Mutex                      // guards SOP author monitor state
	sopAuthorStatus           SOPAuthorStatus                 // devtools-visible SOP author progress
	activityContextCache      *activityContextCache           // recent-activity summary injected into chat prompts (once per session)
	slackDaemon               *daemonClient                   // Slack daemon WebSocket client (nil when not running)
	slackLocal                *slackLocalReceiver             // Slack local Socket Mode receiver (standalone; nil when not running)
	discordLocal              *discordLocalReceiver           // Discord local gateway receiver (standalone; nil when not running)
	feishuDaemon              *daemonClient                   // Feishu daemon WebSocket client (nil when not running)
	imLocal                   map[string]*feishuLocalReceiver // Local Feishu/Lark WebSocket receivers, keyed by brand ("feishu"/"lark")
	feishuWatchdogCancel      context.CancelFunc              // cancels the receiver health watchdog goroutine
	discordDaemon             *daemonClient                   // Discord daemon WebSocket client (nil when not running)
	telegramDaemon            *daemonClient                   // Telegram daemon WebSocket client (nil when not running)
	telegramLocal             *telegramLocalReceiver          // Telegram local bot receiver (standalone; nil when not running)
	wechatMon                 *wechatMonitor                  // WeChat long-polling monitor (nil when not running)
	wechatLastActivity        time.Time                       // last time user DM'd the WeChat bot (zero = never)
	updater                   *updater                        // self-update lifecycle (see version.go)
	bgShells                  *bgShellTracker                 // tracks background shells → session associations
	sessionWorkDirMu          sync.Mutex                      // guards only the per-workspace lock registry
	sessionWorkDirLocks       map[string]*sessionWorkDirLock  // normalized tag → active holders/waiters; guarded by sessionWorkDirMu
	sessionWorkDirRemoveAll   func(string) error              // test seam; nil uses os.RemoveAll
	sessionTempDirs           map[string]string               // tag → work dir for standalone sessions
	sessionFallbackDirs       map[string]struct{}             // tags using temporary fallback dirs; guarded by mu
	pendingTraceIDs           map[string]string               // session tag → originating chat trace_id, for post-run memory pipeline correlation; guarded by mu
	whisper                   *whisperState                   // voice input / sherpa-onnx state (nil until first use)
	tts                       *ttsState                       // text-to-speech / sherpa MeloTTS state (nil until first use)
	meetingTranscriptActive   sync.Map                        // session IDs whose recording is being transcribed
	backgroundAudioMu         sync.Mutex                      // serializes recorder start/stop across settings and privacy transitions
	backgroundAudio           *backgroundAudioCapture         // background speech capture (nil until enabled)
	meetingRecordingMu        sync.Mutex                      // serializes preference changes with automatic meeting lifecycle commits
	meetingRecordingDisabled  atomic.Bool                     // cached inverse preference; zero value preserves default-on behaviour
	meetingRecordingEpoch     atomic.Uint64                   // invalidates captured/queued meeting work across every preference change
	audioScreenLocked         func() bool                     // injected protected-desktop detector, initialized at startup
	voiceID                   *voiceEnrollmentState           // speaker embedding extractor + Self-embedding cache (nil until first use)
	voiceMemoryMu             sync.Mutex
	voiceMemoryGeneration     uint64            // guarded by voiceMemoryMu; fences explicit enrollment across Forget
	authManager               *AuthManager      // centralized auth fallback (see auth_manager.go)
	telemetry                 *PostHogTelemetry // PostHog-backed native.Telemetry; safe to use even when init failed (no-op fallback)

	// captureSpans follows what was in front and who held the microphone at
	// every moment of a chunk (capture_spans.go).
	captureSpans captureSpans

	// callsKeptAtPause are call recordings saved when observation was
	// paused, transcribed when it resumes (meeting_audio_session.go).
	callsKeptAtPauseMu sync.Mutex
	callsKeptAtPause   []callKeptAtPause

	// systemAudioOutsideCalls caches the switch that turns the computer's
	// own sound into text outside calls (background_listening_outside_calls.go).
	systemAudioOutsideCalls     atomic.Bool
	systemAudioOutsideCallsOnce sync.Once

	// Quick Chat Spotlight helper — see DESIGN.md in
	// quick-chat-spotlight/. The supervisor spawns the helper
	// binary at startup; the stream hub fans out chat session
	// events to the SSE consumer running inside the helper. Both
	// nil until App.startup wires them.
	spotlight       *spotlightSupervisor
	spotlightStream *spotlightStreamHub
	// spotlightTags maps active spotlight session tags to their
	// per-session metadata (workdir, accumulated assistant content
	// for batched persistence). Reads happen in the agent:event
	// listener (hot path) so stay under the mu — keep work minimal.
	spotlightTagsMu sync.Mutex
	spotlightTags   map[string]*spotlightTagState

	// In-process listeners for emitAgentEvent. Spotlight registers one
	// to mirror agent stream events into spotlightStreamHub. Future
	// hooks (audit, server-side persistence) can plug in without
	// touching streaming.go.
	agentEventListenersMu sync.RWMutex
	agentEventListeners   []func(AgentEvent)
	// agentEventItems names the rows of turns in flight (agent_event_items.go).
	agentEventItems agentEventItems

	// Managed model accounts (magpie_api.go). magpie owns the gateway child
	// process; magpieModels caches what the bindings report.
	magpie             *magpieSidecar
	magpieModels       magpieModelState
	magpieQuotaOnce    sync.Once
	magpieFallbackOnce sync.Once

	probeSucceeded         bool              // true after credential probe succeeds
	cachedDisplayName      string            // cached from GetUserProfile on first call
	localClaudeEnvSnapshot map[string]string // local Claude-compatible env vars to restore after gateway override
	todayDayShapeUpdates   todayDayShapeUpdateState
	eventBroadcaster       *EventBroadcaster // forwards events to browser WebSocket clients
	browserServer          *browserServer    // HTTP+WS server for browser-mode access
	mobileBridge           *mobileBridge     // opt-in authenticated mobile hub transport
	desktopTaskBridge      *mobileBridge     // task turns when no phone is paired
	// Serialize phone and desktop choice dispatch through the same lock.
	taskChoiceMu         sync.Mutex
	desktopOfferStartMu  sync.Mutex
	desktopStartedOffers map[string]HandoffAcceptResult
	mobileBridgeMu       sync.Mutex // serializes mobile enable, pairing, and shutdown
	mobileBridgeStopping bool
	mobilePoweringOff    atomic.Bool
	handoffShuttingDown  atomic.Bool
	yourAgentMu          sync.Mutex // guards the background Your agent refresh
	yourAgentRefreshing  bool
	remoteAccessFactory  func() mobileRemoteAccessSupervisor
	mobileBriefSvc       mobileBriefSource // daily brief producer for paired phones; set from mobileBriefServiceFactory
	topicsOnce           sync.Once         // makes topics on first use (topics_service.go)
	topics               *topicsService    // what the user has been looking at, and the daily finds on it
	interestModelOnce    sync.Once         // makes the interest model on first use (interests_service.go)
	interestModel        *interestsService // what the user is into, named by a model a few times a day
	// scheduleMissedRaised remembers the missed-schedule notices raised in
	// this process (dedupe key -> true), so a schedule that keeps failing is
	// submitted once a day; guarded by scheduleMissedMu.
	scheduleMissedMu     sync.Mutex
	scheduleMissedRaised map[string]bool
	engagement           *engagementService // paces and routes moments to the phone, the Mac or a text
	singletonMessages    <-chan string      // IPC channel from second-launch attempts; drained in startup to bring window forward
	// calendarSync reads the user's calendars into calendar_events (calendar_sync.go);
	// calendarPeek reads web calendars through the browser while the Mac is idle (calendar_peek.go).
	calendarSync *calendarSyncService
	calendarPeek *calendarPeekService
	// remindersSync reads the user's reminders into reminders and keeps the
	// commitments made from them current (reminders_sync.go).
	remindersSync *remindersSyncService
	// clipboard notes what the user copies (clipboard_observer.go). Set once
	// at startup and read from bindings, hence atomic.
	clipboard atomic.Pointer[clipboardObserver]
	// browserHistory reads the Chromium History databases into
	// browser_visits (browser_history_sync.go).
	browserHistory *browserHistoryReader
	// filesActivity asks Spotlight which documents the user worked on into
	// file_activity (files_activity_sync.go).
	filesActivity *filesActivityReader
	// mailSync reads the headers of recent mail from Mail.app into
	// mail_messages while Mail runs (mail_sync.go).
	mailSync *mailSyncService
	// proactiveSendTransport is replaceable by local fakes in send lifecycle tests.
	proactiveSendTransport proactiveSendTransport
	// contextSignals tracks lock, sleep, idle, power and device use (context_signals.go).
	contextSignals *contextSignalService
	// captureCountsMemo reuses the last capture counts briefly (capture_counts.go).
	captureCountsMemo captureCountsMemo
	// observationCategories caches installed-app categories, never observed usage.
	observationCategories observationCategoryCache
	// doingNow keeps the menu-bar "doing now" line current (doing_now.go).
	doingNow doingNowTracker
	// Slack scan pipeline lifecycle.
	slackPipelineRunMu sync.Mutex // held for full dynamic pipeline runs
	slackScanner       *SlackIncrementalScanner

	// Discord scan pipeline lifecycle.
	discordPipelineRunMu sync.Mutex
	discordScanner       *DiscordIncrementalScanner

	// Feishu incremental scanner (see feishu_incremental_scanner.go).
	feishuPipelineRunMu sync.Mutex // held for full dynamic pipeline runs
	feishuScanner       *FeishuIncrementalScanner

	// Telegram scan pipeline lifecycle.
	telegramPipelineRunMu sync.Mutex
	telegramScanner       *TelegramIncrementalScanner

	// Cron job scheduler (see cron.go).
	cronScheduler *CronScheduler
	cronAPI       *cronAPIServer    // REST API for CLI (see cron_api.go)
	runTokens     runtoken.Registry // memory-only agent credentials, never published

	// mcpRuntime owns the user-installed MCP server subprocesses and
	// the bridge that exposes their tools to the agent loop. nil until
	// initMcpRuntime runs in startup. Guarded by mu — see mcp_hub_runtime.go.
	mcpRuntime *mcpRuntime

	pagePermissions pagePermissionCache
	pageWarm        pageFirstReadWarmer
	// derived holds the page snapshots (derived_views.go). Guarded by mu.
	derived *derivedViews

	memoryCanonicalizer func(prompt, model string) (string, error) // optional canonicalization adapter; nil uses the existing model runner
}

type AgentStatus struct {
	Running bool   `json:"running"`
	Ready   bool   `json:"ready"`
	Port    int    `json:"port"`
	Message string `json:"message,omitempty"`
}

// emitBrowserEvent sends an event to both the Wails runtime (native WebView)
// and any connected browser WebSocket clients. Use this instead of
// runtime.EventsEmit when dual-mode delivery is needed.
// refreshMobileBriefTodaySoon asks the brief service for a coalesced live Today
// refresh. It runs off the emitting goroutine, which may hold other stores' locks.
func (a *App) refreshMobileBriefTodaySoon() {
	if svc, ok := a.mobileBriefSource().(*mobileBriefService); ok && svc != nil {
		svc.refreshTodaySoon()
	}
}

// refreshMobileBriefTodayLiveSoon is refreshMobileBriefTodaySoon for a mail sync:
// live sections only, no store or seal for an existing sealed brief. It reaches
// the live brief service the privacy amendment uses (mobileBriefLive), since a
// mail sync may need the gates to refuse a sealed copy (see refreshToday).
func (a *App) refreshMobileBriefTodayLiveSoon() {
	svc, _ := a.mobileBriefSource().(*mobileBriefService)
	if svc == nil {
		if live := mobileBriefLive.Load(); live != nil && live.app == a {
			svc = live
		}
	}
	if svc == nil || !svc.beginWork() {
		return
	}
	// The work slot is taken here, before the goroutine starts, so a caller that
	// waits for the service sees the refresh it asked for.
	go func() {
		defer svc.wg.Done()
		svc.refreshTodayLiveSoon()
	}()
}

func (a *App) emitBrowserEvent(eventName string, data ...interface{}) {
	if mobileBriefPrivacyInputEvents[eventName] {
		// A privacy change advances the epoch durably; the sealed briefs are
		// amended after it, and any read before that amends its brief first.
		briefPrivacyChanged()
		briefPrivacyAmendSoon()
	} else if mobileBriefReadInputEvents[eventName] {
		noteMobileBriefReadCachesChanged()
	}
	if mobileBriefReadInputEvents[eventName] && !mobileBriefPrivacyInputEvents[eventName] {
		// Source changes refresh Today's live signals in the background, at most
		// once per todayRefreshGap. The brief is read again when that refresh
		// stores a change (brief:stored), never for the event itself (R24).
		if eventName == "mail:synced" {
			// A mail sync refreshes Today's live sections only; it never stores or
			// seals an existing sealed brief (TestFiftyMailSyncsLeaveSealedBriefShown).
			go a.refreshMobileBriefTodayLiveSoon()
		} else {
			go a.refreshMobileBriefTodaySoon()
		}
	}
	a.noteDerivedViewEvent(eventName)
	a.noteStreamPageEvent(eventName)
	if topicFindsSourceEvents[eventName] {
		a.noteTopicFindsSourceChanged(eventName)
	}
	switch eventName {
	case "memory:updated", "sensitive-apps:updated", closedEyesModeChangedEvent, "background-listening:changed", "topics:withdrawn":
		if bridge := a.mobileBridgeForUse(); bridge != nil {
			bridge.invalidateToday()
		}
	case "calendar:synced", "brief:stored", "topics:updated", "handoff:updated", mainChatItemsAppendedEvent, mainChatWaitingChangedEvent, assistantHistoryEvent:
		if bridge := a.mobileBridgeForUse(); bridge != nil {
			bridge.noteTodayChanged()
		}
	}
	switch eventName {
	case "calendar:synced", "brief:stored":
		a.noteTodayDayShapeUpdate()
	case "memory:updated", "sensitive-apps:updated", closedEyesModeChangedEvent, "background-listening:changed":
		a.clearPageSnapshots()
		if eventName == "memory:updated" {
			if service, ok := a.mobileBriefSource().(*mobileBriefService); ok && service != nil {
				service.remakeAudioSoon()
			}
		}
	case magpieModelsUpdatedEvent:
		invalidateYourWeekLive()
	}
	if eventName == "activity:session-saved" || eventName == "activity:activities-updated" {
		a.topicsSvc().markTopicsStale()
	}
	switch eventName {
	case "sensitive-apps:updated", closedEyesModeChangedEvent,
		"background-listening:changed", "permissions:changed":
		a.markMainChatWaitingDirty("privacy:" + eventName)
	case "memory:updated":
		// A belief update is not a privacy action: a forget or correction
		// advances the privacy epoch, and the derived scheduler marks the
		// waiting state then. With snapshots off (tests, onboarding) every
		// update still re-reads it, as before.
		if !a.derivedViews().active() {
			a.markMainChatWaitingDirty("privacy:" + eventName)
		}
	}
	a.noteDoingNowEvent(eventName)
	// Purpose understanding no longer reruns on every belief or self-model
	// update: it reads them on its daily cycle (derived_cadence.go).
	// Unit and browser-mode callers may provide an ordinary context without
	// Wails' event bus. EventsEmit treats that as fatal rather than returning
	// an error, so only address the native runtime when its event value exists.
	if a.ctx != nil && a.ctx.Value("events") != nil {
		runtime.EventsEmit(a.ctx, eventName, data...)
	}
	if a.eventBroadcaster != nil {
		a.eventBroadcaster.Broadcast(eventName, data...)
	}
	// Today and Tasks consume one shared projection. The derived event has
	// no payload: titles, names and brief text do not enter telemetry or the
	// change notification.
	switch eventName {
	case "handoff:created", "handoff:updated", mainChatItemsAppendedEvent, mainChatWaitingChangedEvent, "open-answers:changed":
		a.emitBrowserEvent("today:needs-changed")
	}
	switch eventName {
	case "handoff:created", "handoff:updated", mainChatItemsAppendedEvent, "open-answers:changed", "deliverables:updated", "brief:stored", "cron:state":
		a.emitBrowserEvent("today:done-changed")
	}
}

// stopAllDaemons stops all active platform relay daemons and clears their slots.
// Used when the backend environment changes so no daemon keeps reconnecting to an old base URL.
func (a *App) stopAllDaemons() {
	a.mu.Lock()
	slack := a.slackDaemon
	feishu := a.feishuDaemon
	imLocal := a.imLocal
	discord := a.discordDaemon
	telegram := a.telegramDaemon
	telegramLocal := a.telegramLocal
	slackLocal := a.slackLocal
	discordLocal := a.discordLocal
	wechat := a.wechatMon
	a.slackDaemon = nil
	a.feishuDaemon = nil
	a.imLocal = map[string]*feishuLocalReceiver{}
	a.discordDaemon = nil
	a.telegramDaemon = nil
	a.telegramLocal = nil
	a.slackLocal = nil
	a.discordLocal = nil
	a.wechatMon = nil
	feishuWatchdogCancel := a.feishuWatchdogCancel
	a.feishuWatchdogCancel = nil
	a.mu.Unlock()

	if feishuWatchdogCancel != nil {
		feishuWatchdogCancel()
	}

	for _, daemon := range []*daemonClient{slack, feishu, discord, telegram} {
		if daemon != nil {
			daemon.stop()
		}
	}

	if telegramLocal != nil {
		telegramLocal.stop()
	}
	if slackLocal != nil {
		slackLocal.stop()
	}
	if discordLocal != nil {
		discordLocal.stop()
	}
	if wechat != nil {
		wechat.stop()
	}
	for _, r := range imLocal {
		if r != nil {
			r.stop()
		}
	}
}

// restartRelayDaemonsForCurrentEnv re-runs the per-platform auto-start flow using the current base URL.
// Existing daemons are stopped first so reconnect loops cannot stay pinned to an old environment.
func (a *App) restartRelayDaemonsForCurrentEnv() {
	a.stopAllDaemons()

	go a.tryAutoStartDaemon()
	go a.tryAutoStartFeishuDaemon()
	go a.tryAutoStartDiscordDaemon()
	go a.tryAutoStartTelegramDaemon()
}

type AgentEvent struct {
	Type              string                   `json:"type"`
	RunID             string                   `json:"run_id,omitempty"`
	SessionID         string                   `json:"session_id,omitempty"`
	Tag               string                   `json:"tag,omitempty"`
	Content           string                   `json:"content,omitempty"`
	ToolName          string                   `json:"tool_name,omitempty"`
	ToolInput         json.RawMessage          `json:"tool_input,omitempty"`
	ToolUseID         string                   `json:"tool_use_id,omitempty"`
	Persisted         bool                     `json:"persisted,omitempty"`
	AvailableCommands []agent.AvailableCommand `json:"available_commands,omitempty"`
	Error             string                   `json:"error,omitempty"`
	// Code is an error's proven cause (chat_cause.go); Assistant is the AI
	// the cause line names. Both are omitted when unknown.
	Code                string                   `json:"code,omitempty"`
	Assistant           string                   `json:"assistant,omitempty"`
	PermissionOptions   []agent.PermissionOption `json:"permission_options,omitempty"`
	PermissionRequestID string                   `json:"permission_request_id,omitempty"`
	// UpcomingApproval names the item an Upcoming tool's approval question
	// is about, so the card can say it.
	UpcomingApproval *UpcomingApproval `json:"upcoming_approval,omitempty"`
	// EventID names one emit (emitAgentEvent sets it). Every renderer that
	// receives the event, the app window and a browser preview over the
	// bridge, names the row it builds from it with this id, so their writes
	// land on one stream_items row instead of one row each.
	EventID string `json:"event_id,omitempty"`
	// ItemID names the assistant, thinking or tool row this event builds or
	// grows, and ItemSeq is an assistant or thinking chunk's place in that
	// row, from 1 (agent_event_items.go). Every renderer stores the row under
	// ItemID, so one that joins mid-reply grows the same row.
	ItemID  string `json:"item_id,omitempty"`
	ItemSeq int    `json:"item_seq,omitempty"`
}

func NewApp() *App {
	recordLaunchStage("app_init_start")
	defer recordLaunchStage("app_init_done")
	app := &App{
		storageStartup:         newStorageStartupState(),
		launch:                 newLaunchStartup(),
		streams:                make(map[string]context.CancelFunc),
		imLocal:                make(map[string]*feishuLocalReceiver),
		tagCancels:             make(map[string]context.CancelFunc),
		runIDByTag:             make(map[string]string),
		activeTags:             make(map[string]bool),
		readyCh:                make(chan struct{}),
		sessionTempDirs:        make(map[string]string),
		sessionFallbackDirs:    make(map[string]struct{}),
		pendingTraceIDs:        make(map[string]string),
		updater:                &updater{status: UpdateStatus{Status: "idle"}},
		activityContextCache:   newActivityContextCache(),
		composioActivationErrs: make(map[string]string),
		bgShells:               newBgShellTracker(),
	}
	// Persisted observation policy is unknown until mandatory storage opens.
	app.closedEyes.policyPending.Store(true)
	app.topicSource = &registryTopicSource{app: app}
	app.magpie = newMagpieSidecar(defaultMagpieStateDir())
	app.magpie.onHealthy = app.onMagpieHealthy
	app.meetingRecordingDisabled.Store(loadConfig().AutomaticMeetingRecordingDisabled)
	app.termMgr = terminalsvc.NewManager(func(_ context.Context, terminalID string) {
		app.emitBrowserEvent("terminal:exit", terminalID)
	})
	app.authManager = NewAuthManager(app)
	// The stored "Share diagnostic data" choice is read before the client
	// exists, so an off setting holds from the first event.
	loadTelemetryTierFromDisk()
	app.telemetry = NewPostHogTelemetry()
	recordLaunchStage("telemetry_ready")

	// Initialize SSH connection manager. The status callback emits Wails
	// events so the frontend can display real-time connection state.
	app.sshMgr = sshconn.NewManager(func(status sshconn.ConnectionStatus) {
		if app.ctx != nil {
			app.emitBrowserEvent("ssh:status", status)
		}
	})
	// Storage opens after Wails starts so snapshots can show a progress window.

	app.telemetry.setLocale(app.currentUILanguage())
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// The window was created at the setup size when setup is needed (main.go);
	// this applies the setup styling once the app runs.
	a.applySetupWindowAtLaunch()
	// Schedule the status item before storage and optional services can delay it.
	initMenuBarEarly(a)
	recordLaunchStage("window_created")
	// Whether Screen Recording was on at launch: turned on later, it works
	// only after a reopen (relaunch.go).
	recordLaunchStage("launch_permissions_start")
	recordScreenRecordingAtLaunch()
	recordLaunchStage("launch_permissions_done")
	if ctx.Value("events") != nil {
		runtime.EventsOn(ctx, "pages:first-paint", func(...interface{}) { a.pageFirstReadsPainted() })
	}
	a.telemetry.Capture("app.open", nil)
	a.beginStorageStartup(ctx)
}

func (a *App) startupServices(ctx context.Context) {
	recordLaunchStage("services_start")
	a.startMainChatWaitingLifecycle(ctx)

	a.deferLaunchTask("storage_repair", func() { repairStartupStorage(a.store) })
	a.deferLaunchTask("ocr_prewarm", ocr.PrewarmHelper)
	a.deferLaunchTask("assistant_history", a.scheduleAssistantHistory)
	a.startActivityMaintenance()
	manager, err := a.setupModelManager()
	if err != nil {
		log.Printf("[setup] models: %v", err)
		manager = nil
	}
	a.deferSetupStartup(manager)
	a.telemetry.setLocale(a.currentUILanguage())

	// Drain any IPC "focus" messages that came in from second-launch
	// attempts. Started here because runtime.WindowShow needs ctx, which
	// only exists once Wails has called startup.
	if a.singletonMessages != nil {
		go a.handleSingletonMessages()
	}

	// Hydrate the user's last theme choice from disk so the spotlight
	// helper can be served the right palette via the host bridge before
	// the React side has had a chance to call SetThemePreference.
	loadThemePreferenceFromDisk()
	loadQuickChatHotkeyFromDisk()

	// Stamp startup time so the perf-analysis panel can report uptime
	// without threading an extra field through the App struct.
	recordAppStart()

	// Heartbeat goroutine: emits one log line every watchdogInterval so a
	// future "Yansu hung" report can be diagnosed by inspecting where the
	// heartbeat stopped. See watchdog.go for the interpretation guide.
	go runWatchdog(ctx, time.Now().UnixMilli())

	// Bind this App for audio-capture event emission so the DevTools
	// "Audio Capture" panel receives live recorder lifecycle events.
	// Must happen before background listening starts so the first events
	// aren't dropped.
	registerAudioCaptureEventApp(a)

	// Background listening defaults to enabled for fresh installs, but stays
	// off on upgrades until the user explicitly confirms the setting.
	ensureBackgroundListeningDefaultForFreshInstall()

	// Optional pprof endpoint for performance debugging.
	// Enable with YANSU_PPROF=1 (or YANSU_PPROF=<port>) when starting Yansu.
	// Then: go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30
	if v := os.Getenv("YANSU_PPROF"); v != "" {
		port := "6060"
		if v != "1" {
			port = v
		}
		go func() {
			addr := "127.0.0.1:" + port
			log.Printf("[pprof] listening on http://%s/debug/pprof/", addr)
			if err := http.ListenAndServe(addr, nil); err != nil {
				log.Printf("[pprof] server exited: %v", err)
			}
		}()
	}

	// Source and voice policies were restored before storage readiness.

	// Pre-warm the /Applications scan cache so the saved-apps settings page
	// can render its icon/name enrichment instantly. Scan is ~3 seconds cold;
	// running it in the background here means it's typically warm by the time
	// the user navigates to Settings.
	a.deferLaunchTask("system_apps", func() { listSystemApps() })

	// Sweep .yansu-update-* dirs leaked by older updater versions (os.Exit
	// skipped the deferred cleanup). Best-effort; safe to skip when not
	// running from an installed .app (dev builds).
	if bundle := findAppBundle(); bundle != "" {
		cleanLeakedStagingDirs(filepath.Dir(bundle))
		// Removing a stale prepared update (a whole app) is slow disk work.
		a.deferLaunchTask("prepared_update_cleanup", func() { a.cleanStalePreparedUpdates(bundle) })

		// Self-heal quarantine left behind by older updaters: their detached
		// `xattr -cr` could die before finishing, leaving the bundle with
		// propagated com.apple.quarantine xattrs. A quarantined executable
		// makes the kernel re-stamp the xattr on every file the next update
		// extracts, so one failed strip used to poison every update after it
		// ("Yansu.app is damaged"). The app is running, so Gatekeeper already
		// cleared this copy — stripping is safe and ends the loop.
		a.deferLaunchTask("quarantine_cleanup", func() {
			if removed, err := stripQuarantineRecursive(bundle); err != nil {
				log.Printf("[update] startup quarantine self-heal: %v", err)
			} else if removed > 0 {
				log.Printf("[update] startup quarantine self-heal: stripped %d stale xattrs from %s", removed, bundle)
			}
		})
	}

	// Patch WKWebView UIDelegate to grant microphone permission for voice input.
	// Must run after wails.Run() creates the WailsContext ObjC class.
	patchWKWebViewMediaCapture()

	// The title strip drags the window on the first click, even when the
	// window is not active (macOS).
	patchWebViewTitleStripFirstMouse()

	// The menu bar was scheduled at Wails startup, before storage opened.
	menuBarServicesReady(a)
	a.refreshAppMenu()

	// Policy was restored before storage publication. Only its timer and native
	// presentation wait for the menu bar and observation services to exist.
	a.startObservationResumeTimer()
	a.startBackgroundListeningPrivacyMonitor(ctx)

	// Start at login defaults to enabled for both new installs and upgrades.
	// Respect an explicit user opt-out persisted in config.
	ensureStartAtLoginEnabled()

	// If launched as login item, switch to accessory mode (no Dock icon).
	if shouldStartHidden() {
		runtime.WindowHide(ctx)
		switchToAccessoryApp()
	}

	// Allow triage logger to emit real-time Wails events.
	setTriageApp(a)

	// PATH and ANTHROPIC_* env vars are resolved in main() before
	// wails.Run() so the frontend sees correct values immediately.
	a.captureLocalClaudeEnvSnapshot()

	// Extract embedded app icon for desktop notifications.
	a.deferLaunchTask("notify_icon", a.ensureNotifyIcon)

	// When the user clicks a desktop toast, emit a Wails event so the frontend
	// can navigate to the relevant content.  On darwin
	// this binds to the in-house NSWindow toast; on other platforms it falls
	// back to the OS notification click pipeline.
	setToastClickHandler(func(clickData string) {
		runtime.WindowShow(a.ctx)
		runtime.EventsEmit(a.ctx, "notification:clicked", clickData)
	})

	// Warm the WebKit machinery so the first toast paints without a visible
	// cold-start delay.
	a.deferLaunchTask("toast_prewarm", prewarmDesktopToast)

	// Ensure the bundled yansu CLI is installed on this machine and clean up
	// any legacy skill files older versions injected into agent directories.
	a.deferLaunchTask("bundled_assets", ensureBundledYansuAssets)

	// Isoform no longer installs its skill into the user's own Claude Code or
	// Codex; runs carry it per run (isoformskill.Prompt). Remove the copies
	// older versions installed, once, logging one line when it removes any.
	a.deferLaunchTask("skills_cleanup", removeInstalledIsoformSkillsLogged)

	// Move the assistant model and effort picks out of magpie's per-agent
	// settings and isoform-choice.json into Isoform's own config, once.
	a.deferLaunchTask("agent_choices", a.migrateAgentChoices)

	// Register git path resolver so cmdutil.GitCommand uses the resolved binary.
	// This runs before ensureBundledGit so that system git (if present) is
	// immediately available. The async goroutine below handles the bundled
	// install for systems without git, and updates PATH when done.
	cmdutil.SetGitPathResolver(ResolvedGitPath)

	// Ensure bundled git is available for clone/worktree operations.
	// Only local bundle installation runs here. Missing Windows Git requires
	// a separately confirmed download plan.
	bundledGitInstallStart.Store(true)
	a.deferLaunchTask("bundled_git", func() {
		defer bundledGitInstallOnce.Do(func() {
			close(bundledGitInstallDone)
		})

		if gitPath, err := ensureBundledGit(); err != nil {
			log.Printf("[git] bundled install skipped: %v", err)
		} else if gitPath != "" {
			ensurePathDirInEnv(gitPath)
		}
	})

	// Ensure bundled bun runtime is available for the agent runtime.
	a.deferLaunchTask("bundled_bun", func() {
		if bunPath, err := ensureBundledBun(); err != nil {
			log.Printf("[bun] bundled install skipped: %v", err)
		} else if bunPath != "" {
			ensurePathDirInEnv(bunPath)
		}
	})

	// Ensure bundled ffmpeg is available for activity video transcoding.
	a.deferLaunchTask("bundled_ffmpeg", func() {
		if ffmpegPath, err := ensureBundledFFmpeg(); err != nil {
			log.Printf("[ffmpeg] bundled install failed: %v", err)
		} else {
			log.Printf("[ffmpeg] available at %s", ffmpegPath)
		}
	})

	// Seed the bundled cua-driver skill into the skill hub. No-ops on
	// platforms that don't ship the binary; idempotent across launches.
	a.deferLaunchTask("cua_skill", func() { ensureBundledCuaSkill() })

	// Seed the bundled lark-cli skill (Feishu/Lark). No-ops without the bun
	// runtime; idempotent across launches.
	a.deferLaunchTask("lark_skill", func() { ensureBundledLarkSkill() })

	// Seed the bundled OpenCLI skills (opencli-usage + 4 specialized).
	// Pure-markdown payload — no binary dependency, runs on every platform.
	a.deferLaunchTask("opencli_skills", func() { ensureBundledOpenCLISkills() })

	// Initialise the cua-driver daemon handle, then eager-start it in
	// the background. Eager start is required because yansu-cli's
	// cua_driver tool dials the canonical socket directly from a
	// subprocess — it bypasses /api/cua/call's lazy-spawn path, so
	// without an upfront EnsureRunning the first agent call gets
	// ECONNREFUSED and the model rabbit-holes into shell fallbacks.
	// The goroutine keeps app launch non-blocking; EnsureRunning has
	// its own spawn backoff so failures don't fork-bomb us.
	if daemon, err := NewCuaDriverDaemon(); err != nil {
		log.Printf("[cua-driver] daemon disabled: %v", err)
	} else {
		a.cuaDriver = daemon
		// An unattended run's snapshot asks for its pages' addresses so the
		// page check can see them (yansu-agent tools/cua_page_gate.go).
		agenttools.CuaSourceLocatorCapability = daemon.issueToolSourceLocatorCapability
		a.deferLaunchTask("cua_start", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := daemon.EnsureRunning(ctx); err != nil {
				log.Printf("[cua-driver] eager start failed (will retry on first call): %v", err)
			}
		})
	}

	// Install the pinned OpenCLI bundle locally, without package installation
	// or background updates.
	// Runs async — OpenCLI is an enhancement, not critical for basic operation.
	// Signals opencliInstallDone when complete so callers can wait for it
	// instead of racing.
	a.deferLaunchTask("bundled_opencli", func() {
		defer opencliInstallOnce.Do(func() { close(opencliInstallDone) })
		if opencliPath, err := ensureBundledOpenCLI(); err != nil {
			log.Printf("[opencli] install skipped: %v", err)
		} else if opencliPath != "" {
			ensurePathDirInEnv(opencliPath)
		}
	})

	// Tell opencli to open Chrome windows unfocused — every opencli command
	// the agent runs (via the Bash tool, which inherits Yansu's environment)
	// will see this and pass focused:false to chrome.windows.create. The
	// user's browser tab still opens to do the work, but it does NOT pop to
	// the foreground and steal focus from whatever app they're using. This
	// is the upstream-supported way to dial back opencli's intrusiveness;
	// see opencli docs and bundled-opencli-skills/opencli-usage/SKILL.md
	// (OPENCLI_WINDOW env var).
	_ = os.Setenv("OPENCLI_WINDOW", "background")

	// The Spotlight supervisor exists before the host bridge serves, whose
	// /api/spotlight/* routes read it; it starts the helper further down.
	a.spotlight = newSpotlightSupervisor(0)

	// Start the host bridge so bundled apps can call back into the host.
	if bridge, err := startHostBridge(ctx, a); err != nil {
		log.Printf("[host-bridge] failed to start: %v", err)
	} else {
		a.bridge = bridge
	}

	// Start the browser-mode HTTP+WebSocket server so the UI can run in a
	// regular browser in addition to the native Wails WebView. Gated behind
	// shouldEnableBrowserBridge() so production builds default to off (dev
	// builds default to on). Users can toggle at runtime via Settings.
	bridgeEnabled := shouldEnableBrowserBridge()
	logBrowserBridgeDecision(bridgeEnabled)
	if bridgeEnabled {
		a.eventBroadcaster = NewEventBroadcaster()
		if bSrv, err := startBrowserServer(a, a.eventBroadcaster); err != nil {
			log.Printf("[browser-server] failed to start: %v", err)
		} else {
			a.browserServer = bSrv
		}
	}

	// Kill orphaned bun processes left behind by a previous app instance
	// (e.g. after crash or force-quit) before restoring sessions.
	cleanupOrphanedBunProcesses()
	// The Claude Code janitor reaps only the workers this app started itself,
	// tracked through the agent's process hooks (and research_cli.go) — never
	// anything out of ~/.claude or the user's own sessions.
	agent.SetProcessHooks(agent.ProcessHooks{Started: trackIsoformClaudeWorker, Exited: forgetIsoformClaudeWorker})
	cleanupOrphanedClaudeSessionWorkers()
	startClaudeSessionWorkerCleanup(ctx)

	// Reap any stale whisper-server.exe left behind by pre-sherpa builds
	// (Windows-only; no-op elsewhere). New builds never spawn whisper-server,
	// so anything matching by name is by definition orphaned.
	cleanupLegacyWhisperZombies()

	// Anthropic credentials are handled lazily: the agent starts with native
	// auth first; AuthManager.RecoverAuth injects sub2api only if the probe
	// or a user request fails with an auth error.

	// Initialize activity and memory sequentially after the first screen. Both
	// can load or download models; neither is required to read persisted views.
	a.deferLaunchTask("agent_start", func() {
		if _, err := a.StartAgent(); err != nil {
			a.emitAgentStatus(AgentStatus{Running: false, Ready: false, Message: err.Error()})
		}
	})
	activityDone := a.deferLaunchTask("activity_start", func() {
		a.startObservationActivityMonitor()
		menuBarObservationStarted.Store(true)
		go a.refreshMenuBarPresentation()
		// Early policy loading clears chat only; source-backed project cleanup
		// must wait until Activity startup has opened its store.
		if err := a.scrubSensitiveTopics(); err != nil {
			log.Printf("[topics] startup sensitive-app scrub: %v", err)
		}
	})
	a.deferLaunchTask("saved_chat_work_names", func() {
		if err := a.repairSavedChatWorkNames(context.Background()); err != nil {
			log.Printf("[main-chat] saved work name repair: %v", err)
		}
	})

	// Quick Chat Spotlight: stream hub is created eagerly so any
	// component that wants to publish events doesn't need a nil
	// check. The supervisor (helper process) starts in the
	// background — Show calls fall back gracefully if it isn't
	// installed (typical in dev). See DESIGN.md in quick-chat-spotlight.
	a.spotlightStream = newSpotlightStreamHub()
	a.registerSpotlightAgentBridge()
	if a.bridge != nil {
		a.spotlight.SetHostBridgePort(a.bridge.Port())
	}
	go func() {
		if err := a.spotlight.Start(a.ctx); err != nil {
			log.Printf("[spotlight] supervisor start failed: %v", err)
		}
	}()

	// Quick Chat global hotkey. Non-fatal on failure: the in-app
	// Cmd+J shortcut still works even if the global tap can't start
	// (e.g. Accessibility permission pending).
	go func() {
		if err := a.StartQuickChatHotkey(); err != nil {
			log.Printf("[quickchat] global hotkey unavailable: %v", err)
		}
	}()

	var memoryStore *MemoryStore
	memoryDone := a.deferLaunchTask("memory_open", func() {
		home, err := flavorDataDir()
		if err != nil {
			return
		}
		memCfgDir := home
		memCfg := loadMemoryStoreConfig(memCfgDir)
		ms, msErr := NewMemoryStore(memCfgDir, memCfg, a.emitDownloadProgress)
		if msErr != nil {
			log.Printf("[memory] store init failed: %v", msErr)
			return
		}
		memoryStore = ms
	})

	// Log the effective Claude env state before the agent inherits it.
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		baseURL := os.Getenv("ANTHROPIC_BASE_URL")
		model := os.Getenv("ANTHROPIC_MODEL")
		if baseURL != "" {
			log.Printf("[anthropic-env] custom API configured: base_url=%s model=%s (agent will use user-specified endpoint)", baseURL, model)
		} else {
			log.Printf("[anthropic-env] WARNING: ANTHROPIC_API_KEY is set but ANTHROPIC_BASE_URL is empty — agent will fall back to api.anthropic.com")
		}
	}

	// SSH configs / auto-connect removed along with SSH config persistence.

	// Start Slack incremental scanner early so Trigger() is available
	// before tryAutoStartDaemon fires. The scanner goroutine no-ops while the
	// Furnace feature is paused (see furnaceScanEnabled).
	scanner := NewSlackIncrementalScanner(a)
	a.slackScanner = scanner
	go scanner.Run(a.ctx)

	// Start Telegram incremental scanner early so Trigger() is available
	// before tryAutoStartTelegramDaemon fires.
	telegramScanner := NewTelegramIncrementalScanner(a)
	a.telegramScanner = telegramScanner
	go telegramScanner.Run(a.ctx)

	// Auto-start Slack daemon if persisted config exists.
	go a.tryAutoStartDaemon()

	// Auto-start Feishu daemon if persisted config exists.
	go a.tryAutoStartFeishuDaemon()

	// Start Discord incremental scanner early so Trigger() is available
	// before tryAutoStartDiscordDaemon fires.
	discordScanner := NewDiscordIncrementalScanner(a)
	a.discordScanner = discordScanner
	go discordScanner.Run(a.ctx)

	// Auto-start Discord daemon if binding exists.
	go a.tryAutoStartDiscordDaemon()

	// Auto-start Telegram daemon if persisted config exists.
	go a.tryAutoStartTelegramDaemon()

	// Auto-start WeChat monitor if persisted config exists.
	go a.tryAutoStartWeChatMonitor()

	// Pre-download speech models so voice input is ready on first use.
	// After the engine is ready, start background listening if the user has
	// it enabled.
	a.deferLaunchTask("voice_prewarm", func() {
		if a.GetBackgroundListening() {
			a.startBackgroundListeningIfReady()
		}
		a.prewarmVoiceInput()
		if a.launch.isStopped() {
			return
		}

		// prewarmVoiceInput may return early if another goroutine is already
		// initializing the engine (e.g. lazy init from the first voice
		// input). Wait for readiness via channel instead of polling to avoid
		// a data race on the plain bool field and eliminate busy-wait CPU.
		whisper, whisperReady, _, readyCh := a.whisperSnapshot()
		if whisper != nil && !whisperReady {
			log.Printf("[background-listening] speech engine not ready after prewarm, waiting on readyCh...")
			select {
			case <-readyCh:
				whisperReady = true
				log.Printf("[background-listening] speech engine became ready via readyCh")
			case <-time.After(2 * time.Minute):
				log.Printf("[background-listening] speech engine not ready after 2 min timeout")
			}
		}

		cfg := loadConfig()
		configExists := hasPersistedConfig()
		enabled := resolveBackgroundListeningEnabled(cfg, configExists)
		log.Printf("[background-listening] config check: configured=%v disabled=%v enabled=%v whisperReady=%v whisperNil=%v",
			cfg.BackgroundListeningConfigured, cfg.BackgroundListeningDisabled, enabled, whisperReady, whisper == nil)
		if configExists && !cfg.BackgroundListeningConfigured {
			log.Printf("[background-listening] existing install detected without explicit setting; leaving disabled for upgrade compatibility")
		}
		if enabled {
			a.startBackgroundListeningIfReady()
		}
	})

	// Bring up the cron scheduler before waiting for model initialization. It only depends on
	// a.store (sessions.db, opened before startupServices) and has no need for the
	// embedding model or activity monitor. Keeping it ahead of the wait means
	// a hang in NewMemoryStore or any other model initializer no longer
	// disables automation management.
	a.startCronScheduler()
	a.startContactsSync()
	a.startWorkProfileScheduler(ctx)

	// Wait for both model downloads to finish before wiring up shared resources.
	recordLaunchStage("models_wait")
	<-activityDone
	<-memoryDone
	if ctx.Err() != nil || a.launch.isStopped() {
		return
	}
	recordLaunchStage("models_ready")

	// Generate pending activity summaries (yesterday, last week) in the background.
	a.deferLaunchTask("pending_summaries", a.checkPendingSummaries)

	// Keep each day's and week's summary ready on its own: once after local
	// midnight, finish yesterday's daily (and on Mondays last week's weekly)
	// when missing or partial. Ticks every 15 minutes and no-ops when there
	// is nothing to do.
	go a.runAutoSummaryNightly(a.ctx)

	// Pre-warm the recent-activity context cache so the first chat session
	// of the run already has a summary to inject (generation is too slow
	// to run synchronously on the user's chat send path).
	a.warmupActivityContext()

	// Wire up memory store now that both are initialized.
	if memoryStore != nil {
		a.memoryStore = memoryStore
		memoryStore.SetOnUpdated(func() {
			if a.ctx != nil {
				a.emitBrowserEvent("memory:updated", map[string]string{"reason": "index"})
			}
		})
		go memoryStore.Run(a.ctx)

		// Warm up the ONNX embedding runtime so the user's first
		// chat message doesn't pay the ~15s cold-start tax. Runs
		// async so it never blocks app startup.
		a.deferLaunchTask("embedding_prewarm", func() {
			start := time.Now()
			if _, err := memoryStore.Search(context.Background(), "warmup", 1); err != nil {
				log.Printf("[memory] warmup search errored (non-fatal): %v", err)
			} else {
				log.Printf("[memory] ONNX embedding warmup done in %v", time.Since(start))
			}
		})

		// Share the embedding provider with the activity store for semantic OCR search.
		if a.activity != nil && a.activity.store != nil && memoryStore.embedder != nil {
			a.activity.store.SetEmbedder(memoryStore.embedder)
			go a.activity.store.RunEmbeddingWorker(a.ctx, a.activityEmbeddingGate(a.activity.store))
		}
	}

	// Start the generative memory pipeline (triage → generate → save).
	// Complements the MemoryStore: this generates memory files, MemoryStore indexes them.
	mc := NewMemoryGenerator(a)
	a.memoryGenerator = mc
	go mc.Run(a.ctx)
	a.ensureActivityTriagerStarted()
	a.deferLaunchTask("know_how_backfill", func() { a.runKnowHowKindBackfill(a.ctx) })
	// One-time: hide memories and know-how learned only from agent activity.
	// It reads session files, so it runs off the launch path.
	a.deferLaunchTask("agent_activity_cleanup", func() { go a.runAgentActivityCleanup(a.ctx) })

	// Memory Dream consolidator: a nightly cycle that archives exact
	// duplicates, expired temporaries, and near-duplicate clusters of
	// memory files. Disabled by default; the user opts in from the
	// memory page. The service tolerates a nil memoryStore (will only
	// run L1 rule-based passes until embeddings are available).
	dream := NewMemoryDreamService(a)
	a.memoryDream = dream
	dream.Start(a.ctx)

	// Self Dream periodically re-evaluates the evidence-backed canonical Self.
	// Its production path is shadow-only unless real paired evaluations and an
	// audited rollout event enable exact-duplicate promotion.
	selfDream := NewSelfDreamService(a)
	a.selfDream = selfDream
	selfDream.Start(a.ctx)
	a.portraitService().Start(a.ctx)
	// Page projections are built by one scheduled job each (derived_views.go).
	derived := a.derivedViews()
	derivedViewsLive.Store(derived)
	derived.Start(a.ctx)

	// Start the composer-suggestions pipeline — hourly LLM-generated chips
	// shown under the empty-state composer. Safe to start unconditionally;
	// it no-ops when store/activity are not yet populated.
	sc := NewSuggestionGenerator(a)
	a.suggestionGenerator = sc
	go sc.Run(a.ctx)

	// Maintain the handoff store already published to first-screen readers;
	// reopening it here would replace the store during concurrent UI reads.
	if a.handoffStore != nil {
		go a.runHandoffLifecycle(a.ctx)
	}

	// Open the SOP bundle store (~/.yansu-agent/sops). Best-effort: a nil
	// store just disables the SOP surfaces (no lifecycle GC — SOPs are
	// durable process definitions with no stale state).
	if home, herr := flavorDataDir(); herr == nil {
		if ss, serr := sop.NewStore(filepath.Join(home, "sops")); serr == nil {
			a.sopStore = ss
			if plans, perr := sop.NewPlanStore(ss.Dir()); perr == nil {
				a.playbookPlans = plans
			} else {
				log.Printf("[playbook] plans init failed: %v", perr)
			}
		} else {
			log.Printf("[sop] store init failed: %v", serr)
		}
		// Proposed know-how drafts live in their own bundle store: same SOP
		// format and scope metadata, never listed under Playbooks.
		if cs, cerr := sop.NewStore(filepath.Join(home, "know-how-candidates")); cerr == nil {
			a.knowHowCandidates = cs
		} else {
			log.Printf("[know-how] candidates store init failed: %v", cerr)
		}
	}

	// Write default SOUL.md if it doesn't exist yet (first launch).
	a.ensureDefaultSoulFile()
	a.initPurposeUnderstanding()

	// Post-model cron hygiene: handoff recovery and transcript backfill rely
	// on a.activity / memory state that wasn't ready when startCronScheduler()
	// ran before waiting for model initialization. Scheduler itself is already up at this point.
	if a.store != nil {
		// Recover initiative executions interrupted by a crash. Recovery returns
		// them to approved so a future execution controller can retry explicitly;
		// it never resumes side effects during startup.
		if a.activity != nil && a.activity.store != nil {
			if err := a.activity.store.RecoverInterruptedInitiatives(60 * time.Second); err != nil {
				log.Printf("[initiative] startup recovery failed: %v", err)
			}
		}

		// Backfill transcript chat sessions created before transcripts
		// became chat-persisted. Runs synchronously so the frontend's
		// LoadAllProjects call sees the inserted first-message. Safe to
		// re-run; no-ops when the first assistant message already exists.
		a.backfillTranscriptMessages(context.Background())

		// Meeting notes live with their meetings on Activity: move the old
		// Knowledge entries once, before recovery, so a recovered part joins
		// its moved meeting.
		a.moveMeetingNotesToActivity(context.Background())

		// Recover transcript files that were saved to disk but whose notes
		// were never saved (e.g. app shut down mid-meeting and the database
		// was already closed). Safe to re-run; skips parts already in the
		// notes and meetings the user deleted.
		a.deferLaunchTask("orphan_transcripts", func() { a.recoverOrphanTranscripts(a.ctx) })

		// Learn from recent notes a crash or a newer part left unlearned.
		a.deferLaunchTask("meeting_learning", func() { a.learnPendingMeetingNotes(a.ctx) })
	}

	// Start the self-update checker (3 s after launch, then every 30 min).
	a.initUpdater()

	// Bring up the user-installed MCP supervisor. It auto-starts every
	// Enabled+AutoStart server in the background; tools become visible
	// to the agent once StartAgent links the bridge to the runner's
	// registry. See mcp_hub_runtime.go for the lifecycle shape.
	a.initMcpRuntime()

	// Background understanding (bounded learning missions, including Gmail
	// reads through Computer Use) was a Desk feature whose only controls were
	// on the Desk. It no longer starts now that the Desk is gone; every reader
	// of a.backgroundUnderstanding treats nil as "not running".

	// Mobile devices receive only explicit product APIs through a separate
	// authenticated transport, after the backing stores are initialized.
	a.mobileBridgeMu.Lock()
	if a.mobileBridge == nil && !a.mobileBridgeStopping {
		if bridge, err := startConfiguredMobileBridge(a); err != nil {
			log.Printf("[mobile-bridge] startup skipped: %v", err)
		} else {
			a.mobileBridge = bridge
		}
	}
	a.mobileBridgeMu.Unlock()
	a.startMobileLifecycle(ctx)
	briefService := a.ensureMobileBriefService()
	// Enrollment and pacing must be ready before a launch brief can run.
	a.startEngagement(ctx)
	a.recoverOpenAnswerChecks()
	if briefService != nil {
		briefService.Start(ctx)
		// Today may have read before the service was up: read again.
		a.emitBrowserEvent("brief:stored", "")
	}

	// New finds on the user's topics, once a day after the morning brief.
	a.startTopics(ctx)
	a.startMainChatTopicBackfill(ctx)
	// The interests behind the topics, refreshed a few times a day.
	a.interestsSvc().start(ctx)
	// The day ahead: calendars are read once access is granted (never asked
	// for here), then every fifteen minutes, on change and on wake.
	a.startCalendarSync(ctx)
	a.startHoldWorker(ctx)
	a.startCalendarPeek(ctx)
	a.startRemindersSync(ctx)
	a.startClipboardObserver(ctx)
	a.startBrowserHistoryReader(ctx)
	// Playbooks: notices a site opened at the same time most days and offers
	// to take it on (playbook_routine.go).
	a.startPlaybookRoutines(ctx)
	a.startFilesActivityReader(ctx)
	a.startMailSync(ctx)
	a.startContextSignals(ctx)
	// The menu bar's "doing now" line follows the state started above.
	a.startDoingNow(ctx)
	// Handoffs interrupted by the last exit are announced only once the
	// Engagement service can deliver the moment.
	a.recoverInterruptedHandoffs()
	a.pruneModelCalls()

	// Signal the frontend that all stores (activity, memory) are
	// initialized and ready to query. Without this the frontend may call
	// GetActivitySessions before startup completes
	// and render a false-empty state.
	a.emitBrowserEvent("app:ready")
	a.deferLaunchTask("brief_refresh", func() {
		if source := a.mobileBriefSource(); source != nil {
			_, _ = source.LatestMobileBrief()
			if a.launch != nil {
				a.launch.storedReads.Store(false)
			}
			a.emitBrowserEvent("brief:stored", "")
		}
	})
	a.deferLaunchTask("agent_probe", func() {
		if _, err := a.StartAgent(); err != nil {
			a.emitAgentStatus(AgentStatus{Running: false, Ready: false, Message: err.Error()})
			return
		}
		a.probeAndGreet()
	})

	a.pageFirstReadsReady()
}

// handleSingletonMessages drains the IPC channel populated by second-launch
// attempts. Currently the only message we ship is "focus", which brings the
// existing window to the front — matching the Slack / Discord / VS Code
// behaviour of "double-click the icon → see the running window".
//
// Reuses the same activate path the system tray "Open Isoform" entry uses
// (switchToRegularApp + WindowShow + WindowUnminimise) so focus stealing
// works correctly on macOS 14+'s Cooperative Activation rules.
func (a *App) handleSingletonMessages() {
	for msg := range a.singletonMessages {
		switch msg {
		case "focus":
			if a.ctx == nil {
				continue
			}
			switchToRegularApp() // no-op on non-darwin
			runtime.WindowShow(a.ctx)
			runtime.WindowUnminimise(a.ctx)
		default:
			log.Printf("[singleton] unknown IPC message: %q", msg)
		}
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.handoffShuttingDown.Store(true)
	// Before any teardown and before telemetry starts its final flush, so the
	// quit install and its event are sent with the rest.
	a.installStagedUpdateOnQuit()
	// Send while the relay is still connected. Shutdown must remain bounded.
	done := make(chan struct{})
	go func() {
		defer close(done)
		cause := "closing"
		if a.mobilePoweringOff.Load() {
			cause = "shutting_down"
		}
		a.mobileRelayGoodbye(cause)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
	// Fence timed resumes before any service teardown; keep the deadline.
	a.stopObservationResumeTimer()
	a.stopMainChatWaitingLifecycle()
	a.SetSetupWelcomeVisible(false)
	var modelDownloadsDone <-chan struct{}
	if a.setupModels != nil {
		modelDownloadsDone = a.setupModels.Stop()
	}
	// Start the bounded flush alongside service teardown. The window callback
	// never waits on HTTP; shutdown joins only after the other cleanup work.
	telemetryDone := a.telemetry.BeginClose()
	defer func() { <-telemetryDone }()
	// Watchdog: force-exit after 5s if any service Stop blocks. Symptom
	// without this is `Yansu.exe` lingering in Task Manager after the
	// window closes — Wails' OnShutdown returns but a stuck cgo thread
	// (sherpa-onnx, sqlite, webview2) or unresponsive goroutine keeps
	// the process alive. The 5s budget is well clear of normal shutdown
	// latency (last good run finished every Stop within ~250ms) so a
	// healthy quit never hits the watchdog.
	go func() {
		time.Sleep(5 * time.Second)
		log.Printf("[shutdown] watchdog: 5s elapsed, forcing exit")
		// os.Exit leaves a bundled magpie that is slow to stop running with
		// no parent, so kill it first.
		if a.magpie != nil {
			a.magpie.KillNow()
		}
		os.Exit(0)
	}()
	launchDone := a.launch.cancelPending()
	var servicesDone <-chan struct{}
	if a.storageStartup != nil {
		servicesDone = a.storageStartup.stop()
	}
	// End the research CLIs first: nothing later can leave them running
	// past a forced exit.
	stopResearchProcesses()
	// Write the replies in flight next: stopping the model gateway below
	// can take long enough for the watchdog to exit before StopAgent.
	a.flushAndStopPageFirstReads(modelDownloadsDone)
	// Flush capture before a native warmup can consume the shutdown budget.
	// The idempotent stops below run again after the join to fence a warmup
	// that was already starting listening when cancellation arrived.
	a.stopBackgroundListening()
	a.stopActivityMonitor()
	if servicesDone != nil {
		<-servicesDone
	}
	if launchDone != nil {
		<-launchDone
	}
	// Stop remote reads and decisions before closing the underlying stores.
	a.stopContactsSync()
	if a.topics != nil {
		a.topics.cancelRegistryWorker()
	}
	a.mobileBridgeMu.Lock()
	a.mobileBridgeStopping = true
	mobile := a.mobileBridge
	desktopTasks := a.desktopTaskBridge
	a.mobileBridge = nil
	a.desktopTaskBridge = nil
	a.mobileBridgeMu.Unlock()
	if mobile != nil {
		mobile.Stop()
	}
	if desktopTasks != nil {
		desktopTasks.Stop()
	}
	a.stopDoingNow()
	a.stopContextSignals()
	a.stopCalendarPeek()
	a.stopCalendarSync()
	a.stopRemindersSync()
	a.stopClipboardObserver()
	a.stopBrowserHistoryReader()
	a.stopFilesActivityReader()
	a.stopMailSync()
	a.stopEngagement()
	// Stop the magpie gateway child (SIGTERM, then kill after a grace period).
	if a.magpie != nil {
		a.magpie.Stop()
	}
	a.mobileBridgeMu.Lock()
	briefSvc := a.mobileBriefSvc
	a.mobileBriefSvc = nil
	a.mobileBridgeMu.Unlock()
	if briefSvc != nil {
		briefSvc.Stop()
	}

	// Remove menu bar icon.
	cleanupMenuBar()

	// Stop the Spotlight helper before tearing down anything
	// else; the helper's HTTP shutdown handshake is fast (<1.5s) and
	// we want it gone before we stop the host bridge it talks to.
	if a.spotlight != nil {
		a.spotlight.Stop()
	}

	// Stop the bundled cua-driver daemon. Graceful first, SIGTERM
	// fallback if it doesn't respond — leaving a stray daemon would tie
	// up the Unix socket and confuse the next launch.
	if a.cuaDriver != nil {
		a.cuaDriver.Stop(ctx)
	}

	// Stop Slack incremental scanner.
	if a.slackScanner != nil {
		a.slackScanner.Stop()
	}

	// Stop Feishu incremental scanner.
	if a.feishuScanner != nil {
		a.feishuScanner.Stop()
	}

	// Stop Telegram incremental scanner.
	if a.telegramScanner != nil {
		a.telegramScanner.Stop()
	}

	// Stop ambient Purpose understanding before its model, activity-store, and
	// Agency dependencies. Stop cancels the active no-tools call and waits for
	// deterministic reconciliation to settle.
	a.stopPurposeUnderstanding()

	// Stop background understanding (when one was set) before its
	// Memory/model and MCP dependencies. Stop cancels the active mission and
	// waits for its receipt lifecycle to settle.
	a.backgroundUnderstandingMu.RLock()
	backgroundUnderstanding := a.backgroundUnderstanding
	a.backgroundUnderstandingMu.RUnlock()
	if backgroundUnderstanding != nil {
		backgroundUnderstanding.Stop()
	}

	// Stop generative memory pipeline.
	if a.memoryGenerator != nil {
		a.memoryGenerator.Stop()
	}

	// Stop Memory Dream consolidator. Stop() cancels the daily-tick loop
	// and signals any in-flight cycle to abort; the cycle will finish
	// its current layer and return without partial-applying L3 merges.
	if a.memoryDream != nil {
		a.memoryDream.Stop()
	}
	if a.aboutYouPortrait != nil {
		a.aboutYouPortrait.Stop()
	}
	a.mu.Lock()
	derived := a.derived
	a.mu.Unlock()
	if derived != nil {
		derived.Stop()
		derivedViewsLive.CompareAndSwap(derived, nil)
	}
	if a.selfDream != nil {
		a.selfDream.Stop()
	}

	// Stop composer-suggestions pipeline.
	if a.suggestionGenerator != nil {
		a.suggestionGenerator.Stop()
	}
	// Close memory store.
	if a.memoryStore != nil {
		a.memoryStore.Close()
	}

	// Stop user-installed MCP servers. We do this before stopping
	// activity monitoring because some MCP servers may be reading from
	// the activity store via tool calls; killing them first avoids
	// transient "store closed" errors at the tail of shutdown.
	a.shutdownMcpRuntime()

	// Stop background listening before activity monitor.
	a.stopBackgroundListening()

	// Stop whisper-server.
	if a.whisper != nil {
		a.whisper.mu.Lock()
		srv := a.whisper.server
		a.whisper.server = nil
		a.whisper.mu.Unlock()
		if srv != nil {
			srv.Stop()
		}
	}

	// Stop activity monitoring before other cleanup.
	a.stopActivityMonitor()

	if a.topics != nil {
		a.topics.stopRegistryWorker()
		if a.topics.privacyWithdrawalUnsubscribe != nil {
			a.topics.privacyWithdrawalUnsubscribe()
			a.topics.privacyWithdrawalUnsubscribe = nil
		}
	}

	a.stopHoldWorker()

	// Close the activity store (kept open after stopActivityMonitor for queries).
	if a.activity != nil && a.activity.store != nil {
		a.activity.store.Close()
	}

	if a.browserServer != nil {
		a.browserServer.Stop()
	}
	if a.bridge != nil {
		a.bridge.Stop()
	}
	a.StopDaemon()
	a.StopFeishuDaemon("feishu")
	a.StopFeishuDaemon("lark")
	a.StopDiscordDaemon()
	if a.discordScanner != nil {
		a.discordScanner.Stop()
	}
	if a.sshMgr != nil {
		a.sshMgr.Shutdown()
	}
	// Stop cron API server and scheduler before store.Close().
	a.runTokens.Close()
	if a.cronAPI != nil {
		a.cronAPI.Stop()
	}
	if a.cronScheduler != nil {
		a.cronScheduler.Stop()
	}
	if a.termMgr != nil {
		a.termMgr.CloseAll()
	}
	_ = a.StopAgent()
	// Write what the backend still has to grow, and nothing after.
	a.flushAgentSegmentWrites(true)
	if a.store != nil {
		a.store.Close()
	}

	a.cleanupFallbackSessionDirsOnShutdown()
}

// WindowState represents saved window position and size.
// Maximised is tracked so the window can restore to the maximised state
// instead of restoring a stale geometry captured right before maximise.
type WindowState struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximised bool `json:"maximised"`
}

// CaptureAndSaveWindowState takes one platform-safe snapshot for frontend
// resize/unload events, rather than letting JavaScript fan out native getters.
func (a *App) CaptureAndSaveWindowState() {
	a.captureAndSaveWindowState(a.ctx)
}

// SaveWindowState persists the window position and size
func (a *App) SaveWindowState(state WindowState) error {
	if a.store == nil {
		return nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return a.store.SetUIState(context.Background(), "window_state", string(data))
}

// LoadWindowState retrieves the saved window position and size
func (a *App) LoadWindowState() (*WindowState, error) {
	if a.store == nil {
		return nil, nil
	}
	data, err := a.store.GetUIState(context.Background(), "window_state")
	if err != nil || data == "" {
		return nil, err
	}
	var state WindowState
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// captureAndSaveWindowState queries the live window geometry and persists it.
// On macOS the snapshot is read entirely on the AppKit main thread.
// Called from OnBeforeClose (and on shutdown) so
// state is captured even when the browser `beforeunload` event doesn't fire —
// which is the common case on Windows, where X-to-tray hides the window
// without unloading the webview and process exit from the tray menu also
// skips beforeunload.
func (a *App) captureAndSaveWindowState(ctx context.Context) {
	if a.store == nil || ctx == nil {
		return
	}
	// The setup window's size is not the person's main window size.
	if setupWindowMode.isActive() {
		return
	}
	state, ok := readWindowState(ctx)
	if !ok {
		return
	}
	// Ignore obviously bogus values (Windows reports (-32000,-32000) and
	// tiny size for minimised windows even when IsMinimised returns false
	// in some edge cases).
	if state.X <= -30000 || state.Y <= -30000 {
		return
	}
	if !state.Maximised && (state.Width < 200 || state.Height < 200) {
		return
	}
	if err := a.SaveWindowState(state); err != nil {
		log.Printf("[window-state] save failed: %v", err)
	}
}

// restoreWindowPosition restores geometry once asynchronous storage startup
// finishes; the progress window opens at a default size without waiting on DB I/O.
func (a *App) restoreWindowPosition(ctx context.Context) {
	// During setup the window keeps the setup size; leaving setup restores the frame.
	if setupWindowMode.isActive() {
		return
	}
	state, err := a.LoadWindowState()
	if err != nil || state == nil {
		return
	}
	if state.Maximised {
		runtime.WindowMaximise(ctx)
		return
	}
	if state.Width >= 400 && state.Height >= 300 {
		runtime.WindowSetSize(ctx, state.Width, state.Height)
	}
	if state.X <= -30000 || state.Y <= -30000 {
		return
	}
	if state.Width < 200 || state.Height < 200 {
		return
	}
	runtime.WindowSetPosition(ctx, state.X, state.Y)
}

// buildAgentRunner gives every task the user's ordered assistant candidates,
// with Isoform's native loop as the final backup. Each CLI uses only the
// per-run server backed by the native tool gate.
func (a *App) buildAgentRunner() agent.AgentRunner {
	// The Yansu gateway is retired, so BYOA/BYOK is unconditionally allowed
	// (byoaEntitled() is always true). byoaAllowed is kept as a named flag
	// because the external-agent and API-BYOA branches below read it; the
	// former "configured but not entitled → fall back to gateway" leg is gone
	// since there is no gateway to fall back to.
	byoaAllowed := a.byoaEntitled()

	externalAllowed := byoaAllowed

	runnerCfg := agent.NativeRunnerConfig{
		// Short alias for the default; the runner resolves it via
		// ModelAliases below. Using the alias means env-var overrides
		// (ANTHROPIC_DEFAULT_*_MODEL) and model bumps in
		// claude_model_config.go automatically flow through.
		// ChatModel (not CodeModel): an unset TaskInput.Model must never
		// silently escalate to the most expensive tier — call sites that
		// genuinely need the code model pass it explicitly.
		DefaultModel: a.chatTaskModel(),
		// Short-name → pinned API id. The frontend's model selector
		// stores these short names; the runner rewrites them before
		// dispatch so every call site (streaming, cron, ...)
		// gets the same treatment without repeating the mapping.
		ModelAliases: claudeAPIModelAliases(),
		// Without this, each turn is a fresh call with no memory of
		// prior messages — the model would forget it just wrote a file
		// the user is now asking about.
		History: &streamItemHistoryLoader{store: a.store, loadRows: a.modelChatHistoryRows},
		// PostHog-backed observability so agent.loop.started /
		// agent.tool_pairing.detected / agent.loop.completed events are
		// queryable end-to-end alongside the trace_id that the frontend
		// generates per user send.
		Telemetry:         a.telemetry,
		AuthorityRecorder: a.recordAuthorityEvaluation,
	}

	a.backgroundUsesChoice.Store(loadAIProviderConfig().BackgroundUsesChoice)

	// Managed mode: model calls go to the magpie sidecar, which routes each
	// group to a subscription, key or local model. The sidecar speaks the
	// Anthropic Messages API, so the native client needs no other change. If
	// the sidecar is not running, the runner keeps the same no-endpoint
	// configuration as before and logs once.
	// The sidecar also serves the native loop when a CLI is the provider:
	// assistant attempts may fall back before any action, so the backup
	// still needs its own model endpoint.
	if a.modelSidecarActive() {
		base := ""
		if a.magpie != nil {
			base = a.magpie.BaseURL()
		}
		if base != "" {
			targets := a.magpieRouteTargets()
			applyManagedRunnerConfig(&runnerCfg, base, targets)
			a.configureMagpieRunChoices(&runnerCfg)
			log.Printf("[agent-start] model calls via magpie at %s (provider %s)", base, loadAIProviderConfig().normalizedProvider())

		} else {
			a.magpieFallbackOnce.Do(func() {
				log.Printf("[agent-start] managed model accounts unavailable (magpie not running); agent has no model endpoint until it starts")
			})
		}
	} else if bcfg, ok := aiProviderActive(); ok && byoaAllowed {
		// Advanced path: the user's own key and endpoint. The frontend model
		// selector and the internal light-task callers emit short names
		// ("sonnet"/"opus"/"haiku"), so map them onto the user-chosen models:
		// chat/code roles -> Model (big-brain); the haiku/light role ->
		// LightModel when set, else Model (small-brain falls back to big-brain,
		// so single-model configs are unchanged).
		runnerCfg.ProviderFormat = bcfg.normalizedProvider()
		runnerCfg.BaseURL = strings.TrimSpace(bcfg.BaseURL)
		runnerCfg.APIKey = strings.TrimSpace(bcfg.APIKey)
		// The user's config is the sole source of truth — never let an empty
		// BaseURL/AuthToken fall back to the gateway env (which may have been
		// injected before BYOA was resolved). An empty BaseURL means the
		// provider default (api.anthropic.com / api.openai.com), not Yansu.
		runnerCfg.DisableEnvFallback = true
		runnerCfg.ModelEffort = func(model, priority string) string {
			if strings.Contains(strings.ToLower(model), "haiku") || (priority == agent.PriorityBackground && !a.backgroundUsesChoice.Load()) {
				return ""
			}
			return loadAIProviderConfig().agentChoice(answeringIsoform).Effort
		}
		if m := strings.TrimSpace(bcfg.Model); m != "" {
			light := bcfg.lightModelOrChat() // == m when LightModel unset
			runnerCfg.DefaultModel = m
			runnerCfg.ModelAliases = map[string]string{
				"sonnet": m,
				"opus":   m,
				"haiku":  light,
			}
		}
		log.Printf("[agent-start] BYOA active: provider=%s base=%q model=%q lightModel=%q",
			runnerCfg.ProviderFormat, runnerCfg.BaseURL, runnerCfg.DefaultModel, bcfg.lightModelOrChat())
	}

	// The Yansu gateway is retired, so every build must refuse to fall back to
	// env-injected gateway credentials (ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN
	// that auth.go may still inject). Without a configured BYOA provider the
	// runner has no usable endpoint and AI calls fail until the user configures
	// one — the retirement banner guides them there. This mirrors the former
	// standalone-only guard, now applied universally.
	runnerCfg.DisableEnvFallback = true

	return a.buildAssistantRoutingRunner(agent.NewNativeAgentRunner(runnerCfg), externalAllowed)
}

func (a *App) StartAgent() (AgentStatus, error) {
	if a.launch != nil && !a.launch.envReady.Load() {
		return AgentStatus{Message: "Agent is starting..."}, nil
	}
	startedAt := time.Now()

	a.mu.Lock()
	if a.runner != nil && a.ready {
		status := a.statusLocked()
		a.mu.Unlock()
		if a.startPurposeUnderstanding() && a.claimDerivedRun("purpose_understanding", cadencePurposeUnderstanding, time.Now()) {
			a.triggerPurposeUnderstanding(PurposeUnderstandingTriggerStartup)
		}
		return status, nil
	}
	if a.starting {
		a.mu.Unlock()
		return AgentStatus{Running: false, Ready: false, Message: "Agent is starting..."}, nil
	}
	a.starting = true
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		a.starting = false
		a.mu.Unlock()
	}()

	// Managed mode needs the magpie gateway answering before the runner is
	// built, so its groups match the current consent. Other modes stop it.
	a.syncMagpieSidecar()
	runner := a.buildAgentRunner()
	if err := runner.Init(a.ctx); err != nil {
		return AgentStatus{}, fmt.Errorf("initialize agent: %w", err)
	}

	a.mu.Lock()
	a.runner = runner
	workProfileKick()
	a.setReadyLocked(true)
	a.mu.Unlock()
	readyAt := time.Now()
	log.Printf("[agent-start] runner ready in %s", readyAt.Sub(startedAt).Round(time.Millisecond))

	// Wire background-shell-done callback so the app is notified when
	// scripts started via Bash(run_in_background=true) finish.
	a.wireBgShellCallback()

	// Wire the MCP bridge to the freshly-built runner registry. Any
	// MCP servers that auto-started before this point get their tools
	// registered now. Errors are logged inside; we don't fail
	// StartAgent because the chat itself is fully functional without
	// MCP tools.
	a.linkMcpBridge()
	log.Printf("[agent-start] MCP bridge linked in %s", time.Since(readyAt).Round(time.Millisecond))

	status := a.GetStatus()
	a.emitAgentStatus(status)
	if a.startPurposeUnderstanding() && a.claimDerivedRun("purpose_understanding", cadencePurposeUnderstanding, time.Now()) {
		a.triggerPurposeUnderstanding(PurposeUnderstandingTriggerStartup)
	}
	return status, nil
}

// RestartAgent stops and re-initializes the in-process agent runner.
// Exposed to the frontend for manual retry after crash recovery exhaustion.
// Checks the `starting` flag to prevent concurrent restart attempts from
// creating duplicate runner instances.
func (a *App) RestartAgent() (AgentStatus, error) {
	a.mu.Lock()
	if a.starting {
		a.mu.Unlock()
		return AgentStatus{Running: false, Ready: false, Message: "Agent is restarting..."}, nil
	}
	a.mu.Unlock()

	_ = a.StopAgent()
	return a.StartAgent()
}

func (a *App) StopAgent() error {
	a.bgShells.Clear()

	a.mu.Lock()
	runner := a.runner
	streamCount := len(a.streams)
	tagCount := len(a.tagCancels)
	a.runner = nil
	a.httpPort = 0
	a.setReadyLocked(false)
	for runID, cancel := range a.streams {
		cancel()
		delete(a.streams, runID)
	}
	for tag, cancel := range a.tagCancels {
		cancel()
		delete(a.tagCancels, tag)
	}
	a.mu.Unlock()
	// Write the cancelled turns' replies before the runner's shutdown, which
	// can take seconds.
	a.flushAgentSegmentWrites(false)

	if runner != nil {
		startedAt := time.Now()
		log.Printf("[agent-stop] shutdown starting: runner=%T streams=%d tags=%d", runner, streamCount, tagCount)

		shutdownDone := make(chan struct{})
		go func() {
			select {
			case <-time.After(5 * time.Second):
				buf := make([]byte, 512*1024)
				n := goruntime.Stack(buf, true)
				log.Printf("[agent-stop] shutdown still blocked after 5s; goroutine dump follows (bytes=%d truncated=%v)\n%s",
					n, n == len(buf), buf[:n])
			case <-shutdownDone:
			}
		}()

		_ = runner.Shutdown()
		close(shutdownDone)
		log.Printf("[agent-stop] shutdown completed in %s", time.Since(startedAt))
	}

	a.emitAgentStatus(a.GetStatus())
	return nil
}

// setReadyLocked updates the ready state and signals any waiters via readyCh.
// Must be called with a.mu held.
func (a *App) setReadyLocked(ready bool) {
	if ready == a.ready {
		return
	}
	a.ready = ready
	if ready {
		close(a.readyCh)
	} else {
		a.readyCh = make(chan struct{})
	}
}

func (a *App) GetStatus() AgentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.statusLocked()
}

func (a *App) statusLocked() AgentStatus {
	running := a.runner != nil
	return AgentStatus{
		Running: running,
		Ready:   a.ready,
		Port:    a.httpPort,
	}
}

// RespondToPermission forwards the user's choice for an interactive
// permission request (ExitPlanMode / AskUserQuestion) to the blocked runner.
// sessionID is the owning agent session ID for ordinary chats. Work controls
// pass their stamped permission request ID so a stale click cannot answer a
// later request in the same session.
func (a *App) RespondToPermission(sessionID, optionID string) error {
	return a.respondToWorkPermission(sessionID, optionID, "")
}

// CancelPermission cancels a pending interactive permission request (skip)
// without cancelling the entire run, allowing the agent to continue.
func (a *App) CancelPermission(sessionID string) error {
	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	if runner == nil {
		return fmt.Errorf("runner not initialized")
	}
	runner.CancelPermission(sessionID)
	return nil
}

// errRunnerNotInitialized means the model runner has not started yet (startup).
// A scheduled brief waits for it rather than counting a failure.
var errRunnerNotInitialized = errors.New("runner not initialized")

// FireDelayedDesktopNotification fires a native desktop toast after a 5s delay
// from a background goroutine, so the window can be closed to the menu bar in
// the meantime. Diagnostic tool (Dev Tools > Notifications) for reproducing the
// "a background toast surfaces the hidden main window" bug: the delay runs in Go
// rather than a WebView setTimeout, which macOS throttles once the window is
// hidden. Watch whether the main window surfaces when the toast fires.
func (a *App) FireDelayedDesktopNotification(title, body string) error {
	go func() {
		time.Sleep(5 * time.Second)
		log.Printf("[notify] firing delayed desktop toast (window-pop repro)")
		showDesktopToast(title, body, a.notifyIconPath(), "")
	}()
	return nil
}

// NotifyUser delivers a desktop toast with optional click-routing data.  When
// the user clicks the toast body, a "notification:clicked" Wails event is
// emitted carrying clickData so the frontend can navigate to the relevant
// content.  Always returns "desktop" for caller-side routing; note the in-house
// toast is suppressed on macOS when the user has turned Yansu's notifications OFF
// in System Settings, and the non-darwin fallback delegates to the OS notifier.
func (a *App) NotifyUser(title, body, clickData string) string {
	// Newly observed knowledge and beliefs are proactive, not a response to a
	// button press. They share the automatic notification budget.
	if strings.HasPrefix(clickData, "memory:") || strings.HasPrefix(clickData, "knowledge:") {
		return a.notifyAutomaticDesktop(title, body, clickData)
	}
	var allowed bool
	title, body, allowed = a.notificationForDelivery(title, body, clickData)
	if !allowed {
		return "suppressed"
	}
	showDesktopToast(title, body, a.notifyIconPath(), clickData)
	return "desktop"
}

// TestDesktopNotification triggers a sample toast for QA and permission
// verification.  Useful from the in-app settings screen to confirm toasts are
// being rendered correctly.
func (a *App) TestDesktopNotification() error {
	// A user clicking "Test" wants confirmation either way. If they've turned
	// Yansu's notifications OFF in System Settings, report that instead of
	// silently dropping the toast (showDesktopToast would suppress it), so the
	// settings UI can point them at the system toggle.
	if notificationExplicitlyDenied() {
		return fmt.Errorf("notifications are turned off for Isoform in System Settings")
	}
	showDesktopToast(productName(), "Desktop notifications are working.", a.notifyIconPath(), "")
	return nil
}

// ensureNotifyIcon extracts the embedded app icons to ~/.yansu-agent/
// so desktop notifications can reference them.
func (a *App) ensureNotifyIcon() {
	home, err := flavorDataDir()
	if err != nil {
		return
	}
	dir := home
	_ = os.MkdirAll(dir, 0o755)

	// Extract PNG (used on macOS / Linux).
	pngDst := filepath.Join(dir, "appicon.png")
	if info, err := os.Stat(pngDst); err != nil || info.Size() != int64(len(embeddedAppIcon)) {
		if err := os.WriteFile(pngDst, embeddedAppIcon, 0o644); err != nil {
			log.Printf("[notify] failed to extract app icon (png): %v", err)
		}
	}

	// Extract ICO (required on Windows — System.Drawing.Icon only supports .ico).
	icoDst := filepath.Join(dir, "appicon.ico")
	if info, err := os.Stat(icoDst); err != nil || info.Size() != int64(len(embeddedAppIconICO)) {
		if err := os.WriteFile(icoDst, embeddedAppIconICO, 0o644); err != nil {
			log.Printf("[notify] failed to extract app icon (ico): %v", err)
		}
	}
}

// notifyIconPath returns the absolute path to the app icon used for desktop
// notifications. On Windows it prefers .ico (required by System.Drawing.Icon);
// on other platforms it uses .png.
func (a *App) notifyIconPath() string {
	if home, err := flavorDataDir(); err == nil {
		// Prefer .ico (required by Windows System.Drawing.Icon).
		ico := filepath.Join(home, "appicon.ico")
		if _, err := os.Stat(ico); err == nil {
			return ico
		}
		// Fallback to .png (macOS / Linux).
		png := filepath.Join(home, "appicon.png")
		if _, err := os.Stat(png); err == nil {
			return png
		}
	}
	return "" // no icon found — the OS will use its default
}

// buildLocalID generates a unique ID matching the frontend's buildId() format.
func buildLocalID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b))
}

// formatAutomationNotification builds a branded notification message for automation runs.
func formatAutomationNotification(automationName, runStatus string, runResult string) string {
	statusLabel := "Completed"
	switch runStatus {
	case "error", "failed":
		statusLabel = "Failed"
	case "skipped":
		statusLabel = "Skipped"
	}
	return fmt.Sprintf("[Isoform Automation] %s — %s\n\n%s", automationName, statusLabel, runResult)
}

// sendLocalWeChatDM attempts to send a WeChat DM locally.
// Returns a JSON result string on success, or "" if WeChat is unavailable or fails.
func (a *App) sendLocalWeChatDM(automationName, runStatus, runResult string) string {
	creds := loadWeChatCredentials()
	if creds == nil || creds.UserID == "" {
		return ""
	}
	api := newWeChatAPIClient(creds.BaseURL, creds.BotToken)
	msg := formatAutomationNotification(automationName, runStatus, runResult)
	if err := sendWeChatText(api, creds.UserID, "", msg); err != nil {
		log.Printf("[cron] notify: WeChat DM failed: %v", err)
		return ""
	}
	log.Printf("[cron] notify: WeChat DM sent to %s", creds.UserID)
	wechatResultJSON, _ := json.Marshal(map[string]any{
		"sent":     true,
		"platform": "wechat",
	})
	return string(wechatResultJSON)
}

// sendLocalWeChatDMWithFiles sends the text notification first, then attempts
// each deliverable as a standalone WeChat file message. A file failure does
// not negate the already-sent text notification; callers inspect the returned
// attachment counts/errors for partial success.
func (a *App) sendLocalWeChatDMWithFiles(automationName, runStatus, runResult string, files []deliverableFile) string {
	creds := loadWeChatCredentials()
	if creds == nil || creds.UserID == "" {
		return ""
	}
	api := newWeChatAPIClient(creds.BaseURL, creds.BotToken)
	msg := formatAutomationNotification(automationName, runStatus, runResult)

	if err := sendWeChatText(api, creds.UserID, "", msg); err != nil {
		log.Printf("[cron] notify: WeChat DM failed: %v", err)
		return ""
	}

	attachmentsSent := 0
	var attachmentErrors []string
	for _, f := range files {
		if err := sendWeChatFile(api, creds.UserID, "", f.Filename, f.Data); err != nil {
			log.Printf("[cron] notify: WeChat attachment failed: user=%s file=%s err=%v", creds.UserID, f.Filename, err)
			attachmentErrors = append(attachmentErrors, fmt.Sprintf("%s: %v", f.Filename, err))
			continue
		}
		attachmentsSent++
	}

	log.Printf(
		"[cron] notify: WeChat DM sent to %s (attachments sent=%d failed=%d)",
		creds.UserID,
		attachmentsSent,
		len(attachmentErrors),
	)

	wechatResultJSON, _ := json.Marshal(map[string]any{
		"sent":              true,
		"platform":          "wechat",
		"attachments_sent":  attachmentsSent,
		"attachment_errors": attachmentErrors,
	})
	return string(wechatResultJSON)
}

func (a *App) emitAgentEvent(evt AgentEvent) {
	if evt.Type == "tool_result" {
		evt.Content = chatContextResultContent(evt.ToolName, evt.Content)
	}
	if evt.EventID == "" {
		evt.EventID = uuid.NewString()
	}
	if a.observeMainChatWorkPermission(&evt) {
		return
	}
	// Scheduled runs store their own rows and name them themselves.
	var completedAssistant *finishedSegment
	if !evt.Persisted {
		completedAssistant = a.agentEventItems.stamp(&evt, a.agentSegmentWriter())
	}
	if completedAssistant != nil {
		a.recordFinishedChatDeliverable(*completedAssistant)
	}
	if a.ctx != nil {
		a.emitBrowserEvent("agent:event", evt)
	}
	// In-process fan-out: lets server-side consumers (e.g. the spotlight
	// helper bridge, future log/audit sinks) observe the same stream
	// without going through the Wails frontend round-trip.
	// Copy the entries under the lock: an unsubscriber rewrites an entry in place.
	a.agentEventListenersMu.RLock()
	listeners := append([]func(AgentEvent){}, a.agentEventListeners...)
	a.agentEventListenersMu.RUnlock()
	for _, l := range listeners {
		if l != nil {
			l(evt)
		}
	}
}

// onAgentEvent registers an in-process listener for agent:event. The
// listener is called synchronously from emitAgentEvent — keep it
// quick (push to a channel, append to a buffer) and never block.
// Returns an unsubscriber.
func (a *App) onAgentEvent(fn func(AgentEvent)) func() {
	a.agentEventListenersMu.Lock()
	a.agentEventListeners = append(a.agentEventListeners, fn)
	idx := len(a.agentEventListeners) - 1
	a.agentEventListenersMu.Unlock()
	return func() {
		a.agentEventListenersMu.Lock()
		defer a.agentEventListenersMu.Unlock()
		if idx < len(a.agentEventListeners) {
			a.agentEventListeners[idx] = nil
		}
	}
}

func (a *App) emitAgentStatus(status AgentStatus) {
	if a.ctx != nil {
		a.emitBrowserEvent("agent:status", status)
	}
}

// startCronScheduler brings up the cron scheduler, REST API and sidebar
// groups. Invoked from startup() before waiting for model initialization so a hang in
// NewMemoryStore or any other model-download goroutine cannot prevent
// automation management from working. Only depends on a.store, which
// NewApp() initialises synchronously.
func (a *App) startCronScheduler() {
	if a.store == nil {
		log.Printf("[cron] store unavailable, cron scheduler disabled")
		return
	}

	if err := a.store.MarkStaleRunsAsError(a.ctx); err != nil {
		log.Printf("[cron] failed to mark stale runs: %v", err)
	}

	a.cronScheduler = NewCronScheduler(
		a.store,
		func(event string, data any) {
			if a.ctx != nil {
				a.emitBrowserEvent(event, data)
			}
		},
		func(title, body string) { a.NotifyUser(title, body, "") },
		a.executeAgentJob,
	)
	a.cronScheduler.SetGenerateName(a.GenerateAutomationName)
	a.cronScheduler.SetCheckDuplicate(a.checkAutomationDuplicate)
	a.cronScheduler.SetRunLifecycle(func(event CronRunLifecycleEvent) {
		a.handleCronRunLifecycle(event)
		a.reportPlaybookRun(event)
	})
	a.cronScheduler.SetOnMissed(a.noteScheduleRunHealth)
	a.cronScheduler.SetTelemetryCapture(func(event string, properties map[string]any) {
		if a.telemetry != nil {
			a.telemetry.Capture(event, properties)
		}
	})
	a.cronScheduler.SetNotifyResult(func(automationName, runResult, runStatus string, durationMs int64, sendDM bool, notifyPlatform string, filePaths []string, deliveryTarget *DeliveryTarget, workDir, sessionTempDir string, writtenFiles []string) string {
		// The authority check on the result, before anything is sent: a
		// delivery target the job was not configured with is refused, and the
		// result falls back to the job's own destination.
		if err := a.authorizeScheduledDelivery(notifyPlatform, sendDM, deliveryTarget); err != nil {
			log.Printf("[cron] notify: %v; using the job's own destination", err)
			deliveryTarget = nil
		}

		// Skip notification if disabled and no prompt-specified delivery target.
		// Delivery target from prompt overrides the "disabled" config.
		if (!sendDM || notifyPlatform == "none") && deliveryTarget == nil {
			return ""
		}

		// Read deliverable files from disk (if any were declared by agent).
		// Only files the run itself wrote, or files inside its work/scratch
		// folder, are eligible for upload — a run cannot ship off an
		// arbitrary home-directory file it merely read.
		var deliverables []deliverableFile
		var skippedFiles []string
		if len(filePaths) > 0 {
			writtenSet := make(map[string]struct{}, len(writtenFiles))
			for _, w := range writtenFiles {
				writtenSet[w] = struct{}{}
			}
			deliverables, skippedFiles = readDeliverableFiles(filePaths, workDir, sessionTempDir, writtenSet)
			log.Printf("[cron] notify: %d deliverables read, %d skipped", len(deliverables), len(skippedFiles))
		}

		// Append skip notes to runResult so they appear in the DM text.
		dmText := runResult
		if note := buildSkipNote(skippedFiles); note != "" {
			dmText += note
		}

		// Route to the local WeChat path in two cases:
		//   1) Job config picks WeChat as the default platform and the
		//      prompt didn't override it (deliveryTarget == nil), or
		//   2) The prompt explicitly targets WeChat via <delivery_target>.
		// Case (2) is critical: WeChat in Yansu is a locally-bridged channel
		// with no backend OAuth integration, so the backend can't actually
		// deliver to it. Honoring a prompt-level WeChat target by routing it
		// through the backend would either round-trip pointlessly (when the
		// backend echoes preferred_platform=wechat back to us) or silently
		// drop the message (when the backend is unreachable). Local-only is
		// the right destination for any "this should go to WeChat" signal.
		// WeChat is DM-only here, so any channel/group field on the target
		// is intentionally ignored.
		if (notifyPlatform == "wechat" && deliveryTarget == nil) ||
			(deliveryTarget != nil && deliveryTarget.Platform == "wechat") {
			if result := a.sendLocalWeChatDMWithFiles(automationName, runStatus, dmText, deliverables); result != "" {
				return result
			}
			return ""
		}

		// Track whether any platform successfully sent a DM.
		var backendSent bool
		var backendResult string

		// Send to backend for integration DMs (Slack/Discord/Telegram/Feishu).
		token, err := loadYansuToken()
		if err != nil || token == "" {
			log.Printf("[cron] notify: no auth token, skipping backend notification")
		} else if baseURL := loadAPIBaseURL(); baseURL == "" {
			log.Printf("[cron] notify: no API base URL, skipping backend notification")
		} else {
			// Build WeChat activity signals.
			wechatFields := map[string]string{}
			if creds := loadWeChatCredentials(); creds != nil && creds.UserID != "" {
				a.mu.Lock()
				wla := a.wechatLastActivity
				a.mu.Unlock()
				if !wla.IsZero() {
					wechatFields["wechat_last_interaction_at"] = wla.Format(time.RFC3339)
				}
				if cfg := loadConfig(); cfg.WeChat != nil && cfg.WeChat.ConnectedAt != "" {
					wechatFields["wechat_connected_at"] = cfg.WeChat.ConnectedAt
				}
			}

			var dmResult json.RawMessage

			if len(deliverables) > 0 {
				// Multipart path: send text fields + file attachments.
				fields := map[string]string{
					"automation_name": automationName,
					"run_result":      dmText,
					"run_status":      runStatus,
					"run_duration_ms": fmt.Sprintf("%d", durationMs),
				}
				if notifyPlatform != "" {
					fields["notify_platform"] = notifyPlatform
				}
				if deliveryTarget != nil {
					fields["delivery_target_platform"] = deliveryTarget.Platform
					fields["delivery_target_channel"] = deliveryTarget.Channel
					if deliveryTarget.Server != "" {
						fields["delivery_target_server"] = deliveryTarget.Server
					}
				}
				for k, v := range wechatFields {
					fields[k] = v
				}
				if err := apiPostMultipart(baseURL, token, "/api/automation-notification", fields, deliverables, &dmResult); err != nil {
					log.Printf("[cron] notify: backend multipart notification failed: %v", err)
				} else {
					backendResult = string(dmResult)
					log.Printf("[cron] notify: DM result (multipart): %s", backendResult)
				}
			} else {
				// JSON path: text-only notification (backward compatible).
				body := map[string]any{
					"automation_name": automationName,
					"run_result":      dmText,
					"run_status":      runStatus,
					"run_duration_ms": durationMs,
				}
				if notifyPlatform != "" {
					body["notify_platform"] = notifyPlatform
				}
				if deliveryTarget != nil {
					dt := map[string]string{
						"platform": deliveryTarget.Platform,
						"channel":  deliveryTarget.Channel,
					}
					if deliveryTarget.Server != "" {
						dt["server"] = deliveryTarget.Server
					}
					body["delivery_target"] = dt
				}
				for k, v := range wechatFields {
					body[k] = v
				}
				if err := apiPost(baseURL, token, "/api/automation-notification", body, &dmResult); err != nil {
					log.Printf("[cron] notify: backend notification failed: %v", err)
				} else {
					backendResult = string(dmResult)
					log.Printf("[cron] notify: DM result: %s", backendResult)
				}
			}

			if backendResult != "" {
				var parsed struct {
					Sent              bool   `json:"sent"`
					ChannelDelivered  bool   `json:"channel_delivered"`
					PreferredPlatform string `json:"preferred_platform"`
				}
				if json.Unmarshal(dmResult, &parsed) == nil {
					backendSent = parsed.Sent || parsed.ChannelDelivered
					if parsed.PreferredPlatform == "wechat" {
						if result := a.sendLocalWeChatDMWithFiles(automationName, runStatus, dmText, deliverables); result != "" {
							return result
						}
						return backendResult
					}
				}
			}
		}

		// WeChat DM fallback: send locally if backend didn't deliver a DM.
		// Skip fallback when user explicitly chose a platform — honor "direct route, no fallback".
		if !backendSent && notifyPlatform == "" {
			if result := a.sendLocalWeChatDMWithFiles(automationName, runStatus, dmText, deliverables); result != "" {
				return result
			}
		}

		return backendResult
	})
	// Playbook schedules saved before steps were pinned are switched off
	// before the jobs load, so none of them fires first.
	if home, err := flavorDataDir(); err == nil {
		if plans, err := sop.NewPlanStore(filepath.Join(home, "sops")); err == nil {
			switchOffUnpinnedPlaybookSchedules(a.ctx, a.store, plans)
		}
	}
	go a.cronScheduler.Run(a.ctx)

	// Start the cron REST API server for CLI access.
	if api, err := newCronAPIServer(a); err != nil {
		log.Printf("[cron-api] failed to start: %v", err)
	} else if err := writeCronAPIFiles(api.Port(), api.Secret(), cronCLIKeychainOptions()...); err != nil {
		api.Stop()
		log.Printf("[cron-api] failed to publish credentials: %v", err)
	} else {
		a.mu.Lock()
		a.cronAPI = api
		a.mu.Unlock()
	}

	// Ensure sidebar groups exist.
	a.ensureAutomationsGroup()
	a.ensureHandoffsGroup()
	a.ensureTranscriptsGroup()
	a.ensureMainChatOnStartup()
}

// ---------- Cron job Wails bindings ----------

// CreateCronJob creates a new scheduled automation.
func (a *App) CreateCronJob(input CronJobInput) (CronJobState, error) {
	if a.cronScheduler == nil {
		return CronJobState{}, fmt.Errorf("cron scheduler is unavailable")
	}
	return a.cronScheduler.AddJob(a.ctx, input)
}

// UpdateCronJob patches an existing scheduled automation. Turning on a
// schedule the Agency proposed that nobody approved yet approves it (see
// cron_agency_proposals.go); every other edit, enable and pause, approved
// Agency schedules included, applies like any other automation.
func (a *App) UpdateCronJob(id string, patch CronJobPatch) (CronJobState, error) {
	return a.updateCronJob(id, patch, true)
}

// updateCronJobFromAPI is UpdateCronJob for the cron REST API, which agent
// CLIs call from a chat or a scheduled run. Turning on an Agency proposal is
// the user's decision, so it is refused there instead of being recorded as an
// approval. A change to the job's target, timing or delivery invalidates any
// authority grants bound to its initiative, so a rewritten prompt can never
// inherit the manifest grants the user approved for the original one. The
// REST handler posts the main-chat report line; the Upcoming tools post
// their own.
func (a *App) updateCronJobFromAPI(id string, patch CronJobPatch) (CronJobState, error) {
	if err := a.refuseSystemScheduleFromAPI(id, "change"); err != nil {
		return CronJobState{}, err
	}
	// A change to what the job runs, when it runs or where its results go
	// must not keep the initiative's grants: revoke them before the change
	// lands, and refuse the change when they cannot be revoked.
	if cronPatchChangesExecution(patch) {
		if err := a.revokeInitiativeGrantsForJob(id, "schedule target, timing or delivery changed through the cron API"); err != nil {
			return CronJobState{}, fmt.Errorf("refusing the change: could not revoke the schedule's approved permissions: %w", err)
		}
	}
	return a.updateCronJob(id, patch, false)
}

// cronPatchChangesExecution reports a patch that changes what a job runs,
// when it runs, or where its results are delivered.
func cronPatchChangesExecution(patch CronJobPatch) bool {
	return patch.TargetConfig != nil || patch.ScheduleConfig != nil || patch.TargetKind != nil ||
		patch.ScheduleKind != nil || patch.NotifyDM != nil || patch.NotifyPlatform != nil
}

// apiUpdateStatus maps a cron API patch to a main-chat report status. A
// rewrite of the job's prompt or schedule reports as changed even when the
// same call turns the job on or off, so it is never hidden behind "Resumed".
func apiUpdateStatus(patch CronJobPatch) string {
	if patch.TargetConfig != nil || patch.ScheduleConfig != nil || patch.TargetKind != nil || patch.ScheduleKind != nil {
		return "changed"
	}
	if patch.Enabled != nil {
		if *patch.Enabled {
			return "resumed"
		}
		return "paused"
	}
	return "changed"
}

// revokeInitiativeGrantsForJob revokes every authority grant bound to the
// initiative that owns the given cron job, whatever the initiative's status:
// a finished initiative's grants come back to life if it runs again. Used when
// a schedule is rewritten through the API, so the new prompt cannot run with
// grants the user approved for the previous one. It returns the first error
// so the caller can refuse the change.
func (a *App) revokeInitiativeGrantsForJob(jobID, reason string) error {
	if a.activity == nil || a.activity.store == nil {
		return nil
	}
	initiative, err := a.activity.store.GetInitiativeByExecutionRef(jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find the initiative of job %s: %w", jobID, err)
	}
	if strings.TrimSpace(initiative.ID) == "" {
		return nil
	}
	grants, err := a.activity.store.ListAuthorityGrants(initiative.ID)
	if err != nil {
		return fmt.Errorf("list grants for job %s: %w", jobID, err)
	}
	for _, g := range grants {
		if g.Status == "revoked" {
			continue
		}
		if _, err := a.activity.store.RevokeAuthorityGrant(g.ID, reason); err != nil {
			return fmt.Errorf("revoke grant %s for job %s: %w", g.ID, jobID, err)
		}
	}
	return nil
}

// updateCronJob applies patch. userDecides is true only for the user's own
// Automations action, the one caller that may approve an Agency proposal.
func (a *App) updateCronJob(
	id string,
	patch CronJobPatch,
	userDecides bool,
) (CronJobState, error) {
	if a.cronScheduler == nil {
		return CronJobState{}, fmt.Errorf("cron scheduler is unavailable")
	}
	if !userDecides {
		if err := a.refusePlaybookScheduleFromAPI(id); err != nil {
			return CronJobState{}, err
		}
	}
	if patch.Enabled != nil && *patch.Enabled {
		current, err := a.cronScheduler.GetJob(id)
		if err != nil {
			return CronJobState{}, err
		}
		if err := a.refuseUnpinnedPlaybookJob(current); err != nil {
			return CronJobState{}, err
		}
		if !userDecides {
			if err := a.refuseUnapprovedAgencySchedule(
				current,
				"turn on",
				agencyScheduleUserDecidesRemedy,
			); err != nil {
				return CronJobState{}, err
			}
			return a.cronScheduler.UpdateJob(a.ctx, id, patch)
		}
		unapproved, err := a.agencyScheduleUnapproved(current)
		if err != nil {
			return CronJobState{}, err
		}
		if unapproved {
			return a.approveAgencyScheduleFromAutomations(id, patch)
		}
	}
	state, err := a.cronScheduler.UpdateJob(a.ctx, id, patch)
	if err == nil && patch.Enabled != nil && state.SurfaceKind == playbookSurfaceKind {
		a.syncPlaybookScheduleEnabled(id, *patch.Enabled)
	}
	return state, err
}

// DeleteCronJob removes a scheduled automation. Falls back to a direct DB
// delete when the scheduler failed to initialize, so a user can always remove
// a row from the list regardless of automation state or scheduler health.
// Deleting a schedule the Agency proposed that nobody approved simply removes
// the proposal.
//
// Also cascades to a linked automation chat session when one exists. This
// mainly supports rows imported from the legacy proposal system.
func (a *App) DeleteCronJob(id string) error {
	return a.deleteCronJob(id, true)
}

// deleteCronJobFromAPI is DeleteCronJob for the cron REST API, which agent
// CLIs call from a chat or a scheduled run. Deleting an Agency proposal is the
// user's decision, so it is refused there. The REST handler posts the
// main-chat report line; the Upcoming tools post their own.
func (a *App) deleteCronJobFromAPI(id string) error {
	if err := a.refuseSystemScheduleFromAPI(id, "delete"); err != nil {
		return err
	}
	return a.deleteCronJob(id, false)
}

// cronJobName returns a cron job's name for a report line, best-effort.
func (a *App) cronJobName(id string) string {
	if a.cronScheduler != nil {
		if state, err := a.cronScheduler.GetJob(id); err == nil {
			return state.Name
		}
	}
	if a.store != nil {
		if row, err := a.store.LoadCronJob(agencyContext(a), id); err == nil && row != nil {
			return jobRowToState(row).Name
		}
	}
	return ""
}

// deleteCronJob removes the automation. userDecides is true only for the
// user's own Automations action, the one caller that may delete an Agency
// proposal.
func (a *App) deleteCronJob(id string, userDecides bool) error {
	if !userDecides {
		if err := a.refusePlaybookScheduleFromAPI(id); err != nil {
			return err
		}
	}
	var current *CronJobState
	if a.cronScheduler != nil {
		state, err := a.cronScheduler.GetJob(id)
		if err != nil {
			return err
		}
		current = &state
	} else if a.store != nil {
		row, err := a.store.LoadCronJob(agencyContext(a), id)
		if err != nil {
			return err
		}
		state := jobRowToState(row)
		current = &state
	}
	if current != nil && !userDecides {
		if err := a.refuseUnapprovedAgencySchedule(
			*current,
			"delete",
			agencyScheduleUserDecidesRemedy,
		); err != nil {
			return err
		}
	}
	return a.removeCronJob(id)
}

// removeCronJob deletes the row and its linked legacy chat session.
func (a *App) removeCronJob(id string) error {
	a.cleanupCronJobChatSession(id)

	if a.cronScheduler != nil {
		return a.cronScheduler.RemoveJob(a.ctx, id)
	}
	if a.store == nil {
		return fmt.Errorf("cron storage is unavailable")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, dbCancel := context.WithTimeout(ctx, dbTimeout)
	defer dbCancel()
	if err := a.store.DeleteCronJob(dbCtx, id); err != nil {
		log.Printf("[cron] DeleteCronJob DB fallback failed: %v", err)
		return fmt.Errorf("delete cron job: %w", err)
	}
	a.emitBrowserEvent("cron:state", map[string]any{"id": id, "deleted": true})
	return nil
}

// cleanupCronJobChatSession removes a legacy initiative-notification chat
// session linked to the given cron job. New shadow initiatives do not create
// chat sessions. Best-effort: a missing initiative / session / store just
// short-circuits so callers can stay idempotent.
func (a *App) cleanupCronJobChatSession(cronJobID string) {
	if cronJobID == "" || a.activity == nil || a.activity.store == nil || a.store == nil {
		return
	}
	initiative, err := a.activity.store.GetInitiativeByExecutionRef(cronJobID)
	if err != nil || initiative.ChatSessionID == "" {
		return
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.store.RemoveGroupItemsByRef(ctx, "chat", initiative.ChatSessionID); err != nil {
		log.Printf("[cron] cleanup: remove sidebar group entry failed (continuing): %v", err)
	}
	if err := a.store.DeleteSession(ctx, initiative.ChatSessionID); err != nil {
		log.Printf("[cron] cleanup: delete chat session failed (continuing): %v", err)
	}
	a.mainChatRowsChanged(initiative.ChatSessionID)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "sidebar:groups-changed", nil)
		runtime.EventsEmit(a.ctx, "session:deleted", initiative.ChatSessionID)
	}
}

// RunCronJobNow triggers an immediate execution of the given automation. An
// Agency proposal nobody approved is refused, from Automations and from the
// cron REST API alike: running it would carry out work nobody approved.
func (a *App) RunCronJobNow(id string) error {
	if a.cronScheduler == nil {
		return fmt.Errorf("cron scheduler is unavailable")
	}
	current, err := a.cronScheduler.GetJob(id)
	if err != nil {
		return err
	}
	if err := a.refuseUnapprovedAgencySchedule(
		current,
		"run",
		agencyScheduleRunRemedy,
	); err != nil {
		return err
	}
	// A playbook's run reads pages in the signed-in browser and sends its
	// result by text or email: it starts only on its schedule or from the
	// Playbooks page (RunPlaybookNow), never from Upcoming, the cron REST
	// API or a chat tool.
	if err := a.refusePlaybookScheduleFromAPI(id); err != nil {
		return err
	}
	return a.cronScheduler.RunNow(a.ctx, id)
}

// ReorderCronJobs updates the sort order of cron jobs for drag-and-drop reordering.
func (a *App) ReorderCronJobs(ids []string) error {
	if a.cronScheduler == nil {
		return fmt.Errorf("cron scheduler is unavailable")
	}
	return a.cronScheduler.ReorderJobs(ids)
}

// The proposal-based handoff lifecycle (AcceptHandoffProposal /
// DismissHandoffProposal / DeleteHandoffSession) was removed when handoffs moved
// to the file-first store. See handoff_api.go: AcceptHandoff / AbortHandoff /
// DeleteHandoff.

// ListCronJobs returns all scheduled automations.
func (a *App) ListCronJobs() []CronJobState {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	rows := a.listCronJobsContext(ctx)
	visible := rows[:0]
	for _, row := range rows {
		if row.SurfaceKind != openAnswerSurfaceKind {
			visible = append(visible, row)
		}
	}
	return visible
}

func (a *App) listCronJobsContext(ctx context.Context) []CronJobState {
	if a.cronScheduler == nil {
		log.Printf("[cron] ListCronJobs called but scheduler is nil")
		return a.listCronJobsFromDBContext(ctx)
	}
	result := a.cronScheduler.ListJobs()
	log.Printf("[cron] ListCronJobs returning %d jobs from scheduler", len(result))

	// Database fallback: if scheduler returned nothing, read directly from DB.
	if len(result) == 0 && a.store != nil {
		dbJobs := a.listCronJobsFromDBContext(ctx)
		if len(dbJobs) > 0 {
			log.Printf("[cron] ListCronJobs: scheduler empty but DB has %d jobs, using DB fallback", len(dbJobs))
			result = dbJobs
		}
	}
	return result
}

func (a *App) listCronJobsFromDB() []CronJobState {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.listCronJobsFromDBContext(ctx)
}

func (a *App) listCronJobsFromDBContext(ctx context.Context) []CronJobState {
	if a.store == nil {
		return nil
	}

	rows, err := a.store.ListCronJobs(ctx)
	if err != nil {
		log.Printf("[cron] ListCronJobs DB fallback error: %v", err)
		return nil
	}

	result := make([]CronJobState, 0, len(rows))
	for i := range rows {
		result = append(result, jobRowToState(&rows[i]))
	}
	return result
}

// GetCronJob returns a single scheduled automation.
func (a *App) GetCronJob(id string) (CronJobState, error) {
	if a.cronScheduler == nil {
		return CronJobState{}, fmt.Errorf("cron scheduler is unavailable")
	}
	return a.cronScheduler.GetJob(id)
}

// GetCronRuns returns the recent run history for a scheduled automation.
func (a *App) GetCronRuns(jobID string) ([]CronRunState, error) {
	return a.getCronRunsContext(a.ctx, jobID)
}

func (a *App) getCronRunsContext(ctx context.Context, jobID string) ([]CronRunState, error) {
	if a.cronScheduler == nil {
		return nil, fmt.Errorf("cron scheduler is unavailable")
	}
	return a.cronScheduler.GetRuns(ctx, jobID)
}

// GetCronRunsByDateRange returns all automation runs within a time range, enriched with job names.
func (a *App) GetCronRunsByDateRange(startMs, endMs int64) ([]CronRunWithJobName, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.getCronRunsByDateRangeContext(ctx, startMs, endMs)
}

func (a *App) getCronRunsByDateRangeContext(ctx context.Context, startMs, endMs int64) ([]CronRunWithJobName, error) {
	if a.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}

	rows, err := a.store.ListCronRunsByDateRange(ctx, startMs, endMs, 0)
	if err != nil {
		return nil, fmt.Errorf("query cron runs by date range: %w", err)
	}

	// Build job name lookup from scheduler (quiet path, no log spam).
	// Falls back to DB if scheduler is nil or returns empty — same
	// semantics as ListCronJobs() but without the log.Printf calls.
	jobNames := make(map[string]string)
	var jobs []CronJobState
	if a.cronScheduler != nil {
		jobs = a.cronScheduler.ListJobs()
	}
	if len(jobs) == 0 {
		jobs = a.listCronJobsFromDBContext(ctx)
	}
	for _, j := range jobs {
		jobNames[j.ID] = j.Name
	}

	result := make([]CronRunWithJobName, 0, len(rows))
	for _, row := range rows {
		state := runRowToState(row)
		result = append(result, CronRunWithJobName{
			CronRunState: state,
			JobName:      jobNames[row.JobID],
		})
	}
	return result, nil
}

// LoadCronRunLogs returns the stream items (conversation log) for a specific
// cron run. It uses the run's session_tag and item_sort_start to find the
// correct slice of stream items from the shared session.
func (a *App) LoadCronRunLogs(jobID, runID string) ([]StreamItemData, error) {
	if a.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}

	// Find the run to get session_tag, item_sort_start, started_at.
	runs, err := a.store.ListCronRuns(a.ctx, jobID, 100)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}

	var targetRun *storage.CronRunRow
	for i := range runs {
		if runs[i].ID == runID {
			targetRun = &runs[i]
			break
		}
	}
	if targetRun == nil {
		return nil, fmt.Errorf("run not found: %s", runID)
	}
	if targetRun.SessionTag == "" || targetRun.ItemSortStart < 0 {
		return nil, nil // no logs available
	}

	// Determine the upper bound: the next run's item_sort_start.
	sortEnd, err := a.store.GetNextCronRunSortStart(a.ctx, jobID, runID, targetRun.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("get next cron run sort start: %w", err)
	}

	rows, err := a.store.LoadStreamItemsForCronRun(a.ctx, targetRun.SessionTag, targetRun.ItemSortStart, sortEnd)
	if err != nil {
		return nil, fmt.Errorf("load stream items: %w", err)
	}

	// Normalize legacy cron rows before returning them to the frontend.
	// Older builds persisted stream items twice:
	// 1. directly in cron_exec.go, and
	// 2. again via the frontend agent event listener.
	// That left duplicated assistant/result rows and a "tool_use" row followed
	// by an empty "tool" row that renders as an empty expandable block.
	items := make([]StreamItemData, 0, len(rows))
	for _, r := range rows {
		item := StreamItemData{
			ID:        r.ID,
			Kind:      r.Kind,
			Content:   r.Content,
			ToolName:  r.ToolName,
			ToolInput: r.ToolInput,
			ToolUseID: r.ToolUseID,
			RunID:     r.RunID,
			SessionID: r.SessionID,
			SortOrder: r.SortOrder,
		}
		if item.Kind == "tool_use" {
			item.Kind = "tool"
		}
		if len(items) > 0 && shouldSkipCronLogDuplicate(items[len(items)-1], item) {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func shouldSkipCronLogDuplicate(prev, current StreamItemData) bool {
	if prev.Kind != current.Kind {
		return false
	}

	switch current.Kind {
	case "assistant", "result":
		return current.Content != "" && prev.Content == current.Content
	case "tool":
		return prev.ToolName == current.ToolName &&
			prev.Content == current.Content &&
			prev.ToolInput != "" &&
			current.ToolInput == ""
	default:
		return false
	}
}

func (a *App) checkAutomationDuplicate(ctx context.Context, input CronJobInput, existing []storage.CronJobRow) (bool, string, error) {
	type dedupCandidate struct {
		name        string
		description string
		prompt      string
		score       int
	}

	newPrompt := extractPromptFromTargetConfig(input.TargetConfig)
	newText := buildAutomationDedupText(input.Name, input.Description, newPrompt)
	if newText == "" {
		return false, "", nil
	}

	candidates := make([]dedupCandidate, 0, len(existing))
	for _, job := range existing {
		existingPrompt := extractPromptFromTargetConfig(json.RawMessage(job.TargetConfig))
		score := automationDedupScore(newText, buildAutomationDedupText(job.Name, job.Description, existingPrompt))
		if score < 3 {
			continue
		}
		candidates = append(candidates, dedupCandidate{
			name:        job.Name,
			description: job.Description,
			prompt:      existingPrompt,
			score:       score,
		})
	}
	if len(candidates) == 0 {
		return false, "", nil
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) > 20 {
		candidates = candidates[:20]
	}

	var sb strings.Builder
	sb.WriteString("Existing automations:\n")
	for _, candidate := range candidates {
		desc := candidate.description
		if desc == "" {
			desc = "(no description)"
		}
		prompt := candidate.prompt
		if prompt == "" {
			prompt = "(no prompt)"
		}
		sb.WriteString(fmt.Sprintf("- Name: %q, Description: %q, Prompt: %q\n", candidate.name, desc, prompt))
	}

	desc := input.Description
	if desc == "" {
		desc = "(no description)"
	}
	displayPrompt := newPrompt
	if displayPrompt == "" {
		displayPrompt = "(no prompt)"
	}
	sb.WriteString(fmt.Sprintf("\nProposed new automation:\n- Name: %q\n- Description: %q\n- Prompt: %q\n", input.Name, desc, displayPrompt))

	fullPrompt := `You are checking if a proposed new automation duplicates any existing one.
Two automations are duplicates if they serve the same purpose and would produce overlapping results, even if worded differently.
Different schedules alone do NOT make them non-duplicates if the task is the same.

` + sb.String() + `
Respond with ONLY a JSON object (no markdown, no code fences):
{"isDuplicate": true/false, "matchedName": "name of the matched existing automation, or empty string"}`

	timeout := 30 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			timeout = remaining
		}
	}

	resp, err := a.runEphemeralHaiku(fullPrompt, timeout)
	if err != nil {
		return false, "", fmt.Errorf("haiku call: %w", err)
	}

	var result struct {
		IsDuplicate bool   `json:"isDuplicate"`
		MatchedName string `json:"matchedName"`
	}
	cleaned := stripCodeFences(resp)
	if err := unmarshalJSONBestEffort(cleaned, &result); err != nil {
		return false, "", fmt.Errorf("parse response: %w (raw: %.200s)", err, resp)
	}
	return result.IsDuplicate, result.MatchedName, nil
}

func buildAutomationDedupText(name, description, prompt string) string {
	parts := []string{
		normalizeAutomationFingerprintText(name),
		normalizeAutomationFingerprintText(description),
		normalizeAutomationFingerprintText(prompt),
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func automationDedupScore(left, right string) int {
	if left == "" || right == "" {
		return 0
	}

	score := automationDedupNGramOverlap(left, right)
	if len([]rune(left)) >= 6 && strings.Contains(right, left) {
		score += 10
	}
	if len([]rune(right)) >= 6 && strings.Contains(left, right) {
		score += 10
	}
	return score
}

func automationDedupNGramOverlap(left, right string) int {
	leftSet := automationDedupNGrams(left, 2)
	rightSet := automationDedupNGrams(right, 2)
	if len(leftSet) == 0 || len(rightSet) == 0 {
		return 0
	}

	score := 0
	for gram := range leftSet {
		if _, ok := rightSet[gram]; ok {
			score++
		}
	}
	return score
}

func automationDedupNGrams(input string, size int) map[string]struct{} {
	input = strings.ReplaceAll(normalizeAutomationFingerprintText(input), " ", "")
	runes := []rune(input)
	if len(runes) == 0 || size <= 0 {
		return nil
	}
	if len(runes) < size {
		return map[string]struct{}{string(runes): {}}
	}

	grams := make(map[string]struct{}, len(runes)-size+1)
	for i := 0; i+size <= len(runes); i++ {
		grams[string(runes[i:i+size])] = struct{}{}
	}
	return grams
}

// probeAndGreet sends a lightweight Haiku request through the agent runner to
// verify it can authenticate with Anthropic (via the user's own API key, auth
// token, or Claude Code's native auth such as keychain/session-env/OAuth). On
// success it emits an "auth:greeting" event so the frontend can show a
// friendly toast. On failure it delegates to AuthManager.RecoverAuth for
// sub2api fallback. Skipped when sub2api is already active.
func (a *App) probeAndGreet() {
	if a.authManager.IsActive() {
		return
	}

	// Gateway override is handled at startup before the agent starts,
	// so just skip the probe if it's active.
	if loadConfig().UseYansuGateway {
		return
	}

	name := a.getUserDisplayName()
	if name == "the user" {
		name = "there"
	}

	prompt := fmt.Sprintf(
		"Greet %q in one short friendly sentence, under 10 words. No emoji. No markdown.",
		name,
	)

	greeting, err := a.runEphemeralHaiku(prompt)
	if err != nil {
		log.Printf("[probe] local credential probe failed: %v", err)

		if !isFallbackError(err.Error()) {
			// Non-auth failure (timeout, empty response, network issue).
			// Retry once with a longer timeout for cold-start scenarios
			// where the agent runner may still be spinning up.
			log.Printf("[probe] non-auth error, retrying with longer timeout: %v", err)
			greeting, err = a.runEphemeralHaiku(prompt, 30*time.Second)
			if err != nil {
				// Still failing — silently give up. The greeting is
				// nice-to-have; a transient failure should never scare
				// the user with an "Authentication failed" toast.
				log.Printf("[probe] retry also failed, skipping greeting: %v", err)
				return
			}
		} else {
			// Genuine auth error. With the Yansu gateway retired, RecoverAuth
			// no longer injects sub2api — it returns false silently and the
			// retirement banner guides the user to configure their own
			// endpoint. The greeting is nice-to-have; a missing endpoint must
			// never scare the user with an "Authentication failed" toast, so
			// just give up quietly. (The sub2api verification leg below is
			// retained for the day re-enabling the gateway is reconsidered.)
			if !a.authManager.RecoverAuth() {
				log.Printf("[probe] recovery not applicable (gateway retired); skipping greeting")
				return
			}
			// Sub2api was injected — verify it actually works before greeting.
			greeting, err = a.runEphemeralHaiku(prompt)
			if err != nil {
				log.Printf("[probe] sub2api verification probe failed: %v", err)
				return
			}
		}
	}

	a.mu.Lock()
	alreadyGreeted := a.probeSucceeded
	a.probeSucceeded = true
	a.mu.Unlock()

	log.Printf("[probe] local credentials verified")
	if !alreadyGreeted && a.ctx != nil {
		a.emitBrowserEvent("auth:greeting", map[string]string{
			"message": greeting,
		})
	}
}

// runEphemeralClaudeModel sends a single prompt through the agent runner
// using an ephemeral (no SessionID) session. Returns the concatenated
// assistant response text or an error. An optional timeout can be provided;
// defaults to 23s.
//
// All callers are pure-text/JSON one-shots (suggestion chips, project naming,
// moments JSON extraction, pipeline summaries, greetings, etc.), so we
// suppress both the tool list and the runner's default system prompt:
//
//   - NoTools: small/cheap models (Haiku, and especially the Qwen-routed
//     gateway) get distracted by the tool schema and burn turns calling
//     Bash/Read/Write instead of emitting the requested text. Without this
//     flag, moments calls have been observed looping 49+ turns past their
//     60s timeout, never producing JSON.
//   - NoSystem: the prompt is self-contained; the generic "coding assistant"
//     wrapper steers small models toward tool use.
func (a *App) runEphemeralClaudeModel(prompt, model string, timeout ...time.Duration) (string, error) {
	return a.runEphemeralClaudeModelContext(context.Background(), prompt, model, timeout...)
}

func (a *App) runEphemeralClaudeModelContext(parent context.Context, prompt, model string, timeout ...time.Duration) (string, error) {
	return a.runEphemeralClaudeModelRouted(parent, agent.BackendHintEphemeral, prompt, model, timeout...)
}

// runLearningClaudeModel is runEphemeralClaudeModel for background learning
// calls (the Dream memory merge; shadow task evaluation and Purpose
// extraction set the same hint on their own task inputs): the prompts carry
// observed work and memory contents, so the learning backend hint keeps them
// on Isoform's own runner even when the user picked an assistant for chat.
func (a *App) runLearningClaudeModel(prompt, model string, timeout ...time.Duration) (string, error) {
	return a.runEphemeralClaudeModelRouted(context.Background(), agent.BackendHintLearning, prompt, model, timeout...)
}

func (a *App) runEphemeralClaudeModelRouted(parent context.Context, hint, prompt, model string, timeout ...time.Duration) (string, error) {
	return a.runEphemeralClaudeModelObserved(parent, hint, prompt, model, nil, timeout...)
}

// runEphemeralClaudeModelObserved is runEphemeralClaudeModelRouted that also
// hands each stream event to observe, so a caller can learn which assistant
// answered from the backend_route event.
func (a *App) runEphemeralClaudeModelObserved(parent context.Context, hint, prompt, model string, observe func(*agent.StreamEvent), timeout ...time.Duration) (string, error) {
	if a == nil || a.IsClosedEyesModeEnabled() {
		return "", errors.New("observation is paused")
	}
	prompt = redactActivityModelText(prompt)
	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	if runner == nil {
		return "", errRunnerNotInitialized
	}

	t := 23 * time.Second
	if len(timeout) > 0 && timeout[0] > 0 {
		t = timeout[0]
	}
	ctx, cancel := context.WithTimeout(parent, t)
	defer cancel()

	var response strings.Builder
	input := &agent.TaskInput{
		Prompt:    prompt,
		WorkDir:   os.TempDir(),
		Model:     strings.TrimSpace(model),
		Ephemeral: true,
		NoTools:   true,
		NoSystem:  true,
		// One-shot probes are background work: they route through the
		// background model group so they never drain the plan the user
		// needs for chat.
		BackendHint: hint,
		Priority:    agent.PriorityBackground,
		// runEphemeralClaudeModel is the shared entry for one-shot
		// internal LLM probes (greetings, JSON extraction, project
		// naming, suggestion chips, moments classification, etc.).
		// None of those are user-initiated agent runs — suppress the
		// agentloop.* telemetry so the PostHog dashboard isn't
		// polluted with rows that have no upstream user trace.
		SuppressTelemetry: true,
	}

	_, err := runner.ExecuteTask(ctx, input, func(event *agent.StreamEvent) error {
		if observe != nil {
			observe(event)
		}
		if event.Type == "assistant" && event.Content != "" {
			response.WriteString(event.Content)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	text := strings.TrimSpace(response.String())
	if text == "" {
		return "", fmt.Errorf("empty response from probe")
	}
	return text, nil
}

func (a *App) runEphemeralHaiku(prompt string, timeout ...time.Duration) (string, error) {
	return a.runEphemeralClaudeModel(prompt, a.lightTaskModel(), timeout...)
}

// ── Activity retention configuration ──
//
// The user picks two values via Settings → Activity:
//   1. Recording retention (days)  — age-based purge threshold.
//   2. Storage cap (GB)            — quota-based purge threshold.
// Recordings are deleted when EITHER limit is reached, whichever comes first.
//
// Allowed UI values (RETENTION_OPTIONS / STORAGE_CAP_OPTIONS in
// settings-page.tsx):
//   days: 0 (Unlimited — no age purge), 7, 14, 30, 60, 90, 180, 360.
//   gb:   1, 2, 5, 10, 20, 50, 100 (or any user-entered integer ≥ 1).
// SetActivityRetentionDays accepts any non-negative integer for forward
// compatibility; SetActivityMaxStorageGB rejects values below
// minActivityMaxStorageGB (1) — see "Smaller values are effectively self-DoS"
// note on that constant.

// activityRetentionConfig is persisted to disk as JSON.
//
// MaxStorageGB == 0 in an on-disk config means "field absent / legacy file";
// readActivityRetentionConfig substitutes defaultActivityMaxStorageGB so
// older installs auto-upgrade to the new default cap on first load. To
// genuinely disable the storage cap users would have to pick a very large
// number — there is intentionally no Unlimited option for storage.
type activityRetentionConfig struct {
	RetentionDays int `json:"retention_days"` // 0 = unlimited (age purge disabled)
	MaxStorageGB  int `json:"max_storage_gb"` // 0 = field missing → use default
	// ScreenshotDays is how many days screenshot images are kept (rows and
	// text stay). nil = field absent: images follow RetentionDays, as before
	// the setting existed. 0 = kept until the storage limit needs the space.
	ScreenshotDays *int `json:"screenshot_days,omitempty"`
	// StorageLimitConfirmed records the user's yes to removals under the
	// real-disk storage limit (activity_storage_budget.go). nil = not decided
	// yet: decided on the first pass, without asking when nothing is over.
	StorageLimitConfirmed *bool `json:"storage_limit_confirmed,omitempty"`
}

func activityConfigPath() string {
	home, err := flavorDataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "activity-config.json")
}

// defaultActivityRetentionDays is what we report when no config has been saved.
// It matches the historical default ColdRetention (30 days) so existing
// installs see no behavior change after this feature ships.
const defaultActivityRetentionDays = 30

// defaultActivityMaxStorageGB is the user-facing storage cap used when no
// config file exists or the field is absent. The historical hard-coded value
// was 2GB, but that was bypassable (audio bytes weren't counted) and once
// audio counting was fixed it proved aggressive enough to wipe weeks of
// recordings on a single rotation pass for users who record meetings. 10GB
// is roughly 2-3 weeks of typical meeting audio at observed compression
// rates, which matches the spirit of the 30-day retention default.
const defaultActivityMaxStorageGB = 10

// minActivityMaxStorageGB is the lowest cap we accept. Smaller values are
// effectively self-DoS — even a single meeting recording can exceed them —
// so invalid values fall back to defaultActivityMaxStorageGB instead of
// being clamped to this floor (see activityRotatorConfigFor and
// readActivityRetentionConfig). The setter SetActivityMaxStorageGB rejects
// sub-min values outright so the user gets immediate feedback.
const minActivityMaxStorageGB = 1

// readActivityRetentionConfig returns (days, gb), substituting defaults for
// missing fields / unreadable files / out-of-range values. days==0 is
// preserved as a deliberate Unlimited choice; gb==0 always falls back to
// the default because the UI does not expose an Unlimited storage option.
func readActivityRetentionConfig() (days, gb int) {
	days = defaultActivityRetentionDays
	gb = defaultActivityMaxStorageGB

	path := activityConfigPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cfg activityRetentionConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return
	}
	if cfg.RetentionDays >= 0 {
		days = cfg.RetentionDays
	}
	if cfg.MaxStorageGB >= minActivityMaxStorageGB {
		gb = cfg.MaxStorageGB
	}
	return
}

// readActivityRetentionDays is a thin shim kept for callers that only need
// the days value. Prefer readActivityRetentionConfig in new code.
func readActivityRetentionDays() int {
	d, _ := readActivityRetentionConfig()
	return d
}

// activityRotatorConfigFor builds a RotatorConfig from the two user-facing
// values. days <= 0 means "unlimited" (no age purge); gb values below
// minActivityMaxStorageGB fall back to defaultActivityMaxStorageGB rather than
// being clamped to the minimum — a tiny cap is more often a misconfiguration
// or migration artifact than an intentional choice, and the default gives a
// safe backstop instead of a self-DoS.
// TargetFreeSize is held at 90% of MaxTotalSize: over the limit, the oldest
// images and recordings go one by one until the total is back at 90%, a small
// margin so the next captures do not trip the limit again at once.
func activityRotatorConfigFor(days, gb int) RotatorConfig {
	cfg := DefaultRotatorConfig()
	if days <= 0 {
		cfg.ColdRetention = 0
		cfg.WarmRetention = 7 * 24 * time.Hour // keep something sensible for tier transitions
	} else {
		cold := time.Duration(days) * 24 * time.Hour
		// Warm = ceil(N/4) days, but at least 1 day so segments don't skip the
		// warm tier entirely on small retentions like 7 days.
		warmDays := (days + 3) / 4
		if warmDays < 1 {
			warmDays = 1
		}
		cfg.ColdRetention = cold
		cfg.WarmRetention = time.Duration(warmDays) * 24 * time.Hour
	}
	if gb < minActivityMaxStorageGB {
		gb = defaultActivityMaxStorageGB
	}
	const gigabyte = int64(1024) * 1024 * 1024
	cfg.MaxTotalSize = int64(gb) * gigabyte
	cfg.TargetFreeSize = cfg.MaxTotalSize * 9 / 10
	return cfg
}

// GetActivityRetentionDays returns the configured retention period.
// 0 means "unlimited" (no age-based purge).
func (a *App) GetActivityRetentionDays() int {
	d, _ := readActivityRetentionConfig()
	return d
}

// GetActivityMaxStorageGB returns the configured storage cap in gigabytes.
// Always returns at least minActivityMaxStorageGB.
func (a *App) GetActivityMaxStorageGB() int {
	_, gb := readActivityRetentionConfig()
	return gb
}

// GetActivityStorageStats returns aggregated storage usage for the
// Settings → Activity panel: bytes / session counts split into the
// retained bucket (subject to user retention), plus the earliest retained session
// timestamp so the UI can show "your oldest recording is from X".
//
// Returns a zero-value struct (no error) when the activity store hasn't been
// initialized yet — that's the normal state on platforms / launches where
// activity monitoring isn't running, and we'd rather render an empty card
// than surface a confusing error to the user.
func (a *App) GetActivityStorageStats() (perfValue ActivityStorageStats, perfErr error) {
	defer logPageBinding("GetActivityStorageStats", time.Now(), &perfValue)
	if a.activity == nil || a.activity.store == nil {
		return ActivityStorageStats{}, nil
	}
	stats, err := a.activity.store.GetActivityStorageStats()
	if err == nil && a.activity.rotator != nil {
		// The bytes the storage limit is held to: measured on disk, so files
		// without rows and the database count too.
		stats.RetainedBytes = a.activity.rotator.Usage(time.Minute).TotalBytes
	}
	return stats, err
}

// writeActivityRetentionConfig persists both fields atomically (as far as a
// single os.WriteFile is atomic on the host FS) and re-applies the resulting
// RotatorConfig to the live rotator, kicking off an immediate rotation pass
// in a goroutine so the user sees post-purge state quickly. Splitting writes
// across two setters would race: a user changing retention right after
// changing the cap could clobber the cap field, since each setter would
// rebuild the JSON from a stale read.
func (a *App) writeActivityRetentionConfig(days, gb int, confirmLimit bool) error {
	// The other storage settings in the file are kept.
	if err := updateActivityConfigFile(func(cfg *activityRetentionConfig) {
		cfg.RetentionDays = days
		cfg.MaxStorageGB = gb
		if confirmLimit {
			yes := true
			cfg.StorageLimitConfirmed = &yes
		}
	}); err != nil {
		return err
	}
	// Apply to the live rotator and kick off an immediate pass. We do the
	// rotation in a goroutine because the caller is a Wails binding on the
	// UI thread and rotate() can take minutes (it compacts screenshots).
	if a.activity != nil && a.activity.rotator != nil {
		rotator := a.activity.rotator
		rotator.UpdateConfig(activityRotatorConfigFor(days, gb))
		go rotator.TriggerOnce()
	}
	return nil
}

// SetActivityRetentionDays persists the retention period, applies it to the
// running rotator, and triggers an immediate rotation pass so any data that
// just became eligible is removed without waiting for the next 30-minute tick.
// The storage cap field is preserved.
func (a *App) SetActivityRetentionDays(days int) error {
	if days < 0 {
		return fmt.Errorf("retention days must be non-negative")
	}
	_, gb := readActivityRetentionConfig()
	return a.writeActivityRetentionConfig(days, gb, false)
}

// SetActivityMaxStorageGB persists the storage cap, applies it to the
// running rotator, and triggers an immediate rotation pass. The retention
// days field is preserved. Values below minActivityMaxStorageGB are
// rejected to avoid pathological caps that would delete data faster than
// it can be recorded.
func (a *App) SetActivityMaxStorageGB(gb int) error {
	if gb < minActivityMaxStorageGB {
		return fmt.Errorf("storage cap must be at least %d GB", minActivityMaxStorageGB)
	}
	days, current := readActivityRetentionConfig()
	// Lowering the limit (the page asks first when that removes files) is
	// the user's yes to removals under it.
	return a.writeActivityRetentionConfig(days, gb, gb < current)
}
