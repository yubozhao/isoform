package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The brief's picks (backlog item 65): Isoform decides what is worth saying
// before the model writes a word. At most six items, ranked, each with the
// facts needed to judge it and the step Isoform can take on it. The model
// only writes; it never chooses. What was only seen in activity (browsing,
// what the user typed into an AI tool, an AI's answer, a terminal) never
// becomes a pick: every observed thread stays out, and every pick carries an
// anchor (mobile_brief_anchor.go). Isoform's own learning notes, news and
// recaps stay out too. Checked Isoform noticed suggestions may fill otherwise
// unused places, below every other tier, without becoming obligations.
const (
	mobileBriefPickLimit = 6
	// With no pick the brief is the day ahead in two sentences; one pick is
	// said with the day.
	mobileBriefPickMinimum = 1
	// mobileBriefPickOverdueDays bounds how late a due item may be and still
	// be news; older deadlines are a goal check-in (item 66), not the brief.
	mobileBriefPickOverdueDays = 7
	// mobileBriefPickDueSoonDays is "due in the next three days".
	mobileBriefPickDueSoonDays = 3
	// mobileBriefPickFollowUpDays bounds how long a follow-up owed to a
	// person stays in the brief without a due date.
	mobileBriefPickFollowUpDays = 30
)

// The pick tiers, in rank order. The names appear in the brief's log line.
const (
	mobileBriefPickMeeting  = "meeting"
	mobileBriefPickDue      = "due"
	mobileBriefPickWaiting  = "waiting"
	mobileBriefPickFollowUp = "follow_up"
	mobileBriefPickDueSoon  = "due_soon"
	mobileBriefPickOwnWork  = "own_work"
	mobileBriefPickGoal     = "goal"
	mobileBriefPickNoticed  = "noticed"
	mobileBriefPickObserved = "observed"
)

var mobileBriefPickTiers = []string{
	mobileBriefPickDue, mobileBriefPickWaiting, mobileBriefPickFollowUp,
	mobileBriefPickDueSoon, mobileBriefPickOwnWork, mobileBriefPickGoal, mobileBriefPickMeeting, mobileBriefPickObserved, mobileBriefPickNoticed,
}

// mobileBriefPick is one thing worth saying today.
type mobileBriefPick struct {
	Tier string
	// Section is where the item sits in the brief, for its offer.
	Section string
	Item    MobileBriefItem
}

// mobileBriefPersonFollowUp reads a follow-up owed to a person from an item's
// title: the user promised to get back to someone.
var mobileBriefPersonFollowUp = regexp.MustCompile(`(?i)\b(?:follow[- ]?ups?|reply|respond|get back to|check in with|call|email|text|message|write to|thank)\b|跟进|回复|回信|答复|联系`)

// mobileBriefFollowUpLead is the "follow-up" word a follow-up title opens
// with ("follow-up Priya lamp: Halcyon Arc dimmer").
var mobileBriefFollowUpLead = regexp.MustCompile(`(?i)^\s*(?:follow[- ]?up|跟进)\s*(?:on|with|:)?\s*`)

// mobileBriefPickEmail drops addresses from a meeting's attendee line; the
// brief names people, never their addresses.
var mobileBriefPickEmail = regexp.MustCompile(`[^\s,;]+@[^\s,;]+`)

