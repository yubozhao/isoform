package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Memory Dream Archive
//
// Every memory the Dream cycle removes is preserved as a markdown file under
// `<memoryDir>/.archive/<archive-id>.md`. This mirrors the file-first design
// of the live memory store: the markdown file is the source of truth, and
// the frontmatter carries everything needed to identify the original entry,
// the cycle that archived it, and the survivor it was merged into.
//
// Why files and not a SQLite table:
//   - The live store is already file-based with rich frontmatter; an
//     archive table would diverge.
//   - Users can inspect, grep, and even hand-restore archived memories
//     without the app running.
//   - `.history/` already establishes the "hidden sibling directory"
//     pattern for per-entry version snapshots. Adding `.archive/` extends
//     the same pattern to deletions.
//
// The directory is excluded from the memory index walker (see the patch
// in memory_sync.go) so archived content never resurfaces in search.

const memoryArchiveDirName = ".archive"

// ArchiveReason enumerates the named reasons Dream archives a memory.
// Anything outside this list is rejected at write time so the UI doesn't
// have to handle freeform strings.
type ArchiveReason string

const (
	ArchiveReasonExactDup        ArchiveReason = "exact_dup"
	ArchiveReasonExpired         ArchiveReason = "expired"
	ArchiveReasonSimilarityMerge ArchiveReason = "similarity_merge"
	ArchiveReasonLLMMerge        ArchiveReason = "llm_merge"
	ArchiveReasonManual          ArchiveReason = "manual"
)

var validArchiveReasons = map[ArchiveReason]struct{}{
	ArchiveReasonExactDup:        {},
	ArchiveReasonExpired:         {},
	ArchiveReasonSimilarityMerge: {},
	ArchiveReasonLLMMerge:        {},
	ArchiveReasonManual:          {},
}

// ArchivedMemory is the Wails-exposed view of an entry under .archive/.
type ArchivedMemory struct {
	ArchiveID  string `json:"archiveId"`
	OriginalID string `json:"originalId"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	ArchivedAt int64  `json:"archivedAt"`
	ArchivedBy string `json:"archivedBy"`
	Reason     string `json:"reason"`
	MergedInto string `json:"mergedInto,omitempty"`
	Dimension  string `json:"dimension"`
	Source     string `json:"source"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// memoryArchiveDir returns <memoryDir>/.archive, creating it on demand.
func memoryArchiveDir() (string, error) {
	dir, err := memoryDir()
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, memoryArchiveDirName)
	if err := os.MkdirAll(out, 0700); err != nil {
		return "", err
	}
	return out, nil
}

// archiveHeaderLines returns the Dream-specific frontmatter that prefixes
// every archived memory's original content. Lines order is stable so future
// readers can rely on field positions if they want a cheap parse.
func archiveHeaderLines(orig MemoryEntry, runID string, reason ArchiveReason, mergedInto string) string {
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "# Archived: %s\n\n", strings.Join(strings.Fields(orig.Title), " "))
	fmt.Fprintf(&b, "- **OriginalId**: %s\n", orig.ID)
	fmt.Fprintf(&b, "- **ArchivedAt**: %s\n", now.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **ArchivedAtMs**: %d\n", now.UnixMilli())
	fmt.Fprintf(&b, "- **ArchivedBy**: %s\n", runID)
	fmt.Fprintf(&b, "- **Reason**: %s\n", reason)
	if mergedInto != "" {
		fmt.Fprintf(&b, "- **MergedInto**: %s\n", mergedInto)
	}
	if orig.Dimension != "" {
		fmt.Fprintf(&b, "- **OriginalDimension**: %s\n", orig.Dimension)
	}
	b.WriteString("\n---\n\n")
	return b.String()
}

