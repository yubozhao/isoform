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
	// Any handoff removal is a privacy-change event for the briefs that cite it;
	// any handoff save is a source change for them. The removal advances the
	// privacy epoch durably; the amendment of the sealed copies runs after it.
	handoff.RemovalHook = func(string) {
		briefPrivacyChanged()
		briefPrivacyAmendSoon()
	}
	handoff.ChangedHook = func(string) { briefSourceChanged() }
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

// briefPrivacyChanged records a privacy change: every sealed brief
// is sealed again the next time it is read. It is called for withdrawals and
// for the privacy-class app events (app.go). The epoch is saved first, so a
// change is not lost if the app stops before the affected briefs are sealed again.
func briefPrivacyChanged() {
	mobileBriefReadEpoch.Add(1)
	mobileBriefPrivacyEpochMu.Lock()
	defer mobileBriefPrivacyEpochMu.Unlock()
	value := mobileBriefPrivacyEpoch.Add(1)
	if mobileBriefPrivacyEpochSavedDir != "" {
		_ = writeMobileBriefFileAtomic(mobileBriefPrivacyEpochSavedDir, mobileBriefPrivacyEpochFile, []byte(strconv.FormatUint(value, 10)))
	}
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
			s.sealSources()
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

// memorySourcesChangedOnDisk is a privacy change: a memory or knowledge file
// changed outside the app (the memory watcher found it). Its entries may now be
// unreadable or protected, so the sealed briefs are amended.
func memorySourcesChangedOnDisk() {
	briefPrivacyChanged()
	briefPrivacyAmendSoon()
}

// noteMobileBriefReadCachesChanged records a freshness change (a new
// calendar, mail or memory read, a finished run): caches outside the sealed
// briefs are read again, but no sealed brief is sealed again for it. The
// next brief made or refreshed carries the change.
func noteMobileBriefReadCachesChanged() { mobileBriefReadEpoch.Add(1) }

// mobileBriefPrivacyInputEvents are the app events that change what a sealed
// brief may show. Every other event in mobileBriefReadInputEvents is a freshness
// change (noteMobileBriefReadCachesChanged).
var mobileBriefPrivacyInputEvents = map[string]bool{
	"sensitive-apps:updated": true, closedEyesModeChangedEvent: true,
	"background-listening:changed": true, "topics:withdrawn": true, "mail:synced": true,
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
	data, err := os.ReadFile(filepath.Join(dir, mobileBriefPrivacyEpochFile))
	if err != nil {
		return
	}
	saved, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return
	}
	for {
		current := mobileBriefPrivacyEpoch.Load()
		if saved <= current || mobileBriefPrivacyEpoch.CompareAndSwap(current, saved) {
			return
		}
	}
}

// mobileBriefFinishedRead is the sealed copy of a brief as stored beside it.
type mobileBriefFinishedRead struct {
	Schema int    `json:"schema"`
	Key    string `json:"key,omitempty"`
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
	if briefPolicyPending.Load() {
		return MobileBriefV1{}, mobileBriefWithholding{Reason: mobileBriefWithheldNotReady}, false
	}
	mobileBriefFinishedReadKeys.Add(1)
	previous, found, err := s.store.loadFinishedRead(source.BriefID)
	if err != nil {
		return MobileBriefV1{}, mobileBriefWithholding{Reason: mobileBriefWithheldNotReady}, false
	}
	sealedAt := time.Now()
	cited := source
	if found {
		sealedAt = s.store.finishedReadTime(source.BriefID, previous)
		cited = mobileBriefSealedSources(previous, source)
		if kind, changed := s.app.mobileBriefEditedSources(cited, sealedAt, previous.Fingerprints); kind != "" {
			var old MobileBriefV1
			if unmarshalStoredMobileBrief(previous.Brief, &old) != nil {
				old = mobileBriefPaused(source)
			}
			changed = mobileBriefChangedRefs(cited, source, old, changed)
			return s.withholdEditedRead(ctx, previous, old, kind, changed), mobileBriefWithholding{Reason: mobileBriefWithheldSource, SourceKind: kind}, true
		}
	}
	epoch := mobileBriefPrivacyEpoch.Load()
	print := s.app.briefPrivacyPrint()
	day := s.readDay()
	projected, withheld := s.app.mobileBriefForProjectionWithholding(ctx, source, true)
	brief := s.app.mobileBriefWithCards(projected)
	brief.Audio = s.finishedReadAudio(ctx, source, brief)
	if withheld.Transient() || print != s.app.briefPrivacyPrint() || day != s.readDay() {
		return brief, withheld, false
	}
	raw, err := marshalStoredMobileBrief(brief)
	if err != nil {
		return brief, withheld, false
	}
	fingerprints := previous.Fingerprints
	if !found {
		if kind, changed := s.app.mobileBriefEditedSources(cited, sealedAt, nil); kind != "" {
			changed = mobileBriefChangedRefs(cited, source, brief, changed)
			return mobileBriefWithholdEdited(brief, changed), mobileBriefWithholding{Reason: mobileBriefWithheldSource, SourceKind: kind}, true
		}
		fingerprints = s.app.mobileBriefSourceFingerprints(ctx, cited)
	}
	if kind, changed := s.app.mobileBriefEditedSources(cited, sealedAt, fingerprints); kind != "" {
		changed = mobileBriefChangedRefs(cited, source, brief, changed)
		return mobileBriefWithholdEdited(brief, changed), mobileBriefWithholding{Reason: mobileBriefWithheldSource, SourceKind: kind}, true
	}
	read := mobileBriefFinishedRead{Schema: mobileBriefSealedSchema, Epoch: epoch, Print: print, Source: mobileBriefSourceDigest(source), Day: day, Cites: sealCites(source), SealedAt: sealedAt, Fingerprints: fingerprints, SourceRefs: mobileBriefSealSources(cited), WithheldReason: withheld.Reason, WithheldKind: withheld.SourceKind, Brief: raw}
	if mobileBriefBeforeSealSave != nil {
		mobileBriefBeforeSealSave(source.BriefID)
	}
	if err := s.store.saveFinishedReadIfCurrent(source.BriefID, read); err != nil {
		return brief, withheld, false
	}
	return brief, withheld, true
}

