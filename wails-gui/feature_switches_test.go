package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

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
