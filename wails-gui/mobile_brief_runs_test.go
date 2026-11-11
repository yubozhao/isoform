package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"yansu-agent/agent/native"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

func TestBriefRecordsConsecutiveFailuresAndKeepsCause(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 5)}
	svc, _ := missedBriefRig(t, clock)
	occurrence, ok := svc.slotOccurrence(clock.Now(), svc.config.load(), mobileBriefSlotMorning)
	if !ok {
		t.Fatal("missing morning slot")
	}
	svc.recordFailure(occurrence, errors.New("different failure"))
	for i := 0; i < 3; i++ {
		svc.recordFailure(occurrence, context.DeadlineExceeded)
	}
	runs := svc.runs.list()
	if len(runs) != 1 || runs[0].Attempts != 4 || runs[0].SameFailures != 3 || runs[0].FailureCause != "timeout" {
		t.Fatalf("failures = %+v", runs)
	}
	reloaded := newMobileBriefRunStore(filepath.Dir(svc.runs.path)).list()
	if len(reloaded) != 1 || reloaded[0].SameFailures != 3 || reloaded[0].FailureCause != "timeout" {
		t.Fatalf("failure evidence did not survive reopening: %+v", reloaded)
	}
}

func TestMobileBriefRunStoreUpsertsAndCountsFailures(t *testing.T) {
	store := newMobileBriefRunStore(t.TempDir())
	scheduled := formatMobileBriefTime(mobileBriefTestDay(7, 0))
	first, err := store.record(mobileBriefRun{BriefID: "2026-10-09-morning", Slot: "morning", ScheduledAt: scheduled, Status: mobileBriefRunFailed, At: scheduled})
	if err != nil || first.Attempts != 1 {
		t.Fatalf("first failure = %+v %v", first, err)
	}
	second, _ := store.record(mobileBriefRun{BriefID: "2026-10-09-morning", Slot: "morning", ScheduledAt: scheduled, Status: mobileBriefRunFailed, At: scheduled})
	if second.Attempts != 2 {
		t.Fatalf("second failure attempts = %d", second.Attempts)
	}
	if err := store.markNotified("2026-10-09-morning"); err != nil {
		t.Fatal(err)
	}
	ok, _ := store.record(mobileBriefRun{BriefID: "2026-10-09-morning", Slot: "morning", ScheduledAt: scheduled, Status: mobileBriefRunOK, At: scheduled})
	if ok.Status != mobileBriefRunOK || !ok.Notified {
		t.Fatalf("ok must replace failed and keep notified: %+v", ok)
	}
	again, _ := store.record(mobileBriefRun{BriefID: "2026-10-09-morning", Slot: "morning", ScheduledAt: scheduled, Status: mobileBriefRunFailed, At: scheduled})
	if again.Status != mobileBriefRunOK {
		t.Fatalf("a failure after ok must not move the slot back: %+v", again)
	}
	if runs := store.list(); len(runs) != 1 {
		t.Fatalf("one slot must be one record, got %d", len(runs))
	}
	for i := 0; i < mobileBriefRunsKeep+5; i++ {
		id := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02") + "-evening"
		if _, err := store.record(mobileBriefRun{BriefID: id, Slot: "evening", ScheduledAt: scheduled, Status: mobileBriefRunOK, At: scheduled}); err != nil {
			t.Fatal(err)
		}
	}
	if runs := store.list(); len(runs) != mobileBriefRunsKeep {
		t.Fatalf("the store keeps %d runs, want %d", len(runs), mobileBriefRunsKeep)
	}
	if newMobileBriefRunStore("").list() != nil {
		t.Fatal("no state dir must read as no runs")
	}
}

