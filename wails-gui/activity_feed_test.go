package main

import (
	"testing"
	"time"

	"wails-gui/internal/workonit"
)

// TestActivityFeedClosedEyesIsAGapClippedToTheDay: a Closed Eyes stretch
// shows as one row with no id (nothing to forget), clipped to the window;
// one still on runs to now.
func TestActivityFeedClosedEyesIsAGapClippedToTheDay(t *testing.T) {
	rows := activityFeedClosedRows([]observationPause{
		{Start: 50, End: 150},  // starts before the window
		{Start: 300, End: 400}, // inside
		{Start: 900, End: 0},   // still on
		{Start: 10, End: 40},   // before the window
	}, 100, 1000, 950)
	want := []ActivityFeedRow{{Kind: "closed", StartMs: 100, EndMs: 150}, {Kind: "closed", StartMs: 300, EndMs: 400}, {Kind: "closed", StartMs: 900, EndMs: 950}}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestActivityFeedIsNewestFirst(t *testing.T) {
	rows := []ActivityFeedRow{{ID: "a", StartMs: 1}, {ID: "c", StartMs: 3}, {ID: "b", StartMs: 2}}
	sortActivityFeed(rows)
	if rows[0].ID != "c" || rows[1].ID != "b" || rows[2].ID != "a" {
		t.Fatalf("order = %+v", rows)
	}
}

func TestActivityFeedWatchRowsKeepTheirWindowAndSkipDeleted(t *testing.T) {
	day := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	start, end := day.UnixMilli(), day.Add(24*time.Hour).UnixMilli()
	rows := activityFeedWatchRows([]WatchRecording{
		{ID: "in", Source: "watch", StartedAt: day.Add(9 * time.Hour).Format(time.RFC3339), DurationSeconds: 60, Words: 12},
		{ID: "phone", Source: "phone", StartedAt: day.Add(10 * time.Hour).Format(time.RFC3339)},
		{ID: "gone", State: mobileRecordingDeleted, StartedAt: day.Add(11 * time.Hour).Format(time.RFC3339)},
		{ID: "yesterday", StartedAt: day.Add(-time.Hour).Format(time.RFC3339)},
	}, start, end)
	if len(rows) != 2 || rows[0].ID != "in" || rows[1].ID != "phone" {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Device != "watch" || rows[1].Device != "phone" || rows[0].EndMs-rows[0].StartMs != 60000 || rows[0].Words != 12 {
		t.Fatalf("watch row = %+v", rows[0])
	}
}

// TestActivityFeedShowsAgentsAndForgetsOnlyTheirCapture: the day's agent run
// is its own row with the agent's app, beside the person's moment; Forget on
// it names the capture that goes and removes only that.
func TestActivityFeedShowsAgentsAndForgetsOnlyTheirCapture(t *testing.T) {
	app := newThreadTestApp(t)
	store := app.activity.store
	day, dayMs := threadTestDay(t)
	agent := seedThreadMoment(t, app, threadFixtureMoment{id: "hourly", title: "Hourly PR check in the hub", app: "Claude", bundle: "com.anthropic.claudefordesktop", start: at(day, 10, 0), end: at(day, 10, 20)})
	mine := seedThreadMoment(t, app, threadFixtureMoment{id: "mine", title: "Pricing page copy", app: "Safari", bundle: "com.apple.Safari", start: at(day, 11, 0), end: at(day, 11, 30)})
	run, err := store.GetTimelineRunByID("run-" + agent.ID)
	if err != nil || run == nil {
		t.Fatalf("agent run: %v", err)
	}
	if err := store.MarkTimelineRunAgent(*run); err != nil {
		t.Fatal(err)
	}
	rows, err := app.GetActivityFeed(dayMs, activityThreadDayEnd(dayMs))
	if err != nil {
		t.Fatal(err)
	}
	var agentRow *ActivityFeedRow
	sawMine := false
	for i, row := range rows {
		if row.Kind == "agent" && row.ID == run.ID {
			agentRow = &rows[i]
		}
		sawMine = sawMine || row.Kind == "screen" && row.ID == mine.ID
		if i > 0 && rows[i-1].StartMs < row.StartMs {
			t.Fatalf("not newest first: %+v", rows)
		}
	}
	if agentRow == nil || agentRow.App != "Claude" {
		t.Fatalf("agent row missing or without its app: %+v", rows)
	}
	if !sawMine {
		t.Fatalf("the person's moment is missing: %+v", rows)
	}
	preview, err := app.GetActivityForgetPreview("agent", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Goes) == 0 || preview.Goes[0].Kind != "screen" {
		t.Fatalf("agent preview = %+v", preview)
	}
	if err := app.ForgetActivityFeedRow("agent", run.ID); err != nil {
		t.Fatal(err)
	}
	sessions, err := app.activityAgentRunSessions(run.ID)
	if err == nil && len(sessions) != 0 {
		t.Fatalf("agent capture outlived Forget: %+v", sessions)
	}
	if got, _ := store.GetActivityByID(mine.ID); got == nil {
		t.Fatal("forgetting the agent removed the person's moment")
	}
	if _, err := app.GetActivityForgetPreview("closed", "x"); err == nil {
		t.Fatal("a Closed Eyes row has nothing to forget")
	}
}

// computer-activity v1.2.0, Activity › Agents: each hand-off is a row with its
// agent, its first line and what Isoform knows the agent did.
func TestActivityFeedHandoffRowsSayWhatTheAgentDid(t *testing.T) {
	day := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	at := func(h int) int64 { return day.Add(time.Duration(h) * time.Hour).UnixMilli() }
	handoffs := []workonit.Handoff{
		{ID: "h-done", AgentID: "codex", Text: "Write the test plan\nwith the edge cases", SentAt: at(9), Surface: "computer"},
		{ID: "h-read", AgentID: "claude", Text: "Reply to Sam", SentAt: at(10), Surface: "phone"},
		{ID: "h-quiet", AgentID: "claude-code", Text: "Fix the parser", SentAt: at(1)},
		{ID: "h-new", AgentID: "unknown-agent", Text: "Draft the post", SentAt: at(20)},
		{ID: "h-yesterday", AgentID: "codex", Text: "Old", SentAt: at(-2)},
	}
	records := map[string]workonit.Record{
		"h-done":  {ID: "h-done", Status: workonit.StatusDone, SentAt: at(9), DoneAt: at(11)},
		"h-read":  {ID: "h-read", Status: workonit.StatusTaken, SentAt: at(10)},
		"h-quiet": {ID: "h-quiet", Status: workonit.StatusSent, SentAt: at(1)},
		"h-new":   {ID: "h-new", Status: workonit.StatusSent, SentAt: at(20)},
	}
	rows := activityFeedHandoffRows(handoffs, records, at(0), at(24), at(1)+workonit.AskAfter.Milliseconds())
	if len(rows) != 4 {
		t.Fatalf("rows = %+v", rows)
	}
	byID := map[string]ActivityFeedRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if r := byID["h-done"]; r.Kind != "handoff" || r.App != "Codex" || r.Title != "Write the test plan" || r.Outcome != "done" || r.DoneMs != at(11) {
		t.Fatalf("done row = %+v", r)
	}
	if r := byID["h-read"]; r.Outcome != "taken" || r.Surface != "phone" || r.App != "Claude" {
		t.Fatalf("read row = %+v", r)
	}
	if r := byID["h-quiet"]; r.Outcome != "no_word" {
		t.Fatalf("quiet row = %+v", r)
	}
	if r := byID["h-new"]; r.Outcome != "sent" || r.App != "unknown-agent" {
		t.Fatalf("new row = %+v", r)
	}
}
