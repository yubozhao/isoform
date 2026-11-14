package main

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// seedActivityForDate adds a minimal activity_sessions row so HasActivityData(date)
// returns true. start_time is set to local midnight + 12h on `date`.
func seedActivityForDate(t *testing.T, s *ActivityStore, date string) {
	t.Helper()
	day, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		t.Fatalf("parse date %q: %v", date, err)
	}
	startMs := day.Add(12 * time.Hour).UnixMilli()
	_, err = s.db.Exec(`
		INSERT INTO activity_sessions (
			id, start_time, end_time, trigger_kind, app_names,
			event_count, segment_count, created_at
		) VALUES (?, ?, ?, 'test', '[]', 0, 0, ?)`,
		"sess-"+date, startMs, startMs+60_000, time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("insert session for %s: %v", date, err)
	}
}

// readMomentsPartial reads moments_partial for a (date, 'daily') row. Returns
// -1 if the row is missing.
func readMomentsPartial(t *testing.T, s *ActivityStore, date string) int {
	t.Helper()
	var v int
	err := s.db.QueryRow(
		`SELECT moments_partial FROM activity_summaries WHERE date = ? AND type = 'daily'`,
		date,
	).Scan(&v)
	if err != nil {
		return -1
	}
	return v
}

func TestSaveSummary_ResetsMomentsPartial(t *testing.T) {
	s := newTestStore(t)
	const date = "2026-04-20"

	gen, err := s.SaveSummary(date, "daily", "topic v1", "haiku", false)
	if err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if gen != 1 {
		t.Errorf("first SaveSummary returned gen = %d, want 1", gen)
	}
	if got := readMomentsPartial(t, s, date); got != 1 {
		t.Errorf("after SaveSummary moments_partial = %d, want 1", got)
	}
}

func TestSaveMoments_ClearsMomentsPartial(t *testing.T) {
	s := newTestStore(t)
	const date = "2026-04-20"

	gen, err := s.SaveSummary(date, "daily", "topic", "haiku", false)
	if err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if err := s.SaveMoments(date, `[{"title":"m1"}]`, gen); err != nil {
		t.Fatalf("SaveMoments: %v", err)
	}
	if got := readMomentsPartial(t, s, date); got != 0 {
		t.Errorf("after SaveMoments moments_partial = %d, want 0", got)
	}
}

// TestSaveMoments_EmptyKeepsPartial covers the post-SaveSummary stale-clear
// path: runGenerateDailySummary calls SaveMoments(date, "") to drop the prior
// run's moments before the new persistMoments lands. That clear must not
// flip moments_partial to 0, otherwise a follow-up agent failure would leave
// the row marked "final but empty" and checkPendingSummaries would skip it.
func TestSaveMoments_EmptyKeepsPartial(t *testing.T) {
	s := newTestStore(t)
	const date = "2026-04-20"

	if _, err := s.SaveSummary(date, "daily", "topic", "haiku", false); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if err := s.SaveMoments(date, "", 0); err != nil {
		t.Fatalf("SaveMoments(empty): %v", err)
	}
	if got := readMomentsPartial(t, s, date); got != 1 {
		t.Errorf("after SaveMoments(empty) moments_partial = %d, want 1", got)
	}
}

// TestSaveMoments_StaleGenerationDiscarded simulates a stalled persistMoments
// goroutine from a previous attempt: SaveSummary bumps the generation, then
// the late goroutine calls SaveMoments with the OLD gen — which must no-op so
// the row's moments_partial stays 1 and the active attempt's wait does not
// see a false success.
func TestSaveMoments_StaleGenerationDiscarded(t *testing.T) {
	s := newTestStore(t)
	const date = "2026-04-20"

	staleGen, err := s.SaveSummary(date, "daily", "attempt 1", "haiku", false)
	if err != nil {
		t.Fatalf("SaveSummary attempt 1: %v", err)
	}
	if _, err := s.SaveSummary(date, "daily", "attempt 2", "haiku", false); err != nil {
		t.Fatalf("SaveSummary attempt 2: %v", err)
	}

	// Late write from attempt 1 must be discarded.
	if err := s.SaveMoments(date, `[{"title":"stale"}]`, staleGen); err != nil {
		t.Fatalf("SaveMoments(stale): %v", err)
	}
	if got := readMomentsPartial(t, s, date); got != 1 {
		t.Errorf("stale SaveMoments should not flip partial: got %d, want 1", got)
	}
}

