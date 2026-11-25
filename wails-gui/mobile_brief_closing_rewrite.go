package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// mobileBriefWithoutStaleAudio drops a recording made before recordings were
// bound to their text when a read changes what the brief says. A bound one
// stays for mobileBriefAudioForRead, which plays it only while it says
// exactly what the brief now reads, so audio made again plays.
func mobileBriefWithoutStaleAudio(audio *MobileBriefAudio) *MobileBriefAudio {
	if audio == nil || audio.binding == "" {
		return nil
	}
	return audio
}

// mobileBriefRewrittenClosing rewrites a closing question that an exclusion
// cut (today-review v1.1.8 #1, v1.1.10 C): it asks again with the offers
// still allowed, in Isoform's own words. With none left there is no question.
func mobileBriefRewrittenClosing(brief MobileBriefV1, before MobileBriefPara, language string) MobileBriefV1 {
	n := len(brief.Narrative)
	if n == 0 || brief.Narrative[n-1].Text != "" || before.Text == "" {
		return brief
	}
	kept := map[string]bool{}
	for _, ref := range brief.Evidence {
		if slices.Contains(before.Refs, ref.Ref) {
			kept[ref.Ref] = true
		}
	}
	var allowed []mobileBriefOfferDraft
	for _, draft := range mobileBriefOfferCandidates(brief, language) {
		if len(draft.offer.Refs) > 0 && kept[draft.offer.Refs[0]] {
			allowed = append(allowed, draft)
		}
	}
	para, offers := mobileBriefClosingFrom(brief, mobileBriefPickDrafts(allowed), language)
	if para.Text == "" {
		return brief
	}
	brief.Narrative = append([]MobileBriefPara(nil), brief.Narrative...)
	brief.Narrative[n-1] = para
	brief.Offers = offers
	brief.Audio = mobileBriefWithoutStaleAudio(brief.Audio)
	return brief
}

// mobileBriefClosingChips are Isoform's answers to a closing question that
// has none: one chip for the one offer it asks about, in two to four words,
// then "Not now" (today-review v1.1.7, v1.1.8 #3, v1.1.17 D18). Without a bound question or an
// offer it asks about there are none, and Today then shows no question.
func mobileBriefClosingChips(brief MobileBriefV1, language string) []MobileBriefCard {
	if !featureOn(featureBriefExtras) {
		return nil
	}
	closing := mobileBriefClosingParagraph(brief)
	digest := mobileBriefClosingDigest(brief)
	if closing.Text == "" || closing.Hold != nil || digest == "" {
		return nil
	}
	lang := mobileBriefVoiceLang(language)
	refs := uniqueSortedStrings(closing.Refs)
	chip := func(id, label, message string) MobileBriefCard {
		return MobileBriefCard{ID: id, Kind: mobileBriefCardAction, Mode: mobileBriefCardSend, Label: label, Message: message,
			Refs: append([]string(nil), refs...), AnswerTo: digest, Facts: digest, Writer: mobileBriefCardWriterClosing}
	}
	// Each chip reads its offer as written for its subject now, as the
	// brief's buttons do (mobileBriefPatternCards).
	current := map[string]MobileBriefOffer{}
	for _, draft := range mobileBriefOfferCandidates(brief, language) {
		if offer := draft.offer; len(offer.Refs) > 0 && current[offer.Refs[0]].Label == "" {
			current[offer.Refs[0]] = offer
		}
	}
	var chips []MobileBriefCard
	labels := map[string]bool{}
	for _, stored := range brief.Offers {
		if len(stored.Refs) == 0 {
			continue
		}
		offer := current[stored.Refs[0]]
		offer.ID = stored.ID
		prompt := strings.TrimSpace(offer.Prompt)
		if len(chips) == 1 || len(offer.Refs) == 0 || !slices.Contains(refs, offer.Refs[0]) || prompt == "" ||
			offer.Action == mobileBriefCardOpenActivity || len(prompt) > mobileBriefCardMessageBytes {
			continue
		}
		label := mobileBriefClosingChipLabel(offer.Kind, lang)
		if labels[label] {
			// Two offers of one kind: the second names its subject.
			if label = mobileBriefActionLabel(offer.Label); label == "" || labels[label] {
				continue
			}
		}
		labels[label] = true
		chips = append(chips, chip("closing-"+offer.ID, label, prompt))
	}
	if len(chips) == 0 {
		return nil
	}
	decline := pick(lang, "Not now", "暂不")
	return append(chips, chip("closing-not-now", decline, decline))
}

func mobileBriefClosingChipLabel(kind, lang string) string {
	switch kind {
	case "prepare":
		return pick(lang, "Prepare for it", "帮我准备")
	case "reply":
		return pick(lang, "Draft my reply", "起草回复")
	case "follow_up":
		return pick(lang, "Draft the follow-up", "起草跟进")
	case "review":
		return pick(lang, "Walk me through it", "带我看看")
	case "order":
		return pick(lang, "Finish the order", "完成订单")
	case "note_decision":
		return pick(lang, "Note the decision", "记下决定")
	default:
		return pick(lang, "Plan next steps", "规划下一步")
	}
}

// mobileBriefLegacyRemakeMarker records that the one remake of a brief
// written before paragraphs carried item refs has been checked for.
const mobileBriefLegacyRemakeMarker = "legacy-exclusion-remake-v1"

// remakeLegacyBriefOnce runs at the first launch after paragraphs carried item
// refs (today-review v1.1.11 #2). When the brief Today shows has a paragraph
// without refs and the person has stated an exclusion, a text match can miss
// a reworded line, so that brief is made again once, from its items, with
// audio. Older briefs keep text matching.
func (s *mobileBriefService) remakeLegacyBriefOnce(ctx context.Context) {
	if s.app == nil || s.store.dir == "" || ctx.Err() != nil {
		return
	}
	marker := filepath.Join(s.store.dir, mobileBriefLegacyRemakeMarker)
	if _, err := os.Stat(marker); err == nil {
		return
	}
	defer func() { _ = os.WriteFile(marker, []byte("checked\n"), 0600) }()
	terms := s.app.mobileBriefExclusions()
	if len(terms) == 0 {
		return
	}
	var shown MobileBriefV1
	for _, brief := range s.store.briefs() {
		if brief.NarrativeStatus != "quiet" {
			shown = brief
			break
		}
	}
	legacy := false
	for _, para := range shown.Narrative {
		legacy = legacy || len(para.Refs) == 0
	}
	if shown.BriefID == "" || shown.NarrativeStatus != "ok" || !legacy {
		return
	}
	remade := mobileBriefHygiene(mobileBriefBlankExcluded(shown, terms))
	// Isoform's template reads the brief again from its items, each with its
	// refs, so nothing excluded can come back reworded.
	remade.Narrative, remade.WeeklyRecap, remade.NarrativeStatus = nil, nil, "unavailable"
	remade.Offers, remade.Cards, remade.Audio = nil, nil, nil
	asOf := mobileBriefAsOfTime(shown)
	if asOf.IsZero() {
		asOf = s.now()
	}
	s.patchMu.Lock()
	err := s.store.save(remade, asOf)
	s.patchMu.Unlock()
	if err != nil {
		log.Printf("[mobile-brief] the %s brief could not be made again without its exclusions", shown.Slot)
		return
	}
	s.app.emitBrowserEvent("brief:stored", remade.BriefID)
	s.addAudioWithLatestGuard(ctx, s.app.mobileBriefForDelivery(remade), asOf, false)
}
