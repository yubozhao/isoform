package main

// What an agent's card on the Agents page needs beyond Your agents
// (computer-agents v1.1.0 13c, 13d): Open {agent} for a hand-off that
// couldn't start, and which hand-offs the person judged Useful, for This
// week's "{u} useful" (from their own judgments, never a guess).

import (
	"errors"
	"fmt"

	"wails-gui/internal/workonit"
)

var errAgentNothingToOpen = errors.New("there is nothing to open for that agent")

// agentOpenable reports that Open {agent} has somewhere to go.
func agentOpenable(agent workonit.Agent, found bool) bool {
	if agent.OpenURL != "" || (found && len(agent.AppNames) > 0) {
		return true
	}
	_, ok := signInCommand(agent.ID)
	return ok
}

// OpenAgentApp opens the agent itself, with no task: its app when it is
// installed, else its site. A command-line agent (Claude Code) has neither,
// so Terminal opens on its own sign-in, which is what most often stops it
// from starting ("not logged in").
func (a *App) OpenAgentApp(agentID string) error {
	agent, ok := workonit.AgentByID(agentID)
	if !ok {
		return fmt.Errorf("%w: %q", errWorkOnItNoAgent, agentID)
	}
	s := a.workOnIt()
	if target := workOnItOpenTarget(agent, s.agentPresent(agent)); target != "" {
		return s.openTarget(a, target)
	}
	if _, ok := signInCommand(agent.ID); ok {
		return a.OpenAssistantSignIn(agent.ID)
	}
	return errAgentNothingToOpen
}

// GetUsefulHandoffs lists the hand-offs whose result the person marked
// Useful, by Work on it record id. An undone Useful doesn't count.
func (a *App) GetUsefulHandoffs() ([]string, error) {
	out := []string{}
	for _, record := range a.answersLedger().current() {
		if record.Action == workActionUseful && record.HandoffID != "" {
			out = append(out, record.HandoffID)
		}
	}
	return out, nil
}
