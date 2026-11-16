package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/topicday"
)

// Answers on Your work (phone redesign B3; PROTOCOL.md section 26.1): Yes, No
// with a reason, Not mine, Edit, Useful, Not useful and Undo, from the
// computer or the phone, through one function and one log. The log is the
// person's judgments, kept with where each was given.

// WorkAnswerInput is one answer, from either device.
type WorkAnswerInput struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Removed lists the What it gets ids the person took out on the hand-off
	// sheet; only removable ones (phone-agent v1.0.0 rule 3).
	Removed []string `json:"removed,omitempty"`
	// AgentID changes the agent for this one hand-off.
	AgentID string `json:"agentId,omitempty"`
}

// workAgentKnown says whether an agent id may take a hand-off. Until the
// person's agents are a setting (B4), the agents on this computer are.
var workAgentKnown = func(id string) bool { return id == "claude-code" || id == "codex" }

// workHandOffText is exactly what goes to the agent: the work (or the
// person's words) and each What it gets item the person kept.
func workHandOffText(prompt string, item WorkItem, removed []string) (string, error) {
	out := map[string]bool{}
	for _, id := range removed {
		out[id] = true
	}
	known := map[string]bool{}
	kept := []string{}
	for _, get := range item.Gets {
		known[get.ID] = true
		if out[get.ID] {
			if !get.Removable {
				return "", errWorkAnswerInvalid
			}
			continue
		}
		kept = append(kept, "- "+get.Label)
	}
	for id := range out {
		if !known[id] {
			return "", errWorkAnswerInvalid
		}
	}
	if len(kept) == 0 {
		return prompt, nil
	}
	return strings.TrimSpace(prompt) + "\n\nWhat it gets:\n" + strings.Join(kept, "\n"), nil
}

const (
	workActionYes       = "yes"
	workActionNo        = "no"
	workActionNotMine   = "not_mine"
	workActionEdit      = "edit"
	workActionUseful    = "useful"
	workActionNotUseful = "not_useful"
	workActionUndo      = "undo"

	workSurfaceComputer     = "computer"
	workSurfacePhone        = "phone"
	workSurfaceNotification = "notification"

	workEditMaxBytes = 4000
)

var (
	errWorkItemGone      = errors.New("this item is no longer in Your work")
	errWorkItemChanged   = errors.New("this item was answered or changed")
	errWorkAnswerInvalid = errors.New("this answer doesn't fit this item")
	errWorkUndoClosed    = errors.New("this answer can no longer be undone")
)

func workResultJudgment(action string) bool {
	return action == workActionUseful || action == workActionNotUseful
}

// AnswerWork records the person's answer from the computer's Today.
func (a *App) AnswerWork(id string, input WorkAnswerInput) (WorkItem, error) {
	return a.answerWork(context.Background(), id, input, workAnswerMeta{Surface: workSurfaceComputer})
}

// UndoWork takes back the item's last answer, until the next brief.
func (a *App) UndoWork(id string) (WorkItem, error) {
	return a.answerWork(context.Background(), id, WorkAnswerInput{Action: workActionUndo}, workAnswerMeta{Surface: workSurfaceComputer})
}

// workAnswerMeta says where an answer came from. Seq, when set, grows per
// phone: an answer older than the last one applied to the item is refused,
// so a pair reordered between two paths can't flip an Undo.
type workAnswerMeta struct {
	Surface  string
	DeviceID string
	Seq      uint64
}

var workAnswerMu sync.Mutex

// workStartTask starts a saved task; tests replace it.
var workStartTask = func(a *App, id string) error { return dailyTopicTaskStart(a, id) }

