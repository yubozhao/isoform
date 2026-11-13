package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"yansu-agent/agent"

	"wails-gui/internal/interests"
)

// The interests behind Today's "Your topics": what the user is genuinely
// into, named by a model from what the Mac already summarized, instead of
// phrases counted across titles. A light-model pass refreshes them up to
// once a day when enough new activity came in, anchored on the
// stored list so ids and names stay put; once a month the main model
// reviews the whole month, merging, renaming and dropping. Every pass is
// checked on the Mac (internal/interests) before it is stored, and only an
// interest's name and its short queries ever leave the Mac, as searches.

const (
	interestsCheckEvery       = 30 * time.Minute
	interestsFirstCheck       = 3 * time.Minute
	interestsRefreshGap       = 20 * time.Hour
	interestsRefreshesPerDay  = 1
	interestsMinNewActivities = 5
	interestsConsolidateEvery = 30 * 24 * time.Hour
	interestsConsolidateMin   = 15
	interestsRetryAfter       = time.Hour
	interestsRetryMax         = 12 * time.Hour
	// A failed monthly review waits a day, then longer, while refreshes go
	// on.
	interestsConsolidateRetryAfter = 24 * time.Hour
	interestsConsolidateRetryMax   = 7 * 24 * time.Hour
	// interestsCallsPerDay bounds the model calls of one process in a day,
	// whatever the stored state says: three refreshes and a review.
	interestsCallsPerDay  = interestsRefreshesPerDay + 1
	interestsModelTimeout = 3 * time.Minute
	// interestsOverlap is how far before the last pass a refresh reads, so
	// an activity that went on after it counts again.
	interestsOverlap = 24 * time.Hour
)

// interestsOwnNames are the app's names: never an interest, never searched.
var interestsOwnNames = []string{"Isoform", "Yansu", "言溯"}

// interestsUserState is what the user decided, from topics.json: the names
// they removed, the ids they pinned, the feedback weights, and the word
// counter's labels as hints.
type interestsUserState struct {
	// Removed are the names to refuse: the shown label, the user's rename
	// and the stored name of each removed topic. RemovedIDs are their ids.
	Removed    []string
	RemovedIDs map[string]bool
	// RemovedNames keeps the source identity of labels in the model avoid-list.
	RemovedNames map[string][]string
	Pinned       map[string]bool
	// Renamed holds the ids the user renamed: like a pin, a rename anchors
	// an interest's identity when near-duplicates fold.
	Renamed map[string]bool
	Weights map[string]float64
	Hints   []string
}

// interestsService runs the passes and keeps the list.
type interestsService struct {
	store   *interestsStore
	now     func() time.Time
	collect func(from, now time.Time) interestInput
	// readFilter checks stored evidence again before model replay.
	readFilter func() func(interests.Interest) bool
	// followingFilter verifies activity evidence by following its source refs.
	// Other evidence waits for the background verification snapshot.
	followingFilter   func(context.Context) func(interests.Interest) bool
	followingRevision func(context.Context) (topicRegistryRevision, error)
	followingMu       sync.Mutex
	followingVerified *interestsFollowingVerification
	// rewriteAllowed gates the one why-rewrite pass per start: never while
	// observing is paused (Closed Eyes).
	rewriteAllowed func() bool
	// allowed reports whether a pass may run now: the topics feature on,
	// observing not paused, "Watch my screen" on, a model set up.
	allowed func() bool
	// enabled reports the topics feature switch: a pass that ends after
	// it went off keeps nothing.
	enabled func() bool
	// changed runs after a pass stored a new list.
	changed func()
	// model runs one prompt with no tools on the named model.
	model      func(ctx context.Context, prompt, model string) (string, error)
	lightModel func() string
	mainModel  func() string
	// user returns the user's choices and the word counter's hints;
	// choices returns the choices alone, cheaply, for reads.
	user     func() interestsUserState
	choices  func() interestsUserState
	language func() string

	// names are the people the last read of the evidence named, for the
	// checks made between passes. Guarded by namesMu.
	namesMu sync.Mutex
	names   []string
	namesAt time.Time

	// running makes passes one at a time.
	running sync.Mutex
	// rewriteChecked makes the after-upgrade why-rewrite pass one per process
	// start (maybeRun).
	rewriteChecked bool
	// This process's model calls, counted apart from the stored state, so a
	// pass whose list could not be stored still counts and backs off, each
	// kind of pass on its own. Guarded by running.
	callsOn      string
	calls        int
	callFailed   map[interests.Mode]time.Time
	callFailures map[interests.Mode]int
}

