package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/sop"
	"wails-gui/storage"
)

// Real SQLite and file stores only. No NewApp, capture, providers or real agent data.
func newPageReadFixture(tb testing.TB) (*App, string, time.Time) {
	tb.Helper()
	home := tb.TempDir()
	tb.Setenv("HOME", home)
	tb.Setenv("YANSU_MOBILE_STATE_DIR", filepath.Join(home, "mobile"))
	tb.Setenv(topicsStateDirEnvVar, filepath.Join(home, "topics"))
	root := filepath.Join(home, configDirName)
	must := func(err error) {
		tb.Helper()
		if err != nil {
			_, _, line, _ := runtime.Caller(1)
			tb.Fatalf("fixture line %d: %v", line, err)
		}
	}
	must(os.MkdirAll(root, 0700))
	state, err := storage.New(filepath.Join(root, "sessions.db"))
	must(err)
	tb.Cleanup(func() { _ = state.Close() })
	activity, err := NewActivityStore(root)
	must(err)
	tb.Cleanup(func() { _ = activity.Close() })
	sops, err := sop.NewStore(filepath.Join(root, "sops"))
	must(err)
	a := &App{store: state, activity: &activityMonitorState{store: activity}, sopStore: sops}
	a.engagement = newEngagementService(state, nil, engagementDelivery{})
	tb.Cleanup(a.stopPageFirstReads)
	day := localBeliefDay(time.Now())
	main, err := a.ensureMainChat(context.Background())
	must(err)
	project := ProjectData{ID: "fixture-project", Name: "Fixture", Path: filepath.Join(home, "project"), Workspaces: []WorkspaceData{{ID: "fixture-workspace", IsDefault: true}}}
	for i := 0; i < 300; i++ {
		session := SessionData{LocalID: fmt.Sprintf("chat-%03d", i), Label: "Project review", CreatedAt: day.AddDate(0, 0, -i%30).UnixMilli()}
		for j := 0; j < 20; j++ {
			session.Items = append(session.Items, StreamItemData{ID: fmt.Sprintf("chat-%03d-%02d", i, j), Kind: "user", Content: "Project review and implementation notes"})
		}
		project.Workspaces[0].Sessions = append(project.Workspaces[0].Sessions, session)
	}
	must(a.SaveProjectState(project))
	for _, session := range project.Workspaces[0].Sessions {
		for _, item := range session.Items {
			_, err := a.PersistStreamItem(session.LocalID, item)
			must(err)
		}
	}
	for i := 0; i < 3000; i++ {
		at := day.AddDate(0, 0, -i%30).Add(time.Duration(i%480) * time.Minute).UnixMilli()
		row := storage.StreamItemRow{ID: fmt.Sprintf("main-%04d", i), Kind: "user", Content: "Project review notes", CreatedAt: at}
		if i%10 == 0 {
			row.Kind = "system"
			row.ToolName = mainChatProactiveTool
			row.ToolInput = `{"kind":"capture_paused","state":"pending"}`
		}
		_, err := state.AppendStreamItem(context.Background(), main.LocalID, row)
		must(err)
	}
	people := make([]string, 60)
	for i := range people {
		p, err := a.CreateContact(fmt.Sprintf("Colleague %02d", i), "colleague", nil, nil)
		must(err)
		people[i] = p.ID
	}
	for d := 0; d < 30; d++ {
		for n := 0; n < 24; n++ {
			id := fmt.Sprintf("activity-%02d-%02d", d, n)
			at := day.AddDate(0, 0, -d).Add(time.Duration(8*60+n*20) * time.Minute).UnixMilli()
			events := make([]activitymon.ActivityEvent, 24)
			for e := range events {
				events[e] = activitymon.ActivityEvent{Kind: activitymon.EventFocus, Timestamp: at + int64(e)*20000, AppName: "Code", AppBundleID: "com.example.code", WindowTitle: "Project review"}
			}
			must(activity.SaveSession(ActivitySessionRow{ID: id, StartTime: at, EndTime: at + 600000, AppNames: `["Code"]`, TriageStatus: "done"}, events, nil))
			run := testRun("run-"+id, "Code", at, at+600000, 600000, len(events))
			run.AppKey = "com.example.code"
			run.EventCount = len(events)
			run.SessionIDs = []string{id}
			must(activity.db.QueryRow(`SELECT min(id),max(id) FROM activity_events WHERE session_id=?`, id).Scan(&run.FirstEventRowID, &run.LastEventRowID))
			must(insertTimelineRun(activity.db, run, 0))
			must(activity.InsertActivity(ActivityRow{ID: id, DayStartMs: day.AddDate(0, 0, -d).UnixMilli(), Title: "Project review", Status: activityStatusIdle, RunIDs: composeJSON([]string{run.ID}), ActiveMsByApp: `{"Code":600000}`, Entities: `[]`, StartMs: at, EndMs: at + 600000, CreatedAt: at, UpdatedAt: at}))
			must(activity.UpsertActivityRunLink(run.ID, id, run.SummarizedEventCount, at+600000))
			threadID := fmt.Sprintf("thread-%02d-%02d", d, n/2)
			dayStart := day.AddDate(0, 0, -d).UnixMilli()
			_, err = activity.db.Exec(`INSERT OR IGNORE INTO activity_threads(id,day_start_ms,title,user_title,kind,created_at,updated_at) VALUES(?,?,'Project review','Project review','work',?,?)`, threadID, dayStart, at, at)
			must(err)
			_, err = activity.db.Exec(`INSERT INTO activity_thread_members(activity_id,thread_id,day_start_ms,source,updated_at) VALUES(?,?,?,'user',?)`, id, threadID, dayStart, at)
			must(err)
			_, err = activity.db.Exec(`INSERT OR IGNORE INTO activity_thread_days(day_start_ms,grouped_at,updated_at) VALUES(?,?,?)`, dayStart, at, at)
			must(err)
			must(state.BumpPrincipalSighting(context.Background(), people[n%len(people)], "Code", id, at, "Project review"))
		}
	}
	memory, err := memoryDir()
	must(err)
	knowledge, err := knowledgeDir()
	must(err)
	for i := 0; i < 60; i++ {
		body := fmt.Sprintf("---\ntype: Isoform Memory\ntitle: Review preference %02d\nsource: activity\nactivity_sessions: [activity-00-00, activity-00-01, activity-01-00, activity-01-01]\n---\n\n# Review preference %02d\n\nKeep project reviews clear.\n", i, i)
		must(os.WriteFile(filepath.Join(memory, fmt.Sprintf("belief-%02d.md", i)), []byte(body), 0600))
		must(os.WriteFile(filepath.Join(knowledge, fmt.Sprintf("guide-%02d.md", i)), []byte(fmt.Sprintf("# Project review %02d\n\n## Key Insights\n- Review implementation and tests.\n", i)), 0600))
		_, err = sops.Save(sop.Entry{ID: fmt.Sprintf("sop-%02d", i), Title: "Project review", Body: "Review implementation and tests."})
		must(err)
		_, err = a.createUserPurpose(userPurposeCreate{Key: fmt.Sprintf("goal-%02d", i), Origin: "chat", Kind: PurposeKindGoal, Text: fmt.Sprintf("Review project %02d", i)}, day)
		must(err)
	}
	return a, main.LocalID, day
}

