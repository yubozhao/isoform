package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A new or edited memory file cannot withdraw anything. Only a removed file is
// reported as a withdrawal, and it names the source it was.
func TestMemoryScanOnlyRemovalsAreReportedAsWithdrawals(t *testing.T) {
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
	if changed, removed := s.detectFileChanges(); !changed || len(removed) != 0 {
		t.Fatalf("a new file: changed=%v removed=%v, want true none", changed, removed)
	}
	if changed, removed := s.detectFileChanges(); changed || len(removed) != 0 {
		t.Fatalf("no change: changed=%v removed=%v, want false none", changed, removed)
	}
	if err := os.WriteFile(first, []byte("invented note one, edited"), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, removed := s.detectFileChanges(); !changed || len(removed) != 0 {
		t.Fatalf("an edit: changed=%v removed=%v, want true none", changed, removed)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	changed, removed := s.detectFileChanges()
	if !changed || len(removed) != 1 || removed[0].ID != "second" {
		t.Fatalf("a removal: changed=%v removed=%v, want the one removed source", changed, removed)
	}
}

// A tombstone file write is storage only. The forgotten memory is withdrawn by
// the forget itself (recordMemoryTombstoneScoped), so the write alone advances nothing.
func TestTombstoneFileWriteAloneDoesNotAdvanceThePrivacyEpoch(t *testing.T) {
	dir := t.TempDir()
	before := mobileBriefPrivacyEpoch.Load()
	if err := persistMemoryTombstones(dir, nil); err != nil {
		t.Fatal(err)
	}
	if after := mobileBriefPrivacyEpoch.Load(); after != before {
		t.Fatalf("a tombstone file write moved the privacy epoch from %d to %d", before, after)
	}
}

// An edit made while the app was closed is found at startup, but it is not a
// withdrawal, so the sealed copies are not amended for it.
func TestMemoryWatchStartupDoesNotAmendForOfflineEdits(t *testing.T) {
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
	if mobileBriefPrivacyEpoch.Load() != before {
		t.Fatal("startup advanced the privacy epoch for a file that was not withdrawn")
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
