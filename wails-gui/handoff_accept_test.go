package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/internal/handoff"
	"yansu-agent/agent"
)

func TestStartAgainReopensABlockedRunOnTheSameChat(t *testing.T) {
	rig := newHandoffTestRig(t)
	sessionID, err := rig.app.createHandoffExecutionSession(handoff.Entry{Title: "The export"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.handoffs.Save(handoff.Entry{
		ID: "hf-blocked", Title: "The export", Status: handoff.StatusBlocked, BlockedReason: handoff.BlockedInterrupted,
		Source: handoff.SourceTriage, ChatSessionID: sessionID, Body: "Export the expenses.",
		Criteria: []string{"The export exists"},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := rig.app.AcceptHandoff("hf-blocked")
	if err != nil {
		t.Fatalf("start again on a blocked run: %v", err)
	}
	if result.SessionID != sessionID {
		t.Fatalf("start again did not reuse the chat: want session %q, got %+v", sessionID, result)
	}
	if result.Prompt != "Export the expenses." {
		t.Fatalf("start again must carry the brief for composition: %q", result.Prompt)
	}
	got, _ := rig.handoffs.Get("hf-blocked")
	if got.Status != handoff.StatusInProgress || got.BlockedReason != "" || got.ChatSessionID != sessionID {
		t.Fatalf("reopened entry = %+v", got)
	}
	if got.AttemptStartedAt == 0 {
		t.Fatalf("reopened entry has no attempt start: %+v", got)
	}
}

func TestStartAgainOnABlockedRunWithoutItsChatOpensANewOne(t *testing.T) {
	rig := newHandoffTestRig(t)
	if _, err := rig.handoffs.Save(handoff.Entry{
		ID: "hf-timeout", Title: "The export", Status: handoff.StatusBlocked, BlockedReason: handoff.BlockedTimeout,
		Source: handoff.SourceTriage, ChatSessionID: "deleted-chat", Body: "Export the expenses.",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := rig.app.AcceptHandoff("hf-timeout")
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID == "" || result.SessionID == "deleted-chat" {
		t.Fatalf("a new chat was not made: %+v", result)
	}
	got, _ := rig.handoffs.Get("hf-timeout")
	if got.Status != handoff.StatusInProgress || got.BlockedReason != "" || got.ChatSessionID != result.SessionID {
		t.Fatalf("entry = %+v", got)
	}
}

func TestStartAgainOnABlockedRunRestoresItWhenNoChatCanBeMade(t *testing.T) {
	store, err := handoff.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(handoff.Entry{
		ID: "hf-stuck", Title: "The export", Status: handoff.StatusBlocked, BlockedReason: handoff.BlockedTimeout,
		Source: handoff.SourceTriage, ChatSessionID: "old-chat", AttemptStartedAt: 1730037600000, Body: "Export the expenses.",
	}); err != nil {
		t.Fatal(err)
	}
	// Without the session store the chat cannot be made, so the claim rolls back.
	app := &App{handoffStore: store}

	if _, err := app.AcceptHandoff("hf-stuck"); err == nil {
		t.Fatal("the accept succeeded without a chat")
	}
	got, _ := store.Get("hf-stuck")
	if got.Status != handoff.StatusBlocked || got.BlockedReason != handoff.BlockedTimeout ||
		got.ChatSessionID != "old-chat" || got.AttemptStartedAt != 1730037600000 {
		t.Fatalf("rolled back entry = %+v", got)
	}
}

func TestAcceptHandoffStartsVerifiedAgencyWorkAndStartsItAgain(t *testing.T) {
	rig := newAgencyHandoffTestRig(t)
	result, err := rig.app.AcceptHandoff(rig.entry.ID)
	if err != nil {
		t.Fatalf("start verified Agency work: %v", err)
	}
	entry, err := rig.app.handoffStore.Get(rig.entry.ID)
	if err != nil || entry.Status != handoff.StatusInProgress || entry.ChatSessionID != result.SessionID {
		t.Fatalf("started Agency work = %+v err=%v", entry, err)
	}

	// An interrupted run keeps its verified plan, so Start again works on it.
	rig.app.markHandoffBlocked(entry.ID, handoff.BlockedInterrupted)
	again, err := rig.app.AcceptHandoff(entry.ID)
	if err != nil {
		t.Fatalf("start again on blocked Agency work: %v", err)
	}
	if again.SessionID != result.SessionID {
		t.Fatalf("start again did not reuse the chat: want %q, got %+v", result.SessionID, again)
	}
}

func TestAcceptHandoffRefusesRetiredAgencyWork(t *testing.T) {
	rig := newAgencyHandoffTestRig(t)
	if _, err := rig.app.handoffStore.UpdateStatus(rig.entry.ID, handoff.StatusStale); err != nil {
		t.Fatal(err)
	}

	if result, err := rig.app.AcceptHandoff(rig.entry.ID); err == nil ||
		!strings.Contains(err.Error(), "retired") {
		t.Fatalf("retired Agency work started: result=%+v err=%v", result, err)
	}
	got, _ := rig.app.handoffStore.Get(rig.entry.ID)
	if got.Status != handoff.StatusStale || got.ChatSessionID != "" {
		t.Fatalf("refused entry changed: %+v", got)
	}

	// Stale work from any other source still starts.
	if _, err := rig.app.handoffStore.Save(handoff.Entry{
		ID: "hf-aged", Title: "The outline", Status: handoff.StatusStale, Source: handoff.SourceTriage, Body: "Write the outline.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.app.AcceptHandoff("hf-aged"); err != nil {
		t.Fatalf("stale non-Agency work: %v", err)
	}
}

func TestAcceptHandoffRefusesAgencyWorkWhosePlanNoLongerVerifies(t *testing.T) {
	rig := newAgencyHandoffTestRig(t)
	entry, err := rig.app.handoffStore.Get(rig.entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry.SourceID = "different-source-run"
	if _, err := rig.app.handoffStore.Save(entry); err != nil {
		t.Fatal(err)
	}

	if result, err := rig.app.AcceptHandoff(rig.entry.ID); err == nil ||
		!strings.Contains(err.Error(), "no verified plan") {
		t.Fatalf("unverified Agency work started: result=%+v err=%v", result, err)
	}
	got, _ := rig.app.handoffStore.Get(rig.entry.ID)
	if got.Status != handoff.StatusActive || got.ChatSessionID != "" {
		t.Fatalf("refused entry changed: %+v", got)
	}
}

// --- One-time work boundary: the run gets the task, not the person ---

const (
	handoffPrivateSoul    = "PRIVATE_SOUL_SENTINEL"
	handoffPrivatePeople  = "PRIVATE_PEOPLE_SENTINEL"
	handoffPrivateFocused = "PRIVATE_FOCUSED_SENTINEL"
)

// seedHandoffBoundaryContext plants a persona, a contact and one focused-app
// stash per tag so any prompt the personal-context producers touch carries
// their sentinels — the way quick chat leaves a stash for a session's first
// turn.
func seedHandoffBoundaryContext(t *testing.T, app *App, tags ...string) {
	t.Helper()
	path, err := soulFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(handoffPrivateSoul), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CreateContact("Alice Example", handoffPrivatePeople, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		stashFocusedAppContext(tag, &focusedAppSnapshot{
			BundleID:     "com.example.editor",
			AppName:      "Editor",
			WindowTitle:  handoffPrivateFocused,
			Current:      &ActivitySnapshotRow{App: "Editor", Timestamp: time.Now().UnixMilli(), AXText: handoffPrivateFocused},
			CurrentFresh: true,
			CapturedAt:   time.Now(),
		})
	}
	t.Cleanup(func() {
		for _, tag := range tags {
			focusedAppStash.Delete(tag)
		}
	})
}

// wireHandoffPromptCapture arms a test app for the real chat send path with a
// runner that hands each task's input to the test instead of a model.
func wireHandoffPromptCapture(t *testing.T, app *App) *upcomingCaptureRunner {
	t.Helper()
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 1)}
	app.runner = runner
	app.activeTags = map[string]bool{}
	app.eventBroadcaster = NewEventBroadcaster()
	app.tagCancels = map[string]context.CancelFunc{}
	app.sessionTempDirs = map[string]string{}
	app.sessionFallbackDirs = map[string]struct{}{}
	app.streams = map[string]context.CancelFunc{}
	app.runIDByTag = map[string]string{}
	app.pendingTraceIDs = map[string]string{}
	return runner
}

// sendHandoffBoundaryPrompt sends one turn through the real chat send path and
// returns the composed prompt the run would ship to the model.
func sendHandoffBoundaryPrompt(t *testing.T, app *App, runner *upcomingCaptureRunner, prompt, workDir, tag string) string {
	t.Helper()
	if err := app.SendPromptWithAttachments(prompt, workDir, "m", "", tag, nil, nil, false, false, "", "", "", "en", ""); err != nil {
		t.Fatal(err)
	}
	var input *agent.TaskInput
	select {
	case input = <-runner.inputs:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not receive the turn")
	}
	composed, _ := input.PromptFunc()
	waitForTagIdle(t, app, tag)
	return composed
}

// waitForHandoffSettle lets the run's settle (needs_review on the capture
// runner's empty transcript) finish so it cannot race the temp stores.
func waitForHandoffSettle(t *testing.T, rig *handoffTestRig, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := rig.handoffs.Get(id)
		if err == nil && got.Status != handoff.StatusInProgress {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the run did not settle")
}

// The blocks a one-time work run must never carry (agent boundary audit,
// item 1): memory, people, recent activity, soul and the focused app.
var handoffSkippedBlocks = []string{
	handoffPrivateSoul,
	handoffPrivatePeople,
	handoffPrivateFocused,
	"<soul-context>",
	"<people-context>",
	"<memory-context>",
	"<initiative-context>",
	"<recent-activity-context>",
	"<activity-cli>",
	"<memory-cli>",
	"## Current application",
}

func TestHandoffRunGetsTheTaskNotThePerson(t *testing.T) {
	rig := newHandoffTestRig(t)
	workdir := t.TempDir()
	keyFile := filepath.Join(workdir, "expenses.csv")
	if _, err := rig.handoffs.Save(handoff.Entry{
		ID: "hf-boundary", Title: "The export", Status: handoff.StatusActive,
		Source: handoff.SourceTriage, Body: "Export the expenses for Alice Example.",
		Workdir: workdir, KeyFiles: []string{keyFile},
		Criteria: []string{"The export exists"},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := rig.app.AcceptHandoff("hf-boundary")
	if err != nil {
		t.Fatal(err)
	}
	seedHandoffBoundaryContext(t, rig.app, result.SessionID)
	runner := wireHandoffPromptCapture(t, rig.app)

	composed := sendHandoffBoundaryPrompt(t, rig.app, runner, result.Prompt, result.Workdir, result.SessionID)

	for _, want := range []string{
		"Export the expenses for Alice Example.",
		keyFile,
		workdir,
		`Success criteria, in order: ["The export exists"]`,
		handoffResultFence,
	} {
		if !strings.Contains(composed, want) {
			t.Errorf("handoff prompt lost %q", want)
		}
	}
	for _, skipped := range handoffSkippedBlocks {
		if strings.Contains(composed, skipped) {
			t.Errorf("handoff prompt carries personal context %q", skipped)
		}
	}
	waitForHandoffSettle(t, rig, "hf-boundary")
}

// The boundary is per session kind, not global: the main chat keeps the full
// personal bundle on the same app where the handoff run got none of it.
func TestMainChatKeepsTheFullContextBundle(t *testing.T) {
	rig := newHandoffTestRig(t)
	runner := wireHandoffPromptCapture(t, rig.app)
	main, err := rig.app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seedHandoffBoundaryContext(t, rig.app, main.LocalID)

	composed := sendHandoffBoundaryPrompt(t, rig.app, runner, "What do I know about Alice Example?", "", main.LocalID)

	for _, want := range []string{
		handoffPrivateSoul,
		handoffPrivatePeople,
		handoffPrivateFocused,
		"<soul-context>",
		"<people-context>",
		"## Current application",
	} {
		if !strings.Contains(composed, want) {
			t.Errorf("main chat lost %q from its context bundle", want)
		}
	}
	// Chat reads through its own tools and runs no commands (S25).
	if strings.Contains(composed, "<activity-cli>") || strings.Contains(composed, "<memory-cli>") {
		t.Error("main chat still carries a CLI manual")
	}
}
