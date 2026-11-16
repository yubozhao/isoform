package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/workonit"
)

// The phone's Settings and Your data (phone-settings v1.0.0): the topics
// switch, a pause with no end, and what went to the person's agents.

func phoneSettingsFixture(t *testing.T) (*App, *mobileBridge) {
	t.Helper()
	app, _, _ := dailyTopicsTestApp(t)
	t.Setenv("YANSU_AGENT_HOME", t.TempDir())
	bridge := &mobileBridge{stateDir: t.TempDir(), app: app}
	app.mobileBridge = bridge
	return app, bridge
}

// Proposals from your topics is one setting: the phone turns it on and off,
// and the answer is the topics as the phone now reads them.
func TestThePhoneTurnsProposalsFromYourTopicsOnAndOff(t *testing.T) {
	app, bridge := phoneSettingsFixture(t)
	if got := watchDeskCall(bridge, http.MethodPost, "/topics-daily/enabled", `{"enabled":true}`, mobileScopeBriefRead); got.Code != http.StatusForbidden {
		t.Fatalf("the switch without offers:decide: %d", got.Code)
	}
	if got := watchDeskCall(bridge, http.MethodPost, "/topics-daily/enabled", `{}`, mobileScopeOffersDecide); got.Code != http.StatusBadRequest {
		t.Fatalf("no enabled: %d %s", got.Code, got.Body.String())
	}
	got := watchDeskCall(bridge, http.MethodPost, "/topics-daily/enabled", `{"enabled":true}`, mobileScopeOffersDecide)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"enabled":true`) {
		t.Fatalf("on: %d %s", got.Code, got.Body.String())
	}
	if view, _ := app.GetDailyTopics(); !view.Enabled {
		t.Fatal("the computer's setting is still off")
	}
	got = watchDeskCall(bridge, http.MethodPost, "/topics-daily/enabled", `{"enabled":false}`, mobileScopeOffersDecide)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"enabled":false`) {
		t.Fatalf("off: %d %s", got.Code, got.Body.String())
	}
	if view, _ := app.GetDailyTopics(); view.Enabled {
		t.Fatal("the computer's setting is still on")
	}
}

// "Until I end it": the phone pauses with no end and ends its own pause. A
// pause the computer made later is never taken for the phone's.
func TestThePhonePausesUntilItEndsItAndNeverEndsTheComputersLaterPause(t *testing.T) {
	app, bridge := phoneSettingsFixture(t)
	oldPause, oldResume := mobilePauseObserving, mobileResumeObserving
	t.Cleanup(func() {
		mobilePauseObserving, mobileResumeObserving = oldPause, oldResume
		mobilePhonePauseUntil.Store(0)
		mobilePhonePauseSince.Store(0)
	})
	clock := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	pauseAt := func(at time.Time) {
		app.closedEyes.enabled.Publish(true, time.Time{})
		app.recordTodayObservationPauseStart(true, at)
	}
	resume := func() {
		app.closedEyes.enabled.Publish(false, time.Time{})
		app.recordTodayObservationPauseStart(false, time.Time{})
	}
	var asked []int
	mobilePauseObserving = func(_ *App, minutes int) error {
		asked = append(asked, minutes)
		pauseAt(clock)
		return nil
	}
	mobileResumeObserving = func(*App) error { resume(); return nil }

	if got := watchDeskCall(bridge, http.MethodPost, "/observing/pause", `{"untilEnded":true,"minutes":60}`, mobileScopeChatWrite); got.Code != http.StatusBadRequest {
		t.Fatalf("minutes and untilEnded together: %d", got.Code)
	}
	got := watchDeskCall(bridge, http.MethodPost, "/observing/pause", `{"untilEnded":true}`, mobileScopeChatWrite)
	if got.Code != http.StatusOK || len(asked) != 1 || asked[0] != 0 || !strings.Contains(got.Body.String(), `"startedHere":true`) || strings.Contains(got.Body.String(), `"until"`) {
		t.Fatalf("a pause with no end: %d %s %v", got.Code, got.Body.String(), asked)
	}
	if got := watchDeskCall(bridge, http.MethodPost, "/observing/resume", `{}`, mobileScopeChatWrite); got.Code != http.StatusOK || app.IsClosedEyesModeEnabled() {
		t.Fatalf("the phone ends its own pause: %d %s", got.Code, got.Body.String())
	}

	// The phone pauses with no end; the person resumes at the computer and
	// pauses there again. That pause is the computer's.
	if got := watchDeskCall(bridge, http.MethodPost, "/observing/pause", `{"untilEnded":true}`, mobileScopeChatWrite); got.Code != http.StatusOK {
		t.Fatalf("second pause: %d", got.Code)
	}
	resume()
	pauseAt(clock.Add(time.Minute))
	got = watchDeskCall(bridge, http.MethodPost, "/observing/resume", `{}`, mobileScopeChatWrite)
	if got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "pause_not_from_phone") || !app.IsClosedEyesModeEnabled() {
		t.Fatalf("the computer's later pause was ended from the phone: %d %s", got.Code, got.Body.String())
	}
}

