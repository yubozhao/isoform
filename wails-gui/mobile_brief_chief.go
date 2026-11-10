package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	mobileBriefSectionReview           = "review"
	mobileBriefSectionDecide           = "decide"
	mobileBriefSectionPlan             = "plan"
	mobileBriefSectionHighlights       = "highlights"
	mobileBriefSectionDayAhead         = "dayAhead"
	mobileBriefHighlightBytes          = 160
	mobileBriefChiefLimit              = 3
	mobileBriefCalendarLimit           = 2
	mobileBriefObservationReserveLimit = 24
)

// mobileBriefChiefInput keeps the source collectors and their privacy gates
// intact. Times remain in machine metadata, not the display text of past
// observations.
func mobileBriefChiefInput(in mobileBriefInput) mobileBriefInput {
	clean := func(items []mobileBriefCandidate) []mobileBriefCandidate {
		out := make([]mobileBriefCandidate, 0, len(items))
		for _, item := range items {
			// Browsing is excluded from the narrative. File titles are admitted
			// only through the filtered Today list.
			if item.Kind == "day_shape" {
				// The shape line names only time still ahead today
				// (mobileBriefDayShapeLine); its clocks are forward-looking.
			} else if item.SourceKind != mobileBriefCalendarSource || item.Status != "upcoming" || item.At.Before(in.AsOf) {
				item.Title = mobileBriefWithoutClockTimes(item.Title)
				item.Detail = mobileBriefWithoutClockTimes(item.Detail)
				item.NextStep = mobileBriefWithoutClockTimes(item.NextStep)
			} else {
				language := "en"
				if in.ClockLayout == "15:04" {
					language = "zh"
				}
				item.Title = normalizeBriefText(item.Title, language, in.AsOf, false)
				item.Detail = normalizeBriefText(item.Detail, language, in.AsOf, false)
				item.NextStep = normalizeBriefText(item.NextStep, language, in.AsOf, false)
			}
			out = append(out, item)
		}
		return out
	}
	in.Moved, in.Doing, in.Watching = clean(in.Moved), clean(in.Doing), clean(in.Watching)
	in.Meetings, in.People, in.OpenLoops = clean(in.Meetings), clean(in.People), clean(in.OpenLoops)
	in.Noticed = clean(in.Noticed)
	in.Learned, in.Deliverables, in.Schedules = clean(in.Learned), clean(in.Deliverables), clean(in.Schedules)
	in.WorkingToward, in.Commitments, in.WaitingOnYou = clean(in.WorkingToward), clean(in.Commitments), clean(in.WaitingOnYou)
	// An atomic Purpose card may exceed the remaining evidence budget while
	// its legacy title still fits. Carry its validated report marker before
	// caps so a stored or reloaded legacy row cannot repeat the chat reminder.
	reported := map[string]mobileBriefCandidate{}
	for _, item := range prepareMobileBriefPurposeCandidates(in.Commitments, mobileBriefPurposeFirst) {
		if item.SourceKind == "purpose" && !item.EvidenceUnavailable && item.ChatMessageID != "" {
			reported[mobileBriefSourceKey(item.SourceKind, item.SourceID)] = item
		}
	}
	for i, item := range in.OpenLoops {
		if item.Kind != "commitment" || item.SourceKind != "purpose" {
			continue
		}
		if card, ok := reported[mobileBriefSourceKey(item.SourceKind, item.SourceID)]; ok {
			in.OpenLoops[i].ChatMessageID = card.ChatMessageID
			in.OpenLoops[i].ChatSessionID = card.ChatSessionID
		}
	}
	return in
}

