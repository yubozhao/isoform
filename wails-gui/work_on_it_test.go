package main

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/workonit"
)

type workOnItSeams struct {
	opened []string
	copied []string
	sent   []string
	now    time.Time
}

func newWorkOnItTestApp(t *testing.T, enabled bool) (*App, *workOnItSeams) {
	t.Helper()
	app := newThreadTestApp(t)
	seams := &workOnItSeams{now: time.Now()}
	s := app.workOnIt()
	s.open = func(target string) error { seams.opened = append(seams.opened, target); return nil }
	s.copy = func(text string) error { seams.copied = append(seams.copied, text); return nil }
	s.send = func(_ context.Context, to, body string) error { seams.sent = append(seams.sent, to+"|"+body); return nil }
	s.present = func(agent workonit.Agent) bool { return agent.ID == "claude" }
	s.now = func() time.Time { return seams.now }
	// No computer-use agent unless a test sets one.
	s.paste = &pasteDeps{lookPath: pasteLookPath(), frontmost: func() string { return "" }, now: time.Now}
	if !enabled {
		if err := app.SetWorkOnItEnabled(false); err != nil {
			t.Fatal(err)
		}
	}
	return app, seams
}

func briefInput() workonit.Input {
	return workonit.Input{SourceKind: workonit.SourceBriefItem, SourceID: "b1", Title: "Draft the launch note", Detail: "For Thursday.", Done: "One page"}
}

// TestWorkOnItOnByDefaultUntilTurnedOff: Chat's and Today's hand-offs need
// it on for a new person; the switch still turns it off.
func TestWorkOnItOnByDefaultUntilTurnedOff(t *testing.T) {
	fresh := newThreadTestApp(t)
	if !fresh.GetWorkOnItEnabled() {
		t.Fatal("off for a new person")
	}
	app, _ := newWorkOnItTestApp(t, false)
	if app.GetWorkOnItEnabled() {
		t.Fatal("on after turning it off")
	}
	if _, err := app.PrepareWorkOnIt(briefInput()); err != errWorkOnItOff {
		t.Fatalf("prepare with the switch off: %v", err)
	}
	if _, err := app.SendWorkOnIt("x", "claude", workonit.MethodDeepLink); err != errWorkOnItOff {
		t.Fatalf("send with the switch off: %v", err)
	}
}

func TestWorkOnItClosedEyesStopsIt(t *testing.T) {
	app, seams := newWorkOnItTestApp(t, true)
	prepared, err := app.PrepareWorkOnIt(briefInput())
	if err != nil {
		t.Fatal(err)
	}
	app.closedEyes.enabled.Store(true)
	if _, err := app.PrepareWorkOnIt(briefInput()); err != errWorkOnItClosedEyes {
		t.Fatalf("prepare under Closed Eyes: %v", err)
	}
	if _, err := app.SendWorkOnIt(prepared.TaskID, "claude", workonit.MethodDeepLink); err != errWorkOnItClosedEyes {
		t.Fatalf("send under Closed Eyes: %v", err)
	}
	if len(seams.opened)+len(seams.copied)+len(seams.sent) != 0 {
		t.Fatal("something left under Closed Eyes")
	}
}

