package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"yansu-agent/agent"
)

const timelineGroupSummaryTimeout = 3 * time.Minute

type timelineGroupSummaryResponse struct {
	Title        string                         `json:"title"`
	Description  string                         `json:"description"`
	ActivityType string                         `json:"activityType"`
	Entities     []string                       `json:"entities"`
	Timeline     []ActivityTimelineSemanticItem `json:"timeline"`
}

// errTimelineRunProtected marks a run whose app is on the protected list:
// no summary is ever generated. The worker treats it as a silent skip; the
// on-demand binding maps it to a user-accurate message.
var errTimelineRunProtected = errors.New("timeline run belongs to a protected app")

// GenerateActivityTimelineGroupSummary is the on-demand path for one
// persisted timeline run, called when the user selects a row that has no
// current summary (including runs older than the worker's auto-summarize
// horizon). It serves the cached summary when it is current, refuses while
// another generation holds the claim or a retry backoff is pending, and
// otherwise generates synchronously through the same core as the worker.
func (a *App) GenerateActivityTimelineGroupSummary(groupID string) (*ActivityTimelineGroupSummary, error) {
	if a.activity == nil || a.activity.store == nil {
		return nil, fmt.Errorf("activity store not initialized")
	}
	store := a.activity.store
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil, fmt.Errorf("timeline group id is required")
	}
	run, err := store.GetTimelineRunByID(groupID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("timeline group %s not found", groupID)
	}

	if run.SummaryStatus == timelineSummaryStatusDone && run.EventCount <= run.SummarizedEventCount {
		summaries, err := store.GetTimelineGroupSummaries([]string{run.ID})
		if err != nil {
			return nil, err
		}
		if summary, ok := summaries[run.ID]; ok {
			return &summary, nil
		}
		// Summary row missing despite done status — fall through and regenerate.
	}

	// Status gates: the selection-triggered call must not defeat the worker's
	// claim, the retry backoff, or the permanent-failure park (the frontend
	// fires this on every selection, so an ungated path would burn quota in a
	// loop on a run that fails identically every time).
	switch run.SummaryStatus {
	case timelineSummaryStatusGenerating:
		return nil, fmt.Errorf("timeline group summary generation already in progress")
	case timelineSummaryStatusFailedPermanent:
		return nil, fmt.Errorf("timeline group summary generation permanently failed")
	case timelineSummaryStatusFailedTransient:
		if run.NextRetryAt > time.Now().UnixMilli() {
			return nil, fmt.Errorf("timeline group summary retry pending")
		}
	}

	claimed, err := store.ClaimTimelineRunForSummary(run.ID, []string{
		timelineSummaryStatusNone,
		timelineSummaryStatusFailedTransient,
		timelineSummaryStatusDone,
		timelineSummaryStatusSkipped,
	})
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, fmt.Errorf("timeline group summary generation already in progress")
	}
	// Surface the generating state immediately, and again on resolution.
	a.emitBrowserEvent("activity:timeline-groups-updated")
	summary, genErr := a.generateTimelineRunSummary(context.Background(), *run)
	a.emitBrowserEvent("activity:timeline-groups-updated")
	if errors.Is(genErr, errTimelineRunProtected) {
		return nil, fmt.Errorf("timeline group %s is for a protected app; summaries are not generated", groupID)
	}
	if genErr != nil {
		return nil, genErr
	}
	if summary == nil {
		return nil, fmt.Errorf("timeline group %s has no loadable events", groupID)
	}
	return summary, nil
}

// generateTimelineRunSummary is the shared generation core for the worker and
// the on-demand path. The caller must hold the run's claim (summary_status =
// 'generating'); this function always resolves that state to done, skipped,
// or failed before returning. A nil summary with nil error means the run had
// no loadable events and was marked skipped.
func (a *App) generateTimelineRunSummary(ctx context.Context, run ActivityTimelineRunRow) (*ActivityTimelineGroupSummary, error) {
	job, err := a.prepareTimelineRunSummary(run)
	if job == nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, timelineGroupSummaryTimeout)
	defer cancel()

	images := a.timelineGroupSummaryImages(job.snapshots, 8)
	model := a.lightTaskModel()

	raw, err := a.runTimelineGroupSummaryPrompt(ctx, job.prompt, images, model)
	if err != nil && len(images) > 0 && isImageUnsupportedModelError(err) {
		// The routed upstream cannot accept image blocks (a text-only provider
		// behind the gateway). The prompt already carries the run's screen
		// content as text, so a text-only summary is a faithful degradation —
		// far better than parking the run as failed_permanent.
		log.Printf("[timeline-summary] run %s: model rejected images, retrying text-only", run.ID)
		raw, err = a.runTimelineGroupSummaryPrompt(ctx, job.prompt, nil, model)
	}
	if err != nil {
		a.markTimelineRunSummaryError(run, err)
		return nil, err
	}

	var resp timelineGroupSummaryResponse
	if err := unmarshalJSONBestEffort(stripCodeFences(strings.TrimSpace(raw)), &resp); err != nil {
		err = fmt.Errorf("parse timeline group summary response: %w", err)
		a.markTimelineRunSummaryError(run, err)
		return nil, err
	}
	return a.finishTimelineRunSummary(*job, resp, model, raw)
}

