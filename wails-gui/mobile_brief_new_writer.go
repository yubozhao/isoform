package main

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	briefcore "wails-gui/internal/brief"
)

// The new brief writer (rebuild step 4, newBrief setting). It replaces the
// ranked chain (scorer, lead, writer, judge, revisions) with one model call
// over everything the window holds, then a deterministic repair. The old path
// stays as the fallback when this one fails, and "newBrief": false in
// brief-schedule.json turns it off.

var (
	errNewBriefNoMaterial = errors.New("new brief: no material")
	errNewBriefNoBinding  = errors.New("new brief: sources could not be bound")
)

// newBriefSource is what a material id stands for: the source the brief cites.
type newBriefSource struct {
	kind, id, label string
	ref             string // an evidence ref the brief already has, if any
}

// newBriefOnRequestFloor is how far back an on-request brief reads when the
// previous brief is recent: the start of the local day. A brief asked for a
// minute after the last one still covers the day.
func newBriefOnRequestFloor(asOf time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	local := asOf.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

// newBriefMaterial groups the gathered input for the writer. Every list goes
// through the builder's candidate gate (prepareMobileBriefCandidates), so
// protected, sensitive and untitled records stay out, and the same exclusions
// the builder applied already ran on in. Nothing here reads a store.
func newBriefMaterial(in mobileBriefInput, brief MobileBriefV1, larger, name string) (briefcore.Material, map[string]newBriefSource) {
	loc := mobileBriefInputLocation(in)
	now := in.AsOf.In(loc)
	// Day words are the reader's: a brief made ahead is read on its own day,
	// so the evening it was made is "yesterday".
	dayRef := in.AsOf
	nowWords := now.Format("Monday, January 2, 3:04 PM")
	if !in.BriefDay.IsZero() {
		dayRef = in.briefDayRef(loc)
		nowWords = "the morning of " + dayRef.In(loc).Format("Monday, January 2") + " (the brief is read then)"
	}
	m := briefcore.Material{Slot: in.Slot, Larger: larger, Name: name, Now: nowWords}
	sources := map[string]newBriefSource{}
	existing := map[string]string{}
	for _, ref := range brief.Evidence {
		existing[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] = ref.Ref
	}
	since := in.WindowStart
	seen := map[string]bool{}
	dayWord := func(at time.Time) string {
		if at.IsZero() {
			return ""
		}
		switch days := mobileBriefCivilDays(at, dayRef, loc); {
		case days == 0:
			return "today"
		case days == -1:
			return "yesterday"
		case days < -1 && days > -7:
			return at.In(loc).Format("Monday")
		case days > 0:
			if days == 1 {
				return "tomorrow " + at.In(loc).Format("3:04 PM")
			}
			return at.In(loc).Format("Monday 3:04 PM")
		}
		return at.In(loc).Format("January 2")
	}
	// Preparing candidates for the brief drops the apps they were seen in (an
	// app name is not shown). The agent check needs them, so it reads them from
	// the candidates as gathered.
	appsOf := map[string][]string{}
	for _, list := range [][]mobileBriefCandidate{in.Moved, in.Deliverables, in.Conversations, in.Noticed, in.WaitingOnYou, in.Commitments, in.WorkingToward} {
		for _, c := range list {
			if len(c.Apps) > 0 {
				key := mobileBriefSourceKey(c.SourceKind, c.SourceID)
				appsOf[key] = append(appsOf[key], c.Apps...)
			}
		}
	}
	add := func(group string, c mobileBriefCandidate, agent bool) {
		if c.EvidenceUnavailable || c.ChatMessageID != "" || strings.TrimSpace(c.SourceID) == "" {
			return
		}
		switch c.SourceKind {
		case mobileBriefWebSource, mobileBriefFileSource, "chat_message", mobileBriefDayShapeSource, "brief", "brief_item":
			return
		}
		key := mobileBriefCandidateLoopKey(c)
		if key == "" {
			key = mobileBriefSourceKey(c.SourceKind, c.SourceID)
		}
		if seen[key] {
			return
		}
		if existing[mobileBriefSourceKey(c.SourceKind, c.SourceID)] == "" {
			// A source whose label cannot become evidence can never be cited:
			// keep it out, so a draft never rests its words on it.
			label := mobileBriefText(c.Title, mobileBriefLabelLimit)
			if c.EvidenceLabel != "" {
				label = mobileBriefText(c.EvidenceLabel, mobileBriefLabelLimit)
			}
			if label == "" || !mobileBriefCopySafe(label) || redactThreadOutcomeText(label) != label {
				return
			}
		}
		apps := c.Apps
		if len(apps) == 0 {
			apps = appsOf[mobileBriefSourceKey(c.SourceKind, c.SourceID)]
		}
		agent = agent || briefcore.AgentDriven(apps, c.Title+" "+c.Detail+" "+c.NextStep)
		if agent && (group == briefcore.GroupDone || group == briefcore.GroupAttended) {
			group = briefcore.GroupAgents
		}
		when := dayWord(c.At)
		if group == briefcore.GroupAhead {
			if mobileBriefCivilDays(c.At, dayRef, loc) == 0 {
				when = "today " + c.At.In(loc).Format("3:04 PM")
			}
		} else if !c.DueAt.IsZero() {
			when = "due " + dayWord(c.DueAt)
		}
		detail := c.Detail
		if c.NextStep != "" {
			detail = mobileBriefJoinParts(detail, "next step: "+c.NextStep)
		}
		if c.Person != "" {
			detail = mobileBriefJoinParts(detail, "with "+c.Person)
		}
		id := m.AddItem(briefcore.MaterialItem{
			Group: group, Title: c.Title, Detail: detail, Status: c.Status, When: when,
			Since: !c.At.IsZero() && !c.At.Before(since), Agent: agent,
		})
		if id == "" {
			return
		}
		seen[key] = true
		label := c.Title
		if c.EvidenceLabel != "" {
			label = c.EvidenceLabel
		}
		sources[id] = newBriefSource{kind: c.SourceKind, id: c.SourceID, label: label, ref: existing[mobileBriefSourceKey(c.SourceKind, c.SourceID)]}
	}
	addAll := func(group string, items []mobileBriefCandidate, less func(a, b mobileBriefCandidate) bool, agent bool) {
		for _, c := range prepareMobileBriefCandidates(items, less) {
			add(group, c, agent)
		}
	}
	// What is ahead and what waits on the user come first, so the forward
	// groups keep their room when a busy day fills the past groups.
	addAll(briefcore.GroupAhead, newBriefUpcoming(in.Meetings, in.AsOf), mobileBriefUpcomingFirst, false)
	addAll(briefcore.GroupAhead, newBriefUpcoming(in.TomorrowMeetings, in.AsOf), mobileBriefUpcomingFirst, false)
	// The day's other meetings are known through attendance. Upcoming ones
	// are ahead; finished ones the computer saw are attended.
	attendanceRefs := map[string]string{}
	for _, row := range brief.MeetingAttendance {
		if len(row.Refs) > 0 {
			attendanceRefs[row.EventID] = row.Refs[0]
		}
	}
	for _, obs := range in.Attendance {
		ref := attendanceRefs[obs.EventID]
		title := mobileBriefText(strings.TrimSpace(obs.Title), mobileBriefTextLimit)
		if ref == "" || title == "" || !mobileBriefCopySafe(title) || seen["attendance\x00"+obs.EventID] {
			continue
		}
		group, when := "", ""
		switch {
		case obs.Start.After(in.AsOf):
			group, when = briefcore.GroupAhead, dayWord(obs.Start)
			if mobileBriefCivilDays(obs.Start, dayRef, loc) == 0 {
				when = "today " + obs.Start.In(loc).Format("3:04 PM")
			}
		case obs.State == "attended":
			group, when = briefcore.GroupAttended, dayWord(obs.Start)
		default:
			continue
		}
		id := m.AddItem(briefcore.MaterialItem{Group: group, Title: title, When: when, Status: obs.State, Since: !obs.Start.Before(since)})
		if id == "" {
			continue
		}
		seen["attendance\x00"+obs.EventID] = true
		sources[id] = newBriefSource{kind: mobileBriefAttendanceSource, id: obs.EventID, label: title, ref: ref}
	}
	addAll(briefcore.GroupWaiting, in.WaitingOnYou, mobileBriefOldestTouchFirst, false)
	addAll(briefcore.GroupCommitment, in.Commitments, mobileBriefPurposeFirst, false)
	addAll(briefcore.GroupFollowUp, in.Noticed, mobileBriefNewestFirst, false)
	addAll(briefcore.GroupCommitment, in.WorkingToward, mobileBriefPurposeFirst, false)
	// Done: what moved and finished. Handoffs, deliverables and automation runs
	// are work Isoform's agents did, so they are agent items.
	for _, group := range [][]mobileBriefCandidate{in.Moved, in.Deliverables} {
		for _, c := range prepareMobileBriefCandidates(group, mobileBriefNewestFirst) {
			agent := c.SourceKind == "history" || c.SourceKind == "deliverable" || c.SourceKind == "schedule_run"
			add(briefcore.GroupDone, c, agent)
		}
	}
	// Conversations held: one that asks something of the user is a follow-up,
	// the rest are done.
	for _, c := range prepareMobileBriefCandidates(in.Conversations, mobileBriefNewestFirst) {
		group := briefcore.GroupDone
		if c.NextStep != "" {
			group = briefcore.GroupFollowUp
		}
		add(group, c, false)
	}
	addAll(briefcore.GroupAgents, in.Schedules, mobileBriefNewestFirst, true)
	addAll(briefcore.GroupOpenLoop, in.OpenLoops, mobileBriefOpenLoopFirst, false)
	addAll(briefcore.GroupContext, in.People, mobileBriefNewestFirst, false)
	// Learned habits are context only. A habit learned from agent sessions is
	// the agents' way of working, not the user's, and stays out.
	for _, c := range prepareMobileBriefCandidates(in.Learned, mobileBriefNewestFirst) {
		if briefcore.AgentCue(c.Title + " " + c.Detail) {
			continue
		}
		add(briefcore.GroupContext, c, false)
	}
	return m, sources
}

// newBriefUpcoming keeps the calendar rows that start after asOf.
func newBriefUpcoming(items []mobileBriefCandidate, asOf time.Time) []mobileBriefCandidate {
	var out []mobileBriefCandidate
	for _, c := range items {
		if c.SourceKind == mobileBriefCalendarSource && !c.At.IsZero() && c.At.After(asOf) {
			out = append(out, c)
		}
	}
	return out
}

// newBriefReserve gives a source an evidence ref, reusing the one the brief
// has. It follows the ranked path's rules: copy-safe label, the document's
// evidence limit and the calendar limit.
// newBriefCalendarLimit is how many calendar rows the written words may cite.
// The section builder keeps two for Day ahead; the words name more of the day.
const newBriefCalendarLimit = 10

// Reasons a source gets no evidence ref. A limit only drops the ref; a copy
// check drops the words that rest on it.
const (
	newBriefReserveOK    = ""
	newBriefReserveLimit = "limit"
	newBriefReserveCopy  = "copy"
)

func newBriefReserve(brief *MobileBriefV1, src newBriefSource) (string, bool) {
	ref, reason := newBriefReserveRef(brief, src)
	return ref, reason == newBriefReserveOK
}

func newBriefReserveRef(brief *MobileBriefV1, src newBriefSource) (string, string) {
	key := mobileBriefSourceKey(src.kind, src.id)
	used := map[string]bool{}
	for _, ref := range brief.Evidence {
		if mobileBriefSourceKey(ref.SourceKind, ref.SourceID) == key {
			return ref.Ref, newBriefReserveOK
		}
		used[ref.Ref] = true
	}
	if src.kind == mobileBriefCalendarSource {
		// The same event may already be cited through its attendance row.
		for _, ref := range brief.Evidence {
			if ref.SourceKind == mobileBriefAttendanceSource && ref.SourceID == src.id {
				return ref.Ref, newBriefReserveOK
			}
		}
	}
	if src.kind == mobileBriefAttendanceSource {
		// Attendance rows are cited only through the refs the brief built.
		return "", newBriefReserveLimit
	}
	if src.kind == mobileBriefCalendarSource && mobileBriefEvidenceCounts(brief.Evidence)[mobileBriefCalendarSource] >= newBriefCalendarLimit {
		return "", newBriefReserveLimit
	}
	if len(brief.Evidence) >= mobileBriefEvidenceLimit {
		return "", newBriefReserveLimit
	}
	label := mobileBriefText(src.label, mobileBriefLabelLimit)
	if label == "" || !mobileBriefCopySafe(label) || redactThreadOutcomeText(label) != label {
		return "", newBriefReserveCopy
	}
	if mobileBriefText(src.kind, mobileBriefKindLimit) != src.kind || mobileBriefText(src.id, mobileBriefSourceIDLimit) != src.id {
		return "", newBriefReserveCopy
	}
	n := len(brief.Evidence) + 1
	ref := "e" + strconv.Itoa(n)
	for used[ref] {
		n++
		ref = "e" + strconv.Itoa(n)
	}
	brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: ref, SourceKind: src.kind, SourceID: src.id, Label: label, ObservedAt: brief.AsOf})
	return ref, newBriefReserveOK
}

