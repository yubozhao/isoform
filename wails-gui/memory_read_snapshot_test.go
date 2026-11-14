package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// P0 round 2: GetMemoryEntries read and parsed every note and listed
// .history/<id> for each one on every call (about 4500 file operations per
// call at 2243 notes), and brief, Today, chat and About You poll it. A read
// with nothing changed must not touch the disk.

const (
	fictionalMemoryNotes   = 2000
	fictionalMemoryHistory = 14
)

// countingMemoryFiles counts what the memory read path opens (folders and
// notes) and stats.
type countingMemoryFiles struct {
	opens atomic.Int64
	stats atomic.Int64
}

func (c *countingMemoryFiles) ReadDir(name string) ([]os.DirEntry, error) {
	c.opens.Add(1)
	entries, err := os.ReadDir(name)
	for i, entry := range entries {
		entries[i] = countingDirEntry{DirEntry: entry, stats: &c.stats}
	}
	return entries, err
}

func (c *countingMemoryFiles) ReadFile(name string) ([]byte, error) {
	c.opens.Add(1)
	return os.ReadFile(name)
}

func (c *countingMemoryFiles) take() (opens, stats int64) {
	return c.opens.Swap(0), c.stats.Swap(0)
}

type countingDirEntry struct {
	os.DirEntry
	stats *atomic.Int64
}

func (e countingDirEntry) Info() (os.FileInfo, error) {
	e.stats.Add(1)
	return e.DirEntry.Info()
}

func countMemoryFiles(t testing.TB) *countingMemoryFiles {
	t.Helper()
	counter := &countingMemoryFiles{}
	previous := memoryFiles
	memoryFiles = counter
	t.Cleanup(func() { memoryFiles = previous })
	return counter
}

// fakeMemoryReadClock lets a test move past memoryFolderCheckEvery.
func fakeMemoryReadClock(t testing.TB) func(time.Duration) {
	t.Helper()
	var offset atomic.Int64
	previous := memoryReadNow
	memoryReadNow = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	t.Cleanup(func() { memoryReadNow = previous })
	return func(d time.Duration) { offset.Add(int64(d)) }
}

