package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

// Background work reports into the main chat. Each report is one `system`
// stream item on the main session: Content carries a one-sentence English
// line for the phone and older renderers, ToolName marks it as a report, and
// ToolInput carries the structured payload the desktop renders as a quiet
// card with an optional action (Review, Open, Needs you).

// mainChatReportTool is the ToolName of every report item.
const mainChatReportTool = "main_chat_report"

// mainChatReportDedupeWindow is how many recent main-chat items are scanned
// for a report with the same key before a new one is appended.
const mainChatReportDedupeWindow = 200

// Report actions: the one button the card shows.
const (
	mainChatActionReview   = "review"
	mainChatActionOpen     = "open"
	mainChatActionNeedsYou = "needs_you"
)

// Report sources: which background path produced the line.
const (
	mainChatSourceHandoff     = "handoff"
	mainChatSourceSchedule    = "schedule"
	mainChatSourceMeeting     = "meeting" // older "meeting notes are ready" lines; recordings no longer post
	mainChatSourceEngagement  = "engagement"
	mainChatSourceSideChat    = "side_chat"
	mainChatSourceMessageSend = "message_send"
)

// Target kinds the desktop knows how to open.
const (
	mainChatTargetSession    = "session"
	mainChatTargetHandoff    = "handoff"
	mainChatTargetKnowledge  = "knowledge"
	mainChatTargetAutomation = "automation"
	mainChatTargetToday      = "today"
	mainChatTargetUpcoming   = "upcoming"
	// mainChatTargetThread opens an activity thread in the Activity page's
	// "What you worked on", where the thread outcome was observed.
	mainChatTargetThread = "thread"
	mainChatTargetNone   = "none"
)

// mainChatTarget is what the card's action opens.
type mainChatTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	// RunID narrows an automation target to one run.
	RunID string `json:"runId,omitempty"`
}

// mainChatReport is the structured payload stored in ToolInput.
type mainChatReport struct {
	Routine  *bool                 `json:"routine,omitempty"`
	Evidence []mainChatEvidenceRef `json:"evidence,omitempty"`
	Source   string                `json:"source"`
	// Key dedupes repeats of the same event (a retried completion, a
	// re-delivered moment). Reports with an empty key are never deduped.
	Key    string         `json:"key,omitempty"`
	Action string         `json:"action"`
	Target mainChatTarget `json:"target"`
	// Title is the subject the desktop sentence is built around: the
	// handoff title, the schedule name, the meeting app, the side chat label.
	Title string `json:"title,omitempty"`
	// Status refines the sentence: completed, needs_review, aborted,
	// blocked, ok, error, ready; for a change made from the main chat to an
	// Upcoming item, changed, paused, resumed, started or removed.
	Status string `json:"status,omitempty"`
	// Summary is an optional second line (a side chat's bring-back summary).
	Summary string `json:"summary,omitempty"`
	// Item names an Upcoming item the renderer names itself (brief,
	// brief_morning, brief_evening, news) when there is no Title.
	Item string `json:"item,omitempty"`
	// Schedule is the cron expression an Upcoming item runs on after a
	// change, for the renderer to put into words.
	Schedule    string `json:"schedule,omitempty"`
	Channel     string `json:"channel,omitempty"`
	SentAt      int64  `json:"sentAt,omitempty"`
	PrincipalID string `json:"principalId,omitempty"`
	// Params contains only the engagement template's display parameters.
	// Mail subjects are never copied into main-chat history.
	Params map[string]string `json:"params,omitempty"`
}

// mainChatItemsAppendedEvent tells the renderer a chat grew while it was not
// the one writing to it (main chat reports; a finished background script's
// line in any chat). The renderer reloads the session if it is open, the way
// proactive:notification does for new sessions.
const mainChatItemsAppendedEvent = "session:items-appended"

// mainChatPostMu serializes the dedupe check and the append, so two
// producers posting the same key at once cannot both pass the check.
var mainChatPostMu sync.Mutex

// postToMainChat appends one report to the main chat. It returns the item id
// of the new row, or "" when the report was deduped or could not be stored.
func (a *App) postToMainChat(text string, report mainChatReport) string {
	if a == nil || a.store == nil {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	ctx := context.Background()
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		log.Printf("[main-chat] report dropped (%s): %v", report.Source, err)
		return ""
	}
	mainChatPostMu.Lock()
	defer mainChatPostMu.Unlock()
	if report.Key != "" && a.mainChatHasReport(ctx, main.LocalID, report.Key) {
		return ""
	}
	payload, err := json.Marshal(report)
	if err != nil {
		log.Printf("[main-chat] report payload (%s): %v", report.Source, err)
		return ""
	}
	return a.appendMainChatItem(ctx, main.LocalID, storage.StreamItemRow{
		ID:        uuid.NewString(),
		Kind:      "system",
		Content:   text,
		ToolName:  mainChatReportTool,
		ToolInput: string(payload),
	}, report.Source)
}

