package main

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/storage"
)

// What Isoform is doing right now, in plain words: details in the sidebar's
// ObservingStatus popover. The menu bar derives its observation state from
// the same snapshot (observation_menu.go). It is read from state the app already keeps and
// recomputed on the events that change that state (doingNowNudgeEvents) and
// every doingNowPollInterval for the state no event announces (a meeting
// starting, a chat turn running).
//
// The menu bar never names a task: it is on screen outside the app, in
// screen shares and in Isoform's own screen reading. Only the popover
// inside the window shows the task title.

const (
	// doingNowChangedEvent carries the new DoingNow to the frontend.
	doingNowChangedEvent = "doing-now:changed"
	// activityOpenEvent asks the frontend to open the Activity log.
	activityOpenEvent = "activity:open"
	// doingNowPollInterval is the slowest the line can lag behind state that
	// no event announces.
	doingNowPollInterval = 5 * time.Second
	// doingNowTitleMaxRunes keeps a task title from widening the tooltip.
	doingNowTitleMaxRunes = 40
	// doingNowLookupTimeout bounds a chat lookup, so a busy database delays
	// the line by at most this much.
	doingNowLookupTimeout = time.Second
)

// doingNowNudgeEvents are the existing events after which the line is
// recomputed at once instead of on the next poll.
var doingNowNudgeEvents = map[string]bool{
	closedEyesModeChangedEvent:     true,
	"background-listening:changed": true,
	"activity:status":              true,
	"activity:session-saved":       true,
	"calendar:synced":              true,
	"handoff:updated":              true,
}

type doingNowKind string

const (
	doingNowPaused    doingNowKind = "paused"
	doingNowMeeting   doingNowKind = "meeting"
	doingNowCalendar  doingNowKind = "calendar"
	doingNowWorking   doingNowKind = "working"
	doingNowWatching  doingNowKind = "watching"
	doingNowListening doingNowKind = "listening"
	doingNowIdle      doingNowKind = "idle"
)

// doingNowKinds lists every kind; the copy catalogs (Go and the frontend
// locales) must cover each one.
var doingNowKinds = []doingNowKind{
	doingNowPaused, doingNowMeeting, doingNowCalendar, doingNowWorking,
	doingNowWatching, doingNowListening, doingNowIdle,
}

// DoingNow is what Isoform is doing right now. App is the meeting app of a
// "meeting", Title the task of "working"; either may be empty.
type DoingNow struct {
	Kind           doingNowKind `json:"kind"`
	App            string       `json:"app,omitempty"`
	Title          string       `json:"title,omitempty"`
	PausedUntil    int64        `json:"pausedUntil"`
	FrontApp       string       `json:"frontApp"`
	FrontAppHidden bool         `json:"frontAppHidden"`
	Microphone     string       `json:"microphone"`
	InCall         bool         `json:"inCall"`
}

// doingNowInputs is the state the line is chosen from.
type doingNowInputs struct {
	paused          bool
	inMeeting       bool
	meetingApp      string
	readingCalendar bool
	working         bool
	workTitle       string
	watching        bool
	listening       bool
}

// selectDoingNow picks the one thing to say. A pause outranks everything; a
// call being recorded and a calendar being read come before running work
// because they are what the user can notice from outside the app.
func selectDoingNow(in doingNowInputs) DoingNow {
	if in.paused {
		return DoingNow{Kind: doingNowPaused}
	}
	if in.inMeeting {
		return DoingNow{Kind: doingNowMeeting, App: strings.TrimSpace(in.meetingApp)}
	}
	if in.readingCalendar {
		return DoingNow{Kind: doingNowCalendar}
	}
	if in.working {
		return DoingNow{Kind: doingNowWorking, Title: shortenDoingNowTitle(in.workTitle)}
	}
	if in.watching {
		return DoingNow{Kind: doingNowWatching}
	}
	if in.listening {
		return DoingNow{Kind: doingNowListening}
	}
	return DoingNow{Kind: doingNowIdle}
}

func shortenDoingNowTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) <= doingNowTitleMaxRunes {
		return title
	}
	return strings.TrimSpace(string(runes[:doingNowTitleMaxRunes-1])) + "…"
}

// doingNowTracker runs the refresh loop and remembers what was last shown.
// The zero value is ready; startDoingNow starts the loop.
type doingNowTracker struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	// done is closed when the loop started last has returned.
	done     chan struct{}
	nudges   chan struct{}
	last     DoingNow
	lastText string
	// titles caches the task title of each running chat turn by its tag
	// (see workTitle).
	titles map[string]string
	// lang is the UI language of the menu-bar line, read from state.db once
	// and again after it changes (noteUILanguageChanged); "" while unread.
	// langGen counts the changes, so a read that raced one is not kept.
	lang    string
	langGen uint64
}

