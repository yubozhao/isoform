package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// A task is a thread (PROTOCOL.md 20.7b): where it came from, Isoform's
// messages, the person's replies and taps, the files it made, and one outcome.
// The Mac keeps codes, times and parameters; the phone and the Mac's own pages
// word them from their string catalogs, so a language change rewords old threads.

const (
	mobileTaskThreadTextMax   = 4096
	mobileTaskThreadEventsMax = 60
	mobileTaskReplyMax        = 4096
	mobileTaskTitleMax        = 200
	mobileTaskSummaryMax      = 200
)

// Task actions a thread's ··· menu can offer.
const (
	mobileTaskActionMarkDone = "mark_done"
	mobileTaskActionRename   = "rename"
	mobileTaskActionNotNow   = "not_now"
	mobileTaskActionMade     = "made"
	mobileTaskActionStop     = "stop"
	mobileTaskActionChoose   = "choose"
	mobileTaskActionKeepHold = "keep_hold"
	mobileTaskActionOpened   = "opened"
)

type MobileTaskOrigin struct {
	Kind  string `json:"kind"`
	At    string `json:"at"`
	Label string `json:"label,omitempty"`
	Slot  string `json:"slot,omitempty"`
}

type MobileTaskChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type MobileTaskFile struct {
	Name string `json:"name"`
}