func (a *App) answerWork(ctx context.Context, id string, input WorkAnswerInput, meta workAnswerMeta) (WorkItem, error) {
	input.Action, input.Reason = strings.TrimSpace(input.Action), strings.TrimSpace(input.Reason)
	if len(input.Before) > workEditMaxBytes || len(input.After) > workEditMaxBytes {
		return WorkItem{}, errWorkAnswerInvalid
	}
	workAnswerMu.Lock()
	defer workAnswerMu.Unlock()
	now := time.Now()
	list, err := a.buildWorkList(ctx, now)
	if err != nil {
		return WorkItem{}, err
	}
	item, ok := workFind(list, id)
	if !ok {
		return WorkItem{}, errWorkItemGone
	}
	ledger := a.answersLedger()
	answers := ledger.entries()
	if meta.Seq > 0 {
		for _, record := range answers {
			if record.ItemID == id && record.DeviceID == meta.DeviceID && record.Seq >= meta.Seq {
				return item, errWorkItemChanged
			}
		}
	}
	record := AnswerRecord{ItemID: id, Kind: answerKindWork, Source: item.Source, Title: item.Work.Title, Action: input.Action, At: mobileTimestamp(now),
		Surface: meta.Surface, DeviceID: meta.DeviceID, Seq: meta.Seq, UndoUntil: list.UndoUntil}
	switch input.Action {
	case workActionUndo:
		undone, err := a.undoWorkAnswer(item, answers, now)
		if err != nil {
			return item, err
		}
		record.Undoes, record.UndoUntil = undone.EntryID, ""
		if _, err := ledger.append(record); err != nil {
			return item, err
		}
	case workActionYes, workActionNo, workActionNotMine, workActionEdit:
		if item.State != workStateOpen {
			return item, errWorkItemChanged
		}
		if input.Reason != "" && (input.Action != workActionNo || !workReasonAllowed(item, input.Reason)) {
			return item, errWorkAnswerInvalid
		}
		record.Reason = input.Reason
		if len(input.Removed) > 0 || input.AgentID != "" {
			if (input.Action != workActionYes && input.Action != workActionEdit) || item.Yes.Does != "agent" ||
				(input.AgentID != "" && !workAgentKnown(input.AgentID)) {
				return item, errWorkAnswerInvalid
			}
			record.Removed, record.AgentID = append([]string(nil), input.Removed...), input.AgentID
		}
		if input.Action == workActionEdit {
			if strings.TrimSpace(input.After) == "" {
				return item, errWorkAnswerInvalid
			}
			record.Edited = &WorkEdited{Before: input.Before, After: input.After}
		}
		taskID, err := a.dispatchWorkAnswer(ctx, item, input)
		if err != nil {
			return item, err
		}
		record.TaskID = taskID
		if _, err := ledger.append(record); err != nil {
			return item, err
		}
	case workActionUseful, workActionNotUseful:
		if item.State != workStateBack {
			return item, errWorkItemChanged
		}
		if _, err := ledger.append(record); err != nil {
			return item, err
		}
	default:
		return item, errWorkAnswerInvalid
	}
	if input.Action != workActionUndo {
		noteProposalAnswered(workJudgmentKind(item), id, input.Action == workActionYes || input.Action == workActionEdit || input.Action == workActionUseful)
	}
	a.emitBrowserEvent(workAnsweredEvent, id)
	after, err := a.buildWorkList(ctx, time.Now())
	if err != nil {
		return item, nil
	}
	if updated, ok := workFind(after, id); ok {
		return updated, nil
	}
	// The answer took the item out of the list (Not mine on a topic hides the
	// topic): it comes back answered, without an Undo.
	if input.Action != workActionUndo {
		item.State = workStateAnswered
		item.Answer = &WorkAnswer{Action: record.Action, Reason: record.Reason, Edited: record.Edited, At: record.At, Surface: record.Surface}
		item.Answer.Line = workAnswerLine(item.Work.Title, record.Action, record.Reason, item.State)
	}
	return item, nil
}

func workJudgmentKind(item WorkItem) string {
	if item.Source == workSourceTopic {
		return judgmentTopic
	}
	return judgmentOffer
}

func workReasonAllowed(item WorkItem, reason string) bool {
	for _, allowed := range item.Reasons {
		if allowed == reason {
			return true
		}
	}
	return false
}

