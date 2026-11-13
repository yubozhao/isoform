package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
)

// memoryHistoryDirName is the per-file version history directory that lives
// inside the main memory directory. Kept hidden (leading dot) so the top-level
// memory listing ignores it and it sorts last in Finder.
const memoryHistoryDirName = ".history"

// memoryFileLocks serializes writes to a single memory file across goroutines.
// Two activity sessions ending simultaneously could both land on the same
// dedup target (same memoryID) and clobber each other's append; we can't rely
// on OS file locks because macOS flock behavior across APFS + goroutines is
// inconsistent. A keyed in-memory mutex is simple and enough for a
// single-process desktop app.
//
// The outer map is guarded by its own mutex; callers grab a per-ID mutex and
// must release it via the returned unlock function.
type memoryFileLockSet struct {
	// Admission takes the exclusive side while publishing generated source facts.
	publication sync.RWMutex
	mu          sync.Mutex
	locks       map[string]*sync.Mutex
}

var memoryFileLocks = &memoryFileLockSet{locks: map[string]*sync.Mutex{}}

// Acquire locks the per-memoryID mutex and returns an unlock function. Callers
// should always `defer unlock()` immediately so panics don't leak the lock.
//
// The returned unlock is once-wrapped: calling it more than once is a no-op.
// This lets callers release the lock manually before a fallback path (e.g.
// before delegating to another function that acquires its own lock on a
// different ID) and still safely `defer unlock()` at the top for the error
// path, without risking a double-unlock panic.
func (s *memoryFileLockSet) Acquire(id string) func() {
	s.publication.RLock()
	unlock := s.AcquireRead(id)
	var once sync.Once
	return func() { once.Do(func() { unlock(); s.publication.RUnlock() }) }
}

// AcquireRead serializes an immutable provenance read with the per-file writer.
// It does not acquire the publication barrier: a final read may already own its
// exclusive side, and never changes the file or its revision history.
func (s *memoryFileLockSet) AcquireRead(id string) func() {
	s.mu.Lock()
	lk, ok := s.locks[id]
	if !ok {
		lk = &sync.Mutex{}
		s.locks[id] = lk
	}
	s.mu.Unlock()
	lk.Lock()
	var once sync.Once
	return func() { once.Do(lk.Unlock) }
}

// MemoryEntry represents a memory markdown file for the frontend.
//
// UpdatedAt / Source / ActivitySessionIDs / Apps are parsed from frontmatter
// metadata lines (`- **Key**: Value`) at the top of the markdown body. They
// may be zero / empty for manually authored memories that omit the header.
//
// ActivitySessionIDs contains supporting evidence links, excluding comparison
// inputs retained only for privacy. Ambiguous older generated links are hidden.
// The legacy file format uses a nested bullet list so users can
// jump to any contributing session, not just the first one. Legacy files
// with a single-line `- **Activity Session**: <id>` still parse as a
// one-element list so the frontend can offer at least one jump target.
type MemoryEntry struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Content            string   `json:"content"`
	ModTime            int64    `json:"modTime"`
	SizeBytes          int64    `json:"sizeBytes"`
	Dimension          string   `json:"dimension"`
	Area               string   `json:"area,omitempty"`
	TopicIDs           []string `json:"topicIds,omitempty"`
	UpdatedAt          int64    `json:"updatedAt"`
	Source             string   `json:"source"`
	ActivitySessionIDs []string `json:"activitySessionIds"`
	Apps               []string `json:"apps"`
	// HasHistory is true when the entry has at least one prior snapshot under
	// its `.history/<id>/` directory. Surfaced so the frontend can hide the
	// "History" button on entries that would otherwise open an empty drawer
	// (e.g. INITIAL activity-derived memories that have never been updated).
	HasHistory bool `json:"hasHistory"`
	// Single-entry DTO serialization gates fallback links; list rows leave this nil.
	displayApp *App
}

// memoryHistoryIDs lists the entries with at least one snapshot, by the same
// rule as memoryEntryHasHistory, reading .history once instead of once per
// entry.
func memoryHistoryIDs(dir string) map[string]bool {
	root := filepath.Join(dir, memoryHistoryDirName)
	folders, err := memoryFiles.ReadDir(root)
	if err != nil {
		return nil
	}
	ids := map[string]bool{}
	for _, folder := range folders {
		if !folder.IsDir() && folder.Type()&os.ModeSymlink == 0 {
			continue
		}
		files, err := memoryFiles.ReadDir(filepath.Join(root, folder.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if !file.IsDir() && strings.HasSuffix(file.Name(), ".md") {
				ids[folder.Name()] = true
				break
			}
		}
	}
	return ids
}

// memoryEntryHasHistory returns true when the entry's history directory exists
// and contains at least one snapshot. Uses os.ReadDir over a pure Stat because
// the snapshot dir is created lazily on first update — an empty dir would
// still mislead the UI into showing the History button.
func memoryEntryHasHistory(dir, id string) bool {
	entries, err := os.ReadDir(filepath.Join(dir, memoryHistoryDirName, id))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			return true
		}
	}
	return false
}

// validMemoryDimensions is the set of recognized dimension values.
// Mirrors the classifications used by MemoryGenerator. "custom" is a
// fallback for user-authored manual memories (never emitted by the LLM).
var validMemoryDimensions = map[string]struct{}{
	"user_insight":    {},
	"topic_highlight": {},
	"deep_user_model": {},
	"agentic_memory":  {},
	"custom":          {},
}

// extractMemoryDimension scans memory markdown for its dimension. OKF files
// carry this in YAML frontmatter; legacy files use a `- **Dimension**: xxx`
// metadata line near the top.
func extractMemoryDimension(content string) string {
	return memoryDimensionFromFrontmatter(parseMemoryFrontmatter(content))
}

func memoryDimensionFromFrontmatter(fm memoryFrontmatter) string {
	val := strings.ToLower(fm.Dimension)
	if _, ok := validMemoryDimensions[val]; ok {
		return val
	}
	return ""
}

type memoryFrontmatter struct {
	Area                  string
	TopicIDs              []string
	UpdatedAt             int64
	Source                string
	ActivitySessionIDs    []string
	Apps                  []string
	Dimension             string
	ReadProvenance        string
	CommunicationCategory string
	SentMessageCount      int
	EvidenceFirstAt       int64
	EvidenceLastAt        int64
	EvidenceMessageIDs    []string
}

// parseMemoryFrontmatter extracts `- **Key**: Value` metadata lines from the
// top of a memory markdown file. The frontmatter block must appear
// contiguously at the top, optionally preceded by a single `# Title` heading
// and blank lines. Any other content ends the scan — so body bullets,
// paragraphs, or later `# Section` headings are never treated as metadata.
//
// `Activity Session` has two accepted forms. The current writer emits a
// nested bullet list (one session per bullet); legacy files still use a
// single-line value. Both collapse into ActivitySessionIDs.
func parseMemoryFrontmatter(content string) memoryFrontmatter {
	content = legacyMemoryWithoutConfirmation(content)
	doc, ok := parseOKFDocument(content)
	return parseMemoryFrontmatterFromDocument(content, doc, ok)
}

