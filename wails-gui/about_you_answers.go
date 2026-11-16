package main

import (
	"errors"
	"log"
	"strings"
	"time"
)

// The answers a belief takes on About you, the portrait and New this week
// (answer feedback v1.0.0 section 11), and the Undo each keeps until the
// person leaves the page. That's me is ConfirmMemoryEntry; these are the
// rest.

// UndoConfirmMemoryEntry is Undo after That's me on a belief.
func (a *App) UndoConfirmMemoryEntry(id string) error {
	if err := a.unconfirmMemoryEntry(id); err != nil {
		return err
	}
	a.ledgerBeliefUndo(id)
	return nil
}

// NotQuiteMemoryEntry is Not quite left blank: the belief stops being used
// and keeps its place in About you. It returns the confirmation it took back,
// for Undo.
func (a *App) NotQuiteMemoryEntry(id string) (int64, error) {
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return 0, err
	}
	confirmedAt := memoryConfirmedAt(entry.Content)
	if err := a.markMemoryNotQuite(id); err != nil {
		return 0, err
	}
	a.ledgerBeliefAnswer(id, entry.Content, workActionNo)
	return confirmedAt, nil
}

// UndoNotQuiteMemoryEntry takes back Not quite and restores the confirmation
// it had (0 for none).
func (a *App) UndoNotQuiteMemoryEntry(id string, confirmedAt int64) error {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return err
	}
	if memoryNotQuiteAt(entry.Content) == 0 {
		return nil
	}
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	if err := checkMemoryWriteAllowed(dir, id, entry.Content, false); err != nil {
		return err
	}
	content := clearMemoryNotQuite(entry.Content)
	if confirmedAt > 0 {
		content, err = setMemoryConfirmedAt(content, time.UnixMilli(confirmedAt))
	} else {
		// An envelope that held only the answer goes with it.
		content, err = clearMemoryConfirmation(content)
	}
	if err != nil {
		return err
	}
	if err := writeMemoryFileAtomically(dir, id, content); err != nil {
		return err
	}
	a.markMemoryDirty("not_quite_undo")
	a.ledgerBeliefUndo(id)
	return nil
}

// RewordMemoryEntry is Not quite with the person's own words (Edit on a
// claim): it saves the reworded belief as a user edit and keeps the answer in
// the answers ledger as an Edit, with the words before and after.
func (a *App) RewordMemoryEntry(id, content, wording string) (MemoryEntry, error) {
	before, err := a.GetMemoryEntry(id)
	if err != nil {
		return MemoryEntry{}, err
	}
	saved, err := a.SaveMemoryEntry(id, content)
	if err != nil {
		return saved, err
	}
	old := ""
	if points := parseMemoryPoints(before.Content); len(points) > 0 {
		old = strings.TrimSpace(points[0])
	}
	if _, err := a.recordAnswer(AnswerRecord{ItemID: "belief:" + id, Kind: answerKindBelief, Source: answerSourceAboutYou, Title: old, Action: workActionEdit,
		Edited: &WorkEdited{Before: old, After: strings.TrimSpace(wording)}, Surface: workSurfaceComputer}); err != nil {
		log.Printf("[answers] a belief's new wording was not kept: %v", err)
	}
	return saved, nil
}

var errMemoryWordingChanged = errors.New("memory_wording_changed")

// UndoMemoryWording is Undo after Not quite with words: it puts back the
// version the save kept in history, while the belief still reads as the
// person worded it.
func (a *App) UndoMemoryWording(id, wording string) (MemoryEntry, error) {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return MemoryEntry{}, err
	}
	points := parseMemoryPoints(entry.Content)
	if len(points) == 0 || strings.TrimSpace(points[0]) != strings.TrimSpace(wording) {
		return MemoryEntry{}, errMemoryWordingChanged
	}
	versions, err := a.ListMemoryVersions(id)
	if err != nil {
		return MemoryEntry{}, err
	}
	if len(versions) == 0 {
		return MemoryEntry{}, errMemoryWordingChanged
	}
	before, err := a.GetMemoryVersion(id, versions[0].TimestampMs)
	if err != nil {
		return MemoryEntry{}, err
	}
	dir, err := memoryDir()
	if err != nil {
		return MemoryEntry{}, err
	}
	if err := checkMemoryWriteAllowed(dir, id, before, false); err != nil {
		return MemoryEntry{}, err
	}
	if err := snapshotMemoryFile(dir, id); err != nil {
		return MemoryEntry{}, err
	}
	if err := writeMemoryFileAtomically(dir, id, before); err != nil {
		return MemoryEntry{}, err
	}
	a.markMemoryDirty("wording_undo")
	a.ledgerBeliefUndo(id)
	return a.GetMemoryEntry(id)
}

// ledgerBeliefAnswer keeps a belief's That's me (yes) or Not quite (no) in the
// answers ledger. The ledger never blocks the answer.
func (a *App) ledgerBeliefAnswer(id, content, action string) {
	title := ""
	if points := parseMemoryPoints(content); len(points) > 0 {
		title = points[0]
	}
	if _, err := a.recordAnswer(AnswerRecord{ItemID: "belief:" + id, Kind: answerKindBelief, Source: answerSourceAboutYou, Title: title, Action: action,
		Surface: workSurfaceComputer}); err != nil {
		log.Printf("[answers] a belief's answer was not kept: %v", err)
	}
}

func (a *App) ledgerBeliefUndo(id string) {
	if err := a.undoAnswerFor("belief:"+id, answerKindBelief, workSurfaceComputer); err != nil {
		log.Printf("[answers] a belief's Undo was not kept: %v", err)
	}
}
