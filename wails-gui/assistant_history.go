package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// assistant_history.go is the first-day reading of this computer's Claude Code
// and Codex sessions (docs/handoff-2026-10-28, sections 2 and 7). Pass 1 is
// local and fast; pass 2 asks the person's own AI once per project. The view
// answers from a small cache on disk, so Today and Chat open on what is
// already known. Reads and writes are on by default and can be turned off in
// Settings › Privacy; turning them off forgets everything that came from them.

const (
	assistantHistoryEvent       = "assistant-history:updated"
	assistantHistoryStaleAfter  = 15 * time.Minute
	assistantHistoryLoopEvery   = cadenceAssistantHistory
	assistantHistoryPass2Top    = 5
	assistantHistoryCacheFormat = 1
	assistantHistoryShownCap    = 10
	// assistantHistoryWelcomeWait bounds how long the day-0 welcome waits for a
	// running pass 1 before it keeps its usual text.
	assistantHistoryWelcomeWait = 3 * time.Second
)

// AssistantHistoryView is what Today, Chat, Settings and the setup screen read.
type AssistantHistoryView struct {
	Enabled      bool             `json:"enabled"`
	State        string           `json:"state"` // "off" | "none" (nothing found) | "reading" | "ready"
	Assistants   []string         `json:"assistants"`
	Projects     []HistoryProject `json:"projects"`
	BeliefCount  int              `json:"beliefCount"`
	KnowHowCount int              `json:"knowHowCount"`
	ScannedAt    int64            `json:"scannedAt"`
	Pass2Done    bool             `json:"pass2Done"`
}

// HistoryProject is one repository or folder with recent sessions.
type HistoryProject struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Path             string   `json:"path"`
	Assistants       []string `json:"assistants"`
	SessionsThisWeek int      `json:"sessionsThisWeek"`
	Sessions14d      int      `json:"sessions14d"`
	LastAt           int64    `json:"lastAt"`
	Status           string   `json:"status"`
	OpenProblem      string   `json:"openProblem"`
	OpenProblemAt    int64    `json:"openProblemAt"`
	OpenQuestion     string   `json:"openQuestion"`
	OpenChip         string   `json:"openChip"`
	// AgentSessions14d counts sessions agents ran here (side sessions,
	// scheduled prompts, Codex exec workers). It is labelled context only;
	// nothing is learned from them.
	AgentSessions14d int `json:"agentSessions14d"`
	human            bool
}

// assistantHistoryState guards the in-memory copy of the cache and the single
// scan that may run at a time. writeMu serializes every write a scan makes to
// memories and know-how; a forget takes it too, so nothing lands after it.
type assistantHistoryState struct {
	mu         sync.Mutex
	loaded     bool
	cache      assistantHistoryCache
	running    bool
	cancel     context.CancelFunc
	generation uint64
	writeMu    sync.Mutex
	loopOnce   sync.Once
}

// assistantHistoryCache is the persisted view. Pass 2 fields live on each
// project and survive a pass 1 rescan of the same project.
type assistantHistoryCache struct {
	Version      int              `json:"version"`
	ScannedAt    int64            `json:"scannedAt"`
	Assistants   []string         `json:"assistants"`
	Projects     []HistoryProject `json:"projects"`
	BeliefCount  int              `json:"beliefCount"`
	KnowHowCount int              `json:"knowHowCount"`
	Pass2Done    bool             `json:"pass2Done"`
	// Pass2Marks is the newest session time (unix ms) each project's pass 2
	// has already read. A project with newer sessions is read again.
	Pass2Marks map[string]int64 `json:"pass2Marks"`
}

// assistantHistoryUnderTestWithoutHome is true inside a go test binary that has
// not chosen its own data folder. Such a test must not read or scan the
// developer's own sessions, memories or cache.
func assistantHistoryUnderTestWithoutHome() bool {
	return testing.Testing() && strings.TrimSpace(os.Getenv(agentHomeEnv)) == ""
}

// readAssistantHistoryOn is the opt-out switch: absent means on.
func readAssistantHistoryOn() bool {
	return !loadConfig().AssistantHistoryOff
}

// GetReadAssistantHistory reports the Settings › Privacy switch.
func (a *App) GetReadAssistantHistory() (bool, error) {
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := loadConfigChecked()
	if err != nil {
		return false, err
	}
	return !cfg.AssistantHistoryOff, nil
}

