package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A new memory file cannot withdraw anything, so the scan reports it as a
// change to index but not as a privacy change. An edit or a removal is both.
func TestMemoryScanOnlyEditsAndRemovalsAreReportedAsPrivacyChanges(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.md")
	second := filepath.Join(dir, "second.md")
	if err := os.WriteFile(first, []byte("invented note one"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &MemoryStore{baseDir: dir}
	s.detectFileChanges()

	if err := os.WriteFile(second, []byte("invented note two"), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, edited := s.detectFileChanges(); !changed || edited {
		t.Fatalf("a new file: changed=%v removedOrEdited=%v, want true false", changed, edited)
	}
	if changed, edited := s.detectFileChanges(); changed || edited {
		t.Fatalf("no change: changed=%v removedOrEdited=%v, want false false", changed, edited)
	}
	if err := os.WriteFile(first, []byte("invented note one, edited"), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, edited := s.detectFileChanges(); !changed || !edited {
		t.Fatalf("an edit: changed=%v removedOrEdited=%v, want true true", changed, edited)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	if changed, edited := s.detectFileChanges(); !changed || !edited {
		t.Fatalf("a removal: changed=%v removedOrEdited=%v, want true true", changed, edited)
	}
}

// A tombstone is a retraction in a file the watcher never reads, so writing it
// advances the privacy epoch itself.
func TestTombstoneWriteAdvancesThePrivacyEpoch(t *testing.T) {
	dir := t.TempDir()
	before := mobileBriefPrivacyEpoch.Load()
	if err := persistMemoryTombstones(dir, nil); err != nil {
		t.Fatal(err)
	}
	if after := mobileBriefPrivacyEpoch.Load(); after <= before {
		t.Fatalf("a tombstone write left the privacy epoch at %d", after)
	}
}

func TestMemoryWatchStartupChecksForOfflineEdits(t *testing.T) {
	s, err := NewMemoryStore(t.TempDir(), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := os.WriteFile(filepath.Join(s.baseDir, "note.md"), []byte("# Fictional note\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := mobileBriefPrivacyEpoch.Load()
	s.pollFileChanges(true)
	if mobileBriefPrivacyEpoch.Load() <= before {
		t.Fatal("startup did not invalidate copies after possible offline edits")
	}
}

func TestNewMemoryFileLeavesSealedWrittenBriefVisible(t *testing.T) {
	_, svc, source := newStoredMobileBriefPrivacyFixture(t)
	svc.wg.Wait()
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewMemoryStore(filepath.Dir(dir), MemoryStoreConfig{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.detectFileChanges()
	before, _, err := svc.finishedReadWithholding(source)
	if err != nil || !mobileBriefHasWrittenText(before) {
		t.Fatalf("fixture did not seal written text: %v", err)
	}
	epoch := mobileBriefPrivacyEpoch.Load()
	if err := os.WriteFile(filepath.Join(dir, "new-note.md"), []byte("# New fictional note\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store.pollFileChanges(false)
	if mobileBriefPrivacyEpoch.Load() != epoch {
		t.Fatal("adding a file advanced the privacy epoch")
	}
	after, withheld, err := svc.finishedReadWithholding(source)
	if err != nil || withheld.Reason != "" || !mobileBriefHasWrittenText(after) || after.Narrative[0].Text != before.Narrative[0].Text {
		t.Fatalf("new file changed the written brief: reason=%s error=%v", withheld.Reason, err)
	}
}
