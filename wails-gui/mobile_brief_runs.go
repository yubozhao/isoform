package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Brief run health (docs/notifications-design.md section 6). Every scheduled
// slot ends as one record: ok with the build time, failed with the attempt
// count while retries continue, or missed when the slot's window closed (the
// next slot arrived) without a brief. Today and Settings read last run, next run and the missed
// runs of the last seven days from these records, and the brief_missed
// notification fires once per slot from them.

const (
	mobileBriefRunsFileName = "brief-runs.json"
	mobileBriefRunsVersion  = 1
	mobileBriefRunsKeep     = 60
	// mobileBriefMissedAfterFailures is how many failed builds make a missed
	// slot read as one that kept failing rather than one slept through.
	mobileBriefMissedAfterFailures = 3
	mobileBriefHealthWindow        = 7 * 24 * time.Hour

	mobileBriefRunOK     = "ok"
	mobileBriefRunFailed = "failed"
	mobileBriefRunMissed = "missed"

	mobileBriefMissedAsleep = "asleep"
	mobileBriefMissedClosed = "closed"
)

// mobileBriefRun is one scheduled slot's outcome.
type mobileBriefRun struct {
	BriefID     string `json:"briefId"`
	Slot        string `json:"slot"`
	ScheduledAt string `json:"scheduledAt"`
	// ScheduledReady distinguishes a configured slot from legacy adaptive
	// preparation timestamps when the schedule changes later.
	ScheduledReady bool   `json:"scheduledReady,omitempty"`
	Status         string `json:"status"`
	At             string `json:"at"`
	Attempts       int    `json:"attempts,omitempty"`
	FailureKey     string `json:"failureKey,omitempty"`
	// FailureText is the failure's first line, redacted and at most 200 bytes,
	// kept with the key so the next failure of a kind can be read on the record.
	FailureText      string `json:"failureText,omitempty"`
	FailureCause     string `json:"failureCause,omitempty"`
	FailureAssistant string `json:"failureAssistant,omitempty"`
	SameFailures     int    `json:"sameFailures,omitempty"`
	// Reason says why a missed slot didn't run, only when Isoform observed
	// it (mobileBriefService.missedReason): asleep or closed. Empty when the
	// record can't say.
	Reason string `json:"reason,omitempty"`
	// Notified marks that the missed-brief moment was submitted for this slot.
	Notified bool `json:"notified,omitempty"`
	// Early is set on the run of a brief made before its slot: what made it, when,
	// and whether the slot's own build replaced it (replaced=true).
	Early *mobileBriefEarlyRun `json:"early,omitempty"`
}

// mobileBriefEarlyRun is the record of an early brief.
type mobileBriefEarlyRun struct {
	Trigger  string `json:"trigger"`
	MadeAt   string `json:"madeAt"`
	Replaced bool   `json:"replaced"`
}

type mobileBriefRunsFile struct {
	Version int `json:"version"`
	// FirstSeenAt is when the service first looked at the schedule. Slots
	// before it are never missed, so a fresh install is not greeted with a
	// missed-brief notice for a slot that passed before Isoform existed.
	FirstSeenAt string           `json:"firstSeenAt,omitempty"`
	Runs        []mobileBriefRun `json:"runs"`
}

// mobileBriefRunStore keeps brief-runs.json in the mobile state directory,
// newest run last, at most mobileBriefRunsKeep entries.
type mobileBriefRunStore struct {
	path string
	mu   sync.Mutex
}

func newMobileBriefRunStore(stateDir string) *mobileBriefRunStore {
	store := &mobileBriefRunStore{}
	if stateDir != "" {
		store.path = filepath.Join(stateDir, mobileBriefRunsFileName)
	}
	return store
}

// list returns every recorded run, oldest first. A missing or unreadable
// file is an empty list, and so is a service built without a run store.
func (s *mobileBriefRunStore) list() []mobileBriefRun {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, _, _ := s.loadLocked()
	return file.Runs
}

