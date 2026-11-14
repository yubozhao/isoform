package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/openanswer"
	"wails-gui/internal/platform/mailheaders"
)

// Include short subjects with the same word-boundary matching as extraction.
// A mail called "Hi" must not match "ship"; longer title excerpts are held too.
func mobileBriefMailSubjects(a *App) ([]string, error) {
	rows, err := mobileGoalMailRows(a)
	if err != nil {
		return nil, errMobileGoalMailUnavailable
	}
	var subjects []string
	for _, row := range rows {
		subject := purposeMailTextFold(mailheaders.NormalizeSubject(row.Subject))
		if subject != "" {
			subjects = append(subjects, subject)
		}
	}
	// A repaired title may remain in an older stored brief after Mail prunes
	// its header. Local repair metadata keeps that known echo held back too.
	if a != nil && a.activity != nil && a.activity.store != nil {
		repairs, err := loadPurposeMailTitleRepairs(a.activity.store.reader())
		if err != nil {
			return nil, err
		}
		for _, repair := range repairs {
			if subject := purposeMailTextFold(mailheaders.NormalizeSubject(repair.OriginalTitle)); subject != "" {
				subjects = append(subjects, subject)
			}
		}
	}
	return uniqueSortedStrings(subjects), nil
}

// mobileBriefTextEchoesMail shares the Goals holdback's comparison, without
// exempting copied brief text on the strength of a goal's creation channel.
func mobileBriefTextEchoesMail(text string, subjects []string) bool {
	folded := purposeMailTextFold(text)
	if folded == "" {
		return false
	}
	for _, subject := range subjects {
		if purposeMailContains(folded, subject) || (len([]rune(folded)) >= mobileGoalSubjectMinRunes && purposeMailContains(subject, folded)) {
			return true
		}
	}
	return false
}

// Inspect all wire strings, including evidence labels, identifiers and context
// added by Today. Checking only the visible title leaves those routes open.
func mobileBriefValueEchoesMail(value any, subjects []string) bool {
	if len(subjects) == 0 {
		return false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return true
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return true
	}
	var echoes func(any) bool
	echoes = func(value any) bool {
		switch typed := value.(type) {
		case string:
			return mobileBriefTextEchoesMail(typed, subjects)
		case []any:
			for _, item := range typed {
				if echoes(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range typed {
				if echoes(item) {
					return true
				}
			}
		}
		return false
	}
	return echoes(decoded)
}

// mobileBriefForDelivery rechecks current source policy and mail holdback for
// both new and stored documents. A previous projection is never permission to
// send its text after the source becomes protected. This leaves storage intact.
// A goal the user has since marked done or forgotten leaves the read too.
func (a *App) mobileBriefForDelivery(brief MobileBriefV1) MobileBriefV1 {
	return a.mobileBriefForDeliveryContext(context.Background(), brief)
}

func (a *App) mobileBriefForDeliveryContext(ctx context.Context, brief MobileBriefV1) MobileBriefV1 {
	return a.mobileBriefForProjectionContext(ctx, brief, true)
}

// A new brief passes the same privacy checks before its run record exists.
// Its generation metadata must survive until the scheduler records that run.
func (a *App) mobileBriefForGeneration(brief MobileBriefV1) MobileBriefV1 {
	return a.mobileBriefForProjectionContext(context.Background(), brief, false)
}

func (a *App) mobileBriefForProjectionContext(ctx context.Context, brief MobileBriefV1, useRunTiming bool) MobileBriefV1 {
	brief, _ = a.mobileBriefForProjectionWithholding(ctx, brief, useRunTiming)
	return brief
}

// mobileBriefForProjectionWithholding is mobileBriefForProjectionContext with why the
// read withheld the model's text (an empty Reason when it did not). The sealed read
// (mobile_brief_finished_read.go) needs that reason to tell a transient hold from a
// privacy hold.
func (a *App) mobileBriefForProjectionWithholding(ctx context.Context, brief MobileBriefV1, useRunTiming bool) (MobileBriefV1, mobileBriefWithholding) {
	// Audio and feed readers need only the privacy projection. Navigation is
	// added by card/phone readers with their request's existing source snapshot.
	quiet := brief.NarrativeStatus == "quiet"
	brief.WeekQuestion = nil
	brief = mobileBriefWithoutParagraphTopics(brief)
	if a != nil && useRunTiming {
		if service, ok := a.mobileBriefSource().(*mobileBriefService); ok && service != nil && service.runs != nil {
			brief = mobileBriefWithRunTiming(brief, service.runs.list())
		} else {
			brief.Generation = nil
		}
	}
	brief, withheld := a.mobileBriefDeliveryContext(ctx, brief)
	if quiet {
		brief.NarrativeStatus = "quiet"
	}
	brief = a.mobileBriefWithoutClosedPurposes(brief)
	language := a.modelDisplayLanguage()
	closing := mobileBriefClosingParagraph(brief)
	brief = mobileBriefBlankExcluded(brief, a.mobileBriefExclusions())
	brief = mobileBriefHygiene(mobileBriefRewrittenClosing(brief, closing, uiLanguageDisplayName(language)))
	brief = a.mobileBriefCalendarDisplay(ctx, brief)
	brief = mobileBriefWithDisplayTimes(brief, uiLanguageDisplayName(language))
	brief = mobileBriefDisplay(brief, language)
	now := time.Now()
	anchor := mobileBriefAsOfTime(brief)
	zone := mobileBriefNarrativeLocation(brief)
	if a != nil {
		if service, ok := a.mobileBriefSource().(*mobileBriefService); ok && service != nil {
			now = service.now()
			if service.location != nil {
				zone = service.location
			}
			if service.runs != nil {
				if run := todayBriefRecordedRun(service, brief.BriefID); run != nil {
					if ready := mobileBriefParseTime(run.At); !ready.IsZero() {
						anchor = ready
					}
				}
			}
		}
	}
	brief = mobileBriefRelativeDaysAtRead(brief, now, anchor, zone, uiLanguageDisplayName(language))
	brief = mobileBriefWithoutHoldOffer(brief)
	brief.WeekQuestion = a.morningBriefWeekQuestion(brief, time.Now())
	brief.ConversationKey = mobileBriefConversationKey(brief)
	brief.Audio = mobileBriefAudioForRead(brief)
	return brief, withheld
}

func mobileBriefWeeklyRecapMatches(left, right []MobileBriefPara) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !reflect.DeepEqual(left[i].Hold, right[i].Hold) ||
			left[i].TopicID != right[i].TopicID ||
			left[i].TopicName != right[i].TopicName ||
			left[i].Text != right[i].Text ||
			!reflect.DeepEqual(left[i].Refs, right[i].Refs) {
			return false
		}
	}
	return true
}

