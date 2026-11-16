package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"wails-gui/internal/workonit"
)

// Your agents (phone-map B4, extended by phone-agent v1.1.0 §4): the person
// picks two agents, one for code and one for everything else, as one setting
// for the computer and the phone; the work's kind decides which gets a Yes.
// Each hand-off is kept with exactly what went, when and to whom, for Your
// data: on this computer only, for 90 days, forgettable one by one or all,
// and a withdrawn source takes its hand-offs with it.

const (
	yourAgentKeyPre      = "work_on_it_agent_"
	workOnItHandoffsFile = "handoffs.json"
)

// How an agent is reached, for its row (phone-agent v1.1.0 rule 2).
const (
	agentReachConnected         = "connected"
	agentReachConnectedComputer = "connected_computer"
	agentReachOnComputer        = "on_computer"
	agentReachItsApp            = "its_app"
	agentReachByText            = "by_text"
)

var (
	errYourAgentBadKind  = errors.New("unknown kind of work")
	errYourAgentWrongFor = errors.New("that agent is not for this kind of work")
	errWorkOnItNoHandoff = errors.New("unknown hand-off")
)

// YourAgentView is one agent in Your agents.
type YourAgentView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Reach string `json:"reach"`
	// Found is true when the agent's app or command is on this computer.
	Found     bool `json:"found"`
	Connected bool `json:"connected"`
	// LastWorkedAt is when it last took or finished a hand-off.
	LastWorkedAt int64 `json:"lastWorkedAt,omitempty"`
}

// YourAgents is the two choices and every agent Isoform knows how to reach.
// A choice is empty until the person picks: the first Yes asks once.
type YourAgents struct {
	Choices map[string]string `json:"choices"`
	Agents  []YourAgentView   `json:"agents"`
}

// GetYourAgents is the setting and the list, for Settings, the Agents page
// and the phone.
func (a *App) GetYourAgents() (YourAgents, error) {
	return a.yourAgents(), nil
}

// SetYourAgent picks the agent for one kind of work. An empty id clears it.
func (a *App) SetYourAgent(kind, agentID string) (YourAgents, error) {
	if !workonit.ValidKind(kind) {
		return YourAgents{}, errYourAgentBadKind
	}
	agentID = strings.TrimSpace(agentID)
	if agentID != "" {
		agent, ok := workonit.AgentByID(agentID)
		if !ok {
			return YourAgents{}, errWorkOnItNoAgent
		}
		if agent.Kind != kind {
			return YourAgents{}, errYourAgentWrongFor
		}
	}
	if a.store == nil {
		return YourAgents{}, errors.New("storage is not ready")
	}
	if err := a.store.SetUIState(context.Background(), yourAgentKeyPre+kind, agentID); err != nil {
		return YourAgents{}, err
	}
	return a.yourAgents(), nil
}

// yourAgentChoice is the agent picked for a kind of work, or "".
func (a *App) yourAgentChoice(kind string) string {
	if a == nil || a.store == nil {
		return ""
	}
	value, err := a.store.GetUIState(context.Background(), yourAgentKeyPre+kind)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(value)
	if agent, ok := workonit.AgentByID(id); !ok || agent.Kind != kind {
		return ""
	}
	return id
}

func (a *App) yourAgents() YourAgents {
	s := a.workOnIt()
	connected := a.yourAgentConnections()
	lastWorked := a.yourAgentLastWorked()
	out := YourAgents{
		Choices: map[string]string{workonit.KindCode: a.yourAgentChoice(workonit.KindCode), workonit.KindElse: a.yourAgentChoice(workonit.KindElse)},
		Agents:  []YourAgentView{},
	}
	for _, agent := range workonit.Catalog() {
		found := s.agentFound(agent)
		seen, isConnected := connected[agent.ID]
		if seen > lastWorked[agent.ID] {
			lastWorked[agent.ID] = seen
		}
		view := YourAgentView{ID: agent.ID, Name: agent.Name, Kind: agent.Kind, Found: found, Connected: isConnected, LastWorkedAt: lastWorked[agent.ID]}
		switch {
		case isConnected && agent.Kind == workonit.KindCode:
			view.Reach = agentReachConnectedComputer
		case isConnected:
			view.Reach = agentReachConnected
		case agent.Message:
			view.Reach = agentReachByText
		case agent.Kind == workonit.KindCode:
			view.Reach = agentReachOnComputer
		default:
			view.Reach = agentReachItsApp
		}
		out.Agents = append(out.Agents, view)
	}
	// Any other agent that joined is listed under the name it gave itself
	// (phone-agent v1.1.0 rule 3).
	for _, c := range a.GetConnectedAgents() {
		if c.CatalogID == "" {
			out.Agents = append(out.Agents, YourAgentView{ID: c.ID, Name: c.Name, Kind: workonit.KindElse, Reach: agentReachConnected, Connected: true, LastWorkedAt: c.LastSeenAt})
		}
	}
	return out
}

// yourAgentConnections are the known agents joined to this Isoform
// (phone-agent v1.1.0 §2): catalog id to when it was last seen.
func (a *App) yourAgentConnections() map[string]int64 {
	out := map[string]int64{}
	for _, c := range a.GetConnectedAgents() {
		if c.CatalogID != "" && c.LastSeenAt >= out[c.CatalogID] {
			out[c.CatalogID] = c.LastSeenAt
		}
	}
	return out
}

