package main

// Coding agents on this computer, connected with one click (computer-agents
// v1.0.0 rule 12, C1). Claude Code and Codex run here, so they need no join
// link: Connect asks the person once and records it. A Yes for a connected
// one starts a task pinned to that agent (handoff.Entry.Backend), whatever
// the chat provider is, and fails rather than run on another agent. The
// result comes back through the task, to Your work and the Agents page.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"yansu-agent/agent"

	"wails-gui/internal/handoff"
	"wails-gui/internal/workonit"
)

const agentHereKeyPre = "agent_connected_here_"

var (
	errAgentHereNotCoding  = errors.New("only a coding agent on this computer connects here")
	errAgentHereNotFound   = errors.New("that agent isn't installed on this computer")
	errAgentHereSignedOut  = errors.New("sign in to that agent on this computer first")
	errAgentHereNotStarted = errors.New("the task didn't start")
)

// agentHereBackend is the runner backend for a coding agent here.
func agentHereBackend(agentID string) string {
	switch agentID {
	case "claude-code":
		return string(agent.BackendClaudeCode)
	case "codex":
		return string(agent.BackendCodex)
	default:
		return ""
	}
}

// agentHereSignedIn checks the CLI's own sign-in; a test seam.
var agentHereSignedIn = func(backend string) (bool, error) {
	return agent.ExternalAgentSignedIn(agent.ExternalAgentConfig{Format: backend})
}

// agentHereTaskStart starts a saved task; a test seam over the topic path.
var agentHereTaskStart = func(a *App, id string) error { return dailyTopicTaskStart(a, id) }

// ConnectAgentHere connects Claude Code or Codex on this computer, after
// the person's OK in the sheet: it must be installed and signed in.
func (a *App) ConnectAgentHere(agentID string) (YourAgents, error) {
	backend := agentHereBackend(agentID)
	known, ok := workonit.AgentByID(agentID)
	if backend == "" || !ok {
		return YourAgents{}, errAgentHereNotCoding
	}
	if !a.workOnIt().agentFound(known) {
		return YourAgents{}, errAgentHereNotFound
	}
	signedIn, err := agentHereSignedIn(backend)
	if err != nil || !signedIn {
		return YourAgents{}, errAgentHereSignedOut
	}
	if a.store == nil {
		return YourAgents{}, errors.New("storage is not ready")
	}
	now := a.workOnIt().clock().UnixMilli()
	if err := a.store.SetUIState(context.Background(), agentHereKeyPre+agentID, strconv.FormatInt(now, 10)); err != nil {
		return YourAgents{}, err
	}
	log.Printf("[agents] %s connected on this computer", agentID)
	return a.yourAgents(), nil
}

// DisconnectAgentHere ends it; a task already running keeps running.
func (a *App) DisconnectAgentHere(agentID string) (YourAgents, error) {
	if agentHereBackend(agentID) == "" {
		return YourAgents{}, errAgentHereNotCoding
	}
	if a.store == nil {
		return YourAgents{}, errors.New("storage is not ready")
	}
	if err := a.store.SetUIState(context.Background(), agentHereKeyPre+agentID, ""); err != nil {
		return YourAgents{}, err
	}
	return a.yourAgents(), nil
}

// agentConnectedHere reports a coding agent connected on this computer.
func (a *App) agentConnectedHere(agentID string) bool {
	if a == nil || a.store == nil || agentHereBackend(agentID) == "" {
		return false
	}
	value, err := a.store.GetUIState(context.Background(), agentHereKeyPre+agentID)
	return err == nil && strings.TrimSpace(value) != ""
}

// startAgentHereTask saves the hand-off as a task pinned to the agent and
// starts it. A task that can't start is removed.
func (a *App) startAgentHereTask(task workonit.Task, agentID string) (string, error) {
	if a.handoffStore == nil {
		return "", errTaskStoreNotReady
	}
	title := dailyTopicTaskTitle(handoffFirstLine(task.Text))
	input, err := validateCreateTask(CreateTaskInput{Title: title, Brief: task.Text})
	if err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()
	entry := newTaskEntryFrom(input, handoff.SourceDesktop, &handoff.Origin{Kind: "work-on-it", Label: agentID}, "hf_"+workOnItID(), now)
	entry.Backend = agentHereBackend(agentID)
	saved, err := a.handoffStore.Save(entry)
	if err != nil {
		return "", err
	}
	a.emitBrowserEvent("handoff:created", map[string]any{"id": saved.ID, "title": saved.Title})
	if err := agentHereTaskStart(a, saved.ID); err != nil {
		if removed, cleanupErr := a.handoffStore.DeleteIfUnchanged(saved); cleanupErr == nil && removed {
			a.emitBrowserEvent("handoff:updated", map[string]any{"id": saved.ID})
		}
		return "", fmt.Errorf("%w: %v", errAgentHereNotStarted, err)
	}
	return saved.ID, nil
}

// workOnItTaskState is a pinned task's state and result, for the Agents page.
func (a *App) workOnItTaskState(taskID string) (string, string) {
	if a.handoffStore == nil || taskID == "" {
		return "", ""
	}
	entry, err := a.handoffStore.Get(taskID)
	if err != nil {
		return "", ""
	}
	return dailyTopicTaskState(entry)
}

// handoffFirstLine is a task's title: its first non-empty line.
func handoffFirstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
