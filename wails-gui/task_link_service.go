package main

// Task links in the app (design: docs/design/task-link-handoff.md). The
// internal/tasklink package serves /t/<token>; this file keeps its links
// across restarts, withdraws them when the person withdraws anything or
// turns on Closed Eyes, serves them on a loopback listener behind the
// task_links_enabled switch, and records each agent's result.
//
// The listener only binds loopback. Reaching it from outside is the front's
// job (docs/design/mcp-public-front.md); until the front ships, a dogfood
// bridge on the person's own account points at it and
// ISOFORM_TASK_LINK_BASE_URL names the bridge's public address.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/tasklink"
)

const (
	taskLinksEnabledKey    = "task_links_enabled"
	taskLinksDirName       = "task-links"
	taskLinksOpenFile      = "open.json"
	taskLinksResultsFile   = "results.json"
	taskLinkDefaultAddr    = "127.0.0.1:8792"
	taskLinkResultsKeepFor = 90 * 24 * time.Hour
	// taskLinkAddrEnv moves the loopback listener; taskLinkBaseURLEnv names
	// the public address that reaches it (a dogfood bridge, later the front).
	taskLinkAddrEnv    = "ISOFORM_TASK_LINK_ADDR"
	taskLinkBaseURLEnv = "ISOFORM_TASK_LINK_BASE_URL"
)

var (
	errTaskLinksOff        = errors.New("task links are off")
	errTaskLinksClosedEyes = errors.New("closed eyes is on")
	errTaskLinkNotLoopback = errors.New("the task link listener must bind a loopback address")
)

// TaskLinkResult is one agent's answer as kept on the computer. The task
// text is not kept here: only which task, which agent, and what came back.
type TaskLinkResult struct {
	LinkID     string   `json:"linkId"`
	AgentID    string   `json:"agentId"`
	SourceKind string   `json:"sourceKind,omitempty"`
	SourceID   string   `json:"sourceId,omitempty"`
	Status     string   `json:"status"`
	Summary    string   `json:"summary"`
	Links      []string `json:"links,omitempty"`
	TakenAt    int64    `json:"takenAt,omitempty"`
	ResultAt   int64    `json:"resultAt"`
}

// taskLinkState is the app's task-link service. Zero value is ready; the
// store is made on first use.
type taskLinkState struct {
	once  sync.Once
	store *tasklink.Store
	// fileMu guards the two files; listenMu the listener.
	fileMu      sync.Mutex
	listenMu    sync.Mutex
	server      *http.Server
	listenAddr  string
	unsubscribe func()
	// hooks for Work on it (taken and done) and tests.
	onTaken  func(tasklink.Link)
	onResult func(tasklink.Link, tasklink.Result)
	// test seams
	dir     string
	enabled func() bool
	now     func() time.Time
}

func (s *taskLinkState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *taskLinkState) dirPath() (string, error) {
	if s.dir != "" {
		return s.dir, nil
	}
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, taskLinksDirName), nil
}

func (a *App) taskLinksEnabled() bool {
	if a.taskLinks.enabled != nil {
		return a.taskLinks.enabled()
	}
	if a.store == nil {
		return false
	}
	value, err := a.store.GetUIState(context.Background(), taskLinksEnabledKey)
	return err == nil && strings.EqualFold(strings.TrimSpace(value), "true")
}

// taskLinkStore makes the store once: restored from disk, and subscribed to
// privacy withdrawals so a withdrawal spends every open link.
func (a *App) taskLinkStore() *tasklink.Store {
	s := &a.taskLinks
	s.once.Do(func() {
		s.store = tasklink.NewStore(tasklink.Hooks{
			Allowed:  a.taskLinkAllowed,
			Taken:    a.taskLinkTaken,
			Resulted: a.taskLinkResulted,
			Now:      s.clock,
		})
		if links, err := a.readTaskLinks(); err != nil {
			log.Printf("[task-links] open links not restored: %v", err)
		} else {
			s.store.Restore(links)
		}
		s.unsubscribe = subscribePrivacyWithdrawals(a.taskLinkPrivacyWithdrawal)
	})
	return s.store
}

// taskLinkAllowed runs on every request: a link answers only while the
// switch is on and Closed Eyes is off.
func (a *App) taskLinkAllowed(tasklink.Link) error {
	if !a.taskLinksEnabled() {
		return errTaskLinksOff
	}
	if a.IsClosedEyesModeEnabled() {
		return errTaskLinksClosedEyes
	}
	return nil
}

