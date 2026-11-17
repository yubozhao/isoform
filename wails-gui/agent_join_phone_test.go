package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The phone makes the join link and checks the agent's code on its own sheet
// (phone-agent v1.2.0 rule 9); when the agent answers, that phone alone is
// told once, within the link's ten minutes (rule 11); its Yes connects it.
func TestPhoneMakesTheJoinChecksTheCodeAndIsToldOnce(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	bridge, relay := mobileTestHub(t)
	app.mobileBridge = bridge
	bridge.app = app
	phone := newMobileTestPhone(t)
	mobileTestAddDevice(t, bridge, phone, []string{mobileScopeBriefRead, mobileScopeHandoff, mobileScopePush})
	relay.acceptRegistration(t, mobileTestListed(phone))

	start, joinID, err := app.startPhoneAgentJoin(phone.deviceID, "")
	if err != nil || start.Link == "" || start.ExpiresAt <= time.Now().UnixMilli() || joinID == "" {
		t.Fatalf("start = %+v %q %v", start, joinID, err)
	}
	if view, ok := app.phoneAgentJoinView(joinID); !ok || view.State != "waiting" || view.Code != "" {
		t.Fatalf("before the agent answers = %+v %v", view, ok)
	}
	if _, err := app.answerPhoneAgentJoin(joinID, true); !errors.Is(err, errPhoneJoinNotAsked) {
		t.Fatalf("Yes before the agent asks = %v", err)
	}
	if status, _, raw := agentJoinCall(t, http.MethodPost, start.Link, `{"name":"Claude Code","device":"this computer","mayOpen":"the repo"}`); status != http.StatusAccepted {
		t.Fatalf("the agent's answer: %d %s", status, raw)
	}
	view, ok := app.phoneAgentJoinView(joinID)
	if !ok || view.State != "requested" || !regexp.MustCompile(`^[0-9]{4}$`).MatchString(view.Code) ||
		view.AgentName != "Claude Code" || view.From != "this computer" || view.ToOpen != "the repo" {
		t.Fatalf("the code sheet = %+v %v", view, ok)
	}

	key := mobileTestNoticeKey(t, phone, bridge.identity.Public, bridge.identity.HubID)
	var told *mobileNotice
	for i := 0; i < 5 && told == nil; i++ {
		var frame mobileNoticeFrame
		if json.Unmarshal(relay.nextFrame(t, "out").raw, &frame) != nil || frame.Kind != mobileNoticeKindAgentJoin {
			continue
		}
		if !frame.Notify || frame.Level != "time-sensitive" || frame.Category != "YANSU_MOMENT" {
			t.Fatalf("the notice frame = %+v", frame)
		}
		opened, err := openMobileNotice(key, bridge.identity.HubID, phone.deviceID, frame.NoticeID, frame.CT)
		if err != nil {
			t.Fatal(err)
		}
		told = &opened
	}
	if told == nil || told.Title != "Claude Code is asking to join" || told.Body != "Check the code in Isoform." ||
		told.DestinationKind != mobileNoticeKindAgentJoin || told.DestinationID != joinID || told.ExpiresAt.Unix() != start.ExpiresAt/1000 {
		t.Fatalf("the phone was told %+v", told)
	}
	app.notePhoneAgentJoinChanged()
	relay.expectNoFrame(t, "out", 300*time.Millisecond)

	allowed, err := app.answerPhoneAgentJoin(joinID, true)
	if err != nil || allowed.State != "allowed" || allowed.AgentID == "" || allowed.Code != "" || len(app.GetConnectedAgents()) != 1 {
		t.Fatalf("Yes, it shows the code = %+v %v (agents %d)", allowed, err, len(app.GetConnectedAgents()))
	}
	if _, err := app.answerPhoneAgentJoin(joinID, false); !errors.Is(err, errPhoneJoinNotAsked) {
		t.Fatalf("a second answer = %v", err)
	}
	if _, err := app.answerPhoneAgentJoin("not-this-join", true); !errors.Is(err, errPhoneJoinUnknown) {
		t.Fatalf("an unknown join = %v", err)
	}
	// A join made on the computer since closes the phone's.
	if _, err := app.StartAgentJoin(); err != nil {
		t.Fatal(err)
	}
	if closed, _ := app.phoneAgentJoinView(joinID); closed.State != "expired" {
		t.Fatalf("after a newer join = %+v", closed)
	}
}

// A disconnected agent stays in Your agents for 30 days with when it was
// disconnected; Connect again is a new join that names it, and joining again
// clears it (phone-agent v1.2.0 §6).
func TestDisconnectedAgentsStayForConnectAgain(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	joinTestAgent(t, app, "Claude Code")
	joinTestAgent(t, app, "Ana's Helper")
	ids := map[string]string{}
	for _, c := range app.GetConnectedAgents() {
		ids[c.Name] = c.ID
	}
	for _, name := range []string{"Claude Code", "Ana's Helper"} {
		if err := app.DisconnectAgent(ids[name]); err != nil {
			t.Fatal(err)
		}
	}
	find := func(id string) (YourAgentView, bool) {
		for _, v := range app.yourAgents().Agents {
			if v.ID == id {
				return v, true
			}
		}
		return YourAgentView{}, false
	}
	if code, ok := find("claude-code"); !ok || code.Connected || code.DisconnectedAt == 0 {
		t.Fatalf("Claude Code after Disconnect = %+v %v", code, ok)
	}
	helper, ok := find(ids["Ana's Helper"])
	if !ok || helper.Reach != agentReachDisconnected || helper.Connected || helper.DisconnectedAt == 0 || helper.Name != "Ana's Helper" {
		t.Fatalf("the helper after Disconnect = %+v %v", helper, ok)
	}
	if !app.agentReconnectable(ids["Ana's Helper"]) || !app.agentReconnectable("claude-code") || app.agentReconnectable("nope") {
		t.Fatal("which agents Connect again may name")
	}

	start, joinID, err := app.startPhoneAgentJoin("device-1", ids["Ana's Helper"])
	if err != nil {
		t.Fatal(err)
	}
	if status, _, raw := agentJoinCall(t, http.MethodPost, start.Link, `{"name":"Ana's Helper","device":"her laptop","mayOpen":"the notes"}`); status != http.StatusAccepted {
		t.Fatalf("the agent's answer: %d %s", status, raw)
	}
	if _, err := app.answerPhoneAgentJoin(joinID, true); err != nil {
		t.Fatal(err)
	}
	if _, still := find(ids["Ana's Helper"]); still {
		t.Fatal("the helper is still listed as disconnected after joining again")
	}
	if code, _ := find("claude-code"); code.DisconnectedAt == 0 {
		t.Fatal("Claude Code's entry went with the helper's")
	}
	later := time.Now().Add(31 * 24 * time.Hour)
	app.agentJoin.now = func() time.Time { return later }
	if later := app.disconnectedAgents(); len(later) != 0 {
		t.Fatalf("after 30 days = %+v", later)
	}
}

func TestAgentJoinRouteFamilies(t *testing.T) {
	for path, want := range map[string]string{
		"GET /agents/join/j1":         "GET /agents/join/*",
		"POST /agents/join/j1/answer": "POST /agents/join/*/answer",
		"POST /agents/join/j1":        "",
	} {
		method, rest, _ := strings.Cut(path, " ")
		if got := mobileRouteFamily(method, rest); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}
