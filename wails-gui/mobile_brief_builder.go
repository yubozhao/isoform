package main

import (
	"context"
	"errors"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wails-gui/internal/calendar"
	"wails-gui/internal/handoff"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

// Section keys. They match the JSON names in MobileBriefSections and the
// entries allowed in limits.truncatedSections.
const (
	mobileBriefSectionNoticed       = "noticed"
	mobileBriefSectionMoved         = "moved"
	mobileBriefSectionDoing         = "doing"
	mobileBriefSectionWatching      = "watching"
	mobileBriefSectionMeetings      = "meetings"
	mobileBriefSectionPeople        = "people"
	mobileBriefSectionOpenLoops     = "openLoops"
	mobileBriefSectionLearned       = "learned"
	mobileBriefSectionDeliverables  = "deliverables"
	mobileBriefSectionSchedules     = "schedules"
	mobileBriefSectionWorkingToward = "workingToward"
	mobileBriefSectionCommitments   = "commitments"
	mobileBriefSectionWaitingOnYou  = "waitingOnYou"
)

const (
	mobileBriefSlotMorning   = "morning"
	mobileBriefSlotEvening   = "evening"
	mobileBriefSlotOnRequest = "on_request"

	mobileBriefItemsPerSection = 8
	mobileBriefEvidenceLimit   = 60
	mobileBriefTextLimit       = 320
	mobileBriefLabelLimit      = 200
	mobileBriefKindLimit       = 64
	mobileBriefSourceIDLimit   = 128
	mobileBriefAppNameLimit    = 120
	mobileBriefSessionLimit    = 500
	mobileBriefDefaultHub      = "Computer"
)

// mobileBriefInput is the plain data behind one brief. collectMobileBriefInput
// fills it from the app; buildMobileBriefV1 turns it into the document without
// touching app state, the clock or the environment, so every bound and privacy
// rule can be tested from fixtures.
type mobileBriefInput struct {
	Slot        string
	WindowStart time.Time
	// BriefDay is the day the brief is about (an early brief for the next
	// morning's slot). Zero means the as-of time's own day, which every existing
	// call relies on. Day words, the calendar and the day's sessions use it;
	// the gather still stops at AsOf, the time the brief is made.
	BriefDay time.Time
	AsOf     time.Time
	// ClockLayout is how the brief writes a time of day, the way the app
	// shows times in the user's language ("3:04 PM", or "15:04").
	ClockLayout string
	// Language is "en" or "zh", the language of the brief's own lines such as
	// attention reasons. Empty reads as English.
	Language string
	// RecapStart is where the morning recap and the moved work start: the
	// last day the Mac was in use. Zero outside the morning.
	RecapStart time.Time
	Location   *time.Location
	Timezone   string
	HubName    string

	OpenLoopCounts     map[string]int
	OpenLoopsTruncated bool
	PreviousBriefs     []MobileBriefV1
	// YesterdayActivity keeps the source identities for rechecking topic time.
	YesterdayActivity *yourWeekSnapshot
	// rankedEvidence is reserved before legacy sections consume their ref budget.
	rankedEvidence []MobileBriefRef
	ColdStart      bool
	// History is the assistant history view read when the brief was gathered.
	History AssistantHistoryView

	Moved    []mobileBriefCandidate
	Doing    []mobileBriefCandidate
	Watching []mobileBriefCandidate
	Meetings []mobileBriefCandidate
	// TomorrowMeetings is scoring input, separate from today's displayed rows.
	TomorrowMeetings []mobileBriefCandidate
	People           []mobileBriefCandidate
	OpenLoops        []mobileBriefCandidate
	Learned          []mobileBriefCandidate
	Deliverables     []mobileBriefCandidate
	Schedules        []mobileBriefCandidate
	WorkingToward    []mobileBriefCandidate
	Commitments      []mobileBriefCandidate
	Noticed          []mobileBriefCandidate
	WaitingOnYou     []mobileBriefCandidate
	// Conversations are activity summaries from messaging apps (WeChat, Slack,
	// Messages, ...). They reach only the new writer, never a section, and
	// pass the same sensitive-app, pause and private-window gates as the recap.
	Conversations          []mobileBriefCandidate
	ExcludedSensitiveItems int

	DayShape   *MobileBriefDay
	Attendance []mobileBriefAttendanceObservation
	Today      *mobileBriefTodayInput
	// A current Purpose read replaces stored Purpose rows, even when the read
	// failed or privacy gates left no rows safe to display.
	PurposesRefreshed bool
	// The shape of today was recomputed for this refresh: the stored line,
	// true only when it was built, is not kept (collectMobileBriefTodayShape).
	DayShapeRefreshed bool
	// Retain citations from an existing recap while its local signals refresh.
	RecapEvidence []MobileBriefRef
	// Generation delays this output-only cache until its source fence passes.
	deferAttendanceCache bool
	// memory is the memory folder as the collection began, when pinned by
	// the gather fence (mobileBriefPinnedMemory); nil reads it now.
	memory          *memoryExternalSnapshot
	attendanceCache *mobileBriefAttendanceDay
}

// mobileBriefCandidate is one source record before it becomes a section item.
// Apps lists the app names behind the record and Protected marks a record whose
// source sessions are in the protected privacy state. buildMobileBriefV1 drops
// protected records, drops records whose apps are all sensitive, and never
// renders a sensitive app name. AppsShown adds the remaining app names to the
// detail line; it is off when the title already names the app.
type mobileBriefCandidate struct {
	LoopKey     string
	LoopAliases []string
	OpenedAt    time.Time
	Person      string
	Outcome     *ActivityThreadOutcome
	SourceKind  string
	SourceID    string
	Title       string
	// EvidenceLabel preserves source identity when a card title names a person.
	EvidenceLabel string
	Detail        string
	Status        string
	Kind          string
	At            time.Time
	EndAt         time.Time
	Apps          []string
	AppsShown     bool
	Protected     bool
	// URL is the item's web link (a meeting's join link), http or https.
	URL       string
	LastTouch time.Time
	// confidence is the source's recorded confidence, when available; it is
	// never derived from a due date or a user's observed activity. These local
	// scoring fields are excluded from legacy candidate hashes and wire data.
	confidence          float64
	confidenceKnown     bool
	rankedSourceDigest  string
	rankedOriginDigest  string
	rankedAttendees     []string
	rankedInvitePending bool
	rankedDependencies  []morningBriefSourceDependency
	rankedUnbound       bool
	NextStep            string
	DueAt               time.Time
	MomentID            string
	ChatSessionID       string
	ChatMessageID       string
	AttentionMs         int64
	Urgency             int
	Evidence            []mobileBriefCandidate
	EvidenceUnavailable bool
	WebSource           *MobileBriefWebSource
	// FollowUp marks a commitment the user added from a moment in Activity.
	FollowUp bool
	// writerDetail is what the screen summary says changed, for the new
	// writer only: it never reaches a section item, a card or the wire.
	writerDetail string
}

// briefDayRef is midnight of the day the brief is about, in loc: the slot's
// day when BriefDay is set, otherwise the day of AsOf. Every day-word and
// calendar-day comparison uses only the civil date, so for a brief with no
// BriefDay this is the same date the brief always used.
func (in mobileBriefInput) briefDayRef(loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	day := in.AsOf.In(loc)
	if !in.BriefDay.IsZero() {
		day = in.BriefDay.In(loc)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
}

// buildMobileBriefV1 composes one brief from plain input. Given the same input
// it always returns the same document. Sections are bounded to eight items, the
// whole document to sixty evidence refs, and every string to its byte limit.
func buildMobileBriefV1(in mobileBriefInput) MobileBriefV1 {
	in = mobileBriefAnchorInput(in)
	in = selectMobileBriefOpenLoops(in)
	in = mobileBriefChiefInput(in)
	in = mobileBriefObservationMeetings(in)
	// The local date is the user's: a brief made after 17:00 in California
	// is already tomorrow in UTC.
	loc := in.Location
	if loc == nil {
		loc = time.Local
	}
	localAsOf := in.AsOf.In(loc)
	dayRef := in.briefDayRef(loc)
	localDate := dayRef.Format("2006-01-02")
	slot := mobileBriefSlot(in.Slot)

	builder := &mobileBriefBuilder{
		asOf:     in.AsOf,
		dayRef:   dayRef,
		loc:      loc,
		lang:     mobileBriefVoiceLang(in.Language),
		evidence: []MobileBriefRef{},
		index:    map[string]string{},
	}
	if in.OpenLoopsTruncated {
		builder.truncated = append(builder.truncated, mobileBriefSectionOpenLoops)
	}
	builder.reserveRecap(in.rankedEvidence)
	reserveMobileBriefChiefEvidence(builder, in)
	reserved := mobileBriefEvidenceCounts(builder.evidence)

	// Share the 60-ref budget explicitly: up to 3 upcoming actions, 8 live
	// failures and 4 primary recap citations first; then atomic Purpose rows
	// have at least 45 slots. Additional recap citations and ordinary sections
	// use the remainder. Shared source IDs deduplicate across every reservation,
	// so neither a crowded Purpose Map nor an old recap can starve the other.
	upcoming := builder.reserveToday(in)
	workingToward := builder.sectionLimit(mobileBriefSectionWorkingToward,
		prepareMobileBriefPurposeCandidates(in.WorkingToward, mobileBriefPurposeFirst), 5)
	commitments := builder.section(mobileBriefSectionCommitments,
		prepareMobileBriefPurposeCandidates(in.Commitments, mobileBriefPurposeFirst))
	waitingOnYou := builder.sectionLimit(mobileBriefSectionWaitingOnYou,
		prepareMobileBriefPurposeCandidates(in.WaitingOnYou, mobileBriefOldestTouchFirst), 3)
	builder.reserveRecap(in.RecapEvidence)

	moved := builder.section(
		mobileBriefSectionMoved,
		prepareMobileBriefCandidates(in.Moved, mobileBriefReviewFirst),
	)
	doing := builder.section(
		mobileBriefSectionDoing,
		prepareMobileBriefCandidates(in.Doing, mobileBriefNewestFirst),
	)
	attendance := buildMobileBriefAttendance(builder, in.Attendance)
	watching := builder.section(
		mobileBriefSectionWatching,
		prepareMobileBriefCandidates(in.Watching, mobileBriefNewestFirst),
	)
	meetings := builder.section(
		mobileBriefSectionMeetings,
		prepareMobileBriefCandidates(in.Meetings, mobileBriefUpcomingFirst),
	)
	people := builder.section(
		mobileBriefSectionPeople,
		prepareMobileBriefCandidates(in.People, mobileBriefNewestFirst),
	)

	// Counts are taken from the prepared lists before truncation so they
	// describe everything the brief knows about, not only what it shows.
	openLoopItems := prepareMobileBriefCandidates(in.OpenLoops, mobileBriefOpenLoopFirst)
	openLoopKinds := map[string]int{}
	for _, item := range openLoopItems {
		kind := item.Kind
		if kind == "" {
			kind = "other"
		}
		openLoopKinds[kind]++
	}
	if in.OpenLoopCounts != nil {
		openLoopKinds = in.OpenLoopCounts
	}
	// A source already carried by Moved needs one row on the wire. It still
	// participates in cross-day selection, the cap, counts and recent-brief dedupe.
	movedSources := map[string]bool{}
	for _, item := range moved {
		if len(item.Refs) > 0 {
			movedSources[item.Refs[0]] = true
		}
	}
	openLoops := builder.section(mobileBriefSectionOpenLoops, openLoopItems)
	uniqueLoops := openLoops[:0]
	for _, item := range openLoops {
		if len(item.Refs) == 0 || !movedSources[item.Refs[0]] {
			uniqueLoops = append(uniqueLoops, item)
		}
	}
	openLoops = uniqueLoops

	learnedItems := prepareMobileBriefCandidates(in.Learned, mobileBriefNewestFirst)
	learningItems := 0
	for _, item := range learnedItems {
		if item.SourceKind == "learning" {
			learningItems++
		}
	}
	learned := builder.section(mobileBriefSectionLearned, learnedItems)

	deliverables := builder.section(
		mobileBriefSectionDeliverables,
		prepareMobileBriefCandidates(in.Deliverables, mobileBriefNewestFirst),
	)
	schedules := builder.section(
		mobileBriefSectionSchedules,
		prepareMobileBriefCandidates(in.Schedules, mobileBriefNewestFirst),
	)

	// Optional sections use nil when empty, matching their persisted JSON.
	if len(workingToward) == 0 {
		workingToward = nil
	}
	if len(commitments) == 0 {
		commitments = nil
	}
	if len(waitingOnYou) == 0 {
		waitingOnYou = nil
	}
	sections := MobileBriefSections{
		Noticed: mobileBriefOptionalItems(builder.sectionLimit(mobileBriefSectionNoticed,
			prepareMobileBriefCandidates(in.Noticed, mobileBriefNewestFirst), mobileBriefNoticedLimit)),
		Moved:         moved,
		Doing:         doing,
		Watching:      watching,
		Meetings:      meetings,
		People:        people,
		OpenLoops:     openLoops,
		Learned:       learned,
		Deliverables:  deliverables,
		Schedules:     schedules,
		WorkingToward: workingToward,
		Commitments:   commitments,
		WaitingOnYou:  waitingOnYou,
	}
	// These three are left off the wire when empty, so an empty one is nil: the
	// value a stored copy decodes to. A generated brief then equals its reload.
	if len(sections.WorkingToward) == 0 {
		sections.WorkingToward = nil
	}
	if len(sections.Commitments) == 0 {
		sections.Commitments = nil
	}
	if len(sections.WaitingOnYou) == 0 {
		sections.WaitingOnYou = nil
	}
	brief := MobileBriefV1{
		ownVoiceFacts:     true,
		generationDebug:   mobileBriefGenerationCounts(builder.evidence, reserved),
		Version:           1,
		BriefID:           mobileBriefID(localDate, slot, localAsOf),
		Slot:              slot,
		LocalDate:         localDate,
		Timezone:          mobileBriefTimezone(in.Timezone, loc),
		WindowStart:       formatMobileBriefTime(in.WindowStart),
		AsOf:              formatMobileBriefTime(in.AsOf),
		HubName:           mobileBriefHub(in.HubName),
		ColdStart:         in.ColdStart,
		FromHistory:       len(in.History.Projects) > 0,
		NarrativeStatus:   "unavailable",
		Narrative:         []MobileBriefPara{},
		Sections:          sections,
		Upcoming:          upcoming,
		DayShape:          mobileBriefDayForBrief(in.DayShape),
		MeetingAttendance: attendance,
		Counts: MobileBriefCounts{
			OpenLoopsByKind: openLoopKinds,
			LearningItems:   learningItems,
		},
		Evidence: builder.evidence,
		Limits: MobileBriefLimits{
			ItemsPerSection:        mobileBriefItemsPerSection,
			EvidenceRefs:           mobileBriefEvidenceLimit,
			TruncatedSections:      uniqueSortedStrings(builder.truncated),
			ExcludedSensitiveItems: in.ExcludedSensitiveItems,
		},
	}
	brief.Sections = mobileBriefChiefSections(brief)
	// These sections are left off the wire when empty, so an empty one is nil:
	// the value a stored copy decodes to. A built brief then equals its reload.
	for _, section := range []*[]MobileBriefItem{&brief.Sections.Review, &brief.Sections.Decide, &brief.Sections.Plan, &brief.Sections.DayAhead, &brief.Sections.Highlights} {
		if len(*section) == 0 {
			*section = nil
		}
	}
	if len(brief.MeetingAttendance) == 0 {
		brief.MeetingAttendance = nil
	}
	if in.ColdStart || mobileBriefSectionsEmpty(brief.Sections) {
		brief.NarrativeStatus = "skipped"
	}
	if in.ColdStart {
		// The cold start has no model narrative; the history line is its text.
		if para, ok := mobileBriefHistoryParagraph(in.History, in.Language); ok {
			brief.Narrative = []MobileBriefPara{para}
		}
	}
	applyMobileBriefToday(&brief, in.Today)
	return mobileBriefWithDisplayTimes(brief, in.Language)
}

// mobileBriefBuilder assigns evidence refs to section items. Items that share a
// source record share one ref, and once the evidence budget is spent further
// items are dropped and their section is recorded as truncated.
type mobileBriefBuilder struct {
	asOf      time.Time
	dayRef    time.Time
	loc       *time.Location
	lang      string
	evidence  []MobileBriefRef
	index     map[string]string
	truncated []string
}

func (b *mobileBriefBuilder) section(name string, items []mobileBriefCandidate) []MobileBriefItem {
	return b.sectionLimit(name, items, mobileBriefItemsPerSection)
}

func (b *mobileBriefBuilder) sectionLimit(name string, items []mobileBriefCandidate, limit int) []MobileBriefItem {
	out := []MobileBriefItem{}
	for _, item := range items {
		if len(out) >= limit {
			b.truncated = append(b.truncated, name)
			break
		}
		if item.EvidenceUnavailable {
			// Identity is separate from evidence so old clients also show no
			// source chip. Only the safe ledger title/state survive missing data.
			out = append(out, MobileBriefItem{
				ID: name + "-" + strconv.Itoa(len(out)+1), Title: item.Title,
				Detail: item.Detail, Status: item.Status, Kind: item.Kind,
				Refs: []string{}, PurposeID: item.SourceID, EvidenceUnavailable: true,
			})
			continue
		}
		// A progress statement and its backing references are one unit. Never
		// spend the last ref on a title and silently lose its source evidence.
		needed := map[string]bool{}
		calendarNeeded := map[string]bool{}
		for _, source := range append([]mobileBriefCandidate{item}, item.Evidence...) {
			key := source.SourceKind + "\x00" + source.SourceID
			if _, exists := b.index[key]; !exists {
				needed[key] = true
				if source.SourceKind == mobileBriefCalendarSource {
					calendarNeeded[key] = true
				}
			}
		}
		if len(b.evidence)+len(needed) > mobileBriefEvidenceLimit || mobileBriefEvidenceCounts(b.evidence)[mobileBriefCalendarSource]+len(calendarNeeded) > mobileBriefCalendarLimit {
			b.truncated = append(b.truncated, name)
			continue
		}
		ref, ok := b.refFor(item)
		if !ok {
			b.truncated = append(b.truncated, name)
			continue // Later items may have reserved decision/preparation refs.
		}
		refs := []string{ref}
		for _, source := range item.Evidence {
			if evidenceRef, ok := b.refFor(source); ok {
				if !containsString(refs, evidenceRef) {
					refs = append(refs, evidenceRef)
				}
			} else {
				b.truncated = append(b.truncated, name)
			}
		}
		// Civil-day comparisons only, so the brief's day is the reference. A
		// zero as-of time keeps its old meaning: no attention reason.
		dayAnchor := b.dayRef
		if b.asOf.IsZero() {
			dayAnchor = time.Time{}
		}
		reason, rank := mobileBriefAttention(item, dayAnchor, b.loc, b.lang)
		out = append(out, MobileBriefItem{
			Outcome:             item.Outcome,
			ID:                  name + "-" + strconv.Itoa(len(out)+1),
			Title:               item.Title,
			Detail:              item.Detail,
			Status:              item.Status,
			At:                  formatMobileBriefTime(item.At),
			EndAt:               formatMobileBriefTime(item.EndAt),
			Kind:                item.Kind,
			Refs:                refs,
			LastTouch:           formatMobileBriefTime(item.LastTouch),
			NextStep:            item.NextStep,
			DueAt:               formatMobileBriefTime(item.DueAt),
			MomentID:            item.MomentID,
			ChatSessionID:       item.ChatSessionID,
			ChatMessageID:       item.ChatMessageID,
			URL:                 mobileBriefLink(item.URL),
			Since:               formatMobileBriefTime(item.OpenedAt),
			AttentionReason:     reason,
			AttentionRank:       rank,
			EvidenceUnavailable: item.EvidenceUnavailable,
		})
	}
	b.assignKeys(name, out)
	return out
}

func (b *mobileBriefBuilder) refFor(item mobileBriefCandidate) (string, bool) {
	key := item.SourceKind + "\x00" + item.SourceID
	if ref, ok := b.index[key]; ok {
		return ref, true
	}
	if len(b.evidence) >= mobileBriefEvidenceLimit {
		return "", false
	}
	if item.SourceKind == mobileBriefCalendarSource && mobileBriefEvidenceCounts(b.evidence)[mobileBriefCalendarSource] >= mobileBriefCalendarLimit {
		return "", false
	}
	ref := "e" + strconv.Itoa(len(b.evidence)+1)
	observed := item.At
	if observed.IsZero() && !item.EvidenceUnavailable {
		observed = b.asOf
	}
	b.evidence = append(b.evidence, MobileBriefRef{
		Ref:        ref,
		SourceKind: mobileBriefText(item.SourceKind, mobileBriefKindLimit),
		SourceID:   mobileBriefText(item.SourceID, mobileBriefSourceIDLimit),
		Label:      mobileBriefText(mobileBriefFirstText(item.EvidenceLabel, item.Title), mobileBriefLabelLimit),
		ObservedAt: formatMobileBriefTime(observed),
		WebSource:  item.WebSource,
	})
	b.index[key] = ref
	return ref, true
}

// prepareMobileBriefCandidates applies the privacy rules and the text bounds to
// the records of one section, then orders them. The day shape is filtered by
// mobileBriefDayForBrief.
func prepareMobileBriefCandidates(
	candidates []mobileBriefCandidate,
	less func(a, b mobileBriefCandidate) bool,
) []mobileBriefCandidate {
	out := make([]mobileBriefCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Protected || strings.TrimSpace(candidate.SourceID) == "" || candidate.SourceKind == "purpose" && mobileBriefCandidateSensitive(candidate) {
			continue
		}
		if candidate.Outcome != nil || candidate.Kind == mobileBriefThreadOutcomeSource {
			candidate.Title = redactThreadOutcomeText(candidate.Title)
			candidate.Detail = redactThreadOutcomeText(candidate.Detail)
			candidate.NextStep = redactThreadOutcomeText(candidate.NextStep)
		}
		title := mobileBriefText(candidate.Title, mobileBriefTextLimit)
		if title == "" || !mobileBriefCopySafe(title) {
			continue
		}
		names := mobileBriefCleanNames(candidate.Apps)
		allowed := mobileBriefAllowedNames(names)
		if len(names) > 0 && len(allowed) == 0 {
			continue
		}
		shown := ""
		if candidate.AppsShown {
			shown = strings.Join(allowed, ", ")
		}
		candidate.Title = title
		candidate.Detail = mobileBriefText(
			mobileBriefJoinParts(mobileBriefSafeLines(candidate.Detail), shown),
			mobileBriefTextLimit,
		)
		candidate.Status = mobileBriefText(candidate.Status, mobileBriefKindLimit)
		candidate.Kind = mobileBriefText(candidate.Kind, mobileBriefKindLimit)
		candidate.NextStep = mobileBriefText(mobileBriefSafeLines(candidate.NextStep), mobileBriefTextLimit)
		candidate.writerDetail = mobileBriefText(mobileBriefSafeLines(candidate.writerDetail), mobileBriefTextLimit)
		candidate.MomentID = mobileBriefText(candidate.MomentID, mobileBriefSourceIDLimit)
		candidate.ChatSessionID = mobileBriefText(candidate.ChatSessionID, mobileBriefSourceIDLimit)
		candidate.ChatMessageID = mobileBriefText(candidate.ChatMessageID, mobileBriefSourceIDLimit)
		if candidate.Outcome != nil {
			outcome := *candidate.Outcome
			outcome.Summary = threadOutcomeText(mobileBriefVoiceLabel(outcome.Summary), 200)
			outcome.NextStep = threadOutcomeText(mobileBriefVoiceLabel(outcome.NextStep), 120)
			outcome.Question = threadOutcomeText(mobileBriefVoiceLabel(outcome.Question), 120)
			candidate.Outcome = &outcome
		}
		candidate.Evidence = prepareMobileBriefCandidates(candidate.Evidence, mobileBriefNewestFirst)
		candidate.Apps = nil
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return less(out[i], out[j])
	})
	return out
}

func mobileBriefNewestFirst(a, b mobileBriefCandidate) bool {
	if a.At.IsZero() != b.At.IsZero() {
		return !a.At.IsZero()
	}
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	if a.SourceKind != b.SourceKind {
		return a.SourceKind < b.SourceKind
	}
	return a.SourceID < b.SourceID
}

func mobileBriefGroupedByKind(a, b mobileBriefCandidate) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return mobileBriefNewestFirst(a, b)
}

