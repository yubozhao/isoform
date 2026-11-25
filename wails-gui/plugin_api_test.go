package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/plugins"
	"wails-gui/storage"
)

func pluginTestApp(t *testing.T) *App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YANSU_MOBILE_STATE_DIR", filepath.Join(home, "mobile"))
	t.Setenv(topicsStateDirEnvVar, filepath.Join(home, "topics"))
	t.Setenv("YANSU_BROWSER_BRIDGE", "")
	t.Setenv("WAILS_DEV", "")
	store, err := storage.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &App{store: store}
}

func pluginViewsByID(views []PluginInfo) map[string]PluginInfo {
	byID := make(map[string]PluginInfo, len(views))
	for _, view := range views {
		byID[view.ID] = view
	}
	return byID
}

func TestPluginsListLeavesStoredValuesAndFilesUnchanged(t *testing.T) {
	app := pluginTestApp(t)
	stored := map[string]string{
		"calendar_peek_enabled": " false ", "triage_enable_memory": "false",
		"triage_enable_shadow": "false", "shadow_confidence_adjustment_enabled": "true",
		"work_pattern_profile_influence_enabled": " TRUE ", "permission_enabled:files": "",
	}
	for key, value := range stored {
		if err := app.SaveUIState(key, value); err != nil {
			t.Fatal(err)
		}
	}
	views, err := app.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	byID := pluginViewsByID(views)
	if len(views) != len(plugins.BuiltinsForPlatform(runtime.GOOS)) || !byID["calendar-peek"].Enabled || byID["triage-memory"].Enabled || byID["files-activity"].Enabled {
		t.Fatalf("incorrect stored flags: %+v", views)
	}
	// Listing does not reconcile or rewrite a saved dependent while its
	// provider is off; only an explicit user toggle applies a dependency plan.
	if !byID["shadow-confidence"].Enabled || !byID["work-pattern-profile"].Enabled {
		t.Fatal("list changed saved dependent preferences")
	}
	for key, want := range stored {
		got, err := app.LoadUIState(key)
		if err != nil || got != want {
			t.Fatalf("stored %s = %q, %v; want %q", key, got, err, want)
		}
	}
	entries, err := os.ReadDir(os.Getenv("HOME"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("ListPlugins created files: %v, %v", entries, err)
	}
}

func TestPluginsListPreservesConfigEncodingAndDefaults(t *testing.T) {
	app := pluginTestApp(t)
	path, err := yansuConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte("{\n  \"automatic_meeting_recording_disabled\": true, \"auto_speak_enabled\": false, \"auto_speak_automation_enabled\": true, \"browser_bridge_enabled\": true, \"future_setting\": 7\n}\n")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	views, err := app.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	byID := pluginViewsByID(views)
	if byID["meeting-recording"].Enabled || byID["auto-speak"].Enabled || !byID["auto-speak-automation"].Enabled {
		t.Fatal("config switches were not read as saved")
	}
	if byID["browser-bridge"].Enabled {
		t.Fatal("browser bridge must reflect the existing runtime getter")
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("config was rewritten: %s (%v)", after, err)
	}
}

func TestPluginsEssentialAndUnknownChangesAreRefused(t *testing.T) {
	app := pluginTestApp(t)
	for _, id := range []string{"authority", "cron-api", "notification-pacing"} {
		if _, err := app.SetBuiltinOn(id, false); err == nil || !strings.Contains(err.Error(), "essential") {
			t.Fatalf("disable %s = %v", id, err)
		}
	}
	if _, err := app.SetBuiltinOn("not-a-plugin", false); err == nil {
		t.Fatal("unknown plugin accepted")
	}
	views, err := app.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Essential && (!view.Enabled || view.LockedReasonKey != "plugins.essentialLocked") {
			t.Fatalf("essential is not locked: %+v", view)
		}
	}
}

