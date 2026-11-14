package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Page snapshots on disk (Bo, 2026-11-13): a launch paints About you, How you
// do things and Who you know from the last snapshot instead of building them
// live, which took 4 to 8 s on the first visit after every launch.
//
// A saved snapshot carries a durable privacy stamp: the saved privacy epoch
// (withdrawals, corrections, sensitive apps, Closed Eyes), the forget markers
// file and the UI language. It is served only while that stamp still matches,
// and every privacy action deletes all saved snapshots in the same step
// (derivedDiskDropAll: clearPageSnapshots, noteDerivedPrivacyChanged, a forget
// marker write, a language change). Nothing is saved or served while Closed
// Eyes is on. A served copy counts as out of date, so it is rebuilt behind the
// read. Files are 0600 in a 0700 folder under the app data folder.

// derivedSnapshotsSwitch is the rollback switch for snapshots on disk and for
// the prefetch that starts without waiting for the first paint. Set it to
// "memory" to go back to memory-only snapshots that wait for the first paint.
const derivedSnapshotsSwitch = "YANSU_DERIVED_SNAPSHOTS"

func derivedDiskEnabled() bool { return os.Getenv(derivedSnapshotsSwitch) != "memory" }

const (
	derivedDiskDirName = "derived-snapshots"
	// Version 2 adds the sources a snapshot was built from; a version 1
	// copy cannot show they still exist, so it is dropped unread.
	derivedDiskVersion = 2
)

var (
	// derivedDiskMu orders snapshot writes with deletions.
	derivedDiskMu sync.Mutex
	// derivedDiskGeneration advances on every deletion: a write whose build
	// started before one is discarded.
	derivedDiskGeneration atomic.Uint64
)

type derivedDiskFile struct {
	Version int             `json:"v"`
	View    string          `json:"view"`
	Key     string          `json:"key"`
	Stamp   string          `json:"stamp"`
	At      int64           `json:"at"`
	Raw     json.RawMessage `json:"raw"`
	// Sources are the memory, know-how and person ids that existed when the
	// build started. A copy is served only while every one still exists, so
	// a belief deleted in Finder or a person removed while the app was
	// closed never paints from disk.
	Sources *derivedDiskSources `json:"sources"`
}

// derivedDiskSources is the set of ids a snapshot may cite.
type derivedDiskSources struct {
	Memory    []string `json:"memory"`
	Knowledge []string `json:"knowledge"`
	People    []string `json:"people"`
}

func derivedDiskDir() (string, error) {
	root, err := flavorDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, derivedDiskDirName), nil
}

func derivedDiskPath(view derivedViewName, key string) (string, error) {
	dir, err := derivedDiskDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(string(view) + "\x00" + key))
	return filepath.Join(dir, hex.EncodeToString(sum[:12])+".json"), nil
}

// derivedDiskStamp is the privacy stamp that survives a restart. It is not
// available until the saved privacy epoch has been loaded, nor while Closed
// Eyes is on; then nothing is saved or served.
func (a *App) derivedDiskStamp() (string, bool) {
	if a == nil || !derivedDiskEnabled() || !a.pageSnapshotAllowed() {
		return "", false
	}
	mobileBriefPrivacyEpochMu.Lock()
	loaded := mobileBriefPrivacyEpochSavedDir != ""
	epoch := mobileBriefPrivacyEpoch.Load()
	mobileBriefPrivacyEpochMu.Unlock()
	if !loaded {
		return "", false
	}
	markers := "none"
	if dir, err := memoryDir(); err == nil {
		if info, err := os.Stat(filepath.Join(dir, memoryTombstonesFile)); err == nil {
			markers = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
		} else if !os.IsNotExist(err) {
			return "", false
		}
	}
	return fmt.Sprintf("v%d|%d|%s|%s", derivedDiskVersion, epoch, markers, a.currentUILanguage()), true
}

// derivedDiskToken is taken before a build: the stamp and deletion
// generation the saved copy must still match when it is written.
type derivedDiskToken struct {
	stamp      string
	generation uint64
	sources    *derivedDiskSources
	ok         bool
}

func (a *App) derivedDiskToken() derivedDiskToken {
	generation := derivedDiskGeneration.Load()
	stamp, ok := a.derivedDiskStamp()
	if !ok {
		return derivedDiskToken{}
	}
	// Taken before the build: anything deleted while it runs is still in
	// the list, so the saved copy is dropped at its next load.
	sources, ok := a.derivedDiskSourcesNow()
	return derivedDiskToken{stamp: stamp, generation: generation, sources: sources, ok: ok}
}