// mobileBriefCleanNames trims app names and drops empty ones.
func mobileBriefCleanNames(names []string) []string {
	out := []string{}
	for _, name := range names {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// mobileBriefAllowedNames returns the names that are not user-protected apps,
// de-duplicated, sorted and bounded. The protection check runs on the full
// name before the bound, so a truncated name can never slip past it.
func mobileBriefAllowedNames(names []string) []string {
	out := []string{}
	for _, name := range uniqueSortedStrings(names) {
		if pii.IsSensitiveAppName(name) {
			continue
		}
		out = append(out, mobileBriefText(name, mobileBriefAppNameLimit))
	}
	return out
}

func mobileBriefJoinParts(parts ...string) string {
	kept := []string{}
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "; ")
}

// mobileBriefDayForBrief keeps the day's active time and hours. Today and the
// phone show what moved, not time per app (docs/today-page.md), so the top-app
// list stays empty on the wire. It returns nil when the day has no active time.
func mobileBriefDayForBrief(day *MobileBriefDay) *MobileBriefDay {
	if day == nil || (day.ActiveMinutes <= 0 && len(day.Hours) == 0) {
		return nil
	}
	out := &MobileBriefDay{
		ActiveMinutes: mobileBriefNonNegative(day.ActiveMinutes),
		Hours:         []MobileBriefHour{},
		TopApps:       []MobileBriefAppTime{},
		FirstActive:   day.FirstActive,
		LastActive:    day.LastActive,
		BreakMinutes:  mobileBriefNonNegative(day.BreakMinutes),
	}
	for _, hour := range day.Hours {
		if hour.Hour < 0 || hour.Hour > 23 || hour.ActiveMinutes <= 0 {
			continue
		}
		out.Hours = append(out.Hours, MobileBriefHour{
			Hour:          hour.Hour,
			ActiveMinutes: hour.ActiveMinutes,
		})
	}
	return out
}

func mobileBriefSectionsEmpty(sections MobileBriefSections) bool {
	return len(sections.Moved)+len(sections.Doing)+len(sections.Watching)+
		len(sections.Meetings)+len(sections.People)+len(sections.OpenLoops)+
		len(sections.Learned)+len(sections.Deliverables)+len(sections.Schedules)+len(sections.Noticed)+
		len(sections.Highlights)+len(sections.DayAhead)+len(sections.Review)+len(sections.Decide)+len(sections.Plan)+len(sections.WorkingToward)+len(sections.Commitments)+len(sections.WaitingOnYou) == 0
}

func mobileBriefSlot(slot string) string {
	switch slot {
	case mobileBriefSlotMorning, mobileBriefSlotEvening:
		return slot
	default:
		return mobileBriefSlotOnRequest
	}
}

// mobileBriefID returns <localDate>-<slot> for scheduled briefs and
// <localDate>-on_request-<HHMMSS> otherwise.
func mobileBriefID(localDate, slot string, localAsOf time.Time) string {
	if slot == mobileBriefSlotOnRequest {
		return localDate + "-on_request-" + localAsOf.Format("150405")
	}
	return localDate + "-" + slot
}

func mobileBriefTimezone(name string, loc *time.Location) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return mobileBriefText(trimmed, mobileBriefKindLimit)
	}
	return mobileBriefTimezoneName(loc)
}

