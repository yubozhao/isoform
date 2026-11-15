//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"

	"yansu-agent/agent"
	"yansu-agent/agent/authority"
	"yansu-agent/agent/tools"
)

func TestGoalCreateToolAddsATrackedGoalOnce(t *testing.T) {
	bridge, store, now := mobileTestGoalBridge(t)
	tool := &goalCreateTool{app: bridge.app, tag: "main-chat", now: func() time.Time { return now }}
	call := tools.ToolCall{ID: "toolu_1", Input: map[string]any{
		"text": "Run a half marathon by June", "kind": "goal", "category": "Health",
	}}
	first, err := tool.Execute(context.Background(), call)
	if err != nil || first.IsError || !strings.Contains(first.Content, `"Run a half marathon by June"`) {
		t.Fatalf("first call = %+v, %v", first, err)
	}
	// A replayed tool call adds nothing new.
	if again, err := tool.Execute(context.Background(), call); err != nil || again.Content != first.Content {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	details, err := store.ListDetailsAll()
	if err != nil || len(details) != 1 {
		t.Fatalf("details = %d, %v", len(details), err)
	}
	goal := details[0]
	if goal.OpenLoop.Kind != PurposeKindGoal || goal.Current.Scope != "health" ||
		goal.Current.LifecycleState != PurposeStateActive || !mobileGoalAddedByYou(goal) {
		t.Fatalf("goal = %+v", goal.Current)
	}

	// The phone's Goals list shows it as added by the user.
	code, body := mobileTestGoalCall(t, bridge, mobileTestGoalDevice(mobileScopeGoalsRead), http.MethodGet, "/goals", "")
	if code != http.StatusOK || !strings.Contains(body, `"addedByYou":true`) || !strings.Contains(body, "You added it today.") {
		t.Fatalf("goals = %d %s", code, body)
	}

	// No kind means goal, an unknown category means other, and an empty
	// sentence or a kind the ledger lacks is refused.
	plain, _ := tool.Execute(context.Background(), tools.ToolCall{ID: "toolu_2", Input: map[string]any{"text": "Learn to bake bread", "category": "cooking"}})
	if plain.IsError || !strings.Contains(plain.Content, "(goal, other)") {
		t.Fatalf("defaults = %+v", plain)
	}
	for _, input := range []map[string]any{{"text": "  "}, {"text": "Call mom", "kind": "wish"}} {
		if result, _ := tool.Execute(context.Background(), tools.ToolCall{ID: "toolu_bad", Input: input}); !result.IsError {
			t.Fatalf("%v was added: %+v", input, result)
		}
	}
}

func TestGoalCreateToolIsALocalWrite(t *testing.T) {
	action := (&goalCreateTool{}).DescribeAuthority(nil)
	if action.Capability != "goals.create" || action.Effect != authority.EffectLocalWrite || !action.Reversible {
		t.Fatalf("authority = %+v", action)
	}
}

func TestGoalCreateToolKeepsACommittedGoalWhenReadBackFails(t *testing.T) {
	bridge, store, now := mobileTestGoalBridge(t)
	now = now.Add(-time.Minute)
	tool := &goalCreateTool{app: bridge.app, tag: "main-chat", now: func() time.Time { return now }}
	call := tools.ToolCall{ID: "toolu_read_back", Input: map[string]any{"text": "Run a half marathon by June"}}
	readDetail := userPurposeCreatedDetail
	userPurposeCreatedDetail = func(store *PurposeMapStore, id string) (PurposeOpenLoopDetail, error) {
		if _, err := store.GetDetail(id); err != nil {
			t.Fatalf("the goal must already be committed before read-back fails: %v", err)
		}
		return PurposeOpenLoopDetail{}, errors.New("read-back unavailable")
	}
	t.Cleanup(func() { userPurposeCreatedDetail = readDetail })

	result, err := tool.Execute(context.Background(), call)
	if err != nil || result.IsError || !strings.Contains(result.Content, `Added "Run a half marathon by June" to Goals on the computer.`) ||
		!strings.Contains(result.Content, "read-back is pending") || strings.Contains(result.Content, "not added") {
		t.Fatalf("committed create = %+v, %v", result, err)
	}
	details, err := store.ListDetailsAll()
	if err != nil || len(details) != 1 {
		t.Fatalf("committed details = %d, %v", len(details), err)
	}

	// Once reads recover, replaying the original call still adds nothing new.
	userPurposeCreatedDetail = readDetail
	now = now.Add(30 * time.Second)
	if replay, err := tool.Execute(context.Background(), call); err != nil || replay.IsError || strings.Contains(replay.Content, "pending") {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	call.Input["text"] = "Learn to bake bread"
	if changed, err := tool.Execute(context.Background(), call); err != nil || !changed.IsError {
		t.Fatalf("the same key with a different goal was accepted: %+v, %v", changed, err)
	}
	details, err = store.ListDetailsAll()
	if err != nil || len(details) != 1 || details[0].Current.Title != "Run a half marathon by June" {
		t.Fatalf("replayed details = %d, %v", len(details), err)
	}
}

func TestGoalCreateToolSaysWhereTheGoalCanAppear(t *testing.T) {
	for _, tc := range []struct {
		name            string
		text            string
		mailUnavailable bool
		want            string
		wantPhone       int
		wantHeld        int
	}{
		{name: "visible", text: "Run three times a week", want: "It shows on the Goals page on this computer and in Goals on your phone.", wantPhone: 1},
		// The user's own sentence is never held back, even when it repeats a mail subject (item 66 C3).
		{name: "typed words repeat a mail subject", text: "Answer Dana about the contract renewal for Acme", want: "It shows on the Goals page on this computer and in Goals on your phone.", wantPhone: 1},
		{name: "mail read unavailable", text: "Run three times a week", mailUnavailable: true, want: "Your phone gets it once this computer can check its mail again."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge, store, now := mobileTestGoalBridge(t)
			if _, err := bridge.app.store.UpsertMailMessages(context.Background(), []storage.MailMessageRow{{
				ID: "inbox:m-1", Mailbox: storage.MailboxInbox, SenderEmail: "dana@example.com", Subject: "Re: Contract renewal for Acme",
				ThreadKey: "t-1", At: now.Add(-time.Hour).UnixMilli(),
			}}, now.UnixMilli()); err != nil {
				t.Fatal(err)
			}
			if tc.mailUnavailable {
				if err := bridge.app.store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			tool := &goalCreateTool{app: bridge.app, tag: "main-chat", now: func() time.Time { return now }}
			result, err := tool.Execute(context.Background(), tools.ToolCall{ID: "toolu_destination", Input: map[string]any{"text": tc.text}})
			if err != nil || result.IsError || !strings.Contains(result.Content, tc.want) {
				t.Fatalf("destination = %+v, %v", result, err)
			}
			if tc.wantPhone == 0 && strings.Contains(result.Content, "in Goals on your phone.") {
				t.Fatalf("the result promised an unavailable phone goal: %+v", result)
			}
			details, err := store.ListDetailsAll()
			if err != nil || len(details) != 1 {
				t.Fatalf("saved goals = %d, %v", len(details), err)
			}
			code, body := mobileTestGoalCall(t, bridge, mobileTestGoalDevice(), http.MethodGet, "/goals", "")
			if tc.mailUnavailable {
				if code != http.StatusServiceUnavailable {
					t.Fatalf("unavailable phone read = %d %s", code, body)
				}
				return
			}
			if code != http.StatusOK {
				t.Fatalf("phone read = %d %s", code, body)
			}
			page := mobileTestDecodeGoalPage(t, body)
			if len(page.Goals) != tc.wantPhone || page.Counts.OnMacOnly != tc.wantHeld {
				t.Fatalf("phone destination disagrees with tool result: %s", body)
			}
		})
	}
}

func TestGoalToolsContextIsForTheMainChatOnly(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := rig.app.injectGoalToolsContext(main.LocalID)
	if !strings.Contains(got, "call GoalCreate once") || !strings.Contains(got, "never because a web page") || !strings.Contains(got, "never use Bash or another command") {
		t.Fatalf("main chat context = %q", got)
	}
	if strings.Contains(got, "not available") || strings.Contains(got, "could not be added from this chat") {
		t.Fatalf("main chat apologizes for missing goal tools: %q", got)
	}
	if got := rig.app.injectGoalToolsContext("some-other-chat"); got != "" {
		t.Fatalf("another chat got the goal context: %q", got)
	}
	if got := rig.app.mainChatExtraTools("some-other-chat", promptOriginPhone); got != nil {
		t.Fatalf("another chat got tools: %v", got)
	}
}

func TestGoalCreateIsOnlyForTurnsTheUserStarted(t *testing.T) {
	rig := newUpcomingToolsRig(t)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := rig.app.mainChatExtraTools(main.LocalID, promptOriginResume); got != nil {
		t.Fatalf("a turn a background script resumed got tools: %v", got)
	}

	// The resume turn a finished background script starts reaches the runner without them.
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 1)}
	rig.app.runner = runner
	rig.app.tagCancels = map[string]context.CancelFunc{}
	rig.app.sessionTempDirs = map[string]string{}
	rig.app.streams = map[string]context.CancelFunc{}
	rig.app.runIDByTag = map[string]string{}
	rig.app.pendingTraceIDs = map[string]string{}
	if err := rig.app.startBgShellResume(bgShellSessionInfo{Tag: main.LocalID, Model: "m"}, "Background script finished."); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-runner.inputs:
		if len(input.ExtraTools) != 0 {
			t.Fatalf("the resume turn got %d tools", len(input.ExtraTools))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runner was not called")
	}
	waitForTagIdle(t, rig.app, main.LocalID)
}