func TestMobileBriefHealthViewReportsLastNextMissedAndFailing(t *testing.T) {
	now := mobileBriefTestDay(14, 0)
	settings := defaultMobileBriefSettings()
	latest := MobileBriefV1{BriefID: "2026-10-08-evening", Slot: "evening", AsOf: "2026-10-08T18:00:30Z"}
	runs := []mobileBriefRun{
		{BriefID: "2026-10-09-morning", Slot: "morning", ScheduledAt: formatMobileBriefTime(mobileBriefTestDay(7, 0)), Status: mobileBriefRunMissed},
		{BriefID: "2026-10-08-morning", Slot: "morning", ScheduledAt: formatMobileBriefTime(mobileBriefTestDay(7, 0).AddDate(0, 0, -1)), Status: mobileBriefRunMissed},
		{BriefID: "2026-09-20-morning", Slot: "morning", ScheduledAt: formatMobileBriefTime(mobileBriefTestDay(7, 0).AddDate(0, 0, -19)), Status: mobileBriefRunMissed},
		{BriefID: "2026-10-08-evening", Slot: "evening", ScheduledAt: formatMobileBriefTime(mobileBriefTestDay(18, 0).AddDate(0, 0, -1)), Status: mobileBriefRunOK},
	}
	view := mobileBriefHealthView(now, time.UTC, settings, latest, true, runs)
	if view.LastRunAt != latest.AsOf || view.LastSlot != "evening" {
		t.Fatalf("last run = %+v", view)
	}
	if view.NextSlot != "evening" || view.NextRunAt != "2026-10-09T18:00:00Z" {
		t.Fatalf("next run = %+v", view)
	}
	if view.MissedRuns != 2 {
		t.Fatalf("missed runs = %d, want 2 inside the week", view.MissedRuns)
	}
	if view.FailingSlot != "" {
		t.Fatalf("nothing is failing: %+v", view)
	}

	failing := append(runs, mobileBriefRun{BriefID: "2026-10-09-evening", Slot: "evening", ScheduledAt: formatMobileBriefTime(mobileBriefTestDay(18, 0)), Status: mobileBriefRunFailed, Attempts: 2})
	view = mobileBriefHealthView(mobileBriefTestDay(18, 30), time.UTC, settings, latest, true, failing)
	if view.FailingSlot != "evening" || view.FailedAttempts != 2 {
		t.Fatalf("failing slot = %+v", view)
	}
	if view.NextSlot != "morning" || view.NextRunAt != "2026-10-10T07:00:00Z" {
		t.Fatalf("after the evening slot the next run is tomorrow morning: %+v", view)
	}

	disabled := settings
	disabled.Enabled = false
	view = mobileBriefHealthView(now, time.UTC, disabled, MobileBriefV1{}, false, nil)
	if view.Enabled || view.NextRunAt != "" || view.LastRunAt != "" {
		t.Fatalf("a disabled schedule has no next run: %+v", view)
	}
}

// missedBriefRig wires a brief service to an engagement service so the
// brief_missed moment can be observed.
func missedBriefRig(t *testing.T, clock *fakeMobileBriefClock) (*mobileBriefService, *engagementTestRig) {
	t.Helper()
	svc, _ := newTestMobileBriefService(t, clock)
	rig := newEngagementTestRig(t)
	rig.svc.now = clock.Now
	svc.app.engagement = rig.svc
	// The schedule has been watched since the start of the test day.
	svc.runs.firstSeen(mobileBriefTestDay(0, 0))
	return svc, rig
}

func TestMobileBriefFreshInstallIsNotGreetedWithAMissedBrief(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(14, 0)}
	svc, _ := newTestMobileBriefService(t, clock)
	rig := newEngagementTestRig(t)
	rig.svc.now = clock.Now
	svc.app.engagement = rig.svc
	svc.checkDue(context.Background())
	if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 0 {
		t.Fatalf("a slot that passed before the first tick must not be missed: %+v", moments)
	}
	// The morning's catch-up grace has passed, but the fresh install does not
	// announce a slot that elapsed before the schedule was first watched.
	if runs := svc.runs.list(); len(runs) != 0 {
		t.Fatalf("the first tick should not make a late morning brief: %+v", runs)
	}
	if health := svc.Health(); health.PendingSlot != "" {
		t.Fatalf("Today would wait for a morning that is never made: %+v", health)
	}
	// From now on the schedule is watched: the evening is missed after its grace.
	clock.Set(mobileBriefTestDay(0, 30).AddDate(0, 0, 1))
	svc.checkDue(context.Background())
	runs := svc.runs.list()
	if len(runs) != 1 || runs[0].BriefID != "2026-10-09-evening" || runs[0].Status != mobileBriefRunMissed {
		t.Fatalf("runs after the first watched window closed = %+v", runs)
	}
	if newMobileBriefRunStore("").firstSeen(clock.Now()) != clock.Now() {
		t.Fatal("without a state dir the first-seen time is always now")
	}
}