// mobileBriefPicks ranks what the brief is about: today's meetings that need
// something, what is due today or slipped this week, people waiting on the
// user, follow-ups owed to a person, what is due in the next three days,
// Isoform's own work that finished, stopped or needs an OK, and goals that
// moved since yesterday, then at most two checked suggestions if room remains.
// Each source item appears once, in its first tier.
func mobileBriefPicks(brief MobileBriefV1) []mobileBriefPick {
	reference, loc := mobileBriefTextReference(brief), mobileBriefNarrativeLocation(brief)
	allowed := map[string]bool{}
	for _, ref := range mobileBriefNarrativeEvidenceForDocument(brief) {
		allowed[ref.Ref] = true
	}
	evidence := mobileBriefEvidenceByRef(brief)
	eligible := func(item MobileBriefItem) bool {
		switch item.Kind {
		case mobileBriefThreadOutcomeSource, "recap", "memory", "news", "find", "day_shape":
			return false
		}
		return len(item.Refs) > 0 && !item.EvidenceUnavailable && item.ChatMessageID == "" && item.Outcome == nil &&
			mobileBriefItemNarratable(item, allowed) && mobileBriefItemAnchor(item, evidence) != "" && mobileBriefCardTitle(item) != ""
	}
	// A commitment main chat already reported is not said again; its goal
	// may still be news when it moved.
	reported := map[string]bool{}
	for _, item := range brief.Sections.Commitments {
		if item.ChatMessageID != "" && len(item.Refs) > 0 {
			ref := evidence[item.Refs[0]]
			reported[ref.SourceKind+"\x00"+ref.SourceID] = true
		}
	}
	byTier := map[string][]mobileBriefPick{}
	add := func(tier, section string, item MobileBriefItem) {
		byTier[tier] = append(byTier[tier], mobileBriefPick{Tier: tier, Section: section, Item: item})
	}

	for _, item := range mobileBriefDayItems(brief) {
		if eligible(item) && mobileBriefMeetingHasObservation(item, evidence) && mobileBriefMeetingNeedsSomething(item) {
			add(mobileBriefPickMeeting, mobileBriefSectionMeetings, item)
		}
	}
	s := brief.Sections
	for _, line := range []mobileBriefSectionLine{
		{mobileBriefSectionWaitingOnYou, s.WaitingOnYou},
		{mobileBriefSectionCommitments, s.Commitments},
		{mobileBriefSectionWorkingToward, s.WorkingToward},
		{mobileBriefSectionOpenLoops, s.OpenLoops},
		{mobileBriefSectionDoing, s.Doing},
		{mobileBriefSectionMoved, s.Moved},
		{mobileBriefSectionDeliverables, s.Deliverables},
		{mobileBriefSectionSchedules, s.Schedules},
	} {
		for _, item := range line.items {
			if !eligible(item) {
				continue
			}
			due := mobileBriefParseTime(item.DueAt)
			days := mobileBriefCivilDays(due, reference, loc)
			since := mobileBriefParseTime(item.Since)
			primary := evidence[item.Refs[0]]
			switch true {
			case reported[primary.SourceKind+"\x00"+primary.SourceID]:
				if line.name == mobileBriefSectionWorkingToward && mobileBriefGoalMoved(item, reference, loc) {
					add(mobileBriefPickGoal, line.name, item)
				}
			case !due.IsZero() && days <= 0 && days >= -mobileBriefPickOverdueDays:
				add(mobileBriefPickDue, line.name, item)
			case item.Kind == "waiting_on_reply" || line.name == mobileBriefSectionWaitingOnYou:
				add(mobileBriefPickWaiting, line.name, item)
			case !due.IsZero() && days > 0 && days <= mobileBriefPickDueSoonDays:
				add(mobileBriefPickDueSoon, line.name, item)
			case due.IsZero() && (item.Kind == "follow_up" || mobileBriefPersonFollowUp.MatchString(item.Title)) && !since.IsZero() &&
				mobileBriefCivilDays(since, reference, loc) >= -mobileBriefPickFollowUpDays:
				add(mobileBriefPickFollowUp, line.name, item)
			case mobileBriefOwnWorkNeedsUser(line.name, item, reference):
				add(mobileBriefPickOwnWork, line.name, item)
			case line.name == mobileBriefSectionWorkingToward && mobileBriefGoalMoved(item, reference, loc):
				add(mobileBriefPickGoal, line.name, item)
			}
		}
	}

	for _, line := range []mobileBriefSectionLine{{mobileBriefSectionDoing, s.Doing}, {mobileBriefSectionLearned, s.Learned}} {
		for _, item := range line.items {
			if len(item.Refs) == 0 || !mobileBriefItemNarratable(item, allowed) || item.EvidenceUnavailable || item.ChatMessageID != "" ||
				!mobileBriefCopySafe(item.Title) || redactThreadOutcomeText(item.Title) != item.Title || threadOutcomeTypingNarration.MatchString(item.Title) {
				continue
			}
			ref := evidence[item.Refs[0]]
			switch ref.SourceKind {
			case mobileBriefFileSource, "memory", "knowledge":
				if ref.SourceKind == mobileBriefFileSource && !strings.HasPrefix(ref.SourceID, "file:") {
					continue
				}
				// Only the already-filtered title is offered; no document content,
				// previews, app names or inferred obligations enter this pick.
				item.Detail, item.NextStep, item.Context, item.Outcome = "", "", nil, nil
				item.DueAt, item.Since = "", ""
				item.Refs = item.Refs[:1]
				add(mobileBriefPickObserved, line.name, item)
			}
		}
	}
	for _, item := range s.Noticed {
		if item.Kind == "noticed" && eligible(item) && len(byTier[mobileBriefPickNoticed]) < mobileBriefNoticedLimit {
			add(mobileBriefPickNoticed, mobileBriefSectionNoticed, item)
		}
	}

	oldest := func(picks []mobileBriefPick, at func(MobileBriefItem) time.Time) {
		sort.SliceStable(picks, func(i, j int) bool {
			a, b := at(picks[i].Item), at(picks[j].Item)
			if a.IsZero() != b.IsZero() {
				return !a.IsZero()
			}
			return a.Before(b)
		})
	}
	since := func(item MobileBriefItem) time.Time { return mobileBriefParseTime(item.Since) }
	due := func(item MobileBriefItem) time.Time { return mobileBriefParseTime(item.DueAt) }
	// A meeting on now leads; the rest follow the clock.
	sort.SliceStable(byTier[mobileBriefPickMeeting], func(i, j int) bool {
		a, b := byTier[mobileBriefPickMeeting][i].Item, byTier[mobileBriefPickMeeting][j].Item
		if (a.Status == "now") != (b.Status == "now") {
			return a.Status == "now"
		}
		return mobileBriefParseTime(a.At).Before(mobileBriefParseTime(b.At))
	})
	oldest(byTier[mobileBriefPickDue], due)
	oldest(byTier[mobileBriefPickWaiting], since)
	oldest(byTier[mobileBriefPickFollowUp], since)
	oldest(byTier[mobileBriefPickDueSoon], due)
	// Stopped work first, then work to review, failed runs, and what finished;
	// the oldest first within each.
	ownWorkOrder := func(p mobileBriefPick) int {
		switch true {
		case p.Item.Status == "blocked":
			return 0
		case p.Item.Status == "needs_review":
			return 1
		case p.Section == mobileBriefSectionSchedules:
			return 2
		}
		return 3
	}
	sort.SliceStable(byTier[mobileBriefPickOwnWork], func(i, j int) bool {
		a, b := byTier[mobileBriefPickOwnWork][i], byTier[mobileBriefPickOwnWork][j]
		if ownWorkOrder(a) != ownWorkOrder(b) {
			return ownWorkOrder(a) < ownWorkOrder(b)
		}
		return mobileBriefParseTime(a.Item.At).Before(mobileBriefParseTime(b.Item.At))
	})
	sort.SliceStable(byTier[mobileBriefPickGoal], func(i, j int) bool {
		return mobileBriefParseTime(byTier[mobileBriefPickGoal][i].Item.LastTouch).After(mobileBriefParseTime(byTier[mobileBriefPickGoal][j].Item.LastTouch))
	})

	var out []mobileBriefPick
	seen := map[string]bool{}
	for _, tier := range mobileBriefPickTiers {
		for _, pick := range byTier[tier] {
			ref := evidence[pick.Item.Refs[0]]
			identity := ref.SourceKind + "\x00" + ref.SourceID
			if seen[pick.Item.Refs[0]] || seen[identity] || len(out) == mobileBriefPickLimit {
				continue
			}
			seen[pick.Item.Refs[0]], seen[identity] = true, true
			out = append(out, pick)
		}
	}
	return out
}

