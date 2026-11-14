package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"yansu-agent/agent"
	"yansu-agent/agent/authority"
	"yansu-agent/agent/tools"

	"wails-gui/storage"
)

// upcomingToolsRig is an app with a running scheduler, a brief service on a
// temporary state directory and a main chat, the way the main chat's tools
// see them.
type upcomingToolsRig struct {
	app   *App
	state *storage.Store
	brief *mobileBriefService
	runs  chan string
}

func newUpcomingToolsRig(t *testing.T, jobs ...storage.CronJobRow) *upcomingToolsRig {
	t.Helper()
	base := newHandoffTestRig(t)
	app := base.app
	app.ctx = context.Background()
	app.activeTags = map[string]bool{}
	app.eventBroadcaster = NewEventBroadcaster()
	ctx := context.Background()
	for _, job := range jobs {
		if err := base.state.SaveCronJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	runs := make(chan string, 8)
	scheduler := NewCronScheduler(base.state, func(string, any) {}, func(string, string) {},
		func(_ context.Context, job *storage.CronJobRow) (*cronExecResult, error) {
			runs <- job.ID
			return &cronExecResult{Summary: "done"}, nil
		})
	app.cronScheduler = scheduler
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.Run(runCtx)
	}()
	select {
	case <-scheduler.loadedCh:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("scheduler did not load")
	}
	t.Cleanup(func() {
		cancel()
		scheduler.Stop()
		<-done
	})
	clock := &fakeMobileBriefClock{now: time.Now().UTC()}
	brief, _ := newTestMobileBriefService(t, clock)
	brief.app = app
	app.mobileBriefSvc = brief
	return &upcomingToolsRig{app: app, state: base.state, brief: brief, runs: runs}
}

func upcomingTestJob(id, name, kind, config string, enabled bool) storage.CronJobRow {
	next := int64(0)
	switch kind {
	case "cron", "every":
		next = time.Now().Add(24 * time.Hour).UnixMilli()
	case "at":
		var at scheduleAt
		_ = json.Unmarshal([]byte(config), &at)
		next = at.AtMs
	default:
	}
	return storage.CronJobRow{
		ID: id, Name: name, Description: name + " for the user", Enabled: enabled,
		ScheduleKind: kind, ScheduleConfig: config, TargetKind: "agent",
		TargetConfig: `{"prompt":"do it"}`, NextRunAt: next,
	}
}

// callUpcomingTool runs one tool the way the registry does: schema basics,
// then Execute.
func callUpcomingTool(t *testing.T, app *App, name string, input map[string]any) tools.ToolResult {
	t.Helper()
	registry := tools.NewRegistry()
	for _, tool := range app.upcomingTools() {
		registry.Register(tool)
	}
	result, err := registry.Execute(context.Background(), name, tools.ToolCall{Input: input})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

func upcomingReportsWith(t *testing.T, app *App, status string) []mainChatReport {
	t.Helper()
	var out []mainChatReport
	for _, row := range mainChatReports(t, app) {
		if report := decodeReport(t, row); report.Status == status {
			out = append(out, report)
		}
	}
	return out
}

func TestUpcomingToolsAuthority(t *testing.T) {
	policy := authority.NewPolicy(nil)
	attended := authority.Context{Mode: authority.ExecutionModeNormal}
	unattended := authority.Context{Mode: authority.ExecutionModeNormal, Unattended: true}
	for _, tool := range (&App{}).upcomingTools() {
		action := tools.DescribeAuthority(tool, map[string]any{"id": "brief:morning"})
		if !strings.HasPrefix(action.Capability, "upcoming.") || action.ToolName != tool.Name() {
			t.Fatalf("%s describes %+v", tool.Name(), action)
		}
		// Running now and resuming start unattended work, so the user
		// approves each one, scoped to the one item.
		want := authority.DecisionAllow
		if tool.Name() == upcomingToolRunNow || tool.Name() == upcomingToolResume {
			want = authority.DecisionRequireApproval
			if action.ApprovalPrefix != tool.Name()+":brief:morning:" {
				t.Fatalf("%s approval prefix = %q, want one scoped to the item", tool.Name(), action.ApprovalPrefix)
			}
		}
		if got := policy.Evaluate(context.Background(), attended, action).Decision; got != want {
			t.Fatalf("%s in the attended main chat = %s, want %s", tool.Name(), got, want)
		}
		// No unattended run (a schedule, a handoff) may use them without
		// a scoped grant.
		if got := policy.Evaluate(context.Background(), unattended, action).Decision; got != authority.DecisionDeny {
			t.Fatalf("%s in an unattended run = %s, want deny", tool.Name(), got)
		}
		if needs, _ := tool.NeedsPermission(map[string]any{}); needs {
			t.Fatalf("%s asks for permission on its own", tool.Name())
		}
	}
}

func TestUpcomingListNamesEveryItemWithItsID(t *testing.T) {
	rig := newUpcomingToolsRig(t, upcomingTestJob("weekly", "Weekly update", "cron", `{"expr":"0 9 * * 1"}`, true))
	result := callUpcomingTool(t, rig.app, upcomingToolList, map[string]any{})
	for _, want := range []string{"id: weekly", `"Weekly update"`, "Mondays at 09:00", "id: brief:morning", "the morning brief", "every day at 07:00", "id: brief:evening"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("list lacks %q:\n%s", want, result.Content)
		}
	}
}