func openMoments(t *testing.T, store *storage.Store, kind string) []storage.EngagementMomentRow {
	t.Helper()
	var out []storage.EngagementMomentRow
	for _, state := range []string{storage.EngagementStatePending, storage.EngagementStateScheduled, storage.EngagementStateSent} {
		rows, err := store.ListEngagementMoments(context.Background(), state, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Kind == kind {
				out = append(out, row)
			}
		}
	}
	return out
}

func TestMobileBriefMissedWindowNotifiesOncePerSlot(t *testing.T) {
	// At 18:00 the morning's catch-up grace has elapsed and the evening is due.
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(18, 0)}
	svc, rig := missedBriefRig(t, clock)
	ctx := context.Background()

	svc.checkDue(ctx)
	svc.checkDue(ctx)
	clock.Advance(time.Hour)
	svc.checkDue(ctx)

	moments := openMoments(t, rig.store, string(engagement.KindBriefMissed))
	if len(moments) != 1 || moments[0].SubjectID != "2026-10-09-morning" || moments[0].DedupeKey != "brief_missed:2026-10-09-morning" {
		t.Fatalf("missed moments = %+v, want one for the morning slot", moments)
	}
	// The evening built at 18:00 is recorded as ok with the slot time.
	runs := svc.runs.list()
	if len(runs) != 2 || runs[0].BriefID != "2026-10-09-morning" || runs[0].Status != mobileBriefRunMissed || !runs[0].Notified ||
		runs[1].Status != mobileBriefRunOK || runs[1].Slot != "evening" || runs[1].ScheduledAt != "2026-10-09T18:00:00Z" {
		t.Fatalf("runs = %+v, want the morning missed and the evening built", runs)
	}
	if health := svc.Health(); health.LastSlot != "evening" || health.MissedRuns != 1 || health.NextSlot != "morning" {
		t.Fatalf("health = %+v", health)
	}
}

// Identical failures continue with backoff, and the slot is missed only once
// its window closes: noon for the morning, when the evening brief takes over
// (today-review v1.1.21 R49).
func TestMobileBriefFailuresKeepRetryingUntilTheNextSlot(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 5)}
	svc, rig := missedBriefRig(t, clock)
	svc.gather = func(context.Context, string, time.Time, time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{}, context.DeadlineExceeded
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		svc.checkDue(ctx)
		if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 0 {
			t.Fatalf("failure %d announced the brief missed while its window is open", i+1)
		}
		runs := svc.runs.list()
		if len(runs) != 1 || runs[0].Status != mobileBriefRunFailed || runs[0].Attempts != i+1 {
			t.Fatalf("after failure %d runs = %+v", i+1, runs)
		}
		health := svc.Health()
		wantRetry := formatMobileBriefTime(clock.Now().Add(mobileBriefRetryBackoff(i + 1)))
		if health.FailingSlot != "morning" || health.PendingSlot != "morning" || health.RetryAt != wantRetry {
			t.Fatalf("after failure %d health = %+v", i+1, health)
		}
		// No try before the backoff ends.
		clock.Advance(mobileBriefRetryBackoff(i+1) - time.Second)
		svc.checkDue(ctx)
		if runs := svc.runs.list(); runs[0].Attempts != i+1 {
			t.Fatalf("retried before the backoff ended: %+v", runs)
		}
		clock.Advance(time.Second)
	}
	clock.Set(mobileBriefTestDay(11, 59))
	svc.checkDue(ctx)
	if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 0 {
		t.Fatalf("the morning was announced missed before noon: %+v", moments)
	}
	// Noon closes the morning's window: missed once, attempts kept.
	clock.Set(mobileBriefTestDay(12, 0))
	svc.checkDue(ctx)
	clock.Advance(time.Hour)
	svc.checkDue(ctx)
	moments := openMoments(t, rig.store, string(engagement.KindBriefMissed))
	if len(moments) != 1 || moments[0].SubjectID != "2026-10-09-morning" {
		t.Fatalf("want one missed notice for the morning, got %+v", moments)
	}
	byID := map[string]mobileBriefRun{}
	for _, run := range svc.runs.list() {
		byID[run.BriefID] = run
	}
	if morning := byID["2026-10-09-morning"]; morning.Status != mobileBriefRunMissed || morning.Attempts < mobileBriefMissedAfterFailures || !morning.Notified {
		t.Fatalf("morning = %+v", morning)
	}
}

