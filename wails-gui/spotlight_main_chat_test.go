package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"

	"yansu-agent/agent"
	"yansu-agent/agent/tools"
)

func spotlightMainRig(t *testing.T) (*App, string, *upcomingCaptureRunner) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	rig := newUpcomingToolsRig(t)
	app := rig.app
	main, err := app.ensureMainChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	runner := &upcomingCaptureRunner{inputs: make(chan *agent.TaskInput, 4)}
	app.runner = runner
	app.tagCancels = map[string]context.CancelFunc{}
	app.sessionTempDirs = map[string]string{}
	app.streams = map[string]context.CancelFunc{}
	app.runIDByTag = map[string]string{}
	app.pendingTraceIDs = map[string]string{}
	app.spotlightStream = newSpotlightStreamHub()
	return app, main.LocalID, runner
}

// Quick chat answers in the one Chat thread, so Open in Chat finds the
// exchange already there (computer-chat v1.0.0 rules 10 and 11).
func TestQuickChatAsksInTheMainChatThread(t *testing.T) {
	app, tag, runner := spotlightMainRig(t)
	ctx := context.Background()
	if _, err := app.store.AppendStreamItem(ctx, tag, storage.StreamItemRow{ID: "older", Kind: "user", Content: "Yesterday's question"}); err != nil {
		t.Fatal(err)
	}
	got, err := app.startSpotlightSubmission("What did Sam and I agree on at lunch?", "", "", "req1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionLocalID != tag || got.UserItemID != "spotlight-u-req1" {
		t.Fatalf("response = %+v, want the main chat", got)
	}
	select {
	case input := <-runner.inputs:
		// A main-chat turn: it carries the main chat's own tool rules.
		prompt, _ := input.PromptFunc()
		if !strings.Contains(prompt, "<draft-tool>") {
			t.Fatal("the Quick chat turn is not a main-chat turn")
		}
		// The panel can't show Chat's cards: the turn gets none, and the
		// model points at Open in Chat.
		if !strings.Contains(prompt, "<quick-chat>") {
			t.Fatal("the Quick chat turn was not told where its cards are")
		}
		for _, tool := range input.ExtraTools {
			if quickChatCardTools[tool.Name()] {
				t.Fatalf("a Quick chat turn got %s", tool.Name())
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runner was not called")
	}
	waitForTagIdle(t, app, tag)
	row, err := app.store.LoadStreamItem(ctx, "spotlight-u-req1")
	if err != nil || row == nil || row.SessionLocalID != tag || row.Kind != "user" {
		t.Fatalf("user row = %+v %v", row, err)
	}
	// No separate Quick chat session was made.
	sessions, err := app.store.LoadSessionsByParent(ctx, "")
	if err == nil {
		for _, s := range sessions {
			if strings.HasPrefix(s.LocalID, "spotlight_") {
				t.Fatalf("a Quick chat session was made: %s", s.LocalID)
			}
		}
	}
	// The panel's snapshot is the exchange only, not the thread.
	app.spotlightTagsMu.Lock()
	state := app.spotlightTags[tag]
	app.spotlightTagsMu.Unlock()
	snapshot, err := app.spotlightSnapshot(ctx, tag, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) == 0 || snapshot.Items[0].ID != "spotlight-u-req1" {
		t.Fatalf("snapshot = %+v", snapshot.Items)
	}
	for _, item := range snapshot.Items {
		if item.ID == "older" {
			t.Fatal("the snapshot loaded the whole thread")
		}
	}
	// Edit happens in Chat; a retried request does not run twice.
	if err := app.editSpotlightSubmission("x", "", tag, "spotlight-u-req1", got.SortOrder); !errors.Is(err, errSpotlightMainEdit) {
		t.Fatalf("edit = %v", err)
	}
	if _, err := app.startSpotlightSubmission("again", "", "", "req1"); err == nil {
		t.Fatal("a request from before ran again")
	}
	// A follow-up goes to the same thread.
	if next, err := app.startSpotlightSubmission("And what about pricing?", "", tag, "req2"); err != nil || next.SessionLocalID != tag {
		t.Fatalf("follow-up = %+v %v", next, err)
	}
	<-runner.inputs
	waitForTagIdle(t, app, tag)
	deadline := time.Now().Add(5 * time.Second)
	for app.quickChatTurnActive(tag) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if app.quickChatTurnActive(tag) {
		t.Fatal("the Quick chat mark outlived its turn, so a later Chat turn would lose its cards")
	}
	if got := app.injectQuickChatContext(tag); got != "" {
		t.Fatal("a Chat turn after Quick chat was told it came from Quick chat")
	}
}

func TestQuickChatTurnsGetNoCardTools(t *testing.T) {
	list := []tools.Tool{namedTool("HandOffWork"), namedTool("DraftMessage"), namedTool("CiteSources"), namedTool("CorrectIsoform")}
	got := withoutQuickChatCards(list)
	if len(got) != 2 || got[0].Name() != "DraftMessage" || got[1].Name() != "CorrectIsoform" {
		t.Fatalf("kept %v", got)
	}
	if len(list) != 4 {
		t.Fatal("the turn's own tool list was changed")
	}
}

type namedTool string

func (n namedTool) Name() string               { return string(n) }
func (n namedTool) Description() string        { return "" }
func (n namedTool) JSONSchema() map[string]any { return nil }
func (n namedTool) NeedsPermission(map[string]any) (bool, tools.PermissionPrompt) {
	return false, tools.PermissionPrompt{}
}
func (n namedTool) Execute(context.Context, tools.ToolCall) (tools.ToolResult, error) {
	return tools.ToolResult{}, nil
}

func TestQuickChatWaitsWhileChatIsAnswering(t *testing.T) {
	app, tag, _ := spotlightMainRig(t)
	app.mu.Lock()
	app.activeTags[tag] = true
	app.mu.Unlock()
	if _, err := app.startSpotlightSubmission("Hi", "", "", "req3"); !errors.Is(err, errSpotlightChatBusy) {
		t.Fatalf("busy = %v", err)
	}
	if row, _ := app.store.LoadStreamItem(context.Background(), "spotlight-u-req3"); row != nil {
		t.Fatal("a busy send stored the message")
	}
}
