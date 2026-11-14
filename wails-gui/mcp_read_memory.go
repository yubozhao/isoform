package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Read files directly, unlike SearchMemory which may sync and embed on read.
// Ignore symlinks and large/legacy files whose provenance cannot be checked.
func (p *mcpReadPrivacy) memoryEntries() ([]MemoryEntry, error) {
	if p.memoriesLoaded {
		return p.memories, p.memoriesErr
	}
	p.memoriesLoaded = true
	entries, err := p.loadMemoryEntries()
	p.memories, p.memoriesErr = entries, err
	return entries, err
}

func (p *mcpReadPrivacy) loadMemoryEntries() ([]MemoryEntry, error) {
	dir := filepath.Join(configDirOrFallback(), "memory")
	p.app.mu.Lock()
	if p.app.memoryStore != nil {
		dir = p.app.memoryStore.baseDir
	}
	p.app.mu.Unlock()
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// ReadDir is name sorted. Stat all files before applying the content
	// budget so new hashed activity IDs are not starved by older filenames.
	type candidate struct {
		name string
		info os.FileInfo
	}
	candidates := []candidate{}
	for _, file := range files {
		if p.ctx.Err() != nil {
			return nil, p.ctx.Err()
		}
		if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".md") || isOKFReservedMarkdownFile(file.Name()) {
			continue
		}
		info, err := file.Info()
		if err != nil || !info.Mode().IsRegular() {
			p.memoryScanOmitted++
			continue
		}
		candidates = append(candidates, candidate{file.Name(), info})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].info.ModTime().Equal(candidates[j].info.ModTime()) {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].info.ModTime().After(candidates[j].info.ModTime())
	})
	entries := []MemoryEntry{}
	var scannedBytes int64
	reads := 0
	for i, file := range candidates {
		if p.ctx.Err() != nil {
			return nil, p.ctx.Err()
		}
		if reads >= mcpReadScanLimit {
			p.memoryScanOmitted += len(candidates) - i
			break
		}
		info := file.info
		if info.Size() > 256<<10 {
			p.memoryScanOmitted++
			continue
		}
		scannedBytes += info.Size()
		if scannedBytes > 8<<20 {
			p.memoryScanOmitted += len(candidates) - i
			break
		}
		reads++
		data, err := os.ReadFile(filepath.Join(dir, file.name))
		if err != nil {
			p.memoryScanOmitted++
			continue
		}
		body := string(data)
		if learnedFromAgents(body) {
			// Learned only from agent activity: hidden on every page, so
			// never served to an external assistant either.
			continue
		}
		fm := parseMemoryFrontmatter(body)
		entries = append(entries, MemoryEntry{ID: strings.TrimSuffix(file.name, ".md"), Title: extractTitle(body), Content: body, Dimension: extractMemoryDimension(body), ModTime: info.ModTime().UnixMilli(), UpdatedAt: fm.UpdatedAt, Source: fm.Source, Apps: fm.Apps, ActivitySessionIDs: fm.ActivitySessionIDs})
	}
	sort.Slice(entries, func(i, j int) bool {
		return max(entries[i].UpdatedAt, entries[i].ModTime) > max(entries[j].UpdatedAt, entries[j].ModTime)
	})
	p.memoryScanTruncated = p.memoryScanOmitted > 0
	return entries, nil
}

func (p *mcpReadPrivacy) memoryRead(args mcpReadArguments, result *mcpReadResult) error {
	useEpoch := p.app.contextUseEpoch()
	entries, err := p.memoryEntries()
	if err != nil {
		return err
	}
	filtered := args.Query != "" || args.Kind != ""
	if err := p.memorySearchPrivacyBound(entries, result); err != nil {
		return err
	}
	for _, entry := range entries {
		// A belief the person said Not quite to is not used until they
		// confirm or reword it.
		if (args.Kind != "" && entry.Dimension != args.Kind) || !memoryInUse(entry.Content) {
			continue
		}
		text := mcpReadSnippet(stripMemoryFrontmatter(entry.Content), args.Query)
		if args.Query != "" && !strings.Contains(strings.ToLower(text.Text), strings.ToLower(args.Query)) && !strings.Contains(strings.ToLower(mcpReadText(entry.Title).Text), strings.ToLower(args.Query)) {
			continue
		}
		if !p.memoryAllowed(entry) {
			// A keyword or kind filter must not reveal a private match through
			// budget metadata any more than through a withheld count.
			if !filtered {
				if p.memoryLimited(entry.ID) {
					result.Truncated = true
					result.Omitted++
				} else {
					result.Withheld++
				}
			}
			continue
		}
		result.Total++
		if len(result.Items) < args.Limit {
			result.uses = append(result.uses, contextUseEntry{"memory", entry.ID, useEpoch})
			result.Items = append(result.Items, mcpReadItem{Source: "memory", ID: mcpReadText(entry.ID), At: max(entry.UpdatedAt, entry.ModTime), Title: mcpReadTextPtr(entry.Title), Snippet: &text})
		} else {
			result.Truncated = true
			result.Omitted++
		}
	}
	result.Truncated = result.Truncated || p.memoryScanTruncated
	result.Omitted += p.memoryScanOmitted
	return nil
}

// Report possible privacy-work limits for every list and search, independently
// of the keyword or kind, including when no filter is supplied.
// Actual history and session validation still happens only after matching, so
// an unrelated large history cannot consume a readable match's work budget.
func (p *mcpReadPrivacy) memorySearchPrivacyBound(entries []MemoryEntry, result *mcpReadResult) error {
	if err := p.searchPrivacyBound(result, mcpReadSessionCheckLimit); err != nil {
		return err
	}
	if result.Truncated {
		return nil
	}
	dir := filepath.Join(configDirOrFallback(), "memory")
	p.app.mu.Lock()
	if p.app.memoryStore != nil {
		dir = p.app.memoryStore.baseDir
	}
	p.app.mu.Unlock()
	references := map[string]bool{}
	for _, entry := range entries {
		if p.ctx.Err() != nil {
			return p.ctx.Err()
		}
		fm := parseMemoryFrontmatter(entry.Content)
		provenance := memoryReadProvenanceFromContent(entry.Content)
		// Missing source IDs also spend session checks, but repeated IDs share
		// the request's cache. Match its normalized keys across all snapshots
		// without resolving sources or depending on the requested filter.
		for _, ids := range [][]string{entry.ActivitySessionIDs, provenance.Sessions} {
			for _, id := range ids {
				if id = strings.TrimSpace(id); id != "" {
					references[id] = true
					if len(references) > mcpReadSessionCheckLimit {
						result.Truncated = true
						return nil
					}
				}
			}
		}
		if fm.ReadProvenance != "" {
			continue
		}
		history, err := os.Open(filepath.Join(dir, memoryHistoryDirName, entry.ID))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			result.Truncated = true
			return nil
		}
		// Any legacy history can add source dependencies or exhaust the
		// shared history budget. Inspect at most one directory entry; do not
		// decode revisions or infer query-specific omission counts here.
		_, err = history.ReadDir(1)
		_ = history.Close()
		if !errors.Is(err, io.EOF) {
			result.Truncated = true
			return nil
		}
	}
	return nil
}
