package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"wails-gui/internal/topicday"
)

// Work with an agent, from Your work (phone-agent v1.2.0 rule 3, v1.2.1
// rules 2 to 4; PROTOCOL.md 26.6): a result's files (Read it for text, Open
// on your computer for the rest), Take it back and Ask again. They go through
// the same answer route and the same functions as the computer's Agents page.

const (
	workActionTakeBack = "take_back"
	workActionAskAgain = "ask_again"
	workActionOpenFile = "open_file"

	// workFileReadMax is the most text a phone reads from a result's file.
	workFileReadMax = 256 << 10
	// workFileWhere is where an agent's file lives, in the person's words;
	// the phone adds ", on your computer".
	workFileWhere = "In Isoform's Agents page"
)

var (
	errWorkFileUnknown  = errors.New("that file isn't one this item's agent made")
	errWorkFileTooBig   = errors.New("that file is too big to read on the phone")
	errWorkFileNotText  = errors.New("that file isn't text; open it on your computer")
	errWorkNotTakingNow = errors.New("your computer isn't taking agent work right now")
)

// WorkFile is a file an agent made with its result. Readable says the phone
// may read it as text (Read it); any other file opens on the computer.
type WorkFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Where    string `json:"where"`
	Readable bool   `json:"readable"`
	Bytes    int64  `json:"bytes,omitempty"`
}

// WorkTakenBack marks work the person took back from an agent: it is offered
// again, at the top of Your work.
type WorkTakenBack struct {
	AgentID   string `json:"agentId,omitempty"`
	AgentName string `json:"agentName"`
	At        string `json:"at"`
	UndoUntil string `json:"undoUntil,omitempty"`
}

// workFileID names a result's file without its name in the URL.
func workFileID(linkID, name string) string {
	sum := sha256.Sum256([]byte(linkID + "/" + name))
	return "f_" + hex.EncodeToString(sum[:6])
}

// workLinkResults are the agents' kept results by hand-off, newest first.
func (a *App) workLinkResults() map[string]TaskLinkResult {
	out := map[string]TaskLinkResult{}
	results, err := a.GetTaskLinkResults()
	if err != nil {
		return out
	}
	for _, r := range results {
		if _, seen := out[r.LinkID]; !seen {
			out[r.LinkID] = r
		}
	}
	return out
}