func parseMemoryFrontmatterFromDocument(content string, doc okfDocument, ok bool) memoryFrontmatter {
	var fm memoryFrontmatter
	if ok {
		fm.UpdatedAt = parseOKFTimestampMs(okfString(doc.Frontmatter, "timestamp"))
		fm.Source = okfString(doc.Frontmatter, "source")
		fm.ActivitySessionIDs = okfStringSlice(doc.Frontmatter, "activity_sessions")
		fm.Apps = okfStringSlice(doc.Frontmatter, "apps")
		fm.Dimension = okfString(doc.Frontmatter, "dimension")
		fm.Area = okfString(doc.Frontmatter, "area")
		if raw, exists := doc.Frontmatter["topic_ids"]; exists {
			data, _ := json.Marshal(raw)
			_ = json.Unmarshal(data, &fm.TopicIDs)
		}
		fm.ReadProvenance = okfString(doc.Frontmatter, "read_provenance")
		fm.CommunicationCategory = okfString(doc.Frontmatter, "communication_category")
		fm.SentMessageCount = int(okfBoundedInt(doc.Frontmatter["sent_message_count"], communicationMaxSentMessageCount))
		fm.EvidenceFirstAt = parseOKFTimestampMs(okfString(doc.Frontmatter, "evidence_first_at"))
		fm.EvidenceLastAt = parseOKFTimestampMs(okfString(doc.Frontmatter, "evidence_last_at"))
		fm.EvidenceMessageIDs = okfStringSlice(doc.Frontmatter, "evidence_message_ids")
		if len(fm.EvidenceMessageIDs) > communicationMaxEvidenceIDs {
			fm.EvidenceMessageIDs = fm.EvidenceMessageIDs[len(fm.EvidenceMessageIDs)-communicationMaxEvidenceIDs:]
		}
		return fm
	}
	lines := strings.Split(content, "\n")
	skippedTitle := false
	seen := map[string]bool{}
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			if len(seen) > 0 {
				break
			}
			continue
		}
		// Allow exactly one `# Title` heading before the metadata block.
		if !skippedTitle && len(seen) == 0 && strings.HasPrefix(trimmed, "# ") {
			skippedTitle = true
			continue
		}
		// Anything that is not a `- **Key**: Value` line ends the frontmatter.
		if !strings.HasPrefix(trimmed, "- **") {
			break
		}

		rest := trimmed[len("- **"):]
		end := strings.Index(rest, "**:")
		if end < 0 {
			break
		}
		key := rest[:end]
		value := strings.TrimSpace(rest[end+len("**:"):])
		if seen[key] {
			break
		}
		seen[key] = true

		switch key {
		case "Updated":
			if t, err := time.ParseInLocation("2006-01-02 15:04", value, time.Local); err == nil {
				fm.UpdatedAt = t.UnixMilli()
			}
		case "Source":
			fm.Source = value
		case "Dimension":
			fm.Dimension = value
		case "Area":
			fm.Area = value
		case "Topic IDs":
			_ = json.Unmarshal([]byte(value), &fm.TopicIDs)
		case "Read Provenance":
			fm.ReadProvenance = value
		case "Activity Session":
			if value != "" {
				// Legacy single-line form.
				fm.ActivitySessionIDs = append(fm.ActivitySessionIDs, value)
				continue
			}
			// Nested bullet form. Accepts two shapes and flattens both to
			// the same ActivitySessionIDs list:
			//
			//   flat (legacy):
			//     - **Activity Session**:
			//       - <id> — 10:28, App
			//
			//   grouped (post version-history refactor):
			//     - **Activity Session**:
			//       - **Update 2026-04-20 11:30** (initial)
			//         - <id> — 10:28, App
			//
			// Group header lines start with `**Update ` (after the bullet
			// marker) and carry no session ID; we skip them but keep scanning
			// deeper-indented session bullets that follow.
			for j := i + 1; j < len(lines); j++ {
				raw := lines[j]
				if strings.TrimSpace(raw) == "" {
					break
				}
				// Any indented `- `-prefixed line belongs to the block.
				if !(strings.HasPrefix(raw, " ") && strings.HasPrefix(strings.TrimLeft(raw, " \t"), "- ")) {
					break
				}
				inner := strings.TrimLeft(raw, " \t")
				inner = strings.TrimPrefix(inner, "- ")
				if strings.HasPrefix(strings.TrimSpace(inner), "**Update ") {
					// Group header — no session ID, continue scanning its
					// nested session rows below.
					i = j
					continue
				}
				id := parseSessionBulletID(raw)
				if id != "" {
					fm.ActivitySessionIDs = append(fm.ActivitySessionIDs, id)
				}
				i = j
			}
		case "Apps":
			parts := strings.Split(value, ",")
			apps := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					apps = append(apps, p)
				}
			}
			fm.Apps = apps
		}
	}
	return fm
}

// parseSessionBulletID extracts the session ID from a line like
// `  - <session-id> — 10:28, cmux, Obsidian`. Returns "" if the line does
// not match the expected shape so the parser can skip malformed rows
// without breaking the outer scan.
func parseSessionBulletID(raw string) string {
	trimmed := strings.TrimLeft(raw, " \t")
	trimmed = strings.TrimPrefix(trimmed, "- ")
	// Split on either the em-dash (new format) or a plain space to allow
	// graceful parsing of early hand-written variants.
	for _, sep := range []string{" — ", " - ", " "} {
		if idx := strings.Index(trimmed, sep); idx > 0 {
			return strings.TrimSpace(trimmed[:idx])
		}
	}
	return strings.TrimSpace(trimmed)
}

// extractActivitySessionBlock returns the full multi-line Activity Session
// block from memory content, starting at the `- **Activity Session**:` header
// and including every consecutive indented bullet row below it. Returns "" if
// no such block exists.
//
// The returned string has no trailing newline so callers can control
// separator formatting. Used by the activity triager update path to
// preserve historical session groups verbatim when appending a new group.
func extractActivitySessionBlock(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "- **Activity Session**:" {
			continue
		}
		var b strings.Builder
		b.WriteString(lines[i])
		for j := i + 1; j < len(lines); j++ {
			raw := lines[j]
			if strings.TrimSpace(raw) == "" {
				break
			}
			if !(strings.HasPrefix(raw, " ") && strings.HasPrefix(strings.TrimLeft(raw, " \t"), "- ")) {
				break
			}
			b.WriteString("\n")
			b.WriteString(raw)
		}
		return b.String()
	}
	return ""
}

// isGroupedActivitySessionBlock reports whether the given block (as returned
// by extractActivitySessionBlock) already uses the grouped format. A block is
// grouped iff at least one `- **Update ...**` header line appears inside the
// indented portion. Flat legacy blocks contain only session rows.
func isGroupedActivitySessionBlock(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		inner := strings.TrimLeft(line, " \t")
		inner = strings.TrimPrefix(inner, "- ")
		if strings.HasPrefix(strings.TrimSpace(inner), "**Update ") {
			return true
		}
	}
	return false
}

// upgradeFlatActivitySessionBlock rewrites a legacy flat Activity Session
// block as a grouped block with a single `pre-migration` group anchored at
// the given timestamp. If the block is already grouped, it is returned
// unchanged. Empty input yields empty output.
//
// The output format matches the one emitted by the activity triager
// (plain indented text, not a dash-bulleted list, so gofmt doesn't flatten
// the nesting):
//
//	**Activity Session**:
//	    **Update 2026-04-19 10:30** (pre-migration)
//	        <id> — 10:28, App
//
// preMigrationMs should be the file's mtime at read-time (best-effort
// approximation of when the flat block was last written).
func upgradeFlatActivitySessionBlock(block string, preMigrationMs int64) string {
	if block == "" {
		return ""
	}
	if isGroupedActivitySessionBlock(block) {
		return block
	}
	lines := strings.Split(block, "\n")
	if len(lines) < 2 {
		// Only the header was present — nothing to wrap.
		return block
	}
	ts := time.UnixMilli(preMigrationMs).Format("2006-01-02 15:04")
	var b strings.Builder
	b.WriteString(lines[0]) // `- **Activity Session**:`
	b.WriteString("\n  - **Update ")
	b.WriteString(ts)
	b.WriteString("** (pre-migration)")
	for _, row := range lines[1:] {
		if strings.TrimSpace(row) == "" {
			continue
		}
		// Re-indent legacy 2-space session rows to the 4-space depth used
		// under grouped headers. Strip and rebuild instead of string-replace
		// so misindented rows (e.g. tabs) normalize too.
		inner := strings.TrimLeft(row, " \t")
		b.WriteString("\n    ")
		b.WriteString(inner)
	}
	return b.String()
}

func memoryDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataRoot, "memory")
	os.MkdirAll(dir, 0700)
	return dir, nil
}

// memoryHistoryDir returns dir/.history/<entryID>, creating the tree when
// needed. The leading dot on memoryHistoryDirName keeps Finder + the top-level
// listing in GetMemoryEntries quiet (it already skips directories, but this
// also hides the folder by default on macOS).
func memoryHistoryDir(dir, entryID string) (string, error) {
	historyDir := filepath.Join(dir, memoryHistoryDirName, entryID)
	if err := os.MkdirAll(historyDir, 0700); err != nil {
		return "", fmt.Errorf("mkdir history: %w", err)
	}
	return historyDir, nil
}