// TestSaveSummary_AfterMomentsResetsAgain guards the regeneration loop: when
// checkPendingSummaries kicks off a new run for a day whose Moments succeeded
// last time, the topic upsert must flip moments_partial back to 1 so a Moments
// failure on the new run is still detectable next startup. The generation
// must also bump so a stalled prior-attempt persistMoments cannot fool the
// new wait.
func TestSaveSummary_AfterMomentsResetsAgain(t *testing.T) {
	s := newTestStore(t)
	const date = "2026-04-20"

	gen1, err := s.SaveSummary(date, "daily", "topic v1", "haiku", true)
	if err != nil {
		t.Fatalf("SaveSummary v1: %v", err)
	}
	if err := s.SaveMoments(date, `[{"title":"m1"}]`, gen1); err != nil {
		t.Fatalf("SaveMoments: %v", err)
	}
	if got := readMomentsPartial(t, s, date); got != 0 {
		t.Fatalf("precondition: moments_partial = %d, want 0", got)
	}

	gen2, err := s.SaveSummary(date, "daily", "topic v2 (final)", "haiku", false)
	if err != nil {
		t.Fatalf("SaveSummary v2: %v", err)
	}
	if gen2 <= gen1 {
		t.Errorf("expected gen to bump on re-save: got %d after %d", gen2, gen1)
	}
	if got := readMomentsPartial(t, s, date); got != 1 {
		t.Errorf("after re-run SaveSummary moments_partial = %d, want 1", got)
	}
}