// timelineSummaryJob is one run ready for a summary model call.
type timelineSummaryJob struct {
	run       ActivityTimelineRunRow
	group     ActivityTimelineGroup
	snapshots []ActivitySnapshotRow
	section   string
	prompt    string
}

// prepareTimelineRunSummary loads a run and builds its prompt. A nil job
// means the run needs no model call (protected or empty) or failed to load;
// its status is already recorded.
func (a *App) prepareTimelineRunSummary(run ActivityTimelineRunRow) (*timelineSummaryJob, error) {
	store := a.activity.store
	// Protected-app runs never get an LLM summary. Checked here — in the
	// shared core — so BOTH the background worker and the on-demand UI
	// binding (GenerateActivityTimelineGroupSummary) are covered; the
	// snapshot/OCR loaders below additionally return nothing for protected
	// member sessions via the store's default read policy. The sentinel
	// lets the on-demand caller surface an accurate message instead of
	// "no loadable events".
	if !a.activityTimelineRunAllowsModelRead(store, run) {
		if err := store.MarkTimelineRunSummarySkipped(run.ID, run.EventCount); err != nil {
			// The run must not stay parked in "generating" — fall back to
			// the failure path so the claim resolves and a retry is
			// scheduled (the retry will skip again, converging to skipped).
			log.Printf("[activity] mark protected timeline run %s skipped: %v", run.ID, err)
			a.markTimelineRunSummaryError(run, err)
		}
		return nil, errTimelineRunProtected
	}
	events, err := store.LoadTimelineRunEvents(run)
	if err != nil {
		a.markTimelineRunSummaryError(run, err)
		return nil, err
	}
	if len(events) == 0 {
		// Member sessions were purged (or never matched) — nothing to
		// summarize, and a retry cannot change that.
		if err := store.MarkTimelineRunSummarySkipped(run.ID, run.EventCount); err != nil {
			log.Printf("[activity] mark timeline run %s skipped: %v", run.ID, err)
			a.markTimelineRunSummaryError(run, err)
		}
		return nil, nil
	}
	// Agent work gets no summary, so it never becomes one of the person's
	// moments; the day counts it on its labelled agent line instead.
	if timelineEventsAreAgent(events) {
		if err := store.MarkTimelineRunAgent(run); err != nil {
			log.Printf("[activity] mark agent timeline run %s: %v", run.ID, err)
			a.markTimelineRunSummaryError(run, err)
		}
		return nil, nil
	}
	if err := store.ClearTimelineRunAgent(run.ID); err != nil {
		log.Printf("[activity] clear agent timeline run %s: %v", run.ID, err)
	}
	group := timelineRunToGroup(run, events)
	snapshots := a.timelineGroupSummarySnapshots(group)
	screenContent := a.timelineGroupScreenContent(snapshots)
	languageInstruction := responseLanguageInstruction(a.currentUILanguageName())
	return &timelineSummaryJob{
		run: run, group: group, snapshots: snapshots,
		section: timelineSummaryRunSection(group, screenContent),
		prompt:  buildTimelineGroupSummaryPrompt(group, screenContent, languageInstruction),
	}, nil
}

// finishTimelineRunSummary stores a parsed summary and marks the run done.
func (a *App) finishTimelineRunSummary(job timelineSummaryJob, resp timelineGroupSummaryResponse, model, raw string) (*ActivityTimelineGroupSummary, error) {
	store := a.activity.store
	run, group := job.run, job.group
	summary := sanitizeTimelineGroupSummary(group, resp)
	if err := store.UpsertTimelineGroupSummary(group, summary, model, job.prompt, raw); err != nil {
		a.markTimelineRunSummaryError(run, err)
		return nil, err
	}
	// Watermark with run.EventCount, NOT len(events): loaded events can be
	// fewer than the run's total when member sessions are protected (the
	// store's default read policy drops them) or purged. A lower watermark
	// would keep event_count > summarized_event_count true forever and the
	// due-queue would regenerate this run every pass — an unbounded model
	// loop. The excluded events are intentionally unsummarizable, not
	// pending work.
	if err := store.MarkTimelineRunSummaryDone(run.ID, run.EventCount); err != nil {
		log.Printf("[activity] mark timeline run %s summary done: %v", run.ID, err)
		a.markTimelineRunSummaryError(run, err)
	}
	return &summary, nil
}

