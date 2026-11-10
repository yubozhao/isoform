package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Local-only provenance for frozen prose. A wire revision is an identity, not
// proof that the sources are still authorized or contain the original facts.
// Version 2 requires an unchanged fence starting before source collection;
// version 1 could bind a later correction to earlier collected text.
type mobileBriefModelBinding struct {
	Version     int                      `json:"version"`
	Synthesis   string                   `json:"synthesis"`
	WeeklyRecap string                   `json:"weeklyRecap"`
	Sources     []mobileBriefModelSource `json:"sources"`
	// Profile is the About-you notes the prompt read (backlog item 65): a
	// note deleted, withheld from model reads or retitled since withdraws
	// the text like any other input.
	Profile []mobileBriefModelProfileNote `json:"profile,omitempty"`
	// YesterdayActivity is local-only provenance for any topic-time input.
	YesterdayActivity  *yourWeekSnapshot         `json:"yesterdayActivity,omitempty"`
	YesterdayDigest    string                    `json:"yesterdayDigest,omitempty"`
	RankedInputs       []morningBriefBoundInput  `json:"rankedInputs,omitempty"`
	RankedInputsDigest string                    `json:"rankedInputsDigest,omitempty"`
	RankedLedgerProofs []morningBriefLedgerProof `json:"rankedLedgerProofs,omitempty"`
	RankedLedgerDigest string                    `json:"rankedLedgerDigest,omitempty"`
	WeekIntentDigest   string                    `json:"weekIntentDigest,omitempty"`
}

type mobileBriefModelProfileNote struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// mobileBriefModelProfileNotes are the About-you notes the brief's prompt
// reads now, as the binding records them.
func (a *App) mobileBriefModelProfileNotes(brief MobileBriefV1) []mobileBriefModelProfileNote {
	profile := a.mobileBriefProfileFor(brief)
	out := make([]mobileBriefModelProfileNote, 0, len(profile.NoteIDs))
	for i, id := range profile.NoteIDs {
		out = append(out, mobileBriefModelProfileNote{ID: id, Digest: mobileBriefModelDigest(profile.Notes[i])})
	}
	return out
}

// mobileBriefProfileNotesReadable reports whether every About-you note the
// text was written with is still there, still readable by a model under
// allowRead, and says what the prompt said.
func (a *App) mobileBriefProfileNotesReadable(notes []mobileBriefModelProfileNote, allowRead func(string, string) bool) bool {
	for _, note := range notes {
		entry, err := a.GetMemoryEntry(note.ID)
		if err != nil || allowRead == nil || !allowRead(note.ID, entry.Content) || mobileBriefModelDigest(mobileBriefProfileNoteTitle(entry)) != note.Digest {
			return false
		}
	}
	return true
}

// mobileBriefModelReadEvidence is every evidence ref the brief's model may
// read: the picks and the day the prompt carries (backlog item 65), and the
// narratable sections its buttons come from.
func mobileBriefModelReadEvidence(brief MobileBriefV1) []MobileBriefRef {
	read := mobileBriefPickRefs(brief, mobileBriefPicks(brief))
	for _, ref := range mobileBriefNarrativeInputEvidence(brief) {
		read[ref.Ref] = true
	}
	var out []MobileBriefRef
	for _, ref := range mobileBriefNarrativeEvidenceForDocument(brief) {
		if read[ref.Ref] {
			out = append(out, ref)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return !mobileBriefCalendarShaped(out[i].SourceKind) && mobileBriefCalendarShaped(out[j].SourceKind)
	})
	return out
}

func mobileBriefCalendarShaped(kind string) bool {
	switch kind {
	case mobileBriefCalendarSource, mobileBriefAttendanceSource, mobileBriefDayShapeSource, mobileBriefMeetingAppSource:
		return true
	default:
		return false
	}
}

// Counts only: this local generation diagnostic never contains source content
// or changes the phone document.
type mobileBriefGenerationDebug struct {
	EvidenceByKind      map[string]int `json:"evidenceByKind"`
	ReservedObservation int            `json:"reservedObservation"`
	ReservedCalendar    int            `json:"reservedCalendar"`
}