// snapshotMemoryFile copies dir/<entryID>.md to dir/.history/<entryID>/<unix-ms>.md
// before the caller overwrites the live file. Returns nil silently when the
// source file does not yet exist (first-write path). Any other read/write
// failure is returned so callers can abort the overwrite — losing the old
// contents with no snapshot defeats the whole version-history feature.
//
// Caller MUST hold memoryFileLocks.Acquire(entryID) across snapshot + write to
// avoid two updates racing and overwriting each other's snapshots.
func snapshotMemoryFile(dir, entryID string) error {
	src := filepath.Join(dir, entryID+".md")
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read source %s: %w", src, err)
	}
	historyDir, err := memoryHistoryDir(dir, entryID)
	if err != nil {
		return err
	}
	// Filename is the pre-overwrite UnixMilli. Nanoseconds would be more
	// collision-resistant, but UnixNano overflows JavaScript's
	// Number.MAX_SAFE_INTEGER (2^53), which would silently corrupt the ts
	// when Wails JSON-marshals it back to the UI.
	//
	// Two back-to-back updates on the same entry can still land in the same
	// millisecond even under the per-entry lock (the lock bounds other goroutines,
	// not wall-clock resolution). O_EXCL + a tiny retry loop keeps the second
	// snapshot from stomping the first.
	for attempt := 0; attempt < 8; attempt++ {
		dst := filepath.Join(historyDir, fmt.Sprintf("%d.md", time.Now().UnixMilli()))
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				time.Sleep(time.Millisecond)
				continue
			}
			return fmt.Errorf("open snapshot %s: %w", dst, err)
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil {
			return fmt.Errorf("write snapshot %s: %w", dst, werr)
		}
		if cerr != nil {
			return fmt.Errorf("close snapshot %s: %w", dst, cerr)
		}
		return nil
	}
	return fmt.Errorf("snapshot filename collision for %s after retries", entryID)
}

func extractTitle(content string) string {
	content = legacyMemoryWithoutConfirmation(content)
	doc, ok := parseOKFDocument(content)
	return extractTitleFromDocument(content, doc, ok)
}

func extractTitleFromDocument(content string, doc okfDocument, ok bool) string {
	if ok {
		if title := okfString(doc.Frontmatter, "title"); title != "" {
			return title
		}
	}
	for _, line := range strings.SplitN(content, "\n", 20) {
		line = strings.TrimSpace(line)
		if title, ok := strings.CutPrefix(line, "# "); ok {
			return title
		}
	}
	return ""
}

type parsedMemoryEntryDocument struct {
	content         string
	metadataContent string
	doc             okfDocument
	ok              bool
	frontmatter     memoryFrontmatter
}

func parseMemoryEntryDocument(content string) parsedMemoryEntryDocument {
	metadataContent := legacyMemoryWithoutConfirmation(content)
	doc, ok := parseOKFDocument(metadataContent)
	return parsedMemoryEntryDocument{
		content:         content,
		metadataContent: metadataContent,
		doc:             doc,
		ok:              ok,
		frontmatter:     parseMemoryFrontmatterFromDocument(metadataContent, doc, ok),
	}
}

func (doc parsedMemoryEntryDocument) title() string {
	return extractTitleFromDocument(doc.metadataContent, doc.doc, doc.ok)
}

// GetMemoryEntries returns all memory markdown files, newest first.
// Reads share one parsed snapshot per memory change; callers get copies.
func (a *App) GetMemoryEntries() (perfValue []MemoryEntry, perfErr error) {
	defer logPageBinding("GetMemoryEntries", time.Now(), &perfValue)
	if a == nil {
		dir, err := memoryDir()
		if err != nil {
			return nil, err
		}
		return readMemoryEntries(dir)
	}
	snapshot, err := a.memorySnapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.rawEntries(), nil
}

// readMemoryEntries reads and parses every live note in dir, newest first.
func readMemoryEntries(dir string) ([]MemoryEntry, error) {
	// Preserve read errors so projections distinguish unavailable sources
	// from deleted records; a failed read is never a successful empty list.
	entries, err := memoryFiles.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	history := memoryHistoryIDs(dir)

	var result []MemoryEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || isOKFReservedMarkdownFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if os.IsNotExist(err) {
				// Deleted after enumeration.
				continue
			}
			return nil, err
		}
		data, err := memoryFiles.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				// Deleted after enumeration.
				continue
			}
			return nil, err
		}

		id := strings.TrimSuffix(e.Name(), ".md")
		content := string(data)
		doc := parseMemoryEntryDocument(content)
		title := doc.title()
		if title == "" {
			title = id
		}
		fm := doc.frontmatter

		result = append(result, MemoryEntry{
			ID:                 id,
			Title:              pii.DisplayText(title),
			Content:            content,
			ModTime:            info.ModTime().UnixMilli(),
			SizeBytes:          info.Size(),
			Dimension:          memoryDimensionFromFrontmatter(fm),
			Area:               memoryAreaOnReadDocument(doc),
			TopicIDs:           fm.TopicIDs,
			UpdatedAt:          fm.UpdatedAt,
			Source:             fm.Source,
			ActivitySessionIDs: memoryEvidenceSessionIDsFromDocument(doc),
			Apps:               fm.Apps,
			HasHistory:         history[id],
		})
	}

	// Sort by best-known update time: prefer frontmatter `Updated` (stable
	// across reindex / touch), fall back to filesystem modTime when absent.
	sort.Slice(result, func(i, j int) bool {
		ai := result[i].UpdatedAt
		if ai == 0 {
			ai = result[i].ModTime
		}
		aj := result[j].UpdatedAt
		if aj == 0 {
			aj = result[j].ModTime
		}
		return ai > aj
	})

	return result, nil
}

// GetMemoryEntry returns a single memory entry by ID. Returns os.ErrNotExist
// if no file matches. Used by the cron API /memory/show/:id endpoint.
func (a *App) GetMemoryEntry(id string) (MemoryEntry, error) {
	dir, err := memoryDir()
	if err != nil {
		return MemoryEntry{}, err
	}

	filename := id + ".md"
	if strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		return MemoryEntry{}, fmt.Errorf("invalid memory entry ID")
	}

	filePath := filepath.Join(dir, filename)
	info, err := os.Stat(filePath)
	if err != nil {
		return MemoryEntry{}, err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return MemoryEntry{}, err
	}

	content := string(data)
	title := extractTitle(content)
	if title == "" {
		title = id
	}
	fm := parseMemoryFrontmatter(content)

	return MemoryEntry{
		ID:                 id,
		Title:              pii.DisplayText(title),
		Content:            content,
		ModTime:            info.ModTime().UnixMilli(),
		SizeBytes:          info.Size(),
		Dimension:          extractMemoryDimension(content),
		Area:               memoryAreaOnRead(content, fm),
		TopicIDs:           fm.TopicIDs,
		UpdatedAt:          fm.UpdatedAt,
		Source:             fm.Source,
		ActivitySessionIDs: memoryEvidenceSessionIDs(content),
		displayApp:         a,
		Apps:               fm.Apps,
		HasHistory:         memoryEntryHasHistory(dir, id),
	}, nil
}

// SaveMemoryEntry creates or updates a memory markdown file.
//
// For an existing entry the write path is lock → snapshot → write, matching
// the activity triager's update path so GUI edits also produce version
// history and cannot race with automated updates. New entries skip the
// snapshot step (nothing to preserve).
func (a *App) SaveMemoryEntry(id string, content string) (MemoryEntry, error) {
	return a.saveMemoryEntry(id, content, true)
}

// saveMemoryEntry keeps machine-generated saves separate from user corrections.
func (a *App) saveMemoryEntry(id string, content string, userEdit bool) (MemoryEntry, error) {
	return a.saveMemoryEntryWithRewrite(id, content, userEdit, nil)
}

