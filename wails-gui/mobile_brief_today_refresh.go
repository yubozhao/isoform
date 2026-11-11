package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"wails-gui/internal/calendar"
)

// Current local signals share the stored brief, but do not generate a recap,
// send a notification or advance the scheduled brief's coverage window. Before
// the first brief, today.json holds the same document outside slot history.
const mobileBriefTodayFileName = "today.json"

func (s *mobileBriefService) latestWithToday() (MobileBriefV1, bool) {
	if !s.beginWork() {
		return s.store.current()
	}
	defer s.wg.Done()
	brief, ok, changed := s.refreshToday(false)
	if changed && ok && s.app != nil {
		// A dated brief the refresh saved is sealed by that save. Only the
		// Today-only document (not in the dated store) needs the seal here, so the
		// read-path seal never writes a second copy of a stored brief.
		if _, stored := s.store.load(brief.BriefID); !stored {
			s.saveFinishedRead(brief)
		}
	}
	return brief, ok
}

// todayRefreshGap is the least time between two live Today refreshes. Source
// events ask for one as they arrive; a request inside the gap runs once when
// the gap ends, so a burst of events costs one refresh (R24).
const todayRefreshGap = 5 * time.Minute

// todayRefreshBudget is how old the last live refresh may be before a read of
// Today asks for another. Reads do not refresh inside it.
const todayRefreshBudget = 15 * time.Minute

// mobileBriefTodayRefreshRuns counts Today refresh passes, for tests.
var mobileBriefTodayRefreshRuns atomic.Uint64

// refreshTodaySoon brings the stored Today signals up to date off the read
// path. A source read takes minutes on a busy computer, and a live refresh
// holds todayMu throughout, so Today reads the stored brief at once
// (GetLatestBrief) and a stored change is announced (brief:stored) for the
// next read. A request during a refresh runs one more after it, and no
// refresh starts sooner than todayRefreshGap after the last one.
func (s *mobileBriefService) refreshTodaySoon() {
	s.requestTodayRefresh(true)
}

// refreshTodayLiveSoon is the refresh a mail sync asks for. It refreshes Today's
// live sections like refreshTodaySoon, but a brief that already has a sealed
// copy is neither stored nor sealed again: a mail sync is not a privacy change
// (see refreshToday).
func (s *mobileBriefService) refreshTodayLiveSoon() {
	s.requestTodayRefresh(false)
}

// requestTodayRefresh asks for a live refresh. store says whether the pass may
// store what it finds for a brief that has a sealed copy. A pending store request
// stays pending until a pass takes it, so a queued live-only request never turns
// a store request into a live-only one.
func (s *mobileBriefService) requestTodayRefresh(store bool) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.todayRefreshMu.Lock()
	if store {
		s.todayRefreshStore = true
	}
	if s.todayRefreshing {
		s.todayRefreshAgain = true
		s.todayRefreshMu.Unlock()
		s.mu.Unlock()
		return
	}
	if wait := s.todayRefreshWaitLocked(time.Now()); wait > 0 {
		s.queueTodayRefreshLocked(wait)
		s.todayRefreshMu.Unlock()
		s.mu.Unlock()
		return
	}
	s.todayRefreshing = true
	s.todayRefreshAt = time.Now()
	s.wg.Add(1)
	s.todayRefreshWG.Add(1)
	s.todayRefreshMu.Unlock()
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer s.todayRefreshWG.Done()
		for {
			mobileBriefTodayRefreshRuns.Add(1)
			s.todayRefreshMu.Lock()
			storePass := s.todayRefreshStore
			s.todayRefreshStore = false
			s.todayRefreshMu.Unlock()
			if brief, ok, changed := s.refreshToday(!storePass); changed && ok && s.app != nil {
				s.saveFinishedRead(brief)
				s.app.emitBrowserEvent("brief:stored", "")
			}
			s.todayRefreshMu.Lock()
			if !s.todayRefreshAgain {
				s.todayRefreshing = false
				s.todayRefreshMu.Unlock()
				return
			}
			s.todayRefreshAgain = false
			if wait := s.todayRefreshWaitLocked(time.Now()); wait > 0 {
				s.todayRefreshing = false
				s.queueTodayRefreshLocked(wait)
				s.todayRefreshMu.Unlock()
				return
			}
			s.todayRefreshAt = time.Now()
			s.todayRefreshMu.Unlock()
		}
	}()
}