func mobileBriefEvidenceCounts(refs []MobileBriefRef) map[string]int {
	counts := map[string]int{}
	for _, ref := range refs {
		kind := ref.SourceKind
		switch kind {
		case "activity", "activity_noticed", "activity_outcome", "activity_run", "activity_session", "activity_summary", "activity_timeline_group", "brief_waiting_reply", "calendar", "calendar_day", "chat_message", "deliverable", "engagement_moment", "file", "handoff", "history", "initiative", "knowledge", "knowledge_markdown", "learning", "mail", "manual", "meeting", "meeting_attendance", "meeting_transcript", "memory", "memory_markdown", "news", "principal", "purpose", "schedule", "schedule_run", "self_model_entry", "session", "summary", "thread_outcome", "watching", "web":
		default:
			kind = "other"
		}
		counts[kind]++
	}
	return counts
}

func mobileBriefGenerationCounts(refs []MobileBriefRef, reserved map[string]int) *mobileBriefGenerationDebug {
	debug := &mobileBriefGenerationDebug{EvidenceByKind: mobileBriefEvidenceCounts(refs)}
	for kind, count := range reserved {
		if mobileBriefCalendarShaped(kind) {
			debug.ReservedCalendar += count
		} else {
			debug.ReservedObservation += count
		}
	}
	return debug
}

type mobileBriefModelSource struct {
	Evidence MobileBriefRef `json:"evidence"`
	Digest   string         `json:"digest"`
}

func mobileBriefModelDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return agencyPayloadHash(raw)
}

// Hash prose, offers and their source labels using source identities rather
// than display ref numbers. Today may renumber citations during its projection.
func mobileBriefSynthesisDigest(brief MobileBriefV1) string {
	byRef := map[string]MobileBriefRef{}
	for _, ref := range brief.Evidence {
		byRef[ref.Ref] = ref
	}
	refs := func(ids []string) []string {
		var result []string
		for _, id := range ids {
			ref, ok := byRef[id]
			if !ok {
				return nil
			}
			result = append(result, mobileBriefModelDigest(struct{ Kind, ID, Label string }{ref.SourceKind, ref.SourceID, ref.Label}))
		}
		sort.Strings(result)
		return result
	}
	var paras []MobileBriefPara
	for _, paragraph := range brief.Narrative {
		if mobileBriefIsOpenAnswerParagraph(brief, paragraph) {
			continue
		}
		paras = append(paras, paragraph)
	}
	for i := range paras {
		// Registry navigation is revalidated independently of frozen prose.
		paras[i].TopicID, paras[i].TopicName = "", ""
		paras[i].Refs = refs(paras[i].Refs)
		if len(paras[i].Refs) == 0 {
			return ""
		}
	}
	offers := append([]MobileBriefOffer(nil), brief.Offers...)
	for i := range offers {
		offers[i].Refs = refs(offers[i].Refs)
		if len(offers[i].Refs) == 0 {
			return ""
		}
	}
	return mobileBriefModelDigest(struct {
		ID, AsOf, Slot, Timezone string
		Narrative                []MobileBriefPara
		Offers                   []MobileBriefOffer
	}{brief.BriefID, brief.AsOf, brief.Slot, brief.Timezone, paras, offers})
}

// Capture every input, including uncited inputs, inside the fence that began
// before gathering. Never certify earlier text using only a later source read.
func (a *App) captureMobileBriefModelSources(ctx context.Context, brief MobileBriefV1, gathered *mobileBriefGatherRevision) *mobileBriefModelBinding {
	if !a.validateMobileBriefGatherRevision(ctx, gathered) {
		return nil
	}
	refs := mobileBriefModelReadEvidence(brief)
	if len(refs) == 0 || len(refs) > mobileBriefEvidenceLimit {
		return nil
	}
	sources := a.collectTodayChatSources(ctx, time.Now())
	_, weekly := a.yourWeekForBinding(brief)
	binding := &mobileBriefModelBinding{Version: 2, WeeklyRecap: mobileBriefModelDigest(weekly), Profile: a.mobileBriefModelProfileNotes(brief)}
	noticed := newMobileBriefNoticedLookup(a)
	for _, ref := range refs {
		digest, ok := a.mobileBriefModelSourceDigestWithNoticed(ctx, ref, brief, sources, noticed)
		if !ok {
			return nil
		}
		binding.Sources = append(binding.Sources, mobileBriefModelSource{Evidence: ref, Digest: digest})
	}
	if !a.validateMobileBriefGatherRevision(ctx, gathered) {
		return nil
	}
	return binding
}

