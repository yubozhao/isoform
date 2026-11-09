package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// workProfileDiskSources reads only regular know-how files in the fixed directory.
type workProfileDiskSources struct{ dir string }

func (s workProfileDiskSources) KnowHow() ([]KnowledgeEntry, error) {
	files, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	entries := make([]KnowledgeEntry, 0, len(files))
	for _, file := range files {
		if !file.Type().IsRegular() || !strings.HasSuffix(file.Name(), ".md") || isOKFReservedMarkdownFile(file.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, file.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		entries = append(entries, parseKnowledgeFile(file.Name(), string(data)))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Date > entries[j].Date })
	return entries, nil
}
