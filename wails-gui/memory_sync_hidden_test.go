package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hidden memory file keeps no index row. Before the verdict cache, every
// interval sync saw it as new, counted it as indexed and emitted
// memory:updated, which re-read every page (the 5-minute Today read storm).
func TestMemorySyncHiddenFilesAreNotReindexedEverySync(t *testing.T) {
	store, err := NewMemoryStore(t.TempDir(), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	updates := 0
	store.SetOnUpdated(func() { updates++ })
	hidden := "---\n" + learnedAgentOriginLine + "\n---\n# Agent only\n\n- Learned from a coding agent.\n"
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(store.baseDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"agent-a.md", "agent-b.md", "agent-c.md"} {
		write(name, hidden+name)
	}
	write("control.md", "# Garden\n\n- Waters the orchard on Sundays.\n")

	if err := store.Sync("startup"); err != nil {
		t.Fatal(err)
	}
	if updates != 1 {
		t.Fatalf("first sync updates = %d, want 1 (the control file)", updates)
	}
	for range 3 {
		if err := store.Sync("interval"); err != nil {
			t.Fatal(err)
		}
	}
	if updates != 1 {
		t.Fatalf("unchanged syncs emitted %d more updates; hidden files are re-indexed every sync", updates-1)
	}

	// A file that becomes hidden leaves the index: that is a change.
	write("control.md", hidden+"control")
	if err := store.Sync("interval"); err != nil {
		t.Fatal(err)
	}
	if updates != 2 {
		t.Fatalf("hiding an indexed file: updates = %d, want 2", updates)
	}
	// A hidden file that becomes visible is indexed again.
	write("agent-a.md", "# Visible again\n\n- Waters the orchard.\n")
	if err := store.Sync("interval"); err != nil {
		t.Fatal(err)
	}
	if updates != 3 {
		t.Fatalf("unhiding a file: updates = %d, want 3", updates)
	}
	var paths []string
	rows, err := store.db.Query("SELECT path FROM memory_files")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		paths = append(paths, p)
	}
	if got := strings.Join(paths, ","); got != "agent-a.md" {
		t.Fatalf("indexed files = %q, want only agent-a.md", got)
	}
}

// A knowledge file that becomes hidden leaves the index once. The forget
// removal is scoped to memory notes, so a hidden knowledge file kept its old
// row and counted as changed on every sync (89 files, 2026-11-15).
func TestMemorySyncHiddenKnowledgeFileLeavesTheIndexOnce(t *testing.T) {
	store, err := NewMemoryStore(t.TempDir(), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := os.MkdirAll(store.knowledgeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	updates := 0
	store.SetOnUpdated(func() { updates++ })
	path := filepath.Join(store.knowledgeDir, "knowledge-history-a.md")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("# Release steps\n\n- Build, sign, notarize.\n")
	if err := store.Sync("startup"); err != nil || updates != 1 {
		t.Fatalf("indexing the knowledge file: updates=%d err=%v", updates, err)
	}
	write("---\n" + learnedAgentOriginLine + "\n---\n# Release steps\n\n- Build, sign, notarize.\n")
	if err := store.Sync("interval"); err != nil || updates != 2 {
		t.Fatalf("hiding the knowledge file: updates=%d err=%v", updates, err)
	}
	for range 3 {
		if err := store.Sync("interval"); err != nil {
			t.Fatal(err)
		}
	}
	if updates != 2 {
		t.Fatalf("a hidden knowledge file counted as changed on %d more syncs", updates-2)
	}
	var rows int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM memory_files WHERE path = ?", "knowledge-history-a.md").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("hidden knowledge file still indexed: rows=%d err=%v", rows, err)
	}
}
