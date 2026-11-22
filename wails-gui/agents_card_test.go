package main

import (
	"slices"
	"testing"
	"time"
)

// An agent's card says when it was connected: for one connected here, the
// one click's time; for a joined agent, when it joined.
func TestYourAgentConnectedAtHereAndJoined(t *testing.T) {
	app, _ := newAgentHereTestApp(t, true)
	app.agentJoin.dir = t.TempDir()
	before := time.Now().Add(-time.Second).UnixMilli()
	agents, err := app.ConnectAgentHere("codex")
	if err != nil {
		t.Fatal(err)
	}
	if v := agentView(t, agents, "codex"); v.ConnectedAt < before {
		t.Fatalf("codex here = %+v", v)
	}

	joined := newAgentJoinTestApp(t)
	joinTestAgent(t, joined, "Claude Code")
	joinTestAgent(t, joined, "Ana's Helper")
	byName := map[string]ConnectedAgentView{}
	for _, c := range joined.GetConnectedAgents() {
		byName[c.Name] = c
	}
	list := joined.yourAgents()
	if v := agentView(t, list, "claude-code"); v.ConnectedAt == 0 || v.ConnectedAt != byName["Claude Code"].JoinedAt {
		t.Fatalf("claude-code joined = %+v, joined at %d", v, byName["Claude Code"].JoinedAt)
	}
	if v := agentView(t, list, byName["Ana's Helper"].ID); v.ConnectedAt != byName["Ana's Helper"].JoinedAt {
		t.Fatalf("the helper = %+v", v)
	}
}

// An agent disconnected here fades for 30 days with Connect again, as a
// joined one does (computer-agents v1.1.0 13c rule 9).
func TestDisconnectedHereKeptThirtyDays(t *testing.T) {
	app, _ := newAgentHereTestApp(t, true)
	app.agentJoin.dir = t.TempDir()
	if _, err := app.ConnectAgentHere("codex"); err != nil {
		t.Fatal(err)
	}
	agents, err := app.DisconnectAgentHere("codex")
	if err != nil {
		t.Fatal(err)
	}
	if v := agentView(t, agents, "codex"); v.Connected || v.DisconnectedAt == 0 || v.ConnectedAt != 0 {
		t.Fatalf("after Disconnect = %+v", v)
	}
	if _, err := app.ConnectAgentHere("codex"); err != nil {
		t.Fatal(err)
	}
	if v := agentView(t, app.yourAgents(), "codex"); v.DisconnectedAt != 0 {
		t.Fatalf("connected again still says disconnected: %+v", v)
	}
	if _, err := app.DisconnectAgentHere("codex"); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(31 * 24 * time.Hour)
	app.agentJoin.now = func() time.Time { return later }
	if v := agentView(t, app.yourAgents(), "codex"); v.DisconnectedAt != 0 {
		t.Fatalf("after 30 days = %+v", v)
	}
}

// Open {agent} opens its app or site; a command-line agent opens its own
// sign-in in Terminal; an unknown one is an error.
func TestOpenAgentApp(t *testing.T) {
	app, seams := newWorkOnItTestApp(t, true)
	if err := app.OpenAgentApp("gemini"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(seams.opened, "https://gemini.google.com/app") {
		t.Fatalf("opened %v", seams.opened)
	}
	var script string
	prev := openSignInTerminal
	openSignInTerminal = func(s string) error { script = s; return nil }
	t.Cleanup(func() { openSignInTerminal = prev })
	if err := app.OpenAgentApp("claude-code"); err != nil || script == "" {
		t.Fatalf("claude-code: %v %q", err, script)
	}
	if err := app.OpenAgentApp("nope"); err == nil {
		t.Fatal("an unknown agent opened")
	}
	for _, v := range app.yourAgents().Agents {
		if (v.ID == "claude-code" || v.ID == "gemini") && !v.Opens {
			t.Fatalf("%s can't be opened: %+v", v.ID, v)
		}
	}
}

// This week's "{u} useful" comes from the person's own Useful on a result,
// matched to its hand-off; an undone Useful doesn't count.
func TestUsefulHandoffsFromJudgments(t *testing.T) {
	app, _ := workAnswersTestApp(t)
	ledger := app.answersLedger()
	now := mobileTimestamp(time.Now())
	add := func(r AnswerRecord) AnswerRecord {
		t.Helper()
		r.Kind, r.At, r.Surface = answerKindWork, now, workSurfacePhone
		saved, err := ledger.append(r)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	add(AnswerRecord{ItemID: "w1", Action: workActionYes, HandoffID: "h1"})
	add(AnswerRecord{ItemID: "w1", Action: workActionUseful})
	add(AnswerRecord{ItemID: "w2", Action: workActionYes, HandoffID: "h2"})
	add(AnswerRecord{ItemID: "w2", Action: workActionNotUseful})
	add(AnswerRecord{ItemID: "w3", Action: workActionYes, HandoffID: "h3"})
	undone := add(AnswerRecord{ItemID: "w3", Action: workActionUseful})
	add(AnswerRecord{ItemID: "w3", Action: workActionUndo, Undoes: undone.EntryID})
	got, err := app.GetUsefulHandoffs()
	if err != nil || !slices.Equal(got, []string{"h1"}) {
		t.Fatalf("useful = %v %v", got, err)
	}
}
