package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"
)

func TestMorningRecapReachesBackToTheLastActiveDay(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no tz database")
	}
	morning := time.Date(2026, 10, 15, 7, 0, 0, 0, la)
	eveningBrief := time.Date(2026, 10, 14, 18, 0, 0, 0, la)

	// Worked until 22:00 yesterday: the recap starts at yesterday's midnight.
	if got := mobileBriefMorningStart(morning, eveningBrief, la, time.Date(2026, 10, 14, 22, 0, 0, 0, la), true); !got.Equal(time.Date(2026, 10, 14, 0, 0, 0, 0, la)) {
		t.Fatalf("recap starts %s, want yesterday's midnight", got)
	}
	// A Monday after a Friday: back to Friday.
	monday := time.Date(2026, 10, 19, 7, 0, 0, 0, la)
	if got := mobileBriefMorningStart(monday, monday.Add(-13*time.Hour), la, time.Date(2026, 10, 16, 17, 30, 0, 0, la), true); !got.Equal(time.Date(2026, 10, 16, 0, 0, 0, 0, la)) {
		t.Fatalf("Monday's recap starts %s, want Friday", got)
	}
	// Up early, already working: the recap keeps the brief's window.
	if got := mobileBriefMorningStart(morning, eveningBrief, la, time.Date(2026, 10, 15, 6, 30, 0, 0, la), true); !got.Equal(eveningBrief) {
		t.Fatalf("a morning with activity moved its recap to %s", got)
	}
	// Never more than seven days.
	if got := mobileBriefMorningStart(morning, eveningBrief, la, morning.Add(-30*24*time.Hour), true); got.Before(morning.Add(-mobileBriefMaxWindow)) {
		t.Fatalf("recap reached back to %s", got)
	}
	if got := mobileBriefMorningStart(morning, eveningBrief, la, time.Time{}, false); !got.Equal(eveningBrief) {
		t.Fatalf("no activity at all moved the recap to %s", got)
	}
}

func morningRecapRig(t *testing.T) (*handoffTestRig, time.Time, time.Time) {
	t.Helper()
	rig := newHandoffTestRig(t)
	now := time.Now()
	morning := time.Date(now.Year(), now.Month(), now.Day(), 7, 0, 0, 0, time.Local)
	yesterday := morning.AddDate(0, 0, -1)
	for _, row := range []ActivityRow{
		{ID: "a1", Title: "Wired the brief to the real calendar", ActiveMsByApp: `{"Code": 5400000}`,
			StartMs: yesterday.Add(3 * time.Hour).UnixMilli(), EndMs: yesterday.Add(4*time.Hour + 30*time.Minute).UnixMilli()},
		{ID: "a2", Title: "Reviewed the relay pull request", ActiveMsByApp: `{"Safari": 1800000}`,
			StartMs: yesterday.Add(6 * time.Hour).UnixMilli(), EndMs: yesterday.Add(6*time.Hour + 30*time.Minute).UnixMilli()},
		// A chat in a messaging app stays out.
		{ID: "a3", Title: "Talked about the offer with Priya", ActiveMsByApp: `{"Slack": 3600000}`,
			StartMs: yesterday.Add(8 * time.Hour).UnixMilli(), EndMs: yesterday.Add(9 * time.Hour).UnixMilli()},
		// No readable apps: fails closed.
		{ID: "a4", Title: "Looked at a lab result", ActiveMsByApp: ``,
			StartMs: yesterday.Add(10 * time.Hour).UnixMilli(), EndMs: yesterday.Add(10*time.Hour + 20*time.Minute).UnixMilli()},
		// A private window stays out.
		{ID: "a5", Title: "Gift ideas - Private Browsing", ActiveMsByApp: `{"Firefox": 600000}`,
			StartMs: yesterday.Add(11 * time.Hour).UnixMilli(), EndMs: yesterday.Add(11*time.Hour + 10*time.Minute).UnixMilli()},
		// Paused while it happened: stays out.
		{ID: "a6", Title: "Read the contract draft", ActiveMsByApp: `{"Preview": 1200000}`,
			StartMs: yesterday.Add(12 * time.Hour).UnixMilli(), EndMs: yesterday.Add(12*time.Hour + 20*time.Minute).UnixMilli()},
	} {
		row.Status = "done"
		row.CreatedAt, row.UpdatedAt = row.StartMs, row.EndMs
		row = addMobileBriefRecapProvenance(t, rig, row)
		if err := rig.activity.InsertActivity(row); err != nil {
			t.Fatal(err)
		}
	}
	rig.app.saveObservationPauses([]observationPause{{Start: yesterday.Add(11*time.Hour + 50*time.Minute).UnixMilli(), End: yesterday.Add(13 * time.Hour).UnixMilli()}})
	return rig, morning, yesterday
}

