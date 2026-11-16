package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/topicday"
	"wails-gui/internal/workonit"
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
	// Send false on a Yes or Edit of work for an agent records the Yes and
	// sends nothing (the hand-off sheet's first tap); the action "send" then
	// hands it over. Omitted, a Yes hands it over at once.
	Send *bool `json:"send,omitempty"`
	// RecordID, on send, names the Work on it hand-off the person already
	// made from the computer's own sheet (an agent that hasn't joined: a deep
	// link or a copy into its app). Nothing is sent again.
	RecordID string `json:"recordId,omitempty"`
}

// workAgentKnown says whether an agent id may take a hand-off: one of the
// person's agents (B4's catalog) or an agent that joined (B5).
func (a *App) workAgentKnown(id string) bool {
	if _, ok := workonit.AgentByID(id); ok {
		return true
	}
	for _, agent := range a.GetConnectedAgents() {
		if agent.ID == id {
			return true
		}
	}
	return false
}

// workAgentName is the name Your work shows for an agent id.
func (a *App) workAgentName(id string) string {
	if agent, ok := workonit.AgentByID(id); ok {
		return agent.Name
	}
	for _, agent := range a.GetConnectedAgents() {
		if agent.ID == id {
			return agent.Name
		}
	}
	return "your agent"
}

// workLocalAgent says whether an agent runs here as a local task.
func workLocalAgent(id string) bool { return id == "claude-code" || id == "codex" }

