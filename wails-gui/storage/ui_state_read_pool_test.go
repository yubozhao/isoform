package storage

import (
	"context"
	"testing"
	"time"
)

// UI state reads run on the read pool: while a background job holds the
// single writer connection, GetUIState still returns the committed value
// (2026-11-21: Settings › Your agents stayed empty behind it).
func TestGetUIStateDoesNotWaitForTheWriter(t *testing.T) {
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
	for name, read := range map[string]func(context.Context, string) (string, error){
		"GetUIState":           store.GetUIState,
		"GetStreamPageUIState": store.GetStreamPageUIState,
	} {
		done := make(chan string, 1)
		go func() {
			value, err := read(ctx, "work_on_it_agent_code")
			if err != nil {
				value = "error: " + err.Error()
			}
			done <- value
		}()
		select {
		case value := <-done:
			if value != "codex" {
				t.Fatalf("%s with the writer held = %q, want codex", name, value)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s waited for the writer connection", name)
		}
	}
}

// A value set on the writer is read back at once on the read pool, and an
// absent key is still "".
func TestGetUIStateReadsItsOwnWrite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, value := range []string{"codex", "claude-code", ""} {
		if err := store.SetUIState(ctx, "work_on_it_agent_code", value); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetUIState(ctx, "work_on_it_agent_code")
		if err != nil || got != value {
			t.Fatalf("after SetUIState(%q): %q, %v", value, got, err)
		}
	}
	if got, err := store.ReserveUIStateValue(ctx, "reserved", "first"); err != nil || got != "first" {
		t.Fatalf("reserve: %q, %v", got, err)
	}
	if got, err := store.GetUIState(ctx, "missing"); err != nil || got != "" {
		t.Fatalf("missing key: %q, %v", got, err)
	}
}

// ISOFORM_UISTATE_ON_WRITER=1 puts the read back on the writer: with the
// writer held, the read waits until its context ends.
func TestGetUIStateRollbackSwitchUsesTheWriter(t *testing.T) {
	store := newTestStore(t)
	prev := UIStateOnWriter
	UIStateOnWriter = true
	t.Cleanup(func() { UIStateOnWriter = prev })
	writer, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := store.GetUIState(ctx, "work_on_it_agent_code"); err == nil {
		t.Fatal("the rollback switch read did not wait for the writer")
	}
}