// captureRankedBriefModelSources binds every admitted scorer input inside the
// collection fence, including items the scorer later omits. The caller binds
// any additional profile, history or activity context before model use.
func (a *App) captureRankedBriefModelSources(ctx context.Context, brief MobileBriefV1, gathered *mobileBriefGatherRevision, inputs []morningBriefBoundInput) *mobileBriefModelBinding {
	return a.captureRankedBriefModelSourcesWithLedger(ctx, brief, gathered, inputs, nil, nil)
}

func (a *App) captureRankedBriefModelSourcesWithLedger(ctx context.Context, brief MobileBriefV1, gathered *mobileBriefGatherRevision, inputs []morningBriefBoundInput, store *mobileBriefStore, proofs []morningBriefLedgerProof) *mobileBriefModelBinding {
	if !brief.rankedEdition || len(inputs) == 0 || !a.validateMobileBriefGatherRevision(ctx, gathered) ||
		!a.morningBriefRankedInputsReadableWithLedger(ctx, brief, inputs, store, proofs) {
		return nil
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return nil
	}
	var frozen []morningBriefBoundInput
	if json.Unmarshal(raw, &frozen) != nil || !a.morningBriefRankedInputsReadableWithLedger(ctx, brief, frozen, store, proofs) {
		return nil
	}
	_, weekly := a.yourWeekForBinding(brief)
	binding := &mobileBriefModelBinding{Version: 2, WeeklyRecap: mobileBriefModelDigest(weekly), RankedInputs: frozen, RankedInputsDigest: mobileBriefModelDigest(frozen),
		RankedLedgerProofs: append([]morningBriefLedgerProof(nil), proofs...)}
	if len(binding.RankedLedgerProofs) > 0 {
		binding.RankedLedgerDigest = mobileBriefModelDigest(binding.RankedLedgerProofs)
	}
	if !a.validateMobileBriefGatherRevision(ctx, gathered) {
		return nil
	}
	return binding
}

// The ranked manifest is the exact candidate snapshot used by the scorer and
// is also the future diff ledger. Its digest prevents later edits from being
// mistaken for facts the model actually read.
func mobileBriefRankedManifestValid(brief MobileBriefV1, binding *mobileBriefModelBinding) bool {
	if !brief.rankedEdition {
		return true
	}
	return binding != nil && len(binding.RankedInputs) > 0 && binding.RankedInputsDigest != "" &&
		binding.RankedInputsDigest == mobileBriefModelDigest(binding.RankedInputs)
}

func mobileBriefRankedLedgerValid(brief MobileBriefV1, binding *mobileBriefModelBinding) bool {
	if !brief.rankedEdition {
		return true
	}
	if binding == nil {
		return false
	}
	if len(binding.RankedLedgerProofs) == 0 {
		return binding.RankedLedgerDigest == ""
	}
	return binding.RankedLedgerDigest != "" && binding.RankedLedgerDigest == mobileBriefModelDigest(binding.RankedLedgerProofs)
}

func mobileBriefBindingHasSources(brief MobileBriefV1, binding *mobileBriefModelBinding) bool {
	if binding == nil || binding.Version != 2 || len(binding.Sources) > mobileBriefEvidenceLimit {
		return false
	}
	if brief.rankedEdition {
		return len(binding.RankedInputs) > 0
	}
	return len(binding.Sources) > 0
}

func (a *App) validateMobileBriefModelSources(ctx context.Context, brief MobileBriefV1, binding *mobileBriefModelBinding) bool {
	if a == nil || a.IsClosedEyesModeEnabled() || ctx.Err() != nil || !mobileBriefBindingHasSources(brief, binding) {
		return false
	}
	return a.validateMobileBriefModelSourcesFrom(ctx, brief, binding, a.collectTodayChatSources(ctx, time.Now()))
}

