package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// derivedTestApp is an app whose page snapshots are on, as after onboarding
// and the first screen in the running app.
func derivedTestApp(t *testing.T) (*App, *derivedViews) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	d := app.derivedViews()
	d.ready.Store(true)
	d.privacySeen = app.derivedPrivacyStamp()
	return app, d
}

type derivedTestValue struct {
	Items []string `json:"items"`
}

func derivedTestCell(d *derivedViews, view derivedViewName, build func() (derivedTestValue, error)) *derivedCell {
	return d.cell(view, "test", func() (any, error) { return build() }, nil)
}

func readDerivedTestValue(t *testing.T, app *App, view derivedViewName, build func() (derivedTestValue, error)) derivedTestValue {
	t.Helper()
	value, err := derivedRead(app, view, "test", build, nil)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDerivedViewReadsDoNotRebuild(t *testing.T) {
	app, _ := derivedTestApp(t)
	var builds atomic.Int64
	build := func() (derivedTestValue, error) {
		builds.Add(1)
		return derivedTestValue{Items: []string{"one"}}, nil
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			readDerivedTestValue(t, app, derivedPeopleOverview, build)
		}()
	}
	wg.Wait()
	for range 200 {
		readDerivedTestValue(t, app, derivedPeopleOverview, build)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("builds = %d after 250 reads, want 1", got)
	}
	// A caller that changes what it was given does not change the snapshot.
	value := readDerivedTestValue(t, app, derivedPeopleOverview, build)
	value.Items[0] = "changed"
	if again := readDerivedTestValue(t, app, derivedPeopleOverview, build); again.Items[0] != "one" {
		t.Fatalf("snapshot = %v, want it unchanged by a caller", again.Items)
	}
}

func TestDerivedViewKnowHowBindingBuildsOnceAndOnEdit(t *testing.T) {
	app, d := derivedTestApp(t)
	app.knowHow.counter = &knowHowFakeCounter{}
	ts := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	writeKnowHowFixture(t, "first", buildOKFKnowledgeMarkdown("first", "First fix", "Restart it", ts, "activity", nil, nil, nil, "fix"))
	for range 20 {
		cards, err := app.ListKnowHow()
		if err != nil || len(cards) != 1 {
			t.Fatalf("ListKnowHow = %d cards, %v", len(cards), err)
		}
	}
	if got := d.buildCount(derivedKnowHow); got != 1 {
		t.Fatalf("know-how builds = %d after 20 reads, want 1", got)
	}
	// Background learning waits for the daily build.
	writeKnowHowFixture(t, "second", buildOKFKnowledgeMarkdown("second", "Second fix", "Clear it", ts, "activity", nil, nil, nil, "fix"))
	app.markMemoryDirty("activity-memory")
	if cards, _ := app.ListKnowHow(); len(cards) != 1 || d.buildCount(derivedKnowHow) != 1 {
		t.Fatalf("background change rebuilt: %d cards, %d builds", len(cards), d.buildCount(derivedKnowHow))
	}
	// The person's own save is served from the old snapshot while the
	// scheduler rebuilds it at once (its event brings the new one).
	app.markMemoryDirty("knowledge-save")
	if cards, _ := app.ListKnowHow(); len(cards) != 1 || d.buildCount(derivedKnowHow) != 1 {
		t.Fatalf("an edit blocked the read: %d cards, %d builds", len(cards), d.buildCount(derivedKnowHow))
	}
	d.tick(context.Background(), true)
	if cards, _ := app.ListKnowHow(); len(cards) != 2 || d.buildCount(derivedKnowHow) != 2 {
		t.Fatalf("after an edit: %d cards, %d builds, want 2 and 2", len(cards), d.buildCount(derivedKnowHow))
	}
}

