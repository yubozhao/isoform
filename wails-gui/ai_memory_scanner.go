package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AIMemorySourceID identifies a known AI application whose memory files can be imported.
type AIMemorySourceID string

const (
	AISourceClaudeCode AIMemorySourceID = "claude-code"
)

// AIMemoryFile represents a single importable memory file from an AI application.
type AIMemoryFile struct {
	ID       string `json:"id"`       // unique key: "<source>:<relative-path>"
	Path     string `json:"path"`     // absolute path on disk
	RelPath  string `json:"relPath"`  // path relative to the source root (display only)
	Title    string `json:"title"`    // extracted title (first # heading or filename)
	Preview  string `json:"preview"`  // first ~200 chars of content
	Size     int64  `json:"size"`     // file size in bytes
	Imported bool   `json:"imported"` // true if this file was previously imported
}

// AIMemorySource represents a discovered AI application with importable memory files.
type AIMemorySource struct {
	ID          AIMemorySourceID `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	FileCount   int              `json:"fileCount"`
	TotalSize   int64            `json:"totalSize"`
	Files       []AIMemoryFile   `json:"files"`
}

// AIMemoryImportResult reports the outcome of an import operation.
type AIMemoryImportResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors"`
}

// ScanAIMemories discovers memory files from known AI applications on the user's machine.
// Files that have been previously imported are marked with Imported=true.
func (a *App) ScanAIMemories() ([]AIMemorySource, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	imported := loadImportedSet()

	var sources []AIMemorySource

	// Claude Code memories (per-project memory files with frontmatter)
	if src := scanClaudeCode(home); src != nil {
		for i := range src.Files {
			if imported[src.Files[i].ID] {
				src.Files[i].Imported = true
			}
		}
		sources = append(sources, *src)
	}

	return sources, nil
}

// ImportAIMemories imports the selected AI memory files into Yansu's memory directory.
// fileIDs should be in the format "<source>:<relative-path>" as returned by ScanAIMemories.
func (a *App) ImportAIMemories(fileIDs []string) AIMemoryImportResult {
	result := AIMemoryImportResult{}

	destDir, err := memoryDir()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("cannot access memory directory: %v", err))
		return result
	}

	// Build a lookup of all available files by scanning again.
	home, err := os.UserHomeDir()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("cannot determine home directory: %v", err))
		return result
	}

	allFiles := make(map[string]AIMemoryFile)
	sources := []func(string) *AIMemorySource{scanClaudeCode}
	for _, scanFn := range sources {
		if src := scanFn(home); src != nil {
			for _, f := range src.Files {
				allFiles[f.ID] = f
			}
		}
	}

	requested := make(map[string]bool, len(fileIDs))
	for _, id := range fileIDs {
		requested[id] = true
	}

	var importedIDs []string

	for id := range requested {
		f, ok := allFiles[id]
		if !ok {
			result.Skipped++
			continue
		}

		parts := strings.SplitN(id, ":", 2)
		sourcePrefix := parts[0]

		data, readErr := os.ReadFile(f.Path)
		if readErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("read %s: %v", f.RelPath, readErr))
			continue
		}

		// Every line arrives as a claim to judge (design answer T2).
		written, errs := a.writeImportedClaims(destDir, sourceDisplayName(AIMemorySourceID(sourcePrefix)), string(data))
		result.Errors = append(result.Errors, errs...)
		if written == 0 {
			continue
		}

		result.Imported += written
		importedIDs = append(importedIDs, id)
	}

	if result.Imported > 0 {
		markAsImported(importedIDs)
		a.markMemoryDirty("ai-import")
	}

	return result
}

// ── Claude Code scanner ──────────────────────────────────────────────

func scanClaudeCode(home string) *AIMemorySource {
	claudeDir := filepath.Join(home, ".claude")
	if !dirExists(claudeDir) {
		return nil
	}

	var files []AIMemoryFile
	var totalSize int64

	// Scan per-project memory directories: ~/.claude/projects/*/memory/*.md
	projectsDir := filepath.Join(claudeDir, "projects")
	if dirExists(projectsDir) {
		projects, _ := os.ReadDir(projectsDir)
		for _, proj := range projects {
			if !proj.IsDir() {
				continue
			}
			memDir := filepath.Join(projectsDir, proj.Name(), "memory")
			if !dirExists(memDir) {
				continue
			}
			memFiles, _ := os.ReadDir(memDir)
			for _, mf := range memFiles {
				if mf.IsDir() || !strings.HasSuffix(mf.Name(), ".md") {
					continue
				}
				absPath := filepath.Join(memDir, mf.Name())
				relPath := filepath.Join("projects", proj.Name(), "memory", mf.Name())
				f := readMemoryFile(AISourceClaudeCode, absPath, relPath)
				if f != nil {
					files = append(files, *f)
					totalSize += f.Size
				}
			}
		}
	}

	if len(files) == 0 {
		return nil
	}

	return &AIMemorySource{
		ID:          AISourceClaudeCode,
		Name:        "Claude Code",
		Description: "Per-project memory (preferences, feedback, context)",
		FileCount:   len(files),
		TotalSize:   totalSize,
		Files:       files,
	}
}