// isImageUnsupportedModelError reports whether a model call was rejected
// because the routed upstream cannot accept image content (a text-only
// provider behind the gateway), as opposed to a genuinely malformed request.
// The failure surfaces as a generic HTTP 400, so the upstream's error text is
// the only signal: OpenAI-compatible text-only providers reject the
// "image_url" content-part type by name, others spell out the lack of image
// support.
func isImageUnsupportedModelError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if !strings.Contains(s, "http 400") && !strings.Contains(s, "invalid_request") {
		return false
	}
	return strings.Contains(s, "image_url") ||
		strings.Contains(s, "image input") ||
		strings.Contains(s, "does not support image")
}

// markTimelineRunSummaryError resolves a claimed run's status after a failed
// generation: permanent rejections (and attempt exhaustion) park the run;
// transient failures schedule a retry with the shared triage backoff.
func (a *App) markTimelineRunSummaryError(run ActivityTimelineRunRow, cause error) {
	attempts := run.SummaryAttempts + 1
	permanent := isPermanentExtractionFailure(cause) || attempts >= timelineSummaryMaxAttempts
	nextRetryAt := time.Now().Add(triageCooldownDuration(attempts)).UnixMilli()
	if err := a.activity.store.MarkTimelineRunSummaryFailed(run.ID, permanent, attempts, nextRetryAt); err != nil {
		log.Printf("[activity] mark timeline run %s summary failed: %v", run.ID, err)
	}
}

func (a *App) runTimelineGroupSummaryPrompt(ctx context.Context, prompt string, _ []agent.ImageAttachment, model string) (string, error) {
	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	if runner == nil {
		return "", fmt.Errorf("runner not initialized")
	}

	var result strings.Builder
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		AutoApproveTools:  true,
		Prompt:            redactActivityModelText(prompt),
		Priority:          agent.PriorityBackground,
		Model:             model,
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
		if event.Type == "assistant" {
			result.WriteString(event.Content)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("execute timeline group summary: %w", err)
	}
	return result.String(), nil
}

func buildTimelineGroupSummaryPrompt(group ActivityTimelineGroup, screenContent, languageInstruction string) string {
	if activitySourceAppIsSensitive(group.AppKey, group.AppName) {
		return ""
	}
	var b strings.Builder
	b.WriteString(timelineSummaryInstructions(languageInstruction))
	b.WriteString(timelineSummaryRunSection(group, screenContent))
	b.WriteString("\nRespond immediately with the JSON object.")
	return redactActivityModelText(b.String())
}

// timelineSummaryInstructions is the instruction text shared by the one-run
// and the batched prompt; it ends with the "App run:" heading.
func timelineSummaryInstructions(languageInstruction string) string {
	var b strings.Builder
	if strings.TrimSpace(languageInstruction) != "" {
		b.WriteString(languageInstruction)
		b.WriteString("\n\n")
	}
	b.WriteString(`You summarize one completed per-app desktop activity run for a timeline UI.

Use ALL inputs:
- the "Screen content" text below — window/article titles, URLs, and on-screen text captured during the run; it is the ground truth for names and content
- the agent-friendly event list below for exact user operations and source ids

Return a semantic summary that preserves the run's key information, so future analysis knows exactly what the user worked on and what it was about.

Quoting (overrides the language rule for quoted material only):
- The summary prose follows the UI language above, but material quoted from the screen stays VERBATIM in its original language: article and document titles, message and post text, publication and people names, repos, branches, file paths, URLs, channels, commands. Never translate, paraphrase, abbreviate, or anglicize quoted material.
- Example with an English UI and Chinese content: title "Read 「大势已定，和统当先」 in WeChat" — English prose, the article title kept verbatim.

Rules:
- Output JSON only. No markdown, no code fences.
- title: a past-tense verb + the specific artifact named exactly, 60 characters at most, like a diary entry ("Opened System Settings", never "Open System Settings") — the article's real title, the repo/file, the person or channel. A generic title like "Read article" or "Browse website" is wrong whenever the evidence names the artifact.
- description: 1-2 sentences stating what the user did, the artifact's exact name, and its source (publication, site, repo, channel) when visible.
- entities: exact names of the real things this run touched: article titles, publications, people, repos, files, domains, tools, commands. No generic categories like "article" or "website".
- timeline: each item is one concrete semantic step; merge adjacent raw events that represent the same step. Carry key original information in details — exact titles, authors, URLs, key points read, text typed, commands run — not generic restatements.
- Timeline items must cite raw eventIds from the provided list. Use eventIds only, never array indexes.
- Keyboard events: "typed" is the literal text the user entered (already IME-composed). "imeRawKeys" are raw keystrokes captured under a composing input method (e.g. pinyin) — they are NOT the entered text; never quote or paraphrase them as content.
- Never emit app switches, empty focus changes, or accidental clicks as timeline items; the run is already scoped to one app.
- Never name accessibility roles (AXGroup, Button, StaticText) in a step. A click and the focus it causes are one step, in words: "Clicked Allow for this session in Claude".
- Say "your computer", never "your Mac".
- Quote speech in a step only when it says something a person would recognize later; a scrap such as "There the two." is not a step.
- Do not include status, confidence, intent, timestamps, event indexes, or invented ids.
- If evidence is thin, stay conservative — but still name whatever specific content is visible.

` + activitySecondPersonRule + `

JSON shape:
{
  "title": "Past-tense verb + specific artifact",
  "description": "One or two sentences describing the work in this app run.",
  "activityType": "coding|writing|research|communication|design|ops|browsing|system|other",
  "entities": ["entity", "entity"],
  "timeline": [
    {
      "eventIds": ["ae_123", "ae_124"],
      "action": "Concrete step the user performed",
      "target": "Optional target such as file, article, repo, message, command",
      "details": ["key original information: exact titles, URLs, text"]
    }
  ]
}

App run:
`)
	return b.String()
}