// A failed morning's tries stop at noon, when the evening brief takes over,
// and a failed evening's at midnight, when the next morning's does: Today
// never says Isoform tries again after that (today-review v1.1.21 R49).
func TestMobileBriefRetriesGiveWayToTheNextSlot(t *testing.T) {
	for _, tc := range []struct {
		name, slot, evening string
		failAt              time.Time
		next                time.Time
	}{
		{"morning", "morning", "18:00", mobileBriefTestDay(11, 40), mobileBriefTestDay(18, 0)},
		{"evening", "evening", "18:00", mobileBriefTestDay(23, 40), mobileBriefTestDay(7, 0).AddDate(0, 0, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: tc.failAt}
			svc, rig := missedBriefRig(t, clock)
			if err := svc.config.save(mobileBriefSettings{Enabled: true, Morning: "07:00", Evening: tc.evening}); err != nil {
				t.Fatal(err)
			}
			builds := 0
			svc.gather = func(context.Context, string, time.Time, time.Time) (mobileBriefInput, error) {
				builds++
				return mobileBriefInput{}, context.DeadlineExceeded
			}
			ctx := context.Background()
			// Five failures in a row: the next try waits 30 minutes, past
			// the end of the window.
			for i := 0; i < 5; i++ {
				svc.recordFailure(mobileBriefSlotOccurrence{BriefID: "2026-10-09-" + tc.slot, Slot: tc.slot, ScheduledAt: map[string]time.Time{"morning": mobileBriefTestDay(7, 0), "evening": mobileBriefTestDay(18, 0)}[tc.slot]}, context.DeadlineExceeded)
			}
			health := svc.Health()
			if health.PendingSlot != tc.slot || health.PendingAttempts != 5 {
				t.Fatalf("the failing %s is not pending: %+v", tc.slot, health)
			}
			if health.RetryAt != "" {
				t.Fatalf("Today says the %s tries again at %s, after its window closes", tc.slot, health.RetryAt)
			}
			at, catchUp := svc.slotNextRun(clock.Now(), svc.config.load(), tc.slot)
			if catchUp || !at.After(clock.Now().Add(time.Hour)) {
				t.Fatalf("the %s's next run = %s (catch-up %v), want its next day's slot", tc.slot, at, catchUp)
			}
			if next, ok := mobileBriefScheduledNextRun(clock.Now(), svc.location, svc.config.load(), svc.scheduleFacts(clock.Now(), svc.config.load())); !ok || !next.ScheduledAt.Equal(tc.next) {
				t.Fatalf("next scheduled = %+v, want %s", next, tc.next)
			}
			// After the window closes the failed slot is never tried again.
			clock.Set(tc.failAt.Add(31 * time.Minute))
			svc.checkDue(ctx)
			if builds != 0 {
				t.Fatalf("the %s was tried again after its window closed", tc.slot)
			}
			if _, ok := svc.store.load("2026-10-09-" + tc.slot); ok {
				t.Fatalf("the %s was made after its window closed", tc.slot)
			}
			missed := 0
			for _, moment := range openMoments(t, rig.store, string(engagement.KindBriefMissed)) {
				if moment.SubjectID == "2026-10-09-"+tc.slot {
					missed++
				}
			}
			if missed != 1 {
				t.Fatalf("want one missed notice for the %s, got %d", tc.slot, missed)
			}
		})
	}
}

// A morning whose first tries failed is still made later in the morning,
// labelled late, and is never announced missed.
func TestMobileBriefFailedMorningIsMadeLaterAndLabelledLate(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, rig := missedBriefRig(t, clock)
	base := svc.gather
	failing := true
	svc.gather = func(ctx context.Context, slot string, start, asOf time.Time) (mobileBriefInput, error) {
		if failing {
			return mobileBriefInput{}, context.DeadlineExceeded
		}
		return base(ctx, slot, start, asOf)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		svc.checkDue(ctx)
		clock.Advance(mobileBriefRetryBackoff(i + 1))
	}
	failing = false
	clock.Set(mobileBriefTestDay(9, 0))
	svc.checkDue(ctx)
	morning, ok := svc.store.load("2026-10-09-morning")
	if !ok {
		t.Fatal("the morning was not made after its failed tries")
	}
	if morning.Generation == nil || morning.Generation.Reason != "late" || morning.Generation.ScheduledAt != "2026-10-09T07:00:00Z" || morning.Generation.Message != "Your morning brief, a bit late." {
		t.Fatalf("the late morning is not labelled: %+v", morning.Generation)
	}
	runs := svc.runs.list()
	if len(runs) != 1 || runs[0].Status != mobileBriefRunOK {
		t.Fatalf("runs = %+v", runs)
	}
	if health := svc.Health(); health.PendingSlot != "" || health.RetryAt != "" {
		t.Fatalf("a made brief still reads pending: %+v", health)
	}
	clock.Set(mobileBriefTestDay(18, 30))
	svc.checkDue(ctx)
	if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 0 {
		t.Fatalf("a morning made late was announced missed: %+v", moments)
	}
}

