package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Derived views are periodic, not real-time (Bo, 2026-11-12). Capture stays
// real-time; each page projection below is built by one scheduled job and
// kept as a snapshot in memory. A binding returns the snapshot and waits for
// a build only when there is none or a privacy action changed what it may
// show (all waiting callers share that one build). The person's own edits
// and publications rebuild it at once behind the read, and otherwise it is
// rebuilt no more often than its cadence:
//
//   - Daily (the default): About you (portrait, its Today card, the belief
//     list), How you do things, Who you know (overview and stats), plus
//     RefreshDerivedView.
//   - Hourly, where the day moves: the Today day shape, Before you talk,
//     the main-chat waiting items (and the menu bar that reads them) and
//     the day's noticed items.
//
// A snapshot past its cadence is still returned at once and rebuilt in the
// background (snappy first). Snapshots are also saved to disk under a durable
// privacy stamp, so the first visit after a launch paints from the last one
// (derived_views_disk.go; every privacy action deletes them, and the
// YANSU_DERIVED_SNAPSHOTS=memory switch turns this off). Nothing is saved or
// served from a snapshot while Closed Eyes is on: those reads go to the live
// projection, as before.
type derivedViewName string

const (
	derivedPortrait          derivedViewName = "portrait"
	derivedPortraitTodayCard derivedViewName = "portrait-today-card"
	derivedAboutYou          derivedViewName = "about-you"
	derivedKnowHow           derivedViewName = "know-how"
	derivedPeopleOverview    derivedViewName = "people-overview"
	derivedTodayDayShape     derivedViewName = "today-day-shape"
	derivedWaitingItems      derivedViewName = "main-chat-waiting-items"
	derivedPeopleStats       derivedViewName = "people-stats"
	derivedBeforeYouTalk     derivedViewName = "today-before-you-talk"
	derivedDayNoticed        derivedViewName = "day-noticed"
)

const (
	derivedDaily  = 24 * time.Hour
	derivedHourly = time.Hour
	// derivedIdleAfter is how long no page read a snapshot before the
	// scheduler rebuilds due snapshots, so it never competes with a click.
	derivedIdleAfter = 20 * time.Second
	derivedTick      = time.Minute
	// derivedKeysPerView bounds keyed views (day shape by date, waiting
	// items by page); older keys are dropped.
	derivedKeysPerView = 16
)

// derivedViewSpec is a view's cadence and the event that tells open pages a
// scheduled rebuild changed it. The event is a publication: the frontend
// re-reads the snapshot, which never rebuilds it.
type derivedViewSpec struct {
	every time.Duration
	event string
}

const (
	aboutYouUpdatedEvent        = "about-you:updated"
	peopleOverviewUpdatedEvent  = "people:overview-updated"
	mainChatWaitingItemsUpdated = "main-chat:waiting-items-updated"
	// todayBeforeYouTalkUpdatedEvent tells Today a scheduled Before you
	// talk build changed it.
	todayBeforeYouTalkUpdatedEvent = "today-before-you-talk:updated"
	todayDayShapeUpdatedEvent      = "today-dayshape:updated"
	knowledgeUpdatedEvent          = "knowledge:updated"
	derivedViewRefreshedLogLabel   = "[derived]"
)

var derivedViewSpecs = map[derivedViewName]derivedViewSpec{
	derivedPortrait:          {every: derivedDaily, event: portraitEvent},
	derivedPortraitTodayCard: {every: derivedDaily, event: portraitEvent},
	derivedAboutYou:          {every: derivedDaily, event: aboutYouUpdatedEvent},
	derivedKnowHow:           {every: derivedDaily, event: knowledgeUpdatedEvent},
	derivedPeopleOverview:    {every: derivedDaily, event: peopleOverviewUpdatedEvent},
	derivedPeopleStats:       {every: derivedDaily, event: peopleOverviewUpdatedEvent},
	derivedTodayDayShape:     {every: derivedHourly, event: todayDayShapeUpdatedEvent},
	derivedBeforeYouTalk:     {every: derivedHourly, event: todayBeforeYouTalkUpdatedEvent},
	derivedWaitingItems:      {every: derivedHourly, event: mainChatWaitingItemsUpdated},
	// The scheduled build itself emits noticed:updated when the set changed.
	derivedDayNoticed: {every: derivedHourly},
}