// archiveMemoryFile copies a live memory file into .archive/ with a Dream
// header prepended, then deletes the live file. The corresponding
// .history/<id>/ snapshot tree is removed after its resolved privacy
// provenance has been preserved in the archived document.
//
// archiveID is returned so callers can record it in run metadata and the
// frontend can offer a per-archive restore button.
func (a *App) archiveMemoryFile(orig MemoryEntry, runID string, reason ArchiveReason, mergedInto string) (string, error) {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	if _, ok := validArchiveReasons[reason]; !ok {
		return "", fmt.Errorf("invalid archive reason %q", reason)
	}
	if strings.Contains(orig.ID, "..") || strings.ContainsAny(orig.ID, "/\\") {
		return "", fmt.Errorf("invalid memory id %q", orig.ID)
	}

	dir, err := memoryDir()
	if err != nil {
		return "", err
	}
	archDir, err := memoryArchiveDir()
	if err != nil {
		return "", err
	}

	// Take the file lock so we don't race a concurrent SaveMemoryEntry on
	// the same id (e.g. an activity triager landing a fresh write
	// while Dream is mid-cycle).
	unlock := memoryFileLocks.Acquire(orig.ID)
	defer unlock()

	src := filepath.Join(dir, orig.ID+".md")
	body, err := os.ReadFile(src)
	if err != nil {
		// If the live file has already vanished (deleted by user between
		// scan and archive) treat it as a no-op rather than a fatal
		// cycle abort.
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read live memory %s: %w", orig.ID, err)
	}
	if err := checkMemoryWriteAllowed(dir, orig.ID, string(body), true); err != nil {
		return "", err
	}

	// Generate a collision-resistant id. UnixNano + UUID would be ideal
	// but Wails' JS marshaller cannot represent int64 > 2^53, so we use
	// UnixMilli + a short random suffix. Loop a few times if the file
	// already exists to ride out millisecond collisions during a single
	// cycle (the cycle can archive many entries in the same tick).
	var archiveID, dst string
	for attempt := 0; attempt < 8; attempt++ {
		archiveID = fmt.Sprintf("archive-%d-%s", time.Now().UnixMilli(), randomShortID())
		dst = filepath.Join(archDir, archiveID+".md")
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Resolve before deleting history: otherwise restoring a mixed legacy
	// conversation could mistake the missing revisions for pure lineage.
	provenance := memoryReadProvenanceForFile(dir, orig.ID, string(body))
	preserved := memoryWithResolvedProvenance(orig.ID, string(body), provenance)
	header := archiveHeaderLines(orig, runID, reason, mergedInto)
	out := append([]byte(header), []byte(preserved)...)
	if err := os.WriteFile(dst, out, 0600); err != nil {
		return "", fmt.Errorf("write archive %s: %w", dst, err)
	}

	// A merge is an edit: the watcher must not report this removal as a
	// privacy withdrawal of the note.
	noteMemoryRemovedByRewrite(orig.ID)
	if err := os.Remove(src); err != nil && !os.IsNotExist(err) {
		// Try to roll back the archive write so we don't leak a phantom
		// archive entry pointing at a still-live memory. Best-effort:
		// if the rollback itself fails we surface the original delete
		// error, not the rollback error.
		_ = os.Remove(dst)
		return "", fmt.Errorf("remove live memory %s: %w", src, err)
	}

	// Drop the per-entry version-history tree so Dream-removed memories
	// don't leak their old content back via the history drawer. The live
	// file is gone; keeping its snapshots would contradict the archive's
	// "this memory is gone" semantic.
	_ = os.RemoveAll(filepath.Join(dir, memoryHistoryDirName, orig.ID))

	return archiveID, nil
}

// countArchivedMemories counts readable markdown files without reading their
// contents. Opening each candidate checks the same file access as the full
// listing, including ACLs and symlink targets, without loading archive bodies.
// Dangling links, directory links and special files do not count.
func countArchivedMemories() (int, error) {
	archDir, err := memoryArchiveDir()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(archDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			info, err = os.Stat(filepath.Join(archDir, entry.Name()))
			if err != nil {
				continue
			}
		}
		if !info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(filepath.Join(archDir, entry.Name()))
		if err != nil {
			continue
		}
		// Inspect the opened file too, in case the path changed after the
		// directory scan. No archive bytes are read for the status count.
		info, err = file.Stat()
		_ = file.Close()
		if err == nil && info.Mode().IsRegular() {
			count++
		}
	}
	return count, nil
}