// mobileBriefDayItems is the day the brief looks at, in time order: every
// calendar item on now or still ahead, today for a morning or on-request
// brief, and today's rest and tomorrow for an evening one.
func mobileBriefDayItems(brief MobileBriefV1) []MobileBriefItem {
	reference, loc := mobileBriefTextReference(brief), mobileBriefNarrativeLocation(brief)
	lastDay := 0
	if brief.Slot == mobileBriefSlotEvening {
		lastDay = 1
	}
	calendarRefs := map[string]bool{}
	for _, ref := range mobileBriefNarratableEvidence(brief.Evidence) {
		if ref.SourceKind == mobileBriefCalendarSource {
			calendarRefs[ref.Ref] = true
		}
	}
	var out []MobileBriefItem
	seen := map[string]bool{}
	for _, item := range brief.Sections.Meetings {
		at := mobileBriefParseTime(item.At)
		if item.Kind != "meeting" || len(item.Refs) == 0 || !calendarRefs[item.Refs[0]] || seen[item.Refs[0]] || at.IsZero() ||
			item.Status != "now" && item.Status != "upcoming" {
			continue
		}
		if days := mobileBriefCivilDays(at, reference, loc); item.Status == "upcoming" && (days < 0 || days > lastDay || at.Before(reference)) {
			continue
		}
		seen[item.Refs[0]] = true
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return mobileBriefParseTime(out[i].At).Before(mobileBriefParseTime(out[j].At))
	})
	return out
}