// derivedLanguageGeneration advances when the UI language changes; every
// snapshot is in one language.
var derivedLanguageGeneration atomic.Uint64

// derivedMemoryEditGeneration advances when the person edits a belief or a
// how-to (That's me, Not quite, a correction, a save). Background learning
// does not advance it: those changes wait for the daily rebuild.
var derivedMemoryEditGeneration atomic.Uint64

// derivedBackgroundMemoryReasons are the markMemoryDirty reasons that come
// from learning in the background, not from the person. Any other reason is
// treated as the person's own edit, so a missed one costs a rebuild, never a
// stale page after an edit.
var derivedBackgroundMemoryReasons = map[string]bool{
	"memory-generator": true, "memory-generator-update": true, "activity-memory": true, "activity-memory-update": true,
	"activity-knowledge-relearned": true, "assistant-history": true, "assistant-history-relearned": true,
	"knowledge-relearned": true, "shadow-task-learning": true, "initiative-learning": true,
	"communication-observation": true, "dream-archive": true, "dream-restore": true, "know-how-kind": true,
	"meeting-notes-moved": true, "text-import": true, "ai-import": true,
	"translate": true, "translate-version": true, "translate-all-history": true,
	"knowledge-translate": true, "knowledge-translate-version": true, "knowledge-translate-all-history": true,
}

func derivedMemoryReasonIsEdit(reason string) bool { return !derivedBackgroundMemoryReasons[reason] }

// derivedPrivacyStamp is what every snapshot was built under: the privacy
// epoch (withdrawals, forgets, corrections, People removals, Closed Eyes,
// sensitive apps, background listening), the forget markers, Closed Eyes and
// the language. A snapshot from another stamp is never served.
func (a *App) derivedPrivacyStamp() string {
	return fmt.Sprintf("%d|%d|%t|%d", mobileBriefPrivacyEpoch.Load(), memoryTombstoneGeneration.Load(),
		a.IsClosedEyesModeEnabled(), derivedLanguageGeneration.Load())
}

type derivedCell struct {
	view  derivedViewName
	key   string
	every time.Duration
	event string
	build func() (any, error)
	// privacy is the stamp a snapshot must match to be served at all;
	// fresh is the rest (the person's edits, publications). A snapshot whose
	// fresh stamp moved is still served while it is rebuilt behind the read.
	privacy func() string
	fresh   func() string

	buildMu sync.Mutex // one build at a time per snapshot

	mu        sync.Mutex
	has       bool
	raw       []byte
	privacyOf string
	freshOf   string
	at        time.Time
	lastRead  time.Time
}

type derivedViews struct {
	app *App

	mu      sync.Mutex
	cells   map[derivedViewName]map[string]*derivedCell
	order   map[derivedViewName][]string
	started bool
	ready   atomic.Bool // onboarding done and the first screen painted
	stop    context.CancelFunc
	done    chan struct{}
	kick    chan struct{}

	touches  map[derivedViewName]*atomic.Uint64
	lastRead atomic.Int64
	// builds counts builds per view, for tests and the [derived] log.
	builds map[derivedViewName]*atomic.Uint64
	// dayShapeDisconnected is set when a day shape snapshot says no calendar
	// is connected, so the first calendar sync shows the strip at once.
	dayShapeDisconnected atomic.Bool
	privacySeen          string
}

func newDerivedViews(a *App) *derivedViews {
	d := &derivedViews{app: a, cells: map[derivedViewName]map[string]*derivedCell{}, order: map[derivedViewName][]string{},
		kick: make(chan struct{}, 1), touches: map[derivedViewName]*atomic.Uint64{}, builds: map[derivedViewName]*atomic.Uint64{}}
	for view := range derivedViewSpecs {
		d.touches[view] = &atomic.Uint64{}
		d.builds[view] = &atomic.Uint64{}
	}
	return d
}

