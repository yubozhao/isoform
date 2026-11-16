package main

// The connected agent's channel (phone-agent v1.1.0 rules 7 to 9, B5):
// internal/agentinbox serves /a/inbox and /a/results/<id> beside the task
// links. A Yes for a connected agent puts the task in its inbox; its result
// lands where task-link results do (Back from {agent} in Your work), and
// the text files it made are kept under agents/files/<id>/ for Made by your
// agents. Waiting items are kept across restarts (they hold the task text,
// like open task links), spent by any privacy withdrawal, and dropped when
// the agent is disconnected.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"wails-gui/internal/agentinbox"
	"wails-gui/internal/pii"
	"wails-gui/internal/tasklink"
	"wails-gui/internal/workonit"
)

const (
	agentsInboxFile = "inbox.json"
	agentsFilesDir  = "files"
	agentFileMeta   = ".source.json"
)

type agentInboxState struct {
	once        sync.Once
	box         *agentinbox.Inbox
	fileMu      sync.Mutex
	unsubscribe func()
	// openFile is a test seam for OpenAgentFile: the arguments to open.
	openFile func(args []string) error
}

func (a *App) agentInboxService() *agentinbox.Inbox {
	s := &a.agentInbox
	s.once.Do(func() {
		s.box = agentinbox.New(agentinbox.Hooks{
			Authenticate: func(r *http.Request) (string, bool) {
				agent, ok := a.agentJoinService().Authenticate(r)
				return agent.ID, ok
			},
			Allowed:  a.agentsAllowed,
			Changed:  a.saveAgentInbox,
			Taken:    a.agentInboxTaken,
			Resulted: a.agentInboxResulted,
			Now:      a.agentJoin.now,
		})
		if items, err := a.readAgentInbox(); err != nil {
			log.Printf("[agents] inbox not restored: %v", err)
		} else {
			s.box.Restore(items)
		}
		s.unsubscribe = subscribePrivacyWithdrawals(a.agentInboxPrivacyWithdrawal)
	})
	return s.box
}

// connectedAgentFor is the joined agent a Yes for this agent goes to: the
// joined agent itself, or the latest one that is this catalog agent.
func (a *App) connectedAgentFor(agentID string) (ConnectedAgentView, bool) {
	var found ConnectedAgentView
	ok := false
	for _, c := range a.GetConnectedAgents() {
		if c.ID == agentID || (c.CatalogID != "" && c.CatalogID == agentID) {
			if !ok || c.JoinedAt >= found.JoinedAt {
				found, ok = c, true
			}
		}
	}
	return found, ok
}

// connectedHandoffReady says a Yes for this agent can go to its inbox now.
func (a *App) connectedHandoffReady(agentID string) bool {
	if a.agentConnectedHere(agentID) {
		return !a.IsClosedEyesModeEnabled()
	}
	if a.agentsAllowed() != nil {
		return false
	}
	_, ok := a.connectedAgentFor(agentID)
	return ok
}

// sendWorkOnItConnected hands the task to a connected agent: a task pinned
// to a coding agent connected here (its id returned), or the joined agent's
// inbox.
func (a *App) sendWorkOnItConnected(task workonit.Task, agentID, recordID string) (string, error) {
	if a.agentConnectedHere(agentID) {
		if a.IsClosedEyesModeEnabled() {
			return "", errWorkOnItClosedEyes
		}
		return a.startAgentHereTask(task, agentID)
	}
	if err := a.agentsAllowed(); err != nil {
		return "", err
	}
	joined, ok := a.connectedAgentFor(agentID)
	if !ok {
		return "", errWorkOnItNoMethod
	}
	if err := a.startTaskLinkListener(); err != nil {
		return "", err
	}
	a.agentInboxService().Add(agentinbox.Item{ID: recordID, AgentID: joined.ID, SourceKind: task.SourceKind, SourceID: task.SourceID, Text: task.Text})
	return "", nil
}

// connectedVia is how a connected hand-off went: a task here, or the inbox.
func connectedVia(taskID string) string {
	if taskID != "" {
		return "task"
	}
	return "inbox"
}

func (a *App) agentInboxTaken(item agentinbox.Item) {
	a.workOnItTaskLinkTaken(tasklink.Link{ID: item.ID})
}

func (a *App) agentInboxResulted(item agentinbox.Item, result agentinbox.Result) {
	names := a.keepAgentFiles(item, result.Files)
	agentID := item.AgentID
	if joined, ok := a.connectedAgentFor(item.AgentID); ok && joined.CatalogID != "" {
		agentID = joined.CatalogID
	}
	record := TaskLinkResult{
		LinkID: item.ID, AgentID: agentID, SourceKind: item.SourceKind, SourceID: item.SourceID,
		Status: result.Status, Summary: result.Summary, Links: result.Links, Files: names,
		TakenAt: item.TakenAt, ResultAt: a.taskLinks.clock().UnixMilli(), Via: "inbox",
	}
	if err := a.rewriteTaskLinkResults(func(results []TaskLinkResult) []TaskLinkResult {
		return append(results, record)
	}); err != nil {
		log.Printf("[agents] result not saved: %v", err)
	}
	a.workOnItTaskLinkResulted(tasklink.Link{ID: item.ID}, result.Result)
	a.saveConnectedAgents()
}

