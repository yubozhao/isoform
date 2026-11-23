package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Dream provenance is directional: removing a source removes its derived
// syntheses. Removing a synthesis never marks its independent parents erased.
func memoryDerivedSourceIDs(content string) []string {
	ids := map[string]bool{}
	add := func(value string) {
		value = strings.TrimSuffix(strings.TrimSpace(value), ".md")
		if value != "" {
			ids[value] = true
		}
	}
	if doc, ok := parseOKFDocument(content); ok {
		for _, id := range okfStringSlice(doc.Frontmatter, "merged_from") {
			add(id)
		}
	}
	for _, line := range splitMarkdownTranslationParts(content).Metadata {
		for _, prefix := range []string{"- **MergedFrom**:", "- **Merged From**:"} {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
				for _, id := range strings.Split(value, ",") {
					add(id)
				}
			}
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func memoryDerivedSourceRefs(content string) []string {
	if doc, ok := parseOKFDocument(content); ok {
		return okfStringSlice(doc.Frontmatter, "merged_from_refs")
	}
	for _, line := range splitMarkdownTranslationParts(content).Metadata {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "- **MergedFromRefs**:"); ok {
			var refs []string
			if json.Unmarshal([]byte(strings.TrimSpace(value)), &refs) == nil {
				return refs
			}
		}
	}
	return nil
}

type memoryForgetRecord struct {
	versionOnlySuppression        bool
	id, title, livePath           string
	liveContent                   string
	contents                      []string
	archivePaths, archiveContents []string
	historyPaths, historyContents []string
	historyCount                  int
	items                         []MemoryForgetItem
	preserveLive                  bool
}

func validMemoryForgetRecordID(id string) bool {
	return id != "" && id != "." && !strings.Contains(id, "..") && !strings.ContainsAny(id, "/\\")
}

// The graph includes archived syntheses and saved versions, so a deleted live
// source cannot leave its facts behind in an archived or historical descendant.
func loadMemoryForgetRecords(dir string) (map[string]*memoryForgetRecord, error) {
	records := map[string]*memoryForgetRecord{}
	record := func(id string) *memoryForgetRecord {
		if records[id] == nil {
			records[id] = &memoryForgetRecord{id: id}
		}
		return records[id]
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(entry.Name(), ".md")
		if !validMemoryForgetRecordID(id) {
			return nil, fmt.Errorf("memory file has an invalid record ID")
		}
		item := record(id)
		item.livePath, item.liveContent, item.title = path, string(data), extractTitle(string(data))
		if item.title == "" {
			item.title = id
		}
		item.contents = append(item.contents, string(data))
		item.items = append(item.items, MemoryForgetItem{Kind: "memory", ID: id, Title: item.title})
	}
	archives, err := os.ReadDir(filepath.Join(dir, memoryArchiveDirName))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, archive := range archives {
		if archive.IsDir() || !strings.HasSuffix(archive.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, memoryArchiveDirName, archive.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		archiveID := strings.TrimSuffix(archive.Name(), ".md")
		parsed := parseArchivedMemory(archiveID, string(data))
		if !validMemoryForgetRecordID(parsed.OriginalID) {
			return nil, fmt.Errorf("archived memory has an invalid original record ID")
		}
		item := record(parsed.OriginalID)
		item.archivePaths = append(item.archivePaths, path)
		item.archiveContents = append(item.archiveContents, string(data))
		item.contents = append(item.contents, stripArchiveHeader(string(data)))
		item.historyCount++
		item.items = append(item.items, MemoryForgetItem{Kind: "memory_archive", ID: archiveID, Title: parsed.Title})
	}
	histories, err := os.ReadDir(filepath.Join(dir, memoryHistoryDirName))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, history := range histories {
		if !history.IsDir() {
			continue
		}
		if !validMemoryForgetRecordID(history.Name()) {
			return nil, fmt.Errorf("saved memory versions have an invalid record ID")
		}
		path := filepath.Join(dir, memoryHistoryDirName, history.Name())
		versions, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, version := range versions {
			if version.IsDir() || !strings.HasSuffix(version.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(path, version.Name()))
			if err != nil {
				return nil, err
			}
			item := record(history.Name())
			item.historyPaths = append(item.historyPaths, filepath.Join(path, version.Name()))
			item.historyContents = append(item.historyContents, string(data))
			item.contents = append(item.contents, string(data))
			item.historyCount++
		}
	}
	return records, nil
}

// A user correction retains the same ID as the history memory it replaced.
// Select only the history versions and references to them in that case.
func collectMemoryForgetRecordsScoped(dir, id string, historyVersionsOnly bool) ([]*memoryForgetRecord, error) {
	records, err := loadMemoryForgetRecords(dir)
	if err != nil {
		return nil, err
	}
	return selectMemoryForgetRecords(records, id, historyVersionsOnly, map[string]bool{id: true}, nil)
}

func selectMemoryForgetRecords(records map[string]*memoryForgetRecord, id string, historyVersionsOnly bool, selected, independent map[string]bool) ([]*memoryForgetRecord, error) {

	var priorRoot *memoryForgetRecord
	priorPaths := map[string]bool{}
	if historyVersionsOnly {
		delete(selected, id)
		root := records[id]
		if root == nil || root.livePath == "" || !isUserEditedMemory(root.liveContent) {
			return nil, fmt.Errorf("history memory no longer has an independent current version")
		}
		priorRoot = &memoryForgetRecord{id: root.id, title: root.title, preserveLive: true}
		for n, content := range root.archiveContents {
			body := stripArchiveHeader(content)
			if parseMemoryFrontmatter(body).Source != assistantHistorySource {
				continue
			}
			priorRoot.archivePaths = append(priorRoot.archivePaths, root.archivePaths[n])
			priorPaths[root.archivePaths[n]] = true
			priorRoot.archiveContents = append(priorRoot.archiveContents, content)
			priorRoot.contents = append(priorRoot.contents, body)
			priorRoot.items = append(priorRoot.items, MemoryForgetItem{Kind: "memory_archive", ID: strings.TrimSuffix(filepath.Base(root.archivePaths[n]), ".md"), Title: extractTitle(body)})
			priorRoot.historyCount++
		}
		for n, content := range root.historyContents {
			if parseMemoryFrontmatter(content).Source != assistantHistorySource {
				continue
			}
			priorRoot.historyPaths = append(priorRoot.historyPaths, root.historyPaths[n])
			priorPaths[root.historyPaths[n]] = true
			priorRoot.historyContents = append(priorRoot.historyContents, content)
			priorRoot.contents = append(priorRoot.contents, content)
			priorRoot.items = append(priorRoot.items, MemoryForgetItem{Kind: "memory_history", ID: id + ":" + filepath.Base(root.historyPaths[n]), Title: extractTitle(content)})
			priorRoot.historyCount++
		}
		if len(priorRoot.contents) == 0 {
			return nil, fmt.Errorf("history memory has no history-sourced versions")
		}
		for _, content := range priorRoot.contents {
			selected[memoryForgetHistoricalSelector(id, content)] = true
		}
	}
	for changed := true; changed; {
		changed = false
		if historyVersionsOnly {
			root := records[id]
			for n, content := range root.archiveContents {
				path, body := root.archivePaths[n], stripArchiveHeader(content)
				if priorPaths[path] || !memoryContentCitesRemoved(body, selected) {
					continue
				}
				priorPaths[path], changed = true, true
				priorRoot.archivePaths = append(priorRoot.archivePaths, path)
				priorRoot.archiveContents = append(priorRoot.archiveContents, content)
				priorRoot.contents = append(priorRoot.contents, body)
				priorRoot.items = append(priorRoot.items, MemoryForgetItem{Kind: "memory_archive", ID: strings.TrimSuffix(filepath.Base(path), ".md"), Title: extractTitle(body)})
				priorRoot.historyCount++
				selected[memoryForgetHistoricalSelector(id, body)] = true
			}
			for n, content := range root.historyContents {
				path := root.historyPaths[n]
				if priorPaths[path] || !memoryContentCitesRemoved(content, selected) {
					continue
				}
				priorPaths[path], changed = true, true
				priorRoot.historyPaths = append(priorRoot.historyPaths, path)
				priorRoot.historyContents = append(priorRoot.historyContents, content)
				priorRoot.contents = append(priorRoot.contents, content)
				priorRoot.items = append(priorRoot.items, MemoryForgetItem{Kind: "memory_history", ID: id + ":" + filepath.Base(path), Title: extractTitle(content)})
				priorRoot.historyCount++
				selected[memoryForgetHistoricalSelector(id, content)] = true
			}
		}
		for recordID, record := range records {
			if historyVersionsOnly && recordID == id {
				continue
			}
			if selected[recordID] || independent[recordID] {
				continue
			}
			// An independent explicit correction is a new provenance boundary.
			// Its old snapshots can be erased, but neither its current content
			// nor later records citing the correction inherit the old source.
			if isUserEditedMemory(record.liveContent) && !memoryContentCitesRemoved(record.liveContent, selected) {
				prior := memoryForgetPriorVersions(record, selected)
				for _, content := range prior.contents {
					selector := memoryForgetHistoricalSelector(recordID, content)
					if !selected[selector] {
						selected[selector], changed = true, true
					}
				}
				continue
			}
			for _, content := range record.contents {
				if memoryContentCitesRemoved(content, selected) {
					selected[recordID], changed = true, true
				}
			}
		}
	}
	ids := make([]string, 0, len(records))
	for recordID := range records {
		ids = append(ids, recordID)
	}
	sort.Strings(ids)
	result := make([]*memoryForgetRecord, 0, len(selected))
	for _, recordID := range ids {
		record := records[recordID]
		if historyVersionsOnly && recordID == id {
			result = append(result, priorRoot)
			continue
		}
		if selected[recordID] {
			result = append(result, record)
			continue
		}
		if independent[recordID] || !isUserEditedMemory(record.liveContent) {
			continue
		}
		prior := memoryForgetPriorVersions(record, selected)
		if len(prior.contents) > 0 {
			result = append(result, prior)
		}
	}
	return result, nil
}

func memoryContentCitesRemoved(content string, ids map[string]bool) bool {
	refs := memoryDerivedSourceRefs(content)
	for _, id := range memoryDerivedSourceIDs(content) {
		if ids[id] {
			return true
		}
		refs = append(refs, "memory:"+id)
	}
	for _, ref := range refs {
		if memoryForgetHistoricalRefMatches(ref, refs, ids) {
			return true
		}
	}
	return false
}

func memoryForgetPriorVersions(record *memoryForgetRecord, ids map[string]bool) *memoryForgetRecord {
	result := &memoryForgetRecord{id: record.id, title: record.title, preserveLive: true}
	for n, content := range record.archiveContents {
		body := stripArchiveHeader(content)
		if !memoryContentCitesRemoved(body, ids) {
			continue
		}
		path := record.archivePaths[n]
		archiveID := strings.TrimSuffix(filepath.Base(path), ".md")
		result.archivePaths = append(result.archivePaths, path)
		result.archiveContents = append(result.archiveContents, content)
		result.contents = append(result.contents, body)
		result.items = append(result.items, MemoryForgetItem{Kind: "memory_archive", ID: archiveID, Title: extractTitle(body)})
		result.historyCount++
	}
	for n, content := range record.historyContents {
		if !memoryContentCitesRemoved(content, ids) {
			continue
		}
		path := record.historyPaths[n]
		result.historyPaths = append(result.historyPaths, path)
		result.historyContents = append(result.historyContents, content)
		result.contents = append(result.contents, content)
		result.items = append(result.items, MemoryForgetItem{Kind: "memory_history", ID: record.id + ":" + filepath.Base(path), Title: extractTitle(content)})
		result.historyCount++
	}
	return result
}

type memoryForgetRejectionScope struct {
	before, after []byte
	items         []MemoryForgetItem
}

func collectMemoryForgetRejections(dir string, ids map[string]bool) (memoryForgetRejectionScope, error) {
	var scope memoryForgetRejectionScope
	data, err := os.ReadFile(filepath.Join(dir, memoryRejectionsLogName))
	if os.IsNotExist(err) {
		return scope, nil
	}
	if err != nil {
		return scope, err
	}
	scope.before = data
	for n, line := range strings.SplitAfter(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			scope.after = append(scope.after, line...)
			continue
		}
		var row MemoryRejectionEntry
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return scope, fmt.Errorf("read saved attribution correction: %w", err)
		}
		if ids[row.MemoryID] {
			scope.items = append(scope.items, MemoryForgetItem{Kind: "memory_correction", ID: fmt.Sprintf("rejection:%d", n+1), Title: row.Title})
			continue
		}
		scope.after = append(scope.after, line...)
	}
	return scope, nil
}

func writeMemoryForgetRejections(dir string, scope memoryForgetRejectionScope) error {
	if len(scope.items) == 0 {
		return nil
	}
	path := filepath.Join(dir, memoryRejectionsLogName)
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(current) != string(scope.before) {
		return fmt.Errorf("saved attribution corrections changed; review removal again")
	}
	file, err := os.CreateTemp(dir, ".rejections-forget-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(scope.after); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
