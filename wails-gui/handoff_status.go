package main

// What Isoform says about a hand-off, only from what it knows (agents-join
// v1.0.0 §7, computer-agents v1.1.0): Sent, Opened in its app, Has it,
// Working (only while Isoform sees its process run, or the agent posts
// progress), Back, No word after two hours (with Ask again and Take it
// back), Couldn't start, Taken back. Never an estimate.

import (
	"errors"
	"log"
	"net/http"
	"time"

	"wails-gui/internal/agentinbox"
	"wails-gui/internal/tasklink"
	"wails-gui/internal/workonit"
)

const handoffNoWordAfter = 2 * time.Hour

// Hand-off states.
const (
	handoffSent         = "sent"
	handoffOpened       = "opened"
	handoffHasIt        = "has_it"
	handoffWorking      = "working"
	handoffBack         = "back"
	handoffNoWord       = "no_word"
	handoffCouldntStart = "couldnt_start"
	handoffTakenBack    = "taken_back"
)

var errHandoffDone = errors.New("that hand-off is already back or taken back")

// HandoffStatus is one hand-off as Agents and Today show it.
type HandoffStatus struct {
	ID        string `json:"id"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Title     string `json:"title"`
	SentAt    int64  `json:"sentAt"`
	State     string `json:"state"`
	// Line is the agent's own words: its progress, its summary, or why it
	// couldn't start.
	Line string `json:"line,omitempty"`
	// Since is when it last showed life, for No word since.
	Since int64 `json:"since,omitempty"`
}

// GetHandoffStatuses is every kept hand-off's state, newest first.
func (a *App) GetHandoffStatuses() ([]HandoffStatus, error) {
	handoffs, err := a.GetWorkOnItHandoffs()
	if err != nil {
		return nil, err
	}
	results, _ := a.GetTaskLinkResults()
	byLink := map[string]TaskLinkResult{}
	for _, r := range results {
		if _, seen := byLink[r.LinkID]; !seen {
			byLink[r.LinkID] = r
		}
	}
	records, _ := a.workOnItRead()
	byRecord := map[string]workonit.Record{}
	for _, r := range records {
		byRecord[r.ID] = r
	}
	now := a.workOnIt().clock()
	out := make([]HandoffStatus, 0, len(handoffs))
	for _, h := range handoffs {
		out = append(out, a.handoffStatus(h, byRecord[h.ID], byLink, now))
	}
	return out, nil
}

func (a *App) handoffStatus(h workonit.Handoff, record workonit.Record, results map[string]TaskLinkResult, now time.Time) HandoffStatus {
	status := HandoffStatus{ID: h.ID, AgentID: h.AgentID, AgentName: a.handoffAgentName(h.AgentID, h.AgentName), Title: handoffFirstLine(h.Text), SentAt: h.SentAt, State: handoffSent}
	if h.TakenBackAt != 0 {
		status.State, status.Since = handoffTakenBack, h.TakenBackAt
		return status
	}
	if r, ok := results[h.ID]; ok {
		status.State, status.Line = handoffBack, r.Summary
		return status
	}
	switch h.TaskState {
	case "done":
		status.State, status.Line = handoffBack, h.TaskResult
		return status
	case "failed":
		status.State, status.Line = handoffCouldntStart, h.TaskResult
		return status
	}
	last := h.SentAt
	if h.AskedAgainAt > last {
		last = h.AskedAgainAt
	}
	// After an Ask again only what happened since counts.
	recordTaken := record.TakenAt
	if recordTaken <= h.AskedAgainAt {
		recordTaken = 0
	}
	if recordTaken > last {
		last = recordTaken
	}
	var item agentinbox.Item
	inbox := false
	if h.Method == workonit.MethodConnected && h.TaskID == "" {
		item, inbox = a.agentInboxService().Find(h.ID)
		if inbox && item.TakenAt > last {
			last = item.TakenAt
		}
	}
	switch {
	case h.TaskState == "running":
		// Isoform sees its process run.
		status.State = handoffWorking
		return status
	case inbox && item.Progress != "":
		status.State, status.Line = handoffWorking, item.Progress
		if item.ProgressAt > last {
			last = item.ProgressAt
		}
	case recordTaken != 0 || (inbox && item.TakenAt != 0):
		status.State = handoffHasIt
	case h.Method == workonit.MethodDeepLink || h.Method == workonit.MethodOpenCopy || h.Method == workonit.MethodPaste ||
		(h.Method == workonit.MethodTaskLink && (h.Via == workonit.MethodDeepLink || h.Via == workonit.MethodOpenCopy)):
		status.State = handoffOpened
	}
	if now.UnixMilli()-last >= handoffNoWordAfter.Milliseconds() {
		status.State, status.Since = handoffNoWord, last
	}
	return status
}

// handoffAgentName is the agent's name now, or the one kept when the work
// went (a joined agent that has since disconnected).
func (a *App) handoffAgentName(id, kept string) string {
	if agent, ok := workonit.AgentByID(id); ok {
		return agent.Name
	}
	if joined, ok := a.connectedAgentFor(id); ok {
		return joined.Name
	}
	if kept != "" {
		return kept
	}
	return id
}

func (a *App) findHandoff(id string) (workonit.Handoff, error) {
	list, err := a.GetWorkOnItHandoffs()
	if err != nil {
		return workonit.Handoff{}, err
	}
	for _, h := range list {
		if h.ID == id {
			return h, nil
		}
	}
	return workonit.Handoff{}, errWorkOnItNoHandoff
}

func (a *App) updateHandoff(id string, change func(*workonit.Handoff)) error {
	found := false
	err := a.rewriteWorkOnItHandoffs(func(list []workonit.Handoff) []workonit.Handoff {
		for i := range list {
			if list[i].ID == id {
				change(&list[i])
				found = true
			}
		}
		return list
	})
	if err == nil && !found {
		return errWorkOnItNoHandoff
	}
	return err
}

func (a *App) handoffStatusByID(id string) (HandoffStatus, error) {
	list, err := a.GetHandoffStatuses()
	if err != nil {
		return HandoffStatus{}, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return HandoffStatus{}, errWorkOnItNoHandoff
}

// AskAgentAgain re-offers a hand-off with no word: an agent that reads its
// inbox sees it as new, an agent on this computer that stopped is started
// again, and the two-hour clock starts over.
func (a *App) AskAgentAgain(id string) (HandoffStatus, error) {
	if a.IsClosedEyesModeEnabled() {
		return HandoffStatus{}, errWorkOnItClosedEyes
	}
	h, err := a.findHandoff(id)
	if err != nil {
		return HandoffStatus{}, err
	}
	if current, _ := a.handoffStatusByID(id); current.State == handoffBack || current.State == handoffTakenBack {
		return current, errHandoffDone
	}
	switch {
	case h.TaskID != "" && h.TaskState != "running":
		if _, err := a.StartHandoffAgain(h.TaskID); err != nil {
			return HandoffStatus{}, err
		}
	case h.Method == workonit.MethodConnected:
		a.agentInboxService().Reoffer(id)
	}
	now := a.workOnIt().clock().UnixMilli()
	if err := a.updateHandoff(id, func(x *workonit.Handoff) { x.AskedAgainAt = now }); err != nil {
		return HandoffStatus{}, err
	}
	log.Printf("[work-on-it] asked again agent=%s", h.AgentID)
	return a.handoffStatusByID(id)
}

// TakeHandoffBack ends a hand-off: the agent's token can no longer read it,
// a task here is stopped, and the record says not done. It doesn't promise
// the agent stopped; what it already made stays with it.
func (a *App) TakeHandoffBack(id string) (HandoffStatus, error) {
	h, err := a.findHandoff(id)
	if err != nil {
		return HandoffStatus{}, err
	}
	if current, _ := a.handoffStatusByID(id); current.State == handoffBack || current.State == handoffTakenBack {
		return current, errHandoffDone
	}
	a.agentInboxService().Withdraw(func(item agentinbox.Item) bool { return item.ID == id })
	a.withdrawTaskLinks(func(l tasklink.Link) bool { return l.ID == id })
	if h.TaskID != "" && h.TaskState == "running" {
		if err := a.abortHandoff(h.TaskID, ""); err != nil {
			log.Printf("[work-on-it] the task didn't stop: %v", err)
		}
	}
	now := a.workOnIt().clock().UnixMilli()
	if err := a.updateHandoff(id, func(x *workonit.Handoff) { x.TakenBackAt = now }); err != nil {
		return HandoffStatus{}, err
	}
	if err := a.MarkWorkOnIt(id, workonit.StatusNotDone); err != nil && !errors.Is(err, errWorkOnItNoRecord) {
		log.Printf("[work-on-it] record not updated: %v", err)
	}
	log.Printf("[work-on-it] taken back agent=%s", h.AgentID)
	return a.handoffStatusByID(id)
}

// ── The phone (PROTOCOL.md 27.2) ──

func (bridge *mobileBridge) serveHandoffStatuses(w http.ResponseWriter) {
	list, err := bridge.app.GetHandoffStatuses()
	if err != nil {
		mobileJSON(w, http.StatusInternalServerError, nil, "hand-offs could not be read")
		return
	}
	mobileJSON(w, http.StatusOK, map[string]any{"handoffs": list}, "")
}

func (bridge *mobileBridge) serveHandoffAction(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, id, action string) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		var status HandoffStatus
		var err error
		switch action {
		case "ask-again":
			status, err = bridge.app.AskAgentAgain(id)
		default:
			status, err = bridge.app.TakeHandoffBack(id)
		}
		switch {
		case errors.Is(err, errWorkOnItNoHandoff):
			mobileJSON(w, http.StatusNotFound, nil, err.Error())
		case errors.Is(err, errHandoffDone):
			mobileJSON(w, http.StatusConflict, status, err.Error())
		case errors.Is(err, errWorkOnItClosedEyes):
			mobileJSON(w, http.StatusConflict, nil, "your computer isn't taking agent work right now")
		case err != nil:
			log.Printf("[work-on-it] phone %s failed: %v", action, err)
			mobileJSON(w, http.StatusInternalServerError, nil, "that didn't go through")
		default:
			mobileJSON(w, http.StatusOK, status, "")
		}
	})
}