// SetReadAssistantHistory turns reading on or off. Off cancels a running scan,
// deletes the cache and forgets every memory and know-how item that came from
// history; the frontend's Undo delays this call. On starts a scan.
func (a *App) SetReadAssistantHistory(on bool) error {
	if err := updateConfig(func(c *yansuConfig) {
		c.AssistantHistoryOff = !on
	}); err != nil {
		return err
	}
	if on {
		a.startAssistantHistoryScan(true)
		a.emitAssistantHistoryUpdated()
		return nil
	}
	return a.forgetAssistantHistory()
}

// GetAssistantHistory answers from the cache on disk; it never scans.
func (a *App) GetAssistantHistory() AssistantHistoryView {
	if a == nil {
		return assistantHistoryEmptyView(false)
	}
	if !readAssistantHistoryOn() {
		return assistantHistoryEmptyView(false)
	}
	if assistantHistoryUnderTestWithoutHome() {
		return assistantHistoryEmptyView(true)
	}
	st := &a.assistantHistory
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loadLocked()
	return a.assistantHistoryViewLocked()
}

// RefreshAssistantHistory starts a scan in the background when reading is on.
// It is a no-op while one runs.
func (a *App) RefreshAssistantHistory() error {
	a.startAssistantHistoryScan(true)
	return nil
}

func assistantHistoryEmptyView(enabled bool) AssistantHistoryView {
	state := "off"
	if enabled {
		state = "none"
	}
	return AssistantHistoryView{
		Enabled:    enabled,
		State:      state,
		Assistants: []string{},
		Projects:   []HistoryProject{},
	}
}

// assistantHistoryViewLocked builds the view from the cache. Caller holds mu.
func (a *App) assistantHistoryViewLocked() AssistantHistoryView {
	st := &a.assistantHistory
	cache := st.cache
	view := AssistantHistoryView{
		Enabled:      true,
		Assistants:   append([]string{}, cache.Assistants...),
		Projects:     []HistoryProject{},
		BeliefCount:  cache.BeliefCount,
		KnowHowCount: cache.KnowHowCount,
		ScannedAt:    cache.ScannedAt,
		Pass2Done:    cache.Pass2Done,
	}
	for i, project := range cache.Projects {
		if i == assistantHistoryShownCap {
			break
		}
		project.Assistants = append([]string{}, project.Assistants...)
		view.Projects = append(view.Projects, project)
	}
	switch {
	case st.running:
		view.State = "reading"
	case len(view.Projects) == 0:
		view.State = "none"
	default:
		view.State = "ready"
	}
	return view
}

// loadLocked reads the cache file once. A missing or unreadable file is an
// empty cache, which means the next scan starts from nothing.
func (s *assistantHistoryState) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	path := assistantHistoryCachePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cache assistantHistoryCache
	if err := json.Unmarshal(data, &cache); err != nil || cache.Version != assistantHistoryCacheFormat {
		return
	}
	if cache.Pass2Marks == nil {
		cache.Pass2Marks = map[string]int64{}
	}
	s.cache = cache
}

// saveLocked writes the cache atomically. Caller holds mu. A failed write only
// means the next launch rescans.
func (s *assistantHistoryState) saveLocked() {
	path := assistantHistoryCachePath()
	if path == "" {
		return
	}
	data, err := json.Marshal(s.cache)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Printf("[assistant-history] cache folder: %v", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("[assistant-history] cache write: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[assistant-history] cache rename: %v", err)
	}
}

func assistantHistoryCachePath() string {
	root := configDirOrFallback()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "assistant-history", "view.json")
}

// startAssistantHistoryScan starts pass 1 (and pass 2) unless one is running,
// reading is off, or observation is paused. With force false it also needs
// the cache to be older than the 15-minute window.
func (a *App) startAssistantHistoryScan(force bool) bool {
	if a == nil || !readAssistantHistoryOn() || a.IsClosedEyesModeEnabled() {
		return false
	}
	if assistantHistoryUnderTestWithoutHome() {
		return false
	}
	st := &a.assistantHistory
	st.mu.Lock()
	if st.running {
		st.mu.Unlock()
		return false
	}
	st.loadLocked()
	if !force && st.cache.ScannedAt > 0 && time.Since(time.UnixMilli(st.cache.ScannedAt)) < assistantHistoryStaleAfter {
		st.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	st.running = true
	st.cancel = cancel
	gen := st.generation
	st.mu.Unlock()
	a.emitAssistantHistoryUpdated()
	go a.runAssistantHistoryScan(ctx, gen)
	return true
}

// scheduleAssistantHistory is the launch and periodic check. It scans only
// when setup has finished and the cache is stale, and it starts the 5-minute
// loop once.
func (a *App) scheduleAssistantHistory() {
	if a == nil {
		return
	}
	a.assistantHistory.loopOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(assistantHistoryLoopEvery)
			defer ticker.Stop()
			for range ticker.C {
				a.scheduleAssistantHistory()
			}
		}()
	})
	if a.store == nil || !readAssistantHistoryOn() || !a.onboardingFinished(context.Background()) {
		return
	}
	a.startAssistantHistoryScan(false)
}

