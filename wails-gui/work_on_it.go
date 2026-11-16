package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/tasklink"
	"wails-gui/internal/workonit"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Work on it, v1: the prepared-task path (docs/work-on-it-agents.md).
//
// The surface (a brief item, a topic proposal, a chat card) passes the words
// it shows. Isoform prepares the task from those words alone, shows it, and
// on the person's tap hands it to the agent they pick: a deep link, opening
// the agent with the task on the clipboard, or a message through Messages.
// Only counts are kept (which agent, how, sent, taken, done), never the text.
// Everything is behind work_on_it_enabled, on unless the person turned it off
// (the hub, 2026-11-15: Chat's and Today's hand-offs need it).

const (
	workOnItEnabledKey     = "work_on_it_enabled"
	workOnItHandleKeyPre   = "work_on_it_handle_"
	workOnItRankWindow     = 14 * 24 * time.Hour
	workOnItRankCacheFor   = 24 * time.Hour
	workOnItPreparedTTL    = time.Hour
	workOnItTakenWindow    = 15 * time.Minute
	workOnItRecordsFile    = "records.json"
	workOnItRecordsKeepFor = 90 * 24 * time.Hour
)

var (
	errWorkOnItOff        = errors.New("work on it is off")
	errWorkOnItNoTask     = errors.New("the prepared task is gone; prepare it again")
	errWorkOnItNoAgent    = errors.New("unknown agent")
	errWorkOnItNoMethod   = errors.New("that agent can't take this handoff")
	errWorkOnItNoTitle    = errors.New("nothing to send")
	errWorkOnItBadSource  = errors.New("unknown source")
	errWorkOnItBadStatus  = errors.New("unknown status")
	errWorkOnItNoRecord   = errors.New("unknown task")
	errWorkOnItBadHandle  = errors.New("enter the agent's phone number or Apple ID")
	errWorkOnItClosedEyes = errors.New("closed eyes is on")
)

// workOnItState holds prepared tasks (in memory only), the ranking cache and
// the records file.
type workOnItState struct {
	mu        sync.Mutex
	prepared  map[string]workonit.Task
	usage     map[string]workonit.Usage
	usageAt   time.Time
	recordsMu sync.Mutex
	// test seams
	open    func(target string) error
	copy    func(text string) error
	send    func(ctx context.Context, to, body string) error
	present func(agent workonit.Agent) bool
	now     func() time.Time
	paste   *pasteDeps
	// pasteDone is told when a paste run ends (tests wait on it).
	pasteDone func(err error)
	// taskLinkReady says whether a task link can reach an outside agent.
	taskLinkReady func() bool
	// connectedReady says whether an agent has joined and can take work now.
	connectedReady func(agentID string) bool
}

func (s *workOnItState) pasteDeps() pasteDeps {
	if s.paste != nil {
		return *s.paste
	}
	return defaultPasteDeps()
}

