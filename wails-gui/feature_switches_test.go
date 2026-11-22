package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// featuresOff turns every rework switch off for one test (TestMain turns them
// all on so older tests keep their behavior).
func featuresOff(t *testing.T) {
	t.Helper()
	t.Setenv(featureSwitchesEnv, "")
}

func TestFeatureSwitchesDefaultOffAndTurnBackOn(t *testing.T) {
	featuresOff(t)
	app := &App{}
	if got := app.GetFeatureSwitches(); got != (FeatureSwitches{}) {
		t.Fatalf("defaults = %+v, want every feature off", got)
	}
	t.Setenv(featureSwitchesEnv, "automations, Calendar-Holds")
	if got := app.GetFeatureSwitches(); !got.Automations || !got.CalendarHolds || got.Playbooks || got.ProactiveVoice {
		t.Fatalf("named switches = %+v, want automations and calendar holds only", got)
	}
	t.Setenv(featureSwitchesEnv, "all")
	if got := app.GetFeatureSwitches(); !got.Automations || !got.Playbooks || !got.ProactiveVoice || !got.CalendarHolds {
		t.Fatalf("all = %+v, want every feature on", got)
	}
}

func cronSwitchTestScheduler(t *testing.T) (*storage.Store, *CronScheduler) {
	t.Helper()
	setTestHome(t)
	store, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, NewCronScheduler(store, func(string, any) {}, func(string, string) {}, nil)
}

func cronSwitchTestJob(surface string) CronJobInput {
	schedule, _ := json.Marshal(map[string]any{"every_ms": 3_600_000})
	target, _ := json.Marshal(map[string]any{"prompt": "check the build"})
	return CronJobInput{Name: "job " + surface, ScheduleKind: "every", ScheduleConfig: schedule,
		TargetKind: "agent", TargetConfig: target, SurfaceKind: surface, RetryPolicy: json.RawMessage(`{}`)}
}

// With automations off a user automation can't be added, a saved one is
// never loaded (no timer, no catch-up run, not listed), and chat's own
// one-shot jobs still work.
func TestAutomationsOffLoadsAndAddsOnlyCoreJobs(t *testing.T) {
	store, scheduler := cronSwitchTestScheduler(t)
	ctx := context.Background()
	// Saved while automations were on.
	t.Setenv(featureSwitchesEnv, "all")
	user, err := scheduler.AddJob(ctx, cronSwitchTestJob(""))
	if err != nil {
		t.Fatalf("add user automation with the switch on: %v", err)
	}
	core, err := scheduler.AddJob(ctx, cronSwitchTestJob(openAnswerSurfaceKind))
	if err != nil {
		t.Fatalf("add open answer check: %v", err)
	}

	featuresOff(t)
	if _, err := scheduler.AddJob(ctx, cronSwitchTestJob("")); !errors.Is(err, errFeatureOff(featureAutomations)) {
		t.Fatalf("add user automation with the switch off: err=%v, want feature_off", err)
	}
	if _, err := scheduler.AddJob(ctx, cronSwitchTestJob(playbookSurfaceKind)); !errors.Is(err, errFeatureOff(featurePlaybooks)) {
		t.Fatalf("add playbook schedule with the switch off: err=%v, want feature_off", err)
	}
	if _, err := scheduler.AddJob(ctx, cronSwitchTestJob("handoff")); err != nil {
		t.Fatalf("add handoff with the switch off: %v", err)
	}

	restarted := NewCronScheduler(store, func(string, any) {}, func(string, string) {}, nil)
	if err := restarted.loadJobs(ctx); err != nil {
		t.Fatal(err)
	}
	close(restarted.loadedCh)
	ids := map[string]bool{}
	for _, job := range restarted.ListJobs() {
		ids[job.ID] = true
	}
	if ids[user.ID] {
		t.Fatal("a user automation was loaded with the switch off")
	}
	if !ids[core.ID] {
		t.Fatal("an open answer check was not loaded with the switch off")
	}
	if err := restarted.RunNow(ctx, user.ID); err == nil {
		t.Fatal("a user automation ran with the switch off")
	}

	app := &App{store: store, cronScheduler: restarted}
	for _, job := range app.listCronJobsFromDBContext(ctx) {
		if job.ID == user.ID {
			t.Fatal("the database fallback listed a user automation with the switch off")
		}
	}
}

// The automations entries make no model call with the switch off.
func TestAutomationsOffMakesNoModelCall(t *testing.T) {
	featuresOff(t)
	app := &App{}
	if name := app.GenerateAutomationName("water the plants every morning"); name != "" {
		t.Fatalf("name = %q, want none with the switch off", name)
	}
	if flags := app.loadTriageFeatureFlags(context.Background()); flags.Automation {
		t.Fatal("activity triage still asks the model for automations with the switch off")
	}
}