// Isoform closed at 06:55 and started again at 10:00 (a restart): the morning
// is made on start, labelled late, without a missed notice.
func TestMobileBriefMorningMissedWhileClosedIsMadeOnStart(t *testing.T) {
	for _, adaptive := range []bool{false, true} {
		t.Run(catchUpMode(adaptive), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(10, 0)}
			svc, rig, _ := catchUpRig(t, clock, adaptive)
			if health := svc.Health(); health.PendingSlot != "morning" || health.PendingAt != "2026-10-09T07:00:00Z" {
				t.Fatalf("Today does not see the morning on its way: %+v", health)
			}
			svc.checkDue(context.Background())
			morning, ok := svc.store.load("2026-10-09-morning")
			if !ok || morning.Generation == nil || morning.Generation.Reason != "late" {
				t.Fatalf("the morning was not made on start: ok=%v generation=%+v", ok, morning.Generation)
			}
			if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 0 {
				t.Fatalf("a morning made on start was announced missed: %+v", moments)
			}
		})
	}
}

// Sources that change during collection (a background write) start the build
// over at once instead of failing the slot.
func TestMobileBriefSourcesChangedCollectsAgainAtOnce(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
	svc, _ := missedBriefRig(t, clock)
	base := svc.gather
	calls := 0
	svc.gather = func(ctx context.Context, slot string, start, asOf time.Time) (mobileBriefInput, error) {
		calls++
		if calls < mobileBriefSourceRetries {
			return mobileBriefInput{}, errMobileBriefSourcesChanged
		}
		return base(ctx, slot, start, asOf)
	}
	svc.checkDue(context.Background())
	if !svc.store.exists("2026-10-09-morning") || calls != mobileBriefSourceRetries {
		t.Fatalf("the build did not collect again: calls=%d", calls)
	}
	if runs := svc.runs.list(); len(runs) != 1 || runs[0].Status != mobileBriefRunOK {
		t.Fatalf("runs = %+v", runs)
	}
	// Changes on every try count as one failed attempt, retried later.
	clock.Set(mobileBriefTestDay(18, 0))
	calls = 0
	svc.gather = func(context.Context, string, time.Time, time.Time) (mobileBriefInput, error) {
		calls++
		return mobileBriefInput{}, errMobileBriefSourcesChanged
	}
	svc.checkDue(context.Background())
	runs := svc.runs.list()
	if calls != mobileBriefSourceRetries || len(runs) != 2 || runs[1].Status != mobileBriefRunFailed || runs[1].Attempts != 1 {
		t.Fatalf("calls=%d runs=%+v", calls, runs)
	}
	// The first such failed attempt is retried right away, not after the
	// backoff; a second one in a row waits.
	select {
	case <-svc.wake:
	default:
		t.Fatal("a sources-changed failure did not ask for an immediate retry")
	}
	if svc.retryAfter.After(clock.Now()) {
		t.Fatalf("a sources-changed failure waits until %s", svc.retryAfter)
	}
	calls = 0
	svc.checkDue(context.Background())
	if runs := svc.runs.list(); calls != mobileBriefSourceRetries || runs[1].Attempts != 2 || !svc.retryAfter.Equal(clock.Now().Add(mobileBriefRetryBackoff(2))) {
		t.Fatalf("calls=%d runs=%+v retryAfter=%s", calls, runs, svc.retryAfter)
	}
	select {
	case <-svc.wake:
		t.Fatal("a second sources-changed failure in a row retried at once")
	default:
	}
}