// Why a read withheld the model's text, for the log and for whether the
// withholding may be saved. Names only, never content.
const (
	mobileBriefWithheldClosedEyes   = "closed_eyes"
	mobileBriefWithheldMail         = "mail_unreadable"
	mobileBriefWithheldNotReady     = "store_unavailable"
	mobileBriefWithheldSource       = "source_withdrawn"
	mobileBriefWithheldRedacted     = "redacted"
	mobileBriefWithheldMailEcho     = "mail_echo"
	mobileBriefWithheldRecording    = "recording_announcement"
	mobileBriefWithheldDependencies = "dependency_dropped"
)

// mobileBriefWithholding is the first reason a read withheld the model's
// text, and the kind of source behind it when there is one.
type mobileBriefWithholding struct {
	Reason     string
	SourceKind string
}

// Transient reports a reason that says nothing about the sources themselves:
// Closed Eyes, or a store that cannot be read yet (at launch, before the
// activity store or the app database is open). The text is hidden while it
// lasts and kept on disk for when it clears.
func (w mobileBriefWithholding) Transient() bool {
	switch w.Reason {
	case mobileBriefWithheldClosedEyes, mobileBriefWithheldMail, mobileBriefWithheldNotReady:
		return true
	default:
		return false
	}
}

// mobileBriefModelInputs are the sources the brief's model read, cited or
// not (its binding, mobile_brief_model_binding.go), by source key; nil when
// the brief has no such record, so that every source counts as an input.
func mobileBriefModelInputs(brief MobileBriefV1) map[string]bool {
	binding := brief.modelBinding
	if !mobileBriefBindingHasSources(brief, binding) || !mobileBriefRankedManifestValid(brief, binding) || !mobileBriefRankedLedgerValid(brief, binding) {
		return nil
	}
	inputs := make(map[string]bool, len(binding.Sources)+len(binding.RankedInputs))
	for _, source := range binding.Sources {
		inputs[mobileBriefSourceKey(source.Evidence.SourceKind, source.Evidence.SourceID)] = true
	}
	if brief.rankedEdition {
		for _, item := range binding.RankedInputs {
			inputs[mobileBriefSourceKey(item.Kind, item.ID)] = true
			for _, dependency := range item.Dependencies {
				inputs[mobileBriefSourceKey(dependency.Kind, dependency.ID)] = true
			}
		}
	}
	return inputs
}

// mobileBriefDelivery is mobileBriefForDelivery, with why it withheld the
// model's text (an empty Reason when it did not).
func (a *App) mobileBriefDelivery(brief MobileBriefV1) (MobileBriefV1, mobileBriefWithholding) {
	return a.mobileBriefDeliveryContext(context.Background(), brief)
}

// mobileBriefReadyBackoff is how long a read waits for a store that is still
// opening before it withholds. A launch read can run before the mail, purpose or
// activity store is ready (R64, 2026-11-05: withheld at 09:59:42, 3 min after the
// start). A store that never opens is still withheld, after these waits.
var mobileBriefReadyBackoff = []time.Duration{300 * time.Millisecond, 600 * time.Millisecond}

// mobileBriefGateHold, when set by a test, says the stores are still opening: the
// gate then withholds the text as store_unavailable. Nil in the app.
var mobileBriefGateHold func() bool

// mobileBriefRetryNotReady runs the gate again while it withholds only because a
// store is not ready, waiting backoff[i] before attempt i+1. Any other outcome, or a
// cancelled context, returns at once. Privacy reasons are never retried.
func mobileBriefRetryNotReady(ctx context.Context, backoff []time.Duration, once func() (MobileBriefV1, mobileBriefWithholding)) (MobileBriefV1, mobileBriefWithholding) {
	brief, withheld := once()
	for _, wait := range backoff {
		if withheld.Reason != mobileBriefWithheldNotReady {
			break
		}
		select {
		case <-ctx.Done():
			return brief, withheld
		case <-time.After(wait):
		}
		brief, withheld = once()
	}
	return brief, withheld
}

// mobileBriefDeliveryContext applies the read-time gates, waiting briefly for a
// store that is still opening (see mobileBriefRetryNotReady).
func (a *App) mobileBriefDeliveryContext(ctx context.Context, brief MobileBriefV1) (MobileBriefV1, mobileBriefWithholding) {
	return mobileBriefRetryNotReady(ctx, mobileBriefReadyBackoff, func() (MobileBriefV1, mobileBriefWithholding) {
		return a.mobileBriefDeliveryOnce(ctx, brief)
	})
}

