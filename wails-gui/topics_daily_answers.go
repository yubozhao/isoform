package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/topicday"
)

// Answers to a topic's proposed work. Yes runs it as a Task (createTaskEntryFrom,
// which Claude Code runs) and the task's result comes back to the topic; no and
// "not mine" are stored as feedback the next build reads. Every answer is
// feedback, yes included.

// topicsDailyTaskOrigin marks a Task that came from a topic's proposal.
const topicsDailyTaskOrigin = "topic"

var errDailyTopicNotAllowed = errors.New("this topic is no longer shown")

// AnswerDailyTopicProposal records yes, no or "not mine" for a proposal.
func (a *App) AnswerDailyTopicProposal(topicID, proposalID, verdict string) (DailyTopicView, error) {
	return a.answerDailyTopicProposal(context.Background(), topicID, proposalID, verdict)
}

func (a *App) answerDailyTopicProposal(ctx context.Context, topicID, proposalID, verdict string) (DailyTopicView, error) {
	switch verdict {
	case topicday.VerdictYes, topicday.VerdictNo, topicday.VerdictNotMine:
	default:
		return DailyTopicView{}, errors.New("the answer must be yes, no or not_mine")
	}
	if !a.topicsDailyEnabled(ctx) {
		return DailyTopicView{}, errDailyTopicNotAllowed
	}
	store, err := a.topicsDailyStore()
	if err != nil {
		return DailyTopicView{}, err
	}
	feedback, err := store.Feedback()
	if err != nil {
		return DailyTopicView{}, err
	}
	topicsDailyAnswerMu.Lock()
	defer topicsDailyAnswerMu.Unlock()
	topic, proposal, err := dailyTopicProposal(store, topicID, proposalID)
	if err != nil {
		return DailyTopicView{}, err
	}
	if proposal.State != topicday.StateProposed {
		return DailyTopicView{}, errDailyTopicAnswered
	}
	taskID := ""
	if verdict == topicday.VerdictYes {
		// The task leaves the computer to the agent: the same filter as the
		// build, applied again now.
		if !a.topicsDailyChoices(feedback).Allowed(topic.ID, topic.Name, topic.Apps) {
			return DailyTopicView{}, errDailyTopicNotAllowed
		}
		if taskID, err = a.startDailyTopicTask(topic, proposal); err != nil {
			return DailyTopicView{}, err
		}
	}
	now := time.Now().UnixMilli()
	saved, err := store.UpdateProposal(topicID, proposalID, func(_ *topicday.Topic, p *topicday.Proposal) error {
		switch verdict {
		case topicday.VerdictYes:
			p.State, p.TaskID = topicday.StateRunning, taskID
		case topicday.VerdictNo:
			p.State = topicday.StateDeclined
		case topicday.VerdictNotMine:
			p.State = topicday.StateNotMine
		}
		p.AnsweredAt = now
		return nil
	})
	if err != nil {
		return DailyTopicView{}, err
	}
	if err := store.AppendFeedback(topicday.Feedback{TopicID: topic.ID, TopicName: topic.Name, ProposalID: proposalID,
		ProposalTitle: proposal.Title, Verdict: verdict, At: now}); err != nil {
		return DailyTopicView{}, err
	}
	noteProposalAnswered(judgmentTopic, proposalID, verdict == topicday.VerdictYes)
	a.emitBrowserEvent("topics-daily:updated", map[string]any{"topicId": topicID})
	return dailyTopicView(saved), nil
}

var (
	topicsDailyAnswerMu   sync.Mutex
	errDailyTopicAnswered = errors.New("this proposal was already answered")
)

func dailyTopicProposal(store *topicday.Store, topicID, proposalID string) (topicday.Topic, topicday.Proposal, error) {
	saved, ok := store.Load()
	if !ok {
		return topicday.Topic{}, topicday.Proposal{}, topicday.ErrNotFound
	}
	for _, t := range saved.Topics {
		if t.ID == topicID && t.Proposal != nil && t.Proposal.ID == proposalID {
			return t, *t.Proposal, nil
		}
	}
	return topicday.Topic{}, topicday.Proposal{}, topicday.ErrNotFound
}

// dailyTopicTaskStart starts a saved task; tests replace it.
var dailyTopicTaskStart = func(a *App, id string) error {
	bridge, err := a.desktopTasks()
	if err != nil {
		return err
	}
	_, err = bridge.startHandoff(id)
	return err
}

// startDailyTopicTask saves the proposal as a Task and starts it, the way Up
// next does (StartDesktopTaskOffer). A task that cannot start is removed.
func (a *App) startDailyTopicTask(topic topicday.Topic, proposal topicday.Proposal) (string, error) {
	input, err := validateCreateTask(CreateTaskInput{Title: dailyTopicTaskTitle(proposal.Title), Brief: proposal.Prompt})
	if err != nil {
		return "", err
	}
	saved, err := a.createTaskEntryFrom(input, handoff.SourceDesktop, &handoff.Origin{Kind: topicsDailyTaskOrigin, Label: topic.Name})
	if err != nil {
		return "", err
	}
	if err := dailyTopicTaskStart(a, saved.ID); err != nil {
		if removed, cleanupErr := a.handoffStore.DeleteIfUnchanged(saved); cleanupErr == nil && removed {
			a.emitBrowserEvent("handoff:updated", map[string]any{"id": saved.ID})
		}
		return "", err
	}
	return saved.ID, nil
}

func dailyTopicTaskTitle(title string) string {
	title = strings.TrimSpace(title)
	if len(title) > mobileHandoffTitleMax {
		title = title[:mobileHandoffTitleMax]
	}
	return title
}

// syncDailyTopicTasks brings finished tasks' results back to their topics. It
// reads only the tasks of running proposals, from the local task store.
func (a *App) syncDailyTopicTasks(store *topicday.Store, saved topicday.Copy) topicday.Copy {
	if a.handoffStore == nil {
		return saved
	}
	for i := range saved.Topics {
		p := saved.Topics[i].Proposal
		if p == nil || p.State != topicday.StateRunning || p.TaskID == "" {
			continue
		}
		entry, err := a.handoffStore.Get(p.TaskID)
		if err != nil {
			continue
		}
		state, result := dailyTopicTaskState(entry)
		if state == topicday.StateRunning {
			continue
		}
		if ok, err := store.UpdateByTask(p.TaskID, func(q *topicday.Proposal) bool {
			if q.State != topicday.StateRunning {
				return false
			}
			q.State, q.Result = state, result
			return true
		}); ok && err == nil {
			p.State, p.Result = state, result
		}
	}
	return saved
}

// dailyTopicTaskState maps a task to a proposal state and its result line.
func dailyTopicTaskState(entry handoff.Entry) (string, string) {
	summary := ""
	if entry.Thread != nil && entry.Thread.Outcome != nil {
		summary = strings.TrimSpace(entry.Thread.Outcome.Params["summary"])
	}
	switch entry.Status {
	case handoff.StatusCompleted, handoff.StatusNeedsReview:
		// Needs review is finished work waiting for the person: done here.
		return topicday.StateDone, summary
	case handoff.StatusAborted, handoff.StatusStale:
		return topicday.StateFailed, summary
	default:
		return topicday.StateRunning, ""
	}
}