// todayRefreshWaitLocked is how long a new live refresh must wait for the gap
// after the last one to pass. todayRefreshMu must be held.
func (s *mobileBriefService) todayRefreshWaitLocked(now time.Time) time.Duration {
	if s.todayRefreshAt.IsZero() {
		return 0
	}
	return todayRefreshGap - now.Sub(s.todayRefreshAt)
}

// queueTodayRefreshLocked runs one refresh after wait, unless one is already
// queued. todayRefreshMu must be held.
func (s *mobileBriefService) queueTodayRefreshLocked(wait time.Duration) {
	if s.todayRefreshQueued {
		return
	}
	s.todayRefreshQueued = true
	time.AfterFunc(wait, func() {
		s.todayRefreshMu.Lock()
		s.todayRefreshQueued = false
		s.todayRefreshMu.Unlock()
		// A pending store request is already recorded in todayRefreshStore.
		s.requestTodayRefresh(false)
	})
}

// todayRefreshDue reports whether a read of Today should start a live refresh:
// none has run yet, or the last one is older than the resync budget.
func (s *mobileBriefService) todayRefreshDue(now time.Time) bool {
	s.todayRefreshMu.Lock()
	defer s.todayRefreshMu.Unlock()
	return s.todayRefreshAt.IsZero() || now.Sub(s.todayRefreshAt) >= todayRefreshBudget
}

// mobileBriefPrivacyRefused reports whether a delivery gate withheld stored words
// for a privacy reason. Closed Eyes and an unreadable mail index are transient and
// are not privacy refusals of a source.
func mobileBriefPrivacyRefused(w mobileBriefWithholding) bool {
	switch w.Reason {
	case mobileBriefWithheldSource, mobileBriefWithheldRedacted, mobileBriefWithheldMailEcho:
		return true
	default:
		return false
	}
}

// sealedCopyRefused reports whether the privacy gates now refuse words that the
// sealed copy of brief holds (a mail subject echoed in the text, a withdrawn
// source). It runs the gates the seal runs and saves nothing.
func (s *mobileBriefService) sealedCopyRefused(brief MobileBriefV1) bool {
	saved, found, err := s.store.loadFinishedRead(brief.BriefID)
	if err != nil || !found || saved.Schema != mobileBriefSealedSchema {
		return false
	}
	var copied MobileBriefV1
	if err := unmarshalStoredMobileBrief(saved.Brief, &copied); err != nil {
		return true
	}
	ctx := s.app.mobileBriefSummaryProjection(context.Background(), brief, saved.Fingerprints)
	projected, withheld := s.app.mobileBriefForProjectionWithholding(ctx, brief, true)
	if mobileBriefPrivacyRefused(withheld) {
		return true
	}
	// The gates also drop held items without a reason (a purpose whose title is
	// now a mail subject). The copy differs from what the gates give now.
	now := s.app.mobileBriefWithCards(projected)
	return !mobileBriefSameWords(now, copied)
}