// ── ChatGPT prompt-based import ──────────────────────────────────────

// ImportAIMemoryText saves pasted text (e.g. from ChatGPT memory export) as
// one memory entry per claim line. The `source` parameter is a display label like "ChatGPT".
func (a *App) ImportAIMemoryText(source string, text string) (AIMemoryImportResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return AIMemoryImportResult{}, nil
	}

	destDir, err := memoryDir()
	if err != nil {
		return AIMemoryImportResult{}, err
	}

	// Every line arrives as a claim to judge (design answer T2).
	written, errs := a.writeImportedClaims(destDir, source, text)
	if written == 0 {
		return AIMemoryImportResult{Errors: errs}, nil
	}
	a.markMemoryDirty("text-import")
	return AIMemoryImportResult{Imported: written, Errors: errs}, nil
}

// ── Import tracking ──────────────────────────────────────────────────

const importedSourcesFile = ".imported-ai-sources.json"

func importedSourcesPath() string {
	dir, err := memoryDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, importedSourcesFile)
}

// loadImportedSet reads the set of previously imported file IDs.
func loadImportedSet() map[string]bool {
	path := importedSourcesPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ids []string
	if json.Unmarshal(data, &ids) != nil {
		return nil
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// markAsImported appends the given IDs to the imported set on disk.
func markAsImported(newIDs []string) {
	path := importedSourcesPath()
	if path == "" {
		return
	}
	existing := loadImportedSet()
	if existing == nil {
		existing = make(map[string]bool)
	}
	for _, id := range newIDs {
		existing[id] = true
	}
	all := make([]string, 0, len(existing))
	for id := range existing {
		all = append(all, id)
	}
	data, err := json.Marshal(all)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

// ── Helpers ──────────────────────────────────────────────────────────

func readMemoryFile(source AIMemorySourceID, absPath, relPath string) *AIMemoryFile {
	info, err := os.Stat(absPath)
	if err != nil {
		return nil
	}

	// Skip files larger than 1MB (likely not memory files)
	if info.Size() > 1<<20 {
		return nil
	}

	// Skip empty files
	if info.Size() == 0 {
		return nil
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}

	content := string(data)
	title := extractMemoryTitle(content, filepath.Base(absPath))
	preview := extractPreview(content, 200)

	return &AIMemoryFile{
		ID:      string(source) + ":" + relPath,
		Path:    absPath,
		RelPath: relPath,
		Title:   title,
		Preview: preview,
		Size:    info.Size(),
	}
}

func extractMemoryTitle(content, fallback string) string {
	// Try to extract from YAML frontmatter
	if strings.HasPrefix(content, "---\n") {
		end := strings.Index(content[4:], "\n---")
		if end > 0 {
			frontmatter := content[4 : 4+end]
			for _, line := range strings.Split(frontmatter, "\n") {
				line = strings.TrimSpace(line)
				if name, ok := strings.CutPrefix(line, "name:"); ok {
					name = strings.TrimSpace(name)
					if name != "" {
						return name
					}
				}
			}
		}
	}

	// Try first markdown heading
	for _, line := range strings.SplitN(content, "\n", 20) {
		line = strings.TrimSpace(line)
		if title, ok := strings.CutPrefix(line, "# "); ok {
			return title
		}
	}

	// Fall back to filename without extension
	name := strings.TrimSuffix(fallback, filepath.Ext(fallback))
	return name
}

func extractPreview(content string, maxLen int) string {
	// Skip frontmatter
	s := content
	if strings.HasPrefix(s, "---\n") {
		end := strings.Index(s[4:], "\n---")
		if end > 0 {
			s = strings.TrimSpace(s[4+end+4:])
		}
	}

	// Skip first heading
	lines := strings.SplitN(s, "\n", 30)
	var preview strings.Builder
	skippedHeading := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !skippedHeading && strings.HasPrefix(trimmed, "# ") {
			skippedHeading = true
			continue
		}
		if trimmed == "" && preview.Len() == 0 {
			continue
		}
		if preview.Len() > 0 {
			preview.WriteByte(' ')
		}
		preview.WriteString(trimmed)
		if preview.Len() >= maxLen {
			break
		}
	}

	result := preview.String()
	if len(result) > maxLen {
		result = result[:maxLen] + "..."
	}
	return result
}

func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	result := b.String()
	// Collapse multiple dashes
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}

func sourceDisplayName(id AIMemorySourceID) string {
	switch id {
	case AISourceClaudeCode:
		return "Claude Code"
	default:
		return string(id)
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (a *App) writeImportedMemoryFile(dir, id, content string) error {
	if err := a.prepareMemoryObservedWrite(dir, id, content); err != nil {
		return err
	}
	releaseMutation := acquireMemoryMutationRead()
	defer releaseMutation()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	if err := checkMemoryWriteAllowed(dir, id, content, true); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, id+".md"), []byte(content), 0600)
}
