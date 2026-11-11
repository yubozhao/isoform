package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wails-gui/internal/handoff"
)

func init() {
	// A handoff removal withdraws it from the briefs that cite it. A handoff save
	// or edit is not a withdrawal: sealed copies keep what they show.
	handoff.RemovalHook = func(id string) {
		briefPrivacyChanged(briefWithdrawn{ID: id})
		briefPrivacyAmendSoon()
	}
}

// A brief is sealed once: when it is made (or changed), the read every surface
// shows is worked out with the current privacy gates and saved beside the brief
// as a finished, display-ready copy (docs/brief-rebuild.md, PR 1). Reading it
// is a plain read of that copy. It calls no store, so a store that is still
// opening, or a read at launch, can never hide a brief that was checked when it
// was sealed.
//
// The copy stays valid while the privacy epoch it was sealed under is current.
// Privacy changes (a forgotten source, a private app or window, Closed Eyes,
// mail) advance the epoch; the next read of each affected brief seals it again
// (the amend). The epoch is persisted, so a restart does not seal everything
// again. A copy is also worked out again once its local day has changed, because
// its day words ("today", "yesterday") are relative to the day it was sealed on.
const (
	mobileBriefFinishedReadExt = ".read"
	// mobileBriefSealedSchema is the sealed copy's format. Schema 1 copies
	// carry a read key that is no longer used; they are sealed again on first read.
	mobileBriefSealedSchema = 2
	// mobileBriefSealRule is the seal rule of seal v2 (see mobileBriefFinishedRead.Rule).
	mobileBriefSealRule = 2
	// mobileBriefLatestShownName is the one latest-brief record (PR 1, item 3).
	mobileBriefLatestShownName  = "latest-shown.json"
	mobileBriefPrivacyEpochFile = "privacy-epoch"
)

var errMobileBriefFinishedReadUnreadable = errors.New("the saved brief could not be read")

var (
	// mobileBriefReadEpoch counts every change that a cached reader depends on
	// (privacy changes and freshness changes), for caches outside this file.
	mobileBriefReadEpoch atomic.Uint64
	// mobileBriefPrivacyEpoch counts privacy changes only; a sealed read is
	// valid while the epoch it was sealed under is still current.
	mobileBriefPrivacyEpoch atomic.Uint64
	// mobileBriefPrivacyEpochMu orders the in-memory epoch with its saved copy.
	mobileBriefPrivacyEpochMu       sync.Mutex
	mobileBriefPrivacyEpochSavedDir string
	// mobileBriefFinishedReadKeys counts sealings (the work of making a read),
	// for tests: a read that needs no change must not add to it.
	mobileBriefFinishedReadKeys atomic.Uint64
	// mobileBriefGateRuns counts gate passes over a brief, for tests: a plain
	// read of a sealed brief must add none.
	mobileBriefGateRuns atomic.Uint64
)

// briefWithdrawn names one source a privacy action withdrew, by the kind and id
// the brief cites it with. An empty kind matches any kind with that id.
type briefWithdrawn struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id"`
}

// briefWithdrawal is one privacy epoch change. Keys is empty when the change
// did not name its sources (an app, a pause, an account or a session): the amend
// then runs the gates again for each brief, because it cannot tell what to remove.
type briefWithdrawal struct {
	Epoch uint64           `json:"epoch"`
	Keys  []briefWithdrawn `json:"keys,omitempty"`
}

// briefWithdrawalLog holds the recent privacy changes. Floor is the epoch the log
// starts after: a copy sealed at or after Floor can be amended from the entries
// alone; an older copy cannot, and is amended with the gates.
type briefWithdrawalLog struct {
	Floor   uint64            `json:"floor"`
	Entries []briefWithdrawal `json:"entries"`
}

const (
	mobileBriefWithdrawalLogFile  = "withdrawals.json"
	mobileBriefWithdrawalLogLimit = 500
)

var mobileBriefWithdrawals briefWithdrawalLog

// briefPrivacyChanged records a privacy change and advances the epoch. Every
// sealed copy whose print no longer matches is amended before it is shown. The
// keys name the sources the change withdrew; no keys means the change is not
// keyed. The epoch and the log are saved first, so a change is not lost if the
// app stops before the copies are amended.
func briefPrivacyChanged(keys ...briefWithdrawn) {
	mobileBriefReadEpoch.Add(1)
	mobileBriefPrivacyEpochMu.Lock()
	defer mobileBriefPrivacyEpochMu.Unlock()
	value := mobileBriefPrivacyEpoch.Add(1)
	mobileBriefWithdrawals.Entries = append(mobileBriefWithdrawals.Entries, briefWithdrawal{Epoch: value, Keys: keys})
	if over := len(mobileBriefWithdrawals.Entries) - mobileBriefWithdrawalLogLimit; over > 0 {
		mobileBriefWithdrawals.Floor = mobileBriefWithdrawals.Entries[over-1].Epoch
		mobileBriefWithdrawals.Entries = append([]briefWithdrawal(nil), mobileBriefWithdrawals.Entries[over:]...)
	}
	if dir := mobileBriefPrivacyEpochSavedDir; dir != "" {
		// The log is written before the epoch: a saved epoch always has its entry.
		if data, err := json.Marshal(mobileBriefWithdrawals); err == nil {
			_ = writeMobileBriefFileAtomic(dir, mobileBriefWithdrawalLogFile, data)
		}
		_ = writeMobileBriefFileAtomic(dir, mobileBriefPrivacyEpochFile, []byte(strconv.FormatUint(value, 10)))
	}
}

// withdrawnSince returns the sources withdrawn after epoch. It reports false when
// the log cannot answer: the copy is older than the log, or a change in between
// named no sources.
func withdrawnSince(epoch uint64) ([]briefWithdrawn, bool) {
	mobileBriefPrivacyEpochMu.Lock()
	defer mobileBriefPrivacyEpochMu.Unlock()
	if epoch < mobileBriefWithdrawals.Floor {
		return nil, false
	}
	var keys []briefWithdrawn
	for _, entry := range mobileBriefWithdrawals.Entries {
		if entry.Epoch <= epoch {
			continue
		}
		if len(entry.Keys) == 0 {
			return nil, false
		}
		keys = append(keys, entry.Keys...)
	}
	return keys, true
}

// briefPrivacyAmendSoon starts the amendment of the sealed briefs after a
// privacy change. It runs in its own goroutine, so the caller holds no lock
// the amendment needs, and an idle surface does not wait for a read. A read
// that sees the newer epoch amends its brief before it shows it, so the
// amendment never has to finish first.
func briefPrivacyAmendSoon() {
	if s := mobileBriefLive.Load(); s != nil {
		s.requestAmendment()
	}
}

// briefAmendState coalesces amendments: one pass at a time per service.
type briefAmendState struct {
	mu      sync.Mutex
	running bool
	dirty   bool
}

// requestAmendment starts the amendment pass in its own goroutine, or marks the
// running pass dirty so it goes again. The goroutine is tracked by the service's
// work group, so Stop waits for it and a test cleanup drains it.
func (s *mobileBriefService) requestAmendment() {
	s.amend.mu.Lock()
	if s.amend.running {
		s.amend.dirty = true
		s.amend.mu.Unlock()
		return
	}
	s.amend.mu.Unlock()
	if !s.beginWork() {
		return
	}
	s.amend.mu.Lock()
	s.amend.running = true
	s.amend.mu.Unlock()
	go func() {
		defer s.wg.Done()
		for {
			s.amendSources()
			s.amend.mu.Lock()
			if !s.amend.dirty {
				s.amend.running = false
				s.amend.mu.Unlock()
				return
			}
			s.amend.dirty = false
			s.amend.mu.Unlock()
		}
	}()
}