func mobileBriefHub(name string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return mobileBriefText(trimmed, mobileBriefTextLimit)
	}
	return mobileBriefDefaultHub
}

func formatMobileBriefTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// mobileBriefText trims value and bounds it to maxBytes. agencyBoundedText
// counts runes and appends an ellipsis, so the rune budget is chosen such that
// the marked result still fits the byte limit that the protocol uses.
func mobileBriefText(value string, maxBytes int) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "")
	if len(value) <= maxBytes {
		return value
	}
	const ellipsis = "..."
	budget, used := 0, 0
	for _, r := range value {
		size := utf8.RuneLen(r)
		if used+size+len(ellipsis) > maxBytes {
			break
		}
		used += size
		budget++
	}
	return agencyBoundedText(value, budget)
}

func mobileBriefNonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func mobileBriefFirstText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// mobileBriefDayShape reduces one local day's sessions to active minutes per
// hour, with the shared present-time total. Activity is clipped to
// [dayStart, asOf) and split across hour boundaries in loc. It returns nil
// when there are no sessions, and the caller fills in top apps.
func mobileBriefDayShape(
	rows []ActivitySessionRow,
	loc *time.Location,
	dayStart, asOf time.Time,
	presentMs int64,
) *MobileBriefDay {
	if len(rows) == 0 {
		return nil
	}
	if loc == nil {
		loc = time.UTC
	}
	dayStartMs := dayStart.UnixMilli()
	asOfMs := asOf.UnixMilli()
	var hourMs [24]int64
	for _, row := range rows {
		start := max(row.StartTime, dayStartMs)
		end := min(row.EndTime, asOfMs)
		for cursor := start; cursor < end; {
			current := time.UnixMilli(cursor).In(loc)
			nextHour := time.Date(
				current.Year(), current.Month(), current.Day(),
				current.Hour()+1, 0, 0, 0, loc,
			).UnixMilli()
			segmentEnd := min(nextHour, end)
			if segmentEnd <= cursor {
				break
			}
			hourMs[current.Hour()] += segmentEnd - cursor
			cursor = segmentEnd
		}
	}
	day := &MobileBriefDay{
		ActiveMinutes: int(presentMs / 60000),
		Hours:         []MobileBriefHour{},
		TopApps:       []MobileBriefAppTime{},
	}
	for hour := range hourMs {
		if minutes := hourMs[hour] / 60000; minutes > 0 {
			day.Hours = append(day.Hours, MobileBriefHour{Hour: hour, ActiveMinutes: int(minutes)})
		}
	}
	return day
}