type interestsFollowingVerification struct {
	revision topicRegistryRevision
	readable map[string]bool
}

// interestsNamesFor is how long the names read for the checks between
// passes are kept.
const interestsNamesFor = time.Hour

func newInterestsService(app *App) *interestsService {
	dir, err := topicsStateDir()
	if err != nil {
		log.Printf("[interests] state directory is unavailable")
	}
	return &interestsService{
		store:           newInterestsStore(dir),
		now:             time.Now,
		collect:         func(from, now time.Time) interestInput { return collectInterestInput(app, from, now) },
		readFilter:      app.interestsReadFilter,
		followingFilter: app.interestsFollowingReadFilter,
		followingRevision: func(ctx context.Context) (topicRegistryRevision, error) {
			return app.topicsSvc().captureRegistryRevision(ctx)
		},
		rewriteAllowed: func() bool {
			// Never while observing is paused (Closed Eyes).
			return !app.IsClosedEyesModeEnabled()
		},
		allowed:    app.interestsAllowed,
		enabled:    func() bool { return app.topicsSvc().store.load().enabled() },
		changed:    app.afterInterestsChanged,
		model:      app.runInterestsPrompt,
		lightModel: app.lightTaskModel,
		mainModel:  app.chatTaskModel,
		user:       app.interestsUserState,
		choices:    app.interestsUserChoices,
		language:   app.currentUILanguageName,
	}
}

// interestsSvc returns the app's interest model, made on first use.
func (a *App) interestsSvc() *interestsService {
	a.interestModelOnce.Do(func() { a.interestModel = newInterestsService(a) })
	return a.interestModel
}

// afterInterestsChanged shows a new list: Your topics and the stored Daily
// News prompt follow it.
func (a *App) afterInterestsChanged() {
	a.topicsSvc().invalidate()
	a.afterTopicsChanged(false)
	a.emitBrowserEvent("topics:updated")
}