// A code-owned rewrite runs against the current file under the same lock as
// user edits. It preserves the full source envelope instead of interpreting a
// human attribution correction as a new observation or replacing metadata.
func (a *App) saveMemoryEntryWithRewrite(id string, content string, userEdit bool, rewrite func(string) string) (MemoryEntry, error) {
	if err := validateMemoryMetadata(content); err != nil {
		return MemoryEntry{}, err
	}
	dir, err := memoryDir()
	if err != nil {
		return MemoryEntry{}, err
	}

	isNew := id == ""
	if isNew {
		id = fmt.Sprintf("memory-%d", time.Now().UnixMilli())
	}

	filename := id + ".md"
	if strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		return MemoryEntry{}, fmt.Errorf("invalid memory entry ID")
	}

	if !userEdit {
		if err := a.prepareMemoryObservedWrite(dir, id, content); err != nil {
			return MemoryEntry{}, err
		}
	}
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()

	filePath := filepath.Join(dir, filename)
	existing, readErr := os.ReadFile(filePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return MemoryEntry{}, readErr
	}
	if rewrite != nil {
		if readErr != nil {
			return MemoryEntry{}, readErr
		}
		content = string(existing)
		if _, ok := parseOKFDocument(legacyMemoryWithoutConfirmation(content)); !ok {
			content = memoryWithResolvedProvenance(id, content, memoryReadProvenanceForFile(dir, id, content))
		}
		content = rewrite(content)
	} else if userEdit {
		content = userAuthoredMemoryMarkdown(id, content, string(existing))
	}
	if userEdit {
		content = markMemoryUserEdited(content)
	} else {
		content, err = preserveMemoryConfirmation(content, string(existing))
		if err != nil {
			return MemoryEntry{}, err
		}
	}
	if !userEdit {
		content = preserveMemoryTopics(content, string(existing))
	}
	if err := validateMemoryMetadata(content); err != nil {
		return MemoryEntry{}, err
	}
	if err := checkMemoryWriteAllowed(dir, id, content, !userEdit); err != nil {
		return MemoryEntry{}, err
	}
	unchanged := false
	if !isNew {
		switch true {
		case readErr == nil && (bytes.Equal(existing, []byte(content)) ||
			(userEdit && isUserEditedMemory(string(existing)) && memoryUserEditComparable(string(existing)) == memoryUserEditComparable(content))):
			unchanged = true
			content = string(existing)
		default:
			if err := snapshotMemoryFile(dir, id); err != nil {
				return MemoryEntry{}, err
			}
		}
	}
	if !unchanged {
		if userEdit {
			now := time.Now()
			content = memoryUserEditTimestamp(content, now.Format(time.RFC3339))
			content, err = setMemoryConfirmedAt(content, now)
			if err != nil {
				return MemoryEntry{}, err
			}
			// The person's own wording takes back Not quite.
			content = clearMemoryNotQuite(content)
		}
		if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
			return MemoryEntry{}, err
		}

		// Mark memory store dirty (the file watcher will pick it up,
		// but marking dirty ensures immediate re-index on next search)
		a.markMemoryDirty("save")
	}

	title := extractTitle(content)
	if title == "" {
		title = id
	}

	info, _ := os.Stat(filePath)
	var modTime int64
	var sizeBytes int64
	if info != nil {
		modTime = info.ModTime().UnixMilli()
		sizeBytes = info.Size()
	}
	fm := parseMemoryFrontmatter(content)

	return MemoryEntry{
		ID:                 id,
		Title:              pii.DisplayText(title),
		Content:            content,
		ModTime:            modTime,
		SizeBytes:          sizeBytes,
		Dimension:          extractMemoryDimension(content),
		Area:               memoryAreaOnRead(content, fm),
		TopicIDs:           fm.TopicIDs,
		UpdatedAt:          fm.UpdatedAt,
		Source:             fm.Source,
		ActivitySessionIDs: memoryEvidenceSessionIDs(content),
		displayApp:         a,
		Apps:               fm.Apps,
		HasHistory:         memoryEntryHasHistory(dir, id),
	}, nil
}

// ReadMemoryFileDimension reads a memory file (by path relative to the memory
// directory, e.g. "memory-1234.md") and returns its dimension header. Returns
// empty string if the file can't be read or no valid dimension is found.
func ReadMemoryFileDimension(path string) string {
	dir, err := memoryDir()
	if err != nil {
		return ""
	}
	if strings.Contains(path, "..") {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		return ""
	}
	return extractMemoryDimension(string(data))
}

// DeleteMemoryEntry removes a memory markdown file by ID.
//
// Also clears the entry's version history directory: leaving snapshots behind
// would contradict the "delete" verb (old content resurfacing via the history
// drawer) and could re-leak data through search once the snapshot tree gets
// re-indexed. If history removal fails we still report success — the live
// file is gone, and orphaned history is a background cleanup concern, not a
// blocking one.
func (a *App) DeleteMemoryEntry(id string) error {
	deleted := false
	defer func() {
		if deleted {
			publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "memory", ID: id})
		}
	}()
	dir, err := memoryDir()
	if err != nil {
		return err
	}

	filename := id + ".md"
	if strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		return fmt.Errorf("invalid memory entry ID")
	}

	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	withdrawal, err := a.topicWithdrawalForSource("memory", []string{id}, nil)
	if err != nil {
		return err
	}

	if a.store != nil {
		if err := a.store.DeleteContextUses(context.Background(), "memory", id); err != nil {
			return err
		}
	}
	if err := a.withTopicWithdrawal(withdrawal, func() error {
		err := os.Remove(filepath.Join(dir, filename))
		deleted = err == nil
		return err
	}); err != nil {
		return err
	}
	// Invalidate reads begun between the first erasure and unlink. A writer
	// recreating this ID acquires the same file lock after this operation.
	if a.store != nil {
		if err := a.store.DeleteContextUses(context.Background(), "memory", id); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, memoryHistoryDirName, id)); err != nil {
		return fmt.Errorf("remove memory history: %w", err)
	}

	a.markMemoryDirty("delete")

	if a.telemetry != nil {
		a.telemetry.Capture("memory.deleted", map[string]any{
			"entry_id": id,
		})
	}

	return nil
}

// memoryRejectionsLogName records legacy and phone subject corrections under
// memoryDir(). The reviewed Mac forget flow removes matching rows and does not
// retain the rejected statement or reason here.
const memoryRejectionsLogName = ".rejections.jsonl"

// MemoryRejectionEntry is one row of the rejections log. Fields are stable
// so future tools (telemetry, "what patterns get rejected most" reports,
// P4 auto-learning from corrections) can consume it without re-parsing
// the deleted markdown.
type MemoryRejectionEntry struct {
	MemoryID           string   `json:"memoryId"`
	Title              string   `json:"title,omitempty"`
	Source             string   `json:"source,omitempty"`
	Dimension          string   `json:"dimension,omitempty"`
	ActivitySessionIDs []string `json:"activitySessionIds,omitempty"`
	Reason             string   `json:"reason"`
	RejectedAt         int64    `json:"rejectedAt"`
}

// RejectMemoryAttribution is retained for existing correction callers. Rejection
// uses the same suppression and deletion cascade as an explicit forget. The
// desktop calls the preview-bound variant after showing the complete scope.
func (a *App) RejectMemoryAttribution(id string, reason string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("RejectMemoryAttribution: empty id")
	}
	preview, err := a.GetMemoryForgetPreview(id)
	if err != nil {
		return fmt.Errorf("load entry %s: %w", id, err)
	}
	return a.RejectMemoryAttributionWithPreview(id, reason, preview.ConfirmationToken)
}

// RejectMemoryAttributionWithPreview deliberately retains neither the rejected
// statement nor the free-form reason: either can repeat the forgotten text.
func (a *App) RejectMemoryAttributionWithPreview(id, reason, confirmationToken string) error {
	return a.ForgetMemoryEntry(id, confirmationToken)
}

// memoryRejectionLogError remains for compatibility with older correction
// callers that distinguished a completed deletion from a failed audit write.
type memoryRejectionLogError struct{ err error }

func (e memoryRejectionLogError) Error() string { return e.err.Error() }

func (e memoryRejectionLogError) Unwrap() error { return e.err }