// writeFictionalMemoryFolder writes notes at rest: n notes, the first
// fictionalMemoryHistory of every hundred with one earlier version.
func writeFictionalMemoryFolder(t testing.TB, n int) string {
	t.Helper()
	dir, err := memoryDir()
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("---\nsource: activity\nupdated: %d\napps:\n  - Notes\n---\n# Harbor note %d\n\nRowan Vale planned the Harbor Lane walk for week %d.\n", 1790000000000+int64(i), i, i%52)
		path := filepath.Join(dir, fmt.Sprintf("harbor-note-%04d.md", i))
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < fictionalMemoryHistory && i*100 < n; i++ {
		folder := filepath.Join(dir, memoryHistoryDirName, fmt.Sprintf("harbor-note-%04d", i*100))
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "1789000000000.md"), []byte("# Harbor note, before\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(folder, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func historyCount(entries []MemoryEntry) int {
	n := 0
	for _, entry := range entries {
		if entry.HasHistory {
			n++
		}
	}
	return n
}

func TestRepeatedMemoryReadsTouchNoFilesWhileUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := writeFictionalMemoryFolder(t, fictionalMemoryNotes)
	advance := fakeMemoryReadClock(t)
	files := countMemoryFiles(t)
	loads := 0
	previous := memoryExternalReadLoad
	memoryExternalReadLoad = func(kind string) {
		if kind == "entries" {
			loads++
		}
	}
	t.Cleanup(func() { memoryExternalReadLoad = previous })
	app := &App{}

	first, err := app.GetMemoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	opens, stats := files.take()
	t.Logf("first read of %d notes: %d file opens, %d stats", len(first), opens, stats)
	if len(first) != fictionalMemoryNotes || historyCount(first) != fictionalMemoryHistory {
		t.Fatalf("first read: %d notes, %d with history", len(first), historyCount(first))
	}
	// Fingerprint (folder, .history), folder, notes, then one listing of
	// .history and of each of its folders, not one per note.
	if want := int64(2 + 1 + fictionalMemoryNotes + 1 + fictionalMemoryHistory); opens != want {
		t.Fatalf("first read opened %d files and folders, want %d", opens, want)
	}

	const repeats = 100
	for i := 0; i < repeats; i++ {
		got, err := app.GetMemoryEntries()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && !reflect.DeepEqual(got, first) {
			t.Fatal("a repeated read differs from the first")
		}
	}
	opens, stats = files.take()
	t.Logf("%d repeated reads, nothing changed: %d file opens, %d stats", repeats, opens, stats)
	if opens != 0 || stats != 0 || loads != 1 {
		t.Fatalf("%d repeated reads: %d opens, %d stats, %d folder reads; want 0, 0, 1", repeats, opens, stats, loads)
	}

	// Callers get copies.
	mine, _ := app.GetMemoryEntries()
	mine[0].Content, mine[0].Apps[0] = "edited", "Edited"
	mine[1].TopicIDs = append(mine[1].TopicIDs, "edited")
	if again, _ := app.GetMemoryEntries(); !reflect.DeepEqual(again, first) {
		t.Fatal("a caller's edit leaked into the shared copy")
	}

	// The outside-edit check costs stats and two folder listings every
	// memoryFolderCheckEvery, and no note is read again while nothing changed.
	advance(memoryFolderCheckEvery)
	if _, err := app.GetMemoryEntries(); err != nil {
		t.Fatal(err)
	}
	opens, stats = files.take()
	t.Logf("outside-edit check: %d opens, %d stats", opens, stats)
	if opens != 2 || stats != fictionalMemoryNotes+fictionalMemoryHistory || loads != 1 {
		t.Fatalf("outside-edit check: %d opens, %d stats, %d folder reads", opens, stats, loads)
	}

	// An app write is read at once, without waiting for the check.
	if _, err := app.SaveMemoryEntry("harbor-note-0001", "# Harbor note 1\n\nRowan Vale moved the Harbor Lane walk to Friday.\n"); err != nil {
		t.Fatal(err)
	}
	got, err := app.GetMemoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	if entry := memoryEntryByID(got, "harbor-note-0001"); entry == nil || !strings.Contains(entry.Content, "Rowan Vale moved the Harbor Lane walk to Friday.") || !entry.HasHistory {
		t.Fatalf("app write not read at once: %+v", entry)
	}

	// An edit made outside the app, and an outside history version, are read
	// by the next check.
	advance(memoryFolderSettle)
	_, _ = app.GetMemoryEntries()
	outside := "# Harbor note 7\n\nRowan Vale moved the Harbor Lane walk to Sunday.\n"
	if err := os.WriteFile(filepath.Join(dir, "harbor-note-0007.md"), []byte(outside), 0600); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, memoryHistoryDirName, "harbor-note-0009")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "1789000000000.md"), []byte("# Harbor note 9, before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	advance(memoryFolderCheckEvery)
	got, err = app.GetMemoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	if entry := memoryEntryByID(got, "harbor-note-0007"); entry == nil || entry.Content != outside {
		t.Fatalf("outside edit not read after the check: %+v", entry)
	}
	if entry := memoryEntryByID(got, "harbor-note-0009"); entry == nil || !entry.HasHistory {
		t.Fatalf("outside history version not read after the check: %+v", entry)
	}

	// Removed from outside: gone after the check.
	if err := os.Remove(filepath.Join(dir, "harbor-note-0008.md")); err != nil {
		t.Fatal(err)
	}
	advance(memoryFolderCheckEvery)
	got, _ = app.GetMemoryEntries()
	if memoryEntryByID(got, "harbor-note-0008") != nil || len(got) != fictionalMemoryNotes-1 {
		t.Fatalf("outside delete not read after the check: %d notes", len(got))
	}
}

// The single .history scan keeps the per-note rule: a folder with no
// version, or only other files, is no history.
func TestMemoryHistoryScanMatchesPerNoteCheck(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := writeFictionalMemoryFolder(t, 300)
	history := filepath.Join(dir, memoryHistoryDirName)
	if err := os.MkdirAll(filepath.Join(history, "harbor-note-0010"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(history, "harbor-note-0011"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, "harbor-note-0011", "notes.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, "harbor-note-0012"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := (&App{}).GetMemoryEntries()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.HasHistory != memoryEntryHasHistory(dir, entry.ID) {
			t.Fatalf("%s: HasHistory=%v, per-note check says %v", entry.ID, entry.HasHistory, !entry.HasHistory)
		}
	}
	if historyCount(entries) != 3 {
		t.Fatalf("%d notes with history, want 3", historyCount(entries))
	}
}

// checkMemoryFolderNow makes the next read check the folder for edits made
// outside the app, as the next memoryFolderCheckEvery tick would. Fixtures
// that write notes directly call it after writing.
func checkMemoryFolderNow(a *App) {
	c := &a.memoryExternalReads
	c.mu.Lock()
	if c.snapshot != nil {
		c.snapshot.checked = time.Time{}
	}
	c.mu.Unlock()
}

func memoryEntryByID(entries []MemoryEntry, id string) *MemoryEntry {
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i]
		}
	}
	return nil
}

