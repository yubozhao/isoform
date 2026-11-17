package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

// quietTestSends records every channel the service tries beyond the rig's own
// push and desktop recorders, including the chat report, so a quiet kind can
// be proven to reach none of them.
type quietTestSends struct {
	texted   int
	reported int
}

func (s *quietTestSends) wire(rig *engagementTestRig) {
	rig.svc.deliver.Text = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		s.texted++
		return nil
	}
	rig.svc.deliver.Report = func(engagement.Moment, engagementText) bool {
		s.reported++
		return true
	}
}

func (s *quietTestSends) totals(rig *engagementTestRig) (pushed, desktop, texted, reported int) {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	return len(rig.pushed), len(rig.desktop), s.texted, s.reported
}

func TestEngagementQuietKindNeverInterruptsAndCountsOnTodayQuiet(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	sends := &quietTestSends{}
	sends.wire(rig)
	if err := rig.svc.SetKindQuiet(ctx, engagement.KindCapturePaused, true); err != nil {
		t.Fatal(err)
	}
	// A fresh pause (< 1h) would be a status line in the chat; a stale one
	// (>= 1h) would interrupt as time-sensitive. Both stay quiet.
	fresh, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindCapturePaused, SubjectID: "screen", CreatedAt: rig.clock.Add(-20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindCapturePaused, SubjectID: "screen", DedupeKey: "capture_paused:screen:older", CreatedAt: rig.clock.Add(-2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if pushed, desktop, texted, reported := sends.totals(rig); pushed+desktop+texted+reported != 0 {
		t.Fatalf("a quiet kind reached a channel: push=%d desktop=%d text=%d chat=%d", pushed, desktop, texted, reported)
	}
	for _, id := range []string{fresh.ID, stale.ID} {
		row, err := rig.store.GetEngagementMoment(ctx, id)
		if err != nil || row == nil {
			t.Fatal(err)
		}
		if row.State != storage.EngagementStateSent || row.Channel != string(engagement.ChannelTodayQuiet) || row.SentAt == 0 {
			t.Fatalf("a quiet delivery is recorded on today_quiet: %+v", row)
		}
		receipts, err := rig.store.ListEngagementReceipts(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		sentOnQuiet := false
		for _, r := range receipts {
			if r.Channel == string(engagement.ChannelTodayQuiet) && r.State == storage.EngagementReceiptSent {
				sentOnQuiet = true
			}
		}
		if !sentOnQuiet {
			t.Fatalf("the sent receipt names the quiet channel: %+v", receipts)
		}
	}
	stats, err := rig.svc.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats[string(engagement.KindCapturePaused)].Sent != 2 {
		t.Fatalf("the 30-day counts stay honest: %+v", stats[string(engagement.KindCapturePaused)])
	}
}

func TestEngagementUnquietRestoresDelivery(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	sends := &quietTestSends{}
	sends.wire(rig)
	kind := engagement.KindMeetingPrep
	if err := rig.svc.SetKindQuiet(ctx, kind, true); err != nil {
		t.Fatal(err)
	}
	first, err := rig.svc.Submit(ctx, engagement.Moment{Kind: kind, SubjectID: "h1", Params: map[string]string{"title": "The export", "question": "a project code."}})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if pushed, desktop, texted, reported := sends.totals(rig); pushed+desktop+texted+reported != 0 {
		t.Fatalf("quiet delivery interrupted: push=%d desktop=%d text=%d chat=%d", pushed, desktop, texted, reported)
	}
	if err := rig.svc.SetKindQuiet(ctx, kind, false); err != nil {
		t.Fatal(err)
	}
	second, err := rig.svc.Submit(ctx, engagement.Moment{Kind: kind, SubjectID: "h2", Params: map[string]string{"title": "The import", "question": "a name."}})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if pushed, _, _, _ := sends.totals(rig); pushed != 1 {
		t.Fatalf("un-quieting restores normal delivery: push=%d", pushed)
	}
	row, _ := rig.store.GetEngagementMoment(ctx, second.ID)
	if row.Channel != "push" {
		t.Fatalf("the new delivery went to %+v", row)
	}
	// The quiet delivery is unchanged: it was honest when recorded.
	before, _ := rig.store.GetEngagementMoment(ctx, first.ID)
	if before.Channel != string(engagement.ChannelTodayQuiet) {
		t.Fatalf("the quiet delivery keeps its channel: %+v", before)
	}
}

func TestEngagementDecisionDueCannotBeQuiet(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if err := rig.svc.SetKindQuiet(ctx, engagement.KindDecisionDue, true); !errors.Is(err, engagement.ErrKindCannotBeQuiet) {
		t.Fatalf("decision_due must refuse quiet: %v", err)
	}
	var typed *engagement.KindCannotBeQuietError
	if err := rig.svc.SetKindQuiet(ctx, engagement.KindDecisionDue, true); !errors.As(err, &typed) || typed.Kind != engagement.KindDecisionDue {
		t.Fatalf("structured quiet refusal: %v", err)
	}
	if rig.svc.Prefs().KindQuietOnToday(engagement.KindDecisionDue) {
		t.Fatal("a refused quiet must not stick")
	}
	// Un-quieting is always allowed, and an unknown kind is still unknown.
	if err := rig.svc.SetKindQuiet(ctx, engagement.KindDecisionDue, false); err != nil {
		t.Fatal(err)
	}
	if err := rig.svc.SetKindQuiet(ctx, engagement.Kind("not_a_kind"), true); err == nil {
		t.Fatal("an unknown kind must be refused")
	}

	app := &App{store: rig.store, engagement: rig.svc}
	if _, err := app.SetEngagementKindQuiet("decision_due", true); !errors.Is(err, engagement.ErrKindCannotBeQuiet) {
		t.Fatalf("the binding refuses with the typed error: %v", err)
	}
	if _, err := app.SetEngagementKindQuiet("not_a_kind", true); err == nil {
		t.Fatal("the binding refuses an unknown kind")
	}
	if _, err := (&App{}).SetEngagementKindQuiet("capture_paused", true); !errors.Is(err, errEngagementUnavailable) {
		t.Fatalf("quiet without a service: %v", err)
	}
}

func TestEngagementSettingsViewReportsQuietPerKind(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if err := rig.svc.SetKindQuiet(ctx, engagement.KindCapturePaused, true); err != nil {
		t.Fatal(err)
	}
	view, err := engagementSettingsView(ctx, rig.svc)
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]EngagementKindView{}
	for _, kind := range view.Kinds {
		byKind[kind.Kind] = kind
	}
	if _, exists := byKind[string(engagement.KindCapturePaused)]; exists {
		t.Fatal("retired capture notification remains in Settings")
	}
	decision := byKind[string(engagement.KindDecisionDue)]
	if decision.QuietOnToday || decision.QuietAllowed {
		t.Fatalf("decision_due can never be quiet: %+v", decision)
	}
	brief := byKind[string(engagement.KindBriefReady)]
	if brief.QuietOnToday || !brief.QuietAllowed {
		t.Fatalf("an ordinary kind: %+v", brief)
	}
}

func TestEngagementQuietSurvivesRestartAndOtherWrites(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	kind := engagement.KindCapturePaused
	if err := rig.svc.SetKindQuiet(ctx, kind, true); err != nil {
		t.Fatal(err)
	}
	// A switch and a level rewrite keep the quiet mode.
	if err := rig.svc.SetKindEnabled(ctx, kind, false); err != nil {
		t.Fatal(err)
	}
	if err := rig.svc.SetLevel(ctx, engagement.LevelNormal); err != nil {
		t.Fatal(err)
	}
	control, err := rig.store.GetEngagementKindControl(ctx, string(kind))
	if err != nil || control.DeliveryMode != string(engagement.KindDeliveryQuietOnToday) {
		t.Fatalf("the quiet mode is persisted with the control: %+v %v", control, err)
	}
	fresh := newEngagementService(rig.store, nil, engagementDelivery{})
	fresh.now = rig.svc.now
	if err := fresh.load(ctx); err != nil {
		t.Fatal(err)
	}
	if !fresh.Prefs().KindQuietOnToday(kind) {
		t.Fatal("the quiet mode must survive a restart")
	}
	// Un-quieting persists too.
	if err := fresh.SetKindQuiet(ctx, kind, false); err != nil {
		t.Fatal(err)
	}
	again := newEngagementService(rig.store, nil, engagementDelivery{})
	if err := again.load(ctx); err != nil {
		t.Fatal(err)
	}
	if again.Prefs().KindQuietOnToday(kind) {
		t.Fatal("un-quieting must survive a restart")
	}
}

func TestEngagementQuietBypassesNotificationRateLimits(t *testing.T) {
	rig := newEngagementTestRig(t)
	kind := engagement.KindTopicFinds // Normally digestable, daily cap 1.
	if err := rig.svc.SetKindQuiet(t.Context(), kind, true); err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"one", "two"} {
		row, err := rig.svc.Submit(t.Context(), engagement.Moment{Kind: kind, SubjectID: subject})
		if err != nil {
			t.Fatal(err)
		}
		rig.svc.Pass(t.Context())
		got, err := rig.store.GetEngagementMoment(t.Context(), row.ID)
		if err != nil || got.Channel != "today_quiet" {
			t.Fatalf("quiet line waited for a cap or digest: %+v %v", got, err)
		}
	}
	if rig.svc.state.SentByKindToday[kind] != 0 || len(rig.svc.state.LastSentByKey) != 0 {
		t.Fatalf("quiet delivery consumed pacing: %+v", rig.svc.state)
	}
}

func TestGetTodayQuietLinesGroupsByKindWithRealIntervals(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	app := &App{store: rig.store, engagement: rig.svc}
	for _, kind := range []engagement.Kind{engagement.KindCapturePaused, engagement.KindWorkFinished, engagement.KindSuggestion} {
		if err := rig.svc.SetKindQuiet(ctx, kind, true); err != nil {
			t.Fatal(err)
		}
	}
	loc := rig.clock.Location()
	// Two finished-work moments and one capture outage, all today.
	work1, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "h1", CreatedAt: time.Date(2026, 10, 12, 9, 5, 0, 0, loc)})
	work2, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "h2", CreatedAt: time.Date(2026, 10, 12, 9, 50, 0, 0, loc)})
	outageStart := time.Date(2026, 10, 12, 9, 40, 0, 0, loc)
	outage, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindCapturePaused, SubjectID: "screen", CreatedAt: outageStart})
	// A deliberate pause from the pause log, and one from yesterday that must
	// not show.
	pauseStart := time.Date(2026, 10, 12, 8, 40, 0, 0, loc)
	pauseEnd := time.Date(2026, 10, 12, 8, 55, 0, 0, loc)
	app.recordObservationPause(true, time.Date(2026, 10, 11, 16, 0, 0, 0, loc))
	app.recordObservationPause(false, time.Date(2026, 10, 11, 17, 0, 0, 0, loc))
	app.recordObservationPause(true, pauseStart)
	app.recordObservationPause(false, pauseEnd)

	rig.svc.Pass(ctx)
	for _, id := range []string{work1.ID, work2.ID, outage.ID} {
		row, _ := rig.store.GetEngagementMoment(ctx, id)
		if row.State != storage.EngagementStateSent || row.Channel != string(engagement.ChannelTodayQuiet) {
			t.Fatalf("fixture: quiet delivery %+v", row)
		}
	}

	lines, err := app.GetTodayQuietLines()
	if err != nil {
		t.Fatal(err)
	}
	// Spec order puts work_finished before capture_paused; suggestion had
	// nothing today and shows no line.
	if len(lines) != 2 || lines[0].Kind != string(engagement.KindWorkFinished) || lines[1].Kind != string(engagement.KindCapturePaused) {
		t.Fatalf("quiet lines: %+v", lines)
	}
	work := lines[0]
	if work.Count != 2 || len(work.Intervals) != 0 {
		t.Fatalf("work line: %+v", work)
	}
	if work.FirstAt != time.Date(2026, 10, 12, 9, 5, 0, 0, loc).UTC().Format(time.RFC3339) ||
		work.LastAt != time.Date(2026, 10, 12, 9, 50, 0, 0, loc).UTC().Format(time.RFC3339) {
		t.Fatalf("work line times: %+v", work)
	}
	capture := lines[1]
	if capture.Count != 1 || len(capture.Intervals) != 2 {
		t.Fatalf("capture line: %+v", capture)
	}
	// The real pause and outage stretches, sorted by start.
	pause := capture.Intervals[0]
	if pause.Start != pauseStart.UTC().Format(time.RFC3339) || pause.End != pauseEnd.UTC().Format(time.RFC3339) {
		t.Fatalf("the pause interval is the real one: %+v", pause)
	}
	outageInterval := capture.Intervals[1]
	if outageInterval.Start != outageStart.UTC().Format(time.RFC3339) || outageInterval.End != "" {
		t.Fatalf("an outage has no recorded end: %+v", outageInterval)
	}
	if capture.FirstAt != pauseStart.UTC().Format(time.RFC3339) || capture.LastAt != outageStart.UTC().Format(time.RFC3339) {
		t.Fatalf("capture line times: %+v", capture)
	}

	// Un-quieting work_finished removes only its line.
	if err := rig.svc.SetKindQuiet(ctx, engagement.KindWorkFinished, false); err != nil {
		t.Fatal(err)
	}
	lines, err = app.GetTodayQuietLines()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Kind != string(engagement.KindCapturePaused) {
		t.Fatalf("quiet lines after un-quieting: %+v", lines)
	}
}