// appendMemoryRejection writes one JSON object per line to the rejections
// log. JSON encoder handles all escaping (newlines, quotes, CJK) so the
// log stays grep-able and trivial to consume with `jq -c`.
func appendMemoryRejection(dir string, rec MemoryRejectionEntry) error {
	path := filepath.Join(dir, memoryRejectionsLogName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("encode rejection: %w", err)
	}
	return nil
}

// memoryReassignmentsLogName is the sister of .rejections.jsonl — every
// "this memory is real but belongs to someone else" correction lands here
// in append-only JSON-lines form. Future learning passes (P4 auto-skip)
// can mine this in tandem with the rejection log.
const memoryReassignmentsLogName = ".reassignments.jsonl"

// MemoryReassignmentEntry records a subject reassignment so the offline
// learning loop knows what was reattributed to whom and why.
type MemoryReassignmentEntry struct {
	MemoryID             string   `json:"memoryId"`
	Title                string   `json:"title,omitempty"`
	Source               string   `json:"source,omitempty"`
	OriginalDimension    string   `json:"originalDimension,omitempty"`
	CorrectPrincipalID   string   `json:"correctPrincipalId"`
	CorrectPrincipalName string   `json:"correctPrincipalName,omitempty"`
	CorrectPrincipalKind string   `json:"correctPrincipalKind,omitempty"`
	ActivitySessionIDs   []string `json:"activitySessionIds,omitempty"`
	Reason               string   `json:"reason"`
	ReassignedAt         int64    `json:"reassignedAt"`
}

// ReassignMemoryAttribution rewrites a memory entry so it is attributed to
// the named Principal instead of the local user, then appends a structured
// record to .reassignments.jsonl. The use case: Yansu correctly captured a
// statement, but credited the wrong person — e.g. a chat preference said by
// a friend was recorded as if the user said it. Rather than deleting the
// signal entirely (RejectMemoryAttribution), the user can redirect it to
// the actual speaker.
//
// Concretely the rewrite:
//   - Replaces the entry title with the corrected principal's name as the
//     subject (e.g. "Zhang San likes chestnuts" instead of "I like
//     chestnuts").
//   - Forces dimension = "topic_highlight" so the rewritten point can never
//     be re-promoted to a user_insight even if a search returns it.
//   - Inserts a "Reassigned to: <name> (id=<id>) — <reason>" line in the
//     frontmatter so the provenance is auditable on disk.
//   - Replaces user-first-person phrasing ("I ..." / "My ...") in memory
//     points with third-person framing keyed on the corrected name. This is
//     intentionally a textual best-effort: the model can still produce
//     other voice forms, but the dimension cap and "Reassigned to:" header
//     are the load-bearing safety net.
//
// Reassigning to a non-existent Principal returns an error so the UI can
// surface a clean toast. Reassigning to the self principal is allowed
// (interpreted as "undo a previous reassignment"); the writeup is still
// rewritten so callers can preview the resulting voice change.
func (a *App) ReassignMemoryAttribution(memoryID, correctPrincipalID, reason string) error {
	if strings.TrimSpace(memoryID) == "" {
		return fmt.Errorf("ReassignMemoryAttribution: empty memoryID")
	}
	if strings.TrimSpace(correctPrincipalID) == "" {
		return fmt.Errorf("ReassignMemoryAttribution: empty correctPrincipalID")
	}

	store := a.store
	if store == nil {
		return fmt.Errorf("ReassignMemoryAttribution: storage not initialized")
	}

	entry, err := a.GetMemoryEntry(memoryID)
	if err != nil {
		return fmt.Errorf("load entry %s: %w", memoryID, err)
	}

	target, err := store.GetPrincipal(context.Background(), correctPrincipalID)
	if err != nil {
		return fmt.Errorf("load target principal %s: %w", correctPrincipalID, err)
	}
	targetName := strings.TrimSpace(target.DisplayName)
	if targetName == "" {
		targetName = target.ID
	}

	if _, err := a.saveMemoryEntryWithRewrite(memoryID, "", true, func(current string) string {
		return rewriteMemoryForReassignment(current, targetName, target.ID, strings.TrimSpace(reason))
	}); err != nil {
		return fmt.Errorf("save rewritten entry: %w", err)
	}

	dir, err := memoryDir()
	if err != nil {
		return fmt.Errorf("locate memory dir for reassignment log: %w", err)
	}
	rec := MemoryReassignmentEntry{
		MemoryID:             memoryID,
		Title:                entry.Title,
		Source:               entry.Source,
		OriginalDimension:    entry.Dimension,
		CorrectPrincipalID:   target.ID,
		CorrectPrincipalName: targetName,
		CorrectPrincipalKind: target.Kind,
		ActivitySessionIDs:   entry.ActivitySessionIDs,
		Reason:               strings.TrimSpace(reason),
		ReassignedAt:         time.Now().UnixMilli(),
	}
	if err := appendMemoryReassignment(dir, rec); err != nil {
		return fmt.Errorf("append reassignment log: %w", err)
	}
	a.markMemoryDirty("reassign")
	return nil
}

// rewriteMemoryForReassignment textually rewrites a memory markdown file so
// the corrected principal becomes the subject. Strategy:
//   - Set the dimension frontmatter line to "topic_highlight" (insert if
//     missing — older entries might lack it).
//   - Insert a "Reassigned to: <name> (id=<id>) — <reason>" frontmatter
//     line right after the Dimension line so it stays close to the other
//     attribution metadata.
//   - Strip a leading "# " title that contains first-person phrasing and
//     replace it with a third-person framing rooted in the corrected name.
//   - Rewrite memory bullet lines: first-person clauses ("I ...", "My ...",
//     "我 ...", "我的 ...") become "<name> said: ..." — preserving the
//     surface form rather than trying to fully translate, which keeps the
//     rewrite reversible if the user inspects the on-disk file.
//
// The rewrite is forgiving: if the input doesn't match expected patterns
// the function still inserts the frontmatter header so the provenance is
// recorded, and leaves the body intact for the user to clean up manually.
func rewriteMemoryForReassignment(content, targetName, targetID, reason string) string {
	if doc, ok := parseOKFDocument(content); ok {
		// Rewrite only attribution fields and the body. In particular, Dream
		// dependencies and future provenance fields must stay inside YAML.
		doc.Frontmatter["title"] = fmt.Sprintf("%s — reassigned to %s", okfString(doc.Frontmatter, "title"), targetName)
		doc.Frontmatter["dimension"] = "topic_highlight"
		doc.Body = rewriteMemoryForReassignment(doc.Body, targetName, targetID, reason)
		return renderOKFDocument(doc.Frontmatter, doc.Body)
	}
	dimensionLine := "- **Dimension**: topic_highlight"
	reassignLine := fmt.Sprintf("- **Reassigned to**: %s (id=%s)", targetName, targetID)
	if reason != "" {
		reassignLine = fmt.Sprintf("%s — %s", reassignLine, reason)
	}

	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines)+2)
	dimensionFound := false
	reassignmentInserted := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch true {
		case strings.HasPrefix(trimmed, "- **Dimension**:"):
			out = append(out, dimensionLine)
			dimensionFound = true
		case strings.HasPrefix(trimmed, "- **Reassigned to**:"):
			// Drop any prior reassignment line so the log stays single-source-of-truth.
			continue
		case strings.HasPrefix(line, "# ") && i == 0:
			out = append(out, fmt.Sprintf("# %s — reassigned to %s", strings.TrimSpace(strings.TrimPrefix(line, "# ")), targetName))
		default:
			out = append(out, rewriteMemoryBulletForReassignment(line, targetName))
		}
		if dimensionFound && !reassignmentInserted {
			out = append(out, reassignLine)
			reassignmentInserted = true
		}
	}
	if !dimensionFound {
		// Entry lacked a Dimension frontmatter line entirely — append both
		// the dimension cap and the reassignment header before the first
		// blank line so they sit with the other metadata. As a fallback
		// we just prepend them after the title.
		insertAt := 0
		for i, line := range out {
			if strings.HasPrefix(line, "# ") {
				insertAt = i + 1
				continue
			}
			if strings.TrimSpace(line) == "" && insertAt > 0 {
				break
			}
			if strings.HasPrefix(line, "- **") {
				insertAt = i + 1
			}
		}
		injected := []string{dimensionLine, reassignLine}
		out = append(out[:insertAt], append(injected, out[insertAt:]...)...)
	}
	return strings.Join(out, "\n")
}

