package main

import (
	"errors"
	"sync"

	"wails-gui/storage"
)

// StorageStartupStatus lets the frontend localize storage failures without
// parsing logs or exposing an older binary to a newer database.
type StorageStartupStatus struct {
	Code             string `json:"code"`
	StoredVersion    int    `json:"storedVersion"`
	SupportedVersion int    `json:"supportedVersion"`
}

func storageStartupStatus(err error) StorageStartupStatus {
	if err == nil {
		return StorageStartupStatus{}
	}
	if errors.Is(err, errStartupCutRecovery) {
		return StorageStartupStatus{Code: "cut_recovery_failed"}
	}
	var versionErr *storage.SchemaVersionError
	if errors.As(err, &versionErr) {
		return StorageStartupStatus{
			Code:             "schema_too_new",
			StoredVersion:    versionErr.StoredVersion,
			SupportedVersion: versionErr.SupportedVersion,
		}
	}
	return StorageStartupStatus{Code: "storage_unavailable"}
}

// storageStartupState keeps the status readable while a snapshot or migration
// runs. The lifecycle lock fences publication with shutdown. Optional services
// have a separate join so a slow initializer cannot hold foreground flushing.
type storageStartupState struct {
	mu            sync.Mutex
	lifecycle     sync.Mutex
	started       bool
	done          chan struct{}
	servicesDone  chan struct{}
	status        StorageStartupStatus
	stopped       bool
	quitRequested bool
}

func newStorageStartupState() *storageStartupState {
	return &storageStartupState{
		done:   make(chan struct{}),
		status: StorageStartupStatus{Code: "checking"},
	}
}

func (s *storageStartupState) snapshot() StorageStartupStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *storageStartupState) start(open func() (*storage.Store, error), publish func(*storage.Store), startServices func()) {
	s.mu.Lock()
	if s.stopped && !s.started {
		// Stopped before it began: nothing opens, but waiters on done still finish.
		s.started = true
		close(s.done)
		s.mu.Unlock()
		return
	}
	if s.stopped || s.started && s.status.Code != "cut_recovery_failed" {
		s.mu.Unlock()
		return
	}
	if s.started {
		s.done = make(chan struct{})
	}
	done := s.done
	s.started = true
	s.status = StorageStartupStatus{Code: "checking"}
	s.mu.Unlock()
	go func() {
		defer close(done)
		store, err := open()
		s.lifecycle.Lock()
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			s.lifecycle.Unlock()
			if store != nil {
				_ = store.Close()
			}
			return
		}
		if err == nil {
			publish(store)
		}
		s.mu.Lock()
		s.status = storageStartupStatus(err)
		s.mu.Unlock()
		if err == nil {
			s.servicesDone = make(chan struct{})
			// Wails and mandatory storage are ready. Optional services below
			// may download models for minutes; they must not hold the update
			// acknowledgement hostage to network speed. Failed, pending or
			// stopped storage startup must never acknowledge an update.
			acknowledgeUpdateStartup()
			// The same health signal clears a quit install's trial and its kept previous app.
			if home, err := flavorDataDir(); err == nil {
				ackQuitInstallTrial(home, Version, findAppBundle())
			}
		}
		s.lifecycle.Unlock()
		if err == nil {
			defer close(s.servicesDone)
			startServices()
		}
	}()
}

// stop fences late publication and returns the optional-service join. Callers
// can flush foreground data before waiting on an active native initializer.
func (s *storageStartupState) stop() <-chan struct{} {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	return s.servicesDone
}

func (s *storageStartupState) requestQuit() {
	s.mu.Lock()
	s.quitRequested = true
	s.mu.Unlock()
}

func (s *storageStartupState) keepRunningOnClose() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.Code == "" && !s.quitRequested && !s.stopped
}