// mobileBriefMeetingNeedsSomething reports a meeting worth a sentence of its
// own: an invite not answered, or a call with other people (a join link, or
// a 1:1). A block of time on the user's own calendar (deep work, dinner, an
// appointment) belongs only to the day line.
func mobileBriefMeetingNeedsSomething(item MobileBriefItem) bool {
	detail := strings.ToLower(item.Detail)
	return strings.Contains(detail, "invite not answered") || strings.TrimSpace(item.URL) != "" ||
		strings.Contains(strings.ToLower(item.Title), "1:1")
}

// mobileBriefOwnWorkNeedsUser reports Isoform's own work the user should
// hear about: work stopped or waiting for their review, a deliverable that
// finished in the last day, or an automation run that failed.
func mobileBriefOwnWorkNeedsUser(section string, item MobileBriefItem, reference time.Time) bool {
	switch true {
	case item.Kind == "one_time_work":
		return item.Status == "blocked" || item.Status == "needs_review"
	case section == mobileBriefSectionDeliverables:
		at := mobileBriefParseTime(item.At)
		return !at.IsZero() && !at.After(reference) && reference.Sub(at) <= 24*time.Hour
	case section == mobileBriefSectionSchedules:
		return item.Status == "error" || item.Status == "failed" || item.Status == "timeout"
	}
	return false
}

// mobileBriefGoalMoved reports a goal with recorded progress since the start
// of yesterday. No recorded progress is not news.
func mobileBriefGoalMoved(item MobileBriefItem, reference time.Time, loc *time.Location) bool {
	last := mobileBriefParseTime(item.LastTouch)
	return !last.IsZero() && !last.After(reference) && mobileBriefCivilDays(last, reference, loc) >= -1
}