// timelineSummaryRunSection renders one run's facts, screen content and
// events, the body under an "App run:" heading. Empty for a sensitive app.
func timelineSummaryRunSection(group ActivityTimelineGroup, screenContent string) string {
	if activitySourceAppIsSensitive(group.AppKey, group.AppName) {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("- groupId: %s\n", group.ID))
	b.WriteString(fmt.Sprintf("- app: %s\n", group.AppName))
	b.WriteString(fmt.Sprintf("- appKey: %s\n", group.AppKey))
	b.WriteString(fmt.Sprintf("- durationMs: %d\n", group.ActiveMs))
	b.WriteString(fmt.Sprintf("- rawEventCount: %d\n", group.EventCount))
	if strings.TrimSpace(screenContent) != "" {
		b.WriteString("\nScreen content, chronological:\n")
		b.WriteString(screenContent)
	}
	b.WriteString("\nAgent-friendly events, chronological:\n")
	for _, evt := range group.Events {
		if strings.TrimSpace(evt.EventID) == "" || activitySourceAppIsSensitive(evt.AppBundleID, evt.AppName) || activitymon.IsPrivateBrowsing(evt.WindowTitle) {
			continue
		}
		b.WriteString("- ")
		b.WriteString(renderTimelineSummaryEvent(evt))
		b.WriteString("\n")
	}
	return b.String()
}

func renderTimelineSummaryEvent(evt ActivityTimelineEvent) string {
	if activitySourceAppIsSensitive(evt.AppBundleID, evt.AppName) || activitymon.IsPrivateBrowsing(evt.WindowTitle) {
		return ""
	}
	switch evt.Kind {
	case activitymon.EventClick, activitymon.EventFocus:
		// A click or focus with no name a person can read isn't a step (AR57).
		if _, target := timelineEventActionAndTarget(evt.ActivityEvent); target == "" {
			return ""
		}
	}
	fields := []string{
		fmt.Sprintf("eventId=%s", evt.EventID),
		fmt.Sprintf("kind=%s", evt.Kind),
	}
	if s := strings.TrimSpace(evt.AppName); s != "" {
		fields = append(fields, fmt.Sprintf("app=%q", s))
	}
	if s := strings.TrimSpace(redactActivityModelText(evt.WindowTitle)); s != "" {
		fields = append(fields, fmt.Sprintf("window=%q", truncateRunes(s, 160)))
	}
	if action, target := timelineEventActionAndTarget(evt.ActivityEvent); action != "" {
		fields = append(fields, fmt.Sprintf("action=%q", action))
		if target != "" {
			fields = append(fields, fmt.Sprintf("target=%q", truncateRunes(redactActivityModelText(target), 240)))
		}
	}
	if evt.Kind == activitymon.EventKeyboard {
		if s := strings.TrimSpace(redactActivityModelText(evt.TypedText)); s != "" {
			fields = append(fields, fmt.Sprintf("typed=%q", truncateRunes(s, 240)))
		}
		if len(evt.KeyChars) > 0 {
			key := "chars"
			if evt.IMEComposing {
				key = "imeRawKeys"
			}
			fields = append(fields, fmt.Sprintf("%s=%q", key, truncateRunes(redactActivityModelText(strings.Join(evt.KeyChars, "")), 80)))
		}
	}
	if evt.Kind == activitymon.EventSpeech && strings.TrimSpace(evt.SpeechText) != "" {
		fields = append(fields, fmt.Sprintf("speech=%q", truncateRunes(redactActivityModelText(evt.SpeechText), 240)))
	}
	return redactActivityModelText(strings.Join(fields, " "))
}