// newBriefParagraphs maps the draft's material ids to evidence refs and labels
// each paragraph with its part. A ref that cannot be given evidence is dropped,
// and a paragraph left with none is dropped; both are named in the codes.
func newBriefParagraphs(brief *MobileBriefV1, draft briefcore.Draft, sources map[string]newBriefSource) ([]MobileBriefPara, []string) {
	paragraphs, codes, _, _ := newBriefParagraphsTracked(brief, draft, sources)
	return paragraphs, codes
}

// newBriefParagraphsTracked is newBriefParagraphs that also returns the
// material ids that got no evidence and the material id behind each ref.
func newBriefParagraphsTracked(brief *MobileBriefV1, draft briefcore.Draft, sources map[string]newBriefSource) ([]MobileBriefPara, []string, map[string]bool, map[string]string) {
	var codes []string
	failed := map[string]bool{}
	refIDs := map[string]string{}
	unknownAttendance := map[string]bool{}
	for _, row := range brief.MeetingAttendance {
		if row.State == "unknown" {
			for _, ref := range row.Refs {
				unknownAttendance[ref] = true
			}
		}
	}
	var out []MobileBriefPara
	mapPart := func(part string, paragraphs []briefcore.Paragraph) {
		for _, p := range paragraphs {
			var refs []string
			dropped := false
			for _, id := range p.Refs {
				src, ok := sources[id]
				if !ok {
					codes = append(codes, "ref_unmapped")
					continue
				}
				ref, reason := newBriefReserveRef(brief, src)
				switch reason {
				case newBriefReserveLimit:
					// A budget, not a refusal: only this ref goes.
					codes = append(codes, "evidence_limit_"+src.kind)
					failed[id] = true
					continue
				case newBriefReserveCopy:
					// The label failed a copy check, so the words that rest on
					// it go too.
					codes = append(codes, "paragraph_evidence_unavailable_"+src.kind)
					failed[id] = true
					refs, dropped = nil, true
				}
				if dropped {
					break
				}
				refIDs[ref] = id
				// Hygiene drops an opening paragraph that cites an attendance
				// row the computer could not confirm, so the opening keeps
				// only its other refs.
				if len(out) == 0 && unknownAttendance[ref] {
					codes = append(codes, "opening_unknown_attendance")
					continue
				}
				if !containsString(refs, ref) {
					refs = append(refs, ref)
				}
			}
			if dropped {
				continue
			}
			if len(refs) == 0 {
				codes = append(codes, "paragraph_unbacked")
				continue
			}
			out = append(out, MobileBriefPara{Text: p.Text, Refs: refs, Part: part})
		}
	}
	mapPart(briefPartDone, draft.Done)
	mapPart(briefPartNext, draft.Next)
	return out, codes, failed, refIDs
}

