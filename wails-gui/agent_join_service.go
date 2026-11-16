package main

// Agents join your Isoform (phone-agent v1.1.0 §2, B5; computer-agents
// v1.0.0 rule 13). internal/agentjoin serves /j/<code> beside the task
// links, on loopback and through the front. This file keeps the joined
// agents across restarts (token hashes only), gives the Connect an agent
// sheet its bindings, and lets Your agents show who is connected.
//
// Joining needs the person twice: they make the link and paste it, and they
// tap Allow when the agent's check number matches. It answers only while
// task links are on and Closed Eyes is off.

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/agentinbox"
	"wails-gui/internal/agentjoin"
	"wails-gui/internal/workonit"
)

const (
	agentsDirName       = "agents"
	agentsConnectedFile = "connected.json"
)

var errAgentJoinNoOrigin = errors.New("agents can't reach this computer yet: turn on the agent address in Settings")

type agentJoinState struct {
	once   sync.Once
	svc    *agentjoin.Service
	fileMu sync.Mutex
	// test seams
	dir string
	now func() time.Time
}

func (s *agentJoinState) dirPath() (string, error) {
	if s.dir != "" {
		return s.dir, nil
	}
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, agentsDirName), nil
}

func (a *App) agentJoinService() *agentjoin.Service {
	s := &a.agentJoin
	s.once.Do(func() {
		s.svc = agentjoin.New(agentjoin.Hooks{
			Allowed: func() error { return a.agentsAllowed() },
			Changed: a.saveConnectedAgents,
			Now:     s.now,
		})
		if agents, err := a.readConnectedAgents(); err != nil {
			log.Printf("[agents] connected agents not restored: %v", err)
		} else {
			s.svc.Restore(agents)
		}
	})
	return s.svc
}

// AgentJoinStart is what the sheet shows after Connect an agent.
type AgentJoinStart struct {
	Message   string `json:"message"`
	Link      string `json:"link"`
	ExpiresAt int64  `json:"expiresAt"`
	// Local is true when only agents on this computer can read the link.
	Local bool `json:"local"`
}

// StartAgentJoin makes a join link and the one message to paste. A link
// still open is closed.
func (a *App) StartAgentJoin() (AgentJoinStart, error) {
	if err := a.agentsAllowed(); err != nil {
		return AgentJoinStart{}, err
	}
	if err := a.startTaskLinkListener(); err != nil {
		return AgentJoinStart{}, err
	}
	base, err := a.taskLinkBaseURL()
	if err != nil {
		return AgentJoinStart{}, err
	}
	code, expires, err := a.agentJoinService().Start()
	if err != nil {
		return AgentJoinStart{}, err
	}
	link := base + agentjoin.PathPrefix + code
	return AgentJoinStart{Message: agentjoin.Message(link), Link: link, ExpiresAt: expires.UnixMilli(), Local: !strings.HasPrefix(base, "https://")}, nil
}

// GetAgentJoin is the open join, or nil: waiting, an agent asking with its
// check number, allowed, denied or expired.
func (a *App) GetAgentJoin() *agentjoin.Join {
	return a.agentJoinService().Current()
}

// AnswerAgentJoin is the person's Allow or Deny.
func (a *App) AnswerAgentJoin(allow bool) error {
	if allow {
		if err := a.agentsAllowed(); err != nil {
			return err
		}
	}
	agent, err := a.agentJoinService().Answer(allow)
	if err == nil && agent != nil {
		log.Printf("[agents] %s joined", agent.ID)
	}
	return err
}

// CancelAgentJoin closes the open join link.
func (a *App) CancelAgentJoin() { a.agentJoinService().Cancel() }

// ConnectedAgentView is one joined agent, without its token hash.
type ConnectedAgentView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Device     string `json:"device"`
	MayOpen    string `json:"mayOpen"`
	JoinedAt   int64  `json:"joinedAt"`
	LastSeenAt int64  `json:"lastSeenAt,omitempty"`
	// CatalogID is the known agent it is, when its name says so.
	CatalogID string `json:"catalogId,omitempty"`
}

// GetConnectedAgents lists the joined agents.
func (a *App) GetConnectedAgents() []ConnectedAgentView {
	agents := a.agentJoinService().Agents()
	out := make([]ConnectedAgentView, 0, len(agents))
	for _, ag := range agents {
		out = append(out, ConnectedAgentView{ID: ag.ID, Name: ag.Name, Device: ag.Device, MayOpen: ag.MayOpen, JoinedAt: ag.JoinedAt, LastSeenAt: ag.LastSeenAt, CatalogID: catalogIDForName(ag.Name)})
	}
	return out
}

// DisconnectAgent revokes an agent's token at once.
func (a *App) DisconnectAgent(id string) error {
	if err := a.agentJoinService().Revoke(id); err != nil {
		return err
	}
	a.agentInboxService().Withdraw(func(item agentinbox.Item) bool { return item.AgentID == id })
	log.Printf("[agents] %s disconnected", id)
	return nil
}

// catalogIDForName matches the name an agent gives itself to a known agent:
// "Claude Code" is claude-code, "Codex CLI" is codex. Longer names win, so
// "Claude Code" isn't Claude.
func catalogIDForName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	best, bestLen := "", 0
	for _, agent := range workonit.Catalog() {
		known := strings.ToLower(agent.Name)
		if (n == known || strings.HasPrefix(n, known+" ")) && len(known) > bestLen {
			best, bestLen = agent.ID, len(known)
		}
	}
	return best
}

// ── files ──

func (a *App) connectedAgentsPath() (string, error) {
	dir, err := a.agentJoin.dirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, agentsConnectedFile), nil
}

func (a *App) readConnectedAgents() ([]agentjoin.Agent, error) {
	a.agentJoin.fileMu.Lock()
	defer a.agentJoin.fileMu.Unlock()
	path, err := a.connectedAgentsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var agents []agentjoin.Agent
	return agents, json.Unmarshal(data, &agents)
}

func (a *App) saveConnectedAgents() {
	svc := a.agentJoin.svc
	if svc == nil {
		return
	}
	agents := svc.Agents()
	a.agentJoin.fileMu.Lock()
	defer a.agentJoin.fileMu.Unlock()
	path, err := a.connectedAgentsPath()
	if err == nil {
		err = writeFrontJSON(path, agents)
	}
	if err != nil {
		log.Printf("[agents] connected agents not saved: %v", err)
	}
}
