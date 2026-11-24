package main

// Agents join your Isoform (phone-agent v1.1.0 §2, B5; computer-agents
// v1.0.0 rule 13). internal/agentjoin serves /j/<code> beside the task
// links, on loopback and through the front. This file keeps the joined
// agents across restarts (token hashes only), gives the Connect an agent
// sheet its bindings, and lets Your agents show who is connected.
//
// Joining needs the person twice: they make the link and paste it, and they
// tap Allow when the agent's check number matches. It answers only while
// the link is valid, including during Closed Eyes.

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

var errAgentJoinNoOrigin = errors.New("agents can't reach this computer through the agent address yet")

type agentJoinState struct {
	once   sync.Once
	svc    *agentjoin.Service
	fileMu sync.Mutex
	// phone is the join a phone made (agent_join_phone.go), under phoneMu.
	phoneMu sync.Mutex
	phone   *phoneAgentJoin
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
			Changed: func() {
				a.saveConnectedAgents()
				a.notePhoneAgentJoinChanged()
			},
			Abandoned: func() { go a.closeUnusedAddress() },
			Now:       s.now,
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
	// MCPAddress is the tool server address a chat app's connector takes
	// (agents-join v1.1.0 §2): the person pastes it in the app's settings.
	MCPAddress string `json:"mcpAddress"`
}

// StartAgentJoin makes a join link and the one message to paste. A link
// still open is closed. Making the link is the person's yes; no switch and
// not Closed Eyes stop it, since connecting sees and keeps nothing (G2).
func (a *App) StartAgentJoin() (AgentJoinStart, error) {
	return a.startExistingAgentJoin(false)
}

// The phone may share an already-open address. A desktop local join always
// uses loopback, including when an app agent already owns the front.
func (a *App) startExistingAgentJoin(fromPhone bool) (AgentJoinStart, error) {
	a.front.lifecycle.Lock()
	defer a.front.lifecycle.Unlock()
	if a.front.shutdown {
		return AgentJoinStart{}, errors.New("the computer is shutting down")
	}
	if fromPhone && a.GetAgentAddress().Open {
		base, err := a.taskLinkBaseURL()
		if err != nil {
			return AgentJoinStart{}, err
		}
		return a.startJoinAt(base, true)
	}
	if configuredAgentBridge() {
		base, err := a.privateJoinBase()
		if err != nil {
			return AgentJoinStart{}, err
		}
		return a.startJoinAt(base, false)
	}
	if err := a.startTaskLinkListener(); err != nil {
		return AgentJoinStart{}, err
	}
	a.taskLinks.listenMu.Lock()
	base := "http://" + a.taskLinks.listenAddr
	a.taskLinks.listenMu.Unlock()
	return a.startJoinAt(base, false)
}

func (a *App) startJoinAt(base string, viaAddress bool) (AgentJoinStart, error) {
	code, expires, err := a.agentJoinService().Start(viaAddress)
	if err != nil {
		return AgentJoinStart{}, err
	}
	if a.front.expiry != nil {
		a.front.expiry.Stop()
	}
	// A loopback link can also receive a request through an already-open front.
	a.front.expiry = time.AfterFunc(expires.Sub(a.agentJoinClock()), a.closeUnusedAddress)
	a.closeUnusedAddressLocked()
	link := base + agentjoin.PathPrefix + code
	return AgentJoinStart{Message: agentjoin.Message(link), Link: link, ExpiresAt: expires.UnixMilli(), Local: !strings.HasPrefix(base, "https://"), MCPAddress: base + "/mcp?join=" + a.agentJoinService().MCPKey()}, nil
}

// GetAgentJoin is the open join, or nil: waiting, an agent asking with its
// check number, allowed, denied or expired.
func (a *App) GetAgentJoin() *agentjoin.Join {
	return a.agentJoinService().Current()
}

// AnswerAgentJoin is the person's Allow or Deny.
func (a *App) AnswerAgentJoin(allow bool) error {
	defer a.closeUnusedAddress()
	agent, err := a.agentJoinService().Answer(allow)
	if err == nil && agent != nil {
		// Joined again: Connect again's entry goes (phone-agent v1.2.0 §6).
		a.clearDisconnectedFor(agent.Name, a.phoneJoinReconnect())
		log.Printf("[agents] %s joined", agent.ID)
	}
	return err
}

// CancelAgentJoin closes the open join link.
func (a *App) CancelAgentJoin() {
	a.agentJoinService().Cancel()
	a.closeUnusedAddress()
}

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
	left, _ := a.connectedAgentFor(id)
	if err := a.agentJoinService().Revoke(id); err != nil {
		return err
	}
	a.closeUnusedAddress()
	a.keepDisconnectedAgent(disconnectedAgent{ID: id, Name: left.Name, CatalogID: left.CatalogID, DisconnectedAt: a.agentJoinClock().UnixMilli()})
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
	a.agentJoin.fileMu.Lock()
	defer a.agentJoin.fileMu.Unlock()
	path, err := a.connectedAgentsPath()
	if err == nil {
		err = writeFrontJSON(path, svc.Agents())
	}
	if err != nil {
		log.Printf("[agents] connected agents not saved: %v", err)
	}
}