type MobileTaskEvent struct {
	ID       string             `json:"id"`
	Kind     string             `json:"kind"`
	At       string             `json:"at"`
	Text     string             `json:"text,omitempty"`
	Choices  []MobileTaskChoice `json:"choices,omitempty"`
	ChosenID string             `json:"chosenId,omitempty"`
	File     *MobileTaskFile    `json:"file,omitempty"`
	// Code and Params let an app word a coded message itself; Text is the
	// Mac's wording in its own language.
	Code   string            `json:"code,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

type MobileTaskOutcome struct {
	Kind   string            `json:"kind"`
	By     string            `json:"by"`
	At     string            `json:"at"`
	Params map[string]string `json:"params,omitempty"`
}

func mobileTaskTime(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// mobileTaskThreadText redacts and bounds message text while keeping its
// paragraphs, unlike the single-line offer redactor.
func mobileTaskThreadText(text string, limit int) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		clean := strings.Join(strings.Fields(redactActivityModelText(line)), " ")
		if clean == "" {
			if len(out) > 0 && !blank {
				out = append(out, "")
				blank = true
			}
			continue
		}
		out = append(out, clean)
		blank = false
	}
	joined := strings.TrimSpace(strings.Join(out, "\n"))
	if len(joined) <= limit {
		return joined
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(joined[cut]) {
		cut--
	}
	return strings.TrimSpace(joined[:cut])
}

func mobileTaskOutcome(entry handoff.Entry) *MobileTaskOutcome {
	if entry.Thread == nil || entry.Thread.Outcome == nil || (entry.Status != handoff.StatusCompleted && !handoff.StoppedByYou(entry)) {
		return nil
	}
	outcome := entry.Thread.Outcome
	params := map[string]string{}
	for key, value := range outcome.Params {
		if clean := mobileOfferText(value, mobileTaskSummaryMax); clean != "" {
			params[key] = clean
		}
	}
	if outcome.Kind == handoff.OutcomeCompleted && params["summary"] == "" {
		return nil
	}
	view := &MobileTaskOutcome{Kind: outcome.Kind, By: outcome.By, At: mobileTaskTime(outcome.At)}
	if view.At == "" {
		view.At = mobileTaskTime(entry.UpdatedAt)
	}
	if len(params) > 0 {
		view.Params = params
	}
	return view
}

func mobileTaskOriginView(entry handoff.Entry) *MobileTaskOrigin {
	if entry.Thread == nil || entry.Thread.Origin == nil {
		return nil
	}
	origin := entry.Thread.Origin
	switch origin.Kind {
	case handoff.OriginUpNext, handoff.OriginBriefChip, handoff.OriginChat, handoff.OriginList, handoff.OriginRoutine:
	default:
		return nil
	}
	return &MobileTaskOrigin{
		Kind: origin.Kind, At: mobileTaskTime(origin.At),
		Label: mobileOfferText(origin.Label, mobileTaskSummaryMax), Slot: mobileTaskTime(origin.Slot),
	}
}

// mobileTaskUnread reports whether a Needs you task changed since the person
// last opened its thread.
func mobileTaskUnread(entry handoff.Entry) bool {
	if !handoff.IsChipStatus(entry.Status) {
		return false
	}
	opened := int64(0)
	if entry.Thread != nil {
		opened = entry.Thread.OpenedAt
	}
	return entry.UpdatedAt > opened
}

// mobileTaskActions lists the ··· menu items that apply to a task right now.
func mobileTaskActions(entry handoff.Entry, events []MobileTaskEvent) []string {
	actions := []string{}
	if handoff.IsChipStatus(entry.Status) || entry.Status == handoff.StatusAborted && entry.StoppedBy == handoff.StoppedBySystem && entry.StopCause == handoff.CauseLoopGuard {
		actions = append(actions, mobileTaskActionMarkDone)
	}
	actions = append(actions, mobileTaskActionRename)
	if handoff.IsChipStatus(entry.Status) {
		actions = append(actions, mobileTaskActionNotNow)
	}
	for _, event := range events {
		if event.File != nil {
			actions = append(actions, mobileTaskActionMade)
			break
		}
	}
	if entry.Status == handoff.StatusInProgress {
		actions = append(actions, mobileTaskActionStop)
	}
	return actions
}

// mobileTaskEvents projects the stored thread, plus Isoform's answers to the
// person's replies read from the task's own chat, in time order.
func (a *App) mobileTaskEvents(ctx context.Context, entry handoff.Entry) []MobileTaskEvent {
	if entry.Thread == nil {
		return []MobileTaskEvent{}
	}
	sensitive := pii.IsSensitiveAppName(entry.AppName)
	events := make([]MobileTaskEvent, 0, len(entry.Thread.Events))
	lastStop := -1
	for i, event := range entry.Thread.Events {
		if event.Code == handoff.EventCodeStopped {
			lastStop = i
		}
	}
	for i, event := range entry.Thread.Events {
		view := MobileTaskEvent{ID: event.ID, Kind: event.Kind, At: mobileTaskTime(event.At), ChosenID: event.ChosenID}
		switch event.Kind {
		case handoff.EventIsoform, handoff.EventYou, handoff.EventLine:
			if sensitive && event.Kind != handoff.EventYou {
				continue
			}
			text := event.Text
			if event.Code == "list_start" && event.Kind == handoff.EventIsoform && entry.Thread.Origin != nil && entry.Thread.Origin.Kind == handoff.OriginList {
				text = brainDumpFirstLine(entry.ListItem, a.currentUILanguage(), time.UnixMilli(entry.CreatedAt))
			}
			switch event.Code {
			case "list_project":
				text = "Which project is this for?"
				if a.currentUILanguage() == "zh-CN" {
					text = "这是哪个项目的？"
				}
			case "list_start_failed":
				text = "I couldn't start this task. Please try again."
				if a.currentUILanguage() == "zh-CN" {
					text = "无法开始这项任务，请重试。"
				}
			case handoff.EventCodeStopped, handoff.EventCodeStoppedNoAssistant, handoff.EventCodeStartedAgain:
				text = handoffStopEventText(event, a.currentUILanguage())
				if i == lastStop {
					if sentence := taskRepliesStopSentence(entry, a.currentUILanguage() == "zh-CN"); sentence != "" {
						text += " " + sentence
					}
				}
				view.Code, view.Params = event.Code, mobileStopEventParams(event.Params)
			}
			view.Text = mobileTaskThreadText(text, mobileTaskThreadTextMax)
			if view.Text == "" {
				continue
			}
			for _, choice := range event.Choices {
				label := mobileOfferText(choice.Label, 120)
				if label != "" && mobileValidID(choice.ID) {
					view.Choices = append(view.Choices, MobileTaskChoice{ID: choice.ID, Label: label})
				}
			}
		case handoff.EventFile:
			name := mobileOfferText(filepath.Base(event.Path), 120)
			if sensitive || name == "" || name == "." || name == string(filepath.Separator) {
				continue
			}
			view.File = &MobileTaskFile{Name: name}
		default:
			continue
		}
		events = append(events, view)
		if event.Kind == handoff.EventYou && event.ChatSeq > 0 && !sensitive {
			if answer, ok := a.mobileTaskReplyAnswer(ctx, entry.ChatSessionID, event.ChatSeq); ok {
				events = append(events, answer)
			}
		}
	}
	if entry.Status == handoff.StatusActive && entry.ListItem != nil && entry.ListItem.Who == "draft" && entry.ListItem.DraftTaskID == "" && !mobileGoalsNow().Before(time.UnixMilli(entry.CreatedAt).Add(24*time.Hour)) {
		text := "A draft hasn't appeared yet."
		if a.currentUILanguage() == "zh-CN" {
			text = "草稿还没有出现。"
		}
		events = append(events, MobileTaskEvent{ID: "list-draft-waiting", Kind: handoff.EventIsoform, At: mobileTaskTime(entry.CreatedAt + (24 * time.Hour).Milliseconds()), Text: text})
	}
	if len(events) > mobileTaskThreadEventsMax {
		events = events[len(events)-mobileTaskThreadEventsMax:]
	}
	return events
}

// mobileTaskReplyAnswer reads Isoform's answer to one reply: the last assistant
// text of that turn in the task's chat. Nothing is shown while the turn runs.
func (a *App) mobileTaskReplyAnswer(ctx context.Context, sessionID string, seq int64) (MobileTaskEvent, bool) {
	store := a.storeForMobile()
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return MobileTaskEvent{}, false
	}
	page, err := store.LoadStreamItemsAfter(ctx, sessionID, int(seq-1), 50)
	if err != nil {
		return MobileTaskEvent{}, false
	}
	var answer *storage.StreamItemRow
	for i := range page.Items {
		item := page.Items[i]
		if item.Kind == "user" {
			break
		}
		if item.Kind == "assistant" && strings.TrimSpace(item.Content) != "" {
			answer = &page.Items[i]
		}
	}
	if answer == nil {
		return MobileTaskEvent{}, false
	}
	clean, _ := parseDeliverableRefs(stripHandoffResultBlock(answer.Content))
	text := mobileTaskThreadText(clean, mobileTaskThreadTextMax)
	if text == "" {
		return MobileTaskEvent{}, false
	}
	return MobileTaskEvent{ID: "answer-" + answer.ID, Kind: handoff.EventIsoform, At: mobileTaskTime(answer.CreatedAt), Text: text}, true
}

// recordHandoffThreadResult adds a finished run's result to the task's thread:
// Isoform's closing message, a chip for each file it made and, when the work
// verified, the outcome sentence's summary. It runs inside the store mutation.
func recordHandoffThreadResult(e *handoff.Entry, transcript string, now int64) {
	clean, refs := parseDeliverableRefs(stripHandoffResultBlock(transcript))
	clean = strings.TrimSpace(clean)
	if e.Thread == nil {
		e.Thread = &handoff.Thread{}
	}
	if clean != "" {
		e.Thread.AppendEvent(handoff.Event{ID: "result-" + e.AttemptID, Kind: handoff.EventIsoform, At: now, Text: clean})
	}
	for i, ref := range refs {
		if strings.TrimSpace(ref.Path) == "" {
			continue
		}
		e.Thread.AppendEvent(handoff.Event{ID: "file-" + e.AttemptID + "-" + string(rune('a'+i%26)), Kind: handoff.EventFile, At: now, Path: ref.Path})
	}
	if e.Status == handoff.StatusCompleted && e.Thread.Outcome == nil {
		if summary := handoffResultSummary(clean); summary != "" {
			e.Thread.Outcome = &handoff.Outcome{Kind: handoff.OutcomeCompleted, By: handoff.OutcomeByIsoform, At: now,
				Params: map[string]string{"summary": summary}}
		}
	}
}

// handoffResultSummary is the first sentence of a run's closing message, used
// as the outcome line when no event-specific outcome exists.
func handoffResultSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if line, _, found := strings.Cut(text, "\n"); found {
		text = line
	}
	text = strings.TrimLeft(strings.TrimSpace(text), "#*- ")
	for i, r := range text {
		if r == '.' || r == '!' || r == '?' || r == '。' || r == '！' || r == '？' {
			text = text[:i+utf8.RuneLen(r)]
			break
		}
	}
	if len(text) > mobileTaskSummaryMax {
		return ""
	}
	return strings.TrimSpace(text)
}

func (bridge *mobileBridge) taskForWrite(w http.ResponseWriter, id string) (handoff.Entry, bool) {
	if bridge.app == nil || bridge.app.handoffStore == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return handoff.Entry{}, false
	}
	entry, err := bridge.app.handoffStore.Get(id)
	if err != nil {
		mobileJSON(w, http.StatusNotFound, nil, "this task is no longer on your computer")
		return handoff.Entry{}, false
	}
	subjects, err := mobileGoalSubjects(bridge.app)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return handoff.Entry{}, false
	}
	if _, visible := mobileTaskDetail(entry, subjects); !visible {
		mobileJSON(w, http.StatusNotFound, nil, "this task is no longer on your computer")
		return handoff.Entry{}, false
	}
	return entry, true
}

// serveTaskMessage keeps a reply in its task. A task with its own chat gets
// the reply as its next turn, and Isoform's answer is read back into the
// thread; a task that has not started keeps the reply for when it does.
func (bridge *mobileBridge) serveTaskMessage(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, id string) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Text           string `json:"text"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	text := strings.TrimSpace(input.Text)
	if !valid || !mobileValidID(id) || text == "" || len(text) > mobileTaskReplyMax {
		mobileJSON(w, http.StatusBadRequest, nil, "a task id, an idempotency key and a reply of 1 to 4096 bytes are required")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		bridge.performTaskMessage(w, id, text, input.IdempotencyKey)
	})
}