// A morning that keeps failing never holds back the evening slot.
func TestMobileBriefFailingMorningDoesNotDelayTheEvening(t *testing.T) {
	for _, adaptive := range []bool{false, true} {
		t.Run(catchUpMode(adaptive), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(11, 50)}
			svc, _, _ := catchUpRig(t, clock, adaptive)
			base := svc.gather
			mornings := 0
			svc.gather = func(ctx context.Context, slot string, start, asOf time.Time) (mobileBriefInput, error) {
				if slot == mobileBriefSlotMorning {
					mornings++
					return mobileBriefInput{}, context.DeadlineExceeded
				}
				return base(ctx, slot, start, asOf)
			}
			for _, run := range []mobileBriefRun{{BriefID: "2026-10-09-morning", Slot: "morning", ScheduledAt: "2026-10-09T07:00:00Z", Status: mobileBriefRunFailed, Attempts: 5}} {
				if _, err := svc.runs.record(run); err != nil {
					t.Fatal(err)
				}
			}
			svc.checkDue(context.Background())
			if mornings != 1 || !svc.retryAfter.After(mobileBriefTestDay(12, 0)) {
				t.Fatalf("the sixth failure should wait past noon: tries=%d retry=%s", mornings, svc.retryAfter)
			}
			// Its window closed at noon (today-review v1.1.21 R49): no more tries.
			clock.Set(mobileBriefTestDay(12, 30))
			svc.checkDue(context.Background())
			if mornings != 1 {
				t.Fatalf("the morning was tried %d times, want no try after noon", mornings)
			}
			clock.Set(mobileBriefTestDay(18, 0))
			svc.checkDue(context.Background())
			if !svc.store.exists("2026-10-09-evening") {
				t.Fatal("the morning's backoff held back the evening brief")
			}
		})
	}
}

func TestMobileBriefRetryBackoff(t *testing.T) {
	want := []time.Duration{2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, delay := range want {
		if got := mobileBriefRetryBackoff(i + 1); got != delay {
			t.Fatalf("backoff after %d failures = %s, want %s", i+1, got, delay)
		}
	}
}

func TestMobileBriefMissedSlotsSeeYesterdayAfterMidnight(t *testing.T) {
	settings := defaultMobileBriefSettings()
	none := func(string) bool { return false }
	since := mobileBriefTestDay(0, 0)
	// 02:00 on the 10th: both slots of the 9th have expired;
	// the morning of the 9th is missed too; nothing of the 10th is due yet, and
	// the 8th is before the schedule was watched.
	missed := mobileBriefMissedSlots(mobileBriefTestDay(2, 0).AddDate(0, 0, 1), time.UTC, settings, none, since)
	ids := []string{}
	for _, occurrence := range missed {
		ids = append(ids, occurrence.BriefID)
	}
	if len(ids) != 2 || ids[0] != "2026-10-09-morning" || ids[1] != "2026-10-09-evening" {
		t.Fatalf("missed after midnight = %v", ids)
	}
	// A stored brief is never missed.
	has := func(id string) bool { return id == "2026-10-09-evening" }
	if missed := mobileBriefMissedSlots(mobileBriefTestDay(2, 0).AddDate(0, 0, 1), time.UTC, settings, has, since); len(missed) != 1 {
		t.Fatalf("a stored evening must not be missed: %+v", missed)
	}
	// The stored evening is excluded after its grace has elapsed.
	if missed := mobileBriefMissedSlots(mobileBriefTestDay(23, 59), time.UTC, settings, has, since); len(missed) != 1 || missed[0].Slot != "morning" {
		t.Fatalf("before the window closes only the morning is missed: %+v", missed)
	}
}

func TestMobileBriefFailedSlotBecomesMissedAfterMidnight(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(18, 5)}
	svc, rig := missedBriefRig(t, clock)
	svc.gather = func(context.Context, string, time.Time, time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{}, context.DeadlineExceeded
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		svc.checkDue(ctx)
		clock.Advance(mobileBriefRetryBackoff(i+1) + time.Minute)
	}
	if runs := svc.runs.list(); len(runs) != 2 || runs[1].Status != mobileBriefRunFailed || runs[1].Attempts != 3 {
		t.Fatalf("runs before midnight = %+v", runs)
	}
	// The morning slot was missed too (no brief all day), and announced once.
	clock.Set(mobileBriefTestDay(0, 30).AddDate(0, 0, 1))
	svc.checkDue(ctx)
	runs := svc.runs.list()
	byID := map[string]mobileBriefRun{}
	for _, run := range runs {
		byID[run.BriefID] = run
	}
	evening := byID["2026-10-09-evening"]
	if evening.Status != mobileBriefRunMissed || evening.Attempts != 3 || !evening.Notified {
		t.Fatalf("the failing evening slot must read missed after its window closed, keeping its attempts: %+v", evening)
	}
	if morning := byID["2026-10-09-morning"]; morning.Status != mobileBriefRunMissed || !morning.Notified {
		t.Fatalf("morning = %+v", morning)
	}
	if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 2 {
		t.Fatalf("one notice per slot, got %d", len(moments))
	}
	if health := svc.Health(); health.FailingSlot != "" || health.MissedRuns != 2 {
		t.Fatalf("health after midnight = %+v", health)
	}
	// Later ticks leave the records alone.
	clock.Advance(time.Hour)
	svc.checkDue(ctx)
	if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 2 {
		t.Fatal("a later tick must not announce again")
	}
}

