package main

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// HoldThing carries the source of a user-visible work item, not calendar data.
type HoldThing struct {
	Title      string `json:"title"`
	SourceKind string `json:"sourceKind"`
	SourceID   string `json:"sourceId"`
}

type HoldProposal struct {
	Existing   *HoldStatus     `json:"existing,omitempty"`
	Dismissed  bool            `json:"dismissed,omitempty"`
	ID         string          `json:"id"`
	Thing      HoldThing       `json:"thing"`
	CalendarID string          `json:"calendarId"`
	Slots      []FindTimeRange `json:"slots"`
}

type HoldStatus struct {
	ID            string `json:"id"`
	TaskID        string `json:"taskId,omitempty"`
	State         string `json:"state"`
	CalendarID    string `json:"calendarId"`
	Title         string `json:"title"`
	Start         string `json:"start"`
	End           string `json:"end"`
	WriteAfter    string `json:"writeAfter"`
	RemovedAt     string `json:"removedAt"`
	FailureReason string `json:"failureReason"`
	// ConflictTitle and ConflictStart name the event that filled the chosen slot.
	ConflictTitle string `json:"conflictTitle,omitempty"`
	ConflictStart string `json:"conflictStart,omitempty"`
}

// holdSubjectKey uses the brief's stable source hash for commitment items.
// This links the same Purpose source across Chat, the brief and Your week
// without replacing the recorded surface and source ID.
func holdSubjectKey(thing HoldThing) string {
	switch thing.SourceKind {
	case "promise":
		return mobileBriefItemKey("commitments", "purpose", thing.SourceID)
	case "brief", "week":
		const prefix = "commitments."
		if strings.HasPrefix(thing.SourceID, prefix) {
			hash := strings.TrimPrefix(thing.SourceID, prefix)
			if len(hash) >= 16 {
				if _, err := hex.DecodeString(hash[:16]); err == nil && (len(hash) == 16 || hash[16] == '-') {
					return prefix + hash[:16]
				}
			}
		}
	}
	return thing.SourceKind + ":" + thing.SourceID
}

func normalizeHoldThing(thing HoldThing) HoldThing {
	thing.Title = strings.TrimSpace(thing.Title)
	if title := []rune(thing.Title); len(title) > 40 {
		thing.Title = strings.TrimSpace(string(title[:40]))
	}
	return thing
}

func validHoldThing(thing HoldThing) bool {
	if strings.TrimSpace(thing.Title) != thing.Title || thing.Title == "" || utf8.RuneCountInString(thing.Title) > 40 || thing.SourceID == "" {
		return false
	}
	switch thing.SourceKind {
	case "brief", "task", "promise", "week", "up_next":
		return true
	default:
		return false
	}
}

func (a *App) proposeHold(thing HoldThing, due time.Time, length time.Duration, now time.Time, store holdEventStore, granted bool) (HoldProposal, error) {
	thing = normalizeHoldThing(thing)
	if !validHoldThing(thing) {
		return HoldProposal{}, errHoldUnavailable
	}
	settings, err := a.holdSettings(store, granted)
	if err != nil {
		return HoldProposal{}, err
	}
	if !granted || !settings.OfferHolds || settings.CalendarID == "" {
		return HoldProposal{}, errHoldUnavailable
	}
	windows, err := a.holdWindows(now, due, length, granted)
	if err != nil {
		return HoldProposal{}, err
	}
	slots, err := holdSlots(windows, now, due, length)
	if err != nil {
		return HoldProposal{}, err
	}
	return HoldProposal{ID: uuid.NewString(), Thing: thing, CalendarID: settings.CalendarID, Slots: slots}, nil
}

// ProposeHold reads available time and never writes or requests permission.
// due is an optional RFC3339 time and lengthMinutes is zero for one hour.
func (a *App) ProposeHold(thing HoldThing, due string, lengthMinutes int) (HoldProposal, error) {
	return a.proposeHoldWithAccess(thing, due, lengthMinutes, calendarAccessGranted())
}

func (a *App) proposeHoldWithAccess(thing HoldThing, due string, lengthMinutes int, granted bool) (HoldProposal, error) {
	thing = normalizeHoldThing(thing)
	if !validHoldThing(thing) {
		return HoldProposal{}, errHoldUnavailable
	}
	var deadline time.Time
	var err error
	if due != "" {
		deadline, err = time.Parse(time.RFC3339, due)
		if err != nil {
			return HoldProposal{}, errHoldUnavailable
		}
	}
	if lengthMinutes < 0 || lengthMinutes > 24*60 {
		return HoldProposal{}, errHoldUnavailable
	}
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return HoldProposal{}, err
	}
	settings, err := a.holdSettings(c.calendar, granted)
	if err != nil || !granted || !settings.OfferHolds {
		return HoldProposal{}, errHoldUnavailable
	}
	c.mu.Lock()
	now := c.now()
	if err := c.releaseStarted(now); err != nil {
		c.mu.Unlock()
		return HoldProposal{}, err
	}
	dismissed, err := c.offerDismissed(thing, now)
	if err != nil {
		c.mu.Unlock()
		return HoldProposal{}, err
	}
	if dismissed {
		c.mu.Unlock()
		return HoldProposal{Dismissed: true, Slots: []FindTimeRange{}}, nil
	}
	existing, err := c.activeForSource(thing, now)
	c.mu.Unlock()
	if err != nil {
		return HoldProposal{}, err
	}
	if existing != nil {
		view := statusForHold(*existing)
		return HoldProposal{ID: existing.ID, Thing: thing, CalendarID: existing.CalendarID, Existing: &view, Slots: []FindTimeRange{{Start: view.Start, End: view.End}}}, nil
	}
	return a.proposeHold(thing, deadline, time.Duration(lengthMinutes)*time.Minute, time.Now(), c.calendar, granted)
}

