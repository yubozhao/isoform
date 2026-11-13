package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"wails-gui/internal/platform/activitymon"
	"yansu-agent/agent"
)

// timelineBatchRunner answers every batch with a summary for each groupId it
// finds in the prompt, except the ids in skip.
type timelineBatchRunner struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	skip    map[string]bool
}

func (r *timelineBatchRunner) Init(context.Context) error { return nil }
func (r *timelineBatchRunner) ExecuteTask(_ context.Context, input *agent.TaskInput, callback agent.StreamCallback) (string, error) {
	r.mu.Lock()
	r.calls++
	r.prompts = append(r.prompts, input.Prompt)
	r.mu.Unlock()
	var runs []string
	for _, line := range strings.Split(input.Prompt, "\n") {
		if id, ok := strings.CutPrefix(line, "- groupId: "); ok && !r.skip[id] {
			runs = append(runs, fmt.Sprintf(`{"groupId":%q,"title":"Wrote the %s plan","description":"Wrote a plan.","activityType":"writing","entities":[],"timeline":[]}`, id, id))
		}
	}
	reply := `{"runs":[` + strings.Join(runs, ",") + `]}`
	if len(runs) == 1 && !strings.Contains(input.Prompt, "app runs follow") {
		reply = `{"title":"Wrote the plan","description":"Wrote a plan.","activityType":"writing","entities":[],"timeline":[]}`
	}
	return "run", callback(&agent.StreamEvent{Type: "assistant", Content: reply})
}
func (r *timelineBatchRunner) CancelRun(string) error                   { return nil }
func (r *timelineBatchRunner) RespondToPermission(string, string) error { return nil }
func (r *timelineBatchRunner) CancelPermission(string)                  {}
func (r *timelineBatchRunner) Shutdown() error                          { return nil }
func (r *timelineBatchRunner) IsReady() bool                            { return true }

func saveTimelineBatchRuns(t *testing.T, store *ActivityStore, n int) []ActivityTimelineRunRow {
	t.Helper()
	var runs []ActivityTimelineRunRow
	for i := 0; i < n; i++ {
		start := int64(1_000_000 + i*120_000)
		session := ActivitySessionRow{ID: fmt.Sprintf("tl-session-%02d", i), StartTime: start, EndTime: start + 60_000, AppNames: `["Notes"]`, TriageStatus: "done"}
		event := activitymon.ActivityEvent{Kind: activitymon.EventFocus, AppName: "Notes", AppBundleID: "com.apple.Notes", WindowTitle: fmt.Sprintf("Plan %d", i), Timestamp: start + 1000}
		if err := store.SaveSession(session, []activitymon.ActivityEvent{event}, nil); err != nil {
			t.Fatal(err)
		}
		rows, err := store.GetSessionEventRows(session.ID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("load source event: %v, rows=%d", err, len(rows))
		}
		run := testRun(fmt.Sprintf("tg_batch_%02d", i), "Notes", start, start+60_000, 60_000, 0)
		run.SessionIDs = []string{session.ID}
		run.AppKey = timelineEventAppKey(event)
		run.FirstEventRowID, run.LastEventRowID, run.EventCount = rows[0].ID, rows[0].ID, 1
		run.Status, run.SummaryStatus, run.TriggerKinds = timelineRunStatusClosed, timelineSummaryStatusNone, []string{}
		if err := insertTimelineRun(store.db, run, 0); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	return runs
}

func TestTimelineSummariesBatchRunsIntoFewCalls(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTimelineTestStore(t)
	runner := &timelineBatchRunner{skip: map[string]bool{"tg_batch_03": true}}
	app := &App{runner: runner, activity: &activityMonitorState{store: store}}
	runs := saveTimelineBatchRuns(t, store, 10)
	app.generateTimelineRunSummaries(context.Background(), runs, nil)
	// Ten runs: one call for eight, one for the last two. The per-run worker
	// made ten.
	if runner.calls != 2 {
		t.Fatalf("model calls for 10 runs = %d, want 2", runner.calls)
	}
	for _, run := range runs {
		saved, err := store.GetTimelineRunByID(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := timelineSummaryStatusDone
		if run.ID == "tg_batch_03" {
			want = timelineSummaryStatusFailedTransient
		}
		if saved.SummaryStatus != want {
			t.Fatalf("run %s status=%q, want %q", run.ID, saved.SummaryStatus, want)
		}
	}
	summaries, err := store.GetTimelineGroupSummaries([]string{"tg_batch_01"})
	if err != nil || !strings.Contains(summaries["tg_batch_01"].Title, "tg_batch_01") {
		t.Fatalf("run kept another run's summary: %+v, %v", summaries, err)
	}
}
