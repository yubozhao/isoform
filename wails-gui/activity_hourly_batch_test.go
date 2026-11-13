package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/platform/activitymon"
	"yansu-agent/agent"
)

// batchCountingRunner counts model calls by kind. Triage says every batch is
// worth a memory, so the test sees the follow-on memory call too.
type batchCountingRunner struct {
	mu      sync.Mutex
	calls   int
	prompts []string
}

const batchTriageResponse = `{"title":"Planning","description":"Planned the week","worthMemory":true,"worthKnowledge":false}`

func (r *batchCountingRunner) Init(context.Context) error { return nil }
func (r *batchCountingRunner) ExecuteTask(_ context.Context, input *agent.TaskInput, callback agent.StreamCallback) (string, error) {
	r.mu.Lock()
	r.calls++
	r.prompts = append(r.prompts, input.Prompt)
	r.mu.Unlock()
	if err := callback(&agent.StreamEvent{Type: "assistant", Content: batchTriageResponse}); err != nil {
		return "", err
	}
	return "run-test", nil
}
func (r *batchCountingRunner) CancelRun(string) error                   { return nil }
func (r *batchCountingRunner) RespondToPermission(string, string) error { return nil }
func (r *batchCountingRunner) CancelPermission(string)                  {}
func (r *batchCountingRunner) Shutdown() error                          { return nil }
func (r *batchCountingRunner) IsReady() bool                            { return true }

func (r *batchCountingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func newBatchTestTriager(t *testing.T) (*ActivityTriager, *ActivityStore, *batchCountingRunner) {
	t.Helper()
	isolateActivityPrivacyTest(t)
	store := newTimelineTestStore(t)
	runner := &batchCountingRunner{}
	app := newTranslationTestApp(t, &memoryTranslationFakeRunner{ready: true})
	app.runner = runner
	app.activity = &activityMonitorState{store: store}
	// Task Shadow has its own observer calls; these tests count the summary
	// triage and memory pipeline only.
	if err := app.SaveUIState(triageEnableShadowKey, "false"); err != nil {
		t.Fatal(err)
	}
	return NewActivityTriager(app, nil), store, runner
}

// saveBatchTestSession stores a session that passes the pre-filter: two apps
// of its own (so no two sessions merge), 24 events over two minutes.
func saveBatchTestSession(t *testing.T, store *ActivityStore, i int, start time.Time, apps [2]string, titles [2]string) ActivitySessionRow {
	t.Helper()
	var events []activitymon.ActivityEvent
	for j := 0; j < 24; j++ {
		events = append(events, activitymon.ActivityEvent{
			Timestamp:   start.Add(time.Duration(j) * 5 * time.Second).UnixMilli(),
			Kind:        activitymon.EventFocus,
			AppName:     apps[j%2],
			AppBundleID: "com.example." + strings.ToLower(strings.ReplaceAll(apps[j%2], " ", "")),
			WindowTitle: fmt.Sprintf("%s %d", titles[j%2], i),
		})
	}
	session := ActivitySessionRow{
		ID:           fmt.Sprintf("batch-session-%02d", i),
		StartTime:    start.UnixMilli(),
		EndTime:      start.Add(2 * time.Minute).UnixMilli(),
		TriggerKind:  "idle_transition",
		AppNames:     fmt.Sprintf(`[%q,%q]`, apps[0], apps[1]),
		EventCount:   len(events),
		TriageStatus: "pending",
		CreatedAt:    start.Add(2 * time.Minute).UnixMilli(),
	}
	if err := store.SaveSession(session, events, nil); err != nil {
		t.Fatal(err)
	}
	return session
}

func TestActivityBatchFiftyActivitiesMakeOneBatch(t *testing.T) {
	triager, store, runner := newBatchTestTriager(t)
	hour := time.Now().Add(-time.Hour).Truncate(time.Minute)
	for i := 0; i < 50; i++ {
		apps := [2]string{fmt.Sprintf("Notes %d", i), fmt.Sprintf("Sheets %d", i)}
		saveBatchTestSession(t, store, i, hour.Add(time.Duration(i)*72*time.Second), apps, [2]string{"Quarter plan", "Budget"})
	}
	if err := triager.runBatch(context.Background(), "hourly"); err != nil {
		t.Fatal(err)
	}
	triager.wg.Wait()
	// One triage call for the hour plus its one memory call. The per-activity
	// pipeline made a triage call and a memory call for each of the 50.
	if got := runner.count(); got != 2 {
		t.Fatalf("model calls for 50 activities in an hour = %d, want 2 (one triage, one memory)", got)
	}
	pending, err := store.GetPendingSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("%d sessions still pending after the batch", len(pending))
	}
	runs, err := store.ListActivityPipelineRuns(10)
	if err != nil {
		t.Fatal(err)
	}
	groupRuns := 0
	for _, run := range runs {
		if run.Kind != "tick" && run.TriageStatus == "dismissed" && run.TriageReason == "triage complete" {
			groupRuns++
			if n := pipelineRunSessionCount(run); n != 50 {
				t.Fatalf("batch run covers %d sessions, want 50", n)
			}
		}
	}
	if groupRuns != 1 {
		t.Fatalf("triaged pipeline runs = %d, want 1", groupRuns)
	}
}