func mobileBriefTimeRange(startMs, endMs int64, loc *time.Location, _ string) string {
	if loc == nil {
		loc = time.UTC
	}
	clock := func(ms int64) string {
		at := time.UnixMilli(ms).In(loc)
		return briefShortClock(at.Hour(), at.Minute(), "en")
	}
	start := clock(startMs)
	if endMs <= startMs {
		return start
	}
	return start + " to " + clock(endMs)
}

// mobileBriefClockLayout supplies the base layout. Display normalization turns
// it into the shared English or Chinese clock format before publication.
func mobileBriefClockLayout(languageName string) string {
	lower := strings.ToLower(languageName)
	if briefChinese(languageName) || strings.Contains(lower, "japanese") {
		return calendar.ClockLayout
	}
	return "3:04 PM"
}

// collectMobileBriefInput gathers the brief's source data for the window
// [windowStart, asOf). Each source is best effort: an unavailable source leaves
// its sections empty and is logged without its content.
func collectMobileBriefInput(
	ctx context.Context,
	a *App,
	slot string,
	windowStart, asOf time.Time,
) (mobileBriefInput, error) {
	in := mobileBriefInput{
		Slot:                 slot,
		WindowStart:          windowStart,
		AsOf:                 asOf,
		Location:             time.Local,
		Timezone:             mobileBriefTimezoneName(time.Local),
		HubName:              mobileBriefHubName(),
		deferAttendanceCache: ctx.Value(mobileBriefGatherContextKey{}) == true,
		memory:               mobileBriefPinnedMemory(ctx),
	}
	if a != nil {
		in.ClockLayout = mobileBriefClockLayout(a.currentUILanguageName())
		in.Language = mobileBriefVoiceLang(a.currentUILanguageName())
		if slot == mobileBriefSlotMorning {
			last, ok := mobileBriefLastActive(a, asOf)
			in.RecapStart = mobileBriefMorningStart(asOf, windowStart, in.Location, last, ok)
		}
	}
	if a == nil {
		return in, errors.New("brief source has no app")
	}
	if err := ctx.Err(); err != nil {
		return in, err
	}
	collectMobileBriefUnderstanding(a, &in)
	collectMobileBriefWork(a, &in)
	collectMobileBriefSchedules(a, &in)
	collectMobileBriefSessions(a, &in)
	collectMobileBriefContext(a, &in)
	collectMobileBriefCalendar(a, &in)
	collectMobileBriefAttendance(a, &in)
	collectMobileBriefWatching(a, &in)
	collectMobileBriefDoing(a, &in)
	if err := collectMobileBriefPeople(a, &in); errors.Is(err, storage.ErrReadRevisionChanged) {
		return mobileBriefInput{}, errMobileBriefSourcesChanged
	}
	collectMobileBriefRecall(a, &in)
	collectMobileBriefDeliverables(a, &in)
	collectMobileBriefRecap(a, &in)
	collectMobileBriefOlderThreads(ctx, a, &in)
	collectMobileBriefWaitingReplies(ctx, a, &in)
	collectMobileBriefNews(a, &in)
	collectMobileBriefToday(a, &in)
	mobileBriefEndColdStart(&in)
	if err := ctx.Err(); err != nil {
		return mobileBriefInput{}, err
	}
	return in, nil
}