// listArchivedMemories returns archived entries newest-first. Reads frontmatter
// for each file inline — there are typically only dozens to a few hundred
// archived entries (we GC after 30 days), so a separate index isn't worth the
// staleness risk.
func listArchivedMemories(limit int) ([]ArchivedMemory, error) {
	archDir, err := memoryArchiveDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(archDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []ArchivedMemory
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(archDir, e.Name()))
		if err != nil {
			continue
		}
		archiveID := strings.TrimSuffix(e.Name(), ".md")
		am := parseArchivedMemory(archiveID, string(data))
		am.SizeBytes = info.Size()
		if am.ArchivedAt == 0 {
			am.ArchivedAt = info.ModTime().UnixMilli()
		}
		out = append(out, am)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ArchivedAt > out[j].ArchivedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// parseArchivedMemory reads our header frontmatter from an archived file's
// content. The returned struct's Content field is the FULL file (header
// included) so the UI can show the user exactly what's preserved. We don't
// strip the header for display because doing so lossily reformats the body
// — preserving the bytes is the whole point of an archive.
func parseArchivedMemory(archiveID, content string) ArchivedMemory {
	am := ArchivedMemory{ArchiveID: archiveID, Content: content}
	lines := strings.Split(content, "\n")
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			am.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			am.Title = strings.TrimPrefix(am.Title, "Archived: ")
			continue
		}
		if !strings.HasPrefix(trimmed, "- **") {
			// Stop scanning frontmatter once we hit body content or the
			// `---` rule. Everything below is the original memory's
			// content.
			if strings.HasPrefix(trimmed, "---") {
				break
			}
			continue
		}
		rest := trimmed[len("- **"):]
		end := strings.Index(rest, "**:")
		if end < 0 {
			continue
		}
		key := rest[:end]
		value := strings.TrimSpace(rest[end+len("**:"):])
		switch key {
		case "OriginalId":
			am.OriginalID = value
		case "ArchivedAtMs":
			if v, err := parseInt64(value); err == nil {
				am.ArchivedAt = v
			}
		case "ArchivedBy":
			am.ArchivedBy = value
		case "Reason":
			am.Reason = value
		case "MergedInto":
			am.MergedInto = value
		case "OriginalDimension":
			am.Dimension = value
		case "Source":
			am.Source = value
		}
	}
	return am
}

