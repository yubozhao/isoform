package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

type engagementTestRig struct {
	svc       *engagementService
	store     *storage.Store
	clock     time.Time
	presence  engagement.Presence
	mu        sync.Mutex
	pushed    []engagementText
	desktop   []engagementText
	pushError error
}

func newEngagementTestRig(t *testing.T) *engagementTestRig {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "engagement.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	rig := &engagementTestRig{store: store, clock: time.Date(2026, 10, 12, 10, 0, 0, 0, loc)}
	// Away from the computer, with a phone: the phone rings (notify v1.0.0).
	rig.presence = engagement.Presence{PhoneRegistered: true, PhoneLastSeen: rig.clock, MacIdle: time.Hour}
	rig.svc = newEngagementService(store, func(time.Time) engagement.Presence { return rig.presence }, engagementDelivery{
		Push: func(_ context.Context, _ storage.EngagementMomentRow, _ engagement.Moment, _ engagement.Verdict, text engagementText) error {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			if rig.pushError != nil {
				return rig.pushError
			}
			rig.pushed = append(rig.pushed, text)
			return nil
		},
		Desktop: func(_ context.Context, _ storage.EngagementMomentRow, _ engagement.Moment, _ engagement.Verdict, text engagementText) error {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			rig.desktop = append(rig.desktop, text)
			return nil
		},
	})
	rig.svc.now = func() time.Time { return rig.clock }
	return rig
}

func TestEngagementRenderLocalizesBriefSlot(t *testing.T) {
	rig := newEngagementTestRig(t)
	rig.svc.lang = func() string { return "zh-CN" }
	spec, _ := engagement.Spec(engagement.KindBriefReady)
	moment := engagement.Moment{Kind: engagement.KindBriefReady, Params: map[string]string{"slot": "morning"}}

	if got := rig.svc.render(spec, moment).Title; got != "你的早间简报已经准备好了。" {
		t.Fatalf("Chinese title = %q", got)
	}

	rig.svc.lang = func() string { return "en" }
	if got := rig.svc.render(spec, moment).Title; got != "Your morning brief is ready." {
		t.Fatalf("English title = %q", got)
	}
}

func TestEngagementServiceDeliversBlockedWorkToThePhone(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	row, err := rig.svc.Submit(ctx, engagement.Moment{
		// Routed to the phone: blocked work rings only where the person
		// chose it (notify v1.0.0 B3).
		Kind: engagement.KindWorkBlocked, SubjectID: "hf-1", Channels: []engagement.Channel{engagement.ChannelPush},
		DueAt: rig.clock.Add(time.Hour), HardDeadline: true,
		Params: map[string]string{"title": "The Q3 reply to Sam", "question": "your attention."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.DedupeKey != "work_blocked:hf-1" {
		t.Fatalf("dedupe key = %q", row.DedupeKey)
	}
	dup, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "hf-1"})
	if err != nil || dup.ID != row.ID {
		t.Fatalf("a repeat must return the open moment: %+v %v", dup, err)
	}

	rig.svc.Pass(ctx)
	if len(rig.pushed) != 1 {
		t.Fatalf("expected one push, got %d", len(rig.pushed))
	}
	text := rig.pushed[0]
	if text.PushBody != "Work is waiting on you." || text.Title != "The Q3 reply to Sam needs your attention." || text.Category != "YANSU_MOMENT" {
		t.Fatalf("rendered text = %+v", text)
	}
	if text.Actions["answer"] != "Answer" || text.Actions["stop"] != "Stop" {
		t.Fatalf("actions = %v", text.Actions)
	}
	got, _ := rig.store.GetEngagementMoment(ctx, row.ID)
	if got.State != storage.EngagementStateSent || got.Channel != "push" || got.Level != "time-sensitive" || got.SentAt == 0 {
		t.Fatalf("stored delivery = %+v", got)
	}
}

func TestEngagementServiceRoutesToTheMacWhenTheUserIsThere(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	rig.presence = engagement.Presence{MacForeground: true, MacIdle: 5 * time.Second, PhoneRegistered: true}
	if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindMeetingPrep, SubjectID: "ev1", Params: map[string]string{"title": "Design review"}}); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if len(rig.desktop) != 1 || len(rig.pushed) != 0 {
		t.Fatalf("desktop=%d push=%d", len(rig.desktop), len(rig.pushed))
	}
	// Finished work at the computer is a quiet line on Today, not a toast
	// (notify v1.0.0 rule 4); its words are the same wherever it shows.
	finished := engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "h1", Params: map[string]string{"title": "The Q3 outline"}}
	row, err := rig.svc.Submit(ctx, finished)
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if stored, _ := rig.store.GetEngagementMoment(ctx, row.ID); stored == nil || stored.Channel != string(engagement.ChannelTodayQuiet) || len(rig.desktop) != 1 || len(rig.pushed) != 0 {
		t.Fatalf("finished work at the computer = %+v desktop=%d push=%d", stored, len(rig.desktop), len(rig.pushed))
	}
	spec, _ := engagement.Spec(engagement.KindWorkFinished)
	if title := rig.svc.render(spec, finished).Title; title != "The Q3 outline is ready to review. Nothing was sent." {
		t.Fatalf("title = %q", title)
	}
}

