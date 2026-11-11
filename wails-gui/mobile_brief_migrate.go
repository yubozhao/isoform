package main

import (
	"context"
	"log"
	"time"
)

// The old edited-source rule hid a whole brief as source_withdrawn when a cited
// source was edited, regenerated or removed by retention. Seal v2 reads only the
// copies it wrote, so a copy the old rule hid would stay hidden. migrateOldSealedCopies
// seals each such copy once, from the built brief, through the current gates.
//
// It runs once per start, off the read path, and never on a read. A copy that
// was sealed under seal v2 has mobileBriefSealRule and is never selected again.
// The result is kept whatever it is: a withheld result is the answer for the
// sources as they are now. A transient withholding (the stores still opening)
// leaves the old copy, and the next start tries again.
//
// Recorded keyed withdrawals are honored after the gates: whatever cites a
// withdrawn key is dropped from the saved copy, as the keyed amend drops it.
func (s *mobileBriefService) migrateOldSealedCopies() int {
	if s == nil || s.app == nil {
		return 0
	}
	migrated := 0
	for _, source := range s.store.briefs() {
		if s.isStopped() {
			break
		}
		saved, found, err := s.store.loadFinishedRead(source.BriefID)
		if err != nil || !found || saved.Schema != mobileBriefSealedSchema || saved.Rule == mobileBriefSealRule || saved.WithheldReason != mobileBriefWithheldSource {
			continue
		}
		if !s.migrationDayIsRecent(source) {
			continue
		}
		if s.migrateOldCopy(source) {
			migrated++
		}
	}
	if migrated > 0 {
		log.Printf("[mobile-brief] migrated %d copies hidden by the old edited-source rule", migrated)
	}
	return migrated
}

// migrationMaxAgeDays is how old, in local days, a brief may be and still be
// migrated: today and the two days before it. The keyed withdrawal log keeps only
// its last changes, and the gates cannot tell whether a source cited by an older
// brief was deleted long ago. An older old-rule copy stays as it is.
const migrationMaxAgeDays = 2

// migrationDayIsRecent reports whether a brief's local day is within the last
// migrationMaxAgeDays+1 local days.
func (s *mobileBriefService) migrationDayIsRecent(source MobileBriefV1) bool {
	today, err := time.Parse("2006-01-02", s.readDay())
	if err != nil {
		return false
	}
	day, err := time.Parse("2006-01-02", source.LocalDate)
	if err != nil {
		return false
	}
	age := int(today.Sub(day).Hours() / 24)
	return age >= 0 && age <= migrationMaxAgeDays
}

// migrateOldCopy seals one brief again under seal v2, then drops what cites a
// recorded keyed withdrawal from the saved copy. It reports whether a copy was saved.
func (s *mobileBriefService) migrateOldCopy(source MobileBriefV1) bool {
	lock := briefSealLock(source.BriefID)
	lock.Lock()
	defer lock.Unlock()
	// The stored brief is sealed as it is: the seal checks the copy's source digest
	// against the stored brief, so a trimmed brief would be refused as an older one.
	if _, _, wrote, _ := s.sealResult(context.Background(), source); !wrote {
		return false
	}
	withdrawn := mobileBriefAllWithdrawnKeys()
	if len(withdrawn) == 0 {
		return true
	}
	saved, found, err := s.store.loadFinishedRead(source.BriefID)
	if err != nil || !found {
		return false
	}
	var brief MobileBriefV1
	if unmarshalStoredMobileBrief(saved.Brief, &brief) != nil || brief.BriefID != source.BriefID {
		return false
	}
	changed := map[string]bool{}
	for _, ref := range brief.Evidence {
		if briefRefWithdrawn(ref.SourceID, withdrawn) {
			changed[ref.Ref] = true
		}
	}
	for _, ref := range saved.SourceRefs {
		if briefRefWithdrawn(ref.ID, withdrawn) {
			changed[ref.Ref] = true
		}
	}
	if len(changed) == 0 {
		return true
	}
	raw, err := marshalStoredMobileBrief(mobileBriefDropCited(brief, changed))
	if err != nil {
		return false
	}
	saved.Brief = raw
	return s.store.saveFinishedReadIfCurrent(source.BriefID, saved) == nil
}

// mobileBriefAllWithdrawnKeys returns every keyed withdrawal in the log. The log
// keeps the last changes, so a withdrawal older than its floor is not listed.
func mobileBriefAllWithdrawnKeys() []briefWithdrawn {
	mobileBriefPrivacyEpochMu.Lock()
	defer mobileBriefPrivacyEpochMu.Unlock()
	var keys []briefWithdrawn
	for _, entry := range mobileBriefWithdrawals.Entries {
		keys = append(keys, entry.Keys...)
	}
	return keys
}