// performTaskMessage is shared by the phone route and desktop bindings.
func (bridge *mobileBridge) performTaskMessage(w http.ResponseWriter, id, text, key string) {
	entry, ok := bridge.taskForWrite(w, id)
	if !ok {
		return
	}
	if entry.Status == handoff.StatusInProgress || taskStoppedForReplies(entry) {
		// The run reads it at its next step (tasks-review v1.0.5); a stopped
		// task takes it on Start again (v1.0.12).
		bridge.queueTaskReply(w, entry, text, key)
		return
	}
	app := bridge.app
	now := mobileGoalsNow().UnixMilli()
	var seq int64
	if entry.ChatSessionID != "" && app.chatSessionExists(entry.ChatSessionID) {
		store := app.storeForMobile()
		if store == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
			return
		}
		row, found, err := store.MobileChat(bridge.ctx, entry.ChatSessionID)
		if err != nil || !found {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "this task's chat could not be read")
			return
		}
		_, userSeq, err := bridge.startChatTurn(row, text, nil)
		if errors.Is(err, errMobileChatBusy) {
			mobileJSON(w, http.StatusConflict, nil, "Isoform is still working on this step; send your reply when it ends")
			return
		}
		if err != nil {
			log.Printf("[mobile-tasks] reply turn failed: %v", err)
			mobileJSON(w, http.StatusServiceUnavailable, nil, "your reply could not reach this task; try again")
			return
		}
		seq = userSeq
	}
	saved, err := app.handoffStore.MutateIfStatus(id, nil, func(e *handoff.Entry) {
		if e.Thread == nil {
			e.Thread = &handoff.Thread{}
		}
		e.Thread.AppendEvent(handoff.Event{ID: uuid.NewString(), Kind: handoff.EventYou, At: now, Text: text, ChatSeq: seq})
		e.Thread.OpenedAt = now
		if seq == 0 && e.Status == handoff.StatusActive {
			e.Body = strings.TrimRight(e.Body, "\n") + "\n\n## Notes from you\n\n" + text + "\n"
		}
	})
	if err != nil {
		mobileJSON(w, http.StatusConflict, nil, "this task changed; refresh and try again")
		return
	}
	app.emitBrowserEvent("handoff:updated", map[string]any{"id": id, "status": string(saved.Status)})
	bridge.writeTaskDetail(w, saved)
}