// agentMethods are the agent's handoffs on this computer: paste is offered
// only when a computer-use agent for it is installed.
func (s *workOnItState) agentMethods(agent workonit.Agent, handle string) []string {
	installed := s.agentPresent(agent)
	var out []string
	if s.connectedReady != nil && s.connectedReady(agent.ID) {
		out = append(out, workonit.MethodConnected)
	}
	if s.taskLinkReady != nil && s.taskLinkReady() && agent.TaskLinkCarrier(installed, handle) != "" {
		out = append(out, workonit.MethodTaskLink)
	}
	for _, m := range agent.Methods(installed, handle) {
		if m == workonit.MethodPaste {
			if _, ok := planPasteIntoAgent(agent, installed, s.pasteDeps().lookPath, os.TempDir()); !ok {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func (a *App) workOnIt() *workOnItState {
	a.workOnItOnce.Do(func() {
		a.workOnItS = &workOnItState{prepared: map[string]workonit.Task{}, taskLinkReady: a.taskLinkHandoffReady, connectedReady: a.connectedHandoffReady}
	})
	return a.workOnItS
}

func (s *workOnItState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (a *App) workOnItEnabled() bool {
	if a == nil || a.store == nil {
		return false
	}
	value, err := a.store.GetUIState(context.Background(), workOnItEnabledKey)
	if err != nil {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(value), "false")
}

// WorkOnItAgentView is one agent in the menu.
type WorkOnItAgentView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Methods   []string `json:"methods"`
	Default   bool     `json:"default"`
	TermsNote string   `json:"termsNote,omitempty"`
	// NeedsHandle is true for a text agent with no number set yet.
	NeedsHandle bool `json:"needsHandle,omitempty"`
}

// WorkOnItPrepared is what the sheet shows before the person taps.
type WorkOnItPrepared struct {
	TaskID string `json:"taskId"`
	Text   string `json:"text"`
	// Kind is the kind of work: code or else. The default agent is the
	// person's choice for it when that agent can take the hand-off.
	Kind   string              `json:"kind"`
	Agents []WorkOnItAgentView `json:"agents"`
}

// WorkOnItSent says how the task left.
type WorkOnItSent struct {
	RecordID string `json:"recordId"`
	Method   string `json:"method"`
	// Copied is true when the person still has to paste the task.
	Copied bool `json:"copied"`
	// Via is how a task link's one line went: deep_link, message or
	// open_copy. Empty for other methods.
	Via string `json:"via,omitempty"`
}

// WorkOnItCounts are the measures, for the morning report.
type WorkOnItCounts struct {
	Sent    int            `json:"sent"`
	Taken   int            `json:"taken"`
	Done    int            `json:"done"`
	NotDone int            `json:"notDone"`
	ByAgent map[string]int `json:"byAgent"`
}

// GetWorkOnItEnabled reports the switch.
func (a *App) GetWorkOnItEnabled() bool { return a.workOnItEnabled() }

// SetWorkOnItEnabled turns Work on it on or off. Off keeps what was sent.
func (a *App) SetWorkOnItEnabled(enabled bool) error {
	if a == nil || a.store == nil {
		return errors.New("storage is not ready")
	}
	return a.store.SetUIState(context.Background(), workOnItEnabledKey, fmt.Sprintf("%t", enabled))
}

// PrepareWorkOnIt writes the task from the item's own words. Closed Eyes
// stops it. The text is redacted the way every read model is.
func (a *App) PrepareWorkOnIt(in workonit.Input) (WorkOnItPrepared, error) {
	if !a.workOnItEnabled() {
		return WorkOnItPrepared{}, errWorkOnItOff
	}
	if a.IsClosedEyesModeEnabled() {
		return WorkOnItPrepared{}, errWorkOnItClosedEyes
	}
	if !workonit.ValidSource(in.SourceKind) {
		return WorkOnItPrepared{}, errWorkOnItBadSource
	}
	in.Title = pii.DisplayText(in.Title)
	in.Detail = pii.DisplayText(in.Detail)
	in.Done = pii.DisplayText(in.Done)
	if strings.TrimSpace(in.Title) == "" {
		return WorkOnItPrepared{}, errWorkOnItNoTitle
	}
	text := workonit.PrepareText(in, workOnItWords(a.currentUILanguage()))
	s := a.workOnIt()
	kind := workonit.KindOf(in)
	task := workonit.Task{ID: workOnItID(), SourceKind: in.SourceKind, SourceID: in.SourceID, Kind: kind, Text: text, PreparedAt: s.clock()}
	s.mu.Lock()
	for id, old := range s.prepared {
		if s.clock().Sub(old.PreparedAt) > workOnItPreparedTTL {
			delete(s.prepared, id)
		}
	}
	s.prepared[task.ID] = task
	s.mu.Unlock()
	agents, err := a.workOnItAgents()
	if err != nil {
		return WorkOnItPrepared{}, err
	}
	return WorkOnItPrepared{TaskID: task.ID, Text: text, Kind: kind, Agents: preferYourAgent(agents, a.yourAgentChoice(kind))}, nil
}

// GetWorkOnItAgents is the ranked menu, the top one the default.
func (a *App) GetWorkOnItAgents() ([]WorkOnItAgentView, error) {
	if !a.workOnItEnabled() {
		return nil, errWorkOnItOff
	}
	return a.workOnItAgents()
}

func (a *App) workOnItAgents() ([]WorkOnItAgentView, error) {
	s := a.workOnIt()
	usage := a.workOnItUsage()
	installed := map[string]bool{}
	handles := map[string]string{}
	for _, agent := range workonit.Catalog() {
		installed[agent.ID] = s.agentPresent(agent)
		handles[agent.ID] = a.workOnItHandle(agent.ID)
	}
	ranked := workonit.Rank(usage, installed, handles)
	out := make([]WorkOnItAgentView, 0, len(ranked)+1)
	for _, r := range ranked {
		methods := s.agentMethods(r.Agent, handles[r.Agent.ID])
		if len(methods) == 0 {
			continue
		}
		out = append(out, WorkOnItAgentView{ID: r.Agent.ID, Name: r.Agent.Name, Methods: methods, Default: len(out) == 0, TermsNote: r.Agent.TermsNote})
	}
	// A known agent that only joined (Claude Code) has no other way in.
	listed := map[string]bool{}
	for _, view := range out {
		listed[view.ID] = true
	}
	for _, agent := range workonit.Catalog() {
		if listed[agent.ID] {
			continue
		}
		if methods := s.agentMethods(agent, handles[agent.ID]); len(methods) > 0 {
			out = append(out, WorkOnItAgentView{ID: agent.ID, Name: agent.Name, Methods: methods, Default: len(out) == 0, TermsNote: agent.TermsNote})
		}
	}
	// Any other agent that joined takes work in its inbox.
	if a.agentsAllowed() == nil {
		for _, c := range a.GetConnectedAgents() {
			if c.CatalogID == "" {
				out = append(out, WorkOnItAgentView{ID: c.ID, Name: c.Name, Methods: []string{workonit.MethodConnected}, Default: len(out) == 0})
			}
		}
	}
	// A text agent with no number is still offered, to set one.
	for _, agent := range workonit.Catalog() {
		if agent.Message && handles[agent.ID] == "" {
			out = append(out, WorkOnItAgentView{ID: agent.ID, Name: agent.Name, TermsNote: agent.TermsNote, NeedsHandle: true})
		}
	}
	return out, nil
}

// preferYourAgent makes the person's choice the default when it is in the
// menu; otherwise the ranked top stays the default.
func preferYourAgent(agents []WorkOnItAgentView, choice string) []WorkOnItAgentView {
	at := -1
	for i, view := range agents {
		if view.ID == choice && len(view.Methods) > 0 {
			at = i
		}
	}
	if at < 0 {
		return agents
	}
	for i := range agents {
		agents[i].Default = i == at
	}
	return agents
}

// SendWorkOnIt hands the prepared task over. The call is the person's tap.
func (a *App) SendWorkOnIt(taskID, agentID, method string) (WorkOnItSent, error) {
	if !a.workOnItEnabled() {
		return WorkOnItSent{}, errWorkOnItOff
	}
	if a.IsClosedEyesModeEnabled() {
		return WorkOnItSent{}, errWorkOnItClosedEyes
	}
	s := a.workOnIt()
	s.mu.Lock()
	task, ok := s.prepared[taskID]
	if ok && s.clock().Sub(task.PreparedAt) > workOnItPreparedTTL {
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return WorkOnItSent{}, errWorkOnItNoTask
	}
	agent, ok := workonit.AgentByID(agentID)
	if !ok {
		joined, found := a.connectedAgentFor(agentID)
		if !found || joined.CatalogID != "" {
			return WorkOnItSent{}, errWorkOnItNoAgent
		}
		agent = workonit.Agent{ID: joined.ID, Name: joined.Name, Kind: workonit.KindElse}
	}
	handle := a.workOnItHandle(agent.ID)
	allowed := false
	for _, m := range s.agentMethods(agent, handle) {
		if m == method {
			allowed = true
		}
	}
	if !ok && method == workonit.MethodConnected {
		allowed = true // a joined agent outside the catalog
	}
	if !allowed {
		return WorkOnItSent{}, errWorkOnItNoMethod
	}
	out := WorkOnItSent{Method: method}
	recordID := workOnItID()
	sentLine, startedTask := "", ""
	switch method {
	case workonit.MethodConnected:
		started, err := a.sendWorkOnItConnected(task, agent.ID, recordID)
		if err != nil {
			return WorkOnItSent{}, err
		}
		startedTask, out.Via = started, connectedVia(started)
	case workonit.MethodTaskLink:
		via, line, err := a.sendWorkOnItTaskLink(s, task, agent, handle, recordID)
		if err != nil {
			return WorkOnItSent{}, err
		}
		out.Via, out.Copied, sentLine = via, via == workonit.MethodOpenCopy, line
	case workonit.MethodDeepLink:
		link, err := agent.DeepLinkURL(task.Text)
		if err != nil {
			return WorkOnItSent{}, err
		}
		if err := s.openTarget(a, link); err != nil {
			return WorkOnItSent{}, err
		}
	case workonit.MethodPaste:
		// The text goes on the clipboard; the agent only presses Command+V.
		if err := s.copyText(a, task.Text); err != nil {
			return WorkOnItSent{}, err
		}
		a.startPasteIntoAgent(agent)
		out.Copied = true
	case workonit.MethodOpenCopy:
		if err := s.copyText(a, task.Text); err != nil {
			return WorkOnItSent{}, err
		}
		if err := s.openTarget(a, workOnItOpenTarget(agent, s.agentPresent(agent))); err != nil {
			return WorkOnItSent{}, err
		}
		out.Copied = true
	case workonit.MethodMessage:
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := s.sendText(ctx, handle, task.Text); err != nil {
			return WorkOnItSent{}, err
		}
	default:
		return WorkOnItSent{}, errWorkOnItNoMethod
	}
	record := workonit.Record{ID: recordID, SourceKind: task.SourceKind, AgentID: agent.ID, Method: method, SentAt: s.clock().UnixMilli(), Status: workonit.StatusSent}
	if err := a.workOnItAppend(record); err != nil {
		log.Printf("[work-on-it] record not saved: %v", err)
	}
	a.keepWorkOnItHandoff(workonit.Handoff{
		ID: recordID, SourceKind: task.SourceKind, SourceID: task.SourceID, Kind: task.Kind,
		AgentID: agent.ID, Method: method, Via: out.Via, Text: task.Text, Sent: sentLine,
		SentAt: record.SentAt, Surface: "computer", TaskID: startedTask,
	})
	s.mu.Lock()
	delete(s.prepared, taskID)
	s.mu.Unlock()
	out.RecordID = record.ID
	log.Printf("[work-on-it] sent source=%s agent=%s method=%s", task.SourceKind, agent.ID, method)
	return out, nil
}

// SetWorkOnItHandle stores the number or Apple ID a text agent answers on.
func (a *App) SetWorkOnItHandle(agentID, handle string) error {
	agent, ok := workonit.AgentByID(agentID)
	if !ok || !agent.Message {
		return errWorkOnItNoAgent
	}
	handle = strings.TrimSpace(handle)
	if handle == "" || len(handle) > 200 || strings.ContainsAny(handle, "\n\r\"") {
		return errWorkOnItBadHandle
	}
	return a.store.SetUIState(context.Background(), workOnItHandleKeyPre+agentID, handle)
}

func (a *App) workOnItHandle(agentID string) string {
	if a == nil || a.store == nil {
		return ""
	}
	value, err := a.store.GetUIState(context.Background(), workOnItHandleKeyPre+agentID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// MarkWorkOnIt records the person's answer: done or not done.
func (a *App) MarkWorkOnIt(recordID, status string) error {
	if status != workonit.StatusDone && status != workonit.StatusNotDone && status != workonit.StatusTaken {
		return errWorkOnItBadStatus
	}
	now := a.workOnIt().clock().UnixMilli()
	return a.workOnItUpdate(recordID, func(r *workonit.Record) {
		r.Status = status
		switch status {
		case workonit.StatusTaken:
			if r.TakenAt == 0 {
				r.TakenAt = now
			}
		case workonit.StatusDone:
			r.DoneAt = now
			if r.TakenAt == 0 {
				r.TakenAt = now
			}
		}
	})
}

// GetWorkOnItAsks returns the sent tasks with no result after a day, once
// each, so the surface can ask "Did Claude get it done?". It also marks
// tasks taken when capture shows the person working in that agent soon after
// sending.
func (a *App) GetWorkOnItAsks() ([]workonit.Record, error) {
	if !a.workOnItEnabled() {
		return nil, nil
	}
	s := a.workOnIt()
	now := s.clock()
	var due []workonit.Record
	err := a.workOnItRewrite(func(records []workonit.Record) []workonit.Record {
		for i := range records {
			r := &records[i]
			if r.Status == workonit.StatusSent && r.TakenAt == 0 && now.UnixMilli()-r.SentAt >= workOnItTakenWindow.Milliseconds() {
				if a.workOnItTakenInCapture(*r) {
					r.Status, r.TakenAt = workonit.StatusTaken, r.SentAt
				}
			}
			if r.DueToAsk(now) {
				r.AskedAt = now.UnixMilli()
				due = append(due, *r)
			}
		}
		return records
	})
	return due, err
}

// GetWorkOnItCounts are the measures: sent, taken, done, by agent.
func (a *App) GetWorkOnItCounts() (WorkOnItCounts, error) {
	out := WorkOnItCounts{ByAgent: map[string]int{}}
	records, err := a.workOnItRead()
	if err != nil {
		return out, err
	}
	for _, r := range records {
		out.Sent++
		out.ByAgent[r.AgentID]++
		if r.TakenAt != 0 {
			out.Taken++
		}
		switch r.Status {
		case workonit.StatusDone:
			out.Done++
		case workonit.StatusNotDone:
			out.NotDone++
		}
	}
	return out, nil
}

// ── Handoff actions ──

func (s *workOnItState) openTarget(a *App, target string) error {
	if s.open != nil {
		return s.open(target)
	}
	if strings.Contains(target, "://") {
		if a.ctx != nil {
			wailsRuntime.BrowserOpenURL(a.ctx, target)
			return nil
		}
	}
	return exec.Command("/usr/bin/open", target).Run()
}

func (s *workOnItState) copyText(a *App, text string) error {
	if s.copy != nil {
		return s.copy(text)
	}
	return a.NativeClipboardSetText(text)
}

func (s *workOnItState) sendText(ctx context.Context, to, body string) error {
	if s.send != nil {
		return s.send(ctx, to, body)
	}
	return defaultTextSender().Send(ctx, to, body)
}

// workOnItOpenTarget is the app when it is installed, the site otherwise.
func workOnItOpenTarget(agent workonit.Agent, installed bool) string {
	if installed {
		if path := workOnItAppPath(agent); path != "" {
			return path
		}
	}
	return agent.OpenURL
}

func (s *workOnItState) agentPresent(agent workonit.Agent) bool {
	if s.present != nil {
		return s.present(agent)
	}
	return workOnItAppPath(agent) != ""
}

// workOnItAppPath finds the agent's Mac app in the usual folders.
func workOnItAppPath(agent workonit.Agent) string {
	home, _ := os.UserHomeDir()
	for _, name := range agent.AppNames {
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			path := filepath.Join(dir, name+".app")
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				return path
			}
		}
	}
	return ""
}

// ── Ranking from capture ──

// workOnItUsage counts, per agent, the prompts typed in it and the results
// copied out of it over two weeks. It is built at most once a day and kept
// in memory; only counts.
func (a *App) workOnItUsage() map[string]workonit.Usage {
	s := a.workOnIt()
	s.mu.Lock()
	if s.usage != nil && s.clock().Sub(s.usageAt) < workOnItRankCacheFor {
		usage := s.usage
		s.mu.Unlock()
		return usage
	}
	s.mu.Unlock()
	usage := map[string]workonit.Usage{}
	if a.activity != nil && a.activity.store != nil {
		since := s.clock().Add(-workOnItRankWindow).UnixMilli()
		if counted, err := a.activity.store.workOnItAgentCounts(context.Background(), since, s.clock().UnixMilli()); err == nil {
			usage = counted
		} else {
			log.Printf("[work-on-it] usage not counted: %v", err)
		}
	}
	s.mu.Lock()
	s.usage, s.usageAt = usage, s.clock()
	s.mu.Unlock()
	return usage
}

// workOnItAgentSQL maps an event to a catalog agent id: its Mac app first,
// then a browser tab title that names the agent.
func workOnItAgentSQL() (string, []any) {
	var b strings.Builder
	var args []any
	b.WriteString("CASE")
	for _, agent := range workonit.Catalog() {
		for _, id := range agent.BundleIDs {
			b.WriteString(" WHEN json_extract(data,'$.appBundleId')=? THEN ?")
			args = append(args, id, agent.ID)
		}
		for _, name := range agent.AppNames {
			b.WriteString(" WHEN json_extract(data,'$.appName')=? THEN ?")
			args = append(args, name, agent.ID)
		}
	}
	for _, agent := range workonit.Catalog() {
		for _, word := range agent.TitleWords {
			b.WriteString(" WHEN instr(lower(COALESCE(json_extract(data,'$.windowTitle'),'')),?)>0 THEN ?")
			args = append(args, strings.ToLower(word), agent.ID)
		}
	}
	b.WriteString(" ELSE '' END")
	return b.String(), args
}

func (s *ActivityStore) workOnItAgentCounts(ctx context.Context, since, until int64) (map[string]workonit.Usage, error) {
	agentSQL, args := workOnItAgentSQL()
	query := `SELECT agent, kind, COUNT(*) FROM (SELECT ` + agentSQL + ` AS agent, kind FROM activity_events
		WHERE kind IN ('keyboard','clipboard') AND timestamp >= ? AND timestamp < ? AND json_valid(data))
		WHERE agent != '' GROUP BY agent, kind`
	args = append(args, since, until)
	rows, err := s.reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]workonit.Usage{}
	for rows.Next() {
		var agent, kind string
		var n int
		if err := rows.Scan(&agent, &kind, &n); err != nil {
			return nil, err
		}
		u := out[agent]
		switch kind {
		case "keyboard":
			u.Prompts += n
		case "clipboard":
			u.Copies += n
		}
		out[agent] = u
	}
	return out, rows.Err()
}

// workOnItTakenInCapture reports typing in the agent within the window after
// the task was sent: the person picked it up there.
func (a *App) workOnItTakenInCapture(r workonit.Record) bool {
	if a.activity == nil || a.activity.store == nil {
		return false
	}
	counted, err := a.activity.store.workOnItAgentCounts(context.Background(), r.SentAt, r.SentAt+workOnItTakenWindow.Milliseconds())
	if err != nil {
		return false
	}
	return counted[r.AgentID].Prompts > 0
}

// ── Records (counts only) ──

func workOnItRecordsPath() (string, error) {
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "work-on-it", workOnItRecordsFile), nil
}

func (a *App) workOnItRead() ([]workonit.Record, error) {
	s := a.workOnIt()
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	return workOnItReadLocked()
}

func workOnItReadLocked() ([]workonit.Record, error) {
	path, err := workOnItRecordsPath()
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
	var records []workonit.Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("work on it records: %w", err)
	}
	return records, nil
}

func (a *App) workOnItRewrite(change func([]workonit.Record) []workonit.Record) error {
	s := a.workOnIt()
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	records, err := workOnItReadLocked()
	if err != nil {
		return err
	}
	records = change(records)
	cutoff := s.clock().Add(-workOnItRecordsKeepFor).UnixMilli()
	kept := records[:0]
	for _, r := range records {
		if r.SentAt >= cutoff {
			kept = append(kept, r)
		}
	}
	path, err := workOnItRecordsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	return writeKnowHowAtomic(path, data)
}

func (a *App) workOnItAppend(record workonit.Record) error {
	return a.workOnItRewrite(func(records []workonit.Record) []workonit.Record {
		return append(records, record)
	})
}

func (a *App) workOnItUpdate(id string, change func(*workonit.Record)) error {
	found := false
	err := a.workOnItRewrite(func(records []workonit.Record) []workonit.Record {
		for i := range records {
			if records[i].ID == id {
				change(&records[i])
				found = true
			}
		}
		return records
	})
	if err == nil && !found {
		return errWorkOnItNoRecord
	}
	return err
}

func workOnItID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// workOnItWords are the fixed lines of a prepared task.
func workOnItWords(lang string) workonit.Words {
	if strings.HasPrefix(strings.ToLower(lang), "zh") {
		return workonit.Words{DoneLabel: "完成的样子：", Closing: "做完后，请告诉我你做了什么。"}
	}
	return workonit.Words{DoneLabel: "Done looks like:", Closing: "When you're done, tell me what you did."}
}

// startPasteIntoAgent runs the paste in the background, shown in Tasks.
// When it fails, the agent is opened as with open-and-copy: the task is
// already on the clipboard for the person to paste.
func (a *App) startPasteIntoAgent(agent workonit.Agent) {
	s := a.workOnIt()
	deps := s.pasteDeps()
	installed := s.agentPresent(agent)
	go func() {
		dir, err := os.MkdirTemp("", "isoform-paste-")
		if err == nil {
			defer os.RemoveAll(dir)
		} else {
			dir = os.TempDir()
		}
		run, ok := planPasteIntoAgent(agent, installed, deps.lookPath, dir)
		var runErr error
		if !ok {
			runErr = errors.New("no computer-use agent")
		} else {
			entry, shown := a.pasteIntoAgentTask(agent)
			ctx := a.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			runErr = a.runPasteIntoAgent(ctx, deps, run)
			if shown {
				a.finishPasteIntoAgentTask(entry, runErr)
			}
		}
		if runErr != nil {
			log.Printf("[work-on-it] paste into %s did not finish (%v); opening it to paste by hand", agent.ID, runErr)
			if err := s.openTarget(a, workOnItOpenTarget(agent, installed)); err != nil {
				log.Printf("[work-on-it] fallback open failed: %v", err)
			}
		}
		if s.pasteDone != nil {
			s.pasteDone(runErr)
		}
	}()
}

// sendWorkOnItTaskLink makes the task's link and sends the agent the one
// line that names it, by the agent's best carrier, and returns the carrier
// and the line. The task text stays behind the link. If the line can't be
// delivered the link is withdrawn.
func (a *App) sendWorkOnItTaskLink(s *workOnItState, task workonit.Task, agent workonit.Agent, handle, recordID string) (string, string, error) {
	carrier := agent.TaskLinkCarrier(s.agentPresent(agent), handle)
	if carrier == "" {
		return "", "", errWorkOnItNoMethod
	}
	link, err := a.createTaskLink(tasklink.Link{ID: recordID, AgentID: agent.ID, SourceKind: task.SourceKind, SourceID: task.SourceID, Text: task.Text})
	if err != nil {
		return "", "", err
	}
	prompt := tasklink.Prompt(link)
	withdraw := func() { a.withdrawTaskLinks(func(l tasklink.Link) bool { return l.ID == recordID }) }
	switch carrier {
	case workonit.MethodDeepLink:
		target, err := agent.DeepLinkURL(prompt)
		if err == nil {
			err = s.openTarget(a, target)
		}
		if err != nil {
			withdraw()
			return "", "", err
		}
		return carrier, prompt, nil
	case workonit.MethodMessage:
		ctx := a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := s.sendText(ctx, handle, prompt); err != nil {
			withdraw()
			return "", "", err
		}
		return carrier, prompt, nil
	default:
		if err := s.copyText(a, prompt); err != nil {
			withdraw()
			return "", "", err
		}
		if err := s.openTarget(a, workOnItOpenTarget(agent, s.agentPresent(agent))); err != nil {
			withdraw()
			return "", "", err
		}
		return carrier, prompt, nil
	}
}

// workOnItTaskLinkTaken marks the record taken on the agent's first read.
func (a *App) workOnItTaskLinkTaken(link tasklink.Link) {
	now := a.workOnIt().clock().UnixMilli()
	err := a.workOnItUpdate(link.ID, func(r *workonit.Record) {
		if r.Status == workonit.StatusSent {
			r.Status = workonit.StatusTaken
		}
		if r.TakenAt == 0 {
			r.TakenAt = now
		}
	})
	if err != nil && !errors.Is(err, errWorkOnItNoRecord) {
		log.Printf("[work-on-it] taken not saved: %v", err)
	}
}

// workOnItTaskLinkResulted records the agent's answer: done, or not done
// for blocked and declined (the detail stays with the link's result).
func (a *App) workOnItTaskLinkResulted(link tasklink.Link, result tasklink.Result) {
	now := a.workOnIt().clock().UnixMilli()
	err := a.workOnItUpdate(link.ID, func(r *workonit.Record) {
		if r.TakenAt == 0 {
			r.TakenAt = now
		}
		if result.Status == tasklink.StatusDone {
			r.Status, r.DoneAt = workonit.StatusDone, now
		} else {
			r.Status = workonit.StatusNotDone
		}
	})
	if err != nil && !errors.Is(err, errWorkOnItNoRecord) {
		log.Printf("[work-on-it] result not saved: %v", err)
	}
}