// taskLinkPrivacyWithdrawal spends every open link when the person
// withdraws anything (a memory, a person, a source, an app, a time range,
// Closed Eyes). The prepared text keeps no lineage, so any withdrawal could
// touch it; sending again is one tap. A named source also drops the results
// that came from it. Retention cleanup is not a withdrawal.
func (a *App) taskLinkPrivacyWithdrawal(w privacyWithdrawal) {
	if w.Retention {
		return
	}
	if n := a.withdrawTaskLinks(nil); n > 0 {
		log.Printf("[task-links] withdrew %d open link(s) after a privacy withdrawal", n)
	}
	if w.Scope.Kind == privacyWithdrawalSource && w.Scope.ID != "" {
		kind, id := w.Scope.SourceKind, w.Scope.ID
		if err := a.rewriteTaskLinkResults(func(results []TaskLinkResult) []TaskLinkResult {
			kept := results[:0]
			for _, r := range results {
				if !(r.SourceID == id && (kind == "" || r.SourceKind == kind)) {
					kept = append(kept, r)
				}
			}
			return kept
		}); err != nil {
			log.Printf("[task-links] results not pruned after a withdrawal: %v", err)
		}
	}
}

// withdrawTaskLinks spends the open links that match (all when match is
// nil) and saves what is left.
func (a *App) withdrawTaskLinks(match func(tasklink.Link) bool) int {
	store := a.taskLinkStore()
	n := store.Withdraw(match)
	if n > 0 {
		a.saveTaskLinks()
	}
	return n
}

func (a *App) taskLinkTaken(link tasklink.Link) {
	a.saveTaskLinks()
	a.workOnItTaskLinkTaken(link)
	if hook := a.taskLinks.onTaken; hook != nil {
		hook(link)
	}
}

func (a *App) taskLinkResulted(link tasklink.Link, result tasklink.Result) {
	a.saveTaskLinks()
	record := TaskLinkResult{
		LinkID: link.ID, AgentID: link.AgentID, SourceKind: link.SourceKind, SourceID: link.SourceID,
		Status: result.Status, Summary: result.Summary, Links: result.Links,
		TakenAt: link.TakenAt, ResultAt: a.taskLinks.clock().UnixMilli(),
	}
	if err := a.rewriteTaskLinkResults(func(results []TaskLinkResult) []TaskLinkResult {
		return append(results, record)
	}); err != nil {
		log.Printf("[task-links] result not saved: %v", err)
	}
	a.workOnItTaskLinkResulted(link, result)
	if hook := a.taskLinks.onResult; hook != nil {
		hook(link, result)
	}
}

// createTaskLink makes a link for one prepared task and returns its URL.
// It is called only from the person's tap.
func (a *App) createTaskLink(spec tasklink.Link) (string, error) {
	if !a.taskLinksEnabled() {
		return "", errTaskLinksOff
	}
	if a.IsClosedEyesModeEnabled() {
		return "", errTaskLinksClosedEyes
	}
	if err := a.startTaskLinkListener(); err != nil {
		return "", err
	}
	token, err := a.taskLinkStore().CreateFor(spec)
	if err != nil {
		return "", err
	}
	a.saveTaskLinks()
	base, err := a.taskLinkBaseURL()
	if err != nil {
		return "", err
	}
	return base + tasklink.PathPrefix + token, nil
}

// taskLinkBaseURL is the bridge's public address when one is set, else the
// loopback listener (reachable only from this computer).
func (a *App) taskLinkBaseURL() (string, error) {
	if raw := strings.TrimSpace(os.Getenv(taskLinkBaseURLEnv)); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return "", fmt.Errorf("%s must be an https origin", taskLinkBaseURLEnv)
		}
		return "https://" + u.Host, nil
	}
	// The front reaches the computer's own TLS server by its hostname.
	if host := a.frontHostname(); host != "" {
		return "https://" + host, nil
	}
	a.taskLinks.listenMu.Lock()
	addr := a.taskLinks.listenAddr
	a.taskLinks.listenMu.Unlock()
	if addr == "" {
		addr = taskLinkListenAddr()
	}
	return "http://" + addr, nil
}

func taskLinkListenAddr() string {
	if addr := strings.TrimSpace(os.Getenv(taskLinkAddrEnv)); addr != "" {
		return addr
	}
	return taskLinkDefaultAddr
}

func loopbackListenAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errTaskLinkNotLoopback
	}
	return nil
}

// startTaskLinkListener serves /t/ on loopback. It is idempotent.
func (a *App) startTaskLinkListener() error {
	s := &a.taskLinks
	s.listenMu.Lock()
	defer s.listenMu.Unlock()
	if s.server != nil {
		return nil
	}
	addr := taskLinkListenAddr()
	if err := loopbackListenAddr(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("task link listener: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle(tasklink.PathPrefix, a.taskLinkStore())
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	s.server, s.listenAddr = server, ln.Addr().String()
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[task-links] listener stopped: %v", err)
		}
	}()
	log.Printf("[task-links] serving on %s", s.listenAddr)
	return nil
}