// mobileBriefDeliveryOnce applies the read-time gates once.
func (a *App) mobileBriefDeliveryOnce(ctx context.Context, brief MobileBriefV1) (MobileBriefV1, mobileBriefWithholding) {
	mobileBriefGateRuns.Add(1)
	if mobileBriefGateHold != nil && mobileBriefGateHold() {
		// Tests stand in for a store that is still opening.
		brief.Narrative = nil
		return brief, mobileBriefWithholding{Reason: mobileBriefWithheldNotReady}
	}
	staticEmpty := mobileBriefStaticEmpty(brief)
	subjects, mailErr := mobileBriefMailSubjects(a)
	// One read checks its refs as a batch: the work records are listed once,
	// the read policy is loaded once, and each activity session the refs
	// depend on is checked once, however many refs share it.
	listed := withMainChatReadMemo(context.Background())
	gate := newPeopleReadGateCache()
	dependencies := map[string]bool{}
	dependencyAllowed := func(ctx context.Context, id string, apps []string) bool {
		key := strings.Join(append([]string{strconv.FormatBool(pausedBriefReadAllowed(ctx)), id}, apps...), "\x00")
		if allowed, seen := dependencies[key]; seen {
			return allowed
		}
		allowed := a.activityDependencyAllowsModelReadWithCacheContext(ctx, id, apps, gate)
		dependencies[key] = allowed
		return allowed
	}
	allowMemory, memoryErr := a.memoryReadFilterWithDependencyContext(ctx, func(id string, apps []string) bool { return dependencyAllowed(ctx, id, apps) })
	closed := a == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx)
	pauses, pauseErr := a.observationPauses()
	reader := mobileBriefPurposeReader{app: a, asOf: time.Now(), pauses: pauses, pausesReadable: pauseErr == nil, cache: map[string]mobileBriefPurposeObservation{}}
	if a != nil && a.activity != nil {
		reader.store = a.activity.store
	}
	// Purpose provenance may use a version-qualified memory identity. Apply
	// the same full-content read policy as ordinary recall, including current
	// corrections and the source envelope that predates this stored brief.
	memoryAllowed := func(kind, id string) bool {
		if memoryErr != nil {
			return false
		}
		baseID, version, _ := strings.Cut(id, ":")
		var content string
		var err error
		switch kind {
		case "memory", taskContextSourceMemoryKind:
			var entry MemoryEntry
			entry, err = a.GetMemoryEntry(baseID)
			content = entry.Content
		case "knowledge", taskContextSourceKnowledgeKind:
			var entry KnowledgeEntry
			entry, err = a.GetKnowledgeEntry(baseID)
			content = entry.Content
		}
		if doc, ok := parseOKFDocument(content); ok && okfString(doc.Frontmatter, "source") == meetingKnowledgeSource {
			return false
		}
		// A historical citation cannot bypass restrictions added to the live
		// source after that version was recorded. Both versions must be readable.
		if err != nil || !allowMemory(baseID, content) {
			return false
		}
		if strings.HasPrefix(version, "history:") {
			stamp, parseErr := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(version, "history:"), ".md"), 10, 64)
			if parseErr != nil {
				return false
			}
			switch kind {
			case "memory", taskContextSourceMemoryKind:
				content, err = a.GetMemoryVersion(baseID, stamp)
			case "knowledge", taskContextSourceKnowledgeKind:
				content, err = a.GetKnowledgeVersion(baseID, stamp)
			}
		}
		return err == nil && allowMemory(baseID, content)
	}
	allowedSources := map[string]bool{}
	purposeDetails := map[string]PurposeOpenLoopDetail{}
	missingPurposeEvidence := map[string]bool{}
	purposesLoaded := false
	loadPurposes := func() {
		if purposesLoaded {
			return
		}
		purposesLoaded = true
		if store, err := a.purposeProductStore(); err == nil {
			if details, err := store.ListDetailsAll(); err == nil {
				for _, detail := range details {
					purposeDetails[detail.OpenLoop.ID] = detail
				}
			}
		}
	}
	// Words the user wrote for a goal are theirs (item 66 C3): its title on
	// its card and on its citation is not a mail echo. A card's title that
	// is anything else (a person's name on a waiting card), the card's other
	// text and the brief's written text are still checked.
	purposeUserWrote := func(id, text string) bool {
		if id == "" || text == "" || closed || mailErr != nil || reader.store == nil {
			return false
		}
		loadPurposes()
		detail, exists := purposeDetails[id]
		return exists && mobileGoalUserWords(detail)[mobileGoalFold(text)]
	}
	purposeAllowed := func(id string) bool {
		key := mobileBriefSourceKey("purpose", id)
		if allowed, found := allowedSources[key]; found {
			return allowed
		}
		allowed := false
		if !closed && mailErr == nil && reader.store != nil {
			loadPurposes()
			if detail, exists := purposeDetails[id]; exists {
				observation := reader.purpose(detail)
				missingPurposeEvidence[id] = observation.MissingEvidence
				allowed = !observation.Sensitive && !observation.Unavailable &&
					!mobileBriefValueEchoesMail(mobileGoalForeignText(detail), subjects)
				for _, evidence := range detail.Evidence {
					switch evidence.SourceKind {
					case "memory", "knowledge", taskContextSourceMemoryKind, taskContextSourceKnowledgeKind:
						allowed = allowed && memoryAllowed(evidence.SourceKind, evidence.SourceID)
					case taskContextSourceSelfKind:
						entry, err := reader.store.GetSelfModelEntryByID(evidence.SourceID)
						allowed = allowed && err == nil && a.selfModelEntryAllowsModelRead(entry)
					}
				}
				for _, sessionID := range observation.SessionIDs {
					// Missing raw evidence permits only the canonical fallback.
					// Existing evidence must pass the captured-source policy too.
					privacy := reader.sessionPrivacy([]string{sessionID})
					if !privacy.MissingEvidence {
						allowed = allowed && dependencyAllowed(ctx, sessionID, nil)
					}
				}
			}
		}
		allowedSources[key] = allowed
		return allowed
	}
	// These identities stay local. A checked empty dependency list supports
	// an unknown attendance observation; nil is an unchecked legacy record.
	readableAttendance := map[string]bool{}
	kept, _ := ctx.Value(mobileBriefKeptAttendanceKey{}).(map[string]bool)
	for _, row := range brief.MeetingAttendance {
		allowed := !closed && mailErr == nil && row.sourceSessionIDs != nil && !mobileBriefValueEchoesMail(row, subjects)
		for _, sessionID := range row.sourceSessionIDs {
			if allowed && kept[row.EventID] && reader.sessionRemovedByRetention(sessionID) {
				// Seal v2: the copy kept this row and retention removed its
				// session since; that is not a privacy action, so it stays.
				continue
			}
			allowed = allowed && dependencyAllowed(context.Background(), sessionID, nil)
		}
		if previous, exists := readableAttendance[row.EventID]; exists {
			allowed = allowed && previous
		}
		readableAttendance[row.EventID] = allowed
	}
	var deliverables map[string]bool
	deliverableAllowed := func(id string) bool {
		if deliverables == nil {
			deliverables = map[string]bool{}
			if items, err := a.ListDeliverables(); err == nil {
				allowed := a.deliverableFolderReadFilter(context.Background())
				for _, item := range items {
					deliverables[item.ID] = allowed(item)
				}
			}
		}
		return deliverables[id]
	}
	readable := map[string]bool{}
	purposeRefs := map[string]string{}
	refs := make([]MobileBriefRef, 0, len(brief.Evidence))
	calendarRefs := map[string]bool{}
	// Recording announcements are silenced, not withdrawn for privacy: a
	// recorded call's session or notes (silenced) and a calendar card's
	// "notes ready" (quietProse). The cards citing them leave without being
	// counted as sensitive. A template's paragraphs and offers citing them
	// leave on their own; the model's text leaves whole (see below).
	silenced := map[string]bool{}
	quietProse := map[string]bool{}
	// A withdrawn input removes every card and paragraph that cites it, never
	// the whole brief while anything else is left (hub decision, 2026-11-04).
	withdrawnInputs := map[string]bool{}
	// goneInputs are cited sources the person removed (a calendar event deleted
	// in place). They are not privacy withdrawals: a paragraph citing one keeps
	// its words (seal v2, rule 2). An event missing from the store altogether is
	// still unreadable evidence (walkthrough v1.1.6).
	goneInputs := map[string]bool{}
	narrativeDropped := false
	trimmed := false
	// Without the model's text, the written brief is Isoform's template: each
	// paragraph and offer names only the cards it cites, so one whose cards
	// can no longer be read, or are no longer delivered as written, leaves on
	// its own. Closed Eyes hides every card this way too; it never withdraws a
	// template whole as if the model had written it. The model's text ("ok")
	// may draw on more than it cites, so any withdrawn input still withdraws
	// all of it, as do Closed Eyes and a mail read that cannot rule out a
	// private subject.
	template := !brief.rankedEdition && brief.NarrativeStatus != "ok" && mailErr == nil
	altered := map[string]bool{}
	alteredPurposes := map[string]bool{}
	changed := closed || mailErr != nil
	// The model's text depends only on what its model read (its binding): a
	// source outside that set, such as a learned note or a news item the text
	// never drew on, leaves on its own and the text stays. A brief without
	// the record counts every source as an input, as before.
	inputs := mobileBriefModelInputs(brief)
	evidenceByRef := mobileBriefEvidenceByRef(brief)
	isInput := func(ref MobileBriefRef) bool {
		return inputs == nil || inputs[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)]
	}
	citesInput := func(ids []string, purposeID string) bool {
		if inputs == nil || purposeID != "" && inputs[mobileBriefSourceKey("purpose", purposeID)] {
			return true
		}
		for _, id := range ids {
			if ref, ok := evidenceByRef[id]; !ok || isInput(ref) {
				return true
			}
		}
		return false
	}
	// A store that cannot be read yet withdraws nothing for good.
	notReady := reader.store == nil || memoryErr != nil || pauseErr != nil || a == nil || a.store == nil
	var withheld mobileBriefWithholding
	textChanged := false
	withhold := func(reason, kind string) {
		textChanged = true
		if reason == mobileBriefWithheldSource && notReady {
			reason = mobileBriefWithheldNotReady
		}
		if withheld.Reason == "" {
			withheld = mobileBriefWithholding{Reason: reason, SourceKind: kind}
		}
	}
	switch true {
	case closed:
		withhold(mobileBriefWithheldClosedEyes, "")
	case mailErr != nil:
		withhold(mobileBriefWithheldMail, "")
	}
	noticed := mobileBriefNoticedFor(ctx, a)
	for _, ref := range brief.Evidence {
		calendarRefs[ref.Ref] = ref.SourceKind == mobileBriefCalendarSource
		checked := ref
		if ref.SourceKind == "purpose" && purposeUserWrote(ref.SourceID, ref.Label) {
			checked.Label = ""
		}
		allowed := !closed && !mobileBriefValueEchoesMail(checked, subjects)
		if allowed {
			switch ref.SourceKind {
			case mobileBriefNoticedSource:
				_, allowed = noticed.suggestion(ref.SourceID)
			case "history":
				// Completion removes a live card, not its synthesis dependency.
				// Legacy unresolvable history cannot establish source authority.
				allowed = a.mobileBriefHandoffSourceReadable(listed, ref.SourceID)
			case mobileBriefWaitingReplySource:
				_, allowed = a.mobileBriefWaitingReply(ctx, ref.SourceID, time.Now())
			case "deliverable":
				allowed = deliverableAllowed(ref.SourceID)
			case mobileBriefWebSource:
				allowed = a.mobileBriefWebSourceAllowsRead(ref.WebSource)
			case mobileBriefFileSource:
				// A document can now support prose or a meeting tie. Recheck
				// its current Today listing before retaining either projection.
				current, ok := a.currentTodayBriefCandidate(ctx, ref, brief, mobileBriefAsOfTime(brief))
				allowed = ok && mobileBriefWithoutClockTimes(current.Title) == ref.Label
			case "chat_message":
				if a.store == nil {
					allowed = false
					break
				}
				answer, isPost, postKind, err := a.store.OpenAnswerForMessageWithKind(context.Background(), ref.SourceID)
				switch err == nil {
				case false:
					if !errors.Is(err, sql.ErrNoRows) {
						allowed = false
					}
				case true:
					switch isPost {
					case false:
					case true:
						allowed = false
						switch postKind {
						case "interim", "settled":
							message, err := a.store.LoadStreamItem(context.Background(), ref.SourceID)
							candidate := openanswer.Candidate{Question: answer.Question, Topic: answer.Topic, Source: answer.Source}
							allowed = err == nil && message != nil && message.Kind == "assistant" &&
								a.openAnswerBriefPostAllowed(context.Background(), candidate, message.Content)
						}
					}
				}
			case "purpose":
				purposeRefs[ref.Ref] = ref.SourceID
				allowed = purposeAllowed(ref.SourceID)
			case "memory", "knowledge", taskContextSourceMemoryKind, taskContextSourceKnowledgeKind:
				if a.mobileBriefRecordingAnnouncement(ref) {
					allowed, silenced[ref.Ref] = false, true
					break
				}
				allowed = memoryAllowed(ref.SourceKind, ref.SourceID)
			case mobileBriefCalendarSource:
				// A retained paragraph can cite a subscribed notice or a
				// personal event already underway. Recheck the occurrence as
				// it was classified at this brief's time before showing it.
				if a.store == nil {
					allowed = false
					withhold(mobileBriefWithheldNotReady, mobileBriefCalendarSource)
					break
				}
				row, found, err := a.store.GetCalendarEvent(ctx, ref.SourceID)
				if err != nil {
					allowed = false
					withhold(mobileBriefWithheldNotReady, mobileBriefCalendarSource)
					break
				}
				if !found {
					allowed = false
					break
				}
				if row.DeletedAt != 0 {
					// Removed by the person (an edit): not a withdrawal.
					allowed = false
					goneInputs[ref.Ref] = true
					break
				}
				event := calendarEventFromRow(row)
				if mobileBriefUnprovedCalendarNotice(event) || mobileBriefCalendarAlreadyUnderway(event, mobileBriefAsOfTime(brief)) {
					allowed, silenced[ref.Ref] = false, true
				}
			case mobileBriefAttendanceSource:
				allowed = readableAttendance[ref.SourceID]
			case mobileBriefMeetingAppSource:
				allowed, silenced[ref.Ref] = false, a.mobileBriefRecordingAnnouncement(ref) // cached recording announcements stay silent too
			case mobileBriefRecapSource:
				if strings.HasPrefix(ref.SourceID, threadOutcomeMomentPrefix) {
					allowed = mailErr == nil && a.threadOutcomeSourceReadable(context.Background(), ref.SourceID)
					if !allowed && mailErr == nil && a.threadOutcomeSourceEdited(ctx, reader, ref.SourceID) {
						// The summary was written again (the hourly batch, a new
						// run): an edit, not a withdrawal (seal v2 rule 2).
						goneInputs[ref.Ref] = true
					}
					break
				}
				allowed = reader.store != nil
				if allowed {
					id := ref.SourceID
					if replacements, ok := ctx.Value(mobileBriefSummaryReplacementKey{}).(mobileBriefSummaryProjectionSources); ok && replacements.refs[ref.Ref] != "" {
						id = replacements.refs[ref.Ref]
					}
					row, err := reader.store.GetActivityByID(id)
					observation := reader.source("activity", id, 0, false)
					rowAllowed := err == nil && row != nil && !observation.Sensitive && !observation.Unavailable && len(a.filterComposerModelCandidatesContext(ctx, reader.store, []ActivityRow{*row})) == 1
					allowed = rowAllowed && !observation.MissingEvidence
					if rowAllowed && observation.MissingEvidence {
						// Its runs were regrouped or retained away: an edit.
						goneInputs[ref.Ref] = true
					}
				}
			case taskContextSourceSelfKind, "activity", "activity_summary", "activity_run", "activity_timeline_group", "activity_session", "session", meetingFollowupSourceKind:
				allowed = reader.store != nil
				if allowed {
					observation := reader.source(ref.SourceKind, ref.SourceID, 0, false)
					allowed = !observation.Sensitive && !observation.Unavailable && !observation.MissingEvidence
					for _, sessionID := range observation.SessionIDs {
						allowed = allowed && dependencyAllowed(context.Background(), sessionID, nil)
					}
					if ref.SourceKind == taskContextSourceSelfKind {
						entry, err := reader.store.GetSelfModelEntryByID(ref.SourceID)
						allowed = allowed && err == nil && a.selfModelEntryAllowsModelRead(entry)
					}
				}
			}
		}
		readable[ref.Ref] = allowed
		if allowed {
			if strings.HasPrefix(ref.SourceID, threadOutcomeMomentPrefix) {
				label := redactThreadOutcomeText(ref.Label)
				if label != ref.Label {
					changed = true
					if isInput(ref) {
						withhold(mobileBriefWithheldRedacted, ref.SourceKind)
					}
				}
				ref.Label = label
			}
			refs = append(refs, ref)
		} else if !silenced[ref.Ref] {
			changed = true
			if isInput(ref) && !goneInputs[ref.Ref] {
				withdrawnInputs[ref.Ref] = true
			}
		}
	}
	// goneOnly reports refs that include at least one removed source and no
	// withdrawn one: every other ref is readable.
	goneOnly := func(ids []string) bool {
		gone := false
		for _, id := range ids {
			if goneInputs[id] {
				gone = true
			} else if !readable[id] {
				return false
			}
		}
		return gone
	}
	citesWithdrawn := func(ids []string) bool {
		for _, id := range ids {
			if withdrawnInputs[id] {
				return true
			}
		}
		return false
	}
	// citesSilenced reports refs that are readable except for silenced ones,
	// at least one of which they cite: such a card or paragraph announces a
	// recorded call and is left out on its own.
	citesSilenced := func(ids []string) bool {
		cites := false
		for _, id := range ids {
			if silenced[id] || quietProse[id] {
				cites = true
			} else if !readable[id] {
				return false
			}
		}
		return cites
	}
	var attendance []MobileBriefAttendance
	for _, row := range brief.MeetingAttendance {
		if readableAttendance[row.EventID] && mobileBriefRefsReadable(row.Refs, readable) {
			attendance = append(attendance, row)
		} else {
			changed = true
			if citesInput(row.Refs, "") {
				withhold(mobileBriefWithheldSource, mobileBriefAttendanceSource)
			}
		}
	}
	brief.MeetingAttendance = attendance
	keepItems := func(items []MobileBriefItem, requiresMail, allowsFallback bool) []MobileBriefItem {
		if items == nil {
			return nil
		}
		out := make([]MobileBriefItem, 0, len(items))
		for _, item := range items {
			original := item
			purposeID := item.PurposeID
			if purposeID == "" && len(item.Refs) > 0 {
				purposeID = purposeRefs[item.Refs[0]]
			}
			checked := item
			if purposeUserWrote(purposeID, item.Title) {
				checked.Title = ""
			}
			allowed := !closed && (!requiresMail || mailErr == nil) && !mobileBriefValueEchoesMail(checked, subjects)
			alter := func() {
				for _, ref := range original.Refs {
					altered[ref] = true
				}
				if purposeID != "" {
					alteredPurposes[purposeID] = true
				}
				// An item citing only removed sources leaves the section without
				// withdrawing the written words (see goneOnly).
				if citesInput(original.Refs, purposeID) && !goneOnly(original.Refs) {
					kind := "purpose"
					if len(original.Refs) > 0 {
						kind = evidenceByRef[original.Refs[0]].SourceKind
					}
					withhold(mobileBriefWithheldSource, kind)
				}
			}
			if purposeID != "" {
				allowed = allowed && purposeAllowed(purposeID)
			}
			if allowed && purposeID != "" && (item.EvidenceUnavailable || missingPurposeEvidence[purposeID]) {
				// A previous ledger citation cannot preserve derived progress once
				// its activity disappears. Match collection's title/state fallback.
				allowed = allowsFallback
				if allowed {
					current := purposeDetails[purposeID]
					fallback := MobileBriefItem{ID: item.ID, Key: item.Key,
						Title: mobileBriefText(current.Current.Title, mobileBriefTextLimit), Status: current.Current.LifecycleState, Kind: current.OpenLoop.Kind,
						Detail: mobileBriefPurposeText(a, "Evidence no longer on this computer.", "相关依据已不在这台电脑上。"),
						Refs:   []string{}, PurposeID: purposeID, EvidenceUnavailable: true}
					before, _ := json.Marshal(item)
					after, _ := json.Marshal(fallback)
					if !bytes.Equal(before, after) {
						changed = true
						alter()
					}
					item = fallback
				}
			} else {
				if allowed && len(item.Refs) > 0 && citesSilenced(item.Refs) {
					trimmed = true
					continue
				}
				allowed = allowed && mobileBriefRefsReadable(item.Refs, readable)
			}
			if allowed {
				for _, ref := range item.Refs {
					if !calendarRefs[ref] {
						continue
					}
					var parts []string
					for _, part := range strings.Split(item.Detail, "; ") {
						if part != "notes ready" {
							parts = append(parts, part)
						}
					}
					if detail := strings.Join(parts, "; "); detail != item.Detail {
						item.Detail, quietProse[ref], trimmed = detail, true, true
					}
					break
				}
				if item.Outcome != nil || item.Kind == mobileBriefThreadOutcomeSource {
					before, _ := json.Marshal(item)
					item.Title = redactThreadOutcomeText(item.Title)
					item.Detail = redactThreadOutcomeText(item.Detail)
					item.NextStep = redactThreadOutcomeText(item.NextStep)
					item.Outcome = redactThreadOutcome(item.Outcome)
					if item.Context != nil {
						preview := *item.Context
						preview.SourceLabel = redactThreadOutcomeText(preview.SourceLabel)
						preview.Preview = redactThreadOutcomeText(preview.Preview)
						preview.BlockedReason = redactThreadOutcomeText(preview.BlockedReason)
						preview.Schedule = redactThreadOutcomeText(preview.Schedule)
						item.Context = &preview
					}
					after, _ := json.Marshal(item)
					if !bytes.Equal(before, after) {
						changed = true
						alter()
					}
				}
				out = append(out, item)
			} else {
				changed = true
				alter()
				brief.Limits.ExcludedSensitiveItems++
			}
		}
		return out
	}
	brief.Sections = MobileBriefSections{
		Noticed:    mobileBriefOptionalItems(keepItems(brief.Sections.Noticed, false, false)),
		Highlights: mobileBriefOptionalItems(keepItems(brief.Sections.Highlights, true, false)),
		DayAhead:   mobileBriefOptionalItems(keepItems(brief.Sections.DayAhead, true, false)),
		Review:     mobileBriefOptionalItems(keepItems(brief.Sections.Review, true, true)),
		Decide:     mobileBriefOptionalItems(keepItems(brief.Sections.Decide, true, true)),
		Plan:       mobileBriefOptionalItems(keepItems(brief.Sections.Plan, true, false)),
		Moved:      keepItems(brief.Sections.Moved, false, false), Doing: keepItems(brief.Sections.Doing, false, false),
		Watching: keepItems(brief.Sections.Watching, false, false), Meetings: keepItems(brief.Sections.Meetings, false, false),
		People: keepItems(brief.Sections.People, false, false), OpenLoops: keepItems(brief.Sections.OpenLoops, true, true),
		Learned: keepItems(brief.Sections.Learned, false, false), Deliverables: keepItems(brief.Sections.Deliverables, false, false),
		Schedules: keepItems(brief.Sections.Schedules, false, false), WorkingToward: mobileBriefOptionalItems(keepItems(brief.Sections.WorkingToward, true, true)),
		Commitments: mobileBriefOptionalItems(keepItems(brief.Sections.Commitments, true, false)), WaitingOnYou: mobileBriefOptionalItems(keepItems(brief.Sections.WaitingOnYou, true, false)),
	}
	brief.Upcoming = keepItems(brief.Upcoming, false, false)
	// unaltered reports refs whose cards are all delivered as written.
	unaltered := func(ids []string) bool {
		for _, id := range ids {
			if altered[id] || alteredPurposes[purposeRefs[id]] {
				return false
			}
		}
		return true
	}
	var offers []MobileBriefOffer
	for _, offer := range brief.Offers {
		if !mobileBriefValueEchoesMail(offer, subjects) && citesSilenced(offer.Refs) {
			trimmed = true
			continue
		}
		if !mobileBriefRefsReadable(offer.Refs, readable) || mobileBriefValueEchoesMail(offer, subjects) || (template && !unaltered(offer.Refs)) {
			if template {
				trimmed = true
				continue
			}
			changed = true
			if mobileBriefValueEchoesMail(offer, subjects) {
				withhold(mobileBriefWithheldMailEcho, "")
			} else if citesInput(offer.Refs, "") {
				withhold(mobileBriefWithheldSource, "")
			}
		}
		offers = append(offers, offer)
	}
	if brief.Offers != nil {
		brief.Offers = offers
	}
	// A card leaves with any source it cites that cannot be read now, or that
	// is no longer delivered as written; readers rebuild the rest
	// (mobileBriefWithCards).
	var cards []MobileBriefCard
	for _, card := range brief.Cards {
		if citesSilenced(card.Refs) || !mobileBriefRefsReadable(card.Refs, readable) || mobileBriefValueEchoesMail(card, subjects) || !unaltered(card.Refs) {
			continue
		}
		cards = append(cards, card)
	}
	if brief.Cards != nil {
		brief.Cards = cards
	}
	var narrative []MobileBriefPara
	for _, paragraph := range brief.Narrative {
		if !template && citesWithdrawn(paragraph.Refs) {
			// The model's words cite what was withdrawn: they are dropped, never
			// edited. The plain list of the remaining items stands in for them.
			narrativeDropped, changed = true, true
			continue
		}
		if !mobileBriefValueEchoesMail(paragraph, subjects) && redactThreadOutcomeText(paragraph.Text) == paragraph.Text && citesSilenced(paragraph.Refs) {
			trimmed = true
			continue
		}
		if mobileBriefIsOpenAnswerParagraph(brief, paragraph) && !mobileBriefRefsReadable(paragraph.Refs, readable) {
			// Not spoken, so the recording stays; a store not open yet keeps the stored text.
			if notReady || a.store == nil {
				withhold(mobileBriefWithheldNotReady, "")
			}
			continue
		}
		// Words that cite only sources retention or an edit removed stay as
		// written: that is not a withdrawal, so the synthesis is not cleared.
		if !template && goneOnly(paragraph.Refs) && !mobileBriefValueEchoesMail(paragraph, subjects) && redactThreadOutcomeText(paragraph.Text) == paragraph.Text {
			narrative = append(narrative, paragraph)
			continue
		}
		if !staticEmpty && !mobileBriefRefsReadable(paragraph.Refs, readable) || mobileBriefValueEchoesMail(paragraph, subjects) || redactThreadOutcomeText(paragraph.Text) != paragraph.Text || (template && !unaltered(paragraph.Refs)) {
			if template {
				trimmed = true
				continue
			}
			changed = true
			// The text itself cites what can no longer be read.
			switch true {
			case mobileBriefValueEchoesMail(paragraph, subjects):
				withhold(mobileBriefWithheldMailEcho, "")
			case redactThreadOutcomeText(paragraph.Text) != paragraph.Text:
				withhold(mobileBriefWithheldRedacted, "")
			default:
				withhold(mobileBriefWithheldSource, "")
			}
		}
		narrative = append(narrative, paragraph)
	}
	if brief.Narrative != nil {
		brief.Narrative = narrative
		if len(narrative) == 0 && brief.NarrativeStatus != "quiet" {
			brief.Narrative, brief.NarrativeStatus = []MobileBriefPara{}, "unavailable"
		}
	}
	if narrativeDropped && brief.NarrativeStatus != "quiet" {
		brief.Narrative, brief.NarrativeStatus = []MobileBriefPara{}, "skipped"
		// The recording speaks the words that were dropped.
		brief.Audio = nil
	}
	if len(withdrawnInputs) > 0 && len(brief.Cards) == 0 && len(brief.Narrative) == 0 && len(brief.Offers) == 0 {
		// Nothing is left to show: the whole brief is withheld.
		withhold(mobileBriefWithheldSource, "")
	}
	if trimmed {
		// The spoken recording reads every original paragraph.
		brief.Audio = nil
	}
	if brief.DayShape != nil {
		for _, app := range brief.DayShape.TopApps {
			if activitySourceAppIsSensitive(app.Name, app.Name) {
				changed = true
			}
		}
		if mobileBriefValueEchoesMail(brief.DayShape, subjects) {
			changed = true
		}
	}
	if mobileBriefValueEchoesMail(brief.Audio, subjects) {
		changed = true
	}
	if mobileBriefTextEchoesMail(brief.HubName, subjects) {
		brief.HubName = mobileBriefDefaultHub
		changed = true
	}
	if mobileBriefValueEchoesMail(brief.Generation, subjects) {
		brief.Generation = nil
		changed = true
	}
	if brief.rankedEdition && !staticEmpty && len(brief.Narrative) > 0 && (brief.NarrativeStatus != "ok" ||
		!mobileBriefBindingHasSources(brief, brief.modelBinding) || brief.modelBinding.Synthesis == "" ||
		mobileBriefSynthesisDigest(brief) != brief.modelBinding.Synthesis) {
		changed = true
		withhold(mobileBriefWithheldSource, "")
	}
	if !template && brief.modelBinding != nil && (!mobileBriefRankedManifestValid(brief, brief.modelBinding) || !mobileBriefRankedLedgerValid(brief, brief.modelBinding)) {
		changed = true
		withhold(mobileBriefWithheldSource, "")
	}
	if !template && brief.modelBinding != nil && len(brief.modelBinding.RankedInputs) > 0 {
		readable := false
		if brief.rankedEdition {
			var store *mobileBriefStore
			if len(brief.modelBinding.RankedLedgerProofs) > 0 {
				store = a.morningBriefLedgerStore()
			}
			readable = a.morningBriefRankedInputsReadableWithLedger(ctx, brief, brief.modelBinding.RankedInputs, store, brief.modelBinding.RankedLedgerProofs)
		} else {
			readable = a.morningBriefRankedInputsReadable(ctx, mobileBriefAsOfTime(brief), brief.modelBinding.RankedInputs)
		}
		if !readable {
			// The source reader may be temporarily unavailable. Hide the text
			// without deleting its stored copy until that distinction is known.
			changed = true
			if morningBriefMissingLedgerProof(a.morningBriefLedgerStore(), brief.modelBinding.RankedLedgerProofs) {
				withhold(mobileBriefWithheldSource, "brief")
			} else {
				withhold(mobileBriefWithheldNotReady, "")
			}
		}
	}
	// The About-you notes the model's prompt read are inputs too (backlog
	// item 65): one deleted, withheld or retitled withdraws the text.
	if !template && brief.modelBinding != nil && len(brief.modelBinding.Profile) > 0 &&
		(memoryErr != nil || !a.mobileBriefProfileNotesReadable(brief.modelBinding.Profile, allowMemory)) {
		changed = true
		withhold(mobileBriefWithheldSource, "memory")
	}
	if !template && brief.modelBinding != nil && !a.morningBriefWeekIntentBindingReadable(ctx, brief, brief.modelBinding) {
		changed = true
		withhold(mobileBriefWithheldSource, "week_intent")
	}
	if !template && brief.modelBinding != nil && brief.modelBinding.YesterdayActivity != nil {
		topics, readable := a.mobileBriefYesterdayReadable(ctx, brief, brief.modelBinding.YesterdayActivity)
		switch true {
		case !readable:
			changed = true
			withhold(mobileBriefWithheldNotReady, "activity")
		case mobileBriefModelDigest(topics) != brief.modelBinding.YesterdayDigest:
			changed = true
			withhold(mobileBriefWithheldSource, "activity")
		}
	}
	// The model's text may mention a recorded call it read without citing it
	// (#62).
	if !template && (len(silenced) > 0 || len(quietProse) > 0) {
		changed = true
		for _, ids := range []map[string]bool{silenced, quietProse} {
			for id := range ids {
				if citesInput([]string{id}, "") {
					withhold(mobileBriefWithheldRecording, evidenceByRef[id].SourceKind)
				}
			}
		}
	}
	if changed {
		// Citations are not a complete synthesis dependency ledger: removing
		// any input invalidates the model's narrative, its offers and the
		// spoken recording. A template has already left out what it must.
		if !template && textChanged {
			// The model wrote the cards' words with the same inputs.
			brief.Offers, brief.Cards = nil, nil
			brief.Narrative, brief.NarrativeStatus = []MobileBriefPara{}, "unavailable"
		}
		// The recording speaks the text: it stays while the text does.
		if template || textChanged {
			brief.Audio = nil
		}
		brief.DayShape = nil
		brief.Counts = MobileBriefCounts{OpenLoopsByKind: map[string]int{}}
		for _, item := range brief.Sections.OpenLoops {
			kind := mobileBriefFirstText(item.Kind, "other")
			brief.Counts.OpenLoopsByKind[kind]++
		}
	}
	// Drop orphan labels as well as blocked refs. A removed card's neutral
	// sibling citation must not keep its old subject or source ID in JSON.
	used := map[string]bool{}
	for _, section := range mobileBriefSectionLines(brief.Sections) {
		for _, item := range section.items {
			for _, ref := range item.Refs {
				used[ref] = true
			}
		}
	}
	for _, item := range brief.Upcoming {
		for _, ref := range item.Refs {
			used[ref] = true
		}
	}
	for _, attendance := range brief.MeetingAttendance {
		for _, ref := range attendance.Refs {
			used[ref] = true
		}
	}
	for _, paragraph := range brief.Narrative {
		for _, ref := range paragraph.Refs {
			used[ref] = true
		}
	}
	brief.Evidence = []MobileBriefRef{}
	for _, ref := range refs {
		if used[ref.Ref] {
			brief.Evidence = append(brief.Evidence, ref)
			if changed && ref.SourceKind == "learning" {
				brief.Counts.LearningItems++
			}
		}
	}
	week, recap := a.yourWeekForDeliveryWithLeadContext(ctx, brief, true)
	if len(brief.WeeklyRecap) > 0 && !mobileBriefWeeklyRecapMatches(brief.WeeklyRecap, recap) {
		// A cached recording may still speak a withdrawn part of the week.
		brief.Audio = nil
	}
	brief.WeeklyRecap, brief.YourWeek = recap, week
	brief.ConversationKey = mobileBriefConversationKey(brief)
	// A template's paragraphs leave on their own, so it reports no withholding,
	// unless what hid them passes on its own (Closed Eyes, a store not open
	// yet, an unreadable mail index). The reader needs that reason so it never
	// saves the withdrawn projection over the stored brief.
	if (template || !textChanged) && !withheld.Transient() {
		withheld = mobileBriefWithholding{}
	}
	return brief, withheld
}

