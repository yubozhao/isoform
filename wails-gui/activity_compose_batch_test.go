package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"yansu-agent/agent"
)

// composerBatchRunner answers a batched prompt with one decision per runId it
// lists: every third run joins the activity the run before it started, the
// rest start new activities. A one-run prompt gets a one-run "new" reply.
type composerBatchRunner struct {
	mu       sync.Mutex
	calls    int
	batches  int
	runsSeen []int
	omit     map[string]bool
}

var composerBatchRunIDRe = regexp.MustCompile(`\(runId=([^)]+)\)`)

func (r *composerBatchRunner) Init(context.Context) error { return nil }
func (r *composerBatchRunner) ExecuteTask(_ context.Context, input *agent.TaskInput, callback agent.StreamCallback) (string, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	bucket := `"bucket":{"title":"Work on the plan","description":"Planning.","status":"active","latestChange":"Wrote","category":"build","entities":[]}`
	matches := composerBatchRunIDRe.FindAllStringSubmatch(input.Prompt, -1)
	if len(matches) == 0 {
		return "run", callback(&agent.StreamEvent{Type: "assistant", Content: `{"route":"new","confidence":"high","reason":"new_task",` + bucket + `}`})
	}
	r.mu.Lock()
	r.batches++
	r.runsSeen = append(r.runsSeen, len(matches))
	r.mu.Unlock()
	var entries []string
	for i, m := range matches {
		id := m[1]
		if r.omit[id] {
			continue
		}
		if i%3 == 2 {
			prev := "act_" + matches[i-1][1]
			entries = append(entries, fmt.Sprintf(`{"runId":%q,"route":"join","joinBucketId":%q,"confidence":"high","reason":"same_document","admission":[{"bucketId":%q,"decision":"join","objectiveMatch":"strong","subjectMatch":"strong","stateContinuity":"strong","negativeEvidence":[],"reason":"same plan"}],%s}`, id, prev, prev, bucket))
			continue
		}
		entries = append(entries, fmt.Sprintf(`{"runId":%q,"route":"new","confidence":"high","reason":"new_task",%s}`, id, bucket))
	}
	return "run", callback(&agent.StreamEvent{Type: "assistant", Content: `{"runs":[` + strings.Join(entries, ",") + `]}`})
}
func (r *composerBatchRunner) CancelRun(string) error                   { return nil }
func (r *composerBatchRunner) RespondToPermission(string, string) error { return nil }
func (r *composerBatchRunner) CancelPermission(string)                  {}
func (r *composerBatchRunner) Shutdown() error                          { return nil }
func (r *composerBatchRunner) IsReady() bool                            { return true }

// saveComposerHour stores n summarized one-minute runs, each in its own app,
// across the last hour.
func saveComposerHour(t *testing.T, store *ActivityStore, n int) []ActivityTimelineRunRow {
	t.Helper()
	base := time.Now().Add(-time.Hour).UnixMilli()
	var runs []ActivityTimelineRunRow
	for i := 0; i < n; i++ {
		start := base + int64(i)*2*60_000
		app := fmt.Sprintf("Notes %02d", i)
		session := ActivitySessionRow{ID: fmt.Sprintf("compose-session-%02d", i), StartTime: start, EndTime: start + 60_000, AppNames: fmt.Sprintf("[%q]", app), TriageStatus: "done"}
		event := activitymon.ActivityEvent{Kind: activitymon.EventFocus, AppName: app, Timestamp: start + 1000, WindowTitle: fmt.Sprintf("Plan part %d", i)}
		if err := store.SaveSession(session, []activitymon.ActivityEvent{event}, nil); err != nil {
			t.Fatal(err)
		}
		rows, err := store.GetSessionEventRows(session.ID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("source events: rows=%v err=%v", rows, err)
		}
		run := testRun(fmt.Sprintf("tg_compose_%02d", i), app, start, start+60_000, 60_000, 1)
		run.AppKey = timelineEventAppKey(event)
		run.SessionIDs = []string{session.ID}
		run.FirstEventRowID, run.LastEventRowID, run.EventCount = rows[0].ID, rows[0].ID, 1
		insertComposableTimelineRun(t, store, run, ActivityTimelineGroupSummary{Title: fmt.Sprintf("Wrote plan part %d", i)})
		runs = append(runs, run)
	}
	return runs
}

// 2026-11-13: the composer made 18 of 29 calls in an hour of Bo's app, one
// per run. An hour of 29 runs is now placed in at most four calls, in order.
func TestComposerPlacesAnHourOfRunsInFewCalls(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTimelineTestStore(t)
	runner := &composerBatchRunner{}
	app := &App{runner: runner, activity: &activityMonitorState{store: store}}
	runs := saveComposerHour(t, store, 29)
	triager := &ActivityTriager{app: app}
	triager.runActivityComposerPass(context.Background())

	if runner.calls > 4 {
		t.Fatalf("composer calls for 29 runs in an hour = %d, want at most 4", runner.calls)
	}
	if runner.calls != runner.batches {
		t.Fatalf("%d of %d calls fell back to one run each", runner.calls-runner.batches, runner.calls)
	}
	ready, err := store.GetComposerReadyRuns(100, 0, 0)
	if err != nil || len(ready) != 0 {
		t.Fatalf("%d runs left unplaced (%v)", len(ready), err)
	}
	// Order holds inside a batch: every third run joined the activity the
	// run just before it started in the same call.
	for i, run := range runs {
		var activityID string
		if err := store.db.QueryRow(`SELECT activity_id FROM activity_run_links WHERE run_id = ?`, run.ID).Scan(&activityID); err != nil {
			t.Fatalf("run %s has no link: %v", run.ID, err)
		}
		want := "act_" + run.ID
		if i%activityComposerBatchSize%3 == 2 {
			want = "act_" + runs[i-1].ID
		}
		if activityID != want {
			t.Fatalf("run %d placed in %s, want %s", i, activityID, want)
		}
	}
}

func TestComposerBatchPlacesLeftOutRunsOneAtATime(t *testing.T) {
	isolateActivityPrivacyTest(t)
	store := newTimelineTestStore(t)
	runner := &composerBatchRunner{omit: map[string]bool{"tg_compose_04": true}}
	app := &App{runner: runner, activity: &activityMonitorState{store: store}}
	saveComposerHour(t, store, 6)
	triager := &ActivityTriager{app: app}
	triager.runActivityComposerPass(context.Background())
	if runner.batches != 1 || runner.calls != 2 {
		t.Fatalf("calls=%d batches=%d, want one batch and one single call for the left-out run", runner.calls, runner.batches)
	}
	ready, err := store.GetComposerReadyRuns(100, 0, 0)
	if err != nil || len(ready) != 0 {
		t.Fatalf("%d runs left unplaced (%v)", len(ready), err)
	}
}

func TestComposerOneRunPromptUnchangedBySplit(t *testing.T) {
	run := testRun("tg_one", "Notes", 1000, 61_000, 60_000, 1)
	summary := ActivityTimelineGroupSummary{Title: "Wrote the plan"}
	prompt := buildComposerPrompt(nil, run, summary, composerTransition{}, "")
	for _, part := range []string{"You maintain a person's \"activities\"", "Candidate resumable activities:\n(no eligible candidates)", "\nNew app run to place:\n- app: Notes", "Respond with ONLY this JSON object:"} {
		if !strings.Contains(prompt, part) {
			t.Fatalf("one-run prompt lost %q", part)
		}
	}
	if strings.Contains(prompt, "runId=") {
		t.Fatal("one-run prompt carries batch markers")
	}
}