func (a *App) stopTaskLinkListener() {
	s := &a.taskLinks
	s.listenMu.Lock()
	server := s.server
	s.server, s.listenAddr = nil, ""
	s.listenMu.Unlock()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

// startTaskLinksIfEnabled runs at launch: open links answer again only when
// the switch is on.
func (a *App) startTaskLinksIfEnabled() {
	if !a.taskLinksEnabled() {
		return
	}
	a.taskLinkStore()
	if len(a.taskLinkStore().Links()) == 0 {
		return
	}
	if err := a.startTaskLinkListener(); err != nil {
		log.Printf("[task-links] %v", err)
	}
}

// GetTaskLinksEnabled reports the switch.
func (a *App) GetTaskLinksEnabled() bool { return a.taskLinksEnabled() }

// SetTaskLinksEnabled turns task links on or off. Off spends every open
// link and stops the listener.
func (a *App) SetTaskLinksEnabled(enabled bool) error {
	if a.store == nil {
		return errors.New("storage is not ready")
	}
	if err := a.store.SetUIState(context.Background(), taskLinksEnabledKey, fmt.Sprintf("%t", enabled)); err != nil {
		return err
	}
	if !enabled {
		a.withdrawTaskLinks(nil)
		a.stopTaskLinkListener()
	}
	return nil
}

// GetTaskLinkResults lists the agents' answers, newest first.
func (a *App) GetTaskLinkResults() ([]TaskLinkResult, error) {
	a.taskLinks.fileMu.Lock()
	defer a.taskLinks.fileMu.Unlock()
	results, err := a.readTaskLinkResultsLocked()
	if err != nil {
		return nil, err
	}
	out := make([]TaskLinkResult, 0, len(results))
	for i := len(results) - 1; i >= 0; i-- {
		out = append(out, results[i])
	}
	return out, nil
}

func (a *App) shutdownTaskLinks() {
	a.stopTaskLinkListener()
	if unsubscribe := a.taskLinks.unsubscribe; unsubscribe != nil {
		unsubscribe()
	}
}

// ── files ──────────────────────────────────────────────────────────────
// Both files are private to the person (0600 in a 0700 folder). The open
// file keeps token hashes, never tokens.

func (a *App) taskLinkFile(name string) (string, error) {
	dir, err := a.taskLinks.dirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func (a *App) readTaskLinks() ([]tasklink.Link, error) {
	a.taskLinks.fileMu.Lock()
	defer a.taskLinks.fileMu.Unlock()
	var links []tasklink.Link
	err := a.readTaskLinkJSON(taskLinksOpenFile, &links)
	return links, err
}

func (a *App) saveTaskLinks() {
	links := a.taskLinkStore().Links()
	a.taskLinks.fileMu.Lock()
	defer a.taskLinks.fileMu.Unlock()
	if err := a.writeTaskLinkJSON(taskLinksOpenFile, links); err != nil {
		log.Printf("[task-links] open links not saved: %v", err)
	}
}

func (a *App) readTaskLinkResultsLocked() ([]TaskLinkResult, error) {
	var results []TaskLinkResult
	err := a.readTaskLinkJSON(taskLinksResultsFile, &results)
	return results, err
}

func (a *App) rewriteTaskLinkResults(change func([]TaskLinkResult) []TaskLinkResult) error {
	a.taskLinks.fileMu.Lock()
	defer a.taskLinks.fileMu.Unlock()
	results, err := a.readTaskLinkResultsLocked()
	if err != nil {
		return err
	}
	results = change(results)
	cutoff := a.taskLinks.clock().Add(-taskLinkResultsKeepFor).UnixMilli()
	kept := results[:0]
	for _, r := range results {
		if r.ResultAt >= cutoff {
			kept = append(kept, r)
		}
	}
	return a.writeTaskLinkJSON(taskLinksResultsFile, kept)
}

func (a *App) readTaskLinkJSON(name string, into any) error {
	path, err := a.taskLinkFile(name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, into)
}

func (a *App) writeTaskLinkJSON(name string, value any) error {
	path, err := a.taskLinkFile(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// taskLinkHandoffReady says whether Work on it may offer a task link: the
// switch is on and a public https origin reaches the listener (a loopback
// link is useless to an agent in the cloud).
func (a *App) taskLinkHandoffReady() bool {
	if !a.taskLinksEnabled() || strings.TrimSpace(os.Getenv(taskLinkBaseURLEnv)) == "" {
		return false
	}
	_, err := a.taskLinkBaseURL()
	return err == nil
}
