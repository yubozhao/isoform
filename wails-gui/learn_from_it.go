package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"wails-gui/internal/workonit"
)

// The catalog ID is the settings key. Transcripts and existing OKF records
// use the source name on the right.
var historySources = map[string]string{"claude-code": "claude", "codex": "codex"}

type learnFromItChoice struct {
	On        bool   `json:"on"`
	ChangedAt int64  `json:"changedAt"`
	ChangedBy string `json:"changedBy"`
}

func (c *learnFromItChoice) UnmarshalJSON(data []byte) error {
	var raw struct {
		On        *bool
		ChangedAt int64
		ChangedBy string
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.On == nil || raw.ChangedAt <= 0 || (raw.ChangedBy != "person" && raw.ChangedBy != "migration") {
		return errors.New("invalid Learn from it choice")
	}
	*c = learnFromItChoice{*raw.On, raw.ChangedAt, raw.ChangedBy}
	return nil
}

type learnFromItSettings struct {
	Migrated bool                         `json:"migrated"`
	Choices  map[string]learnFromItChoice `json:"choices"`
	// Retain an interrupted withdrawal until its Forget cascade succeeds.
	Forget        string   `json:"forget,omitempty"`
	ForgetSources []string `json:"forgetSources,omitempty"`
}

type LearnFromItView struct {
	AgentID   string `json:"agentId"`
	On        bool   `json:"on"`
	ChangedAt int64  `json:"changedAt"`
	Available bool   `json:"available"`
}

// Read settings independently of long transcript walks. A choice change
// excludes source readers while publishing and serializes its Forget cascade.
var consentChangeMu sync.Mutex
var historySourceMu sync.RWMutex

func learnFromItPath() (string, error) {
	dir, err := flavorDataDir()
	return filepath.Join(dir, "learn-from-it.json"), err
}

func saveLearnFromIt(s learnFromItSettings) error {
	path, err := learnFromItPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeKnowHowAtomic(path, data)
}

// Caller holds configMu. Migration inspects only Isoform's retained data.
func readLearnFromItLocked() (learnFromItSettings, error) {
	var s learnFromItSettings
	path, err := learnFromItPath()
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &s); err != nil {
			return s, err
		}
		if s.Choices == nil {
			return s, errors.New("missing Learn from it choices")
		}
		if s.Forget != "" && s.Forget != "all" && historySources[s.Forget] == "" {
			return s, errors.New("invalid pending agent withdrawal")
		}
		if s.Migrated {
			return s, nil
		}
	} else if !os.IsNotExist(err) {
		return s, err
	} else if _, lstatErr := os.Lstat(path); !os.IsNotExist(lstatErr) {
		// A dangling settings symlink is unreadable, not a fresh install.
		return s, err
	}
	if s.Choices == nil {
		s.Choices = map[string]learnFromItChoice{}
	}
	if len(s.Choices) == 0 {
		cfg, err := loadConfigChecked()
		if err != nil {
			return s, err
		}
		if !cfg.AssistantHistoryOff {
			sources, err := retainedHistorySources()
			if err != nil {
				return s, err
			}
			for id, name := range historySources {
				if sources[name] {
					s.Choices[id] = learnFromItChoice{true, time.Now().UnixMilli(), "migration"}
				}
			}
		}
	}
	s.Migrated = true
	return s, saveLearnFromIt(s)
}

func retainedHistorySources() (map[string]bool, error) {
	out := map[string]bool{}
	var cache assistantHistoryState
	cache.loadLocked()
	for _, name := range cache.cache.Assistants {
		out[name] = true
	}
	for _, project := range cache.cache.Projects {
		for _, name := range project.Assistants {
			out[name] = true
		}
	}
	add := func(content string) {
		doc, ok := parseOKFDocument(content)
		if !ok || okfString(doc.Frontmatter, "source") != assistantHistorySource {
			return
		}
		for _, name := range okfStringSlice(doc.Frontmatter, "historyAgents") {
			out[name] = true
		}
		out[okfString(doc.Frontmatter, "history_assistant")] = true
	}
	dir, err := memoryDir()
	if err != nil {
		return nil, err
	}
	records, err := loadMemoryForgetRecords(dir)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		for _, content := range record.contents {
			add(content)
		}
	}
	cards, err := knowledgeEntriesIncludingHidden()
	if err != nil {
		return nil, err
	}
	for _, card := range cards {
		add(card.Content)
	}
	return out, nil
}

func readLearnFromIt() (learnFromItSettings, error) {
	configMu.Lock()
	defer configMu.Unlock()
	return readLearnFromItLocked()
}

func learnFromItOn(agentID string) (bool, error) {
	s, err := readLearnFromIt()
	if err != nil {
		return false, err
	}
	return s.Choices[agentID].On, nil
}

func (s learnFromItSettings) allowedSources() map[string]bool {
	sources := map[string]bool{}
	if s.Forget == "" {
		for id, name := range historySources {
			if s.Choices[id].On {
				sources[name] = true
			}
		}
	}
	return sources
}

