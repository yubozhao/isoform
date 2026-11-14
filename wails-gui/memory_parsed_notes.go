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
}

func (c *parsedNoteCache) lookup(path string, info os.FileInfo) (MemoryEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	note, ok := c.notes[path]
	if !ok || note.size != info.Size() || note.modTime != info.ModTime().UnixNano() {
		return MemoryEntry{}, false
	}
	return cloneParsedMemoryEntry(note.entry), true
}

func (c *parsedNoteCache) store(path string, info os.FileInfo, entry MemoryEntry, readSize int64) {
	if readSize != info.Size() || memoryReadNow().Sub(info.ModTime()) < memoryFolderSettle {
		// Changed while it was read, or too new to trust its time: read it again.
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.notes == nil {
		c.notes = map[string]parsedNote{}
	}
	c.notes[path] = parsedNote{size: info.Size(), modTime: info.ModTime().UnixNano(), entry: cloneParsedMemoryEntry(entry)}
}

// prune drops the notes of dir that the last read no longer found, so a note
// removed from the folder leaves the cache with it.
func (c *parsedNoteCache) prune(dir string, live []MemoryEntry) {
	keep := make(map[string]bool, len(live))
	for _, entry := range live {
		keep[filepath.Join(dir, entry.ID+".md")] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for path := range c.notes {
		if filepath.Dir(path) == dir && !keep[path] {
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