func TestTheMorningBriefRecapsYesterday(t *testing.T) {
	rig, morning, yesterday := morningRecapRig(t)
	t.Setenv("YANSU_MOBILE_STATE_DIR", t.TempDir())
	svc := newMobileBriefService(rig.app)
	svc.app = rig.app
	svc.power = func(string) (func(), error) { return func() {}, nil }
	svc.speaker = mobileBriefSpeaker{}
	svc.now = func() time.Time { return morning }
	brief, err := svc.generate(context.Background(), mobileBriefSlotMorning, mobileBriefReasonSettings)
	if err != nil {
		t.Fatal(err)
	}
	if mobileBriefIsEmpty(brief) || brief.ColdStart {
		t.Fatalf("the morning brief after a working day is empty: %+v", brief)
	}
	var titles []string
	for _, item := range brief.Sections.Moved {
		titles = append(titles, item.Title)
		if item.Title == "Wired the brief to the real calendar" && item.Detail != "90 min" {
			t.Fatalf("the recap should keep duration without past clock times: %q", item.Detail)
		}
	}
	joined := strings.Join(titles, " | ")
	if !strings.Contains(joined, "Wired the brief") || !strings.Contains(joined, "Reviewed the relay") {
		t.Fatalf("recap = %s", joined)
	}
	for _, kept := range []string{"Priya", "lab result", "Private Browsing", "contract draft"} {
		if strings.Contains(joined, kept) {
			t.Fatalf("%q reached the recap: %s", kept, joined)
		}
	}
	// The brief's own window stays the last 24 hours: only the recap reaches
	// back.
	start, err := time.Parse(time.RFC3339Nano, brief.WindowStart)
	if err != nil || !start.Equal(yesterday) {
		t.Fatalf("window starts %s, want the brief's own window", brief.WindowStart)
	}
}

func TestOnlyTheRecapUsesTheLookBack(t *testing.T) {
	rig, morning, yesterday := morningRecapRig(t)
	in := mobileBriefInput{
		Slot: mobileBriefSlotMorning, WindowStart: morning.Add(-time.Hour), AsOf: morning,
		RecapStart: yesterday.Add(-7 * time.Hour), Location: time.Local, ClockLayout: "3:04 PM",
	}
	collectMobileBriefRecap(rig.app, &in)
	if len(in.Moved) == 0 {
		t.Fatal("the recap did not reach back to yesterday")
	}
	if !in.inRecapWindow(yesterday.Add(3*time.Hour)) || in.inWindow(yesterday.Add(3*time.Hour)) {
		t.Fatal("yesterday counts in the brief's own window")
	}
}

func TestMorningBriefOmitsRetiredNews(t *testing.T) {
	rig := newHandoffTestRig(t)
	ctx := context.Background()
	job := storage.CronJobRow{ID: "news", Name: todayNewsJobName, Enabled: true, ScheduleKind: "manual", ScheduleConfig: `{}`, TargetKind: "agent", TargetConfig: `{}`, SurfaceKind: todayNewsSurfaceKind}
	if err := rig.state.SaveCronJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	story := withResearchMeta(`<news_json>[{"title":"Old story","url":"https://example.com/story"}]</news_json>`, researchMeta{Via: researchViaClaudeCode, Results: 1, URLs: []string{"https://example.com/story"}, Personal: researchPersonalNo})
	if err := rig.state.InsertCronRun(ctx, storage.CronRunRow{ID: "old-news", JobID: job.ID, Status: "ok", StartedAt: time.Now().UnixMilli(), FinishedAt: time.Now().UnixMilli(), ResultSummary: story}); err != nil {
		t.Fatal(err)
	}
	scheduler := NewCronScheduler(rig.state, func(string, any) {}, func(string, string) {}, nil)
	if err := scheduler.loadJobs(ctx); err != nil {
		t.Fatal(err)
	}
	close(scheduler.loadedCh)
	rig.app.cronScheduler = scheduler
	in := mobileBriefInput{Slot: mobileBriefSlotMorning, AsOf: time.Now()}
	collectMobileBriefNews(rig.app, &in)
	if len(in.Watching) != 0 {
		t.Fatalf("retired news reached the morning brief: %+v", in.Watching)
	}
}

