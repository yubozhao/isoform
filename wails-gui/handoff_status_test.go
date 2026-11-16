package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/agentinbox"
	"wails-gui/internal/workonit"
)

func statusOf(t *testing.T, app *App, id string) HandoffStatus {
	t.Helper()
	s, err := app.handoffStatusByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestHandoffStatusSaysOnlyWhatIsoformKnows: an app opened with the task is
// "opened" and never working; a connected agent goes sent, has it, working
// only with its own progress line, back; two hours of nothing is no word.
func TestHandoffStatusSaysOnlyWhatIsoformKnows(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	now := time.Now()
	app.workOnIt().now = func() time.Time { return now }
	s := app.workOnIt()
	s.open = func(string) error { return nil }

	prepared, _ := app.PrepareWorkOnIt(briefInput())
	opened, err := app.SendWorkOnIt(prepared.TaskID, "claude", workonit.MethodDeepLink)
	if err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, app, opened.RecordID); st.State != handoffOpened || st.AgentName != "Claude" || st.Title != "Draft the launch note" {
		t.Fatalf("opened in Claude = %+v", st)
	}

	token, base := joinTestAgent(t, app, "Ana's Helper")
	joined := app.GetConnectedAgents()[0]
	prepared, _ = app.PrepareWorkOnIt(briefInput())
	sent, err := app.SendWorkOnIt(prepared.TaskID, joined.ID, workonit.MethodConnected)
	if err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, app, sent.RecordID); st.State != handoffSent {
		t.Fatalf("sent = %+v", st)
	}
	agentCall(t, http.MethodGet, base+agentinbox.InboxPath, token, "")
	if st := statusOf(t, app, sent.RecordID); st.State != handoffHasIt {
		t.Fatalf("read = %+v", st)
	}
	if err := app.agentInboxService().Progress(joined.ID, sent.RecordID, "Comparing two venues."); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, app, sent.RecordID); st.State != handoffWorking || st.Line != "Comparing two venues." {
		t.Fatalf("progress = %+v", st)
	}

	// Two hours of nothing.
	now = now.Add(handoffNoWordAfter + time.Minute)
	if st := statusOf(t, app, opened.RecordID); st.State != handoffNoWord || st.Since == 0 {
		t.Fatalf("no word = %+v", st)
	}
	if st := statusOf(t, app, sent.RecordID); st.State != handoffNoWord {
		t.Fatalf("no word after progress = %+v", st)
	}

	// Ask again: the inbox shows it as new, the clock starts over.
	st, err := app.AskAgentAgain(sent.RecordID)
	if err != nil || st.State != handoffSent {
		t.Fatalf("ask again = %+v %v", st, err)
	}
	if item, _ := app.agentInboxService().Find(sent.RecordID); item.TakenAt != 0 || item.Progress != "" {
		t.Fatalf("not re-offered: %+v", item)
	}

	agentCall(t, http.MethodPost, base+agentinbox.ResultsPrefix+sent.RecordID, token, `{"status":"done","summary":"Venue B."}`)
	if st := statusOf(t, app, sent.RecordID); st.State != handoffBack || st.Line != "Venue B." {
		t.Fatalf("back = %+v", st)
	}
	if _, err := app.AskAgentAgain(sent.RecordID); err != errHandoffDone {
		t.Fatalf("ask again after back: %v", err)
	}
}