func TestEngagementServiceHoldsThroughQuietHoursAndDigests(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	night := time.Date(2026, 10, 12, 23, 0, 0, 0, rig.clock.Location())
	rig.clock = night
	calm, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "h2"})
	meeting, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindMeetingEnded, SubjectID: "s1", Params: map[string]string{"title": "Pricing sync", "decisions": "3", "followups": "2"}})
	rig.svc.Pass(ctx)
	if len(rig.pushed) != 0 {
		t.Fatalf("nothing should go out at night, got %d", len(rig.pushed))
	}
	held, _ := rig.store.GetEngagementMoment(ctx, calm.ID)
	if held.State != storage.EngagementStateScheduled || held.Reason != "quiet hours" {
		t.Fatalf("active moment at night = %+v", held)
	}
	digest, _ := rig.store.GetEngagementMoment(ctx, meeting.ID)
	morning := time.Date(2026, 10, 13, 8, 30, 0, 0, rig.clock.Location())
	if digest.State != storage.EngagementStateScheduled || digest.HoldUntil != morning.UnixMilli() {
		t.Fatalf("passive moment should wait for the 08:30 digest: %+v", digest)
	}

	rig.clock = time.Date(2026, 10, 13, 8, 5, 0, 0, rig.clock.Location())
	rig.svc.Pass(ctx)
	if len(rig.pushed) != 1 || rig.pushed[0].PushBody != "Work finished." {
		t.Fatalf("the held moment goes out after quiet hours: %+v", rig.pushed)
	}
	rig.clock = morning.Add(time.Minute)
	rig.svc.Pass(ctx)
	// At digest time it is a quiet line on Today, never a push (B3).
	sent, _ := rig.store.GetEngagementMoment(ctx, meeting.ID)
	if len(rig.pushed) != 1 || sent.State != storage.EngagementStateSent || sent.Channel != string(engagement.ChannelTodayQuiet) || sent.Level != "passive" {
		t.Fatalf("the digest moment at digest time: %+v %+v", sent, rig.pushed)
	}
}

func TestEngagementServiceRetriesAfterADeliveryFailure(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	rig.pushError = context.DeadlineExceeded
	row, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "h3", Channels: []engagement.Channel{engagement.ChannelPush},
		Params: map[string]string{"title": "The export", "question": "a project code."}})
	rig.svc.Pass(ctx)
	got, _ := rig.store.GetEngagementMoment(ctx, row.ID)
	if got.State != storage.EngagementStateScheduled || got.Reason != "retry" || got.HoldUntil != rig.clock.Add(engagementRetryDelay).UnixMilli() {
		t.Fatalf("failed delivery should retry later: %+v", got)
	}
	rig.pushError = nil
	rig.clock = rig.clock.Add(engagementRetryDelay + time.Second)
	rig.svc.Pass(ctx)
	if len(rig.pushed) != 1 || rig.pushed[0].Title != "The export needs a project code." {
		t.Fatalf("retry did not deliver: %+v", rig.pushed)
	}
}