type pageReadCase struct {
	name string
	read func() (any, error)
}

func pageReadCases(a *App, main string, day time.Time) []pageReadCase {
	start, end := day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli()
	return []pageReadCase{
		{"GetMainChat", func() (any, error) { return a.GetMainChat() }},
		{"GetMainChatSetup", func() (any, error) { return a.GetMainChatSetup("en") }},
		{"GetMainChatOverview", func() (any, error) { return a.GetMainChatOverview() }},
		{"LoadStreamItemsPage", func() (any, error) { return a.LoadStreamItemsPage(main, -1, 100) }},
		{"ListMainChatWaitingItems", func() (any, error) { return a.ListMainChatWaitingItems("", 50) }},
		{"LoadAllProjects", func() (any, error) { return a.LoadAllProjects() }},
		{"GetActivitiesInRange", func() (any, error) { return a.GetActivitiesInRange(start, end) }},
		{"GetDayThreads", func() (any, error) {
			view, err := a.GetDayThreads(start, end)
			if err == nil && len(view.Threads) != 12 {
				return nil, fmt.Errorf("fixture threads=%d, want 12", len(view.Threads))
			}
			return view, err
		}},
		{"GetDailySummary", func() (any, error) { return a.GetDailySummary(day.Format("2006-01-02")) }},
		{"GetWeeklySummary", func() (any, error) { return a.GetWeeklySummary(day.Format("2006-01-02")) }},
		{"GetActivityMonthStats", func() (any, error) { return a.GetActivityMonthStats(day.Format("2006-01")) }},
		{"GetActivitySessions", func() (any, error) { return a.GetActivitySessions(200, 0, start, end, "") }},
		{"GetActivityTimelineGroups", func() (any, error) { return a.GetActivityTimelineGroups(200, 0, start, end, "") }},
		{"GetRecentMeetingNotes", func() (any, error) { return a.GetRecentMeetingNotes(7) }},
		{"ListGoals", func() (any, error) { return a.ListGoals() }},
		{"GetAboutYou", func() (any, error) { return a.GetAboutYou() }},
		{"GetAboutYouPortrait", func() (any, error) { return a.GetAboutYouPortrait() }},
		{"GetMemoryEntries", func() (any, error) { return a.GetMemoryEntries() }},
		{"GetMemoryStatus", func() (any, error) { return a.GetMemoryStatus(), nil }},
		{"ListKnowHow", func() (any, error) { return a.ListKnowHow() }},
		{"GetKnowHowMatch", func() (any, error) { return a.GetKnowHowMatch() }},
		{"GetKnowledgeEntries", func() (any, error) { return a.GetKnowledgeEntries() }},
		{"ListSOPs", func() (any, error) { return a.ListSOPs() }},
		{"ListPeopleOverview", func() (any, error) { return a.ListPeopleOverview() }},
		{"ListPrincipalsWithStats", func() (any, error) { return a.ListPrincipalsWithStats() }},
		{"GetSettingsOverview", func() (any, error) { return a.GetSettingsOverview() }},
		{"GetPlatformPermissions", func() (any, error) { return a.GetPlatformPermissions(), nil }},
		{"GetMobileConnectionStatus", func() (any, error) { return a.GetMobileConnectionStatus() }},
		{"GetTextChannelStatus", func() (any, error) { return a.GetTextChannelStatus() }},
		{"GetSMSChannelStatus", func() (any, error) { return a.GetSMSChannelStatus() }},
		{"GetEmailChannelStatus", func() (any, error) { return a.GetEmailChannelStatus() }},
		{"GetSensitiveApps", func() (any, error) { return a.GetSensitiveApps(), nil }},
		{"GetVoiceInputApps", func() (any, error) { return a.GetVoiceInputApps(), nil }},
		{"GetBackgroundListening", func() (any, error) { return a.GetBackgroundListening(), nil }},
		{"GetAutomaticMeetingRecordingEnabled", func() (any, error) { return a.GetAutomaticMeetingRecordingEnabled(), nil }},
		{"LoadAllSidebarGroups", func() (any, error) { return a.LoadAllSidebarGroups() }},
		{"LoadUIState", func() (any, error) { return a.LoadUIState("search-recents") }},
		{"ListHandoffs", func() (any, error) { return a.ListHandoffs() }},
		{"SearchEverything", func() (any, error) { return a.SearchEverything("review", 5) }},
		{"GetEngagementSettings", func() (any, error) { return a.GetEngagementSettings() }},
	}
}