func TestActivityBatchIdleHourMakesNoCalls(t *testing.T) {
	triager, _, runner := newBatchTestTriager(t)
	for i := 0; i < 2; i++ {
		if err := triager.runBatch(context.Background(), "hourly"); err != nil {
			t.Fatal(err)
		}
	}
	triager.wg.Wait()
	if got := runner.count(); got != 0 {
		t.Fatalf("idle hour made %d model calls, want 0", got)
	}
	if triager.lastBatchRun().IsZero() {
		t.Fatal("an idle batch still records its time")
	}
}

func TestActivityBatchAgentActivityMakesNoUserMemories(t *testing.T) {
	triager, store, runner := newBatchTestTriager(t)
	hour := time.Now().Add(-time.Hour).Truncate(time.Minute)
	// Claude Code in a terminal next to the Codex app: every app is an agent.
	session := saveBatchTestSession(t, store, 1, hour, [2]string{"Codex", "Ghostty"}, [2]string{"Codex", "Claude Code"})
	// The Claude app alone, captured as a session without stored events.
	claude := ActivitySessionRow{ID: "batch-session-claude", StartTime: hour.Add(10 * time.Minute).UnixMilli(), EndTime: hour.Add(13 * time.Minute).UnixMilli(),
		TriggerKind: "idle_transition", AppNames: `["Claude"]`, EventCount: 30, TriageStatus: "pending"}
	if err := store.SaveSession(claude, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := triager.runBatch(context.Background(), "hourly"); err != nil {
		t.Fatal(err)
	}
	triager.wg.Wait()
	if got := runner.count(); got != 0 {
		t.Fatalf("agent activity made %d model calls, want 0", got)
	}
	for _, id := range []string{session.ID, claude.ID} {
		saved, err := store.GetSessionByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if saved.TriageStatus != "dismissed" {
			t.Fatalf("agent session %s status=%q, want dismissed", id, saved.TriageStatus)
		}
	}
	runs, err := store.ListActivityPipelineRuns(10)
	if err != nil {
		t.Fatal(err)
	}
	labelled := false
	for _, run := range runs {
		if run.TriageReason == activityAgentReason && pipelineRunSessionCount(run) == 2 {
			labelled = true
		}
	}
	if !labelled {
		t.Fatal("agent sessions were not labelled agent activity")
	}
}

func TestActivityBatchMixedHourKeepsAgentSessionsOutOfThePrompt(t *testing.T) {
	triager, store, runner := newBatchTestTriager(t)
	hour := time.Now().Add(-time.Hour).Truncate(time.Minute)
	saveBatchTestSession(t, store, 1, hour, [2]string{"Codex", "Ghostty"}, [2]string{"agent-only-marker", "Claude Code"})
	saveBatchTestSession(t, store, 2, hour.Add(20*time.Minute), [2]string{"Notes", "Sheets"}, [2]string{"Quarter plan", "Budget"})
	if err := triager.runBatch(context.Background(), "hourly"); err != nil {
		t.Fatal(err)
	}
	triager.wg.Wait()
	if got := runner.count(); got != 2 {
		t.Fatalf("model calls = %d, want 2 (one triage, one memory)", got)
	}
	for _, prompt := range runner.prompts {
		if strings.Contains(prompt, "agent-only-marker") {
			t.Fatal("agent activity reached the triage or memory prompt")
		}
	}
}

func TestActivitySessionIsAgent(t *testing.T) {
	focus := func(bundle, name, title string) activitymon.ActivityEvent {
		return activitymon.ActivityEvent{Kind: activitymon.EventFocus, AppBundleID: bundle, AppName: name, WindowTitle: title}
	}
	cases := []struct {
		name    string
		session ActivitySessionRow
		events  []activitymon.ActivityEvent
		want    bool
	}{
		{"claude app", ActivitySessionRow{}, []activitymon.ActivityEvent{focus("com.anthropic.claudefordesktop", "Claude", "Code")}, true},
		{"codex app", ActivitySessionRow{}, []activitymon.ActivityEvent{focus("com.openai.codex", "Codex", "")}, true},
		{"terminal running claude code", ActivitySessionRow{}, []activitymon.ActivityEvent{focus("com.mitchellh.ghostty", "Ghostty", "Claude Code")}, true},
		{"plain terminal", ActivitySessionRow{}, []activitymon.ActivityEvent{focus("com.mitchellh.ghostty", "Ghostty", "zsh")}, false},
		{"agent and browser", ActivitySessionRow{}, []activitymon.ActivityEvent{focus("com.openai.codex", "Codex", ""), focus("com.google.Chrome", "Google Chrome", "Docs")}, false},
		{"speech next to agent", ActivitySessionRow{}, []activitymon.ActivityEvent{focus("com.openai.codex", "Codex", ""), {Kind: activitymon.EventSpeech, SpeechText: "hello"}}, false},
		{"names only", ActivitySessionRow{AppNames: `["Claude","Codex"]`}, nil, true},
		{"meeting names only", ActivitySessionRow{AppNames: `["Claude"]`, IsMeeting: true}, nil, false},
		{"no apps", ActivitySessionRow{}, nil, false},
	}
	for _, tc := range cases {
		if got := activitySessionIsAgent(tc.session, tc.events); got != tc.want {
			t.Errorf("%s: agent=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestActivityBatchClockGatesActivityVoice(t *testing.T) {
	now := time.Now()
	app := &App{}
	if !app.activityBatchAllowsVoice(engagement.KindCapturePaused, now) {
		t.Fatal("without a batch schedule the voice check runs at once")
	}
	app.activityBatch.setDue(now.Add(30 * time.Minute))
	if app.activityBatchAllowsVoice(engagement.KindCapturePaused, now) || app.activityBatchAllowsVoice(engagement.KindCommitmentSlipping, now) {
		t.Fatal("activity-driven voice ran before its batch")
	}
	for _, kind := range []engagement.Kind{engagement.KindWaitingOnReply, engagement.KindBriefReady, engagement.KindMeetingPrep} {
		if !app.activityBatchAllowsVoice(kind, now) {
			t.Fatalf("%s is an engagement card, not activity-driven, and must not wait", kind)
		}
	}
	if got := app.engagementChatReportRetryAt(engagement.KindCapturePaused, now); !got.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("deferred card retries at %v, want the batch", got)
	}
	if got := app.engagementChatReportRetryAt(engagement.KindWaitingOnReply, now); !got.Equal(now.Add(engagementRetryDelay)) {
		t.Fatalf("engagement card retries at %v, want five minutes", got)
	}
	closeWindow := app.activityBatch.openWindow()
	if !app.activityBatchAllowsVoice(engagement.KindCapturePaused, now) {
		t.Fatal("the batch voice pass did not open the window")
	}
	closeWindow()
	if !app.activityBatchAllowsVoice(engagement.KindCapturePaused, now.Add(time.Hour)) {
		t.Fatal("a due batch keeps the window closed")
	}
}

func TestActivityBatchDeferredCardPostsNoChatRowBeforeBatch(t *testing.T) {
	runner := &memoryTranslationFakeRunner{ready: true}
	app := newTranslationTestApp(t, runner)
	app.activityBatch.setDue(time.Now().Add(time.Hour))
	m := engagement.Moment{ID: "m-capture", Kind: engagement.KindCapturePaused, SubjectID: "microphone",
		DedupeKey: "capture_paused:microphone", CreatedAt: time.Now(), Params: map[string]string{"what": "Microphone"}}
	if app.postEngagementReport(m, engagementText{PushBody: "Microphone capture stopped"}) {
		t.Fatal("activity-driven card posted before its batch")
	}
	if runner.calls != 0 {
		t.Fatalf("deferred card made %d model calls", runner.calls)
	}
}

func TestActivityBatchNextRunFollowsLastRun(t *testing.T) {
	triager, _, _ := newBatchTestTriager(t)
	now := time.Now()
	if got := triager.nextBatchAt(now); !got.Equal(now.Add(activityBatchInterval)) {
		t.Fatalf("first batch at %v, want one hour after start", got)
	}
	triager.recordBatchRun(now.Add(-3 * time.Hour))
	restarted := NewActivityTriager(triager.app, nil)
	if got := restarted.nextBatchAt(now); !got.Equal(now.Add(activityBatchStartupDelay)) {
		t.Fatalf("overdue batch after restart at %v, want after the startup delay", got)
	}
	restarted.recordBatchRun(now.Add(-10 * time.Minute))
	if got := restarted.nextBatchAt(now); !got.Equal(now.Add(50 * time.Minute)) {
		t.Fatalf("next batch at %v, want an hour after the last", got)
	}
}

func pipelineRunSessionCount(run ActivityPipelineRunRow) int {
	var ids []string
	_ = json.Unmarshal([]byte(run.SessionIDs), &ids)
	return len(ids)
}