// appendMainChatItem stores one item on the main chat and tells the renderer.
// It returns the item id, or "" when the append failed.
func (a *App) appendMainChatItem(ctx context.Context, mainID string, item storage.StreamItemRow, source string) string {
	sortOrder, err := a.store.AppendStreamItem(ctx, mainID, item)
	if err != nil {
		log.Printf("[main-chat] append (%s): %v", source, err)
		return ""
	}
	// Complete derived membership before the reload event can reach a reader.
	if item.ToolName == mainChatProactiveTool || item.ToolName == mainChatReportTool {
		_ = a.tagMainChatRowTopics(ctx, item.ID, mainChatRowTopicRefs(item))
	}
	a.mainChatRowsChanged(mainID)
	a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{
		"sessionId": mainID,
		"itemId":    item.ID,
		"sortOrder": sortOrder,
		"kind":      item.Kind,
		"source":    source,
	})
	return item.ID
}

// mainChatHasReport reports whether a recent main-chat item already carries
// the dedupe key.
func (a *App) mainChatHasReport(ctx context.Context, mainID, key string) bool {
	page, err := a.store.LoadStreamItemsLatest(ctx, mainID, mainChatReportDedupeWindow)
	if err != nil {
		return false
	}
	for _, row := range page.Items {
		if row.Kind != "system" || row.ToolName != mainChatReportTool {
			continue
		}
		var stored mainChatReport
		if json.Unmarshal([]byte(row.ToolInput), &stored) == nil && stored.Key == key {
			return true
		}
	}
	return false
}

// mainChatReportKey builds a dedupe key from stable parts.
func mainChatReportKey(parts ...string) string {
	return strings.Join(parts, ":")
}

// ── Producers ──

// postHandoffReport snapshots the retained execution result into an actionable
// main-chat message. The execution chat remains available as history.
func (a *App) postHandoffReport(entry handoff.Entry) {
	a.postHandoffResult(entry, a.handoffTurnTranscript(entry.ChatSessionID))
}

// scheduleRunSurfacesToUser reports whether a schedule's runs belong in the
// main chat: only the user's own schedules, the surface kinds the brief lists
// ("" is the legacy default, "scheduled" the explicit one). The app's own
// Daily News job (surface "system") feeds the brief directly, and one-shot
// jobs behind handoffs (surface "handoff") report through the handoff producer.
func scheduleRunSurfacesToUser(surfaceKind string) bool {
	switch strings.ToLower(strings.TrimSpace(surfaceKind)) {
	case "", "scheduled":
		return true
	default:
		return false
	}
}

// scheduleRunDay formats the day a run ended, for per-day dedupe keys.
func scheduleRunDay(event CronRunLifecycleEvent) string {
	at := event.FinishedAt
	if at <= 0 {
		at = event.StartedAt
	}
	return time.UnixMilli(at).Format("2006-01-02")
}

// postScheduleRunReport reports a finished schedule run. Running and skipped
// runs produce nothing, an ok run posts only when it produced a result, and
// a schedule gets at most one ok line and one failure line per day, so a
// frequent schedule does not flood the chat. The action opens the schedule.
func (a *App) postScheduleRunReport(event CronRunLifecycleEvent, jobName, surfaceKind string) {
	if !scheduleRunSurfacesToUser(surfaceKind) {
		return
	}
	name := strings.TrimSpace(jobName)
	switch event.Status {
	case "ok":
		if strings.TrimSpace(event.ResultSummary) == "" && len(event.FilePaths) == 0 && event.DeliveryTarget == nil {
			return
		}
	case "error":
	default:
		return
	}
	a.postMainChatStatus(mainChatStatusEntry{
		ID:     mainChatReportKey(mainChatSourceSchedule, event.JobID, event.Status, scheduleRunDay(event)),
		Kind:   "schedule_" + event.Status,
		Text:   engagement.StatusReport(a.currentUILanguage(), "schedule_"+event.Status, name),
		Target: mainChatTarget{Kind: mainChatTargetAutomation, ID: event.JobID, RunID: event.RunID},
	}, time.Now())
}