func TestGetTodayQuietLinesWithoutService(t *testing.T) {
	lines, err := (&App{}).GetTodayQuietLines()
	if err != nil || len(lines) != 0 {
		t.Fatalf("without a service the answer is an empty list: %+v %v", lines, err)
	}
}

func TestEngagementCapturePausedTargetIsTheSameEverywhere(t *testing.T) {
	click := engagementClickData(engagement.Moment{ID: "capture-paused-moment", Kind: engagement.KindCapturePaused})
	target, _ := engagementChatTarget(engagement.Moment{Kind: engagement.KindCapturePaused})
	if target.Kind != "settings" || target.ID != "activity" {
		t.Fatalf("the main-chat target opens %+v, want the Observation settings", target)
	}
	if click != "main-chat-item:capture-paused-moment" {
		t.Fatalf("the toast click = %q, want the card's item id", click)
	}
}

func TestEngagementQuietSkipsPendingChatReport(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "sent", true: "partial"}[partial], func(t *testing.T) {
			rig := newEngagementTestRig(t)
			ctx := t.Context()
			sends := &quietTestSends{}
			sends.wire(rig)
			row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "retry", Channels: []engagement.Channel{engagement.ChannelPush, engagement.ChannelText}})
			if err != nil {
				t.Fatal(err)
			}
			state := storage.EngagementStateSent
			if partial {
				state = storage.EngagementStateScheduled
			}
			if err := rig.store.SetEngagementMomentState(ctx, row.ID, state, "chat report", storage.EngagementMomentRow{Channel: "push", SentAt: rig.clock.UnixMilli(), HoldUntil: rig.clock.UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			if err := rig.svc.SetKindQuiet(ctx, engagement.KindWorkFinished, true); err != nil {
				t.Fatal(err)
			}
			rig.svc.Pass(ctx)
			if p, d, text, report := sends.totals(rig); p+d+text+report != 0 {
				t.Fatalf("quiet retry sent: %d %d %d %d", p, d, text, report)
			}
			got, err := rig.store.GetEngagementMoment(ctx, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Reason == "chat report" {
				t.Fatalf("quiet retry remains pending: %+v", got)
			}
		})
	}
}

