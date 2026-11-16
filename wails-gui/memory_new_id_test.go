package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Two notes saved in the same millisecond each keep their own file.
func TestNewNotesSavedTogetherKeepTheirOwnFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := mainChatTestApp(t)
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.WriteFile(filepath.Join(dir, "memory-"+itoa(now.UnixMilli())+".md"), []byte("# Taken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, second := newMemoryEntryID(dir, now), newMemoryEntryID(dir, now)
	if first == second || first == "memory-"+itoa(now.UnixMilli()) {
		t.Fatalf("ids = %q %q", first, second)
	}
	a, err := app.SaveMemoryEntry("", "# One\n- **Source**: user_edit\n\nOne.\n")
	if err != nil {
		t.Fatal(err)
	}
	b, err := app.SaveMemoryEntry("", "# Two\n- **Source**: user_edit\n\nTwo.\n")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("two notes share %q", a.ID)
	}
	for _, id := range []string{a.ID, b.ID} {
		if _, err := app.GetMemoryEntry(id); err != nil {
			t.Fatalf("note %s lost: %v", id, err)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