func (a *App) derivedViews() *derivedViews {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.derived == nil {
		a.derived = newDerivedViews(a)
	}
	return a.derived
}

// active says whether reads are served from snapshots. Before the scheduler
// runs (tests, onboarding) and while Closed Eyes is on, reads are live.
func (d *derivedViews) active() bool {
	if d == nil || !d.ready.Load() {
		return false
	}
	return d.app.pageSnapshotAllowed()
}

func (d *derivedViews) buildCount(view derivedViewName) uint64 {
	if counter := d.builds[view]; counter != nil {
		return counter.Load()
	}
	return 0
}

// touch records the person's own change to a view: the next read builds it
// again, and the scheduler starts that build now.
func (d *derivedViews) touch(views ...derivedViewName) {
	if d == nil {
		return
	}
	for _, view := range views {
		if view == derivedPeopleOverview {
			// Who you know reads the overview and the stats together;
			// Before you talk names people too.
			views = append(views, derivedPeopleStats, derivedBeforeYouTalk)
		}
	}
	for _, view := range views {
		if counter := d.touches[view]; counter != nil {
			counter.Add(1)
		}
	}
	// The saved copies go in the same step: until the rebuild saves, the
	// next launch must not paint what the person just changed.
	derivedDiskDropViews(views...)
	d.wake()
}

func (d *derivedViews) wake() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// touchDerivedViews is the App-level touch; it is a no-op on a nil App.
func (a *App) touchDerivedViews(views ...derivedViewName) {
	if a == nil {
		return
	}
	a.derivedViews().touch(views...)
}

func (d *derivedViews) cell(view derivedViewName, key string, build func() (any, error), extra func() string) *derivedCell {
	d.mu.Lock()
	defer d.mu.Unlock()
	byKey := d.cells[view]
	if byKey == nil {
		byKey = map[string]*derivedCell{}
		d.cells[view] = byKey
	}
	if c := byKey[key]; c != nil {
		return c
	}
	spec := derivedViewSpecs[view]
	touches := d.touches[view]
	c := &derivedCell{view: view, key: key, every: spec.every, event: spec.event, build: build}
	c.privacy = d.app.derivedPrivacyStamp
	c.fresh = func() string {
		stamp := fmt.Sprint(touches.Load())
		if extra != nil {
			stamp += "|" + extra()
		}
		return stamp
	}
	// A launch starts from the saved snapshot. It counts as out of date, so
	// the first read serves it and a rebuild follows behind the read.
	if raw, at, ok := d.app.derivedDiskLoad(view, key); ok {
		c.has, c.raw, c.privacyOf, c.freshOf, c.at = true, raw, c.privacy(), "disk", at
	}
	byKey[key] = c
	d.order[view] = append(d.order[view], key)
	if over := len(d.order[view]) - derivedKeysPerView; over > 0 {
		for _, old := range d.order[view][:over] {
			delete(byKey, old)
		}
		d.order[view] = append([]string(nil), d.order[view][over:]...)
	}
	return c
}