// mobileBriefPickFacts is what the model needs to judge a pick, in plain
// words: when a meeting is, who is in it and whether the invite is answered;
// how late or how old an item is; what Isoform's work needs. Clock times of
// the past and addresses never appear.
func mobileBriefPickFacts(brief MobileBriefV1, p mobileBriefPick, lang string) string {
	reference, loc := mobileBriefTextReference(brief), mobileBriefNarrativeLocation(brief)
	item := p.Item
	var facts []string
	switch p.Tier {
	case mobileBriefPickMeeting:
		if item.Status == "now" {
			facts = append(facts, pick(lang, "on now", "正在进行"))
		} else if when := mobileBriefWhen(item, reference, loc, lang); when != "" {
			facts = append(facts, when)
		}
		if detail := mobileBriefPickMeetingDetail(item.Detail); detail != "" {
			facts = append(facts, detail)
		}
	case mobileBriefPickWaiting:
		if since := mobileBriefSinceWords(mobileBriefParseTime(item.Since), reference, loc, lang); since != "" {
			facts = append(facts, pick(lang, "waiting on you "+since, since+"一直在等你"))
		} else {
			facts = append(facts, pick(lang, "waiting on you", "在等你"))
		}
	case mobileBriefPickOwnWork:
		switch true {
		case item.Status == "blocked":
			facts = append(facts, pick(lang, "stopped and waiting for you", "已暂停，在等你"))
		case item.Status == "needs_review":
			facts = append(facts, pick(lang, "ready for your review", "等你审阅"))
		case p.Section == mobileBriefSectionSchedules:
			facts = append(facts, pick(lang, "the last run failed", "上次运行失败"))
		default:
			facts = append(facts, pick(lang, "finished", "已完成"))
		}
	case mobileBriefPickNoticed:
		facts = append(facts, mobileBriefVoiceLabel(item.Detail))
	case mobileBriefPickGoal:
		facts = append(facts, pick(lang, "worked on since yesterday", "从昨天起有进展"))
	}
	if due := mobileBriefDueWords(mobileBriefParseTime(item.DueAt), reference, loc, lang); due != "" {
		facts = append(facts, pick(lang, "it "+due, due))
	}
	if p.Tier != mobileBriefPickWaiting && p.Tier != mobileBriefPickMeeting {
		if since := mobileBriefSinceWords(mobileBriefParseTime(item.Since), reference, loc, lang); since != "" {
			facts = append(facts, pick(lang, "open "+since, since+"一直未结"))
		}
	}
	if next := mobileBriefVoiceLabel(item.NextStep); next != "" && p.Tier != mobileBriefPickMeeting {
		facts = append(facts, pick(lang, "next step on the plan: ", "计划里的下一步：")+next)
	}
	return strings.Join(facts, "; ")
}

// mobileBriefPickReason is why a pick comes first, as one clause for the
// opener ("it is on now, and the invite is not answered yet").
func mobileBriefPickReason(brief MobileBriefV1, p mobileBriefPick, lang string) string {
	reference, loc := mobileBriefTextReference(brief), mobileBriefNarrativeLocation(brief)
	item := p.Item
	since := mobileBriefSinceWords(mobileBriefParseTime(item.Since), reference, loc, lang)
	due := mobileBriefDueWords(mobileBriefParseTime(item.DueAt), reference, loc, lang)
	switch p.Tier {
	case mobileBriefPickMeeting:
		reason := pick(lang, "it is on now", "它正在进行")
		if item.Status != "now" {
			when := mobileBriefWhen(item, reference, loc, lang)
			if when == "" {
				return ""
			}
			reason = pick(lang, "it is "+when, "它"+when+"开始")
		}
		if strings.Contains(strings.ToLower(item.Detail), "invite not answered") {
			reason += pick(lang, ", and the invite is not answered yet", "，邀请还没有回复")
		}
		return reason
	case mobileBriefPickDue, mobileBriefPickDueSoon:
		return pick(lang, "it "+due, due)
	case mobileBriefPickWaiting:
		if since == "" {
			return pick(lang, "they are waiting on you", "对方在等你")
		}
		return pick(lang, "they have been waiting "+since, since+"一直在等你")
	case mobileBriefPickFollowUp:
		return pick(lang, "it has been open "+since, since+"一直未结")
	case mobileBriefPickOwnWork:
		switch true {
		case item.Status == "blocked":
			return pick(lang, "it is stopped and waiting for you", "它已暂停，在等你")
		case item.Status == "needs_review":
			return pick(lang, "it is ready for your review", "它在等你审阅")
		case p.Section == mobileBriefSectionSchedules:
			return pick(lang, "its last run failed", "它上次运行失败")
		}
		return pick(lang, "it is finished", "它已完成")
	case mobileBriefPickGoal:
		return pick(lang, "it moved since yesterday", "它从昨天起有进展")
	}
	return ""
}

