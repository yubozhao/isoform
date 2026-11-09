package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/pii"
)

const knowledgeHistoryDirName = ".history"

// KnowledgeEntry represents a single knowledge file for the frontend.
//
// SessionIDs lists every activity session that contributed to this entry's
// current content. The file format uses a nested bullet list; legacy files
// with a single-line `- **Activity Session**: <id>` still produce a
// one-element slice so jump affordances keep working.
type KnowledgeEntry struct {
	ID         string   `json:"id"`         // filename without extension
	Title      string   `json:"title"`      // extracted from markdown heading
	Date       string   `json:"date"`       // extracted from metadata
	Apps       string   `json:"apps"`       // extracted from metadata
	SessionIDs []string `json:"sessionIds"` // every contributing session
	Content    string   `json:"content"`    // full markdown content
	HasHistory bool     `json:"hasHistory"` // true when .history/<id>/ contains snapshots
}

var knowledgeMetaRe = regexp.MustCompile(`\*\*(.+?)\*\*:\s*(.*)`)

func knowledgeDir() (string, error) {
	dataRoot, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataRoot, "knowledge")
	os.MkdirAll(dir, 0700)
	return dir, nil
}

func knowledgeHistoryDir(dir, entryID string) (string, error) {
	historyDir := filepath.Join(dir, knowledgeHistoryDirName, entryID)
	if err := os.MkdirAll(historyDir, 0700); err != nil {
		return "", fmt.Errorf("mkdir knowledge history: %w", err)
	}
	return historyDir, nil
}

// knowledgeHistoryPresence remembers whether each history folder holds a
// snapshot, keyed on the folder's modification time: creating or removing a
// snapshot changes it, so a stale answer is never kept. Stats replace the
// directory read this check used to make for every entry on every list.
var knowledgeHistoryPresence = struct {
	sync.Mutex
	entries map[string]knowledgeHistoryEntry
}{entries: map[string]knowledgeHistoryEntry{}}

type knowledgeHistoryEntry struct {
	mod int64
	has bool
}

const knowledgeHistoryPresenceLimit = 4096

func knowledgeEntryHasHistory(dir, id string) bool {
	historyDir := filepath.Join(dir, knowledgeHistoryDirName, id)
	info, err := os.Stat(historyDir)
	if err != nil {
		return false
	}
	mod := info.ModTime().UnixNano()
	knowledgeHistoryPresence.Lock()
	if e, ok := knowledgeHistoryPresence.entries[historyDir]; ok && e.mod == mod {
		knowledgeHistoryPresence.Unlock()
		return e.has
	}
	knowledgeHistoryPresence.Unlock()
	has := knowledgeHistoryHasSnapshot(historyDir)
	knowledgeHistoryPresence.Lock()
	if len(knowledgeHistoryPresence.entries) >= knowledgeHistoryPresenceLimit {
		knowledgeHistoryPresence.entries = map[string]knowledgeHistoryEntry{}
	}
	knowledgeHistoryPresence.entries[historyDir] = knowledgeHistoryEntry{mod: mod, has: has}
	knowledgeHistoryPresence.Unlock()
	return has
}

func knowledgeHistoryHasSnapshot(historyDir string) bool {
	entries, err := os.ReadDir(historyDir)
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

func snapshotKnowledgeFile(dir, entryID string) error {
	src := filepath.Join(dir, entryID+".md")
	data, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read source %s: %w", src, err)
	}
	historyDir, err := knowledgeHistoryDir(dir, entryID)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 8; attempt++ {
		dst := filepath.Join(historyDir, fmt.Sprintf("%d.md", time.Now().UnixMilli()))
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if os.IsExist(err) {
				time.Sleep(time.Millisecond)
				continue
			}
			return fmt.Errorf("open knowledge snapshot %s: %w", dst, err)
		}
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil {
			return fmt.Errorf("write knowledge snapshot %s: %w", dst, werr)
		}
		if cerr != nil {
			return fmt.Errorf("close knowledge snapshot %s: %w", dst, cerr)
		}
		return nil
	}
	return fmt.Errorf("knowledge snapshot filename collision for %s after retries", entryID)
}