// serveTaskAction runs one ··· menu item or a tapped choice.
type taskActionInput struct {
	IdempotencyKey string `json:"idempotencyKey"`
	Action         string `json:"action"`
	Title          string `json:"title,omitempty"`
	EventID        string `json:"eventId,omitempty"`
	ChoiceID       string `json:"choiceId,omitempty"`
	// Text is the person's edit of a draft, sent in its place.
	Text          string `json:"text,omitempty"`
	DraftRevision string `json:"draftRevision,omitempty"`
	// QueuedAt is when the phone queued a send that waited for a sleeping computer
	// (PROTOCOL.md section 23). Only a send reads it.
	QueuedAt string `json:"queuedAt,omitempty"`
}

func (bridge *mobileBridge) serveTaskAction(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, id string) {
	var input taskActionInput
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid || !mobileValidID(id) {
		mobileJSON(w, http.StatusBadRequest, nil, "a valid task id and idempotency key are required")
		return
	}
	if !validateTaskAction(w, id, input) {
		return
	}
	queuedAt, ok := mobileChatQueuedAt(input.QueuedAt, time.Now())
	if !ok {
		mobileJSON(w, http.StatusBadRequest, nil, "the waiting fields are not valid")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		// A send that waited too long needs a fresh yes (PROTOCOL.md section 23). The
		// refusal is not stored, so the phone's new request with a new key can run.
		if input.Action == "send" && mobileSendNeedsConfirmation(queuedAt, time.Now()) {
			data := map[string]string{"code": mobileConfirmSendCode}
			if name := bridge.mobileSendRecipientName(id); name != "" {
				data["person"] = name
			}
			mobileJSON(w, http.StatusConflict, data, mobileConfirmSendCode)
			return
		}
		bridge.performTaskAction(w, device, id, input, engagement.ChannelPush)
	})
}

