package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A memory marked as learned only from agent activity is hidden from every
// page. External assistants (MCP read) and memory search (chat tools,
// scheduled prompts) must not serve it either.
func TestAgentLearnedMemoryHiddenFromMCPReadAndSearch(t *testing.T) {
	a := newMCPReadTestApp(t)
	control, err := a.SaveMemoryEntry("control", "# Orchard control\n\n- Orchard public release notes are allowed.\n")
	if err != nil {
		t.Fatal(err)
	}
	hidden, ok := markLearnedFromAgents(buildOKFMemoryMarkdown("agent-only", "Orchard agent run", time.Now(), "activity", "work_pattern", []string{"Orchard agent-only detail."}))
	if !ok {
		t.Fatal("fixture not marked")
	}
	writeMCPReadMarkdown(t, "agent-only", hidden)

	for _, tool := range []string{"memory_list", "memory_search"} {
		args := map[string]any{}
		if tool == "memory_search" {
			args["query"] = "orchard"
		}
		got := callMCPReadTest(t, a, tool, args)
		for _, item := range got.Items {
			if item.ID.Text == "agent-only" {
				t.Fatalf("%s served an agent-learned memory: %+v", tool, got)
			}
		}
		if len(got.Items) != 1 || got.Items[0].ID.Text != control.ID {
			t.Fatalf("%s lost the control memory: %+v", tool, got)
		}
	}

	store, err := NewMemoryStore(t.TempDir(), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	files := map[string]string{"agent-only.md": hidden, "control.md": control.Content}
	// Unrelated notes give the keyword scores a corpus to clear the
	// minimum-score filter against.
	for i := range 20 {
		files[fmt.Sprintf("unrelated-%02d.md", i)] = "# Cooking\n\n- Roast vegetables for dinner.\n"
	}
	if err := os.MkdirAll(store.baseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(store.baseDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Sync("test"); err != nil {
		t.Fatal(err)
	}
	results, err := store.Search(context.Background(), "orchard", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range results {
		if filepath.Base(r.Path) == "agent-only.md" {
			t.Fatalf("memory search indexed an agent-learned memory: %+v", r)
		}
		if filepath.Base(r.Path) == "control.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("memory search lost the control memory: %+v", results)
	}
}