func TestEngagementServiceReceiptsMuteAndPause(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	// Without digest times a passive moment is scheduled for right now and
	// goes out on the next pass; one per local day keeps the daily cap happy.
	rig.svc.prefs.DigestTimes = nil
	var ids []string
	for i := 0; i < engagementAutoPauseMinSent; i++ {
		rig.clock = rig.clock.Add(24 * time.Hour)
		m, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "goal", DedupeKey: engagement.DedupeKey(engagement.KindWorkFinished, "goal", rig.clock.Format("2006-01-02"))})
		if err != nil {
			t.Fatal(err)
		}
		rig.svc.Pass(ctx)
		rig.clock = rig.clock.Add(time.Minute)
		rig.svc.Pass(ctx)
		ids = append(ids, m.ID)
	}
	if len(rig.pushed) != engagementAutoPauseMinSent {
		t.Fatalf("expected %d deliveries, got %d", engagementAutoPauseMinSent, len(rig.pushed))
	}
	for i, id := range ids {
		state := storage.EngagementReceiptOpened
		if i < 13 {
			state = storage.EngagementReceiptDismissed
		}
		if err := rig.svc.Receipt(ctx, id, "push", state, "", rig.clock); err != nil {
			t.Fatal(err)
		}
	}
	if !rig.svc.Paused(engagement.KindWorkFinished) {
		t.Fatal("a kind with a 65 percent dismiss rate after 20 sends must pause")
	}
	rig.clock = rig.clock.Add(24 * time.Hour)
	paused, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "goal", DedupeKey: "goal_check_in:goal:paused"})
	rig.svc.Pass(ctx)
	got, _ := rig.store.GetEngagementMoment(ctx, paused.ID)
	if got.State != storage.EngagementStateSuppressed || got.Reason != "kind off" {
		t.Fatalf("paused kind should be suppressed: %+v", got)
	}

	if err := rig.svc.Receipt(ctx, ids[0], "push", storage.EngagementReceiptMuted, "", rig.clock); err != nil {
		t.Fatal(err)
	}
	prefs, _ := rig.store.ListEngagementPrefs(ctx)
	if len(prefs) != 1 || prefs[0].Kind != "work_finished" || prefs[0].Enabled != 0 {
		t.Fatalf("a muted receipt must persist the kind as off: %+v", prefs)
	}
	if err := rig.svc.SetKindEnabled(ctx, engagement.KindWorkFinished, true); err != nil {
		t.Fatal(err)
	}
	if rig.svc.Paused(engagement.KindWorkFinished) {
		t.Fatal("turning a kind back on clears its pause")
	}
}

func TestEngagementServiceRetriesNeverBypassTheBudget(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	rig.svc.prefs.DailyInterruptBudget = 1
	rig.pushError = context.DeadlineExceeded
	failed, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "h1", Params: map[string]string{"title": "One"}})
	rig.svc.Pass(ctx)
	rig.pushError = nil
	if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "h2", Params: map[string]string{"title": "Two"}}); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if len(rig.pushed) != 1 || rig.pushed[0].Title != "Two is ready to review. Nothing was sent." {
		t.Fatalf("the budget's one delivery should be the second moment: %+v", rig.pushed)
	}
	rig.clock = rig.clock.Add(engagementRetryDelay + time.Second)
	rig.svc.Pass(ctx)
	got, _ := rig.store.GetEngagementMoment(ctx, failed.ID)
	if len(rig.pushed) != 1 || got.State != storage.EngagementStateScheduled || got.Reason != "budget" {
		t.Fatalf("a retry past the budget must wait for tomorrow: pushed=%d row=%+v", len(rig.pushed), got)
	}
}

