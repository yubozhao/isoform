package main

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// parsedNotes keeps each memory note as last parsed, keyed by its path and
// checked by its size and modification time. A memory write bumps the read
// revision, and a background writer (the memory updater) writes many times a
// minute; without this every write made the next GetMemoryEntries read and
// parse every note again (1.2 to 2.4 s at 4,867 notes). Now only the notes that
// changed on disk are read: the cache refreshes on a real file change only.
//
// A note is kept only once its modification time is older than
// memoryFolderSettle at the time it was read: file times are coarse on some
// file systems, so a same-size rewrite inside one tick would otherwise keep the
// old words. A note changed more recently is read from disk every time.
var parsedNotes parsedNoteCache

type parsedNoteCache struct {
	mu    sync.Mutex
	notes map[string]parsedNote
}

type parsedNote struct {
	size    int64
	modTime int64
	entry   MemoryEntry
	// hidden is a note learned only from agent activity (learnedFromAgents):
	// kept on disk, never listed. Caching the verdict keeps a rescan from
	// reading every hidden note again (1,548 of 4,876 on Bo's computer).
	hidden bool
}

func (c *parsedNoteCache) lookup(path string, info os.FileInfo) (entry MemoryEntry, hidden, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	note, found := c.notes[path]
	if !found || note.size != info.Size() || note.modTime != info.ModTime().UnixNano() {
		return MemoryEntry{}, false, false
	}
	if note.hidden {
		return MemoryEntry{}, true, true
	}
	return cloneParsedMemoryEntry(note.entry), false, true
}

func (c *parsedNoteCache) store(path string, info os.FileInfo, entry MemoryEntry, readSize int64) {
	c.put(path, info, parsedNote{entry: cloneParsedMemoryEntry(entry)}, readSize)
}

// storeHidden records that the note at path is hidden.
func (c *parsedNoteCache) storeHidden(path string, info os.FileInfo, readSize int64) {
	c.put(path, info, parsedNote{hidden: true}, readSize)
}

func (c *parsedNoteCache) put(path string, info os.FileInfo, note parsedNote, readSize int64) {
	if readSize != info.Size() || memoryReadNow().Sub(info.ModTime()) < memoryFolderSettle {
		// Changed while it was read, or too new to trust its time: read it again.
		return
	}
	note.size, note.modTime = info.Size(), info.ModTime().UnixNano()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.notes == nil {
		c.notes = map[string]parsedNote{}
	}
	c.notes[path] = note
}

// prune drops the notes of dir that the last read no longer found (seen holds
// every note path it listed, hidden ones too), so a note removed from the
// folder leaves the cache with it.
func (c *parsedNoteCache) prune(dir string, seen map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for path := range c.notes {
		if filepath.Dir(path) == dir && !seen[path] {
			delete(c.notes, path)
		}
	}
}

// reset empties the cache; tests use it.
func (c *parsedNoteCache) reset() {
	c.mu.Lock()
	c.notes = nil
	c.mu.Unlock()
}

// cloneParsedMemoryEntry copies the slices, so no caller shares them with the
// cache.
func cloneParsedMemoryEntry(entry MemoryEntry) MemoryEntry {
	entry.TopicIDs = slices.Clone(entry.TopicIDs)
	entry.ActivitySessionIDs = slices.Clone(entry.ActivitySessionIDs)
	entry.Apps = slices.Clone(entry.Apps)
	return entry
}