// memorySourcesRemovedOnDisk is a privacy change: memory or knowledge files were
// deleted outside the app (the memory watcher found them gone). Only the removed
// sources are withdrawn. An edited file is not a withdrawal and is not reported here.
func memorySourcesRemovedOnDisk(removed []briefWithdrawn) {
	briefPrivacyChanged(removed...)
	briefPrivacyAmendSoon()
}

// noteMobileBriefReadCachesChanged records a freshness change (a new
// calendar, mail or memory read, a finished run): caches outside the sealed
// briefs are read again, but no sealed brief is sealed again for it. The
// next brief made or refreshed carries the change.
func noteMobileBriefReadCachesChanged() { mobileBriefReadEpoch.Add(1) }

// mobileBriefPrivacyInputEvents are the app events that are privacy actions:
// they change what a sealed brief may show, and each one advances the epoch
// without naming its sources (the gates decide). A mail sync is not one of them:
// it happens many times a day and withdraws nothing. It still refreshes Today
// (a freshness change): the refresh builds new briefs and never re-seals a
// sealed brief that already exists.
var mobileBriefPrivacyInputEvents = map[string]bool{
	"sensitive-apps:updated": true, closedEyesModeChangedEvent: true,
	"background-listening:changed": true, "topics:withdrawn": true,
}

// mobileBriefReadInputEvents are the app events after which caches outside the
// brief are read again. brief:stored is not one: the stored brief is in its seal.
var mobileBriefReadInputEvents = map[string]bool{
	"cron:state": true, "handoff:created": true, "handoff:updated": true, "insight:saved": true,
	"memory:updated": true, "activity:summary-ready": true, "deliverables:updated": true, "agency:updated": true,
	"sensitive-apps:updated": true, closedEyesModeChangedEvent: true, "background-listening:changed": true,
	"topics:withdrawn": true, "topics:updated": true, "calendar:synced": true, "mail:synced": true, "people:updated": true,
}

// adoptMobileBriefPrivacyEpoch loads the saved epoch from a briefs directory
// the first time it is opened. The in-memory epoch never goes backwards.
func adoptMobileBriefPrivacyEpoch(dir string) {
	if dir == "" {
		return
	}
	mobileBriefPrivacyEpochMu.Lock()
	defer mobileBriefPrivacyEpochMu.Unlock()
	if mobileBriefPrivacyEpochSavedDir == dir {
		return
	}
	mobileBriefPrivacyEpochSavedDir = dir
	if data, err := os.ReadFile(filepath.Join(dir, mobileBriefPrivacyEpochFile)); err == nil {
		if saved, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
			for {
				current := mobileBriefPrivacyEpoch.Load()
				if saved <= current || mobileBriefPrivacyEpoch.CompareAndSwap(current, saved) {
					break
				}
			}
		}
	}
	// The log is loaded with the epoch. Without a log (an older build), it starts
	// at the current epoch, so every older copy is amended with the gates.
	mobileBriefWithdrawals = briefWithdrawalLog{Floor: mobileBriefPrivacyEpoch.Load()}
	if data, err := os.ReadFile(filepath.Join(dir, mobileBriefWithdrawalLogFile)); err == nil {
		var log briefWithdrawalLog
		if json.Unmarshal(data, &log) == nil {
			mobileBriefWithdrawals = log
		}
	}
	// An epoch newer than the log's last entry means a change was saved without
	// its entry; the log cannot answer for it, so only the gates can.
	last := mobileBriefWithdrawals.Floor
	if n := len(mobileBriefWithdrawals.Entries); n > 0 {
		last = mobileBriefWithdrawals.Entries[n-1].Epoch
	}
	if mobileBriefPrivacyEpoch.Load() > last {
		mobileBriefWithdrawals = briefWithdrawalLog{Floor: mobileBriefPrivacyEpoch.Load()}
	}
}

// mobileBriefFinishedRead is the sealed copy of a brief as stored beside it.
type mobileBriefFinishedRead struct {
	Schema int    `json:"schema"`
	Key    string `json:"key,omitempty"`
	// Rule is the seal rule that made the copy. Seal v2 writes mobileBriefSealRule;
	// a copy without it (0) was sealed under the old edited-source rule, and the
	// one-time migration (migrateOldSealedCopies) seals it again.
	Rule int `json:"rule,omitempty"`
	// Epoch is the privacy epoch the copy was read under. A write never replaces
	// a copy sealed under a newer epoch (saveFinishedReadIfCurrent).
	Epoch uint64 `json:"epoch,omitempty"`
	// Print is the privacy state the copy was sealed under (briefPrivacyPrint).
	Print string `json:"print"`
	// Source is the digest of the stored brief the copy was made from.
	Source string `json:"source"`
	// Day is the local date the copy was sealed on.
	Day string `json:"day,omitempty"`
	// Cites are the source keys the brief's words were checked against, sorted;
	// nil when the brief has no binding record (every source then counts).
	Cites []string `json:"cites,omitempty"`
	// SealedAt remains the original boundary when a copy is amended. Source
	// fingerprints are local provenance for a later per-part amendment.
	SealedAt     time.Time               `json:"sealed_at,omitempty"`
	Fingerprints map[string]string       `json:"fingerprints,omitempty"`
	SourceRefs   []mobileBriefSealSource `json:"source_refs"`
	// Withheld is the non-transient withholding the copy was sealed with (a
	// source the person protected, for example). Reads report it, so the reason
	// is logged on read as it was before sealing. Older copies have none.
	WithheldReason string          `json:"withheld_reason,omitempty"`
	WithheldKind   string          `json:"withheld_kind,omitempty"`
	Brief          json.RawMessage `json:"brief"`
}

// mobileBriefLatestShown is the one record of the brief every surface shows.
type mobileBriefLatestShown struct {
	Version int    `json:"version"`
	BriefID string `json:"briefId"`
	Slot    string `json:"slot,omitempty"`
	At      string `json:"at,omitempty"`
}

func (s *mobileBriefStore) finishedReadPath(briefID string) (string, bool) {
	if s.dir == "" || !mobileBriefIDPattern.MatchString(briefID) {
		return "", false
	}
	return filepath.Join(s.dir, briefID+mobileBriefFinishedReadExt), true
}

func (s *mobileBriefStore) saveFinishedRead(briefID string, read mobileBriefFinishedRead) error {
	if _, ok := s.finishedReadPath(briefID); !ok {
		return errMobileBriefStateUnavailable
	}
	data, err := json.Marshal(read)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return writeMobileBriefFileAtomic(s.dir, briefID+mobileBriefFinishedReadExt, data)
}

// errMobileBriefStaleSeal is a seal made under an older privacy epoch than the
// copy already saved: the newer seal stands, and this one is dropped.
var errMobileBriefStaleSeal = errors.New("a newer privacy epoch already sealed this brief")

