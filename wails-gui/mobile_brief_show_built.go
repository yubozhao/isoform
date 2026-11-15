package main

import (
	"sort"
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
	withdrawn := mobileBriefAllWithdrawnKeys()
	kept, keptChanged := mobileBriefKeepBuilt(base, built, withdrawn)
	stored, _ := mobileBriefKeepBuilt(source, built, withdrawn)
	kept, played := mobileBriefBuiltRecordingFor(kept, stored, built, withdrawn)
	return kept, changed || keptChanged || played
}

// mobileBriefBuiltRecordingFor gives shown the build's recording when shown
// says the build's words as the read renders them. The sealed copy renders
// times and days for the moment it was sealed, so its text can differ from
// .built while saying the same thing (2026-11-14 morning: three of four
// paragraphs rendered differently, recording dropped). That holds when the
// stored words (what every rendering starts from) are still the build's,
// shown has every paragraph with words in it (a stated exclusion blanks one),
// and no explicit withdrawal touches what the build cites. A stored rewrite
// or a removal never plays the old recording
// (TestMobileBriefAudioNeverSpeaksTextTheBriefNoLongerShows).
func mobileBriefBuiltRecordingFor(shown, stored, built MobileBriefV1, withdrawn []briefWithdrawn) (MobileBriefV1, bool) {
	if built.Audio == nil || built.Audio.Status != mobileBriefAudioStatusOK || built.Audio.Bytes == 0 || built.BriefID != shown.BriefID || built.AsOf != shown.AsOf {
		return shown, false
	}
	if shown.Audio != nil && shown.Audio.Status == mobileBriefAudioStatusOK {
		return shown, false
	}
	if !mobileBriefShowsWrittenWords(shown) || len(shown.Narrative) != len(built.Narrative) || !mobileBriefSameNarrativeText(stored, built) {
		return shown, false
	}
	for _, para := range shown.Narrative {
		if strings.TrimSpace(para.Text) == "" {
			return shown, false
		}
	}
	if _, touched := mobileBriefDropWithdrawnParagraphs(built, withdrawn); touched {
		return shown, false
	}
	audio := *built.Audio
	shown.Audio = &audio
	return shown, true
}

// mobileBriefKeepBuilt is the one rule every save, seal and read of a built
// brief follows: doc keeps at least the build's words and its recording.
// Only the person's explicit keyed withdrawals (forget, withdraw, a stated
// exclusion) leave out the paragraphs that cite what they name. A gate, a
// live refresh or a citation limit never removes a paragraph the build
// wrote: a paragraph it dropped comes back from the build, and a paragraph
// it kept (corrected wording included) stays as it is. The recording comes
// back with the words it speaks, when those are the build's own words. It
// reports whether it changed doc.
func mobileBriefKeepBuilt(doc, built MobileBriefV1, withdrawn []briefWithdrawn) (MobileBriefV1, bool) {
	if doc.BriefID == "" || built.BriefID != doc.BriefID || built.AsOf != doc.AsOf || built.NarrativeStatus != "ok" || len(built.Narrative) == 0 {
		return doc, false
	}
	changed := false
	// What the person withdrew leaves every copy, not only the build's words.
	if amended, dropped := mobileBriefDropWithdrawnParagraphs(doc, withdrawn); dropped {
		doc, changed = amended, true
	}
	want := mobileBriefWithBuiltWords(doc, built, withdrawn)
	if merged, missing := mobileBriefMergeBuiltWords(doc, want); missing {
		doc, changed = merged, true
	}
	// A paragraph the person withdrew is never spoken
	// (TestMobileBriefAudioNeverSpeaksTextTheBriefNoLongerShows).
	if built.Audio != nil && built.Audio.Status == mobileBriefAudioStatusOK && (doc.Audio == nil || doc.Audio.Status != mobileBriefAudioStatusOK) && mobileBriefSameNarrativeText(doc, built) {
		audio := *built.Audio
		doc.Audio = &audio
		changed = true
	}
	return doc, changed
}

// mobileBriefDropWithdrawnParagraphs leaves out of doc each paragraph that
// cites a source the person explicitly withdrew (a correction, which leaves
// wording in its place, is not a removal).
func mobileBriefDropWithdrawnParagraphs(doc MobileBriefV1, withdrawn []briefWithdrawn) (MobileBriefV1, bool) {
	removed := map[string]bool{}
	for _, ref := range doc.Evidence {
		for _, w := range withdrawn {
			if w.Text == "" && briefRefWithdrawn(ref.SourceID, []briefWithdrawn{w}) {
				removed[ref.Ref] = true
			}
		}
	}
	if len(removed) == 0 {
		return doc, false
	}
	kept := make([]MobileBriefPara, 0, len(doc.Narrative))
	for _, para := range doc.Narrative {
		cites := false
		for _, ref := range para.Refs {
			cites = cites || removed[ref]
		}
		if !cites {
			kept = append(kept, para)
		}
	}
	if len(kept) == len(doc.Narrative) {
		return doc, false
	}
	doc.Narrative, doc.NarrativeParts = kept, nil
	return doc, true
}

// mobileBriefMergeBuiltWords puts back the paragraphs of want (the build's
// words less what the person withdrew) that doc no longer has. A paragraph of
// doc with the same words, or citing the same sources (a correction), stands
// for its built paragraph. It reports whether any paragraph was missing.
func mobileBriefMergeBuiltWords(doc, want MobileBriefV1) (MobileBriefV1, bool) {
	if !mobileBriefShowsWrittenWords(doc) {
		return want, len(want.Narrative) > 0
	}
	// want's evidence starts with doc's, so both narratives cite into it.
	byRef := make(map[string]string, len(want.Evidence))
	for _, ref := range want.Evidence {
		byRef[ref.Ref] = mobileBriefSourceKey(ref.SourceKind, ref.SourceID)
	}
	sources := func(para MobileBriefPara) string {
		keys := make([]string, 0, len(para.Refs))
		for _, id := range para.Refs {
			if key, ok := byRef[id]; ok {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		return strings.Join(keys, "\x01")
	}
	used := make([]bool, len(doc.Narrative))
	match := func(para MobileBriefPara) int {
		for i, have := range doc.Narrative {
			if !used[i] && have.Text == para.Text {
				return i
			}
		}
		if len(para.Refs) == 0 {
			return -1
		}
		key := sources(para)
		for i, have := range doc.Narrative {
			if !used[i] && len(have.Refs) > 0 && sources(have) == key {
				return i
			}
		}
		return -1
	}
	merged := make([]MobileBriefPara, 0, len(want.Narrative))
	missing := false
	for _, para := range want.Narrative {
		if i := match(para); i >= 0 {
			used[i] = true
			merged = append(merged, doc.Narrative[i])
			continue
		}
		missing = true
		merged = append(merged, para)
	}
	if !missing {
		return doc, false
	}
	// A paragraph the build did not write (none today) is kept at the end.
	for i, have := range doc.Narrative {
		if !used[i] {
			merged = append(merged, have)
		}
	}
	out := want
	out.Narrative = merged
	if len(merged) != len(want.Narrative) {
		out.NarrativeParts = nil
	}
	return out, true
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