func TestEngagementServiceLoadRestoresTodaysCounters(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	rig.svc.prefs.DailyInterruptBudget = 2
	for _, id := range []string{"a", "b"} {
		if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: id}); err != nil {
			t.Fatal(err)
		}
	}
	rig.svc.Pass(ctx)
	if len(rig.pushed) != 2 {
		t.Fatalf("two deliveries expected, got %d", len(rig.pushed))
	}

	fresh := newEngagementService(rig.store, func(time.Time) engagement.Presence { return rig.presence }, engagementDelivery{
		Push: func(_ context.Context, _ storage.EngagementMomentRow, _ engagement.Moment, _ engagement.Verdict, text engagementText) error {
			rig.pushed = append(rig.pushed, text)
			return nil
		},
	})
	fresh.now = func() time.Time { return rig.clock }
	fresh.prefs.DailyInterruptBudget = 2
	if err := fresh.load(ctx); err != nil {
		t.Fatal(err)
	}
	if fresh.state.InterruptsToday != 2 || fresh.state.SentByKindToday[engagement.KindWorkFinished] != 2 {
		t.Fatalf("today's counters not restored: %+v", fresh.state)
	}
	if _, err := fresh.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "c"}); err != nil {
		t.Fatal(err)
	}
	fresh.Pass(ctx)
	if len(rig.pushed) != 2 {
		t.Fatalf("a restart must not reset the daily budget, got %d deliveries", len(rig.pushed))
	}
}

func TestEngagementServiceReEnableSurvivesLaterReceipts(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	rig.svc.prefs.Enabled[engagement.KindGoalCheckIn] = true
	rig.svc.prefs.DigestTimes = nil
	var ids []string
	for i := 0; i < engagementAutoPauseMinSent; i++ {
		rig.clock = rig.clock.Add(24 * time.Hour)
		m, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindGoalCheckIn, SubjectID: "goal", DedupeKey: engagement.DedupeKey(engagement.KindGoalCheckIn, "goal", rig.clock.Format("2006-01-02"))})
		if err != nil {
			t.Fatal(err)
		}
		rig.svc.Pass(ctx)
		rig.clock = rig.clock.Add(time.Minute)
		rig.svc.Pass(ctx)
		ids = append(ids, m.ID)
	}
	for _, id := range ids[:14] {
		if err := rig.svc.Receipt(ctx, id, "push", storage.EngagementReceiptDismissed, "", rig.clock); err != nil {
			t.Fatal(err)
		}
	}
	if !rig.svc.Paused(engagement.KindGoalCheckIn) {
		t.Fatal("the kind should have paused")
	}
	rig.clock = rig.clock.Add(time.Hour)
	if err := rig.svc.SetKindEnabled(ctx, engagement.KindGoalCheckIn, true); err != nil {
		t.Fatal(err)
	}
	if err := rig.svc.Receipt(ctx, ids[15], "push", storage.EngagementReceiptOpened, "", rig.clock); err != nil {
		t.Fatal(err)
	}
	if rig.svc.Paused(engagement.KindGoalCheckIn) {
		t.Fatal("a receipt from before the re-enable must not pause the kind again")
	}
	fresh := newEngagementService(rig.store, nil, engagementDelivery{})
	fresh.now = func() time.Time { return rig.clock }
	if err := fresh.load(ctx); err != nil {
		t.Fatal(err)
	}
	if fresh.Paused(engagement.KindGoalCheckIn) {
		t.Fatal("the re-enable must survive a restart")
	}
}

func TestEngagementServiceKeepsStakesApartFromScore(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	rig.clock = time.Date(2026, 10, 12, 23, 0, 0, 0, rig.clock.Location())
	row, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "h1", Stakes: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	got, _ := rig.store.GetEngagementMoment(ctx, row.ID)
	if got.Stakes != 0.9 || got.Score == 0 || got.Score == 0.9 {
		t.Fatalf("stakes and score must be stored apart: %+v", got)
	}
	if m := momentFromRow(*got); m.Stakes != 0.9 {
		t.Fatalf("stakes must round-trip: %v", m.Stakes)
	}
}

