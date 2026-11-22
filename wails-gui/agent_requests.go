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

	"wails-gui/internal/workonit"
)

// Your agents asking each other (phone-agent v1.2.0 rules 1 and 2,
// computer-today v1.1.0 rules 7 and 8; PROTOCOL.md 26.4). One agent asks
// another for something only the person can allow; the request is an item in
// Your work with Yes and No only. It waits a day, then ends by itself, and it
// never notifies. The agent tools (ask_agent, get_answer) create requests and
// read their answers through the functions below.

const (
	agentRequestUnconfirmed = "delivery unconfirmed"
	agentRequestUnavailable = "target unavailable"
	agentRequestSource      = "agent_request"
	agentRequestsFile       = "requests.json"
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
	ID              string `json:"id"`
	From            string `json:"from"`
	To              string `json:"to"`
	HandoffID       string `json:"handoffId,omitempty"`
	TargetHandoffID string `json:"targetHandoffId,omitempty"`
	TargetTaskID    string `json:"targetTaskId,omitempty"`
	SendError       string `json:"sendError,omitempty"`
	SendReason      string `json:"sendReason,omitempty"`
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
	agentRequestsMu          sync.Mutex
	agentRequestSending      sync.Map // reserved hand-off IDs currently being delivered
	errAgentRequestNotFailed = errors.New("this request has no failed delivery to send again")
	errAgentRequestInput     = errors.New("a request needs two agents and a sentence")
	errAgentRequestOver      = errors.New("this request was answered or ran out")
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

func updateAgentRequest(id string, now time.Time, change func(*AgentRequest) error) error {
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
		if err := change(&list[i]); err != nil {
			return err
		}
		return writeAgentRequestsLocked(list, now)
	}
	return errWorkItemGone
}

// Reserve the delivery id with the Yes before sending. A retry cannot start
// a second task, and an interrupted delivery cannot claim the target has it.
func (a *App) answerAgentRequest(id, answer, surface string, now time.Time) error {
	if answer == agentRequestYes {
		return a.sendAgentRequest(id, surface, now, false)
	}
	return updateAgentRequest(id, now, func(r *AgentRequest) error {
		if r.Answer != "" || r.ExpiresAt <= now.UnixMilli() {
			return errAgentRequestOver
		}
		r.Answer, r.AnsweredAt = answer, now.UnixMilli()
		return nil
	})
}

// SendAgentRequestAgain retries the original approved payload from Your work.
func (a *App) SendAgentRequestAgain(requestID string) error {
	return a.sendAgentRequest(strings.TrimPrefix(requestID, "request:"), workSurfaceComputer, time.Now(), true)
}

func (a *App) sendAgentRequest(id, surface string, now time.Time, again bool) error {
	var request AgentRequest
	handoffID := workOnItID()
	err := updateAgentRequest(id, now, func(r *AgentRequest) error {
		if again {
			if r.ExpiresAt <= now.UnixMilli() || !a.agentRequestSendFailed(*r) {
				return errAgentRequestNotFailed
			}
		} else {
			if r.Answer != "" || r.ExpiresAt <= now.UnixMilli() {
				return errAgentRequestOver
			}
			r.Answer, r.AnsweredAt = agentRequestYes, now.UnixMilli()
		}
		r.TargetHandoffID, r.SendError, r.SendReason = handoffID, agentRequestUnconfirmed, ""
		if agentHereBackend(r.To) != "" {
			r.TargetTaskID = "hf_" + handoffID
		}
		agentRequestSending.Store(handoffID, true)
		request = *r
		return nil
	})
	defer agentRequestSending.Delete(handoffID)
	if err != nil {
		return err
	}
	task := workonit.Task{ID: request.TargetHandoffID, SourceKind: agentRequestSource, SourceID: id, Kind: workonit.KindElse, Title: request.Sentence, Text: request.Moves, PreparedAt: now}
	if agentHereBackend(request.To) != "" {
		task.Kind = workonit.KindCode
	}
	taskID, sendErr := a.deliverConnectedTask(task, request.To, request.TargetHandoffID, surface)
	reason := agentRequestSendReason(request.To, sendErr)
	err = updateAgentRequest(id, now, func(r *AgentRequest) error {
		if sendErr != nil {
			r.SendError, r.SendReason = agentRequestUnavailable, reason
			r.TargetHandoffID, r.TargetTaskID = "", ""
		} else {
			r.TargetTaskID = taskID
			r.SendError = ""
		}
		return nil
	})
	if again {
		a.touchDerivedViews(derivedWorkList)
		if err == nil {
			return sendErr
		}
	}
	return err
}

func agentRequestSendReason(target string, err error) string {
	switch true {
	case errors.Is(err, errAgentHereSignedOut):
		return "signed_out"
	case errors.Is(err, errWorkOnItClosedEyes), errors.Is(err, errTaskLinksClosedEyes):
		return "closed_eyes"
	case errors.Is(err, errAgentNotConnected), errors.Is(err, errWorkOnItNoMethod):
		return "disconnected"
	case errors.Is(err, errAgentHereNotStarted):
		if signedIn, probeErr := agentHereSignedIn(agentHereBackend(target)); probeErr == nil && !signedIn {
			return "signed_out"
		}
		return "not_running"
	default:
		return ""
	}
}

// A reservation can survive a restart on either side of delivery. Check the
// pinned task, scoped result or inbox receipt before offering another send.
func (a *App) agentRequestSendFailed(r AgentRequest) bool {
	if r.Answer != agentRequestYes {
		return false
	}
	switch r.SendError {
	case agentRequestUnavailable:
		return true
	case agentRequestUnconfirmed:
		if _, sending := agentRequestSending.Load(r.TargetHandoffID); sending {
			return false
		}
		if r.TargetTaskID != "" {
			if a.handoffStore == nil {
				return false
			}
			_, err := a.handoffStore.Get(r.TargetTaskID)
			return os.IsNotExist(err)
		}
		if _, found, err := a.agentRequestResult(r); found || err != nil {
			return false
		}
		item, ok := a.agentInboxService().Find(r.TargetHandoffID)
		return !ok || item.AgentID != r.To || item.SourceID != r.ID
	default:
		return false
	}
}

func workRequestID(id string) string { return "request:" + id }

// workRequestItems keeps a failed Yes open until the request expires.
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
		failed := a.agentRequestSendFailed(r)
		ranOut := (r.Answer == "" || failed) && r.ExpiresAt <= now.UnixMilli()
		if ranOut && now.Sub(time.UnixMilli(r.ExpiresAt)) > 24*time.Hour {
			continue
		}
		if !failed && r.Answer != "" && !now.Before(time.UnixMilli(r.AnsweredAt).Add(agentRequestWait)) {
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
		action := r.Answer
		if ranOut {
			action = agentRequestRanOut
		}
		switch action {
		case agentRequestRanOut:
			item.State = workStateAnswered
			item.Answer = &WorkAnswer{Action: agentRequestRanOut, At: item.ExpiresAt,
				Line: from + "'s request to " + to + " ran out · nothing moved"}
		case agentRequestYes, agentRequestNo:
			item.Answer = &WorkAnswer{Action: r.Answer, At: mobileTimestamp(time.UnixMilli(r.AnsweredAt))}
			if failed {
				item.Request.SendError, item.Request.SendReason = agentRequestUnavailable, r.SendReason
			} else {
				item.State = workStateAnswered
				verb := "allowed"
				if r.Answer == agentRequestNo {
					verb = "declined"
				}
				item.Answer.Line = from + "'s request to " + to + ": " + verb
			}
		}
		items = append(items, item)
	}
	return items
}
