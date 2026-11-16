package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/handoff"
	"wails-gui/storage"
)

func engagementPendingMoments(t *testing.T, rig *engagementTestRig) []storage.EngagementMomentRow {
	t.Helper()
	rows, err := rig.store.ListEngagementMoments(context.Background(), storage.EngagementStatePending, 0)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func engagementMomentFor(t *testing.T, rows []storage.EngagementMomentRow, dedupeKey string) storage.EngagementMomentRow {
	t.Helper()
	for _, row := range rows {
		if row.DedupeKey == dedupeKey {
			return row
		}
	}
	t.Fatalf("no pending moment with dedupe key %q in %+v", dedupeKey, rows)
	return storage.EngagementMomentRow{}
}

func engagementMomentParams(t *testing.T, row storage.EngagementMomentRow) map[string]string {
	t.Helper()
	params := map[string]string{}
	if err := json.Unmarshal([]byte(row.Params), &params); err != nil {
		t.Fatalf("params %q: %v", row.Params, err)
	}
	return params
}

func TestEngagementProducersAreInertWithoutTheService(t *testing.T) {
	app := &App{}
	app.produceWorkFinished(handoff.Entry{ID: "hf-1", Title: "Outline"})
	app.produceWorkBlocked(handoff.Entry{ID: "hf-2", Title: "Export", BlockedReason: handoff.BlockedTimeout})
	if app.produceBriefReady(MobileBriefV1{BriefID: "brief-1", Slot: mobileBriefSlotMorning}, true, false) {
		t.Fatal("without pacing a brief must not send its own push")
	}
}

func TestEngagementWorkFinishedFollowsACompletedHandoff(t *testing.T) {
	rig := newEngagementTestRig(t)
	store := newLifecycleHandoffStore(t, rig)
	app := &App{engagement: rig.svc, handoffStore: store}
	for _, id := range []string{"hf-done", "hf-failed"} {
		saveLifecycleHandoff(t, store, handoff.Entry{ID: id, Title: "The Q3 outline", Status: handoff.StatusInProgress, Source: handoff.SourceTriage, ChatSessionID: "sess-" + id, Body: "Draft the outline."})
	}

	app.markHandoffDone("hf-failed", false, "")
	if rows := engagementPendingMoments(t, rig); len(rows) != 0 {
		t.Fatalf("a run that ended in error is not finished: %+v", rows)
	}

	app.markHandoffDone("hf-done", true, "")
	rows := engagementPendingMoments(t, rig)
	if len(rows) != 1 {
		t.Fatalf("want one work_finished moment, got %+v", rows)
	}
	row := engagementMomentFor(t, rows, "work_finished:hf-done")
	if row.Kind != "work_finished" || row.SubjectID != "hf-done" || row.Stakes != 0.6 {
		t.Fatalf("work_finished moment = %+v", row)
	}
	if got := engagementMomentParams(t, row)["title"]; got != "The Q3 outline" {
		t.Fatalf("title = %q", got)
	}
}

func TestEngagementWorkBlockedFollowsStartupRecovery(t *testing.T) {
	rig := newEngagementTestRig(t)
	store := newLifecycleHandoffStore(t, rig)
	app := &App{engagement: rig.svc, handoffStore: store, activeTags: map[string]bool{"sess-running": true}}
	saveLifecycleHandoff(t, store, handoff.Entry{ID: "hf-restart", Title: "The expense export", Status: handoff.StatusInProgress, Source: handoff.SourceTriage, ChatSessionID: "sess-restart"})
	saveLifecycleHandoff(t, store, handoff.Entry{ID: "hf-running", Title: "The pricing notes", Status: handoff.StatusInProgress, Source: handoff.SourceTriage, ChatSessionID: "sess-running"})
	saveLifecycleHandoff(t, store, handoff.Entry{ID: "hf-done", Title: "Done already", Status: handoff.StatusCompleted, Source: handoff.SourceTriage, ChatSessionID: "sess-done"})

	app.recoverInterruptedHandoffs()

	restart, _ := store.Get("hf-restart")
	if restart.Status != handoff.StatusBlocked || restart.BlockedReason != handoff.BlockedInterrupted {
		t.Fatalf("interrupted handoff = %+v", restart)
	}
	if running, _ := store.Get("hf-running"); running.Status != handoff.StatusInProgress {
		t.Fatalf("a live turn must not be recovered: %+v", running)
	}
	if done, _ := store.Get("hf-done"); done.Status != handoff.StatusCompleted {
		t.Fatalf("a completed handoff must not move: %+v", done)
	}

	rows := engagementPendingMoments(t, rig)
	if len(rows) != 1 {
		t.Fatalf("want one work_blocked moment, got %+v", rows)
	}
	row := engagementMomentFor(t, rows, "work_blocked:hf-restart:"+engagement.HashPart(handoff.BlockedInterrupted))
	params := engagementMomentParams(t, row)
	if params["title"] != "The expense export" || params["question"] != "to be picked up again." || row.Stakes != 0.7 {
		t.Fatalf("work_blocked moment = %+v params=%v", row, params)
	}
	set, _ := engagement.Copy(engagement.KindWorkBlocked, "en")
	if got := engagement.Render(set.Sealed, params); got != "The expense export needs to be picked up again." {
		t.Fatalf("sealed text = %q", got)
	}
}

func TestEngagementWorkBlockedFollowsTheIdleTimeout(t *testing.T) {
	rig := newEngagementTestRig(t)
	store := newLifecycleHandoffStore(t, rig)
	app := &App{engagement: rig.svc, handoffStore: store, activeTags: map[string]bool{"sess-live": true}}
	now := rig.clock
	stale := now.Add(-handoffInProgressIdleTimeout - time.Minute).UnixMilli()
	saveLifecycleHandoff(t, store, handoff.Entry{ID: "hf-idle", Title: "The export", Status: handoff.StatusInProgress, Source: handoff.SourceTriage, ChatSessionID: "sess-idle", UpdatedAt: stale})
	saveLifecycleHandoff(t, store, handoff.Entry{ID: "hf-recent", Title: "The memo", Status: handoff.StatusInProgress, Source: handoff.SourceTriage, ChatSessionID: "sess-recent", UpdatedAt: now.Add(-handoffInProgressIdleTimeout + time.Minute).UnixMilli()})
	saveLifecycleHandoff(t, store, handoff.Entry{ID: "hf-live", Title: "The pricing", Status: handoff.StatusInProgress, Source: handoff.SourceTriage, ChatSessionID: "sess-live", UpdatedAt: stale, LastActivityAt: now.UnixMilli()})

	app.blockIdleHandoffs(now)

	idle, _ := store.Get("hf-idle")
	if idle.Status != handoff.StatusBlocked || idle.BlockedReason != handoff.BlockedTimeout {
		t.Fatalf("idle handoff = %+v", idle)
	}
	if recent, _ := store.Get("hf-recent"); recent.Status != handoff.StatusInProgress {
		t.Fatalf("a recently active handoff must stay in progress: %+v", recent)
	}
	if live, _ := store.Get("hf-live"); live.Status != handoff.StatusInProgress {
		t.Fatalf("recent stream activity keeps the turn working: %+v", live)
	}

	rows := engagementPendingMoments(t, rig)
	if len(rows) != 1 {
		t.Fatalf("want one work_blocked moment, got %+v", rows)
	}
	row := engagementMomentFor(t, rows, "work_blocked:hf-idle:"+engagement.HashPart(handoff.BlockedTimeout))
	if got := engagementMomentParams(t, row)["question"]; got != "a check-in to keep going." {
		t.Fatalf("question = %q", got)
	}
}

func TestEngagementBriefReadyTakesOverTheBriefPush(t *testing.T) {
	rig := newEngagementTestRig(t)
	app := &App{engagement: rig.svc}
	morning := MobileBriefV1{BriefID: "brief-7", Slot: mobileBriefSlotMorning}
	if app.produceBriefReady(morning, false, false) {
		t.Fatal("a brief that does not notify keeps notify off")
	}
	if rows := engagementPendingMoments(t, rig); len(rows) != 0 {
		t.Fatalf("a brief that does not notify submits nothing: %+v", rows)
	}
	if app.produceBriefReady(morning, true, false) {
		t.Fatal("the moment carries the push, so the brief publish must not notify")
	}
	rows := engagementPendingMoments(t, rig)
	row := engagementMomentFor(t, rows, "brief_ready:brief-7")
	if row.Kind != "brief_ready" || row.SubjectID != "brief-7" || row.Stakes != 0.4 {
		t.Fatalf("brief moment = %+v", row)
	}
	if got := engagementMomentParams(t, row)["slot"]; got != "morning" {
		t.Fatalf("slot = %q", got)
	}
	if row.ExpiresAt != rig.clock.Add(12*time.Hour).UnixMilli() {
		t.Fatalf("brief expires after 12 hours: %+v", row)
	}

	app.produceBriefReady(MobileBriefV1{BriefID: "brief-8", Slot: mobileBriefSlotOnRequest, DisplaySlot: mobileBriefSlotEvening}, true, false)
	onRequest := engagementMomentFor(t, engagementPendingMoments(t, rig), "brief_ready:brief-8")
	if got := engagementMomentParams(t, onRequest)["slot"]; got != "evening" {
		t.Fatalf("on-request slot = %q", got)
	}

	if (&App{}).produceBriefReady(morning, true, false) {
		t.Fatal("without pacing the brief must not send its own push")
	}
}

func TestEngagementStartSuppressesSilentKinds(t *testing.T) {
	rig := newEngagementTestRig(t)
	ctx := context.Background()
	ended, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindMeetingEnded, SubjectID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	followup, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindMeetingFollowup, SubjectID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := rig.svc.Submit(ctx, engagement.Moment{Kind: engagement.KindWorkFinished, SubjectID: "h1"})
	if err != nil {
		t.Fatal(err)
	}

	rig.svc.suppressSilentKinds(ctx)

	for _, id := range []string{ended.ID, followup.ID} {
		row, err := rig.store.GetEngagementMoment(ctx, id)
		if err != nil || row == nil || row.State != storage.EngagementStateSuppressed {
			t.Fatalf("a stored meeting moment must not go out: %+v %v", row, err)
		}
	}
	if row, _ := rig.store.GetEngagementMoment(ctx, finished.ID); row == nil || row.State != storage.EngagementStatePending {
		t.Fatalf("other kinds stay pending: %+v", row)
	}
}

func TestEngagementTitleIsOneBoundedLine(t *testing.T) {
	if got := engagementTitle("  Approve\n the  reply \t now "); got != "Approve the reply now" {
		t.Fatalf("title = %q", got)
	}
	long := strings.Repeat("a", engagementTitleMax+40)
	if got := engagementTitle(long); len([]rune(got)) != engagementTitleMax+3 || !strings.HasSuffix(got, "...") {
		t.Fatalf("long title = %d runes %q", len([]rune(got)), got)
	}
}

func TestEngagementBriefHookSubmitsTheScheduledBriefInsteadOfPushingIt(t *testing.T) {
	clock := &fakeMobileBriefClock{now: mobileBriefTestDay(7, 15)}
	svc, _ := newTestMobileBriefService(t, clock)
	rig := newEngagementTestRig(t)
	svc.app.engagement = rig.svc

	svc.checkDue(context.Background())
	waitMobileBriefIdle(t, svc)

	rows := engagementPendingMoments(t, rig)
	if len(rows) != 1 || !strings.HasPrefix(rows[0].DedupeKey, "brief_ready:") {
		t.Fatalf("the scheduled brief must be submitted as one brief_ready moment: %+v", rows)
	}
	if got := engagementMomentParams(t, rows[0])["slot"]; got != "morning" {
		t.Fatalf("slot = %q", got)
	}
}

// ST86: a brief the person asked for is stored as a silent brief_ready moment,
// and a scheduled one is not.
func TestEngagementBriefReadyManualRequestCarriesTheSilentMarker(t *testing.T) {
	rig := newEngagementTestRig(t)
	app := &App{engagement: rig.svc}
	app.produceBriefReady(MobileBriefV1{BriefID: "brief-manual", Slot: mobileBriefSlotOnRequest}, true, true)
	app.produceBriefReady(MobileBriefV1{BriefID: "brief-scheduled", Slot: mobileBriefSlotMorning}, true, false)
	rows := engagementPendingMoments(t, rig)
	manual := engagementMomentParams(t, engagementMomentFor(t, rows, "brief_ready:brief-manual"))
	if manual[engagement.BriefRequestParam] != engagement.BriefRequestManual {
		t.Fatalf("manual brief params = %+v", manual)
	}
	scheduled := engagementMomentParams(t, engagementMomentFor(t, rows, "brief_ready:brief-scheduled"))
	if _, marked := scheduled[engagement.BriefRequestParam]; marked {
		t.Fatalf("scheduled brief carries the manual marker: %+v", scheduled)
	}
}

// A written brief's notice waits for its recording, at most two minutes, and
// goes as soon as the recording step ends; never before the brief's slot. The
// moment is stored at once, so the first read still waits on it.
func TestEngagementBriefReadyWaitsForTheRecording(t *testing.T) {
	rig := newEngagementTestRig(t)
	store := rig.store
	app := &App{engagement: rig.svc, store: store}
	written := MobileBriefV1{BriefID: "brief-9", Slot: mobileBriefSlotMorning, NarrativeStatus: "ok"}
	app.produceBriefReady(written, true, false)
	row, err := store.GetOpenEngagementMomentByKey(context.Background(), "brief_ready:brief-9")
	if err != nil || row == nil {
		t.Fatalf("the brief's moment was not stored: %v", err)
	}
	if row.State != storage.EngagementStateScheduled || row.HoldUntil != rig.clock.Add(mobileBriefNotifyHoldMax).UnixMilli() {
		t.Fatalf("a brief without its recording did not wait for it: %+v", row)
	}
	app.releaseBriefReady(written)
	row, _ = store.GetOpenEngagementMomentByKey(context.Background(), "brief_ready:brief-9")
	if row == nil || row.HoldUntil > rig.clock.UnixMilli() {
		t.Fatalf("the recording step ended but the notice still waits: %+v", row)
	}

	spoken := MobileBriefV1{BriefID: "brief-10", Slot: mobileBriefSlotMorning, NarrativeStatus: "ok",
		Audio: &MobileBriefAudio{Status: mobileBriefAudioStatusOK, Attachment: &MobileBriefAudioAttachment{}}}
	app.produceBriefReady(spoken, true, false)
	if row := engagementMomentFor(t, engagementPendingMoments(t, rig), "brief_ready:brief-10"); row.HoldUntil != 0 {
		t.Fatalf("a brief that already plays waited: %+v", row)
	}
}

// On a phone that reads Your work, a finished task's notice names its item
// and carries Useful, so it is answered without opening the app; an older
// phone gets the notice as before.
func TestWorkNoticeActionsFollowTheKind(t *testing.T) {
	if got := workNoticeAction(engagement.KindMeetingPrep); got.ID != "not_needed" || got.Label != "Not needed" || got.Auth {
		t.Fatalf("prep action = %+v", got)
	}
	if got := workNoticeAction(engagement.KindWorkFinished); got.ID != "useful" || got.Label != "Useful" || got.Auth {
		t.Fatalf("finished-work action = %+v", got)
	}
	raw, err := mobileNoticeEncode(mobileNotice{ID: "n", Kind: "work_finished", Title: "Claude finished the test plan", Category: "YANSU_MOMENT",
		SubjectID: "hf-1", WorkItemID: "result:hf-1", CreatedAt: time.Unix(0, 0)})
	if err != nil || !strings.Contains(string(raw), `"workItemId":"result:hf-1"`) {
		t.Fatalf("the sealed notice does not name its item: %s %v", raw, err)
	}
}