// dispatchWorkAnswer does what the answer means at the item's source and
// returns the task a Yes started, if any.
func (a *App) dispatchWorkAnswer(ctx context.Context, item WorkItem, input WorkAnswerInput) (string, error) {
	switch item.Source {
	case workSourceTopic:
		verdict := input.Action
		if verdict == workActionEdit {
			// A topic's proposal starts as it was proposed.
			return "", errWorkAnswerInvalid
		}
		view, err := a.answerDailyTopicProposal(ctx, item.Ref.TopicID, item.Ref.ProposalID, verdict)
		switch {
		case errors.Is(err, errDailyTopicAnswered):
			return "", errWorkItemChanged
		case errors.Is(err, topicday.ErrNotFound), errors.Is(err, errDailyTopicNotAllowed):
			return "", errWorkItemGone
		case err != nil:
			return "", err
		}
		if view.Proposal != nil {
			return view.Proposal.TaskID, nil
		}
		return "", nil
	case workSourcePrep:
		// Prep is work Isoform did: a Yes keeps it with the meeting and a No
		// says it isn't needed; both are recorded. It can't be rewritten here.
		if input.Action == workActionEdit {
			return "", errWorkAnswerInvalid
		}
		return "", nil
	case workSourceAgent:
		// A result is judged Useful or Not useful, not answered Yes or No.
		return "", errWorkAnswerInvalid
	case workSourceBrief:
		if input.Action != workActionYes && input.Action != workActionEdit {
			return "", nil
		}
		prompt, ok := a.workBriefOfferPrompt(ctx, item)
		if !ok {
			return "", errWorkItemGone
		}
		if input.Action == workActionEdit {
			prompt = strings.TrimSpace(input.After)
		}
		text, err := workHandOffText(prompt, item, input.Removed)
		if err != nil {
			return "", err
		}
		return a.startWorkTask(item, text)
	}
	return "", errWorkAnswerInvalid
}

// workBriefOfferPrompt reads the offer's request from the brief's package.
func (a *App) workBriefOfferPrompt(ctx context.Context, item WorkItem) (string, bool) {
	svc := a.mobileBriefSource()
	if svc == nil {
		return "", false
	}
	packager, ok := svc.(mobileBriefPackagerContext)
	if !ok {
		return "", false
	}
	pkg, ok := packager.BriefPackageContext(ctx, item.Ref.BriefID)
	if !ok {
		return "", false
	}
	var phone struct {
		Offers []MobileBriefOffer `json:"offers"`
	}
	if json.Unmarshal(pkg.Bytes, &phone) != nil {
		return "", false
	}
	for _, offer := range phone.Offers {
		if offer.ID == item.Ref.OfferID && strings.TrimSpace(offer.Prompt) != "" {
			return offer.Prompt, true
		}
	}
	return "", false
}

// workBriefTaskOrigin marks a task a Yes on a brief's offer started.
const workBriefTaskOrigin = "brief-offer"

// startWorkTask saves offered work as a task and starts it, as a topic's Yes
// does. A task that cannot start is removed.
func (a *App) startWorkTask(item WorkItem, prompt string) (string, error) {
	if a.handoffStore == nil {
		return "", errMobileStoreUnavailable
	}
	input, err := validateCreateTask(CreateTaskInput{Title: dailyTopicTaskTitle(item.Work.Title), Brief: prompt})
	if err != nil {
		return "", errWorkAnswerInvalid
	}
	saved, err := a.createTaskEntryFrom(input, handoff.SourceDesktop, &handoff.Origin{Kind: workBriefTaskOrigin, Label: item.Ref.BriefID})
	if err != nil {
		return "", err
	}
	if err := workStartTask(a, saved.ID); err != nil {
		if removed, cleanupErr := a.handoffStore.DeleteIfUnchanged(saved); cleanupErr == nil && removed {
			a.emitBrowserEvent("handoff:updated", map[string]any{"id": saved.ID})
		}
		return "", err
	}
	return saved.ID, nil
}

// undoWorkAnswer finds the item's last answer still standing, for the undo
// entry that takes it back. A Yes that handed the work to an agent is not
// undone here: the task is stopped on its own page.
func (a *App) undoWorkAnswer(item WorkItem, answers []AnswerRecord, now time.Time) (AnswerRecord, error) {
	record, ok := answersStanding(answers)[item.ID]
	if !ok {
		return AnswerRecord{}, errWorkItemChanged
	}
	if until, err := time.Parse(time.RFC3339, record.UndoUntil); err == nil && now.After(until) {
		return AnswerRecord{}, errWorkUndoClosed
	}
	switch record.Action {
	case workActionYes, workActionEdit:
		if record.TaskID != "" {
			return AnswerRecord{}, errWorkUndoClosed
		}
	case workActionNo, workActionNotMine:
		if item.Source == workSourceTopic {
			if err := a.reopenDailyTopicProposal(item.Ref.TopicID, item.Ref.ProposalID); err != nil {
				return AnswerRecord{}, err
			}
		}
	}
	return record, nil
}

