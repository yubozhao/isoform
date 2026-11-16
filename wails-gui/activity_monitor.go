package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/jpeg"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wails-gui/internal/activitybuf"
	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/activitymon/appparser"
	"wails-gui/internal/platform/ocr"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// ActivitySnapshot represents a captured moment of user activity ("Live Photo").
type ActivitySnapshot struct {
	ID           string                      `json:"id"`
	StartTime    int64                       `json:"startTime"`
	EndTime      int64                       `json:"endTime"`
	Events       []activitymon.ActivityEvent `json:"events"`
	SegmentPaths []string                    `json:"segmentPaths"`
	TriggerKind  string                      `json:"triggerKind"`
	AppNames     []string                    `json:"appNames"`
}

// ActivityMonitorStatus is emitted to the frontend.
type ActivityMonitorStatus struct {
	Monitoring    bool    `json:"monitoring"`
	SnapshotCount int     `json:"snapshotCount"`
	StorageMB     float64 `json:"storageMB"`
}

// ActivitySessionSummary is returned by the Wails binding for listing sessions.
type ActivitySessionSummary struct {
	ID            string   `json:"id"`
	StartTime     int64    `json:"startTime"`
	EndTime       int64    `json:"endTime"`
	TriggerKind   string   `json:"triggerKind"`
	AppNames      []string `json:"appNames"`
	EventCount    int      `json:"eventCount"`
	SnapshotCount int      `json:"snapshotCount"`
	TriageStatus  string   `json:"triageStatus"`
	OCRStatus     string   `json:"ocrStatus"`
}