// runAssistantHistoryScan is the background worker: pass 1, then pass 2 for the
// top projects with new sessions.
func (a *App) runAssistantHistoryScan(ctx context.Context, gen uint64) {
	defer a.beginPresence(mobilePresenceReadingHistory(mobileTaskRunner(loadAIProviderConfig().Provider)))()
	st := &a.assistantHistory
	completed := false
	defer func() {
		st.mu.Lock()
		st.running = false
		st.cancel = nil
		st.mu.Unlock()
		a.emitAssistantHistoryUpdated()
		if completed {
			a.portraitService().poke()
		}
	}()

	roots, err := assistantHistoryRootsFromEnv()
	if err != nil {
		log.Printf("[assistant-history] folders unavailable: %v", err)
		return
	}
	scan := scanAssistantHistory(roots, time.Now())
	if !a.assistantHistoryCurrent(ctx, gen) {
		return
	}
	st.writeMu.Lock()
	if !a.assistantHistoryCurrent(ctx, gen) {
		st.writeMu.Unlock()
		return
	}
	st.mu.Lock()
	st.cache = mergeAssistantHistoryPass1(st.cache, scan)
	st.saveLocked()
	st.mu.Unlock()
	st.writeMu.Unlock()
	a.emitAssistantHistoryUpdated()

	if a.IsClosedEyesModeEnabled() {
		return
	}
	a.runAssistantHistoryPass2(ctx, gen, scan)
	completed = a.assistantHistoryCurrent(ctx, gen)
}

// assistantHistoryCurrent reports whether the scan may still write: it is not
// cancelled, and no off switch has happened since it started.
func (a *App) assistantHistoryCurrent(ctx context.Context, gen uint64) bool {
	if ctx.Err() != nil {
		return false
	}
	st := &a.assistantHistory
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.generation == gen
}

// mergeAssistantHistoryPass1 replaces the pass 1 fields with a fresh scan and
// keeps the pass 2 fields of projects that are still there.
func mergeAssistantHistoryPass1(old assistantHistoryCache, scan assistantHistoryScan) assistantHistoryCache {
	previous := map[string]HistoryProject{}
	for _, project := range old.Projects {
		previous[project.ID] = project
	}
	next := old
	next.Version = assistantHistoryCacheFormat
	next.ScannedAt = scan.ScannedAt.UnixMilli()
	next.Assistants = append([]string{}, scan.Assistants...)
	next.Projects = make([]HistoryProject, 0, len(scan.Projects))
	for _, project := range scan.Projects {
		if before, ok := previous[project.ID]; ok {
			project.Status = before.Status
			project.OpenProblem = before.OpenProblem
			project.OpenProblemAt = before.OpenProblemAt
			project.OpenQuestion = before.OpenQuestion
			project.OpenChip = before.OpenChip
		}
		next.Projects = append(next.Projects, project)
	}
	if next.Pass2Marks == nil {
		next.Pass2Marks = map[string]int64{}
	}
	return next
}