func TestEngagementQuietOverridesExplicitChannelsAndDigest(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := t.Context()
	sends := &quietTestSends{}
	sends.wire(rig)
	emails, digests := 0, 0
	rig.svc.deliver.Email = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		emails++
		return nil
	}
	rig.svc.deliver.Digest = func(context.Context, []engagementDigestItem) (engagement.Channel, error) {
		digests++
		return engagement.ChannelDesktop, nil
	}
	for _, kind := range []engagement.Kind{engagement.KindPlaybookResult, engagement.KindTopicFinds} {
		if err := rig.svc.SetKindQuiet(ctx, kind, true); err != nil {
			t.Fatal(err)
		}
	}
	chosen, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindPlaybookResult, SubjectID: "chosen", Channels: []engagement.Channel{engagement.ChannelPush, engagement.ChannelDesktop, engagement.ChannelText, engagement.ChannelEmail}})
	if err != nil {
		t.Fatal(err)
	}
	finds, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindTopicFinds, SubjectID: "digest"})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	rig.clock = rig.clock.Add(4 * time.Hour)
	rig.svc.Pass(ctx)
	if p, d, text, report := sends.totals(rig); p+d+text+report+emails+digests != 0 {
		t.Fatalf("quiet escaped: push=%d desktop=%d text=%d report=%d email=%d digest=%d", p, d, text, report, emails, digests)
	}
	for _, id := range []string{chosen.ID, finds.ID} {
		row, err := rig.store.GetEngagementMoment(ctx, id)
		if err != nil || row.Channel != "today_quiet" {
			t.Fatalf("quiet receipt: %+v %v", row, err)
		}
	}
}

