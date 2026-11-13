package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"wails-gui/internal/meetingdetect"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/activitymon/appparser"
	"wails-gui/storage"

	"yansu-agent/agent"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	triagerIdleTrigger  = 2 * time.Minute
	triagerMinSnapshots = 3
	triagerMinApps      = 2
	triagerMinEvents    = 20
	triagerMinDuration  = 30 * time.Second
	// High-activity exemption thresholds for the apps prefilter. A session
	// pinned to a single app (cmux/IDE/terminal focused work) normally fails
	// the >=2 apps gate even when it carries rich signal. When BOTH event
	// count AND duration clearly exceed the baseline minimums, treat the
	// session as a deep-focus session and let it through to AI triage.
	// Conservative AND-of-both bar: a single high number alone (e.g. lots of
	// events from idle key-repeat or a long mostly-idle window) does not
	// trigger the exemption.
	triagerHighActivityEvents   = 60              // 3x triagerMinEvents
	triagerHighActivityDuration = 2 * time.Minute // 4x triagerMinDuration
	triagerMergeGap             = 5 * time.Minute // max gap between sessions to merge
	// triagerTriageTimeout bounds a single AI triage call. Successful
	// calls observed in production run ~50–110s; setting the cap well under
	// the 5-minute ticker interval ensures a stuck agent peer cannot hold runMu
	// across multiple tick periods and silently stall the whole pipeline.
	triagerTriageTimeout = 3 * time.Minute
)

const (
	// maxTriagerGoroutines limits concurrent AI goroutines (memory gen,
	// knowledge gen) spawned by the triager. Without a cap,
	// slow AI calls accumulate goroutines that hold session data in memory
	// indefinitely, contributing to multi-GB leaks over long-running sessions.
	maxTriagerGoroutines = 3
)

// ActivityTriager evaluates activity sessions and triages them,
// generating memory and knowledge for sessions deemed worth keeping.
type ActivityTriager struct {
	app    *App
	mc     *MemoryGenerator
	mu     sync.Mutex
	cancel context.CancelFunc
	aiSem  chan struct{} // bounds concurrent AI goroutines
	// shadowExecutionSem serializes mirrored candidate-plan runs so production
	// activity cannot fan out into an unbounded set of agent executions.
	shadowExecutionSem chan struct{}
	// taskShadowSem serializes production onset/lifecycle observation passes.
	// Task execution has its own semaphore because it may outlive detection.
	taskShadowSem chan struct{}
	// taskShadowObserverAdmissionMu makes exact-input lookup plus observer-root
	// creation one admission decision. The app is single-instance; this closes
	// the remaining in-process race before taskShadowSem serializes execution.
	taskShadowObserverAdmissionMu sync.Mutex
	// wg tracks every fire-and-forget goroutine dispatched by evaluate() so
	// Stop() can wait for them to exit. Without this, Stop() returns while
	// in-flight AI goroutines keep running past shutdown and log noise like
	// "operating on stopped context".
	wg sync.WaitGroup
	// runMu is held for the entire duration of an evaluate() invocation and
	// is acquired via TryLock so that a manual trigger (RunActivityPipelineNow)
	// cannot collide with the hourly batch and process the same pending
	// sessions twice.
	runMu sync.Mutex
	// semanticMu serializes the independent semantic-event pass (its own
	// goroutine/ticker), so it never shares evaluate()'s runMu and a stuck or
	// slow main triage cannot starve it.
	semanticMu sync.Mutex
	// timelineSummaryMu serializes the independent timeline-run summary pass
	// (its own goroutine/ticker), with the same decoupling rationale as
	// semanticMu.
	timelineSummaryMu sync.Mutex
	// timelineSummaryKick wakes the summary worker as soon as a session save
	// changes the run table, so closed runs queue immediately instead of
	// waiting for the fallback ticker. Buffered(1): one pending kick covers
	// any number of coalesced changes.
	timelineSummaryKick chan struct{}
	// activityComposerMu serializes the independent activity-composer pass
	// (layer 3) — its own goroutine/ticker, decoupled like the others so a slow
	// compose pass cannot starve the run-summary or triage layers.
	activityComposerMu sync.Mutex
	// activityComposerCleaned gates the one-time purge of trivial activities
	// composed before the glance filter existed. Guarded by activityComposerMu.
	activityComposerCleaned bool
	// triageCooldown rate-limits retries on groups whose previous triage
	// failed. Without this guard, the 5-minute ticker keeps re-running the
	// same ~60K-token prompt on the same failing group every cycle. See
	// activity_triage_cooldown.go for the backoff schedule.
	triageCooldown triageCooldownState
	// semanticCooldown does the same for the semantic-event pass, which has
	// its own status track and its own 2-minute ticker: without it a
	// persistently failing group re-burns its extraction prompt every tick.
	semanticCooldown triageCooldownState
	// visionCache dedupes VLM screenshot reads by exact image hash so an
	// unchanged window re-captured across sessions is described once. See
	// activity_event_vision.go.
	visionCache visionResultCache
	// lastBatch is the start of the latest hourly batch (activity_hourly_batch.go);
	// batchScheduled marks that the first batch after startup was scheduled.
	lastBatch      time.Time
	batchScheduled bool
}

// activityTriageResponse is the AI triage result for an activity session.
type activityTriageResponse struct {
	Title          string `json:"title"`
	Description    string `json:"description"`
	WorthMemory    bool   `json:"worthMemory"`
	WorthKnowledge bool   `json:"worthKnowledge"`
	// Initiative analysis fields describe what the user is likely to do next,
	// whether or not that action is suitable for automation.
	WorthInitiative      bool    `json:"worthInitiative"`
	InitiativeTitle      string  `json:"initiativeTitle,omitempty"`
	InitiativeGoal       string  `json:"initiativeGoal,omitempty"`
	PredictedUserAction  string  `json:"predictedUserAction,omitempty"`
	InitiativeRationale  string  `json:"initiativeRationale,omitempty"`
	InitiativeConfidence float64 `json:"initiativeConfidence,omitempty"`
	// Shadow prediction fields preserve independently measurable dimensions.
	// InitiativeRationale explains Yansu's evidence-based prediction; reasoning
	// hypotheses below describe possible user reasoning and may be empty when
	// behavior does not reveal why the user acted.
	ShadowCapabilityKey       string                      `json:"shadowCapabilityKey,omitempty"`
	ShadowExpectedResult      string                      `json:"shadowExpectedResult,omitempty"`
	ShadowExpectedTiming      string                      `json:"shadowExpectedTiming,omitempty"`
	ShadowReasoningHypotheses []ShadowReasoningHypothesis `json:"shadowReasoningHypotheses,omitempty"`
	ShadowGoalConfidence      float64                     `json:"shadowGoalConfidence,omitempty"`
	ShadowResultConfidence    float64                     `json:"shadowResultConfidence,omitempty"`
	ShadowTimingConfidence    float64                     `json:"shadowTimingConfidence,omitempty"`
	// Legacy outcome fields remain for stored-response compatibility. New
	// prediction prompts never request them; the independent ShadowRun
	// comparator owns all production outcome matching.
	MatchedInitiativeID        string  `json:"matchedInitiativeId,omitempty"`
	ObservedUserAction         string  `json:"observedUserAction,omitempty"`
	ObservedResult             string  `json:"observedResult,omitempty"`
	PredictionAssessment       string  `json:"predictionAssessment,omitempty"`
	PredictionAssessmentReason string  `json:"predictionAssessmentReason,omitempty"`
	PredictionMatchConfidence  float64 `json:"predictionMatchConfidence,omitempty"`
	// Automation analysis fields — populated when worthAutomation=true
	WorthAutomation            bool            `json:"worthAutomation"`
	AutomationName             string          `json:"automationName,omitempty"`
	AutomationDescription      string          `json:"automationDescription,omitempty"`
	AutomationPrompt           string          `json:"automationPrompt,omitempty"`
	AutomationScheduleKind     string          `json:"automationScheduleKind,omitempty"`
	AutomationScheduleConfig   json.RawMessage `json:"automationScheduleConfig,omitempty"`
	AutomationScheduleEvidence string          `json:"automationScheduleEvidence,omitempty"`
	AutomationRationale        string          `json:"automationRationale,omitempty"`
	AutomationConfidence       float64         `json:"automationConfidence,omitempty"`
	// Handoff analysis fields — populated when worthHandoff=true. Handoff has no
	// schedule (always at-kind firing on accept), so no schedule fields needed.
	WorthHandoff       bool   `json:"worthHandoff"`
	HandoffName        string `json:"handoffName,omitempty"`
	HandoffDescription string `json:"handoffDescription,omitempty"`
	HandoffPrompt      string `json:"handoffPrompt,omitempty"`
	// SOP analysis fields — populated when worthSOP=true. SOPs accrete
	// evidence across days, so like knowledge they carry create-or-update
	// routing (sopAction/sopUpdateId) against the existing-SOP dedup list.
	WorthSOP       bool   `json:"worthSOP"`
	SOPAction      string `json:"sopAction,omitempty"`   // "create" | "update"
	SOPUpdateID    string `json:"sopUpdateId,omitempty"` // existing bundle slug when sopAction=update
	SOPName        string `json:"sopName,omitempty"`
	SOPDescription string `json:"sopDescription,omitempty"`
	SOPTrigger     string `json:"sopTrigger,omitempty"`
	SOPOutline     string `json:"sopOutline,omitempty"` // 3-8 line observed step sketch that seeds the author
}

func (r *activityTriageResponse) GetAutomationName() string         { return r.AutomationName }
func (r *activityTriageResponse) GetAutomationDescription() string  { return r.AutomationDescription }
func (r *activityTriageResponse) GetAutomationPrompt() string       { return r.AutomationPrompt }
func (r *activityTriageResponse) GetAutomationScheduleKind() string { return r.AutomationScheduleKind }
func (r *activityTriageResponse) GetAutomationScheduleConfig() json.RawMessage {
	return r.AutomationScheduleConfig
}
func (r *activityTriageResponse) GetAutomationScheduleEvidence() string {
	return r.AutomationScheduleEvidence
}
func (r *activityTriageResponse) GetAutomationRationale() string   { return r.AutomationRationale }
func (r *activityTriageResponse) GetAutomationConfidence() float64 { return r.AutomationConfidence }
func (r *activityTriageResponse) GetPredictedUserAction() string   { return r.PredictedUserAction }
func (r *activityTriageResponse) GetInitiativeGoal() string        { return r.InitiativeGoal }
func (r *activityTriageResponse) GetInitiativeRationale() string   { return r.InitiativeRationale }
func (r *activityTriageResponse) GetInitiativeConfidence() float64 { return r.InitiativeConfidence }

// knowledgeGenerationResponse is the AI-generated knowledge from an activity session.
type knowledgeGenerationResponse struct {
	Kind            string   `json:"kind"`
	Action          string   `json:"action"`             // "create", "update", or "skip"
	UpdateID        string   `json:"updateId,omitempty"` // filename (without .md) of existing entry to update
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	KnowledgePoints []string `json:"knowledgePoints"`
	provenance      knowledgeGenerationProvenance
}

// sessionGroup is a bounded extraction batch of adjacent capture sessions.
// It is not a task identity: a task may span several groups or share a group
// with unrelated work. The activity composer consumes these boundaries only
// as optional hints and routes runs using their observed evidence.
type sessionGroup struct {
	Sessions []ActivitySessionRow
	// entrySeed, when set, keys the group's memory and knowledge entries in
	// place of its first session: a meeting's notes learn into entries of
	// their own, beside what triage learned from the same call's capture.
	entrySeed string
	// onOutcome, when set, hears every memory and knowledge generation
	// outcome, including those of a pass without a pipeline run.
	onOutcome func(status, reason string)
}

// triageSourceExcludedReason is the outcome reason when a privacy recheck
// stops generation.
const triageSourceExcludedReason = "source excluded by current privacy settings"

func (g *sessionGroup) primaryID() string { return g.Sessions[0].ID }
func (g *sessionGroup) startTime() int64  { return g.Sessions[0].StartTime }
func (g *sessionGroup) endTime() int64    { return g.Sessions[len(g.Sessions)-1].EndTime }
func (g *sessionGroup) totalEventCount() int {
	n := 0
	for _, s := range g.Sessions {
		n += s.EventCount
	}
	return n
}

// insightSource tags the insight:saved event. What a call's notes teach
// arrives silently, like the notes themselves.
func (g *sessionGroup) insightSource() string {
	if g.entrySeed != "" {
		return meetingNotesInsightSource
	}
	return "activity"
}

// entryKey is what the group's memory and knowledge entry ids derive from.
func (g *sessionGroup) entryKey(session ActivitySessionRow) string {
	if g.entrySeed != "" {
		return g.entrySeed
	}
	return session.ID
}

// writeActivitySessionsBlock writes all sessions in a group as a nested
// frontmatter list (flat format). Still used by the knowledge write path —
// knowledge entries dedup by merging session ID sets, not by preserving
// per-update trajectory, so they stay flat.
//
// Output format (indentation shows nesting; gofmt would flatten a
// dash-bulleted example so we use plain indented text):
//
//	**Activity Session**:
//	    <session-id> — <HH:mm>, <app1>, <app2>
//	    <session-id> — <HH:mm>, <app1>
//
// Memory entries MUST use writeGroupedActivitySessionBlock instead — every
// update is preserved under its own `**Update ...**` header so users can trace
// which group triggered which change.
func writeActivitySessionsBlock(b *strings.Builder, group sessionGroup) {
	b.WriteString("- **Activity Session**:\n")
	writeFlatSessionBullets(b, "  ", group.Sessions)
}

// writeFlatSessionBullets writes the session rows under an Activity Session
// block (either flat or under a group header). `indent` controls the leading
// whitespace so callers can embed the rows at different nesting depths.
func writeFlatSessionBullets(b *strings.Builder, indent string, sessions []ActivitySessionRow) {
	for _, s := range sessions {
		var apps []string
		_ = json.Unmarshal([]byte(s.AppNames), &apps)
		cleaned := make([]string, 0, len(apps))
		for _, name := range apps {
			name = strings.TrimSpace(name)
			if name != "" {
				cleaned = append(cleaned, name)
			}
		}
		ts := time.UnixMilli(s.StartTime).Format("15:04")
		if len(cleaned) > 0 {
			fmt.Fprintf(b, "%s- %s — %s, %s\n", indent, s.ID, ts, strings.Join(cleaned, ", "))
		} else {
			fmt.Fprintf(b, "%s- %s — %s\n", indent, s.ID, ts)
		}
	}
}

// mergeFlatActivitySessionBlock returns an updated Activity Session block for
// knowledge entries: existing session rows are preserved verbatim, and rows
// for sessions in `group` that are not already listed are appended at the
// bottom. Knowledge entries represent facts (not trajectory), so we dedup to
// a flat list rather than grouping by update.
//
// Handles three legacy input shapes:
//   - No block present: emits a fresh header + new group rows
//   - Single-line legacy form (`- **Activity Session**: <id>`): upgrades to
//     nested form with an ID-only row (time/apps were never stored)
//   - Current nested flat form: preserves all rows verbatim and appends new
func mergeFlatActivitySessionBlock(existing string, group sessionGroup) string {
	fm := parseMemoryFrontmatter(existing)
	seen := make(map[string]struct{}, len(fm.ActivitySessionIDs)+len(group.Sessions))
	for _, id := range fm.ActivitySessionIDs {
		seen[id] = struct{}{}
	}

	existingBlock := extractActivitySessionBlock(existing)

	var b strings.Builder
	if existingBlock != "" {
		b.WriteString(existingBlock)
		if !strings.HasSuffix(existingBlock, "\n") {
			b.WriteString("\n")
		}
	} else {
		// No nested block (either truly missing or legacy single-line). Emit a
		// fresh header; if the parser recovered any IDs from legacy form,
		// stub them in as ID-only rows so they survive the next merge.
		b.WriteString("- **Activity Session**:\n")
		for _, id := range fm.ActivitySessionIDs {
			fmt.Fprintf(&b, "  - %s\n", id)
		}
	}

	newRows := make([]ActivitySessionRow, 0, len(group.Sessions))
	for _, s := range group.Sessions {
		if _, ok := seen[s.ID]; ok {
			continue
		}
		seen[s.ID] = struct{}{}
		newRows = append(newRows, s)
	}
	writeFlatSessionBullets(&b, "  ", newRows)
	return b.String()
}