func (in mobileBriefInput) inWindow(at time.Time) bool {
	return !at.IsZero() && !at.Before(in.WindowStart) && at.Before(in.AsOf)
}

// collectMobileBriefUnderstanding adds the current Purposes as open loops and
// the patterns shadow learning supports as learned items, and decides whether
// the Mac is still in its cold start: no Self, no work, no automations and no
// Purpose yet.
func collectMobileBriefUnderstanding(a *App, in *mobileBriefInput) {
	if a.activity == nil || a.activity.store == nil {
		return
	}
	store := a.activity.store
	if purposeStore, err := a.purposeProductStore(); err == nil {
		details, err := purposeStore.ListDetailsAll()
		if err != nil {
			log.Printf("[mobile-brief] purposes unavailable: %v", err)
		}
		collectMobileBriefPurposes(a, in, details, nil)
	}
	patterns, err := store.learnedPatterns()
	if err != nil {
		log.Printf("[mobile-brief] learning unavailable: %v", err)
	}
	for _, pattern := range patterns {
		in.Learned = append(in.Learned, mobileBriefCandidate{
			SourceKind: "learning",
			SourceID:   pattern.ID,
			Title:      pattern.Statement,
			Detail:     pattern.Uncertainty,
			Status:     "supported",
			Kind:       "supported",
			At:         mobileBriefMillis(pattern.LastAt),
		})
	}
	selfEntries, err := store.listUsableSelfModelEntries()
	if err != nil {
		log.Printf("[mobile-brief] Self unavailable: %v", err)
		return
	}
	work := 0
	if a.handoffStore != nil {
		if items, err := a.handoffStore.List(); err == nil {
			work = len(items)
		}
	}
	in.History = a.GetAssistantHistory()
	in.ColdStart = len(selfEntries) == 0 && work == 0 &&
		mobileBriefUserAutomations(a.ListCronJobs()) == 0 && len(in.OpenLoops) == 0 &&
		len(in.WorkingToward) == 0 && len(in.Commitments) == 0 && len(in.WaitingOnYou) == 0
}

