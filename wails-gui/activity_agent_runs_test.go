package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
)

func agentTimelineEvent(app, bundle, title string, kind activitymon.EventKind) ActivityTimelineEvent {
	return ActivityTimelineEvent{ActivityEvent: activitymon.ActivityEvent{Kind: kind, AppName: app, AppBundleID: bundle, WindowTitle: title}}
}

func TestTimelineEventsAreAgent(t *testing.T) {
	claude := agentTimelineEvent("Claude", "com.anthropic.claudefordesktop", "isoform hub", activitymon.EventFocus)
	codex := agentTimelineEvent("Codex", "com.openai.codex", "", activitymon.EventFocus)
	terminalAgent := agentTimelineEvent("Terminal", "com.apple.Terminal", "codex exec -m gpt", activitymon.EventFocus)
	terminalMine := agentTimelineEvent("Terminal", "com.apple.Terminal", "zsh: ~/src", activitymon.EventFocus)
	safari := agentTimelineEvent("Safari", "com.apple.Safari", "Docs", activitymon.EventFocus)
	speech := ActivityTimelineEvent{ActivityEvent: activitymon.ActivityEvent{Kind: activitymon.EventSpeech}}
	for _, tc := range []struct {
		name   string
		events []ActivityTimelineEvent
		agent  bool
	}{
		{"agent apps only", []ActivityTimelineEvent{claude, codex, terminalAgent}, true},
		{"a plain terminal is the person's", []ActivityTimelineEvent{claude, terminalMine}, false},
		{"another app is the person's", []ActivityTimelineEvent{codex, safari}, false},
		{"speech is the person's", []ActivityTimelineEvent{claude, speech}, false},
		{"no app events", nil, false},
	} {
		if got := timelineEventsAreAgent(tc.events); got != tc.agent {
			t.Errorf("%s: agent=%t, want %t", tc.name, got, tc.agent)
		}
	}
}