func mobileBriefRefsReadable(refs []string, readable map[string]bool) bool {
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if !readable[ref] {
			return false
		}
	}
	return true
}

// Optional sections stay absent on the wire when every source is withheld.
func mobileBriefOptionalItems(items []MobileBriefItem) []MobileBriefItem {
	if len(items) == 0 {
		return nil
	}
	return items
}

// mobileBriefRecordingAnnouncement reports a recorded call's session or the
// notes saved from it. Both stay silent on Today and the phone (#62): they are
// left out, but unlike a source withdrawn for privacy they do not withdraw
// the rest of the written brief.
func (a *App) mobileBriefRecordingAnnouncement(ref MobileBriefRef) bool {
	baseID, _, _ := strings.Cut(ref.SourceID, ":")
	var content string
	switch ref.SourceKind {
	case mobileBriefMeetingAppSource:
		return true
	case "memory", taskContextSourceMemoryKind:
		entry, err := a.GetMemoryEntry(baseID)
		if err != nil {
			return false
		}
		content = entry.Content
	case "knowledge", taskContextSourceKnowledgeKind:
		entry, err := a.GetKnowledgeEntry(baseID)
		if err != nil {
			return false
		}
		content = entry.Content
	default:
		return false
	}
	doc, ok := parseOKFDocument(content)
	return ok && okfString(doc.Frontmatter, "source") == meetingKnowledgeSource
}