// ensure returns the snapshot, building it when it is missing, built under
// another privacy stamp, or (unless servable) under another fresh stamp or
// older than maxAge. servable is the foreground rule: any snapshot under the
// current privacy stamp will do, so readers queued behind one build take its
// result instead of each building again. changed is true when a build
// replaced an earlier snapshot with a different one.
func (d *derivedViews) ensure(c *derivedCell, maxAge time.Duration, servable bool) (raw []byte, changed bool, err error) {
	c.buildMu.Lock()
	defer c.buildMu.Unlock()
	privacy, fresh := c.privacy(), c.fresh()
	c.mu.Lock()
	if c.has && c.privacyOf == privacy && (servable || (c.freshOf == fresh && time.Since(c.at) < maxAge)) {
		raw = c.raw
		c.mu.Unlock()
		return raw, false, nil
	}
	previous, had := c.raw, c.has
	c.mu.Unlock()
	disk := d.app.derivedDiskToken()
	started := time.Now()
	value, err := c.build()
	d.builds[c.view].Add(1)
	if err != nil {
		return nil, false, err
	}
	raw, err = json.Marshal(value)
	if err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	// The stamp is the one taken before the build: a privacy change during the
	// build leaves this snapshot unserved, and the next read builds again.
	builtAt := time.Now()
	c.has, c.raw, c.privacyOf, c.freshOf, c.at = true, raw, privacy, fresh, builtAt
	c.mu.Unlock()
	log.Printf("%s built view=%s ms=%d", derivedViewRefreshedLogLabel, c.view, time.Since(started).Milliseconds())
	if privacy == d.app.derivedPrivacyStamp() {
		d.app.derivedDiskSave(c.view, c.key, raw, builtAt, disk)
	}
	return raw, had && !bytes.Equal(previous, raw), nil
}

// read serves the snapshot. Only a missing snapshot, or one from before a
// privacy change, is built while the caller waits; every caller waiting then
// shares that one build. A snapshot past its cadence, or one the person's
// edit or a publication made out of date, is served at once and rebuilt
// behind the read; the page's event brings the new one.
func (d *derivedViews) read(c *derivedCell) ([]byte, error) {
	now := time.Now()
	d.lastRead.Store(now.UnixNano())
	privacy := c.privacy()
	c.mu.Lock()
	c.lastRead = now
	if c.has && c.privacyOf == privacy {
		raw, behind := c.raw, now.Sub(c.at) >= c.every || c.freshOf != c.fresh()
		c.mu.Unlock()
		if behind {
			d.wake()
		}
		return raw, nil
	}
	c.mu.Unlock()
	raw, changed, err := d.ensure(c, time.Duration(math.MaxInt64), true)
	if err == nil && changed {
		d.publish(c)
	}
	return raw, err
}

func (d *derivedViews) publish(c *derivedCell) {
	if c.event != "" && d.app.ctx != nil {
		d.app.emitBrowserEvent(c.event)
	}
}

// derivedRead is the binding side: a snapshot when snapshots are active,
// otherwise the live projection.
func derivedRead[T any](a *App, view derivedViewName, key string, build func() (T, error), extra func() string) (T, error) {
	d := a.derivedViews()
	if !d.active() {
		// Before the scheduler runs, the saved snapshot still paints the
		// first visit after a launch; the scheduler's prefetch rebuilds it.
		if raw, _, ok := a.derivedDiskLoad(view, key); ok {
			var out T
			if json.Unmarshal(raw, &out) == nil {
				d.wake()
				return out, nil
			}
		}
		return build()
	}
	c := d.cell(view, key, func() (any, error) { return build() }, extra)
	raw, err := d.read(c)
	var out T
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return build()
	}
	return out, nil
}

// portraitFileStamp names the portrait file's version without reading it: a
// written portrait, That's me, Not quite or a dismissed Today card.
func portraitFileStamp() string {
	path, err := portraitPath()
	if err != nil {
		return "none"
	}
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

// portraitWritten says whether a first portrait exists.
func portraitWritten() bool {
	path, err := portraitPath()
	if err != nil {
		return false
	}
	doc, err := readPortrait(path)
	return err == nil && doc != nil && doc.GeneratedAt > 0
}

func derivedMemoryEditStamp() string { return fmt.Sprint(derivedMemoryEditGeneration.Load()) }

// derivedPortraitStamp fences the portrait views. Before the first portrait
// the file's version is in it, so the learning progress moves as it is
// saved; once a portrait is published only its own publications (touches
// from emitPortraitUpdated, a dismissed card, a seen mark) and the person's
// edits rebuild it, not the writer's ten-minute bookkeeping saves.
func derivedPortraitStamp() string {
	file := portraitFileStamp()
	if portraitPublishedAt(file) {
		return "published|" + derivedMemoryEditStamp()
	}
	return file + "|" + derivedMemoryEditStamp()
}

var portraitPublishedMemo struct {
	sync.Mutex
	file      string
	published bool
}

func portraitPublishedAt(file string) bool {
	portraitPublishedMemo.Lock()
	defer portraitPublishedMemo.Unlock()
	if portraitPublishedMemo.file != file {
		portraitPublishedMemo.file, portraitPublishedMemo.published = file, portraitWritten()
	}
	return portraitPublishedMemo.published
}

// Start runs the scheduler: it waits for onboarding and the first screen,
// prefetches every view once, then rebuilds a view when its cadence is due
// and no page has read a snapshot for derivedIdleAfter, and at once after a
// privacy action or the person's own edit.
func (d *derivedViews) Start(parent context.Context) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return
	}
	d.started = true
	ctx, cancel := context.WithCancel(parent)
	d.stop, d.done = cancel, make(chan struct{})
	done := d.done
	d.mu.Unlock()
	go func() {
		defer close(done)
		d.run(ctx)
	}()
}