// TestWorkOnItHandoffs: the default is the installed agent with a deep link;
// the deep link carries exactly the text the sheet showed; opening an agent
// copies the text; a text agent sends only to the number set for it; each
// send keeps a count and never the text.
func TestWorkOnItHandoffs(t *testing.T) {
	app, seams := newWorkOnItTestApp(t, true)
	prepared, err := app.PrepareWorkOnIt(briefInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Agents) == 0 || prepared.Agents[0].ID != "claude" || !prepared.Agents[0].Default {
		t.Fatalf("agents = %+v, want Claude (installed) as the default", prepared.Agents)
	}
	var instinct *WorkOnItAgentView
	for i := range prepared.Agents {
		if prepared.Agents[i].ID == "instinct" {
			instinct = &prepared.Agents[i]
		}
	}
	if instinct == nil || !instinct.NeedsHandle || instinct.TermsNote == "" {
		t.Fatalf("instinct = %+v, want a terms note and a number to set", instinct)
	}

	sent, err := app.SendWorkOnIt(prepared.TaskID, "claude", workonit.MethodDeepLink)
	if err != nil || sent.Copied {
		t.Fatalf("deep link: %+v %v", sent, err)
	}
	link, _ := url.Parse(seams.opened[0])
	if link.Query().Get("q") != prepared.Text {
		t.Fatalf("deep link text differs from the shown text")
	}
	if _, err := app.SendWorkOnIt(prepared.TaskID, "claude", workonit.MethodDeepLink); err != errWorkOnItNoTask {
		t.Fatalf("a task was sent twice: %v", err)
	}

	second, _ := app.PrepareWorkOnIt(briefInput())
	copied, err := app.SendWorkOnIt(second.TaskID, "chatgpt", workonit.MethodOpenCopy)
	if err != nil || !copied.Copied || len(seams.copied) != 1 || seams.copied[0] != second.Text || seams.opened[1] != "https://chatgpt.com/" {
		t.Fatalf("open and copy: %+v %v opened=%v", copied, err, seams.opened)
	}

	third, _ := app.PrepareWorkOnIt(briefInput())
	if _, err := app.SendWorkOnIt(third.TaskID, "instinct", workonit.MethodMessage); err != errWorkOnItNoMethod {
		t.Fatalf("message without a number: %v", err)
	}
	if err := app.SetWorkOnItHandle("instinct", "+15550100"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SendWorkOnIt(third.TaskID, "instinct", workonit.MethodMessage); err != nil {
		t.Fatal(err)
	}
	if len(seams.sent) != 1 || seams.sent[0] != "+15550100|"+third.Text {
		t.Fatalf("message = %v", seams.sent)
	}

	counts, err := app.GetWorkOnItCounts()
	if err != nil || counts.Sent != 3 || counts.ByAgent["claude"] != 1 || counts.ByAgent["chatgpt"] != 1 || counts.ByAgent["instinct"] != 1 {
		t.Fatalf("counts = %+v %v", counts, err)
	}
	path, _ := workOnItRecordsPath()
	if data, _ := readFileString(path); strings.Contains(data, "launch note") {
		t.Fatal("the records keep task text")
	}

	// A day later Isoform asks once; the answer is counted.
	seams.now = seams.now.Add(25 * time.Hour)
	asks, err := app.GetWorkOnItAsks()
	if err != nil || len(asks) != 3 {
		t.Fatalf("asks = %d %v", len(asks), err)
	}
	if again, _ := app.GetWorkOnItAsks(); len(again) != 0 {
		t.Fatal("asked twice")
	}
	if err := app.MarkWorkOnIt(asks[0].ID, workonit.StatusDone); err != nil {
		t.Fatal(err)
	}
	counts, _ = app.GetWorkOnItCounts()
	if counts.Done != 1 || counts.Taken != 1 {
		t.Fatalf("after done: %+v", counts)
	}
}

// TestWorkOnItRanksFromCapture: prompts typed in an agent and results copied
// out of it rank it; a browser tab names its agent; other apps don't count.
func TestWorkOnItRanksFromCapture(t *testing.T) {
	app, seams := newWorkOnItTestApp(t, true)
	store := app.activity.store
	at := seams.now.Add(-time.Hour).UnixMilli()
	events := []activitymon.ActivityEvent{
		{Kind: activitymon.EventKeyboard, Timestamp: at, AppBundleID: "com.anthropic.claudefordesktop", AppName: "Claude"},
		{Kind: activitymon.EventKeyboard, Timestamp: at + 1, AppBundleID: "com.apple.Safari", AppName: "Safari", WindowTitle: "ChatGPT"},
		{Kind: activitymon.EventClipboard, Timestamp: at + 2, AppBundleID: "com.apple.Safari", AppName: "Safari", WindowTitle: "ChatGPT - Draft"},
		{Kind: activitymon.EventClipboard, Timestamp: at + 3, AppBundleID: "com.apple.Safari", AppName: "Safari", WindowTitle: "ChatGPT - Draft"},
		{Kind: activitymon.EventKeyboard, Timestamp: at + 4, AppBundleID: "com.apple.Notes", AppName: "Notes", WindowTitle: "Groceries"},
		{Kind: activitymon.EventFocus, Timestamp: at + 5, AppBundleID: "com.openai.chat", AppName: "ChatGPT"},
	}
	if err := store.SaveSession(ActivitySessionRow{ID: "s-agents", StartTime: at, EndTime: at + 10, AppNames: `["Claude","Safari"]`, TriageStatus: "done"}, events, nil); err != nil {
		t.Fatal(err)
	}
	usage, err := store.workOnItAgentCounts(context.Background(), at-1, seams.now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if usage["claude"] != (workonit.Usage{Prompts: 1}) || usage["chatgpt"] != (workonit.Usage{Prompts: 1, Copies: 2}) || len(usage) != 2 {
		t.Fatalf("usage = %+v", usage)
	}
	agents, err := app.GetWorkOnItAgents()
	if err != nil || agents[0].ID != "chatgpt" {
		t.Fatalf("agents = %+v %v, want ChatGPT first (results copied out of it)", agents, err)
	}
}

func readFileString(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
