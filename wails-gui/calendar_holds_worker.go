package main

import (
	"context"
	"log"
	"time"

	"wails-gui/internal/calendar"
	"wails-gui/internal/engagement"
)

// runHoldWorker resumes saved pending writes and follows calendar changes.
// The caller owns the ticker and waits for this loop before closing the store.
func runHoldWorker(ctx context.Context, coordinator *holdCoordinator, ticks <-chan time.Time, changes <-chan struct{}) {
	runDue := func() {
		if err := coordinator.recoverWrites(); err != nil {
			log.Printf("[holds] recover write: %v", err)
			return
		}
		if err := coordinator.runDue(); err != nil {
			log.Printf("[holds] pending write: %v", err)
		}
	}
	runDue()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			runDue()
		case <-changes:
			if err := coordinator.reconcile(); err != nil {
				log.Printf("[holds] reconcile: %v", err)
			}
		}
	}
}

// holdSlotCovered checks the entire proposed interval against cached busy
// events and quiet hours, without limiting validation to the first windows.
func holdSlotCovered(event holdEvent, events []calendar.Event, quiet engagement.QuietWindow) bool {
	if !event.End.After(event.Start) {
		return false
	}
	for at := event.Start; at.Before(event.End); at = at.Add(time.Minute) {
		if quiet.Contains(at) {
			return false
		}
	}
	blocks, err := calendar.FreeBlocks(events, event.Start, event.End, event.End.Sub(event.Start))
	return err == nil && len(blocks) == 1 && !blocks[0].Start.After(event.Start) && !blocks[0].End.Before(event.End)
}

func (a *App) holdCoverageWithAccess(event holdEvent, now time.Time, granted bool) error {
	if _, err := a.findTimeWithAccess(context.Background(), now, granted); err != nil {
		return errHoldCoverageUnknown
	}
	events, err := a.listCalendarEvents(context.Background(), event.Start, event.End)
	if err != nil {
		return errHoldCoverageUnknown
	}
	quiet := engagement.DefaultPrefs().Quiet
	if a.engagement != nil {
		quiet = a.engagement.Prefs().Quiet
	}
	if !holdSlotCovered(event, events, quiet) {
		return errHoldUnavailable
	}
	return nil
}

func (a *App) startHoldWorker(parent context.Context) {
	// No background loop while holds are off (feature_switches.go).
	if a == nil || a.activity == nil || a.activity.store == nil || !featureOn(featureCalendarHolds) {
		return
	}
	a.holdsMu.Lock()
	defer a.holdsMu.Unlock()
	if a.holds != nil {
		return
	}
	coordinator := &holdCoordinator{
		db:       a.activity.store.db,
		calendar: eventKitHoldStore{},
		now:      time.Now,
		verifyCoverage: func(event holdEvent) error {
			return a.holdCoverageWithAccess(event, time.Now(), calendarAccessGranted())
		},
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	a.holds, a.holdsCancel, a.holdsDone = coordinator, cancel, done
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		runHoldWorker(ctx, coordinator, ticker.C, holdsStoreChanged)
	}()
}

func (a *App) stopHoldWorker() {
	a.holdsMu.Lock()
	cancel, done := a.holdsCancel, a.holdsDone
	a.holds, a.holdsCancel, a.holdsDone = nil, nil, nil
	a.holdsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
