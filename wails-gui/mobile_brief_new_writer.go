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
	m := briefcore.Material{Slot: in.Slot, Larger: larger, Name: name, Now: now.Format("Monday, January 2, 3:04 PM")}
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
		switch days := mobileBriefCivilDays(at, in.AsOf, loc); {
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
		agent = agent || briefcore.AgentDriven(c.Apps, c.Title+" "+c.Detail+" "+c.NextStep)
		if agent && (group == briefcore.GroupDone || group == briefcore.GroupAttended) {
			group = briefcore.GroupAgents
		}
		when := dayWord(c.At)
		if group == briefcore.GroupAhead {
			if mobileBriefCivilDays(c.At, in.AsOf, loc) == 0 {
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
			if mobileBriefCivilDays(obs.Start, in.AsOf, loc) == 0 {
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
func newBriefReserve(brief *MobileBriefV1, src newBriefSource) (string, bool) {
	key := mobileBriefSourceKey(src.kind, src.id)
	used := map[string]bool{}
	for _, ref := range brief.Evidence {
		if mobileBriefSourceKey(ref.SourceKind, ref.SourceID) == key {
			return ref.Ref, true
		}
		used[ref.Ref] = true
	}
	if src.kind == mobileBriefAttendanceSource {
		// Attendance rows are cited only through the refs the brief built.
		return "", false
	}
	if src.kind == mobileBriefCalendarSource && mobileBriefEvidenceCounts(brief.Evidence)[mobileBriefCalendarSource] >= mobileBriefCalendarLimit {
		return "", false
	}
	label := mobileBriefText(src.label, mobileBriefLabelLimit)
	if label == "" || !mobileBriefCopySafe(label) || redactThreadOutcomeText(label) != label || len(brief.Evidence) >= mobileBriefEvidenceLimit {
		return "", false
	}
	if mobileBriefText(src.kind, mobileBriefKindLimit) != src.kind || mobileBriefText(src.id, mobileBriefSourceIDLimit) != src.id {
		return "", false
	}
	n := len(brief.Evidence) + 1
	ref := "e" + strconv.Itoa(n)
	for used[ref] {
		n++
		ref = "e" + strconv.Itoa(n)
	}
	brief.Evidence = append(brief.Evidence, MobileBriefRef{Ref: ref, SourceKind: src.kind, SourceID: src.id, Label: label, ObservedAt: brief.AsOf})
	return ref, true
}

// newBriefParagraphs maps the draft's material ids to evidence refs and labels
// each paragraph with its part. A ref that cannot be given evidence is dropped,
// and a paragraph left with none is dropped; both are named in the codes.
func newBriefParagraphs(brief *MobileBriefV1, draft briefcore.Draft, sources map[string]newBriefSource) ([]MobileBriefPara, []string) {
	var codes []string
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
				ref, ok := newBriefReserve(brief, src)
				if !ok {
					// The label may have failed a copy check, so the words that
					// rest on it go too.
					codes = append(codes, "paragraph_evidence_unavailable_"+src.kind)
					refs, dropped = nil, true
					break
				}
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
	return out, codes
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

// newBriefWithoutUnbound drops every paragraph that cites a ref whose source
// could not be bound: its words may still describe that source, and the reason
// may be a gate (protected, withdrawn, unreadable). The other paragraphs stay.
// Each drop is named by the source kind.
func newBriefWithoutUnbound(paragraphs []MobileBriefPara, unbound map[string]string) ([]MobileBriefPara, []string) {
	if len(unbound) == 0 {
		return paragraphs, nil
	}
	var codes []string
	var out []MobileBriefPara
	for _, p := range paragraphs {
		bad := ""
		for _, ref := range p.Refs {
			if kind, ok := unbound[ref]; ok {
				bad = kind
				break
			}
		}
		if bad != "" {
			codes = append(codes, "paragraph_unbound_"+bad)
			continue
		}
		out = append(out, p)
	}
	return out, codes
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
	raw, err := ask(ctx, briefcore.WritePrompt(material, lang))
	if err != nil {
		logLine("model_call:"+mobileBriefWriterErrorClass(err), nil, 0)
		return brief, nil, nil, err
	}
	draft, codes, err := briefcore.RepairDraft(raw, material)
	if err != nil {
		logLine("draft_unusable", codes, 0)
		return brief, nil, nil, err
	}
	brief.Evidence = append([]MobileBriefRef(nil), brief.Evidence...)
	paragraphs, mapCodes := newBriefParagraphs(&brief, draft, sources)
	codes = append(codes, mapCodes...)
	if len(paragraphs) == 0 {
		logLine("no_backed_paragraphs", codes, 0)
		return brief, nil, nil, errNewBriefNoMaterial
	}
	var binding *mobileBriefModelBinding
	if s.app != nil {
		var unbound map[string]string
		binding, unbound = s.app.captureNewBriefModelSources(ctx, brief, paragraphs, gathered)
		var bindCodes []string
		paragraphs, bindCodes = newBriefWithoutUnbound(paragraphs, unbound)
		codes = append(codes, bindCodes...)
		if binding == nil || len(paragraphs) == 0 {
			logLine("binding", codes, len(paragraphs))
			return brief, nil, nil, errNewBriefNoBinding
		}
	}
	logLine("ok", codes, len(paragraphs))
	brief.writerItems = len(material.Items)
	return brief, paragraphs, binding, nil
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