func TestUpcomingChangeMovesTheMorningBriefAndRecordsIt(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	result := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "brief:morning", "time": "7:30"})
	if result.IsError || !strings.Contains(result.Content, "now runs every day at 07:30") || !strings.Contains(result.Content, "one sentence") {
		t.Fatalf("change = %+v", result)
	}
	if got := rig.brief.config.load().Morning; got != "07:30" {
		t.Fatalf("morning = %q", got)
	}
	reports := upcomingReportsWith(t, rig.app, upcomingReportChanged)
	if len(reports) != 1 || reports[0].Item != "brief_morning" || reports[0].Schedule != "30 7 * * *" || reports[0].Target.Kind != mainChatTargetUpcoming {
		t.Fatalf("record lines = %+v", reports)
	}

	refused := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "brief:morning", "days": "weekends"})
	if !refused.IsError || !strings.Contains(refused.Content, "cannot be chosen") {
		t.Fatalf("brief days = %+v", refused)
	}
	late := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "brief:morning", "time": "19:00"})
	if !late.IsError || rig.brief.config.load().Morning != "07:30" {
		t.Fatalf("morning after evening = %+v", late)
	}
	if bad := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "brief:morning", "time": "7.30pm"}); !bad.IsError {
		t.Fatalf("a malformed time was accepted: %+v", bad)
	}
}

func TestUpcomingChangeSetsDaysAndTimeOnASchedule(t *testing.T) {
	rig := newUpcomingToolsRig(t,
		upcomingTestJob("news-like", "Morning digest", "cron", `{"expr":"0 9 * * *","tz":"UTC"}`, true),
		upcomingTestJob("interval", "Inbox sweep", "every", `{"every_ms":3600000}`, true),
	)
	result := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "news-like", "time": "08:15", "days": "weekdays"})
	if result.IsError || !strings.Contains(result.Content, "weekdays at 08:15") {
		t.Fatalf("change = %+v", result)
	}
	job, err := rig.state.LoadCronJob(context.Background(), "news-like")
	if err != nil {
		t.Fatal(err)
	}
	var cfg scheduleCron
	_ = json.Unmarshal([]byte(job.ScheduleConfig), &cfg)
	// The UTC schedule now keeps the user's local time.
	if cfg.Expr != "15 8 * * 1-5" || cfg.TZ != "" || job.NextRunAt <= time.Now().UnixMilli() {
		t.Fatalf("stored schedule = %+v next %v", cfg, time.UnixMilli(job.NextRunAt))
	}
	if reports := upcomingReportsWith(t, rig.app, upcomingReportChanged); len(reports) != 1 || reports[0].Title != "Morning digest" || reports[0].Schedule != "15 8 * * 1-5" {
		t.Fatalf("record lines = %+v", reports)
	}

	weekends := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "Morning digest", "days": "sat, sun"})
	if weekends.IsError {
		t.Fatalf("change by name = %+v", weekends)
	}
	job, _ = rig.state.LoadCronJob(context.Background(), "news-like")
	_ = json.Unmarshal([]byte(job.ScheduleConfig), &cfg)
	if cfg.Expr != "15 8 * * 0,6" {
		t.Fatalf("weekend schedule = %q", cfg.Expr)
	}

	if interval := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "interval", "time": "07:00"}); !interval.IsError {
		t.Fatalf("an interval schedule took a time: %+v", interval)
	}
}