// yourAgentLastWorked is, per agent, the latest time it took or finished a
// hand-off.
func (a *App) yourAgentLastWorked() map[string]int64 {
	out := map[string]int64{}
	records, err := a.workOnItRead()
	if err != nil {
		return out
	}
	for _, r := range records {
		at := r.DoneAt
		if r.TakenAt > at {
			at = r.TakenAt
		}
		if at > out[r.AgentID] {
			out[r.AgentID] = at
		}
	}
	return out
}

// agentFound reports the agent's app or command on this computer.
func (s *workOnItState) agentFound(agent workonit.Agent) bool {
	if s.agentPresent(agent) {
		return true
	}
	look := s.pasteDeps().lookPath
	for _, cli := range agent.CLIs {
		if look == nil {
			break
		}
		if _, err := look(cli); err == nil {
			return true
		}
	}
	return false
}

// ── The hand-off log ──

func workOnItHandoffsPath() (string, error) {
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "work-on-it", workOnItHandoffsFile), nil
}

func readWorkOnItHandoffsLocked() ([]workonit.Handoff, error) {
	path, err := workOnItHandoffsPath()
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
	var out []workonit.Handoff
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("work on it hand-offs: %w", err)
	}
	return out, nil
}

// rewriteWorkOnItHandoffs changes the log under the records lock and drops
// what is past the keep window.
func (a *App) rewriteWorkOnItHandoffs(change func([]workonit.Handoff) []workonit.Handoff) error {
	s := a.workOnIt()
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	handoffs, err := readWorkOnItHandoffsLocked()
	if err != nil {
		return err
	}
	handoffs = change(handoffs)
	cutoff := s.clock().Add(-workOnItRecordsKeepFor).UnixMilli()
	kept := make([]workonit.Handoff, 0, len(handoffs))
	for _, h := range handoffs {
		if h.SentAt >= cutoff {
			kept = append(kept, h)
		}
	}
	path, err := workOnItHandoffsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	return writeKnowHowAtomic(path, data)
}

func (a *App) keepWorkOnItHandoff(h workonit.Handoff) {
	if err := a.rewriteWorkOnItHandoffs(func(list []workonit.Handoff) []workonit.Handoff { return append(list, h) }); err != nil {
		log.Printf("[work-on-it] hand-off not kept: %v", err)
	}
}

// GetWorkOnItHandoffs lists the kept hand-offs, newest first.
func (a *App) GetWorkOnItHandoffs() ([]workonit.Handoff, error) {
	s := a.workOnIt()
	s.recordsMu.Lock()
	list, err := readWorkOnItHandoffsLocked()
	s.recordsMu.Unlock()
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []workonit.Handoff{}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].SentAt > list[j].SentAt })
	return list, nil
}

// ForgetWorkOnItHandoff removes one kept hand-off; an empty id removes all.
func (a *App) ForgetWorkOnItHandoff(id string) error {
	found := false
	err := a.rewriteWorkOnItHandoffs(func(list []workonit.Handoff) []workonit.Handoff {
		if id == "" {
			found = true
			return nil
		}
		kept := list[:0]
		for _, h := range list {
			if h.ID == id {
				found = true
				continue
			}
			kept = append(kept, h)
		}
		return kept
	})
	if err == nil && !found {
		return errWorkOnItNoHandoff
	}
	return err
}

// workOnItPrivacyWithdrawal drops the hand-offs of a withdrawn source.
// Retention removals of raw sources don't touch them: a hand-off is the
// person's own Yes, kept for its own window.
func (a *App) workOnItPrivacyWithdrawal(w privacyWithdrawal) {
	if w.Retention || w.Scope.Kind != privacyWithdrawalSource || w.Scope.ID == "" {
		return
	}
	kind, id := w.Scope.SourceKind, w.Scope.ID
	if err := a.rewriteWorkOnItHandoffs(func(list []workonit.Handoff) []workonit.Handoff {
		kept := list[:0]
		for _, h := range list {
			if !(h.SourceID == id && (kind == "" || h.SourceKind == kind)) {
				kept = append(kept, h)
			}
		}
		return kept
	}); err != nil {
		log.Printf("[work-on-it] hand-offs not pruned after a withdrawal: %v", err)
	}
}

// startWorkOnIt subscribes the hand-off log to privacy withdrawals.
func (a *App) startWorkOnIt() {
	subscribePrivacyWithdrawals(a.workOnItPrivacyWithdrawal)
}

// ── The phone ──

func (bridge *mobileBridge) serveYourAgents(w http.ResponseWriter) {
	agents, err := bridge.app.GetYourAgents()
	if err != nil {
		mobileJSON(w, http.StatusInternalServerError, nil, "your agents could not be read")
		return
	}
	mobileJSON(w, http.StatusOK, agents, "")
}

func (bridge *mobileBridge) serveYourAgentChoice(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Kind           string `json:"kind"`
		AgentID        string `json:"agentId"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if !workonit.ValidKind(input.Kind) {
		mobileJSON(w, http.StatusBadRequest, nil, errYourAgentBadKind.Error())
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		agents, err := bridge.app.SetYourAgent(input.Kind, input.AgentID)
		switch {
		case errors.Is(err, errWorkOnItNoAgent), errors.Is(err, errYourAgentWrongFor):
			mobileJSON(w, http.StatusBadRequest, nil, err.Error())
		case err != nil:
			log.Printf("[your-agents] choice not saved: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the choice could not be saved")
		default:
			mobileJSON(w, http.StatusOK, agents, "")
		}
	})
}
