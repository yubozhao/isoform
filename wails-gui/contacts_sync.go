package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

const contactsSyncPausedKey = "contacts_sync_paused"

// One worker owns enumeration and import. Startup and grant requests coalesce;
// cancellation covers the native enumeration and every database operation.
// This worker never queues person research or asks for OS permission.
type contactsSyncScheduler struct {
	importer contactsImporter
	source   contactSource
	allowed  func() bool
	notify   func()
	wake     chan struct{}
	mu       sync.Mutex
	cancel   context.CancelFunc
	state    string
}

func (s *contactsSyncScheduler) request() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *contactsSyncScheduler) cancelCurrent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *contactsSyncScheduler) status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *contactsSyncScheduler) run(ctx context.Context, ticks <-chan time.Time) {
	s.request()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		case <-s.wake:
		}
		if ctx.Err() != nil {
			return
		}
		s.refresh(ctx)
	}
}

func (s *contactsSyncScheduler) refresh(parent context.Context) {
	if !hasCurrentContactsConsent(s.importer.consentPath) || !s.allowed() {
		return
	}
	paused, err := s.importer.store.GetUIState(parent, contactsSyncPausedKey)
	if err != nil || paused == "true" {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.cancel = cancel
	s.state = "syncing"
	s.mu.Unlock()
	s.notify()
	im := s.importer
	im.incremental = true
	result, err := im.importContacts(ctx, s.source, nil, contactsConsentVersion)
	cancel()
	s.mu.Lock()
	s.cancel = nil
	s.state = "idle"
	if err != nil && !errors.Is(err, context.Canceled) || len(result.Errors) > 0 {
		s.state = "error"
	}
	s.mu.Unlock()
	s.notify()
}

func (a *App) startContactsSync() {
	if !systemContactsSupported() {
		return
	}
	a.contactsSyncMu.Lock()
	defer a.contactsSyncMu.Unlock()
	if a.contactsSync != nil {
		a.contactsSync.request()
		return
	}
	im, err := a.newContactsImporter()
	if err != nil {
		return
	}
	parent := a.ctx
	if parent == nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s := &contactsSyncScheduler{importer: im, source: systemContactsSource{}, allowed: contactsAccessGranted, wake: make(chan struct{}, 1), notify: func() { a.emitBrowserEvent("contacts:updated") }}
	a.contactsSync = s
	a.contactsSyncStop = cancel
	a.contactsSyncDone = make(chan struct{})
	done := a.contactsSyncDone
	go func() {
		defer close(done)
		ticker := time.NewTicker(cadenceContacts)
		defer ticker.Stop()
		s.run(ctx, ticker.C)
	}()
}

func (a *App) CancelContactsSync() {
	a.contactsSyncMu.Lock()
	defer a.contactsSyncMu.Unlock()
	if a.contactsSync != nil {
		a.contactsSync.cancelCurrent()
	}
}

func (a *App) stopContactsSync() {
	a.contactsSyncMu.Lock()
	stop, done := a.contactsSyncStop, a.contactsSyncDone
	a.contactsSyncMu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

func (a *App) resumeContactsSync() error {
	if a.store != nil {
		if err := a.store.SetUIState(context.Background(), contactsSyncPausedKey, ""); err != nil {
			return err
		}
	}
	a.startContactsSync()
	return nil
}