// saveFinishedReadIfCurrent is the compare-and-swap of a seal: the write happens
// only if no copy sealed under a newer epoch is saved. The check and the write
// hold the store lock, so two seals of one brief cannot interleave.
func (s *mobileBriefStore) saveFinishedReadIfCurrent(briefID string, read mobileBriefFinishedRead) error {
	if _, ok := s.finishedReadPath(briefID); !ok {
		return errMobileBriefStateUnavailable
	}
	data, err := json.Marshal(read)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, err := os.ReadFile(filepath.Join(s.dir, briefID+mobileBriefFinishedReadExt)); err == nil {
		var saved struct {
			Schema int    `json:"schema"`
			Epoch  uint64 `json:"epoch"`
		}
		if json.Unmarshal(current, &saved) == nil && saved.Schema == mobileBriefSealedSchema && saved.Epoch > read.Epoch {
			log.Printf("[mobile-brief] dropped a seal from privacy epoch %d: the saved copy is from epoch %d", read.Epoch, saved.Epoch)
			return errMobileBriefStaleSeal
		}
	}
	// The seal was computed from one stored version of the brief. A seal from an
	// older version (a copy made before a save, such as the cards a save added)
	// must not replace the copy of the newer one, even in the same epoch.
	if stored, err := os.ReadFile(filepath.Join(s.dir, briefID+".json")); err == nil {
		if brief, ok := s.storedBriefFromData(stored); ok && mobileBriefSourceDigest(brief) != read.Source {
			log.Printf("[mobile-brief] dropped a seal of an older version of brief %s", briefID)
			return errMobileBriefStaleSeal
		}
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return writeMobileBriefFileAtomic(s.dir, briefID+mobileBriefFinishedReadExt, data)
}

// loadFinishedRead returns the saved copy, found=false when there is none,
// and an error when one is there but cannot be read; that one is removed so
// the next read seals the brief again.
func (s *mobileBriefStore) loadFinishedRead(briefID string) (mobileBriefFinishedRead, bool, error) {
	path, ok := s.finishedReadPath(briefID)
	if !ok {
		return mobileBriefFinishedRead{}, false, nil
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return mobileBriefFinishedRead{}, false, nil
	}
	var data []byte
	var info os.FileInfo
	if err == nil {
		info, err = file.Stat()
		if err == nil {
			data, err = io.ReadAll(file)
		}
		_ = file.Close()
	}
	var read mobileBriefFinishedRead
	if err == nil && json.Unmarshal(data, &read) == nil && (read.Schema == 1 || read.Schema == mobileBriefSealedSchema) {
		// Read the old seal and its mtime from the same open file. A concurrent
		// atomic replacement must not supply a newer timestamp for older text.
		if read.SealedAt.IsZero() {
			read.SealedAt = info.ModTime()
		}
		return read, true, nil
	}
	_ = os.Remove(path)
	return mobileBriefFinishedRead{}, false, errMobileBriefFinishedReadUnreadable
}

// readSealedBrief returns the sealed copy of a stored brief when it is valid
// now: the same privacy print and local day. It calls no store and
// runs no gate, so any reader may use it.
func readSealedBrief(store *mobileBriefStore, briefID, print, day string) (MobileBriefV1, bool) {
	if store == nil {
		return MobileBriefV1{}, false
	}
	saved, found, err := store.loadFinishedRead(briefID)
	if err != nil || !found || saved.Schema != mobileBriefSealedSchema {
		return MobileBriefV1{}, false
	}
	if saved.Print != print || saved.Day != day {
		return MobileBriefV1{}, false
	}
	var brief MobileBriefV1
	if unmarshalStoredMobileBrief(saved.Brief, &brief) != nil || brief.BriefID != briefID {
		return MobileBriefV1{}, false
	}
	return brief, true
}

// readDay is the local date a seal and its day words are kept to.
func (s *mobileBriefService) readDay() string {
	loc := s.location
	if loc == nil {
		loc = time.Local
	}
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	return now.In(loc).Format("2006-01-02")
}

// mobileBriefSourceDigest names the stored brief a sealed copy is made from. A
// stored brief rewritten by any writer then no longer matches its copy.
func mobileBriefSourceDigest(source MobileBriefV1) string {
	raw, err := marshalStoredMobileBrief(source)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// sealCites are the source keys a brief's words were checked against.
func sealCites(source MobileBriefV1) []string {
	inputs := mobileBriefModelInputs(source)
	if inputs == nil {
		return nil
	}
	cites := make([]string, 0, len(inputs))
	for key := range inputs {
		cites = append(cites, key)
	}
	sort.Strings(cites)
	return cites
}

// seal works the brief out with the current gates and saves it as the sealed
// copy. It returns the brief as readers show it and whether the copy was saved.
// A transient withholding (a store still opening, Closed Eyes, mail that
// cannot be read) is shown but not saved: the copy on disk is kept, so the
// words come back once the gates can say. Any other outcome is saved, with its
// withholding, because it is the answer for the sources as they are now.
func (s *mobileBriefService) seal(ctx context.Context, source MobileBriefV1) (MobileBriefV1, mobileBriefWithholding, bool) {
	brief, withheld, wrote, _ := s.sealResult(ctx, source)
	return brief, withheld, wrote
}

// sealResult works the brief out with the current gates and saves the copy. A
// brief is sealed from the stored brief, not from its live sources: a source
// that was edited, regenerated or removed by retention is not a reason to
// withhold it. Only a privacy gate that now refuses an item (a withdrawal, a
// pause, a sensitive app) removes it from the copy. It reports whether the copy
// was saved, and whether the brief was malformed (a failure that cannot recover).
func (s *mobileBriefService) sealResult(ctx context.Context, source MobileBriefV1) (MobileBriefV1, mobileBriefWithholding, bool, bool) {
	if briefPolicyPending.Load() {
		return MobileBriefV1{}, mobileBriefWithholding{Reason: mobileBriefWithheldNotReady}, false, false
	}
	mobileBriefFinishedReadKeys.Add(1)
	previous, found, err := s.store.loadFinishedRead(source.BriefID)
	if err != nil {
		return MobileBriefV1{}, mobileBriefWithholding{Reason: mobileBriefWithheldNotReady}, false, false
	}
	sealedAt := time.Now()
	cited := source
	if found {
		sealedAt = s.store.finishedReadTime(source.BriefID, previous)
		cited = mobileBriefSealedSources(previous, source)
	}
	epoch := mobileBriefPrivacyEpoch.Load()
	print := s.app.briefPrivacyPrint()
	day := s.readDay()
	projectionContext := s.app.mobileBriefSummaryProjection(ctx, source, previous.Fingerprints)
	projected, withheld := s.app.mobileBriefForProjectionWithholding(projectionContext, source, true)
	brief := s.app.mobileBriefWithCards(projected)
	brief.Audio = s.finishedReadAudio(ctx, source, brief)
	if withheld.Transient() || print != s.app.briefPrivacyPrint() || day != s.readDay() {
		return brief, withheld, false, false
	}
	raw, err := marshalStoredMobileBrief(brief)
	if err != nil {
		return brief, withheld, false, true
	}
	fingerprints := previous.Fingerprints
	if !found {
		fingerprints = s.app.mobileBriefSourceFingerprints(ctx, cited)
	}
	read := mobileBriefFinishedRead{Schema: mobileBriefSealedSchema, Rule: mobileBriefSealRule, Epoch: epoch, Print: print, Source: mobileBriefSourceDigest(source), Day: day, Cites: sealCites(source), SealedAt: sealedAt, Fingerprints: fingerprints, SourceRefs: mobileBriefSealSources(cited), WithheldReason: withheld.Reason, WithheldKind: withheld.SourceKind, Brief: raw}
	if mobileBriefBeforeSealSave != nil {
		mobileBriefBeforeSealSave(source.BriefID)
	}
	if err := s.store.saveFinishedReadIfCurrent(source.BriefID, read); err != nil {
		return brief, withheld, false, false
	}
	return brief, withheld, true, false
}

// finishedReadWithholding is source as the readers show it. A read compares the
// privacy print the copy was sealed under with the current one (in memory) and
// returns the copy. It reads no source and runs no gate, and it never seals a
// brief that has a copy. When the privacy print has changed (an explicit
// withdrawal, a pause or a sensitive app), the copy is amended before it is
// shown, so withdrawn content is not shown after the action returns. A brief
// with no copy yet is sealed once, as before.
func (s *mobileBriefService) finishedReadWithholding(source MobileBriefV1) (result MobileBriefV1, resultWithheld mobileBriefWithholding, resultErr error) {
	defer func() {
		status, reason := "shown", "none"
		switch true {
		case resultErr != nil:
			status, reason = "withheld", "read_error"
		case len(result.Narrative) == 0:
			status, reason = "withheld", resultWithheld.Reason
			if reason == "" {
				reason = "no_written_text"
			}
		}
		log.Printf("[mobile-brief] read brief=%s written=%s reason=%s kind=%s", source.BriefID, status, reason, resultWithheld.SourceKind)
	}()
	mobileBriefLive.Store(s)
	brief, withheld, pending, err := s.sealedReadOrPending(source)
	if err != nil {
		return MobileBriefV1{}, mobileBriefWithholding{}, err
	}
	if mobileBriefAfterSealedRead != nil {
		mobileBriefAfterSealedRead(source.BriefID)
	}
	if !pending && s.app != nil {
		saved, found, err := s.store.loadFinishedRead(source.BriefID)
		if err != nil {
			return MobileBriefV1{}, mobileBriefWithholding{}, err
		}
		if found {
			// Use the same saved copy for the words and their privacy metadata.
			brief = s.sealedReadSnapshot(source, saved)
			withheld = mobileBriefWithholding{Reason: saved.WithheldReason, SourceKind: saved.WithheldKind}
			if saved.Print != s.app.briefPrivacyPrint() {
				// The amend: the privacy state changed since the copy was sealed, so
				// the brief is sealed again with the current gates and that is shown.
				// Only when the gates cannot decide yet are the words withheld, and the
				// brief is sealed again in the background.
				kind := briefPrivacyChangeKind(saved.Print, s.app.briefPrivacyPrint())
				if sealed, amended, wrote := s.amendSealedCopy(source); wrote {
					return sealed, amended, nil
				}
				s.scheduleSealRetry(source)
				return mobileBriefPaused(brief), mobileBriefWithholding{Reason: mobileBriefWithheldPrivacyCheck, SourceKind: kind}, nil
			}
			if saved.Day != s.readDay() {
				// Day words ("today") are relative to the day the copy was sealed on.
				// The copy is shown meanwhile; the job re-seals it off the read path.
				s.scheduleSealRetry(source)
			}
		}
	}
	if pending {
		// Storage is still opening: the brief's identity, time and slot show with
		// the opening line, and no content (brief-rules rule 4).
		return mobileBriefPaused(source), mobileBriefWithholding{Reason: mobileBriefWithheldNotReady}, nil
	}
	return brief, withheld, nil
}

// sealedReadOrPending is the sealed copy, or pending when there is none yet and
// the copy cannot be made now. A brief with no copy while the observation policy
// is loaded is sealed here once (legacy migration; the only read that seals).
func (s *mobileBriefService) sealedReadOrPending(source MobileBriefV1) (MobileBriefV1, mobileBriefWithholding, bool, error) {
	saved, found, err := s.store.loadFinishedRead(source.BriefID)
	if err != nil {
		s.scheduleSealRetry(source)
		return MobileBriefV1{}, mobileBriefWithholding{}, false, err
	}
	if !found || saved.Schema != mobileBriefSealedSchema {
		if briefPolicyPending.Load() {
			s.scheduleSealRetry(source)
			return MobileBriefV1{}, mobileBriefWithholding{}, true, nil
		}
		if s.isStopped() {
			return MobileBriefV1{}, mobileBriefWithholding{}, false, errMobileBriefUnavailable
		}
		// A brief that could not be sealed recently is not gated again on every
		// read; it stays withheld until its backoff ends or its content changes.
		if s.sealRetryWaiting(source) {
			return MobileBriefV1{}, mobileBriefWithholding{}, true, nil
		}
		sealed, withheld, wrote := s.seal(context.Background(), source)
		if !wrote {
			s.scheduleSealRetry(source)
			return MobileBriefV1{}, mobileBriefWithholding{}, true, nil
		}
		s.noteLatestShownIfNewer(sealed)
		return sealed, withheld, false, nil
	}
	brief := s.sealedReadSnapshot(source, saved)
	return brief, mobileBriefWithholding{Reason: saved.WithheldReason, SourceKind: saved.WithheldKind}, false, nil
}

// mobileBriefWithheldPrivacyCheck is the withholding of a copy whose privacy
// state no longer matches the state it was sealed under (see finishedReadWithholding).
const mobileBriefWithheldPrivacyCheck = "privacy_check"

// mobileBriefLive is the service whose sealed briefs a source change re-seals.
var mobileBriefLive atomic.Pointer[mobileBriefService]

func (s *mobileBriefService) sealedReadSnapshot(source MobileBriefV1, saved mobileBriefFinishedRead) MobileBriefV1 {
	var brief MobileBriefV1
	if saved.Schema != mobileBriefSealedSchema || unmarshalStoredMobileBrief(saved.Brief, &brief) != nil || brief.BriefID != source.BriefID {
		s.scheduleSealRetry(source)
		return MobileBriefV1{}
	}
	if s.app != nil && s.app.IsClosedEyesModeEnabled() {
		// Closed Eyes is known in memory: nothing the brief says stays readable.
		return mobileBriefPaused(brief)
	}
	if s.app != nil && (s.app.store == nil || s.app.store.Closing()) {
		// The policy store is closed, so a web source's hold cannot be confirmed:
		// every web-source item is left out and the rest of the brief shows.
		brief = mobileBriefWithoutWebSources(brief)
	}
	return brief
}

// mobileBriefWithoutWebSources leaves out every item that comes from a web source,
// with the paragraphs, cards and recording that speak of it, and counts them in the
// brief's excluded-items limit.
func mobileBriefWithoutWebSources(brief MobileBriefV1) MobileBriefV1 {
	web := map[string]bool{}
	for _, ref := range brief.Evidence {
		if ref.WebSource != nil {
			web[ref.Ref] = true
		}
	}
	touches := func(refs []string) bool {
		for _, id := range refs {
			if web[id] {
				return true
			}
		}
		return false
	}
	removed := len(brief.Sections.Watching)
	brief.Sections.Watching = nil
	var narrative []MobileBriefPara
	for _, para := range brief.Narrative {
		if touches(para.Refs) {
			removed++
			continue
		}
		narrative = append(narrative, para)
	}
	var cards []MobileBriefCard
	for _, card := range brief.Cards {
		if touches(card.Refs) {
			removed++
			continue
		}
		cards = append(cards, card)
	}
	var evidence []MobileBriefRef
	for _, ref := range brief.Evidence {
		if ref.WebSource != nil {
			continue
		}
		evidence = append(evidence, ref)
	}
	if removed > 0 {
		brief.Narrative, brief.Cards, brief.Evidence = narrative, cards, evidence
		brief.Audio = nil
		brief.Limits.ExcludedSensitiveItems += removed
	}
	return brief
}

// copyPrivacyChange reports a stored copy sealed under other privacy state, and
// which privacy input moved (see briefPrivacyChangeKind).
func (s *mobileBriefService) copyPrivacyChange(source MobileBriefV1) (string, bool) {
	saved, found, err := s.store.loadFinishedRead(source.BriefID)
	if err != nil || !found || saved.Schema != mobileBriefSealedSchema {
		return "", false
	}
	current := s.app.briefPrivacyPrint()
	if saved.Print == current {
		return "", false
	}
	return briefPrivacyChangeKind(saved.Print, current), true
}

// mobileBriefAfterSealedRead lets a test replace the copy between read steps.
var mobileBriefAfterSealedRead func(briefID string)

// mobileBriefBeforeSealSave, when set by a test, runs after a seal is worked out
// and before it is saved, where a newer stored version can arrive. Nil in the app.
var mobileBriefBeforeSealSave func(briefID string)

// briefAmendAttempts bounds how often one read seals a brief whose copy has a
// stale privacy print. A seal can lose a race (a newer stored version or a newer
// privacy epoch arrived while it worked); the next attempt works from the newest
// stored version under the current print, instead of withholding the words.
const briefAmendAttempts = 3

// briefSealLocks serializes the amend of one brief, so concurrent reads share
// one seal: the reads that wait find the copy the first one saved.
var briefSealLocks sync.Map

func briefSealLock(briefID string) *sync.Mutex {
	lock, _ := briefSealLocks.LoadOrStore(briefID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// amendSealedCopy seals source again under the current privacy print. It reports
// whether a copy was saved; when none was, the caller withholds the words.
func (s *mobileBriefService) amendSealedCopy(source MobileBriefV1) (MobileBriefV1, mobileBriefWithholding, bool) {
	lock := briefSealLock(source.BriefID)
	lock.Lock()
	defer lock.Unlock()
	for attempt := 0; attempt < briefAmendAttempts && !s.isStopped(); attempt++ {
		if _, changed := s.copyPrivacyChange(source); !changed {
			// Another read saved the copy while this one waited.
			brief, withheld, pending, err := s.sealedReadOrPending(source)
			return brief, withheld, err == nil && !pending
		}
		if saved, found, err := s.store.loadFinishedRead(source.BriefID); err == nil && found && saved.Schema == mobileBriefSealedSchema {
			if brief, ok := s.amendKeyed(source, saved); ok {
				return brief, mobileBriefWithholding{Reason: saved.WithheldReason, SourceKind: saved.WithheldKind}, true
			}
		}
		// The log cannot say what changed: the gates decide again for this brief.
		sealed, withheld, wrote := s.seal(context.Background(), source)
		if wrote {
			s.noteLatestShownIfNewer(sealed)
			return sealed, withheld, true
		}
		newer, ok := s.storedSource(source.BriefID)
		if !ok {
			break
		}
		source = newer
	}
	return MobileBriefV1{}, mobileBriefWithholding{}, false
}

// amendKeyed amends a sealed copy from the sources its privacy changes named. It
// reads no live source and runs no gate: the paragraphs, cards, items and evidence
// that cite a withdrawn source are dropped, the rest stays, and the copy is saved
// under the current print. It reports false when the log cannot say what changed
// (an unnamed change, or a copy older than the log), or when the change is not
// only a change of the epoch (a pause or a sensitive app); the caller then runs the gates.
func (s *mobileBriefService) amendKeyed(source MobileBriefV1, saved mobileBriefFinishedRead) (MobileBriefV1, bool) {
	print := s.app.briefPrivacyPrint()
	if !briefPrintMovedOnlyInEpoch(saved.Print, print) {
		return MobileBriefV1{}, false
	}
	withdrawn, ok := withdrawnSince(saved.Epoch)
	if !ok {
		return MobileBriefV1{}, false
	}
	var brief MobileBriefV1
	if unmarshalStoredMobileBrief(saved.Brief, &brief) != nil || brief.BriefID != source.BriefID {
		return MobileBriefV1{}, false
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
	if len(changed) > 0 {
		brief = mobileBriefDropCited(brief, changed)
	}
	epoch := mobileBriefPrivacyEpoch.Load()
	raw, err := marshalStoredMobileBrief(brief)
	if err != nil {
		return MobileBriefV1{}, false
	}
	read := saved
	read.Epoch, read.Print, read.Brief = epoch, print, raw
	if epoch != mobileBriefPrivacyEpoch.Load() || print != s.app.briefPrivacyPrint() {
		return MobileBriefV1{}, false
	}
	if err := s.store.saveFinishedReadIfCurrent(source.BriefID, read); err != nil {
		return MobileBriefV1{}, false
	}
	return s.sealedReadSnapshot(source, read), true
}

// amendSources brings every stored brief in line with the privacy state. A copy
// whose print is current is left alone. A stale copy is amended, and an amended
// copy is announced. A brief with no usable copy is sealed, as sealSources does.
func (s *mobileBriefService) amendSources() {
	sources := s.store.briefs()
	if today, ok := s.store.currentToday(); ok {
		sources = append(sources, today)
	}
	for _, source := range sources {
		if source.NarrativeStatus == "quiet" {
			continue
		}
		saved, found, err := s.store.loadFinishedRead(source.BriefID)
		if err == nil && found && saved.Schema == mobileBriefSealedSchema {
			if saved.Print == s.app.briefPrivacyPrint() {
				continue
			}
			if _, _, wrote := s.amendSealedCopy(source); wrote && s.app != nil {
				s.app.emitBrowserEvent("brief:stored", source.BriefID)
			}
			continue
		}
		s.sealAndAnnounce(source)
	}
}

// briefRefWithdrawn reports whether a cited source is one a privacy change
// withdrew. Matching is by id, whole or after a prefix ("file:x"), and ignores
// the kind: a kind named by one action can differ from the kind a brief cites
// (activity rows, sessions). A match can only widen what is dropped, never narrow it.
func briefRefWithdrawn(id string, withdrawn []briefWithdrawn) bool {
	if id == "" {
		return false
	}
	for _, w := range withdrawn {
		if w.ID == "" {
			continue
		}
		if w.ID == id || strings.HasSuffix(w.ID, ":"+id) || strings.HasSuffix(id, ":"+w.ID) {
			return true
		}
	}
	return false
}

// briefPrintMovedOnlyInEpoch reports whether the only part of the print that
// changed is the epoch. Pause and sensitive-app changes are not keyed.
func briefPrintMovedOnlyInEpoch(saved, current string) bool {
	was, now := strings.Split(saved, "|"), strings.Split(current, "|")
	if len(was) != 4 || len(now) != 4 {
		return false
	}
	return was[1] == now[1] && was[2] == now[2] && was[3] == now[3]
}

// briefPrivacyChangeKind names the privacy input that moved between the print a
// copy was sealed under and the current one. The print is epoch|enabled|paused|apps
// (briefPrivacyPrint): the epoch moves on every privacy event and on memory or
// handoff file changes, the pause on Closed Eyes or a timed pause, and the apps on
// the sensitive-app list.
func briefPrivacyChangeKind(saved, current string) string {
	was, now := strings.Split(saved, "|"), strings.Split(current, "|")
	if len(was) != 4 || len(now) != 4 {
		return "print"
	}
	var kinds []string
	if was[0] != now[0] {
		kinds = append(kinds, "epoch")
	}
	if was[1] != now[1] || was[2] != now[2] {
		kinds = append(kinds, "pause")
	}
	if was[3] != now[3] {
		kinds = append(kinds, "sensitive_apps")
	}
	if len(kinds) == 0 {
		return "print"
	}
	return strings.Join(kinds, ",")
}

// mobileBriefPaused is a brief with everything it says removed, while its
// identity, time and slot stay (the pause is shown, not the content).
func mobileBriefPaused(brief MobileBriefV1) MobileBriefV1 {
	brief.Narrative, brief.WeeklyRecap, brief.Audio, brief.Cards = nil, nil, nil, nil
	brief.Sections = MobileBriefV1{}.Sections
	brief.Offers, brief.Evidence, brief.Upcoming = nil, nil, nil
	brief.YourWeek, brief.WeekQuestion, brief.DayShape = nil, nil, nil
	brief.WeekLead, brief.WeekLeadFacts = "", ""
	brief.NarrativeStatus = "unavailable"
	return brief
}

// briefPolicyPending is true from the start of launch until the observation
// policy is restored. Nothing is sealed before then: a brief stored meanwhile
// stays unsealed and shows nothing private (brief-rules rule 4, "storage still
// opening") until briefObservationPolicyLoaded seals it.
var briefPolicyPending atomic.Bool

// briefObservationPolicyLoaded ends the pending state once per launch: it seals
// every stored brief that waited for the policy. Each launch starts pending again.
func briefObservationPolicyLoaded() {
	briefPolicyPending.Store(false)
	briefSourceChanged()
}

// briefSaved seals a stored brief at the moment it is stored, so its copy
// appears together with it (a store is a write path, not a display read).
func briefSaved(briefID string) {
	if s := mobileBriefLive.Load(); s != nil {
		s.sealStored(briefID)
	}
}

// briefSourceChanged says Isoform itself added to what a brief shows (its audio,
// a week answer). Every stored brief is sealed again so the addition appears.
// Edits and regenerated sources do not call it: a sealed copy keeps what it shows.
// Hooks run after their locks are released (see briefSaved), so a re-seal may read the stores.
func briefSourceChanged() {
	if s := mobileBriefLive.Load(); s != nil {
		s.resealAll()
	}
}

// sealStored seals one stored brief now.
func (s *mobileBriefService) sealStored(briefID string) {
	if !s.beginWork() {
		return
	}
	defer s.wg.Done()
	source, ok := s.store.load(briefID)
	if !ok || source.NarrativeStatus == "quiet" {
		return
	}
	s.resetSealFailure(source.BriefID)
	// The saver announces the brief (brief:stored) itself; sealing it here
	// only saves the copy, so one stored brief is not announced twice.
	sealed, _, wrote := s.seal(context.Background(), source)
	if !wrote {
		s.scheduleSealRetry(source)
		return
	}
	s.noteLatestShownIfNewer(sealed)
}

// resealAll seals every stored brief now.
func (s *mobileBriefService) resealAll() {
	if !s.beginWork() {
		return
	}
	defer s.wg.Done()
	s.sealSources()
}

// sealSources seals each stored brief (and the Today-only document) again. The
// caller holds a work slot.
func (s *mobileBriefService) sealSources() {
	seen := map[string]bool{}
	sources := s.store.briefs()
	for _, source := range sources {
		seen[source.BriefID] = true
	}
	// The Today-only document is stored apart from the dated briefs, but a
	// privacy change must seal it again like any other brief it may show.
	if today, ok := s.store.currentToday(); ok && !seen[today.BriefID] {
		sources = append(sources, today)
	}
	for _, source := range sources {
		if source.NarrativeStatus == "quiet" {
			continue
		}
		s.sealAndAnnounce(source)
	}
}

// sealAndAnnounce seals a brief, makes it the latest when it has words, and
// announces a saved copy (brief:stored). A brief whose gates cannot decide yet
// is sealed again in the background.
func (s *mobileBriefService) sealAndAnnounce(source MobileBriefV1) {
	s.resetSealFailure(source.BriefID)
	sealed, _, wrote := s.seal(context.Background(), source)
	if !wrote {
		s.scheduleSealRetry(source)
		return
	}
	s.noteLatestShownIfNewer(sealed)
	if s.app != nil {
		s.app.emitBrowserEvent("brief:stored", source.BriefID)
	}
}

// A retry job owns the lifecycle of a seal that could not finish immediately.
// The state file contains no brief text and lets launch resume a scheduled retry.
const mobileBriefSealStateExt = ".seal-state"

type briefSealState string

const (
	briefSealPending   briefSealState = "pending"
	briefSealSealing   briefSealState = "sealing"
	briefSealRetryWait briefSealState = "retry_wait"
	briefSealSealed    briefSealState = "sealed"
	briefSealWithheld  briefSealState = "withheld"
	briefSealFailed    briefSealState = "failed"
)

// transitionBriefSeal is the only writer of a job's state. A terminal outcome
// can be reopened when its source changes or an explicit source event occurs.
func transitionBriefSeal(from, to briefSealState) (briefSealState, bool) {
	allowed := false
	switch from {
	case "":
		allowed = to == briefSealPending
	case briefSealPending:
		allowed = to == briefSealSealing || to == briefSealFailed
	case briefSealSealing:
		allowed = to == briefSealRetryWait || to == briefSealSealed || to == briefSealWithheld || to == briefSealFailed
	case briefSealRetryWait:
		allowed = to == briefSealSealing || to == briefSealPending || to == briefSealFailed || to == briefSealRetryWait
	case briefSealSealed, briefSealWithheld, briefSealFailed:
		allowed = to == briefSealPending
	}
	if !allowed {
		return from, false
	}
	return to, true
}

type briefSealRecord struct {
	State       briefSealState `json:"state"`
	Digest      string         `json:"digest"`
	Reason      string         `json:"reason,omitempty"`
	NextAttempt time.Time      `json:"next_attempt,omitempty"`
}

type briefSealJob struct {
	record  briefSealRecord
	timer   *time.Timer
	active  bool
	changed bool
}

type briefSealJobKey struct {
	service *mobileBriefService
	id      string
}

var briefSealJobs = struct {
	sync.Mutex
	jobs map[briefSealJobKey]*briefSealJob
}{jobs: make(map[briefSealJobKey]*briefSealJob)}
var sealRetryDelays = []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second}
var sealRetryBackoff = time.Hour
var mobileBriefSealRetryAttempts atomic.Uint64

// mobileBriefBeforeRetryAnnounce observes the saved copy at the publication
// boundary in tests; it is nil during normal operation.
var mobileBriefBeforeRetryAnnounce func(string)

func (s *mobileBriefService) sealStatePath(id string) string {
	if s.store.dir == "" || !mobileBriefIDPattern.MatchString(id) {
		return ""
	}
	return filepath.Join(s.store.dir, id+mobileBriefSealStateExt)
}

// Caller holds briefSealJobs. Save the wait or failure before scheduling it;
// if storage is unavailable the live timer still retries.
func (s *mobileBriefService) setSealState(id string, job *briefSealJob, next briefSealState) bool {
	state, ok := transitionBriefSeal(job.record.State, next)
	if !ok {
		return false
	}
	job.record.State = state
	path := s.sealStatePath(id)
	if path != "" {
		if state == briefSealSealed || state == briefSealWithheld {
			_ = os.Remove(path)
		} else if data, err := json.Marshal(job.record); err == nil {
			if err := os.MkdirAll(s.store.dir, 0700); err == nil {
				if err := writeMobileBriefFileAtomic(s.store.dir, filepath.Base(path), data); err != nil {
					log.Printf("[mobile-brief] could not save seal state for %s: %v", id, err)
				}
			}
		}
	}
	return true
}

// A source event is an explicit opportunity to recover a terminal failure.
func (s *mobileBriefService) resetSealFailure(id string) {
	key := briefSealJobKey{s, id}
	briefSealJobs.Lock()
	defer briefSealJobs.Unlock()
	job := briefSealJobs.jobs[key]
	if job == nil {
		data, err := os.ReadFile(s.sealStatePath(id))
		var record briefSealRecord
		if err == nil && json.Unmarshal(data, &record) == nil && record.State == briefSealFailed {
			job = &briefSealJob{record: record}
		}
	}
	if job != nil && job.record.State == briefSealFailed {
		s.setSealState(id, job, briefSealPending)
		delete(briefSealJobs.jobs, key)
		_ = os.Remove(s.sealStatePath(id))
	}
}

// loadSealFailure prevents reads from restarting a terminal failure before
// startup has restored its state.
func (s *mobileBriefService) loadSealFailure(key briefSealJobKey) *briefSealJob {
	data, err := os.ReadFile(s.sealStatePath(key.id))
	var record briefSealRecord
	if err != nil || json.Unmarshal(data, &record) != nil || record.State != briefSealFailed {
		return nil
	}
	job := &briefSealJob{record: record}
	briefSealJobs.jobs[key] = job
	return job
}

func (s *mobileBriefService) sealRetryWaiting(source MobileBriefV1) bool {
	briefSealJobs.Lock()
	defer briefSealJobs.Unlock()
	key := briefSealJobKey{s, source.BriefID}
	job := briefSealJobs.jobs[key]
	if job == nil {
		job = s.loadSealFailure(key)
	}
	return job != nil && job.record.Digest == mobileBriefSourceDigest(source) && (job.active || job.record.State == briefSealRetryWait || job.record.State == briefSealFailed)
}

// scheduleSealRetry keeps one job per service and brief. A changed digest wakes
// a waiting job; a change while it is sealing is picked up before it exits.
func (s *mobileBriefService) scheduleSealRetry(source MobileBriefV1) {
	if s == nil || s.isStopped() || s.sealStatePath(source.BriefID) == "" {
		return
	}
	key := briefSealJobKey{s, source.BriefID}
	digest := mobileBriefSourceDigest(source)
	briefSealJobs.Lock()
	job := briefSealJobs.jobs[key]
	if job == nil {
		job = s.loadSealFailure(key)
	}
	if job == nil {
		job = &briefSealJob{record: briefSealRecord{Digest: digest}}
		briefSealJobs.jobs[key] = job
		s.setSealState(key.id, job, briefSealPending)
	} else if job.record.Digest != digest {
		job.record.Digest = digest
		job.changed = true
		if !job.active {
			if job.timer != nil {
				job.timer.Stop()
				job.timer = nil
			}
			s.setSealState(key.id, job, briefSealPending)
		}
	} else if job.active || job.record.State == briefSealRetryWait || job.record.State == briefSealFailed {
		briefSealJobs.Unlock()
		return
	} else {
		s.setSealState(key.id, job, briefSealPending)
	}
	if !job.active {
		s.startSealJobLocked(key, job)
	}
	briefSealJobs.Unlock()
}

func (s *mobileBriefService) startSealJobLocked(key briefSealJobKey, job *briefSealJob) {
	if !s.beginWork() {
		delete(briefSealJobs.jobs, key)
		return
	}
	job.active = true
	job.changed = false
	go s.runSealJob(key, job)
}

// A missing directory or unreadable Today file can recover; only an absent
// dated source and a definitively absent or different Today source are terminal.
func (s *mobileBriefService) sealSourceMissing(id string) bool {
	if info, err := os.Stat(s.store.dir); err != nil || !info.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(s.store.dir, id+".json")); !os.IsNotExist(err) {
		return false
	}
	if _, err := os.Stat(filepath.Join(s.store.dir, mobileBriefTodayFileName)); os.IsNotExist(err) {
		return true
	} else if err != nil {
		return false
	}
	today, ok := s.store.currentToday()
	return ok && (today.BriefID != id || today.LocalDate != s.readDay())
}

func (s *mobileBriefService) runSealJob(key briefSealJobKey, job *briefSealJob) {
	defer s.wg.Done()
	var last mobileBriefWithholding
	for attempt, delay := range sealRetryDelays {
		if !s.sleepUnlessStopped(delay) {
			break
		}
		briefSealJobs.Lock()
		s.setSealState(key.id, job, briefSealSealing)
		job.changed = false
		briefSealJobs.Unlock()
		current, found := s.storedSource(key.id)
		if found {
			briefSealJobs.Lock()
			if !job.changed {
				job.record.Digest = mobileBriefSourceDigest(current)
			}
			briefSealJobs.Unlock()
			mobileBriefSealRetryAttempts.Add(1)
			sealed, withheld, wrote, permanent := s.sealResult(context.Background(), current)
			last = withheld
			briefSealJobs.Lock()
			changed := job.changed || job.record.Digest != mobileBriefSourceDigest(current)
			if wrote && !changed {
				state := briefSealSealed
				if withheld.Reason != "" {
					state = briefSealWithheld
				}
				s.setSealState(key.id, job, state)
				delete(briefSealJobs.jobs, key)
				briefSealJobs.Unlock()
				s.noteLatestShownIfNewer(sealed)
				if s.app != nil {
					if mobileBriefBeforeRetryAnnounce != nil {
						mobileBriefBeforeRetryAnnounce(key.id)
					}
					s.app.emitBrowserEvent("brief:stored", key.id)
				}
				return
			}
			if permanent && !changed {
				job.record.Reason = "invalid_brief"
				s.setSealState(key.id, job, briefSealFailed)
				job.active = false
				briefSealJobs.Unlock()
				log.Printf("[mobile-brief] brief %s cannot be sealed (reason=invalid_brief)", key.id)
				return
			}
			briefSealJobs.Unlock()
		} else if s.sealSourceMissing(key.id) {
			briefSealJobs.Lock()
			if !job.changed {
				job.record.Reason = "source_missing"
				s.setSealState(key.id, job, briefSealFailed)
				job.active = false
				briefSealJobs.Unlock()
				log.Printf("[mobile-brief] brief %s cannot be sealed (reason=source_missing)", key.id)
				return
			}
			briefSealJobs.Unlock()
		} else {
			last.Reason = mobileBriefWithheldNotReady
		}
		briefSealJobs.Lock()
		job.record.Reason = last.Reason
		if job.record.Reason == "" {
			job.record.Reason = "seal_unavailable"
		}
		wait := sealRetryBackoff
		if attempt+1 < len(sealRetryDelays) {
			wait = sealRetryDelays[attempt+1]
		}
		job.record.NextAttempt = time.Now().Add(wait)
		s.setSealState(key.id, job, briefSealRetryWait)
		briefSealJobs.Unlock()
	}
	briefSealJobs.Lock()
	defer briefSealJobs.Unlock()
	if s.isStopped() {
		delete(briefSealJobs.jobs, key)
		return
	}
	if job.record.State == briefSealSealing {
		s.setSealState(key.id, job, briefSealRetryWait)
	}
	job.record.Reason = last.Reason
	if job.record.Reason == "" {
		job.record.Reason = "seal_unavailable"
	}
	job.record.NextAttempt = time.Now().Add(sealRetryBackoff)
	if job.changed {
		job.record.NextAttempt = time.Now()
	}
	// Record the final next-attempt time even if the last attempt already entered wait.
	s.setSealState(key.id, job, briefSealRetryWait)
	job.active = false
	wait := time.Until(job.record.NextAttempt)
	job.timer = time.AfterFunc(max(wait, 0), func() {
		briefSealJobs.Lock()
		defer briefSealJobs.Unlock()
		if briefSealJobs.jobs[key] != job {
			return
		}
		job.timer = nil
		s.startSealJobLocked(key, job)
	})
	log.Printf("[mobile-brief] the brief %s could not be sealed yet (reason=%s kind=%s); the next try is in %s unless the brief changes", key.id, job.record.Reason, last.SourceKind, sealRetryBackoff)
}

// stopSealRetries cancels timers belonging to this service; a running attempt
// sees the stopped flag and exits. Its saved wait is retained for the next launch.
func (s *mobileBriefService) stopSealRetries() {
	briefSealJobs.Lock()
	defer briefSealJobs.Unlock()
	for key, job := range briefSealJobs.jobs {
		if key.service != s {
			continue
		}
		if job.timer != nil {
			job.timer.Stop()
		}
		delete(briefSealJobs.jobs, key)
	}
}

// resumeSealRetries restores unfinished waits after the service starts.
func (s *mobileBriefService) resumeSealRetries() {
	if s.isStopped() {
		return
	}
	entries, err := os.ReadDir(s.store.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), mobileBriefSealStateExt) {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), mobileBriefSealStateExt)
		if s.sealStatePath(id) == "" {
			continue
		}
		data, err := os.ReadFile(s.sealStatePath(id))
		if err != nil {
			continue
		}
		var record briefSealRecord
		if json.Unmarshal(data, &record) != nil {
			continue
		}
		source, found := s.storedSource(id)
		if found && record.Digest != mobileBriefSourceDigest(source) {
			s.scheduleSealRetry(source)
			continue
		}
		key := briefSealJobKey{s, id}
		briefSealJobs.Lock()
		if s.isStopped() {
			briefSealJobs.Unlock()
			return
		}
		if briefSealJobs.jobs[key] != nil {
			briefSealJobs.Unlock()
			continue
		}
		job := &briefSealJob{record: record}
		briefSealJobs.jobs[key] = job
		switch record.State {
		case briefSealFailed:
			// A changed source or explicit source event can reopen this state.
		case briefSealRetryWait:
			wait := max(time.Until(record.NextAttempt), 0)
			job.timer = time.AfterFunc(wait, func() {
				briefSealJobs.Lock()
				defer briefSealJobs.Unlock()
				if briefSealJobs.jobs[key] == job {
					job.timer = nil
					s.startSealJobLocked(key, job)
				}
			})
		case briefSealPending, briefSealSealing:
			// An interrupted attempt is safe to retry from the beginning.
			if record.State == briefSealSealing {
				s.setSealState(id, job, briefSealRetryWait)
			}
			s.startSealJobLocked(key, job)
		default:
			delete(briefSealJobs.jobs, key)
		}
		briefSealJobs.Unlock()
	}
}