func BenchmarkGetMemoryEntries(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	writeFictionalMemoryFolder(b, fictionalMemoryNotes)
	app := &App{}
	if _, err := app.GetMemoryEntries(); err != nil {
		b.Fatal(err)
	}
	b.Run("unchanged", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := app.GetMemoryEntries(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("after-each-write", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			app.invalidateMemoryExternalReads()
			if _, err := app.GetMemoryEntries(); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// A background writer (the memory updater) saves notes many times a minute,
// and each save bumps the read revision. The next read must parse only the
// notes that changed on disk, not every note again.
func TestMemoryReadsParseOnlyChangedNotesWithABackgroundWriter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parsedNotes.reset()
	t.Cleanup(parsedNotes.reset)
	writeFictionalMemoryFolder(t, fictionalMemoryNotes)
	app := &App{}
	if _, err := app.GetMemoryEntries(); err != nil {
		t.Fatal(err)
	}
	files := countMemoryFiles(t)

	stop := make(chan struct{})
	done := make(chan struct{})
	var writes atomic.Int64
	go func() {
		defer close(done)
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			id := fmt.Sprintf("harbor-note-%04d", n%20)
			if _, err := app.SaveMemoryEntry(id, fmt.Sprintf("# Harbor note %d\n\nRowan Vale moved the walk, take %d.\n", n%20, n)); err != nil {
				t.Error(err)
				return
			}
			writes.Add(1)
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var worst time.Duration
	var worstFiles int64
	for i := 0; i < 30; i++ {
		files.take()
		start := time.Now()
		got, err := app.GetMemoryEntries()
		took := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != fictionalMemoryNotes {
			t.Fatalf("read %d: %d notes", i, len(got))
		}
		opens, _ := files.take()
		worst, worstFiles = max(worst, took), max(worstFiles, opens)
		time.Sleep(15 * time.Millisecond)
	}
	close(stop)
	<-done
	t.Logf("%d writes; worst read %s, at most %d files opened in one read", writes.Load(), worst, worstFiles)
	// Folder listings plus the notes the writer touched; never every note.
	if worstFiles > 100 {
		t.Fatalf("a read during background writes opened %d files; want only the changed notes", worstFiles)
	}
	// The writer's last words are what is read.
	got, _ := app.GetMemoryEntries()
	for _, entry := range got {
		if entry.ID == "harbor-note-0000" && !strings.Contains(entry.Content, "Rowan Vale moved the walk") {
			t.Fatalf("a changed note kept its old words: %q", entry.Content)
		}
	}
}

// Notes hidden as learned only from agent activity stay in the parse cache as
// hidden: after a memory write, the next read rescans the folder's file times
// but opens none of the hidden notes again.
func TestHiddenNotesAreNotReadAgainOnARescan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parsedNotes.reset()
	t.Cleanup(parsedNotes.reset)
	dir := writeFictionalMemoryFolder(t, fictionalMemoryNotes)
	old := time.Now().Add(-time.Hour)
	const hidden = 500
	for i := 0; i < hidden; i++ {
		path := filepath.Join(dir, fmt.Sprintf("harbor-note-%04d.md", i))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		marked, ok := markLearnedFromAgents(string(data))
		if !ok {
			t.Fatal("fixture note could not be marked")
		}
		if err := os.WriteFile(path, []byte(marked), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{}
	first, err := app.GetMemoryEntries()
	if err != nil || len(first) != fictionalMemoryNotes-hidden {
		t.Fatalf("first read: %d notes, %v", len(first), err)
	}
	files := countMemoryFiles(t)
	app.invalidateMemoryExternalReads()
	start := time.Now()
	got, err := app.GetMemoryEntries()
	took := time.Since(start)
	opens, _ := files.take()
	t.Logf("rescan after a write with %d hidden notes: %d opens in %s", hidden, opens, took)
	if err != nil || len(got) != fictionalMemoryNotes-hidden {
		t.Fatalf("rescan: %d notes, %v", len(got), err)
	}
	// Folder listings only (fingerprint, notes, .history and its folders).
	if opens > 2+1+1+fictionalMemoryHistory {
		t.Fatalf("a rescan opened %d files; the hidden notes were read again", opens)
	}
	// A hidden note that is unhidden (rewritten) is read again and listed.
	path := filepath.Join(dir, "harbor-note-0000.md")
	if err := os.WriteFile(path, []byte("# Harbor note 0\n\nRowan Vale is back.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app.invalidateMemoryExternalReads()
	got, _ = app.GetMemoryEntries()
	if memoryEntryByID(got, "harbor-note-0000") == nil {
		t.Fatal("a rewritten hidden note was not read again")
	}
}