// restoreArchivedMemory copies an archived memory back to the live store
// (stripping the Dream header) and removes the archive file on success.
// Returns the restored memory id.
//
// If a memory with the same id already exists in the live store, we fail
// rather than overwriting — overwriting would defeat the user's intent
// (a fresh memory with that id has appeared after the archive was created;
// silently clobbering it could lose data). The user can delete the
// conflicting live memory first and try again.
func (a *App) restoreArchivedMemory(archiveID string) (string, error) {
	if strings.Contains(archiveID, "..") || strings.ContainsAny(archiveID, "/\\") {
		return "", fmt.Errorf("invalid archive id %q", archiveID)
	}
	archDir, err := memoryArchiveDir()
	if err != nil {
		return "", err
	}
	srcPath := filepath.Join(archDir, archiveID+".md")
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return "", err
	}
	am := parseArchivedMemory(archiveID, string(raw))
	if am.OriginalID == "" || strings.Contains(am.OriginalID, "..") || strings.ContainsAny(am.OriginalID, "/\\") {
		return "", fmt.Errorf("archive %s has invalid OriginalId", archiveID)
	}

	originalBody := stripArchiveHeader(string(raw))
	body := originalBody
	if doc, ok := parseOKFDocument(body); !ok || okfString(doc.Frontmatter, "read_provenance") == "" {
		// Old archives discarded their history. Do not recreate source
		// privileges from the surviving last revision alone.
		body = memoryWithResolvedProvenance(am.OriginalID, body, memoryReadProvenance{Version: 1, Withheld: true})
	}

	dir, err := memoryDir()
	if err != nil {
		return "", err
	}
	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock := memoryFileLocks.Acquire(am.OriginalID)
	defer unlock()
	dst := filepath.Join(dir, am.OriginalID+".md")
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("memory %s already exists; delete it before restoring", am.OriginalID)
	}
	current, err := os.ReadFile(srcPath)
	if err != nil {
		return "", err
	}
	if string(current) != string(raw) {
		return "", fmt.Errorf("memory archive changed while restoring; try again")
	}
	if err := authorizeMemoryHumanMutation(dir, am.OriginalID, originalBody, body); err != nil {
		return "", err
	}

	if err := os.WriteFile(dst, []byte(body), 0600); err != nil {
		return "", err
	}
	if err := os.Remove(srcPath); err != nil {
		logMemoryDreamError("restore_remove_archive", "", err)
	}
	a.markMemoryDirty("dream-restore")
	return am.OriginalID, nil
}

// stripArchiveHeader removes the `# Archived:` title, the frontmatter block,
// and the trailing `---` separator from an archived file, leaving only the
// original memory's bytes. Returns the input verbatim if no separator is
// found (defensive: better to restore something than nothing).
func stripArchiveHeader(content string) string {
	idx := strings.Index(content, "\n---\n")
	if idx < 0 {
		return content
	}
	after := content[idx+len("\n---\n"):]
	return strings.TrimLeft(after, "\n")
}

// purgeArchiveBefore deletes archived markdown files whose ArchivedAt is
// older than the cutoff (Unix ms). Returns the count removed. Files with no
// parseable timestamp fall back to ModTime; if both are zero we keep them
// (safer to retain unidentifiable archives than to drop them).
func purgeArchiveBefore(cutoffMs int64) (int, error) {
	archDir, err := memoryArchiveDir()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(archDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var n int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(archDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		am := parseArchivedMemory(strings.TrimSuffix(e.Name(), ".md"), string(data))
		ts := am.ArchivedAt
		if ts == 0 {
			ts = info.ModTime().UnixMilli()
		}
		if ts > 0 && ts < cutoffMs {
			if err := os.Remove(path); err == nil {
				n++
			}
		}
	}
	return n, nil
}

// undoDreamRun restores every memory archived by a given run id. Returns the
// number restored. Memories that fail to restore (e.g. live conflict) are
// skipped with a log line — partial undo beats no undo.
func (a *App) undoDreamRun(runID string) (int, error) {
	items, err := listArchivedMemories(0)
	if err != nil {
		return 0, err
	}
	var restored int
	for _, am := range items {
		if am.ArchivedBy != runID {
			continue
		}
		if _, err := a.restoreArchivedMemory(am.ArchiveID); err != nil {
			logMemoryDreamError("undo_restore", am.ArchiveID, err)
			continue
		}
		restored++
	}
	return restored, nil
}

// parseInt64 is a tiny strconv wrapper that swallows whitespace and rejects
// empty strings. Used by the archive parser so we don't pull in strconv at
// the top.
func parseInt64(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var v int64
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, err
	}
	return v, nil
}

// randomShortID returns a short alphanumeric suffix used to avoid millisecond
// collisions in archive ids during a single Dream cycle. UUID would work but
// the full 36-char form bloats the filename and a 6-char suffix is plenty
// inside a single ms.
func randomShortID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	const n = 6
	now := time.Now().UnixNano()
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(alphabet[int(now>>uint(i*5))&31%len(alphabet)])
	}
	return b.String()
}