// runAssistantHistoryPass2 reads the top projects that have new sessions, one
// model call each, and stores what it finds.
func (a *App) runAssistantHistoryPass2(ctx context.Context, gen uint64, scan assistantHistoryScan) {
	st := &a.assistantHistory
	st.mu.Lock()
	marks := map[string]int64{}
	for id, at := range st.cache.Pass2Marks {
		marks[id] = at
	}
	st.mu.Unlock()

	targets := make([]HistoryProject, 0, assistantHistoryPass2Top)
	for _, project := range scan.Projects {
		if len(targets) == assistantHistoryPass2Top {
			break
		}
		if marks[project.ID] >= project.LastAt && project.LastAt > 0 {
			continue
		}
		targets = append(targets, project)
	}
	languageName := uiLanguageDisplayName(a.currentUILanguage())
	for _, project := range targets {
		if !a.assistantHistoryCurrent(ctx, gen) {
			return
		}
		update, called, err := a.runAssistantHistoryProject(ctx, project, scan.Sessions[project.ID], languageName)
		if !a.assistantHistoryCurrent(ctx, gen) {
			return
		}
		if err != nil {
			if a.IsClosedEyesModeEnabled() || ctx.Err() != nil {
				// Paused or cancelled: try again on a later scan.
				return
			}
			log.Printf("[assistant-history] project not read: %v", err)
		}
		// Only an answer from the model changes the project's text; a project
		// with no readable session keeps what it had.
		answered := called && err == nil
		if err := a.storeAssistantHistoryProject(ctx, gen, project, update, answered); err != nil {
			log.Printf("[assistant-history] project not stored: %v", err)
		}
	}
	if !a.assistantHistoryCurrent(ctx, gen) {
		return
	}
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	if !a.assistantHistoryCurrent(ctx, gen) {
		return
	}
	beliefs, knowHow := a.assistantHistoryCounts()
	st.mu.Lock()
	st.cache.BeliefCount = beliefs
	st.cache.KnowHowCount = knowHow
	st.cache.Pass2Done = true
	st.saveLocked()
	st.mu.Unlock()
	a.emitAssistantHistoryUpdated()
}

// storeAssistantHistoryProject writes one project's pass 2 result. A failed
// model call still marks the project read, so the same sessions are not sent
// again on every scan; new sessions bring it back.
func (a *App) storeAssistantHistoryProject(ctx context.Context, gen uint64, project HistoryProject, update assistantHistoryProjectUpdate, ok bool) error {
	st := &a.assistantHistory
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	if !a.assistantHistoryCurrent(ctx, gen) {
		return nil
	}
	now := time.Now()
	var errs []error
	if ok {
		if _, err := a.storeAssistantHistoryBeliefs(project, update.Beliefs, now); err != nil {
			errs = append(errs, err)
		}
		if _, err := a.storeAssistantHistoryKnowHow(project, update.KnowHow, now); err != nil {
			errs = append(errs, err)
		}
	}
	st.mu.Lock()
	for i := range st.cache.Projects {
		if st.cache.Projects[i].ID != project.ID {
			continue
		}
		if ok {
			st.cache.Projects[i].Status = update.Status
			st.cache.Projects[i].OpenProblem = update.OpenProblem
			st.cache.Projects[i].OpenProblemAt = update.OpenProblemAt
			st.cache.Projects[i].OpenQuestion = update.OpenQuestion
			st.cache.Projects[i].OpenChip = update.OpenChip
		}
	}
	st.cache.Pass2Marks[project.ID] = project.LastAt
	st.saveLocked()
	st.mu.Unlock()
	a.emitAssistantHistoryUpdated()
	return errors.Join(errs...)
}

// forgetAssistantHistory runs after reading is switched off. It stops any
// scan, takes the write lock so none lands afterwards, removes every memory
// and know-how item with a history source, and clears the cache.
func (a *App) forgetAssistantHistory() error {
	st := &a.assistantHistory
	st.mu.Lock()
	st.generation++
	if st.cancel != nil {
		st.cancel()
	}
	st.mu.Unlock()

	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	err := a.forgetAssistantHistoryRecords()

	st.mu.Lock()
	st.cache = assistantHistoryCache{Version: assistantHistoryCacheFormat, Pass2Marks: map[string]int64{}}
	st.loaded = true
	if path := assistantHistoryCachePath(); path != "" {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}
	st.mu.Unlock()
	a.emitAssistantHistoryUpdated()
	return err
}

