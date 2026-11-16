package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func drainBrowserEventNames(t *testing.T, events <-chan []byte) []string {
	t.Helper()
	var names []string
	for {
		select {
		case raw := <-events:
			var event BrowserEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			names = append(names, event.Event)
		default:
			return names
		}
	}
}

func countName(names []string, want string) int {
	n := 0
	for _, name := range names {
		if name == want {
			n++
		}
	}
	return n
}

// A forget is a privacy action: the screens must clear every saved copy at
// once, so it sends memory:withdrawn before memory:updated.
func TestForgetSendsMemoryWithdrawnBeforeMemoryUpdated(t *testing.T) {
	app, dir := newForgetTestApp(t)
	app.ctx = context.Background()
	app.eventBroadcaster = NewEventBroadcaster()
	events := app.eventBroadcaster.AddClient("memory-withdrawn")
	t.Cleanup(func() { app.eventBroadcaster.RemoveClient("memory-withdrawn") })
	const id = "private-forget-event"
	content := "# Private\n\n- **Source**: activity\n- **Dimension**: user_insight\n\n## Memory Points\n\n- A private point.\n"
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.memoryStore.Sync("test"); err != nil {
		t.Fatal(err)
	}
	preview, err := app.GetMemoryForgetPreview(id)
	if err != nil {
		t.Fatal(err)
	}
	drainBrowserEventNames(t, events)
	if err := app.ForgetMemoryEntry(id, preview.ConfirmationToken); err != nil {
		t.Fatal(err)
	}
	names := drainBrowserEventNames(t, events)
	first := -1
	for i, name := range names {
		if name == memoryWithdrawnEvent || name == "memory:updated" {
			first = i
			break
		}
	}
	if first < 0 || names[first] != memoryWithdrawnEvent {
		t.Fatalf("forget events=%v, want memory:withdrawn before any memory:updated", names)
	}
	if countName(names, memoryWithdrawnEvent) == 0 || countName(names, memoryWithdrawnEvent) != countName(names, "memory:updated") {
		t.Fatalf("forget events=%v, want every memory:updated paired with memory:withdrawn", names)
	}
}

// An index pass or background learning is a content change only.
func TestRoutineMemoryUpdatesDoNotWithdraw(t *testing.T) {
	for _, reason := range []string{"memory-generator", "memory-generator-update", "activity-memory", "knowledge-relearned", "dream-archive", "translate"} {
		if memoryReasonWithdraws(reason) {
			t.Errorf("background reason %q withdraws; it must only mark copies stale", reason)
		}
	}
	for _, reason := range []string{"forget", "delete", "not_quite", "today_not_quite", "reassign", "agent-activity-hidden", "memory-duplicates-hidden", "a-reason-nobody-listed"} {
		if !memoryReasonWithdraws(reason) {
			t.Errorf("reason %q does not withdraw; a person's change (or an unknown one) must clear saved copies", reason)
		}
	}
	app := &App{ctx: context.Background(), eventBroadcaster: NewEventBroadcaster()}
	events := app.eventBroadcaster.AddClient("memory-routine")
	t.Cleanup(func() { app.eventBroadcaster.RemoveClient("memory-routine") })
	app.emitMemoryUpdated(false, map[string]string{"reason": "index"})
	if names := drainBrowserEventNames(t, events); countName(names, memoryWithdrawnEvent) != 0 || countName(names, "memory:updated") != 1 {
		t.Fatalf("index update events=%v, want one memory:updated and no memory:withdrawn", names)
	}
}