func parseKnowledgeFile(filename, content string) (entry KnowledgeEntry) {
	defer func() { entry.Title = pii.DisplayText(entry.Title) }()
	entry = KnowledgeEntry{
		ID:      strings.TrimSuffix(filename, ".md"),
		Content: content,
	}
	if doc, ok := parseOKFDocument(content); ok {
		entry.Title = okfString(doc.Frontmatter, "title")
		entry.Date = okfString(doc.Frontmatter, "timestamp")
		entry.Apps = strings.Join(okfStringSlice(doc.Frontmatter, "apps"), ", ")
		entry.SessionIDs = okfStringSlice(doc.Frontmatter, "activity_sessions")
		if entry.Title == "" {
			entry.Title = entry.ID
		}
		return entry
	}

	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "# ") && entry.Title == "" {
			entry.Title = strings.TrimPrefix(trimmed, "# ")
			continue
		}
		m := knowledgeMetaRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		value := strings.TrimSpace(m[2])
		switch m[1] {
		case "Date":
			entry.Date = value
		case "Apps":
			entry.Apps = value
		case "Activity Session":
			if value != "" {
				// Legacy single-line form.
				entry.SessionIDs = append(entry.SessionIDs, value)
				continue
			}
			// Nested bullet form: consume indented bullets directly
			// below this line. Stop at the first non-indented or
			// non-bullet line.
			for j := i + 1; j < len(lines); j++ {
				raw := lines[j]
				if strings.TrimSpace(raw) == "" {
					continue
				}
				if !(strings.HasPrefix(raw, " ") && strings.HasPrefix(strings.TrimLeft(raw, " \t"), "- ")) {
					break
				}
				id := parseSessionBulletID(raw)
				if id != "" {
					entry.SessionIDs = append(entry.SessionIDs, id)
				}
				i = j
			}
		}
	}

	if entry.Title == "" {
		entry.Title = entry.ID
	}

	return entry
}

// GetKnowledgeEntries returns all knowledge entries, newest first.
func (a *App) GetKnowledgeEntries() (perfValue []KnowledgeEntry, perfErr error) {
	defer logPageBinding("GetKnowledgeEntries", time.Now(), &perfValue)
	dir, err := knowledgeDir()
	if err != nil {
		return nil, err
	}

	// Preserve read errors so projections distinguish unavailable sources
	// from deleted records; a failed read is never a successful empty list.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var result []KnowledgeEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || isOKFReservedMarkdownFile(e.Name()) {
			continue
		}
		// Bytes come from the size-and-mtime body cache shared with memory
		// reads; each entry is still parsed on every list.
		data, err := readMemoryBody(filepath.Join(dir, e.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				// Deleted after enumeration.
				continue
			}
			return nil, err
		}
		parsed := parseKnowledgeFile(e.Name(), string(data))
		parsed.HasHistory = knowledgeEntryHasHistory(dir, parsed.ID)
		result = append(result, parsed)
	}

	// Sort by date descending (newest first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].Date > result[j].Date
	})

	return result, nil
}

// GetKnowledgeForSession returns knowledge entries linked to a specific activity session.
func (a *App) GetKnowledgeForSession(sessionID string) ([]KnowledgeEntry, error) {
	all, err := a.GetKnowledgeEntries()
	if err != nil {
		return nil, err
	}

	// Also check by deterministic hash (the filename pattern used in saveKnowledge)
	hash := sha256.Sum256([]byte(sessionID))
	expectedID := fmt.Sprintf("activity-%x", hash[:8])

	var result []KnowledgeEntry
	for _, e := range all {
		if e.ID == expectedID || slices.Contains(e.SessionIDs, sessionID) {
			result = append(result, e)
		}
	}
	return result, nil
}

func (a *App) GetKnowledgeEntry(id string) (KnowledgeEntry, error) {
	dir, err := knowledgeDir()
	if err != nil {
		return KnowledgeEntry{}, err
	}
	filename := id + ".md"
	if strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		return KnowledgeEntry{}, fmt.Errorf("invalid knowledge entry ID")
	}
	path := filepath.Join(dir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return KnowledgeEntry{}, fmt.Errorf("knowledge entry not found")
		}
		return KnowledgeEntry{}, err
	}
	entry := parseKnowledgeFile(filename, string(data))
	entry.HasHistory = knowledgeEntryHasHistory(dir, id)
	return entry, nil
}