// keepAgentFiles writes the result's files, 0600, and returns their names.
func (a *App) keepAgentFiles(item agentinbox.Item, files []agentinbox.File) []string {
	if len(files) == 0 {
		return nil
	}
	dir, err := a.agentFilesDir(item.ID)
	if err != nil {
		log.Printf("[agents] files not kept: %v", err)
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("[agents] files not kept: %v", err)
		return nil
	}
	meta, _ := json.Marshal(map[string]string{"sourceKind": item.SourceKind, "sourceId": item.SourceID})
	_ = os.WriteFile(filepath.Join(dir, agentFileMeta), meta, 0o600)
	var names []string
	for _, f := range files {
		// Names are checked by agentinbox.ParseResult; Base is a second guard.
		name := filepath.Base(f.Name)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(f.Text), 0o600); err != nil {
			log.Printf("[agents] a file was not kept: %v", err)
			continue
		}
		if err := quarantineAgentFile(path); err != nil {
			// Unflagged, it could run without a prompt: don't keep it.
			log.Printf("[agents] a file was not kept: quarantine: %v", err)
			_ = os.Remove(path)
			continue
		}
		names = append(names, name)
	}
	return names
}

func (a *App) agentFilesDir(id string) (string, error) {
	root, err := a.agentJoin.dirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, agentsFilesDir, filepath.Base(id)), nil
}

// agentInboxPrivacyWithdrawal spends every waiting item on any withdrawal,
// as task links are, and removes the files of a withdrawn source's results.
func (a *App) agentInboxPrivacyWithdrawal(w privacyWithdrawal) {
	if w.Retention {
		return
	}
	if n := a.agentInboxService().Withdraw(nil); n > 0 {
		log.Printf("[agents] withdrew %d waiting hand-off(s) after a privacy withdrawal", n)
	}
	if w.Scope.Kind != privacyWithdrawalSource || w.Scope.ID == "" {
		return
	}
	root, err := a.agentJoin.dirPath()
	if err != nil {
		return
	}
	dirs, _ := os.ReadDir(filepath.Join(root, agentsFilesDir))
	for _, d := range dirs {
		path := filepath.Join(root, agentsFilesDir, d.Name())
		data, err := os.ReadFile(filepath.Join(path, agentFileMeta))
		if err != nil {
			continue
		}
		var meta struct{ SourceKind, SourceID string }
		if json.Unmarshal(data, &meta) == nil && meta.SourceID == w.Scope.ID && (w.Scope.SourceKind == "" || meta.SourceKind == w.Scope.SourceKind) {
			_ = os.RemoveAll(path)
		}
	}
}

// ── files ──

func (a *App) readAgentInbox() ([]agentinbox.Item, error) {
	a.agentInbox.fileMu.Lock()
	defer a.agentInbox.fileMu.Unlock()
	root, err := a.agentJoin.dirPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, agentsInboxFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var items []agentinbox.Item
	return items, json.Unmarshal(data, &items)
}

func (a *App) saveAgentInbox() {
	box := a.agentInbox.box
	if box == nil {
		return
	}
	items := box.Items()
	a.agentInbox.fileMu.Lock()
	defer a.agentInbox.fileMu.Unlock()
	root, err := a.agentJoin.dirPath()
	if err == nil {
		err = writeFrontJSON(filepath.Join(root, agentsInboxFile), items)
	}
	if err != nil {
		log.Printf("[agents] inbox not saved: %v", err)
	}
}

// ── The phone ──

var errAgentNotConnected = errors.New("that agent isn't connected; send it from the phone's own sheet")