func TestEngagementServiceLoadsPrefsAndHistoryOnStart(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if err := rig.store.UpsertEngagementPref(ctx, storage.EngagementPrefRow{Kind: "person_reappeared", Weight: 0.9, Enabled: 1, MutedUntil: rig.clock.Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	row, _ := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindBriefReady, SubjectID: "b1"})
	rig.svc.Pass(ctx)
	if len(rig.pushed) != 1 {
		t.Fatalf("brief should be pushed, got %d", len(rig.pushed))
	}

	fresh := newEngagementService(rig.store, nil, engagementDelivery{})
	fresh.now = func() time.Time { return rig.clock }
	if err := fresh.load(ctx); err != nil {
		t.Fatal(err)
	}
	prefs := fresh.Prefs()
	if prefs.Weights[engagement.KindPersonReappeared] != 0.9 || !prefs.Enabled[engagement.KindPersonReappeared] || prefs.MutedUntil[engagement.KindPersonReappeared].IsZero() {
		t.Fatalf("prefs not loaded: %+v", prefs)
	}
	if last, ok := fresh.state.LastSentByKey[row.DedupeKey]; !ok || last.UnixMilli() != rig.clock.UnixMilli() {
		t.Fatalf("delivery history not loaded: %v %v", last, ok)
	}
}

func TestEngagementServiceLevelRewritesSwitchesAndRulesAndSurvivesRestart(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	if rig.svc.Level() != engagement.LevelNormal {
		t.Fatalf("a fresh service reads as normal, got %s", rig.svc.Level())
	}
	if err := rig.svc.SetLevel(ctx, engagement.LevelFewer); err != nil {
		t.Fatal(err)
	}
	prefs := rig.svc.Prefs()
	meeting, _ := engagement.Spec(engagement.KindMeetingEnded)
	blocked, _ := engagement.Spec(engagement.KindWorkBlocked)
	if prefs.KindEnabled(meeting) || !prefs.KindEnabled(blocked) || prefs.DailyInterruptBudget != 3 || len(prefs.DigestTimes) != 1 {
		t.Fatalf("fewer was not applied: %+v", prefs)
	}
	if rig.svc.Level() != engagement.LevelFewer {
		t.Fatalf("level reads %s after fewer", rig.svc.Level())
	}

	// One hand-made change turns the dial to custom.
	if err := rig.svc.SetKindEnabled(ctx, engagement.KindMeetingEnded, true); err != nil {
		t.Fatal(err)
	}
	if rig.svc.Level() != engagement.LevelCustom {
		t.Fatalf("level reads %s after a switch, want custom", rig.svc.Level())
	}

	// A fresh service over the same store reads the persisted switches, budget,
	// digest times and quiet hours back.
	restarted := newEngagementService(rig.store, func(time.Time) engagement.Presence { return rig.presence }, engagementDelivery{})
	restarted.now = func() time.Time { return rig.clock }
	if err := restarted.load(ctx); err != nil {
		t.Fatal(err)
	}
	loaded := restarted.Prefs()
	if !loaded.KindEnabled(meeting) || !loaded.KindEnabled(blocked) || loaded.DailyInterruptBudget != 3 || len(loaded.DigestTimes) != 1 || loaded.DigestTimes[0] != 18*time.Hour {
		t.Fatalf("restart lost the level: %+v", loaded)
	}
	if loaded.Quiet != (engagement.QuietWindow{Start: 21 * time.Hour, End: 9 * time.Hour}) {
		t.Fatalf("restart lost the quiet window: %+v", loaded.Quiet)
	}
	if restarted.Level() != engagement.LevelCustom {
		t.Fatalf("restart reads %s, want custom", restarted.Level())
	}

	// Off suppresses every kind, including one that was paused by receipts.
	rig.svc.mu.Lock()
	rig.svc.paused[engagement.KindGoalCheckIn] = true
	rig.svc.mu.Unlock()
	if err := rig.svc.SetLevel(ctx, engagement.LevelOff); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkBlocked, SubjectID: "hf-off", HardDeadline: true, DueAt: rig.clock.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rig.svc.Pass(ctx)
	if len(rig.pushed)+len(rig.desktop) != 0 {
		t.Fatalf("off must deliver nothing, got %d pushes and %d toasts", len(rig.pushed), len(rig.desktop))
	}
	// Off survives a restart as Off: its quiet window is stored in minutes.
	afterOff := newEngagementService(rig.store, func(time.Time) engagement.Presence { return rig.presence }, engagementDelivery{})
	afterOff.now = func() time.Time { return rig.clock }
	if err := afterOff.load(ctx); err != nil {
		t.Fatal(err)
	}
	if afterOff.Level() != engagement.LevelOff {
		t.Fatalf("off reads as %s after a restart", afterOff.Level())
	}
	// More turns everything on and ends the auto-pause.
	if err := rig.svc.SetLevel(ctx, engagement.LevelMore); err != nil {
		t.Fatal(err)
	}
	if rig.svc.Paused(engagement.KindGoalCheckIn) {
		t.Fatal("more must resume a paused kind")
	}
	view, err := engagementSettingsView(ctx, rig.svc)
	if err != nil || view.Level != "more" {
		t.Fatalf("view level = %q %v", view.Level, err)
	}
}

