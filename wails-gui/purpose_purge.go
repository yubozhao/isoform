package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Deleting what the Purpose Map learned from a source that was deleted.
//
// The Purpose Map is an append-only ledger: its tables refuse updates and
// deletes. That is right for history and wrong for erasure. When a person
// deletes a meeting's notes or an hour the watch recorded, the commitments
// that were made from that transcript must go too: deleted means deleted, not
// dismissed and hidden.
//
// The erasure itself is the one "Forget this" already uses for memory
// (deleteMemoryForgetDerivatives): inside one transaction it lifts the
// no-delete guards of exactly the tables it touches, deletes a commitment's
// whole tree (the root, its events, their evidence, its relations and their
// events, and the extraction records that point at it), and puts the guards
// back. Other connections never see the exception and a rollback restores
// everything.
//
// What is purged: the commitments discovered from the source, that is, whose
// first event cites it. A commitment that began somewhere else and was later
// reinforced by the source stays: it was not learned from it.

// purposeOpenLoopsDiscoveredFrom returns the open loops whose first event
// cites one of the given sources of that kind.
func purposeOpenLoopsDiscoveredFrom(db *sql.DB, sourceKind string, sourceIDs []string) ([]string, error) {
	if db == nil || len(sourceIDs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(sourceIDs)+1)
	args = append(args, sourceKind)
	for _, id := range sourceIDs {
		args = append(args, id)
	}
	rows, err := db.Query(`
		SELECT DISTINCT event.open_loop_id
		FROM purpose_open_loop_evidence_v1 AS evidence
		JOIN purpose_open_loop_events_v1 AS event ON event.id = evidence.event_id
		WHERE event.sequence = 1 AND evidence.source_kind = ?
		  AND evidence.source_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(sourceIDs)), ",")+`)
		ORDER BY event.open_loop_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// purgePurposeOpenLoops deletes the given open loops and everything that
// hangs on them, for good. It returns how many it was asked to delete.
func (a *App) purgePurposeOpenLoops(loopIDs []string) (int, error) {
	var ids []string
	for _, id := range loopIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if a == nil || a.activity == nil || a.activity.store == nil || a.activity.store.db == nil {
		return 0, errors.New("Purpose storage is unavailable")
	}
	// Like "Forget this": no learning pass may write a result that cites a
	// source while it is being erased.
	memoryMutationMu.Lock()
	defer memoryMutationMu.Unlock()
	tx, err := a.activity.store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := deleteMemoryForgetDerivatives(tx, ids[0], ids[1:]...); err != nil {
		return 0, fmt.Errorf("purge Purpose Map rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// A deleted commitment is a source a sealed brief may cite: seal again.
	briefPrivacyChanged()
	if a.ctx != nil {
		a.emitBrowserEvent("agency:updated")
	}
	return len(ids), nil
}

// purgePurposeLearnedFrom deletes the commitments discovered from the given
// sources (a deleted meeting's sessions, a deleted hour's transcript).
func (a *App) purgePurposeLearnedFrom(sourceKind string, sourceIDs []string) (int, error) {
	if a == nil || a.activity == nil || a.activity.store == nil || a.activity.store.db == nil {
		return 0, nil
	}
	ids, err := purposeOpenLoopsDiscoveredFrom(a.activity.store.db, sourceKind, sourceIDs)
	if err != nil {
		return 0, err
	}
	return a.purgePurposeOpenLoops(ids)
}