// Reserve the evidence needed for decisions and preparation before lower
// priority legacy lists spend the shared budget. Section() reuses these refs.
func reserveMobileBriefChiefEvidence(builder *mobileBriefBuilder, in mobileBriefInput) {
	reserve := func(items []mobileBriefCandidate, limit int) {
		for i, item := range items {
			if i >= limit || len(builder.evidence) >= mobileBriefObservationReserveLimit {
				break
			}
			// The content-free fallback for purged evidence has no source: it
			// never takes an evidence slot.
			if item.EvidenceUnavailable {
				continue
			}
			switch item.SourceKind {
			case mobileBriefCalendarSource, mobileBriefAttendanceSource, mobileBriefDayShapeSource, mobileBriefMeetingAppSource, mobileBriefWebSource:
				continue
			}
			if _, ok := builder.refFor(item); !ok {
				break
			}
		}
	}
	reserve(prepareMobileBriefCandidates(in.OpenLoops, mobileBriefOpenLoopFirst), mobileBriefItemsPerSection)
	var needsYou []mobileBriefCandidate
	for _, items := range [][]mobileBriefCandidate{in.Moved, in.Doing} {
		for _, item := range items {
			switch item.Status {
			case "needs_review", "blocked":
				needsYou = append(needsYou, item)
			}
		}
	}
	reserve(prepareMobileBriefCandidates(needsYou, mobileBriefNewestFirst), mobileBriefChiefLimit*2)
	for _, items := range [][]mobileBriefCandidate{in.Moved, in.Doing, in.People, in.Noticed, in.Learned} {
		reserve(prepareMobileBriefCandidates(items, mobileBriefNewestFirst), mobileBriefItemsPerSection)
	}
}

// Only a current, readable observation with an exact shared person or title
// anchors a calendar item. Carry its source ref with the meeting so the link
// is rechecked with the rest of the brief's provenance.
func mobileBriefObservationMeetings(in mobileBriefInput) mobileBriefInput {
	var observations []mobileBriefCandidate
	for _, group := range [][]mobileBriefCandidate{in.People, in.Doing, in.Deliverables, in.OpenLoops, in.Moved} {
		observations = append(observations, prepareMobileBriefCandidates(group, mobileBriefNewestFirst)...)
	}
	meetings := prepareMobileBriefCandidates(in.Meetings, mobileBriefUpcomingFirst)
	in.Meetings = nil
	calendarCount := 0
	for _, meeting := range meetings {
		if meeting.SourceKind != mobileBriefCalendarSource {
			in.Meetings = append(in.Meetings, meeting)
			continue
		}
		if calendarCount >= mobileBriefCalendarLimit || meeting.Status == "done" || meeting.Status == "upcoming" && meeting.At.Before(in.AsOf) {
			continue
		}
		calendarCount++
		for _, observed := range observations {
			if observed.SourceKind == mobileBriefCalendarSource || observed.SourceKind == mobileBriefAttendanceSource || observed.SourceKind == mobileBriefWebSource {
				continue
			}
			matched := false
			if observed.SourceKind == "principal" {
				for _, name := range meeting.rankedAttendees {
					matched = matched || strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(observed.Title))
				}
			} else if observed.SourceKind == mobileBriefFileSource || observed.SourceKind == "deliverable" {
				matched = strings.EqualFold(strings.TrimSpace(observed.Title), strings.TrimSpace(meeting.Title))
			}
			if matched && strings.TrimSpace(observed.Title) != "" {
				meeting.Evidence = append(meeting.Evidence, observed)
				break
			}
		}
		in.Meetings = append(in.Meetings, meeting)
	}
	return in
}

func mobileBriefReviewFirst(a, b mobileBriefCandidate) bool {
	priority := func(item mobileBriefCandidate) bool { return item.Status == "needs_review" || item.Status == "blocked" }
	if priority(a) != priority(b) {
		return priority(a)
	}
	aRank := mobileBriefHighlightPriority(MobileBriefItem{Kind: a.Kind, Status: a.Status})
	bRank := mobileBriefHighlightPriority(MobileBriefItem{Kind: b.Kind, Status: b.Status})
	if aRank != bRank {
		return aRank < bRank
	}
	return mobileBriefNewestFirst(a, b)
}

func mobileBriefUpcomingFirst(a, b mobileBriefCandidate) bool {
	if (a.Status == "upcoming") != (b.Status == "upcoming") {
		return a.Status == "upcoming"
	}
	if a.Status == "upcoming" && !a.At.Equal(b.At) {
		return a.At.Before(b.At)
	}
	return mobileBriefNewestFirst(a, b)
}