// rewriteMemoryBulletForReassignment rewrites first-person phrasing inside
// a bullet line so the corrected principal becomes the speaker. Conservative:
// only matches "- I ...", "- My ...", "- 我 ...", "- 我的 ..." at the start
// of a bullet — leaves everything else intact so the user can decide how to
// rewrite less obvious cases manually.
func rewriteMemoryBulletForReassignment(line, targetName string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "- ") {
		return line
	}
	indent := line[:len(line)-len(trimmed)]
	body := strings.TrimPrefix(trimmed, "- ")
	prefixes := []struct {
		old string
		new string
	}{
		{"I ", fmt.Sprintf("%s said: I ", targetName)},
		{"My ", fmt.Sprintf("%s said: My ", targetName)},
		{"我 ", fmt.Sprintf("%s 说:我 ", targetName)},
		{"我的 ", fmt.Sprintf("%s 说:我的 ", targetName)},
		{"我", fmt.Sprintf("%s 说:我", targetName)}, // bare "我" without space (CJK)
	}
	for _, p := range prefixes {
		if strings.HasPrefix(body, p.old) {
			return indent + "- " + p.new + strings.TrimPrefix(body, p.old)
		}
	}
	return line
}

func appendMemoryReassignment(dir string, rec MemoryReassignmentEntry) error {
	path := filepath.Join(dir, memoryReassignmentsLogName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("encode reassignment: %w", err)
	}
	return nil
}

// SearchMemory performs FTS5 full-text search across all memory chunks.
// Triggers on-search sync if the index is dirty (like openclaw).
func (a *App) SearchMemory(query string, limit int) ([]MemorySearchResult, error) {
	a.mu.Lock()
	store := a.memoryStore
	a.mu.Unlock()

	if store == nil || a.IsClosedEyesModeEnabled() {
		return nil, nil
	}

	searchStart := time.Now()
	if limit <= 0 {
		limit = memoryDefaultMaxResults
	}
	allowRead, err := a.memoryReadFilter()
	if err != nil {
		return nil, err
	}
	results, err := store.searchReadable(context.Background(), query, limit, allowRead)
	for i := range results {
		results[i].Snippet = pii.DisplayText(results[i].Snippet)
	}
	if err == nil && a.telemetry != nil {
		a.telemetry.Capture("memory.searched", map[string]any{
			"results_count": len(results),
			"duration_ms":   time.Since(searchStart).Milliseconds(),
		})
	}
	return results, err
}

// SyncMemoryIndex triggers a re-index of all memory files.
func (a *App) SyncMemoryIndex() error {
	a.mu.Lock()
	store := a.memoryStore
	a.mu.Unlock()

	if store == nil {
		return nil
	}

	if err := store.Sync("manual"); err != nil {
		return err
	}

	if a.ctx != nil {
		a.emitBrowserEvent("memory:updated")
	}
	return nil
}

// GetMemoryStatus returns the current memory index status.
func (a *App) GetMemoryStatus() (perfValue MemoryStoreStatus) {
	defer logPageBinding("GetMemoryStatus", time.Now(), &perfValue)
	a.mu.Lock()
	store := a.memoryStore
	a.mu.Unlock()

	if store == nil {
		return MemoryStoreStatus{}
	}

	return store.Status()
}

// SaveMemoryDiary appends content to today's diary file (memory/YYYY-MM-DD.md).
// Like openclaw's dated memory files with temporal decay.
func (a *App) SaveMemoryDiary(content string) error {
	a.mu.Lock()
	store := a.memoryStore
	a.mu.Unlock()

	if store == nil {
		return fmt.Errorf("memory store not initialized")
	}

	return store.SaveDiaryEntry(content, func(id, value string) error {
		return a.prepareMemoryObservedWrite(store.baseDir, id, value)
	})
}

// GetMemoryConfig returns the current memory store configuration.
func (a *App) GetMemoryConfig() MemoryStoreConfig {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return MemoryStoreConfig{}
	}
	return loadMemoryStoreConfig(dataRoot)
}

// SaveMemoryConfig saves the memory store configuration and reinitializes the store.
func (a *App) SaveMemoryConfig(cfg MemoryStoreConfig) error {
	cfgDir, err := flavorDataDir()
	if err != nil {
		return err
	}
	if err := saveMemoryStoreConfig(cfgDir, cfg); err != nil {
		return err
	}

	// Reinitialize memory store with new config
	a.mu.Lock()
	oldStore := a.memoryStore
	a.mu.Unlock()

	if oldStore != nil {
		oldStore.Close()
	}

	ms, err := NewMemoryStore(cfgDir, cfg, a.emitDownloadProgress)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.memoryStore = ms
	a.mu.Unlock()

	ms.SetOnUpdated(func() {
		if a.ctx != nil {
			a.emitBrowserEvent("memory:updated", map[string]string{"reason": "index"})
		}
	})
	go ms.Run(a.ctx)

	return nil
}

// emitDownloadProgress emits an embedding download progress event to the frontend.
func (a *App) emitDownloadProgress(fileName string, downloaded, total int64, bytesPerSec float64) {
	if a.ctx == nil {
		return
	}
	a.emitBrowserEvent("embedding:download-progress", map[string]any{
		"fileName":    fileName,
		"downloaded":  downloaded,
		"total":       total,
		"bytesPerSec": bytesPerSec,
	})
}