// GetDoingNow returns what Isoform is doing right now. Wails binding,
// read-only.
func (a *App) GetDoingNow() (perfValue DoingNow) {
	defer logPageBinding("GetDoingNow", time.Now(), &perfValue)
	if a == nil {
		return DoingNow{Kind: doingNowIdle, Microphone: "off"}
	}
	in := a.doingNowInputs()
	view := selectDoingNow(in)
	var front activitymon.AppInfo
	if !in.paused && (view.Kind == doingNowWatching || view.Kind == doingNowWorking) {
		front = activitymon.FrontmostAppInfo()
	}
	status := a.observationAudioStatus(in.paused)
	// Recheck after native probes; a pause suppresses the additive app data.
	pause := a.observationPauseSnapshot()
	if pause.enabled {
		view = DoingNow{Kind: doingNowPaused}
		if !pause.resumeAt.IsZero() {
			view.PausedUntil = pause.resumeAt.UnixMilli()
		}
	}
	return observationDoingNow(view, front, status, in.inMeeting)
}

// startDoingNow starts the refresh loop. It runs once from startup. A loop
// still stopping is waited for first, so two loops never send at once.
func (a *App) startDoingNow(ctx context.Context) {
	if a == nil {
		return
	}
	t := &a.doingNow
	t.mu.Lock()
	if t.cancel != nil {
		t.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	previous := t.done
	done := make(chan struct{})
	t.cancel = cancel
	t.done = done
	t.nudges = make(chan struct{}, 1)
	nudges := t.nudges
	t.mu.Unlock()
	go func() {
		defer close(done)
		if previous != nil {
			<-previous
		}
		a.runDoingNow(ctx, nudges)
	}()
}

// stopDoingNow stops the loop and returns once it has, so nothing is sent
// after it returns.
func (a *App) stopDoingNow() {
	if a == nil {
		return
	}
	t := &a.doingNow
	t.mu.Lock()
	cancel := t.cancel
	done := t.done
	t.cancel = nil
	t.nudges = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// doingNowResting reports the screen locked or the display asleep. Nobody can
// see the menu bar then, so the poll skips its ticks until it wakes. The
// first refresh and a nudge (an event that changed the line) always run.
var doingNowResting = func() bool { return activitymon.IsScreenLocked() || activitymon.IsDisplayAsleep() }

// doingNowTickEvery is the poll's tick; tests shorten it.
var doingNowTickEvery = doingNowPollInterval

func (a *App) runDoingNow(ctx context.Context, nudges <-chan struct{}) {
	ticker := time.NewTicker(doingNowTickEvery)
	defer ticker.Stop()
	a.refreshDoingNow()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if doingNowResting() {
				continue
			}
		case <-nudges:
		}
		// A stop that lands with a tick or a nudge must still win.
		if ctx.Err() != nil {
			return
		}
		a.refreshDoingNow()
	}
}

// noteDoingNowEvent is called for every event the app emits. It asks the
// loop for an early refresh after the events that change the line. It never
// blocks: a refresh already asked for covers this one too.
func (a *App) noteDoingNowEvent(eventName string) {
	a.noteMenuBarEvent(eventName)
	if a == nil || !doingNowNudgeEvents[eventName] {
		return
	}
	a.nudgeDoingNow()
}

// nudgeDoingNow asks the loop for an early refresh, without blocking.
func (a *App) nudgeDoingNow() {
	t := &a.doingNow
	t.mu.Lock()
	nudges := t.nudges
	t.mu.Unlock()
	if nudges == nil {
		return
	}
	select {
	case nudges <- struct{}{}:
	default:
	}
}

// noteUILanguageChanged is called after the UI language is saved: the
// menu-bar line is read in the new language at once.
func (a *App) noteUILanguageChanged() {
	if a == nil {
		return
	}
	derivedLanguageGeneration.Add(1)
	a.derivedViews().wake()
	t := &a.doingNow
	t.mu.Lock()
	t.lang = ""
	t.langGen++
	t.mu.Unlock()
	a.nudgeDoingNow()
	updateMenuBarClosedEyesMode(a.IsClosedEyesModeEnabled())
}

// doingNowLanguage is the UI language of the menu-bar line. It reads
// state.db only when the cache is empty, not on every refresh; a failed read
// is tried again on the next one.
func (a *App) doingNowLanguage() string {
	t := &a.doingNow
	t.mu.Lock()
	lang, gen := t.lang, t.langGen
	t.mu.Unlock()
	if lang != "" {
		return lang
	}
	if a.store == nil {
		return "en"
	}
	value, err := a.store.GetUIState(context.Background(), uiLanguageStateKey)
	if err != nil {
		return "en"
	}
	lang = normalizeUILanguage(value)
	t.mu.Lock()
	if t.langGen == gen {
		t.lang = lang
	}
	t.mu.Unlock()
	return lang
}

// refreshDoingNow recomputes the line and passes a change on to the menu bar
// and the frontend. Only the loop calls it, so changes go out in order.
func (a *App) refreshDoingNow() {
	a.refreshMenuBarPresentation()
	view := a.GetDoingNow()
	text := observingMenuText(view, a.isActivityMonitorRunning() || a.backgroundListeningRunningNow(), a.doingNowLanguage())
	t := &a.doingNow
	t.mu.Lock()
	viewChanged := view != t.last
	textChanged := text != t.lastText
	t.last = view
	t.lastText = text
	t.mu.Unlock()
	if textChanged {
		updateMenuBarDoingNow(text)
	}
	if viewChanged {
		a.emitBrowserEvent(doingNowChangedEvent, view)
	}
}