func BenchmarkPageFirstReads(b *testing.B) {
	a, main, day := newPageReadFixture(b)
	for _, tc := range pageReadCases(a, main, day) {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := tc.read(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// This is a page's first read after the sequential startup warmer, not a loop
// of previously timed foreground calls. Native cold work remains separately
// measured by BenchmarkPageFirstReads and BenchmarkPagePermissionProbes.
func BenchmarkPagePreloadedReads(b *testing.B) {
	a, main, day := newPageReadFixture(b)
	allowPageWarmer(b, a)
	runPageFirstReads(context.Background(), a.pageFirstReadSteps(context.Background()), a.pageFirstReadsAllowed, 0)
	for _, tc := range pageReadCases(a, main, day) {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := tc.read(); err != nil {
					b.Fatal(err)
				}
			}
			if b.Elapsed()/time.Duration(b.N) > 100*time.Millisecond {
				b.Fatalf("%s exceeded the 100ms preloaded read budget", tc.name)
			}
		})
	}
}

func BenchmarkPageBindingDelivery(b *testing.B) {
	a, main, day := newPageReadFixture(b)
	for _, tc := range pageReadCases(a, main, day) {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				value, err := tc.read()
				if err != nil {
					b.Fatal(err)
				}
				if _, err := json.Marshal(value); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