// TestEmbeddingResult contains the result of a test embedding operation.
type TestEmbeddingResult struct {
	Success    bool   `json:"success"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	LatencyMs  int64  `json:"latencyMs"`
	Error      string `json:"error,omitempty"`
}

// TestEmbedding tests the given embedding configuration by building a transient
// embedder from cfg and embedding a short string. The config is NOT persisted —
// this is intended to validate user input in the settings UI before saving.
func (a *App) TestEmbedding(cfg MemoryStoreConfig) TestEmbeddingResult {
	cfgDir, err := flavorDataDir()
	if err != nil {
		return TestEmbeddingResult{Error: err.Error()}
	}

	embedder := resolveEmbeddingProvider(cfgDir, cfg, a.emitDownloadProgress)
	if embedder == nil {
		return TestEmbeddingResult{Error: "No embedding provider configured. Vector search is disabled."}
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vec, err := embedder.EmbedQuery(ctx, "hello world")
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return TestEmbeddingResult{
			Provider:  embedder.Provider(),
			Model:     embedder.Model(),
			LatencyMs: latency,
			Error:     err.Error(),
		}
	}

	return TestEmbeddingResult{
		Success:    true,
		Provider:   embedder.Provider(),
		Model:      embedder.Model(),
		Dimensions: len(vec),
		LatencyMs:  latency,
	}
}

// markMemoryDirty marks the memory index as needing re-sync and notifies frontend.
func (a *App) markMemoryDirty(reason string) {
	a.markMemoryDirtyWithCorrelation(reason, "")
}

func (a *App) markMemoryDirtyWithCorrelation(reason, correlation string) {
	a.invalidateMemoryExternalReads()
	a.mu.Lock()
	store := a.memoryStore
	a.mu.Unlock()

	if store != nil {
		store.MarkDirty(reason)
	}
	// The person's own edit rebuilds About you and How you do things before
	// the page re-reads them; background learning waits for the daily build.
	edit := derivedMemoryReasonIsEdit(reason)
	if edit {
		derivedMemoryEditGeneration.Add(1)
		a.derivedViews().wake()
	}

	if a.ctx != nil {
		payload := map[string]string{"reason": reason}
		if correlation != "" {
			payload["correlation"] = correlation
		}
		a.emitBrowserEvent("memory:updated", payload)
	}
	// Background learning no longer wakes the portrait writer for each
	// belief: its own tick decides. Only the first portrait, and the
	// person's edits, wake it at once.
	if edit || !portraitWritten() {
		a.portraitMemoryChanged()
	}
}

// MemoryVersion describes a single historical snapshot of a memory file. The
// snapshot is read-only — the UI is expected to surface versions for review,
// not for rollback.
type MemoryVersion struct {
	// TimestampMs is the wall-clock UnixMilli captured at snapshot-write time
	// (i.e. right before the live file gets overwritten), not the live file's
	// prior mtime. Close enough for display, and it doubles as the filename
	// stem (`<TimestampMs>.md`) and the key passed to GetMemoryVersion.
	TimestampMs int64 `json:"timestampMs"`
	// SizeBytes is the snapshot file size, exposed so the UI can render a
	// compact history list without having to read every snapshot body.
	SizeBytes int64 `json:"sizeBytes"`
}

// ListMemoryVersions returns every historical snapshot for a memory entry,
// newest first. Returns an empty slice when the entry has no history yet (no
// updates applied, or it was created before version history shipped) — NOT
// an error, so the frontend can render "no history" uniformly.
func (a *App) ListMemoryVersions(id string) ([]MemoryVersion, error) {
	if id == "" {
		return nil, fmt.Errorf("memory id is required")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return nil, fmt.Errorf("invalid memory id")
	}
	dir, err := memoryDir()
	if err != nil {
		return nil, err
	}
	historyDir := filepath.Join(dir, memoryHistoryDirName, id)
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []MemoryVersion{}, nil
		}
		return nil, fmt.Errorf("read history dir: %w", err)
	}

	versions := make([]MemoryVersion, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		// Require stem to be a non-empty run of ASCII digits — strconv.ParseInt
		// would otherwise accept "+123" or "-1", which we'd then try to read
		// back as "123.md" / "-1.md" and fail with "version not found".
		if stem == "" || !isAllDigits(stem) {
			continue
		}
		ts, parseErr := strconv.ParseInt(stem, 10, 64)
		if parseErr != nil {
			// Unknown file shape — skip rather than error out so a stray
			// file doesn't break the whole list.
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		versions = append(versions, MemoryVersion{
			TimestampMs: ts,
			SizeBytes:   info.Size(),
		})
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].TimestampMs > versions[j].TimestampMs
	})
	return versions, nil
}

// GetMemoryVersion returns the raw markdown content of a specific historical
// snapshot. Callers should pass timestampMs from a prior ListMemoryVersions
// result; passing an unknown timestamp returns an error.
func (a *App) GetMemoryVersion(id string, timestampMs int64) (string, error) {
	if id == "" {
		return "", fmt.Errorf("memory id is required")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return "", fmt.Errorf("invalid memory id")
	}
	if timestampMs <= 0 {
		return "", fmt.Errorf("invalid timestamp")
	}
	dir, err := memoryDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, memoryHistoryDirName, id, fmt.Sprintf("%d.md", timestampMs))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("version not found")
		}
		return "", fmt.Errorf("read version: %w", err)
	}
	return string(data), nil
}

// Explicit transformations keep the original record's provenance. They are
// not new observations and do not need a semantic model to authorize a save.
// Preserve the exact resulting record in active forget markers so subsequent
// reads obey the same decision; erased identities and versions still win.
// Caller holds the mutation and per-entry locks and has revalidated the source.
func authorizeMemoryHumanMutation(dir, id, previous, result string) error {
	memoryTombstoneMu.Lock()
	defer memoryTombstoneMu.Unlock()
	tombstones, err := loadMemoryTombstones(dir)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	changed := false
	for i := range tombstones {
		marker := &tombstones[i]
		if marker.ExpiresAt <= now {
			continue
		}
		if memoryTombstoneIdentityMatch(*marker, id, previous) || memoryTombstoneIdentityMatch(*marker, id, result) {
			return errMemoryForgotten
		}
		if !memoryTombstonePreserves(*marker, id, result) {
			marker.PreservedRecords = append(marker.PreservedRecords, memoryPreservedRecordKey(id, result))
			sort.Strings(marker.PreservedRecords)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return persistMemoryTombstones(dir, tombstones)
}

// TranslateMemoryEntryToCurrentLanguage explicitly translates the live memory
// entry to the current UI language. It snapshots the current file before
// overwriting so the pre-translation content remains available in history.
func (a *App) TranslateMemoryEntryToCurrentLanguage(id string) (MemoryEntry, error) {
	return a.translateMemoryEntryToLanguage(id, a.currentUILanguageName())
}

func (a *App) translateMemoryEntryToLanguage(id, languageName string) (MemoryEntry, error) {
	if id == "" {
		return MemoryEntry{}, fmt.Errorf("memory id is required")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return MemoryEntry{}, fmt.Errorf("invalid memory id")
	}
	dir, err := memoryDir()
	if err != nil {
		return MemoryEntry{}, err
	}
	filePath := filepath.Join(dir, id+".md")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return MemoryEntry{}, fmt.Errorf("memory entry not found")
		}
		return MemoryEntry{}, fmt.Errorf("read memory: %w", err)
	}
	translated, err := a.translateMarkdownToLanguage("memory", string(data), languageName)
	if err != nil {
		return MemoryEntry{}, err
	}

	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()

	current, err := os.ReadFile(filePath)
	if err != nil {
		return MemoryEntry{}, err
	}
	if !bytes.Equal(current, data) {
		return MemoryEntry{}, fmt.Errorf("memory changed while translating; try again")
	}
	if err := authorizeMemoryHumanMutation(dir, id, string(data), translated); err != nil {
		return MemoryEntry{}, err
	}
	if err := snapshotMemoryFile(dir, id); err != nil {
		return MemoryEntry{}, err
	}
	if err := os.WriteFile(filePath, []byte(translated), 0600); err != nil {
		return MemoryEntry{}, fmt.Errorf("write translated memory: %w", err)
	}
	a.markMemoryDirty("translate")
	return a.GetMemoryEntry(id)
}

// TranslateMemoryVersionToCurrentLanguage translates a read-only historical
// snapshot and writes the translated result as the live memory entry. The
// existing live entry is snapshotted first; the historical snapshot itself is
// never modified.
func (a *App) TranslateMemoryVersionToCurrentLanguage(id string, timestampMs int64) (MemoryEntry, error) {
	content, err := a.GetMemoryVersion(id, timestampMs)
	if err != nil {
		return MemoryEntry{}, err
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return MemoryEntry{}, fmt.Errorf("invalid memory id")
	}
	dir, err := memoryDir()
	if err != nil {
		return MemoryEntry{}, err
	}
	filePath := filepath.Join(dir, id+".md")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return MemoryEntry{}, fmt.Errorf("memory entry not found")
		}
		return MemoryEntry{}, err
	}
	translated, err := a.translateMarkdownToCurrentUILanguage("memory history snapshot", content)
	if err != nil {
		return MemoryEntry{}, err
	}

	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()

	current, err := os.ReadFile(filePath)
	if err != nil {
		return MemoryEntry{}, err
	}
	if !bytes.Equal(current, data) {
		return MemoryEntry{}, fmt.Errorf("memory changed while translating; try again")
	}
	selected, err := a.GetMemoryVersion(id, timestampMs)
	if err != nil {
		return MemoryEntry{}, err
	}
	if selected != content {
		return MemoryEntry{}, fmt.Errorf("memory history changed while translating; try again")
	}
	if err := authorizeMemoryHumanMutation(dir, id, content, translated); err != nil {
		return MemoryEntry{}, err
	}
	if err := snapshotMemoryFile(dir, id); err != nil {
		return MemoryEntry{}, err
	}
	if err := os.WriteFile(filePath, []byte(translated), 0600); err != nil {
		return MemoryEntry{}, fmt.Errorf("write translated memory snapshot: %w", err)
	}
	a.markMemoryDirty("translate-version")
	return a.GetMemoryEntry(id)
}

func (a *App) translateAllMemoryVersionsToLanguage(id, languageName string) ([]MemoryVersion, error) {
	if id == "" {
		return nil, fmt.Errorf("memory id is required")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return nil, fmt.Errorf("invalid memory id")
	}
	dir, err := memoryDir()
	if err != nil {
		return nil, err
	}
	versions, err := a.ListMemoryVersions(id)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return versions, nil
	}

	historyDir := filepath.Join(dir, memoryHistoryDirName, id)
	for _, version := range versions {
		path := filepath.Join(historyDir, fmt.Sprintf("%d.md", version.TimestampMs))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read memory history snapshot: %w", err)
		}
		translated, err := a.translateMarkdownToLanguage("memory history snapshot", string(data), languageName)
		if err != nil {
			return nil, err
		}
		if err := replaceTranslatedMemoryHistory(dir, id, path, data, translated); err != nil {
			return nil, err
		}
	}
	a.markMemoryDirty("translate-all-history")
	return a.ListMemoryVersions(id)
}

// Replace only the historical bytes that were actually translated, after the
// network call finishes. A concurrent forget removes the file and wins here.
func replaceTranslatedMemoryHistory(dir, id, path string, previous []byte, translated string) error {
	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, previous) {
		return fmt.Errorf("memory history changed while translating; try again")
	}
	if err := authorizeMemoryHumanMutation(dir, id, string(previous), translated); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(translated), 0600)
}

func (a *App) translateMarkdownToCurrentUILanguage(kind, markdown string) (string, error) {
	return a.translateMarkdownToLanguage(kind, markdown, a.currentUILanguageName())
}

func (a *App) normalizeGeneratedMarkdownToCurrentUILanguage(kind, markdown string) string {
	if a == nil {
		return markdown
	}
	translated, err := a.translateMarkdownToCurrentUILanguage(kind, markdown)
	if err != nil {
		log.Printf("[memory] failed to normalize generated %s markdown to current UI language: %s", kind, activityPrivateLogField("error", err.Error()))
		return markdown
	}
	return translated
}

func (a *App) translateMarkdownToLanguage(kind, markdown, languageName string) (string, error) {
	languageName = strings.TrimSpace(languageName)
	if languageName == "" {
		languageName = "English"
	}
	parts := splitMarkdownTranslationParts(markdown)
	markdownForTranslation := markdown
	if parts.HasMetadata {
		markdownForTranslation = parts.TranslatableMarkdown()
	}
	prompt := fmt.Sprintf(`Translate this %s Markdown entry into %s.

Rules:
- Return ONLY the translated Markdown. No code fence, no explanation.
- Preserve Markdown structure.
- Metadata has already been removed from the input. Do not add metadata lines such as "- **Updated**:", "- **Date**:", "- **Source**:", "- **Dimension**:", "- **Apps**:", or "- **Activity Session**:".
- Preserve machine-readable values exactly when changing them could break parsing, including source names, dimension keys, activity session IDs, timestamps, file paths, URLs, commands, code identifiers, and fenced code blocks.
- Preserve section headings used by the app exactly, including "## Memory Points", "## Key Insights", and "## Description".
- Translate human-facing prose: the top-level title and prose/list-item text that is not one of the preserved machine-readable values.

Markdown:
---
%s
---`, kind, languageName, markdownForTranslation)

	out, err := a.runEphemeralClaudeModel(prompt, a.lightTaskModel(), 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("translate %s to %s: %w", kind, languageName, err)
	}
	out = strings.TrimSpace(stripCodeFences(out))
	if out == "" {
		return "", fmt.Errorf("translation returned empty content")
	}
	if parts.HasMetadata {
		out = parts.Recombine(out)
	}
	return out + "\n", nil
}

type markdownTranslationParts struct {
	ConfirmationPrefix string
	TitleLine          string
	Metadata           []string
	Body               string
	HasTitle           bool
	HasMetadata        bool
	IsOKF              bool
	OKFMeta            map[string]any
}

func (p markdownTranslationParts) TranslatableMarkdown() string {
	var b strings.Builder
	if p.HasTitle {
		b.WriteString(strings.TrimSpace(p.TitleLine))
		b.WriteString("\n\n")
	}
	b.WriteString(strings.TrimSpace(p.Body))
	return strings.TrimSpace(b.String()) + "\n"
}

func (p markdownTranslationParts) Recombine(translated string) string {
	title, body := splitTranslatedTitleAndBody(translated, p.TitleLine)
	body = stripLeadingMarkdownMetadataBlock(body)
	body = trimTranslationEnvelopeLines(body)
	if p.IsOKF {
		fm := make(map[string]any, len(p.OKFMeta))
		for k, v := range p.OKFMeta {
			fm[k] = v
		}
		if strings.TrimSpace(title) != "" {
			fm["title"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(title), "# "))
		}
		return strings.TrimSpace(renderOKFDocument(fm, body))
	}

	var b strings.Builder
	if strings.TrimSpace(title) != "" {
		b.WriteString(strings.TrimSpace(title))
		b.WriteString("\n\n")
	}
	if len(p.Metadata) > 0 {
		b.WriteString(strings.Trim(strings.Join(p.Metadata, "\n"), "\n"))
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(body) != "" {
		b.WriteString(strings.TrimSpace(body))
	}
	return strings.TrimSpace(p.ConfirmationPrefix + b.String())
}

func splitMarkdownTranslationParts(markdown string) markdownTranslationParts {
	legacy := legacyMemoryWithoutConfirmation(markdown)
	prefix := ""
	if legacy != markdown {
		prefix = markdown[:len(markdown)-len(legacy)]
	}
	markdown = legacy
	if doc, ok := parseOKFDocument(markdown); ok {
		parts := markdownTranslationParts{
			Body:        doc.Body,
			HasMetadata: true,
			IsOKF:       true,
			OKFMeta:     doc.Frontmatter,
		}
		if title := okfString(doc.Frontmatter, "title"); title != "" {
			parts.TitleLine = "# " + title
			parts.HasTitle = true
		}
		return parts
	}
	lines := strings.Split(markdown, "\n")
	i := 0
	for i < len(lines) && isIgnorableTranslationEnvelopeLine(lines[i]) {
		i++
	}

	parts := markdownTranslationParts{ConfirmationPrefix: prefix, HasMetadata: prefix != ""}
	if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "# ") {
		parts.TitleLine = strings.TrimSpace(lines[i])
		parts.HasTitle = true
		i++
	}

	metadataStart := i
	hasMetadata := false
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if isIgnorableTranslationEnvelopeLine(line) {
			i++
			continue
		}
		if isMarkdownMetadataLine(trimmed) {
			hasMetadata = true
			i++
			continue
		}
		if hasMetadata && isIndentedMarkdownBullet(line) {
			i++
			continue
		}
		break
	}
	if hasMetadata {
		parts.Metadata = append([]string(nil), lines[metadataStart:i]...)
		parts.HasMetadata = true
	}
	parts.Body = strings.Join(lines[i:], "\n")
	return parts
}

func splitTranslatedTitleAndBody(markdown, fallbackTitle string) (string, string) {
	lines := strings.Split(strings.TrimSpace(markdown), "\n")
	i := 0
	for i < len(lines) && isIgnorableTranslationEnvelopeLine(lines[i]) {
		i++
	}
	if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "# ") {
		return strings.TrimSpace(lines[i]), strings.Join(lines[i+1:], "\n")
	}
	return fallbackTitle, strings.Join(lines[i:], "\n")
}

func stripLeadingMarkdownMetadataBlock(markdown string) string {
	lines := strings.Split(markdown, "\n")
	i := 0
	hasMetadata := false
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if isIgnorableTranslationEnvelopeLine(line) {
			i++
			continue
		}
		if isMarkdownMetadataLine(trimmed) {
			hasMetadata = true
			i++
			continue
		}
		if hasMetadata && isIndentedMarkdownBullet(line) {
			i++
			continue
		}
		break
	}
	if !hasMetadata {
		return markdown
	}
	return strings.Join(lines[i:], "\n")
}

func trimTranslationEnvelopeLines(markdown string) string {
	lines := strings.Split(markdown, "\n")
	start := 0
	for start < len(lines) && isIgnorableTranslationEnvelopeLine(lines[start]) {
		start++
	}
	end := len(lines)
	for end > start && isIgnorableTranslationEnvelopeLine(lines[end-1]) {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

func isMarkdownMetadataLine(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "- **") {
		return false
	}
	rest := strings.TrimPrefix(trimmed, "- **")
	end := strings.Index(rest, "**:")
	return end > 0
}

func isIgnorableTranslationEnvelopeLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || trimmed == "---"
}

func isIndentedMarkdownBullet(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	return trimmed != line && strings.HasPrefix(trimmed, "- ")
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