func TestUpcomingChangeToAPassedTimeSuggestsInstead(t *testing.T) {
	tomorrow := time.Now().Add(24 * time.Hour)
	at := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 10, 0, 0, 0, time.Local)
	rig := newUpcomingToolsRig(t, upcomingTestJob("once", "Send the deck", "at", `{"at_ms":`+jsonInt(at.UnixMilli())+`}`, true))
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	result := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "once", "time": "09:00", "date": yesterday})
	if result.IsError || !strings.Contains(result.Content, "Not changed") || !strings.Contains(result.Content, "Suggest") {
		t.Fatalf("passed time = %+v", result)
	}
	job, _ := rig.state.LoadCronJob(context.Background(), "once")
	if job.NextRunAt != at.UnixMilli() {
		t.Fatalf("a passed time changed the schedule: next %v", time.UnixMilli(job.NextRunAt))
	}
	if reports := upcomingReportsWith(t, rig.app, upcomingReportChanged); len(reports) != 0 {
		t.Fatalf("a refused change was recorded: %+v", reports)
	}
}

func TestUpcomingPauseResumeAndRunNow(t *testing.T) {
	rig := newUpcomingToolsRig(t, upcomingTestJob("weekly", "Weekly update", "cron", `{"expr":"0 9 * * 1"}`, true))

	if result := callUpcomingTool(t, rig.app, upcomingToolPause, map[string]any{"id": "weekly"}); result.IsError {
		t.Fatalf("pause = %+v", result)
	}
	if job, _ := rig.app.GetCronJob("weekly"); job.Enabled {
		t.Fatal("pause left the schedule on")
	}
	if result := callUpcomingTool(t, rig.app, upcomingToolResume, map[string]any{"id": "weekly"}); result.IsError || !strings.Contains(result.Content, "Next run") {
		t.Fatalf("resume = %+v", result)
	}
	if job, _ := rig.app.GetCronJob("weekly"); !job.Enabled {
		t.Fatal("resume left the schedule paused")
	}
	if result := callUpcomingTool(t, rig.app, upcomingToolRunNow, map[string]any{"id": "weekly"}); result.IsError {
		t.Fatalf("run now = %+v", result)
	}
	select {
	case id := <-rig.runs:
		if id != "weekly" {
			t.Fatalf("ran %s", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run now did not run the schedule")
	}
	for _, status := range []string{upcomingReportPaused, upcomingReportResumed, upcomingReportStarted} {
		if reports := upcomingReportsWith(t, rig.app, status); len(reports) != 1 || reports[0].Target.ID != "weekly" {
			t.Fatalf("%s record lines = %+v", status, reports)
		}
	}

	result := callUpcomingTool(t, rig.app, upcomingToolPause, map[string]any{"id": "brief:evening"})
	if result.IsError || !strings.Contains(result.Content, "share one switch") || rig.brief.config.load().Enabled {
		t.Fatalf("pause the evening brief = %+v", result)
	}
}

func TestUpcomingRunNowRefusesIsoformsOwnSearch(t *testing.T) {
	finds := upcomingTestJob("finds", topicFindsJobName, "manual", `{}`, true)
	finds.SurfaceKind = todayNewsSurfaceKind
	rig := newUpcomingToolsRig(t, finds)
	job, err := rig.app.GetCronJob("finds")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.app.runUpcomingNow(upcomingTarget{Kind: upcomingKindSchedule, Job: job}); err == nil || !strings.Contains(err.Error(), "Find new on Today") {
		t.Fatalf("run now of the topic search = %v", err)
	}
	select {
	case id := <-rig.runs:
		t.Fatalf("the chat ran Isoform's search %s", id)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestUpcomingToolsRefuseAProposalWaitingForApproval(t *testing.T) {
	proposal := upcomingTestJob("proposal", "Weekly release notes", "every", `{"every_ms":604800000}`, false)
	proposal.SourcePlatform = "agency"
	proposal.SurfaceKind = "schedule_permission"
	proposal.ScheduleEvidence = "You write the release notes every Friday."
	rig := newUpcomingToolsRig(t, proposal)

	for _, name := range []string{upcomingToolResume, upcomingToolRunNow, upcomingToolRemove, upcomingToolChange} {
		result := callUpcomingTool(t, rig.app, name, map[string]any{"id": "proposal", "days": "weekdays"})
		if !result.IsError || !strings.Contains(result.Content, "waiting for the user's approval") {
			t.Fatalf("%s on a proposal = %+v", name, result)
		}
	}
	if job, _ := rig.app.GetCronJob("proposal"); job.Enabled {
		t.Fatal("the chat turned on a proposal")
	}
	select {
	case id := <-rig.runs:
		t.Fatalf("the chat ran the proposal %s", id)
	default:
	}
	if items := upcomingConfirmCards(t, rig.app); len(items) != 0 {
		t.Fatalf("a confirm card was posted for a proposal: %+v", items)
	}
}

func upcomingConfirmCards(t *testing.T, app *App) []storage.StreamItemRow {
	t.Helper()
	main, err := app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items, err := app.store.LoadStreamItems(context.Background(), main.LocalID)
	if err != nil {
		t.Fatal(err)
	}
	var out []storage.StreamItemRow
	for _, item := range items {
		if item.ToolName == upcomingConfirmTool {
			out = append(out, item)
		}
	}
	return out
}

func TestUpcomingRemoveOnlyHappensOnTheUsersPress(t *testing.T) {
	rig := newUpcomingToolsRig(t,
		upcomingTestJob("weekly", "Weekly update", "cron", `{"expr":"0 9 * * 1"}`, true),
		upcomingTestJob("daily", "Daily digest", "cron", `{"expr":"0 8 * * *"}`, true),
	)
	for _, id := range []string{"brief", "brief:morning"} {
		if result := callUpcomingTool(t, rig.app, upcomingToolRemove, map[string]any{"id": id}); !result.IsError {
			t.Fatalf("remove %s = %+v", id, result)
		}
	}

	result := callUpcomingTool(t, rig.app, upcomingToolRemove, map[string]any{"id": "weekly"})
	if result.IsError || !strings.Contains(result.Content, "confirm card") {
		t.Fatalf("remove = %+v", result)
	}
	if _, err := rig.app.GetCronJob("weekly"); err != nil {
		t.Fatal("the tool removed the schedule before the user confirmed")
	}
	cards := upcomingConfirmCards(t, rig.app)
	if len(cards) != 1 {
		t.Fatalf("cards = %+v", cards)
	}

	// Keep leaves it; a second answer changes nothing.
	if got, err := rig.app.ResolveUpcomingConfirm(cards[0].ID, false); err != nil || got.Status != upcomingConfirmKept {
		t.Fatalf("keep = %+v, %v", got, err)
	}
	if got, _ := rig.app.ResolveUpcomingConfirm(cards[0].ID, true); got.Status != upcomingConfirmKept {
		t.Fatalf("a kept card acted again: %+v", got)
	}
	if _, err := rig.app.GetCronJob("weekly"); err != nil {
		t.Fatal("keep removed the schedule")
	}

	// Remove on a fresh card removes it and records the line.
	callUpcomingTool(t, rig.app, upcomingToolRemove, map[string]any{"id": "weekly"})
	cards = upcomingConfirmCards(t, rig.app)
	if got, err := rig.app.ResolveUpcomingConfirm(cards[1].ID, true); err != nil || got.Status != upcomingConfirmRemoved {
		t.Fatalf("remove = %+v, %v", got, err)
	}
	if _, err := rig.app.GetCronJob("weekly"); err == nil {
		t.Fatal("the schedule is still there")
	}
	if reports := upcomingReportsWith(t, rig.app, upcomingReportRemoved); len(reports) != 1 || reports[0].Title != "Weekly update" {
		t.Fatalf("removed record lines = %+v", reports)
	}
	saved, _ := rig.state.LoadStreamItem(context.Background(), cards[1].ID)
	if saved == nil || !strings.Contains(saved.ToolInput, `"status":"removed"`) {
		t.Fatalf("card state = %+v", saved)
	}

	// An expired card removes nothing.
	callUpcomingTool(t, rig.app, upcomingToolRemove, map[string]any{"id": "daily"})
	cards = upcomingConfirmCards(t, rig.app)
	expired := cards[2]
	upcomingConfirmMu.Lock()
	issued := upcomingConfirmIssued[expired.ID]
	issued.expiresAt = time.Now().Add(-time.Minute)
	upcomingConfirmIssued[expired.ID] = issued
	upcomingConfirmMu.Unlock()
	if got, _ := rig.app.ResolveUpcomingConfirm(expired.ID, true); got.Status != upcomingConfirmExpired {
		t.Fatalf("expired card = %+v", got)
	}
	if _, err := rig.app.GetCronJob("daily"); err != nil {
		t.Fatal("an expired card removed the schedule")
	}

	var payload upcomingConfirmPayload
	_ = json.Unmarshal([]byte(expired.ToolInput), &payload)

	// A card this process did not post (written by anything else, or from
	// before a restart) removes nothing, even in the main chat.
	ctx := context.Background()
	mainChat, err := rig.app.ensureMainChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload.Status = upcomingConfirmPending
	payload.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	unknown, _ := json.Marshal(payload)
	if _, err := rig.state.AppendStreamItem(ctx, mainChat.LocalID, storage.StreamItemRow{ID: "written-elsewhere", Kind: "system", ToolName: upcomingConfirmTool, ToolInput: string(unknown)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := rig.app.ResolveUpcomingConfirm("written-elsewhere", true); got.Status != upcomingConfirmExpired {
		t.Fatalf("an unknown card = %+v", got)
	}
	if _, err := rig.app.GetCronJob("daily"); err != nil {
		t.Fatal("an unknown card removed the schedule")
	}

	// A card outside the main chat is refused.
	if err := rig.state.SaveSession(ctx, storage.SessionRow{LocalID: "side", ProjectID: mainChat.ProjectID, Label: "Side", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	payload.JobID = "daily"
	data, _ := json.Marshal(payload)
	if _, err := rig.state.AppendStreamItem(ctx, "side", storage.StreamItemRow{ID: "forged", Kind: "system", ToolName: upcomingConfirmTool, ToolInput: string(data)}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.app.ResolveUpcomingConfirm("forged", true); err == nil {
		t.Fatal("a card outside the main chat was acted on")
	}
	if _, err := rig.app.GetCronJob("daily"); err != nil {
		t.Fatal("a forged card removed the schedule")
	}
}

func TestUpcomingToolsContextIsForTheMainChatOnly(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rig.app.injectUpcomingToolsContext(main.LocalID, promptOriginMac)
	if !strings.Contains(got, "UpcomingChange") || !strings.Contains(got, "Never use yansu cron update, yansu cron delete or yansu cron run") {
		t.Fatalf("main chat context = %q", got)
	}
	if strings.Contains(got, "not available") || strings.Contains(got, "tell the user what to change") {
		t.Fatalf("main chat apologizes for missing Upcoming tools: %q", got)
	}
	if got := rig.app.injectUpcomingToolsContext("some-other-chat", promptOriginMac); got != "" {
		t.Fatalf("another chat got the Upcoming context: %q", got)
	}
}

func TestUpcomingToolsAreForTheMacOnly(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := rig.app.mainChatExtraTools(main.LocalID, promptOriginMac); len(got) != 8 || got[7].Name() != chatWebSearchToolName {
		t.Fatalf("the main chat on the Mac got %d tools, want the six Upcoming tools, GoalCreate and SearchWeb", len(got))
	}
	if got := rig.app.mainChatExtraTools(main.LocalID, promptOriginPhone); len(got) != 2 || got[0].Name() != goalToolCreate || got[1].Name() != chatWebSearchToolName {
		t.Fatalf("a message from the phone got tools other than GoalCreate and SearchWeb: %v", got)
	}
	if got := rig.app.mainChatExtraTools("some-other-chat", promptOriginMac); got != nil {
		t.Fatalf("another chat got tools: %v", got)
	}

	// The phone's turn is told to change nothing and to point at the Mac.
	phone := rig.app.injectUpcomingToolsContext(main.LocalID, promptOriginPhone)
	if !strings.Contains(phone, "can be changed only on the computer") || !strings.Contains(phone, "do not use the yansu cron CLI") || strings.Contains(phone, "call UpcomingList") {
		t.Fatalf("phone context = %q", phone)
	}
	if !strings.Contains(phone, "ask in Chat on the computer (say 聊天 in Chinese)") {
		t.Fatalf("phone context lacks the localized Chat name: %q", phone)
	}

	// The turn the phone starts reaches the runner without the Upcoming
	// tools, and its prompt carries the phone's context.
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 2)}
	rig.app.runner = runner
	rig.app.tagCancels = map[string]context.CancelFunc{}
	rig.app.sessionTempDirs = map[string]string{}
	rig.app.streams = map[string]context.CancelFunc{}
	rig.app.runIDByTag = map[string]string{}
	rig.app.pendingTraceIDs = map[string]string{}
	for _, tc := range []struct {
		origin    promptOrigin
		wantTools int
		wantText  string
	}{
		{promptOriginPhone, 2, "can be changed only on the computer"},
		{promptOriginMac, 9, "call UpcomingList"}, // with SearchWeb and DraftMessage
	} {
		if err := rig.app.executePrompt("move my weekly update", "", "m", "", main.LocalID, nil, false, false, "", nil, nil, "", "", tc.origin); err != nil {
			t.Fatal(err)
		}
		var input *agent.TaskInput
		select {
		case input = <-runner.inputs:
		case <-time.After(5 * time.Second):
			t.Fatal("the runner was not called")
		}
		if len(input.ExtraTools) != tc.wantTools {
			t.Fatalf("origin %q: %d tools, want %d", tc.origin, len(input.ExtraTools), tc.wantTools)
		}
		prompt, _ := input.PromptFunc()
		if !strings.Contains(prompt, tc.wantText) {
			t.Fatalf("origin %q: prompt lacks %q", tc.origin, tc.wantText)
		}
		if tc.origin == promptOriginPhone {
			for _, retired := range []string{"main chat", "Main chat", "主对话"} {
				if strings.Contains(prompt, retired) {
					t.Fatalf("phone prompt contains retired chat name %q: %q", retired, prompt)
				}
			}
		}
		waitForTagIdle(t, rig.app, main.LocalID)
	}
}

// upcomingCaptureRunner hands each task's input to the test and ends the
// run at once.
type upcomingCaptureRunner struct {
	inputs chan *agent.TaskInput
}

func (r *upcomingCaptureRunner) Init(context.Context) error { return nil }
func (r *upcomingCaptureRunner) IsReady() bool              { return true }
func (r *upcomingCaptureRunner) ExecuteTask(_ context.Context, input *agent.TaskInput, _ agent.StreamCallback) (string, error) {
	r.inputs <- input
	return "upcoming-run", nil
}
func (r *upcomingCaptureRunner) CancelRun(string) error                   { return nil }
func (r *upcomingCaptureRunner) RespondToPermission(string, string) error { return nil }
func (r *upcomingCaptureRunner) CancelPermission(string)                  {}
func (r *upcomingCaptureRunner) Shutdown() error                          { return nil }

func waitForTagIdle(t *testing.T, app *App, tag string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		app.mu.Lock()
		busy := app.activeTags[tag]
		app.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the turn did not finish")
}

func TestMainChatContextOffersNoCronCLIForExistingSchedules(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mainContext := rig.app.injectYansuContext("", main.LocalID, "")
	if strings.Contains(mainContext, "cron delete") || strings.Contains(mainContext, "cron update") || !strings.Contains(mainContext, "follow <upcoming-tools>") {
		t.Fatalf("main chat yansu context still offers the CLI for existing schedules:\n%s", mainContext)
	}
	if !strings.Contains(mainContext, "cron add") {
		t.Fatal("the main chat lost the way to create a schedule")
	}
	if other := rig.app.injectYansuContext("", "some-other-chat", ""); !strings.Contains(other, "cron delete") {
		t.Fatal("another chat lost its cron CLI instructions")
	}
}

// soonerTestJob is a weekly schedule whose next run is in three days, at
// 09:00, with its stored next run where the scheduler would put it.
func soonerTestJob(t *testing.T, enabled bool) (storage.CronJobRow, time.Weekday) {
	t.Helper()
	day := (time.Now().Weekday() + 3) % 7
	config := `{"expr":"0 9 * * ` + strconv.Itoa(int(day)) + `"}`
	job := upcomingTestJob("weekly", "Weekly update", "cron", config, enabled)
	next, err := computeFirstRun("cron", json.RawMessage(config), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	job.NextRunAt = next
	return job, day
}

func TestUpcomingChangeThatBringsARunSoonerAsksFirst(t *testing.T) {
	job, day := soonerTestJob(t, true)
	rig := newUpcomingToolsRig(t, job)
	policy := authority.NewPolicy(nil)
	attended := authority.Context{Mode: authority.ExecutionModeNormal}
	change := &upcomingTool{app: rig.app, name: upcomingToolChange, now: time.Now}
	dayName := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	soon := time.Now().Add(2 * time.Minute).Format("15:04")

	cases := []struct {
		name  string
		input map[string]any
		asks  bool
	}{
		{"a time minutes away", map[string]any{"id": "weekly", "time": soon, "days": "every_day"}, true},
		{"days that come sooner", map[string]any{"id": "weekly", "days": "every_day"}, true},
		{"more days, same next run", map[string]any{"id": "weekly", "days": dayName[day] + "," + dayName[(day+1)%7]}, true},
		{"a later day", map[string]any{"id": "weekly", "days": dayName[(day+1)%7]}, false},
		{"a later time the same day", map[string]any{"id": "weekly", "time": "10:00"}, false},
	}
	for _, tc := range cases {
		action := tools.DescribeAuthority(change, tc.input)
		decision := policy.Evaluate(context.Background(), attended, action).Decision
		if asks := decision == authority.DecisionRequireApproval; asks != tc.asks {
			t.Fatalf("%s: decision %s, want asking %v", tc.name, decision, tc.asks)
		}
		if !tc.asks {
			continue
		}
		// The question names the schedule, and "allow for this chat"
		// covers only this one new schedule.
		if !strings.Contains(action.ApprovalReason, `"Weekly update"`) || strings.Contains(action.ApprovalReason, "weekly ") {
			t.Fatalf("%s: reason %q does not name the schedule", tc.name, action.ApprovalReason)
		}
		approval, _ := change.approval(tc.input)
		if action.ApprovalPrefix != upcomingToolChange+":weekly:"+approval.Schedule+":" || approval.Schedule == "" {
			t.Fatalf("%s: prefix %q, schedule %q", tc.name, action.ApprovalPrefix, approval.Schedule)
		}
	}

	// The brief makes one brief per slot a day, so moving it never asks.
	brief := tools.DescribeAuthority(change, map[string]any{"id": "brief:morning", "time": soon})
	if got := policy.Evaluate(context.Background(), attended, brief).Decision; got != authority.DecisionAllow {
		t.Fatalf("moving the brief = %s, want allow", got)
	}
}

func TestUpcomingChangeOfAPausedScheduleNeverTurnsItOn(t *testing.T) {
	paused, _ := soonerTestJob(t, false)
	yesterday := time.Now().Add(-24 * time.Hour)
	finished := upcomingTestJob("once", "Email the Q3 summary", "at", `{"at_ms":`+jsonInt(yesterday.UnixMilli())+`}`, false)
	finished.LastRunAt = yesterday.UnixMilli()
	finished.LastRunStatus = "ok"
	finished.NextRunAt = 0
	rig := newUpcomingToolsRig(t, paused, finished)
	policy := authority.NewPolicy(nil)
	attended := authority.Context{Mode: authority.ExecutionModeNormal}
	soon := time.Now().Add(2 * time.Minute)

	// A one-time schedule that already ran keeps its new time but stays
	// off; turning it on is a resume, which the user approves.
	input := map[string]any{"id": "once", "time": soon.Format("15:04"), "date": soon.Format("2006-01-02")}
	change := &upcomingTool{app: rig.app, name: upcomingToolChange, now: time.Now}
	if got := policy.Evaluate(context.Background(), attended, tools.DescribeAuthority(change, input)).Decision; got != authority.DecisionAllow {
		t.Fatalf("changing a finished schedule = %s; it starts nothing, so it need not ask", got)
	}
	result := callUpcomingTool(t, rig.app, upcomingToolChange, input)
	if result.IsError || !strings.Contains(result.Content, "stays off") || !strings.Contains(result.Content, "UpcomingResume") {
		t.Fatalf("change = %+v", result)
	}
	job, err := rig.app.GetCronJob("once")
	if err != nil {
		t.Fatal(err)
	}
	if job.Enabled {
		t.Fatal("a change turned a finished one-time schedule back on")
	}
	var at scheduleAt
	raw, _ := json.Marshal(job.ScheduleConfig)
	_ = json.Unmarshal(raw, &at)
	if time.UnixMilli(at.AtMs).Format("15:04") != soon.Format("15:04") {
		t.Fatalf("the new time was not kept: %v", time.UnixMilli(at.AtMs))
	}

	// A paused repeating schedule moved to a minute from now stays paused.
	if result := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "weekly", "time": soon.Format("15:04"), "days": "every_day"}); result.IsError || !strings.Contains(result.Content, "paused") {
		t.Fatalf("paused change = %+v", result)
	}
	if job, _ := rig.app.GetCronJob("weekly"); job.Enabled {
		t.Fatal("a change turned a paused schedule on")
	}
	select {
	case id := <-rig.runs:
		t.Fatalf("a change ran %s", id)
	default:
	}
}

func TestUpcomingApprovalsNameTheItem(t *testing.T) {
	rig := newUpcomingToolsRig(t, upcomingTestJob("3f9c-weekly", "Weekly update", "cron", `{"expr":"0 9 * * 1"}`, false))
	for _, name := range []string{upcomingToolResume, upcomingToolRunNow} {
		tool := &upcomingTool{app: rig.app, name: name, now: time.Now}
		action := tools.DescribeAuthority(tool, map[string]any{"id": "3f9c-weekly"})
		if !strings.Contains(action.ApprovalReason, `"Weekly update"`) || strings.Contains(action.ApprovalReason, "3f9c") {
			t.Fatalf("%s reason = %q", name, action.ApprovalReason)
		}
		if action.ApprovalPrefix != name+":3f9c-weekly:" {
			t.Fatalf("%s prefix = %q", name, action.ApprovalPrefix)
		}
		raw, _ := json.Marshal(map[string]any{"id": "3f9c-weekly"})
		if got := rig.app.upcomingApprovalFor(name, raw); got == nil || got.Title != "Weekly update" || got.Item != "" {
			t.Fatalf("%s card subject = %+v", name, got)
		}
	}
	raw, _ := json.Marshal(map[string]any{"id": "brief:morning"})
	if got := rig.app.upcomingApprovalFor(upcomingToolRunNow, raw); got == nil || got.Item != "brief_morning" || got.Title != "" {
		t.Fatalf("brief card subject = %+v", got)
	}
	if got := rig.app.upcomingApprovalFor("Bash", raw); got != nil {
		t.Fatalf("another tool got a subject: %+v", got)
	}
	encoded, _ := json.Marshal(AgentEvent{Type: "permission_request", UpcomingApproval: &UpcomingApproval{Title: "Weekly update"}})
	if !strings.Contains(string(encoded), `"upcoming_approval":{"title":"Weekly update"}`) {
		t.Fatalf("event = %s", encoded)
	}
}

func TestUpcomingChangeRefusesManualWorkAndSaysAPausedBrief(t *testing.T) {
	rig := newUpcomingToolsRig(t, upcomingTestJob("asked", "Draft the report", "manual", `{}`, true))
	result := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "asked", "time": "09:00"})
	if !result.IsError || !strings.Contains(result.Content, "only when the user asks") {
		t.Fatalf("manual change = %+v", result)
	}
	if job, _ := rig.app.GetCronJob("asked"); job.ScheduleKind != "manual" {
		t.Fatalf("manual work became %s", job.ScheduleKind)
	}

	schedule, err := rig.brief.Schedule()
	if err != nil {
		t.Fatal(err)
	}
	schedule.Enabled = false
	if _, err := rig.brief.SetSchedule(schedule); err != nil {
		t.Fatal(err)
	}
	paused := callUpcomingTool(t, rig.app, upcomingToolChange, map[string]any{"id": "brief:evening", "time": "19:00"})
	if paused.IsError || !strings.Contains(paused.Content, "The brief is paused") {
		t.Fatalf("paused brief change = %+v", paused)
	}
}

func TestUpcomingRunNowWaitsBetweenRunsFromTheChat(t *testing.T) {
	rig := newUpcomingToolsRig(t, upcomingTestJob("cool", "Cooldown check", "cron", `{"expr":"0 9 * * 1"}`, true))
	if first := callUpcomingTool(t, rig.app, upcomingToolRunNow, map[string]any{"id": "cool"}); first.IsError || !strings.Contains(first.Content, "running now") {
		t.Fatalf("first run = %+v", first)
	}
	<-rig.runs
	second := callUpcomingTool(t, rig.app, upcomingToolRunNow, map[string]any{"id": "cool"})
	if !strings.Contains(second.Content, "started from this chat a few minutes ago") {
		t.Fatalf("second run = %+v", second)
	}
	select {
	case id := <-rig.runs:
		t.Fatalf("a second run started: %s", id)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCronInLocalTimeMovesTimeAndDays(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no zone data")
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, la) // PDT, UTC-7
	// 09:00 UTC on weekdays is 02:00 the same day in Los Angeles.
	got, err := cronInLocalTime(scheduleCron{Expr: "0 9 * * 1-5", TZ: "UTC"}, now)
	if err != nil || got.Expr != "0 2 * * 1-5" || got.TZ != "" {
		t.Fatalf("same-day move = %+v, %v", got, err)
	}
	// 03:00 UTC on Monday is 20:00 on Sunday in Los Angeles.
	got, err = cronInLocalTime(scheduleCron{Expr: "0 3 * * 1", TZ: "UTC"}, now)
	if err != nil || got.Expr != "0 20 * * 0" {
		t.Fatalf("previous-day move = %+v, %v", got, err)
	}
	if got, err := cronInLocalTime(scheduleCron{Expr: "0 9 * * *"}, now); err != nil || got.Expr != "0 9 * * *" {
		t.Fatalf("a local schedule changed: %+v, %v", got, err)
	}
	if _, err := cronInLocalTime(scheduleCron{Expr: "*/30 * * * *", TZ: "UTC"}, now); err == nil {
		t.Fatal("a many-times-a-day schedule in another zone was rewritten")
	}
}
