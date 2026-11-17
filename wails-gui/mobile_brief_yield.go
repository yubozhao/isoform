package main

import (
	"context"
	"log"
	"time"
)

// A brief made ahead of its slot carries the relay's own scheduled push, for a
// computer that sleeps through the slot (PROTOCOL.md 8.2). An awake computer
// decides at the slot where the brief rings (its brief_ready moment, held until
// then, goes through the router: notify v1.0.0 rule 2), so shortly before the
// slot it asks the relay to drop that push: one place rings, never both.

const (
	// mobileBriefYieldLead is how long before the slot the relay's push yields.
	mobileBriefYieldLead = 90 * time.Second
	mobileBriefYieldTick = 30 * time.Second
)

// takeYields marks and returns the early briefs whose slot is within lead,
// whose push is still owed, and that haven't yielded yet.
func (o *mobileBriefOutbox) takeYields(now time.Time, lead time.Duration) []mobileBriefOutboxEntry {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []mobileBriefOutboxEntry
	for id, entry := range o.entries {
		if !entry.Notify || entry.DeliverAt == "" || entry.Yielded {
			continue
		}
		at := mobileBriefParseTime(entry.DeliverAt)
		if at.IsZero() || at.Sub(now) > lead || now.Sub(at) > lead {
			continue
		}
		entry.Yielded = true
		o.entries[id] = entry
		out = append(out, entry)
	}
	if len(out) > 0 {
		o.persistLocked()
	}
	return out
}

// yieldMadeAheadBriefPushes asks the relay to drop the scheduled pushes due
// soon. A relay that doesn't know the frame refuses it and stays connected,
// and the phone may then get both.
func (a *App) yieldMadeAheadBriefPushes(now time.Time) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return
	}
	bridge.mu.Lock()
	client := bridge.client
	bridge.mu.Unlock()
	if client == nil {
		return
	}
	for _, entry := range bridge.briefOutbox.takeYields(now, mobileBriefYieldLead) {
		if err := client.send(map[string]any{"t": "brief_yield", "deviceId": entry.DeviceID}); err != nil {
			log.Printf("[mobile-brief] the early brief's push could not yield: %v", err)
			continue
		}
		log.Printf("[engagement] route kind=brief_ready channel=router reason=awake_at_slot brief=%s", entry.BriefID)
	}
}

// runBriefYields checks every 30 seconds until ctx ends.
func (a *App) runBriefYields(ctx context.Context) {
	ticker := time.NewTicker(mobileBriefYieldTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.yieldMadeAheadBriefPushes(now)
		}
	}
}