func TestDerivedViewCadence(t *testing.T) {
	cases := []struct {
		view        derivedViewName
		notDue, due time.Duration
	}{
		{derivedTodayDayShape, 50 * time.Minute, 61 * time.Minute},
		{derivedBeforeYouTalk, 50 * time.Minute, 61 * time.Minute},
		{derivedWaitingItems, 50 * time.Minute, 61 * time.Minute},
		{derivedDayNoticed, 50 * time.Minute, 61 * time.Minute},
		{derivedPeopleOverview, 23 * time.Hour, 25 * time.Hour},
		{derivedPeopleStats, 23 * time.Hour, 25 * time.Hour},
		{derivedAboutYou, 23 * time.Hour, 25 * time.Hour},
		{derivedKnowHow, 23 * time.Hour, 25 * time.Hour},
		{derivedPortrait, 23 * time.Hour, 25 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(string(tc.view), func(t *testing.T) {
			_, d := derivedTestApp(t)
			var builds atomic.Int64
			c := derivedTestCell(d, tc.view, func() (derivedTestValue, error) {
				return derivedTestValue{Items: []string{fmt.Sprint(builds.Add(1))}}, nil
			})
			if _, err := d.read(c); err != nil {
				t.Fatal(err)
			}
			age := func(by time.Duration) {
				c.mu.Lock()
				c.at = time.Now().Add(-by)
				c.mu.Unlock()
			}
			d.lastRead.Store(time.Now().Add(-time.Hour).UnixNano()) // idle
			age(tc.notDue)
			d.tick(context.Background(), false)
			if builds.Load() != 1 {
				t.Fatalf("rebuilt after %v, before its cadence", tc.notDue)
			}
			// Past the cadence a read is served at once, from the snapshot.
			age(tc.due)
			if _, err := d.read(c); err != nil || builds.Load() != 1 {
				t.Fatalf("a stale read built in the foreground: builds %d, %v", builds.Load(), err)
			}
			// The scheduler rebuilds it once, when no page is reading.
			d.lastRead.Store(time.Now().Add(-time.Hour).UnixNano())
			d.tick(context.Background(), false)
			d.tick(context.Background(), false)
			if builds.Load() != 2 {
				t.Fatalf("builds = %d after the cadence, want 2", builds.Load())
			}
		})
	}
}

func TestDerivedViewWaitsForIdleBeforeCadenceRebuild(t *testing.T) {
	_, d := derivedTestApp(t)
	var builds atomic.Int64
	c := derivedTestCell(d, derivedPeopleOverview, func() (derivedTestValue, error) {
		builds.Add(1)
		return derivedTestValue{}, nil
	})
	_, _ = d.read(c)
	c.mu.Lock()
	c.at = time.Now().Add(-25 * time.Hour)
	c.mu.Unlock()
	d.lastRead.Store(time.Now().UnixNano()) // a page just read
	d.tick(context.Background(), false)
	if builds.Load() != 1 {
		t.Fatal("a due rebuild ran while a page was reading")
	}
}

func TestDerivedViewPrivacyActionFencesSnapshot(t *testing.T) {
	app, d := derivedTestApp(t)
	var withdrawn atomic.Bool
	var builds atomic.Int64
	build := func() (derivedTestValue, error) {
		builds.Add(1)
		if withdrawn.Load() {
			return derivedTestValue{Items: []string{}}, nil
		}
		return derivedTestValue{Items: []string{"private-source"}}, nil
	}
	if got := readDerivedTestValue(t, app, derivedAboutYou, build); len(got.Items) != 1 {
		t.Fatalf("first read = %v", got.Items)
	}
	// A withdrawal: the next read never returns the old snapshot.
	withdrawn.Store(true)
	briefPrivacyChanged()
	if got := readDerivedTestValue(t, app, derivedAboutYou, build); len(got.Items) != 0 {
		t.Fatalf("after a withdrawal = %v, want the source gone", got.Items)
	}
	// The scheduler drops every snapshot from before the change.
	withdrawn.Store(false)
	_ = readDerivedTestValue(t, app, derivedPeopleOverview, build)
	d.privacySeen = app.derivedPrivacyStamp()
	withdrawn.Store(true)
	memoryTombstoneGeneration.Add(1)
	d.tick(context.Background(), true)
	for _, c := range d.allCells() {
		c.mu.Lock()
		has, stamp := c.has, c.privacyOf
		c.mu.Unlock()
		if has && stamp != c.privacy() {
			t.Fatalf("%s kept a snapshot from before the forget", c.view)
		}
	}
	if got := readDerivedTestValue(t, app, derivedPeopleOverview, build); len(got.Items) != 0 {
		t.Fatalf("after a forget = %v", got.Items)
	}
}

func TestDerivedViewClosedEyesReadsLive(t *testing.T) {
	app, d := derivedTestApp(t)
	var builds atomic.Int64
	build := func() (derivedTestValue, error) {
		builds.Add(1)
		return derivedTestValue{}, nil
	}
	readDerivedTestValue(t, app, derivedKnowHow, build)
	app.closedEyes.enabled.Store(true)
	readDerivedTestValue(t, app, derivedKnowHow, build)
	readDerivedTestValue(t, app, derivedKnowHow, build)
	if builds.Load() != 3 {
		t.Fatalf("builds = %d, want every Closed Eyes read live", builds.Load())
	}
	d.tick(context.Background(), true)
	for _, c := range d.allCells() {
		c.mu.Lock()
		has := c.has
		c.mu.Unlock()
		if has {
			t.Fatalf("%s kept a snapshot while Closed Eyes is on", c.view)
		}
	}
}

func TestDerivedViewPeopleEditRebuildsAtOnce(t *testing.T) {
	app, d := derivedTestApp(t)
	name := "Ada"
	build := func() (derivedTestValue, error) { return derivedTestValue{Items: []string{name}}, nil }
	readDerivedTestValue(t, app, derivedPeopleOverview, build)
	name = "Ada Lovelace"
	if got := readDerivedTestValue(t, app, derivedPeopleOverview, build); got.Items[0] != "Ada" {
		t.Fatalf("a source change showed before the hourly build: %v", got.Items)
	}
	d.touch(derivedPeopleOverview) // UpdatePrincipal, MergePeople, ...
	d.tick(context.Background(), true)
	if got := readDerivedTestValue(t, app, derivedPeopleOverview, build); got.Items[0] != "Ada Lovelace" {
		t.Fatalf("the person's edit did not show: %v", got.Items)
	}
}

// A snapshot read stays under 100 ms while a build of the same view, and a
// writer to the databases, run in the background.
func TestDerivedViewReadUnder100msWithBackgroundWriter(t *testing.T) {
	app, d := derivedTestApp(t)
	release := make(chan struct{})
	var slow atomic.Bool
	build := func() (derivedTestValue, error) {
		if slow.Load() {
			<-release
		}
		items := make([]string, 500)
		for i := range items {
			items[i] = fmt.Sprintf("person %d with a longer display line", i)
		}
		return derivedTestValue{Items: items}, nil
	}
	readDerivedTestValue(t, app, derivedPeopleOverview, build)
	c := derivedTestCell(d, derivedPeopleOverview, build)
	slow.Store(true)
	c.mu.Lock()
	c.at = time.Now().Add(-25 * time.Hour)
	c.mu.Unlock()
	go func() { _, _, _ = d.ensure(c, derivedDaily, false) }() // a scheduled build holding the view

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		dir := filepath.Join(t.TempDir(), "writer")
		_ = os.MkdirAll(dir, 0700)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = app.store.SetUIState(context.Background(), fmt.Sprintf("derived-test-%d", i%8), fmt.Sprint(i))
			_ = os.WriteFile(filepath.Join(dir, "f"), []byte(fmt.Sprint(i)), 0600)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	var worst time.Duration
	for range 200 {
		started := time.Now()
		value := readDerivedTestValue(t, app, derivedPeopleOverview, build)
		if took := time.Since(started); took > worst {
			worst = took
		}
		if len(value.Items) != 500 {
			t.Fatalf("read %d items", len(value.Items))
		}
	}
	close(stop)
	close(release)
	<-writerDone
	t.Logf("worst snapshot read %v", worst)
	if worst >= 100*time.Millisecond {
		t.Fatalf("worst snapshot read %v, want under 100 ms", worst)
	}
}

func TestDerivedViewsOffBeforeTheScheduler(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	var builds atomic.Int64
	build := func() (derivedTestValue, error) {
		builds.Add(1)
		return derivedTestValue{}, nil
	}
	readDerivedTestValue(t, app, derivedKnowHow, build)
	readDerivedTestValue(t, app, derivedKnowHow, build)
	if builds.Load() != 2 {
		t.Fatalf("builds = %d, want live reads before the scheduler admits snapshots", builds.Load())
	}
}

func TestDerivedMemoryReasons(t *testing.T) {
	for _, reason := range []string{"confirm", "unconfirm", "not_quite", "save", "delete", "forget", "knowledge-save", "knowhow-confirm", "wording_undo", "reassign"} {
		if !derivedMemoryReasonIsEdit(reason) {
			t.Fatalf("%s is the person's edit", reason)
		}
	}
	for _, reason := range []string{"memory-generator", "activity-memory", "assistant-history", "shadow-task-learning", "know-how-kind"} {
		if derivedMemoryReasonIsEdit(reason) {
			t.Fatalf("%s is background learning", reason)
		}
	}
}

// A deleted how-to (a withdrawal) never shows from the snapshot.
func TestDerivedViewKnowHowDeleteFencesSnapshot(t *testing.T) {
	app, d := derivedTestApp(t)
	app.knowHow.counter = &knowHowFakeCounter{}
	ts := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	writeKnowHowFixture(t, "keep", buildOKFKnowledgeMarkdown("keep", "Keep fix", "Keep", ts, "activity", nil, nil, nil, "fix"))
	writeKnowHowFixture(t, "gone", buildOKFKnowledgeMarkdown("gone", "Gone fix", "Gone", ts, "activity", nil, nil, nil, "fix"))
	if cards, err := app.ListKnowHow(); err != nil || len(cards) != 2 {
		t.Fatalf("ListKnowHow = %d, %v", len(cards), err)
	}
	if err := app.DeleteKnowledgeEntry("gone"); err != nil {
		t.Fatal(err)
	}
	cards, err := app.ListKnowHow()
	if err != nil || len(cards) != 1 || cards[0].ID != "keep" {
		t.Fatalf("after delete = %+v, %v", cards, err)
	}
	if d.buildCount(derivedKnowHow) != 2 {
		t.Fatalf("builds = %d, want 2", d.buildCount(derivedKnowHow))
	}
}

// TestDerivedViewTimingsBeforeAfter measures the About you belief list and
// How you do things on 300 beliefs and 200 how-tos: the live projection
// (before) against a snapshot read (after), with a writer marking memory
// dirty as capture does. It logs both; it fails only if a snapshot read is
// not under 100 ms.
func TestDerivedViewTimingsBeforeAfter(t *testing.T) {
	app, d := derivedTestApp(t)
	app.knowHow.counter = &knowHowFakeCounter{}
	ts := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	for i := range 300 {
		id := fmt.Sprintf("belief-%03d", i)
		writeMemoryFixture(t, id, fmt.Sprintf("Belief number %d about how you work", i))
	}
	for i := range 200 {
		id := fmt.Sprintf("howto-%03d", i)
		writeKnowHowFixture(t, id, buildOKFKnowledgeMarkdown(id, fmt.Sprintf("How-to %d", i), "Steps", ts, "activity", []string{"Terminal"}, []string{"one", "two"}, []string{"a", "b"}, "fix"))
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				app.markMemoryDirty("activity-memory")
			}
		}
	}()
	measure := func(name string, read func() error) (live, snap time.Duration) {
		d.ready.Store(false)
		started := time.Now()
		for range 5 {
			if err := read(); err != nil {
				t.Fatal(err)
			}
		}
		live = time.Since(started) / 5
		d.ready.Store(true)
		_ = read() // the one build
		started = time.Now()
		for range 50 {
			if err := read(); err != nil {
				t.Fatal(err)
			}
		}
		snap = time.Since(started) / 50
		t.Logf("%s: live %v, snapshot %v", name, live, snap)
		return live, snap
	}
	_, aboutSnap := measure("GetAboutYou", func() error { _, err := app.GetAboutYou(); return err })
	_, knowSnap := measure("ListKnowHow", func() error { _, err := app.ListKnowHow(); return err })
	close(stop)
	<-done
	if aboutSnap >= 100*time.Millisecond || knowSnap >= 100*time.Millisecond {
		t.Fatalf("snapshot reads %v and %v, want under 100 ms", aboutSnap, knowSnap)
	}
}

