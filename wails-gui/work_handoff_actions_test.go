package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"wails-gui/internal/topicday"
	"wails-gui/internal/workonit"
)

const workTestPlanID = "brief:2026-11-14-morning:offer:o2"

// workTestConnectedYes records a Yes on the plan that went to a connected
// agent's inbox, as handOffToConnectedAgent leaves it.
func workTestConnectedYes(t *testing.T, app *App, recordID string) {
	t.Helper()
	app.taskLinks.dir, app.agentJoin.dir = t.TempDir(), t.TempDir()
	now := time.Now()
	if _, err := app.answersLedger().append(AnswerRecord{ItemID: workTestPlanID, Kind: answerKindWork, Source: workSourceBrief, Title: "Plan the Q4 outline",
		Action: workActionYes, At: mobileTimestamp(now), Surface: workSurfacePhone, HandoffID: recordID, AgentID: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	app.keepWorkOnItHandoff(workonit.Handoff{ID: recordID, SourceKind: workonit.SourceWorkItem, SourceID: workTestPlanID, AgentID: "claude-code",
		Method: workonit.MethodConnected, Via: "inbox", Text: "Plan.", SentAt: now.UnixMilli(), Surface: workSurfacePhone})
}

// A connected agent's result comes back to Your work with its files: text
// the phone may read, anything else only opened on the computer; where each
// lives is said (phone-agent v1.2.0 rules 3 and 4).
func TestWorkResultFromAConnectedAgentCarriesItsFiles(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	const recordID = "wo_result_files"
	workTestConnectedYes(t, app, recordID)
	ctx := context.Background()
	list, err := app.buildWorkList(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if item := workTestItem(t, list, workTestPlanID); item.State != workStateWithAgent || item.Ref.HandoffID != recordID || item.Agent == nil || item.Agent.Name != "Claude Code" {
		t.Fatalf("before the result = %+v", item)
	}

	dir, err := app.agentFilesDir(recordID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"outline.md": "# Q4", "run.sh": "echo hi", "big.txt": strings.Repeat("a", workFileReadMax+1)}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.rewriteTaskLinkResults(func(results []TaskLinkResult) []TaskLinkResult {
		return append(results, TaskLinkResult{LinkID: recordID, AgentID: "claude-code", Status: "done", Summary: "Outlined it.",
			Files: []string{"outline.md", "run.sh", "big.txt", "gone.md"}, ResultAt: time.Now().UnixMilli(), Via: "inbox"})
	}); err != nil {
		t.Fatal(err)
	}
	list, _ = app.buildWorkList(ctx, time.Now())
	back := workTestItem(t, list, workTestPlanID)
	if back.State != workStateBack || back.Kind != workKindResult || back.Work.Text != "Outlined it." || len(back.Files) != 3 || list.BackFromAgents != 1 {
		t.Fatalf("the result = %+v (back %d)", back, list.BackFromAgents)
	}
	byName := map[string]WorkFile{}
	for _, f := range back.Files {
		byName[f.Name] = f
		if f.Where != workFileWhere || f.ID != workFileID(recordID, f.Name) {
			t.Fatalf("file %+v", f)
		}
	}
	if !byName["outline.md"].Readable || byName["run.sh"].Readable || byName["big.txt"].Readable || byName["big.txt"].Bytes <= workFileReadMax {
		t.Fatalf("readable = %+v", byName)
	}

	// Read it on the phone: text only, 256 KB at most, only this item's files.
	bridge := &mobileBridge{app: app}
	device := mobileDeviceRecord{}
	device.Scopes = []string{mobileScopeBriefRead}
	read := func(item, file string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/work/"+item+"/file?file="+file, nil)
		w := httptest.NewRecorder()
		bridge.serveWorkFile(w, r, device, item)
		return w
	}
	got := read(workTestPlanID, byName["outline.md"].ID)
	var body struct {
		Data struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"data"`
	}
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &body) != nil || body.Data.Name != "outline.md" || body.Data.Text != "# Q4" {
		t.Fatalf("read = %d %s", got.Code, got.Body.String())
	}
	for _, c := range []struct {
		item, file string
		code       int
	}{
		{workTestPlanID, byName["run.sh"].ID, http.StatusUnsupportedMediaType},
		{workTestPlanID, byName["big.txt"].ID, http.StatusRequestEntityTooLarge},
		{workTestPlanID, workFileID(recordID, "gone.md"), http.StatusNotFound},
		{workTestPrepID, byName["outline.md"].ID, http.StatusNotFound},
		{"brief:nope", byName["outline.md"].ID, http.StatusNotFound},
	} {
		if got := read(c.item, c.file); got.Code != c.code {
			t.Errorf("read %s %s = %d, want %d", c.item, c.file, got.Code, c.code)
		}
	}

	// Open on your computer: text in TextEdit, anything else shown in Finder;
	// nothing is kept in the answers.
	var opened [][]string
	app.agentInbox.openFile = func(args []string) error { opened = append(opened, args); return nil }
	before := len(app.answersLedger().entries())
	for _, name := range []string{"outline.md", "run.sh"} {
		if _, err := app.AnswerWork(workTestPlanID, WorkAnswerInput{Action: workActionOpenFile, File: byName[name].ID}); err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
	}
	if len(opened) != 2 || opened[0][0] != "-e" || opened[1][0] != "-R" || filepath.Base(opened[1][1]) != "run.sh" {
		t.Fatalf("opened %v", opened)
	}
	if _, err := app.AnswerWork(workTestPlanID, WorkAnswerInput{Action: workActionOpenFile, File: "f_unknown"}); !errors.Is(err, errWorkFileUnknown) {
		t.Fatalf("an unknown file = %v", err)
	}
	if after := len(app.answersLedger().entries()); after != before {
		t.Fatalf("open_file kept %d entries", after-before)
	}
	// The result is judged as any other.
	if judged, err := app.AnswerWork(workTestPlanID, WorkAnswerInput{Action: workActionUseful}); err != nil || judged.State != workStateAnswered || len(judged.Files) != 3 {
		t.Fatalf("Useful = %+v %v", judged, err)
	}
}

// Take it back on work with a local agent: the task stops, the item is
// offered again at the top with who it was taken from, and Undo sends it to
// that agent again (phone-agent v1.2.1 rules 2 and 4). Ask again keeps it
// with the agent and keeps nothing.
func TestWorkTakeBackOffersItAgainAndUndoSendsItBack(t *testing.T) {
	app, handoffs := workAnswersTestApp(t)
	app.agentJoin.dir = t.TempDir()
	sent, err := app.AnswerWork(workTestPlanID, WorkAnswerInput{Action: workActionYes, AgentID: "codex"})
	if err != nil || sent.State != workStateWithAgent || sent.Ref.TaskID == "" || sent.Ref.HandoffID != sent.Ref.TaskID {
		t.Fatalf("Yes to Codex = %+v %v", sent, err)
	}
	taskID := sent.Ref.TaskID
	if kept, _ := app.GetWorkOnItHandoffs(); len(kept) != 1 || kept[0].ID != taskID || kept[0].TaskID != taskID {
		t.Fatalf("the hand-off doesn't carry its task: %+v", kept)
	}
	if _, err := handoffs.UpdateStatus(taskID, handoff.StatusInProgress); err != nil {
		t.Fatal(err)
	}

	before := len(app.answersLedger().entries())
	asked, err := app.AnswerWork(workTestPlanID, WorkAnswerInput{Action: workActionAskAgain})
	if err != nil || asked.State != workStateWithAgent {
		t.Fatalf("Ask again = %+v %v", asked, err)
	}
	if kept, _ := app.GetWorkOnItHandoffs(); kept[0].AskedAgainAt == 0 {
		t.Fatal("Ask again didn't start the clock over")
	}
	if after := len(app.answersLedger().entries()); after != before {
		t.Fatal("Ask again was kept as an answer")
	}

	taken, err := app.AnswerWork(workTestPlanID, WorkAnswerInput{Action: workActionTakeBack})
	if err != nil || taken.State != workStateOpen || taken.Kind != workKindOffered || taken.Answer != nil || taken.TakenBack == nil ||
		taken.TakenBack.AgentID != "codex" || taken.TakenBack.AgentName != "Codex" || taken.TakenBack.At == "" {
		t.Fatalf("Take it back = %+v %v", taken, err)
	}
	if entry, _ := handoffs.Get(taskID); entry.Status != handoff.StatusAborted {
		t.Fatalf("the task wasn't stopped: %s", entry.Status)
	}
	if status, _ := app.handoffStatusByID(taskID); status.State != handoffTakenBack {
		t.Fatalf("the Agents page says %q", status.State)
	}
	list, _ := app.buildWorkList(context.Background(), time.Now())
	if top := list.Items[0]; top.ID != workTestPlanID || top.TakenBack == nil {
		t.Fatalf("the list starts with %+v", top)
	}
	if _, err := app.AnswerWork(workTestPlanID, WorkAnswerInput{Action: workActionTakeBack}); !errors.Is(err, errWorkItemChanged) {
		t.Fatalf("twice = %v", err)
	}
	if _, err := app.AnswerWork(workTestPlanID, WorkAnswerInput{Action: workActionAskAgain}); !errors.Is(err, errWorkItemChanged) {
		t.Fatalf("Ask again on taken-back work = %v", err)
	}

	again, err := app.UndoWork(workTestPlanID)
	if err != nil || again.State != workStateWithAgent || again.TakenBack != nil || again.Ref.TaskID == "" || again.Ref.TaskID == taskID ||
		again.Agent == nil || again.Agent.Name != "Codex" {
		t.Fatalf("Undo of the take back = %+v %v", again, err)
	}
	if _, err := handoffs.Get(again.Ref.TaskID); err != nil {
		t.Fatalf("the work wasn't sent again: %v", err)
	}
	if standing := app.answersLedger().current()[workTestPlanID]; standing.Action != workActionYes || standing.TaskID != again.Ref.TaskID {
		t.Fatalf("the standing answer = %+v", standing)
	}
}

// A topic's proposal taken back from its agent goes back to proposed, so a
// later Yes starts it fresh.
func TestWorkTakeBackOnATopicOffersTheProposalAgain(t *testing.T) {
	app, handoffs := workAnswersTestApp(t)
	app.agentJoin.dir = t.TempDir()
	ctx := context.Background()
	if err := app.store.SetUIState(ctx, topicsDailyEnabledKey, "true"); err != nil {
		t.Fatal(err)
	}
	if err := app.buildDailyTopics(ctx); err != nil {
		t.Fatal(err)
	}
	view, _ := app.GetDailyTopics()
	tide := view.Topics[1]
	id := workTopicProposalID(tide.ID, tide.Proposal.ID)
	sent, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionYes})
	if err != nil || sent.State != workStateWithAgent || sent.Ref.TaskID == "" {
		t.Fatalf("Yes = %+v %v", sent, err)
	}
	if _, err := handoffs.UpdateStatus(sent.Ref.TaskID, handoff.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	taken, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionTakeBack})
	if err != nil || taken.State != workStateOpen || taken.TakenBack == nil {
		t.Fatalf("Take it back = %+v %v", taken, err)
	}
	if entry, _ := handoffs.Get(sent.Ref.TaskID); entry.Status != handoff.StatusAborted {
		t.Fatalf("the topic's task wasn't stopped: %s", entry.Status)
	}
	after, _ := app.GetDailyTopics()
	if p := after.Topics[1].Proposal; p.State != topicday.StateProposed || p.TaskID != "" {
		t.Fatalf("the proposal = %+v", p)
	}
	if again, err := app.AnswerWork(id, WorkAnswerInput{Action: workActionYes}); err != nil || again.State != workStateWithAgent || again.TakenBack != nil {
		t.Fatalf("Yes after the take back = %+v %v", again, err)
	}
}

// The phone's file read and the new actions are routed by their templates.
func TestWorkFileRouteFamily(t *testing.T) {
	if got := mobileRouteFamily(http.MethodGet, "/work/brief:b:offer:o1/file"); got != "GET /work/*/file" {
		t.Fatalf("family = %q", got)
	}
	if got := mobileRouteFamily(http.MethodPost, "/work/brief:b:offer:o1/file"); got != "" {
		t.Fatalf("a POST to the file = %q", got)
	}
}
