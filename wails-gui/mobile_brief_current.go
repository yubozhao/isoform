package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// The latest brief (PROTOCOL.md section 8.1) is one read, latestShown, and
// every surface uses it: Today, the menu bar, Chat's brief card, the launch
// read and every phone route. It reads the one latest-brief record
// (mobileBriefLatestShown) and that brief's sealed copy (see
// mobile_brief_finished_read.go). The record moves only when a brief with written
// words is sealed, or when the brief it names no longer has words. A quiet slot
// is never latest, and an empty brief never replaces the last one with words
// (design rule R61).
//
// With no record (a fresh store, or one from before this record), the newest
// stored brief with written words is chosen, and with none, the newest brief
// stands in, as before.

// mobileBriefCurrenter is the optional part of a brief source that answers
// the latest brief from the one read.
type mobileBriefCurrenter interface {
	CurrentMobileBrief() (MobileBriefV1, bool)
}

// mobileBriefHasWrittenText reports a brief whose words were written.
func mobileBriefHasWrittenText(brief MobileBriefV1) bool {
	return brief.NarrativeStatus == "ok" && len(brief.Narrative) > 0
}

// mobileBriefChooseCurrent applies the rule to briefs listed newest first.
func mobileBriefChooseCurrent(briefs []MobileBriefV1) (MobileBriefV1, bool) {
	var newest MobileBriefV1
	found := false
	for _, brief := range briefs {
		if brief.NarrativeStatus == "quiet" {
			continue
		}
		if mobileBriefHasWrittenText(brief) {
			return brief, true
		}
		if !found {
			newest, found = brief, true
		}
	}
	return newest, found
}