// mobileBriefUserAutomations counts the automations the user sees in
// Automations. The app's own Daily News job (surface "system") and the
// one-shot jobs behind one-time work (surface "handoff") are not the user's
// automations, so they do not end the cold start.
func mobileBriefUserAutomations(jobs []CronJobState) int {
	count := 0
	for _, job := range jobs {
		switch job.SurfaceKind {
		case todayNewsSurfaceKind, "handoff":
		default:
			count++
		}
	}
	return count
}

// collectMobileBriefWork adds the one-time work whose state changed in the
// window to the moved section.
func collectMobileBriefWork(a *App, in *mobileBriefInput) {
	if a.handoffStore == nil {
		return
	}
	items, err := a.handoffStore.List()
	if err != nil {
		log.Printf("[mobile-brief] one-time work unavailable: %v", err)
		return
	}
	for _, item := range items {
		if !a.mobileBriefHandoffReadable(context.Background(), item) {
			continue
		}
		detail := mobileBriefWorkDetail(item.Status)
		confidence, confidenceKnown := morningBriefWorkConfidence(item.Source)
		at := mobileBriefMillis(item.UpdatedAt)
		if detail == "" {
			continue
		}
		if mobileBriefWorkOpen(item.Status) && !at.After(in.AsOf) {
			in.OpenLoops = append(in.OpenLoops, mobileBriefCandidate{SourceKind: "history", SourceID: item.ID, Title: item.Title, Detail: detail, Status: string(item.Status), Kind: "one_time_work", At: at, LastTouch: at, OpenedAt: mobileBriefMillis(item.CreatedAt), confidence: confidence, confidenceKnown: confidenceKnown, rankedSourceDigest: mobileBriefModelDigest(item), rankedOriginDigest: morningBriefHandoffOriginDigest(item)})
		}
		if !in.inRecapWindow(at) {
			continue
		}
		in.Moved = append(in.Moved, mobileBriefCandidate{
			SourceKind:         "history",
			SourceID:           item.ID,
			Title:              item.Title,
			Detail:             detail,
			Status:             string(item.Status),
			Kind:               "one_time_work",
			At:                 at,
			OpenedAt:           mobileBriefMillis(item.CreatedAt),
			confidence:         confidence,
			confidenceKnown:    confidenceKnown,
			rankedSourceDigest: mobileBriefModelDigest(item),
			rankedOriginDigest: morningBriefHandoffOriginDigest(item),
		})
	}
}

