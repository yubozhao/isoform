package main

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"wails-gui/storage"
)

// A fresh activity store runs about 80 migrations (about 0.3 s) and a fresh
// sessions store about 50 ms of them. The test binary migrates one database of
// each once and starts every fresh store from a copy (a few milliseconds).
// Tests that prove an empty database migrates call withoutStoreSeeds.
type storeSeed struct {
	once  sync.Once
	dir   string
	path  string
	err   error
	build func(dir string) (string, error)
}

var (
	storeSeedsOff atomic.Int32
	activitySeed  = &storeSeed{build: func(dir string) (string, error) {
		store, err := NewActivityStore(dir)
		if err != nil {
			return "", err
		}
		// Closing the last connection checkpoints the WAL into the file.
		return filepath.Join(dir, "activity.db"), store.Close()
	}}
	sessionsSeed = &storeSeed{build: func(dir string) (string, error) {
		path := filepath.Join(dir, "sessions.db")
		store, err := storage.New(path)
		if err != nil {
			return "", err
		}
		return path, store.Close()
	}}
)

func installStoreSeeds() {
	activityStoreSeed = activitySeed.fill
	storage.NewDatabaseSeed = sessionsSeed.fill
}

func (s *storeSeed) fill(dbPath string) {
	if storeSeedsOff.Load() > 0 {
		return
	}
	s.once.Do(s.prepare)
	if s.err != nil {
		return
	}
	if err := copySeedFile(s.path, dbPath); err != nil {
		_ = os.Remove(dbPath)
	}
}

func (s *storeSeed) prepare() {
	storeSeedsOff.Add(1)
	defer storeSeedsOff.Add(-1)
	dir, err := os.MkdirTemp("", "store-seed-")
	if err != nil {
		s.err = err
		return
	}
	s.dir = dir
	path, err := s.build(dir)
	if err == nil {
		if _, statErr := os.Stat(path + "-wal"); statErr == nil {
			err = os.ErrExist
		}
	}
	s.path, s.err = path, err
}

func removeStoreSeeds() {
	for _, s := range []*storeSeed{activitySeed, sessionsSeed} {
		if s.dir != "" {
			_ = os.RemoveAll(s.dir)
		}
	}
}

// withoutStoreSeeds makes this test's fresh stores run every migration, for
// tests about migrating an empty database.
func withoutStoreSeeds(t testing.TB) {
	t.Helper()
	storeSeedsOff.Add(1)
	t.Cleanup(func() { storeSeedsOff.Add(-1) })
}

func copySeedFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}