// interestsAllowed reports whether a pass may run: the summaries it reads
// go to the model, and its names go to searches later, so it follows the
// same gates as the daily search, and needs the runner.
func (a *App) interestsAllowed() bool {
	if a == nil || !a.topicsSvc().store.load().enabled() || !a.topicLabelsMayLeave() {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runner != nil
}

// interestsUserState reads the user's choices about topics and interests,
// and the word counter's labels as hints.
func (a *App) interestsUserState() interestsUserState {
	out := a.interestsUserChoices()
	// Model hints must rebuild from current sources; the display cache has
	// no complete contributor ledger and cannot authorize replay.
	phrases, _ := a.topicsSvc().rankedPhrases(true)
	for _, t := range phrases {
		out.Hints = append(out.Hints, t.Label)
	}
	return out
}

// interestsUserChoices reads the user's choices: pins, removals, weights.
func (a *App) interestsUserChoices() interestsUserState {
	svc := a.topicsSvc()
	state := svc.store.load()
	out := interestsUserState{Pinned: map[string]bool{}, RemovedIDs: map[string]bool{}, RemovedNames: map[string][]string{}, Renamed: map[string]bool{}, Weights: state.Weights}
	stored := a.interestsSvc().byID()
	seen := map[string]bool{}
	addRemoved := func(name string) {
		if key := strings.ToLower(strings.TrimSpace(name)); key != "" && !seen[key] {
			seen[key] = true
			out.Removed = append(out.Removed, strings.TrimSpace(name))
		}
	}
	for id, o := range state.Overrides {
		switch {
		case o.Removed:
			out.RemovedIDs[id] = true
			out.RemovedNames[id] = []string{o.Label, o.Key, stored[id].Name}
			addRemoved(o.Label)
			addRemoved(o.Key)
			addRemoved(stored[id].Name)
		case o.Pinned:
			out.Pinned[id] = true
		default:
		}
		if o.Renamed {
			out.Renamed[id] = true
		}
	}
	sort.Strings(out.Removed)
	return out
}

// runInterestsPrompt runs one prompt the way the timeline summaries do: no
// tools, no system prompt, nothing kept as a session.
func (a *App) runInterestsPrompt(ctx context.Context, prompt, model string) (string, error) {
	a.mu.Lock()
	runner := a.runner
	a.mu.Unlock()
	if runner == nil {
		return "", fmt.Errorf("runner not initialized")
	}
	var result strings.Builder
	_, err := runner.ExecuteTask(ctx, &agent.TaskInput{
		Priority:          agent.PriorityBackground,
		AutoApproveTools:  true,
		Prompt:            prompt,
		Model:             model,
		PlanMode:          true,
		Ephemeral:         true,
		NoTools:           true,
		NoSystem:          true,
		SuppressTelemetry: true,
	}, func(event *agent.StreamEvent) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if event.Type == "assistant" {
			result.WriteString(event.Content)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("run interests prompt: %w", err)
	}
	return result.String(), nil
}

// list returns the stored interests, best first, checked again with the
// names known now, without the ones whose newest evidence is older than the
// window unless the user pinned them: a pass needs new activity, so a quiet
// stretch must not keep searching for old interests.
func (s *interestsService) list() []interests.Interest {
	list := s.store.load().Interests
	// With nothing to return, no label needs evidence or name revalidation.
	// Avoid collecting the full history on a cold Today topics read.
	if len(list) == 0 {
		return nil
	}
	floor := s.now().Add(-interests.RefreshWindow)
	user := s.userChoices()
	var out []interests.Interest
	readable := func(interests.Interest) bool { return true }
	if s.readFilter != nil {
		readable = s.readFilter()
	}
	for _, it := range list {
		if readable == nil || !readable(it) {
			continue
		}
		if last := it.Stats(time.Local).LastAt; !last.IsZero() && last.Before(floor) && !user.Pinned[it.ID] {
			continue
		}
		if interests.HasReDerivedRef(it) && !it.WhyVerified {
			// The why may have been learned from a contributor the read
			// filter can only re-derive, never fully re-authorize: it stays
			// out of sight until a pass rewrites it from readable evidence.
			it.Why = ""
		}
		out = append(out, it)
	}
	return interests.Recheck(out, s.rules(user), user.Pinned)
}

// following reads the stored list without collecting evidence or waiting for a
// pass. A revision-matched background verification covers every evidence kind;
// otherwise the activity reader fails closed for anything it cannot verify.
func (s *interestsService) following(ctx context.Context, revision topicRegistryRevision) ([]interests.Interest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	list := s.store.load().Interests
	if len(list) == 0 {
		return nil, ctx.Err()
	}
	s.followingMu.Lock()
	verified := s.followingVerified
	s.followingMu.Unlock()
	readable := func(interests.Interest) bool { return true }
	if verified != nil && sameTopicRegistrySources(verified.revision, revision) {
		readable = func(it interests.Interest) bool { return verified.readable[it.ID] }
	} else if s.readFilter != nil {
		readable = nil
		if s.followingFilter != nil {
			readable = s.followingFilter(ctx)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	floor := s.now().Add(-interests.RefreshWindow)
	user := s.userChoices()
	out := make([]interests.Interest, 0, len(list))
	for _, it := range list {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if readable == nil || !readable(it) {
			continue
		}
		if last := it.Stats(time.Local).LastAt; !last.IsZero() && last.Before(floor) && !user.Pinned[it.ID] {
			continue
		}
		if interests.HasReDerivedRef(it) && !it.WhyVerified {
			it.Why = ""
		}
		out = append(out, it)
	}
	s.namesMu.Lock()
	names := append([]string(nil), s.names...)
	s.namesMu.Unlock()
	out = interests.Recheck(out, interests.Rules{Names: names, OwnNames: interestsOwnNames, Removed: user.Removed}, user.Pinned)
	return out, ctx.Err()
}

// rememberFollowing publishes evidence decisions only when the complete source
// revision stayed fixed throughout the background check. The interests store
// is part of that revision, so its newly stored list is covered too. The
// filter is built fresh after the starting revision capture: the pass's
// pre-model closure memoizes decisions (and memory/policy and lazy
// hash/Purpose state) from before the model call, so a source protected or
// deleted while the model ran would otherwise keep its stale allow.
func (s *interestsService) rememberFollowing(ctx context.Context, list []interests.Interest) {
	if s.followingRevision == nil || s.readFilter == nil {
		return
	}
	before, err := s.followingRevision(ctx)
	if err != nil {
		return
	}
	readable := s.readFilter()
	if readable == nil {
		return
	}
	allowed := make(map[string]bool, len(list))
	for _, it := range list {
		if ctx.Err() != nil {
			return
		}
		allowed[it.ID] = readable(it)
	}
	after, err := s.followingRevision(ctx)
	if err != nil || !sameTopicRegistrySources(before, after) {
		return
	}
	s.followingMu.Lock()
	s.followingVerified = &interestsFollowingVerification{revision: after, readable: allowed}
	s.followingMu.Unlock()
}

func (s *interestsService) invalidateFollowingVerification() {
	s.followingMu.Lock()
	s.followingVerified = nil
	s.followingMu.Unlock()
}

// userChoices returns the user's choices, or none in a bare service.
func (s *interestsService) userChoices() interestsUserState {
	if s.choices == nil {
		return interestsUserState{}
	}
	return s.choices()
}

// rules are the checks made between passes: the names the last read of
// the evidence found (read again when an hour old), the app's names and the
// user's removals.
func (s *interestsService) rules(user interestsUserState) interests.Rules {
	return interests.Rules{Names: s.knownNames(), OwnNames: interestsOwnNames, Removed: user.Removed}
}

// knownNames returns the names the evidence held at the last read.
func (s *interestsService) knownNames() []string {
	s.namesMu.Lock()
	fresh := !s.namesAt.IsZero() && s.now().Sub(s.namesAt) < interestsNamesFor
	names := s.names
	s.namesMu.Unlock()
	if fresh || s.collect == nil {
		return names
	}
	now := s.now()
	in := s.collect(now.Add(-interests.RefreshWindow), now)
	_, read := interestItems(in)
	s.rememberNames(read, now)
	return read
}

// rememberNames keeps the names a read found.
func (s *interestsService) rememberNames(names []string, at time.Time) {
	s.namesMu.Lock()
	s.names, s.namesAt = names, at
	s.namesMu.Unlock()
}

// safeToSend reports whether a label may go to a search now.
func (s *interestsService) safeToSend(label string) bool {
	return interests.SafeToSend(label, s.rules(s.userChoices()))
}

// forget drops the interests and their evidence (the feature went off). A
// pass still running stores nothing once the switch is off (maybeRun).
func (s *interestsService) forget() error {
	return s.store.forget()
}

// byID indexes every stored interest.
func (s *interestsService) byID() map[string]interests.Interest {
	out := map[string]interests.Interest{}
	for _, it := range s.store.load().Interests {
		out[it.ID] = it
	}
	return out
}

// whyRewriteDue reports a stored interest whose why is unverified on a
// re-derived reference: after an upgrade those whys stay out of sight until
// a pass rewrites them from currently readable evidence.
func whyRewriteDue(list []interests.Interest) bool {
	for _, it := range list {
		if !it.WhyVerified && interests.HasReDerivedRef(it) {
			return true
		}
	}
	return false
}

// interestsTiming says which pass the clock allows now, before anything is
// read: the monthly review when a month has passed since the last one, a
// refresh when today has room and the last pass is old enough. A failed
// refresh backs refreshes off; a failed review backs only reviews off.
func interestsTiming(st interestsStateFile, now time.Time) (consolidate, refresh bool) {
	last := parseRFC3339(st.RefreshedAt)
	consolidated := parseRFC3339(st.ConsolidatedAt)
	consolidate = (consolidated.IsZero() || now.Sub(consolidated) >= interestsConsolidateEvery) &&
		!backingOff(st.ConsolidateFailedAt, st.ConsolidateFailures, interestsConsolidateRetryAfter, interestsConsolidateRetryMax, now)
	runs := st.RunsToday
	if st.RunsOn != now.In(time.Local).Format("2006-01-02") {
		runs = 0
	}
	refresh = runs < interestsRefreshesPerDay && (last.IsZero() || now.Sub(last) >= interestsRefreshGap) &&
		!backingOff(st.FailedAt, st.Failures, interestsRetryAfter, interestsRetryMax, now)
	return consolidate, refresh
}

// backingOff reports a wait after failures in a row: base, doubling, at
// most limit.
func backingOff(failedAt string, failures int, base, limit time.Duration, now time.Time) bool {
	failed := parseRFC3339(failedAt)
	if failed.IsZero() || failures <= 0 {
		return false
	}
	return now.Sub(failed) < retryWait(failures, base, limit)
}

func retryWait(failures int, base, limit time.Duration) time.Duration {
	wait := base << min(max(failures-1, 0), 6)
	if wait > limit {
		return limit
	}
	return wait
}

func parseRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// interestsMode picks the pass from what was read: the monthly review once
// there is a month's worth of activity to review, else a refresh when
// enough new activity came in since the last pass (or, with no interests
// yet, enough activity at all).
func interestsMode(st interestsStateFile, consolidate, refresh bool, items []interests.Item) (interests.Mode, bool) {
	last := parseRFC3339(st.RefreshedAt)
	total, fresh := 0, 0
	for _, item := range items {
		if item.Source != interests.SourceActivity {
			continue
		}
		total++
		if last.IsZero() || item.At.After(last) {
			fresh++
		}
	}
	switch {
	case consolidate && total >= interestsConsolidateMin:
		return interests.ModeConsolidate, true
	case refresh && len(st.Interests) == 0 && total >= interestsMinNewActivities:
		return interests.ModeRefresh, true
	case refresh && fresh >= interestsMinNewActivities:
		return interests.ModeRefresh, true
	default:
		return "", false
	}
}

// refreshItems keeps, for an anchored refresh, the evidence since shortly
// before the last pass; what describes the user now (Purpose Map, About
// you, chats) always stays.
func refreshItems(items []interests.Item, since time.Time) []interests.Item {
	var out []interests.Item
	for _, item := range items {
		switch item.Source {
		case interests.SourceGoal, interests.SourceAbout, interests.SourceChat:
			out = append(out, item)
		default:
			if item.At.IsZero() || !item.At.Before(since) {
				out = append(out, item)
			}
		}
	}
	return out
}

// maybeRun runs a pass when one is due and allowed. It reports whether a
// pass ran and stored a list.
func (s *interestsService) maybeRun(ctx context.Context, trigger string) bool {
	if s.allowed != nil && !s.allowed() {
		return false
	}
	if !s.running.TryLock() {
		return false
	}
	defer s.running.Unlock()

	now := s.now()
	st := s.store.load()
	// One why-rewrite pass per start, soon after launch: after an upgrade,
	// stored interests whose why is unverified on a re-derived reference stay
	// out of sight until a pass rewrites them from readable evidence.
	rewrite := false
	if !s.rewriteChecked {
		s.rewriteChecked = true
		rewrite = s.rewriteAllowed != nil && s.rewriteAllowed() && whyRewriteDue(st.Interests)
	}
	consolidate, refresh := interestsTiming(st, now)
	consolidate = consolidate && s.mayCall(now, interests.ModeConsolidate)
	refresh = refresh && s.mayCall(now, interests.ModeRefresh)
	if !consolidate && !refresh && !rewrite {
		return false
	}
	window := interests.RefreshWindow
	if consolidate {
		window = interests.ConsolidateWindow
	}
	in := s.collect(now.Add(-window), now)
	if in.PeopleUnknown {
		// Without the people list the names to refuse are missing.
		return false
	}
	items, names := interestItems(in)
	s.rememberNames(names, now)
	mode, ok := interestsMode(st, consolidate, refresh, items)
	forced := false
	if !ok && rewrite && len(items) > 0 && s.mayCall(now, interests.ModeRefresh) {
		// The rewrite pass is a refresh on the light model, due or not; it
		// spends one of the day's calls, once.
		mode, ok, forced = interests.ModeRefresh, true, true
	}
	if !ok {
		return false
	}
	window = interests.RefreshWindow
	model := s.lightModel()
	if mode == interests.ModeConsolidate {
		window = interests.ConsolidateWindow
		model = s.mainModel()
	} else {
		floor := now.Add(-window)
		items = refreshItems(items, floor)
		if !forced {
			if last := parseRFC3339(st.RefreshedAt); len(st.Interests) > 0 && !last.IsZero() && last.Add(-interestsOverlap).After(floor) {
				items = refreshItems(items, last.Add(-interestsOverlap))
			}
		}
	}

	user := interestsUserState{}
	if s.user != nil {
		user = s.user()
	}
	language := ""
	if s.language != nil {
		language = s.language()
	}
	// Stored labels are derived text too. Recheck every dependency before
	// replay; held interests remain local and cannot seed a fresh result.
	readable := func(interests.Interest) bool { return true }
	if s.readFilter != nil {
		readable = s.readFilter()
	}
	held := map[string]bool{}
	readableIDs := map[string]bool{}
	var heldInterests, allowedInterests []interests.Interest
	for _, it := range st.Interests {
		if readable == nil || !readable(it) {
			held[it.ID] = true
			heldInterests = append(heldInterests, it)
		} else {
			readableIDs[it.ID] = true
			allowedInterests = append(allowedInterests, it)
		}
	}
	if forced {
		due := false
		for _, it := range allowedInterests {
			if !it.WhyVerified && interests.HasReDerivedRef(it) {
				due = true
				break
			}
		}
		if !due {
			// Nothing rewriteable is readable; no call is spent.
			return false
		}
	}
	rules := interests.Rules{Names: names, OwnNames: interestsOwnNames, Removed: user.Removed}
	st.Interests = append(interests.Recheck(allowedInterests, rules, user.Pinned), heldInterests...)
	var current []interests.Interest
	for _, it := range st.Interests {
		if !held[it.ID] && !user.RemovedIDs[it.ID] {
			if interests.HasReDerivedRef(it) && !it.WhyVerified {
				// An unverified stored why is not replayed to the model
				// either; the answer writes a fresh one from the evidence.
				it.Why = ""
			}
			current = append(current, it)
		}
	}
	// Removed labels still enforce local acceptance rules, but a label from
	// held or missing evidence must not leak through the model avoid-list.
	removed := user.Removed
	if user.RemovedNames != nil {
		removed = nil
		for id, labels := range user.RemovedNames {
			if readableIDs[id] {
				removed = append(removed, labels...)
			}
		}
		sort.Strings(removed)
	}
	prompt, ids := interests.Prompt(interests.PromptInput{
		Mode: mode, Items: items, Hints: user.Hints, Current: current,
		Removed: removed, OwnNames: interestsOwnNames, Language: language,
	})

	started := time.Now()
	s.countCall(now)
	callCtx, cancel := context.WithTimeout(ctx, interestsModelTimeout)
	raw, err := s.model(callCtx, prompt, model)
	cancel()
	var accepted []interests.Interest
	var refused []interests.Refusal
	if err == nil {
		var ans interests.Answer
		if ans, err = interests.Parse(raw); err == nil {
			s.rememberNames(append(append([]string(nil), names...), ans.People...), now)
			accepted, refused = interests.Accept(ans, ids, items, rules)
			if len(accepted) == 0 {
				err = fmt.Errorf("no interest passed the checks (%d named)", len(ans.Interests))
			}
		}
	}
	if err != nil {
		s.recordFailure(now, mode)
		log.Printf("[interests] %s pass (%s) failed: %v", mode, trigger, err)
		return false
	}
	// The feature may have gone off while the model ran.
	if s.allowed != nil && !s.allowed() {
		return false
	}

	merged := interests.Merge(st.Interests, accepted, interests.MergeOptions{
		Mode: mode, Keep: user.Pinned, Removed: user.RemovedIDs, Held: held, Now: now, Window: window,
	})
	// A pass that could not match a held or reworded twin stored the same
	// interest twice; fold near-duplicates, keeping the id the user chose.
	kept := map[string]bool{}
	for id := range user.Pinned {
		kept[id] = true
	}
	for id := range user.Renamed {
		kept[id] = true
	}
	merged = interests.Dedupe(merged, interests.DedupeOptions{
		Keep: kept, Removed: user.RemovedIDs, Readable: readable,
	})
	// Holding a read does not pin the row. The ordinary storage cap may
	// still evict a whole old interest, without stripping its dependencies.
	ranked := interests.Rank(merged, user.Weights, user.Pinned, now, time.Local)
	stamp := now.UTC().Format(time.RFC3339Nano)
	today := now.In(time.Local).Format("2006-01-02")
	stored, err := s.store.updateUnless(func() bool { return s.enabled != nil && !s.enabled() }, func(file *interestsStateFile) {
		file.Interests = ranked
		file.RefreshedAt = stamp
		if mode == interests.ModeConsolidate {
			file.ConsolidatedAt = stamp
		}
		if file.RunsOn != today {
			file.RunsOn, file.RunsToday = today, 0
		}
		file.RunsToday++
		file.FailedAt, file.Failures = "", 0
		if mode == interests.ModeConsolidate {
			file.ConsolidateFailedAt, file.ConsolidateFailures = "", 0
		}
	})
	if err != nil {
		// The call was made and counted: back off in memory, since the
		// store that would record it is the one failing.
		s.callFailedNow(now, mode)
		log.Printf("[interests] the list could not be stored: %v", err)
		return false
	}
	delete(s.callFailures, mode)
	if !stored {
		return false
	}
	s.rememberFollowing(ctx, ranked)
	if s.changed != nil {
		s.changed()
	}
	log.Printf("[interests] %s pass (%s): %d interests from %d evidence items in %s, %s",
		mode, trigger, len(ranked), len(ids), time.Since(started).Round(time.Second), refusalSummary(refused))
	return true
}

// mayCall reports whether this process may call the model now for a pass
// of this kind: at most interestsCallsPerDay calls a day in all, and none
// while failures of the same kind back off. It holds even when the stored
// state cannot be written.
func (s *interestsService) mayCall(now time.Time, mode interests.Mode) bool {
	if s.callsOn == now.In(time.Local).Format("2006-01-02") && s.calls >= interestsCallsPerDay {
		return false
	}
	failures := s.callFailures[mode]
	if failures == 0 {
		return true
	}
	base, limit := interestsRetryAfter, interestsRetryMax
	if mode == interests.ModeConsolidate {
		base, limit = interestsConsolidateRetryAfter, interestsConsolidateRetryMax
	}
	return now.Sub(s.callFailed[mode]) >= retryWait(failures, base, limit)
}

// callFailedNow counts a failed call of one kind in memory.
func (s *interestsService) callFailedNow(now time.Time, mode interests.Mode) {
	if s.callFailed == nil {
		s.callFailed, s.callFailures = map[interests.Mode]time.Time{}, map[interests.Mode]int{}
	}
	s.callFailed[mode] = now
	s.callFailures[mode]++
}

// countCall counts one model call for today.
func (s *interestsService) countCall(now time.Time) {
	if today := now.In(time.Local).Format("2006-01-02"); s.callsOn != today {
		s.callsOn, s.calls = today, 0
	}
	s.calls++
}

// recordFailure backs the next pass of the same kind off, in the store and
// in memory.
func (s *interestsService) recordFailure(now time.Time, mode interests.Mode) {
	s.callFailedNow(now, mode)
	if _, err := s.store.update(func(file *interestsStateFile) {
		stamp := now.UTC().Format(time.RFC3339Nano)
		if mode == interests.ModeConsolidate {
			file.ConsolidateFailedAt = stamp
			file.ConsolidateFailures++
			return
		}
		file.FailedAt = stamp
		file.Failures++
	}); err != nil {
		log.Printf("[interests] a failure could not be recorded: %v", err)
	}
}

// refusalSummary counts refusals by reason, for the log: never the text.
func refusalSummary(refused []interests.Refusal) string {
	if len(refused) == 0 {
		return "nothing refused"
	}
	counts := map[string]int{}
	for _, r := range refused {
		counts[r.What+" "+r.Reason]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return "refused " + strings.Join(parts, ", ")
}

// start checks a few minutes after launch, then every half hour,
// whether a pass is due.
func (s *interestsService) start(ctx context.Context) {
	go func() {
		timer := time.NewTimer(interestsFirstCheck)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.maybeRun(ctx, "tick")
				timer.Reset(interestsCheckEvery)
			}
		}
	}()
}