func TestCronNativeResearchPersonalizationFollowsTaskContext(t *testing.T) {
	rig := newHandoffTestRig(t)
	items := []storage.StreamItemRow{{Kind: "assistant", Content: "<news_json>[]</news_json>"}}
	if err := rig.state.SaveProject(context.Background(), storage.ProjectRow{ID: "p", Name: "p", Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := rig.state.SaveSession(context.Background(), storage.SessionRow{LocalID: "news-log", ProjectID: "p", Label: "Daily News", Hidden: true}); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		rig.app.PersistStreamItem("news-log", StreamItemData{ID: "i1", Kind: item.Kind, Content: item.Content})
	}
	for _, tc := range []struct{ name, job, prompt, want string }{
		{"generic news", todayNewsJobName, "plain news prompt", researchPersonalNo},
		{"interests named by task", todayNewsJobName, todayNewsInterestsHeader + "\nWails", researchPersonalYes},
		{"topic search", topicFindsJobName, "Wails", researchPersonalYes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := rig.app.recordNativeResearch(context.Background(), "news-log", 0, "", nil, tc.job, tc.prompt)
			_, meta, ok := splitResearchMeta(summary)
			if !ok || meta.Personal != tc.want {
				t.Fatalf("personalization=%+v (ok=%v), want %q", meta, ok, tc.want)
			}
		})
	}
}

// A screen summary without a thread outcome still tells the new writer what
// changed (its latest-change line), never the section item: the Moved detail
// stays the duration. Typing narration stays out of the writer too.
func TestRecapLatestChangeReachesOnlyTheWriter(t *testing.T) {
	rig := newHandoffTestRig(t)
	now := time.Now()
	morning := time.Date(now.Year(), now.Month(), now.Day(), 7, 0, 0, 0, time.Local)
	yesterday := morning.AddDate(0, 0, -1)
	for _, row := range []ActivityRow{
		{ID: "c1", Title: "Wired the brief to the real calendar", LatestChange: "Moved the calendar read behind the snapshot", ActiveMsByApp: `{"Code": 5400000}`,
			StartMs: yesterday.Add(3 * time.Hour).UnixMilli(), EndMs: yesterday.Add(4*time.Hour + 30*time.Minute).UnixMilli()},
		{ID: "c2", Title: "Reviewed the relay pull request", LatestChange: "You typed a reply to the reviewer", ActiveMsByApp: `{"Safari": 1800000}`,
			StartMs: yesterday.Add(6 * time.Hour).UnixMilli(), EndMs: yesterday.Add(6*time.Hour + 30*time.Minute).UnixMilli()},
	} {
		row.Status = "done"
		row.CreatedAt, row.UpdatedAt = row.StartMs, row.EndMs
		row = addMobileBriefRecapProvenance(t, rig, row)
		if err := rig.activity.InsertActivity(row); err != nil {
			t.Fatal(err)
		}
	}
	in := mobileBriefInput{Slot: mobileBriefSlotMorning, WindowStart: yesterday, AsOf: morning, Location: time.Local, Timezone: "Local", ClockLayout: "3:04 PM"}
	collectMobileBriefRecap(rig.app, &in)
	brief := buildMobileBriefV1(in)
	for _, item := range brief.Sections.Moved {
		if strings.Contains(item.Detail, "snapshot") || strings.Contains(item.Detail, "typed") {
			t.Fatalf("the latest change reached a section item: %+v", item)
		}
	}
	m, _ := newBriefMaterial(in, brief, briefPartDone, "")
	found := map[string]string{}
	for _, item := range m.Items {
		found[item.Title] = item.Detail
	}
	if detail := found["Wired the brief to the real calendar"]; !strings.Contains(detail, "Moved the calendar read behind the snapshot") || !strings.Contains(detail, "90 min") {
		t.Fatalf("writer detail = %q, want the latest change and the duration", detail)
	}
	if detail, ok := found["Reviewed the relay pull request"]; !ok || strings.Contains(detail, "typed") {
		t.Fatalf("typing narration reached the writer: %q (found %v)", detail, ok)
	}
}
