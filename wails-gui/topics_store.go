package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/interests"
)

// topics.json keeps what the topic feature needs between runs, on this Mac
// only: the user's choices about topics (pinned, renamed, removed), the
// weights their feedback set, a fourteen-day journal of short phrases from
// the browser (the visits themselves are kept for three days), hashes of the
// links visited (to skip finds the user has already read), the latest finds,
// durable work-topic records, and the separate sidebar pins.

const (
	topicsStateFileName  = "topics.json"
	topicsStateVersion   = 1
	topicJournalDays     = 14
	topicJournalPerDay   = 40
	topicVisitedKeep     = 14 * 24 * time.Hour
	topicShownKeep       = 30 * 24 * time.Hour
	topicFeedbackKeep    = 200
	topicWeightMin       = 0.25
	topicWeightMax       = 3
	topicLabelMaxRunes   = 48
	topicsStateDirEnvVar = "YANSU_TOPICS_STATE_DIR"
)

// topicJournalPhrase is one short phrase the browser showed on a day.
type topicJournalPhrase struct {
	Text   string `json:"text"`
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// topicJournalDay is one browser's phrases on one local date.
type topicJournalDay struct {
	Date    string               `json:"date"`
	Browser string               `json:"browser"`
	Phrases []topicJournalPhrase `json:"phrases"`
}

// topicOverride is what the user decided about one topic.
type topicOverride struct {
	Key     string `json:"key"`
	Label   string `json:"label,omitempty"`
	Pinned  bool   `json:"pinned,omitempty"`
	Removed bool   `json:"removed,omitempty"`
	// Renamed marks a label the user typed; Label otherwise keeps the name
	// shown when the user pinned or removed the topic.
	Renamed bool   `json:"renamed,omitempty"`
	At      string `json:"at"`
}

// topicSent is a topic as it was sent to the search, so finds are matched to
// it and say when the user last looked.
type topicSent struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Label    string `json:"label"`
	LookedAt string `json:"lookedAt,omitempty"`
	Rank     int    `json:"rank"`
	// Queries are an interest's short searches; none for a phrase topic or
	// an interest the user renamed.
	Queries []string `json:"queries,omitempty"`
	// ReadSources is written before dispatch, never taken from search output.
	// Retain the exact evidence set so later reads cannot substitute a refreshed
	// interest's sources for the sources that informed this search.
	ReadSources *topicSentReadSources `json:"readSources,omitempty"`
}

type topicSentReadSources struct {
	Version   int             `json:"version"`
	UserNamed bool            `json:"userNamed,omitempty"`
	Evidence  []interests.Ref `json:"evidence,omitempty"`
}

// topicFeedback is one open or answer on a find.
type topicFeedback struct {
	FindID  string `json:"findId"`
	TopicID string `json:"topicId"`
	Action  string `json:"action"`
	At      string `json:"at"`
}

// topicFindsState is the latest search and what came of it.
type topicFindsState struct {
	// StartedOn is the local date a daily search was last started; RunID is
	// the run whose result is shown and RanOn its local date.
	StartedOn string      `json:"startedOn,omitempty"`
	StartedAt string      `json:"startedAt,omitempty"`
	Sent      []topicSent `json:"sent,omitempty"`
	// Manual marks a search the user started from Today; it brings no notice.
	Manual bool   `json:"manual,omitempty"`
	RunID  string `json:"runId,omitempty"`
	RanOn  string `json:"ranOn,omitempty"`
	// Unavailable marks a last run whose searches returned nothing; Via
	// names how it searched.
	Unavailable bool        `json:"unavailable,omitempty"`
	Via         string      `json:"via,omitempty"`
	RanAt       string      `json:"ranAt,omitempty"`
	Items       []TopicFind `json:"items,omitempty"`
	// Shown holds the hashes of links shown before, so a find is shown once.
	Shown map[string]int64 `json:"shown,omitempty"`
	// NotifiedOn is the local date of the last notice; MomentID names it so
	// opens on Today count as receipts.
	NotifiedOn string          `json:"notifiedOn,omitempty"`
	MomentID   string          `json:"momentId,omitempty"`
	Feedback   []topicFeedback `json:"feedback,omitempty"`
}

type topicsStateFile struct {
	Version                int                 `json:"version"`
	Today                  *topicTodaySnapshot `json:"today,omitempty"`
	RefreshIntervalMinutes int                 `json:"refreshIntervalMinutes,omitempty"`
	// Enabled is the feature switch; nil means on.
	Enabled     *bool                    `json:"enabled,omitempty"`
	Overrides   map[string]topicOverride `json:"overrides,omitempty"`
	Weights     map[string]float64       `json:"weights,omitempty"`
	Journal     []topicJournalDay        `json:"journal,omitempty"`
	Visited     map[string]int64         `json:"visited,omitempty"`
	Finds       topicFindsState          `json:"finds"`
	Projects    []topicProject           `json:"projects,omitempty"`
	SidebarPins map[string]bool          `json:"sidebarPins,omitempty"`
	ChatTopics  []topicChatSnapshot      `json:"chatTopics,omitempty"`
	// Added are the topics the person named themselves (Topics v1.0.0 rule
	// 30). They skip the two-day rule and keep their place in every daily
	// build (rule 32).
	Added []topicAdded `json:"added,omitempty"`
}

// topicAdded is one topic the person named.
type topicAdded struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	At    string `json:"at"`
}

func (f topicsStateFile) backgroundRefreshInterval() time.Duration {
	minutes := f.RefreshIntervalMinutes
	if minutes == 0 {
		return topicBackgroundRefreshDefault
	}
	if minutes < 5 {
		minutes = 5
	}
	if minutes > 120 {
		minutes = 120
	}
	return time.Duration(minutes) * time.Minute
}