func sessionIDsFromGroup(group sessionGroup) []string {
	ids := make([]string, 0, len(group.Sessions))
	for _, s := range group.Sessions {
		if strings.TrimSpace(s.ID) != "" {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

func mergeKnowledgeSessionIDs(existing string, group sessionGroup) []string {
	seen := map[string]struct{}{}
	var out []string
	if doc, ok := parseOKFDocument(existing); ok {
		for _, id := range okfStringSlice(doc.Frontmatter, "activity_sessions") {
			if _, exists := seen[id]; !exists {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	} else {
		fm := parseMemoryFrontmatter(existing)
		for _, id := range fm.ActivitySessionIDs {
			if _, exists := seen[id]; !exists {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	for _, id := range sessionIDsFromGroup(group) {
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// writeGroupedActivitySessionBlock emits a grouped Activity Session block for
// memory entries. Each update appears under its own `**Update <ts>** (<kind>)`
// header so trajectory is preserved across dedup updates.
//
// existingBlock, when non-empty, MUST already be in grouped format (callers
// should pre-upgrade legacy flat blocks via upgradeFlatActivitySessionBlock).
// It is copied verbatim, then a new group with the supplied sessions is
// appended below. updateKind should be one of "initial", "update", or
// "pre-migration".
//
// Output format (indentation shows nesting; plain text, no bullets, so
// gofmt doesn't flatten the hierarchy):
//
//	**Activity Session**:
//	    **Update 2026-04-20 11:30** (initial)
//	        <session-id> — <HH:mm>, <app>
//	    **Update 2026-04-20 14:15** (update)
//	        <session-id> — <HH:mm>, <app>
func writeGroupedActivitySessionBlock(b *strings.Builder, existingBlock string, group sessionGroup, updateKind string, updateTime time.Time) {
	if existingBlock == "" {
		b.WriteString("- **Activity Session**:\n")
	} else {
		b.WriteString(existingBlock)
		if !strings.HasSuffix(existingBlock, "\n") {
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(b, "  - **Update %s** (%s)\n", updateTime.Format("2006-01-02 15:04"), updateKind)
	writeFlatSessionBullets(b, "    ", group.Sessions)
}

func (g *sessionGroup) allAppNames() []string {
	seen := map[string]struct{}{}
	var result []string
	for _, s := range g.Sessions {
		var names []string
		json.Unmarshal([]byte(s.AppNames), &names)
		for _, name := range names {
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				result = append(result, name)
			}
		}
	}
	return result
}

func (g *sessionGroup) isSpeechOnly() bool {
	if len(g.Sessions) == 0 {
		return false
	}
	for _, s := range g.Sessions {
		if s.TriggerKind != "speech_idle_transition" && s.TriggerKind != "meeting_flush" {
			return false
		}
	}
	return true
}

func (g *sessionGroup) hasMeetingSessions() bool {
	for _, s := range g.Sessions {
		if s.IsMeeting {
			return true
		}
	}
	return false
}

// The media playback bundle ID list now lives in media_playback_apps.go
// (mediaPlaybackBundleIDs / isMediaPlaybackApp) so it can be shared with the
// OS-level CATap exclusion path in background_audio_system_darwin.go. Single
// source of truth, same conservative match policy.

// browserMediaPlaybackTitleHints are window-title substrings that strongly
// indicate the browser tab is hosting media playback. Matched only when
// the frontmost bundle is a known browser — outside browsers these
// strings are too generic. Kept conservative: each entry is a site name
// that almost always appears in the window title precisely because the
// page's <title> tag includes it.
//
// False-positive risk: a Notion / Twitter / search page that mentions
// one of these names in its title (e.g. "YouTube notes 2026" in Notion
// opened in a browser tab). The cost is real user speech inside such a
// tab being downgraded to "ambient" — uncomfortable but not catastrophic,
// and the LLM prompt explicitly instructs the model to re-evaluate
// ambient labels using surrounding context. The asymmetric cost is
// worth it because browser video is the single largest playback channel
// we previously had no signal for.
var browserMediaPlaybackTitleHints = []string{
	"YouTube",
	"哔哩哔哩", "Bilibili", "bilibili.com",
	"TikTok", "tiktok.com",
	"抖音", "douyin.com",
	"Netflix",
	"Twitch", "twitch.tv",
	"腾讯视频", "v.qq.com",
	"爱奇艺", "iqiyi.com",
	"优酷", "Youku",
	"Vimeo",
	"Disney+", "disneyplus.com",
	"SoundCloud",
	"网易云音乐",
	"QQ音乐",
}

// isLikelyNonUserSpeech reports whether a speech event is overwhelmingly
// likely to be audio played through the Mac's speakers (videos, music,
// remote meeting audio) rather than the user actually speaking. The
// triage LLM should treat such events as ambient context, not as the
// user's words/work.
//
// Four independent signals trigger the flag:
//  1. SpeechSource == "system_audio" — by definition the remote/output
//     side of the audio path.
//  2. SpeechLikelyEcho — captured concurrently with audible speaker
//     output detected by the system_audio tap.
//  3. AppBundleID matches a known media-playback app, OR the focus is
//     微信 with a video/playback window title.
//  4. The frontmost app is a browser and the window title matches a
//     known media-playback site (YouTube, Bilibili, etc.). Covers the
//     large class of web-based video where signals 1-3 don't fire.
//
// Signals 3-4 are the only branches that catch Bug A on machines where
// the system_audio tap can't see playback at all (e.g. macOS 26 Process
// Tap privacy restrictions return silent PCM).
func isLikelyNonUserSpeech(evt activitymon.ActivityEvent) bool {
	if evt.SpeechSource == "system_audio" {
		return true
	}
	if evt.SpeechLikelyEcho {
		return true
	}
	// Strongest mic-side signal: voice fingerprint says this mic chunk
	// wasn't the enrolled Self. Catches the "co-located coworker speaks
	// into my mic" case that none of signals 1-3 cover. nil = "didn't
	// run" (no enrollment / no model / pre-feature data) and is not a
	// flag — only explicit false = "ran, didn't match" trips it.
	if evt.SpeechSpeakerVerified != nil && !*evt.SpeechSpeakerVerified {
		return true
	}
	// The frontmost-app heuristic only applies to mic-captured speech.
	// Without this guard, any future source type would silently trigger
	// the playback bundle/window matches, and the heuristic would shadow
	// legitimate non-mic signals. An empty source field is allowed
	// through for backward compat with events persisted before
	// SpeechSource was added.
	if evt.SpeechSource != "" && evt.SpeechSource != "microphone" {
		return false
	}
	if isMediaPlaybackApp(evt.AppBundleID) {
		return true
	}
	if evt.AppBundleID == "com.tencent.xinWeChat" {
		// "图片和视频" is the WeChat full-screen image/video viewer.
		// "视频通话" is the in-app video call window — note that this is
		// a meeting context, so this branch only fires when
		// hasMeetingSessions() returned false at the call site (i.e. the
		// session pipeline didn't classify the call as a meeting), which
		// means treating it as ambient is the safer default.
		if strings.Contains(evt.WindowTitle, "图片和视频") ||
			strings.Contains(evt.WindowTitle, "视频通话") {
			return true
		}
	}
	if meetingdetect.IsBrowserBundle(evt.AppBundleID) {
		for _, hint := range browserMediaPlaybackTitleHints {
			if strings.Contains(evt.WindowTitle, hint) {
				return true
			}
		}
	}
	return false
}

func formatActivityAppNames(appNames []string) string {
	if len(appNames) == 0 {
		return "Background audio"
	}
	return strings.Join(appNames, ", ")
}

// activityPrivateLogField keeps source-derived text out of operational logs.
// Apply it only at the logging boundary: canonical evidence, user-visible
// artifacts, and returned errors must retain their original content.
func activityPrivateLogField(name, value string) string {
	field := activityLogFieldMetadata(name, value)
	if os.Getenv(backgroundTranscriptDebugEnv) != "1" {
		return field
	}
	const maxDebugBytes = 256
	debugValue := truncateAtRuneBoundary(value, maxDebugBytes)
	if len(debugValue) < len(value) {
		debugValue += "...(truncated)"
	}
	return field + fmt.Sprintf(" %s=%q", name, debugValue)
}

// activityLogFieldMetadata is the disk-safe form: debug never adds source text.
func activityLogFieldMetadata(name, value string) string {
	field := fmt.Sprintf("%s_bytes=%d", name, len(value))
	if name == "error" {
		field = "error_kind=" + activityLogErrorKind(value) + " " + field
	}
	return field
}

// Error strings can embed prompts and paths. Emit only a fixed diagnostic
// category outside debug mode; never copy a matched substring into the log.
func activityLogErrorKind(value string) string {
	value = strings.ToLower(value)
	for _, category := range []struct {
		kind    string
		markers []string
	}{
		{"parse", []string{"parse ", "malformed", "unexpected end of json", "invalid character", "cannot unmarshal"}},
		{"locked", []string{"database is locked", "database table is locked", "sqlite_busy", "sqlite_locked"}},
		{"timeout", []string{"deadline exceeded", "timed out", "timeout"}},
		{"canceled", []string{"context canceled", "context cancelled"}},
		{"permission", []string{"permission denied", "operation not permitted", "access denied"}},
		{"not_found", []string{"no such file", "file does not exist", "not found"}},
		{"io", []string{"input/output", "i/o error", "broken pipe", "unexpected eof"}},
		{"unavailable", []string{"unavailable", "connection refused", "connection reset"}},
		{"invalid", []string{"invalid"}},
	} {
		if strings.Contains(value, "error_kind="+category.kind) {
			return category.kind
		}
		for _, marker := range category.markers {
			if strings.Contains(value, marker) {
				return category.kind
			}
		}
	}
	return "unknown"
}

func logSpeechMemoryTriage(group sessionGroup, sessionID, title string) {
	if !group.isSpeechOnly() {
		return
	}
	log.Printf("[background-audio] memory triage accepted: session=%s %s", sessionID, activityPrivateLogField("title", title))
}

func logSpeechMemorySaved(group sessionGroup, sessionID, action, title, filePath string, pointCount int) {
	if !group.isSpeechOnly() {
		return
	}
	log.Printf(
		"[background-audio] memory %s: session=%s %s points=%d %s",
		action,
		sessionID,
		activityPrivateLogField("title", title),
		pointCount,
		activityPrivateLogField("file", filePath),
	)
}

// NewActivityTriager creates a new triager instance.
func NewActivityTriager(app *App, mc *MemoryGenerator) *ActivityTriager {
	return &ActivityTriager{
		app:                 app,
		mc:                  mc,
		aiSem:               make(chan struct{}, maxTriagerGoroutines),
		shadowExecutionSem:  make(chan struct{}, 1),
		taskShadowSem:       make(chan struct{}, 1),
		timelineSummaryKick: make(chan struct{}, 1),
	}
}

// Run starts the triager loop. Blocks until ctx is cancelled.
func (c *ActivityTriager) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()

	// Drain any sessions stuck in the transient "triaging" state from a prior
	// run. These leak when the process is killed mid-triage (e.g. agent peer
	// hang followed by shutdown) and would otherwise be invisible to the
	// pending-sessions query forever.
	if c.app != nil && c.app.activity != nil && c.app.activity.store != nil {
		if n, err := c.app.activity.store.ResetTriagingToPending(); err != nil {
			log.Printf("[triager] failed to reset stuck triaging sessions: %s", activityPrivateLogField("error", err.Error()))
		} else if n > 0 {
			log.Printf("[triager] reset %d stuck triaging sessions back to pending", n)
		}
		// Same leak recovery for timeline runs left in 'generating' by a
		// process kill mid-summary.
		if n, err := c.app.activity.store.ResetGeneratingTimelineRuns(); err != nil {
			log.Printf("[triager] failed to reset stuck generating timeline runs: %s", activityPrivateLogField("error", err.Error()))
		} else if n > 0 {
			log.Printf("[triager] reset %d stuck generating timeline runs", n)
		}
	}

	// Semantic event extraction runs on its OWN goroutine + ticker + lock, fully
	// decoupled from triage evaluate(), so a slow/timing-out main triage can never
	// starve it. Start it BEFORE the synchronous startup evaluate(): that first
	// evaluate can take minutes (large triage prompts), and the loop must not wait
	// on it to begin — otherwise the startup path itself starves the very loop this
	// design promises never to starve.
	go c.runSemanticLoop(ctx)

	// Timeline-run summaries follow the same decoupled-loop design.
	go c.runTimelineSummaryLoop(ctx)

	// The activity composer (layer 3) groups summarized runs into activities on
	// its own decoupled loop, consuming run summaries the timeline loop produces.
	go c.runActivityComposerLoop(ctx)
	go c.runThreadOutcomeLoop(ctx)
	go c.runActivityThreadLoop(ctx)

	// Triage and learning run as one hourly batch over everything captured
	// since the last batch (activity_hourly_batch.go).
	c.runHourlyBatches(ctx)
}

// Stop cancels the triager loop and waits for any in-flight
// memory/knowledge goroutines to return before unblocking shutdown.
func (c *ActivityTriager) Stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.mu.Unlock()
	c.wg.Wait()
}

// errGeneratorBusy is returned by evaluate when another invocation is
// already running. Manual triggers surface this to the caller so the UI can
// show "already running" instead of a misleading success toast.
var errGeneratorBusy = fmt.Errorf("activity triager already running")

// evaluate checks pending activity sessions and runs the triage pipeline.
// TryLock-guards against re-entry so the hourly batch and a manual trigger
// cannot process the same pending sessions concurrently. Returns
// errGeneratorBusy if another invocation held the lock.
func (c *ActivityTriager) evaluate(ctx context.Context) error {
	if c.app.activity == nil || c.app.activity.store == nil {
		return nil
	}

	// Build the tick trace record and register the deferred saver BEFORE
	// acquiring runMu. This ensures busy-skipped ticks (where another
	// evaluate is already running) still write a trace row with Error set,
	// so the Settings UI surfaces "pipeline stuck" instead of going silent.
	tickStart := time.Now()
	tickRun := ActivityPipelineRunRow{
		Kind:      "tick",
		StartedAt: tickStart.UnixMilli(),
	}
	defer func() {
		tickRun.CompletedAt = time.Now().UnixMilli()
		// Skip saving "empty" ticks (no pending sessions, no error). 5-minute
		// ticks would otherwise flood the 200-entry retention cap in ~17 hours
		// and evict valuable group runs long before the 7-day window.
		if tickRun.PendingCount == 0 && tickRun.Error == "" {
			return
		}
		c.recordPipelineRun(&tickRun)
	}()

	if !c.runMu.TryLock() {
		log.Printf("[triager] evaluate already in progress; skipping this invocation")
		tickRun.Error = "previous evaluate still running (busy-skip)"
		return errGeneratorBusy
	}
	defer c.runMu.Unlock()

	shadowEnabled := c.app.loadTriageFeatureFlags(ctx).Shadow
	if shadowEnabled {
		recovery := c.recoverTaskShadowContinuations(ctx, "triager_tick")
		if len(recovery.Errors) > 0 {
			log.Printf(
				"[shadow-task-continuation] tick completed with errors: %s",
				activityPrivateLogField("error", strings.Join(recovery.Errors, "; ")),
			)
		}
	}

	sessions, err := c.app.activity.store.GetPendingSessions()
	if err != nil {
		log.Printf("[triager] failed to get pending sessions: %s", activityPrivateLogField("error", err.Error()))
		tickRun.Error = activityLogFieldMetadata("error", err.Error())
		return fmt.Errorf("failed to get pending sessions: %w", err)
	}

	tickRun.PendingCount = len(sessions)
	if len(sessions) == 0 {
		return nil
	}

	// Coding agents at work are agent activity: no model reads them and they
	// never become memories about the user.
	sessions, agentSessions := c.splitAgentActivity(sessions)
	c.recordAgentActivity(agentSessions)
	tickRun.FilteredCount += len(agentSessions)
	if len(sessions) == 0 {
		return nil
	}

	// Step 1: Merge adjacent sessions that likely form a single workflow (no AI cost).
	// Sessions are already sorted by start_time ASC from the query.
	groups := mergeSessions(sessions)
	tickRun.GroupCount = len(groups)

	// Step 2: Heuristic pre-filter on merged groups (no AI cost)
	var candidates []sessionGroup
	for _, g := range groups {
		if reason := c.shouldEvaluateGroupReason(g); reason != "" {
			dismissRun := newGroupPipelineRun(g, time.Now())
			dismissRun.PrefilterPass = boolPtr(false)
			dismissRun.PrefilterReason = reason
			dismissRun.TriageStatus = "dismissed"
			dismissRun.TriageReason = reason
			dismissRun.ShadowObserverAdmission = TaskShadowObserverAdmissionPrefiltered
			c.recordPipelineRun(&dismissRun)
			receipt := c.dispatchTaskShadowContinuationFlow(
				ctx,
				g,
				c.activityEventsForGroup(g),
				TaskShadowObserverAdmissionPrefiltered,
				reason,
				dismissRun.ID,
			)
			applyTaskShadowObserverDispatchReceipt(
				&dismissRun,
				TaskShadowObserverAdmissionPrefiltered,
				receipt,
			)
			tickRun.FilteredCount++
			logTriage("activity", g.primaryID(), "dismiss", reason)
			for _, s := range g.Sessions {
				c.app.activity.store.UpdateTriageStatus(s.ID, "dismissed")
			}
			dismissRun.CompletedAt = time.Now().UnixMilli()
			c.recordPipelineRun(&dismissRun)
		} else {
			candidates = append(candidates, g)
		}
	}
	tickRun.CandidateCount = len(candidates)

	if len(candidates) == 0 {
		return nil
	}

	// Step 3: one AI triage for the whole batch. Every candidate group since
	// the last batch joins one group, so an hour of activity makes one triage
	// call and at most one memory and one knowledge call. Skip the batch if
	// its previous triage failed within the cooldown window — re-running the
	// same prompt burns ~60K input tokens for a result we already know fails.
	candidates = batchCandidateGroups(candidates)
	now := time.Now()
	for _, g := range candidates {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if rem := c.triageCooldown.remaining(g, now); rem > 0 {
			reason := fmt.Sprintf(
				"legacy activity triage cooldown: %s remaining",
				rem.Round(time.Second),
			)
			cooldownRun := newGroupPipelineRun(g, time.Now())
			cooldownRun.PrefilterPass = boolPtr(true)
			cooldownRun.TriageStatus = "cooldown"
			cooldownRun.TriageReason = reason
			cooldownRun.ShadowObserverAdmission = TaskShadowObserverAdmissionCooldown
			c.recordPipelineRun(&cooldownRun)
			receipt := c.dispatchTaskShadowContinuationFlow(
				ctx,
				g,
				c.activityEventsForGroup(g),
				TaskShadowObserverAdmissionCooldown,
				reason,
				cooldownRun.ID,
			)
			applyTaskShadowObserverDispatchReceipt(
				&cooldownRun,
				TaskShadowObserverAdmissionCooldown,
				receipt,
			)
			cooldownRun.CompletedAt = time.Now().UnixMilli()
			c.recordPipelineRun(&cooldownRun)
			log.Printf("[triager] skipping group %s: triage cooldown %s remaining", g.primaryID(), rem.Round(time.Second))
			tickRun.FilteredCount++
			continue
		}

		c.triageGroup(ctx, g)
	}
	return nil
}

func (c *ActivityTriager) activityEventsForGroup(
	group sessionGroup,
) []activitymon.ActivityEvent {
	if c == nil || c.app == nil || c.app.activity == nil ||
		c.app.activity.store == nil {
		return nil
	}
	result := make([]activitymon.ActivityEvent, 0)
	for _, session := range group.Sessions {
		events, err := c.app.activity.store.GetSessionEvents(session.ID)
		if err != nil {
			log.Printf(
				"[triager] failed to get events for session %s: %s",
				session.ID,
				activityPrivateLogField("error", err.Error()),
			)
			continue
		}
		result = append(result, events...)
	}
	return result
}

// mergeSessions groups adjacent sessions whose gap is within triagerMergeGap
// and share at least one common app. Sessions MUST be sorted by start_time ASC.
// Pure function: shared by the triager pipeline, daily-summary Moments view,
// and optional composer hints. These extraction boundaries must never split
// or join an activity; capture flushes and idle gaps carry no task identity.
func mergeSessions(sessions []ActivitySessionRow) []sessionGroup {
	if len(sessions) == 0 {
		return nil
	}

	groups := []sessionGroup{{Sessions: []ActivitySessionRow{sessions[0]}}}

	for i := 1; i < len(sessions); i++ {
		cur := sessions[i]
		last := &groups[len(groups)-1]
		prev := last.Sessions[len(last.Sessions)-1]

		gap := time.Duration(cur.StartTime-prev.EndTime) * time.Millisecond
		if gap <= triagerMergeGap && hasAppOverlap(prev, cur) {
			last.Sessions = append(last.Sessions, cur)
		} else {
			groups = append(groups, sessionGroup{Sessions: []ActivitySessionRow{cur}})
		}
	}

	return groups
}

// hasAppOverlap returns true if two sessions share at least one app.
func hasAppOverlap(a, b ActivitySessionRow) bool {
	var namesA, namesB []string
	json.Unmarshal([]byte(a.AppNames), &namesA)
	json.Unmarshal([]byte(b.AppNames), &namesB)

	set := map[string]struct{}{}
	for _, n := range namesA {
		set[n] = struct{}{}
	}
	for _, n := range namesB {
		if _, ok := set[n]; ok {
			return true
		}
	}
	return false
}

// shouldEvaluateGroupReason applies heuristic pre-filtering on a session group (no AI cost).
// Returns an empty string if the group passes; otherwise a human-readable rejection reason.
func (c *ActivityTriager) shouldEvaluateGroupReason(group sessionGroup) string {
	// Meeting sessions always qualify — they're high-value regardless
	// of event count or app diversity.
	if group.hasMeetingSessions() {
		return ""
	}

	if cnt := group.totalEventCount(); cnt < triagerMinEvents {
		return fmt.Sprintf("too few events: %d < %d", cnt, triagerMinEvents)
	}

	duration := time.Duration(group.endTime()-group.startTime()) * time.Millisecond
	if duration < triagerMinDuration {
		return fmt.Sprintf("too short duration: %s < %s", duration, triagerMinDuration)
	}

	if n := len(group.allAppNames()); n < triagerMinApps && !group.isSpeechOnly() {
		// High-activity exemption: a single-app session that clearly
		// represents focused deep work (cmux/IDE/terminal) carries enough
		// signal to be worth AI triage even without app diversity. Require
		// BOTH event count AND duration to clearly exceed baselines, so
		// long-but-idle or busy-but-momentary sessions still get rejected.
		eventCount := group.totalEventCount()
		duration := time.Duration(group.endTime()-group.startTime()) * time.Millisecond
		if eventCount >= triagerHighActivityEvents && duration >= triagerHighActivityDuration {
			// Pass through to AI triage.
		} else {
			return fmt.Sprintf("too few apps: %d < %d (events=%d duration=%s, below high-activity exemption %d / %s)",
				n, triagerMinApps,
				eventCount, duration,
				triagerHighActivityEvents, triagerHighActivityDuration)
		}
	}

	return ""
}

// triageGroup runs AI triage on a session group and generates memory/knowledge if worthy.
func (c *ActivityTriager) triageGroup(ctx context.Context, group sessionGroup) {
	// Recheck historical sources before changing their status or dispatching
	// observers. An empty summary is not a privacy gate for those earlier reads.
	if !c.triageGroupAllowsModelRead(group) {
		return
	}
	// Build the observability run record. Populated as we go and persisted
	// (best-effort) on every return path via defer.
	run := newGroupPipelineRun(group, time.Now())
	run.PrefilterPass = boolPtr(true)
	run.ShadowObserverAdmission = TaskShadowObserverAdmissionEligible

	// Deferred cleanup: unconditionally sweep this group's sessions for any
	// rows still in the transient "triaging" state and push them back to
	// "pending". This is a scoped recovery — it only touches IDs from this
	// group and only rows whose status is still "triaging", so successfully
	// terminal sessions (dismissed / pending-after-error) are
	// untouched. Covers panics AND silently-failed UpdateTriageStatus writes
	// on the terminal paths below.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[triager] panic during triage for group %s: %s", group.primaryID(), activityPrivateLogField("panic", fmt.Sprint(r)))
			run.Error = activityLogFieldMetadata("error", fmt.Sprintf("panic: %v", r))
		}
		ids := make([]string, len(group.Sessions))
		for i, s := range group.Sessions {
			ids[i] = s.ID
		}
		if n, err := c.app.activity.store.ResetTriagingSessionsByIDs(ids); err != nil {
			log.Printf("[triager] cleanup sweep failed for group %s: %s", group.primaryID(), activityPrivateLogField("error", err.Error()))
		} else if n > 0 {
			log.Printf("[triager] cleanup sweep recovered %d stranded session(s) for group %s", n, group.primaryID())
		}
		run.CompletedAt = time.Now().UnixMilli()
		c.recordPipelineRun(&run)
	}()

	// Persist the exact production source identity before observer dispatch.
	// The row remains pending if the process stops before the immutable root is
	// recorded, which lets the read-only coverage gate diagnose the interruption.
	c.recordPipelineRun(&run)

	// Mark all sessions in the group as triaging
	for _, s := range group.Sessions {
		if err := c.app.activity.store.UpdateTriageStatus(s.ID, "triaging"); err != nil {
			log.Printf("[triager] failed to mark session %s as triaging: %s", s.ID, activityPrivateLogField("error", err.Error()))
		}
	}

	// Shadow receives retained production activity before the legacy router.
	// Its lifecycle and execution are independent of triage model success.
	allEvents := c.activityEventsForGroup(group)
	receipt := c.dispatchTaskShadowProductionFlow(
		ctx,
		group,
		allEvents,
		run.ID,
	)
	applyTaskShadowObserverDispatchReceipt(
		&run,
		TaskShadowObserverAdmissionEligible,
		receipt,
	)
	c.recordPipelineRun(&run)

	profile := ProfileGroup(group, allEvents)
	log.Printf("[triager] subject profile for group %s: dominant=%s performed=%d received=%d observed=%d ambient=%d unknown=%d total=%d",
		group.primaryID(), profile.Dominant,
		profile.PerformedCount, profile.ReceivedCount, profile.ObservedCount,
		profile.AmbientCount, profile.UnknownCount, profile.Total)

	// Build activity summary for AI triage. selectedFrames captures the
	// exact parsed snapshots + OCR samples that survived the per-group
	// caps so the Settings UI can show operators which frames drove this
	// triage decision.
	summary, selectedFrames, enrichedProfile := c.buildGroupSummary(group, allEvents, profile)
	profile = enrichedProfile
	if cs := profile.ChatSenders; cs.Total() > 0 {
		log.Printf("[triager] chat sender stats for group %s: self=%d contact=%d anonymous=%d unknown=%d (heavily_non_self=%v)",
			group.primaryID(), cs.SelfMsgCount, cs.ContactMsgCount, cs.AnonymousMsgCount, cs.UnknownMsgCount, cs.ChatHeavilyNonSelf())
	}
	run.AISummary = summary
	run.SelectedFrames = selectedFrames
	if summary == "" || !c.triageGroupAllowsModelRead(group) {
		run.TriageReason = "source excluded by current privacy settings"
		return
	}

	// Run AI triage with a hard timeout. Without this, a hung agent peer can
	// block the call for >1 hour, holding runMu and silently freezing every
	// subsequent 5-minute tick. Scale timeout for large sessions.
	triageTimeout := triagerTriageTimeout
	totalEvents := group.totalEventCount()
	if totalEvents > 500 {
		extra := time.Duration((totalEvents-500)/500+1) * time.Minute
		triageTimeout += extra
		if triageTimeout > 5*time.Minute {
			triageTimeout = 5 * time.Minute
		}
	}
	triageCtx, cancelTriage := context.WithTimeout(ctx, triageTimeout)
	// All sessions are screen-captured "activity" now that IM extractor injection
	// is gone. Drives per-source guidance, Triage Log source, and handoff throttle.
	groupSource := "activity"
	// Per-source user guidance, appended to the triage prompt to steer
	// attention/routing for this kind of session.
	customInstruction := c.app.triageInstructionForSource(groupSource)
	triageResult, rawResponse, flags, prompt, err := c.runActivityTriage(triageCtx, summary, customInstruction)
	cancelTriage()
	run.AIResponseRaw = rawResponse
	// Capture triage prompt diagnostics on every path — the deferred
	// recordPipelineRun persists the row regardless of which branch we take,
	// and the Settings UI uses these fields to surface flag state + prompt
	// size + full prompt text in the Activity Pipeline detail panel.
	run.TriageFlags = encodeTriageFlags(flags)
	run.TriagePrompt = prompt
	run.TriagePromptBytes = len(prompt)
	if err != nil {
		log.Printf("[triager] triage failed for group %s: %s", group.primaryID(), activityPrivateLogField("error", err.Error()))
		run.Error = activityLogFieldMetadata("error", err.Error())
		// Permanent vs transient split. Permanent errors (Anthropic
		// invalid_request_error — safety filter, schema, oversized
		// prompt) will be rejected on every retry with the same prompt,
		// so feeding them into the cooldown + retry loop just burns
		// quota. Observed incident: a single group hit the safety
		// filter 12 times across 10 hours before this fix. Mark those
		// groups terminal (dismissed) with a reason that's separable
		// from AI-rejected and settings-disabled, and skip markFailed.
		//
		// Transient errors (timeouts, 429, 5xx, network) keep the
		// existing pending + markFailed behaviour so the rotator's
		// exponential backoff can give them a chance to recover.
		if isPermanentTriageError(err) {
			run.TriageStatus = "dismissed"
			run.TriageReason = "permanent: rejected by Anthropic (invalid_request_error)"
			// Track whether any session's terminal-state write failed.
			// On the happy path (all dismiss writes succeed) we deliberately
			// skip markFailed because dismissed is itself terminal — the
			// rotator never picks the session again, so a cooldown entry
			// is dead weight. On the SAD path (DB lock / disk full / IO
			// error on the SET triage_status = 'dismissed' update) the
			// session stays in "triaging" and the deferred sweep at
			// line 706 reverts it to "pending". Without a cooldown entry
			// the next ticker pass (5 min later) would burn another
			// ExecuteTask on the same permanently-failing prompt. Enroll
			// in the cooldown only in that case so DB failure inherits
			// the same 30min → 24h backoff used for transient errors.
			anyDBFailure := false
			for _, s := range group.Sessions {
				if err := c.app.activity.store.UpdateTriageStatus(s.ID, "dismissed"); err != nil {
					log.Printf("[triager] failed to mark session %s dismissed after permanent triage error: %s", s.ID, activityPrivateLogField("error", err.Error()))
					anyDBFailure = true
				}
			}
			if anyDBFailure {
				c.triageCooldown.markFailed(group, time.Now())
			}
			return
		}
		// Transient: cooldown + retry. Record the failure so the next
		// ticker pass skips this group instead of re-running the same
		// expensive prompt every 5 minutes. Without this, observed
		// retries hit 110+ on a single group inside 6.5h.
		c.triageCooldown.markFailed(group, time.Now())
		for _, s := range group.Sessions {
			if err := c.app.activity.store.UpdateTriageStatus(s.ID, "pending"); err != nil {
				log.Printf("[triager] failed to reset session %s to pending after triage error: %s", s.ID, activityPrivateLogField("error", err.Error()))
			}
		}
		return
	}

	// Triage succeeded — drop any prior cooldown so a future transient
	// failure does not inherit escalated backoff from earlier hiccups.
	c.triageCooldown.clear(group)

	// All triage outputs are disabled in Settings. The LLM was not
	// called at all (runActivityTriage short-circuited), so Worth* must
	// stay nil (= "not evaluated", per the activity_pipeline_runs.go
	// contract) — never boolPtr(false), which means "AI evaluated and
	// said no". Mark sessions terminal so the rotator does not keep
	// retrying, with a reason that distinguishes settings opt-out from
	// real AI rejection.
	if !flags.anyEnabled() {
		run.TriageStatus = "dismissed"
		run.TriageReason = "all triage features disabled by settings"
		for _, s := range group.Sessions {
			if err := c.app.activity.store.UpdateTriageStatus(s.ID, "dismissed"); err != nil {
				log.Printf("[triager] failed to mark session %s as dismissed (settings disabled): %s", s.ID, activityPrivateLogField("error", err.Error()))
			}
		}
		logTriageEntry(TriageLogEntry{
			Source:   groupSource,
			ID:       group.primaryID(),
			Decision: "skipped",
			Reason:   "all triage features disabled by settings",
		})
		return
	}

	learningSummary := summary

	// Log the AI triage decision with structured worth fields.
	if triageResult.WorthMemory {
		logSpeechMemoryTriage(group, group.primaryID(), triageResult.Title)
	}

	// Disabled axes get nil — they were stripped from the prompt and the
	// model was never asked. boolPtr(false) would falsely claim "AI said no".
	run.WorthMemory = boolPtrIf(flags.Memory, triageResult.WorthMemory)
	run.WorthKnowledge = boolPtrIf(flags.Knowledge, triageResult.WorthKnowledge)
	run.WorthAutomation = boolPtrIf(flags.Automation, triageResult.WorthAutomation)
	run.WorthHandoff = boolPtrIf(flags.Handoff, triageResult.WorthHandoff)

	logTriageEntry(TriageLogEntry{
		Source:          groupSource,
		ID:              group.primaryID(),
		Decision:        "triage",
		Reason:          activityPrivateLogField("title", triageResult.Title) + " " + activityPrivateLogField("description", triageResult.Description),
		Title:           triageResult.Title,
		Description:     triageResult.Description,
		WorthMemory:     run.WorthMemory,
		WorthKnowledge:  run.WorthKnowledge,
		WorthAutomation: run.WorthAutomation,
		WorthHandoff:    run.WorthHandoff,
	})

	// Trigger memory+knowledge generation under evaluationMu to serialize
	// corpus writes with scan-triggered and conversation-triggered pipelines.
	if triageResult.WorthMemory || triageResult.WorthKnowledge {
		select {
		case c.aiSem <- struct{}{}:
			// "dispatched" = handed off to async worker. We INSERT the run row
			// right now (before launching the goroutine) so the worker has a
			// stable run.ID it can UPDATE with the final outcome
			// (created/updated/skipped/error). Without this pre-dispatch
			// insert, the row only gets persisted by the deferred
			// recordPipelineRun above, leaving memory_status frozen at
			// "dispatched" forever.
			if triageResult.WorthMemory {
				run.MemoryStatus = "dispatched"
			}
			if triageResult.WorthKnowledge {
				run.KnowledgeStatus = "dispatched"
			}
			c.recordPipelineRun(&run)
			runID := run.ID
			c.wg.Add(1)
			go func() {
				defer c.wg.Done()
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[triager] panic in memory/knowledge gen for %s: %s", group.primaryID(), activityPrivateLogField("panic", fmt.Sprint(r)))
					}
					<-c.aiSem
				}()
				if c.mc != nil {
					c.mc.evaluationMu.Lock()
					defer c.mc.evaluationMu.Unlock()
				}
				if triageResult.WorthMemory {
					c.generateAndSaveMemory(ctx, runID, group, learningSummary, profile)
				}
				if triageResult.WorthKnowledge {
					c.generateAndSaveKnowledge(ctx, runID, group, learningSummary, profile)
				}
			}()
		default:
			log.Printf("[triager] skipping memory/knowledge gen for %s: AI goroutine limit reached", group.primaryID())
			if triageResult.WorthMemory {
				run.MemoryStatus = "skipped"
				run.MemoryReason = "AI goroutine limit reached"
			}
			if triageResult.WorthKnowledge {
				run.KnowledgeStatus = "skipped"
				run.KnowledgeReason = "AI goroutine limit reached"
			}
		}
	}

	// Handle SOP (independent of the other deliverables). Per-session
	// sop_status guard prevents re-dispatching on retry. SOP outcomes are
	// log-observed only for now — no pipeline-run columns (see plan note).
	alreadyProcessedSOP := group.Sessions[0].SOPStatus != ""
	if triageResult.WorthSOP && !alreadyProcessedSOP {
		result := c.app.dispatchTriageSOP(triageResult, group.primaryID(), summary, group.startTime(), group.endTime(), sessionIDsFromGroup(group)...)
		for _, s := range group.Sessions {
			if err := c.app.activity.store.UpdateSOPStatus(s.ID, result.Status); err != nil {
				log.Printf("[triager] failed to set sop_status for session %s: %s", s.ID, activityPrivateLogField("error", err.Error()))
			}
		}
		log.Printf("[triager] sop dispatch for group %s: %s (%s)", group.primaryID(), result.Status, result.Reason)
	} else if triageResult.WorthSOP && alreadyProcessedSOP {
		log.Printf("[triager] sop dispatch skipped for group %s: session already has sop_status (retry guard)", group.primaryID())
	}

	// The group has already been triaged for Memory, Knowledge, and Playbooks.
	// Shadow task observation ran through its independent source-bound path
	// before this summary model. Agency owns all new work origination. Mark the
	// summary group terminal so the rotator stops retrying it.
	run.TriageStatus = "dismissed"
	run.TriageReason = "triage complete"
	for _, s := range group.Sessions {
		if err := c.app.activity.store.UpdateTriageStatus(s.ID, "dismissed"); err != nil {
			log.Printf("[triager] failed to mark session %s as processed: %s", s.ID, activityPrivateLogField("error", err.Error()))
		}
	}

	if c.app.telemetry != nil {
		c.app.telemetry.Capture("activity.session.triaged", map[string]any{
			"session_count":    len(group.Sessions),
			"worth_memory":     triageResult.WorthMemory,
			"worth_knowledge":  triageResult.WorthKnowledge,
			"worth_automation": triageResult.WorthAutomation,
			"worth_handoff":    triageResult.WorthHandoff,
			"worth_sop":        triageResult.WorthSOP,
		})
	}
}

// recordPipelineRun upserts an observability record and emits a Wails event
// so the Settings UI can update in real time.
//
// If run.ID == 0 the row is INSERTed and run.ID is populated in place so the
// caller can later reference the same row (e.g. to write back the async
// memory-generation outcome). If run.ID > 0 the synchronous-portion columns
// (completion timestamp, top-level error, triage/automation outcomes) are
// UPDATEd in place; memory_status and knowledge_status are intentionally
// excluded — those belong to the async worker (see
// UpdateActivityPipelineRunMemoryOutcome) and writing them here would race.
//
// Failures are logged but never propagated — observability must not affect
// the pipeline itself.
func (c *ActivityTriager) recordPipelineRun(run *ActivityPipelineRunRow) {
	if run == nil {
		return
	}
	if c.app == nil || c.app.activity == nil || c.app.activity.store == nil {
		return
	}
	if run.ID > 0 {
		if err := c.app.activity.store.UpdateActivityPipelineRunCompletion(
			run.ID, run.CompletedAt, run.Error,
			run.TriageStatus, run.TriageReason,
			run.AutomationStatus, run.AutomationReason, run.InitiativeID, run.ShadowRunID,
			run.HandoffStatus, run.HandoffReason, run.HandoffProposalID,
			run.ShadowObserverExpected,
			run.ShadowObserverAdmission,
			run.ShadowObserverDispatchStatus,
			run.ShadowObserverDispatchReason,
			run.ShadowObserverTraceID,
		); err != nil {
			log.Printf("[triager] failed to update pipeline run %d: %s", run.ID, activityPrivateLogField("error", err.Error()))
		}
		// Re-fetch before emitting: the async memory worker may already have
		// written its terminal status via UpdateActivityPipelineRunMemoryOutcome
		// and emitted its own event. Emitting the in-memory *run here would
		// carry the stale "dispatched" placeholder for memory_status and let
		// the frontend replace-by-id regress the UI back to "dispatched".
		if c.app.ctx != nil && c.app.ctx.Value("events") != nil {
			row, err := c.app.activity.store.GetActivityPipelineRunByID(run.ID)
			if err != nil {
				log.Printf("[triager] failed to fetch pipeline run %d for emit: %s", run.ID, activityPrivateLogField("error", err.Error()))
				return
			}
			runtime.EventsEmit(c.app.ctx, "activity_pipeline:run", row)
		}
		return
	}

	id, err := c.app.activity.store.SaveActivityPipelineRun(*run)
	if err != nil {
		log.Printf("[triager] failed to save pipeline run: %s", activityPrivateLogField("error", err.Error()))
		return
	}
	run.ID = id
	if c.app.ctx != nil && c.app.ctx.Value("events") != nil {
		// Insert path: the row was just created with no async writeback yet,
		// so emitting the in-memory copy is safe and saves a DB round-trip.
		runtime.EventsEmit(c.app.ctx, "activity_pipeline:run", *run)
	}
}

// knowledgeResponseOutcome describes a terminal pre-persist outcome of a
// knowledge generation response — either the model asked us to dedup-skip
// or returned an empty response we cannot persist. Non-terminal responses
// (create/update with points) are signalled by classifyKnowledgeResponse
// returning a nil outcome, and the caller proceeds to save/update.
type knowledgeResponseOutcome struct {
	status       string // telemetry status written to activity_pipeline_runs.knowledge_status
	reason       string // telemetry detail written to activity_pipeline_runs.knowledge_reason
	triageEvent  string // logTriage event name (e.g. "dedup_skip", "skip")
	triageDetail string // logTriage detail line
}

// classifyKnowledgeResponse is the single source of truth for deciding how to
// interpret a knowledgeGenerationResponse. It returns the normalized action
// and, for terminal shapes, a pre-filled outcome the caller should writeback.
//
// Ordering matters. The generation prompt explicitly requires
// `{"action":"skip","knowledgePoints":[]}` for the dedup path. If we checked
// emptiness before the action, every prompt-compliant dedup skip would be
// misattributed as "AI returned no knowledge points", collapsing the two
// distinct K-P3 telemetry signals ("generation produced nothing" vs "dedup
// suppressed duplicate"). We normalize action first, handle skip, and only
// then treat empty points as a prompt non-compliant failure.
func classifyKnowledgeResponse(knowledge *knowledgeGenerationResponse) (action string, outcome *knowledgeResponseOutcome) {
	action = knowledge.Action
	if action == "" {
		action = "create" // backwards compatibility
	}

	if action == "skip" {
		return action, &knowledgeResponseOutcome{
			status:       "skipped",
			reason:       "dedup_skip: " + activityLogFieldMetadata("title", knowledge.Title),
			triageEvent:  "dedup_skip",
			triageDetail: activityLogFieldMetadata("title", knowledge.Title),
		}
	}

	if len(knowledge.KnowledgePoints) == 0 {
		return action, &knowledgeResponseOutcome{
			status:       "skipped",
			reason:       "AI returned no knowledge points",
			triageEvent:  "skip",
			triageDetail: "AI returned no knowledge points",
		}
	}

	return action, nil
}

// generateAndSaveKnowledge runs a stronger model to generate high-quality knowledge,
// then persists it. This mirrors an analysis+generation two-phase approach.
//
// runID is the activity_pipeline_runs row ID written before dispatch. Each
// terminal path calls updateOutcome to write back the real status
// (created/updated/skipped/error) so the Settings UI reflects what actually
// happened instead of leaving the row stuck at "dispatched". runID == 0 means
// no row was inserted (e.g. semaphore-skipped path) and writebacks are no-ops.
func (c *ActivityTriager) generateAndSaveKnowledge(ctx context.Context, runID int64, group sessionGroup, summary string, profile SessionContentProfile) {
	session := group.Sessions[0]

	if profile.Dominant == ContentModeAmbient {
		reason := "ambient-dominant session: no extractable knowledge signal"
		log.Printf("[triager] subject gate skip knowledge for group %s: %s", session.ID, reason)
		logTriage("knowledge", session.ID, "subject_gate_skip", reason)
		if runID > 0 && c.app != nil && c.app.activity != nil && c.app.activity.store != nil {
			if err := c.app.activity.store.UpdateActivityPipelineRunKnowledgeOutcome(runID, "skipped", reason); err != nil {
				log.Printf("[triager] failed to update knowledge outcome for run %d: %s", runID, activityPrivateLogField("error", err.Error()))
			}
		}
		return
	}

	// updateOutcome writes back the real knowledge generation outcome to
	// activity_pipeline_runs and immediately re-emits the row so the
	// Settings UI can replace its in-memory copy (which is still showing
	// the "dispatched" placeholder written before the goroutine started).
	// All steps are best-effort — telemetry must never block or panic the
	// knowledge pipeline.
	updateOutcome := func(status, reason string) {
		if status == "error" {
			reason = activityLogFieldMetadata("error", reason)
		}
		if group.onOutcome != nil {
			group.onOutcome(status, reason)
		}
		if runID <= 0 {
			return
		}
		if c.app == nil || c.app.activity == nil || c.app.activity.store == nil {
			return
		}
		if err := c.app.activity.store.UpdateActivityPipelineRunKnowledgeOutcome(runID, status, reason); err != nil {
			log.Printf("[triager] failed to update knowledge outcome for run %d: %s", runID, activityPrivateLogField("error", err.Error()))
			return
		}
		if c.app.ctx == nil || c.app.ctx.Value("events") == nil {
			return
		}
		row, err := c.app.activity.store.GetActivityPipelineRunByID(runID)
		if err != nil {
			log.Printf("[triager] failed to fetch run %d after knowledge outcome write: %s", runID, activityPrivateLogField("error", err.Error()))
			return
		}
		runtime.EventsEmit(c.app.ctx, "activity_pipeline:run", row)
	}

	// This worker may have waited behind another generation since admission.
	// Recheck protection before the retained summary reaches another model.
	if !c.triageGroupAllowsModelRead(group) {
		updateOutcome("skipped", triageSourceExcludedReason)
		return
	}
	genCtx, cancelGen := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelGen()
	knowledge, err := c.runKnowledgeGeneration(genCtx, summary)
	if err != nil {
		log.Printf("[triager] knowledge generation failed for group %s: %s", session.ID, activityPrivateLogField("error", err.Error()))
		updateOutcome("error", err.Error())
		return
	}

	// Link diagnostics to the actual saved artifact for current panel reads.
	var panelEntryID string
	logDecision := func(decision, reason string) {
		logTriageEntry(TriageLogEntry{
			Source: "knowledge", ID: session.ID, EntryID: panelEntryID, Decision: decision, Reason: reason,
			Title: knowledge.Title, Description: knowledge.Description,
		})
	}

	action, outcome := classifyKnowledgeResponse(knowledge)
	if outcome != nil {
		// Terminal path: either dedup skip or prompt-non-compliant empty points.
		// classifyKnowledgeResponse is the single source of truth for ordering —
		// see its docstring for why action==skip must be checked before emptiness.
		if outcome.triageEvent == "dedup_skip" {
			log.Printf("[triager] knowledge generation returned skip for group %s — duplicate of existing knowledge", session.ID)
		} else {
			log.Printf("[triager] knowledge generation returned no points for group %s, skipping", session.ID)
		}
		triageDetail := outcome.triageDetail
		if outcome.triageEvent == "dedup_skip" {
			triageDetail = activityPrivateLogField("title", knowledge.Title)
		}
		logDecision(outcome.triageEvent, triageDetail)
		updateOutcome(outcome.status, outcome.reason)
		return
	}

	switch action {
	case "update":
		if knowledge.UpdateID != "" {
			// updateKnowledge may fall back to create when the updateID is
			// invalid or the target file is missing. The returned action
			// string ("updated" vs "created") is what we record so telemetry
			// reflects the actual code path, not the AI's requested action.
			actualAction, err := c.updateKnowledge(session, group, knowledge)
			if err != nil {
				logDecision("error", "update failed: "+activityPrivateLogField("error", err.Error()))
				updateOutcome("error", fmt.Sprintf("update knowledge (id=%s): %v", knowledge.UpdateID, err))
				return
			}
			panelEntryID = knowledge.UpdateID
			if actualAction == "created" {
				panelEntryID = activityKnowledgeEntryID(group.entryKey(session))
			}
			logDecision("dedup_update", fmt.Sprintf("%s points=%d %s action=%s", activityPrivateLogField("update_id", knowledge.UpdateID), len(knowledge.KnowledgePoints), activityPrivateLogField("title", knowledge.Title), actualAction))
			updateOutcome(actualAction, fmt.Sprintf("%s points=%d", activityLogFieldMetadata("update_id", knowledge.UpdateID), len(knowledge.KnowledgePoints)))
			artifactID := knowledge.UpdateID
			if actualAction == "created" {
				artifactID = activityKnowledgeEntryID(group.entryKey(session))
			}
			c.app.linkInitiativeArtifact("activity", group.primaryID(), "knowledge", artifactID)
		} else {
			entryID, err := c.saveKnowledge(session, group, knowledge)
			if err != nil {
				logDecision("error", "save failed: "+activityPrivateLogField("error", err.Error()))
				updateOutcome("error", fmt.Sprintf("save knowledge (update without id): %v", err))
				return
			}
			c.app.linkInitiativeArtifact("activity", group.primaryID(), "knowledge", entryID)
			panelEntryID = entryID
			logDecision("saved", fmt.Sprintf("points=%d %s (update without id)", len(knowledge.KnowledgePoints), activityPrivateLogField("title", knowledge.Title)))
			updateOutcome("created", fmt.Sprintf("points=%d (update without id, fell back to create)", len(knowledge.KnowledgePoints)))
		}
	default: // "create"
		entryID, err := c.saveKnowledge(session, group, knowledge)
		if err != nil {
			logDecision("error", "save failed: "+activityPrivateLogField("error", err.Error()))
			updateOutcome("error", fmt.Sprintf("save knowledge: %v", err))
			return
		}
		panelEntryID = entryID
		logDecision("saved", fmt.Sprintf("points=%d %s", len(knowledge.KnowledgePoints), activityPrivateLogField("title", knowledge.Title)))
		updateOutcome("created", fmt.Sprintf("points=%d", len(knowledge.KnowledgePoints)))
		c.app.linkInitiativeArtifact("activity", group.primaryID(), "knowledge", entryID)
		if c.app.ctx != nil && entryID != "" {
			c.app.emitBrowserEvent("insight:saved", map[string]string{
				"kind":   "knowledge",
				"id":     entryID,
				"title":  knowledge.Title,
				"source": group.insightSource(),
			})
		}
	}
}

// saveKnowledge persists extracted knowledge points to ~/.yansu-agent/knowledge/.
// Returns (entryID, nil) on success, ("", err) on any failure (home dir lookup,
// mkdir, or file write). Callers MUST inspect err so telemetry writebacks
// (see P3 in docs/reviews/2026-04-19-activity-knowledge-bottleneck-findings.md)
// record the real outcome rather than a false-positive "created".
func (c *ActivityTriager) saveKnowledge(session ActivitySessionRow, group sessionGroup, knowledge *knowledgeGenerationResponse) (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		log.Printf("[triager] failed to get home dir for knowledge: %s", activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("home dir: %w", err)
	}
	knowledgeDir := filepath.Join(dataRoot, "knowledge")
	if err := os.MkdirAll(knowledgeDir, 0700); err != nil {
		log.Printf("[triager] failed to create knowledge dir: %s", activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("mkdir %s: %w", knowledgeDir, err)
	}

	// Generate deterministic filename from session ID to avoid duplicates
	entryID := activityKnowledgeEntryID(group.entryKey(session))
	filename := entryID + ".md"
	appNames := group.allAppNames()
	moment := KnowHowMoment{At: session.StartTime, SessionID: session.ID}
	if len(appNames) > 0 {
		moment.App = appNames[0]
	}
	if existing, ok := relearnKnowHowWithEvidence(knowledgeDir, knowledge.Title, entryID, moment, knowledge.KnowledgePoints, "activity", appNames, knowledge.provenance, sessionIDsFromGroup(group)...); ok {
		log.Printf("[triager] same lesson as an existing entry; added the moment: %s", activityPrivateLogField("entry", existing))
		c.app.markMemoryDirty("activity-knowledge-relearned")
		return existing, nil
	}
	unlock := memoryFileLocks.Acquire("knowledge:" + entryID)
	defer unlock()

	ts := time.UnixMilli(session.StartTime)
	filePath := filepath.Join(knowledgeDir, filename)
	if err := snapshotKnowledgeFile(knowledgeDir, entryID); err != nil {
		log.Printf("[triager] failed to snapshot knowledge file: %s %s", activityPrivateLogField("file", filePath), activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("snapshot %s: %w", entryID, err)
	}
	content := buildOKFKnowledgeMarkdown(entryID, knowledge.Title, knowledge.Description, ts, "activity", appNames, sessionIDsFromGroup(group), knowledge.KnowledgePoints, normalizeKnowHowKind(knowledge.Kind))
	content = c.app.normalizeGeneratedMarkdownToCurrentUILanguage("knowledge", content)
	previous, readErr := os.ReadFile(filePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", readErr
	}
	content = knowledgeProvenance(content, string(previous), knowledgeProvenanceUnion([]string{"activity"}, knowledge.provenance.sources), knowledgeProvenanceUnion(appNames, knowledge.provenance.apps), knowledge.provenance.complete)
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		log.Printf("[triager] failed to write knowledge file: %s %s", activityPrivateLogField("file", filePath), activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("write %s: %w", filePath, err)
	}

	log.Printf("[triager] saved knowledge: %s", activityPrivateLogField("file", filePath))

	if c.app.telemetry != nil {
		c.app.telemetry.Capture("knowledge.created", map[string]any{
			"entry_id":     entryID,
			"points_count": len(knowledge.KnowledgePoints),
			"source":       "activity",
		})
	}

	return entryID, nil
}

func activityKnowledgeEntryID(sessionID string) string {
	hash := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("activity-%x", hash[:8])
}

// updateKnowledge updates an existing knowledge file identified by
// knowledge.UpdateID.
//
// Returns ("updated", nil) on a real in-place update, ("created", nil) when
// updateID is invalid / missing and we fall back to creating a fresh entry,
// and ("", err) on any failure. The action string is what the caller must
// write to activity_pipeline_runs.knowledge_status so telemetry reflects the
// actual code path taken (see P3 in
// docs/reviews/2026-04-19-activity-knowledge-bottleneck-findings.md).
func (c *ActivityTriager) updateKnowledge(session ActivitySessionRow, group sessionGroup, knowledge *knowledgeGenerationResponse) (string, error) {
	dir, err := knowledgeDir()
	if err != nil {
		log.Printf("[triager] failed to get knowledge dir: %s", activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("knowledge dir: %w", err)
	}

	// Sanitize updateID to prevent path traversal
	if strings.Contains(knowledge.UpdateID, "..") || strings.ContainsAny(knowledge.UpdateID, "/\\") {
		log.Printf("[triager] invalid knowledge updateID, falling back to create: %s", activityPrivateLogField("update_id", knowledge.UpdateID))
		if _, err := c.saveKnowledge(session, group, knowledge); err != nil {
			return "", err
		}
		return "created", nil
	}

	filePath := filepath.Join(dir, knowledge.UpdateID+".md")
	unlock := memoryFileLocks.Acquire("knowledge:" + knowledge.UpdateID)
	defer unlock()

	// Verify the target file exists
	existingData, readErr := os.ReadFile(filePath)
	if os.IsNotExist(readErr) {
		log.Printf("[triager] knowledge updateID not found, falling back to create: %s", activityPrivateLogField("update_id", knowledge.UpdateID))
		unlock()
		if _, err := c.saveKnowledge(session, group, knowledge); err != nil {
			return "", err
		}
		return "created", nil
	}
	if readErr != nil {
		return "", fmt.Errorf("read %s: %w", filePath, readErr)
	}

	appNames := group.allAppNames()
	ts := time.UnixMilli(session.StartTime)

	// Merge session IDs: previous updates must stay attached to this
	// knowledge entry. OKF stores them as an extension list rather than a
	// body metadata block.
	mergedSessionIDs := mergeKnowledgeSessionIDs(string(existingData), group)

	if err := snapshotKnowledgeFile(dir, knowledge.UpdateID); err != nil {
		log.Printf("[triager] failed to snapshot knowledge file: %s %s", activityPrivateLogField("file", filePath), activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("snapshot %s: %w", knowledge.UpdateID, err)
	}
	content := buildOKFKnowledgeMarkdown(knowledge.UpdateID, knowledge.Title, knowledge.Description, ts, "activity", appNames, mergedSessionIDs, knowledge.KnowledgePoints, updatedKnowHowKind(string(existingData), knowledge.Kind))
	content = c.app.normalizeGeneratedMarkdownToCurrentUILanguage("knowledge", content)
	content = preserveKnowHowMoments(content, string(existingData))
	content = knowledgeProvenance(content, string(existingData), knowledgeProvenanceUnion([]string{"activity"}, knowledge.provenance.sources), knowledgeProvenanceUnion(appNames, knowledge.provenance.apps), knowledge.provenance.complete)
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		log.Printf("[triager] failed to update knowledge file: %s %s", activityPrivateLogField("file", filePath), activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("write %s: %w", filePath, err)
	}

	log.Printf("[triager] updated existing knowledge: %s", activityPrivateLogField("file", filePath))

	if c.app.telemetry != nil {
		c.app.telemetry.Capture("knowledge.updated", map[string]any{
			"entry_id":     knowledge.UpdateID,
			"points_count": len(knowledge.KnowledgePoints),
			"source":       "activity",
		})
	}

	return "updated", nil
}

// updateActivityMemory updates an existing memory file identified by updateID.
//
// Returns ("updated", nil) on a real in-place update, ("created", nil) when
// updateID is invalid / missing and we fall back to creating a fresh entry,
// and ("", err) on any failure. The action string is what the caller must
// write to activity_pipeline_runs.memory_status so telemetry reflects the
// actual code path taken (see P3 in
// docs/reviews/2026-04-18-activity-memory-bottleneck-findings.md).
func (c *ActivityTriager) updateActivityMemory(ctx context.Context, updateID string, session ActivitySessionRow, group sessionGroup, memory *mcMemoryGenerationResponse) (string, error) {
	dir, err := memoryDir()
	if err != nil {
		log.Printf("[triager] failed to get memory dir: %s", activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("memory dir: %w", err)
	}

	// Sanitize updateID to prevent path traversal
	if strings.Contains(updateID, "..") || strings.ContainsAny(updateID, "/\\") {
		log.Printf("[triager] invalid memory updateID, falling back to create: %s", activityPrivateLogField("update_id", updateID))
		if _, err := c.saveActivityMemoryContext(ctx, session, group, memoryGenerationUnverifiedUpdate(memory)); err != nil {
			return "", err
		}
		return "created", nil
	}

	filePath := filepath.Join(dir, updateID+".md")

	// Serialize against any concurrent save/update for this memory. Acquired
	// before stat so a racing saveActivityMemory can't write the file between
	// the existence check and the read-modify-write cycle below.
	unlock := memoryFileLocks.Acquire(updateID)
	defer unlock()

	// Verify the target file exists
	info, statErr := os.Stat(filePath)
	if os.IsNotExist(statErr) {
		log.Printf("[triager] memory updateID not found, falling back to create: %s", activityPrivateLogField("update_id", updateID))
		// saveActivityMemory also acquires memoryFileLocks for entryID. That
		// keyed lock is re-entrant across different keys; the fallback uses a
		// deterministic entryID derived from session.ID, which differs from
		// the stale updateID we just failed to find — no self-deadlock.
		unlock()
		if _, err := c.saveActivityMemoryContext(ctx, session, group, memoryGenerationUnverifiedUpdate(memory)); err != nil {
			return "", err
		}
		return "created", nil
	}
	if statErr != nil {
		return "", fmt.Errorf("stat %s: %w", filePath, statErr)
	}

	// Read the existing file so we can (a) preserve the historical session
	// trajectory and (b) snapshot it to .history/ before overwriting.
	existingData, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", filePath, err)
	}
	existing := string(existingData)
	memory.Title = memoryTitleInSecondPerson(memory.Title)
	candidate := buildOKFMemoryMarkdown(updateID, memory.Title, time.Now(), memorySource("activity"), memory.Dimension, memory.MemoryPoints)
	if doc, ok := parseOKFDocument(candidate); ok {
		sessionIDs := []string{session.ID}
		for _, observed := range group.Sessions {
			sessionIDs = append(sessionIDs, observed.ID)
		}
		doc.Frontmatter["activity_sessions"] = sessionIDs
		candidate = renderOKFDocument(doc.Frontmatter, doc.Body)
	}
	unlock()

	if err := checkMemoryWriteAllowed(dir, updateID, candidate, true); err != nil {
		return "", err
	}
	existingPoints := parseMemoryPoints(existing)
	userName := c.app.getUserDisplayName()
	// Activity triager is a background pipeline (not user-initiated), so
	// pass "" — rewriteMemoryPointsFirstPerson then flips SuppressTelemetry on
	// instead of emitting a trace-less agentloop.* row.
	finalPoints, err := c.app.rewriteMemoryUpdatePoints(ctx, updateID, existing, existingPoints, memory.MemoryPoints, userName, "")
	if err != nil {
		return "", fmt.Errorf("rewrite memory points: %w", err)
	}

	// Extract the Activity Session block verbatim, then upgrade it in memory
	// if it's still in the legacy flat format. The upgrade wraps the existing
	// rows under a single `(pre-migration)` group anchored at the file's
	// mtime — best-effort approximation of when the flat block was last
	// written, since we never stored that timestamp explicitly.
	existingBlock := extractActivitySessionBlock(existing)
	existingBlock = upgradeFlatActivitySessionBlock(existingBlock, info.ModTime().UnixMilli())

	appNames := group.allAppNames()
	now := time.Now()

	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s\n\n", strings.Join(strings.Fields(memory.Title), " ")))
	b.WriteString(fmt.Sprintf("- **Updated**: %s\n", now.Format("2006-01-02 15:04")))
	b.WriteString("- **Source**: activity\n")
	b.WriteString(fmt.Sprintf("- **Dimension**: %s\n", normalizeMemoryDimension(memory.Dimension)))
	b.WriteString(fmt.Sprintf("- **Apps**: %s\n", formatActivityAppNames(appNames)))
	writeGroupedActivitySessionBlock(&b, existingBlock, group, "update", now)
	b.WriteString("\n")

	b.WriteString("## Memory Points\n\n")
	for _, point := range finalPoints {
		b.WriteString(fmt.Sprintf("- %s\n", point))
	}
	b.WriteString("\n")

	provenance := memoryGenerationProvenance(memory, memoryReadProvenanceForFile(dir, updateID, existing), memoryGenerationCurrentSource(memory, activityGroupMemoryProvenance(group)))
	content := activityMemoryWithProvenance(updateID, b.String(), provenance, append(parseMemoryFrontmatter(existing).Apps, appNames...), append(memoryEvidenceSessionIDs(existing), sessionIDsFromGroup(group)...))
	if err := c.app.prepareMemoryObservedWriteContext(ctx, dir, updateID, content); err != nil {
		return "", err
	}
	if c.app.memoryUpdateAllowsModelRead(updateID, existing) {
		// Keep the legacy metadata outside the translation input, then put
		// the same verified source envelope around the translated result.
		translated := c.app.normalizeGeneratedMarkdownToCurrentUILanguage("memory", b.String())
		content = activityMemoryWithProvenance(updateID, translated, provenance, append(parseMemoryFrontmatter(existing).Apps, appNames...), append(memoryEvidenceSessionIDs(existing), sessionIDsFromGroup(group)...))
	}
	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock = memoryFileLocks.Acquire(updateID)
	defer unlock()
	if err := validateMemoryObservationUnchanged(dir, updateID, existingData, content); err != nil {
		return "", err
	}
	content = preserveMemoryTopics(content, existing)
	content, err = preserveMemoryConfirmation(content, existing)
	if err != nil {
		return "", err
	}
	// Snapshot BEFORE any write so an interrupted update (crash between
	// snapshot and write) leaves the old content recoverable from .history/.
	if err := snapshotMemoryFile(dir, updateID); err != nil {
		log.Printf("[triager] snapshot memory before update failed: %s %s", activityPrivateLogField("entry_id", updateID), activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("snapshot %s: %w", updateID, err)
	}

	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		log.Printf("[triager] failed to update memory file: %s %s", activityPrivateLogField("file", filePath), activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("write %s: %w", filePath, err)
	}

	log.Printf("[triager] updated existing memory: %s", activityPrivateLogField("file", filePath))
	logSpeechMemorySaved(group, session.ID, "updated", memory.Title, filePath, len(finalPoints))
	if c.app.ctx != nil {
		c.app.emitBrowserEvent("memory:updated")
	}
	c.app.markMemoryDirty("activity-memory-update")
	return "updated", nil
}

// generateAndSaveMemory runs the strongest model to generate personal context memories,
// then persists them. Follows the same pattern as generateAndSaveKnowledge.
//
// runID is the activity_pipeline_runs row ID written before dispatch. Each
// terminal path calls updateOutcome to write back the real status
// (created/updated/skipped/error) so the Settings UI reflects what actually
// happened instead of leaving the row stuck at "dispatched". runID == 0 means
// no row was inserted (e.g. semaphore-skipped path) and writebacks are no-ops.
func (c *ActivityTriager) generateAndSaveMemory(ctx context.Context, runID int64, group sessionGroup, summary string, profile SessionContentProfile) {
	session := group.Sessions[0]

	// updateOutcome writes back the real memory generation outcome to
	// activity_pipeline_runs and immediately re-emits the row so the
	// Settings UI can replace its in-memory copy (which is still showing
	// the "dispatched" placeholder written before the goroutine started).
	// All steps are best-effort — telemetry must never block or panic the
	// memory pipeline.
	updateOutcome := func(status, reason string) {
		if status == "error" {
			reason = activityLogFieldMetadata("error", reason)
		}
		if group.onOutcome != nil {
			group.onOutcome(status, reason)
		}
		if runID <= 0 {
			return
		}
		if c.app == nil || c.app.activity == nil || c.app.activity.store == nil {
			return
		}
		if err := c.app.activity.store.UpdateActivityPipelineRunMemoryOutcome(runID, status, reason); err != nil {
			log.Printf("[triager] failed to update memory outcome for run %d: %s", runID, activityPrivateLogField("error", err.Error()))
			return
		}
		if c.app.ctx == nil || c.app.ctx.Value("events") == nil {
			return
		}
		row, err := c.app.activity.store.GetActivityPipelineRunByID(runID)
		if err != nil {
			log.Printf("[triager] failed to fetch run %d after memory outcome write: %s", runID, activityPrivateLogField("error", err.Error()))
			return
		}
		runtime.EventsEmit(c.app.ctx, "activity_pipeline:run", row)
	}

	// This worker may have waited behind another generation since admission.
	// Recheck protection before the retained summary reaches another model.
	if !c.triageGroupAllowsModelRead(group) {
		updateOutcome("skipped", triageSourceExcludedReason)
		return
	}
	genCtx, cancelGen := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelGen()
	memory, err := c.runMemoryGeneration(genCtx, group.primaryID(), summary, profile)
	if err != nil {
		log.Printf("[triager] memory generation failed for group %s: %s", session.ID, activityPrivateLogField("error", err.Error()))
		updateOutcome("error", err.Error())
		return
	}

	// Link diagnostics to the actual saved artifact for current panel reads.
	var panelEntryID string
	logDecision := func(decision, reason string) {
		logTriageEntry(TriageLogEntry{
			Source: "memory", ID: session.ID, EntryID: panelEntryID, Decision: decision, Reason: reason,
			Title: memory.Title, Description: "",
		})
	}

	if len(memory.MemoryPoints) == 0 {
		log.Printf("[triager] memory generation returned no points for group %s, skipping", session.ID)
		logDecision("skip", "AI returned no memory points")
		updateOutcome("skipped", "AI returned no memory points")
		return
	}

	gate, reason := ValidateMemoryDimension(profile, memory.Dimension)
	logReason := activityLogFieldMetadata("reason", reason)
	if triageSystemReason(reason) {
		logReason = reason
	}
	switch gate {
	case MemoryGateSkip:
		log.Printf("[triager] subject gate skip for group %s: %s", session.ID, logReason)
		logDecision("subject_gate_skip", reason)
		updateOutcome("skipped", logReason)
		return
	case MemoryGateCoerceToHighlight:
		log.Printf("[triager] subject gate coerce for group %s: %s", session.ID, logReason)
		logDecision("subject_gate_coerce", reason)
		memory.Dimension = "topic_highlight"
	}

	// Correction-match gate: if this memory's title or any point overlaps
	// closely with a recently rejected/reassigned memory, refuse to save —
	// the user has already told us that exact attribution is wrong.
	if dir, err := memoryDir(); err == nil {
		if corrections, err := LoadRecentCorrections(dir, 50); err != nil {
			log.Printf("[triager] load corrections for gate failed: %s", activityPrivateLogField("error", err.Error()))
		} else if match := FindMatchingCorrection(corrections, memory.Title, memory.MemoryPoints); match != nil {
			logReason := fmt.Sprintf("kind=%s score=%.2f %s %s", match.Correction.Kind, match.Score,
				activityPrivateLogField("title", match.Correction.Title), activityPrivateLogField("reason", match.Correction.Reason))
			log.Printf("[triager] correction-match skip for group %s: %s", session.ID, logReason)
			logDecision("correction_match_skip", logReason)
			updateOutcome("skipped", fmt.Sprintf("%s score=%.2f %s %s", activityLogFieldMetadata("kind", string(match.Correction.Kind)), match.Score, activityLogFieldMetadata("title", match.Correction.Title), activityLogFieldMetadata("reason", match.Correction.Reason)))
			return
		}
	}

	// Handle dedup action
	action := memory.Action
	if action == "" {
		action = "create" // backwards compatibility
	}

	switch action {
	case "skip":
		log.Printf("[triager] memory generation returned skip for group %s — duplicate of existing memory", session.ID)
		logDecision("dedup_skip", activityPrivateLogField("title", memory.Title))
		updateOutcome("skipped", "dedup_skip: "+activityLogFieldMetadata("title", memory.Title))
	case "update":
		if memory.UpdateID != "" {
			// updateActivityMemory may fall back to create when the
			// updateID is invalid or missing. The returned action
			// string ("updated" vs "created") is what we record so
			// telemetry reflects the actual code path, not the AI's
			// requested action.
			actualAction, err := c.updateActivityMemory(ctx, memory.UpdateID, session, group, memory)
			if err != nil {
				logDecision("error", "update failed: "+activityPrivateLogField("error", err.Error()))
				updateOutcome("error", fmt.Sprintf("update memory (id=%s): %v", memory.UpdateID, err))
				return
			}
			panelEntryID = memory.UpdateID
			if actualAction == "created" {
				panelEntryID = activityMemoryEntryID(group.entryKey(session))
			}
			logDecision("dedup_update", fmt.Sprintf("%s points=%d %s action=%s", activityPrivateLogField("update_id", memory.UpdateID), len(memory.MemoryPoints), activityPrivateLogField("title", memory.Title), actualAction))
			updateOutcome(actualAction, fmt.Sprintf("%s points=%d", activityLogFieldMetadata("update_id", memory.UpdateID), len(memory.MemoryPoints)))
			artifactID := memory.UpdateID
			if actualAction == "created" {
				artifactID = activityMemoryEntryID(group.entryKey(session))
			}
			c.app.linkInitiativeArtifact("activity", group.primaryID(), "memory", artifactID)
		} else {
			entryID, err := c.saveActivityMemoryContext(ctx, session, group, memory)
			if err != nil {
				logDecision("error", "save failed: "+activityPrivateLogField("error", err.Error()))
				updateOutcome("error", fmt.Sprintf("save memory (update without id): %v", err))
				return
			}
			c.app.linkInitiativeArtifact("activity", group.primaryID(), "memory", entryID)
			panelEntryID = entryID
			logDecision("saved", fmt.Sprintf("points=%d %s (update without id)", len(memory.MemoryPoints), activityPrivateLogField("title", memory.Title)))
			updateOutcome("created", fmt.Sprintf("points=%d (update without id, fell back to create)", len(memory.MemoryPoints)))
		}
	default: // "create"
		entryID, err := c.saveActivityMemoryContext(ctx, session, group, memory)
		if err != nil {
			logDecision("error", "save failed: "+activityPrivateLogField("error", err.Error()))
			updateOutcome("error", fmt.Sprintf("save memory: %v", err))
			return
		}
		panelEntryID = entryID
		logDecision("saved", fmt.Sprintf("points=%d %s", len(memory.MemoryPoints), activityPrivateLogField("title", memory.Title)))
		updateOutcome("created", fmt.Sprintf("points=%d", len(memory.MemoryPoints)))
		c.app.linkInitiativeArtifact("activity", group.primaryID(), "memory", entryID)
		if c.app.ctx != nil && entryID != "" {
			c.app.emitBrowserEvent("insight:saved", map[string]string{
				"kind":   "memory",
				"id":     entryID,
				"title":  memory.Title,
				"source": group.insightSource(),
			})
		}
	}
}

// saveActivityMemory persists memory points to ~/.yansu-agent/memory/ in the
// standard format. Returns (entryID, nil) on success, ("", err) on any
// failure (memory dir lookup or file write). Callers MUST inspect err to
// avoid recording false-positive "created" outcomes in
// activity_pipeline_runs.memory_status — that telemetry is the whole point
// of P3.
func (c *ActivityTriager) saveActivityMemory(session ActivitySessionRow, group sessionGroup, memory *mcMemoryGenerationResponse) (string, error) {
	return c.saveActivityMemoryContext(c.app.memorySemanticContext(), session, group, memory)
}

func (c *ActivityTriager) saveActivityMemoryContext(ctx context.Context, session ActivitySessionRow, group sessionGroup, memory *mcMemoryGenerationResponse) (string, error) {
	memory.Title = memoryTitleInSecondPerson(memory.Title)
	dir, err := memoryDir()
	if err != nil {
		log.Printf("[triager] failed to get memory dir: %s", activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("memory dir: %w", err)
	}

	// Deterministic filename from source + session ID
	entryID := activityMemoryEntryID(group.entryKey(session))
	filename := entryID + ".md"

	appNames := group.allAppNames()

	now := time.Now()
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s\n\n", strings.Join(strings.Fields(memory.Title), " ")))
	b.WriteString(fmt.Sprintf("- **Updated**: %s\n", now.Format("2006-01-02 15:04")))
	b.WriteString("- **Source**: activity\n")
	b.WriteString(fmt.Sprintf("- **Dimension**: %s\n", normalizeMemoryDimension(memory.Dimension)))
	b.WriteString(fmt.Sprintf("- **Apps**: %s\n", formatActivityAppNames(appNames)))
	writeGroupedActivitySessionBlock(&b, "", group, "initial", now)
	b.WriteString("\n")

	b.WriteString("## Memory Points\n\n")
	for _, point := range memory.MemoryPoints {
		b.WriteString(fmt.Sprintf("- %s\n", point))
	}
	b.WriteString("\n")

	filePath := filepath.Join(dir, filename)
	content := c.app.normalizeGeneratedMarkdownToCurrentUILanguage("memory", b.String())
	content = activityMemoryWithProvenance(entryID, content, memoryGenerationProvenance(memory, memoryGenerationCurrentSource(memory, activityGroupMemoryProvenance(group))), appNames, sessionIDsFromGroup(group))
	if err := c.app.prepareMemoryObservedWriteContext(ctx, dir, entryID, content); err != nil {
		return "", err
	}
	// Serialize concurrent writes to this entry. Two sessions racing into the
	// same deterministic entryID (identical session IDs are rare but possible
	// with clock skew / retries) would otherwise clobber each other.
	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock := memoryFileLocks.Acquire(entryID)
	defer unlock()

	if err := checkMemoryWriteAllowed(dir, entryID, content, true); err != nil {
		return "", err
	}
	if previous, err := os.ReadFile(filePath); err == nil {
		content = preserveMemoryTopics(content, string(previous))
		content, err = preserveMemoryConfirmation(content, string(previous))
		if err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// Snapshot before overwriting so an accidental collision with an existing
	// file (e.g. resume after crash where the row was re-queued) is
	// recoverable from .history/. No-op when the target doesn't exist yet,
	// which is the common case.
	if err := snapshotMemoryFile(dir, entryID); err != nil {
		log.Printf("[triager] snapshot memory before save failed: %s %s", activityPrivateLogField("entry_id", entryID), activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("snapshot %s: %w", entryID, err)
	}

	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		log.Printf("[triager] failed to write memory file: %s %s", activityPrivateLogField("file", filePath), activityPrivateLogField("error", err.Error()))
		return "", fmt.Errorf("write %s: %w", filePath, err)
	}

	log.Printf("[triager] saved memory: %s", activityPrivateLogField("file", filePath))
	logSpeechMemorySaved(group, session.ID, "saved", memory.Title, filePath, len(memory.MemoryPoints))

	if c.app.ctx != nil {
		c.app.emitBrowserEvent("memory:updated")
	}
	// Mark memory store dirty so the FTS5+vector index picks up the new file.
	c.app.markMemoryDirty("activity-memory")
	return entryID, nil
}

func activityMemoryEntryID(sessionID string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("activity:%s", sessionID)))
	return fmt.Sprintf("activity-%x", hash[:8])
}

// runMemoryGeneration uses the strongest model to extract personal context memories.
func (c *ActivityTriager) runMemoryGeneration(ctx context.Context, sourceID, summary string, profile SessionContentProfile) (*mcMemoryGenerationResponse, error) {
	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()

	if runner == nil {
		return nil, fmt.Errorf("runner not initialized")
	}

	userName := c.app.getUserDisplayName()

	// Load existing memories for dedup context
	promptContext := loadMemoryGenerationPromptContextForQuery(ctx, c.app, summary)
	existingMemories := promptContext.existing
	var existingSection string
	if existingMemories != "" {
		existingSection = fmt.Sprintf(`
## Existing memories (IMPORTANT — check for duplicates!)

These memories are comparison hints for choosing create/update/skip, not sources of new facts.
For create, extract only from the current data. For update, merge only the selected updateId with the current data.

The following memories already exist. Before creating a new entry:
1. If the new data is essentially the SAME as an existing entry (same topic, same insights), return action="skip".
2. If the new data UPDATES or EXPANDS an existing entry (same topic but new/changed details), return action="update" with the updateId set to the existing entry's id.
3. Only return action="create" if this is genuinely NEW information not covered by any existing entry.

%s`, existingMemories)
	}

	languageInstruction := responseLanguageInstruction(c.app.currentUILanguageName())
	perspectiveInstruction := profile.PerspectiveInstruction(userName)

	correctionsSection := promptContext.corrections
	initiativeBoundary := memoryGenerationSourceBoundary

	prompt := fmt.Sprintf(`%s

Extract personal memory points from this activity session. If nothing is worth remembering, return an empty array.

%s
%s
%s
Activity summary:
---
%s
---
%s
Respond with ONLY a JSON object (no markdown, no code fences):
{
  "action": "create|update|skip",
  "updateId": "existing-entry-id-if-updating",
  "title": "concise title describing the memory context",
  "memoryPoints": ["memory point 1", "memory point 2"],
  "dimension": "user_insight|topic_highlight|deep_user_model|agentic_memory"
}

## action field rules:

- "skip": The data duplicates existing memories with no new information. Return empty memoryPoints.
- "update": The data relates to an existing memory but has new or updated details. Set updateId to the id of the entry to update (from the [id=xxx] tag in existing memories). Return the COMPLETE updated memoryPoints (merged old + new).
- "create": The data contains genuinely new personal context not covered by any existing memory.

## What is memory?

Memory captures personal context about %s to help future AI interactions be more personalized and helpful. Think of it as building a profile of %s over time — written by %s, about %s.

## Three mandatory properties:

1. **Personal** — The insight is about %s specifically: their own preferences, habits, patterns, context. Generic facts that apply to everyone are NOT memory. Things OTHER people said about %s are NOT memory.

2. **Durable** — The insight is likely to remain true over time. Fleeting states are NOT memory. Stable patterns ("I prefer verbose error messages") ARE memory.

3. **Actionable** — Knowing this changes how you interact with %s. "I prefer bun over npm" directly influences future recommendations.

## Good memory examples (note the first-person voice):

- "I prefer using bun over npm for all Node.js projects"
- "My main project uses Go backend + React frontend + PostgreSQL"
- "I use Vim keybindings in VS Code"
- "My team follows conventional commits format"
- "I frequently work on frontend performance optimization"

## Bad memory examples:

- "Today I had a conversation about X" (diary entry, not memory)
- "Someone called me 'the goat'" / "Others praised my work on Y" (third-party opinion ABOUT me, not my own preference or behavior)
- "%s prefers bun over npm" (third-person voice — write in first person instead: "I prefer bun over npm")
- "React is a popular framework" (generic fact, not personal)
- "The code was deployed at 3pm" (ephemeral, not durable)
- "Programming requires attention to detail" (truism, not actionable)

## Output rules:

- Default: return action="skip" with empty memoryPoints []. This is correct when no personal context is present or when existing memories already cover it.
- Each point must be a clear, self-contained FIRST-PERSON statement about myself (from the user's perspective).
- Prefer specific details over vague observations.
- One genuine memory is better than five mediocre ones.
- Do NOT duplicate information that would already be in existing memories.
- Title should describe the CONTEXT, not what happened.

## Dimension classification (required)

Every memory you create or update MUST be tagged with exactly ONE dimension.
Choose the BEST fit from these four keys (use the exact lowercase key, no other value is accepted):

- "user_insight" — Concrete working habits, preferences, and tool choices.
  Examples: prefers Rust for CLI tools, uses dark mode, always commits from a feature branch,
  prefers bun over npm, uses Vim keybindings in VS Code.

- "topic_highlight" — Recent topics of focus, active investigations, short-term themes.
  Examples: researching vector databases this week, evaluating Kubernetes operators,
  currently debugging a Postgres replication lag, reading about rate limiting strategies.

- "deep_user_model" — Long-term persona, role, identity, or durable responsibilities.
  Examples: senior backend engineer focused on distributed systems, tech lead of the
  payments team at Acme, PhD student in ML research, solo founder building a
  developer-tool startup.

- "agentic_memory" — AI operational preferences and instructions for how the assistant
  should behave with this user.
  Examples: always run lint before commit, use conventional commits, don't suggest
  emoji in code, prefer minimal diffs, ask before running destructive commands.

Do NOT invent other values. Do NOT return "custom" — that is reserved for manual
user-authored memories and must never come from generation. If none of the four
fit, pick the closest match (usually "user_insight").`,
		languageInstruction, perspectiveInstruction, correctionsSection, initiativeBoundary, summary, existingSection,
		userName, userName, userName, userName,
		userName, userName,
		userName,
		userName)

	var result strings.Builder

	// Chat-tier model: structured extraction with a detailed rubric — the
	// chat tier is sufficient and the code tier's cost is not justified for
	// a 5-minute-cadence background job fed 200KB summaries.
	log.Printf("[triager] memory generation ExecuteTask starting")
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            redactActivityModelText(prompt),
		Model:             claudeCodeModelConfig().ChatModel,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
		case "error":
			log.Printf("[triager] memory generation stream error: %s", activityPrivateLogField("error", event.Error))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("execute memory generation: %w", err)
	}
	log.Printf("[triager] memory generation ExecuteTask completed, response length=%d", result.Len())

	raw := strings.TrimSpace(result.String())
	raw = stripCodeFences(raw)

	var resp mcMemoryGenerationResponse
	if err := unmarshalJSONBestEffort(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse memory generation response: %w", err)
	}

	promptContext.apply(&resp)
	dropPromptMemoryPoints(&resp)
	return &resp, nil
}

// ── Dedup helpers ────────────────────────────────────────────────────

// renderParsedContentSection collects high-fidelity parsed snapshots
// across the group's sessions and renders them as a markdown section
// for the triage prompt. Returns the section text (or "" when nothing
// renders) and the set of snapshot IDs whose parsed payload was used —
// callers exclude those IDs from OCR sampling so the same screen isn't
// re-described twice in worse form.
//
// "High fidelity" means: the parsed payload carries real semantic
// content beyond a content-type tag — at least one parsed message for
// conversation, or ≥ minParsedBodyLen of body text for web_page /
// document / task_list / events. Stubs (DedupOfHash) are resolved
// against sibling snapshots in the same session before the check.
//
// Within the group we dedup by (app, channel/title/url) and keep the
// "richest" instance — most messages, or longest body — so the prompt
// gets one good summary per conversation/page rather than 20 near-
// identical Slack snapshots that all show the same channel.
//
// Total output is capped at maxParsedSectionBytes to bound triage
// token cost; entries beyond the cap are dropped silently (the OCR
// section and event timeline still cover the activity).
func (c *ActivityTriager) renderParsedContentSection(group sessionGroup, labeler *senderLabeler) (string, map[int64]bool, []SelectedFrame) {
	covered := map[int64]bool{}
	if c.app == nil || c.app.activity == nil || c.app.activity.store == nil {
		return "", covered, nil
	}

	const (
		minParsedBodyLen      = 200
		maxParsedSectionBytes = 30_000
		maxMessagesPerConv    = 30
		maxBodyCharsPerEntry  = 1500
		// Single-message conversations need at least this many bytes
		// of total content to count as high fidelity. Guards against
		// "user just switched to Slack" frames where AX has only
		// loaded 1 placeholder message ("loading...", emoji-only
		// reply) — those frames' OCR is often the previous app's
		// real content (code, URLs) and shouldn't be suppressed.
		// 80 bytes ≈ 27 CJK chars ≈ 80 ASCII chars; a normal Chinese
		// reply (~30 chars) clears it, "好的"/"OK" do not.
		minSingleMessageChars = 80
	)

	type parsedEntry struct {
		app        string
		group      string // dedup key suffix: channel | title | URL
		ct         string
		title      string
		url        string
		channel    string
		messages   []appparser.ConversationMessage
		body       string
		snapshotID int64
		sessionID  string // owning session — needed to look up OCR for partial-AX fusion
		axCoverage string // ParsedContent.Metadata["axCoverage"] — drives OCR splice
		when       int64  // snapshot timestamp (ms)
	}

	// First pass: collect all snapshots in chronological order, build
	// a hash → full ParsedContent map for stub resolution. Group by
	// session because both lookups need it. Each snapshot's parsed
	// JSON is unmarshaled exactly once and the resulting ParsedContent
	// is cached by snapshot ID — the second pass below re-uses these
	// instead of paying for json.Unmarshal again.
	type sessionPack struct {
		sessionID    string
		snapshots    []ActivitySnapshotRow
		parsedByHash map[string]appparser.ParsedContent
		parsedBySnap map[int64]appparser.ParsedContent
	}
	packs := make([]sessionPack, 0, len(group.Sessions))
	for _, s := range group.Sessions {
		snaps, err := c.app.activity.store.GetSessionSnapshots(s.ID)
		if err != nil || len(snaps) == 0 {
			continue
		}
		hashMap := map[string]appparser.ParsedContent{}
		snapMap := map[int64]appparser.ParsedContent{}
		for _, sn := range snaps {
			if sn.ParsedJSON == "" {
				continue
			}
			var pc appparser.ParsedContent
			if err := json.Unmarshal([]byte(sn.ParsedJSON), &pc); err != nil {
				continue
			}
			snapMap[sn.ID] = pc
			if pc.DedupOfHash != "" {
				continue
			}
			if h := pc.Metadata["contentHash"]; h != "" {
				hashMap[h] = pc
			}
		}
		packs = append(packs, sessionPack{
			sessionID:    s.ID,
			snapshots:    snaps,
			parsedByHash: hashMap,
			parsedBySnap: snapMap,
		})
	}

	// Second pass: walk every snapshot, resolve stubs, decide whether
	// the parsed payload is high-fidelity, group by (app, dedupKey)
	// and keep the richest instance per group.
	type groupKey struct{ app, key string }
	best := map[groupKey]parsedEntry{}
	// siblingIDs records every snapshot that contributed to each
	// group, so OCR-suppression can be applied to all of them at
	// once IF (and only if) the group survives the size cap and
	// actually renders. The winner-only snapshotID inside best[gk]
	// would miss the other replicas (e.g. 5 Slack frames of the same
	// channel: only one becomes winner, but all 5 OCR to the same
	// noise and should be suppressed together).
	//
	// sessionID is tracked alongside snapshotID so the post-render emit
	// loop can attribute each sibling to its owning session — siblings
	// can come from any session in the merged group, and the Activity
	// page's per-session highlight filter needs that pairing.
	type siblingRef struct {
		sessionID  string
		snapshotID int64
	}
	siblingIDs := map[groupKey][]siblingRef{}
	order := []groupKey{}

	for _, pack := range packs {
		for _, sn := range pack.snapshots {
			if sn.ParsedJSON == "" {
				continue
			}
			pc, ok := pack.parsedBySnap[sn.ID]
			if !ok {
				// Either an unmarshal failure noted in the first
				// pass, or a row that arrived after pack assembly.
				// Either way, skip silently.
				continue
			}
			thisSnapshotID := sn.ID
			if pc.DedupOfHash != "" {
				resolved, ok := pack.parsedByHash[pc.DedupOfHash]
				if !ok {
					continue
				}
				pc = resolved
			}

			// High-fidelity gate. URL alone is NOT sufficient: a parsed
			// payload that renders to a single "URL: ..." line spends
			// almost nothing of the prompt's signal budget while still
			// suppressing the OCR for that frame, which is a net loss.
			// Require real content (messages or body) — URL becomes a
			// rendering bonus only when paired with body.
			//
			// For conversations: >=2 messages always passes (real
			// dialogue), single-message frames need enough body to
			// distinguish a substantive reply from placeholder/loading
			// state captured during an app switch. See
			// minSingleMessageChars for rationale.
			hasMessages := false
			if n := len(pc.Messages); n > 0 {
				if n >= 2 {
					hasMessages = true
				} else {
					hasMessages = len(strings.TrimSpace(pc.Messages[0].Content)) >= minSingleMessageChars
				}
			}
			hasBody := len(strings.TrimSpace(pc.Body)) >= minParsedBodyLen
			if !hasMessages && !hasBody {
				continue
			}

			app := pc.Metadata["app"]
			if app == "" {
				app = sn.App
			}
			if app == "" {
				app = "unknown"
			}

			dedupKey := pc.Channel
			if dedupKey == "" {
				dedupKey = pc.URL
			}
			if dedupKey == "" {
				dedupKey = pc.Title
			}
			if dedupKey == "" {
				// No semantic dedup key — don't fold distinct payloads
				// into one synthetic group keyed on bare ContentType
				// (that would silently merge three unrelated documents
				// into one). Use the snapshot ID so each instance lives
				// in its own group and renders independently.
				dedupKey = fmt.Sprintf("snap:%d", thisSnapshotID)
			}

			gk := groupKey{app: app, key: dedupKey}
			candidate := parsedEntry{
				app:        app,
				group:      dedupKey,
				ct:         pc.ContentType,
				title:      pc.Title,
				url:        pc.URL,
				channel:    pc.Channel,
				messages:   pc.Messages,
				body:       pc.Body,
				snapshotID: thisSnapshotID,
				sessionID:  pack.sessionID,
				axCoverage: pc.Metadata["axCoverage"],
				when:       sn.Timestamp,
			}
			cur, exists := best[gk]
			if !exists {
				order = append(order, gk)
				best[gk] = candidate
				siblingIDs[gk] = []siblingRef{{sessionID: pack.sessionID, snapshotID: thisSnapshotID}}
				continue
			}
			// Prefer the richer rendering: more messages, then longer
			// body, then later timestamp as tiebreaker.
			curScore := len(cur.messages)*10000 + len(cur.body)
			candScore := len(candidate.messages)*10000 + len(candidate.body)
			if candScore > curScore || (candScore == curScore && candidate.when > cur.when) {
				best[gk] = candidate
			}
			// Track every snapshot that contributed to this group so
			// that — IF the group actually gets rendered into the
			// prompt below — we can suppress OCR for all of them
			// (siblings would all OCR to the same Electron noise as
			// the chosen winner). Critically, we do NOT mark covered
			// here: the size cap may drop this group from the
			// rendered output, in which case suppressing its OCR
			// would silently kill BOTH signals for those frames.
			siblingIDs[gk] = append(siblingIDs[gk], siblingRef{sessionID: pack.sessionID, snapshotID: thisSnapshotID})
		}
	}

	if len(order) == 0 {
		return "", covered, nil
	}

	var selected []SelectedFrame
	// renderedGroups marks the (app, key) groups whose winner actually
	// emitted content into the prompt. Used after the loop to attribute
	// "parsed_oversize" skip records to groups that never rendered —
	// either because the size cap fired before their turn, or because
	// their renderer produced an empty section.
	renderedGroups := make(map[groupKey]bool, len(order))

	var b strings.Builder
	b.WriteString("\n## App-specific structured content\n")
	b.WriteString("(captured via accessibility / parser; lossless where present)\n\n")

	for _, gk := range order {
		if b.Len() >= maxParsedSectionBytes {
			break
		}
		e := best[gk]
		// Snapshot the section length so we can detect whether this
		// group actually contributed any content before marking its
		// snapshots as covered.
		sectionLenBefore := b.Len()
		switch e.ct {
		case "conversation":
			fmt.Fprintf(&b, "### [%s] %s\n", e.app, firstNonEmpty(e.channel, e.title, "(no channel)"))
			msgs := e.messages
			if len(msgs) > maxMessagesPerConv {
				// Keep the tail — most recent messages are usually the
				// reason the user opened the app.
				msgs = msgs[len(msgs)-maxMessagesPerConv:]
				fmt.Fprintf(&b, "(showing last %d of %d messages)\n", maxMessagesPerConv, len(e.messages))
			}
			for _, m := range msgs {
				if b.Len() >= maxParsedSectionBytes {
					break
				}
				body := strings.TrimSpace(redactActivityModelText(m.Content))
				if body == "" {
					continue
				}
				// Rune-aware truncation: a byte slice would split
				// multi-byte UTF-8 (e.g. CJK) and emit invalid bytes
				// into the LLM prompt.
				body = truncateForFocusedAppPrompt(body, 240)
				prefix := "-"
				if m.ThreadParent != "" {
					prefix = "  ↳"
				}
				ts := m.Timestamp
				if ts == "" {
					ts = "—"
				}
				senderLabel := labeler.Label(context.Background(), e.app, m.Sender, m.Content, e.sessionID, e.when)
				if senderLabel == "" || senderLabel == "—" {
					senderLabel = m.Sender
					if senderLabel == "" {
						senderLabel = "—"
					}
				}
				fmt.Fprintf(&b, "%s [%s] %s: %s\n", prefix, ts, senderLabel, body)
			}
			b.WriteString("\n")
		default:
			// web_page / document / task_list / events / unknown.
			fmt.Fprintf(&b, "### [%s] %s\n", e.app, firstNonEmpty(e.title, e.channel, e.url, "(untitled)"))
			if e.url != "" {
				fmt.Fprintf(&b, "URL: %s\n", e.url)
			}
			body := redactActivityModelText(e.body)
			// Partial-AX fusion: PowerPoint outline-only / Numbers
			// first-column-only bodies pass the high-fidelity gate
			// (200+ bytes of skeleton) but suppress the per-frame OCR
			// section below — without splicing OCR back in here, the
			// final prompt would carry only the skeleton and lose the
			// canvas / cell pixels. Reuse the merge helper so the
			// section header taxonomy ([AX: …] / [OCR: …]) matches
			// the quick-chat path. Safe no-op when no OCR landed yet.
			if e.snapshotID > 0 && e.sessionID != "" {
				probe := &appparser.ParsedContent{
					Body:     e.body,
					Metadata: map[string]string{"axCoverage": e.axCoverage},
				}
				if appparser.IsPartialAX(probe) {
					if ocr, err := c.app.activity.store.GetOCRTextBySnapshot(e.sessionID, e.snapshotID); err == nil {
						body = redactActivityModelText(appparser.MergeWithOCR(probe, ocr))
					}
				}
			}
			if trimmed := strings.TrimSpace(body); trimmed != "" {
				// Rune-aware truncation, see message branch above.
				trimmed = truncateForFocusedAppPrompt(trimmed, maxBodyCharsPerEntry)
				b.WriteString(trimmed)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
		// Only mark this group's snapshots as OCR-covered after we've
		// verified the renderer actually emitted content for it.
		// Otherwise the OCR section would lose those frames too while
		// the parsed section never got them in.
		if b.Len() > sectionLenBefore {
			renderedGroups[gk] = true
			for _, sib := range siblingIDs[gk] {
				covered[sib.snapshotID] = true
			}
			label := firstNonEmpty(e.channel, e.title, e.url, e.ct)
			// Slice out the exact text this frame just emitted into the
			// prompt builder so the audit UI can show "as seen by LLM"
			// without re-running the renderer / truncators.
			promptText := strings.TrimSpace(b.String()[sectionLenBefore:])
			// Per-winner sibling count: how many OTHER snapshots got
			// folded into this winner via dedup. Zero = singleton (no
			// dedup happened, winner represents only itself). Subtract
			// 1 because siblingIDs[gk] includes the winner itself.
			siblingCount := len(siblingIDs[gk]) - 1
			if siblingCount < 0 {
				siblingCount = 0
			}
			selected = append(selected, SelectedFrame{
				Kind:               "parsed",
				SessionID:          e.sessionID,
				SnapshotID:         e.snapshotID,
				App:                e.app,
				Label:              label,
				PromptText:         promptText,
				ParsedSiblingCount: siblingCount,
				// e.url comes from pc.URL — empty for non-browser
				// content (Slack conversations / Notion docs); the
				// frontend's hostname extractor will treat empty as
				// "no badge".
				BrowserURL: e.url,
			})
			// Record the dedup siblings (every other snapshot keyed
			// by the same (app, channel/title/url)) as parsed_sibling
			// entries so the Activity timeline can render them with a
			// muted tick. Operators auditing the strategy need to see
			// "these N frames were folded into the winner" — otherwise
			// 5 Slack snapshots show as 1 green tick and 4 invisible,
			// which reads as data loss when it's actually intentional
			// dedup.
			for _, sib := range siblingIDs[gk] {
				if sib.snapshotID == e.snapshotID {
					continue
				}
				selected = append(selected, SelectedFrame{
					Kind:       "parsed_sibling",
					SessionID:  sib.sessionID,
					SnapshotID: sib.snapshotID,
					App:        e.app,
					Label:      label,
					SkipReason: "parsed_dedup_to_winner",
					// Siblings inherit the winner's URL — they were in
					// the same (app, key) dedup group, so by definition
					// they pointed at the same page. Surfacing it lets
					// future UI consumers attribute the sibling without
					// re-reading the snapshot row.
					BrowserURL: e.url,
				})
			}
		}
	}

	// Audit the size-cap drop. Any group in `order` that never rendered
	// represents (a) the size cap firing before its turn, or (b) the
	// renderer producing an empty section despite passing the high-
	// fidelity gate. Both look the same to operators ("this group had
	// content but didn't get into the prompt") so we emit one
	// parsed_skipped record per snapshot — the per-snapshot ticks let
	// the operator see at a glance how much of the day was clipped by
	// the 30 KB budget vs. how much was OCR-only territory.
	for _, gk := range order {
		if renderedGroups[gk] {
			continue
		}
		e := best[gk]
		label := firstNonEmpty(e.channel, e.title, e.url, e.ct)
		for _, sib := range siblingIDs[gk] {
			selected = append(selected, SelectedFrame{
				Kind:       "parsed_skipped",
				SessionID:  sib.sessionID,
				SnapshotID: sib.snapshotID,
				App:        e.app,
				Label:      label,
				SkipReason: "parsed_oversize",
				// Group identity carried through so the audit
				// matches the contract on SelectedFrame.BrowserURL.
				BrowserURL: e.url,
			})
		}
	}

	return b.String(), covered, selected
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// truncateAtRuneBoundary returns the longest prefix of s that is at
// most maxBytes bytes long without splitting a UTF-8 rune. Use this
// for downstream BYTE caps (LLM prompt budgets, on-disk blob limits)
// over content that may include CJK or emoji — a naive s[:maxBytes]
// cut can land inside a multi-byte rune and produce invalid UTF-8.
//
// Caller is responsible for any "truncated" suffix: this helper only
// makes the cut safe, it doesn't annotate it.
func truncateAtRuneBoundary(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	// s[maxBytes] is the byte at which a naive cut would land. Walk
	// back to the most recent UTF-8 lead byte (top two bits != 10).
	n := maxBytes
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

// normalizeOCRForDedup produces a stable key that collapses near-identical
// OCR frames. OCR output for the same screen typically only differs by a few
// trailing characters (a row that was clipped on one frame appears on the
// next) plus whitespace noise. Lower-casing, dropping all whitespace, and
// truncating to a 120-char prefix gives a key that survives those minor
// variations while still distinguishing genuinely different screen contents.
//
// Empty / very short results return "" so callers can treat them as
// non-deduppable (we'd rather over-include a 3-character line than wrongly
// suppress a real signal because it normalized to the same prefix).
func normalizeOCRForDedup(s string) string {
	const prefixLen = 120
	var b strings.Builder
	b.Grow(min(len(s), prefixLen))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', ' ', '　':
			continue
		}
		// Rune-aware lower-case so accented Latin (É/À/Ñ) and other
		// Unicode case pairs collapse correctly. ASCII-only fast-path
		// would miss these and leave near-identical OCR frames as
		// distinct dedup keys.
		r = unicode.ToLower(r)
		b.WriteRune(r)
		if b.Len() >= prefixLen {
			break
		}
	}
	if b.Len() < 16 {
		return ""
	}
	return b.String()
}

// deduplicateEvents collapses consecutive similar events to reduce summary
// size while preserving all event types. For example, 10 consecutive clicks
// in Finder become "Clicked 10 times in Finder". Speech, app switches, and
// keyboard events are always kept individually (they carry unique content).
type dedupedEvent struct {
	evt   activitymon.ActivityEvent
	count int // 1 = singular, >1 = collapsed run
}

func deduplicateEvents(events []activitymon.ActivityEvent) []dedupedEvent {
	if len(events) == 0 {
		return nil
	}

	result := make([]dedupedEvent, 0, len(events))
	result = append(result, dedupedEvent{evt: events[0], count: 1})

	for i := 1; i < len(events); i++ {
		evt := events[i]
		prev := &result[len(result)-1]

		// Collapse consecutive clicks on the same element in the same app.
		// Different elements (Save vs Export) are kept as separate entries.
		if evt.Kind == activitymon.EventClick && prev.evt.Kind == activitymon.EventClick &&
			evt.AppName == prev.evt.AppName &&
			evt.ClickRole == prev.evt.ClickRole && evt.ClickLabel == prev.evt.ClickLabel {
			prev.count++
			prev.evt = evt
			continue
		}

		// Collapse consecutive focus changes on the same element.
		if evt.Kind == activitymon.EventFocus && prev.evt.Kind == activitymon.EventFocus &&
			evt.AppName == prev.evt.AppName &&
			evt.FocusRole == prev.evt.FocusRole && evt.FocusLabel == prev.evt.FocusLabel {
			prev.count++
			prev.evt = evt
			continue
		}

		// Cross-kind collapse: a click on a UI element almost always
		// emits a paired focus event on the same element, so the prompt
		// otherwise renders both as near-duplicate lines ("Clicked X" /
		// "Focused on X") that burn triage tokens without adding
		// signal. Collapse runs of click/focus that share AppName plus
		// a non-empty label match. Keyboard / speech / app-switch
		// stay untouched.
		if (evt.Kind == activitymon.EventClick || evt.Kind == activitymon.EventFocus) &&
			(prev.evt.Kind == activitymon.EventClick || prev.evt.Kind == activitymon.EventFocus) &&
			evt.AppName == prev.evt.AppName {
			prevLabel := prev.evt.ClickLabel
			if prevLabel == "" {
				prevLabel = prev.evt.FocusLabel
			}
			currLabel := evt.ClickLabel
			if currLabel == "" {
				currLabel = evt.FocusLabel
			}
			if prevLabel != "" && prevLabel == currLabel {
				// Promote a focus carrier to its paired click — the
				// prompt branch in buildGroupSummary renders a click
				// line ("Clicked element: <label>") that's strictly
				// more useful than the focus line ("Focused on …"),
				// since clicks imply a deliberate user action.
				if evt.Kind == activitymon.EventClick {
					prev.evt = evt
				}
				prev.count++
				continue
			}
		}

		// Everything else (speech, app switch, keyboard) kept individually.
		result = append(result, dedupedEvent{evt: evt, count: 1})
	}

	return result
}

// buildGroupSummary creates a structured text summary from a session group's
// events. The second return value records every frame that contributed to the
// summary (high-fidelity parsed snapshots + the OCR samples that survived
// dedup and the per-group cap) so callers can persist it on the pipeline run
// row for operator inspection. The third return value is the input profile
// enriched with chat sender stats accumulated while rendering parsed
// content; downstream callers must use this enriched profile so the memory
// pipeline sees per-sender attribution data.
func (c *ActivityTriager) buildGroupSummary(group sessionGroup, events []activitymon.ActivityEvent, profile SessionContentProfile) (string, []SelectedFrame, SessionContentProfile) {
	if !c.triageGroupAllowsModelRead(group) {
		return "", nil, SessionContentProfile{}
	}
	var store *storage.Store
	if c.app != nil {
		store = c.app.store
	}
	labeler := newSenderLabeler(store)

	body, selectedFrames := c.buildGroupSummaryBody(group, redactTriageModelEvents(events), labeler)
	profile.ChatSenders = labeler.Stats()

	var b strings.Builder
	if block := profile.ProfileSummaryBlock(); block != "" {
		b.WriteString(block)
	}
	b.WriteString(body)
	for i := range selectedFrames {
		selectedFrames[i].PromptText = redactActivityModelText(selectedFrames[i].PromptText)
		selectedFrames[i].Label = redactActivityModelText(selectedFrames[i].Label)
		selectedFrames[i].BrowserURL = redactActivityModelText(selectedFrames[i].BrowserURL)
	}
	return redactActivityModelText(b.String()), selectedFrames, profile
}

// buildGroupSummaryBody renders everything except the profile header. Split
// out so buildGroupSummary can enrich the profile (with chat sender stats
// collected during render) before emitting the header at the top.
func (c *ActivityTriager) buildGroupSummaryBody(group sessionGroup, events []activitymon.ActivityEvent, labeler *senderLabeler) (string, []SelectedFrame) {
	var b strings.Builder
	var selectedFrames []SelectedFrame

	appNames := group.allAppNames()
	duration := time.Duration(group.endTime()-group.startTime()) * time.Millisecond

	b.WriteString(fmt.Sprintf("Sessions: %d\n", len(group.Sessions)))
	b.WriteString(fmt.Sprintf("Time span: %s\n", duration.Round(time.Second)))
	b.WriteString(fmt.Sprintf("Applications used: %s\n", formatActivityAppNames(appNames)))
	b.WriteString(fmt.Sprintf("Total events: %d\n\n", group.totalEventCount()))

	// Source attribution preamble: tells the triage LLM that the structured
	// content below comes from multiple apps/windows and should NOT be
	// auto-correlated. Without this, the model treats temporal adjacency
	// (parsed Notes page + OCR'd Slack notification at the same second) as
	// evidence of topical relatedness, producing moments like "the user was
	// thinking about <Slack convo> while writing <Notes page>" when the
	// two are completely independent contexts (rizzotho's "笔记 app 侧边栏
	// 输入框 vs 中间笔记" scenario). The [app=...] tags below carry the
	// per-frame source; this paragraph is the prior on how to read them.
	b.WriteString("Source attribution: each [app=X] tag and ### [X] heading below marks an independent source. " +
		"Material from different sources may be unrelated even when adjacent in time — only assume connections when the content explicitly references the same task, document, or entity.\n\n")

	// Detail key actions with accessibility data.
	// Deduplicate consecutive similar events (e.g., 10 clicks in Finder →
	// one line) to reduce summary size while preserving all event types.
	// Cap total summary text at 30 KB to prevent AI triage timeouts.
	const maxSummaryBytes = 200_000 // 200 KB — context windows are large and growing; let the model see everything

	dedupedEvents := deduplicateEvents(events)

	b.WriteString("Activity timeline:\n")

	var typingBuf strings.Builder
	var lastTypingApp string

	flushTyping := func() {
		if typingBuf.Len() > 0 {
			// Rune-aware truncation: byte slicing here would split CJK
			// IME output mid-rune and emit invalid UTF-8 into the LLM
			// prompt — the typing buffer is exactly where Chinese
			// users hit this every keystroke.
			text := truncateForFocusedAppPrompt(redactActivityModelText(typingBuf.String()), 200)
			b.WriteString(fmt.Sprintf("- Typed in %s: %q\n", lastTypingApp, text))
			typingBuf.Reset()
		}
	}

	for _, de := range dedupedEvents {

		evt := de.evt
		switch evt.Kind {
		case activitymon.EventAppSwitch:
			flushTyping()
			b.WriteString(fmt.Sprintf("- Switched to %s (%s)\n", evt.AppName, evt.WindowTitle))
		case activitymon.EventClick:
			flushTyping()
			if de.count > 1 {
				b.WriteString(fmt.Sprintf("- Clicked %d times in %s", de.count, evt.AppName))
				// Include the last click's label for context.
				if evt.ClickRole != "" || evt.ClickLabel != "" {
					label := evt.ClickLabel
					if label == "" {
						label = evt.ClickRole
					}
					b.WriteString(fmt.Sprintf(" (last: %s)", label))
				}
				b.WriteByte('\n')
			} else if evt.ClickRole != "" || evt.ClickLabel != "" {
				role := evt.ClickRole
				if role == "" {
					role = "element"
				}
				label := evt.ClickLabel
				if label == "" {
					label = "(unlabeled)"
				}
				b.WriteString(fmt.Sprintf("- Clicked %s: %s in %s\n", role, label, evt.AppName))
			} else {
				b.WriteString(fmt.Sprintf("- Clicked at (%.0f, %.0f) in %s\n", evt.ClickX, evt.ClickY, evt.AppName))
			}
		case activitymon.EventKeyboard:
			if typed := strings.TrimSpace(evt.TypedText); typed != "" {
				// Committed text recovered from focused-field diffs — the
				// real input, IME-composed. Prefer it over raw keystrokes.
				if lastTypingApp != evt.AppName {
					flushTyping()
					lastTypingApp = evt.AppName
				}
				typingBuf.WriteString(typed)
			} else if kcs := evt.GetKeyChars(); len(kcs) > 0 && !evt.IMEComposing {
				// Raw keystrokes under a composing IME are pinyin/kana
				// noise, not literal text — drop them rather than feed the
				// model a misleading "typed" line.
				if lastTypingApp != evt.AppName {
					flushTyping()
					lastTypingApp = evt.AppName
				}
				for _, kc := range kcs {
					typingBuf.WriteString(kc)
				}
			}
		case activitymon.EventFocus:
			flushTyping()
			if evt.FocusRole != "" {
				if de.count > 1 {
					b.WriteString(fmt.Sprintf("- Focused on %d elements in %s (last: %s)\n", de.count, evt.AppName, evt.FocusLabel))
				} else {
					b.WriteString(fmt.Sprintf("- Focused on %s: %s\n", evt.FocusRole, evt.FocusLabel))
				}
			}
		case activitymon.EventClipboard:
			// A copy is a strong sign of intent: one line, nothing more.
			flushTyping()
			b.WriteString(clipboardTimelineLine(evt))
		case activitymon.EventSpeech:
			flushTyping()
			if evt.SpeechText != "" {
				text := evt.SpeechText
				// For meeting sessions, preserve full speech text for
				// richer AI triage context. Truncate only for non-meetings.
				// Use rune-aware helper so CJK speech transcripts don't
				// get sliced mid-character.
				if !group.hasMeetingSessions() {
					text = truncateForFocusedAppPrompt(text, 300)
				}
				// Rendering needs to distinguish three cases so the triage
				// LLM doesn't credit the wrong person:
				//   1. Meeting + system_audio: this is by definition the
				//      remote/output side, i.e. other participants — never
				//      the local user. Render as "Other meeting participant
				//      audio" so the LLM treats it as third-party speech.
				//   2. Outside a meeting + isLikelyNonUserSpeech: speaker
				//      output bleeding into the mic (videos, music, remote
				//      audio). Render as ambient.
				//   3. Everything else (mic in a meeting, mic outside
				//      meeting with no echo markers): the local user.
				inMeeting := group.hasMeetingSessions()
				isSystemAudio := evt.SpeechSource == "system_audio"
				switch {
				case inMeeting && isSystemAudio:
					if evt.AppName != "" && evt.AppName != "Background audio" {
						b.WriteString(fmt.Sprintf("- Other meeting participant audio (in %s, NOT the user's voice): %q\n", evt.AppName, text))
					} else {
						b.WriteString(fmt.Sprintf("- Other meeting participant audio (NOT the user's voice): %q\n", text))
					}
				case evt.SpeechDictationApp != "" && !isSystemAudio:
					b.WriteString(fmt.Sprintf("- Dictated to %s (the user's own words to that app, not a conversation): %q\n", evt.SpeechDictationApp, text))
				case !inMeeting && isLikelyNonUserSpeech(evt):
					if evt.AppName != "" && evt.AppName != "Background audio" {
						b.WriteString(fmt.Sprintf("- Ambient audio heard near %s (likely speaker/video output, NOT the user's voice): %q\n", evt.AppName, text))
					} else {
						b.WriteString(fmt.Sprintf("- Ambient audio (likely speaker/video output, NOT the user's voice): %q\n", text))
					}
				default:
					if evt.AppName != "" && evt.AppName != "Background audio" {
						b.WriteString(fmt.Sprintf("- Spoke (in %s): %q\n", evt.AppName, text))
					} else {
						b.WriteString(fmt.Sprintf("- Spoke: %q\n", text))
					}
				}
			}
		}
	}
	flushTyping()

	// App-specific structured content. Electron apps (Slack, Discord,
	// 微信, Notion, ChatGPT, etc.) reduce to OCR-noise on screen but
	// expose clean structured data via accessibility — the parser path
	// captures sender/timestamp/channel/messages losslessly while OCR
	// of the same frames returns "+AaO@ • Toby" gibberish.
	// renderParsedContentSection emits a "## App-specific structured
	// content" section AND returns the snapshot IDs whose parsed payload
	// was high-fidelity enough to render — those IDs are then excluded
	// from the OCR sampling pass below so we don't burn the OCR budget
	// re-describing the same frames in worse form.
	parsedSection, parsedCoveredSnapshotIDs, parsedFrames := c.renderParsedContentSection(group, labeler)
	if parsedSection != "" {
		b.WriteString(parsedSection)
	}
	selectedFrames = append(selectedFrames, parsedFrames...)

	// Enrich with OCR text samples for richer AI context. Walk the
	// frames in chronological order and skip ones whose normalized
	// prefix already appeared in this group — when a user stares at
	// the same screen for a while OCR captures the same text many
	// times, and the existing "first 5 frames" cap simply burned
	// triage tokens on copies. The dedup runs per group (not per
	// session) so the same screen viewed across two adjacent sessions
	// in a merged group also collapses.
	//
	// Per-frame parsed-vs-OCR replacement: skip frames whose snapshot
	// ID was already covered by the high-fidelity parsed section above.
	// Frames without a parsed counterpart (Terminal, Finder, native
	// macOS apps, PDFs, design tools, secondary-display content) still
	// flow through OCR — both signals coexist, the parsed signal just
	// pre-empts the OCR sample for snapshots where it's strictly
	// higher quality.
	if c.app.activity != nil && c.app.activity.store != nil {
		// Total OCR samples per group. normalizeOCRForDedup + the parser-
		// coverage filter above already drop most near-identical frames,
		// so this cap is a runaway-case bound rather than a tight ceiling.
		// 12 ≈ 2-3 KB of prompt text at 200 chars/frame, well under 1% of
		// maxSummaryBytes (200 KB). The previous value of 5 was carried
		// over from when the cap was per-session — once we collapsed the
		// budget to per-group, a long group spanning several parser-miss
		// apps (Terminal, IDE, PDF, Finder) was starved of OCR samples
		// even though prefix dedup had plenty of unique frames to offer.
		const ocrSamplesPerGroup = 12
		// Bound how many ocr_skipped audit entries we keep per group so
		// pathological screens (e.g. an idle desktop with 5000 near-
		// identical OCR frames) can't bloat selected_frames JSON. The
		// operator only needs enough skip samples to see the pattern;
		// once you've seen 20 "prefix_dedup" entries you know dedup is
		// firing.
		const maxOCRSkipsPerReason = 20
		// seenOCRPrefixes was a set in P0; in P1 it counts hits so
		// ocr_prefix_dedup skip records can carry "you're the Nth
		// frame with this text" as a saturation signal. Zero in the
		// map means "first occurrence" (the winner); positive means
		// "this many prior frames already had this prefix".
		seenOCRPrefixes := make(map[string]int)
		ocrLines := make([]string, 0, ocrSamplesPerGroup)
		skipCounts := make(map[string]int, 4)
		recordOCRSkip := func(reason string, frame OCRFrameRow, snapshotID int64, sessionID string, snap *ActivitySnapshotRow, chainLen int) {
			if skipCounts[reason] >= maxOCRSkipsPerReason {
				return
			}
			skipCounts[reason]++
			f := SelectedFrame{
				Kind:          "ocr_skipped",
				SessionID:     sessionID,
				SnapshotID:    snapshotID,
				FrameTime:     frame.FrameTime,
				Label:         truncateForFocusedAppPrompt(strings.TrimSpace(frame.Text), 80),
				SkipReason:    reason,
				OCRConfidence: frame.Confidence,
				DedupChainLen: chainLen,
			}
			if snap != nil {
				f.BrowserURL = snap.BrowserURL
			}
			selectedFrames = append(selectedFrames, f)
		}
		// Per-session caches so we resolve OCR frame_time → snapshot.id without
		// re-querying snapshots/segments for every OCR row. Required because
		// OCRFrameRow only carries the video segment id; the operator-facing
		// audit view (and any other consumer of SelectedFrame.SnapshotID) needs
		// the activity_snapshots primary key to render a tick on the snapshot
		// timeline.
		sessionSnapshotCache := make(map[string][]ActivitySnapshotRow)
		sessionSegmentByID := make(map[string]map[int64]ActivitySegmentRow)
		// Snapshot id → row, built lazily per session so the winner /
		// skip records can pull BrowserURL without a linear scan on
		// every emit. Map population pairs with sessionSnapshotCache.
		sessionSnapshotByID := make(map[string]map[int64]ActivitySnapshotRow)
		for _, s := range group.Sessions {
			// Stop touching the DB once BOTH budgets are exhausted —
			// the line budget is full and we've collected enough
			// over-cap audit samples to show operators that dedup ran.
			// Iterating remaining sessions just to record more skips
			// wastes I/O without buying any extra observability.
			if len(ocrLines) >= ocrSamplesPerGroup && skipCounts["ocr_over_cap"] >= maxOCRSkipsPerReason {
				break
			}
			ocrFrames, err := c.app.activity.store.GetOCRFramesBySession(s.ID)
			if err != nil || len(ocrFrames) == 0 {
				continue
			}
			for _, f := range ocrFrames {
				f.Text = redactActivityModelText(f.Text)
				trimmed := strings.TrimSpace(f.Text)
				if trimmed == "" {
					continue
				}
				// Resolve the OCR frame to its nearest activity_snapshots row
				// FIRST. We need the real snapshot id for two things: the
				// parsedCoveredSnapshotIDs lookup right below (which is keyed
				// by snapshot.id, while f.SegmentID is overloaded and can
				// hold either snapshot.id or segment.id — see OCRFrameRow
				// doc), and the SelectedFrame audit record. Pre-resolution
				// the coverage check silently misfired in both directions on
				// legacy segment-based rows.
				snaps, ok := sessionSnapshotCache[s.ID]
				if !ok {
					snaps, _ = c.app.activity.store.GetSessionSnapshots(s.ID)
					sessionSnapshotCache[s.ID] = snaps
					byID := make(map[int64]ActivitySnapshotRow, len(snaps))
					for _, sn := range snaps {
						byID[sn.ID] = sn
					}
					sessionSnapshotByID[s.ID] = byID
				}
				segIdx, ok := sessionSegmentByID[s.ID]
				if !ok {
					segs, _ := c.app.activity.store.GetSessionSegments(s.ID)
					segIdx = make(map[int64]ActivitySegmentRow, len(segs))
					for _, seg := range segs {
						segIdx[seg.ID] = seg
					}
					sessionSegmentByID[s.ID] = segIdx
				}
				// OCR frames have two producers (see activity_ocr.go):
				//   - snapshot-based path: SegmentID *is* snapshot.id (the
				//     schema column is reused; the OCR ran directly against
				//     the JPEG snapshot)
				//   - legacy segment-based path: SegmentID points at
				//     activity_segments and FrameTime is a seconds offset
				//     into the video segment
				// Try the snapshot interpretation first (free, exact match);
				// fall back to the segment interpretation with a tight
				// nearest-snapshot search bounded by 3s so OCR frames that
				// don't have a close snapshot don't surface as a misleading
				// edge tick on the timeline.
				const maxOCRMatchDeltaMS = 3000
				snapshotID := int64(0)
				var matchedSnap ActivitySnapshotRow
				for _, sn := range snaps {
					if sn.ID == f.SegmentID {
						snapshotID = sn.ID
						matchedSnap = sn
						break
					}
				}
				if snapshotID == 0 {
					if seg, ok := segIdx[f.SegmentID]; ok && len(snaps) > 0 {
						targetMS := seg.StartTime + int64(f.FrameTime*1000)
						best := snaps[0]
						bestDelta := targetMS - best.Timestamp
						if bestDelta < 0 {
							bestDelta = -bestDelta
						}
						for _, sn := range snaps[1:] {
							d := targetMS - sn.Timestamp
							if d < 0 {
								d = -d
							}
							if d < bestDelta {
								best = sn
								bestDelta = d
							}
						}
						if bestDelta <= maxOCRMatchDeltaMS {
							snapshotID = best.ID
							matchedSnap = best
						}
					}
				}
				// Resolve to the actual snapshot row (if any) so winner
				// and skip records can carry BrowserURL. Nil snapPtr is
				// fine — the helpers and emit paths treat it as "no
				// browser context to surface".
				var snapPtr *ActivitySnapshotRow
				if snapshotID != 0 {
					if sn, ok := sessionSnapshotByID[s.ID][snapshotID]; ok {
						snapPtr = &sn
					}
				}
				// Now that snapshotID is in the same space as
				// parsedCoveredSnapshotIDs (snapshot.id), the coverage
				// filter is sound. Only skip when we're sure: if the OCR
				// frame couldn't be mapped (snapshotID==0), keep it — we
				// don't know what it would have collided with.
				if snapshotID != 0 && parsedCoveredSnapshotIDs[snapshotID] {
					recordOCRSkip("ocr_covered_by_parsed", f, snapshotID, s.ID, snapPtr, 0)
					continue
				}
				// Resolve the source app tag BEFORE deduping. Two
				// different apps occasionally produce overlapping OCR
				// text (a status line, a copied snippet) — folding both
				// into one entry would erase the "saw the same content
				// in two contexts" signal, which is exactly what source
				// attribution exists to preserve. snap.App is the parser
				// short key ("slack", "notion"); empty for non-parsed
				// apps (Terminal, Finder, IDE). Fall back to "unknown"
				// rather than the session's app set, because the group
				// app names are too coarse to attribute a per-frame
				// sample (a 2-app group would lie half the time).
				appTag := matchedSnap.App
				if appTag == "" {
					appTag = "unknown"
				}
				// Per normalizeOCRForDedup's contract, key == "" means the
				// frame is too short to dedup safely (prefix collisions are
				// likely to drop genuine signals like button labels or
				// status text). Honor that: include the sample but skip the
				// dedup map. Only frames with a meaningful key get checked
				// against — and added to — seenOCRPrefixes. The appTag
				// prefix scopes dedup to per-source so cross-source
				// repeats survive (see comment above).
				key := normalizeOCRForDedup(f.Text)
				if key != "" {
					// appTag prefix scopes dedup per-source (from main):
					// cross-source repeats stay distinct so the same
					// "Submit" button rendered in different apps each
					// gets a chance to surface.
					key = appTag + ":" + key
					if priorHits := seenOCRPrefixes[key]; priorHits > 0 {
						// priorHits is "how many earlier frames already
						// shared this prefix" — N=1 means a single
						// near-duplicate just before; N=20 means the
						// user stared at this screen for minutes.
						recordOCRSkip("ocr_prefix_dedup", f, snapshotID, s.ID, snapPtr, priorHits)
						seenOCRPrefixes[key] = priorHits + 1
						continue
					}
					seenOCRPrefixes[key] = 1
				}
				// Cap check moved here (was an early break above): a
				// frame that survived the coverage and dedup gates is
				// genuine signal, so its drop is meaningful audit
				// information rather than "we never looked at it".
				// Record-and-continue lets the inner loop keep walking
				// the session's frames; the outer session loop's
				// short-circuit takes over once skip budget is also
				// full so we don't iterate forever. Done before the
				// text flatten below so a dropped frame doesn't pay
				// the string allocation.
				if len(ocrLines) >= ocrSamplesPerGroup {
					recordOCRSkip("ocr_over_cap", f, snapshotID, s.ID, snapPtr, 0)
					continue
				}
				// Vision returns multiple text observations per frame
				// joined by '\n' (see recognizeBackgroundOCRText). Without
				// flattening, the prompt would emit one "- [t] [app=X]"
				// bullet whose body straddles several real newlines, and
				// every line past the first would render in the LLM input
				// without any source tag — silently defeating the
				// attribution upstream added. Use "; " so distinct UI
				// elements (button labels, list items, wrapped text rows)
				// stay visually separated within the single bullet
				// instead of merging into one run-on string.
				flat := strings.ReplaceAll(strings.TrimSpace(f.Text), "\n", "; ")
				// Rune-aware truncation: OCR output frequently contains
				// CJK / emoji that byte slicing would split mid-rune.
				text := truncateForFocusedAppPrompt(flat, 200)
				promptLine := fmt.Sprintf("- [%.1fs] [app=%s] %s\n", f.FrameTime, appTag, text)
				ocrLines = append(ocrLines, promptLine)
				// Record the frame so operators can inspect exactly which
				// OCR samples drove the triage. Use a short preview (80
				// chars) as the label — full text already lives in the
				// rendered prompt and stuffing it here would bloat the
				// row pointlessly.
				winner := SelectedFrame{
					Kind:          "ocr",
					SessionID:     s.ID,
					SnapshotID:    snapshotID,
					FrameTime:     f.FrameTime,
					Label:         truncateForFocusedAppPrompt(trimmed, 80),
					PromptText:    strings.TrimSpace(promptLine),
					OCRConfidence: f.Confidence,
				}
				if snapPtr != nil {
					winner.BrowserURL = snapPtr.BrowserURL
				}
				selectedFrames = append(selectedFrames, winner)
			}
		}
		if len(ocrLines) > 0 {
			b.WriteString("\nScreen text samples (frames not covered by structured parser above):\n")
			for _, line := range ocrLines {
				b.WriteString(line)
			}
		}
	}

	// If the rendered summary exceeds the size cap, keep the head and tail
	// (beginning and end of the session) and truncate the middle. The start
	// shows what the user began doing; the end shows what they concluded with.
	// Redact complete fields before the head/tail cap can split a credential
	// into fragments that no longer match the redaction patterns.
	summary := redactActivityModelText(b.String())
	if len(summary) > maxSummaryBytes {
		tailBudget := maxSummaryBytes / 5                // 20% for the tail
		headBudget := maxSummaryBytes - tailBudget - 200 // room for the marker

		// Find a clean line break near the head boundary.
		headEnd := headBudget
		if idx := strings.LastIndex(summary[:headEnd], "\n"); idx > headBudget/2 {
			headEnd = idx + 1
		}
		// Find a clean line break near the tail boundary.
		tailStart := len(summary) - tailBudget
		if idx := strings.Index(summary[tailStart:], "\n"); idx >= 0 && idx < tailBudget/2 {
			tailStart += idx + 1
		}

		// UTF-8 safety: when the line-break search above found a clean
		// '\n' the cut already lands on a rune boundary (\n is ASCII),
		// but the fallback (no nearby newline — e.g. a 50 KB CJK
		// dedup-section line, or an unbroken speech transcript) leaves
		// a raw byte index that may split a multi-byte rune. Snap
		// headEnd back / tailStart forward to the nearest UTF-8 lead
		// byte so the resulting prompt is never invalid UTF-8.
		for headEnd > 0 && (summary[headEnd]&0xC0) == 0x80 {
			headEnd--
		}
		for tailStart < len(summary) && (summary[tailStart]&0xC0) == 0x80 {
			tailStart++
		}

		omittedBytes := tailStart - headEnd
		summary = summary[:headEnd] +
			fmt.Sprintf("\n  ... (%d bytes of mid-session activity omitted) ...\n\n", omittedBytes) +
			summary[tailStart:]
	}

	return summary, selectedFrames
}

// runActivityTriage calls AI to evaluate whether an activity session is worth keeping.
// Returns the parsed response, the raw response text (always populated when the
// AI call completes, even on parse failure), the per-feature flag set used to
// build the prompt (so callers can distinguish "AI did not evaluate this axis"
// from "AI evaluated and rejected"), the assembled prompt text (for the
// diagnostic UI; empty when no prompt was built — runner-not-ready or
// all-features-disabled), and any error.
func (c *ActivityTriager) runActivityTriage(ctx context.Context, summary, customInstruction string) (*activityTriageResponse, string, triageFeatureFlags, string, error) {
	settings := c.app.loadTriageFeatureFlags(ctx)
	flags := activitySummaryTriageFlags(settings)

	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()

	// Decide whether to skip / fail / proceed BEFORE doing any work.
	// Critically, the settings-skip check must precede the runner check —
	// otherwise users who disabled every triage output would still see
	// retryable "runner not initialized" errors during startup windows
	// (before StartAgent assigns runner), feeding the rotator's cooldown
	// + retry loop. See triagePreflight comment for details.
	switch skip, perr := triagePreflight(flags, runner != nil); {
	case perr != nil:
		return nil, "", flags, "", perr
	case skip:
		log.Printf("[triager] activity triage skipped — all features disabled in settings")
		return &activityTriageResponse{}, "", flags, "", nil
	}

	// Only load dedup sections for enabled features. Each one can run up
	// to 50 KB (the cap below); skipping the ones the user disabled is the
	// whole point of the toggle — it is also why a daily-50-trigger user
	// can save real money over a month.
	const maxDedupSectionBytes = 50_000 // 50 KB per section — full dedup context helps the model avoid duplicates
	// Memory, Knowledge, and Playbook dedup sections frequently contain CJK
	// content. Naive byte-slicing at 50 KB would
	// land inside a multi-byte rune ~2/3 of the time and emit invalid
	// UTF-8 into the prompt. truncateAtRuneBoundary keeps the byte cap
	// while guaranteeing every cut falls on a rune boundary.
	var existingMemories, existingKnowledge, sopDedup string
	if flags.Memory {
		// Use the same privacy-filtered snapshots as memory generation;
		// hidden titles must not enter an earlier model prompt either.
		existingMemories, _ = loadMemoryGenerationContextForQuery(ctx, c.app, summary, maxDedupSectionBytes)
		if len(existingMemories) > maxDedupSectionBytes {
			existingMemories = truncateAtRuneBoundary(existingMemories, maxDedupSectionBytes) + "\n... (truncated)\n"
		}
	}
	if flags.Knowledge {
		existingKnowledge = c.app.loadRelevantKnowledgeSummaryWithBudget(c.app.memoryStore, summary, maxDedupSectionBytes)
		if len(existingKnowledge) > maxDedupSectionBytes {
			existingKnowledge = truncateAtRuneBoundary(existingKnowledge, maxDedupSectionBytes) + "\n... (truncated)\n"
		}
	}
	if flags.SOP {
		sopDedup = c.app.buildSOPDedupSection()
		if len(sopDedup) > maxDedupSectionBytes {
			sopDedup = truncateAtRuneBoundary(sopDedup, maxDedupSectionBytes) + "\n... (truncated)\n"
		}
	}

	languageInstruction := responseLanguageInstruction(c.app.currentUILanguageName())
	prompt := redactActivityModelText(buildTriagePrompt(flags, summary, languageInstruction, existingMemories, existingKnowledge, "", "", sopDedup, customInstruction))

	var result strings.Builder

	log.Printf("[triager] activity triage ExecuteTask starting (flags M=%v K=%v S=%v A=%v H=%v SOP=%v, prompt %d bytes)",
		flags.Memory, flags.Knowledge, flags.Shadow, flags.Automation, flags.Handoff, flags.SOP, len(prompt))
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            redactActivityModelText(prompt),
		Model:             c.app.lightTaskModel(),
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
		case "error":
			log.Printf("[triager] triage stream error: %s", activityPrivateLogField("error", event.Error))
		default:
			log.Printf("[triager] activity triage event: type=%s", event.Type)
		}
		return nil
	})
	if err != nil {
		log.Printf("[triager] activity triage ExecuteTask failed: %s", activityPrivateLogField("error", err.Error()))
		return nil, result.String(), flags, prompt, fmt.Errorf("execute triage: %w", err)
	}
	log.Printf("[triager] activity triage ExecuteTask completed, response length=%d", result.Len())

	raw := strings.TrimSpace(result.String())
	raw = stripCodeFences(raw)

	var resp activityTriageResponse
	if err := unmarshalJSONBestEffort(raw, &resp); err != nil {
		return nil, raw, flags, prompt, fmt.Errorf("parse triage response: %w", err)
	}

	// Defense in depth: zero out fields the user has disabled, in case the
	// model emitted them anyway despite the prompt omitting their sections.
	applyTriageFeatureFlags(&resp, flags)

	return &resp, raw, flags, prompt, nil
}

// runKnowledgeGeneration uses a stronger model to extract high-quality knowledge
// from an activity summary. This is the "generation" phase, analogous to the memory generation phase.
func (c *ActivityTriager) runKnowledgeGeneration(ctx context.Context, summary string) (*knowledgeGenerationResponse, error) {
	c.app.mu.Lock()
	runner := c.app.runner
	c.app.mu.Unlock()

	if runner == nil {
		return nil, fmt.Errorf("runner not initialized")
	}

	// Load existing knowledge for dedup context
	provenance := newKnowledgeGenerationProvenance()
	existingKnowledge := c.app.loadModelDedupSummaryWithCapture(nil, "", 100_000, 0, MemorySourceKnowledge, provenance.addNote)
	var existingSection string
	if existingKnowledge != "" {
		existingSection = fmt.Sprintf(`
## Existing knowledge entries (IMPORTANT — check for duplicates!)

The following knowledge entries already exist. Before creating a new entry:
1. If the new data is essentially the SAME as an existing entry (same topic, same insights), return action="skip".
2. If the new data UPDATES or EXPANDS an existing entry (same topic but new/changed details), return action="update" with the updateId set to the existing entry's id.
3. Only return action="create" if this is genuinely NEW information not covered by any existing entry.

%s`, existingKnowledge)
	}

	languageInstruction := responseLanguageInstruction(c.app.currentUILanguageName())
	prompt := fmt.Sprintf(`%s

Extract reusable knowledge from this activity session. If none exists, return an empty array — that is the expected outcome for most sessions.

Activity summary:
---
%s
---
%s
Respond with ONLY a JSON object (no markdown, no code fences):
{
  "action": "create|update|skip",
  "kind": "decision|how_to|fix|gotcha|reference",
  "updateId": "existing-entry-id-if-updating",
  "title": "concise title summarizing the knowledge",
  "description": "brief context about what led to this knowledge",
  "knowledgePoints": ["knowledge point 1", "knowledge point 2"]
}

## kind field rules:

Decision rationale -> decision; process knowledge -> how_to; failure patterns and technical insights pairing an error with its workaround -> fix; environment/infra quirks and vendor/API gotchas -> gotcha; people/org, domain/business and other technical facts -> reference. Always return one valid kind. For updates keep the existing kind unless the content warrants a different valid kind.

## action field rules:

- "skip": The data duplicates existing knowledge with no new information. Return empty knowledgePoints.
- "update": The data relates to an existing knowledge entry but has new or updated details. Set updateId to the id of the entry to update (from the [id=xxx] tag in existing knowledge). Return the COMPLETE updated knowledgePoints (merged old + new).
- "create": The data contains genuinely NEW knowledge not covered by any existing entry.

## What is knowledge?

Knowledge is NOT a diary, NOT a memory, NOT a summary of what happened. It is a reusable, atomic, valuable insight that someone can APPLY in a future situation.

Think of knowledge as a reference card: when someone encounters a specific problem in the future, they search their knowledge base, find this card, and it directly helps them solve or avoid the problem.

Three mandatory properties:

1. **Reusable** — The insight can be applied by ANYONE, at ANY TIME, in a SIMILAR situation. It is decoupled from this specific session. If it only makes sense in the context of "what user X did on date Y", it is a diary entry, not knowledge.

2. **Atomic** — One point = one discrete insight. Not a paragraph summarizing a workflow. Not "the user did A, B, and C." Each point must stand alone as a single, self-contained fact that can be independently searched, retrieved, and applied.

3. **Valuable** — Reading this point changes what someone DOES. It reveals a non-obvious mechanism, a specific gotcha, a concrete solution, or an exact technique. If it states something everyone already knows ("flexible config is good", "retry patterns help"), it has zero value.

## The litmus test

For each candidate, ask: "If I put this on a reference card, would someone ever pull it out and use it to solve a real problem?"

- YES → keep it.
- NO → it's a diary entry or a truism. Discard.

## What knowledge is NOT

- ❌ Activity summary: "User conducted a workflow spanning multiple applications"
- ❌ Generic advice: "Repository tools benefit from flexible configuration"
- ❌ Architecture description: "Two-phase pipelines separate collection from filtering"
- ❌ Feature requirement: "Permission dialogs require audit trail logging"
- ❌ Vague observation: "Release automation can fail due to version-related issues"

These all fail because they are NOT reusable (too vague to apply), NOT atomic (too broad), or NOT valuable (no one would look this up to solve a problem).

## What knowledge IS

Externally-anchored knowledge (publicly searchable technical gotchas):

- ✅ "golangci-lint v1.55+ requires Go 1.21 minimum; running on Go 1.20 produces a misleading 'no Go files' error, not a version mismatch error"
  → Reusable: anyone hitting this error benefits. Atomic: one fact. Valuable: saves debugging time.

- ✅ "In Tailwind, bg-black/10 on dark-900 backgrounds is invisible; use bg-white/5 for dark-mode card overlays"
  → Reusable: any Tailwind dark-mode project. Atomic: one specific fix. Valuable: non-obvious.

- ✅ "macOS Accessibility API returns AXRole='AXWebArea' for all Electron BrowserWindow content; use AXDOMIdentifier instead for per-element identification"
  → Reusable: any macOS accessibility integration. Atomic: one API behavior. Valuable: the workaround.

Locally-anchored knowledge (project / team / tool-specific facts — EQUALLY valid):

- ✅ "cmux workspaces are stored under '~/.cmux/workspaces/<name>'; killing the parent tmux session does NOT delete the workspace directory"
  → Reusable: anyone debugging cmux state on this machine. Atomic: one observable fact. Valuable: the location is not in any public doc.

- ✅ "Isoform activity pipeline dedupes memory by title similarity, not by session ID; rewording a title creates a new memory for the same underlying session"
  → Reusable: anyone editing the memory pipeline. Atomic: one dedup rule. Valuable: non-obvious invariant.

- ✅ "This project uses squash merge for PRs + 'git branch -D' for post-merge cleanup — NOT merge commits + remote branch deletion"
  → Reusable: any contributor to this repo. Atomic: one convention. Valuable: prevents wrong merge style.

Notice the pattern: every valid knowledge point contains a **specific anchor** that pins it to reality. Legal anchor forms include — version number, CSS class, API name, error message, command flag (external) OR project path, repo convention, observed tool artifact, specific PR/file/config location, internal-term dedup rule (local). Both families count; the requirement is "specific and quotable", not "publicly Google-able".

## Title rules

The title names the KNOWLEDGE TOPIC, not what the user did.
❌ "Knowledge synthesis patterns and multi-project configuration management"
❌ "Release automation diagnostics and monitoring configuration"
✅ "golangci-lint Go version mismatch produces misleading error"
✅ "Tailwind dark-mode card overlay opacity values"

## Output rules

- Each point MUST contain at least one specific anchor — external (tool name, version, API, error message, config key, CSS class, command flag, code pattern) OR local (project path, repo convention, PR/file name, observed tool artifact, internal-term rule). Both count; the requirement is "specific and quotable", not "publicly searchable".
- If you cannot quote a specific anchor from the activity data to justify the point, discard it.
- One genuine insight is better than five mediocre observations.
- NEVER pad output with generic advice, summaries, or truisms.
- The opening "return empty knowledgePoints for most sessions" guidance still applies — the quality bar is high, just not externally-biased.`, languageInstruction, summary, existingSection)

	var result strings.Builder

	// Chat-tier model (stronger than the light tier, cheaper than code tier)
	log.Printf("[triager] knowledge generation ExecuteTask starting")
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            redactActivityModelText(prompt),
		Model:             claudeCodeModelConfig().ChatModel,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		switch event.Type {
		case "assistant":
			result.WriteString(event.Content)
		case "error":
			log.Printf("[triager] knowledge generation stream error: %s", activityPrivateLogField("error", event.Error))
		default:
			log.Printf("[triager] knowledge generation event: type=%s", event.Type)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("execute knowledge generation: %w", err)
	}
	log.Printf("[triager] knowledge generation ExecuteTask completed, response length=%d", result.Len())

	raw := strings.TrimSpace(result.String())
	raw = stripCodeFences(raw)

	var resp knowledgeGenerationResponse
	if err := unmarshalJSONBestEffort(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse knowledge generation response: %w", err)
	}

	resp.provenance = provenance
	return &resp, nil
}

func activityGroupMemoryProvenance(group sessionGroup) memoryReadProvenance {
	ids := sessionIDsFromGroup(group)
	return memoryReadProvenance{Version: 1, Kinds: []string{"activity"}, Sessions: ids, Withheld: len(ids) == 0}
}

func activityMemoryWithProvenance(id, content string, p memoryReadProvenance, apps, evidence []string) string {
	content = memoryWithResolvedProvenance(id, content, p)
	doc, _ := parseOKFDocument(content)
	doc.Frontmatter["area"] = classifyMemoryArea(memoryAreaWords(content), okfString(doc.Frontmatter, "dimension"))
	doc.Frontmatter["activity_sessions"] = p.Sessions
	doc.Frontmatter["evidence_sessions"] = uniqueNonEmpty(evidence, strings.TrimSpace)
	doc.Frontmatter["apps"] = compactStrings(apps)
	return renderOKFDocument(doc.Frontmatter, doc.Body)
}