// mobileBriefChiefSections is a read-only projection of already-filtered
// evidence. It neither expands the evidence budget nor invents obligations
// from contact sightings. The Purpose Map owns richer purpose rows; their
// legacy open-loop aliases stay on the wire without repeating in these groups.
func mobileBriefChiefSections(brief MobileBriefV1) MobileBriefSections {
	s := brief.Sections
	s.Review, s.Decide, s.Plan, s.DayAhead, s.Highlights = nil, nil, nil, nil, nil
	byRef := make(map[string]MobileBriefRef, len(brief.Evidence))
	allowed := map[string]bool{}
	for _, ref := range mobileBriefNarrativeEvidenceForDocument(brief) {
		byRef[ref.Ref], allowed[ref.Ref] = ref, true
	}
	used := map[string]bool{}
	all := mobileBriefEvidenceByRef(brief)
	// The brief text speaks only of anchored items (mobile_brief_anchor.go);
	// what was only seen in activity stays under "What you worked on".
	place := func(section string, out *[]MobileBriefItem, item MobileBriefItem) {
		if len(*out) >= mobileBriefChiefLimit || len(item.Refs) == 0 || !mobileBriefItemNarratable(item, allowed) {
			return
		}
		ref := byRef[item.Refs[0]]
		if ref.SourceKind == mobileBriefAttendanceSource || ref.SourceKind == mobileBriefCalendarSource && !mobileBriefMeetingHasObservation(item, all) {
			return
		}
		identity := ref.SourceKind + ":" + ref.SourceID
		if used[identity] {
			return
		}
		used[identity] = true
		item.ID = section + "-" + strconv.Itoa(len(*out)+1)
		item.Key = mobileBriefItemKey(section, ref.SourceKind, ref.SourceID)
		*out = append(*out, item)
	}
	add := func(section string, out *[]MobileBriefItem, item MobileBriefItem) {
		if mobileBriefItemAnchor(item, all) != "" {
			place(section, out, item)
		}
	}
	openLoops := mobileBriefChiefOpenLoops(s)
	work := append(append(append([]MobileBriefItem{}, s.Moved...), s.Doing...), openLoops...)
	for _, item := range work {
		if item.Status == "needs_review" {
			add(mobileBriefSectionReview, &s.Review, item)
		}
	}
	for _, item := range work {
		switch item.Status {
		case "blocked", "waiting":
			add(mobileBriefSectionDecide, &s.Decide, item)
		}
	}
	for _, item := range openLoops {
		if item.Kind == "commitment" {
			add(mobileBriefSectionDecide, &s.Decide, item)
		}
	}
	upcoming := append([]MobileBriefItem{}, s.Meetings...)
	sort.SliceStable(upcoming, func(i, j int) bool { return upcoming[i].At < upcoming[j].At })
	asOf, _ := time.Parse(time.RFC3339, brief.AsOf)
	for _, item := range upcoming {
		at, err := time.Parse(time.RFC3339, item.At)
		if item.Status == "upcoming" && err == nil && !at.Before(asOf) && len(item.Refs) > 0 && mobileBriefItemNarratable(item, allowed) {
			if mobileBriefMeetingHasObservation(item, all) {
				add(mobileBriefSectionPlan, &s.Plan, item)
			}
			dayItem := item
			dayItem.ID = mobileBriefSectionDayAhead + "-" + strconv.Itoa(len(s.DayAhead)+1)
			ref := byRef[item.Refs[0]]
			dayItem.Key = mobileBriefItemKey(mobileBriefSectionDayAhead, ref.SourceKind, ref.SourceID)
			if len(s.DayAhead) < mobileBriefCalendarLimit {
				s.DayAhead = append(s.DayAhead, dayItem)
			}
		}
	}
	for _, item := range openLoops {
		add(mobileBriefSectionPlan, &s.Plan, item)
	}
	for _, item := range work {
		switch item.Status {
		case "active", "in_progress":
			add(mobileBriefSectionPlan, &s.Plan, item)
		}
	}
	// Select by importance before recency: recorded outcomes, decisions,
	// confirmed meeting signals, then observed progress. Never use browsing,
	// files, contact sightings or arbitrary item details as accomplishments.
	var highlights []MobileBriefItem
	for _, item := range append(append([]MobileBriefItem{}, s.Moved...), s.Deliverables...) {
		switch item.Status {
		case "active", "in_progress", "needs_review", "blocked", "waiting", "failed", "cancelled":
			continue
		}
		if mobileBriefItemAnchor(item, all) == "" {
			continue
		}
		highlights = append(highlights, item)
	}
	sort.SliceStable(highlights, func(i, j int) bool {
		a, b := mobileBriefHighlightPriority(highlights[i]), mobileBriefHighlightPriority(highlights[j])
		if a != b {
			return a < b
		}
		if highlights[i].At != highlights[j].At {
			return highlights[i].At > highlights[j].At
		}
		return highlights[i].Key < highlights[j].Key
	})
	for _, item := range highlights {
		item.Title = mobileBriefText(mobileBriefVoiceLabel(item.Title), mobileBriefHighlightBytes)
		if strings.TrimSpace(item.Title) == "" {
			continue
		}
		// Retain machine provenance, but no second line, preview or next step.
		// A retrospective line never asks for attention.
		item.Detail, item.NextStep, item.Context = "", "", nil
		item.Outcome = nil
		item.AttentionReason, item.AttentionRank = "", 0
		place(mobileBriefSectionHighlights, &s.Highlights, item)
	}

	return s
}