func (f topicsStateFile) enabled() bool {
	return f.Enabled == nil || *f.Enabled
}

// topicsStore reads and writes topics.json atomically.
type topicsStore struct {
	path        string
	mu          sync.Mutex
	publication sync.RWMutex
}

func topicsStateDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(topicsStateDirEnvVar)); dir != "" {
		return dir, nil
	}
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "topics"), nil
}

func newTopicsStore(dir string) *topicsStore {
	if dir == "" {
		return &topicsStore{}
	}
	return &topicsStore{path: filepath.Join(dir, topicsStateFileName)}
}

// load returns the stored state, or an empty one.
func (s *topicsStore) load() topicsStateFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *topicsStore) loadLocked() topicsStateFile {
	state := topicsStateFile{Version: topicsStateVersion}
	if s.path == "" {
		return state
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return state
	}
	if json.Unmarshal(raw, &state) != nil {
		return topicsStateFile{Version: topicsStateVersion}
	}
	return state
}

// update applies fn to the stored state and writes it back.
func (s *topicsStore) update(fn func(*topicsStateFile)) (topicsStateFile, error) {
	s.publication.Lock()
	defer s.publication.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.loadLocked()
	fn(&state)
	return s.saveLocked(state)
}

func (s *topicsStore) saveLocked(state topicsStateFile) (topicsStateFile, error) {
	state.Version = topicsStateVersion
	if s.path == "" {
		return state, errors.New("the topics folder is unavailable")
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return state, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return state, err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return state, err
	}
	return state, os.Rename(tmp, s.path)
}

// topicLinkKey is a link reduced to what the stored visits keep (scheme,
// host without "www.", path without a trailing slash), so a find and a visit
// to the same page match. Query strings and fragments are dropped.
func topicLinkKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path := strings.TrimRight(u.EscapedPath(), "/")
	return host + path
}

// topicLinkHash is the stored form of a link key: the journal never keeps
// the links themselves.
func topicLinkHash(raw string) string {
	key := topicLinkKey(raw)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// pruneTopicsState drops journal days, visits, shown links and feedback past
// their keep.
func pruneTopicsState(state *topicsStateFile, now time.Time) {
	floor := now.Add(-topicJournalDays * 24 * time.Hour).In(time.Local).Format("2006-01-02")
	kept := state.Journal[:0]
	for _, day := range state.Journal {
		if day.Date >= floor {
			kept = append(kept, day)
		}
	}
	state.Journal = kept
	pruneTopicTimes(state.Visited, now.Add(-topicVisitedKeep))
	pruneTopicTimes(state.Finds.Shown, now.Add(-topicShownKeep))
	if extra := len(state.Finds.Feedback) - topicFeedbackKeep; extra > 0 {
		state.Finds.Feedback = append([]topicFeedback(nil), state.Finds.Feedback[extra:]...)
	}
}

func pruneTopicTimes(m map[string]int64, floor time.Time) {
	for key, at := range m {
		if at < floor.UnixMilli() {
			delete(m, key)
		}
	}
}

// recordTopicJournal adds a journal day for each browser and finished local
// date the stored visits cover and the journal does not have yet, and
// remembers the visited links by hash.
func recordTopicJournal(state *topicsStateFile, rows []BrowserVisitRow, phrases func(rows []BrowserVisitRow) []topicJournalPhrase, now time.Time) {
	today := now.In(time.Local).Format("2006-01-02")
	have := map[string]bool{}
	for _, day := range state.Journal {
		have[day.Date+"\x00"+day.Browser] = true
	}
	byDay := map[string][]BrowserVisitRow{}
	var order []string
	if state.Visited == nil {
		state.Visited = map[string]int64{}
	}
	for _, row := range rows {
		if hash := topicLinkHash(row.URL); hash != "" && row.VisitedAt > state.Visited[hash] {
			state.Visited[hash] = row.VisitedAt
		}
		date := time.UnixMilli(row.VisitedAt).In(time.Local).Format("2006-01-02")
		if date >= today {
			continue
		}
		key := date + "\x00" + row.Browser
		if have[key] {
			continue
		}
		if _, ok := byDay[key]; !ok {
			order = append(order, key)
		}
		byDay[key] = append(byDay[key], row)
	}
	sort.Strings(order)
	for _, key := range order {
		date, browser, _ := strings.Cut(key, "\x00")
		state.Journal = append(state.Journal, topicJournalDay{Date: date, Browser: browser, Phrases: phrases(byDay[key])})
	}
}

// clampTopicWeight keeps a feedback weight inside its bounds.
func clampTopicWeight(w float64) float64 {
	if w < topicWeightMin {
		return topicWeightMin
	}
	if w > topicWeightMax {
		return topicWeightMax
	}
	return w
}

// publishRegistryLocked commits only a validated snapshot. The caller owns
// publication through the final source fences.
// Other topic writers retain their fields; a concurrent change asks for retry.
func (s *topicsStore) publishRegistryLocked(expected topicsStateFile, projects []topicProject, chat []topicChatSnapshot, today *topicTodaySnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.loadLocked()
	// Compare the persisted representation: omitempty normalizes empty maps
	// and slices, so a successful journal update must not look like a race.
	currentJSON, err := json.Marshal(current)
	if err != nil {
		return err
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(currentJSON, expectedJSON) {
		return errTopicRegistryChanged
	}
	candidate := current
	candidate.Projects = projects
	candidate.ChatTopics = chat
	candidate.Today = today
	if today != nil {
		today.State = topicTodayStateHash(candidate)
	}
	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	if bytes.Equal(currentJSON, candidateJSON) {
		return nil
	}
	current.Projects = projects
	current.ChatTopics = chat
	current.Today = today
	_, err = s.saveLocked(current)
	return err
}