// forgetAssistantHistoryRecords uses the same preview and forget path as the
// Forget button (preview, then confirm with its token), so derived records and
// version history go with each memory.
func (a *App) forgetAssistantHistoryRecords() error {
	var errs []error
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	records, err := loadMemoryForgetRecords(dir)
	if err != nil {
		return fmt.Errorf("list memory history: %w", err)
	}
	historyIDs := map[string]bool{}
	for id, record := range records {
		for _, content := range record.contents {
			if parseMemoryFrontmatter(content).Source == assistantHistorySource {
				historyIDs[id] = true
				break
			}
		}
	}
	// Capture the full descendant set before deleting any source. Know-how
	// can cite a synthesis as well as an original history memory.
	removedIDs := map[string]bool{}
	unresolved := map[string]bool{}
	for id := range historyIDs {
		record := records[id]
		versionsOnly := record.livePath != "" && isUserEditedMemory(record.liveContent)
		derived, err := collectMemoryForgetRecordsScoped(dir, id, versionsOnly)
		if err != nil {
			unresolved[id] = true
			errs = append(errs, fmt.Errorf("trace history memory %s: %w", id, err))
			continue
		}
		for _, record := range derived {
			if !record.preserveLive {
				removedIDs[record.id] = true
			} else {
				for _, content := range record.contents {
					removedIDs[memoryForgetHistoricalSelector(record.id, content)] = true
				}
			}
		}
	}
	for id := range historyIDs {
		if unresolved[id] {
			continue
		}
		record := records[id]
		versionsOnly := record.livePath != "" && isUserEditedMemory(record.liveContent)
		preview, err := a.getMemoryForgetPreviewScoped(id, versionsOnly)
		if os.IsNotExist(err) {
			continue // A previous cascade removed it.
		}
		if err == nil {
			plan, planErr := loadMemoryForgetPreparedPlan(id, preview.ConfirmationToken)
			if planErr != nil {
				err = planErr
			} else {
				err = a.ForgetMemoryEntry(id, preview.ConfirmationToken)
				if err == nil {
					for _, sourceID := range plan.scopeIDs {
						removedIDs[sourceID] = true
					}
				}
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("forget memory %s: %w", id, err))
		}
	}
	knowledge, err := a.GetKnowledgeEntries()
	if err != nil {
		errs = append(errs, fmt.Errorf("list know-how: %w", err))
	}
	// A know-how item may itself be the source of a later item. Trace those
	// links before deleting, so the result does not depend on listing order.
	removedKnowledge := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, entry := range knowledge {
			if removedKnowledge[entry.ID] {
				continue
			}
			doc, ok := parseOKFDocument(entry.Content)
			if !ok {
				continue
			}
			source := okfString(doc.Frontmatter, "source")
			derived := memoryContentCitesRemoved(entry.Content, removedIDs)
			sourceIDs := okfStringSlice(doc.Frontmatter, "source_ids")
			for _, id := range sourceIDs {
				derived = derived || removedIDs[id] || memoryForgetHistoricalRefMatches(id, sourceIDs, removedIDs)
			}
			if source == assistantHistorySource || source == assistantHistoryClaudeMd || derived {
				removedKnowledge[entry.ID], removedIDs[entry.ID], changed = true, true, true
			}
		}
	}
	for id := range removedKnowledge {
		if err := a.DeleteKnowledgeEntry(id); err != nil {
			errs = append(errs, fmt.Errorf("forget know-how %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// assistantHistoryAssistantsWithin gives the first-day welcome the assistants
// history found, waiting at most d for a running pass 1 to finish. Nil means no
// history or not ready in time; the welcome then keeps its usual text.
func (a *App) assistantHistoryAssistantsWithin(d time.Duration) []string {
	if a == nil || !readAssistantHistoryOn() || assistantHistoryUnderTestWithoutHome() {
		return nil
	}
	st := &a.assistantHistory
	deadline := time.Now().Add(d)
	for {
		st.mu.Lock()
		st.loadLocked()
		if st.cache.ScannedAt > 0 {
			assistants := append([]string{}, st.cache.Assistants...)
			st.mu.Unlock()
			return assistants
		}
		running := st.running
		st.mu.Unlock()
		// Only wait for a scan that is already running: the welcome never
		// starts one, so a launch without history does not delay it.
		if !running || time.Now().After(deadline) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// emitAssistantHistoryUpdated tells the open pages to read the view again.
func (a *App) emitAssistantHistoryUpdated() {
	a.emitBrowserEvent(assistantHistoryEvent)
}

// assistantHistoryHistoryKey is the Params value the first-week welcome uses
// to name the source: "claude", "codex", "both", or "" for none.
func assistantHistoryHistoryKey(assistants []string) string {
	hasClaude := containsString(assistants, "claude")
	hasCodex := containsString(assistants, "codex")
	switch {
	case hasClaude && hasCodex:
		return "both"
	case hasClaude:
		return "claude"
	case hasCodex:
		return "codex"
	default:
		return ""
	}
}
