package storage

import (
	"context"
	"testing"
	"time"
)

// Settings › Your agents reads its choices on the read connection: while a
// background job holds the single writer connection, the read still returns
// the committed value (2026-11-21: the section stayed empty behind it).
func TestStreamPageUIStateDoesNotWaitForTheWriter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.SetUIState(ctx, "work_on_it_agent_code", "codex"); err != nil {
		t.Fatal(err)
	}
	writer, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	done := make(chan string, 1)
	go func() {
		value, err := store.GetStreamPageUIState(ctx, "work_on_it_agent_code")
		if err != nil {
			value = "error: " + err.Error()
		}
		done <- value
	}()
	select {
	case value := <-done:
		if value != "codex" {
			t.Fatalf("read with the writer held = %q, want codex", value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a UI state read waited for the writer connection")
	}
}