// derivedDiskSourcesNow lists the memory and know-how files and the stored
// people. ok is false when one cannot be read: then nothing is saved or
// served.
func (a *App) derivedDiskSourcesNow() (*derivedDiskSources, bool) {
	out := &derivedDiskSources{}
	for _, spec := range []struct {
		dir func() (string, error)
		ids *[]string
	}{{memoryDir, &out.Memory}, {knowledgeDir, &out.Knowledge}} {
		dir, err := spec.dir()
		if err != nil {
			return nil, false
		}
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return nil, false
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || isOKFReservedMarkdownFile(e.Name()) {
				continue
			}
			*spec.ids = append(*spec.ids, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	if a.store != nil {
		people, err := a.store.PrincipalIDs(context.Background())
		if err != nil {
			return nil, false
		}
		for id := range people {
			out.People = append(out.People, id)
		}
		sort.Strings(out.People)
	}
	return out, true
}

// derivedDiskSourcesRemain reports whether every id in saved still exists.
func derivedDiskSourcesRemain(saved, now *derivedDiskSources) bool {
	if saved == nil || now == nil {
		return false
	}
	for _, pair := range [][2][]string{{saved.Memory, now.Memory}, {saved.Knowledge, now.Knowledge}, {saved.People, now.People}} {
		have := make(map[string]bool, len(pair[1]))
		for _, id := range pair[1] {
			have[id] = true
		}
		for _, id := range pair[0] {
			if !have[id] {
				return false
			}
		}
	}
	return true
}

// derivedDiskSave writes a built snapshot unless a privacy action happened
// since its build started.
func (a *App) derivedDiskSave(view derivedViewName, key string, raw []byte, at time.Time, token derivedDiskToken) {
	if !token.ok {
		return
	}
	path, err := derivedDiskPath(view, key)
	if err != nil {
		return
	}
	data, err := json.Marshal(derivedDiskFile{Version: derivedDiskVersion, View: string(view), Key: key, Stamp: token.stamp, At: at.UnixMilli(), Raw: raw, Sources: token.sources})
	if err != nil {
		return
	}
	derivedDiskMu.Lock()
	defer derivedDiskMu.Unlock()
	if derivedDiskGeneration.Load() != token.generation {
		return
	}
	if stamp, ok := a.derivedDiskStamp(); !ok || stamp != token.stamp {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	temp, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return
	}
	name := temp.Name()
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		os.Remove(name)
		return
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(name)
		return
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
	}
}

// derivedDiskLoad returns a saved snapshot only when its stamp matches the
// current one. A copy from another stamp is deleted, never served.
func (a *App) derivedDiskLoad(view derivedViewName, key string) ([]byte, time.Time, bool) {
	stamp, ok := a.derivedDiskStamp()
	if !ok {
		return nil, time.Time{}, false
	}
	path, err := derivedDiskPath(view, key)
	if err != nil {
		return nil, time.Time{}, false
	}
	derivedDiskMu.Lock()
	defer derivedDiskMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	var file derivedDiskFile
	if json.Unmarshal(data, &file) != nil || file.Version != derivedDiskVersion ||
		file.View != string(view) || file.Key != key || file.Stamp != stamp || len(file.Raw) == 0 {
		_ = os.Remove(path)
		return nil, time.Time{}, false
	}
	// A source deleted since the build (in the app, in Finder, by a sync
	// tool, while the app was closed) means the copy may still show it.
	if now, ok := a.derivedDiskSourcesNow(); !ok || !derivedDiskSourcesRemain(file.Sources, now) {
		_ = os.Remove(path)
		return nil, time.Time{}, false
	}
	return file.Raw, time.UnixMilli(file.At), true
}

// derivedDiskDropViews deletes the saved snapshots of the given views, every
// key. It runs in the same step as a touch (the person's own change), so a
// quit before the rebuild saves never leaves the old copy to paint the next
// launch.
func derivedDiskDropViews(views ...derivedViewName) {
	if len(views) == 0 {
		return
	}
	dir, err := derivedDiskDir()
	if err != nil {
		return
	}
	want := map[string]bool{}
	for _, view := range views {
		want[string(view)] = true
	}
	derivedDiskMu.Lock()
	defer derivedDiskMu.Unlock()
	// A build that started before this touch must not write its copy back.
	derivedDiskGeneration.Add(1)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var file struct {
			View string `json:"view"`
		}
		if json.Unmarshal(data, &file) != nil || want[file.View] {
			_ = os.Remove(path)
		}
	}
}

// derivedDiskDropAll deletes every saved snapshot. It runs in the same step
// as every privacy action, whether or not the switch is on, so turning the
// switch off never leaves old copies behind.
func derivedDiskDropAll() {
	derivedDiskMu.Lock()
	defer derivedDiskMu.Unlock()
	derivedDiskGeneration.Add(1)
	if dir, err := derivedDiskDir(); err == nil {
		_ = os.RemoveAll(dir)
	}
}