// performTaskAction keeps the phone and desktop task operations identical.
func validateTaskAction(w http.ResponseWriter, id string, input taskActionInput) bool {
	title := strings.TrimSpace(input.Title)
	if strings.HasPrefix(id, mobileDraftTaskPrefix) {
		text := strings.TrimSpace(input.Text)
		if len(text) > mainChatEditedDraftMax {
			mobileJSON(w, http.StatusBadRequest, nil, "the reply is too long to send")
			return false
		}
		return true
	}
	switch input.Action {
	case mobileTaskActionMarkDone, mobileTaskActionNotNow, mobileTaskActionOpened, mobileTaskActionKeepHold:
	case "send":
		if len(strings.TrimSpace(input.Text)) > mainChatEditedDraftMax {
			mobileJSON(w, http.StatusBadRequest, nil, "the reply is too long to send")
			return false
		}
	case mobileTaskActionRename:
		if title == "" || len(title) > mobileTaskTitleMax || strings.ContainsAny(title, "\r\n") {
			mobileJSON(w, http.StatusBadRequest, nil, "a title of 1 to 200 bytes on one line is required")
			return false
		}
	case mobileTaskActionRunEnded:
	case mobileTaskActionReplyUndo, mobileTaskActionReplyLeaveOut, mobileTaskActionReplyUse, mobileTaskActionReplyUseAgain,
		mobileTaskActionReplyChange, mobileTaskActionStepSkip, mobileTaskActionStepRedo, mobileTaskActionStepChange:
		if !mobileValidID(input.EventID) {
			mobileJSON(w, http.StatusBadRequest, nil, "a reply or step id is required")
			return false
		}
	case mobileTaskActionChoose:
		if !mobileValidID(input.EventID) || !mobileValidID(input.ChoiceID) {
			mobileJSON(w, http.StatusBadRequest, nil, "a message id and a choice id are required")
			return false
		}
	default:
		mobileJSON(w, http.StatusBadRequest, nil, "unknown task action")
		return false
	}
	return true
}