// TestTakeItBack: the agent's token can't read the hand-off any more, the
// record says not done, and it can't be taken back twice.
func TestTakeItBack(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	token, base := joinTestAgent(t, app, "Ana's Helper")
	joined := app.GetConnectedAgents()[0]
	prepared, _ := app.PrepareWorkOnIt(briefInput())
	sent, err := app.SendWorkOnIt(prepared.TaskID, joined.ID, workonit.MethodConnected)
	if err != nil {
		t.Fatal(err)
	}
	st, err := app.TakeHandoffBack(sent.RecordID)
	if err != nil || st.State != handoffTakenBack {
		t.Fatalf("take back = %+v %v", st, err)
	}
	if _, raw := agentCall(t, http.MethodGet, base+agentinbox.InboxPath, token, ""); strings.Contains(string(raw), sent.RecordID) {
		t.Fatal("the agent still reads a taken-back hand-off")
	}
	if status, _ := agentCall(t, http.MethodPost, base+agentinbox.ResultsPrefix+sent.RecordID, token, `{"status":"done","summary":"late"}`); status != http.StatusNotFound {
		t.Fatalf("a result for a taken-back hand-off: %d", status)
	}
	records, _ := app.workOnItRead()
	if len(records) != 1 || records[0].Status != workonit.StatusNotDone {
		t.Fatalf("records = %+v", records)
	}
	if _, err := app.TakeHandoffBack(sent.RecordID); err != errHandoffDone {
		t.Fatalf("twice: %v", err)
	}
	// After Disconnect the hand-off still says who it went to.
	if err := app.DisconnectAgent(joined.ID); err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, app, sent.RecordID); st.AgentName != "Ana's Helper" {
		t.Fatalf("name after Disconnect = %q", st.AgentName)
	}
	// It fades with Connect again for 30 days, holding no credential.
	view := agentView(t, app.yourAgents(), joined.ID)
	if view.Reach != agentReachDisconnected || view.Connected || view.DisconnectedAt == 0 || view.Name != "Ana's Helper" {
		t.Fatalf("disconnected view = %+v", view)
	}
	raw, _ := os.ReadFile(filepath.Join(app.agentJoin.dir, agentsDisconnectedFile))
	if strings.Contains(strings.ToLower(string(raw)), "token") || strings.Contains(string(raw), "Hash") {
		t.Fatalf("a credential was kept: %s", raw)
	}
	later := time.Now().Add(agentDisconnectedKeep + time.Hour)
	app.agentJoin.now = func() time.Time { return later }
	for _, v := range app.yourAgents().Agents {
		if v.ID == joined.ID {
			t.Fatal("still listed after 30 days")
		}
	}
}

// TestTakeBackStopsATaskHere: a hand-off running on Codex here is stopped.
func TestTakeBackStopsATaskHere(t *testing.T) {
	app, started := newAgentHereTestApp(t, true)
	if _, err := app.ConnectAgentHere("codex"); err != nil {
		t.Fatal(err)
	}
	in := briefInput()
	in.Kind = workonit.KindCode
	prepared, _ := app.PrepareWorkOnIt(in)
	sent, err := app.SendWorkOnIt(prepared.TaskID, "codex", workonit.MethodConnected)
	if err != nil || len(*started) != 1 {
		t.Fatalf("send: %v %v", err, *started)
	}
	st, err := app.TakeHandoffBack(sent.RecordID)
	if err != nil || st.State != handoffTakenBack {
		t.Fatalf("take back = %+v %v", st, err)
	}
}

// TestPhoneHandoffStatus: the phone reads the same states, and Ask again and
// Take it back are receipted, refused twice, and routed by their template.
func TestPhoneHandoffStatus(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	joinTestAgent(t, app, "Ana's Helper")
	joined := app.GetConnectedAgents()[0]
	prepared, _ := app.PrepareWorkOnIt(briefInput())
	sent, err := app.SendWorkOnIt(prepared.TaskID, joined.ID, workonit.MethodConnected)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &mobileBridge{app: app, ctx: t.Context()}
	device := mobileDeviceRecord{MobileDeviceView: MobileDeviceView{DeviceID: "phone-1"}}

	rec := httptest.NewRecorder()
	bridge.serveHandoffStatuses(rec)
	var read struct {
		Data struct {
			Handoffs []HandoffStatus `json:"handoffs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil || len(read.Data.Handoffs) != 1 || read.Data.Handoffs[0].State != handoffSent || read.Data.Handoffs[0].AgentName != "Ana's Helper" {
		t.Fatalf("status = %s", rec.Body.String())
	}

	act := func(action, key string) *httptest.ResponseRecorder {
		path := "/agents/handoffs/" + sent.RecordID + "/" + action
		if got := mobileRouteFamily(http.MethodPost, path); got != "POST /agents/handoffs/*/"+action {
			t.Fatalf("route family = %q", got)
		}
		body, _ := json.Marshal(map[string]string{"idempotencyKey": key})
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		bridge.serveHandoffAction(rec, req, device, sent.RecordID, action)
		return rec
	}
	if rec := act("ask-again", "k1"); rec.Code != http.StatusOK {
		t.Fatalf("ask again: %d %s", rec.Code, rec.Body.String())
	}
	if rec := act("take-back", "k2"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), handoffTakenBack) {
		t.Fatalf("take back: %d %s", rec.Code, rec.Body.String())
	}
	if rec := act("take-back", "k2"); rec.Code != http.StatusOK {
		t.Fatalf("a retried key isn't answered from its receipt: %d", rec.Code)
	}
	if rec := act("take-back", "k3"); rec.Code != http.StatusConflict {
		t.Fatalf("taken back twice: %d", rec.Code)
	}
	if rec := act("ask-again", "k4"); rec.Code != http.StatusConflict {
		t.Fatalf("asked again after take back: %d", rec.Code)
	}
}