func (d *derivedViews) Stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	stop, done := d.stop, d.done
	d.stop = nil
	d.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

func (d *derivedViews) run(ctx context.Context) {
	for !d.ready.Load() {
		if d.admit(ctx) {
			d.ready.Store(true)
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		case <-d.kick:
		}
	}
	d.privacySeen = d.app.derivedPrivacyStamp()
	d.prefetch(ctx)
	ticker := time.NewTicker(derivedTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx, false)
		case <-d.kick:
			d.tick(ctx, true)
		}
	}
}

// admit says whether the scheduler may start: onboarding finished, the first
// screen painted, and snapshots allowed (Closed Eyes off).
func (d *derivedViews) admit(ctx context.Context) bool {
	a := d.app
	a.pageWarm.mu.Lock()
	painted := a.pageWarm.painted
	a.pageWarm.mu.Unlock()
	// The scheduler starts after models_ready (startupServices). With the
	// switch on it does not also wait for the first paint: a window that
	// launches hidden may not paint for minutes, and every first visit
	// before then built its page live.
	if !painted && !derivedDiskEnabled() {
		return false
	}
	admission, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return a.pageFirstReadsAllowed(admission)
}

// prefetch builds every view once, one at a time, so a page's first open
// paints from a snapshot.
func (d *derivedViews) prefetch(ctx context.Context) {
	a := d.app
	today := a.todayDayShapeNow().In(time.Local)
	steps := []func(){
		func() { _, _ = a.GetPortraitTodayCard() },
		func() { _, _ = a.GetTodayDayShape(today.Format("2006-01-02")) },
		func() { _, _ = a.ListMainChatWaitingItems("", chatFirstPageSize) },
		func() { _, _ = a.GetAboutYouPortrait() },
		func() { _, _ = a.GetAboutYou() },
		func() { _, _ = a.ListKnowHow() },
		func() { _, _ = a.ListPeopleOverview() },
		func() { _, _ = a.ListPrincipalsWithStats() },
		func() { _, _ = a.GetTodayBeforeYouTalk() },
		func() { _, _ = a.GetDayNoticed(today.Format("2006-01-02")) },
		func() { _, _ = a.GetTodayDayShape(today.AddDate(0, 0, 1).Format("2006-01-02")) },
	}
	for _, step := range steps {
		if ctx.Err() != nil || !d.active() {
			return
		}
		step()
	}
}