func timelineEventActionAndTarget(evt activitymon.ActivityEvent) (string, string) {
	switch evt.Kind {
	case activitymon.EventClick:
		return "clicked", timelineReadableLabel(evt.ClickLabel)
	case activitymon.EventKeyboard:
		if s := strings.TrimSpace(evt.TypedText); s != "" {
			return "typed", s
		}
		if evt.IMEComposing {
			// Raw keystrokes under a composing IME are pinyin/kana noise,
			// not literal text — surface them only via imeRawKeys.
			return "typed via IME", ""
		}
		if len(evt.KeyChars) > 0 || strings.TrimSpace(evt.KeyChar) != "" {
			return "typed", strings.Join(evt.KeyChars, "")
		}
		return "pressed key", ""
	case activitymon.EventFocus:
		return "focused", timelineReadableLabel(evt.FocusLabel)
	case activitymon.EventAppSwitch:
		return "switched app", strings.TrimSpace(evt.AppName)
	case activitymon.EventSpeech:
		return "dictated", strings.TrimSpace(evt.SpeechText)
	default:
		return string(evt.Kind), ""
	}
}

// timelineAXRoleToken matches an accessibility role name such as "AXGroup".
var timelineAXRoleToken = regexp.MustCompile(`\bAX[A-Z][A-Za-z]*\b`)

var timelineRoleWords = map[string]bool{
	"group": true, "button": true, "statictext": true, "text": true, "textfield": true, "textarea": true,
	"image": true, "cell": true, "row": true, "link": true, "window": true, "menuitem": true, "checkbox": true,
	"radiobutton": true, "popupbutton": true, "scrollarea": true, "webarea": true, "toolbar": true, "list": true,
	"outline": true, "table": true, "tab": true, "tabgroup": true, "splitter": true, "unknown": true,
}

// timelineReadableLabel is a click or focus label a person can read, or ""
// when it is only an accessibility role ("AXGroup", "Button", "StaticText")
// (live walk AR57).
func timelineReadableLabel(label string) string {
	label = strings.TrimSpace(label)
	if timelineAXRoleToken.MatchString(label) || timelineRoleWords[strings.ToLower(strings.ReplaceAll(label, " ", ""))] {
		return ""
	}
	return label
}

var timelineYourMac = regexp.MustCompile(`\b([Yy]our) Mac(?:Book(?: Pro| Air)?)?\b`)

// timelineTitleSaysComputer names the computer, not the brand (live walk AR58).
func timelineTitleSaysComputer(title string) string {
	return timelineYourMac.ReplaceAllString(title, "${1} computer")
}

func sanitizeTimelineGroupSummary(group ActivityTimelineGroup, resp timelineGroupSummaryResponse) ActivityTimelineGroupSummary {
	allowed := make(map[string]bool, len(group.Events))
	appSwitchIDs := make(map[string]bool)
	scrapSpeechIDs := make(map[string]bool)
	for _, evt := range group.Events {
		if strings.TrimSpace(evt.EventID) == "" || activitySourceAppIsSensitive(evt.AppBundleID, evt.AppName) || activitymon.IsPrivateBrowsing(evt.WindowTitle) {
			continue
		}
		allowed[evt.EventID] = true
		if evt.Kind == activitymon.EventAppSwitch {
			appSwitchIDs[evt.EventID] = true
		}
		if evt.Kind == activitymon.EventSpeech && timelineSpeechIsScrap(evt.SpeechText) {
			scrapSpeechIDs[evt.EventID] = true
		}
	}

	timeline := make([]ActivityTimelineSemanticItem, 0, len(resp.Timeline))
	switchOnly := make([]bool, 0, len(resp.Timeline))
	for _, item := range resp.Timeline {
		action := strings.TrimSpace(item.Action)
		if action == "" || timelineAXRoleToken.MatchString(action) {
			continue
		}
		ids := make([]string, 0, len(item.EventIDs))
		seen := map[string]bool{}
		itemSwitchOnly := true
		itemScrapOnly := true
		for _, id := range item.EventIDs {
			id = strings.TrimSpace(id)
			if !allowed[id] || seen[id] {
				continue
			}
			ids = append(ids, id)
			seen[id] = true
			if !appSwitchIDs[id] {
				itemSwitchOnly = false
			}
			if !scrapSpeechIDs[id] {
				itemScrapOnly = false
			}
		}
		if len(ids) == 0 || itemScrapOnly {
			continue
		}
		timeline = append(timeline, ActivityTimelineSemanticItem{
			EventIDs: ids,
			Action:   truncateRunes(action, 120),
			Target:   truncateRunes(strings.TrimSpace(item.Target), 160),
			Details:  cleanStringList(item.Details, 4, 180),
		})
		switchOnly = append(switchOnly, itemSwitchOnly)
	}

	// The prompt forbids app-switch steps, but small models still emit them;
	// enforce here. Keep switch-only items only when nothing else survived
	// (a run whose entire activity really was the switch).
	meaningful := make([]ActivityTimelineSemanticItem, 0, len(timeline))
	for i, item := range timeline {
		if switchOnly[i] {
			continue
		}
		meaningful = append(meaningful, item)
	}
	if len(meaningful) > 0 {
		timeline = meaningful
	}

	title := timelineTitleSaysComputer(timelineTitlePastTense(strings.TrimSpace(resp.Title)))
	if title == "" {
		title = fmt.Sprintf("Used %s", group.AppName)
	}
	description := strings.TrimSpace(resp.Description)
	if description == "" {
		description = fmt.Sprintf("Worked in %s during this activity run.", group.AppName)
	}
	activityType := strings.TrimSpace(resp.ActivityType)
	if activityType == "" {
		activityType = "other"
	}
	return ActivityTimelineGroupSummary{
		GroupID:      group.ID,
		Title:        truncateRunes(title, 80),
		Description:  truncateRunes(description, 280),
		ActivityType: truncateRunes(activityType, 40),
		Entities:     cleanStringList(resp.Entities, 12, 80),
		Timeline:     timeline,
	}
}

