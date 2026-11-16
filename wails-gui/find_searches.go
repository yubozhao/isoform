package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// What the person searches for in ⌘K is kept on this computer and shown in
// Activity under Your day (computer-find v1.0.0 rule 6). One small file,
// mode 0600, the last findSearchesMax searches; Forget removes one. Nothing
// is kept while Closed Eyes is on.
const (
	findSearchesDirName  = "find"
	findSearchesFileName = "searches.json"
	findSearchesMax      = 500
)

// FindSearch is one kept search.
type FindSearch struct {
	ID    string `json:"id"`
	Query string `json:"query"`
	At    int64  `json:"at"`
}

var findSearchesMu sync.Mutex

func findSearchesPath() (string, error) {
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, findSearchesDirName, findSearchesFileName), nil
}

func readFindSearches(path string) ([]FindSearch, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []FindSearch{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []FindSearch
	if err := json.Unmarshal(data, &out); err != nil {
		return []FindSearch{}, nil
	}
	return out, nil
}

func writeFindSearches(path string, searches []FindSearch) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(searches)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NoteFindSearch keeps a search the person made in ⌘K: when they open a
// result or press Enter. The same query again moves to now.
func (a *App) NoteFindSearch(query string) error {
	query = strings.TrimSpace(query)
	if len(findQueryWords(query)) == 0 || a.IsClosedEyesModeEnabled() {
		return nil
	}
	path, err := findSearchesPath()
	if err != nil {
		return err
	}
	findSearchesMu.Lock()
	defer findSearchesMu.Unlock()
	searches, err := readFindSearches(path)
	if err != nil {
		return err
	}
	kept := searches[:0]
	for _, s := range searches {
		if !strings.EqualFold(s.Query, query) {
			kept = append(kept, s)
		}
	}
	kept = append(kept, FindSearch{ID: uuid.NewString(), Query: query, At: time.Now().UnixMilli()})
	if over := len(kept) - findSearchesMax; over > 0 {
		kept = kept[over:]
	}
	return writeFindSearches(path, kept)
}

// ListFindSearches returns the searches kept in [startMs, endMs), newest
// first, for Activity's Your day.
func (a *App) ListFindSearches(startMs, endMs int64) ([]FindSearch, error) {
	path, err := findSearchesPath()
	if err != nil {
		return nil, err
	}
	findSearchesMu.Lock()
	searches, err := readFindSearches(path)
	findSearchesMu.Unlock()
	if err != nil {
		return nil, err
	}
	out := []FindSearch{}
	for i := len(searches) - 1; i >= 0; i-- {
		if s := searches[i]; s.At >= startMs && (endMs <= 0 || s.At < endMs) {
			out = append(out, s)
		}
	}
	return out, nil
}

// ForgetFindSearch removes one kept search for good.
func (a *App) ForgetFindSearch(id string) error {
	path, err := findSearchesPath()
	if err != nil {
		return err
	}
	findSearchesMu.Lock()
	defer findSearchesMu.Unlock()
	searches, err := readFindSearches(path)
	if err != nil {
		return err
	}
	kept := searches[:0]
	for _, s := range searches {
		if s.ID != id {
			kept = append(kept, s)
		}
	}
	return writeFindSearches(path, kept)
}