// handOffToConnectedAgent is a Yes from the phone (or any surface without
// the sheet) for a connected agent: the computer writes the task from the
// item's own words, puts it in the agent's inbox, and keeps the hand-off.
// agentID empty means the person's agent for the work's kind.
func (a *App) handOffToConnectedAgent(in workonit.Input, agentID, surface string) (string, string, error) {
	if a.IsClosedEyesModeEnabled() {
		return "", "", errWorkOnItClosedEyes
	}
	if !workonit.ValidSource(in.SourceKind) {
		return "", "", errWorkOnItBadSource
	}
	in.Title, in.Detail, in.Done = pii.DisplayText(in.Title), pii.DisplayText(in.Detail), pii.DisplayText(in.Done)
	if strings.TrimSpace(in.Title) == "" {
		return "", "", errWorkOnItNoTitle
	}
	kind := workonit.KindOf(in)
	if agentID == "" {
		agentID = a.yourAgentChoice(kind)
	}
	if !a.connectedHandoffReady(agentID) {
		return "", "", errAgentNotConnected
	}
	s := a.workOnIt()
	task := workonit.Task{ID: workOnItID(), SourceKind: in.SourceKind, SourceID: in.SourceID, Kind: kind, Text: workonit.PrepareText(in, workOnItWords(a.currentUILanguage())), PreparedAt: s.clock()}
	recordID := workOnItID()
	taskID, err := a.sendWorkOnItConnected(task, agentID, recordID)
	if err != nil {
		return "", "", err
	}
	record := workonit.Record{ID: recordID, SourceKind: task.SourceKind, AgentID: agentID, Method: workonit.MethodConnected, SentAt: s.clock().UnixMilli(), Status: workonit.StatusSent}
	if err := a.workOnItAppend(record); err != nil {
		log.Printf("[work-on-it] record not saved: %v", err)
	}
	a.keepWorkOnItHandoff(workonit.Handoff{
		ID: recordID, SourceKind: task.SourceKind, SourceID: task.SourceID, Kind: kind, AgentID: agentID,
		Method: workonit.MethodConnected, Via: connectedVia(taskID), Text: task.Text, SentAt: record.SentAt, Surface: surface, TaskID: taskID,
	})
	log.Printf("[work-on-it] sent source=%s agent=%s method=connected surface=%s", task.SourceKind, agentID, surface)
	return recordID, agentID, nil
}

func (bridge *mobileBridge) serveAgentHandoff(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		AgentID        string `json:"agentId"`
		Kind           string `json:"kind"`
		SourceKind     string `json:"sourceKind"`
		SourceID       string `json:"sourceId"`
		Title          string `json:"title"`
		Detail         string `json:"detail"`
		Done           string `json:"done"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	if input.Kind != "" && !workonit.ValidKind(input.Kind) {
		mobileJSON(w, http.StatusBadRequest, nil, errYourAgentBadKind.Error())
		return
	}
	if len(input.Title)+len(input.Detail)+len(input.Done) > workonit.MaxTaskRunes*4 {
		mobileJSON(w, http.StatusBadRequest, nil, "the task is too long")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		in := workonit.Input{SourceKind: input.SourceKind, SourceID: input.SourceID, Title: input.Title, Detail: input.Detail, Done: input.Done, Kind: input.Kind}
		recordID, agentID, err := bridge.app.handOffToConnectedAgent(in, strings.TrimSpace(input.AgentID), "phone")
		switch {
		case errors.Is(err, errAgentNotConnected):
			mobileJSON(w, http.StatusConflict, nil, err.Error())
		case errors.Is(err, errWorkOnItBadSource), errors.Is(err, errWorkOnItNoTitle):
			mobileJSON(w, http.StatusBadRequest, nil, err.Error())
		case errors.Is(err, errWorkOnItClosedEyes), errors.Is(err, errTaskLinksOff), errors.Is(err, errTaskLinksClosedEyes):
			mobileJSON(w, http.StatusConflict, nil, "your computer isn't taking agent work right now")
		case err != nil:
			log.Printf("[your-agents] phone hand-off failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the hand-off could not be sent")
		default:
			mobileJSON(w, http.StatusCreated, map[string]string{"recordId": recordID, "agentId": agentID}, "")
		}
	})
}

var errAgentFileUnknown = errors.New("that file isn't one an agent made")

// agentTextExts are the file kinds an agent's text may be opened as. They
// open in TextEdit (open -e), never in the app the file names, so an
// agent's file can't run or open anything. Everything else is only shown in
// Finder (open -R), where macOS's quarantine prompt still applies.
var agentTextExts = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".text": true, ".csv": true, ".tsv": true,
	".json": true, ".yaml": true, ".yml": true, ".log": true, ".diff": true, ".patch": true,
}

func agentFileOpenArgs(path string) []string {
	if agentTextExts[strings.ToLower(filepath.Ext(path))] {
		return []string{"-e", path}
	}
	return []string{"-R", path}
}

// agentResultFilePath is where a file a connected agent made is kept, and
// only for a file named in a kept result: the one check every reader (Open
// here, the phone's file read) goes through.
func (a *App) agentResultFilePath(linkID, name string) (string, error) {
	results, err := a.GetTaskLinkResults()
	if err != nil {
		return "", err
	}
	for _, r := range results {
		if r.LinkID != linkID {
			continue
		}
		for _, f := range r.Files {
			if f != name || filepath.Base(name) != name {
				continue
			}
			dir, err := a.agentFilesDir(linkID)
			if err != nil {
				return "", err
			}
			path := filepath.Join(dir, name)
			if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
				return "", errAgentFileUnknown
			}
			return path, nil
		}
	}
	return "", errAgentFileUnknown
}

// OpenAgentFile opens a file a connected agent made, on this computer, with
// the app the person uses for it. Only a file named in a kept result opens.
func (a *App) OpenAgentFile(linkID, name string) error {
	path, err := a.agentResultFilePath(linkID, name)
	if err != nil {
		return err
	}
	args := agentFileOpenArgs(path)
	if open := a.agentInbox.openFile; open != nil {
		return open(args)
	}
	return exec.Command("/usr/bin/open", args...).Run()
}