// OCRMatchFrame represents a matched OCR frame returned to the frontend.
type OCRMatchFrame struct {
	SegmentID  int64   `json:"segmentId"`
	FrameTime  float64 `json:"frameTime"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

// ActivitySessionDetail is returned by the Wails binding for viewing a single session.
type ActivitySessionDetail struct {
	ActivitySessionSummary
	Events    []activitymon.ActivityEvent `json:"events"`
	Segments  []ActivitySegmentRow        `json:"segments"`
	Snapshots []ActivitySnapshotRow       `json:"snapshots"`
}

// ActivityTimelineEvent preserves the raw event plus the capture-session
// metadata needed to recover debug context after app-run grouping.
type ActivityTimelineEvent struct {
	activitymon.ActivityEvent
	EventID          string `json:"eventId"`
	SessionID        string `json:"sessionId"`
	SessionStartTime int64  `json:"sessionStartTime"`
	SessionEndTime   int64  `json:"sessionEndTime"`
	SessionTrigger   string `json:"sessionTrigger"`
	EventIndexInSess int    `json:"eventIndexInSession"`
}

// ActivityTimelineSemanticItem is one model-generated step in an app-run
// summary. EventIDs points back to raw ActivityTimelineEvent.EventID values so
// the UI can jump to the exact recording moment without model timestamps.
type ActivityTimelineSemanticItem struct {
	EventIDs []string `json:"eventIds"`
	Action   string   `json:"action"`
	Target   string   `json:"target,omitempty"`
	Details  []string `json:"details,omitempty"`
}

// ActivityTimelineGroupSummary is the semantic layer cached for one per-app
// timeline group.
type ActivityTimelineGroupSummary struct {
	GroupID      string                         `json:"groupId"`
	Title        string                         `json:"title"`
	Description  string                         `json:"description"`
	ActivityType string                         `json:"activityType"`
	Entities     []string                       `json:"entities"`
	Timeline     []ActivityTimelineSemanticItem `json:"timeline"`
}

// ActivityTimelineGroup is the user-facing Activity timeline unit: one
// contiguous run of the same app across one or more raw capture sessions.
type ActivityTimelineGroup struct {
	ID               string                        `json:"id"`
	AppKey           string                        `json:"appKey"`
	AppName          string                        `json:"appName"`
	StartTime        int64                         `json:"startTime"`
	EndTime          int64                         `json:"endTime"`
	ActiveMs         int64                         `json:"activeMs"`
	EventCount       int                           `json:"eventCount"`
	SessionIDs       []string                      `json:"sessionIds"`
	TriggerKinds     []string                      `json:"triggerKinds"`
	PrimarySessionID string                        `json:"primarySessionId"`
	Events           []ActivityTimelineEvent       `json:"events,omitempty"`
	Summary          *ActivityTimelineGroupSummary `json:"summary,omitempty"`
	IdentityAnchors  []string                      `json:"identityAnchors,omitempty"`
	// FirstFrame is the run's first kept screenshot, nil when none was kept.
	FirstFrame *ActivityRunFrame `json:"firstFrame,omitempty"`
	// SummaryStatus is the persisted run's summary generation state
	// (none|generating|done|skipped|failed_transient|failed_permanent), so
	// the UI can show progress without a summary payload.
	SummaryStatus string `json:"summaryStatus"`
}

// snapshotEntry holds an in-flight JPEG snapshot captured during an activity burst.
type snapshotEntry struct {
	relPath     string
	timestamp   int64
	width       int
	height      int
	sizeBytes   int64
	trigger     string
	axText      string
	browserURL  string
	contentType string // ParsedContent.ContentType, empty when no parser matched
	app         string // ParsedContent.Metadata["app"], empty when no parser matched
	parsedJSON  string // full ParsedContent serialized as JSON, empty when no parser matched
	// Normalized [0,1] bounds of the focused window at the same instant as
	// the capture. Zero means unknown (legacy, multi-display fallback,
	// non-darwin). Consumed by the OCR worker to crop to the focused window
	// before recognition.
	focusRectX float64
	focusRectY float64
	focusRectW float64
	focusRectH float64
	// write is set while the async PII mask still owns the file write; nil
	// when the file was written before the entry joined the session.
	write *snapshotWrite
}

// pendingPIISnapshot keeps a captured JPEG in memory while GLiNER is still
// downloading/loading. Do not persist these bytes before full image-level PII
// detection has run.
type pendingPIISnapshot struct {
	path      string
	jpegData  []byte
	quality   int
	snapshot  snapshotEntry
	bundleID  string
	parseHash string
}

// activityMonitorState holds the runtime state for the activity monitor.
// It is managed by App-level methods; not a standalone struct with its own goroutines.
type activityMonitorState struct {
	mu                sync.Mutex
	monitor           activitymon.Monitor
	capture           activitymon.ScreenCapture
	buf               *activitybuf.RingBuffer
	store             *ActivityStore
	rotator           *StorageRotator
	triager           *ActivityTriager
	ocrWorker         *ActivityOCRWorker
	ocrEngine         ocr.Engine   // dedicated OCR engine for PII masking
	piiDetector       pii.Detector // ONNX-based PII detector
	piiDetectorMu     sync.RWMutex // guards piiDetector while async init / Detect / Destroy overlap
	piiInitInFlight   bool
	piiInitRetryAfter time.Time
	piiSem            chan struct{} // semaphore to limit concurrent PII goroutines
	captureSem        chan struct{} // semaphore to prevent overlapping full-screen captures
	// lastPIIResult caches the merged detection slice from the previous PII
	// run so back-to-back frames with byte-identical OCR output skip a full
	// ONNX inference. See runAsyncPIIMasking for correctness rationale.
	lastPIIResult atomic.Pointer[piiCacheEntry]
	lastPIIOCRMs  atomic.Int64

	// lastAXWalkMsByBundle throttles the accessibility-tree walk per foreground
	// app for heavy Chromium/CEF apps we neither prime nor parse (see
	// isHeavyAXWalkApp). Repeatedly walking their AX tree forces the app to
	// rebuild a live accessibility tree on its UI thread, which starves the
	// foreground app and makes typing sluggish (observed with DingTalk).
	// Guarded by mu.
	lastAXWalkMsByBundle map[string]int64

	// ocrBoxesFlight collapses concurrent on-demand highlight OCR calls for
	// the same snapshot into one engine run (the result is keyword-
	// independent). See getOrComputeOCRBoxes in activity_ocr_boxes.go.
	ocrBoxesFlight singleflight.Group
	// ocrBoxesSem bounds how many of those engine runs execute at once.
	// singleflight only dedupes the SAME frame; a tag click enabling many
	// distinct uncached frames used to spawn that many parallel Accurate OCR
	// helper processes, and the contention pushed per-frame latency from ~1s
	// to tens of seconds (observed p90 50s). Queued callers proceed in
	// request order, so viewport-visible frames naturally resolve first.
	ocrBoxesSem chan struct{}

	// activityRunFramesCache keeps the representative screenshot metadata for
	// timeline runs. The Activities tab asks for the same run frames repeatedly
	// across the main list, outline refreshes, and drill-downs; caching avoids
	// re-reading full session snapshot rows when the run has not changed.
	activityRunFramesMu    sync.Mutex
	activityRunFramesCache map[string][]ActivityRunFrame

	enabled         bool
	sessionCount    int
	lastAppBundleID string
	lastEventTime   int64
	idleTimer       *time.Timer

	// Event-driven snapshot state
	lastCaptureTime          int64           // debounce: min interval between captures
	lastCaptureBundleID      string          // bundle ID of the most recent capture; drives the per-app debounce so switching to a different app doesn't wait out the same-app cool-down
	typingTimer              *time.Timer     // capture after typing pause
	heartbeatTimer           *time.Timer     // periodic capture during activity
	allSnapshots             []snapshotEntry // accumulated during activity burst
	pendingPIISnapshots      []pendingPIISnapshot
	pendingPIIBytes          int64
	pendingPIIDraining       bool
	pendingPIIExtractTrigger string
	comparer                 frameComparer // visual change detection; updated after every capture
	lastSavedTime            int64         // for max-skip safety valve
	visualStopCh             chan struct{} // signal to stop visual change goroutine
	stateEventWindowMs       int64         // start of the current app-switch/focus burst window
	stateEventCount          int           // app-switch/focus events seen in the current burst window
	stateEventChurnUntilMs   int64         // extend capture floor while high-frequency window churn is active
	visualChurnStreak        int           // consecutive high-visualDist captures (video/scroll); drives visual-churn pacing
	visualChurnUntilMs       int64         // raise the capture floor until this time while screen content is in constant motion
	lastChurnCaptureMs       int64         // last capture that ran the pipeline; paces every entry point uniformly
	lastChurnFrameMs         int64         // time of the last in-motion frame counted toward the streak (time-bounds "consecutive")
	lastChurnBundle          string        // app bundle of the last in-motion frame; an app/window change restarts the streak
	lastUserActivityTime     int64         // last non-speech input/window event; speech-only sessions should not drive visual captures

	// Typed-text segment tracker (IME-aware committed text). A segment spans
	// one stretch of typing into one field: opened by a burst-start keydown
	// (whose focused-field snapshot is the diff baseline) and closed by a
	// commit key, click, typing pause, or app/focus change. See
	// activity_typed_text.go.
	typedSegmentOpen        bool
	typedSegmentBaseline    string
	typedSegmentValid       bool
	typedSegmentLatest      string
	typedSegmentLatestValid bool
	typedSegmentBundleID    string
	typedSegmentAppName     string
	typedSegmentWindowTitle string

	configDir string

	lastWindowTitle string // window title from most recent event
	// lastWindowTitleBundleID is the app lastWindowTitle came from.
	lastWindowTitleBundleID string

	// Per-bundleID cache of the most recent snapshot's parser content
	// hash for the current session burst. When the next snapshot's
	// parser output hashes to the same value, we elide its parsed_json
	// down to a `{"dedupOfHash": "..."}` stub so the row still records
	// the moment but doesn't redundantly persist (or later re-LLM) the
	// same Slack/chat conversation. Cleared when allSnapshots resets at
	// session end. Guarded by mu like the rest of the capture state.
	lastParsedHashByBundle map[string]string
	// lastParsedWriteByBundle is the file write of the snapshot that set
	// lastParsedHashByBundle. A later identical frame is elided to a dedup
	// stub only once that write has landed: a referent the PII mask drops
	// before its session is saved never becomes a row, and the stub would
	// resolve to nothing.
	lastParsedWriteByBundle map[string]*snapshotWrite
	// pendingSnapshotSessions counts committed rows whose masked file is
	// still being written, so the OCR worker can wait for them.
	pendingSnapshotSessions pendingSnapshotSessions

	// Periodic flush for speech events so long meetings don't lose data
	// to the ring buffer's time-window eviction.
	firstSpeechTime int64 // timestamp (ms) of first unflushed speech event; 0 = none
	lastSpeechMs    int64 // timestamp (ms) of most recent speech event; never cleared by extractSession

	// Meeting state: owned by the meeting lifecycle watcher goroutine.
	// inMeeting flips true on MEETING_STARTED (active meeting window detected
	// for meetingTransitionPolls consecutive polls) and false on MEETING_ENDED.
	// The idle timer is suppressed while inMeeting==true — the watcher owns
	// session boundaries for meetings.
	inMeeting             bool
	meetingAppName        string // "Zoom", "Google Meet", etc.
	meetingBundleID       string // "us.zoom.xos", etc.
	meetingStartMs        int64  // Unix ms when current meeting started; 0 = not in meeting
	meetingRecordingEpoch uint64 // preference generation that admitted the current automatic meeting
	// meetingID is the UUID shared by every chat session this meeting
	// produces (transcripts + handoffs). Generated on the false→true edge
	// in onMeetingStarted; cleared on MEETING_ENDED. Empty when not in a
	// meeting.
	meetingID string
	// Last meeting's id / app / end timestamp + the meeting app's PID at
	// the moment we minted that id. The PID is the primary fingerprint
	// for resume decisions: if the meeting app's process is still alive
	// with the SAME PID when the next MEETING_STARTED arrives, it's by
	// definition the same call (the user never quit Zoom — the watcher
	// just lost the meeting window title for a stretch). lastMeetingEndMs
	// is the secondary fallback when the PID can't be resolved at decision
	// time. See onMeetingStarted's resume logic.
	lastMeetingID     string
	lastMeetingApp    string
	lastMeetingPID    int
	lastMeetingPaused bool // known lock/observation boundary, not an ordinary call end
	lastMeetingEndMs  int64
	// endedMeetingSpanStartMs and endedMeetingSpanEndMs are the time span of
	// the last call's saved meeting session. Speech the transcriber finishes
	// after the call's end still says it was captured in the call; when it all
	// falls inside this span it belongs to that saved call (whose transcript
	// comes from its full recording) and never becomes a second meeting session.
	endedMeetingSpanStartMs int64
	endedMeetingSpanEndMs   int64
	meetingWatcherCh        chan struct{}       // closed to stop the watcher goroutine
	lastMeetingEventMs      int64               // Unix ms of most recent event matching IsMeetingEvent; 0 = none
	lastMeetingAudioMs      int64               // Unix ms of recent meeting audio captured before ASR returns; 0 = none
	meetingAudioWriter      *meetingAudioWriter // streams raw PCM to disk during meetings; nil when not recording
	// meetingBrowserMicReleased: the browser of a browser call let go of
	// the microphone, so the call is over (callChunkOnMicrophoneLocked).
	meetingBrowserMicReleased bool
	// meetingBrowserGapSinceMs is when the browser of a browser call last
	// let go of the microphone (0 while it holds it); meetingBrowserObservedMs
	// the latest observation (noteBrowserCallHoldLocked).
	meetingBrowserGapSinceMs int64
	meetingBrowserObservedMs int64

	// Conservative fallback state for supported calls the watcher did not
	// confirm. Candidate sessions are judged holistically at extraction time and
	// only promoted to meeting transcripts when the full evidence is strong.
	meetingCandidateActive         bool
	meetingCandidateAppName        string
	meetingCandidateStartMs        int64
	meetingCandidateRecordingEpoch uint64
	lastMeetingCandidateAudioMs    int64
	// meetingCandidateFrontmostStreak counts microphone chunks in a row
	// with the frontmost-meeting-app evidence (background_transcription_filter.go).
	meetingCandidateFrontmostStreak int
	meetingCandidateAudioWriter     *meetingAudioWriter

	// Periodic session flusher: forces extractSession on a wall-clock cadence
	// so long stretches of active work (no idle window, no speech) still
	// produce timely activity rows. Without this the timeline can lag by
	// many minutes while the user is mid-stream. Closed to stop.
	periodicFlushStopCh chan struct{}

	// Capture failure backoff: avoid hammering GDI when captures fail repeatedly.
	captureFailCount      int       // consecutive capture failures
	captureBackoff        time.Time // don't attempt capture before this time
	captureBurstStartedAt time.Time // first failure in the current burst (zero outside a burst)
}

const (
	idleThreshold = 30 * time.Second
	// Meeting lifecycle watcher tunables. The watcher polls meeting windows
	// every meetingPollInterval and requires meetingTransitionPolls consecutive
	// same-state polls before transitioning (absorbs title flicker).
	meetingPollInterval    = 15 * time.Second
	meetingTransitionPolls = 2
	// How long recent call evidence keeps a meeting "sticky": the watcher
	// won't fire MEETING_ENDED while a known meeting app is running and the
	// evidence is this recent. Covers transient title changes during screen
	// sharing, view toggles, and Zoom toolbar animations.
	meetingEventStickyMs = 60_000  // 1 minute — recent meeting-app interaction prevents MEETING_ENDED
	meetingAudioStickyMs = 180_000 // 3 minutes past the meeting app's last hold on the microphone
	// A running process is not a call identity: Zoom can host many calls
	// without exiting. Bound every rejoin, including a matching native PID.
	meetingResumeWindowMs int64 = 30 * 60 * 1000
	// Per-app capture debounce
	// ─────────────────────────
	// snapshotDebounceMs applies only when the user re-focuses the SAME app
	// they were just captured on — stops refocus/alt-tab oscillation from
	// producing near-identical captures of the same window.
	//
	// minCaptureIntervalMs is the absolute floor between any two captures,
	// present to protect the JPEG+OCR+PII pipeline from melting. Cross-app
	// switches only wait this floor, so "glanced at Notes → back to iTerm"
	// — previously silently dropped by a single 1.5s global window — now
	// produces a Notes capture while you're still on Notes.
	snapshotDebounceMs              = 10000 // same-app refocus cool-down
	minCaptureIntervalMs            = 1000  // hard floor between any two captures
	windowChurnWindowMs             = 2000  // burst window for high-frequency app-switch/focus events
	windowChurnThreshold            = 8     // events within windowChurnWindowMs before capture pacing kicks in
	windowChurnCooldownMs           = 5000  // how long capture pacing stays active after a detected burst
	windowChurnMinCaptureIntervalMs = 3000  // hard floor while high-frequency window churn is active
	// Visual-churn pacing: video playback and continuous scrolling change most
	// of the screen every frame, so visualDist stays high and the dedup gate
	// never skips — the full CaptureRaw + per-window JPEG encode + OCR pipeline
	// then fires every minCaptureIntervalMs (measured: drives the main process
	// to ~100% while a video plays). After a streak of in-motion frames we raise
	// the floor so that pipeline backs off ~6x; per-frame snapshots of a playing
	// video carry no activity-tracking value anyway. Distinct from windowChurn
	// above, which paces app-switch/focus storms, not screen-content motion.
	visualChurnDistThreshold        = 0.15  // Hellinger dist above which a frame counts as "in motion" (dedup floor is 0.02)
	visualChurnStreakThreshold      = 3     // consecutive in-motion captures before pacing kicks in
	visualChurnCooldownMs           = 8000  // pacing stays active this long after the last in-motion capture (> floor so it stays engaged)
	visualChurnMinCaptureIntervalMs = 6000  // hard floor while visual churn is active
	visualChurnStreakWindowMs       = 12000 // max gap between in-motion frames still counted as one streak; bounds "consecutive" in time (> floor so active pacing doesn't self-reset). Beyond it, or on app/window change, the streak restarts.
	typingPauseMs                   = 1000  // capture after typing pause
	calendarTypingPauseMs           = 700   // Google Calendar editor updates are easy to miss with full-screen visual dedup
	// Slide-show navigation: a manually-paged Keynote/PowerPoint review
	// fires one Page Up/Down/Home/End per slide. The 10s
	// visualCheckInterval drops most of those, so we force-capture on
	// the keystroke itself. piiSem (cap=1) is the implicit rate-limit
	// when the user holds the key down or pages rapidly.
	calendarSaveFollowupDelay   = 750 * time.Millisecond
	heartbeatInterval           = 60 * time.Second // periodic capture during activity
	maxSkipDuration             = 60 * time.Second // force capture after this long without saving
	memoryTrimSnapshotThreshold = 12               // trim retained capture memory after snapshot-heavy sessions
	memoryTrimSizeBytes         = 50 * 1024 * 1024 // trim retained capture memory after large sessions
	speechFlushInterval         = 10 * time.Minute // flush speech events periodically so long meetings don't lose data
	meetingFlushInterval        = 2 * time.Hour    // keep meetings as one contiguous session; only flush for very long calls
	// periodicFlushInterval forces extractSession on a wall-clock cadence
	// regardless of idle / speech state. Catches the active-but-quiet case:
	// user is busy typing/clicking with no idle window and no speech, so the
	// idle_transition (30s) and speech_idle_transition (10m) triggers never
	// fire — without this the activity timeline could go an hour without
	// updating. 5 minutes balances UI freshness against the synchronous
	// cost of writing a session (events + snapshots) to SQLite.
	periodicFlushInterval = 5 * time.Minute
	snapshotQuality       = 50 // JPEG quality 1-100 (lower = smaller files, 50 is fine for screen content)
	// Global PII OCR pacing keeps Apple Vision/CoreML work from spiking during
	// rapid app switching. Frames inside the cooldown are dropped, including
	// frames with a blurred sensitive window: the rest of such a frame still
	// needs the OCR-based mask.
	piiOCRMinInterval = 3 * time.Second

	// Visual change detection — mirrors screenpipe's VisualChange trigger.
	// Captures when screen content changes without user input (video, animation, etc).
	visualCheckInterval   = 10 * time.Second // low-priority background visual-change check
	visualActivityWindow  = idleThreshold    // stop visual polling once only speech/background events are keeping the session alive
	visualChangeThreshold = 0.05             // Hellinger distance; above this = changed (screenpipe: 0.05)

	// Capture failure backoff: exponential backoff when screen capture fails
	// repeatedly (e.g. GetDIBits failures on Windows during DWM transitions).
	captureBackoffBase = 500 * time.Millisecond // initial backoff after first failure
	captureBackoffMax  = 30 * time.Second       // cap backoff at 30s

	// sessionEndCaptureWait bounds how long the one-shot session_end capture
	// waits for a contended capture slot / PII slot instead of dropping the
	// session's only guaranteed frame. Every session must end up with at
	// least one snapshot; transient collisions with an in-flight capture or
	// a still-running PII pass are worth waiting out at a session boundary.
	sessionEndCaptureWait = 5 * time.Second

	// First-run / upgrade PII guard: while GLiNER is downloading/loading, keep
	// screenshots in RAM only. The cap is deliberately small so a slow network
	// cannot turn raw screenshot retention into an unbounded memory problem.
	pendingPIIQueueMaxFrames = 24
	pendingPIIQueueMaxBytes  = 64 * 1024 * 1024
	piiInitRetryDelay        = 5 * time.Minute
)

func (a *App) armActivityIdleTimerLocked(am *activityMonitorState, triggerKind string) {
	if am.idleTimer != nil {
		am.idleTimer.Stop()
		am.idleTimer = nil
	}
	// During meetings, the lifecycle watcher owns session boundaries. Don't
	// arm an idle timer — silent stretches during a long call are expected
	// and must not cause the session to split.
	if am.inMeeting {
		return
	}
	threshold := idleThreshold
	// When speech is actively accumulating (someone is talking), use a much
	// longer idle threshold. This prevents splitting transcripts for meeting
	// platforms we don't recognize (Preply, webinars, etc.) where the
	// lifecycle watcher can't suppress the timer.
	if am.firstSpeechTime > 0 || am.meetingCandidateActive {
		threshold = 5 * time.Minute
	}
	am.idleTimer = time.AfterFunc(threshold, func() {
		a.extractSession(triggerKind)
	})
}

func (a *App) ensureActivityTriagerStarted() {
	if a.ctx == nil || a.activity == nil || a.activity.store == nil {
		return
	}

	mc := a.getMemoryGenerator()
	if mc == nil {
		return
	}

	am := a.activity
	am.mu.Lock()
	if am.triager != nil {
		am.mu.Unlock()
		return
	}
	triager := NewActivityTriager(a, mc)
	am.triager = triager
	am.mu.Unlock()

	go triager.Run(a.ctx)
	log.Printf("[activity] triager started")
}

func stopActivityTimersLocked(am *activityMonitorState) {
	if am.idleTimer != nil {
		am.idleTimer.Stop()
		am.idleTimer = nil
	}
	if am.typingTimer != nil {
		am.typingTimer.Stop()
		am.typingTimer = nil
	}
	if am.heartbeatTimer != nil {
		am.heartbeatTimer.Stop()
		am.heartbeatTimer = nil
	}
	if am.visualStopCh != nil {
		close(am.visualStopCh)
		am.visualStopCh = nil
	}
}

func collectSpeechEventStats(events []activitymon.ActivityEvent) (int, int) {
	count := 0
	totalChars := 0

	for _, evt := range events {
		if evt.Kind != activitymon.EventSpeech {
			continue
		}

		text := strings.TrimSpace(evt.SpeechText)
		if text == "" {
			continue
		}

		count++
		totalChars += len([]rune(text))
	}

	return count, totalChars
}

// initActivityMonitor sets up the activity monitoring subsystem.
// Called from startup() when permissions are granted.
// prepareActivityReads opens the persisted source and privacy ledgers without
// starting capture, recovery, inference or a background writer.
func (a *App) prepareActivityReads() {
	if a.activity != nil {
		return
	}
	baseDir, err := flavorDataDir()
	if err != nil {
		log.Printf("[activity] UserHomeDir failed: %v", err)
		return
	}

	store, err := openActivityStore(baseDir, false)
	if err != nil {
		log.Printf("[activity] failed to create activity store: %v", err)
		return
	}

	// Buffer window must be >= meetingFlushInterval so early meeting speech
	// isn't evicted before the session is extracted.
	buf := activitybuf.NewRingBuffer(5000, meetingFlushInterval+time.Minute)
	mon := activitymon.New()
	cap := activitymon.NewScreenCapture()
	rotator := NewStorageRotator(store, activityRotatorConfigFor(readActivityRetentionConfig()))

	ocrEng := ocr.New()

	a.activity = &activityMonitorState{
		monitor:     mon,
		capture:     cap,
		buf:         buf,
		store:       store,
		rotator:     rotator,
		ocrEngine:   ocrEng,
		piiSem:      make(chan struct{}, 1), // serialize PII work — was 2; parallel inferences hammered all cores
		captureSem:  make(chan struct{}, 1),
		ocrBoxesSem: make(chan struct{}, ocrHighlightMaxConcurrent),
		configDir:   baseDir,
	}
	// From here on, deleting an activity session or an activity also deletes
	// the commitments that were learned from it (purpose_purge.go).
	purposePurgeApp.Store(a)
}

func (a *App) initActivityMonitor() {
	a.prepareActivityReads()
	a.startActivityMaintenance()
}

func (a *App) startActivityMaintenance() <-chan struct{} {
	if a.activity == nil || a.activity.store == nil {
		return nil
	}
	store := a.activity.store
	// Queue delays must not make this run's in-flight writes look abandoned.
	beforeMs := launchStartedAt.UnixMilli()
	a.deferLaunchTask("snapshot_reconcile", func() { reconcileSnapshotFiles(store, beforeMs) })
	recovered := a.deferLaunchTask("meeting_audio_recovery", func() { a.recoverMeetingAudio(store, meetingTranscriptRecoveryCutoff) })
	a.ensureActivityTriagerStarted()
	return recovered
}

func (a *App) startPIIDetectorAsync(am *activityMonitorState) {
	if am == nil || am.configDir == "" || am.ocrEngine == nil || !am.ocrEngine.IsSupported() {
		return
	}

	am.piiDetectorMu.Lock()
	if am.piiDetector != nil || am.piiInitInFlight {
		am.piiDetectorMu.Unlock()
		return
	}
	if !am.piiInitRetryAfter.IsZero() && time.Now().Before(am.piiInitRetryAfter) {
		am.piiDetectorMu.Unlock()
		return
	}
	am.piiInitInFlight = true
	am.piiInitRetryAfter = time.Time{}
	am.piiDetectorMu.Unlock()

	log.Printf("[activity] PII detector init started in background")
	go func() {
		det, err := pii.NewDetector(am.configDir, nil)

		am.piiDetectorMu.Lock()
		am.piiInitInFlight = false

		if err != nil {
			am.piiInitRetryAfter = time.Now().Add(piiInitRetryDelay)
			am.piiDetectorMu.Unlock()
			log.Printf("[activity] PII detector init failed (GLiNER disabled, regex masking still active): %v", err)
			a.dropPendingPIISnapshots(am, "PII detector init failed")
			a.resumeDeferredPIIExtract(am)
			return
		}

		am.mu.Lock()
		enabled := am.enabled
		am.mu.Unlock()
		if a.activity != am || !enabled {
			am.piiDetectorMu.Unlock()
			det.Destroy()
			log.Printf("[activity] PII detector init finished after monitor stopped; discarded detector")
			a.dropPendingPIISnapshots(am, "activity monitor stopped before PII ready")
			a.resumeDeferredPIIExtract(am)
			return
		}

		if old := am.piiDetector; old != nil {
			old.Destroy()
		}
		am.piiDetector = det
		am.piiInitRetryAfter = time.Time{}
		am.lastPIIResult.Store(nil)
		am.piiDetectorMu.Unlock()
		log.Printf("[activity] PII detector ready for activity masking")
		if !a.startPendingPIIDrain(am) {
			a.resumeDeferredPIIExtract(am)
		}
	}()
}

func (am *activityMonitorState) piiDetectorState() (ready bool, initInFlight bool) {
	am.piiDetectorMu.RLock()
	defer am.piiDetectorMu.RUnlock()
	return am.piiDetector != nil && am.piiDetector.IsReady(), am.piiInitInFlight
}

func (a *App) fillPIIPerfStats(stats *PerfPIIPipelineStats) {
	if a == nil || stats == nil {
		return
	}
	am := a.activity
	if am == nil {
		stats.Mode = "unavailable"
		return
	}

	am.piiDetectorMu.RLock()
	ready := am.piiDetector != nil && am.piiDetector.IsReady()
	initInFlight := am.piiInitInFlight
	retryAfter := am.piiInitRetryAfter
	am.piiDetectorMu.RUnlock()

	am.mu.Lock()
	ocrSupported := am.ocrEngine != nil && am.ocrEngine.IsSupported()
	stats.QueuedFrames = len(am.pendingPIISnapshots)
	stats.QueuedBytes = am.pendingPIIBytes
	stats.Draining = am.pendingPIIDraining
	am.mu.Unlock()

	stats.Ready = ready
	stats.InitInFlight = initInFlight
	switch {
	case !ocrSupported:
		stats.Mode = "unavailable"
	case ready:
		stats.Mode = "gliner"
	case initInFlight:
		stats.Mode = "initializing"
	case !retryAfter.IsZero() && time.Now().Before(retryAfter):
		stats.Mode = "regex_only_retry"
		stats.RetryAfterMs = time.Until(retryAfter).Milliseconds()
	default:
		stats.Mode = "regex_only"
	}
}

func (am *activityMonitorState) queuePendingPIISnapshot(frame pendingPIISnapshot) bool {
	if am == nil || len(frame.jpegData) == 0 {
		return false
	}
	size := int64(len(frame.jpegData))
	if size > pendingPIIQueueMaxBytes {
		log.Printf("[pii-mask] dropping pending screenshot larger than queue cap (%d > %d bytes)",
			size, int64(pendingPIIQueueMaxBytes))
		return false
	}

	am.mu.Lock()
	if !am.enabled {
		am.mu.Unlock()
		return false
	}

	dropped := 0
	for len(am.pendingPIISnapshots) > 0 &&
		(len(am.pendingPIISnapshots) >= pendingPIIQueueMaxFrames ||
			am.pendingPIIBytes+size > int64(pendingPIIQueueMaxBytes)) {
		old := am.pendingPIISnapshots[0]
		am.pendingPIIBytes -= int64(len(old.jpegData))
		old.jpegData = nil
		am.pendingPIISnapshots[0] = pendingPIISnapshot{}
		am.pendingPIISnapshots = am.pendingPIISnapshots[1:]
		dropped++
	}

	am.pendingPIISnapshots = append(am.pendingPIISnapshots, frame)
	am.pendingPIIBytes += size
	am.lastCaptureTime = frame.snapshot.timestamp
	am.lastCaptureBundleID = frame.bundleID
	am.lastSavedTime = frame.snapshot.timestamp
	queueLen := len(am.pendingPIISnapshots)
	queueBytes := am.pendingPIIBytes
	am.mu.Unlock()

	if dropped > 0 {
		log.Printf("[pii-mask] pending PII queue dropped %d oldest frame(s) to stay within cap", dropped)
	}
	if queueLen == 1 || dropped > 0 || queueLen == pendingPIIQueueMaxFrames {
		log.Printf("[pii-mask] queued screenshot until GLiNER is ready (queued=%d, bytes=%d)",
			queueLen, queueBytes)
	}
	return true
}

func (a *App) dropPendingPIISnapshots(am *activityMonitorState, reason string) {
	if am == nil {
		return
	}
	am.mu.Lock()
	count := len(am.pendingPIISnapshots)
	bytes := am.pendingPIIBytes
	for i := range am.pendingPIISnapshots {
		am.pendingPIISnapshots[i].jpegData = nil
	}
	am.pendingPIISnapshots = nil
	am.pendingPIIBytes = 0
	am.mu.Unlock()

	if count > 0 {
		log.Printf("[pii-mask] dropped %d pending screenshot(s), %d bytes (%s)", count, bytes, reason)
	}
}

func (a *App) startPendingPIIDrain(am *activityMonitorState) bool {
	if a == nil || am == nil {
		return false
	}
	ready, _ := am.piiDetectorState()
	if !ready {
		return false
	}

	am.mu.Lock()
	if am.pendingPIIDraining || len(am.pendingPIISnapshots) == 0 {
		am.mu.Unlock()
		return false
	}
	am.pendingPIIDraining = true
	am.mu.Unlock()

	go a.drainPendingPIISnapshots(am)
	return true
}

func (a *App) drainPendingPIISnapshots(am *activityMonitorState) {
	for {
		am.mu.Lock()
		if len(am.pendingPIISnapshots) == 0 {
			am.pendingPIIDraining = false
			am.mu.Unlock()
			a.resumeDeferredPIIExtract(am)
			return
		}
		frames := am.pendingPIISnapshots
		queueBytes := am.pendingPIIBytes
		am.pendingPIISnapshots = nil
		am.pendingPIIBytes = 0
		am.mu.Unlock()

		log.Printf("[pii-mask] draining %d queued screenshot(s) through GLiNER (bytes=%d)",
			len(frames), queueBytes)
		for i := range frames {
			frame := frames[i]
			if a.shouldDiscardPIIOutput(am) {
				frame.jpegData = nil
				frames[i] = pendingPIISnapshot{}
				continue
			}

			am.piiSem <- struct{}{}
			activityPipelineMetrics.piiLaunched.Add(1)
			activityPipelineMetrics.piiInFlight.Add(1)
			piiStart := time.Now()
			writtenBytes, ok, ocrErr := am.maskAndWritePIISnapshot(frame.path, frame.jpegData, frame.quality, func() bool {
				return a.shouldDiscardPIIOutput(am)
			})
			activityPipelineMetrics.piiDurations.add(time.Since(piiStart))
			activityPipelineMetrics.piiInFlight.Add(-1)
			<-am.piiSem

			if ok {
				frame.snapshot.sizeBytes = writtenBytes
				am.appendCapturedSnapshot(frame.snapshot, frame.bundleID, frame.parseHash)
			}
			frame.jpegData = nil
			frames[i] = pendingPIISnapshot{}

			// A helper that overran even its cold-start budget is wedged;
			// each further frame would hold its unmasked JPEG in memory for
			// another full budget. Drop the rest of this batch.
			var timeoutErr *ocr.HelperTimeoutError
			if errors.As(ocrErr, &timeoutErr) && timeoutErr.Cold {
				dropped := dropPendingPIIFrames(frames[i+1:])
				if dropped > 0 {
					log.Printf("[pii-mask] OCR helper overran its cold-start budget; dropped %d queued screenshot(s)", dropped)
				}
				break
			}
		}
	}
}

// dropPendingPIIFrames releases the JPEGs of frames that will not be masked
// and returns how many held one.
func dropPendingPIIFrames(frames []pendingPIISnapshot) int {
	dropped := 0
	for i := range frames {
		if frames[i].jpegData != nil {
			dropped++
		}
		frames[i] = pendingPIISnapshot{}
	}
	return dropped
}

func (a *App) shouldDiscardPIIOutput(am *activityMonitorState) bool {
	if a == nil || am == nil || a.IsClosedEyesModeEnabled() {
		return true
	}
	am.mu.Lock()
	enabled := am.enabled
	am.mu.Unlock()
	return !enabled || a.activity != am
}

func (a *App) deferExtractSessionUntilPIIReady(am *activityMonitorState, triggerKind string) bool {
	if a == nil || am == nil || am.ocrEngine == nil || !am.ocrEngine.IsSupported() {
		return false
	}
	if triggerKind == "shutdown_in_meeting" {
		return false
	}
	ready, initInFlight := am.piiDetectorState()

	am.mu.Lock()
	if !am.enabled {
		am.mu.Unlock()
		return false
	}
	pending := len(am.pendingPIISnapshots) > 0 || am.pendingPIIDraining
	if ready && !pending {
		am.mu.Unlock()
		return false
	}
	if !initInFlight && !pending {
		am.mu.Unlock()
		return false
	}
	prev := am.pendingPIIExtractTrigger
	am.pendingPIIExtractTrigger = mergeDeferredPIIExtractTrigger(prev, triggerKind)
	queueLen := len(am.pendingPIISnapshots)
	draining := am.pendingPIIDraining
	am.mu.Unlock()

	if prev == "" {
		log.Printf("[activity] deferring session extraction until PII queue is clear (trigger=%s, queued=%d, draining=%t, init_in_flight=%t)",
			triggerKind, queueLen, draining, initInFlight)
	}
	return true
}

func mergeDeferredPIIExtractTrigger(prev, next string) string {
	if prev == "" || next == "shutdown_in_meeting" || next == "meeting_ended" {
		return next
	}
	if prev == "periodic_flush" && next != "periodic_flush" {
		return next
	}
	return prev
}

func (a *App) resumeDeferredPIIExtract(am *activityMonitorState) {
	if a == nil || am == nil {
		return
	}
	am.mu.Lock()
	if len(am.pendingPIISnapshots) > 0 || am.pendingPIIDraining {
		am.mu.Unlock()
		return
	}
	trigger := am.pendingPIIExtractTrigger
	am.pendingPIIExtractTrigger = ""
	am.mu.Unlock()

	if trigger == "" || a.IsClosedEyesModeEnabled() || a.activity != am {
		return
	}
	log.Printf("[activity] resuming deferred session extraction after PII queue cleared (trigger=%s)", trigger)
	go a.extractSession(trigger)
}

// startActivityMonitor begins monitoring user activity.
func (a *App) startActivityMonitor() error {
	if a.activity == nil {
		return fmt.Errorf("activity monitor not initialized")
	}
	if a.IsClosedEyesModeEnabled() {
		log.Printf("[activity] closed-eyes mode active, skipping activity monitor start")
		return nil
	}

	am := a.activity
	am.mu.Lock()
	if am.enabled {
		am.mu.Unlock()
		return nil
	}
	am.mu.Unlock()

	// Start event monitoring
	err := am.monitor.Start(a.ctx, func(evt activitymon.ActivityEvent) {
		if a.IsClosedEyesModeEnabled() {
			return
		}
		// A private or incognito window keeps no keystrokes, window title
		// or element labels, not only no screenshots. The full event still
		// reaches handleActivityEvent, which needs the title to tell that
		// the window is private.
		am.mu.Lock()
		private := privateWindowEventLocked(am, evt)
		am.mu.Unlock()
		if stored, keep := scrubPrivateWindowEvent(evt, private); keep {
			am.buf.Push(stored)
		}
		a.handleActivityEvent(evt)
	})
	if err != nil {
		return fmt.Errorf("start activity monitor: %w", err)
	}

	am.mu.Lock()
	am.enabled = true
	am.mu.Unlock()

	// GLiNER can require a one-time 665 MB fp32 model download after upgrade.
	// Start it in the background; screenshots captured before readiness stay in
	// a bounded in-memory queue so GLiNER-only PII never lands on disk first.
	a.startPIIDetectorAsync(am)

	// Start storage rotator. Its daily sweep leaves the frames the capture
	// still holds alone, and a call recording stays as it is until its
	// transcript is final.
	am.rotator.framesInUse = am.storageFramesInUse
	am.rotator.recordingFinal = a.storageRecordingFinal
	go am.rotator.Run(a.ctx)

	// Start triager even when full screen/input monitoring permissions
	// are unavailable, so speech-only sessions can still be processed.
	a.ensureActivityTriagerStarted()

	// Start OCR worker
	am.ocrWorker = NewActivityOCRWorker(a)
	go am.ocrWorker.Run(a.ctx)

	// Start visual change detection goroutine
	am.mu.Lock()
	am.visualStopCh = make(chan struct{})
	am.mu.Unlock()
	go a.runVisualChangeDetection()

	// Start meeting lifecycle watcher. Owns meeting session boundaries
	// (start / end) independently of speech events or idle timers.
	am.mu.Lock()
	am.meetingWatcherCh = make(chan struct{})
	watcherStop := am.meetingWatcherCh
	am.mu.Unlock()
	go a.runMeetingLifecycleWatcher(watcherStop, am)

	// Start periodic session flusher. Keeps the activity timeline current
	// during long active-but-quiet stretches that would otherwise wait for
	// an idle window.
	am.mu.Lock()
	am.periodicFlushStopCh = make(chan struct{})
	flushStop := am.periodicFlushStopCh
	am.mu.Unlock()
	go a.runPeriodicSessionFlusher(flushStop)

	// Set a soft memory limit so the Go runtime GC keeps RSS in check
	// without the latency spikes of periodic forced debug.FreeOSMemory()
	// calls. The runtime will trigger GC automatically as the heap
	// approaches this limit.
	debug.SetMemoryLimit(512 * 1024 * 1024) // 512 MB soft limit

	a.emitActivityStatus()
	updateMenuBarStatus(0) // recording
	log.Printf("[activity] monitoring started (event-driven snapshots)")
	return nil
}

// stopActivityMonitor stops all activity monitoring.
func (a *App) stopActivityMonitor() {
	a.stopActivityMonitorWithOptions(true, false)
}

func (a *App) stopActivityMonitorForClosedEyes() {
	a.stopActivityMonitorWithOptions(false, true)
}

func (a *App) stopActivityMonitorWithOptions(flushInMeeting bool, discardPending bool) {
	if a.activity == nil {
		return
	}

	am := a.activity
	am.mu.Lock()
	if !am.enabled {
		triager := am.triager
		am.triager = nil
		stopActivityTimersLocked(am)
		am.mu.Unlock()
		if discardPending {
			a.discardPendingActivityRecording(am)
		}
		if triager != nil {
			triager.Stop()
		}
		return
	}
	am.enabled = false
	triager := am.triager
	am.triager = nil
	// Stop the meeting lifecycle watcher.
	if am.meetingWatcherCh != nil {
		close(am.meetingWatcherCh)
		am.meetingWatcherCh = nil
	}
	// Stop the periodic session flusher.
	if am.periodicFlushStopCh != nil {
		close(am.periodicFlushStopCh)
		am.periodicFlushStopCh = nil
	}
	wasInMeeting := am.inMeeting
	wasCandidateActive := am.meetingCandidateActive
	am.mu.Unlock()

	// If we were mid-meeting at shutdown, flush so we don't lose the transcript.
	if flushInMeeting && wasInMeeting {
		a.extractSession("shutdown_in_meeting")
	} else if flushInMeeting && wasCandidateActive {
		a.extractSession("shutdown_meeting_candidate")
	}

	am.monitor.Stop()

	am.mu.Lock()
	stopActivityTimersLocked(am)
	for i := range am.pendingPIISnapshots {
		am.pendingPIISnapshots[i].jpegData = nil
	}
	am.pendingPIISnapshots = nil
	am.pendingPIIBytes = 0
	am.pendingPIIDraining = false
	am.pendingPIIExtractTrigger = ""
	// Paused mid-call: the call ends here and keeps its recording so far.
	var pausedCall *meetingAudioWriter
	var pausedEpoch uint64
	if discardPending {
		pausedCall, pausedEpoch = endCallForObservationPauseLocked(am)
	}
	// Abort any dangling audio writer not handled by shutdown_in_meeting.
	if am.meetingAudioWriter != nil {
		am.meetingAudioWriter.Abort()
		am.meetingAudioWriter = nil
	}
	if am.meetingCandidateAudioWriter != nil {
		am.meetingCandidateAudioWriter.Abort()
		am.meetingCandidateAudioWriter = nil
	}
	am.mu.Unlock()
	a.keepCallRecordingAtPause(pausedCall, pausedEpoch)
	if discardPending {
		a.discardPendingActivityRecording(am)
	}

	if triager != nil {
		triager.Stop()
	}
	if am.ocrWorker != nil {
		am.ocrWorker.Stop()
	}
	am.piiDetectorMu.Lock()
	if am.piiDetector != nil {
		am.piiDetector.Destroy()
		am.piiDetector = nil
	}
	am.lastPIIResult.Store(nil)
	am.piiDetectorMu.Unlock()
	// Do NOT close the store here — it must remain open so historical
	// sessions stay queryable via GetActivitySessions even while the
	// monitor is stopped. The store is closed in app shutdown instead.

	updateMenuBarStatus(3) // stopped
	log.Printf("[activity] monitoring stopped")
}

func (a *App) discardPendingActivityRecording(am *activityMonitorState) {
	if am == nil {
		return
	}
	if am.buf != nil {
		am.buf.Clear()
	}

	var snapshotPaths []string
	am.mu.Lock()
	if len(am.allSnapshots) > 0 && am.store != nil {
		snapshotPaths = make([]string, 0, len(am.allSnapshots))
		for _, snap := range am.allSnapshots {
			if snap.relPath != "" {
				snapshotPaths = append(snapshotPaths, filepath.Join(am.store.SnapshotsDir(), snap.relPath))
			}
		}
	}
	am.allSnapshots = nil
	for i := range am.pendingPIISnapshots {
		am.pendingPIISnapshots[i].jpegData = nil
	}
	am.pendingPIISnapshots = nil
	am.pendingPIIBytes = 0
	am.pendingPIIDraining = false
	am.pendingPIIExtractTrigger = ""
	am.lastParsedHashByBundle = nil
	am.lastParsedWriteByBundle = nil
	am.firstSpeechTime = 0
	am.lastSpeechMs = 0
	am.lastMeetingAudioMs = 0
	am.meetingBrowserMicReleased = false
	am.meetingBrowserGapSinceMs = 0
	am.meetingBrowserObservedMs = 0
	am.meetingCandidateActive = false
	am.meetingCandidateAppName = ""
	am.meetingCandidateStartMs = 0
	am.meetingCandidateRecordingEpoch = 0
	am.lastMeetingCandidateAudioMs = 0
	if am.meetingAudioWriter != nil {
		am.meetingAudioWriter.Abort()
		am.meetingAudioWriter = nil
	}
	if am.meetingCandidateAudioWriter != nil {
		am.meetingCandidateAudioWriter.Abort()
		am.meetingCandidateAudioWriter = nil
	}
	am.mu.Unlock()

	am.comparer.reset()
	for _, path := range snapshotPaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[activity] failed to discard pending closed-eyes snapshot %s: %v", path, err)
		}
	}
}

// handleActivityEvent processes a single activity event for snapshot decisions.
func (a *App) handleActivityEvent(evt activitymon.ActivityEvent) {
	if a.IsClosedEyesModeEnabled() {
		return
	}
	am := a.activity
	am.mu.Lock()
	defer am.mu.Unlock()

	now := evt.Timestamp

	// Always track the frontmost app for PII masking (sensitive app detection).
	if evt.AppBundleID != "" {
		am.lastAppBundleID = evt.AppBundleID
		a.captureSpans.noteActivityFront(evt.AppBundleID, evt.PrivateWindow || privateWindowEventLocked(am, evt), now)
	}
	am.lastUserActivityTime = now

	switch evt.Kind {
	case activitymon.EventAppSwitch:
		// Reset visual comparer so first frame of new app is never deduped
		am.comparer.reset()
		am.recordStateEventChurnLocked(now)
	case activitymon.EventFocus:
		// Reset visual comparer on focus change so new context is captured
		am.comparer.reset()
		am.recordStateEventChurnLocked(now)
	}

	// Track window title for session metadata.
	trackWindowTitleLocked(am, evt)

	// Event-driven meeting detection: when the user clicks on, switches to,
	// or focuses a meeting app with an active-meeting title while that app
	// holds the microphone, confirm the meeting instantly — no 15s poll or
	// 30s debounce needed. Keyboard events are excluded (too noisy from
	// meeting chat typing). Speech events are handled separately by
	// injectSpeechEvent.
	// A call in a private window never starts a recording from its title.
	if a.meetingRecordingOn() && !privateWindowEventLocked(am, evt) {
		switch evt.Kind {
		case activitymon.EventClick, activitymon.EventAppSwitch, activitymon.EventFocus:
			// Browsers are left to the watcher, which reads the tab's
			// address: a page title proves nothing.
			if result := meetingdetect.IsMeetingEvent(evt.AppBundleID, evt.WindowTitle); result.IsMeeting && !meetingdetect.IsBrowserBundle(evt.AppBundleID) &&
				!userListedAppMatches(evt.AppBundleID) && meetingProbes.micHolders().couldHold(evt.AppBundleID) {
				am.lastMeetingEventMs = evt.Timestamp
				if !am.inMeeting {
					// Instant meeting start. Release lock because
					// onMeetingStarted calls extractSession which takes am.mu.
					am.mu.Unlock()
					a.onMeetingStarted(am, result)
					am.mu.Lock()
				}
			}
		}
	}

	// Reset idle timer on any user activity.
	a.armActivityIdleTimerLocked(am, "idle_transition")

	// Reset heartbeat timer: capture every heartbeatInterval during activity
	if am.heartbeatTimer != nil {
		am.heartbeatTimer.Stop()
	}
	am.heartbeatTimer = time.AfterFunc(heartbeatInterval, func() {
		a.captureSnapshotForEvent("heartbeat")
	})

	// Restart visual change detection if it was stopped during idle.
	if am.visualStopCh == nil {
		am.visualStopCh = make(chan struct{})
		go a.runVisualChangeDetection()
	}

	am.lastEventTime = now

	// Typed-text segment transitions (IME-aware committed text). Keyboard
	// events advance/open the segment; clicks close it against the pre-click
	// snapshot (IME bursts re-read post-click instead, since a candidate
	// click commits the composition only after delivery); app switches and
	// focus changes drop it — the segment's field is gone.
	switch evt.Kind {
	case activitymon.EventKeyboard:
		if privateWindowEventLocked(am, evt) {
			// Nothing typed in a private window is recovered.
			a.closeTypedSegmentLocked(am, "", false)
			break
		}
		a.trackTypedSegmentKeyLocked(am, evt)
	case activitymon.EventClick:
		// The click carries its own pre-click field snapshot (captured on the
		// event-tap thread); a send-button click still holds the message text.
		a.closeTypedSegmentLocked(am, evt.FieldText, evt.FieldTextValid)
	case activitymon.EventAppSwitch, activitymon.EventFocus:
		a.closeTypedSegmentLocked(am, "", false)
	}

	// Decide whether to capture a snapshot based on event type
	switch evt.Kind {
	case activitymon.EventClick, activitymon.EventAppSwitch:
		if am.typingTimer != nil {
			am.typingTimer.Stop()
			am.typingTimer = nil
		}
		if isGoogleCalendarSaveClick(evt) {
			a.captureSnapshotForcedLocked("calendar_save")
			time.AfterFunc(calendarSaveFollowupDelay, func() {
				a.captureSnapshotForEvent("calendar_save_followup")
			})
			return
		}
		// Slide navigator click (Keynote / PowerPoint thumbnail).
		// EventClick would otherwise route through the debounced
		// path which honors snapshotDebounceMs (10s) when the
		// frontmost app hasn't changed — so paging through a deck by
		// clicking thumbnails would silently merge into one capture.
		if isSlideNavigatorClick(evt, am.lastAppBundleID) {
			a.captureSnapshotForcedLocked("slide_change")
			return
		}
		a.captureSnapshotDebounced(string(evt.Kind))

	case activitymon.EventKeyboard:
		if am.typingTimer != nil {
			am.typingTimer.Stop()
			am.typingTimer = nil
		}
		// Slide-show navigation: a Page Up/Down/Home/End keystroke
		// against a frontmost Keynote/PowerPoint window jumps slides
		// instantly. visualCheckInterval (10s) and typingPauseMs (1s)
		// together would drop most of a manually-paged review. Force
		// the capture on the keystroke itself; piiSem caps the
		// pipeline at one in-flight capture so a held-down key can't
		// melt the encoder.
		if isSlideNavKeyEvent(evt, am.lastAppBundleID) {
			a.captureSnapshotForcedLocked("slide_change")
			return
		}
		trigger := "keyboard"
		delayMs := typingPauseMs
		if isGoogleCalendarWindowTitle(evt.WindowTitle) {
			trigger = "calendar_keyboard"
			delayMs = calendarTypingPauseMs
		}
		am.typingTimer = time.AfterFunc(time.Duration(delayMs)*time.Millisecond, func() {
			// Typing paused: resolve the typed-text segment first so the
			// committed text (IME-composed) lands on the burst's keyboard
			// event before any downstream consumer sees the snapshot. Pure Go
			// over already-captured snapshots — no off-thread AX read.
			am.mu.Lock()
			a.flushTypedSegmentLocked(am)
			am.mu.Unlock()
			a.captureSnapshotForEvent(trigger)
		})

	case activitymon.EventFocus:
		if am.typingTimer != nil {
			am.typingTimer.Stop()
			am.typingTimer = nil
		}
		a.captureSnapshotDebounced("focus")
	}
}

// captureSnapshotDebounced decides whether to capture a snapshot in response
// to a focus / click / app-switch event. Must be called with am.mu held.
//
// The decision is three-tier so the user's rapid-switch context isn't silently
// dropped by a single global window:
//
//  1. Hard floor (minCaptureIntervalMs): no capture within the minimum interval of the
//     previous one, full stop. Protects the JPEG+OCR+PII pipeline.
//  2. Same-app cool-down (snapshotDebounceMs): if the frontmost app hasn't
//     changed since the last capture, wait the full same-app window so refocus
//     oscillation doesn't produce near-identical snapshots.
//  3. Window-churn pacing: during synthetic or pathological app-switch/focus
//     bursts, temporarily raise the floor for state-triggered captures.
//
// A switch TO a different app needs only the hard floor — so "glanced at
// Notes while cmd-tabbing back to iTerm" now captures Notes instead of
// being eaten by the global debounce.
func (a *App) captureSnapshotDebounced(trigger string) {
	am := a.activity
	now := time.Now().UnixMilli()
	elapsed := now - am.lastCaptureTime
	minIntervalMs := int64(minCaptureIntervalMs)
	if isWindowStateTrigger(trigger) && am.stateEventChurnUntilMs > now {
		minIntervalMs = int64(windowChurnMinCaptureIntervalMs)
	}
	// Visual-churn pacing is NOT applied here: it lives in
	// captureSnapshotForEvent so it covers every capture entry point uniformly
	// (the visual_change loop and the keyboard timer call that function
	// directly, bypassing this debounce), and so window-state triggers can be
	// exempted there without losing the prompt app-switch capture this path
	// guarantees via the hard floor above.
	if elapsed < minIntervalMs {
		return
	}
	// Same-app cool-down. An empty lastCaptureBundleID (first capture after
	// startup or a miss) skips this branch and falls through to the capture.
	if am.lastCaptureBundleID != "" &&
		am.lastCaptureBundleID == am.lastAppBundleID &&
		elapsed < int64(snapshotDebounceMs) {
		return
	}

	// Reserve the capture slot before launching the expensive screenshot +
	// AX parse pipeline. The monitor drain goroutine must keep draining OS
	// events during bursty window churn; captureSem prevents overlap if a
	// previous capture is still running.
	am.lastCaptureTime = now
	go a.captureSnapshotForEvent(trigger)
}

func isWindowStateTrigger(trigger string) bool {
	switch trigger {
	case string(activitymon.EventAppSwitch), "focus":
		return true
	default:
		return false
	}
}

func isForcedActivityCaptureTrigger(trigger string) bool {
	switch trigger {
	case "calendar_keyboard", "calendar_save", "calendar_save_followup",
		"slide_change":
		// slide_change must bypass the visual-distance dedup floor:
		// two consecutive slides often share template/background pixels
		// and would land below dedupThreshold otherwise, defeating the
		// keystroke-driven capture entirely.
		return true
	default:
		return false
	}
}

// visualChurnEligible reports whether a capture trigger participates in
// visual-churn pacing. Window-state triggers (app_switch/focus) are paced by
// windowChurn and must capture promptly; forced/keepalive triggers must always
// land. Everything else — input debounce, the visual_change loop, the keyboard
// timer — is paced while the screen is in sustained motion.
func visualChurnEligible(trigger string) bool {
	return !isWindowStateTrigger(trigger) && !isForcedActivityCaptureTrigger(trigger) &&
		trigger != "heartbeat" && trigger != "session_end"
}

// visualChurnThrottles reports whether an eligible capture should be skipped:
// pacing is armed (churnUntilMs in the future) and a capture already ran within
// the floor. Pure, so the pacing state machine is unit-testable.
func visualChurnThrottles(churnUntilMs, lastChurnMs, nowMs int64) bool {
	return churnUntilMs > nowMs && nowMs-lastChurnMs < int64(visualChurnMinCaptureIntervalMs)
}

// visualChurnStreakBroken reports whether the running in-motion streak should
// restart before counting the current frame: the foreground app/window changed
// (the first frame after a switch reads visualDist≈1.0 from the frame
// comparer's reset, which is not real motion), or too long elapsed since the
// last in-motion frame for them to be "consecutive". Pure, for unit testing.
func visualChurnStreakBroken(trigger, lastBundle, bundle string, lastFrameMs, nowMs int64) bool {
	// A window-state trigger (app switch or focus change) is an explicit
	// user-initiated context shift — even between two windows of the SAME app
	// (two Chrome / two VS Code windows share a bundle ID) — so it always ends
	// the streak. Bundle-ID comparison alone misses the same-app case.
	if isWindowStateTrigger(trigger) {
		return true
	}
	if lastBundle != bundle {
		return true
	}
	return lastFrameMs != 0 && nowMs-lastFrameMs > int64(visualChurnStreakWindowMs)
}

// nextVisualChurnStreak advances the in-motion streak for one captured frame and
// returns the updated streak and pacing deadline. A high-distance frame grows
// the streak and, once it reaches the threshold, arms pacing for
// visualChurnCooldownMs. breakStreak (computed by visualChurnStreakBroken)
// restarts the count first, so neither a stale streak nor a lone app-switch
// frame can arm pacing on its own. A low-distance frame resets the streak.
func nextVisualChurnStreak(streak int, churnUntilMs int64, visualDist float64, nowMs int64, breakStreak bool) (int, int64) {
	if visualDist < visualChurnDistThreshold {
		return 0, churnUntilMs
	}
	if breakStreak {
		streak = 0
	}
	streak++
	if streak >= visualChurnStreakThreshold {
		churnUntilMs = nowMs + int64(visualChurnCooldownMs)
	}
	return streak, churnUntilMs
}

// googleCalendarLocalizedTitles enumerates the product name as Google
// localizes it across the languages we've encountered in dog-fooding.
// Used by isGoogleCalendarWindowTitle to gate the Calendar-specific
// forced-capture path — false negatives here mean Save clicks won't
// force a snapshot for users in that locale.
var googleCalendarLocalizedTitles = []string{
	"Google Calendar",   // en
	"Google 日历",         // zh-CN
	"Google 日曆",         // zh-TW / zh-HK
	"Google カレンダー",      // ja
	"Google 캘린더",        // ko
	"Google Agenda",     // fr / pt / nl
	"Google Kalender",   // de / da
	"Google Calendario", // es / it
	"Календарь Google",  // ru (product name trails)
}

func isGoogleCalendarWindowTitle(title string) bool {
	for _, t := range googleCalendarLocalizedTitles {
		if strings.Contains(title, t) {
			return true
		}
	}
	return false
}

func isGoogleCalendarSaveClick(evt activitymon.ActivityEvent) bool {
	if evt.Kind != activitymon.EventClick || !isGoogleCalendarWindowTitle(evt.WindowTitle) {
		return false
	}
	label := strings.TrimSpace(evt.ClickLabel)
	return label == "保存" || strings.EqualFold(label, "Save")
}

// slidePresentationBundleIDs lists the apps that benefit from the
// slide_change force-capture trigger. The set intentionally tracks the
// office_presentation parser's bundle id list so the trigger and the
// parser stay paired.
var slidePresentationBundleIDs = map[string]bool{
	"com.apple.iWork.Keynote":  true,
	"com.apple.Keynote":        true,
	"com.microsoft.Powerpoint": true,
}

// slideNavKeyCodes are the macOS CGKeyCodes for unambiguous page-
// navigation keys. These trigger slide_change regardless of modifier
// state — they have no edit-mode meaning to clash with.
var slideNavKeyCodes = map[uint16]bool{
	0x74: true, // Page Up
	0x79: true, // Page Down
	0x73: true, // Home
	0x77: true, // End
}

// slideArrowKeyCodes are the macOS CGKeyCodes for arrow keys. Arrow
// keys ARE used to navigate slides (e.g. ⌃⌥↑/↓ in Keynote, ←/→ in
// presentation mode), but a bare arrow press also moves the text
// cursor or nudges a shape — far too noisy to force-capture on. We
// therefore only fire when at least one of Ctrl / Option / Command /
// Shift is held: that combination has no benign edit-mode meaning,
// so a modifier-arrow press in a presentation app is almost certainly
// a slide-navigation hotkey.
var slideArrowKeyCodes = map[uint16]bool{
	0x7B: true, // Left Arrow
	0x7C: true, // Right Arrow
	0x7D: true, // Down Arrow
	0x7E: true, // Up Arrow
}

// macOS CGEventFlags / NSEventModifierFlags bits for the four modifier
// keys we treat as "this is a hotkey, not a literal keypress".
const (
	cgFlagMaskShift   uint64 = 1 << 17 // 0x20000
	cgFlagMaskControl uint64 = 1 << 18 // 0x40000
	cgFlagMaskOption  uint64 = 1 << 19 // 0x80000
	cgFlagMaskCommand uint64 = 1 << 20 // 0x100000
)

// hasSlideNavModifier reports whether any of Ctrl / Option / Command /
// Shift is set in a macOS event modifier bitmask. Used to disambiguate
// arrow-key edit movement (no modifier) from arrow-key slide hotkeys
// (modifier present).
func hasSlideNavModifier(modifiers uint64) bool {
	return modifiers&(cgFlagMaskShift|cgFlagMaskControl|cgFlagMaskOption|cgFlagMaskCommand) != 0
}

// isSlideNavKeyEvent reports whether a keyboard event is a slide-show
// navigation keystroke against a known presentation app. Frontmost
// bundle id is read from the event first (precise at the keystroke
// instant); when absent, fall back to the activity monitor's tracked
// last-app value.
func isSlideNavKeyEvent(evt activitymon.ActivityEvent, fallbackBundleID string) bool {
	if evt.Kind != activitymon.EventKeyboard {
		return false
	}
	bid := evt.AppBundleID
	if bid == "" {
		bid = fallbackBundleID
	}
	if !slidePresentationBundleIDs[bid] {
		return false
	}
	if slideNavKeyCodes[evt.KeyCode] {
		return true
	}
	if slideArrowKeyCodes[evt.KeyCode] && hasSlideNavModifier(evt.Modifiers) {
		return true
	}
	return false
}

// isSlideNavigatorClick reports whether a click event is targeting the
// slide navigator (the left-rail thumbnails) of a presentation app.
// Keynote and PowerPoint both render navigator entries as AXRow nodes
// — clicking a thumbnail is the most common way users page through
// slides, but it fires as EventClick and would otherwise be subject
// to the 10s snapshotDebounceMs same-app cool-down.
//
// AXRow is a broad role, but in Keynote/PowerPoint windows the
// dominant AXRow clicks are navigator selections; any false positive
// (e.g. a click in the Inspector's checklist) is rate-limited by
// piiSem cap=1 and harmless.
func isSlideNavigatorClick(evt activitymon.ActivityEvent, fallbackBundleID string) bool {
	if evt.Kind != activitymon.EventClick {
		return false
	}
	if evt.ClickRole != "AXRow" {
		return false
	}
	bid := evt.AppBundleID
	if bid == "" {
		bid = fallbackBundleID
	}
	return slidePresentationBundleIDs[bid]
}

func (a *App) captureSnapshotForcedLocked(trigger string) {
	am := a.activity
	if am == nil {
		return
	}
	am.lastCaptureTime = time.Now().UnixMilli()
	go a.captureSnapshotForEvent(trigger)
}

func (am *activityMonitorState) recordStateEventChurnLocked(nowMs int64) {
	if nowMs <= 0 {
		nowMs = time.Now().UnixMilli()
	}
	if am.stateEventWindowMs == 0 || nowMs-am.stateEventWindowMs > int64(windowChurnWindowMs) {
		am.stateEventWindowMs = nowMs
		am.stateEventCount = 1
		return
	}
	am.stateEventCount++
	if am.stateEventCount >= windowChurnThreshold {
		am.stateEventChurnUntilMs = nowMs + int64(windowChurnCooldownMs)
	}
}

func (am *activityMonitorState) hasRecentUserActivityForVisualChangeLocked(nowMs int64) bool {
	if am.lastUserActivityTime == 0 {
		return false
	}
	return nowMs-am.lastUserActivityTime <= int64(visualActivityWindow/time.Millisecond)
}

// parserMessageCount returns the number of structured ConversationMessage
// items a parser produced, or 0 when the parser didn't run / no messages.
// Used by the parser:dispatch trace.
func parserMessageCount(p *appparser.ParsedContent) int {
	if p == nil {
		return 0
	}
	return len(p.Messages)
}

// parserTitle / parserChannel return the parser-provided title and channel
// for the parser:dispatch trace, or empty strings on miss.
func parserTitle(p *appparser.ParsedContent) string {
	if p == nil {
		return ""
	}
	return p.Title
}

func parserChannel(p *appparser.ParsedContent) string {
	if p == nil {
		return ""
	}
	return p.Channel
}

// contentHashForDedup returns a stable hash that identifies the textual
// content of a parsed snapshot. Returns "" when the snapshot carries
// nothing dedup-worthy (no messages and no body) — callers treat "" as
// "always write the full payload".
//
// FNV-64 is fine here: this is a dedup KEY, not a security primitive.
// Two snapshots that hash identically are treated as the same content;
// the rare false-collision worst case is a single duplicate prompt
// entry that the downstream LLM tolerates.
//
// The hash is built from the addressing fields
// (contentType + title + url + channel) plus an ordered fingerprint
// of each message (sender + timestamp + length + full content), so:
//   - hover/cursor/state visual changes that don't touch the message
//     list collapse cleanly to the same hash;
//   - any new/edited/reordered message produces a different hash;
//   - streaming LLM responses where only the trailing bytes grow
//     (ChatGPT / Claude live token rendering) keep producing distinct
//     hashes — content length is mixed in and the full body is hashed,
//     so a "prefix-stable, tail-growing" frame is never treated as a
//     duplicate of a shorter one;
//   - empty / non-conversation parsed payloads (e.g. browser-stripped
//     web pages) hash on Body when no messages are present, so the
//     same URL refreshed twice still dedups.
func contentHashForDedup(p *appparser.ParsedContent) string {
	if p == nil {
		return ""
	}
	h := fnv.New64a()
	var lenBuf [8]byte
	writeLen := func(n int) {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(n))
		h.Write(lenBuf[:])
	}
	// Mix in the addressing fields (contentType / title / URL / channel)
	// before the body, so two payloads with the same body text but
	// different page identity (e.g. two docs that happen to share the
	// same first paragraph) never collide. Cheap — ~tens of bytes.
	h.Write([]byte(p.ContentType))
	h.Write([]byte{0})
	h.Write([]byte(p.Title))
	h.Write([]byte{0})
	h.Write([]byte(p.URL))
	h.Write([]byte{0})
	h.Write([]byte(p.Channel))
	h.Write([]byte{0})
	if len(p.Messages) > 0 {
		writeLen(len(p.Messages))
		for _, m := range p.Messages {
			h.Write([]byte(m.Sender))
			h.Write([]byte{0})
			h.Write([]byte(m.Timestamp))
			h.Write([]byte{0})
			writeLen(len(m.Content))
			h.Write([]byte(m.Content))
			h.Write([]byte{0})
			h.Write([]byte(m.ThreadParent))
			h.Write([]byte{0x1f})
		}
	} else if p.Body != "" {
		writeLen(len(p.Body))
		h.Write([]byte(p.Body))
	} else {
		// Nothing distinctive to hash on — caller will treat "" as
		// "skip dedup, write full payload (which is already small)".
		return ""
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

func (am *activityMonitorState) tryBeginCapture() bool {
	if am.captureSem == nil {
		return true
	}

	select {
	case am.captureSem <- struct{}{}:
		return true
	default:
		return false
	}
}

// beginCaptureWait blocks up to timeout for the in-flight capture to finish
// before claiming the slot. Used by the session_end final capture, which is
// the session's last chance at a snapshot — dropping it on a transient
// collision (like every other trigger does) would persist the session with
// zero frames.
func (am *activityMonitorState) beginCaptureWait(timeout time.Duration) bool {
	if am.captureSem == nil {
		return true
	}

	select {
	case am.captureSem <- struct{}{}:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (am *activityMonitorState) endCapture() {
	if am.captureSem == nil {
		return
	}
	<-am.captureSem
}

// captureSnapshotForEvent takes a screenshot, walks the accessibility tree for
// content dedup, encodes JPEG, and stores it. Matches screenpipe's do_capture flow:
// screenshot → AX tree walk → content hash dedup → JPEG write.
func (a *App) captureSnapshotForEvent(trigger string) {
	a.captureSnapshotForEventWithWait(trigger, sessionEndCaptureWait)
}

// captureSnapshotForEventWithWait is captureSnapshotForEvent with the time a
// session_end frame may wait for a busy capture or PII slot. Shutdown flushes
// pass 0 so the quit finishes inside the shutdown watchdog.
func (a *App) captureSnapshotForEventWithWait(trigger string, sessionEndWait time.Duration) {
	if a.IsClosedEyesModeEnabled() {
		return
	}
	activityPipelineMetrics.captureAttempts.Add(1)
	captureStart := time.Now()
	defer func() {
		activityPipelineMetrics.captureDurations.add(time.Since(captureStart))
	}()

	am := a.activity
	if am == nil {
		return
	}

	if !am.tryBeginCapture() {
		if trigger != "session_end" {
			activityPipelineMetrics.captureSkippedBackpressure.Add(1)
			return
		}
		// session_end is the one-shot final capture — wait out the in-flight
		// capture instead of dropping the session's only guaranteed frame.
		if !am.beginCaptureWait(sessionEndWait) {
			activityPipelineMetrics.captureSkippedBackpressure.Add(1)
			log.Printf("[activity] session_end capture gave up waiting for in-flight capture (%v)", sessionEndWait)
			return
		}
	}
	defer am.endCapture()

	// 0. Lock screen detection: skip capture when screen is locked.
	if activitymon.IsScreenLocked() {
		return
	}

	am.mu.Lock()
	lastWindowTitle := am.lastWindowTitle
	private := privateFrontLocked(am)
	am.mu.Unlock()

	// 0a. Skip capture when browser is in private/incognito mode.
	if private {
		return
	}
	// 0a'. Skip capture while Isoform itself is driving the screen (web
	// calendar peek): that page is not the user's activity.
	if activityCaptureSuppressed.Load() {
		return
	}

	// 0b. Capture failure backoff: skip if we're in a backoff period.
	// session_end is the one-shot final capture for a session — bypass the
	// backoff so we always give the last frame a chance. The PrintWindow
	// fallback in capture_windows.go and the retry below cover the case
	// where the underlying GDI path is intermittently failing.
	if trigger != "session_end" {
		am.mu.Lock()
		if !am.captureBackoff.IsZero() && time.Now().Before(am.captureBackoff) {
			am.mu.Unlock()
			return
		}
		am.mu.Unlock()
	}

	// 0c. Pipeline back-pressure — skip captures we know runAsyncPIIMasking
	// would drop. piiSem is size 1; when it's full, the previous PII pass
	// is still running, and firing another capture only to have the frame
	// dropped at the goroutine-spawn site wastes CaptureRaw (~14 MB alloc),
	// JPEG encode, accessibility-tree walk, and a sensitive-window sweep
	// — the things that were showing up in vmmap cgo retention on
	// overnight profiles. Heartbeat + session_end bypass this because the
	// activity store needs a keepalive write every maxSkipDuration.
	if trigger != "heartbeat" && trigger != "session_end" {
		if len(am.piiSem) >= cap(am.piiSem) {
			activityPipelineMetrics.captureSkippedBackpressure.Add(1)
			return
		}
	}

	// 0d. Visual-churn pacing — unified across every capture entry point
	// (debounced input, the visual_change loop, and the keyboard timer all
	// reach here). While screen content is in constant motion (video /
	// continuous scroll) run the expensive CaptureRaw + JPEG + OCR pipeline at
	// most once per visualChurnMinCaptureIntervalMs. Window-state triggers
	// (app_switch/focus) are exempt so switching apps still captures promptly —
	// they are paced separately by windowChurn — and forced/keepalive triggers
	// (slide_change/calendar/heartbeat/session_end) are exempt so slides and
	// session boundaries always land. Only the throttle decision is made here;
	// the floor clock is advanced after the dedup gate (below) so frames that
	// are skipped before the pipeline actually runs — own-window, capture
	// failure, or a visually-identical dedup hit — do not consume the interval
	// and falsely throttle a later real capture.
	churnNowMs := time.Now().UnixMilli()
	am.mu.Lock()
	throttled := visualChurnEligible(trigger) &&
		visualChurnThrottles(am.visualChurnUntilMs, am.lastChurnCaptureMs, churnNowMs)
	am.mu.Unlock()
	if throttled {
		return
	}

	// 1. Capture raw RGBA pixels at full resolution.
	// The CG capture call can take 10-100ms during which the user may
	// switch focus OR drag/resize a window — and the latter is invisible to
	// bundle-ID-only checks (two Chrome windows share the same bundle, two
	// Slack workspaces share the same bundle, etc). To avoid pinning a rect
	// to the wrong window, sample bundle + bounds both before and after the
	// capture and require both to match. On any disagreement zero the rect
	// so the OCR worker falls back to full-frame recognition rather than
	// cropping confidently against stale geometry — losing the focused-
	// window scope on those frames is preferable to misleading audit UI.
	preBundle := activitymon.FrontmostAppBundleID()
	preBounds := activitymon.FrontmostWindowBounds()

	// Skip the capture entirely when wails-gui itself is the focused
	// window. WebView2 (the framework Yansu's UI runs on) renders via
	// DComp / Direct3D which the GDI capture path can't read — every
	// attempt fails through BitBlt, CAPTUREBLT, and PrintWindow, then
	// trips the captureBackoff state and pollutes subsequent captures
	// for other (perfectly capturable) apps. Silently skipping is the
	// right behaviour: the user is just looking at past activity in
	// our own UI, not producing new content worth a snapshot. The
	// activity event still gets recorded via the input-hook path, so
	// nothing is lost beyond the (irrelevant) screenshot.
	if isOwnProcessFocused(preBundle) {
		activityPipelineMetrics.captureSkippedSelfFocus.Add(1)
		return
	}
	// The cached title above can predate a private window taking focus
	// without an event; a browser in front is checked live.
	if liveBrowserWindowPrivate(preBundle) {
		return
	}

	rgba, w, h, err := am.capture.CaptureRaw()
	// session_end is the only chance to land a snapshot for this session,
	// and DWM/GDI failures often clear within a few hundred ms. Pay the
	// extra sleep+attempt to recover the final frame; non-final triggers
	// have heartbeat/visual-change retries already.
	if err != nil && trigger == "session_end" {
		log.Printf("[activity] session_end capture failed, retrying after 200ms: %v", err)
		time.Sleep(200 * time.Millisecond)
		preBundle = activitymon.FrontmostAppBundleID()
		preBounds = activitymon.FrontmostWindowBounds()
		rgba, w, h, err = am.capture.CaptureRaw()
		if err == nil {
			log.Printf("[activity] session_end capture recovered on retry")
		}
	}
	// The pid tells two apps without a bundle id apart (Termius, CLI tools).
	capturePID := activitymon.FrontmostAppPID()
	captureBundleID := activitymon.FrontmostAppBundleID()
	captureAppName := activitymon.FrontmostAppName()
	postBounds := activitymon.FrontmostWindowBounds()
	captureFocusBounds := postBounds
	const boundsMatchEpsilon = 0.001 // ~0.1% of screen — tolerates float noise but rejects real window moves
	boundsClose := func(a, b activitymon.WindowBounds) bool {
		abs := func(f float64) float64 {
			if f < 0 {
				return -f
			}
			return f
		}
		return abs(a.X-b.X) < boundsMatchEpsilon &&
			abs(a.Y-b.Y) < boundsMatchEpsilon &&
			abs(a.W-b.W) < boundsMatchEpsilon &&
			abs(a.H-b.H) < boundsMatchEpsilon
	}
	if preBundle == "" || preBundle != captureBundleID || !boundsClose(preBounds, postBounds) {
		captureFocusBounds = activitymon.WindowBounds{}
	}
	// app_switch / freshly-focused windows can report a sliver bounds (e.g. only
	// the title bar, h≈1.7%) before layout settles — that defeats the focus-crop,
	// so the thumbnail falls back to full-screen. Retry once after a short settle,
	// keeping the result only if the window stabilized and focus stayed on the
	// same app.
	isSliver := func(b activitymon.WindowBounds) bool {
		const minFraction = 0.05 // a laid-out window covers >5% per axis; below = sliver
		return (b.W > 0 && b.W < minFraction) || (b.H > 0 && b.H < minFraction)
	}
	if isSliver(captureFocusBounds) {
		time.Sleep(120 * time.Millisecond)
		if retry := activitymon.FrontmostWindowBounds(); !isSliver(retry) && activitymon.FrontmostAppName() == captureAppName {
			captureFocusBounds = retry
		}
	}
	if err != nil {
		am.mu.Lock()
		am.captureFailCount++
		backoff := min(captureBackoffBase*time.Duration(1<<min(am.captureFailCount-1, 6)), captureBackoffMax)
		am.captureBackoff = time.Now().Add(backoff)
		count := am.captureFailCount
		if count == 1 {
			am.captureBurstStartedAt = time.Now()
		}
		am.mu.Unlock()
		if count == 1 {
			log.Printf("[activity] snapshot capture burst started (backoff=%v): %v", backoff, err)
		} else if count%50 == 0 {
			log.Printf("[activity] snapshot capture still failing (count=%d, backoff=%v): %v", count, backoff, err)
		}
		return
	}

	// Capture succeeded — reset backoff state.
	am.mu.Lock()
	if am.captureFailCount > 0 {
		burstDuration := time.Duration(0)
		if !am.captureBurstStartedAt.IsZero() {
			burstDuration = time.Since(am.captureBurstStartedAt)
		}
		log.Printf("[activity] snapshot capture burst ended after %d failures (lasted %v)", am.captureFailCount, burstDuration.Round(time.Millisecond))
		am.captureBurstStartedAt = time.Time{}
		am.captureFailCount = 0
		am.captureBackoff = time.Time{}
	}
	am.mu.Unlock()
	if len(rgba) == 0 {
		return
	}
	defer activitymon.ReleaseCaptureBuffer(rgba)

	now := time.Now()

	// 2. Visual dedup: compare against previous frame using pixel histograms.
	// This is the primary dedup gate — it catches both identical frames AND
	// cases where the AX tree text is too coarse (Windows only returns window
	// title, so text hash never changes within the same app).
	visualDist := am.comparer.distance(rgba, w, h)

	// 3. Determine whether to skip this frame.
	// Never skip heartbeat/session_end triggers — they must always write.
	// Force a write every maxSkipDuration even if frame looks identical.
	am.mu.Lock()
	timeSinceLastSave := now.UnixMilli() - am.lastSavedTime
	// Visual-churn detection: a streak of high-distance frames means the
	// foreground is in constant motion (video/scroll). Once the streak crosses
	// the threshold we raise the capture floor (enforced at the top of this
	// function) so we stop snapshotting + OCR'ing every frame. The streak is
	// bounded in time and to one app/window — a stale count or the visualDist≈1.0
	// frame produced by switching apps cannot arm pacing on its own. A single
	// low-distance frame resets the streak but leaves the cooldown to expire
	// naturally, so a brief still moment inside a video does not flap pacing.
	churnFrameMs := now.UnixMilli()
	breakChurnStreak := visualChurnStreakBroken(trigger, am.lastChurnBundle, captureBundleID, am.lastChurnFrameMs, churnFrameMs)
	prevChurnStreak := am.visualChurnStreak
	am.visualChurnStreak, am.visualChurnUntilMs = nextVisualChurnStreak(
		prevChurnStreak, am.visualChurnUntilMs, visualDist, churnFrameMs, breakChurnStreak)
	if visualDist >= visualChurnDistThreshold {
		am.lastChurnFrameMs = churnFrameMs
		am.lastChurnBundle = captureBundleID
	}
	if prevChurnStreak < visualChurnStreakThreshold && am.visualChurnStreak >= visualChurnStreakThreshold {
		log.Printf("[activity] visual-churn pacing engaged (dist=%.2f, floor=%dms)", visualDist, visualChurnMinCaptureIntervalMs)
	}
	am.mu.Unlock()

	forceCapture := timeSinceLastSave >= int64(maxSkipDuration/time.Millisecond)
	alwaysWrite := trigger == "heartbeat" || trigger == "session_end" || isForcedActivityCaptureTrigger(trigger)

	if !forceCapture && !alwaysWrite && visualDist < dedupThreshold {
		activityPipelineMetrics.captureSkippedDedup.Add(1)
		return
	}

	// The frame is now committed to the expensive pipeline (per-window
	// ScreenCaptureKit capture, JPEG encode, OCR). Advance the visual-churn floor
	// clock here rather than at the throttle check above, so only real pipeline
	// runs consume the interval. Exempt triggers (app_switch/focus/forced/
	// keepalive) reach here too, so they keep pacing subsequent eligible captures.
	am.mu.Lock()
	am.lastChurnCaptureMs = now.UnixMilli()
	am.mu.Unlock()

	// 4. Snapshot image: prefer a per-window ScreenCaptureKit capture (focus
	// window only — no full-screen multi-app noise, no focus-rect cropping needed).
	// Fall back to the full-screen encode when per-window capture is unavailable
	// (windowID=0, capture failure, or our own Wails window already skipped above).
	// Resolve the focused window for per-window capture. windowID == 0 means the
	// frontmost app has no normal window — a system process (Universal Control,
	// the desktop), not real per-app activity. Skip rather than emit a full-screen
	// multi-app frame mislabeled as that system process.
	frontWindow := activitymon.FrontmostWindowInfo()
	wid, windowOwner := frontWindow.ID, frontWindow.OwnerName
	// On platforms with per-window capture (macOS), windowID == 0 means the
	// frontmost app has no normal window — skip rather than emit a full-screen
	// multi-app frame mislabeled as that system process. On platforms WITHOUT
	// per-window capture (Windows, Linux) wid is always 0; there we must NOT skip
	// — fall through to the full-screen encode below so existing capture keeps working.
	if activitymon.SupportsWindowCapture() && wid == 0 {
		return
	}
	// The sensitivity checks below use captureBundleID. If focus moved to
	// another app (say a password manager) after it was sampled, the window
	// captured here is that app's, and it would skip the blur meant for it.
	if !captureFocusStable(
		frontmostSample{bundleID: captureBundleID, pid: capturePID},
		frontmostSample{bundleID: frontWindow.BundleID, pid: frontWindow.PID},
		activitymon.SupportsWindowCapture(),
	) {
		activityPipelineMetrics.captureSkippedFocusChanged.Add(1)
		return
	}
	// Prefer the captured window's owner name (real app name from the window list)
	// whenever present. It's reliable — wid is the window we screenshot — and beats
	// both an empty frontmost bundle/name (Termius/AirJelly report none) AND a
	// bundle id whose last segment is a generic word (com.termius-dmg.mac → "mac").
	if windowOwner != "" {
		captureAppName = windowOwner
	}
	perWindow := false
	var jpegData []byte
	if b, winW, winH := activitymon.CaptureWindowJPEG(wid, snapshotQuality); len(b) > 0 {
		jpegData = b
		// Use the captured window's dimensions, not the full-screen w/h from
		// CaptureRaw — otherwise the snapshot's stored width/height mismatch the
		// per-window image (wrong aspect ratio in the UI, broken OCR coordinates).
		w = winW
		h = winH
		perWindow = true
	}
	if !perWindow {
		// Per-window capture failed (window vanished mid-capture, permission) —
		// fall back to a full-screen encode so we don't drop the frame entirely.
		encoded, encErr := am.capture.EncodeJPEG(rgba, w, h, snapshotQuality)
		if encErr != nil {
			log.Printf("[activity] JPEG encode failed: %v", encErr)
			return
		}
		jpegData = encoded
	} else {
		// The per-window image IS the focused window, so the focus rect spans the
		// whole frame — downstream focus-cropping becomes a no-op.
		captureFocusBounds = activitymon.WindowBounds{X: 0, Y: 0, W: 1, H: 1}
	}

	// 4b. Collect the structured AX tree AND flat text. The structured tree
	// enables app-specific parsers (Slack/ChatGPT/iMessage) that use role,
	// frame, and DOM class info to produce high-quality output; the flat
	// text is retained as a fallback for apps without a structured parser
	// (and as the storage format when no parser matches).
	windowTitle := lastWindowTitle
	// For heavy CEF apps we neither prime nor parse (DingTalk, …), walking the
	// full AX tree on every committed frame is expensive AND low-value: the
	// tree is just native chrome (unprimed), while each kAXChildren query forces
	// the app to build/maintain a live accessibility tree on its UI thread,
	// which makes typing sluggish. Throttle the walk hard for these apps and let
	// the screenshot/OCR path carry the frame; other apps are unaffected.
	var axTree *activitymon.AXNode
	var axText string
	if !isHeavyAXWalkApp(captureBundleID) || am.reserveAXWalk(captureBundleID, now) {
		axTree = activitymon.CollectFocusedWindowTree()
		axText = activitymon.CollectFocusedWindowText()
		// The walk reads whichever app is frontmost now. If focus moved
		// since captureBundleID was sampled, the text belongs to another app
		// and would get that app's (possibly weaker) protection.
		laterPID := activitymon.FrontmostAppPID()
		if !captureFocusStable(
			frontmostSample{bundleID: captureBundleID, pid: capturePID},
			frontmostSample{bundleID: activitymon.FrontmostAppBundleID(), pid: laterPID},
			true,
		) {
			activityPipelineMetrics.captureSkippedFocusChanged.Add(1)
			return
		}
	}

	// Skip our own Activity replay page (the Events/Timeline/Summary view). It
	// renders past snapshots, so capturing it is "a screenshot of screenshots":
	// the frame is correctly labeled Yansu (it IS the frontmost window) but the
	// visible content is whatever app is being replayed, which reads as a mislabel.
	// Only this page is skipped — Yansu's chat/task pages are real activity and are
	// still captured (SCK reads our WebView fine on macOS).
	if isOwnActivitySurface(axText) {
		activityPipelineMetrics.captureSkippedSelfFocus.Add(1)
		return
	}

	// Fetch the active browser URL before dispatch so URL-aware parsers
	// (Notion/Linear/etc at their web domains) can pre-empt the generic
	// browser chrome stripper when the frontmost app is a browser. Skipped
	// for protected apps (relevant when a user adds a browser to the
	// protected list — the active tab URL is content).
	browserURL := ""
	if _, isSensitive := pii.IsSensitiveApp(captureBundleID); !isSensitive {
		browserURL = activitymon.GetBrowserURL(captureBundleID)
	}

	// Run parsers. ParseTree prefers structured parsers (TreeParser) when
	// the tree is available, then falls back to flat-text parsers
	// (TextParser, e.g. Terminal/Browser chrome-stripper).
	//
	// We also capture the structured fields (content_type, app, full
	// ParsedContent JSON) so the snapshot row carries enough metadata for
	// per-app filtering and lossless replay without re-running the parser.
	parseStart := time.Now()
	parsed := appparser.ParseTree(axTree, axText, captureBundleID, windowTitle, browserURL)
	// Fallback: when no parser matched but we know the frontmost app, attach a
	// minimal parsed record carrying the bundleId (and the raw ax body), so every
	// snapshot keeps an app identity. Without this, parser-less apps (Clash, many
	// native tools) persist as app="" and the event extractor — seeing no app
	// boundary — merges unrelated windows into one event.
	if parsed == nil && (strings.TrimSpace(captureBundleID) != "" || strings.TrimSpace(captureAppName) != "") {
		meta := map[string]string{}
		if strings.TrimSpace(captureBundleID) != "" {
			meta["bundleId"] = captureBundleID
		}
		// localizedName fallback: bundle-less apps (Clash Verge, CLI tools) have a
		// nil bundleId but a usable name, so resolveRealApp uses the name instead
		// of collapsing them to "?".
		if strings.TrimSpace(captureAppName) != "" {
			meta["app"] = captureAppName
		}
		parsed = &appparser.ParsedContent{Body: axText, Metadata: meta}
	}
	parseElapsedMs := time.Since(parseStart).Milliseconds()
	var parsedContentType, parsedApp, parsedJSON, parsedHash string
	parsedDeduped := false
	if parsed != nil {
		redactParsedContentPII(parsed)
		axText = parsed.Body
		parsedContentType = parsed.ContentType
		if parsed.Metadata != nil {
			parsedApp = parsed.Metadata["app"]
		}
		// A matched parser can still leave app empty: terminal/text parsers
		// (Termius, cmux, ...) extract body text but carry no app identity. The
		// owner-name fallback above only ran when NO parser matched, so for these
		// the resolved window owner was discarded and the snapshot persisted as
		// app="" — losing the app boundary the event extractor needs. Fill it here.
		if strings.TrimSpace(parsedApp) == "" && strings.TrimSpace(captureAppName) != "" {
			parsedApp = captureAppName
			if parsed.Metadata == nil {
				parsed.Metadata = map[string]string{}
			}
			parsed.Metadata["app"] = captureAppName
		}

		// Dedup: when consecutive snapshots from the same app produce
		// the same parsed message list (extremely common — visual dedup
		// passes hover/cursor changes through, but the textual content
		// is unchanged), elide the redundant payload to a tiny stub.
		// Saves storage AND lets every downstream LLM consumer skip the
		// repeat in O(1) by checking for `dedupOfHash`. The non-stub
		// snapshot for this hash is still in the same session burst, so
		// `GetSessionSnapshots` resolves the body without an extra
		// query.
		//
		// IMPORTANT: only inspect prevHash here; do NOT advance
		// lastParsedHashByBundle until the snapshot has been appended
		// to allSnapshots below. Otherwise a non-dup full payload that
		// gets dropped by PII throttling/busy below would still record
		// its hash, and the next identical frame would be elided to a
		// stub with no resolvable sibling — losing structured content.
		// For the same reason the referent's masked file must have landed
		// (canElideParsedPayload): a frame the async mask drops before its
		// session is saved never becomes a row.
		parsedHash = contentHashForDedup(parsed)
		if parsedHash != "" {
			if am.canElideParsedPayload(captureBundleID, parsedHash) {
				stub := struct {
					DedupOfHash string `json:"dedupOfHash"`
					App         string `json:"app,omitempty"`
					ContentType string `json:"contentType,omitempty"`
				}{DedupOfHash: parsedHash, App: parsedApp, ContentType: parsedContentType}
				if data, err := json.Marshal(stub); err == nil {
					parsedJSON = string(data)
					parsedDeduped = true
				}
			}
		}

		if !parsedDeduped {
			// Embed the hash in the full payload so downstream
			// consumers (and dedup-stub resolvers) can match without
			// re-hashing.
			if parsedHash != "" {
				if parsed.Metadata == nil {
					parsed.Metadata = map[string]string{}
				}
				parsed.Metadata["contentHash"] = parsedHash
			}
			if data, err := json.Marshal(parsed); err == nil {
				parsedJSON = string(data)
			}
		}
	}

	// Emit a per-capture parser dispatch trace so a frontend debug
	// subscriber can render parser hits/misses in the Wails devtools
	// console. Cheap (one event per snapshot, ≤1/s) and emitted
	// unconditionally — the frontend gates console output on a
	// localStorage flag so it's silent for non-developers.
	a.emitBrowserEvent("parser:dispatch", map[string]any{
		"bundleId":    captureBundleID,
		"url":         browserURL,
		"windowTitle": windowTitle,
		"parserApp":   parsedApp,
		"contentType": parsedContentType,
		"bodyLen":     len(axText),
		"messages":    parserMessageCount(parsed),
		"title":       parserTitle(parsed),
		"channel":     parserChannel(parsed),
		"elapsedMs":   parseElapsedMs,
	})

	// Drop AX text AND the parsed payload entirely when the frontmost app
	// is protected. The screenshot is already blurred and typed-text is
	// already dropped (activity_typed_text.go); parsedJSON carries the
	// full parsed body so it must be cleared alongside axText, otherwise
	// protected-app content still reaches disk via the snapshot row.
	if _, isSensitive := pii.IsSensitiveApp(captureBundleID); isSensitive {
		axText = ""
		parsedJSON = ""
		parsedHash = ""
	}

	// Redact PII (API keys, tokens, passwords, credit cards, etc.) from AX
	// text before it hits disk. The GLiNER+regex image-level PII masking runs
	// in a separate async pass on the screenshot; this handles the text path.
	// Keep the distinction explicit: AX/parser/activity text is regex-only
	// today, while screenshots get GLiNER once the async detector is ready.
	if axText != "" {
		if redacted, n := pii.RedactText(axText); n > 0 {
			axText = redacted
		}
	}

	// 5. PII masking: scan all visible windows for sensitive apps (not just
	// the frontmost) and blur each matching window region.
	// Batch all regions into a single JPEG decode+encode to avoid O(n) cycles.
	blur := sensitiveBlurNone
	if perWindow {
		// The per-window image contains ONLY the focused window, so other windows'
		// sensitive content can't leak in. Blur the whole frame only when the
		// focused app itself is sensitive.
		if _, frontIsSensitive := pii.IsSensitiveApp(captureBundleID); frontIsSensitive {
			if blurred, err := pii.BlurEntireImage(jpegData, snapshotQuality); err == nil {
				jpegData = blurred
				blur = sensitiveBlurFull
			} else {
				log.Printf("[pii-mask] full blur failed: %v", err)
				blur = sensitiveBlurFailed
			}
		}
	} else if sensitiveWindows := activitymon.FindVisibleSensitiveWindows(pii.AllBundlePrefixes()); len(sensitiveWindows) > 0 {
		regions := make([]pii.WindowRegion, 0, len(sensitiveWindows))
		for _, wb := range sensitiveWindows {
			log.Printf("[pii-mask] sensitive window visible, blurring region (%.2f,%.2f %.2fx%.2f)",
				wb.X, wb.Y, wb.W, wb.H)
			regions = append(regions, pii.WindowRegion{X: wb.X, Y: wb.Y, W: wb.W, H: wb.H})
		}
		if blurred, err := pii.BlurRegionsBatch(jpegData, regions, snapshotQuality); err == nil {
			jpegData = blurred
			blur = sensitiveBlurRegions
		} else {
			log.Printf("[pii-mask] batch region blur failed: %v", err)
			blur = sensitiveBlurFailed
		}
	} else if _, frontIsSensitive := pii.IsSensitiveApp(captureBundleID); frontIsSensitive {
		// Fallback: if CGWindowList scanning returned nothing (e.g. non-darwin)
		// but the frontmost app is sensitive, blur its window or the entire frame.
		// Reuse the bounds captured at the same instant as the JPEG so the blur
		// rect can't drift if the user switches focus during the synchronous
		// PII pipeline.
		wb := captureFocusBounds
		if wb.W > 0 && wb.H > 0 {
			log.Printf("[pii-mask] sensitive app (%s), blurring window region (%.2f,%.2f %.2fx%.2f)",
				captureBundleID, wb.X, wb.Y, wb.W, wb.H)
			if blurred, err := pii.BlurRegion(jpegData, wb.X, wb.Y, wb.W, wb.H, snapshotQuality); err == nil {
				jpegData = blurred
				blur = sensitiveBlurRegions
			} else {
				log.Printf("[pii-mask] region blur failed: %v", err)
				blur = sensitiveBlurFailed
			}
		} else {
			log.Printf("[pii-mask] sensitive app (%s), blurring entire frame (no window bounds)", captureBundleID)
			if blurred, err := pii.BlurEntireImage(jpegData, snapshotQuality); err == nil {
				jpegData = blurred
				blur = sensitiveBlurFull
			} else {
				log.Printf("[pii-mask] full blur failed: %v", err)
				blur = sensitiveBlurFailed
			}
		}
	}

	// A sensitive window that could not be blurred must not reach disk.
	if blur == sensitiveBlurFailed {
		log.Printf("[pii-mask] sensitive window blur failed; frame dropped")
		return
	}

	if a.IsClosedEyesModeEnabled() {
		return
	}

	dateDir := now.Format("2006-01-02")
	snapName := fmt.Sprintf("%d-%s.jpg", now.UnixMilli(), uuid.New().String()[:8])
	relPath := filepath.Join(dateDir, snapName)
	fullPath := filepath.Join(am.store.SnapshotsDir(), relPath)

	if err := os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
		log.Printf("[activity] failed to create snapshot dir: %v", err)
		return
	}

	snapshot := snapshotEntry{
		relPath:     relPath,
		timestamp:   now.UnixMilli(),
		width:       w,
		height:      h,
		sizeBytes:   int64(len(jpegData)),
		trigger:     trigger,
		axText:      axText,
		browserURL:  browserURL,
		focusRectX:  captureFocusBounds.X,
		focusRectY:  captureFocusBounds.Y,
		focusRectW:  captureFocusBounds.W,
		focusRectH:  captureFocusBounds.H,
		contentType: parsedContentType,
		app:         parsedApp,
		parsedJSON:  parsedJSON,
	}

	// Defer file write to the async PII goroutine so the unmasked image
	// never lands on disk. Sensitive window regions are already blurred
	// synchronously above; the async pass handles OCR-based PII detection
	// for content outside those regions (e.g. API keys in a code editor).
	// Frames that arrive during the OCR cooldown, or while a pass is
	// running, are dropped rather than written unmasked. A fully blurred
	// frame has nothing legible left to mask, so it is written as is.
	ocrAvailable := am.ocrEngine != nil && am.ocrEngine.IsSupported()
	if !unmaskedWriteAllowed(blur, ocrAvailable) {
		ready, initInFlight := am.piiDetectorState()
		if !ready && !initInFlight {
			a.startPIIDetectorAsync(am)
			ready, initInFlight = am.piiDetectorState()
		}
		if !ready && initInFlight {
			queued := am.queuePendingPIISnapshot(pendingPIISnapshot{
				path:      fullPath,
				jpegData:  jpegData,
				quality:   snapshotQuality,
				snapshot:  snapshot,
				bundleID:  captureBundleID,
				parseHash: parsedHash,
			})
			if !queued {
				log.Printf("[pii-mask] pending screenshot not queued; frame dropped until GLiNER is ready")
			}
			return
		}

		piiBusyWait := time.Duration(0)
		if trigger == "session_end" {
			piiBusyWait = sessionEndWait
		}
		write := newSnapshotWrite(relPath)
		launch := am.runAsyncPIIMasking(fullPath, jpegData, snapshotQuality, now, piiOCRInterval(trigger), piiBusyWait, a.IsClosedEyesModeEnabled, write)
		switch launch {
		case piiMaskLaunched:
			snapshot.write = write
		case piiMaskThrottled, piiMaskBusy:
			// Blurring a sensitive window leaves the rest of the frame
			// legible, so a frame the mask cannot OCR now is dropped like
			// any other: written as is, it would put PII elsewhere on
			// screen on disk unmasked.
			if launch == piiMaskBusy {
				activityPipelineMetrics.captureDroppedPIIBusy.Add(1)
			}
			return
		}
	} else {
		// PII masking not available (or the whole frame is blurred), write
		// the image with any sensitive blurs already applied.
		if err := writeSnapshotFile(fullPath, jpegData); err != nil {
			log.Printf("[activity] failed to write snapshot: %v", err)
			return
		}
	}

	if a.IsClosedEyesModeEnabled() {
		_ = os.Remove(fullPath)
		return
	}

	am.appendCapturedSnapshot(snapshot, captureBundleID, parsedHash)
}

// frontmostSample is one reading of which app is frontmost.
type frontmostSample struct {
	bundleID string
	pid      int // 0 when unknown
}

// captureFocusStable reports whether a later sample of the frontmost app is
// still the app every privacy check of the frame keys on. The bundle ids and
// the pids must both match: two apps without a bundle id (Termius,
// command-line tools) otherwise look the same. A pid known on one side and
// unknown (0) on the other counts as a change. later is only meaningful where
// checkable is true (macOS reports the owner of the captured window; other
// platforms report nothing).
func captureFocusStable(capture, later frontmostSample, checkable bool) bool {
	if !checkable {
		return true
	}
	return later.bundleID == capture.bundleID && later.pid == capture.pid
}

// sensitiveBlur records what the synchronous sensitive-window blur did to a
// frame before the OCR-based PII mask.
type sensitiveBlur int

const (
	sensitiveBlurNone    sensitiveBlur = iota // no sensitive window in the frame
	sensitiveBlurRegions                      // sensitive windows blurred; the rest stays legible
	sensitiveBlurFull                         // the whole frame is blurred
	sensitiveBlurFailed                       // a sensitive window is visible but the blur failed
)

// unmaskedWriteAllowed reports whether a frame may be written without the
// OCR-based PII mask: only when nothing legible is left (the whole frame is
// blurred) or the platform has no OCR to mask with. A frame whose sensitive
// window could not be blurred is never written.
func unmaskedWriteAllowed(blur sensitiveBlur, ocrAvailable bool) bool {
	switch blur {
	case sensitiveBlurFailed:
		return false
	case sensitiveBlurFull:
		return true
	default:
		return !ocrAvailable
	}
}

// piiOCRInterval returns the minimum time between Apple Vision OCR launches
// for image-level PII masking. Heartbeat and session_end are forced store
// keepalives, so they bypass the event-driven cooldown and then reset the
// global OCR clock like any other OCR pass.
func piiOCRInterval(trigger string) time.Duration {
	if isForcedActivityCaptureTrigger(trigger) {
		return 0
	}
	if trigger == "heartbeat" || trigger == "session_end" {
		return 0
	}
	return piiOCRMinInterval
}

// reserveAXWalk reports whether an accessibility-tree walk should run now for a
// heavy AX app, advancing the per-bundle throttle clock when it returns true.
// Only called for apps where isHeavyAXWalkApp is true; every other app walks
// unconditionally.
func (am *activityMonitorState) reserveAXWalk(bundleID string, now time.Time) bool {
	nowMs := now.UnixMilli()
	minMs := int64(heavyAXWalkMinInterval / time.Millisecond)
	am.mu.Lock()
	defer am.mu.Unlock()
	if am.lastAXWalkMsByBundle == nil {
		am.lastAXWalkMsByBundle = make(map[string]int64)
	}
	if last := am.lastAXWalkMsByBundle[bundleID]; last > 0 && nowMs-last < minMs {
		return false
	}
	am.lastAXWalkMsByBundle[bundleID] = nowMs
	return true
}

func (am *activityMonitorState) reservePIIOCR(now time.Time, minInterval time.Duration) bool {
	if minInterval <= 0 {
		am.lastPIIOCRMs.Store(now.UnixMilli())
		return true
	}
	nowMs := now.UnixMilli()
	minMs := int64(minInterval / time.Millisecond)
	for {
		last := am.lastPIIOCRMs.Load()
		if last > 0 && nowMs-last < minMs {
			return false
		}
		if am.lastPIIOCRMs.CompareAndSwap(last, nowMs) {
			return true
		}
	}
}

func (am *activityMonitorState) appendCapturedSnapshot(entry snapshotEntry, bundleID, parsedHash string) bool {
	if am == nil {
		return false
	}
	am.mu.Lock()
	am.appendCapturedSnapshotLocked(entry, bundleID, parsedHash)
	am.mu.Unlock()
	activityPipelineMetrics.captureCompleted.Add(1)
	return true
}

// canElideParsedPayload reports whether a frame whose parsed content hashes
// to parsedHash can be stored as a dedup stub of the previous frame from the
// same app. The previous frame must carry that hash and its file must already
// be written. A referent still in the PII mask may yet be dropped, and if
// that happens before its session is saved it never becomes a row, so the
// stub would resolve to nothing. (A referent dropped after the save keeps
// its row and text, marked image-missing.)
func (am *activityMonitorState) canElideParsedPayload(bundleID, parsedHash string) bool {
	am.mu.Lock()
	prevHash := am.lastParsedHashByBundle[bundleID]
	prevWrite := am.lastParsedWriteByBundle[bundleID]
	am.mu.Unlock()
	return prevHash != "" && prevHash == parsedHash && prevWrite.writtenOK()
}

func (am *activityMonitorState) appendCapturedSnapshotLocked(entry snapshotEntry, bundleID, parsedHash string) {
	am.lastCaptureTime = entry.timestamp
	am.lastCaptureBundleID = bundleID
	am.lastSavedTime = entry.timestamp
	am.allSnapshots = append(am.allSnapshots, entry)
	// Advance the dedup hash only after a successful append. Doing it earlier
	// would record a hash for a payload dropped by PII throttling/busy and let
	// the next identical frame collapse to an unresolvable dedup stub.
	if parsedHash != "" {
		if am.lastParsedHashByBundle == nil {
			am.lastParsedHashByBundle = make(map[string]string)
		}
		am.lastParsedHashByBundle[bundleID] = parsedHash
		if am.lastParsedWriteByBundle == nil {
			am.lastParsedWriteByBundle = make(map[string]*snapshotWrite)
		}
		am.lastParsedWriteByBundle[bundleID] = entry.write
	}
}

// piiCacheEntry records the merged detection slice produced for a specific
// OCR observation fingerprint. Reused across frames when the fingerprint
// matches — safe because GLiNER + regex are deterministic on identical
// observations, so byte-equal inputs yield byte-equal outputs.
type piiCacheEntry struct {
	fingerprint uint64
	detections  []pii.DetectionResult
}

// observationFingerprint hashes the OCR output (text + bbox per region) so we
// can detect frames whose text layout didn't change. Using FNV-64a keeps the
// hot path branch-free; the probability of a collision across adjacent
// frames in practice is negligible, and a false-positive hit would only
// carry forward a still-correct detection (the preceding frame's PII regions
// correspond to the same text boxes).
func observationFingerprint(obs []ocr.TextObservation) uint64 {
	h := fnv.New64a()
	var buf [8]byte
	for _, o := range obs {
		h.Write([]byte(o.Text))
		h.Write([]byte{0})
		// Encode bbox as fixed-width IEEE 754 so a shifted box invalidates
		// the cache even when the text is unchanged.
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(o.X))
		h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(o.Y))
		h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(o.Width))
		h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(o.Height))
		h.Write(buf[:])
	}
	return h.Sum64()
}

type piiMaskLaunchResult int

const (
	piiMaskLaunched piiMaskLaunchResult = iota
	piiMaskBusy
	piiMaskThrottled
)

func writePIIProcessedSnapshot(path string, data []byte, shouldDiscard func() bool) (int64, bool) {
	if shouldDiscard != nil && shouldDiscard() {
		return 0, false
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("[pii-mask] mkdir failed: %v", err)
		return 0, false
	}
	if err := writeSnapshotFile(path, data); err != nil {
		log.Printf("[pii-mask] write failed: %v", err)
		return 0, false
	}
	return int64(len(data)), true
}

// maskAndWritePIISnapshot OCRs the frame, masks any PII it finds and writes
// the result to path. ok reports that the file was written; ocrErr is the OCR
// failure that dropped the frame, if that was the cause.
func (am *activityMonitorState) maskAndWritePIISnapshot(path string, jpegData []byte, quality int, shouldDiscard func() bool) (writtenBytes int64, ok bool, ocrErr error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[pii-mask] panic (frame dropped): %v", r)
			writtenBytes = 0
			ok = false
		}
	}()

	prio := ocr.PriorityMid
	callID := nextOCRBreadcrumbID()
	ocrStart := time.Now()
	log.Printf("[ocr] breadcrumb start call_id=%d source=pii-mask priority=%s file=%s bytes=%d",
		callID, ocrPriorityLabel(prio), filepath.Base(path), len(jpegData))

	observations, ocrErr := am.ocrEngine.RecognizeJPEGWithBoxes(jpegData, getOCRLanguagePrefs(am.ocrEngine), prio)
	if ocrErr != nil {
		log.Printf("[ocr] breadcrumb failed call_id=%d source=pii-mask duration_ms=%d err=%v",
			callID, time.Since(ocrStart).Milliseconds(), ocrErr)
		log.Printf("[pii-mask] OCR error (frame dropped): %v", ocrErr)
		return 0, false, ocrErr
	}
	log.Printf("[ocr] breadcrumb done call_id=%d source=pii-mask duration_ms=%d observations=%d",
		callID, time.Since(ocrStart).Milliseconds(), len(observations))
	if len(observations) == 0 {
		// No text found — image is clean, safe to write original.
		written, ok := writePIIProcessedSnapshot(path, jpegData, shouldDiscard)
		return written, ok, nil
	}

	// Skip the expensive GLiNER ONNX pass when this frame's OCR output is
	// byte-identical to the previous frame's — cursor blink, mouse motion, or
	// anti-aliasing flicker dominate the capture rate while the reader is idle,
	// and re-running inference on identical observations just reproduces the
	// same detections. Cache miss or a nil result still takes the full path.
	fingerprint := observationFingerprint(observations)
	var detections []pii.DetectionResult
	var glinerCount, regexCount int
	cached := am.lastPIIResult.Load()
	if cached != nil && cached.fingerprint == fingerprint {
		detections = cached.detections
	} else {
		var glinerDetections []pii.DetectionResult
		am.piiDetectorMu.RLock()
		if am.piiDetector != nil && am.piiDetector.IsReady() {
			glinerDetections = am.piiDetector.Detect(observations)
		}
		am.piiDetectorMu.RUnlock()
		regexDetections := pii.DetectWithRegex(observations)
		detections = pii.MergeDetections(glinerDetections, regexDetections)
		glinerCount = len(glinerDetections)
		regexCount = len(regexDetections)
		am.lastPIIResult.Store(&piiCacheEntry{
			fingerprint: fingerprint,
			detections:  detections,
		})
	}
	if len(detections) == 0 {
		// No PII detected — image is clean, safe to write original.
		written, ok := writePIIProcessedSnapshot(path, jpegData, shouldDiscard)
		return written, ok, nil
	}

	if cached == nil || cached.fingerprint != fingerprint {
		log.Printf("[pii-mask] detected %d PII regions in %s (gliner=%d, regex=%d)",
			len(detections), filepath.Base(path), glinerCount, regexCount)
	}

	masked, maskErr := pii.MaskRegions(jpegData, detections, quality)
	if maskErr != nil {
		log.Printf("[pii-mask] mask failed (frame dropped): %v", maskErr)
		return 0, false, nil
	}
	written, ok := writePIIProcessedSnapshot(path, masked, shouldDiscard)
	return written, ok, nil
}

// runAsyncPIIMasking runs OCR + GLiNER + regex detection asynchronously.
// The goroutine writes the file to disk only when the image is confirmed clean
// (no text or no PII detected) or after successful masking. On any error
// (OCR failure, masking failure, panic), the frame is dropped to ensure
// unmasked PII never lands on disk, and write records the drop so the
// screenshot never becomes a row without a file.
// Returns whether the goroutine was launched (and will handle the file write),
// or why this frame should be dropped / handled by the caller.
func (am *activityMonitorState) runAsyncPIIMasking(path string, jpegData []byte, quality int, now time.Time, minInterval, busyWait time.Duration, shouldDiscard func() bool, write *snapshotWrite) piiMaskLaunchResult {
	// Non-blocking send by default: if PII goroutines are all busy, tell the
	// caller to drop the frame rather than writing unmasked data. busyWait > 0
	// (the session_end final frame) instead waits out the in-flight pass —
	// that frame is the session's last chance at a snapshot and must not be
	// lost to a transient collision.
	acquired := false
	select {
	case am.piiSem <- struct{}{}:
		acquired = true
	default:
	}
	if !acquired && busyWait > 0 {
		select {
		case am.piiSem <- struct{}{}:
			acquired = true
		case <-time.After(busyWait):
		}
	}
	if !acquired {
		activityPipelineMetrics.piiBusyDrops.Add(1)
		return piiMaskBusy
	}

	if !am.reservePIIOCR(now, minInterval) {
		<-am.piiSem
		activityPipelineMetrics.piiThrottled.Add(1)
		return piiMaskThrottled
	}

	activityPipelineMetrics.piiLaunched.Add(1)
	activityPipelineMetrics.piiInFlight.Add(1)
	go func() {
		piiStart := time.Now()
		defer func() {
			activityPipelineMetrics.piiDurations.add(time.Since(piiStart))
			activityPipelineMetrics.piiInFlight.Add(-1)
			<-am.piiSem
		}()

		written, ok, _ := am.maskAndWritePIISnapshot(path, jpegData, quality, shouldDiscard)
		am.finishSnapshotWrite(write, written, ok)
	}()
	return piiMaskLaunched
}

// runVisualChangeDetection periodically screenshots and compares frames to detect
// screen content changes without user input (video playback, animations, auto-scroll).
// Mirrors screenpipe's VisualChange trigger: check every 3s, fire if diff > 5%.
func (a *App) runVisualChangeDetection() {
	am := a.activity
	if am == nil {
		return
	}

	am.mu.Lock()
	stopCh := am.visualStopCh
	am.mu.Unlock()

	ticker := time.NewTicker(visualCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			a.checkVisualChange()
		}
	}
}

// checkVisualChange captures a frame and triggers a snapshot if the screen
// content changed significantly (above visualChangeThreshold).
// Matches screenpipe: visual check captures for comparison only, then triggers
// a full capture through the standard path (which includes content dedup).
func (a *App) checkVisualChange() {
	if a.IsClosedEyesModeEnabled() {
		return
	}
	activityPipelineMetrics.visualChecks.Add(1)
	visualStart := time.Now()
	defer func() {
		activityPipelineMetrics.visualDurations.add(time.Since(visualStart))
	}()

	am := a.activity
	if am == nil {
		activityPipelineMetrics.visualSkipped.Add(1)
		return
	}

	// Skip when screen is locked — avoids wasting disk on lock screen frames.
	if activitymon.IsScreenLocked() {
		activityPipelineMetrics.visualSkipped.Add(1)
		return
	}

	// Skip if in capture failure backoff period.
	now := time.Now()
	nowMs := now.UnixMilli()
	am.mu.Lock()
	inBackoff := !am.captureBackoff.IsZero() && now.Before(am.captureBackoff)
	recentUserActivity := am.hasRecentUserActivityForVisualChangeLocked(nowMs)
	timeSinceCapture := nowMs - am.lastCaptureTime
	am.mu.Unlock()
	if inBackoff {
		activityPipelineMetrics.visualSkipped.Add(1)
		return
	}
	if !recentUserActivity {
		activityPipelineMetrics.visualSkipped.Add(1)
		return
	}

	// Respect the global debounce — don't check if we just captured
	if timeSinceCapture < int64(snapshotDebounceMs) {
		activityPipelineMetrics.visualSkipped.Add(1)
		return
	}

	// Pipeline back-pressure — if PII is saturated, skip this tick. The
	// visual-change path is a pure optimisation (catch changes without
	// user input), heartbeat will still keep the store alive at the
	// maxSkipDuration cadence.
	if len(am.piiSem) >= cap(am.piiSem) {
		activityPipelineMetrics.visualSkipped.Add(1)
		return
	}

	// Capture at reduced resolution for visual comparison only.
	// Full-res CaptureRaw() allocates ~14 MB per call; capturing at 1/6
	// resolution reduces this to ~400 KB while producing equivalent
	// grayscale histograms for change detection.
	sw, sh := activitymon.ScreenDimensions()
	tw := sw / dedupDownscaleFactor
	th := sh / dedupDownscaleFactor
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	bgra, w, h, err := activitymon.CaptureScreen(tw, th)
	if err != nil || len(bgra) == 0 {
		activityPipelineMetrics.visualSkipped.Add(1)
		return
	}
	defer activitymon.ReleaseCaptureBuffer(bgra)

	// Convert BGRA to grayscale and compare with previous frame
	gray := bgraToGray(bgra, w, h)
	dist := am.comparer.distanceFromGray(gray)
	if dist < visualChangeThreshold {
		activityPipelineMetrics.visualSkipped.Add(1)
		return
	}

	// Screen changed — capture through the standard path
	// (includes AX tree walk, content dedup, JPEG encode, etc.)
	activityPipelineMetrics.visualPromoted.Add(1)
	a.captureSnapshotForEvent("visual_change")
}

// extractSession takes events from the ring buffer and persists a session with snapshots.
// lateSpeechOfEndedCall reports whether every event lies inside the span of
// the last saved call: speech transcribed after that call ended.
func (am *activityMonitorState) lateSpeechOfEndedCall(events []activitymon.ActivityEvent) bool {
	am.mu.Lock()
	start, end := am.endedMeetingSpanStartMs, am.endedMeetingSpanEndMs
	am.mu.Unlock()
	if len(events) == 0 || end <= 0 {
		return false
	}
	for _, evt := range events {
		if evt.Timestamp < start || evt.Timestamp > end {
			return false
		}
	}
	return true
}

func (a *App) extractSession(triggerKind string) {
	am := a.activity
	if am == nil {
		return
	}
	if a.IsClosedEyesModeEnabled() {
		a.discardPendingActivityRecording(am)
		return
	}

	sessionID := uuid.New().String()
	audioLocked := a.isBackgroundListeningScreenLocked()
	isPeriodicFlush := triggerKind == "periodic_flush"
	isTerminalMeeting := triggerKind == "meeting_ended" || triggerKind == "shutdown_in_meeting"

	am.mu.Lock()
	// During a call only its end (meeting_ended, shutdown_in_meeting) and
	// its 2-hour slice set session boundaries; anything else (a periodic
	// flush, an idle timer armed before the call, a deferred extraction)
	// would end the call's state while its recording stayed attached.
	if am.inMeeting && !isTerminalMeeting && triggerKind != meetingSliceTrigger {
		am.mu.Unlock()
		if !isPeriodicFlush {
			log.Printf("[activity] %s ignored during a call", triggerKind)
		}
		return
	}
	am.mu.Unlock()

	if am.buf.Len() == 0 {
		// Even with no events to persist, a terminal meeting trigger
		// (meeting_ended / shutdown_in_meeting) MUST clear the active
		// meeting state and stash the meeting_id so the next
		// MEETING_STARTED can resume it via the watcher-flicker window.
		// Skipping this is what made consecutive 30-second Zoom blips
		// each mint a fresh meeting_id even though they were one call.
		a.flushMeetingStateOnEmptyExtract(am, isPeriodicFlush, triggerKind)
		return
	}

	if a.deferExtractSessionUntilPIIReady(am, triggerKind) {
		return
	}

	// Single critical section: bail-out check, atomic buffer drain, meeting
	// metadata snapshot, and transient state reset — all under am.mu so we
	// cannot race the meeting watcher between the inMeeting check and the
	// meeting state reset. The buffer drain is atomic (one buf.mu acquisition
	// instead of separate Snapshot + Clear) so two concurrent extractSession
	// calls cannot both observe the same events and persist them under
	// different session IDs.
	am.mu.Lock()
	meetingRecordingEpoch, _ := a.automaticMeetingRecordingEpoch()
	if isPeriodicFlush && (am.inMeeting || am.meetingCandidateActive) {
		am.mu.Unlock()
		return
	}
	events := am.buf.Drain()
	if am.inMeeting && (isTerminalMeeting || triggerKind == meetingSliceTrigger) {
		// Recorded with the drain, under the same lock: an extraction of the
		// call's late speech drains after this and always sees the span.
		start := am.meetingStartMs
		for _, evt := range events {
			if start == 0 || evt.Timestamp < start {
				start = evt.Timestamp
			}
		}
		am.endedMeetingSpanStartMs, am.endedMeetingSpanEndMs = start, time.Now().UnixMilli()
	}
	if len(events) == 0 {
		var candidateAudioWriter *meetingAudioWriter
		if !isPeriodicFlush && am.meetingCandidateActive {
			candidateAudioWriter = am.meetingCandidateAudioWriter
			am.meetingCandidateAudioWriter = nil
			am.meetingCandidateActive = false
			am.meetingCandidateAppName = ""
			am.meetingCandidateStartMs = 0
			am.meetingCandidateRecordingEpoch = 0
			am.lastMeetingCandidateAudioMs = 0
		}
		// Same as the buf.Len()==0 early-return above: terminal meeting
		// triggers must clear meeting state + stash meeting_id even when
		// there's nothing to persist, otherwise the resume-window can't
		// rescue back-to-back watcher flickers.
		var endedRecording *meetingAudioWriter
		endedEpoch := am.meetingRecordingEpoch
		if !isPeriodicFlush && am.inMeeting && triggerKind != meetingSliceTrigger {
			endedRecording = detachCallRecordingLocked(am)
		}
		if !isPeriodicFlush && am.inMeeting && am.meetingID != "" && triggerKind != meetingSliceTrigger {
			am.lastMeetingID = am.meetingID
			am.lastMeetingApp = am.meetingAppName
			am.lastMeetingEndMs = time.Now().UnixMilli()
			am.lastMeetingPaused = audioLocked
			am.inMeeting = false
			am.meetingAppName = ""
			am.meetingBundleID = ""
			am.meetingStartMs = 0
			am.meetingRecordingEpoch = 0
			am.meetingID = ""
			am.lastMeetingEventMs = 0
			am.lastMeetingAudioMs = 0
			am.meetingBrowserMicReleased = false
			am.meetingBrowserGapSinceMs = 0
			am.meetingBrowserObservedMs = 0
		}
		am.mu.Unlock()
		if candidateAudioWriter != nil {
			candidateAudioWriter.Abort()
		}
		a.persistEndedCallRecording(endedRecording, endedEpoch, triggerKind)
		return
	}
	if am.heartbeatTimer != nil {
		am.heartbeatTimer.Stop()
		am.heartbeatTimer = nil
	}
	if am.typingTimer != nil {
		am.typingTimer.Stop()
		am.typingTimer = nil
	}
	am.firstSpeechTime = 0
	// Snapshot meeting metadata BEFORE clearing so we can force IsMeeting=true
	// on the session even if speech was sparse (listener-only meetings). Also
	// use meetingStartMs as a lower bound on session StartTime.
	wasInMeeting := am.inMeeting
	wasMeetingApp := am.meetingAppName
	wasMeetingStartMs := am.meetingStartMs
	wasMeetingRecordingEpoch := am.meetingRecordingEpoch
	wasMeetingID := am.meetingID
	wasCandidateActive := am.meetingCandidateActive
	wasCandidateStartMs := am.meetingCandidateStartMs
	wasCandidateRecordingEpoch := am.meetingCandidateRecordingEpoch
	// Activity rotations persist events, but the call owns one recording until
	// its end. Rotating the writer here split sibling mic/system chunks across
	// files and allowed a late write into an already finalized writer.
	isMeetingSlice := triggerKind == meetingSliceTrigger
	meetingSliceEndMs := int64(0)
	// Persist pending identities and privacy metadata before the rotation
	// releases its lock. Recovery recreates these exact rows after a failed
	// transaction; committed-but-missing dependencies remain fail-closed.
	sourceWriters := []*meetingAudioWriter{am.meetingAudioWriter, am.meetingCandidateAudioWriter}
	if isMeetingSlice || isTerminalMeeting || wasCandidateActive {
		start := wasMeetingStartMs
		if wasCandidateActive && (start == 0 || wasCandidateStartMs < start) {
			start = wasCandidateStartMs
		}
		proof := callRecordingSource(sessionID, start, time.Now().UnixMilli(), events)
		for _, writer := range sourceWriters {
			if err := writer.reserveActivitySession(proof, !isMeetingSlice); err != nil {
				log.Printf("[meeting-audio] reserve source: %v", err)
				writer.Abort()
			}
		}
	}
	var audioWriter *meetingAudioWriter
	if isTerminalMeeting {
		audioWriter = detachCallRecordingLocked(am)
	}
	var candidateAudioWriter *meetingAudioWriter
	if !isPeriodicFlush && !isMeetingSlice && wasCandidateActive {
		candidateAudioWriter = am.meetingCandidateAudioWriter
		am.meetingCandidateAudioWriter = nil
	}
	// Skip the meeting-state reset for periodic_flush: we verified !inMeeting
	// above under the same lock, and the meeting watcher is allowed to flip
	// it back to true as soon as we release. Clearing it would trample the
	// watcher's transition. Other triggers ARE meaningful session boundaries
	// (idle is suppressed during meetings; meeting_* are the boundary
	// themselves) so the reset is correct for them.
	if isMeetingSlice {
		// Activity slices retain disjoint time ranges. The writer separately
		// owns the call's original start and every slice's provenance.
		nowMs := time.Now().UnixMilli()
		meetingSliceEndMs = nowMs
		if am.inMeeting {
			am.meetingStartMs = nowMs
		}
		if am.meetingCandidateActive {
			am.meetingCandidateStartMs = nowMs
		}
	} else if !isPeriodicFlush {
		// Remember this meeting's id so a follow-up MEETING_STARTED within
		// the resume window for the same app can reuse it. Only stash when
		// the just-flushed session actually represented a meeting; don't
		// poison the resume slot from a non-meeting flush.
		if wasInMeeting && am.meetingID != "" {
			am.lastMeetingID = am.meetingID
			am.lastMeetingApp = am.meetingAppName
			am.lastMeetingEndMs = time.Now().UnixMilli()
			am.lastMeetingPaused = audioLocked
		}
		am.inMeeting = false
		am.meetingAppName = ""
		am.meetingBundleID = ""
		am.meetingStartMs = 0
		am.meetingRecordingEpoch = 0
		am.meetingID = ""
		am.lastMeetingEventMs = 0
		am.lastMeetingAudioMs = 0
		am.meetingBrowserMicReleased = false
		am.meetingBrowserGapSinceMs = 0
		am.meetingBrowserObservedMs = 0
		am.meetingCandidateActive = false
		am.meetingCandidateAppName = ""
		am.meetingCandidateStartMs = 0
		am.meetingCandidateRecordingEpoch = 0
		am.lastMeetingCandidateAudioMs = 0
	}
	if am.visualStopCh != nil {
		close(am.visualStopCh)
		am.visualStopCh = nil
	}
	am.mu.Unlock()

	// Final capture: guarantee at least one snapshot per session.
	// This runs synchronously before draining, so it appends to allSnapshots
	// and resolves the race where heartbeat and idle fire simultaneously.
	//
	// For periodic_flush the capture is normally skipped — the user is still
	// mid-stream, the heartbeat timer (60s) already drives captures, and the
	// synchronous capture work would introduce visible stutter every 5
	// minutes during active use. BUT the skip only holds when the session
	// already has at least one frame: a short flush window (<60s of activity,
	// no heartbeat) whose event captures were all eaten by dedup/back-pressure
	// gates would otherwise persist with zero snapshots, and the Timeline
	// session view has nothing to show for it. Triage-level merging does not
	// rescue that — it merges triage groups, not the per-session recording.
	am.mu.Lock()
	haveSessionSnapshots := len(am.allSnapshots) > 0
	am.mu.Unlock()
	if triggerKind != "periodic_flush" || !haveSessionSnapshots {
		a.captureSnapshotForEventWithWait("session_end", finalCaptureWait(triggerKind))
	}

	// Collect accumulated snapshots and reset backoff state for fresh start.
	am.mu.Lock()
	snapshots := make([]snapshotEntry, len(am.allSnapshots))
	copy(snapshots, am.allSnapshots)
	am.allSnapshots = nil
	am.lastParsedHashByBundle = nil // fresh dedup window per session
	am.lastParsedWriteByBundle = nil
	am.captureFailCount = 0
	am.captureBackoff = time.Time{}
	am.captureBurstStartedAt = time.Time{}
	am.mu.Unlock()

	// Reset frame comparer so next session starts fresh
	am.comparer.reset()

	// The events kept below span the captured frames, including any the
	// PII mask is about to drop, so take the span before the wait.
	var snapshotSpanStart, snapshotSpanEnd int64
	if len(snapshots) > 0 {
		snapshotSpanStart = snapshots[0].timestamp
		snapshotSpanEnd = snapshots[len(snapshots)-1].timestamp
	}

	// Screenshots still in the async PII mask (normally the session_end frame
	// above) have no file yet. Wait briefly for them and leave out any the
	// mask dropped, so no row points at a file that was never written.
	snapshots = awaitSnapshotWrites(snapshots, snapshotWriteWait(triggerKind))

	// Collect unique app names and find the dominant (most-events) bundle ID.
	appSet := make(map[string]bool)
	bundleCounts := make(map[string]int)
	for _, evt := range events {
		if evt.AppName != "" {
			appSet[evt.AppName] = true
		}
		if evt.AppBundleID != "" {
			bundleCounts[evt.AppBundleID]++
		}
		if evt.MeetingRecordingEpoch == meetingRecordingEpoch && evt.MeetingCandidate && meetingdetect.IsTranscriptionSupportedMeetingName(evt.MeetingCandidateApp) {
			appSet[evt.MeetingCandidateApp] = true
		}
	}
	appNames := make([]string, 0, len(appSet))
	for name := range appSet {
		appNames = append(appNames, name)
	}

	// If ANY app in the session is protected/sensitive, mark the entire
	// session protected so the triage pipeline never evaluates it and no
	// memories are generated. Previously only the dominant (most-events)
	// app was checked, which missed protected apps that the user
	// switched away from briefly (e.g. writing a diary in 手记 while
	// checking WeChat/terminal — the latter generate more events).
	// Checked by bundle ID first, then by display name for events that
	// carry a name but no bundle ID.
	var sensitiveBundle string
	for bid := range bundleCounts {
		if _, isSensitive := pii.IsSensitiveApp(bid); isSensitive {
			sensitiveBundle = bid
			break
		}
	}
	if sensitiveBundle == "" {
		for name := range appSet {
			if pii.IsSensitiveAppName(name) {
				sensitiveBundle = name
				break
			}
		}
	}

	appNamesJSON, _ := json.Marshal(appNames)

	nowMs := time.Now().UnixMilli()

	// Build snapshot rows
	snapshotRows := make([]ActivitySnapshotRow, 0, len(snapshots))
	var totalSize int64
	for _, snap := range snapshots {
		snapshotRows = append(snapshotRows, ActivitySnapshotRow{
			SessionID:   sessionID,
			Timestamp:   snap.timestamp,
			FilePath:    snap.relPath,
			Width:       snap.width,
			Height:      snap.height,
			SizeBytes:   snap.sizeBytes,
			Trigger:     snap.trigger,
			StorageTier: "hot",
			AXText:      snap.axText,
			BrowserURL:  snap.browserURL,
			ContentType: snap.contentType,
			App:         snap.app,
			ParsedJSON:  snap.parsedJSON,
			FocusRectX:  snap.focusRectX,
			FocusRectY:  snap.focusRectY,
			FocusRectW:  snap.focusRectW,
			FocusRectH:  snap.focusRectH,
		})
		totalSize += snap.sizeBytes
	}

	// Filter events to only those within snapshot time range
	if snapshotSpanEnd > 0 {
		earliest := snapshotSpanStart
		latest := snapshotSpanEnd
		filtered := make([]activitymon.ActivityEvent, 0, len(events))
		for _, evt := range events {
			if evt.Kind == activitymon.EventSpeech || (evt.Timestamp >= earliest && evt.Timestamp <= latest) {
				filtered = append(filtered, evt)
			}
		}
		if len(filtered) > 0 {
			events = filtered
		}
	}

	startTime := events[0].Timestamp
	endTime := events[len(events)-1].Timestamp

	// "protected" is a distinct terminal state from "dismissed": the triage
	// pipeline writes "dismissed" for every NORMALLY COMPLETED group
	// (activity_triage.go: "dismissed" = done), so privacy consumers must
	// not key off it. "protected" marks sessions that must never be
	// triaged, OCR'd, embedded, searched, or surfaced in prompts.
	triageStatus := "pending"
	if sensitiveBundle != "" {
		triageStatus = "protected"
		log.Printf("[activity] session %s protected: app %s is in the protected list (%v)", sessionID, sensitiveBundle, appNames)
	}

	session := ActivitySessionRow{
		ID:             sessionID,
		StartTime:      startTime,
		EndTime:        endTime,
		TriggerKind:    triggerKind,
		AppNames:       string(appNamesJSON),
		EventCount:     len(events),
		SegmentCount:   0,
		SnapshotCount:  len(snapshotRows),
		TotalSizeBytes: totalSize,
		TriageStatus:   triageStatus,
		CreatedAt:      nowMs,
	}

	speechCount, speechChars := collectSpeechEventStats(events)

	// Serialize the meeting-specific classification, audio finalization, and
	// activity-row commit with preference changes. Ordinary activity still
	// persists when the captured generation is stale; only meeting artifacts
	// are discarded. The fence closes the final check-to-commit race.
	a.meetingRecordingMu.Lock()
	meetingRecordingFenceHeld := true
	defer func() {
		if meetingRecordingFenceHeld {
			a.meetingRecordingMu.Unlock()
		}
	}()
	meetingRecordingAllowed := a.automaticMeetingRecordingEpochIsCurrent(meetingRecordingEpoch)
	meetingEvents := meetingSpeechEventsForRecordingEpoch(events, meetingRecordingEpoch)
	meetingSpeechCount, _ := collectSpeechEventStats(meetingEvents)
	wasCurrentMeeting := meetingRecordingAllowed && wasInMeeting && wasMeetingRecordingEpoch == meetingRecordingEpoch
	wasCurrentCandidate := meetingRecordingAllowed && wasCandidateActive && wasCandidateRecordingEpoch == meetingRecordingEpoch

	// Detect meeting sessions. The watcher is the source of truth when it
	// flagged the session as a meeting; fall back to speech-event inference
	// otherwise. The watcher path covers listener-only meetings where no
	// speech events were captured.
	meetingApp, isMeeting := "", false
	candidateMeetingApp, isCandidateMeeting := "", false
	var finalizedAudioWriter *meetingAudioWriter
	if meetingRecordingAllowed {
		meetingApp, isMeeting = detectMeetingInEvents(meetingEvents)
		candidateMeetingApp, isCandidateMeeting = detectMeetingCandidateInEvents(meetingEvents)
		if isMeeting && !wasCurrentMeeting && am.lateSpeechOfEndedCall(meetingEvents) {
			log.Printf("[activity] %d late speech events from the ended call kept as activity, not a second meeting", len(meetingEvents))
			meetingApp, isMeeting = "", false
		}
		if wasCurrentMeeting && wasMeetingApp != "" {
			session.IsMeeting = true
			session.MeetingApp = wasMeetingApp
			// Honor the watcher's earlier boundary so the transcript covers the
			// full call even when the first captured event arrived later.
			if wasMeetingStartMs > 0 {
				session.StartTime = wasMeetingStartMs
			}
			if isMeetingSlice {
				session.EndTime = meetingSliceEndMs
			}
			if !isMeeting {
				// Override the local vars for the log line below.
				meetingApp = wasMeetingApp
				isMeeting = true
			}
		} else if isMeeting {
			session.IsMeeting = true
			session.MeetingApp = meetingApp
		} else if wasCurrentCandidate && isCandidateMeeting {
			session.IsMeeting = true
			session.MeetingApp = candidateMeetingApp
			meetingApp = candidateMeetingApp
			isMeeting = true
			if wasCandidateStartMs > 0 {
				session.StartTime = wasCandidateStartMs
			}
			if isMeetingSlice {
				session.EndTime = meetingSliceEndMs
			}
		}
	}
	// Complete the pending journal with final snapshot privacy before audio
	// finalization or the row commit can fail. It contains no captured content.
	if isMeetingSlice || isTerminalMeeting || wasCandidateActive {
		proof := callRecordingSource(session.ID, session.StartTime, session.EndTime, events)
		proof.Private = proof.Private || session.TriageStatus == "protected"
		for _, snapshot := range snapshotRows {
			proof.Apps = append(proof.Apps, snapshot.App)
			if strings.TrimSpace(snapshot.ParsedJSON) != "" {
				var content struct {
					Title    string            `json:"title"`
					Metadata map[string]string `json:"metadata"`
				}
				if json.Unmarshal([]byte(snapshot.ParsedJSON), &content) != nil {
					proof.Private = true
				} else {
					proof.Apps = append(proof.Apps, content.Metadata["bundleId"])
					proof.Private = proof.Private || activitymon.IsPrivateBrowsing(content.Title)
				}
			}
		}
		for _, writer := range sourceWriters {
			if err := writer.completeActivitySession(proof, !isMeetingSlice); err != nil {
				log.Printf("[meeting-audio] finalize source privacy: %v", err)
				writer.Abort()
			}
		}
	}
	var protectedMeetingMetas []meetingAudioMeta
	// Keep captured app identities in the committed owner as well, so privacy
	// survives later audio retention that removes its sidecar.
	for _, writer := range []*meetingAudioWriter{audioWriter, candidateAudioWriter} {
		if writer != nil {
			meta := writer.metaSnapshot()
			if meetingRecordingTriageStatus(meta) == "protected" {
				session.TriageStatus = "protected"
				protectedMeetingMetas = append(protectedMeetingMetas, meta)
			}
			appNames = compactStrings(append(appNames, meta.CaptureSource.Apps...))
			if raw, err := json.Marshal(appNames); err == nil {
				session.AppNames = string(raw)
			}
		}
	}

	if audioWriter != nil {
		if wasCurrentMeeting && session.IsMeeting {
			// Privacy follows the entire recording, including earlier rotations.
			meta := audioWriter.metaSnapshot()
			if meta.Protected {
				session.TriageStatus = "protected"
				protectedMeetingMetas = append(protectedMeetingMetas, meta)
			}
			// Input events can stop long before a listen-only call ends.
			session.EndTime = time.Now().UnixMilli()
			var err error
			session.AudioPath, session.AudioSizeBytes, err = audioWriter.Finalize()
			if err != nil {
				audioWriter.RemoveFinalized(session.AudioPath)
				audioWriter.KeepForRecovery(session.ID)
				session.AudioPath = ""
				session.AudioSizeBytes = 0
				log.Printf("[meeting-audio] finalize error: %v", err)
			} else if session.AudioPath != "" {
				finalizedAudioWriter = audioWriter
				log.Printf("[meeting-audio] saved: %s (%d bytes)", session.AudioPath, session.AudioSizeBytes)
			}
		} else {
			audioWriter.Abort()
		}
	}

	if candidateAudioWriter != nil {
		if session.AudioPath == "" && wasCurrentCandidate && session.IsMeeting && session.MeetingApp == candidateMeetingApp && isCandidateMeeting {
			var err error
			session.AudioPath, session.AudioSizeBytes, err = candidateAudioWriter.Finalize()
			if err != nil {
				candidateAudioWriter.RemoveFinalized(session.AudioPath)
				candidateAudioWriter.KeepForRecovery(session.ID)
				session.AudioPath = ""
				session.AudioSizeBytes = 0
				log.Printf("[meeting-audio] finalize candidate error: %v", err)
			} else if session.AudioPath != "" {
				finalizedAudioWriter = candidateAudioWriter
				log.Printf("[meeting-audio] saved candidate: %s (%d bytes)", session.AudioPath, session.AudioSizeBytes)
			}
		} else {
			candidateAudioWriter.Abort()
		}
	}

	privacyWithdrawalScopes := make([]privacyWithdrawalScope, 0, 1+len(protectedMeetingMetas)*3)
	seenPrivacyWithdrawalScopes := make(map[privacyWithdrawalScope]struct{}, cap(privacyWithdrawalScopes))
	addPrivacyWithdrawalScope := func(scope privacyWithdrawalScope) {
		if _, exists := seenPrivacyWithdrawalScopes[scope]; exists {
			return
		}
		seenPrivacyWithdrawalScopes[scope] = struct{}{}
		privacyWithdrawalScopes = append(privacyWithdrawalScopes, scope)
	}
	if sensitiveBundle != "" {
		addPrivacyWithdrawalScope(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: session.ID})
	}
	for _, meta := range protectedMeetingMetas {
		addPrivacyWithdrawalScope(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: session.ID})
		for _, sessionID := range meta.ActivitySessions {
			if sessionID != "" && sessionID != session.ID {
				addPrivacyWithdrawalScope(privacyWithdrawalScope{Kind: privacyWithdrawalSession, ID: sessionID})
			}
		}
		if meta.StartMs > 0 {
			addPrivacyWithdrawalScope(privacyWithdrawalScope{
				Kind:    privacyWithdrawalTimeRange,
				StartMs: meta.StartMs,
				EndMs:   session.EndTime,
			})
		}
	}

	audioCommitted := false
	defer func() {
		// The session could not be written (every early return below is a
		// database failure, a full disk most likely). The finished WAV and
		// its sidecar stay, as saveCallRecordingLocked keeps them: a
		// recording without a row beats no recording, and storage never
		// sweeps a recording that has its sidecar.
		if !audioCommitted && finalizedAudioWriter != nil && session.AudioPath != "" {
			log.Printf("[meeting-audio] session %s not saved; its recording %s stays on disk", session.ID, session.AudioPath)
		}
	}()

	if speechCount > 0 {
		log.Printf(
			"[activity] persisting speech events: session=%s trigger=%s speech=%d/%d chars=%d meeting=%v(%s)",
			sessionID,
			triggerKind,
			speechCount,
			len(events),
			speechChars,
			isMeeting,
			meetingApp,
		)
	}

	// Screenshots still being masked count as pending for this session from
	// before its commit, so the OCR worker waits for their files.
	am.trackSnapshotWrites(sessionID, snapshots)
	snapshotsCommitted := false
	defer func() {
		if !snapshotsCommitted {
			am.untrackSnapshotWrites(snapshots)
		}
	}()

	// Save in a transaction: session + events + snapshots
	tx, err := am.store.BeginTx()
	if err != nil {
		log.Printf("[activity] failed to begin tx: %v", err)
		return
	}
	defer tx.Rollback()

	// Insert session
	isMeetingInt := 0
	if session.IsMeeting {
		isMeetingInt = 1
	}
	_, err = tx.Exec(`
		INSERT INTO activity_sessions (id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, snapshot_count, total_size_bytes, triage_status,
			is_meeting, meeting_app, transcript_path, audio_path, audio_size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.StartTime, session.EndTime, session.TriggerKind,
		session.AppNames, session.EventCount, session.SegmentCount, session.SnapshotCount,
		session.TotalSizeBytes, session.TriageStatus,
		isMeetingInt, session.MeetingApp, session.TranscriptPath,
		session.AudioPath, session.AudioSizeBytes,
		session.CreatedAt,
	)
	if err != nil {
		log.Printf("[activity] failed to insert session: %v", err)
		return
	}

	// Insert events
	if len(events) > 0 {
		stmt, err := tx.Prepare(`
			INSERT INTO activity_events (session_id, timestamp, kind, data, sort_order)
			VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			log.Printf("[activity] failed to prepare event insert: %v", err)
			return
		}
		defer stmt.Close()

		for i := range events {
			evt := &events[i]
			// Scrub content-bearing fields from protected-app events before
			// they hit disk. Downstream consumers (timeline summaries, daily
			// summaries, activity context injection) read these raw rows
			// without re-checking sensitivity, so the scrub must happen here.
			// Mutates the slice element in place so in-memory consumers after
			// this point (meeting transcript copy, timeline reconcile) see
			// the scrubbed event too. The event itself is kept (bundle ID +
			// timestamp) for timeline continuity, but carries no readable
			// content.
			scrubProtectedActivityEvent(evt)
			data, _ := json.Marshal(evt)
			if _, err := stmt.Exec(session.ID, evt.Timestamp, string(evt.Kind), string(data), i); err != nil {
				log.Printf("[activity] failed to insert event %d: %v", i, err)
				return
			}
		}
	}

	// Insert snapshots
	if err := am.store.SaveSnapshots(tx, snapshotRows); err != nil {
		log.Printf("[activity] failed to save snapshots: %v", err)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("[activity] failed to commit session: %v", err)
		return
	}
	for _, writer := range sourceWriters {
		writer.commitActivitySession(sessionID)
	}
	snapshotsCommitted = true
	am.markSnapshotWritesPersisted(snapshots)
	audioCommitted = true
	a.meetingRecordingMu.Unlock()
	meetingRecordingFenceHeld = false
	for _, scope := range privacyWithdrawalScopes {
		publishPrivacyWithdrawal(scope)
	}

	if speechCount > 0 {
		log.Printf(
			"[activity] persisted speech session: session=%s speech=%d snapshots=%d trigger=%s",
			sessionID,
			speechCount,
			len(snapshotRows),
			triggerKind,
		)
	}

	am.mu.Lock()
	am.sessionCount++
	am.mu.Unlock()

	a.emitActivityStatus()

	if a.telemetry != nil {
		a.telemetry.Capture("activity.session.created", map[string]any{
			"session_id":     session.ID,
			"duration_ms":    session.EndTime - session.StartTime,
			"snapshot_count": len(snapshotRows),
			"speech_count":   speechCount,
			"is_meeting":     session.IsMeeting,
			"trigger":        triggerKind,
		})
	}

	// Fold the new events into the persisted timeline runs before notifying,
	// so the frontend's refetch sees the fresh runs.
	a.reconcileTimelineRunsAfterSave()

	// Notify frontend so the activity list can refresh in real time.
	a.emitBrowserEvent("activity:session-saved", session.ID)

	// Save a full transcript for meeting sessions. Run in a goroutine so it
	// doesn't block the activity pipeline. Protected sessions (protected app
	// touched during the call) skip the transcript entirely — it would be
	// AI-cleaned and posted to chat, bypassing the privacy gate.
	if !isMeetingSlice && (audioWriter == nil || session.AudioPath != "") && session.IsMeeting && (session.AudioPath != "" || meetingSpeechCount > 0) && session.TriageStatus != "protected" && meetingdetect.IsTranscriptionSupportedMeetingName(session.MeetingApp) {
		eventsCopy := make([]activitymon.ActivityEvent, len(meetingEvents))
		copy(eventsCopy, meetingEvents)
		go a.saveAutomaticMeetingTranscript(session, eventsCopy, session.MeetingApp, meetingRecordingEpoch, wasMeetingID)
	}

	// The soft memory limit handles normal operation. Snapshot-heavy burst
	// sessions also ask the runtime to return memory once the idle boundary
	// is persisted, which keeps post-stress RSS from staying inflated.
	if shouldTrimActivitySessionMemory(len(snapshotRows), totalSize) {
		go func() {
			time.Sleep(2 * time.Second)
			debug.FreeOSMemory()
		}()
	}

	// Check if this session belongs to a previous day → trigger summary generation.
	go a.onSessionDateRollover(session)
}

func scrubProtectedActivityEvent(evt *activitymon.ActivityEvent) bool {
	if evt == nil {
		return false
	}
	if _, isSensitive := pii.IsSensitiveApp(evt.AppBundleID); !isSensitive {
		return false
	}
	evt.WindowTitle = ""
	evt.ClickLabel = ""
	evt.FocusLabel = ""
	evt.KeyChars = nil
	evt.KeyChar = ""
	evt.TypedText = ""
	evt.TypedTextCaptureMethod = ""
	evt.SpeechText = ""
	return true
}

func shouldTrimActivitySessionMemory(snapshotCount int, totalSizeBytes int64) bool {
	return snapshotCount >= memoryTrimSnapshotThreshold || totalSizeBytes >= memoryTrimSizeBytes
}

// emitActivityStatus sends the current monitoring status to the frontend.
func (a *App) emitActivityStatus() {
	if a.ctx == nil || a.activity == nil {
		return
	}

	am := a.activity
	am.mu.Lock()
	count := am.sessionCount
	am.mu.Unlock()

	var storageMB float64
	if am.store != nil {
		totalBytes, _ := am.store.GetTotalStorageBytes()
		storageMB = float64(totalBytes) / (1024 * 1024)
	}

	a.emitBrowserEvent("activity:status", ActivityMonitorStatus{
		Monitoring:    am.monitor.IsRunning(),
		SnapshotCount: count,
		StorageMB:     storageMB,
	})
}

// ── Wails binding methods ─────────────────────────────────────────────

// GetActivityMonitorStatus returns the current activity monitor state.
func (a *App) GetActivityMonitorStatus() (perfValue ActivityMonitorStatus) {
	defer logPageBinding("GetActivityMonitorStatus", time.Now(), &perfValue)
	if a.activity == nil {
		return ActivityMonitorStatus{}
	}

	am := a.activity
	am.mu.Lock()
	count := am.sessionCount
	am.mu.Unlock()

	var storageMB float64
	if am.store != nil {
		totalBytes, _ := am.store.GetTotalStorageBytes()
		storageMB = float64(totalBytes) / (1024 * 1024)
	}

	return ActivityMonitorStatus{
		Monitoring:    am.monitor.IsRunning(),
		SnapshotCount: count,
		StorageMB:     storageMB,
	}
}

// SetActivityMonitorEnabled enables or disables activity monitoring. Settings >
// Plugins calls this writer through SetBuiltinOn; it remains a Wails binding.
func (a *App) SetActivityMonitorEnabled(enabled bool) error {
	if enabled {
		if a.activity == nil {
			a.initActivityMonitor()
		}
		return a.startActivityMonitor()
	}
	a.stopActivityMonitor()
	return nil
}

// GetActivitySessions returns a paginated list of activity sessions with optional filters.
// Pass startMs=0 / endMs=0 to skip time bounds. Empty keyword means no keyword filter.
// Uses the IncludingProtected variant: this is the user viewing their own
// timeline in the Activity page, so protected sessions stay visible (their
// content fields were already scrubbed/blurred at capture).
func (a *App) GetActivitySessions(limit, offset int, startMs, endMs int64, keyword string) (perfValue []ActivitySessionSummary, perfErr error) {
	defer logPageBinding("GetActivitySessions", time.Now(), &perfValue)
	if a.activity == nil || a.activity.store == nil {
		return nil, nil
	}

	sessions, err := a.activity.store.GetFilteredSessionsIncludingProtected(limit, offset, startMs, endMs, keyword)
	if err != nil {
		return nil, err
	}

	result := make([]ActivitySessionSummary, len(sessions))
	for i, s := range sessions {
		var appNames []string
		json.Unmarshal([]byte(s.AppNames), &appNames)
		result[i] = ActivitySessionSummary{
			ID:            s.ID,
			StartTime:     s.StartTime,
			EndTime:       s.EndTime,
			TriggerKind:   s.TriggerKind,
			AppNames:      appNames,
			EventCount:    s.EventCount,
			SnapshotCount: s.SnapshotCount,
			TriageStatus:  s.TriageStatus,
			OCRStatus:     s.OCRStatus,
		}
	}
	return result, nil
}

// GetActivityTimelineGroups returns the Activity timeline as user-facing app
// runs, read from the persisted activity_timeline_groups table (built
// incrementally at session-save time — see activity_timeline_runs.go). Raw
// activity_sessions remain the capture/storage boundary; run ids are stable
// across queries, so cached summaries and the UI selection survive window,
// limit, and keyword changes.
func (a *App) GetActivityTimelineGroups(limit, offset int, startMs, endMs int64, keyword string) (perfValue []ActivityTimelineGroup, perfErr error) {
	defer logPageBinding("GetActivityTimelineGroups", time.Now(), &perfValue)
	defer func() { a.secondPersonTimeline(perfValue) }()
	if a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	store := a.activity.store

	var runs []ActivityTimelineRunRow
	var err error
	if strings.TrimSpace(keyword) == "" {
		runs, err = store.GetTimelineRuns(limit, offset, startMs, endMs)
	} else {
		runs, err = a.filterTimelineRunsByKeyword(limit, offset, startMs, endMs, keyword)
	}
	if err != nil {
		return nil, err
	}
	return a.assembleTimelineGroups(runs)
}

// GetActivityTimelineGroupsForSessions returns the timeline groups containing
// any of the given sessions, regardless of the sidebar's date window. Used by
// session-anchored entry points (Who you know page person rows, memory/knowledge
// jumps, pipeline-run restrictions) so the anchored run can be merged into
// the list even when it falls outside the current filter range.
func (a *App) GetActivityTimelineGroupsForSessions(sessionIDs []string) ([]ActivityTimelineGroup, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	runs, err := a.activity.store.GetTimelineRunsForSessions(sessionIDs)
	if err != nil {
		return nil, err
	}
	return a.assembleTimelineGroups(runs)
}

// assembleTimelineGroups loads each run's member events and cached summary
// and produces the Wails-facing group payloads, preserving run order.
func (a *App) assembleTimelineGroups(runs []ActivityTimelineRunRow) ([]ActivityTimelineGroup, error) {
	store := a.activity.store
	if len(runs) == 0 {
		return []ActivityTimelineGroup{}, nil
	}

	groups := make([]ActivityTimelineGroup, 0, len(runs))
	groupIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		events, err := store.LoadTimelineRunEvents(run)
		if err != nil {
			return nil, err
		}
		if !timelineRunIsWork(run, events) {
			continue
		}
		group := timelineRunToGroup(run, events)
		if frames := a.activityRunFrames(run); len(frames) > 0 {
			first := frames[0]
			group.FirstFrame = &first
		}
		groups = append(groups, group)
		groupIDs = append(groupIDs, run.ID)
	}

	summaries, err := store.GetTimelineGroupSummaries(groupIDs)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if summary, ok := summaries[groups[i].ID]; ok {
			groups[i].Summary = &summary
			groups[i].IdentityAnchors = displayActivityAnchors(summary)
		}
	}

	return groups, nil
}

// filterTimelineRunsByKeyword returns the runs in range whose member sessions
// match the keyword (same matching surface as the legacy session sidebar:
// app names, event payloads, OCR text), paginating after the filter so
// limit/offset count runs, not sessions.
func (a *App) filterTimelineRunsByKeyword(limit, offset int, startMs, endMs int64, keyword string) ([]ActivityTimelineRunRow, error) {
	store := a.activity.store
	matching, err := store.GetFilteredSessionIDs(startMs, endMs, keyword)
	if err != nil {
		return nil, err
	}
	if len(matching) == 0 {
		return nil, nil
	}
	all, err := store.GetTimelineRunsInRange(startMs, endMs)
	if err != nil {
		return nil, err
	}
	filtered := make([]ActivityTimelineRunRow, 0, len(all))
	for _, run := range all {
		for _, sessionID := range run.SessionIDs {
			if matching[sessionID] {
				filtered = append(filtered, run)
				break
			}
		}
	}
	if offset >= len(filtered) {
		return nil, nil
	}
	filtered = filtered[offset:]
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func timelineEventAppKey(evt activitymon.ActivityEvent) string {
	return timelineAppKey(evt.AppBundleID, evt.AppName)
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueStrings(values []string, next []string) []string {
	for _, value := range next {
		values = appendUniqueString(values, value)
	}
	return values
}

// AXParserDispatch is the parser-side summary of a probe — which parser
// fired (if any) and the headline counts/labels it produced. Returned by
// ProbeAXParser and embedded in the on-disk dump JSON so the dev-tools
// panel and ax-dumps directory share the same shape.
type AXParserDispatch struct {
	MatchedApp     string `json:"matchedApp,omitempty"`
	ContentType    string `json:"contentType,omitempty"`
	MessagesCount  int    `json:"messagesCount"`
	Title          string `json:"title,omitempty"`
	Channel        string `json:"channel,omitempty"`
	ParsedNonEmpty bool   `json:"parsedNonEmpty"`
}

// AXProbeOptions configures a one-shot AX-parser probe initiated from the
// DevTools UI. All fields are optional — the zero value runs against the
// frontmost app's focused window with no delay and no AX-tree payload in
// the response (the on-disk dump always contains the full tree).
type AXProbeOptions struct {
	// BundleID, when non-empty, bypasses NSWorkspace.frontmostApplication
	// and walks the AX tree of the running app with this bundle ID. Use
	// this on multi-monitor setups where Universal Control hijacks the
	// frontmost-app signal.
	BundleID string `json:"bundleId"`
	// WindowIndex selects which top-level window to walk. -1 (the default
	// zero value via JSON omission requires the binding to translate) or
	// 0 with BundleID="" both mean "focused / first window"; >= 0 with
	// BundleID set picks that index from ListAXWindowsByBundle. Ignored
	// when BundleID is empty.
	WindowIndex int `json:"windowIndex"`
	// DelaySeconds lets the operator switch OS focus to the target app
	// before the walk starts. Capped at 60s so this can't be abused as
	// an arbitrary sleep RPC. Ignored when BundleID is set (no frontmost
	// race to win).
	DelaySeconds int `json:"delaySeconds"`
	// IncludeTree controls whether the raw AX-tree JSON and flat AX-text
	// are returned to the caller. Defaults to false to keep IPC payloads
	// small — the on-disk dump always contains them either way.
	IncludeTree bool `json:"includeTree"`
	// IncludeOCR runs an on-demand OCR pass on the current screen
	// alongside the AX walk so the DevTools UI can show exactly what
	// the LLM consumer sees: the AX-parsed body, the OCR text, and the
	// fused output produced by appparser.MergeWithOCR. Best results in
	// frontmost mode (the screen capture is whatever is actually on
	// screen — in bundle mode that may be Yansu, not the target app).
	IncludeOCR bool `json:"includeOCR"`
	// UpscaleOCR doubles the captured JPEG (nearest-neighbor) before
	// handing it to Vision. Apple's docs hint that recognition is best
	// above ~24 px text height, and dark-mode UIs (Asana, Notion,
	// chat apps) frequently render CJK glyphs at 14-16 px — the OCR
	// output for those is catastrophically garbled. Upscaling roughly
	// doubles per-glyph pixel count, sometimes flipping unreadable
	// output into recognisable text. Only meaningful when IncludeOCR
	// is true.
	UpscaleOCR bool `json:"upscaleOCR"`
	// Label is the human-readable tag appended to the dump filename.
	Label string `json:"label"`
}

// AXProbeResult is the structured payload returned to the DevTools UI.
// Mirrors the on-disk dump shape so the same renderer works for both.
type AXProbeResult struct {
	Timestamp   int64                    `json:"timestamp"`
	BundleID    string                   `json:"bundleId"`
	AppName     string                   `json:"appName"`
	WindowTitle string                   `json:"windowTitle"`
	BrowserURL  string                   `json:"browserURL,omitempty"`
	AXTreeBytes int                      `json:"axTreeBytes"`
	AXTextBytes int                      `json:"axTextBytes"`
	AXTree      string                   `json:"axTree,omitempty"`
	AXText      string                   `json:"axText,omitempty"`
	Parsed      *appparser.ParsedContent `json:"parsed,omitempty"`
	Dispatch    AXParserDispatch         `json:"dispatch"`
	// OCRText holds the OCR pass output when IncludeOCR was requested.
	// Empty when OCR wasn't run or the engine returned no observations.
	OCRText string `json:"ocrText,omitempty"`
	// OCRDurationMs reports the OCR call latency in milliseconds so the
	// operator can sanity-check whether the OCR engine actually ran
	// (zero means OCR was skipped or failed).
	OCRDurationMs int64 `json:"ocrDurationMs,omitempty"`
	// OCRError surfaces the OCR failure reason, if any, so the panel
	// can show "AX ran, OCR failed because X" instead of silently
	// dropping the OCR section.
	OCRError string `json:"ocrError,omitempty"`
	// FusedBody is the exact string the activity triager and
	// quick-chat-context would hand to the LLM for this frame. Equals
	// parsed.Body when IsPartialAX is false (AX captured everything),
	// or MergeWithOCR(parsed, ocrText) when IsPartialAX is true (AX
	// incomplete, pipeline splices OCR). Mirrors the activity-pipeline
	// rule so "what the LLM sees" in the panel actually matches.
	FusedBody string `json:"fusedBody,omitempty"`
	// FusedSpliced reports whether OCR was actually spliced into
	// FusedBody by the pipeline rule. False means the pipeline used
	// parsed.Body raw (either because AX was complete or no OCR was
	// available).
	FusedSpliced bool `json:"fusedSpliced"`
	// FusedReason explains the splice decision in one human-readable
	// sentence so the panel can render "OCR spliced because X" / "OCR
	// skipped because Y" without the operator reading parser source.
	FusedReason string `json:"fusedReason,omitempty"`
	// FusedWithOCRPreview is the always-spliced view —
	// MergeWithOCR(parsed, ocrText) regardless of IsPartialAX — so the
	// operator can manually preview the spliced output even on parsers
	// the pipeline wouldn't normally fuse. Empty when the spliced view
	// would equal FusedBody anyway (already spliced) or when no OCR
	// text is available to splice.
	FusedWithOCRPreview string `json:"fusedWithOCRPreview,omitempty"`
	// DumpPath is the absolute path to the on-disk JSON dump. Always
	// populated — the probe writes the dump unconditionally so operators
	// have a reusable artifact for later inspection.
	DumpPath string `json:"dumpPath,omitempty"`
}

// runAXProbe collects the AX tree of the requested target, runs every
// parser in the registry against it, and returns the structured result
// plus the raw JSON/text strings needed for the on-disk dump. Caller is
// responsible for persisting the dump and trimming the response payload
// per AXProbeOptions.IncludeTree.
//
// Returns: (probe result, AX tree JSON, flat AX text, error).
//
// The probe result honours opts.IncludeTree to keep the IPC payload back
// to the frontend small (a 1 MB AX tree blob would otherwise tank the
// Wails JSON-RPC channel). The separate axTreeJSON / axText returns are
// always populated for writeAXProbeDump — the on-disk dump file must
// stay complete regardless of how the caller wants the IPC payload
// trimmed.
func runAXProbe(opts AXProbeOptions) (AXProbeResult, string, string, error) {
	if opts.DelaySeconds < 0 {
		opts.DelaySeconds = 0
	}
	if opts.DelaySeconds > 60 {
		opts.DelaySeconds = 60
	}
	if opts.DelaySeconds > 0 && opts.BundleID == "" {
		time.Sleep(time.Duration(opts.DelaySeconds) * time.Second)
	}

	var (
		info       activitymon.AppInfo
		axTreeJSON string
		axText     string
		axTree     *activitymon.AXNode
		browserURL string
	)
	// Each C tree walker call is a full AX traversal on the target app —
	// 50-200ms for Slack / VSCode-class Electron windows. The struct
	// helpers (CollectFocusedWindowTree / CollectWindowTreeByBundle*)
	// each invoke the C walker themselves, so calling both the JSON and
	// struct variants in sequence doubled probe latency AND added a
	// window during which a focus / resize event could land between the
	// two snapshots. Call the JSON variant once and unmarshal locally
	// for the struct view downstream parsers need.
	unmarshalTree := func(raw string) *activitymon.AXNode {
		if raw == "" {
			return nil
		}
		var node activitymon.AXNode
		if err := json.Unmarshal([]byte(raw), &node); err != nil {
			return nil
		}
		if node.Role == "" {
			return nil
		}
		return &node
	}
	if opts.BundleID != "" {
		info = activitymon.AppInfo{BundleID: opts.BundleID}
		// WindowIndex < 0 = caller wants the focused / first-window
		// fallback (sentinel value sent by the DevTools UI when no row
		// is selected). >= 0 = walk that specific window so picking
		// index 0 walks windows[0] explicitly, not the focused-window
		// heuristic which can land on a popup.
		if opts.WindowIndex >= 0 {
			axTreeJSON = activitymon.CollectWindowTreeByBundleIndexJSON(opts.BundleID, opts.WindowIndex)
		} else {
			axTreeJSON = activitymon.CollectWindowTreeByBundleJSON(opts.BundleID)
		}
		axTree = unmarshalTree(axTreeJSON)
		// FrontmostAppInfo() is skipped in bundle mode, so info.WindowTitle
		// starts empty. The C tree walker roots the dump at the AXWindow
		// node whose Title attribute IS the window title — fill it back in
		// so parsers that key on windowTitle (code_editor, asana, …) work
		// the same in both modes.
		if axTree != nil && axTree.Title != "" {
			info.WindowTitle = axTree.Title
		}
		// CollectFocusedWindowText still goes through frontmost — leave
		// axText empty in bundle mode. Tree-aware parsers don't need
		// the flat-text fallback.
	} else {
		info = activitymon.FrontmostAppInfo()
		browserURL = activitymon.GetBrowserURL(info.BundleID)
		axTreeJSON = activitymon.CollectFocusedWindowTreeJSON()
		axText = activitymon.CollectFocusedWindowText()
		axTree = unmarshalTree(axTreeJSON)
	}

	parsed := appparser.ParseTree(axTree, axText, info.BundleID, info.WindowTitle, browserURL)
	d := AXParserDispatch{ParsedNonEmpty: parsed != nil}
	if parsed != nil {
		d.ContentType = parsed.ContentType
		d.MessagesCount = parserMessageCount(parsed)
		d.Title = parserTitle(parsed)
		d.Channel = parserChannel(parsed)
		if parsed.Metadata != nil {
			d.MatchedApp = parsed.Metadata["app"]
		}
	}

	result := AXProbeResult{
		Timestamp:   time.Now().UnixMilli(),
		BundleID:    info.BundleID,
		AppName:     info.AppName,
		WindowTitle: info.WindowTitle,
		BrowserURL:  browserURL,
		AXTreeBytes: len(axTreeJSON),
		AXTextBytes: len(axText),
		Parsed:      parsed,
		Dispatch:    d,
	}
	if opts.IncludeTree {
		result.AXTree = axTreeJSON
		result.AXText = axText
	}

	// Optional OCR pass so the DevTools panel can show "what the LLM
	// actually sees" — AX-parsed body + OCR text, plus the fused
	// output that MergeWithOCR produces downstream. Run inline (no
	// activity-pipeline semaphore) because this is a single
	// user-triggered call, not a sustained capture stream.
	if opts.IncludeOCR {
		ocrText, ocrDur, ocrErr := captureAndOCR(opts.BundleID == "", opts.UpscaleOCR)
		result.OCRDurationMs = ocrDur.Milliseconds()
		if ocrErr != nil {
			result.OCRError = ocrErr.Error()
		} else {
			result.OCRText = ocrText
		}
	}
	populateFusedFields(&result, parsed, opts.IncludeOCR)
	return result, axTreeJSON, axText, nil
}

// populateFusedFields fills in the four Fused* fields on the probe
// result by replaying the activity-pipeline splice decision: if the
// parser flagged partial AX coverage AND OCR text is available, we
// splice via MergeWithOCR; otherwise we hand back parsed.Body raw.
// We also stash an always-spliced preview when the pipeline declined
// to fuse, so the panel can offer a manual "see what it would look
// like" toggle.
//
// Both FusedBody and FusedWithOCRPreview are prefixed with the same
// per-frame section header the activity triager's
// renderParsedContentSection emits ("### [app] label" + optional
// "URL:" line). Without this prefix the panel's "what the LLM sees"
// claim is misleading: the title gets extracted into a separate field
// but never appears as a visible line in the body, even though the
// real LLM prompt always carries it as a markdown heading.
func populateFusedFields(result *AXProbeResult, parsed *appparser.ParsedContent, ocrRequested bool) {
	ocr := strings.TrimSpace(result.OCRText)
	hasOCR := ocr != ""
	partial := appparser.IsPartialAX(parsed)

	var body string
	switch {
	case parsed == nil && hasOCR:
		// No parser claimed the app, but OCR did pick up text. The
		// triager's no-parser branch still emits OCR-only bodies,
		// so mirror that here.
		body = appparser.MergeWithOCR(nil, ocr)
		result.FusedSpliced = true
		result.FusedReason = "No parser matched — body comes entirely from OCR."
	case parsed == nil:
		result.FusedReason = "No parser matched and no OCR text available."
	case partial && hasOCR:
		body = appparser.MergeWithOCR(parsed, ocr)
		result.FusedSpliced = true
		result.FusedReason = fusedSpliceReason(parsed.Metadata["axCoverage"])
	case partial && ocrRequested:
		body = parsed.Body
		result.FusedReason = "AX flagged partial (" +
			parsed.Metadata["axCoverage"] +
			") so the pipeline would splice OCR — but OCR returned no text this run."
	case partial:
		body = parsed.Body
		result.FusedReason = "AX flagged partial (" +
			parsed.Metadata["axCoverage"] +
			") — the pipeline splices OCR here. Toggle Include OCR to preview the spliced output."
	default:
		body = parsed.Body
		result.FusedReason = "AX captured the full content — the pipeline skips OCR for this frame."
	}
	result.FusedBody = prependFusedHeading(parsed, body)

	// Manual override: always-spliced preview when the pipeline did
	// NOT splice but OCR is available. Lets the operator eyeball the
	// alternative without re-probing.
	if !result.FusedSpliced && hasOCR && parsed != nil {
		result.FusedWithOCRPreview = prependFusedHeading(parsed, appparser.MergeWithOCR(parsed, ocr))
	}
}

// prependFusedHeading prefixes the rendered body with the per-frame
// markdown section the activity triager emits before any frame's
// content. Conversation entries are labeled by channel-first, document
// / web_page / task_list entries by title-first — mirrors the priority
// in renderParsedContentSection so the DevTools panel matches the real
// prompt shape, not just the parser's raw Body field.
//
// Returns body unchanged when parsed is nil (no parser matched, no
// section heading would be emitted).
func prependFusedHeading(parsed *appparser.ParsedContent, body string) string {
	if parsed == nil {
		return body
	}
	app := "unknown"
	if parsed.Metadata != nil {
		if a := parsed.Metadata["app"]; a != "" {
			app = a
		}
	}
	var label string
	switch parsed.ContentType {
	case "conversation":
		label = firstNonEmpty(parsed.Channel, parsed.Title, "(no channel)")
	default:
		label = firstNonEmpty(parsed.Title, parsed.Channel, parsed.URL, "(untitled)")
	}
	heading := fmt.Sprintf("### [%s] %s", app, label)
	// URL line only on non-conversation entries — matches the
	// `if e.url != ""` branch in renderParsedContentSection's default
	// arm.
	if parsed.URL != "" && parsed.ContentType != "conversation" {
		heading += "\nURL: " + parsed.URL
	}
	if body == "" {
		return heading
	}
	return heading + "\n" + body
}

// fusedSpliceReason returns a human sentence explaining why the
// pipeline spliced OCR into the body, keyed off the parser's
// axCoverage tag. Unknown coverages fall back to a generic message so
// new parsers don't need to update this switch in lockstep.
func fusedSpliceReason(coverage string) string {
	switch coverage {
	case appparser.AXCoverageMetadataOnly:
		return "AX captured only metadata (filename / workspace / title) — OCR provides the body."
	case appparser.AXCoverageFirstColumnOnly:
		return "AX captured headers + first column only — OCR fills the remaining cells."
	case appparser.AXCoverageOutlineOnly:
		return "AX captured the slide outline only — OCR fills the canvas content."
	default:
		return "AX flagged partial (" + coverage + ") — pipeline spliced OCR."
	}
}

// captureAndOCR screenshots the main display, optionally crops to the
// focused window (so other on-screen apps don't bleed into the OCR
// output the way they would in a naive full-frame pass), and runs the
// platform OCR engine. Returns the joined recognised text, the
// wall-clock duration, and any engine error.
//
// When cropToFocus is true the helper samples FrontmostWindowBounds
// before AND after the screen capture and only crops if both match —
// same bracketing trick the activity OCR pipeline uses to keep stale
// geometry from misleading the prompt when the user switches focus or
// resizes mid-capture. On mismatch / unavailable bounds it falls back
// to full-frame OCR.
//
// In Bundle mode (cropToFocus=false) we always OCR the full frame: the
// AX walker targets an app that isn't necessarily frontmost, so
// FrontmostWindowBounds would point at Yansu instead of the probed app
// and a confident crop would produce zero useful text. Operators on
// multi-monitor setups should arrange the target window on the main
// display when they want OCR to land — that's the only display
// CaptureRaw reaches.
//
// Lives next to runAXProbe rather than activity_ocr.go because the
// DevTools probe is a manual, one-shot operation — it doesn't share
// the pipeline's PII semaphore, language config, or breadcrumb log.
func captureAndOCR(cropToFocus, upscale bool) (string, time.Duration, error) {
	start := time.Now()
	cap := activitymon.NewScreenCapture()

	// Bracket the capture with bundle+bounds samples so we can reject
	// crops when the focus moved mid-grab.
	var preBundle string
	var preBounds activitymon.WindowBounds
	if cropToFocus {
		preBundle = activitymon.FrontmostAppBundleID()
		preBounds = activitymon.FrontmostWindowBounds()
	}

	pixels, w, h, err := cap.CaptureRaw()
	if err != nil {
		return "", time.Since(start), fmt.Errorf("capture: %w", err)
	}

	var cropBounds activitymon.WindowBounds
	if cropToFocus {
		postBundle := activitymon.FrontmostAppBundleID()
		postBounds := activitymon.FrontmostWindowBounds()
		if preBundle != "" && preBundle == postBundle && windowBoundsClose(preBounds, postBounds) {
			cropBounds = postBounds
		}
	}

	jpegData, err := cap.EncodeJPEG(pixels, w, h, 80)
	if err != nil {
		return "", time.Since(start), fmt.Errorf("encode jpeg: %w", err)
	}

	// Apply optional crop + optional 2x upscale through a single
	// JPEG decode/encode round trip. Chaining the JPEG wrappers
	// (cropJPEGToFocusRect → upscaleJPEG2x) would decode and encode
	// the bytes twice, wasting ~50-100ms per probe on a 4K capture.
	// Vision's sweet spot is text taller than ~24 px; dark-mode CJK
	// UIs frequently render below that and produce gibberish without
	// upscaling. Nearest-neighbor (vs. bilinear / Lanczos) keeps glyph
	// edges sharp — OCR engines prefer that to smoothed-out edges.
	wantCrop := cropBounds.W > 0 && cropBounds.H > 0
	if wantCrop || upscale {
		if img, _, decErr := image.Decode(bytes.NewReader(jpegData)); decErr == nil {
			if wantCrop {
				if cropped, _, ok := cropImageToFocusRect(
					img, cropBounds.X, cropBounds.Y, cropBounds.W, cropBounds.H,
				); ok {
					img = cropped
				}
			}
			if upscale {
				img = upscaleImage2x(img)
			}
			var buf bytes.Buffer
			if encErr := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); encErr == nil && buf.Len() > 0 {
				jpegData = buf.Bytes()
			}
		}
	}

	engine := ocr.New()
	if !engine.IsSupported() {
		return "", time.Since(start), fmt.Errorf("ocr engine not supported on this platform")
	}
	res, err := engine.RecognizeJPEG(jpegData, getOCRLanguagePrefs(engine), ocr.PriorityHigh)
	if err != nil {
		return "", time.Since(start), fmt.Errorf("ocr: %w", err)
	}
	if res == nil {
		return "", time.Since(start), nil
	}
	return res.Text, time.Since(start), nil
}

// windowBoundsClose reports whether two normalized window rects match
// within a small epsilon (~0.1% of screen). Mirrors the boundsClose
// closure used by the activity-pipeline OCR path so probe and pipeline
// agree on what "stable focus" means.
func windowBoundsClose(a, b activitymon.WindowBounds) bool {
	const eps = 0.001
	abs := func(f float64) float64 {
		if f < 0 {
			return -f
		}
		return f
	}
	return abs(a.X-b.X) < eps &&
		abs(a.Y-b.Y) < eps &&
		abs(a.W-b.W) < eps &&
		abs(a.H-b.H) < eps
}

// writeAXProbeDump persists a probe result + raw AX tree to
// ~/.yansu-agent/ax-dumps/<YYYYMMDD-HHMMSS>-<label>.json. Returns the
// absolute path. Label is sanitised to filename-safe chars.
func writeAXProbeDump(result AXProbeResult, axTreeJSON, axText, label string) (string, error) {
	// Embed the AX tree JSON as a raw message so the dump file stays a
	// single well-formed JSON document instead of a string-with-escapes.
	// The C tree walker truncates on budget exhaustion (TREE_MAX_NODES /
	// 1 MB buffer) and can leave invalid JSON — e.g. a string clipped
	// mid-character, or an unclosed object when a write hits the cap.
	// Without validation here, json.MarshalIndent below fails on the
	// rawTree field and the entire RPC returns an error, leaving no
	// dump file at all. Falling back to embedding the raw JSON as a
	// quoted string lets the dump still write so the operator can
	// inspect the partial tree (mirrors cmd/ax-tree-dump's behaviour).
	var rawTree json.RawMessage
	if axTreeJSON != "" {
		if json.Valid([]byte(axTreeJSON)) {
			rawTree = json.RawMessage(axTreeJSON)
		} else {
			quoted, _ := json.Marshal(axTreeJSON)
			rawTree = json.RawMessage(quoted)
		}
	}

	bundle := struct {
		Timestamp   int64                    `json:"timestamp"`
		Label       string                   `json:"label"`
		BundleID    string                   `json:"bundleId"`
		AppName     string                   `json:"appName"`
		WindowTitle string                   `json:"windowTitle"`
		BrowserURL  string                   `json:"browserURL,omitempty"`
		AXTree      json.RawMessage          `json:"axTree,omitempty"`
		AXText      string                   `json:"axText"`
		Parsed      *appparser.ParsedContent `json:"parsed,omitempty"`
		Dispatch    AXParserDispatch         `json:"dispatch"`
	}{
		Timestamp:   result.Timestamp,
		Label:       label,
		BundleID:    result.BundleID,
		AppName:     result.AppName,
		WindowTitle: result.WindowTitle,
		BrowserURL:  result.BrowserURL,
		AXTree:      rawTree,
		AXText:      axText,
		Parsed:      result.Parsed,
		Dispatch:    result.Dispatch,
	}

	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal dump: %w", err)
	}

	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(dataRoot, "ax-dumps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir ax-dumps: %w", err)
	}

	// Sanitize label — only [a-zA-Z0-9._-], collapse anything else into
	// '-'. Cap at 100 runes so the final filename
	// "<YYYYMMDD-HHMMSS>-<label>.json" stays comfortably under the
	// 255-byte filename limit on APFS / HFS+ even when the label
	// contains multi-byte UTF-8 sequences.
	const maxLabelRunes = 100
	cleanLabel := make([]rune, 0, len(label))
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			cleanLabel = append(cleanLabel, r)
		} else {
			cleanLabel = append(cleanLabel, '-')
		}
		if len(cleanLabel) >= maxLabelRunes {
			break
		}
	}
	if len(cleanLabel) == 0 {
		cleanLabel = []rune("dump")
	}

	fname := fmt.Sprintf("%s-%s.json",
		time.Now().Format("20060102-150405"),
		string(cleanLabel))
	path := filepath.Join(dir, fname)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write dump: %w", err)
	}
	return path, nil
}

// ProbeAXParser runs the AX-tree walker + parser dispatcher against the
// requested target (frontmost app or a specific bundle ID, optionally a
// specific window index), returns the parser dispatch result, and writes
// a full dump to ~/.yansu-agent/ax-dumps/ for later inspection. The dump
// path is returned in AXProbeResult.DumpPath.
//
// Wired into Settings → Dev Tools → AX Parser so developers can validate
// new parsers without going through the activity-pipeline delay.
func (a *App) ProbeAXParser(opts AXProbeOptions) (AXProbeResult, error) {
	result, axTreeJSON, axText, err := runAXProbe(opts)
	if err != nil {
		return AXProbeResult{}, err
	}
	label := opts.Label
	if label == "" {
		if opts.BundleID != "" {
			label = "probe-" + opts.BundleID
		} else {
			label = "probe-frontmost"
		}
	}
	path, derr := writeAXProbeDump(result, axTreeJSON, axText, label)
	if derr != nil {
		// Surface the dump-write error in logs but don't fail the RPC —
		// the UI still has the parsed result to render. A missing
		// dump file just means the operator can't re-open it later.
		log.Printf("ProbeAXParser: dump write failed: %v", derr)
	} else {
		result.DumpPath = path
	}
	return result, nil
}

// ListAXParserCatalog exposes the appparser registry's declarative
// metadata to the DevTools UI so the panel can show which apps and URL
// hosts are claimed by which parser.
func (a *App) ListAXParserCatalog() []appparser.ParserCatalogEntry {
	return appparser.Catalog()
}

// ListAXParserUnsupportedApps exposes the static list of apps we
// investigated for AX parsing and decided not to support (e.g. WeChat,
// where Tencent opts the app out of macOS Accessibility). The DevTools
// panel renders this below the supported-parsers catalog so future
// readers don't repeat investigations that already concluded.
func (a *App) ListAXParserUnsupportedApps() []appparser.UnsupportedApp {
	return appparser.UnsupportedAppsList()
}

// ListAXWindowsByBundle returns a JSON-array description of every
// top-level window of the running app with the given bundle ID. Used by
// the DevTools panel to let operators pick a specific window in
// multi-window Electron apps before probing.
//
// Returns the raw C-side JSON (string) so the frontend can render the
// `index`, `role`, `title`, `frame`, and `treeBytes` fields without
// every new field requiring a Wails-binding type update.
func (a *App) ListAXWindowsByBundle(bundleID string) string {
	if bundleID == "" {
		return ""
	}
	return activitymon.ListWindowsByBundleJSON(bundleID)
}

// ActivitySessionExists is a lightweight existence check used by chat UI to
// decide whether a session-id reference should render as a clickable jump.
// Distinct from GetActivitySessionDetail (which loads events / segments /
// snapshots) so we don't pay the full detail cost just to know "does this
// id resolve to anything." Returns:
//   - (true, nil)  — session present
//   - (false, nil) — session definitively not found (sql.ErrNoRows)
//   - (false, err) — transient failure (monitor not initialized, db error);
//     callers should treat this as "unknown" and retry once startup is done
//     rather than caching as missing.
func (a *App) ActivitySessionExists(id string) (bool, error) {
	if a.activity == nil || a.activity.store == nil {
		return false, fmt.Errorf("activity monitor not initialized")
	}
	if _, err := a.activity.store.GetSessionByID(id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetActivitySessionDetail returns full details of a specific session.
// Uses the IncludingProtected variants: this is the user viewing their own
// timeline; protected-app content in these rows was already scrubbed/blurred
// at capture, so what remains is safe to show the user themselves.
func (a *App) GetActivitySessionDetail(id string) (perfValue *ActivitySessionDetail, perfErr error) {
	defer logPageBinding("GetActivitySessionDetail", time.Now(), &perfValue)
	if a.activity == nil || a.activity.store == nil {
		return nil, fmt.Errorf("activity monitor not initialized")
	}

	session, err := a.activity.store.GetSessionByID(id)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}

	events, err := a.activity.store.GetSessionEventsIncludingProtected(id)
	if err != nil {
		return nil, err
	}

	segments, err := a.activity.store.GetSessionSegmentsIncludingProtected(id)
	if err != nil {
		return nil, err
	}

	snapshots, err := a.activity.store.GetSessionSnapshotsIncludingProtected(id)
	if err != nil {
		return nil, err
	}

	var rawAppNames []string
	json.Unmarshal([]byte(session.AppNames), &rawAppNames)
	appNames := make([]string, 0, len(rawAppNames))
	for _, name := range rawAppNames {
		appNames = appendUniqueString(appNames, timelineDisplayAppName(name))
	}

	return &ActivitySessionDetail{
		ActivitySessionSummary: ActivitySessionSummary{
			ID:            session.ID,
			StartTime:     session.StartTime,
			EndTime:       session.EndTime,
			TriggerKind:   session.TriggerKind,
			AppNames:      appNames,
			EventCount:    session.EventCount,
			SnapshotCount: session.SnapshotCount,
			TriageStatus:  session.TriageStatus,
			OCRStatus:     session.OCRStatus,
		},
		Events:    events,
		Segments:  segments,
		Snapshots: snapshots,
	}, nil
}

// GetActivityOCRMatches returns OCR frames that match a keyword for a given session.
func (a *App) GetActivityOCRMatches(sessionID, keyword string) ([]OCRMatchFrame, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, fmt.Errorf("activity monitor not initialized")
	}
	if keyword == "" {
		return nil, nil
	}

	frames, err := a.activity.store.GetOCRMatchFrames(sessionID, keyword)
	if err != nil {
		return nil, err
	}

	result := make([]OCRMatchFrame, len(frames))
	for i, f := range frames {
		result[i] = OCRMatchFrame{
			SegmentID:  f.SegmentID,
			FrameTime:  f.FrameTime,
			Text:       f.Text,
			Confidence: f.Confidence,
		}
	}
	return result, nil
}

// GetSnapshotOCRText returns the OCR text recorded for a single snapshot
// (empty string when nothing was extracted). Used by the activity-detail
// event popup so users can see exactly what the OCR worker produced for the
// frame they clicked, alongside parsed structured content and AX text.
func (a *App) GetSnapshotOCRText(sessionID string, snapshotID int64) (string, error) {
	if a.activity == nil || a.activity.store == nil {
		return "", fmt.Errorf("activity monitor not initialized")
	}
	return a.activity.store.GetOCRTextBySnapshot(sessionID, snapshotID)
}

// OCRHighlightBox represents a text region on a screenshot that matches the search keyword.
type OCRHighlightBox struct {
	X      float64 `json:"x"`      // normalized [0,1] from left
	Y      float64 `json:"y"`      // normalized [0,1] from top
	Width  float64 `json:"width"`  // normalized [0,1]
	Height float64 `json:"height"` // normalized [0,1]
	Text   string  `json:"text"`
}

// GetOCRHighlightBoxes returns bounding boxes for OCR lines matching the
// keyword on a snapshot. Boxes come from the persistent cache (worker-stored
// or written back by a previous call) when available; only frames the OCR
// worker never sampled pay an on-demand OCR, once ever per frame. See
// activity_ocr_boxes.go.
func (a *App) GetOCRHighlightBoxes(snapshotRelPath string, keyword string) ([]OCRHighlightBox, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, fmt.Errorf("activity monitor not initialized")
	}
	if keyword == "" || snapshotRelPath == "" {
		return nil, nil
	}
	boxes, err := a.activity.getOrComputeOCRBoxes(snapshotRelPath)
	if err != nil {
		return nil, err
	}
	return filterOCRBoxesByKeyword(boxes, keyword), nil
}

// SemanticSearchActivitySessions performs semantic search over activity OCR text.
// Returns sessions ranked by embedding similarity to the query.
func (a *App) SemanticSearchActivitySessions(query string, limit int) ([]ActivitySemanticResult, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	if query == "" || limit <= 0 {
		return nil, nil
	}
	return a.activity.store.SemanticSearchSessions(context.Background(), query, limit)
}

// Ensure SaveSnapshots is accessible via the store's db. The store field `db` needs
// to be accessible from extractSession. We add a helper method on ActivityStore.
func (s *ActivityStore) BeginTx() (*sql.Tx, error) {
	return s.db.Begin()
}

// writeMeetingAudioChunk appends accepted meeting audio to the active raw
// meeting recording, when one is open.
func (a *App) writeMeetingAudioChunk(chunk capturedAudioChunk) {
	if !a.automaticMeetingRecordingEpochIsCurrent(chunk.MeetingRecordingEpoch) || a.IsClosedEyesModeEnabled() || a.isBackgroundListeningScreenLocked() {
		return
	}
	am := a.activity
	if am == nil {
		return
	}
	am.mu.Lock()
	if !a.automaticMeetingRecordingEpochIsCurrent(chunk.MeetingRecordingEpoch) {
		am.mu.Unlock()
		return
	}
	var w *meetingAudioWriter
	if chunk.CapturedInMeeting && am.inMeeting && am.meetingRecordingEpoch == chunk.MeetingRecordingEpoch {
		w = am.meetingAudioWriter
	}
	if w == nil && chunk.CapturedMeetingCandidate &&
		am.meetingCandidateRecordingEpoch == chunk.MeetingRecordingEpoch &&
		meetingdetect.IsTranscriptionSupportedMeetingName(chunk.CandidateMeetingAppName) {
		if am.meetingCandidateAudioWriter == nil && am.store != nil {
			candidateWriter, err := newMeetingAudioWriter(am.store.AudioDir())
			if err != nil {
				log.Printf("[meeting-audio] failed to create candidate writer: %v", err)
			} else {
				candidateWriter.SetMeeting(chunk.CandidateMeetingAppName, chunk.MeetingAppOnMicBundleID, "")
				am.meetingCandidateAudioWriter = candidateWriter
			}
		}
		w = am.meetingCandidateAudioWriter
	}
	// Serialize the write with detachment/finalization. Selecting w and then
	// dropping am.mu let a terminal extraction close w before these bytes landed.
	if w != nil {
		w.WriteCapturedChunk(meetingRecordingChunk(chunk))
	}
	am.mu.Unlock()
}

// isKnownMeetingBundleRunning returns true if any known native meeting app
// (Zoom, Teams, FaceTime, etc.) is currently in the running-apps list,
// regardless of whether it has an active-meeting window title.
func (a *App) isKnownMeetingBundleRunning() bool {
	for _, ra := range meetingProbes.runningApps() {
		if meetingdetect.IsKnownMeetingBundle(ra.BundleID) {
			return true
		}
	}
	return false
}

// isMeetingContextRunning checks whether the app that produced the active
// meeting context is still running. This covers browser/Slack calls as well as
// native Zoom/Teams windows.
func (a *App) isMeetingContextRunning(bundleID string) bool {
	if strings.TrimSpace(bundleID) == "" {
		return a.isKnownMeetingBundleRunning()
	}

	for _, ra := range meetingProbes.runningApps() {
		if strings.HasPrefix(ra.BundleID, bundleID) || strings.HasPrefix(bundleID, ra.BundleID) {
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────────────
// Meeting lifecycle watcher
// ──────────────────────────────────────────────────────────────────────────────

// accessibilityWarnOnce tracks which known meeting bundles we've already
// logged an Accessibility-permission warning for, so we don't spam logs on
// every 15s poll when TCC is revoked.
var accessibilityWarnOnce sync.Map // map[string]bool keyed by bundle ID

// detectActiveMeeting identifies an active meeting. Every strategy needs a
// call window or title and the meeting's own app holding the microphone
// (meeting_call_evidence.go), returning the first match:
//  1. The frontmost app showing a call (IsMeetingEvent: Zoom's "Zoom
//     Meeting" window, a Slack huddle; a browser by its tab's address, a
//     Google Meet or Zoom web call page) and that app on the microphone.
//     An idle "Zoom Workplace" in front, or a chat titled "Zoom Meeting
//     prep", is no call.
//  2. A supported native meeting app (Zoom, Teams) holding the microphone
//     (macOS 14+ process audio objects) with a call window.
//  3. Native meeting apps running in the background with a call window
//     (title hints or window count, Settings left out) and on the
//     microphone.
//  4. The browser of a browser call already in progress, while one of its
//     windows still shows the call and it has not let go of the
//     microphone. A browser window in the background never starts a call.
//
// If a known native meeting bundle is running but ListAppWindows returns
// zero windows, we log an Accessibility-permission warning once per bundle
// since that's the most common silent-failure mode.
func (a *App) detectActiveMeeting(now time.Time) meetingdetect.Result {
	if !a.meetingRecordingOn() {
		return meetingdetect.Result{}
	}

	var ongoing struct {
		inMeeting   bool
		appName     string
		bundleID    string
		browserOver bool
	}
	if am := a.activity; am != nil {
		am.mu.Lock()
		ongoing.inMeeting = am.inMeeting
		ongoing.appName = am.meetingAppName
		ongoing.bundleID = am.meetingBundleID
		ongoing.browserOver = am.inMeeting && am.meetingBrowserMicReleased
		am.mu.Unlock()
	}
	mic := meetingProbes.micHolders()

	// 1. Frontmost — cheapest and covers both focused browser meetings and
	//    focused native apps.
	frontmost := meetingProbes.frontmost()
	if res := frontCall(frontmost, mic, now); res.IsMeeting && !(ongoing.browserOver && bundlesMatch(frontmost.BundleID, ongoing.bundleID)) {
		if res.BundleID == "" {
			res.BundleID = frontmost.BundleID
		}
		if res.AppName == "" {
			res.AppName = frontmost.AppName
		}
		return res
	}

	// 2. A supported meeting app holding the microphone with a call window
	//    (meeting_call_evidence.go).
	if res := callOnMicrophone(mic, now); res.IsMeeting {
		if res.AppName == "" {
			res.AppName = res.MeetingName
		}
		return res
	}

	// 3 & 4. Background scan — native meeting apps, and the browser of a
	// browser call in progress.
	browserCall := ongoing.inMeeting && !ongoing.browserOver && meetingdetect.IsBrowserBundle(ongoing.bundleID)
	for _, ra := range meetingProbes.runningApps() {
		isNativeMeeting := meetingdetect.IsKnownMeetingBundle(ra.BundleID)
		isCallBrowser := browserCall && bundlesMatch(ra.BundleID, ongoing.bundleID)
		if !isNativeMeeting && !isCallBrowser {
			continue
		}
		if !mic.couldHold(ra.BundleID) || userListedAppMatches(ra.BundleID) {
			continue
		}

		windows := meetingProbes.appWindows(ra.Name)

		if isNativeMeeting && len(windows) == 0 {
			// Running but zero visible windows — Accessibility may be
			// revoked. Log once per bundle so we don't spam.
			if _, logged := accessibilityWarnOnce.LoadOrStore(ra.BundleID, true); !logged {
				log.Printf("[meeting-watcher] %s is running but returned 0 windows — check Accessibility permission", ra.BundleID)
			}
		}

		if isNativeMeeting {
			if res := meetingCallWindow(ra.BundleID, ra.Name, windows); res.IsMeeting {
				return res
			}
		}

		if isCallBrowser {
			for _, w := range windows {
				// A private window's title never keeps a call going.
				if activitymon.IsPrivateBrowsing(w.Title) {
					continue
				}
				if res := meetingdetect.IsMeetingEvent(ra.BundleID, w.Title); res.IsMeeting && res.MeetingName == ongoing.appName {
					res.BundleID = ra.BundleID
					res.AppName = ra.Name
					return res
				}
			}
		}
	}

	return meetingdetect.Result{}
}

// runPeriodicSessionFlusher fires extractSession("periodic_flush") on a
// wall-clock cadence so the activity timeline stays current during long
// active-but-quiet stretches.
//
// Why this exists: the existing triggers all fire only on a specific
// condition — idle_transition needs ≥30s of no input, speech_idle_transition
// needs ≥10 min of continuous speech, meeting_* needs a meeting boundary.
// When the user is typing/clicking continuously with no speech (e.g. coding,
// writing a doc), none of these fire, so events accumulate in the ring
// buffer and never reach SQLite. The UI then shows nothing for the current
// session until the user finally pauses.
//
// Cost considerations:
//   - We skip the call entirely when the buffer is empty or we're in a
//     meeting (meeting_flush handles long calls; we don't want to tear down
//     meeting state from a periodic tick).
//   - extractSession on this path skips the synchronous session_end screen
//     capture (the user is mid-stream; heartbeat captures every 60s already
//     populate snapshots) — keeps the flush cheap to avoid UI stutter.
//   - The triager's mergeSessions joins adjacent sessions with shared
//     apps within triagerMergeGap (5 min), so a steady flow of
//     5-minute flushes triages as one merged group rather than many small
//     LLM calls.
func (a *App) runPeriodicSessionFlusher(stopCh <-chan struct{}) {
	ticker := time.NewTicker(periodicFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
		}

		am := a.activity
		if am == nil || am.buf == nil {
			continue
		}
		if am.buf.Len() == 0 {
			continue
		}

		am.mu.Lock()
		inMeeting := am.inMeeting
		candidateActive := am.meetingCandidateActive
		am.mu.Unlock()
		if inMeeting || candidateActive {
			continue
		}

		a.extractSession("periodic_flush")
	}
}

// runMeetingLifecycleWatcher polls every meetingPollInterval to detect when
// a meeting starts and ends, using active-meeting window titles as the
// signal. Transitions are debounced (meetingTransitionPolls consecutive
// same-state polls) to absorb title flicker.
//
// The watcher binds to the activityMonitorState snapshot captured at
// startup. If the monitor is toggled off and on, the old watcher's stopCh
// is closed before the new watcher starts, so the old one never operates
// on the new state.
func (a *App) runMeetingLifecycleWatcher(stopCh <-chan struct{}, am *activityMonitorState) {
	if am == nil {
		return
	}

	ticker := time.NewTicker(meetingPollInterval)
	defer ticker.Stop()

	var watcher meetingWatcherState
	stopped := func() bool {
		select {
		case <-stopCh:
			return true
		default:
			return false
		}
	}
	poll := func() { a.pollMeetingLifecycle(am, &watcher, time.Now(), stopped) }

	poll() // run once immediately so we catch meetings that are already active

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			poll()
		}
	}
}

// meetingWatcherState is the debounce the lifecycle watcher keeps between
// polls.
type meetingWatcherState struct {
	consecutiveSame int
	pendingState    bool // what we're transitioning toward
}

// pollMeetingLifecycle is one watcher poll at now. stopped reports whether
// the watcher was told to stop, so a late transition never fires.
func (a *App) pollMeetingLifecycle(am *activityMonitorState, w *meetingWatcherState, now time.Time, stopped func() bool) {
	if !a.meetingRecordingOn() {
		w.consecutiveSame = 0
		return
	}

	// Abandon the poll if the watcher has been signalled to stop;
	// this prevents a late transition from firing on a monitor that's
	// been toggled off.
	if stopped() {
		return
	}

	result := a.detectActiveMeeting(now)
	active := result.IsMeeting

	am.mu.Lock()
	currentlyInMeeting := am.inMeeting
	lastMeetingEvt := am.lastMeetingEventMs
	lastMeetingAudio := am.lastMeetingAudioMs
	meetingBundleID := am.meetingBundleID
	candidateActive := am.meetingCandidateActive
	lastCandidateEvidence := am.lastMeetingCandidateAudioMs
	browserMicReleased := am.meetingBrowserMicReleased
	am.mu.Unlock()

	nowMs := now.UnixMilli()

	// A candidate call whose meeting app has not held the microphone for
	// the sticky span is over.
	if candidateActive && !currentlyInMeeting && lastCandidateEvidence > 0 && nowMs-lastCandidateEvidence >= meetingAudioStickyMs {
		log.Printf("[meeting-watcher] candidate call ended: no microphone evidence for %s", time.Duration(nowMs-lastCandidateEvidence)*time.Millisecond)
		a.extractSession("meeting_candidate_idle_transition")
		w.consecutiveSame = 0
		return
	}

	// Stickiness: when in a meeting and the poll says not-active,
	// absorb the mismatch if recent signals confirm the meeting is
	// still going:
	//   1. Microphone evidence (180s): the meeting's own app still held
	//      the microphone (meeting_call_evidence.go micHeldByMeeting).
	//      System audio alone never counts: music after a call is not
	//      the call, and speech heard from it would keep it alive. A
	//      browser that let go of the microphone ended its call.
	//   2. Event stickiness (60s): the user recently interacted with
	//      the meeting app.
	if currentlyInMeeting && !active {
		sticky := false
		if lastMeetingAudio > 0 && (nowMs-lastMeetingAudio) < meetingAudioStickyMs && !browserMicReleased {
			sticky = true
		}
		if lastMeetingEvt > 0 && (nowMs-lastMeetingEvt) < meetingEventStickyMs {
			sticky = true
		}
		if sticky && a.isMeetingContextRunning(meetingBundleID) {
			w.consecutiveSame = 0
			return
		}
	}

	if active == currentlyInMeeting {
		// Steady state; reset the debounce counter.
		w.consecutiveSame = 0
		return
	}

	// State mismatch — accumulate toward transition.
	if w.pendingState != active {
		w.pendingState = active
		w.consecutiveSame = 1
	} else {
		w.consecutiveSame++
	}

	if w.consecutiveSame < meetingTransitionPolls {
		return
	}

	// Debounce satisfied — commit the transition. Re-check stopCh
	// so we don't race a shutdown into a late transition.
	if stopped() {
		return
	}

	if active {
		a.onMeetingStarted(am, result)
	} else {
		a.onMeetingEnded(am)
	}
	w.consecutiveSame = 0
}

// onMeetingStarted flushes any in-progress non-meeting activity as its own
// session, then marks the monitor as in-meeting so the idle timer is
// suppressed and speech events get tagged with the meeting app.
//
// Race note: there's a small window between extractSession returning and
// acquiring the lock to set inMeeting=true. Speech events arriving in that
// window (typically <100ms) get written with non-meeting AppName/AppBundleID
// at event level. The session-level IsMeeting flag is still forced true by
// extractSession's wasInMeeting path when the meeting ends, so transcripts
// and daily summaries remain correct; only per-event app tagging is slightly
// off for a handful of events at meeting start. Narrowing this further
// requires an atomic drain-and-transition which would need an extractSession
// refactor.
func (a *App) onMeetingStarted(am *activityMonitorState, result meetingdetect.Result) {
	meetingEpoch, enabled := a.automaticMeetingRecordingEpoch()
	if am == nil || !enabled {
		return
	}

	// Guard against double-start: both handleActivityEvent and the watcher
	// can call this concurrently. Without this check, both would call
	// extractSession("pre_meeting_flush") and split the buffer.
	am.mu.Lock()
	if am.inMeeting {
		am.mu.Unlock()
		return
	}
	promoteCandidate := am.meetingCandidateActive &&
		am.meetingCandidateRecordingEpoch == meetingEpoch &&
		am.meetingCandidateAppName == result.MeetingName
	am.mu.Unlock()

	// Flush any pre-meeting activity if the buffer has events. Runs outside
	// the lock since extractSession takes its own locks. extractSession
	// early-returns when the buffer is empty so this is safe to call
	// unconditionally, but we check Len() to avoid the lock churn.
	if !promoteCandidate && am.buf != nil && am.buf.Len() > 0 {
		a.extractSession("pre_meeting_flush")
	}

	// Preference changes and the final lifecycle transition share this fence.
	// A disable that ran during the pre-meeting flush invalidates this epoch;
	// a disable that follows waits, then clears and aborts everything below.
	a.meetingRecordingMu.Lock()
	defer a.meetingRecordingMu.Unlock()
	if !a.automaticMeetingRecordingEpochIsCurrent(meetingEpoch) {
		return
	}

	am.mu.Lock()
	if am.inMeeting {
		am.mu.Unlock()
		return
	}
	promoteCandidate = am.meetingCandidateActive &&
		am.meetingCandidateRecordingEpoch == meetingEpoch &&
		am.meetingCandidateAppName == result.MeetingName
	am.inMeeting = true
	am.meetingAppName = result.MeetingName
	am.meetingBundleID = result.BundleID
	am.meetingStartMs = time.Now().UnixMilli()
	am.meetingRecordingEpoch = meetingEpoch
	// A short watcher interruption may be the same call. A matching process
	// alone never joins calls hours apart into one Library/memory record.
	// A known privacy interruption retains its existing same-process identity.
	currentPID := pidForMeetingBundle(result.BundleID)
	canResume := false
	if am.lastMeetingID != "" && am.lastMeetingApp == result.MeetingName && am.lastMeetingEndMs > 0 {
		since := am.meetingStartMs - am.lastMeetingEndMs
		sameProcess := currentPID != 0 && currentPID == am.lastMeetingPID
		canResume = since >= 0 && ((since <= meetingResumeWindowMs &&
			(currentPID == 0 || am.lastMeetingPID == 0 || sameProcess)) ||
			(am.lastMeetingPaused && sameProcess))
	}
	if canResume {
		am.meetingID = am.lastMeetingID
	} else {
		am.meetingID = uuid.New().String()
	}
	// Always record the current PID (or 0 if unavailable) so the next
	// MEETING_STARTED can compare against it via the PID-primary path.
	am.lastMeetingPID = currentPID
	am.lastMeetingPaused = false
	if promoteCandidate && am.meetingCandidateStartMs > 0 && am.meetingCandidateStartMs < am.meetingStartMs {
		am.meetingStartMs = am.meetingCandidateStartMs
	}
	am.lastMeetingAudioMs = 0
	am.meetingBrowserMicReleased = false
	am.meetingBrowserGapSinceMs = 0
	am.meetingBrowserObservedMs = 0
	// The meeting had ended, so a recording still here belongs to an ended
	// call (it is detached at every meeting end); never append this call to it.
	staleRecording := detachCallRecordingLocked(am)
	if promoteCandidate {
		am.meetingAudioWriter = am.meetingCandidateAudioWriter
		am.meetingCandidateAudioWriter = nil
		if am.meetingAudioWriter != nil {
			am.meetingAudioWriter.SetMeeting(result.MeetingName, result.BundleID, am.meetingID)
		}
	} else if am.meetingCandidateAudioWriter != nil {
		am.meetingCandidateAudioWriter.Abort()
		am.meetingCandidateAudioWriter = nil
	}
	am.meetingCandidateActive = false
	am.meetingCandidateAppName = ""
	am.meetingCandidateStartMs = 0
	am.meetingCandidateRecordingEpoch = 0
	am.lastMeetingCandidateAudioMs = 0
	// Cancel any armed idle timer — the watcher owns meeting boundaries.
	if am.idleTimer != nil {
		am.idleTimer.Stop()
		am.idleTimer = nil
	}
	// Start audio recording for this meeting.
	if meetingdetect.IsTranscriptionSupportedMeetingName(result.MeetingName) && am.meetingAudioWriter == nil && am.store != nil {
		w, err := newMeetingAudioWriter(am.store.AudioDir())
		if err != nil {
			log.Printf("[meeting-audio] failed to create writer: %v", err)
		} else {
			w.SetMeeting(result.MeetingName, result.BundleID, am.meetingID)
			am.meetingAudioWriter = w
		}
	}
	am.mu.Unlock()
	if staleRecording != nil {
		log.Printf("[meeting-audio] saving a recording left from an ended call")
		a.persistEndedCallRecordingLocked(staleRecording, meetingEpoch, "meeting_ended")
	}

	log.Printf("[meeting-watcher] MEETING_STARTED: %s (%s)", result.MeetingName, result.BundleID)
	// Match the calendar event behind this meeting off the monitor lock.
	go a.linkMeetingToCalendarEvent(result, time.Now())
}

// onMeetingEnded extracts the meeting session (flushes all buffered events
// as one transcript) and clears the meeting state.
func (a *App) onMeetingEnded(am *activityMonitorState) {
	if am == nil {
		return
	}

	am.mu.Lock()
	if !am.inMeeting {
		am.mu.Unlock()
		return
	}
	appName := am.meetingAppName
	am.mu.Unlock()

	log.Printf("[meeting-watcher] MEETING_ENDED: %s", appName)

	// Extract the session — this flushes all buffered events as one transcript.
	// extractSession itself clears am.inMeeting and related state, and
	// early-returns on empty buffer so it's safe even when racing with
	// stopActivityMonitor's shutdown_in_meeting flush.
	a.extractSession("meeting_ended")
}

// flushMeetingStateOnEmptyExtract clears the active-meeting state and
// stashes the just-ended meeting's id into lastMeetingID, even when the
// buffer was empty and we're skipping the normal extract path. Without
// this, two 30-second Zoom blips in quick succession would each mint a
// fresh meeting_id (because lastMeetingID is still ""), and the
// resume-window logic in onMeetingStarted never gets a chance to fuse
// them. Only does the bookkeeping for terminal meeting triggers
// (meeting_ended / shutdown_in_meeting): periodic_flush never reaches
// here with an empty buffer in a way that matters.
func (a *App) flushMeetingStateOnEmptyExtract(am *activityMonitorState, isPeriodicFlush bool, triggerKind string) {
	if isPeriodicFlush || triggerKind == meetingSliceTrigger {
		return
	}
	audioLocked := a.isBackgroundListeningScreenLocked()
	var candidateAudioWriter, endedRecording *meetingAudioWriter
	var endedEpoch uint64
	am.mu.Lock()
	if am.meetingCandidateActive {
		candidateAudioWriter = am.meetingCandidateAudioWriter
		am.meetingCandidateAudioWriter = nil
		am.meetingCandidateActive = false
		am.meetingCandidateAppName = ""
		am.meetingCandidateStartMs = 0
		am.meetingCandidateRecordingEpoch = 0
		am.lastMeetingCandidateAudioMs = 0
	}
	if am.inMeeting {
		endedEpoch = am.meetingRecordingEpoch
		endedRecording = detachCallRecordingLocked(am)
		if am.meetingID != "" {
			am.lastMeetingID = am.meetingID
			am.lastMeetingApp = am.meetingAppName
			am.lastMeetingEndMs = time.Now().UnixMilli()
			am.lastMeetingPaused = audioLocked
		}
		am.inMeeting = false
		am.meetingAppName = ""
		am.meetingBundleID = ""
		am.meetingStartMs = 0
		am.meetingRecordingEpoch = 0
		am.meetingID = ""
		am.lastMeetingEventMs = 0
		am.lastMeetingAudioMs = 0
		am.meetingBrowserMicReleased = false
		am.meetingBrowserGapSinceMs = 0
		am.meetingBrowserObservedMs = 0
	}
	am.mu.Unlock()

	if candidateAudioWriter != nil {
		candidateAudioWriter.Abort()
	}
	a.persistEndedCallRecording(endedRecording, endedEpoch, triggerKind)
}

// pidForMeetingBundle returns the PID of the currently-running meeting
// app (Zoom, Teams, etc.) identified by bundle ID. Returns 0 when the
// app isn't found, the platform doesn't implement ListRunningApps
// (currently every non-macOS platform — Windows/Linux are stubs), or
// the meeting is a browser tab with no native meeting process. Used by
// onMeetingStarted as a tie-breaker — when both sides have a PID and
// they differ, the user definitely quit + relaunched, so it's a new
// meeting regardless of timing.
func pidForMeetingBundle(bundleID string) int {
	if bundleID == "" {
		return 0
	}
	for _, ra := range meetingProbes.runningApps() {
		if ra.BundleID == bundleID {
			return ra.PID
		}
	}
	return 0
}