func mobileBriefMeetingHasObservation(item MobileBriefItem, evidence map[string]MobileBriefRef) bool {
	if len(item.Refs) == 0 {
		return false
	}
	for _, id := range item.Refs[1:] {
		switch evidence[id].SourceKind {
		case "principal", mobileBriefFileSource, "deliverable":
			return true
		}
	}
	return false
}

// The old-phone rows share their primary ref with the richer Purpose cards.
// Deduplicate by the primary Purpose ref before any chief projection so a
// reported reminder cannot return through Decide, Plan or the template. The
// richer WorkingToward row remains available for independent progress.
func mobileBriefChiefOpenLoops(sections MobileBriefSections) []MobileBriefItem {
	representedRefs, representedPurposes := map[string]bool{}, map[string]bool{}
	for _, items := range [][]MobileBriefItem{sections.WorkingToward, sections.Commitments, sections.WaitingOnYou} {
		for _, item := range items {
			if len(item.Refs) > 0 {
				representedRefs[item.Refs[0]] = true
			}
			if item.PurposeID != "" {
				representedPurposes[item.PurposeID] = true
			}
		}
	}
	commitmentRefs := map[string]bool{}
	for _, item := range sections.Commitments {
		if len(item.Refs) > 0 {
			commitmentRefs[item.Refs[0]] = true
		}
	}
	var out []MobileBriefItem
	for _, item := range sections.OpenLoops {
		commitment := item.Kind == "commitment" || item.Kind == "" && len(item.Refs) > 0 && commitmentRefs[item.Refs[0]]
		represented := representedPurposes[item.PurposeID] || len(item.Refs) > 0 && representedRefs[item.Refs[0]]
		if item.ChatMessageID != "" || commitment && represented {
			continue
		}
		out = append(out, item)
	}
	return out
}

func mobileBriefHighlightPriority(item MobileBriefItem) int {
	if item.Kind == mobileBriefThreadOutcomeSource {
		return 0
	}
	if item.Kind == "recap" {
		// Observation is not a completed outcome.
		return 3
	}
	if item.Kind == "decision" || item.Kind == "decision_recorded" {
		return 1
	}
	if item.Kind == mobileBriefAttendanceSource {
		return 2
	}
	switch item.Status {
	case "done", "completed", "verified", "reported", "fixed":
		return 0
	}
	if item.Kind == "initiative_completed" {
		return 0
	}
	return 3
}