// Only user-created tasks establish their title and workflow state directly.
// Other handoff origins may be inferred and keep confidence unknown.
func morningBriefWorkConfidence(source handoff.Source) (float64, bool) {
	switch source {
	case handoff.SourceMobile, handoff.SourceDesktop:
		return 1, true
	default:
		return 0, false
	}
}

// mobileBriefWorkOpen reports one-time work that is not finished.
func mobileBriefWorkOpen(status handoff.Status) bool {
	switch status {
	case handoff.StatusActive, handoff.StatusInProgress, handoff.StatusBlocked, handoff.StatusNeedsReview:
		return true
	default:
		return false
	}
}

// mobileBriefWorkDetail says where a piece of one-time work stands. Work that
// was dismissed or retired is not news, so it has no detail.
func mobileBriefWorkDetail(status handoff.Status) string {
	switch status {
	case handoff.StatusActive:
		return "Waiting to start."
	case handoff.StatusInProgress:
		return "Isoform is working on it."
	case handoff.StatusBlocked:
		return "Blocked, waiting for you."
	case handoff.StatusNeedsReview:
		return "Finished, ready for your review."
	case handoff.StatusCompleted:
		return "Done."
	default:
		return ""
	}
}

func collectMobileBriefSchedules(a *App, in *mobileBriefInput) {
	jobs := a.ListCronJobs()
	known := make(map[string]bool, len(jobs))
	hidden := map[string]bool{}
	for _, job := range jobs {
		known[job.ID] = true
		if job.SurfaceKind == todayNewsSurfaceKind && job.Name == todayNewsJobName {
			hidden[job.ID] = true
		}
	}
	runs, err := a.GetCronRunsByDateRange(in.WindowStart.UnixMilli(), in.AsOf.UnixMilli())
	if err != nil {
		log.Printf("[mobile-brief] schedule runs unavailable: %v", err)
		return
	}
	for _, run := range runs {
		if hidden[run.JobID] || (!known[run.JobID] && run.JobName == todayNewsJobName) {
			continue
		}
		// A missed time is not a run; Upcoming and the missed-run notice
		// say it instead.
		if run.Status == cronRunMissed {
			continue
		}
		finished := run.FinishedAt
		if finished <= 0 {
			finished = run.StartedAt
		}
		in.Schedules = append(in.Schedules, mobileBriefCandidate{
			SourceKind: "schedule_run",
			SourceID:   run.ID,
			Title:      mobileBriefFirstText(run.JobName, "automation"),
			Status:     run.Status,
			Kind:       run.TriggerKind,
			At:         mobileBriefMillis(finished),
		})
	}
}

func collectMobileBriefSessions(a *App, in *mobileBriefInput) {
	if a.activity == nil || a.activity.store == nil {
		return
	}
	store := a.activity.store
	loc := in.Location
	// Recorded calls are available in Library. Their existence is never a
	// Today/phone item; calendar meetings are collected independently.

	dayStart := in.briefDayRef(loc)
	dayRows, err := store.modelSessionsOverlapping(dayStart.UnixMilli(), in.AsOf.UnixMilli())
	if err != nil {
		log.Printf("[mobile-brief] day shape unavailable: %v", err)
		return
	}
	dayRows = a.filterActivityModelSessions(store, dayRows)
	presentMs, _, err := a.activityPresentTimeMs(context.Background(), store, dayRows, dayStart.UnixMilli(), in.AsOf.UnixMilli())
	if err != nil {
		log.Printf("[mobile-brief] day shape present time unavailable: %v", err)
		return
	}
	day := mobileBriefDayShape(dayRows, loc, dayStart, in.AsOf, presentMs)
	if day == nil {
		return
	}
	stats := a.computeAppBreakdown(
		store,
		dayStart.UnixMilli(), in.AsOf.UnixMilli(),
		int64(day.ActiveMinutes)*60000,
	)
	sort.SliceStable(stats, func(i, j int) bool {
		return stats[i].ActiveMs > stats[j].ActiveMs
	})
	for _, stat := range stats {
		if minutes := int(stat.ActiveMs / 60000); minutes > 0 {
			day.TopApps = append(day.TopApps, MobileBriefAppTime{
				Name:          stat.AppName,
				ActiveMinutes: minutes,
			})
		}
	}
	in.DayShape = day
}

