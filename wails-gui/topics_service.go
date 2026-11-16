package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"wails-gui/internal/interests"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/browserhistory"
	"wails-gui/internal/privacy"
	"wails-gui/internal/topics"
)

// The topics the user has been looking at (Today's "Your topics"). They are
// the interests the model named (interests_service.go) once it has, and
// until then the phrases ranked on this Mac from what it observed
// (topics_collect.go); the user can pin, rename or remove each one, and turn
// the feature off. Only the labels, and an interest's short queries, leave
// the Mac, as searches for new finds (topic_finds.go).

const (
	topicsShown                   = 10
	topicRefreshInterval          = 10 * time.Minute
	topicBackgroundRefreshDefault = 2 * time.Hour
	topicWithdrawalRebuildDelay   = 30 * time.Second
	topicSnapshotRankThreshold    = 2
	topicSnapshotSummaryThreshold = 0.7
	topicsFeedbackOpen            = 1.1
	topicsFeedbackMore            = 1.35
	topicsFeedbackLess            = 0.7
)

// UserTopic is one topic as Today and Settings show it.
type UserTopic struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Original is the label Isoform found, when the user renamed it.
	Original   string   `json:"original,omitempty"`
	Pinned     bool     `json:"pinned"`
	LastSeenAt string   `json:"lastSeenAt,omitempty"`
	Sources    []string `json:"sources"`
	// Why and Kind come with an interest: why it matters to the user, and
	// project, research, theme or routine.
	Why  string `json:"why,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// UserTopicsView is the "Your topics" list.
type UserTopicsView struct {
	Enabled bool `json:"enabled"`
	// Interests reports a list the model named, not the phrase fallback.
	Interests bool        `json:"interests"`
	Topics    []UserTopic `json:"topics"`
	// Removed are the topics the user took off, so one can come back.
	Removed []UserTopic `json:"removed"`
}

// UserTopicUpdate is one change to a topic: pin, unpin, rename, remove or
// restore. Label is the new name for rename.
type UserTopicUpdate struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Label  string `json:"label"`
}

// topicsService ranks topics and keeps the user's choices about them.
type topicsService struct {
	app *App
	// findsSnap holds the saved Today finds view's refresh state.
	findsSnap topicFindsSnapshotState
	store     *topicsStore
	snapshots *topicSnapshotStore
	now       func() time.Time
	collect   func(now time.Time) topicInput
	// interests returns the stored interests, best first.
	interests func() []interests.Interest
	// following reuses verified evidence without doing a background pass's work.
	following                    func(context.Context, topicRegistryRevision) ([]interests.Interest, error)
	privacyWithdrawalUnsubscribe func()

	mu      sync.Mutex
	cacheAt time.Time
	cache   []topics.Topic
	// rebuilding is set while a background rebuild of the ranking runs.
	rebuilding bool
	// lastStart is when Find new last started a search, for its cooldown.
	lastStart time.Time
	// startMu makes starting a search one step at a time.
	startMu sync.Mutex
	// registryMu makes rebuilding and persisting project identity one step at a time.
	registryMu                   sync.Mutex
	registryWorkerMu             sync.Mutex
	registryRefresh              chan struct{}
	registryWorkerContext        context.Context
	registryWorkerCancel         context.CancelFunc
	registryWorkerDone           chan struct{}
	registryBuildPending         bool
	registryFullBuilding         bool
	registryNextRefresh          time.Time
	registryWithdrawalRebuildAt  time.Time
	registryLastBuildStart       time.Time
	registryStale                bool
	registryManualBuildPending   bool
	registryBuildManual          bool
	registryScheduleChanged      chan struct{}
	registryAfter                func(time.Duration) <-chan time.Time
	registryLastSnapshotReplaced bool
	registrySnapshotMu           sync.Mutex
	registryWithdrawalScopes     []privacy.Scope
	registryObservedSchema       int64
	registryObserverGeneration   int64
	registryRevisionMu           sync.Mutex
	registryObservedDB           *sql.DB
	registryFilesMu              sync.Mutex
	registryFilesCache           *topicRegistryFileCache
	registryCacheMu              sync.RWMutex
	registryCache                *topicRegistryCache
	registryPartial              *topicTodaySnapshot
	registryPartialNext          time.Time
	registryPartialRunning       bool
	registryDeferredChat         *topicRegistryChatWork // Owned by registryMu, like collection.
	registryTestReadBudget       time.Duration          // Race fixtures only; zero uses the production budget.
	registryFailuresMu           sync.Mutex
	registryFailures             map[string]bool
	registryRetryAt              time.Time
	registryRetrying             bool
	registryCollectContext       func(context.Context) (context.Context, context.CancelFunc)
	chatCacheMu                  sync.Mutex
	chatCache                    *topicChatSourceCache
	chatCacheEpoch               uint64
	// registryBeforePublish is a deterministic source-withdrawal test seam.
	registryBeforePublish        func()
	registryAfterPublish         func()
	registryDuringCollect        func()
	registryBeforeSnapshot       func()
	registryChatContext          func(context.Context) (context.Context, context.CancelFunc)
	registryDuringChat           func(string)
	registryFilesHashed          func()
	registryFileStats            func(time.Duration)
	registryRecordingHeadersRead func()
	registrySessionFilesCaptured func()
}

func newTopicsService(app *App) *topicsService {
	dir, err := topicsStateDir()
	if err != nil {
		log.Printf("[topics] state directory is unavailable")
	}
	svc := &topicsService{app: app, store: newTopicsStore(dir), snapshots: newTopicSnapshotStore(dir), now: time.Now}
	svc.findsSnap.launchAt = time.Now()
	svc.findsSnap.launchDelay = topicFindsLaunchDelay
	svc.findsSnap.eventDelay = topicFindsEventDelay
	svc.collect = func(now time.Time) topicInput { return collectTopicInput(app, now) }
	svc.interests = func() []interests.Interest { return app.interestsSvc().list() }
	svc.following = app.interestsSvc().following
	svc.privacyWithdrawalUnsubscribe = subscribePrivacyWithdrawals(func(withdrawal privacy.Withdrawal) {
		if svc.app != nil && svc.app.topics == svc {
			svc.applyPrivacyWithdrawal(withdrawal.Scope)
		}
	})
	svc.snapshots.Load()
	svc.migrateSnapshotIfNeeded()
	return svc
}

// topicsSvc returns the app's topics service, made on first use.
func (a *App) topicsSvc() *topicsService {
	a.topicsOnce.Do(func() { a.topics = newTopicsService(a) })
	return a.topics
}

// invalidate drops the cached ranking after the user changed something.
func (s *topicsService) invalidate() {
	s.mu.Lock()
	s.cacheAt = time.Time{}
	s.cache = nil
	s.mu.Unlock()
}

// ranked returns the interests the model named, or the phrase ranking
// until it has named any.
func (s *topicsService) ranked(force bool) ([]topics.Topic, topicsStateFile) {
	phrases, state := s.rankedPhrases(force)
	if !state.enabled() || s.interests == nil {
		return phrases, state
	}
	if list := s.interests(); len(list) > 0 {
		return interestTopics(list), state
	}
	return phrases, state
}

// interestTopics shows interests the way the rest of the topics code reads
// a ranking.
func interestTopics(list []interests.Interest) []topics.Topic {
	out := make([]topics.Topic, 0, len(list))
	for _, it := range list {
		stats := it.Stats(time.Local)
		t := topics.Topic{ID: it.ID, Key: it.Key, Label: it.Name, Score: it.Score, LastAt: stats.LastAt, Days: stats.Days, Mentions: stats.Items}
		if it.FirstAt > 0 {
			t.FirstAt = time.UnixMilli(it.FirstAt)
		}
		for _, source := range stats.Sources {
			t.Sources = append(t.Sources, topics.Source(source))
		}
		out = append(out, t)
	}
	return out
}

// rankedPhrases returns the phrase ranking, recomputing it at most every
// ten minutes, and records the browser journal on the way.
// rankedPhrases answers from the cached ranking. A stale ranking is served at once
// and rebuilt in the background, one rebuild at a time: a rebuild collects the
// sources and redacts every signal, which took seconds on the request path (R24b,
// 2026-11-05). Only a cold start, with nothing cached, builds on the request.
func (s *topicsService) rankedPhrases(force bool) ([]topics.Topic, topicsStateFile) {
	state := s.store.load()
	if !state.enabled() {
		return nil, state
	}
	now := s.now()
	s.mu.Lock()
	if !force && s.cache != nil {
		cached := s.cache
		if now.Sub(s.cacheAt) >= topicRefreshInterval && !s.rebuilding {
			s.rebuilding = true
			go func() {
				s.rebuildRankedPhrases(now)
				s.mu.Lock()
				s.rebuilding = false
				s.mu.Unlock()
			}()
		}
		s.mu.Unlock()
		return cached, state
	}
	s.mu.Unlock()
	return s.rebuildRankedPhrases(now)
}

// rebuildRankedPhrases collects the sources, records the journal and ranks the topics,
// then publishes the ranking as the cache.
func (s *topicsService) rebuildRankedPhrases(now time.Time) ([]topics.Topic, topicsStateFile) {
	state := s.store.load()
	in := s.collect(now)
	if updated, err := s.store.update(func(st *topicsStateFile) {
		// The switch may have gone off while the sources were read.
		if !st.enabled() {
			return
		}
		recordTopicJournal(st, topicJournalRows(in), func(rows []BrowserVisitRow) []topicJournalPhrase {
			return topicJournalPhrases(in, rows)
		}, now)
		pruneTopicsState(st, now)
	}); err == nil {
		state = updated
	}
	in.Journal = state.Journal
	ranked := rankTopics(in, state, now)

	s.mu.Lock()
	s.cache = ranked
	s.cacheAt = now
	s.mu.Unlock()
	return ranked, state
}

// rankTopics applies the user's choices to one ranking.
func rankTopics(in topicInput, state topicsStateFile, now time.Time) []topics.Topic {
	signals, names := topicSignals(in)
	var blocked []string
	for _, o := range state.Overrides {
		if o.Removed {
			blocked = append(blocked, o.Key)
		}
	}
	return topics.Rank(signals, topics.Options{
		Now:      now,
		Window:   topicWindow,
		Location: time.Local,
		Limit:    topicsShown,
		Names:    names,
		Blocked:  blocked,
		Weights:  state.Weights,
	})
}

// topicJournalRows keeps the visits the journal may remember: not from a
// protected browser, not in a private window, not while paused.
func topicJournalRows(in topicInput) []BrowserVisitRow {
	var out []BrowserVisitRow
	for _, row := range in.Visits {
		browser, _ := browserhistory.BrowserNamed(row.Browser)
		if browserVisitSensitive(browser.BundleID, row.Browser) || activitymon.IsPrivateBrowsing(row.Title) {
			continue
		}
		at := time.UnixMilli(row.VisitedAt)
		if in.PausesUnknown || pausedAt(in.Pauses, at) {
			continue
		}
		out = append(out, row)
	}
	return out
}

// topicJournalPhrases digests one browser's visits on one day, with the same
// exclusions as a ranking.
func topicJournalPhrases(in topicInput, rows []BrowserVisitRow) []topicJournalPhrase {
	day := topicInput{Now: in.Now, Pauses: in.Pauses, PausesUnknown: in.PausesUnknown, Visits: rows}
	signals, _ := topicSignals(day)
	var out []topicJournalPhrase
	for _, p := range topics.Digest(signals, in.People, topicJournalPerDay) {
		out = append(out, topicJournalPhrase{Text: p.Text, Source: string(p.Source), Count: p.Count})
	}
	return out
}

// userTopics merges the ranking with the user's pins, names and removals:
// pinned topics first, then the ranking, at most ten.
func userTopics(ranked []topics.Topic, state topicsStateFile) UserTopicsView {
	return userTopicsLimit(ranked, state, topicsShown)
}

func userTopicsLimit(ranked []topics.Topic, state topicsStateFile, limit int) UserTopicsView {
	view := UserTopicsView{Enabled: state.enabled(), Topics: []UserTopic{}, Removed: []UserTopic{}}
	byID := map[string]topics.Topic{}
	for _, t := range ranked {
		byID[t.ID] = t
	}
	ids := make([]string, 0, len(state.Overrides))
	for id := range state.Overrides {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := state.Overrides[ids[i]], state.Overrides[ids[j]]
		if a.At != b.At {
			return a.At < b.At
		}
		return ids[i] < ids[j]
	})
	seen := map[string]bool{}
	for _, id := range ids {
		o := state.Overrides[id]
		if o.Removed {
			view.Removed = append(view.Removed, userTopicFrom(id, o, byID[id]))
		} else if o.Pinned {
			view.Topics = append(view.Topics, userTopicFrom(id, o, byID[id]))
			seen[id] = true
		}
	}
	if !view.Enabled {
		return view
	}
	for _, t := range ranked {
		if len(view.Topics) >= limit {
			break
		}
		if seen[t.ID] {
			continue
		}
		o, ok := state.Overrides[t.ID]
		if ok && o.Removed {
			continue
		}
		view.Topics = append(view.Topics, userTopicFrom(t.ID, o, t))
	}
	return view
}

func userTopicFrom(id string, o topicOverride, t topics.Topic) UserTopic {
	label := t.Label
	if label == "" {
		label = o.Label
	}
	if label == "" {
		label = o.Key
	}
	out := UserTopic{ID: id, Label: label, Pinned: o.Pinned, Sources: []string{}}
	if o.Renamed && o.Label != "" && o.Label != label {
		out.Original = label
		out.Label = o.Label
	}
	if !t.LastAt.IsZero() {
		out.LastSeenAt = t.LastAt.UTC().Format(time.RFC3339)
	}
	for _, source := range t.Sources {
		out.Sources = append(out.Sources, string(source))
	}
	return out
}

// view returns the "Your topics" list, with each interest's why line.
func (s *topicsService) view() UserTopicsView {
	ranked, state := s.ranked(false)
	return s.viewRanked(ranked, state)
}

func (s *topicsService) viewRanked(ranked []topics.Topic, state topicsStateFile) UserTopicsView {
	view := userTopics(ranked, state)
	if s.interests == nil {
		return view
	}
	return userTopicsWithInterests(view, s.interests())
}

func userTopicsWithInterests(view UserTopicsView, list []interests.Interest) UserTopicsView {
	if !view.Enabled {
		return view
	}
	byID := map[string]interests.Interest{}
	for _, it := range list {
		byID[it.ID] = it
	}
	if len(byID) == 0 {
		return view
	}
	view.Interests = true
	for _, topics := range [][]UserTopic{view.Topics, view.Removed} {
		for i := range topics {
			if it, ok := byID[topics[i].ID]; ok {
				topics[i].Why, topics[i].Kind = it.Why, string(it.Kind)
			}
		}
	}
	return view
}

// update applies one change to a topic.
func (s *topicsService) update(input UserTopicUpdate) (UserTopicsView, error) {
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	if input.Action == "add" {
		return s.add(input.Label)
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return UserTopicsView{}, errors.New("choose a topic")
	}
	ranked, _ := s.ranked(false)
	var found *topics.Topic
	for i := range ranked {
		if ranked[i].ID == id {
			found = &ranked[i]
			break
		}
	}
	switch input.Action {
	case "pin", "unpin", "rename", "remove", "restore":
	default:
		return UserTopicsView{}, errors.New("unknown topic change")
	}
	label := ""
	if input.Action == "rename" {
		clean, err := cleanTopicLabel(input.Label)
		if err != nil {
			return UserTopicsView{}, err
		}
		label = clean
	}
	now := s.now()
	_, err := s.store.update(func(st *topicsStateFile) {
		if st.Overrides == nil {
			st.Overrides = map[string]topicOverride{}
		}
		o, ok := st.Overrides[id]
		if !ok {
			if found == nil {
				return
			}
			o = topicOverride{Key: found.Key}
		}
		if !o.Renamed && found != nil && input.Action != "rename" {
			// Keep the label as shown, so a pinned topic reads the same
			// after it leaves the ranking.
			o.Label = found.Label
		}
		switch input.Action {
		case "pin":
			o.Pinned, o.Removed = true, false
		case "unpin":
			o.Pinned = false
		case "rename":
			o.Label, o.Renamed = label, true
		case "remove":
			o.Removed, o.Pinned = true, false
			removeTopicFinds(st, id)
		case "restore":
			o.Removed = false
		default:
			return
		}
		o.At = now.UTC().Format(time.RFC3339Nano)
		if !o.Pinned && !o.Removed && !o.Renamed {
			delete(st.Overrides, id)
			return
		}
		st.Overrides[id] = o
	})
	if err != nil {
		return UserTopicsView{}, err
	}
	s.invalidate()
	return s.view(), nil
}

// add keeps a topic the person named (Topics v1.0.0 rule 30). The same name
// twice is one topic.
func (s *topicsService) add(raw string) (UserTopicsView, error) {
	label, err := cleanTopicLabel(raw)
	if err != nil {
		return UserTopicsView{}, err
	}
	now := s.now()
	_, err = s.store.update(func(st *topicsStateFile) {
		for _, added := range st.Added {
			if strings.EqualFold(added.Label, label) {
				return
			}
		}
		st.Added = append(st.Added, topicAdded{ID: addedTopicPrefix + uuid.NewString(), Label: label, At: now.UTC().Format(time.RFC3339Nano)})
	})
	if err != nil {
		return UserTopicsView{}, err
	}
	s.invalidate()
	return s.view(), nil
}

// addedTopicPrefix marks the id of a topic the person named.
const addedTopicPrefix = "added:"

// removeTopicFinds drops the shown finds of a removed topic, and the topic
// from the search waiting to run, so a run that starts later never searches
// it.
func removeTopicFinds(st *topicsStateFile, topicID string) {
	kept := st.Finds.Items[:0]
	for _, item := range st.Finds.Items {
		if item.TopicID != topicID {
			kept = append(kept, item)
		}
	}
	st.Finds.Items = kept
	sent := st.Finds.Sent[:0]
	for _, s := range st.Finds.Sent {
		if s.ID != topicID {
			sent = append(sent, s)
		}
	}
	st.Finds.Sent = sent
}

// setEnabled pauses or resumes topic finds without deleting observations,
// interests, finds, or the history of their research schedules.
func (s *topicsService) setEnabled(enabled bool) (UserTopicsView, error) {
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	// Wait for a start in progress, so no search starts after Off.
	s.startMu.Lock()
	defer s.startMu.Unlock()
	_, err := s.store.update(func(st *topicsStateFile) {
		on := enabled
		st.Enabled = &on
	})
	if err != nil {
		return UserTopicsView{}, err
	}
	if !enabled && s.app != nil {
		s.app.pauseTopicFindsJob()
	}
	s.invalidate()
	return s.view(), nil
}

// cleanTopicLabel checks a name the user typed: a few words, never a link,
// a path or an address, since the label is what the search sends.
func cleanTopicLabel(raw string) (string, error) {
	label := strings.Join(strings.Fields(raw), " ")
	if label == "" {
		return "", errors.New("a topic needs a name")
	}
	if utf8.RuneCountInString(label) > topicLabelMaxRunes || len(strings.Fields(label)) > 6 {
		return "", errors.New("keep a topic to a few words")
	}
	if strings.Contains(label, "://") || strings.ContainsAny(label, "/\\@") {
		return "", errors.New("a topic cannot be a link, a path or an address")
	}
	return label, nil
}

// adjustTopicWeight multiplies a topic's feedback weight.
func adjustTopicWeight(st *topicsStateFile, key string, factor float64) {
	if key == "" {
		return
	}
	if st.Weights == nil {
		st.Weights = map[string]float64{}
	}
	w, ok := st.Weights[key]
	if !ok {
		w = 1
	}
	st.Weights[key] = clampTopicWeight(w * factor)
}

// Wails bindings.

// GetUserTopics returns the "Your topics" list.
func (a *App) GetUserTopics() (perfValue UserTopicsView, perfErr error) {
	defer logPageBinding("GetUserTopics", time.Now(), &perfValue)
	svc := a.topicsSvc()
	view := svc.view()
	state := svc.store.load()
	cache := svc.loadRegistryCache()
	for i := range view.Topics {
		id := view.Topics[i].ID
		canonical := id
		if cache != nil && cache.result.canonical[id] != "" {
			canonical = cache.result.canonical[id]
		}
		pinned := state.SidebarPins[id] || state.SidebarPins[canonical]
		for _, project := range state.Projects {
			if project.ID == canonical {
				pinned = pinned || topicProjectPinned(project, state.SidebarPins)
				break
			}
		}
		view.Topics[i].Pinned = pinned
	}
	return displayModelCopy(view, a.modelDisplayLanguage()), nil
}

// UpdateUserTopic adds, pins, unpins, renames, removes or restores one
// topic. An added topic gets its first proposal at once (Topics v1.0.0
// rule 30), not at the next daily build.
func (a *App) UpdateUserTopic(input UserTopicUpdate) (UserTopicsView, error) {
	view, err := a.topicsSvc().update(input)
	if err == nil {
		a.afterTopicsChanged(input.Action == "remove")
		a.emitBrowserEvent("topics:updated")
		if input.Action == "add" {
			go a.proposeAddedTopics(context.Background())
		}
	}
	if err != nil {
		return displayModelCopy(view, a.modelDisplayLanguage()), err
	}
	return a.GetUserTopics()
}

// SetTopicFindsEnabled turns "New on your topics" on or off.
func (a *App) SetTopicFindsEnabled(enabled bool) (UserTopicsView, error) {
	view, err := a.topicsSvc().setEnabled(enabled)
	if err == nil {
		a.afterTopicsChanged(false)
		a.emitBrowserEvent("topics:updated")
	}
	if err != nil {
		return displayModelCopy(view, a.modelDisplayLanguage()), err
	}
	return a.GetUserTopics()
}

// afterTopicsChanged drops the topic-search log when a topic is removed.
// Turning the feature off only pauses collection; its logs and results stay.
func (a *App) afterTopicsChanged(forget bool) {
	if !a.topicsLive() || !forget {
		return
	}
	if job := a.findTopicFindsJob(); job != nil {
		a.dropResearchLog(job)
	}
}

// dropResearchLog deletes a research schedule's log session and clears its
// tag, so the next run starts a new one.
func (a *App) dropResearchLog(job *CronJobState) {
	raw, err := json.Marshal(job.TargetConfig)
	if err != nil {
		return
	}
	var cfg cronTargetConfig
	if json.Unmarshal(raw, &cfg) != nil || cfg.SessionTag == "" {
		return
	}
	if a.store != nil {
		if err := a.store.DeleteSession(context.Background(), cfg.SessionTag); err != nil {
			log.Printf("[topics] a research log session could not be removed")
		}
	}
	cfg.SessionTag = ""
	updated, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	target := json.RawMessage(updated)
	if _, err := a.UpdateCronJob(job.ID, CronJobPatch{TargetConfig: &target}); err != nil {
		log.Printf("[topics] a research schedule could not be updated: %v", err)
	}
}

// startTopics runs the daily search check: two minutes after launch, then
// every fifteen minutes, it starts the day's search once the morning has
// come (topic_finds.go). The search itself waits for the morning brief when
// the brief is on.
func (a *App) startTopics(ctx context.Context) {
	a.topicsSvc().startRegistryWorker(ctx)
	go func() {
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				a.maybeStartDailyTopicFinds("tick")
				timer.Reset(15 * time.Minute)
			}
		}
	}()
}
