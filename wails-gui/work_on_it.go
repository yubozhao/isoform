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
// Everything is behind work_on_it_enabled, off by default.

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
}

func (a *App) workOnIt() *workOnItState {
	a.workOnItOnce.Do(func() {
		a.workOnItS = &workOnItState{prepared: map[string]workonit.Task{}}
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
	return err == nil && strings.EqualFold(strings.TrimSpace(value), "true")
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
	TaskID string              `json:"taskId"`
	Text   string              `json:"text"`
	Agents []WorkOnItAgentView `json:"agents"`
}

// WorkOnItSent says how the task left.
type WorkOnItSent struct {
	RecordID string `json:"recordId"`
	Method   string `json:"method"`
	// Copied is true when the person still has to paste the task.
	Copied bool `json:"copied"`
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
	task := workonit.Task{ID: workOnItID(), SourceKind: in.SourceKind, SourceID: in.SourceID, Text: text, PreparedAt: s.clock()}
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
	return WorkOnItPrepared{TaskID: task.ID, Text: text, Agents: agents}, nil
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
	for i, r := range ranked {
		out = append(out, WorkOnItAgentView{ID: r.Agent.ID, Name: r.Agent.Name, Methods: r.Methods, Default: i == 0, TermsNote: r.Agent.TermsNote})
	}
	// A text agent with no number is still offered, to set one.
	for _, agent := range workonit.Catalog() {
		if agent.Message && handles[agent.ID] == "" {
			out = append(out, WorkOnItAgentView{ID: agent.ID, Name: agent.Name, TermsNote: agent.TermsNote, NeedsHandle: true})
		}
	}
	return out, nil
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
		return WorkOnItSent{}, errWorkOnItNoAgent
	}
	handle := a.workOnItHandle(agent.ID)
	allowed := false
	for _, m := range agent.Methods(s.agentPresent(agent), handle) {
		if m == method {
			allowed = true
		}
	}
	if !allowed {
		return WorkOnItSent{}, errWorkOnItNoMethod
	}
	out := WorkOnItSent{Method: method}
	switch method {
	case workonit.MethodDeepLink:
		link, err := agent.DeepLinkURL(task.Text)
		if err != nil {
			return WorkOnItSent{}, err
		}
		if err := s.openTarget(a, link); err != nil {
			return WorkOnItSent{}, err
		}
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
	record := workonit.Record{ID: workOnItID(), SourceKind: task.SourceKind, AgentID: agent.ID, Method: method, SentAt: s.clock().UnixMilli(), Status: workonit.StatusSent}
	if err := a.workOnItAppend(record); err != nil {
		log.Printf("[work-on-it] record not saved: %v", err)
	}
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