func TestEngagementQuietDoesNotSpendBudgetOrWaitForQuietHours(t *testing.T) {
	rig := newEngagementTestRig(t)
	kind := engagement.KindWorkFinished
	if err := rig.svc.SetKindQuiet(t.Context(), kind, true); err != nil {
		t.Fatal(err)
	}
	rig.clock = time.Date(2026, 10, 12, 23, 0, 0, 0, rig.clock.Location())
	rig.presence.ScreenLocked = true
	rig.svc.state.Day = rig.clock.Format("2006-01-02")
	rig.svc.state.InterruptsToday = 6 // A normal day has already spent its budget.
	for i := 0; i < 8; i++ {
		row, err := rig.svc.Submit(t.Context(), engagement.Moment{Kind: kind, SubjectID: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
		rig.svc.Pass(t.Context())
		got, err := rig.store.GetEngagementMoment(t.Context(), row.ID)
		if err != nil || got.Channel != "today_quiet" {
			t.Fatalf("quiet line delayed: %+v %v", got, err)
		}
	}
	if rig.svc.state.InterruptsToday != 6 || rig.svc.state.SentByKindToday[kind] != 0 {
		t.Fatalf("quiet spent notification allowance: %+v", rig.svc.state)
	}
	// Simulate the previous PR head's persisted active pacing receipt too.
	legacy, err := rig.store.QuietEngagementMomentByKey(t.Context(), "work_finished:0")
	if err != nil || legacy == nil {
		t.Fatalf("quiet fixture: %+v %v", legacy, err)
	}
	if err := rig.store.RecordEngagementReceipt(t.Context(), storage.EngagementReceiptRow{MomentID: legacy.ID, Channel: "pacing", State: storage.EngagementReceiptSent, Action: "active", At: rig.clock.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	fresh := newEngagementService(rig.store, nil, engagementDelivery{})
	fresh.now = rig.svc.now
	if err := fresh.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if fresh.state.InterruptsToday != 0 || fresh.state.SentByKindToday[kind] != 0 || len(fresh.state.LastSentByKey) != 0 {
		t.Fatalf("restart restored quiet pacing: %+v", fresh.state)
	}
	// The same once-only event does not mint another delivery receipt.
	duplicate, err := fresh.Submit(t.Context(), engagement.Moment{Kind: kind, SubjectID: "0"})
	if err != nil || duplicate.State != storage.EngagementStateSent {
		t.Fatalf("quiet event identity: %+v %v", duplicate, err)
	}
}

func TestEngagementQuietCaptureIntervalsCloseOnRecovery(t *testing.T) {
	rig := newEngagementTestRig(t)
	app := &App{store: rig.store, engagement: rig.svc}
	ctx := t.Context()
	if err := rig.svc.SetKindQuiet(ctx, engagement.KindCapturePaused, true); err != nil {
		t.Fatal(err)
	}
	tracker := newTestEngagementCaptureTracker()
	snapshot := captureSnapshot{Kind: "screen", Label: "Screen capture", Granted: true, Enabled: true, Running: true}
	sample := func(at time.Time) {
		app.observeCaptureHealth(ctx, tracker, at, func() []captureSnapshot { return []captureSnapshot{snapshot} })
	}
	sample(rig.clock.Add(-time.Minute))
	start := rig.clock
	snapshot.Running = false
	sample(start)
	rig.clock = start.Add(20 * time.Minute)
	sample(rig.clock)
	rig.svc.Pass(ctx)
	recovered := start.Add(25 * time.Minute)
	snapshot.Running = true
	sample(recovered)
	rig.clock = recovered
	// Read using a new App, proving the endpoint is durable rather than tracker state.
	lines, err := (&App{store: rig.store, engagement: rig.svc}).GetTodayQuietLines()
	if err != nil || len(lines) != 1 || len(lines[0].Intervals) != 1 {
		t.Fatalf("lines: %+v %v", lines, err)
	}
	interval := lines[0].Intervals[0]
	if interval.Start != start.UTC().Format(time.RFC3339) || interval.End != recovered.UTC().Format(time.RFC3339) {
		t.Fatalf("observed endpoints: %+v", interval)
	}
}

func TestGetTodayQuietLinesUsesLocalDSTDayAndNoPageLimit(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := t.Context()
	if err := rig.svc.SetKindQuiet(ctx, engagement.KindWorkFinished, true); err != nil {
		t.Fatal(err)
	}
	// Fall-back day is 25 hours. The final hour must remain part of today.
	rig.clock = time.Date(2026, 11, 1, 23, 45, 0, 0, rig.clock.Location())
	midnight := time.Date(2026, 11, 1, 0, 0, 0, 0, rig.clock.Location())
	for i := 0; i < 1002; i++ {
		at := midnight.Add(time.Duration(i) * time.Second)
		if i == 1001 {
			at = rig.clock
		}
		row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: fmt.Sprint(i), CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		if err := rig.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSent, "fixture", storage.EngagementMomentRow{Channel: "today_quiet", SentAt: at.UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := (&App{store: rig.store, engagement: rig.svc}).GetTodayQuietLines()
	if err != nil || len(lines) != 1 || lines[0].Count != 1002 || lines[0].LastAt != rig.clock.UTC().Format(time.RFC3339) {
		t.Fatalf("DST/day paging: %+v %v", lines, err)
	}
}

func TestEngagementQuietDedicatedHandoffReportUsesToday(t *testing.T) {
	for _, status := range []handoff.Status{handoff.StatusCompleted, handoff.StatusBlocked, handoff.StatusAborted} {
		t.Run(string(status), func(t *testing.T) {
			app := mainChatTestApp(t)
			app.engagement = newEngagementService(app.store, nil, engagementDelivery{})
			kind := engagement.KindWorkFinished
			if status != handoff.StatusCompleted {
				kind = engagement.KindWorkBlocked
			}
			if err := app.engagement.SetKindQuiet(t.Context(), kind, true); err != nil {
				t.Fatal(err)
			}
			entry := handoff.Entry{ID: "quiet-handoff", Title: "A report", Status: status, UpdatedAt: 1}
			app.postHandoffResult(entry, "The report is ready.")
			app.engagement.Pass(t.Context())
			app.postHandoffResult(entry, "The same result replayed.")
			if items := mainChatProactiveItems(t, app); len(items) != 0 {
				t.Fatalf("quiet handoff reached chat: %+v", items)
			}
			row, err := app.store.GetEngagementMoment(t.Context(), mainChatEngagementMomentID(mainChatHandoffMomentID(entry)))
			if err != nil || row == nil || row.Channel != "today_quiet" {
				t.Fatalf("dedicated quiet receipt: %+v %v", row, err)
			}
		})
	}
}

func TestEngagementQuietCaptureResetDoesNotInventRecovery(t *testing.T) {
	for _, stop := range []string{"closed_eyes", "source_missing"} {
		t.Run(stop, func(t *testing.T) {
			rig := newEngagementTestRig(t)
			app := &App{store: rig.store, engagement: rig.svc}
			if err := rig.svc.SetKindQuiet(t.Context(), engagement.KindCapturePaused, true); err != nil {
				t.Fatal(err)
			}
			tracker := newTestEngagementCaptureTracker()
			snapshot := captureSnapshot{Kind: "screen", Label: "Screen capture", Granted: true, Enabled: true, Running: true}
			sample := func() []captureSnapshot { return []captureSnapshot{snapshot} }
			start := rig.clock
			app.observeCaptureHealth(t.Context(), tracker, start.Add(-time.Minute), sample)
			snapshot.Running = false
			app.observeCaptureHealth(t.Context(), tracker, start, sample)
			rig.clock = start.Add(20 * time.Minute)
			app.observeCaptureHealth(t.Context(), tracker, rig.clock, sample)
			rig.svc.Pass(t.Context())
			rig.clock = rig.clock.Add(5 * time.Minute)
			switch stop {
			case "closed_eyes":
				app.closedEyes.enabled.Store(true)
				app.observeCaptureHealth(t.Context(), tracker, rig.clock, func() []captureSnapshot { t.Fatal("sampled while paused"); return nil })
			case "source_missing":
				app.observeCaptureHealth(t.Context(), tracker, rig.clock, func() []captureSnapshot { return nil })
			}
			lines, err := app.GetTodayQuietLines()
			if err != nil || len(lines) != 1 || len(lines[0].Intervals) != 1 || lines[0].Intervals[0].End != "" {
				t.Fatalf("reset invented recovery: %+v %v", lines, err)
			}
		})
	}
}

func TestEngagementQuietChangeWaitsForInFlightDelivery(t *testing.T) {
	rig := newEngagementTestRig(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var published atomic.Bool
	rig.svc.deliver.Push = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		close(entered)
		<-release
		published.Store(true)
		return nil
	}
	if _, err := rig.svc.Submit(t.Context(), engagement.Moment{Kind: engagement.KindMeetingPrep, SubjectID: "inflight"}); err != nil {
		t.Fatal(err)
	}
	passDone := make(chan struct{})
	go func() { rig.svc.Pass(t.Context()); close(passDone) }()
	<-entered
	// Witness the barrier while publication is actually in flight, without
	// sleep-based assertions about whether another goroutine has been scheduled.
	if rig.svc.deliveryMu.TryLock() {
		rig.svc.deliveryMu.Unlock()
		t.Fatal("in-flight delivery has no mode-change barrier")
	}
	rig.svc.settingsChanged = func(engagement.Prefs) {
		if !published.Load() {
			t.Error("mode change overtook the in-flight publication")
		}
	}
	changed := make(chan error, 1)
	go func() { changed <- rig.svc.SetKindQuiet(t.Context(), engagement.KindMeetingPrep, true) }()
	releaseOnce.Do(func() { close(release) })
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	<-passDone
	if !published.Load() {
		t.Fatal("mode change returned without finishing the previous publication")
	}
	// Every publication after the setter returns uses the new mode.
	rig.svc.deliver.Push = func(context.Context, storage.EngagementMomentRow, engagement.Moment, engagement.Verdict, engagementText) error {
		t.Error("published on old mode after setter returned")
		return nil
	}
	row, err := rig.svc.Submit(t.Context(), engagement.Moment{Kind: engagement.KindMeetingPrep, SubjectID: "after-change"})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(t.Context())
	got, err := rig.store.GetEngagementMoment(t.Context(), row.ID)
	if err != nil || got.Channel != "today_quiet" {
		t.Fatalf("new mode receipt: %+v %v", got, err)
	}
}

func TestGetTodayQuietLinesUsesQuietReceiptDayForPartialDelivery(t *testing.T) {
	rig := newEngagementTestRig(t)
	previous := rig.clock.AddDate(0, 0, -1)
	row, err := rig.svc.Submit(t.Context(), engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "partial-yesterday", CreatedAt: previous, Channels: []engagement.Channel{engagement.ChannelText}})
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.store.SetEngagementMomentState(t.Context(), row.ID, storage.EngagementStateScheduled, "partly sent", storage.EngagementMomentRow{Channel: "push", SentAt: previous.UnixMilli(), HoldUntil: rig.clock.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := rig.svc.SetKindQuiet(t.Context(), engagement.KindWorkFinished, true); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(t.Context())
	got, err := rig.store.GetEngagementMoment(t.Context(), row.ID)
	if err != nil || got.SentAt != previous.UnixMilli() || got.Channel != "push+today_quiet" {
		t.Fatalf("lost earlier send evidence: %+v %v", got, err)
	}
	lines, err := (&App{store: rig.store, engagement: rig.svc}).GetTodayQuietLines()
	if err != nil || len(lines) != 1 || lines[0].Count != 1 {
		t.Fatalf("today's quiet receipt was hidden by yesterday's push: %+v %v", lines, err)
	}
}

func TestEngagementNormalHandoffReportDoesNotWaitForPass(t *testing.T) {
	app := mainChatTestApp(t)
	rig := newEngagementTestRig(t)
	rig.svc.store, app.engagement = app.store, rig.svc
	entered, release, passDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); <-passDone })
	rig.svc.deliver.Report = func(engagement.Moment, engagementText) bool { close(entered); <-release; return true }
	if _, err := rig.svc.Submit(t.Context(), engagement.Moment{Kind: engagement.KindBriefMissed, SubjectID: "slow-report"}); err != nil {
		t.Fatal(err)
	}
	go func() { rig.svc.Pass(t.Context()); close(passDone) }()
	<-entered // The pass is blocked exactly where unrelated model copy can block.
	done := make(chan struct{})
	go func() {
		app.postHandoffResult(handoff.Entry{ID: "finished", Title: "A report", Status: handoff.StatusCompleted}, "The result is ready.")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ordinary handoff report waited for the delivery pass")
	}
	select {
	case <-passDone:
		t.Fatal("pass unexpectedly finished before handoff report")
	default:
	}
	if items := mainChatProactiveItems(t, app); len(items) != 1 || items[0].Work == nil {
		t.Fatalf("normal handoff result missing: %+v", items)
	}
	once.Do(func() { close(release) })
}

func TestEngagementQuietReleasesExistingBudgetHold(t *testing.T) {
	rig := newEngagementTestRig(t)
	rig.svc.prefs.DailyInterruptBudget = 1
	if _, err := rig.store.CreateEngagementMoment(t.Context(), storage.EngagementMomentRow{Kind: "work_blocked", DedupeKey: "spent-budget", State: storage.EngagementStateSent, Channel: "push", SentAt: rig.clock.UnixMilli(), CreatedAt: rig.clock.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	row, err := rig.svc.Submit(t.Context(), engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "held"})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(t.Context())
	held, _ := rig.store.GetEngagementMoment(t.Context(), row.ID)
	if held.Reason != "budget" {
		t.Fatalf("fixture was not budget held: %+v", held)
	}
	if err := rig.svc.SetKindQuiet(t.Context(), engagement.KindWorkFinished, true); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(t.Context())
	got, _ := rig.store.GetEngagementMoment(t.Context(), row.ID)
	if got.Channel != "today_quiet" {
		t.Fatalf("mode change kept yesterday's pacing hold: %+v", got)
	}
}

func TestEngagementQuietCompletionUsesTwoHourWindow(t *testing.T) {
	for _, delay := range []time.Duration{time.Hour, 2 * time.Hour, 2*time.Hour + time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			rig := newEngagementTestRig(t)
			if err := rig.svc.SetKindQuiet(t.Context(), engagement.KindWorkBlocked, true); err != nil {
				t.Fatal(err)
			}
			row, err := rig.svc.Submit(t.Context(), engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "item"})
			if err != nil {
				t.Fatal(err)
			}
			rig.svc.Pass(t.Context())
			completed := rig.clock.Add(delay)
			rig.svc.completion = func(context.Context, storage.EngagementMomentRow) (time.Time, error) { return completed, nil }
			rig.clock = completed
			rig.svc.Pass(t.Context())
			receipts, err := rig.store.ListEngagementReceipts(t.Context(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			acted := false
			for _, receipt := range receipts {
				if receipt.State == storage.EngagementReceiptActed {
					acted = true
				}
			}
			if acted != (delay <= 2*time.Hour) {
				t.Fatalf("quiet completion attribution at %s: acted=%v", delay, acted)
			}
		})
	}
}

func TestEngagementReminderQuietIsExplicitAndNotSuggested(t *testing.T) {
	rig := newEngagementTestRig(t)
	app := &App{store: rig.store, engagement: rig.svc}
	view, err := app.GetEngagementSettings()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range view.Kinds {
		if kind.Kind == string(engagement.KindReminder) && (!kind.QuietAllowed || kind.QuietSuggestionAllowed || kind.QuietOnToday) {
			t.Fatalf("reminder default/suggestion policy: %+v", kind)
		}
	}
	if _, err := app.SetEngagementKindQuiet("reminder", true); err != nil {
		t.Fatal(err)
	}
	due := rig.clock.Add(time.Hour)
	row, err := rig.svc.Submit(t.Context(), engagement.Moment{Kind: engagement.KindReminder, SubjectID: "my-reminder", NotBefore: due})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetEngagementKindQuiet("reminder", true); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(t.Context())
	got, _ := rig.store.GetEngagementMoment(t.Context(), row.ID)
	if got.State != storage.EngagementStateScheduled || got.SentAt != 0 {
		t.Fatalf("quiet brought reminder forward: %+v", got)
	}
	rig.clock = due
	rig.svc.Pass(t.Context())
	got, _ = rig.store.GetEngagementMoment(t.Context(), row.ID)
	if got.Channel != "today_quiet" {
		t.Fatalf("explicit quiet reminder was not respected: %+v", got)
	}
}

func TestEngagementUnquietDoesNotReplayQuietEventAfterRestart(t *testing.T) {
	rig := newEngagementTestRig(t)
	kind := engagement.KindMeetingPrep
	if err := rig.svc.SetKindQuiet(t.Context(), kind, true); err != nil {
		t.Fatal(err)
	}
	moment := engagement.Moment{Kind: kind, SubjectID: "same-slot"}
	original, err := rig.svc.Submit(t.Context(), moment)
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(t.Context())
	if err := rig.svc.SetKindQuiet(t.Context(), kind, false); err != nil {
		t.Fatal(err)
	}
	fresh := newEngagementService(rig.store, rig.svc.presence, rig.svc.deliver)
	fresh.now = rig.svc.now
	if err := fresh.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	replayed, err := fresh.Submit(t.Context(), moment)
	if err != nil || replayed.ID != original.ID {
		t.Fatalf("quiet event lost its identity on un-quiet: %+v %v", replayed, err)
	}
	fresh.Pass(t.Context())
	if len(rig.pushed)+len(rig.desktop) != 0 {
		t.Fatal("old quiet event was notified after un-quiet")
	}
	// A genuinely new event still uses ordinary delivery.
	next, err := fresh.Submit(t.Context(), engagement.Moment{Kind: kind, SubjectID: "next-slot"})
	if err != nil {
		t.Fatal(err)
	}
	fresh.Pass(t.Context())
	got, err := rig.store.GetEngagementMoment(t.Context(), next.ID)
	if err != nil || got.Channel != "push" || len(rig.pushed) != 1 {
		t.Fatalf("new event did not resume normal delivery: %+v %v", got, err)
	}
}