func TestMobileBriefScheduleTurnedBackOnDoesNotReportTheOffPeriod(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(10, 0)}
	svc, rig := missedBriefRig(t, clock)
	ctx := context.Background()
	svc.gather = func(context.Context, string, time.Time, time.Time) (mobileBriefInput, error) {
		return mobileBriefInput{}, context.DeadlineExceeded
	}
	// Off for a day: the morning and evening slots of that day are not missed.
	if _, err := svc.SetSchedule(MobileBriefSchedule{Enabled: false, Morning: "07:00", Evening: "18:00"}); err != nil {
		t.Fatal(err)
	}
	// Back on at 14:00 the next day, after the morning's catch-up window.
	clock.Set(mobileBriefTestDay(14, 0).AddDate(0, 0, 1))
	svc.checkDue(ctx)
	if _, err := svc.SetSchedule(MobileBriefSchedule{Enabled: true, Morning: "07:00", Evening: "18:00"}); err != nil {
		t.Fatal(err)
	}
	if enabledAt := svc.config.load().EnabledAt; enabledAt != "2026-10-10T14:00:00Z" {
		t.Fatalf("enabledAt = %q", enabledAt)
	}
	svc.checkDue(ctx)
	if moments := openMoments(t, rig.store, string(engagement.KindBriefMissed)); len(moments) != 0 {
		t.Fatalf("slots from the off period must not be missed: %+v", moments)
	}
	if runs := svc.runs.list(); len(runs) != 0 {
		t.Fatalf("no run may be recorded for the off period: %+v", runs)
	}
	// The first slot after turning it back on counts after its grace closes.
	clock.Set(mobileBriefTestDay(0, 30).AddDate(0, 0, 2))
	svc.checkDue(ctx)
	runs := svc.runs.list()
	if len(runs) != 1 || runs[0].BriefID != "2026-10-10-evening" || runs[0].Status != mobileBriefRunMissed {
		t.Fatalf("runs after the first watched window = %+v", runs)
	}
	// Turning it on again while already on keeps the first enabled time.
	if _, err := svc.SetSchedule(MobileBriefSchedule{Enabled: true, Morning: "07:00", Evening: "18:00"}); err != nil {
		t.Fatal(err)
	}
	if enabledAt := svc.config.load().EnabledAt; enabledAt != "2026-10-10T14:00:00Z" {
		t.Fatalf("enabledAt must not move while on: %q", enabledAt)
	}
}

func TestMobileBriefOnRequestStopsFailedScheduledRetry(t *testing.T) {
	for _, adaptive := range []bool{false, true} {
		t.Run(catchUpMode(adaptive), func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
			svc, _, _ := catchUpRig(t, clock, adaptive)
			original := svc.gather
			scheduled := 0
			svc.gather = func(ctx context.Context, slot string, start, asOf time.Time) (mobileBriefInput, error) {
				if slot == mobileBriefSlotMorning {
					scheduled++
					return mobileBriefInput{}, context.DeadlineExceeded
				}
				return original(ctx, slot, start, asOf)
			}
			svc.checkDue(context.Background())
			clock.Set(mobileBriefTestDay(9, 1))
			if _, err := svc.generate(context.Background(), mobileBriefSlotOnRequest, mobileBriefReasonSettings); err != nil {
				t.Fatal(err)
			}
			svc.checkDue(context.Background())
			if scheduled != 1 {
				t.Fatalf("scheduled attempts=%d", scheduled)
			}
			health := svc.Health()
			if health.PendingSlot != "" || health.RetryAt != "" || health.NextSlot == mobileBriefSlotMorning && health.NextRunAt == formatMobileBriefTime(clock.Now()) {
				t.Fatalf("health=%+v", health)
			}
		})
	}
}