// firstSeen returns when the schedule was first watched, recording now when
// nothing was recorded yet. Without a state directory it is always now, so no
// slot ever reads as missed.
func (s *mobileBriefRunStore) firstSeen(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, _, loadErr := s.loadLocked()
	if loadErr != nil {
		return now
	}
	if seen, err := time.Parse(time.RFC3339Nano, file.FirstSeenAt); err == nil {
		return seen
	}
	if s.path == "" {
		return now
	}
	file.FirstSeenAt = formatMobileBriefTime(now)
	if err := s.saveFileLocked(file); err != nil {
		return now
	}
	return now
}

// seenAt returns when the schedule was first watched, without recording it.
func (s *mobileBriefRunStore) seenAt() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, _, loadErr := s.loadLocked()
	if loadErr != nil {
		return time.Time{}, false
	}
	seen, err := time.Parse(time.RFC3339Nano, file.FirstSeenAt)
	return seen, err == nil
}

// mobileBriefRunsState distinguishes first use from data that needs recovery.
type mobileBriefRunsState int

const (
	mobileBriefRunsMissing mobileBriefRunsState = iota
	mobileBriefRunsValid
	mobileBriefRunsUnsupported
	mobileBriefRunsUnreadable
)

// mobileBriefRunsTransition only allows writes from a known schema or first use.
func mobileBriefRunsTransition(state mobileBriefRunsState) bool {
	switch state {
	case mobileBriefRunsMissing, mobileBriefRunsValid:
		return true
	case mobileBriefRunsUnsupported, mobileBriefRunsUnreadable:
		return false
	default:
		return false
	}
}

func (s *mobileBriefRunStore) loadLocked() (mobileBriefRunsFile, mobileBriefRunsState, error) {
	empty := mobileBriefRunsFile{Version: mobileBriefRunsVersion}
	if s.path == "" {
		return empty, mobileBriefRunsMissing, nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, mobileBriefRunsMissing, nil
	}
	if err != nil {
		return mobileBriefRunsFile{}, mobileBriefRunsUnreadable, err
	}
	var file mobileBriefRunsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return mobileBriefRunsFile{}, mobileBriefRunsUnreadable, err
	}
	if file.Version != mobileBriefRunsVersion {
		return mobileBriefRunsFile{}, mobileBriefRunsUnsupported, fmt.Errorf("unsupported brief run history version %d", file.Version)
	}
	return file, mobileBriefRunsValid, nil
}

func (s *mobileBriefRunStore) saveLocked(runs []mobileBriefRun) error {
	file, state, err := s.loadLocked()
	if !mobileBriefRunsTransition(state) {
		return err
	}
	file.Runs = runs
	return s.saveFileLocked(file)
}