// sleepUnlessStopped waits for d in short steps; false when the service stopped.
func (s *mobileBriefService) sleepUnlessStopped(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		s.mu.Lock()
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			return false
		}
		left := time.Until(deadline)
		if left <= 0 {
			return true
		}
		time.Sleep(min(200*time.Millisecond, left))
	}
}

// finishedRead is source as the readers show it (see finishedReadWithholding).
func (s *mobileBriefService) finishedRead(source MobileBriefV1) (MobileBriefV1, error) {
	brief, _, err := s.finishedReadWithholding(source)
	return brief, err
}

// isStopped reports whether Stop has been called. A stopped service still reads
// its saved copies, but it seals nothing: sealing writes the copy.
func (s *mobileBriefService) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

// saveFinishedRead seals a brief the moment it is made or changed (generation,
// its audio, a patch, Today's refresh).
func (s *mobileBriefService) saveFinishedRead(source MobileBriefV1) {
	if s == nil || s.app == nil || source.BriefID == "" {
		return
	}
	sealed, _, ok := s.seal(context.Background(), source)
	if ok {
		s.noteLatestShownIfNewer(sealed)
	} else {
		s.scheduleSealRetry(source)
	}
}

// storedSource is the stored brief Today shows under briefID: a stored brief,
// or today's snapshot before any.
func (s *mobileBriefService) storedSource(briefID string) (MobileBriefV1, bool) {
	if brief, ok := s.store.load(briefID); ok {
		return brief, true
	}
	brief, ok := s.store.currentToday()
	return brief, ok && brief.BriefID == briefID && brief.LocalDate == s.readDay()
}