// mobileBriefSameWords reports whether two briefs show the same written words,
// offers and cards.
func mobileBriefSameWords(a, b MobileBriefV1) bool {
	left, errA := json.Marshal(struct {
		Narrative []MobileBriefPara
		Offers    []MobileBriefOffer
		Cards     []MobileBriefCard
	}{a.Narrative, a.Offers, a.Cards})
	right, errB := json.Marshal(struct {
		Narrative []MobileBriefPara
		Offers    []MobileBriefOffer
		Cards     []MobileBriefCard
	}{b.Narrative, b.Offers, b.Cards})
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

// hasSealedCopy reports whether a brief already has a current-schema sealed copy.
func (s *mobileBriefService) hasSealedCopy(briefID string) bool {
	saved, found, err := s.store.loadFinishedRead(briefID)
	return err == nil && found && saved.Schema == mobileBriefSealedSchema
}

// refreshToday reads Today's live signals into the stored brief and reports
// whether the stored copy changed. liveOnly is a mail sync's pass: a stored brief
// with a sealed copy is not stored or sealed again, and a withdrawn or protected
// source it finds asks for the copy to be amended.
func (s *mobileBriefService) refreshToday(liveOnly bool) (MobileBriefV1, bool, bool) {
	if s.app == nil {
		brief, ok := s.store.current()
		return brief, ok, false
	}
	// Order live projections without blocking audio, finds or generation while
	// source stores (including the scheduler's initial load) are being read.
	s.todayMu.Lock()
	defer s.todayMu.Unlock()
	now, loc := s.now(), s.location
	if loc == nil {
		loc = time.Local
	}
	in := mobileBriefTodayRefreshInput(s.app, now, loc)
	collectMobileBriefToday(s.app, &in)
	collectMobileBriefTodayPurposes(s.app, &in)
	s.patchMu.Lock()
	defer s.patchMu.Unlock()
	// Read after collection: a generated recap or audio patch may have arrived.
	// The current brief is what Today shows. An earlier day's brief stays
	// current until today's arrives, so its live signals wait for it.
	latest, found := s.store.current()
	day := now.In(loc).Format("2006-01-02")
	if found && latest.LocalDate != day {
		return latest, true, false
	}
	scheduled := found
	base := latest
	if !scheduled {
		base, _ = s.store.currentToday()
		if base.LocalDate != day {
			local := now.In(loc)
			start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
			base = buildMobileBriefV1(mobileBriefInput{Slot: mobileBriefSlotOnRequest, AsOf: now, WindowStart: start, Location: loc})
		}
	}
	collectMobileBriefTodayShape(s.app, &in, base)
	readable, withheld := mobileBriefTodayReadableSynthesis(s.app, base)
	updated := projectMobileBriefToday(readable, in)
	if !scheduled {
		// The snapshot is the day so far: it is named by the schedule's slot
		// at this moment (06:10 is still the morning), never by the hour it
		// was first written.
		updated.DisplaySlot = mobileBriefDisplaySlot(MobileBriefV1{Slot: mobileBriefSlotOnRequest, AsOf: formatMobileBriefTime(now), Timezone: updated.Timezone}, s.config.load())
	}
	// JSON is the persisted contract: omitempty normalizes nil and empty
	// slices/maps. In-memory equality otherwise rewrites files on every read.
	before, _ := json.Marshal(base)
	after, _ := json.Marshal(updated)
	changed := !bytes.Equal(before, after)
	if changed && base.NarrativeStatus == "ok" && updated.NarrativeStatus != "ok" {
		// The text the model wrote is shown no longer. Say why, once.
		if withheld.Reason == "" {
			withheld.Reason = mobileBriefWithheldDependencies
		}
		if logged := base.BriefID + "|" + withheld.Reason + "|" + withheld.SourceKind; logged != s.withheldLogged && withheld.Reason != mobileBriefWithheldClosedEyes {
			s.withheldLogged = logged
			log.Printf("[mobile-brief] the written %s brief is withheld on read: reason=%s kind=%s kept=%t", base.Slot, withheld.Reason, withheld.SourceKind, withheld.Transient())
		}
	}
	// A reason that passes (Closed Eyes, a store not open yet at launch, a
	// mail index that cannot be read) hides the text while it lasts, the
	// model's text and Isoform's template alike: keep the stored text for the
	// read after it. A Today read never saves a withdrawn delivery projection
	// over the stored brief.
	if changed && withheld.Transient() {
		changed = false
	}
	if !scheduled && mobileBriefIsEmpty(updated) {
		// Do not leave yesterday's or removed work in the current snapshot.
		if _, exists := s.store.currentToday(); exists && changed {
			if err := s.store.saveCurrentToday(updated); err != nil {
				log.Printf("[mobile-brief] current Today snapshot could not be cleared: %v", err)
				return latest, found, false
			}
			return latest, found, true
		}
		return latest, found, false
	}
	// A live pass that finds a privacy gate refusing the stored words (a withdrawn
	// or protected source, a mail subject echoed in the text) amends the copy: the
	// store below seals it again through the gates. Any other live pass leaves a
	// sealed copy as it is.
	if liveOnly && scheduled && s.hasSealedCopy(base.BriefID) {
		if mobileBriefPrivacyRefused(withheld) || s.sealedCopyRefused(base) {
			changed = true
		} else {
			return updated, true, false
		}
	}
	if changed {
		var err error
		if scheduled {
			err = s.store.save(updated, now)
		} else {
			err = s.store.saveCurrentToday(updated)
		}
		if err != nil {
			log.Printf("[mobile-brief] current Today signals could not be stored: %v", err)
			return latest, found, false
		}
	}
	if changed {
		s.app.noteTodayDayShapeUpdate()
	}
	return updated, true, changed
}

// Refresh only the sources folded from Today. Unrelated sections and the
// generated recap keep their content. Context marks records owned by this
// projection, so deleted insights and completed work leave it after a successful
// source read. Unavailable sources retain their previous rows and context,
// except sources whose privacy must be checked again, including Purposes.
func refreshMobileBriefToday(app *App, base MobileBriefV1, now time.Time, loc *time.Location) MobileBriefV1 {
	in := mobileBriefTodayRefreshInput(app, now, loc)
	in.Slot, in.WindowStart, in.HubName = base.Slot, mobileBriefParseTime(base.WindowStart), base.HubName
	in.DayShape, in.ColdStart = base.DayShape, base.ColdStart
	collectMobileBriefToday(app, &in)
	collectMobileBriefTodayPurposes(app, &in)
	collectMobileBriefTodayShape(app, &in, base)
	readable, _ := mobileBriefTodayReadableSynthesis(app, base)
	return projectMobileBriefToday(readable, in)
}

// mobileBriefTodayRefreshInput starts a Today refresh the way a build starts
// (collectMobileBriefInput): the reader's zone and the UI's language and clock,
// so a recomputed line reads in the same language as the rest of the brief.
func mobileBriefTodayRefreshInput(app *App, now time.Time, loc *time.Location) mobileBriefInput {
	in := mobileBriefInput{AsOf: now, Location: loc}
	if app != nil {
		name := app.currentUILanguageName()
		in.ClockLayout = mobileBriefClockLayout(name)
		in.Language = mobileBriefVoiceLang(name)
	}
	return in
}

// collectMobileBriefTodayShape recomputes the shape of today when Today is
// refreshed. The stored line names only time ahead of when it was built; by
// the afternoon its morning clocks have passed. A brief that shows the shape
// gets a fresh line from the stored calendar, or none once nothing is left
// ahead or the calendar cannot be read.
func collectMobileBriefTodayShape(app *App, in *mobileBriefInput, base MobileBriefV1) {
	shown := false
	for _, item := range base.Sections.Meetings {
		shown = shown || item.Kind == "day_shape"
	}
	if !shown {
		return
	}
	in.DayShapeRefreshed = true
	loc := mobileBriefInputLocation(*in)
	if app == nil || base.LocalDate != in.AsOf.In(loc).Format("2006-01-02") {
		return
	}
	from, to := mobileBriefCalendarWindow(mobileBriefSlotMorning, in.AsOf, loc)
	events, err := app.listCalendarEvents(context.Background(), from, to)
	if err != nil {
		return
	}
	language := in.Language
	if language == "" {
		language = app.currentUILanguageName()
	}
	shape := calendar.ShapeDay(events, in.AsOf, loc, in.AsOf)
	if line := mobileBriefDayShapeLine(shape, in.AsOf, loc, mobileBriefVoiceLang(language)); len(shape.Events) > 0 && line != "" {
		in.Meetings = append(in.Meetings, mobileBriefCandidate{
			SourceKind: mobileBriefDayShapeSource, SourceID: from.Format("2006-01-02"), Title: "The shape of today",
			Detail: line, Status: "day_shape", Kind: "day_shape", At: from,
		})
	}
}

// Check the full stored dependency set before the live projection can remove
// an unavailable source. A paragraph's displayed citations are not all the
// sources consumed by its synthesis, so checking only after projection loses
// the evidence needed to withdraw that synthesis. Keep the local source rows,
// including attendance provenance; this transfers only the derived fields.
func mobileBriefTodayReadableSynthesis(app *App, base MobileBriefV1) (MobileBriefV1, mobileBriefWithholding) {
	readable, withheld := app.mobileBriefDelivery(base)
	quiet := base.NarrativeStatus == "quiet"
	base.Narrative, base.NarrativeStatus = readable.Narrative, readable.NarrativeStatus
	if quiet {
		base.NarrativeStatus = "quiet"
	}
	base.Offers = readable.Offers
	base.DayShape, base.Counts = readable.DayShape, readable.Counts
	// A recording bound to its text stays stored while the brief as read
	// still says what it speaks, even when this read hides it for another
	// reason (a live row leaving a template brief): the next read plays it
	// again. One whose text changed or was withdrawn leaves.
	if stored := base.Audio; stored != nil && stored.binding != "" {
		readable.Audio = stored
		base.Audio = mobileBriefAudioForRead(readable)
	} else {
		base.Audio = readable.Audio
	}
	// A recorded call stays silent (#62): delivery has left out what cites
	// it, so it is no longer a dependency of the text that remains. Keeping it
	// would read as a dropped dependency below and withdraw the whole brief.
	evidence := make([]MobileBriefRef, 0, len(base.Evidence))
	for _, ref := range base.Evidence {
		if !app.mobileBriefRecordingAnnouncement(ref) {
			evidence = append(evidence, ref)
		}
	}
	base.Evidence = evidence
	return base, withheld
}

func collectMobileBriefTodayPurposes(app *App, in *mobileBriefInput) {
	// An unavailable ledger must not restore previously visible Purpose text.
	in.PurposesRefreshed = true
	collectMobileBriefPurposes(app, in, mobileBriefRefreshPurposeDetails(app), nil)
}

// The projection rebuilds every section, so the Purpose Map is read again with
// the same privacy gates as a scheduled brief. Leaving it out would erase the
// goals, commitments and waiting-on-you rows from Today and the phone brief the
// first time either is opened. A Purpose that is no longer current leaves the
// brief here too. A test replaces the reader.
var mobileBriefRefreshPurposeDetails = func(app *App) []PurposeOpenLoopDetail {
	if app == nil || app.activity == nil || app.activity.store == nil {
		return nil
	}
	store, err := app.purposeProductStore()
	if err != nil {
		return nil
	}
	details, err := store.ListDetailsAll()
	if err != nil {
		log.Printf("[mobile-brief] purposes unavailable for the Today refresh: %v", err)
		return nil
	}
	return details
}

func projectMobileBriefToday(base MobileBriefV1, in mobileBriefInput) MobileBriefV1 {
	now := in.AsOf
	in.Slot, in.WindowStart = base.Slot, mobileBriefParseTime(base.WindowStart)
	in.HubName, in.DayShape, in.ColdStart = base.HubName, base.DayShape, base.ColdStart
	oldRefs := map[string]MobileBriefRef{}
	for _, ref := range base.Evidence {
		oldRefs[ref.Ref] = ref
	}
	// Attendance shares the rebuilt evidence budget and chief sections. Retain
	// its source identity before allocation, then remap the stored row below;
	// carrying only its old ref would let a live task take the meeting's citation.
	retainedAttendance := map[string]MobileBriefAttendance{}
	attendanceIDs := map[string]bool{}
	for _, observation := range in.Attendance {
		attendanceIDs[observation.EventID] = true
	}
	for _, attendance := range base.MeetingAttendance {
		if attendanceIDs[attendance.EventID] {
			continue
		}
		for _, id := range attendance.Refs {
			ref, ok := oldRefs[id]
			if !ok || ref.SourceKind != mobileBriefAttendanceSource || ref.SourceID != attendance.EventID {
				continue
			}
			in.Attendance = append(in.Attendance, mobileBriefAttendanceObservation{
				EventID: attendance.EventID, Title: attendance.Title,
				State: attendance.State, Reason: attendance.Reason,
				ObservedAt: mobileBriefParseTime(ref.ObservedAt),
			})
			attendanceIDs[attendance.EventID] = true
			retainedAttendance[attendance.EventID] = attendance
			break
		}
	}
	retain := func(items []MobileBriefItem, into *[]mobileBriefCandidate) {
		for _, item := range items {
			if in.PurposesRefreshed && item.PurposeID != "" || in.DayShapeRefreshed && item.Kind == "day_shape" {
				continue
			}
			if item.EvidenceUnavailable && item.PurposeID != "" {
				appendMobileBriefSource(into, mobileBriefCandidate{SourceKind: "purpose", SourceID: item.PurposeID, Title: item.Title, Detail: item.Detail, Kind: item.Kind, Status: item.Status, EvidenceUnavailable: true})
				continue
			}
			if len(item.Refs) == 0 {
				continue
			}
			ref, ok := oldRefs[item.Refs[0]]
			if !ok {
				continue
			}
			// Suggestions are always replaced by GetDayNoticed, even on a failed read.
			if ref.SourceKind == mobileBriefNoticedSource {
				continue
			}
			// Older briefs identify Purpose rows through their primary evidence.
			if in.PurposesRefreshed && ref.SourceKind == "purpose" {
				continue
			}
			// Memory and knowledge rows are gated by their source sessions. When the
			// source cannot be read the gate cannot be run again, so a stored row is
			// never restored for the phone: preserving a row is not authorizing it.
			if in.Today.Unavailable[ref.SourceKind] && (ref.SourceKind == "memory" || ref.SourceKind == "knowledge") {
				// Left out, and counted as left out (the brief's excluded items).
				in.ExcludedSensitiveItems++
				continue
			}
			key := mobileBriefSourceKey(ref.SourceKind, ref.SourceID)
			if _, live := in.Today.Context[key]; live || (item.Context != nil && !in.Today.Unavailable[ref.SourceKind]) {
				continue
			}
			if item.Context != nil {
				in.Today.Context[key] = *item.Context
			}
			// Keep an existing main-chat report marker even if its richer card
			// was capped; rebuilding a legacy row must not revive the reminder.
			candidate := mobileBriefCandidate{Outcome: item.Outcome, SourceKind: ref.SourceKind, SourceID: ref.SourceID, WebSource: ref.WebSource, Title: item.Title, EvidenceLabel: ref.Label, Detail: item.Detail, Status: item.Status, Kind: item.Kind, At: mobileBriefParseTime(item.At), EndAt: mobileBriefParseTime(item.EndAt), URL: item.URL,
				LastTouch: mobileBriefParseTime(item.LastTouch), NextStep: item.NextStep, DueAt: mobileBriefParseTime(item.DueAt), MomentID: item.MomentID, ChatSessionID: item.ChatSessionID, ChatMessageID: item.ChatMessageID,
				OpenedAt: mobileBriefParseTime(item.Since)}
			for _, id := range item.Refs[1:] {
				if evidence, ok := oldRefs[id]; ok {
					candidate.Evidence = append(candidate.Evidence, mobileBriefCandidate{SourceKind: evidence.SourceKind, SourceID: evidence.SourceID, WebSource: evidence.WebSource, Title: evidence.Label, At: mobileBriefParseTime(evidence.ObservedAt)})
				}
			}
			appendMobileBriefSource(into, candidate)
		}
	}
	// A task already in the recap stays in Moved, with its current state.
	for _, item := range base.Sections.Moved {
		if len(item.Refs) == 0 {
			continue
		}
		ref := oldRefs[item.Refs[0]]
		for i, live := range in.OpenLoops {
			if ref.SourceKind == live.SourceKind && ref.SourceID == live.SourceID {
				in.Moved = append(in.Moved, live)
				in.OpenLoops = append(in.OpenLoops[:i], in.OpenLoops[i+1:]...)
				break
			}
		}
	}
	retain(base.Upcoming, &in.Today.Upcoming)
	retain(base.Sections.WorkingToward, &in.WorkingToward)
	retain(base.Sections.Commitments, &in.Commitments)
	retain(base.Sections.WaitingOnYou, &in.WaitingOnYou)
	retain(base.Sections.Moved, &in.Moved)
	retain(base.Sections.Doing, &in.Doing)
	retain(base.Sections.Watching, &in.Watching)
	retain(base.Sections.Meetings, &in.Meetings)
	retain(base.Sections.People, &in.People)
	retain(base.Sections.OpenLoops, &in.OpenLoops)
	retain(base.Sections.Learned, &in.Learned)
	retain(base.Sections.Deliverables, &in.Deliverables)
	retain(base.Sections.Schedules, &in.Schedules)
	retain(base.Sections.Noticed, &in.Noticed)
	// Reserve one citation per paragraph first, then its other citations.
	// Upcoming actions and current failures still get the first evidence slots.
	for _, para := range base.Narrative {
		for _, id := range para.Refs {
			if ref, ok := oldRefs[id]; ok {
				in.RecapEvidence = append(in.RecapEvidence, ref)
				break
			}
		}
	}
	for _, para := range base.Narrative {
		for _, id := range para.Refs {
			if ref, ok := oldRefs[id]; ok {
				in.RecapEvidence = append(in.RecapEvidence, ref)
			}
		}
	}
	// The model read more than its text cites (its binding): keep those
	// inputs in the evidence as well, so every later read still checks them.
	var inputs map[string]bool
	if base.NarrativeStatus == "ok" {
		inputs = mobileBriefModelInputs(base)
	}
	for _, ref := range base.Evidence {
		if inputs[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] {
			in.RecapEvidence = append(in.RecapEvidence, ref)
		}
	}
	fresh := buildMobileBriefV1(in)
	for i, attendance := range fresh.MeetingAttendance {
		if stored, ok := retainedAttendance[attendance.EventID]; ok {
			// Preserve the stored attendance and its local provenance; only the
			// evidence ref changes. Delivery still applies current read policy.
			stored.Refs = attendance.Refs
			fresh.MeetingAttendance[i] = stored
		}
	}
	// Rebuilding evidence can renumber refs. Keep the recap's citations tied
	// to their source records, including records no longer in a live section.
	builder := &mobileBriefBuilder{asOf: now, evidence: fresh.Evidence, index: map[string]string{}}
	for _, ref := range fresh.Evidence {
		builder.index[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] = ref.Ref
	}
	// Isoform's template (no model text) names exactly the cards each
	// paragraph cites: a paragraph stays while every one of them is cited, so
	// later reads can still withdraw it. See mobileBriefForDelivery.
	template := base.NarrativeStatus != "ok"
	narrative := []MobileBriefPara{}
	for _, para := range base.Narrative {
		refs := []string{}
		complete := true
		for _, id := range para.Refs {
			old, ok := oldRefs[id]
			if !ok {
				complete = false
				continue
			}
			ref, ok := builder.refFor(mobileBriefCandidate{SourceKind: old.SourceKind, SourceID: old.SourceID, WebSource: old.WebSource, Title: old.Label, At: mobileBriefParseTime(old.ObservedAt)})
			if ok {
				refs = append(refs, ref)
			} else {
				complete = false
			}
		}
		if len(refs) > 0 && (complete || !template) {
			narrative = append(narrative, MobileBriefPara{Text: para.Text, Refs: refs, Hold: para.Hold})
		}
	}
	// Counts for the recap describe all known records, including rows beyond
	// section caps. Only open one-time work belongs to the live projection.
	counts := map[string]int{}
	for kind, count := range base.Counts.OpenLoopsByKind {
		if kind != "one_time_work" {
			counts[kind] = count
		}
	}
	if count := fresh.Counts.OpenLoopsByKind["one_time_work"]; count > 0 {
		counts["one_time_work"] = count
	}
	base.Counts.OpenLoopsByKind = counts
	// A recap's citations are only a display subset of its inputs. If this
	// projection drops any original dependency, stop replaying all synthesis
	// now, before a later refresh can mistake the smaller set for complete.
	// The dependencies are the model's inputs when the brief records them,
	// and every source otherwise.
	retainedSources := map[string]bool{}
	for _, ref := range builder.evidence {
		retainedSources[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] = true
	}
	for _, ref := range base.Evidence {
		key := mobileBriefSourceKey(ref.SourceKind, ref.SourceID)
		if !template && (inputs == nil || inputs[key]) && !retainedSources[key] {
			narrative = []MobileBriefPara{}
			base.NarrativeStatus = "unavailable"
			base.Audio, base.DayShape = nil, nil
			base.Offers = nil
			base.Counts = fresh.Counts
			break
		}
	}
	if template && len(narrative) < len(base.Narrative) {
		base.Audio = nil // the spoken brief reads every original paragraph
	}
	base.Sections, base.Limits = fresh.Sections, fresh.Limits
	base.Upcoming, base.ColdStart = fresh.Upcoming, fresh.ColdStart
	base.MeetingAttendance = fresh.MeetingAttendance
	base.Evidence, base.Narrative = builder.evidence, narrative
	offers := make([]MobileBriefOffer, 0, len(base.Offers))
	for _, offer := range base.Offers {
		var refs []string
		for _, id := range offer.Refs {
			if old, ok := oldRefs[id]; ok {
				if ref, ok := builder.index[mobileBriefSourceKey(old.SourceKind, old.SourceID)]; ok {
					refs = append(refs, ref)
				}
			}
		}
		// A template's offer, like its paragraphs, stays only while its
		// subject is still cited.
		if template && len(refs) < len(offer.Refs) {
			continue
		}
		offer.Refs = refs
		offers = append(offers, offer)
	}
	if base.Offers != nil {
		base.Offers = offers
	}
	// Stored cards follow their sources to the rebuilt refs; a card whose
	// source the refresh no longer cites leaves, and readers write it again.
	cards := make([]MobileBriefCard, 0, len(base.Cards))
	for _, card := range base.Cards {
		var refs []string
		for _, id := range card.Refs {
			if old, ok := oldRefs[id]; ok {
				if ref, ok := builder.index[mobileBriefSourceKey(old.SourceKind, old.SourceID)]; ok {
					refs = append(refs, ref)
				}
			}
		}
		if len(refs) == 0 || len(refs) < len(card.Refs) {
			continue
		}
		card.Refs = refs
		cards = append(cards, card)
	}
	if base.Cards != nil {
		base.Cards = cards
	}
	// A brief stored before the top-app list went empty still carries app
	// minutes; Today shows what moved, not time per app.
	if base.DayShape != nil && len(base.DayShape.TopApps) > 0 {
		day := *base.DayShape
		day.TopApps = []MobileBriefAppTime{}
		base.DayShape = &day
	}
	base.ConversationKey = mobileBriefConversationKey(base)
	return base
}

func (s *mobileBriefStore) currentToday() (MobileBriefV1, bool) {
	if s.dir == "" {
		return MobileBriefV1{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.dir, mobileBriefTodayFileName))
	var brief MobileBriefV1
	if err != nil || unmarshalStoredMobileBrief(data, &brief) != nil || !mobileBriefIDPattern.MatchString(brief.BriefID) {
		return MobileBriefV1{}, false
	}
	return brief, true
}

func (s *mobileBriefStore) saveCurrentToday(brief MobileBriefV1) error {
	if s.dir == "" {
		return errMobileBriefStateUnavailable
	}
	data, err := marshalStoredMobileBrief(brief)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return writeMobileBriefFileAtomic(s.dir, mobileBriefTodayFileName, data)
}

// Reserve actionable local records and one citation per recap paragraph before
// Purpose rows. buildMobileBriefV1 allocates remaining citations afterward.
func (b *mobileBriefBuilder) reserveToday(in mobileBriefInput) []MobileBriefItem {
	upcoming := b.upcoming(in.Today)
	if in.Today != nil {
		schedules := prepareMobileBriefCandidates(in.Schedules, mobileBriefNewestFirst)
		for i, item := range schedules {
			if i >= mobileBriefItemsPerSection {
				break
			}
			if _, live := in.Today.Context[mobileBriefSourceKey(item.SourceKind, item.SourceID)]; live && item.Status == "failed" {
				b.refFor(item)
			}
		}
	}
	// projectMobileBriefToday puts each paragraph's first citation at the front.
	b.reserveRecap(in.RecapEvidence[:min(len(in.RecapEvidence), mobileBriefNarrativeMaxParagraphs)])
	return upcoming
}

func (b *mobileBriefBuilder) reserveRecap(refs []MobileBriefRef) {
	for _, ref := range refs {
		b.refFor(mobileBriefCandidate{SourceKind: ref.SourceKind, SourceID: ref.SourceID, WebSource: ref.WebSource, Title: ref.Label, At: mobileBriefParseTime(ref.ObservedAt)})
	}
}