func TestMobileBriefRetryTimeDoesNotSlide(t *testing.T) {
	settings := mobileBriefSettings{Enabled: true, Morning: "07:00", Evening: "18:00"}
	retry := mobileBriefTestDay(8, 45)
	facts := mobileBriefScheduleFacts{RetryAt: func(string) time.Time { return retry }}
	for _, now := range []time.Time{mobileBriefTestDay(8, 46), mobileBriefTestDay(8, 47)} {
		got, _ := mobileBriefSlotNextRun(now, time.UTC, settings, mobileBriefSlotMorning, facts)
		if !got.Equal(retry) {
			t.Fatalf("at %v retry moved to %v", now, got)
		}
	}
}

func TestMobileBriefFailureRecordsProvenSignedOutCause(t *testing.T) {
	for _, tc := range []struct {
		name, backend, cause, assistant string
	}{
		{"known assistant", "codex", "signed_out", "codex"},
		{"unknown assistant", "native", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 0)}
			svc, _ := missedBriefRig(t, clock)
			occurrence := mobileBriefSlotOccurrence{BriefID: "2026-10-09-morning", Slot: mobileBriefSlotMorning, ScheduledAt: clock.Now()}
			err := mobileBriefAttributeAssistantError(&native.HTTPError{Status: 401}, tc.backend)
			svc.recordFailure(occurrence, err)
			runs := svc.runs.list()
			if len(runs) != 1 || runs[0].FailureCause != tc.cause || runs[0].FailureAssistant != tc.assistant {
				t.Fatalf("runs=%+v", runs)
			}
		})
	}
}

func TestBriefRunHistoryTransitions(t *testing.T) {
	for _, tc := range []struct {
		state   mobileBriefRunsState
		allowed bool
	}{
		{mobileBriefRunsMissing, true}, {mobileBriefRunsValid, true},
		{mobileBriefRunsUnsupported, false}, {mobileBriefRunsUnreadable, false},
		{mobileBriefRunsState(99), false},
	} {
		if got := mobileBriefRunsTransition(tc.state); got != tc.allowed {
			t.Errorf("state %d: allowed = %v", tc.state, got)
		}
	}
}

func TestBriefRunHistoryPreservedOnReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		state          mobileBriefRunsState
		directory      bool
	}{
		{"malformed", "{broken", mobileBriefRunsUnreadable, false},
		{"future version", `{"version":2,"runs":[{"briefId":"kept"}]}`, mobileBriefRunsUnsupported, false},
		{"read error", "", mobileBriefRunsUnreadable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMobileBriefRunStore(t.TempDir())
			if tc.directory {
				if err := os.Mkdir(store.path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(store.path, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			_, state, err := store.loadLocked()
			if state != tc.state || err == nil {
				t.Fatalf("state %v error %v", state, err)
			}
			if _, err := store.record(mobileBriefRun{BriefID: "new", Status: mobileBriefRunFailed}); err == nil {
				t.Fatal("record accepted unreadable history")
			}
			if err := store.markNotified("new"); err == nil {
				t.Fatal("notification accepted unreadable history")
			}
			if err := store.markEarlyReplaced("new"); err == nil {
				t.Fatal("replacement accepted unreadable history")
			}
			store.firstSeen(time.Now())
			if !tc.directory {
				got, err := os.ReadFile(store.path)
				if err != nil || string(got) != tc.contents {
					t.Fatalf("history changed: %q, %v", got, err)
				}
			} else if info, err := os.Stat(store.path); err != nil || !info.IsDir() {
				t.Fatalf("read error path changed: %v", err)
			}
		})
	}
	store := newMobileBriefRunStore(t.TempDir())
	_, state, err := store.loadLocked()
	if err != nil || state != mobileBriefRunsMissing {
		t.Fatalf("first use state %v error %v", state, err)
	}
	if _, err := store.record(mobileBriefRun{BriefID: "new", Status: mobileBriefRunFailed}); err != nil {
		t.Fatal(err)
	}
	_, state, err = store.loadLocked()
	if err != nil || state != mobileBriefRunsValid {
		t.Fatalf("saved state %v error %v", state, err)
	}
}