func collectMobileBriefPeople(a *App, in *mobileBriefInput) error {
	people, err := a.ListPrincipalsWithStats()
	if err != nil {
		log.Printf("[mobile-brief] people unavailable: %v", err)
		return err
	}
	for _, person := range people {
		kind := person.Principal.Kind
		if kind == storage.PrincipalKindSelf || kind == storage.PrincipalKindAnonymous {
			continue
		}
		name := strings.TrimSpace(person.Principal.DisplayName)
		seenAt := mobileBriefMillis(person.Stats.LastSeenAt)
		if name == "" || !in.inWindow(seenAt) {
			continue
		}
		var apps []string
		for _, sighting := range person.Stats.ByApp {
			if in.inWindow(mobileBriefMillis(sighting.LastSeenAt)) {
				apps = append(apps, sighting.App)
			}
		}
		in.People = append(in.People, mobileBriefCandidate{
			SourceKind: "principal",
			SourceID:   person.Principal.ID,
			Title:      name,
			Status:     "seen",
			Kind:       kind,
			At:         seenAt,
			Apps:       apps,
			AppsShown:  true,
		})
	}
	return nil
}

// collectMobileBriefRecall adds memory and knowledge entries updated in the
// window to the learned section. Only titles and app names leave this function;
// entry content never does.
func collectMobileBriefRecall(a *App, in *mobileBriefInput) {
	allowRead, err := a.memoryReadFilter()
	if err != nil {
		log.Printf("[mobile-brief] recall privacy unavailable: %v", err)
		return
	}
	// A collection reads the memory folder as it began (the gather fence's
	// pinned snapshot), not the notes written while it runs.
	var memories []MemoryEntry
	if in.memory != nil {
		memories = in.memory.rawEntries()
	} else if memories, err = a.GetMemoryEntries(); err != nil {
		log.Printf("[mobile-brief] memory unavailable: %v", err)
	}
	for _, entry := range memories {
		updated := entry.UpdatedAt
		if updated == 0 {
			updated = entry.ModTime
		}
		at := mobileBriefMillis(updated)
		if !in.inWindow(at) {
			continue
		}
		in.Learned = append(in.Learned, mobileBriefCandidate{
			SourceKind: "memory",
			SourceID:   entry.ID,
			Title:      entry.Title,
			Status:     "updated",
			Kind:       "memory",
			At:         at,
			Apps:       entry.Apps,
			AppsShown:  true,
			Protected:  !allowRead(entry.ID, entry.Content),
		})
	}

	knowledge, err := a.GetKnowledgeEntries()
	if err != nil {
		log.Printf("[mobile-brief] knowledge unavailable: %v", err)
	}
	for _, entry := range knowledge {
		if doc, ok := parseOKFDocument(entry.Content); ok && okfString(doc.Frontmatter, "source") == meetingKnowledgeSource {
			continue
		}
		at := mobileBriefParseTime(entry.Date)
		if !in.inWindow(at) {
			continue
		}
		in.Learned = append(in.Learned, mobileBriefCandidate{
			SourceKind: "knowledge",
			SourceID:   entry.ID,
			Title:      entry.Title,
			Status:     "updated",
			Kind:       "knowledge",
			At:         at,
			Apps:       strings.Split(entry.Apps, ","),
			AppsShown:  true,
			Protected:  !allowRead(entry.ID, entry.Content),
		})
	}
}

func collectMobileBriefDeliverables(a *App, in *mobileBriefInput) {
	items, err := a.ListDeliverables()
	if err != nil {
		log.Printf("[mobile-brief] deliverables unavailable: %v", err)
		return
	}
	allowed := a.deliverableFolderReadFilter(context.Background())
	for _, item := range items {
		if !allowed(item) {
			continue
		}
		at := mobileBriefMillis(item.UpdatedAt)
		if !in.inWindow(at) {
			continue
		}
		in.Deliverables = append(in.Deliverables, mobileBriefCandidate{
			SourceKind: "deliverable",
			SourceID:   item.ID,
			Title:      item.Title,
			Status:     item.Status,
			Kind:       item.Source,
			At:         at,
		})
	}
}

func mobileBriefMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func mobileBriefParseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed
	}
	if ms := parseOKFTimestampMs(value); ms > 0 {
		return time.UnixMilli(ms)
	}
	return time.Time{}
}

func mobileBriefHubName() string {
	name, err := os.Hostname()
	if err != nil {
		return mobileBriefDefaultHub
	}
	name = strings.TrimSuffix(strings.TrimSpace(name), ".local")
	if strings.TrimSpace(name) == "" {
		return mobileBriefDefaultHub
	}
	return name
}

// mobileBriefTimezoneName returns an IANA name when one is available. time.Local
// reports only "Local", so the TZ variable and the /etc/localtime link are
// consulted before falling back to the zone abbreviation.
func mobileBriefTimezoneName(loc *time.Location) string {
	if loc == nil {
		return "UTC"
	}
	if name := loc.String(); name != "" && name != "Local" {
		return name
	}
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return tz
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		const marker = "zoneinfo/"
		if i := strings.Index(target, marker); i >= 0 {
			return target[i+len(marker):]
		}
	}
	zone, _ := time.Now().In(loc).Zone()
	return zone
}
