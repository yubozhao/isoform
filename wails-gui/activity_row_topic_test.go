package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func rowTopicProjects(t *testing.T, a *App, projects ...topicProject) {
	t.Helper()
	_, err := a.topicsSvc().store.update(func(st *topicsStateFile) { st.Projects = projects })
	if err != nil {
		t.Fatal(err)
	}
}

func rowTopicSnapshot(t *testing.T, a *App, row ActivityRow, address string) {
	t.Helper()
	s := a.activity.store
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = s.SaveSnapshots(tx, []ActivitySnapshotRow{{SessionID: "session-" + row.ID, Timestamp: row.StartMs + 1, FilePath: "snap-" + row.ID, AXText: "captured private sentence", BrowserURL: address}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestActivityRowTopicDetailsAndOpen(t *testing.T) {
	a := newThreadTestApp(t)
	day, start := threadTestDay(t)
	seed := func(id string, h int, app, bundle string) ActivityRow {
		return seedThreadMoment(t, a, threadFixtureMoment{id: id, title: "Captured title", app: app, bundle: bundle, start: at(day, h, 0), end: at(day, h, 10)})
	}
	page := seed("page", 8, "Safari", "com.apple.Safari")
	file := seed("file", 9, "Safari", "com.apple.Safari")
	call := seed("call", 10, "Zoom", "us.zoom.xos")
	protected := seed("protected", 11, "Private Notes", "com.example.private")
	paused := seed("paused", 12, "Safari", "com.apple.Safari")
	private := seed("private", 13, "Safari", "com.apple.Safari")
	seed("no-detail", 14, "Notes", "com.apple.Notes")
	path := filepath.Join(t.TempDir(), "quarterly report.pdf")
	if err := os.WriteFile(path, []byte("example"), 0600); err != nil {
		t.Fatal(err)
	}
	rowTopicSnapshot(t, a, file, (&url.URL{Scheme: "file", Path: path}).String())
	for _, row := range []ActivityRow{page, protected, paused, private} {
		rowTopicSnapshot(t, a, row, "https://example.org/captured-query?secret=example")
	}
	db := a.activity.store.db
	editor := seed("editor", 15, "Code", "com.microsoft.VSCode")
	rowTopicSnapshot(t, a, editor, "")
	if _, err := db.Exec(`UPDATE activity_snapshots SET app='code_editor',parsed_json=json_object('metadata',json_object('relativePath',?)) WHERE session_id='session-editor'`, path); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE activity_sessions SET is_meeting=1,audio_path='recording.m4a' WHERE id=?`, "session-"+call.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE activity_events SET data=json_set(data,'$.privateWindow',json('true')) WHERE session_id='session-private'`); err != nil {
		t.Fatal(err)
	}
	a.saveObservationPauses([]observationPause{{Start: paused.StartMs, End: paused.EndMs}})
	rowTopicProjects(t, a, topicProject{ID: "larger", Name: "Halcyon", ActivityIDs: []string{"page", "other"}}, topicProject{ID: "small", Name: "Smaller", ActivityIDs: []string{"page"}}, topicProject{ID: "deferred", Name: "Reserved", Deferred: true, ActivityIDs: []string{"file"}}, topicProject{ID: "untitled", Name: "  ", ActivityIDs: []string{"file", "no-detail"}})
	details, err := a.GetActivityRowDetails(start, activityThreadDayEnd(start))
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 4 {
		t.Fatalf("details = %+v", details)
	}
	want := map[string]ActivityOpenTarget{"page": {"page", "example.org"}, "file": {"file", "quarterly report.pdf"}, "editor": {"file", "quarterly report.pdf"}, "call": {"call", "Recording"}}
	for _, d := range details {
		if d.Kind != "screen" || d.Open == nil || *d.Open != want[d.ID] {
			t.Fatalf("unexpected detail %+v with target %+v", d, d.Open)
		}
		if d.ID == "page" {
			if d.TopicID != "larger" || d.TopicName != "Halcyon" {
				t.Fatalf("topic = %+v", d)
			}
		} else if d.TopicID != "" || d.TopicName != "" {
			t.Fatalf("unexpected topic = %+v", d)
		}
		delete(want, d.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing targets: %v", want)
	}
	var opened []string
	previous := openActivityLocation
	t.Cleanup(func() { openActivityLocation = previous })
	openActivityLocation = func(address string) error { opened = append(opened, address); return nil }
	if _, err := db.Exec(`UPDATE activity_snapshots SET browser_url='https://changed.example/path' WHERE session_id='session-page'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"page", "file"} {
		if err := a.OpenActivityRow("screen", id); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(opened, []string{"https://changed.example/path", resolved}) {
		t.Fatalf("opened = %v", opened)
	}
	if err := a.OpenActivityRow("screen", "call"); !errors.Is(err, errActivityOpenInApp) || err.Error() != "activity_open_in_app:session-call" {
		t.Fatalf("call = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenActivityRow("screen", "file"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file = %v", err)
	}
	for _, id := range []string{"unknown", "private", "paused", "protected"} {
		if err := a.OpenActivityRow("screen", id); err == nil {
			t.Fatalf("opened %s", id)
		}
	}
	if len(opened) != 2 {
		t.Fatalf("unexpected launches = %v", opened)
	}

	// With Closed Eyes on, rows from before it stay openable (A9); a row
	// inside a pause still has nothing to open.
	if _, err := a.setClosedEyesModeEnabled(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.setClosedEyesModeEnabled(false) })
	// Closed Eyes is on from late in the fixture day, after every row, so
	// the test doesn't depend on the time it runs.
	a.saveObservationPauses([]observationPause{{Start: paused.StartMs, End: paused.EndMs}, {Start: at(day, 23, 0).UnixMilli()}})
	details, err = a.GetActivityRowDetails(start, activityThreadDayEnd(start))
	if err != nil {
		t.Fatal(err)
	}
	pageOpen := false
	for _, d := range details {
		if d.ID == "page" && d.Open != nil && d.Open.Kind == "page" {
			pageOpen = true
		}
		if d.ID == "paused" && d.Open != nil {
			t.Fatalf("a row inside a pause can be opened: %+v", d)
		}
	}
	if !pageOpen {
		t.Fatalf("a past page lost its open target under Closed Eyes: %+v", details)
	}
	if err := a.OpenActivityRow("screen", "page"); err != nil {
		t.Fatalf("a past page did not open under Closed Eyes: %v", err)
	}
	if err := a.OpenActivityRow("screen", "paused"); err == nil {
		t.Fatal("a row inside a pause opened under Closed Eyes")
	}
}

func TestActivityRowTopicExclusionSurvivesRebuild(t *testing.T) {
	a := newThreadTestApp(t)
	day, _ := threadTestDay(t)
	row := seedThreadMoment(t, a, threadFixtureMoment{id: "plan", app: "Code", title: "Halcyon import", entities: []Entity{{Type: entityProject, Name: "Halcyon"}}, start: at(day, 10, 0), end: at(day, 10, 15)})
	in := topicRegistryData{now: time.Now(), activities: []ActivityRow{row}}
	built := buildTopicRegistry(in)
	if len(built.projects) != 1 || !slices.Contains(built.projects[0].ActivityIDs, row.ID) {
		t.Fatal(built.projects)
	}
	id := built.projects[0].ID
	rowTopicProjects(t, a, built.projects...)
	// A ledger failure is visible even after membership and the sidecar are
	// saved. Repeating the choice finishes the same answer exactly once.
	ledger := a.answersLedger()
	answerPath := ledger.path
	ledger.path = ""
	if err := a.NotThisTopic("screen", row.ID, id); err == nil {
		t.Fatal("ledger failure returned success")
	}
	ledger.path = answerPath
	// The interrupted write did not even reach its newline. The retry must
	// survive the fragment and leave its own answer readable on the next retry.
	if err := os.MkdirAll(filepath.Dir(answerPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(answerPath, []byte(`{"id":"torn"`), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := a.NotThisTopic("screen", row.ID, id); err != nil {
			t.Fatal(err)
		}
		if got := ledger.entries(); len(got) != 1 || got[0].Action != "not_this_topic" {
			t.Fatalf("retry %d left unreadable or duplicate answers: %+v", i+1, got)
		}
	}
	in.existing = a.topicsSvc().store.load().Projects
	if len(in.existing) != 1 || len(in.existing[0].ActivityIDs) != 0 {
		t.Fatalf("saved = %+v", in.existing)
	}
	var err error
	in.activityExclusions, err = a.topicsSvc().activityExclusions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	again := buildTopicRegistry(in)
	if len(again.projects) != 1 || again.projects[0].ID != id || len(again.projects[0].ActivityIDs) != 0 || again.views[0].SourceCounts["activity"] != 0 {
		t.Fatalf("rebuilt = %+v", again)
	}
	// Production reserves a project that lost its last readable source.
	in.existing, in.withheld = a.readableTopicProjects(context.Background(), in.existing, in.activities, nil, in.now, nil, nil)
	reserved := buildTopicRegistry(in)
	for _, p := range reserved.projects {
		if slices.Contains(p.ActivityIDs, row.ID) {
			t.Fatalf("recreated rejected topic: %+v", p)
		}
	}
	entries := a.answersLedger().entries()
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	r := entries[0]
	if r.Action != "not_this_topic" || r.Kind != "activity" || r.Source != "topic" || r.Title != row.Title || r.ItemID != "activity:"+row.ID {
		t.Fatal(r)
	}
	stats, err := a.GetJudgmentStats(1)
	if err != nil || stats.Days[0].Total != (JudgmentCounts{TopicCorrections: 1}) || stats.Days[0].BySource["topic"] != (JudgmentCounts{TopicCorrections: 1}) {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	if err := a.activity.store.DeleteActivity(row.ID); err != nil {
		t.Fatal(err)
	}
	x, err := a.topicsSvc().activityExclusions(context.Background())
	if err != nil || len(x) != 0 {
		t.Fatalf("pruned = %v, %v", x, err)
	}
}

func TestActivityRowTopicFailedReads(t *testing.T) {
	a := newThreadTestApp(t)
	rowTopicProjects(t, a)
	s := a.topicsSvc().store
	check := func() {
		t.Helper()
		if got, err := a.GetActivityRowDetails(1, 2); err == nil || got != nil {
			t.Fatalf("read returned %v, %v", got, err)
		}
	}
	if err := os.WriteFile(s.activityExclusionPath(), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	check()
	if err := os.Remove(s.activityExclusionPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	check()
	rowTopicProjects(t, a)
	if err := a.store.SetUIState(context.Background(), observationPausesKey, "{"); err != nil {
		t.Fatal(err)
	}
	check()
	if err := a.activity.store.Close(); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestActivityRowTopicCorrectionsSharePeriodicRebuild(t *testing.T) {
	a := newThreadTestApp(t)
	now := time.Now()
	configureTopicRegistryTest(a, now)
	svc := a.topicsSvc()
	ids := []string{"import", "review", "verify", "keep"}
	for i, id := range ids {
		end := now.Add(-time.Duration(i+1) * time.Hour)
		seedThreadMoment(t, a, threadFixtureMoment{id: id, app: "Code", title: "Halcyon import", entities: []Entity{{Type: entityProject, Name: "Halcyon"}}, start: end.Add(-time.Minute), end: end})
	}
	if _, err := collectAndReadTopicSnapshotViewsForTest(a); err != nil {
		t.Fatal(err)
	}
	before := svc.store.load()
	if before.Today == nil || len(before.Projects) != 1 || len(before.Projects[0].ActivityIDs) != 4 {
		t.Fatalf("initial projection: %+v", before.Projects)
	}
	projectID := before.Projects[0].ID

	// Drive the production worker's periodic timer. Each new timer means the
	// worker has finished its previous pass and is waiting again.
	tick := make(chan time.Time, 1)
	armed := make(chan time.Duration, 4)
	svc.registryAfter = func(delay time.Duration) <-chan time.Time { armed <- delay; return tick }
	var builds atomic.Int32
	svc.registryDuringCollect = func() { builds.Add(1) }
	waitForTimer := func() {
		t.Helper()
		select {
		case delay := <-armed:
			if delay != before.backgroundRefreshInterval() {
				t.Fatalf("worker scheduled in %v, want %v", delay, before.backgroundRefreshInterval())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not arm its periodic timer")
		}
	}
	svc.startRegistryWorker(t.Context())
	t.Cleanup(svc.stopRegistryWorker)
	waitForTimer()
	for i, id := range ids[:3] {
		if err := a.NotThisTopic("screen", id, projectID); err != nil {
			t.Fatal(err)
		}
		state := svc.store.load()
		if !reflect.DeepEqual(state.Today, before.Today) {
			t.Fatal("correction changed the published Today snapshot")
		}
		members := state.Projects[0].ActivityIDs
		if len(members) != len(ids)-i-1 || slices.Contains(members, id) {
			t.Fatalf("membership after correcting %s: %v", id, members)
		}
		today, err := a.GetTodayTopics()
		if err != nil || !today.Stale || today.Refreshing || len(today.Topics) != 1 || builds.Load() != 0 || svc.registryPending() {
			t.Fatalf("before tick: Today=%+v err=%v builds=%d", today, err, builds.Load())
		}
	}

	tick <- now
	waitForTimer()
	after := svc.store.load()
	if builds.Load() != 1 || svc.topicsStale() || svc.registryPending() || after.Today == nil || len(after.Today.Views) != 1 || after.Today.Views[0].SourceCounts["activity"] != 1 {
		t.Fatalf("after tick: builds=%d stale=%t Today=%+v", builds.Load(), svc.topicsStale(), after.Today)
	}
	if len(after.Projects) != 1 || !slices.Equal(after.Projects[0].ActivityIDs, ids[3:]) {
		t.Fatalf("rebuilt projects: %+v", after.Projects)
	}
}