// finishedBrief is GetMobileBrief from the sealed read.
func (s *mobileBriefService) finishedBrief(briefID string) (MobileBriefV1, bool, error) {
	brief, _, ok, err := s.finishedBriefWithholding(briefID)
	return brief, ok, err
}

// finishedBriefWithholding is finishedBrief with the reason the words were held
// back, when they were (R40): a store still opening is held; anything else is
// withheld for good.
func (s *mobileBriefService) finishedBriefWithholding(briefID string) (MobileBriefV1, mobileBriefWithholding, bool, error) {
	source, ok := s.storedSource(briefID)
	if !ok {
		return MobileBriefV1{}, mobileBriefWithholding{}, false, nil
	}
	brief, withheld, err := s.finishedReadWithholding(source)
	return brief, withheld, err == nil, err
}

// A live row leaving a template brief hides its recording on this read only
// until Today's refresh stores the brief without that row (see
// mobileBriefTodayReadableSynthesis). Read the brief as that refresh stores
// it: the recording stays when what it speaks is still exactly what is shown.
func (s *mobileBriefService) finishedReadAudio(ctx context.Context, source, delivered MobileBriefV1) *MobileBriefAudio {
	if delivered.Audio != nil || s.app == nil || source.Audio == nil || source.Audio.binding == "" {
		return delivered.Audio
	}
	stored, withheld := s.app.mobileBriefDeliveryContext(ctx, source)
	if withheld.Reason != "" {
		return nil
	}
	stored.Audio = source.Audio
	again := s.app.mobileBriefForDeliveryContext(ctx, stored)
	if again.Audio == nil || !mobileBriefParagraphsSay(again.Narrative, delivered.Narrative) || !mobileBriefParagraphsSay(again.WeeklyRecap, delivered.WeeklyRecap) {
		return nil
	}
	return again.Audio
}

func mobileBriefParagraphsSay(left, right []MobileBriefPara) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Text != right[i].Text {
			return false
		}
	}
	return true
}

// storedBriefFromData parses a stored brief document. The caller holds s.mu.
func (s *mobileBriefStore) storedBriefFromData(data []byte) (MobileBriefV1, bool) {
	var brief MobileBriefV1
	if err := unmarshalStoredMobileBrief(data, &brief); err != nil || !mobileBriefIDPattern.MatchString(brief.BriefID) {
		return MobileBriefV1{}, false
	}
	return brief, true
}