// Playbooks, SOPs and goals off: the reads are empty, the writes and model
// calls are refused, chat gets no goal tool, the phone and Search show no
// goals, and no goal check-in is sent.
func TestPlaybooksOffHasNoEntryNoLoopNoModelCall(t *testing.T) {
	featuresOff(t)
	app := &App{}
	if state, err := app.GetPlaybooksState(); err != nil || len(state.Plans) != 0 {
		t.Fatalf("playbooks state = %+v err=%v, want empty", state, err)
	}
	if sops, err := app.ListSOPs(); err != nil || len(sops) != 0 {
		t.Fatalf("SOPs = %v err=%v, want empty", sops, err)
	}
	if err := app.GenerateSOPFromDescription("file the weekly report"); !errors.Is(err, errFeatureOff(featurePlaybooks)) {
		t.Fatalf("writing a playbook: err=%v, want feature_off", err)
	}
	if err := app.GenerateSOPFromRange(1, 2); !errors.Is(err, errFeatureOff(featurePlaybooks)) {
		t.Fatalf("writing a playbook from a range: err=%v, want feature_off", err)
	}
	if _, err := app.RunSOP("any"); !errors.Is(err, errFeatureOff(featurePlaybooks)) {
		t.Fatalf("running a playbook: err=%v, want feature_off", err)
	}
	if page, err := app.ListGoals(); err != nil || len(page.Tracking)+len(page.Done)+len(page.Held) != 0 {
		t.Fatalf("goals = %+v err=%v, want empty", page, err)
	}
	if checkIn, err := app.GetGoalCheckIn(); err != nil || checkIn != nil {
		t.Fatalf("goal check-in = %+v err=%v, want none", checkIn, err)
	}
	if _, err := app.CreateGoal(GoalCreateInput{}); !errors.Is(err, errFeatureOff(featurePlaybooks)) {
		t.Fatalf("adding a goal: err=%v, want feature_off", err)
	}
	if tools := app.goalTools("main"); len(tools) != 0 {
		t.Fatalf("chat goal tools = %d, want none", len(tools))
	}
	if found, err := app.searchGoals(context.Background(), []string{"launch"}); err != nil || len(found) != 0 {
		t.Fatalf("search goals = %v err=%v, want none", found, err)
	}
	if flags := app.loadTriageFeatureFlags(context.Background()); flags.SOP {
		t.Fatal("activity triage still asks the model for playbooks with the switch off")
	}
	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local)
	detail := purposeSignalDetail(PurposeKindGoal, PurposeDueUnknown, "", created, true)
	if moments := engagementPurposeSignals(detail, created.AddDate(0, 0, 14)); len(moments) != 0 {
		t.Fatalf("goal check-ins = %+v, want none with the switch off", moments)
	}
}

// Voice cards off: no card about a person or a promise is posted, the status
// cards keep their catalog copy, nothing reaches the voice model, and a card
// voiced earlier shows its catalog copy on read.
func TestProactiveVoiceOffPostsNoVoiceCardAndCallsNoModel(t *testing.T) {
	featuresOff(t)
	app := &App{}
	for _, kind := range []engagement.Kind{engagement.KindWaitingOnReply, engagement.KindCommitmentSlipping} {
		item := mainChatProactive{Kind: string(kind)}
		if app.voiceMainChatCopy(engagement.Moment{Kind: kind}, &item) {
			t.Fatalf("%s card posted with voice cards off", kind)
		}
	}
	for _, kind := range []engagement.Kind{engagement.KindBriefReady, engagement.KindCapturePaused} {
		item := mainChatProactive{Kind: string(kind)}
		if !app.voiceMainChatCopy(engagement.Moment{Kind: kind}, &item) {
			t.Fatalf("%s status card withheld with voice cards off", kind)
		}
		if item.VoiceVersion != "" || item.VoiceReceipt != "" {
			t.Fatalf("%s card was voiced with voice cards off: %+v", kind, item)
		}
	}

	voiced := mainChatProactive{Kind: string(engagement.KindBriefReady), VoiceVersion: "v3", VoiceReceipt: "receipt",
		Localized: map[string]mainChatProactiveCopy{"en": {Headline: "Mira is waiting on the contract"}}}
	app.projectProactiveVoiceItem(context.Background(), &voiced, "en")
	if voiced.VoiceVersion != "" || voiced.VoiceReceipt != "" || voiced.Localized["en"].Headline == "Mira is waiting on the contract" {
		t.Fatalf("an earlier voiced card kept its voiced copy: %+v", voiced)
	}
}

// Holds off: holds are never offered or proposed, the hold worker does not
// start, and Your week makes no hold ask.
func TestCalendarHoldsOffOffersNothingAndStartsNoWorker(t *testing.T) {
	featuresOff(t)
	app := newTodayContextTestApp(t, true)
	settings, err := app.holdSettings(&fakeHoldEventStore{}, true)
	if err != nil || settings.OfferHolds {
		t.Fatalf("hold settings = %+v err=%v, want holds not offered", settings, err)
	}
	if _, err := app.proposeHold(HoldThing{Title: "Send the deck", SourceKind: "commitment", SourceID: "c1"}, time.Now().Add(48*time.Hour), time.Hour, time.Now(), &fakeHoldEventStore{}, true); err == nil {
		t.Fatal("a hold was proposed with holds off")
	}
	app.startHoldWorker(context.Background())
	if app.holds != nil {
		app.stopHoldWorker()
		t.Fatal("the hold worker started with holds off")
	}
	if hold := app.yourWeekCalendarHold(context.Background(), time.Now(), time.Now(), "the launch"); hold != nil {
		t.Fatalf("Your week asked for a hold with holds off: %+v", hold)
	}
}