// Sent to your agents: what went, when, to whom, newest first, in pages.
func TestThePhoneReadsWhatWentToYourAgents(t *testing.T) {
	app, bridge := phoneSettingsFixture(t)
	now := time.Now()
	for i, h := range []workonit.Handoff{
		{ID: "r1", SourceKind: workonit.SourceTopicProposal, SourceID: "t1:p1", Kind: workonit.KindCode, AgentID: "claude-code",
			Method: workonit.MethodPaste, Text: "Write the launch checklist.", Surface: "phone"},
		{ID: "r2", SourceKind: workonit.SourceBriefItem, SourceID: "b:o1", Kind: workonit.KindElse, AgentID: "someone-new",
			Method: workonit.MethodTaskLink, Text: "Prepare for Lena.", Sent: "Prepare for Lena: https://example.invalid/t/1", Surface: "computer"},
	} {
		h.SentAt = now.Add(time.Duration(i) * time.Minute).UnixMilli()
		app.keepWorkOnItHandoff(h)
	}
	if got := watchDeskCall(bridge, http.MethodGet, "/agents/handoffs", "", mobileScopeBriefRead); got.Code != http.StatusForbidden {
		t.Fatalf("hand-offs without chat:read: %d", got.Code)
	}
	if got := watchDeskCall(bridge, http.MethodGet, "/agents/handoffs?limit=0", "", mobileScopeChatRead); got.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: %d", got.Code)
	}
	read := func(query string) mobileAgentHandoffsPage {
		t.Helper()
		got := watchDeskCall(bridge, http.MethodGet, "/agents/handoffs"+query, "", mobileScopeChatRead)
		var response struct {
			Data mobileAgentHandoffsPage `json:"data"`
		}
		if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &response) != nil {
			t.Fatalf("GET /agents/handoffs%s = %d %s", query, got.Code, got.Body.String())
		}
		return response.Data
	}
	first := read("?limit=1")
	if len(first.Handoffs) != 1 || first.Next != "r2" {
		t.Fatalf("first page = %+v", first)
	}
	newest := first.Handoffs[0]
	if newest.ID != "r2" || newest.AgentName != "someone-new" || newest.Sent == "" || newest.Surface != "computer" || newest.Source.ID != "b:o1" {
		t.Fatalf("newest = %+v", newest)
	}
	if _, err := time.Parse(time.RFC3339, newest.SentAt); err != nil {
		t.Fatalf("sentAt = %q", newest.SentAt)
	}
	rest := read("?before=r2")
	if len(rest.Handoffs) != 1 || rest.Next != "" || rest.Handoffs[0].AgentName != "Claude Code" || rest.Handoffs[0].Kind != workonit.KindCode {
		t.Fatalf("second page = %+v", rest)
	}
	// A hand-off forgotten on the computer is gone from the phone too.
	if err := app.ForgetWorkOnItHandoff("r1"); err != nil {
		t.Fatal(err)
	}
	if all := read(""); len(all.Handoffs) != 1 || all.Handoffs[0].ID != "r2" {
		t.Fatalf("after forgetting r1: %+v", all)
	}
}

// The status route says the computer takes all three.
func TestStatusAdvertisesThePhoneSettingsRoutes(t *testing.T) {
	_, bridge := phoneSettingsFixture(t)
	got := watchDeskCall(bridge, http.MethodGet, "/status", "", mobileScopeBriefRead)
	for _, field := range []string{`"observingPauseUntilEnded":true`, `"topicsDailySwitch":true`, `"agentHandoffs":true`} {
		if !strings.Contains(got.Body.String(), field) {
			t.Errorf("status lacks %s: %s", field, got.Body.String())
		}
	}
}