// timelineSpeechIsScrap reports a transcript too thin to be a step
// (activity review v1.0.5 V6): under four words, or under six characters in
// a language written without spaces.
func timelineSpeechIsScrap(text string) bool {
	text = strings.TrimSpace(text)
	if words := strings.Fields(text); len(words) > 1 {
		return len(words) < 4
	}
	return utf8.RuneCountInString(text) < 6
}

// imperativeTitleVerbs maps the leading verb of an English session title to
// its past tense, so titles read like a diary (activity review v1.0.5 V4).
var imperativeTitleVerbs = map[string]string{
	"Add": "Added", "Analyze": "Analyzed", "Ask": "Asked", "Attend": "Attended", "Browse": "Browsed", "Build": "Built", "Call": "Called",
	"Check": "Checked", "Click": "Clicked", "Close": "Closed", "Commit": "Committed", "Compose": "Composed",
	"Configure": "Configured", "Copy": "Copied", "Create": "Created", "Debug": "Debugged", "Delete": "Deleted",
	"Chat": "Chatted", "Discuss": "Discussed", "Download": "Downloaded", "Draft": "Drafted", "Edit": "Edited", "Email": "Emailed", "Explore": "Explored",
	"Find": "Found", "Fix": "Fixed", "Go": "Went", "Inspect": "Inspected", "Install": "Installed",
	"Join": "Joined", "Launch": "Launched", "Listen": "Listened", "Look": "Looked", "Make": "Made",
	"Meet": "Met", "Merge": "Merged", "Message": "Messaged", "Navigate": "Navigated", "Open": "Opened", "Plan": "Planned",
	"Play": "Played", "Push": "Pushed", "Remove": "Removed", "Reply": "Replied", "Research": "Researched",
	"Respond": "Responded", "Review": "Reviewed", "Run": "Ran", "Save": "Saved", "Schedule": "Scheduled",
	"Scroll": "Scrolled", "Search": "Searched", "Select": "Selected", "Send": "Sent", "Share": "Shared",
	"Sign": "Signed", "Start": "Started", "Switch": "Switched", "Talk": "Talked", "Test": "Tested", "Text": "Texted", "Type": "Typed",
	"Update": "Updated", "Upload": "Uploaded", "Use": "Used", "View": "Viewed", "Visit": "Visited",
	"Watch": "Watched", "Write": "Wrote",
}

func timelineTitlePastTense(title string) string {
	verb, rest, _ := strings.Cut(title, " ")
	if past, ok := imperativeTitleVerbs[verb]; ok {
		if rest == "" {
			return past
		}
		return past + " " + rest
	}
	return title
}

func cleanStringList(values []string, maxItems int, maxRunes int) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		out = append(out, truncateRunes(value, maxRunes))
		seen[key] = true
		if len(out) >= maxItems {
			break
		}
	}
	return out
}

// timelineGroupSummarySnapshots collects the run's app-and-timespan-matched
// snapshots across its member sessions in chronological order. Shared by the
// frame picker and the screen-content section so both see the same evidence.
func (a *App) timelineGroupSummarySnapshots(group ActivityTimelineGroup) []ActivitySnapshotRow {
	if a.activity == nil || a.activity.store == nil {
		return nil
	}
	snapshots := make([]ActivitySnapshotRow, 0)
	for _, sessionID := range group.SessionIDs {
		rows, err := a.activity.store.GetSessionSnapshots(sessionID)
		if err != nil {
			log.Printf("[activity] timeline summary snapshots failed session=%s: %v", sessionID, err)
			continue
		}
		for _, snap := range rows {
			if timelineGroupSnapshotMatches(group, snap) && !activitySourceAppIsSensitive(parsedSnapshotBundleID(snap.ParsedJSON), snap.App) {
				snapshots = append(snapshots, snap)
			}
		}
	}
	sort.SliceStable(snapshots, func(i, j int) bool {
		return snapshots[i].Timestamp < snapshots[j].Timestamp
	})
	return snapshots
}