func (bridge *mobileBridge) performTaskAction(w http.ResponseWriter, device mobileDeviceRecord, id string, input taskActionInput, channel engagement.Channel) {
	if !validateTaskAction(w, id, input) {
		return
	}
	if strings.HasPrefix(id, mobileDraftTaskPrefix) {
		bridge.performDraftTaskAction(w, device, id, input.Action, strings.TrimSpace(input.Text), input.DraftRevision, channel)
		return
	}
	title := strings.TrimSpace(input.Title)
	if input.Action == mobileTaskActionChoose {
		bridge.app.taskChoiceMu.Lock()
		defer bridge.app.taskChoiceMu.Unlock()
	}
	entry, ok := bridge.taskForWrite(w, id)
	if !ok {
		return
	}
	if input.Action == "send" {
		bridge.performLinkedListDraftSend(w, device, entry, input, channel)
		return
	}
	if taskReplyAction(input.Action) {
		bridge.performTaskReplyAction(w, entry, input)
		return
	}
	app := bridge.app
	now := mobileGoalsNow().UnixMilli()
	if input.Action == mobileTaskActionChoose && input.EventID == brainDumpProjectEvent && listHandoffBackend(&entry) != "" {
		bridge.chooseBrainDumpProject(w, entry, input.ChoiceID, now)
		return
	}
	var choiceSeq int64
	if input.Action == mobileTaskActionChoose {
		// Validate a copy before starting the turn. Chat startup reads the
		// handoff store, so it must run outside a store mutation.
		preview := handoff.Thread{}
		if entry.Thread != nil {
			preview = *entry.Thread
			preview.Events = append([]handoff.Event(nil), entry.Thread.Events...)
		}
		label, err := preview.Choose(input.EventID, input.ChoiceID, now, uuid.NewString())
		if err != nil {
			mobileJSON(w, http.StatusConflict, nil, "that choice is no longer open")
			return
		}
		if entry.ChatSessionID != "" && app.chatSessionExists(entry.ChatSessionID) {
			store := app.storeForMobile()
			if store == nil {
				mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
				return
			}
			row, found, err := store.MobileChat(bridge.ctx, entry.ChatSessionID)
			if err != nil || !found {
				mobileJSON(w, http.StatusServiceUnavailable, nil, "this task's chat could not be read")
				return
			}
			_, choiceSeq, err = bridge.startChatTurn(row, label, nil)
			if errors.Is(err, errMobileChatBusy) {
				mobileJSON(w, http.StatusConflict, nil, "Isoform is still working on this step; send your reply when it ends")
				return
			}
			if err != nil {
				log.Printf("[mobile-tasks] choice turn failed: %v", err)
				mobileJSON(w, http.StatusServiceUnavailable, nil, "your reply could not reach this task; try again")
				return
			}
		}
	}
	var allowed []handoff.Status
	var mutate func(e *handoff.Entry) error
	switch input.Action {
	case mobileTaskActionMarkDone:
		allowed = []handoff.Status{handoff.StatusActive, handoff.StatusBlocked, handoff.StatusNeedsReview, handoff.StatusStale}
		if entry.Status == handoff.StatusAborted && entry.StoppedBy == handoff.StoppedBySystem && entry.StopCause == handoff.CauseLoopGuard {
			allowed = append(allowed, handoff.StatusAborted)
		}
		upcoming := app.upcomingHoldRefs(entry)
		mutate = func(e *handoff.Entry) error {
			if e.Status == handoff.StatusAborted && (e.StoppedBy != handoff.StoppedBySystem || e.StopCause != handoff.CauseLoopGuard) {
				return errors.New("task changed before marking done")
			}
			thread := ensureThread(e)
			thread.Outcome = &handoff.Outcome{Kind: handoff.OutcomeMarkedDone, By: handoff.OutcomeByYou, At: now}
			for i := range thread.Leftovers {
				if thread.Leftovers[i].Kind == handoff.LeftoverCalendarHold && thread.Leftovers[i].State == handoff.LeftoverPlaced && upcoming[thread.Leftovers[i].Ref] {
					thread.Leftovers[i].State = handoff.LeftoverAsked
				}
			}
			e.Status = handoff.StatusCompleted
			return nil
		}
	case mobileTaskActionKeepHold:
		mutate = func(e *handoff.Entry) error {
			kept := false
			for i := range ensureThread(e).Leftovers {
				if leftover := &e.Thread.Leftovers[i]; leftover.Kind == handoff.LeftoverCalendarHold && leftover.State == handoff.LeftoverAsked {
					leftover.State, kept = handoff.LeftoverKept, true
				}
			}
			if !kept {
				return errors.New("no hold to keep")
			}
			return nil
		}
	case mobileTaskActionRename:
		mutate = func(e *handoff.Entry) error { e.Title = title; return nil }
	case mobileTaskActionChoose:
		mutate = func(e *handoff.Entry) error {
			if e.ChatSessionID != entry.ChatSessionID || (entry.Status == handoff.StatusActive && e.Status != entry.Status && choiceSeq == 0) {
				return errors.New("task changed while choosing")
			}
			thread := ensureThread(e)
			label, err := thread.Choose(input.EventID, input.ChoiceID, now, uuid.NewString())
			if err != nil {
				return err
			}
			thread.Events[len(thread.Events)-1].ChatSeq = choiceSeq
			if choiceSeq == 0 && e.Status == handoff.StatusActive {
				e.Body = strings.TrimRight(e.Body, "\n") + "\n\n## Notes from you\n\n" + label + "\n"
			}
			return nil
		}
	}
	var actionErr error
	var saved handoff.Entry
	var err error
	switch input.Action {
	case mobileTaskActionOpened:
		// Bookkeeping, not news: the task keeps its update time and place.
		saved, err = app.handoffStore.MutateThread(id, func(thread *handoff.Thread) {
			thread.OpenedAt = now
		})
	case mobileTaskActionNotNow:
		saved, err = app.putOffHandoff(id, time.UnixMilli(now), 0)
	default:
		saved, err = app.handoffStore.MutateIfStatus(id, allowed, func(e *handoff.Entry) {
			actionErr = mutate(e)
		})
	}
	if actionErr != nil {
		if errors.Is(actionErr, handoff.ErrChoiceUnavailable) {
			mobileJSON(w, http.StatusConflict, nil, "that choice is no longer open")
		} else {
			mobileJSON(w, http.StatusConflict, nil, "this task changed; refresh and try again")
		}
		return
	}
	if err != nil {
		mobileJSON(w, http.StatusConflict, nil, "this task changed; refresh and try again")
		return
	}
	app.emitBrowserEvent("handoff:updated", map[string]any{"id": id, "status": string(saved.Status)})
	bridge.writeTaskDetail(w, saved)
}