var errWorkAgentAway = errors.New("that agent isn't connected")

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
	// workActionSend hands over work a Yes kept for the sheet (phone-map §2,
	// item 3: the Yes on the first tap, Send on the second).
	workActionSend = "send"

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
		// A brief's sentence is never in the list, but can be answered Not right.
		if item, ok = a.workBriefSentenceItem(id); !ok {
			return WorkItem{}, errWorkItemGone
		}
	}
	sentence := workIsBriefSentence(item)
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
				(input.AgentID != "" && !a.workAgentKnown(input.AgentID)) {
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
		if sentence {
			// Not right on a sentence: a No, with a reason or "Or say what's
			// right" in after. Nothing is started or withdrawn.
			if input.Action != workActionNo {
				return item, errWorkAnswerInvalid
			}
			if after := strings.TrimSpace(input.After); after != "" {
				record.Edited = &WorkEdited{Before: item.Noticed, After: after}
			}
			if _, err := ledger.append(record); err != nil {
				return item, err
			}
			break
		}
		if input.Send != nil && !*input.Send {
			if (input.Action != workActionYes && input.Action != workActionEdit) || item.Yes.Does != "agent" {
				return item, errWorkAnswerInvalid
			}
			// The Yes is the judgment; nothing goes until Send.
			record.NotSent = true
			if _, err := ledger.append(record); err != nil {
				return item, err
			}
			break
		}
		sent, err := a.dispatchWorkAnswer(ctx, item, input, meta.Surface)
		if err != nil {
			return item, err
		}
		record.TaskID, record.HandoffID = sent.TaskID, sent.HandoffID
		if sent.AgentID != "" {
			record.AgentID = sent.AgentID
		}
		if _, err := ledger.append(record); err != nil {
			return item, err
		}
	case workActionSend:
		standing, ok := answersStanding(answers)[id]
		if !ok || !standing.NotSent {
			return item, errWorkItemChanged
		}
		send := WorkAnswerInput{Action: standing.Action, Removed: standing.Removed, AgentID: standing.AgentID}
		if standing.Edited != nil {
			send.Before, send.After = standing.Edited.Before, standing.Edited.After
		}
		if input.Removed != nil {
			send.Removed = input.Removed
		}
		if input.AgentID != "" {
			if !a.workAgentKnown(input.AgentID) {
				return item, errWorkAnswerInvalid
			}
			send.AgentID = input.AgentID
		}
		// The item stood answered while the sheet was open; it is sent as the
		// open item it was.
		pending := item
		pending.State, pending.Answer = workStateOpen, nil
		var sent workSent
		var err error
		if input.RecordID != "" {
			sent, err = a.workHandedByWorkOnIt(pending, input.RecordID)
		} else {
			sent, err = a.dispatchWorkAnswer(ctx, pending, send, meta.Surface)
		}
		if err != nil {
			return item, err
		}
		record.TaskID, record.HandoffID, record.Removed, record.AgentID = sent.TaskID, sent.HandoffID, send.Removed, send.AgentID
		if sent.AgentID != "" {
			record.AgentID = sent.AgentID
		}
		record.UndoUntil = ""
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
	if input.Action != workActionUndo && input.Action != workActionSend {
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
	if sentence {
		if updated, ok := a.workBriefSentenceItem(id); ok {
			return updated, nil
		}
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

// workSent is where a Yes sent the work: a local task, or a hand-off to a
// connected agent (B5), and the agent it went to.
type workSent struct {
	TaskID    string
	HandoffID string
	AgentID   string
}

// dispatchWorkAnswer does what the answer means at the item's source.
func (a *App) dispatchWorkAnswer(ctx context.Context, item WorkItem, input WorkAnswerInput, surface string) (workSent, error) {
	switch item.Source {
	case workSourceTopic:
		verdict := input.Action
		if verdict == workActionEdit {
			// A topic's proposal starts as it was proposed.
			return workSent{}, errWorkAnswerInvalid
		}
		if len(input.Removed) > 0 {
			return workSent{}, errWorkAnswerInvalid
		}
		if verdict == workActionYes {
			// The same rule as the brief's work: the person's agent takes it.
			// The agents on this computer run the topic's own task; another
			// agent gets it in its inbox, or not from here.
			agent := input.AgentID
			if agent == "" {
				agent = a.yourAgentChoice(workonit.KindOf(workonit.Input{Kind: item.Yes.Kind}))
			}
			if agent != "" && !workLocalAgent(agent) {
				return a.handOffTopicProposal(ctx, item, agent, surface)
			}
		}
		view, err := a.answerDailyTopicProposal(ctx, item.Ref.TopicID, item.Ref.ProposalID, verdict)
		switch {
		case errors.Is(err, errDailyTopicAnswered):
			return workSent{}, errWorkItemChanged
		case errors.Is(err, topicday.ErrNotFound), errors.Is(err, errDailyTopicNotAllowed):
			return workSent{}, errWorkItemGone
		case err != nil:
			return workSent{}, err
		}
		if view.Proposal != nil {
			return workSent{TaskID: view.Proposal.TaskID, AgentID: input.AgentID}, nil
		}
		return workSent{}, nil
	case workSourcePrep:
		// Prep is work Isoform did: a Yes keeps it with the meeting and a No
		// says it isn't needed; both are recorded. It can't be rewritten here.
		if input.Action == workActionEdit {
			return workSent{}, errWorkAnswerInvalid
		}
		return workSent{}, nil
	case workSourceAgent:
		// A result is judged Useful or Not useful, not answered Yes or No.
		return workSent{}, errWorkAnswerInvalid
	case workSourceBrief:
		if input.Action != workActionYes && input.Action != workActionEdit {
			return workSent{}, nil
		}
		prompt, ok := a.workBriefOfferPrompt(ctx, item)
		if !ok {
			return workSent{}, errWorkItemGone
		}
		if input.Action == workActionEdit {
			prompt = strings.TrimSpace(input.After)
		}
		text, err := workHandOffText(prompt, item, input.Removed)
		if err != nil {
			return workSent{}, err
		}
		return a.handOffWork(item, text, input.AgentID, surface)
	}
	return workSent{}, errWorkAnswerInvalid
}

// handOffWork sends offered work to the person's agent (phone-agent v1.1.0):
// to a connected agent's inbox through Work on it (B5), else, for the agents
// on this computer, as a local task, kept in the hand-offs either way so Your
// data sees every one.
func (a *App) handOffWork(item WorkItem, text, agentID, surface string) (workSent, error) {
	in := workonit.Input{SourceKind: workonit.SourceWorkItem, SourceID: item.ID, Title: item.Work.Title, Detail: text, Kind: item.Yes.Kind}
	recordID, sentTo, err := a.handOffToConnectedAgent(in, agentID, surface)
	if err == nil {
		return workSent{HandoffID: recordID, AgentID: sentTo}, nil
	}
	if !errors.Is(err, errAgentNotConnected) {
		return workSent{}, err
	}
	local := agentID
	if local == "" {
		local = a.yourAgentChoice(workonit.KindOf(in))
	}
	if local != "" && !workLocalAgent(local) {
		return workSent{}, errWorkAgentAway
	}
	taskID, err := a.startWorkTask(item, text)
	if err != nil {
		return workSent{}, err
	}
	a.keepWorkOnItHandoff(workonit.Handoff{ID: taskID, SourceKind: workonit.SourceWorkItem, SourceID: item.ID, Kind: workonit.KindOf(in),
		AgentID: local, Method: workonit.MethodLocalTask, Text: text, SentAt: time.Now().UnixMilli(), Surface: surface})
	return workSent{TaskID: taskID, AgentID: local}, nil
}

// handOffTopicProposal hands a topic's proposal to an agent that joined, and
// keeps the topic's answer as a Yes with its agent, with no task here. An
// agent that hasn't joined can't take it from the computer.
func (a *App) handOffTopicProposal(ctx context.Context, item WorkItem, agentID, surface string) (workSent, error) {
	store, err := a.topicsDailyStore()
	if err != nil {
		return workSent{}, err
	}
	feedback, err := store.Feedback()
	if err != nil {
		return workSent{}, err
	}
	topic, proposal, err := dailyTopicProposal(store, item.Ref.TopicID, item.Ref.ProposalID)
	if err != nil {
		return workSent{}, errWorkItemGone
	}
	if proposal.State != topicday.StateProposed {
		return workSent{}, errWorkItemChanged
	}
	// The work leaves the computer: the same filter as the build, again now.
	if !a.topicsDailyChoices(feedback).Allowed(topic.ID, topic.Name, topic.Apps) {
		return workSent{}, errWorkItemGone
	}
	in := workonit.Input{SourceKind: workonit.SourceWorkItem, SourceID: item.ID, Title: item.Work.Title, Detail: proposal.Prompt, Kind: item.Yes.Kind}
	recordID, sentTo, err := a.handOffToConnectedAgent(in, agentID, surface)
	switch {
	case errors.Is(err, errAgentNotConnected):
		return workSent{}, errWorkAgentAway
	case err != nil:
		return workSent{}, err
	}
	if err := a.markDailyTopicHandedOff(topic, proposal); err != nil {
		log.Printf("[work] the topic did not record its hand-off: %v", err)
	}
	return workSent{HandoffID: recordID, AgentID: sentTo}, nil
}

// markDailyTopicHandedOff records a topic's Yes whose work went to an agent's
// inbox: the proposal is running, with no task on this computer.
func (a *App) markDailyTopicHandedOff(topic topicday.Topic, proposal topicday.Proposal) error {
	store, err := a.topicsDailyStore()
	if err != nil {
		return err
	}
	topicsDailyAnswerMu.Lock()
	defer topicsDailyAnswerMu.Unlock()
	now := time.Now().UnixMilli()
	if _, err := store.UpdateProposal(topic.ID, proposal.ID, func(_ *topicday.Topic, p *topicday.Proposal) error {
		if p.State != topicday.StateProposed {
			return errDailyTopicAnswered
		}
		p.State, p.AnsweredAt = topicday.StateRunning, now
		return nil
	}); err != nil {
		return err
	}
	if err := store.AppendFeedback(topicday.Feedback{TopicID: topic.ID, TopicName: topic.Name, ProposalID: proposal.ID,
		ProposalTitle: proposal.Title, Verdict: topicday.VerdictYes, At: now}); err != nil {
		return err
	}
	noteProposalAnswered(judgmentTopic, proposal.ID, true)
	a.emitBrowserEvent("topics-daily:updated", map[string]any{"topicId": topic.ID})
	return nil
}

// workHandedByWorkOnIt takes a hand-off the person made from the computer's
// Work on it sheet as this item's Send: it must be for this item.
func (a *App) workHandedByWorkOnIt(item WorkItem, recordID string) (workSent, error) {
	handoffs, err := a.GetWorkOnItHandoffs()
	if err != nil {
		return workSent{}, err
	}
	for _, h := range handoffs {
		if h.ID != recordID {
			continue
		}
		if h.SourceKind != workonit.SourceWorkItem || h.SourceID != item.ID {
			return workSent{}, errWorkAnswerInvalid
		}
		if item.Source == workSourceTopic {
			store, err := a.topicsDailyStore()
			if err == nil {
				if topic, proposal, err := dailyTopicProposal(store, item.Ref.TopicID, item.Ref.ProposalID); err == nil && proposal.State == topicday.StateProposed {
					if err := a.markDailyTopicHandedOff(topic, proposal); err != nil {
						log.Printf("[work] the topic did not record its hand-off: %v", err)
					}
				}
			}
		}
		return workSent{HandoffID: h.ID, AgentID: h.AgentID}, nil
	}
	return workSent{}, errWorkAnswerInvalid
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
		// The work left: a local task, or an agent's inbox, which keeps it.
		if record.TaskID != "" || record.HandoffID != "" {
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
	answers := a.answersLedger().standing()
	for i := range items {
		item := &items[i]
		record, ok := answers[item.ID]
		if !ok {
			continue
		}
		answer := &WorkAnswer{Action: record.Action, Reason: record.Reason, Edited: record.Edited, At: record.At,
			Surface: record.Surface, UndoUntil: record.UndoUntil, NotSent: record.NotSent}
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
			if record.HandoffID != "" && (record.Action == workActionYes || record.Action == workActionEdit) {
				// With a connected agent: its result comes back through the
				// task's results (B5).
				item.State = workStateWithAgent
				item.Agent = &WorkAgent{Name: a.workAgentName(record.AgentID), Since: record.At}
			}
			if record.TaskID != "" && (record.Action == workActionYes || record.Action == workActionEdit || workResultJudgment(record.Action)) {
				item.Ref.TaskID = record.TaskID
				item.State = workStateWithAgent
				name := "your agent"
				if record.AgentID != "" {
					name = a.workAgentName(record.AgentID)
				}
				item.Agent = &WorkAgent{Name: name, Since: record.At}
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
		if record.NotSent {
			item.State = workStateAnswered
			item.Answer = answer
			item.Answer.Line = item.Work.Title + ": yes · not sent"
			continue
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