func TestPluginsApplyExistingUIStateSettersAndDependencies(t *testing.T) {
	app := pluginTestApp(t)
	if _, err := app.SetBuiltinOn("work-pattern-profile", true); err != nil {
		t.Fatal(err)
	}
	views, err := app.SetBuiltinOn("triage-shadow", false)
	if err != nil {
		t.Fatal(err)
	}
	byID := pluginViewsByID(views)
	for _, id := range []string{"triage-shadow", "shadow-confidence", "work-pattern-profile"} {
		if byID[id].Enabled {
			t.Errorf("%s remains enabled after provider off", id)
		}
	}
	views, err = app.SetBuiltinOn("shadow-confidence", true)
	if err != nil {
		t.Fatal(err)
	}
	byID = pluginViewsByID(views)
	if !byID["triage-shadow"].Enabled || !byID["shadow-confidence"].Enabled || byID["work-pattern-profile"].Enabled {
		t.Fatal("enabling dependent did not enable exactly its provider")
	}
	raw, err := app.LoadUIState("triage_enable_shadow")
	if err != nil || raw != "true" {
		t.Fatalf("existing setter value = %q, %v", raw, err)
	}
}

func TestPluginsCalendarSetterAndNotificationSwitches(t *testing.T) {
	app := pluginTestApp(t)
	if _, err := app.SetBuiltinOn("calendar-peek", false); err != nil {
		t.Fatal(err)
	}
	if app.calendarPeekEnabled() {
		t.Fatal("calendar setter did not apply")
	}
	app.engagement = newEngagementService(app.store, nil, engagementDelivery{})
	if _, err := app.SetBuiltinOn("notify-work_blocked", false); err != nil {
		t.Fatal(err)
	}
	spec, _ := engagement.Spec(engagement.KindWorkBlocked)
	if app.engagement.Prefs().KindEnabled(spec) {
		t.Fatal("existing notification setter did not apply")
	}
	rows, err := app.store.ListEngagementPrefs(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Enabled != 0 {
		t.Fatalf("notification state = %+v, %v", rows, err)
	}
}

func TestPluginsCannotBypassScreenObservationPrivacyGate(t *testing.T) {
	app := pluginTestApp(t)
	for _, key := range []string{"screen_recording", "accessibility"} {
		if err := app.SaveUIState(permissionUIStateKey(key), "false"); err != nil {
			t.Fatal(err)
		}
		if _, err := app.SetBuiltinOn("activity-monitor", true); err == nil {
			t.Fatalf("activity plugin bypassed %s", key)
		}
		if app.activity != nil {
			t.Fatal("blocked plugin initialized activity capture")
		}
		if err := app.SaveUIState(permissionUIStateKey(key), "true"); err != nil {
			t.Fatal(err)
		}
	}
	app.closedEyes.enabled.Store(true)
	if _, err := app.SetBuiltinOn("activity-monitor", true); err == nil {
		t.Fatal("activity plugin bypassed observation pause")
	}
}

func TestPluginsStartAtLoginUsesTheSystemGetter(t *testing.T) {
	app := pluginTestApp(t)
	login, err := app.GetStartAtLogin()
	if err != nil {
		t.Fatal(err)
	}
	if err := updateConfig(func(cfg *yansuConfig) { cfg.StartAtLoginDisabled = login }); err != nil {
		t.Fatal(err)
	}
	views, err := app.ListPlugins()
	if err != nil {
		t.Fatal(err)
	}
	if got := pluginViewsByID(views)["start-at-login"].Enabled; got != login {
		t.Fatalf("Plugins start-at-login = %v, disagrees with the General page", got)
	}
}

func TestPluginsUnreadableRowDoesNotBlockUnrelatedControls(t *testing.T) {
	app := pluginTestApp(t)
	path := filepath.Join(os.Getenv("YANSU_MOBILE_STATE_DIR"), "relay.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"version":2,"enabled":true}`, `{broken`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		views, err := app.ListPlugins()
		if err != nil {
			t.Fatal(err)
		}
		rows := pluginViewsByID(views)
		if rows["mobile-connection"].ReadErrorKey != "plugins.readError" || rows["triage-memory"].ReadErrorKey != "" {
			t.Fatalf("read failure was not isolated: %+v", rows)
		}
		for _, on := range []bool{false, true} {
			if _, err := app.SetBuiltinOn("mobile-connection", on); err == nil || !strings.Contains(err.Error(), "plugin settings are unreadable") {
				t.Fatalf("unreadable row toggle %v = %v", on, err)
			}
			views, err = app.SetBuiltinOn("triage-memory", on)
			if err != nil || pluginViewsByID(views)["triage-memory"].Enabled != on {
				t.Fatalf("unrelated toggle %v failed: %v", on, err)
			}
		}
		if after, err := os.ReadFile(path); err != nil || string(after) != raw {
			t.Fatalf("unreadable row was rewritten: %q, %v", after, err)
		}
	}
}

func TestPluginsMemorySwitchesPreserveUnrelatedStoredValues(t *testing.T) {
	app := pluginTestApp(t)
	app.memoryDream = NewMemoryDreamService(app)
	path, err := dreamSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := `{"enabled":true,"llmEnabled":true,"dailyTime":"","similarityMergeThreshold":0.5,"llmBatchSize":65,"future":{"value":" untouched ","count":12345678901234567890}}`
	var before map[string]json.RawMessage
	if err := json.Unmarshal([]byte(original), &before); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"memory-dream-llm", "memory-dream"} {
		if err := os.WriteFile(path, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := app.SetBuiltinOn(id, false); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var after map[string]json.RawMessage
		if err := json.Unmarshal(raw, &after); err != nil {
			t.Fatal(err)
		}
		for key, want := range before {
			if key == "llmEnabled" || (id == "memory-dream" && key == "enabled") {
				want = json.RawMessage("false")
			}
			got, _ := json.Marshal(after[key])
			expected, _ := json.Marshal(want)
			if string(got) != string(expected) {
				t.Errorf("%s changed %s: %s, want %s", id, key, got, expected)
			}
		}
	}
}

func TestPluginsUnreadableMemoryDependenciesAreRefusedBeforeWrites(t *testing.T) {
	app := pluginTestApp(t)
	app.memoryDream = NewMemoryDreamService(app)
	path, err := dreamSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{broken`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"memory-dream", "memory-dream-llm"} {
		for _, on := range []bool{false, true} {
			if _, err := app.SetBuiltinOn(id, on); err == nil || !strings.Contains(err.Error(), "plugin settings are unreadable") {
				t.Fatalf("unreadable %s = %v: %v", id, on, err)
			}
		}
	}
	if raw, _ := os.ReadFile(path); string(raw) != `{broken` {
		t.Fatalf("failed toggle changed unreadable settings: %q", raw)
	}
}

func TestPluginsMailSwitchRetainsMessagesAndMoments(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Mail sync is available only on macOS")
	}
	app := pluginTestApp(t)
	ctx := context.Background()
	app.mailSync = &mailSyncService{store: app.store, enabled: newMailSyncService(app).enabled}
	if _, err := app.store.UpsertMailMessages(ctx, []storage.MailMessageRow{{ID: "m", Mailbox: storage.MailboxInbox, Subject: "Keep this", At: time.Now().UnixMilli()}}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.CreateEngagementMoment(ctx, storage.EngagementMomentRow{
		Kind: string(engagement.KindWaitingOnReply), DedupeKey: "waiting_on_reply:keep", Params: `{"subject":"Keep this"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetBuiltinOn("mail-sync", false); err != nil {
		t.Fatal(err)
	}
	app.mailSync.syncIfAllowed(ctx, "interval")
	rows, err := app.store.ListMailMessages(ctx, 0)
	if err != nil || len(rows) != 1 || rows[0].Subject != "Keep this" {
		t.Fatalf("mail switch lost stored data: %+v, %v", rows, err)
	}
	moment, err := app.store.GetOpenEngagementMomentByKey(ctx, "waiting_on_reply:keep")
	if err != nil || moment == nil || moment.Params != `{"subject":"Keep this"}` {
		t.Fatalf("mail switch lost stored moment: %+v, %v", moment, err)
	}
}

func TestPluginsTopicSwitchRetainsObservationsSchedulesAndLogs(t *testing.T) {
	app := pluginTestApp(t)
	ctx := context.Background()
	app.ctx = ctx
	if err := app.store.SaveProject(ctx, storage.ProjectRow{ID: "p", Name: "p", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, job := range []struct{ id, name string }{{"news", todayNewsJobName}, {"finds", topicFindsJobName}} {
		if err := app.store.SaveSession(ctx, storage.SessionRow{LocalID: job.id + "-log", ProjectID: "p", Label: job.name, Hidden: true}); err != nil {
			t.Fatal(err)
		}
		target, _ := json.Marshal(cronTargetConfig{Prompt: "saved prompt", SessionTag: job.id + "-log"})
		if err := app.store.SaveCronJob(ctx, storage.CronJobRow{
			ID: job.id, Name: job.name, Enabled: true, ScheduleKind: "manual", ScheduleConfig: `{}`,
			TargetKind: "agent", TargetConfig: string(target), SurfaceKind: todayNewsSurfaceKind,
		}); err != nil {
			t.Fatal(err)
		}
		if err := app.store.InsertCronRun(ctx, storage.CronRunRow{ID: job.id + "-run", JobID: job.id, Status: "success", ResultSummary: "saved answer"}); err != nil {
			t.Fatal(err)
		}
	}
	scheduler := NewCronScheduler(app.store, func(string, any) {}, func(string, string) {}, nil)
	app.cronScheduler = scheduler
	runCtx, cancel := context.WithCancel(ctx)
	go scheduler.Run(runCtx)
	select {
	case <-scheduler.loadedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not load")
	}
	t.Cleanup(func() { cancel(); scheduler.Stop() })
	svc := app.topicsSvc()
	before, err := svc.store.update(func(st *topicsStateFile) {
		st.Journal = []topicJournalDay{{Date: "2026-10-14", Browser: "Chrome"}}
		st.Visited = map[string]int64{"saved-link": 1}
		st.Finds.Items = []TopicFind{{ID: "saved-find", Title: "Saved find"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	interestPath := filepath.Join(os.Getenv(topicsStateDirEnvVar), interestsStateFileName)
	interests := []byte(`{"version":1,"interests":[{"id":"saved-interest","name":"Saved interest"}]}`)
	if err := os.WriteFile(interestPath, interests, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetBuiltinOn("topic-finds", false); err != nil {
		t.Fatal(err)
	}
	after := svc.store.load()
	if after.enabled() || !reflect.DeepEqual(before.Journal, after.Journal) || !reflect.DeepEqual(before.Visited, after.Visited) || !reflect.DeepEqual(before.Finds, after.Finds) {
		t.Fatalf("switch lost saved topic data: before %+v, after %+v", before, after)
	}
	if got, err := os.ReadFile(interestPath); err != nil || !reflect.DeepEqual(interests, got) {
		t.Fatalf("switch changed saved interests: %q, %v", got, err)
	}
	for _, id := range []string{"news", "finds"} {
		if _, err := app.store.LoadSession(ctx, id+"-log"); err != nil {
			t.Fatalf("switch lost %s log: %v", id, err)
		}
		runs, err := app.store.ListCronRuns(ctx, id, 10)
		if err != nil {
			t.Fatal(err)
		}
		if id == "news" {
			if len(runs) != 0 {
				t.Fatalf("retired news runs remain: %+v", runs)
			}
		} else if len(runs) != 1 || runs[0].ResultSummary != "saved answer" {
			t.Fatalf("switch lost %s runs: %+v", id, runs)
		}
	}
	if job := app.findTopicFindsJob(); job == nil || job.Enabled {
		t.Fatalf("research schedule was not paused: %+v", job)
	}
	if id, err := app.ensureTopicFindsJob("next prompt"); err != nil || id != "finds" || !app.findTopicFindsJob().Enabled {
		t.Fatalf("research schedule did not resume in place: %q, %v", id, err)
	}
}