// upcomingHoldRefs lists the task's calendar holds that are on the calendar
// and have not started. Only those get the clean-up ask.
func (a *App) upcomingHoldRefs(entry handoff.Entry) map[string]bool {
	refs := map[string]bool{}
	if entry.Thread == nil {
		return refs
	}
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return refs
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, leftover := range entry.Thread.Leftovers {
		if leftover.Kind != handoff.LeftoverCalendarHold {
			continue
		}
		if record, err := c.record(leftover.Ref); err == nil && (record.State == "held" || record.State == "moved") && record.Start.After(c.now()) {
			refs[leftover.Ref] = true
		}
	}
	return refs
}

func ensureThread(e *handoff.Entry) *handoff.Thread {
	if e.Thread == nil {
		e.Thread = &handoff.Thread{}
	}
	return e.Thread
}

// performLinkedListDraftSend delegates to the existing source-checked draft
// action. Only its confirmed outcome can complete the list task.
func (bridge *mobileBridge) performLinkedListDraftSend(w http.ResponseWriter, device mobileDeviceRecord, entry handoff.Entry, input taskActionInput, channel engagement.Channel) {
	if entry.ListItem == nil || entry.ListItem.Who != "draft" || entry.ListItem.DraftTaskID == "" {
		mobileJSON(w, http.StatusBadRequest, nil, "this task has no reply ready to send")
		return
	}
	switch entry.Status {
	case handoff.StatusActive, handoff.StatusInProgress, handoff.StatusBlocked, handoff.StatusNeedsReview, handoff.StatusCompleted:
	default:
		mobileJSON(w, http.StatusConflict, nil, "this task is no longer open")
		return
	}
	draft, found, err := bridge.app.mobileDraftTask(bridge.ctx, entry.ListItem.DraftTaskID)
	item := BrainDumpItem{Who: "draft", Person: entry.ListItem.Person, Title: entry.ListItem.Original}
	if err != nil || !found || matchBrainDumpDraftWithDone(bridge.app, item, []mobileDraftTask{draft}, true) == nil {
		mobileJSON(w, http.StatusConflict, nil, "this draft is no longer available")
		return
	}
	if entry.Status == handoff.StatusCompleted && !draft.done() {
		mobileJSON(w, http.StatusConflict, nil, "this task is no longer open")
		return
	}
	captured := &mobileCapturedResponse{header: http.Header{}}
	bridge.performDraftTaskAction(captured, device, entry.ListItem.DraftTaskID, "send", strings.TrimSpace(input.Text), input.DraftRevision, channel)
	if captured.status < 200 || captured.status >= 300 {
		mobileReplay(w, captured.status, strings.TrimSuffix(captured.body.String(), "\n"))
		return
	}
	var response struct {
		Data MobileTaskDetail `json:"data"`
	}
	if err := json.Unmarshal(captured.body.Bytes(), &response); err != nil || response.Data.Outcome == nil {
		mobileJSON(w, http.StatusConflict, nil, "the reply outcome is unavailable")
		return
	}
	outcome := response.Data.Outcome
	at, err := time.Parse(time.RFC3339, outcome.At)
	if err != nil {
		mobileJSON(w, http.StatusConflict, nil, "the reply outcome is unavailable")
		return
	}
	saved, err := bridge.app.handoffStore.CompleteLinkedDraft(entry.ID, entry.ListItem.DraftTaskID, handoff.Outcome{
		Kind: outcome.Kind, By: outcome.By, At: at.UnixMilli(), Params: outcome.Params,
	})
	if err != nil {
		mobileJSON(w, http.StatusConflict, nil, "the reply was handled but this task could not be updated")
		return
	}
	bridge.app.emitBrowserEvent("handoff:updated", map[string]any{"id": entry.ID, "status": string(saved.Status)})
	bridge.writeTaskDetail(w, saved)
}

func (bridge *mobileBridge) writeTaskDetail(w http.ResponseWriter, entry handoff.Entry) {
	subjects, err := mobileGoalSubjects(bridge.app)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	detail, visible := bridge.app.mobileTaskThreadDetail(bridge.ctx, entry, subjects)
	if !visible {
		mobileJSON(w, http.StatusNotFound, nil, "this task is no longer on your computer")
		return
	}
	mobileJSON(w, http.StatusOK, detail, "")
}