// reopenDailyTopicProposal puts a declined proposal back to proposed.
func (a *App) reopenDailyTopicProposal(topicID, proposalID string) error {
	store, err := a.topicsDailyStore()
	if err != nil {
		return err
	}
	topicsDailyAnswerMu.Lock()
	defer topicsDailyAnswerMu.Unlock()
	_, err = store.UpdateProposal(topicID, proposalID, func(_ *topicday.Topic, p *topicday.Proposal) error {
		if p.State != topicday.StateDeclined && p.State != topicday.StateNotMine {
			return errWorkUndoClosed
		}
		p.State, p.AnsweredAt = topicday.StateProposed, 0
		return nil
	})
	if err == nil {
		a.emitBrowserEvent("topics-daily:updated", map[string]any{"topicId": topicID})
	}
	return err
}

func workFind(list WorkList, id string) (WorkItem, bool) {
	for _, item := range list.Items {
		if item.ID == id {
			return item, true
		}
	}
	return WorkItem{}, false
}

// applyWorkAnswers gives each item its state from the answers log and, for a
// Yes that started a task, from that task.
func (a *App) applyWorkAnswers(items []WorkItem) []WorkItem {
	answers := a.answersLedger().current()
	for i := range items {
		item := &items[i]
		record, ok := answers[item.ID]
		if !ok {
			continue
		}
		answer := &WorkAnswer{Action: record.Action, Reason: record.Reason, Edited: record.Edited, At: record.At,
			Surface: record.Surface, UndoUntil: record.UndoUntil}
		switch item.Source {
		case workSourceTopic:
			// The topic store holds the state; the log adds where and why.
			if item.Answer != nil && !workResultJudgment(record.Action) {
				answer.Line = item.Answer.Line
				item.Answer = answer
			}
		case workSourcePrep:
			item.Answer, item.State = answer, workStateAnswered
			if record.Action == workActionYes {
				answer.Line = item.Work.Title + ": kept with the meeting"
			}
		case workSourceBrief:
			item.Answer = answer
			item.State = workStateAnswered
			if record.TaskID != "" && (record.Action == workActionYes || record.Action == workActionEdit || workResultJudgment(record.Action)) {
				item.Ref.TaskID = record.TaskID
				item.State = workStateWithAgent
				item.Agent = &WorkAgent{Name: "your agent", Since: record.At}
				if a.handoffStore != nil {
					if entry, err := a.handoffStore.Get(record.TaskID); err == nil {
						if state, result := dailyTopicTaskState(entry); state != topicday.StateRunning {
							item.State, item.Kind, item.Row = workStateBack, workKindResult, "result"
							item.Work.Text = result
						}
					}
				}
			}
		}
		if item.State == workStateBack && workResultJudgment(record.Action) {
			item.State = workStateAnswered
			item.Answer = answer
		}
		if item.Answer != nil && item.Answer.Line == "" {
			item.Answer.Line = workAnswerLine(item.Work.Title, item.Answer.Action, item.Answer.Reason, item.State)
		}
	}
	return items
}

// workAnswerLine is Done today's line for an answer (phone-today rule 15).
func workAnswerLine(title, action, reason, state string) string {
	switch {
	case action == workActionNo && reason == "not_needed":
		return title + ": not needed"
	case state == workStateWithAgent:
		return title + ": with your agent"
	case state == workStateBack:
		return title + ": back from your agent"
	case action == workActionUseful:
		return title + ": useful"
	case action == workActionNotUseful:
		return title + ": not useful"
	case action == workActionNotMine:
		return title + ": not yours"
	case action == workActionNo && reason == "not_now":
		return title + ": not now"
	case action == workActionNo:
		return title + ": no"
	}
	return title + ": done"
}