// finishedReadWithholding is source as the readers show it. A display read
// touches no store and never seals: it returns the sealed copy. When the copy
// no longer matches what it was sealed under (the privacy print, the source, or
// the local day), the brief is sealed again in the background, and the copy is
// shown meanwhile, without words and audio when Closed Eyes is on. A brief with
// no copy yet is sealed in the background and is not shown until then.
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
	if !pending && s.app != nil {
		saved, found, err := s.store.loadFinishedRead(source.BriefID)
		if err != nil {
			return MobileBriefV1{}, mobileBriefWithholding{}, err
		}
		if found {
			cited := mobileBriefSealedSources(saved, source)
			if kind, changed := s.app.mobileBriefEditedSources(cited, s.store.finishedReadTime(source.BriefID, saved), saved.Fingerprints); kind != "" {
				changed = mobileBriefChangedRefs(cited, source, brief, changed)
				return s.withholdEditedRead(context.Background(), saved, brief, kind, changed), mobileBriefWithholding{Reason: mobileBriefWithheldSource, SourceKind: kind}, nil
			}
		}
		if kind, changed := s.copyPrivacyChange(source); changed {
			// The amend: the privacy state changed since the copy was sealed, so the
			// next read seals the brief again with the current gates and shows that.
			// Only when the gates cannot decide yet are the words withheld, and the
			// brief is sealed again in the background.
			if sealed, amended, wrote := s.amendSealedCopy(source); wrote {
				return sealed, amended, nil
			}
			s.scheduleSealRetry(source)
			return mobileBriefPaused(brief), mobileBriefWithholding{Reason: mobileBriefWithheldPrivacyCheck, SourceKind: kind}, nil
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
		if sealRetryBackedOff(source.BriefID, mobileBriefSourceDigest(source), time.Now()) {
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
	brief, err := s.sealedRead(source)
	return brief, mobileBriefWithholding{Reason: saved.WithheldReason, SourceKind: saved.WithheldKind}, false, err
}

// mobileBriefWithheldPrivacyCheck is the withholding of a copy whose privacy
// state no longer matches the state it was sealed under (see finishedReadWithholding).
const mobileBriefWithheldPrivacyCheck = "privacy_check"

// mobileBriefLive is the service whose sealed briefs a source change re-seals.
var mobileBriefLive atomic.Pointer[mobileBriefService]

func (s *mobileBriefService) sealedRead(source MobileBriefV1) (MobileBriefV1, error) {
	saved, found, err := s.store.loadFinishedRead(source.BriefID)
	if err != nil {
		return MobileBriefV1{}, err
	}
	var brief MobileBriefV1
	if !found || saved.Schema != mobileBriefSealedSchema || unmarshalStoredMobileBrief(saved.Brief, &brief) != nil || brief.BriefID != source.BriefID {
		s.scheduleSealRetry(source)
		return MobileBriefV1{}, nil
	}
	if s.app == nil || saved.Print != s.app.briefPrivacyPrint() || saved.Source != mobileBriefSourceDigest(source) || saved.Day != s.readDay() {
		s.scheduleSealRetry(source)
	}
	if s.app != nil && s.app.IsClosedEyesModeEnabled() {
		// Closed Eyes is known in memory: nothing the brief says stays readable.
		return mobileBriefPaused(brief), nil
	}
	if s.app != nil && (s.app.store == nil || s.app.store.Closing()) {
		// The policy store is closed, so a web source's hold cannot be confirmed:
		// every web-source item is left out and the rest of the brief shows.
		brief = mobileBriefWithoutWebSources(brief)
	}
	return brief, nil
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

// briefSourceChanged says a source a sealed brief was made from has changed (a
// handoff, a belief, the audio, a week answer). Every stored brief is sealed
// again before the action that fired it returns. Hooks run after their locks
// are released (see briefSaved), so a re-seal may read the stores.
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
	// The saver announces the brief (brief:stored) itself; sealing it here
	// only saves the copy, so one stored brief is not announced twice.
	sealed, withheld, wrote := s.seal(context.Background(), source)
	if !wrote {
		if withheld.Transient() {
			s.scheduleSealRetry(source)
		}
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
	sealed, withheld, wrote := s.seal(context.Background(), source)
	if !wrote {
		if withheld.Transient() {
			s.scheduleSealRetry(source)
		}
		return
	}
	s.noteLatestShownIfNewer(sealed)
	if s.app != nil {
		s.app.emitBrowserEvent("brief:stored", source.BriefID)
	}
}

// sealRetryDelays are the waits before a brief whose gates could not decide yet
// (a store still opening) is sealed again. Its surfaces refresh when it is saved.
var sealRetryDelays = []time.Duration{time.Second, 3 * time.Second, 10 * time.Second, 30 * time.Second}

// sealRetryBackoff is how long a brief whose seal still fails after its retries
// is left alone. Reads in that window keep showing the withheld brief and do not
// run the gates again. A change to the brief's stored content (a new digest) or
// a restart tries again at once.
const sealRetryBackoff = time.Hour

// sealRetryPause is the backoff of one brief: the stored-content digest it failed
// under and the time before which no new attempt is made for that digest.
type sealRetryPause struct {
	digest string
	until  time.Time
}

// sealRetryPaused maps a brief ID to its sealRetryPause.
var sealRetryPaused sync.Map

// mobileBriefSealRetryAttempts counts the sealing attempts the background retries
// make, so tests can see that a backed-off brief is not tried again on each read.
var mobileBriefSealRetryAttempts atomic.Uint64

// sealRetryBackedOff reports whether a brief failed to seal under this digest
// recently enough that it should not be tried again yet.
func sealRetryBackedOff(briefID, digest string, now time.Time) bool {
	value, ok := sealRetryPaused.Load(briefID)
	if !ok {
		return false
	}
	pause := value.(sealRetryPause)
	return pause.digest == digest && now.Before(pause.until)
}

// sealRetrying names the briefs with a retry in flight, so each has one.
var sealRetrying sync.Map

// scheduleSealRetry seals a brief again in the background while its gates cannot
// decide, then announces it (brief:stored) so Today and the other surfaces read
// it. The waits poll the service's stop flag, so shutdown never waits on one.
// When every retry fails, the reason is logged once and the brief is left alone
// for sealRetryBackoff, unless its stored content changes first.
func (s *mobileBriefService) scheduleSealRetry(source MobileBriefV1) {
	if sealRetryBackedOff(source.BriefID, mobileBriefSourceDigest(source), time.Now()) {
		return
	}
	if _, busy := sealRetrying.LoadOrStore(source.BriefID, true); busy {
		return
	}
	if !s.beginWork() {
		sealRetrying.Delete(source.BriefID)
		return
	}
	go func() {
		defer s.wg.Done()
		defer sealRetrying.Delete(source.BriefID)
		var last mobileBriefWithholding
		for _, wait := range sealRetryDelays {
			if !s.sleepUnlessStopped(wait) {
				return
			}
			current, ok := s.storedSource(source.BriefID)
			if !ok {
				return
			}
			mobileBriefSealRetryAttempts.Add(1)
			sealed, withheld, wrote := s.seal(context.Background(), current)
			if wrote {
				sealRetryPaused.Delete(source.BriefID)
				s.noteLatestShownIfNewer(sealed)
				if s.app != nil {
					s.app.emitBrowserEvent("brief:stored", source.BriefID)
				}
				return
			}
			last = withheld
		}
		sealRetryPaused.Store(source.BriefID, sealRetryPause{
			digest: mobileBriefSourceDigest(source),
			until:  time.Now().Add(sealRetryBackoff),
		})
		log.Printf("[mobile-brief] the brief %s could not be sealed yet (reason=%s kind=%s); the next try is in %s unless the brief changes",
			source.BriefID, last.Reason, last.SourceKind, sealRetryBackoff)
	}()
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