func (a *App) mobileTaskThreadDetail(ctx context.Context, entry handoff.Entry, subjects []string) (MobileTaskDetail, bool) {
	detail, visible := mobileTaskDetail(a.mobileHoldTaskProjection(entry), subjects)
	if !visible {
		return MobileTaskDetail{}, false
	}
	detail.Origin = mobileTaskOriginView(entry)
	detail.Events = a.mobileTaskEvents(ctx, entry)
	detail.Actions = mobileTaskActions(entry, detail.Events)
	detail.Replies = mobileTaskReplies(entry)
	if entry.Thread != nil {
		detail.EffectSteps = mobileTaskEffectSteps(entry.Thread.Steps)
		detail.Skipped = mobileTaskEffectSteps(entry.Thread.Skipped)
	}
	// A task's calendar hold reaches the phone only while calendar holds are switched on.
	if entry.Thread != nil && featureOn(featureCalendarHolds) {
		for i := len(entry.Thread.Leftovers) - 1; i >= 0; i-- {
			leftover := entry.Thread.Leftovers[i]
			if leftover.Kind != handoff.LeftoverCalendarHold {
				continue
			}
			if status, err := a.GetHoldStatus(leftover.Ref); err == nil && !mobileTaskEchoesMail(status.Title, subjects) {
				detail.Hold = &status
				detail.HoldCleanup = leftover.State == handoff.LeftoverAsked && a.upcomingHoldRefs(entry)[leftover.Ref]
			}
			break
		}
	}
	if entry.ListItem != nil && entry.ListItem.Who == "draft" && entry.ListItem.DraftTaskID != "" && entry.Status != handoff.StatusCompleted {
		draft, found, err := a.mobileDraftTask(ctx, entry.ListItem.DraftTaskID)
		item := BrainDumpItem{Who: "draft", Person: entry.ListItem.Person, Title: entry.ListItem.Original}
		if err == nil && found && matchBrainDumpDraft(a, item, []mobileDraftTask{draft}) != nil {
			ready := draft.detail(a)
			detail.Draft = ready.Draft
			detail.Status, detail.Group = ready.Status, ready.Group
			detail.Actions = append(ready.Actions, mobileTaskActionRename)
			detail.Events = append(detail.Events, ready.Events...)
		}
	}
	return detail, true
}

const (
	mobileTasksScheduledWindow = 48 * time.Hour
	mobileTasksScheduledMax    = 5
)

// MobileScheduledTask is a row in Tasks › Scheduled: a brief or one of the
// person's routines, at its next run (20.7b).
type MobileScheduledTask struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Slot  string `json:"slot,omitempty"`
	Title string `json:"title,omitempty"`
	At    string `json:"at"`
}

// mobileListReminders projects the person's own still-open list tasks, including
// untimed ones, without the 48-hour limit used for routines.
func mobileListReminders(entries []handoff.Entry) []MobileScheduledTask {
	out := make([]MobileScheduledTask, 0)
	for _, entry := range entries {
		if entry.Status != handoff.StatusActive || entry.ListItem == nil || entry.ListItem.Who != "reminder" || !mobileValidID(entry.ID) {
			continue
		}
		title := mobileOfferText(entry.Title, mobileTaskTitleMax)
		if title == "" {
			continue
		}
		out = append(out, MobileScheduledTask{ID: entry.ID, Kind: "reminder", Title: title, At: entry.ListItem.When})
	}
	return out
}

// mobileScheduledTasks lists the briefs and enabled routines due in the next
// 48 hours, soonest first. The app's own news job and anything that echoes a
// held mail subject stay off the phone.
func mobileScheduledTasks(items []UpcomingItem, subjects []string, now time.Time) []MobileScheduledTask {
	out := []MobileScheduledTask{}
	end := now.Add(mobileTasksScheduledWindow).UnixMilli()
	for _, item := range items {
		if item.NextRunAt <= now.UnixMilli() || item.NextRunAt > end || item.System {
			continue
		}
		row := MobileScheduledTask{At: mobileTaskTime(item.NextRunAt)}
		switch item.Kind {
		case upcomingKindBrief:
			if item.Slot == "" {
				continue
			}
			row.ID, row.Kind, row.Slot = "brief."+item.Slot, "brief", item.Slot
		case upcomingKindSchedule:
			title := mobileOfferText(item.Name, mobileTaskTitleMax)
			if !item.Enabled || title == "" || !mobileValidID(item.JobID) || mobileTaskEchoesMail(item.Name, subjects) {
				continue
			}
			row.ID, row.Kind, row.Title = "routine."+item.JobID, "routine", title
		default:
			continue
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	if len(out) > mobileTasksScheduledMax {
		out = out[:mobileTasksScheduledMax]
	}
	return out
}

// mobileTaskRunner names who runs one-time work on this computer: the
// person's own Claude Code or Codex when one is set up, otherwise nothing
// (the phone says "On your computer").
func mobileTaskRunner(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude-code":
		return "claude_code"
	case "codex":
		return "codex"
	}
	return ""
}
