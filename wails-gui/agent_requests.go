package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Your agents asking each other (phone-agent v1.2.0 rules 1 and 2,
// computer-today v1.1.0 rules 7 and 8; PROTOCOL.md 26.4). One agent asks
// another for something only the person can allow; the request is an item in
// Your work with Yes and No only. It waits a day, then ends by itself, and it
// never notifies. The agent tools (ask_agent, get_answer) create requests and
// read their answers through the functions below.

const (
	agentRequestsFile = "requests.json"
	// agentRequestWait is how long a request waits for the person.
	agentRequestWait = 24 * time.Hour
	// agentRequestKeep is how long a request is kept after it ends.
	agentRequestKeep    = 7 * 24 * time.Hour
	agentRequestTextMax = 600

	agentRequestYes    = "yes"
	agentRequestNo     = "no"
	agentRequestRanOut = "ran_out"
)

// AgentRequest is one agent's request to another, waiting on the person.
type AgentRequest struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	HandoffID string `json:"handoffId,omitempty"`
	// Sentence is what is asked, in the asking agent's words; Moves is what
	// a Yes would move.
	Sentence   string `json:"sentence"`
	Moves      string `json:"moves,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	ExpiresAt  int64  `json:"expiresAt"`
	Answer     string `json:"answer,omitempty"`
	AnsweredAt int64  `json:"answeredAt,omitempty"`
}

var (
	agentRequestsMu      sync.Mutex
	errAgentRequestInput = errors.New("a request needs two agents and a sentence")
	errAgentRequestOver  = errors.New("this request was answered or ran out")
)

func agentRequestsPath() (string, error) {
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "agents", agentRequestsFile), nil
}

func readAgentRequestsLocked() ([]AgentRequest, error) {
	path, err := agentRequestsPath()
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
	var out []AgentRequest
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeAgentRequestsLocked(list []AgentRequest, now time.Time) error {
	cutoff := now.Add(-agentRequestKeep).UnixMilli()
	kept := make([]AgentRequest, 0, len(list))
	for _, r := range list {
		if r.ExpiresAt >= cutoff || (r.AnsweredAt >= cutoff && r.AnsweredAt > 0) {
			kept = append(kept, r)
		}
	}
	path, err := agentRequestsPath()
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

func agentRequestBound(text string) string {
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > agentRequestTextMax {
		return string(runes[:agentRequestTextMax])
	}
	return text
}

// createAgentRequest puts one agent's request to another in Your work. A
// zero expiresAt waits a day.
func (a *App) createAgentRequest(from, to, handoffID, sentence, moves string, expiresAt time.Time) (AgentRequest, error) {
	from, to, sentence = strings.TrimSpace(from), strings.TrimSpace(to), agentRequestBound(sentence)
	if from == "" || to == "" || from == to || sentence == "" {
		return AgentRequest{}, errAgentRequestInput
	}
	now := time.Now()
	if expiresAt.IsZero() || expiresAt.Before(now) || expiresAt.After(now.Add(agentRequestWait)) {
		expiresAt = now.Add(agentRequestWait)
	}
	request := AgentRequest{ID: uuid.NewString(), From: from, To: to, HandoffID: strings.TrimSpace(handoffID),
		Sentence: sentence, Moves: agentRequestBound(moves), CreatedAt: now.UnixMilli(), ExpiresAt: expiresAt.UnixMilli()}
	agentRequestsMu.Lock()
	list, err := readAgentRequestsLocked()
	if err == nil {
		err = writeAgentRequestsLocked(append(list, request), now)
	}
	agentRequestsMu.Unlock()
	if err != nil {
		return AgentRequest{}, err
	}
	a.touchDerivedViews(derivedWorkList)
	return request, nil
}

// getAgentRequest reads one request, with ran_out once it waited too long.
func (a *App) getAgentRequest(id string) (AgentRequest, bool) {
	agentRequestsMu.Lock()
	list, err := readAgentRequestsLocked()
	agentRequestsMu.Unlock()
	if err != nil {
		return AgentRequest{}, false
	}
	now := time.Now().UnixMilli()
	for _, r := range list {
		if r.ID == id {
			if r.Answer == "" && r.ExpiresAt <= now {
				r.Answer, r.AnsweredAt = agentRequestRanOut, r.ExpiresAt
			}
			return r, true
		}
	}
	return AgentRequest{}, false
}

// agentRequestAnswer is a request's answer ("yes", "no", "ran_out", or ""
// while it waits) and when it was given, for the agent tool get_answer.
func (a *App) agentRequestAnswer(id string) (string, time.Time) {
	r, ok := a.getAgentRequest(id)
	if !ok || r.Answer == "" {
		return "", time.Time{}
	}
	return r.Answer, time.UnixMilli(r.AnsweredAt)
}

// answerAgentRequest records the person's Yes or No.
func (a *App) answerAgentRequest(id, answer string, now time.Time) error {
	agentRequestsMu.Lock()
	defer agentRequestsMu.Unlock()
	list, err := readAgentRequestsLocked()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].ID != id {
			continue
		}
		if list[i].Answer != "" || list[i].ExpiresAt <= now.UnixMilli() {
			return errAgentRequestOver
		}
		list[i].Answer, list[i].AnsweredAt = answer, now.UnixMilli()
		return writeAgentRequestsLocked(list, now)
	}
	return errWorkItemGone
}

func workRequestID(id string) string { return "request:" + id }

// workRequestItems are the requests waiting on the person, and the ones that
// ran out today (Done today: "… ran out · nothing moved").
func (a *App) workRequestItems(now time.Time) []WorkItem {
	agentRequestsMu.Lock()
	list, err := readAgentRequestsLocked()
	agentRequestsMu.Unlock()
	if err != nil || len(list) == 0 {
		return nil
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].CreatedAt > list[j].CreatedAt })
	items := []WorkItem{}
	for _, r := range list {
		ranOut := r.Answer == "" && r.ExpiresAt <= now.UnixMilli()
		if ranOut && now.Sub(time.UnixMilli(r.ExpiresAt)) > 24*time.Hour {
			continue
		}
		if r.Answer != "" && now.Sub(time.UnixMilli(r.AnsweredAt)) > 24*time.Hour {
			continue
		}
		from, to := a.workAgentName(r.From), a.workAgentName(r.To)
		item := WorkItem{
			ID: workRequestID(r.ID), Kind: workKindRequest, Row: "request", State: workStateOpen, Source: workSourceAgent,
			Ref: WorkRef{HandoffID: r.HandoffID},
			First: []WorkTextPart{{Text: from + " asks " + to, Key: "agent_asks"},
				{Key: "clock", At: mobileTimestamp(time.UnixMilli(r.CreatedAt))}},
			Noticed: r.Sentence, Work: WorkBody{Title: r.Sentence, Text: r.Moves},
			Yes:       WorkYes{Does: "allow", Line: "Yes lets " + to + " do it."},
			Reasons:   []string{},
			Request:   &WorkRequest{From: WorkAgentRef{AgentID: r.From, Name: from}, To: WorkAgentRef{AgentID: r.To, Name: to}, Moves: r.Moves},
			ExpiresAt: mobileTimestamp(time.UnixMilli(r.ExpiresAt)), CreatedAt: mobileTimestamp(time.UnixMilli(r.CreatedAt)),
		}
		if ranOut {
			item.State = workStateAnswered
			item.Answer = &WorkAnswer{Action: agentRequestRanOut, At: item.ExpiresAt,
				Line: from + "'s request to " + to + " ran out · nothing moved"}
		}
		items = append(items, item)
	}
	return items
}
