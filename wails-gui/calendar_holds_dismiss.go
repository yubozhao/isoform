package main

import (
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

func migrateCalendarHoldDismissals(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS hold_offer_dismissals (
		id TEXT PRIMARY KEY,
		source_kind TEXT NOT NULL,
		source_id TEXT NOT NULL,
		subject_key TEXT NOT NULL,
		local_day TEXT NOT NULL,
		dismissed_at INTEGER NOT NULL,
		UNIQUE(subject_key, local_day)
	)`)
	return err
}

func (c *holdCoordinator) offerDismissed(thing HoldThing, now time.Time) (bool, error) {
	var count int
	err := c.db.QueryRow(`SELECT count(*) FROM hold_offer_dismissals WHERE subject_key = ? AND local_day = ?`, holdSubjectKey(thing), now.In(time.Local).Format(time.DateOnly)).Scan(&count)
	return count > 0, err
}

func (c *holdCoordinator) dismissOffer(thing HoldThing) (string, error) {
	if !validHoldThing(thing) {
		return "", errHoldUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	id := uuid.NewString()
	_, err := c.db.Exec(`INSERT INTO hold_offer_dismissals (id, source_kind, source_id, subject_key, local_day, dismissed_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(subject_key, local_day) DO UPDATE SET id=excluded.id, dismissed_at=excluded.dismissed_at`, id, thing.SourceKind, thing.SourceID, holdSubjectKey(thing), now.In(time.Local).Format(time.DateOnly), now.UnixMilli())
	return id, err
}

func (c *holdCoordinator) undoDismissOffer(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	result, err := c.db.Exec(`DELETE FROM hold_offer_dismissals WHERE id = ? AND dismissed_at <= ? AND dismissed_at > ?`, id, now.UnixMilli(), now.Add(-5*time.Second).UnixMilli())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("dismissal can no longer be undone")
	}
	return nil
}

// DismissHoldOffer hides this item's offer for the rest of the local day.
func (a *App) DismissHoldOffer(thing HoldThing) (string, error) {
	// The Today strip shows the person's own hold change at once.
	defer a.touchDerivedViews(derivedTodayDayShape)
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return "", err
	}
	return c.dismissOffer(thing)
}

// UndoDismissHoldOffer restores an offer during the five-second Undo window.
func (a *App) UndoDismissHoldOffer(id string) error {
	// The Today strip shows the person's own hold change at once.
	defer a.touchDerivedViews(derivedTodayDayShape)
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return err
	}
	return c.undoDismissOffer(id)
}
