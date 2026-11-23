package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wails-gui/storage"
)

func awaitStorageStartup(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("storage startup did not finish")
	}
}

func TestStorageStartupWaitsForDatabaseAndPublishesBeforeServices(t *testing.T) {
	state := newStorageStartupState()
	opening := make(chan struct{})
	allowOpen := make(chan struct{})
	servicesStarted := make(chan struct{})
	finishServices := make(chan struct{})
	path := filepath.Join(t.TempDir(), "sessions.db")
	var published *storage.Store
	state.start(func() (*storage.Store, error) {
		close(opening)
		<-allowOpen
		return storage.New(path)
	}, func(store *storage.Store) {
		published = store
	}, func() {
		close(servicesStarted)
		<-finishServices
	})
	awaitStorageStartup(t, opening)
	if got := state.snapshot().Code; got != "checking" {
		t.Fatalf("pending status = %q, want checking", got)
	}
	if state.keepRunningOnClose() {
		t.Fatal("closing a pending startup must quit")
	}
	select {
	case <-servicesStarted:
		t.Fatal("services started before the database opened")
	default:
	}
	close(allowOpen)
	awaitStorageStartup(t, servicesStarted)
	t.Cleanup(func() { _ = published.Close() })
	// A slow model download must not block the status getter or main UI.
	if got := state.snapshot().Code; got != "" {
		t.Fatalf("status during services startup = %q, want ready", got)
	}
	if err := published.SetUIState(t.Context(), "startup-test", "ready"); err != nil {
		t.Fatalf("database was not published before readiness: %v", err)
	}
	if !state.keepRunningOnClose() {
		t.Fatal("ready app should support close-to-tray")
	}
	state.requestQuit()
	if state.keepRunningOnClose() {
		t.Fatal("explicit quit must not hide to tray")
	}
	close(finishServices)
	awaitStorageStartup(t, state.done)
}

func TestStorageStartupNewerDatabaseBlocksServices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", storage.SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	state := newStorageStartupState()
	state.start(func() (*storage.Store, error) {
		return storage.New(path)
	}, func(*storage.Store) {
		t.Error("failed database must not be published")
	}, func() {
		t.Error("services must not start with a newer database")
	})
	awaitStorageStartup(t, state.done)
	want := StorageStartupStatus{Code: "schema_too_new", StoredVersion: storage.SchemaVersion + 1, SupportedVersion: storage.SchemaVersion}
	if got := state.snapshot(); got != want {
		t.Fatalf("failed status = %+v, want %+v", got, want)
	}
	if state.keepRunningOnClose() {
		t.Fatal("closing a failed startup must quit")
	}
}

func TestStorageStartupExistingDatabaseVersionsBecomeReady(t *testing.T) {
	for _, version := range []int{57, 58, 59, 60, 61, 62, 63, 64, 65, 66} {
		t.Run(fmt.Sprintf("schema_%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.db")
			// A populated database exercises the real startup gate and migration
			// path. Historical column-shape fixtures live in storage's tests.
			previous, err := storage.New(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := previous.SetUIState(t.Context(), "startup-test", "preserved"); err != nil {
				_ = previous.Close()
				t.Fatal(err)
			}
			if err := previous.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			state := newStorageStartupState()
			var published *storage.Store
			servicesStarted := false
			state.start(func() (*storage.Store, error) {
				return storage.New(path)
			}, func(store *storage.Store) {
				published = store
			}, func() {
				servicesStarted = true
				if published == nil || state.snapshot().Code != "" {
					t.Error("services started before database publication and readiness")
				}
			})
			awaitStorageStartup(t, state.done)
			if published != nil {
				t.Cleanup(func() { _ = published.Close() })
			}
			if got := state.snapshot(); got != (StorageStartupStatus{}) {
				t.Fatalf("startup status = %+v, want ready", got)
			}
			if published == nil || !servicesStarted {
				t.Fatal("compatible database did not publish and start services")
			}
			if got, err := published.GetUIState(t.Context(), "startup-test"); err != nil || got != "preserved" {
				t.Fatalf("existing data = %q, error %v; want preserved", got, err)
			}
			if !state.keepRunningOnClose() {
				t.Fatal("ready app should support close-to-tray")
			}
			db, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var migratedVersion int
			if err := db.QueryRow("PRAGMA user_version").Scan(&migratedVersion); err != nil {
				t.Fatal(err)
			}
			if migratedVersion != storage.SchemaVersion {
				t.Fatalf("migrated version = %d, want %d", migratedVersion, storage.SchemaVersion)
			}
		})
	}
}