// workResultFiles lists a result's files that are still kept.
func (a *App) workResultFiles(linkID string, names []string) []WorkFile {
	files := []WorkFile{}
	for _, name := range names {
		path, err := a.agentResultFilePath(linkID, name)
		if err != nil {
			continue
		}
		file := WorkFile{ID: workFileID(linkID, name), Name: name, Where: workFileWhere,
			Readable: agentTextExts[strings.ToLower(filepath.Ext(name))]}
		if info, err := os.Stat(path); err == nil {
			file.Bytes = info.Size()
			if info.Size() > workFileReadMax {
				file.Readable = false
			}
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil
	}
	return files
}

// workItemFile finds one of the item's files.
func workItemFile(item WorkItem, fileID string) (WorkFile, bool) {
	for _, f := range item.Files {
		if f.ID == fileID {
			return f, true
		}
	}
	return WorkFile{}, false
}

// workHandoffOf is the hand-off an item's work went out as: the Work on it
// record of a connected agent, else the local task, whose hand-off shares
// its id.
func workHandoffOf(item WorkItem, record AnswerRecord, answered bool) string {
	if answered {
		if record.HandoffID != "" {
			return record.HandoffID
		}
		if record.TaskID != "" {
			return record.TaskID
		}
	}
	if item.Ref.HandoffID != "" {
		return item.Ref.HandoffID
	}
	return item.Ref.TaskID
}

// workHasHandoff says whether a hand-off is kept under an id.
func (a *App) workHasHandoff(id string) bool {
	_, err := a.findHandoff(id)
	return err == nil
}

// takeWorkBack stops the agent reading the work (TakeHandoffBack, as the
// Agents page does) and offers the item again. A topic's proposal goes back
// to proposed, so a later Yes starts it fresh.
func (a *App) takeWorkBack(item WorkItem, record AnswerRecord, answered bool) (string, error) {
	if item.State != workStateWithAgent {
		return "", errWorkItemChanged
	}
	id := workHandoffOf(item, record, answered)
	switch {
	case id != "" && a.workHasHandoff(id):
		if _, err := a.TakeHandoffBack(id); err != nil {
			if errors.Is(err, errHandoffDone) {
				return "", errWorkItemChanged
			}
			return "", err
		}
	case item.Ref.TaskID == "":
		return "", errWorkItemChanged
	}
	if item.Ref.TaskID != "" {
		// A topic's own task (no hand-off record), or a hand-off kept before
		// it carried its task: stop the task here too. A stopped one stays.
		if err := a.abortHandoff(item.Ref.TaskID, ""); err != nil {
			log.Printf("[work] the task didn't stop: %v", err)
		}
	}
	if item.Source == workSourceTopic {
		if err := a.reofferDailyTopicProposal(item.Ref.TopicID, item.Ref.ProposalID); err != nil {
			log.Printf("[work] the topic's proposal was not offered again: %v", err)
		}
	}
	return id, nil
}

// undoWorkTakeBack takes back a take back: the work goes to its agent again
// as the Yes before it said, and the item is with that agent once more. The
// send entry carries the new hand-off; nothing is kept if it can't go.
func (a *App) undoWorkTakeBack(ctx context.Context, item WorkItem, answers []AnswerRecord, undo AnswerRecord, meta workAnswerMeta) (WorkItem, error) {
	before, answered := answersStanding(append(append([]AnswerRecord(nil), answers...), undo))[item.ID]
	input := WorkAnswerInput{Action: workActionYes}
	if answered && (before.Action == workActionYes || before.Action == workActionEdit) {
		input = WorkAnswerInput{Action: before.Action, Removed: before.Removed, AgentID: before.AgentID}
		if before.Edited != nil {
			input.Before, input.After = before.Edited.Before, before.Edited.After
		}
	}
	pending := item
	pending.State, pending.Answer, pending.TakenBack = workStateOpen, nil, nil
	sent, err := a.dispatchWorkAnswer(ctx, pending, input, meta.Surface)
	if err != nil {
		return item, err
	}
	ledger := a.answersLedger()
	if _, err := ledger.append(undo); err != nil {
		return item, err
	}
	if answered {
		agentID := sent.AgentID
		if agentID == "" {
			agentID = input.AgentID
		}
		// With no Yes in the log (a topic answered on its own page), the
		// topic's store holds the new state and the undo is enough.
		if _, err := ledger.append(AnswerRecord{ItemID: item.ID, Kind: answerKindWork, Source: item.Source, Title: item.Work.Title,
			Action: workActionSend, At: undo.At, Surface: meta.Surface, DeviceID: meta.DeviceID,
			TaskID: sent.TaskID, HandoffID: sent.HandoffID, AgentID: agentID, Removed: input.Removed}); err != nil {
			return item, err
		}
	}
	a.emitBrowserEvent(workAnsweredEvent, item.ID)
	return a.workAnswerItemNow(ctx, item), nil
}

// askWorkAgain re-offers the work to the same agent (AskAgentAgain, as the
// Agents page does). The item stays with the agent.
func (a *App) askWorkAgain(item WorkItem, record AnswerRecord, answered bool) error {
	if item.State != workStateWithAgent {
		return errWorkItemChanged
	}
	id := workHandoffOf(item, record, answered)
	if id != "" && a.workHasHandoff(id) {
		switch _, err := a.AskAgentAgain(id); {
		case errors.Is(err, errHandoffDone):
			return errWorkItemChanged
		case errors.Is(err, errWorkOnItClosedEyes):
			return errWorkNotTakingNow
		default:
			return err
		}
	}
	if item.Ref.TaskID == "" || a.handoffStore == nil {
		return errWorkItemChanged
	}
	if a.IsClosedEyesModeEnabled() {
		return errWorkNotTakingNow
	}
	entry, err := a.handoffStore.Get(item.Ref.TaskID)
	if err != nil {
		return errWorkItemGone
	}
	if state, _ := dailyTopicTaskState(entry); state == topicday.StateRunning {
		// It is running; there is nothing to start again.
		return nil
	}
	_, err = a.StartHandoffAgain(item.Ref.TaskID)
	return err
}

// openWorkFile opens a result's file on this computer: text in TextEdit,
// anything else shown in Finder (OpenAgentFile).
func (a *App) openWorkFile(item WorkItem, fileID string) error {
	file, ok := workItemFile(item, fileID)
	if !ok || item.Ref.HandoffID == "" {
		return errWorkFileUnknown
	}
	if err := a.OpenAgentFile(item.Ref.HandoffID, file.Name); err != nil {
		if errors.Is(err, errAgentFileUnknown) {
			return errWorkFileUnknown
		}
		return err
	}
	return nil
}

// readWorkFile is a result's text file, for Read it on the phone.
func (a *App) readWorkFile(item WorkItem, fileID string) (string, string, error) {
	file, ok := workItemFile(item, fileID)
	if !ok || item.Ref.HandoffID == "" {
		return "", "", errWorkFileUnknown
	}
	path, err := a.agentResultFilePath(item.Ref.HandoffID, file.Name)
	if err != nil {
		return "", "", errWorkFileUnknown
	}
	if !agentTextExts[strings.ToLower(filepath.Ext(file.Name))] {
		return "", "", errWorkFileNotText
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", errWorkFileUnknown
	}
	if info.Size() > workFileReadMax {
		return "", "", errWorkFileTooBig
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", errWorkFileUnknown
	}
	if !utf8.Valid(data) {
		return "", "", errWorkFileNotText
	}
	return file.Name, string(data), nil
}

// reofferDailyTopicProposal puts a proposal taken back from its agent back
// to proposed, with no task.
func (a *App) reofferDailyTopicProposal(topicID, proposalID string) error {
	store, err := a.topicsDailyStore()
	if err != nil {
		return err
	}
	topicsDailyAnswerMu.Lock()
	defer topicsDailyAnswerMu.Unlock()
	_, err = store.UpdateProposal(topicID, proposalID, func(_ *topicday.Topic, p *topicday.Proposal) error {
		switch p.State {
		case topicday.StateRunning, topicday.StateDone, topicday.StateFailed:
		default:
			return errWorkItemChanged
		}
		p.State, p.TaskID, p.Result, p.AnsweredAt = topicday.StateProposed, "", "", 0
		return nil
	})
	if err == nil {
		a.emitBrowserEvent("topics-daily:updated", map[string]any{"topicId": topicID})
	}
	return err
}

// workTakenBackFirst puts work taken back from an agent at the top of the
// list, in the order it was.
func workTakenBackFirst(items []WorkItem) []WorkItem {
	out := make([]WorkItem, 0, len(items))
	for _, item := range items {
		if item.TakenBack != nil {
			out = append(out, item)
		}
	}
	for _, item := range items {
		if item.TakenBack == nil {
			out = append(out, item)
		}
	}
	return out
}

// serveWorkFile answers GET /work/{id}/file?file= (PROTOCOL.md 26.6): the
// text of a result's file, read-only, from an item this phone may read.
func (bridge *mobileBridge) serveWorkFile(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, id string) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	list, err := bridge.app.workListNow()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	item, ok := workFind(mobileWorkListForDevice(list, device), id)
	if !ok {
		mobileJSON(w, http.StatusNotFound, nil, errWorkItemGone.Error())
		return
	}
	name, text, err := bridge.app.readWorkFile(item, r.URL.Query().Get("file"))
	switch {
	case errors.Is(err, errWorkFileTooBig):
		mobileJSON(w, http.StatusRequestEntityTooLarge, nil, err.Error())
	case errors.Is(err, errWorkFileNotText):
		mobileJSON(w, http.StatusUnsupportedMediaType, nil, err.Error())
	case err != nil:
		mobileJSON(w, http.StatusNotFound, nil, errWorkFileUnknown.Error())
	default:
		mobileJSON(w, http.StatusOK, map[string]string{"name": name, "text": text}, "")
	}
}

// workAnswerItemNow is the item as the list has it after an action.
func (a *App) workAnswerItemNow(ctx context.Context, item WorkItem) WorkItem {
	after, err := a.buildWorkList(ctx, time.Now())
	if err != nil {
		return item
	}
	if updated, ok := workFind(after, item.ID); ok {
		return updated
	}
	return item
}