func TestEngagementWeekCountsCountCompletedItemsOnce(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	mk := func(key string, receipts ...string) {
		row, err := rig.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{Kind: "work_finished", DedupeKey: key})
		if err != nil {
			t.Fatal(err)
		}
		if err := rig.store.SetEngagementMomentState(ctx, row.ID, storage.EngagementStateSent, "ok", storage.EngagementMomentRow{Channel: "desktop", SentAt: rig.clock.Add(-time.Hour).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		for _, state := range receipts {
			channel, action := "desktop", ""
			if state == storage.EngagementReceiptActed {
				channel, action = storage.EngagementCompletionChannel, storage.EngagementCompletionAction
			}
			if err := rig.store.RecordEngagementReceipt(ctx, storage.EngagementReceiptRow{MomentID: row.ID, Channel: channel, State: state, Action: action, At: rig.clock.UnixMilli()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("a", storage.EngagementReceiptDelivered, storage.EngagementReceiptOpened, storage.EngagementReceiptActed)
	mk("b", storage.EngagementReceiptDelivered, storage.EngagementReceiptDismissed)
	mk("c")
	old, err := rig.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{Kind: "work_finished", DedupeKey: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.store.SetEngagementMomentState(ctx, old.ID, storage.EngagementStateSent, "ok", storage.EngagementMomentRow{Channel: "push", SentAt: rig.clock.Add(-8 * 24 * time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	counts, err := rig.svc.WeekCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Sent != 3 || counts.ActedOn != 1 {
		t.Fatalf("week counts = %+v, want 3 sent and 1 acted on", counts)
	}
}

func TestEngagementServiceNewKindFollowsASavedOffOrFewer(t *testing.T) {
	for _, level := range []engagement.Level{engagement.LevelOff, engagement.LevelFewer} {
		rig := newEngagementTestRig(t)
		ctx := context.Background()
		if err := rig.svc.SetLevel(ctx, level); err != nil {
			t.Fatal(err)
		}
		// The level was saved by a build that did not know topic_finds yet.
		if err := rig.store.UpsertEngagementPref(ctx, storage.EngagementPrefRow{
			Kind: string(engagement.KindTopicFinds), Weight: -1, Enabled: -1, UpdatedAt: rig.clock.UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
		restarted := newEngagementService(rig.store, func(time.Time) engagement.Presence { return rig.presence }, engagementDelivery{})
		restarted.now = func() time.Time { return rig.clock }
		if err := restarted.load(ctx); err != nil {
			t.Fatal(err)
		}
		spec, _ := engagement.Spec(engagement.KindTopicFinds)
		if restarted.Prefs().KindEnabled(spec) {
			t.Fatalf("%s: a kind added later came back on", level)
		}
		if restarted.Level() != level {
			t.Fatalf("%s: the level reads %s after the restart", level, restarted.Level())
		}
	}
}