// doingNowInputs reads the state. Each part takes only its own short lock.
func (a *App) doingNowInputs() doingNowInputs {
	in := doingNowInputs{paused: a.IsClosedEyesModeEnabled()}
	if in.paused {
		return in
	}
	if am := a.activity; am != nil {
		am.mu.Lock()
		in.inMeeting = am.inMeeting
		in.meetingApp = am.meetingAppName
		am.mu.Unlock()
	}
	in.readingCalendar = a.calendarPeek.isPeeking() || a.calendarSync.isSyncing()
	in.working, in.workTitle = a.runningWork()
	in.watching = a.isActivityMonitorRunning()
	in.listening = a.backgroundListeningRunningNow()
	return in
}

// backgroundListeningRunningNow is isBackgroundListeningRunning without
// backgroundAudioMu, which stopping listening holds across a whole drain of
// queued transcription; the line must not wait for that.
func (a *App) backgroundListeningRunningNow() bool {
	a.mu.Lock()
	capture := a.backgroundAudio
	a.mu.Unlock()
	return capture != nil && capture.isRunning()
}

func (s *calendarPeekService) isPeeking() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *calendarSyncService) isSyncing() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncing
}

// runningWork reports whether a chat turn is running (one-time work runs as
// one too) and the title of one, preferring a turn whose title is known.
func (a *App) runningWork() (bool, string) {
	a.mu.Lock()
	tags := make([]string, 0, len(a.activeTags))
	for tag, active := range a.activeTags {
		if active {
			tags = append(tags, tag)
		}
	}
	a.mu.Unlock()
	if len(tags) == 0 {
		a.doingNow.mu.Lock()
		a.doingNow.titles = nil
		a.doingNow.mu.Unlock()
		return false, ""
	}
	sort.Strings(tags)
	return true, a.workTitle(tags)
}

// workTitle returns the title of the first tag that has one. Titles are
// cached per tag while its turn runs, so the poll reads the stores once per
// turn (the handoff store reads every one-time work file); a chat still
// without a name is read again on the next poll, since a new chat is named
// after its first turn starts.
func (a *App) workTitle(tags []string) string {
	t := &a.doingNow
	t.mu.Lock()
	known := make(map[string]string, len(tags))
	var unknown []string
	for _, tag := range tags {
		if title, ok := t.titles[tag]; ok {
			known[tag] = title
		} else {
			unknown = append(unknown, tag)
		}
	}
	t.mu.Unlock()

	final := map[string]string{}
	for _, tag := range unknown {
		title, settled := a.lookUpWorkTitle(tag)
		known[tag] = title
		if settled {
			final[tag] = title
		}
	}
	if len(unknown) > 0 {
		t.mu.Lock()
		// Keep only the running turns, so the cache never outgrows them.
		next := make(map[string]string, len(tags))
		for _, tag := range tags {
			if title, ok := t.titles[tag]; ok {
				next[tag] = title
			}
			if title, ok := final[tag]; ok {
				next[tag] = title
			}
		}
		t.titles = next
		t.mu.Unlock()
	}

	for _, tag := range tags {
		if title := known[tag]; title != "" {
			return title
		}
	}
	return ""
}

// handoffSessionLabelPrefix starts the name of a chat that runs one-time
// work (createHandoffExecutionSession).
const handoffSessionLabelPrefix = "Handoff: "

// lookUpWorkTitle names the work a chat turn is doing: the one-time work it
// runs, else the chat's name. The main chat and turns that are not a saved
// chat have no title. settled is false while a chat has no name yet.
func (a *App) lookUpWorkTitle(tag string) (title string, settled bool) {
	if a.store == nil {
		return "", true
	}
	ctx, cancel := context.WithTimeout(context.Background(), doingNowLookupTimeout)
	defer cancel()
	row, err := a.store.LoadSession(ctx, tag)
	if err != nil {
		return "", false
	}
	if row == nil || row.Kind == storage.SessionKindMain {
		return "", true
	}
	label := strings.TrimSpace(row.Label)
	if strings.HasPrefix(label, handoffSessionLabelPrefix) {
		// The chat name is cut at 60 bytes; the one-time work keeps the
		// whole title.
		if title := a.inProgressHandoffTitle(tag); title != "" {
			return title, true
		}
		return strings.TrimSpace(strings.TrimPrefix(label, handoffSessionLabelPrefix)), true
	}
	return label, label != ""
}

func (a *App) inProgressHandoffTitle(chatSessionID string) string {
	if a.handoffStore == nil {
		return ""
	}
	entries, err := a.handoffStore.List()
	if err != nil {
		log.Printf("[doing-now] list one-time work: %v", err)
		return ""
	}
	for _, entry := range entries {
		if entry.ChatSessionID == chatSessionID && entry.Status == handoff.StatusInProgress {
			return strings.TrimSpace(entry.Title)
		}
	}
	return ""
}