const (
	// timelineSummaryScreenBodyMaxRunes caps the evidence-first text carried per
	// screen observation into the summary prompt.
	timelineSummaryScreenBodyMaxRunes = 280
	// timelineSummaryScreenMaxEntries caps how many collapsed screen
	// observations the prompt carries for one run.
	timelineSummaryScreenMaxEntries = 12
)

// timelineGroupScreenContent renders the run's captured on-screen text (parsed
// titles, URLs, head-of-body) so the summary model can copy key identifiers
// verbatim instead of re-reading them from pixels. It reuses the semantic-event
// preprocessing stack: app parser > AX text > OCR fallback, with scroll frames
// collapsed into one observation each.
func (a *App) timelineGroupScreenContent(snapshots []ActivitySnapshotRow) string {
	if len(snapshots) == 0 || a.activity == nil || a.activity.store == nil {
		return ""
	}
	store := a.activity.store
	allowed := make([]ActivitySnapshotRow, 0, len(snapshots))
	for _, snap := range snapshots {
		if !activitySourceAppIsSensitive(parsedSnapshotBundleID(snap.ParsedJSON), snap.App) {
			allowed = append(allowed, snap)
		}
	}
	snapshots = allowed
	ocr := make(map[int64]string, len(snapshots))
	for _, sn := range snapshots {
		txt, err := store.GetOCRTextBySnapshot(sn.SessionID, sn.ID)
		if err != nil {
			continue
		}
		if t := strings.TrimSpace(txt); t != "" {
			ocr[sn.ID] = t
		}
	}
	observations := collapseScrolling(snapshots, ocr)
	return renderTimelineScreenContent(sampleTimelineScreenObservations(observations, snapshots, timelineSummaryScreenMaxEntries))
}

func sampleTimelineScreenObservations(observations []Observation, snapshots []ActivitySnapshotRow, max int) []Observation {
	// A parser may return a concise body while AX still holds the path the
	// user inspected. Check each snapshot at most once to choose observations,
	// then augment only the selected bodies instead of every AX line in a run.
	byID := make(map[int64]ActivitySnapshotRow, len(snapshots))
	for _, snap := range snapshots {
		byID[snap.ID] = snap
	}
	identifierPresence := make(map[int64]bool, len(snapshots))
	filtered := make([]Observation, 0, len(observations))
	presence := make([]bool, 0, len(observations))
	for _, observation := range observations {
		hasEvidence := timelineObservationHasIdentifiers(observation)
		for _, id := range observation.SnapshotIDs {
			if hasEvidence {
				break
			}
			found, checked := identifierPresence[id]
			if !checked {
				found = len(extractActivityIdentifierTokens(byID[id].AXText, 0)) > 0
				identifierPresence[id] = found
			}
			hasEvidence = found
		}
		if !hasEvidence && strings.TrimSpace(observation.Title+observation.Body+observation.URL) == "" {
			continue
		}
		filtered = append(filtered, observation)
		presence = append(presence, hasEvidence)
	}
	selected := sampleEvidenceObservationsByPresence(filtered, presence, max)
	for i := range selected {
		seen := make(map[string]bool)
		for _, line := range strings.Split(selected[i].Body, "\n") {
			seen[strings.TrimSpace(line)] = true
		}
		var body strings.Builder
		body.WriteString(selected[i].Body)
		for _, id := range selected[i].SnapshotIDs {
			for _, line := range strings.Split(byID[id].AXText, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || seen[line] {
					continue
				}
				seen[line] = true
				if len(extractActivityIdentifierTokens(line, 0)) > 0 {
					if body.Len() > 0 {
						body.WriteByte('\n')
					}
					body.WriteString(line)
				}
			}
		}
		selected[i].Body = body.String()
	}
	return selected
}

func timelineObservationHasIdentifiers(observation Observation) bool {
	return len(extractActivityIdentifierTokens(observation.Title+"\n"+observation.Body+"\n"+observation.URL, 0)) > 0
}

// sampleEvidenceObservations reserves at most half its slots for identifiers
// spread over the run. Endpoints and evenly spaced context preserve its ending
// even when identifiers occur only at the start or in every observation.
func sampleEvidenceObservations(observations []Observation, max int) []Observation {
	presence := make([]bool, len(observations))
	for i, observation := range observations {
		presence[i] = timelineObservationHasIdentifiers(observation)
	}
	return sampleEvidenceObservationsByPresence(observations, presence, max)
}