func (s *mobileBriefRunStore) saveFileLocked(file mobileBriefRunsFile) error {
	if s.path == "" {
		return errMobileBriefStateUnavailable
	}
	file.Version = mobileBriefRunsVersion
	if len(file.Runs) > mobileBriefRunsKeep {
		file.Runs = file.Runs[len(file.Runs)-mobileBriefRunsKeep:]
	}
	if file.Runs == nil {
		file.Runs = []mobileBriefRun{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return writeMobileBriefFileAtomic(dir, filepath.Base(s.path), data)
}

// record upserts the run for a brief id and returns the stored record. A slot
// already marked ok or missed is never moved back to failed, and the notified
// flag survives every update.
// markEarlyReplaced sets replaced=true on the early run of a brief that its slot's
// own build replaced.
func (s *mobileBriefRunStore) markEarlyReplaced(briefID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, state, err := s.loadLocked()
	if !mobileBriefRunsTransition(state) {
		return err
	}
	runs := file.Runs
	for i := range runs {
		if runs[i].BriefID == briefID && runs[i].Early != nil {
			runs[i].Early.Replaced = true
			return s.saveLocked(runs)
		}
	}
	return nil
}

func (s *mobileBriefRunStore) record(run mobileBriefRun) (mobileBriefRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, state, err := s.loadLocked()
	if !mobileBriefRunsTransition(state) {
		return mobileBriefRun{}, err
	}
	runs := file.Runs
	for i, existing := range runs {
		if existing.BriefID != run.BriefID {
			continue
		}
		// The slot's own run keeps the early record and its replaced flag.
		if run.Early == nil && existing.Early != nil {
			run.Early = existing.Early
		}
		switch existing.Status {
		case mobileBriefRunOK, mobileBriefRunMissed:
			if run.Status == mobileBriefRunFailed {
				return existing, nil
			}
		default:
		}
		switch run.Status {
		case mobileBriefRunFailed:
			run.Attempts = existing.Attempts + 1
			if run.FailureKey != "" && run.FailureKey == existing.FailureKey {
				run.SameFailures = existing.SameFailures + 1
			} else if run.FailureKey != "" {
				run.SameFailures = 1
			}
		case mobileBriefRunMissed:
			// The failed attempts stay on the record once the window closed.
			run.Attempts = existing.Attempts
			run.FailureKey, run.FailureText, run.FailureCause, run.FailureAssistant, run.SameFailures = existing.FailureKey, existing.FailureText, existing.FailureCause, existing.FailureAssistant, existing.SameFailures
			if run.Attempts > 0 {
				run.Reason = ""
			}
		default:
		}
		run.Notified = run.Notified || existing.Notified
		runs[i] = run
		return run, s.saveLocked(runs)
	}
	if run.Status == mobileBriefRunFailed && run.Attempts == 0 {
		run.Attempts = 1
	}
	if run.Status == mobileBriefRunFailed && run.FailureKey != "" {
		run.SameFailures = 1
	}
	runs = append(runs, run)
	return run, s.saveLocked(runs)
}

// markNotified remembers that the missed-brief moment went out for a slot.
func (s *mobileBriefRunStore) markNotified(briefID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, state, err := s.loadLocked()
	if !mobileBriefRunsTransition(state) {
		return err
	}
	runs := file.Runs
	for i := range runs {
		if runs[i].BriefID == briefID {
			if runs[i].Notified {
				return nil
			}
			runs[i].Notified = true
			return s.saveLocked(runs)
		}
	}
	return nil
}

// mobileBriefSlotOccurrence is one scheduled slot on one local date.
type mobileBriefSlotOccurrence struct {
	BriefID     string
	Slot        string
	ScheduledAt time.Time
}

// mobileBriefTodaySlots lists today's scheduled slots in clock order.
func mobileBriefTodaySlots(now time.Time, loc *time.Location, settings mobileBriefSettings) []mobileBriefSlotOccurrence {
	if loc == nil {
		loc = time.Local
	}
	local := now.In(loc)
	year, month, day := local.Date()
	datePrefix := local.Format("2006-01-02")
	var out []mobileBriefSlotOccurrence
	for _, candidate := range mobileBriefSlotClock(settings) {
		out = append(out, mobileBriefSlotOccurrence{
			BriefID:     datePrefix + "-" + candidate.slot,
			Slot:        candidate.slot,
			ScheduledAt: time.Date(year, month, day, candidate.hour, candidate.minute, 0, 0, loc),
		})
	}
	return out
}

// mobileBriefSlotCloses ends a slot's window: the next slot, capped by noon
// for a slot before noon and by midnight for one from noon, the times a brief
// is named by (today-review v1.1.16 D14, v1.1.21 R49). Until then a brief that
// failed, or was missed while the computer was off, asleep or restarting, is
// still made.
func mobileBriefSlotCloses(occurrence mobileBriefSlotOccurrence, loc *time.Location, settings mobileBriefSettings) time.Time {
	if loc == nil {
		loc = time.Local
	}
	at := occurrence.ScheduledAt.In(loc)
	closes := time.Date(at.Year(), at.Month(), at.Day()+1, 0, 0, 0, 0, loc)
	if at.Hour() < 12 {
		closes = time.Date(at.Year(), at.Month(), at.Day(), 12, 0, 0, 0, loc)
	}
	if next, ok := mobileBriefNextSlot(occurrence.ScheduledAt, loc, settings); ok && next.ScheduledAt.Before(closes) {
		closes = next.ScheduledAt
	}
	return closes
}

// mobileBriefRetryBackoff is the wait after the given number of failed builds
// of one slot in a row: 2, 5, 10 and 20 minutes, then every 30 minutes until
// the slot's window closes.
func mobileBriefRetryBackoff(attempts int) time.Duration {
	steps := []time.Duration{mobileBriefRetryDelay, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute}
	if attempts < 1 {
		attempts = 1
	}
	if attempts > len(steps) {
		return 30 * time.Minute
	}
	return steps[attempts-1]
}

// mobileBriefPendingOccurrence is today's slot whose time has passed while its
// window is still open, with no brief made since then: the brief Today says is
// on its way. allowed says whether a slot scheduled at a time may still be
// made (mobileBriefService.catchUpAllowed). The second result is false when
// nothing is pending.
func mobileBriefPendingOccurrence(now time.Time, loc *time.Location, settings mobileBriefSettings, latest MobileBriefV1, hasLatest bool, allowed func(time.Time) bool) (mobileBriefSlotOccurrence, bool) {
	if !settings.Enabled {
		return mobileBriefSlotOccurrence{}, false
	}
	if loc == nil {
		loc = time.Local
	}
	latestAt := time.Time{}
	if hasLatest {
		latestAt = mobileBriefAsOfTime(latest)
	}
	for _, occurrence := range mobileBriefTodaySlots(now, loc, settings) {
		if now.Before(occurrence.ScheduledAt) || !now.Before(mobileBriefSlotCloses(occurrence, loc, settings)) || (allowed != nil && !allowed(occurrence.ScheduledAt)) {
			continue
		}
		if hasLatest && (latest.BriefID == occurrence.BriefID || !latestAt.Before(occurrence.ScheduledAt) || mobileBriefMadeJustBefore(latestAt, occurrence.ScheduledAt)) {
			continue
		}
		return occurrence, true
	}
	return mobileBriefSlotOccurrence{}, false
}

// mobileBriefMissedSlots lists yesterday's and today's slots whose window has
// closed (mobileBriefSlotCloses) without a stored brief, skipping slots
// scheduled before since (when the schedule was first watched). Yesterday is
// included because the evening slot's window closes at midnight and is only
// seen on the next local date.
func mobileBriefMissedSlots(now time.Time, loc *time.Location, settings mobileBriefSettings, exists func(briefID string) bool, since time.Time) []mobileBriefSlotOccurrence {
	if loc == nil {
		loc = time.Local
	}
	var out []mobileBriefSlotOccurrence
	for _, day := range []int{-1, 0} {
		for _, occurrence := range mobileBriefTodaySlots(now.In(loc).AddDate(0, 0, day), loc, settings) {
			if occurrence.ScheduledAt.Before(since) || now.Before(mobileBriefSlotCloses(occurrence, loc, settings)) || exists(occurrence.BriefID) {
				continue
			}
			out = append(out, occurrence)
		}
	}
	return out
}

// mobileBriefScheduleFacts is what the scheduler (checkDue) knows when it
// decides whether a slot runs: which briefs are stored, whether a brief made
// on request after a slot's time stands in for it, whether a late slot may
// still be made, and when a failed slot is tried again. A nil field reads as
// nothing stored, nothing standing in, catch-up allowed and no retry wait.
type mobileBriefScheduleFacts struct {
	Exists         func(briefID string) bool
	StandsIn       func(scheduledAt time.Time) bool
	CatchUpAllowed func(scheduledAt time.Time) bool
	RetryAt        func(briefID string) time.Time
	// MadeJustBefore says a brief was made in the two hours before a slot's
	// time, so that slot is skipped (today-review v1.1.16 D17).
	MadeJustBefore func(scheduledAt time.Time) bool
}

// mobileBriefSkipAfterMade is how recently a brief may have been made for the
// next scheduled one to be skipped.
const mobileBriefSkipAfterMade = 2 * time.Hour

// mobileBriefMadeJustBefore reports a brief made at madeAt in the two hours
// before scheduledAt.
func mobileBriefMadeJustBefore(madeAt, scheduledAt time.Time) bool {
	return !madeAt.IsZero() && madeAt.Before(scheduledAt) && scheduledAt.Sub(madeAt) <= mobileBriefSkipAfterMade
}

// mobileBriefSlotNextRun is when the scheduler next makes slot, the one next
// time Today, Tasks and the phone show: today's late slot while its window is
// open and the scheduler will still make it (now, or at its retry), otherwise
// the next occurrence after now whose brief isn't stored. catchUp says it is
// today's late slot. Zero when the brief is off or no slot is set.
func mobileBriefSlotNextRun(now time.Time, loc *time.Location, settings mobileBriefSettings, slot string, facts mobileBriefScheduleFacts) (at time.Time, catchUp bool) {
	if !settings.Enabled {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.Local
	}
	for day := 0; day < 2; day++ {
		for _, occurrence := range mobileBriefTodaySlots(now.In(loc).AddDate(0, 0, day), loc, settings) {
			if occurrence.Slot != slot || (facts.Exists != nil && facts.Exists(occurrence.BriefID)) {
				continue
			}
			if facts.MadeJustBefore != nil && facts.MadeJustBefore(occurrence.ScheduledAt) {
				continue
			}
			if occurrence.ScheduledAt.After(now) {
				return occurrence.ScheduledAt, false
			}
			closes := mobileBriefSlotCloses(occurrence, loc, settings)
			if day != 0 || !now.Before(closes) {
				continue
			}
			if facts.CatchUpAllowed != nil && !facts.CatchUpAllowed(occurrence.ScheduledAt) {
				continue
			}
			if facts.StandsIn != nil && facts.StandsIn(occurrence.ScheduledAt) {
				continue
			}
			next := now
			if facts.RetryAt != nil {
				if retry := facts.RetryAt(occurrence.BriefID); !retry.IsZero() {
					next = retry
				}
			}
			if next.Before(closes) {
				return next, true
			}
		}
	}
	return time.Time{}, false
}

// mobileBriefScheduledNextRun is the soonest slot the scheduler makes at its
// own time, skipping today's late slot (Today shows that as PendingSlot).
func mobileBriefScheduledNextRun(now time.Time, loc *time.Location, settings mobileBriefSettings, facts mobileBriefScheduleFacts) (mobileBriefSlotOccurrence, bool) {
	facts.CatchUpAllowed = func(time.Time) bool { return false }
	var soonest mobileBriefSlotOccurrence
	found := false
	for _, entry := range mobileBriefSlotClock(settings) {
		at, _ := mobileBriefSlotNextRun(now, loc, settings, entry.slot, facts)
		if at.IsZero() || (found && !at.Before(soonest.ScheduledAt)) {
			continue
		}
		soonest, found = mobileBriefSlotOccurrence{Slot: entry.slot, ScheduledAt: at}, true
	}
	return soonest, found
}

// mobileBriefNextSlot returns the first scheduled slot after now, today or
// tomorrow. The second result is false when no slot is configured.
func mobileBriefNextSlot(now time.Time, loc *time.Location, settings mobileBriefSettings) (mobileBriefSlotOccurrence, bool) {
	if loc == nil {
		loc = time.Local
	}
	for day := 0; day < 2; day++ {
		for _, occurrence := range mobileBriefTodaySlots(now.In(loc).Add(time.Duration(day)*24*time.Hour), loc, settings) {
			if occurrence.ScheduledAt.After(now) {
				return occurrence, true
			}
		}
	}
	return mobileBriefSlotOccurrence{}, false
}

// MobileBriefHealth is what Today and Settings show about scheduled briefs.
// Times are RFC 3339; empty means unknown.
type MobileBriefHealth struct {
	Enabled bool `json:"enabled"`
	// LastRunAt is when the newest stored brief was built; LastSlot names it.
	LastRunAt string `json:"lastRunAt"`
	LastSlot  string `json:"lastSlot"`
	// NextRunAt is the next scheduled slot; NextSlot names it.
	NextRunAt string `json:"nextRunAt"`
	NextSlot  string `json:"nextSlot"`
	// MissedRuns counts slots missed in the last seven days.
	MissedRuns int `json:"missedRuns"`
	// FailingSlot names a slot whose build has failed and is being retried,
	// with the attempt count; empty when nothing is failing.
	FailingSlot    string `json:"failingSlot"`
	FailedAttempts int    `json:"failedAttempts"`
	// PendingSlot names today's slot whose time has passed with no brief yet
	// while its window is open (mobileBriefPendingOccurrence); PendingAt is
	// its scheduled time and RetryAt, when set, the next try after a failure.
	PendingSlot string `json:"pendingSlot"`
	PendingAt   string `json:"pendingAt"`
	RetryAt     string `json:"retryAt"`
	// PendingAttempts is how many times the pending slot's build has failed,
	// from its run record; 0 when the record has no count.
	PendingAttempts int `json:"pendingAttempts,omitempty"`
	// PendingCause is why the last try failed, when Isoform knows (e.g. "timeout").
	PendingCause     string `json:"pendingCause,omitempty"`
	PendingAssistant string `json:"pendingAssistant,omitempty"`
	// LatestBriefID names the newest stored brief and Audio its spoken
	// version, when one was stored, so Today can offer to play it.
	LatestBriefID string            `json:"latestBriefId"`
	Audio         *MobileBriefAudio `json:"audio,omitempty"`
	// CurrentBriefID names the current brief (mobile_brief_current.go) and
	// Audio is set only when it has words and a recording; it is set by
	// GetMobileBriefHealth, not by the run records.
	CurrentBriefID string `json:"currentBriefId"`
}

// mobileBriefHealthView composes the health view from the schedule, the
// newest stored brief and the run records. It reads no clock of its own.
func mobileBriefHealthView(now time.Time, loc *time.Location, settings mobileBriefSettings, latest MobileBriefV1, hasLatest bool, runs []mobileBriefRun) MobileBriefHealth {
	view := MobileBriefHealth{Enabled: settings.Enabled}
	if hasLatest {
		view.LastRunAt = latest.AsOf
		view.LastSlot = latest.Slot
		view.LatestBriefID = latest.BriefID
		// Today's existing play control interprets any Audio as playable.
		// A status-only phone response has no file to offer there yet.
		if latest.Audio != nil && latest.Audio.Bytes > 0 {
			view.Audio = latest.Audio
		}
	}
	if settings.Enabled {
		if next, ok := mobileBriefNextSlot(now, loc, settings); ok {
			view.NextRunAt = formatMobileBriefTime(next.ScheduledAt)
			view.NextSlot = next.Slot
		}
	}
	since := now.Add(-mobileBriefHealthWindow)
	sorted := append([]mobileBriefRun(nil), runs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ScheduledAt < sorted[j].ScheduledAt })
	for _, run := range sorted {
		scheduled, err := time.Parse(time.RFC3339Nano, run.ScheduledAt)
		if err != nil || scheduled.Before(since) {
			continue
		}
		switch run.Status {
		case mobileBriefRunMissed:
			view.MissedRuns++
		case mobileBriefRunFailed:
			view.FailingSlot = run.Slot
			view.FailedAttempts = run.Attempts
		default:
		}
	}
	return view
}

// mobileBriefWithRunTiming projects only the matching completed run. The
// generation metadata on an old brief is not evidence of a cause.
func mobileBriefWithRunTiming(brief MobileBriefV1, runs []mobileBriefRun) MobileBriefV1 {
	brief.Generation = nil
	for _, run := range runs {
		if run.BriefID != brief.BriefID || run.Status != mobileBriefRunOK {
			continue
		}
		scheduled, scheduleErr := time.Parse(time.RFC3339Nano, run.ScheduledAt)
		at, atErr := time.Parse(time.RFC3339Nano, run.At)
		if scheduleErr != nil || atErr != nil || at.Sub(scheduled) <= mobileBriefLateAfter {
			return brief
		}
		brief.Generation = &MobileBriefGeneration{ScheduledAt: run.ScheduledAt, GeneratedAt: run.At, Reason: briefScheduleRecordedReason(run.Reason)}
		return brief
	}
	return brief
}