// current returns the brief the rule chooses from the stored briefs. The
// choice is computed on the first read after a store or removal, then kept.
func (s *mobileBriefStore) current() (MobileBriefV1, bool) {
	if s.dir == "" {
		return MobileBriefV1{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.currentFresh {
		s.currentBrief, s.currentOK = mobileBriefChooseCurrent(s.briefs())
		s.currentFresh = true
	}
	return s.currentBrief, s.currentOK
}

// CurrentMobileBrief is the latest brief as the phone and the Mac's displays
// read it: the same read as Today's (finishedLatest).
func (s *mobileBriefService) CurrentMobileBrief() (MobileBriefV1, bool) {
	brief, ok, err := s.finishedLatest()
	return brief, ok && err == nil
}

// readLatestShown returns the latest-brief record, when there is a valid one.
func (s *mobileBriefStore) readLatestShown() (mobileBriefLatestShown, bool) {
	if s.dir == "" {
		return mobileBriefLatestShown{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.dir, mobileBriefLatestShownName))
	if err != nil {
		return mobileBriefLatestShown{}, false
	}
	var record mobileBriefLatestShown
	if json.Unmarshal(data, &record) != nil || record.Version != 1 || !mobileBriefIDPattern.MatchString(record.BriefID) {
		return mobileBriefLatestShown{}, false
	}
	return record, true
}

// Separate store instances can refer to the same directory.
var mobileBriefLatestShownMu sync.Mutex

// writeLatestShown saves the latest-brief record atomically.
func (s *mobileBriefStore) writeLatestShown(record mobileBriefLatestShown) error {
	if s.dir == "" {
		return errMobileBriefStateUnavailable
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return writeMobileBriefFileAtomic(s.dir, mobileBriefLatestShownName, data)
}

// mobileBriefLatestState describes whether a readable record names a brief.
type mobileBriefLatestState int

const (
	mobileBriefLatestMissing mobileBriefLatestState = iota
	mobileBriefLatestRecorded
)

// Equal timestamps use the same ID order as store.briefs.
type mobileBriefLatestMove int

const (
	mobileBriefLatestKeep mobileBriefLatestMove = iota
	mobileBriefLatestAdvance
)

func mobileBriefLatestTransition(state mobileBriefLatestState, current MobileBriefV1, candidate MobileBriefV1) mobileBriefLatestMove {
	switch state {
	case mobileBriefLatestMissing:
		return mobileBriefLatestAdvance
	case mobileBriefLatestRecorded:
	default:
		return mobileBriefLatestKeep
	}
	oldAt, newAt := mobileBriefAsOfTime(current), mobileBriefAsOfTime(candidate)
	if newAt.After(oldAt) || (newAt.Equal(oldAt) && candidate.BriefID > current.BriefID) {
		return mobileBriefLatestAdvance
	}
	return mobileBriefLatestKeep
}

// noteLatestShown compares and updates under one lock, including when a read
// repairs a stale pointer. Held and non-written briefs never advance it.
func (s *mobileBriefService) noteLatestShown(brief MobileBriefV1) {
	if !mobileBriefHasWrittenText(brief) || mobileBriefHeld(brief, mobileBriefClock()) {
		return
	}
	mobileBriefLatestShownMu.Lock()
	defer mobileBriefLatestShownMu.Unlock()
	var current MobileBriefV1
	found := false
	if record, ok := s.store.readLatestShown(); ok {
		current, found = s.store.load(record.BriefID)
		found = found && mobileBriefHasWrittenText(current) && !mobileBriefHeld(current, mobileBriefClock())
	}
	state := mobileBriefLatestMissing
	if found {
		state = mobileBriefLatestRecorded
	}
	if mobileBriefLatestTransition(state, current, brief) != mobileBriefLatestAdvance {
		return
	}
	if err := s.store.writeLatestShown(mobileBriefLatestShown{Version: 1, BriefID: brief.BriefID, Slot: brief.Slot, At: brief.AsOf}); err != nil {
		log.Printf("[mobile-brief] the latest brief could not be recorded")
	}
}

func (s *mobileBriefService) noteLatestShownIfNewer(brief MobileBriefV1) {
	s.noteLatestShown(brief)
}

// latestShown is the latest brief as every surface reads it. Stored briefs are
// checked newest first, including any newer than the record. The first whose
// sealed words may be shown is the latest. A brief whose words were withheld
// for a transient reason ends the search: it is hidden while the reason lasts,
// and older briefs are not shown in its place. With no brief that has written
// words, the newest readable brief stands in (a template).
func (s *mobileBriefService) latestShown() (MobileBriefV1, bool, error) {
	seen := map[string]bool{}
	var template MobileBriefV1
	haveTemplate := false
	// The newest stored brief with written words stands in when its words are
	// withheld now: a template never replaces a newer written brief (R61).
	var written MobileBriefV1
	haveWritten := false
	visit := func(source MobileBriefV1) (MobileBriefV1, bool, error) {
		if seen[source.BriefID] || source.NarrativeStatus == "quiet" || mobileBriefHeld(source, mobileBriefClock()) {
			return MobileBriefV1{}, false, nil
		}
		seen[source.BriefID] = true
		read, withheld, err := s.finishedReadWithholding(source)
		if err != nil {
			return MobileBriefV1{}, false, err
		}
		// A brief withheld for privacy never stands in as the written one.
		if !haveWritten && mobileBriefHasWrittenText(source) && withheld.Reason != mobileBriefWithheldPrivacyCheck {
			written, haveWritten = source, true
		}
		if withheld.Reason != "" {
			// The current brief's withholding is said once per brief and reason (R64).
			logCurrentBriefWithheld(source.BriefID, withheld)
		}
		if mobileBriefHasWrittenText(read) {
			return read, true, nil
		}
		// Storage is still opening: this brief's placeholder is today's brief, and
		// an older brief is never shown in its place.
		if withheld.Reason == mobileBriefWithheldNotReady && mobileBriefHasWrittenText(source) {
			return read, true, nil
		}
		// A brief whose words are withheld now is passed over: the design rules
		// (rule 4) show the last brief that can be shown, under its own time.
		if !haveTemplate {
			template, haveTemplate = read, true
		}
		return MobileBriefV1{}, false, nil
	}
	pick := func(brief MobileBriefV1) (MobileBriefV1, bool, error) {
		if mobileBriefHasWrittenText(brief) {
			s.noteLatestShown(brief)
		}
		return brief, true, nil
	}
	// A previous record can lag a newer completed seal, for example after a
	// restart between saving the copy and updating the pointer. Inspect the
	// stored briefs in time order before repairing the record in pick.
	for _, source := range s.store.briefs() {
		brief, done, err := visit(source)
		if err != nil {
			return MobileBriefV1{}, false, err
		}
		if done {
			return pick(brief)
		}
	}
	if haveWritten {
		read, _, err := s.finishedReadWithholding(written)
		if err != nil {
			return MobileBriefV1{}, false, err
		}
		return read, true, nil
	}
	if haveTemplate {
		return template, true, nil
	}
	return MobileBriefV1{}, false, nil
}

// currentBriefWithheldLogged is the last current-brief withholding logged, so a
// brief withheld for one reason is named once, not on every read.
var currentBriefWithheldLogged string

func logCurrentBriefWithheld(briefID string, withheld mobileBriefWithholding) {
	key := briefID + "|" + withheld.Reason + "|" + withheld.SourceKind
	if key == currentBriefWithheldLogged {
		return
	}
	currentBriefWithheldLogged = key
	log.Printf("[mobile-brief] the current brief %s is withheld on read: reason=%s kind=%s kept=%t", briefID, withheld.Reason, withheld.SourceKind, withheld.Transient())
}