// SaveKnowledgeEntry overwrites the markdown file for an existing knowledge
// entry with user-edited content. Unlike memory's SaveMemoryEntry, this only
// updates entries that already exist — knowledge is produced by the activity
// triager and the GUI does not let users author entries from scratch.
//
// We acquire the same `memoryFileLocks` key as the triager and translate
// paths so a user edit can't race with an automated update on the same entry,
// and we snapshot the prior content before writing so the version history
// drawer can recover the original if the edit was a mistake.
func (a *App) SaveKnowledgeEntry(id string, content string) (KnowledgeEntry, error) {
	if id == "" {
		return KnowledgeEntry{}, fmt.Errorf("knowledge id is required")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return KnowledgeEntry{}, fmt.Errorf("invalid knowledge entry ID")
	}
	dir, err := knowledgeDir()
	if err != nil {
		return KnowledgeEntry{}, err
	}
	unlock := memoryFileLocks.Acquire("knowledge:" + id)
	defer unlock()

	filePath := filepath.Join(dir, id+".md")
	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			return KnowledgeEntry{}, fmt.Errorf("knowledge entry not found")
		}
		return KnowledgeEntry{}, err
	}
	previous, err := os.ReadFile(filePath)
	if err != nil {
		return KnowledgeEntry{}, err
	}
	content = knowledgeProvenance(content, string(previous), []string{"manual"}, nil, true)
	if err := snapshotKnowledgeFile(dir, id); err != nil {
		return KnowledgeEntry{}, err
	}
	if err := os.WriteFile(filePath, []byte(content), 0600); err != nil {
		return KnowledgeEntry{}, fmt.Errorf("write knowledge entry: %w", err)
	}
	a.markMemoryDirty("knowledge-save")

	entry, err := a.GetKnowledgeEntry(id)
	if err != nil {
		return KnowledgeEntry{}, err
	}
	if a.ctx != nil {
		a.emitBrowserEvent("insight:saved", map[string]string{
			"kind":   "knowledge",
			"id":     entry.ID,
			"title":  entry.Title,
			"source": "edit",
		})
	}

	if a.telemetry != nil {
		a.telemetry.Capture("knowledge.updated", map[string]any{
			"entry_id":     entry.ID,
			"content_size": len(content),
			"source":       "edit",
		})
	}

	return entry, nil
}

// DeleteKnowledgeEntry removes a knowledge entry by ID.
func (a *App) DeleteKnowledgeEntry(id string) error {
	deleted := false
	defer func() {
		if deleted {
			publishPrivacyWithdrawal(privacyWithdrawalScope{Kind: privacyWithdrawalSource, SourceKind: "knowledge", ID: id})
		}
	}()
	dir, err := knowledgeDir()
	if err != nil {
		return err
	}

	// Sanitize: prevent path traversal
	filename := id + ".md"
	if strings.Contains(filename, "..") || strings.ContainsAny(filename, "/\\") {
		return fmt.Errorf("invalid knowledge entry ID")
	}
	unlock := memoryFileLocks.Acquire("knowledge:" + id)
	defer unlock()
	withdrawal, err := a.topicWithdrawalForSource("knowledge", []string{id}, nil)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, filename)
	content, _ := os.ReadFile(path)
	if a.store != nil {
		if err := a.store.DeleteContextUses(context.Background(), "knowledge", id); err != nil {
			return err
		}
	}
	if err := a.withTopicWithdrawal(withdrawal, func() error {
		err := os.Remove(path)
		deleted = err == nil
		return err
	}); err != nil {
		return err
	}
	// Invalidate reads begun between the first erasure and unlink. A writer
	// recreating this ID acquires the same file lock after this operation.
	if a.store != nil {
		if err := a.store.DeleteContextUses(context.Background(), "knowledge", id); err != nil {
			return err
		}
	}
	_ = os.RemoveAll(filepath.Join(dir, knowledgeHistoryDirName, id))
	// Deleting a call's notes deletes the call: its recording and transcript.
	a.forgetMeetingRecordings(string(content))
	a.emitBrowserEvent("knowledge:updated")

	if a.telemetry != nil {
		a.telemetry.Capture("knowledge.deleted", map[string]any{
			"entry_id": id,
		})
	}

	return nil
}

type KnowledgeVersion struct {
	TimestampMs int64 `json:"timestampMs"`
	SizeBytes   int64 `json:"sizeBytes"`
}

func (a *App) ListKnowledgeVersions(id string) ([]KnowledgeVersion, error) {
	if id == "" {
		return nil, fmt.Errorf("knowledge id is required")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return nil, fmt.Errorf("invalid knowledge id")
	}
	dir, err := knowledgeDir()
	if err != nil {
		return nil, err
	}
	historyDir := filepath.Join(dir, knowledgeHistoryDirName, id)
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []KnowledgeVersion{}, nil
		}
		return nil, fmt.Errorf("read knowledge history dir: %w", err)
	}
	versions := make([]KnowledgeVersion, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		if stem == "" || !isAllDigits(stem) {
			continue
		}
		ts, parseErr := strconv.ParseInt(stem, 10, 64)
		if parseErr != nil {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		versions = append(versions, KnowledgeVersion{
			TimestampMs: ts,
			SizeBytes:   info.Size(),
		})
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].TimestampMs > versions[j].TimestampMs
	})
	return versions, nil
}

func (a *App) GetKnowledgeVersion(id string, timestampMs int64) (string, error) {
	if id == "" {
		return "", fmt.Errorf("knowledge id is required")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return "", fmt.Errorf("invalid knowledge id")
	}
	if timestampMs <= 0 {
		return "", fmt.Errorf("invalid timestamp")
	}
	dir, err := knowledgeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, knowledgeHistoryDirName, id, fmt.Sprintf("%d.md", timestampMs))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("version not found")
		}
		return "", fmt.Errorf("read knowledge version: %w", err)
	}
	return string(data), nil
}