func TestListPendingSummaryDates(t *testing.T) {
	s := newTestStore(t)

	now := time.Now()
	today := now.Format("2006-01-02")
	d1 := now.AddDate(0, 0, -1).Format("2006-01-02") // missing summary
	d2 := now.AddDate(0, 0, -2).Format("2006-01-02") // is_partial=1
	d3 := now.AddDate(0, 0, -3).Format("2006-01-02") // moments_partial=1 (topic done)
	d4 := now.AddDate(0, 0, -4).Format("2006-01-02") // both done — should NOT be listed
	d5 := now.AddDate(0, 0, -5).Format("2006-01-02") // no activity sessions — should NOT be listed
	d8 := now.AddDate(0, 0, -8).Format("2006-01-02") // outside 7-day window — should NOT be listed

	for _, d := range []string{today, d1, d2, d3, d4, d8} {
		seedActivityForDate(t, s, d)
	}

	if _, err := s.SaveSummary(d2, "daily", "partial topic", "haiku", true); err != nil {
		t.Fatal(err)
	}

	if _, err := s.SaveSummary(d3, "daily", "final topic", "haiku", false); err != nil {
		t.Fatal(err)
	}
	// d3 has moments_partial=1 from SaveSummary; we never call SaveMoments, so
	// it stays partial — exactly the silent-Moments-failure case we want to
	// catch.

	d4Gen, err := s.SaveSummary(d4, "daily", "final topic", "haiku", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMoments(d4, `[{"title":"m1"}]`, d4Gen); err != nil {
		t.Fatal(err)
	}

	// today: is_partial + has activity, but excluded because today is in progress.
	if _, err := s.SaveSummary(today, "daily", "today partial", "haiku", true); err != nil {
		t.Fatal(err)
	}

	// d8: stale row from outside the scan window — would otherwise match
	// is_partial=1 but the day-range filter must drop it.
	if _, err := s.SaveSummary(d8, "daily", "old partial", "haiku", true); err != nil {
		t.Fatal(err)
	}

	got := s.ListPendingSummaryDates(7)

	wantSet := map[string]bool{d1: true, d2: true, d3: true}
	for _, d := range got {
		if !wantSet[d] {
			t.Errorf("unexpected pending date %q (got %v)", d, got)
		}
		delete(wantSet, d)
	}
	for d := range wantSet {
		t.Errorf("missing pending date %q (got %v)", d, got)
	}

	// Spot-check exclusions explicitly so a future regression that
	// over-reports doesn't pass just because the want-set drained.
	for _, d := range []string{today, d4, d5, d8} {
		if slices.Contains(got, d) {
			t.Errorf("date %q should not be pending (got %v)", d, got)
		}
	}
}

// TestMomentsPartialReset_RescuesLegacyStaleRows simulates the worst legacy
// state that the original backfill couldn't detect: is_partial=0 with a stale
// non-empty moments_json (older clients that never cleared moments after a
// Moments-only failure). The dedicated reset migration must force such rows
// back to moments_partial=1 so ListPendingSummaryDates picks them up.
func TestMomentsPartialReset_RescuesLegacyStaleRows(t *testing.T) {
	db := newTestDB(t)
	s := &ActivityStore{db: db, appliedMigrations: make(map[string]bool)}

	// Apply every migration except the reset so we can paint the legacy state.
	for _, m := range allActivityMigrations {
		if m.Name == "activity_summaries_moments_partial_reset" {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Fn(tx); err != nil {
			tx.Rollback()
			t.Fatalf("migration %q: %v", m.Name, err)
		}
		tx.Commit()
	}

	// Two pre-existing rows the old backfill would have left as
	// moments_partial=0: a topic-final row with stale non-empty moments
	// (the worst case) and a fully-clean row that we'd ideally leave alone
	// but conservatively mark partial too.
	if _, err := db.Exec(`
		INSERT INTO activity_summaries (date, type, summary, model, is_partial, moments_partial, moments_json, created_at)
		VALUES
		  ('2026-04-20', 'daily', 'topic', 'haiku', 0, 0, '[{"title":"stale"}]', 1),
		  ('2026-04-21', 'daily', 'topic', 'haiku', 0, 0, '[{"title":"fresh"}]', 1)
	`); err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateActivitySummariesMomentsPartialReset(tx); err != nil {
		tx.Rollback()
		t.Fatalf("reset migration: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{"2026-04-20", "2026-04-21"} {
		if got := readMomentsPartial(t, s, d); got != 1 {
			t.Errorf("after reset migration moments_partial[%s] = %d, want 1", d, got)
		}
	}
}

// TestWaitForMomentsPersisted exercises both branches: a row that flips to
// moments_partial=0 mid-poll (success) and one that never flips before the
// timeout (failure → caller will retry).
func TestWaitForMomentsPersisted(t *testing.T) {
	a := &App{activity: &activityMonitorState{store: newTestStore(t)}}
	const date = "2026-04-20"

	gen, err := a.activity.store.SaveSummary(date, "daily", "topic", "haiku", false)
	if err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := a.activity.store.SaveMoments(date, `[{"title":"m1"}]`, gen); err != nil {
			t.Errorf("SaveMoments in goroutine: %v", err)
		}
	}()
	if got := a.waitForMomentsPersistedForLanguage(date, gen, "", 2*time.Second); got != momentsWaitPersisted {
		t.Errorf("waitForMomentsPersisted = %v, want momentsWaitPersisted once SaveMoments lands", got)
	}

	if _, err := a.activity.store.db.Exec(
		`UPDATE activity_summaries SET moments_partial = 1 WHERE date = ? AND type = 'daily'`,
		date,
	); err != nil {
		t.Fatalf("flip back to partial: %v", err)
	}
	if got := a.waitForMomentsPersistedForLanguage(date, gen, "", 300*time.Millisecond); got != momentsWaitTimeout {
		t.Errorf("waitForMomentsPersisted = %v, want momentsWaitTimeout when nothing flips", got)
	}
}

// TestPersistMoments_DriftDoesNotPoisonRow verifies that a stalled prior-
// attempt persistMoments goroutine cannot overwrite a newer attempt's
// moments_json. The newer attempt will land its own (unrelated) write; what
// we assert here is only that the stale call leaves the row in the post-
// SaveSummary "moments_partial=1, moments_json=” " state — i.e. the gen-
// gated SaveMoments + verify path correctly no-ops. The matching
// "no moments-error event emitted" half of the contract is covered by code
// review rather than a unit test, since wails runtime emits aren't trivially
// mockable here.
func TestPersistMoments_DriftDoesNotPoisonRow(t *testing.T) {
	a := &App{activity: &activityMonitorState{store: newTestStore(t)}}
	const date = "2026-04-20"

	staleGen, err := a.activity.store.SaveSummary(date, "daily", "attempt 1", "haiku", false)
	if err != nil {
		t.Fatalf("SaveSummary attempt 1: %v", err)
	}
	// Mimic runGenerateDailySummary's post-SaveSummary clear so the row is
	// in the state attempt 2 would have left.
	if err := a.activity.store.SaveMoments(date, "", 0); err != nil {
		t.Fatalf("clear moments: %v", err)
	}
	// Newer attempt bumps generation past the stalled goroutine.
	if _, err := a.activity.store.SaveSummary(date, "daily", "attempt 2", "haiku", false); err != nil {
		t.Fatalf("SaveSummary attempt 2: %v", err)
	}

	// Stalled attempt 1 persistMoments finally fires with its (now stale)
	// generation. Run synchronously by calling the function on a closed
	// channel — the channel send pattern in production is the buffered
	// momentsCh fed by generateMomentsAsync.
	ch := make(chan []MomentData, 1)
	ch <- []MomentData{{Title: "stale moment"}}
	a.persistMomentsForLanguage(date, staleGen, "", ch)

	// Row must still reflect attempt 2's empty-clear, not the stale write.
	var partial int
	var momentsJSON string
	err = a.activity.store.db.QueryRow(
		`SELECT moments_partial, moments_json FROM activity_summaries WHERE date = ? AND type = 'daily'`,
		date,
	).Scan(&partial, &momentsJSON)
	if err != nil {
		t.Fatalf("readback row: %v", err)
	}
	if partial != 1 {
		t.Errorf("moments_partial = %d after stale persist, want 1", partial)
	}
	if momentsJSON != "" {
		t.Errorf("moments_json = %q after stale persist, want empty", momentsJSON)
	}
}

// TestWaitForMomentsPersisted_GenerationDriftReturnsDrift verifies the
// cross-attempt safeguard: if a second SaveSummary bumps the row's generation
// while we're still waiting on attempt 1, the wait must report drift —
// distinct from timeout — so the retry loop can yield rather than clobber
// the new owner's freshly written summary.
func TestWaitForMomentsPersisted_GenerationDriftReturnsDrift(t *testing.T) {
	a := &App{activity: &activityMonitorState{store: newTestStore(t)}}
	const date = "2026-04-20"

	gen1, err := a.activity.store.SaveSummary(date, "daily", "topic v1", "haiku", false)
	if err != nil {
		t.Fatalf("SaveSummary v1: %v", err)
	}

	// Bump generation in the background mid-wait, then satisfy the new gen
	// to confirm the wait DID see the drift (rather than waiting for the
	// updated row to flip).
	go func() {
		time.Sleep(150 * time.Millisecond)
		gen2, err := a.activity.store.SaveSummary(date, "daily", "topic v2", "haiku", false)
		if err != nil {
			t.Errorf("SaveSummary v2: %v", err)
			return
		}
		if err := a.activity.store.SaveMoments(date, `[{"title":"m2"}]`, gen2); err != nil {
			t.Errorf("SaveMoments v2: %v", err)
		}
	}()

	if got := a.waitForMomentsPersistedForLanguage(date, gen1, "", 3*time.Second); got != momentsWaitDrift {
		t.Errorf("wait on stale gen = %v, want momentsWaitDrift", got)
	}
}

// Launch tasks run one at a time, so the pending-summaries task must never
// wait: with no runner up it skips at once instead of holding the queue.
func TestCheckPendingSummariesDoesNotWaitForProbe(t *testing.T) {
	app := &App{activity: &activityMonitorState{store: newTestStore(t)}, launch: newLaunchStartup()}
	done := make(chan struct{})
	go func() { app.checkPendingSummaries(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pending summaries waited for the credential probe")
	}
}

// The pass runs once the model runner is up, whether or not the credential
// probe ran: the probe never verifies anything while UseYansuGateway is
// on, which loadConfig forces for every install (the pass was skipped on
// every launch, before and after it moved behind the probe).
func TestPendingSummariesRunWithReadyRunnerWithoutProbeFlag(t *testing.T) {
	if !loadConfig().UseYansuGateway {
		t.Fatal("loadConfig no longer forces UseYansuGateway; revisit this gate")
	}
	app := &App{}
	if app.pendingSummariesCanRun() {
		t.Fatal("pending summaries may not run before the runner is up")
	}
	app.runner = &purposeExtractionFakeRunner{}
	app.ready = true
	if !app.pendingSummariesCanRun() {
		t.Fatal("a ready runner without the probe flag skipped pending summaries")
	}
}

// The probe launch task queues the pending summaries after the probe ran, so
// they see verified credentials instead of timing out ahead of the probe.
func TestDeferAgentProbeQueuesPendingSummariesAfterProbe(t *testing.T) {
	launch := newLaunchStartup()
	app := &App{launch: launch}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launch.startWorker(ctx)
	defer launch.stop()

	var mu sync.Mutex
	var order []string
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}
	summariesDone := make(chan struct{})
	app.deferAgentProbe(
		func() error { record("start"); return nil },
		func() {
			app.mu.Lock()
			app.greeted = true
			app.mu.Unlock()
			record("probe")
		},
		func() {
			app.mu.Lock()
			probed := app.greeted
			app.mu.Unlock()
			record(fmt.Sprintf("summaries verified=%t", probed))
			close(summariesDone)
		},
	)
	select {
	case <-summariesDone:
	case <-time.After(3 * time.Second):
		t.Fatal("pending summaries never ran after the probe")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"start", "probe", "summaries verified=true"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("launch order = %v, want %v", order, want)
	}
}

// A failed agent start queues no summaries: there is no runner to write them.
func TestDeferAgentProbeSkipsSummariesWhenAgentFails(t *testing.T) {
	launch := newLaunchStartup()
	app := &App{launch: launch}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launch.startWorker(ctx)
	defer launch.stop()

	ran := make(chan string, 2)
	app.deferAgentProbe(
		func() error { return fmt.Errorf("no agent") },
		func() { ran <- "probe" },
		func() { ran <- "summaries" },
	)
	// Two markers: anything the probe task queued lands before the second.
	for _, name := range []string{"marker_1", "marker_2"} {
		select {
		case <-app.deferLaunchTask(name, func() {}):
		case <-time.After(3 * time.Second):
			t.Fatal("launch queue stalled")
		}
	}
	select {
	case step := <-ran:
		t.Fatalf("%s ran after a failed agent start", step)
	default:
	}
}