// postScheduleAPIChange posts a quiet main-chat report line for a schedule
// change that came through the cron REST API (the yansu CLI), so a create,
// update, enable, pause, run or delete made outside the app's own UI is still
// visible to the user. status is one of created, changed, resumed, paused,
// started, removed; the renderer localizes it from Status, and text is the
// English fallback. jobID lets the card open the schedule.
func (a *App) postScheduleAPIChange(jobID, name, status string) {
	routine := true
	display := strings.TrimSpace(name)
	if display == "" {
		display = "A schedule"
	}
	var text string
	switch status {
	case "created":
		text = fmt.Sprintf("Added %q to your schedules.", display)
	case "changed":
		text = fmt.Sprintf("Changed %q.", display)
	case "resumed":
		text = fmt.Sprintf("Resumed %q.", display)
	case "paused":
		text = fmt.Sprintf("Paused %q.", display)
	case "started":
		text = fmt.Sprintf("Started %q now.", display)
	case "removed":
		text = fmt.Sprintf("Removed %q.", display)
	default:
		text = fmt.Sprintf("Changed %q.", display)
	}
	a.postToMainChat(text, mainChatReport{
		Routine: &routine,
		Source:  mainChatSourceSchedule,
		Action:  mainChatActionOpen,
		Target:  mainChatTarget{Kind: mainChatTargetAutomation, ID: jobID},
		Title:   strings.TrimSpace(name),
		Status:  status,
	})
}

// handleCronRunLifecycle is the scheduler's run callback: it keeps the
// initiative receipts, posts finished runs into the main chat and, after a
// failure, checks whether the schedule missed twice in a row. The job's
// name and surface come with the event, from the scheduler's own record.
func (a *App) handleCronRunLifecycle(event CronRunLifecycleEvent) {
	if event.SurfaceKind == openAnswerSurfaceKind {
		if a != nil && a.store != nil && (event.Status == "error" || event.Status == "skipped") {
			ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
			defer cancel()
			answer, err := a.store.GetOpenAnswer(ctx, openAnswerIDFromJobName(event.JobName))
			if err == nil {
				if err := a.ensureOpenAnswerCheckAfter(answer, event.JobID, time.Now().Add(openAnswerCheckPeriod)); err != nil {
					log.Printf("[open-answer] failed to recover check after %s run: job=%s err=%v", event.Status, event.JobID, err)
				}
			}
		}
		return
	}
	a.recordInitiativeCronLifecycle(event)
	a.handleTopicFindsRun(event)
	a.noteTodayNewsRetryRun(event)
	if event.Status == "running" || event.Status == "skipped" {
		return
	}
	a.postScheduleRunReport(event, event.JobName, event.SurfaceKind)
	if event.TriggerKind == "manual" {
		// A run started by hand says nothing about the schedule.
		return
	}
	switch event.Status {
	case "error":
		a.noteScheduleRunHealth(event.JobID)
	case "ok":
		a.retireScheduleMissed(event.JobID)
	default:
	}
}

// engagementKindHasOwnReport lists the moment kinds whose event already posts
// through a dedicated producer, so the desktop delivery must not post twice.
func engagementKindHasOwnReport(kind engagement.Kind) bool {
	switch kind {
	case engagement.KindWorkFinished, engagement.KindWorkBlocked, engagement.KindMeetingEnded:
		return true
	default:
		return false
	}
}

// engagementKindQuotesMail lists the kinds whose text names a mail subject.
// The main chat's lines reach its model prompt, and mail subjects stay out of
// every prompt, so these post their generic line.
func engagementKindQuotesMail(kind engagement.Kind) bool {
	switch kind {
	case engagement.KindWaitingOnReply:
		return true
	default:
		return false
	}
}

// These sightings say only that a person appeared; they cannot name what is open.
func mainChatUnspecificPersonMoment(m engagement.Moment) bool {
	return m.Kind == engagement.KindPersonReappeared || (m.Kind == engagement.KindFirstWeek && m.Params["stage"] == "person")
}