func (a *App) TranslateKnowledgeEntryToCurrentLanguage(id string) (KnowledgeEntry, error) {
	return a.translateKnowledgeEntryToLanguage(id, a.currentUILanguageName())
}

func (a *App) translateKnowledgeEntryToLanguage(id, languageName string) (KnowledgeEntry, error) {
	if id == "" {
		return KnowledgeEntry{}, fmt.Errorf("knowledge id is required")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return KnowledgeEntry{}, fmt.Errorf("invalid knowledge id")
	}
	dir, err := knowledgeDir()
	if err != nil {
		return KnowledgeEntry{}, err
	}
	unlock := memoryFileLocks.Acquire("knowledge:" + id)
	defer unlock()

	filePath := filepath.Join(dir, id+".md")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return KnowledgeEntry{}, fmt.Errorf("knowledge entry not found")
		}
		return KnowledgeEntry{}, fmt.Errorf("read knowledge: %w", err)
	}
	translated, err := a.translateMarkdownToLanguage("knowledge", string(data), languageName)
	if err != nil {
		return KnowledgeEntry{}, err
	}

	translated = knowledgeProvenance(translated, string(data), []string{"translation"}, nil, knowledgeProvenanceKnown(string(data)))
	if err := snapshotKnowledgeFile(dir, id); err != nil {
		return KnowledgeEntry{}, err
	}
	if err := os.WriteFile(filePath, []byte(translated), 0600); err != nil {
		return KnowledgeEntry{}, fmt.Errorf("write translated knowledge: %w", err)
	}
	a.markMemoryDirty("knowledge-translate")

	if a.telemetry != nil {
		a.telemetry.Capture("knowledge.translated", map[string]any{
			"entry_id":        id,
			"target_language": languageName,
		})
	}

	return a.GetKnowledgeEntry(id)
}

func (a *App) TranslateKnowledgeVersionToCurrentLanguage(id string, timestampMs int64) (KnowledgeEntry, error) {
	content, err := a.GetKnowledgeVersion(id, timestampMs)
	if err != nil {
		return KnowledgeEntry{}, err
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return KnowledgeEntry{}, fmt.Errorf("invalid knowledge id")
	}
	dir, err := knowledgeDir()
	if err != nil {
		return KnowledgeEntry{}, err
	}
	unlock := memoryFileLocks.Acquire("knowledge:" + id)
	defer unlock()

	filePath := filepath.Join(dir, id+".md")
	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			return KnowledgeEntry{}, fmt.Errorf("knowledge entry not found")
		}
		return KnowledgeEntry{}, err
	}
	translated, err := a.translateMarkdownToCurrentUILanguage("knowledge history snapshot", content)
	if err != nil {
		return KnowledgeEntry{}, err
	}
	previous, err := os.ReadFile(filePath)
	if err != nil {
		return KnowledgeEntry{}, err
	}
	translated = knowledgeProvenance(translated, string(previous), []string{"translation"}, nil, knowledgeProvenanceKnown(content) && knowledgeProvenanceKnown(string(previous)))

	if err := snapshotKnowledgeFile(dir, id); err != nil {
		return KnowledgeEntry{}, err
	}
	if err := os.WriteFile(filePath, []byte(translated), 0600); err != nil {
		return KnowledgeEntry{}, fmt.Errorf("write translated knowledge snapshot: %w", err)
	}
	a.markMemoryDirty("knowledge-translate-version")
	return a.GetKnowledgeEntry(id)
}

func (a *App) translateAllKnowledgeVersionsToLanguage(id, languageName string) ([]KnowledgeVersion, error) {
	if id == "" {
		return nil, fmt.Errorf("knowledge id is required")
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "/\\") {
		return nil, fmt.Errorf("invalid knowledge id")
	}
	dir, err := knowledgeDir()
	if err != nil {
		return nil, err
	}
	versions, err := a.ListKnowledgeVersions(id)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return versions, nil
	}

	unlock := memoryFileLocks.Acquire("knowledge:" + id)
	defer unlock()

	historyDir := filepath.Join(dir, knowledgeHistoryDirName, id)
	for _, version := range versions {
		path := filepath.Join(historyDir, fmt.Sprintf("%d.md", version.TimestampMs))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read knowledge history snapshot: %w", err)
		}
		translated, err := a.translateMarkdownToLanguage("knowledge history snapshot", string(data), languageName)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(translated), 0600); err != nil {
			return nil, fmt.Errorf("write translated knowledge history snapshot: %w", err)
		}
	}
	a.markMemoryDirty("knowledge-translate-all-history")
	return a.ListKnowledgeVersions(id)
}