// tick rebuilds what is due. A privacy change drops every snapshot built
// before it (nothing it withdrew stays in memory) and rebuilds them now; the
// person's own edits rebuild their views now; cadence rebuilds wait until no
// page is reading.
func (d *derivedViews) tick(ctx context.Context, kicked bool) {
	if ctx.Err() != nil {
		return
	}
	if !d.app.pageSnapshotAllowed() {
		d.drop(func(*derivedCell) bool { return true })
		derivedDiskDropAll()
		return
	}
	privacy := d.app.derivedPrivacyStamp()
	if privacy != d.privacySeen {
		d.privacySeen = privacy
		d.drop(func(c *derivedCell) bool { return true })
		derivedDiskDropAll()
		d.app.markMainChatWaitingDirty("privacy:derived")
		d.prefetch(ctx)
		return
	}
	idle := time.Since(time.Unix(0, d.lastRead.Load())) >= derivedIdleAfter
	for _, c := range d.allCells() {
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		has, privacyOf, freshOf, at := c.has, c.privacyOf, c.freshOf, c.at
		c.mu.Unlock()
		if !has {
			continue
		}
		fenced := privacyOf != c.privacy() || freshOf != c.fresh()
		due := time.Since(at) >= c.every
		if !fenced && !(due && (idle || kicked)) {
			continue
		}
		maxAge := c.every
		if fenced {
			maxAge = time.Duration(math.MaxInt64)
		}
		if _, changed, err := d.ensure(c, maxAge, false); err == nil && changed {
			d.publish(c)
		}
	}
}

func (d *derivedViews) allCells() []*derivedCell {
	d.mu.Lock()
	defer d.mu.Unlock()
	views := make([]string, 0, len(d.cells))
	for view := range d.cells {
		views = append(views, string(view))
	}
	sort.Strings(views)
	var out []*derivedCell
	for _, view := range views {
		for _, key := range d.order[derivedViewName(view)] {
			if c := d.cells[derivedViewName(view)][key]; c != nil {
				out = append(out, c)
			}
		}
	}
	return out
}

func (d *derivedViews) drop(match func(*derivedCell) bool) {
	for _, c := range d.allCells() {
		if !match(c) {
			continue
		}
		c.mu.Lock()
		c.has, c.raw, c.privacyOf, c.freshOf = false, nil, "", ""
		c.mu.Unlock()
	}
}

// noteDerivedPrivacyChanged is called after every privacy action: the
// scheduler drops and rebuilds the snapshots now. Reads are fenced anyway.
func noteDerivedPrivacyChanged() {
	derivedDiskDropAll()
	if live := derivedViewsLive.Load(); live != nil {
		live.wake()
	}
}

// noteDerivedViewEvent maps the few app events that mean the person changed
// a view, or that a view's set really changed, to a rebuild. Source events
// (memory, calendar, mail, activity) are not here: they wait for the cadence.
func (a *App) noteDerivedViewEvent(eventName string) {
	d := a.derivedViews()
	if !d.active() {
		return
	}
	switch eventName {
	case mainChatWaitingChangedEvent:
		// The waiting set changed (answered, dismissed, expired or new): the
		// holder only emits this when its ids or counts differ.
		d.touch(derivedWaitingItems)
	case "brief:stored":
		// A stored brief can carry the closing question's hold preview.
		d.touch(derivedTodayDayShape)
	case "calendar:synced":
		// Only the first sync after connecting shows the strip at once.
		if d.dayShapeDisconnected.CompareAndSwap(true, false) {
			d.touch(derivedTodayDayShape)
		}
	case "contacts:updated":
		d.touch(derivedPeopleOverview)
	}
}

// derivedViewsLive is the running app's scheduler, for package-level privacy
// hooks such as briefPrivacyChanged.
var derivedViewsLive atomic.Pointer[derivedViews]

// RefreshDerivedView rebuilds one page's snapshot now (the manual refresh for
// About you and How you do things). Names: "about-you" (portrait, its Today
// card and the belief list), "know-how", "people" (overview and stats),
// "today" (day shape, Before you talk, noticed), "waiting".
func (a *App) RefreshDerivedView(name string) error {
	views := map[string][]derivedViewName{
		"about-you": {derivedPortrait, derivedPortraitTodayCard, derivedAboutYou},
		"know-how":  {derivedKnowHow},
		"people":    {derivedPeopleOverview, derivedPeopleStats},
		"today":     {derivedTodayDayShape, derivedBeforeYouTalk, derivedDayNoticed},
		"waiting":   {derivedWaitingItems},
	}[name]
	if len(views) == 0 {
		return fmt.Errorf("unknown view")
	}
	a.touchDerivedViews(views...)
	return nil
}