func (a *App) currentHoldCoordinator() (*holdCoordinator, error) {
	if a == nil {
		return nil, errors.New("holds are not ready")
	}
	a.holdsMu.Lock()
	defer a.holdsMu.Unlock()
	if a.holds == nil {
		return nil, errors.New("holds are not ready")
	}
	return a.holds, nil
}

func statusForHold(h holdRecord) HoldStatus {
	out := HoldStatus{ID: h.ID, State: h.State, CalendarID: h.CalendarID, Title: h.Title, Start: h.Start.Format(time.RFC3339), End: h.End.Format(time.RFC3339), FailureReason: h.FailureReason}
	if !h.WriteAfter.IsZero() {
		out.WriteAfter = h.WriteAfter.Format(time.RFC3339Nano)
	}
	if !h.RemovedAt.IsZero() {
		out.RemovedAt = h.RemovedAt.Format(time.RFC3339Nano)
	}
	return out
}

// HoldTime is called only on the person's Hold. The coordinator rechecks the
// complete window and persists the five-second cancellation deadline.
func (a *App) HoldTime(proposal HoldProposal, chosen FindTimeRange, calendarID string) (HoldStatus, error) {
	// The Today strip shows the person's own hold change at once.
	defer a.touchDerivedViews(derivedTodayDayShape)
	return a.holdTimeWithAccess(proposal, chosen, calendarID, calendarAccessGranted())
}

func (a *App) holdTimeWithAccess(proposal HoldProposal, chosen FindTimeRange, calendarID string, granted bool) (HoldStatus, error) {
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return HoldStatus{}, err
	}
	if !validHoldThing(proposal.Thing) || proposal.ID == "" || calendarID == "" {
		return HoldStatus{}, errHoldUnavailable
	}
	selected := false
	for _, slot := range proposal.Slots {
		if slot == chosen {
			selected = true
			break
		}
	}
	if !selected {
		return HoldStatus{}, errHoldUnavailable
	}
	start, startErr := time.Parse(time.RFC3339, chosen.Start)
	end, endErr := time.Parse(time.RFC3339, chosen.End)
	if startErr != nil || endErr != nil {
		return HoldStatus{}, errHoldUnavailable
	}
	settings, err := a.holdSettings(c.calendar, granted)
	if err != nil || !granted || !settings.OfferHolds {
		return HoldStatus{}, errHoldUnavailable
	}
	writable := false
	for _, calendar := range settings.Calendars {
		if calendar.ID == calendarID {
			writable = true
			break
		}
	}
	if !writable {
		if settings.CalendarID != "" && settings.CalendarID != calendarID {
			return HoldStatus{State: "calendar_unavailable", CalendarID: settings.CalendarID}, nil
		}
		return HoldStatus{}, errHoldUnavailable
	}
	h := holdRecord{ID: proposal.ID, CalendarID: calendarID, Title: proposal.Thing.Title, Start: start, End: end, SourceKind: proposal.Thing.SourceKind, SourceID: proposal.Thing.SourceID}
	if err := c.begin(h); err != nil {
		if !errors.Is(err, errHoldUnavailable) {
			return HoldStatus{}, err
		}
		h.CreatedAt = c.now()
		c.report(h, "failed", "slot_unavailable")
		conflict := HoldStatus{State: "conflict"}
		if raw, readErr := c.calendar.ReadWindow(start, end.Add(10*time.Minute)); readErr == nil {
			if blocker, blocked, _ := holdWindowBlocker(raw, holdEvent{CalendarID: calendarID, Title: h.Title, Start: start, End: end}); blocked {
				conflict.ConflictTitle, conflict.ConflictStart = blocker.Title, blocker.Start.Format(time.RFC3339)
			}
		}
		// Recheck the other offered slots against both the cached coverage and
		// EventKit. No calendar write happens while looking for an alternative.
		for index, candidate := range proposal.Slots {
			if index >= 4 {
				break
			}
			if candidate == chosen {
				continue
			}
			candidateStart, startErr := time.Parse(time.RFC3339, candidate.Start)
			candidateEnd, endErr := time.Parse(time.RFC3339, candidate.End)
			if startErr != nil || endErr != nil {
				continue
			}
			event := holdEvent{CalendarID: calendarID, Title: proposal.Thing.Title, Start: candidateStart, End: candidateEnd}
			if c.verifyCoverage != nil && c.verifyCoverage(event) == nil && checkHoldWindow(c.calendar, event, c.now()) == nil {
				conflict.Start, conflict.End = candidate.Start, candidate.End
				return conflict, nil
			}
		}
		return conflict, nil
	}
	return a.GetHoldStatus(h.ID)
}

func (a *App) GetHoldStatus(id string) (HoldStatus, error) {
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return HoldStatus{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h, err := c.record(id)
	if err != nil {
		return HoldStatus{}, err
	}
	return statusForHold(h), nil
}

func (a *App) UndoHold(id string) error {
	// The Today strip shows the person's own hold change at once.
	defer a.touchDerivedViews(derivedTodayDayShape)
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return err
	}
	return c.undo(id)
}
func (a *App) RemoveHold(id string) error {
	// The Today strip shows the person's own hold change at once.
	defer a.touchDerivedViews(derivedTodayDayShape)
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return err
	}
	return c.remove(id)
}
func (a *App) UndoRemoveHold(id string) error {
	// The Today strip shows the person's own hold change at once.
	defer a.touchDerivedViews(derivedTodayDayShape)
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return err
	}
	return c.undoRemove(id)
}