// The knowledge list is parsed again only after a real file change.
func TestKnowledgeEntriesParsedOnlyOnFileChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := &App{}
	ts := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	writeKnowHowFixture(t, "one", buildOKFKnowledgeMarkdown("one", "One", "Steps", ts, "activity", nil, nil, nil, "fix"))
	first, err := app.GetKnowledgeEntries()
	if err != nil || len(first) != 1 {
		t.Fatalf("first = %d, %v", len(first), err)
	}
	first[0].Title = "changed by a caller"
	again, _ := app.GetKnowledgeEntries()
	if again[0].Title != "One" {
		t.Fatalf("cached list changed by a caller: %q", again[0].Title)
	}
	writeKnowHowFixture(t, "two", buildOKFKnowledgeMarkdown("two", "Two", "Steps", ts, "activity", nil, nil, nil, "fix"))
	if after, _ := app.GetKnowledgeEntries(); len(after) != 2 {
		t.Fatalf("after a new file = %d, want 2", len(after))
	}
	dir, _ := knowledgeDir()
	if err := os.Remove(filepath.Join(dir, "one.md")); err != nil {
		t.Fatal(err)
	}
	if after, _ := app.GetKnowledgeEntries(); len(after) != 1 || after[0].ID != "two" {
		t.Fatalf("after a delete = %+v", after)
	}
}

