package main

import (
	"strconv"
	"strings"
)

// Once a brief is built, the latest brief shows, with its words and its
// recording, on the computer and the phone (Bo, 2026-11-15: "1. show up
// latest 2. quality. we can give up privacy and citation and others to make
// sure we reach those 2 goals"). The ranked goals, highest first:
//
//  1. The newest built brief shows: its words and its recording.
//  2. Quality: the words and recording the build made are what show; cards may
//     refresh around them, and a citation that no longer resolves loses its
//     link, nothing more.
//  3. Only the person's own explicit actions (forget, withdraw) change the
//     words after the build, and they leave out the paragraphs that cite what
//     was withdrawn. They never blank the brief.
//
// A lower goal never blocks a higher one. mobileBriefShowBuilt is where that
// is decided, once, at the read every surface shares (finishedReadWithholding:
// Today, the menu bar and the phone package). Whatever the read-time gates,
// the live Today projection, the seal or a store error left of the words, the
// build's own copy (.built) is shown when the result has none. On 2026-11-15
// the morning brief lost its words and recording 26 seconds after it was
// built, on both devices, because a live refresh treated three calendar
// citations it could not fit as dropped dependencies.

// mobileBriefShowBuilt returns the brief to show for source: result when it
// has written words, otherwise result (or source, when the read produced
// nothing) with the build's words and recording. It reports whether it
// changed anything.
func (s *mobileBriefService) mobileBriefShowBuilt(source, result MobileBriefV1) (MobileBriefV1, bool) {
	if s == nil || s.store == nil || source.BriefID == "" {
		return result, false
	}
	built, ok := s.store.loadBuilt(source.BriefID)
	if !ok || built.AsOf != source.AsOf || built.NarrativeStatus != "ok" || len(built.Narrative) == 0 {
		return result, false
	}
	// A read that produced nothing (an unreadable sealed copy, a store still
	// opening) shows the stored brief, with the build's words when it has none.
	base, changed := result, false
	if base.BriefID != source.BriefID {
		base, changed = source, true
	}
	if !mobileBriefShowsWrittenWords(base) {
		base = mobileBriefWithBuiltWords(base, built, mobileBriefAllWithdrawnKeys())
		changed = true
	}
	// The recording comes back with the words it speaks: when the words
	// shown are the build's own. A paragraph the person withdrew is never
	// spoken (TestMobileBriefAudioNeverSpeaksTextTheBriefNoLongerShows).
	if base.Audio == nil && built.Audio != nil && mobileBriefSameNarrativeText(base, built) {
		audio := *built.Audio
		base.Audio = &audio
		changed = true
	}
	return base, changed
}

// mobileBriefShowsWrittenWords reports whether brief shows the words a model
// wrote (or a quiet day's).
func mobileBriefShowsWrittenWords(brief MobileBriefV1) bool {
	return len(brief.Narrative) > 0 && (brief.NarrativeStatus == "ok" || brief.NarrativeStatus == "quiet")
}

// mobileBriefWithBuiltWords puts the build's words into brief. A paragraph that
// cites a source the person explicitly withdrew is left out; a correction
// (a withdrawal with replacement wording) is not a removal. Citations are tied
// to brief's evidence by source, and a cited source brief no longer lists is
// added under a new ref, so every kept citation still links.
func mobileBriefWithBuiltWords(brief, built MobileBriefV1, withdrawn []briefWithdrawn) MobileBriefV1 {
	builtByRef := make(map[string]MobileBriefRef, len(built.Evidence))
	for _, ref := range built.Evidence {
		builtByRef[ref.Ref] = ref
	}
	removed := func(ref MobileBriefRef) bool {
		for _, w := range withdrawn {
			if w.Text == "" && briefRefWithdrawn(ref.SourceID, []briefWithdrawn{w}) {
				return true
			}
		}
		return false
	}
	evidence := append([]MobileBriefRef(nil), brief.Evidence...)
	byKey := make(map[string]string, len(evidence))
	next := 0
	for _, ref := range evidence {
		byKey[mobileBriefSourceKey(ref.SourceKind, ref.SourceID)] = ref.Ref
		if n, err := strconv.Atoi(strings.TrimPrefix(ref.Ref, "e")); err == nil && n > next {
			next = n
		}
	}
	link := func(ref MobileBriefRef) string {
		key := mobileBriefSourceKey(ref.SourceKind, ref.SourceID)
		if id, ok := byKey[key]; ok {
			return id
		}
		next++
		ref.Ref = "e" + strconv.Itoa(next)
		evidence = append(evidence, ref)
		byKey[key] = ref.Ref
		return ref.Ref
	}
	narrative := make([]MobileBriefPara, 0, len(built.Narrative))
	dropped := false
	for _, para := range built.Narrative {
		cited := make([]MobileBriefRef, 0, len(para.Refs))
		withdrawnHere := false
		for _, id := range para.Refs {
			ref, ok := builtByRef[id]
			if !ok {
				continue // a citation that does not resolve loses its link
			}
			if removed(ref) {
				withdrawnHere = true
				break
			}
			cited = append(cited, ref)
		}
		if withdrawnHere {
			dropped = true
			continue
		}
		refs := make([]string, 0, len(cited))
		for _, ref := range cited {
			refs = append(refs, link(ref))
		}
		para.Refs = refs
		narrative = append(narrative, para)
	}
	brief.Narrative, brief.NarrativeStatus = narrative, built.NarrativeStatus
	if dropped {
		// The parts map onto the paragraphs as written; with some left out
		// the paragraphs stand on their own.
		brief.NarrativeParts = nil
	} else {
		brief.NarrativeParts = built.NarrativeParts
	}
	brief.Evidence = evidence
	brief.modelBinding, brief.ownVoiceFacts = built.modelBinding, built.ownVoiceFacts
	return brief
}

// mobileBriefSameNarrativeText reports whether a and b say the same words.
func mobileBriefSameNarrativeText(a, b MobileBriefV1) bool {
	if len(a.Narrative) == 0 || len(a.Narrative) != len(b.Narrative) {
		return false
	}
	for i := range a.Narrative {
		if a.Narrative[i].Text != b.Narrative[i].Text {
			return false
		}
	}
	return true
}

// mobileBriefStoredAudio is the recording of the stored brief, or of its build
// when a later pass left the stored copy without one.
func (s *mobileBriefStore) mobileBriefStoredAudio(brief MobileBriefV1) *MobileBriefAudio {
	if brief.Audio != nil {
		return brief.Audio
	}
	built, ok := s.loadBuilt(brief.BriefID)
	if !ok || built.AsOf != brief.AsOf {
		return nil
	}
	return built.Audio
}
