package main

import (
	"net/http"
	"testing"
	"time"

	"wails-gui/internal/agentinbox"
	"wails-gui/internal/workonit"
)

// TestBriefAgentNewsSaysWhatCameBackAndWhatHasNoWord: results since the last
// brief, overnight ones marked; a hand-off with no word for two hours; and
// nothing at all on a day with no hand-offs.
func TestBriefAgentNewsSaysWhatCameBackAndWhatHasNoWord(t *testing.T) {
	app, _ := newAgentJoinTestApp(t)
	start := time.Now()
	if !app.briefAgentNews(start.Add(-time.Hour), time.Time{}).Empty() {
		t.Fatal("news on a day with no hand-offs")
	}
	token, base := joinTestAgent(t, app, "Ana's Helper")
	joined := app.GetConnectedAgents()[0]
	send := func() string {
		prepared, _ := app.PrepareWorkOnIt(briefInput())
		sent, err := app.SendWorkOnIt(prepared.TaskID, joined.ID, workonit.MethodConnected)
		if err != nil {
			t.Fatal(err)
		}
		return sent.RecordID
	}
	back := send()
	quiet := send()
	agentCall(t, http.MethodPost, base+agentinbox.ResultsPrefix+back, token, `{"status":"done","summary":"Venue B, and why."}`)

	news := app.briefAgentNews(start.Add(-time.Hour), start.Add(-time.Minute))
	if len(news.Results) != 1 || news.Results[0].Summary != "Venue B, and why." || news.Results[0].AgentName != "Ana's Helper" || !news.Results[0].Overnight {
		t.Fatalf("results = %+v", news.Results)
	}
	if len(news.NoWord) != 0 || len(news.Requests) != 0 {
		t.Fatalf("too early for no word: %+v", news)
	}
	if later := app.briefAgentNews(time.Now().Add(time.Hour), time.Time{}); len(later.Results) != 0 {
		t.Fatal("an old result counted for a later brief")
	}

	app.workOnIt().now = func() time.Time { return time.Now().Add(handoffNoWordAfter + time.Minute) }
	news = app.briefAgentNews(start.Add(-time.Hour), time.Time{})
	if len(news.NoWord) != 1 || news.NoWord[0].ID != quiet || news.Results[0].Overnight {
		t.Fatalf("no word = %+v", news)
	}
}