// captureNewBriefModelSources binds the sources the written words rest on:
// every cited ref and the narratable refs the old binding covered (offers and
// cards cite those), inside the collection fence. A cited ref whose source
// cannot be bound is returned in unbound, so the caller drops it from the
// words instead of dropping the whole brief.
func (a *App) captureNewBriefModelSources(ctx context.Context, brief MobileBriefV1, paragraphs []MobileBriefPara, gathered *mobileBriefGatherRevision) (*mobileBriefModelBinding, map[string]string) {
	if a == nil || !a.validateMobileBriefGatherRevision(ctx, gathered) {
		return nil, nil
	}
	want := map[string]bool{}
	for _, p := range paragraphs {
		for _, ref := range p.Refs {
			want[ref] = true
		}
	}
	var refs []MobileBriefRef
	added := map[string]bool{}
	for _, ref := range brief.Evidence {
		if want[ref.Ref] && !added[ref.Ref] {
			refs = append(refs, ref)
			added[ref.Ref] = true
		}
	}
	for _, ref := range mobileBriefModelReadEvidence(brief) {
		if !added[ref.Ref] && len(refs) < mobileBriefEvidenceLimit {
			refs = append(refs, ref)
			added[ref.Ref] = true
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	sources := a.collectTodayChatSources(ctx, time.Now())
	_, weekly := a.yourWeekForBinding(brief)
	binding := &mobileBriefModelBinding{Version: 2, WeeklyRecap: mobileBriefModelDigest(weekly), Profile: a.mobileBriefModelProfileNotes(brief)}
	noticed := newMobileBriefNoticedLookup(a)
	unbound := map[string]string{}
	for _, ref := range refs {
		digest, ok := a.mobileBriefModelSourceDigestWithNoticed(ctx, ref, brief, sources, noticed)
		if !ok {
			if want[ref.Ref] {
				unbound[ref.Ref] = ref.SourceKind
			}
			continue
		}
		binding.Sources = append(binding.Sources, mobileBriefModelSource{Evidence: ref, Digest: digest})
	}
	if len(binding.Sources) == 0 || !a.validateMobileBriefGatherRevision(ctx, gathered) {
		return nil, nil
	}
	return binding, unbound
}

// newBriefWithoutUnbound drops the paragraphs that cite a ref whose source
// could not be bound: their words may still describe that source, and the
// reason may be a gate. A ref for which soft says yes (a calendar row, or a
// summary that still exists and passes the gates) only leaves its paragraph,
// which keeps its words while it has other refs. Each drop is named by kind.
func newBriefWithoutUnbound(paragraphs []MobileBriefPara, unbound map[string]string, soft func(ref, kind string) bool) ([]MobileBriefPara, []string) {
	if len(unbound) == 0 {
		return paragraphs, nil
	}
	var codes []string
	var out []MobileBriefPara
	for _, p := range paragraphs {
		hard := ""
		var refs []string
		for _, ref := range p.Refs {
			kind, bad := unbound[ref]
			switch {
			case !bad:
				refs = append(refs, ref)
			case soft != nil && soft(ref, kind):
				codes = append(codes, "ref_unbound_"+kind)
			default:
				hard = kind
			}
		}
		if hard != "" {
			codes = append(codes, "paragraph_unbound_"+hard)
			continue
		}
		if len(refs) == 0 {
			codes = append(codes, "paragraph_unbound_all")
			continue
		}
		p.Refs = refs
		out = append(out, p)
	}
	return out, codes
}

// newBriefSoftUnbound says whether an unbound ref may leave its paragraph
// without taking the words: a calendar row (its binding reads the day, which a
// brief made ahead cannot), or a summary that is still there and passes the
// privacy gates (not a conversation). Anything else is a refusal.
func (a *App) newBriefSoftUnbound(brief MobileBriefV1, conversations map[string]bool) func(ref, kind string) bool {
	byRef := mobileBriefEvidenceByRef(brief)
	return func(ref, kind string) bool {
		ev := byRef[ref]
		switch kind {
		case mobileBriefCalendarSource:
			return true
		case mobileBriefRecapSource:
			if a == nil || a.activity == nil || a.activity.store == nil || conversations[ev.SourceID] {
				return false
			}
			pauses, err := a.observationPauses()
			if err != nil {
				return false
			}
			reader := mobileBriefPurposeReader{app: a, store: a.activity.store, asOf: time.Now(), pauses: pauses, pausesReadable: true, cache: map[string]mobileBriefPurposeObservation{}}
			id := ev.SourceID
			if strings.HasPrefix(id, threadOutcomeMomentPrefix) {
				if at := strings.LastIndex(id, ":"); at > 0 {
					id = strings.TrimPrefix(id[:at], threadOutcomeMomentPrefix)
				}
			}
			row, err := a.activity.store.GetActivityByID(id)
			if err != nil || row == nil {
				return false
			}
			obs := reader.source("activity", id, 0, false)
			return !obs.Sensitive && !obs.Unavailable && len(a.filterComposerModelCandidatesContext(context.Background(), a.activity.store, []ActivityRow{*row})) == 1
		}
		return false
	}
}

// newBriefAsk is the one model call. Tests replace it.
type newBriefAsk func(ctx context.Context, prompt string) (string, error)

// composeNewBrief writes the narrative with one model call and returns the
// brief with its evidence for the cited sources, the paragraphs and their
// binding. Any error leaves the caller on the old path. It logs one line of
// counts and repair codes, never text.
func (s *mobileBriefService) composeNewBrief(ctx context.Context, input mobileBriefInput, brief MobileBriefV1, gathered *mobileBriefGatherRevision, ask newBriefAsk) (MobileBriefV1, []MobileBriefPara, *mobileBriefModelBinding, error) {
	start := time.Now()
	loc := mobileBriefInputLocation(input)
	balanceSlot := rankedBalanceSlot(brief.Slot, input.AsOf.In(loc))
	larger := rankedPartsBalance(balanceSlot)
	name := ""
	if s.app != nil {
		name = s.app.mobileBriefProfileFor(brief).Name
	}
	material, sources := newBriefMaterial(input, brief, larger, name)
	counts := map[string]int{}
	agents := 0
	for _, item := range material.Items {
		counts[item.Group]++
		if item.Agent {
			agents++
		}
	}
	logLine := func(outcome string, codes []string, paragraphs int) {
		log.Printf("[brief-new] slot=%s balance=%s conversations=%d items=%d done=%d attended=%d agents=%d agent_marked=%d ahead=%d commitment=%d waiting=%d follow_up=%d open_loop=%d context=%d outcome=%s paragraphs=%d codes=%s ms=%d",
			brief.Slot, larger, len(input.Conversations), len(material.Items), counts[briefcore.GroupDone], counts[briefcore.GroupAttended], counts[briefcore.GroupAgents], agents,
			counts[briefcore.GroupAhead], counts[briefcore.GroupCommitment], counts[briefcore.GroupWaiting], counts[briefcore.GroupFollowUp],
			counts[briefcore.GroupOpenLoop], counts[briefcore.GroupContext], outcome, paragraphs, briefcore.SummarizeCodes(codes), time.Since(start).Milliseconds())
	}
	if len(material.Items) == 0 {
		logLine("no_material", nil, 0)
		return brief, nil, nil, errNewBriefNoMaterial
	}
	lang := input.Language
	if lang == "" {
		lang = "en"
	}
	conversations := map[string]bool{}
	for _, c := range input.Conversations {
		conversations[c.SourceID] = true
	}
	original := brief
	var codes []string
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			codes = append(codes, "retry")
		}
		prompt := briefcore.WritePrompt(material, lang)
		if attempt > 0 {
			prompt += briefcore.RetryNote
		}
		raw, err := ask(ctx, prompt)
		if err != nil {
			logLine("model_call:"+mobileBriefWriterErrorClass(err), codes, 0)
			return original, nil, nil, err
		}
		draft, draftCodes, err := briefcore.RepairDraft(raw, material)
		codes = append(codes, draftCodes...)
		if err != nil {
			if attempt == 0 && ctx.Err() == nil {
				continue
			}
			logLine("draft_unusable", codes, 0)
			return original, nil, nil, err
		}
		brief = original
		brief.Evidence = append([]MobileBriefRef(nil), original.Evidence...)
		paragraphs, mapCodes, failed, refIDs := newBriefParagraphsTracked(&brief, draft, sources)
		codes = append(codes, mapCodes...)
		var binding *mobileBriefModelBinding
		if s.app != nil && len(paragraphs) > 0 {
			var unbound map[string]string
			binding, unbound = s.app.captureNewBriefModelSources(ctx, brief, paragraphs, gathered)
			var bindCodes []string
			paragraphs, bindCodes = newBriefWithoutUnbound(paragraphs, unbound, s.app.newBriefSoftUnbound(brief, conversations))
			codes = append(codes, bindCodes...)
			for ref := range unbound {
				failed[refIDs[ref]] = true
			}
			if binding == nil {
				paragraphs = nil
			}
		}
		// The same hygiene publication runs, so a draft it would empty is
		// written again here instead of leaving the slot quiet.
		if kept := newBriefHygieneKeeps(brief, paragraphs); kept < len(paragraphs) {
			codes = append(codes, "hygiene_drops="+strconv.Itoa(len(paragraphs)-kept))
			if kept == 0 {
				paragraphs = nil
			}
		}
		if len(paragraphs) > 0 && newBriefHasBothParts(paragraphs) || len(paragraphs) > 0 && attempt == 1 {
			logLine("ok", codes, len(paragraphs))
			brief.writerItems = len(material.Items)
			return brief, paragraphs, binding, nil
		}
		if attempt == 0 && ctx.Err() == nil {
			// Write once more without the items that could not be cited.
			var kept []briefcore.MaterialItem
			for _, item := range material.Items {
				if !failed[item.ID] {
					kept = append(kept, item)
				}
			}
			material.Items = kept
			continue
		}
		logLine("no_backed_paragraphs", codes, 0)
		return original, nil, nil, errNewBriefNoMaterial
	}
	logLine("no_backed_paragraphs", codes, 0)
	return original, nil, nil, errNewBriefNoMaterial
}