// The startup pile-up seen on Bo's app (2026-11-12, build 1ae0757fc): page
// reads queued behind the first portrait build, and each touch during that
// build made the next queued reader build again (4 builds, up to 36 s). Every
// reader waiting on a cold build now shares that one build.
func TestDerivedViewColdReadersShareOneBuildDespiteTouches(t *testing.T) {
	app, d := derivedTestApp(t)
	release := make(chan struct{})
	var builds atomic.Int64
	build := func() (derivedTestValue, error) {
		if builds.Add(1) == 1 {
			<-release
		}
		return derivedTestValue{Items: []string{"portrait"}}, nil
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := readDerivedTestValue(t, app, derivedPortrait, build); len(got.Items) != 1 {
				t.Errorf("read %v", got.Items)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	// Publications and edits land while the first build runs.
	for range 3 {
		d.touch(derivedPortrait)
		derivedMemoryEditGeneration.Add(1)
	}
	close(release)
	wg.Wait()
	if got := builds.Load(); got != 1 {
		t.Fatalf("builds = %d for 12 readers queued on a cold build, want 1", got)
	}
}

// A snapshot made out of date by an edit or a publication is served at once;
// only the scheduler rebuilds it.
func TestDerivedViewFreshnessChangeNeverBlocksARead(t *testing.T) {
	app, d := derivedTestApp(t)
	var slow atomic.Bool
	release := make(chan struct{})
	build := func() (derivedTestValue, error) {
		if slow.Load() {
			<-release
		}
		return derivedTestValue{Items: []string{"v"}}, nil
	}
	readDerivedTestValue(t, app, derivedPortrait, build)
	slow.Store(true)
	d.touch(derivedPortrait)
	go d.tick(context.Background(), true) // the rebuild, held open
	time.Sleep(20 * time.Millisecond)
	started := time.Now()
	readDerivedTestValue(t, app, derivedPortrait, build)
	if took := time.Since(started); took > 100*time.Millisecond {
		t.Fatalf("a read after a touch took %v while the rebuild ran", took)
	}
	close(release)
}