// postEngagementReport acknowledges a durable main-chat item independently of
// the chosen notification channels. Kinds with a dedicated report are skipped.
func (a *App) postEngagementReport(m engagement.Moment, text engagementText) bool {
	if engagementKindHasOwnReport(m.Kind) || engagementKindSilent(m.Kind) ||
		mainChatUnspecificPersonMoment(m) {
		return true
	}
	if a.engagement != nil {
		prefs := a.engagement.Prefs()
		spec, known := engagement.Spec(m.Kind)
		if known && engagementChatKindMuted(spec, prefs, a.engagement.Paused(m.Kind), a.engagement.now()) {
			return true
		}
	}
	now := time.Now()
	if a.engagement != nil {
		now = a.engagement.now()
	}
	// Activity-driven cards wait for the hourly batch, which runs their voice
	// check; the engagement service holds them until then. Their phone and
	// desktop notifications are not held.
	if !a.activityBatchAllowsVoice(m.Kind, now) {
		logProactiveVoiceFallback("deferred_to_batch")
		return false
	}
	item, ok := a.proactiveItem(m, text)
	if !ok {
		return false
	}
	key := engagementReportKey(m, now)
	if engagementMomentIsStatus(m, now) {
		if a.postMainChatStatus(proactiveVoiceStatusEntry(item, key), now) == "" {
			return false
		}
		main, err := a.ensureMainChat(context.Background())
		if err != nil {
			return false
		}
		stored, sentAt, err := a.mainChatMeasuredItem(context.Background(), main.LocalID, key)
		if err != nil {
			return false
		}
		return a.recordMainChatDelivery(*stored, sentAt) == nil
	}
	return a.postProactiveItem(item, key) != ""
}

func mainChatEngagementParams(m engagement.Moment) map[string]string {
	if m.Kind == engagement.KindFirstWeek || m.Kind == engagement.KindWeeklyRecap {
		return m.Params
	}
	if engagementKindQuotesMail(m.Kind) {
		return nil
	}
	copy, ok := engagement.Copy(m.Kind, "en")
	if !ok {
		return nil
	}
	params := make(map[string]string)
	for key, value := range m.Params {
		if strings.Contains(copy.Sealed, "{"+key+"}") {
			params[key] = value
		}
	}
	for key, value := range m.Params {
		if strings.HasPrefix(key, "event_") {
			params[key] = value
		}
	}
	return params
}

// engagementReportKey dedupes desktop engagement lines by kind and subject
// within the hour of the moment's CreatedAt. A capture outage keeps its start
// as CreatedAt across every 5-minute check, so it gets one line per outage;
// commitment and goal check-ins stamp each check with now, so they get at
// most one line per subject per hour. The moment's own DedupeKey is not
// used for those checks: some producers mint a new key on each check.
// Playbook results instead keep each run's moment identity, so two runs of the
// same plan within an hour both remain visible while retries still dedupe.
func engagementReportKey(m engagement.Moment, now time.Time) string {
	if m.Kind == engagement.KindPlaybookResult {
		id := m.ID
		if id == "" {
			id = m.DedupeKey
		}
		if id != "" {
			return mainChatReportKey(mainChatSourceEngagement, string(m.Kind), id)
		}
	}
	at := m.CreatedAt
	if at.IsZero() {
		at = now
	}
	return mainChatReportKey(mainChatSourceEngagement, string(m.Kind), m.SubjectID, at.UTC().Truncate(time.Hour).Format(time.RFC3339))
}

// mainChatReportContextLines is how many recent report lines the main chat's
// model sees ahead of each turn.
const mainChatReportContextLines = 12

// injectMainChatReports is the composePrompt producer for the main chat. The
// history loader replays only user and assistant rows, so the lines
// background work posted would otherwise be invisible to the model and
// "what finished while I was away?" would go unanswered.
func (a *App) injectMainChatReports(tag string) string {
	if a == nil || a.store == nil || strings.TrimSpace(tag) == "" {
		return ""
	}
	ctx := context.Background()
	row, err := a.store.LoadSession(ctx, tag)
	if err != nil || row == nil || row.Kind != storage.SessionKindMain {
		return ""
	}
	page, err := a.store.LoadStreamItemsLatest(ctx, tag, mainChatReportDedupeWindow)
	if err != nil {
		return ""
	}
	var lines []string
	for _, item := range page.Items {
		if item.Kind != "system" {
			continue
		}
		line := mainChatPromptReportLine(item)
		if line != "" {
			lines = append(lines, fmt.Sprintf("- [%s] %s", time.UnixMilli(item.CreatedAt).Format("Jan 2 15:04"), line))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if len(lines) > mainChatReportContextLines {
		lines = lines[len(lines)-mainChatReportContextLines:]
	}
	var b strings.Builder
	b.WriteString("<background-reports>\n")
	b.WriteString("Status lines Isoform posted into this chat between the user's messages, oldest first. They are not the user's words; use them to answer questions about what finished, failed or needs attention.\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n</background-reports>\n\n")
	return b.String()
}
