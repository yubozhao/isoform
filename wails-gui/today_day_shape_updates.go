package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

const todayDayShapeCoalesce = 50 * time.Millisecond

type todayDayShapeUpdateState struct {
	mu        sync.Mutex
	published string
	pending   string
	timer     *time.Timer
}

// Compare only the visible calendar and hold state. The moving clock and
// unrelated brief prose must not turn periodic syncs into strip updates.
// Read synchronously: the coalescing timer never touches application stores.
func (a *App) todayDayShapeFingerprint() (string, bool) {
	if a == nil || a.store == nil {
		return "", false
	}
	now := a.todayDayShapeNow()
	var brief MobileBriefV1
	if svc, ok := a.mobileBriefSource().(*mobileBriefService); ok {
		brief, _ = svc.store.latest()
		if current, found := svc.store.currentToday(); found && current.LocalDate == now.In(time.Local).Format("2006-01-02") && brief.LocalDate != current.LocalDate {
			brief = current
		}
	}
	days := todayDayShapeDates(now)
	shapes := map[string]TodayDayShape{}
	visible := false
	for _, day := range days {
		key := day.Format("2006-01-02") + "|" + day.Location().String()
		shape, err := a.todayDayShapeAt(day, now, brief)
		if err != nil {
			return "", false
		}
		shape.Now = ""
		visible = visible || shape.CalendarConnected || shape.Hold != nil
		shapes[key] = shape
	}
	raw, err := json.Marshal(shapes)
	if err != nil {
		return "", false
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), visible
}

// A read establishes the baseline without emitting an update for itself.
func (a *App) primeTodayDayShapeUpdates() {
	state := &a.todayDayShapeUpdates
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.published == "" && state.timer == nil {
		state.published, _ = a.todayDayShapeFingerprint()
	}
}

func (a *App) noteTodayDayShapeUpdate() {
	if a == nil || a.store == nil {
		return
	}
	if a.derivedViews().active() {
		// The day shape is a snapshot now: the scheduled build compares and
		// publishes today-dayshape:updated; a source event never builds it.
		return
	}
	state := &a.todayDayShapeUpdates
	state.mu.Lock()
	defer state.mu.Unlock()
	fingerprint, visible := a.todayDayShapeFingerprint()
	if fingerprint == "" {
		return
	}
	if state.published == "" && !visible {
		state.published = fingerprint
	}
	state.pending = fingerprint
	if fingerprint == state.published || state.timer != nil {
		return
	}
	state.timer = time.AfterFunc(todayDayShapeCoalesce, func() {
		state.mu.Lock()
		state.timer = nil
		changed := state.pending != state.published
		state.published = state.pending
		state.mu.Unlock()
		if changed {
			a.emitBrowserEvent("today-dayshape:updated")
		}
	})
}