func TestStorageStartupShutdownDuringOpenClosesLateDatabase(t *testing.T) {
	state := newStorageStartupState()
	opening := make(chan struct{})
	allowOpen := make(chan struct{})
	opened := make(chan *storage.Store, 1)
	path := filepath.Join(t.TempDir(), "sessions.db")
	state.start(func() (*storage.Store, error) {
		close(opening)
		<-allowOpen
		store, err := storage.New(path)
		opened <- store
		return store, err
	}, func(*storage.Store) {
		t.Error("database must not be published after shutdown")
	}, func() {
		t.Error("services must not start after shutdown")
	})
	awaitStorageStartup(t, opening)
	stopped := make(chan struct{})
	go func() {
		state.stop()
		close(stopped)
	}()
	awaitStorageStartup(t, stopped)
	close(allowOpen)
	awaitStorageStartup(t, state.done)
	store := <-opened
	if store == nil {
		t.Fatal("test database did not open")
	}
	if _, err := store.GetUIState(t.Context(), "startup-test"); err == nil {
		t.Fatal("late-opened database was not closed")
	}
}

func TestStorageStartupShutdownCanFlushBeforeJoiningServices(t *testing.T) {
	state := newStorageStartupState()
	started, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
		awaitStorageStartup(t, state.done)
	}()
	var published *storage.Store
	path := filepath.Join(t.TempDir(), "sessions.db")
	state.start(func() (*storage.Store, error) {
		return storage.New(path)
	}, func(store *storage.Store) {
		published = store
	}, func() {
		close(started)
		<-release
	})
	awaitStorageStartup(t, started)
	t.Cleanup(func() { _ = published.Close() })
	stopped := make(chan struct{})
	var join <-chan struct{}
	go func() { join = state.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("publication fence waited for optional services before foreground flush")
	}
	if err := published.SetUIState(t.Context(), "foreground-flush", "saved"); err != nil {
		t.Fatal(err)
	}
	if join == nil {
		t.Fatal("active services have no shutdown join")
	}
	select {
	case <-join:
		t.Fatal("active services were not retained through foreground flush")
	default:
	}
	close(release)
	released = true
	awaitStorageStartup(t, join)
	if got, err := published.GetUIState(t.Context(), "foreground-flush"); err != nil || got != "saved" {
		t.Fatalf("foreground flush = %q, %v", got, err)
	}
}

func TestStorageStartupCutDiscoveryOptional(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YANSU_MOBILE_STATE_DIR", "")
	t.Setenv(agentHomeEnv, "invalid-relative-path")
	if _, err := mobileStateDirectory(); err == nil {
		t.Fatal("fixture resolved a mobile directory")
	}
	store, err := openStartupStorageAt(home)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	home = t.TempDir()
	t.Setenv(agentHomeEnv, home)
	if dirs := startupCutDirectories(home); len(dirs) != 0 {
		t.Fatal(dirs)
	}
	if err := recoverStartupWatchCut(home, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}
func TestStorageStartupCutFailureCanRetry(t *testing.T) {
	home := t.TempDir()
	t.Setenv(agentHomeEnv, home)
	t.Setenv("YANSU_MOBILE_STATE_DIR", "")
	// A real published marker with unreadable JSON must keep storage closed.
	stage := filepath.Join(home, "mobile", "recordings", watchForgetJournal)
	if err := os.MkdirAll(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "journal.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	state := newStorageStartupState()
	var opened *storage.Store
	start := func() { state.start(openStartupStorage, func(s *storage.Store) { opened = s }, func() {}) }
	start()
	awaitStorageStartup(t, state.done)
	if got := state.snapshot().Code; got != "cut_recovery_failed" || opened != nil {
		t.Fatal(got)
	}
	if err := os.RemoveAll(stage); err != nil {
		t.Fatal(err)
	}
	start()
	awaitStorageStartup(t, state.done)
	if state.snapshot().Code != "" || opened == nil {
		t.Fatal(state.snapshot())
	}
	opened.Close()
}