// newBriefHasBothParts reports a narrative with a done and a next paragraph.
func newBriefHasBothParts(paragraphs []MobileBriefPara) bool {
	done, next := false, false
	for _, p := range paragraphs {
		done = done || p.Part == briefPartDone
		next = next || p.Part == briefPartNext
	}
	return done && next
}

// newBriefHygieneKeeps counts the paragraphs the publication hygiene keeps.
func newBriefHygieneKeeps(brief MobileBriefV1, paragraphs []MobileBriefPara) int {
	if len(paragraphs) == 0 {
		return 0
	}
	brief.Narrative, brief.NarrativeStatus = append([]MobileBriefPara(nil), paragraphs...), "ok"
	return len(mobileBriefHygiene(brief).Narrative)
}

// newBriefEnabled reports the newBrief setting. A service without a config
// store (tests) uses the default.
func (s *mobileBriefService) newBriefEnabled() bool {
	if s == nil || s.config == nil {
		return defaultMobileBriefSettings().NewBrief
	}
	return s.config.load().NewBrief
}

// logBriefReady is the pre-flight line for a stored brief: which writer made
// the words, how many items it read, the part counts, the audio status and
// whether a reader is shown the words. Counts and states only.
func (s *mobileBriefService) logBriefReady(brief MobileBriefV1) {
	writer, items := "old", brief.writerItems
	if items > 0 {
		writer = "new"
	}
	audio, shown := "none", false
	done, next := 0, 0
	if s != nil && s.store != nil {
		if stored, ok := s.store.load(brief.BriefID); ok {
			brief = stored
			if stored.Audio != nil {
				audio = stored.Audio.Status
			}
			if read, _, err := s.finishedReadWithholding(stored); err == nil {
				shown = len(read.Narrative) > 0
			}
		}
	}
	for _, part := range brief.NarrativeParts {
		switch part.Kind {
		case briefPartDone:
			done = part.Count
		case briefPartNext:
			next = part.Count
		}
	}
	log.Printf("[brief-ready] slot=%s writer=%s items=%d parts=done:%d,next:%d audio=%s shown=%t status=%s",
		brief.Slot, writer, items, done, next, audio, shown, brief.NarrativeStatus)
}