func sampleEvidenceObservationsByPresence(observations []Observation, presence []bool, max int) []Observation {
	if max <= 0 || len(observations) <= max {
		return observations
	}
	if max == 1 {
		return observations[len(observations)-1:]
	}
	selected := make(map[int]bool, max)
	selected[0], selected[len(observations)-1] = true, true
	var evidence []int
	for i, found := range presence {
		if found {
			evidence = append(evidence, i)
		}
	}
	evidenceSlots := min(max/2, max-2, len(evidence))
	for pick := 0; pick < evidenceSlots; pick++ {
		index := 0
		if evidenceSlots > 1 {
			index = pick * (len(evidence) - 1) / (evidenceSlots - 1)
		}
		selected[evidence[index]] = true
	}
	remaining := max - len(selected)
	for pick := 0; pick < remaining; pick++ {
		selected[(pick+1)*(len(observations)-1)/(remaining+1)] = true
	}
	// Fill collisions from a full-run grid before any sequential fallback.
	for pick := 0; len(selected) < max && pick < max; pick++ {
		selected[pick*(len(observations)-1)/(max-1)] = true
	}
	for i := range observations {
		if len(selected) >= max {
			break
		}
		selected[i] = true
	}
	out := make([]Observation, 0, max)
	for i, observation := range observations {
		if selected[i] {
			out = append(out, observation)
		}
	}
	return out
}

func compactTimelineEvidenceText(text string, maxRunes int) string {
	text, _ = pii.RedactText(text)
	var evidence, other []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		if len(extractActivityIdentifierTokens(line, 0)) > 0 {
			evidence = append(evidence, line)
		} else {
			other = append(other, line)
		}
	}
	return compactText(strings.Join(append(evidence, other...), "\n"), maxRunes)
}

// sampleObservationsEvenly keeps at most max observations, evenly spaced
// across the run (always keeping the first and last), mirroring the frame
// picker's spacing.
func sampleObservationsEvenly(observations []Observation, max int) []Observation {
	if max <= 0 || len(observations) <= max {
		return observations
	}
	out := make([]Observation, 0, max)
	denom := max - 1
	if denom <= 0 {
		denom = 1
	}
	lastIdx := -1
	for pick := 0; pick < max; pick++ {
		idx := pick * (len(observations) - 1) / denom
		if idx == lastIdx {
			continue
		}
		lastIdx = idx
		out = append(out, observations[idx])
	}
	return out
}

func renderTimelineScreenContent(observations []Observation) string {
	var b strings.Builder
	for _, o := range observations {
		if activitySourceAppIsSensitive(o.RealApp, o.RealApp) {
			continue
		}
		b.WriteString("- ")
		b.WriteString(renderTimelineScreenObservation(o))
		b.WriteByte('\n')
	}
	return redactActivityModelText(b.String())
}

func renderTimelineScreenObservation(o Observation) string {
	fields := []string{fmt.Sprintf("[%s]", formatClock(o.StartMs))}
	if t := compactText(redactActivityModelText(o.Title), 160); t != "" {
		fields = append(fields, fmt.Sprintf("title=%q", t))
	}
	if u := strings.TrimSpace(redactActivityModelText(o.URL)); u != "" {
		fields = append(fields, fmt.Sprintf("url=%q", truncateRunes(u, 200)))
	}
	if body := compactTimelineEvidenceText(redactActivityModelText(o.Body), timelineSummaryScreenBodyMaxRunes); body != "" {
		fields = append(fields, fmt.Sprintf("text=%q", body))
	}
	return redactActivityModelText(strings.Join(fields, " "))
}

// Screen crops have no trustworthy per-crop masking receipt. Keep timeline
// model inputs text-only; the OCR, AX and parsed text above is redacted before
// truncation. Re-enable images only with a verified masking boundary.
func (a *App) timelineGroupSummaryImages(_ []ActivitySnapshotRow, _ int) []agent.ImageAttachment {
	return nil
}

func timelineGroupSnapshotMatches(group ActivityTimelineGroup, snap ActivitySnapshotRow) bool {
	if snap.Timestamp < group.StartTime || snap.Timestamp > group.EndTime {
		return false
	}
	if bundleID := parsedSnapshotBundleID(snap.ParsedJSON); bundleID != "" {
		return bundleID == group.AppKey || bundleID == strings.TrimPrefix(group.AppKey, "app:")
	}
	if strings.TrimSpace(snap.App) != "" {
		return strings.EqualFold(strings.TrimSpace(snap.App), strings.TrimSpace(group.AppName))
	}
	return true
}

func parsedSnapshotBundleID(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var parsed struct {
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Metadata["bundleId"])
}