func (a *App) historyAvailable(id string, roots assistantHistoryRoots) bool {
	var root string
	switch historySources[id] {
	case "claude":
		root = roots.Claude
	case "codex":
		root = roots.Codex
	default:
		return false
	}
	// Inspect the installation directory, never enumerate an off source.
	if info, err := os.Stat(filepath.Dir(root)); err == nil && info.IsDir() {
		return true
	}
	agent, ok := workonit.AgentByID(id)
	return ok && a.workOnIt().agentFound(agent)
}

func (a *App) GetLearnFromIt() (map[string]LearnFromItView, error) {
	s, err := readLearnFromIt()
	if err != nil {
		return nil, err
	}
	roots, err := assistantHistoryRootsFromEnv()
	if err != nil {
		return nil, err
	}
	out := map[string]LearnFromItView{}
	for _, agent := range workonit.Catalog() {
		c := s.Choices[agent.ID]
		out[agent.ID] = LearnFromItView{agent.ID, c.On, c.ChangedAt, a.historyAvailable(agent.ID, roots)}
	}
	return out, nil
}

func (a *App) GetReadAssistantHistory() (bool, error) {
	s, err := readLearnFromIt()
	if err != nil {
		return false, err
	}
	for id := range historySources {
		if s.Choices[id].On {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) SetReadAssistantHistory(on bool) error { return a.changeLearnFromIt("all", on) }
func (a *App) SetLearnFromIt(agentID string, on bool) error {
	if historySources[agentID] == "" {
		return fmt.Errorf("unsupported history agent %q", agentID)
	}
	return a.changeLearnFromIt(agentID, on)
}

func (a *App) finishHistoryWithdrawal(s *learnFromItSettings) error {
	if s.Forget == "" {
		return nil
	}
	if err := a.forgetAgentHistory(historySources[s.Forget], s); err != nil {
		return err
	}
	s.Forget = ""
	s.ForgetSources = nil
	configMu.Lock()
	defer configMu.Unlock()
	return saveLearnFromIt(*s)
}

// A retry needs the IDs that descendants cite even after their source
// files have gone. Retain only IDs until the whole cascade succeeds.
func (s *learnFromItSettings) rememberForgetSources(ids map[string]bool) error {
	if s == nil || len(ids) == len(s.ForgetSources) {
		return nil
	}
	sources := make([]string, 0, len(ids))
	for id := range ids {
		sources = append(sources, id)
	}
	slices.Sort(sources)
	s.ForgetSources = sources
	configMu.Lock()
	defer configMu.Unlock()
	return saveLearnFromIt(*s)
}

func (a *App) changeLearnFromIt(id string, on bool) error {
	consentChangeMu.Lock()
	defer consentChangeMu.Unlock()
	s, err := readLearnFromIt()
	if err != nil {
		return err
	}
	if err = a.finishHistoryWithdrawal(&s); err != nil {
		return err
	}
	roots, err := assistantHistoryRootsFromEnv()
	if err != nil {
		return err
	}
	alreadyOn := s.Choices[id].On
	for agent := range historySources {
		if id != "all" && agent != id {
			continue
		}
		if on && !a.historyAvailable(agent, roots) {
			if id != "all" {
				return errors.New("agent is unavailable on this computer")
			}
			continue
		}
		s.Choices[agent] = learnFromItChoice{on, time.Now().UnixMilli(), "person"}
	}
	if !on {
		s.Forget = id
	}
	invalidate := !on || !alreadyOn || id == "all"
	if invalidate {
		a.invalidateAssistantHistoryScan()
	}
	historySourceMu.Lock()
	configMu.Lock()
	err = saveLearnFromIt(s)
	if err == nil && invalidate {
		a.invalidateAssistantHistoryScan()
	}
	configMu.Unlock()
	historySourceMu.Unlock()
	if err != nil {
		return err
	}
	if err = a.finishHistoryWithdrawal(&s); err != nil {
		return err
	}
	if on && (!alreadyOn || id == "all") {
		a.startHistorySourceScan(true, historySources[id])
	}
	a.emitAssistantHistoryUpdated()
	return nil
}

// Hold consent through the filesystem operation, so an off choice cannot
// land between authorization and opening a session (or the thread index).
func (a *App) withHistorySource(ctx context.Context, source string, read func()) error {
	historySourceMu.RLock()
	defer historySourceMu.RUnlock()
	s, err := readLearnFromIt()
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !a.IsClosedEyesModeEnabled() && s.allowedSources()[source] {
		read()
	}
	return nil
}

func (a *App) prepareHistorySchedule() bool {
	consentChangeMu.Lock()
	defer consentChangeMu.Unlock()
	s, err := readLearnFromIt()
	if err == nil {
		err = a.finishHistoryWithdrawal(&s)
	}
	if err != nil {
		log.Printf("[assistant-history] consent: %v", err)
		return false
	}
	return len(s.allowedSources()) > 0
}