func (a *App) validateMobileBriefModelSourcesFrom(ctx context.Context, brief MobileBriefV1, binding *mobileBriefModelBinding, sources todayChatSources) bool {
	if a == nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) || ctx.Err() != nil || !mobileBriefBindingHasSources(brief, binding) || !mobileBriefRankedManifestValid(brief, binding) || !mobileBriefRankedLedgerValid(brief, binding) {
		return false
	}
	if len(binding.RankedInputs) > 0 {
		readable := false
		if brief.rankedEdition {
			var store *mobileBriefStore
			if len(binding.RankedLedgerProofs) > 0 {
				store = a.morningBriefLedgerStore()
			}
			readable = a.morningBriefRankedInputsReadableWithLedger(ctx, brief, binding.RankedInputs, store, binding.RankedLedgerProofs)
		} else {
			readable = a.morningBriefRankedInputsReadable(ctx, mobileBriefAsOfTime(brief), binding.RankedInputs)
		}
		if !readable {
			return false
		}
	}
	// "Your week" paragraphs use their own frozen identities and current
	// gates, rather than the daily evidence-ref table. Bind their exact
	// projection too; a new read cannot bless changed weekly prose.
	_, weekly := a.yourWeekForDeliveryWithLeadContext(ctx, brief, false)
	if binding.WeeklyRecap != mobileBriefModelDigest(weekly) {
		return false
	}
	if binding.YesterdayActivity != nil && !a.mobileBriefYesterdayBindingReadable(ctx, brief, binding) {
		return false
	}
	if !a.morningBriefWeekIntentBindingReadable(ctx, brief, binding) {
		return false
	}
	allowMemory, err := a.memoryReadFilterContext(ctx)
	if err != nil || !a.mobileBriefProfileNotesReadable(binding.Profile, allowMemory) {
		return false
	}
	known := map[string]bool{}
	noticed := newMobileBriefNoticedLookup(a)
	for _, source := range binding.Sources {
		key := mobileBriefSourceKey(source.Evidence.SourceKind, source.Evidence.SourceID)
		if known[key] || source.Digest == "" {
			return false
		}
		known[key] = true
		digest, ok := a.mobileBriefModelSourceDigestWithNoticed(ctx, source.Evidence, brief, sources, noticed)
		if !ok || digest != source.Digest {
			return false
		}
	}
	if brief.rankedEdition {
		for _, item := range binding.RankedInputs {
			known[mobileBriefSourceKey(item.Kind, item.ID)] = true
			for _, dependency := range item.Dependencies {
				known[mobileBriefSourceKey(dependency.Kind, dependency.ID)] = true
			}
		}
	}
	for _, paragraph := range brief.Narrative {
		if mobileBriefIsOpenAnswerParagraph(brief, paragraph) {
			continue
		}
		for _, id := range paragraph.Refs {
			found := false
			for _, ref := range brief.Evidence {
				if ref.Ref == id && known[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	for _, offer := range brief.Offers {
		for _, id := range offer.Refs {
			found := false
			for _, ref := range brief.Evidence {
				if ref.Ref == id && known[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return ctx.Err() == nil
}

func (a *App) finishMobileBriefModelBinding(ctx context.Context, brief *MobileBriefV1, binding *mobileBriefModelBinding) {
	if a == nil || a.IsClosedEyesModeEnabled() || ctx.Err() != nil || binding == nil || len(brief.Narrative) == 0 {
		brief.modelBinding = nil
		return
	}
	a.finishMobileBriefModelBindingFrom(ctx, brief, binding, a.collectTodayChatSources(ctx, time.Now()))
}

func (a *App) finishMobileBriefModelBindingFrom(ctx context.Context, brief *MobileBriefV1, binding *mobileBriefModelBinding, sources todayChatSources) {
	brief.modelBinding = nil
	if len(brief.Narrative) == 0 || !a.validateMobileBriefModelSourcesFrom(ctx, *brief, binding, sources) {
		return
	}
	binding.Synthesis = mobileBriefSynthesisDigest(*brief)
	if binding.Synthesis != "" {
		brief.modelBinding = binding
	}
}

func (a *App) mobileBriefModelSourceDigest(ctx context.Context, ref MobileBriefRef, brief MobileBriefV1, sources todayChatSources) (string, bool) {
	return a.mobileBriefModelSourceDigestWithNoticed(ctx, ref, brief, sources, newMobileBriefNoticedLookup(a))
}

func (a *App) mobileBriefModelSourceDigestWithNoticed(ctx context.Context, ref MobileBriefRef, brief MobileBriefV1, sources todayChatSources, noticed *mobileBriefNoticedLookup) (string, bool) {
	if ctx.Err() != nil || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) {
		return "", false
	}
	switch ref.SourceKind {
	case "brief", todayChatBriefCardKind, "brief_item":
		return "", false
	case mobileBriefNoticedSource:
		item, ok := noticed.suggestion(ref.SourceID)
		if !ok {
			return "", false
		}
		return mobileBriefModelDigest(item), true
	case "purpose":
		for _, purpose := range sources.Purposes {
			if purpose.OpenLoop.ID == ref.SourceID {
				return mobileBriefModelDigest(purpose), true
			}
		}
		return "", false
	}
	// Purpose decorations are authorized by their current canonical Purpose
	// read, which reconstructs and checks the entire provenance closure.
	if todayChatPurposeDecoration(ref.SourceKind) {
		var parents []string
		refID := ""
		for _, current := range brief.Evidence {
			if current.SourceKind == ref.SourceKind && current.SourceID == ref.SourceID {
				refID = current.Ref
				break
			}
		}
		for _, section := range mobileBriefSectionLines(brief.Sections) {
			for _, item := range section.items {
				if len(item.Refs) == 0 {
					continue
				}
				uses := false
				for _, id := range item.Refs {
					uses = uses || id == refID
				}
				if !uses {
					continue
				}
				for _, primary := range brief.Evidence {
					if primary.Ref == item.Refs[0] && primary.SourceKind == "purpose" {
						digest, ok := a.mobileBriefModelSourceDigestWithNoticed(ctx, primary, brief, sources, noticed)
						if !ok {
							return "", false
						}
						parents = append(parents, digest)
					}
				}
			}
		}
		if len(parents) > 0 {
			return mobileBriefModelDigest(uniqueSortedStrings(parents)), true
		}
	}
	if ref.SourceKind == mobileBriefAttendanceSource {
		return a.mobileBriefAttendanceModelDigest(ctx, ref.SourceID, brief)
	}
	resolved, err := a.resolveTodayChatItem(ctx, TodayChatContextRef{SourceKind: ref.SourceKind, SourceID: ref.SourceID}, sources, mobileBriefAsOfTime(brief))
	if err == nil {
		if ref.SourceKind == mobileBriefRecapSource && strings.HasPrefix(ref.SourceID, threadOutcomeMomentPrefix) {
			id := strings.TrimPrefix(ref.SourceID[:strings.LastIndex(ref.SourceID, ":")], threadOutcomeMomentPrefix)
			families, err := loadMobileBriefThreadFamilies(ctx, a.activity.store, time.Now().UnixMilli())
			if err != nil {
				return "", false
			}
			for _, family := range families {
				contains := false
				for _, row := range family {
					contains = contains || row.id == id
				}
				if !contains {
					continue
				}
				var metadata [][]any
				for _, row := range family {
					metadata = append(metadata, []any{row.id, row.parent, row.start, row.end, row.kind, row.state, row.acted, row.version})
				}
				return mobileBriefModelDigest(struct {
					Record todayChatRecord
					Family [][]any
				}{resolved.Record, metadata}), true
			}
			return "", false
		}
		return mobileBriefModelDigest(resolved.Record), true
	}
	// This path enforces Calendar consent (UI and OS) and current source gates.
	// Freeze the time window, not the underlying rows or permissions.
	candidate, _, ok := a.currentTodayBriefCandidateWithPrefill(ctx, ref, brief, mobileBriefAsOfTime(brief))
	if !ok || ctx.Err() != nil {
		return "", false
	}
	return mobileBriefModelDigest(candidate), true
}

// Read-only attendance reconstruction: the collector's daily UI cache must
// never be written while the Today sender holds its admission revision.
func (a *App) mobileBriefAttendanceModelDigest(ctx context.Context, id string, brief MobileBriefV1) (string, bool) {
	calendarRef := MobileBriefRef{SourceKind: mobileBriefCalendarSource, SourceID: id}
	candidate, _, ok := a.currentTodayBriefCandidateWithPrefill(ctx, calendarRef, brief, mobileBriefAsOfTime(brief))
	if !ok || a.store == nil || a.activity == nil || a.activity.store == nil {
		return "", false
	}
	loc := mobileBriefNarrativeLocation(brief)
	asOf := mobileBriefAsOfTime(brief)
	day := asOf.In(loc)
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	events, err := a.listCalendarEvents(ctx, start, start.AddDate(0, 0, 1))
	if err != nil {
		return "", false
	}
	pauses, err := a.observationPauses()
	if err != nil || pausedAt(pauses, asOf) {
		return "", false
	}
	activity, gaps, err := loadMobileBriefAttendanceActivity(a.activity.store, start, asOf)
	if err != nil {
		return "", false
	}
	activity, gaps = filterMobileBriefAttendanceActivity(a, activity, gaps, asOf)
	for _, observation := range inferMobileBriefAttendance(events, activity, gaps, pauses, asOf, true) {
		if observation.EventID == id {
			return mobileBriefModelDigest(struct {
				Event       mobileBriefCandidate
				Observation mobileBriefAttendanceObservation
			}{candidate, observation}), ctx.Err() == nil
		}
	}
	return "", false
}