// mobileBriefPickMeetingDetail is a meeting's place, people and invite state
// without its clock times or anyone's address.
func mobileBriefPickMeetingDetail(detail string) string {
	var parts []string
	for _, part := range strings.Split(detail, ";") {
		part = mobileBriefPickEmail.ReplaceAllString(part, "")
		part = strings.Trim(strings.Join(strings.Fields(part), " "), " ,")
		part = strings.ReplaceAll(part, ", ,", ",")
		if part = mobileBriefVoiceLabel(part); part != "" && part != "+" && !strings.HasPrefix(strings.ToLower(part), "http") {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "; ")
}

// mobileBriefPickCounts is the log line's tally: picks by tier, in rank
// order, never their text.
// mobileBriefLogSummary is the one log line a stored brief gets: its picks by tier,
// who wrote the words and how many sentences they are. It carries no words (design R74 v1.0.0).
func mobileBriefLogSummary(brief MobileBriefV1) string {
	writer := "none"
	switch {
	case brief.NarrativeStatus == "ok":
		writer = "model"
	case len(brief.Narrative) > 0:
		writer = "template"
	}
	return fmt.Sprintf("[mobile-brief] stored %s brief: %s writer=%s sentences=%d",
		brief.Slot, mobileBriefPickCounts(mobileBriefPicks(brief)), writer, mobileBriefSentenceCount(brief.Narrative))
}

// mobileBriefSentenceCount counts sentence ends: a full stop, question or
// exclamation mark followed by a space or the end, or a Chinese full stop, question or exclamation mark.
var mobileBriefSentenceMark = regexp.MustCompile(`[.!?](?:\s|$)|[。！？]`)

func mobileBriefSentenceCount(paragraphs []MobileBriefPara) int {
	n := 0
	for _, paragraph := range paragraphs {
		n += len(mobileBriefSentenceMark.FindAllStringIndex(paragraph.Text, -1))
	}
	return n
}

func mobileBriefPickCounts(picks []mobileBriefPick) string {
	counts := map[string]int{}
	for _, pick := range picks {
		counts[pick.Tier]++
	}
	parts := []string{fmt.Sprintf("picks=%d", len(picks))}
	for _, tier := range mobileBriefPickTiers {
		parts = append(parts, fmt.Sprintf("%s=%d", tier, counts[tier]))
	}
	return strings.Join(parts, " ")
}

// mobileBriefPickRefs are the refs the written brief may cite: its picks and
// its day.
func mobileBriefPickRefs(brief MobileBriefV1, picks []mobileBriefPick) map[string]bool {
	refs := map[string]bool{}
	for _, pick := range picks {
		for _, ref := range pick.Item.Refs {
			refs[ref] = true
		}
	}
	for _, item := range mobileBriefDayItems(brief) {
		for _, ref := range item.Refs {
			refs[ref] = true
		}
	}
	return refs
}

// mobileBriefPickClocks are the clock times each ref may be written with:
// a calendar item still ahead, at its own start time as the day line gives
// it. A meeting already on has none, so no past clock time is ever said.
func mobileBriefPickClocks(brief MobileBriefV1, languageName string) map[string]map[string]bool {
	clocks := mobileBriefFutureClocks(brief, languageName)
	loc := mobileBriefNarrativeLocation(brief)
	clock := mobileBriefClockLayout(languageName)
	for _, item := range mobileBriefDayItems(brief) {
		if item.Status != "upcoming" {
			continue
		}
		for _, ref := range item.Refs {
			if clocks[ref] == nil {
				clocks[ref] = map[string]bool{}
			}
			for _, token := range mobileBriefDisplayClock.FindAllString(mobileBriefLocalTime(item.At, loc, clock), -1) {
				clocks[ref][strings.TrimSpace(token)] = true
			}
		}
	}
	return clocks
}
