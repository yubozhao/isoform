package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"

	"yansu-agent/agent"
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
		// The panel shows Chat's cards: the turn keeps them, and nothing
		// sends work to Open in Chat any more (computer-chat v1.0.0 rule 11).
		if strings.Contains(prompt, "<quick-chat>") {
			t.Fatal("the Quick chat turn still points work at Open in Chat")
		}
		names := map[string]bool{}
		for _, tool := range input.ExtraTools {
			names[tool.Name()] = true
		}
		if !names["HandOffWork"] || !names["CiteSources"] {
			t.Fatalf("a Quick chat turn lost its cards: %v", names)
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