// TestAgentRunsLeaveThreadsAndShowAsTheAgentLine: a moment made only of
// agent runs leaves the day's moments and threads; the day reports the agent
// work on its labelled line instead. The person's moment stays.
func TestAgentRunsLeaveThreadsAndShowAsTheAgentLine(t *testing.T) {
	app := newThreadTestApp(t)
	store := app.activity.store
	day, dayMs := threadTestDay(t)
	mine := seedThreadMoment(t, app, threadFixtureMoment{id: "mine", title: "Evaluate the OpenBCI bundle", app: "Safari", start: at(day, 9, 0), end: at(day, 9, 30)})
	agent := seedThreadMoment(t, app, threadFixtureMoment{id: "hourly", title: "Hourly PR check in the hub", app: "Claude", bundle: "com.anthropic.claudefordesktop", start: at(day, 10, 0), end: at(day, 10, 20)})

	run, err := store.GetTimelineRunByID("run-" + agent.ID)
	if err != nil || run == nil {
		t.Fatalf("agent run: %v", err)
	}
	if err := store.MarkTimelineRunAgent(*run); err != nil {
		t.Fatal(err)
	}
	activityThreadCacheForget(dayMs)

	ctx := context.Background()
	rows, err := store.dayActivityRows(ctx, dayMs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != mine.ID {
		t.Fatalf("day moments = %v, want only the person's", rowIDs(rows))
	}
	views, err := app.buildActivitiesInRange(dayMs, activityThreadDayEnd(dayMs))
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.ID == agent.ID {
			t.Fatal("the agent moment is still on the Activity page")
		}
	}
	got, err := store.agentRunDay(ctx, dayMs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runs != 1 || got.ActiveMs != run.ActiveMs {
		t.Fatalf("agent line = %+v, want one run of %d ms", got, run.ActiveMs)
	}
	threads, err := app.buildDayThreads(ctx, dayMs)
	if err != nil {
		t.Fatal(err)
	}
	if threads.Agents.Runs != 1 {
		t.Fatalf("day threads agent line = %+v", threads.Agents)
	}

	// The person shows up in the run: the mark goes.
	if err := store.ClearTimelineRunAgent(run.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.agentRunDay(ctx, dayMs); got.Runs != 0 {
		t.Fatalf("agent line after clear = %+v", got)
	}
}

func rowIDs(rows []ActivityRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func TestWithoutAgentMomentsKeepsMixedMoments(t *testing.T) {
	agentRuns := map[string]bool{"a": true, "b": true}
	rows := []ActivityRow{
		{ID: "agent", RunIDs: `["a","b"]`},
		{ID: "mixed", RunIDs: `["a","c"]`},
		{ID: "none", RunIDs: ``},
	}
	got := withoutAgentMoments(rows, agentRuns)
	if len(got) != 2 || got[0].ID != "mixed" || got[1].ID != "none" {
		t.Fatalf("kept %v", rowIDs(got))
	}
}

// TestCodexThreadIndexPlacesArchivedRecords: Codex archived the thread's
// file, so only its index places the record. An exec worker's record is
// agent, an interactive thread's is the person's, and a time no thread
// covers stays unplaced.
func TestCodexThreadIndexPlacesArchivedRecords(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	if err := os.MkdirAll(filepath.Join(codex, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(codex, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	ms := func(t time.Time) int64 { return t.UnixMilli() }
	for _, stmt := range []string{
		`CREATE TABLE threads (id TEXT, cwd TEXT, source TEXT, originator TEXT, created_at_ms INTEGER, updated_at_ms INTEGER, first_user_message TEXT)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(id, cwd, source, originator string, start, end time.Time) {
		if _, err := db.Exec(`INSERT INTO threads VALUES (?,?,?,?,?,?,?)`, id, cwd, source, originator, ms(start), ms(end), "never read"); err != nil {
			t.Fatal(err)
		}
	}
	insert("w", filepath.Join(home, "isoform-codex", "wt-cx-walk108"), "exec", "codex_exec", base, base.Add(time.Hour))
	insert("m", filepath.Join(home, "src", "thinking"), "vscode", "Codex Desktop", base, base.Add(time.Hour))
	insert("s", filepath.Join(home, "src", "thinking"), `{"subagent":{"thread_spawn":{"depth":1}}}`, "", base.Add(2*time.Hour), base.Add(3*time.Hour))
	db.Close()

	roots := assistantHistoryRoots{Home: home, Claude: filepath.Join(home, ".claude", "projects"), Codex: filepath.Join(codex, "sessions")}
	spans := readCodexThreadSpans(roots)
	if len(spans) != 3 {
		t.Fatalf("spans = %d", len(spans))
	}
	worker := &agentCleanupRecord{assistant: "codex", project: "wt-cx-walk108", sessionsAt: []time.Time{base.Add(30 * time.Minute)}}
	mine := &agentCleanupRecord{assistant: "codex", project: "thinking", sessionsAt: []time.Time{base.Add(30 * time.Minute)}}
	sub := &agentCleanupRecord{assistant: "codex", project: "thinking", sessionsAt: []time.Time{base.Add(150 * time.Minute)}}
	lost := &agentCleanupRecord{assistant: "codex", project: "thinking", sessionsAt: []time.Time{base.Add(10 * time.Hour)}}
	got := resolveCodexRecordsByThreads(spans, []*agentCleanupRecord{worker, mine, sub, lost})
	if agent, ok := got[worker]; !ok || !agent {
		t.Fatalf("worker record: %t %t", agent, ok)
	}
	if agent, ok := got[mine]; !ok || agent {
		t.Fatalf("interactive record: %t %t", agent, ok)
	}
	if agent, ok := got[sub]; !ok || !agent {
		t.Fatalf("subagent record: %t %t", agent, ok)
	}
	if _, ok := got[lost]; ok {
		t.Fatal("a record no thread covers was placed")
	}
}

// An agent mark is metadata about a run (when, how long). When the run is
// deleted (retention, a fold, a privacy purge), the mark goes with it, so the
// day no longer counts agent work whose activity is gone.
func TestAgentRunMarkLeavesWithItsRun(t *testing.T) {
	app := newThreadTestApp(t)
	store := app.activity.store
	day, dayMs := threadTestDay(t)
	agent := seedThreadMoment(t, app, threadFixtureMoment{id: "hourly", title: "Hourly PR check in the hub", app: "Claude", bundle: "com.anthropic.claudefordesktop", start: at(day, 10, 0), end: at(day, 10, 20)})
	run, err := store.GetTimelineRunByID("run-" + agent.ID)
	if err != nil || run == nil {
		t.Fatalf("agent run: %v", err)
	}
	if err := store.MarkTimelineRunAgent(*run); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, _ := store.agentRunDay(ctx, dayMs); got.Runs != 1 {
		t.Fatalf("agent line before delete = %+v", got)
	}
	if _, err := store.db.Exec(`DELETE FROM activity_timeline_groups WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.agentRunDay(ctx, dayMs); got.Runs != 0 || got.ActiveMs != 0 {
		t.Fatalf("agent line outlived its run = %+v", got)
	}
	var left int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM activity_agent_runs WHERE run_id = ?`, run.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("agent mark rows left = %d, %v", left, err)
	}
}
